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
// THE SIZE HALF IS NOT SIZED THAT WAY, BECAUSE IT MEASURABLY DOES NOT NEED TO
// BE (byb-0b8). The size threshold was long suspected of being the next thing
// to go red on an oracle bump, by analogy with the PSNR slack above. It was
// then measured against five pngquant builds spanning 2019-2024 -- 2.12.2
// (Ubuntu jammy), 2.17.0 (Debian bookworm), 2.18.0 (Ubuntu noble = the CI
// runner), 3.0.3 (Homebrew = local dev) and 3.0.3 from Alpine -- and the
// analogy does not hold. Only three of those five behave distinctly: 2.12.2
// and 2.17.0 are byte-identical to each other, and the two independently built
// 3.0.3s are byte-identical to each other, so packaging is not a variable, only
// version is. Per-image sum-ratio (byblos total / pngquant total over the whole
// ladder), worst build first:
//
//	scanpage  1.4534  1.4534  1.4534  1.4534  1.4534   spread 0.0000
//	gradient  0.9702  0.9702  1.2958  1.3323  1.3323   spread 0.3621
//	scanjpeg  1.1279  1.1279  1.1533  1.1314  1.1314   spread 0.0254
//	photo     0.9801  0.9801  1.1252  1.1205  1.1205   spread 0.1452
//	          2.12.2  2.17.0  2.18.0  3.0.3   3.0.3a
//
// The binding image is invariant. scanpage is 655 bytes on every build at every
// N -- the version spread on the only row that constrains the threshold is
// exactly zero, across a five-year span and a major version bump. The rows that
// DO move (gradient 0.9702 -> 1.3323, because pngquant got 25% better at
// gradient between 2.17.0 and 2.18.0, not because byblos changed) are the rows
// with 12%+ headroom. So the threshold stays at 1.50: widening it would be
// widening against a spread that the measurement says is not there, on the one
// row where a spread would matter.
//
// WHY scanpage BINDS IS NOT QUANTIZATION QUALITY, and this is the part worth
// carrying forward. Both sides emit a structurally identical file for it --
// bitdepth 2, colortype 3, a 12-byte PLTE, filter 0 on all 800 scanlines. All
// 297 bytes of the 952-vs-655 gap are IDAT, and all of it is PALETTE ORDER:
// pngquant puts the dominant colour (paper) at index 0, so the paper run and
// the per-scanline filter bytes are both 0x00 and LZ77 matches them as one run
// across row boundaries; byblos puts paper at index 1, so every row is 0x00
// followed by 150 bytes of 0x55 and the run breaks 800 times. Recompressing
// byblos's own scanline stream with zlib-9 gives 862 against Go's 871, so
// compress/flate is not the cause; re-indexing byblos's output by descending
// pixel population and recompressing gives 664 against pngquant's 655. Filed as
// byb-20b. IF THAT LANDS, RE-DERIVE THIS THRESHOLD: scanpage would fall to
// ~1.014 and scanjpeg to ~1.031, at which point 1.50 is ~48% dead slack and the
// size half stops biting anywhere.
//
// Because it does bite today, and only there. A png.DefaultCompression
// regression (a plausible one-token slip, not a strawman) takes scanpage to
// 1.968x and fails; the same mutation leaves gradient, photo and scanjpeg green,
// since those sit 12-34% under the threshold. scanpage is the whole size gate.
//
// The oracle can still move, but only two ways, and they are not symmetric. CI
// pins runs-on: ubuntu-24.04 rather than ubuntu-latest, so its 2.18.0 cannot
// drift without someone editing ci.yml -- re-measure when that pin moves. The
// Homebrew build under local dev floats on brew upgrade with no such gate, so
// a local-only failure here is an oracle bump until proven otherwise.
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
