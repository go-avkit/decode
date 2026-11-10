// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/gen2brain/h265/hevc"
	"github.com/go-avkit/avkit/container"
)

func hevcStream(t *testing.T, path string) (vps, sps, pps [][]byte, samples [][][]byte, raw []byte) {
	t.Helper()
	raw = readFile(t, path)
	vps, sps, pps, samples = hevcSets(hevcAccessUnits(splitNALs(raw)))
	return
}

// decodeAnnexBChunks feeds a stream in chunks of n bytes and returns every
// picture, flushed.
func decodeAnnexBChunks(t *testing.T, raw []byte, n int) []Frame {
	t.Helper()
	d, err := NewHEVC(Config{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var out []Frame
	for rest := raw; len(rest) > 0; {
		k := min(n, len(rest))
		fs, err := d.DecodeAnnexB(rest[:k])
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fs...)
		rest = rest[k:]
	}
	fs, err := d.Flush()
	if err != nil {
		t.Fatal(err)
	}
	return append(out, fs...)
}

func TestHEVCAnnexB(t *testing.T) {
	for _, path := range hevcStreams(t) {
		raw := readFile(t, path)
		want := hevcReference(t, path)
		for _, n := range []int{1 << 20, 997, 3, 1} {
			frames := decodeAnnexBChunks(t, raw, n)
			if !slices.Equal(sums(frames), want) {
				t.Fatalf("%s in chunks of %d: pictures differ from ffmpeg", path, n)
			}
			for i, f := range frames {
				if f.PTS != int64(i) || f.Corrupt || f.Chroma != Chroma420 {
					t.Fatalf("%s picture %d: PTS %d corrupt %v chroma %d", path, i, f.PTS, f.Corrupt, f.Chroma)
				}
				step := 1
				if f.BitDepth == 10 {
					step = 2
				}
				if len(f.Y) != f.Width*f.Height*step || f.YStride != f.Width*step || len(f.Cb) != len(f.Y)/4 || f.CStride != f.YStride/2 {
					t.Fatalf("%s picture %d: planes %d/%d/%d for %dx%d at %d bits", path, i, len(f.Y), len(f.Cb), len(f.Cr), f.Width, f.Height, f.BitDepth)
				}
			}
		}
	}
	// A conformance window cuts a 100x70 picture out of the 104x72 the
	// stream codes.
	if f := decodeAnnexBChunks(t, readFile(t, "testdata/hevc/streams/main_ipb.265"), 1<<20)[0]; f.Width != 100 || f.Height != 70 {
		t.Errorf("cropped size %dx%d, want 100x70", f.Width, f.Height)
	}
}

func TestHEVCSamplesInBand(t *testing.T) {
	for _, path := range hevcStreams(t) {
		raw := readFile(t, path)
		aus := hevcAccessUnits(splitNALs(raw))
		d, err := NewHEVC(Config{}, 4)
		if err != nil {
			t.Fatal(err)
		}
		var frames []Frame
		for i, au := range aus {
			fs, err := d.DecodeSample(prefixed(au, 4), int64(i)*40+80)
			if err != nil {
				t.Fatal(err)
			}
			frames = append(frames, fs...)
		}
		fs, err := d.Flush()
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, fs...)
		d.Close()
		if !slices.Equal(sums(frames), hevcReference(t, path)) {
			t.Fatalf("%s: pictures differ from ffmpeg", path)
		}
		// Each picture carries the presentation time of the sample that held its
		// first slice, whatever order the pictures come out in.
		var got, want []int64
		for i, f := range frames {
			got = append(got, f.PTS)
			want = append(want, int64(i)*40+80)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("%s: PTS %v, want %v", path, got, want)
		}
	}
}

