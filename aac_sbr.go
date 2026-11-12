// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"fmt"
)

// SBR bitstream parser (HE-AAC v1): the sbr_extension_data carried by a fill
// element, its header, grid, delta-coding flags, inverse filtering modes,
// envelope and noise floor scalefactors, added harmonics. The order of the fields
// and the state kept from one frame to the next follow ISO/IEC 14496-3 subpart 4
// (SBR payload, tables not verified against the standard) as ffmpeg n9.0.2
// libavcodec/aacsbr_template.c:188-247, 638-1180 reads it; read, not copied.
//
// A parse error leaves SBR off until the next header (turnoff): the caller keeps
// decoding the core signal, as ffmpeg does. State is per decoder, never shared.

// Bounds of what the stream can size.
const (
	sbrTimeSlots = 16 // time slots per frame (1024-sample frames)
	sbrMaxEnv    = 5
	sbrMaxBands  = 48
	sbrMaxNoise  = 5
)

// Frame classes of bs_frame_class.
const (
	sbrFixFix = iota
	sbrFixVar
	sbrVarFix
	sbrVarVar
)

// SBR header extension ids of sbr_extension: PS is the only one with a meaning.
const sbrExtPS = 2

var errSBR = errors.New("invalid SBR data")

func sbrErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errSBR, fmt.Sprintf(format, a...))
}

// sbrChannel is the parsed SBR data of one channel, with the values the next
// frame's parse reads back (the [0] entries are the last of the previous frame).
type sbrChannel struct {
	frameClass uint8
	numEnv     int
	numNoise   int
	ampRes     uint8
	freqRes    [sbrMaxEnv + 1]uint8
	tEnv       [sbrMaxEnv + 1]uint8
	tQ         [3]uint8
	eA         [2]int8 // l_APrev, l_A
	dfEnv      [sbrMaxEnv]uint8
	dfNoise    [2]uint8
	invf       [2][sbrMaxNoise]uint8 // [0] this frame, [1] the previous
	envQ       [sbrMaxEnv + 1][sbrMaxBands]int16
	noiseQ     [3][sbrMaxNoise]int16
	harmonic   bool
	addHarm    [sbrMaxBands]uint8
}

// sbrState is the SBR state of one channel element: header, tables and the
// parsed data of its one or two channels.
type sbrState struct {
	t       sbrTables
	spec    sbrSpectrum
	haveSpe bool // spec holds a header that built the tables
	started bool // a valid header has been read: SBR data is parsed and used

	ampResHeader uint8
	limBands     uint8
	limGains     uint8
	interpolFreq uint8
	smoothing    uint8
	coupling     bool
	psSeen       bool // a PS extension was found in the payload (not decoded)
	extBits      int  // bits of bs_extension_data in the last frame, 0 for none
	ch           [2]sbrChannel
}

// newSBRState returns the state of a stream whose core runs at coreRate: SBR
// doubles the rate.
func newSBRState(coreRate int) *sbrState {
	s := &sbrState{}
	s.t.rate = 2 * coreRate
	s.turnoff()
	return s
}

// turnoff puts SBR in pure upsampling mode until the next header.
func (s *sbrState) turnoff() {
	s.started = false
	s.haveSpe = false
	s.t.kx[1] = 32
	s.t.m[1] = 0
	s.ch[0].eA[1] = -1
	s.ch[1].eA[1] = -1
}

// The ten Huffman codes of the envelope and noise scalefactors, built at start-up
// from the normative lengths: the code of the i-th entry is the canonical one,
// entries being listed by increasing length. They are read-only afterwards. The
// lengths are constants of the program, checked complete by a test, so a table
// that does not build is a bug and stops the program at start-up.
var sbrHuff = mustSBRHuff(aacSBRHuffLens)

func mustSBRHuff(lens [10][]uint8) [10]*aacHuff {
	hs, err := buildSBRHuff(lens)
	if err != nil {
		panic(err)
	}
	return hs
}

// sbrHuffOffset is the value of symbol 0 of each code: a symbol s is the delta
// s+offset.
var sbrHuffOffset = [10]int{-60, -60, -24, -24, -31, -31, -12, -12, -31, -12}

// Indexes of the codes.
const (
	sbrTEnv15 = iota
	sbrFEnv15
	sbrTEnvBal15
	sbrFEnvBal15
	sbrTEnv30
	sbrFEnv30
	sbrTEnvBal30
	sbrFEnvBal30
	sbrTNoise30
	sbrTNoiseBal30
)

