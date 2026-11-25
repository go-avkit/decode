// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"fmt"

	"github.com/go-avkit/avkit/container"
	"github.com/thesyncim/govpx"
)

// vpx holds the VP8 or VP9 decoder of a Decoder (github.com/thesyncim/govpx, a pure Go port
// of libvpx).
type vpx struct {
	vp8 *govpx.VP8Decoder
	vp9 *govpx.VP9Decoder
}

// NewVP8 returns a VP8 decoder (RFC 6386). A sample is one VP8 frame, as MP4 and Matroska
// store it; there are no parameter sets, and the first sample must be a key frame. Config.MaxFrameMBs
// bounds the picture in 16x16 macroblocks, Config.EnforceLevel has no effect.
func NewVP8(cfg Config) (*Decoder, error) {
	return newVPX(cfg, false)
}

// NewVP9 returns a VP9 decoder (VP9 bitstream specification v0.6). A sample is one VP9 frame or
// superframe. Profiles 0 to 3 are decoded (8, 10 and 12 bits; 4:2:0, 4:2:2 and 4:4:4); a 4:4:0
// stream is ErrUnsupported, because Frame cannot describe it. Config.MaxFrameMBs
// bounds the picture in 16x16 macroblocks.
func NewVP9(cfg Config) (*Decoder, error) {
	return newVPX(cfg, true)
}

// The govpx constructors, replaceable so that a test can make one refuse.
var (
	newGovpx8 = govpx.NewVP8Decoder
	newGovpx9 = govpx.NewVP9Decoder
)

func newVPX(cfg Config, vp9 bool) (*Decoder, error) {
	// A cap per side; the area is bounded by Config.MaxFrameMBs in checkVPX, before the decoder sees a header.
	d := &Decoder{cfg: cfg, lengthSize: 4, vpx: new(vpx)}
	var err error
	if vp9 {
		side := int(min(int64(cfg.frameMBs())*16, 1<<16)) // VP9 sizes are 16 bits plus one
		d.vpx.vp9, err = newGovpx9(govpx.VP9DecoderOptions{MaxWidth: side, MaxHeight: side})
	} else {
		side := int(min(int64(cfg.frameMBs())*16, 1<<14-1)) // VP8 sizes are 14 bits

		d.vpx.vp8, err = newGovpx8(govpx.DecoderOptions{MaxWidth: side, MaxHeight: side})
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return d, nil
}

// isVP8 and isVP9 report whether a container track's sample entry names the codec.
func isVP8(tc container.TrackConfig) bool { return tc.Codec == "vp08" }
func isVP9(tc container.TrackConfig) bool { return tc.Codec == "vp09" }

// vpxSample decodes one VP8 frame or VP9 frame or superframe. The pictures of VP8 and VP9 come
// out in decoding order (there is no reordering), so the sample's pts is the picture's.
func (d *Decoder) vpxSample(data []byte, pts int64) ([]Frame, error) {
	d.sampleMode = true
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty sample", ErrSample)
	}
	if err := d.checkVPX(data); err != nil {
		return nil, err
	}
	v := d.vpx
	var (
		img       govpx.Image
		ok        bool
		corrupted bool
		err       error
	)
	if v.vp9 != nil {
		if err = v.vp9.DecodeWithPTS(data, uint64(pts)); err == nil {
			img, ok = v.vp9.NextFrame()
			corrupted, _ = v.vp9.LastFrameCorrupted()
		}
	} else {
		if err = v.vp8.DecodeWithPTS(data, uint64(pts)); err == nil {
			img, ok = v.vp8.NextFrame()
			corrupted, _ = v.vp8.LastFrameCorrupted()
		}
	}
	if err != nil {
		return nil, classifyVPX(err)
	}
	return d.vpxPicture(img, ok, corrupted, pts)
}

// vpxPicture turns the decoder's output for one sample into the frames to return: none for a
// hidden frame (references updated, nothing to show), one otherwise.
func (d *Decoder) vpxPicture(img govpx.Image, ok, corrupted bool, pts int64) ([]Frame, error) {
	if !ok {
		return nil, nil
	}
	f, err := d.vpxFrame(img, pts)
	if err != nil {
		return nil, err
	}
	f.Corrupt = corrupted
	return []Frame{f}, nil
}

