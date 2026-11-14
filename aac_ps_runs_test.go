// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "testing"

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

// TestPSTransientRowOrder: the run-based sums give exactly the power sums of the row by
// row loop, in both layouts.
func TestPSTransientRowOrder(t *testing.T) {
	x := uint32(5)
	next := func() float64 {
		x = x*1664525 + 1013904223
		return float64(int32(x)) / float64(1<<31)
	}
	for i, dl := range psDecLayouts {
		var rows [sbrQMFSlots][psMaxRows]complex128
		for n := range rows {
			for k := range rows[n][:dl.rows] {
				rows[n][k] = complex(next(), next())
			}
		}
		var m psMixer
		m.transient(&rows, dl)
		// the row by row reference, with the same smoothing
		var peak, avg, dev [psMaxBands]float64
		for n := range rows {
			var pw [psMaxBands]float64
			for k, v := range rows[n][:dl.rows] {
				pw[dl.band[k]] += real(v)*real(v) + imag(v)*imag(v)
			}
			for b := 0; b < dl.nb; b++ {
				pk := max(psPeakDecay*peak[b], pw[b])
				peak[b] = pk
				avg[b] += psSmooth * (pw[b] - avg[b])
				dev[b] += psSmooth * (pk - pw[b] - dev[b])
				g := 1.0
				if psImpact*dev[b] > avg[b] {
					g = avg[b] / (psImpact * dev[b])
				}
				if m.tg[n][b] != g {
					t.Fatalf("layout %d slot %d band %d: gain %v, row by row %v", i, n, b, m.tg[n][b], g)
				}
			}
		}
	}
}
