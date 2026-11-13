// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "testing"

// A writer of SBR payloads for the tests: the inverse of the parser, written from
// the same syntax, used to build streams that real encoders do not produce (every
// frame class, every delta-coding case, every error).

type sbrBW struct {
	b []byte
	n int
}

func (w *sbrBW) put(n int, v int) {
	for i := n - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>uint(i)&1 != 0 {
			w.b[w.n/8] |= 0x80 >> uint(w.n%8)
		}
		w.n++
	}
}

// sbrTestCode returns the canonical code and length of the entry of table c that
// stands for delta.
func sbrTestCode(t testing.TB, c, delta int) (code, n int) {
	t.Helper()
	lens := aacSBRHuffLens[c]
	cur := 0
	for i := range lens {
		if i > 0 {
			cur = (cur + 1) << (lens[i] - lens[i-1])
		}
		if int(aacSBRHuffSyms[c][i])+sbrHuffOffset[c] == delta {
			return cur, int(lens[i])
		}
	}
	t.Fatalf("table %d has no code for %d", c, delta)
	return 0, 0
}

func (w *sbrBW) huff(t testing.TB, c, delta int) {
	code, n := sbrTestCode(t, c, delta)
	w.put(n, code)
}

type sbrHdr struct {
	amp, start, stop, xover int
	extra1                  bool
	freqScale, alter, nb    int
	extra2                  bool
	limBands, limGains, ip  int
	smooth                  int
}

// defaultSBRHdr is a valid header at an SBR rate of 44100 Hz.
func defaultSBRHdr() sbrHdr { return sbrHdr{start: 7, stop: 9} }

