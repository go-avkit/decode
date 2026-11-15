// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// pcmC is the record of an ipcm or fpcm entry (ISO/IEC 23003-5 5.1).
func pcmC(little bool, bits byte) []byte {
	var f byte
	if little {
		f = 1
	}
	return []byte{0, 0, 0, 0, f, bits}
}

func pcmTrack(codec string, channels int, conf []byte) container.TrackConfig {
	return container.TrackConfig{Kind: container.Audio, Codec: codec, Channels: channels, SampleRate: 8000, CodecConfig: conf}
}

// TestPCMWAVFiles: WAVE files, which are ipcm, fpcm and 'raw ' tracks, decode to
// what ffmpeg decodes them to, sample for sample (the .raw files). Reported from
// go-perceptual: DecodeAudioTrack refused every one of them.
func TestPCMWAVFiles(t *testing.T) {
	for _, tc := range []struct {
		file     string
		codec    string
		format   SampleFormat
		channels int
	}{
		{"pcm_s16_stereo.wav", "ipcm", SampleS16, 2},
		{"pcm_s24_mono.wav", "ipcm", SampleS32, 1},
		{"pcm_s32_3ch.wav", "ipcm", SampleS32, 3},
		{"pcm_f32_stereo.wav", "fpcm", SampleF32, 2},
		{"pcm_f64_mono.wav", "fpcm", SampleF32, 1},
		{"pcm_u8_mono.wav", "raw ", SampleS16, 1},
	} {
		t.Run(tc.file, func(t *testing.T) {
			r, id, want := audioFixture(t, tc.file)
			cfg, err := r.TrackConfig(id)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Codec != tc.codec {
				t.Fatalf("the container states %q, want %q", cfg.Codec, tc.codec)
			}
			as, err := DecodeAudioTrack(r, id, AudioConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if len(as) == 0 || as[0].Format != tc.format || as[0].Channels != tc.channels || as[0].SampleRate != 8000 {
				t.Fatalf("first buffer %+v, want %v, %d channels, 8000 Hz", as, tc.format, tc.channels)
			}
			if got := pcmBytes(t, as); !bytes.Equal(got, want) {
				t.Fatalf("%d bytes differ from ffmpeg's %d", len(got), len(want))
			}
		})
	}
}

// decodePCM runs one sample of a hand-built track.
func decodePCM(t *testing.T, tc container.TrackConfig, data []byte) Audio {
	t.Helper()
	d, err := NewAudioTrack(tc, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	as, err := d.DecodeSample(container.Sample{Data: data})
	if err != nil || len(as) != 1 {
		t.Fatalf("%v, %d buffers", err, len(as))
	}
	return as[0]
}

func TestPCMConversions(t *testing.T) {
	f64 := func(v float64) []byte { return binary.LittleEndian.AppendUint64(nil, math.Float64bits(v)) }
	f64be := func(v float64) []byte { return binary.BigEndian.AppendUint64(nil, math.Float64bits(v)) }
	third := 1.0 / 3
	for name, tc := range map[string]struct {
		track  container.TrackConfig
		in     []byte
		format SampleFormat
		out    []byte
	}{
		"s16 little":     {pcmTrack("ipcm", 1, pcmC(true, 16)), []byte{0x01, 0x80, 0xFF, 0x7F}, SampleS16, []byte{0x01, 0x80, 0xFF, 0x7F}},
		"s16 big":        {pcmTrack("ipcm", 1, pcmC(false, 16)), []byte{0x80, 0x01, 0x7F, 0xFF}, SampleS16, []byte{0x01, 0x80, 0xFF, 0x7F}},
		"s8 signed":      {pcmTrack("ipcm", 1, pcmC(true, 8)), []byte{0x80, 0xFF, 0, 0x7F}, SampleS16, []byte{0x00, 0x80, 0x00, 0xFF, 0, 0, 0x00, 0x7F}},
		"s24 negative":   {pcmTrack("ipcm", 1, pcmC(true, 24)), []byte{0xFF, 0xFF, 0xFF, 0x00, 0x00, 0x80}, SampleS32, []byte{0x00, 0xFF, 0xFF, 0xFF, 0x00, 0x00, 0x00, 0x80}},
		"s24 big":        {pcmTrack("ipcm", 1, pcmC(false, 24)), []byte{0x80, 0x00, 0x01}, SampleS32, []byte{0x00, 0x01, 0x00, 0x80}},
		"s32 big":        {pcmTrack("ipcm", 1, pcmC(false, 32)), []byte{0x80, 0, 0, 1}, SampleS32, []byte{1, 0, 0, 0x80}},
		"f32 big":        {pcmTrack("fpcm", 1, pcmC(false, 32)), []byte{0x3F, 0x80, 0, 0}, SampleF32, []byte{0, 0, 0x80, 0x3F}},
		"f64 rounds":     {pcmTrack("fpcm", 1, pcmC(true, 64)), f64(third), SampleF32, binary.LittleEndian.AppendUint32(nil, math.Float32bits(float32(third)))},
		"f64 big rounds": {pcmTrack("fpcm", 1, pcmC(false, 64)), f64be(-third), SampleF32, binary.LittleEndian.AppendUint32(nil, math.Float32bits(float32(-third)))},
		"u8 silence":     {pcmTrack("raw ", 1, nil), []byte{128, 0, 255}, SampleS16, []byte{0, 0, 0x00, 0x80, 0x00, 0x7F}},
	} {
		t.Run(name, func(t *testing.T) {
			a := decodePCM(t, tc.track, tc.in)
			if a.Format != tc.format || !bytes.Equal(a.Data, tc.out) {
				t.Fatalf("%v % x, want %v % x", a.Format, a.Data, tc.format, tc.out)
			}
		})
	}
}

// TestPCMNeedsItsWidthFromTheContainer: the width is the pcmC record's, never a
// guess from the sample's length, so an entry without it, or with a width that is
// not converted, is refused.
func TestPCMNeedsItsWidthFromTheContainer(t *testing.T) {
	for name, tc := range map[string]struct {
		track container.TrackConfig
		want  error
	}{
		"ipcm without pcmC":  {pcmTrack("ipcm", 2, nil), ErrAudioConfig},
		"fpcm short pcmC":    {pcmTrack("fpcm", 2, []byte{0, 0, 0, 0, 1}), ErrAudioConfig},
		"ipcm 12 bits":       {pcmTrack("ipcm", 2, pcmC(true, 12)), ErrAudioUnsupported},
		"ipcm 64 bits":       {pcmTrack("ipcm", 2, pcmC(true, 64)), ErrAudioUnsupported},
		"fpcm 16 bits":       {pcmTrack("fpcm", 2, pcmC(true, 16)), ErrAudioUnsupported},
		"no channels":        {pcmTrack("ipcm", 0, pcmC(true, 16)), ErrAudioConfig},
		"no rate":            {container.TrackConfig{Codec: "ipcm", Channels: 1, CodecConfig: pcmC(true, 16)}, ErrAudioConfig},
		"too many channels":  {pcmTrack("ipcm", 9, pcmC(true, 16)), ErrOverLimit},
		"raw with no rate":   {container.TrackConfig{Codec: "raw ", Channels: 1}, ErrAudioConfig},
		"raw with 0 channel": {pcmTrack("raw ", 0, nil), ErrAudioConfig},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := AudioConfig{}
			if tc.want == ErrOverLimit {
				cfg.MaxChannels = 8
			}
			if _, err := NewAudioTrack(tc.track, cfg); !errors.Is(err, tc.want) {
				t.Fatalf("%v, want %v", err, tc.want)
			}
		})
	}
}

