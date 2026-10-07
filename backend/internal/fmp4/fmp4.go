// Package fmp4 contains a minimal, allocation-light parser for the fragmented
// MP4 byte stream that FFmpeg writes with
// `-movflags empty_moov+default_base_moof+frag_keyframe`.
//
// We only need three things from the stream:
//
//  1. Split it into top-level boxes and group them into the initialization
//     segment (ftyp+moov) and media segments (moof+mdat).
//  2. Read the codec parameters from the init segment so the browser can build
//     an exact MSE MIME type (e.g. `video/mp4; codecs="avc1.64001f"`).
//  3. Detect whether a media segment starts with a sync sample (keyframe), so
//     that late-joining viewers can begin decoding immediately.
package fmp4

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MaxBoxSize guards against corrupt streams that would make us allocate huge
// buffers. A single fragment of a sane live stream is far below this.
const MaxBoxSize = 32 << 20 // 32 MiB

// Box is a single top-level ISO-BMFF box including its 8/16-byte header.
type Box struct {
	Type string
	Data []byte // full box bytes, header included
}

// Reader reads top-level boxes from a byte stream.
type Reader struct {
	r *bufio.Reader
}

// NewReader wraps r in a buffered box reader.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 256<<10)}
}

// Next returns the next top-level box.
func (br *Reader) Next() (Box, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(br.r, hdr[:]); err != nil {
		return Box{}, err
	}
	size := uint64(binary.BigEndian.Uint32(hdr[0:4]))
	typ := string(hdr[4:8])
	hdrLen := uint64(8)

	var large [8]byte
	switch size {
	case 1: // 64-bit largesize follows
		if _, err := io.ReadFull(br.r, large[:]); err != nil {
			return Box{}, err
		}
		size = binary.BigEndian.Uint64(large[:])
		hdrLen = 16
	case 0: // box extends to EOF – never valid for a live fragmented stream
		return Box{}, fmt.Errorf("fmp4: unbounded %q box not supported", typ)
	}
	if size < hdrLen {
		return Box{}, fmt.Errorf("fmp4: invalid size %d for box %q", size, typ)
	}
	if size > MaxBoxSize {
		return Box{}, fmt.Errorf("fmp4: box %q too large (%d bytes)", typ, size)
	}

	buf := make([]byte, size)
	copy(buf, hdr[:])
	if hdrLen == 16 {
		copy(buf[8:], large[:])
	}
	if _, err := io.ReadFull(br.r, buf[hdrLen:]); err != nil {
		return Box{}, err
	}
	return Box{Type: typ, Data: buf}, nil
}

// child is a view onto a box nested in a parent payload.
type child struct {
	typ     string
	payload []byte
}

// children iterates over the boxes contained in payload.
func children(payload []byte) []child {
	var out []child
	for len(payload) >= 8 {
		size := uint64(binary.BigEndian.Uint32(payload[0:4]))
		typ := string(payload[4:8])
		hdr := uint64(8)
		if size == 1 {
			if len(payload) < 16 {
				return out
			}
			size = binary.BigEndian.Uint64(payload[8:16])
			hdr = 16
		} else if size == 0 {
			size = uint64(len(payload))
		}
		if size < hdr || size > uint64(len(payload)) {
			return out
		}
		out = append(out, child{typ: typ, payload: payload[hdr:size]})
		payload = payload[size:]
	}
	return out
}

