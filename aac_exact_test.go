// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// The exact reference: the same bitstream, the same integer stages (the parser), and
// every real-valued stage in float64: inverse quantisation, noise, M/S, intensity, TNS,
// the inverse MDCT by its definition (a double sum, no FFT), the windows from their
// formulas, the overlap-add. It is test-only code, written for clarity, not speed.
// TestAACAccuracy measures the decoder and ffmpeg's builds against it.

type exactChan struct {
	saved     [aacFrameLen]float64
	prevSeq   uint8
	prevShape uint8
}

type exactSynth struct {
	rate  int
	noise uint32
	chans []exactChan
}

func exactSine(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = math.Sin((float64(i) + 0.5) * (math.Pi / (2 * float64(n))))
	}
	return w
}

// aacBesselI0 is the modified Bessel function of the first kind, order zero, by its
// power series: the reference's own, independent of the library the decoder takes
// its windows from.
func aacBesselI0(x float64) float64 {
	sum, term := 1.0, 1.0
	q := x * x / 4
	for k := 1; term > 1e-18*sum; k++ {
		term *= q / float64(k*k)
		sum += term
	}
	return sum
}

func exactKBD(alpha float64, n int) []float64 {
	a2 := 4 * (alpha * math.Pi / float64(n)) * (alpha * math.Pi / float64(n))
	k := make([]float64, n/2+1)
	var total float64
	for i := range k {
		k[i] = aacBesselI0(math.Sqrt(float64(i) * float64(n-i) * a2))
		w := 1.0
		if i != 0 && i < n/2 {
			w = 2
		}
		total += k[i] * w
	}
	total++
	w := make([]float64, n)
	sum := 0.0
	for i := range w {
		if i <= n/2 {
			sum += k[i]
		} else {
			sum += k[n-i]
		}
		w[i] = math.Sqrt(sum / total)
	}
	return w
}

// exactIMDCT is the definition, with the output arrangement and scale the decoder
// uses (the first half of the output from one phase, the second from another).
func exactIMDCT(src []float64) []float64 {
	n := len(src)
	half := n / 2
	phase := math.Pi / (4 * float64(n))
	scale := 1 / float64(n) / 32768
	out := make([]float64, n)
	for i := 0; i < half; i++ {
		var d, u float64
		id := phase * float64(4*half-2*i-1)
		iu := phase * float64(3*n+2*i+1)
		for j := 0; j < n; j++ {
			a := float64(2*j + 1)
			d += math.Cos(a*id) * src[j]
			u += math.Cos(a*iu) * src[j]
		}
		out[i], out[i+half] = d*scale, -u*scale
	}
	return out
}

func exactWindow(dst, a, b, win []float64, n int) {
	for i := 0; i < n; i++ {
		j := n - 1 - i
		s0, s1 := a[i], b[j]
		wi, wj := win[i], win[2*n-1-i]
		dst[i] = s0*wj - s1*wi
		dst[2*n-1-i] = s0*wi + s1*wj
	}
}

func (s *exactSynth) spectrum(ch *aacChannel) []float64 {
	ics := &ch.ics
	out := make([]float64, aacFrameLen)
	w0 := 0
	for g := 0; g < int(ics.numGroups); g++ {
		for sfb := 0; sfb < int(ics.maxSfb); sfb++ {
			cb := ch.cb[g][sfb]
			lo, hi := int(ics.swb[sfb]), int(ics.swb[sfb+1])
			switch {
			case cb == aacBandZero || cb > aacBandNoise:
			case cb == aacBandNoise:
				gain := math.Exp2(float64(min(max(int(ch.sf[g][sfb]), -100), 155)) / 4)
				for k := 0; k < int(ics.groupLen[g]); k++ {
					base := (w0 + k) * aacShortLen
					var e float64
					for i := base + lo; i < base+hi; i++ {
						out[i] = float64(aacNoise(&s.noise))
						e += out[i] * out[i]
					}
					for i := base + lo; i < base+hi; i++ {
						out[i] *= gain / math.Sqrt(e)
					}
				}
			default:
				gain := math.Exp2(float64(int(ch.sf[g][sfb])-100) / 4)
				for k := 0; k < int(ics.groupLen[g]); k++ {
					base := (w0 + k) * aacShortLen
					for i := base + lo; i < base+hi; i++ {
						q := float64(ch.q[i])
						out[i] = math.Copysign(math.Pow(math.Abs(q), 4.0/3.0), q) * gain
					}
				}
			}
		}
		w0 += int(ics.groupLen[g])
	}
	return out
}

