// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// The written streams S1 to S19 of docs/aac-ps-hybrid.md section 5 and
// docs/aac-ps-mixing.md section 7.3. Each is an ADTS stream: a mono AAC-LC core made by
// ffmpeg's encoder from a synthetic signal (testdata/aac/ps/core-*.aac), with an SBR
// fill element that the SBR and PS writers add to every frame. The PS parameters of the
// frames are the stream's whole content, so each stream isolates one stage in the PCM.
// ffmpeg 9.0.2's decode of each (testdata/aac/ref/ps-*.f32, its C code paths) is the
// oracle; PS_RECORD=<dir> writes the streams and records ffmpeg's output again.

const psStreamFrames = 12

// psStreamFrame is one frame of a written stream.
type psStreamFrame struct {
	ps      *psFrame // nil: an SBR frame with no ps_data
	sbrHdr  *sbrHdr  // a new SBR header
	corrupt bool     // ps_data that does not parse
	noSBR   bool     // no SBR fill element at all: the core frame alone (SBR drops out)
}

type psStreamSpec struct {
	name    string
	core    string
	bounded bool // the bound applies; else the difference is measured and logged
	ffImage bool // compared under the test switch that gives the image rows of IPD/OPD ffmpeg's rule (Q5)
	frames  []psStreamFrame
}

// psHdr is the header of a first frame.
func psHdr(iid, icc int) *psHdrSpec { return &psHdrSpec{iid: iid, icc: icc} }

// psEnvs makes count envelopes of n bands whose index of band b of envelope e is f(e, b).
func psEnvs(n, count int, f func(e, b int) int) []psEnvSpec {
	out := make([]psEnvSpec, count)
	for e := range out {
		out[e].v = make([]int, n)
		for b := range out[e].v {
			out[e].v[b] = f(e, b)
		}
	}
	return out
}

func psConstF(v int) func(e, b int) int { return func(e, b int) int { return v } }

// psFixed makes a fixed-class frame of count envelopes (0, 1, 2 or 4) with the indices of
// iid and icc over the bands of the modes in force.
func psFixed(hdr *psHdrSpec, iidBands, iccBands, count int, iid, icc func(e, b int) int) *psFrame {
	code := map[int]int{0: 0, 1: 1, 2: 2, 4: 3}[count]
	f := &psFrame{hdr: hdr, class: 0, envCode: code}
	if iidBands > 0 {
		f.iid = psEnvs(iidBands, count, iid)
	}
	if iccBands > 0 {
		f.icc = psEnvs(iccBands, count, icc)
	}
	return f
}

func psRepeat(n int, f func(i int) psStreamFrame) []psStreamFrame {
	out := make([]psStreamFrame, n)
	for i := range out {
		out[i] = f(i)
	}
	return out
}

// psCombo gives the IID index and ICC index of the k-th combination: every IID index of
// the resolution against every ICC index.
func psCombo(k int, fine bool) (iid, icc int) {
	n := 15
	if fine {
		n = 31
	}
	k %= n * 8
	return k/8 - n/2, k % 8
}

