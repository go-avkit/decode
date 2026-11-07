// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
)

func TestConformanceAnnexB(t *testing.T) {
	for _, path := range streams(t) {
		t.Run(path, func(t *testing.T) {
			data := readFile(t, path)
			want := reference(t, path)
			for _, chunk := range []int{len(data), 997} {
				d, err := New(Config{}, 4)
				if err != nil {
					t.Fatal(err)
				}
				var got []Frame
				for rest := data; len(rest) > 0; {
					n := min(chunk, len(rest))
					fs, err := d.DecodeAnnexB(rest[:n])
					if err != nil {
						t.Fatal(err)
					}
					got = append(got, fs...)
					rest = rest[n:]
				}
				fs, err := d.Flush()
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, fs...)
				if !slices.Equal(sums(got), want) {
					t.Fatalf("chunk %d: %d pictures, ffmpeg has %d, or a picture differs", chunk, len(got), len(want))
				}
				for i, f := range got {
					if f.PTS != int64(i) || f.BitDepth != 8 || f.Chroma != Chroma420 {
						t.Fatalf("picture %d: PTS %d depth %d chroma %d", i, f.PTS, f.BitDepth, f.Chroma)
					}
				}
				if err := d.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestConformanceSamples(t *testing.T) {
	for _, path := range streams(t) {
		t.Run(path, func(t *testing.T) {
			data := readFile(t, path)
			want := reference(t, path)
			aus := accessUnits(splitNALs(data))
			sps, pps, slices_ := parameterSets(aus)
			cases := map[string]func() (*Decoder, [][]byte){
				"in-band": func() (*Decoder, [][]byte) {
					d, err := New(Config{}, 4)
					if err != nil {
						t.Fatal(err)
					}
					var s [][]byte
					for _, au := range aus {
						s = append(s, prefixed(au, 4))
					}
					return d, s
				},
				"avcC": func() (*Decoder, [][]byte) {
					d, err := NewAVCC(avcC(sps, pps, 2, true), Config{})
					if err != nil {
						t.Fatal(err)
					}
					var s [][]byte
					for _, au := range slices_ {
						s = append(s, prefixed(au, 2))
					}
					return d, s
				},
			}
			for name, mk := range cases {
				d, samples := mk()
				var got []Frame
				for i, s := range samples {
					fs, err := d.DecodeSample(s, int64(i)*3+7)
					if err != nil {
						t.Fatalf("%s: sample %d: %v", name, i, err)
					}
					got = append(got, fs...)
				}
				fs, err := d.Flush()
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, fs...)
				if !slices.Equal(sums(got), want) {
					t.Fatalf("%s: %d pictures, ffmpeg has %d, or a picture differs", name, len(got), len(want))
				}
				for i, f := range got {
					if f.PTS != int64(i)*3+7 {
						t.Fatalf("%s: picture %d has PTS %d, want %d", name, i, f.PTS, int64(i)*3+7)
					}
				}
			}
		})
	}
}

// One sample holding every picture: the first gets the sample's time, the
// rest the last time given.
func TestSampleHoldingManyPictures(t *testing.T) {
	path := "testdata/streams/main_ip_cabac.264"
	var all [][]byte
	for _, au := range accessUnits(splitNALs(readFile(t, path))) {
		all = append(all, au...)
	}
	d, err := New(Config{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.DecodeSample(prefixed(all, 4), 42)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := d.Flush()
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, fs...)
	if !slices.Equal(sums(got), reference(t, path)) {
		t.Fatal("pictures differ")
	}
	for i, f := range got {
		if f.PTS != 42 {
			t.Fatalf("picture %d has PTS %d, want 42", i, f.PTS)
		}
	}
}

func TestUseAfterClose(t *testing.T) {
	d, err := New(Config{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := d.DecodeSample([]byte{0, 0, 0, 1, 9}, 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("DecodeSample: %v", err)
	}
	if _, err := d.DecodeAnnexB([]byte{0, 0, 1, 9}); !errors.Is(err, ErrClosed) {
		t.Fatalf("DecodeAnnexB: %v", err)
	}
	if _, err := d.Flush(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Flush: %v", err)
	}
}

func TestNewRefusals(t *testing.T) {
	for _, n := range []int{0, 3, 5, -1} {
		if _, err := New(Config{}, n); !errors.Is(err, ErrConfig) {
			t.Errorf("length size %d: %v", n, err)
		}
	}
}

func TestSampleRefusals(t *testing.T) {
	d, err := New(Config{MaxSampleBytes: 16}, 4)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		data []byte
		want error
	}{
		"oversize":      {make([]byte, 17), ErrOverLimit},
		"short length":  {[]byte{0, 0}, ErrSample},
		"zero length":   {[]byte{0, 0, 0, 0}, ErrSample},
		"past the end":  {[]byte{0, 0, 0, 9, 1, 2}, ErrSample},
		"length > int":  {[]byte{0xff, 0xff, 0xff, 0xff, 1}, ErrSample},
		"trailing junk": {[]byte{0, 0, 0, 1, 9, 0}, ErrSample},
	}
	for name, c := range cases {
		if _, err := d.DecodeSample(c.data, 0); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if _, err := d.DecodeAnnexB(make([]byte, 17)); !errors.Is(err, ErrOverLimit) {
		t.Errorf("annexb oversize: %v", err)
	}
}

func TestParseAVCC(t *testing.T) {
	sps := []byte{0x67, 66, 0xc0, 10, 1, 2, 3}
	pps := []byte{0x68, 9}
	good := avcC([][]byte{sps}, [][]byte{pps}, 4, false)
	c, err := ParseAVCC(good)
	if err != nil {
		t.Fatal(err)
	}
	if c.Profile != 66 || c.Compatibility != 0xc0 || c.Level != 10 || c.LengthSize != 4 ||
		len(c.SPS) != 1 || len(c.PPS) != 1 || string(c.SPS[0]) != string(sps) || string(c.PPS[0]) != string(pps) {
		t.Fatalf("parsed %+v", c)
	}
	high := []byte{0x67, 100, 0, 30, 1}
	if _, err := ParseAVCC(avcC([][]byte{high}, [][]byte{pps}, 1, true)); err != nil {
		t.Fatalf("high record: %v", err)
	}
	// A High record that stops before the extension fields is tolerated.
	if _, err := ParseAVCC(avcC([][]byte{high}, [][]byte{pps}, 1, false)); err != nil {
		t.Fatalf("high record without extension: %v", err)
	}
	extLen := func(b []byte) []byte { return append(b[:len(b)-1:len(b)-1], 1, 0, 5, 1) }
	bad := map[string][]byte{
		"short":           good[:6],
		"version":         append([]byte{2}, good[1:]...),
		"length size 3":   append([]byte{1, 66, 0xc0, 10, 0xfe}, good[5:]...),
		"cut in SPS len":  good[:7],
		"SPS past end":    good[:8],
		"SPS zero length": {1, 66, 0, 10, 0xff, 0xe1, 0, 0, 1},
		"no PPS count":    good[:6+2+len(sps)],
		"PPS cut":         good[:len(good)-1],
		"extension cut":   extLen(avcC([][]byte{high}, [][]byte{pps}, 1, true)),
		"zero PPS length": append(append([]byte(nil), good[:6+2+len(sps)]...), 1, 0, 0),
	}
	for name, b := range bad {
		if _, err := ParseAVCC(b); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := NewAVCC(good[:3], Config{}); !errors.Is(err, ErrConfig) {
		t.Errorf("NewAVCC: %v", err)
	}
}

func TestParameterSetRefusals(t *testing.T) {
	if _, err := newWithSets(Config{}, 4, [][]byte{{}}, nil); !errors.Is(err, ErrConfig) {
		t.Errorf("empty set: %v", err)
	}
	// An SPS that cannot be parsed is the decoder's error, passed through.
	if _, err := newWithSets(Config{}, 4, [][]byte{{0x67, 0xff}}, nil); err == nil {
		t.Error("a truncated SPS was accepted")
	}
}

// sps writes a Main profile sequence parameter set (H.264 section 7.3.2.1.1)
// for a picture of w x h macroblocks.
func sps(w, h int, frameMbsOnly bool) []byte {
	var b bitWriter
	b.bits(77, 8)
	b.bits(0, 8)
	b.bits(40, 8)
	b.ue(0) // seq_parameter_set_id
	b.ue(0) // log2_max_frame_num_minus4
	b.ue(0) // pic_order_cnt_type
	b.ue(0) // log2_max_pic_order_cnt_lsb_minus4
	b.ue(1) // max_num_ref_frames
	b.bits(0, 1)
	b.ue(uint(w - 1))
	b.ue(uint(h - 1))
	if frameMbsOnly {
		b.bits(1, 1)
	} else {
		b.bits(0, 1)
		b.bits(0, 1) // mb_adaptive_frame_field_flag
	}
	b.bits(1, 1) // direct_8x8_inference_flag
	b.bits(0, 1) // frame_cropping_flag
	b.bits(0, 1) // vui_parameters_present_flag
	b.bits(1, 1) // rbsp_stop_one_bit
	return append([]byte{0x67}, b.bytes()...)
}

type bitWriter struct {
	buf  []byte
	nbit int
}

func (w *bitWriter) bits(v uint, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.nbit%8 == 0 {
			w.buf = append(w.buf, 0)
		}
		w.buf[len(w.buf)-1] |= byte(v>>uint(i)&1) << (7 - w.nbit%8)
		w.nbit++
	}
}

func (w *bitWriter) ue(v uint) {
	v++
	n := 0
	for x := v; x > 1; x >>= 1 {
		n++
	}
	w.bits(0, n)
	w.bits(v, n+1)
}

func (w *bitWriter) bytes() []byte { return w.buf }

var errAny = errors.New("any refusal")

func TestUnsupportedAndOversize(t *testing.T) {
	cases := map[string]struct {
		sps  []byte
		want error
	}{
		"interlaced":   {sps(11, 9, false), ErrUnsupported},
		"too many MBs": {sps(1024, 1024, true), ErrOverLimit},
		// Past the decoder's own ceiling the SPS parser refuses it as out of
		// range, before any limit of ours is consulted.
		"level ceiling": {sps(4096, 4096, true), errAny},
		"just inside":   {sps(192, 192, true), nil},
	}
	for name, c := range cases {
		_, err := newWithSets(Config{}, 4, [][]byte{c.sps}, nil)
		if c.want == nil {
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if c.want == errAny && err != nil {
			continue
		}
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if _, err := newWithSets(Config{EnforceLevel: true}, 4, [][]byte{sps(192, 192, true)}, nil); !errors.Is(err, ErrOverLimit) {
		t.Errorf("a level 4.0 stream of 36864 macroblocks passed EnforceLevel: %v", err)
	}
}

// A bomb is a stream whose parameter set sizes the buffers: it has to be
// refused before they exist. The heap is capped, and what the refusals together
// allocate is measured.
func TestBombRefusedUnderMemoryLimit(t *testing.T) {
	old := debug.SetMemoryLimit(256 << 20)
	defer debug.SetMemoryLimit(old)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for _, s := range [][]byte{sps(1024, 1024, true), sps(8192, 8192, true), sps(1<<14, 1<<14, true)} {
		d, err := New(Config{}, 4)
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.DecodeSample(prefixed([][]byte{s}, 4), 0)
		if err == nil {
			t.Fatalf("bomb of %d bytes was accepted", len(s))
		}
		d.Close()
	}
	runtime.ReadMemStats(&after)
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 8<<20 {
		t.Fatalf("refusing three bombs allocated %d bytes", grown)
	}
}

func TestClassify(t *testing.T) {
	if classify(nil) != nil {
		t.Error("nil")
	}
	plain := errors.New("boom")
	if got := classify(plain); got != plain {
		t.Errorf("plain: %v", got)
	}
	if got := classify(errors.New("x: unsupported bitstream feature: y")); !errors.Is(got, ErrUnsupported) || !strings.Contains(got.Error(), "y") {
		t.Errorf("unsupported: %v", got)
	}
}

func TestDefaults(t *testing.T) {
	var c Config
	if c.frameMBs() != DefaultMaxFrameMBs || c.sampleBytes() != DefaultMaxSampleBytes {
		t.Error("defaults")
	}
	c = Config{MaxFrameMBs: 5, MaxSampleBytes: 6}
	if c.frameMBs() != 5 || c.sampleBytes() != 6 {
		t.Error("explicit")
	}
}
