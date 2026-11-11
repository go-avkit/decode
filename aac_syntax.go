// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"fmt"
)

// The raw_data_block syntax of AAC-LC, ISO/IEC 14496-3 subpart 4 (4.4.2). The
// parser turns the bits of one frame into quantised spectra and the side
// information the later stages (dequantisation, M/S, intensity, PNS, TNS, IMDCT)
// need. Everything it allocates has a size fixed by the syntax, never by the
// stream: a frame names at most aacMaxElements channel elements, each a fixed
// 2 x 1024 coefficients, and a stream that names more is refused.

// Window sequences (ics_info.window_sequence).
const (
	aacOnlyLong = iota
	aacLongStart
	aacEightShort
	aacLongStop
)

// Sizes fixed by the syntax.
const (
	aacFrameLen   = 1024 // coefficients of one long window
	aacShortLen   = 128  // coefficients of one short window
	aacMaxSfb     = 51   // most scalefactor bands in any table, long window
	aacMaxGroups  = 8    // window groups of an eight short sequence
	aacMaxQuant   = 8191 // largest quantised magnitude the escape code can state
	aacTNSMaxLong = 12   // TNS filter order bound, long window, AAC-LC
	aacTNSMaxShrt = 7    // the same, short windows
)

// Band types (the codebook of a section). 0 is the zero codebook, 1 to 11 the
// spectrum codebooks, 13 perceptual noise substitution, 14 and 15 intensity.
const (
	aacBandZero   = 0
	aacBandNoise  = 13
	aacBandIntens = 14 // and 15
)

// aacICS is ics_info (Table 4.6) with what follows from the sampling rate.
type aacICS struct {
	windowSeq   uint8
	windowShape uint8
	maxSfb      uint8
	numWindows  uint8 // 1 or 8
	numGroups   uint8
	groupLen    [aacMaxGroups]uint8
	// swb holds the band offsets inside one window, numSwb+1 values.
	swb []uint16
}

func (ics *aacICS) short() bool { return ics.windowSeq == aacEightShort }

// aacTNSFilter is one filter of tns_data (Table 4.48), coefficients undecoded.
type aacTNSFilter struct {
	length   uint8
	order    uint8
	down     bool  // direction
	res      uint8 // coefficient resolution in bits, 3 or 4
	compress uint8 // coef_compress
	coef     [aacTNSMaxLong]uint8
}

// aacTNS is the TNS data of one channel: up to three filters in each of the
// windows (one in each of eight short windows).
type aacTNS struct {
	present bool
	n       [aacMaxGroups]uint8
	filt    [aacMaxGroups][3]aacTNSFilter
}

// aacPulse is pulse_data (Table 4.7) with positions made absolute.
type aacPulse struct {
	n   int
	pos [4]uint16
	amp [4]uint8
}

// aacChannel is an individual_channel_stream.
type aacChannel struct {
	ics        aacICS
	globalGain uint8
	// cb and sf are the band type and scalefactor of each band of each window
	// group. For a normal band sf is the scalefactor, 0 to 255; for noise
	// substitution the noise energy offset; for intensity the position.
	cb    [aacMaxGroups][aacMaxSfb]uint8
	sf    [aacMaxGroups][aacMaxSfb]int16
	pulse aacPulse
	tns   aacTNS
	// q holds the quantised spectrum. A long window fills q[0:1024]; eight short
	// windows are stored one after the other, window w at q[w*128:(w+1)*128].
	q [aacFrameLen]int32
}

// aacElement is a single_channel_element, lfe_channel_element or
// channel_pair_element.
type aacElement struct {
	id     uint8 // aacIDSCE, aacIDCPE or aacIDLFE
	tag    uint8
	nch    uint8
	common bool
	msMask uint8 // ms_mask_present: 0 none, 1 flags in msUsed, 2 every band
	msUsed [aacMaxGroups][aacMaxSfb]bool
	ch     [2]aacChannel
}

// aacMaxElements bounds the channel elements of a frame.
const aacMaxElements = 16