func psStreamSpecs() []psStreamSpec {
	alt := func(e, b int) int { return []int{-7, 7}[b%2] }
	alt34 := func(e, b int) int { return []int{-7, 7, 7, -7}[b%4] }
	comboC := func(e, b int) int { i, _ := psCombo(e*20+b, false); return i }
	comboCC := func(e, b int) int { _, c := psCombo(e*20+b, false); return c }
	comboF := func(e, b int) int { i, _ := psCombo(e*20+b, true); return i }
	comboFC := func(e, b int) int { _, c := psCombo(e*20+b, true); return c }
	// the frame index offsets the combinations so that the frames cover all of them
	shift := func(f func(e, b int) int, fi, step int) func(e, b int) int {
		return func(e, b int) int { return f(e+fi*step, b) }
	}
	frame20 := func(i int, hdr bool, count int, iid, icc func(e, b int) int) psStreamFrame {
		var h *psHdrSpec
		if hdr {
			h = psHdr(1, 1)
		}
		return psStreamFrame{ps: psFixed(h, 20, 20, count, iid, icc)}
	}
	frame34 := func(i int, hdr bool, count int, iid, icc func(e, b int) int) psStreamFrame {
		var h *psHdrSpec
		if hdr {
			h = psHdr(2, 2)
		}
		return psStreamFrame{ps: psFixed(h, 34, 34, count, iid, icc)}
	}
	mixed := func(e, b int) int { return (e*3+b)%15 - 7 }
	mixedC := func(e, b int) int { return (e + b*3) % 7 }
	var specs []psStreamSpec
	add := func(name, core string, bounded bool, fr []psStreamFrame) {
		specs = append(specs, psStreamSpec{name: name, core: core, bounded: bounded, frames: fr})
	}
	add("S01", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return frame20(i, i == 0, 1, psConstF(0), psConstF(0))
	}))
	add("S02", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return frame20(i, i == 0, 1, alt, psConstF(0))
	}))
	add("S03", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return frame34(i, i == 0, 1, psConstF(0), psConstF(0))
	}))
	add("S04", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return frame34(i, i == 0, 1, alt34, psConstF(0))
	}))
	add("S05", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		if i/4 == 1 {
			return frame34(i, i%4 == 0, 1, alt34, psConstF(0))
		}
		return frame20(i, i%4 == 0, 1, alt, psConstF(0))
	}))
	add("S06", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		switch {
		case i == 5:
			return psStreamFrame{corrupt: true}
		case i == 6:
			return frame20(i, true, 1, psConstF(0), psConstF(0))
		}
		return frame20(i, i == 0, 1, psConstF(0), psConstF(0))
	}))
	// S06b: a fault in the middle of frames of two envelopes with a decorrelated signal:
	// PS is suspended with all its state frozen and resumes from where it stopped, the
	// matrix carried being the slot the faulty frame's envelope count names (Q14)
	add("S06b", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		switch i {
		case 5:
			return psStreamFrame{corrupt: true}
		case 6:
			return frame20(i, true, 2, shift(mixed, i, 2), func(e, b int) int { return 2 + (e+b)%3 })
		}
		return frame20(i, i == 0, 2, shift(mixed, i, 2), func(e, b int) int { return 2 + (e+b)%3 })
	}))
	add("S07", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return frame20(i, i == 0, 1, psConstF(0), psConstF(7))
	}))
	add("S08", "onsets", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return frame20(i, i == 0, 1, psConstF(0), psConstF(7))
	}))
	add("S09c", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return frame20(i, i == 0, 4, shift(comboC, i, 4), shift(comboCC, i, 4))
	}))
	add("S09f", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		var h *psHdrSpec
		if i == 0 {
			h = psHdr(4, 1)
		}
		return psStreamFrame{ps: psFixed(h, 20, 20, 4, shift(comboF, i, 4), shift(comboFC, i, 4))}
	}))
	add("S10", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		count := []int{1, 2, 4, 0}[i%4]
		return frame20(i, i == 0, count, shift(mixed, i, 2), shift(mixedC, i, 3))
	}))
	// class 1: free borders, with equal borders and a first border at 0
	borders := [][]int{{15, 31}, {15, 15}, {0, 0, 31}, {0, 5, 5, 31}, {3, 31, 31}, {7, 20, 31, 31}}
	add("S11", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		b := borders[i%len(borders)]
		var h *psHdrSpec
		if i == 0 {
			h = psHdr(1, 1)
		}
		f := &psFrame{hdr: h, class: 1, envCode: len(b) - 1, borders: b}
		if len(b) == 1 {
			f.envCode = 0
		}
		f.iid = psEnvs(20, len(b), shift(mixed, i, 2))
		f.icc = psEnvs(20, len(b), shift(mixedC, i, 3))
		return psStreamFrame{ps: f}
	}))
	add("S12", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		var h *psHdrSpec
		iidB, mode := 10, 0
		if i >= 6 {
			mode = 3
		}
		switch i {
		case 0:
			h = psHdr(0, 0)
		case 6:
			h = psHdr(3, 0)
		}
		iid := shift(func(e, b int) int { return (e+b*2)%15 - 7 }, i, 2)
		if mode == 3 {
			iid = shift(func(e, b int) int { return (e*3+b*5)%31 - 15 }, i, 2)
		}
		return psStreamFrame{ps: psFixed(h, iidB, 10, 2, iid, shift(func(e, b int) int { return (e + b) % 8 }, i, 1))}
	}))
	add("S13a", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return frame34(i, i == 0, 1, psConstF(0), psConstF(7))
	}))
	add("S13b", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return frame34(i, i == 0, 4, shift(func(e, b int) int { return (e*2+b)%15 - 7 }, i, 4), shift(func(e, b int) int { return (e + b*3) % 8 }, i, 4))
	}))
	add("S14a", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		var h *psHdrSpec
		if i == 0 {
			h = psHdr(2, 1)
		}
		return psStreamFrame{ps: psFixed(h, 34, 20, 2, shift(func(e, b int) int { return (e*2+b)%15 - 7 }, i, 2), shift(func(e, b int) int { return (e + b) % 8 }, i, 2))}
	}))
	add("S14b", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		var h *psHdrSpec
		if i == 0 {
			h = psHdr(0, 2)
		}
		return psStreamFrame{ps: psFixed(h, 10, 34, 2, shift(func(e, b int) int { return (e*2+b*3)%15 - 7 }, i, 2), shift(func(e, b int) int { return (e + b) % 8 }, i, 2))}
	}))
	// S14c: IID at 20 bands over ICC at 34, whose 20 -> 34 averages of negative odd sums
	// show whether the index average truncates toward zero (Q6)
	add("S14c", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		var h *psHdrSpec
		if i == 0 {
			h = psHdr(1, 2)
		}
		return psStreamFrame{ps: psFixed(h, 20, 34, 2, shift(func(e, b int) int { return (e*5+b*3)%15 - 7 }, i, 2), shift(func(e, b int) int { return (e + b) % 8 }, i, 2))}
	}))
	icc2 := func(e, b int) int { return 2 + (e+b)%3 }
	add("S15", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		if i%4 < 2 {
			return frame34(i, i%4 == 0, []int{1, 2}[i%2], shift(mixed, i, 2), icc2)
		}
		return frame20(i, i%4 == 2, []int{1, 4}[i%2], shift(mixed, i, 2), icc2)
	}))
	ipd := func(n int, v int) []psEnvSpec { return psEnvs(n, 1, psConstF(v)) }
	ipdFrame := func(i int, is34 bool, phase int) psStreamFrame {
		n, mode, ipdN := 20, 1, 11
		if is34 {
			n, mode, ipdN = 34, 2, 17
		}
		var h *psHdrSpec
		if i == 0 {
			h = &psHdrSpec{iid: mode, icc: mode, ext: true}
		}
		f := psFixed(h, n, n, 1, shift(mixed, i, 2), icc2)
		f.ext = []psExtItem{{id: 0, enable: true, ipd: ipd(ipdN, phase), opd: ipd(ipdN, phase)}}
		return psStreamFrame{ps: f}
	}
	add("S16a20", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame { return ipdFrame(i, false, 0) }))
	add("S16a34", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame { return ipdFrame(i, true, 0) }))
	// S16b and the streams after it put phases on the image rows too, where ffmpeg's rule
	// differs from the specification's and from ISO's (Q5): they are compared under the test
	// switch ffImage, and TestPSImageRowRule shows the default differs.
	addFF := func(name string, fr []psStreamFrame) {
		add(name, "stationary", true, fr)
		specs[len(specs)-1].ffImage = true
	}
	addFF("S16b20", psRepeat(psStreamFrames, func(i int) psStreamFrame { return ipdFrame(i, false, 1+i%7) }))
	addFF("S16b34", psRepeat(psStreamFrames, func(i int) psStreamFrame { return ipdFrame(i, true, 1+i%7) }))
	// IPD and OPD apart, over 4 and 2 envelopes (the smoothing runs over envelopes and
	// across frames), and every phase index
	phased := func(i int, h *psHdrSpec, iidN, iccN, ipdN, count int) psStreamFrame {
		f := psFixed(h, iidN, iccN, count, shift(mixed, i, 2), icc2)
		f.ext = []psExtItem{{id: 0, enable: true,
			ipd: psEnvs(ipdN, count, func(e, b int) int { return (1 + i + 2*e + b) % 8 }),
			opd: psEnvs(ipdN, count, func(e, b int) int { return (3 + 2*i + e + 3*b) % 8 })}}
		return psStreamFrame{ps: f}
	}
	extHdr := func(i, iid, icc int) *psHdrSpec {
		if i == 0 {
			return &psHdrSpec{iid: iid, icc: icc, ext: true}
		}
		return nil
	}
	addFF("S16c20", psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return phased(i, extHdr(i, 1, 1), 20, 20, 11, []int{4, 2}[i%2])
	}))
	addFF("S16c34", psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return phased(i, extHdr(i, 2, 2), 34, 34, 17, []int{4, 2}[i%2])
	}))
	// the phase bands of the other resolutions: 5 -> 11 (IID mode 0), 5 -> 17 and 11 -> 17
	addFF("S16f", psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return phased(i, extHdr(i, 0, 1), 10, 20, 5, 2)
	}))
	addFF("S16g", psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return phased(i, extHdr(i, 0, 2), 10, 34, 5, 2)
	}))
	addFF("S16h", psRepeat(psStreamFrames, func(i int) psStreamFrame {
		return phased(i, extHdr(i, 1, 2), 20, 34, 11, 2)
	}))
	// the layout switches with phases: the history of the smoothing and the carried matrix
	// (S16d is measured, not bounded: after a layout switch ffmpeg's phases differ from
	// ours for one or two frames, and no ISO stream switches layout with phases; Q16.)
	addFF("S16d", psRepeat(psStreamFrames, func(i int) psStreamFrame {
		var h *psHdrSpec
		if i%2 == 0 {
			h = &psHdrSpec{iid: 1, icc: 1, ext: true}
			if i%4 == 0 {
				h.iid, h.icc = 2, 2
			}
		}
		if i%4 < 2 {
			return phased(i, h, 34, 34, 17, 2)
		}
		return phased(i, h, 20, 20, 11, 2)
	}))
	specs[len(specs)-1].bounded = false
	// phases switched off and on again: a frame whose extension says so, between frames that send them
	addFF("S16e", psRepeat(psStreamFrames, func(i int) psStreamFrame {
		f := phased(i, extHdr(i, 1, 1), 20, 20, 11, 2)
		if i%4 == 2 {
			f.ps.ext = []psExtItem{{id: 0, enable: false}}
		}
		return f
	}))
	// S17: a header that lowers stop_freq (and so the SBR top) in mid-stream, which shows
	// the clearing above the top (Q10); S17b raises it again. Both rest on the SBR restart
	// of the noise table at a header (its own pull request).
	add("S17b", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		f := frame20(i, i == 0, 1, psConstF(0), psConstF(7))
		switch i {
		case 0:
			f.sbrHdr = &sbrHdr{start: 7, stop: 8}
		case 6:
			f.sbrHdr = &sbrHdr{start: 7, stop: 9}
		}
		return f
	}))
	add("S17", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		f := frame20(i, i == 0, 1, psConstF(0), psConstF(7))
		switch i {
		case 0:
			f.sbrHdr = &sbrHdr{start: 7, stop: 9}
		case 6:
			f.sbrHdr = &sbrHdr{start: 7, stop: 8}
		}
		return f
	}))
	add("S18", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		var h *psHdrSpec
		if i == 0 {
			h = psHdr(1, 4)
		}
		return psStreamFrame{ps: psFixed(h, 20, 20, 4, shift(comboC, i, 4), shift(comboCC, i, 4))}
	}))
	// procedure B on every IID and ICC index: 34 bands with the fine IID (mode 5, ICC mode
	// 5), 10 bands with the fine IID and ICC mode 3 (the 10 -> 20 maps), and ICC mode 3 with
	// 34 bands against the coarse IID
	add("S18b", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		var h *psHdrSpec
		if i == 0 {
			h = psHdr(5, 5)
		}
		return psStreamFrame{ps: psFixed(h, 34, 34, 4, shift(comboF, i, 4), shift(comboFC, i, 4))}
	}))
	add("S18c", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		var h *psHdrSpec
		if i == 0 {
			h = psHdr(3, 3)
		}
		return psStreamFrame{ps: psFixed(h, 10, 10, 2, shift(comboF, i, 2), shift(comboFC, i, 2))}
	}))
	add("S18d", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		var h *psHdrSpec
		if i == 0 {
			h = psHdr(2, 3)
		}
		return psStreamFrame{ps: psFixed(h, 34, 10, 2, shift(comboC, i, 2), shift(comboCC, i, 2))}
	}))
	// procedure B with phases
	addFF("S18e", psRepeat(psStreamFrames, func(i int) psStreamFrame {
		f := phased(i, extHdr(i, 1, 4), 20, 20, 11, 2)
		f.ps.iid = psEnvs(20, 2, shift(comboC, i, 2))
		f.ps.icc = psEnvs(20, 2, shift(comboCC, i, 2))
		return f
	}))
	// S19c: the first two frames both carry ps_data, then frames without
	add("S19c", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		if i >= 2 && i != 4 && i != 6 && i != 9 {
			return psStreamFrame{}
		}
		return frame20(i, i == 0, 1, shift(mixed, i, 2), icc2)
	}))
	// S19a: SBR frames with no ps_data between frames of one layout and one envelope each
	// (Q15: a frame without data starts from the slot numbered by the count of the frame
	// parsed before the last one, so the frames after the first fade in again).
	add("S19a", "stationary", true, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		if i%3 != 0 {
			return psStreamFrame{}
		}
		return frame20(i, i == 0, 1, shift(mixed, i, 2), icc2)
	}))
	add("S19b", "stationary", false, psRepeat(psStreamFrames, func(i int) psStreamFrame {
		switch i % 4 {
		case 1, 3:
			return psStreamFrame{} // an SBR frame with no ps_data
		case 2:
			return frame34(i, i == 2, 1, shift(mixed, i, 2), icc2)
		}
		return frame20(i, i == 0 || i == 4, 1, shift(mixed, i, 2), icc2)
	}))
	return specs
}

