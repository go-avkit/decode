// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-avkit/h265"
)

// sampleAt is the sample a synthetic picture holds at (x, y) of plane c: a different value for
// every plane and position, masked to the plane's depth, so that a wrong stride, plane or depth
// shows as a wrong sample.
func sampleAt(c, x, y, depth int) uint16 {
	return uint16((c*7919+x*131+y*17+1)*40503) & (1<<depth - 1)
}

// formatOutput is a decoded picture of w x h luma samples in a chroma format at two depths, the
// way the HEVC decoder gives it: byte planes at 8 bits and word planes above, rows padded to a
// stride of 3 more than the width, the padding filled with a value no sample takes.
func formatOutput(w, h int, chroma Chroma, depth, depthC int) h265.Output {
	var cw, ch int
	switch chroma {
	case Chroma420:
		cw, ch = (w+1)/2, (h+1)/2
	case Chroma422:
		cw, ch = (w+1)/2, h
	case Chroma444:
		cw, ch = w, h
	}
	plane := func(c, pw, ph, d int) h265.Plane {
		stride := pw + 3
		n := (ph-1)*stride + pw
		pl := h265.Plane{Width: pw, Height: ph, Stride: stride}
		if d == 8 {
			pl.Pix8 = make([]uint8, n)
		} else {
			pl.Pix16 = make([]uint16, n)
		}
		for i := range n {
			x, y := i%stride, i/stride
			v := uint16(0xffff)
			if x < pw {
				v = sampleAt(c, x, y, d)
			}
			if d == 8 {
				pl.Pix8[i] = uint8(v)
			} else {
				pl.Pix16[i] = v
			}
		}
		return pl
	}
	p := &h265.DecodedPicture{Components: 1, BitDepth: [3]int{depth, depthC, depthC}}
	p.Planes[0] = plane(0, w, h, depth)
	if chroma != Chroma400 {
		p.Components = 3
		p.Planes[1], p.Planes[2] = plane(1, cw, ch, depthC), plane(2, cw, ch, depthC)
	}
	return h265.Output{Picture: p, Tag: 5}
}

// checkFormatFrame verifies that f is the Frame of picture o in the layout of the design: the
// lengths and strides of the table, and every sample, from the planes' own At.
func checkFormatFrame(f Frame, o h265.Output, chroma Chroma, depth, depthC int) error {
	p := o.Picture
	wantC := depthC
	if chroma == Chroma400 {
		wantC = 0
	}
	// the picture does not say its chroma format, so a shape that two formats share (a single
	// row of 4:2:0 is one of 4:2:2; one sample is every one) is labelled by the larger format;
	// the bytes and the strides are the same either way
	sameShape := func(a, b Chroma) bool {
		aw, ah := Frame{Width: p.Planes[0].Width, Height: p.Planes[0].Height, Chroma: a}.ChromaSize()
		bw, bh := Frame{Width: p.Planes[0].Width, Height: p.Planes[0].Height, Chroma: b}.ChromaSize()
		return chroma != Chroma400 && f.Chroma != Chroma400 && aw == bw && ah == bh
	}
	if (f.Chroma != chroma && !sameShape(f.Chroma, chroma)) || f.BitDepth != depth || f.BitDepthC != wantC ||
		f.Width != p.Planes[0].Width || f.Height != p.Planes[0].Height {
		return fmt.Errorf("format %d %d/%d at %dx%d, want %d %d/%d", f.Chroma, f.BitDepth, f.BitDepthC, f.Width, f.Height, chroma, depth, wantC)
	}
	planes := [][]byte{f.Y, f.Cb, f.Cr}
	strides := []int{f.YStride, f.CStride, f.CStride}
	cw, ch := f.ChromaSize()
	for c := range 3 {
		w, h := f.Width, f.Height
		if c > 0 {
			w, h = cw, ch
		}
		b := f.SampleBytes(c)
		if b == 0 {
			if planes[c] != nil || strides[c] != 0 {
				return fmt.Errorf("plane %d is not absent: %d bytes, stride %d", c, len(planes[c]), strides[c])
			}
			continue
		}
		if len(planes[c]) != w*h*b || strides[c] != w*b {
			return fmt.Errorf("plane %d: %d bytes stride %d, want %d and %d", c, len(planes[c]), strides[c], w*h*b, w*b)
		}
		for y := range h {
			for x := range w {
				want := p.Planes[c].At(x, y)
				got := uint16(planes[c][y*strides[c]+x*b])
				if b == 2 {
					got |= uint16(planes[c][y*strides[c]+x*b+1]) << 8
				}
				if got != want {
					return fmt.Errorf("plane %d sample (%d,%d): %#x, want %#x", c, x, y, got, want)
				}
			}
		}
	}
	if f.Corrupt != o.Corrupt {
		return errors.New("corrupt flag lost")
	}
	return nil
}

