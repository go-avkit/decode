// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"fmt"
	"math"

	"github.com/go-avkit/avkit/container"
)

// Errors PosterFrame refuses with, besides the ones above.
var (
	// ErrPosterLimit means the picture asked for is further from the keyframe it
	// is decoded from than PosterConfig.MaxSamples allows, or the track lists
	// more samples than PosterConfig.MaxTrackSamples. It is raised before the
	// decoder is built, so a stream with a distant keyframe costs nothing.
	ErrPosterLimit = errors.New("decode: poster frame exceeds the configured work limit")
	// ErrTime means the time asked for is negative or not a number.
	ErrTime = errors.New("decode: invalid poster time")
	// ErrNoVideo means the file has no video track.
	ErrNoVideo = errors.New("decode: no video track")
)

// Default PosterConfig bounds.
const (
	// DefaultMaxPosterSamples is 600 samples, ten seconds of 60 frames per
	// second video decoded for a single picture.
	DefaultMaxPosterSamples = 600
	// DefaultMaxTrackSamples is 1<<20 samples, about 4.8 hours at 60 frames per
	// second.
	DefaultMaxTrackSamples = 1 << 20
)

// PosterConfig bounds the work PosterFrame does for one picture, on top of the
// decoder's own Config. A zero field takes its default.
type PosterConfig struct {
	Config
	// MaxSamples is the most samples decoded for the picture: the run from the
	// keyframe it starts at, up to the sample that outputs it, which can come
	// several samples after the sample holding it when the stream reorders.
	MaxSamples int
	// MaxTrackSamples is the longest sample list accepted.
	MaxTrackSamples int
}

func (c PosterConfig) maxSamples() int {
	if c.MaxSamples > 0 {
		return c.MaxSamples
	}
	return DefaultMaxPosterSamples
}

func (c PosterConfig) maxTrackSamples() int {
	if c.MaxTrackSamples > 0 {
		return c.MaxTrackSamples
	}
	return DefaultMaxTrackSamples
}

// FirstVideoTrack returns the ID of the first video track of a demuxed file.
func FirstVideoTrack(r *container.Reader) (uint32, error) {
	for _, tr := range r.File().Tracks {
		if tr.Kind == container.Video {
			return tr.ID, nil
		}
	}
	return 0, ErrNoVideo
}

// PosterFrame decodes the one picture a track displays at t seconds: the one
// with the greatest presentation time at or before t, where the presentation time
// is the one DecodeTrack gives: the sample's decode time plus its composition
// offset, as the track's edit list presents it (Config.IgnoreEditList leaves the
// edit list out, and gives the time before it). It returns DecodeTrack's picture
// and PTS byte for byte. A track whose edit list presents no picture, or states
// what ErrEditList describes, is refused with ErrEditList.
//
// A t past the end of the track gives the last picture, and a t before the first
// presentation time gives the first. A negative or NaN t is ErrTime. Decoding
// starts at the last sample marked Sync at or before the picture's sample (the
// first sample when none is) and stops as soon as the picture is out, so the work
// is bounded by cfg.MaxSamples, not by the length of the track (ErrPosterLimit).
// Only H.264 and HEVC are decoded; another codec is ErrUnsupported.
//
// The track's samples are listed by container.Reader.Samples, which a reader on
// a file that stays on disk (container.NewFileReader) fetches whole: the limit on
// their number is checked once they are listed.
func PosterFrame(r *container.Reader, trackID uint32, t float64, cfg PosterConfig) (Frame, error) {
	if math.IsNaN(t) || t < 0 {
		return Frame{}, fmt.Errorf("%w: %v seconds", ErrTime, t)
	}
	tc, err := r.TrackConfig(trackID)
	if err != nil {
		return Frame{}, err
	}
	switch tc.Codec {
	case "avc1", "avc3", "hvc1", "hev1":
	default:
		return Frame{}, fmt.Errorf("%w: codec %q, only H.264 (avc1, avc3) and HEVC (hvc1, hev1) are decoded", ErrUnsupported, tc.Codec)
	}
	samples, err := r.Samples(trackID)
	if err != nil {
		return Frame{}, err
	}
	if len(samples) == 0 || len(samples) > cfg.maxTrackSamples() {
		return Frame{}, fmt.Errorf("%w: track lists %d samples, want 1 to %d", ErrPosterLimit, len(samples), cfg.maxTrackSamples())
	}
	target, targetPTS, shift := 0, int64(0), int64(0)
	if cfg.IgnoreEditList || tc.EditList == nil {
		target, targetPTS = posterSample(samples, t, tc.Timescale)
	} else {
		runs, err := editRuns(tc, samples)
		if err != nil {
			return Frame{}, err
		}
		if target, targetPTS, shift = editedSample(samples, runs, t, tc.Timescale); target < 0 {
			return Frame{}, fmt.Errorf("%w: the edit list presents no picture", ErrEditList)
		}
	}
	start := target
	for start > 0 && !samples[start].Sync {
		start--
	}
	if n := target - start + 1; n > cfg.maxSamples() {
		return Frame{}, fmt.Errorf("%w: %d samples from the keyframe, the limit is %d", ErrPosterLimit, n, cfg.maxSamples())
	}
	var dts int64
	for _, s := range samples[:start] {
		dts += int64(s.Duration)
	}
	d, err := NewTrack(tc, cfg.Config)
	if err != nil {
		return Frame{}, err
	}
	defer d.Close()
	fed := 0
	for _, s := range samples[start:] {
		if fed == cfg.maxSamples() {
			return Frame{}, fmt.Errorf("%w: the picture is not out after %d samples", ErrPosterLimit, fed)
		}
		fed++
		fs, err := d.DecodeSample(s.Data, dts+int64(s.CompositionOffset))
		if f, ok := pick(fs, targetPTS); ok {
			f.PTS += shift
			return f, nil
		}
		if err != nil {
			return Frame{}, err
		}
		dts += int64(s.Duration)
	}
	fs, err := d.Flush()
	if f, ok := pick(fs, targetPTS); ok {
		f.PTS += shift
		return f, nil
	}
	return Frame{}, fmt.Errorf("%w: no picture at presentation time %d (flush: %v)", ErrSample, targetPTS, err)
}

