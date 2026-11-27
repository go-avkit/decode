// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"github.com/go-fft/fft"
	"github.com/go-simd/floats"
)

// The SBR QMF banks (HE-AAC v1): the 32-band complex analysis bank that takes the
// core signal to the subband domain, and the 64-band synthesis bank that takes the
// full-band subband signal to PCM at twice the rate. Both are cosine-modulated
// filter banks with the 640-tap prototype sbrQMFWindow (the analysis bank uses every
// second tap). The subband domain is in 16-bit units: the analysis bank scales the
// +-1.0 input by 32768 and the synthesis bank scales back, as ISO/IEC 14496-3
// subpart 4 and ffmpeg n9.0.2 do, because the envelope adjustment adds absolute
// constants to energies.
//
// Written from the definitions of the standard (clause numbers not verified):
//
//	analysis, per block of 32 new samples (x[0] the newest of the last 320):
//	  u[n]   = sum over j < 5 of c[n+64j] * x[n+64j],  n < 64,  c[i] = prototype[2i]
//	  X[k]   = 2 * sum over n < 64 of u[n] * exp(i*pi/128 * (k+1/2) * (4n-1)),  k < 32
//	synthesis, per slot of 64 subband samples X[k] (v a history of 1280, newest first):
//	  v[n]   = Re sum over k < 64 of X[k]/64 * exp(i*pi/128 * (k+1/2) * (2n-255)),  n < 128
//	  g[128m+q] = v[256m+q], g[128m+64+q] = v[256m+192+q],  m < 5, q < 64
//	  out[q] = sum over n < 10 of c[64n+q] * g[64n+q]
//
// ffmpeg n9.0.2 libavcodec/aacsbr_template.c:1366-1461 and sbrdsp.c:46-95,
// sbrdsp_template.c:27-48 were read to confirm these forms and the order of the
// stages (window, five-term sum, transform, shuffle; transform, butterfly, window in
// ten passes); read, not copied. With the angle of the kernel written as
// pi*(2k+1)*(2m+1)/256, the analysis is ONE real 64-point DCT-IV of the 64 sums folded
// onto odd indices m, whose lower half is the real parts and whose upper half, reversed
// and negated, is the imaginary parts (derived at process); the synthesis is two
// DCT-IVs per slot, one on the real plane and one on the imaginary plane, then a
// butterfly. Both run on go-fft's float32 DCT-IV plan, which has the factor 2 the
// analysis needs built in.
//
// The banks run in float32 (go-avkit/decode#77): the history, the windows, the window
// sums (floats.MulAddRows32, one fused step per tap) and the DCT-IV
// (fft.DCTPlan32, relative 2-norm error at most eps32*log2(64) against
// double-double, asserted by go-fft), the analysis four slots per call (DCTQuad), the
// synthesis a plane pair per slot (DCTPair, DCTQuad for the two channels of a
// parameter stereo frame). The analysis of go-avkit/decode up to #133 ran two float64
// transforms per slot; that version is the oracle of the tests (aac_sbr_qmff64_test.go).
// The error of a slot is bounded, per slot and in
// 2-norm, by the sums derived in aac_sbr_qmf_bound_test.go from those parts, and the
// tests assert it against the definition in float64 and against 256-bit arithmetic;
// the chain of a whole stream is asserted to an empirical 2^-21 of its peak. The
// output PCM is no longer bit identical across routes (arm64, amd64 with AVX2+FMA,
// portable), see the goldens of aac_ps_golden_test.go. The plan is immutable and shared; the working memory of one channel
// lives in its sbrAnalysis and sbrSynthesis, so decoders never share a buffer.

// Sizes of the banks.
const (
	sbrQMFSlots = aacFrameLen / 32 // QMF time slots of one frame
	sbrAnaBands = 32
	sbrSynBands = 64
	sbrAnaTaps  = 320
	sbrPCMUnit  = 32768.0 // the subband domain is the PCM in 16-bit units
	// sbrAnaKeep is the number of samples the analysis bank carries from one
	// frame to the next.
	sbrAnaKeep = sbrAnaTaps - 32
	// sbrSynKeep is the history of the synthesis bank: 10 blocks of 128 less the 128
	// of the slot being written.
	sbrSynKeep = 1280 - 128
)

