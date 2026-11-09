// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"os"
	"path/filepath"
	"testing"
)

// The HEVC fuzz targets mirror the H.264 ones: the hvcC record, a length-prefixed
// sample and a raw Annex B chunk. The corpus in testdata/fuzz is written by
// `jvt seeds` from the committed .265 streams. The frame limit of fuzzConfig is
// the bound under test: a mutated SPS asking for more must be refused.

func checkFrames(t *testing.T, fs ...[]Frame) {
	t.Helper()
	for _, set := range fs {
		for _, fr := range set {
			if fr.Width <= 0 || fr.Height <= 0 || fr.Width*fr.Height > fuzzConfig.MaxFrameMBs*256 {
				t.Fatalf("picture %dx%d breaks the %d macroblock bound", fr.Width, fr.Height, fuzzConfig.MaxFrameMBs)
			}
		}
	}
}

func FuzzParseHVCC(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := ParseHVCC(b)
		if err != nil {
			return
		}
		for _, s := range append(append(c.VPS, c.SPS...), c.PPS...) {
			if len(s) == 0 || len(s) > len(b) {
				t.Fatalf("parameter set of %d bytes from a record of %d", len(s), len(b))
			}
		}
		if c.LengthSize != 1 && c.LengthSize != 2 && c.LengthSize != 4 {
			t.Fatalf("length size %d", c.LengthSize)
		}
		if d, err := NewHVCC(b, fuzzConfig); err == nil {
			d.Close()
		}
	})
}

func FuzzHEVCSample(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := NewHEVC(fuzzConfig, 4)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		frames, _ := d.DecodeSample(b, 0)
		more, _ := d.DecodeSample(b, 1)
		last, _ := d.Flush()
		checkFrames(t, frames, more, last)
	})
}

func FuzzHEVCAnnexB(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := NewHEVC(fuzzConfig, 4)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		half := len(b) / 2
		frames, _ := d.DecodeAnnexB(b[:half])
		more, _ := d.DecodeAnnexB(b[half:])
		last, _ := d.Flush()
		checkFrames(t, frames, more, last)
	})
}

func TestHEVCFuzzCorpusIsCommitted(t *testing.T) {
	for _, target := range []string{"FuzzParseHVCC", "FuzzHEVCSample", "FuzzHEVCAnnexB"} {
		entries, err := os.ReadDir(filepath.Join("testdata", "fuzz", target))
		if err != nil || len(entries) < 6 {
			t.Errorf("%s: corpus of %d entries (%v), want the seeds from `jvt seeds`", target, len(entries), err)
		}
	}
}