// buildSBRHuff builds the codes for the given lengths.
func buildSBRHuff(lens [10][]uint8) (hs [10]*aacHuff, err error) {
	for i, l := range lens {
		codes := make([]uint32, len(l))
		var code uint32
		for j := range l {
			if j > 0 {
				if l[j] < l[j-1] {
					return hs, fmt.Errorf("SBR huffman %d: lengths decrease at %d", i, j)
				}
				code = (code + 1) << (l[j] - l[j-1])
			}
			codes[j] = code
		}
		if hs[i], err = buildAACHuff(codes, l); err != nil {
			return hs, fmt.Errorf("SBR huffman %d: %w", i, err)
		}
	}
	return hs, nil
}

// huff reads one delta with code number c.
func sbrReadHuff(r *aacBits, c int) (int, error) {
	i, ok := sbrHuff[c].decode(r)
	if !ok {
		return 0, errTruncated()
	}
	return int(aacSBRHuffSyms[c][i]) + sbrHuffOffset[c], nil
}

func readBits1(r *aacBits, v []uint8) {
	for i := range v {
		v[i] = uint8(r.bit())
	}
}

// sbrEntry is the SBR state of the channel element with this type and instance
// tag.
type sbrEntry struct {
	id, tag uint8
	s       *sbrState
}

// enableSBR makes the parser read the SBR data of fill elements (an HE-AAC stream)
// instead of refusing it.
func (p *aacParser) enableSBR() { p.sbrOn = true }

// sbrFill reads the SBR data of a fill element that follows element idx of the
// frame (-1 for none), nbits bits long from r's position, and leaves r at its end.
// A fault is kept in p.sbrErr and does not fail the frame: the core signal decodes
// without SBR, as in ffmpeg.
func (p *aacParser) sbrFill(r *aacBits, crc bool, nbits, idx int) {
	skip := func(err error) {
		p.sbrErr = err
		r.skip(nbits)
	}
	if idx < 0 {
		skip(sbrErr("SBR data before the first channel element"))
		return
	}
	el := &p.elems[idx]
	if el.id != aacIDSCE && el.id != aacIDCPE {
		skip(sbrErr("SBR data after element type %d", el.id))
		return
	}
	var s *sbrState
	for _, e := range p.sbrStates {
		if e.id == el.id && e.tag == el.tag {
			s = e.s
		}
	}
	if s == nil {
		if len(p.sbrStates) >= aacMaxElements {
			skip(sbrErr("SBR for more than %d channel elements", aacMaxElements))
			return
		}
		s = newSBRState(p.cfg.rate)
		p.sbrStates = append(p.sbrStates, sbrEntry{el.id, el.tag, s})
	}
	if err := s.parse(r, crc, nbits, int(el.id)); err != nil {
		p.sbrErr = err
		return
	}
	if s.started {
		el.sbr = s
	}
}

// parse reads the sbr_extension_data of a fill element: nbits bits that start at
// r's position (the fill element's byte count less its 4-bit extension type).
// elem is the element the data belongs to, the one before the fill element:
// aacIDSCE or aacIDCPE. It leaves r at the end of the payload. On an error SBR is
// turned off.
func (s *sbrState) parse(r *aacBits, crc bool, nbits, elem int) error {
	start := r.bitPos()
	err := s.parsePayload(r, crc, elem)
	used := r.bitPos() - start
	if err == nil && (r.overrun || used > nbits) {
		err = sbrErr("payload of %d bits read %d", nbits, used)
	}
	if err != nil {
		s.turnoff()
	}
	if used < nbits && !r.overrun {
		r.skip(nbits - used)
	}
	return err
}

func (s *sbrState) parsePayload(r *aacBits, crc bool, elem int) error {
	reset := false
	s.extBits, s.psSeen = 0, false
	if crc {
		r.skip(10) // bs_sbr_crc_bits: not checked
	}
	s.t.kx[0], s.t.m[0] = s.t.kx[1], s.t.m[1]
	if r.bit() != 0 { // bs_header_flag
		reset = s.header(r)
	}
	if reset {
		if err := s.reset(); err != nil {
			s.turnoff()
			return nil // pure upsampling, as ffmpeg: the frame is not an error
		}
	}
	if !s.started {
		return nil
	}
	switch elem {
	case aacIDSCE:
		if err := s.single(r); err != nil {
			return err
		}
	case aacIDCPE:
		if err := s.pair(r); err != nil {
			return err
		}
	default:
		return sbrErr("SBR on element type %d", elem)
	}
	if r.bit() != 0 { // bs_extended_data
		left := int(r.read(4)) // bs_extension_size
		if left == 15 {
			left += int(r.read(8))
		}
		left <<= 3
		s.extBits = left
		// the size is in whole bytes: a size of 0 has nothing to read, any other has
		// room for the 2-bit extension id
		if left > 0 {
			if r.read(2) == sbrExtPS {
				s.psSeen = true
			}
			// Parametric Stereo is not decoded and no other extension is defined:
			// the rest of the extension is skipped.
			r.skip(left - 2)
		}
	}
	return nil
}

