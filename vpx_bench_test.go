package decode

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vpxBench decodes every file named by VPX_BENCH_FILES (a comma-separated list of IVF or WebM
// paths) start to finish, one fresh decoder per iteration, and reports ns/frame and frames/s next
// to the usual ns/op and allocations. The codec is read from the container (IVF fourcc VP80 or
// VP90, WebM CodecID V_VP8 or V_VP9), never from the file name. It skips when the variable is unset.
func vpxBench(b *testing.B, vp9 bool) {
	list := os.Getenv("VPX_BENCH_FILES")
	if list == "" {
		b.Skip("VPX_BENCH_FILES is not set: no VP8/VP9 stream to decode")
	}
	ran := 0
	for _, path := range strings.Split(list, ",") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		base := filepath.Base(path)
		data, err := os.ReadFile(path)
		if err != nil {
			b.Fatal(err)
		}
		isVP8, err := vpxCodecIsVP8(data)
		if err != nil {
			b.Fatalf("%s: %v", base, err)
		}
		if isVP8 == vp9 {
			continue
		}
		ran++
		b.Run(base, func(b *testing.B) {
			samples, err := libvpxSamples(path)
			if err != nil {
				b.Fatal(err)
			}
			mk := NewVP9
			if isVP8 {
				mk = NewVP8
			}
			var pictures int
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d, err := mk(Config{})
				if err != nil {
					b.Fatal(err)
				}
				pictures = 0
				for j, s := range samples {
					frames, err := d.DecodeSample(s, int64(j))
					if err != nil {
						b.Fatalf("sample %d: %v", j, err)
					}
					pictures += len(frames)
				}
				d.Close()
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(pictures), "ns/frame")
			b.ReportMetric(float64(pictures), "frames")
		})
	}
	if ran == 0 {
		b.Skip("VPX_BENCH_FILES names no file for this codec")
	}
}

// vpxCodecIsVP8 reads the codec of an IVF or WebM file from its container: true for VP8, false for
// VP9, an error for anything else. An IVF header carries the fourcc at bytes 8 to 11; a WebM file
// carries the CodecID of a TrackEntry, found by walking the EBML elements (Segment, Tracks and
// TrackEntry are entered, the rest skipped), which works on the old files the container reader
// refuses.
func vpxCodecIsVP8(b []byte) (bool, error) {
	if len(b) >= 12 && string(b[:4]) == "DKIF" {
		switch string(b[8:12]) {
		case "VP80":
			return true, nil
		case "VP90":
			return false, nil
		}
		return false, fmt.Errorf("IVF fourcc %q is neither VP80 nor VP90", b[8:12])
	}
	const segment, tracks, trackEntry, codecID = 0x18538067, 0x1654AE6B, 0xAE, 0x86
	pos := 0
	vint := func(keepMarker bool) (uint64, bool) {
		if pos >= len(b) || b[pos] == 0 {
			return 0, false
		}
		n := 1
		for b[pos]&(0x80>>(n-1)) == 0 {
			n++
		}
		if pos+n > len(b) {
			return 0, false
		}
		v := uint64(b[pos])
		if !keepMarker {
			v &= 0xff >> n
		}
		for _, c := range b[pos+1 : pos+n] {
			v = v<<8 | uint64(c)
		}
		pos += n
		return v, true
	}
	for pos < len(b) {
		id, ok := vint(true)
		if !ok {
			break
		}
		size, ok := vint(false)
		if !ok {
			break
		}
		switch id {
		case segment, tracks, trackEntry:
			continue // enter it
		}
		if size > uint64(len(b)-pos) {
			break
		}
		body := b[pos : pos+int(size)]
		pos += int(size)
		if id == codecID {
			switch string(body) {
			case "V_VP8":
				return true, nil
			case "V_VP9":
				return false, nil
			}
			return false, fmt.Errorf("WebM CodecID %q is neither V_VP8 nor V_VP9", body)
		}
	}
	return false, errors.New("neither an IVF file nor a WebM file with a track CodecID")
}

func TestVPXCodecFromContainer(t *testing.T) {
	ivf := func(fourcc string) []byte {
		h := make([]byte, 32)
		copy(h, "DKIF")
		copy(h[8:], fourcc)
		return h
	}
	for name, c := range map[string]struct {
		data []byte
		vp8  bool
		ok   bool
	}{
		"ivf VP80":      {ivf("VP80"), true, true},
		"ivf VP90":      {ivf("VP90"), false, true},
		"ivf H264":      {ivf("H264"), false, false},
		"webm VP8":      {readFile(t, "testdata/vpx/vp8_altref.webm"), true, true},
		"webm VP9":      {readFile(t, "testdata/vpx/vp9_altref.webm"), false, true},
		"webm profile1": {readFile(t, "testdata/vpx/vp9_profile1.webm"), false, true},
		"garbage":       {[]byte("not a video file at all"), false, false},
		"empty":         {nil, false, false},
	} {
		got, err := vpxCodecIsVP8(c.data)
		if (err == nil) != c.ok || (c.ok && got != c.vp8) {
			t.Errorf("%s: vp8=%v err=%v, want vp8=%v ok=%v", name, got, err, c.vp8, c.ok)
		}
	}
}

func BenchmarkVP8Decode(b *testing.B) { vpxBench(b, false) }

func BenchmarkVP9Decode(b *testing.B) { vpxBench(b, true) }
