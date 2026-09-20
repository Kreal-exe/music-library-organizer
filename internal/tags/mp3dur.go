package tags

// MP3 files do not record their own playing time, so it is derived from the
// stream: a Xing/Info or VBRI header gives an exact frame count, and without
// one the length is estimated from the first frame's bitrate.
//
// The duration is only used to pick the right lyrics, so an estimate is fine.

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"time"
)

// Bitrates in kbit/s, indexed by the 4-bit field in the frame header.
var bitrates = map[int][16]int{
	// MPEG 1, Layer III.
	3: {0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
	// MPEG 2 and 2.5, Layer III.
	2: {0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
}

var sampleRates = map[int][4]int{
	3: {44100, 48000, 32000, 0}, // MPEG 1
	2: {22050, 24000, 16000, 0}, // MPEG 2
	0: {11025, 12000, 8000, 0},  // MPEG 2.5
}

func mp3Duration(path string) time.Duration {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0
	}

	start, err := audioStart(f)
	if err != nil {
		return 0
	}

	// A frame header sits within the first few kilobytes of audio unless the
	// file is padded with junk, which is not worth chasing.
	buf := make([]byte, 8192)
	n, err := f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return 0
	}
	buf = buf[:n]

	for off := 0; off+4 <= len(buf); off++ {
		frame, ok := parseFrameHeader(buf[off : off+4])
		if !ok {
			continue
		}

		if frames, ok := vbrFrameCount(buf[off:], frame); ok {
			return time.Duration(frames) * time.Duration(frame.samples) *
				time.Second / time.Duration(frame.sampleRate)
		}

		// Constant bitrate: the remaining bytes divided by the byte rate.
		if frame.bitrate == 0 {
			return 0
		}
		audioBytes := info.Size() - (start + int64(off))
		return time.Duration(audioBytes) * 8 * time.Second / time.Duration(frame.bitrate*1000)
	}

	return 0
}

// audioStart returns the offset just past an ID3v2 tag, if there is one.
func audioStart(r io.ReaderAt) (int64, error) {
	var head [10]byte
	if _, err := r.ReadAt(head[:], 0); err != nil {
		return 0, err
	}
	if string(head[:3]) != "ID3" {
		return 0, nil
	}

	// The size is stored as four synchsafe bytes, seven bits each.
	size := int64(head[6]&0x7F)<<21 | int64(head[7]&0x7F)<<14 |
		int64(head[8]&0x7F)<<7 | int64(head[9]&0x7F)
	start := 10 + size
	if head[5]&0x10 != 0 {
		start += 10 // A footer is present.
	}
	return start, nil
}

type frameHeader struct {
	versionID  int // 3 = MPEG 1, 2 = MPEG 2, 0 = MPEG 2.5
	bitrate    int // kbit/s
	sampleRate int
	samples    int // Samples per frame.
	mono       bool
}

func parseFrameHeader(b []byte) (frameHeader, bool) {
	var h frameHeader

	// Eleven sync bits, then version and layer, neither of which may be the
	// reserved value.
	if b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return h, false
	}
	h.versionID = int(b[1] >> 3 & 0x03)
	layer := int(b[1] >> 1 & 0x03)
	if h.versionID == 1 || layer != 1 { // Layer III is encoded as 1.
		return h, false
	}

	rateKey := h.versionID
	if rateKey == 0 { // MPEG 2.5 shares the MPEG 2 bitrate table.
		rateKey = 2
	}
	table, ok := bitrates[rateKey]
	if !ok {
		return h, false
	}
	h.bitrate = table[b[2]>>4&0x0F]

	rates, ok := sampleRates[h.versionID]
	if !ok {
		return h, false
	}
	h.sampleRate = rates[b[2]>>2&0x03]
	if h.sampleRate == 0 {
		return h, false
	}

	h.samples = 1152
	if h.versionID != 3 {
		h.samples = 576 // MPEG 2 and 2.5 halve the frame size.
	}
	h.mono = b[3]>>6&0x03 == 3

	return h, true
}

// vbrFrameCount reads the frame count out of a Xing, Info or VBRI header,
// which variable-bitrate encoders place inside the first frame.
func vbrFrameCount(frame []byte, h frameHeader) (int, bool) {
	// Xing and Info sit right after the side information, whose length depends
	// on the MPEG version and the channel mode.
	sideInfo := 32 // MPEG 1, more than one channel.
	switch {
	case h.versionID == 3 && h.mono:
		sideInfo = 17
	case h.versionID != 3 && !h.mono:
		sideInfo = 17
	case h.versionID != 3 && h.mono:
		sideInfo = 9
	}
	offset := 4 + sideInfo

	if offset+12 <= len(frame) {
		tag := frame[offset : offset+4]
		if bytes.Equal(tag, []byte("Xing")) || bytes.Equal(tag, []byte("Info")) {
			flags := binary.BigEndian.Uint32(frame[offset+4 : offset+8])
			if flags&0x0001 != 0 {
				count := int(binary.BigEndian.Uint32(frame[offset+8 : offset+12]))
				if count > 0 {
					return count, true
				}
			}
		}
	}

	// VBRI is always 32 bytes past the header, with the count 14 bytes in.
	const vbriAt = 36
	if vbriAt+18 <= len(frame) && bytes.Equal(frame[vbriAt:vbriAt+4], []byte("VBRI")) {
		count := int(binary.BigEndian.Uint32(frame[vbriAt+14 : vbriAt+18]))
		if count > 0 {
			return count, true
		}
	}

	return 0, false
}
