// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/go-avkit/avkit/container"
	"github.com/mewkiz/flac/frame"
)

// FLAC (RFC 9639) through github.com/mewkiz/flac, a pure Go decoder that is
// public domain (the Unlicense). FLAC is lossless, so the output is checked
// against ffmpeg sample for sample.

func init() {
	audioDecoders["fLaC"] = newFLAC
}

const (
	flacMarker         = "fLaC"
	flacStreamInfoType = 0
	flacStreamInfoSize = 34
)

type flacDecoder struct {
	rate, channels, depth int
	layout                ChannelLayout
	maxBlock              int
}

// flacLayouts are the speaker positions of a FLAC stream of 1 to 8 channels
// (RFC 9639 9.1.3), the layouts ffmpeg's flac decoder names.
var flacLayouts = [...]ChannelLayout{
	1: LayoutMono, 2: LayoutStereo, 3: Layout3, 4: LayoutQuad,
	5: Layout5, 6: Layout51, 7: Layout61, 8: Layout71,
}

// newFLAC reads the STREAMINFO block of tc.CodecConfig: the "fLaC" marker and the
// metadata blocks, as Matroska's CodecPrivate holds them and as a native file
// starts. An MP4's dfLa box holds the same blocks without the marker, and the
// marker is therefore optional here.
func newFLAC(tc container.TrackConfig, cfg AudioConfig) (audioCodec, error) {
	conf := bytes.TrimPrefix(tc.CodecConfig, []byte(flacMarker))
	if len(conf) < 4+flacStreamInfoSize {
		return nil, fmt.Errorf("%w: a FLAC configuration of %d bytes holds no STREAMINFO", ErrAudioConfig, len(tc.CodecConfig))
	}
	if conf[0]&0x7F != flacStreamInfoType || int(conf[1])<<16|int(conf[2])<<8|int(conf[3]) != flacStreamInfoSize {
		return nil, fmt.Errorf("%w: the first FLAC metadata block is not STREAMINFO", ErrAudioConfig)
	}
	packed := binary.BigEndian.Uint64(conf[4+10 : 4+18])
	d := &flacDecoder{
		rate:     int(packed >> 44),
		channels: int(packed>>41&7) + 1,
		depth:    int(packed>>36&0x1F) + 1,
		maxBlock: int(binary.BigEndian.Uint16(conf[4+2:])),
	}
	switch {
	case d.rate == 0:
		return nil, fmt.Errorf("%w: FLAC STREAMINFO states a sample rate of 0", ErrAudioConfig)
	case d.depth < 4:
		return nil, fmt.Errorf("%w: FLAC STREAMINFO states %d bits per sample, the minimum is 4", ErrAudioConfig, d.depth)
	case d.depth > 24:
		// RFC 9639 allows up to 32 bits (frame header code 0b111); mewkiz/flac
		// stops at 24 and reads that code as reserved.
		return nil, fmt.Errorf("%w: FLAC at %d bits per sample, the decoder behind this one reads up to 24", ErrAudioUnsupported, d.depth)
	case d.channels > cfg.channels():
		return nil, fmt.Errorf("%w: %d channels, the limit is %d", ErrOverLimit, d.channels, cfg.channels())
	case d.maxBlock != 0 && d.maxBlock < 16:
		return nil, fmt.Errorf("%w: FLAC STREAMINFO states a maximum block size of %d, the minimum is 16", ErrAudioConfig, d.maxBlock)
	}
	d.layout = flacLayouts[d.channels]
	d.maxBlock = cfg.block()
	return d, nil
}

func (d *flacDecoder) priming() int { return 0 }

func (d *flacDecoder) sampleRate() int { return d.rate }

func (d *flacDecoder) flush() ([]Audio, error) { return nil, nil }

// decode decodes one FLAC frame. The block size is read from the frame header
// and checked against the limit before the subframes, which are what the block
// size sizes, are parsed.
func (d *flacDecoder) decode(data []byte) ([]Audio, error) {
	r := bytes.NewReader(data)
	f, err := frame.New(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAudioSample, noEOF(err))
	}
	if f.BitsPerSample == 0 {
		f.BitsPerSample = uint8(d.depth)
	}
	switch {
	case f.BlockSize == 0:
		// The 16-bit block size field is the size less one: 65535 is a block of
		// 65536, which RFC 9639 9.1.6 forbids and the decoder behind this one
		// wraps to zero.
		return nil, fmt.Errorf("%w: FLAC block of 65536 samples, which RFC 9639 forbids", ErrAudioSample)
	case f.Channels.Count() != d.channels || int(f.BitsPerSample) != d.depth:
		return nil, fmt.Errorf("%w: frame of %d channels at %d bits in a stream of %d at %d",
			ErrAudioSample, f.Channels.Count(), f.BitsPerSample, d.channels, d.depth)
	case int(f.BlockSize) > d.maxBlock:
		return nil, fmt.Errorf("%w: FLAC block of %d samples, the limit is %d", ErrOverLimit, f.BlockSize, d.maxBlock)
	}
	if err := f.Parse(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAudioSample, noEOF(err))
	}
	// Every subframe holds BlockSize samples here: the decoder behind this one
	// refuses one that does not (TestFLACLPCOrderBeyondBlock), and pcm indexes by it.
	return []Audio{d.pcm(f)}, nil
}

// noEOF turns the bare io.EOF a truncated frame header returns into the error it
// is.
func noEOF(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// pcm interleaves the subframes. A sample of 16 bits or fewer is left-justified in
// 16 bits and a longer one in 32, which is what ffmpeg's flac decoder outputs
// (s16 and s32).
func (d *flacDecoder) pcm(f *frame.Frame) Audio {
	n := int(f.BlockSize)
	a := Audio{SampleRate: d.rate, Channels: d.channels, Layout: d.layout}
	if d.depth <= 16 {
		a.Format = SampleS16
		a.Data = make([]byte, n*d.channels*2)
		shift := 16 - d.depth
		for ch, sf := range f.Subframes {
			for i, s := range sf.Samples {
				binary.LittleEndian.PutUint16(a.Data[(i*d.channels+ch)*2:], uint16(s<<shift))
			}
		}
		return a
	}
	a.Format = SampleS32
	a.Data = make([]byte, n*d.channels*4)
	shift := 32 - d.depth
	for ch, sf := range f.Subframes {
		for i, s := range sf.Samples {
			binary.LittleEndian.PutUint32(a.Data[(i*d.channels+ch)*4:], uint32(s<<shift))
		}
	}
	return a
}
