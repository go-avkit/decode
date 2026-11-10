// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"testing"

	"github.com/go-avkit/avkit/container"
)

func TestMP3Config(t *testing.T) {
	good := container.TrackConfig{Codec: "mp3", SampleRate: 44100, Channels: 2, Timescale: 44100}
	if d, err := NewAudioTrack(good, AudioConfig{}); err != nil || d == nil {
		t.Fatalf("a good configuration: %v", err)
	}
	for name, c := range map[string]struct {
		mod  func(*container.TrackConfig)
		cfg  AudioConfig
		want error
	}{
		"no rate":        {func(c *container.TrackConfig) { c.SampleRate = 0 }, AudioConfig{}, ErrAudioConfig},
		"no channels":    {func(c *container.TrackConfig) { c.Channels = 0 }, AudioConfig{}, ErrAudioConfig},
		"three channels": {func(c *container.TrackConfig) { c.Channels = 3 }, AudioConfig{MaxChannels: 8}, ErrAudioConfig},
		"over channels":  {func(c *container.TrackConfig) {}, AudioConfig{MaxChannels: 1}, ErrOverLimit},
		"over block":     {func(c *container.TrackConfig) {}, AudioConfig{MaxBlockSamples: 1000}, ErrOverLimit},
	} {
		tc := good
		c.mod(&tc)
		if _, err := NewAudioTrack(tc, c.cfg); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

// TestMP2IsRefused: Layer II is refused by its name when the track says so, and by
// the frame when a track that says MP3 carries it.
func TestMP2IsRefused(t *testing.T) {
	r, id, _ := audioFixture(t, "mp2_stereo.mp2")
	tc, err := r.TrackConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewAudioTrack(tc, AudioConfig{}); !errors.Is(err, ErrAudioUnsupported) {
		t.Fatalf("track %q: %v, want ErrAudioUnsupported", tc.Codec, err)
	}
	samples, err := r.Samples(id)
	if err != nil {
		t.Fatal(err)
	}
	tc.Codec, tc.EditList = "mp3", nil
	d, err := NewAudioTrack(tc, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DecodeSample(samples[0]); !errors.Is(err, ErrAudioUnsupported) {
		t.Fatalf("a Layer II frame: %v, want ErrAudioUnsupported", err)
	}
}

func TestMP3FrameChecks(t *testing.T) {
	r, id, _ := audioFixture(t, "mp3_stereo.mka")
	tc, _ := r.TrackConfig(id)
	samples, _ := r.Samples(id)

	// A frame at another rate than the track's.
	other := tc
	other.SampleRate = 48000
	d, err := NewAudioTrack(other, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var got error
	for _, s := range samples {
		if _, got = d.DecodeSample(s); got != nil {
			break
		}
	}
	if !errors.Is(got, ErrAudioSample) {
		t.Fatalf("a frame at 44100 Hz in a 48000 Hz track: %v", got)
	}

	// Bytes with no frame header, and nothing at all, decode to nothing.
	d, _ = NewAudioTrack(tc, AudioConfig{})
	for _, data := range [][]byte{nil, {1, 2, 3, 4, 5, 6, 7, 8}} {
		if as, err := d.DecodeSample(container.Sample{Data: data}); err != nil || len(as) != 0 {
			t.Fatalf("%v: %d buffers, %v", data, len(as), err)
		}
	}
	if as, err := d.Flush(); err != nil || len(as) != 0 {
		t.Fatalf("flush: %d buffers, %v", len(as), err)
	}
}

// TestMP3End: with no edit list the track ends where its samples' durations say,
// less the delay; a track with no timescale states none.
func TestMP3End(t *testing.T) {
	r, id, _ := audioFixture(t, "mp3_stereo.mka")
	tc, _ := r.TrackConfig(id)
	samples, _ := r.Samples(id)
	c, err := newMP3(tc, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.end(tc, samples); got < 17000 || got > 18000 {
		t.Fatalf("end %d samples", got)
	}
	// A delay longer than the track leaves nothing.
	d := c.(*mp3Decoder)
	d.delay = 1 << 40
	if got := c.end(tc, samples); got != 0 {
		t.Fatalf("end %d samples after a huge delay", got)
	}
	if got := c.priming(); got != 1<<31-1 {
		t.Fatalf("priming %d", got)
	}
	tc.Timescale = 0
	if got := c.end(tc, samples); got != -1 {
		t.Fatalf("end %d with no timescale", got)
	}
	tc.Timescale, tc.EditList = 1000, &container.EditList{}
	if got := c.end(tc, samples); got != -1 {
		t.Fatalf("end %d with an edit list", got)
	}
}
