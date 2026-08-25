package pdfbuild

// This is internal/pdfbuild's first test file (byb-jo9). It pins
// validatePage's existing field-by-field checks -- non-positive page box and
// image dimensions, empty data, the FlateDecode BPC allowlist, the zlib
// header check, the filter allowlist, the DCTDecode/JBIG2Decode
// filter/BPC/colour-space combinations, and the /Indexed palette-bounds check
// -- plus the new Width overflow bound maxSampleValue's caller needed. It is
// a pin on what validatePage already rejects, not a spec for what it should.

import (
	"bytes"
	"compress/zlib"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// deflate zlib-compresses raw sample bytes the way an encoder would, so a
// test image's Data decodes under FlateDecode.
func deflate(raw []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	return buf.Bytes()
}

func TestValidatePage(t *testing.T) {
	// A 2x1 /Indexed image, hival 3 (4-entry DeviceRGB palette), samples
	// [0, 3] -- the last legitimately at the palette's edge. Reused as the
	// "otherwise valid" base for the field-specific error cases below.
	validImage := func() pdfdoc.EncodedImage {
		return pdfdoc.EncodedImage{
			Width: 2, Height: 1, BPC: 8,
			ColorSpace: pdfdoc.ColorSpace{Name: "Indexed", HiVal: 3, Base: "DeviceRGB", Lookup: make([]byte, 4*3)},
			Filter:     "FlateDecode",
			Data:       deflate([]byte{0, 3}),
		}
	}

	tests := []struct {
		name    string
		page    Page
		wantErr string // substring; "" means validatePage must return nil
	}{
		{
			name:    "valid indexed image",
			page:    Page{Image: validImage(), WidthPt: 612, HeightPt: 792},
			wantErr: "",
		},
		{
			name:    "page box out of range",
			page:    Page{Image: validImage(), WidthPt: -1, HeightPt: 792},
			wantErr: "is not in the representable range",
		},
		{
			name: "non-positive image dimensions",
			page: func() Page {
				img := validImage()
				img.Width = 0
				return Page{Image: img, WidthPt: 612, HeightPt: 792}
			}(),
			wantErr: "are not positive",
		},
		{
			// The value from byb-jo9's repro: Width*BPC wraps past zero, so
			// rowLen comes out 0 and maxInRow slices a 0-byte row with the
			// un-overflowed Width. Without the bound this case PANICS rather
			// than failing, which is the behaviour the bead exists to stop.
			name: "width wraps rowLen past zero (byb-jo9)",
			page: func() Page {
				img := validImage()
				img.Width = 1 << 61
				return Page{Image: img, WidthPt: 612, HeightPt: 792}
			}(),
			wantErr: "exceeds the maximum",
		},
		{
			// One past the bound, so the case pins the CONSTANT and not just
			// the guard's presence: loosening 1<<31 to anything larger makes
			// this fail, where the wrap-past-zero case above would stay green
			// for any bound below 1<<61.
			name: "width one past the bound (byb-jo9)",
			page: func() Page {
				img := validImage()
				img.Width = 1<<31 + 1
				return Page{Image: img, WidthPt: 612, HeightPt: 792}
			}(),
			wantErr: "exceeds the maximum",
		},
		{
			name: "empty data",
			page: func() Page {
				img := validImage()
				img.Data = nil
				return Page{Image: img, WidthPt: 612, HeightPt: 792}
			}(),
			wantErr: "no data",
		},
		{
			name: "FlateDecode bad BPC",
			page: func() Page {
				img := validImage()
				img.BPC = 3
				return Page{Image: img, WidthPt: 612, HeightPt: 792}
			}(),
			wantErr: "is not one of 1, 2, 4, 8",
		},
		{
			name: "FlateDecode unsupported colour space",
			page: func() Page {
				img := validImage()
				img.ColorSpace = pdfdoc.ColorSpace{Name: "Lab"}
				return Page{Image: img, WidthPt: 612, HeightPt: 792}
			}(),
			wantErr: "is not supported",
		},
		{
			name: "FlateDecode data does not decode under its own filter",
			page: func() Page {
				img := validImage()
				img.Data = []byte("not a zlib stream")
				return Page{Image: img, WidthPt: 612, HeightPt: 792}
			}(),
			wantErr: "does not decode under the filter it declares",
		},
		{
			name: "indexed sample past the palette's hival",
			page: func() Page {
				img := validImage()
				img.Data = deflate([]byte{0, 9}) // 9 > HiVal 3
				return Page{Image: img, WidthPt: 612, HeightPt: 792}
			}(),
			wantErr: "past the palette's hival",
		},
		{
			name: "unsupported filter",
			page: func() Page {
				img := validImage()
				img.Filter = "LZWDecode"
				return Page{Image: img, WidthPt: 612, HeightPt: 792}
			}(),
			wantErr: `filter "LZWDecode" is not supported`,
		},
		{
			name: "DCTDecode bad BPC",
			page: Page{Image: pdfdoc.EncodedImage{
				Width: 2, Height: 1, BPC: 16,
				ColorSpace: pdfdoc.ColorSpace{Name: "DeviceGray"},
				Filter:     "DCTDecode",
				Data:       []byte{0xff, 0xd8},
			}, WidthPt: 612, HeightPt: 792},
			wantErr: "bits per component must be 8",
		},
		{
			name: "DCTDecode unsupported colour space",
			page: Page{Image: pdfdoc.EncodedImage{
				Width: 2, Height: 1, BPC: 8,
				ColorSpace: pdfdoc.ColorSpace{Name: "DeviceCMYK"},
				Filter:     "DCTDecode",
				Data:       []byte{0xff, 0xd8},
			}, WidthPt: 612, HeightPt: 792},
			wantErr: "only DeviceGray/DeviceRGB",
		},
		{
			name: "JBIG2Decode bad BPC",
			page: Page{Image: pdfdoc.EncodedImage{
				Width: 2, Height: 1, BPC: 8,
				ColorSpace: pdfdoc.ColorSpace{Name: "DeviceGray"},
				Filter:     "JBIG2Decode",
				Data:       []byte{0x00},
			}, WidthPt: 612, HeightPt: 792},
			wantErr: "bits per component must be 1",
		},
		{
			name: "JBIG2Decode unsupported colour space",
			page: Page{Image: pdfdoc.EncodedImage{
				Width: 2, Height: 1, BPC: 1,
				ColorSpace: pdfdoc.ColorSpace{Name: "DeviceRGB"},
				Filter:     "JBIG2Decode",
				Data:       []byte{0x00},
			}, WidthPt: 612, HeightPt: 792},
			wantErr: "colour space must be DeviceGray",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePage(tc.page)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validatePage() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validatePage() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validatePage() = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}
