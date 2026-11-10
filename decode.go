// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

// Package decode turns the coded video samples avkit's container package
// demuxes into pictures.
//
// The first codec is H.264 (ITU-T H.264 | ISO/IEC 14496-10), through
// github.com/go-avkit/h264/decoder (a decoder imported from oops1/go.264), the
// second HEVC (ITU-T H.265), through
// github.com/gen2brain/h265: pure Go decoders that are checked here, picture for
// picture, against ffmpeg. See the README for the profiles that claim holds
// for and the conformance bitstreams it was measured on.
//
// Input is what a container hands over: an AVCDecoderConfigurationRecord or an
// HEVCDecoderConfigurationRecord (ISO/IEC 14496-15) or the parameter sets of a
// container.TrackConfig, then one length-prefixed access unit per sample.
// Output is every picture in display order as planar YUV, with its chroma
// format, bit depth and presentation time.
package decode

import (
	"errors"
	"fmt"
	"slices"

	"github.com/gen2brain/h265/hevc"
	h264dec "github.com/go-avkit/h264/decoder"
)

// Errors a decoder can refuse with. Each is matched with errors.Is.
var (
	// ErrConfig means the codec configuration (an avcC record or a track's
	// parameter sets) is malformed.
	ErrConfig = errors.New("decode: invalid codec configuration")
	// ErrSample means a sample is not a well formed run of length-prefixed
	// NAL units.
	ErrSample = errors.New("decode: invalid sample")
	// ErrOverLimit means the stream asks for more than Config allows: a
	// picture larger than MaxFrameMBs, or a sample larger than MaxSampleBytes.
	// It is raised before anything sized by the stream is allocated.
	ErrOverLimit = errors.New("decode: stream exceeds the configured limits")
	// ErrUnsupported means the stream uses a coding tool this decoder does
	// not implement (see the README): for H.264 interlaced coding, a chroma
	// format other than 4:2:0, a bit depth above 8, lossless transform bypass;
	// for HEVC a chroma format other than 4:2:0, a bit depth other than 8 or
	// 10, and, because the HEVC decoder reports both the same way, a picture
	// larger than Config.MaxFrameMBs.
	ErrUnsupported = errors.New("decode: unsupported coding tool")
	// ErrClosed means the decoder was used after Close.
	ErrClosed = errors.New("decode: use of a closed decoder")
)

// Default limits. A stream is untrusted input: the picture size in its
// sequence parameter set sizes every buffer the decoder allocates, so it is
// bounded before the first of them is.
const (
	// DefaultMaxFrameMBs is 36864 macroblocks, a 4096x2304 picture: level 5.1
	// of H.264 Annex A.
	DefaultMaxFrameMBs = 36864
	// DefaultMaxSampleBytes is 64 MiB, far above any single access unit a
	// level 6.2 stream can carry at its maximum bit rate and frame rate.
	DefaultMaxSampleBytes = 64 << 20
)

// Config bounds what a stream may make the decoder allocate. A zero field takes
// its default.
type Config struct {
	// MaxFrameMBs is the largest picture accepted, in 16x16 macroblocks.
	MaxFrameMBs int
	// MaxSampleBytes is the largest sample (access unit) accepted.
	MaxSampleBytes int
	// EnforceLevel also refuses a stream that exceeds its own declared
	// level's limits (Annex A), where MaxFrameMBs alone would admit it.
	EnforceLevel bool
	// IgnoreEditList makes DecodeTrack and PosterFrame label pictures with track
	// time, the decoding time of a sample plus its composition offset, and return
	// every picture, as ffmpeg's -ignore_editlist 1 does. The default applies the
	// track's edit list: pictures it leaves out are decoded but not returned, and
	// presentation times are those of the presentation, as ffmpeg does by default.
	// The streaming Decoder does not read it.
	IgnoreEditList bool
}

func (c Config) frameMBs() int {
	if c.MaxFrameMBs > 0 {
		return c.MaxFrameMBs
	}
	return DefaultMaxFrameMBs
}

