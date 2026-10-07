package stream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/omkarabagade/rtsp-stream-viewer/backend/internal/fmp4"
)

// State is the lifecycle state of an upstream stream, reported to viewers.
type State string

const (
	StateConnecting   State = "connecting"
	StateLive         State = "live"
	StateReconnecting State = "reconnecting"
	StateError        State = "error"
)

// Message is one WebSocket frame destined for a viewer.
type Message struct {
	Binary bool
	Data   []byte
}

// statusMsg is sent to viewers whenever the upstream state changes.
type statusMsg struct {
	Type    string `json:"type"` // "status"
	State   State  `json:"state"`
	Message string `json:"message,omitempty"`
	RetryIn int64  `json:"retryInMs,omitempty"`
}

// initMsg precedes the binary init segment and tells the browser which codec
// to configure MediaSource with.
type initMsg struct {
	Type       string `json:"type"` // "init"
	Codec      string `json:"codec"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Transcoded bool   `json:"transcoded"`
}

func jsonMsg(v any) Message {
	b, _ := json.Marshal(v)
	return Message{Data: b}
}

// Subscriber is a single viewer. The hub never blocks on a subscriber: if its
// buffer is full the subscriber skips media until the next keyframe, so one
// slow client can't stall the others (or the FFmpeg pipe).
type Subscriber struct {
	ch       chan Message
	dropping bool // guarded by Hub.mu
	closed   bool // guarded by Hub.mu
}

// C returns the channel of outgoing messages. It is closed when the hub
// shuts down or evicts the subscriber.
func (s *Subscriber) C() <-chan Message { return s.ch }

const subscriberBuffer = 128

// maxGOPCacheBytes bounds the per-stream keyframe cache used for instant
// start-up of late joiners (protects memory with very long GOPs).
const maxGOPCacheBytes = 8 << 20

// HubOptions configure a Hub.
type HubOptions struct {
	FFmpegPath         string
	Transcode          string
	TranscodeMaxHeight int
	ConnectTimeout     time.Duration
	StallTimeout       time.Duration
	IdleTimeout        time.Duration
	MaxViewers         int
}

// Hub fans out a single FFmpeg pipeline to any number of viewers.
// One RTSP connection is opened per unique URL regardless of viewer count.
type Hub struct {
	ID       string
	url      string
	redacted string
	opts     HubOptions
	log      *slog.Logger
	onStop   func(*Hub)

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	subs      map[*Subscriber]struct{}
	init      []Message // [initMsg json, init segment binary]; nil until known
	codec     fmp4.CodecInfo
	transcode bool
	gop       [][]byte // segments since (and including) the last keyframe
	gopBytes  int
	status    statusMsg
	stopped   bool
	idle      *time.Timer
	startedAt time.Time
	restarts  int

	bytesIn  atomic.Int64
	segments atomic.Int64
}

var (
	errHubStopped = errors.New("stream is shutting down")
	errTooManyViewers = errors.New("too many viewers for this stream")
)

func newHub(parent context.Context, id, url, redacted string, opts HubOptions, log *slog.Logger, onStop func(*Hub)) *Hub {
	ctx, cancel := context.WithCancel(parent)
	h := &Hub{
		ID: id, url: url, redacted: redacted, opts: opts,
		log:    log.With("stream", id, "url", redacted),
		onStop: onStop, ctx: ctx, cancel: cancel,
		subs:      make(map[*Subscriber]struct{}),
		status:    statusMsg{Type: "status", State: StateConnecting, Message: "Connecting to camera…"},
		startedAt: time.Now(),
	}
	go h.run()
	return h
}

// Subscribe registers a viewer and primes it with the current status, the
// init segment and the cached GOP so playback starts immediately.
func (h *Hub) Subscribe() (*Subscriber, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopped {
		return nil, errHubStopped
	}
	if h.opts.MaxViewers > 0 && len(h.subs) >= h.opts.MaxViewers {
		return nil, errTooManyViewers
	}
	if h.idle != nil {
		h.idle.Stop()
		h.idle = nil
	}
	s := &Subscriber{ch: make(chan Message, subscriberBuffer+len(h.gop)+3)}
	s.ch <- jsonMsg(h.status)
	if h.init != nil {
		for _, m := range h.init {
			s.ch <- m
		}
		for _, seg := range h.gop {
			s.ch <- Message{Binary: true, Data: seg}
		}
	}
	h.subs[s] = struct{}{}
	return s, nil
}

// Unsubscribe removes a viewer. When the last viewer leaves, FFmpeg is kept
// alive for IdleTimeout so that page reloads / pause-play are instant.
func (h *Hub) Unsubscribe(s *Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[s]; !ok {
		return
	}
	delete(h.subs, s)
	h.closeSub(s)
	if len(h.subs) == 0 && !h.stopped && h.idle == nil {
		h.idle = time.AfterFunc(h.opts.IdleTimeout, h.stopIfIdle)
	}
}

func (h *Hub) stopIfIdle() {
	h.mu.Lock()
	if len(h.subs) > 0 || h.stopped {
		h.mu.Unlock()
		return
	}
	h.stopped = true
	h.mu.Unlock()
	h.log.Info("no viewers, stopping stream")
	h.cancel()
}

// Stop terminates the pipeline and disconnects all viewers.
func (h *Hub) Stop() {
	h.mu.Lock()
	h.stopped = true
	h.mu.Unlock()
	h.cancel()
}

func (h *Hub) closeSub(s *Subscriber) {
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
}

// broadcastLocked delivers m to every subscriber without blocking.
// Control messages (status/init) that don't fit evict the subscriber, since
// it would otherwise be stuck with an unusable decoder state.
func (h *Hub) broadcastLocked(m Message, keyframe bool) {
	for s := range h.subs {
		if m.Binary && s.dropping {
			if !keyframe {
				continue
			}
			s.dropping = false
		}
		select {
		case s.ch <- m:
		default:
			if m.Binary {
				s.dropping = true
				continue
			}
			delete(h.subs, s)
			h.closeSub(s)
		}
	}
}

func (h *Hub) setStatus(state State, msg string, retryIn time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.status.State == state && h.status.Message == msg && retryIn == 0 {
		return
	}
	h.status = statusMsg{Type: "status", State: state, Message: msg, RetryIn: retryIn.Milliseconds()}
	h.broadcastLocked(jsonMsg(h.status), false)
}

func (h *Hub) setInit(info fmp4.CodecInfo, transcoded bool, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.codec, h.transcode = info, transcoded
	h.init = []Message{
		jsonMsg(initMsg{Type: "init", Codec: info.Codec, Width: info.Width, Height: info.Height, Transcoded: transcoded}),
		{Binary: true, Data: data},
	}
	h.gop, h.gopBytes = nil, 0
	for s := range h.subs {
		s.dropping = false // new timeline: everyone starts fresh
	}
	for _, m := range h.init {
		h.broadcastLocked(m, false)
	}
}

func (h *Hub) pushSegment(seg []byte, keyframe bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if keyframe || h.gopBytes+len(seg) > maxGOPCacheBytes {
		h.gop, h.gopBytes = h.gop[:0:0], 0
	}
	if keyframe || len(h.gop) > 0 {
		h.gop = append(h.gop, seg)
		h.gopBytes += len(seg)
	}
	h.broadcastLocked(Message{Binary: true, Data: seg}, keyframe)
}

// run supervises FFmpeg: it restarts the pipeline with exponential backoff
// until the hub is stopped.
func (h *Hub) run() {
	defer func() {
		h.mu.Lock()
		h.stopped = true
		for s := range h.subs {
			delete(h.subs, s)
			h.closeSub(s)
		}
		if h.idle != nil {
			h.idle.Stop()
		}
		h.mu.Unlock()
		if h.onStop != nil {
			h.onStop(h)
		}
		h.log.Info("stream stopped")
	}()

	transcode := h.opts.Transcode == "always"
	const minBackoff, maxBackoff = time.Second, 30 * time.Second
	backoff := minBackoff
	failures := 0

	switchedToTranscode := false
	for attempt := 0; ; attempt++ {
		if h.ctx.Err() != nil {
			return
		}
		switch {
		case attempt == 0:
			h.setStatus(StateConnecting, "Connecting to camera…", 0)
		case switchedToTranscode:
			switchedToTranscode = false
			h.setStatus(StateConnecting, "Converting "+h.codecName()+" to H.264…", 0)
		default:
			h.setStatus(StateReconnecting, "Reconnecting…", 0)
			h.mu.Lock()
			h.restarts++
			h.mu.Unlock()
		}

		started := time.Now()
		gotData, err := h.runOnce(transcode)
		if h.ctx.Err() != nil {
			return
		}

		if errors.Is(err, errNeedsTranscode) {
			h.log.Info("non-H.264 source, switching to transcoding", "codec", h.codecName())
			transcode, switchedToTranscode = true, true
			continue
		}
		var unsupported errUnsupportedCodec
		if errors.As(err, &unsupported) {
			h.setStatus(StateError, unsupported.Error(), 0)
			<-h.ctx.Done()
			return
		}

		// A stream that played and then dropped is a valid source that hiccuped:
		// reconnect quickly instead of continuing the backoff ladder.
		if gotData && time.Since(started) > 5*time.Second {
			backoff, failures = minBackoff, 0
		}
		failures++
		msg := err.Error()
		state := StateReconnecting
		if failures >= 3 {
			state = StateError
		}
		h.log.Warn("ffmpeg exited", "err", msg, "retry_in", backoff, "failures", failures)
		h.setStatus(state, msg, backoff)

		select {
		case <-time.After(backoff):
		case <-h.ctx.Done():
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (h *Hub) codecName() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.codec.SampleEntry
}

// runOnce runs a single FFmpeg process until it exits or the hub stops.
// It returns whether any media was produced and a user-facing error.
func (h *Hub) runOnce(transcode bool) (gotData bool, err error) {
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()

	args := ffmpegArgs(h.url, transcode, h.opts.ConnectTimeout, h.opts.TranscodeMaxHeight)
	cmd := exec.CommandContext(ctx, h.opts.FFmpegPath, args...)
	cmd.WaitDelay = 3 * time.Second
	stderr := &stderrTail{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, errors.New("could not start FFmpeg: " + err.Error())
	}
	h.log.Debug("ffmpeg started", "pid", cmd.Process.Pid, "transcode", transcode)

	// Watchdog: FFmpeg's own -timeout covers socket stalls, but a source can
	// also keep the socket open while sending nothing useful.
	var lastData atomic.Int64
	lastData.Store(time.Now().UnixNano())
	stalled := atomic.Bool{}
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if time.Since(time.Unix(0, lastData.Load())) > h.opts.StallTimeout {
					stalled.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	readErr := h.consume(fmp4.NewReader(stdout), transcode, &lastData, &gotData)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		cancel() // e.g. needs-transcode / parse error: kill FFmpeg now
	}
	waitErr := cmd.Wait()

	switch {
	case errors.Is(readErr, errNeedsTranscode):
		return gotData, readErr
	case errors.As(readErr, new(errUnsupportedCodec)):
		return gotData, readErr
	case stalled.Load():
		return gotData, errors.New("No video received for " + h.opts.StallTimeout.String() + " — stream stalled")
	}
	raw := stderr.String()
	if raw != "" {
		h.log.Debug("ffmpeg stderr", "stderr", raw)
	}
	if waitErr == nil {
		// FFmpeg exited cleanly: the source closed the session (camera
		// rebooted, publisher stopped, …). Any stderr is decoder noise.
		return gotData, errors.New("Stream ended by the source")
	}
	return gotData, errors.New(friendlyError(raw, waitErr))
}

func (h *Hub) consume(r *fmp4.Reader, transcode bool, lastData *atomic.Int64, gotData *bool) error {
	var ftyp []byte
	var moof fmp4.Box
	haveInit := false
	for {
		box, err := r.Next()
		if err != nil {
			return err
		}
		lastData.Store(time.Now().UnixNano())
		h.bytesIn.Add(int64(len(box.Data)))

		switch box.Type {
		case "ftyp":
			ftyp = box.Data
		case "moov":
			info, err := fmp4.ParseInit(box)
			if err != nil {
				return err
			}
			h.mu.Lock()
			h.codec = info
			h.mu.Unlock()
			if !info.IsH264() {
				if transcode {
					return errors.New("transcoder produced " + info.SampleEntry)
				}
				if h.opts.Transcode == "never" {
					return errUnsupportedCodec{codec: info.SampleEntry}
				}
				return errNeedsTranscode
			}
			init := make([]byte, 0, len(ftyp)+len(box.Data))
			init = append(append(init, ftyp...), box.Data...)
			h.setInit(info, transcode, init)
			haveInit = true
		case "moof":
			moof = box
		case "mdat":
			if !haveInit || moof.Data == nil {
				continue
			}
			seg := make([]byte, 0, len(moof.Data)+len(box.Data))
			seg = append(append(seg, moof.Data...), box.Data...)
			key := fmp4.StartsWithKeyframe(moof)
			moof = fmp4.Box{}
			if !*gotData {
				*gotData = true
				h.setStatus(StateLive, "", 0)
				h.log.Info("stream live", "codec", h.codec.Codec, "w", h.codec.Width, "h", h.codec.Height, "transcode", transcode)
			}
			h.segments.Add(1)
			h.pushSegment(seg, key)
		}
	}
}

// Stats is a snapshot of a hub for the /api/streams endpoint.
type Stats struct {
	ID         string    `json:"id"`
	URL        string    `json:"url"` // credentials redacted
	State      State     `json:"state"`
	Message    string    `json:"message,omitempty"`
	Viewers    int       `json:"viewers"`
	Codec      string    `json:"codec,omitempty"`
	Width      int       `json:"width,omitempty"`
	Height     int       `json:"height,omitempty"`
	Transcoded bool      `json:"transcoded"`
	BytesIn    int64     `json:"bytesIn"`
	Segments   int64     `json:"segments"`
	Restarts   int       `json:"restarts"`
	StartedAt  time.Time `json:"startedAt"`
}

// Stats returns a snapshot of this hub.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Stats{
		ID: h.ID, URL: h.redacted, State: h.status.State, Message: h.status.Message,
		Viewers: len(h.subs), Codec: h.codec.Codec, Width: h.codec.Width, Height: h.codec.Height,
		Transcoded: h.transcode, BytesIn: h.bytesIn.Load(), Segments: h.segments.Load(),
		Restarts: h.restarts, StartedAt: h.startedAt,
	}
}
