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
	// The back variants are what Vorbis names for five and six channels.
	Layout5Back  = Layout3 | BackLeft | BackRight
	Layout51Back = Layout5Back | LowFrequency
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
	// end is how many samples the track presents when its edit list states no
	// end, from what the samples' durations say, or -1 for no limit.
	end(tc container.TrackConfig, samples []container.Sample) int64
}

// AudioDecoder decodes one audio track, sample by sample. It is not safe for
// concurrent use.
type AudioDecoder struct {
	codec  audioCodec
	cfg    AudioConfig
	skip   int64 // samples still to drop from the start
	pos    int64 // presentation position of the next sample delivered
	closed bool
	// tc and rate are what skip was computed from: the codec's output rate when the
	// decoder was made. A codec whose rate is known only from its first frame (AAC
	// with SBR that its configuration does not signal) changes it then, and skip and
	// pos, which are in samples of that rate, follow (setRate).
	tc       container.TrackConfig
	rate     int
	skipEdit bool // skip came from the edit list
	fed      bool // some audio has been placed
	// gapless, when set, is where skip came from: iTunSMPB's encoder delay, or the whole
	// packets the edit list drops if that is more (see gaplessSkip).
	gapless *gaplessStart
}

// gaplessStart is the start of a track whose iTunSMPB gives an encoder delay: both numbers
// are in ticks of the track's timescale.
type gaplessStart struct {
	delay   int64 // the encoder delay
	dropped int64 // the packets the edit list drops whole, which are not decoded for ffmpeg
}

// skipAt is the samples to drop from the start, at the output rate r: the larger of the
// encoder delay and the packets dropped. ffmpeg replaces the edit list's own start (the
// dropped packets and the part of the next one) by the delay, and the packets it drops
// are not decoded, so they count against it: a delay of 1000 with 2048 samples dropped
// drops nothing more, a delay of 3000 drops 3000 from the start of the track.
func (g *gaplessStart) skipAt(tc container.TrackConfig, r int) int64 {
	return max(rescale(g.delay, int64(r), int64(tc.Timescale)), rescale(g.dropped, int64(r), int64(tc.Timescale)))
}

// maxPrimingTicks is the largest encoder delay ffmpeg takes from iTunSMPB, exclusive
// (libavformat/mov.c 5587, n9.0.2 and master alike): a longer one is read as no delay.
const maxPrimingTicks = 16384

// useGapless makes the decoder drop what iTunSMPB says at the start of the track: its
// encoder delay replaces the start the edit list states, as it does in ffmpeg, when it
// is in (0, 16384) ticks. samples are the track's packets, from which the ones the edit
// list drops whole are found.
func (d *AudioDecoder) useGapless(g *container.Gapless, samples []container.Sample) {
	if g == nil || g.Delay == 0 || g.Delay >= maxPrimingTicks || d.tc.Codec != "mp4a" || d.tc.Timescale == 0 {
		return
	}
	d.gapless = &gaplessStart{delay: int64(g.Delay), dropped: droppedTicks(d.tc, samples)}
	d.skip = d.gapless.skipAt(d.tc, d.rate)
	d.skipEdit = false
}

// droppedTicks is the duration, in ticks of the track's timescale, of the packets that
// precede the start of the first non-empty edit of the edit list altogether: ffmpeg
// removes them from the track (a packet that holds the start is kept and cut), and with
// no edit list, or one that starts at 0, there are none.
func droppedTicks(tc container.TrackConfig, samples []container.Sample) int64 {
	el := tc.EditList
	if el == nil {
		return 0
	}
	for _, e := range el.Edits {
		if e.MediaTime == container.EmptyEdit {
			continue
		}
		var cum int64
		for _, s := range samples {
			if cum+int64(s.Duration) > e.MediaTime {
				break
			}
			cum += int64(s.Duration)
		}
		return cum
	}
	return 0
}

// audioDecoders maps a sample entry (TrackConfig.Codec) to its constructor. Each
// codec file registers itself.
var audioDecoders = map[string]func(container.TrackConfig, AudioConfig) (audioCodec, error){}

