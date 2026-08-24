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
// unmodified under /DCTDecode, reading only the SOF and JFIF APP0 markers to
// learn the dimensions, colour space and declared density.

import (
	"encoding/binary"
	"fmt"
)

var pngSignature = [8]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// EmbedPNG lifts a PNG file's pixel data into a ready EncodedImage under
// /FlateDecode, plus the DPI its pHYs chunk declares (0 if the file declares
// none -- see BuildPage's doc comment on why 0 is not silently defaulted).
//
// Refused, because none of these can be carried by this route: colour types 4
// and 6 (grayscale+alpha, truecolour+alpha -- alpha needs an /SMask split
// this function does not do), interlace method other than 0 (Adam7 rows are
// not PDF scanlines) and 16-bit depth (internal/pdfbuild's writer rejects it;
// see validatePage's doc comment).
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

	var cs ColorSpace
	var colors int
	switch info.colorType {
	case 0: // grayscale
		cs, colors = ColorSpace{Name: "DeviceGray"}, 1
	case 2: // truecolour
		cs, colors = ColorSpace{Name: "DeviceRGB"}, 3
	case 3: // indexed
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
	dpi                 float64
}

func parsePNG(data []byte) (pngInfo, error) {
	var info pngInfo
	if len(data) < len(pngSignature) || [8]byte(data[:8]) != pngSignature {
		return info, fmt.Errorf("not a PNG file")
	}
	p := data[len(pngSignature):]
	sawIHDR := false
	for len(p) >= 8 {
		length := binary.BigEndian.Uint32(p[0:4])
		typ := string(p[4:8])
		if uint64(len(p)) < 8+uint64(length)+4 {
			return info, fmt.Errorf("truncated %s chunk", typ)
		}
		chunk := p[8 : 8+length]
		switch typ {
		case "IHDR":
			if length != 13 {
				return info, fmt.Errorf("IHDR is %d bytes; want 13", length)
			}
			info.width = int(binary.BigEndian.Uint32(chunk[0:4]))
			info.height = int(binary.BigEndian.Uint32(chunk[4:8]))
			info.bitDepth = chunk[8]
			info.colorType = chunk[9]
			info.interlace = chunk[12]
			sawIHDR = true
		case "PLTE":
			info.plte = append([]byte(nil), chunk...)
		case "IDAT":
			info.idat = append(info.idat, chunk...)
		case "pHYs":
			if length == 9 && chunk[8] == 1 { // unit specifier 1 = metre
				if ppux := binary.BigEndian.Uint32(chunk[0:4]); ppux > 0 {
					info.dpi = float64(ppux) * 0.0254
				}
			}
		}
		p = p[8+length+4:]
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
// Only single-scan baseline/progressive JPEGs with 1 (grayscale) or 3
// (YCbCr/RGB) components are supported, matching internal/pdfbuild's
// DCTDecode allowlist (DeviceGray/DeviceRGB only): a 4-component (CMYK or
// Adobe YCCK) JPEG is refused, because byblos has no reader for CMYK JPEG's
// Adobe-inverted convention and writing one this library's own read side
// cannot round-trip would be worse than refusing it.
func EmbedJPEG(data []byte) (EncodedImage, float64, error) {
	info, err := parseJPEG(data)
	if err != nil {
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedJPEG: %w", err)
	}
	var cs ColorSpace
	switch info.numComponents {
	case 1:
		cs = ColorSpace{Name: "DeviceGray"}
	case 3:
		cs = ColorSpace{Name: "DeviceRGB"}
	default:
		return EncodedImage{}, 0, fmt.Errorf("byblos: EmbedJPEG: %d colour components is not supported (only DeviceGray/DeviceRGB)", info.numComponents)
	}
	return EncodedImage{
		Width:      info.width,
		Height:     info.height,
		BPC:        8,
		ColorSpace: cs,
		Filter:     "DCTDecode",
		Data:       data,
	}, info.dpi, nil
}

type jpegInfo struct {
	width, height int
	numComponents int
	dpi           float64
}

// parseJPEG walks data's marker segments far enough to learn the frame's
// dimensions and component count (the SOFn marker) and its declared density
// (the JFIF APP0 marker, if present), without decoding any pixel. It stops at
// the first start-of-scan marker, since nothing needed lives past it.
func parseJPEG(data []byte) (jpegInfo, error) {
	var info jpegInfo
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return info, fmt.Errorf("not a JPEG file (no SOI marker)")
	}
	i := 2
	sawSOF := false
	for i+1 < len(data) {
		if data[i] != 0xFF {
			return info, fmt.Errorf("malformed marker at byte %d", i)
		}
		marker := data[i+1]
		switch {
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7): // markers with no payload
			i += 2
			continue
		case marker == 0xD9: // EOI
			i += 2
			continue
		}
		if i+4 > len(data) {
			return info, fmt.Errorf("truncated marker segment at byte %d", i)
		}
		length := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if length < 2 || i+2+length > len(data) {
			return info, fmt.Errorf("marker segment length %d at byte %d is out of range", length, i)
		}
		seg := data[i+4 : i+2+length]

		switch {
		case marker == 0xE0 && len(seg) >= 12 && string(seg[0:5]) == "JFIF\x00":
			units := seg[7]
			density := int(binary.BigEndian.Uint16(seg[8:10]))
			switch {
			case units == 1 && density > 0: // dots per inch
				info.dpi = float64(density)
			case units == 2 && density > 0: // dots per cm
				info.dpi = float64(density) * 2.54
			}
		case marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC: // SOFn
			if len(seg) < 6 {
				return info, fmt.Errorf("SOF marker segment is %d bytes; want at least 6", len(seg))
			}
			info.height = int(binary.BigEndian.Uint16(seg[1:3]))
			info.width = int(binary.BigEndian.Uint16(seg[3:5]))
			info.numComponents = int(seg[5])
			sawSOF = true
		case marker == 0xDA: // start of scan: nothing further is a header
			i = len(data)
			continue
		}
		if sawSOF {
			break
		}
		i += 2 + length
	}
	if !sawSOF {
		return info, fmt.Errorf("no SOF marker found")
	}
	if info.width <= 0 || info.height <= 0 {
		return info, fmt.Errorf("SOF declares %dx%d; dimensions must be positive", info.width, info.height)
	}
	return info, nil
}