func find(payload []byte, path ...string) []byte {
	cur := payload
	for _, p := range path {
		found := false
		for _, c := range children(cur) {
			if c.typ == p {
				cur, found = c.payload, true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return cur
}

func payloadOf(b Box) []byte {
	if len(b.Data) >= 16 && binary.BigEndian.Uint32(b.Data[0:4]) == 1 {
		return b.Data[16:]
	}
	return b.Data[8:]
}

// ErrNoVideoTrack is returned when the init segment has no recognised video
// sample entry.
var ErrNoVideoTrack = errors.New("fmp4: no video track found in init segment")

// CodecInfo describes the video track of an init segment.
type CodecInfo struct {
	// SampleEntry is the 4CC of the sample entry, e.g. "avc1", "hev1".
	SampleEntry string
	// Codec is the RFC 6381 codec string for MSE, e.g. "avc1.64001f".
	Codec string
	Width  int
	Height int
}

// IsH264 reports whether the browser can be expected to decode the track.
func (c CodecInfo) IsH264() bool {
	return c.SampleEntry == "avc1" || c.SampleEntry == "avc3"
}

// ParseInit extracts video codec info from a moov box.
func ParseInit(moov Box) (CodecInfo, error) {
	for _, trak := range children(payloadOf(moov)) {
		if trak.typ != "trak" {
			continue
		}
		stsd := find(trak.payload, "mdia", "minf", "stbl", "stsd")
		// stsd: version/flags(4) + entry_count(4) + entries
		if len(stsd) < 8 {
			continue
		}
		for _, entry := range children(stsd[8:]) {
			info := CodecInfo{SampleEntry: entry.typ}
			// VisualSampleEntry: 6 reserved + 2 data_ref_idx + 16 pre_defined/reserved
			// + width(2) + height(2) + ... = 78 bytes before child boxes.
			if len(entry.payload) < 78 {
				continue
			}
			info.Width = int(binary.BigEndian.Uint16(entry.payload[24:26]))
			info.Height = int(binary.BigEndian.Uint16(entry.payload[26:28]))
			switch entry.typ {
			case "avc1", "avc3":
				avcC := find(entry.payload[78:], "avcC")
				if len(avcC) < 4 {
					return info, fmt.Errorf("fmp4: %s entry without avcC", entry.typ)
				}
				info.Codec = fmt.Sprintf("%s.%02x%02x%02x", entry.typ, avcC[1], avcC[2], avcC[3])
				return info, nil
			case "hev1", "hvc1", "av01", "vp09", "mp4v":
				// Recognised video, but we don't build a codec string: the caller
				// will transcode to H.264 for maximum browser compatibility.
				info.Codec = entry.typ
				return info, nil
			}
		}
	}
	return CodecInfo{}, ErrNoVideoTrack
}

const sampleIsNonSync = 0x00010000

// StartsWithKeyframe reports whether the first sample of a moof box is a sync
// sample. If the flags cannot be determined it optimistically returns true,
// which with `frag_keyframe` is the common case anyway.
func StartsWithKeyframe(moof Box) bool {
	for _, traf := range children(payloadOf(moof)) {
		if traf.typ != "traf" {
			continue
		}
		var (
			defaultFlags    uint32
			hasDefaultFlags bool
		)
		if tfhd := find(traf.payload, "tfhd"); len(tfhd) >= 8 {
			flags := binary.BigEndian.Uint32(tfhd[0:4]) & 0xFFFFFF
			off := 8 // version/flags + track_ID
			if flags&0x01 != 0 {
				off += 8
			}
			if flags&0x02 != 0 {
				off += 4
			}
			if flags&0x08 != 0 {
				off += 4
			}
			if flags&0x10 != 0 {
				off += 4
			}
			if flags&0x20 != 0 && len(tfhd) >= off+4 {
				defaultFlags = binary.BigEndian.Uint32(tfhd[off : off+4])
				hasDefaultFlags = true
			}
		}
		trun := find(traf.payload, "trun")
		if len(trun) < 8 {
			continue
		}
		flags := binary.BigEndian.Uint32(trun[0:4]) & 0xFFFFFF
		off := 8 // version/flags + sample_count
		if flags&0x01 != 0 {
			off += 4 // data_offset
		}
		if flags&0x04 != 0 && len(trun) >= off+4 {
			return binary.BigEndian.Uint32(trun[off:off+4])&sampleIsNonSync == 0
		}
		if flags&0x04 != 0 {
			off += 4
		}
		if flags&0x400 != 0 {
			// per-sample flags: skip duration/size if present
			if flags&0x100 != 0 {
				off += 4
			}
			if flags&0x200 != 0 {
				off += 4
			}
			if len(trun) >= off+4 {
				return binary.BigEndian.Uint32(trun[off:off+4])&sampleIsNonSync == 0
			}
		}
		if hasDefaultFlags {
			return defaultFlags&sampleIsNonSync == 0
		}
		return true
	}
	return true
}
