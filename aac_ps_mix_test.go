// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"math/cmplx"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// The tests of the decorrelator and the mixing follow docs/aac-ps-mixing.md section 7:
// 7.1 properties that need no reference, 7.2 the independent reference of
// aac_ps_mixref_test.go. The streams against ffmpeg are in aac_ps_streams_test.go.

// psTestState is a PS state as the parser would leave it for a frame: iid and icc give
// the index of envelope e, band b.
func psTestState(is34 bool, iidMode, iccMode, numEnv int, border []int, iid, icc func(e, b int) int) *psState {
	p := &psState{enableIID: iidMode >= 0, enableICC: iccMode >= 0, is34: is34, numEnv: numEnv, start: true}
	if iidMode >= 0 {
		p.iidMode, p.nrIID, p.iidQuant = iidMode, psBandsOf[iidMode], iidMode >= 3
	}
	if iccMode >= 0 {
		p.iccMode, p.nrICC = iccMode, psBandsOf[iccMode]
	}
	for i, b := range border {
		p.border[i] = int8(b)
	}
	for e := 0; e < numEnv; e++ {
		for b := 0; b < psMaxBands; b++ {
			if iid != nil && b < p.nrIID {
				p.iid[e][b] = int8(iid(e, b))
			}
			if icc != nil && b < p.nrICC {
				p.icc[e][b] = int8(icc(e, b))
			}
		}
	}
	return p
}

var psFixedBorders = map[int][]int{1: {-1, 31}, 2: {-1, 15, 31}, 4: {-1, 7, 15, 23, 31}}

// psNoiseRows makes the rows of a frame: complex noise of the given level, with a loud
// burst over the slots from burst on (0 for none) so that the transient detector acts.
func psNoiseRows(rng *rand.Rand, rows int, level float64, burst int) (s [sbrQMFSlots][psMaxRows]complex128) {
	for n := range s {
		g := level
		if burst > 0 && n >= burst && n < burst+3 {
			g *= 40
		}
		for k := 0; k < rows; k++ {
			s[n][k] = complex(g*rng.NormFloat64(), g*rng.NormFloat64())
		}
	}
	return s
}

// psRefStep is one frame of a reference scenario.
type psRefStep struct {
	p     *psState
	fresh bool
	top   int
	burst int
}

// psRefScenarios exercise every remap, both layouts, switches, the clearing above the
// SBR top, a frame without data, equal borders and every envelope count.
func psRefScenarios() map[string][]psRefStep {
	rnd := rand.New(rand.NewSource(11))
	iidC := func(e, b int) int { return (e*5+b*3+rnd.Intn(4))%15 - 7 }
	iidF := func(e, b int) int { return (e*7+b*5+rnd.Intn(4))%31 - 15 }
	icc := func(e, b int) int { return (e + 2*b + rnd.Intn(3)) % 8 }
	return map[string][]psRefStep{
		"20 bands, 4 envelopes": {
			{psTestState(false, 1, 1, 4, psFixedBorders[4], iidC, icc), true, 55, 9},
			{psTestState(false, 1, 1, 4, psFixedBorders[4], iidC, icc), true, 55, 0},
			{psTestState(false, 1, 1, 2, psFixedBorders[2], iidC, icc), true, 55, 20},
		},
		"34 bands fine": {
			{psTestState(true, 5, 2, 2, psFixedBorders[2], iidF, icc), true, 64, 12},
			{psTestState(true, 5, 2, 1, psFixedBorders[1], iidF, icc), true, 64, 0},
		},
		"10 bands to 20, fine and coarse": {
			{psTestState(false, 0, 0, 2, psFixedBorders[2], iidC, icc), true, 50, 0},
			{psTestState(false, 3, 3, 2, psFixedBorders[2], iidF, icc), true, 50, 6},
		},
		"20 to 34 and 10 to 34 maps": {
			{psTestState(true, 1, 2, 2, psFixedBorders[2], iidC, icc), true, 64, 0},
			{psTestState(true, 5, 0, 2, psFixedBorders[2], iidF, icc), true, 64, 0},
			{psTestState(true, 0, 0, 1, psFixedBorders[1], iidC, icc), true, 64, 0},
		},
		"switches, no data, a falling top": {
			{psTestState(false, 1, 1, 2, psFixedBorders[2], iidC, icc), true, 60, 5},
			{psTestState(true, 2, 2, 2, psFixedBorders[2], iidC, icc), true, 60, 5},
			{psTestState(true, 2, 2, 2, psFixedBorders[2], iidC, icc), false, 40, 0},
			{psTestState(true, 2, 2, 1, psFixedBorders[1], iidC, icc), true, 24, 0},
			{psTestState(false, 1, 1, 1, psFixedBorders[1], iidC, icc), true, 24, 3},
		},
		"free borders, equal borders": {
			{psTestState(false, 1, 1, 4, []int{-1, 0, 0, 10, 31}, iidC, icc), true, 55, 3},
			{psTestState(false, 1, 1, 3, []int{-1, 31, 31, 31}, iidC, icc), true, 55, 0},
			{psTestState(false, 1, 1, 2, []int{-1, 15, 31}, nil, nil), true, 55, 0},
		},
	}
}

