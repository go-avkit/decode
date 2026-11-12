// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"github.com/go-fft/fft"
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
// ten passes); read, not copied. The transforms are not the 64-point MDCT ffmpeg
// uses: with the angle of the kernel written as pi*(2k+1)*(2m+1)/256, the analysis
// is a DCT-IV (real part) plus i times a DST-IV (imaginary part) of the 64 sums
// folded onto odd indices m, and the synthesis is the same pair of transforms in the
// other direction. go-fft's DCT-IV and DST-IV plans compute them, with the factor 2
// the analysis needs built in.
//
// Every stage runs in float64 and the synthesis rounds once, to float32, at the
// output: the error of the banks is that one rounding plus about 1e-13 of the
// signal. The plans are immutable and shared; the working memory of one channel
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
	sbrDCT4 = fft.NewDCTPlan(sbrSynBands, 4)
	sbrDST4 = fft.NewDSTPlan(sbrSynBands, 4)

	// sbrAnaWindow is the analysis window: every second tap of the prototype.
	sbrAnaWindow = func() (w [sbrAnaTaps]float64) {
		for i := range w {
			w[i] = sbrQMFWindow[2*i]
		}
		return w
	}()
)

// sbrAnalysis is the state of the analysis bank of one channel.
type sbrAnalysis struct {
	hist    [sbrAnaKeep + aacFrameLen]float64 // oldest sample first, in 16-bit units
	u, v, w [sbrSynBands]float64
	c, s    [sbrSynBands]float64
}

// process takes the 1024 core samples of a frame and writes the 32 subband
// samples of each of the 32 bands to low[k][first:first+32].
func (a *sbrAnalysis) process(in []float32, low *[sbrAnaBands][sbrLowSlots]complex128, first int) {
	copy(a.hist[:sbrAnaKeep], a.hist[aacFrameLen:])
	for i, s := range in[:aacFrameLen] {
		a.hist[sbrAnaKeep+i] = float64(s) * sbrPCMUnit
	}
	for l := 0; l < sbrQMFSlots; l++ {
		// x[i] of the definition is hist[l*32+319-i]
		h := a.hist[l*32 : l*32+sbrAnaTaps]
		for n := range a.u {
			var sum float64
			for j := 0; j < 5; j++ {
				sum += sbrAnaWindow[n+64*j] * h[sbrAnaTaps-1-n-64*j]
			}
			a.u[n] = sum
		}
		// fold the 64 sums onto the odd angles of the DCT-IV and DST-IV kernels: the
		// angle (4n-1) is 2m+1 for m = 2n-1 up to n = 32, and for n >= 33 it is
		// mirrored (256-(4n-1) = 2m+1 with m = 128-2n), which negates the cosine
		// and keeps the sine; n = 0 is m = -1, the mirror of m = 0 about -1/2.
		a.v[0], a.w[0] = a.u[0], -a.u[0]
		for n := 1; n <= 32; n++ {
			a.v[2*n-1], a.w[2*n-1] = a.u[n], a.u[n]
		}
		for n := 33; n < sbrSynBands; n++ {
			a.v[128-2*n], a.w[128-2*n] = -a.u[n], a.u[n]
		}
		sbrDCT4.DCT(a.c[:], a.v[:], fft.NormBackward)
		sbrDST4.DST(a.s[:], a.w[:], fft.NormBackward)
		for k := 0; k < sbrAnaBands; k++ {
			low[k][first+l] = complex(a.c[k], a.s[k])
		}
	}
}

// sbrSynthesis is the state of the synthesis bank of one channel: the history of
// v, newest first, in a buffer that moves back to its end once per frame so that a
// slot never shifts it.
type sbrSynthesis struct {
	buf        [sbrSynKeep + sbrQMFSlots*128]float64
	off        int // v[0] of the newest slot is buf[off]
	a, b, c, s [sbrSynBands]float64
}

// process turns the 32 slots of subband samples x (each of 64 bands, in 16-bit
// units) into the 2048 samples of out, in +-1.0 units.
func (y *sbrSynthesis) process(out []float32, x *[sbrQMFSlots][sbrSynBands]complex128) {
	start := len(y.buf) - sbrSynKeep
	copy(y.buf[start:], y.buf[y.off:y.off+sbrSynKeep])
	y.off = start
	out = out[:sbrQMFSlots*sbrSynBands]
	for l := range x {
		for k, z := range x[l] {
			y.a[k], y.b[k] = real(z), imag(z)
		}
		sbrDCT4.DCT(y.c[:], y.a[:], fft.NormBackward)
		sbrDST4.DST(y.s[:], y.b[:], fft.NormBackward)
		y.off -= 128
		v := y.buf[y.off : y.off+128]
		// the 1/64 of the definition, the 1/2 of the transforms' factor 2 and the
		// 1/32768 back to +-1.0: 2^-22, exact
		const scale = 1.0 / (128 * sbrPCMUnit)
		for n := 0; n < sbrSynBands; n++ {
			v[n] = (y.s[n] - y.c[n]) * scale
			v[127-n] = (y.c[n] + y.s[n]) * scale
		}
		o := out[l*sbrSynBands : (l+1)*sbrSynBands]
		for q := range o {
			var sum float64
			for n := 0; n < 10; n++ {
				sum += sbrQMFWindow[64*n+q] * y.buf[y.off+128*n+64*(n&1)+q]
			}
			o[q] = float32(sum)
		}
	}
}
