package byblos

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"math/rand"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// This file is the RED stage of byb-b3's Downsample. Downsample does not
// exist yet, so nothing here compiles -- that is the point.
//
// Signature (design spec, byb-b3):
//
//	func Downsample(img image.Image, srcDPI, dstDPI float64) (image.Image, error)

func TestDownsampleNilImageErrors(t *testing.T) {
	if _, err := Downsample(nil, 300, 150); err == nil {
		t.Fatal("Downsample(nil, ...): want error, got nil")
	}
}

func TestDownsampleEmptyBoundsErrors(t *testing.T) {
	empty := image.NewRGBA(image.Rect(0, 0, 0, 0))
	if _, err := Downsample(empty, 300, 150); err == nil {
		t.Fatal("Downsample on an empty-bounds image: want error, got nil")
	}
}

func TestDownsampleBadDPIErrors(t *testing.T) {
	img := corpus.Scanpage()
	for _, tc := range []struct {
		name           string
		srcDPI, dstDPI float64
	}{
		{"src zero", 0, 150},
		{"src negative", -300, 150},
		{"dst zero", 300, 0},
		{"dst negative", 300, -150},
		{"src NaN", math.NaN(), 150},
		{"dst NaN", 300, math.NaN()},
		{"src +Inf", math.Inf(1), 150},
		{"dst +Inf", 300, math.Inf(1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Downsample(img, tc.srcDPI, tc.dstDPI); err == nil {
				t.Fatalf("Downsample(_, %v, %v): want error, got nil", tc.srcDPI, tc.dstDPI)
			}
		})
	}
}

// TestDownsampleNoOpWhenSrcNotGreaterThanDst checks the design's settled
// no-op rule: srcDPI <= dstDPI returns the input image unchanged (and
// specifically does not error -- Kleio's compression ladder must be able to
// ask for 300 on a 150 DPI scan and get the scan back, not a failure).
func TestDownsampleNoOpWhenSrcNotGreaterThanDst(t *testing.T) {
	img := corpus.Scanpage()
	for _, tc := range []struct{ src, dst float64 }{
		{150, 300}, {150, 150},
	} {
		out, err := Downsample(img, tc.src, tc.dst)
		if err != nil {
			t.Fatalf("Downsample(_, %v, %v): %v", tc.src, tc.dst, err)
		}
		if out != image.Image(img) {
			t.Errorf("Downsample(_, %v, %v) did not return the identical image value", tc.src, tc.dst)
		}
	}
}

// TestDownsampleOutputDimensions checks the output is scaled by the DPI
// ratio, rounded, floored at 1.
func TestDownsampleOutputDimensions(t *testing.T) {
	img := corpus.Scanpage() // 600x800
	out, err := Downsample(img, 300, 150)
	if err != nil {
		t.Fatalf("Downsample: %v", err)
	}
	wantW := int(math.Round(float64(corpus.ImageW) * 150 / 300))
	wantH := int(math.Round(float64(corpus.ImageH) * 150 / 300))
	b := out.Bounds()
	if b.Dx() != wantW || b.Dy() != wantH {
		t.Errorf("Downsample(300->150) output %dx%d; want %dx%d", b.Dx(), b.Dy(), wantW, wantH)
	}
}

// TestDownsampleRatioRoundsToIdenticalDimensionsIsNoOp checks the second
// no-op case the design settled: even with srcDPI > dstDPI, if the rounded
// output dimensions equal the input's, Downsample returns the input
// unchanged rather than a resampled copy that happens to be the same size.
func TestDownsampleRatioRoundsToIdenticalDimensionsIsNoOp(t *testing.T) {
	img := corpus.Scanpage()
	// A tiny DPI delta that rounds back to the same pixel dimensions.
	out, err := Downsample(img, 300, 299.9)
	if err != nil {
		t.Fatalf("Downsample: %v", err)
	}
	if out != image.Image(img) {
		t.Error("Downsample with a ratio rounding to identical dimensions did not return the input unchanged")
	}
}

