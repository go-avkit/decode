// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"fmt"
	"sync"
)

// aacHuff decodes one prefix code, bit by bit, down a binary tree. Node 0 is the
// root. A child value c >= 0 is the index of a node; c < 0 is the leaf for symbol
// -c-1. A child of 0 that no code reaches stays 0, which no code can name (the
// root is never a child), so an unused branch is detected by the tree check.
type aacHuff struct {
	node [][2]int32
	// lut answers the codes of at most aacLUTBits bits in one lookup, indexed by the
	// next aacLUTBits bits of the stream: symbol<<8 | length, or 0 for a longer code,
	// which is then walked down the tree.
	lut [1 << aacLUTBits]uint32
}

const aacLUTBits = 9

// buildAACHuff builds the tree of a code given by codewords and lengths and
// checks it is a complete prefix code: no codeword is the start of another and no
// branch is left open (Kraft's sum is exactly one).
func buildAACHuff(codes []uint32, lens []uint8) (*aacHuff, error) {
	if len(codes) != len(lens) || len(codes) == 0 {
		return nil, errors.New("aac huffman: codes and lengths differ")
	}
	h := &aacHuff{node: make([][2]int32, 1, 2*len(codes))}
	for sym, c := range codes {
		n := int(lens[sym])
		if n < 1 || n > 32 || (n < 32 && c>>uint(n) != 0) {
			return nil, fmt.Errorf("aac huffman: symbol %d has a codeword of %d bits that is %#x", sym, n, c)
		}
		if n <= aacLUTBits {
			base := int(c) << uint(aacLUTBits-n)
			for j := 0; j < 1<<uint(aacLUTBits-n); j++ {
				h.lut[base+j] = uint32(sym)<<8 | uint32(n)
			}
		}
		cur := 0
		for i := n - 1; i >= 0; i-- {
			b := (c >> uint(i)) & 1
			next := h.node[cur][b]
			if i == 0 {
				if next != 0 {
					return nil, fmt.Errorf("aac huffman: symbol %d repeats a code or is a prefix", sym)
				}
				h.node[cur][b] = -int32(sym) - 1
				break
			}
			if next < 0 {
				return nil, fmt.Errorf("aac huffman: symbol %d extends the code of another", sym)
			}
			if next == 0 {
				h.node = append(h.node, [2]int32{})
				next = int32(len(h.node) - 1)
				h.node[cur][b] = next
			}
			cur = int(next)
		}
	}
	for i, n := range h.node {
		if n[0] == 0 || n[1] == 0 {
			return nil, fmt.Errorf("aac huffman: node %d has an open branch", i)
		}
	}
	return h, nil
}

// buildListedHuff builds the code of entries listed in codeword order, given their
// lengths: the codeword of an entry is the next free slot of the code space at its
// length, which must be a slot of that length (the entries before it filled a whole
// number of them). Lengths that never decrease give the canonical code; the PS codes
// are listed in an order that is not by length. The code is checked complete.
func buildListedHuff(lens []uint8) (*aacHuff, error) {
	codes := make([]uint32, len(lens))
	var next uint64 // the first free codeword, left-aligned in 32 bits
	for i, l := range lens {
		if l < 1 || l > 32 {
			return nil, fmt.Errorf("aac huffman: entry %d has a length of %d bits", i, l)
		}
		span := uint64(1) << (32 - l)
		if next%span != 0 || next >= 1<<32 {
			return nil, fmt.Errorf("aac huffman: entry %d of %d bits does not fit the code space", i, l)
		}
		codes[i] = uint32(next >> (32 - l))
		next += span
	}
	return buildAACHuff(codes, lens)
}

// buildListedHuffs builds ten codes with buildListedHuff.
func buildListedHuffs(lens [10][]uint8) (hs [10]*aacHuff, err error) {
	for i, l := range lens {
		if hs[i], err = buildListedHuff(l); err != nil {
			return hs, fmt.Errorf("huffman code %d: %w", i, err)
		}
	}
	return hs, nil
}

// mustListedHuffs is buildListedHuffs for tables that are constants of the program:
// one that does not build is a bug.
func mustListedHuffs(lens [10][]uint8) [10]*aacHuff {
	hs, err := buildListedHuffs(lens)
	if err != nil {
		panic(err)
	}
	return hs
}

// decode reads one symbol. The tree is complete, so every bit string leads to a
// symbol; a reader that runs out returns false.
func (h *aacHuff) decode(r *aacBits) (int, bool) {
	if e := h.lut[r.peek(aacLUTBits)]; e != 0 {
		r.skip(int(e & 0xff))
		return int(e >> 8), !r.overrun
	}
	cur := 0
	for {
		c := h.node[cur][r.bit()]
		if r.overrun {
			return 0, false
		}
		if c < 0 {
			return int(-c - 1), true
		}
		cur = int(c)
	}
}

// The codebooks, built once from the normative tables in aac_tables.go.
var (
	aacHuffOnce     sync.Once
	aacHuffSpectral [11]*aacHuff
	aacHuffScale    *aacHuff
	aacHuffErr      error
)

func aacHuffInit() {
	aacHuffSpectral, aacHuffScale, aacHuffErr = buildAACCodebooks(aacSpectralCodes, aacSpectralBits, aacScalefactorCodes, aacScalefactorBits)
}

// buildAACCodebooks builds the twelve codebooks from their tables.
func buildAACCodebooks(spec [11][]uint16, specBits [11][]uint8, sf []uint32, sfBits []uint8) (hs [11]*aacHuff, scale *aacHuff, err error) {
	for i := range hs {
		codes := make([]uint32, len(spec[i]))
		for j, c := range spec[i] {
			codes[j] = uint32(c)
		}
		if hs[i], err = buildAACHuff(codes, specBits[i]); err != nil {
			return hs, nil, fmt.Errorf("spectrum codebook %d: %w", i+1, err)
		}
	}
	scale, err = buildAACHuff(sf, sfBits)
	return hs, scale, err
}

// aacCodebooks returns the spectrum codebooks 1 to 11 (index 0 to 10) and the
// scalefactor codebook.
func aacCodebooks() (*[11]*aacHuff, *aacHuff, error) {
	aacHuffOnce.Do(aacHuffInit)
	return &aacHuffSpectral, aacHuffScale, aacHuffErr
}
