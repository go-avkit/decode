// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/go-avkit/avkit/container"
	"github.com/jfreymuth/vorbis"
)

// Vorbis I through github.com/jfreymuth/vorbis (MIT, pure Go), the packet decoder
// under jfreymuth/oggvorbis. The library trusts its headers: a codebook that
// states 2^24 entries makes it allocate gigabytes, one that states none makes it
// allocate 2^32 nodes, and a malformed packet panics. The identification header
// and the codebook section of the setup header are therefore checked here, against
// the limits below, before the library sees them, and every call into it recovers.

func init() {
	audioDecoders["vorb"] = newVorbis
}

// Limits on what the setup header may ask for. A codebook of libvorbis has a few
// thousand entries at most.
const (
	maxVorbisEntries = 1 << 18
	maxVorbisValues  = 1 << 22 // entries times dimensions of a codebook with a lookup table
	maxVorbisBooks   = 256
)

type vorbisDecoder struct {
	dec      *vorbis.Decoder
	channels int
	rate     int
	order    []int // order[i] is the Vorbis channel that is output channel i
	layout   ChannelLayout
}

// vorbisOrder is, for 1 to 8 channels, where each channel of the WAVE order is in
// Vorbis's (Vorbis I 4.3.9), and the speakers they are. ffmpeg's decoder reorders
// the same way.
var vorbisOrder = [...]struct {
	order  []int
	layout ChannelLayout
}{
	1: {[]int{0}, LayoutMono},
	2: {[]int{0, 1}, LayoutStereo},
	3: {[]int{0, 2, 1}, Layout3},
	4: {[]int{0, 1, 2, 3}, LayoutQuad},
	5: {[]int{0, 2, 1, 3, 4}, Layout5Back},
	6: {[]int{0, 2, 1, 5, 3, 4}, Layout51Back},
	7: {[]int{0, 2, 1, 6, 5, 3, 4}, Layout61},
	8: {[]int{0, 2, 1, 7, 5, 6, 3, 4}, Layout71},
}

// xiphSplit cuts the three header packets out of the Xiph lacing Matroska's
// CodecPrivate and avkit's Ogg reader use: a count less one, the sizes of all but
// the last packet, then the packets.
func xiphSplit(b []byte) (ident, comment, setup []byte, ok bool) {
	if len(b) < 1 || b[0] != 2 {
		return nil, nil, nil, false
	}
	b = b[1:]
	var sizes [2]int
	for i := range sizes {
		for {
			if len(b) == 0 {
				return nil, nil, nil, false
			}
			c := b[0]
			b = b[1:]
			sizes[i] += int(c)
			if c < 255 {
				break
			}
		}
	}
	if sizes[0]+sizes[1] > len(b) {
		return nil, nil, nil, false
	}
	return b[:sizes[0]], b[sizes[0] : sizes[0]+sizes[1]], b[sizes[0]+sizes[1]:], true
}

func newVorbis(tc container.TrackConfig, cfg AudioConfig) (audioCodec, error) {
	ident, _, setup, ok := xiphSplit(tc.CodecConfig)
	if !ok {
		return nil, fmt.Errorf("%w: a Vorbis configuration is three Xiph-laced header packets", ErrAudioConfig)
	}
	// Vorbis I 4.2.2: "\x01vorbis", version, channels, rate, three bitrates, the
	// block sizes as two exponents, the framing bit.
	if len(ident) != 30 || ident[0] != 1 || string(ident[1:7]) != "vorbis" || binary.LittleEndian.Uint32(ident[7:]) != 0 ||
		ident[29]&1 == 0 {
		return nil, fmt.Errorf("%w: not a Vorbis identification header", ErrAudioConfig)
	}
	d := &vorbisDecoder{channels: int(ident[11]), rate: int(binary.LittleEndian.Uint32(ident[12:]))}
	short, long := int(ident[28]&15), int(ident[28]>>4)
	switch {
	case d.channels == 0 || d.rate == 0 || d.rate > math.MaxInt32:
		return nil, fmt.Errorf("%w: %d channels at %d Hz", ErrAudioConfig, d.channels, d.rate)
	case short < 6 || long > 13 || short > long:
		return nil, fmt.Errorf("%w: block sizes 2^%d and 2^%d, Vorbis I 4.2.2 allows 2^6 to 2^13 with the short one first", ErrAudioConfig, short, long)
	case d.channels > cfg.channels():
		return nil, fmt.Errorf("%w: %d channels, the limit is %d", ErrOverLimit, d.channels, cfg.channels())
	case 1<<long/2 > cfg.block():
		return nil, fmt.Errorf("%w: blocks of %d samples, the limit is %d", ErrOverLimit, 1<<long/2, cfg.block())
	}
	if err := checkVorbisCodebooks(setup); err != nil {
		return nil, err
	}
	d.dec = &vorbis.Decoder{}
	// The comment header is not read: the library allocates what it states.
	if err := guard(func() error {
		if err := d.dec.ReadHeader(ident); err != nil {
			return err
		}
		return d.dec.ReadHeader(setup)
	}); err != nil || !d.dec.HeadersRead() {
		return nil, fmt.Errorf("%w: the Vorbis headers are refused: %v", ErrAudioConfig, err)
	}
	if d.dec.Channels() != d.channels {
		return nil, fmt.Errorf("%w: %d channels read from the headers, %d stated", ErrAudioConfig, d.dec.Channels(), d.channels)
	}
	if d.channels < len(vorbisOrder) {
		d.order, d.layout = vorbisOrder[d.channels].order, vorbisOrder[d.channels].layout
	} else {
		// Above eight channels Vorbis leaves the layout to the application.
		d.order = make([]int, d.channels)
		for i := range d.order {
			d.order[i] = i
		}
	}
	return d, nil
}

