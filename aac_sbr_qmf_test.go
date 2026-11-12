// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"math/cmplx"
	"testing"
)

// sbrTestPCM is a deterministic test signal in +-1.0: noise, three tones and a
// decaying burst, frames of 1024 samples.
func sbrTestPCM(frames int, seed uint32) []float32 {
	n := frames * aacFrameLen
	pcm := make([]float32, n)
	x := seed
	for i := range pcm {
		x = x*1664525 + 1013904223
		noise := float64(int32(x))/float64(1<<31)*0.15 + 0
		t := float64(i)
		v := noise + 0.3*math.Sin(0.05*t) + 0.2*math.Sin(0.9*t+1) + 0.1*math.Sin(2.1*t)
		v += 0.25 * math.Exp(-float64(i%4096)/700) * math.Sin(0.4*t*(1+float64(i%4096)/4000))
		pcm[i] = float32(v)
	}
	return pcm
}

func maxAbsC(z []complex128) (m float64) {
	for _, v := range z {
		m = math.Max(m, cmplx.Abs(v))
	}
	return m
}

// TestSBRQMFAnalysis: the analysis bank agrees with its definition (a direct sum,
// refAnalysis) over several frames to 1e-12 of the peak subband value, and a single
// slot agrees with the 256-bit evaluation to the same bound; the definition itself
// is within 1e-13 of the 256-bit value.
func TestSBRQMFAnalysis(t *testing.T) {
	const frames = 5
	pcm := sbrTestPCM(frames, 1)
	var d sbrChanDSP
	ref := &refAnalysis{}
	var worst, peak float64
	for f := 0; f < frames; f++ {
		fr := pcm[f*aacFrameLen : (f+1)*aacFrameLen]
		d.analyse(fr)
		want := ref.frame(fr)
		for l := 0; l < sbrQMFSlots; l++ {
			for k := 0; k < sbrAnaBands; k++ {
				got := d.low[k][sbrHFGen+l]
				worst = math.Max(worst, cmplx.Abs(got-want[l][k]))
				peak = math.Max(peak, cmplx.Abs(want[l][k]))
			}
		}
	}
	t.Logf("analysis: largest difference to the definition %.3e, peak subband value %.3e (relative %.3e)", worst, peak, worst/peak)
	if worst > 1e-12*peak {
		t.Errorf("analysis differs from the definition by %.3e of a peak of %.3e", worst, peak)
	}

	// one slot against 256 bits: the last slot of the last frame
	x := make([]float64, sbrAnaTaps)
	newest := frames*aacFrameLen - 1
	for i := range x {
		x[i] = float64(pcm[newest-i]) * sbrPCMUnit
	}
	exact := bigAnalysisSlot(x)
	var eOurs, eRef float64
	for k := 0; k < sbrAnaBands; k++ {
		eOurs = math.Max(eOurs, cmplx.Abs(d.low[k][sbrLowSlots-1]-exact[k]))
	}
	// the definition on the same history
	r2 := &refAnalysis{}
	for f := 0; f < frames; f++ {
		last := r2.frame(pcm[f*aacFrameLen : (f+1)*aacFrameLen])
		if f == frames-1 {
			for k := 0; k < sbrAnaBands; k++ {
				eRef = math.Max(eRef, cmplx.Abs(last[sbrQMFSlots-1][k]-exact[k]))
			}
		}
	}
	t.Logf("analysis, last slot against 256 bits: decoder %.3e, definition %.3e", eOurs, eRef)
	if eOurs > 1e-12*peak || eRef > 1e-13*peak {
		t.Errorf("against 256 bits: decoder %.3e, definition %.3e, peak %.3e", eOurs, eRef, peak)
	}
}

// TestSBRQMFSynthesis: the synthesis bank agrees with its definition to one float32
// rounding of the output plus 1e-12 of the peak, over several frames of arbitrary
// subband samples, and with the 256-bit evaluation of one output block.
func TestSBRQMFSynthesis(t *testing.T) {
	const frames = 4
	x := uint32(7)
	next := func() float64 {
		x = x*1664525 + 1013904223
		return float64(int32(x)) / float64(1<<31) * 3000
	}
	var syn sbrSynthesis
	ref := &refSynthesis{}
	var all [][sbrSynBands]complex128
	var worstRound, worstAbs, peak float64
	for f := 0; f < frames; f++ {
		var in [sbrQMFSlots][sbrSynBands]complex128
		for l := range in {
			for k := range in[l] {
				in[l][k] = complex(next(), next())
			}
		}
		out := make([]float32, sbrQMFSlots*sbrSynBands)
		syn.process(out, &in)
		for l := range in {
			all = append(all, in[l])
			want := ref.slot(&in[l])
			for q, w := range want {
				got := float64(out[l*sbrSynBands+q])
				peak = math.Max(peak, math.Abs(w))
				worstAbs = math.Max(worstAbs, math.Abs(got-w))
				// one rounding to float32 is at most half an ulp of the value
				worstRound = math.Max(worstRound, math.Abs(got-w)-math.Abs(w)*0x1p-24)
			}
		}
	}
	t.Logf("synthesis: largest difference to the definition %.3e, peak output %.3e; beyond one float32 rounding %.3e", worstAbs, peak, worstRound)
	if worstRound > 1e-12*peak {
		t.Errorf("synthesis differs from the definition by %.3e beyond a float32 rounding, peak %.3e", worstRound, peak)
	}

	// the last output block against 256 bits, from the last 10 slots
	var last [10][sbrSynBands]complex128
	for b := range last {
		last[b] = all[len(all)-1-b]
	}
	exact := bigSynthesisSlot(last)
	var in2 [sbrQMFSlots][sbrSynBands]complex128
	var syn2 sbrSynthesis
	out2 := make([]float32, sbrQMFSlots*sbrSynBands)
	// feed the same slots to a fresh bank: the last 10 of 32 slots are the history
	for l := 0; l < 22; l++ {
		in2[l] = [sbrSynBands]complex128{}
	}
	for b := 9; b >= 0; b-- {
		in2[sbrQMFSlots-1-b] = last[b]
	}
	syn2.process(out2, &in2)
	var e float64
	for q := 0; q < 64; q++ {
		e = math.Max(e, math.Abs(float64(out2[(sbrQMFSlots-1)*64+q])-exact[q])-math.Abs(exact[q])*0x1p-24)
	}
	t.Logf("synthesis, last block against 256 bits: beyond one float32 rounding %.3e", e)
	if e > 1e-12*peak {
		t.Errorf("against 256 bits: %.3e beyond a float32 rounding, peak %.3e", e, peak)
	}
}