// aacParser parses frames of one stream. It keeps its element buffers between
// frames, so a frame costs no allocation once the buffers are grown. It is not
// safe for concurrent use.
type aacParser struct {
	cfg      aacConfig
	maxCh    int
	spec     *[11]*aacHuff
	scale    *aacHuff
	elems    []aacElement
	pce      *aacPCE // the last program_config_element seen in a frame
	used     int     // bytes the last frame took, up to its end element
	longSwb  []uint16
	shortSwb []uint16
}

// newAACParser returns a parser for a stream of the given configuration that
// accepts at most maxChannels channels in one frame.
func newAACParser(cfg aacConfig, maxChannels int) (*aacParser, error) {
	spec, scale, err := aacCodebooks()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAudioConfig, err)
	}
	if cfg.channels > maxChannels {
		return nil, fmt.Errorf("%w: %d channels, the limit is %d", ErrOverLimit, cfg.channels, maxChannels)
	}
	return &aacParser{
		cfg: cfg, maxCh: maxChannels, spec: spec, scale: scale,
		longSwb:  aacSwbLongTables[aacSwbLongIndex[cfg.rateIndex]],
		shortSwb: aacSwbShortTables[aacSwbShortIndex[cfg.rateIndex]],
	}, nil
}

func errTruncated() error { return fmt.Errorf("%w: truncated frame", ErrAudioSample) }

// parseFrame reads one raw_data_block. The elements it returns are valid until
// the next call.
func (p *aacParser) parseFrame(data []byte) ([]aacElement, error) {
	r := newAACBits(data)
	p.elems = p.elems[:0]
	p.pce = nil
	channels := 0
	for {
		id := int(r.read(3))
		switch id {
		case aacIDSCE, aacIDLFE, aacIDCPE:
			n := 1
			if id == aacIDCPE {
				n = 2
			}
			if channels += n; channels > p.maxCh || len(p.elems) >= aacMaxElements {
				return nil, fmt.Errorf("%w: more than %d channels in a frame", ErrOverLimit, p.maxCh)
			}
			p.elems = append(p.elems, aacElement{id: uint8(id), nch: uint8(n)})
			el := &p.elems[len(p.elems)-1]
			el.tag = uint8(r.read(4))
			var err error
			if id == aacIDCPE {
				err = p.pair(r, el)
			} else {
				err = p.channel(r, &el.ch[0], false)
			}
			if err != nil {
				return nil, err
			}
		case aacIDCCE:
			return nil, fmt.Errorf("%w: coupling channel element", ErrAudioUnsupported)
		case aacIDDSE:
			r.skip(4) // element_instance_tag
			align := r.bit() != 0
			cnt := int(r.read(8))
			if cnt == 255 {
				cnt += int(r.read(8))
			}
			if align {
				r.align()
			}
			r.skip(8 * cnt)
		case aacIDPCE:
			pce, err := parsePCE(r)
			if err != nil {
				return nil, errTruncated()
			}
			p.pce = pce
		case aacIDFIL:
			cnt := int(r.read(4))
			if cnt == 15 {
				cnt += int(r.read(8)) - 1
			}
			if cnt > 0 {
				if t := r.read(4); t == 13 || t == 14 { // EXT_SBR_DATA, EXT_SBR_DATA_CRC
					return nil, fmt.Errorf("%w: HE-AAC (SBR data in a fill element)", ErrAudioUnsupported)
				}
				r.skip(8*cnt - 4)
			}
		default: // aacIDEND
			r.align()
			p.used = r.pos / 8
			return p.elems, nil
		}
		if r.overrun {
			return nil, errTruncated()
		}
	}
}

// pair reads a channel_pair_element after its tag (Table 4.4).
func (p *aacParser) pair(r *aacBits, el *aacElement) error {
	el.common = r.bit() != 0
	if el.common {
		if err := p.info(r, &el.ch[0].ics); err != nil {
			return err
		}
		el.ch[1].ics = el.ch[0].ics
		el.msMask = uint8(r.read(2))
		switch el.msMask {
		case 1:
			ics := &el.ch[0].ics
			for g := 0; g < int(ics.numGroups); g++ {
				for sfb := 0; sfb < int(ics.maxSfb); sfb++ {
					el.msUsed[g][sfb] = r.bit() != 0
				}
			}
		case 3:
			return fmt.Errorf("%w: reserved ms_mask_present", ErrAudioSample)
		}
	}
	if err := p.channel(r, &el.ch[0], el.common); err != nil {
		return err
	}
	return p.channel(r, &el.ch[1], el.common)
}

