// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package decode

import (
	"bytes"
	"path/filepath"
	"sync"
	"testing"

	"github.com/go-avkit/avkit/container"
)

type aacTrackFixture struct {
	tc      container.TrackConfig
	samples []container.Sample
}

// decode returns the track's decoded audio as one byte string.
func (f aacTrackFixture) decode() ([]byte, error) {
	as, err := decodeAudio(f.tc, f.samples, AudioConfig{})
	if err != nil {
		return nil, err
	}
	var b []byte
	for _, a := range as {
		b = append(b, a.Data...)
	}
	return b, nil
}

// TestAACConcurrentDecoders: the transforms are shared by every decoder, so
// decoders running in different goroutines must not share their working memory.
// Several goroutines each decode the same fixtures through their own decoders, and
// every one of them must produce the bytes the single goroutine did. Under -race
// (CI runs the suite with it) a shared scratch buffer is also a reported race.
func TestAACConcurrentDecoders(t *testing.T) {
	var fixtures []aacTrackFixture
	var want [][]byte
	for _, name := range []string{"st44.aac", "surround51.aac", "mono22.aac"} {
		cfg, frames := aacADTSFrames(t, filepath.Join("testdata", "aac", name))
		f := aacTrackFixture{tc: aacTrack(cfg.rateIndex, cfg.config)}
		for _, fr := range frames {
			f.samples = append(f.samples, container.Sample{Data: fr, Duration: 1024})
		}
		w, err := f.decode()
		if err != nil {
			t.Fatal(err)
		}
		fixtures, want = append(fixtures, f), append(want, w)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < 3; round++ {
				for i, f := range fixtures {
					got, err := f.decode()
					if err != nil {
						t.Error(err)
						return
					}
					if !bytes.Equal(got, want[i]) {
						t.Errorf("fixture %d: a goroutine's output differs from the single-goroutine output", i)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	// the SBR signal path shares its plans the same way (aac_sbr_hf_test.go)
	t.Run("SBR", TestSBRConcurrentChannels)
}
