// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// The files of testdata/gapless are one fixture each, made by Apple's afconvert from 0.3 s of
// audio (macOS 27): lc.m4a with `-d aac -b 64000` (AAC-LC, 32000 Hz, 12 packets of 1024) and
// he.m4a with `-d aach -b 32000` (HE-AAC, timescale 16000, output 32000 Hz, 8 packets of 1024
// ticks, 2048 output samples each). Each has no edit list and an iTunSMPB:
//
//	lc.m4a  delay 2112, padding 576, samples 9600 ticks    (12288 ticks of media)
//	he.m4a  delay 2112, padding 1280, samples 4800 ticks   (8192 ticks of media)
//
// The numbers are ticks of the track's timescale: for HE-AAC they are core-rate samples, and
// ffmpeg doubles them as it does a time (libavformat/mov.c 5588, n9.0.2).
//
// gaplessVariant makes the other shapes from them -- an edit list added, the iTunSMPB
// changed or removed -- by rewriting boxes in place, and the table below says what ffmpeg
// 9.0.2 delivers for each: the length and where in the whole decode it starts. The table is
// recorded, not computed: GAPLESS_RECORD=<directory> runs ffmpeg on every variant and logs
// the lines to paste (the directory is where the variants and ffmpeg's output are kept).
// The samples are compared with ffmpeg's decode of the file with neither iTunSMPB nor edit
// list, testdata/gapless/*.f32 (`ffmpeg -i plain.m4a -f f32le`).

// smpbText formats the three numbers of an iTunSMPB.
func smpbText(delay, padding, samples uint64) string {
	return fmt.Sprintf("%08X %08X %016X", delay, padding, samples)
}

const (
	smpbKeep   = ""  // leave the iTunSMPB as it is
	smpbRemove = "x" // rename it so that nothing reads it
	noEdit     = -1  // no edit list
)

// gaplessVariant rewrites a copy of one of the fixtures: with an edit list of one edit,
// media time mediaTime and duration dur (both in ticks of the track's timescale), unless
// mediaTime is noEdit; and with its iTunSMPB numbers set to smpb, left (smpbKeep) or
// renamed (smpbRemove). The edit list takes the room of the file's free box, so that the
// chunk offsets stay right.
func gaplessVariant(t testing.TB, base []byte, mediaTime, dur int64, smpb string) []byte {
	t.Helper()
	b := append([]byte(nil), base...)
	find := func(lo, hi int, name string) (int, int) {
		for p := lo; p+8 <= hi; {
			n := int(binary.BigEndian.Uint32(b[p:]))
			if n < 8 || p+n > hi {
				t.Fatalf("bad box size %d at %d", n, p)
			}
			if string(b[p+4:p+8]) == name {
				return p, n
			}
			p += n
		}
		t.Fatalf("no %s box", name)
		return 0, 0
	}
	const key = "iTunSMPB"
	i := strings.Index(string(b), key)
	if i < 0 {
		t.Fatal("no iTunSMPB in the fixture")
	}
	switch smpb {
	case smpbKeep:
	case smpbRemove:
		copy(b[i:], "iTunSMPX")
	default:
		j := strings.Index(string(b[i:]), " 00000000 ")
		if j < 0 || len(smpb) != 34 {
			t.Fatalf("iTunSMPB text not found or %q is not three numbers", smpb)
		}
		copy(b[i+j+10:], smpb)
	}
	if mediaTime == noEdit {
		return b
	}
	moov, moovSize := find(0, len(b), "moov")
	mvhd, _ := find(moov+8, moov+moovSize, "mvhd")
	trak, trakSize := find(moov+8, moov+moovSize, "trak")
	mdia, mdiaSize := find(trak+8, trak+trakSize, "mdia")
	mdhd, _ := find(mdia+8, mdia+mdiaSize, "mdhd")
	movieScale := int64(binary.BigEndian.Uint32(b[mvhd+20:]))
	trackScale := int64(binary.BigEndian.Uint32(b[mdhd+20:]))
	edts := make([]byte, 36)
	binary.BigEndian.PutUint32(edts, 36)
	copy(edts[4:], "edts")
	binary.BigEndian.PutUint32(edts[8:], 28)
	copy(edts[12:], "elst")
	binary.BigEndian.PutUint32(edts[20:], 1) // one entry
	binary.BigEndian.PutUint32(edts[24:], uint32(dur*movieScale/trackScale))
	binary.BigEndian.PutUint32(edts[28:], uint32(mediaTime))
	binary.BigEndian.PutUint32(edts[32:], 1<<16)
	free, freeSize := find(moov+moovSize, len(b), "free")
	if freeSize < 8+len(edts) {
		t.Fatalf("the free box has no room for an edit list: %d bytes", freeSize)
	}
	end := trak + trakSize
	out := append(append(append([]byte(nil), b[:end]...), edts...), b[end:free]...)
	binary.BigEndian.PutUint32(out[trak:], uint32(trakSize+len(edts)))
	binary.BigEndian.PutUint32(out[moov:], uint32(moovSize+len(edts)))
	out = binary.BigEndian.AppendUint32(out, uint32(freeSize-len(edts)))
	out = append(out, "free"...)
	out = append(out, b[free+8+len(edts):]...)
	if len(out) != len(b) {
		t.Fatalf("variant is %d bytes, the fixture %d", len(out), len(b))
	}
	return out
}

