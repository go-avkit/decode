// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "testing"

// TestPSRowsMonotone: the QMF band of a row never decreases, so the rows at or above an SBR
// top are the trailing ones (clearAbove relies on it).
func TestPSRowsMonotone(t *testing.T) {
	for i, dl := range psDecLayouts {
		for k := 1; k < dl.rows; k++ {
			if dl.qmf[k] < dl.qmf[k-1] {
				t.Fatalf("layout %d: row %d is QMF band %d after %d", i, k, dl.qmf[k], dl.qmf[k-1])
			}
		}
	}
}
