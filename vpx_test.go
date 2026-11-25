// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// The VP8 and VP9 streams in testdata/vpx were made with ffmpeg's libvpx and libvpx-vp9
// encoders from lavfi sources (the commands are in testdata/vpx/README.md); the .md5 next to
// each is `ffmpeg -f framemd5 -pix_fmt yuv420p` of the same file, ffmpeg's native decoder.

// vpxStreams are the committed streams that are decoded: name, codec, picture size.
var vpxStreams = []struct {
	name, codec string
	w, h        int
}{
	{"vp8_altref", "vp08", 176, 144}, // hidden alt-ref frames
	{"vp8_odd", "vp08", 161, 97},     // sizes that are no multiple of 16 or 2
	{"vp9_altref", "vp09", 176, 144}, // superframes holding a hidden frame
	{"vp9_odd", "vp09", 161, 97},
	{"vp9_tiles", "vp09", 640, 96}, // two tile columns
}

func vpxTrack(t testing.TB, name string) (*container.Reader, uint32) {
	t.Helper()
	r, err := container.NewReader(readFile(t, filepath.Join("testdata", "vpx", name+".webm")))
	if err != nil {
		t.Fatal(err)
	}
	id, err := FirstVideoTrack(r)
	if err != nil {
		t.Fatal(err)
	}
	return r, id
}

// vpxReference is the hash column of ffmpeg's framemd5 for a stream.
func vpxReference(t testing.TB, name string) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(string(readFile(t, filepath.Join("testdata", "vpx", name+".md5"))), "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" || l[0] == '#' {
			continue
		}
		f := strings.Split(l, ",")
		out = append(out, strings.TrimSpace(f[len(f)-1]))
	}
	return out
}

// TestVPXMatchesFFmpeg: every picture of every stream is ffmpeg's, byte for byte, through
// DecodeTrack and through Frames.
func TestVPXMatchesFFmpeg(t *testing.T) {
	for _, s := range vpxStreams {
		t.Run(s.name, func(t *testing.T) {
			r, id := vpxTrack(t, s.name)
			tc, err := r.TrackConfig(id)
			if err != nil || tc.Codec != s.codec {
				t.Fatalf("codec %q (%v), want %q", tc.Codec, err, s.codec)
			}
			want := vpxReference(t, s.name)
			frames, err := DecodeTrack(r, id, Config{})
			if err != nil {
				t.Fatal(err)
			}
			got := sums(frames)
			if len(got) != len(want) {
				t.Fatalf("%d pictures, ffmpeg gives %d", len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("picture %d: md5 %s, ffmpeg %s", i, got[i], want[i])
				}
			}
			var last int64 = -1
			for i, f := range frames {
				if f.Width != s.w || f.Height != s.h || f.Chroma != Chroma420 || f.BitDepth != 8 || f.BitDepthC != 8 || f.Corrupt {
					t.Fatalf("picture %d: %dx%d chroma %d depth %d/%d corrupt %v", i, f.Width, f.Height, f.Chroma, f.BitDepth, f.BitDepthC, f.Corrupt)
				}
				if cw, ch := f.ChromaSize(); len(f.Cb) != cw*ch || f.YStride != f.Width || f.CStride != cw {
					t.Fatalf("picture %d: planes do not follow the Frame layout", i)
				}
				if f.PTS <= last {
					t.Fatalf("picture %d: pts %d after %d", i, f.PTS, last)
				}
				last = f.PTS
			}
			n := 0
			for f, err := range Frames(r, id, Config{}) {
				if err != nil {
					t.Fatal(err)
				}
				if n < len(got) && sum(*f) != got[n] {
					t.Errorf("Frames picture %d differs from DecodeTrack", n)
				}
				n++
			}
			if n != len(got) {
				t.Errorf("Frames gave %d pictures, DecodeTrack %d", n, len(got))
			}
		})
	}
}

