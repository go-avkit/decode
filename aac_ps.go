// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"fmt"
)

// Parametric Stereo bitstream parser (HE-AAC v2): the ps_data carried in the SBR
// extension of a mono channel element. Its fields (header with the IID and ICC
// resolutions, frame class and envelope borders, the delta-coded IID and ICC, and the
// extension that holds IPD and OPD) are read in the order ISO/IEC 14496-3 subpart 8
// gives (clause and table numbers not verified), as FFmpeg n9.0.2
// libavcodec/aacps_common.c:28-306 reads them: read to understand what is parsed and
// what is kept from one frame to the next, not copied.
//
// Only the parameters are decoded here; no signal is processed (docs/aac-ps-design.md,
// PS1). A fault turns PS off until the next header and zeroes the parameters: the
// frame still decodes, as an SBR frame whose PS data is bad does. The data is read
// from a copy of exactly its own bits, so a field that reads past its end is an error
// and never takes bits of the next element.

// Bounds of what the stream can size.
const (
	psMaxEnv   = 5  // four envelopes, and the one a last border before the end adds
	psMaxBands = 34 // parameter bands at the finest resolution
	psSlots    = 32 // QMF time slots of a frame
)

var errPS = errors.New("invalid PS data")

func psErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errPS, fmt.Sprintf(format, a...))
}

// psBandsOf is the number of IID or ICC parameter bands of modes 0 to 5 and psIPDBandsOf
// the number of IPD and OPD bands that go with an IID mode: modes 3 to 5 have the
// bands of modes 0 to 2 and a finer quantiser.
var (
	psBandsOf    = [6]int{10, 20, 34, 10, 20, 34}
	psIPDBandsOf = [6]int{5, 11, 17, 5, 11, 17}
)

// psEnvOf is the number of envelopes of bs_num_env, by frame class: a fixed class
// can have none (the previous parameters hold), a variable one has at least one.
var psEnvOf = [2][4]int{{0, 1, 2, 4}, {1, 2, 3, 4}}

// Indexes of the Huffman codes: the IID codes come in fine and coarse resolution, each
// in frequency-delta and time-delta (+1) form.
const (
	psHuffIIDFine   = 0
	psHuffIIDCoarse = 2
	psHuffICC       = 4
	psHuffIPD       = 6
	psHuffOPD       = 8
)

// psHuffOffset is the value of symbol 0 of each code.
var psHuffOffset = [10]int{-30, -30, -14, -14, -7, -7, 0, 0, 0, 0}

// The ten codes, built at start-up from the normative pairs in codeword order. They
// are read-only afterwards; a table that does not build is a bug and stops the program.
var psHuff = mustListedHuffs(aacPSHuffLens)

// psParams holds the indices of the envelopes of one parameter, by parameter band.
type psParams [psMaxEnv][psMaxBands]int8

// psState is the Parametric Stereo state of one channel element: the header in force
// and the parameters of the last frame, with what the next frame's parse reads back.
type psState struct {
	// The header in force.
	enableIID, enableICC, enableExt bool
	iidMode, iccMode                int
	iidQuant                        bool // IID in 31 steps instead of 15
	nrIID, nrICC, nrIPDOPD          int  // parameter bands
	enableIPDOPD                    bool // IPD and OPD were sent, and hold until a frame that sends none resets them
	is34, is34Old                   bool // the 34-band layout is in force, and was in the frame before
	start                           bool // a valid header has been read and no fault since: the data may be used

	// The envelopes of the last frame: numEnv counts the closing one a border short
	// of the end makes, border[0] is -1.
	frameClass         uint8
	numEnv, numEnvOld  int
	border             [psMaxEnv + 1]int8
	iid, icc, ipd, opd psParams

	// What the last frame carried, for tests and the library census.
	hdr          bool // the frame had a header
	ipdopdSent   bool // the frame sent IPD and OPD in its extension
	extraEnvelop bool // a border short of the end made an envelope
	slack        int  // bits of the data after the last field: the fill of the extension, under 8 for a well-formed one

	buf [sbrMaxPayload]byte // the data being parsed
}

// A psCoding states how the indices of one parameter are coded (docs/aac-ps-syntax.md,
// section 5): the frequency-delta Huffman table (the time-delta one follows it), the
// range an index must keep, or, for IPD and OPD, that the running value wraps modulo 8.
type psCoding struct {
	table  int
	lo, hi int
	wrap   bool
}

var (
	psCodeIIDFine   = psCoding{table: psHuffIIDFine, lo: -15, hi: 15}
	psCodeIIDCoarse = psCoding{table: psHuffIIDCoarse, lo: -7, hi: 7}
	psCodeICC       = psCoding{table: psHuffICC, lo: 0, hi: 7}
	psCodeIPD       = psCoding{table: psHuffIPD, wrap: true}
	psCodeOPD       = psCoding{table: psHuffOPD, wrap: true}
)

