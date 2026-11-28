// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"github.com/go-simd/floats"
)

// The decorrelation and mixing stage of Parametric Stereo on float32 split planes, from the
// hybrid analysis output to the hybrid synthesis input (decode#77). A row is a pair of
// float32 (a real plane and an imaginary plane), so that the all-pass rows run on
// floats.AllPass3Split32, the mixing is a real multiply-add per component and nothing is
// converted between complex128 and float32 inside the stage. The complex128 stage (step,
// transientSlot, bandPowers, psMixPair, mixPhased) stays as the oracle the tests hold this
// one to.
//
// Contract (stated bound, not bit identity): with u = 2^-24, and every output compared with
// the complex128 stage on the same inputs, the mixed rows differ by a few units of u times
// the largest magnitude that entered the row (TestPSStage32AgainstOracle measures it).

// psPlanes is the rows of a slot as two float32 planes, real then imaginary.
type psPlanes [2][psMaxRows]float32

// Kinds of the source of a row's decorrelated signal.
const (
	psSrcAllPass = iota // the output of the all-pass chain
	psSrcLong           // the input delayed by psLongDelay slots
	psSrcShort          // the input delayed by psShortDelay slots
)

// psMixSeg is rows [start, end) of one parameter band that share one source of the
// decorrelated signal: a run of psDecLayout.runs split at the end of the all-pass rows and of
// the long-delay rows.
type psMixSeg struct{ start, end, band, src uint8 }

// psStage32 is the float32 state of a mixer: the input ring (rows of the last psHist+1
// slots), the rings of the three links, the output of the all-pass rows and the plan that
// proves their memory.
type psStage32 struct {
	in      [2][(psHist + 1) * psMaxRows]float32
	w       [3][2][(psLinkMaxDelay + 1) * psMaxAllPass]float32
	apOut   [2][psMaxAllPass]float32
	plan    *floats.AllPass3Plan32
	planFor *psDecLayout
	planMix *psStage32
}

func (dl *psDecLayout) initStage32() {
	n := dl.nAP
	t := &dl.apRows32
	mk := func() []float32 { return make([]float32, n) }
	t.PhiRe, t.PhiIm, t.Q0Re, t.Q0Im, t.Q1Re, t.Q1Im, t.Q2Re, t.Q2Im = mk(), mk(), mk(), mk(), mk(), mk(), mk(), mk()
	t.C0, t.C1, t.C2 = mk(), mk(), mk()
	for r := 0; r < n; r++ {
		a := &dl.ap[r]
		t.PhiRe[r], t.PhiIm[r] = float32(real(a.phi)), float32(imag(a.phi))
		t.Q0Re[r], t.Q0Im[r] = float32(real(a.q[0])), float32(imag(a.q[0]))
		t.Q1Re[r], t.Q1Im[r] = float32(real(a.q[1])), float32(imag(a.q[1]))
		t.Q2Re[r], t.Q2Im[r] = float32(real(a.q[2])), float32(imag(a.q[2]))
		t.C0[r], t.C1[r], t.C2[r] = float32(a.c[0]), float32(a.c[1]), float32(a.c[2])
	}
	kind := func(k int) uint8 {
		switch {
		case k < dl.nAP:
			return psSrcAllPass
		case k < dl.nLong:
			return psSrcLong
		}
		return psSrcShort
	}
	for _, r := range dl.runs {
		s := int(r.start)
		for k := int(r.start); k < int(r.end); k++ {
			if k+1 == int(r.end) || kind(k+1) != kind(k) {
				dl.segs = append(dl.segs, psMixSeg{uint8(s), uint8(k + 1), r.band, kind(k)})
				s = k + 1
			}
		}
	}
}

// allPassPlan32 is the plan of the all-pass rows on the float32 rings of layout dl, rebuilt
// when the layout changes or the stage is a copy of another one.
func (s *psStage32) allPassPlan32(dl *psDecLayout) *floats.AllPass3Plan32 {
	if s.plan != nil && s.planFor == dl && s.planMix == s {
		return s.plan
	}
	var w [3]floats.Split32
	var ws [3]int
	for l := range w {
		n := (psLinkDelay[l] + 1) * psMaxAllPass
		w[l] = floats.Split32{Re: s.w[l][0][:n], Im: s.w[l][1][:n]}
		ws[l] = psMaxAllPass
	}
	p, err := floats.NewAllPass3TickPlan32(&dl.apRows32, floats.Split32{Re: s.apOut[0][:dl.nAP], Im: s.apOut[1][:dl.nAP]},
		floats.Split32{Re: s.in[0][:], Im: s.in[1][:]}, psMaxRows, w, ws, psPreDelay)
	if err != nil {
		panic(err) // the layout tables and the stage's arrays are fixed: a refusal is a bug
	}
	s.plan, s.planFor, s.planMix = p, dl, s
	return p
}

