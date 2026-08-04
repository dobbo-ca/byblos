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
// Adaptive, not global: a scan with uneven illumination -- a shadowed gutter,
// a page corner lifted off the platen -- has no single cutoff that both keeps
// the shadow white and keeps the text in the bright half black, because the
// ink and paper intensity bands overlap. Sauvola computes, for every pixel, a
// threshold from the mean m and standard deviation s of an odd windowSize x
// windowSize neighbourhood centred on it:
//
//	T(x,y) = m * (1 + k * (s/r - 1))
//
// r is the dynamic range of s, fixed at 128 for 8-bit greyscale, and k
// controls how strongly local contrast lowers the threshold in text-bearing
// regions. k=0.5 is Sauvola's own paper's value; windowSize=31 suits the
// stroke widths in this corpus. Both were swept (windowSize 15..41, k
// 0.2..0.5) and the output moves smoothly and monotonically across that
// range -- more ink for a wider window, less for a larger k -- so these are a
// conservative point on a continuum, not a cliff edge.
//
// A pixel with value strictly less than its local threshold is ink (bit 1);
// this matches Bitmap's convention (set bit = black), the inverse of
// /DeviceGray.
//
// Known and intrinsic: a uniform dark region LARGER than the window comes out
// hollow. Inside such a region s is ~0 and m is the fill's own value, so
// T -> m*(1-k) and no pixel clears it. Sauvola marks edges of solid fills,
// not their interiors; that is the algorithm, not a defect in this code, and
// it costs nothing for text (strokes are thinner than the window) while
// visibly hollowing out stamps and logos.
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
