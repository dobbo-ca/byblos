package byblos

// EmbedPNG and EmbedJPEG are the missing route byb-js5.5 names: BuildPDF's
// only image-bytes constructor before this file was QuantizeIndexed, which is
// lossy by construction (median-cut/Lloyd over <=256 colours) and refuses any
// non-opaque pixel. A caller with an already-encoded PNG or JPEG that must
// reach a PDF LOSSLESSLY -- Kleio's img2pdf call site is exactly this -- had
// no exported way to do that.
//
// Both functions lift the source bytes rather than re-encoding them: EmbedPNG
// pulls the IHDR fields and the concatenated IDAT payload straight out of the
// PNG's own chunk framing (that payload already IS a /FlateDecode
// /Predictor 15 scanline stream -- see extractPLTEAndIDAT's doc comment in
// quantize_indexed.go for why), and EmbedJPEG carries the JPEG bytes
// unmodified under /DCTDecode, using image/jpeg.DecodeConfig for the
// dimensions and colour space and a JFIF APP0 scan for the declared density.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/color"
	"image/jpeg"
)

var pngSignature = [8]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// EmbedPNG lifts a PNG file's pixel data into a ready EncodedImage under
// /FlateDecode, plus the DPI its pHYs chunk declares (0 if the file declares
// none -- see BuildPage's doc comment on why 0 is not silently defaulted).
//
// Refused, because none of these can be carried by this route: colour types 4
// and 6 (grayscale+alpha, truecolour+alpha -- alpha needs an /SMask split
// this function does not do), a tRNS chunk on any other colour type (that's
// colour-key or per-palette-entry transparency -- the same SMask gap),
// interlace method other than 0 (Adam7 rows are not PDF scanlines) and 16-bit
// depth (internal/pdfbuild's writer rejects it; see validatePage's doc
// comment).
func EmbedPNG(data []byte) (EncodedImage, float64, error) {
	info, err := parsePNG(data)
	if err != nil {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: %w", err)
	}
	if info.bitDepth == 16 {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: 16-bit-per-component PNG is not supported")
	}
	if info.interlace != 0 {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: interlaced PNG is not supported")
	}
	if info.hasTRNS {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: colour type %d has a tRNS chunk, which needs an /SMask split this function does not do", info.colorType)
	}

	var cs ColorSpace
	var colors int
	switch info.colorType {
	case 0: // grayscale
		if info.bitDepth != 1 && info.bitDepth != 2 && info.bitDepth != 4 && info.bitDepth != 8 {
			return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: colour type 0 does not allow bit depth %d", info.bitDepth)
		}
		cs, colors = ColorSpace{Name: "DeviceGray"}, 1
	case 2: // truecolour
		if info.bitDepth != 8 {
			return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: colour type 2 does not allow bit depth %d", info.bitDepth)
		}
		cs, colors = ColorSpace{Name: "DeviceRGB"}, 3
	case 3: // indexed
		if info.bitDepth != 1 && info.bitDepth != 2 && info.bitDepth != 4 && info.bitDepth != 8 {
			return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: colour type 3 does not allow bit depth %d", info.bitDepth)
		}
		if len(info.plte) == 0 || len(info.plte)%3 != 0 {
			return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: indexed PNG has no usable PLTE chunk")
		}
		cs = ColorSpace{Name: "Indexed", Base: "DeviceRGB", HiVal: len(info.plte)/3 - 1, Lookup: info.plte}
		colors = 1
	case 4, 6:
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: colour type %d carries alpha, which needs an /SMask split this function does not do", info.colorType)
	default:
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedPNG: unsupported PNG colour type %d", info.colorType)
	}

	depth := int(info.bitDepth)
	return EncodedImage{
		Width:      info.width,
		Height:     info.height,
		BPC:        depth,
		ColorSpace: cs,
		Filter:     "FlateDecode",
		DecodeParms: &DecodeParms{
			Predictor:        15,
			Colors:           colors,
			BitsPerComponent: depth,
			Columns:          info.width,
		},
		Data: info.idat,
	}, info.dpi, nil
}

// pngInfo is everything EmbedPNG needs out of one walk over a PNG file's
// chunk framing (ISO/IEC 15948).
type pngInfo struct {
	width, height       int
	bitDepth, colorType byte
	interlace           byte
	plte, idat          []byte
	hasTRNS             bool
	dpi                 float64
}

