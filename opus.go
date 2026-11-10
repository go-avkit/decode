// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/go-avkit/avkit/container"
	"github.com/pion/opus"
)

// Opus (RFC 6716) through github.com/pion/opus (MIT, pure Go): SILK, CELT and hybrid
// packets, mono and stereo (channel mapping family 0). A stream of more channels
// (mapping family 1 or 255) is ErrAudioUnsupported: the library decodes one or two.
// The output is always 48 kHz (the decoder's, whatever rate the track was recorded
// at), float, with the pre-skip of the identification header dropped and the output
// gain it states applied, as ffmpeg's decoder does.

func init() {
	audioDecoders["Opus"] = newOpus
}

const (
	opusRate      = 48000
	opusMaxFrame  = 5760 // samples per channel in 120 ms, the longest packet (RFC 6716 3.4 R5)
	opusHeadMin   = 19
	opusHeadMagic = "OpusHead"
)

type opusDecoder struct {
	dec      opus.Decoder
	channels int
	preSkip  int
	gain     float32 // linear; 1 for none
	pcm      []float32
}

// newOpus reads the identification header of RFC 7845 5.1 from tc.CodecConfig:
// what a Matroska CodecPrivate holds, what an Ogg stream opens with, and what
// avkit rebuilds from an MP4's dOps box.
func newOpus(tc container.TrackConfig, cfg AudioConfig) (audioCodec, error) {
	h := tc.CodecConfig
	if len(h) < opusHeadMin || string(h[:8]) != opusHeadMagic || h[8]>>4 != 0 {
		return nil, fmt.Errorf("%w: not an Opus identification header", ErrAudioConfig)
	}
	d := &opusDecoder{
		channels: int(h[9]),
		preSkip:  int(binary.LittleEndian.Uint16(h[10:])),
		gain:     1,
	}
	// The output gain is a signed Q7.8 number of dB (RFC 7845 5.1).
	if q := int16(binary.LittleEndian.Uint16(h[16:])); q != 0 {
		d.gain = float32(math.Pow(10, float64(q)/(20*256)))
	}
	switch {
	case d.channels == 0:
		return nil, fmt.Errorf("%w: an Opus header of no channels", ErrAudioConfig)
	case h[18] != 0:
		return nil, fmt.Errorf("%w: Opus channel mapping family %d, only family 0 (mono and stereo) is decoded", ErrAudioUnsupported, h[18])
	case d.channels > 2:
		return nil, fmt.Errorf("%w: %d channels with mapping family 0", ErrAudioConfig, d.channels)
	case d.channels > cfg.channels():
		return nil, fmt.Errorf("%w: %d channels, the limit is %d", ErrOverLimit, d.channels, cfg.channels())
	case opusMaxFrame > cfg.block():
		return nil, fmt.Errorf("%w: packets of %d samples, the limit is %d", ErrOverLimit, opusMaxFrame, cfg.block())
	}
	// Init refuses only a rate other than 8, 12, 16, 24 or 48 kHz and a channel
	// count other than 1 or 2, and both are checked above.
	_ = d.dec.Init(opusRate, d.channels)
	d.pcm = make([]float32, opusMaxFrame*d.channels)
	return d, nil
}

func (d *opusDecoder) priming() int    { return d.preSkip }
func (d *opusDecoder) sampleRate() int { return opusRate }
func (d *opusDecoder) flush() ([]Audio, error) {
	return nil, nil
}

// end: with no edit list, the track ends where its samples' durations say, less the
// pre-skip. An Ogg stream states the end to the sample (the last duration is cut to
// the granule position, RFC 7845 4.4); a Matroska one to the millisecond.
func (d *opusDecoder) end(tc container.TrackConfig, samples []container.Sample) int64 {
	if tc.EditList != nil || tc.Timescale == 0 {
		return -1
	}
	var total int64
	for _, s := range samples {
		total += int64(s.Duration)
	}
	return max(rescale(total, opusRate, int64(tc.Timescale))-int64(d.preSkip), 0)
}

func (d *opusDecoder) decode(data []byte) ([]Audio, error) {
	n, err := d.dec.DecodeToFloat32(data, d.pcm)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAudioSample, err)
	}
	// n is at most opusMaxFrame: the library refuses a packet that does not fit
	// the buffer, which is that long.
	a := Audio{Format: SampleF32, SampleRate: opusRate, Channels: d.channels, Layout: LayoutStereo, Data: make([]byte, n*d.channels*4)}
	if d.channels == 1 {
		a.Layout = LayoutMono
	}
	for i, s := range d.pcm[:n*d.channels] {
		binary.LittleEndian.PutUint32(a.Data[i*4:], math.Float32bits(s*d.gain))
	}
	return []Audio{a}, nil
}