// TestHEVCFrameEveryFormat converts a synthetic picture of every chroma format at every pair of
// depths a stream can state, 8 to 16 each (equal and unequal), with odd and even sizes, and
// reads every sample back.
func TestHEVCFrameEveryFormat(t *testing.T) {
	d, _ := NewHEVC(Config{}, 4)
	depths := []int{8, 9, 10, 11, 12, 13, 14, 15, 16}
	for _, chroma := range []Chroma{Chroma400, Chroma420, Chroma422, Chroma444} {
		for _, size := range [][2]int{{8, 6}, {7, 5}, {5, 2}, {16, 3}} {
			for _, depth := range depths {
				for _, depthC := range depths {
					o := formatOutput(size[0], size[1], chroma, depth, depthC)
					f, err := d.hevcFrame(o)
					if err == nil {
						err = checkFormatFrame(f, o, chroma, depth, depthC)
					}
					if err != nil {
						t.Fatalf("chroma %d %dx%d %d/%d: %v", chroma, size[0], size[1], depth, depthC, err)
					}
				}
			}
		}
	}
}

// TestHEVCFrameIsRefusedWhenItIsNotAFrame: every way a picture can fail to be the shape of a
// Frame is refused as malformed, with both sides of each guard (the accepted picture next to it).
func TestHEVCFrameIsRefusedWhenItIsNotAFrame(t *testing.T) {
	d, _ := NewHEVC(Config{}, 4)
	for name, tc := range map[string]struct {
		chroma        Chroma
		depth, depthC int
		mutate        func(*h265.DecodedPicture)
	}{
		"wrong chroma width":    {Chroma420, 10, 10, func(p *h265.DecodedPicture) { p.Planes[1].Width, p.Planes[2].Width = 3, 3 }},
		"wrong chroma height":   {Chroma422, 10, 10, func(p *h265.DecodedPicture) { p.Planes[1].Height, p.Planes[2].Height = 2, 2 }},
		"Cr is not Cb's width":  {Chroma444, 8, 8, func(p *h265.DecodedPicture) { p.Planes[2].Width = 7 }},
		"Cr is not Cb's height": {Chroma444, 8, 8, func(p *h265.DecodedPicture) { p.Planes[2].Height = 5 }},
		"Cr depth differs":      {Chroma444, 10, 10, func(p *h265.DecodedPicture) { p.BitDepth[2] = 12 }},
		"luma under 8 bits":     {Chroma420, 8, 8, func(p *h265.DecodedPicture) { p.BitDepth[0] = 7 }},
		"luma over 16 bits":     {Chroma420, 10, 10, func(p *h265.DecodedPicture) { p.BitDepth[0] = 17 }},
		"chroma under 8 bits":   {Chroma444, 8, 8, func(p *h265.DecodedPicture) { p.BitDepth[1], p.BitDepth[2] = 7, 7 }},
		"chroma over 16 bits":   {Chroma422, 10, 10, func(p *h265.DecodedPicture) { p.BitDepth[1], p.BitDepth[2] = 17, 17 }},
		"a word plane at 8":     {Chroma420, 8, 8, func(p *h265.DecodedPicture) { p.Planes[0].Pix16, p.Planes[0].Pix8 = make([]uint16, 64), nil }},
		"a byte plane at 10":    {Chroma420, 10, 10, func(p *h265.DecodedPicture) { p.Planes[0].Pix8, p.Planes[0].Pix16 = make([]uint8, 64), nil }},
		"a byte chroma at 12":   {Chroma420, 12, 12, func(p *h265.DecodedPicture) { p.Planes[1].Pix8, p.Planes[1].Pix16 = make([]uint8, 64), nil }},
		"a word luma at 8/12":   {Chroma420, 8, 12, func(p *h265.DecodedPicture) { p.Planes[0].Pix16, p.Planes[0].Pix8 = make([]uint16, 64), nil }},
		"stride under width":    {Chroma400, 8, 8, func(p *h265.DecodedPicture) { p.Planes[0].Stride = 7 }},
		"a short plane":         {Chroma400, 16, 16, func(p *h265.DecodedPicture) { p.Planes[0].Pix16 = p.Planes[0].Pix16[:20] }},
		"a short chroma":        {Chroma444, 8, 8, func(p *h265.DecodedPicture) { p.Planes[2].Pix8 = p.Planes[2].Pix8[:10] }},
		"no planes":             {Chroma400, 8, 8, func(p *h265.DecodedPicture) { p.Planes[0] = h265.Plane{} }},
	} {
		o := formatOutput(8, 6, tc.chroma, tc.depth, tc.depthC)
		if f, err := d.hevcFrame(o); err != nil || f.Chroma != tc.chroma {
			t.Fatalf("%s: the unmutated picture: %v", name, err)
		}
		tc.mutate(o.Picture)
		noPanic(t, name, func() {
			if _, err := d.hevcFrame(o); !errors.Is(err, ErrBitstream) {
				t.Errorf("%s: %v, want a malformed picture", name, err)
			}
		})
	}
}

