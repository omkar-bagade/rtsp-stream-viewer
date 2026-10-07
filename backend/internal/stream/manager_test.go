package stream

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func testManager(allowed ...string) *Manager {
	return NewManager(context.Background(), HubOptions{}, 4, allowed, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestValidate(t *testing.T) {
	m := testManager()
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"rtsp://127.0.0.1:8554/cam1", false},
		{"RTSP://Camera.Local/stream", false},
		{"rtsps://user:pass@cam.example.com:322/live", false},
		{"", true},
		{"http://example.com/stream", true},
		{"rtsp:///nohost", true},
		{"not a url", true},
		{"rtsp://" + strings.Repeat("a", 3000), true},
	}
	for _, c := range cases {
		_, _, err := m.Validate(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("Validate(%q) err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
	}
}

func TestValidateRedactsAndNormalises(t *testing.T) {
	m := testManager()
	norm, red, err := m.Validate("RTSP://admin:secret@CAM.local:554/live")
	if err != nil {
		t.Fatal(err)
	}
	if norm != "rtsp://admin:secret@cam.local:554/live" {
		t.Errorf("normalised = %q", norm)
	}
	if strings.Contains(red, "secret") {
		t.Errorf("redacted URL leaks password: %q", red)
	}
	if StreamID(norm) != StreamID("rtsp://admin:secret@cam.local:554/live") {
		t.Error("StreamID not stable")
	}
}

func TestAllowedHosts(t *testing.T) {
	m := testManager("localhost", "*.example.com", "10.0.0.0/8")
	ok := []string{"rtsp://localhost/a", "rtsp://cam.example.com/a", "rtsp://10.1.2.3/a"}
	bad := []string{"rtsp://evil.com/a", "rtsp://192.168.1.2/a", "rtsp://example.com.evil.io/a"}
	for _, u := range ok {
		if _, _, err := m.Validate(u); err != nil {
			t.Errorf("%s should be allowed: %v", u, err)
		}
	}
	for _, u := range bad {
		if _, _, err := m.Validate(u); err == nil {
			t.Errorf("%s should be rejected", u)
		}
	}
}

func TestFriendlyError(t *testing.T) {
	cases := map[string]string{
		"method DESCRIBE failed: 401 Unauthorized":               "Authentication",
		"Server returned 404 Not Found":                          "not found",
		"Connection to tcp://x:1 failed: Connection refused":     "refused",
		"Failed to resolve hostname nope.invalid":                "DNS",
		"Connection to tcp://10.255.255.1:554 failed: timed out": "timed out",
	}
	for in, want := range cases {
		if got := friendlyError(in, nil); !strings.Contains(got, want) {
			t.Errorf("friendlyError(%q) = %q, want substring %q", in, got, want)
		}
	}
}

func TestFFmpegArgsCopyVsTranscode(t *testing.T) {
	copyArgs := strings.Join(ffmpegArgs("rtsp://h/p", false, 5e9, 720), " ")
	if !strings.Contains(copyArgs, "-c:v copy") || !strings.Contains(copyArgs, "-rtsp_transport tcp") {
		t.Errorf("copy args: %s", copyArgs)
	}
	tx := strings.Join(ffmpegArgs("rtsp://h/p", true, 5e9, 480), " ")
	if !strings.Contains(tx, "libx264") || !strings.Contains(tx, "min(480,ih)") {
		t.Errorf("transcode args: %s", tx)
	}
}
