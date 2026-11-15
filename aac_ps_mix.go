// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "math"

// Parametric Stereo decorrelation and mixing (HE-AAC v2), stages 2 to 5 of
// docs/aac-ps-design.md, written from docs/aac-ps-mixing.md sections 1 to 5 alone (a
// clean-room specification: no reference implementation was opened for this file), and
// section 6 (PS5: procedure B for ICC modes 3 to 5, IPD/OPD with their smoothing and
// interpolation). This is the full profile, which the decoder applies by default; the
// baseline profile (procedure A for every ICC mode, IPD/OPD ignored) is kept behind the
// test switch psMixer.baseline (sbrState.psBaselineTest).
//
// For each applied frame, the mono hybrid rows s are turned into a decorrelated signal d
// (an all-pass chain in the lowest 23 QMF bands, a plain delay above, both scaled by a
// transient-attenuation gain per parameter band), and the left and right rows are
//
//	l = gLs s + gLd d,   r = gRs s + gRd d
//
// with four real gains per parameter band that are interpolated linearly in time between
// the envelopes' parameter positions.

// A psGain holds the four gains of one parameter band at one instant, in the order
// mono-to-left, decorrelated-to-left, mono-to-right, decorrelated-to-right.
type psGain [psNGains]float64

const (
	psGLs = iota
	psGLd
	psGRs
	psGRd
	psNGains
)

// psMixGains are the gains of procedure A (section 4.1) for every IID and ICC index of
// both resolutions, computed from the definitions at start-up. Derived.
var (
	psMixCoarse = psMixTable(psIIDGainCoarse)
	psMixFine   = psMixTable(psIIDGainFine)
)

func psMixTable(ratio []float64) [][8]psGain {
	t := make([][8]psGain, len(ratio))
	for i, c := range ratio {
		cR := math.Sqrt2 / math.Sqrt(1+c*c)
		cL := c * cR
		for icc, alpha := range psICCAngle {
			beta := alpha * (cR - cL) / math.Sqrt2
			sp, cp := math.Sincos(beta + alpha)
			sm, cm := math.Sincos(beta - alpha)
			t[i][icc] = psGain{psGLs: cL * cp, psGLd: cL * sp, psGRs: cR * cm, psGRd: cR * sm}
		}
	}
	return t
}

// psMixGain returns the gains of IID index iid and ICC index icc.
func psMixGain(iid, icc int, fine bool) psGain {
	if fine {
		return psMixFine[iid+15][icc]
	}
	return psMixCoarse[iid+7][icc]
}

// psMixGainB returns the gains of procedure B (section 6.1) for the same indices.
func psMixGainB(iid, icc int, fine bool) psGain {
	if fine {
		return psMixBFine[iid+15][icc]
	}
	return psMixBCoarse[iid+7][icc]
}

// psMixB are the gains of procedure B (ICC modes 3 to 5, section 6.1) for every IID and
// ICC index. The matrix is the principal-axis decomposition of the covariance the
// parameters describe (powers in the ratio c^2 normalised to a sum of 2, correlation
// rho' = max(rho, 0.05)): a rotation by alpha of (sqrt2 cos gamma s, sqrt2 sin gamma d).
// Derived, from the definitions.
var (
	psMixBCoarse = psMixTableB(psIIDGainCoarse)
	psMixBFine   = psMixTableB(psIIDGainFine)
)

// psRhoFloor is the floor of the coherence in procedure B: a normative value.
const psRhoFloor = 0.05

func psMixTableB(ratio []float64) [][8]psGain {
	t := make([][8]psGain, len(ratio))
	for i, c := range ratio {
		c2 := c * c
		for icc, rho := range psICC {
			rho = max(rho, psRhoFloor)
			alpha := 0.5 * math.Atan2(2*c*rho, c2-1)
			// the eigenvalues of the normalised covariance are 1 +- nu, and
			// (1-nu)/(1+nu) = x/(1+nu)^2 with x = 1-nu^2, which does not cancel near nu = 1
			x := 4 * c2 * (1 - rho*rho) / ((1 + c2) * (1 + c2))
			nu := math.Sqrt(max(1-x, 0))
			gamma := math.Atan(math.Sqrt(x) / (1 + nu))
			sa, ca := math.Sincos(alpha)
			sg, cg := math.Sincos(gamma)
			t[i][icc] = psGain{
				psGLs: math.Sqrt2 * ca * cg, psGLd: -math.Sqrt2 * sa * sg,
				psGRs: math.Sqrt2 * sa * cg, psGRd: math.Sqrt2 * ca * sg,
			}
		}
	}
	return t
}