func (s *psStage32) reset() {
	s.in = [2][(psHist + 1) * psMaxRows]float32{}
	s.w = [3][2][(psLinkMaxDelay + 1) * psMaxAllPass]float32{}
}

// clearAbove is psMixer.clearAbove on the planes.
func (s *psStage32) clearAbove(dl *psDecLayout, kt int) {
	for c := range s.in {
		for sl := 0; sl <= psHist; sl++ {
			clear(s.in[c][sl*psMaxRows+kt : sl*psMaxRows+dl.rows])
		}
	}
	if kt < dl.nAP {
		for l := range s.w {
			for c := range s.w[l] {
				for sl := 0; sl <= psLinkMaxDelay; sl++ {
					clear(s.w[l][c][sl*psMaxAllPass+kt : sl*psMaxAllPass+dl.nAP])
				}
			}
		}
	}
}

// inSlot is the rows of the slot of tick t in the input ring, which the hybrid analysis
// fills and the detector and the mixer read.
func (s *psStage32) inSlot(t int) (re, im *[psMaxRows]float32) {
	o := (t % (psHist + 1)) * psMaxRows
	return (*[psMaxRows]float32)(s.in[0][o:]), (*[psMaxRows]float32)(s.in[1][o:])
}

// transientSlot32 is transientSlot for the rows of the slot as planes.
func (m *psMixer) transientSlot32(n int, re, im *[psMaxRows]float32, dl *psDecLayout) {
	var pw [psMaxBands]float64
	// the power of every row (independent products), then the sums of the runs
	var p [psMaxRows]float32
	rr, ii := re[:dl.rows], im[:dl.rows]
	pp := p[:len(rr)]
	ii = ii[:len(rr)]
	for k, v := range rr {
		pp[k] = v*v + ii[k]*ii[k]
	}
	for _, r := range dl.runs {
		sum := p[r.start]
		for k := r.start + 1; k < r.end; k++ {
			sum += p[k]
		}
		pw[r.band] += float64(sum)
	}
	m.transientBands(n, &pw, dl.nb)
}

// step32 decorrelates and mixes the slot of tick m.tick, whose rows the caller put in the
// input ring (inSlot): the left rows go to l, the right rows to r.
func (m *psMixer) step32(n int, dl *psDecLayout, l, r *psPlanes) {
	st := &m.st32
	t := m.tick
	const ring = psHist + 1
	sRe, sIm := st.inSlot(t)
	if dl.nAP > 0 {
		st.allPassPlan32(dl).RunTick(t)
	}
	lgRe, lgIm := st.inSlot(t + ring - psLongDelay)
	shRe, shIm := st.inSlot(t + ring - psShortDelay)
	gn, tn := &m.gain[n], &m.tg[n]
	for _, sg := range dl.segs {
		a, e, b := int(sg.start), int(sg.end), sg.band
		var dRe, dIm []float32
		switch sg.src {
		case psSrcAllPass:
			dRe, dIm = st.apOut[0][a:e], st.apOut[1][a:e]
		case psSrcLong:
			dRe, dIm = lgRe[a:e], lgIm[a:e]
		default:
			dRe, dIm = shRe[a:e], shIm[a:e]
		}
		g := &gn[b]
		if m.phased && int(b) < m.nPh {
			m.mixPhased32(n, a, e, b, dl, sRe, sIm, dRe, dIm, l, r)
			continue
		}
		tg := tn[b]
		gLs, gLd, gRs, gRd := float32(g[psGLs]), float32(g[psGLd]*tg), float32(g[psGRs]), float32(g[psGRd]*tg)
		mixSeg32(gLs, gLd, gRs, gRd, sRe[a:e], dRe, l[0][a:e], r[0][a:e])
		mixSeg32(gLs, gLd, gRs, gRd, sIm[a:e], dIm, l[1][a:e], r[1][a:e])
	}
	m.tick++
}

// mixSeg32 is l = gLs s + gLd d and r = gRs s + gRd d on one component of a run of rows
// that share their gains (the transient gain already in gLd and gRd).
func mixSeg32(gLs, gLd, gRs, gRd float32, s, d, l, r []float32) {
	d, l, r = d[:len(s)], l[:len(s)], r[:len(s)]
	for i, sv := range s {
		dv := d[i]
		l[i] = gLs*sv + gLd*dv
		r[i] = gRs*sv + gRd*dv
	}
}

