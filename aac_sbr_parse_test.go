// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"testing"
)

const sbrTestCore = 22050 // core rate of the written streams: SBR at 44100 Hz

func sbrTestTables(t testing.TB) *sbrTables {
	t.Helper()
	return sbrPayload{}.tables(t, sbrTestCore, defaultSBRHdr())
}

// sbrRun parses p into s. With exact the payload size is the bits written, else the
// whole of the bytes.
func sbrRun(t testing.TB, s *sbrState, p sbrPayload, tb *sbrTables, exact bool) error {
	t.Helper()
	b, n := p.build(t, tb)
	if !exact {
		n = 8 * len(b)
	}
	return s.parse(newAACBits(b), p.crc, n, p.elem)
}

func sbrBasic() sbrPayload {
	h := defaultSBRHdr()
	return sbrPayload{hdr: &h, elem: aacIDSCE, grids: [2]sbrGrid{gridFixFix(1, 0), gridFixFix(1, 0)}, first: [2]int{10, 5}}
}

func mustSBR(t testing.TB, s *sbrState, p sbrPayload, tb *sbrTables) {
	t.Helper()
	if err := sbrRun(t, s, p, tb, true); err != nil {
		t.Fatal(err)
	}
}

func TestSBRSingleChannelSequence(t *testing.T) {
	tb := sbrTestTables(t)
	s := newSBRState(sbrTestCore)
	if s.started || s.t.kx[1] != 32 {
		t.Fatalf("a new state: started %v kx %d", s.started, s.t.kx[1])
	}

	// frame 1: a header, one envelope, CRC, bs_data_extra, harmonics
	p := sbrBasic()
	p.hdr.amp = 1
	p.crc, p.extra = true, true
	p.grids[0] = gridFixFix(0, 1)
	p.harm[0] = []int{1, 0}
	mustSBR(t, s, p, tb)
	c := &s.ch[0]
	if !s.started || c.numEnv != 1 || c.ampRes != 0 || c.tEnv[0] != 0 || c.tEnv[1] != 16 || c.numNoise != 1 || c.tQ[0] != 0 || c.tQ[1] != 16 || c.eA != [2]int8{-1, -1} {
		t.Fatalf("frame 1: %+v", c)
	}
	if !c.harmonic || c.addHarm[0] != 1 || c.addHarm[1] != 0 || c.addHarm[2] != 1 || c.envQ[1][0] != 10 || c.envQ[0] != c.envQ[1] || c.noiseQ[1][0] != 5 {
		t.Fatalf("frame 1 data: %+v", c)
	}
	if s.t.nQ == 0 || s.limBands != 2 || s.ampResHeader != 1 {
		t.Fatalf("header: %+v", s)
	}

	// frame 2: no header; two envelopes coded in time, high to low resolution then equal
	p = sbrBasic()
	p.hdr = nil
	p.ampKept = 1
	p.grids[0] = gridFixFix(1, 0)
	p.dfEnv[0] = []int{1, 1}
	p.dfNoise[0] = []int{1, 1}
	mustSBR(t, s, p, tb)
	if c.numEnv != 2 || c.tEnv != [6]uint8{0, 8, 16} || c.tQ != [3]uint8{0, 8, 16} || c.freqRes[0] != 0 && c.freqRes[0] != 1 {
		t.Fatalf("frame 2: %+v", c)
	}
	for e := 1; e <= 2; e++ {
		for j := 0; j < s.t.n[0]; j++ {
			if c.envQ[e][j] != 10 {
				t.Fatalf("frame 2 env %d band %d: %d", e, j, c.envQ[e][j])
			}
		}
	}

	// frame 3: low to high resolution, then equal
	p.grids[0] = gridFixFix(1, 1)
	mustSBR(t, s, p, tb)
	for j := 0; j < s.t.n[1]; j++ {
		if c.envQ[1][j] != 10 {
			t.Fatalf("frame 3 band %d: %d", j, c.envQ[1][j])
		}
	}
	// frame 4: four envelopes, the first in frequency, then high to low once more
	p.grids[0] = gridFixFix(2, 0)
	p.dfEnv[0] = []int{0, 1, 1, 1}
	mustSBR(t, s, p, tb)
	if c.numEnv != 4 || c.tEnv != [6]uint8{0, 4, 8, 12, 16} {
		t.Fatalf("frame 4: %+v", c)
	}
	// frame 5: time-coded noise after a frame of two noise floors
	p.grids[0] = gridFixFix(1, 1)
	p.dfEnv[0] = []int{1, 1}
	p.dfNoise[0] = []int{0, 1}
	mustSBR(t, s, p, tb)
}

