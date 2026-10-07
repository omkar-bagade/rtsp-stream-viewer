// Package server wires the HTTP API, WebSocket endpoint and static file
// serving around a stream.Manager.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/omkarabagade/rtsp-stream-viewer/backend/internal/config"
	"github.com/omkarabagade/rtsp-stream-viewer/backend/internal/stream"
)

// Server is the HTTP front of the application.
type Server struct {
	cfg     config.Config
	mgr     *stream.Manager
	log     *slog.Logger
	started time.Time
}

// New creates a Server.
func New(cfg config.Config, mgr *stream.Manager, log *slog.Logger) *Server {
	return &Server{cfg: cfg, mgr: mgr, log: log, started: time.Now()}
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/config", s.config)
	mux.HandleFunc("GET /api/streams", s.listStreams)
	mux.HandleFunc("POST /api/streams/validate", s.validate)
	mux.HandleFunc("GET /ws", s.websocket)
	if s.cfg.StaticDir != "" {
		mux.Handle("GET /", spaHandler(s.cfg.StaticDir))
	}
	return s.cors(recoverer(s.log, mux))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"uptimeSeconds": int(time.Since(s.started).Seconds()),
		"activeStreams": len(s.mgr.Stats()),
	})
}

func (s *Server) config(w http.ResponseWriter, _ *http.Request) {
	demos := s.cfg.DemoStreams
	if demos == nil {
		demos = []config.DemoStream{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"demoStreams":         demos,
		"maxStreams":          s.cfg.MaxStreams,
		"maxViewersPerStream": s.cfg.MaxViewersPerStream,
	})
}

func (s *Server) listStreams(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"streams": s.mgr.Stats()})
}

// validate lets the UI check a URL before adding it to the grid.
func (s *Server) validate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}
	norm, redacted, err := s.mgr.Validate(body.URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": stream.StreamID(norm), "url": redacted})
}

func (s *Server) originAllowed(origin string) bool {
	if origin == "" {
		return true // non-browser clients
	}
	for _, o := range s.cfg.AllowedOrigins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
		// Allow wildcard sub-domains, e.g. https://*.vercel.app
		if pre, suf, ok := strings.Cut(o, "*"); ok && strings.HasPrefix(origin, pre) && strings.HasSuffix(origin, suf) {
			return true
		}
	}
	return false
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Error("panic in handler", "err", v, "path", r.URL.Path)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// spaHandler serves a built single-page app, falling back to index.html for
// client-side routes.
func spaHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fs.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
