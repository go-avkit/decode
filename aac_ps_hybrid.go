// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"sort"
)

// The Parametric Stereo hybrid filter bank (HE-AAC v2), stages 1 and 6 of
// docs/aac-ps-design.md, written from docs/aac-ps-hybrid.md alone (a clean-room
// specification: no reference implementation was opened for this file). The lowest QMF
// bands are split into several finer rows by a 13-tap filter bank (H. Purnhagen,
// DAFx'04, eq. 11):
//
//	y[k,m][l] = sum over t = -6..6 of g[|t|] exp(-j*pi*(2m+1)*t/M) x[k][l+t]
//
// with a symmetric real prototype g, advanced by 6 slots so that the unsplit bands pass
// through unchanged and undelayed (the output at slot l needs the input up to l+6). The
// synthesis sums the rows of each split band back.
//
// The symmetry folds the sum: with s[t] = x[l+t] + x[l-t] and d[t] = x[l+t] - x[l-t],
//
//	y = g[0] x[l] + sum over t = 1..6 of g[t] (cos(theta t) s[t] - j sin(theta t) d[t]),
//	theta = pi(2m+1)/M,
//
// and the outputs that a layout sums into one row (m and M-1-m, whose sines cancel)
// become one filter with the summed coefficients. A row is therefore c0, c[t], s[t].

// Sizes of the layouts.
const (
	psLook     = 6  // slots of look-ahead and of history: the filter is 13 taps wide, centred
	psSplitMax = 5  // QMF bands that the finest layout splits
	psMaxRows  = 91 // hybrid rows of the 34-band layout
)

// The five prototypes. psProtoTwo is the real two-band split of QMF bands 1 and 2 at 20
// bands; the other four are complex banks of M = 8 (20 bands, QMF 0), 12, 8 and 4 (34
// bands, QMF 0, 1 and 2..4).
const (
	psProtoM8 = iota
	psProtoTwo
	psProtoM12
	psProtoM8b
	psProtoM4
	psNProto
)

// psProtoM is the M of each prototype, which sets its centre tap 1/M and the taps that
// perfect reconstruction forces to zero: the multiples of M (the two-band split behaves
// as M = 2: its low and high halves sum to a unit impulse of 2*g[0] = 1).
var psProtoM = [psNProto]int{8, 2, 12, 8, 4}

// psTaps holds the 26 normative taps g[1..6] of the prototypes (docs/aac-ps-hybrid.md,
// table 2.3); g[0] and the zero taps are derived by psPrototype, and their entries here
// are left at 0.
var psTaps = [psNProto][psLook + 1]float64{
	psProtoM8:  {0, 0.11793710567217, 0.09885108575264, 0.07266113929591, 0.04546865930473, 0.02270420949825, 0.00746082949812},
	psProtoTwo: {0, 0.30596630545168, 0, -0.07293139167538, 0, 0.01899487526049, 0},
	psProtoM12: {0, 0.08100347892914, 0.07428313801106, 0.06399831151592, 0.05144908135699, 0.03812810994926, 0.04081179924692},
	psProtoM8b: {0, 0.12222452249753, 0.10307344158036, 0.08417044116767, 0.05417891378782, 0.03752716391991, 0.01565675600122},
	psProtoM4:  {0, 0.23279856662996, 0.16486303567403, 0.07778723915851, 0, -0.04871498374946, -0.05908211155639},
}

// psPrototype completes prototype p: the centre tap is 1/M and every other multiple of M
// is zero, which makes the sum of the modulated filters a unit impulse (the identity of
// analysis followed by synthesis, whatever the other taps are).
func psPrototype(p int) (g [psLook + 1]float64) {
	g = psTaps[p]
	m := psProtoM[p]
	g[0] = 1 / float64(m)
	for t := m; t <= psLook; t += m {
		g[t] = 0
	}
	return g
}

// psHybRow is one split row: y = c0 x[l] + sum over t of c[t] s[t] + s[t] d[t] (see above),
// from QMF band band, with centre frequency f in QMF band units.
type psHybRow struct {
	band int
	c0   float64
	c, s [psLook + 1]float64
	f    float64
}

// psLayout is a hybrid layout: the filters of the split rows, in row order, with the
// rows of QMF band k at start[k]..start[k+1]-1, then the unsplit bands in order.
type psLayout struct {
	nSplit int // QMF bands 0..nSplit-1 are split
	start  [psSplitMax + 1]int
	filt   []psHybRow
}

// rows is the number of hybrid rows: the split rows and the unsplit bands.
func (ly *psLayout) rows() int { return ly.start[ly.nSplit] + sbrSynBands - ly.nSplit }

