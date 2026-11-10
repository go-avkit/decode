// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-avkit/avkit/container"
)

func TestAudioTypes(t *testing.T) {
	for f, want := range map[SampleFormat]int{SampleS16: 2, SampleS32: 4, SampleF32: 4, 0: 0, 9: 0} {
		if f.Size() != want {
			t.Errorf("%v: size %d, want %d", f, f.Size(), want)
		}
	}
	for f, want := range map[SampleFormat]string{SampleS16: "s16le", SampleS32: "s32le", SampleF32: "f32le", 9: "SampleFormat(9)"} {
		if f.String() != want {
			t.Errorf("%d is %q, want %q", uint8(f), f.String(), want)
		}
	}
	for l, want := range map[ChannelLayout]int{0: 0, LayoutMono: 1, LayoutStereo: 2, Layout51: 6, Layout61: 7, Layout71: 8} {
		if l.Channels() != want {
			t.Errorf("layout %#x: %d channels, want %d", uint32(l), l.Channels(), want)
		}
	}
	a := Audio{Data: make([]byte, 2400*4), Format: SampleS16, SampleRate: 48000, Channels: 2}
	if a.Samples() != 2400 || a.Duration() != 50*time.Millisecond {
		t.Errorf("%d samples, %v", a.Samples(), a.Duration())
	}
	for _, z := range []Audio{{Data: a.Data, Format: SampleS16}, {Data: a.Data, Channels: 2}} {
		if z.Samples() != 0 || z.Duration() != 0 {
			t.Errorf("%+v is not empty", z.Format)
		}
	}
}

func TestAudioConfigDefaults(t *testing.T) {
	var c AudioConfig
	if c.channels() != DefaultMaxAudioChannels || c.block() != DefaultMaxBlockSamples ||
		c.sampleBytes() != DefaultMaxSampleBytes || c.output() != DefaultMaxAudioOutput {
		t.Fatalf("defaults: %+v", c)
	}
	c = AudioConfig{MaxChannels: 1, MaxBlockSamples: 2, MaxSampleBytes: 3, MaxOutputBytes: 4}
	if c.channels() != 1 || c.block() != 2 || c.sampleBytes() != 3 || c.output() != 4 {
		t.Fatalf("explicit: %+v", c)
	}
}

// flacTrack returns the configuration and samples of a FLAC fixture.
func flacTrack(t *testing.T, name string) (container.TrackConfig, []container.Sample, []byte) {
	t.Helper()
	r, id, want := audioFixture(t, name)
	tc, err := r.TrackConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	samples, err := r.Samples(id)
	if err != nil {
		t.Fatal(err)
	}
	return tc, samples, want
}

