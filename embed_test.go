package byblos

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// insertPHYs splices a pHYs chunk (ppux, ppuy pixels per metre) right after
// the IHDR chunk of an encoded PNG, so EmbedPNG's DPI path can be tested
// against a file that actually declares one -- stdlib's png.Encoder never
// writes pHYs itself.
func insertPHYs(t *testing.T, pngData []byte, ppux, ppuy uint32) []byte {
	t.Helper()
	const sigLen = 8
	if len(pngData) < sigLen+8 {
		t.Fatal("PNG too short")
	}
	ihdrLen := binary.BigEndian.Uint32(pngData[sigLen:])
	ihdrEnd := sigLen + 8 + int(ihdrLen) + 4 // length+type+data+crc
	payload := make([]byte, 9)
	binary.BigEndian.PutUint32(payload[0:4], ppux)
	binary.BigEndian.PutUint32(payload[4:8], ppuy)
	payload[8] = 1 // unit specifier: metre
	var chunk bytes.Buffer
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(payload)))
	chunk.Write(lenBuf[:])
	chunk.WriteString("pHYs")
	chunk.Write(payload)
	crc := crc32.NewIEEE()
	crc.Write([]byte("pHYs"))
	crc.Write(payload)
	var crcBuf [4]byte
	binary.BigEndian.PutUint32(crcBuf[:], crc.Sum32())
	chunk.Write(crcBuf[:])

	out := append([]byte(nil), pngData[:ihdrEnd]...)
	out = append(out, chunk.Bytes()...)
	out = append(out, pngData[ihdrEnd:]...)
	return out
}

func TestEmbedPNGGrayscaleRoundTripsThroughBuildPDF(t *testing.T) {
	w, h := 17, 11
	src := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src.SetGray(x, y, color.Gray{Y: byte((x*7 + y*13) % 256)})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}

	enc, dpi, err := EmbedPNG(buf.Bytes())
	if err != nil {
		t.Fatalf("EmbedPNG: %v", err)
	}
	if dpi != 0 {
		t.Errorf("dpi = %v; want 0 (source PNG declares no pHYs chunk)", dpi)
	}
	if enc.Width != w || enc.Height != h || enc.BPC != 8 || enc.ColorSpace.Name != "DeviceGray" || enc.Filter != "FlateDecode" {
		t.Fatalf("EncodedImage = %+v; want an 8bpc DeviceGray FlateDecode image", enc)
	}

	var pdf bytes.Buffer
	if err := BuildPDF(&pdf, []BuildPage{{Image: enc, WidthPt: 72, HeightPt: 72}}); err != nil {
		t.Fatalf("BuildPDF: %v", err)
	}
	pr, err := ExtractPageRaster(bytes.NewReader(pdf.Bytes()), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
	b := pr.Image.Bounds()
	if b.Dx() != w || b.Dy() != h {
		t.Fatalf("extracted raster is %dx%d; want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			want := src.GrayAt(x, y).Y
			got := color.GrayModel.Convert(pr.Image.At(b.Min.X+x, b.Min.Y+y)).(color.Gray).Y
			if got != want {
				t.Fatalf("pixel (%d,%d) = %d; want %d -- EmbedPNG's embedded stream is not byte-identical to the source payload", x, y, got, want)
			}
		}
	}
}

func TestEmbedPNGTruecolourRoundTripsThroughBuildPDF(t *testing.T) {
	w, h := 13, 9
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src.SetRGBA(x, y, color.RGBA{R: byte(x * 17), G: byte(y * 23), B: byte(x + y), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}

	enc, _, err := EmbedPNG(buf.Bytes())
	if err != nil {
		t.Fatalf("EmbedPNG: %v", err)
	}
	if enc.ColorSpace.Name != "DeviceRGB" {
		t.Fatalf("an opaque RGBA source PNG encoded ColorSpace %q; want DeviceRGB", enc.ColorSpace.Name)
	}

	var pdf bytes.Buffer
	if err := BuildPDF(&pdf, []BuildPage{{Image: enc, WidthPt: 72, HeightPt: 72}}); err != nil {
		t.Fatalf("BuildPDF: %v", err)
	}
	pr, err := ExtractPageRaster(bytes.NewReader(pdf.Bytes()), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
	b := pr.Image.Bounds()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			want := src.RGBAAt(x, y)
			got := color.RGBAModel.Convert(pr.Image.At(b.Min.X+x, b.Min.Y+y)).(color.RGBA)
			if got.R != want.R || got.G != want.G || got.B != want.B {
				t.Fatalf("pixel (%d,%d) = %+v; want %+v", x, y, got, want)
			}
		}
	}
}