// psBits reads the fields of one ps_data inside its own nbits: a read that crosses
// the end (or a Huffman code that does not finish) sets bad and yields zeros, so that
// a parser can read a whole stage and check once.
type psBits struct {
	b   *aacBits
	end int
	bad bool
}

func (s *psBits) pos() int { return s.b.bitPos() }

// take reads an n-bit unsigned field.
func (s *psBits) take(n int) int {
	if s.bad || s.pos()+n > s.end {
		s.bad = true
		return 0
	}
	return int(s.b.read(n))
}

// symbol reads one Huffman symbol.
func (s *psBits) symbol(h *aacHuff) int {
	if s.bad {
		return 0
	}
	v, ok := h.decode(s.b)
	if !ok || s.pos() > s.end {
		s.bad = true
		return 0
	}
	return v
}

// skip moves n bits forward.
func (s *psBits) skip(n int) {
	if s.bad || s.pos()+n > s.end {
		s.bad = true
		return
	}
	s.b.skip(n)
}

// fail turns PS off until the next header and clears the four parameter arrays.
func (p *psState) fail() {
	p.iid, p.icc, p.ipd, p.opd = psParams{}, psParams{}, psParams{}, psParams{}
	p.start = false
}

// parse reads one ps_data of nbits bits from r and leaves r exactly nbits further.
func (p *psState) parse(r *aacBits, nbits int) {
	p.hdr, p.ipdopdSent, p.extraEnvelop, p.slack = false, false, false, 0
	if nbits < 0 {
		p.fail()
		return
	}
	if nbits > 8*len(p.buf) || nbits > r.left() {
		p.fail()
		r.skip(nbits)
		return
	}
	for i := 0; i < nbits; i += 8 {
		n := min(8, nbits-i)
		p.buf[i/8] = byte(r.read(n) << (8 - n))
	}
	s := &psBits{b: newAACBits(p.buf[:(nbits+7)/8]), end: nbits}
	if p.frame(s) != nil {
		p.fail()
		return
	}
	p.slack = nbits - s.pos()
}

// frame reads the fields in order and finishes the frame's state.
func (p *psState) frame(s *psBits) error {
	if err := p.header(s); err != nil {
		return err
	}
	p.frameClass = uint8(s.take(1))
	p.numEnvOld = p.numEnv
	p.numEnv = psEnvOf[p.frameClass][s.take(2)]
	if err := p.borders(s); err != nil {
		return err
	}
	if err := p.parameters(s); err != nil {
		return err
	}
	if err := p.extension(s); err != nil {
		return err
	}
	if s.bad {
		return psErr("fields run past the end of the data")
	}
	if err := p.closing(); err != nil {
		return err
	}
	if p.enableIID || p.enableICC {
		p.is34Old = p.is34
		p.is34 = p.enableIID && p.nrIID == 34 || p.enableICC && p.nrICC == 34
	}
	if p.hdr {
		p.start = true
	}
	return nil
}

// mode reads a 3-bit resolution mode; 6 and 7 are reserved.
func psMode(s *psBits, what string) (int, error) {
	m := s.take(3)
	if m > 5 {
		return 0, psErr("reserved %s mode %d", what, m)
	}
	return m, nil
}

// header reads the optional header and updates what persists.
func (p *psState) header(s *psBits) error {
	if s.take(1) == 0 {
		return nil
	}
	p.hdr = true
	var err error
	if p.enableIID = s.take(1) == 1; p.enableIID {
		if p.iidMode, err = psMode(s, "IID"); err != nil {
			return err
		}
		p.nrIID = psBandsOf[p.iidMode]
		p.nrIPDOPD = psIPDBandsOf[p.iidMode]
		p.iidQuant = p.iidMode >= 3
	}
	if p.enableICC = s.take(1) == 1; p.enableICC {
		if p.iccMode, err = psMode(s, "ICC"); err != nil {
			return err
		}
		p.nrICC = psBandsOf[p.iccMode]
	}
	p.enableExt = s.take(1) == 1
	return nil
}

// borders sets the envelope borders: computed for a fixed class, read for a variable one.
func (p *psState) borders(s *psBits) error {
	p.border[0] = -1
	for e := 1; e <= p.numEnv; e++ {
		if p.frameClass == 0 {
			p.border[e] = int8(e*psSlots/p.numEnv - 1)
			continue
		}
		b := s.take(5)
		if b < int(p.border[e-1]) {
			return psErr("envelope border %d falls to %d", e, b)
		}
		p.border[e] = int8(b)
	}
	return nil
}

