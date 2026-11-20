// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"fmt"
	"iter"

	"github.com/go-avkit/avkit/container"
)

// AVCConfig is an AVCDecoderConfigurationRecord, the content of the avcC box
// (ISO/IEC 14496-15). The standard's text is paywalled and was not opened; the
// layout is that of mp4ff v0.62.0, avc/avcdecoderconfigurationrecord.go, which
// was.
type AVCConfig struct {
	// Profile, Compatibility and Level are AVCProfileIndication,
	// profile_compatibility and AVCLevelIndication, the first three bytes of
	// the SPS after the NAL header.
	Profile, Compatibility, Level uint8
	// LengthSize is the size in bytes of each NAL unit's length prefix:
	// lengthSizeMinusOne + 1: 1, 2 or 4 (3 is reserved).
	LengthSize int
	// SPS and PPS are the parameter sets, as NAL units without a prefix.
	SPS, PPS [][]byte
}

// ParseAVCC parses an AVCDecoderConfigurationRecord. It reads the record only as
// far as the decoder needs: the extension fields a High profile record appends
// (chroma format, bit depths, SPS extensions) are bounds-checked but not kept,
// since the sequence parameter set itself is authoritative.
func ParseAVCC(b []byte) (AVCConfig, error) {
	var c AVCConfig
	if len(b) < 7 {
		return c, fmt.Errorf("%w: avcC of %d bytes, the fixed part is 7", ErrConfig, len(b))
	}
	if b[0] != 1 {
		return c, fmt.Errorf("%w: avcC configurationVersion %d, want 1", ErrConfig, b[0])
	}
	c.Profile, c.Compatibility, c.Level = b[1], b[2], b[3]
	c.LengthSize = int(b[4]&3) + 1
	if c.LengthSize == 3 {
		return c, fmt.Errorf("%w: avcC lengthSizeMinusOne 2 (3-byte lengths) is not allowed", ErrConfig)
	}
	n := int(b[5] & 31)
	pos := 6
	var err error
	if c.SPS, pos, err = avcSets(b, pos, n, "SPS"); err != nil {
		return c, err
	}
	if pos >= len(b) {
		return c, fmt.Errorf("%w: avcC ends before numOfPictureParameterSets", ErrConfig)
	}
	n = int(b[pos])
	if c.PPS, pos, err = avcSets(b, pos+1, n, "PPS"); err != nil {
		return c, err
	}
	switch c.Profile {
	case 66, 77, 88:
	default:
		// chroma_format, bit_depth_luma_minus8, bit_depth_chroma_minus8,
		// numOfSequenceParameterSetExt and the extensions, for every profile
		// but Baseline, Main and Extended (as mp4ff reads it). Absent in
		// records written before the High profiles were added, which is
		// tolerated.
		if pos+4 <= len(b) {
			if _, _, err = avcSets(b, pos+3, int(b[pos+3]), "SPS extension"); err != nil {
				return c, err
			}
		}
	}
	return c, nil
}

// avcSets reads n length-prefixed (16-bit) parameter sets at pos.
func avcSets(b []byte, pos, n int, what string) ([][]byte, int, error) {
	var sets [][]byte
	for range n {
		if pos+2 > len(b) {
			return nil, 0, fmt.Errorf("%w: avcC ends inside a %s length", ErrConfig, what)
		}
		l := int(binary.BigEndian.Uint16(b[pos:]))
		pos += 2
		if l == 0 || l > len(b)-pos {
			return nil, 0, fmt.Errorf("%w: avcC %s of %d bytes with %d left", ErrConfig, what, l, len(b)-pos)
		}
		sets = append(sets, b[pos:pos+l])
		pos += l
	}
	return sets, pos, nil
}

// NewAVCC returns a decoder configured by an avcC record, as MP4 stores it.
func NewAVCC(avcC []byte, cfg Config) (*Decoder, error) {
	c, err := ParseAVCC(avcC)
	if err != nil {
		return nil, err
	}
	return newWithSets(cfg, c.LengthSize, c.SPS, c.PPS)
}

