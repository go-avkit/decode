// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/go-avkit/avkit/container"
)

// ErrEditList means the track's edit list says something this package does not
// apply, and applying part of it would present other pictures than the file
// states: a media rate other than 1, an edit that lasts to the end of the media
// (duration 0), an empty edit after the first edit that shows media, or a time
// that does not fit. The message names the edit. Set Config.IgnoreEditList to
// decode the track without its edit list.
var ErrEditList = errors.New("decode: edit list not supported")

// maxEditWork bounds, as a multiple of a track's samples, how many samples an
// edit list may make the decoder walk in all: an edit list is part of the
// untrusted input, and one that repeats a stretch of the track a million times
// would otherwise cost a million decodes.
const maxEditWork = 16

// presented returns tc with the edit list the track is presented by, or the
// track as it is when it has none and none is implied.
//
// A Matroska or WebM block states its presentation time. container.Reader turns
// the blocks into samples that decode from 0, as an MP4 track does, which needs a
// composition offset of the longest reordering delay on every sample, and states
// no edit list for it: the pictures would be presented that delay late (40 ms on
// bframes.mkv, where the blocks start at 0 and ffmpeg's first picture is at 0).
// The delay is the smallest composition time, so the implied edit list is one
// that starts the presentation there, the one an MP4 muxer writes for the same
// delay.
func presented(r *container.Reader, tc container.TrackConfig, samples []container.Sample) container.TrackConfig {
	if tc.EditList != nil || (r.File().Format != "matroska" && r.File().Format != "webm") {
		return tc
	}
	var dts, delay int64
	for i, s := range samples {
		if c := dts + int64(s.CompositionOffset); i == 0 || c < delay {
			delay = c
		}
		dts += int64(s.Duration)
	}
	if delay <= 0 {
		return tc
	}
	tc.EditList = &container.EditList{MovieTimescale: tc.Timescale, Edits: []container.Edit{{
		SegmentDuration: 1 << 62, MediaTime: delay, Rate: container.NormalRate,
	}}}
	return tc
}

// editRun is one edit of an edit list that shows media, as ffmpeg's mov_fix_index
// (libavformat/mov.c) lays it out: the samples it makes the decoder walk, which
// start at the keyframe at or before the edit's media time so that the pictures
// the edit shows can be decoded, and the composition times it keeps.
type editRun struct {
	// first and end bound the samples to decode, end exclusive; dts is the
	// decoding time of sample first.
	first, end int
	dts        int64
	// A picture whose composition time t has lo <= t < hi is presented, at t+shift.
	// The others are decoded, for they may be references, and not returned.
	lo, hi, shift int64
}

