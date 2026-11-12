// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"fmt"
	"math"
	"slices"
)

// SBR frequency tables (HE-AAC v1): the master table, the high, low, noise and
// limiter tables derived from it, and the patch layout of the high frequency
// generator. Written from the derivation rules of ISO/IEC 14496-3 subpart 4 as
// ffmpeg n9.0.2 libavcodec/aacsbr_template.c:122-186, 249-622 states them (read,
// not copied; docs/aac-design.md lists the lines). The real-valued steps (a
// logarithm, a power) are evaluated in float64, where ffmpeg uses float32: a
// result differs only when a value lies within float32 rounding of a half.

// Sizes of the tables, as the derivation bounds them.
const (
	sbrMaxMaster = 49 // f_master: at most 48 bands
	sbrMaxHigh   = 49
	sbrMaxLow    = 25
	sbrMaxFNoise = 6
	sbrMaxLim    = 32
	sbrMaxPatch  = 6
)

// sbrOffset is the start-frequency offset table of one sampling-rate class, by
// bs_start_freq. The rows are, in order: fs_sbr 16000, 22050, 24000, 32000,
// 44100 to 64000, above 64000. Read in ffmpeg n9.0.2 aacsbrdata.h:35-42.
var sbrOffset = [6][16]int8{
	{-8, -7, -6, -5, -4, -3, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7},
	{-5, -4, -3, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13},
	{-5, -3, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13, 16},
	{-6, -4, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13, 16},
	{-4, -2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13, 16, 20},
	{-2, -1, 0, 1, 2, 3, 4, 5, 6, 7, 9, 11, 13, 16, 20, 24},
}

// sbrLimWarp is 2^(0.49/x) for x = 1.2, 2, 3: the limiter bands per octave of
// bs_limiter_bands 1, 2, 3 (14496-3 limiter frequency band table).
var sbrLimWarp = [3]float64{math.Exp2(0.49 / 1.2), math.Exp2(0.49 / 2), math.Exp2(0.49 / 3)}

// sbrSpectrum is the part of the SBR header that, when it changes, rebuilds the
// frequency tables.
type sbrSpectrum struct {
	startFreq, stopFreq, xoverBand uint8
	freqScale, alterScale          uint8
	noiseBands                     uint8
}

// sbrTables holds the frequency tables of one SBR element.
type sbrTables struct {
	rate    int    // the SBR (output) sampling rate
	k       [3]int // k0, k1, k2
	kx, m   [2]int // [1] current, [0] the previous frame's
	nMaster int
	fMaster [sbrMaxMaster]int
	n       [2]int // N_low, N_high
	nQ      int    // noise floor bands
	nLim    int    // limiter bands
	fLow    [sbrMaxLow]int
	fHigh   [sbrMaxHigh]int
	fNoise  [sbrMaxFNoise]int
	fLim    [sbrMaxLim]int

	numPatches int
	patchNum   [sbrMaxPatch]int
	patchStart [sbrMaxPatch]int
}

var errSBRTables = errors.New("invalid SBR frequency tables")

func sbrTableErr(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errSBRTables, fmt.Sprintf(format, a...))
}

// sbrMakeBands splits [start, stop) into num bands whose widths grow
// geometrically: the width of band k is the difference of the rounded values of
// start*(stop/start)^(k/num). It writes the widths to out[:num].
func sbrMakeBands(out []int, start, stop, num int) {
	base := math.Pow(float64(stop)/float64(start), 1/float64(num))
	prod := float64(start)
	prev := start
	for k := 0; k < num-1; k++ {
		prod *= base
		cur := int(math.RoundToEven(prod))
		out[k] = cur - prev
		prev = cur
	}
	out[num-1] = stop - prev
}

// sbrRateClass returns the row of sbrOffset for a sampling rate, or -1.
func sbrRateClass(rate int) int {
	switch rate {
	case 16000:
		return 0
	case 22050:
		return 1
	case 24000:
		return 2
	case 32000:
		return 3
	case 44100, 48000, 64000:
		return 4
	case 88200, 96000, 128000, 176400, 192000:
		return 5
	}
	return -1
}