func (h sbrHdr) write(w *sbrBW) {
	w.put(1, h.amp)
	w.put(4, h.start)
	w.put(4, h.stop)
	w.put(3, h.xover)
	w.put(2, 0)
	w.put(1, b2i(h.extra1))
	w.put(1, b2i(h.extra2))
	if h.extra1 {
		w.put(2, h.freqScale)
		w.put(1, h.alter)
		w.put(2, h.nb)
	}
	if h.extra2 {
		w.put(2, h.limBands)
		w.put(2, h.limGains)
		w.put(1, h.ip)
		w.put(1, h.smooth)
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// sbrGrid is a written sbr_grid: its bits, and what the envelope data that
// follows needs to know of it.
type sbrGrid struct {
	bits    func(w *sbrBW)
	numEnv  int
	freqRes []int // bs_freq_res of envelopes 1 to numEnv
	amp0    bool  // bs_amp_res is forced to 0
}

func gridFixFix(logN, res int) sbrGrid {
	n := 1 << uint(logN)
	g := sbrGrid{numEnv: n, amp0: n == 1}
	for i := 0; i < n; i++ {
		g.freqRes = append(g.freqRes, res)
	}
	g.bits = func(w *sbrBW) { w.put(2, sbrFixFix); w.put(2, logN); w.put(1, res) }
	return g
}

func gridFixVar(trail int, rels []int, ptr int, res []int) sbrGrid {
	n := len(rels) + 1
	g := sbrGrid{numEnv: n, freqRes: res}
	g.bits = func(w *sbrBW) {
		w.put(2, sbrFixVar)
		w.put(2, trail)
		w.put(2, len(rels))
		for _, r := range rels {
			w.put(2, r)
		}
		w.put(int(sbrCeilLog2[min(n, 5)]), ptr)
		for i := n - 1; i >= 0; i-- { // freq_res of envelope n first
			w.put(1, res[i])
		}
	}
	return g
}

func gridVarFix(t0 int, rels []int, ptr int, res []int) sbrGrid {
	n := len(rels) + 1
	g := sbrGrid{numEnv: n, freqRes: res}
	g.bits = func(w *sbrBW) {
		w.put(2, sbrVarFix)
		w.put(2, t0)
		w.put(2, len(rels))
		for _, r := range rels {
			w.put(2, r)
		}
		w.put(int(sbrCeilLog2[min(n, 5)]), ptr)
		for _, r := range res {
			w.put(1, r)
		}
	}
	return g
}

func gridVarVar(t0, trail int, lead, rel []int, ptr int, res []int) sbrGrid {
	n := len(lead) + len(rel) + 1
	g := sbrGrid{numEnv: n, freqRes: res}
	g.bits = func(w *sbrBW) {
		w.put(2, sbrVarVar)
		w.put(2, t0)
		w.put(2, trail)
		w.put(2, len(lead))
		w.put(2, len(rel))
		for _, r := range lead {
			w.put(2, r)
		}
		for _, r := range rel {
			w.put(2, r)
		}
		w.put(int(sbrCeilLog2[min(n, 5)]), ptr)
		for _, r := range res {
			w.put(1, r)
		}
	}
	return g
}

// sbrPayload describes one sbr_extension_data.
type sbrPayload struct {
	crc      bool
	hdr      *sbrHdr
	elem     int
	coupling bool
	extra    bool // bs_data_extra
	noData   bool // stop after the header flag and the header
	ampKept  int  // bs_amp_res of the header in force, when this payload has none
	grids    [2]sbrGrid
	dfEnv    [2][]int
	dfNoise  [2][]int
	invf     int
	// delta gives the Huffman delta written for channel ch, kind 0 envelope or 1
	// noise, envelope i, band j; nil is 0.
	delta func(ch, kind, i, j int) int
	first [2]int // first value of an envelope or noise floor coded in frequency
	harm  [2][]int
	// ext is written as bs_extended_data when not nil: its size in bytes (0 to
	// 270) and the extension id of its first 2 bits, then zero bits.
	ext *[2]int
	// extBody writes the data that follows the id of ext (a PS payload), the rest of the
	// extension being zero bits.
	extBody func(w *sbrBW)
	// bytesAfter is appended as raw bits after the data: n bits of value v.
	pad [2]int
}

// tables returns the frequency tables the header of p builds at SBR rate 2*core.
func (p sbrPayload) tables(t testing.TB, core int, hdr sbrHdr) *sbrTables {
	t.Helper()
	tb := &sbrTables{rate: 2 * core}
	sp := sbrSpectrum{uint8(hdr.start), uint8(hdr.stop), uint8(hdr.xover), 2, 1, 2}
	if hdr.extra1 {
		sp = sbrSpectrum{uint8(hdr.start), uint8(hdr.stop), uint8(hdr.xover), uint8(hdr.freqScale), uint8(hdr.alter), uint8(hdr.nb)}
	}
	lb := 2
	if hdr.extra2 {
		lb = hdr.limBands
	}
	if err := tb.makeMaster(sp); err != nil {
		t.Fatal(err)
	}
	if err := tb.makeDerived(sp, lb); err != nil {
		t.Fatal(err)
	}
	return tb
}

// build writes the payload; tb is the table set of the header in force.
func (p sbrPayload) build(t testing.TB, tb *sbrTables) (b []byte, nbits int) {
	t.Helper()
	w := &sbrBW{}
	if p.crc {
		w.put(10, 0)
	}
	w.put(1, b2i(p.hdr != nil))
	if p.hdr != nil {
		p.hdr.write(w)
	}
	amp := p.ampKept
	if p.hdr != nil {
		amp = p.hdr.amp
	}
	chs := 1
	if p.elem == aacIDCPE {
		chs = 2
	}
	delta := func(ch, kind, i, j int) int {
		if p.delta == nil {
			return 0
		}
		return p.delta(ch, kind, i, j)
	}
	env := func(ch int) {
		g := p.grids[ch]
		if p.coupling {
			g = p.grids[0]
		}
		a := amp
		if g.amp0 {
			a = 0
		}
		d := 1
		var bits, tc, fc int
		switch {
		case p.coupling && ch == 1 && a != 0:
			bits, tc, fc = 5, sbrTEnvBal30, sbrFEnvBal30
		case p.coupling && ch == 1:
			bits, tc, fc = 6, sbrTEnvBal15, sbrFEnvBal15
		case a != 0:
			bits, tc, fc = 6, sbrTEnv30, sbrFEnv30
		default:
			bits, tc, fc = 7, sbrTEnv15, sbrFEnv15
		}
		_ = d
		for i := 0; i < g.numEnv; i++ {
			n := tb.n[g.freqRes[i]]
			if i < len(p.dfEnv[ch]) && p.dfEnv[ch][i] != 0 {
				for j := 0; j < n; j++ {
					w.huff(t, tc, delta(ch, 0, i, j))
				}
				continue
			}
			w.put(bits, p.first[0])
			for j := 1; j < n; j++ {
				w.huff(t, fc, delta(ch, 0, i, j))
			}
		}
	}
	noise := func(ch int) {
		g := p.grids[ch]
		if p.coupling {
			g = p.grids[0]
		}
		nn := 1
		if g.numEnv > 1 {
			nn = 2
		}
		tc, fc := sbrTNoise30, sbrFEnv30
		if p.coupling && ch == 1 {
			tc, fc = sbrTNoiseBal30, sbrFEnvBal30
		}
		for i := 0; i < nn; i++ {
			if i < len(p.dfNoise[ch]) && p.dfNoise[ch][i] != 0 {
				for j := 0; j < tb.nQ; j++ {
					w.huff(t, tc, delta(ch, 1, i, j))
				}
				continue
			}
			w.put(5, p.first[1])
			for j := 1; j < tb.nQ; j++ {
				w.huff(t, fc, delta(ch, 1, i, j))
			}
		}
	}
	dtdf := func(ch int) {
		g := p.grids[ch]
		if p.coupling {
			g = p.grids[0]
		}
		for i := 0; i < g.numEnv; i++ {
			v := 0
			if i < len(p.dfEnv[ch]) {
				v = p.dfEnv[ch][i]
			}
			w.put(1, v)
		}
		nn := 1
		if g.numEnv > 1 {
			nn = 2
		}
		for i := 0; i < nn; i++ {
			v := 0
			if i < len(p.dfNoise[ch]) {
				v = p.dfNoise[ch][i]
			}
			w.put(1, v)
		}
	}
	invf := func() {
		for i := 0; i < tb.nQ; i++ {
			w.put(2, p.invf)
		}
	}
	harm := func(ch int) {
		if p.harm[ch] == nil {
			w.put(1, 0)
			return
		}
		w.put(1, 1)
		for j := 0; j < tb.n[1]; j++ {
			w.put(1, p.harm[ch][j%len(p.harm[ch])])
		}
	}
	if !p.noData {
		w.put(1, b2i(p.extra))
		if p.extra {
			if chs == 1 {
				w.put(4, 0)
			} else {
				w.put(8, 0)
			}
		}
		if chs == 2 {
			w.put(1, b2i(p.coupling))
		}
		p.grids[0].bits(w)
		if chs == 2 && !p.coupling {
			p.grids[1].bits(w)
		}
		dtdf(0)
		if chs == 2 {
			dtdf(1)
		}
		invf()
		if chs == 2 && !p.coupling {
			invf()
		}
		switch {
		case chs == 1:
			env(0)
			noise(0)
		case p.coupling:
			env(0)
			noise(0)
			env(1)
			noise(1)
		default:
			env(0)
			env(1)
			noise(0)
			noise(1)
		}
		harm(0)
		if chs == 2 {
			harm(1)
		}
		if p.ext != nil {
			w.put(1, 1)
			size := p.ext[0]
			if size >= 15 {
				w.put(4, 15)
				w.put(8, size-15)
			} else {
				w.put(4, size)
			}
			if size > 0 {
				w.put(2, p.ext[1])
				at := w.n
				if p.extBody != nil {
					p.extBody(w)
				}
				w.put(8*size-2-(w.n-at), 0)
			}
		} else {
			w.put(1, 0)
		}
	}
	if p.pad[0] > 0 {
		w.put(p.pad[0], p.pad[1])
	}
	return w.b, w.n
}
