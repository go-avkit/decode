// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// checkPSState checks what a parse must leave whatever the bits said: every index in
// its range (the ones an error cleared are zero), at most psMaxEnv envelopes, borders
// that never fall, and a frame that ends at the last slot when PS is on.
func checkPSState(t testing.TB, p *psState) {
	t.Helper()
	if p.numEnv < 0 || p.numEnv > psMaxEnv || p.numEnvOld < 0 || p.numEnvOld > psMaxEnv {
		t.Fatalf("%d envelopes after %d", p.numEnv, p.numEnvOld)
	}
	for e := range p.iid {
		for b := range p.iid[e] {
			if v := p.iid[e][b]; v < -15 || v > 15 {
				t.Fatalf("IID %d in envelope %d band %d", v, e, b)
			}
			if v := p.icc[e][b]; v < 0 || v > 7 {
				t.Fatalf("ICC %d in envelope %d band %d", v, e, b)
			}
			if v, w := p.ipd[e][b], p.opd[e][b]; v < 0 || v > 7 || w < 0 || w > 7 {
				t.Fatalf("IPD %d OPD %d in envelope %d band %d", v, w, e, b)
			}
		}
	}
	if p.start {
		if p.numEnv < 1 || p.border[0] != -1 || p.border[p.numEnv] != psSlots-1 {
			t.Fatalf("PS on with %d envelopes, borders %v", p.numEnv, p.border)
		}
		for e := 1; e <= p.numEnv; e++ {
			if p.border[e] < p.border[e-1] {
				t.Fatalf("borders %v", p.border[:p.numEnv+1])
			}
		}
	}
	if p.nrIID > psMaxBands || p.nrICC > psMaxBands || p.nrIPDOPD > psMaxBands/2 {
		t.Fatalf("bands %d %d %d", p.nrIID, p.nrICC, p.nrIPDOPD)
	}
}

// psFuzzSeeds are the frames the fuzz corpus starts from: a header with every
// resolution, free borders, the extension, and the hand-written errors.
func psFuzzSeeds(t testing.TB) map[string][]byte {
	seeds := map[string][]byte{}
	add := func(name string, f func(w *sbrBW)) {
		b, _ := psRaw(f)
		seeds[name] = append([]byte{0}, b...)
	}
	for _, c := range []struct {
		name     string
		iid, icc int
	}{{"20-coarse", 1, 1}, {"20-fine", 4, 4}, {"34-fine", 5, 2}, {"10-coarse", 0, 3}} {
		bands := psTestBands[c.iid]
		add("frame-"+c.name, func(w *sbrBW) {
			newPSEnc().write(t, w, psFrame{hdr: &psHdrSpec{iid: c.iid, icc: c.icc}, class: 1, envCode: 1, borders: []int{7, 20},
				iid: psConst(bands, 2, 1), icc: psConst(psTestBands[c.icc], 2, 2)})
		})
	}
	add("frame-ipdopd", func(w *sbrBW) {
		it := psExtItem{enable: true, ipd: psConst(17, 1, 3), opd: psConst(17, 1, 5)}
		newPSEnc().write(t, w, psFrame{hdr: &psHdrSpec{iid: 2, icc: 2, ext: true}, class: 0, envCode: 1,
			iid: psConst(34, 1, 1), icc: psConst(34, 1, 2), ext: []psExtItem{it}})
	})
	add("frame-ext-escape", func(w *sbrBW) {
		newPSEnc().write(t, w, psFrame{hdr: &psHdrSpec{iid: 1, icc: 1, ext: true}, class: 0, envCode: 1,
			iid: psConst(20, 1, 1), icc: psConst(20, 1, 2), ext: []psExtItem{{id: 1}}, extCount: 16})
	})
	for name, build := range psErrorCases() {
		add("error-"+name, func(w *sbrBW) { build(t, w) })
	}
	return seeds
}

// FuzzPSData feeds ps_data to the parser three times in a row, so that the state a frame
// leaves is read back: the first byte says how many bits (0 to 7) of the last byte are
// not data, the rest is the data. Whatever the bits say the parse stops at the end of
// the data, indices stay in their ranges, and PS is on only for a frame that ends at the
// last slot.
func FuzzPSData(f *testing.F) {
	for _, s := range psFuzzSeeds(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 1 || len(data) > 400 {
			return
		}
		nbits := max(8*(len(data)-1)-int(data[0]&7), 0)
		var p psState
		for i := 0; i < 3; i++ {
			r := newAACBits(data[1:])
			p.parse(r, nbits)
			if r.bitPos() != nbits && nbits <= 8*len(p.buf) {
				t.Fatalf("read %d bits of %d", r.bitPos(), nbits)
			}
			checkPSState(t, &p)
		}
	})
}