func (c Config) sampleBytes() int {
	if c.MaxSampleBytes > 0 {
		return c.MaxSampleBytes
	}
	return DefaultMaxSampleBytes
}

// Chroma is the chroma sampling of a Frame.
type Chroma uint8

// The chroma formats of H.264 (chroma_format_idc). Only Chroma420 is
// produced today; the others are named so that a Frame can describe the
// profiles that are not decoded yet without an API change.
const (
	Chroma400 Chroma = iota
	Chroma420
	Chroma422
	Chroma444
)

// Frame is one decoded picture, cropped to the stream's display window.
//
// The field names and meaning match go-videodsp/yuv.Picture for 8-bit 4:2:0 so
// that converting is a field-by-field copy. A Frame owns its planes.
type Frame struct {
	// Y, Cb and Cr are the planes. A sample is one byte when BitDepth is 8
	// and a little-endian 16-bit word above that. A Chroma400 frame has no
	// Cb or Cr.
	Y, Cb, Cr []byte
	// YStride and CStride are the plane row lengths in bytes.
	YStride, CStride int
	// Width and Height are the luma size in samples.
	Width, Height int
	// Chroma is the chroma sampling.
	Chroma Chroma
	// BitDepth is the luma bit depth.
	BitDepth int
	// PTS is the presentation time in the track timescale: the decode time of
	// the sample plus its composition offset, as DecodeSample and, with
	// Config.IgnoreEditList, DecodeTrack and PosterFrame give it; DecodeTrack and
	// PosterFrame by default give that time as the track's edit list presents it. For a raw Annex B stream it
	// is the picture's index in display order.
	PTS int64
	// Corrupt reports that the HEVC decoder lost a slice of the picture, failed
	// partway through it, or predicted it from a reference the stream never
	// delivered. The picture is still returned, as far as it was decoded. It is
	// never set for H.264.
	Corrupt bool
}

// Decoder decodes one H.264 or HEVC elementary stream. It is not safe for
// concurrent use.
type Decoder struct {
	dec        *h264dec.Decoder
	h265       *hevc.Decoder // set for HEVC, in which case dec is nil
	cfg        Config
	lengthSize int
	annexB     []byte
	pending    []int64 // presentation times of samples not yet output, sorted
	last       int64
	nextIndex  int64
	sampleMode bool
	started    bool
	closed     bool
}

// New returns a decoder for length-prefixed samples with lengthSize-byte
// lengths (1, 2 or 4 as avcC allows; MP4 and Matroska use 4). The parameter
// sets must then arrive in-band, or through Config via NewAVCC or NewTrack.
func New(cfg Config, lengthSize int) (*Decoder, error) {
	if lengthSize != 1 && lengthSize != 2 && lengthSize != 4 {
		return nil, fmt.Errorf("%w: NAL length size %d, want 1, 2 or 4", ErrConfig, lengthSize)
	}
	return newDecoder(cfg, lengthSize), nil
}

// newDecoder builds a decoder for a length size already known to be valid.
func newDecoder(cfg Config, lengthSize int) *Decoder {
	d := &Decoder{cfg: cfg, lengthSize: lengthSize}
	// The decoder is pure Go; there is no hardware path to choose from.
	d.dec = h264dec.New(h264dec.Limits{
		MaxFrameMBs:  cfg.frameMBs(),
		MaxNALBytes:  cfg.sampleBytes(),
		EnforceLevel: cfg.EnforceLevel,
	})
	return d
}

