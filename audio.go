// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"time"

	"github.com/go-avkit/avkit/container"
)

// The audio half of the package mirrors the video half: an AudioDecoder is built
// from a container.TrackConfig (NewAudioTrack), fed one container.Sample at a
// time (DecodeSample) and drained (Flush), or a whole track is decoded at once
// (DecodeAudioTrack). What comes out is Audio: interleaved PCM with its sample
// rate, channel count and layout, sample format and presentation time.

// ErrAudioConfig means the codec configuration of an audio track (a FLAC
// STREAMINFO, a Vorbis or Opus header) is malformed or disagrees with the track.
var ErrAudioConfig = errors.New("decode: invalid audio configuration")

// ErrAudioSample means an audio sample is not a well formed packet of its codec.
var ErrAudioSample = errors.New("decode: invalid audio sample")

// ErrAudioUnsupported means the track's codec is one this package does not decode,
// or the stream uses a feature of it that the decoder behind it does not.
var ErrAudioUnsupported = errors.New("decode: unsupported audio codec")

// SampleFormat is the format of the samples of an Audio buffer. All are
// little-endian and interleaved, which is ffmpeg's s16le, s32le and f32le.
type SampleFormat uint8

// The sample formats a decoder produces.
const (
	// SampleS16 is signed 16-bit integer samples.
	SampleS16 SampleFormat = iota + 1
	// SampleS32 is signed 32-bit integer samples. A source of fewer bits is
	// left-justified: a 24-bit FLAC sample is shifted up by eight, as ffmpeg does.
	SampleS32
	// SampleF32 is IEEE 754 single precision samples, nominally in [-1, 1].
	SampleF32
)

// Size returns the bytes one sample takes, 0 for a format that is not one of the
// constants above.
func (f SampleFormat) Size() int {
	switch f {
	case SampleS16:
		return 2
	case SampleS32, SampleF32:
		return 4
	}
	return 0
}

// String names the format as ffmpeg does.
func (f SampleFormat) String() string {
	switch f {
	case SampleS16:
		return "s16le"
	case SampleS32:
		return "s32le"
	case SampleF32:
		return "f32le"
	}
	return fmt.Sprintf("SampleFormat(%d)", uint8(f))
}

// ChannelLayout is a set of speaker positions, one bit per position in the order
// of the Microsoft WAVEFORMATEXTENSIBLE channel mask (and ffmpeg's AV_CH_*). The
// channels of an Audio buffer are interleaved in the order of the bits, lowest
// first, which is also the order FLAC and WAVE use. Vorbis, Opus and MPEG audio
// state a different order for some layouts; the decoders here reorder to this one.
type ChannelLayout uint32

// Speaker positions.
const (
	FrontLeft ChannelLayout = 1 << iota
	FrontRight
	FrontCenter
	LowFrequency
	BackLeft
	BackRight
	FrontLeftOfCenter
	FrontRightOfCenter
	BackCenter
	SideLeft
	SideRight
)

// Common layouts.
const (
	LayoutMono   = FrontCenter
	LayoutStereo = FrontLeft | FrontRight
	Layout3      = LayoutStereo | FrontCenter
	LayoutQuad   = LayoutStereo | BackLeft | BackRight
	Layout5      = Layout3 | SideLeft | SideRight
	Layout51     = Layout5 | LowFrequency
	Layout61     = Layout3 | LowFrequency | BackCenter | SideLeft | SideRight
	Layout71     = Layout51 | BackLeft | BackRight
)

// Channels returns how many speaker positions the layout names.
func (l ChannelLayout) Channels() int { return bits.OnesCount32(uint32(l)) }

// Audio is one buffer of decoded PCM.
type Audio struct {
	// Data holds the samples, interleaved: sample 0 of every channel, then sample
	// 1 of every channel, and so on. A buffer owns its bytes.
	Data []byte
	// Format is the sample format of Data.
	Format SampleFormat
	// SampleRate is in Hz: the rate the decoder outputs, which for Opus is 48000
	// whatever rate the track was recorded at.
	SampleRate int
	// Channels is the number of interleaved channels.
	Channels int
	// Layout names the speakers when the codec does; it is zero for a stream that
	// numbers its channels without naming them.
	Layout ChannelLayout
	// PTS is the presentation time of the first sample, in samples of
	// SampleRate: a buffer starting at the first presented sample has PTS 0. The
	// samples an encoder's priming put ahead of it are not delivered, and the
	// padding at the end of the track is not either.
	PTS int64
}

// Samples returns how many samples each channel of the buffer holds.
func (a Audio) Samples() int {
	if a.Channels <= 0 || a.Format.Size() == 0 {
		return 0
	}
	return len(a.Data) / (a.Channels * a.Format.Size())
}

// Duration returns the buffer's length.
func (a Audio) Duration() time.Duration {
	if a.SampleRate <= 0 {
		return 0
	}
	return time.Duration(int64(a.Samples()) * int64(time.Second) / int64(a.SampleRate))
}