// psADTS wraps a raw data block of a mono AAC-LC stream at 22050 Hz in an ADTS frame.
func psADTS(raw []byte) []byte {
	n := len(raw) + 7
	return append([]byte{
		0xFF, 0xF1, 1<<6 | 7<<2 | 0, 1<<6 | byte(n>>11)&3, byte(n >> 3), byte(n<<5) | 0x1F, 0xFC,
	}, raw...)
}

// psSplice puts a fill element that holds an SBR extension of nbits bits (payload b) in
// front of the end element of a raw data block.
func psSplice(t testing.TB, raw []byte, b []byte, nbits int) []byte {
	t.Helper()
	end := -1 // the bit index of the last set bit: the end element's last bit
	for i := 8*len(raw) - 1; i >= 0; i-- {
		if raw[i/8]>>(7-uint(i%8))&1 != 0 {
			end = i
			break
		}
	}
	if end < 2 || 8*len(raw)-end > 8 {
		t.Fatalf("no end element in the core frame (last set bit %d of %d)", end, 8*len(raw))
	}
	w := &sbrBW{}
	for i := 0; i < end-2; i++ {
		w.put(1, int(raw[i/8]>>(7-uint(i%8))&1))
	}
	nbytes := (4 + nbits + 7) / 8
	w.put(3, 6) // fill element
	if nbytes >= 15 {
		w.put(4, 15)
		w.put(8, nbytes-14)
	} else {
		w.put(4, nbytes)
	}
	w.put(4, 13) // EXT_SBR_DATA
	for i := 0; i < nbits; i++ {
		w.put(1, int(b[i/8]>>(7-uint(i%8))&1))
	}
	w.put(8*nbytes-4-nbits, 0)
	w.put(3, 7) // end element
	return w.b
}

