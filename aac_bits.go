// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

// aacBits reads a bit string most significant bit first, as every MPEG-4 audio
// syntax element is written. A read past the end does not panic: it returns
// zeros and leaves the reader marked as overrun, so that a parser checks once
// per element instead of once per field.
type aacBits struct {
	b       []byte
	pos     int // next bit to read
	overrun bool
}

func newAACBits(b []byte) *aacBits { return &aacBits{b: b} }

// left returns how many bits remain.
func (r *aacBits) left() int { return len(r.b)*8 - r.pos }

// bit reads one bit.
func (r *aacBits) bit() uint32 {
	if r.pos >= len(r.b)*8 {
		r.overrun = true
		return 0
	}
	v := uint32(r.b[r.pos>>3]>>(7-r.pos&7)) & 1
	r.pos++
	return v
}

// read reads n bits, 0 <= n <= 32.
func (r *aacBits) read(n int) uint32 {
	if n > r.left() {
		r.pos = len(r.b) * 8
		r.overrun = true
		return 0
	}
	var v uint32
	for ; n > 0; n-- {
		v = v<<1 | uint32(r.b[r.pos>>3]>>(7-r.pos&7))&1
		r.pos++
	}
	return v
}

// peek returns the next n bits, 1 <= n <= 25, without moving; bits past the end
// read as zeros and do not mark an overrun.
func (r *aacBits) peek(n int) uint32 {
	i := r.pos >> 3
	var w uint32
	for k := 0; k < 4; k++ {
		w <<= 8
		if i+k < len(r.b) {
			w |= uint32(r.b[i+k])
		}
	}
	return w << uint(r.pos&7) >> uint(32-n)
}

// skip moves n bits forward.
func (r *aacBits) skip(n int) {
	if n > r.left() {
		r.pos = len(r.b) * 8
		r.overrun = true
		return
	}
	r.pos += n
}

// align moves to the next byte boundary.
func (r *aacBits) align() { r.pos = (r.pos + 7) &^ 7 }