// Default audio limits. A stream is untrusted input: what it states about its
// block size and channel count sizes the buffers a decoder allocates.
const (
	// DefaultMaxSampleBytes is shared with video: the largest coded sample.
	// DefaultMaxAudioChannels is eight, the most FLAC and MPEG audio can have;
	// Vorbis and Opus allow more and are bounded by it.
	DefaultMaxAudioChannels = 8
	// DefaultMaxBlockSamples is 65535, the largest block a FLAC frame states, per
	// channel: far above any other codec here (Opus 5760, Vorbis 8192, MP3 1152).
	DefaultMaxBlockSamples = 65535
	// DefaultMaxAudioOutput is 128 MiB, the most PCM one call to DecodeAudioTrack
	// will return.
	DefaultMaxAudioOutput = 128 << 20
)

// AudioConfig bounds what a stream may make an audio decoder allocate. A zero
// field takes its default.
type AudioConfig struct {
	// MaxChannels is the largest channel count accepted.
	MaxChannels int
	// MaxBlockSamples is the largest block a codec may decode at once, in
	// samples per channel. It is checked before the block is allocated.
	MaxBlockSamples int
	// MaxSampleBytes is the largest coded sample accepted.
	MaxSampleBytes int
	// MaxOutputBytes bounds the PCM DecodeAudioTrack returns.
	MaxOutputBytes int
}

func (c AudioConfig) channels() int {
	if c.MaxChannels > 0 {
		return c.MaxChannels
	}
	return DefaultMaxAudioChannels
}

func (c AudioConfig) block() int {
	if c.MaxBlockSamples > 0 {
		return c.MaxBlockSamples
	}
	return DefaultMaxBlockSamples
}

func (c AudioConfig) sampleBytes() int {
	if c.MaxSampleBytes > 0 {
		return c.MaxSampleBytes
	}
	return DefaultMaxSampleBytes
}

func (c AudioConfig) output() int {
	if c.MaxOutputBytes > 0 {
		return c.MaxOutputBytes
	}
	return DefaultMaxAudioOutput
}

// audioCodec is what each wrapped library is adapted to: one coded sample in, the
// PCM it decodes to out, untrimmed and without a presentation time.
type audioCodec interface {
	// decode decodes one container sample.
	decode(data []byte) ([]Audio, error)
	// flush returns what the codec still holds.
	flush() ([]Audio, error)
	// priming is how many samples, at the output rate, the codec's own header
	// says to drop from the start of the stream (Opus pre-skip); zero for the
	// codecs whose header says nothing.
	priming() int
	// sampleRate is the sample rate the codec outputs, in Hz.
	sampleRate() int
}

// AudioDecoder decodes one audio track, sample by sample. It is not safe for
// concurrent use.
type AudioDecoder struct {
	codec  audioCodec
	cfg    AudioConfig
	skip   int64 // samples still to drop from the start
	pos    int64 // presentation position of the next sample delivered
	closed bool
}

// audioDecoders maps a sample entry (TrackConfig.Codec) to its constructor. Each
// codec file registers itself.
var audioDecoders = map[string]func(container.TrackConfig, AudioConfig) (audioCodec, error){}

// NewAudioTrack returns a decoder for an audio track of a demuxed file. The codec
// is named by tc.Codec ("fLaC" for FLAC); its configuration comes from
// tc.CodecConfig. The start of the track is trimmed as the track states it: its
// edit list, its StartDelay or the pre-skip of Opus, whichever the container
// used, which is what ffmpeg does.
func NewAudioTrack(tc container.TrackConfig, cfg AudioConfig) (*AudioDecoder, error) {
	mk, ok := audioDecoders[tc.Codec]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrAudioUnsupported, tc.Codec)
	}
	c, err := mk(tc, cfg)
	if err != nil {
		return nil, err
	}
	skip := int64(c.priming())
	if skip == 0 {
		skip = startSkip(tc, c.sampleRate())
	}
	return &AudioDecoder{codec: c, cfg: cfg, skip: skip}, nil
}

// startSkip is how many samples of the track's output rate the container drops
// from its start: the media time of the first edit of an edit list, or the
// StartDelay.
func startSkip(tc container.TrackConfig, rate int) int64 {
	if el := tc.EditList; el != nil {
		return editSkip(el, tc, rate)
	}
	return rescale(int64(tc.StartDelay), int64(rate), int64(time.Second))
}

// editSkip reads the media time of the first non-empty edit, which is where the
// presentation starts, in the track's timescale, and converts it to samples.
func editSkip(el *container.EditList, tc container.TrackConfig, rate int) int64 {
	for _, e := range el.Edits {
		if e.MediaTime != container.EmptyEdit {
			return rescale(e.MediaTime, int64(rate), int64(tc.Timescale))
		}
	}
	return 0
}

