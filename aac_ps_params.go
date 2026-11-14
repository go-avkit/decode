// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "math"

// The values the PS parameter indices stand for. Which are normative values and which
// follow from a formula is declared in .provenance.yml; docs/aac-ps-design.md has the
// table. They are used by the signal path (PS2 and PS3); the parser needs none.

// psIIDdBCoarse and psIIDdBFine are the intensity differences in dB the IID indices
// -7..7 (15 steps) and -15..15 (31 steps) stand for: normative values.
var (
	psIIDdBCoarse = [15]int8{-25, -18, -14, -10, -7, -4, -2, 0, 2, 4, 7, 10, 14, 18, 25}
	psIIDdBFine   = [31]int8{
		-50, -45, -40, -35, -30, -25, -22, -19, -16, -13, -10, -8, -6, -4, -2, 0,
		2, 4, 6, 8, 10, 13, 16, 19, 22, 25, 30, 35, 40, 45, 50,
	}
)

// psIIDGainCoarse and psIIDGainFine are the linear ratios c = 10^(IID/20) of the two
// channels' amplitudes (P1 eq. 5: the IID is the power ratio in dB, 20 log10 of the
// amplitude ratio), computed from the dB values above. Derived.
var (
	psIIDGainCoarse = psGains(psIIDdBCoarse[:])
	psIIDGainFine   = psGains(psIIDdBFine[:])
)

func psGains(db []int8) []float64 {
	g := make([]float64, len(db))
	for i, d := range db {
		g[i] = math.Pow(10, float64(d)/20)
	}
	return g
}

// psICC are the inter-channel coherences the ICC indices 0..7 stand for: normative
// values.
var psICC = [8]float64{1, 0.937, 0.84118, 0.60092, 0.36764, 0, -0.589, -1}

// psICCAngle is the rotation angle of the up-mix of each ICC index, arccos(ICC)/2
// (P1 eq. 5), computed from the values above. Derived.
var psICCAngle = func() (a [8]float64) {
	for i, c := range psICC {
		a[i] = math.Acos(c) / 2
	}
	return a
}()

// psPhase is the angle k*pi/4 the IPD and OPD indices 0..7 stand for. Derived.
var psPhase = func() (a [8]float64) {
	for k := range a {
		a[k] = float64(k) * math.Pi / 4
	}
	return a
}()

// psLinkDelay are the delays in slots of the decorrelator's three all-pass links
// (P1 §4.1: "a chain of three delay lines with the length of 3, 4, and 5 QMF samples").
// psLinkDecay are their decay factors exp(-d/7), derived (decision of the main session
// 2026-11-13: the formula is accepted as the definition, and it reproduces the three
// values the references hold, 0.65143905753106, 0.56471812200776 and 0.48954165955695,
// to 4.4e-15; TestPSLinkDecayAgreement measures that). psPeakDecay is the decay of the
// transient detector's peak, a normative value (it equals exp(-4/15) to 1.3e-15, but no
// source states that).
var (
	psLinkDelay = [3]int{3, 4, 5}
	psLinkDecay = [3]float64{math.Exp(-3.0 / 7), math.Exp(-4.0 / 7), math.Exp(-5.0 / 7)}
	psPeakDecay = 0.76592833836465
)
