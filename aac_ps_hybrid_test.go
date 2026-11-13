// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"math/big"
	"math/cmplx"
	"math/rand"
	"path/filepath"
	"sort"
	"testing"
)

// The tests of the PS hybrid bank follow docs/aac-ps-hybrid.md, section 5: T1 identity,
// T2 frequency placement, T3 an independent reference in math/big. The reference below
// is written from the definition (eq. 2.1) and from the row tables 3 and 4 typed here,
// and shares with the decoder only the 26 normative taps and the two parameter maps.

type psQFrame = [sbrQMFSlots][sbrSynBands]complex128

// psRefRow is a row of the reference: outputs outs of the M-output modulation of QMF band
// band, summed. The two-band real rows are the M = 4 modulation with prototype psProtoTwo
// turned into g4 (g4[t] = g2[t] / (2 cos(pi t / 4)) at odd t, g4[0] = g2[0] / 2).
type psRefRow struct {
	band, m, proto int
	outs           []int
}

// psRefRows20 is table 3 of the specification, rows 0..9 in order.
var psRefRows20 = []psRefRow{
	{0, 8, psProtoM8, []int{6}}, {0, 8, psProtoM8, []int{7}}, {0, 8, psProtoM8, []int{0}},
	{0, 8, psProtoM8, []int{1}}, {0, 8, psProtoM8, []int{2, 5}}, {0, 8, psProtoM8, []int{3, 4}},
	{1, 4, psProtoTwo, []int{1, 2}}, {1, 4, psProtoTwo, []int{0, 3}},
	{2, 4, psProtoTwo, []int{0, 3}}, {2, 4, psProtoTwo, []int{1, 2}},
}

// psRefRows34 is table 4: by QMF band, then by output.
func psRefRows34() (r []psRefRow) {
	for band, mp := range [][2]int{{12, psProtoM12}, {8, psProtoM8b}, {4, psProtoM4}, {4, psProtoM4}, {4, psProtoM4}} {
		for m := 0; m < mp[0]; m++ {
			r = append(r, psRefRow{band, mp[0], mp[1], []int{m}})
		}
	}
	return r
}

func psRefRowsOf(is34 bool) []psRefRow {
	if is34 {
		return psRefRows34()
	}
	return psRefRows20
}

const psRefPrec = 192

func bigAtanInv(n int64) *big.Float {
	x := new(big.Float).SetPrec(psRefPrec).Quo(big.NewFloat(1).SetPrec(psRefPrec), new(big.Float).SetPrec(psRefPrec).SetInt64(n))
	x2 := new(big.Float).SetPrec(psRefPrec).Mul(x, x)
	sum := new(big.Float).SetPrec(psRefPrec)
	pow := new(big.Float).SetPrec(psRefPrec).Set(x)
	for k := 0; k < 90; k++ {
		term := new(big.Float).SetPrec(psRefPrec).Quo(pow, new(big.Float).SetPrec(psRefPrec).SetInt64(int64(2*k+1)))
		if k%2 == 0 {
			sum.Add(sum, term)
		} else {
			sum.Sub(sum, term)
		}
		pow.Mul(pow, x2)
	}
	return sum
}

// bigPi is Machin's pi = 16 atan(1/5) - 4 atan(1/239).
func bigPi() *big.Float {
	a := new(big.Float).SetPrec(psRefPrec).Mul(big.NewFloat(16), bigAtanInv(5))
	b := new(big.Float).SetPrec(psRefPrec).Mul(big.NewFloat(4), bigAtanInv(239))
	return a.Sub(a, b)
}

// bigSinCos is the Taylor series of sin and cos.
func bigSinCos(x *big.Float) (s, c *big.Float) {
	s, c = new(big.Float).SetPrec(psRefPrec), new(big.Float).SetPrec(psRefPrec)
	term := new(big.Float).SetPrec(psRefPrec).SetInt64(1)
	for k := 0; k < 120; k++ {
		switch k % 4 {
		case 0:
			c.Add(c, term)
		case 1:
			s.Add(s, term)
		case 2:
			c.Sub(c, term)
		case 3:
			s.Sub(s, term)
		}
		term.Mul(term, x)
		term.Quo(term, new(big.Float).SetPrec(psRefPrec).SetInt64(int64(k+1)))
	}
	return s, c
}