// DecodeSample decodes one access unit in length-prefixed form, which is
// container.Sample.Data, and returns the pictures that became ready, in display
// order. pts is the sample's presentation time; it is attached to the pictures
// in increasing order as they come out, so it need not belong to the picture
// the sample itself contains. A picture can come out several samples late.
func (d *Decoder) DecodeSample(data []byte, pts int64) ([]Frame, error) {
	if d.closed {
		return nil, ErrClosed
	}
	if len(data) > d.cfg.sampleBytes() {
		return nil, fmt.Errorf("%w: sample of %d bytes, the limit is %d", ErrOverLimit, len(data), d.cfg.sampleBytes())
	}
	if d.h265 != nil {
		return d.hevcSample(data, pts)
	}
	annexB, err := d.toAnnexB(data)
	if err != nil {
		return nil, err
	}
	d.sampleMode = true
	i, _ := slices.BinarySearch(d.pending, pts)
	d.pending = slices.Insert(d.pending, i, pts)
	return d.run(annexB)
}

// DecodeAnnexB decodes part of a raw Annex B byte stream (start code
// delimited, as in a .264 file). The pictures' PTS is their index in display
// order. The chunk need not end on a NAL unit boundary.
func (d *Decoder) DecodeAnnexB(data []byte) ([]Frame, error) {
	if d.closed {
		return nil, ErrClosed
	}
	if len(data) > d.cfg.sampleBytes() {
		return nil, fmt.Errorf("%w: chunk of %d bytes, the limit is %d", ErrOverLimit, len(data), d.cfg.sampleBytes())
	}
	if d.h265 != nil {
		return d.hevcAnnexB(data)
	}
	return d.run(data)
}

func (d *Decoder) run(annexB []byte) ([]Frame, error) {
	pics, err := d.dec.Decode(annexB)
	return d.collect(pics, err)
}

// Flush returns the pictures still held for reordering. Call it at the end of
// the stream.
func (d *Decoder) Flush() ([]Frame, error) {
	if d.closed {
		return nil, ErrClosed
	}
	if d.h265 != nil {
		return d.hevcFlush()
	}
	pics, err := d.dec.Flush()
	return d.collect(pics, err)
}

// Close releases the decoder.
func (d *Decoder) Close() error {
	if d.closed {
		return nil
	}
	d.closed = true
	d.annexB, d.pending = nil, nil
	if d.h265 != nil {
		d.h265.Reset()
		return nil
	}
	d.dec = nil
	return nil
}

func (d *Decoder) collect(pics []*h264dec.Frame, err error) ([]Frame, error) {
	var out []Frame
	for _, p := range pics {
		out = append(out, d.frame(p))
	}
	return out, classify(err)
}

// frame copies a decoder picture out of its reference buffer, packed, so the
// caller owns it however long it keeps it.
func (d *Decoder) frame(p *h264dec.Frame) Frame {
	cw, ch := (p.Width+1)/2, (p.Height+1)/2
	f := Frame{
		Y:        make([]byte, p.Width*p.Height),
		Cb:       make([]byte, cw*ch),
		Cr:       make([]byte, cw*ch),
		YStride:  p.Width,
		CStride:  cw,
		Width:    p.Width,
		Height:   p.Height,
		Chroma:   Chroma420,
		BitDepth: 8,
	}
	for y := 0; y < p.Height; y++ {
		copy(f.Y[y*p.Width:], p.Y[y*p.StrideY:y*p.StrideY+p.Width])
	}
	for y := 0; y < ch; y++ {
		copy(f.Cb[y*cw:], p.Cb[y*p.StrideC:y*p.StrideC+cw])
		copy(f.Cr[y*cw:], p.Cr[y*p.StrideC:y*p.StrideC+cw])
	}
	switch {
	case len(d.pending) > 0:
		f.PTS, d.pending = d.pending[0], d.pending[1:]
		d.last = f.PTS
	case d.sampleMode:
		// More pictures than samples (a sample holding several): the best
		// time left is the last one given.
		f.PTS = d.last
	default:
		f.PTS = d.nextIndex
	}
	d.nextIndex++
	return f
}

// classify maps the decoder's errors onto this package's.
func classify(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, h264dec.ErrOverLimit):
		return fmt.Errorf("%w: %w", ErrOverLimit, err)
	case errors.Is(err, h264dec.ErrUnsupported):
		return fmt.Errorf("%w: %w", ErrUnsupported, err)
	}
	return err
}