// psBuildStream writes the ADTS stream of spec.
func psBuildStream(t testing.TB, spec psStreamSpec) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "aac", "ps", "core-"+spec.core+".aac"))
	if err != nil {
		t.Fatal(err)
	}
	var cores [][]byte
	for len(b) > 0 {
		h, err := parseADTS(b)
		if err != nil {
			t.Fatal(err)
		}
		cores = append(cores, b[h.headerLength:h.frameLength])
		b = b[h.frameLength:]
	}
	hdr := defaultSBRHdr()
	tb := sbrPayload{}.tables(t, sbrTestCore, hdr)
	enc := newPSEnc()
	var out []byte
	for i, f := range spec.frames {
		if f.noSBR {
			out = append(out, psADTS(cores[i%len(cores)])...)
			continue
		}
		p := sbrPayload{elem: aacIDSCE, grids: [2]sbrGrid{gridFixFix(1, 0), gridFixFix(1, 0)}, first: [2]int{20, 12}, ampKept: hdr.amp}
		if i == 0 || f.sbrHdr != nil {
			if f.sbrHdr != nil {
				hdr = *f.sbrHdr
			}
			tb = p.tables(t, sbrTestCore, hdr)
			h := hdr
			p.hdr = &h
		}
		switch {
		case f.corrupt:
			bad := psFrame{hdr: &psHdrSpec{iid: 1, icc: 1}, class: 1, envCode: 1, borders: []int{9, 3}}
			probe := newPSEnc()
			var sc sbrBW
			probe.write(t, &sc, bad)
			p.ext = &[2]int{(sc.n + 2 + 7) / 8, sbrExtPS}
			p.extBody = func(w *sbrBW) { newPSEnc().write(t, w, bad) }
		case f.ps != nil:
			probe := *enc
			var sc sbrBW
			probe.write(t, &sc, *f.ps)
			p.ext = &[2]int{(sc.n + 2 + 7) / 8, sbrExtPS}
			ps := *f.ps
			p.extBody = func(w *sbrBW) { enc.write(t, w, ps) }
		}
		payload, nbits := p.build(t, tb)
		out = append(out, psADTS(psSplice(t, cores[i%len(cores)], payload, nbits))...)
	}
	return out
}