// makeMaster builds the master frequency band table from the spectrum
// parameters.
func (t *sbrTables) makeMaster(sp sbrSpectrum) error {
	class := sbrRateClass(t.rate)
	if class < 0 {
		return sbrTableErr("sampling rate %d", t.rate)
	}
	temp := 5000
	if t.rate < 32000 {
		temp = 3000
	} else if t.rate < 64000 {
		temp = 4000
	}
	startMin := ((temp << 7) + t.rate>>1) / t.rate
	stopMin := ((temp << 8) + t.rate>>1) / t.rate

	t.k[0] = startMin + int(sbrOffset[class][sp.startFreq])
	switch {
	case sp.stopFreq < 14:
		t.k[2] = stopMin
		var dk [13]int
		sbrMakeBands(dk[:], stopMin, 64, 13)
		slices.Sort(dk[:])
		for i := 0; i < int(sp.stopFreq); i++ {
			t.k[2] += dk[i]
		}
	case sp.stopFreq == 14:
		t.k[2] = 2 * t.k[0]
	default:
		t.k[2] = 3 * t.k[0]
	}
	t.k[2] = min(64, t.k[2])

	maxSub := 32
	switch {
	case t.rate <= 32000:
		maxSub = 48
	case t.rate == 44100:
		maxSub = 35
	}
	// Every header at every rate stays within the 48 bands the arrays hold, and
	// every derived table is strictly increasing: TestSBRTablesSweep builds all of
	// them (the input space is finite) and checks it, which is why the steps below
	// do not test for what no header can produce.
	if t.k[2]-t.k[0] > maxSub {
		return sbrTableErr("k0 %d, k2 %d", t.k[0], t.k[2])
	}

	xover := int(sp.xoverBand)
	if sp.freqScale == 0 {
		dk := int(sp.alterScale) + 1
		t.nMaster = ((t.k[2] - t.k[0] + dk&2) >> uint(dk)) << 1
		if err := t.checkMaster(xover); err != nil {
			return err
		}
		var widths [sbrMaxMaster]int
		for k := 1; k <= t.nMaster; k++ {
			widths[k] = dk
		}
		if diff := t.k[2] - t.k[0] - t.nMaster*dk; diff < 0 {
			widths[1]--
			if diff < -1 {
				widths[2]--
			}
		} else if diff > 0 {
			widths[t.nMaster]++
		}
		t.fMaster[0] = t.k[0]
		for k := 1; k <= t.nMaster; k++ {
			t.fMaster[k] = t.fMaster[k-1] + widths[k]
		}
		return nil
	}

	half := 7 - int(sp.freqScale)
	twoRegions := 49*t.k[2] > 110*t.k[0]
	if twoRegions {
		t.k[1] = 2 * t.k[0]
	} else {
		t.k[1] = t.k[2]
	}
	nb0 := int(math.RoundToEven(float64(half)*math.Log2(float64(t.k[1])/float64(t.k[0])))) * 2
	if nb0 <= 0 || nb0 > 48 {
		return sbrTableErr("num_bands_0 %d", nb0)
	}
	var vk0 [sbrMaxMaster]int
	sbrMakeBands(vk0[1:], t.k[0], t.k[1], nb0)
	slices.Sort(vk0[1 : nb0+1])
	vdk0Max := vk0[nb0]
	vk0[0] = t.k[0]
	for k := 1; k <= nb0; k++ {
		if vk0[k] <= 0 {
			return sbrTableErr("vDk0[%d] = %d", k, vk0[k])
		}
		vk0[k] += vk0[k-1]
	}
	if !twoRegions {
		t.nMaster = nb0
		if err := t.checkMaster(xover); err != nil {
			return err
		}
		copy(t.fMaster[:], vk0[:nb0+1])
		return nil
	}

	warp := 1.0
	if sp.alterScale != 0 {
		warp = 10.0 / 13.0
	}
	nb1 := int(math.RoundToEven(float64(half)*warp*math.Log2(float64(t.k[2])/float64(t.k[1])))) * 2
	var vk1 [sbrMaxMaster]int
	sbrMakeBands(vk1[1:], t.k[1], t.k[2], nb1)
	if slices.Min(vk1[1:nb1+1]) < vdk0Max {
		slices.Sort(vk1[1 : nb1+1])
		change := min(vdk0Max-vk1[1], (vk1[nb1]-vk1[1])>>1)
		vk1[1] += change
		vk1[nb1] -= change
	}
	slices.Sort(vk1[1 : nb1+1])
	vk1[0] = t.k[1]
	for k := 1; k <= nb1; k++ {
		vk1[k] += vk1[k-1]
	}
	t.nMaster = nb0 + nb1
	copy(t.fMaster[:], vk0[:nb0+1])
	copy(t.fMaster[nb0+1:], vk1[1:nb1+1])
	return nil
}

func (t *sbrTables) checkMaster(xover int) error {
	if t.nMaster <= 0 || t.nMaster > 48 {
		return sbrTableErr("n_master %d", t.nMaster)
	}
	if xover >= t.nMaster {
		return sbrTableErr("crossover band %d, n_master %d", xover, t.nMaster)
	}
	return nil
}