// NewAudioTrack returns a decoder for an audio track of a demuxed file. The codec
// is named by tc.Codec ("fLaC" for FLAC); its configuration comes from
// tc.CodecConfig. The start of the track is trimmed as the track states it: its
// edit list (MP4, or the one avkit states for an MP3's LAME tag) or the pre-skip
// of Opus, which is what ffmpeg does. StartDelay alone drops nothing: ffmpeg
// treats a Matroska CodecDelay as a timestamp shift.
func NewAudioTrack(tc container.TrackConfig, cfg AudioConfig) (*AudioDecoder, error) {
	mk, ok := audioDecoders[tc.Codec]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrAudioUnsupported, tc.Codec)
	}
	c, err := mk(tc, cfg)
	if err != nil {
		return nil, err
	}
	// A start delay with no edit list is a Matroska CodecDelay (or an encoder's
	// first-packet allowance): ffmpeg shifts the timestamps by it and drops
	// nothing, except for Opus, whose pre-skip is the codec's own (priming).
	skip := int64(c.priming())
	fromEdit := false
	if skip == 0 && tc.EditList != nil {
		skip = editSkip(tc.EditList, tc, c.sampleRate())
		fromEdit = true
	}
	return &AudioDecoder{codec: c, cfg: cfg, skip: skip, tc: tc, rate: c.sampleRate(), skipEdit: fromEdit}, nil
}

