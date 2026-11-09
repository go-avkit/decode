// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

	"github.com/gen2brain/h265/hevc"
)

// ErrBitstream means the HEVC bitstream itself is malformed: a parameter set,
// slice header or slice data the decoder cannot read. A bitstream the decoder
// can read but does not implement is ErrUnsupported instead.
var ErrBitstream = errors.New("decode: malformed bitstream")

// maxFrameSamples bounds the picture size handed to the HEVC decoder, which
// counts luma samples, so that Config.MaxFrameMBs times 256 cannot wrap an int
// on a 32-bit system.
const maxFrameSamples = 1<<31 - 1

// NewHEVC returns an HEVC (H.265) decoder for length-prefixed samples with
// lengthSize-byte lengths (1, 2 or 4). The parameter sets must then arrive
// in-band, or through NewHVCC or NewTrack. For HEVC, Config.MaxFrameMBs counts
// 256 luma samples (a 16x16 block, as for H.264) and Config.EnforceLevel has no
// effect.
func NewHEVC(cfg Config, lengthSize int) (*Decoder, error) {
	if lengthSize != 1 && lengthSize != 2 && lengthSize != 4 {
		return nil, fmt.Errorf("%w: NAL length size %d, want 1, 2 or 4", ErrConfig, lengthSize)
	}
	return newHEVC(cfg, lengthSize), nil
}

func newHEVC(cfg Config, lengthSize int) *Decoder {
	d := &Decoder{cfg: cfg, lengthSize: lengthSize, h265: &hevc.Decoder{}}
	d.h265.FrameSizeLimit(int(min(int64(cfg.frameMBs())*256, maxFrameSamples)))
	return d
}

// newHEVCWithSets builds a decoder and feeds it the parameter sets of a
// configuration record or of a track.
func newHEVCWithSets(cfg Config, lengthSize int, vps, sps, pps [][]byte) (*Decoder, error) {
	d := newHEVC(cfg, lengthSize)
	for _, set := range slices.Concat(vps, sps, pps) {
		nal, ok := hevc.ParseNAL(set)
		if !ok {
			return nil, fmt.Errorf("%w: parameter set is not a NAL unit", ErrConfig)
		}
		if _, err := d.h265.DecodeNAL(nal); err != nil {
			return nil, fmt.Errorf("decode: parameter set: %w", d.classifyHEVC(err))
		}
	}
	return d, nil
}

// hevcSample decodes one length-prefixed access unit. The sample's pts rides on
// every NAL unit as its tag and comes back on the picture the first slice of
// which it carried, however the pictures are reordered.
func (d *Decoder) hevcSample(data []byte, pts int64) ([]Frame, error) {
	units, err := splitSample(data, d.lengthSize)
	if err != nil {
		return nil, err
	}
	nals := make([]hevc.NALUnit, len(units))
	for i, u := range units {
		var ok bool
		if nals[i], ok = hevc.ParseNAL(u); !ok {
			return nil, fmt.Errorf("%w: NAL unit %d has an invalid header", ErrSample, i)
		}
		nals[i].Tag = uint64(pts)
	}
	d.sampleMode = true
	return d.hevcNALs(nals)
}

// hevcAnnexB decodes part of an Annex B byte stream (ITU-T H.265 clause B.2). A
// NAL unit is complete only when the start code of the next one follows, so
// what comes after the last start code is held until more arrives.
func (d *Decoder) hevcAnnexB(data []byte) ([]Frame, error) {
	if len(d.annexB)+len(data) > d.cfg.sampleBytes() {
		return nil, fmt.Errorf("%w: %d bytes held without a complete NAL unit, the limit is %d",
			ErrOverLimit, len(d.annexB)+len(data), d.cfg.sampleBytes())
	}
	// Only the bytes that arrived now (and the two before them, which a start
	// code may straddle) are searched, so a NAL unit fed in small pieces is not
	// scanned again for each.
	from := max(len(d.annexB)-2, 0)
	d.annexB = append(d.annexB, data...)
	cut := lastStartCode(d.annexB, from)
	if cut <= 0 {
		// No start code yet, or the one that opens the unit still being held.
		return nil, nil
	}
	head := d.annexB[:cut+3]
	rest := bytes.Clone(d.annexB[cut:])
	nals := hevc.SplitAnnexB(head)
	d.annexB = rest
	return d.hevcNALs(nals)
}

// lastStartCode returns the index of the last three-byte start code prefix
// (0x000001) in b that begins at or after from, or -1.
func lastStartCode(b []byte, from int) int {
	for i := len(b) - 3; i >= from; i-- {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			return i
		}
	}
	return -1
}

// hevcNALs feeds NAL units to the decoder in order and returns the pictures
// that became ready. Pictures that came out before an error are returned with it.
func (d *Decoder) hevcNALs(nals []hevc.NALUnit) ([]Frame, error) {
	var out []Frame
	for _, n := range nals {
		pics, err := d.h265.DecodeNAL(n)
		fs, ferr := d.hevcFrames(pics)
		out = append(out, fs...)
		if err != nil {
			return out, d.classifyHEVC(err)
		}
		if ferr != nil {
			return out, ferr
		}
	}
	return out, nil
}