func TestEmbedPNGDeclaresDPIFromPHYs(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	// 300 DPI = 300 / 0.0254 pixels per metre, rounded.
	withPHYs := insertPHYs(t, buf.Bytes(), 11811, 11811)

	_, dpi, err := EmbedPNG(withPHYs)
	if err != nil {
		t.Fatalf("EmbedPNG: %v", err)
	}
	if dpi < 299 || dpi > 301 {
		t.Errorf("dpi = %v; want ~300", dpi)
	}
}

func TestEmbedPNGRefusesAlpha(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			src.SetNRGBA(x, y, color.NRGBA{R: 10, G: 20, B: 30, A: byte(x * 60)})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	if _, _, err := EmbedPNG(buf.Bytes()); err == nil {
		t.Fatal("EmbedPNG on an alpha-bearing PNG: want an error, got nil")
	}
}

func TestEmbedPNGRefusesSixteenBit(t *testing.T) {
	src := image.NewGray16(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	if _, _, err := EmbedPNG(buf.Bytes()); err == nil {
		t.Fatal("EmbedPNG on a 16-bit PNG: want an error, got nil")
	}
}

func TestEmbedPNGRefusesInterlaced(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	data := append([]byte(nil), buf.Bytes()...)
	// IHDR's interlace method byte: signature(8) + length(4) + type(4) +
	// width(4) + height(4) + bitdepth(1) + colortype(1) + compression(1) +
	// filter(1) = offset 28.
	const interlaceOffset = 8 + 8 + 12
	data[interlaceOffset] = 1
	if _, _, err := EmbedPNG(data); err == nil {
		t.Fatal("EmbedPNG on an interlaced PNG: want an error, got nil")
	}
}

func TestEmbedJPEGCarriesTheSourceBytesVerbatim(t *testing.T) {
	w, h := 21, 15
	src := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src.SetGray(x, y, color.Gray{Y: byte((x*11 + y*3) % 256)})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}

	enc, dpi, err := EmbedJPEG(buf.Bytes())
	if err != nil {
		t.Fatalf("EmbedJPEG: %v", err)
	}
	if dpi != 0 {
		t.Errorf("dpi = %v; want 0 (Go's jpeg writer emits no JFIF APP0 segment at all)", dpi)
	}
	if enc.Width != w || enc.Height != h || enc.BPC != 8 || enc.ColorSpace.Name != "DeviceGray" || enc.Filter != "DCTDecode" {
		t.Fatalf("EncodedImage = %+v; want an 8bpc DeviceGray DCTDecode image", enc)
	}
	if !bytes.Equal(enc.Data, buf.Bytes()) {
		t.Fatal("EmbedJPEG's Data is not byte-identical to the source JPEG payload")
	}

	var pdf bytes.Buffer
	if err := BuildPDF(&pdf, []BuildPage{{Image: enc, WidthPt: 72, HeightPt: 72}}); err != nil {
		t.Fatalf("BuildPDF: %v", err)
	}
	if _, err := ExtractPageRaster(bytes.NewReader(pdf.Bytes()), 1); err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
}

// TestJFIFDPIReadsTheDeclaredDensity builds a JFIF APP0 segment by hand,
// since Go's own jpeg.Encode never emits one (see the comment above), and
// checks jfifDPI reads both unit specifiers.
func TestJFIFDPIReadsTheDeclaredDensity(t *testing.T) {
	jfif := func(units byte, density uint16) []byte {
		seg := make([]byte, 14) // JFIF\0(5) + version(2) + units(1) + x/y density(4) + thumbnail w/h(2)
		copy(seg, "JFIF\x00")
		seg[5], seg[6] = 1, 2 // version 1.02
		seg[7] = units
		binary.BigEndian.PutUint16(seg[8:10], density)
		binary.BigEndian.PutUint16(seg[10:12], density)
		var buf bytes.Buffer
		buf.Write([]byte{0xFF, 0xD8, 0xFF, 0xE0})
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(seg)+2))
		buf.Write(lenBuf[:])
		buf.Write(seg)
		return buf.Bytes()
	}
	if dpi := jfifDPI(jfif(1, 300)); dpi != 300 {
		t.Errorf("units=1 (dpi) density=300: jfifDPI = %v; want 300", dpi)
	}
	if dpi := jfifDPI(jfif(2, 100)); dpi < 253 || dpi > 255 {
		t.Errorf("units=2 (dots/cm) density=100: jfifDPI = %v; want ~254", dpi)
	}
	if dpi := jfifDPI(jfif(0, 4)); dpi != 0 {
		t.Errorf("units=0 (aspect ratio only): jfifDPI = %v; want 0", dpi)
	}
}