// info reads ics_info (Table 4.6).
func (p *aacParser) info(r *aacBits, ics *aacICS) error {
	r.skip(1) // ics_reserved_bit
	ics.windowSeq = uint8(r.read(2))
	ics.windowShape = uint8(r.read(1))
	ics.groupLen = [aacMaxGroups]uint8{}
	if ics.short() {
		ics.maxSfb = uint8(r.read(4))
		ics.numWindows, ics.numGroups = 8, 1
		ics.groupLen[0] = 1
		grouping := r.read(7)
		for i := 6; i >= 0; i-- {
			if grouping>>uint(i)&1 != 0 {
				ics.groupLen[ics.numGroups-1]++
			} else {
				ics.numGroups++
				ics.groupLen[ics.numGroups-1] = 1
			}
		}
		ics.swb = p.shortSwb
	} else {
		ics.maxSfb = uint8(r.read(6))
		ics.numWindows, ics.numGroups = 1, 1
		ics.groupLen[0] = 1
		if r.bit() != 0 { // predictor_data_present
			return fmt.Errorf("%w: prediction data (AAC Main or LTP) in an AAC-LC stream", ErrAudioUnsupported)
		}
		ics.swb = p.longSwb
	}
	if r.overrun {
		return errTruncated()
	}
	if int(ics.maxSfb) > len(ics.swb)-1 {
		return fmt.Errorf("%w: max_sfb %d, the table has %d bands", ErrAudioSample, ics.maxSfb, len(ics.swb)-1)
	}
	return nil
}

// channel reads an individual_channel_stream (Table 4.44). With common set, the
// ics_info was read for the pair and ch.ics already holds it.
func (p *aacParser) channel(r *aacBits, ch *aacChannel, common bool) error {
	ch.globalGain = uint8(r.read(8))
	if !common {
		if err := p.info(r, &ch.ics); err != nil {
			return err
		}
	}
	if err := p.sections(r, ch); err != nil {
		return err
	}
	if err := p.scalefactors(r, ch); err != nil {
		return err
	}
	if r.bit() != 0 {
		if err := p.pulseData(r, ch); err != nil {
			return err
		}
	}
	if r.bit() != 0 {
		if err := p.tnsData(r, ch); err != nil {
			return err
		}
	}
	if r.bit() != 0 {
		return fmt.Errorf("%w: gain control (AAC SSR)", ErrAudioUnsupported)
	}
	if r.overrun {
		return errTruncated()
	}
	if err := p.spectral(r, ch); err != nil {
		return err
	}
	ch.applyPulses()
	return nil
}

// sections reads section_data (Table 4.46).
func (p *aacParser) sections(r *aacBits, ch *aacChannel) error {
	ics := &ch.ics
	lenBits, esc := 5, 31
	if ics.short() {
		lenBits, esc = 3, 7
	}
	for g := 0; g < int(ics.numGroups); g++ {
		for k := 0; k < int(ics.maxSfb); {
			cb := uint8(r.read(4))
			if cb == 12 {
				return fmt.Errorf("%w: reserved codebook 12", ErrAudioSample)
			}
			n := 0
			for {
				inc := int(r.read(lenBits))
				n += inc
				if r.overrun {
					return errTruncated()
				}
				if inc != esc {
					break
				}
			}
			if k+n > int(ics.maxSfb) {
				return fmt.Errorf("%w: a section of %d bands ends past max_sfb %d", ErrAudioSample, k+n, ics.maxSfb)
			}
			for i := k; i < k+n; i++ {
				ch.cb[g][i] = cb
			}
			k += n
		}
	}
	return nil
}

