// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "testing"

// TestPSRowsBelow: the QMF band of a row never decreases, so the rows below an SBR top
// are the leading ones, and rowsBelow counts them for every top from 0 to 64.
func TestPSRowsBelow(t *testing.T) {
	for i, dl := range psDecLayouts {
		for k := 1; k < dl.rows; k++ {
			if dl.qmf[k] < dl.qmf[k-1] {
				t.Fatalf("layout %d: row %d is QMF band %d after %d", i, k, dl.qmf[k], dl.qmf[k-1])
			}
		}
		for top := 0; top <= 64; top++ {
			want := 0
			for k := 0; k < dl.rows; k++ {
				if int(dl.qmf[k]) < top {
					want++
				}
			}
			if got := dl.rowsBelow(top); got != want {
				t.Errorf("layout %d top %d: %d rows below, want %d", i, top, got, want)
			}
		}
	}
}