// guard runs f and turns a panic of the library into an error.
func guard(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("decoder panic: %v", r)
		}
	}()
	return f()
}

// lsbBits reads a Vorbis header: bits least significant first (Vorbis I 2.1.4).
type lsbBits struct {
	b    []byte
	pos  int // in bits
	over bool
}

func (r *lsbBits) read(n int) uint32 {
	var v uint32
	for i := range n {
		if r.pos>>3 >= len(r.b) {
			r.over = true
			return 0
		}
		v |= uint32(r.b[r.pos>>3]>>(r.pos&7)&1) << i
		r.pos++
	}
	return v
}

// checkVorbisCodebooks walks the codebook section of a setup header (Vorbis I
// 4.2.4, 3.2.1) and refuses what would make the library allocate more than the
// limits or read past the header.
func checkVorbisCodebooks(setup []byte) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: Vorbis setup header: "+format, append([]any{ErrAudioConfig}, a...)...)
	}
	if len(setup) < 8 || setup[0] != 5 || string(setup[1:7]) != "vorbis" {
		return bad("not a setup header")
	}
	r := &lsbBits{b: setup[7:]}
	books := int(r.read(8)) + 1
	if books > maxVorbisBooks {
		return bad("%d codebooks", books)
	}
	for i := range books {
		if r.read(24) != 0x564342 {
			return bad("codebook %d has no sync pattern", i)
		}
		dim, entries := int(r.read(16)), int(r.read(24))
		if dim == 0 || entries == 0 || entries > maxVorbisEntries {
			return bad("codebook %d of %d entries and %d dimensions", i, entries, dim)
		}
		if r.read(1) == 1 { // ordered
			length := int(r.read(5)) + 1
			for cur := 0; cur < entries; length++ {
				num := int(r.read(bitLen(entries - cur)))
				if length > 32 || num > entries-cur || r.over {
					return bad("codebook %d: entry lengths run out of bounds", i)
				}
				cur += num
			}
		} else {
			sparse := r.read(1) == 1
			for range entries {
				if !sparse || r.read(1) == 1 {
					r.read(5)
				}
				if r.over {
					return bad("codebook %d is cut short", i)
				}
			}
		}
		switch lookup := r.read(4); {
		case lookup == 0:
		case lookup > 2:
			return bad("codebook %d: lookup type %d", i, lookup)
		default:
			r.read(32)
			r.read(32)
			valueBits := int(r.read(4)) + 1
			r.read(1)
			count := entries * dim
			if lookup == 1 {
				count = int(math.Floor(math.Pow(float64(entries), 1/float64(dim))))
			}
			if entries*dim > maxVorbisValues {
				return bad("codebook %d: %d lookup values", i, entries*dim)
			}
			r.pos += count * valueBits
		}
		if r.over || r.pos>>3 > len(r.b) {
			return bad("codebook %d is cut short", i)
		}
	}
	return nil
}

// bitLen is the number of bits n takes, Vorbis I's ilog.
func bitLen(n int) int {
	b := 0
	for ; n > 0; n >>= 1 {
		b++
	}
	return b
}

func (d *vorbisDecoder) priming() int    { return 0 }
func (d *vorbisDecoder) sampleRate() int { return d.rate }
func (d *vorbisDecoder) flush() ([]Audio, error) {
	return nil, nil
}

// end: ffmpeg does not cut the last packet of an Ogg Vorbis stream to the granule
// position (avkit states it as a shorter last sample duration, and measured on the
// fixtures ffmpeg delivers the whole packet), so the output is every sample the
// packets decode to, as ffmpeg's is.
func (d *vorbisDecoder) end(container.TrackConfig, []container.Sample) int64 { return -1 }

func (d *vorbisDecoder) decode(data []byte) ([]Audio, error) {
	if len(data) == 0 || data[0]&1 != 0 {
		return nil, fmt.Errorf("%w: not a Vorbis audio packet", ErrAudioSample)
	}
	var pcm []float32
	err := guard(func() (err error) {
		pcm, err = d.dec.Decode(data)
		return err
	})
	if err != nil {
		// A panic leaves the overlap half written: start the next packet afresh.
		d.dec.Clear()
		return nil, fmt.Errorf("%w: %v", ErrAudioSample, err)
	}
	if len(pcm) == 0 {
		return nil, nil
	}
	n := len(pcm) / d.channels
	a := Audio{Format: SampleF32, SampleRate: d.rate, Channels: d.channels, Layout: d.layout, Data: make([]byte, len(pcm)*4)}
	for i := range n {
		for ch, src := range d.order {
			binary.LittleEndian.PutUint32(a.Data[(i*d.channels+ch)*4:], math.Float32bits(pcm[i*d.channels+src]))
		}
	}
	return []Audio{a}, nil
}