// TestVPXPoster: the poster of a VP8 and a VP9 file is a picture of the stream.
func TestVPXPoster(t *testing.T) {
	for _, s := range vpxStreams {
		r, id := vpxTrack(t, s.name)
		f, err := PosterFrame(r, id, 0, PosterConfig{})
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if f.Width != s.w || f.Height != s.h || sum(f) != vpxReference(t, s.name)[0] {
			t.Errorf("%s: poster %dx%d is not the first picture", s.name, f.Width, f.Height)
		}
	}
}

// TestVP9OtherProfilesDecode: profile 1 (4:4:4, 8 bits) and profile 2 (4:2:0, 10 bits) streams
// decode to Frames of that sampling and depth, planes packed (little-endian words above 8 bits).
// Picture equality for profiles 1 to 3 is TestLibvpxVectors, against libvpx's own md5s.
func TestVP9OtherProfilesDecode(t *testing.T) {
	for _, c := range []struct {
		name   string
		chroma Chroma
		depth  int
	}{{"vp9_profile1", Chroma444, 8}, {"vp9_profile2", Chroma420, 10}} {
		r, id := vpxTrack(t, c.name)
		frames, err := DecodeTrack(r, id, Config{})
		if err != nil || len(frames) == 0 {
			t.Fatalf("%s: %d pictures, err %v", c.name, len(frames), err)
		}
		for i, f := range frames {
			cw, ch := f.ChromaSize()
			sb := f.SampleBytes(0)
			if f.Chroma != c.chroma || f.BitDepth != c.depth || f.BitDepthC != c.depth ||
				f.YStride != f.Width*sb || len(f.Y) != f.YStride*f.Height ||
				f.CStride != cw*sb || len(f.Cb) != f.CStride*ch || len(f.Cr) != len(f.Cb) {
				t.Fatalf("%s picture %d: chroma %d depth %d/%d, %dx%d strides %d/%d planes %d/%d/%d",
					c.name, i, f.Chroma, f.BitDepth, f.BitDepthC, f.Width, f.Height, f.YStride, f.CStride, len(f.Y), len(f.Cb), len(f.Cr))
			}
		}
	}
}

// TestVPXOverLimit: a stream over Config.MaxFrameMBs is refused as ErrOverLimit.
func TestVPXOverLimit(t *testing.T) {
	for _, name := range []string{"vp8_altref", "vp9_altref", "vp9_tiles"} {
		r, id := vpxTrack(t, name)
		frames, err := DecodeTrack(r, id, Config{MaxFrameMBs: 50})
		if !errors.Is(err, ErrOverLimit) || len(frames) != 0 {
			t.Errorf("%s: %d pictures, err %v; want ErrOverLimit", name, len(frames), err)
		}
	}
}

