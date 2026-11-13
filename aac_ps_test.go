// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"slices"
	"testing"
)

func psRow(n int, f func(b int) int) []int {
	v := make([]int, n)
	for b := range v {
		v[b] = f(b)
	}
	return v
}

// psIdx reads the indices of one envelope of a parameter back.
func psIdx(par *psParams, env, n int) []int {
	v := make([]int, n)
	for b := range v {
		v[b] = int(par[env][b])
	}
	return v
}

// psFeed writes f with enc and parses it, as data of the given length in bits (the
// bits written when n is 0, rounded up to whole bytes when n is -1).
func psFeed(t testing.TB, p *psState, enc *psEnc, f psFrame, n int) {
	t.Helper()
	w := &sbrBW{}
	bits := enc.write(t, w, f)
	switch n {
	case 0:
		n = bits
	case -1:
		n = 8 * len(w.b)
	}
	p.parse(newAACBits(w.b), n)
}

func TestPSHuffmanTables(t *testing.T) {
	wantN := [10]int{61, 61, 29, 29, 15, 15, 8, 8, 8, 8}
	unsorted := 0
	for c, n := range wantN {
		syms, lens := aacPSHuffSyms[c], aacPSHuffLens[c]
		if len(syms) != n || len(lens) != n {
			t.Fatalf("code %d: %d symbols, %d lengths, want %d", c, len(syms), len(lens), n)
		}
		seen := make([]bool, n)
		for i, s := range syms {
			if int(s) >= n || seen[s] {
				t.Fatalf("code %d: symbol %d out of range or repeated", c, s)
			}
			seen[s] = true
			if i > 0 && lens[i] < lens[i-1] {
				unsorted++
			}
		}
		// every symbol decodes back from the codeword the writer gives it
		for _, s := range syms {
			delta := int(s) + psHuffOffset[c]
			w := &sbrBW{}
			code, l := psTestCode(t, c, delta)
			w.put(l, code)
			w.put(32, 0) // the reader peeks past the code
			i, ok := psHuff[c].decode(newAACBits(w.b))
			if !ok || int(syms[i])+psHuffOffset[c] != delta {
				t.Fatalf("code %d: delta %d read back as %d", c, delta, int(syms[i])+psHuffOffset[c])
			}
		}
		// the most probable difference, 0, has the shortest codeword
		if _, l := psTestCode(t, c, 0); l != int(slices.Min(lens)) {
			t.Errorf("code %d: delta 0 has %d bits, the shortest is %d", c, l, slices.Min(lens))
		}
	}
	// the generalised builder is needed: the entries are not listed by length
	if unsorted == 0 {
		t.Error("every PS code is listed by increasing length: the listed-order builder is not exercised")
	}
}