// psRef evaluates eq. 2.1 as a direct 13-term sum at 192 bits.
type psRef struct {
	pi   *big.Float
	trig map[[2]int][2]*big.Float // e^{-j pi p / M} as (cos, sin) of pi p / M
	taps map[[2]int]*big.Float    // prototype, tap |t|
}

func newPSRef() *psRef {
	return &psRef{pi: bigPi(), trig: map[[2]int][2]*big.Float{}, taps: map[[2]int]*big.Float{}}
}

func (r *psRef) cossin(p, m int) (c, s *big.Float) {
	key := [2]int{p, m}
	if v, ok := r.trig[key]; ok {
		return v[0], v[1]
	}
	x := new(big.Float).SetPrec(psRefPrec).Mul(r.pi, new(big.Float).SetPrec(psRefPrec).SetInt64(int64(p)))
	x.Quo(x, new(big.Float).SetPrec(psRefPrec).SetInt64(int64(m)))
	s, c = bigSinCos(x)
	r.trig[key] = [2]*big.Float{c, s}
	return c, s
}

// tap is g[t] of the prototype the row uses, t = 0..6. The centre tap and the zero taps
// are written out here from the specification (1/M, and the multiples of M).
func (r *psRef) tap(row psRefRow, t int) *big.Float {
	key := [2]int{row.proto, t}
	if v, ok := r.taps[key]; ok {
		return v
	}
	g := new(big.Float).SetPrec(psRefPrec)
	switch {
	case row.proto == psProtoTwo:
		// g4 for the pair-summed M = 4 bank; the even taps drop out of the sums
		switch {
		case t == 0:
			g.SetFloat64(0.25)
		case t%2 == 1:
			c, _ := r.cossin(t, 4)
			den := new(big.Float).SetPrec(psRefPrec).Mul(big.NewFloat(2), c)
			g.Quo(new(big.Float).SetPrec(psRefPrec).SetFloat64(psTaps[psProtoTwo][t]), den)
		}
	case t == 0:
		g.Quo(big.NewFloat(1), new(big.Float).SetPrec(psRefPrec).SetInt64(int64(row.m)))
	case t%row.m == 0:
	default:
		g.SetFloat64(psTaps[row.proto][t])
	}
	r.taps[key] = g
	return g
}

// sumTaps is the sum over members and taps of |g|: the scale of the error bound.
func (r *psRef) sumTaps(row psRefRow) float64 {
	var s float64
	for range row.outs {
		for t := -psLook; t <= psLook; t++ {
			g, _ := r.tap(row, max(t, -t)).Float64()
			s += math.Abs(g)
		}
	}
	return s
}

// at is the row's output at slot n of the signal get(band, slot), as big floats.
func (r *psRef) at(row psRefRow, get func(k, n int) complex128, n int) (re, im *big.Float) {
	re, im = new(big.Float).SetPrec(psRefPrec), new(big.Float).SetPrec(psRefPrec)
	for _, m := range row.outs {
		for t := -psLook; t <= psLook; t++ {
			x := get(row.band, n+t)
			if x == 0 {
				continue
			}
			p := ((2*m+1)*t%(2*row.m) + 2*row.m) % (2 * row.m)
			c, s := r.cossin(p, row.m) // e^{-j pi p/M} = c - j s
			g := r.tap(row, max(t, -t))
			gc := new(big.Float).SetPrec(psRefPrec).Mul(g, c)
			gs := new(big.Float).SetPrec(psRefPrec).Mul(g, s)
			xr := new(big.Float).SetPrec(psRefPrec).SetFloat64(real(x))
			xi := new(big.Float).SetPrec(psRefPrec).SetFloat64(imag(x))
			// (gc - j gs)(xr + j xi)
			re.Add(re, new(big.Float).SetPrec(psRefPrec).Mul(gc, xr))
			re.Add(re, new(big.Float).SetPrec(psRefPrec).Mul(gs, xi))
			im.Add(im, new(big.Float).SetPrec(psRefPrec).Mul(gc, xi))
			im.Sub(im, new(big.Float).SetPrec(psRefPrec).Mul(gs, xr))
		}
	}
	return re, im
}

