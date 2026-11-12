// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"hash/fnv"
	"math"
	"math/cmplx"
	"path/filepath"
	"sync"
	"testing"
)

// testLowBand fills a low band array with a mixture that has tonal and noisy
// subbands, deterministic in seed.
func testLowBand(seed uint32) (low [sbrAnaBands][sbrLowSlots]complex128) {
	x := seed
	next := func() float64 {
		x = x*1664525 + 1013904223
		return float64(int32(x)) / float64(1<<31)
	}
	for k := range low {
		tone := k%3 == 0
		for i := range low[k] {
			z := complex(next(), next()) * 100
			if tone {
				z = z*0.01 + cmplx.Rect(1000, 0.7*float64(i)+float64(k))
			}
			low[k][i] = z
		}
	}
	return low
}

// TestSBRPredictor: the float64 estimate of the prediction-error filter agrees with
// the exact rational solution of the same normal equations, relatively to 1e-9, over
// noisy and tonal subbands (the 1+1e-6 regularisation leaves six digits of float64 in
// a pure tone: the bound is the conditioning, not the arithmetic), and the degenerate
// and failed estimates give zero as the standard says.
func TestSBRPredictor(t *testing.T) {
	var worst, worstF32 float64
	n := 0
	for seed := uint32(1); seed <= 6; seed++ {
		low := testLowBand(seed)
		for k := range low {
			a0, a1 := sbrPredictor(&low[k])
			e0, e1 := refPredictor(&low[k])
			if (e0 == 0) != (a0 == 0) || (e1 == 0) != (a1 == 0) {
				t.Fatalf("seed %d band %d: zeroing decisions differ: %v %v against %v %v", seed, k, a0, a1, e0, e1)
			}
			d := math.Max(cmplx.Abs(a0-e0), cmplx.Abs(a1-e1)) / math.Max(1, math.Max(cmplx.Abs(e0), cmplx.Abs(e1)))
			worst = math.Max(worst, d)
			f0, f1 := sbrPredictorF32(&low[k])
			worstF32 = math.Max(worstF32, math.Max(cmplx.Abs(f0-e0), cmplx.Abs(f1-e1))/math.Max(1, math.Max(cmplx.Abs(e0), cmplx.Abs(e1))))
			n++
		}
	}
	t.Logf("predictor: %d subbands, largest relative error %.3e (the same estimate in float32 arithmetic: %.3e)", n, worst, worstF32)
	if worst > 1e-9 {
		t.Errorf("predictor error %.3e", worst)
	}

	var z [sbrLowSlots]complex128
	if a0, a1 := sbrPredictor(&z); a0 != 0 || a1 != 0 {
		t.Errorf("silence: %v %v", a0, a1)
	}
	// only the last two samples nonzero: a zero determinant, and a0 from them alone
	z[38], z[39] = 1, 1
	if a0, a1 := sbrPredictor(&z); a0 != -1 || a1 != 0 {
		t.Errorf("degenerate: %v %v", a0, a1)
	}
	if e0, e1 := refPredictor(&z); e0 != -1 || e1 != 0 {
		t.Errorf("degenerate, exact: %v %v", e0, e1)
	}
	// a signal that grows by a factor of 5 per sample asks for a0 = -5, beyond the
	// limit: a failed estimate, both coefficients zero
	var g [sbrLowSlots]complex128
	for i := range g {
		g[i] = complex(math.Pow(5, float64(i)), 0)
	}
	if a0, a1 := sbrPredictor(&g); a0 != 0 || a1 != 0 {
		t.Errorf("growing signal: %v %v", a0, a1)
	}
	if e0, e1 := refPredictor(&g); e0 != 0 || e1 != 0 {
		t.Errorf("growing signal, exact: %v %v", e0, e1)
	}
}

// sbrPredictorF32 is the same estimate evaluated in float32 arithmetic, the
// precision ffmpeg's float build works in: the yardstick for how much the float64
// evaluation gains, not a decoder path.
func sbrPredictorF32(x *[sbrLowSlots]complex128) (a0, a1 complex128) {
	type c32 = complex64
	var r11, r22 float32
	var r01, r12, r02 c32
	for i := 0; i < sbrLowSlots-2; i++ {
		xi := c32(x[i])
		r22 += real(xi)*real(xi) + imag(xi)*imag(xi)
		r12 += c32(x[i+1]) * c32(cmplx.Conj(complex128(xi)))
		r02 += c32(x[i+2]) * c32(cmplx.Conj(complex128(xi)))
	}
	for i := 1; i < sbrLowSlots-1; i++ {
		xi := c32(x[i])
		r11 += real(xi)*real(xi) + imag(xi)*imag(xi)
		r01 += c32(x[i+1]) * c32(cmplx.Conj(complex128(xi)))
	}
	det := r11*r22 - (real(r12)*real(r12)+imag(r12)*imag(r12))/1.000001
	var b1, b0 c32
	if det != 0 {
		n := r01*r12 - r02*c32(complex(r11, 0))
		b1 = complex(real(n)/det, imag(n)/det)
	}
	if r11 != 0 {
		n := r01 + b1*c32(cmplx.Conj(complex128(r12)))
		b0 = -complex(real(n)/r11, imag(n)/r11)
	}
	a0, a1 = complex128(b0), complex128(b1)
	if sbrAbs2(a1) >= 16 || sbrAbs2(a0) >= 16 {
		return 0, 0
	}
	return a0, a1
}