func TestPSBuildListedHuff(t *testing.T) {
	for name, lens := range map[string][]uint8{
		"length 0":            {0, 1},
		"length 33":           {33, 1},
		"a 1-bit code at 1/4": {2, 1, 1},
		"code space full":     {1, 1, 1},
		"open branch":         {1, 2},
	} {
		if _, err := buildListedHuff(lens); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// an order that is not by length, and a canonical one, both build and decode
	h, err := buildListedHuff([]uint8{2, 2, 1})
	if err != nil {
		t.Fatal(err)
	}
	for sym, bits := range []string{"00", "01", "1"} {
		w := &sbrBW{}
		for _, c := range bits {
			w.put(1, int(c-'0'))
		}
		w.put(32, 0)
		if got, ok := h.decode(newAACBits(w.b)); !ok || got != sym {
			t.Errorf("code %q read as %d", bits, got)
		}
	}
	if _, err := buildListedHuffs([10][]uint8{{1, 1}, {2}}); err == nil {
		t.Error("an incomplete code among ten accepted")
	}
}

func TestPSParseSequence(t *testing.T) {
	p := &psState{}
	enc := newPSEnc()
	ramp := func(n, off int) []int { return psRow(n, func(b int) int { return b%5 - 2 + off }) }

	// A: header, 20 coarse IID bands, 20 ICC bands, a fixed class of two envelopes
	// that ends at the last slot; the second envelope in time.
	iidA0, iccA0 := ramp(20, 0), psRow(20, func(b int) int { return b % 8 })
	iidA1 := psRow(20, func(b int) int { return iidA0[b] + 1 })
	iccA1 := psRow(20, func(b int) int { return 7 - b%8 })
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: 1, icc: 1}, class: 0, envCode: 2,
		iid: []psEnvSpec{{v: iidA0}, {dt: true, v: iidA1}}, icc: []psEnvSpec{{v: iccA0}, {v: iccA1}}}, 0)
	if !p.start || !p.hdr || p.numEnv != 2 || p.extraEnvelop || p.is34 || p.iidQuant || p.nrIID != 20 || p.nrICC != 20 || p.nrIPDOPD != 11 || p.iidMode != 1 || p.iccMode != 1 || p.slack != 0 {
		t.Fatalf("A: %+v", p)
	}
	if p.border[0] != -1 || p.border[1] != 15 || p.border[2] != 31 {
		t.Fatalf("A borders %v", p.border)
	}
	if !slices.Equal(psIdx(&p.iid, 0, 20), iidA0) || !slices.Equal(psIdx(&p.iid, 1, 20), iidA1) || !slices.Equal(psIdx(&p.icc, 0, 20), iccA0) || !slices.Equal(psIdx(&p.icc, 1, 20), iccA1) {
		t.Fatalf("A indices: %v %v", p.iid[0][:20], p.icc[1][:20])
	}

	// B: free borders, three envelopes, the last ending short of the slot 31: a
	// fourth is made from the third. The first is coded in time against A's last.
	iidB := [][]int{psRow(20, func(b int) int { return iidA1[b] - 1 }), ramp(20, 1), psRow(20, func(b int) int { return 3 - b%7 })}
	iccB := [][]int{psRow(20, func(b int) int { return iccA1[b] }), psRow(20, func(b int) int { return b % 3 }), psRow(20, func(b int) int { return 4 })}
	psFeed(t, p, enc, psFrame{class: 1, envCode: 2, borders: []int{5, 12, 20},
		iid: []psEnvSpec{{dt: true, v: iidB[0]}, {v: iidB[1]}, {dt: true, v: iidB[2]}},
		icc: []psEnvSpec{{dt: true, v: iccB[0]}, {dt: true, v: iccB[1]}, {v: iccB[2]}}}, -1)
	if !p.start || p.hdr || p.numEnv != 4 || !p.extraEnvelop || p.numEnvOld != 2 || p.frameClass != 1 || p.slack > 7 {
		t.Fatalf("B: %+v", p)
	}
	if p.border != [6]int8{-1, 5, 12, 20, 31, 0} {
		t.Fatalf("B borders %v", p.border)
	}
	for e := 0; e < 3; e++ {
		if !slices.Equal(psIdx(&p.iid, e, 20), iidB[e]) || !slices.Equal(psIdx(&p.icc, e, 20), iccB[e]) {
			t.Fatalf("B envelope %d", e)
		}
	}
	if p.iid[3] != p.iid[2] || p.icc[3] != p.icc[2] {
		t.Fatal("B: the closing envelope is not a copy of the third")
	}

	// C: a fixed class without envelopes: the parameters of B's last hold.
	psFeed(t, p, enc, psFrame{class: 0, envCode: 0}, 0)
	if !p.start || p.numEnv != 1 || p.numEnvOld != 4 || p.border[1] != 31 || p.iid[0] != p.iid[2] || p.icc[0] != p.icc[2] || !p.extraEnvelop {
		t.Fatalf("C: %+v", p)
	}
	// ... and the frame after reads its time deltas against them
	iidC := psRow(20, func(b int) int { return iidB[2][b] + 2 })
	psFeed(t, p, enc, psFrame{class: 0, envCode: 1, iid: []psEnvSpec{{dt: true, v: iidC}}, icc: []psEnvSpec{{dt: true, v: iccB[2]}}}, 0)
	if !slices.Equal(psIdx(&p.iid, 0, 20), iidC) || p.numEnv != 1 || p.extraEnvelop {
		t.Fatalf("C2: %+v", p.iid[0])
	}

	// D: the fine IID (31 steps) at 20 bands, ICC at 20 bands, one envelope
	iidD := psRow(20, func(b int) int { return 15 - b })
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: 4, icc: 4}, class: 0, envCode: 1, iid: []psEnvSpec{{v: iidD}}, icc: psConst(20, 1, 3)}, -1)
	if !p.start || !p.iidQuant || p.nrIID != 20 || p.is34 || p.is34Old || !slices.Equal(psIdx(&p.iid, 0, 20), iidD) {
		t.Fatalf("D: %+v", p)
	}

	// E: 34 bands in both, then 10: the layout in force and the one before
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: 5, icc: 2}, class: 1, envCode: 0, borders: []int{31},
		iid: psConst(34, 1, -15), icc: psConst(34, 1, 7)}, 0)
	if !p.is34 || p.is34Old || p.nrIID != 34 || p.nrICC != 34 || p.nrIPDOPD != 17 || p.numEnv != 1 || p.extraEnvelop || p.iid[0][33] != -15 {
		t.Fatalf("E: %+v", p)
	}
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: 3, icc: 0}, class: 0, envCode: 1, iid: psConst(10, 1, 4), icc: psConst(10, 1, 1)}, 0)
	if p.is34 || !p.is34Old || p.nrIID != 10 || p.nrICC != 10 || p.nrIPDOPD != 5 {
		t.Fatalf("E2: %+v", p)
	}
	// only IID, then only ICC at 34 bands, then neither: the layout follows what is sent
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: 2, icc: -1}, class: 0, envCode: 1, iid: psConst(34, 1, 0)}, 0)
	if !p.is34 || p.enableICC || p.icc != (psParams{}) {
		t.Fatalf("E3: %+v", p)
	}
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: -1, icc: 5}, class: 0, envCode: 1, icc: psConst(34, 1, 2)}, 0)
	if !p.is34 || p.enableIID || p.iid != (psParams{}) {
		t.Fatalf("E4: %+v", p)
	}
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: -1, icc: -1}, class: 0, envCode: 1}, 0)
	if !p.is34 || p.is34Old != true || p.enableIID || p.enableICC {
		t.Fatalf("E5: nothing sent keeps the layout: %+v", p)
	}
}

