package byblos

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"image"
	"os"
	"runtime"
	"slices"
	"testing"
)

// B2: PageRaster carries the chosen image XObject's source, and a caller that
// asks for it gets a declared-bitonal image as a packed Bitmap instead of a
// widened *image.RGBA. Every case runs through ExtractPageRaster and through
// Document.PageRaster, which must agree.

func zlibBytes(b []byte) []byte {
	var o bytes.Buffer
	w := zlib.NewWriter(&o)
	w.Write(b)
	w.Close()
	return o.Bytes()
}

// onePageImagePDF is a w x h page painted edge to edge by object 5, an image
// XObject with the given dictionary entries and stream bytes. extra objects
// follow as 6, 7, ...
func onePageImagePDF(w, h int, dict string, data []byte, extra ...string) []byte {
	body := fmt.Sprintf("q %d 0 0 %d 0 0 cm 0 g /Im0 Do Q", w, h)
	bodies := []string{catalog, pages1,
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents 4 0 R /Resources << /XObject << /Im0 5 0 R >> >> >>", w, h),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(body), body),
		fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d %s /Length %d >>\nstream\n%s\nendstream", w, h, dict, len(data), data)}
	return pdfFile(append(bodies, extra...), "", 0)
}

func streamObj(dict string, data []byte) string {
	return fmt.Sprintf("<< /Type /XObject /Subtype /Image %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
}

// rasters extracts page 1 of pdf one-shot and through an open Document, under lim.
func rasters(t *testing.T, pdf []byte, lim Limits) map[string]*PageRaster {
	t.Helper()
	one, err := ExtractPageRasterLimits(context.Background(), bytes.NewReader(pdf), 1, lim)
	if err != nil {
		t.Fatalf("ExtractPageRasterLimits: %v", err)
	}
	doc, err := OpenDocument(context.Background(), bytes.NewReader(pdf), lim)
	if err != nil {
		t.Fatalf("OpenDocument: %v", err)
	}
	defer doc.Close()
	open, err := doc.PageRaster(context.Background(), 1)
	if err != nil {
		t.Fatalf("Document.PageRaster: %v", err)
	}
	return map[string]*PageRaster{"one-shot": one, "open-once": open}
}

func TestPageRasterSourceCarriesTheDCTBytes(t *testing.T) {
	enc, _, err := EmbedJPEG(dcOnlyJPEG(64, 48, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := BuildPDF(&buf, []BuildPage{{Image: enc, WidthPt: 64, HeightPt: 48}}); err != nil {
		t.Fatal(err)
	}
	for name, pr := range rasters(t, buf.Bytes(), Limits{}) {
		s := pr.Source
		if !bytes.Equal(s.Data, enc.Data) {
			t.Errorf("%s: Source.Data is %d bytes, not the %d EmbedJPEG wrote", name, len(s.Data), len(enc.Data))
		}
		if !slices.Equal(s.Filters, []string{"DCTDecode"}) || s.BPC != 8 || s.ColorSpace != enc.ColorSpace.Name || s.SMask || s.Mask || s.Globals != nil {
			t.Errorf("%s: Source = filters %v bpc %d cs %q smask %v mask %v globals %d", name, s.Filters, s.BPC, s.ColorSpace, s.SMask, s.Mask, len(s.Globals))
		}
	}
}

func TestPageRasterSourceCarriesJBIG2AndItsGlobals(t *testing.T) {
	page, globals := symbolFixture(t, "symbol-page.jb2"), symbolFixture(t, "symbol-globals.jb2")
	pdf := jbig2GlobalsPDF(page, globals, 640, 480)
	for name, pr := range rasters(t, pdf, Limits{}) {
		s := pr.Source
		if !bytes.Equal(s.Data, page) || !bytes.Equal(s.Globals, globals) {
			t.Errorf("%s: Source.Data %d bytes, Globals %d; want %d and %d", name, len(s.Data), len(s.Globals), len(page), len(globals))
		}
		if !slices.Equal(s.Filters, []string{"JBIG2Decode"}) || s.BPC != 1 || s.ColorSpace != "DeviceGray" {
			t.Errorf("%s: Source = filters %v bpc %d cs %q", name, s.Filters, s.BPC, s.ColorSpace)
		}
		if _, ok := pr.Image.(*image.Gray); !ok || pr.Bitmap != nil {
			t.Errorf("%s: without the option the raster is %T and Bitmap %v; want *image.Gray and nil", name, pr.Image, pr.Bitmap)
		}
	}
	for name, pr := range rasters(t, pdf, Limits{PackedBitonal: true}) {
		if pr.Image != nil || pr.Bitmap == nil {
			t.Fatalf("%s: packed: Image %T Bitmap %v", name, pr.Image, pr.Bitmap)
		}
		if n := bitmapInk(pr.Bitmap); n != symbolFixtureInk {
			t.Errorf("%s: Bitmap has %d set bits; want %d", name, n, symbolFixtureInk)
		}
		if !bytes.Equal(pr.Source.Data, page) || !bytes.Equal(pr.Source.Globals, globals) {
			t.Errorf("%s: packed: Source lost the streams", name)
		}
	}
}

// Source.Globals must be the caller's own copy: for an unfiltered globals
// stream pdfcpu's Content is the same slice as the xref entry's Raw, so a write
// through it would corrupt the next page's decode.
func TestPageRasterSourceGlobalsAreACopy(t *testing.T) {
	page, globals := symbolFixture(t, "symbol-page.jb2"), symbolFixture(t, "symbol-globals.jb2")
	pdf := jbig2GlobalsPDF(page, globals, 640, 480)
	for _, lim := range []Limits{{}, {PackedBitonal: true}} {
		doc, err := OpenDocument(context.Background(), bytes.NewReader(pdf), lim)
		if err != nil {
			t.Fatal(err)
		}
		pr, err := doc.PageRaster(context.Background(), 1)
		if err != nil {
			t.Fatal(err)
		}
		clear(pr.Source.Globals)
		clear(pr.Source.Data)
		again, err := doc.PageRaster(context.Background(), 1)
		if err != nil {
			t.Fatalf("PageRaster after the caller wrote to Source: %v", err)
		}
		if !bytes.Equal(again.Source.Globals, globals) || !bytes.Equal(again.Source.Data, page) {
			t.Error("second read's Source differs from the document's streams")
		}
		doc.Close()
	}
}

func bitmapInk(b *Bitmap) int {
	n := 0
	for y := 0; y < b.Height; y++ {
		for x := 0; x < b.Width; x++ {
			n += int(b.At(x, y))
		}
	}
	return n
}

// ccittWedge is a w x h Group 4 stream of a black wedge: row y is white up to
// column wedgeStart(w, y) and black from there. Every row is coded with vertical
// modes against the one above, so the encoder is a few lines.
func wedgeStart(w, y int) int { return max(w-3*(y+1), 0) }

func ccittWedge(w, h int) []byte {
	var out []byte
	var acc, n uint
	put := func(code string) {
		for _, c := range code {
			acc = acc<<1 | uint(c-'0')
			if n++; n == 8 {
				out, acc, n = append(out, byte(acc)), 0, 0
			}
		}
	}
	vl := []string{"1", "010", "000010", "0000010"} // V0, VL1, VL2, VL3
	prev := w
	for y := 0; y < h; y++ {
		s := wedgeStart(w, y)
		put(vl[prev-s]) // the white-to-black change, against the one above (or the row's end)
		put("1")        // the black-to-white change at the row's end
		prev = s
	}
	for ; n != 0; n = (n + 1) % 8 {
		acc <<= 1
	}
	return out
}

const ccittDict = "/ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /CCITTFaxDecode /DecodeParms << /K -1 /Columns %d /Rows %d >>"

func TestPackedBitmapOfACCITTPageEqualsTheThresholdedRGBA(t *testing.T) {
	const w, h = 2400, 3200 // an RGBA of 30 MiB against a packed 960 KB
	pdf := onePageImagePDF(w, h, fmt.Sprintf(ccittDict, w, h), ccittWedge(w, h))

	for name, wide := range rasters(t, pdf, Limits{}) {
		rgba, ok := wide.Image.(*image.RGBA)
		if !ok {
			t.Fatalf("%s: default raster is %T", name, wide.Image)
		}
		if !slices.Equal(wide.Source.Filters, []string{"CCITTFaxDecode"}) || wide.Source.BPC != 1 {
			t.Errorf("%s: Source = %+v", name, wide.Source)
		}
		want := NewBitmap(w, h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if rgba.Pix[y*rgba.Stride+4*x] < 128 {
					want.Set(x, y, 1)
				}
			}
		}
		if wedgeAt := want.At(w-1, 0); wedgeAt != 1 || want.At(0, 0) != 0 || want.At(0, h-1) != 1 {
			t.Fatalf("%s: the RGBA decode is not the wedge the fixture encodes", name)
		}

		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		var got *PageRaster
		if name == "one-shot" {
			got = rasters1(t, pdf, Limits{PackedBitonal: true})
		} else {
			got = rasters2(t, pdf, Limits{PackedBitonal: true})
		}
		runtime.ReadMemStats(&m1)

		if got.Image != nil || got.Bitmap == nil {
			t.Fatalf("%s: packed: Image %T Bitmap %v", name, got.Image, got.Bitmap)
		}
		if !bytes.Equal(got.Bitmap.Pix, want.Pix) || got.Bitmap.Width != w || got.Bitmap.Height != h || got.Bitmap.Stride != want.Stride {
			t.Errorf("%s: Bitmap differs from the RGBA thresholded at 128", name)
		}
		if d := int64(m1.TotalAlloc - m0.TotalAlloc); d >= 4*w*h {
			t.Errorf("%s: packed extraction allocated %d bytes; the RGBA alone is %d", name, d, 4*w*h)
		} else {
			t.Logf("%s: packed extraction allocated %d bytes (RGBA %d)", name, d, 4*w*h)
		}
		if !got.Bitonal {
			t.Errorf("%s: Bitonal not set", name)
		}
	}
}

// rasters1 and rasters2 extract once, one-shot and open-once, so the allocation
// each costs can be measured alone.
func rasters1(t *testing.T, pdf []byte, lim Limits) *PageRaster {
	t.Helper()
	pr, err := ExtractPageRasterLimits(context.Background(), bytes.NewReader(pdf), 1, lim)
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

func rasters2(t *testing.T, pdf []byte, lim Limits) *PageRaster {
	t.Helper()
	doc, err := OpenDocument(context.Background(), bytes.NewReader(pdf), lim)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	pr, err := doc.PageRaster(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

// 1-bpc Flate: sample 0 is black by default, /Decode [1 0] swaps them, a
// non-gray or masked image stays wide, and an unpacked read of the same page
// agrees with the packed one.
func TestPackedBitmapOfAOneBitFlateImage(t *testing.T) {
	rows := []byte{0xF0, 0x0F, 0xAA, 0x55} // 8 x 4
	for _, c := range []struct {
		name, dict string
		want       [4][8]uint8 // 1 = ink
	}{
		{"default", "", [4][8]uint8{{0, 0, 0, 0, 1, 1, 1, 1}, {1, 1, 1, 1, 0, 0, 0, 0}, {0, 1, 0, 1, 0, 1, 0, 1}, {1, 0, 1, 0, 1, 0, 1, 0}}},
		{"inverted", "/Decode [1 0]", [4][8]uint8{{1, 1, 1, 1, 0, 0, 0, 0}, {0, 0, 0, 0, 1, 1, 1, 1}, {1, 0, 1, 0, 1, 0, 1, 0}, {0, 1, 0, 1, 0, 1, 0, 1}}},
	} {
		pdf := onePageImagePDF(8, 4, "/ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /FlateDecode "+c.dict, zlibBytes(rows))
		for name, pr := range rasters(t, pdf, Limits{PackedBitonal: true}) {
			if pr.Bitmap == nil || pr.Image != nil {
				t.Fatalf("%s %s: Image %T Bitmap %v", c.name, name, pr.Image, pr.Bitmap)
			}
			for y := 0; y < 4; y++ {
				for x := 0; x < 8; x++ {
					if got := pr.Bitmap.At(x, y); got != c.want[y][x] {
						t.Errorf("%s %s: (%d,%d) = %d, want %d", c.name, name, x, y, got, c.want[y][x])
					}
				}
			}
			if s := pr.Source; !slices.Equal(s.Filters, []string{"FlateDecode"}) || s.BPC != 1 || s.Data != nil {
				t.Errorf("%s %s: Source = %+v", c.name, name, s)
			}
		}
		// The same page without the option is the RGBA these bits were thresholded from.
		for name, pr := range rasters(t, pdf, Limits{}) {
			rgba := pr.Image.(*image.RGBA)
			for y := 0; y < 4; y++ {
				for x := 0; x < 8; x++ {
					if ink := rgba.Pix[y*rgba.Stride+4*x] < 128; ink != (c.want[y][x] == 1) {
						t.Errorf("%s %s: RGBA (%d,%d) disagrees with the packed fixture", c.name, name, x, y)
					}
				}
			}
		}
	}
}

// What cannot be packed faithfully stays wide: an Indexed palette, an
// SMask, even at 1 bpc.
func TestPackedOptionLeavesWhatItCannotPackWide(t *testing.T) {
	cases := map[string][]byte{
		"indexed": onePageImagePDF(8, 4, "/ColorSpace [/Indexed /DeviceRGB 1 <FF000000FF00>] /BitsPerComponent 1 /Filter /FlateDecode", zlibBytes([]byte{0xF0, 0x0F, 0xAA, 0x55})),
		"smask": onePageImagePDF(8, 4, "/ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /FlateDecode /SMask 6 0 R", zlibBytes([]byte{0xF0, 0x0F, 0xAA, 0x55}),
			streamObj("/Width 8 /Height 4 /ColorSpace /DeviceGray /BitsPerComponent 8", bytes.Repeat([]byte{128}, 32))),
	}
	for c, pdf := range cases {
		for name, pr := range rasters(t, pdf, Limits{PackedBitonal: true}) {
			if pr.Image == nil || pr.Bitmap != nil {
				t.Errorf("%s %s: Image %T Bitmap %v; want a wide raster", c, name, pr.Image, pr.Bitmap)
			}
			if pr.Source.BPC != 1 {
				t.Errorf("%s %s: Source.BPC = %d", c, name, pr.Source.BPC)
			}
		}
	}
}

// Today's behaviour for masked and 16-bpc images, pinned. pdfcpu renders the
// image to a PNG and byblos decodes that:
//   - an /SMask survives as per-pixel alpha, in an *image.NRGBA (alpha
//     not premultiplied) the colour channels untouched;
//   - a stencil /Mask and a colour-key /Mask are ignored: the raster is opaque;
//   - a 16-bpc sample is reduced to 8 bits as floor(v * 255 / 65535), not v >> 8
//     (0x8000 arrives as 127, not 128).
func TestPinnedArrivalOfMaskedAndSixteenBitImages(t *testing.T) {
	grey := []byte{10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 110, 120, 130, 140, 150, 160}
	alpha := []byte{0, 17, 34, 51, 68, 85, 102, 119, 136, 153, 170, 187, 204, 221, 238, 255}
	const gray4 = "/Width 4 /Height 4 /ColorSpace /DeviceGray /BitsPerComponent 8"

	t.Run("smask", func(t *testing.T) {
		pdf := onePageImagePDF(4, 4, "/ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode /SMask 6 0 R", zlibBytes(grey),
			streamObj(gray4+" /Filter /FlateDecode", zlibBytes(alpha)))
		for name, pr := range rasters(t, pdf, Limits{}) {
			n, ok := pr.Image.(*image.NRGBA)
			if !ok {
				t.Fatalf("%s: raster is %T; want *image.NRGBA", name, pr.Image)
			}
			for i := 0; i < 16; i++ {
				if got, want := n.Pix[4*i:4*i+4], []byte{grey[i], grey[i], grey[i], alpha[i]}; !bytes.Equal(got, want) {
					t.Errorf("%s: pixel %d = %v, want %v", name, i, got, want)
				}
			}
			if !pr.Source.SMask || pr.Source.Mask {
				t.Errorf("%s: Source SMask %v Mask %v", name, pr.Source.SMask, pr.Source.Mask)
			}
		}
	})

	for _, c := range []struct {
		name, mask string
		obj        []string
	}{
		{"colour-key mask", "/Mask [10 40]", nil},
		{"stencil mask", "/Mask 6 0 R", []string{streamObj("/Width 4 /Height 4 /ImageMask true /BitsPerComponent 1", []byte{0xA0, 0x50, 0xA0, 0x50})}},
	} {
		t.Run(c.name, func(t *testing.T) {
			pdf := onePageImagePDF(4, 4, "/ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /FlateDecode "+c.mask, zlibBytes(grey), c.obj...)
			for name, pr := range rasters(t, pdf, Limits{}) {
				rgba, ok := pr.Image.(*image.RGBA)
				if !ok {
					t.Fatalf("%s: raster is %T; want *image.RGBA", name, pr.Image)
				}
				for i := 0; i < 16; i++ {
					if got, want := rgba.Pix[4*i:4*i+4], []byte{grey[i], grey[i], grey[i], 255}; !bytes.Equal(got, want) {
						t.Errorf("%s: pixel %d = %v, want an opaque %v", name, i, got, want)
					}
				}
				if !pr.Source.Mask || pr.Source.SMask {
					t.Errorf("%s: Source SMask %v Mask %v", name, pr.Source.SMask, pr.Source.Mask)
				}
			}
		})
	}

	t.Run("16 bpc gray", func(t *testing.T) {
		pdf := onePageImagePDF(4, 2, "/ColorSpace /DeviceGray /BitsPerComponent 16 /Filter /FlateDecode",
			zlibBytes([]byte{0x12, 0x34, 0xFF, 0xFF, 0x00, 0x01, 0x80, 0x00, 0xAB, 0xCD, 0x00, 0x00, 0x7F, 0xFF, 0x01, 0x02}))
		want := []byte{18, 255, 0, 127, 171, 0, 127, 1}
		for name, pr := range rasters(t, pdf, Limits{}) {
			rgba, ok := pr.Image.(*image.RGBA)
			if !ok {
				t.Fatalf("%s: raster is %T; want *image.RGBA", name, pr.Image)
			}
			for i, v := range want {
				if got := rgba.Pix[4*i : 4*i+4]; !bytes.Equal(got, []byte{v, v, v, 255}) {
					t.Errorf("%s: sample %d = %v, want %d", name, i, got, v)
				}
			}
			if pr.Source.BPC != 16 {
				t.Errorf("%s: Source.BPC = %d", name, pr.Source.BPC)
			}
		}
	})

	t.Run("16 bpc rgb", func(t *testing.T) {
		px := append(bytes.Repeat([]byte{0x12, 0x34, 0x56, 0x78, 0x9A, 0xBC}, 7), 0x80, 0x00, 0xFF, 0xFF, 0x00, 0x01)
		pdf := onePageImagePDF(4, 2, "/ColorSpace /DeviceRGB /BitsPerComponent 16 /Filter /FlateDecode", zlibBytes(px))
		for name, pr := range rasters(t, pdf, Limits{}) {
			rgba, ok := pr.Image.(*image.RGBA)
			if !ok {
				t.Fatalf("%s: raster is %T; want *image.RGBA", name, pr.Image)
			}
			if got := rgba.Pix[:4]; !bytes.Equal(got, []byte{18, 86, 154, 255}) {
				t.Errorf("%s: first pixel %v, want 18 86 154 255", name, got)
			}
			if got := rgba.Pix[28:32]; !bytes.Equal(got, []byte{127, 255, 0, 255}) {
				t.Errorf("%s: last pixel %v, want 127 255 0 255", name, got)
			}
		}
	})
}

// An image mask has no /ColorSpace, which pdfcpu's renderer refuses, so today
// it is no raster at all; asked for packed bits it is the painted pixels.
func TestPackedBitmapOfAnImageMask(t *testing.T) {
	pdf := onePageImagePDF(8, 2, "/ImageMask true /BitsPerComponent 1", []byte{0xF0, 0x0F})
	if _, err := ExtractPageRaster(bytes.NewReader(pdf), 1); err == nil {
		t.Fatal("an image mask extracted wide; this test's premise is gone")
	}
	for name, pr := range rasters(t, pdf, Limits{PackedBitonal: true}) {
		if pr.Bitmap == nil || !bytes.Equal(pr.Bitmap.Pix, []byte{0x0F, 0xF0}) || !pr.Bitonal {
			t.Errorf("%s: Bitmap %+v; want rows 0F F0 (sample 0 paints)", name, pr.Bitmap)
		}
	}
}

// A four-component DCT image is rendered to PNG by pdfcpu, which hands back no
// encoded stream, so Source.Data is documented as nil for it.
func TestPageRasterSourceLeavesOutCMYKDCT(t *testing.T) {
	jpg, err := os.ReadFile("testdata/img2pdf/cmyk.jpg")
	if err != nil {
		t.Fatal(err)
	}
	enc, _, err := EmbedJPEG(jpg)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := BuildPDF(&buf, []BuildPage{{Image: enc, WidthPt: 64, HeightPt: 48}}); err != nil {
		t.Fatal(err)
	}
	for name, pr := range rasters(t, buf.Bytes(), Limits{}) {
		if pr.Source.ColorSpace != "DeviceCMYK" || pr.Source.Data != nil {
			t.Errorf("%s: cs %q, Data %d bytes; want DeviceCMYK and nil", name, pr.Source.ColorSpace, len(pr.Source.Data))
		}
	}
}