// TestDownsampleGraySurvivesAsGray checks a *image.Gray in produces a
// *image.Gray out -- CatmullRom.Scale accepts a Gray destination directly.
func TestDownsampleGraySurvivesAsGray(t *testing.T) {
	gray := image.NewGray(image.Rect(0, 0, corpus.ImageW, corpus.ImageH))
	for y := 0; y < corpus.ImageH; y++ {
		for x := 0; x < corpus.ImageW; x++ {
			gray.SetGray(x, y, color.Gray{Y: uint8((x + y) % 256)})
		}
	}
	out, err := Downsample(gray, 300, 150)
	if err != nil {
		t.Fatalf("Downsample: %v", err)
	}
	if _, ok := out.(*image.Gray); !ok {
		t.Errorf("Downsample on *image.Gray returned %T; want *image.Gray", out)
	}
}

// TestDownsampleSolidRegionSurvives checks a known solid-colour region is
// not garbage after resampling: a Scanpage's paper region, well away from any
// edge, must remain close to the paper colour.
func TestDownsampleSolidRegionSurvives(t *testing.T) {
	out, err := Downsample(corpus.Scanpage(), 300, 150)
	if err != nil {
		t.Fatalf("Downsample: %v", err)
	}
	// (10,200) in the half-size output maps to source row ~400 -- well
	// inside the paper region. (10,10) (source row ~20) is NOT a safe
	// choice: it sits exactly on the letterhead band's boundary (source rows
	// 20-59), and CatmullRom.Scale's kernel support at a 2x downsample ratio
	// reaches source rows 17-24, mixing band into every scaler's output
	// there (measured: CatmullRom, ApproxBiLinear, BiLinear and
	// NearestNeighbor all return something between paper and band at (10,10)
	// on this fixture).
	r, g, b, _ := out.At(10, 200).RGBA()
	wantR, wantG, wantB := uint32(250), uint32(246), uint32(236)
	tol := uint32(10)
	if absDiff(r>>8, wantR) > tol || absDiff(g>>8, wantG) > tol || absDiff(b>>8, wantB) > tol {
		t.Errorf("Downsample paper region at (10,10) = (%d,%d,%d); want close to (%d,%d,%d)",
			r>>8, g>>8, b>>8, wantR, wantG, wantB)
	}
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

// TestDownsampleNonZeroOriginBounds checks a source image whose bounds do
// not start at (0,0) is handled correctly.
func TestDownsampleNonZeroOriginBounds(t *testing.T) {
	full := corpus.Scanpage()
	sub := image.NewRGBA(image.Rect(100, 200, 100+corpus.ImageW, 200+corpus.ImageH))
	for y := sub.Bounds().Min.Y; y < sub.Bounds().Max.Y; y++ {
		for x := sub.Bounds().Min.X; x < sub.Bounds().Max.X; x++ {
			sub.Set(x, y, full.At(x-100, y-200))
		}
	}
	out, err := Downsample(sub, 300, 150)
	if err != nil {
		t.Fatalf("Downsample on a non-zero-origin image: %v", err)
	}
	wantW := int(math.Round(float64(corpus.ImageW) * 150 / 300))
	wantH := int(math.Round(float64(corpus.ImageH) * 150 / 300))
	b := out.Bounds()
	if b.Dx() != wantW || b.Dy() != wantH {
		t.Errorf("Downsample non-zero-origin output %dx%d; want %dx%d", b.Dx(), b.Dy(), wantW, wantH)
	}
}

// bitonalScanpage is corpus.Scanpage forced to pure black and white: the shape
// of a bitonal text scan, as an *image.RGBA, which is what a decoded 1-bpc PDF
// image actually looks like in memory (pdfcpu renders it to PNG and
// image.Decode returns *image.RGBA -- measured, and asserted by
// TestDownsampleDeclaredBPC1KeepsABilevelSourceBilevel below).
//
// The same pixels serve both sides of byb-plj, which is the point: declared
// 1 bpc they must be subsampled, declared 8 bpc they must be interpolated, and
// nothing about the pixels themselves can tell the two apart.
func bitonalScanpage() *image.RGBA {
	src := corpus.Scanpage()
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, _ := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			// Rec. 601 luma, the weighting a scanner's bitonal threshold uses.
			luma := (299*float64(r>>8) + 587*float64(g>>8) + 114*float64(bl>>8)) / 1000
			v := uint8(0)
			if luma >= 128 {
				v = 255
			}
			out.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return out
}

// distinctLevels returns the distinct 8-bit channel values in img. A bilevel
// image has exactly two; anything more on a bilevel source is a grey level the
// resampling kernel invented and 1 bpc cannot store.
func distinctLevels(img image.Image) map[uint32]bool {
	levels := map[uint32]bool{}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, _, _, _ := img.At(x, y).RGBA()
			levels[r>>8] = true
		}
	}
	return levels
}