func TestPSExtension(t *testing.T) {
	p := &psState{}
	enc := newPSEnc()
	// a header with the extension flag, 34 coarse IID bands: 17 IPD/OPD bands
	hdr := &psHdrSpec{iid: 2, icc: 2, ext: true}
	ipd := [][]int{psRow(17, func(b int) int { return b % 8 }), psRow(17, func(b int) int { return (3 + 2*b) % 8 })}
	opd := [][]int{psRow(17, func(b int) int { return 7 - b%8 }), psRow(17, func(b int) int { return b % 3 })}
	item := psExtItem{enable: true,
		ipd: []psEnvSpec{{v: ipd[0]}, {dt: true, v: ipd[1]}},
		opd: []psEnvSpec{{dt: true, v: opd[0]}, {v: opd[1]}}}
	f := psFrame{hdr: hdr, class: 0, envCode: 2, iid: psConst(34, 2, 1), icc: psConst(34, 2, 2), ext: []psExtItem{item}}
	psFeed(t, p, enc, f, -1)
	if !p.start || !p.enableExt || !p.enableIPDOPD || !p.ipdopdSent || p.numEnv != 2 || p.slack > 7 {
		t.Fatalf("with IPD/OPD: %+v", p)
	}
	for e := 0; e < 2; e++ {
		if !slices.Equal(psIdx(&p.ipd, e, 17), ipd[e]) || !slices.Equal(psIdx(&p.opd, e, 17), opd[e]) {
			t.Fatalf("IPD/OPD of envelope %d: %v %v", e, p.ipd[e][:17], p.opd[e][:17])
		}
	}
	// a frame whose extension holds nothing: IPD/OPD stay in force, as they were
	psFeed(t, p, enc, psFrame{class: 0, envCode: 1, iid: psConst(34, 1, 1), icc: psConst(34, 1, 2)}, 0)
	if !p.enableIPDOPD || p.ipdopdSent || !slices.Equal(psIdx(&p.ipd, 1, 17), ipd[1]) {
		t.Fatalf("a frame without IPD/OPD: %+v", p)
	}
	// ... closing an envelope repeats them too
	psFeed(t, p, enc, psFrame{class: 1, envCode: 0, borders: []int{9}, iid: psConst(34, 1, 1), icc: psConst(34, 1, 2)}, 0)
	if p.numEnv != 2 || p.ipd[1] != p.ipd[0] || p.opd[1] != p.opd[0] {
		t.Fatalf("closing with IPD/OPD: %+v", p)
	}
	// IPD/OPD time-coded against the frame before: the sum wraps at 8
	// (the rows a reader holds: the frames before sent no IPD/OPD, so they are those of
	// the first frame, repeated by the closing envelope)
	for b := 0; b < 17; b++ {
		enc.last[2][b], enc.last[3][b] = int(p.ipd[1][b]), int(p.opd[1][b])
	}
	wrapIPD, wrapOPD := psRow(17, func(b int) int { return 1 }), psRow(17, func(b int) int { return 0 })
	psFeed(t, p, enc, psFrame{class: 0, envCode: 1, iid: psConst(34, 1, 1), icc: psConst(34, 1, 2),
		ext: []psExtItem{{enable: true, ipd: []psEnvSpec{{dt: true, v: wrapIPD}}, opd: []psEnvSpec{{dt: true, v: wrapOPD}}}}}, -1)
	if p.slack > 7 || !slices.Equal(psIdx(&p.ipd, 0, 17), wrapIPD) || !slices.Equal(psIdx(&p.opd, 0, 17), wrapOPD) {
		t.Fatalf("wrapped IPD/OPD: %v %v", p.ipd[0][:17], p.opd[0][:17])
	}
	// the extension says IPD/OPD off: they are cleared
	psFeed(t, p, enc, psFrame{class: 0, envCode: 1, iid: psConst(34, 1, 1), icc: psConst(34, 1, 2), ext: []psExtItem{{enable: false}}}, -1)
	if p.enableIPDOPD || p.ipdopdSent || p.ipd != (psParams{}) || p.opd != (psParams{}) {
		t.Fatalf("IPD/OPD off: %+v", p)
	}
	// a size of 15 or more bytes is escaped (the zero bits after an unknown id are read as
	// further ids, each an empty IPD/OPD extension), and an unknown id alone is passed over
	psFeed(t, p, enc, psFrame{class: 0, envCode: 1, iid: psConst(34, 1, 1), icc: psConst(34, 1, 2),
		ext: []psExtItem{{id: 1}}, extCount: 16}, -1)
	if !p.start || p.slack > 7 || p.enableIPDOPD {
		t.Fatalf("escaped extension size: %+v", p)
	}
	psFeed(t, p, enc, psFrame{class: 0, envCode: 1, iid: psConst(34, 1, 1), icc: psConst(34, 1, 2), ext: []psExtItem{{id: 2}}}, -1)
	if !p.start || p.slack > 7 {
		t.Fatalf("escaped extension size: %+v", p)
	}
	// a header that clears the extension flag: its frames have none
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: 1, icc: 1}, class: 0, envCode: 1, iid: psConst(20, 1, 1), icc: psConst(20, 1, 2)}, 0)
	if p.enableExt || !p.start {
		t.Fatalf("no extension: %+v", p)
	}
}

