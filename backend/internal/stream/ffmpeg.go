package stream

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// errNeedsTranscode signals that the source codec is not browser-friendly and
// the pipeline should be restarted with re-encoding enabled.
var errNeedsTranscode = errors.New("source codec requires transcoding")

// errUnsupportedCodec is permanent: transcoding is disabled and the codec
// can't be played via MSE.
type errUnsupportedCodec struct{ codec string }

func (e errUnsupportedCodec) Error() string {
	return fmt.Sprintf("unsupported codec %q (set TRANSCODE=auto to convert it to H.264)", e.codec)
}

// ffmpegArgs builds the FFmpeg command line that pulls an RTSP source and
// writes fragmented MP4 (H.264 video only) to stdout.
//
// Design notes:
//   - `-c:v copy` is used whenever possible: no decode/encode means a stream
//     costs ~1% of a CPU core, which is what makes many concurrent streams
//     feasible on small instances.
//   - Fragments are cut at every keyframe and additionally every 500ms
//     (`-frag_duration`) to keep glass-to-glass latency low regardless of the
//     camera's GOP length.
//   - Audio is dropped: surveillance-style RTSP audio is frequently G.711/PCM
//     which MSE cannot play, and the grid view is muted anyway.
func ffmpegArgs(src string, transcode bool, connectTimeout time.Duration, maxHeight int) []string {
	args := []string{
		"-hide_banner", "-nostdin", "-loglevel", "error",
		"-fflags", "+genpts+nobuffer",
		"-analyzeduration", "2000000", "-probesize", "2000000",
	}
	if strings.HasPrefix(strings.ToLower(src), "rtsp") {
		args = append(args,
			"-rtsp_transport", "tcp",
			// RTSP socket I/O timeout, microseconds (FFmpeg >= 5).
			"-timeout", strconv.FormatInt(connectTimeout.Microseconds(), 10),
		)
	}
	args = append(args, "-i", src, "-map", "0:v:0", "-an", "-sn", "-dn")

	if transcode {
		args = append(args,
			"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency",
			"-profile:v", "main", "-pix_fmt", "yuv420p",
			"-g", "50", "-sc_threshold", "0",
			"-vf", fmt.Sprintf("scale=-2:'min(%d,ih)':flags=fast_bilinear", maxHeight),
		)
	} else {
		args = append(args, "-c:v", "copy")
	}

	args = append(args,
		"-f", "mp4",
		"-movflags", "empty_moov+default_base_moof+frag_keyframe",
		"-frag_duration", "500000",
		"pipe:1",
	)
	return args
}

// stderrTail keeps the last few KB of FFmpeg's stderr for error reporting.
type stderrTail struct {
	mu  sync.Mutex
	buf []byte
}

const stderrTailMax = 4096

func (s *stderrTail) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
	if len(s.buf) > stderrTailMax {
		s.buf = s.buf[len(s.buf)-stderrTailMax:]
	}
	return len(p), nil
}

func (s *stderrTail) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(string(s.buf))
}

// friendlyError turns FFmpeg's stderr into a short, actionable message for
// the UI. The raw text is still logged server-side.
func friendlyError(stderr string, fallback error) string {
	l := strings.ToLower(stderr)
	switch {
	case strings.Contains(l, "401") || strings.Contains(l, "unauthorized"):
		return "Authentication failed — check the username/password in the URL"
	case strings.Contains(l, "404") || strings.Contains(l, "not found"):
		return "Stream not found (404) — check the stream path"
	case strings.Contains(l, "connection refused"):
		return "Connection refused — is the RTSP server running on that port?"
	case strings.Contains(l, "timed out") || strings.Contains(l, "timeout"):
		return "Connection timed out — the camera/server is not responding"
	case strings.Contains(l, "name or service not known"),
		strings.Contains(l, "failed to resolve"),
		strings.Contains(l, "temporary failure in name resolution"),
		strings.Contains(l, "no address associated"):
		return "Host not found — DNS lookup failed"
	case strings.Contains(l, "no route to host"), strings.Contains(l, "network is unreachable"):
		return "Host unreachable"
	case strings.Contains(l, "connection reset"), strings.Contains(l, "broken pipe"):
		return "Connection lost — the server closed the stream"
	case strings.Contains(l, "invalid data found"):
		return "Invalid stream data — is this a valid RTSP video stream?"
	case strings.Contains(l, "does not contain any stream"), strings.Contains(l, "matches no streams"):
		return "The stream has no video track"
	}
	// Otherwise use the last meaningful line, skipping per-frame decoder
	// warnings such as "[h264 @ 0x…] mmco: unref short failure".
	lines := strings.Split(stderr, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "[h264 @") || strings.HasPrefix(line, "[hevc @") {
			continue
		}
		return "Stream error: " + line
	}
	if fallback != nil {
		return "Stream ended: " + fallback.Error()
	}
	return "Stream ended unexpectedly"
}