// isAVC reports whether a container track's sample entry names H.264. A track
// that states no codec at all is taken as H.264, as it always was.
func isAVC(tc container.TrackConfig) bool {
	switch tc.Codec {
	case "", "avc1", "avc2", "avc3", "avc4":
		return true
	}
	return false
}

// NewTrack returns a decoder for an avkit container track: an HEVC decoder for a
// track whose sample entry is hvc1 or hev1, an H.264 decoder for avc1 to avc4 (or
// no codec stated). Any other sample entry is refused by name with a *CodecError.
// Samples are length-prefixed with four-byte lengths, which is what
// container.Reader hands over for MP4 and Matroska. The track's parameter sets
// are decoded first.
func NewTrack(tc container.TrackConfig, cfg Config) (*Decoder, error) {
	switch {
	case isHEVC(tc):
		return newHEVCWithSets(cfg, 4, tc.VPS, tc.SPS, tc.PPS)
	case isAVC(tc):
		return newWithSets(cfg, 4, tc.SPS, tc.PPS)
	}
	return nil, &CodecError{Codec: tc.Codec}
}

func newWithSets(cfg Config, lengthSize int, sps, pps [][]byte) (*Decoder, error) {
	d := newDecoder(cfg, lengthSize)
	out := d.begin()
	for _, set := range append(append([][]byte(nil), sps...), pps...) {
		if len(set) == 0 {
			return nil, fmt.Errorf("%w: empty parameter set", ErrConfig)
		}
		out = d.appendNAL(out, set)
	}
	d.annexB, d.started = out, true
	if _, err := d.run(out); err != nil {
		return nil, fmt.Errorf("decode: parameter set: %w", err)
	}
	return d, nil
}

var startCode = []byte{0, 0, 0, 1}

// appendNAL adds one NAL unit to an Annex B buffer. Every unit is followed by
// the next start code rather than preceded by it, so that the decoder, which
// holds back the last unit of what it has been given until the code that ends
// it arrives, acts on each unit at once: a parameter set that breaks a limit
// is refused in the call that carried it, not in the next one.
func (d *Decoder) appendNAL(out, nal []byte) []byte {
	out = append(out, nal...)
	return append(out, startCode...)
}

// begin returns the buffer a stream's first call starts from: the very first
// start code, which no unit's trailer supplies. It must be called only when the
// buffer is certain to be handed over, or the code would be lost.
func (d *Decoder) begin() []byte {
	out := d.annexB[:0]
	if !d.started {
		out = append(out, startCode...)
	}
	return out
}

// splitSample cuts one length-prefixed sample into its NAL units. Every length is
// checked against what is left of the sample before anything is returned, so a
// malformed sample yields no units at all.
func splitSample(data []byte, lengthSize int) ([][]byte, error) {
	var units [][]byte
	for len(data) > 0 {
		if len(data) < lengthSize {
			return nil, fmt.Errorf("%w: %d bytes left, a length is %d", ErrSample, len(data), lengthSize)
		}
		var l uint32
		for _, c := range data[:lengthSize] {
			l = l<<8 | uint32(c)
		}
		data = data[lengthSize:]
		if l == 0 || uint64(l) > uint64(len(data)) {
			return nil, fmt.Errorf("%w: NAL unit of %d bytes with %d left", ErrSample, l, len(data))
		}
		units = append(units, data[:l])
		data = data[l:]
	}
	return units, nil
}

// toAnnexB rewrites one length-prefixed sample as Annex B.
func (d *Decoder) toAnnexB(data []byte) ([]byte, error) {
	units, err := splitSample(data, d.lengthSize)
	if err != nil {
		return nil, err
	}
	out := d.begin()
	for _, u := range units {
		out = d.appendNAL(out, u)
	}
	d.annexB, d.started = out, true
	return out, nil
}

// DecodeTrack decodes every sample of one video track of a demuxed file and
// returns the pictures in display order with their presentation times in the
// track's timescale. It is the whole-file form of NewTrack and DecodeSample.
//
// The track's edit list is applied, as ffmpeg applies it by default (mov.c,
// mov_fix_index): a picture presented before the media time of the edit or at
// its end or after is decoded, for it may be a reference, and not returned, and
// the presentation times are shifted so that the pictures of the edit start at
// the delay the leading empty edits state, or at 0. Several edits are laid end
// to end. An edit list this package does not apply is refused with ErrEditList.
// Config.IgnoreEditList returns every picture at its track time instead.
func DecodeTrack(r *container.Reader, trackID uint32, cfg Config) ([]Frame, error) {
	var out []Frame
	_, err := walkTrack(r, trackID, cfg, func(f Frame) bool {
		out = append(out, f)
		return true
	})
	return out, err
}