func TestHEVCHVCC(t *testing.T) {
	for _, size := range []int{4, 2} {
		path := "testdata/hevc/streams/main10_ipb.265"
		vps, sps, pps, samples, _ := hevcStream(t, path)
		d, err := NewHVCC(hvcC(vps, sps, pps, size), Config{})
		if err != nil {
			t.Fatal(err)
		}
		var frames []Frame
		for i, s := range samples {
			fs, err := d.DecodeSample(prefixed(s, size), int64(i))
			if err != nil {
				t.Fatal(err)
			}
			frames = append(frames, fs...)
		}
		fs, err := d.Flush()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(sums(append(frames, fs...)), hevcReference(t, path)) {
			t.Fatalf("length size %d: pictures differ from ffmpeg", size)
		}
	}
}

// hevcMP4 muxes a stream into an MP4 with avkit's own muxer, parameter sets in
// the sample entry as hvc1 requires.
func hevcMP4(t *testing.T, path string, offset int32) []byte {
	t.Helper()
	vps, sps, pps, samples, _ := hevcStream(t, path)
	var buf bytes.Buffer
	m := container.NewProgressiveMuxer(&buf)
	id, err := m.AddTrack(container.TrackConfig{
		Kind: container.Video, Codec: "hvc1", Timescale: 1000, Width: 100, Height: 70, VPS: vps, SPS: sps, PPS: pps,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range samples {
		if err := m.WriteSample(id, container.Sample{
			Data: prefixed(s, 4), Duration: 40, Sync: i == 0, CompositionOffset: offset,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestHEVCDecodeTrack(t *testing.T) {
	// An intra stream comes out in decoding order, so its times are in order too.
	path := "testdata/hevc/streams/main_intra.265"
	r, err := container.NewReader(hevcMP4(t, path, 80))
	if err != nil {
		t.Fatal(err)
	}
	// The 80 ms of composition offset is stated by the edit list the muxer writes:
	// the presentation starts at 0, or at 80 when the edit list is ignored.
	for _, c := range []struct {
		cfg   Config
		first int64
	}{{Config{}, 0}, {Config{IgnoreEditList: true}, 80}} {
		frames, err := DecodeTrack(r, r.TrackIDs()[0], c.cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(sums(frames), hevcReference(t, path)) {
			t.Fatal("pictures differ from ffmpeg")
		}
		for i, f := range frames {
			if want := int64(i)*40 + c.first; f.PTS != want {
				t.Fatalf("%+v, picture %d: PTS %d, want %d", c.cfg, i, f.PTS, want)
			}
		}
	}
	path = "testdata/hevc/streams/main10_ipb.265"
	if r, err = container.NewReader(hevcMP4(t, path, 0)); err != nil {
		t.Fatal(err)
	}
	if frames, err := DecodeTrack(r, r.TrackIDs()[0], Config{}); err != nil || !slices.Equal(sums(frames), hevcReference(t, path)) {
		t.Fatalf("Main 10 track: %v", err)
	}
	// The picture size limit is refused when the first slice names its picture.
	if _, err = DecodeTrack(r, r.TrackIDs()[0], Config{MaxFrameMBs: 1}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("limit: %v", err)
	}
}

func TestHEVCRefusals(t *testing.T) {
	vps, sps, pps, samples, raw := hevcStream(t, "testdata/hevc/streams/main_ipb.265")

	if _, err := NewHEVC(Config{}, 3); !errors.Is(err, ErrConfig) {
		t.Errorf("length size 3: %v", err)
	}
	if _, err := NewHVCC(nil, Config{}); !errors.Is(err, ErrConfig) {
		t.Errorf("empty hvcC: %v", err)
	}

	// Parameter sets that are not NAL units, or are NAL units that do not parse.
	if _, err := newHEVCWithSets(Config{}, 4, nil, [][]byte{{0x80, 1}}, nil); !errors.Is(err, ErrConfig) {
		t.Errorf("a set with a forbidden bit: %v", err)
	}
	if _, err := newHEVCWithSets(Config{}, 4, nil, [][]byte{{0x42, 1, 0xff}}, nil); !errors.Is(err, ErrBitstream) {
		t.Errorf("a truncated SPS: %v", err)
	}
	if _, err := NewTrack(container.TrackConfig{Codec: "hev1", VPS: vps, SPS: sps, PPS: pps}, Config{}); err != nil {
		t.Errorf("hev1 track: %v", err)
	}

	d, _ := NewHEVC(Config{MaxSampleBytes: 100}, 4)
	defer d.Close()
	if _, err := d.DecodeSample(make([]byte, 101), 0); !errors.Is(err, ErrOverLimit) {
		t.Errorf("a long sample: %v", err)
	}
	if _, err := d.DecodeSample([]byte{0, 0, 0}, 0); !errors.Is(err, ErrSample) {
		t.Errorf("a short length: %v", err)
	}
	if _, err := d.DecodeSample([]byte{0, 0, 0, 2, 0x80, 1}, 0); !errors.Is(err, ErrSample) {
		t.Errorf("a header with the forbidden bit: %v", err)
	}
	// What is held waiting for the next start code is bounded too.
	if _, err := d.DecodeAnnexB(append([]byte{0, 0, 1}, make([]byte, 60)...)); err != nil {
		t.Errorf("a first chunk: %v", err)
	}
	if _, err := d.DecodeAnnexB(make([]byte, 60)); !errors.Is(err, ErrOverLimit) {
		t.Errorf("a NAL unit that never ends: %v", err)
	}

	// A slice that names a parameter set the stream never sent.
	fresh, _ := NewHEVC(Config{}, 4)
	if _, err := fresh.DecodeSample(prefixed(samples[0][len(samples[0])-1:], 4), 0); !errors.Is(err, ErrBitstream) {
		t.Errorf("a slice with no parameter sets: %v", err)
	}
	// The same slice held at the end of an Annex B stream is refused by Flush.
	last := splitNALs(raw)[4]
	if _, err := fresh.DecodeAnnexB(append([]byte{0, 0, 1}, last...)); err != nil {
		t.Errorf("a held slice: %v", err)
	}
	if _, err := fresh.Flush(); !errors.Is(err, ErrBitstream) {
		t.Errorf("flushing a held slice: %v", err)
	}

	// 4:4:4 is valid HEVC this package does not decode.
	fs, ferr := decodeAll(t, readFile(t, "testdata/hevc/streams/yuv444_unsupported.265"))
	if !errors.Is(ferr, ErrUnsupported) || len(fs) != 0 {
		t.Errorf("4:4:4: %d pictures, %v", len(fs), ferr)
	}
	// And so is a picture over the size limit, which the HEVC decoder reports as
	// it does an unimplemented tool.
	lim, _ := NewHEVC(Config{MaxFrameMBs: 1}, 4)
	if _, err := lim.DecodeAnnexB(raw); !errors.Is(err, ErrUnsupported) {
		t.Errorf("a frame over the limit: %v", err)
	}
	if got := lim.classifyHEVC(errors.New("other")); got.Error() != "other" {
		t.Errorf("an unknown error became %v", got)
	}

	// Closed, a decoder refuses everything.
	d.Close()
	if _, err := d.Flush(); !errors.Is(err, ErrClosed) {
		t.Errorf("flush after close: %v", err)
	}
}

// decodeAll decodes a whole Annex B stream and returns the pictures and the
// first error.
func decodeAll(t *testing.T, raw []byte) ([]Frame, error) {
	t.Helper()
	d, err := NewHEVC(Config{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	fs, err := d.DecodeAnnexB(raw)
	if err != nil {
		return fs, err
	}
	rest, err := d.Flush()
	return append(fs, rest...), err
}

func TestHEVCFrame(t *testing.T) {
	d, _ := NewHEVC(Config{}, 4)
	pic := func(depth int) *hevc.Picture {
		p := &hevc.Picture{
			Width: 8, Height: 4, CropX: 2, CropY: 2, CropW: 4, CropH: 2, ChromaFormat: 1,
			BitDepth: depth, BitDepthC: depth, StrideY: 8, StrideC: 4, WidthC: 4, HeightC: 2, POC: 3, Tag: 9,
		}
		if depth > 8 {
			p.Y16, p.Cb16, p.Cr16 = make([]uint16, 32), make([]uint16, 8), make([]uint16, 8)
			for i := range p.Y16 {
				p.Y16[i] = uint16(0x100 + i)
			}
			return p
		}
		p.Y, p.Cb, p.Cr = make([]byte, 32), make([]byte, 8), make([]byte, 8)
		for i := range p.Y {
			p.Y[i] = byte(i)
		}
		return p
	}
	f, err := d.hevcFrame(pic(8))
	if err != nil || !slices.Equal(f.Y, []byte{18, 19, 20, 21, 26, 27, 28, 29}) || f.Width != 4 || f.Height != 2 || f.PTS != 0 {
		t.Fatalf("8-bit: %v %+v", err, f)
	}
	d.sampleMode = true
	f, err = d.hevcFrame(pic(10))
	if err != nil || !slices.Equal(f.Y[:4], []byte{0x12, 0x01, 0x13, 0x01}) || f.BitDepth != 10 || f.PTS != 9 || f.YStride != 8 {
		t.Fatalf("10-bit: %v %+v", err, f)
	}
	p := pic(8)
	p.Corrupt = true
	if f, _ = d.hevcFrame(p); !f.Corrupt {
		t.Error("a corrupt picture was reported whole")
	}

	bad := map[string]func(*hevc.Picture){
		"4:2:2":                  func(p *hevc.Picture) { p.ChromaFormat = 2 },
		"unequal depths":         func(p *hevc.Picture) { p.BitDepthC = 10 },
		"12 bits":                func(p *hevc.Picture) { p.BitDepth, p.BitDepthC = 12, 12 },
		"an empty window":        func(p *hevc.Picture) { p.CropW = 0 },
		"a window off the right": func(p *hevc.Picture) { p.CropX = 6 },
		"a negative origin":      func(p *hevc.Picture) { p.CropX = -2 },
		"a window below":         func(p *hevc.Picture) { p.CropH = 4 },
		"a short plane":          func(p *hevc.Picture) { p.Y = p.Y[:20] },
		"a short chroma":         func(p *hevc.Picture) { p.Cr = p.Cr[:2] },
	}
	for name, mutate := range bad {
		p := pic(8)
		mutate(p)
		if _, err := d.hevcFrame(p); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	p = pic(10)
	p.Cb16 = nil
	if _, err := d.hevcFrame(p); !errors.Is(err, ErrBitstream) {
		t.Errorf("a short 16-bit plane: %v", err)
	}
	if _, err := d.hevcFrame(&hevc.Picture{ChromaFormat: 2}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("unsupported: %v", err)
	}

	// Every picture of a batch is converted; the first error is the one returned.
	good, wrong := pic(8), pic(8)
	wrong.ChromaFormat = 3
	again := pic(8)
	again.ChromaFormat = 2
	fs, err := d.hevcFrames([]*hevc.Picture{wrong, good, again})
	if len(fs) != 1 || !errors.Is(err, ErrUnsupported) {
		t.Errorf("a batch: %d pictures, %v", len(fs), err)
	}
}

func TestHEVCCloseAndFlush(t *testing.T) {
	d, _ := NewHEVC(Config{}, 4)
	if fs, err := d.Flush(); err != nil || len(fs) != 0 {
		t.Errorf("flushing nothing: %v %v", fs, err)
	}
	if err := d.Close(); err != nil {
		t.Error(err)
	}
	if _, err := d.DecodeAnnexB(nil); !errors.Is(err, ErrClosed) {
		t.Errorf("%v", err)
	}
}

func TestLastStartCode(t *testing.T) {
	if got := lastStartCode([]byte{1, 0, 0, 1, 5, 0, 0, 0, 1}, 0); got != 6 {
		t.Errorf("got %d, want 6", got)
	}
	if got := lastStartCode([]byte{0, 0, 1, 5}, 1); got != -1 {
		t.Errorf("a code before from: %d", got)
	}
}
