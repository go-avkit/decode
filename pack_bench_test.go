package decode

import (
	"testing"

	"github.com/go-avkit/h265"
)

func BenchmarkPackPlane(b *testing.B) {
	pl8 := h265.Plane{Width: 1920, Height: 1080, Stride: 1920, Pix8: make([]uint8, 1920*1080)}
	for i := range pl8.Pix8 {
		pl8.Pix8[i] = uint8(i)
	}
	pl16 := h265.Plane{Width: 1920, Height: 1080, Stride: 1920, Pix16: make([]uint16, 1920*1080)}
	for i := range pl16.Pix16 {
		pl16.Pix16[i] = uint16(i)
	}
	dst8 := make([]byte, 1920*1080)
	dst16 := make([]byte, 1920*1080*2)
	b.Run("8bit", func(b *testing.B) {
		b.SetBytes(1920 * 1080)
		for i := 0; i < b.N; i++ {
			packPlane(dst8, &pl8)
		}
	})
	b.Run("10bit", func(b *testing.B) {
		b.SetBytes(1920 * 1080 * 2)
		for i := 0; i < b.N; i++ {
			packPlane(dst16, &pl16)
		}
	})
}