// TestPSMixAgainstReference: the decoder's left and right rows agree with the
// independent reference per sample within 2^-44 of the largest input of the row over the
// last 600 slots (section 7.2). The bound is asserted per sample, never as an average.
func TestPSMixAgainstReference(t *testing.T) {
	if testing.Short() || sbrEmulated() {
		t.Skip("the reference is in math/big and double-double: the native lanes run it")
	}
	for name, steps := range psRefScenarios() {
		t.Run(name, func(t *testing.T) {
			rng := rand.New(rand.NewSource(5))
			m := new(psMixer)
			ref := newPSRefMix(false)
			var worst, worstRel float64
			for fi, st := range steps {
				h := new(psHybrid)
				dl := psDecLayouts[0]
				if st.p.is34 {
					dl = psDecLayouts[1]
				}
				h.rows = psNoiseRows(rng, dl.rows, 300, st.burst)
				s := h.rows
				m.apply(h, st.p, st.fresh, st.top)
				out := ref.frame(st.p, st.fresh, st.top, &s)
				for k := 0; k < dl.rows; k++ {
					var peak float64
					hist := ref.s[k]
					for _, v := range hist[max(0, len(hist)-psRefLen):] {
						peak = math.Max(peak, cmplx.Abs(v))
					}
					for n := 0; n < sbrQMFSlots; n++ {
						for side, pair := range [2]struct {
							dec complex128
							ref cdd
						}{{h.rows[n][k], out.l[n][k]}, {m.rr[n][k], out.r[n][k]}} {
							d := pair.ref.diff(pair.dec)
							worst = math.Max(worst, d)
							if peak > 0 {
								worstRel = math.Max(worstRel, d/peak)
							}
							if bound := 0x1p-44 * peak; d > bound {
								t.Fatalf("frame %d row %d slot %d side %d: |dec - ref| = %g over %g", fi, k, n, side, d, bound)
							}
						}
					}
				}
			}
			t.Logf("largest difference %.3g, %.3g of the row's peak (bound 2^-44 = %.3g)", worst, worstRel, 0x1p-44)
		})
	}
}

// psRefUnit is the table of anchor gains of section 4.1: IID 0 and each ICC index, the
// gain g_Ls = g_Rs and g_Ld = -g_Rd.
var psAnchors = [8][3]float64{
	{1, 1, 0},
	{0.937, 0.98412397592986, 0.17748239349299},
	{0.84118, 0.95947381412939, 0.28179779984947},
	{0.60092, 0.89468430186295, 0.44669900380458},
	{0.36764, 0.82693409652765, 0.56229885292431},
	{0, math.Sqrt2 / 2, math.Sqrt2 / 2},
	{-0.589, 0.45332107826573, 0.89134729482957},
	{-1, 0, 1},
}