func TestSBRFrameClasses(t *testing.T) {
	tb := sbrTestTables(t)
	type tc struct {
		name   string
		grid   sbrGrid
		numEnv int
		tEnv   []uint8
		tQ     [3]uint8
		eA     [2]int8
	}
	for _, c := range []tc{
		{"fixvar no pointer", gridFixVar(1, []int{0, 1}, 0, []int{0, 1, 0}), 3, []uint8{0, 11, 15, 17}, [3]uint8{0, 15, 17}, [2]int8{-1, -1}},
		{"fixvar pointer", gridFixVar(1, []int{0, 1}, 1, []int{0, 1, 0}), 3, []uint8{0, 11, 15, 17}, [3]uint8{0, 15, 17}, [2]int8{-1, 3}},
		{"fixvar one envelope", gridFixVar(0, nil, 0, []int{1}), 1, []uint8{0, 16}, [3]uint8{0, 16, 0}, [2]int8{-1, -1}},
		{"varfix pointer 0", gridVarFix(1, []int{0, 1}, 0, []int{0, 1, 0}), 3, []uint8{1, 3, 7, 16}, [3]uint8{1, 3, 16}, [2]int8{-1, -1}},
		{"varfix pointer 1", gridVarFix(1, []int{0, 1}, 1, []int{0, 1, 0}), 3, []uint8{1, 3, 7, 16}, [3]uint8{1, 7, 16}, [2]int8{-1, -1}},
		{"varfix pointer 2", gridVarFix(1, []int{0, 1}, 2, []int{0, 1, 0}), 3, []uint8{1, 3, 7, 16}, [3]uint8{1, 3, 16}, [2]int8{-1, 1}},
		{"varfix pointer 3", gridVarFix(1, []int{0, 1}, 3, []int{0, 1, 0}), 3, []uint8{1, 3, 7, 16}, [3]uint8{1, 7, 16}, [2]int8{-1, 2}},
		{"varvar", gridVarVar(1, 0, []int{0}, []int{1}, 2, []int{1, 0, 1}), 3, []uint8{1, 3, 12, 16}, [3]uint8{1, 12, 16}, [2]int8{-1, 2}},
	} {
		s := newSBRState(sbrTestCore)
		p := sbrBasic()
		p.grids[0] = c.grid
		mustSBR(t, s, p, tb)
		ch := &s.ch[0]
		if ch.numEnv != c.numEnv || ch.eA != c.eA || ch.tQ[0] != c.tQ[0] || ch.tQ[1] != c.tQ[1] && c.numEnv > 1 || ch.tQ[c.numEnv/c.numEnv*ch.numNoise] != c.tQ[ch.numNoise] {
			t.Errorf("%s: %+v", c.name, ch)
		}
		for i, v := range c.tEnv {
			if ch.tEnv[i] != v {
				t.Errorf("%s: borders %v, want %v", c.name, ch.tEnv[:c.numEnv+1], c.tEnv)
				break
			}
		}
	}
	// a frame after one whose last envelope was the one the pointer named: l_APrev
	s := newSBRState(sbrTestCore)
	p := sbrBasic()
	p.grids[0] = gridFixVar(1, []int{0, 1}, 1, []int{0, 1, 0}) // eA[1] = 3 = numEnv
	mustSBR(t, s, p, tb)
	p.grids[0] = gridFixFix(0, 0)
	mustSBR(t, s, p, tb)
	if s.ch[0].eA != [2]int8{0, -1} {
		t.Errorf("l_APrev %v", s.ch[0].eA)
	}
}

