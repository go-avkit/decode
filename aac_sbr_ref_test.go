// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"math/big"
	"math/cmplx"
	"sync"
)

// The exact reference of the SBR signal path: QMF analysis, the prediction-error
// estimate, high frequency generation and QMF synthesis, each by its definition (a
// direct sum, no transform, nothing folded) in float64, and two extended-precision
// oracles for what the float64 path itself could get wrong: math/big.Float at 256
// bits for the banks, math/big.Rat (exact) for the estimate. Test-only code, written
// for clarity, not speed. The decoder's own error is asserted against these.

// refUnit is exp(i*pi*m/256) for an integer m, the angle reduced exactly before the
// one float64 evaluation.
func refUnit(m int) complex128 { return refUnitTab[(m%512+512)%512] }

var refUnitTab = func() (t [512]complex128) {
	for m := range t {
		s, c := math.Sincos(math.Pi * float64(m) / 256)
		t[m] = complex(c, s)
	}
	return t
}()

// refAnalysis is the analysis bank by its definition, over the whole input at once.
type refAnalysis struct {
	in []float64 // every input sample so far, in 16-bit units
}

// frame appends 1024 samples and returns the 32 slots of 32 bands they complete.
func (r *refAnalysis) frame(pcm []float32) (out [sbrQMFSlots][sbrAnaBands]complex128) {
	base := len(r.in)
	for _, s := range pcm[:aacFrameLen] {
		r.in = append(r.in, float64(s)*sbrPCMUnit)
	}
	for l := 0; l < sbrQMFSlots; l++ {
		newest := base + 32*l + 31
		x := func(i int) float64 { // x[0] is the newest sample
			if newest-i < 0 {
				return 0
			}
			return r.in[newest-i]
		}
		var u [64]float64
		for n := range u {
			for j := 0; j < 5; j++ {
				u[n] += sbrQMFWindow[2*(n+64*j)] * x(n+64*j)
			}
		}
		for k := 0; k < sbrAnaBands; k++ {
			var sum complex128
			for n, un := range u {
				sum += complex(2*un, 0) * refUnit((2*k+1)*(4*n-1))
			}
			out[l][k] = sum
		}
	}
	return out
}

// refSynthesis is the synthesis bank by its definition: the blocks of v are kept
// whole, newest first.
type refSynthesis struct {
	blocks [][128]float64 // blocks[0] is the newest
}

// slot takes the 64 bands of one slot and returns its 64 output samples.
func (r *refSynthesis) slot(x *[sbrSynBands]complex128) (out [64]float64) {
	var v [128]float64
	for n := range v {
		var sum complex128
		for k, z := range x {
			sum += z * refUnit((2*k+1)*(2*n-255))
		}
		v[n] = real(sum) / 64
	}
	r.blocks = append([][128]float64{v}, r.blocks...)
	if len(r.blocks) > 10 {
		r.blocks = r.blocks[:10]
	}
	at := func(b, i int) float64 {
		if b >= len(r.blocks) {
			return 0
		}
		return r.blocks[b][i]
	}
	for q := range out {
		var sum float64
		for m := 0; m < 5; m++ {
			sum += sbrQMFWindow[128*m+q] * at(2*m, q)
			sum += sbrQMFWindow[128*m+64+q] * at(2*m+1, 64+q)
		}
		out[q] = sum / sbrPCMUnit
	}
	return out
}

// refGenerate is the high band by its definition from a low band, coefficients and
// chirp factors: X_high[k][i] = L[p][i+2] + a0*bw*L[p][i+1] + a1*bw^2*L[p][i].
func refGenerate(t *sbrTables, c *sbrChannel, low *[sbrAnaBands][sbrLowSlots]complex128, alpha *[sbrAnaBands][2]complex128, bw *[sbrMaxNoise]float64) (high [sbrSynBands][sbrHFSlots]complex128) {
	k := t.kx[1]
	for j := 0; j < t.numPatches; j++ {
		for x := 0; x < t.patchNum[j]; x++ {
			p := t.patchStart[j] + x
			g := 0 // the noise floor band holding k: the last whose lower border is <= k
			for b := 0; b < t.nQ; b++ {
				if t.fNoise[b] <= k {
					g = b
				}
			}
			for i := 2 * int(c.tEnv[0]); i < 2*int(c.tEnv[c.numEnv]); i++ {
				high[k][i] = low[p][i+2] + alpha[p][0]*complex(bw[g], 0)*low[p][i+1] + alpha[p][1]*complex(bw[g]*bw[g], 0)*low[p][i]
			}
			k++
		}
	}
	return high
}