// makeDerived builds the high, low and noise tables, the patches and the
// limiter table from the master table.
func (t *sbrTables) makeDerived(sp sbrSpectrum, limiterBands int) error {
	xover := int(sp.xoverBand)
	t.n[1] = t.nMaster - xover
	t.n[0] = (t.n[1] + 1) >> 1
	copy(t.fHigh[:], t.fMaster[xover:xover+t.n[1]+1])
	t.m[1] = t.fHigh[t.n[1]] - t.fHigh[0]
	t.kx[1] = t.fHigh[0]
	if t.kx[1] > 32 {
		return sbrTableErr("start frequency border %d", t.kx[1])
	}

	t.fLow[0] = t.fHigh[0]
	odd := t.n[1] & 1
	for k := 1; k <= t.n[0]; k++ {
		t.fLow[k] = t.fHigh[2*k-odd]
	}
	t.nQ = max(1, int(math.RoundToEven(float64(sp.noiseBands)*math.Log2(float64(t.k[2])/float64(t.kx[1])))))
	if t.nQ > 5 {
		n := t.nQ
		t.nQ = 1
		return sbrTableErr("%d noise floor bands", n)
	}
	t.fNoise[0] = t.fLow[0]
	acc := 0
	for k := 1; k <= t.nQ; k++ {
		acc += (t.n[0] - acc) / (t.nQ + 1 - k)
		t.fNoise[k] = t.fLow[acc]
	}
	if err := t.makePatches(); err != nil {
		return err
	}
	t.makeLim(limiterBands)
	return nil
}

// makePatches lays out the patches of the high frequency generator: which
// subbands of the low band are copied up to fill the high band.
func (t *sbrTables) makePatches() error {
	lastK, lastMsb, sb := -1, -1, 0
	msb := t.k[0]
	usb := t.kx[1]
	goal := ((1000 << 11) + t.rate>>1) / t.rate
	t.numPatches = 0

	k := t.nMaster
	if goal < t.kx[1]+t.m[1] {
		for k = 0; t.fMaster[k] < goal; k++ {
		}
	}
	for {
		odd := 0
		if k == lastK && msb == lastMsb {
			return sbrTableErr("patch construction failed")
		}
		lastK, lastMsb = k, msb
		for i := k; ; {
			sb = t.fMaster[i]
			odd = (sb + t.k[0]) & 1
			i--
			if sb <= t.k[0]-1+msb-odd {
				break
			}
			if i < 0 {
				return sbrTableErr("patch construction ran below the master table")
			}
		}
		if t.numPatches > 5 {
			return sbrTableErr("%d patches", t.numPatches)
		}
		t.patchNum[t.numPatches] = max(sb-usb, 0)
		t.patchStart[t.numPatches] = t.k[0] - odd - t.patchNum[t.numPatches]
		if t.patchNum[t.numPatches] > 0 {
			usb, msb = sb, sb
			t.numPatches++
		} else {
			msb = t.kx[1]
		}
		if t.fMaster[k]-sb < 3 {
			k = t.nMaster
		}
		if sb == t.kx[1]+t.m[1] {
			break
		}
	}
	if t.numPatches > 1 && t.patchNum[t.numPatches-1] < 3 {
		t.numPatches--
	}
	if t.numPatches == 0 {
		return sbrTableErr("no patch")
	}
	return nil
}

// makeLim builds the limiter band table from the low table and the patch
// borders, for bs_limiter_bands = limiterBands (0 to 3).
func (t *sbrTables) makeLim(limiterBands int) {
	if limiterBands == 0 {
		t.fLim[0] = t.fLow[0]
		t.fLim[1] = t.fLow[t.n[0]]
		t.nLim = 1
		return
	}
	warp := sbrLimWarp[limiterBands-1]
	var borders [sbrMaxPatch + 1]int
	borders[0] = t.kx[1]
	for k := 1; k <= t.numPatches; k++ {
		borders[k] = borders[k-1] + t.patchNum[k-1]
	}
	isBorder := func(v int) bool {
		return slices.Contains(borders[:t.numPatches+1], v)
	}
	copy(t.fLim[:], t.fLow[:t.n[0]+1])
	if t.numPatches > 1 {
		copy(t.fLim[t.n[0]+1:], borders[1:t.numPatches])
	}
	slices.Sort(t.fLim[:t.numPatches+t.n[0]])
	t.nLim = t.n[0] + t.numPatches - 1
	in, out := 1, 0
	for out < t.nLim {
		switch {
		case float64(t.fLim[in]) >= float64(t.fLim[out])*warp:
			out++
			t.fLim[out] = t.fLim[in]
			in++
		case t.fLim[in] == t.fLim[out] || !isBorder(t.fLim[in]):
			in++
			t.nLim--
		case !isBorder(t.fLim[out]):
			t.fLim[out] = t.fLim[in]
			in++
			t.nLim--
		default:
			out++
			t.fLim[out] = t.fLim[in]
			in++
		}
	}
}
