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
		t.Errorf("dpi = %v; want 0 (Go's jpeg writer declares an aspect ratio, not a density)", dpi)
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
