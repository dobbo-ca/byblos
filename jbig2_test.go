package byblos

import (
	"bytes"
	"slices"
	"testing"
)

// borderBitmap is T.88 Figure H.6 built through the public Bitmap type: a 54x44
// rectangle with a 2-pixel ink border.
func borderBitmap() *Bitmap {
	const w, h = 54, 44
	stride := (w + 7) / 8
	b := &Bitmap{Width: w, Height: h, Stride: stride, Pix: make([]byte, stride*h)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if y < 2 || y >= h-2 || x < 2 || x >= w-2 {
				b.Pix[y*stride+x/8] |= 0x80 >> (uint(x) % 8)
			}
		}
	}
	return b
}

// The public API must reach the T.88 Annex H.1 region data through the adapter.
// This is what catches a bit-order or ink-convention mismatch between the root
// Bitmap and internal/jbig2: any such mismatch changes these bytes.
func TestEncodeJBIG2GenericReachesSpecVector(t *testing.T) {
	got, err := EncodeJBIG2Generic(borderBitmap())
	if err != nil {
		t.Fatalf("EncodeJBIG2Generic() error = %v", err)
	}
	want := []byte{0x04, 0xEE, 0xED, 0x87, 0xFB, 0xCB, 0x2B, 0xFF, 0xAC}
	if !bytes.HasSuffix(got, want) {
		t.Fatalf("stream does not end with the T.88 Annex H.1 region data\ngot tail: % 02X\nwant:     % 02X",
			got[max(0, len(got)-len(want)):], want)
	}
}

func TestEncodeJBIG2GenericRejectsEmptyBitmap(t *testing.T) {
	if _, err := EncodeJBIG2Generic(&Bitmap{Width: 0, Height: 0}); err == nil {
		t.Fatal("EncodeJBIG2Generic() on a 0x0 bitmap: want error, got nil")
	}
}

func TestEncodeJBIG2GenericRejectsShortPixSlice(t *testing.T) {
	b := &Bitmap{Width: 16, Height: 4, Stride: 2, Pix: make([]byte, 3)}
	if _, err := EncodeJBIG2Generic(b); err == nil {
		t.Fatal("EncodeJBIG2Generic() with a truncated Pix: want error, got nil")
	}
}

// The capability string is what UpgradeCandidates keys on (design spec section
// 6), so it is API surface and must not drift.
func TestCapabilityStringIsStable(t *testing.T) {
	if CapabilityJBIG2Generic != "jbig2-generic" {
		t.Errorf("CapabilityJBIG2Generic = %q; want %q", CapabilityJBIG2Generic, "jbig2-generic")
	}
	if slices.Contains(Capabilities(), "jbig2-symbol") {
		t.Error("Capabilities() advertises jbig2-symbol, which this build does not implement")
	}
	if !slices.Contains(Capabilities(), CapabilityJBIG2Generic) {
		t.Errorf("Capabilities() = %v; want it to contain %q", Capabilities(), CapabilityJBIG2Generic)
	}
}