// header reads sbr_header and reports whether the tables must be rebuilt.
func (s *sbrState) header(r *aacBits) (reset bool) {
	s.started = true
	s.ampResHeader = uint8(r.bit())
	sp := sbrSpectrum{startFreq: uint8(r.read(4)), stopFreq: uint8(r.read(4)), xoverBand: uint8(r.read(3))}
	r.skip(2) // bs_reserved
	extra1, extra2 := r.bit(), r.bit()
	if extra1 != 0 {
		sp.freqScale = uint8(r.read(2))
		sp.alterScale = uint8(r.bit())
		sp.noiseBands = uint8(r.read(2))
	} else {
		sp.freqScale, sp.alterScale, sp.noiseBands = 2, 1, 2
	}
	reset = !s.haveSpe || sp != s.spec
	s.spec = sp
	oldLim := s.limBands
	if extra2 != 0 {
		s.limBands = uint8(r.read(2))
		s.limGains = uint8(r.read(2))
		s.interpolFreq = uint8(r.bit())
		s.smoothing = uint8(r.bit())
	} else {
		s.limBands, s.limGains, s.interpolFreq, s.smoothing = 2, 2, 1, 1
	}
	if s.limBands != oldLim && !reset {
		s.t.makeLim(int(s.limBands))
	}
	return reset
}

// reset rebuilds the frequency tables from the header.
func (s *sbrState) reset() error {
	if err := s.t.makeMaster(s.spec); err != nil {
		return err
	}
	if err := s.t.makeDerived(s.spec, int(s.limBands)); err != nil {
		return err
	}
	s.haveSpe = true
	return nil
}

var sbrCeilLog2 = [...]uint{0, 1, 2, 2, 3, 3}

// grid reads sbr_grid for one channel.
func (s *sbrState) grid(r *aacBits, c *sbrChannel) error {
	oldEnv := c.numEnv
	c.freqRes[0] = c.freqRes[oldEnv]
	c.ampRes = s.ampResHeader
	trail := sbrTimeSlots
	pointer := 0
	class := int(r.read(2))
	switch class {
	case sbrFixFix:
		n := 1 << r.read(2)
		if n > 4 {
			return sbrErr("%d envelopes in a FIXFIX frame", n)
		}
		c.numEnv = n
		if n == 1 {
			c.ampRes = 0
		}
		c.tEnv[0], c.tEnv[n] = 0, uint8(trail)
		step := (trail + n>>1) / n
		for i := 0; i < n-1; i++ {
			c.tEnv[i+1] = c.tEnv[i] + uint8(step)
		}
		c.freqRes[1] = uint8(r.bit())
		for i := 1; i < n; i++ {
			c.freqRes[i+1] = c.freqRes[1]
		}
	case sbrFixVar:
		trail += int(r.read(2))
		rel := int(r.read(2))
		c.numEnv = rel + 1
		c.tEnv[0], c.tEnv[c.numEnv] = 0, uint8(trail)
		for i := 0; i < rel; i++ {
			c.tEnv[c.numEnv-1-i] = c.tEnv[c.numEnv-i] - uint8(2*r.read(2)+2)
		}
		pointer = int(r.read(int(sbrCeilLog2[c.numEnv])))
		for i := 0; i < c.numEnv; i++ {
			c.freqRes[c.numEnv-i] = uint8(r.bit())
		}
	case sbrVarFix:
		c.tEnv[0] = uint8(r.read(2))
		rel := int(r.read(2))
		c.numEnv = rel + 1
		c.tEnv[c.numEnv] = uint8(trail)
		for i := 0; i < rel; i++ {
			c.tEnv[i+1] = c.tEnv[i] + uint8(2*r.read(2)+2)
		}
		pointer = int(r.read(int(sbrCeilLog2[c.numEnv])))
		readBits1(r, c.freqRes[1:c.numEnv+1])
	default: // sbrVarVar
		c.tEnv[0] = uint8(r.read(2))
		trail += int(r.read(2))
		lead := int(r.read(2))
		rel := int(r.read(2))
		n := lead + rel + 1
		if n > sbrMaxEnv {
			return sbrErr("%d envelopes in a VARVAR frame", n)
		}
		c.numEnv = n
		c.tEnv[n] = uint8(trail)
		for i := 0; i < lead; i++ {
			c.tEnv[i+1] = c.tEnv[i] + uint8(2*r.read(2)+2)
		}
		for i := 0; i < rel; i++ {
			c.tEnv[n-1-i] = c.tEnv[n-i] - uint8(2*r.read(2)+2)
		}
		pointer = int(r.read(int(sbrCeilLog2[n])))
		readBits1(r, c.freqRes[1:n+1])
	}
	c.frameClass = uint8(class)
	if pointer > c.numEnv+1 {
		return sbrErr("bs_pointer %d with %d envelopes", pointer, c.numEnv)
	}
	if r.overrun {
		return errTruncated()
	}
	// the borders are unsigned bytes: a border that went below zero wrapped to a
	// large value, and the last border is at most 19, so the strict increase
	// tested here refuses it
	for i := 1; i <= c.numEnv; i++ {
		if c.tEnv[i-1] >= c.tEnv[i] {
			return sbrErr("time borders not strictly increasing")
		}
	}
	c.numNoise = 1
	if c.numEnv > 1 {
		c.numNoise = 2
	}
	c.tQ[0], c.tQ[c.numNoise] = c.tEnv[0], c.tEnv[c.numEnv]
	if c.numNoise > 1 {
		var idx int
		switch {
		case class == sbrFixFix:
			idx = c.numEnv >> 1
		case class&1 != 0: // FIXVAR, VARVAR
			idx = c.numEnv - max(pointer-1, 1)
		case pointer == 0:
			idx = 1
		case pointer == 1:
			idx = c.numEnv - 1
		default:
			idx = pointer - 1
		}
		c.tQ[1] = c.tEnv[idx]
	}
	if c.eA[1] != int8(oldEnv) {
		c.eA[0] = -1
	} else {
		c.eA[0] = 0
	}
	c.eA[1] = -1
	if class&1 != 0 && pointer != 0 {
		c.eA[1] = int8(c.numEnv + 1 - pointer)
	} else if class == sbrVarFix && pointer > 1 {
		c.eA[1] = int8(pointer - 1)
	}
	return nil
}

