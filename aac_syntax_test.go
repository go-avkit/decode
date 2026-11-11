// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"math"
	"math/rand"
	"strings"
	"testing"
)

// The writers below are the inverse of the parser, written from the syntax
// tables, so a stream can be built with exactly the fields a test needs.

// aacRate44 is the sampling_frequency_index of 44100 Hz.
const aacRate44 = 4

// sym writes the codeword of a symbol of a spectrum codebook (1 to 11).
func (w *bitWriter) sym(cb, idx int) {
	w.bits(uint(aacSpectralCodes[cb-1][idx]), int(aacSpectralBits[cb-1][idx]))
}

// sfDelta writes a scalefactor difference.
func (w *bitWriter) sfDelta(d int) {
	w.bits(uint(aacScalefactorCodes[d+60]), int(aacScalefactorBits[d+60]))
}

// info writes ics_info with no prediction.
func (w *bitWriter) info(seq, shape, maxSfb, grouping int) {
	w.bits(0, 1)
	w.bits(uint(seq), 2)
	w.bits(uint(shape), 1)
	if seq == aacEightShort {
		w.bits(uint(maxSfb), 4)
		w.bits(uint(grouping), 7)
		return
	}
	w.bits(uint(maxSfb), 6)
	w.bits(0, 1)
}

// section writes one section of a section_data.
func (w *bitWriter) section(short bool, cb, n int) {
	bitsN, esc := 5, 31
	if short {
		bitsN, esc = 3, 7
	}
	w.bits(uint(cb), 4)
	for n >= esc {
		w.bits(uint(esc), bitsN)
		n -= esc
	}
	w.bits(uint(n), bitsN)
}

// band writes the spectral values of a band: codewords, then signs, then escapes.
func (w *bitWriter) band(cb int, vals []int32) {
	dim, base, off := 2, 0, 0
	switch cb {
	case 1, 2:
		dim, base, off = 4, 3, 1
	case 3, 4:
		dim, base = 4, 3
	case 5, 6:
		base, off = 9, 4
	case 7, 8:
		base = 8
	case 9, 10:
		base = 13
	case 11:
		base = 17
	}
	unsigned := cb >= 3 && cb != 5 && cb != 6
	for i := 0; i < len(vals); i += dim {
		idx := 0
		for _, v := range vals[i : i+dim] {
			a := int(v)
			if unsigned {
				a = int(math.Abs(float64(v)))
				if cb == 11 {
					a = min(a, 16)
				}
			}
			idx = idx*base + a + off
		}
		w.sym(cb, idx)
		if unsigned {
			for _, v := range vals[i : i+dim] {
				if v != 0 {
					s := uint(0)
					if v < 0 {
						s = 1
					}
					w.bits(s, 1)
				}
			}
		}
		if cb == 11 {
			for _, v := range vals[i : i+dim] {
				if m := int(math.Abs(float64(v))); m >= 16 {
					n := 4
					for m>>uint(n+1) != 0 {
						n++
					}
					w.bits((1<<uint(n-4))-1, n-4)
					w.bits(0, 1)
					w.bits(uint(m-1<<uint(n)), n)
				}
			}
		}
	}
}

// bandLimit is the largest magnitude codebook cb can state without an escape.
func bandLimit(cb int) int32 {
	return [...]int32{0, 1, 1, 2, 2, 4, 4, 7, 7, 12, 12, 8191}[cb]
}

// tChan is a channel stream for the writer: bands carry what the stream states.
type tChan struct {
	gain    int
	seq     int
	maxSfb  int
	group   int // scale_factor_grouping of eight short windows
	secs    [][]tSec
	sf      []tSF // scalefactor_data in order
	spec    []tSpec
	pulse   []int // number of pulses, start sfb, then offset and amplitude pairs
	tns     []int // raw bits as (value, width) pairs
	gainCtl bool
}

type tSec struct{ cb, n int }
type tSF struct {
	delta int
	raw9  int // >= 0: the first noise energy, written as 9 bits
}
type tSpec struct {
	cb   int
	vals []int32
}

// write writes the channel after its element header; info is written when common
// is false.
func (c *tChan) write(w *bitWriter, common bool) {
	w.bits(uint(c.gain), 8)
	if !common {
		w.info(c.seq, 0, c.maxSfb, c.group)
	}
	for _, g := range c.secs {
		for _, s := range g {
			w.section(c.seq == aacEightShort, s.cb, s.n)
		}
	}
	for _, s := range c.sf {
		if s.raw9 >= 0 {
			w.bits(uint(s.raw9), 9)
		} else {
			w.sfDelta(s.delta)
		}
	}
	if c.pulse != nil {
		w.bits(1, 1)
		w.bits(uint(c.pulse[0]-1), 2)
		w.bits(uint(c.pulse[1]), 6)
		for i := 2; i+1 < len(c.pulse); i += 2 {
			w.bits(uint(c.pulse[i]), 5)
			w.bits(uint(c.pulse[i+1]), 4)
		}
	} else {
		w.bits(0, 1)
	}
	if c.tns != nil {
		w.bits(1, 1)
		for i := 0; i+1 < len(c.tns); i += 2 {
			w.bits(uint(c.tns[i]), c.tns[i+1])
		}
	} else {
		w.bits(0, 1)
	}
	if c.gainCtl {
		w.bits(1, 1)
	} else {
		w.bits(0, 1)
	}
	for _, s := range c.spec {
		w.band(s.cb, s.vals)
	}
}