// TestPSMixGains (P4): the anchor table, c_L^2 + c_R^2 = 2 for all 46 x 8 index pairs,
// and the gains against the reference at 192 bits. The reference holds eq. 7 as well:
// the decoder follows eq. 8 and differs from eq. 7 where the specification says it does
// (Q1).
func TestPSMixGains(t *testing.T) {
	for icc, a := range psAnchors {
		g := psMixGain(0, icc, false)
		want := psGain{a[1], a[2], a[1], -a[2]}
		for i := range g {
			if math.Abs(g[i]-want[i]) > 2e-14 {
				t.Errorf("IID 0, ICC index %d: gain %d is %.14f, want %.14f", icc, i, g[i], want[i])
			}
		}
	}
	// l = d and r = -d exactly for IID 0 and ICC index 7; l - r = sqrt2 d for index 5
	if g := psMixGain(0, 7, true); math.Abs(g[psGLs]) > 1e-15 || math.Abs(g[psGLd]-1) > 1e-15 || math.Abs(g[psGRd]+1) > 1e-15 {
		t.Errorf("ICC index 7: %v", g)
	}
	var diff8, diff7 float64
	for fine, tab := range [2][][8]psGain{psMixCoarse, psMixFine} {
		db := psIIDdBCoarse[:]
		if fine == 1 {
			db = psIIDdBFine[:]
		}
		for i := range tab {
			for icc := range tab[i] {
				g := tab[i][icc]
				if s := g[0]*g[0] + g[1]*g[1] + g[2]*g[2] + g[3]*g[3]; math.Abs(s-2) > 1e-13 {
					t.Errorf("IID %d dB, ICC %d: sum of squares %v, want 2", db[i], icc, s)
				}
				r8 := psRefGains(int(db[i]), icc, false)
				r7 := psRefGains(int(db[i]), icc, true)
				for j := range g {
					f8, _ := r8[j].Float64()
					f7, _ := r7[j].Float64()
					diff8 = math.Max(diff8, math.Abs(g[j]-f8))
					diff7 = math.Max(diff7, math.Abs(g[j]-f7))
				}
			}
		}
	}
	t.Logf("largest gain difference to the eq. 8 reference %.3g, to eq. 7 %.3g", diff8, diff7)
	if diff8 > 1e-14 {
		t.Errorf("the gains differ from the eq. 8 reference by %g", diff8)
	}
	if diff7 < 1e-3 {
		t.Errorf("eq. 7 and eq. 8 should differ at large IID: %g", diff7)
	}
}

// TestPSLinkDecayAgreement measures the agreement of exp(-d/7) with the three values the
// references hold (the digits are those of docs/aac-ps-mixing.md section 3.3): 4.4e-15.
func TestPSLinkDecayAgreement(t *testing.T) {
	held := [3]float64{0.65143905753106, 0.56471812200776, 0.48954165955695}
	var worst float64
	for i, h := range held {
		worst = math.Max(worst, math.Abs(psLinkDecay[i]-h))
	}
	t.Logf("exp(-d/7) against the held values: %.2g", worst)
	if worst > 5e-15 {
		t.Errorf("the link decay differs from the held values by %g", worst)
	}
	if e := math.Abs(psPeakDecay - math.Exp(-4.0/15)); e > 2e-15 {
		t.Errorf("peak decay differs from exp(-4/15) by %g", e)
	}
}

// psImpulse drives the decorrelator of row k alone (the gains are set so that the left
// row is the decorrelated signal) with a unit impulse at slot 0 and returns its output
// over frames frames.
func psImpulse(is34 bool, k, frames int) []complex128 {
	m := new(psMixer)
	dl := psDecLayouts[0]
	if is34 {
		dl = psDecLayouts[1]
	}
	for n := range m.gain {
		for b := range m.gain[n] {
			m.gain[n][b] = psGain{0, 1, 0, -1}
			m.tg[n][b] = 1
		}
	}
	h := new(psHybrid)
	var out []complex128
	for f := 0; f < frames; f++ {
		h.rows = [sbrQMFSlots][psMaxRows]complex128{}
		if f == 0 {
			h.rows[0][k] = 1
		}
		m.row(h, dl, k)
		for n := range h.rows {
			out = append(out, h.rows[n][k])
		}
	}
	return out
}

