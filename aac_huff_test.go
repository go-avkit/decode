// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"strings"
	"testing"
)

// TestAACCodebooksAreCompletePrefixCodes checks the twelve normative codebooks:
// every one is prefix free and complete, with the size the standard gives it.
func TestAACCodebooksAreCompletePrefixCodes(t *testing.T) {
	spec, sf, err := aacCodebooks()
	if err != nil {
		t.Fatal(err)
	}
	sizes := [11]int{81, 81, 81, 81, 81, 81, 64, 64, 169, 169, 289}
	for i, h := range spec {
		if got := len(aacSpectralCodes[i]); got != sizes[i] || len(aacSpectralBits[i]) != sizes[i] {
			t.Errorf("codebook %d has %d codes and %d lengths, want %d", i+1, got, len(aacSpectralBits[i]), sizes[i])
		}
		if len(h.node) < 2 {
			t.Errorf("codebook %d: %d nodes", i+1, len(h.node))
		}
	}
	if len(aacScalefactorCodes) != 121 || len(aacScalefactorBits) != 121 || sf == nil {
		t.Errorf("scalefactor codebook has %d codes and %d lengths, want 121", len(aacScalefactorCodes), len(aacScalefactorBits))
	}
}

// TestAACHuffRoundTrip encodes every symbol of every codebook and decodes it back.
func TestAACHuffRoundTrip(t *testing.T) {
	spec, sf, err := aacCodebooks()
	if err != nil {
		t.Fatal(err)
	}
	for i, h := range spec {
		w := &bitWriter{}
		for s := range aacSpectralCodes[i] {
			w.bits(uint(aacSpectralCodes[i][s]), int(aacSpectralBits[i][s]))
		}
		r := newAACBits(w.bytes())
		for s := range aacSpectralCodes[i] {
			if got, ok := h.decode(r); !ok || got != s {
				t.Fatalf("codebook %d symbol %d decoded as %d (%v)", i+1, s, got, ok)
			}
		}
	}
	w := &bitWriter{}
	for s := range aacScalefactorCodes {
		w.bits(uint(aacScalefactorCodes[s]), int(aacScalefactorBits[s]))
	}
	r := newAACBits(w.bytes())
	for s := range aacScalefactorCodes {
		if got, ok := sf.decode(r); !ok || got != s {
			t.Fatalf("scalefactor symbol %d decoded as %d (%v)", s, got, ok)
		}
	}
}

func TestAACHuffDecodeTruncated(t *testing.T) {
	_, sf, _ := aacCodebooks()
	r := newAACBits(nil)
	if _, ok := sf.decode(r); ok {
		t.Error("decoded a symbol from no bits")
	}
	// a long code cut short: nineteen 1 bits would be a symbol, eight are not
	r = newAACBits([]byte{0xff})
	if _, ok := sf.decode(r); ok {
		t.Error("decoded a symbol from a truncated code")
	}
}

func TestBuildAACHuffRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		codes []uint32
		lens  []uint8
		want  string
	}{
		"empty":      {nil, nil, "differ"},
		"mismatch":   {[]uint32{0}, []uint8{1, 1}, "differ"},
		"zero len":   {[]uint32{0, 1}, []uint8{0, 1}, "codeword of 0 bits"},
		"too wide":   {[]uint32{0, 1}, []uint8{1, 33}, "codeword of 33 bits"},
		"overflows":  {[]uint32{0, 2}, []uint8{1, 1}, "codeword of 1 bits"},
		"repeat":     {[]uint32{0, 0}, []uint8{1, 1}, "repeats"},
		"prefix":     {[]uint32{0, 1, 3}, []uint8{1, 1, 2}, "extends"},
		"open":       {[]uint32{0, 2}, []uint8{1, 2}, "open branch"},
		"inner open": {[]uint32{0, 2, 3}, []uint8{2, 2, 2}, "open branch"},
	} {
		_, err := buildAACHuff(tc.codes, tc.lens)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one containing %q", name, err, tc.want)
		}
	}
	// 32 bit codewords are allowed
	if _, err := buildAACHuff([]uint32{0}, []uint8{32}); err == nil {
		t.Error("a 32 bit code on its own is not complete")
	}
}

func TestBuildAACCodebooksRefusals(t *testing.T) {
	var spec [11][]uint16
	var bits [11][]uint8
	for i := range spec {
		spec[i], bits[i] = []uint16{0, 1}, []uint8{1, 1}
	}
	if _, _, err := buildAACCodebooks(spec, bits, []uint32{0, 1}, []uint8{1, 1}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildAACCodebooks(spec, bits, []uint32{0, 0}, []uint8{1, 1}); err == nil {
		t.Error("a bad scalefactor book was built")
	}
	bits[4] = []uint8{1, 2}
	if _, _, err := buildAACCodebooks(spec, bits, []uint32{0, 1}, []uint8{1, 1}); err == nil || !strings.Contains(err.Error(), "spectrum codebook 5") {
		t.Errorf("err = %v", err)
	}
}
