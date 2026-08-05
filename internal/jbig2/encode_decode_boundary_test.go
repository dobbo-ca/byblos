package jbig2

import (
	"bytes"
	"testing"
)

// TestEncodeDecodeSizeBoundary states, as an executable fact, exactly which
// page sizes byblos can both ENCODE and DECODE and which it can only encode.
//
// It is written to describe the tip AS IT IS, so it PASSES today, and that is
// the point: its job is to PIN the boundary, not to move it. The claim it pins
// against is the doc comment on the exported DecodeJBIG2Generic, which says the
// decoder "inverts EncodeJBIG2Generic UP TO A SIZE, bit-identically, and nothing
// wider", and then names the envelope: any page of at most 67,108,864 pixels
// whose bitmaps pack into 16 MiB. The table below is the measurement behind that
// sentence, taken in both directions -- the four sizes that round-trip, and the
// four the encoder writes and the decoder refuses.
//
// The asymmetry is REAL, and it is deliberate rather than a defect to be closed
// by widening either side. EmbeddedStream has no resource budget at all -- it
// checks only that the dimensions are positive and fit in the 32-bit region
// fields -- while DecodeEmbeddedStream has five, because the read side is where
// an untrusted stream arrives. The encoder's range is therefore strictly the
// wider of the two and always has been: it writes a 600-dpi A3 page in 81 bytes
// and the decoder refuses it.
//
// An earlier version of this comment quoted DecodeJBIG2Generic as claiming the
// two sides were "the exact inverse of EncodeJBIG2Generic and nothing wider" and
// asserted that the claim was FALSE at the tip. The claim was corrected in
// jbig2.go in the same commit that wrote this file, and this file went on
// quoting the retracted wording -- the same drift this test exists to prevent,
// running in the other direction.
//
// Whichever way the boundary moves next -- the budgets raised, or the encoder
// taught to refuse what it cannot read back -- this table has to be edited
// deliberately for it, and DecodeJBIG2Generic's envelope paragraph edited with
// it. That is what stops the two sides drifting apart again silently.
//
// Every case is a REAL round trip, not a header inspection: the bitmap is
// built, encoded by this package's encoder, and fed back to this package's
// decoder. The bitmaps are blank apart from two corner pixels, so TPGD codes
// them in about 80 bytes and the whole table costs a quarter of a second; the
// pixel-for-pixel correctness of the codec is TestDecodeRoundTripBitIdentical's
// job, and what is being pinned here is the SIZE at which the round trip stops
// existing.
//
// The dimensions and the pixel counts are LITERALS. Written in terms of
// MaxPagePixels or maxStreamBitmapBytes they would move with the constants and
// stop asserting anything.
func TestEncodeDecodeSizeBoundary(t *testing.T) {
	for _, c := range []struct {
		name    string
		w, h    int
		decodes bool
		why     string
	}{
		// ---- encodes AND decodes ----

		// 34,806,376 pixels; the page and the region pack to 4,356,936 bytes
		// each, 8,713,872 in all. The 600-dpi preservation master, the single
		// most likely bitonal page byblos is handed, and the size the budget
		// comment names as its design point.
		{"600dpi-A4", 4961, 7016, true, "34,806,376 pixels, inside the 67,108,864 page budget"},
		// 33,660,000 pixels; 4,210,800 bytes a side.
		{"600dpi-Letter", 5100, 6600, true, "33,660,000 pixels"},
		// 42,840,000 pixels. The largest of the three sheet sizes extract.go
		// names, and still inside.
		{"600dpi-Legal", 5100, 8400, true, "42,840,000 pixels"},
		// 61,867,356 pixels; 7,735,758 bytes a side, 15,471,516 together. The
		// largest page in this table that round-trips, and it clears the memory
		// budget by 1,305,700 bytes -- so the pixel budget is what it is close
		// to, not the byte budget.
		{"800dpi-A4", 6614, 9354, true, "61,867,356 pixels, the largest sheet that round-trips"},

		// ---- encodes, CANNOT decode ----

		// 69,605,736 pixels, 2,496,872 past the page budget. EmbeddedStream
		// writes it in 81 bytes. This is the smallest standard sheet on the
		// wrong side of the line.
		{"600dpi-A3", 7016, 9921, false, "69,605,736 pixels, 2,496,872 past the page budget"},
		// 134,640,000 pixels, almost exactly twice the budget.
		{"1200dpi-Letter", 10200, 13200, false, "134,640,000 pixels, twice the page budget"},
		// 139,320,603 pixels.
		{"600dpi-A2", 9921, 14043, false, "139,320,603 pixels"},
		// The other budget, and the reason this table is not just a restatement
		// of MaxPagePixels. 8,388,609 pixels is an EIGHTH of the page budget,
		// so rule 1 has nothing to say -- but a 1-pixel-wide row still occupies
		// a whole byte, so the page and the region pack to 8,388,609 bytes each
		// and 16,777,218 together, TWO bytes past the memory budget. byblos
		// encodes this column in 109 bytes and cannot read it back.
		{"1x8388609", 1, 8388609, false, "16,777,218 packed bytes, two past the memory budget"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := NewBitmap(c.w, c.h)
			b.Set(0, 0, 1)
			b.Set(c.w-1, c.h-1, 1)

			// The encoder takes every one of these. It has no budget: nothing
			// in EmbeddedStream refuses a size, so if this ever starts failing
			// the boundary has moved to the WRITE side and the table above is
			// describing something that no longer exists.
			s, err := EmbeddedStream(b)
			if err != nil {
				t.Fatalf("EncodeJBIG2Generic's own encoder refused a %dx%d bitmap (%s): %v. "+
					"The encoder is documented as bounded only by the 32-bit region fields; "+
					"if it has grown a size budget, this table is the place to say so.",
					c.w, c.h, c.why, err)
			}
			t.Logf("%s: %dx%d encodes to %d bytes", c.name, c.w, c.h, len(s))

			got, derr := DecodeEmbeddedStream(s)
			if !c.decodes {
				if derr == nil {
					t.Fatalf("a %dx%d page (%s) DECODED. byblos could not read this back when "+
						"this test was written, and DecodeJBIG2Generic's doc comment names "+
						"the envelope this size is outside of. If the budgets were raised on "+
						"purpose, move this case to the round-tripping half and correct that "+
						"envelope paragraph with it -- do not delete the case.",
						c.w, c.h, c.why)
				}
				t.Logf("%s: encodes in %d bytes, does not decode: %v", c.name, len(s), derr)
				return
			}
			if derr != nil {
				t.Fatalf("a %dx%d page (%s) encodes to %d bytes and then will not decode: %v. "+
					"byblos cannot read back what byblos wrote.", c.w, c.h, c.why, len(s), derr)
			}
			if got.W != c.w || got.H != c.h {
				t.Fatalf("decoded %dx%d; want %dx%d", got.W, got.H, c.w, c.h)
			}
			// Bit-identical, padding included. EmbeddedStream masks b's padding
			// on the way through, so both sides are directly comparable.
			if !bytes.Equal(got.Pix, b.Pix) {
				t.Errorf("%s: the round trip is not bit-identical", c.name)
			}
		})
	}
}
