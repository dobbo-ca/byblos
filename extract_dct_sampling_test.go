package byblos

import (
	"bytes"
	"context"
	"image/color"
	"testing"

	jpegcopy "github.com/dobbo-ca/jpeg-go"
)

// dcOnlyJPEG writes a baseline 3-component JPEG whose blocks carry a DC
// coefficient only, with Y sampled hY x vY and Cb, Cr 1x1, so the chroma
// sampling is 4:4:0 (1x2), 4:1:1 (4x1) or 4:1:0 (4x2) as the arguments say.
// DC steps differ block to block so upsampling and IDCT differences show.
func dcOnlyJPEG(w, h, hY, vY int) []byte {
	var b bytes.Buffer
	seg := func(marker byte, body ...byte) {
		n := len(body) + 2
		b.Write([]byte{0xFF, marker, byte(n >> 8), byte(n)})
		b.Write(body)
	}
	b.Write([]byte{0xFF, 0xD8})
	dqt := append([]byte{0}, bytes.Repeat([]byte{8}, 64)...)
	seg(0xDB, dqt...)
	seg(0xC0, 8, byte(h>>8), byte(h), byte(w>>8), byte(w), 3,
		1, byte(hY<<4|vY), 0, 2, 0x11, 0, 3, 0x11, 0)
	dcCounts := [16]byte{0, 1, 5, 1, 1, 1, 1, 1, 1}
	dcVals := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	seg(0xC4, append(append([]byte{0x00}, dcCounts[:]...), dcVals...)...)
	acCounts := [16]byte{0, 2} // two 2-bit codes: 00 is EOB, 01 is ZRL
	seg(0xC4, append(append([]byte{0x10}, acCounts[:]...), 0x00, 0xF0)...)
	seg(0xDA, 3, 1, 0, 2, 0, 3, 0, 0, 63, 0)

	type code struct{ bits, n int }
	dc := map[int]code{}
	c, k := 0, 0
	for l := 1; l <= 16; l++ {
		for i := 0; i < int(dcCounts[l-1]); i++ {
			dc[int(dcVals[k])] = code{c, l}
			c++
			k++
		}
		c <<= 1
	}
	var acc, nacc int
	put := func(v, n int) {
		for i := n - 1; i >= 0; i-- {
			acc = acc<<1 | (v>>i)&1
			if nacc++; nacc == 8 {
				b.WriteByte(byte(acc))
				if acc == 0xFF {
					b.WriteByte(0)
				}
				acc, nacc = 0, 0
			}
		}
	}
	var pred [3]int
	block := func(comp, val int) {
		d := val - pred[comp]
		pred[comp] = val
		cat := 0
		for a := max(d, -d); a > 0; a >>= 1 {
			cat++
		}
		put(dc[cat].bits, dc[cat].n)
		if d < 0 {
			d += 1<<cat - 1
		}
		put(d, cat)
		put(0, 2) // EOB
	}
	mw, mh := (w+8*hY-1)/(8*hY), (h+8*vY-1)/(8*vY)
	for my := 0; my < mh; my++ {
		for mx := 0; mx < mw; mx++ {
			for j := 0; j < vY; j++ {
				for i := 0; i < hY; i++ {
					bx, by := mx*hY+i, my*vY+j
					block(0, (bx*37+by*53)%97-48)
				}
			}
			block(1, (mx*29+my*11)%41-20)
			block(2, (mx*7+my*31)%37-18)
		}
	}
	put(0x7F, 7) // pad with ones
	b.Write([]byte{0xFF, 0xD9})
	return b.Bytes()
}

// A DCT page is decoded by the jpeg-go copy, so its pixels are libjpeg-turbo's
// (the decoder poppler and ocrmypdf use), not image/jpeg's: IDCT and chroma
// upsampling differ. 4:4:0, 4:1:1 and 4:1:0 are the samplings the copy added.
func TestExtractDCTPageDecodesWithJpegGo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		hY, vY int
	}{{"440", 1, 2}, {"411", 4, 1}, {"410", 4, 2}, {"420", 2, 2}, {"444", 1, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			data := dcOnlyJPEG(75, 50, tc.hY, tc.vY)
			enc, _, err := EmbedJPEG(data)
			if err != nil {
				t.Fatalf("EmbedJPEG: %v", err)
			}
			var pdf bytes.Buffer
			if err := BuildPDF(&pdf, []BuildPage{{Image: enc, WidthPt: 75, HeightPt: 50}}); err != nil {
				t.Fatalf("BuildPDF: %v", err)
			}
			pr, err := ExtractPageRaster(bytes.NewReader(pdf.Bytes()), 1)
			if err != nil {
				t.Fatalf("ExtractPageRaster: %v", err)
			}
			want, err := jpegcopy.Decode(context.Background(), data, &jpegcopy.DecodeOptions{Format: jpegcopy.FormatRGBA})
			if err != nil {
				t.Fatalf("jpeg-go Decode: %v", err)
			}
			if pr.Image.Bounds() != want.Bounds() {
				t.Fatalf("bounds %v, want %v", pr.Image.Bounds(), want.Bounds())
			}
			bad := 0
			for y := 0; y < 50; y++ {
				for x := 0; x < 75; x++ {
					if color.RGBAModel.Convert(pr.Image.At(x, y)) != want.At(x, y) {
						bad++
					}
				}
			}
			if bad != 0 {
				t.Errorf("%d of %d pixels differ from the jpeg-go decode", bad, 75*50)
			}
		})
	}
}