// TestPSFirstFrames: a time-coded first envelope with nothing before it reads zeros,
// and a frame without a header does not start PS.
func TestPSFirstFrames(t *testing.T) {
	p := &psState{}
	enc := newPSEnc()
	psFeed(t, p, enc, psFrame{class: 0, envCode: 1}, 0)
	if p.start || p.numEnv != 1 || p.hdr {
		t.Fatalf("no header: %+v", p)
	}
	p = &psState{}
	enc = newPSEnc()
	v := psRow(20, func(b int) int { return 2 })
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: 1, icc: -1}, class: 0, envCode: 1, iid: []psEnvSpec{{dt: true, v: v}}}, 0)
	if !p.start || !slices.Equal(psIdx(&p.iid, 0, 20), v) {
		t.Fatalf("first envelope in time: %+v", p.iid[0])
	}
	// no envelope and nothing before: one envelope of zeros
	p = &psState{}
	enc = newPSEnc()
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: 1, icc: 1}, class: 0, envCode: 0}, 0)
	if !p.start || p.numEnv != 1 || p.iid != (psParams{}) {
		t.Fatalf("no envelope at all: %+v", p)
	}
	// and with one envelope before, the same row is repeated (the source is envelope 0)
	psFeed(t, p, enc, psFrame{class: 0, envCode: 0}, 0)
	if p.numEnv != 1 || p.numEnvOld != 1 {
		t.Fatalf("again: %+v", p)
	}
}

// psRaw builds the bits of a frame by hand.
func psRaw(f func(w *sbrBW)) (b []byte, n int) {
	w := &sbrBW{}
	f(w)
	return w.b, w.n
}

