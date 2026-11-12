// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"math/rand"
	"testing"
)

func testICS(short bool, maxSfb int, lens ...uint8) aacICS {
	ics := aacICS{maxSfb: uint8(maxSfb), numWindows: 1, numGroups: 1, windowSeq: aacOnlyLong, swb: aacSwbLongTables[aacSwbLongIndex[aacRate44]]}
	ics.groupLen[0] = 1
	if short {
		ics.windowSeq, ics.numWindows, ics.swb = aacEightShort, 8, aacSwbShortTables[aacSwbShortIndex[aacRate44]]
		ics.numGroups = uint8(len(lens))
		copy(ics.groupLen[:], lens)
	}
	return ics
}

// TestAACMDCTMatchesDefinition compares the transform with the inverse MDCT in
// float64, scaled by 1/N/32768. The error allowed is what float32 arithmetic
// through log2(N) stages can add: 4*log2(N) half ulps of the largest possible sum.
func TestAACMDCTMatchesDefinition(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for _, n := range []int{1024, 128} {
		m := newAACMDCT(n)
		sc := m.newScratch()
		src := make([]float32, n)
		sum := 0.0
		for i := range src {
			src[i] = float32(rng.NormFloat64() * 1000)
			sum += math.Abs(float64(src[i]))
		}
		dst := make([]float32, n)
		m.inverse(dst, src, sc)
		scale := 1.0 / float64(n) / 32768
		// The definition in float64, as the transform library states its naive
		// inverse: the first half of the output from one phase, the second from
		// another, which is the arrangement the window overlap reads.
		half := n / 2
		phase := math.Pi / (4.0 * float64(n))
		var worst, peak float64
		for i := 0; i < half; i++ {
			var down, up float64
			id := phase * float64(4*half-2*i-1)
			iu := phase * float64(3*n+2*i+1)
			for j := 0; j < n; j++ {
				a := float64(2*j + 1)
				down += math.Cos(a*id) * float64(src[j])
				up += math.Cos(a*iu) * float64(src[j])
			}
			worst = max(worst, math.Abs(float64(dst[i])-down*scale), math.Abs(float64(dst[i+half])+up*scale))
			peak = max(peak, math.Abs(down*scale), math.Abs(up*scale))
		}
		// The transform is float64 and rounds to float32 once: the error is half an
		// ulp of the sample (2^-24 of its magnitude) plus the float64 error of the
		// transform, 4*log2(N) half ulps of the sum of the absolute inputs, scaled.
		bound := peak*0x1p-24 + 4*math.Log2(float64(n))*0x1p-53*scale*sum
		t.Logf("n=%d: largest error %g, bound %g", n, worst, bound)
		if worst > bound {
			t.Errorf("n=%d: error %g exceeds %g", n, worst, bound)
		}
	}
}

// TestAACFFTParseval: a DFT of n points, whatever order it leaves its bins in,
// multiplies the energy by n. The sizes cover the base cases (2, 4 and 8) and
// every combine step up to 512.
func TestAACFFTParseval(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	for _, n := range []int{2, 4, 8, 16, 32, 64, 128, 256, 512} {
		f := newAACFFT(max(n, 8))
		f.n = n
		z := make([]cpx, n)
		var ein float64
		for i := range z {
			z[i] = cpx{rng.NormFloat64(), rng.NormFloat64()}
			ein += float64(z[i].re)*float64(z[i].re) + float64(z[i].im)*float64(z[i].im)
		}
		f.run(z)
		var eout float64
		for _, c := range z {
			eout += float64(c.re)*float64(c.re) + float64(c.im)*float64(c.im)
		}
		if math.Abs(eout-float64(n)*ein) > 1e-5*eout {
			t.Errorf("n=%d: energy %g, want %g", n, eout, float64(n)*ein)
		}
	}
}

func TestAACSplitRadixPermIsAPermutation(t *testing.T) {
	for _, n := range []int{8, 64, 512} {
		seen := make([]bool, n)
		for _, p := range newAACFFT(n).perm {
			if p < 0 || int(p) >= n || seen[p] {
				t.Fatalf("n=%d: %d repeated or out of range", n, p)
			}
			seen[p] = true
		}
	}
}

