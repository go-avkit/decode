// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/go-avkit/avkit/container"
)

// Linear PCM: the sample entries ipcm and fpcm of ISO/IEC 23003-5 and the 'raw '
// entry of unsigned 8-bit audio, which is what a WAVE file's tracks are called.
// There is nothing to decode, the samples are converted to the formats of this
// package and left-justified when they are shorter, as ffmpeg's PCM decoders
// output them (pcm_s16le to s16, pcm_s24le to s32, pcm_f64le to dbl).
//
// The width and the byte order are the container's: an ipcm or fpcm entry states
// them in its pcmC record, which is the track's CodecConfig (version and flags,
// format_flags whose low bit says little-endian, the sample size in bits). They
// are never inferred from the length of a sample or the duration of the track. An
// entry without its record is refused, and so is a width this package does not
// convert.
//
//	ipcm  8 bits   signed        -> SampleS16 (v << 8)
//	ipcm 16 bits                 -> SampleS16
//	ipcm 24 bits                 -> SampleS32 (v << 8: left-justified, full scale kept)
//	ipcm 32 bits                 -> SampleS32
//	fpcm 32 bits                 -> SampleF32
//	fpcm 64 bits                 -> SampleF32, rounded to the nearest single: LOSSY
//	'raw ' 8 bits  unsigned      -> SampleS16 ((v - 128) << 8: silence is 128)

func init() {
	audioDecoders["ipcm"] = newPCM
	audioDecoders["fpcm"] = newPCM
	audioDecoders["raw "] = newPCM
	audioDecoders["raw"] = newPCM
}

type pcmDecoder struct {
	rate, channels int
	width          int // bytes per input sample
	float, little  bool
	unsigned       bool
	layout         ChannelLayout
	format         SampleFormat
}

// newPCM reads the entry's width and byte order from its pcmC record.
func newPCM(tc container.TrackConfig, cfg AudioConfig) (audioCodec, error) {
	d := &pcmDecoder{rate: tc.SampleRate, channels: tc.Channels, little: true}
	var bits int
	switch tc.Codec {
	case "ipcm", "fpcm":
		c := tc.CodecConfig
		if len(c) < 6 {
			return nil, fmt.Errorf("%w: %s needs its pcmC record as CodecConfig, which states the sample size (got %d bytes)", ErrAudioConfig, tc.Codec, len(c))
		}
		d.little, bits = c[4]&1 != 0, int(c[5])
		d.float = tc.Codec == "fpcm"
		ok := bits == 8 || bits == 16 || bits == 24 || bits == 32
		if d.float {
			ok = bits == 32 || bits == 64
		}
		if !ok {
			return nil, fmt.Errorf("%w: %s samples of %d bits", ErrAudioUnsupported, tc.Codec, bits)
		}
	default: // 'raw '
		bits, d.unsigned = 8, true
	}
	d.width = bits / 8
	switch {
	case d.channels < 1:
		return nil, fmt.Errorf("%w: PCM track of %d channels", ErrAudioConfig, d.channels)
	case d.rate < 1:
		return nil, fmt.Errorf("%w: PCM track at %d Hz", ErrAudioConfig, d.rate)
	case d.channels > cfg.channels():
		return nil, fmt.Errorf("%w: %d channels, the limit is %d", ErrOverLimit, d.channels, cfg.channels())
	}
	switch {
	case d.float:
		d.format = SampleF32
	case d.width <= 2:
		d.format = SampleS16
	default:
		d.format = SampleS32
	}
	if d.channels < len(flacLayouts) {
		d.layout = flacLayouts[d.channels]
	}
	return d, nil
}

func (d *pcmDecoder) priming() int { return 0 }

func (d *pcmDecoder) sampleRate() int { return d.rate }

func (d *pcmDecoder) end(container.TrackConfig, []container.Sample) int64 { return -1 }

func (d *pcmDecoder) flush() ([]Audio, error) { return nil, nil }

// decode converts one container sample: a whole number of frames.
func (d *pcmDecoder) decode(data []byte) ([]Audio, error) {
	frame := d.channels * d.width
	if len(data) == 0 {
		return nil, nil
	}
	if len(data)%frame != 0 {
		return nil, fmt.Errorf("%w: PCM sample of %d bytes, a frame is %d", ErrAudioSample, len(data), frame)
	}
	n := len(data) / d.width
	a := Audio{SampleRate: d.rate, Channels: d.channels, Layout: d.layout, Format: d.format}
	a.Data = make([]byte, n*d.format.Size())
	order := binary.ByteOrder(binary.LittleEndian)
	if !d.little {
		order = binary.BigEndian
	}
	for i := range n {
		src := data[i*d.width:]
		switch {
		case d.float && d.width == 4:
			binary.LittleEndian.PutUint32(a.Data[i*4:], order.Uint32(src))
		case d.float:
			f := float32(math.Float64frombits(order.Uint64(src)))
			binary.LittleEndian.PutUint32(a.Data[i*4:], math.Float32bits(f))
		case d.unsigned:
			binary.LittleEndian.PutUint16(a.Data[i*2:], uint16(int16(int(src[0])-128)<<8))
		case d.width == 1:
			binary.LittleEndian.PutUint16(a.Data[i*2:], uint16(int16(int8(src[0]))<<8))
		case d.width == 2:
			binary.LittleEndian.PutUint16(a.Data[i*2:], order.Uint16(src))
		case d.width == 3:
			var v uint32
			if d.little {
				v = uint32(src[0])<<8 | uint32(src[1])<<16 | uint32(src[2])<<24
			} else {
				v = uint32(src[2])<<8 | uint32(src[1])<<16 | uint32(src[0])<<24
			}
			binary.LittleEndian.PutUint32(a.Data[i*4:], v)
		default:
			binary.LittleEndian.PutUint32(a.Data[i*4:], order.Uint32(src))
		}
	}
	return []Audio{a}, nil
}
