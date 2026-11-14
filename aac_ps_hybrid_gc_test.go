// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"runtime"
	"sync/atomic"
	"testing"
)

// TestPSChainUnderGC runs psChain while another goroutine collects garbage in a loop,
// so that a collection scans psChain's frame at each of its calls. On s390x, Go 1.27.4
// kept the end pointer of a by-value copy of d.x (src+size, one past the end of the
// heap object) in a stack slot across psRoundTrip, and the collector died with "found
// bad pointer in Go heap" (decode#74). psChain copies through slices now; this test
// is the witness that no such pointer is left in its frame.
func TestPSChainUnderGC(t *testing.T) {
	var stop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			runtime.GC()
		}
	}()
	for range 3 {
		in, back, rows := psChain(3, 2)
		if len(in) != 3 || len(back) != 3 || len(rows) != 3 {
			t.Fatalf("psChain returned %d, %d and %d frames, want 3 of each", len(in), len(back), len(rows))
		}
	}
	stop.Store(true)
	<-done
}