// TestSBRChirp: the chirp factors follow their definition for every pair of
// inverse filtering modes and a spread of previous values, including the 0.6 case
// and the cut-off under 1/64.
func TestSBRChirp(t *testing.T) {
	for cur := uint8(0); cur < 4; cur++ {
		for prev := uint8(0); prev < 4; prev++ {
			for _, old := range []float64{0, 0.001, 0.01, 0.3, 0.6, 0.75, 0.9, 0.98} {
				var d sbrChanDSP
				var c sbrChannel
				d.bw[2] = old
				c.invf[0][2], c.invf[1][2] = cur, prev
				d.chirp(&c, 3)
				want := refChirp(cur, prev, old)
				if math.Abs(d.bw[2]-want) > 1e-15 {
					t.Errorf("modes %d, %d, previous %v: %v, want %v", cur, prev, old, d.bw[2], want)
				}
				if d.bw[0] != 0 || d.bw[1] != 0 || d.bw[3] != 0 {
					t.Errorf("a factor outside the bands changed: %v", d.bw)
				}
			}
		}
	}
}

// sbrTestHeaders are headers whose tables differ in patches, noise bands and
// crossover.
func sbrTestHeaders() []sbrHdr {
	a := defaultSBRHdr()
	b := sbrHdr{start: 5, stop: 8, xover: 2, extra1: true, freqScale: 3, alter: 1, nb: 3}
	c := sbrHdr{start: 8, stop: 14, xover: 0, extra1: true, freqScale: 1, alter: 0, nb: 0}
	d := sbrHdr{start: 0, stop: 5, xover: 1, extra1: true, freqScale: 0, alter: 1, nb: 1}
	return []sbrHdr{a, b, c, d}
}

// sbrTestChannel is a channel with the given envelope borders and inverse filtering modes.
func sbrTestChannel(borders []uint8, modes [sbrMaxNoise]uint8) sbrChannel {
	var c sbrChannel
	c.numEnv = len(borders) - 1
	copy(c.tEnv[:], borders)
	c.invf[0] = modes
	c.invf[1] = [sbrMaxNoise]uint8{3, 1, 0, 2, 1}
	return c
}

// TestSBRGenerate: the high band agrees with its definition (to 1e-13 of the peak:
// the same sums, evaluated with different rounding) over several header shapes and
// envelope borders; in the bands from kx to kx+m, slots outside the envelope range
// and bands above the last patch are zero (stale content is cleared).
func TestSBRGenerate(t *testing.T) {
	borders := [][]uint8{{0, 16}, {0, 8, 16}, {2, 6, 14, 19}, {3, 4, 19}}
	var worst, peak float64
	cases := 0
	for hi, h := range sbrTestHeaders() {
		tb := sbrPayload{}.tables(t, sbrTestCore, h)
		t.Logf("header %d: k0 %d kx %d m %d, %d patches %v, %d noise floor bands", hi, tb.k[0], tb.kx[1], tb.m[1], tb.numPatches, tb.patchNum[:tb.numPatches], tb.nQ)
		for bi, br := range borders {
			var d sbrChanDSP
			d.low = testLowBand(uint32(10*hi + bi))
			c := sbrTestChannel(br, [sbrMaxNoise]uint8{1, 2, 3, 0, 1})
			d.inverseFilter(tb.k[0])
			d.chirp(&c, tb.nQ)
			d.chirp(&c, tb.nQ) // two frames: the factors have history
			for k := range d.high {
				for i := range d.high[k] {
					d.high[k][i] = 7 // stale content must go
				}
			}
			bw := d.bw
			d.generate(tb, &c)
			want := refGenerate(tb, &c, &d.low, &d.alpha, &bw)
			for k := tb.kx[1]; k < tb.kx[1]+tb.m[1]; k++ { // the bands the generator owns
				for i := range want[k] {
					worst = math.Max(worst, cmplx.Abs(d.high[k][i]-want[k][i]))
					peak = math.Max(peak, cmplx.Abs(want[k][i]))
				}
			}
			cases++
		}
	}
	t.Logf("generate: %d cases, largest difference %.3e, peak %.3e", cases, worst, peak)
	if worst > 1e-13*peak {
		t.Errorf("high band differs from the definition by %.3e of a peak of %.3e", worst, peak)
	}
}

