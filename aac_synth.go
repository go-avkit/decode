// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"sync"
)

// Synthesis of AAC-LC: noise substitution, M/S and intensity stereo, TNS, the
// inverse MDCT, windowing and overlap-add (ISO/IEC 14496-3 subpart 4, 4.6).
//
// Every float32 expression whose rounding matters is written with an explicit
// float32 conversion on each product, which the Go specification says prevents a
// compiler from fusing it into an FMA: the result is then the same on every
// architecture.

// mul is a float32 product that is never fused with an addition.
func mul(a, b float32) float32 { return float32(a * b) }

// aacFuse selects how the multiply-adds are rounded: fused (one rounding, as a C
// compiler that contracts a*b+c does) or not. The default is fused, because that
// is what the reference decoder's C code does when it is built with clang on
// arm64 (measured in TestAACOracle: unfused, more than half of the samples differ
// by an ulp; fused, none), and math.FMA gives the same bits on every architecture.
// Only tests change it.
var aacFuse = true

func fma32(a, b, c float32) float32 { return float32(math.FMA(float64(a), float64(b), float64(c))) }

// msub is a*b - c*d, and madd is a*b + c*d: when fused, the first product is
// fused with the sum and the second is rounded, which is how a C compiler
// contracts the expression.
func msub(a, b, c, d float32) float32 {
	if aacFuse {
		return fma32(a, b, -mul(c, d))
	}
	return mul(a, b) - mul(c, d)
}

func madd(a, b, c, d float32) float32 {
	if aacFuse {
		return fma32(a, b, mul(c, d))
	}
	return mul(a, b) + mul(c, d)
}

// addmul is c + a*b and submul is c - a*b.
func addmul(c, a, b float32) float32 {
	if aacFuse {
		return fma32(a, b, c)
	}
	return c + mul(a, b)
}

func submul(c, a, b float32) float32 {
	if aacFuse {
		return fma32(-a, b, c)
	}
	return c - mul(a, b)
}

// aacNoiseSeed starts the noise generator of perceptual noise substitution at the
// beginning of a stream.
const aacNoiseSeed = 0x1f2e3d4c

// aacNoise draws the next noise sample: the linear congruential generator
// x -> 1664525*x + 1013904223 modulo 2^32, read as a signed integer.
func aacNoise(state *uint32) float32 {
	*state = *state*1664525 + 1013904223
	return float32(int32(*state))
}

// aacTNSMap holds the TNS reflection coefficients for a coefficient resolution
// (3 or 4 bits) with or without compression, by the raw bits read: index
// 2*compress + res-3. The value for the signed index s is -sin(s/q) with
// q = (2^(res-1) -/+ 1/2) / (pi/2), the smaller divisor for s >= 0, rounded to
// float32 once. Five of the 36 values (0_3[6], 0_4[1], 0_4[9], 1_3[2], 1_4[1], as
// the reference decoder names its tables) differ by one float32 ulp from the
// reference decoder's printed table, which is not the correctly rounded sine: a
// stream that uses those coefficients is not bit-equal to its output there.
var aacTNSMap = func() (m [4][16]float32) {
	for compress := 0; compress < 2; compress++ {
		for res := 3; res <= 4; res++ {
			bitsN := res - compress
			for i := 0; i < 1<<bitsN; i++ {
				s := i
				if i >= 1<<(bitsN-1) {
					s = i - 1<<bitsN
				}
				q := (float64(int(1)<<(res-1)) - 0.5) / (math.Pi / 2)
				if s < 0 {
					q = (float64(int(1)<<(res-1)) + 0.5) / (math.Pi / 2)
				}
				v := 0.0
				if s != 0 {
					v = -math.Sin(float64(s) / q)
				}
				m[2*compress+res-3][i] = float32(v)
			}
		}
	}
	return m
}()

