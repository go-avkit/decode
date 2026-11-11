// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"
	"path/filepath"
	"testing"
)

// The noise generator of perceptual noise substitution is not normative, so no
// stream can ask for its exact samples; what a stream defines is the energy of each
// noise band (from its scalefactor) and, between the two channels of a pair, that
// the noise is independent unless the bitstream says otherwise. TestAACNoiseBands
// checks both on every noise band of the fixture that uses PNS:
//
//   - the energy of the generated band equals the energy the scalefactor states,
//     2^(sf/2), within what summing width float32 squares can lose (4 * width * 2^-24
//     relative);
//   - in the bands both channels of a pair substitute, with no M/S flag, the
//     normalised correlation of the two noise vectors is that of independent noise:
//     on average no more than 2/sqrt(width) in absolute value.
func TestAACNoiseBands(t *testing.T) {
	cfg, frames := aacADTSFrames(t, filepath.Join("testdata", "aac", "st44-tools.aac"))
	p := newTestParser(t, cfg.rateIndex, cfg.channels)
	state := uint32(aacNoiseSeed)
	var bands, pairs int
	var worst, corrSum, corrBound float64
	for fi, f := range frames {
		els, err := p.parseFrame(f)
		if err != nil {
			t.Fatal(err)
		}
		for e := range els {
			el := &els[e]
			var out [2][aacFrameLen]float32
			for c := 0; c < int(el.nch); c++ {
				el.ch[c].dequantise(&out[c], &state)
			}
			for c := 0; c < int(el.nch); c++ {
				ch := &el.ch[c]
				ics := &ch.ics
				w0 := 0
				for g := 0; g < int(ics.numGroups); g++ {
					for sfb := 0; sfb < int(ics.maxSfb); sfb++ {
						if ch.cb[g][sfb] != aacBandNoise {
							continue
						}
						lo, hi := int(ics.swb[sfb]), int(ics.swb[sfb+1])
						want := math.Exp2(float64(min(max(int(ch.sf[g][sfb]), -100), 155)) / 2)
						for k := 0; k < int(ics.groupLen[g]); k++ {
							base := (w0 + k) * aacShortLen
							var energy float64
							for i := base + lo; i < base+hi; i++ {
								energy += float64(out[c][i]) * float64(out[c][i])
							}
							rel := math.Abs(energy-want) / want
							worst = math.Max(worst, rel/(4*float64(hi-lo)*0x1p-24))
							if rel > 4*float64(hi-lo)*0x1p-24 {
								t.Fatalf("frame %d band %d: energy %g, the scalefactor states %g (relative error %g)", fi, sfb, energy, want, rel)
							}
							bands++
							if c == 1 && el.ch[0].cb[g][sfb] == aacBandNoise && !el.msUsed[g][sfb] && hi-lo >= 16 {
								var xy, xx, yy float64
								for i := base + lo; i < base+hi; i++ {
									xy += float64(out[0][i]) * float64(out[1][i])
									xx += float64(out[0][i]) * float64(out[0][i])
									yy += float64(out[1][i]) * float64(out[1][i])
								}
								corrSum += math.Abs(xy / math.Sqrt(xx*yy))
								corrBound += 2 / math.Sqrt(float64(hi-lo))
								pairs++
							}
						}
					}
					w0 += int(ics.groupLen[g])
				}
			}
		}
	}
	t.Logf("%d noise bands: the energy error is at most %.2f of the allowed 4*width*2^-24; %d band pairs, mean |correlation| %.3f, allowed %.3f", bands, worst, pairs, corrSum/float64(max(pairs, 1)), corrBound/float64(max(pairs, 1)))
	if bands == 0 || pairs == 0 {
		t.Fatalf("the fixture has %d noise bands and %d noise pairs", bands, pairs)
	}
	if corrSum > corrBound {
		t.Errorf("mean |correlation| %.3f above %.3f: the channels' noise is not independent", corrSum/float64(pairs), corrBound/float64(pairs))
	}
}