// scalefactors reads scale_factor_data (Table 4.47).
func (p *aacParser) scalefactors(r *aacBits, ch *aacChannel) error {
	ics := &ch.ics
	normal, noise, intens := int(ch.globalGain), int(ch.globalGain)-90, 0
	firstNoise := true
	delta := func() (int, bool) {
		s, ok := p.scale.decode(r)
		return s - 60, ok
	}
	for g := 0; g < int(ics.numGroups); g++ {
		for sfb := 0; sfb < int(ics.maxSfb); sfb++ {
			switch cb := ch.cb[g][sfb]; {
			case cb == aacBandZero:
			case cb >= aacBandIntens:
				d, ok := delta()
				if !ok {
					return errTruncated()
				}
				intens += d
				ch.sf[g][sfb] = int16(intens)
			case cb == aacBandNoise:
				if firstNoise {
					noise += int(r.read(9)) - 256
					firstNoise = false
				} else {
					d, ok := delta()
					if !ok {
						return errTruncated()
					}
					noise += d
				}
				ch.sf[g][sfb] = int16(noise)
			default:
				d, ok := delta()
				if !ok {
					return errTruncated()
				}
				if normal += d; normal < 0 || normal > 255 {
					return fmt.Errorf("%w: scalefactor %d out of range", ErrAudioSample, normal)
				}
				ch.sf[g][sfb] = int16(normal)
			}
		}
	}
	if r.overrun {
		return errTruncated()
	}
	return nil
}

// pulseData reads pulse_data (Table 4.7).
func (p *aacParser) pulseData(r *aacBits, ch *aacChannel) error {
	ics := &ch.ics
	if ics.short() {
		return fmt.Errorf("%w: pulse data in an eight short sequence", ErrAudioSample)
	}
	n := int(r.read(2)) + 1
	start := int(r.read(6))
	if start >= len(ics.swb)-1 {
		return fmt.Errorf("%w: pulse_start_sfb %d", ErrAudioSample, start)
	}
	pos := int(ics.swb[start])
	ch.pulse.n = n
	for i := 0; i < n; i++ {
		pos += int(r.read(5))
		ch.pulse.amp[i] = uint8(r.read(4))
		if pos >= aacFrameLen {
			return fmt.Errorf("%w: pulse position %d", ErrAudioSample, pos)
		}
		ch.pulse.pos[i] = uint16(pos)
	}
	if r.overrun {
		return errTruncated()
	}
	return nil
}

// tnsData reads tns_data (Table 4.48).
func (p *aacParser) tnsData(r *aacBits, ch *aacChannel) error {
	ics := &ch.ics
	nBits, lenBits, ordBits, maxOrd := 2, 6, 5, aacTNSMaxLong
	if ics.short() {
		nBits, lenBits, ordBits, maxOrd = 1, 4, 3, aacTNSMaxShrt
	}
	ch.tns.present = true
	for w := 0; w < int(ics.numWindows); w++ {
		n := int(r.read(nBits))
		ch.tns.n[w] = uint8(n)
		if n == 0 {
			continue
		}
		res := uint8(r.read(1)) + 3
		for f := 0; f < n; f++ {
			t := &ch.tns.filt[w][f]
			t.length = uint8(r.read(lenBits))
			t.order = uint8(r.read(ordBits))
			t.res = res
			if int(t.order) > maxOrd {
				return fmt.Errorf("%w: TNS filter of order %d, the limit is %d", ErrAudioSample, t.order, maxOrd)
			}
			if t.order == 0 {
				continue
			}
			t.down = r.bit() != 0
			t.compress = uint8(r.read(1))
			for i := 0; i < int(t.order); i++ {
				t.coef[i] = uint8(r.read(int(res - t.compress)))
			}
		}
	}
	if r.overrun {
		return errTruncated()
	}
	return nil
}

