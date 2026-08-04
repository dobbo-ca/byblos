package byblos

import (
	"fmt"
	"image"
	"math"
)

// Sauvola binarizes img by local adaptive thresholding (J. Sauvola & M.
// Pietikäinen, "Adaptive document image binarization", Pattern Recognition
// 33(2), 2000), returning a Bitmap ready for EncodeJBIG2Generic.
//
// jbig2enc's default is local adaptive thresholding, not global -- -G is the
// documented opt-out to a single global threshold -- so matching its shipped
// behaviour means adaptive, not a single cutoff over the whole page. Sauvola
// computes, for every pixel, a threshold from the mean m and standard
// deviation s of an odd windowSize x windowSize neighbourhood centred on it:
//
//	T(x,y) = m * (1 + k * (s/r - 1))
//
// r is the dynamic range of s, fixed at 128 for 8-bit greyscale, and k
// controls how strongly local contrast lowers the threshold in text-bearing
// regions. windowSize=31 and k=0.5 are the values most implementations (and
// Sauvola's own paper) treat as defaults; see sauvola_oracle_test.go for how
// they were checked against jbig2enc's own local threshold rather than just
// assumed.
//
// A pixel with value strictly less than its local threshold is ink (bit 1);
// this matches Bitmap's convention (set bit = black), the inverse of
// /DeviceGray.
//
// The mean and variance are both obtained from a single pass over two
// integral images (sum and sum-of-squares) built from img, giving O(1) work
// per pixel regardless of window size after that O(width*height) setup.
func Sauvola(img image.Image) (*Bitmap, error) {
	if img == nil {
		return nil, fmt.Errorf("byblos: sauvola: image is nil")
	}
	b := img.Bounds()
	if b.Empty() {
		return nil, fmt.Errorf("byblos: sauvola: image bounds %v are empty", b)
	}

	const windowSize = 31 // odd, so every window is centred on its pixel
	const k = 0.5
	const r = 128.0

	w, h := b.Dx(), b.Dy()
	// gray is always re-anchored to (0,0): even when img is already an
	// *image.Gray, its Bounds().Min need not be (0,0) (a sub-image, for
	// instance), and every read below indexes it as if it were.
	gray := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			gray.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}

	// sum and sumSq are (w+1)x(h+1) integral images, 1-indexed so row/column 0
	// is the all-zero border: sum[y][x] is the total (and sumSq the total of
	// squares) of every pixel with row < y and column < x.
	sum := make([][]int64, h+1)
	sumSq := make([][]int64, h+1)
	for y := range sum {
		sum[y] = make([]int64, w+1)
		sumSq[y] = make([]int64, w+1)
	}
	for y := 0; y < h; y++ {
		var rowSum, rowSumSq int64
		for x := 0; x < w; x++ {
			v := int64(gray.GrayAt(x, y).Y)
			rowSum += v
			rowSumSq += v * v
			sum[y+1][x+1] = sum[y][x+1] + rowSum
			sumSq[y+1][x+1] = sumSq[y][x+1] + rowSumSq
		}
	}

	out := NewBitmap(w, h)
	half := windowSize / 2
	for y := 0; y < h; y++ {
		y0 := max(0, y-half)
		y1 := min(h, y+half+1)
		for x := 0; x < w; x++ {
			x0 := max(0, x-half)
			x1 := min(w, x+half+1)
			n := int64(x1-x0) * int64(y1-y0)

			s := sum[y1][x1] - sum[y0][x1] - sum[y1][x0] + sum[y0][x0]
			sq := sumSq[y1][x1] - sumSq[y0][x1] - sumSq[y1][x0] + sumSq[y0][x0]

			mean := float64(s) / float64(n)
			variance := float64(sq)/float64(n) - mean*mean
			if variance < 0 {
				variance = 0 // guards float rounding at near-uniform windows
			}
			stddev := math.Sqrt(variance)

			threshold := mean * (1 + k*(stddev/r-1))
			v := gray.GrayAt(x, y).Y
			if float64(v) < threshold {
				out.Set(x, y, 1)
			}
		}
	}

	return out, nil
}
