// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"math/cmplx"
	"testing"
)

// The tests of the full profile (PS5): procedure B and IPD/OPD, docs/aac-ps-mixing.md
// section 6. The independent reference of aac_ps_mixref_test.go covers both
// (TestPSMixAgainstReference scenarios "procedure B" and "phases"); the streams against
// ffmpeg are S16 and S18 of aac_ps_streams_test.go; ISO's al_sbr_ps_03 and 04 are in
// TestAACConformancePS.

// TestPSMixGainsB (P7): for all 46 x 8 index pairs the output covariance of procedure B
// equals the target (powers c_L^2 and c_R^2, correlation rho' = max(rho, 0.05)) when s
// and d are uncorrelated and of equal power, the gains match the reference's numerical
// eigendecomposition at 192 bits, and a negative ICC acts as 0.05.
func TestPSMixGainsB(t *testing.T) {
	var worstCov, worstRef float64
	for fine, tab := range [2][][8]psGain{psMixBCoarse, psMixBFine} {
		ratio, db := psIIDGainCoarse, psIIDdBCoarse[:]
		if fine == 1 {
			ratio, db = psIIDGainFine, psIIDdBFine[:]
		}
		for i := range tab {
			cR := math.Sqrt2 / math.Sqrt(1+ratio[i]*ratio[i])
			cL := ratio[i] * cR
			for icc := range tab[i] {
				g := tab[i][icc]
				rho := math.Max(psICC[icc], psRhoFloor)
				cov := [3]float64{g[psGLs]*g[psGLs] + g[psGLd]*g[psGLd], g[psGRs]*g[psGRs] + g[psGRd]*g[psGRd], g[psGLs]*g[psGRs] + g[psGLd]*g[psGRd]}
				want := [3]float64{cL * cL, cR * cR, cL * cR * rho}
				for j := range cov {
					worstCov = math.Max(worstCov, math.Abs(cov[j]-want[j]))
				}
				if sbrEmulated() {
					continue
				}
				r := psRefGainsB(int(db[i]), icc)
				for j := range g {
					f, _ := r[j].Float64()
					worstRef = math.Max(worstRef, math.Abs(g[j]-f))
				}
			}
		}
	}
	t.Logf("largest covariance error %.3g; largest gain difference to the reference %.3g", worstCov, worstRef)
	if worstCov > 1e-12 {
		t.Errorf("the output covariance differs from the target by %g", worstCov)
	}
	if worstRef > 1e-14 {
		t.Errorf("the gains differ from the reference's eigendecomposition by %g", worstRef)
	}
	for _, fine := range []bool{false, true} {
		if psMixGainB(0, 6, fine) != psMixGainB(0, 7, fine) {
			t.Errorf("fine=%v: ICC index 6 (rho < 0) and 7 (rho = -1) must both act as 0.05", fine)
		}
	}
	// ICC index 0 (rho = 1): no decorrelated signal, and the gains of procedure A
	for i, a := range psMixCoarse {
		b := psMixBCoarse[i][0]
		for j := range b {
			if math.Abs(a[0][j]-b[j]) > 1e-15 {
				t.Errorf("IID index %d, ICC 0: procedure A gain %d is %v, procedure B's %v", i-7, j, a[0][j], b[j])
			}
		}
	}
}

// TestPSSmoothPhase: the smoothing of section 6.2 from its definition: a history of angle
// 0, the weights 0.25, 0.5, 1, the unit phasor of the sum's argument.
func TestPSSmoothPhase(t *testing.T) {
	h := [2]complex128{1, 1}
	want := func(prev2, prev1 complex128, k int8) complex128 {
		sum := 0.25*prev2 + 0.5*prev1 + cmplx.Rect(1, float64(k)*math.Pi/4)
		return cmplx.Rect(1, cmplx.Phase(sum))
	}
	seq := []int8{2, 6, 6, 3, 0, 5}
	var p2, p1 complex128 = 1, 1
	for i, k := range seq {
		got := psSmoothPhase(&h, k)
		if e := cmplx.Abs(got - want(p2, p1, k)); e > 1e-15 {
			t.Errorf("envelope %d (index %d): %v, want %v", i, k, got, want(p2, p1, k))
		}
		p2, p1 = p1, cmplx.Rect(1, float64(k)*math.Pi/4)
	}
	// a constant phase is reached once the history holds it
	h = [2]complex128{1, 1}
	var got complex128
	for range 3 {
		got = psSmoothPhase(&h, 2)
	}
	if cmplx.Abs(got-complex(0, 1)) > 1e-15 {
		t.Errorf("constant phase pi/2 gives %v after three envelopes", got)
	}
}

