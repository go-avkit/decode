// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"testing"

	"github.com/go-fft/fft"
)

// fivePassAnalysis is the analysis bank as it was before the window sum became one
// floats.MulAddRows call: the history oldest first, five passes of u[n] += win[n]*x[63-n].
type fivePassAnalysis struct {
	hist    [sbrAnaKeep + aacFrameLen]float64
	u, v, w [sbrSynBands]float64
	c, s    [sbrSynBands]float64
}

func (a *fivePassAnalysis) process(in []float32, low *[sbrAnaBands][sbrLowSlots]complex128, first int) {
	copy(a.hist[:sbrAnaKeep], a.hist[aacFrameLen:])
	for i, s := range in[:aacFrameLen] {
		a.hist[sbrAnaKeep+i] = float64(s) * sbrPCMUnit
	}
	for l := 0; l < sbrQMFSlots; l++ {
		h := a.hist[l*32 : l*32+sbrAnaTaps]
		a.u = [sbrSynBands]float64{}
		for j := 0; j < 5; j++ {
			win := (*[sbrSynBands]float64)(sbrAnaWindow[64*j:])
			x := (*[sbrSynBands]float64)(h[sbrAnaTaps-64-64*j:])
			for n := range a.u {
				// fused, as the compiler made it on arm64 (it does not fuse on amd64 or
				// 386, where the old loop rounded twice; MulAddRows fuses everywhere)
				a.u[n] = math.FMA(win[n], x[63-n], a.u[n])
			}
		}
		a.v[0], a.w[63] = a.u[0], -a.u[0]
		for n := 1; n <= 32; n++ {
			a.v[2*n-1], a.w[64-2*n] = a.u[n], a.u[n]
		}
		for n := 33; n < sbrSynBands; n++ {
			a.v[128-2*n], a.w[2*n-65] = -a.u[n], a.u[n]
		}
		sbrDCT4.DCT(a.c[:], a.v[:], fft.NormBackward)
		sbrDCT4.DCT(a.s[:], a.w[:], fft.NormBackward)
		for k := 0; k < sbrAnaBands; k += 2 {
			low[k][first+l] = complex(a.c[k], a.s[k])
			low[k+1][first+l] = complex(a.c[k+1], -a.s[k+1])
		}
	}
}

// TestSBRAnalysisBitIdentical: the analysis bank equals the five-pass loop it replaced
// bit for bit, over several frames in a row (so the history carried between frames is
// checked), on synthetic audio and on random input including a loud burst. A mutation
// of the new loop (an off-by-one start of the slot's window, or the history copy)
// fails it.
func TestSBRAnalysisBitIdentical(t *testing.T) {
	const frames = 6
	x := uint32(11)
	rnd := make([]float32, frames*aacFrameLen)
	for i := range rnd {
		x = x*1664525 + 1013904223
		rnd[i] = float32(int32(x)) / float32(1<<31)
		if i/aacFrameLen == 3 {
			rnd[i] *= 8 // a burst
		}
	}
	for name, pcm := range map[string][]float32{"audio": sbrTestPCM(frames, 3), "random": rnd} {
		var a sbrAnalysis
		var ref fivePassAnalysis
		for f := 0; f < frames; f++ {
			in := pcm[f*aacFrameLen : (f+1)*aacFrameLen]
			var got, want [sbrAnaBands][sbrLowSlots]complex128
			a.process(in, &got, 0)
			ref.process(in, &want, 0)
			for k := range got {
				for l := 0; l < sbrQMFSlots; l++ {
					g, w := got[k][l], want[k][l]
					if math.Float64bits(real(g)) != math.Float64bits(real(w)) || math.Float64bits(imag(g)) != math.Float64bits(imag(w)) {
						t.Fatalf("%s frame %d band %d slot %d: %v, five-pass loop %v", name, f, k, l, g, w)
					}
				}
			}
		}
	}
}
