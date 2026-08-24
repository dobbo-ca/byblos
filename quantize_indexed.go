package byblos

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
)

// QuantizeIndexed reduces img to at most colors distinct colours by the same
// median-cut/Lloyd/population-order core QuantizePNG uses (quantizeCore,
// quantize.go), and packages the result as a PDF /Indexed, /FlateDecode
// image: EncodedImage.Data is the raw PNG-predicted, deflate-compressed
// scanline stream (not a PNG file -- see EncodedImage.Data's doc comment in
// internal/pdfdoc/write.go for why a PNG file is the wrong shape here), ready
// for pdfdoc.ReplaceImage.
//
// Container decision (byb-96p, lead 5): QuantizeIndexed shares quantizeCore
// with QuantizePNG to get the identical *image.Paletted -- crucially
// including byb-20b's population-order permutation, which must not drift
// between the two entry points -- then runs that SAME image through the
// standard library's PNG encoder and pulls the /Indexed colour table (PLTE)
// and the concatenation of every IDAT chunk's payload straight out of the
// resulting chunk framing. Per the PNG spec the IDAT payloads concatenate
// into one continuous zlib stream regardless of how the encoder split them
// across chunks, and that stream IS the predictor-prefixed scanline data a
// PDF /FlateDecode filter with /DecodeParms /Predictor 15 expects -- so
// nothing needs decoding or re-encoding, only chunk parsing. This is
// deliberately option (a) from the bead (parse chunks out of a PNG), applied
// to quantizeCore's private *image.Paletted rather than to QuantizePNG's
// public []byte, so QuantizePNG's own bytes (and its calibrated oracle test)
// are untouched by this file.
func QuantizeIndexed(img image.Image, colors int) (EncodedImage, error) {
	out, err := quantizeCore(img, colors)
	if err != nil {
		return EncodedImage{}, err
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, out); err != nil {
		return EncodedImage{}, fmt.Errorf("byblos: quantizeindexed: encode: %w", err)
	}
	plte, idat, err := extractPLTEAndIDAT(buf.Bytes())
	if err != nil {
		return EncodedImage{}, fmt.Errorf("byblos: quantizeindexed: %w", err)
	}

	b := out.Bounds()
	depth := paletteBitDepth(len(out.Palette))
	return EncodedImage{
		Width:  b.Dx(),
		Height: b.Dy(),
		BPC:    depth,
		ColorSpace: ColorSpace{
			Name:   "Indexed",
			Base:   "DeviceRGB",
			HiVal:  len(out.Palette) - 1,
			Lookup: plte,
		},
		Filter: "FlateDecode",
		DecodeParms: &DecodeParms{
			Predictor:        15,
			Colors:           1,
			BitsPerComponent: depth,
			Columns:          b.Dx(),
		},
		Data: idat,
	}, nil
}

// paletteBitDepth mirrors image/png's own palette bit-depth selection
// (image/png/writer.go, Encoder.Encode): 1/2/4/8 bits per sample for palette
// lengths <=2/<=4/<=16/else. QuantizeIndexed's /DecodeParms must agree with
// whichever depth the PNG encoder actually chose when producing the scanline
// bytes this function reads back out.
func paletteBitDepth(n int) int {
	switch {
	case n <= 2:
		return 1
	case n <= 4:
		return 2
	case n <= 16:
		return 4
	default:
		return 8
	}
}

// pngChunk is one chunk out of walkPNGChunks.
type pngChunk struct {
	typ  string
	data []byte
}

// walkPNGChunks walks pngData's chunk framing (ISO/IEC 15948), skipping the
// 8-byte signature, and returns every chunk up to and including IEND (chunks
// after IEND, if any, are not part of the image and are ignored). Shared by
// extractPLTEAndIDAT (below) and parsePNG (embed.go) so the framing walk --
// and its bounds checking -- exists exactly once.
func walkPNGChunks(pngData []byte) ([]pngChunk, error) {
	const sigLen = 8
	if len(pngData) < sigLen {
		return nil, fmt.Errorf("not a PNG file")
	}
	p := pngData[sigLen:]
	var chunks []pngChunk
	for len(p) >= 8 {
		length := uint64(binary.BigEndian.Uint32(p[0:4]))
		typ := string(p[4:8])
		if length+12 > uint64(len(p)) {
			return nil, fmt.Errorf("truncated %s chunk", typ)
		}
		n := int(length) // safe: length+12 <= len(p), so length fits in an int
		chunks = append(chunks, pngChunk{typ, p[8 : 8+n]})
		p = p[8+n+4:]
		if typ == "IEND" {
			break
		}
	}
	return chunks, nil
}

// extractPLTEAndIDAT returns the PLTE chunk's payload and the concatenation
// of every IDAT chunk's payload out of pngData. Concatenating every IDAT --
// not just the first -- is required: image/png splits its zlib stream across
// multiple IDAT chunks once it exceeds an internal buffer, and the chunks are
// one continuous stream, not independent ones (see
// TestQuantizeIndexedMultipleIDATNotTruncated).
func extractPLTEAndIDAT(pngData []byte) (plte, idat []byte, err error) {
	chunks, err := walkPNGChunks(pngData)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range chunks {
		switch c.typ {
		case "PLTE":
			plte = append([]byte(nil), c.data...)
		case "IDAT":
			idat = append(idat, c.data...)
		}
	}
	if plte == nil {
		return nil, nil, fmt.Errorf("no PLTE chunk in encoded PNG")
	}
	if idat == nil {
		return nil, nil, fmt.Errorf("no IDAT chunk in encoded PNG")
	}
	return plte, idat, nil
}