// TestPSRemapPhase: the phase band maps of section 6.2.
func TestPSRemapPhase(t *testing.T) {
	src := []int8{1, 2, 3, 4, 5, 6, 7, 1, 2, 3, 4, 5, 6, 7, 1, 2, 3}
	var dst11 [11]int8
	var dst17 [17]int8
	// 5 -> 11: the 10 -> 20 map, band 10 is 0
	psRemapPhase(dst11[:], src, 5, 20)
	for j := 0; j < 10; j++ {
		if dst11[j] != src[j/2] {
			t.Errorf("5 -> 11: band %d is %d, want %d", j, dst11[j], src[j/2])
		}
	}
	if dst11[10] != 0 {
		t.Errorf("5 -> 11: band 10 is %d, want 0", dst11[10])
	}
	// 5 -> 17: the 10 -> 34 map for bands 0 to 15, band 16 is 0
	psRemapPhase(dst17[:], src, 5, 34)
	for j, s := range []int{0, 0, 0, 1, 1, 1, 2, 2, 2, 2, 3, 3, 4, 4, 4, 4} {
		if dst17[j] != src[s] {
			t.Errorf("5 -> 17: band %d is %d, want %d", j, dst17[j], src[s])
		}
	}
	if dst17[16] != 0 {
		t.Errorf("5 -> 17: band 16 is %d, want 0", dst17[16])
	}
	// 11 -> 17: the 20 -> 34 map, whose band 1 averages source bands 0 and 1
	psRemapPhase(dst17[:], src, 11, 34)
	if dst17[1] != (src[0]+src[1])/2 || dst17[16] != src[10] || dst17[3] != src[2] {
		t.Errorf("11 -> 17: %v", dst17)
	}
	// 17 -> 11: the first 11 targets of 34 -> 20, with its 1/3 - 2/3 weights
	psRemapPhase(dst11[:], src, 17, 20)
	if dst11[0] != (2*src[0]+src[1])/3 || dst11[10] != src[16] || dst11[9] != (src[14]+src[15])/2 {
		t.Errorf("17 -> 11: %v", dst11)
	}
	// the same count: a copy; a count no mode gives: zeros
	psRemapPhase(dst11[:], src, 11, 20)
	if dst11[10] != src[10] || dst11[0] != src[0] {
		t.Errorf("11 -> 11: %v", dst11)
	}
	psRemapPhase(dst11[:], src, 0, 20)
	if dst11 != [11]int8{} {
		t.Errorf("0 bands give %v", dst11)
	}
}

// TestPSPhaseBandsAreAllPass: every row of a phase band (the first 11 parameter bands at 20
// bands, 17 at 34) is an all-pass row, so IPD/OPD never reach the long and short delay
// rows, and the mixer needs a phased path for the all-pass rows only.
func TestPSPhaseBandsAreAllPass(t *testing.T) {
	for i, dl := range psDecLayouts {
		nPh := psPhaseBandsOf(i == 1)
		for k := dl.nAP; k < dl.rows; k++ {
			if int(dl.band[k]) < nPh {
				t.Errorf("layout %d: delay row %d is in phase band %d", i, k, dl.band[k])
			}
		}
	}
}

// TestPSImageRows: the rows of negative centre frequency, which take conjugate phases.
func TestPSImageRows(t *testing.T) {
	for i, want := range [2][]int{{0, 1}, {9, 10, 11}} {
		var got []int
		dl := psDecLayouts[i]
		for k := 0; k < dl.rows; k++ {
			if dl.image[k] {
				got = append(got, k)
			}
		}
		if len(got) != len(want) {
			t.Errorf("layout %d: image rows %v, want %v", i, got, want)
			continue
		}
		for j := range got {
			if got[j] != want[j] {
				t.Errorf("layout %d: image rows %v, want %v", i, got, want)
			}
		}
	}
}

