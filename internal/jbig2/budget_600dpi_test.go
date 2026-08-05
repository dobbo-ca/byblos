package jbig2

import "testing"

// TestResourceBudgetsAdmitAFullResolutionScan is the pin from below that the
// budgets did not have in the sense that matters.
//
// TestResourceBudgetsArePinnedFromBelow already asserts that a 400-dpi page is
// admitted, and every mutation of the constants UPWARD is killed by
// TestResourceBudgetsArePinnedFromAbove. What nothing pinned was the design
// point the constants' own comment stated. segment_decode.go said, in the block
// declaring them, that the envelope was one 600-dpi A4 page, and quoted
// "4961x7016, 34.8 million pixels" -- while MaxPagePixels was 1<<25 =
// 33,554,432, so the code refused the exact page its comment said it admitted.
// Nothing failed when that happened, because "is this hostile stream refused?"
// is satisfied by a budget of zero and "is a 400-dpi page admitted?" is
// satisfied by a budget that stops one page short of the requirement.
//
// 600 dpi is not an exotic ask. It is the standard preservation-master
// resolution for bitonal text in every archival imaging guideline in use, which
// makes a 600-dpi bitonal scan the single most likely JBIG2 page byblos will
// ever be handed -- and byblos's own EncodeJBIG2Generic will happily write one
// it then cannot read back, which is the byb-riy bug all over again one
// resolution higher.
//
// The stream is page-covering because that is the only shape this package's
// encoder emits, so it has to clear every rule at once: the page against rule
// 1, the region against rules 2 and 5, both together against rule 4's memory
// budget, and their 1:1 ratio against rule 3. The dimensions are LITERALS, and
// must stay literals: written in terms of MaxPagePixels they would move with
// the constant and stop asserting anything.
func TestResourceBudgetsAdmitAFullResolutionScan(t *testing.T) {
	for _, c := range []struct {
		name string
		w, h int
	}{
		// 4961 x 7016 = 34,806,376 pixels; 621 x 7016 = 4,356,936 packed bytes.
		{"600dpi-A4", 4961, 7016},
		// 5100 x 6600 = 33,660,000 pixels; 638 x 6600 = 4,210,800 packed bytes.
		{"600dpi-Letter", 5100, 6600},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := emptyRegionSegments(c.w, c.h, uint32(c.w), uint32(c.h), 1)
			w, h, err := PageSize(s)
			if err != nil {
				t.Fatalf("a %dx%d page-covering scan is a 600-dpi preservation master, the "+
					"resolution the budget comment itself names as the design point, and it "+
					"must be decodable: %v", c.w, c.h, err)
			}
			if w != c.w || h != c.h {
				t.Errorf("PageSize() = %dx%d; want %dx%d", w, h, c.w, c.h)
			}
		})
	}
}