// hevcFlush decodes what an Annex B stream still holds and drains the decoder.
func (d *Decoder) hevcFlush() ([]Frame, error) {
	out, err := d.hevcNALs(hevc.SplitAnnexB(d.annexB))
	d.annexB = nil
	if err != nil {
		return out, err
	}
	fs, err := d.hevcFrames(d.h265.Flush())
	return append(out, fs...), err
}

// hevcFrames converts decoder pictures to Frames and hands each one's planes
// back to the decoder. It converts every picture and returns the first error.
func (d *Decoder) hevcFrames(pics []*hevc.Picture) ([]Frame, error) {
	var out []Frame
	var first error
	for _, p := range pics {
		f, err := d.hevcFrame(p)
		p.Release()
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		out = append(out, f)
	}
	return out, first
}

// hevcFrame copies a decoder picture out of its reference buffer, cropped to the
// conformance window (ITU-T H.265 clause C.5.2.4, step 2) and packed. Samples
// of more than eight bits are written as little-endian 16-bit words, which is
// ffmpeg's yuv420p10le.
func (d *Decoder) hevcFrame(p *hevc.Picture) (Frame, error) {
	if p.ChromaFormat != 1 || p.BitDepth != p.BitDepthC || (p.BitDepth != 8 && p.BitDepth != 10) {
		return Frame{}, fmt.Errorf("%w: HEVC chroma_format_idc %d at %d/%d bits, only 4:2:0 at 8 or 10 bits is decoded",
			ErrUnsupported, p.ChromaFormat, p.BitDepth, p.BitDepthC)
	}
	w, h := p.CropW, p.CropH
	x, y := p.CropX, p.CropY
	cw, ch := w/2, h/2
	if w <= 0 || h <= 0 || x < 0 || y < 0 || x+w > p.Width || y+h > p.Height ||
		!planeFits(p, x, y, w, h) {
		return Frame{}, fmt.Errorf("%w: picture %dx%d at (%d,%d) does not lie in its %dx%d planes",
			ErrBitstream, w, h, x, y, p.Width, p.Height)
	}
	step := 1
	if p.BitDepth > 8 {
		step = 2
	}
	f := Frame{
		Y:        make([]byte, w*h*step),
		Cb:       make([]byte, cw*ch*step),
		Cr:       make([]byte, cw*ch*step),
		YStride:  w * step,
		CStride:  cw * step,
		Width:    w,
		Height:   h,
		Chroma:   Chroma420,
		BitDepth: p.BitDepth,
		Corrupt:  p.Corrupt,
	}
	if step == 1 {
		cropPlane8(f.Y, p.Y, p.StrideY, x, y, w, h)
		cropPlane8(f.Cb, p.Cb, p.StrideC, x/2, y/2, cw, ch)
		cropPlane8(f.Cr, p.Cr, p.StrideC, x/2, y/2, cw, ch)
	} else {
		cropPlane16(f.Y, p.Y16, p.StrideY, x, y, w, h)
		cropPlane16(f.Cb, p.Cb16, p.StrideC, x/2, y/2, cw, ch)
		cropPlane16(f.Cr, p.Cr16, p.StrideC, x/2, y/2, cw, ch)
	}
	switch {
	case d.sampleMode:
		f.PTS = int64(p.Tag)
	default:
		f.PTS = d.nextIndex
	}
	d.nextIndex++
	return f, nil
}

// planeFits reports whether the planes of p hold the cropped window: every row
// the copy will read lies inside its slice.
func planeFits(p *hevc.Picture, x, y, w, h int) bool {
	need := func(stride, x, y, w, h int) int { return (y+h-1)*stride + x + w }
	ny, nc := need(p.StrideY, x, y, w, h), need(p.StrideC, x/2, y/2, w/2, h/2)
	if p.BitDepth > 8 {
		return len(p.Y16) >= ny && len(p.Cb16) >= nc && len(p.Cr16) >= nc
	}
	return len(p.Y) >= ny && len(p.Cb) >= nc && len(p.Cr) >= nc
}

// cropPlane8 copies a w x h window at (x, y) of an 8-bit plane, packed.
func cropPlane8(dst, src []byte, stride, x, y, w, h int) {
	for r := range h {
		copy(dst[r*w:], src[(y+r)*stride+x:(y+r)*stride+x+w])
	}
}

// cropPlane16 copies a w x h window at (x, y) of a 16-bit plane as
// little-endian words, packed.
func cropPlane16(dst []byte, src []uint16, stride, x, y, w, h int) {
	for r := range h {
		row := src[(y+r)*stride+x : (y+r)*stride+x+w]
		for i, v := range row {
			dst[(r*w+i)*2] = byte(v)
			dst[(r*w+i)*2+1] = byte(v >> 8)
		}
	}
}

// classifyHEVC maps the HEVC decoder's errors onto this package's.
func (d *Decoder) classifyHEVC(err error) error {
	switch {
	case errors.Is(err, hevc.ErrUnsupported):
		// FrameSizeLimit refuses a picture over the limit with the same error as
		// a coding tool that is not implemented; both are a refusal, and the text
		// of the decoder's error says nothing more, so the limit is named here.
		return fmt.Errorf("%w: %w (picture size limit %d luma samples, or a coding tool not implemented)",
			ErrUnsupported, err, int64(d.cfg.frameMBs())*256)
	case errors.Is(err, hevc.ErrInvalid):
		return fmt.Errorf("%w: %w", ErrBitstream, err)
	}
	return err
}
