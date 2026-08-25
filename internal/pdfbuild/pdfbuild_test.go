package pdfbuild

// This is internal/pdfbuild's first test file (byb-jo9). It pins the new
// Width overflow bound validatePage gained for byb-jo9 against a baseline
// otherwise-valid image, so a later edit that loosens or drops the bound
// fails here. It is not a general characterization suite for validatePage's
// other, pre-existing checks.

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
			// 1<<31 + 1 (not 1<<61) so this pins the actual bound
			// validatePage enforces: loosening the constant to anything
			// under this value makes the case fail, where a far-past-the-
			// boundary width would not have caught the drift.
			name: "width overflows rowLen's Width*BPC product (byb-jo9)",
			page: func() Page {
				img := validImage()
				img.Width = 1<<31 + 1
				return Page{Image: img, WidthPt: 612, HeightPt: 792}
			}(),
			wantErr: "exceeds the maximum",
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