// The weights of the phase smoothing over the last two envelopes and the current one
// (section 6.2): normative values.
const (
	psPhaseW2 = 0.25
	psPhaseW1 = 0.5
	psPhaseW0 = 1.0
)

// psPhaseBandsOf is the number of parameter bands that carry phases in a layout.
func psPhaseBandsOf(is34 bool) int {
	if is34 {
		return 17
	}
	return 11
}

// psRemapPhase maps the n phase bands of src (5, 11 or 17: the IPD/OPD bands of the IID
// mode) to the len(dst) phase bands of the layout nb (20 or 34 parameter bands), by the
// maps of section 4.2 restricted to the phase bands (section 6.2): the source bands past
// n are taken as 0, so that target band 10 of 5 -> 11 and 16 of 5 -> 17 are 0 and the
// averages of the other maps never reach a band the source lacks.
func psRemapPhase(dst, src []int8, n, nb int) {
	from := 0
	switch n {
	case 5:
		from = 10
	case 11:
		from = 20
	case 17:
		from = 34
	default:
		clear(dst)
		return
	}
	var pad, full [psMaxBands]int8
	copy(pad[:], src[:n])
	psRemapBands(full[:], pad[:from], from, nb)
	copy(dst, full[:len(dst)])
}

// psPhasor is e^{j k pi/4} for a phase index.
func psPhasor(k int8) complex128 {
	s, c := math.Sincos(psPhase[k&7])
	return complex(c, s)
}

// psSmoothPhase pushes the phase index k into the history h (the phasors of the last two
// envelopes, the latest first) and returns the unit phasor of the smoothed phase,
// arg(w2 e^{j th[e-2]} + w1 e^{j th[e-1]} + w0 e^{j th[e]}) (section 6.2).
func psSmoothPhase(h *[2]complex128, k int8) complex128 {
	u := psPhasor(k)
	sum := psPhaseW2*h[1] + psPhaseW1*h[0] + psPhaseW0*u
	h[1], h[0] = h[0], u
	return sum / complex(math.Hypot(real(sum), imag(sum)), 0)
}

// Constants of the transient attenuation (section 3.5): the peak decay is psPeakDecay;
// the smoothing and the impact are normative values.
const (
	psSmooth = 0.25
	psImpact = 1.5
)

// Constants of the decorrelator (section 3.3 and 3.4): the all-pass pre-delay in slots,
// the fractional pre-delay and link delays in thousandths of a slot, and the slope of
// the decay with the QMF band. Normative values.
const (
	psPreDelay    = 2
	psLongDelay   = 14
	psShortDelay  = 1
	psDecaySlope  = 0.05
	psAllPassQMF  = 23 // QMF bands below this use the all-pass chain
	psLongEndQMF  = 35 // and from here on the short delay
	psHist        = psLongDelay
	psMaxAllPass  = 50 // all-pass rows of the 34-band layout
	psLinkSpan    = 5 + sbrQMFSlots
	psPreFracMil  = 390
	psAngleMilDen = 24 * 1000 * 2 // angle pi*delta*f, with f in 24ths and delta in thousandths, per turn of 2 pi
)

var psLinkFracMil = [3]int{430, 750, 347}

// psAllPass holds the constants of one all-pass row: the fractional pre-delay rotation
// phi, and for each link the rotation q, the coefficient c = a*gamma and c*q.
type psAllPass struct {
	phi   complex128
	q, cq [3]complex128
	c     [3]float64
}

// psDecLayout is the row classes and the all-pass constants of a layout.
type psDecLayout struct {
	rows, nAP, nLong int
	nb               int
	qmf              [psMaxRows]uint8
	band             []uint8
	runs             []psBandRun // the rows grouped by consecutive equal band, in row order
	ap               [psMaxAllPass]psAllPass
	image            [psMaxRows]bool // rows of negative centre frequency: they take the conjugate phases (section 6.2)
}

// psBandRun is rows [start, end) of one parameter band.
type psBandRun struct{ start, end, band uint8 }

var psDecLayouts = [2]*psDecLayout{newPSDecLayout(false), newPSDecLayout(true)}

// psRotation is e^{-j pi delta f} for delta in thousandths and f in 24ths of a QMF
// band, with the angle reduced exactly in integers before it becomes a float.
func psRotation(deltaMil, f24 int) complex128 {
	turns := (deltaMil * f24) % psAngleMilDen // in units of pi/(24*1000)
	s, c := math.Sincos(-math.Pi * float64(turns) / (24 * 1000))
	return complex(c, s)
}