// sbrX assembles the 64 bands of the slots of a frame without the envelope
// adjustment (which is the next part): the low band below kx, delayed by sbrHFAdj
// slots, and the generated bands above it as they are. Shared by the decoder path
// and the reference, which differ only in the arithmetic that made low and high.
func sbrX(low *[sbrAnaBands][sbrLowSlots]complex128, high *[sbrSynBands][sbrHFSlots]complex128, kx, m int) (x [sbrQMFSlots][sbrSynBands]complex128) {
	for l := range x {
		for k := 0; k < kx; k++ {
			x[l][k] = low[k][l+sbrHFAdj]
		}
		for k := kx; k < kx+m; k++ {
			x[l][k] = high[k][l]
		}
	}
	return x
}

// sbrChainStats are the error measures of one run of the signal path.
type sbrChainStats struct {
	frames, hfFrames int
	outPeak          float64
	outErr           float64     // largest |output - reference| beyond a float32 rounding
	outErrRel        float64     // the same, relative to the peak
	lowErr           float64     // largest |low band - reference low band|, relative to its peak
	want             [][]float64 // the reference path's output, per channel
}

// sbrChain runs the signal path over the first frames frames of an HE-AAC fixture:
// the core signal from the AAC-LC decoder, the analysis bank, high frequency
// generation when the stream has SBR data, the synthesis bank (the bands assembled
// without adjustment). It returns the float32 output of every channel. With ref it
// runs the exact reference path next to it, on the same core signal, and measures.
func sbrChain(t testing.TB, name string, frames int, ref bool) (out [][]float32, st sbrChainStats) {
	t.Helper()
	cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", name))
	p, err := newAACParser(cfg, DefaultMaxAudioChannels)
	if err != nil {
		t.Fatal(err)
	}
	p.enableSBR()
	synth := newAACSynth(cfg.rateIndex, cfg.channels)
	nch := cfg.channels
	dsps := make([]*sbrChanDSP, nch)
	refAna := make([]*refAnalysis, nch)
	refSyn := make([]*refSynthesis, nch)
	refLow := make([][sbrAnaBands][sbrLowSlots]complex128, nch)
	refBW := make([][sbrMaxNoise]float64, nch)
	for c := range dsps {
		dsps[c] = new(sbrChanDSP)
		refAna[c], refSyn[c] = &refAnalysis{}, &refSynthesis{}
	}
	out = make([][]float32, nch)
	var lowPeak float64
	for fi := 0; fi < min(frames, len(fr)); fi++ {
		els, err := p.parseFrame(fr[fi])
		if err != nil {
			t.Fatal(err)
		}
		if len(els) != 1 {
			t.Fatalf("%d elements", len(els))
		}
		el := &els[0]
		var pcm [2][aacFrameLen]float32
		synth.element(el, 0, [2][]float32{pcm[0][:], pcm[1][:]})
		st.frames++
		for c := 0; c < int(el.nch); c++ {
			d := dsps[c]
			d.analyse(pcm[c][:])
			kx, m := 32, 0
			var chn *sbrChannel
			if el.sbr != nil {
				el.sbr.dsp[c] = d
				el.sbr.hfGenerate(c)
				kx, m = el.sbr.t.kx[1], el.sbr.t.m[1]
				chn = &el.sbr.ch[c]
				if c == 0 {
					st.hfFrames++
				}
			}
			x := sbrX(&d.low, &d.high, kx, m)
			o := make([]float32, sbrQMFSlots*sbrSynBands)
			d.syn.process(o, &x)
			out[c] = append(out[c], o...)
			if !ref {
				continue
			}
			// the reference path: its own analysis, estimate, chirp, generation, synthesis
			rl := &refLow[c]
			for k := range rl {
				copy(rl[k][:sbrHFGen], rl[k][sbrQMFSlots:])
			}
			ra := refAna[c].frame(pcm[c][:])
			for l := range ra {
				for k := range ra[l] {
					rl[k][sbrHFGen+l] = ra[l][k]
					lowPeak = math.Max(lowPeak, cmplx.Abs(ra[l][k]))
					st.lowErr = math.Max(st.lowErr, cmplx.Abs(d.low[k][sbrHFGen+l]-ra[l][k]))
				}
			}
			var rhigh [sbrSynBands][sbrHFSlots]complex128
			if chn != nil {
				var alpha [sbrAnaBands][2]complex128
				for k := 0; k < min(el.sbr.t.k[0], sbrAnaBands); k++ {
					alpha[k][0], alpha[k][1] = refPredictor(&rl[k])
				}
				for i := 0; i < el.sbr.t.nQ; i++ {
					refBW[c][i] = refChirp(chn.invf[0][i], chn.invf[1][i], refBW[c][i])
				}
				rhigh = refGenerate(&el.sbr.t, chn, rl, &alpha, &refBW[c])
			}
			rx := sbrX(rl, &rhigh, kx, m)
			if st.want == nil {
				st.want = make([][]float64, nch)
			}
			for l := range rx {
				w := refSyn[c].slot(&rx[l])
				st.want[c] = append(st.want[c], w[:]...)
				for q, v := range w {
					got := float64(o[l*sbrSynBands+q])
					st.outPeak = math.Max(st.outPeak, math.Abs(v))
					st.outErr = math.Max(st.outErr, math.Abs(got-v)-math.Abs(v)*0x1p-24)
				}
			}
		}
	}
	st.outErrRel = st.outErr / st.outPeak
	st.lowErr /= lowPeak
	return out, st
}