// setRate follows a change of the codec's output rate to r: the start still to drop
// and the position are in samples of the rate, so they are converted. Before any
// audio has been placed the start is computed again from the edit list at the new
// rate, exactly; after, what remains of it is scaled.
func (d *AudioDecoder) setRate(r int) {
	if r <= 0 || r == d.rate {
		return
	}
	if d.gapless != nil && !d.fed {
		d.skip = d.gapless.skipAt(d.tc, r)
	} else if d.skipEdit && !d.fed {
		d.skip = editSkip(d.tc.EditList, d.tc, r)
	} else {
		d.skip = rescale(d.skip, int64(r), int64(d.rate))
	}
	d.pos = rescale(d.pos, int64(r), int64(d.rate))
	d.rate = r
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
		d.setRate(a.SampleRate)
		d.fed = true
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
// its container states them (edit list, Opus pre-skip, DiscardPadding). The sample
// count is meant to be what those edit list and gapless metadata define, measured in
// the output rate, and is not yet in one case, a defect of this package: Matroska
// priming and trimming are applied in milliseconds (go-avkit/decode#54).
//
// An MP4's iTunSMPB (container.Metadata.Gapless) is applied to an AAC track the way
// ffmpeg 9.0.2 does (libavformat/mov.c mov_read_custom, libavformat/demux.c), to the
// first audio track of the file, the one the item describes.
//   - Start: an encoder delay in (0, 16384) ticks replaces the start the edit list
//     states; the packets the edit list drops whole still count against it, so the track
//     starts at the larger of the delay and those packets. With no edit list it starts
//     at the delay.
//   - End: when the padding is not 0 and the track's duration (the media header's, or
//     the edit list's total if that is less) is longer, in samples, than both the padding
//     and the original sample count, the track ends at the duration less the padding, in
//     place of the end the last packet's duration gives. Otherwise, as when an edit list
//     already cuts the track to the original count, that end stands.
//   - The three numbers are ticks of the track's timescale and become samples of the
//     output rate as an edit list's start does, so an HE-AAC track whose timescale is
//     the core rate has them doubled.
//
// NewAudioTrack does not apply it, nor to anything but AAC in an MP4.
//
// It equals the count ffmpeg delivers except where ffmpeg is known to differ, which
// are deliberate divergences:
//   - go-avkit/decode#57: an HE-AAC MP4 track whose timescale is the core rate, with an
//     edit list that starts after 0 and no iTunSMPB delay to replace it. The start is in
//     core-rate ticks and is converted to the doubled output rate (1000 ticks at 22050
//     Hz are 2000 samples at 44100 Hz); ffmpeg 9.0.2 drops 1000 of an implicitly
//     signalled one, so it delivers 1000 more, and of an explicitly signalled one drops
//     the packets before the start whole and none of the one that holds it.
//   - go-avkit/decode#20: a fragmented MP4, whose edit list ffmpeg does not apply.
//   - iTunSMPB on the wrong stream: ffmpeg attaches the item to the last stream parsed
//     when it reads it, so in a file whose audio track comes before another track it lands
//     on that one and ffmpeg trims no audio. Here the item trims the first audio track, as
//     it says. (The delay's range, 0 to 16384 ticks, and the padding's conditions are
//     ffmpeg's sanity checks on the values and are kept.)
func DecodeAudioTrack(r *container.Reader, trackID uint32, cfg AudioConfig) ([]Audio, error) {
	tc, err := r.TrackConfig(trackID)
	var samples []container.Sample
	if err == nil {
		samples, err = r.Samples(trackID)
	}
	if err != nil {
		return nil, err
	}
	return decodeAudioGapless(tc, samples, cfg, mediaDuration(r, trackID), gaplessOf(r, trackID))
}

// gaplessOf is the iTunSMPB that applies to a track: the file's, when the track is its
// first audio track, which is the one an Apple encoder's item describes, else nil.
// (ffmpeg attaches it to the last stream parsed when it reads the item, libavformat/mov.c
// mov_read_custom: see DecodeAudioTrack.)
func gaplessOf(r *container.Reader, trackID uint32) *container.Gapless {
	if id, err := FirstAudioTrack(r); err != nil || id != trackID {
		return nil
	}
	return r.File().Metadata.Gapless
}

// mediaDuration is the duration the track's media header states, in its timescale, or 0.
func mediaDuration(r *container.Reader, trackID uint32) uint64 {
	for _, tr := range r.File().Tracks {
		if tr.ID == trackID {
			return tr.Duration
		}
	}
	return 0
}

// decodeAudio is DecodeAudioTrack for a track already read.
func decodeAudio(tc container.TrackConfig, samples []container.Sample, cfg AudioConfig) ([]Audio, error) {
	return decodeAudioDur(tc, samples, cfg, 0)
}

// decodeAudioDur is decodeAudio with the media duration the container states (0 for
// none). An AAC track ends at the least of three things: what its edit list presents, the
// media header's duration, and the sum of its samples' durations less the start the edit
// list drops. The last of them is where the encoder says how much of its last frame is
// padding (a last sample of 1922 where the others have 2048), and ffmpeg trims the last
// packet to it; an edit list in a movie timescale of milliseconds cannot say it exactly.
func decodeAudioDur(tc container.TrackConfig, samples []container.Sample, cfg AudioConfig, mediaDur uint64) ([]Audio, error) {
	return decodeAudioGapless(tc, samples, cfg, mediaDur, nil)
}

// decodeAudioGapless is decodeAudioDur with the iTunSMPB of the file, nil for none.
func decodeAudioGapless(tc container.TrackConfig, samples []container.Sample, cfg AudioConfig, mediaDur uint64, g *container.Gapless) ([]Audio, error) {
	d, err := NewAudioTrack(tc, cfg)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	d.useGapless(g, samples)
	// limit is how many samples the track presents, at the rate r
	limit := func(r int) int64 {
		if tc.Codec == "mp4a" && tc.Timescale > 0 {
			return -1 // by packets and a cut of the last, below
		}
		l := presentedSamples(tc, r)
		if l < 0 {
			l = d.codec.end(tc, samples)
		}
		return l
	}

	mp4 := tc.Codec == "mp4a" && tc.Timescale > 0
	if mp4 {
		n, _ := mp4aEndGapless(tc, nil, samples, mediaDur, d.rate, 0)
		samples = samples[:n]
	}
	lim := limit(d.rate)
	limRate := d.rate
	var out []Audio
	var total int64
	emit := func(fs []Audio) error {
		if d.rate != limRate {
			// the codec's rate changed with its first frame: the length is in samples of
			// the new one
			lim = limit(d.rate)
		}
		limRate = d.rate
		for _, a := range fs {
			if lim >= 0 && a.PTS+int64(a.Samples()) > lim {
				keep := max(lim-a.PTS, 0)

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
	err = errors.Join(emit(fs), err)
	if mp4 {
		_, pad := mp4aEndGapless(tc, g, samples, mediaDur, d.rate, codecFrame(d.codec))
		out = trimTail(out, pad)
	}
	return out, err
}

// trimTail drops the last n samples (per channel) of out.
func trimTail(out []Audio, n int64) []Audio {
	for n > 0 && len(out) > 0 {
		a := &out[len(out)-1]
		have := int64(a.Samples())
		cut := min(n, have)
		a.Data = a.Data[:int(have-cut)*a.Channels*a.Format.Size()]
		n -= cut
		if cut == have {
			out = out[:len(out)-1]
		}
	}
	return out
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
