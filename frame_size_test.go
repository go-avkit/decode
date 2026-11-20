// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math/big"
	"math/rand/v2"
	"testing"
)

// bigFrameBytes is the oracle: the same sizes in arbitrary precision.
func bigFrameBytes(w, h uint64, c Chroma, depth, depthC int) (total *big.Int) {
	b := func(d int) int64 {
		if d == 8 {
			return 1
		}
		return 2
	}
	mul := func(xs ...*big.Int) *big.Int {
		r := big.NewInt(1)
		for _, x := range xs {
			r.Mul(r, x)
		}
		return r
	}
	bw, bh := new(big.Int).SetUint64(w), new(big.Int).SetUint64(h)
	total = mul(bw, bh, big.NewInt(b(depth)))
	if c != Chroma400 {
		sw, sh := chromaDivisors(c)
		cw := new(big.Int).SetUint64((w + sw - 1) / sw) // w < 2^63 here, so the sum cannot wrap
		ch := new(big.Int).SetUint64((h + sh - 1) / sh)
		total.Add(total, mul(big.NewInt(2), cw, ch, big.NewInt(b(depthC))))
	}
	return total
}

// TestFrameBytesAtTheCap asserts both sides of the cap for every format at its widest depth, on
// every platform (it runs in CI's 386 lane): the last width that fits is accepted with the exact
// size, the next one is refused.
func TestFrameBytesAtTheCap(t *testing.T) {
	for _, tc := range []struct {
		name        string
		c           Chroma
		depth, dc   int
		perW        uint64 // bytes of a one-row picture, per column
		lastFitting uint64
	}{
		{"4:4:4 16", Chroma444, 16, 16, 6, 357913941},
		{"4:4:4 8", Chroma444, 8, 8, 3, 715827882},
		{"4:2:2 16", Chroma422, 16, 16, 4, 536870911}, // ceil(w/2) chroma columns: w = 2k-1 or 2k
		{"4:0:0 16", Chroma400, 16, 0, 2, 1073741823},
		{"4:0:0 8", Chroma400, 8, 0, 1, 2147483647},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, total, ok := frameBytes(tc.lastFitting, 1, tc.c, tc.depth, tc.dc)
			if !ok || total > maxFrameBytes {
				t.Fatalf("width %d refused or over the cap: total %d ok %v", tc.lastFitting, total, ok)
			}
			if want := bigFrameBytes(tc.lastFitting, 1, tc.c, tc.depth, tc.dc); !want.IsUint64() || want.Uint64() != total {
				t.Fatalf("width %d: total %d, exact %v", tc.lastFitting, total, want)
			}
			// 4:2:2 at width 2k-1 and 2k have the same chroma, so the first refused width is further on.
			w := tc.lastFitting + 1
			for ; ; w++ {
				if _, _, _, ok := frameBytes(w, 1, tc.c, tc.depth, tc.dc); !ok {
					break
				}
				if w > tc.lastFitting+2 {
					t.Fatalf("width %d still accepted", w)
				}
			}
			if bigFrameBytes(w, 1, tc.c, tc.depth, tc.dc).Cmp(big.NewInt(maxFrameBytes)) <= 0 {
				t.Fatalf("width %d refused but it fits", w)
			}
		})
	}
}

// TestFrameBytesAgainstBig compares the decision and the sizes with arbitrary precision over
// random shapes, the sides up to 2^40 so that every product wraps a uint64 too.
func TestFrameBytesAgainstBig(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	limit := big.NewInt(maxFrameBytes)
	for range 200000 {
		w, h := rng.Uint64N(1<<(1+rng.UintN(40)))+1, rng.Uint64N(1<<(1+rng.UintN(40)))+1
		c := Chroma(rng.UintN(4))
		depth, dc := 8+int(rng.UintN(9)), 8+int(rng.UintN(9))
		y, chroma, total, ok := frameBytes(w, h, c, depth, dc)
		want := bigFrameBytes(w, h, c, depth, dc)
		fits := want.Cmp(limit) <= 0
		if ok != fits {
			t.Fatalf("%dx%d %d %d/%d: ok %v, exact %v (fits %v)", w, h, c, depth, dc, ok, want, fits)
		}
		if ok && (total != want.Uint64() || y+2*chroma != total) {
			t.Fatalf("%dx%d %d %d/%d: total %d y %d chroma %d, exact %v", w, h, c, depth, dc, total, y, chroma, want)
		}
	}
}

func TestFrameBytesRefusesAShapeThatIsNotAFrames(t *testing.T) {
	for name, tc := range map[string]struct {
		w, h      uint64
		c         Chroma
		depth, dc int
	}{
		"no width":          {0, 4, Chroma420, 8, 8},
		"no height":         {4, 0, Chroma420, 8, 8},
		"luma under 8":      {4, 4, Chroma420, 7, 8},
		"luma over 16":      {4, 4, Chroma420, 17, 8},
		"chroma under 8":    {4, 4, Chroma420, 8, 7},
		"chroma over 16":    {4, 4, Chroma444, 8, 17},
		"unknown format":    {4, 4, 4, 8, 8},
		"width over cap":    {1 << 32, 1, Chroma400, 8, 0},
		"height over cap":   {1, 1 << 32, Chroma400, 8, 0},
		"plane over cap":    {1 << 16, 1 << 15, Chroma400, 16, 0},
		"chroma 0 on 4:2:0": {4, 4, Chroma420, 8, 0},
		// w*h*bytes wraps a uint64 to 0 or a small size: only a check of each side before the
		// product refuses these; a total compared after the fact would accept them
		"4 x 2^62, 8 bits": {1 << 62, 4, Chroma400, 8, 0},
		"2^63 x 2, 4:4:4":  {1 << 63, 2, Chroma444, 16, 16},
		"4 x 2^62, 4:2:0":  {4, 1 << 62, Chroma420, 8, 8},
		"2^32 x 2^32, 16":  {1 << 32, 1 << 32, Chroma400, 16, 0},
	} {
		if _, _, _, ok := frameBytes(tc.w, tc.h, tc.c, tc.depth, tc.dc); ok {
			t.Errorf("%s: accepted", name)
		}
	}
	// No chroma planes: the chroma depth is not looked at.
	if y, c, total, ok := frameBytes(4, 3, Chroma400, 12, 0); !ok || y != 24 || c != 0 || total != 24 {
		t.Errorf("4:0:0: %d %d %d %v", y, c, total, ok)
	}
	if sw, sh := chromaDivisors(9); sw != 0 || sh != 0 {
		t.Errorf("a value that is no format has divisors %d %d", sw, sh)
	}
}
