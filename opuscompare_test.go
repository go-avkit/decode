// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"fmt"
	"math"
)

// opusCompare is a Go port of opus_compare.c from the libopus tree
// (github.com/xiph/opus, src/opus_compare.c), Copyright (c) 2011-2012 Xiph.Org
// Foundation and Mozilla Corporation, written by Jean-Marc Valin and Timothy B.
// Terriberry, under the BSD licence of the original (retained in the notice below).
// RFC 6716 section 6 makes its quality metric the conformance test of an Opus
// decoder: the output must be "within the thresholds specified by the
// opus_compare.c tool", which is a quality of 0 or more.
//
//	Redistribution and use in source and binary forms, with or without
//	modification, are permitted provided that the following conditions are met:
//	- Redistributions of source code must retain the above copyright notice, this
//	  list of conditions and the following disclaimer.
//	- Redistributions in binary form must reproduce the above copyright notice,
//	  this list of conditions and the following disclaimer in the documentation
//	  and/or other materials provided with the distribution.
//	THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
//	AND ANY EXPRESS OR IMPLIED WARRANTIES ARE DISCLAIMED (full text in the
//	original file).
//
// The arithmetic is single precision where the C is, and every float32 product is
// converted explicitly so that the compiler cannot fuse it into a multiply-add,
// which would change the last bit on arm64.
const (
	ocBands   = 21
	ocFreqs   = 240
	ocWinSize = 480
	ocWinStep = 120
)

var ocBandEdges = [ocBands + 1]int{0, 2, 4, 6, 8, 10, 12, 14, 16, 20, 24, 28, 32, 40, 48, 56, 68, 80, 96, 120, 156, 200}

// ocBandEnergy is band_energy of opus_compare.c. in holds interleaved samples.
func ocBandEnergy(out, ps []float32, bands []int, nbands int, in []float32, nch, nframes, window, step, downsample int) {
	win := make([]float32, window)
	c := make([]float32, window)
	s := make([]float32, window)
	x := make([]float32, nch*window)
	psz := window / 2
	const pi = float32(3.14159265)
	for j := range window {
		win[j] = float32(0.5) - float32(0.5)*float32(math.Cos(float64(float32(2*pi/float32(window-1))*float32(j))))
	}
	for j := range window {
		c[j] = float32(math.Cos(float64(float32(2*pi/float32(window)) * float32(j))))
		s[j] = float32(math.Sin(float64(float32(2*pi/float32(window)) * float32(j))))
	}
	for xi := range nframes {
		for ci := range nch {
			for k := range window {
				x[ci*window+k] = float32(win[k] * in[(xi*step+k)*nch+ci])
			}
		}
		xj := 0
		for bi := range nbands {
			var p [2]float32
			for ; xj < bands[bi+1]; xj++ {
				for ci := range nch {
					var re, im float32
					ti := 0
					for k := range window {
						re += float32(c[ti] * x[ci*window+k])
						im -= float32(s[ti] * x[ci*window+k])
						ti += xj
						if ti >= window {
							ti -= window
						}
					}
					re *= float32(downsample)
					im *= float32(downsample)
					v := float32(re*re) + float32(im*im) + 100000
					ps[(xi*psz+xj)*nch+ci] = v
					p[ci] += v
				}
			}
			if out != nil {
				out[(xi*nbands+bi)*nch] = p[0] / float32(bands[bi+1]-bands[bi])
				if nch == 2 {
					out[(xi*nbands+bi)*nch+1] = p[1] / float32(bands[bi+1]-bands[bi])
				}
			}
		}
	}
}