func TestVPXAnnexBRefused(t *testing.T) {
	for _, mk := range []func(Config) (*Decoder, error){NewVP8, NewVP9} {
		d, err := mk(Config{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.DecodeAnnexB([]byte{0, 0, 1, 2}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("Annex B: %v, want ErrUnsupported", err)
		}
		if fs, err := d.Flush(); fs != nil || err != nil {
			t.Errorf("Flush: %v, %v", fs, err)
		}
		d.Close()
		if _, err := d.DecodeSample([]byte{1}, 0); !errors.Is(err, ErrClosed) {
			t.Errorf("after Close: %v, want ErrClosed", err)
		}
	}
}

func TestVPXMalformedSamples(t *testing.T) {
	for _, mk := range []func(Config) (*Decoder, error){NewVP8, NewVP9} {
		d, _ := mk(Config{})
		if _, err := d.DecodeSample(nil, 0); !errors.Is(err, ErrSample) {
			t.Errorf("empty sample: %v, want ErrSample", err)
		}
		if _, err := d.DecodeSample([]byte{0x01, 0x02, 0x03, 0x04}, 0); err == nil {
			t.Error("garbage decoded")
		}
		if _, err := d.DecodeSample(make([]byte, 100), 0); err == nil {
			t.Error("zeros decoded")
		}
		d.Close()
	}
	d, _ := NewVP8(Config{MaxSampleBytes: 8})
	if _, err := d.DecodeSample(make([]byte, 9), 0); !errors.Is(err, ErrOverLimit) {
		t.Errorf("sample over MaxSampleBytes: %v", err)
	}
}

// vp8Key is a VP8 key frame header declaring a w x h picture, with no data behind it.
func vp8Key(w, h int) []byte {
	return []byte{0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a, byte(w), byte(w >> 8), byte(h), byte(h >> 8), 0, 0, 0, 0}
}

// vp9Key is a profile 0 key frame header declaring a w x h picture.
func vp9Key(w, h int) []byte {
	var bits []bool
	put := func(v, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, v>>i&1 == 1)
		}
	}
	put(2, 2)         // frame_marker
	put(0, 2)         // profile 0
	put(0, 1)         // show_existing_frame
	put(0, 1)         // key frame
	put(1, 1)         // show_frame
	put(0, 1)         // error_resilient_mode
	put(0x498342, 24) // sync code
	put(1, 3)         // color_space
	put(0, 1)         // color_range
	put(w-1, 16)
	put(h-1, 16)
	put(0, 8)
	out := make([]byte, (len(bits)+7)/8+8)
	for i, b := range bits {
		if b {
			out[i/8] |= 0x80 >> (i % 8)
		}
	}
	return out
}

// TestVPXSizeBomb: a few dozen bytes declaring a picture of 16383x16383 (VP8) or 65536x65536
// (VP9) are refused as ErrOverLimit under a memory limit, and the heap grows by nothing near
// the planes a decoder that trusted them would take.
func TestVPXSizeBomb(t *testing.T) {
	old := debug.SetMemoryLimit(256 << 20)
	defer debug.SetMemoryLimit(old)
	for _, tc := range []struct {
		name   string
		mk     func(Config) (*Decoder, error)
		sample []byte
	}{
		{"VP8 16383x16383", NewVP8, vp8Key(16383, 16383)},
		{"VP8 4096x4096", NewVP8, vp8Key(4096, 4096)},
		{"VP9 65536x65536", NewVP9, vp9Key(65536, 65536)},
		{"VP9 8192x8192", NewVP9, vp9Key(8192, 8192)},
	} {
		d, err := tc.mk(Config{})
		if err != nil {
			t.Fatal(err)
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		_, err = d.DecodeSample(tc.sample, 0)
		runtime.ReadMemStats(&after)
		d.Close()
		if !errors.Is(err, ErrOverLimit) {
			t.Fatalf("%s: %v, want ErrOverLimit", tc.name, err)
		}
		if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 32<<20 {
			t.Fatalf("%s: the heap grew by %d bytes", tc.name, grown)
		}
	}
	// The byte cap holds with the macroblock limit out of the way: 65536x65536 is 6 GB of planes.
	d, _ := NewVP9(Config{MaxFrameMBs: 1 << 30})
	defer d.Close()
	if _, err := d.DecodeSample(vp9Key(65536, 65536), 0); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("VP9 65536x65536 with the limit raised: %v, want ErrOverLimit", err)
	}
}

// TestVP9FrameSize pins the header reader, including the superframe index.
func TestVP9FrameSize(t *testing.T) {
	w, h, ok := vp9FrameSize(vp9Key(1920, 1080))
	if !ok || w != 1920 || h != 1080 {
		t.Fatalf("%d x %d ok %v", w, h, ok)
	}
	// a superframe of two frames of 12 bytes each, one-byte sizes
	a, b := vp9Key(100, 50), vp9Key(200, 60)
	n := len(a)
	sf := append(append(append([]byte{}, a...), b...), 0xc1, byte(n), byte(len(b)), 0xc1)
	sizes := vp9Sizes(sf)
	if len(sizes) != 2 || sizes[0] != [2]int{100, 50} || sizes[1] != [2]int{200, 60} {
		t.Fatalf("superframe sizes %v", sizes)
	}
}