// copyGrid gives dst, the second channel of a coupled pair, the grid of src.
func (dst *sbrChannel) copyGrid(src *sbrChannel) {
	dst.freqRes[0] = dst.freqRes[dst.numEnv]
	if dst.eA[1] != int8(dst.numEnv) {
		dst.eA[0] = -1
	} else {
		dst.eA[0] = 0
	}
	copy(dst.freqRes[1:], src.freqRes[1:])
	dst.tEnv = src.tEnv
	dst.tQ = src.tQ
	dst.numEnv = src.numEnv
	dst.ampRes = src.ampRes
	dst.numNoise = src.numNoise
	dst.frameClass = src.frameClass
	dst.eA[1] = src.eA[1]
}

func (s *sbrState) dtdf(r *aacBits, c *sbrChannel) {
	readBits1(r, c.dfEnv[:c.numEnv])
	readBits1(r, c.dfNoise[:c.numNoise])
}

func (s *sbrState) invf(r *aacBits, c *sbrChannel) {
	c.invf[1] = c.invf[0]
	for i := 0; i < s.t.nQ; i++ {
		c.invf[0][i] = uint8(r.read(2))
	}
}

// envelope reads sbr_envelope of channel ch (1 is the balance channel of a coupled
// pair).
func (s *sbrState) envelope(r *aacBits, c *sbrChannel, ch int) error {
	delta := 1
	if ch == 1 && s.coupling {
		delta = 2
	}
	odd := s.t.n[1] & 1
	var bits, tc, fc int
	switch {
	case s.coupling && ch == 1 && c.ampRes != 0:
		bits, tc, fc = 5, sbrTEnvBal30, sbrFEnvBal30
	case s.coupling && ch == 1:
		bits, tc, fc = 6, sbrTEnvBal15, sbrFEnvBal15
	case c.ampRes != 0:
		bits, tc, fc = 6, sbrTEnv30, sbrFEnv30
	default:
		bits, tc, fc = 7, sbrTEnv15, sbrFEnv15
	}
	for i := 0; i < c.numEnv; i++ {
		res := c.freqRes[i+1]
		n := s.t.n[res]
		if c.dfEnv[i] != 0 {
			for j := 0; j < n; j++ {
				k := j
				switch {
				case res == c.freqRes[i]:
				case res != 0: // low to high resolution: the band of the low table that holds j
					k = (j + odd) >> 1
				case j > 0: // high to low: the band of the high table that starts band j
					k = 2*j - odd
				default:
					k = 0
				}
				d, err := sbrReadHuff(r, tc)
				if err != nil {
					return err
				}
				v := int(c.envQ[i][k]) + delta*d
				if v < 0 || v > 127 {
					return sbrErr("envelope scalefactor %d", v)
				}
				c.envQ[i+1][j] = int16(v)
			}
			continue
		}
		c.envQ[i+1][0] = int16(delta * int(r.read(bits)))
		for j := 1; j < n; j++ {
			d, err := sbrReadHuff(r, fc)
			if err != nil {
				return err
			}
			v := int(c.envQ[i+1][j-1]) + delta*d
			if v < 0 || v > 127 {
				return sbrErr("envelope scalefactor %d", v)
			}
			c.envQ[i+1][j] = int16(v)
		}
	}
	c.envQ[0] = c.envQ[c.numEnv]
	return nil
}

