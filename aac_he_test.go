// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// aacHEFixture is a real HE-AAC stream: its core rate and channel configuration, the
// extension rate index of its explicit signalling, and whether ffmpeg's decoded output
// of it is comparable (a PS stream's is stereo).
type aacHEFixture struct {
	name                  string
	rate, config, extRate int
	ps                    bool
	ffmpeg                bool
}

var aacHEFixtures = []aacHEFixture{
	{"he44-st.aac", 7, 2, 4, false, true},
	{"he32-st.aac", 8, 2, 5, false, true},
	{"he48-mono.aac", 6, 1, 3, false, true},
	{"he44-ps.aac", 7, 1, 4, true, false},
}

// aacHETrack is the track of a fixture, with the signalling asked for: explicit
// (object type 5 or 29), backward compatible, or none (implicit).
func aacHETrack(fx aacHEFixture, signalling string) container.TrackConfig {
	tc := aacTrack(fx.rate, fx.config)
	switch signalling {
	case "explicit":
		tc.CodecConfig = ascHE(fx.ps, fx.rate, fx.config, fx.extRate, 2)
	case "backward compatible":
		tc.CodecConfig = ascBackward(fx.rate, fx.config, fx.extRate, fx.ps)
	}
	return tc
}

// aacHEDecode decodes the first frames of a fixture through the decoder and returns
// each channel's samples, the output rate and the samples per frame of each frame.
func aacHEDecode(t testing.TB, fx aacHEFixture, signalling string, frames int) (ch [][]float32, rate int, per []int) {
	t.Helper()
	_, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", fx.name))
	c, err := newAAC(aacHETrack(fx, signalling), AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	n := aacChannelConfig(fx.config)
	ch = make([][]float32, n)
	for i := 0; i < min(frames, len(fr)); i++ {
		out, err := c.decode(fr[i])
		if err != nil {
			t.Fatalf("%s frame %d: %v", fx.name, i, err)
		}
		a := out[0]
		rate = a.SampleRate
		per = append(per, a.Samples())
		for s := 0; s < a.Samples(); s++ {
			for k := 0; k < n; k++ {
				ch[k] = append(ch[k], math.Float32frombits(binary.LittleEndian.Uint32(a.Data[(s*n+k)*4:])))
			}
		}
	}
	return ch, rate, per
}

// TestAACDecodeHE: real HE-AAC streams decode at twice the core rate, whichever way the
// AudioSpecificConfig signals SBR (explicit, backward compatible, or not at all: the
// first SBR data then turns it on), 2048 samples per frame from the first; the output
// agrees with the exact float64 path to within a float32 rounding plus 1e-6 of the
// peak, and with ffmpeg 9.0.2's (macOS clang and linux gcc) to 3e-6 sample by sample.
// A PS stream decodes as its SBR mono channel (ffmpeg's is stereo: not compared).
func TestAACDecodeHE(t *testing.T) {
	for _, fx := range aacHEFixtures {
		frames := sbrRefFrames(16, 6)
		_, want := sbrAdjRun(t, fx.name, frames)
		for _, sig := range []string{"explicit", "backward compatible", "implicit"} {
			if sig == "backward compatible" && fx.ps {
				continue // the PS stream is signalled by object type 29
			}
			got, rate, per := aacHEDecode(t, fx, sig, frames)
			if rate != 2*aacRates[fx.rate] {
				t.Errorf("%s, %s: output at %d Hz", fx.name, sig, rate)
			}
			for i, p := range per {
				if p != 2048 {
					t.Fatalf("%s, %s: frame %d has %d samples", fx.name, sig, i, p)
				}
			}
			for c := range got {
				e, peak := sbrBeyondRounding(got[c], want[c])
				if e.max > 1e-6*peak {
					t.Errorf("%s, %s, channel %d: %.2e beyond a float32 rounding, peak %.3f", fx.name, sig, c, e.max, peak)
				}
			}
			if !fx.ffmpeg || sig != "explicit" {
				continue
			}
			for _, dir := range []string{"ref", "ref-linux"} {
				all := readF32(t, filepath.Join("testdata", "aac", dir, fx.name[:len(fx.name)-4]+".f32"))
				for c := range got {
					var worst float64
					for i := 0; i < min(len(got[c]), len(all)/2); i++ {
						worst = math.Max(worst, math.Abs(float64(got[c][i])-float64(all[2*i+c])))
					}
					t.Logf("%s against ffmpeg (%s), channel %d: largest difference %.2e", fx.name, dir, c, worst)
					if worst > 3e-6 {
						t.Errorf("%s: %s differs from the decoder by %.2e on channel %d", fx.name, dir, worst, c)
					}
				}
			}
		}
	}
}

// TestAACDecodeHEFirstFrames: a stream that signals nothing is decoded at the core
// rate until its first SBR data, then at twice the rate: an LC frame, then an SBR one.
func TestAACDecodeHEFirstFrames(t *testing.T) {
	fx := aacHEFixtures[0]
	_, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", fx.name))
	c, err := newAAC(aacHETrack(fx, "implicit"), AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	d := c.(*aacDecoder)
	if d.sampleRate() != 22050 {
		t.Fatalf("rate %d before SBR", d.sampleRate())
	}
	out, err := c.decode(silentFrame(2))
	if err != nil || out[0].Samples() != 1024 || out[0].SampleRate != 22050 {
		t.Fatalf("a frame without SBR data: %v %+v", err, out)
	}
	out, err = c.decode(fr[0])
	if err != nil || out[0].Samples() != 2048 || out[0].SampleRate != 44100 || d.sampleRate() != 44100 {
		t.Fatalf("the first frame with SBR data: %v %d samples at %d", err, out[0].Samples(), out[0].SampleRate)
	}
	// once on, a frame without SBR data is upsampled
	out, err = c.decode(silentFrame(2))
	if err != nil || out[0].Samples() != 2048 || out[0].SampleRate != 44100 {
		t.Fatalf("a frame without SBR data after: %v %d samples", err, out[0].Samples())
	}
	// a stream whose elements carry more distinct instance tags than a frame holds
	// elements runs out of SBR states, and says so
	c, _ = newAAC(aacHETrack(fx, "explicit"), AudioConfig{})
	d = c.(*aacDecoder)
	for i := 0; i < aacMaxElements; i++ {
		d.parser.sbrStates = append(d.parser.sbrStates, sbrEntry{id: aacIDSCE, tag: uint8(100 + i)})
	}
	if _, err = c.decode(fr[0]); err == nil || !errors.Is(err, ErrAudioSample) {
		t.Fatalf("more SBR states than elements: %v", err)
	}
	// the block limit is checked when SBR turns on
	c, _ = newAAC(aacHETrack(fx, "implicit"), AudioConfig{MaxBlockSamples: 1024})
	if _, err = c.decode(fr[0]); err == nil {
		t.Fatal("SBR frames over the block limit")
	}
}

// TestAACDecodeHEImplicitEditList: an MP4 made with `ffmpeg -c copy` from an HE-AAC
// ADTS stream says AAC-LC at the core rate (SBR is implicit) and carries an edit list.
// The rate is known only when the first frame turns SBR on, so the start and the
// length the edit list states are converted then: the track has the samples ffmpeg
// gives (twice the core's frames' worth, minus the priming), equal to 2e-5.
func TestAACDecodeHEImplicitEditList(t *testing.T) {
	for _, tc := range []struct {
		name    string
		samples int
	}{{"imp-he44-st", 94208}, {"imp-he32-st", 69632}} {
		b, err := os.ReadFile(filepath.Join("testdata", "aac", tc.name+".mp4"))
		if err != nil {
			t.Fatal(err)
		}
		r, err := container.NewReader(b)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := FirstAudioTrack(r)
		bufs, err := DecodeAudioTrack(r, id, AudioConfig{})
		if err != nil {
			t.Fatal(err)
		}
		ref := readF32(t, filepath.Join("testdata", "aac", "ref", tc.name+".f32"))
		var got []float32
		n := 0
		for _, a := range bufs {
			n += a.Samples()
			for i := 0; i+4 <= len(a.Data); i += 4 {
				got = append(got, math.Float32frombits(binary.LittleEndian.Uint32(a.Data[i:])))
			}
		}
		if n != tc.samples || len(got) != len(ref) {
			t.Errorf("%s: %d samples (%d values), ffmpeg gives %d (%d)", tc.name, n, len(got), tc.samples, len(ref))
			continue
		}
		var worst float64
		for i, v := range got {
			worst = math.Max(worst, math.Abs(float64(v)-float64(ref[i])))
		}
		t.Logf("%s: %d samples, largest difference to ffmpeg %.2e", tc.name, n, worst)
		// over the whole track ffmpeg's float32 arithmetic drifts further than over
		// the first 16 frames of TestAACDecodeHE (1.0e-5 on he32-st, 1.2e-6 on he44-st)
		if worst > 2e-5 {
			t.Errorf("%s: differs from ffmpeg by %.2e", tc.name, worst)
		}
	}
}

// TestAACDecodeHERateChange: when SBR turns on after audio has been placed (an LC frame
// first, as a stream that signals nothing may do), what remains of the start to drop and
// the position follow the new rate: 3000 samples of start at the core rate are 1976
// after the first frame, which are 3952 at twice the rate, dropped over two frames.
func TestAACDecodeHERateChange(t *testing.T) {
	fx := aacHEFixtures[0]
	_, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", fx.name))
	tc := aacHETrack(fx, "implicit")
	tc.Timescale = 22050
	tc.EditList = &container.EditList{MovieTimescale: 22050, Edits: []container.Edit{{MediaTime: 3000}}}
	d, err := NewAudioTrack(tc, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for i, frame := range [][]byte{silentFrame(2), fr[0], fr[1]} {
		out, err := d.DecodeSample(container.Sample{Data: frame})
		if err != nil {
			t.Fatal(err)
		}
		switch i {
		case 0, 1:
			if len(out) != 0 {
				t.Fatalf("frame %d is still inside the start: %+v", i, out)
			}
		case 2:
			if len(out) != 1 || out[0].Samples() != 2048-1904 || out[0].SampleRate != 44100 || out[0].PTS != 0 {
				t.Fatalf("frame 2: %d buffers, first %d samples at %d Hz, PTS %d", len(out), out[0].Samples(), out[0].SampleRate, out[0].PTS)
			}
		}
	}
}
