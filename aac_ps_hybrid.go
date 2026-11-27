// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"sort"
	"sync"

	"github.com/go-simd/floats"
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
// The symmetry folds the sum: with p[t] = x[l+t] + x[l-t] and d[t] = x[l+t] - x[l-t],
//
//	y = g[0] x[l] + sum over t = 1..6 of g[t] (cos(theta t) p[t] - j sin(theta t) d[t]),
//	theta = pi(2m+1)/M,
//
// and the outputs that a layout sums into one row (m and M-1-m, whose sines cancel)
// become one filter with the summed coefficients. A row is therefore c0, c[t] (the
// cosine coefficients) and s[t] (the sine ones).

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

// psHybRow is one split row: y = c0 x[l] + sum over t of c[t] p[t] - j s[t] d[t] (see above),
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

// psTrig returns cos and sin of pi*p/m for integer p, reduced to the first quadrant (p up to m/2) so
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

// psHybTaps is the number of rows of one split row's dot product: the centre, the six
// sums and the six differences.
const psHybTaps = 2*psLook + 1

// psHybSplit is the most split rows of a layout (the 34-band layout's 12+8+4+4+4).
const psHybSplit = 32

// psHybWin is the length of a split band's window: the history, the frame, the look-ahead.
const psHybWin = psLook + sbrQMFSlots + psLook

// psHybCoefs is the filters of a layout as the broadcast rows MulAddRows32 takes: for
// split row r, row t of [psHybTaps][sbrQMFSlots] is c0 (t = 0), c[t] (1..6) or s[t-6]
// (7..12) in every column.
type psHybCoefs [psHybSplit][psHybTaps * sbrQMFSlots]float32

var (
	psHybCoefsOnce sync.Once
	psHybCoefsTab  [2]*psHybCoefs
)

func psHybCoefsOf(is34 bool) *psHybCoefs {
	psHybCoefsOnce.Do(func() {
		for i := range psHybCoefsTab {
			ly := psLayouts[i]
			if ly.start[ly.nSplit] > psHybSplit {
				panic("decode: PS layout has more split rows than psHybSplit")
			}
			tab := new(psHybCoefs)
			for r, f := range ly.filt[:ly.start[ly.nSplit]] {
				fill := func(t int, v float64) {
					for j := 0; j < sbrQMFSlots; j++ {
						tab[r][t*sbrQMFSlots+j] = float32(v)
					}
				}
				fill(0, f.c0)
				for t := 1; t <= psLook; t++ {
					fill(t, f.c[t])
					fill(psLook+t, f.s[t])
				}
			}
			psHybCoefsTab[i] = tab
		}
	})
	if is34 {
		return psHybCoefsTab[1]
	}
	return psHybCoefsTab[0]
}

// psHyb32 is the hybrid bank of one channel element in binary32: the history of the split
// QMF bands, which serves both layouts (a switch between them needs no reset), the window
// of the band being filtered, and the split rows of the frame. The filters are the
// fused multiply-add rows of a band's centre, sums and differences against a
// broadcast coefficient row, over the 32 slots of a frame at once (band-major); the
// pipeline then reads one slot of them at a time.
type psHyb32 struct {
	hist [psSplitMax][2][psLook]float32 // slots 26..31 of the previous frame, [re/im]
	win  [2][psHybWin]float32
	sig  [2][psHybTaps * sbrQMFSlots]float32 // the rows of the band's dot products
	out  [psHybSplit][2][sbrQMFSlots]float32 // split row, re/im, slot
}