// fitTone returns the amplitude and the rms of the residual of the least-squares
// fit of a sinusoid of angular frequency w (radians per sample) to y.
func fitTone(y []float32, w float64) (amp, resid float64) {
	var sc, cc, ssn, yc, ys float64
	for i, v := range y {
		c, s := math.Cos(w*float64(i)), math.Sin(w*float64(i))
		cc += c * c
		ssn += s * s
		sc += s * c
		yc += float64(v) * c
		ys += float64(v) * s
	}
	det := cc*ssn - sc*sc
	a := (yc*ssn - ys*sc) / det
	b := (ys*cc - yc*sc) / det
	var e float64
	for i, v := range y {
		r := float64(v) - a*math.Cos(w*float64(i)) - b*math.Sin(w*float64(i))
		e += r * r
	}
	return math.Hypot(a, b), math.Sqrt(e / float64(len(y)))
}

// TestSBRQMFTone: a sinusoid at the centre of analysis band k is largest in band k,
// and through the analysis bank and the synthesis bank with the high bands empty (a
// stream's pure upsampling) it comes out at twice the sampling rate with its
// amplitude and frequency, to a residual that is the aliasing of the near-perfect
// reconstruction (the prototype is not exactly perfect).
func TestSBRQMFTone(t *testing.T) {
	const frames = 8
	for _, k := range []int{0, 1, 5, 17, 30, 31} {
		// band k of 32 spans [k, k+1] * pi/32 rad/sample at the core rate
		w := (float64(k) + 0.5) * math.Pi / 32
		pcm := make([]float32, frames*aacFrameLen)
		for i := range pcm {
			pcm[i] = float32(0.5 * math.Sin(w*float64(i)+0.3))
		}
		var d sbrChanDSP
		var energy [sbrAnaBands]float64
		var out []float32
		for f := 0; f < frames; f++ {
			d.analyse(pcm[f*aacFrameLen : (f+1)*aacFrameLen])
			var x [sbrQMFSlots][sbrSynBands]complex128
			for l := range x {
				for b := 0; b < sbrAnaBands; b++ {
					z := d.low[b][sbrHFGen+l]
					x[l][b] = z
					if f >= 2 {
						energy[b] += sbrAbs2(z)
					}
				}
			}
			o := make([]float32, sbrQMFSlots*sbrSynBands)
			d.syn.process(o, &x)
			out = append(out, o...)
		}
		best := 0
		for b := range energy {
			if energy[b] > energy[best] {
				best = b
			}
		}
		if best != k {
			t.Errorf("tone at the centre of band %d is largest in band %d", k, best)
		}
		// steady state: the last 4 frames at twice the rate, frequency w/2 per sample
		tail := out[len(out)-4*2*aacFrameLen:]
		amp, resid := fitTone(tail, w/2)
		t.Logf("band %2d: output amplitude %.6f (input 0.5), residual rms %.2e", k, amp, resid)
		if math.Abs(amp-0.5) > 2e-3 || resid > 2e-3 {
			t.Errorf("band %d: amplitude %.6f, residual %.2e", k, amp, resid)
		}
	}
}

// TestSBRQMFNoAllocs: a frame through both banks allocates nothing.
func TestSBRQMFNoAllocs(t *testing.T) {
	if raceEnabled {
		t.Skip("go-fft keeps its transform scratch in a sync.Pool, which the race detector empties at random")
	}
	pcm := sbrTestPCM(1, 3)
	d := new(sbrChanDSP)
	var x [sbrQMFSlots][sbrSynBands]complex128
	out := make([]float32, sbrQMFSlots*sbrSynBands)
	n := testing.AllocsPerRun(20, func() {
		d.analyse(pcm)
		for l := range x {
			for b := 0; b < sbrAnaBands; b++ {
				x[l][b] = d.low[b][sbrHFGen+l]
			}
		}
		d.syn.process(out, &x)
	})
	if n != 0 {
		t.Errorf("%v allocations per frame", n)
	}
}
