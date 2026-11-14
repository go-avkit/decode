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
	imp     map[[2]int][]cdd
	gains   map[[3]int][4]*big.Float
}

func newPSRefMix(eq7 bool) *psRefMix {
	r := &psRefMix{eq7: eq7, imp: map[[2]int][]cdd{}, gains: map[[3]int][4]*big.Float{}}
	r.resetTransient()
	for e := range r.store {
		for b := range r.store[e] {
			for i := range r.store[e][b] {
				r.store[e][b][i] = bnew()
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

func (r *psRefMix) gain(iid, icc int, fine bool) [4]*big.Float {
	var db int
	if fine {
		db = int(psIIDdBFine[iid+15])
	} else {
		db = int(psIIDdBCoarse[iid+7])
	}
	key := [3]int{db, icc, 0}
	if g, ok := r.gains[key]; ok {
		return g
	}
	g := psRefGains(db, icc, r.eq7)
	r.gains[key] = g
	return g
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
		for en := range r.store {
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
							sum = badd(sum, bmul(bi(int64(e.w[j])), r.store[en][e.src[j]][i]))
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
			r.store[en] = next
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
	var gain [sbrQMFSlots][psMaxBands][4]dd
	prev := r.store[p.numEnvOld]
	r.store[0] = prev
	for e := 1; e <= numEnv; e++ {
		next := prev
		var iid, icc [psMaxBands]int8
		if p.enableIID {
			psRemapBands(iid[:], p.iid[e-1][:], p.nrIID, nb)
		}
		if p.enableICC {
			psRemapBands(icc[:], p.icc[e-1][:], p.nrICC, nb)
		}
		for b := 0; b < nb; b++ {
			next[b] = r.gain(int(iid[b]), int(icc[b]), p.iidQuant)
		}
		r.store[e] = next
		b0, b1 := int(border[e-1]), int(border[e])
		for n := b0 + 1; n <= b1; n++ {
			frac := bquo(bi(int64(n-b0)), bi(int64(b1-b0)))
			for b := 0; b < nb; b++ {
				for i := 0; i < 4; i++ {
					v := badd(prev[b][i], bmul(frac, bsub(next[b][i], prev[b][i])))
					gain[n][b][i] = ddFromBig(v)
				}
			}
		}
		prev = next
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
			g := gain[n][b]
			out.l[n][k] = sv.scale(g[psGLs]).add(d.scale(g[psGLd]))
			out.r[n][k] = sv.scale(g[psGRs]).add(d.scale(g[psGRd]))
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