func psStreamPath(name string) string {
	return filepath.Join("testdata", "aac", "ref", "ps-"+name+".f32")
}

func psSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:8])
}

// psStreamHashes pins the bytes of each written stream (the first 8 bytes of the
// SHA-256), so that a change of the writers that would make ffmpeg's recorded output
// the output of another stream fails the test and asks for a new recording.
var psStreamHashes = map[string]string{
	"S01":    "f5b7ccf1aea89d1d",
	"S02":    "26ec9d682c3ce235",
	"S03":    "6e5b523076ef40a3",
	"S04":    "c7003c91e3f7d323",
	"S05":    "2f903acd63e4f966",
	"S06":    "85306fd8671f00f6",
	"S06b":   "765b037160821748",
	"S19c":   "ef044a523e1129a7",
	"S07":    "0f81bdb178a9b0fc",
	"S08":    "81b2d58d8079858d",
	"S09c":   "52da94721a4283e3",
	"S09f":   "f65f469943eef98c",
	"S10":    "d2f9e87708f28455",
	"S11":    "c813c88524946804",
	"S12":    "8a45428604effbce",
	"S13a":   "c3673416987f257b",
	"S13b":   "0075af7a824ed6d5",
	"S14a":   "9f8db503e2e49cf3",
	"S14b":   "eee90e44c8e1f058",
	"S14c":   "6a67d9534cd82c27",
	"S15":    "b6c87d13ce9d9248",
	"S16a20": "2515622b45702c62",
	"S16a34": "260ebe7f8fd99edf",
	"S16b20": "27e790a9cf35fa9b",
	"S16b34": "02dcb0dc9bcca144",
	"S16c20": "c3b6cddc0818160a",
	"S16c34": "dcab8951c3821c7d",
	"S16f":   "d16d862c3ec105cd",
	"S16g":   "621e0c2c3e536dde",
	"S16h":   "1b6a7921359cd961",
	"S16d":   "8c0c9517a16f98b0",
	"S16e":   "7a67aa1964a20ea5",
	"S17":    "bbc6c96a1c572fec",
	"S17b":   "6c861bad4af1c55c",
	"S18b":   "1b3eaa98820f16b8",
	"S18c":   "4912b489dc24cc85",
	"S18d":   "51e66c59abbb234b",
	"S18e":   "b342417b820396d7",
	"S18":    "0d0148771894035c",
	"S19a":   "6477df9b99078106",
	"S19b":   "b0e571bc968d14e1",
}