// vpxFrame copies the decoder's picture, whose planes alias decoder storage, into a Frame.
func (d *Decoder) vpxFrame(img govpx.Image, pts int64) (Frame, error) {
	w, h := img.Width, img.Height
	var chroma Chroma
	switch img.Subsampling {
	case govpx.Subsampling420:
		chroma = Chroma420
	case govpx.Subsampling422:
		chroma = Chroma422
	case govpx.Subsampling444:
		chroma = Chroma444
	default: // 4:4:0, which Frame cannot describe
		return Frame{}, fmt.Errorf("%w: VP9 4:4:0 chroma sampling (Frame has 4:0:0, 4:2:0, 4:2:2 and 4:4:4)", ErrUnsupported)
	}
	depth := 8
	switch img.BitDepth {
	case 0, 8:
	case 10, 12:
		depth = int(img.BitDepth)
	default:
		return Frame{}, fmt.Errorf("%w: VP9 bit depth %d", ErrBitstream, img.BitDepth)
	}
	f := Frame{
		YStride: w * depthBytes(depth), Width: w, Height: h,
		Chroma: chroma, BitDepth: depth, BitDepthC: depth, PTS: pts,
	}
	cw, ch := f.ChromaSize()
	f.CStride = cw * depthBytes(depth)
	malformed := func() error {
		return fmt.Errorf("%w: picture %dx%d does not lie in its planes", ErrBitstream, w, h)
	}
	var ok bool
	if depth == 8 {
		ok = w > 0 && h > 0 && planeFits(img.Y, img.YStride, w, h) && planeFits(img.U, img.UStride, cw, ch) &&
			planeFits(img.V, img.VStride, cw, ch)
	} else {
		ok = w > 0 && h > 0 && planeFits(img.Y16, img.YStride, w, h) && planeFits(img.U16, img.UStride, cw, ch) &&
			planeFits(img.V16, img.VStride, cw, ch)
	}
	if !ok {
		return Frame{}, malformed()
	}
	// planeFits holds stride >= w, so each plane has at least w*h (cw*ch) samples: these products
	// are lengths of slices that exist, and cannot overflow int.
	f.Y, f.Cb, f.Cr = make([]byte, f.YStride*h), make([]byte, f.CStride*ch), make([]byte, f.CStride*ch)
	if depth == 8 {
		packPlane8(f.Y, img.Y, img.YStride, w, h)
		packPlane8(f.Cb, img.U, img.UStride, cw, ch)
		packPlane8(f.Cr, img.V, img.VStride, cw, ch)
	} else {
		packPlane16(f.Y, img.Y16, img.YStride, w, h)
		packPlane16(f.Cb, img.U16, img.UStride, cw, ch)
		packPlane16(f.Cr, img.V16, img.VStride, cw, ch)
	}
	d.nextIndex++
	return f, nil
}

func packPlane8(dst, src []byte, stride, w, h int) {
	for y := range h {
		copy(dst[y*w:(y+1)*w], src[y*stride:y*stride+w])
	}
}

// packPlane16 writes samples as little-endian 16-bit words, the Frame layout.
func packPlane16(dst []byte, src []uint16, stride, w, h int) {
	for y := range h {
		row := src[y*stride : y*stride+w]
		out := dst[y*w*2:]
		for x, v := range row {
			out[2*x], out[2*x+1] = byte(v), byte(v>>8)
		}
	}
}

// planeFits reports whether h rows of w samples at stride lie in p, computed in int64.
func planeFits[S ~[]E, E byte | uint16](p S, stride, w, h int) bool {
	return stride >= w && w > 0 && h > 0 && int64(len(p)) >= int64(h-1)*int64(stride)+int64(w)
}

// checkVPX refuses, before the decoder allocates anything, a frame that declares a picture over
// Config.MaxFrameMBs or a Frame over maxFrameBytes (at the largest VP9 sampling and depth).
func (d *Decoder) checkVPX(data []byte) error {
	var sizes [][2]int
	if d.vpx.vp9 != nil {
		sizes = vp9Sizes(data)
	} else if w, h, ok := vp8KeySize(data); ok {
		sizes = [][2]int{{w, h}}
	}
	for _, s := range sizes {
		w, h := s[0], s[1]
		mbs := int64((w+15)/16) * int64((h+15)/16)
		// The largest picture VP9 can declare is 4:4:4 at 12 bits; VP8 is 4:2:0 at 8.
		c, depth := Chroma420, 8
		if d.vpx.vp9 != nil {
			c, depth = Chroma444, 12
		}
		if _, _, _, ok := frameBytes(uint64(w), uint64(h), c, depth, depth); !ok {
			return hevcOverLimit(int64(w), int64(h))
		}
		if mbs > int64(d.cfg.frameMBs()) {
			return fmt.Errorf("%w: a %dx%d picture is %d macroblocks, the limit is %d",
				ErrOverLimit, w, h, mbs, d.cfg.frameMBs())
		}
	}
	return nil
}

// vp8KeySize reads the picture size of a VP8 key frame (RFC 6386 section 9.1): a three-byte
// frame tag whose bit 0 is 0 for a key frame, the start code 9d 01 2a, then two 14-bit sizes.
func vp8KeySize(data []byte) (w, h int, ok bool) {
	if len(data) < 10 || data[0]&1 != 0 || data[3] != 0x9d || data[4] != 0x01 || data[5] != 0x2a {
		return 0, 0, false
	}
	return int(data[6]) | int(data[7]&0x3f)<<8, int(data[8]) | int(data[9]&0x3f)<<8, true
}

