// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"errors"
	"testing"

	"github.com/thesyncim/govpx"
)

// An empty sample is ErrSample for both codecs, not a decoder error.
func TestVPXEmptySample(t *testing.T) {
	for name, mk := range map[string]func(Config) (*Decoder, error){"vp8": NewVP8, "vp9": NewVP9} {
		d, err := mk(fuzzConfig)
		if err != nil {
			t.Fatal(err)
		}
		_, err = d.DecodeSample(nil, 0)
		if !errors.Is(err, ErrSample) {
			t.Errorf("%s: empty sample gave %v, want ErrSample", name, err)
		}
		d.Close()
	}
}

// vpxFrame refuses a picture that does not lie in its planes, and copies one that does.
func TestVPXFrameRefusesShortPlanes(t *testing.T) {
	d, err := NewVP8(fuzzConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	good := govpx.Image{
		Width: 3, Height: 3,
		Y: make([]byte, 9), U: make([]byte, 4), V: make([]byte, 4),
		YStride: 3, UStride: 2, VStride: 2,
	}
	if f, err := d.vpxFrame(good, 7); err != nil || f.Width != 3 || len(f.Cb) != 4 || f.PTS != 7 {
		t.Fatalf("good picture: %+v, %v", f, err)
	}
	for name, mutate := range map[string]func(*govpx.Image){
		"zero width":  func(i *govpx.Image) { i.Width = 0 },
		"zero height": func(i *govpx.Image) { i.Height = 0 },
		"short Y":     func(i *govpx.Image) { i.Y = i.Y[:8] },
		"short U":     func(i *govpx.Image) { i.U = i.U[:3] },
		"short V":     func(i *govpx.Image) { i.V = i.V[:3] },
		"narrow Y":    func(i *govpx.Image) { i.YStride = 2 },
	} {
		img := good
		mutate(&img)
		if _, err := d.vpxFrame(img, 0); !errors.Is(err, ErrBitstream) {
			t.Errorf("%s: %v, want ErrBitstream", name, err)
		}
	}
}

// vp9Frames: the superframe index is honoured exactly at its edges.
func TestVP9FramesIndex(t *testing.T) {
	const m = 0xc1 // marker, one size byte, two frames
	cases := []struct {
		name string
		data []byte
		want [][]byte
	}{
		{"two frames", []byte{'A', 'A', 'A', 'B', 'B', m, 3, 2, m}, [][]byte{[]byte("AAA"), []byte("BB")}},
		{"index is the whole packet", []byte{m, 0, 0, m}, [][]byte{{}, {}}},
		{"index longer than the packet", []byte{'A', 'B', m}, [][]byte{{'A', 'B', m}}},
		{"first marker byte differs", []byte{'A', 'A', 'A', 'B', 'B', 0xc0, 3, 2, m}, [][]byte{{'A', 'A', 'A', 'B', 'B', 0xc0, 3, 2, m}}},
		{"sizes exceed the packet", []byte{'A', 'A', 'A', 'B', 'B', m, 3, 3, m}, [][]byte{{'A', 'A', 'A', 'B', 'B', m, 3, 3, m}}},
		{"no index", []byte{'A', 'B'}, [][]byte{{'A', 'B'}}},
	}
	for _, c := range cases {
		got := vp9Frames(c.data)
		if len(got) != len(c.want) {
			t.Errorf("%s: %d frames, want %d", c.name, len(got), len(c.want))
			continue
		}
		for i := range got {
			if !bytes.Equal(got[i], c.want[i]) {
				t.Errorf("%s: frame %d = %q, want %q", c.name, i, got[i], c.want[i])
			}
		}
	}
}

// Flush has nothing to give back (no reordering) and Close releases the govpx decoder.
func TestVPXFlushAndClose(t *testing.T) {
	for name, mk := range map[string]func(Config) (*Decoder, error){"vp8": NewVP8, "vp9": NewVP9} {
		d, err := mk(fuzzConfig)
		if err != nil {
			t.Fatal(err)
		}
		if frames, err := d.Flush(); frames != nil || err != nil {
			t.Errorf("%s: Flush = %v, %v, want nil, nil", name, frames, err)
		}
		if _, err := d.DecodeAnnexB([]byte{0}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: DecodeAnnexB = %v, want ErrUnsupported", name, err)
		}
		if err := d.Close(); err != nil {
			t.Errorf("%s: Close = %v", name, err)
		}
		if d.vpx != nil {
			t.Errorf("%s: Close left the govpx decoder", name)
		}
		if err := d.Close(); err != nil {
			t.Errorf("%s: second Close = %v", name, err)
		}
	}
}

// A hidden frame yields nothing; a picture outside its planes is refused; Corrupt is carried.
func TestVPXPicture(t *testing.T) {
	d, err := NewVP9(fuzzConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	good := govpx.Image{
		Width: 2, Height: 2,
		Y: make([]byte, 4), U: make([]byte, 1), V: make([]byte, 1),
		YStride: 2, UStride: 1, VStride: 1,
	}
	if fr, err := d.vpxPicture(govpx.Image{}, false, false, 0); fr != nil || err != nil {
		t.Errorf("hidden frame: %v, %v, want nil, nil", fr, err)
	}
	if _, err := d.vpxPicture(govpx.Image{Width: 2, Height: 2}, true, false, 0); !errors.Is(err, ErrBitstream) {
		t.Errorf("empty planes: %v, want ErrBitstream", err)
	}
	fr, err := d.vpxPicture(good, true, true, 5)
	if err != nil || len(fr) != 1 || !fr[0].Corrupt || fr[0].PTS != 5 {
		t.Errorf("good picture: %+v, %v", fr, err)
	}
	if fr, _ := d.vpxPicture(good, true, false, 5); len(fr) != 1 || fr[0].Corrupt {
		t.Errorf("clean picture flagged corrupt: %+v", fr)
	}
}

// A refusal of the govpx constructor is ErrConfig.
func TestVPXConstructorRefusal(t *testing.T) {
	boom := errors.New("boom")
	old8, old9 := newGovpx8, newGovpx9
	defer func() { newGovpx8, newGovpx9 = old8, old9 }()
	newGovpx8 = func(govpx.DecoderOptions) (*govpx.VP8Decoder, error) { return nil, boom }
	newGovpx9 = func(govpx.VP9DecoderOptions) (*govpx.VP9Decoder, error) { return nil, boom }
	for name, mk := range map[string]func(Config) (*Decoder, error){"vp8": NewVP8, "vp9": NewVP9} {
		d, err := mk(fuzzConfig)
		if d != nil || !errors.Is(err, ErrConfig) || !errors.Is(err, boom) {
			t.Errorf("%s: %v, %v, want ErrConfig wrapping the cause", name, d, err)
		}
	}
}