// TestHEVCFrameMonochromeIgnoresTheChromaDepth: a 4:0:0 picture has no chroma plane to hold a
// depth against, whatever BitDepth[1] says, and its Frame says 0.
func TestHEVCFrameMonochromeIgnoresTheChromaDepth(t *testing.T) {
	d, _ := NewHEVC(Config{}, 4)
	for _, bad := range []int{0, 7, 17, 12} {
		o := formatOutput(8, 6, Chroma400, 10, 10)
		o.Picture.BitDepth[1] = bad
		f, err := d.hevcFrame(o)
		if err != nil || f.BitDepthC != 0 || f.Cb != nil || f.Cr != nil || f.CStride != 0 || len(f.Y) != 8*6*2 {
			t.Errorf("chroma depth %d: %v %+v", bad, err, f)
		}
	}
}

func FuzzHEVCFrame(f *testing.F) {
	f.Add(uint8(8), uint8(6), uint8(1), uint8(8), uint8(8), uint8(0), uint16(0), uint8(0))
	f.Add(uint8(7), uint8(5), uint8(3), uint8(16), uint8(16), uint8(1), uint16(0), uint8(0))
	f.Add(uint8(16), uint8(3), uint8(2), uint8(12), uint8(8), uint8(0), uint16(0), uint8(1))
	f.Add(uint8(9), uint8(9), uint8(0), uint8(9), uint8(0), uint8(2), uint16(7), uint8(0))
	f.Add(uint8(4), uint8(4), uint8(1), uint8(10), uint8(10), uint8(0), uint16(3), uint8(2))
	f.Fuzz(func(t *testing.T, w, h, chromaSel, depth, depthC, strideMore uint8, cut uint16, flip uint8) {
		d, _ := NewHEVC(Config{}, 4)
		chroma := Chroma(chromaSel % 4)
		depth, depthC = depth%20, depthC%20 // reaches under 8 and over 16
		wi, hi := int(w%33)+1, int(h%33)+1
		// a well-formed picture is converted exactly; an ill-formed one is refused, never copied
		valid := depth >= 8 && depth <= 16 && (chroma == Chroma400 || (depthC >= 8 && depthC <= 16))
		o := formatOutput(wi, hi, chroma, 10, 10)
		if valid {
			o = formatOutput(wi, hi, chroma, int(depth), int(depthC))
		}
		p := o.Picture
		p.BitDepth = [3]int{int(depth), int(depthC), int(depthC)}
		damaged := !valid
		for c := range p.Components {
			pl := &p.Planes[c]
			if flip&(1<<c) != 0 { // the plane's kind no longer says its depth
				damaged = true
				if pl.Pix8 != nil {
					pl.Pix8, pl.Pix16 = nil, make([]uint16, pl.Len())
				} else {
					pl.Pix16, pl.Pix8 = nil, make([]uint8, pl.Len())
				}
			}
		}
		if cut > 0 && cut < 200 { // a plane shorter than its rows
			n := p.Planes[0].Len() - int(cut)
			switch {
			case n < 0: // the whole plane cut away
				n = 0
				damaged = true
			case n < p.Planes[0].Len():
				damaged = true
			}
			if p.Planes[0].Pix8 != nil {
				p.Planes[0].Pix8 = p.Planes[0].Pix8[:n]
			} else {
				p.Planes[0].Pix16 = p.Planes[0].Pix16[:n]
			}
		}
		if strideMore%4 == 3 { // a stride under the width
			p.Planes[0].Stride = p.Planes[0].Width - 1
			damaged = true
		}
		fr, err := d.hevcFrame(o)
		switch {
		case err != nil:
			if !errors.Is(err, ErrBitstream) && !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrOverLimit) {
				t.Fatalf("unclassified refusal: %v", err)
			}
			if !damaged && valid {
				t.Fatalf("a well-formed picture was refused: %v", err)
			}
		case damaged:
			// the damage may leave a picture that is still whole (a cut of nothing); then it must
			// be exactly the picture
			fallthrough
		default:
			if cerr := checkFormatFrame(fr, o, chroma, int(depth), int(depthC)); cerr != nil {
				t.Fatalf("%v", cerr)
			}
		}
	})
}