// TestPSAllPass (P1, P2, P8): every all-pass row of both layouts has unit gain at every
// frequency, its impulse response is the series of section 3.2, and it starts at slot 2
// with -a0 a1 a2 gamma^3 times the rotation; the delay rows delay by 14 and 1 slots.
func TestPSAllPass(t *testing.T) {
	for layout, is34 := range []bool{false, true} {
		dl := psDecLayouts[layout]
		ly := psLayoutOf(is34)
		for k := 0; k < dl.nAP; k++ {
			h := psImpulse(is34, k, 40)
			for w := 0; w < 1024; w++ {
				var sum complex128
				for n, v := range h {
					sum += v * cmplx.Rect(1, -2*math.Pi*float64(w)/1024*float64(n))
				}
				if d := math.Abs(cmplx.Abs(sum) - 1); d > 1e-13 {
					t.Fatalf("layout %d row %d: |A| = 1 + %g at omega %d/1024", layout, k, d, w)
				}
			}
			q1 := ly.nSplit
			split := k < ly.start[ly.nSplit]
			f24 := psRefCentre24[layout][0]
			if split {
				f24 = psRefCentre24[layout][k]
			} else {
				f24 = 24*int(dl.qmf[k]) + 12
			}
			series := psRefImpulse(int(dl.qmf[k]), q1, split, f24)
			var worst float64
			for n := range series {
				worst = math.Max(worst, series[n].diff(h[n]))
			}
			if worst > 1e-14 {
				t.Errorf("layout %d row %d: impulse response differs from the series by %g", layout, k, worst)
			}
			if h[0] != 0 || h[1] != 0 {
				t.Errorf("layout %d row %d: output before slot 2: %v %v", layout, k, h[0], h[1])
			}
			gamma := 1.0
			if !split {
				gamma = 1 - 0.05*float64(int(dl.qmf[k])-q1)
			}
			first := -psLinkDecay[0] * psLinkDecay[1] * psLinkDecay[2] * gamma * gamma * gamma
			phi := psRotation(390, int(math.Round(ly.centre(k)*24)))
			if d := cmplx.Abs(h[2] - complex(first, 0)*phi); d > 1e-14 {
				t.Errorf("layout %d row %d: first sample %v, want %v", layout, k, h[2], complex(first, 0)*phi)
			}
		}
		for k := dl.nAP; k < dl.rows; k++ {
			delay := 1
			if k < dl.nLong {
				delay = 14
			}
			h := psImpulse(is34, k, 2)
			for n, v := range h {
				want := complex(0, 0)
				if n == delay {
					want = 1
				}
				if v != want {
					t.Fatalf("layout %d row %d: output %v at slot %d, want %v", layout, k, v, n, want)
				}
			}
		}
	}
}

// TestPSRowClasses: the class counts of section 3.1 (30 and 50 all-pass rows, 12 long
// delay rows, the rest short) and the centre frequencies against the specification's.
func TestPSRowClasses(t *testing.T) {
	for layout, c := range [2][3]int{{30, 42, 71}, {50, 62, 91}} {
		dl := psDecLayouts[layout]
		if dl.nAP != c[0] || dl.nLong != c[1] || dl.rows != c[2] {
			t.Errorf("layout %d: %d all-pass, long up to %d, %d rows; want %v", layout, dl.nAP, dl.nLong, dl.rows, c)
		}
		ly := psLayoutOf(layout == 1)
		for k, f24 := range psRefCentre24[layout] {
			if got := ly.centre(k) * 24; math.Abs(got-float64(f24)) > 1e-12 {
				t.Errorf("layout %d row %d: centre %v/24, want %d/24", layout, k, got, f24)
			}
		}
	}
}