// aacTNSMaxBands is the number of scalefactor bands TNS may cover, by sampling
// frequency index: row 0 for long windows, row 1 for short ones.
var aacTNSMaxBands = [2][13]uint8{
	{31, 31, 34, 40, 42, 51, 46, 46, 42, 42, 42, 39, 39},
	{9, 9, 10, 14, 14, 14, 14, 14, 14, 14, 14, 14, 14},
}

// aacBesselI0 is the modified Bessel function of the first kind, order zero.
func aacBesselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	q := x * x / 4
	for k := 1; term > 1e-18*sum; k++ {
		term *= q / float64(k*k)
		sum += term
	}
	return sum
}

// aacKBD returns the rising half, n points, of the Kaiser-Bessel-derived window
// of 2n points with parameter alpha: the square root of the running sum of a
// Kaiser window of n+1 points over its total.
func aacKBD(dst []float32, alpha float64, n int) {
	a2 := 4 * (alpha * math.Pi / float64(n)) * (alpha * math.Pi / float64(n))
	kaiser := make([]float64, n/2+1)
	var scale float64
	for i := range kaiser {
		kaiser[i] = aacBesselI0(math.Sqrt(float64(i) * float64(n-i) * a2))
		w := 1.0
		if i != 0 && i < n/2 {
			w = 2
		}
		scale += kaiser[i] * w
	}
	scale = 1 / (scale + 1)
	sum := 0.0
	for i := 0; i < n; i++ {
		if i <= n/2 {
			sum += kaiser[i]
		} else {
			sum += kaiser[n-i]
		}
		dst[i] = float32(math.Sqrt(sum * scale))
	}
}

// The windows and the transforms are built once.
var (
	aacSynthOnce              sync.Once
	aacSineLong, aacKBDLong   [1024]float32
	aacSineShort, aacKBDShort [128]float32
	aacMDCTLong, aacMDCTShort *aacMDCT
)

func aacSynthInit() {
	// The sine of the argument rounded to float32, as a single precision sine
	// takes it.
	for i := range aacSineLong {
		aacSineLong[i] = float32(math.Sin(float64(float32((float64(i) + 0.5) * (math.Pi / (2.0 * 1024))))))
	}
	for i := range aacSineShort {
		aacSineShort[i] = float32(math.Sin(float64(float32((float64(i) + 0.5) * (math.Pi / (2.0 * 128))))))
	}
	aacKBD(aacKBDLong[:], 4, 1024)
	aacKBD(aacKBDShort[:], 6, 128)
	aacMDCTLong, aacMDCTShort = newAACMDCT(1024), newAACMDCT(128)
}

// aacChanSynth is what the overlap-add of one channel carries from frame to
// frame.
type aacChanSynth struct {
	saved     [aacFrameLen]float32
	prevSeq   uint8
	prevShape uint8
}

// aacSynth turns parsed elements into samples. It is not safe for concurrent use.
type aacSynth struct {
	rate  int // sampling frequency index
	noise uint32
	chans []aacChanSynth
	coef  [2][aacFrameLen]float32
	buf   [aacFrameLen]float32
	temp  [aacShortLen]float32
}

func newAACSynth(rateIndex, channels int) *aacSynth {
	aacSynthOnce.Do(aacSynthInit)
	return &aacSynth{rate: rateIndex, noise: aacNoiseSeed, chans: make([]aacChanSynth, channels)}
}

// element synthesises the channels of one element. first is the index of the
// element's first channel in the synthesiser; out[c] receives the 1024 samples of
// its channel c.
func (s *aacSynth) element(el *aacElement, first int, out [2][]float32) {
	n := int(el.nch)
	for c := 0; c < n; c++ {
		el.ch[c].dequantise(&s.coef[c], &s.noise)
	}
	if n == 2 {
		if el.common && el.msMask != 0 {
			s.midSide(el)
		}
		s.intensity(el)
	}
	// The window history of the second channel of a common-window pair is the
	// first channel's sequence and its own shape: ics_info is shared.
	if n == 2 && el.common {
		s.chans[first+1].prevSeq = s.chans[first].prevSeq
	}
	for c := 0; c < n; c++ {
		ch := &el.ch[c]
		if ch.tns.present {
			s.tns(&s.coef[c], ch)
		}
		s.imdctWindow(&s.chans[first+c], ch, &s.coef[c], out[c])
	}
}

