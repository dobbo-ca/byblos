package byblos

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// This file is the oracle-backed half of byb-b3's JPEG recompression
// acceptance test: rate-distortion comparison against ghostscript.
//
// Two traps, both measured during design and worth restating here because a
// test written against the obvious knobs silently passes for the wrong
// reason:
//   - Without -dPassThroughJPEGImages=false, gs does not recompress at all.
//   - -dJPEGQ is ignored by gs 10.07.1 on this path; the working knob is
//     /QFactor via setdistillerparams.

// gsRecompressPDF runs the single-image PDF built below through gs's
// pdfwrite device at the given /QFactor and returns the resulting PDF bytes.
func gsRecompressPDF(t *testing.T, pdf []byte, qFactor float64) []byte {
	t.Helper()
	dir := t.TempDir()
	inPath := filepath.Join(dir, "in.pdf")
	outPath := filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(inPath, pdf, 0o644); err != nil {
		t.Fatalf("writing gs input: %v", err)
	}
	params := fmt.Sprintf("<< /ColorImageDict << /QFactor %v /Blend 1 "+
		"/HSamples [2 1 1 2] /VSamples [2 1 1 2] >> >> setdistillerparams", qFactor)
	cmd := exec.Command("gs", "-q", "-dNOPAUSE", "-dBATCH", "-sDEVICE=pdfwrite",
		"-sOutputFile="+outPath,
		"-dPassThroughJPEGImages=false",
		"-dAutoFilterColorImages=false",
		"-dColorImageFilter=/DCTEncode",
		"-dDownsampleColorImages=false",
		"-c", params,
		"-f", inPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gs QFactor %v: %v: %s", qFactor, err, out)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading gs output: %v", err)
	}
	return data
}

// TestOptimizeRecompressJPEGRateDistortionAgainstGhostscript is byb-b3's
// acceptance test for the JPEG recompression pass (bead's own words: "Output
// size and PSNR within bounds of ... ghostscript output on the corpus"),
// pinned as design spec section 4 settled:
//
//	psnr_byblos >= interp_gs(size_byblos) - 1.5 dB
//
// gs's curve is built over QFactor in {0.1, 0.2, 0.4, 0.7, 1.0, 1.5, 2.0} by
// running the SAME single-image PDF through gs's pdfwrite device; byblos's
// curve is Optimize(RecompressJPEG:true, JPEGQuality) in {30, 50, 75, 90} on
// that same document, so both sides start from and are measured against the
// identical source image.
//
// The size axis compares whole-PDF byte counts from two different PDF
// writers, not extracted image-stream bytes: pdfcpu's container overhead
// measured ~1287 B against gs pdfwrite's ~2798 B on this fixture, a ~1.5 KB
// credit to byblos on every point that is worth roughly 0.6-1.0 dB near the
// low end of gs's curve. It fits inside the 1.5 dB slack, but a chunk of
// byblos's apparent margin here is container overhead, not JPEG quality.
func TestOptimizeRecompressJPEGRateDistortionAgainstGhostscript(t *testing.T) {
	requireTool(t, "gs")
	requireTool(t, "pdfimages")

	src := corpus.Photo()
	var srcJPEG bytes.Buffer
	if err := jpeg.Encode(&srcJPEG, src, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encoding source JPEG: %v", err)
	}

	widthPt := float64(corpus.ImageW) * 72 / 300
	heightPt := float64(corpus.ImageH) * 72 / 300
	encoded := EncodedImage{
		Width:      corpus.ImageW,
		Height:     corpus.ImageH,
		BPC:        8,
		ColorSpace: ColorSpace{Name: "DeviceRGB"},
		Filter:     "DCTDecode",
		Data:       srcJPEG.Bytes(),
	}
	var pdfBuf bytes.Buffer
	if err := BuildPDF(&pdfBuf, []BuildPage{{Image: encoded, WidthPt: widthPt, HeightPt: heightPt}}); err != nil {
		t.Fatalf("BuildPDF: %v", err)
	}
	srcImg := pdfimagesPNG(t, pdfBuf.Bytes())

	type point struct {
		size int
		psnr float64
	}
	var curve []point
	for _, qFactor := range []float64{0.1, 0.2, 0.4, 0.7, 1.0, 1.5, 2.0} {
		gsOut := gsRecompressPDF(t, pdfBuf.Bytes(), qFactor)
		gsImg := pdfimagesPNG(t, gsOut)
		curve = append(curve, point{len(gsOut), psnrRGB(srcImg, gsImg)})
	}
	sort.Slice(curve, func(i, j int) bool { return curve[i].size < curve[j].size })

	interp := func(size int) float64 {
		if size <= curve[0].size {
			return curve[0].psnr
		}
		if size >= curve[len(curve)-1].size {
			return curve[len(curve)-1].psnr
		}
		for i := 1; i < len(curve); i++ {
			if size <= curve[i].size {
				a, b := curve[i-1], curve[i]
				frac := float64(size-a.size) / float64(b.size-a.size)
				return a.psnr + frac*(b.psnr-a.psnr)
			}
		}
		return curve[len(curve)-1].psnr
	}

	const psnrSlackDB = 1.5
	prevSize := 0
	for _, q := range []int{30, 50, 75, 90} {
		var out bytes.Buffer
		if err := Optimize(&out, bytes.NewReader(pdfBuf.Bytes()), OptimizeOptions{RecompressJPEG: true, JPEGQuality: q}); err != nil {
			t.Fatalf("Optimize quality %d: %v", q, err)
		}
		byblosImg := pdfimagesPNG(t, out.Bytes())
		sb, bb := srcImg.Bounds(), byblosImg.Bounds()
		if sb.Dx() != bb.Dx() || sb.Dy() != bb.Dy() {
			t.Fatalf("quality %d: dimensions differ: src %dx%d, byblos %dx%d", q, sb.Dx(), sb.Dy(), bb.Dx(), bb.Dy())
		}
		byblosSize := out.Len()
		// A rate-distortion comparison alone cannot catch a pass whose size
		// axis is disconnected from the request (see quantize's analogous
		// size-ratio gate): a no-op passthrough, or a quality that never
		// shrinks the quality-95 source, would still land somewhere on gs's
		// curve. Pin size directly: recompressing a quality-95 source must
		// shrink it at every quality on this ladder, and a lower requested
		// quality must not produce a LARGER output than a higher one.
		if byblosSize >= pdfBuf.Len() {
			t.Errorf("quality %d: output %d bytes is not smaller than the quality-95 input's %d", q, byblosSize, pdfBuf.Len())
		}
		if prevSize > 0 && byblosSize < prevSize {
			t.Errorf("quality %d: output %d bytes is smaller than a lower quality's %d bytes; want non-decreasing with quality", q, byblosSize, prevSize)
		}
		prevSize = byblosSize
		byblosPSNR := psnrRGB(srcImg, byblosImg)
		want := interp(byblosSize) - psnrSlackDB
		if byblosPSNR < want {
			t.Errorf("quality %d: PSNR %.2f dB < gs-interpolated %.2f dB - %.1f slack",
				q, byblosPSNR, want+psnrSlackDB, psnrSlackDB)
		}
	}
}