// TestPSTransient (P3): the detector's gain is invariant under scaling by powers of two,
// is 1 on constant-power input and in silence, falls after a burst and rises back toward
// 1, and never exceeds 1.
func TestPSTransient(t *testing.T) {
	dl := psDecLayouts[0]
	rng := rand.New(rand.NewSource(2))
	// constant power per band: unit modulus rows with random phases
	var steady [sbrQMFSlots][psMaxRows]complex128
	for n := range steady {
		for k := 0; k < dl.rows; k++ {
			steady[n][k] = cmplx.Rect(3, 2*math.Pi*rng.Float64())
		}
	}
	m := new(psMixer)
	for f := 0; f < 3; f++ {
		m.transient(&steady, dl)
		for n := range m.tg {
			for b := 0; b < dl.nb; b++ {
				if m.tg[n][b] != 1 {
					t.Fatalf("constant power: gain %v at frame %d slot %d band %d", m.tg[n][b], f, n, b)
				}
			}
		}
	}
	var silent [sbrQMFSlots][psMaxRows]complex128
	m = new(psMixer)
	m.transient(&silent, dl)
	if m.tg[31][5] != 1 {
		t.Errorf("silence: gain %v", m.tg[31][5])
	}
	// a burst of three slots, then the level of before; scaled copy
	burst := psNoiseRows(rand.New(rand.NewSource(3)), dl.rows, 1, 8)
	for n := range burst {
		for k := 0; k < dl.rows; k++ {
			burst[n][k] = cmplx.Rect(cmplx.Abs(burst[n][k]), 0.7*float64(k))
			if n >= 8 && n < 11 {
				burst[n][k] = cmplx.Rect(40, 0.7*float64(k))
			} else {
				burst[n][k] = cmplx.Rect(1, 0.7*float64(k))
			}
		}
	}
	a, b := new(psMixer), new(psMixer)
	scaled := burst
	for n := range scaled {
		for k := range scaled[n] {
			scaled[n][k] *= 1 << 20
		}
	}
	a.transient(&burst, dl)
	b.transient(&scaled, dl)
	lo := 1.0
	for n := range a.tg {
		for bd := 0; bd < dl.nb; bd++ {
			if a.tg[n][bd] != b.tg[n][bd] || a.tg[n][bd] > 1 || a.tg[n][bd] <= 0 {
				t.Fatalf("slot %d band %d: gain %v, scaled %v", n, bd, a.tg[n][bd], b.tg[n][bd])
			}
		}
		lo = math.Min(lo, a.tg[n][19])
	}
	if lo > 0.9 {
		t.Errorf("no duck after a burst: lowest gain %v", lo)
	}
	// the levels of before again: the gain rises back toward 1
	steadyUnit := burst
	for n := range steadyUnit {
		for k := range steadyUnit[n] {
			steadyUnit[n][k] = cmplx.Rect(1, 0.7*float64(k))
		}
	}
	prev := a.tg[31][19]
	for f := 0; f < 30; f++ {
		a.transient(&steadyUnit, dl)
	}
	if end := a.tg[31][19]; !(end > prev && end > 0.999) {
		t.Errorf("the gain went from %v to %v over 30 frames at a steady level", prev, end)
	}
}

