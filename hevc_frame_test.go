// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/go-avkit/h265"
)

// hevcOutput is a decoded picture the way the HEVC decoder gives it: planes that are windows
// of larger ones (stride 8 and 4, the window 4 x 2 at (2, 2) of an 8 x 4 picture).
func hevcOutput(depth int) h265.Output {
	pl := func(w, h, stride int, base uint16) h265.Plane {
		pix := make([]uint16, (h-1)*stride+w)
		for i := range pix {
			pix[i] = base + uint16(i)
		}
		return h265.Plane{Width: w, Height: h, Stride: stride, Pix: pix}
	}
	base := uint16(18)
	if depth > 8 {
		base = 0x112
	}
	return h265.Output{
		Picture: &h265.DecodedPicture{
			Components: 3, BitDepth: [3]int{depth, depth, depth},
			Planes: [3]h265.Plane{pl(4, 2, 8, base), pl(2, 1, 4, 5), pl(2, 1, 4, 7)},
		},
		POC: 3, Tag: 9,
	}
}

func TestHEVCFrame(t *testing.T) {
	d, _ := NewHEVC(Config{}, 4)
	f, err := d.hevcFrame(hevcOutput(8))
	if err != nil || !slices.Equal(f.Y, []byte{18, 19, 20, 21, 26, 27, 28, 29}) || f.Width != 4 || f.Height != 2 || f.PTS != 0 ||
		!slices.Equal(f.Cb, []byte{5, 6}) || !slices.Equal(f.Cr, []byte{7, 8}) || f.YStride != 4 || f.CStride != 2 {
		t.Fatalf("8-bit: %v %+v", err, f)
	}
	d.sampleMode = true // the tag of the first slice is the sample's time
	f, err = d.hevcFrame(hevcOutput(10))
	if err != nil || !slices.Equal(f.Y[:4], []byte{0x12, 0x01, 0x13, 0x01}) || f.BitDepth != 10 || f.PTS != 9 || f.YStride != 8 || f.CStride != 4 {
		t.Fatalf("10-bit: %v %+v", err, f)
	}
	o := hevcOutput(8)
	o.Corrupt = true
	if f, _ = d.hevcFrame(o); !f.Corrupt {
		t.Error("a corrupt picture was reported whole")
	}

	// Formats a Frame cannot carry are refused by name.
	refused := map[string]struct {
		mutate func(*h265.Output)
		names  string
	}{
		"4:2:2": {func(o *h265.Output) {
			o.Picture.Planes[1], o.Picture.Planes[2] = h265.Plane{Width: 2, Height: 2, Stride: 2, Pix: make([]uint16, 4)}, h265.Plane{Width: 2, Height: 2, Stride: 2, Pix: make([]uint16, 4)}
		}, "4:2:2"},
		"4:4:4": {func(o *h265.Output) {
			o.Picture.Planes[1], o.Picture.Planes[2] = h265.Plane{Width: 4, Height: 2, Stride: 4, Pix: make([]uint16, 8)}, h265.Plane{Width: 4, Height: 2, Stride: 4, Pix: make([]uint16, 8)}
		}, "4:4:4"},
		"4:0:0":          {func(o *h265.Output) { o.Picture.Components = 1 }, "4:0:0"},
		"unequal depths": {func(o *h265.Output) { o.Picture.BitDepth[1] = 10 }, "8/10"},
		"12 bits":        {func(o *h265.Output) { o.Picture.BitDepth = [3]int{12, 12, 12} }, "12/12"},
		"16 bits":        {func(o *h265.Output) { o.Picture.BitDepth = [3]int{16, 16, 16} }, "16/16"},
		"no picture":     {func(o *h265.Output) { o.Picture = nil }, "0/0"},
	}
	for name, c := range refused {
		o := hevcOutput(8)
		c.mutate(&o)
		_, err := d.hevcFrame(o)
		if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), c.names) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A picture that does not lie in its planes is a malformed one.
	for name, mutate := range map[string]func(*h265.Output){
		"an empty plane":     func(o *h265.Output) { o.Picture.Planes[0].Width = 0 },
		"a short plane":      func(o *h265.Output) { o.Picture.Planes[0].Pix = o.Picture.Planes[0].Pix[:5] },
		"a short chroma":     func(o *h265.Output) { o.Picture.Planes[2].Pix = o.Picture.Planes[2].Pix[:1] },
		"a stride too small": func(o *h265.Output) { o.Picture.Planes[1].Stride = 1 },
		"odd chroma":         func(o *h265.Output) { o.Picture.Planes[1].Width = 1 },
	} {
		o := hevcOutput(8)
		mutate(&o)
		if _, err := d.hevcFrame(o); !errors.Is(err, ErrBitstream) && !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s was accepted: %v", name, err)
		}
	}

	// Every picture of a batch is converted; the first error is the one returned.
	good := hevcOutput(8)
	wrong, again := hevcOutput(8), hevcOutput(8)
	wrong.Picture.BitDepth = [3]int{12, 12, 12}
	again.Picture.Components = 1
	fs, err := d.hevcFrames([]h265.Output{wrong, good, again})
	if len(fs) != 1 || !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "12/12") {
		t.Errorf("a batch: %d pictures, %v", len(fs), err)
	}
}