// posterSample returns the index and presentation time of the sample whose
// picture is displayed at t seconds. It allocates nothing.
func posterSample(samples []container.Sample, t float64, timescale uint32) (int, int64) {
	want := ticks(t, timescale)
	var dts int64
	best, bestPTS := -1, int64(0)  // greatest PTS <= want
	first, firstPTS := 0, int64(0) // smallest PTS, for a t before them all
	for i, s := range samples {
		pts := dts + int64(s.CompositionOffset)
		dts += int64(s.Duration)
		if i == 0 || pts < firstPTS {
			first, firstPTS = i, pts
		}
		if pts <= want && (best < 0 || pts > bestPTS) {
			best, bestPTS = i, pts
		}
	}
	if best < 0 {
		return first, firstPTS
	}
	return best, bestPTS
}

// ticks is t seconds in units of 1/timescale, rounded down; a time beyond what an
// int64 holds is the largest one.
func ticks(t float64, timescale uint32) int64 {
	if x := math.Floor(t * float64(timescale)); x < 9e18 {
		return int64(x)
	}
	return math.MaxInt64
}

// editedSample is posterSample for a track whose edit list is applied: among the
// pictures the runs present, the one with the greatest presentation time at or
// before t, or the first when t is before them all. It returns the sample, its
// track time (what the decoder labels the picture with), and what to add to that
// for its presentation time; the sample is -1 when no picture is presented.
func editedSample(samples []container.Sample, runs []editRun, t float64, timescale uint32) (sample int, key, shift int64) {
	want := ticks(t, timescale)
	best, first := -1, -1
	var bestPTS, firstPTS int64
	var bestKey, firstKey, bestShift, firstShift int64
	for _, run := range runs {
		dts := run.dts
		for k := run.first; k < run.end; k++ {
			c := dts + int64(samples[k].CompositionOffset)
			dts += int64(samples[k].Duration)
			if c < run.lo || c >= run.hi {
				continue
			}
			pts := c + run.shift
			if first < 0 || pts < firstPTS {
				first, firstPTS, firstKey, firstShift = k, pts, c, run.shift
			}
			if pts <= want && (best < 0 || pts > bestPTS) {
				best, bestPTS, bestKey, bestShift = k, pts, c, run.shift
			}
		}
	}
	if best < 0 {
		return first, firstKey, firstShift
	}
	return best, bestKey, bestShift
}

// pick returns the first picture of fs presented at pts.
func pick(fs []Frame, pts int64) (Frame, bool) {
	for _, f := range fs {
		if f.PTS == pts {
			return f, true
		}
	}
	return Frame{}, false
}
