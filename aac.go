// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/bits"

	"github.com/go-avkit/avkit/container"
)

// AAC-LC, pure Go, written from ISO/IEC 14496-3 and from the order of float32
// operations of FFmpeg's decoder (docs/aac-design.md). Output is float32 in the
// range -1 to 1, as ffmpeg's aac decoder gives.
//
// Not decoded, and refused with ErrAudioUnsupported: HE-AAC (SBR, PS), AAC Main,
// LTP, SSR, 960-sample frames, coupling channels, and channel configurations that
// have no fixed layout (a program_config_element, and 11, 12, 14).

func init() {
	audioDecoders["mp4a"] = newAAC
}

// aacSamplesPerFrame is the length of an AAC-LC frame.
const aacSamplesPerFrame = aacFrameLen

// aacElemMap says what the elements of a channel configuration are, in the order
// they come in a frame, and the speakers their channels feed.
type aacElemMap struct {
	id  int
	pos [2]ChannelLayout
}

var aacConfigMaps = [8][]aacElemMap{
	1: {{aacIDSCE, [2]ChannelLayout{FrontCenter}}},
	2: {{aacIDCPE, [2]ChannelLayout{FrontLeft, FrontRight}}},
	3: {{aacIDSCE, [2]ChannelLayout{FrontCenter}}, {aacIDCPE, [2]ChannelLayout{FrontLeft, FrontRight}}},
	4: {{aacIDSCE, [2]ChannelLayout{FrontCenter}}, {aacIDCPE, [2]ChannelLayout{FrontLeft, FrontRight}}, {aacIDSCE, [2]ChannelLayout{BackCenter}}},
	5: {{aacIDSCE, [2]ChannelLayout{FrontCenter}}, {aacIDCPE, [2]ChannelLayout{FrontLeft, FrontRight}}, {aacIDCPE, [2]ChannelLayout{BackLeft, BackRight}}},
	6: {{aacIDSCE, [2]ChannelLayout{FrontCenter}}, {aacIDCPE, [2]ChannelLayout{FrontLeft, FrontRight}}, {aacIDCPE, [2]ChannelLayout{BackLeft, BackRight}}, {aacIDLFE, [2]ChannelLayout{LowFrequency}}},
	7: {{aacIDSCE, [2]ChannelLayout{FrontCenter}}, {aacIDCPE, [2]ChannelLayout{FrontLeftOfCenter, FrontRightOfCenter}}, {aacIDCPE, [2]ChannelLayout{FrontLeft, FrontRight}}, {aacIDCPE, [2]ChannelLayout{BackLeft, BackRight}}, {aacIDLFE, [2]ChannelLayout{LowFrequency}}},
}

type aacDecoder struct {
	cfg    aacConfig
	layout ChannelLayout
	parser *aacParser
	synth  *aacSynth
	elems  []aacElemMap
	tags   []int // instance tag of each element for a PCE, else -1
	firsts []int // index of each element's first channel in the synthesiser
	// slot[e][c] is the position in an interleaved sample of channel c of element e.
	slot [][2]int
	pcm  [2][aacSamplesPerFrame]float32
}

func newAAC(tc container.TrackConfig, cfg AudioConfig) (audioCodec, error) {
	if len(tc.CodecConfig) == 0 {
		return nil, fmt.Errorf("%w: an AAC track needs its AudioSpecificConfig", ErrAudioConfig)
	}
	ac, err := parseAudioSpecificConfig(tc.CodecConfig)
	if err != nil {
		return nil, err
	}
	var elems []aacElemMap
	var tags []int // the element_instance_tag of each, or -1 when the order says which is which
	switch {
	case ac.config >= 1 && ac.config <= 7:
		elems = aacConfigMaps[ac.config]
		for range elems {
			tags = append(tags, -1)
		}
	case ac.pce != nil:
		var err error
		if elems, tags, err = aacPCEMap(ac.pce); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: AAC channel configuration %d has no fixed layout", ErrAudioUnsupported, ac.config)
	}
	if aacSamplesPerFrame > cfg.block() {
		return nil, fmt.Errorf("%w: frames of %d samples, the limit is %d", ErrOverLimit, aacSamplesPerFrame, cfg.block())
	}
	p, err := newAACParser(ac, cfg.channels())
	if err != nil {
		return nil, err
	}
	d := &aacDecoder{cfg: ac, parser: p, synth: newAACSynth(ac.rateIndex, ac.channels), elems: elems, tags: tags}
	for _, e := range d.elems {
		for c := 0; c < aacChannelsOfID(e.id); c++ {
			d.layout |= e.pos[c]
		}
	}
	first := 0
	for _, e := range d.elems {
		var s [2]int
		for c := 0; c < aacChannelsOfID(e.id); c++ {
			s[c] = bits.OnesCount32(uint32(d.layout & (e.pos[c] - 1)))
		}
		d.slot = append(d.slot, s)
		d.firsts = append(d.firsts, first)
		first += aacChannelsOfID(e.id)
	}
	return d, nil
}