// psDiff is |dec - ref| for a decoder value and a big reference.
func psDiff(dec complex128, re, im *big.Float) float64 {
	dr := new(big.Float).SetPrec(psRefPrec).SetFloat64(real(dec))
	di := new(big.Float).SetPrec(psRefPrec).SetFloat64(imag(dec))
	a, _ := dr.Sub(dr, re).Float64()
	b, _ := di.Sub(di, im).Float64()
	return math.Hypot(a, b)
}

// psRandFrames makes n frames of random complex samples in [-1, 1).
func psRandFrames(n int, seed int64) []psQFrame {
	rng := rand.New(rand.NewSource(seed))
	f := make([]psQFrame, n)
	for i := range f {
		for l := range f[i] {
			for k := range f[i][l] {
				f[i][l][k] = complex(2*rng.Float64()-1, 2*rng.Float64()-1)
			}
		}
	}
	return f
}

// psStream runs frames through the hybrid analysis (layout is34[i] for frame i, the
// look-ahead the head of the next frame as the SBR path gives it) and returns each
// frame's rows and the synthesis of them.
func psStream(frames []psQFrame, is34 []bool) (rows [][sbrQMFSlots][psMaxRows]complex128, back []psQFrame) {
	h := new(psHybrid)
	for i := range frames {
		var look [psLook][psSplitMax]complex128
		if i+1 < len(frames) {
			for j := range look {
				for k := range look[j] {
					look[j][k] = frames[i+1][j][k]
				}
			}
		}
		x := frames[i]
		h.analyse(&x, &look, is34[i])
		rows = append(rows, h.rows)
		h.synthesise(&x, is34[i])
		back = append(back, x)
	}
	return rows, back
}

var psLayoutSequences = [][]bool{
	{false, false, false, false},
	{true, true, true, true},
	{false, true, false, true}, // switches: the history of QMF 0..4 carries over
}

// TestPSHybridTaps checks the prototypes against the digits of table 2.3 of the
// specification: the 26 normative taps are those digits, the centre and zero taps are
// computed from 1/M and the multiples of M and agree with the printed ones.
func TestPSHybridTaps(t *testing.T) {
	spec := [psNProto][psLook + 1]float64{
		psProtoM8:  {0.125, 0.11793710567217, 0.09885108575264, 0.07266113929591, 0.04546865930473, 0.02270420949825, 0.00746082949812},
		psProtoTwo: {0.5, 0.30596630545168, 0, -0.07293139167538, 0, 0.01899487526049, 0},
		psProtoM12: {0.08333333333333, 0.08100347892914, 0.07428313801106, 0.06399831151592, 0.05144908135699, 0.03812810994926, 0.04081179924692},
		psProtoM8b: {0.125, 0.12222452249753, 0.10307344158036, 0.08417044116767, 0.05417891378782, 0.03752716391991, 0.01565675600122},
		psProtoM4:  {0.25, 0.23279856662996, 0.16486303567403, 0.07778723915851, 0, -0.04871498374946, -0.05908211155639},
	}
	normative := 0
	for p := range spec {
		g := psPrototype(p)
		for tt, want := range spec[p] {
			if math.Abs(g[tt]-want) > 1e-14 {
				t.Errorf("prototype %d tap %d = %.14f, want %.14f", p, tt, g[tt], want)
			}
			if tt > 0 && tt%psProtoM[p] != 0 {
				normative++
				if g[tt] != want {
					t.Errorf("prototype %d tap %d: normative value differs from the table", p, tt)
				}
			}
		}
	}
	if normative != 26 {
		t.Errorf("%d normative taps, want 26", normative)
	}
}

