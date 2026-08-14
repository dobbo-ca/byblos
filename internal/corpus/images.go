package corpus

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
)

// ImageW, ImageH are the pixel dimensions every generator in this file uses.
// byb-b3's design settled on 600x800: large enough that the median-cut
// histogram and Lloyd refinement see realistic costs, small enough that the
// oracle-backed tests stay fast.
const (
	ImageW = 600
	ImageH = 800
)

// Gradient is a smooth RGB sweep -- the pathological case for a palette
// quantizer. Every pixel is a distinct or near-distinct colour, so there is
// no way to reproduce it losslessly at any small N.
func Gradient() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, ImageW, ImageH))
	for y := 0; y < ImageH; y++ {
		for x := 0; x < ImageW; x++ {
			r := uint8(x * 255 / (ImageW - 1))
			g := uint8(y * 255 / (ImageH - 1))
			b := uint8((x + y) * 255 / (ImageW + ImageH - 2))
			img.SetRGBA(x, y, color.RGBA{r, g, b, 255})
		}
	}
	return img
}

// Photo is a sinusoidal colour field: many distinct colours (451,597 at
// ImageW x ImageH, measured -- most of the image's 480,000 pixels), none of
// them dominant, deterministic.
func Photo() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, ImageW, ImageH))
	for y := 0; y < ImageH; y++ {
		for x := 0; x < ImageW; x++ {
			fx, fy := float64(x), float64(y)
			r := uint8(127 + 127*math.Sin(fx/23.0))
			g := uint8(127 + 127*math.Sin(fy/31.0))
			b := uint8(127 + 127*math.Sin((fx+fy)/17.0))
			img.SetRGBA(x, y, color.RGBA{r, g, b, 255})
		}
	}
	return img
}

// Scanpage is a synthetic scanned-document page: warm paper, a letterhead
// band, dark text strokes and a blue stamp. It is built from a handful of
// flat colours on purpose -- it is the "quantizes exactly" case, and a
// palette quantizer with enough colours must reproduce it losslessly.
func Scanpage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, ImageW, ImageH))
	paper := color.RGBA{250, 246, 236, 255}
	for y := 0; y < ImageH; y++ {
		for x := 0; x < ImageW; x++ {
			img.SetRGBA(x, y, paper)
		}
	}
	band := color.RGBA{225, 220, 205, 255}
	for y := 20; y < 60; y++ {
		for x := 0; x < ImageW; x++ {
			img.SetRGBA(x, y, band)
		}
	}
	ink := color.RGBA{40, 35, 30, 255}
	for line := 0; line < 20; line++ {
		y := 120 + line*30
		if y+1 >= ImageH {
			break
		}
		for x := 60; x < ImageW-60; x += 3 {
			img.SetRGBA(x, y, ink)
			img.SetRGBA(x, y+1, ink)
		}
	}
	stamp := color.RGBA{30, 60, 160, 255}
	cx, cy, radius := ImageW-120, ImageH-120, 50
	for y := cy - radius; y <= cy+radius; y++ {
		for x := cx - radius; x <= cx+radius; x++ {
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= radius*radius {
				img.SetRGBA(x, y, stamp)
			}
		}
	}
	return img
}

// Scanjpeg is Scanpage round-tripped through JPEG at quality 75, 4:2:0 --
// what a scanner's real /DCTDecode output actually looks like, chroma
// subsampling and DCT block artifacts included. This is the realistic B3
// input, not the idealized flat-colour Scanpage.
func Scanjpeg() image.Image {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, Scanpage(), &jpeg.Options{Quality: 75}); err != nil {
		panic(err)
	}
	img, err := jpeg.Decode(&buf)
	if err != nil {
		panic(err)
	}
	return img
}

// ScanJPEG is a two-page PDF whose single shared image XObject is a real
// DCTDecode (JPEG) stream rather than a raw DeviceGray sample array -- the
// shape byb-b3's JPEG recompression pass targets. Both pages reference the
// SAME object (unlike dupRaster, which duplicates the content into two
// objects), so a correct recompression pass touches it once and both pages'
// PageProvenance.Applied must record the substitution.
func ScanJPEG() []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, Scanpage(), &jpeg.Options{Quality: 95}); err != nil {
		panic(err)
	}
	jpegBytes := buf.Bytes()

	w := newWriter()
	cat, pages := w.reserve(), w.reserve()
	p1, c1 := w.reserve(), w.reserve()
	p2, c2 := w.reserve(), w.reserve()
	img := w.reserve()
	body := fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)
	dict := fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode", ImageW, ImageH)

	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R %d 0 R] /Count 2 >>", p1, p2))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, c1))
	w.fillStream(c1, "", []byte(body))
	w.fill(p2, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, c2))
	w.fillStream(c2, "", []byte(body))
	w.fillRawStream(img, dict, jpegBytes)
	return w.finish(cat)
}

// ScanImageMask is a one-page PDF whose only image XObject is an /ImageMask
// stencil: 1 bit per sample, no /ColorSpace, painted in the current fill colour
// (ISO 32000-1 section 8.9.6.2).
//
// It is a standalone fixture rather than a member of All(), for the reason the
// "Known gap" note on jbig2() gives: a stencil is not extractable, so putting it
// in the corpus every extraction test walks would add a document that can only
// ever produce a failure. Nothing here extracts it.
//
// It exists for byb-js5.2. ImageRef.Bitonal is "1 bit per component OR an image
// mask", so a caller picking bilevel candidates for a JBIG2 substitution by
// Bitonal alone selects exactly the images ReplaceImages refuses. This is the
// document that shows the two fields answering different questions.
func ScanImageMask() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	// A stencil is painted in the fill colour, so the content stream sets one;
	// without it the page paints black on black and says nothing.
	body := fmt.Sprintf("q 0 0 0 rg %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)

	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, cont))
	w.fillStream(cont, "", []byte(body))
	// No /ColorSpace: table 89 forbids one on a stencil, and /BitsPerComponent
	// may only be 1.
	w.fillStream(img, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ImageMask true /BitsPerComponent 1", ScanImageW, ScanImageH),
		bilevelPixels(ScanImageW, ScanImageH))
	return w.finish(cat)
}

// ScanSMaskJPEG is a one-page PDF whose image XObject is a DCTDecode stream
// carrying an /SMask -- the case a recompression pass must skip, since
// ReplaceImage refuses /SMask outright (write.go).
func ScanSMaskJPEG() []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, Scanpage(), &jpeg.Options{Quality: 95}); err != nil {
		panic(err)
	}
	jpegBytes := buf.Bytes()

	w := newWriter()
	cat, pages := w.reserve(), w.reserve()
	p1, c1 := w.reserve(), w.reserve()
	img, smask := w.reserve(), w.reserve()
	body := fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)
	dict := fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /DCTDecode /SMask %d 0 R", ImageW, ImageH, smask)

	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, c1))
	w.fillStream(c1, "", []byte(body))
	w.fillRawStream(img, dict, jpegBytes)
	w.fillStream(smask, imageDict(ImageW, ImageH), whitePixels(ImageW, ImageH))
	return w.finish(cat)
}