// parameters reads all IID envelopes, then all ICC envelopes.
func (p *psState) parameters(s *psBits) error {
	if p.enableIID {
		for e := 0; e < p.numEnv; e++ {
			if err := p.envelope(s, &p.iid, e, p.iidCoding(), p.nrIID); err != nil {
				return err
			}
		}
	} else {
		p.iid = psParams{}
	}
	if p.enableICC {
		for e := 0; e < p.numEnv; e++ {
			if err := p.envelope(s, &p.icc, e, &psCodeICC, p.nrICC); err != nil {
				return err
			}
		}
	} else {
		p.icc = psParams{}
	}
	return nil
}

// iidCoding is the coding of IID at the resolution in force.
func (p *psState) iidCoding() *psCoding {
	if p.iidQuant {
		return &psCodeIIDFine
	}
	return &psCodeIIDCoarse
}

// envelope reads the time-delta flag and the nb indices of envelope e of par.
func (p *psState) envelope(s *psBits, par *psParams, e int, c *psCoding, nb int) error {
	tab := c.table
	ref := &par[max(p.numEnvOld-1, 0)]
	if e > 0 {
		ref = &par[e-1]
	}
	timeDelta := s.take(1) == 1
	if timeDelta {
		tab++
	}
	row, prev := &par[e], 0
	for b := 0; b < nb; b++ {
		v := int(aacPSHuffSyms[tab][s.symbol(psHuff[tab])]) + psHuffOffset[tab]
		if timeDelta {
			v += int(ref[b])
		} else {
			v += prev
		}
		if c.wrap {
			v &= 7
		} else if v < c.lo || v > c.hi {
			return psErr("index %d of band %d out of range %d..%d", v, b, c.lo, c.hi)
		}
		row[b], prev = int8(v), v
	}
	return nil
}

// extension reads the extension: items of a 2-bit id inside a budget of 8*size bits.
func (p *psState) extension(s *psBits) error {
	if !p.enableExt {
		p.afterExtension()
		return nil
	}
	size := s.take(4)
	if size == 15 {
		size += s.take(8)
	}
	budget := 8 * size
	for budget >= 8 {
		id := s.take(2)
		budget -= 2
		if id == 0 {
			at := s.pos()
			p.ipdopd(s)
			budget -= s.pos() - at
		}
	}
	if budget < 0 {
		return psErr("extension overruns its size")
	}
	s.skip(budget)
	p.afterExtension()
	return nil
}

// afterExtension clears IPD and OPD when they are not in force.
func (p *psState) afterExtension() {
	if !p.enableIPDOPD {
		p.ipd, p.opd = psParams{}, psParams{}
	}
}

// ipdopd reads the IPD and OPD item of the extension.
// IPD and OPD wrap and have no range, so reading them cannot fail on a value; a read
// past the end is caught by the frame (s.bad).
func (p *psState) ipdopd(s *psBits) {
	if p.enableIPDOPD = s.take(1) == 1; !p.enableIPDOPD {
		return
	}
	p.ipdopdSent = true
	for e := 0; e < p.numEnv; e++ {
		_ = p.envelope(s, &p.ipd, e, &psCodeIPD, p.nrIPDOPD)
		_ = p.envelope(s, &p.opd, e, &psCodeOPD, p.nrIPDOPD)
	}
	s.take(1)
}

// closing adds the envelope that ends at slot 31 when the last one ends before it.
func (p *psState) closing() error {
	if p.numEnv > 0 && p.border[p.numEnv] >= 31 {
		return nil
	}
	dst, src := p.numEnv, p.numEnv-1
	if p.numEnv == 0 {
		src = p.numEnvOld - 1
	}
	if src >= 0 {
		if p.enableIID {
			copy(p.iid[dst][:p.nrIID], p.iid[src][:p.nrIID])
			c := p.iidCoding()
			for _, v := range p.iid[dst][:p.nrIID] {
				if int(v) < c.lo || int(v) > c.hi {
					return psErr("repeated IID index %d out of range %d..%d", v, c.lo, c.hi)
				}
			}
		}
		if p.enableICC {
			copy(p.icc[dst][:p.nrICC], p.icc[src][:p.nrICC])
		}
		if p.enableIPDOPD {
			copy(p.ipd[dst][:p.nrIPDOPD], p.ipd[src][:p.nrIPDOPD])
			copy(p.opd[dst][:p.nrIPDOPD], p.opd[src][:p.nrIPDOPD])
		}
	}
	p.numEnv++
	p.border[p.numEnv] = 31
	p.extraEnvelop = true
	return nil
}