func TestSBRExtensions(t *testing.T) {
	tb := sbrTestTables(t)
	for _, c := range []struct {
		name string
		ext  *[2]int
		ps   bool
		bits int
	}{
		{"none", nil, false, 0},
		{"empty", &[2]int{0, 0}, false, 0},
		{"PS", &[2]int{3, sbrExtPS}, true, 24},
		{"other", &[2]int{20, 1}, false, 160},
		{"escaped size", &[2]int{15, sbrExtPS}, true, 120},
		{"one byte", &[2]int{1, 0}, false, 8},
	} {
		s := newSBRState(sbrTestCore)
		p := sbrBasic()
		p.ext = c.ext
		mustSBR(t, s, p, tb)
		if s.psSeen != c.ps || s.extBits != c.bits {
			t.Errorf("%s: PS %v, %d bits", c.name, s.psSeen, s.extBits)
		}
	}
	// a seven-bit tail after the data is skipped; a payload that ends early is
	// an error, and so is bs_extended_data larger than the payload
	s := newSBRState(sbrTestCore)
	p := sbrBasic()
	p.pad = [2]int{7, 0}
	if err := sbrRun(t, s, p, tb, false); err != nil {
		t.Fatal(err)
	}
	p.ext = &[2]int{9, 0}
	b, n := p.build(t, tb)
	if err := s.parse(newAACBits(b), false, n-16, aacIDSCE); !errors.Is(err, errSBR) || s.started {
		t.Errorf("payload shorter than its data: %v, started %v", err, s.started)
	}
}

func TestSBRChannelPair(t *testing.T) {
	tb := sbrTestTables(t)
	mk := func(amp int, coupling bool) sbrPayload {
		p := sbrBasic()
		p.hdr.amp = amp
		p.ampKept = amp
		p.elem = aacIDCPE
		p.coupling = coupling
		p.extra = true
		p.harm = [2][]int{{1}, {0, 1}}
		p.delta = func(ch, kind, i, j int) int {
			if kind == 1 {
				return 0
			}
			return (i + j + ch) % 3
		}
		return p
	}
	// two coupled frames in a row, the first ending on its last envelope (l_A =
	// numEnv), so the copy into the second channel finds l_A = numEnv
	{
		s := newSBRState(sbrTestCore)
		p := mk(0, true)
		p.grids[0] = gridFixVar(1, []int{0, 1}, 1, []int{0, 1, 0})
		mustSBR(t, s, p, tb)
		p.hdr = nil
		mustSBR(t, s, p, tb)
		if s.ch[1].eA[0] != 0 {
			t.Fatalf("l_APrev %v", s.ch[1].eA)
		}
	}
	for _, amp := range []int{0, 1} {
		// coupled: one grid, a balance channel
		s := newSBRState(sbrTestCore)
		p := mk(amp, true)
		p.grids[0] = gridFixFix(1, 0)
		p.dfEnv = [2][]int{{0, 1}, {1, 0}}
		p.dfNoise = [2][]int{{0, 1}, {1, 0}}
		mustSBR(t, s, p, tb)
		if !s.coupling || s.ch[1].numEnv != 2 || s.ch[1].tEnv != s.ch[0].tEnv || s.ch[0].invf[0] != s.ch[1].invf[0] {
			t.Fatalf("amp %d coupled: %+v", amp, s.ch[1])
		}
		// the next frame copies a grid over what the second channel held
		p.grids[0] = gridFixFix(2, 1)
		p.hdr = nil
		mustSBR(t, s, p, tb)
		p.grids[0] = gridFixFix(0, 1)
		mustSBR(t, s, p, tb)
		// uncoupled: two grids
		s = newSBRState(sbrTestCore)
		p = mk(amp, false)
		p.grids = [2]sbrGrid{gridFixFix(1, 0), gridVarFix(1, []int{0, 1}, 0, []int{0, 1, 0})}
		p.dfEnv = [2][]int{{1, 0}, {0, 1, 1}}
		mustSBR(t, s, p, tb)
		if s.coupling || s.ch[0].numEnv != 2 || s.ch[1].numEnv != 3 {
			t.Fatalf("amp %d uncoupled: %+v %+v", amp, s.ch[0].numEnv, s.ch[1].numEnv)
		}
	}
}

