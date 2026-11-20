// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

// maxFrameBytes is the largest Frame plane, and the largest Frame, the decoder builds: 2^31-1
// bytes, the same on every platform, so that a 32-bit host (where int wraps) and a 64-bit one
// refuse the same streams.
const maxFrameBytes = 1<<31 - 1

// chromaDivisors are SubWidthC and SubHeightC of a chroma format (ITU-T H.265 Table 6-1); 0 for a
// format with no chroma planes (and for a value that is no format).
func chromaDivisors(c Chroma) (subW, subH uint64) {
	switch c {
	case Chroma420:
		return 2, 2
	case Chroma422:
		return 2, 1
	case Chroma444:
		return 1, 1
	}
	return 0, 0
}

// frameBytes is the size in bytes of the Frame of a w x h picture of format c at luma depth
// depth and chroma depth depthC: each plane and the total. It is computed in uint64 and divides
// before it multiplies, so that no product wraps whatever the width of int; ok is false when a
// plane, or the total, is over maxFrameBytes, or when the shape is not a Frame's (a size or a
// depth out of range, an unknown format).
func frameBytes(w, h uint64, c Chroma, depth, depthC int) (y, chroma, total uint64, ok bool) {
	if w == 0 || h == 0 || depth < 8 || depth > 16 || c > Chroma444 {
		return 0, 0, 0, false
	}
	by := uint64(depthBytes(depth))
	// A plane has at least one byte a sample, so a side over the cap is over it however short the
	// other is. With both sides under it, w*h*2 is below 2^63 and 2*(cw*ch*2) below 2^64: no
	// product below wraps, and the total is compared with the cap once, at the end.
	if w > maxFrameBytes || h > maxFrameBytes {
		return 0, 0, 0, false
	}
	y = w * h * by
	total = y
	if c != Chroma400 {
		if depthC < 8 || depthC > 16 {
			return 0, 0, 0, false
		}
		bc := uint64(depthBytes(depthC))
		subW, subH := chromaDivisors(c)
		cw, ch := (w+subW-1)/subW, (h+subH-1)/subH
		chroma = cw * ch * bc
		total += 2 * chroma
	}
	return y, chroma, total, total <= maxFrameBytes
}
