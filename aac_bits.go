// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import "github.com/go-avkit/bitstream"

// aacBits reads a bit string most significant bit first, as every MPEG-4 audio
// syntax element is written, over go-avkit/bitstream's Reader. A read past the end
// does not return an error: it returns zeros and leaves the reader marked as
// overrun, so that a parser checks once per element instead of once per field.
type aacBits struct {
	r       *bitstream.Reader
	overrun bool
}

func newAACBits(b []byte) *aacBits { return &aacBits{r: bitstream.NewReader(b)} }

// left returns how many bits remain.
func (r *aacBits) left() int { return r.r.Left() }

// bitPos returns how many bits have been read.
func (r *aacBits) bitPos() int { return r.r.Pos() }

// bit reads one bit.
func (r *aacBits) bit() uint32 {
	v, err := r.r.Bit()
	if err != nil {
		r.overrun = true
		return 0
	}
	return v
}

// read reads n bits, 0 <= n <= 32. A read past the end consumes what is left.
func (r *aacBits) read(n int) uint32 {
	v, err := r.r.Bits(n)
	if err != nil {
		r.overrun = true
		return 0
	}
	return v
}

// peek returns the next n bits, 1 <= n <= 25, without moving; bits past the end
// read as zeros and do not mark an overrun.
func (r *aacBits) peek(n int) uint32 {
	v, _ := r.r.PeekPadded(n)
	return v
}

// skip moves n bits forward.
func (r *aacBits) skip(n int) {
	if r.r.Skip(n) != nil {
		r.r.Skip(r.r.Left())
		r.overrun = true
	}
}

// align moves to the next byte boundary.
func (r *aacBits) align() { r.r.Align() }