// TestPSPrototypeResponse checks the frequency response G(w) = g[0] + 2 sum g[t] cos(wt)
// of each prototype on a grid of 4001 points in [0, pi]. The values were MEASURED first
// (2026-11-13) and are asserted with a margin: the stop band |w| >= 2pi/M stays under
// 1.25 times the measured maximum, G(0) within 0.002 of its measured value, and the
// two-band filter's edges G2(0) = 1.00406 and G2(pi) = -0.00406 within 0.001, with
// G2(pi/2) = 1/2 exactly (derived). This catches a sign, a transposition or a shift of
// the taps between columns. It does NOT catch a change in a last digit: that is
// TestPSHybridTaps's job.
func TestPSPrototypeResponse(t *testing.T) {
	cases := [psNProto]struct{ g0, pi, stop float64 }{ // measured G(0), G(pi), stop-band maximum
		psProtoM8:  {0.85517, 0.00196, 0.06598},
		psProtoTwo: {1.00406, -0.00406, 0.00406},
		psProtoM12: {0.78268, 0.05016, 0.09847},
		psProtoM8b: {0.95866, -0.01703, 0.01746},
		psProtoM4:  {0.98530, -0.06218, 0.07343},
	}
	for p, c := range cases {
		g := psPrototype(p)
		G := func(w float64) float64 {
			s := g[0]
			for k := 1; k <= psLook; k++ {
				s += 2 * g[k] * math.Cos(w*float64(k))
			}
			return s
		}
		tol := 0.002
		if p == psProtoTwo {
			tol = 0.001
		}
		if math.Abs(G(0)-c.g0) > tol || math.Abs(G(math.Pi)-c.pi) > tol {
			t.Errorf("prototype %d: G(0) = %.5f, G(pi) = %.5f, want %.5f, %.5f", p, G(0), G(math.Pi), c.g0, c.pi)
		}
		var worst float64
		for i := 0; i <= 4000; i++ {
			if w := math.Pi * float64(i) / 4000; w >= 2*math.Pi/float64(psProtoM[p]) {
				worst = max(worst, math.Abs(G(w)))
			}
		}
		if worst > 1.25*c.stop {
			t.Errorf("prototype %d: stop band maximum %.5f over %.5f", p, worst, 1.25*c.stop)
		}
		if p == psProtoTwo && math.Abs(G(math.Pi/2)-0.5) > 1e-12 {
			t.Errorf("two-band G(pi/2) = %v, want 1/2", G(math.Pi/2))
		}
	}
}

func TestPSTrig(t *testing.T) {
	for _, m := range []int{4, 8, 12} {
		for p := -3 * m; p < 3*m; p++ {
			c, s := psTrig(p, m)
			q := (p%(2*m) + 2*m) % (2 * m) // the reference reduces first: Cos of a large argument loses digits
			wc, ws := math.Cos(math.Pi*float64(q)/float64(m)), math.Sin(math.Pi*float64(q)/float64(m))
			if math.Abs(c-wc) > 2e-15 || math.Abs(s-ws) > 2e-15 { // the reference's own argument is rounded
				t.Errorf("psTrig(%d, %d) = %v, %v, want %v, %v", p, m, c, s, wc, ws)
			}
		}
		for p := 1; p < m; p++ {
			_, a := psTrig(p, m)
			if _, b := psTrig(2*m-p, m); a != -b {
				t.Errorf("psTrig(%d, %d) and its opposite are not exact negatives", p, m)
			}
		}
	}
}

// The split rows of the 34-band layout as docs/aac-ps-hybrid.md table 4 states them, by
// QMF band 0..4 and output: the centre frequency in 1/24 band units and the parameter
// band. They are the expected values of TestPSHybridLayouts.
var (
	psTable4F24 = [][]int{{2, 6, 10, 14, 18, 22, 26, 30, 34, -10, -6, -2}, {51, 57, 15, 21, 27, 33, 39, 45}, {54, 66, 78, 42}, {102, 66, 78, 90}, {102, 114, 126, 90}}
	psTable4PB  = [][]int{{0, 1, 2, 3, 4, 5, 6, 6, 7, 2, 1, 0}, {10, 10, 4, 5, 6, 7, 8, 9}, {10, 11, 12, 9}, {14, 11, 12, 13}, {14, 15, 16, 13}}
)

