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
// psnrSlackDB threshold below is not large -- post-byb-20b the worst measured
// point sits 1.677 dB inside it. Do not name the constant's value here: this
// sentence still said "the 3.0 dB threshold" long after the slack became 4.5.
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
//	sum(size_byblos) <= sizeRatioMax * sum(size_pngquant), per image, where
//	sizeRatioMax is 1.38 blanket, except scanpage, which carries its own
//	tighter scanpageRatioMax of 1.06 (see below for why).
//
// THE SLACK IS SIZED BY THE ORACLE'S OWN VERSION SPREAD, NOT BY BYBLOS.
// pngquant is a moving target: the two builds this suite actually runs against
// disagree with each other by more than byblos disagrees with either.
//
//	pngquant 3.0.3   (Homebrew, local dev)      worst deficit 2.8083 dB at gradient@32
//	pngquant 2.18.0  (Ubuntu noble, CI runner)  worst deficit 2.8232 dB at gradient@32
//
// GRADIENT@32 IS NOW THE WHOLE STORY -- byb-20b's palette reorder shrank
// scanjpeg@8 from 3116 to 2558 bytes, which lowered the interpolated target
// enough that byblos now WINS there against 3.0.3 by 1.9593 dB; against
// 2.18.0 it still loses, but only by 0.5682 dB, no longer the worst point on
// either build. An earlier 3.0 dB slack was calibrated on 3.0.3 alone and
// went red in CI on the first run against 2.18.0. 4.5 dB covers the worst
// point either build produces with 1.677 dB spare.
//
// Widening it does NOT make this test theatre, which was checked rather than
// assumed: disabling the Lloyd refinement -- a real quality regression at the
// correct palette size -- puts scanjpeg@8 at a 9.40 dB deficit against 3.0.3
// and 11.89 dB against 2.18.0, so 4.5 dB still fails it by 4.9 dB on the
// closer of the two. What the slack cannot catch is a regression that
// also SHRINKS the output, since that slides down pngquant's own curve; the
// bit-depth and palette-size tests cover that direction instead.
//
// THE SIZE HALF IS NOT SIZED BY VERSION SPREAD, IT IS SIZED BY A MEASURED
// REGRESSION WINDOW (byb-0b8, re-derived under byb-20b). The threshold was
// long suspected of being the next thing to go red on an oracle bump, by
// analogy with the PSNR slack above. It was measured against five pngquant
// builds spanning 2019-2024 -- 2.12.2 (Ubuntu jammy), 2.17.0 (Debian
// bookworm), 2.18.0 (Ubuntu noble = the CI runner), 3.0.3 (Homebrew = local
// dev) and 3.0.3 from Alpine -- and the analogy does not hold. Only three of
// those five behave distinctly: 2.12.2 and 2.17.0 are byte-identical to each
// other, and the two independently built 3.0.3s are byte-identical to each
// other, so packaging is not a variable, only version is. All five were re-run
// for byb-20b and both identities still hold. Per-image sum-ratio (byblos
// total / pngquant total over the whole ladder), builds oldest to newest:
//
//	          2.12.2  2.17.0  2.18.0  3.0.3   3.0.3a  spread
//	gradient  0.9702  0.9702  1.2959  1.3323  1.3323  0.3621
//	photo     0.9797  0.9797  1.1248  1.1200  1.1200  0.1451
//	scanjpeg  1.0282  1.0282  1.0513  1.0313  1.0313  0.0231
//	scanpage  1.0061  1.0061  1.0061  1.0061  1.0061  0.0000
//
// GRADIENT BINDS, at 1.3323 on 3.0.3 (local dev) and 1.2959 on 2.18.0 (CI).
// It binds because it has no dominant colour, so byb-20b's palette reorder
// moved it by exactly ONE byte over the whole ladder (42616 -> 42617) while
// scanpage fell 5712 -> 3954 and scanjpeg 43392 -> 39555. Before byb-20b
// scanpage bound instead, at 1.4534 on all five builds, and the threshold was
// 1.50. sizeRatioMax = 1.38 leaves gradient 3.58% headroom on 3.0.3 and 6.49%
// on 2.18.0 -- comparable to the 3.20% scanpage had at 1.50 -- while still
// failing the reference regression below on both of those builds.
//
// THE GATE IS STILL LIVE, WHICH WAS MEASURED, NOT ASSUMED. The reference
// regression is png.DefaultCompression in QuantizePNG's encoder: a plausible
// one-token slip, not a strawman. Its ratios, same five builds:
//
//	          2.12.2  2.17.0  2.18.0  3.0.3   3.0.3a
//	gradient  1.0671  1.0671  1.4253  1.4654  1.4654
//	scanpage  1.1099  1.1099  1.1099  1.1099  1.1099
//
// On the two builds this suite actually runs against it clears 1.38 on
// gradient by 3.29% (2.18.0) and 6.19% (3.0.3), so a single constant does
// catch it there. On 2.12.2/2.17.0 it does not: their gradient denominator is
// 37% larger, the mutation only reaches 1.0671, and a blanket 1.38 would be
// dead. THAT is why scanpage carries a second, tighter bound of its own.
//
// scanpageRatioMax = 1.06 is the one place a tight bound is not an oracle
// tripwire, and that is a measurement rather than a hope: pngquant emits 655
// bytes for scanpage on every build at every N, spread exactly 0.0000 across a
// five-year span and a major version bump, so byblos sits at 1.0061 on all
// five and DefaultCompression at 1.1099 on all five. 1.06 splits them with
// 5.4% and 4.7% either side, identically everywhere. Its job is to make this
// gate's liveness independent of gradient, whose 0.3621 spread is the largest
// in the corpus: with only the blanket bound, a 3.3% swing in pngquant's
// gradient output would drop DefaultCompression under 1.38 and silently disarm
// the whole size gate -- and pngquant moved gradient by 25% in one release,
// between 2.17.0 and 2.18.0. Landing byb-20b against an unchanged 1.50 would
// have caused exactly that silent disarm, which is what this rewrite is for.
//
// The division of labour with the PSNR half is also on record rather than
// assumed, and it is NOT clean. Three further mutations were measured and do
// NOT trip the size gate, correctly: dropping the Lloyd refinement (gradient
// 1.3062 on 3.0.3 / 1.2705 on 2.18.0), halving the Lloyd iteration budget
// (1.3287 / 1.2923), and splitting median-cut boxes on population instead of
// variance (1.3324 / 1.2960). All three leave the output the same size or
// smaller -- they are quality regressions. But the PSNR slack above only
// catches two of them: no-Lloyd (9.4060 dB deficit @3.0.3 / 11.8963 dB
// @2.18.0) and the population-split (9.6034 / 12.4655 dB) both blow well
// past the 4.5 dB slack. Halving the Lloyd budget does NOT -- its worst PSNR
// margin is -2.72 dB on both builds, inside the 4.5 dB slack, so that mutant
// passes both halves of this test. THIS IS A KNOWN UNCAUGHT GAP, not a
// claim that the slack covers it. Forcing 8-bit depth by padding the palette
// out to 256 entries does trip the size gate, at scanpage 2.7893 on every
// build.
//
// WHY scanpage COMPRESSES AS IT DOES is worth carrying forward, because it is
// what byb-20b changed. Both sides emit a structurally identical file for it
// -- bitdepth 2, colortype 3, a 12-byte PLTE, filter 0 on all 800 scanlines --
// so all 297 bytes of the old 952-vs-655 gap were IDAT, and all of it was
// PALETTE ORDER. pngquant puts the dominant colour (paper) at index 0, so the
// paper run and the per-scanline filter bytes are both 0x00 and LZ77 matches
// them as one run across row boundaries; byblos used to put paper at index 1,
// so its 625 all-paper rows were 0x00 followed by 150 bytes of 0x55 and the
// run broke at every row boundary. The longest single-byte run in byblos's
// stream was 150 bytes; in pngquant's it is 10,448. Recompressing byblos's own
// scanline stream with zlib-9 gave 862 against Go's 871, so compress/flate was
// never the cause. byb-20b re-indexes by descending pixel population, which
// makes the scanline stream byte-identical to pngquant's; zlib-9 then
// reproduces 655 exactly and Go's png.BestCompression gives 659, which is the
// residual 1.0061 rather than 1.0000.
//
// The oracle can still move, but only two ways, and they are not symmetric. CI
// pins runs-on: ubuntu-24.04 rather than ubuntu-latest, so its 2.18.0 cannot
// drift without someone editing ci.yml -- re-measure when that pin moves. The
// Homebrew build under local dev floats on brew upgrade with no such gate, so
// a local-only failure here is an oracle bump until proven otherwise. Which
// row went red says which: gradient is the 3.5%-wide window described above,
// but scanpage going red is not oracle drift, because scanpage has never moved
// on any build measured.
//
// scanpage is lossless on both sides, so its PSNR rows are +Inf and the
// comparison passes on +Inf < +Inf being false. That is correct, not vacuous:
// a byblos regression there yields a finite PSNR against pngquant's +Inf and
// fails immediately.
func TestQuantizePNGRateDistortionAgainstPngquant(t *testing.T) {
	requireTool(t, "pngquant")

	const psnrSlackDB = 4.5
	const sizeRatioMax = 1.38
	// scanpage carries a second, tighter bound: it is the only row whose
	// pngquant denominator is identical on every measured build, so it is
	// the only row where a tight bound is not an oracle tripwire. See above.
	const scanpageRatioMax = 1.06
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
			maxRatio := sizeRatioMax
			if name == "scanpage" {
				maxRatio = scanpageRatioMax
			}
			if ratio := float64(byblosTotal) / float64(pngquantTotal); ratio > maxRatio {
				t.Errorf("%s: total byblos size %d is %.3fx pngquant's %d; want <= %.2fx",
					name, byblosTotal, ratio, pngquantTotal, maxRatio)
			}
		})
	}
}
