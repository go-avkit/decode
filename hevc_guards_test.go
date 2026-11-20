// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"errors"
	"testing"

	"github.com/go-avkit/h265"
)

// noPanic runs f and fails the test, with a message that names the case, when f panics: the
// guards below were held only by the panic their absence caused (#109), which says nothing of
// what they are for.
func noPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s: panicked: %v", name, r)
		}
	}()
	f()
}

// TestHEVCUnitHeaderGuard (#109, hevc.go hevcUnit): a unit shorter than its header, with the
// forbidden bit set or with nuh_temporal_id_plus1 0 is refused, and one just inside is accepted.
func TestHEVCUnitHeaderGuard(t *testing.T) {
	for name, raw := range map[string][]byte{
		"nil":          nil,
		"empty":        {},
		"one byte":     {0x42},
		"forbidden":    {0x80, 0x01},
		"temporal 0":   {0x42, 0x00},
		"temporal 0 b": {0x42, 0xf8},
	} {
		noPanic(t, name, func() {
			if _, ok := hevcUnit(raw, 0); ok {
				t.Errorf("%s: accepted", name)
			}
		})
	}
	for name, raw := range map[string][]byte{"two bytes": {0x42, 0x01}, "temporal 7": {0x42, 0x07}} {
		noPanic(t, name, func() {
			if _, ok := hevcUnit(raw, 0); !ok {
				t.Errorf("%s: refused", name)
			}
		})
	}
}

// TestHEVCAnnexBHoldsAnUnfinishedUnit (#109, hevc.go hevcAnnexB): bytes with no start code, or
// with only the one that opens the unit still being held, produce nothing and are kept.
func TestHEVCAnnexBHoldsAnUnfinishedUnit(t *testing.T) {
	for name, data := range map[string][]byte{
		"no start code":      {0x42, 0x01, 0xaa, 0xbb},
		"start code at 0":    {0, 0, 1, 0x42, 0x01, 0xaa},
		"zeros, then a code": {0, 0, 0, 1},
	} {
		d, _ := NewHEVC(Config{}, 4)
		noPanic(t, name, func() {
			fs, err := d.hevcAnnexB(data)
			if err != nil || len(fs) != 0 {
				t.Errorf("%s: %d pictures, %v", name, len(fs), err)
			}
			if len(d.annexB) == 0 {
				t.Errorf("%s: the held bytes were dropped", name)
			}
		})
		d.Close()
	}
}

// TestHEVCFrameEmptyPlanes (#109, hevc.go hevcFrame): a picture with an empty luma plane, or
// with a chroma plane that is not the size of its format, is a malformed picture, refused and
// not copied.
func TestHEVCFrameEmptyPlanes(t *testing.T) {
	d, _ := NewHEVC(Config{}, 4)
	for name, mutate := range map[string]func(*h265.Output){
		"no width":      func(o *h265.Output) { o.Picture.Planes[0].Width = 0 },
		"no height":     func(o *h265.Output) { o.Picture.Planes[0].Height = 0 },
		"negative":      func(o *h265.Output) { o.Picture.Planes[0].Width = -4 },
		"wide chroma":   func(o *h265.Output) { o.Picture.Planes[1].Width = 3 },
		"tall chroma":   func(o *h265.Output) { o.Picture.Planes[1].Height = 2 },
		"narrow chroma": func(o *h265.Output) { o.Picture.Planes[1].Width = 1 },
	} {
		o := hevcOutput(8)
		mutate(&o)
		noPanic(t, name, func() {
			if _, err := d.hevcFrame(o); !errors.Is(err, ErrBitstream) && !errors.Is(err, ErrUnsupported) {
				t.Errorf("%s: %v", name, err)
			}
		})
	}
}
