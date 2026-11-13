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

// fail turns PS off until the next header and clears the parameters.
func (p *psState) fail() {
	p.start = false
	p.iid, p.icc, p.ipd, p.opd = psParams{}, psParams{}, psParams{}, psParams{}
}

// parse reads the ps_data of nbits bits that start at r's position and moves r to
// their end. A fault is not returned: PS is turned off (fail) and the caller goes on.
func (p *psState) parse(r *aacBits, nbits int) {
	n := (nbits + 7) / 8
	for i := 0; i < n; i++ {
		take := min(8, nbits-8*i)
		p.buf[i] = byte(r.read(take) << uint(8-take))
	}
	sub := newAACBits(p.buf[:n])
	err := p.data(sub)
	if err == nil && (sub.overrun || sub.bitPos() > nbits) {
		err = psErr("data of %d bits read %d", nbits, sub.bitPos())
	}
	if err != nil {
		p.fail()
		return
	}
	p.slack = nbits - sub.bitPos()
}

// data reads ps_data.
func (p *psState) data(r *aacBits) error {
	p.hdr, p.ipdopdSent, p.extraEnvelop = false, false, false
	if r.bit() != 0 { // enable_ps_header
		p.hdr = true
		if err := p.header(r); err != nil {
			return err
		}
	}
	p.frameClass = uint8(r.bit())
	p.numEnvOld = p.numEnv
	p.numEnv = psEnvOf[p.frameClass][r.read(2)]
	p.border[0] = -1
	for e := 1; e <= p.numEnv; e++ {
		if p.frameClass == 0 {
			// the frame cut in numEnv equal parts, each border the last slot of its part
			p.border[e] = int8(e*psSlots/p.numEnv - 1)
			continue
		}
		p.border[e] = int8(r.read(5))
		if p.border[e] < p.border[e-1] {
			return psErr("borders %d then %d", p.border[e-1], p.border[e])
		}
	}
	if p.enableIID {
		code, lim := psHuffIIDCoarse, 7
		if p.iidQuant {
			code, lim = psHuffIIDFine, 15
		}
		for e := 0; e < p.numEnv; e++ {
			dt := int(r.bit())
			if err := p.readEnvelope(r, &p.iid, e, p.nrIID, code+dt, dt != 0, -lim, lim, 0); err != nil {
				return err
			}
		}
	} else {
		p.iid = psParams{}
	}
	if p.enableICC {
		for e := 0; e < p.numEnv; e++ {
			dt := int(r.bit())
			if err := p.readEnvelope(r, &p.icc, e, p.nrICC, psHuffICC+dt, dt != 0, 0, 7, 0); err != nil {
				return err
			}
		}
	} else {
		p.icc = psParams{}
	}
	if p.enableExt {
		if err := p.extensions(r); err != nil {
			return err
		}
	}
	if err := p.closeEnvelopes(); err != nil {
		return err
	}
	p.is34Old = p.is34
	if p.enableIID || p.enableICC {
		p.is34 = (p.enableIID && p.nrIID == 34) || (p.enableICC && p.nrICC == 34)
	}
	if !p.enableIPDOPD {
		p.ipd, p.opd = psParams{}, psParams{}
	}
	if p.hdr {
		p.start = true
	}
	return nil
}

// header reads the rest of the header: the IID and ICC modes and the extension flag.
func (p *psState) header(r *aacBits) error {
	if p.enableIID = r.bit() != 0; p.enableIID {
		m := int(r.read(3))
		if m >= len(psBandsOf) {
			return psErr("iid_mode %d is reserved", m)
		}
		p.iidMode, p.iidQuant = m, m > 2
		p.nrIID, p.nrIPDOPD = psBandsOf[m], psIPDBandsOf[m]
	}
	if p.enableICC = r.bit() != 0; p.enableICC {
		m := int(r.read(3))
		if m >= len(psBandsOf) {
			return psErr("icc_mode %d is reserved", m)
		}
		p.iccMode, p.nrICC = m, psBandsOf[m]
	}
	p.enableExt = r.bit() != 0
	return nil
}

