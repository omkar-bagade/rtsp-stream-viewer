package fmp4

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// generate produces a fragmented MP4 using the same muxer flags as the
// server. Tests are skipped when FFmpeg isn't installed.
func generate(t *testing.T, codecArgs ...string) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	args := []string{"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25", "-t", "4"}
	args = append(args, codecArgs...)
	args = append(args, "-f", "mp4", "-movflags", "empty_moov+default_base_moof+frag_keyframe",
		"-frag_duration", "200000", "pipe:1")
	out, err := exec.Command("ffmpeg", args...).Output()
	if err != nil {
		t.Fatalf("ffmpeg: %v", err)
	}
	return out
}

func TestParseH264Stream(t *testing.T) {
	data := generate(t, "-c:v", "libx264", "-profile:v", "high", "-g", "25", "-pix_fmt", "yuv420p")
	r := NewReader(bytes.NewReader(data))

	var types []string
	var keyframes, segments int
	var info CodecInfo
	for {
		b, err := r.Next()
		if err != nil {
			break
		}
		types = append(types, b.Type)
		switch b.Type {
		case "moov":
			if info, err = ParseInit(b); err != nil {
				t.Fatalf("ParseInit: %v", err)
			}
		case "moof":
			segments++
			if StartsWithKeyframe(b) {
				keyframes++
			}
		}
	}
	if len(types) < 4 || types[0] != "ftyp" || types[1] != "moov" {
		t.Fatalf("unexpected box order: %v", types[:min(len(types), 4)])
	}
	if !info.IsH264() || !strings.HasPrefix(info.Codec, "avc1.64") {
		t.Fatalf("unexpected codec %+v", info)
	}
	if info.Width != 320 || info.Height != 240 {
		t.Fatalf("unexpected size %dx%d", info.Width, info.Height)
	}
	// 4s at GOP 1s => 4 keyframe fragments; 200ms fragments => ~20 total.
	if keyframes != 4 {
		t.Errorf("keyframe fragments = %d, want 4 (of %d)", keyframes, segments)
	}
	if segments < 10 {
		t.Errorf("segments = %d, expected frag_duration to split GOPs", segments)
	}
}

func TestParseHEVCRequiresTranscode(t *testing.T) {
	if exec.Command("ffmpeg", "-hide_banner", "-h", "encoder=libx265").Run() != nil {
		t.Skip("libx265 not available")
	}
	data := generate(t, "-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p")
	r := NewReader(bytes.NewReader(data))
	for {
		b, err := r.Next()
		if err != nil {
			t.Fatal("no moov box")
		}
		if b.Type == "moov" {
			info, err := ParseInit(b)
			if err != nil {
				t.Fatal(err)
			}
			if info.IsH264() {
				t.Fatalf("HEVC reported as H.264: %+v", info)
			}
			return
		}
	}
}

func TestRejectsOversizedBox(t *testing.T) {
	hdr := []byte{0x7F, 0xFF, 0xFF, 0xFF, 'm', 'd', 'a', 't'}
	if _, err := NewReader(bytes.NewReader(hdr)).Next(); err == nil {
		t.Fatal("expected error for oversized box")
	}
}