// TestPSHybridLayouts checks the row order, the centre frequencies and the parameter
// band of every row against tables 3 and 4 of the specification and the property that
// the parameter band never decreases as |f| grows.
func TestPSHybridLayouts(t *testing.T) {
	type row struct {
		f  float64
		pb int
	}
	r20 := []row{{-3. / 8, 1}, {-1. / 8, 0}, {1. / 8, 0}, {3. / 8, 1}, {5. / 8, 2}, {7. / 8, 3}, {5. / 4, 4}, {7. / 4, 5}, {9. / 4, 6}, {11. / 4, 7}}
	unsplit := func(k0 int, ranges [][2]int) (r []row) { // ranges: last QMF band, parameter band
		pb := 0
		for k := k0; k < 64; k++ {
			for len(ranges) > 0 && k > ranges[0][0] {
				ranges = ranges[1:]
			}
			pb = ranges[0][1]
			r = append(r, row{float64(k) + 0.5, pb})
		}
		return r
	}
	r20 = append(r20, unsplit(3, [][2]int{{3, 8}, {4, 9}, {5, 10}, {6, 11}, {7, 12}, {8, 13}, {10, 14}, {13, 15}, {17, 16}, {22, 17}, {34, 18}, {63, 19}})...)
	var r34 []row
	for b := range psTable4F24 {
		for i, v := range psTable4F24[b] {
			r34 = append(r34, row{float64(v) / 24, psTable4PB[b][i]})
		}
	}
	r34 = append(r34, unsplit(5, [][2]int{{5, 16}, {6, 17}, {7, 18}, {8, 19}, {9, 20}, {10, 21}, {12, 22}, {14, 23}, {16, 24}, {18, 25}, {20, 26}, {23, 27}, {26, 28}, {29, 29}, {32, 30}, {36, 31}, {40, 32}, {63, 33}})...)
	for _, c := range []struct {
		name string
		ly   *psLayout
		want []row
		maps []uint8
	}{{"20 bands", psLayoutOf(false), r20, aacPSBandOf20[:]}, {"34 bands", psLayoutOf(true), r34, aacPSBandOf34[:]}} {
		if c.ly.rows() != len(c.want) || len(c.maps) != len(c.want) {
			t.Fatalf("%s: %d rows, %d in the map, want %d", c.name, c.ly.rows(), len(c.maps), len(c.want))
		}
		for r, w := range c.want {
			if f := c.ly.centre(r); math.Abs(f-w.f) > 1e-12 {
				t.Errorf("%s row %d: centre %v, want %v", c.name, r, f, w.f)
			}
			if int(c.maps[r]) != w.pb {
				t.Errorf("%s row %d: parameter band %d, want %d", c.name, r, c.maps[r], w.pb)
			}
		}
		order := make([]int, len(c.want))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(i, j int) bool {
			return math.Abs(c.ly.centre(order[i])) < math.Abs(c.ly.centre(order[j]))-1e-12
		})
		for i := 1; i < len(order); i++ {
			if c.maps[order[i]] < c.maps[order[i-1]] {
				t.Errorf("%s: parameter band falls from row %d to row %d as |f| grows", c.name, order[i-1], order[i])
			}
		}
	}
	// the 20-band rows are in increasing order of signed centre frequency
	for r := 1; r < 71; r++ {
		if psLayoutOf(false).centre(r) <= psLayoutOf(false).centre(r-1) {
			t.Errorf("20 bands: row %d not above row %d", r, r-1)
		}
	}
}

// TestPSHybridIdentity (T1): analysis followed by synthesis returns the input, at the
// frame edges too, in both layouts and across switches. The bound per band is 4 * 2^-53
// times the sum of the absolute coefficients of its rows times the peak.
func TestPSHybridIdentity(t *testing.T) {
	frames := psRandFrames(4, 1)
	for _, is34 := range psLayoutSequences {
		_, back := psStream(frames, is34)
		var worst float64
		for i := range frames {
			ly := psLayoutOf(is34[i])
			for l := range frames[i] {
				for k := range frames[i][l] {
					bound := 0.0
					if k < ly.nSplit {
						for _, f := range ly.filt[ly.start[k]:ly.start[k+1]] {
							bound += math.Abs(f.c0)
							for tt := 1; tt <= psLook; tt++ {
								bound += 2 * (math.Abs(f.c[tt]) + math.Abs(f.s[tt]))
							}
						}
					}
					bound = 4 * 0x1p-53 * (bound + 1) * math.Sqrt2
					e := cmplx.Abs(back[i][l][k] - frames[i][l][k])
					worst = max(worst, e)
					if e > bound {
						t.Fatalf("layouts %v frame %d slot %d band %d: error %g over the bound %g", is34, i, l, k, e, bound)
					}
				}
			}
		}
		t.Logf("layouts %v: worst error %.3g", is34, worst)
	}
}

