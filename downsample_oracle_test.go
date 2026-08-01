package byblos

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// This file is the oracle-backed half of byb-b3's Downsample acceptance
// test: agreement with ghostscript's /Bicubic downsampler on a text-bearing
// image (design spec section 4).
//
// -dColorImageDownsampleType=/Bicubic is NOT optional: measured during
// design, gs's default Subsample path is nearest-neighbour (NearestNeighbor
// scores psnrRGB = +Inf against gs/Subsample, which only happens when the
// two are pixel-identical). Pinning /Bicubic is what makes CatmullRom the
// right x/image kernel to compare against, and the test below would be
// meaningless without it.

func gsDownsamplePDF(t *testing.T, inPDF string, dstDPI int) []byte {
	t.Helper()
	dir := t.TempDir()
	inPath := filepath.Join(dir, "in.pdf")
	outPath := filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(inPath, []byte(inPDF), 0o644); err != nil {
		t.Fatalf("writing gs input: %v", err)
	}
	cmd := exec.Command("gs", "-q", "-dNOPAUSE", "-dBATCH", "-sDEVICE=pdfwrite",
		"-sOutputFile="+outPath,
		"-dDownsampleColorImages=true",
		fmt.Sprintf("-dColorImageResolution=%d", dstDPI),
		"-dColorImageDownsampleType=/Bicubic",
		"-dAutoFilterColorImages=false",
		"-dColorImageFilter=/FlateEncode",
		"-f", inPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gs: %v: %s", err, out)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("reading gs output: %v", err)
	}
	return data
}

func flateEncodeRGB(t *testing.T, img image.Image) []byte {
	t.Helper()
	b := img.Bounds()
	px := make([]byte, 0, b.Dx()*b.Dy()*3)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			px = append(px, byte(r>>8), byte(g>>8), byte(bl>>8))
		}
	}
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(px); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	return buf.Bytes()
}

// TestDownsampleAgreesWithGhostscriptBicubic builds a 300 DPI page from
// corpus.Scanpage via BuildPDF, downsamples it two ways -- gs to 150 DPI
// with /Bicubic, and byblos.Downsample directly on the source image -- and
// requires exact output dimensions plus psnrRGB >= 34 dB.
//
// Scanpage, not Photo, is the deliberate choice here: on Photo (a smooth
// image) most scalers score in the mid-50s dB against gs/Bicubic --
// ApproxBiLinear 54.41, BiLinear 54.21, CatmullRom 52.36 -- and even
// NearestNeighbor clears 34 dB at 37.53, so Photo cannot discriminate at
// this threshold. On Scanpage, measured against the shipped BuildPDF-based
// harness: CatmullRom vs gs/Bicubic = 43.44 dB (9.4 dB of headroom over this
// test's 34 dB threshold), BiLinear = 38.83 dB (also passes), while
// ApproxBiLinear = 32.51 dB and NearestNeighbor = 22.99 dB both fail here --
// which is what makes the threshold meaningful. The threshold selects against
// the two nearest-neighbour-ish scalers, not uniquely for CatmullRom.
func TestDownsampleAgreesWithGhostscriptBicubic(t *testing.T) {
	requireTool(t, "gs")
	requireTool(t, "pdfimages")

	src := corpus.Scanpage()
	const srcDPI = 300
	const dstDPI = 150
	widthPt := float64(corpus.ImageW) * 72 / srcDPI
	heightPt := float64(corpus.ImageH) * 72 / srcDPI

	encoded := EncodedImage{
		Width:      corpus.ImageW,
		Height:     corpus.ImageH,
		BPC:        8,
		ColorSpace: ColorSpace{Name: "DeviceRGB"},
		Filter:     "FlateDecode",
		Data:       flateEncodeRGB(t, src),
	}
	var pdfBuf bytes.Buffer
	if err := BuildPDF(&pdfBuf, []BuildPage{{Image: encoded, WidthPt: widthPt, HeightPt: heightPt}}); err != nil {
		t.Fatalf("BuildPDF: %v", err)
	}

	gsOut := gsDownsamplePDF(t, pdfBuf.String(), dstDPI)
	gsImg := pdfimagesPNG(t, gsOut)

	byblosImg, err := Downsample(src, srcDPI, dstDPI)
	if err != nil {
		t.Fatalf("Downsample: %v", err)
	}

	gb, bb := gsImg.Bounds(), byblosImg.Bounds()
	if gb.Dx() != bb.Dx() || gb.Dy() != bb.Dy() {
		t.Fatalf("dimensions differ: gs %dx%d, byblos %dx%d", gb.Dx(), gb.Dy(), bb.Dx(), bb.Dy())
	}

	const wantPSNR = 34.0
	got := psnrRGB(gsImg, byblosImg)
	if got < wantPSNR {
		t.Errorf("psnrRGB(gs/Bicubic, byblos CatmullRom) = %.2f dB; want >= %.1f dB", got, wantPSNR)
	}
}
