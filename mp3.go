// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"

	"github.com/go-avkit/avkit/container"
	mp3 "github.com/tphakala/go-mp3"
)

// MPEG-1/2/2.5 Layer III through github.com/tphakala/go-mp3 (MIT, pure Go, a port of
// minimp3, which is public domain). It decodes Layer III only: a Layer II (MP2) or
// Layer I frame is ErrAudioUnsupported, and no pure Go MP2 decoder under a licence
// compatible with this package was found (hajimehoshi/go-mp3 derives from PDMP3,
// which is LGPL, and decodes Layer III only too).

func init() {
	audioDecoders["mp3"] = newMP3
}

// mp3SamplesPerFrame is the most a Layer III frame holds per channel (MPEG-1).
const mp3SamplesPerFrame = 1152

type mp3Decoder struct {
	dec  *mp3.Decoder
	rate int
	pcm  []float32
	// delay is the encoder delay a Matroska track states, in samples.
	delay int64
}

func newMP3(tc container.TrackConfig, cfg AudioConfig) (audioCodec, error) {
	switch {
	case tc.SampleRate <= 0:
		return nil, fmt.Errorf("%w: an MP3 track needs its sample rate", ErrAudioConfig)
	case tc.Channels < 1 || tc.Channels > 2:
		return nil, fmt.Errorf("%w: MP3 has one or two channels, not %d", ErrAudioConfig, tc.Channels)
	case tc.Channels > cfg.channels():
		return nil, fmt.Errorf("%w: %d channels, the limit is %d", ErrOverLimit, tc.Channels, cfg.channels())
	case mp3SamplesPerFrame > cfg.block():
		return nil, fmt.Errorf("%w: frames of %d samples, the limit is %d", ErrOverLimit, mp3SamplesPerFrame, cfg.block())
	}
	d := &mp3Decoder{dec: mp3.NewDecoder(), rate: tc.SampleRate, pcm: make([]float32, mp3SamplesPerFrame*2)}
	if tc.EditList == nil {
		d.delay = rescale(int64(tc.StartDelay), int64(tc.SampleRate), int64(time.Second))
	}
	return d, nil
}

// priming: a track with an edit list is trimmed by it. One without, from Matroska,
// states the encoder's delay as CodecDelay (avkit's StartDelay), which ffmpeg
// skips for MP3 (measured: 1105 samples of an ffmpeg-made .mka are dropped).
func (d *mp3Decoder) priming() int    { return int(min(d.delay, math.MaxInt32)) }
func (d *mp3Decoder) sampleRate() int { return d.rate }
func (d *mp3Decoder) flush() ([]Audio, error) {
	return nil, nil
}

// end: with no edit list, the samples' durations. Matroska states times in
// milliseconds, so what remains of them after the delay is exact only to that
// resolution (about 44 samples at 44.1 kHz); the exact end is the DiscardPadding
// avkit folds into the last duration.
func (d *mp3Decoder) end(tc container.TrackConfig, samples []container.Sample) int64 {
	if tc.EditList != nil || tc.Timescale == 0 {
		return -1
	}
	var total int64
	for _, s := range samples {
		total += int64(s.Duration)
	}
	return max(rescale(total, int64(d.rate), int64(tc.Timescale))-d.delay, 0)
}

// decode decodes one container sample, which is one MPEG audio frame. A frame
// that yields no samples (the Xing or Info frame of a LAME stream, or the first
// frames whose bit reservoir points before the start) returns none, as ffmpeg's
// decoder does.
func (d *mp3Decoder) decode(data []byte) ([]Audio, error) {
	n, info, err := d.dec.DecodeFrame(data, d.pcm)
	if err != nil {
		return nil, fmt.Errorf("%w: Layer %d: %w", ErrAudioUnsupported, info.Layer, err)
	}
	if n == 0 {
		return nil, nil
	}
	if info.Channels < 1 || info.Channels > 2 || n > mp3SamplesPerFrame || info.SampleRate != d.rate {
		return nil, fmt.Errorf("%w: a frame of %d channels at %d Hz in a track at %d Hz", ErrAudioSample, info.Channels, info.SampleRate, d.rate)
	}
	a := Audio{Format: SampleF32, SampleRate: d.rate, Channels: info.Channels, Layout: LayoutStereo, Data: make([]byte, n*info.Channels*4)}
	if info.Channels == 1 {
		a.Layout = LayoutMono
	}
	for i, s := range d.pcm[:n*info.Channels] {
		binary.LittleEndian.PutUint32(a.Data[i*4:], math.Float32bits(s))
	}
	return []Audio{a}, nil
}