// TestPSMaps (P5): 10 -> 34 is 20 -> 34 after 10 -> 20, every map keeps a constant, the
// averages of indices truncate toward zero, and the spec's examples hold.
func TestPSMaps(t *testing.T) {
	src := make([]int, 10)
	for i := range src {
		src[i] = 3*i - 12
	}
	var via, direct [34]int
	var mid [20]int
	psRemapBands(mid[:], src, 10, 20)
	psRemapBands(via[:], mid[:], 20, 34)
	psRemapBands(direct[:], src, 10, 34)
	if via != direct {
		t.Errorf("10 -> 34 is %v, 20 -> 34 after 10 -> 20 is %v", direct, via)
	}
	// the written-out form of section 4.2
	groups := [10][2]int{{0, 3}, {3, 6}, {6, 10}, {10, 12}, {12, 16}, {16, 18}, {18, 20}, {20, 24}, {24, 28}, {28, 34}}
	for s, g := range groups {
		for b := g[0]; b < g[1]; b++ {
			if direct[b] != src[s] {
				t.Errorf("10 -> 34: band %d is %d, want source %d = %d", b, direct[b], s, src[s])
			}
		}
	}
	for _, c := range []struct{ from, to int }{{10, 20}, {10, 34}, {20, 34}, {34, 20}, {20, 20}, {34, 34}} {
		in := make([]int, c.from)
		out := make([]int, c.to)
		for i := range in {
			in[i] = -5
		}
		psRemapBands(out, in, c.from, c.to)
		fin, fout := make([]float64, c.from), make([]float64, c.to)
		for i := range fin {
			fin[i] = 0.3
		}
		psRemapBands(fout, fin, c.from, c.to)
		for i := range out {
			if out[i] != -5 || math.Abs(fout[i]-0.3) > 1e-15 {
				t.Errorf("%d -> %d: band %d is %d / %v for a constant input", c.from, c.to, i, out[i], fout[i])
			}
		}
	}
	// 34 -> 20: (2a + b)/3 truncates toward zero: (2*-3 + -2)/3 = -2, not -3
	in := make([]int, 34)
	in[0], in[1] = -3, -2
	out := make([]int, 20)
	psRemapBands(out, in, 34, 20)
	if out[0] != -2 {
		t.Errorf("34 -> 20 band 0: %d, want -2 (truncation toward zero)", out[0])
	}
	// 20 -> 34: band 1 is (0+1)/2 and band 4 (2+3)/2; 34 -> 20 band 18 is (28+29+30+31)/4
	in20 := make([]int, 20)
	in20[0], in20[1], in20[2], in20[3] = 1, 4, 2, 7
	var o34 [34]int
	psRemapBands(o34[:], in20, 20, 34)
	if o34[1] != 2 || o34[4] != 4 || o34[0] != 1 {
		t.Errorf("20 -> 34: %v", o34[:6])
	}
	in[28], in[29], in[30], in[31] = 1, 2, 3, 6
	psRemapBands(out, in, 34, 20)
	if out[18] != 3 {
		t.Errorf("34 -> 20 band 18: %d, want 3", out[18])
	}
}

// TestPSInterpolation (P6): at an envelope's parameter position the gain is that
// envelope's matrix exactly, a constant matrix gives constant gains, and a zero-length
// envelope covers no slot and still starts the next one.
func TestPSInterpolation(t *testing.T) {
	iid := func(e, b int) int { return []int{-5, 3, 6}[e] }
	icc := func(e, b int) int { return []int{1, 2, 4}[e] }
	p := psTestState(false, 1, 1, 3, []int{-1, 15, 15, 31}, iid, icc)
	m := new(psMixer)
	first := psMixGain(0, 0, false)
	m.matrices(p, true, 20)
	h1, h2, h3 := psMixGain(-5, 1, false), psMixGain(3, 2, false), psMixGain(6, 4, false)
	for b := 0; b < 20; b++ {
		if m.gain[15][b] != h1 {
			t.Fatalf("slot 15 is %v, want the first envelope's matrix %v", m.gain[15][b], h1)
		}
		if m.gain[31][b] != h3 {
			t.Fatalf("slot 31 is %v, want %v", m.gain[31][b], h3)
		}
		for i := range h1 {
			// the zero-length envelope 2 skips no matrix: slot 16 starts from h2
			if want := h2[i] + (h3[i]-h2[i])/16; math.Abs(m.gain[16][b][i]-want) > 1e-15 {
				t.Fatalf("slot 16: %v, want %v", m.gain[16][b][i], want)
			}
			// the first envelope rises from the all-zero matrix of the first frame
			if want := h1[i] * 1 / 16; math.Abs(m.gain[0][b][i]-want) > 1e-15 {
				t.Fatalf("slot 0: %v, want %v (a fade in from zero over %d slots)", m.gain[0][b][i], want, 16)
			}
		}
	}
	_ = first
	if m.cur[3] != h3 {
		t.Errorf("the carried matrix is %v, want %v", m.cur[3], h3)
	}
	// a frame whose parameters are the carried matrix keeps it
	same := psTestState(false, 1, 1, 1, []int{-1, 31}, func(e, b int) int { return 6 }, func(e, b int) int { return 4 })
	m.matrices(same, true, 20)
	for n := range m.gain {
		if m.gain[n][7] != h3 {
			t.Fatalf("constant matrix: slot %d is %v", n, m.gain[n][7])
		}
	}
	// a frame without data holds the matrix too
	m.matrices(same, false, 20)
	for n := range m.gain {
		if m.gain[n][7] != h3 {
			t.Fatalf("no data: slot %d is %v", n, m.gain[n][7])
		}
	}
}

