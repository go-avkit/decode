// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"fmt"
	"math"
	"testing"
)

// TestPSBandRuns: the runs of a layout cover its rows in order and each run is rows of
// one band, so the transient detector, which walks the runs, adds the rows in the
// same order as a row by row sum (a band may appear in several runs).
func TestPSBandRuns(t *testing.T) {
	for i, dl := range psDecLayouts {
		next := 0
		for _, r := range dl.runs {
			if int(r.start) != next || r.end <= r.start {
				t.Fatalf("layout %d: run %+v does not follow row %d", i, r, next)
			}
			for k := r.start; k < r.end; k++ {
				if dl.band[k] != r.band {
					t.Errorf("layout %d: row %d is band %d, its run says %d", i, k, dl.band[k], r.band)
				}
			}
			next = int(r.end)
		}
		if next != dl.rows {
			t.Errorf("layout %d: runs end at row %d of %d", i, next, dl.rows)
		}
	}
}

// psRowByRowPowers is the definition of the band powers: every row added to its band in
// row order, from zero.
func psRowByRowPowers(dl *psDecLayout, s *[psMaxRows]complex128) (pw [psMaxBands]float64) {
	for k, v := range s[:dl.rows] {
		pw[dl.band[k]] += real(v)*real(v) + imag(v)*imag(v)
	}
	return pw
}

// TestPSBandPowersRowOrder: the band powers equal, bit for bit, the row by row sum, in
// both layouts, for slots whose rows span many orders of magnitude, so that the order of
// the additions shows in the last bit. Mutations applied by hand: adding a run's rows in
// reverse fails layout0 (synthetic), layout1 and layout2 (the two real layouts); starting
// a run's sum from zero instead of from the band's value fails layout0 only, because in
// the real layouts every later run of a band is one row, which sums the same either way.
func TestPSBandPowersRowOrder(t *testing.T) {
	x := uint32(5)
	next := func() float64 {
		x = x*1664525 + 1013904223
		return float64(int32(x)) / float64(1<<31)
	}
	// In the two real layouts a band that is in several runs has one row in each run
	// after the first, so a sum started from zero would give the same bits there; a
	// layout with two rows in a later run shows the difference.
	synthetic := &psDecLayout{rows: 8, nb: 2, band: []uint8{0, 0, 0, 1, 0, 0, 0, 0}}
	for r := 0; r < synthetic.rows; r++ {
		if r > 0 && synthetic.band[r] == synthetic.band[r-1] {
			synthetic.runs[len(synthetic.runs)-1].end++
			continue
		}
		synthetic.runs = append(synthetic.runs, psBandRun{uint8(r), uint8(r + 1), synthetic.band[r]})
	}
	layouts := append([]*psDecLayout{synthetic}, psDecLayouts[:]...)
	for i, dl := range layouts {
		t.Run(fmt.Sprintf("layout%d", i), func(t *testing.T) {
			for slot := 0; slot < 200; slot++ {
				var s [psMaxRows]complex128
				for k := range s[:dl.rows] {
					scale := 1 + 1e6*next()*next()
					s[k] = complex(next()*scale, next()*scale)
				}
				var pw [psMaxBands]float64
				dl.bandPowers(&pw, &s)
				want := psRowByRowPowers(dl, &s)
				for b := 0; b < dl.nb; b++ {
					if math.Float64bits(pw[b]) != math.Float64bits(want[b]) {
						t.Fatalf("slot %d band %d: power %v, row by row %v", slot, b, pw[b], want[b])
					}
				}
			}
		})
	}
}

// TestPSTransientBursts: on a frame that is quiet and then bursts, the detector attenuates
// (some gains fall below 1) and its gains equal those computed from the row by row
// powers, bit for bit.
func TestPSTransientBursts(t *testing.T) {
	x := uint32(9)
	next := func() float64 {
		x = x*1664525 + 1013904223
		return float64(int32(x)) / float64(1<<31)
	}
	for i, dl := range psDecLayouts {
		var rows [sbrQMFSlots][psMaxRows]complex128
		for n := range rows {
			level := 1e-3
			if n >= 20 {
				level = 1e3
			}
			for k := range rows[n][:dl.rows] {
				rows[n][k] = complex(next()*level, next()*level)
			}
		}
		var m psMixer
		m.transient(&rows, dl)
		var peak, avg, dev [psMaxBands]float64
		attenuated := 0
		for n := range rows {
			pw := psRowByRowPowers(dl, &rows[n])
			for b := 0; b < dl.nb; b++ {
				pk := max(psPeakDecay*peak[b], pw[b])
				peak[b] = pk
				avg[b] += psSmooth * (pw[b] - avg[b])
				dev[b] += psSmooth * (pk - pw[b] - dev[b])
				g := 1.0
				if psImpact*dev[b] > avg[b] {
					g = avg[b] / (psImpact * dev[b])
				}
				if g < 1 {
					attenuated++
				}
				if math.Float64bits(m.tg[n][b]) != math.Float64bits(g) {
					t.Fatalf("layout %d slot %d band %d: gain %v, row by row %v", i, n, b, m.tg[n][b], g)
				}
			}
		}
		if attenuated == 0 {
			t.Errorf("layout %d: no gain below 1: the burst does not exercise the detector", i)
		}
	}
}
