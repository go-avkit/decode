// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"os"
	"path/filepath"
	"testing"
)

// The fuzz targets exercise every entry point that takes bytes from a file: the
// avcC record, a length-prefixed sample, and a raw Annex B chunk. What they
// assert is that nothing panics, and that the work a stream can demand stays
// inside the configured bounds. The corpus is in testdata/fuzz, written by
// `jvt seeds` from the committed streams (real streams cut short, so that
// mutation starts from well formed parameter sets and slice headers).
//
// A frame limit of 396 macroblocks (CIF) keeps each execution fast and is the
// bound under test: a mutated SPS asking for more must be refused, not decoded.
var fuzzConfig = Config{MaxFrameMBs: 396, MaxSampleBytes: 1 << 20}

func FuzzParseAVCC(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := ParseAVCC(b)
		if err != nil {
			return
		}
		for _, s := range append(c.SPS, c.PPS...) {
			if len(s) == 0 || len(s) > len(b) {
				t.Fatalf("parameter set of %d bytes from a record of %d", len(s), len(b))
			}
		}
		if c.LengthSize != 1 && c.LengthSize != 2 && c.LengthSize != 4 {
			t.Fatalf("length size %d", c.LengthSize)
		}
	})
}

func FuzzDecodeSample(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := New(fuzzConfig, 4)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		frames, _ := d.DecodeSample(b, 0)
		more, _ := d.DecodeSample(b, 1)
		last, _ := d.Flush()
		for _, fr := range append(append(frames, more...), last...) {
			if fr.Width <= 0 || fr.Height <= 0 || fr.Width*fr.Height > fuzzConfig.MaxFrameMBs*256 {
				t.Fatalf("picture %dx%d breaks the %d macroblock bound", fr.Width, fr.Height, fuzzConfig.MaxFrameMBs)
			}
		}
	})
}

func FuzzDecodeAnnexB(f *testing.F) {
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := New(fuzzConfig, 4)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		half := len(b) / 2
		frames, _ := d.DecodeAnnexB(b[:half])
		more, _ := d.DecodeAnnexB(b[half:])
		last, _ := d.Flush()
		for _, fr := range append(append(frames, more...), last...) {
			if fr.Width <= 0 || fr.Height <= 0 || fr.Width*fr.Height > fuzzConfig.MaxFrameMBs*256 {
				t.Fatalf("picture %dx%d breaks the %d macroblock bound", fr.Width, fr.Height, fuzzConfig.MaxFrameMBs)
			}
		}
	})
}

// The committed corpus must exist, or `go test` would run the targets on
// nothing and the fuzzing above would look like it was covered.
func TestFuzzCorpusIsCommitted(t *testing.T) {
	for _, target := range []string{"FuzzParseAVCC", "FuzzDecodeSample", "FuzzDecodeAnnexB"} {
		entries, err := os.ReadDir(filepath.Join("testdata", "fuzz", target))
		if err != nil || len(entries) < 10 {
			t.Errorf("%s: corpus of %d entries (%v), want the seeds from `jvt seeds`", target, len(entries), err)
		}
	}
}