var (
	// sbrDCT4f is the float32 plan of both banks (single precision throughout where the
	// CPU has AVX2 and FMA).
	sbrDCT4f = fft.NewDCTPlan32(sbrSynBands, 4)

	// sbrAnaWindow is the analysis window: every second tap of the prototype.
	sbrAnaWindow = func() (w [sbrAnaTaps]float64) {
		for i := range w {
			w[i] = sbrQMFWindow[2*i]
		}
		return w
	}()

	// the windows in float32, which the banks' window sums read
	sbrAnaWindow32 = func() (w [sbrAnaTaps]float32) {
		for i := range w {
			w[i] = float32(sbrAnaWindow[i])
		}
		return w
	}()
	// the synthesis window carries the 1/64 of the definition, the 1/2 of the
	// transforms' factor 2 and the 1/32768 back to +-1.0 (2^-22), so that the butterfly
	// writes the unscaled values: scaling by a power of two is exact, the products and
	// sums are the ones of the scaled values bit for bit
	sbrQMFWindow32 = func() (w [640]float32) {
		for i := range w {
			w[i] = float32(sbrQMFWindow[i] / (128 * sbrPCMUnit))
		}
		return w
	}()
)

// sbrAnaBatch is the number of slots whose transforms the analysis runs in one
// fft.DCTPlan32.DCTQuad call.
const sbrAnaBatch = 4

// sbrAnalysis is the state of the analysis bank of one channel.
type sbrAnalysis struct {
	hist [sbrAnaKeep + aacFrameLen]float32 // newest sample first, in 16-bit units
	u    [sbrSynBands]float32
	w, c [sbrAnaBatch][sbrSynBands]float32 // the folded sums and their transforms
}

// process takes the 1024 core samples of a frame and writes the 32 subband
// samples of each of the 32 bands to low[k][first:first+32].
//
// One real 64-point transform per slot: with the 64 window sums u folded into w (see
// below), the transform C of w, factor 2 included, is the whole slot,
//
//	X[k] = C[k] - i C[63-k],  k < 32.
//
// Derivation. X[k] = 2 sum over n < 64 of u[n] exp(i*theta), theta = (2k+1)(4n-1) pi/256.
// The angle (4n-1) is 2m+1 for m = 2n-1 up to n = 32, and for n >= 33 it is mirrored
// (256-(4n-1) = 2m+1 with m = 128-2n), which turns cos theta into -cos of the mirrored
// angle and keeps sin theta; n = 0 is m = -1, the mirror of m = 0 about -1/2 (cos kept,
// sin negated). So w[2n-1] = u[n] (n = 1..32), w[128-2n] = -u[n] (n = 33..63), w[0] = u[0],
// and Re X[k] = C[k] with C[j] = 2 sum over m of w[m] cos((2j+1)(2m+1) pi/256). For the
// imaginary part, (2(63-k)+1) = 128-(2k+1) gives cos((2m+1)pi/2 - (2k+1)(2m+1)pi/256) =
// (-1)^m sin((2k+1)(2m+1)pi/256): the odd m of n <= 32 give -sin = -sin theta; the even
// m of n >= 33 give -sin of the mirrored angle, and sin theta is +sin of it; n = 0 gives
// +sin((2k+1)pi/256) where sin theta is the negative of it. In all three cases the term
// of C[63-k] is the negative of the term of Im X[k], so C[63-k] = -Im X[k]. The upper
// half of the transform is the imaginary part: one transform per slot, no second one.
func (a *sbrAnalysis) process(in []float32, low *[sbrAnaBands][sbrLowSlots]complex128, first int) {
	// the history is kept newest first, so that the samples the window meets in the
	// order of the definition (x[0] the newest) are consecutive: the old frame's newest
	// samples move behind the new frame, which is written backwards
	copy(a.hist[aacFrameLen:], a.hist[:sbrAnaKeep])
	for i, s := range in[:aacFrameLen] {
		a.hist[aacFrameLen-1-i] = s * sbrPCMUnit // exact: a power of two
	}
	for l0 := 0; l0 < sbrQMFSlots; l0 += sbrAnaBatch {
		for q := range a.w {
			// x[i] of the definition is h[i], h the 320 samples from the newest of the
			// slot: u[n] = sum over j < 5 of c[n+64j] * x[n+64j], five rows of 64 that the
			// kernel sums with the 64 results in registers, in the order of j
			l := l0 + q
			h := a.hist[aacFrameLen-32-32*l : aacFrameLen-32-32*l+sbrAnaTaps]
			a.u = [sbrSynBands]float32{}
			floats.MulAddRows32(a.u[:], sbrAnaWindow32[:], 64, h, 64, 5)
			w := &a.w[q]
			w[0] = a.u[0]
			for n := 1; n <= 32; n++ {
				w[2*n-1] = a.u[n]
			}
			for n := 33; n < sbrSynBands; n++ {
				w[128-2*n] = -a.u[n]
			}
		}
		sbrDCT4f.DCTQuad([4][]float32{a.c[0][:], a.c[1][:], a.c[2][:], a.c[3][:]},
			[4][]float32{a.w[0][:], a.w[1][:], a.w[2][:], a.w[3][:]}, fft.NormBackward)
		for q := range a.c {
			c := &a.c[q]
			for k := 0; k < sbrAnaBands; k++ {
				low[k][first+l0+q] = complex(float64(c[k]), -float64(c[sbrSynBands-1-k]))
			}
		}
	}
}

