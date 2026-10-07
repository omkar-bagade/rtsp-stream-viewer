package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/omkarabagade/rtsp-stream-viewer/backend/internal/stream"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 40 * time.Second
	pingInterval   = 15 * time.Second
	subscribeWait  = 10 * time.Second
	maxClientFrame = 8 << 10
)

// Wire protocol (all JSON frames carry a "type"):
//
//	client → server  {"type":"subscribe","url":"rtsp://…"}   (first frame, required)
//	server → client  {"type":"status","state":"connecting|live|reconnecting|error","message":"…","retryInMs":n}
//	server → client  {"type":"init","codec":"avc1.64001f","width":1280,"height":720,"transcoded":false}
//	server → client  <binary> init segment (ftyp+moov) – always right after "init"
//	server → client  <binary> media segment (moof+mdat), repeated
//	server → client  {"type":"error","message":"…"} then close (fatal, e.g. invalid URL)
//
// The URL is sent in a frame rather than the query string so camera
// credentials never end up in access logs.
type clientMsg struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	up := websocket.Upgrader{
		ReadBufferSize:    4 << 10,
		WriteBufferSize:   64 << 10,
		EnableCompression: false, // video is already compressed
		CheckOrigin:       func(r *http.Request) bool { return s.originAllowed(r.Header.Get("Origin")) },
	}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote an HTTP error
	}
	defer conn.Close()
	conn.SetReadLimit(maxClientFrame)

	// 1. Wait for the subscribe frame.
	_ = conn.SetReadDeadline(time.Now().Add(subscribeWait))
	var msg clientMsg
	if err := conn.ReadJSON(&msg); err != nil || msg.Type != "subscribe" {
		fatal(conn, "Expected a subscribe message")
		return
	}

	hub, sub, err := s.mgr.Subscribe(msg.URL)
	if err != nil {
		var ve stream.ValidationError
		if errors.As(err, &ve) {
			fatal(conn, ve.Msg)
		} else {
			fatal(conn, err.Error())
		}
		return
	}
	log := s.log.With("stream", hub.ID, "remote", r.RemoteAddr)
	log.Info("viewer joined")
	defer func() {
		hub.Unsubscribe(sub)
		log.Info("viewer left")
	}()

	// 2. Reader: we don't expect more client frames, but must read to process
	// control frames (pong/close) and to notice disconnects.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(pongWait))
		})
		for {
			if _, _, err := conn.NextReader(); err != nil {
				return
			}
		}
	}()

	// 3. Writer: the only goroutine that writes to conn.
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case m, ok := <-sub.C():
			if !ok {
				_ = conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseGoingAway, "stream closed"),
					time.Now().Add(time.Second))
				return
			}
			typ := websocket.TextMessage
			if m.Binary {
				typ = websocket.BinaryMessage
			}
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(typ, m.Data); err != nil {
				return
			}
		case <-ping.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

func fatal(conn *websocket.Conn, msg string) {
	b, _ := json.Marshal(map[string]string{"type": "error", "message": msg})
	_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
	_ = conn.WriteMessage(websocket.TextMessage, b)
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, truncate(msg, 120)),
		time.Now().Add(time.Second))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
