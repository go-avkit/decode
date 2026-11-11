// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"math/rand"
	"testing"
)

// naiveBlock is the windowed time signal (2M samples) of one block, by the
// definition of the inverse MDCT as a double sum (M coefficients, window of 2M).
func naiveBlock(coef []float64, win func(m int) float64) []float64 {
	M := len(coef)
	N := 2 * M
	n0 := (float64(N)/2 + 1) / 2
	scale := 1 / float64(M) / 32768
	out := make([]float64, N)
	for m := range out {
		var x float64
		for k, c := range coef {
			x += c * math.Cos(2*math.Pi/float64(N)*(float64(m)+n0)*(float64(k)+0.5))
		}
		out[m] = x * scale * win(m)
	}
	return out
}

// TestAACWindowSequences: every order of window sequences, the legal ones and the
// others, with a shape change between the blocks, against a reference that windows
// each block by the definition (the window of its sequence, its left half in the
// shape of the previous block, its right half in its own), overlaps the second half
// of the one with the first half of the next, and has no half-transform or
// symmetry in it.
func TestAACWindowSequences(t *testing.T) {
	aacSynthOnce.Do(aacSynthInit)
	rng := rand.New(rand.NewSource(6))
	tbl := func(shape uint8) (l, s []float64) {
		if shape != 0 {
			return exactKBD(4, 1024), exactKBD(6, 128)
		}
		return exactSine(1024), exactSine(128)
	}
	// a block of 2048 samples: long windows by sequence, or eight shorts
	block := func(coef []float64, seq, ls, rs uint8) []float64 {
		lL, lS := tbl(ls)
		rL, rS := tbl(rs)
		if seq != aacEightShort {
			return naiveBlock(coef, func(m int) float64 {
				if m < 1024 {
					switch {
					case seq != aacLongStop:
						return lL[m]
					case m < 448:
						return 0
					case m < 576:
						return lS[m-448]
					}
					return 1
				}
				u := m - 1024
				switch {
				case seq != aacLongStart:
					return rL[1023-u]
				case u < 448:
					return 1
				case u < 576:
					return rS[127-(u-448)]
				}
				return 0
			})
		}
		sum := make([]float64, 2048)
		for j := 0; j < 8; j++ {
			left := lS
			if j > 0 {
				left = rS
			}
			b := naiveBlock(coef[j*128:(j+1)*128], func(m int) float64 {
				if m < 128 {
					return left[m]
				}
				return rS[255-m]
			})
			for m, v := range b {
				sum[448+128*j+m] += v
			}
		}
		return sum
	}
	var worst float64
	for a := uint8(0); a < 4; a++ {
		for b := uint8(0); b < 4; b++ {
			for _, shapes := range [][2]uint8{{0, 1}, {1, 0}} {
				var ref [2][]float64
				var coefs [2][]float64
				seqs := [2]uint8{a, b}
				for f := 0; f < 2; f++ {
					coefs[f] = make([]float64, 1024)
					for i := range coefs[f] {
						coefs[f][i] = float64(float32(rng.NormFloat64() * 1000))
					}
				}
				// the reference: block 0 starts after a block of zeros (the state a
				// stream starts in), block 1 follows block 0
				b0 := block(coefs[0], a, 0, shapes[0])
				b1 := block(coefs[1], b, shapes[0], shapes[1])
				ref[0] = b0[:1024]
				ref[1] = make([]float64, 1024)
				for i := range ref[1] {
					ref[1][i] = b0[1024+i] + b1[i]
				}
				s := newAACSynth(aacRate44, 1)
				for f := 0; f < 2; f++ {
					ch := &aacChannel{ics: testICS(seqs[f] == aacEightShort, 1)}
					ch.ics.windowSeq, ch.ics.windowShape = seqs[f], shapes[f]
					var coef [aacFrameLen]float32
					for i, v := range coefs[f] {
						coef[i] = float32(v)
					}
					out := make([]float32, 1024)
					s.imdctWindow(&s.chans[0], ch, &coef, out)
					for i, v := range out {
						worst = math.Max(worst, math.Abs(float64(v)-ref[f][i]))
						if d := math.Abs(float64(v) - ref[f][i]); d > 1e-8 {
							t.Fatalf("sequences %d then %d, shapes %v: frame %d sample %d: %v, definition %v", a, b, shapes, f, i, v, ref[f][i])
						}
					}
				}
			}
		}
	}
	t.Logf("all 16 orders, both shape changes: largest difference from the definition %.2e", worst)
}
