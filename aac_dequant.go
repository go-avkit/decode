// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "math"

// Inverse quantisation, ISO/IEC 14496-3 4.6.3.3:
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
// Bands of the zero codebook and of the intensity codebooks come out as zeros:
// intensity stereo fills them from the other channel afterwards. A band of the
// noise codebook is filled with noise when noise is not nil, as zeros otherwise:
// 4.6.13, random values from the generator at *noise, one per coefficient,
// window by window in band order, scaled so that the band's energy is the
// energy the scalefactor states.
func (ch *aacChannel) dequantise(out *[aacFrameLen]float32, noise *uint32) {
	ics := &ch.ics
	*out = [aacFrameLen]float32{}
	w0 := 0
	for g := 0; g < int(ics.numGroups); g++ {
		for sfb := 0; sfb < int(ics.maxSfb); sfb++ {
			cb := ch.cb[g][sfb]
			if cb == aacBandZero || cb > aacBandNoise {
				continue
			}
			lo, hi := int(ics.swb[sfb]), int(ics.swb[sfb+1])
			if cb == aacBandNoise {
				if noise == nil {
					continue
				}
				gain := aacGain[min(max(int(ch.sf[g][sfb]), -100), 155)+100]
				for k := 0; k < int(ics.groupLen[g]); k++ {
					base := (w0 + k) * aacShortLen
					var energy float32
					for i := base + lo; i < base+hi; i++ {
						out[i] = aacNoise(noise)
						energy = addmul(energy, out[i], out[i])
					}
					scale := gain / float32(math.Sqrt(float64(energy)))
					for i := base + lo; i < base+hi; i++ {
						out[i] = mul(out[i], scale)
					}
				}
				continue
			}
			gain := aacGain[ch.sf[g][sfb]]
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