// gaplessCase is one variant and what ffmpeg 9.0.2 delivers for it.
type gaplessCase struct {
	name       string
	file       string // lc.m4a or he.m4a
	mediaTime  int64  // of the edit list, or noEdit
	dur        int64  // of the edit list's edit
	smpb       string // smpbKeep, smpbRemove or numbers
	length     int    // samples per channel ffmpeg delivers
	start      int    // where they start in ffmpeg's whole decode
	startsMidd bool   // ffmpeg starts the decode after the start of the stream (the edit list drops packets)
}

// gaplessCases returns the table for one fixture, in terms of its numbers.
func gaplessCases(file string, delay, padding, samples, media uint64) []gaplessCase {
	d, p, s, m := int64(delay), int64(padding), int64(samples), int64(media)
	c := func(name string, mt, dur int64, text string) gaplessCase {
		return gaplessCase{name: name, file: file, mediaTime: mt, dur: dur, smpb: text}
	}
	return []gaplessCase{
		c("plain", noEdit, 0, smpbRemove),
		c("iTunSMPB only", noEdit, 0, smpbKeep),
		c("edit list only", d, s, smpbRemove),
		c("both, consistent", d, s, smpbKeep),
		c("both, delay below the packets dropped", d, s, smpbText(1000, uint64(p), samples)),
		c("both, delay above the packets dropped", d, s, smpbText(3000, uint64(p), samples)),
		c("both, edit list from 0", 0, s/2, smpbKeep),
		c("both, edit list from the fourth packet", 3136, m-3136, smpbKeep),
		c("both, padding beyond the edit list", d, s, smpbText(delay, padding+2048, samples)),
		c("padding beyond", noEdit, 0, smpbText(delay, padding+2048, samples)),
		c("samples as long as the media", noEdit, 0, smpbText(delay, padding, media)),
		c("delay beyond the limit", noEdit, 0, smpbText(16384, padding, samples)),
		c("delay zero", noEdit, 0, smpbText(0, padding, samples)),
		c("padding zero", noEdit, 0, smpbText(delay, 0, samples)),
	}
}

