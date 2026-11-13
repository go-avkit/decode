// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"strings"
	"testing"
)

// sbrSynCase is a written stream of a channel pair or a single channel: a header, a
// grid for each channel, the stream's choices of envelope values and sinusoids.
type sbrSynCase struct {
	name     string
	hdr      sbrHdr
	elem     int
	coupling bool
	grids    [2]sbrGrid
	harm     int // index of the band with a sinusoid, or -1
	first    [2]int
}

func sbrSynCases() []sbrSynCase {
	h := defaultSBRHdr()
	withLim := func(h sbrHdr, bands, gains, ip, smooth int) sbrHdr {
		h.extra2, h.limBands, h.limGains, h.ip, h.smooth = true, bands, gains, ip, smooth
		return h
	}
	fixVar := gridFixVar(1, []int{0, 1}, 1, []int{0, 1, 0})
	varVar := gridVarVar(1, 0, []int{0}, []int{1}, 2, []int{1, 0, 1})
	varFix := gridVarFix(1, []int{1, 0}, 2, []int{0, 1, 0})
	fix2 := gridFixFix(1, 1)
	return []sbrSynCase{
		{"default header, single channel, one envelope", h, aacIDSCE, false, [2]sbrGrid{gridFixFix(0, 0)}, -1, [2]int{10, 5}},
		{"smoothing on, no interpolation, limiter 1 band, gain -3 dB", withLim(h, 1, 0, 0, 0), aacIDSCE, false, [2]sbrGrid{fix2}, -1, [2]int{12, 5}},
		{"smoothing on, interpolation, no limiter bands, gain 0 dB", withLim(h, 0, 1, 1, 0), aacIDSCE, false, [2]sbrGrid{fix2}, 1, [2]int{12, 5}},
		{"limiter 3 bands, gain +3 dB, no smoothing", withLim(h, 3, 2, 0, 1), aacIDSCE, false, [2]sbrGrid{fix2}, 2, [2]int{30, 3}},
		{"limiter 2 bands, no gain limit", withLim(h, 2, 3, 1, 0), aacIDSCE, false, [2]sbrGrid{fix2}, 0, [2]int{25, 3}},
		{"attack in a FIXVAR grid, smoothing on, sinusoid", withLim(h, 2, 2, 1, 0), aacIDSCE, false, [2]sbrGrid{fixVar}, 1, [2]int{20, 4}},
		{"VARVAR grid, sinusoid", withLim(h, 2, 2, 0, 0), aacIDSCE, false, [2]sbrGrid{varVar}, 2, [2]int{20, 4}},
		{"VARFIX grid", withLim(h, 2, 2, 1, 0), aacIDSCE, false, [2]sbrGrid{varFix}, 1, [2]int{20, 4}},
		{"uncoupled pair", h, aacIDCPE, false, [2]sbrGrid{fix2, fixVar}, -1, [2]int{15, 6}},
		{"coupled pair, balance", withLim(h, 2, 2, 1, 0), aacIDCPE, true, [2]sbrGrid{fix2}, 1, [2]int{20, 6}},
		{"coupled pair, 3 dB steps", withLim(sbrHdr{start: 7, stop: 9, amp: 1}, 2, 2, 1, 0), aacIDCPE, true, [2]sbrGrid{fix2}, -1, [2]int{10, 3}},
		{"a header reset with smoothing on", withLim(h, 2, 2, 1, 0), aacIDSCE, false, [2]sbrGrid{fix2}, 1, [2]int{20, 4}},
		{"gains the limiter would let past 1e5", withLim(h, 2, 3, 1, 0), aacIDSCE, false, [2]sbrGrid{fix2}, -1, [2]int{120, 5}},
		{"another table shape", withLim(sbrHdr{start: 5, stop: 8, xover: 2, extra1: true, freqScale: 3, alter: 1, nb: 3}, 3, 2, 0, 0), aacIDSCE, false, [2]sbrGrid{fixVar}, 3, [2]int{18, 5}},
		{"two noise floors, low resolution first", withLim(sbrHdr{start: 8, stop: 14, extra1: true, freqScale: 1, nb: 0}, 2, 2, 1, 0), aacIDSCE, false, [2]sbrGrid{varVar}, -1, [2]int{18, 5}},
	}
}