func parsePNG(data []byte) (pngInfo, error) {
	var info pngInfo
	if len(data) < len(pngSignature) || [8]byte(data[:8]) != pngSignature {
		return info, fmt.Errorf("not a PNG file")
	}
	chunks, err := walkPNGChunks(data)
	if err != nil {
		return info, err
	}
	sawIHDR := false
	for _, c := range chunks {
		switch c.typ {
		case "IHDR":
			if len(c.data) != 13 {
				return info, fmt.Errorf("IHDR is %d bytes; want 13", len(c.data))
			}
			info.width = int(binary.BigEndian.Uint32(c.data[0:4]))
			info.height = int(binary.BigEndian.Uint32(c.data[4:8]))
			info.bitDepth = c.data[8]
			info.colorType = c.data[9]
			info.interlace = c.data[12]
			sawIHDR = true
		case "PLTE":
			info.plte = append([]byte(nil), c.data...)
		case "IDAT":
			info.idat = append(info.idat, c.data...)
		case "tRNS":
			info.hasTRNS = true
		case "pHYs":
			if len(c.data) == 9 && c.data[8] == 1 { // unit specifier 1 = metre
				if ppux := binary.BigEndian.Uint32(c.data[0:4]); ppux > 0 {
					info.dpi = float64(ppux) * 0.0254
				}
			}
		}
	}
	if !sawIHDR {
		return info, fmt.Errorf("no IHDR chunk")
	}
	if info.width <= 0 || info.height <= 0 {
		return info, fmt.Errorf("IHDR declares %dx%d; dimensions must be positive", info.width, info.height)
	}
	if info.idat == nil {
		return info, fmt.Errorf("no IDAT chunk")
	}
	return info, nil
}

// EmbedJPEG lifts a JPEG file's compressed data into a ready EncodedImage
// under /DCTDecode -- carried verbatim, not re-encoded, so the embed costs no
// generation of quality -- plus the DPI its JFIF APP0 segment declares (0 if
// the file declares none or carries only an aspect ratio).
//
// Dimensions, precision and colour components come from image/jpeg's own
// DecodeConfig, which reads exactly the same SOF marker byblos's own JPEG
// reader (extract.go) does, so an EmbedJPEG file is guaranteed decodable:
// DecodeConfig accepts only single-scan baseline/extended-sequential/
// progressive, 8-bit-precision frames, matching internal/pdfbuild's
// DCTDecode allowlist (DeviceGray/DeviceRGB only). A 4-component (CMYK or
// Adobe YCCK) JPEG is refused, because byblos has no reader for CMYK JPEG's
// Adobe-inverted convention and writing one this library's own read side
// cannot round-trip would be worse than refusing it.
func EmbedJPEG(data []byte) (EncodedImage, float64, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedJPEG: %w", err)
	}
	var cs ColorSpace
	switch cfg.ColorModel {
	case color.GrayModel:
		cs = ColorSpace{Name: "DeviceGray"}
	case color.YCbCrModel, color.RGBAModel:
		cs = ColorSpace{Name: "DeviceRGB"}
	default:
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedJPEG: unsupported JPEG colour model (only DeviceGray/DeviceRGB)")
	}
	return EncodedImage{
		Width:      cfg.Width,
		Height:     cfg.Height,
		BPC:        8,
		ColorSpace: cs,
		Filter:     "DCTDecode",
		Data:       data,
	}, jfifDPI(data), nil
}

// jfifDPI returns the DPI data's JFIF APP0 segment declares, or 0 if data
// declares none or carries only an aspect ratio. Per the JFIF spec (ITU-T
// T.871 sec 4), an APP0 JFIF marker segment -- if present -- must be the
// first segment after SOI, so this looks only there rather than walking the
// whole marker chain; jpeg.DecodeConfig has already validated data's overall
// structure by the time this runs.
func jfifDPI(data []byte) float64 {
	if len(data) < 20 || data[0] != 0xFF || data[1] != 0xD8 || data[2] != 0xFF || data[3] != 0xE0 {
		return 0
	}
	length := int(binary.BigEndian.Uint16(data[4:6]))
	if length < 14 || 4+length > len(data) {
		return 0
	}
	seg := data[6 : 4+length]
	if len(seg) < 12 || string(seg[0:5]) != "JFIF\x00" {
		return 0
	}
	units := seg[7]
	density := int(binary.BigEndian.Uint16(seg[8:10]))
	switch {
	case units == 1 && density > 0: // dots per inch
		return float64(density)
	case units == 2 && density > 0: // dots per cm
		return float64(density) * 2.54
	}
	return 0
}
