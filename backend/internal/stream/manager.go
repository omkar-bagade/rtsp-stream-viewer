package stream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// ValidationError is returned for bad user input (HTTP 400 / WS error).
type ValidationError struct{ Msg string }

func (e ValidationError) Error() string { return e.Msg }

// ErrCapacity is returned when MaxStreams upstream pulls are already running.
var ErrCapacity = errors.New("server is at capacity: too many concurrent streams")

// Manager owns all hubs, keyed by a hash of the normalised source URL, so
// any number of browser tabs watching the same camera share one RTSP session.
type Manager struct {
	ctx          context.Context
	log          *slog.Logger
	opts         HubOptions
	maxStreams   int
	allowedHosts []string

	mu   sync.Mutex
	hubs map[string]*Hub
}

// NewManager creates a Manager. Cancel ctx (or call Shutdown) to stop all hubs.
func NewManager(ctx context.Context, opts HubOptions, maxStreams int, allowedHosts []string, log *slog.Logger) *Manager {
	return &Manager{
		ctx: ctx, log: log, opts: opts, maxStreams: maxStreams,
		allowedHosts: allowedHosts, hubs: make(map[string]*Hub),
	}
}

// Validate checks that raw is an acceptable RTSP URL and returns the
// normalised URL and a credential-free version for logs/UI.
func (m *Manager) Validate(raw string) (normalized, redacted string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", ValidationError{"URL is required"}
	}
	if len(raw) > 2048 {
		return "", "", ValidationError{"URL is too long"}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", ValidationError{"URL is not valid: " + err.Error()}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "rtsp" && u.Scheme != "rtsps" {
		return "", "", ValidationError{"Only rtsp:// and rtsps:// URLs are supported"}
	}
	host := u.Hostname()
	if host == "" {
		return "", "", ValidationError{"URL must include a host"}
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return "", "", ValidationError{"URL must not contain whitespace"}
	}
	if len(m.allowedHosts) > 0 && !hostAllowed(host, m.allowedHosts) {
		return "", "", ValidationError{fmt.Sprintf("Host %q is not allowed on this server", host)}
	}
	u.Host = strings.ToLower(u.Host)
	return u.String(), u.Redacted(), nil
}

func hostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(host)
	for _, a := range allowed {
		a = strings.ToLower(a)
		if a == "*" || a == host || (strings.HasPrefix(a, "*.") && strings.HasSuffix(host, a[1:])) {
			return true
		}
		if _, cidr, err := net.ParseCIDR(a); err == nil {
			if ip := net.ParseIP(host); ip != nil && cidr.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// StreamID returns the stable identifier for a normalised URL.
func StreamID(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:8])
}

// Subscribe validates rawURL, starts (or reuses) its pipeline and returns a
// subscriber for it.
func (m *Manager) Subscribe(rawURL string) (*Hub, *Subscriber, error) {
	normalized, redacted, err := m.Validate(rawURL)
	if err != nil {
		return nil, nil, err
	}
	id := StreamID(normalized)

	for range 3 { // retry if we raced with an idle hub shutting down
		m.mu.Lock()
		if m.ctx.Err() != nil {
			m.mu.Unlock()
			return nil, nil, errHubStopped
		}
		h, ok := m.hubs[id]
		if !ok {
			if m.maxStreams > 0 && len(m.hubs) >= m.maxStreams {
				m.mu.Unlock()
				return nil, nil, ErrCapacity
			}
			h = newHub(m.ctx, id, normalized, redacted, m.opts, m.log, m.remove)
			m.hubs[id] = h
			m.log.Info("stream started", "stream", id, "url", redacted, "active", len(m.hubs))
		}
		m.mu.Unlock()

		sub, err := h.Subscribe()
		if errors.Is(err, errHubStopped) {
			m.remove(h)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		return h, sub, nil
	}
	return nil, nil, errHubStopped
}

func (m *Manager) remove(h *Hub) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.hubs[h.ID]; ok && cur == h {
		delete(m.hubs, h.ID)
	}
}

// Stats returns a snapshot of every active stream, sorted by start time.
func (m *Manager) Stats() []Stats {
	m.mu.Lock()
	hubs := make([]*Hub, 0, len(m.hubs))
	for _, h := range m.hubs {
		hubs = append(hubs, h)
	}
	m.mu.Unlock()
	out := make([]Stats, 0, len(hubs))
	for _, h := range hubs {
		out = append(out, h.Stats())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// Shutdown stops every hub.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	hubs := make([]*Hub, 0, len(m.hubs))
	for _, h := range m.hubs {
		hubs = append(hubs, h)
	}
	m.mu.Unlock()
	for _, h := range hubs {
		h.Stop()
	}
}
