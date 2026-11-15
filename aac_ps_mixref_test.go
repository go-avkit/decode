// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"math/big"
)

// The independent reference of docs/aac-ps-mixing.md section 7.2, written from that
// document and structured differently from the decoder: the decorrelator is the
// convolution of the input with the closed-form impulse response of the all-pass chain
// (not the recursion), in double-double arithmetic; the transient gain and the gains of
// the mixing matrix are evaluated in math/big at 192 bits; the interpolation is the
// closed form. It shares with the decoder only the declared normative values: the IID
// dB steps, the parameter maps, the band tables and the constants of section 3.

// dd is a double-double: hi + lo with |lo| <= ulp(hi)/2, about 106 bits.
type dd struct{ hi, lo float64 }

func twoSum(a, b float64) (s, e float64) {
	s = a + b
	bb := s - a
	return s, (a - (s - bb)) + (b - bb)
}

func (a dd) add(b dd) dd {
	s, e := twoSum(a.hi, b.hi)
	e += a.lo + b.lo
	hi := s + e
	return dd{hi, e - (hi - s)}
}

func (a dd) neg() dd { return dd{-a.hi, -a.lo} }

func (a dd) mul(b dd) dd {
	p := a.hi * b.hi
	e := math.FMA(a.hi, b.hi, -p)
	e += a.hi*b.lo + a.lo*b.hi
	hi := p + e
	return dd{hi, e - (hi - p)}
}

func ddFromBig(x *big.Float) dd {
	hi, _ := x.Float64()
	r := new(big.Float).SetPrec(psRefPrec).Sub(x, new(big.Float).SetPrec(psRefPrec).SetFloat64(hi))
	lo, _ := r.Float64()
	return dd{hi, lo}
}

// cdd is a complex double-double.
type cdd struct{ re, im dd }

func (a cdd) add(b cdd) cdd { return cdd{a.re.add(b.re), a.im.add(b.im)} }
func (a cdd) mul(b cdd) cdd {
	return cdd{a.re.mul(b.re).add(a.im.mul(b.im).neg()), a.re.mul(b.im).add(a.im.mul(b.re))}
}
func (a cdd) scale(g dd) cdd { return cdd{a.re.mul(g), a.im.mul(g)} }
func cddOf(z complex128) cdd { return cdd{dd{real(z), 0}, dd{imag(z), 0}} }

// diff is |dec - ref| as a float64.
func (a cdd) diff(dec complex128) float64 {
	return math.Hypot(a.re.add(dd{-real(dec), 0}).hi, a.im.add(dd{-imag(dec), 0}).hi)
}

func bf(x float64) *big.Float { return new(big.Float).SetPrec(psRefPrec).SetFloat64(x) }
func bi(x int64) *big.Float   { return new(big.Float).SetPrec(psRefPrec).SetInt64(x) }
func bnew() *big.Float        { return new(big.Float).SetPrec(psRefPrec) }
func bmul(a, b *big.Float) *big.Float {
	return bnew().Mul(a, b)
}
func badd(a, b *big.Float) *big.Float { return bnew().Add(a, b) }
func bsub(a, b *big.Float) *big.Float { return bnew().Sub(a, b) }
func bquo(a, b *big.Float) *big.Float { return bnew().Quo(a, b) }
func bsqrt(a *big.Float) *big.Float   { return bnew().Sqrt(a) }
func bdec(s string) *big.Float {
	x, _, err := big.ParseFloat(s, 10, psRefPrec, big.ToNearestEven)
	if err != nil {
		panic(err)
	}
	return x
}

// bigReduce returns x modulo 2 pi, in [-pi, pi].
func bigReduce(x *big.Float) *big.Float {
	pi := bigPi()
	twoPi := badd(pi, pi)
	q, _ := bquo(x, twoPi).Float64()
	return bsub(x, bmul(twoPi, bf(math.Round(q))))
}

// bigExp is exp(x) by its Taylor series, for |x| below 4.
func bigExp(x *big.Float) *big.Float {
	sum, term := bi(1), bi(1)
	for k := int64(1); k < 120; k++ {
		term = bquo(bmul(term, x), bi(k))
		sum = badd(sum, term)
	}
	return sum
}

// bigRoot is the k-th root of a positive a by Newton's method from a float64 start.
func bigRoot(a *big.Float, k int) *big.Float {
	f, _ := a.Float64()
	x := bf(math.Pow(f, 1/float64(k)))
	for i := 0; i < 6; i++ {
		xk1 := bi(1)
		for j := 0; j < k-1; j++ {
			xk1 = bmul(xk1, x)
		}
		xk := bmul(xk1, x)
		x = bsub(x, bquo(bsub(xk, a), bmul(bi(int64(k)), xk1)))
	}
	return x
}

