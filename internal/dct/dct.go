// Package dct reads a JPEG's frame header, without allocating, so a decode can
// be sized and refused before any decoder makes a buffer from it.
package dct

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/dobbo-ca/byblos/internal/limits"
)

// Frame is the first SOF marker's shape.
type Frame struct {
	W, H, N int
	Samp    [4][2]int // each component's horizontal and vertical sampling
	SOF     byte      // the SOF marker's low byte: 0xC0 baseline, 0xC9 arithmetic, ...
	Prec    int       // sample precision in bits
	Coefs   bool      // progressive, or more than one scan: coefficients outlive a scan
}

// Parse reads the first SOF's frame. Bytes between segments are skipped to the
// next marker, as image/jpeg and libjpeg do. ok is false when no complete SOF
// precedes the first scan.
func Parse(data []byte) (f Frame, ok bool) {
	for i := 2; i+4 <= len(data); {
		m := data[i+1]
		switch {
		case data[i] != 0xFF:
			i++
		case m == 0xFF:
			i++
		case m == 0 || m == 0x01 || m >= 0xD0 && m <= 0xD8:
			i += 2
		case m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC:
			if i+10 > len(data) {
				return Frame{}, false
			}
			f.SOF, f.Prec = m, int(data[i+4])
			f.H, f.W, f.N = int(data[i+5])<<8|int(data[i+6]), int(data[i+7])<<8|int(data[i+8]), int(data[i+9])
			if f.N < 1 || f.N > 4 || i+10+3*f.N > len(data) {
				return Frame{}, false
			}
			for k := 0; k < f.N; k++ {
				hv := data[i+11+3*k]
				f.Samp[k] = [2]int{int(hv >> 4), int(hv & 15)}
				if f.Samp[k][0] < 1 || f.Samp[k][0] > 4 || f.Samp[k][1] < 1 || f.Samp[k][1] > 4 {
					return Frame{}, false
				}
			}
			// Any SOS after the first is a second scan; a stray FFDA in a
			// segment only makes this true, which over-reserves.
			f.Coefs = m&3 == 2 || countSOS(data[i:]) > 1
			return f, true
		case m == 0xDA:
			return Frame{}, false
		default:
			i += 2 + (int(data[i+2])<<8 | int(data[i+3]))
		}
	}
	return Frame{}, false
}

// Variant names why no byblos decoder reads f: "lossless", "arith" (arithmetic
// coding) or "12bit"; empty for a frame the decoders do read.
func (f Frame) Variant() string {
	switch {
	case f.SOF == 0xC3 || f.SOF == 0xC7 || f.SOF == 0xCB || f.SOF == 0xCF:
		return "lossless"
	case f.SOF >= 0xC9:
		return "arith"
	case f.Prec == 12:
		return "12bit"
	}
	return ""
}

func countSOS(b []byte) (n int) {
	for i := 0; i+1 < len(b); i++ {
		if b[i] == 0xFF && b[i+1] == 0xDA {
			n++
			i++
		}
	}
	return n
}

// Need is the most bytes a decode of f holds, by either decoder byblos uses
// (jpeg-go, or image/jpeg for what jpeg-go does not read): the MCU-padded
// planes, int32 coefficients when they outlive a scan, the RGBA (or CMYK)
// image and package Decode's copy of it, and fixed state, each buffer rounded
// up a page. It is an upper bound, not jpeg-go's own estimate, which that
// module does not export.
func (f Frame) Need() int64 {
	const page = 8192
	maxH, maxV := 1, 1
	for k := 0; k < f.N; k++ {
		maxH, maxV = max(maxH, f.Samp[k][0]), max(maxV, f.Samp[k][1])
	}
	mxx, myy := int64(f.W+8*maxH-1)/int64(8*maxH), int64(f.H+8*maxV-1)/int64(8*maxV)
	need := int64(1<<16) + 2*int64(f.W) + 4*page // package Decode's copy rounds up to 3 pages
	for k := 0; k < f.N; k++ {
		need += mxx*myy*int64(maxH*maxV)*64 + 4*page // a plane, as large as the MCU grid allows
		if f.Coefs {
			need += mxx * myy * int64(f.Samp[k][0]*f.Samp[k][1]) * 256
		}
	}
	out := 4 * int64(f.W) * int64(f.H)
	need += 2 * out
	if f.N == 4 {
		need += out
	}
	return need
}

// Reserve is the gate every DCT decode passes. It reads data's SOF, refuses a
// frame past maxPixels (a *limits.LimitError) or that the image dictionary does
// not allow, and reserves Need() plus extra from byblos's memory budget, the one
// pool every byblos operation shares; the caller releases it after the decode.
// The dictionary allows a frame of at most dictPixels pixels (negative: no
// dictionary) and dictN components (0: unknown). A smaller frame is allowed and
// decodes at its own size, as viewers do. A budget that stays held until ctx ends
// wraps limits.ErrMemoryLimit. Data that is not a JPEG passes with nothing
// reserved.
func Reserve(ctx context.Context, data []byte, maxPixels, dictPixels int64, dictN int, extra int64) (Frame, func(), error) {
	if !bytes.HasPrefix(data, []byte{0xFF, 0xD8}) {
		return Frame{}, func() {}, nil
	}
	f, ok := Parse(data)
	if !ok {
		return f, nil, errors.New("dct: no frame header before the first scan")
	}
	px := limits.MulSat(int64(f.W), int64(f.H))
	if err := limits.Check("image-pixels", px, maxPixels); err != nil {
		return f, nil, err
	}
	if dictPixels >= 0 && px > dictPixels || dictN != 0 && f.N != dictN {
		return f, nil, fmt.Errorf("dct: frame is %dx%d with %d components; the image dictionary allows %d pixels and %d components",
			f.W, f.H, f.N, dictPixels, dictN)
	}
	release, err := limits.Reserve(ctx, f.Need()+extra)
	return f, release, err
}