// Frames is DecodeTrack as a sequence: the same pictures, in the same order, at
// the same presentation times (the edit list applied or not as Config says, H.264
// and HEVC alike), handed over one at a time as they come out of the decoder
// instead of all together at the end. A track of 1080p pictures held whole is
// 3 MB a picture; ranging over Frames holds the few the decoder still needs, so
// the memory of a long file stays flat.
//
//	for f, err := range decode.Frames(r, id, decode.Config{}) {
//		if err != nil {
//			return err
//		}
//		use(f)
//	}
//
// An error ends the sequence: it comes as the last pair, with a nil Frame,
// after every picture decoded before it (DecodeTrack returns those pictures
// beside its error), and it is what DecodeTrack would return. Breaking out of
// the loop stops the decoding and releases the decoder.
//
// Every Frame is the caller's: it owns its planes and nothing else refers to
// them, so it can be kept past the next iteration, sent to another goroutine or
// appended to a slice, as the Frames of DecodeTrack can. Nothing is reused
// between iterations, which is what makes the default safe; the cost is the
// allocation of a picture per frame, which the garbage collector gives back as
// soon as the caller lets go of it.
//
// It is the shape of an iterator, a [iter.Seq2], and not a callback, because the
// loop body is then ordinary code: break, return and continue work, a defer in
// the caller's function sees them, and the error is a value of the loop and not a
// second result to remember to check after it.
func Frames(r *container.Reader, trackID uint32, cfg Config) iter.Seq2[*Frame, error] {
	return func(yield func(*Frame, error) bool) {
		stopped := false
		_, err := walkTrack(r, trackID, cfg, func(f Frame) bool {
			if !yield(&f, nil) {
				stopped = true
				return false
			}
			return true
		})
		if err != nil && !stopped {
			yield(nil, err)
		}
	}
}

// walkTrack decodes the track and gives every picture to emit, in order. It stops
// when emit returns false, and then reports that it did, or on the first error.
func walkTrack(r *container.Reader, trackID uint32, cfg Config, emit func(Frame) bool) (stopped bool, err error) {
	tc, err := r.TrackConfig(trackID)
	var samples []container.Sample
	if err == nil {
		samples, err = r.Samples(trackID)
	}
	if err != nil {
		return false, err
	}
	if tc = presented(r, tc, samples); cfg.IgnoreEditList || tc.EditList == nil {
		return walkSpan(tc, samples, 0, cfg, nil, emit)
	}
	runs, err := editRuns(tc, samples)
	if err != nil {
		return false, err
	}
	for _, run := range runs {
		stopped, err := walkSpan(tc, samples[run.first:run.end], run.dts, cfg, &run, emit)
		if stopped || err != nil {
			return stopped, err
		}
	}
	return false, nil
}

// walkSpan decodes samples, the first of which decodes at dts, with a decoder of
// its own, and gives the pictures it outputs to emit. With a run only the
// pictures the run presents are given, at their presentation time.
func walkSpan(tc container.TrackConfig, samples []container.Sample, dts int64, cfg Config, run *editRun, emit func(Frame) bool) (stopped bool, err error) {
	d, err := NewTrack(tc, cfg)
	if err != nil {
		return false, err
	}
	defer d.Close()
	keep := func(fs []Frame) bool {
		for _, f := range fs {
			if run != nil {
				if f.PTS < run.lo || f.PTS >= run.hi {
					continue
				}
				f.PTS += run.shift
			}
			if !emit(f) {
				return false
			}
		}
		return true
	}
	for _, s := range samples {
		fs, err := d.DecodeSample(s.Data, dts+int64(s.CompositionOffset))
		if !keep(fs) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		dts += int64(s.Duration)
	}
	fs, err := d.Flush()
	if !keep(fs) {
		return true, nil
	}
	return false, err
}