// FuzzHEVCFormat: whatever an SPS states, hevcFormat answers nil or a refusal, never a panic, and
// answers nil exactly for a Frame that fits and is not coded as separate colour planes.
func FuzzHEVCFormat(f *testing.F) {
	f.Add(uint8(1), uint8(8), uint8(8), uint8(0), uint32(64), uint32(64))
	f.Add(uint8(3), uint8(16), uint8(16), uint8(0), uint32(1<<20), uint32(1<<20))
	f.Add(uint8(3), uint8(8), uint8(8), uint8(1), uint32(64), uint32(64))
	f.Add(uint8(0), uint8(12), uint8(8), uint8(0), uint32(8), uint32(89478480))
	f.Add(uint8(2), uint8(9), uint8(16), uint8(0), uint32(8), uint32(8))
	f.Fuzz(func(t *testing.T, chroma, depth, depthC, separate uint8, w, h uint32) {
		chroma, depth, depthC, separate = chroma%4, 8+depth%9, 8+depthC%9, separate%2
		w, h = w&^7|8, h&^7|8 // a multiple of the 8-sample coding block, at least one block
		u, ok := hevcUnit(formatSPS(uint64(w), uint64(h), uint64(chroma), uint64(depth), uint64(depthC), uint64(separate)), 0)
		if !ok {
			t.Fatal("not a unit")
		}
		d, _ := NewHEVC(fuzzConfig, 4)
		err := d.hevcFormat(u)
		_, _, _, fits := frameBytes(uint64(w), uint64(h), Chroma(chroma), int(depth), int(depthC))
		switch {
		case err == nil && !fits:
			t.Fatalf("a frame over the cap was accepted: %dx%d chroma %d %d/%d", w, h, chroma, depth, depthC)
		case err == nil && chroma == 3 && separate == 1:
			t.Fatal("separate colour planes were accepted")
		case err != nil && !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrOverLimit):
			t.Fatalf("unclassified refusal: %v", err)
		}
	})
}