// finish writes the end element and returns the frame.
func (w *bitWriter) finish() []byte {
	w.bits(aacIDEND, 3)
	return w.bytes()
}

func newTestParser(t testing.TB, rateIndex, channels int) *aacParser {
	t.Helper()
	p, err := newAACParser(aacConfig{rateIndex: rateIndex, rate: aacRates[rateIndex], channels: channels}, DefaultMaxAudioChannels)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func wantErr(t *testing.T, what string, err, kind error, text string) {
	t.Helper()
	if !errors.Is(err, kind) || !strings.Contains(err.Error(), text) {
		t.Errorf("%s: err = %v, want %v containing %q", what, err, kind, text)
	}
}

// longChannel builds a long-window channel that uses every codebook, with the
// spectrum it states, the scalefactors and the expected quantised values.
func longChannel(rng *rand.Rand) (c tChan, q [aacFrameLen]int32, cb [aacMaxSfb]uint8, sf [aacMaxSfb]int16) {
	swb := aacSwbLongTables[aacSwbLongIndex[aacRate44]]
	c = tChan{gain: 100, seq: aacOnlyLong, maxSfb: 48}
	normal := 100
	add := func(sfb, cbook int) {
		cb[sfb] = uint8(cbook)
		switch {
		case cbook == 0:
		case cbook == aacBandNoise:
			c.sf = append(c.sf, tSF{raw9: 256 + 5})
			sf[sfb] = int16(100 - 90 + 5)
		case cbook >= aacBandIntens:
			d := rng.Intn(7) - 3
			c.sf = append(c.sf, tSF{delta: d, raw9: -1})
			sf[sfb] = int16(d)
		default:
			d := rng.Intn(7) - 3
			normal += d
			c.sf = append(c.sf, tSF{delta: d, raw9: -1})
			sf[sfb] = int16(normal)
			vals := make([]int32, swb[sfb+1]-swb[sfb])
			lim := bandLimit(cbook)
			for i := range vals {
				vals[i] = int32(rng.Intn(int(2*lim+1))) - lim
				if cbook == 11 && rng.Intn(3) == 0 {
					vals[i] = int32(rng.Intn(2*300)) - 300 // escapes
				}
			}
			if cbook == 11 {
				vals[0] = 8191
				vals[1] = -16
			}
			copy(q[swb[sfb]:], vals)
			c.spec = append(c.spec, tSpec{cbook, vals})
		}
	}
	for sfb := 0; sfb < 11; sfb++ {
		c.secs = append(c.secs, nil)
		add(sfb, sfb+1)
	}
	add(11, 0)
	add(12, aacBandNoise)
	add(13, 14)
	add(14, 15)
	for sfb := 15; sfb < 48; sfb++ {
		add(sfb, 9)
	}
	secs := []tSec{}
	for sfb := 0; sfb < 11; sfb++ {
		secs = append(secs, tSec{sfb + 1, 1})
	}
	secs = append(secs, tSec{0, 1}, tSec{aacBandNoise, 1}, tSec{14, 1}, tSec{15, 1}, tSec{9, 33})
	c.secs = [][]tSec{secs}
	// The scalefactors are written in band order, which is how add was called,
	// but the noise and intensity deltas interleave: intensity deltas accumulate
	// on their own. Recompute their expected positions.
	pos := 0
	for sfb := 0; sfb < 48; sfb++ {
		if cb[sfb] >= aacBandIntens {
			// add stored the delta; the position is the running sum
			pos += int(sf[sfb])
			sf[sfb] = int16(pos)
		}
	}
	return c, q, cb, sf
}

func TestAACRoundTripLong(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 20; iter++ {
		c, q, cb, sf := longChannel(rng)
		w := &bitWriter{}
		w.bits(aacIDSCE, 3)
		w.bits(3, 4)
		c.write(w, false)
		p := newTestParser(t, aacRate44, 1)
		els, err := p.parseFrame(w.finish())
		if err != nil {
			t.Fatal(err)
		}
		if len(els) != 1 || els[0].id != aacIDSCE || els[0].tag != 3 || els[0].nch != 1 {
			t.Fatalf("elements = %+v", els)
		}
		ch := &els[0].ch[0]
		if ch.q != q {
			for i := range q {
				if ch.q[i] != q[i] {
					t.Fatalf("q[%d] = %d, want %d", i, ch.q[i], q[i])
				}
			}
		}
		for sfb := 0; sfb < 48; sfb++ {
			if ch.cb[0][sfb] != cb[sfb] || ch.sf[0][sfb] != sf[sfb] {
				t.Fatalf("band %d: cb %d sf %d, want %d %d", sfb, ch.cb[0][sfb], ch.sf[0][sfb], cb[sfb], sf[sfb])
			}
		}
		if p.used != len(w.bytes()) {
			t.Fatalf("used %d of %d bytes", p.used, len(w.bytes()))
		}
		// Dequantisation against the formula in float64: the product of two float32
		// roundings is within two ulp of it.
		var out [aacFrameLen]float32
		ch.dequantise(&out, nil)
		swb := ch.ics.swb
		for sfb := 0; sfb < 48; sfb++ {
			for i := int(swb[sfb]); i < int(swb[sfb+1]); i++ {
				var want float64
				if cb[sfb] != 0 && cb[sfb] < aacBandNoise {
					want = math.Copysign(math.Pow(math.Abs(float64(q[i])), 4.0/3.0), float64(q[i])) * math.Exp2(float64(sf[sfb]-100)/4)
				}
				if got := float64(out[i]); got != want && math.Abs(got-want) > 2*math.Abs(want)*0x1p-23 {
					t.Fatalf("coefficient %d (band %d, q %d): %v, want %v", i, sfb, q[i], got, want)
				}
			}
		}
	}
}

// TestAACRoundTripShort groups eight short windows and gives each group its own
// sections, in the order the syntax interleaves their coefficients.
func TestAACRoundTripShort(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	swb := aacSwbShortTables[aacSwbShortIndex[aacRate44]]
	groups := []int{2, 2, 1, 1, 2}
	c := tChan{gain: 120, seq: aacEightShort, maxSfb: 14, group: 0b1010001}
	var q [aacFrameLen]int32
	normal := 120
	var wantSf [5][14]int16
	w0 := 0
	for g, gl := range groups {
		c.secs = append(c.secs, []tSec{{5, 7}, {0, 2}, {11, 5}})
		for sfb := 0; sfb < 14; sfb++ {
			cb := 5
			switch {
			case sfb >= 9:
				cb = 11
			case sfb >= 7:
				cb = 0
			}
			if cb == 0 {
				continue
			}
			d := rng.Intn(5) - 2
			normal += d
			wantSf[g][sfb] = int16(normal)
			c.sf = append(c.sf, tSF{delta: d, raw9: -1})
			for k := 0; k < gl; k++ {
				vals := make([]int32, swb[sfb+1]-swb[sfb])
				lim := bandLimit(cb)
				for i := range vals {
					vals[i] = int32(rng.Intn(int(2*lim+1))) - lim
					if cb == 11 {
						vals[i] = int32(rng.Intn(100)) - 50
					}
				}
				copy(q[(w0+k)*128+int(swb[sfb]):], vals)
				c.spec = append(c.spec, tSpec{cb, vals})
			}
		}
		w0 += gl
	}
	w := &bitWriter{}
	w.bits(aacIDLFE, 3)
	w.bits(1, 4)
	c.write(w, false)
	p := newTestParser(t, aacRate44, 1)
	els, err := p.parseFrame(w.finish())
	if err != nil {
		t.Fatal(err)
	}
	ch := &els[0].ch[0]
	if els[0].id != aacIDLFE || ch.ics.numGroups != 5 || ch.ics.numWindows != 8 || ch.ics.groupLen != [8]uint8{2, 2, 1, 1, 2} {
		t.Fatalf("ics = %+v", ch.ics)
	}
	if ch.q != q {
		t.Fatal("short window spectrum differs")
	}
	for g := range wantSf {
		for sfb := range wantSf[g] {
			if ch.sf[g][sfb] != wantSf[g][sfb] {
				t.Fatalf("sf[%d][%d] = %d, want %d", g, sfb, ch.sf[g][sfb], wantSf[g][sfb])
			}
		}
	}
	var out [aacFrameLen]float32
	ch.dequantise(&out, nil)
	for i := range out {
		if (q[i] == 0) != (out[i] == 0) {
			t.Fatalf("coefficient %d: q %d, out %v", i, q[i], out[i])
		}
	}
	// the second window of the first group is where the interleaving shows: its
	// first band is the one after the first window's, in the same group
	if out[128+int(swb[0])] == 0 && q[128+int(swb[0])] != 0 {
		t.Fatal("window 1 lost")
	}
}

func TestAACPairCommonAndMS(t *testing.T) {
	for _, mask := range []int{0, 1, 2} {
		w := &bitWriter{}
		w.bits(aacIDCPE, 3)
		w.bits(0, 4)
		w.bits(1, 1) // common_window
		w.info(aacOnlyLong, 1, 3, 0)
		w.bits(uint(mask), 2)
		if mask == 1 {
			w.bits(0b101, 3)
		}
		for ch := 0; ch < 2; ch++ {
			c := tChan{gain: 90, maxSfb: 3, secs: [][]tSec{{{1, 3}}}}
			for sfb := 0; sfb < 3; sfb++ {
				c.sf = append(c.sf, tSF{delta: 0, raw9: -1})
				c.spec = append(c.spec, tSpec{1, []int32{1, 0, -1, 0}})
			}
			c.write(w, true)
		}
		p := newTestParser(t, aacRate44, 2)
		els, err := p.parseFrame(w.finish())
		if err != nil {
			t.Fatal(err)
		}
		e := els[0]
		if !e.common || e.msMask != uint8(mask) || e.nch != 2 || e.ch[1].ics.maxSfb != 3 || e.ch[1].ics.windowShape != 1 {
			t.Fatalf("mask %d: element %+v", mask, e.ch[1].ics)
		}
		if mask == 1 && (!e.msUsed[0][0] || e.msUsed[0][1] || !e.msUsed[0][2]) {
			t.Fatalf("ms flags = %v", e.msUsed[0][:3])
		}
		if e.ch[1].q[0] != 1 || e.ch[1].q[2] != -1 {
			t.Fatalf("second channel q = %v", e.ch[1].q[:4])
		}
	}
	// without a common window each channel states its own ics_info
	w := &bitWriter{}
	w.bits(aacIDCPE, 3)
	w.bits(0, 4)
	w.bits(0, 1)
	for ch := 0; ch < 2; ch++ {
		c := tChan{gain: 90, maxSfb: 1, secs: [][]tSec{{{0, 1}}}}
		c.write(w, false)
	}
	els, err := newTestParser(t, aacRate44, 2).parseFrame(w.finish())
	if err != nil || els[0].common {
		t.Fatalf("separate infos: %v", err)
	}
}

func TestAACPulse(t *testing.T) {
	// two pulses on band 2 (offset 8 at 44.1 kHz): the first lands on a positive
	// value, the second on a zero
	c := tChan{gain: 100, maxSfb: 4, secs: [][]tSec{{{5, 4}}}, pulse: []int{2, 2, 1, 3, 2, 7}}
	var vals [4][]int32
	for sfb := range vals {
		vals[sfb] = []int32{0, 0, 0, 0}
		c.sf = append(c.sf, tSF{delta: 0, raw9: -1})
	}
	vals[2] = []int32{0, 1, 0, 0}
	for sfb := range vals {
		c.spec = append(c.spec, tSpec{5, vals[sfb]})
	}
	w := &bitWriter{}
	w.bits(aacIDSCE, 3)
	w.bits(0, 4)
	c.write(w, false)
	els, err := newTestParser(t, aacRate44, 1).parseFrame(w.finish())
	if err != nil {
		t.Fatal(err)
	}
	q := els[0].ch[0].q
	if q[9] != 1+3 || q[11] != -7 || q[8] != 0 {
		t.Fatalf("q[8:12] = %v", q[8:12])
	}
}

func TestAACTNS(t *testing.T) {
	// long: two filters in the one window, the first of order 2, the second of
	// order 0; coefficients of 4 bits with compression off, 3 bits with it on
	long := []int{2, 2, 1, 1, 10, 6, 2, 5, 1, 1, 0, 1, 0, 4, 15, 4, 3, 6, 0, 5}
	c := tChan{gain: 100, maxSfb: 1, secs: [][]tSec{{{0, 1}}}, tns: long}
	w := &bitWriter{}
	w.bits(aacIDSCE, 3)
	w.bits(0, 4)
	c.write(w, false)
	els, err := newTestParser(t, aacRate44, 1).parseFrame(w.finish())
	if err != nil {
		t.Fatal(err)
	}
	tns := els[0].ch[0].tns
	f := tns.filt[0][0]
	if !tns.present || tns.n[0] != 2 || f.length != 10 || f.order != 2 || !f.down || f.compress != 0 || f.res != 4 || f.coef[0] != 0 || f.coef[1] != 15 {
		t.Fatalf("tns = %+v", tns)
	}
	if g := tns.filt[0][1]; g.length != 3 || g.order != 0 {
		t.Fatalf("second filter = %+v", g)
	}
	// short: one filter in each of the eight windows, in a window without one
	var short []int
	for win := 0; win < 8; win++ {
		if win == 3 {
			short = append(short, 0, 1)
			continue
		}
		short = append(short, 1, 1) // n_filt
		short = append(short, 0, 1) // coef_res: 3 bits
		short = append(short, 5, 4) // length
		short = append(short, 1, 3) // order
		short = append(short, 0, 1) // direction
		short = append(short, 1, 1) // coef_compress: 2 bit coefficients
		short = append(short, 2, 2) // coefficient
	}
	cs := tChan{gain: 100, seq: aacEightShort, maxSfb: 1, group: 0, secs: [][]tSec{{{0, 1}}, {{0, 1}}, {{0, 1}}, {{0, 1}}, {{0, 1}}, {{0, 1}}, {{0, 1}}, {{0, 1}}}, tns: short}
	w = &bitWriter{}
	w.bits(aacIDSCE, 3)
	w.bits(0, 4)
	cs.write(w, false)
	els, err = newTestParser(t, aacRate44, 1).parseFrame(w.finish())
	if err != nil {
		t.Fatal(err)
	}
	tns = els[0].ch[0].tns
	if tns.n[3] != 0 || tns.n[0] != 1 || tns.filt[7][0].length != 5 || tns.filt[7][0].order != 1 || tns.filt[7][0].coef[0] != 2 || tns.filt[7][0].compress != 1 || tns.filt[7][0].res != 3 {
		t.Fatalf("short tns = %+v", tns.filt[7][0])
	}
}

func TestAACSkippedElements(t *testing.T) {
	w := &bitWriter{}
	// a data_stream_element of 3 bytes, byte aligned, then one of 255+2 bytes
	w.bits(aacIDDSE, 3)
	w.bits(5, 4)
	w.bits(1, 1)
	w.bits(3, 8)
	w.bits(0, 0)
	for w.nbit%8 != 0 {
		w.bits(0, 1)
	}
	w.bits(0xabcdef, 24)
	w.bits(aacIDDSE, 3)
	w.bits(5, 4)
	w.bits(0, 1)
	w.bits(255, 8)
	w.bits(2, 8)
	for i := 0; i < 257; i++ {
		w.bits(0x5a, 8)
	}
	// a fill element of 20 bytes (count 15 plus an escape), an extension of type 0xB
	w.bits(aacIDFIL, 3)
	w.bits(15, 4)
	w.bits(6, 8)
	w.bits(0xb, 4)
	for i := 0; i < 20*8-4; i++ {
		w.bits(1, 1)
	}
	// an empty fill element
	w.bits(aacIDFIL, 3)
	w.bits(0, 4)
	// a program config element with every list
	w.bits(aacIDPCE, 3)
	pcePayload(w)
	p := newTestParser(t, aacRate44, 1)
	els, err := p.parseFrame(w.finish())
	if err != nil {
		t.Fatal(err)
	}
	if len(els) != 0 || p.pce == nil || p.pce.channels() != 6 || p.pce.rateIndex != 3 {
		t.Fatalf("elements %d, pce %+v", len(els), p.pce)
	}
	if p.used != len(w.bytes()) {
		t.Fatalf("used %d of %d", p.used, len(w.bytes()))
	}
}

// pcePayload writes a program_config_element after its id: one front CPE, one
// front SCE, one side SCE, one back SCE, one LFE, an associated data and a
// coupling channel, mixdowns, and a 2 byte comment.
func pcePayload(w *bitWriter) {
	w.bits(0, 4) // tag
	w.bits(1, 2) // profile
	w.bits(3, 4) // rate 48000
	w.bits(2, 4) // front
	w.bits(1, 4) // side
	w.bits(1, 4) // back
	w.bits(1, 2) // lfe
	w.bits(1, 3) // assoc
	w.bits(1, 4) // cc
	w.bits(1, 1) // mono mixdown
	w.bits(0, 4)
	w.bits(1, 1) // stereo mixdown
	w.bits(0, 4)
	w.bits(1, 1) // matrix mixdown
	w.bits(0, 3)
	w.bits(1, 1) // front 0 is a CPE
	w.bits(0, 4)
	w.bits(0, 1) // front 1 is an SCE
	w.bits(1, 4)
	w.bits(0, 1) // side
	w.bits(2, 4)
	w.bits(0, 1) // back
	w.bits(3, 4)
	w.bits(0, 4) // lfe
	w.bits(0, 4) // assoc
	w.bits(0, 1) // cc: is_cpe/independent
	w.bits(0, 4)
	for w.nbit%8 != 0 {
		w.bits(0, 1)
	}
	w.bits(2, 8)
	w.bits(0x4142, 16)
}

func TestAACFrameRefusals(t *testing.T) {
	type tc struct {
		name string
		kind error
		text string
		mk   func(w *bitWriter)
		pr   func() *aacParser
	}
	std := func() *aacParser { return newTestParser(t, aacRate44, 2) }
	// cut, when a case sets it, is the bit at which the stream is cut: whatever the
	// case wrote after it is dropped, to the byte, so the parser runs out of bits
	// where the case says.
	cut := 0
	ics := func(w *bitWriter, c tChan) {
		w.bits(aacIDSCE, 3)
		w.bits(0, 4)
		c.write(w, false)
	}
	zero := func(sfbs int) tChan {
		c := tChan{gain: 100, maxSfb: sfbs}
		if sfbs > 0 {
			c.secs = [][]tSec{{{0, sfbs}}}
		}
		return c
	}
	cases := []tc{
		{"coupling element", ErrAudioUnsupported, "coupling", func(w *bitWriter) { w.bits(aacIDCCE, 3) }, std},
		{"SBR in a fill element", ErrAudioUnsupported, "SBR", func(w *bitWriter) {
			w.bits(aacIDFIL, 3)
			w.bits(2, 4)
			w.bits(13, 4)
			w.bits(0, 12)
		}, std},
		{"SBR with CRC", ErrAudioUnsupported, "SBR", func(w *bitWriter) {
			w.bits(aacIDFIL, 3)
			w.bits(1, 4)
			w.bits(14, 4)
			w.bits(0, 4)
		}, std},
		{"truncated PCE", ErrAudioSample, "truncated", func(w *bitWriter) {
			w.bits(aacIDPCE, 3)
			w.bits(0xffffff, 24)
		}, std},
		{"no end element", ErrAudioSample, "truncated", func(w *bitWriter) {
			w.bits(aacIDFIL, 3)
			w.bits(0, 4)
			w.bits(0, 1) // pads the byte; the next three bits read as a SCE of nothing
		}, std},
		{"truncated skip", ErrAudioSample, "truncated", func(w *bitWriter) {
			w.bits(aacIDDSE, 3)
			w.bits(0, 4)
			w.bits(0, 1)
			w.bits(100, 8)
		}, std},
		{"too many channels", ErrOverLimit, "channels", func(w *bitWriter) {
			for i := 0; i < 2; i++ {
				ics(w, zero(0))
			}
			w.bits(aacIDCPE, 3)
		}, func() *aacParser {
			p, err := newAACParser(aacConfig{rateIndex: aacRate44, channels: 2}, 2)
			if err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"max_sfb past the table", ErrAudioSample, "max_sfb", func(w *bitWriter) { ics(w, zero(60)) }, std},
		{"prediction", ErrAudioUnsupported, "prediction", func(w *bitWriter) {
			w.bits(aacIDSCE, 3)
			w.bits(0, 4)
			w.bits(100, 8)
			w.bits(0, 1)
			w.bits(0, 2)
			w.bits(0, 1)
			w.bits(0, 6)
			w.bits(1, 1)
		}, std},
		{"truncated ics_info", ErrAudioSample, "truncated", func(w *bitWriter) {
			w.bits(aacIDSCE, 3)
			w.bits(0, 4)
			w.bits(100, 8)
			w.bits(0, 3)
		}, std},
		{"codebook 12", ErrAudioSample, "codebook 12", func(w *bitWriter) {
			c := zero(1)
			c.secs = [][]tSec{{{12, 1}}}
			ics(w, c)
		}, std},
		{"section past max_sfb", ErrAudioSample, "past max_sfb", func(w *bitWriter) {
			c := zero(2)
			c.secs = [][]tSec{{{0, 3}}}
			ics(w, c)
		}, std},
		{"truncated sections", ErrAudioSample, "truncated", func(w *bitWriter) {
			w.bits(aacIDSCE, 3)
			w.bits(0, 4)
			w.bits(100, 8)
			w.info(aacOnlyLong, 0, 2, 0)
			w.bits(0, 4)
			w.bits(0, 5)
			w.bits(0, 8)
		}, std},
		{"truncated scalefactors", ErrAudioSample, "truncated", func(w *bitWriter) {
			w.bits(aacIDSCE, 3)
			w.bits(0, 4)
			w.bits(100, 8)
			w.info(aacOnlyLong, 0, 1, 0)
			w.section(false, 3, 1)
			// the scalefactor is ones, which is the start of a 19 bit code, and the
			// stream is cut inside it
			w.bits(0xffff, 16)
			cut = 3 + 4 + 8 + 11 + 9 + 5
		}, std},
		{"scalefactor out of range", ErrAudioSample, "out of range", func(w *bitWriter) {
			c := zero(1)
			c.gain = 0
			c.secs = [][]tSec{{{3, 1}}}
			c.sf = []tSF{{delta: -1, raw9: -1}}
			ics(w, c)
		}, std},
		{"pulse in short windows", ErrAudioSample, "eight short", func(w *bitWriter) {
			ics(w, tChan{gain: 100, seq: aacEightShort, pulse: []int{1, 0, 0, 0}})
		}, std},
		{"pulse start band", ErrAudioSample, "pulse_start_sfb", func(w *bitWriter) { ics(w, tChan{gain: 100, maxSfb: 0, pulse: []int{1, 63, 0, 0}}) }, std},
		{"pulse position", ErrAudioSample, "pulse position", func(w *bitWriter) {
			c := tChan{gain: 100, maxSfb: 0, pulse: []int{4, 48, 31, 0, 31, 0, 31, 0, 31, 0}}
			ics(w, c)
		}, std},
		{"TNS order", ErrAudioSample, "TNS filter of order", func(w *bitWriter) {
			c := zero(0)
			c.tns = []int{1, 2, 0, 1, 5, 6, 13, 5}
			ics(w, c)
		}, std},
		{"truncated TNS", ErrAudioSample, "truncated", func(w *bitWriter) {
			w.bits(aacIDSCE, 3)
			w.bits(0, 4)
			// n_filt 1, 4 bit coefficients, length 5, order 2, up, no compression:
			// eight coefficient bits are due and the stream is cut before them
			c := zero(0)
			c.tns = []int{1, 2, 1, 1, 5, 6, 2, 5, 0, 1, 0, 1}
			c.write(w, false)
			cut = 3 + 4 + 8 + 11 + 1 + 1 + 16
		}, std},
		{"gain control", ErrAudioUnsupported, "gain control", func(w *bitWriter) {
			c := zero(0)
			c.gainCtl = true
			ics(w, c)
		}, std},
		{"truncated spectrum", ErrAudioSample, "truncated", func(w *bitWriter) {
			// ten bands of four values need at least twenty bits of codewords and
			// the stream has none: only the padding of its last byte
			c := zero(10)
			c.secs = [][]tSec{{{5, 10}}}
			for i := 0; i < 10; i++ {
				c.sf = append(c.sf, tSF{delta: 0, raw9: -1})
			}
			w.bits(aacIDSCE, 3)
			w.bits(0, 4)
			c.write(w, false)
		}, std},
		{"escape too long", ErrAudioSample, "escape", func(w *bitWriter) {
			c := zero(1)
			c.secs = [][]tSec{{{11, 1}}}
			c.sf = []tSF{{delta: 0, raw9: -1}}
			w.bits(aacIDSCE, 3)
			w.bits(0, 4)
			c.write(w, false)
			w.sym(11, 16*17) // the pair (16, 0), its sign, then ten ones: no zero ends the escape
			w.bits(0, 1)
			w.bits(0xffff, 16)
		}, std},
		{"reserved ms_mask_present", ErrAudioSample, "ms_mask_present", func(w *bitWriter) {
			w.bits(aacIDCPE, 3)
			w.bits(0, 4)
			w.bits(1, 1)
			w.info(aacOnlyLong, 0, 0, 0)
			w.bits(3, 2)
		}, std},
		{"pair: common ics_info refused", ErrAudioSample, "max_sfb", func(w *bitWriter) {
			w.bits(aacIDCPE, 3)
			w.bits(0, 4)
			w.bits(1, 1)
			w.info(aacOnlyLong, 0, 60, 0)
		}, std},
		{"intensity position truncated", ErrAudioSample, "truncated", func(w *bitWriter) {
			w.bits(aacIDSCE, 3)
			w.bits(0, 4)
			w.bits(100, 8)
			w.info(aacOnlyLong, 0, 1, 0)
			w.section(false, 14, 1)
			w.bits(0xffff, 16)
			cut = 3 + 4 + 8 + 11 + 9 + 5
		}, std},
		{"noise energy delta truncated", ErrAudioSample, "truncated", func(w *bitWriter) {
			w.bits(aacIDSCE, 3)
			w.bits(0, 4)
			w.bits(100, 8)
			w.info(aacOnlyLong, 0, 2, 0)
			w.section(false, aacBandNoise, 2)
			w.bits(256, 9) // the first band's energy; the second's delta follows
			w.bits(0xffff, 16)
			cut = 3 + 4 + 8 + 11 + 9 + 9 + 5
		}, std},
		{"truncated pulse data", ErrAudioSample, "truncated", func(w *bitWriter) {
			w.bits(aacIDSCE, 3)
			w.bits(0, 4)
			w.bits(100, 8)
			w.info(aacOnlyLong, 0, 0, 0)
			w.bits(1, 1) // pulse_data_present, and 17 bits of it are due
			cut = 3 + 4 + 8 + 11 + 1
		}, std},
		{"stream ends before the gain control flag", ErrAudioSample, "truncated", func(w *bitWriter) {
			// eight short windows: ics_info is 15 bits, so the three flags end the
			// 32 bits that are there, and the gain control flag is the first past them
			w.bits(aacIDSCE, 3)
			w.bits(0, 4)
			w.bits(100, 8)
			w.info(aacEightShort, 0, 0, 0)
			w.bits(0, 2)
			cut = 3 + 4 + 8 + 15 + 2
		}, std},
		{"pair: first channel refused", ErrAudioSample, "codebook 12", func(w *bitWriter) {
			w.bits(aacIDCPE, 3)
			w.bits(0, 4)
			w.bits(1, 1)
			w.info(aacOnlyLong, 0, 1, 0)
			w.bits(0, 2)
			c := tChan{gain: 90, maxSfb: 1, secs: [][]tSec{{{12, 1}}}}
			c.write(w, true)
		}, std},
		{"pair: second channel missing", ErrAudioSample, "truncated", func(w *bitWriter) {
			w.bits(aacIDCPE, 3)
			w.bits(0, 4)
			w.bits(1, 1)
			w.info(aacOnlyLong, 0, 0, 0)
			w.bits(0, 2)
			c := zero(0)
			c.write(w, true)
		}, std},
	}
	for _, c := range cases {
		w := &bitWriter{}
		cut = 0
		c.mk(w)
		data := w.bytes()
		if cut > 0 {
			data = data[:(cut+7)/8]
		}
		_, err := c.pr().parseFrame(data)
		wantErr(t, c.name, err, c.kind, c.text)
	}
}

func TestAACParserCodebookFailure(t *testing.T) {
	spec, scale, _ := aacCodebooks()
	defer func() { aacHuffSpectral, aacHuffScale, aacHuffErr = *spec, scale, nil }()
	aacHuffErr = errors.New("a codebook is broken")
	_, err := newAACParser(aacConfig{rateIndex: 3, channels: 1}, 1)
	wantErr(t, "broken codebook", err, ErrAudioConfig, "broken")
}

func TestAACParserLimits(t *testing.T) {
	_, err := newAACParser(aacConfig{rateIndex: 3, channels: 6}, 2)
	wantErr(t, "six channels, limit two", err, ErrOverLimit, "6 channels")
	// more elements than aacMaxElements, though the channel limit allows them
	p, err := newAACParser(aacConfig{rateIndex: 3, channels: 1}, 64)
	if err != nil {
		t.Fatal(err)
	}
	w := &bitWriter{}
	for i := 0; i < aacMaxElements+1; i++ {
		w.bits(aacIDSCE, 3)
		w.bits(0, 4)
		c := tChan{gain: 100}
		c.write(w, false)
	}
	_, err = p.parseFrame(w.finish())
	wantErr(t, "17 elements", err, ErrOverLimit, "channels")
}

func TestAACBitsEdges(t *testing.T) {
	r := newAACBits([]byte{0xa5})
	if r.read(0) != 0 || r.overrun {
		t.Fatal("read(0)")
	}
	if r.read(4) != 0xa || r.left() != 4 {
		t.Fatal("read(4)")
	}
	r.skip(2)
	if r.overrun || r.left() != 2 {
		t.Fatal("skip")
	}
	r.align()
	if r.left() != 0 || r.overrun {
		t.Fatal("align")
	}
	if r.bit() != 0 || !r.overrun {
		t.Fatal("bit past the end")
	}
	// peek reads ahead without moving, and zeros past the end
	r = newAACBits([]byte{0xa5, 0xc3})
	r.skip(3)
	if r.peek(9) != 0b001011100 || r.left() != 13 || r.peek(25) != 0b0010111000011000000000000 || r.overrun {
		t.Fatalf("peek: %b", r.peek(9))
	}
	r = newAACBits([]byte{0xff})
	if r.read(9) != 0 || !r.overrun || r.left() != 0 {
		t.Fatal("read past the end")
	}
	r = newAACBits([]byte{0xff})
	r.skip(9)
	if !r.overrun {
		t.Fatal("skip past the end")
	}
}

func TestAACTupleMatchesEncoder(t *testing.T) {
	// every symbol of every codebook decodes to values that the writer maps back to
	// the same symbol
	for cb := 1; cb <= 11; cb++ {
		dim := 2
		if cb <= 4 {
			dim = 4
		}
		for idx := range aacSpectralCodes[cb-1] {
			v := make([]int32, dim)
			aacTuple(cb, idx, v)
			w := &bitWriter{}
			w.band(cb, v)
			r := newAACBits(w.bytes())
			sym, ok := aacHuffSpectral[cb-1].decode(r)
			if !ok || sym != idx {
				t.Fatalf("codebook %d symbol %d -> %v -> %d", cb, idx, v, sym)
			}
		}
	}
}