func (s *exactSynth) element(el *aacElement, first int) [2][]float64 {
	var c [2][]float64
	n := int(el.nch)
	for i := 0; i < n; i++ {
		c[i] = s.spectrum(&el.ch[i])
	}
	if n == 2 {
		ics := &el.ch[0].ics
		w0 := 0
		for g := 0; g < int(ics.numGroups); g++ {
			for sfb := 0; sfb < int(ics.maxSfb); sfb++ {
				for k := 0; k < int(ics.groupLen[g]); k++ {
					base := (w0 + k) * aacShortLen
					for i := base + int(ics.swb[sfb]); i < base+int(ics.swb[sfb+1]); i++ {
						if el.common && el.msMask != 0 && (el.msMask == 2 || el.msUsed[g][sfb]) && el.ch[0].cb[g][sfb] < aacBandNoise && el.ch[1].cb[g][sfb] < aacBandNoise {
							c[0][i], c[1][i] = c[0][i]+c[1][i], c[0][i]-c[1][i]
						}
					}
				}
			}
			w0 += int(ics.groupLen[g])
		}
		ch := &el.ch[1]
		w0 = 0
		for g := 0; g < int(ics.numGroups); g++ {
			for sfb := 0; sfb < int(ics.maxSfb); sfb++ {
				cb := ch.cb[g][sfb]
				if cb < aacBandIntens {
					continue
				}
				sign := float64(-1 + 2*(int(cb)-14))
				if el.common && el.msMask != 0 && (el.msMask == 2 || el.msUsed[g][sfb]) {
					sign = -sign
				}
				scale := sign * math.Exp2(-float64(min(max(int(ch.sf[g][sfb]), -155), 100))/4)
				for k := 0; k < int(ics.groupLen[g]); k++ {
					base := (w0 + k) * aacShortLen
					for i := base + int(ics.swb[sfb]); i < base+int(ics.swb[sfb+1]); i++ {
						c[1][i] = c[0][i] * scale
					}
				}
			}
			w0 += int(ics.groupLen[g])
		}
	}
	if n == 2 && el.common {
		s.chans[first+1].prevSeq = s.chans[first].prevSeq
	}
	var out [2][]float64
	for i := 0; i < n; i++ {
		ch := &el.ch[i]
		if ch.tns.present {
			s.tns(c[i], ch)
		}
		out[i] = s.imdct(&s.chans[first+i], ch, c[i])
	}
	return out
}

func (s *exactSynth) tns(coef []float64, ch *aacChannel) {
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
			lpc := make([]float64, order)
			bitsN := int(t.res) - int(t.compress)
			for i := 0; i < order; i++ {
				raw := int(t.coef[i])
				sv := raw
				if raw >= 1<<(bitsN-1) {
					sv = raw - 1<<bitsN
				}
				q := (float64(int(1)<<(t.res-1)) - 0.5) / (math.Pi / 2)
				if sv < 0 {
					q = (float64(int(1)<<(t.res-1)) + 0.5) / (math.Pi / 2)
				}
				r := math.Sin(float64(sv) / q)
				prev := append([]float64(nil), lpc...)
				for j := 0; j < i; j++ {
					lpc[j] = prev[j] + r*prev[i-1-j]
				}
				lpc[i] = r
			}
			start, end := int(ics.swb[min(bottom, mmm)]), int(ics.swb[min(top, mmm)])
			if end-start <= 0 {
				continue
			}
			pos := func(m int) int {
				if t.down {
					return end - 1 - m + w*aacShortLen
				}
				return start + m + w*aacShortLen
			}
			for m := 0; m < end-start; m++ {
				for k := 1; k <= min(m, order); k++ {
					coef[pos(m)] -= coef[pos(m-k)] * lpc[k-1]
				}
			}
		}
	}
}

