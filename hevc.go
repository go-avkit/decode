// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/go-avkit/h265"
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
	return &Decoder{cfg: cfg, lengthSize: lengthSize, h265: h265.NewDecoder(h265.Options{
		Limits: h265.Limits{MaxPixels: min(int64(cfg.frameMBs())*256, maxFrameSamples)},
		// A Frame owns its planes (hevcFrame copies them), so the decoder's picture goes back
		// to its pool the moment it is copied: no fresh allocation, and no page faults, per picture.
		RecyclePictures: true,
	})}
}

// hevcUnit reads the two header bytes of a NAL unit (ITU-T H.265 clause 7.3.1.2); false
// when the forbidden bit is set, the unit is shorter than its header, or
// nuh_temporal_id_plus1 is 0. The payload is a window into raw.
func hevcUnit(raw []byte, tag uint64) (h265.Unit, bool) {
	if len(raw) < 2 || raw[0]&0x80 != 0 || raw[1]&7 == 0 {
		return h265.Unit{}, false
	}
	return h265.Unit{
		Type:       h265.UnitType(raw[0] >> 1 & 0x3f),
		LayerID:    (raw[0]&1)<<5 | raw[1]>>3,
		TemporalID: raw[1]&7 - 1,
		// The last byte of a NAL unit is never 0x00 (7.4.2: a final 0x03 follows cabac_zero_words),
		// so zero bytes after the payload belong to the framing, as they do between Annex B units;
		// muxers that cut a byte stream at its start codes leave the one of a four-byte code on
		// the unit before it, and a sample then ends in a zero byte.
		Payload: bytes.TrimRight(raw[2:], "\x00"),
		Tag:     tag,
	}, true
}

// newHEVCWithSets builds a decoder and feeds it the parameter sets of a
// configuration record or of a track.
func newHEVCWithSets(cfg Config, lengthSize int, vps, sps, pps [][]byte) (*Decoder, error) {
	d := newHEVC(cfg, lengthSize)
	for _, set := range slices.Concat(vps, sps, pps) {
		u, ok := hevcUnit(set, 0)
		if !ok {
			return nil, fmt.Errorf("%w: parameter set is not a NAL unit", ErrConfig)
		}
		if err := d.hevcFormat(u); err != nil {
			return nil, fmt.Errorf("decode: parameter set: %w", err)
		}
		if _, err := d.h265.Decode(u); err != nil {
			return nil, fmt.Errorf("decode: parameter set: %w", d.classifyHEVC(err))
		}
	}
	return d, nil
}

// hevcSample decodes one length-prefixed access unit. The sample's pts rides on
// every NAL unit as its tag and comes back on the picture the first slice of
// which it carried, however the pictures are reordered.
func (d *Decoder) hevcSample(data []byte, pts int64) ([]Frame, error) {
	raws, err := splitSample(data, d.lengthSize)
	if err != nil {
		return nil, err
	}
	units := make([]h265.Unit, len(raws))
	for i, raw := range raws {
		var ok bool
		if units[i], ok = hevcUnit(raw, uint64(pts)); !ok {
			return nil, fmt.Errorf("%w: NAL unit %d has an invalid header", ErrSample, i)
		}
	}
	d.sampleMode = true
	return d.hevcUnits(units)
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
	d.annexB = rest
	if len(bytes.Trim(head[:cut], "\x00")) == 0 {
		return nil, nil // zero bytes and a start code: no unit yet
	}
	return d.hevcAnnexBUnits(head)
}

// hevcAnnexBUnits splits and decodes complete Annex B units.
func (d *Decoder) hevcAnnexBUnits(stream []byte) ([]Frame, error) {
	units, err := h265.SplitAnnexB(stream)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBitstream, err)
	}
	return d.hevcUnits(units)
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

// hevcUnits feeds NAL units to the decoder in order and returns the pictures
// that became ready. Pictures that came out before an error are returned with it.
func (d *Decoder) hevcUnits(units []h265.Unit) ([]Frame, error) {
	var out []Frame
	for _, u := range units {
		if err := d.hevcFormat(u); err != nil {
			return out, err
		}
		outs, err := d.h265.Decode(u)
		var rerr error
		if out, rerr = d.hevcResult(out, outs, u, err); rerr != nil {
			return out, rerr
		}
	}
	return out, nil
}