func newPSDecLayout(is34 bool) *psDecLayout {
	ly := psLayoutOf(is34)
	dl := &psDecLayout{rows: ly.rows(), nb: 20, band: aacPSBandOf20[:]}
	if is34 {
		dl.nb, dl.band = 34, aacPSBandOf34[:]
	}
	nSplitRows := ly.start[ly.nSplit]
	for r := 0; r < dl.rows; r++ {
		q := r - nSplitRows + ly.nSplit
		if r < nSplitRows {
			q = ly.filt[r].band
		}
		dl.qmf[r] = uint8(q)
		dl.image[r] = ly.centre(r) < 0
		switch {
		case q < psAllPassQMF:
			dl.nAP = r + 1
		case q < psLongEndQMF:
			dl.nLong = r + 1
		}
		if q >= psAllPassQMF {
			continue
		}
		f24 := int(math.Round(ly.centre(r) * 24))
		gamma := 1.0
		if r >= nSplitRows {
			gamma = min(max(1-psDecaySlope*float64(q-ly.nSplit), 0), 1)
		}
		ap := &dl.ap[r]
		ap.phi = psRotation(psPreFracMil, f24)
		for m := range ap.q {
			ap.q[m] = psRotation(psLinkFracMil[m], f24)
			ap.c[m] = psLinkDecay[m] * gamma
			ap.cq[m] = complex(ap.c[m], 0) * ap.q[m]
		}
	}
	for r := 0; r < dl.rows; r++ {
		if r > 0 && dl.band[r] == dl.band[r-1] {
			dl.runs[len(dl.runs)-1].end++
			continue
		}
		dl.runs = append(dl.runs, psBandRun{uint8(r), uint8(r + 1), dl.band[r]})
	}
	return dl
}

// psMixer is the state of the decorrelator, the transient detector and the carried
// matrix of one channel element, and the scratch of a frame.
type psMixer struct {
	started bool
	is34    bool // layout of the last applied frame

	// baseline is the test switch for baseline mixing: procedure A for every ICC mode and
	// IPD/OPD ignored (what decode v0.7.0 did). By default the full profile is applied.
	baseline bool

	// ffImage is a second test switch: the image rows of IPD/OPD follow ffmpeg's rule
	// (rows 0 and 1 at 20 bands, 9 to 13 at 34, the imaginary part running from minus its
	// value at the start of the envelope to its value at the end) instead of the
	// conjugate the specification derives, which ISO's al_sbr_ps_04 supports (0.033 LSB
	// against 0.76 LSB). It exists to show that everything else is ffmpeg's to 1e-6.
	ffImage bool

	// store[e] is the matrix of envelope e of the last applied frame, store[0] the one the
	// frame started from. The next frame starts from store[numEnvOld], the envelope count
	// of the frame parsed before the last one (what ffmpeg's output shows: S06b, S19; the
	// ISO conformance streams are to arbitrate, Q14 and Q15). storeIm holds the imaginary
	// parts of the gains, which IPD/OPD make complex (zero without them).
	store, storeIm [psMaxEnv + 1][psMaxBands]psGain

	// phased: the frame applies IPD/OPD, to the first nPh parameter bands. hist[0] and
	// hist[1] are the phasors of the last two envelopes of IPD and OPD by phase band.
	phased bool
	nPh    int
	hist   [2][psMaxBands][2]complex128

	// transient detector, per parameter band
	peak, avg, dev [psMaxBands]float64

	// in[k] holds the last psHist slots of row k's input, then the current frame's;
	// w[k][m] the internal signal of link m of all-pass row k, its slot n at index
	// psLinkDelay[m]+n, the delayed values in front.
	in [psMaxRows][psHist + sbrQMFSlots]complex128
	w  [psMaxAllPass][3][psLinkSpan]complex128

	// the frame's scratch
	gain, gainIm [sbrQMFSlots][psMaxBands]psGain
	imStart      [sbrQMFSlots][psMaxBands]psGain // ffImage only: the start's share of gainIm
	tg           [sbrQMFSlots][psMaxBands]float64
	rr           [sbrQMFSlots][psMaxRows]complex128
	xr           [sbrQMFSlots][sbrSynBands]complex128
	pl, pr       sbrPlanes    // the left and the right block as planes, for the synthesis banks
	syn          sbrSynthesis // the synthesis bank of the right channel
}

