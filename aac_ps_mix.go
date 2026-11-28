// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"unsafe"

	"github.com/go-simd/floats"
)

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
	psPreDelay     = 2
	psLongDelay    = 14
	psShortDelay   = 1
	psDecaySlope   = 0.05
	psAllPassQMF   = 23 // QMF bands below this use the all-pass chain
	psLongEndQMF   = 35 // and from here on the short delay
	psHist         = psLongDelay
	psMaxAllPass   = 50 // all-pass rows of the 34-band layout
	psLinkMaxDelay = 5  // the longest of the link delays 3, 4 and 5
	psPreFracMil   = 390
	psAngleMilDen  = 24 * 1000 * 2 // angle pi*delta*f, with f in 24ths and delta in thousandths, per turn of 2 pi
)

var psLinkFracMil = [3]int{430, 750, 347}

// psAllPass holds the constants of one all-pass row: the fractional pre-delay rotation
// phi, and for each link the rotation q, the coefficient c = a*gamma and c*q.
type psAllPass struct {
	phi complex128
	q   [3]complex128
	c   [3]float64
}

// psDecLayout is the row classes and the all-pass constants of a layout.
type psDecLayout struct {
	rows, nAP, nLong int
	nb               int
	qmf              [psMaxRows]uint8
	band             []uint8
	runs             []psBandRun // the rows grouped by consecutive equal band, in row order
	ap               [psMaxAllPass]psAllPass
	apRows           floats.AllPassRows   // the constants of ap as one table per field, for floats.AllPass3
	image            [psMaxRows]bool      // rows of negative centre frequency: they take the conjugate phases (section 6.2)
	apRows32         floats.AllPassRows32 // the same constants in float32, for floats.AllPass3Split32
	segs             []psMixSeg           // runs split by the source of the decorrelated signal
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
		}
	}
	rows := &dl.apRows
	rows.Phi, rows.Q0, rows.Q1, rows.Q2 = make([]complex128, dl.nAP), make([]complex128, dl.nAP), make([]complex128, dl.nAP), make([]complex128, dl.nAP)
	rows.C0, rows.C1, rows.C2 = make([]float64, dl.nAP), make([]float64, dl.nAP), make([]float64, dl.nAP)
	for r := 0; r < dl.nAP; r++ {
		a := &dl.ap[r]
		rows.Phi[r] = a.phi
		rows.Q0[r], rows.Q1[r], rows.Q2[r] = a.q[0], a.q[1], a.q[2]
		rows.C0[r], rows.C1[r], rows.C2[r] = a.c[0], a.c[1], a.c[2]
	}
	for r := 0; r < dl.rows; r++ {
		if r > 0 && dl.band[r] == dl.band[r-1] {
			dl.runs[len(dl.runs)-1].end++
			continue
		}
		dl.runs = append(dl.runs, psBandRun{uint8(r), uint8(r + 1), dl.band[r]})
	}
	dl.initStage32()
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

	// The delay lines, as rings over the slots, so that they do not grow with the frame:
	// in[t%(psHist+1)] holds the input rows of slot t (the last psHist slots are the
	// delays of the rows that delay); w[m][t%(psLinkDelay[m]+1)] the internal signal of
	// link m of the all-pass rows at slot t. tick is the slot count, which only the
	// positions in the rings depend on.
	in   [psHist + 1][psMaxRows]complex128
	w    [3][psLinkMaxDelay + 1][psMaxAllPass]complex128
	tick int

	// the float32 stage's rings and plan (aac_ps_mix32.go); the complex128 ones above and
	// below are the oracle's
	st32 psStage32

	// the proved memory of the all-pass rows (allPassPlan) and its output
	plan     *floats.AllPass3Plan
	planFor  *psDecLayout
	planSelf *psMixer
	apOut    [psMaxAllPass]complex128

	// the frame's scratch
	gain, gainIm [sbrQMFSlots][psMaxBands]psGain
	imStart      [sbrQMFSlots][psMaxBands]psGain // ffImage only: the start's share of gainIm
	tg           [sbrQMFSlots][psMaxBands]float64
	rr           [sbrQMFSlots][psMaxRows]complex128
	xr           [sbrQMFSlots][sbrSynBands]complex128
	syn          sbrSynthesis // the synthesis bank of the right channel
}