func (s *exactSynth) imdct(st *exactChan, ch *aacChannel, coef []float64) []float64 {
	ics := &ch.ics
	neg := make([]float64, aacFrameLen)
	for i := range neg {
		neg[i] = -coef[i]
	}
	sineL, sineS := exactSine(1024), exactSine(128)
	kbdL, kbdS := exactKBD(4, 1024), exactKBD(6, 128)
	lwPrev, swPrev, sw := sineL, sineS, sineS
	if st.prevShape != 0 {
		lwPrev, swPrev = kbdL, kbdS
	}
	if ics.windowShape != 0 {
		sw = kbdS
	}
	buf := make([]float64, aacFrameLen)
	if ics.short() {
		for i := 0; i < aacFrameLen; i += aacShortLen {
			copy(buf[i:], exactIMDCT(neg[i:i+aacShortLen]))
		}
	} else {
		copy(buf, exactIMDCT(neg))
	}
	out := make([]float64, aacFrameLen)
	temp := make([]float64, aacShortLen)
	saved := st.saved[:]
	prevLong := st.prevSeq == aacOnlyLong || st.prevSeq == aacLongStop
	curLong := ics.windowSeq == aacOnlyLong || ics.windowSeq == aacLongStart
	if prevLong && curLong {
		exactWindow(out, saved, buf, lwPrev, 512)
	} else {
		copy(out[:448], saved[:448])
		if ics.short() {
			exactWindow(out[448:], saved[448:], buf, swPrev, 64)
			exactWindow(out[448+128:], buf[64:], buf[128:], sw, 64)
			exactWindow(out[448+256:], buf[128+64:], buf[256:], sw, 64)
			exactWindow(out[448+384:], buf[256+64:], buf[384:], sw, 64)
			exactWindow(temp, buf[384+64:], buf[512:], sw, 64)
			copy(out[448+512:], temp[:64])
		} else {
			exactWindow(out[448:], saved[448:], buf, swPrev, 64)
			copy(out[576:], buf[64:512])
		}
	}
	switch {
	case ics.short():
		copy(saved[:64], temp[64:128])
		exactWindow(saved[64:], buf[512+64:], buf[640:], sw, 64)
		exactWindow(saved[192:], buf[640+64:], buf[768:], sw, 64)
		exactWindow(saved[320:], buf[768+64:], buf[896:], sw, 64)
		copy(saved[448:], buf[896+64:896+128])
	case ics.windowSeq == aacLongStart:
		copy(saved[:448], buf[512:960])
		copy(saved[448:], buf[896+64:896+128])
	default:
		copy(saved[:512], buf[512:1024])
	}
	st.prevSeq, st.prevShape = ics.windowSeq, ics.windowShape
	return out
}