// reset clears the decorrelator and the transient detector (a layout switch).
func (m *psMixer) reset() {
	m.in = [psMaxRows][psHist + sbrQMFSlots]complex128{}
	m.w = [psMaxAllPass][3][psLinkSpan]complex128{}
	m.peak, m.avg, m.dev = [psMaxBands]float64{}, [psMaxBands]float64{}, [psMaxBands]float64{}
	// the phase history before the first envelope, and after a layout switch, is angle 0
	for i := range m.hist {
		for b := range m.hist[i] {
			m.hist[i][b] = [2]complex128{1, 1}
		}
	}
}

// switchTo remaps the carried matrix (both parts of its gains) to the layout is34, by the
// value forms of the parameter maps, and resets the decorrelator.
func (m *psMixer) switchTo(is34 bool) {
	from, to := 20, 34
	if !is34 {
		from, to = 34, 20
	}
	var src, dst [psMaxBands]float64
	for _, store := range []*[psMaxEnv + 1][psMaxBands]psGain{&m.store, &m.storeIm} {
		for e := range store {
			for i := range psNGains {
				for b := 0; b < from; b++ {
					src[b] = store[e][b][i]
				}
				psRemapBands(dst[:], src[:], from, to)
				for b := 0; b < to; b++ {
					store[e][b][i] = dst[b]
				}
			}
		}
	}
	m.reset()
	m.is34 = is34
}

// clearAbove zeroes the past of every row of a QMF band at or above top, the SBR top.
func (m *psMixer) clearAbove(dl *psDecLayout, top int) {
	for k := 0; k < dl.rows; k++ {
		if int(dl.qmf[k]) < top {
			continue
		}
		clear(m.in[k][:psHist])
		if k < dl.nAP {
			for j := range m.w[k] {
				clear(m.w[k][j][:psLinkDelay[j]])
			}
		}
	}
}

// matrices computes the gains of every slot of the frame (section 4.3) into m.gain and
// keeps the matrix of every envelope. The frame starts from store[numEnvOld]. A frame
// without ps_data uses the parameters and borders of the last parsed one.
func (m *psMixer) matrices(p *psState, nb int) {
	numEnv, border := p.numEnv, p.border
	// A frame without phases is real (its imaginary parts are zero at once) and leaves the
	// stored imaginary parts of the matrices as they were: the next frame with phases
	// starts from those (ffmpeg's output, S16e; ISO has no such stream).
	prev, prevIm := m.store[p.numEnvOld], m.storeIm[p.numEnvOld]
	m.store[0] = prev
	if m.phased {
		m.storeIm[0] = prevIm
	}
	for e := 1; e <= numEnv; e++ {
		var next, nextIm [psMaxBands]psGain
		m.envelopeGains(p, e-1, nb, &next, &nextIm)
		m.store[e] = next
		if m.phased {
			m.storeIm[e] = nextIm
		}
		b0, b1 := int(border[e-1]), int(border[e])
		for n := b0 + 1; n <= b1; n++ {
			if n == b1 {
				copy(m.gain[n][:nb], next[:nb])
				copy(m.gainIm[n][:nb], nextIm[:nb])
				clear(m.imStart[n][:nb])
				continue
			}
			f := float64(n-b0) / float64(b1-b0)
			// the interpolation of every band, the four gains unrolled
			gn, pv, nx := m.gain[n][:nb], prev[:nb], next[:nb]
			for b := range gn {
				p, q, g := &pv[b], &nx[b], &gn[b]
				g[0] = p[0] + f*(q[0]-p[0])
				g[1] = p[1] + f*(q[1]-p[1])
				g[2] = p[2] + f*(q[2]-p[2])
				g[3] = p[3] + f*(q[3]-p[3])
			}
			for b := 0; b < nb; b++ {
				if m.phased && b < m.nPh {
					for i := range psNGains {
						m.gainIm[n][b][i] = prevIm[b][i] + f*(nextIm[b][i]-prevIm[b][i])
						if m.ffImage {
							m.imStart[n][b][i] = prevIm[b][i] * (1 - f)
						}
					}
				}
			}
		}
		prev, prevIm = next, nextIm
	}
}