// TestHEVCFrameIsOwned: the Frame is a copy, so a caller that writes into it (or keeps it) never
// touches the decoder's picture, which later pictures are predicted from.
func TestHEVCFrameIsOwned(t *testing.T) {
	d, _ := NewHEVC(Config{}, 4)
	o := hevcOutput(8)
	before := slices.Clone(o.Picture.Planes[0].Pix)
	f, err := d.hevcFrame(o)
	if err != nil {
		t.Fatal(err)
	}
	for i := range f.Y {
		f.Y[i] ^= 0xff
	}
	if !slices.Equal(o.Picture.Planes[0].Pix, before) {
		t.Error("writing to the Frame changed the decoder's picture")
	}
}

// TestHEVCSampleHeaders: the two header bytes of a NAL unit, as the decoder reads them.
func TestHEVCSampleHeaders(t *testing.T) {
	u, ok := hevcUnit([]byte{0x42, 0x01, 0xaa}, 7)
	if !ok || u.Type != h265.UnitSPS || u.LayerID != 0 || u.TemporalID != 0 || u.Tag != 7 || len(u.Payload) != 1 {
		t.Errorf("an SPS: %v %+v", ok, u)
	}
	u, ok = hevcUnit([]byte{0x03, 0x0a, 1}, 0) // layer 1 (bit 0 of byte 0 is the top bit of the layer), temporal id 1
	if !ok || u.LayerID != 1<<5|1 || u.TemporalID != 1 {
		t.Errorf("a layer: %v %+v", ok, u)
	}
	// zero bytes after the payload are the framing's, not the unit's
	u, ok = hevcUnit([]byte{0x26, 0x01, 0xaf, 0x00, 0x00}, 0)
	if !ok || !slices.Equal(u.Payload, []byte{0xaf}) {
		t.Errorf("a trailing zero: %v %+v", ok, u)
	}
	for name, raw := range map[string][]byte{"forbidden bit": {0x80, 1}, "one byte": {0x42}, "temporal id plus 1 of 0": {0x42, 0x00}} {
		if _, ok := hevcUnit(raw, 0); ok {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestHEVCFormatIsRefusedAtTheParameterSet: a format a Frame cannot carry is refused when its SPS
// arrives, before a picture of it is decoded.
func TestHEVCFormatIsRefusedAtTheParameterSet(t *testing.T) {
	raw := readFile(t, "testdata/hevc/streams/yuv444_unsupported.265")
	d, _ := NewHEVC(Config{}, 4)
	defer d.Close()
	fs, err := d.DecodeAnnexB(raw)
	if err == nil {
		var rest []Frame
		rest, err = d.Flush()
		fs = append(fs, rest...)
	}
	if !errors.Is(err, ErrUnsupported) || len(fs) != 0 || !strings.Contains(err.Error(), "4:4:4") {
		t.Errorf("4:4:4: %d pictures, %v", len(fs), err)
	}
	// and in a configuration record
	vps, sps, pps, _, _ := hevcStream(t, "testdata/hevc/streams/yuv444_unsupported.265")
	if _, err := NewHVCC(hvcC(vps, sps, pps, 4), Config{}); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "4:4:4") {
		t.Errorf("4:4:4 hvcC: %v", err)
	}
}
