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

// TestSauvolaAgreesWithJbig2enc is byb-jj5's acceptance test. Binarization
// quality has no byte-exact oracle -- "is this threshold correct" is
// perceptual, so this is a TOLERANCE comparison (fraction of disagreeing
// pixels), never byte-for-byte, against jbig2enc's own local threshold:
// `jbig2 -O <outfile.png> <infile.png>` dumps the thresholded image it would
// otherwise feed straight into JBIG2 generic-region coding, with no -G, so it
// exercises jbig2enc's default LOCAL ADAPTIVE thresholding, the same
// behaviour byblos is replacing.
//
// THE TOLERANCE IS A MEASUREMENT, NOT A GUESS. Measured against jbig2enc
// 0.32 (Homebrew) on this corpus's two scan-shaped images -- scanpage (flat
// synthetic ink on paper) and scanjpeg (the same page round-tripped through
// JPEG at quality 75, so it carries real DCT block artifacts) -- the
// disagreement is:
//
//	scanpage  2001/480000 = 0.4169%
//	scanjpeg  2922/480000 = 0.6088%
//
// disagreementMax = 2% leaves scanjpeg (the noisier, more realistic input)
// 3.3x headroom over its measured rate.
//
// photo and gradient -- this corpus's two continuous-tone, non-text images --
// were also measured and are DELIBERATELY EXCLUDED from this gate: 25.74%
// and 9.39% disagreement respectively. That is not a byblos defect. Sauvola
// (and jbig2enc's own local threshold) are built for text-on-paper, where a
// window straddles a thin dark stroke against a light background; on a smooth
// gradient or photographic image there is no such structure, so two
// independently-implemented local thresholders diverge heavily on which side
// of a near-flat local mean each pixel falls, without either being "wrong".
// Binarizing a photograph is not a real byblos use case (Sauvola/JBIG2 target
// scanned text), so gating this test on it would make the gate noise rather
// than signal.
func TestSauvolaAgreesWithJbig2enc(t *testing.T) {
	requireTool(t, "jbig2")

	const disagreementMax = 0.02
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

			disagree := 0
			total := bm.Width * bm.Height
			for y := 0; y < bm.Height; y++ {
				for x := 0; x < bm.Width; x++ {
					r, _, _, _ := oracleImg.At(ob.Min.X+x, ob.Min.Y+y).RGBA()
					var oracleInk uint8
					if r>>8 < 128 {
						oracleInk = 1
					}
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
