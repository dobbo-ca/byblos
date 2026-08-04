package byblos

import (
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// TestSauvolaAgreesWithJbig2enc is a SANITY CROSS-CHECK, not byb-jj5's
// acceptance gate. The acceptance gate is
// TestSauvolaSeparatesTextUnderUnevenIllumination in sauvola_test.go, which
// needs no oracle and tests the property the bead actually bought. Read this
// test as "byblos lands in the same neighbourhood as a shipping encoder on
// scan-shaped input", and nothing stronger, for two measured reasons.
//
// FIRST, THE ORACLE IS NOT ADAPTIVE IN ANY USEFUL SENSE. `jbig2 -O out.png
// in.png` with no -G is documented as local adaptive thresholding, and that
// documentation is what an earlier draft of this file took at face value.
// Measured, jbig2enc 0.32 puts a SINGLE INTENSITY CUT across a smooth
// horizontal ramp: over a 240x240 ramp its decision boundary is maxInk=59 /
// minBg=60 in every one of eight row bands, and on the corpus gradient its
// ink pixels span grey 0..53 against background 51..255. That is an
// Otsu-style cut, so on the corpus's continuous-tone images (gradient, photo)
// it converts texture-free tone into large black areas -- 9.42% and 26.48% of
// the frame -- where correct Sauvola emits almost none. Those two images are
// excluded from this gate because the disagreement there measures jbig2enc's
// choice, not a byblos defect; see the gradient case in
// TestSauvolaSeparatesTextUnderUnevenIllumination's fixture for the same
// situation with ground truth attached, where jbig2enc scores 46.48% wrong
// and byblos scores 0%.
//
// SECOND, THE INPUTS IT DOES GATE ON ARE NEARLY BILEVEL, SO THEY CANNOT
// DISCRIMINATE. Measured grey distributions:
//
//	scanpage  <64: 2.939%   64..200: 0.000%   >200: 97.061%   4 grey levels
//	scanjpeg  <64: 2.825%   64..200: 0.114%   >200: 97.061%   111 grey levels
//
// With no midtones to place, every thresholder lands within a percent of
// every other one here; replacing the whole formula with a constant 128 still
// clears the 2% tolerance below. Tightening the number does not fix that --
// only an input with midtone structure does, which is what the acceptance
// gate supplies.
//
// What this test still earns: it catches gross breakage against a real
// encoder -- inverted output, wrong dimensions, ink volume off by an order of
// magnitude -- on realistic scan input including JPEG DCT artifacts.
// Measured disagreement is 0.4169% (scanpage) and 0.6088% (scanjpeg) against
// a 2% tolerance, and Sauvola's ink volume is 0.827x and 0.765x the oracle's
// (it hollows out the solid blue stamp, which is larger than the window; see
// Sauvola's doc comment) against an inkRatio band of 0.5x..1.5x.
func TestSauvolaAgreesWithJbig2enc(t *testing.T) {
	requireTool(t, "jbig2")

	const disagreementMax = 0.02
	const inkRatioMin, inkRatioMax = 0.5, 1.5
	dir := t.TempDir()

	images := map[string]image.Image{
		"scanpage": corpus.Scanpage(),
		"scanjpeg": corpus.Scanjpeg(),
	}
	for name, src := range images {
		t.Run(name, func(t *testing.T) {
			g := toGrayForOracle(src)
			bm, err := Sauvola(g)
			if err != nil {
				t.Fatalf("Sauvola(%s): %v", name, err)
			}

			inPath := filepath.Join(dir, name+"-in.png")
			outPath := filepath.Join(dir, name+"-out.png")
			f, err := os.Create(inPath)
			if err != nil {
				t.Fatalf("creating oracle input: %v", err)
			}
			if err := png.Encode(f, g); err != nil {
				t.Fatalf("encoding oracle input: %v", err)
			}
			f.Close()

			cmd := exec.Command("jbig2", "-O", outPath, inPath)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("jbig2 -O: %v: %s", err, out)
			}

			oracleFile, err := os.Open(outPath)
			if err != nil {
				t.Fatalf("opening oracle output: %v", err)
			}
			defer oracleFile.Close()
			oracleImg, err := png.Decode(oracleFile)
			if err != nil {
				t.Fatalf("decoding oracle output: %v", err)
			}

			ob := oracleImg.Bounds()
			if ob.Dx() != bm.Width || ob.Dy() != bm.Height {
				t.Fatalf("oracle output is %v; want %dx%d", ob, bm.Width, bm.Height)
			}

			disagree, oracleTotal, ourTotal := 0, 0, 0
			total := bm.Width * bm.Height
			for y := 0; y < bm.Height; y++ {
				for x := 0; x < bm.Width; x++ {
					r, _, _, _ := oracleImg.At(ob.Min.X+x, ob.Min.Y+y).RGBA()
					var oracleInk uint8
					if r>>8 < 128 {
						oracleInk = 1
					}
					oracleTotal += int(oracleInk)
					ourTotal += int(bm.At(x, y))
					if oracleInk != bm.At(x, y) {
						disagree++
					}
				}
			}
			rate := float64(disagree) / float64(total)
			if rate > disagreementMax {
				t.Errorf("%s: disagreement %.4f%% (%d/%d px) exceeds %.2f%%",
					name, 100*rate, disagree, total, 100*disagreementMax)
			}
			// Ink VOLUME, not just per-pixel agreement: on a page this sparse
			// a binarizer that stops emitting ink altogether disagrees on only
			// the 2.4% of pixels that are ink, which a percentage-of-frame
			// tolerance alone cannot see.
			if oracleTotal == 0 {
				t.Fatalf("%s: oracle produced no ink at all", name)
			}
			if ratio := float64(ourTotal) / float64(oracleTotal); ratio < inkRatioMin || ratio > inkRatioMax {
				t.Errorf("%s: ink volume %d px is %.3fx the oracle's %d px; want %.1fx..%.1fx",
					name, ourTotal, ratio, oracleTotal, inkRatioMin, inkRatioMax)
			}
		})
	}
}

// toGrayForOracle converts src to *image.Gray, the shape both Sauvola and the
// jbig2enc oracle need as input: an 8bpp greyscale PNG.
func toGrayForOracle(src image.Image) *image.Gray {
	b := src.Bounds()
	g := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			g.Set(x-b.Min.X, y-b.Min.Y, src.At(x, y))
		}
	}
	return g
}