// centre is the centre frequency of row r in QMF band units: that of the filter for a
// split row, k + 1/2 for the unsplit band k.
func (ly *psLayout) centre(r int) float64 {
	if n := ly.start[ly.nSplit]; r >= n {
		return float64(r-n+ly.nSplit) + 0.5
	}
	return ly.filt[r].f
}

// psSpec describes one row to build: output members of the M-output modulation of QMF
// band band, summed, with prototype proto.
type psSpec struct {
	band, m, proto int
	members        []int
}

// psSpecs20 and psSpecs34 list the rows of the layouts. At 20 bands QMF 0 has M = 8 and
// its outputs 2+5 and 3+4 are summed (P1 Fig. 4); QMF 1 and 2 are the two-band real
// split, which is the M = 4 modulation with its outputs summed in the pairs (0, 3) (low)
// and (1, 2) (high): their frequencies label the rows, the filters come from psProtoTwo.
// At 34 bands no output is summed.
var (
	psSpecs20 = []psSpec{
		{0, 8, psProtoM8, []int{0}}, {0, 8, psProtoM8, []int{1}}, {0, 8, psProtoM8, []int{2, 5}},
		{0, 8, psProtoM8, []int{3, 4}}, {0, 8, psProtoM8, []int{6}}, {0, 8, psProtoM8, []int{7}},
		{1, 4, psProtoTwo, []int{0, 3}}, {1, 4, psProtoTwo, []int{1, 2}},
		{2, 4, psProtoTwo, []int{0, 3}}, {2, 4, psProtoTwo, []int{1, 2}},
	}
	psSpecs34 = func() (s []psSpec) {
		for band, b := range [psSplitMax]struct{ m, proto int }{{12, psProtoM12}, {8, psProtoM8b}, {4, psProtoM4}, {4, psProtoM4}, {4, psProtoM4}} {
			for m := 0; m < b.m; m++ {
				s = append(s, psSpec{band, b.m, b.proto, []int{m}})
			}
		}
		return s
	}()
)

// psFreq is the centre frequency of output m of QMF band k split in M: 2(m+1/2)/M modulo
// 2, the value in [k-1/2, k+3/2), the band's own interval widened by its two half-band
// transitions.
func psFreq(k, m, out int) float64 {
	f := float64(2*out+1) / float64(m)
	return f + 2*math.Ceil((float64(k)-0.5-f)/2)
}

// psTrig returns cos and sin of pi*p/m for integer p, reduced to the first octant so
// that opposite angles give exactly opposite sines and the right angles are exact.
func psTrig(p, m int) (c, s float64) {
	p %= 2 * m
	switch {
	case p > m:
		c, s = psTrig(2*m-p, m)
		return c, -s
	case 2*p > m:
		c, s = psTrig(m-p, m)
		return -c, s
	case p == 0:
		return 1, 0
	case 2*p == m:
		return 0, 1
	}
	s, c = math.Sincos(math.Pi * float64(p) / float64(m))
	return c, s
}

// newPSLayout builds a layout from its specs; the 20-band rows are put in order of
// increasing centre frequency (signed), the 34-band rows stay in order of band and
// output.
func newPSLayout(specs []psSpec, nSplit int, byFreq bool) *psLayout {
	ly := &psLayout{nSplit: nSplit}
	for _, sp := range specs {
		g := psPrototype(sp.proto)
		r := psHybRow{band: sp.band}
		if sp.proto == psProtoTwo {
			// the real filters: low is g, high is (-1)^t g
			sign := 1.0
			if sp.members[0] != 0 {
				sign = -1
			}
			r.c0 = g[0]
			for t := 1; t <= psLook; t++ {
				r.c[t] = sign * g[t]
				sign = -sign
			}
		} else {
			for _, m := range sp.members {
				r.c0 += g[0]
				for t := 1; t <= psLook; t++ {
					c, s := psTrig((2*m+1)*t, sp.m)
					r.c[t] += g[t] * c
					r.s[t] += g[t] * s
				}
			}
		}
		// a row with one output takes its frequency in the widened interval, a summed
		// pair the member that lies in the band's own interval
		r.f = psFreq(sp.band, sp.m, sp.members[0])
		for _, m := range sp.members[1:] {
			if f := psFreq(sp.band, sp.m, m); f >= float64(sp.band) && f < float64(sp.band+1) {
				r.f = f
			}
		}
		ly.filt = append(ly.filt, r)
	}
	if byFreq {
		sort.SliceStable(ly.filt, func(i, j int) bool { return ly.filt[i].f < ly.filt[j].f })
	}
	for _, r := range ly.filt {
		ly.start[r.band+1]++
	}
	for k := 1; k <= nSplit; k++ {
		ly.start[k] += ly.start[k-1]
	}
	return ly
}

