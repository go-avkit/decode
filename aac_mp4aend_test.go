// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// TestMP4AEnd: which packets an MP4 AAC track decodes and how many samples are cut from
// the end of the last, in the shapes the real files have (numbers taken from them: track
// timescale 44100, movie timescale 1000, packets of 2048 and a last one of 1922; and a
// timescale at the core rate, packets of 1024 whose durations do not add up to whole
// frames), against ffmpeg's demuxer.
func TestMP4AEnd(t *testing.T) {
	packets := func(n int, ts, last uint32) []container.Sample {
		s := make([]container.Sample, n)
		for i := range s {
			s[i].Duration = ts
		}
		s[n-1].Duration = last
		return s
	}
	edits := func(e ...container.Edit) *container.EditList {
		return &container.EditList{MovieTimescale: 1000, Edits: e}
	}
	tc := func(ts uint32, el *container.EditList) container.TrackConfig {
		return container.TrackConfig{Codec: "mp4a", Timescale: ts, EditList: el}
	}
	// a track at the core rate, one packet a duration short in the middle
	irregular := packets(262, 1024, 1024)
	irregular[100].Duration = 1023
	for _, c := range []struct {
		name     string
		tc       container.TrackConfig
		samples  []container.Sample
		mediaDur uint64
		r, frame int
		packets  int
		pad      int64
	}{
		{"no edit list: a short last packet is cut", tc(44100, nil), packets(10, 2048, 1922), 0, 44100, 2048, 10, 126},
		{"no edit list: the media header is less", tc(44100, nil), packets(10, 2048, 2048), 20000, 44100, 2048, 10, 480},
		{"no information", tc(44100, nil), nil, 0, 44100, 2048, 0, 0},
		// a start of 5058, a duration of 52103 ms (2297742) over 1122 packets whose last
		// has 1922 and a media duration of 2297730: the packets end first
		{"the end of the packets", tc(44100, edits(container.Edit{MediaTime: 5058, SegmentDuration: 52103})), packets(1122, 2048, 1922), 2297730, 44100, 2048, 1122, 126},
		// the edit list ends first (123691 ms is 5454773), 11 samples into the last packet
		{"the edit list's end", tc(44100, edits(container.Edit{MediaTime: 5058, SegmentDuration: 123691})), packets(2666, 2048, 1922), 5454784, 44100, 2048, 2666, 137},
		// an empty edit first: its 66 ms count in the track's duration, the packets are kept
		// whole up to the one that reaches the end of the non-empty edit
		{"an empty edit first", tc(44100, edits(container.Edit{MediaTime: container.EmptyEdit, SegmentDuration: 66}, container.Edit{MediaTime: 0, SegmentDuration: 11310})), packets(244, 2048, 2048), 498750, 44100, 2048, 244, 0},
		// the same, ending inside a packet: 5000 ms is 220500, and the packet that reaches it
		// ends at 108 * 2048 = 221184; the stream lasts 66 ms + 5000 ms = 223411, and the last
		// packet starts at 107 * 2048 + 2911 on its timeline, which leaves it 1364 of 2048
		{"a packet kept whole", tc(44100, edits(container.Edit{MediaTime: container.EmptyEdit, SegmentDuration: 66}, container.Edit{MediaTime: 0, SegmentDuration: 5000})), packets(244, 2048, 2048), 499712, 44100, 2048, 108, 684},
		// an edit that lasts to the end of the media states no end
		{"an open edit", tc(44100, edits(container.Edit{MediaTime: 1000, SegmentDuration: 0})), packets(10, 2048, 2048), 0, 44100, 2048, 10, 0},
		// at twice the track's rate: every figure doubles
		{"at twice the rate", tc(44100, nil), packets(10, 2048, 1922), 0, 88200, 4096, 10, 252},
		// a codec that does not say its frame: the last packet's duration is the span
		{"no frame size", tc(44100, nil), packets(10, 2048, 1922), 0, 44100, 0, 10, 0},
		// a track whose timescale is the core rate (22050, packets of 1024 that decode to
		// 2048), 262 of them 1 short in all, with the media header saying so and an edit
		// list of 12168 ms (268304) that is longer: ffmpeg gives 262 whole frames
		{"core-rate timescale with an edit list", tc(22050, edits(container.Edit{MediaTime: 0, SegmentDuration: 12168})), irregular, 268287, 44100, 2048, 262, 0},
		{"core-rate timescale, no edit list", tc(22050, nil), irregular, 268287, 44100, 2048, 262, 0},
		// the stream ends at or before the last packet's start: nothing is cut
		{"a last packet past the end", tc(44100, nil), packets(10, 2048, 2048), 18000, 44100, 2048, 10, 0},
	} {
		n, pad := mp4aEnd(c.tc, c.samples, c.mediaDur, c.r, c.frame)
		if n != c.packets || pad != c.pad {
			t.Errorf("%s: %d packets, %d cut; want %d, %d", c.name, n, pad, c.packets, c.pad)
		}
	}
	// the media duration of a track the file does not have is 0
	b, err := os.ReadFile(filepath.Join("testdata", "aac", "imp-he44-st.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	rd, err := container.NewReader(b)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := FirstAudioTrack(rd)
	if mediaDuration(rd, id) == 0 || mediaDuration(rd, id+100) != 0 {
		t.Errorf("media durations %d, %d", mediaDuration(rd, id), mediaDuration(rd, id+100))
	}
}

// TestTrimTail: samples are cut from the end across buffers, whole ones going away.
func TestTrimTail(t *testing.T) {
	mk := func(n int) Audio {
		return Audio{Format: SampleF32, SampleRate: 44100, Channels: 2, Data: make([]byte, n*2*4)}
	}
	out := trimTail([]Audio{mk(10), mk(4), mk(3)}, 9)
	if len(out) != 1 || out[0].Samples() != 8 {
		t.Fatalf("after cutting 9 of 17: %d buffers", len(out))
	}
	if out := trimTail([]Audio{mk(5)}, 0); len(out) != 1 || out[0].Samples() != 5 {
		t.Fatalf("cutting nothing: %+v", out)
	}
	if out := trimTail([]Audio{mk(5)}, 50); len(out) != 0 {
		t.Fatalf("cutting more than there is: %d buffers", len(out))
	}
}

// fakeCodec is a codec that does not say its frame size.
type fakeCodec struct{ audioCodec }

// TestCodecFrame: AAC says its frame, in samples of its output rate; a codec that does not,
// 0.
func TestCodecFrame(t *testing.T) {
	if codecFrame(fakeCodec{}) != 0 {
		t.Fatal("a codec without a frame size")
	}
	fx := aacHEFixtures[0]
	c, err := newAAC(aacHETrack(fx, "implicit"), AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if f := codecFrame(c); f != 1024 {
		t.Fatalf("before SBR: %d", f)
	}
	_, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", fx.name))
	if _, err := c.decode(fr[0]); err != nil {
		t.Fatal(err)
	}
	if f := codecFrame(c); f != 2048 {
		t.Fatalf("with SBR: %d", f)
	}
}

// TestAACCoreTimescaleLength: an HE-AAC track whose timescale is the core rate has packets of
// 1024 that decode to 2048. ffmpeg gives every packet whole whatever the durations add up to
// (real files: 262 packets lasting 268287 in all give 262 * 2048 samples, where this decoder
// gave 2 fewer), and cuts only a last packet that states less than its frame.
func TestAACCoreTimescaleLength(t *testing.T) {
	fx := aacHEFixtures[0]
	_, fr := aacADTSFrames(t, filepath.Join("testdata", "aac", fx.name))
	n := min(len(fr), 40)
	for _, c := range []struct {
		name  string
		last  uint32
		short int // a duration short in the middle
		want  int
	}{{"whole", 1024, 1, n * 2048}, {"a short last packet", 1000, 0, n*2048 - 48}} {
		tc := aacHETrack(fx, "explicit")
		tc.Timescale = uint32(aacRates[fx.rate])
		samples := make([]container.Sample, n)
		var sum uint64
		for i := range samples {
			samples[i] = container.Sample{Data: fr[i], Duration: 1024}
		}
		samples[n-1].Duration = c.last
		samples[n/2].Duration -= uint32(c.short)
		for _, s := range samples {
			sum += uint64(s.Duration)
		}
		bufs, err := decodeAudioDur(tc, samples, AudioConfig{}, sum)
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		for _, a := range bufs {
			got += a.Samples()
		}
		if got != c.want {
			t.Errorf("%s: %d samples, want %d", c.name, got, c.want)
		}
	}
}