func TestSBRHeaders(t *testing.T) {
	tb := sbrTestTables(t)
	s := newSBRState(sbrTestCore)

	// data before any header is skipped
	p := sbrBasic()
	p.hdr = nil
	if err := sbrRun(t, s, p, tb, false); err != nil || s.started {
		t.Fatalf("no header: %v started %v", err, s.started)
	}
	// header with both extra parts
	p = sbrBasic()
	p.hdr.extra1, p.hdr.freqScale, p.hdr.alter, p.hdr.nb = true, 1, 0, 3
	p.hdr.extra2, p.hdr.limBands, p.hdr.limGains, p.hdr.ip, p.hdr.smooth = true, 3, 1, 0, 0
	tb2 := p.tables(t, sbrTestCore, *p.hdr)
	mustSBR(t, s, p, tb2)
	if s.spec.freqScale != 1 || s.spec.noiseBands != 3 || s.limBands != 3 || s.limGains != 1 || s.interpolFreq != 0 || s.smoothing != 0 {
		t.Fatalf("header: %+v", s)
	}
	// the same header again: no new tables; a change of limiter bands alone only
	// rebuilds the limiter table
	nLim := s.t.nLim
	mustSBR(t, s, p, tb2)
	p.hdr.limBands = 1
	tb3 := p.tables(t, sbrTestCore, *p.hdr)
	mustSBR(t, s, p, tb3)
	if s.limBands != 1 || s.t.nLim != tb3.nLim || nLim == 0 {
		t.Fatalf("limiter bands: nLim %d, want %d", s.t.nLim, tb3.nLim)
	}
	// a header of other frequencies rebuilds everything
	p.hdr.start, p.hdr.stop = 5, 6
	tb4 := p.tables(t, sbrTestCore, *p.hdr)
	mustSBR(t, s, p, tb4)
	if s.t.k != tb4.k {
		t.Fatalf("k %v, want %v", s.t.k, tb4.k)
	}

	// headers the tables refuse turn SBR off without an error; find both kinds
	var badMaster, badDerived *sbrHdr
	for start := 0; start < 16 && (badMaster == nil || badDerived == nil); start++ {
		for stop := 0; stop < 16; stop++ {
			for scale := 0; scale < 4; scale++ {
				h := sbrHdr{start: start, stop: stop, extra1: true, freqScale: scale, alter: 1, nb: 3}
				var x sbrTables
				x.rate = 2 * sbrTestCore
				sp := sbrSpectrum{uint8(start), uint8(stop), 0, uint8(scale), 1, 3}
				if x.makeMaster(sp) != nil {
					badMaster = &h
				} else if x.makeDerived(sp, 2) != nil {
					badDerived = &h
				}
			}
		}
	}
	if badMaster == nil || badDerived == nil {
		t.Fatalf("no refused header found: master %v derived %v", badMaster, badDerived)
	}
	for _, h := range []*sbrHdr{badMaster, badDerived} {
		p := sbrBasic()
		p.hdr = h
		p.noData = true
		if err := sbrRun(t, s, p, tb, false); err != nil || s.started || s.t.kx[1] != 32 || s.t.m[1] != 0 {
			t.Fatalf("refused header %+v: %v started %v", *h, err, s.started)
		}
	}
	// a state that was turned off takes the same header again as a new one
	mustSBR(t, s, sbrBasic(), tb)
	if !s.started {
		t.Fatal("a valid header after a refused one did not start SBR")
	}
}

func TestSBRElementTypes(t *testing.T) {
	tb := sbrTestTables(t)
	s := newSBRState(sbrTestCore)
	p := sbrBasic()
	p.elem = aacIDSCE
	b, n := p.build(t, tb)
	if err := s.parse(newAACBits(b), false, n, aacIDLFE); !errors.Is(err, errSBR) || s.started {
		t.Errorf("SBR on an LFE: %v started %v", err, s.started)
	}
}