// reset clears the decorrelator and the transient detector (a layout switch).
func (m *psMixer) reset() {
	m.in = [psHist + 1][psMaxRows]complex128{}
	m.w = [3][psLinkMaxDelay + 1][psMaxAllPass]complex128{}
	m.st32.reset()
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
	// the QMF band of a row never decreases (TestPSRowsBelow): the rows at or above top
	// are the trailing ones, from kt. Every position of a ring is cleared: the one that
	// the first slot overwrites is not read.
	kt := dl.rows
	for k := 0; k < dl.rows; k++ {
		if int(dl.qmf[k]) >= top {
			kt = k
			break
		}
	}
	m.st32.clearAbove(dl, kt)
	for s := range m.in {
		clear(m.in[s][kt:dl.rows])
	}
	if kt < dl.nAP {
		for j := range m.w {
			for s := range m.w[j] {
				clear(m.w[j][s][kt:dl.nAP])
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
	for n := range rows {
		m.transientSlot(n, &rows[n], dl)
	}
}

// transientSlot is transient for slot n, the rows of which are s; the slots come in order.
func (m *psMixer) transientSlot(n int, s *[psMaxRows]complex128, dl *psDecLayout) {
	var pw [psMaxBands]float64
	dl.bandPowers(&pw, s)
	m.transientBands(n, &pw, dl.nb)
}

// transientBands is the detector of slot n from the powers pw of the nb parameter bands.
func (m *psMixer) transientBands(n int, pw *[psMaxBands]float64, nb int) {
	for b := 0; b < nb; b++ {
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

// begin starts a frame: the layout (a switch resets the decorrelator), the phase mode,
// the past of the rows above the SBR top, and the matrices of the frame's slots. It
// returns the layout.
func (m *psMixer) begin(p *psState, top int) *psDecLayout {
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
	return dl
}

// allPassPlan is the proved memory of the all-pass rows of layout dl: the input ring, the
// three link rings and the output apOut of this mixer, proved disjoint once (floats.AllPass3Plan).
// It is rebuilt when the layout changes, or when the mixer is a copy of another one (a plan
// proves the memory of the mixer it was built for, not of a copy's).
func (m *psMixer) allPassPlan(dl *psDecLayout) *floats.AllPass3Plan {
	if m.plan != nil && m.planFor == dl && m.planSelf == m {
		return m.plan
	}
	var w [3][]complex128
	var ws [3]int
	for l := range w {
		w[l] = unsafe.Slice(&m.w[l][0][0], (psLinkDelay[l]+1)*psMaxAllPass)
		ws[l] = psMaxAllPass
	}
	in := unsafe.Slice(&m.in[0][0], (psHist+1)*psMaxRows)
	p, err := floats.NewAllPass3TickPlan(&dl.apRows, m.apOut[:dl.nAP], in, psMaxRows, w, ws, psPreDelay)
	if err != nil {
		panic(err) // the layout tables and the mixer's arrays are fixed: a refusal is a bug
	}
	m.plan, m.planFor, m.planSelf = p, dl, m
	return p
}

// step decorrelates and mixes slot n: rows holds the hybrid rows of the slot and is
// overwritten with the left rows, rr receives the right rows. The slots of a frame come in
// order, and each leaves its input and its link signals in the rings for the ones after.
func (m *psMixer) step(n int, rows, rr *[psMaxRows]complex128, dl *psDecLayout) {
	t := m.tick
	const ring = psHist + 1
	cur := t % ring
	copy(m.in[cur][:dl.rows], rows[:dl.rows])
	s := &m.in[cur]
	nAP := dl.nAP
	// the all-pass rows: the three links along the rows of the slot, which are independent
	var dec *[psMaxAllPass]complex128
	if nAP > 0 {
		m.allPassPlan(dl).RunTick(t)
		dec = &m.apOut
	}
	// the mixing: the decorrelated signal of an all-pass row, the delayed input of a delay row
	gn, tn := &m.gain[n], &m.tg[n]
	for k := 0; k < nAP; k++ {
		b := dl.band[k]
		if m.phased && int(b) < m.nPh {
			// the rows of the phase bands are all-pass rows (TestPSPhaseBandsAreAllPass): the delay
			// rows, QMF 23 and up, never carry phases
			rows[k], rr[k] = m.mixPhased(n, k, b, dl, s[k], dec[k])
			continue
		}
		rows[k], rr[k] = psMixPair(s[k], dec[k], &gn[b], tn[b])
	}
	if dl.nLong > nAP {
		d := &m.in[(t+ring-psLongDelay)%ring]
		for k := nAP; k < dl.nLong; k++ {
			b := dl.band[k]
			rows[k], rr[k] = psMixPair(s[k], d[k], &gn[b], tn[b])
		}
	}
	d := &m.in[(t+ring-psShortDelay)%ring]
	for k := max(dl.nLong, nAP); k < dl.rows; k++ {
		b := dl.band[k]
		rows[k], rr[k] = psMixPair(s[k], d[k], &gn[b], tn[b])
	}
	m.tick++
}

// mixPhased mixes row k of a phase band at slot n with the complex gains of the slot: the
// same l = gLs s + gLd d, r = gRs s + gRd d, with complex gains and the transient gain on
// d. A row of negative centre frequency (image) takes the conjugate gains.
func (m *psMixer) mixPhased(n, k int, b uint8, dl *psDecLayout, s, d complex128) (l, r complex128) {
	sg := 1.0
	if dl.image[k] && !m.ffImage {
		sg = -1
	}
	g, gi := &m.gain[n][b], m.gainIm[n][b]
	if m.ffImage && dl.imageFF(k) {
		for i := range gi {
			gi[i] -= 2 * m.imStart[n][b][i] // minus the start, plus the end
		}
	}
	x := complex(m.tg[n][b], 0) * d
	l = complex(g[psGLs], sg*gi[psGLs])*s + complex(g[psGLd], sg*gi[psGLd])*x
	r = complex(g[psGRs], sg*gi[psGRs])*s + complex(g[psGRd], sg*gi[psGRd])*x
	return l, r
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