// TestPSHybridReference (T3): every row of every frame against the 192-bit evaluation
// of eq. 2.1, to 2^-48 times the sum of |g| times the peak of the input, per sample.
func TestPSHybridReference(t *testing.T) {
	frames := psRandFrames(3, 2)
	ref := newPSRef()
	get := func(k, n int) complex128 {
		if n < 0 || n >= len(frames)*sbrQMFSlots {
			return 0
		}
		return frames[n/sbrQMFSlots][n%sbrQMFSlots][k]
	}
	peak := 0.0
	for i := range frames {
		for l := range frames[i] {
			for _, v := range frames[i][l] {
				peak = max(peak, cmplx.Abs(v))
			}
		}
	}
	for _, is34 := range psLayoutSequences {
		rows, _ := psStream(frames, is34)
		var worst float64
		for i := range frames {
			rr := psRefRowsOf(is34[i])
			for l := 0; l < sbrQMFSlots; l++ {
				for r, row := range rr {
					re, im := ref.at(row, get, i*sbrQMFSlots+l)
					d := psDiff(rows[i][l][r], re, im)
					bound := 0x1p-48 * ref.sumTaps(row) * peak
					worst = max(worst, d/bound)
					if d > bound {
						t.Fatalf("layouts %v frame %d slot %d row %d: |dec - ref| = %g over %g", is34, i, l, r, d, bound)
					}
				}
			}
		}
		t.Logf("layouts %v: worst error %.3g of its bound", is34, worst)
	}
}

// TestPSHybridPlacement (T2a): a complex exponential at omega = pi f_r in the QMF band of
// row r comes out of row r with the gain the row's filter has at omega (computed here as
// the sum over its outputs of G(omega - omega_m)), to 1e-12, and with a smaller gain from
// every other row of the band.
func TestPSHybridPlacement(t *testing.T) {
	for _, is34 := range []bool{false, true} {
		ly := psLayoutOf(is34)
		rr := psRefRowsOf(is34)
		for r, row := range rr {
			omega := math.Pi * ly.centre(r)
			tone := func(n int) complex128 { return cmplx.Rect(1, omega*float64(n)) }
			frames := make([]psQFrame, 3)
			for i := range frames {
				for l := range frames[i] {
					frames[i][l][row.band] = tone(i*sbrQMFSlots + l)
				}
			}
			rows, _ := psStream(frames, []bool{is34, is34, is34})
			var want complex128
			for _, m := range row.outs {
				for tt := -psLook; tt <= psLook; tt++ {
					g := psTapOf(row, tt)
					wm := math.Pi * float64((2*m+1)*tt) / float64(row.m)
					want += complex(g, 0) * cmplx.Rect(1, omega*float64(tt)-wm)
				}
			}
			for l := 0; l < sbrQMFSlots; l++ {
				got := rows[1][l][r]
				exp := want * tone(sbrQMFSlots+l)
				if cmplx.Abs(got-exp) > 1e-12 {
					t.Fatalf("is34 %v row %d slot %d: %v, want %v", is34, r, l, got, exp)
				}
				for o := ly.start[row.band]; o < ly.start[row.band+1]; o++ {
					if o != r && cmplx.Abs(rows[1][l][o]) >= cmplx.Abs(got) {
						t.Fatalf("is34 %v row %d slot %d: row %d has gain %v, not below %v", is34, r, l, o, cmplx.Abs(rows[1][l][o]), cmplx.Abs(got))
					}
				}
			}
		}
	}
}

// psTapOf is the reference tap in float64 (for the placement test).
func psTapOf(row psRefRow, t int) float64 {
	g, _ := newPSRef().tap(row, max(t, -t)).Float64()
	return g
}