// sbrErrorCases are payloads the parser refuses; each must also leave SBR off.
func sbrErrorCases(t *testing.T) map[string]sbrPayload {
	cases := map[string]sbrPayload{}
	add := func(name string, f func(p *sbrPayload)) {
		p := sbrBasic()
		f(&p)
		cases[name] = p
	}
	add("fixfix of 8", func(p *sbrPayload) { p.grids[0] = gridFixFix(3, 0) })
	add("varvar of 7", func(p *sbrPayload) {
		p.grids[0] = gridVarVar(0, 0, []int{0, 0, 0}, []int{0, 0, 0}, 0, make([]int, 7))
	})
	add("pointer too large", func(p *sbrPayload) {
		p.grids[0] = gridFixVar(0, []int{0, 0, 0}, 6, []int{0, 0, 0, 0})
	})
	add("borders meet", func(p *sbrPayload) { p.grids[0] = gridFixVar(0, []int{3, 3}, 0, []int{0, 0, 0}) })
	add("borders wrap", func(p *sbrPayload) { p.grids[0] = gridFixVar(0, []int{3, 3, 3}, 0, []int{0, 0, 0, 0}) })
	add("borders cross", func(p *sbrPayload) { p.grids[0] = gridVarFix(3, []int{3, 3, 3}, 0, []int{0, 0, 0, 0}) })
	add("env too high", func(p *sbrPayload) {
		p.first[0] = 100
		p.delta = func(ch, kind, i, j int) int {
			if kind == 0 {
				return 12
			}
			return 0
		}
	})
	add("env below zero", func(p *sbrPayload) {
		p.first[0] = 0
		p.delta = func(ch, kind, i, j int) int {
			if kind == 0 {
				return -1
			}
			return 0
		}
	})
	add("env time too high", func(p *sbrPayload) {
		p.grids[0] = gridFixFix(2, 0)
		p.first[0] = 100
		p.dfEnv[0] = []int{0, 1, 1, 1}
		p.delta = func(ch, kind, i, j int) int {
			if kind == 0 && i >= 1 {
				return 12
			}
			return 0
		}
	})
	add("env time below zero", func(p *sbrPayload) {
		p.grids[0] = gridFixFix(1, 0)
		p.first[0] = 0
		p.dfEnv[0] = []int{0, 1}
		p.delta = func(ch, kind, i, j int) int {
			if kind == 0 && i == 1 {
				return -1
			}
			return 0
		}
	})
	add("noise too high", func(p *sbrPayload) {
		p.first[1] = 5
		p.delta = func(ch, kind, i, j int) int {
			if kind == 1 && ch == 0 {
				return 30
			}
			return 0
		}
	})
	add("noise below zero", func(p *sbrPayload) {
		p.first[1] = 0
		p.delta = func(ch, kind, i, j int) int {
			if kind == 1 && ch == 0 {
				return -1
			}
			return 0
		}
	})
	add("noise time too high", func(p *sbrPayload) {
		p.grids[0] = gridFixFix(1, 0)
		p.first[1] = 20
		p.dfNoise[0] = []int{0, 1}
		p.delta = func(ch, kind, i, j int) int {
			if kind == 1 && ch == 0 && i == 1 {
				return 30
			}
			return 0
		}
	})
	add("noise time below zero", func(p *sbrPayload) {
		p.grids[0] = gridFixFix(1, 0)
		p.first[1] = 0
		p.dfNoise[0] = []int{0, 1}
		p.delta = func(ch, kind, i, j int) int {
			if kind == 1 && ch == 0 && i == 1 {
				return -1
			}
			return 0
		}
	})
	return cases
}

