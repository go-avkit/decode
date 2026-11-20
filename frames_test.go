// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"errors"
	"testing"

	"github.com/go-avkit/avkit/container"
)

// collect drains Frames into a slice and the error that ended it, checking that
// an error is the last pair and comes with no Frame.
func collect(t *testing.T, r *container.Reader, id uint32, cfg Config) ([]Frame, error) {
	t.Helper()
	var out []Frame
	var last error
	for f, err := range Frames(r, id, cfg) {
		if last != nil {
			t.Fatalf("a pair after the error %v", last)
		}
		if err != nil {
			if f != nil {
				t.Fatalf("an error %v with a Frame", err)
			}
			last = err
			continue
		}
		if f == nil {
			t.Fatal("a nil Frame without an error")
		}
		out = append(out, *f)
	}
	return out, last
}

func sameFrames(t *testing.T, got, want []Frame) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d pictures, DecodeTrack %d", len(got), len(want))
	}
	for i := range got {
		g, w := got[i], want[i]
		if g.PTS != w.PTS || g.Width != w.Width || g.Height != w.Height || g.YStride != w.YStride ||
			g.CStride != w.CStride || g.Chroma != w.Chroma || g.BitDepth != w.BitDepth || g.Corrupt != w.Corrupt ||
			!bytes.Equal(g.Y, w.Y) || !bytes.Equal(g.Cb, w.Cb) || !bytes.Equal(g.Cr, w.Cr) {
			t.Fatalf("picture %d differs from DecodeTrack's (PTS %d and %d)", i, g.PTS, w.PTS)
		}
	}
}

// Frames gives exactly the pictures of DecodeTrack, at the same times, on every
// fixture of the edit-list corpus (H.264 and HEVC, with and without a B-frame
// delay, trimmed, delayed, fragmented, several edits, Matroska), with the edit
// list applied and ignored, and they are the ones ffmpeg's recordings state.
func TestFramesMatchDecodeTrack(t *testing.T) {
	for _, fx := range editFixtures {
		t.Run(fx.name, func(t *testing.T) {
			r, id := reader(t, readFile(t, fixturePath(t, fx.name)))
			for _, cfg := range []Config{{}, {IgnoreEditList: true}} {
				want, err := DecodeTrack(r, id, cfg)
				if err != nil {
					t.Fatal(err)
				}
				got, err := collect(t, r, id, cfg)
				if err != nil {
					t.Fatal(err)
				}
				sameFrames(t, got, want)
				if !cfg.IgnoreEditList {
					compareWithFFmpeg(t, got, recording(t, fx.name, false), fx.ptsLies)
				} else if !fx.noEdits {
					compareWithFFmpeg(t, got, recording(t, fx.name, true), true)
				}
			}
		})
	}
}

// Every Frame is the caller's own: the ones of the whole sequence, kept, are still
// what DecodeTrack returns after the loop has gone on to the last.
func TestFramesAreNotReused(t *testing.T) {
	r, id := reader(t, readFile(t, fixturePath(t, "h264-delay.mp4")))
	want, err := DecodeTrack(r, id, Config{})
	if err != nil {
		t.Fatal(err)
	}
	var kept []*Frame
	for f, err := range Frames(r, id, Config{}) {
		if err != nil {
			t.Fatal(err)
		}
		kept = append(kept, f)
	}
	got := make([]Frame, len(kept))
	for i, f := range kept {
		got[i] = *f
	}
	sameFrames(t, got, want)
	for i := 1; i < len(kept); i++ {
		if &kept[i].Y[0] == &kept[i-1].Y[0] {
			t.Fatalf("pictures %d and %d share a plane", i-1, i)
		}
	}
}

// Breaking out of the loop stops the decoding: no more pictures are asked for, and
// no error follows.
func TestFramesBreak(t *testing.T) {
	for _, name := range []string{"h264-edits.mp4", "hevc-edits.mp4", "h264-delay.mp4"} {
		t.Run(name, func(t *testing.T) {
			r, id := reader(t, readFile(t, fixturePath(t, name)))
			want, err := DecodeTrack(r, id, Config{})
			if err != nil || len(want) < 3 {
				t.Fatalf("%d pictures, %v", len(want), err)
			}
			for stop := 1; stop <= len(want); stop += max(1, len(want)/5) {
				n := 0
				for f, err := range Frames(r, id, Config{}) {
					if err != nil {
						t.Fatal(err)
					}
					if sum(*f) != sum(want[n]) {
						t.Fatalf("picture %d differs", n)
					}
					n++
					if n == stop {
						break
					}
				}
				if n != stop {
					t.Fatalf("stopped at %d, asked for %d", n, stop)
				}
			}
		})
	}
}

// What DecodeTrack refuses, Frames refuses, as the one pair of the sequence; a
// limit that is hit partway comes after the pictures before it, as DecodeTrack
// returns them beside its error.
func TestFramesErrors(t *testing.T) {
	r, id := reader(t, readFile(t, fixturePath(t, "h264-trim.mp4")))
	for _, cfg := range []Config{{MaxFrameMBs: 1}, {MaxSampleBytes: 100}} {
		want, wantErr := DecodeTrack(r, id, cfg)
		got, err := collect(t, r, id, cfg)
		if !errors.Is(err, ErrOverLimit) || !errors.Is(wantErr, ErrOverLimit) {
			t.Fatalf("%+v: %v, DecodeTrack %v", cfg, err, wantErr)
		}
		sameFrames(t, got, want)
	}
	if _, err := collect(t, r, 99, Config{}); err == nil {
		t.Error("a track that does not exist was accepted")
	}
	dwell := edit(400, 0)
	dwell.Rate = 0
	r, id = reader(t, mp4Edits(t, &container.EditList{MovieTimescale: 1000, Edits: []container.Edit{dwell}}))
	if fs, err := collect(t, r, id, Config{}); !errors.Is(err, ErrEditList) || len(fs) != 0 {
		t.Errorf("an edit list that is refused: %d pictures, %v", len(fs), err)
	}
	if fs, err := collect(t, r, id, Config{IgnoreEditList: true}); err != nil || len(fs) == 0 {
		t.Errorf("IgnoreEditList: %d pictures, %v", len(fs), err)
	}
	// An edit that presents nothing is an empty sequence.
	r, id = reader(t, mp4Edits(t, &container.EditList{MovieTimescale: 1000, Edits: []container.Edit{edit(400, 1<<40)}}))
	if fs, err := collect(t, r, id, Config{}); err != nil || len(fs) != 0 {
		t.Errorf("an edit past the end: %d pictures, %v", len(fs), err)
	}
}
