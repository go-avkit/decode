package decode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vpxBench decodes every file named by VPX_BENCH_FILES (a comma-separated list of IVF or WebM
// paths) start to finish, one fresh decoder per iteration, and reports ns/frame and frames/s next
// to the usual ns/op and allocations. A "vp80" file name prefix or a ".vp8" marker selects VP8 as
// in the libvpx vectors; anything else is VP9. It skips when the variable is unset.
func vpxBench(b *testing.B, vp9 bool) {
	list := os.Getenv("VPX_BENCH_FILES")
	if list == "" {
		b.Skip("VPX_BENCH_FILES is not set: no VP8/VP9 stream to decode")
	}
	ran := 0
	for _, path := range strings.Split(list, ",") {
		path = strings.TrimSpace(path)
		base := filepath.Base(path)
		isVP8 := strings.HasPrefix(base, "vp80") || strings.Contains(base, ".vp8.")
		if isVP8 == vp9 || path == "" {
			continue
		}
		ran++
		b.Run(base, func(b *testing.B) {
			samples, err := libvpxSamples(path)
			if err != nil {
				b.Fatal(err)
			}
			mk := NewVP9
			if isVP8 {
				mk = NewVP8
			}
			var pictures int
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d, err := mk(Config{})
				if err != nil {
					b.Fatal(err)
				}
				pictures = 0
				for j, s := range samples {
					frames, err := d.DecodeSample(s, int64(j))
					if err != nil {
						b.Fatalf("sample %d: %v", j, err)
					}
					pictures += len(frames)
				}
				d.Close()
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(pictures), "ns/frame")
			b.ReportMetric(float64(pictures), "frames")
		})
	}
	if ran == 0 {
		b.Skip("VPX_BENCH_FILES names no file for this codec")
	}
}

func BenchmarkVP8Decode(b *testing.B) { vpxBench(b, false) }

func BenchmarkVP9Decode(b *testing.B) { vpxBench(b, true) }