func TestSBRErrors(t *testing.T) {
	tb := sbrTestTables(t)
	for name, p := range sbrErrorCases(t) {
		for _, elem := range []int{aacIDSCE, aacIDCPE} {
			for _, coupling := range []bool{false, true} {
				if elem == aacIDSCE && coupling {
					continue
				}
				q := p
				q.elem, q.coupling = elem, coupling
				if elem == aacIDCPE {
					q.grids[1] = q.grids[0]
				}
				s := newSBRState(sbrTestCore)
				err := sbrRun(t, s, q, tb, true)
				if !errors.Is(err, errSBR) || s.started || s.ch[0].eA[1] != -1 {
					t.Errorf("%s elem %d coupling %v: %v, started %v", name, elem, coupling, err, s.started)
				}
			}
		}
	}
	// a fault in the second channel of a pair
	p := sbrErrorCases(t)["env too high"]
	p.elem = aacIDCPE
	p.grids[1] = p.grids[0]
	p.delta = func(ch, kind, i, j int) int {
		if ch == 1 && kind == 0 {
			return 40
		}
		return 0
	}
	p.first[0] = 100
	s := newSBRState(sbrTestCore)
	if err := sbrRun(t, s, p, tb, true); !errors.Is(err, errSBR) {
		t.Errorf("fault in the second channel: %v", err)
	}
}

// TestSBRTruncation parses every byte prefix of valid payloads: the parser stops
// with an error at whatever field the data ends in, and never reads beyond it.
func TestSBRTruncation(t *testing.T) {
	tb := sbrTestTables(t)
	var payloads []sbrPayload
	for _, elem := range []int{aacIDSCE, aacIDCPE} {
		for _, coupling := range []bool{false, true} {
			for _, g := range []sbrGrid{gridFixFix(1, 1), gridFixVar(1, []int{0, 1}, 1, []int{0, 1, 0}), gridVarFix(1, []int{0}, 1, []int{1, 0}), gridVarVar(1, 0, []int{0}, []int{1}, 2, []int{1, 0, 1})} {
				if elem == aacIDSCE && coupling {
					continue
				}
				p := sbrBasic()
				p.elem, p.coupling, p.crc, p.extra = elem, coupling, true, true
				p.grids = [2]sbrGrid{g, g}
				p.dfEnv = [2][]int{{0, 1, 1}, {1, 1, 0}}
				p.dfNoise = [2][]int{{0, 1}, {1, 0}}
				p.harm = [2][]int{{1}, {1}}
				p.ext = &[2]int{2, sbrExtPS}
				payloads = append(payloads, p)
			}
		}
	}
	errs := 0
	for _, p := range payloads {
		b, n := p.build(t, tb)
		if err := newSBRState(sbrTestCore).parse(newAACBits(b[:len(b)/2]), p.crc, n, p.elem); err == nil {
			t.Fatal("a payload longer than the data was accepted")
		}
		for k := 0; k < len(b); k++ {
			s := newSBRState(sbrTestCore)
			if err := s.parse(newAACBits(b[:k]), p.crc, 8*k, p.elem); err != nil {
				errs++
				if s.started {
					t.Fatalf("an error left SBR on")
				}
			}
		}
	}
	if errs < 100 {
		t.Errorf("only %d truncations refused", errs)
	}
}