// readEnvelope reads the n indices of envelope e of one parameter with Huffman code
// code, coded as differences to the previous band (dt false) or to the same band of
// the previous envelope (dt true, the last envelope of the previous frame for the
// first one). Sums wrap with mask when it is not 0 and must lie in lo..hi.
func (p *psState) readEnvelope(r *aacBits, par *psParams, e, n, code int, dt bool, lo, hi, mask int) error {
	prev := e - 1
	if e == 0 {
		prev = max(p.numEnvOld-1, 0)
	}
	v := 0
	for b := 0; b < n; b++ {
		s, ok := psHuff[code].decode(r)
		if !ok {
			return errTruncated()
		}
		d := int(aacPSHuffSyms[code][s]) + psHuffOffset[code]
		if dt {
			v = int(par[prev][b]) + d
		} else {
			v += d
		}
		if mask != 0 {
			v &= mask
		}
		if v < lo || v > hi {
			return psErr("index %d outside %d to %d", v, lo, hi)
		}
		par[e][b] = int8(v)
	}
	return nil
}

// extensions reads the extension of the header's flag: a byte count, then extensions
// of a 2-bit id each, until fewer than 8 bits remain. Only id 0 (IPD and OPD) is
// defined; the others have no known length and are passed over as the standard's
// loop does, taking their id bits only.
func (p *psState) extensions(r *aacBits) error {
	count := int(r.read(4))
	if count == 15 {
		count += int(r.read(8))
	}
	left := 8 * count
	for left > 7 {
		id := r.read(2)
		left -= 2
		if id == 0 {
			n, err := p.ipdopd(r)
			if err != nil {
				return err
			}
			left -= n
		}
	}
	if left < 0 {
		return psErr("extension overflows by %d bits", -left)
	}
	r.skip(left)
	return nil
}

// ipdopd reads the extension 0: a flag, IPD and OPD of every envelope, a reserved
// bit. It returns its length in bits.
func (p *psState) ipdopd(r *aacBits) (int, error) {
	start := r.bitPos()
	if p.enableIPDOPD = r.bit() != 0; p.enableIPDOPD {
		p.ipdopdSent = true
		for e := 0; e < p.numEnv; e++ {
			dt := int(r.bit())
			if err := p.readEnvelope(r, &p.ipd, e, p.nrIPDOPD, psHuffIPD+dt, dt != 0, 0, 7, 7); err != nil {
				return 0, err
			}
			dt = int(r.bit())
			if err := p.readEnvelope(r, &p.opd, e, p.nrIPDOPD, psHuffOPD+dt, dt != 0, 0, 7, 7); err != nil {
				return 0, err
			}
		}
	}
	r.skip(1) // reserved_ps
	return r.bitPos() - start, nil
}

// closeEnvelopes makes the frame end at its last slot: when there is no envelope or
// the last border is short of the end, one more envelope repeats the last one (the
// last of the previous frame when there is none) and ends at the last slot. A repeated
// IID index is checked again, the resolution may have changed since it was read (an ICC
// index is valid whatever the resolution, and so is one that was cleared).
func (p *psState) closeEnvelopes() error {
	if p.numEnv > 0 && int(p.border[p.numEnv]) == psSlots-1 {
		return nil
	}
	src := p.numEnv - 1
	if p.numEnv == 0 {
		src = p.numEnvOld - 1
	}
	if src >= 0 && src != p.numEnv {
		if p.enableIID {
			p.iid[p.numEnv] = p.iid[src]
		}
		if p.enableICC {
			p.icc[p.numEnv] = p.icc[src]
		}
		if p.enableIPDOPD {
			p.ipd[p.numEnv] = p.ipd[src]
			p.opd[p.numEnv] = p.opd[src]
		}
	}
	if p.enableIID {
		lim := 7
		if p.iidQuant {
			lim = 15
		}
		for _, v := range p.iid[p.numEnv][:p.nrIID] {
			if int(v) < -lim || int(v) > lim {
				return psErr("repeated IID index %d", v)
			}
		}
	}
	p.numEnv++
	p.border[p.numEnv] = psSlots - 1
	p.extraEnvelop = true
	return nil
}