func TestPCMSampleMustBeWholeFrames(t *testing.T) {
	d, err := NewAudioTrack(pcmTrack("ipcm", 2, pcmC(true, 16)), AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DecodeSample(container.Sample{Data: make([]byte, 6)}); !errors.Is(err, ErrAudioSample) {
		t.Fatalf("%v, want ErrAudioSample", err)
	}
	if as, err := d.DecodeSample(container.Sample{}); err != nil || len(as) != 0 {
		t.Fatalf("an empty sample: %v, %d buffers", err, len(as))
	}
}

// FuzzPCMSample: whatever pcmC record and samples a track states, the decoder
// returns an error or buffers whose size follows from the format and the input.
func FuzzPCMSample(f *testing.F) {
	f.Fuzz(func(t *testing.T, kind uint8, channels uint8, conf, data []byte) {
		codec := [...]string{"ipcm", "fpcm", "raw "}[int(kind)%3]
		d, err := NewAudioTrack(pcmTrack(codec, int(channels), conf), AudioConfig{})
		if err != nil {
			return
		}
		as, err := d.DecodeSample(container.Sample{Data: data})
		if err != nil {
			return
		}
		var n int
		for _, a := range as {
			n += a.Samples() * a.Channels
			if len(a.Data) != a.Samples()*a.Channels*a.Format.Size() || a.Channels != int(channels) {
				t.Fatalf("buffer of %d bytes, %d channels, %v", len(a.Data), a.Channels, a.Format)
			}
		}
		if len(data) > 0 && n == 0 {
			t.Fatalf("%d input bytes gave %d samples", len(data), n)
		}
	})
}
