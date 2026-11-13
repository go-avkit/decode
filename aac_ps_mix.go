// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "math"

// Parametric Stereo decorrelation and mixing (HE-AAC v2), stages 2 to 5 of
// docs/aac-ps-design.md, written from docs/aac-ps-mixing.md sections 1 to 5 alone (a
// clean-room specification: no reference implementation was opened for this file).
// Procedure B and IPD/OPD (section 6) are not applied: the parser reads them and this
// file ignores them.
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
	ap               [psMaxAllPass]psAllPass
}

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
	return dl
}

// psMixer is the state of the decorrelator, the transient detector and the carried
// matrix of one channel element, and the scratch of a frame.
type psMixer struct {
	started bool
	is34    bool // layout of the last applied frame

	cur [psMaxBands]psGain // the matrix in force at the end of the last applied frame

	// transient detector, per parameter band
	peak, avg, dev [psMaxBands]float64

	// in[k] holds the last psHist slots of row k's input, then the current frame's;
	// w[k][m] the internal signal of link m of all-pass row k, its slot n at index
	// psLinkDelay[m]+n, the delayed values in front.
	in [psMaxRows][psHist + sbrQMFSlots]complex128
	w  [psMaxAllPass][3][psLinkSpan]complex128

	// the frame's scratch
	gain [sbrQMFSlots][psMaxBands]psGain
	tg   [sbrQMFSlots][psMaxBands]float64
	rr   [sbrQMFSlots][psMaxRows]complex128
	xr   [sbrQMFSlots][sbrSynBands]complex128
	syn  sbrSynthesis // the synthesis bank of the right channel
}

// reset clears the decorrelator and the transient detector (a layout switch).
func (m *psMixer) reset() {
	m.in = [psMaxRows][psHist + sbrQMFSlots]complex128{}
	m.w = [psMaxAllPass][3][psLinkSpan]complex128{}
	m.peak, m.avg, m.dev = [psMaxBands]float64{}, [psMaxBands]float64{}, [psMaxBands]float64{}
}

// switchTo remaps the carried matrix to the layout is34, by the value forms of the
// parameter maps, and resets the decorrelator.
func (m *psMixer) switchTo(is34 bool) {
	from, to := 20, 34
	if !is34 {
		from, to = 34, 20
	}
	var src, dst [psMaxBands]float64
	for i := range psNGains {
		for b := 0; b < from; b++ {
			src[b] = m.cur[b][i]
		}
		psRemapBands(dst[:], src[:], from, to)
		for b := 0; b < to; b++ {
			m.cur[b][i] = dst[b]
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
// keeps the matrix of the last envelope. A frame without ps_data holds the carried
// matrix over its single closing envelope.
func (m *psMixer) matrices(p *psState, fresh bool, nb int) {
	numEnv := 1
	border := [psMaxEnv + 1]int8{-1, sbrQMFSlots - 1}
	if fresh {
		numEnv, border = p.numEnv, p.border
	}
	prev := m.cur
	for e := 1; e <= numEnv; e++ {
		next := prev
		if fresh {
			m.envelopeGains(p, e-1, nb, &next)
		}
		b0, b1 := int(border[e-1]), int(border[e])
		for n := b0 + 1; n <= b1; n++ {
			f := float64(n-b0) / float64(b1-b0)
			for b := 0; b < nb; b++ {
				if n == b1 {
					m.gain[n][b] = next[b]
					continue
				}
				for i := range psNGains {
					m.gain[n][b][i] = prev[b][i] + f*(next[b][i]-prev[b][i])
				}
			}
		}
		prev = next
	}
	m.cur = prev
}

// envelopeGains fills h with the gains of envelope e: its indices mapped to the nb
// bands of the layout, through the tables of section 4.1.
func (m *psMixer) envelopeGains(p *psState, e, nb int, h *[psMaxBands]psGain) {
	var iid, icc [psMaxBands]int8
	if p.enableIID {
		psRemapBands(iid[:], p.iid[e][:], p.nrIID, nb)
	}
	if p.enableICC {
		psRemapBands(icc[:], p.icc[e][:], p.nrICC, nb)
	}
	for b := 0; b < nb; b++ {
		h[b] = psMixGain(int(iid[b]), int(icc[b]), p.iidQuant)
	}
}

// transient computes the attenuation gain of every parameter band and slot into m.tg
// (section 3.5) from the power of all rows of the band.
func (m *psMixer) transient(rows *[sbrQMFSlots][psMaxRows]complex128, dl *psDecLayout) {
	var pw [psMaxBands]float64
	for n := range rows {
		clear(pw[:dl.nb])
		for k, v := range rows[n][:dl.rows] {
			pw[dl.band[k]] += real(v)*real(v) + imag(v)*imag(v)
		}
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
// (m.rr), for an applied frame. fresh says the frame carried ps_data, top is the SBR top
// kx+M.
func (m *psMixer) apply(h *psHybrid, p *psState, fresh bool, top int) {
	dl := psDecLayouts[0]
	if p.is34 {
		dl = psDecLayouts[1]
	}
	switch {
	case !m.started:
		m.started, m.is34 = true, p.is34
	case m.is34 != p.is34:
		m.switchTo(p.is34)
	}
	m.clearAbove(dl, top)
	m.matrices(p, fresh, dl.nb)
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
	switch {
	case k < dl.nAP:
		ap := &dl.ap[k]
		w := &m.w[k]
		for n := range h.rows {
			x := ap.phi * in[psHist+n-psPreDelay]
			for j := range w {
				old := w[j][n]
				cur := x + ap.cq[j]*old
				w[j][psLinkDelay[j]+n] = cur
				x = ap.q[j]*old - complex(ap.c[j]*real(cur), ap.c[j]*imag(cur))
			}
			m.mixSlot(h, k, n, b, x)
		}
		for j := range w {
			copy(w[j][:psLinkDelay[j]], w[j][sbrQMFSlots:])
		}
	case k < dl.nLong:
		for n := range h.rows {
			m.mixSlot(h, k, n, b, in[psHist+n-psLongDelay])
		}
	default:
		for n := range h.rows {
			m.mixSlot(h, k, n, b, in[psHist+n-psShortDelay])
		}
	}
	copy(in[:psHist], in[sbrQMFSlots:])
}

// mixSlot applies the transient gain to the decorrelated sample x and writes the left
// and right samples of row k at slot n.
func (m *psMixer) mixSlot(h *psHybrid, k, n int, b uint8, x complex128) {
	s := h.rows[n][k]
	g := &m.gain[n][b]
	t := m.tg[n][b]
	dr, di := t*real(x), t*imag(x)
	h.rows[n][k] = complex(g[psGLs]*real(s)+g[psGLd]*dr, g[psGLs]*imag(s)+g[psGLd]*di)
	m.rr[n][k] = complex(g[psGRs]*real(s)+g[psGRd]*dr, g[psGRs]*imag(s)+g[psGRd]*di)
}