// TestSBRChain: over real HE-AAC streams (core signal, SBR data, tables, envelope
// borders and inverse filtering modes as the encoder wrote them) the whole signal
// path agrees with the exact reference path: the low band to 1e-13 of its peak, and
// the output, beyond one float32 rounding, to 1e-9 of its peak (measured: none; the
// margin is for the conditioning of the prediction-error estimate on tonal bands,
// where a 1e-6 regularisation leaves six digits and the estimate itself is known to
// 4e-10, see TestSBRPredictor).
func TestSBRChain(t *testing.T) {
	for _, fx := range sbrFixtures {
		_, st := sbrChain(t, fx.name, 12, true)
		t.Logf("%s: %d frames, %d with high frequency generation; low band error %.2e of its peak; output error beyond a float32 rounding %.2e of the output peak %.2e (%.2e)",
			fx.name, st.frames, st.hfFrames, st.lowErr, st.outErrRel, st.outPeak, st.outErr)
		if st.hfFrames < 6 {
			t.Errorf("%s: high frequency generation ran in %d frames", fx.name, st.hfFrames)
		}
		if st.lowErr > 1e-13 || st.outErrRel > 1e-9 {
			t.Errorf("%s: low band error %.2e, output error %.2e", fx.name, st.lowErr, st.outErrRel)
		}
	}
}

// TestSBRConcurrentChannels: the plans are shared, the state is not: goroutines each
// run the whole signal path over the same streams with their own state and produce
// the bytes the single goroutine did (under -race a shared buffer is also reported).
func TestSBRConcurrentChannels(t *testing.T) {
	digest := func(tb testing.TB, name string) uint64 {
		out, _ := sbrChain(tb, name, 16, false)
		h := fnv.New64a()
		for _, ch := range out {
			for _, v := range ch {
				b := math.Float32bits(v)
				h.Write([]byte{byte(b), byte(b >> 8), byte(b >> 16), byte(b >> 24)})
			}
		}
		return h.Sum64()
	}
	want := map[string]uint64{}
	for _, fx := range sbrFixtures {
		want[fx.name] = digest(t, fx.name)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, fx := range sbrFixtures {
				if got := digest(t, fx.name); got != want[fx.name] {
					t.Errorf("%s: a goroutine's output differs from the single-goroutine output", fx.name)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestSBRQMFWindow pins the prototype: symmetric about tap 320, except that the
// four taps 128, 256, 384, 512 are antisymmetric (two of the 640 values are written
// with a sign outside the value in the source they were read from, which a first
// version of the generator dropped: the tone test, the 256-bit oracle and the
// definition all agreed with the wrong table, only the comparison with ffmpeg's
// output and the reconstruction error caught it).
func TestSBRQMFWindow(t *testing.T) {
	for i := 1; i < 640; i++ {
		want := sbrQMFWindow[640-i]
		if i == 128 || i == 256 || i == 384 || i == 512 {
			want = -want
		}
		if sbrQMFWindow[i] != want {
			t.Errorf("tap %d is %v, its mirror %v", i, sbrQMFWindow[i], sbrQMFWindow[640-i])
		}
	}
	// the two taps with no distinct mirror, pinned exactly
	if sbrQMFWindow[0] != 0 || sbrQMFWindow[320] != 0.85373856 {
		t.Errorf("taps 0 and 320: %v %v", sbrQMFWindow[0], sbrQMFWindow[320])
	}
	for i, w := range sbrAnaWindow {
		if w != sbrQMFWindow[2*i] {
			t.Fatalf("analysis tap %d", i)
		}
	}
}