// refChirp is the chirp factor recurrence by its definition, one factor.
func refChirp(cur, prev uint8, old float64) float64 {
	tab := map[uint8]float64{0: 0, 1: 0.75, 2: 0.9, 3: 0.98}
	bw := tab[cur]
	if cur+prev == 1 {
		bw = 0.6
	}
	if bw < old {
		bw = 0.75*bw + 0.25*old
	} else {
		bw = 0.90625*bw + 0.09375*old
	}
	if bw < 0.015625 {
		bw = 0
	}
	return bw
}

// ratC is an exact complex number.
type ratC struct{ re, im *big.Rat }

func ratZero() ratC { return ratC{new(big.Rat), new(big.Rat)} }

func ratOf(z complex128) ratC {
	var c ratC
	c.re, c.im = new(big.Rat), new(big.Rat)
	c.re.SetFloat64(real(z))
	c.im.SetFloat64(imag(z))
	return c
}

func (a ratC) add(b ratC) ratC {
	return ratC{new(big.Rat).Add(a.re, b.re), new(big.Rat).Add(a.im, b.im)}
}

func (a ratC) sub(b ratC) ratC {
	return ratC{new(big.Rat).Sub(a.re, b.re), new(big.Rat).Sub(a.im, b.im)}
}

func (a ratC) mul(b ratC) ratC {
	re := new(big.Rat).Sub(new(big.Rat).Mul(a.re, b.re), new(big.Rat).Mul(a.im, b.im))
	im := new(big.Rat).Add(new(big.Rat).Mul(a.re, b.im), new(big.Rat).Mul(a.im, b.re))
	return ratC{re, im}
}

func (a ratC) conj() ratC { return ratC{a.re, new(big.Rat).Neg(a.im)} }

func (a ratC) scale(f *big.Rat) ratC {
	return ratC{new(big.Rat).Mul(a.re, f), new(big.Rat).Mul(a.im, f)}
}

func (a ratC) float() complex128 {
	re, _ := a.re.Float64()
	im, _ := a.im.Float64()
	return complex(re, im)
}

// refPredictor solves the normal equations of the prediction-error filter in exact
// rational arithmetic: the result is the one number the float64 estimate is
// rounding. The determinant carries the standard's 1/(1+1e-6) as the exact rational
// 1000000/1000001.
func refPredictor(x *[sbrLowSlots]complex128) (a0, a1 complex128) {
	r := func(i, j int) ratC { // R(i,j) = sum over n = 2..39 of x(n-i)*conj(x(n-j))
		s := ratZero()
		for n := 2; n < sbrLowSlots; n++ {
			s = s.add(ratOf(x[n-i]).mul(ratOf(x[n-j]).conj()))
		}
		return s
	}
	r01, r02, r11, r12, r22 := r(0, 1), r(0, 2), r(1, 1), r(1, 2), r(2, 2)
	reg := big.NewRat(1000000, 1000001)
	abs12 := new(big.Rat).Add(new(big.Rat).Mul(r12.re, r12.re), new(big.Rat).Mul(r12.im, r12.im))
	det := new(big.Rat).Sub(new(big.Rat).Mul(r11.re, r22.re), new(big.Rat).Mul(abs12, reg))
	a1r, a0r := ratZero(), ratZero()
	if det.Sign() != 0 {
		a1r = r01.mul(r12).sub(r02.mul(r11)).scale(new(big.Rat).Inv(det))
	}
	if r11.re.Sign() != 0 {
		a0r = r01.add(a1r.mul(r12.conj())).scale(new(big.Rat).Inv(r11.re))
		a0r = ratC{new(big.Rat).Neg(a0r.re), new(big.Rat).Neg(a0r.im)}
	}
	a0, a1 = a0r.float(), a1r.float()
	if cmplx.Abs(a0) >= 4 || cmplx.Abs(a1) >= 4 {
		return 0, 0
	}
	return a0, a1
}

// pi70 is pi to 70 digits, for the 256-bit oracle.
const pi70 = "3.1415926535897932384626433832795028841971693993751058209749445923078164"

var (
	bigUnitMu    sync.Mutex
	bigUnitCache = map[int][2]*big.Float{}
)

