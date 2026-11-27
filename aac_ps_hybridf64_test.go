// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

// The float64 hybrid bank of v0.17.1 (complex128, scalar), which is the oracle of the
// binary32 one in aac_ps_hybrid.go: the tests that exercise the mixer and the golden
// output run on it, TestPSHybridBound and TestPSHybridBoundStreams hold the decoder's
// binary32 bank to a stated bound of it, and installHybOracle routes a whole decode
// through it (the hook of aac_ps_hybrid.go, as installAdjOracle does for the
// adjustment).

// psHybrid is the hybrid bank of one channel element: the history of the split QMF
// bands, which serves both layouts (a switch between them needs no reset), and the rows
// of the frame.
type psHybrid struct {
	hist [psSplitMax][psLook]complex128 // slots 26..31 of the previous frame
	win  [psSplitMax][psLook + sbrQMFSlots + psLook]complex128
	rows [sbrQMFSlots][psMaxRows]complex128
}

// analyse computes the hybrid rows of the 32 slots of x, the 64-band signal of the
// frame, into h.rows. look[j][k] is slot 32+j of band k, which the SBR output has not
// released yet. It also takes the frame's last six slots as the next frame's history.
func (h *psHybrid) analyse(x *[sbrQMFSlots][sbrSynBands]complex128, look *[psLook][psSplitMax]complex128, is34 bool) {
	h.begin(x, look, is34)
	for l := range h.rows {
		h.slot(l, x, is34, &h.rows[l])
	}
	h.end(x)
}

// begin starts the analysis of a frame: the window of every split band, the six slots of
// history, the 32 of the frame and the six of look-ahead.
func (h *psHybrid) begin(x *[sbrQMFSlots][sbrSynBands]complex128, look *[psLook][psSplitMax]complex128, is34 bool) {
	ly := psLayoutOf(is34)
	for k := 0; k < ly.nSplit; k++ {
		w := &h.win[k]
		copy(w[:psLook], h.hist[k][:])
		for l := range x {
			w[psLook+l] = x[l][k]
		}
		for j := range look {
			w[psLook+sbrQMFSlots+j] = look[j][k]
		}
	}
}

// slot computes the hybrid rows of slot l into rows: the filters of the split bands over
// the window, and the other bands as they are.
func (h *psHybrid) slot(l int, x *[sbrQMFSlots][sbrSynBands]complex128, is34 bool, rows *[psMaxRows]complex128) {
	ly := psLayoutOf(is34)
	for k := 0; k < ly.nSplit; k++ {
		w := &h.win[k]
		filt := ly.filt[ly.start[k]:ly.start[k+1]]
		out := ly.start[k]
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
			rows[out+i] = complex(yr, yi)
		}
	}
	n := ly.start[ly.nSplit]
	copy(rows[n:], x[l][ly.nSplit:])
}

// end finishes the frame: its last six slots are the history of the next. All five bands
// keep a history, whichever are split now.
func (h *psHybrid) end(x *[sbrQMFSlots][sbrSynBands]complex128) {
	for k := range h.hist {
		for i := range h.hist[k] {
			h.hist[k][i] = x[sbrQMFSlots-psLook+i][k]
		}
	}
}

// synthesise sums the rows of each split band of h.rows back into the 64-band signal x,
// and copies the others: no filter, gain or delay.
func (h *psHybrid) synthesise(x *[sbrQMFSlots][sbrSynBands]complex128, is34 bool) {
	psSynthesise(&h.rows, x, is34)
}

