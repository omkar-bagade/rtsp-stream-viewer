// Package config loads runtime configuration from environment variables.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all tunables for the server. Every field has a sensible
// default so the binary runs with zero configuration in development.
type Config struct {
	// Addr is the HTTP listen address. PORT (set by Render/Railway/Fly) wins.
	Addr string
	// AllowedOrigins for WebSocket/CORS. "*" allows any origin.
	AllowedOrigins []string
	// AllowedHosts optionally restricts which RTSP hosts may be pulled
	// (protects a public deployment from being used as an open relay).
	// Empty means any host.
	AllowedHosts []string
	// StaticDir, if set, is served at / (single-binary deployment).
	StaticDir string

	FFmpegPath string
	// Transcode controls the video pipeline:
	//   "auto"   – copy H.264 untouched, transcode anything else (default)
	//   "always" – always re-encode to H.264 (normalises odd cameras, costs CPU)
	//   "never"  – never re-encode; non-H.264 sources fail with a clear error
	Transcode string
	// TranscodeMaxHeight caps the output height when transcoding.
	TranscodeMaxHeight int

	MaxStreams          int           // concurrent upstream RTSP pulls
	MaxViewersPerStream int           // WebSocket clients per stream
	IdleTimeout         time.Duration // keep FFmpeg alive this long after the last viewer leaves
	ConnectTimeout      time.Duration // RTSP socket timeout
	StallTimeout        time.Duration // restart FFmpeg if no data for this long

	// DemoStreams are advertised to the UI as one-click examples.
	DemoStreams []DemoStream
}

// DemoStream is a named example URL shown in the UI.
type DemoStream struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Load reads configuration from the environment.
func Load() Config {
	c := Config{
		Addr:                env("ADDR", ":8080"),
		AllowedOrigins:      list(env("ALLOWED_ORIGINS", "*")),
		AllowedHosts:        list(env("ALLOWED_RTSP_HOSTS", "")),
		StaticDir:           env("STATIC_DIR", ""),
		FFmpegPath:          env("FFMPEG_PATH", "ffmpeg"),
		Transcode:           strings.ToLower(env("TRANSCODE", "auto")),
		TranscodeMaxHeight:  envInt("TRANSCODE_MAX_HEIGHT", 720),
		MaxStreams:          envInt("MAX_STREAMS", 16),
		MaxViewersPerStream: envInt("MAX_VIEWERS_PER_STREAM", 50),
		IdleTimeout:         envDuration("IDLE_TIMEOUT", 15*time.Second),
		ConnectTimeout:      envDuration("RTSP_TIMEOUT", 10*time.Second),
		StallTimeout:        envDuration("STALL_TIMEOUT", 15*time.Second),
	}
	if p := os.Getenv("PORT"); p != "" {
		c.Addr = ":" + p
	}
	// DEMO_STREAMS="Name|rtsp://host/path,Other|rtsp://..."
	for _, item := range list(env("DEMO_STREAMS", "")) {
		name, url, ok := strings.Cut(item, "|")
		if !ok {
			name, url = item, item
		}
		c.DemoStreams = append(c.DemoStreams, DemoStream{Name: strings.TrimSpace(name), URL: strings.TrimSpace(url)})
	}
	return c
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(env(key, "")); err == nil && v > 0 {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(env(key, "")); err == nil && v > 0 {
		return v
	}
	return def
}

func list(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