// begin starts the analysis of a frame: for every split band the window (six slots of
// history, the 32 of the frame and six of look-ahead), and its rows over all the slots.
func (h *psHyb32) begin(x *[sbrQMFSlots][sbrSynBands]complex128, look *[psLook][psSplitMax]complex128, is34 bool) {
	ly := psLayoutOf(is34)
	tab := psHybCoefsOf(is34)
	w, sg := &h.win, &h.sig
	for k := 0; k < ly.nSplit; k++ {
		copy(w[0][:psLook], h.hist[k][0][:])
		copy(w[1][:psLook], h.hist[k][1][:])
		for l := range x {
			w[0][psLook+l], w[1][psLook+l] = float32(real(x[l][k])), float32(imag(x[l][k]))
		}
		for j := range look {
			v := look[j][k]
			w[0][psLook+sbrQMFSlots+j], w[1][psLook+sbrQMFSlots+j] = float32(real(v)), float32(imag(v))
		}
		copy(sg[0][:sbrQMFSlots], w[0][psLook:])
		copy(sg[1][:sbrQMFSlots], w[1][psLook:])
		for t := 1; t <= psLook; t++ {
			up, dn := psLook+t, psLook-t
			p := t * sbrQMFSlots
			d := (psLook + t) * sbrQMFSlots
			floats.Add32(sg[0][p:p+sbrQMFSlots], w[0][up:up+sbrQMFSlots], w[0][dn:dn+sbrQMFSlots])
			floats.Add32(sg[1][p:p+sbrQMFSlots], w[1][up:up+sbrQMFSlots], w[1][dn:dn+sbrQMFSlots])
			// y_re takes + s imag(a-b) and y_im - s real(a-b)
			floats.Sub32(sg[0][d:d+sbrQMFSlots], w[1][up:up+sbrQMFSlots], w[1][dn:dn+sbrQMFSlots])
			floats.Sub32(sg[1][d:d+sbrQMFSlots], w[0][dn:dn+sbrQMFSlots], w[0][up:up+sbrQMFSlots])
		}
		for r := ly.start[k]; r < ly.start[k+1]; r++ {
			o := &h.out[r]
			o[0], o[1] = [sbrQMFSlots]float32{}, [sbrQMFSlots]float32{}
			floats.MulAddRows32(o[0][:], sg[0][:], sbrQMFSlots, tab[r][:], sbrQMFSlots, psHybTaps)
			floats.MulAddRows32(o[1][:], sg[1][:], sbrQMFSlots, tab[r][:], sbrQMFSlots, psHybTaps)
		}
	}
}

// slot gives the hybrid rows of slot l: the split rows begin computed, and the other
// bands as they are.
func (h *psHyb32) slot(l int, x *[sbrQMFSlots][sbrSynBands]complex128, is34 bool, rows *[psMaxRows]complex128) {
	ly := psLayoutOf(is34)
	n := ly.start[ly.nSplit]
	for r := 0; r < n; r++ {
		rows[r] = complex(float64(h.out[r][0][l]), float64(h.out[r][1][l]))
	}
	copy(rows[n:], x[l][ly.nSplit:])
}

// end finishes the frame: its last six slots are the history of the next. All five bands
// keep a history, whichever are split now.
func (h *psHyb32) end(x *[sbrQMFSlots][sbrSynBands]complex128) {
	for k := range h.hist {
		for i := range h.hist[k][0] {
			v := x[sbrQMFSlots-psLook+i][k]
			h.hist[k][0][i], h.hist[k][1][i] = float32(real(v)), float32(imag(v))
		}
	}
}

// psSynthesiseSlot32 is the synthesis of one slot of rows: the sums of the rows of the
// split bands (added in row order, in complex128) and the other rows copied, into the
// synthesis bank's inputs, binary32: a[k] the real part of band k and b[63-k] its
// imaginary part.
func psSynthesiseSlot32(r *[psMaxRows]complex128, is34 bool, a, b *[sbrSynBands]float32) {
	ly := psLayoutOf(is34)
	for k := 0; k < ly.nSplit; k++ {
		var sum complex128
		for _, v := range r[ly.start[k]:ly.start[k+1]] {
			sum += v
		}
		a[k], b[sbrSynBands-1-k] = float32(real(sum)), float32(imag(sum))
	}
	for j, v := range r[ly.start[ly.nSplit]:] {
		k := ly.nSplit + j
		if k >= sbrSynBands {
			break
		}
		a[k], b[sbrSynBands-1-k] = float32(real(v)), float32(imag(v))
	}
}

// psHybOracle is the slot pipeline of a frame; the decoder's is psHyb32.pipeline and the
// tests substitute the float64 bank (psNewHybOracle, as sbrNewAdjOracle does for the
// envelope adjustment).
type psHybOracle interface {
	pipeline(s *sbrState, d *sbrChanDSP, kx, m int, out [2][]float32)
}

