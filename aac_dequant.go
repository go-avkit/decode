// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "math"

// Inverse quantisation (the formula as ffmpeg's decoder applies it; the clause of
// the standard is not verified):
//
//	x = sign(q) * |q|^(4/3) * 2^((sf-100)/4)
//
// The two factors come from float32 tables rounded from float64, and the product
// is a single float32 multiplication, so the result does not depend on whether the
// compiler fuses operations.

// aacPow43 holds |q|^(4/3) for every magnitude a quantised value can have: the
// escape code reaches 8191 and a pulse adds up to 15.
var aacPow43 = func() (t [aacMaxQuant + 16]float32) {
	for i := range t {
		t[i] = float32(math.Pow(float64(i), 4.0/3.0))
	}
	return t
}()

// aacGain holds 2^((sf-100)/4) for sf 0 to 255, correctly rounded to float32.
var aacGain = func() (t [256]float32) {
	for i := range t {
		t[i] = float32(math.Exp2(float64(i-100) / 4))
	}
	return t
}()

// dequantise writes the scaled spectrum of ch to out, in the layout of ch.q.
// Bands of the zero, noise and intensity codebooks come out as zeros: the stages
// that give them values (noise substitution, intensity stereo) run after this one
// and need the scalefactors, not the quantised values.
func (ch *aacChannel) dequantise(out *[aacFrameLen]float32) {
	ics := &ch.ics
	*out = [aacFrameLen]float32{}
	w0 := 0
	for g := 0; g < int(ics.numGroups); g++ {
		for sfb := 0; sfb < int(ics.maxSfb); sfb++ {
			if cb := ch.cb[g][sfb]; cb == aacBandZero || cb >= aacBandNoise {
				continue
			}
			gain := aacGain[ch.sf[g][sfb]]
			lo, hi := int(ics.swb[sfb]), int(ics.swb[sfb+1])
			for k := 0; k < int(ics.groupLen[g]); k++ {
				base := (w0 + k) * aacShortLen
				for i := base + lo; i < base+hi; i++ {
					q := ch.q[i]
					if q < 0 {
						out[i] = -(aacPow43[-q] * gain)
					} else {
						out[i] = aacPow43[q] * gain
					}
				}
			}
		}
		w0 += int(ics.groupLen[g])
	}
}