// TestPSWriteCorpus writes the seed corpus of FuzzPSData when PS_WRITE_CORPUS is set to
// testdata/fuzz.
func TestPSWriteCorpus(t *testing.T) {
	dir := os.Getenv("PS_WRITE_CORPUS")
	if dir == "" {
		t.Skip("PS_WRITE_CORPUS is not set")
	}
	seeds := psFuzzSeeds(t)
	// the PS data of the first frames of the real stream, cut out of the state that
	// parsed them
	fx := aacHEFixtures[3]
	_, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", fx.name))
	c, err := newAAC(aacHETrack(fx, "explicit"), AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	d0 := c.(*aacDecoder)
	for i := 0; i < 4; i++ {
		if _, err := d0.decode(fr[i]); err != nil {
			t.Fatal(err)
		}
		s := d0.parser.sbrOf(aacIDSCE, 0)
		nbits := s.extBits - 2
		seeds[fmt.Sprintf("real-%s-%d", fx.name, i)] = append([]byte{byte((8 - nbits%8) % 8)}, s.ps.buf[:(nbits+7)/8]...)
	}
	for _, target := range []string{"FuzzPSData", "FuzzPSApply"} {
		d := filepath.Join(dir, target)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, data := range seeds {
			body := fmt.Sprintf("go test fuzz v1\n[]byte(%q)\n", data)
			if err := os.WriteFile(filepath.Join(d, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestPSFuzzCorpusIsCommitted: the corpora hold the seeds.
func TestPSFuzzCorpusIsCommitted(t *testing.T) {
	for _, target := range []string{"FuzzPSData", "FuzzPSApply"} {
		entries, err := os.ReadDir(filepath.Join("testdata", "fuzz", target))
		if err != nil || len(entries) < len(psFuzzSeeds(t)) {
			t.Errorf("%s: corpus of %d entries (%v), want %d: set PS_WRITE_CORPUS=testdata/fuzz and run TestPSWriteCorpus", target, len(entries), err, len(psFuzzSeeds(t)))
		}
	}
}

// FuzzPSApply is the PS path under the test switch: the data is parsed as in FuzzPSData
// and, while PS is usable, the frame's mono rows (noise from the data, with a loud burst)
// go through the decorrelator and the mixing, three frames in a row so that the state a
// frame leaves is read back, with an SBR top and a layout that the data chooses. Whatever
// the bits say there is no panic, every output is finite, and no output exceeds a bound
// that follows from the gains (at most sqrt(2) on the mono rows, the same on the
// decorrelated ones, which the all-pass chain amplifies by at most the l1 norm 12.2).
func FuzzPSApply(f *testing.F) {
	for _, s := range psFuzzSeeds(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 2 || len(data) > 400 {
			return
		}
		nbits := max(8*(len(data)-1)-int(data[0]&7), 0)
		rng := uint64(len(data))
		for _, b := range data {
			rng = rng*6364136223846793005 + uint64(b) + 1442695040888963407
		}
		next := func() float64 {
			rng = rng*6364136223846793005 + 1442695040888963407
			return float64(int64(rng>>11))/float64(1<<52) - 1
		}
		var p psState
		m := new(psMixer)
		for fi := 0; fi < 3; fi++ {
			p.parse(newAACBits(data[1:]), nbits)
			if !p.start {
				continue
			}
			dl := psDecLayouts[0]
			if p.is34 {
				dl = psDecLayouts[1]
			}
			h := new(psHybrid)
			peak := 0.0
			for n := range h.rows {
				g := 100.0
				if n%11 == 0 {
					g = 4000
				}
				for k := 0; k < dl.rows; k++ {
					h.rows[n][k] = complex(g*next(), g*next())
					peak = math.Max(peak, math.Abs(real(h.rows[n][k]))+math.Abs(imag(h.rows[n][k])))
				}
			}
			top := int(data[len(data)-1]) % 65
			m.apply(h, &p, fi != 1, top)
			// the history of the all-pass chain adds up to 12.2 times the peak, and each
			// output is two gains of at most sqrt(2) over mono and decorrelated rows
			bound := 2 * 1.5 * 13 * math.Max(peak, 1) * float64(fi+1)
			for n := range h.rows {
				for k := 0; k < dl.rows; k++ {
					for _, v := range []complex128{h.rows[n][k], m.rr[n][k]} {
						if math.IsNaN(real(v)) || math.IsNaN(imag(v)) || math.Abs(real(v)) > bound || math.Abs(imag(v)) > bound {
							t.Fatalf("frame %d slot %d row %d: %v (bound %g)", fi, n, k, v, bound)
						}
					}
				}
			}
		}
	})
}

// TestPSParseTooLong: a length no extension can have is refused and skipped.
func TestPSParseTooLong(t *testing.T) {
	var p psState
	p.start = true
	r := newAACBits(make([]byte, 600))
	p.parse(r, 8*600)
	if p.start || r.bitPos() != 8*600 {
		t.Errorf("start %v, at bit %d", p.start, r.bitPos())
	}
}