func TestAACWindows(t *testing.T) {
	aacSynthOnce.Do(aacSynthInit)
	for name, w := range map[string][]float32{"sine 1024": aacSineLong[:], "kbd 1024": aacKBDLong[:], "sine 128": aacSineShort[:], "kbd 128": aacKBDShort[:]} {
		n := len(w)
		for i := 0; i < n; i++ {
			// Princen-Bradley: the rising and falling halves of the overlap add to one
			if d := math.Abs(float64(w[i])*float64(w[i]) + float64(w[n-1-i])*float64(w[n-1-i]) - 1); d > 1e-6 {
				t.Fatalf("%s: w[%d]^2 + w[%d]^2 differs from 1 by %g", name, i, n-1-i, d)
			}
			if i > 0 && w[i] < w[i-1] {
				t.Fatalf("%s: not rising at %d", name, i)
			}
		}
	}
	// the windows are the reference's, built independently in float64, rounded to
	// float32 (half an ulp, 6e-8, at most)
	for name, c := range map[string]struct {
		got  []float32
		want []float64
	}{
		"sine 1024": {aacSineLong[:], exactSine(1024)},
		"sine 128":  {aacSineShort[:], exactSine(128)},
		"kbd 1024":  {aacKBDLong[:], exactKBD(4, 1024)},
		"kbd 128":   {aacKBDShort[:], exactKBD(6, 128)},
	} {
		for i, g := range c.got {
			if d := math.Abs(float64(g) - c.want[i]); d > 6e-8 {
				t.Errorf("%s[%d] = %v, the reference has %v (differ by %g)", name, i, g, c.want[i], d)
				break
			}
		}
	}
	for x, want := range map[float64]float64{0: 1, 1: 1.2660658777520082, 5: 27.239871823604442} {
		if got := aacBesselI0(x); math.Abs(got-want) > 1e-12*want {
			t.Errorf("I0(%g) = %v, want %v", x, got, want)
		}
	}
}

func TestAACNoiseSubstitution(t *testing.T) {
	// one band of eight coefficients substituted: the energy is the gain squared
	ch := &aacChannel{ics: testICS(false, 2)}
	ch.cb[0][0], ch.cb[0][1] = aacBandNoise, aacBandNoise
	ch.sf[0][0], ch.sf[0][1] = 16, -300 // 2^(16/4) = 16; the second is clipped to -100
	state := uint32(aacNoiseSeed)
	var out [aacFrameLen]float32
	ch.dequantise(&out, &state)
	// the generator is x -> 1664525 x + 1013904223
	seed := uint32(aacNoiseSeed)
	x := seed*1664525 + 1013904223
	if state == aacNoiseSeed || out[0] == 0 {
		t.Fatalf("no noise drawn: state %#x", state)
	}
	first := float64(int32(x))
	var energy float64
	for i := 0; i < 4; i++ {
		energy += float64(out[i]) * float64(out[i])
	}
	if math.Abs(energy-256) > 256*1e-5 {
		t.Errorf("band 0 energy %g, want 256", energy)
	}
	if out[0] != 0 && math.Signbit(first) != math.Signbit(float64(out[0])) {
		t.Errorf("the first sample has the sign of the first draw: %v and %v", first, out[0])
	}
	energy = 0
	for i := 4; i < 8; i++ {
		energy += float64(out[i]) * float64(out[i])
	}
	if want := math.Exp2(-50); math.Abs(energy-want) > want*1e-4 {
		t.Errorf("band 1 energy %g, want %g (the position clipped to -100)", energy, want)
	}
}