// bigAcos is arccos(r) for a decimal r in [-1, 1], by Newton's method on cos.
func bigAcos(r *big.Float) *big.Float {
	switch r.Cmp(bi(1)) {
	case 0:
		return bnew()
	}
	if r.Cmp(bi(-1)) == 0 {
		return bigPi()
	}
	f, _ := r.Float64()
	x := bf(math.Acos(f))
	for i := 0; i < 6; i++ {
		s, c := bigSinCos(x)
		x = badd(x, bquo(bsub(c, r), s))
	}
	return x
}

// bigAtan is arctan(y) by Newton's method on sin(x) - y cos(x).
func bigAtan(y *big.Float) *big.Float {
	f, _ := y.Float64()
	x := bf(math.Atan(f))
	for i := 0; i < 6; i++ {
		s, c := bigSinCos(x)
		num := bsub(s, bmul(y, c))
		den := badd(c, bmul(y, s))
		x = bsub(x, bquo(num, den))
	}
	return x
}

// The ICC values as the decimals the specification gives, typed again here.
var psRefICC = [8]string{"1", "0.937", "0.84118", "0.60092", "0.36764", "0", "-0.589", "-1"}

// psRefGains are the four gains of procedure A for an IID in dB and an ICC index, from
// the definitions, at 192 bits. eq7 selects the exact beta (eq. 7) instead of eq. 8.
func psRefGains(db, icc int, eq7 bool) [4]*big.Float {
	// c = 10^(db/20): the 20th root of 10^|db|, inverted for a negative db
	a := bnew().SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(max(db, -db))), nil))
	c := bigRoot(a, 20)
	if db < 0 {
		c = bquo(bi(1), c)
	}
	sqrt2 := bsqrt(bi(2))
	cR := bquo(sqrt2, bsqrt(badd(bi(1), bmul(c, c))))
	cL := bmul(c, cR)
	alpha := bquo(bigAcos(bdec(psRefICC[icc])), bi(2))
	var beta *big.Float
	if eq7 {
		sa, ca := bigSinCos(alpha)
		y := bmul(bquo(sa, ca), bquo(bsub(cR, cL), badd(cR, cL)))
		beta = bigAtan(y)
	} else {
		beta = bquo(bmul(alpha, bsub(cR, cL)), sqrt2)
	}
	sp, cp := bigSinCos(badd(beta, alpha))
	sm, cm := bigSinCos(bsub(beta, alpha))
	return [4]*big.Float{bmul(cL, cp), bmul(cL, sp), bmul(cR, cm), bmul(cR, sm)}
}

// psRefGainsB are the four gains of procedure B (section 6.1) for an IID in dB and an ICC
// index, at 192 bits, from a numerical eigendecomposition of the target covariance (the
// decoder evaluates the closed form): the covariance of the two mixed channels has the
// powers c_L^2 and c_R^2 (summing to 2) and the correlation rho' = max(rho, 0.05); one
// Jacobi rotation diagonalises it; the major axis, in the first quadrant, carries the
// mono signal and the minor one the decorrelated signal.
func psRefGainsB(db, icc int) [4]*big.Float {
	a := bnew().SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(max(db, -db))), nil))
	c := bigRoot(a, 20)
	if db < 0 {
		c = bquo(bi(1), c)
	}
	cR := bquo(bsqrt(bi(2)), bsqrt(badd(bi(1), bmul(c, c))))
	cL := bmul(c, cR)
	rho := bdec(psRefICC[icc])
	if floor := bdec("0.05"); rho.Cmp(floor) < 0 {
		rho = floor
	}
	c11, c22, c12 := bmul(cL, cL), bmul(cR, cR), bmul(bmul(cL, cR), rho)
	tau := bquo(bsub(c22, c11), bmul(bi(2), c12))
	t := bquo(bi(1), badd(bnew().Abs(tau), bsqrt(badd(bi(1), bmul(tau, tau)))))
	if tau.Sign() < 0 {
		t.Neg(t)
	}
	cs := bquo(bi(1), bsqrt(badd(bi(1), bmul(t, t))))
	sn := bmul(t, cs)
	l1, l2 := bsub(c11, bmul(t, c12)), badd(c22, bmul(t, c12)) // eigenvalues of (cs, -sn) and (sn, cs)
	major := [2]*big.Float{cs, bnew().Neg(sn)}
	lM, lm := l1, l2
	if l2.Cmp(l1) > 0 {
		major, lM, lm = [2]*big.Float{sn, cs}, l2, l1
	}
	if major[0].Sign() < 0 || major[0].Sign() == 0 && major[1].Sign() < 0 {
		major = [2]*big.Float{bnew().Neg(major[0]), bnew().Neg(major[1])}
	}
	if lm.Sign() < 0 {
		lm = bnew()
	}
	sM, sm := bsqrt(lM), bsqrt(lm)
	u1, u2 := major[0], major[1]
	return [4]*big.Float{bmul(u1, sM), bnew().Neg(bmul(u2, sm)), bmul(u2, sM), bmul(u1, sm)}
}