// packDeviceGray1 packs img into 1-bpc /DeviceGray samples, MSB first, one row
// per (Width+7)/8 bytes. DeviceGray 1 is WHITE (the inverse of byblos.Bitmap,
// where a set bit is black ink), so a pixel at or above mid-grey sets its bit.
func packDeviceGray1(img image.Image) []byte {
	b := img.Bounds()
	stride := (b.Dx() + 7) / 8
	out := make([]byte, stride*b.Dy())
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			if r, _, _, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA(); r>>8 >= 128 {
				out[y*stride+x/8] |= 1 << (7 - uint(x)%8)
			}
		}
	}
	return out
}

// bitonalPDF is a one-page PDF whose only image is declared
// /BitsPerComponent 1 /DeviceGray /FlateDecode -- exactly the shape byb-plj
// reports on -- placed so its pixels land at srcDPI.
func bitonalPDF(t *testing.T, img image.Image, srcDPI float64) []byte {
	t.Helper()
	b := img.Bounds()
	enc := EncodedImage{
		Width:      b.Dx(),
		Height:     b.Dy(),
		BPC:        1,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       flateEncode(t, packDeviceGray1(img)),
	}
	var buf bytes.Buffer
	if err := BuildPDF(&buf, []BuildPage{{
		Image:    enc,
		WidthPt:  float64(b.Dx()) * 72 / srcDPI,
		HeightPt: float64(b.Dy()) * 72 / srcDPI,
	}}); err != nil {
		t.Fatalf("BuildPDF: %v", err)
	}
	return buf.Bytes()
}