var psLayouts = [2]*psLayout{
	newPSLayout(psSpecs20, 3, true),
	newPSLayout(psSpecs34, psSplitMax, false),
}

func psLayoutOf(is34 bool) *psLayout {
	if is34 {
		return psLayouts[1]
	}
	return psLayouts[0]
}

// psHybrid is the hybrid bank of one channel element: the history of the split QMF
// bands, which serves both layouts (a switch between them needs no reset), and the rows
// of the frame.
type psHybrid struct {
	hist [psSplitMax][psLook]complex128 // slots 26..31 of the previous frame
	win  [psLook + sbrQMFSlots + psLook]complex128
	rows [sbrQMFSlots][psMaxRows]complex128
}

// analyse computes the hybrid rows of the 32 slots of x, the 64-band signal of the
// frame, into h.rows. look[j][k] is slot 32+j of band k, which the SBR output has not
// released yet. It also takes the frame's last six slots as the next frame's history.
func (h *psHybrid) analyse(x *[sbrQMFSlots][sbrSynBands]complex128, look *[psLook][psSplitMax]complex128, is34 bool) {
	ly := psLayoutOf(is34)
	w := &h.win
	for k := 0; k < ly.nSplit; k++ {
		copy(w[:psLook], h.hist[k][:])
		for l := range x {
			w[psLook+l] = x[l][k]
		}
		for j := range look {
			w[psLook+sbrQMFSlots+j] = look[j][k]
		}
		filt := ly.filt[ly.start[k]:ly.start[k+1]]
		out := ly.start[k]
		for l := 0; l < sbrQMFSlots; l++ {
			var sm, df [psLook + 1]complex128
			for t := 1; t <= psLook; t++ {
				a, b := w[l+psLook+t], w[l+psLook-t]
				sm[t], df[t] = a+b, a-b
			}
			ctr := w[l+psLook]
			for i := range filt {
				f := &filt[i]
				yr, yi := f.c0*real(ctr), f.c0*imag(ctr)
				for t := 1; t <= psLook; t++ {
					yr += f.c[t]*real(sm[t]) + f.s[t]*imag(df[t])
					yi += f.c[t]*imag(sm[t]) - f.s[t]*real(df[t])
				}
				h.rows[l][out+i] = complex(yr, yi)
			}
		}
	}
	n := ly.start[ly.nSplit]
	for l := range x {
		copy(h.rows[l][n:], x[l][ly.nSplit:])
	}
	// all five bands keep a history, whichever are split now
	for k := range h.hist {
		for i := range h.hist[k] {
			h.hist[k][i] = x[sbrQMFSlots-psLook+i][k]
		}
	}
}

// synthesise sums the rows of each split band of h.rows back into the 64-band signal x,
// and copies the others: no filter, gain or delay.
func (h *psHybrid) synthesise(x *[sbrQMFSlots][sbrSynBands]complex128, is34 bool) {
	ly := psLayoutOf(is34)
	n := ly.start[ly.nSplit]
	for l := range x {
		r := &h.rows[l]
		for k := 0; k < ly.nSplit; k++ {
			var sum complex128
			for _, v := range r[ly.start[k]:ly.start[k+1]] {
				sum += v
			}
			x[l][k] = sum
		}
		copy(x[l][ly.nSplit:], r[n:])
	}
}

// psLookAhead takes the six slots that follow the frame from the low band analysis: the
// SBR output lags the analysis by sbrHFAdj slots and the array holds sbrHFGen slots of
// history, so slot 32+j of the output is low[k][32+sbrHFAdj+j]. A band at or above the
// start frequency kx has nothing there and gets zeros.
func (d *sbrChanDSP) psLookAhead(kx int, look *[psLook][psSplitMax]complex128) {
	for j := range look {
		for k := range look[j] {
			look[j][k] = 0
			if k < kx {
				look[j][k] = d.low[k][sbrQMFSlots+sbrHFAdj+j]
			}
		}
	}
}

// psRoundTrip runs the frame of d.x through the hybrid analysis and synthesis. The PS
// stages between them are not written yet, so this is the identity: it is how the bank
// is wired and tested before PS is applied to the output (sbrState.psHybridTest).
func (s *sbrState) psRoundTrip(d *sbrChanDSP, kx int) {
	if s.hyb == nil {
		s.hyb = new(psHybrid)
	}
	var look [psLook][psSplitMax]complex128
	d.psLookAhead(kx, &look)
	s.hyb.analyse(&d.x, &look, s.ps.is34)
	s.hyb.synthesise(&d.x, s.ps.is34)
}
