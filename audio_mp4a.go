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
	return mp4aEndGapless(tc, nil, samples, mediaDur, r, frame)
}

// mp4aEndGapless is mp4aEnd with the file's iTunSMPB, nil for none. When its end padding
// applies (see gaplessPad) the cut it makes is the one returned, in place of the last
// packet's: ffmpeg sets where the stream ends from it first, and the rule for the last
// packet only sets it when it is not set (libavformat/mov.c 11710, n9.0.2).
func mp4aEndGapless(tc container.TrackConfig, g *container.Gapless, samples []container.Sample, mediaDur uint64, r, frame int) (packets int, pad int64) {
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
	if p, ok := gaplessPad(g, samples[:packets], dur, start, empty, r, frame, ts); ok {
		return packets, p
	}
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

// gaplessPad is the cut iTunSMPB's end padding makes at the end of the kept packets, in
// samples at the rate r, and false when it makes none because it does not apply.
//
// It applies when the file states a padding and the stream's duration dur (in ticks; 0 for
// none) is longer, as samples at r, than both the padding and the original sample count.
// The stream then ends where the padding begins, F = duration less padding, and a kept
// packet that reaches past F loses what lies beyond it, at most the whole packet; a packet
// that starts at or after the duration loses nothing (libavformat/demux.c 1536-1543,
// libavformat/mov.c 5590-5600, n9.0.2). The numbers are in ticks of the track's timescale
// ts, like the edit list's; start and empty are the first non-empty edit's media time and
// the empty edits before it, as in mp4aEnd, and place each packet on the stream's timeline.
func gaplessPad(g *container.Gapless, samples []container.Sample, dur, start, empty int64, r, frame int, ts int64) (int64, bool) {
	if g == nil || g.Padding == 0 || dur <= 0 || r <= 0 {
		return 0, false
	}
	conv := func(v uint64) int64 { return rescale(int64(min(v, math.MaxInt64)), int64(r), ts) }
	total, rem, orig := conv(uint64(dur)), conv(g.Padding), conv(g.Samples)
	if total <= rem || total <= orig {
		return 0, false
	}
	first := total - rem
	var cum, pad int64
	for _, s := range samples {
		d := int64(s.Duration)
		at := cum - start // a packet before the edit starts is before 0
		if empty > 0 && at > math.MaxInt64-empty {
			at = math.MaxInt64
		} else {
			at += empty
		}
		var begin int64
		if at < 0 {
			begin = -rescale(-at, int64(r), ts)
		} else {
			begin = rescale(at, int64(r), ts)
		}
		n := max(int64(frame), conv(uint64(d)))
		end := int64(math.MaxInt64) // saturated: a container states these numbers
		if begin <= math.MaxInt64-n {
			end = begin + n
		}
		if n > 0 && end >= first && begin < total {
			cut := min(end-first, n)
			pad = min(pad, math.MaxInt64-cut) + cut
		}
		cum += d
	}
	return pad, true
}
