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
}

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

// decode reads one symbol. The tree is complete, so every bit string leads to a
// symbol; a reader that runs out returns false.
func (h *aacHuff) decode(r *aacBits) (int, bool) {
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