// midSide turns the M and S spectra of the bands flagged in the element into
// L = M + S and R = M - S, for the bands that carry neither noise nor intensity.
func (s *aacSynth) midSide(el *aacElement) {
	ics := &el.ch[0].ics
	a, b := &s.coef[0], &s.coef[1]
	w0 := 0
	for g := 0; g < int(ics.numGroups); g++ {
		for sfb := 0; sfb < int(ics.maxSfb); sfb++ {
			if !(el.msMask == 2 || el.msUsed[g][sfb]) || el.ch[0].cb[g][sfb] >= aacBandNoise || el.ch[1].cb[g][sfb] >= aacBandNoise {
				continue
			}
			for k := 0; k < int(ics.groupLen[g]); k++ {
				base := (w0 + k) * aacShortLen
				for i := base + int(ics.swb[sfb]); i < base+int(ics.swb[sfb+1]); i++ {
					t := a[i] - b[i]
					a[i] += b[i]
					b[i] = t
				}
			}
		}
		w0 += int(ics.groupLen[g])
	}
}

// intensity fills the bands of the second channel that are coded as intensity
// positions with the first channel's spectrum, scaled by 2^(-position/4) and
// signed by the band type and the M/S flag.
func (s *aacSynth) intensity(el *aacElement) {
	ch := &el.ch[1]
	ics := &ch.ics
	a, b := &s.coef[0], &s.coef[1]
	w0 := 0
	for g := 0; g < int(ics.numGroups); g++ {
		for sfb := 0; sfb < int(ics.maxSfb); sfb++ {
			cb := ch.cb[g][sfb]
			if cb < aacBandIntens {
				continue
			}
			c := float32(-1 + 2*(int(cb)-14))
			if el.common && el.msMask != 0 && (el.msMask == 2 || el.msUsed[g][sfb]) {
				c = -c
			}
			pos := min(max(int(ch.sf[g][sfb]), -155), 100)
			scale := mul(c, aacGain[100-pos])
			for k := 0; k < int(ics.groupLen[g]); k++ {
				base := (w0 + k) * aacShortLen
				for i := base + int(ics.swb[sfb]); i < base+int(ics.swb[sfb+1]); i++ {
					b[i] = mul(a[i], scale)
				}
			}
		}
		w0 += int(ics.groupLen[g])
	}
}

// tns runs the TNS all-pole filters over the spectrum of a channel (4.6.9).
func (s *aacSynth) tns(coef *[aacFrameLen]float32, ch *aacChannel) {
	ics := &ch.ics
	row := 0
	if ics.short() {
		row = 1
	}
	mmm := min(int(aacTNSMaxBands[row][s.rate]), int(ics.maxSfb))
	if mmm == 0 {
		return
	}
	numSwb := len(ics.swb) - 1
	var lpc [aacTNSMaxLong]float32
	for w := 0; w < int(ics.numWindows); w++ {
		bottom := numSwb
		for f := 0; f < int(ch.tns.n[w]); f++ {
			t := &ch.tns.filt[w][f]
			top := bottom
			bottom = max(0, top-int(t.length))
			order := int(t.order)
			if order == 0 {
				continue
			}
			// reflection coefficients to filter coefficients
			m := &aacTNSMap[2*int(t.compress)+int(t.res)-3]
			for i := 0; i < order; i++ {
				r := -m[t.coef[i]]
				lpc[i] = r
				for j := 0; j < (i+1)>>1; j++ {
					fj, bj := lpc[j], lpc[i-1-j]
					lpc[j] = addmul(fj, r, bj)
					lpc[i-1-j] = addmul(bj, r, fj)
				}
			}
			start := int(ics.swb[min(bottom, mmm)])
			end := int(ics.swb[min(top, mmm)])
			size := end - start
			if size <= 0 {
				continue
			}
			inc := 1
			if t.down {
				inc = -1
				start = end - 1
			}
			start += w * aacShortLen
			for i := 0; i < size; i, start = i+1, start+inc {
				for k := 1; k <= min(i, order); k++ {
					coef[start] = submul(coef[start], coef[start-k*inc], lpc[k-1])
				}
			}
		}
	}
}