// envelopeGains fills h with the gains of envelope e: its indices mapped to the nb
// bands of the layout, through the tables of section 4.1 (procedure A) or 6.1 (procedure
// B, ICC modes 3 to 5). With IPD/OPD in force, the first phase bands get complex gains:
// h holds their real parts and hi their imaginary parts.
func (m *psMixer) envelopeGains(p *psState, e, nb int, h, hi *[psMaxBands]psGain) {
	var iid, icc [psMaxBands]int8
	if p.enableIID {
		psRemapBands(iid[:], p.iid[e][:], p.nrIID, nb)
	}
	if p.enableICC {
		psRemapBands(icc[:], p.icc[e][:], p.nrICC, nb)
	}
	gain := psMixGain
	if !m.baseline && p.enableICC && p.iccMode >= 3 {
		gain = psMixGainB
	}
	for b := 0; b < nb; b++ {
		h[b] = gain(int(iid[b]), int(icc[b]), p.iidQuant)
	}
	if m.phased {
		m.applyPhases(p, e, nb, h, hi)
	}
}

// applyPhases rotates the gains of the phase bands of envelope e by the smoothed IPD and
// OPD (section 6.2): the left gains by e^{j opd}, the right ones by e^{j (opd - ipd)}.
func (m *psMixer) applyPhases(p *psState, e, nb int, h, hi *[psMaxBands]psGain) {
	var ipd, opd [psMaxBands]int8
	psRemapPhase(ipd[:m.nPh], p.ipd[e][:], p.nrIPDOPD, nb)
	psRemapPhase(opd[:m.nPh], p.opd[e][:], p.nrIPDOPD, nb)
	for b := 0; b < m.nPh; b++ {
		oi := psSmoothPhase(&m.hist[0][b], ipd[b])
		oo := psSmoothPhase(&m.hist[1][b], opd[b])
		rot := [psNGains]complex128{psGLs: oo, psGLd: oo, psGRs: oo * complex(real(oi), -imag(oi)), psGRd: oo * complex(real(oi), -imag(oi))}
		for i, r := range rot {
			re, im := h[b][i]*real(r), h[b][i]*imag(r)
			h[b][i], hi[b][i] = re, im
		}
	}
}

// bandPowers writes into pw the power of each parameter band of the slot s: the sum of
// |s[k]|² over its rows, added in row order, as a row by row loop does, but a run of
// rows of one band keeps its sum in a register.
func (dl *psDecLayout) bandPowers(pw *[psMaxBands]float64, s *[psMaxRows]complex128) {
	clear(pw[:dl.nb])
	for _, r := range dl.runs {
		sum := pw[r.band]
		for _, v := range s[r.start:r.end] {
			sum += real(v)*real(v) + imag(v)*imag(v)
		}
		pw[r.band] = sum
	}
}

// transient computes the attenuation gain of every parameter band and slot into m.tg
// (section 3.5) from the power of all rows of the band.
func (m *psMixer) transient(rows *[sbrQMFSlots][psMaxRows]complex128, dl *psDecLayout) {
	var pw [psMaxBands]float64
	for n := range rows {
		dl.bandPowers(&pw, &rows[n])
		for b := 0; b < dl.nb; b++ {
			pk := max(psPeakDecay*m.peak[b], pw[b])
			m.peak[b] = pk
			m.avg[b] += psSmooth * (pw[b] - m.avg[b])
			m.dev[b] += psSmooth * (pk - pw[b] - m.dev[b])
			g := 1.0
			if psImpact*m.dev[b] > m.avg[b] {
				g = m.avg[b] / (psImpact * m.dev[b])
			}
			m.tg[n][b] = g
		}
	}
}

// apply turns the mono hybrid rows of h into the left rows (in place) and the right rows
// (m.rr), for an applied frame; top is the SBR top kx+M.
func (m *psMixer) apply(h *psHybrid, p *psState, top int) {
	dl := psDecLayouts[0]
	if p.is34 {
		dl = psDecLayouts[1]
	}
	switch {
	case !m.started:
		m.started, m.is34 = true, p.is34
		m.reset()
	case m.is34 != p.is34:
		m.switchTo(p.is34)
	}
	m.phased = !m.baseline && p.enableIPDOPD
	m.nPh = psPhaseBandsOf(p.is34)
	m.clearAbove(dl, top)
	m.matrices(p, dl.nb)
	m.transient(&h.rows, dl)
	for k := 0; k < dl.rows; k++ {
		m.row(h, dl, k)
	}
}