// TestDownsampleDeclaredBPC1KeepsABilevelSourceBilevel is byb-plj end to end,
// on the exact path the bead reports: a PDF image declared /BitsPerComponent 1
// goes in, ExtractPageRaster decodes it, and the decoded raster is downsampled.
//
// It asserts the reachability finding the fix is built on as well as the fix.
// Inspect DOES know the declaration -- ImageRef.Bitonal is true, keyed by
// ObjNr -- while the decoded raster does NOT: it comes back with 8-bit
// channels holding two distinct values, and no depth. That gap is why
// Downsample cannot be repaired in place and why DownsampleDeclaredBPC takes
// the depth as an argument. Handed the same raster with no depth, Downsample
// returns 99 distinct levels here (measured); handed the declaration, this
// returns 2.
//
// Kill power, with no oracle involved:
//   - kernel reverted to Catmull-Rom for declaredBPC 1: the level count fails
//     (99 levels, not 2).
//   - body gutted to `return img, nil`: the dimension check fails (600x800,
//     not 300x400). This is the mutation the previous attempt at byb-plj
//     shipped green.
func TestDownsampleDeclaredBPC1KeepsABilevelSourceBilevel(t *testing.T) {
	src := bitonalScanpage()
	pdf := bitonalPDF(t, src, 300)

	pages, err := Inspect(bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(pages) != 1 || len(pages[0].Images) != 1 {
		t.Fatalf("Inspect returned %d pages; want one page with one image", len(pages))
	}
	if !pages[0].Images[0].Bitonal {
		t.Fatal("ImageRef.Bitonal = false on a /BitsPerComponent 1 image; " +
			"the declared depth is what DownsampleDeclaredBPC needs and Inspect is where a caller reads it")
	}

	raster, err := ExtractPageRaster(bytes.NewReader(pdf), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
	if levels := distinctLevels(raster.Image); len(levels) != 2 {
		t.Fatalf("the extracted raster has %d distinct levels %v; want the 2 the 1-bpc source had", len(levels), levels)
	}

	out, err := DownsampleDeclaredBPC(raster.Image, 1, 300, 150)
	if err != nil {
		t.Fatalf("DownsampleDeclaredBPC: %v", err)
	}
	if b := out.Bounds(); b.Dx() != 300 || b.Dy() != 400 {
		t.Errorf("DownsampleDeclaredBPC(_, 1, 300, 150) output %dx%d; want 300x400", b.Dx(), b.Dy())
	}
	levels := distinctLevels(out)
	if len(levels) != 2 || !levels[0] || !levels[255] {
		t.Errorf("DownsampleDeclaredBPC(_, 1, ...) on a bilevel raster produced levels %v; "+
			"want exactly {0, 255} -- a 1-bpc image cannot store anything else", levels)
	}
}

// TestDownsampleDeclaredBPC1SubsamplesRatherThanBlursAndThresholds is the
// oracle-free half of byb-plj's kernel identity. The oracle-backed
// TestDownsampleDeclaredBPC1AgreesWithGhostscriptSubsample
// (downsample_oracle_test.go) distinguishes true point subsampling from
// CatmullRom-then-threshold-to-{0,255}, but only when gs is on PATH; the
// no-oracles CI configuration runs without it, and nothing else in this
// package tells the two apart -- both produce dimension-correct,
// exactly-two-level output on this package's other bilevel fixtures.
//
// This test does, with no external tool. Point subsampling by an exact
// integer factor k is a FIXED spatial phase applied uniformly across the
// whole image: out[x,y] == src[k*x+px, k*y+py] for one (px,py) shared by
// every destination pixel. A blur (of any kernel) followed by a threshold is
// a local majority vote instead -- it does not reduce to copying a single
// source pixel per destination pixel. On a per-pixel-random bilevel source,
// resample-then-threshold's output will not match the source at ANY single
// global phase, while true subsampling always will.
func TestDownsampleDeclaredBPC1SubsamplesRatherThanBlursAndThresholds(t *testing.T) {
	const k = 2
	const w, h = 64, 64
	rng := rand.New(rand.NewSource(1))
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := uint8(0)
			if rng.Intn(2) == 1 {
				v = 255
			}
			src.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	const srcDPI, dstDPI = 300, 150 // ratio 1/k
	out, err := DownsampleDeclaredBPC(src, 1, srcDPI, dstDPI)
	if err != nil {
		t.Fatalf("DownsampleDeclaredBPC: %v", err)
	}
	ob := out.Bounds()
	if ob.Dx() != w/k || ob.Dy() != h/k {
		t.Fatalf("output %dx%d; want %dx%d", ob.Dx(), ob.Dy(), w/k, h/k)
	}
	matched := false
	for py := 0; py < k && !matched; py++ {
		for px := 0; px < k && !matched; px++ {
			all := true
			for y := 0; y < ob.Dy() && all; y++ {
				for x := 0; x < ob.Dx(); x++ {
					or, _, _, _ := out.At(x, y).RGBA()
					sr, _, _, _ := src.At(k*x+px, k*y+py).RGBA()
					if or>>8 != sr>>8 {
						all = false
						break
					}
				}
			}
			if all {
				matched = true
			}
		}
	}
	if !matched {
		t.Error("DownsampleDeclaredBPC(_, 1, ...) output does not equal the source subsampled at any fixed phase; " +
			"true point subsampling copies one source pixel per destination pixel at a single phase shared by the " +
			"whole image, which a blur-then-threshold kernel cannot reproduce on random bilevel noise")
	}
}

// TestDownsampleDoesNotInferBilevelFromPixels is the other half of byb-plj, and
// the reason the first attempt at it was rejected. An 8-bpc source whose pixels
// happen to be pure black and white -- a bitonal scan widened to 8 bpc, which is
// legitimate and common -- is not a mono image to any PDF tool, and Ghostscript
// sends it through /ColorImageDownsampleType like any other colour image. So
// Downsample must go on interpolating it, and the pixels must not be consulted.
//
// This needs no oracle to discriminate: Catmull-Rom must invent intermediate
// greys on this input and point subsampling cannot invent any. The oracle-backed
// cost of getting it wrong was measured at 41.66 dB down to 21.24 dB against
// this package's 34 dB gate.
func TestDownsampleDoesNotInferBilevelFromPixels(t *testing.T) {
	out, err := Downsample(bitonalScanpage(), 300, 150)
	if err != nil {
		t.Fatalf("Downsample: %v", err)
	}
	levels := distinctLevels(out)
	if len(levels) <= 2 {
		t.Errorf("Downsample produced only %v on a pure-black-and-white 8-bpc source: "+
			"it inferred bilevel-ness from pixel data instead of interpolating. "+
			"Depth is declared, not detected -- DownsampleDeclaredBPC(img, 1, ...) is for a source declared 1 bpc", levels)
	}
}

// TestDownsampleDeclaredBPCRejectsAnUndeclarableDepth pins the validation.
// 0 matters most: it is pdfdoc.ImageInfo.BPC's "no /BitsPerComponent entry", so
// a caller forwarding an unknown depth must get an error and not the contone
// kernel by default -- being silently given the contone kernel IS byb-plj.
func TestDownsampleDeclaredBPCRejectsAnUndeclarableDepth(t *testing.T) {
	img := bitonalScanpage()
	for _, bpc := range []int{0, -1, 3, 5, 7, 9, 24, 32} {
		if _, err := DownsampleDeclaredBPC(img, bpc, 300, 150); err == nil {
			t.Errorf("DownsampleDeclaredBPC(_, %d, ...): want an error, got nil", bpc)
		}
	}
	for _, bpc := range []int{1, 2, 4, 8, 16} {
		if _, err := DownsampleDeclaredBPC(img, bpc, 300, 150); err != nil {
			t.Errorf("DownsampleDeclaredBPC(_, %d, ...): %v", bpc, err)
		}
	}
}

// TestDownsampleDeclaredBPC1ObeysDownsamplesRules pins, on the bilevel path
// specifically, every rule DownsampleDeclaredBPC's doc comment claims it shares
// with Downsample. The previous attempt at byb-plj made that same claim in a
// doc comment and pinned none of it, which is why its bilevel function passed
// the whole suite when gutted to `return img, nil`.
func TestDownsampleDeclaredBPC1ObeysDownsamplesRules(t *testing.T) {
	img := bitonalScanpage()

	if _, err := DownsampleDeclaredBPC(nil, 1, 300, 150); err == nil {
		t.Error("DownsampleDeclaredBPC(nil, 1, ...): want an error, got nil")
	}
	empty := image.NewRGBA(image.Rect(0, 0, 0, 0))
	if _, err := DownsampleDeclaredBPC(empty, 1, 300, 150); err == nil {
		t.Error("DownsampleDeclaredBPC on empty bounds: want an error, got nil")
	}
	for _, tc := range []struct{ src, dst float64 }{
		{0, 150}, {-300, 150}, {300, 0}, {300, -150},
		{math.NaN(), 150}, {300, math.NaN()},
		{math.Inf(1), 150}, {300, math.Inf(1)},
	} {
		if _, err := DownsampleDeclaredBPC(img, 1, tc.src, tc.dst); err == nil {
			t.Errorf("DownsampleDeclaredBPC(_, 1, %v, %v): want an error, got nil", tc.src, tc.dst)
		}
	}
	// The two no-op rules: not merely an image of the same size, the identical
	// image value.
	for _, tc := range []struct{ src, dst float64 }{
		{150, 300}, {150, 150}, {300, 299.9},
	} {
		out, err := DownsampleDeclaredBPC(img, 1, tc.src, tc.dst)
		if err != nil {
			t.Fatalf("DownsampleDeclaredBPC(_, 1, %v, %v): %v", tc.src, tc.dst, err)
		}
		if out != image.Image(img) {
			t.Errorf("DownsampleDeclaredBPC(_, 1, %v, %v) did not return the identical image value", tc.src, tc.dst)
		}
	}
	// And the resampling case really does resample, so none of the above is
	// passing because the function returns its input for everything.
	out, err := DownsampleDeclaredBPC(img, 1, 300, 150)
	if err != nil {
		t.Fatalf("DownsampleDeclaredBPC: %v", err)
	}
	if b := out.Bounds(); b.Dx() != 300 || b.Dy() != 400 {
		t.Errorf("DownsampleDeclaredBPC(_, 1, 300, 150) output %dx%d; want 300x400", b.Dx(), b.Dy())
	}
}

// TestDownsampleUsesTheContoneKernelDownsampleDeclaredBPCDoes pins the binding
// Downsample's doc comment states, which is what keeps the two entry points
// from drifting into two implementations: Downsample is not a peer of
// DownsampleDeclaredBPC, it is one call of it with the depth it always
// silently assumed.
//
// It cannot pin the literal value 8 by itself: DownsampleDeclaredBPC's switch
// branches only on declaredBPC == 1 (NearestNeighbor) vs everything else
// (CatmullRom), so 2, 4, 8 and 16 are pixel-identical by construction and no
// output comparison can tell them apart. What IS observable, and what this
// test asserts, is that Downsample takes the contone (non-1) branch rather
// than the bilevel one -- comparing only against declaredBPC 8 let a
// delegation change to any of 2, 4 or 16 pass silently, so this also checks
// against 2, 4 and 16, and requires the declaredBPC-1 output to differ so a
// regression to the bilevel branch is still caught.
func TestDownsampleUsesTheContoneKernelDownsampleDeclaredBPCDoes(t *testing.T) {
	for _, src := range []image.Image{corpus.Scanpage(), bitonalScanpage()} {
		plain, err := Downsample(src, 300, 150)
		if err != nil {
			t.Fatalf("Downsample: %v", err)
		}
		for _, bpc := range []int{2, 4, 8, 16} {
			declared, err := DownsampleDeclaredBPC(src, bpc, 300, 150)
			if err != nil {
				t.Fatalf("DownsampleDeclaredBPC(_, %d, ...): %v", bpc, err)
			}
			pb, db := plain.Bounds(), declared.Bounds()
			if pb != db {
				t.Fatalf("bounds differ: Downsample %v, DownsampleDeclaredBPC(_, %d, ...) %v", pb, bpc, db)
			}
			for y := pb.Min.Y; y < pb.Max.Y; y++ {
				for x := pb.Min.X; x < pb.Max.X; x++ {
					if plain.At(x, y) != declared.At(x, y) {
						t.Fatalf("pixel (%d,%d) differs: Downsample %v, DownsampleDeclaredBPC(_, %d, ...) %v",
							x, y, plain.At(x, y), bpc, declared.At(x, y))
					}
				}
			}
		}
		bilevel, err := DownsampleDeclaredBPC(src, 1, 300, 150)
		if err != nil {
			t.Fatalf("DownsampleDeclaredBPC(_, 1, ...): %v", err)
		}
		same := true
		pb := plain.Bounds()
		for y := pb.Min.Y; y < pb.Max.Y && same; y++ {
			for x := pb.Min.X; x < pb.Max.X; x++ {
				if plain.At(x, y) != bilevel.At(x, y) {
					same = false
					break
				}
			}
		}
		if same {
			t.Fatalf("Downsample matches DownsampleDeclaredBPC(_, 1, ...) pixel for pixel; " +
				"it should be taking the contone (CatmullRom) branch, not the bilevel (NearestNeighbor) one")
		}
	}
}