// noise reads sbr_noise of channel ch.
func (s *sbrState) noise(r *aacBits, c *sbrChannel, ch int) error {
	delta := 1
	tc, fc := sbrTNoise30, sbrFEnv30
	if s.coupling && ch == 1 {
		delta = 2
		tc, fc = sbrTNoiseBal30, sbrFEnvBal30
	}
	for i := 0; i < c.numNoise; i++ {
		if c.dfNoise[i] != 0 {
			for j := 0; j < s.t.nQ; j++ {
				d, err := sbrReadHuff(r, tc)
				if err != nil {
					return err
				}
				v := int(c.noiseQ[i][j]) + delta*d
				if v < 0 || v > 30 {
					return sbrErr("noise scalefactor %d", v)
				}
				c.noiseQ[i+1][j] = int16(v)
			}
			continue
		}
		c.noiseQ[i+1][0] = int16(delta * int(r.read(5)))
		for j := 1; j < s.t.nQ; j++ {
			d, err := sbrReadHuff(r, fc)
			if err != nil {
				return err
			}
			v := int(c.noiseQ[i+1][j-1]) + delta*d
			if v < 0 || v > 30 {
				return sbrErr("noise scalefactor %d", v)
			}
			c.noiseQ[i+1][j] = int16(v)
		}
	}
	c.noiseQ[0] = c.noiseQ[c.numNoise]
	return nil
}

func (s *sbrState) harmonics(r *aacBits, c *sbrChannel) {
	c.harmonic = r.bit() != 0
	if c.harmonic {
		readBits1(r, c.addHarm[:s.t.n[1]])
	}
}

// single reads sbr_single_channel_element.
func (s *sbrState) single(r *aacBits) error {
	if r.bit() != 0 { // bs_data_extra
		r.skip(4)
	}
	c := &s.ch[0]
	if err := s.grid(r, c); err != nil {
		return err
	}
	s.dtdf(r, c)
	s.invf(r, c)
	if err := s.envelope(r, c, 0); err != nil {
		return err
	}
	if err := s.noise(r, c, 0); err != nil {
		return err
	}
	s.harmonics(r, c)
	return nil
}

// pair reads sbr_channel_pair_element.
func (s *sbrState) pair(r *aacBits) error {
	if r.bit() != 0 { // bs_data_extra
		r.skip(8)
	}
	c0, c1 := &s.ch[0], &s.ch[1]
	s.coupling = r.bit() != 0
	if s.coupling {
		if err := s.grid(r, c0); err != nil {
			return err
		}
		c1.copyGrid(c0)
		s.dtdf(r, c0)
		s.dtdf(r, c1)
		s.invf(r, c0)
		c1.invf[1] = c1.invf[0]
		c1.invf[0] = c0.invf[0]
		for _, f := range []func() error{
			func() error { return s.envelope(r, c0, 0) },
			func() error { return s.noise(r, c0, 0) },
			func() error { return s.envelope(r, c1, 1) },
			func() error { return s.noise(r, c1, 1) },
		} {
			if err := f(); err != nil {
				return err
			}
		}
	} else {
		if err := s.grid(r, c0); err != nil {
			return err
		}
		if err := s.grid(r, c1); err != nil {
			return err
		}
		s.dtdf(r, c0)
		s.dtdf(r, c1)
		s.invf(r, c0)
		s.invf(r, c1)
		for _, f := range []func() error{
			func() error { return s.envelope(r, c0, 0) },
			func() error { return s.envelope(r, c1, 1) },
			func() error { return s.noise(r, c0, 0) },
			func() error { return s.noise(r, c1, 1) },
		} {
			if err := f(); err != nil {
				return err
			}
		}
	}
	s.harmonics(r, c0)
	s.harmonics(r, c1)
	return nil
}
