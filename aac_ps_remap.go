// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

// The parameter maps of docs/aac-ps-mixing.md section 4.2, written from that
// specification alone (a clean-room specification: no reference implementation was
// opened for this file). IID and ICC are each carried at 10, 20 or 34 bands and mapped
// to the layout's band count, per envelope, on the indices. The same maps applied to
// gain values give the carried matrix of a 20 <-> 34 layout switch (section 4.3).

// psMapEnt is one target band of a map: the weighted sum of up to four source bands
// over a divisor. Weights of 1 and a divisor equal to their count are the plain
// averages; (2a+b)/3 is the 1/3 - 2/3 weighting of the first four bands of 34 -> 20.
type psMapEnt struct {
	src [4]uint8
	w   [4]uint8
	div uint8
}

func psAvg(div uint8, src ...uint8) psMapEnt {
	e := psMapEnt{div: div}
	for i, s := range src {
		e.src[i], e.w[i] = s, 1
	}
	return e
}

func psWeighted(a, b uint8, wa, wb uint8) psMapEnt {
	return psMapEnt{src: [4]uint8{a, b}, w: [4]uint8{wa, wb}, div: wa + wb}
}

// psMap34to20 and psMap20to34 are normative values (table numbers not verified; the
// 10 -> 20 map, each source band twice, is a formula, and 10 -> 34 is the composition
// of the two).
var (
	psMap34to20 = [20]psMapEnt{
		psWeighted(0, 1, 2, 1), psWeighted(1, 2, 1, 2), psWeighted(3, 4, 2, 1), psWeighted(4, 5, 1, 2),
		psAvg(2, 6, 7), psAvg(2, 8, 9), psAvg(1, 10), psAvg(1, 11),
		psAvg(2, 12, 13), psAvg(2, 14, 15), psAvg(1, 16), psAvg(1, 17),
		psAvg(1, 18), psAvg(1, 19), psAvg(2, 20, 21), psAvg(2, 22, 23),
		psAvg(2, 24, 25), psAvg(2, 26, 27), psAvg(4, 28, 29, 30, 31), psAvg(2, 32, 33),
	}
	psMap20to34 = [34]psMapEnt{
		psAvg(1, 0), psAvg(2, 0, 1), psAvg(1, 1), psAvg(1, 2), psAvg(2, 2, 3), psAvg(1, 3),
		psAvg(1, 4), psAvg(1, 4), psAvg(1, 5), psAvg(1, 5), psAvg(1, 6), psAvg(1, 7),
		psAvg(1, 8), psAvg(1, 8), psAvg(1, 9), psAvg(1, 9), psAvg(1, 10), psAvg(1, 11),
		psAvg(1, 12), psAvg(1, 13), psAvg(1, 14), psAvg(1, 14), psAvg(1, 15), psAvg(1, 15),
		psAvg(1, 16), psAvg(1, 16), psAvg(1, 17), psAvg(1, 17), psAvg(1, 18), psAvg(1, 18),
		psAvg(1, 18), psAvg(1, 18), psAvg(1, 19), psAvg(1, 19),
	}
)

// psRemapNum is the type a map works on: integer indices, where the average truncates
// toward zero, and float64 gains, where it is exact.
type psRemapNum interface{ ~int | ~int8 | ~float64 }

// psRemapBands maps the from bands of src (10, 20 or 34) to the to bands of dst (20 or
// 34); from == to copies.
func psRemapBands[T psRemapNum](dst, src []T, from, to int) {
	switch {
	case from == to:
		copy(dst[:to], src[:from])
	case from == 10:
		var mid [20]T
		for j := range mid {
			mid[j] = src[j/2]
		}
		if to == 20 {
			copy(dst, mid[:])
			return
		}
		psRemapBands(dst, mid[:], 20, 34)
	case to == 34:
		psApplyMap(dst, src, psMap20to34[:])
	default:
		psApplyMap(dst, src, psMap34to20[:])
	}
}

func psApplyMap[T psRemapNum](dst, src []T, m []psMapEnt) {
	for j, e := range m {
		var sum T
		for i, s := range e.src {
			sum += T(e.w[i]) * src[s]
		}
		dst[j] = sum / T(e.div)
	}
}