func TestAACStereoTools(t *testing.T) {
	ics := testICS(true, 2, 3, 5)
	swb := ics.swb
	mk := func(ms uint8, used [2]bool, cb1 [2][2]uint8, sf1 [2][2]int16) (*aacSynth, *aacElement) {
		s := newAACSynth(aacRate44, 2)
		el := &aacElement{id: aacIDCPE, nch: 2, common: true, msMask: ms}
		el.ch[0].ics, el.ch[1].ics = ics, ics
		for g := 0; g < 2; g++ {
			el.msUsed[g][0], el.msUsed[g][1] = used[0], used[1]
			el.ch[0].cb[g] = [aacMaxSfb]uint8{1, 1}
			el.ch[1].cb[g][0], el.ch[1].cb[g][1] = cb1[g][0], cb1[g][1]
			el.ch[1].sf[g][0], el.ch[1].sf[g][1] = sf1[g][0], sf1[g][1]
		}
		for i := range s.coef[0] {
			s.coef[0][i] = float32(i%7) + 1
			s.coef[1][i] = float32(i%5) - 2
		}
		return s, el
	}
	// M/S on band 0 only, in both groups: windows 0-2 and 3-7
	s, el := mk(1, [2]bool{true, false}, [2][2]uint8{{1, 1}, {1, 1}}, [2][2]int16{})
	a0, b0 := s.coef[0], s.coef[1]
	s.midSide(el)
	for w := 0; w < 8; w++ {
		for i := w*128 + int(swb[0]); i < w*128+int(swb[2]); i++ {
			sum := i < w*128+int(swb[1])
			if sum && (s.coef[0][i] != a0[i]+b0[i] || s.coef[1][i] != a0[i]-b0[i]) {
				t.Fatalf("window %d coefficient %d: M/S not applied", w, i)
			}
			if !sum && (s.coef[0][i] != a0[i] || s.coef[1][i] != b0[i]) {
				t.Fatalf("window %d coefficient %d: M/S applied outside its band", w, i)
			}
		}
	}
	// ms_mask_present 2 means every band, and a noise or intensity band is left out
	s, el = mk(2, [2]bool{}, [2][2]uint8{{1, aacBandNoise}, {14, 1}}, [2][2]int16{})
	a0, b0 = s.coef[0], s.coef[1]
	s.midSide(el)
	for g, w0 := range []int{0, 3} {
		for sfb := 0; sfb < 2; sfb++ {
			i := w0*128 + int(swb[sfb])
			done := s.coef[0][i] == a0[i]+b0[i]
			if want := !(g == 0 && sfb == 1) && !(g == 1 && sfb == 0); done != want {
				t.Errorf("group %d band %d: M/S applied %v, want %v", g, sfb, done, want)
			}
		}
	}
	// intensity: band type 15 is +1 and 14 is -1; M/S flips the sign; the position p
	// scales by 2^(-p/4), clipped to [-155, 100]
	for _, tc := range []struct {
		ms   uint8
		cb   uint8
		pos  int16
		want float32
	}{
		{0, 15, 8, 0.25}, {0, 14, 8, -0.25}, {1, 15, 8, -0.25}, {2, 14, 8, 0.25}, {0, 15, -8, 4}, {0, 15, 700, float32(math.Exp2(-25))}, {0, 15, -700, float32(math.Exp2(38.75))},
	} {
		s, el := mk(tc.ms, [2]bool{true, true}, [2][2]uint8{{tc.cb, 1}, {1, 1}}, [2][2]int16{{tc.pos, 0}, {0, 0}})
		s.coef[0][swb[0]] = 1
		s.intensity(el)
		if got := s.coef[1][swb[0]]; got != tc.want {
			t.Errorf("intensity ms %d type %d position %d: %v, want %v", tc.ms, tc.cb, tc.pos, got, tc.want)
		}
	}
}