// hevcResult appends the frames of the pictures the decoder gave (decoding unit u, or the zero
// unit for a flush) to prior, with the decoder's error if it gave one, else the first error of
// the conversion.
func (d *Decoder) hevcResult(prior []Frame, outs []h265.Output, u h265.Unit, err error) ([]Frame, error) {
	fs, ferr := d.hevcFrames(outs)
	prior = append(prior, fs...)
	if err != nil {
		return prior, d.hevcError(u, err)
	}
	return prior, ferr
}

// hevcFlush decodes what an Annex B stream still holds and drains the decoder.
func (d *Decoder) hevcFlush() ([]Frame, error) {
	held := d.annexB
	d.annexB = nil
	var out []Frame
	if len(held) > 0 {
		var err error
		if out, err = d.hevcAnnexBUnits(held); err != nil {
			return out, err
		}
	}
	outs, err := d.h265.Flush()
	return d.hevcResult(out, outs, h265.Unit{}, err)
}

// hevcFramer is hevcFrame, a variable only so that a test can make a conversion fail: a
// picture the decoder gives is always one hevcFrame accepts, since the format was checked when
// its parameter set arrived.
var hevcFramer = (*Decoder).hevcFrame

// hevcFrames converts decoder pictures to Frames. It converts every picture and
// returns the first error.
func (d *Decoder) hevcFrames(outs []h265.Output) ([]Frame, error) {
	var out []Frame
	var first error
	for _, o := range outs {
		f, err := hevcFramer(d, o)
		if o.Picture != nil {
			o.Picture.Release() // copied into f (or refused): the planes are the decoder's again
		}
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

// hevcFormatError is the refusal of a picture format a Frame cannot carry: it names it.
func hevcFormatError(chroma string, bitDepth, bitDepthC int) error {
	return fmt.Errorf("%w: HEVC %s at %d/%d bits, only 4:2:0 at 8 or 10 bits is decoded",
		ErrUnsupported, chroma, bitDepth, bitDepthC)
}

// hevcFormat refuses the sequence parameter set of a format a Frame cannot carry (4:0:0,
// 4:2:2, 4:4:4, unequal luma and chroma bit depths, and anything but 8 or 10 bits), before a
// picture of it is decoded. Units that are not a base-layer SPS, or an SPS that does not
// parse (the decoder reports that), pass.
func (d *Decoder) hevcFormat(u h265.Unit) error {
	// ParseSPS refuses a unit that is no SPS and one of a layer above 0 (Annex F sets have
	// another syntax), and both pass here like any SPS that does not parse.
	s, err := h265.ParseSPS(u)
	if err != nil {
		return nil
	}
	chroma := [4]string{"4:0:0", "4:2:0", "4:2:2", "4:4:4"}[s.ChromaFormat&3]
	if s.ChromaFormat != 1 || s.BitDepthLuma != s.BitDepthChroma || (s.BitDepthLuma != 8 && s.BitDepthLuma != 10) {
		return hevcFormatError(chroma, int(s.BitDepthLuma), int(s.BitDepthChroma))
	}
	return nil
}

// hevcFrame copies a decoder picture into a Frame that owns its planes, packed (the picture
// is already cut to the conformance window, ITU-T H.265 clause C.5.2.4, step 2). Samples of
// more than eight bits are written as little-endian 16-bit words, which is ffmpeg's
// yuv420p10le.
func (d *Decoder) hevcFrame(o h265.Output) (Frame, error) {
	p := o.Picture
	chroma := "4:0:0"
	var w, h, cw, ch int
	if p != nil && p.Components > 0 {
		w, h = p.Planes[0].Width, p.Planes[0].Height
		if p.Components >= 3 {
			cw, ch = p.Planes[1].Width, p.Planes[1].Height
			switch {
			case cw == w && ch == h:
				chroma = "4:4:4"
			case cw == w/2 && ch == h:
				chroma = "4:2:2"
			default:
				chroma = "4:2:0"
			}
		}
	}
	if p == nil || p.Components != 3 || chroma != "4:2:0" || p.BitDepth[0] != p.BitDepth[1] || (p.BitDepth[0] != 8 && p.BitDepth[0] != 10) {
		var bd, bdc int
		if p != nil {
			bd, bdc = p.BitDepth[0], p.BitDepth[1]
		}
		return Frame{}, hevcFormatError(chroma, bd, bdc)
	}
	if w <= 0 || h <= 0 || cw != (w+1)/2 || ch != (h+1)/2 || !planesFit(p) {
		return Frame{}, fmt.Errorf("%w: picture %dx%d with chroma planes %dx%d does not lie in its planes",
			ErrBitstream, w, h, cw, ch)
	}
	step := 1
	if p.BitDepth[0] > 8 {
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
		BitDepth: p.BitDepth[0],
		Corrupt:  o.Corrupt,
	}
	packPlane(f.Y, &p.Planes[0])
	packPlane(f.Cb, &p.Planes[1])
	packPlane(f.Cr, &p.Planes[2])
	switch {
	case d.sampleMode:
		f.PTS = int64(o.Tag)
	default:
		f.PTS = d.nextIndex
	}
	d.nextIndex++
	return f, nil
}

// planesFit reports whether every row of the three planes of p lies inside its slice, as
// the copy will read it.
func planesFit(p *h265.DecodedPicture) bool {
	for c := range 3 {
		pl := &p.Planes[c]
		if pl.Stride < pl.Width || pl.Len() < (pl.Height-1)*pl.Stride+pl.Width {
			return false
		}
	}
	return true
}

// packPlane copies a plane packed: the bytes of a byte plane as they are, one memmove a row, and
// the samples of a 16-bit plane as little-endian words.
func packPlane(dst []byte, pl *h265.Plane) {
	for r := range pl.Height {
		if pl.Pix8 != nil { // a byte plane: one memmove a row
			copy(dst[r*pl.Width:(r+1)*pl.Width], pl.Pix8[r*pl.Stride:r*pl.Stride+pl.Width])
			continue
		}
		row := pl.Pix16[r*pl.Stride : r*pl.Stride+pl.Width]
		out := dst[r*pl.Width*2 : (r+1)*pl.Width*2]
		i := 0
		for ; i+4 <= len(row); i += 4 {
			s, o := row[i:i+4:i+4], out[2*i:2*i+8:2*i+8]
			binary.LittleEndian.PutUint64(o, uint64(s[0])|uint64(s[1])<<16|uint64(s[2])<<32|uint64(s[3])<<48)
		}
		for ; i < len(row); i++ {
			out[2*i], out[2*i+1] = byte(row[i]), byte(row[i]>>8)
		}
	}
}

// hevcError classifies the error the decoder gave for unit u. A sequence parameter set over the
// size limit is refused when it arrives, and the slices that name it then find no set: they are
// refused for the same reason, not reported as a stream that lacks its parameter set.
func (d *Decoder) hevcError(u h265.Unit, err error) error {
	cerr := d.classifyHEVC(err)
	switch {
	case u.Type == h265.UnitSPS && errors.Is(cerr, ErrOverLimit):
		d.refusedSPS = cerr
	case d.refusedSPS != nil && errors.Is(err, h265.ErrMissingParameterSet):
		return d.refusedSPS
	}
	return cerr
}

// classifyHEVC maps the HEVC decoder's errors onto this package's.
func (d *Decoder) classifyHEVC(err error) error {
	switch {
	case errors.Is(err, h265.ErrPictureTooLarge), errors.Is(err, h265.ErrDPBTooLarge):
		// The limits are Config's: both a stream over them and a stream the decoder does not
		// take match ErrOverLimit, and ErrUnsupported as they did when the HEVC decoder could
		// not tell the two apart.
		return fmt.Errorf("%w: %w (picture size limit %d luma samples)", ErrOverLimit, errors.Join(ErrUnsupported, err),
			int64(d.cfg.frameMBs())*256)
	case errors.Is(err, h265.ErrNotReconstructed),
		// the one value ErrUnsupportedSPS also stands for that is a coding tool and not a
		// field out of range: an SPS extension of 3D or screen content coding
		errors.Is(err, h265.ErrUnsupportedSPS) && strings.Contains(err.Error(), "extension"):
		return fmt.Errorf("%w: %w", ErrUnsupported, err)
	case errors.Is(err, h265.ErrUnsupportedSPS), errors.Is(err, h265.ErrSliceHeader), errors.Is(err, h265.ErrSliceData), errors.Is(err, h265.ErrResidual),
		errors.Is(err, h265.ErrMissingParameterSet), errors.Is(err, h265.ErrCorruptPicture), errors.Is(err, h265.ErrMissingReference),
		errors.Is(err, h265.ErrUnsupportedPPS), errors.Is(err, h265.ErrNotSPS), errors.Is(err, h265.ErrNotPPS),
		errors.Is(err, h265.ErrRefLists), errors.Is(err, h265.ErrPOCRange), errors.Is(err, h265.ErrNoOrder),
		errors.Is(err, h265.ErrScalingList), errors.Is(err, h265.ErrSEI), errors.Is(err, h265.ErrRefIdxRange):
		return fmt.Errorf("%w: %w", ErrBitstream, err)
	}
	return err
}