// psSynthesise is the synthesis of any block of rows (the left or the right channel).
func psSynthesise(rows *[sbrQMFSlots][psMaxRows]complex128, x *[sbrQMFSlots][sbrSynBands]complex128, is34 bool) {
	ly := psLayoutOf(is34)
	n := ly.start[ly.nSplit]
	for l := range x {
		r := &rows[l]
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

// psSlotPlanes is one slot of the 64-band signal as the two real planes the synthesis
// bank's transforms take: re[k] the real part of band k and im[63-k] its imaginary part.
type psSlotPlanes struct {
	re, im [sbrSynBands]float64
}

// psSynthesiseSlot is psSynthesise for one slot, into planes: the sums of the rows of the
// split bands (added in row order) and the other rows copied.
func psSynthesiseSlot(r *[psMaxRows]complex128, is34 bool, p *psSlotPlanes) {
	ly := psLayoutOf(is34)
	for k := 0; k < ly.nSplit; k++ {
		var sum complex128
		for _, v := range r[ly.start[k]:ly.start[k+1]] {
			sum += v
		}
		p.re[k], p.im[sbrSynBands-1-k] = real(sum), imag(sum)
	}
	for j, v := range r[ly.start[ly.nSplit]:] {
		k := ly.nSplit + j
		if k >= sbrSynBands {
			break
		}
		p.re[k], p.im[sbrSynBands-1-k] = real(v), imag(v)
	}
}

// slotQuadF64 is slotQuad with the float64 planes of v0.17.1, converted to binary32 at
// the synthesis bank.
func slotQuadF64(y1, y2 *sbrSynthesis, o1, o2 *[sbrSynBands]float32, a1, b1, a2, b2 *[sbrSynBands]float64) {
	for k := range y1.a {
		y1.a[k], y1.b[k] = float32(a1[k]), float32(b1[k])
		y2.a[k], y2.b[k] = float32(a2[k]), float32(b2[k])
	}
	slotQuad32(y1, y2, o1, o2)
}

// apply turns the mono hybrid rows of h into the left rows (in place) and the right rows
// (m.rr), for an applied frame; top is the SBR top kx+M. It is the slot steps run over
// the whole frame; the slot by slot pipeline interleaves them with the stages that
// precede and follow.
func (m *psMixer) apply(h *psHybrid, p *psState, top int) {
	dl := m.begin(p, top)
	for n := range h.rows {
		m.transientSlot(n, &h.rows[n], dl)
		m.step(n, &h.rows[n], &m.rr[n], dl)
	}
}

// psRoundTripF64 runs the frame of d.x through the float64 analysis and synthesis, the
// identity to 1e-14.
func psRoundTripF64(h *psHybrid, d *sbrChanDSP, kx int, is34 bool) {
	var look [psLook][psSplitMax]complex128
	d.psLookAhead(kx, &look)
	h.analyse(&d.x, &look, is34)
	h.synthesise(&d.x, is34)
}

// psHybOracleF64 is the v0.17.1 slot pipeline of a frame: the float64 hybrid bank, the
// float64 planes, and the synthesis banks fed through them.
type psHybOracleF64 struct{ hyb psHybrid }

func (o *psHybOracleF64) pipeline(s *sbrState, d *sbrChanDSP, kx, m int, out [2][]float32) {
	mx, h := s.mix, &o.hyb
	is34 := s.ps.is34
	var look [psLook][psSplitMax]complex128
	d.psLookAhead(kx, &look)
	h.begin(&d.x, &look, is34)
	dl := mx.begin(&s.ps, kx+m)
	outL, outR := d.syn.begin(out[0]), mx.syn.begin(out[1])
	var rows, rr [psMaxRows]complex128
	var pl, pr psSlotPlanes
	for n := 0; n < sbrQMFSlots; n++ {
		h.slot(n, &d.x, is34, &rows)
		mx.transientSlot(n, &rows, dl)
		mx.step(n, &rows, &rr, dl)
		psSynthesiseSlot(&rows, is34, &pl)
		psSynthesiseSlot(&rr, is34, &pr)
		slotQuadF64(&d.syn, &mx.syn, (*[sbrSynBands]float32)(outL[n*sbrSynBands:]), (*[sbrSynBands]float32)(outR[n*sbrSynBands:]),
			&pl.re, &pl.im, &pr.re, &pr.im)
	}
	h.end(&d.x)
}

// installHybOracle makes every sbrState made from now on run its PS frames through the
// float64 bank; the returned function restores the decoder's.
func installHybOracle() func() {
	old := psNewHybOracle
	psNewHybOracle = func() psHybOracle { return new(psHybOracleF64) }
	return func() { psNewHybOracle = old }
}