// sbrSynthesis is the state of the synthesis bank of one channel: the history of
// v, newest first, in a buffer that moves back to its end once per frame so that a
// slot never shifts it.
type sbrSynthesis struct {
	buf        [sbrSynKeep + sbrQMFSlots*128]float32
	off        int // v[0] of the newest slot is buf[off]
	a, b, c, s [sbrSynBands]float32
}

// process turns the 32 slots of subband samples x (each of 64 bands, in 16-bit
// units) into the 2048 samples of out, in +-1.0 units.
func (y *sbrSynthesis) process(out []float32, x *[sbrQMFSlots][sbrSynBands]complex128) {
	out = y.begin(out)
	for l := range x {
		for k, z := range x[l] {
			y.a[k], y.b[sbrSynBands-1-k] = float32(real(z)), float32(imag(z))
		}
		y.slot32((*[sbrSynBands]float32)(out[l*sbrSynBands:]), &y.a, &y.b)
	}
}

// begin starts a frame: it moves the history back to the end of its buffer, once per
// frame, so that a slot never shifts it, and returns the frame's output. The 32 slots of
// the frame then follow, in order, through slot.
func (y *sbrSynthesis) begin(out []float32) []float32 {
	start := len(y.buf) - sbrSynKeep
	copy(y.buf[start:], y.buf[y.off:y.off+sbrSynKeep])
	y.off = start
	return out[:sbrQMFSlots*sbrSynBands]
}

// slot32 synthesises the next slot of the frame: the real parts a and the reversed
// imaginary parts b (b[63-k] the imaginary part of band k) of its 64 bands into the 64
// samples o.
func (y *sbrSynthesis) slot32(o *[sbrSynBands]float32, a, b *[sbrSynBands]float32) {
	sbrDCT4f.DCTPair(y.c[:], y.s[:], a[:], b[:], fft.NormBackward)
	y.finish(o)
}

// slotQuad32 is slot32 of two banks (the left and the right channel of a parameter
// stereo frame), whose inputs a and b are already in place, with their four transforms
// batched in one DCTQuad call.
func slotQuad32(y1, y2 *sbrSynthesis, o1, o2 *[sbrSynBands]float32) {
	sbrDCT4f.DCTQuad([4][]float32{y1.c[:], y1.s[:], y2.c[:], y2.s[:]},
		[4][]float32{y1.a[:], y1.b[:], y2.a[:], y2.b[:]}, fft.NormBackward)
	y1.finish(o1)
	y2.finish(o2)
}

// finish takes the transforms in y.c and y.s to the slot's 64 output samples.
func (y *sbrSynthesis) finish(o *[sbrSynBands]float32) {
	y.off -= 128
	v := y.buf[y.off : y.off+128]
	// the scale 2^-22 is in the window. The sine part is (-1)^n y.s[n].
	for n := 0; n < sbrSynBands; n += 2 {
		v[n] = y.s[n] - y.c[n]
		v[127-n] = y.c[n] + y.s[n]
		v[n+1] = -y.s[n+1] - y.c[n+1]
		v[126-n] = y.c[n+1] - y.s[n+1]
	}
	// ten products of 64 values: block n of the window against the 64 values of
	// v that the definition's g takes at 128n (+64 for odd n)
	// The even blocks are rows 256 apart of v against rows 128 apart of the window,
	// and so are the odd blocks 192 on: two fused multiply-add kernels that keep the
	// 64 sums in registers across their five rows, the even blocks first.
	*o = [sbrSynBands]float32{}
	w := y.buf[y.off:]
	floats.MulAddRows32(o[:], sbrQMFWindow32[:], 128, w, 256, 5)
	floats.MulAddRows32(o[:], sbrQMFWindow32[64:], 128, w[192:], 256, 5)
}
