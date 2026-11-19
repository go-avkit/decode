// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-avkit/h265"
)

// TestHEVCErrorClasses: the decoder's errors as this package reports them.
func TestHEVCErrorClasses(t *testing.T) {
	d, _ := NewHEVC(Config{}, 4)
	for name, c := range map[string]struct {
		err  error
		want []error
	}{
		"not implemented":      {h265.ErrNotReconstructed, []error{ErrUnsupported}},
		"a coding extension":   {fmt.Errorf("%w: a 3D or screen content coding extension", h265.ErrUnsupportedSPS), []error{ErrUnsupported}},
		"a field out of range": {fmt.Errorf("%w: sps_max_sub_layers_minus1 of 7", h265.ErrUnsupportedSPS), []error{ErrBitstream}},
		"picture too large":    {h265.ErrPictureTooLarge, []error{ErrOverLimit, ErrUnsupported}},
		"buffer too large":     {h265.ErrDPBTooLarge, []error{ErrOverLimit, ErrUnsupported}},
		"corrupt picture":      {h265.ErrCorruptPicture, []error{ErrBitstream}},
		"missing reference":    {h265.ErrMissingReference, []error{ErrBitstream}},
		"missing set":          {h265.ErrMissingParameterSet, []error{ErrBitstream}},
	} {
		got := d.classifyHEVC(c.err)
		for _, w := range c.want {
			if !errors.Is(got, w) {
				t.Errorf("%s: %v is not %v", name, got, w)
			}
		}
	}
	if got := d.classifyHEVC(errors.New("other")); got.Error() != "other" {
		t.Errorf("an unknown error became %v", got)
	}
}

// TestHEVCConversionErrorsAreReturnedWithThePictures: a picture that cannot be converted is
// reported with the others, from the units (the first picture out) and from Flush (the last).
func TestHEVCConversionErrorsAreReturnedWithThePictures(t *testing.T) {
	old := hevcFramer
	defer func() { hevcFramer = old }()
	raw := readFile(t, "testdata/hevc/streams/main_ipb.265")
	for _, failPOC := range []int32{0, 11} {
		hevcFramer = func(d *Decoder, o h265.Output) (Frame, error) {
			if o.POC == failPOC {
				return Frame{}, fmt.Errorf("%w: a made-up refusal", ErrUnsupported)
			}
			return old(d, o)
		}
		d, _ := NewHEVC(Config{}, 4)
		var frames []Frame
		var errs []error
		fs, err := d.DecodeAnnexB(raw)
		frames = append(frames, fs...)
		errs = append(errs, err)
		fs, err = d.Flush()
		frames = append(frames, fs...)
		errs = append(errs, err)
		d.Close()
		if !errors.Is(errors.Join(errs...), ErrUnsupported) || len(frames) == 0 {
			t.Errorf("POC %d refused: %d pictures, %v", failPOC, len(frames), errs)
		}
	}
}

// TestHEVCUnitTooShort: an Annex B unit of one byte, which has no header, is malformed.
func TestHEVCUnitTooShort(t *testing.T) {
	d, _ := NewHEVC(Config{}, 4)
	defer d.Close()
	if _, err := d.DecodeAnnexB([]byte{0, 0, 1, 0x42, 0, 0, 1, 0x40, 1}); !errors.Is(err, ErrBitstream) {
		t.Errorf("a one-byte unit: %v", err)
	}
	d2, _ := NewHEVC(Config{}, 4)
	defer d2.Close()
	if _, err := d2.DecodeAnnexB([]byte{0, 0, 1, 0x42}); err != nil {
		t.Fatal(err)
	}
	if _, err := d2.Flush(); !errors.Is(err, ErrBitstream) {
		t.Errorf("a held one-byte unit: %v", err)
	}
}