// mixPhased32 is mixPhased on the rows [a, e) of a phase band: the complex gains of the slot,
// the transient gain on d, and the conjugate gains for the rows of negative centre frequency.
func (m *psMixer) mixPhased32(n, a, e int, b uint8, dl *psDecLayout, sRe, sIm *[psMaxRows]float32, dRe, dIm []float32, l, r *psPlanes) {
	g, gi := &m.gain[n][b], &m.gainIm[n][b]
	tg := m.tg[n][b]
	for k := a; k < e; k++ {
		sg := 1.0
		if dl.image[k] && !m.ffImage {
			sg = -1
		}
		gim := *gi
		if m.ffImage && dl.imageFF(k) {
			for i := range gim {
				gim[i] -= 2 * m.imStart[n][b][i]
			}
		}
		// the transient gain on d, then the complex gains: (gr + j gi) * z
		xr, xi := float32(tg)*dRe[k-a], float32(tg)*dIm[k-a]
		sr, si := sRe[k], sIm[k]
		lsr, lsi := float32(g[psGLs]), float32(sg*gim[psGLs])
		ldr, ldi := float32(g[psGLd]), float32(sg*gim[psGLd])
		rsr, rsi := float32(g[psGRs]), float32(sg*gim[psGRs])
		rdr, rdi := float32(g[psGRd]), float32(sg*gim[psGRd])
		l[0][k] = lsr*sr - lsi*si + ldr*xr - ldi*xi
		l[1][k] = lsr*si + lsi*sr + ldr*xi + ldi*xr
		r[0][k] = rsr*sr - rsi*si + rdr*xr - rdi*xi
		r[1][k] = rsr*si + rsi*sr + rdr*xi + rdi*xr
	}
}

// psSynthesiseSlot32P is psSynthesiseSlot32 on planes: the sums of the rows of the split
// bands (added in row order, in float32) and the other rows copied, into a (the real part of
// band k) and b (its imaginary part at 63-k).
func psSynthesiseSlot32P(p *psPlanes, is34 bool, a, b *[sbrSynBands]float32) {
	ly := psLayoutOf(is34)
	re, im := &p[0], &p[1]
	for k := 0; k < ly.nSplit; k++ {
		var sr, si float32
		for r := ly.start[k]; r < ly.start[k+1]; r++ {
			sr += re[r]
			si += im[r]
		}
		a[k], b[sbrSynBands-1-k] = sr, si
	}
	n := ly.start[ly.nSplit]
	ns := ly.nSplit
	copy(a[ns:], re[n:n+sbrSynBands-ns])
	src := im[n : n+sbrSynBands-ns]
	for j, v := range src {
		b[sbrSynBands-1-ns-j] = v
	}
}

// slot32 gives the hybrid rows of slot l as planes: the split rows, then the other bands.
func (h *psHyb32) slot32(l int, x *[sbrQMFSlots][sbrSynBands]complex128, is34 bool, re, im *[psMaxRows]float32) {
	ly := psLayoutOf(is34)
	n := ly.start[ly.nSplit]
	for r := 0; r < n; r++ {
		re[r], im[r] = h.out[r][0][l], h.out[r][1][l]
	}
	for j, v := range x[l][ly.nSplit:] {
		re[n+j], im[n+j] = float32(real(v)), float32(imag(v))
	}
}

// pipeline32 is pipeline with the stage between the hybrid analysis and synthesis on float32
// planes.
func (h *psHyb32) pipeline32(s *sbrState, d *sbrChanDSP, kx, m int, out [2][]float32) {
	mx := s.mix
	is34 := s.ps.is34
	var look [psLook][psSplitMax]complex128
	d.psLookAhead(kx, &look)
	h.begin(&d.x, &look, is34)
	dl := mx.begin(&s.ps, kx+m)
	outL, outR := d.syn.begin(out[0]), mx.syn.begin(out[1])
	var pl, pr psPlanes
	for n := 0; n < sbrQMFSlots; n++ {
		re, im := mx.st32.inSlot(mx.tick)
		h.slot32(n, &d.x, is34, re, im)
		mx.transientSlot32(n, re, im, dl)
		mx.step32(n, dl, &pl, &pr)
		psSynthesiseSlot32P(&pl, is34, &d.syn.a, &d.syn.b)
		psSynthesiseSlot32P(&pr, is34, &mx.syn.a, &mx.syn.b)
		slotQuad32(&d.syn, &mx.syn, (*[sbrSynBands]float32)(outL[n*sbrSynBands:]), (*[sbrSynBands]float32)(outR[n*sbrSynBands:]))
	}
	h.end(&d.x)
}