// psNewHybOracle, when set by a test, makes every sbrState made after it run its PS
// frames through the oracle it returns.
var psNewHybOracle func() psHybOracle

// pipeline is the applied PS path of a frame run slot by slot: for each of the 32
// slots the hybrid analysis, the transient detector, the decorrelator and mixer, the hybrid
// synthesis of the left and of the right block, and the synthesis bank's transform of
// each, so that a slot's rows live in the cache from analysis to PCM instead of the frame's
// 46 KB matrices being written and read by every stage in turn.
func (h *psHyb32) pipeline(s *sbrState, d *sbrChanDSP, kx, m int, out [2][]float32) {
	mx := s.mix
	is34 := s.ps.is34
	var look [psLook][psSplitMax]complex128
	d.psLookAhead(kx, &look)
	h.begin(&d.x, &look, is34)
	dl := mx.begin(&s.ps, kx+m)
	outL, outR := d.syn.begin(out[0]), mx.syn.begin(out[1])
	var rows, rr [psMaxRows]complex128
	for n := 0; n < sbrQMFSlots; n++ {
		h.slot(n, &d.x, is34, &rows)
		mx.transientSlot(n, &rows, dl)
		mx.step(n, &rows, &rr, dl)
		psSynthesiseSlot32(&rows, is34, &d.syn.a, &d.syn.b)
		psSynthesiseSlot32(&rr, is34, &mx.syn.a, &mx.syn.b)
		slotQuad32(&d.syn, &mx.syn, (*[sbrSynBands]float32)(outL[n*sbrSynBands:]), (*[sbrSynBands]float32)(outR[n*sbrSynBands:]))
	}
	h.end(&d.x)
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

// psApplyFrame is the PS path of a mono element's frame (taken when sbrState.psOn, unless
// the test switch psOffTest says otherwise):
// d.x, the mono 64-band signal of the frame with SBR top kx+m, becomes the left block
// and the mixer's block the right one, and each goes through its synthesis bank into
// out[0] and out[1]. A frame that does not apply PS (no SBR data, or PS not usable)
// sends the mono block through both banks, whose histories advance in every frame.
func (s *sbrState) psApplyFrame(d *sbrChanDSP, kx, m int, withSBR bool, out [2][]float32) {
	if s.mix == nil {
		s.mix = new(psMixer)
	}
	mx := s.mix
	mx.baseline, mx.ffImage = s.psBaselineTest, s.psFFImageTest
	if withSBR && s.ps.start {
		if s.hybOracle != nil {
			s.hybOracle.pipeline(s, d, kx, m, out)
			return
		}
		if s.hyb == nil {
			s.hyb = new(psHyb32)
		}
		s.hyb.pipeline(s, d, kx, m, out)
		return
	}
	{
		// PS is suspended (a fault until the next header, or no SBR data): both channels are
		// the mono signal and the state of PS, the decorrelator, the transient detector, the
		// matrices and the hybrid history, is frozen until it resumes (Q14)
		mx.xr = d.x
	}
	d.syn.process(out[0], &d.x)
	mx.syn.process(out[1], &mx.xr)
}

// psRoundTrip runs the frame of d.x through the hybrid analysis and synthesis. The PS
// stages between them are not applied, so this is the identity (to binary32): it is how
// the bank is wired and tested in a real stream (sbrState.psHybridTest).
func (s *sbrState) psRoundTrip(d *sbrChanDSP, kx int) {
	if s.hyb == nil {
		s.hyb = new(psHyb32)
	}
	h, is34 := s.hyb, s.ps.is34
	var look [psLook][psSplitMax]complex128
	d.psLookAhead(kx, &look)
	h.begin(&d.x, &look, is34)
	ly := psLayoutOf(is34)
	var rows [psMaxRows]complex128
	for l := range d.x {
		h.slot(l, &d.x, is34, &rows)
		for k := 0; k < ly.nSplit; k++ {
			var sum complex128
			for _, v := range rows[ly.start[k]:ly.start[k+1]] {
				sum += v
			}
			d.x[l][k] = sum
		}
	}
	h.end(&d.x)
}