// exactDecode returns the exact output of a fixture, interleaved, for its first
// frames frames.
func exactDecode(t *testing.T, name string, frames int) (out []float64, nch int) {
	t.Helper()
	cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", name+".aac"))
	asc := &bitWriter{}
	asc.bits(2, 5)
	asc.bits(uint(cfg.rateIndex), 4)
	asc.bits(uint(cfg.config), 4)
	asc.bits(0, 3)
	c, err := newAAC(container.TrackConfig{CodecConfig: asc.bytes()}, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	d := c.(*aacDecoder)
	s := &exactSynth{rate: cfg.rateIndex, noise: aacNoiseSeed, chans: make([]exactChan, cfg.channels)}
	nch = cfg.channels
	for fi := 0; fi < frames; fi++ {
		els, err := d.parser.parseFrame(fr[fi])
		if err != nil {
			t.Fatal(err)
		}
		pcm := make([]float64, aacFrameLen*nch)
		for e := range els {
			o := s.element(&els[e], d.firsts[e])
			for ci := 0; ci < int(els[e].nch); ci++ {
				for i := 0; i < aacFrameLen; i++ {
					pcm[i*nch+d.slot[e][ci]] = o[ci][i]
				}
			}
		}
		out = append(out, pcm...)
	}
	return out, nch
}

type errStat struct{ max, rms float64 }

func f32Errors(got []float32, exact []float64) errStat {
	var s errStat
	var sum float64
	for i, x := range exact {
		d := math.Abs(float64(got[i]) - x)
		s.max = math.Max(s.max, d)
		sum += d * d
	}
	s.rms = math.Sqrt(sum / float64(len(exact)))
	return s
}

func readF32(t *testing.T, path string) []float32 {
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f := make([]float32, len(b)/4)
	for i := range f {
		f[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return f
}

// TestAACAccuracy is the gate: against the exact result (float64 in every
// real-valued stage), this decoder's float32 output must be at least as accurate, in
// largest and in RMS error, as the most accurate of ffmpeg's builds, on every
// fixture. Equality with ffmpeg is information (TestAACOracle), not the gate.
func TestAACAccuracy(t *testing.T) {
	builds := map[string]string{
		"ffmpeg macOS clang": filepath.Join("testdata", "aac", "ref"),
		"ffmpeg linux gcc":   filepath.Join("testdata", "aac", "ref-linux"),
	}
	for _, fx := range aacFixtures {
		name := fx.name[:len(fx.name)-4]
		ref := readF32(t, filepath.Join(builds["ffmpeg macOS clang"], name+".f32"))
		frames := len(ref) / (aacFrameLen * fx.channels)
		exact, nch := exactDecode(t, name, frames)
		cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", name+".aac"))
		asc := &bitWriter{}
		asc.bits(2, 5)
		asc.bits(uint(cfg.rateIndex), 4)
		asc.bits(uint(cfg.config), 4)
		asc.bits(0, 3)
		c, _ := newAAC(container.TrackConfig{CodecConfig: asc.bytes()}, AudioConfig{})
		var ours []float32
		for fi := 0; fi < frames; fi++ {
			o, err := c.decode(fr[fi])
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < len(o[0].Data)/4; i++ {
				ours = append(ours, math.Float32frombits(binary.LittleEndian.Uint32(o[0].Data[i*4:])))
			}
		}
		_ = nch
		us := f32Errors(ours, exact)
		best := errStat{math.Inf(1), math.Inf(1)}
		for label, dir := range builds {
			b := readF32(t, filepath.Join(dir, name+".f32"))
			st := f32Errors(b, exact)
			t.Logf("%-14s %-20s max %.3e  rms %.3e", name, label, st.max, st.rms)
			best.max, best.rms = math.Min(best.max, st.max), math.Min(best.rms, st.rms)
		}
		t.Logf("%-14s %-20s max %.3e  rms %.3e", name, "this decoder", us.max, us.rms)
		if us.max > best.max || us.rms > best.rms {
			t.Errorf("%s: this decoder (max %.3e, rms %.3e) is less accurate than the best ffmpeg build (max %.3e, rms %.3e)", name, us.max, us.rms, best.max, best.rms)
		}
	}
}

// TestAACTNSBound: the TNS filter, on a stream that uses it in many frames (the
// other fixtures use it in one: testdata/aac/README says how tns44.aac was made). Two
// bounds. Against the exact result, this decoder is at least as accurate, in largest
// and RMS error, as ffmpeg's decode of the same stream. Against ffmpeg's output the
// distance is at most 1e-6 per sample, as in TestAACDecodeTrack.
func TestAACTNSBound(t *testing.T) {
	const name = "tns44"
	ref := readF32(t, filepath.Join("testdata", "aac", "ref", name+".f32"))
	cfg, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", name+".aac"))
	frames := len(ref) / (aacFrameLen * cfg.channels)
	// the stream must keep using TNS: a fixture that no longer does proves nothing
	p, err := newAACParser(cfg, DefaultMaxAudioChannels)
	if err != nil {
		t.Fatal(err)
	}
	tnsFrames := 0
	for _, f := range fr {
		els, err := p.parseFrame(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range els {
			for c := 0; c < int(e.nch); c++ {
				if e.ch[c].tns.present {
					tnsFrames++
				}
			}
		}
	}
	if tnsFrames < 10 {
		t.Fatalf("%s has TNS in %d channel frames, want at least 10", name, tnsFrames) // 12 as made
	}
	exact, _ := exactDecode(t, name, frames)
	asc := &bitWriter{}
	asc.bits(2, 5)
	asc.bits(uint(cfg.rateIndex), 4)
	asc.bits(uint(cfg.config), 4)
	asc.bits(0, 3)
	c, _ := newAAC(container.TrackConfig{CodecConfig: asc.bytes()}, AudioConfig{})
	var ours []float32
	for fi := 0; fi < frames; fi++ {
		o, err := c.decode(fr[fi])
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < len(o[0].Data)/4; i++ {
			ours = append(ours, math.Float32frombits(binary.LittleEndian.Uint32(o[0].Data[i*4:])))
		}
	}
	us, ff := f32Errors(ours, exact), f32Errors(ref[:len(exact)], exact)
	t.Logf("%s: TNS in %d channel frames; vs exact: this decoder max %.3e rms %.3e, ffmpeg max %.3e rms %.3e", name, tnsFrames, us.max, us.rms, ff.max, ff.rms)
	if us.max > ff.max || us.rms > ff.rms {
		t.Errorf("%s: this decoder (max %.3e, rms %.3e) is less accurate than ffmpeg (max %.3e, rms %.3e)", name, us.max, us.rms, ff.max, ff.rms)
	}
	var apart float64
	for i, v := range ours {
		apart = math.Max(apart, math.Abs(float64(v)-float64(ref[i])))
	}
	t.Logf("%s: largest distance to ffmpeg's output %.3e", name, apart)
	if apart > 1e-6 {
		t.Errorf("%s: %.3e from ffmpeg's output, bound 1e-6", name, apart)
	}
}