// spectral reads spectral_data (Table 4.50) into ch.q.
func (p *aacParser) spectral(r *aacBits, ch *aacChannel) error {
	ics := &ch.ics
	w0 := 0
	for g := 0; g < int(ics.numGroups); g++ {
		for sfb := 0; sfb < int(ics.maxSfb); sfb++ {
			cb := int(ch.cb[g][sfb])
			if cb == aacBandZero || cb >= aacBandNoise {
				continue
			}
			lo, hi := int(ics.swb[sfb]), int(ics.swb[sfb+1])
			for k := 0; k < int(ics.groupLen[g]); k++ {
				base := (w0 + k) * aacShortLen
				if err := p.band(r, cb, ch.q[base+lo:base+hi]); err != nil {
					return err
				}
			}
		}
		w0 += int(ics.groupLen[g])
	}
	return nil
}

// aacTuple expands the index of a codeword of spectrum codebook cb (1 to 11) into
// the values it stands for: four for codebooks 1 to 4, two for 5 to 11. Codebooks
// 1, 2, 5 and 6 are signed (values from -m to m); the others give magnitudes and
// the signs follow in the stream. The first value is the most significant digit of
// the index.
func aacTuple(cb, idx int, out []int32) {
	t := &aacTuples[cb-1][idx]
	for i := range out {
		out[i] = int32(t[i])
	}
}

// aacTuples is aacTupleOf for every symbol of every spectrum codebook, built once.
var aacTuples = func() (t [11][289][4]int8) {
	for cb := 1; cb <= 11; cb++ {
		for idx := range aacSpectralCodes[cb-1] {
			var v [4]int32
			aacTupleOf(cb, idx, v[:])
			for i, x := range v {
				t[cb-1][idx][i] = int8(x)
			}
		}
	}
	return t
}()

func aacTupleOf(cb, idx int, out []int32) {
	var base, off, n int
	switch cb {
	case 1, 2:
		base, off, n = 3, 1, 4
	case 3, 4:
		base, off, n = 3, 0, 4
	case 5, 6:
		base, off, n = 9, 4, 2
	case 7, 8:
		base, off, n = 8, 0, 2
	case 9, 10:
		base, off, n = 13, 0, 2
	default: // 11
		base, off, n = 17, 0, 2
	}
	for i := n - 1; i >= 0; i-- {
		out[i] = int32(idx%base - off)
		idx /= base
	}
}

// band reads the codewords of one band of one window into dst: len(dst) values,
// a multiple of four.
func (p *aacParser) band(r *aacBits, cb int, dst []int32) error {
	h := p.spec[cb-1]
	dim := 2
	if cb <= 4 {
		dim = 4
	}
	unsigned := cb >= 3 && cb != 5 && cb != 6
	for i := 0; i < len(dst); i += dim {
		sym, ok := h.decode(r)
		if !ok {
			return errTruncated()
		}
		v := dst[i : i+dim]
		aacTuple(cb, sym, v)
		if unsigned {
			for j := range v {
				if v[j] != 0 && r.bit() != 0 {
					v[j] = -v[j]
				}
			}
		}
		if cb == 11 {
			for j := range v {
				a := v[j]
				if a < 0 {
					a = -a
				}
				if a != 16 {
					continue
				}
				m, err := escape(r)
				if err != nil {
					return err
				}
				if v[j] < 0 {
					m = -m
				}
				v[j] = m
			}
		}
	}
	return nil
}

// escape reads an escape sequence: a run of ones and a zero, then that many plus
// four bits, which add to 2 to the power of the same number.
func escape(r *aacBits) (int32, error) {
	n := 4
	for r.bit() != 0 {
		if n++; n > 12 {
			return 0, fmt.Errorf("%w: escape sequence longer than 8191 allows", ErrAudioSample)
		}
	}
	return int32(1)<<uint(n) + int32(r.read(n)), nil
}

// applyPulses adds the pulses to the quantised spectrum: a pulse adds its
// amplitude to a positive value and subtracts it from any other, so it moves the
// value away from zero.
func (ch *aacChannel) applyPulses() {
	for i := 0; i < ch.pulse.n; i++ {
		q := &ch.q[ch.pulse.pos[i]]
		if *q > 0 {
			*q += int32(ch.pulse.amp[i])
		} else {
			*q -= int32(ch.pulse.amp[i])
		}
	}
}