// bigAtan2 is the argument of x + iy in (-pi, pi].
func bigAtan2(y, x *big.Float) *big.Float {
	switch {
	case x.Sign() > 0:
		return bigAtan(bquo(y, x))
	case x.Sign() < 0:
		a := bigAtan(bquo(y, x))
		if y.Sign() >= 0 {
			return badd(a, bigPi())
		}
		return bsub(a, bigPi())
	}
	half := bquo(bigPi(), bi(2))
	if y.Sign() < 0 {
		half.Neg(half)
	}
	return half
}

// A cbig is a complex number at 192 bits.
type cbig struct{ re, im *big.Float }

func (a cbig) mul(b cbig) cbig {
	return cbig{bsub(bmul(a.re, b.re), bmul(a.im, b.im)), badd(bmul(a.re, b.im), bmul(a.im, b.re))}
}
func (a cbig) conj() cbig { return cbig{a.re, bnew().Neg(a.im)} }

// psRefPhasor is e^{j k pi/4}.
func psRefPhasor(k int) cbig {
	s, c := bigSinCos(bquo(bmul(bigPi(), bi(int64(k))), bi(4)))
	return cbig{c, s}
}

// psRefCentre24 are the centre frequencies of the all-pass rows in 24ths of a QMF band:
// the split rows as the specification lists them (section 3.3), then q + 1/2.
var psRefCentre24 = [2][]int{
	{-9, -3, 3, 9, 15, 21, 30, 42, 54, 66},
	{2, 6, 10, 14, 18, 22, 26, 30, 34, -10, -6, -2, 51, 57, 15, 21, 27, 33, 39, 45, 54, 66, 78, 42, 102, 66, 78, 90, 102, 114, 126, 90},
}

const psRefLen = 600 // slots of the truncated impulse response

// psRefRotation is e^{-j pi delta f} for delta in thousandths and f in 24ths.
func psRefRotation(deltaMil, f24 int) (re, im *big.Float) {
	x := bquo(bmul(bigPi(), bi(int64(deltaMil*f24))), bi(24000))
	s, c := bigSinCos(bigReduce(x))
	return c, bnew().Neg(s)
}

// psRefImpulse is the impulse response of the all-pass row (rowQ is its QMF band, split
// its being a split row, f24 its centre) over psRefLen slots: the pre-delay and
// rotation times the three link series of section 3.2 convolved.
func psRefImpulse(q, q1 int, split bool, f24 int) []cdd {
	gamma := bi(1)
	if !split {
		gamma = bsub(bi(1), bquo(bi(int64(q-q1)), bi(20)))
	}
	pr, pi := psRefRotation(390, f24)
	cur := make([]cdd, psRefLen)
	cur[0] = cdd{dd{1, 0}, dd{}}
	for m, d := range psLinkDelay {
		a := bigExp(bnew().Neg(bquo(bi(int64(d)), bi(7))))
		c := bmul(a, gamma)
		qr, qi := psRefRotation([]int{430, 750, 347}[m], f24)
		series := make([]cdd, psRefLen)
		series[0] = cdd{ddFromBig(bnew().Neg(c)), dd{}}
		g := bsub(bi(1), bmul(c, c))
		pw, pre, pim := bi(1), bi(1), bnew()
		for i := 1; i*d < psRefLen; i++ {
			pre, pim = bsub(bmul(pre, qr), bmul(pim, qi)), badd(bmul(pre, qi), bmul(pim, qr))
			coef := bmul(g, pw)
			series[i*d] = cdd{ddFromBig(bmul(coef, pre)), ddFromBig(bmul(coef, pim))}
			pw = bmul(pw, c)
		}
		next := make([]cdd, psRefLen)
		for i, hi := range series {
			if hi == (cdd{}) {
				continue
			}
			for j := 0; i+j < psRefLen; j++ {
				if cur[j] == (cdd{}) {
					continue
				}
				next[i+j] = next[i+j].add(hi.mul(cur[j]))
			}
		}
		cur = next
	}
	phi := cdd{ddFromBig(pr), ddFromBig(pi)}
	out := make([]cdd, psRefLen)
	for n := psPreDelay; n < psRefLen; n++ {
		out[n] = cur[n-psPreDelay].mul(phi)
	}
	return out
}

