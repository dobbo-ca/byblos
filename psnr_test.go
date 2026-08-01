package byblos

import (
	"image"
	"math"
)

// psnrRGB is peak signal-to-noise ratio in dB over the three colour
// channels, 8 bits per channel, MAX=255. It returns math.Inf(1) for
// identical images: ImageMagick's `compare -metric PSNR` reports 120 there,
// which is a display cap, not a measurement, and a test that compared
// against it would be comparing against ImageMagick's build options.
//
// Cross-checked against `compare -metric PSNR` on three ghostscript outputs
// during design work for byb-b3: identical to four decimal places (50.0066 /
// 46.1327 / 38.9357 both ways). This is the same metric, computed without an
// ImageMagick dependency, so the pure-property tests can run in the
// oracle-free job too.
func psnrRGB(a, b image.Image) float64 {
	ba, bb := a.Bounds(), b.Bounds()
	if ba.Dx() != bb.Dx() || ba.Dy() != bb.Dy() {
		return math.NaN()
	}
	var sum float64
	var n int64
	for y := 0; y < ba.Dy(); y++ {
		for x := 0; x < ba.Dx(); x++ {
			ar, ag, ab, _ := a.At(ba.Min.X+x, ba.Min.Y+y).RGBA()
			br, bg, bb2, _ := b.At(bb.Min.X+x, bb.Min.Y+y).RGBA()
			dr := float64(int(ar>>8) - int(br>>8))
			dg := float64(int(ag>>8) - int(bg>>8))
			db := float64(int(ab>>8) - int(bb2>>8))
			sum += dr*dr + dg*dg + db*db
			n += 3
		}
	}
	if sum == 0 {
		return math.Inf(1)
	}
	mse := sum / float64(n)
	return 10 * math.Log10(255*255/mse)
}