// row decorrelates and mixes row k over the frame's slots.
func (m *psMixer) row(h *psHybrid, dl *psDecLayout, k int) {
	b := dl.band[k]
	in := &m.in[k]
	for n := range h.rows {
		in[psHist+n] = h.rows[n][k]
	}
	// the rows of the phase bands are all-pass rows (TestPSPhaseBandsAreAllPass): the delay
	// rows, QMF 23 and up, never carry phases
	phased := m.phased && int(b) < m.nPh && k < dl.nAP
	switch {
	case k < dl.nAP:
		ap := &dl.ap[k]
		w := &m.w[k]
		var d [sbrQMFSlots]complex128 // the decorrelated signal of a phased row, before the transient gain
		// the three links written out, the delays 3, 4 and 5 constants
		w0, w1, w2 := &w[0], &w[1], &w[2]
		phi, q0, q1, q2 := ap.phi, ap.q[0], ap.q[1], ap.q[2]
		cq0, cq1, cq2, c0, c1, c2 := ap.cq[0], ap.cq[1], ap.cq[2], ap.c[0], ap.c[1], ap.c[2]
		for n := range h.rows {
			x := phi * in[psHist+n-psPreDelay]
			old := w0[n]
			cur := x + cq0*old
			w0[3+n] = cur
			x = q0*old - complex(c0*real(cur), c0*imag(cur))
			old = w1[n]
			cur = x + cq1*old
			w1[4+n] = cur
			x = q1*old - complex(c1*real(cur), c1*imag(cur))
			old = w2[n]
			cur = x + cq2*old
			w2[5+n] = cur
			x = q2*old - complex(c2*real(cur), c2*imag(cur))
			if phased {
				d[n] = x
				continue
			}
			h.rows[n][k], m.rr[n][k] = psMixPair(in[psHist+n], x, &m.gain[n][b], m.tg[n][b])
		}
		for j := range w {
			copy(w[j][:psLinkDelay[j]], w[j][sbrQMFSlots:])
		}
		if phased {
			m.mixPhased(h, k, b, dl, &d)
		}
	case k < dl.nLong:
		for n := range h.rows {
			h.rows[n][k], m.rr[n][k] = psMixPair(in[psHist+n], in[psHist+n-psLongDelay], &m.gain[n][b], m.tg[n][b])
		}
	default:
		for n := range h.rows {
			h.rows[n][k], m.rr[n][k] = psMixPair(in[psHist+n], in[psHist+n-psShortDelay], &m.gain[n][b], m.tg[n][b])
		}
	}
	copy(in[:psHist], in[sbrQMFSlots:])
}

// mixPhased mixes row k of a phase band with the complex gains of the slot: the same
// l = gLs s + gLd d, r = gRs s + gRd d, with complex gains and the transient gain on d.
// A row of negative centre frequency (image) takes the conjugate gains.
func (m *psMixer) mixPhased(h *psHybrid, k int, b uint8, dl *psDecLayout, d *[sbrQMFSlots]complex128) {
	in := &m.in[k]
	sg := 1.0
	if dl.image[k] && !m.ffImage {
		sg = -1
	}
	ff := m.ffImage && dl.imageFF(k)
	for n := range h.rows {
		g, gi := &m.gain[n][b], m.gainIm[n][b]
		if ff {
			for i := range gi {
				gi[i] -= 2 * m.imStart[n][b][i] // minus the start, plus the end
			}
		}
		s, x := in[psHist+n], complex(m.tg[n][b], 0)*d[n]
		h.rows[n][k] = complex(g[psGLs], sg*gi[psGLs])*s + complex(g[psGLd], sg*gi[psGLd])*x
		m.rr[n][k] = complex(g[psGRs], sg*gi[psGRs])*s + complex(g[psGRd], sg*gi[psGRd])*x
	}
}

// imageFF reports whether ffmpeg flips row k (test switch ffImage): the rows of negative
// centre frequency, and at 34 bands also rows 12 and 13 (QMF band 1, positive centre).
func (dl *psDecLayout) imageFF(k int) bool {
	return dl.image[k] || dl.nb == 34 && k >= 9 && k <= 13
}

// psMixPair applies the transient gain t to the decorrelated sample x and returns the
// left and right samples for the direct sample s and the gains g. It is small enough
// to be inlined into the loops of row.
func psMixPair(s, x complex128, g *psGain, t float64) (l, r complex128) {
	dr, di := t*real(x), t*imag(x)
	return complex(g[psGLs]*real(s)+g[psGLd]*dr, g[psGLs]*imag(s)+g[psGLd]*di),
		complex(g[psGRs]*real(s)+g[psGRd]*dr, g[psGRs]*imag(s)+g[psGRd]*di)
}