// psRefMix is the reference's state across frames: the whole input of every row, the
// transient detector, and the matrix in force.
type psRefMix struct {
	eq7     bool
	started bool
	is34    bool
	n       int                     // slots seen since the last reset
	s       [psMaxRows][]complex128 // input by row, all slots so far
	peak    [psMaxBands]*big.Float
	avg     [psMaxBands]*big.Float
	dev     [psMaxBands]*big.Float
	store   [psMaxEnv + 1][psMaxBands][4]*big.Float // the matrices of the last frame by envelope
	storeIm [psMaxEnv + 1][psMaxBands][4]*big.Float // their imaginary parts (IPD/OPD)
	hist    [2][psMaxBands][2]cbig                  // the phasors of the last two envelopes of IPD and OPD, by phase band
	imp     map[[2]int][]cdd
	gains   map[[3]int][4]*big.Float
}

func newPSRefMix(eq7 bool) *psRefMix {
	r := &psRefMix{eq7: eq7, imp: map[[2]int][]cdd{}, gains: map[[3]int][4]*big.Float{}}
	r.resetTransient()
	r.resetPhases()
	for e := range r.store {
		for b := range r.store[e] {
			for i := range r.store[e][b] {
				r.store[e][b][i], r.storeIm[e][b][i] = bnew(), bnew()
			}
		}
	}
	return r
}

func (r *psRefMix) resetTransient() {
	for b := range r.peak {
		r.peak[b], r.avg[b], r.dev[b] = bnew(), bnew(), bnew()
	}
}

// resetPhases sets the history of the phase smoothing to angle 0.
func (r *psRefMix) resetPhases() {
	for i := range r.hist {
		for b := range r.hist[i] {
			r.hist[i][b] = [2]cbig{{bi(1), bnew()}, {bi(1), bnew()}}
		}
	}
}

// smooth pushes the phase index k into the history of (parameter, band) and returns the
// unit phasor of the smoothed phase: the argument of 0.25 u[e-2] + 0.5 u[e-1] + u[e],
// taken with an arctangent and put back through sine and cosine.
func (r *psRefMix) smooth(param, band, k int) cbig {
	h := &r.hist[param][band]
	u := psRefPhasor(k)
	sum := cbig{
		badd(badd(bmul(bdec("0.25"), h[1].re), bmul(bdec("0.5"), h[0].re)), u.re),
		badd(badd(bmul(bdec("0.25"), h[1].im), bmul(bdec("0.5"), h[0].im)), u.im),
	}
	h[1], h[0] = h[0], u
	s, c := bigSinCos(bigAtan2(sum.im, sum.re))
	return cbig{c, s}
}

func (r *psRefMix) gain(iid, icc int, fine, procB bool) [4]*big.Float {
	var db int
	if fine {
		db = int(psIIDdBFine[iid+15])
	} else {
		db = int(psIIDdBCoarse[iid+7])
	}
	key := [3]int{db, icc, 0}
	if procB {
		key[2] = 1
	}
	if g, ok := r.gains[key]; ok {
		return g
	}
	var g [4]*big.Float
	if procB {
		g = psRefGainsB(db, icc)
	} else {
		g = psRefGains(db, icc, r.eq7)
	}
	r.gains[key] = g
	return g
}

