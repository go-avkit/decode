// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"testing"
)

func TestPSIIDValues(t *testing.T) {
	for name, tc := range map[string]struct {
		db   []int8
		gain []float64
		max  int
	}{
		"coarse": {psIIDdBCoarse[:], psIIDGainCoarse, 25},
		"fine":   {psIIDdBFine[:], psIIDGainFine, 50},
	} {
		n := len(tc.db)
		if len(tc.gain) != n || tc.db[n/2] != 0 || int(tc.db[0]) != -tc.max || int(tc.db[n-1]) != tc.max {
			t.Fatalf("%s: %d steps from %d to %d", name, n, tc.db[0], tc.db[n-1])
		}
		for i := range tc.db {
			if i > 0 && tc.db[i] <= tc.db[i-1] || tc.db[i] != -tc.db[n-1-i] {
				t.Errorf("%s: step %d is %d dB after %d, mirror %d", name, i, tc.db[i], tc.db[max(i-1, 0)], tc.db[n-1-i])
			}
			// the gain is the amplitude ratio of that many dB, and the two ends of a mirror
			// pair are inverses
			if got := 20 * math.Log10(tc.gain[i]); math.Abs(got-float64(tc.db[i])) > 1e-12 {
				t.Errorf("%s: step %d: gain %v is %v dB, want %d", name, i, tc.gain[i], got, tc.db[i])
			}
			if p := tc.gain[i] * tc.gain[n-1-i]; math.Abs(p-1) > 1e-14 {
				t.Errorf("%s: step %d: gains %v and %v multiply to %v", name, i, tc.gain[i], tc.gain[n-1-i], p)
			}
		}
		if tc.gain[n/2] != 1 {
			t.Errorf("%s: 0 dB is %v", name, tc.gain[n/2])
		}
	}
}

func TestPSICCValues(t *testing.T) {
	for i, c := range psICC {
		if i > 0 && c >= psICC[i-1] {
			t.Errorf("ICC %d is %v after %v", i, c, psICC[i-1])
		}
		// the angle is the one whose double has that cosine, in the first two quadrants
		if a := psICCAngle[i]; a < 0 || a > math.Pi/2 || math.Abs(math.Cos(2*a)-c) > 1e-15 {
			t.Errorf("ICC %d = %v: angle %v has cos(2a) = %v", i, c, a, math.Cos(2*psICCAngle[i]))
		}
	}
	if psICCAngle[0] != 0 || psICCAngle[5] != math.Pi/4 || psICCAngle[7] != math.Pi/2 {
		t.Errorf("angles of ICC 1, 0 and -1: %v %v %v", psICCAngle[0], psICCAngle[5], psICCAngle[7])
	}
}

func TestPSPhase(t *testing.T) {
	for k, a := range psPhase {
		if math.Abs(math.Cos(a)-math.Cos(-a)) > 0 || math.Abs(math.Sin(a)*math.Sin(a)+math.Cos(a)*math.Cos(a)-1) > 1e-15 {
			t.Errorf("phase %d = %v", k, a)
		}
		if k > 0 && math.Abs(a-psPhase[k-1]-math.Pi/4) > 1e-15 {
			t.Errorf("phase %d is %v after %v", k, a, psPhase[k-1])
		}
	}
	if psPhase[0] != 0 || psPhase[4] != math.Pi {
		t.Errorf("phases 0 and 4: %v %v", psPhase[0], psPhase[4])
	}
}

// TestPSDecays: the decorrelator's decays are normative values, not computed. They are
// held to 14 digits and agree with exp(-d/7) and exp(-4/15) to that: a consistency
// check on the digits, not where they come from.
func TestPSDecays(t *testing.T) {
	for i, d := range []float64{3, 4, 5} {
		if e := math.Exp(-d / 7); math.Abs(psLinkDecay[i]-e) > 1e-13 {
			t.Errorf("link %d: %v, exp(-%v/7) is %v", i, psLinkDecay[i], d, e)
		}
	}
	if e := math.Exp(-4.0 / 15); math.Abs(psPeakDecay-e) > 1e-13 {
		t.Errorf("peak decay %v, exp(-4/15) is %v", psPeakDecay, e)
	}
}

// TestPSBandMaps: each hybrid row maps to a parameter band; every band of the layout is
// fed by at least one row, and the rows of the unsplit QMF bands never go back.
func TestPSBandMaps(t *testing.T) {
	for name, tc := range map[string]struct {
		m      []uint8
		rows   int
		bands  int
		sorted int // the first row from which the map never decreases
	}{
		"20 bands": {aacPSBandOf20[:], 71, 20, 3},
		"34 bands": {aacPSBandOf34[:], 91, 34, 32}, // the rows of the split QMF bands 0 to 4 are interleaved by their frequency
	} {
		if len(tc.m) != tc.rows {
			t.Fatalf("%s: %d rows, want %d", name, len(tc.m), tc.rows)
		}
		used := make([]bool, tc.bands)
		for k, b := range tc.m {
			if int(b) >= tc.bands {
				t.Fatalf("%s: row %d maps to band %d", name, k, b)
			}
			used[b] = true
			if k > tc.sorted && b < tc.m[k-1] {
				t.Errorf("%s: row %d maps to band %d after %d", name, k, b, tc.m[k-1])
			}
		}
		for b, u := range used {
			if !u {
				t.Errorf("%s: band %d has no row", name, b)
			}
		}
	}
}