// rescale returns v*num/den rounded to the nearest, as ffmpeg's av_rescale does,
// without overflowing: a negative v or a zero den is 0 and a result beyond an
// int64 is the largest one. A container states these numbers and a hostile one
// states them at the limits.
func rescale(v, num, den int64) int64 {
	if v <= 0 || num <= 0 || den <= 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(v), uint64(num))
	lo, carry := bits.Add64(lo, uint64(den)/2, 0)
	hi += carry
	if hi >= uint64(den) {
		return math.MaxInt64
	}
	q, _ := bits.Div64(hi, lo, uint64(den))
	return int64(min(q, math.MaxInt64))
}

// DecodeSample decodes one container sample and returns the PCM it yields, after
// the start of the track has been trimmed.
func (d *AudioDecoder) DecodeSample(s container.Sample) ([]Audio, error) {
	if d.closed {
		return nil, ErrClosed
	}
	if len(s.Data) > d.cfg.sampleBytes() {
		return nil, fmt.Errorf("%w: sample of %d bytes, the limit is %d", ErrOverLimit, len(s.Data), d.cfg.sampleBytes())
	}
	out, err := d.codec.decode(s.Data)
	return d.place(out), err
}

// Flush returns what the decoder still holds. Call it at the end of the stream.
func (d *AudioDecoder) Flush() ([]Audio, error) {
	if d.closed {
		return nil, ErrClosed
	}
	out, err := d.codec.flush()
	return d.place(out), err
}

// Close releases the decoder.
func (d *AudioDecoder) Close() error {
	d.closed = true
	return nil
}

// place drops the samples of the start that are not presented and numbers the rest.
func (d *AudioDecoder) place(in []Audio) []Audio {
	out := in[:0]
	for _, a := range in {
		n := int64(a.Samples())
		if d.skip > 0 {
			cut := min(d.skip, n)
			d.skip -= cut
			a.Data = a.Data[int(cut)*a.Channels*a.Format.Size():]
			n -= cut
		}
		if n == 0 {
			continue
		}
		a.PTS = d.pos
		d.pos += n
		out = append(out, a)
	}
	return out
}

// ErrNoAudio means a file has no audio track.
var ErrNoAudio = errors.New("decode: no audio track")

// FirstAudioTrack returns the ID of the first audio track of a demuxed file.
func FirstAudioTrack(r *container.Reader) (uint32, error) {
	for _, tr := range r.File().Tracks {
		if tr.Kind == container.Audio {
			return tr.ID, nil
		}
	}
	return 0, ErrNoAudio
}

// DecodeAudioTrack decodes every sample of one audio track of a demuxed file and
// returns the PCM in presentation order, with the track's start and end trimmed as
// its container states them (edit list, StartDelay, Opus pre-skip, DiscardPadding):
// the sample count is what ffmpeg delivers.
func DecodeAudioTrack(r *container.Reader, trackID uint32, cfg AudioConfig) ([]Audio, error) {
	tc, err := r.TrackConfig(trackID)
	var samples []container.Sample
	if err == nil {
		samples, err = r.Samples(trackID)
	}
	if err != nil {
		return nil, err
	}
	return decodeAudio(tc, samples, cfg)
}

// decodeAudio is DecodeAudioTrack for a track already read.
func decodeAudio(tc container.TrackConfig, samples []container.Sample, cfg AudioConfig) ([]Audio, error) {
	d, err := NewAudioTrack(tc, cfg)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	limit := presentedSamples(tc, d.codec.sampleRate())
	var out []Audio
	var total int64
	emit := func(fs []Audio) error {
		for _, a := range fs {
			if limit >= 0 && a.PTS+int64(a.Samples()) > limit {
				keep := max(limit-a.PTS, 0)
				a.Data = a.Data[:int(keep)*a.Channels*a.Format.Size()]
				if keep == 0 {
					continue
				}
			}
			total += int64(len(a.Data))
			if total > int64(cfg.output()) {
				return fmt.Errorf("%w: more than %d bytes of PCM", ErrOverLimit, cfg.output())
			}
			out = append(out, a)
		}
		return nil
	}
	for _, s := range samples {
		fs, err := d.DecodeSample(s)
		if err := errors.Join(emit(fs), err); err != nil {
			return out, err
		}
	}
	fs, err := d.Flush()
	return out, errors.Join(emit(fs), err)
}

// presentedSamples is how many samples the track presents, in samples of its
// output rate, or -1 when its container states no end: the summed duration of the
// non-empty edits of its edit list. An edit of duration zero lasts to the end of
// the media (ISO/IEC 14496-12 8.6.6), which states none.
func presentedSamples(tc container.TrackConfig, rate int) int64 {
	el := tc.EditList
	if el == nil || el.MovieTimescale == 0 {
		return -1
	}
	var total int64
	for _, e := range el.Edits {
		switch {
		case e.MediaTime == container.EmptyEdit:
		case e.SegmentDuration == 0 || e.SegmentDuration > math.MaxInt64-uint64(total):
			return -1
		default:
			total += int64(e.SegmentDuration)
		}
	}
	return rescale(total, int64(rate), int64(el.MovieTimescale))
}