// TestSBRAdjustWritten: streams written to exercise every branch of the adjustment (the
// limiter's four modes and gains, both interpolation modes, smoothing, attack
// envelopes, sinusoids from the attack on and carried over, coupled and uncoupled
// pairs, 1.5 and 3 dB steps, two noise floors) agree with the exact path to within a
// float32 rounding plus 1e-6 of the output peak, over five frames each, the
// parameters of the frames varying so that state carried between frames is read.
func TestSBRAdjustWritten(t *testing.T) {
	for i, tc := range sbrSynCases() {
		if sbrRefFrames(1, 0) == 0 && i%3 != 0 {
			continue // emulated architectures run a third of the cases
		}
		tb := sbrPayload{}.tables(t, sbrTestCore, tc.hdr)
		s := newSBRState(sbrTestCore)
		n := 1 + tc.elem - aacIDSCE
		h := newSBRAdjHarness(n)
		frames := sbrRefFrames(6, 2)
		pcm := sbrTestPCM(frames, 11)
		var peak, worst float64
		cur := tc.hdr
		for f := 0; f < frames; f++ {
			p := sbrBasic()
			p.elem, p.grids, p.first = tc.elem, tc.grids, tc.first
			p.coupling = tc.coupling
			switch {
			case f == 0:
				p.hdr = &cur
			case f == 3 && strings.HasPrefix(tc.name, "a header reset"):
				cur.stop++ // new spectrum parameters: the tables are rebuilt, a reset
				tb = sbrPayload{}.tables(t, sbrTestCore, cur)
				p.hdr = &cur
			default:
				p.hdr, p.ampKept = nil, tc.hdr.amp
			}
			if tc.harm >= 0 {
				for c := 0; c < n; c++ {
					p.harm[c] = make([]int, tb.n[1])
					p.harm[c][tc.harm%tb.n[1]] = 1
				}
			}
			p.delta = func(ch, kind, i, j int) int {
				if kind == 1 && tc.coupling && ch == 1 {
					return 0 // the balance of the noise floor stays where it starts
				}
				return 1 - 2*((f+ch+kind+i+j)&1)
			}
			if err := sbrRun(t, s, p, tb, true); err != nil {
				t.Fatalf("%s, frame %d: %v", tc.name, f, err)
			}
			if !s.started {
				t.Fatalf("%s: SBR is off", tc.name)
			}
			var in [2][]float32
			for c := 0; c < n; c++ {
				in[c] = pcm[f*aacFrameLen : (f+1)*aacFrameLen]
			}
			got, want := h.step(s, in)
			for c := 0; c < n; c++ {
				e, pk := sbrBeyondRounding(got[c], want[c])
				worst, peak = math.Max(worst, e.max), math.Max(peak, pk)
			}
		}
		t.Logf("%-62s largest error beyond a float32 rounding %.2e, peak %.3f", tc.name, worst, peak)
		if peak == 0 || worst > 1e-6*peak {
			t.Errorf("%s: error %.2e, peak %.3f", tc.name, worst, peak)
		}
	}
}

// TestSBRNoiseTable pins the noise table, which no other test can see (its effect is
// far below the noise of ffmpeg's float32 arithmetic): the sums of its values and of
// their squares, the first and last entries, and that it is 512 entries of at most
// magnitude 1 (the table of a noise generator, unit variance per component by
// construction of the standard).
func TestSBRNoiseTable(t *testing.T) {
	var re, im, pw float64
	for _, v := range sbrNoiseTable {
		re += v[0]
		im += v[1]
		pw += v[0]*v[0] + v[1]*v[1]
		if math.Abs(v[0]) > 1 || math.Abs(v[1]) > 1 {
			t.Fatalf("entry %v beyond 1", v)
		}
	}
	t.Logf("sums: re %.14f im %.14f power %.14f; first %v last %v", re, im, pw, sbrNoiseTable[0], sbrNoiseTable[511])
	if sbrNoiseTable[0] != [2]float64{-0.99948153278296, -0.59483417516607} || sbrNoiseTable[511] != [2]float64{0.91599807087376, -0.98147830385781} {
		t.Errorf("first and last entries %v %v", sbrNoiseTable[0], sbrNoiseTable[511])
	}
	// measured from the table as generated; a changed entry moves at least one
	if math.Abs(re+0.10424741445666) > 1e-12 || math.Abs(im-6.60738368833519) > 1e-12 || math.Abs(pw-513.64296162868357) > 1e-10 {
		t.Errorf("sums changed: re %.14f im %.14f power %.14f", re, im, pw)
	}
}

// TestSBRSmoothing: the smoothing filter sums to one and is the standard's five
// values (0.33333333333333, 0.30150283239582, 0.21816949906249, 0.11516383427084,
// 0.03183050093751 to 1e-13).
func TestSBRSmoothing(t *testing.T) {
	want := [5]float64{0.33333333333333, 0.30150283239582, 0.21816949906249, 0.11516383427084, 0.03183050093751}
	var sum float64
	for i, w := range want {
		sum += sbrSmooth[i]
		if math.Abs(sbrSmooth[i]-w) > 1e-13 {
			t.Errorf("tap %d: %v, want %v", i, sbrSmooth[i], w)
		}
	}
	if math.Abs(sum-1) > 1e-15 {
		t.Errorf("sum %v", sum)
	}
}
