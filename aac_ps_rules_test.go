// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "testing"

// TestPSEqualBorders: two envelopes may end at the same slot (spec section 4); one that
// ends before its predecessor is a fault.
func TestPSEqualBorders(t *testing.T) {
	p := &psState{}
	enc := newPSEnc()
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: 1, icc: 1}, class: 1, envCode: 1, borders: []int{5, 5},
		iid: psConst(20, 2, 1), icc: psConst(20, 2, 2)}, 0)
	if !p.start || p.numEnv != 3 || p.border != [6]int8{-1, 5, 5, 31, 0, 0} {
		t.Fatalf("equal borders: %+v", p)
	}
	enc = newPSEnc()
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: 1, icc: 1}, class: 1, envCode: 1, borders: []int{5, 4},
		iid: psConst(20, 2, 1), icc: psConst(20, 2, 2)}, 0)
	if p.start {
		t.Fatalf("falling borders accepted: %+v", p)
	}
}

// TestPSFrequencyWrap: in frequency coding the running IPD value is the wrapped one:
// deltas of 6 from 0 give 6, 4, 2, 0, 6 (modulo 8), not 6, 12, 18 ...
func TestPSFrequencyWrap(t *testing.T) {
	put := func(w *sbrBW, table, delta int) {
		code, l := psTestCode(t, table, delta)
		w.put(l, code)
	}
	item := func(w *sbrBW) int {
		bits := 0
		count := func(w *sbrBW, n, v int) { w.put(n, v); bits += n }
		count(w, 2, 0) // id: IPD/OPD
		count(w, 1, 1) // enabled
		count(w, 1, 0) // IPD, frequency-delta
		for b := 0; b < 5; b++ {
			code, l := psTestCode(t, psHuffIPD, 6)
			count(w, l, code)
		}
		count(w, 1, 0) // OPD, frequency-delta
		for b := 0; b < 5; b++ {
			code, l := psTestCode(t, psHuffOPD, 0)
			count(w, l, code)
		}
		count(w, 1, 0) // reserved
		return bits
	}
	var bits int
	probe := &sbrBW{}
	bits = item(probe)
	size := (bits + 7) / 8
	b, n := psRaw(func(w *sbrBW) {
		w.put(1, 1) // header
		w.put(1, 1) // IID on, mode 0: 10 bands, 5 IPD/OPD bands
		w.put(3, 0)
		w.put(1, 0) // ICC off
		w.put(1, 1) // extension
		w.put(1, 0) // fixed class
		w.put(2, 1) // one envelope, ending at slot 31
		w.put(1, 0) // IID frequency-delta
		for b := 0; b < 10; b++ {
			put(w, psHuffIIDCoarse, 0)
		}
		w.put(4, size)
		item(w)
		w.put(8*size-bits, 0)
	})
	p := &psState{}
	p.parse(newAACBits(b), n)
	want := [5]int8{6, 4, 2, 0, 6}
	if !p.start || p.slack != 0 || [5]int8(p.ipd[0][:5]) != want || [5]int8(p.opd[0][:5]) != [5]int8{} {
		t.Fatalf("frequency-coded IPD: %+v %v", p.start, p.ipd[0][:5])
	}
}

// TestPSNegativeLength: a negative length is a fault that reads nothing.
func TestPSNegativeLength(t *testing.T) {
	p := &psState{start: true}
	r := newAACBits([]byte{0xff})
	p.parse(r, -1)
	if p.start || r.bitPos() != 0 {
		t.Fatalf("negative length: start %v, %d bits read", p.start, r.bitPos())
	}
}