// phaseIndices maps the n IPD or OPD indices of envelope e to the nPh phase bands of the
// layout of nb parameter bands, by the maps of section 4.2 restricted to the phase bands
// (section 6.2): 5 -> 11 and 5 -> 17 from the 10-band maps with the missing band 0, 17 ->
// 11 and 11 -> 17 from the 34 -> 20 and 20 -> 34 maps.
func psRefPhaseIndices(src []int8, n, nb, nPh int) []int {
	out := make([]int, nPh)
	switch {
	case n == 5:
		var mid [10]int8
		copy(mid[:], src[:5])
		var full [psMaxBands]int8
		psRemapBands(full[:], mid[:], 10, nb)
		for b := range out {
			out[b] = int(full[b])
		}
	case n == 11 && nb == 20 || n == 17 && nb == 34:
		for b := range out {
			out[b] = int(src[b])
		}
	case n == 17:
		var wide [34]int8
		copy(wide[:], src[:17])
		var full [20]int8
		psRemapBands(full[:], wide[:], 34, 20)
		for b := range out {
			out[b] = int(full[b])
		}
	case n == 11:
		var wide [20]int8
		copy(wide[:], src[:11])
		var full [34]int8
		psRemapBands(full[:], wide[:], 20, 34)
		for b := range out {
			out[b] = int(full[b])
		}
	}
	return out
}

// psRefOut is one frame of the reference's output: the left and right rows.
type psRefOut struct{ l, r [sbrQMFSlots][psMaxRows]cdd }