func TestSBRFill(t *testing.T) {
	tb := sbrTestTables(t)
	frame := func(p sbrPayload) (*aacBits, int) {
		b, n := p.build(t, tb)
		return newAACBits(b), n
	}
	newParser := func() *aacParser {
		p := &aacParser{cfg: aacConfig{rate: sbrTestCore}}
		p.sbrOn = true
		p.elems = []aacElement{{id: aacIDSCE, tag: 1}, {id: aacIDLFE, tag: 2}}
		return p
	}
	// before the first element, and after an LFE: skipped with a reason
	for _, idx := range []int{-1, 1} {
		p := newParser()
		r, n := frame(sbrBasic())
		p.sbrFill(r, false, n, idx)
		if !errors.Is(p.sbrErr, errSBR) || r.bitPos() != n {
			t.Errorf("element %d: %v, at bit %d of %d", idx, p.sbrErr, r.bitPos(), n)
		}
	}
	// a good one attaches the state to the element, and the next frame finds it
	p := newParser()
	r, n := frame(sbrBasic())
	p.sbrFill(r, false, n, 0)
	if p.sbrErr != nil || p.elems[0].sbr == nil || len(p.sbrStates) != 1 {
		t.Fatalf("fill: %v %v", p.sbrErr, p.elems[0].sbr)
	}
	st := p.elems[0].sbr
	p.elems[0].sbr = nil
	q := sbrBasic()
	q.hdr = nil
	r, n = frame(q)
	p.sbrFill(r, false, n, 0)
	if p.elems[0].sbr != st || len(p.sbrStates) != 1 {
		t.Fatalf("the state was not reused")
	}
	// a payload with a fault: kept in sbrErr, the element has no SBR
	p.elems[0].sbr = nil
	bad := sbrErrorCases(t)["fixfix of 8"]
	bad.hdr = nil
	r, n = frame(bad)
	p.sbrFill(r, false, n, 0)
	if !errors.Is(p.sbrErr, errSBR) || p.elems[0].sbr != nil {
		t.Fatalf("faulty fill: %v", p.sbrErr)
	}
	// without a header SBR is not on: the data is read and dropped
	p = newParser()
	q = sbrBasic()
	q.hdr = nil
	r, n = frame(q)
	p.sbrFill(r, false, n, 0)
	if p.sbrErr != nil || p.elems[0].sbr != nil {
		t.Fatalf("no header: %v", p.sbrErr)
	}
	// too many elements
	p = newParser()
	for i := 0; i < aacMaxElements; i++ {
		p.sbrStates = append(p.sbrStates, sbrEntry{id: aacIDSCE, tag: uint8(100 + i)})
	}
	r, n = frame(sbrBasic())
	p.sbrFill(r, false, n, 0)
	if !errors.Is(p.sbrErr, errSBR) || len(p.sbrStates) != aacMaxElements {
		t.Fatalf("limit: %v, %d states", p.sbrErr, len(p.sbrStates))
	}
}

// TestSBRRefusedWithoutOptIn: a stream whose fill element carries SBR data is
// refused unless the parser was told it is HE-AAC.
func TestSBRRefusedWithoutOptIn(t *testing.T) {
	cfg, frames := aacADTSFrames(t, "testdata/aac/he44-st.aac")
	p, err := newAACParser(cfg, DefaultMaxAudioChannels)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.parseFrame(frames[0]); !errors.Is(err, ErrAudioUnsupported) {
		t.Errorf("SBR data without opt-in: %v", err)
	}
}

// TestSBROverrun: a payload that says more than the fill element holds (an
// extension of 9 bytes in a payload cut 2 bytes short) is an error, and the reader
// ends exactly at the end of the declared payload: the element after it, here a
// marker byte, is read from the right bit. A payload larger than a fill element
// can be is refused and skipped the same way.
func TestSBROverrun(t *testing.T) {
	tb := sbrTestTables(t)
	p := sbrBasic()
	p.ext = &[2]int{9, 0}
	b, _ := p.build(t, tb)
	declared := 8 * (len(b) - 2)
	data := append(append([]byte{}, b[:len(b)-2]...), 0xA5, 0x5A)
	s := newSBRState(sbrTestCore)
	r := newAACBits(data)
	if err := s.parse(r, false, declared, aacIDSCE); !errors.Is(err, errSBR) || s.started {
		t.Fatalf("overlong extension: %v, started %v", err, s.started)
	}
	if r.bitPos() != declared || r.read(8) != 0xA5 {
		t.Fatalf("reader at bit %d, want %d then the marker", r.bitPos(), declared)
	}
	// the same payload, honest about its size, leaves the reader on the marker
	data = append(append([]byte{}, b...), 0xA5)
	r = newAACBits(data)
	if err := s.parse(r, false, 8*len(b), aacIDSCE); err != nil || r.read(8) != 0xA5 {
		t.Fatalf("honest payload: %v", err)
	}
	// larger than any fill element
	big := make([]byte, sbrMaxPayload+2)
	big[len(big)-1] = 0xA5
	r = newAACBits(big)
	if err := s.parse(r, false, 8*(len(big)-1), aacIDSCE); !errors.Is(err, errSBR) || r.read(8) != 0xA5 {
		t.Fatalf("oversized payload: %v", err)
	}
}