// editRuns reads tc.EditList the way mov_fix_index does for a video track and
// returns, in presentation order, the runs that show at least one picture.
//
// What it mirrors, in the track's timescale (a segment duration is rescaled from
// the movie timescale, rounding to the nearest, as get_edit_list_entry does):
//   - Empty edits before the first edit that shows media only delay the
//     presentation: their durations are summed and every presentation time is
//     counted from the end of them (st->start_time).
//   - An edit keeps the samples whose composition time (decoding time plus
//     composition offset) is at or after its media time and before its media time
//     plus its duration.
//   - The first kept sample of an edit is presented at the sum of the durations of
//     the edits before it; the samples after it, kept or not, advance by their
//     decoding time, and a picture is presented at that time plus its composition
//     offset.
//   - Finally the smallest presentation time, less the leading empty edits, is
//     taken off every time when it is positive, so that the first picture is
//     presented at the end of the leading empty edits and not at its composition
//     offset (min_corrected_pts).
//
// Several edits are laid end to end, a sample being decoded again for each edit
// that covers it. ffmpeg also ignores the media rate of an edit; a rate other than
// 1 is refused here instead, as are the shapes listed at ErrEditList.
func editRuns(tc container.TrackConfig, samples []container.Sample) ([]editRun, error) {
	el := tc.EditList
	if el.MovieTimescale == 0 || tc.Timescale == 0 {
		return nil, fmt.Errorf("%w: movie timescale %d, track timescale %d", ErrEditList, el.MovieTimescale, tc.Timescale)
	}
	n := len(samples)
	dts := make([]int64, n+1)
	hasCTTS := false
	for i, s := range samples {
		dts[i+1] = dts[i] + int64(s.Duration)
		hasCTTS = hasCTTS || s.CompositionOffset != 0
	}
	cts := func(i int) int64 { return dts[i] + int64(samples[i].CompositionOffset) }

	var (
		runs     []editRun
		counter  int64 // where the next edit starts in the presentation
		empty    int64 // the leading empty edits
		minPTS   = int64(-1)
		started  bool
		work     int
		maxWork  = maxEditWork * (n + 1)
		timeline = func(i int, e container.Edit, why string) error {
			return fmt.Errorf("%w: edit %d of %d (media time %d, duration %d, rate %#x): %s",
				ErrEditList, i+1, len(el.Edits), e.MediaTime, e.SegmentDuration, e.Rate, why)
		}
	)
	for i, e := range el.Edits {
		if e.SegmentDuration > math.MaxInt64 {
			return nil, timeline(i, e, "the duration does not fit")
		}
		dur := rescale(int64(e.SegmentDuration), int64(tc.Timescale), int64(el.MovieTimescale))
		if e.MediaTime != container.EmptyEdit && dur > math.MaxInt64-e.MediaTime {
			dur = 0 // get_edit_list_entry: a duration that overflows is none
		}
		if dur > math.MaxInt64-counter {
			return nil, timeline(i, e, "the durations add up beyond what fits")
		}
		entry := counter
		counter += dur
		if !started && e.MediaTime == container.EmptyEdit {
			empty += dur
			continue
		}
		started = true
		switch {
		case e.MediaTime == container.EmptyEdit:
			return nil, timeline(i, e, "an empty edit after an edit that shows media")
		case e.MediaTime < 0:
			return nil, timeline(i, e, "the media time is before the start of the track")
		case e.Rate != container.NormalRate:
			return nil, timeline(i, e, "the media rate is not 1")
		case e.SegmentDuration == 0:
			return nil, timeline(i, e, "the edit lasts to the end of the media, which ffmpeg does not present")
		}
		lo, hi := e.MediaTime, e.MediaTime+dur
		start := startSample(samples, dts, hasCTTS, lo)
		run := editRun{first: start, end: n, dts: dts[start], lo: lo, hi: hi}
		firstIn, keyAfter := -1, false
		for k := start; k < n; k++ {
			if work++; work > maxWork {
				return nil, fmt.Errorf("%w: the edit list makes the decoder walk more than %d times the %d samples of the track",
					ErrOverLimit, maxEditWork, n)
			}
			c := cts(k)
			if c >= lo && c < hi {
				if firstIn < 0 {
					firstIn = k
				}
				v := entry + dts[k] - dts[firstIn] + int64(samples[k].CompositionOffset)
				if minPTS < 0 {
					minPTS = v
				} else {
					minPTS = min(minPTS, v)
				}
			}
			// The last sample lasts as long as the edit, as ffmpeg takes it.
			d := dur
			if k+1 < n {
				d = dts[k+1] - dts[k]
			}
			if c+d >= hi && samples[k].Sync {
				// With composition offsets a trailing B-frame of the edit can follow
				// the first keyframe after it: wait for one more.
				if hasCTTS && !keyAfter {
					keyAfter = true
					continue
				}
				run.end = k + 1
				break
			}
		}
		if firstIn >= 0 {
			run.shift = entry - dts[firstIn]
			runs = append(runs, run)
		}
	}
	minPTS -= empty
	if minPTS > 0 {
		for i := range runs {
			runs[i].shift -= minPTS
		}
	}
	return runs, nil
}

// startSample is the first sample to decode for an edit that starts at media
// time lo: find_prev_closest_index of mov.c. It is the last keyframe whose
// decoding time is at or before lo and, when the track has composition offsets,
// whose composition time is too; failing that the last sample of any kind that
// qualifies, and failing that the first.
func startSample(samples []container.Sample, dts []int64, hasCTTS bool, lo int64) int {
	for _, anyFrame := range []bool{false, true} {
		// The last sample decoding at or before lo.
		m := sort.Search(len(samples), func(i int) bool { return dts[i] > lo }) - 1
		for !anyFrame && m >= 0 && !samples[m].Sync {
			m--
		}
		for i := m; i > 0 && dts[i] == dts[i-1]; i-- {
			if anyFrame || samples[i-1].Sync {
				m = i - 1
			}
		}
		for hasCTTS && m >= 0 && !(dts[m]+int64(samples[m].CompositionOffset) <= lo && samples[m].Sync) {
			m--
		}
		if m >= 0 {
			return m
		}
	}
	return 0
}