// TestPSHybridTones (T2b): a real sinusoid at the centre frequency of a row (in QMF band
// units, at the core rate pi f / 32 rad per sample) fed through the analysis bank puts
// most of its energy in that row, for every row whose centre lies in its own band
// interval [k, k+1).
func TestPSHybridTones(t *testing.T) {
	tested := 0
	defer func() {
		if tested < 8+16 { // the rows whose centre is in their own band interval: 8 at 20 bands, 16 at 34
			t.Errorf("only %d tones tested", tested)
		}
	}()
	for _, is34 := range []bool{false, true} {
		ly := psLayoutOf(is34)
		for r := 0; r < ly.start[ly.nSplit]; r++ {
			f := ly.centre(r)
			if f <= 0 || f < float64(ly.filt[r].band) || f >= float64(ly.filt[r].band+1) {
				continue
			}
			tested++
			d, h := new(sbrChanDSP), new(psHybrid)
			pcm := make([]float32, aacFrameLen)
			for fr := 0; fr < 4; fr++ {
				for i := range pcm {
					pcm[i] = float32(0.5 * math.Cos(math.Pi*f*float64(fr*aacFrameLen+i)/32))
				}
				d.analyse(pcm)
				d.assemble(&d.x, sbrAnaBands, 0, 0)
				var look [psLook][psSplitMax]complex128
				d.psLookAhead(sbrAnaBands, &look)
				h.analyse(&d.x, &look, is34)
			}
			var e [psMaxRows]float64
			for l := range h.rows {
				for i, v := range h.rows[l][:ly.rows()] {
					e[i] += real(v)*real(v) + imag(v)*imag(v)
				}
			}
			best := 0
			for i := range ly.rows() {
				if e[i] > e[best] {
					best = i
				}
			}
			if best != r {
				t.Errorf("is34 %v: a tone at f = %v (row %d) peaks in row %d (centre %v)", is34, f, r, best, ly.centre(best))
			}
		}
	}
}

// psChain runs n frames of noise through the decoder's analysis, assembly and the hybrid
// round trip with start frequency kx, and returns the 64-band input of each frame as the
// hybrid bank saw it, its rows, and what the round trip wrote back.
func psChain(n, kx int) (in, back []psQFrame, rows [][sbrQMFSlots][psMaxRows]complex128) {
	d := new(sbrChanDSP)
	s := &sbrState{}
	rng := rand.New(rand.NewSource(3))
	pcm := make([]float32, aacFrameLen)
	for fr := 0; fr < n; fr++ {
		for i := range pcm {
			pcm[i] = float32(rng.Float64() - 0.5)
		}
		d.analyse(pcm)
		d.assemble(&d.x, kx, 0, 0)
		in = append(in, d.x)
		s.psRoundTrip(d, kx)
		back = append(back, d.x)
		rows = append(rows, s.hyb.rows)
	}
	return in, back, rows
}

// TestPSHybridWiring: the look-ahead the SBR path hands to the bank is the next frame's
// first slots. Against the 192-bit reference on the continuous sequence of the frames'
// inputs (so independent of the wiring), every row of the frames that have a successor
// agrees to the T3 bound.
func TestPSHybridWiring(t *testing.T) {
	in, _, rows := psChain(5, sbrAnaBands)
	get := func(k, n int) complex128 {
		if n < 0 || n >= len(in)*sbrQMFSlots {
			return 0
		}
		return in[n/sbrQMFSlots][n%sbrQMFSlots][k]
	}
	ref := newPSRef()
	peak := 0.0
	for i := range in {
		for l := range in[i] {
			for _, v := range in[i][l][:psSplitMax] {
				peak = max(peak, cmplx.Abs(v))
			}
		}
	}
	for i := 0; i < len(in)-1; i++ {
		for l := 0; l < sbrQMFSlots; l++ {
			for r, row := range psRefRows20 {
				re, im := ref.at(row, get, i*sbrQMFSlots+l)
				if d, bound := psDiff(rows[i][l][r], re, im), 0x1p-48*ref.sumTaps(row)*peak; d > bound {
					t.Fatalf("frame %d slot %d row %d: |dec - ref| = %g over %g", i, l, r, d, bound)
				}
			}
		}
	}
}