func TestGaplessRecord(t *testing.T) {
	dir := os.Getenv("GAPLESS_RECORD")
	if dir == "" {
		t.Skip("GAPLESS_RECORD=<directory> records what ffmpeg delivers for the variants")
	}
	for _, fx := range []struct {
		file                           string
		delay, padding, samples, media uint64
	}{{"lc.m4a", 2112, 576, 9600, 12288}, {"he.m4a", 2112, 1280, 4800, 8192}} {
		base, err := os.ReadFile(filepath.Join("testdata", "gapless", fx.file))
		if err != nil {
			t.Fatal(err)
		}
		cases := gaplessCases(fx.file, fx.delay, fx.padding, fx.samples, fx.media)
		var whole []float32
		for i, c := range cases {
			name := fmt.Sprintf("%s-%02d", strings.TrimSuffix(fx.file, ".m4a"), i)
			in, out := filepath.Join(dir, name+".m4a"), filepath.Join(dir, name+".f32")
			if err := os.WriteFile(in, gaplessVariant(t, base, c.mediaTime, c.dur, c.smpb), 0o644); err != nil {
				t.Fatal(err)
			}
			if b, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-y", "-i", in, "-f", "f32le", out).CombinedOutput(); err != nil {
				t.Fatalf("ffmpeg: %v\n%s", err, b)
			}
			got := readF32(t, out)
			if i == 0 {
				whole = got
				if err := os.WriteFile(filepath.Join("testdata", "gapless", name[:2]+".f32"), mustRead(t, out), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			n := len(got) / 2
			// where it ends in the whole decode: its last 1024 frames, which ffmpeg decodes with
			// the same history whatever it started from, match the whole decode's at one place
			w := min(n, 1024)
			start := -1
			for e := n; e <= len(whole)/2; e++ {
				ok := true
				for k := 0; k < 2*w && ok; k++ {
					ok = math.Abs(float64(got[2*n-2*w+k])-float64(whole[2*e-2*w+k])) < 1e-6
				}
				if ok {
					start = e - n
					break
				}
			}
			t.Logf("{%q, %d, %d}, // %s %q: mediaTime %d dur %d", c.name, n, start, fx.file, c.smpb, c.mediaTime, c.dur)
		}
	}
}

func mustRead(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// gaplessWant is what ffmpeg 9.0.2 delivers for the case of that name, recorded by
// TestGaplessRecord.
//
// gaplessOurs is what this package delivers where it is known
// to differ from ffmpeg (the doc of DecodeAudioTrack): an HE-AAC track whose timescale is the
// core rate and whose edit list starts after 0. The edit list's 2112 ticks are 4224 samples of
// the output, which is what is dropped; ffmpeg counts the part of the packet that holds the
// start in ticks, 64, against the 4096 samples of the two packets it dropped whole, and drops
// no more than they are. It is not an iTunSMPB matter -- it is the same without one.
type gaplessWant struct {
	name          string
	length, start int
}

// gaplessOurs is {length, start} for the cases named "file/case" where it is not ffmpeg's.
var gaplessOurs = map[string][2]int{"he.m4a/edit list only": {9600, 4224}}

var (
	lcWant = []gaplessWant{
		{"plain", 12288, 0},
		{"iTunSMPB only", 9600, 2112},
		{"edit list only", 9600, 2112},
		{"both, consistent", 9600, 2112},
		{"both, delay below the packets dropped", 9664, 2048},
		{"both, delay above the packets dropped", 8712, 3000},
		{"both, edit list from 0", 2688, 2112},
		{"both, edit list from the fourth packet", 9216, 3072},
		{"both, padding beyond the edit list", 9600, 2112},
		{"padding beyond", 7552, 2112},
		{"samples as long as the media", 10176, 2112},
		{"delay beyond the limit", 11712, 0},
		{"delay zero", 11712, 0},
		{"padding zero", 10176, 2112},
	}
	heWant = []gaplessWant{
		{"plain", 16384, 0},
		{"iTunSMPB only", 9600, 4224},
		{"edit list only", 9728, 4096},
		{"both, consistent", 9600, 4224},
		{"both, delay below the packets dropped", 9728, 4096},
		{"both, delay above the packets dropped", 7824, 6000},
		{"both, edit list from 0", 576, 4224},
		{"both, edit list from the fourth packet", 7680, 6144},
		{"both, padding beyond the edit list", 9600, 4224},
		{"padding beyond", 5504, 4224},
		{"samples as long as the media", 12160, 4224},
		{"delay beyond the limit", 13824, 0},
		{"delay zero", 13824, 0},
		{"padding zero", 12160, 4224},
	}
)

func TestGaplessMatchesFFmpeg(t *testing.T) {
	for _, fx := range []struct {
		file                           string
		delay, padding, samples, media uint64
		want                           []gaplessWant
	}{
		{"lc.m4a", 2112, 576, 9600, 12288, lcWant},
		{"he.m4a", 2112, 1280, 4800, 8192, heWant},
	} {
		base := mustRead(t, filepath.Join("testdata", "gapless", fx.file))
		whole := readF32(t, filepath.Join("testdata", "gapless", strings.TrimSuffix(fx.file, ".m4a")+".f32"))
		cases := gaplessCases(fx.file, fx.delay, fx.padding, fx.samples, fx.media)
		if len(cases) != len(fx.want) {
			t.Fatalf("%d cases, %d recorded", len(cases), len(fx.want))
		}
		for i, c := range cases {
			w := fx.want[i]
			if w.name != c.name {
				t.Fatalf("case %d is %q, recorded as %q", i, c.name, w.name)
			}
			t.Run(fx.file+"/"+c.name, func(t *testing.T) {
				r, id := audioReader(t, gaplessVariant(t, base, c.mediaTime, c.dur, c.smpb))
				bufs, err := DecodeAudioTrack(r, id, AudioConfig{})
				if err != nil {
					t.Fatal(err)
				}
				var got []float32
				for _, a := range bufs {
					if a.Format != SampleF32 || a.Channels != 2 {
						t.Fatalf("format %v, %d channels", a.Format, a.Channels)
					}
					for k := 0; k+4 <= len(a.Data); k += 4 {
						got = append(got, math.Float32frombits(binary.LittleEndian.Uint32(a.Data[k:])))
					}
				}
				length, start := w.length, w.start
				if o, ok := gaplessOurs[fx.file+"/"+c.name]; ok {
					length, start = o[0], o[1]
				}
				if n := len(got) / 2; n != length {
					t.Errorf("%d samples, want %d (ffmpeg delivers %d, starting at %d)", n, length, w.length, w.start)
				}
				// The samples: ffmpeg starts decoding at the first packet it keeps, with nothing
				// before it, so its first frame differs where an edit list has dropped packets;
				// from the next frame on, and always at the end, they are the whole decode's.
				from := 0
				if c.mediaTime > 0 {
					from = 2 * codecFrameOf(t, r, id)
				}
				var worst float64
				for k := from; k < len(got) && 2*start+k < len(whole); k++ {
					worst = max(worst, math.Abs(float64(got[k])-float64(whole[2*start+k])))
				}
				if worst > 3e-6 {
					t.Errorf("samples differ from ffmpeg's by %g", worst)
				}
			})
		}
	}
}

// codecFrameOf is the output samples of one packet of the track.
func codecFrameOf(t testing.TB, r *container.Reader, id uint32) int {
	t.Helper()
	tc, err := r.TrackConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewAudioTrack(tc, AudioConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	return max(codecFrame(d.codec), 1024)
}

// audioReader opens an MP4 and finds its audio track.
func audioReader(t testing.TB, data []byte) (*container.Reader, uint32) {
	t.Helper()
	r, err := container.NewReader(data)
	if err != nil {
		t.Fatal(err)
	}
	id, err := FirstAudioTrack(r)
	if err != nil {
		t.Fatal(err)
	}
	return r, id
}

func TestGaplessOfTheLastTrackOnly(t *testing.T) {
	r, id := audioReader(t, mustRead(t, filepath.Join("testdata", "gapless", "lc.m4a")))
	g := gaplessOf(r, id)
	if g == nil || *g != (container.Gapless{Delay: 2112, Padding: 576, Samples: 9600}) {
		t.Fatalf("gaplessOf = %+v", g)
	}
	if g := gaplessOf(r, id+1); g != nil {
		t.Fatalf("gaplessOf another track = %+v, want nil", g)
	}
}

func TestDroppedTicks(t *testing.T) {
	pk := func(durs ...uint32) []container.Sample {
		s := make([]container.Sample, len(durs))
		for i, d := range durs {
			s[i].Duration = d
		}
		return s
	}
	edit := func(mt int64, more ...container.Edit) container.TrackConfig {
		return container.TrackConfig{EditList: &container.EditList{MovieTimescale: 1000, Edits: append([]container.Edit{{MediaTime: mt}}, more...)}}
	}
	for name, c := range map[string]struct {
		tc   container.TrackConfig
		want int64
	}{
		"no edit list":               {container.TrackConfig{}, 0},
		"from 0":                     {edit(0), 0},
		"inside the first packet":    {edit(1000), 0},
		"on the second packet":       {edit(1024), 1024},
		"inside the third":           {edit(2112), 2048},
		"past the end":               {edit(1 << 40), 3072},
		"negative":                   {edit(-7), 0},
		"empty edits first":          {edit(container.EmptyEdit, container.Edit{MediaTime: 1030}), 1024},
		"only empty edits":           {edit(container.EmptyEdit), 0},
		"packets of different sizes": {edit(1500), 1424},
	} {
		got := droppedTicks(c.tc, pk(1024, 1024, 1024))
		if name == "packets of different sizes" {
			got = droppedTicks(c.tc, pk(1024, 400, 2000))
		}
		if got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}

// TestGaplessPad: the cut iTunSMPB's padding makes, on packets of 1024 samples at a rate
// equal to the timescale.
func TestGaplessPad(t *testing.T) {
	pk := func(n int) []container.Sample {
		s := make([]container.Sample, n)
		for i := range s {
			s[i].Duration = 1024
		}
		return s
	}
	g := func(padding, samples uint64) *container.Gapless {
		return &container.Gapless{Delay: 2112, Padding: padding, Samples: samples}
	}
	const big = math.MaxInt64
	for name, c := range map[string]struct {
		g           *container.Gapless
		n           int
		dur         int64
		start, lead int64
		pad         int64
		ok          bool
	}{
		"no iTunSMPB":              {nil, 12, 12288, 0, 0, 0, false},
		"no padding":               {g(0, 9600), 12, 12288, 0, 0, 0, false},
		"no duration":              {g(576, 9600), 12, 0, 0, 0, 0, false},
		"padding is the duration":  {g(12288, 9600), 12, 12288, 0, 0, 0, false},
		"samples are the duration": {g(576, 12288), 12, 12288, 0, 0, 0, false},
		"the last packet":          {g(576, 9600), 12, 12288, 0, 0, 576, true},
		"two packets":              {g(1600, 9600), 12, 12288, 0, 0, 1600, true},
		"after the edit":           {g(576, 9600), 12, 12288, 2048, 0, 0, true},   // pts is 2048 less: nothing reaches 11712
		"empty edit leads":         {g(576, 9600), 12, 12288, 0, 1024, 576, true}, // each packet 1024 later: the one before the last takes the cut
		"beyond the data":          {g(576, 9600), 6, 12288, 0, 0, 0, true},
		"first packet before 0":    {g(12000, 9600), 12, 12288, 100, 0, 11900, true}, // from 288: 636 of the first packet (-100..924), all of the other 11
		"hostile start":            {g(576, 9600), 3, 12288, 1 << 62, big, 0, true},
		"hostile lead":             {g(576, 9600), 3, 12288, 0, big, 0, true},
		"hostile duration":         {g(1, 2), 2, big, 0, 0, 0, true},
	} {
		got, ok := gaplessPad(c.g, pk(c.n), c.dur, c.start, c.lead, 32000, 1024, 32000)
		if ok != c.ok || got != c.pad {
			t.Errorf("%s: gaplessPad = %d, %v, want %d, %v", name, got, ok, c.pad, c.ok)
		}
	}
	// a rate that is not the timescale's: ticks of 1/16000 s at 32000 Hz
	if got, ok := gaplessPad(g(1280, 4800), pk(8), 8192, 0, 0, 32000, 2048, 16000); !ok || got != 2560 {
		t.Errorf("at twice the rate: %d, %v, want 2560, true", got, ok)
	}
}

// TestGaplessFollowsTheRateOfAnImplicitHE: an HE-AAC track that does not signal its SBR has
// its output rate from its first frame (22050 Hz core, 44100 Hz out), after the start was
// first worked out at the core rate. The delay, in ticks of the 44100 timescale, is the
// same number of samples at the rate that follows.
func TestGaplessFollowsTheRateOfAnImplicitHE(t *testing.T) {
	r, id := audioReader(t, mustRead(t, filepath.Join("testdata", "aac", "imp-he44-st.mp4")))
	tc, err := r.TrackConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	samples, err := r.Samples(id)
	if err != nil {
		t.Fatal(err)
	}
	count := func(g *container.Gapless) int {
		bufs, err := decodeAudioGapless(tc, samples, AudioConfig{}, 0, g)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, a := range bufs {
			if a.SampleRate != 44100 {
				t.Fatalf("output rate %d", a.SampleRate)
			}
			n += a.Samples()
		}
		return n
	}
	plain := count(nil)
	if tc.Timescale != 44100 || plain == 0 {
		t.Fatalf("fixture: timescale %d, %d samples", tc.Timescale, plain)
	}
	if got := count(&container.Gapless{Delay: 1000}); got != plain-1000 {
		t.Errorf("a delay of 1000 ticks: %d samples, want %d", got, plain-1000)
	}
}

// countWriter counts what is written to it.
type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

// TestGaplessRealFiles runs the files listed (one path a line) in the file named by
// GAPLESS_REAL_LIST, which are not in this repository: those whose first audio track is AAC
// and whose movie states an iTunSMPB are decoded with it and without, and the length ffmpeg
// delivers for the track is counted. Nothing is logged but a hash of the path and numbers.
func TestGaplessRealFiles(t *testing.T) {
	list := os.Getenv("GAPLESS_REAL_LIST")
	if list == "" {
		t.Skip("GAPLESS_REAL_LIST=<file of paths> checks real files against ffmpeg")
	}
	var scanned, aac, anyGapless, withSMPB, changed, same, differ int
	for _, path := range strings.Split(strings.TrimSpace(string(mustRead(t, list))), "\n") {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			continue
		}
		r, err := container.NewFileReader(f, st.Size())
		if err != nil {
			f.Close()
			continue
		}
		scanned++
		id, err := FirstAudioTrack(r)
		g := gaplessOf(r, id)
		tc, tcErr := r.TrackConfig(id)
		if err == nil && tcErr == nil && tc.Codec == "mp4a" {
			aac++
		}
		if r.File().Metadata.Gapless != nil {
			anyGapless++
		}
		if err != nil || g == nil || tcErr != nil || tc.Codec != "mp4a" {
			f.Close()
			continue
		}
		withSMPB++
		with, err1 := DecodeAudioTrack(r, id, AudioConfig{})
		samples, err2 := r.Samples(id)
		without, err3 := decodeAudioDur(tc, samples, AudioConfig{}, mediaDuration(r, id))
		f.Close()
		count := func(bufs []Audio) (n, ch int) {
			for _, a := range bufs {
				n += a.Samples()
				ch = a.Channels
			}
			return n, ch
		}
		nw, ch := count(with)
		nwo, _ := count(without)
		var cw countWriter
		cmd := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-i", path, "-map", "0:a:0", "-f", "f32le", "pipe:1")
		cmd.Stdout = &cw
		ffErr := cmd.Run()
		sum := fmt.Sprintf("%x", sha256.Sum256([]byte(path)))[:10]
		ff := int64(-1)
		if ffErr == nil && ch > 0 {
			ff = cw.n / int64(4*ch)
		}
		verdict := "SAME"
		switch {
		case err1 != nil || err2 != nil || err3 != nil:
			verdict = fmt.Sprintf("ERROR %v %v %v", err1, err2, err3)
			differ++
		case int64(nw) != ff:
			verdict = "DIFFERS"
			differ++
		default:
			same++
		}
		if nw != nwo {
			changed++
		}
		t.Logf("%s ts=%d ch=%d smpb=%d/%d/%d without=%d with=%d ffmpeg=%d %s", sum, tc.Timescale, ch, g.Delay, g.Padding, g.Samples, nwo, nw, ff, verdict)
	}
	t.Logf("scanned %d MP4 files, %d with an AAC first audio track, %d of any with an iTunSMPB; %d of the AAC ones apply it; %d change length with it; %d equal ffmpeg's length, %d not", scanned, aac, anyGapless, withSMPB, changed, same, differ)
}

// FuzzGaplessPad gives gaplessPad the numbers a file states, at their limits: it must not
// panic or overflow into a negative cut.
func FuzzGaplessPad(f *testing.F) {
	f.Add(uint64(576), uint64(9600), int64(12288), int64(0), int64(0), 12, uint32(1024), 32000, 1024, int64(32000))
	f.Add(uint64(1<<63), uint64(1<<63), int64(math.MaxInt64), int64(math.MaxInt64), int64(math.MaxInt64), 3, uint32(math.MaxUint32), math.MaxInt32, math.MaxInt32, int64(1))
	f.Fuzz(func(t *testing.T, padding, samples uint64, dur, start, empty int64, n int, d uint32, rate, frame int, ts int64) {
		n = max(0, min(n, 64))
		pk := make([]container.Sample, n)
		for i := range pk {
			pk[i].Duration = d
		}
		pad, ok := gaplessPad(&container.Gapless{Padding: padding, Samples: samples}, pk, dur, start, empty, rate, frame, ts)
		if pad < 0 || !ok && pad != 0 {
			t.Fatalf("pad %d, applies %v", pad, ok)
		}
	})
}
