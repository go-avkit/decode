// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// opusHead builds an identification header (RFC 7845 5.1).
func opusHead(channels int, preSkip uint16, gain int16, family byte) []byte {
	h := make([]byte, 19)
	copy(h, "OpusHead")
	h[8], h[9] = 1, byte(channels)
	binary.LittleEndian.PutUint16(h[10:], preSkip)
	binary.LittleEndian.PutUint32(h[12:], 48000)
	binary.LittleEndian.PutUint16(h[16:], uint16(gain))
	h[18] = family
	return h
}

func opusTrack(h []byte) container.TrackConfig {
	return container.TrackConfig{Codec: "Opus", CodecConfig: h, SampleRate: 48000, Channels: 2, Timescale: 48000}
}

func TestOpusConfig(t *testing.T) {
	if d, err := NewAudioTrack(opusTrack(opusHead(2, 312, 0, 0)), AudioConfig{}); err != nil || d == nil {
		t.Fatalf("a good header: %v", err)
	}
	badVersion := opusHead(2, 0, 0, 0)
	badVersion[8] = 0x10
	badMagic := opusHead(2, 0, 0, 0)
	badMagic[0] = 'X'
	for name, c := range map[string]struct {
		head []byte
		cfg  AudioConfig
		want error
	}{
		"empty":           {nil, AudioConfig{}, ErrAudioConfig},
		"short":           {opusHead(2, 0, 0, 0)[:18], AudioConfig{}, ErrAudioConfig},
		"magic":           {badMagic, AudioConfig{}, ErrAudioConfig},
		"version":         {badVersion, AudioConfig{}, ErrAudioConfig},
		"no channels":     {opusHead(0, 0, 0, 0), AudioConfig{}, ErrAudioConfig},
		"family 1":        {opusHead(6, 0, 0, 1), AudioConfig{}, ErrAudioUnsupported},
		"family 255":      {opusHead(255, 0, 0, 255), AudioConfig{}, ErrAudioUnsupported},
		"three channels":  {opusHead(3, 0, 0, 0), AudioConfig{}, ErrAudioConfig},
		"over channels":   {opusHead(2, 0, 0, 0), AudioConfig{MaxChannels: 1}, ErrOverLimit},
		"over block size": {opusHead(2, 0, 0, 0), AudioConfig{MaxBlockSamples: 2880}, ErrOverLimit},
	} {
		if _, err := NewAudioTrack(opusTrack(c.head), c.cfg); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

// TestOpusSurroundIsRefused: a 5.1 stream (mapping family 1) made by libopus is
// refused by name, not decoded as stereo.
func TestOpusSurroundIsRefused(t *testing.T) {
	r, id, _ := audioFixture(t, "opus_51.ogg")
	if _, err := DecodeAudioTrack(r, id, AudioConfig{}); !errors.Is(err, ErrAudioUnsupported) {
		t.Fatalf("%v, want ErrAudioUnsupported", err)
	}
}

// TestOpusChannelBomb: a header that states 255 channels is refused before anything
// is allocated for them, under a memory limit.
func TestOpusChannelBomb(t *testing.T) {
	old := debug.SetMemoryLimit(64 << 20)
	defer debug.SetMemoryLimit(old)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 100 {
		if _, err := NewAudioTrack(opusTrack(opusHead(255, 0, 0, 1)), AudioConfig{}); !errors.Is(err, ErrAudioUnsupported) {
			t.Fatalf("%v", err)
		}
		if _, err := NewAudioTrack(opusTrack(opusHead(255, 0, 0, 0)), AudioConfig{}); !errors.Is(err, ErrAudioConfig) {
			t.Fatalf("%v", err)
		}
	}
	runtime.ReadMemStats(&after)
	if grown := int64(after.TotalAlloc) - int64(before.TotalAlloc); grown > 1<<20 {
		t.Fatalf("refusals allocated %d bytes", grown)
	}
}

// TestOpusGain: the output gain of the header, a Q7.8 number of dB, scales the PCM
// as ffmpeg's decoder does. 1541/256 dB is a factor of two.
func TestOpusGain(t *testing.T) {
	r, id, _ := audioFixture(t, "opus_stereo.mka")
	tc, _ := r.TrackConfig(id)
	samples, _ := r.Samples(id)
	plain, err := decodeAudio(tc, samples, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	loud := tc
	loud.CodecConfig = bytes.Clone(tc.CodecConfig)
	binary.LittleEndian.PutUint16(loud.CodecConfig[16:], 1541)
	boosted, err := decodeAudio(loud, samples, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	a, b := pcmBytes(t, plain), pcmBytes(t, boosted)
	if len(a) != len(b) || len(a) == 0 {
		t.Fatalf("%d bytes against %d", len(a), len(b))
	}
	gain := float32(math.Pow(10, 1541/(20*256.0)))
	for i := 0; i < len(a); i += 4 {
		x := math.Float32frombits(binary.LittleEndian.Uint32(a[i:]))
		y := math.Float32frombits(binary.LittleEndian.Uint32(b[i:]))
		if y != x*gain {
			t.Fatalf("sample %d: %v, want %v", i/4, y, x*gain)
		}
	}
	if gain < 1.99 || gain > 2.01 {
		t.Fatalf("gain %v", gain)
	}
}

func TestOpusPacketErrors(t *testing.T) {
	d, err := NewAudioTrack(opusTrack(opusHead(2, 0, 0, 0)), AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"empty":                {},
		"code 3 no count":      {0x03},
		"code 0 over 1275":     append([]byte{0x00}, make([]byte, 1300)...),
		"code 3 zero frames":   {0x03, 0x00},
		"code 2 length beyond": {0x02, 0xFF, 0xFF},
	} {
		if _, err := d.DecodeSample(container.Sample{Data: data}); !errors.Is(err, ErrAudioSample) {
			t.Errorf("%s: %v, want ErrAudioSample", name, err)
		}
	}
	if as, err := d.Flush(); err != nil || len(as) != 0 {
		t.Fatalf("flush: %d buffers, %v", len(as), err)
	}
}

// TestOpusEnd: with no edit list the track ends where the samples' durations say,
// less the pre-skip; an edit list or no timescale states no end of this kind.
func TestOpusEnd(t *testing.T) {
	r, id, _ := audioFixture(t, "opus_stereo_cbr.ogg")
	tc, _ := r.TrackConfig(id)
	samples, _ := r.Samples(id)
	c, err := newOpus(tc, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.end(tc, samples); got != 19200 {
		t.Fatalf("end %d samples, want 19200", got)
	}
	tc.Timescale = 0
	if got := c.end(tc, samples); got != -1 {
		t.Fatalf("end %d with no timescale", got)
	}
	tc.Timescale, tc.EditList = 48000, &container.EditList{}
	if got := c.end(tc, samples); got != -1 {
		t.Fatalf("end %d with an edit list", got)
	}
	// A pre-skip longer than the track leaves nothing.
	short := []container.Sample{{Duration: 100}}
	tc.EditList = nil
	if got := c.end(tc, short); got != 0 {
		t.Fatalf("end %d for 100 samples under a pre-skip of 312", got)
	}
}