// vp9Sizes lists the explicit picture sizes of the frames of a VP9 packet (a frame, or a
// superframe, Annex B of the specification).
func vp9Sizes(data []byte) [][2]int {
	var out [][2]int
	for _, fr := range vp9Frames(data) {
		if w, h, ok := vp9FrameSize(fr); ok {
			out = append(out, [2]int{w, h})
		}
	}
	return out
}

// vp9Frames splits a superframe by its index (VP9 specification Annex B); a packet without a
// well-formed index is one frame, as govpx reads it.
func vp9Frames(data []byte) [][]byte {
	n := len(data)
	m := data[n-1]
	if m&0xe0 != 0xc0 {
		return [][]byte{data}
	}
	count, mag := int(m&7)+1, int(m>>3&3)+1
	idx := 2 + mag*count
	if idx > n || data[n-idx] != m {
		return [][]byte{data}
	}
	var frames [][]byte
	pos, p := 0, n-idx+1
	for range count {
		sz := 0
		for b := range mag {
			sz |= int(data[p+b]) << (8 * b)
		}
		p += mag
		if sz > n-idx-pos {
			return [][]byte{data}
		}
		frames = append(frames, data[pos:pos+sz])
		pos += sz
	}
	return frames
}

// vp9FrameSize reads the explicit frame_size of an uncompressed header (clause 6.2); ok is false
// when the frame carries none (show_existing_frame, or a size copied from a reference), which an
// earlier frame already bounded. A header that ends early is left to the decoder to refuse.
func vp9FrameSize(fr []byte) (w, h int, ok bool) {
	r := bitReader{b: fr}
	if r.u(2) != 2 { // frame_marker
		return 0, 0, false
	}
	profile := r.u(1) | r.u(1)<<1
	if profile == 3 {
		r.u(1) // reserved_zero
	}
	if r.u(1) == 1 { // show_existing_frame
		return 0, 0, false
	}
	key, show, errRes := r.u(1) == 0, r.u(1), r.u(1)
	var intraOnly uint32
	// colorConfig skips color_config (clause 6.2.2), whose length depends on the profile.
	colorConfig := func() {
		if profile >= 2 {
			r.u(1) // ten_or_twelve_bit
		}
		odd := profile == 1 || profile == 3
		if r.u(3) != 7 { // color_space, not sRGB
			r.u(1) // color_range
			if odd {
				r.u(3) // subsampling_x, subsampling_y, reserved_zero
			}
		} else if odd {
			r.u(1) // reserved_zero
		}
	}
	switch {
	case key:
		r.u(24) // sync code
		colorConfig()
	default:
		if show == 0 {
			intraOnly = r.u(1)
		}
		if errRes == 0 {
			r.u(2) // reset_frame_context
		}
		switch {
		case intraOnly == 1:
			r.u(24) // sync code
			if profile > 0 {
				colorConfig()
			}
			r.u(8) // refresh_frame_flags
		default:
			r.u(8) // refresh_frame_flags
			for range 3 {
				r.u(4) // ref_frame_idx, sign_bias
			}
			for range 3 { // size_with_refs: found_ref
				if r.u(1) == 1 {
					return 0, 0, false
				}
			}
		}
	}
	w, h = int(r.u(16))+1, int(r.u(16))+1
	return w, h, !r.over
}

// bitReader reads bits most significant first; past the end it reads zeros and sets over.
type bitReader struct {
	b    []byte
	pos  int
	over bool
}

func (r *bitReader) u(n int) uint32 {
	var v uint32
	for range n {
		var bit uint32
		if r.pos>>3 < len(r.b) {
			bit = uint32(r.b[r.pos>>3]>>(7-r.pos&7)) & 1
		} else {
			r.over = true
		}
		r.pos++
		v = v<<1 | bit
	}
	return v
}

// classifyVPX maps govpx's errors onto this package's.
func classifyVPX(err error) error {
	switch {
	case errors.Is(err, govpx.ErrVP9NotImplemented):
		return fmt.Errorf("%w: %w", ErrUnsupported, err)
	case errors.Is(err, govpx.ErrFrameRejected):
		return fmt.Errorf("%w: %w", ErrOverLimit, err)
	case errors.Is(err, govpx.ErrClosed):
		return ErrClosed
	case errors.Is(err, govpx.ErrInvalidData), errors.Is(err, govpx.ErrInvalidVP9Data), errors.Is(err, govpx.ErrNeedKeyFrame):
		return fmt.Errorf("%w: %w", ErrBitstream, err)
	}
	return err
}