// bigUnit is exp(i*pi*m/256) at 256 bits, by the Taylor series of the angle reduced
// to [0, 2*pi) (the series loses 3 digits there, the 256 bits keep 70). The 512
// values are computed once; the caller must not modify them.
func bigUnit(m int) (c, s *big.Float) {
	m = (m%512 + 512) % 512
	bigUnitMu.Lock()
	defer bigUnitMu.Unlock()
	if v, ok := bigUnitCache[m]; ok {
		return v[0], v[1]
	}
	c, s = bigUnitSeries(m)
	bigUnitCache[m] = [2]*big.Float{c, s}
	return c, s
}

func bigUnitSeries(m int) (c, s *big.Float) {
	const prec = 256
	pi, _ := new(big.Float).SetPrec(prec).SetString(pi70)
	x := new(big.Float).SetPrec(prec).Mul(pi, big.NewFloat(float64(m)))
	x.Quo(x, big.NewFloat(256))
	x2 := new(big.Float).SetPrec(prec).Mul(x, x)
	c, s = new(big.Float).SetPrec(prec).SetInt64(1), new(big.Float).SetPrec(prec).Set(x)
	tc, ts := new(big.Float).SetPrec(prec).SetInt64(1), new(big.Float).SetPrec(prec).Set(x)
	for k := 1; k < 70; k++ {
		tc.Mul(tc, x2).Quo(tc, big.NewFloat(float64((2*k-1)*(2*k)))).Neg(tc)
		ts.Mul(ts, x2).Quo(ts, big.NewFloat(float64((2*k)*(2*k+1)))).Neg(ts)
		c.Add(c, tc)
		s.Add(s, ts)
	}
	return c, s
}

// bigAnalysisSlot is the analysis of one slot at 256 bits: x[0] is the newest of the
// 320 samples (16-bit units).
func bigAnalysisSlot(x []float64) (out [sbrAnaBands]complex128) {
	const prec = 256
	var u [64]*big.Float
	for n := range u {
		u[n] = new(big.Float).SetPrec(prec)
		for j := 0; j < 5; j++ {
			t := new(big.Float).SetPrec(prec).Mul(big.NewFloat(sbrQMFWindow[2*(n+64*j)]), big.NewFloat(x[n+64*j]))
			u[n].Add(u[n], t)
		}
	}
	for k := range out {
		re, im := new(big.Float).SetPrec(prec), new(big.Float).SetPrec(prec)
		for n := range u {
			c, s := bigUnit((2*k + 1) * (4*n - 1))
			re.Add(re, new(big.Float).SetPrec(prec).Mul(c, u[n]))
			im.Add(im, new(big.Float).SetPrec(prec).Mul(s, u[n]))
		}
		re.Mul(re, big.NewFloat(2))
		im.Mul(im, big.NewFloat(2))
		r, _ := re.Float64()
		i, _ := im.Float64()
		out[k] = complex(r, i)
	}
	return out
}

// bigSynthesisSlot is the output of the synthesis bank at 256 bits for the 10 most
// recent slots xs (xs[0] the newest), in +-1.0 units, rounded to float64.
func bigSynthesisSlot(xs [10][sbrSynBands]complex128) (out [64]float64) {
	const prec = 256
	var blocks [10][128]*big.Float
	for b := range blocks {
		for n := 0; n < 128; n++ {
			sum := new(big.Float).SetPrec(prec)
			for k, z := range xs[b] {
				c, s := bigUnit((2*k + 1) * (2*n - 255))
				// Re((a+ib)(c+is)) = a*c - b*s
				t := new(big.Float).SetPrec(prec).Mul(c, big.NewFloat(real(z)))
				u := new(big.Float).SetPrec(prec).Mul(s, big.NewFloat(imag(z)))
				sum.Add(sum, t.Sub(t, u))
			}
			blocks[b][n] = sum.Quo(sum, big.NewFloat(64))
		}
	}
	for q := range out {
		sum := new(big.Float).SetPrec(prec)
		for m := 0; m < 5; m++ {
			sum.Add(sum, new(big.Float).SetPrec(prec).Mul(big.NewFloat(sbrQMFWindow[128*m+q]), blocks[2*m][q]))
			sum.Add(sum, new(big.Float).SetPrec(prec).Mul(big.NewFloat(sbrQMFWindow[128*m+64+q]), blocks[2*m+1][64+q]))
		}
		sum.Quo(sum, big.NewFloat(sbrPCMUnit))
		out[q], _ = sum.Float64()
	}
	return out
}