func TestPSErrors(t *testing.T) {
	ext := false
	hdr := func(w *sbrBW, iid, icc int) { // enable_ps_header, IID and ICC (-1 off), the extension flag in ext
		w.put(1, 1)
		w.put(1, b2i(iid >= 0))
		if iid >= 0 {
			w.put(3, iid)
		}
		w.put(1, b2i(icc >= 0))
		if icc >= 0 {
			w.put(3, icc)
		}
		w.put(1, b2i(ext))
	}
	// huff writes delta with the code of table c
	huff := func(t *testing.T, w *sbrBW, c, delta int) {
		code, n := psTestCode(t, c, delta)
		w.put(n, code)
	}
	band := func(t *testing.T, w *sbrBW, c, n int, first int) { // n bands: first, then zero steps
		huff(t, w, c, first)
		for b := 1; b < n; b++ {
			huff(t, w, c, 0)
		}
	}
	for name, build := range map[string]func(t *testing.T, w *sbrBW){
		"iid mode 6":   func(t *testing.T, w *sbrBW) { hdr(w, 6, -1) },
		"iid mode 7":   func(t *testing.T, w *sbrBW) { hdr(w, 7, -1) },
		"icc mode 6":   func(t *testing.T, w *sbrBW) { hdr(w, -1, 6) },
		"icc mode 7":   func(t *testing.T, w *sbrBW) { hdr(w, 1, 7) },
		"borders fall": func(t *testing.T, w *sbrBW) { hdr(w, -1, -1); w.put(1, 1); w.put(2, 1); w.put(5, 10); w.put(5, 5) },
		"iid high": func(t *testing.T, w *sbrBW) {
			hdr(w, 1, -1)
			w.put(3, 0b001) // class 0, 1 envelope
			w.put(1, 0)
			band(t, w, psHuffIIDCoarse, 20, 8)
		},
		"iid low": func(t *testing.T, w *sbrBW) {
			hdr(w, 1, -1)
			w.put(3, 0b001)
			w.put(1, 0)
			band(t, w, psHuffIIDCoarse, 20, -8)
		},
		"iid fine high": func(t *testing.T, w *sbrBW) {
			hdr(w, 4, -1)
			w.put(3, 0b001)
			w.put(1, 0)
			band(t, w, psHuffIIDFine, 20, 16)
		},
		"iid climbs": func(t *testing.T, w *sbrBW) { // 7 is fine, a step of one more is not
			hdr(w, 1, -1)
			w.put(3, 0b001)
			w.put(1, 0)
			huff(t, w, psHuffIIDCoarse, 7)
			huff(t, w, psHuffIIDCoarse, 1)
		},
		"icc high": func(t *testing.T, w *sbrBW) {
			hdr(w, -1, 1)
			w.put(3, 0b001)
			w.put(1, 0)
			huff(t, w, psHuffICC, 7)
			huff(t, w, psHuffICC, 1)
		},
		"icc low": func(t *testing.T, w *sbrBW) {
			hdr(w, -1, 1)
			w.put(3, 0b001)
			w.put(1, 0)
			huff(t, w, psHuffICC, -1)
		},
		"iid in time from nothing, too high": func(t *testing.T, w *sbrBW) {
			hdr(w, 1, -1)
			w.put(3, 0b001)
			w.put(1, 1)
			huff(t, w, psHuffIIDCoarse+1, 9)
		},
		"extension overflows": func(t *testing.T, w *sbrBW) {
			ext = true
			hdr(w, 1, -1)
			w.put(3, 0b001)
			w.put(1, 0)
			band(t, w, psHuffIIDCoarse, 20, 0)
			w.put(4, 1) // one byte of extension: no room for 11 bands of IPD and OPD
			w.put(2, 0) // IPD and OPD
			w.put(1, 1) // enabled
			w.put(1, 0)
			band(t, w, psHuffIPD, 11, 0)
			w.put(1, 0)
			band(t, w, psHuffOPD, 11, 0)
			w.put(1, 0)
		},
		"ipd cut": func(t *testing.T, w *sbrBW) {
			ext = true
			hdr(w, 1, -1)
			w.put(3, 0b001)
			w.put(1, 0)
			band(t, w, psHuffIIDCoarse, 20, 0)
			w.put(4, 15)
			w.put(8, 20)
			w.put(2, 0)
			w.put(1, 1)
			w.put(1, 0) // and the data ends
		},
	} {
		ext = false
		b, n := psRaw(func(w *sbrBW) { build(t, w) })
		p := &psState{}
		p.parse(newAACBits(b), n)
		if p.start || p.iid != (psParams{}) || p.icc != (psParams{}) || p.ipd != (psParams{}) || p.opd != (psParams{}) {
			t.Errorf("%s: PS on or parameters left: %+v", name, p)
		}
	}

	// a fault leaves the parameters cleared, PS off until the next header
	p := &psState{}
	enc := newPSEnc()
	good := psFrame{hdr: &psHdrSpec{iid: 4, icc: 1}, class: 0, envCode: 1, iid: psConst(20, 1, 12), icc: psConst(20, 1, 3)}
	psFeed(t, p, enc, good, 0)
	if !p.start {
		t.Fatal("a good frame did not start PS")
	}
	// the resolution drops to coarse and the frame sends no envelope: the repeated
	// index 12 is no longer valid
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: 1, icc: 1}, class: 0, envCode: 0}, 0)
	if p.start || p.iid != (psParams{}) {
		t.Fatalf("a repeated index out of range: %+v", p)
	}
	enc = newPSEnc()
	psFeed(t, p, enc, good, 0)
	if !p.start || p.iid[0][0] != 12 {
		t.Fatalf("a header starts PS again: %+v", p)
	}

	// data shorter than what it says: cut inside a code (the reader runs out), and cut by
	// one bit inside a byte (the fill of the last byte is read as data)
	w := &sbrBW{}
	n := newPSEnc().write(t, w, good)
	for _, cut := range []int{n - 1, n - 9, 3, 0} {
		p := &psState{}
		p.parse(newAACBits(w.b), cut)
		if p.start {
			t.Errorf("data cut to %d of %d bits accepted", cut, n)
		}
	}
	// a length that is not a whole number of bytes: the bits after it are not read
	p = &psState{}
	p.parse(newAACBits(w.b), n)
	if !p.start || p.slack != 0 {
		t.Errorf("exact length: %+v", p)
	}
}

