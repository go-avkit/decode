// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"math"

	"github.com/go-avkit/avkit/container"
)

// frameSizer is implemented by a codec whose output frame has a fixed size at its
// current rate (AAC: 1024 samples, 2048 once SBR is on).
type frameSizer interface {
	frameSamples() int
}

// codecFrame is the samples per output frame of c, or 0 when it does not say.
func codecFrame(c audioCodec) int {
	if f, ok := c.(frameSizer); ok {
		return f.frameSamples()
	}
	return 0
}

// mp4aEnd says where an MP4 AAC track ends: how many of its samples are decoded, and how
// many samples at the rate r are dropped from the end of what they decode to. frame is the
// samples of a decoded frame at r (0 if unknown).
//
// The packets are the ones that ffmpeg's mov demuxer keeps (libavformat/mov.c 4625): all
// of them, or with an edit list that has an end, up to and including the first that
// reaches the end of the non-empty edits, a packet being kept whole. Every kept packet
// decodes to a whole frame whatever its stts duration says; only the last one is cut, as
// libavformat/demux.c 1535-1543 does with the padding it computes from timestamps
// (libavformat/mov.c 11697-11713 sets where the stream ends): the frame, or the last
// packet's duration if longer, less what the stream's duration (the media header's, or
// the edit list's total, empty edits included, if that is less) leaves of the last packet,
// measured from that packet's own start. So an encoder's short last packet (1922 where the
// others have 2048) is cut by 126, but a track whose durations do not add up to its
// packets' whole frames (a timescale at the core rate and a duration or two lost along the
// way) loses nothing: ffmpeg counts the frames it decodes, not the durations.
func mp4aEnd(tc container.TrackConfig, samples []container.Sample, mediaDur uint64, r, frame int) (packets int, pad int64) {
	if len(samples) == 0 {
		return 0, 0
	}
	ts := int64(tc.Timescale)
	var all, start, nonEmpty, empty int64 // in track timescale units
	started, open := false, false
	if el := tc.EditList; el != nil && el.MovieTimescale != 0 {
		for _, e := range el.Edits {
			d := rescale(int64(min(e.SegmentDuration, math.MaxInt64)), ts, int64(el.MovieTimescale))
			open = open || e.SegmentDuration == 0
			all += d
			if e.MediaTime == container.EmptyEdit && !started {
				empty += d // the presentation timeline starts after the empty edits
			}
			if e.MediaTime != container.EmptyEdit {
				if !started {
					start, started = e.MediaTime, true
				}
				nonEmpty += d
			}
		}
	}
	var sum int64
	packets = len(samples)
	last := int64(samples[len(samples)-1].Duration)
	for i, s := range samples {
		sum += int64(s.Duration)
		if started && !open && sum >= start+nonEmpty {
			packets, last = i+1, int64(s.Duration)
			break
		}
	}
	// the stream's duration, in track units, or 0 for none
	dur := int64(min(mediaDur, math.MaxInt64))
	if started && !open && all > 0 && (dur == 0 || all < dur) {
		dur = all
	}
	toR := func(v int64) int64 { return rescale(v, int64(r), ts) }
	// the last kept packet's start on the stream's timeline: after the empty edits that
	// lead (mov.c 4452, 4460: they count in the timestamps and in the duration)
	lastPTS := max(sum-last-start+empty, 0)
	kept := last // what the stream's duration leaves of the last packet
	if dur > 0 {
		kept = min(last, max(dur-lastPTS, 0))
	}
	span := max(int64(frame), toR(last))
	if dur > 0 && toR(lastPTS) >= toR(dur) {
		return packets, 0 // the last packet starts at or after the end: ffmpeg cuts nothing
	}
	return packets, span - toR(kept) // between 0 and span: kept is between 0 and last
}