// fmulWindow overlaps two blocks under a window: for the n pairs it writes
// 2n samples to dst, from the n samples of a and n of b, with a window of 2n
// points.
func fmulWindow(dst, a, b, win []float32, n int) {
	for i := 0; i < n; i++ {
		j := n - 1 - i
		s0, s1 := a[i], b[j]
		wi, wj := win[i], win[2*n-1-i]
		dst[i] = msub(s0, wj, s1, wi)
		dst[2*n-1-i] = madd(s0, wi, s1, wj)
	}
}

// imdctWindow transforms the spectrum of a channel to its 1024 output samples,
// overlapping the second half of the previous frame's block with the first half of
// this one, and keeps the second half for the next frame.
func (s *aacSynth) imdctWindow(st *aacChanSynth, ch *aacChannel, coef *[aacFrameLen]float32, out []float32) {
	ics := &ch.ics
	// The spectrum of the transform has the sign of the reference's inverse
	// quantiser: scalefactor gains are applied with a negative sign there.
	for i := range coef {
		coef[i] = -coef[i]
	}
	lwPrev, swPrev, sw := aacSineLong[:], aacSineShort[:], aacSineShort[:]
	if st.prevShape != 0 {
		lwPrev, swPrev = aacKBDLong[:], aacKBDShort[:]
	}
	if ics.windowShape != 0 {
		sw = aacKBDShort[:]
	}
	buf := s.buf[:]
	if ics.short() {
		for i := 0; i < aacFrameLen; i += aacShortLen {
			aacMDCTShort.inverse(buf[i:i+aacShortLen], coef[i:i+aacShortLen])
		}
	} else {
		aacMDCTLong.inverse(buf, coef[:])
	}
	prevLong := st.prevSeq == aacOnlyLong || st.prevSeq == aacLongStop
	curLong := ics.windowSeq == aacOnlyLong || ics.windowSeq == aacLongStart
	saved := st.saved[:]
	switch {
	case prevLong && curLong:
		fmulWindow(out, saved, buf, lwPrev, 512)
	default:
		copy(out[:448], saved[:448])
		if ics.short() {
			fmulWindow(out[448:], saved[448:], buf, swPrev, 64)
			fmulWindow(out[448+128:], buf[64:], buf[128:], sw, 64)
			fmulWindow(out[448+2*128:], buf[128+64:], buf[2*128:], sw, 64)
			fmulWindow(out[448+3*128:], buf[2*128+64:], buf[3*128:], sw, 64)
			fmulWindow(s.temp[:], buf[3*128+64:], buf[4*128:], sw, 64)
			copy(out[448+4*128:], s.temp[:64])
		} else {
			fmulWindow(out[448:], saved[448:], buf, swPrev, 64)
			copy(out[576:], buf[64:512])
		}
	}
	switch {
	case ics.short():
		copy(saved[:64], s.temp[64:128])
		fmulWindow(saved[64:], buf[4*128+64:], buf[5*128:], sw, 64)
		fmulWindow(saved[192:], buf[5*128+64:], buf[6*128:], sw, 64)
		fmulWindow(saved[320:], buf[6*128+64:], buf[7*128:], sw, 64)
		copy(saved[448:], buf[7*128+64:7*128+128])
	case ics.windowSeq == aacLongStart:
		copy(saved[:448], buf[512:960])
		copy(saved[448:], buf[7*128+64:7*128+128])
	default:
		copy(saved[:512], buf[512:1024])
	}
	st.prevSeq, st.prevShape = ics.windowSeq, ics.windowShape
}