// TestPSMixTiming (P8): an impulse in the mono rows at slot 5 reaches the outputs at
// slot 5 (the mono path) and from slot 7 on (the all-pass), and at no slot between.
func TestPSMixTiming(t *testing.T) {
	m := new(psMixer)
	p := psTestState(false, 1, 1, 1, psFixedBorders[1], func(e, b int) int { return 0 }, func(e, b int) int { return 3 })
	for f := 0; f < 2; f++ {
		h := new(psHybrid)
		if f == 1 {
			h.rows[5][12] = 1000 // an all-pass row
		}
		m.apply(h, p, true, 64)
		if f == 0 {
			continue
		}
		for n := 0; n < sbrQMFSlots; n++ {
			v, w := h.rows[n][12], m.rr[n][12]
			switch {
			case n == 5:
				if v == 0 || w == 0 {
					t.Errorf("the mono path is missing at slot 5: %v %v", v, w)
				}
			case n == 6:
				if v != 0 || w != 0 {
					t.Errorf("output at slot 6: %v %v", v, w)
				}
			case n < 5:
				if v != 0 || w != 0 {
					t.Errorf("output before the impulse at slot %d: %v %v", n, v, w)
				}
			case n == 7 || n >= 10:
				// the chain's response has terms at lags 2, 5, 6, 7, ...: slots 8 and 9 stay zero
				if v == 0 || w == 0 {
					t.Errorf("no decorrelated output at slot %d", n)
				}
			case n == 8 || n == 9:
				if v != 0 || w != 0 {
					t.Errorf("output at slot %d, which the response of the chain skips: %v %v", n, v, w)
				}
			}
		}
	}
}

// TestPSMixOutputStatistics (P4): over a long stationary noise signal the IID and ICC of
// the output in the long-delay bands equal the transmitted ones to the stated tolerance
// for procedure A with eq. 8 (the approximation of the rotation angle makes the ICC off
// by a measured amount at large IID; the table logs it).
func TestPSMixOutputStatistics(t *testing.T) {
	rng := rand.New(rand.NewSource(8))
	for _, c := range []struct{ iid, icc int }{{0, 1}, {0, 3}, {0, 5}, {2, 2}, {-3, 4}, {4, 3}, {0, 6}} {
		m := new(psMixer)
		p := psTestState(false, 1, 1, 1, psFixedBorders[1], func(e, b int) int { return c.iid }, func(e, b int) int { return c.icc })
		var ll, rr, lr float64
		for f := 0; f < 400; f++ {
			h := new(psHybrid)
			h.rows = psNoiseRows(rng, 71, 100, 0)
			m.apply(h, p, true, 64)
			if f < 4 {
				continue
			}
			for n := 0; n < sbrQMFSlots; n++ {
				for k := 30; k < 71; k++ { // the long and the short delay rows (bands 18, 19)
					l, r := h.rows[n][k], m.rr[n][k]
					ll += real(l)*real(l) + imag(l)*imag(l)
					rr += real(r)*real(r) + imag(r)*imag(r)
					lr += real(l)*real(r) + imag(l)*imag(r)
				}
			}
		}
		gotIID := 10 * math.Log10(ll/rr)
		gotICC := lr / math.Sqrt(ll*rr)
		wantIID := float64(psIIDdBCoarse[c.iid+7])
		wantICC := psICC[c.icc]
		t.Logf("IID index %d (%g dB), ICC index %d (%g): output %.3f dB, %.4f", c.iid, wantIID, c.icc, wantICC, gotIID, gotICC)
		if math.Abs(gotIID-wantIID) > 0.1 || math.Abs(gotICC-wantICC) > 0.01 {
			t.Errorf("IID %.3f dB (want %g), ICC %.4f (want %g)", gotIID, wantIID, gotICC, wantICC)
		}
	}
}

