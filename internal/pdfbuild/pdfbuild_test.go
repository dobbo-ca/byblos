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
	"time"

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
			// byb-37h: Width*Height past maxImagePixels, with neither axis
			// alone anywhere near byb-jo9's old 1<<31 bound -- pins that the
			// new guard fires on the PRODUCT, not a per-axis check.
			name: "pixel budget (byb-37h)",
			page: func() Page {
				img := validImage()
				img.Width, img.Height = 1<<13+1, 1<<13+1 // (2^13+1)^2 > 1<<26
				return Page{Image: img, WidthPt: 612, HeightPt: 792}
			}(),
			wantErr: "exceeds the maximum",
		},
		{
			// Width and Height each at the largest value a real PNG IHDR
			// can declare (uint32 max); their product overflows int64
			// before an unwidened comparison would see it -- bug class 10.
			// If the guard compared plain int64 instead of uint64, this
			// product would wrap to a small positive number and pass.
			name: "pixel budget product overflow guard (byb-37h)",
			page: func() Page {
				img := validImage()
				img.Width, img.Height = 1<<32-1, 1<<32-1
				return Page{Image: img, WidthPt: 612, HeightPt: 792}
			}(),
			wantErr: "exceeds the maximum",
		},
		{
			// byb-37h: Width and Height are declared int, not uint32, and
			// Page is a public struct a caller can build directly -- so an
			// axis can be large enough that uint64(Width)*uint64(Height)
			// itself wraps mod 2^64 and comes out UNDER the budget (here it
			// computes to exactly 0). Bug class 10 one level up: an axis
			// bound must run before the product, not instead of a widened
			// product. Width alone (1<<61) is already far past
			// maxImagePixels, so the per-axis check must catch this even
			// though the product check alone would not.
			name: "pixel budget axis overflows uint64 product (byb-37h)",
			page: func() Page {
				img := validImage()
				img.Width, img.Height = 1<<61, 8
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

// TestValidatePageRefusesOversizedImageCheaply pins the CPU half of byb-37h:
// a 1 x 999,000,000 /Indexed image, backed by ~255 KB of deflated zeros,
// used to make validatePage spend seconds streaming maxSampleValue's row
// loop 999 million times over before it ever found a bad sample -- flat
// memory, all cost in CPU. The maxImagePixels guard must refuse it before
// maxSampleValue is reached at all. This asserts elapsed time, not just the
// error string: the unfixed code also eventually returns an error (a short
// stream, once the 255 KB of zeros run out), just after paying the full
// cost -- a string-only assertion would pass on either version.
//
// IT SURVIVES EVERY SINGLE-GUARD MUTATION, AND THAT IS EXPECTED. Height is
// 999,000,000, so the axis bound and the product bound each refuse this input
// on their own: neutering either one alone leaves this test PASS in 0.39s
// (measured, both directions). Only the COMPOUND mutation -- both bounds gone
// -- reddens it, and then it reddens at the full 3.3s. It earns its place
// anyway because it is the only assertion in the package on the COST of the
// rejection path, which is the whole substance of byb-37h; the two
// TestValidatePage subtests named for the bounds are what pin them
// individually and selectively. Do not "fix" this by narrowing the input to
// something one bound alone catches -- no such input exists for the CPU half,
// because a row count large enough to spend seconds is already past both.
func TestValidatePageRefusesOversizedImageCheaply(t *testing.T) {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	buf := make([]byte, 1<<20)
	for i := 0; i < 256; i++ {
		_, _ = zw.Write(buf)
	}
	_ = zw.Close()

	page := Page{
		Image: pdfdoc.EncodedImage{
			Width: 1, Height: 999_000_000, BPC: 1, Filter: "FlateDecode",
			ColorSpace: pdfdoc.ColorSpace{Name: "Indexed", Base: "DeviceGray", HiVal: 1, Lookup: []byte{0, 255}},
			Data:       z.Bytes(),
		},
		WidthPt: 0.001, HeightPt: 999000,
	}

	start := time.Now()
	err := validatePage(page)
	el := time.Since(start)
	t.Logf("validatePage err = %v, elapsed = %v", err, el)

	if err == nil {
		t.Fatal("validatePage on a 999M-row declared image: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds the maximum") {
		t.Fatalf("validatePage() = %q, want it to contain %q", err.Error(), "exceeds the maximum")
	}
	// The bug measured 3.3s on this exact input; 100ms is two orders of
	// magnitude below that and comfortably above a guard-only refusal.
	const ceiling = 100 * time.Millisecond
	if el > ceiling {
		t.Fatalf("elapsed %v exceeds the %v ceiling", el, ceiling)
	}
}
