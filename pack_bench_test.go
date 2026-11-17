package decode

import (
	"testing"

	"github.com/go-avkit/h265"
)

func BenchmarkPackPlane(b *testing.B) {
	pl := h265.Plane{Width: 1920, Height: 1080, Stride: 1920, Pix: make([]uint16, 1920*1080)}
	for i := range pl.Pix {
		pl.Pix[i] = uint16(i)
	}
	dst8 := make([]byte, 1920*1080)
	dst16 := make([]byte, 1920*1080*2)
	b.Run("8bit", func(b *testing.B) {
		b.SetBytes(1920 * 1080)
		for i := 0; i < b.N; i++ {
			packPlane(dst8, &pl, 1)
		}
	})
	b.Run("10bit", func(b *testing.B) {
		b.SetBytes(1920 * 1080 * 2)
		for i := 0; i < b.N; i++ {
			packPlane(dst16, &pl, 2)
		}
	})
}
