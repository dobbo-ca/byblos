package byblos

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// This file is the oracle-backed half of byb-b3's QuantizePNG acceptance
// test. pngquant is REQUIRED to be invoked with --nofs: measured during
// design, pngquant's default Floyd-Steinberg dithering makes its own output
// 5-23x larger AND 1.4-2.1 dB worse on PSNR than --nofs on this corpus, so
// benchmarking against the default would flatter byblos on both axes at
// once. --strip and --speed 1 are pinned for cross-platform/version
// stability (see design spec section 4).
func runPngquant(t *testing.T, img image.Image, n int) (size int, decoded image.Image) {
	t.Helper()
	dir := t.TempDir()
	inPath := filepath.Join(dir, "in.png")
	outPath := filepath.Join(dir, "out.png")

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encoding oracle input: %v", err)
	}
	if err := os.WriteFile(inPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("writing oracle input: %v", err)
	}

	cmd := exec.Command("pngquant", "--force", "--nofs", "--strip", "--speed", "1",
		fmt.Sprint(n), "-o", outPath, inPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pngquant: %v: %s", err, out)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading pngquant output: %v", err)
	}
	out, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decoding pngquant output: %v", err)
	}
	return len(data), out
}

// pngquantCurve is pngquant's (size, PSNR) rate-distortion curve over the
// ladder N in {8,16,32,64,128,256}, sorted by size ascending.
type curvePoint struct {
	size int
	psnr float64
}

func pngquantCurve(t *testing.T, orig image.Image) []curvePoint {
	t.Helper()
	var pts []curvePoint
	for _, n := range []int{8, 16, 32, 64, 128, 256} {
		size, decoded := runPngquant(t, orig, n)
		pts = append(pts, curvePoint{size, psnrRGB(orig, decoded)})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].size < pts[j].size })
	return pts
}

// interpAtSize linearly interpolates a (size, PSNR) curve at size, clamping
// to the curve's endpoints outside its range. The design's rate-distortion
// comparison requires this rather than a matched-N comparison, because
// pngquant and byblos produce different sizes at the same N.
//
// This is linear in raw size, not in log(size) as an earlier draft of the
// design spec assumed. On a concave rate-distortion curve that is the
// LENIENT direction (it underestimates the required PSNR by up to ~0.3 dB
// across this corpus), which matters here because the real margin on the
// 3.0 dB threshold below is already thin -- see the measured worst cases.
func interpAtSize(pts []curvePoint, size int) float64 {
	if size <= pts[0].size {
		return pts[0].psnr
	}
	if size >= pts[len(pts)-1].size {
		return pts[len(pts)-1].psnr
	}
	for i := 1; i < len(pts); i++ {
		if size <= pts[i].size {
			a, b := pts[i-1], pts[i]
			frac := float64(size-a.size) / float64(b.size-a.size)
			return a.psnr + frac*(b.psnr-a.psnr)
		}
	}
	return pts[len(pts)-1].psnr
}

// TestQuantizePNGRateDistortionAgainstPngquant is byb-b3's acceptance test
// (bead's own words: "Output size and PSNR within bounds of pngquant ...
// output on the corpus"), pinned numerically as the design settled:
//
//	psnr_byblos(N) >= interp_pngquant(size_byblos(N)) - 4.5 dB, every N, every image
//	sum(size_byblos) <= 1.50 * sum(size_pngquant), per image
//
// THE SLACK IS SIZED BY THE ORACLE'S OWN VERSION SPREAD, NOT BY BYBLOS.
// pngquant is a moving target: the two builds this suite actually runs against
// disagree with each other by more than byblos disagrees with either.
//
//	pngquant 3.0.3   (Homebrew, local dev)      worst deficit 2.82 dB at gradient@32
//	pngquant 2.18.0  (Ubuntu noble, CI runner)  worst deficit 3.42 dB at scanjpeg@8
//
// scanjpeg@8 is the whole story. Against 3.0.3 byblos WINS there by 0.27 dB;
// against 2.18.0 it loses by 3.42 dB, because 2.18.0 scores 3.69 dB better on
// that one point than 3.0.3 does. An earlier 3.0 dB slack was calibrated on
// 3.0.3 alone and went red in CI on the first run against 2.18.0. 4.5 dB covers
// the worst point either build produces with ~1.1 dB spare.
//
// Widening it does NOT make this test theatre, which was checked rather than
// assumed: disabling the Lloyd refinement -- a real quality regression at the
// correct palette size -- puts scanjpeg@8 at a 11.11 dB deficit, so 4.5 dB
// still fails it by 6.6 dB. What the slack cannot catch is a regression that
// also SHRINKS the output, since that slides down pngquant's own curve; the
// bit-depth and palette-size tests cover that direction instead.
//
// The size margin is thinnest on scanpage (1.4534x of the 1.50x threshold,
// ~3.1% headroom) -- not gradient, which sits at 1.3323x. That one has no
// version-spread evidence behind it yet and is the likeliest next thing to go
// red on an oracle bump.
//
// scanpage is lossless on both sides, so its PSNR rows are +Inf and the
// comparison passes on +Inf < +Inf being false. That is correct, not vacuous:
// a byblos regression there yields a finite PSNR against pngquant's +Inf and
// fails immediately.
func TestQuantizePNGRateDistortionAgainstPngquant(t *testing.T) {
	requireTool(t, "pngquant")

	const psnrSlackDB = 4.5
	const sizeRatioMax = 1.50
	ladder := []int{8, 16, 32, 64, 128, 256}

	images := map[string]image.Image{
		"gradient": corpus.Gradient(),
		"photo":    corpus.Photo(),
		"scanpage": corpus.Scanpage(),
		"scanjpeg": corpus.Scanjpeg(),
	}
	for name, img := range images {
		t.Run(name, func(t *testing.T) {
			curve := pngquantCurve(t, img)
			var byblosTotal, pngquantTotal int
			for _, n := range ladder {
				out, err := QuantizePNG(img, n)
				if err != nil {
					t.Fatalf("QuantizePNG(%s, %d): %v", name, n, err)
				}
				decoded, err := png.Decode(bytes.NewReader(out))
				if err != nil {
					t.Fatalf("decoding QuantizePNG(%s, %d) output: %v", name, n, err)
				}
				byblosSize := len(out)
				byblosPSNR := psnrRGB(img, decoded)
				want := interpAtSize(curve, byblosSize) - psnrSlackDB
				if byblosPSNR < want {
					t.Errorf("%s@%d: PSNR %.2f dB < pngquant-interpolated %.2f dB - %.1f slack",
						name, n, byblosPSNR, want+psnrSlackDB, psnrSlackDB)
				}
				byblosTotal += byblosSize
			}
			for _, pt := range curve {
				pngquantTotal += pt.size
			}
			if ratio := float64(byblosTotal) / float64(pngquantTotal); ratio > sizeRatioMax {
				t.Errorf("%s: total byblos size %d is %.3fx pngquant's %d; want <= %.2fx",
					name, byblosTotal, ratio, pngquantTotal, sizeRatioMax)
			}
		})
	}
}