// TestEmbedJPEGRefusesLossless hand-builds the smallest possible SOF3
// (lossless) frame header: EmbedJPEG must not accept it, since byblos's own
// JPEG reader (extract.go) cannot decode lossless JPEG back out.
func TestEmbedJPEGRefusesLossless(t *testing.T) {
	data := []byte{
		0xFF, 0xD8, // SOI
		0xFF, 0xC3, 0x00, 0x0B, 0x08, 0x00, 0x04, 0x00, 0x04, 0x01, 0x01, 0x11, 0x00, // SOF3, 4x4, 1 component
		0xFF, 0xDA, 0x00, 0x02, // SOS (empty)
	}
	if _, _, err := EmbedJPEG(data); err == nil {
		t.Fatal("EmbedJPEG on a lossless (SOF3) JPEG: want an error, got nil")
	}
}

func TestEmbedPNGIndexedRoundTripsThroughBuildPDF(t *testing.T) {
	w, h := 6, 5
	pal := color.Palette{
		color.RGBA{R: 10, G: 20, B: 30, A: 255},
		color.RGBA{R: 200, G: 100, B: 50, A: 255},
		color.RGBA{R: 0, G: 0, B: 0, A: 255},
	}
	src := image.NewPaletted(image.Rect(0, 0, w, h), pal)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src.SetColorIndex(x, y, uint8((x+y)%len(pal)))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}

	enc, _, err := EmbedPNG(buf.Bytes())
	if err != nil {
		t.Fatalf("EmbedPNG: %v", err)
	}
	if enc.ColorSpace.Name != "Indexed" || enc.ColorSpace.HiVal != len(pal)-1 {
		t.Fatalf("EncodedImage.ColorSpace = %+v; want Indexed with HiVal %d", enc.ColorSpace, len(pal)-1)
	}

	var pdf bytes.Buffer
	if err := BuildPDF(&pdf, []BuildPage{{Image: enc, WidthPt: 72, HeightPt: 72}}); err != nil {
		t.Fatalf("BuildPDF: %v", err)
	}
	pr, err := ExtractPageRaster(bytes.NewReader(pdf.Bytes()), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
	b := pr.Image.Bounds()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			want := pal[(x+y)%len(pal)].(color.RGBA)
			got := color.RGBAModel.Convert(pr.Image.At(b.Min.X+x, b.Min.Y+y)).(color.RGBA)
			if got.R != want.R || got.G != want.G || got.B != want.B {
				t.Fatalf("pixel (%d,%d) = %+v; want %+v", x, y, got, want)
			}
		}
	}
}

func TestEmbedPNGRefusesTRNS(t *testing.T) {
	pal := color.Palette{
		color.NRGBA{R: 0, G: 0, B: 0, A: 0}, // fully transparent entry
		color.NRGBA{R: 255, G: 255, B: 255, A: 255},
	}
	src := image.NewPaletted(image.Rect(0, 0, 4, 4), pal)
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	if _, _, err := EmbedPNG(buf.Bytes()); err == nil {
		t.Fatal("EmbedPNG on a PNG with a tRNS chunk: want an error, got nil")
	}
}

func TestEmbedPNGRefusesBadBitDepthColourTypeCombo(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	data := append([]byte(nil), buf.Bytes()...)
	// IHDR colour type byte (offset 8+8+9=25): rewrite as truecolour (2),
	// leaving bit depth at grayscale's 8 -- that combo is legal, so also drop
	// the bit depth (offset 24) to 1, which colour type 2 never allows.
	const bitDepthOffset, colorTypeOffset = 8 + 8 + 8, 8 + 8 + 9
	data[bitDepthOffset] = 1
	data[colorTypeOffset] = 2
	if _, _, err := EmbedPNG(data); err == nil {
		t.Fatal("EmbedPNG on colour type 2 / bit depth 1: want an error, got nil")
	}
}

func TestEmbedJPEGColourRoundTrip(t *testing.T) {
	w, h := 19, 12
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src.SetRGBA(x, y, color.RGBA{R: byte(x * 13), G: byte(y * 21), B: byte(x + y), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	enc, _, err := EmbedJPEG(buf.Bytes())
	if err != nil {
		t.Fatalf("EmbedJPEG: %v", err)
	}
	if enc.ColorSpace.Name != "DeviceRGB" {
		t.Fatalf("ColorSpace = %q; want DeviceRGB", enc.ColorSpace.Name)
	}
}