// opusCompare returns the Opus quality metric of y against the reference x, both
// 16-bit interleaved: x with two channels (as the reference vectors are), y with
// nch (1 or 2) at the given rate. It is main() of opus_compare.c. A negative
// result is "Test vector FAILS"; 100 means identical.
func opusCompare(xs []int16, ys []int16, nch, rate int) (q float64, err error) {
	ybands, yfreqs, downsample := ocBands, ocFreqs, 1
	switch rate {
	case 8000:
		ybands = 13
	case 12000:
		ybands = 15
	case 16000:
		ybands = 17
	case 24000:
		ybands = 19
	case 48000:
	default:
		return 0, fmt.Errorf("sampling rate %d", rate)
	}
	downsample = 48000 / rate
	yfreqs = ocFreqs / downsample
	xlength := len(xs) / 2
	x := make([]float32, 2*xlength)
	for i := range x {
		x[i] = float32(xs[i])
	}
	if nch == 1 {
		for xi := range xlength {
			x[xi] = float32(0.5 * float64(x[2*xi]+x[2*xi+1]))
		}
	}
	ylength := len(ys) / nch
	y := make([]float32, nch*ylength)
	for i := range y {
		y[i] = float32(ys[i])
	}
	if xlength != ylength*downsample {
		return 0, fmt.Errorf("sample counts do not match (%d!=%d)", xlength, ylength*downsample)
	}
	if xlength < ocWinSize {
		return 0, errors.New("insufficient sample data")
	}
	nframes := (xlength - ocWinSize + ocWinStep) / ocWinStep
	xb := make([]float32, nframes*ocBands*nch)
	X := make([]float32, nframes*ocFreqs*nch)
	Y := make([]float32, nframes*yfreqs*nch)
	ocBandEnergy(xb, X, ocBandEdges[:], ocBands, x, nch, nframes, ocWinSize, ocWinStep, 1)
	ocBandEnergy(nil, Y, ocBandEdges[:], ybands, y, nch, nframes, ocWinSize/downsample, ocWinStep/downsample, downsample)
	for xi := range nframes {
		for bi := 1; bi < ocBands; bi++ {
			for ci := range nch {
				xb[(xi*ocBands+bi)*nch+ci] += float32(0.1 * xb[(xi*ocBands+bi-1)*nch+ci])
			}
		}
		for bi := ocBands - 1; bi > 0; bi-- {
			for ci := range nch {
				xb[(xi*ocBands+bi-1)*nch+ci] += float32(0.03 * xb[(xi*ocBands+bi)*nch+ci])
			}
		}
		if xi > 0 {
			for bi := range ocBands {
				for ci := range nch {
					xb[(xi*ocBands+bi)*nch+ci] += float32(0.5 * xb[((xi-1)*ocBands+bi)*nch+ci])
				}
			}
		}
		if nch == 2 {
			for bi := range ocBands {
				l, r := xb[(xi*ocBands+bi)*nch], xb[(xi*ocBands+bi)*nch+1]
				xb[(xi*ocBands+bi)*nch] += float32(0.01 * r)
				xb[(xi*ocBands+bi)*nch+1] += float32(0.01 * l)
			}
		}
		for bi := range ybands {
			for xj := ocBandEdges[bi]; xj < ocBandEdges[bi+1]; xj++ {
				for ci := range nch {
					m := float32(0.1 * xb[(xi*ocBands+bi)*nch+ci])
					X[(xi*ocFreqs+xj)*nch+ci] += m
					Y[(xi*yfreqs+xj)*nch+ci] += m
				}
			}
		}
	}
	for bi := range ybands {
		for xj := ocBandEdges[bi]; xj < ocBandEdges[bi+1]; xj++ {
			for ci := range nch {
				xtmp, ytmp := X[xj*nch+ci], Y[xj*nch+ci]
				for xi := 1; xi < nframes; xi++ {
					xtmp2, ytmp2 := X[(xi*ocFreqs+xj)*nch+ci], Y[(xi*yfreqs+xj)*nch+ci]
					X[(xi*ocFreqs+xj)*nch+ci] += xtmp
					Y[(xi*yfreqs+xj)*nch+ci] += ytmp
					xtmp, ytmp = xtmp2, ytmp2
				}
			}
		}
	}
	maxCompare := ocBandEdges[ocBands]
	switch rate {
	case 48000:
	case 12000:
		maxCompare = ocBandEdges[ybands]
	default:
		maxCompare = ocBandEdges[ybands] - 3
	}
	var e float64
	for xi := range nframes {
		var ef float64
		for bi := range ybands {
			var eb float64
			for xj := ocBandEdges[bi]; xj < ocBandEdges[bi+1] && xj < maxCompare; xj++ {
				for ci := range nch {
					re := Y[(xi*yfreqs+xj)*nch+ci] / X[(xi*ocFreqs+xj)*nch+ci]
					im := float32(float64(re) - math.Log(float64(re)) - 1)
					if xj >= 79 && xj <= 81 {
						im *= 0.1
					}
					if xj == 80 {
						im *= 0.1
					}
					eb += float64(im)
				}
			}
			eb /= float64((ocBandEdges[bi+1] - ocBandEdges[bi]) * nch)
			ef += eb * eb
		}
		ef /= ocBands
		ef *= ef
		e += ef * ef
	}
	e = math.Pow(e/float64(nframes), 1.0/16)
	return float64(float32(100 * (1 - 0.5*math.Log(1+e)/math.Log(1.13)))), nil
}