// TestPSPhaseApplication: a mono signal of constant rows, IID 0 and ICC index 0 (so the
// decorrelated signal has no weight), constant IPD and OPD for as many envelopes as the
// smoothing needs: the left channel is rotated by the OPD and the right one by the OPD
// minus the IPD, and a row of negative centre frequency takes the conjugate rotation.
func TestPSPhaseApplication(t *testing.T) {
	const ipdIdx, opdIdx = 2, 1 // pi/2 and pi/4
	for _, is34 := range []bool{false, true} {
		mode := 1
		if is34 {
			mode = 2
		}
		dl := psDecLayouts[b2idx(is34)]
		m := new(psMixer)
		var h *psHybrid
		for frame := 0; frame < 4; frame++ {
			h = new(psHybrid)
			for n := range h.rows {
				for k := 0; k < dl.rows; k++ {
					h.rows[n][k] = 1
				}
			}
			p := psWithPhases(psTestState(is34, mode, mode, 1, psFixedBorders[1], func(e, b int) int { return 0 }, func(e, b int) int { return 0 }),
				func(e, b int) int { return ipdIdx }, func(e, b int) int { return opdIdx })
			p.numEnvOld = 1
			if frame == 0 {
				p.numEnvOld = 0
			}
			m.apply(h, p, 64)
		}
		nPh := psPhaseBandsOf(is34)
		checked := 0
		for k := 0; k < dl.rows; k++ {
			if int(dl.band[k]) >= nPh {
				continue
			}
			checked++
			l, r := h.rows[31][k], m.rr[31][k]
			sign := 1.0
			if dl.image[k] {
				sign = -1
			}
			wantL := cmplx.Rect(1, sign*opdIdx*math.Pi/4)
			wantR := cmplx.Rect(1, sign*(opdIdx-ipdIdx)*math.Pi/4)
			if cmplx.Abs(l-wantL) > 1e-12 || cmplx.Abs(r-wantR) > 1e-12 {
				t.Errorf("34=%v row %d (band %d, image %v): left %v (want %v), right %v (want %v)", is34, k, dl.band[k], dl.image[k], l, wantL, r, wantR)
			}
		}
		if checked < 10 {
			t.Errorf("34=%v: only %d rows checked", is34, checked)
		}
		// a row above the phase bands keeps real gains
		for k := 0; k < dl.rows; k++ {
			if int(dl.band[k]) >= nPh {
				if imag(h.rows[31][k]) != 0 || imag(m.rr[31][k]) != 0 {
					t.Errorf("34=%v row %d (band %d) has phases", is34, k, dl.band[k])
				}
			}
		}
	}
}

// TestPSPhaseInterpolation: the real and imaginary parts of the gains are interpolated
// linearly between the envelopes, per gain (section 4.3 with 6.2): with ICC index 0 the
// left gain is e^{j opd} times a real gain, so over an envelope it goes from the
// matrix at its start to the one at its end along the chord, not along the arc.
func TestPSPhaseInterpolation(t *testing.T) {
	m := new(psMixer)
	h := new(psHybrid)
	for n := range h.rows {
		for k := 0; k < 71; k++ {
			h.rows[n][k] = 1
		}
	}
	// the first frame: opd index 0 (angle 0 history, smoothing gives 0) for the single envelope
	zero := func(e, b int) int { return 0 }
	p := psWithPhases(psTestState(false, 1, 1, 1, psFixedBorders[1], zero, zero), zero, zero)
	m.apply(h, p, 64)
	// the second frame: opd index 4 (pi) for the envelope: smoothed phase arg(0.25 + 0.5 + e^{j pi}) = pi
	h = new(psHybrid)
	for n := range h.rows {
		for k := 0; k < 71; k++ {
			h.rows[n][k] = 1
		}
	}
	four := func(e, b int) int { return 4 }
	p2 := psWithPhases(psTestState(false, 1, 1, 1, psFixedBorders[1], zero, zero), zero, four)
	p2.numEnvOld = 1
	m.apply(h, p2, 64)
	// the left gain of a phase band goes from 1 (end of the first frame) to -1 along the
	// real axis: halfway (slot 15 of 32 slots, border -1 to 31) it is 1 - 2*16/32 = 0
	k := 12
	l := h.rows[15][k]
	if want := complex(1-2*16.0/32, 0); cmplx.Abs(l-want) > 1e-12 {
		t.Errorf("row %d slot 15: left %v, want %v", k, l, want)
	}
	if l := h.rows[31][k]; cmplx.Abs(l-(-1)) > 1e-12 {
		t.Errorf("row %d slot 31: left %v, want -1", k, l)
	}
}