// TestPSLookAhead: bands at or above kx get no look-ahead, the others the low band's last
// analysis slots; a round trip with a start frequency inside the split bands still
// returns its input.
func TestPSLookAhead(t *testing.T) {
	in, back, _ := psChain(3, 2)
	d := new(sbrChanDSP)
	for k := range d.low {
		for i := range d.low[k] {
			d.low[k][i] = complex(float64(k), float64(i))
		}
	}
	var look [psLook][psSplitMax]complex128
	d.psLookAhead(2, &look)
	for j := range look {
		for k := range look[j] {
			want := complex(float64(k), float64(sbrQMFSlots+sbrHFAdj+j))
			if k >= 2 {
				want = 0
			}
			if look[j][k] != want {
				t.Errorf("look[%d][%d] = %v, want %v", j, k, look[j][k], want)
			}
		}
	}
	peak := 0.0
	for i := range in {
		for l := range in[i] {
			for _, v := range in[i][l] {
				peak = max(peak, cmplx.Abs(v))
			}
		}
	}
	for i := range in {
		for l := range in[i] {
			for k := range in[i][l] {
				if cmplx.Abs(in[i][l][k]-back[i][l][k]) > 1e-14*peak { // the error is relative to the window's peak, not the sample
					t.Fatalf("frame %d slot %d band %d: %v, want %v", i, l, k, back[i][l][k], in[i][l][k])
				}
			}
		}
	}
}

// psFixturePCM decodes the first frames of he44-ps.aac to the output of the SBR path,
// with or without the hybrid round trip.
func psFixturePCM(t *testing.T, hybrid bool, frames int) (out []float32, s *sbrState) {
	t.Helper()
	cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", "he44-ps.aac"))
	p, err := newAACParser(cfg, DefaultMaxAudioChannels)
	if err != nil {
		t.Fatal(err)
	}
	p.enableSBR()
	synth := newAACSynth(cfg.rateIndex, cfg.channels)
	for fi := 0; fi < min(frames, len(fr)); fi++ {
		els, err := p.parseFrame(fr[fi])
		if err != nil {
			t.Fatal(err)
		}
		var pcm [2][aacFrameLen]float32
		synth.element(&els[0], 0, [2][]float32{pcm[0][:], pcm[1][:]})
		if els[0].sbr != nil {
			s = els[0].sbr
			s.psHybridTest = hybrid
		}
		if s == nil {
			continue
		}
		got := [2][]float32{make([]float32, sbrQMFSlots*sbrSynBands), nil}
		s.frame(1, [2][]float32{pcm[0][:], pcm[1][:]}, got)
		out = append(out, got[0]...)
	}
	return out, s
}

// TestPSHybridKeepsOutput: PS is not applied yet, so the output of a real PS stream with
// the hybrid round trip in the path is that of the path without it, to the float32
// rounding of an identity that holds to 1e-15.
func TestPSHybridKeepsOutput(t *testing.T) {
	n := sbrRefFrames(120, 30)
	plain, _ := psFixturePCM(t, false, n)
	hyb, s := psFixturePCM(t, true, n)
	if s == nil || s.hyb == nil || len(plain) == 0 || len(plain) != len(hyb) {
		t.Fatalf("the round trip did not run: %d and %d samples", len(plain), len(hyb))
	}
	var worst float64
	for i := range plain {
		worst = max(worst, math.Abs(float64(plain[i])-float64(hyb[i])))
	}
	if worst > 1e-7 {
		t.Errorf("the round trip moved the output by %g", worst)
	}
	t.Logf("%d samples, largest difference %g", len(plain), worst)
}

// TestPSHybridDoesNotAllocate: a frame's analysis and synthesis allocate nothing.
func TestPSHybridDoesNotAllocate(t *testing.T) {
	frames := psRandFrames(1, 4)
	h := new(psHybrid)
	var look [psLook][psSplitMax]complex128
	for _, is34 := range []bool{false, true} {
		if a := testing.AllocsPerRun(20, func() {
			h.analyse(&frames[0], &look, is34)
			h.synthesise(&frames[0], is34)
		}); a != 0 {
			t.Errorf("is34 %v: %v allocations per frame", is34, a)
		}
	}
}

// BenchmarkPSHybrid is the analysis and the synthesis of one frame (32 slots).
func BenchmarkPSHybrid(b *testing.B) {
	for _, c := range []struct {
		name string
		is34 bool
	}{{"20bands", false}, {"34bands", true}} {
		b.Run(c.name, func(b *testing.B) {
			frames := psRandFrames(1, 5)
			h := new(psHybrid)
			var look [psLook][psSplitMax]complex128
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h.analyse(&frames[0], &look, c.is34)
				h.synthesise(&frames[0], c.is34)
			}
		})
	}
}