// aacPCEMap gives the speakers of the elements a program_config_element lists: a
// front pair is left and right (a second one left and right of centre), a front
// single is the centre, a side pair is the side speakers, a back pair the back
// ones and a back single the back centre, and an LFE is the LFE. A layout that
// puts two elements on the same speaker, or has a side single, is refused.
func aacPCEMap(p *aacPCE) ([]aacElemMap, []int, error) {
	var out []aacElemMap
	var tags []int
	var used ChannelLayout
	front := 0
	for _, e := range p.elems {
		m := aacElemMap{id: int(e.id)}
		switch {
		case e.pos == 0 && e.id == aacIDCPE && front == 0:
			m.pos = [2]ChannelLayout{FrontLeft, FrontRight}
			front++
		case e.pos == 0 && e.id == aacIDCPE:
			m.pos = [2]ChannelLayout{FrontLeftOfCenter, FrontRightOfCenter}
		case e.pos == 0:
			m.pos[0] = FrontCenter
		case e.pos == 1 && e.id == aacIDCPE:
			m.pos = [2]ChannelLayout{SideLeft, SideRight}
		case e.pos == 2 && e.id == aacIDCPE:
			m.pos = [2]ChannelLayout{BackLeft, BackRight}
		case e.pos == 2:
			m.pos[0] = BackCenter
		case e.pos == 3:
			m.pos[0] = LowFrequency
		default:
			return nil, nil, fmt.Errorf("%w: a single side channel in a program_config_element", ErrAudioUnsupported)
		}
		for c := 0; c < aacChannelsOfID(m.id); c++ {
			if used&m.pos[c] != 0 {
				return nil, nil, fmt.Errorf("%w: two elements of a program_config_element on one speaker", ErrAudioUnsupported)
			}
			used |= m.pos[c]
		}
		out = append(out, m)
		tags = append(tags, int(e.tag))
	}
	return out, tags, nil
}

func aacChannelsOfID(id int) int {
	if id == aacIDCPE {
		return 2
	}
	return 1
}

func (d *aacDecoder) priming() int    { return 0 }
func (d *aacDecoder) sampleRate() int { return d.cfg.rate }
func (d *aacDecoder) flush() ([]Audio, error) {
	return nil, nil
}

// end: a track without an edit list is presented whole.
func (d *aacDecoder) end(tc container.TrackConfig, samples []container.Sample) int64 { return -1 }

// decode decodes one raw_data_block: 1024 samples of every channel.
func (d *aacDecoder) decode(data []byte) ([]Audio, error) {
	els, err := d.parser.parseFrame(data)
	if err != nil {
		return nil, err
	}
	if len(els) != len(d.elems) {
		return nil, fmt.Errorf("%w: %d channel elements, the configuration has %d", ErrAudioSample, len(els), len(d.elems))
	}
	nch := d.cfg.channels
	a := Audio{Format: SampleF32, SampleRate: d.cfg.rate, Channels: nch, Layout: d.layout, Data: make([]byte, aacSamplesPerFrame*nch*4)}
	var seen uint32
	for e := range els {
		// the element's place: by position for a channel configuration, by type and
		// instance tag for a program_config_element
		idx := e
		if d.tags[0] >= 0 {
			idx = -1
			for i, m := range d.elems {
				if m.id == int(els[e].id) && d.tags[i] == int(els[e].tag) && seen&(1<<uint(i)) == 0 {
					idx = i
					break
				}
			}
			if idx < 0 {
				return nil, fmt.Errorf("%w: element %d (type %d, tag %d) is not in the program_config_element", ErrAudioSample, e, els[e].id, els[e].tag)
			}
		} else if int(els[e].id) != d.elems[e].id {
			return nil, fmt.Errorf("%w: element %d is of type %d, the configuration has %d", ErrAudioSample, e, els[e].id, d.elems[e].id)
		}
		seen |= 1 << uint(idx)
		d.synth.element(&els[e], d.firsts[idx], [2][]float32{d.pcm[0][:], d.pcm[1][:]})
		for c := 0; c < int(els[e].nch); c++ {
			slot := d.slot[idx][c]
			for i, v := range d.pcm[c] {
				binary.LittleEndian.PutUint32(a.Data[(i*nch+slot)*4:], math.Float32bits(v))
			}
		}
	}
	return []Audio{a}, nil
}
