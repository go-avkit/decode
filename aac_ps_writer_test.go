// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "testing"

// A writer of ps_data for the tests: the inverse of the parser, written from the
// syntax (docs/aac-ps-design.md), to build frames that real encoders do not produce
// (the 34-band layouts, IPD and OPD, free borders, every error). The caller gives the
// indices each envelope must decode to; the writer turns them into deltas against what
// a reader holds (the previous band, or the last envelope of the frame before).

// psTestCode returns the codeword and length of the entry of code c that stands for
// delta: the next free slot of the code space at its length, entries in list order.
func psTestCode(t testing.TB, c, delta int) (code, n int) {
	t.Helper()
	var next uint64
	for i, l := range aacPSHuffLens[c] {
		if int(aacPSHuffSyms[c][i])+psHuffOffset[c] == delta {
			return int(next >> (32 - l)), int(l)
		}
		next += 1 << (32 - l)
	}
	t.Fatalf("code %d has no entry for %d", c, delta)
	return 0, 0
}

// psEnvSpec is one envelope of one parameter: its indices, and whether they are coded
// in time (against the envelope before) or in frequency.
type psEnvSpec struct {
	dt bool
	v  []int
}

// psExtItem is one extension of the frame's extension data: id 0 holds IPD and OPD
// (enable false sends the flag only), any other id is junk bits long.
type psExtItem struct {
	id       int
	enable   bool
	ipd, opd []psEnvSpec
	junk     int
}

// psHdrSpec is a header: the IID and ICC modes (-1 for disabled) and the extension flag.
type psHdrSpec struct {
	iid, icc int
	ext      bool
}

// psFrame is one ps_data. numEnv follows from class and envCode.
type psFrame struct {
	hdr      *psHdrSpec
	class    int
	envCode  int
	borders  []int // class 1: border of each envelope
	iid, icc []psEnvSpec
	ext      []psExtItem
	extCount int // bytes the extension claims; 0 for the size of its items
	raw      int // bits of value 1 after the data
}

// psEnc is the writer's state: the header in force and the last envelope of each
// parameter (IID, ICC, IPD, OPD) of the frame before.
type psEnc struct {
	iid, icc int
	ext      bool
	ipdOn    bool
	last     [4][psMaxBands]int
}

func newPSEnc() *psEnc { return &psEnc{iid: -1, icc: -1} }

var (
	psTestBands = [6]int{10, 20, 34, 10, 20, 34}
	psTestIPD   = [6]int{5, 11, 17, 5, 11, 17}
	psTestEnv   = [2][4]int{{0, 1, 2, 4}, {1, 2, 3, 4}}
)

// env writes one parameter's envelopes with code table code (+1 when in time), the
// bands n, par the index into last, mask true for IPD/OPD (values wrap at 8).
func (e *psEnc) env(t testing.TB, w *sbrBW, specs []psEnvSpec, code, n, par int, mask bool) {
	t.Helper()
	for i := range specs {
		e.envelope(t, w, specs, i, code, n, par, mask)
	}
}

// envelope writes envelope i of specs.
func (e *psEnc) envelope(t testing.TB, w *sbrBW, specs []psEnvSpec, i, code, n, par int, mask bool) {
	t.Helper()
	s := specs[i]
	dt := b2i(s.dt)
	w.put(1, dt)
	for b := 0; b < n; b++ {
		base := 0
		switch {
		case s.dt && i > 0:
			base = specs[i-1].v[b]
		case s.dt:
			base = e.last[par][b]
		case b > 0:
			base = s.v[b-1]
		}
		d := s.v[b] - base
		if mask {
			d &= 7
		}
		c, l := psTestCode(t, code+dt, d)
		w.put(l, c)
	}
}

// extItems writes the extension items without the count.
func (e *psEnc) extItems(t testing.TB, w *sbrBW, items []psExtItem) {
	t.Helper()
	for _, it := range items {
		w.put(2, it.id)
		if it.id != 0 {
			w.put(it.junk, 0)
			continue
		}
		w.put(1, b2i(it.enable))
		if it.enable {
			if e.iid < 0 {
				t.Fatal("IPD and OPD over a frame whose header disables IID")
			}
			for i := range it.ipd {
				e.envelope(t, w, it.ipd, i, psHuffIPD, psTestIPD[e.iid], 2, true)
				e.envelope(t, w, it.opd, i, psHuffOPD, psTestIPD[e.iid], 3, true)
			}
		}
		w.put(1, 0)
	}
}

// write writes f and updates the state; it returns the bits written.
func (e *psEnc) write(t testing.TB, w *sbrBW, f psFrame) int {
	t.Helper()
	start := w.n
	w.put(1, b2i(f.hdr != nil))
	if f.hdr != nil {
		e.iid, e.icc, e.ext = f.hdr.iid, f.hdr.icc, f.hdr.ext
		w.put(1, b2i(e.iid >= 0))
		if e.iid >= 0 {
			w.put(3, e.iid)
		}
		w.put(1, b2i(e.icc >= 0))
		if e.icc >= 0 {
			w.put(3, e.icc)
		}
		w.put(1, b2i(e.ext))
	}
	w.put(1, f.class)
	w.put(2, f.envCode)
	numEnv := psTestEnv[f.class][f.envCode]
	if f.class == 1 {
		for _, b := range f.borders {
			w.put(5, b)
		}
	}
	if e.iid >= 0 {
		code := psHuffIIDCoarse
		if e.iid > 2 {
			code = psHuffIIDFine
		}
		e.env(t, w, f.iid, code, psTestBands[e.iid], 0, false)
	}
	if e.icc >= 0 {
		e.env(t, w, f.icc, psHuffICC, psTestBands[e.icc], 1, false)
	}
	if e.ext {
		var scratch sbrBW
		e.extItems(t, &scratch, f.ext)
		count := (scratch.n + 7) / 8
		if f.extCount != 0 {
			count = f.extCount
		}
		if count >= 15 {
			w.put(4, 15)
			w.put(8, count-15)
		} else {
			w.put(4, count)
		}
		at := w.n
		e.extItems(t, w, f.ext)
		if pad := 8*count - (w.n - at); pad > 0 {
			w.put(pad, 0)
		}
	}
	w.put(f.raw, 1<<uint(f.raw)-1)
	// the last envelope of the frame, as the next frame's time deltas read it
	set := func(par int, specs []psEnvSpec) {
		if numEnv > 0 && len(specs) >= numEnv {
			copy(e.last[par][:], specs[numEnv-1].v)
		}
	}
	if e.iid >= 0 {
		set(0, f.iid)
	} else {
		e.last[0] = [psMaxBands]int{}
	}
	if e.icc >= 0 {
		set(1, f.icc)
	} else {
		e.last[1] = [psMaxBands]int{}
	}
	for _, it := range f.ext {
		if it.id == 0 {
			e.ipdOn = it.enable
			if it.enable && numEnv > 0 {
				copy(e.last[2][:], it.ipd[numEnv-1].v)
				copy(e.last[3][:], it.opd[numEnv-1].v)
			}
		}
	}
	if !e.ipdOn {
		e.last[2], e.last[3] = [psMaxBands]int{}, [psMaxBands]int{}
	}
	return w.n - start
}

// psConst makes count envelopes of n bands that all hold v, coded in frequency.
func psConst(n, count, v int) []psEnvSpec {
	var out []psEnvSpec
	for i := 0; i < count; i++ {
		vs := make([]int, n)
		for b := range vs {
			vs[b] = v
		}
		out = append(out, psEnvSpec{v: vs})
	}
	return out
}