func TestNoAudioTrack(t *testing.T) {
	r, err := container.NewReader(readFile(t, filepath.Join("testdata", "files", "hvc1.mp4")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FirstAudioTrack(r); !errors.Is(err, ErrNoAudio) {
		t.Fatalf("%v, want ErrNoAudio", err)
	}
	if _, err := DecodeAudioTrack(r, 99, AudioConfig{}); err == nil {
		t.Fatal("a track that does not exist decoded")
	}
}

func TestUnknownCodec(t *testing.T) {
	_, err := NewAudioTrack(container.TrackConfig{Codec: "zzzz"}, AudioConfig{})
	if !errors.Is(err, ErrAudioUnsupported) {
		t.Fatalf("%v, want ErrAudioUnsupported", err)
	}
}

func TestAudioDecoderLifecycle(t *testing.T) {
	tc, samples, _ := flacTrack(t, "flac_s16_stereo.mka")
	d, err := NewAudioTrack(tc, AudioConfig{MaxSampleBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DecodeSample(container.Sample{Data: make([]byte, 101)}); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("an oversized sample: %v", err)
	}
	if _, err := d.DecodeSample(container.Sample{Data: samples[0].Data[:3]}); !errors.Is(err, ErrAudioSample) {
		t.Fatalf("a truncated sample: %v", err)
	}
	if fs, err := d.Flush(); err != nil || len(fs) != 0 {
		t.Fatalf("flush: %v, %v", fs, err)
	}
	if d.Close() != nil || d.Close() != nil {
		t.Fatal("close failed")
	}
	if _, err := d.DecodeSample(container.Sample{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("decode after close: %v", err)
	}
	if _, err := d.Flush(); !errors.Is(err, ErrClosed) {
		t.Fatalf("flush after close: %v", err)
	}
}

// TestAudioStartTrim: the start of a track is dropped as its container states it,
// by the media time of its edit list, and the rest is numbered
// from zero. 1000 samples of a 44.1 kHz stereo track are 4000 bytes.
func TestAudioStartTrim(t *testing.T) {
	tc, samples, want := flacTrack(t, "flac_s16_stereo.mka")
	// 882 samples are 20 ms. 4410, 100 ms, is more than the 4096 of the first
	// frame, which is then dropped whole.
	for _, skip := range []int64{882, 4410} {
		for name, mod := range map[string]func(*container.TrackConfig){
			"edit list": func(c *container.TrackConfig) {
				c.EditList = &container.EditList{MovieTimescale: 1000, Edits: []container.Edit{
					{SegmentDuration: 50, MediaTime: container.EmptyEdit, Rate: 1 << 16},
					{SegmentDuration: 0, MediaTime: skip * int64(c.Timescale) / 44100, Rate: 1 << 16},
				}}
			},
		} {
			t.Run(fmt.Sprintf("%s %d", name, skip), func(t *testing.T) {
				c := tc
				mod(&c)
				as, err := decodeAudio(c, samples, AudioConfig{})
				if err != nil {
					t.Fatal(err)
				}
				if got := pcmBytes(t, as); !bytes.Equal(got, want[skip*4:]) {
					t.Fatalf("%d bytes, want the reference without its first %d samples (%d bytes)", len(got), skip, int64(len(want))-skip*4)
				}
			})
		}
	}
}

// TestStartDelayAloneDropsNothing: a Matroska CodecDelay is a timestamp shift in
// ffmpeg, and a track that states one without an edit list is decoded whole.
func TestStartDelayAloneDropsNothing(t *testing.T) {
	tc, samples, want := flacTrack(t, "flac_s16_stereo.mka")
	tc.StartDelay, tc.EditList = 20*time.Millisecond, nil
	as, err := decodeAudio(tc, samples, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := pcmBytes(t, as); !bytes.Equal(got, want) {
		t.Fatalf("%d bytes, want all %d", len(got), len(want))
	}
}

func TestEditSkip(t *testing.T) {
	tc := container.TrackConfig{SampleRate: 48000, Timescale: 48000}
	for name, c := range map[string]struct {
		el   *container.EditList
		ts   uint32
		want int64
	}{
		"only empty edits": {&container.EditList{Edits: []container.Edit{{MediaTime: container.EmptyEdit}}}, 48000, 0},
		"no timescale":     {&container.EditList{Edits: []container.Edit{{MediaTime: 5}}}, 0, 0},
		"negative":         {&container.EditList{Edits: []container.Edit{{MediaTime: -5}}}, 48000, 0},
		"media time":       {&container.EditList{Edits: []container.Edit{{MediaTime: 1105}}}, 48000, 1105},
		"rescaled":         {&container.EditList{Edits: []container.Edit{{MediaTime: 1000}}}, 1000, 48000},
	} {
		c2 := tc
		c2.Timescale, c2.EditList = c.ts, c.el
		if got := editSkip(c.el, c2, 48000); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}

func TestRescale(t *testing.T) {
	for _, c := range []struct{ v, num, den, want int64 }{
		{1000, 44100, 1000000, 44},           // 44.1 rounds down
		{1000, 48000, 1000000, 48},           // exact
		{1, 1, 2, 1},                         // a half rounds up
		{0, 5, 5, 0},                         // nothing
		{-1, 5, 5, 0},                        // a negative time
		{5, 0, 5, 0},                         // no rate
		{5, 5, 0, 0},                         // no timescale
		{math.MaxInt64, 3, 2, math.MaxInt64}, // beyond an int64
		{math.MaxInt64, math.MaxInt64, math.MaxInt64, math.MaxInt64},
		{1 << 40, 1 << 40, 1 << 41, 1 << 39}, // a 128-bit product
	} {
		if got := rescale(c.v, c.num, c.den); got != c.want {
			t.Errorf("rescale(%d, %d, %d) = %d, want %d", c.v, c.num, c.den, got, c.want)
		}
	}
}

func TestPresentedSamples(t *testing.T) {
	const big = uint64(1) << 62
	tc := container.TrackConfig{SampleRate: 1000}
	for name, c := range map[string]struct {
		el   *container.EditList
		want int64
	}{
		"none":             {nil, -1},
		"no movie scale":   {&container.EditList{Edits: []container.Edit{{SegmentDuration: 5}}}, -1},
		"open end":         {&container.EditList{MovieTimescale: 1000, Edits: []container.Edit{{MediaTime: 0}}}, -1},
		"one edit":         {&container.EditList{MovieTimescale: 100, Edits: []container.Edit{{SegmentDuration: 25}}}, 250},
		"empty ignored":    {&container.EditList{MovieTimescale: 100, Edits: []container.Edit{{SegmentDuration: 9, MediaTime: container.EmptyEdit}, {SegmentDuration: 25}}}, 250},
		"two edits":        {&container.EditList{MovieTimescale: 100, Edits: []container.Edit{{SegmentDuration: 25}, {SegmentDuration: 5}}}, 300},
		"one huge edit":    {&container.EditList{MovieTimescale: 1, Edits: []container.Edit{{SegmentDuration: big}}}, math.MaxInt64},
		"the sum wraps":    {&container.EditList{MovieTimescale: 1, Edits: []container.Edit{{SegmentDuration: big}, {SegmentDuration: big}}}, -1},
		"sum just fits":    {&container.EditList{MovieTimescale: 1, Edits: []container.Edit{{SegmentDuration: 3}, {SegmentDuration: 4}}}, 7000},
		"zero sample rate": {&container.EditList{MovieTimescale: 1, Edits: []container.Edit{{SegmentDuration: 3}}}, 0},
	} {
		c2 := tc
		if name == "zero sample rate" {
			c2.SampleRate = 0
		}
		c2.EditList = c.el
		if got := presentedSamples(c2, c2.SampleRate); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}

// TestAudioEndTrim: an edit list that ends before the media does cuts the last
// buffer, and drops one that lies wholly beyond it.
func TestAudioEndTrim(t *testing.T) {
	tc, samples, want := flacTrack(t, "flac_s16_stereo.mka")
	for _, keep := range []int64{1, 1000, 4096, 4097, 8000} {
		c := tc
		c.EditList = &container.EditList{MovieTimescale: 44100, Edits: []container.Edit{{SegmentDuration: uint64(keep)}}}
		as, err := decodeAudio(c, samples, AudioConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if got := pcmBytes(t, as); !bytes.Equal(got, want[:keep*4]) {
			t.Fatalf("keep %d: %d bytes, want %d", keep, len(got), keep*4)
		}
	}
}

func TestAudioOutputBound(t *testing.T) {
	tc, samples, _ := flacTrack(t, "flac_s16_stereo.mka")
	if _, err := decodeAudio(tc, samples, AudioConfig{MaxOutputBytes: 1 << 12}); !errors.Is(err, ErrOverLimit) {
		t.Fatalf("%v, want ErrOverLimit", err)
	}
}

func TestAudioDecodeErrorStops(t *testing.T) {
	tc, samples, want := flacTrack(t, "flac_s16_stereo.mka")
	bad := append([]container.Sample{samples[0], {Data: []byte{1, 2, 3}}}, samples[1:]...)
	as, err := decodeAudio(tc, bad, AudioConfig{})
	if !errors.Is(err, ErrAudioSample) {
		t.Fatalf("%v, want ErrAudioSample", err)
	}
	if got := pcmBytes(t, as); len(got) == 0 || !bytes.Equal(got, want[:len(got)]) {
		t.Fatalf("the buffers before the bad sample were not returned intact (%d bytes)", len(got))
	}
}