// TestPSTimeDeltaAfterFault: the parameters a fault cleared are what the next time
// delta reads.
func TestPSTimeDeltaAfterFault(t *testing.T) {
	p := &psState{}
	enc := newPSEnc()
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: 1, icc: -1}, class: 0, envCode: 1, iid: psConst(20, 1, 5)}, 0)
	b, n := psRaw(func(w *sbrBW) { w.put(1, 1); w.put(3, 7) }) // reserved mode
	p.parse(newAACBits(b), n)
	if p.start {
		t.Fatal("PS on after a fault")
	}
	// the next frame has a header again; a time delta reads the cleared row
	enc = newPSEnc()
	psFeed(t, p, enc, psFrame{hdr: &psHdrSpec{iid: 1, icc: -1}, class: 0, envCode: 1, iid: []psEnvSpec{{dt: true, v: psRow(20, func(int) int { return 3 })}}}, 0)
	if !p.start || p.iid[0][0] != 3 {
		t.Fatalf("%+v", p.iid[0])
	}
}

// TestPSInSBR: the PS data rides in the SBR extension of a mono element. It is parsed
// only where the channel policy says PS may be present, a fault in it does not turn
// SBR off, and the bits after it are not its own.
func TestPSInSBR(t *testing.T) {
	tb := sbrTestTables(t)
	good := psFrame{hdr: &psHdrSpec{iid: 1, icc: 1}, class: 0, envCode: 2, iid: psConst(20, 2, 2), icc: psConst(20, 2, 1)}
	w := &sbrBW{}
	newPSEnc().write(t, w, good)
	size := (w.n + 2 + 7) / 8 // bytes of the extension: its id and the data
	run := func(psOn bool, f psFrame) *sbrState {
		s := newSBRState(sbrTestCore)
		s.psOn = psOn
		p := sbrBasic()
		p.ext = &[2]int{size, sbrExtPS}
		p.extBody = func(w *sbrBW) { newPSEnc().write(t, w, f) }
		if err := sbrRun(t, s, p, tb, true); err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := run(true, good)
	if !s.psSeen || !s.started || !s.ps.start || s.ps.numEnv != 2 || s.ps.iid[1][19] != 2 || s.ps.slack > 7 {
		t.Fatalf("PS data in SBR: %+v", s.ps)
	}
	s = run(false, good)
	if !s.psSeen || !s.started || s.ps.start || s.ps.numEnv != 0 {
		t.Fatalf("PS data where PS is not expected is skipped: %+v", s.ps)
	}
	// a fault in the PS data: SBR goes on, PS is off
	bad := psFrame{hdr: &psHdrSpec{iid: 1, icc: 1}, class: 1, envCode: 1, borders: []int{9, 3}}
	s = run(true, bad)
	if !s.started || s.ps.start {
		t.Fatalf("a PS fault turned SBR off or left PS on: started %v, PS %v", s.started, s.ps.start)
	}
}