// psStreamFile writes a stream where the decoder reads it.
func psStreamFile(t testing.TB, name string, b []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".aac")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPSRecordStreams writes the streams and ffmpeg's decode of them (PS_RECORD=<dir>):
// the .aac, the .f32 of the C code paths that testdata/aac/ref keeps, and a .neon.f32 of
// the default build, for the measure of ffmpeg's own float32 spread. It logs the lines
// of psStreamHashes.
func TestPSRecordStreams(t *testing.T) {
	dir := os.Getenv("PS_RECORD")
	if dir == "" {
		t.Skip("PS_RECORD=<directory> records ffmpeg's output for the written streams")
	}
	for _, spec := range psStreamSpecs() {
		b := psBuildStream(t, spec)
		in := filepath.Join(dir, spec.name+".aac")
		if err := os.WriteFile(in, b, 0o644); err != nil {
			t.Fatal(err)
		}
		for _, v := range []struct {
			suffix string
			flags  []string
		}{{".f32", []string{"-cpuflags", "0"}}, {".neon.f32", nil}} {
			args := append([]string{"-nostdin", "-v", "error", "-y"}, v.flags...)
			args = append(args, "-i", in, "-f", "f32le", filepath.Join(dir, spec.name+v.suffix))
			if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
				t.Fatalf("%s: ffmpeg: %v\n%s", spec.name, err, out)
			}
		}
		ref, err := os.ReadFile(filepath.Join(dir, spec.name+".f32"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(psStreamPath(spec.name), ref, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("\t%q: %q,", spec.name, psSHA(b))
	}
}

// psStreamCompare decodes the stream of spec with PS applied and compares it with the
// recording of ffmpeg's: the largest absolute difference per channel over the common
// samples.
func psStreamCompare(t testing.TB, spec psStreamSpec) (maxL, maxR float64, n int) {
	return psStreamCompareMode(t, spec, psTestMode{ffImage: spec.ffImage})
}

// psStreamCompareMode is psStreamCompare under the switches of mode.
func psStreamCompareMode(t testing.TB, spec psStreamSpec, mode psTestMode) (maxL, maxR float64, n int) {
	t.Helper()
	b := psBuildStream(t, spec)
	if want, ok := psStreamHashes[spec.name]; !ok || want != psSHA(b) {
		t.Fatalf("%s: stream hash %s is not the recorded %q: record ffmpeg's output again (PS_RECORD)", spec.name, psSHA(b), want)
	}
	cfg, frames := aacADTSFrames(t, psStreamFile(t, spec.name, b))
	l, r, _ := psDecodeFramesMode(t, cfg, frames, psStreamFrames, mode)
	fl, fr := psReadF32Stereo(t, psStreamPath(spec.name))
	if len(fl) != len(l) {
		t.Fatalf("%s: %d samples, ffmpeg gives %d", spec.name, len(l), len(fl))
	}
	for i := range l {
		maxL = math.Max(maxL, math.Abs(float64(l[i])-float64(fl[i])))
		maxR = math.Max(maxR, math.Abs(float64(r[i])-float64(fr[i])))
	}
	return maxL, maxR, len(l)
}

// psStreamBound is the bound against ffmpeg on the written streams: the SBR bound, which
// the measure of the float32 terms of section 7.5 supports (the table of the pull
// request).
const psStreamBound = 1e-6

// TestPSStreamsAgainstFFmpeg: every written stream decoded with PS applied is within
// the bound of ffmpeg 9.0.2's PCM, per sample, on both channels. The streams whose
// difference is expected (IPD/OPD with phases, procedure B, frames without data) log it.
func TestPSStreamsAgainstFFmpeg(t *testing.T) {
	for _, spec := range psStreamSpecs() {
		t.Run(spec.name, func(t *testing.T) {
			l, r, n := psStreamCompare(t, spec)
			t.Logf("%s: %d samples per channel, largest difference L %.2e R %.2e", spec.name, n, l, r)
			if spec.bounded && (l > psStreamBound || r > psStreamBound) {
				t.Errorf("%s: differs from ffmpeg by L %.2e, R %.2e (bound %.0e)", spec.name, l, r, psStreamBound)
			}
		})
	}
}

// TestPSBaselineSwitch: the test switch psBaselineTest gives baseline mixing (procedure A
// for every ICC mode, IPD/OPD ignored), which the default full profile differs from on
// the streams that use procedure B (S18) or phases (S16b20); it is the same on a stream
// that uses neither (S09c: ICC mode 1, no phases).
func TestPSBaselineSwitch(t *testing.T) {
	run := func(name string, mode psTestMode) [2][]float32 {
		for _, spec := range psStreamSpecs() {
			if spec.name == name {
				cfg, fr := aacADTSFrames(t, psStreamFile(t, name, psBuildStream(t, spec)))
				l, r, _ := psDecodeFramesMode(t, cfg, fr, psStreamFrames, mode)
				return [2][]float32{l, r}
			}
		}
		t.Fatalf("no stream %s", name)
		return [2][]float32{}
	}
	for _, name := range []string{"S18", "S16b20"} {
		full, base := run(name, psTestMode{}), run(name, psTestMode{baseline: true})
		if slices.Equal(full[0], base[0]) && slices.Equal(full[1], base[1]) {
			t.Errorf("%s: the baseline switch changes nothing", name)
		}
		if slices.Equal(base[0], base[1]) {
			t.Errorf("%s: baseline mixing gives no stereo image", name)
		}
	}
	// IPD/OPD ignored: phases or none, the same samples; and a stream without either
	a, b := run("S16a20", psTestMode{baseline: true}), run("S16b20", psTestMode{baseline: true})
	if !slices.Equal(a[0], b[0]) || !slices.Equal(a[1], b[1]) {
		t.Error("baseline mixing: IPD/OPD with phases changed the output")
	}
	full, base := run("S09c", psTestMode{}), run("S09c", psTestMode{baseline: true})
	if !slices.Equal(full[0], base[0]) || !slices.Equal(full[1], base[1]) {
		t.Error("S09c uses neither procedure B nor phases: the profiles must agree")
	}
}

// TestPSImageRowRule (Q5): the default image-row rule of IPD/OPD (the conjugate gains on
// the rows of negative centre frequency, which ISO's al_sbr_ps_04 supports) differs from
// ffmpeg's on streams that put phases on those rows, in the left channel (the right
// carries opd - ipd); and the streams whose image bands have phase 0 agree with ffmpeg
// to the bound under both rules.
func TestPSImageRowRule(t *testing.T) {
	for _, spec := range psStreamSpecs() {
		switch spec.name {
		case "S16b20", "S16b34", "S16c20":
			l, r, _ := psStreamCompareMode(t, spec, psTestMode{})
			if l < 1e-3 && r < 1e-3 {
				t.Errorf("%s: the default rule is as close to ffmpeg's as the bound (L %.2e R %.2e): the rules do not differ here", spec.name, l, r)
			}
			t.Logf("%s under the specification's image-row rule: L %.2e R %.2e from ffmpeg's", spec.name, l, r)
		case "S16a20", "S16a34":
			l, r, _ := psStreamCompareMode(t, spec, psTestMode{})
			if l > psStreamBound || r > psStreamBound {
				t.Errorf("%s: phases 0 on the image bands, but differs from ffmpeg by L %.2e R %.2e", spec.name, l, r)
			}
		}
	}
}

var _ = fmt.Sprint