// TestPSMixDoesNotAllocate: a frame's decorrelation and mixing, and the two hybrid
// syntheses, allocate nothing in steady state, in both layouts.
func TestPSMixDoesNotAllocate(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	for _, is34 := range []bool{false, true} {
		p := psTestState(is34, 1+b2i(is34), 1+b2i(is34), 4, psFixedBorders[4], func(e, b int) int { return (e+b)%15 - 7 }, func(e, b int) int { return (e + 2*b) % 8 })
		dl := psDecLayouts[b2i(is34)]
		m := new(psMixer)
		h := new(psHybrid)
		rows := psNoiseRows(rng, dl.rows, 100, 7)
		var x [sbrQMFSlots][sbrSynBands]complex128
		if a := testing.AllocsPerRun(20, func() {
			h.rows = rows
			m.apply(h, p, true, 60)
			h.synthesise(&x, is34)
			psSynthesise(&m.rr, &m.xr, is34)
		}); a != 0 {
			t.Errorf("is34 %v: %v allocations per frame", is34, a)
		}
	}
}

// BenchmarkPSMix is one frame (32 slots) of decorrelation, mixing and the two hybrid
// syntheses, from mono rows to the left and right 64-band blocks.
func BenchmarkPSMix(b *testing.B) {
	for _, is34 := range []bool{false, true} {
		name := map[bool]string{false: "20bands", true: "34bands"}[is34]
		b.Run(name, func(b *testing.B) {
			rng := rand.New(rand.NewSource(4))
			p := psTestState(is34, 1+b2i(is34), 1+b2i(is34), 4, psFixedBorders[4], func(e, b int) int { return (e+b)%15 - 7 }, func(e, b int) int { return (e + 2*b) % 8 })
			dl := psDecLayouts[b2i(is34)]
			m := new(psMixer)
			h := new(psHybrid)
			rows := psNoiseRows(rng, dl.rows, 100, 7)
			var x [sbrQMFSlots][sbrSynBands]complex128
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h.rows = rows
				m.apply(h, p, true, 55)
				h.synthesise(&x, is34)
				psSynthesise(&m.rr, &m.xr, is34)
			}
		})
	}
}

// BenchmarkPSDecode is the whole decode of an HE-AAC v2 ADTS file (PS_BENCH_FILE) with
// PS applied, the two syntheses included, and without PS (the mono SBR path) for the
// difference; the same file under `ffmpeg -benchmark` is the comparison.
func BenchmarkPSDecode(b *testing.B) {
	path := os.Getenv("PS_BENCH_FILE")
	if path == "" {
		path = filepath.Join("testdata", "aac", "he44-ps.aac")
	}
	cfg, fr := aacADTSFrames(b, path)
	for _, apply := range []bool{true, false} {
		b.Run(map[bool]string{true: "ps", false: "mono"}[apply], func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				cfg.sbr = true
				p, err := newAACParser(cfg, DefaultMaxAudioChannels)
				if err != nil {
					b.Fatal(err)
				}
				p.enableSBR()
				synth := newAACSynth(cfg.rateIndex, cfg.channels)
				var out [2][]float32
				out[0], out[1] = make([]float32, 2048), make([]float32, 2048)
				for _, f := range fr {
					els, err := p.parseFrame(f)
					if err != nil {
						b.Fatal(err)
					}
					var pcm [2][aacFrameLen]float32
					synth.element(&els[0], 0, [2][]float32{pcm[0][:], pcm[1][:]})
					s := p.sbrOf(els[0].id, els[0].tag)
					s.psApplyTest = apply
					s.process(1, [2][]float32{pcm[0][:], pcm[1][:]}, out, els[0].sbr != nil)
				}
			}
		})
	}
}