func TestAACTNSFilter(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	// reference: the same recursion in float64 from the same reflection coefficients
	ref := func(coef []float64, ics *aacICS, tns *aacTNS, maxBands int) {
		mmm := min(maxBands, int(ics.maxSfb))
		numSwb := len(ics.swb) - 1
		for w := 0; w < int(ics.numWindows); w++ {
			bottom := numSwb
			for f := 0; f < int(tns.n[w]); f++ {
				tf := &tns.filt[w][f]
				top := bottom
				bottom = max(0, top-int(tf.length))
				order := int(tf.order)
				if order == 0 {
					continue
				}
				lpc := make([]float64, order)
				for i := 0; i < order; i++ {
					r := -float64(aacTNSMap[2*int(tf.compress)+int(tf.res)-3][tf.coef[i]])
					prev := append([]float64(nil), lpc...)
					for j := 0; j < i; j++ {
						lpc[j] = prev[j] + r*prev[i-1-j]
					}
					lpc[i] = r
				}
				start, end := int(ics.swb[min(bottom, mmm)]), int(ics.swb[min(top, mmm)])
				if end-start <= 0 {
					continue
				}
				idx := func(m int) int {
					if tf.down {
						return end - 1 - m + w*128
					}
					return start + m + w*128
				}
				for m := 0; m < end-start; m++ {
					for k := 1; k <= min(m, order); k++ {
						coef[idx(m)] -= coef[idx(m-k)] * lpc[k-1]
					}
				}
			}
		}
	}
	// the reflection coefficients stay small so that the filters are stable and
	// the float32 recursion stays close to the float64 one
	long := testICS(false, 40)
	wide := testICS(false, 45)
	short := testICS(true, 14, 1, 1, 1, 1, 1, 1, 1, 1)
	for name, tc := range map[string]struct {
		ics     aacICS
		filters [][]aacTNSFilter // by window
	}{
		"long, two filters, one up, one down, one of length zero": {long, [][]aacTNSFilter{{
			{length: 20, order: 8, res: 4, coef: [12]uint8{1, 2, 15, 14, 1, 2, 15, 14}},
			{length: 10, order: 3, down: true, res: 3, compress: 1, coef: [12]uint8{1, 3, 3}},
			{length: 0, order: 4, res: 3, coef: [12]uint8{1, 2, 7, 6}},
		}}},
		"long, an empty filter": {long, [][]aacTNSFilter{{{length: 5, order: 0}}}},
		"long, limited by the band count": {wide, [][]aacTNSFilter{{
			{length: 30, order: 12, down: true, res: 4, coef: [12]uint8{1, 15, 2, 14, 1, 15, 2, 14, 1, 15, 2, 14}},
		}}},
		"short, windows 0 and 5": {short, [][]aacTNSFilter{
			{{length: 6, order: 5, res: 4, compress: 1, coef: [12]uint8{1, 6, 2, 7, 1}}},
			nil, nil, nil, nil,
			{{length: 14, order: 7, down: true, res: 3, coef: [12]uint8{1, 7, 1, 7, 1, 7, 1}}},
		}},
	} {
		ch := &aacChannel{ics: tc.ics}
		ch.tns.present = true
		for w, fs := range tc.filters {
			ch.tns.n[w] = uint8(len(fs))
			for f, tf := range fs {
				ch.tns.filt[w][f] = tf
			}
		}
		s := newAACSynth(aacRate44, 1)
		row := 0
		if tc.ics.short() {
			row = 1
		}
		var coef [aacFrameLen]float32
		want := make([]float64, aacFrameLen)
		for i := range coef {
			coef[i] = float32(rng.NormFloat64() * 100)
			want[i] = float64(coef[i])
		}
		s.rate = aacRate44
		ref(want, &tc.ics, &ch.tns, int(aacTNSMaxBands[row][aacRate44]))
		s.tns(&coef, ch)
		var worst float64
		for i := range coef {
			worst = max(worst, math.Abs(float64(coef[i])-want[i])/(1+math.Abs(want[i])))
		}
		if worst > 1e-4 {
			t.Errorf("%s: relative error %g against the float64 filter", name, worst)
		}
	}
	// no band to filter: untouched
	ch := &aacChannel{ics: testICS(false, 0)}
	ch.tns.present, ch.tns.n[0] = true, 1
	ch.tns.filt[0][0] = aacTNSFilter{length: 5, order: 2, res: 3, coef: [12]uint8{1, 2}}
	var coef [aacFrameLen]float32
	coef[0], coef[1] = 1, 2
	newAACSynth(aacRate44, 1).tns(&coef, ch)
	if coef[0] != 1 || coef[1] != 2 {
		t.Error("a TNS filter with no band to cover changed the spectrum")
	}
}

func TestAACTNSMap(t *testing.T) {
	// the four tables: zero first, and the values that the reference prints
	for idx, want := range map[[2]int]float32{{0, 1}: -0.43388373, {0, 3}: -0.97492790, {0, 4}: 0.98480773, {2, 3}: 0.34202015, {3, 7}: 0.18374951} {
		if got := aacTNSMap[idx[0]][idx[1]]; got != want {
			t.Errorf("map %d [%d] = %v, want %v", idx[0], idx[1], got, want)
		}
	}
	// the five that differ from the reference decoder's printed table by one ulp
	for idx, printed := range map[[2]int]float32{{0, 6}: 0.64278758, {1, 1}: -0.20791170, {1, 9}: 0.96182561, {2, 2}: 0.64278758, {3, 1}: -0.20791170} {
		got := aacTNSMap[idx[0]][idx[1]]
		if d := int64(math.Float32bits(got)) - int64(math.Float32bits(printed)); got == printed || d < -1 || d > 1 {
			t.Errorf("map %d [%d] = %v, the printed table has %v: expected one ulp apart", idx[0], idx[1], got, printed)
		}
	}
	for i := range aacTNSMap {
		if aacTNSMap[i][0] != 0 || math.Signbit(float64(aacTNSMap[i][0])) {
			t.Errorf("map %d starts with %v", i, aacTNSMap[i][0])
		}
	}
}
