// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// psSignalling says how a mono track's AudioSpecificConfig signals SBR and PS.
func psSignalling(cc []byte, ac aacConfig) string {
	obj := aacObjectType(newAACBits(cc))
	switch {
	case obj == aacObjPS:
		return "explicit, object type 29 (PS)"
	case obj == aacObjSBR && ac.psOff:
		return "explicit, object type 5, PS stated absent"
	case obj == aacObjSBR:
		return "explicit, object type 5, PS not stated"
	case ac.sbr && ac.ps:
		return "backward compatible, PS present"
	case ac.sbr && ac.psOff:
		return "backward compatible, PS stated absent"
	case ac.sbr:
		return "backward compatible, PS not stated"
	}
	return "implicit (AAC-LC, no SBR signal)"
}

// psCensus counts what the PS data of a library holds: by file for what a file does
// at least once, by frame for the totals. Numbers only, never a name.
type psCensus struct {
	files  map[string]int
	frames map[string]int
}

func (c *psCensus) file(k string, a ...any)  { c.files[fmt.Sprintf(k, a...)]++ }
func (c *psCensus) frame(k string, a ...any) { c.frames[fmt.Sprintf(k, a...)]++ }

// psCensusFile parses every frame of one track's PS data. A frame is parsed even
// where the channel policy would not (a stream that signals nothing), to count them.
// It checks the oracle of the parser: every frame's data is consumed to within the
// fill of its extension.
func psCensusFile(t *testing.T, c *psCensus, r *container.Reader, id uint32, tc container.TrackConfig, ac aacConfig) {
	samples, err := r.Samples(id)
	if err != nil {
		return
	}
	dec, err := newAAC(tc, AudioConfig{})
	if err != nil {
		return
	}
	d := dec.(*aacDecoder)
	d.parser.cfg.sbr, d.parser.cfg.psOff = true, false // parse the PS data whatever the signal says
	var (
		sbrFrames, psFrames, noPS, errs, hdrs, ipd, ipdStale, extra, first int
		iid, icc                                                           = map[int]bool{}, map[int]bool{}
		classes, envs                                                      = map[int]bool{}, map[int]bool{}
		was34, any34, is34Switch, extFlag, quantFine                       bool
		started                                                            bool
	)
	for i, s := range samples {
		els, err := d.parser.parseFrame(s.Data)
		if err != nil {
			errs++
			continue
		}
		for _, e := range els {
			if e.id != aacIDSCE {
				continue
			}
			st := d.parser.sbrOf(e.id, e.tag)
			if !st.started {
				continue
			}
			sbrFrames++
			p := &st.ps
			if !st.psSeen {
				noPS++
				continue
			}
			psFrames++
			if i == 0 {
				first++
			}
			if !p.start && !p.hdr {
				c.frame("PS frame before any valid header, or after a fault")
				continue
			}
			if p.slack < 0 || p.slack > 7 {
				t.Errorf("frame %d: the data leaves %d bits before the end of its extension", i, p.slack)
			}
			if p.hdr {
				hdrs++
				started = true
				if p.enableIID {
					iid[p.iidMode] = true
					quantFine = quantFine || p.iidQuant
				}
				if p.enableICC {
					icc[p.iccMode] = true
				}
				if p.enableExt {
					extFlag = true
				}
			}
			if started && p.is34 != was34 {
				is34Switch = true
			}
			was34 = p.is34
			any34 = any34 || p.is34
			classes[int(p.frameClass)] = true
			n := p.numEnv
			if p.extraEnvelop {
				n--
				extra++
			}
			envs[n] = true
			c.frame("frame class %d, %d envelopes", p.frameClass, n)
			switch {
			case p.ipdopdSent:
				ipd++
			case p.enableIPDOPD:
				ipdStale++
			}
		}
	}
	if sbrFrames == 0 {
		return
	}
	keys := func(m map[int]bool) string { return fmt.Sprint(slices.Sorted(maps.Keys(m))) }
	c.file("signalling: %s", psSignalling(tc.CodecConfig, ac))
	if psFrames == 0 {
		c.file("SBR, no PS data in any frame")
		return
	}
	c.file("carries PS data")
	c.file("frames with PS data: all of the SBR frames: %v", noPS == 0)
	c.file("IID modes %s (enabled in a header)", keys(iid))
	c.file("ICC modes %s (enabled in a header)", keys(icc))
	c.file("IID in 31 steps (fine): %v", quantFine)
	c.file("34 bands in force at some frame: %v; switches between 20 and 34 bands: %v", any34, is34Switch)
	c.file("frame classes %s", keys(classes))
	c.file("envelope counts of a frame %s", keys(envs))
	c.file("IPD/OPD sent in at least one frame: %v", ipd > 0)
	c.file("frames that hold IPD/OPD of an earlier frame without sending them: %v", ipdStale > 0)
	c.file("extension (enable_ext) in a header: %v", extFlag)
	c.file("a closing envelope added at a border short of the end: %v", extra > 0)
	c.file("PS data in the first frame: %v; header in the first PS frame: %v", first > 0, hdrs > 0)
	c.file("frames the parser refused (PS off until the next header): %v", errs > 0)
	c.frames["SBR frames"] += sbrFrames
	c.frames["PS frames"] += psFrames
	c.frames["PS frames with a header"] += hdrs
	c.frames["PS frames with IPD/OPD"] += ipd
}

// TestAACPSCensus counts the PS configurations of the HE-AAC files under
// AAC_REAL_HE_DIR (the same files as TestAACRealHE), by file and by frame, to say
// which paths of PS2 and PS3 the library exercises. It logs numbers only. Skipped
// without the directory.
func TestAACPSCensus(t *testing.T) {
	dir := os.Getenv("AAC_REAL_HE_DIR")
	if dir == "" {
		t.Skip("AAC_REAL_HE_DIR is not set")
	}
	c := &psCensus{files: map[string]int{}, frames: map[string]int{}}
	n := 0
	aacRealHEFiles(t, dir, func(path string, b []byte, r *container.Reader, id uint32, tc container.TrackConfig) {
		ac, err := parseAudioSpecificConfig(tc.CodecConfig)
		if err != nil || ac.channels != 1 {
			return
		}
		n++
		psCensusFile(t, c, r, id, tc, ac)
	})
	t.Logf("%d mono AAC files", n)
	for _, k := range slices.Sorted(maps.Keys(c.files)) {
		t.Logf("files  %4d  %s", c.files[k], k)
	}
	for _, k := range slices.Sorted(maps.Keys(c.frames)) {
		t.Logf("frames %9d  %s", c.frames[k], k)
	}
}