// frame processes one applied frame: s are its mono rows, p the parameters in force
// (those of the last parsed frame when this one had no ps_data), top the SBR top.
func (r *psRefMix) frame(p *psState, top int, s *[sbrQMFSlots][psMaxRows]complex128) *psRefOut {
	dl := psDecLayouts[0]
	q1, nSplit := 3, 10
	if p.is34 {
		dl, q1, nSplit = psDecLayouts[1], 5, 32
	}
	nb := dl.nb
	if r.started && r.is34 != p.is34 {
		for k := range r.s {
			r.s[k] = nil
		}
		r.n = 0
		r.resetTransient()
		to := 34
		if !p.is34 {
			to = 20
		}
		r.resetPhases()
		for _, store := range []*[psMaxEnv + 1][psMaxBands][4]*big.Float{&r.store, &r.storeIm} {
			for en := range store {
				var next [psMaxBands][4]*big.Float
				for i := 0; i < 4; i++ {
					for b := 0; b < to; b++ {
						e := psMap20to34[b]
						if !p.is34 {
							e = psMap34to20[b]
						}
						sum := bnew()
						for j := range e.src {
							if e.w[j] != 0 {
								sum = badd(sum, bmul(bi(int64(e.w[j])), store[en][e.src[j]][i]))
							}
						}
						next[b][i] = bquo(sum, bi(int64(e.div)))
					}
				}
				for b := to; b < psMaxBands; b++ {
					for i := range next[b] {
						next[b][i] = bnew()
					}
				}
				store[en] = next
			}
		}
	}
	r.started, r.is34 = true, p.is34
	start := r.n
	r.n += sbrQMFSlots
	for k := 0; k < dl.rows; k++ {
		if r.s[k] == nil {
			r.s[k] = make([]complex128, start)
		}
		if int(dl.qmf[k]) >= top {
			clear(r.s[k])
		}
		for n := range s {
			r.s[k] = append(r.s[k], s[n][k])
		}
	}
	// the matrices of every slot, as big floats
	numEnv, border := p.numEnv, p.border
	var gain, gainIm [sbrQMFSlots][psMaxBands][4]dd
	phased := p.enableIPDOPD
	nPh := 11
	if p.is34 {
		nPh = 17
	}
	procB := p.enableICC && p.iccMode >= 3
	prev, prevIm := r.store[p.numEnvOld], r.storeIm[p.numEnvOld]
	r.store[0] = prev
	if phased {
		r.storeIm[0] = prevIm
	}
	for e := 1; e <= numEnv; e++ {
		next := prev
		nextIm := r.storeIm[0] // the imaginary parts of this envelope: zero unless a phase band
		for b := range nextIm {
			for i := range nextIm[b] {
				nextIm[b][i] = bnew()
			}
		}
		var iid, icc [psMaxBands]int8
		if p.enableIID {
			psRemapBands(iid[:], p.iid[e-1][:], p.nrIID, nb)
		}
		if p.enableICC {
			psRemapBands(icc[:], p.icc[e-1][:], p.nrICC, nb)
		}
		for b := 0; b < nb; b++ {
			next[b] = r.gain(int(iid[b]), int(icc[b]), p.iidQuant, procB)
		}
		if phased {
			ipd := psRefPhaseIndices(p.ipd[e-1][:], p.nrIPDOPD, nb, nPh)
			opd := psRefPhaseIndices(p.opd[e-1][:], p.nrIPDOPD, nb, nPh)
			for b := 0; b < nPh; b++ {
				oi, oo := r.smooth(0, b, ipd[b]), r.smooth(1, b, opd[b])
				orot := oo.mul(oi.conj())
				rot := [4]cbig{oo, oo, orot, orot}
				for i := 0; i < 4; i++ {
					re, im := bmul(next[b][i], rot[i].re), bmul(next[b][i], rot[i].im)
					next[b][i], nextIm[b][i] = re, im
				}
			}
		}
		r.store[e] = next
		if phased {
			r.storeIm[e] = nextIm
		}
		b0, b1 := int(border[e-1]), int(border[e])
		for n := b0 + 1; n <= b1; n++ {
			frac := bquo(bi(int64(n-b0)), bi(int64(b1-b0)))
			for b := 0; b < nb; b++ {
				for i := 0; i < 4; i++ {
					v := badd(prev[b][i], bmul(frac, bsub(next[b][i], prev[b][i])))
					gain[n][b][i] = ddFromBig(v)
					if phased && b < nPh {
						vi := badd(prevIm[b][i], bmul(frac, bsub(nextIm[b][i], prevIm[b][i])))
						gainIm[n][b][i] = ddFromBig(vi)
					}
				}
			}
		}
		prev, prevIm = next, nextIm
	}
	// the transient gain
	var tg [sbrQMFSlots][psMaxBands]dd
	lam, mu, kap := bdec("0.76592833836465"), bdec("0.25"), bdec("1.5")
	for n := range s {
		var pw [psMaxBands]*big.Float
		for b := 0; b < nb; b++ {
			pw[b] = bnew()
		}
		for k := 0; k < dl.rows; k++ {
			v := s[n][k]
			pw[dl.band[k]] = badd(pw[dl.band[k]], badd(bmul(bf(real(v)), bf(real(v))), bmul(bf(imag(v)), bf(imag(v)))))
		}
		for b := 0; b < nb; b++ {
			pk := bmul(lam, r.peak[b])
			if pw[b].Cmp(pk) > 0 {
				pk = pw[b]
			}
			r.peak[b] = pk
			r.avg[b] = badd(r.avg[b], bmul(mu, bsub(pw[b], r.avg[b])))
			r.dev[b] = badd(r.dev[b], bmul(mu, bsub(bsub(pk, pw[b]), r.dev[b])))
			g := bi(1)
			if kd := bmul(kap, r.dev[b]); kd.Cmp(r.avg[b]) > 0 {
				g = bquo(r.avg[b], kd)
			}
			tg[n][b] = ddFromBig(g)
		}
	}
	out := new(psRefOut)
	for k := 0; k < dl.rows; k++ {
		b := dl.band[k]
		hist := r.s[k]
		base := len(hist) - sbrQMFSlots
		image := k < nSplit && psRefCentre24[b2idx(p.is34)][k] < 0
		var h []cdd
		switch {
		case k < dl.nAP:
			split := k < nSplit
			f24 := psRefCentre24[b2idx(p.is34)][0]
			if split {
				f24 = psRefCentre24[b2idx(p.is34)][k]
			} else {
				f24 = 24*int(dl.qmf[k]) + 12
			}
			key := [2]int{b2idx(p.is34), k}
			var ok bool
			if h, ok = r.imp[key]; !ok {
				h = psRefImpulse(int(dl.qmf[k]), q1, split, f24)
				r.imp[key] = h
			}
		}
		for n := 0; n < sbrQMFSlots; n++ {
			var d cdd
			switch {
			case k < dl.nAP:
				for i := 0; i < psRefLen && base+n-i >= 0; i++ {
					if h[i] == (cdd{}) {
						continue
					}
					d = d.add(h[i].mul(cddOf(hist[base+n-i])))
				}
			case k < dl.nLong:
				if base+n-14 >= 0 {
					d = cddOf(hist[base+n-14])
				}
			default:
				if base+n-1 >= 0 {
					d = cddOf(hist[base+n-1])
				}
			}
			d = d.scale(tg[n][b])
			sv := cddOf(hist[base+n])
			g, gi := gain[n][b], gainIm[n][b]
			if image {
				// a row of negative centre frequency takes the conjugate gains
				for i := range gi {
					gi[i] = gi[i].neg()
				}
			}
			c := func(i int) cdd { return cdd{g[i], gi[i]} }
			out.l[n][k] = sv.mul(c(psGLs)).add(d.mul(c(psGLd)))
			out.r[n][k] = sv.mul(c(psGRs)).add(d.mul(c(psGRd)))
		}
	}
	return out
}

func b2idx(is34 bool) int {
	if is34 {
		return 1
	}
	return 0
}
