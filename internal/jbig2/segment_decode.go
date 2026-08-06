package jbig2

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Segment types this decoder recognises beyond the two the encoder emits.
// T.88 7.3 gives these as a numbered list, not a table.
const (
	segTypeIntermediateGenericRegion = 36
	segTypeImmediateGenericRegion    = 38
	segTypeEndOfPage                 = 49
	segTypeEndOfStripe               = 50
	segTypeEndOfFile                 = 51
	segTypeProfiles                  = 52
	segTypeTables                    = 53
	segTypeExtension                 = 62
)

// ErrUnsupportedFeature reports a JBIG2 stream this package parsed correctly
// and refuses to decode: a symbol dictionary, a text region, a refinement or
// halftone region, MMR coding, or a generic region using anything other than
// GBTEMPLATE 0 with the nominal AT pixels.
//
// It is a distinct sentinel from a parse failure on purpose. Everything it
// covers is a legal stream some other producer wrote; the honest answer is "not
// implemented here", and the caller's correct response is to divert the page,
// never to present a partial raster. Nothing in this package guesses.
var ErrUnsupportedFeature = errors.New("jbig2: unsupported feature")

// Resource bounds. Region dimensions are 32-bit (T.88 7.4.1), so a corrupt or
// hostile header can ask for 2^64 pixels, and the bitmap is allocated from that
// header before a single coded bit is read.
//
// THE ACCEPTANCE BAR IS PROPORTIONALITY, NOT SMALLNESS. An earlier round of
// this work aimed at "no reachable path costs more than a few milliseconds on
// input under 1 KB", and that bar is not reachable while byblos can also read a
// legitimate 600-dpi scan back: decoding 34.8 million pixels costs hundreds of
// milliseconds however few bytes encode them, and the paragraph below on
// pixels-per-coded-byte explains why no input-length bound can tell the two
// apart. The bar that is both true and protective is this one:
//
//	The worst stream these budgets admit costs about what decoding ONE
//	maximal legitimate page costs. Anything whose decode cost exceeds the
//	pixels it can HAND BACK by more than a small factor is refused from the
//	headers, before one coded bit is read and before one bitmap is
//	allocated.
//
// THAT LAST CLAUSE IS NOT "HAVING ALLOCATED NOTHING", which is what an earlier
// version of this comment claimed. Reading the headers is itself an allocation
// -- one 32-byte descriptor per segment, plus the slice growth to hold them --
// and it is the one cost these rules cannot charge for, because it is paid in
// order to find out what to charge. Measured on this code with rule 5 removed:
// 1,114,113 one-pixel region segments in 41,222,211 bytes of stream, refused by
// rule 3 exactly as intended, from the headers, in 30 ms with nothing decoded --
// having allocated 239.8 MiB to get there, against a 16 MiB bitmap budget, and
// an ACCEPTED stream one region smaller reached 393.9 MiB. Rule 5 is what
// bounds that, and it is the only one of the five that cannot wait for the
// headers to finish parsing.
//
// Five rules, each bounding a different thing. Four are evaluated in planStream
// from segment headers alone; the fifth runs inside parseSegments, while the
// headers are still being read, because what it bounds is the reading. They are
// whole-STREAM rules: a stream may carry any number of individually legal
// regions, every one decoded and RETAINED until the page is composed, so
// bounding each region on its own bounds nothing.
//
//  1. OUTPUT. The page is at most MaxPagePixels. The page is the only thing a
//     caller gets back, and the PDF layer expands it to one byte per pixel, so
//     this is also the ceiling on the *image.Gray byblos hands out. That is why
//     the constant is exported: decodeJBIG2Placement applies the same number to
//     an image dictionary's declared /Width and /Height before it opens the
//     stream at all.
//
//  2. WORK. The regions sum to at most MaxPagePixels. Decode time is linear in
//     this and in nothing else (measured below), so the worst admitted decode is
//     one maximal page's worth of MQ decisions and no more, whatever the stream
//     does with them. parseRegionInfo applies the same cap to ONE region, which
//     is no looser than the sum and costs one multiply.
//
//  3. USEFULNESS -- the rule byb-riy round 3 was missing, and the one that
//     makes rule 2's budget a bound on cost that PRODUCES something. A region
//     that overhangs its page is clipped by composite() under T.88 6.2.4, so
//     every pixel outside the page is decoded and then dropped. Charging a
//     region's pixels without asking whether the page can show them let a
//     67-byte stream -- a 1x1 page under an 8192x4095 region, inside every
//     other rule here -- buy 33,546,240 MQ decisions and hand back one pixel.
//     So the regions may sum to at most maxRegionOverdraw times the page, plus
//     a floor of overdrawFloorPixels.
//
//     maxRegionOverdraw = 4 is chosen to admit legitimate shapes generously:
//     regions tiling a page sum to 1x, and regions deliberately layered over
//     one another under the AND/XOR/XNOR combination operators sum to as many
//     times the page as there are layers. Four layers is past anything a scan
//     produces, and it is 8,386,560 times tighter than the 33,546,240x ratio of
//     the attack above -- there is nothing delicate about where in that range
//     the number sits.
//
//     overdrawFloorPixels = 1<<16 = 65,536 is the floor under that ratio, and it
//     is derived from an ABSOLUTE cost rather than fitted to a fixture. A ratio
//     is the wrong instrument on a SMALL page: T.88 6.2.4's clip rule exists so
//     that a 54x44 region can land on a 20x20 page, and a 300x300 region on a
//     100x100 thumbnail, and those are 5.9x and 9x their pages while costing
//     2,376 and 90,000 MQ decisions -- amounts no caller can perceive. Overdraw
//     is only worth refusing when the work it wastes is LARGE in absolute terms,
//     so the floor is an absolute pixel count, chosen against a cost these
//     budgets already concede unconditionally.
//
//     That cost is the residual. The largest page rule 1 admits takes about
//     1.55s to decode however few bytes encode it (measured below), and no
//     bound makes that smaller. Conceding a THOUSANDTH of it as unpoliced
//     overdraw is not a decision any caller can feel. A thousandth of 1.55s is
//     1.5ms; at 51 Mpx/s, the slowest rate measured below, that is about 77,000
//     pixels, and the power of two under it is 1<<16 = 65,536. Measured over
//     200 decodes on each of the shapes that reach the floor, 65,536 pixels
//     costs 1.0ms at a region width of 4 and 1.5ms at 256 -- so the most a
//     hostile stream can buy from the floor is one and a half milliseconds,
//     1/1,020 of the residual, whatever page size it declares. The previous
//     floor, 1,024, cost 22us on the same measurement.
//
//     Derived that way it clears the tightest legal clip shape by a third: the
//     300x300-on-100x100 case needs 90,000 - 4*10,000 = 50,000. The previous
//     floor of 1,024 was the smallest power of two admitting the ONE clipped
//     fixture in this package's tests, and fitting it to that fixture is what
//     made it refuse every other legal overhang -- 60x50 on 20x20, 256x256 on
//     64x64, 300x300 on 100x100, measured at 89us, 1.53ms and 2.11ms of
//     decoding, and every one of them a document byblos would not open. A false
//     refusal of a legal stream is as much a defect as a missed attack.
//
//     THE FLOOR AND THE RATIO DIVIDE THE SPACE BETWEEN THEM at
//     overdrawFloorPixels / maxRegionOverdraw = 16,384 pixels, which is a
//     128x128 page. Below that the floor is the larger term and is what governs;
//     above it the ratio governs and the floor is a rounding error -- on a
//     600-dpi A4 page it is 0.05% of the allowance. So neither constant can be
//     pinned on a shape the other one reaches, and they are pinned separately:
//     the ratio on a 2048x2048 page, where the floor is 0.4% of the bound, and
//     the floor on a 1x1 page, where the ratio contributes four pixels.
//
//     A page of unknown height (0xFFFFFFFF, T.88 7.4.8.2) is SIZED FROM its
//     regions, so its ratio is about 1 by construction and rule 3 never fires
//     falsely on one. That is why it is evaluated after the loop that resolves
//     the height, not inside it.
//
//  4. MEMORY. The page's packed bytes plus every region's packed bytes are at
//     most maxStreamBitmapBytes. It is separate from rules 1 and 2 because
//     pixels understate memory by up to 8x: a bitmap row is padded to a byte,
//     so a 1-pixel-wide region of 14,680,065 rows is a fifth of the pixel
//     budget and still exhausts the memory budget. Measured before any of this
//     existed: 71 bytes of input, 512 MiB allocated, no error.
//
//  5. HEADER COST. The stream carries at most maxStreamSegments segments, and
//     that is checked as they are parsed rather than afterwards. Rules 1-4 all
//     count PACKED BITMAP BYTES, so nothing in them sees per-segment object
//     overhead at all: a one-pixel region is one byte of bitmap and about 190
//     bytes of everything else -- a 32-byte descriptor in the parsed-segment
//     slice and again in the region slice, a 48-byte entry in the decode
//     slice, a Bitmap header, and the slice growth behind each of those. On a
//     stream of one-pixel regions the budget being charged is a thousandth of
//     the memory actually being used, which is how 41 MB of input reached
//     393.9 MiB with every one of rules 1-4 satisfied.
//
//     maxStreamSegments = 1<<16 = 65,536 is derived from the only reason a
//     legitimate page carries many region segments: STRIPING (T.88 7.4.8.2). A
//     striped page is emitted one stripe at a time, one immediate generic
//     region per stripe, and the finest striping the format permits is one
//     stripe per ROW. So the honest ceiling on regions per page is ROWS per
//     page, and the tallest page these rules can admit is bounded by rule 1
//     once a width is assumed: 67,108,864 pixels over a page 1,024 pixels wide
//     -- 1.7 inches at 600 dpi, narrower than any scanned document sheet -- is
//     65,536 rows. A page narrower still is not a document, and rules 1 and 4
//     already bound what it can cost in bitmap.
//
//     The headroom over anything real is large and one-directional. Byblos's
//     own encoder emits TWO segments per page. The tallest sheet that
//     round-trips here, 800-dpi A4 at 9,354 rows, striped one row at a time --
//     which no producer does; jbig2enc and the Luratech encoders stripe at
//     hundreds of rows or not at all -- comes to 9,355 segments, a seventh of
//     the cap. What the cap refuses is a stream with more region segments than
//     the page it declares has rows, which is a stream no encoder produces.
//
//     What it costs at the cap, measured on this code: a stream of 65,536
//     segments -- one page information segment and 65,535 one-pixel regions --
//     is 2,424,825 bytes, parses in 2.0 ms, and allocates 10.6 MiB of segment
//     descriptors, 12.6 MiB by the time planStream has collected the region
//     segments into their own slice. That is inside the 16 MiB rules 1-4
//     already concede for bitmap, which is the property worth having: the worst
//     header cost is now the same order as the worst bitmap cost instead of
//     being set by the input length. ONE SEGMENT PAST THE CAP is refused in
//     0.6 ms through PageSize having allocated 10.6 MiB and no more, whatever
//     the rest of the stream claims -- and so is a stream FOUR TIMES the cap, at
//     the same 10.6 MiB, which is the property in one line. The check is made
//     INSIDE parseSegments, where the slice is built, and that is what buys it;
//     it is NOT that the check sits before the append, and an earlier version of
//     this comment said it was. See the comment at the check.
//
// THE MAGNITUDES ARE A PRODUCT DECISION, not a security one, and they are the
// only thing here to retune. There is no bound derivable from the input length,
// and it is worth being precise about why, because "a region cannot decode more
// pixels than its coded bytes can describe" is the obvious idea and it is false
// here. Under TPGDON (T.88 6.2.5.7) a row that repeats the row above costs one
// MQ decision, and a run of identical decisions in the most skewed state costs a
// fraction of a bit. This package's own encoder emits a legitimate, losslessly
// coded 8192x65536 blank region -- 536,870,912 pixels -- in SEVEN bytes. Any
// pixels-per-coded-byte ratio loose enough to admit that is loose enough to
// admit anything, and any ratio tight enough to be useful refuses byblos's own
// output on a blank page, which is the exact bug byb-riy exists to fix. So the
// only honest bound is a constant on what the headers CLAIM, and the residual
// cost of the largest stream the constant admits is paid in full.
//
// MaxPagePixels = 1<<26 = 67,108,864 is the design point, and it is stated as a
// PAGE because that is now what it bounds. It admits one 600-dpi preservation
// master on any of the sheet sizes byblos is handed -- A4 (4961x7016,
// 34,806,376 pixels), US Letter (5100x6600, 33,660,000) and US Legal
// (5100x8400, 42,840,000) -- with room for 800-dpi A4 (6614x9354, 61,867,356).
// 600 dpi bitonal is the standard preservation resolution in every archival
// imaging guideline in use, and byblos's own EncodeJBIG2Generic will write one,
// so refusing to read it back is the byb-riy bug one resolution higher. What it
// still refuses: anything at 1200 dpi, and 600-dpi A3 (7016x9921, 69,605,736
// pixels). Retuning is these numbers and nothing else.
//
// maxStreamBitmapBytes = 16<<20 is constrained from both sides or it and the
// pixel budget cannot both be live. A bitmap's pixels never exceed eight times
// its bytes, so a memory budget at or below MaxPagePixels/8 refuses everything
// the pixel budget would have refused, one gate earlier, and the pixel budget
// becomes dead policy; one at or above MaxPagePixels is dead in the same way
// from the other side. MaxPagePixels/4 sits between them, and at this ratio the
// arithmetic is exact: a page and its regions each at the pixel budget pack to
// 8,388,608 bytes apiece when the width is a multiple of 8, which is the memory
// budget to the byte. So rule 4 binds on exactly the shapes whose rows waste
// padding -- a width of 1, 2 or 3 -- and rules 1 and 2 govern everything else.
//
// MEASURED ON THIS CODE AS COMMITTED, on an M-series laptop, CGO_ENABLED=0.
// Every figure here was taken from the final constants and nothing is carried
// over from an earlier round; the pixel counts are counted, not derived, by the
// MQ-decision counter in generic_decode.go.
//
// THE MQ DECODER'S RATE IS A RANGE, AND WHAT SETS IT IS THE REGION'S WIDTH.
// Isolated from composite() by calling decodeGenericRegion directly, TPGDON off
// (the attacker's choice, because it costs one decision per pixel), on
// 67,108,864 pixels twice and 67,107,447 once -- the middle row is a real page
// shape rather than a divisor of the budget, so it lands 1,417 pixels short of
// it, and an earlier version of this comment rounded all three to "67,108,864
// pixels every time":
//
//	region 8 x 8,388,608      1.002-1.043s   64.4-67.0 Mpx/s   67,108,864 px
//	region 4961 x 13,527      1.215-1.233s   54.4-55.3 Mpx/s   67,107,447 px
//	region 8192 x 8192        1.238-1.323s   50.7-54.2 Mpx/s   67,108,864 px
//
// A narrow region's three context rows are a few bytes and stay in L1; a
// page-wide region's are three kilobytes and do not. 51 Mpx/s is the figure to
// size anything by: it is the slowest, and it is the shape a real page has. An
// earlier round of this comment claimed a flat 44 Mpx/s "with TPGDON off". That
// number was decode PLUS composite over a whole page, not the decoder's rate,
// and it understated the decoder by a fifth. Composite is not free: the same
// 67,092,481 pixels through DecodeJBIG2Generic, page-covering on an 8191x8191
// page, take 1.536-1.552s -- 43.2-43.7 Mpx/s end to end.
//
// THE COST OF EVERY SHAPE THAT MATTERS, at each entry point, from 67 bytes of
// JBIG2 except where a stream size is given. Elapsed / TotalAlloc / pixels
// decoded, for pixels returned. One cold run each, and the microsecond rows
// repeat to within about 20% -- EXCEPT the three REFUSAL rows, whose allocation
// is the WARM per-call cost, because a cold figure for those is dominated by
// one-time runtime machinery rather than by the stream. The paragraph under the
// table says why, and why two earlier attempts at those three cells were wrong.
//
//	                                  DecodeJBIG2Generic     ExtractPageRaster
//	8191x8191 page, page-covering     1.552s 16.0MiB 67.1M   1.768s 80.3MiB 67.1M
//	  (the worst admitted for TIME)     for 67,092,481          for 67,092,481
//	8191x8191 page, 1x1 region        145us  8.0MiB  1        54.3ms 72.3MiB 1
//	  (the worst for ALLOCATION at      for 67,092,481          for 67,092,481
//	   67 bytes)
//	600-dpi A4, page-covering         785ms  8.3MiB  34.8M    909ms  41.8MiB 34.8M
//	  (a legitimate scan)               for 34,806,376          for 34,806,376
//	1x1 page, 8192x4095 region        8-644us 0 px            60-108us 0 px
//	  (the stream rule 3 exists for)    REFUSED, 650 B          DIVERTED, ~299 KiB
//	16384x8191 page, 1x1 region       8-24us  0 px            65-98us  0 px
//	  (twice MaxPagePixels, rule 1)     REFUSED, 578 B          DIVERTED, ~298 KiB
//	1x1 page, 64x1025 region          9-84us  0 px            59-98us  0 px
//	  (60 pixels past the floor)        REFUSED, 650 B          DIVERTED, ~299 KiB
//	1x1 page, 4x16385 region          994us  18.4KiB 65,540   1.10ms 316KiB  65,540
//	  (the floor's own boundary)        for 1                   for 1
//	20x20 page, 54x44 region          71us   752B    2,376    138us  299KiB  2,376
//	20x20 page, 60x50 region          79us   848B    3,000    131us  299KiB  3,000
//	64x64 page, 256x256 region        1.53ms 8.9KiB  65,536   1.59ms 311KiB  65,536
//	100x100 page, 300x300 region      2.13ms 13.7KiB 90,000   2.21ms 322KiB  90,000
//	  (the four legal T.88 6.2.4 clip cases. All but the first were REFUSED by
//	   the 1,024-pixel floor an earlier round replaced.)
//
// THE REFUSAL ROWS QUOTE THE WARM COST, WHICH IS THE ONLY PART THAT IS THE
// STREAM'S. Measured over 1,000 repeated calls in three processes per shape, a
// refusal costs 650, 578 and 650 bytes and 0.39-1.37 us. That is what the header
// checks and the error string actually cost.
//
// A COLD MEASUREMENT OF THE SAME REFUSAL IS ABOUT 2.8 KB LARGER, and the excess
// is the first use of Go's error-formatting machinery -- a one-time cost any
// program pays once, not something the stream buys. It also does not settle on a
// single value: two earlier attempts at this table were both refuted by
// remeasurement. The first quoted exact figures (3,344 / 2,672 / 2,672 B), none
// of which reproduced, with 2,672 below everything ever observed. The second
// quoted a pair of values per shape and asserted every run lands on one of them;
// 200 runs per shape then produced values outside the pair. So the lesson is not
// "measure more carefully" -- it is that a cold-start allocation has no true
// figure to quote, in any form, and the honest table does not quote one. The
// rule 1 row is 64 bytes cheaper than the rule 3 rows because its message is
// shorter; the two rule 3 rows agree because they share one message.
//
// The elapsed figures are min-max over those runs including outliers -- one
// 644 us and one 84 us, both first-decode scheduling noise -- with the large
// majority under 25 us. The ExtractPageRaster allocation columns are given to
// three figures for the same reason as above. NONE OF THESE IS A BOUND: they are
// what a sample saw, and a wider range is the expected result of measuring again
// on a busier machine. The bounds are the five rules; this table is evidence
// about their effect, not a specification.
//
// AND THE THREE SHAPES RULE 5 IS FOR, which are not 67-byte streams: nothing
// under a megabyte can carry enough segment headers to matter, and that is the
// point -- the cost is per HEADER, so the input has to be large to buy it, and
// it was still buying 10x its own size.
//
//	                                  DecodeJBIG2Generic     ExtractPageRaster
//	1024x65536 page,                  75.5ms 29.7MiB 65,535  130ms  113.7MiB
//	  65,535 1x1 regions                for 67,108,864          for 67,108,864
//	  (2,424,825 B: the segment cap
//	   itself, and it is ACCEPTED)
//	1024x65536 page,                  1.67ms 10.6MiB 0        1.47ms 18.0MiB 0
//	  65,536 1x1 regions                REFUSED                 DIVERTED
//	  (2,424,862 B: one segment past)
//	512x512 page,                     401us  10.6MiB 0        5.50ms 131.2MiB 0
//	  1,114,112 1x1 regions             REFUSED                 DIVERTED
//	  (41,222,174 B: the shape that
//	   reached 393.9 MiB and was
//	   ACCEPTED before rule 5)
//
// The last row is the whole of rule 5 in one line: the same stream, admitted at
// 393.9 MiB before and refused at 10.6 MiB now, in 401 microseconds. The 131.2
// MiB through ExtractPageRaster is not the decoder -- the PDF carrying it is 41
// MB and has to be read.
//
// Through RecordExtraction over the same shapes on a two-page document, which
// pays for each of them twice: the three 67-byte refusals cost 157-681us and
// decode nothing; the 8191x8191 page-covering stream costs 3.513s and 160.6 MiB
// for 134,184,962 returned pixels; the 600-dpi A4 costs 1.808s and 83.7 MiB; the
// floor's own boundary shape costs 2.13ms. The two rule 5 shapes are diverted on
// both pages, the one-past-the-cap stream in 2.75ms and the 1,114,112-region one
// in 15.8ms -- out of an 82 MB document, having decoded nothing.
//
// The 1.55s residual is not small, and no bound can make it small, because that
// stream is indistinguishable from a legitimate scan -- it IS one, and that is
// what decoding a 67-million-pixel page costs whatever encodes it. What rules 2
// and 3 buy is that nothing costs MORE than that, and that nothing pays it for
// an answer it cannot return. With no budget at all a 326-byte stream cost
// 38.1s and 512 MiB, and a 71-byte one asked for 512 GiB.
const (
	MaxPagePixels        = 1 << 26
	maxStreamBitmapBytes = 16 << 20
	maxRegionOverdraw    = 4
	overdrawFloorPixels  = 1 << 16
	maxStreamSegments    = 1 << 16
)

// segment is one parsed T.88 7.2 segment header together with the slice of the
// input holding its data. The data is not copied.
type segment struct {
	number uint32
	typ    byte
	data   []byte
}

// parseSegments walks the sequence of segment headers in the embedded file
// organization (T.88 7.2, and ISO 32000-1:2008 7.4.7 for what PDF strips: no
// file header, and the segment headers are not separated from their data).
//
// The unknown-data-length form (0xFFFFFFFF, T.88 7.2.7) is rejected. It is only
// decodable by scanning the coded data for a terminating sequence, which means
// running the arithmetic decoder to find out where the segment ends; this
// package's own encoder never emits it. It is reported as ErrUnsupportedFeature
// rather than as damage, because that is what it is: the stream is intact and
// legal and a fuller decoder reads it.
//
// Rule 5 of the budget comment above is enforced here rather than in planStream,
// and it is the only one that is, because what it bounds is the cost of this
// function -- the parsed-segment slice is allocated before any rule can look at
// it.
//
// The page association of T.88 7.2.6 is enforced here for a related reason and
// is documented at the check: segments that disagree about which page they are
// on are refused, and holding the field long enough to compare it in planStream
// would cost every stream a wider segment descriptor.
func parseSegments(s []byte) ([]segment, error) {
	var out []segment
	// The page every segment that names one is on, and the segment it was first
	// seen from. Zero means "no segment has named a page yet", which is exactly
	// the value T.88 7.2.6 gives a segment associated with no page.
	var streamPage, streamPageSeg uint32
	for off := 0; off < len(s); {
		rest := s[off:]
		// The 11-byte minimum is a T.88 7.2 fact -- four bytes of segment
		// number, one of flags, one referred-to count, one page association and
		// four of data length -- and it is also what makes every fixed-offset
		// read below safe without a check of its own: rest[4], rest[5], and the
		// four-byte long-form count at rest[5:9] are all inside it.
		if len(rest) < 11 {
			return nil, fmt.Errorf("jbig2: segment header at offset %d is %d bytes; the minimum is 11", off, len(rest))
		}
		num := binary.BigEndian.Uint32(rest[0:4])
		flags := rest[4]
		typ := flags & 0x3F
		i := 5

		// Referred-to segment count and retain flags (T.88 7.2.4). Counts up to
		// 4 fit in the top 3 bits of a single byte alongside the retain bits; a
		// value of 7 there selects the long form, a 4-byte count whose low 29
		// bits are the count, followed by ceil((count+1)/8) retain bytes.
		//
		// The four-byte long-form count is read at rest[5:9], which the 11-byte
		// minimum above already covers -- i is 5 at this point and can be
		// nothing else -- so there is no second length check here. There was
		// one, and it was unreachable: its condition was 9 > len(rest) with
		// len(rest) >= 11 enforced eleven lines earlier. Anything that lowers
		// that minimum has to put it back.
		count := int(rest[i] >> 5)
		if count == 7 {
			n := binary.BigEndian.Uint32(rest[i:i+4]) & 0x1FFFFFFF
			if uint64(n) > uint64(len(s)) {
				return nil, fmt.Errorf("jbig2: segment %d: refers to %d segments in a %d-byte stream", num, n, len(s))
			}
			count = int(n)
			i += 4 + (count+8)/8
		} else {
			i++
		}

		// Referred-to segment numbers (T.88 7.2.5): sized by THIS segment's
		// number, not by the numbers being referred to.
		refSize := 1
		switch {
		case num > 65536:
			refSize = 4
		case num > 256:
			refSize = 2
		}
		i += count * refSize

		// Page association (T.88 7.2.6): four bytes when bit 6 of the flags is
		// set, one otherwise. Its OFFSET is kept here and its VALUE read after
		// the bounds check below, because the field is part of the header that
		// check is about and nothing may read a byte of a header before
		// establishing that the header is all there.
		pageOff, pageLen := i, 1
		if flags&0x40 != 0 {
			pageLen = 4
		}
		i += pageLen

		// i has accumulated count*refSize, and count is attacker-chosen up to
		// len(s). The "i < 0" half of this is DEAD ON A 64-BIT BUILD and is kept
		// for the 32-bit one, where count is bounded by a len that can reach
		// 2^31 and multiplying it by 4 wraps to a negative offset -- which then
		// slices rest backwards. It is deliberately not given a test, because no
		// test on this platform can fail without it; it is the same 32-bit sign
		// hazard EmbeddedStream's uint64 comparison guards (segment.go), and the
		// same one composite()'s negative-coordinate clip guards. Dropping
		// 32-bit support is the only thing that makes all three removable, and
		// they go together or not at all. pageOff is that same wrapped offset one
		// field earlier -- i can come back non-negative from a page association
		// added to a negative pageOff -- so it is dead on this build for the same
		// reason and guarded here for the same reason.
		if i < 0 || pageOff < 0 || i+4 > len(rest) {
			return nil, fmt.Errorf("jbig2: segment %d: header runs past the end of the stream", num)
		}

		// The page association is readable now, and only now: pageOff+pageLen is
		// i, and i+4 <= len(rest), so both forms of the field are inside the
		// slice without a bounds check of their own.
		//
		// THE ONLY THING THIS DECODER DOES WITH THE FIELD IS REFUSE A STREAM
		// WHOSE SEGMENTS DISAGREE ABOUT WHICH PAGE THEY ARE ON. It never routes
		// by it: there is one page here and every region is composited onto it.
		//
		// Ignoring the field entirely was the other candidate and it is
		// defensible -- ISO 32000-1:2008 7.4.7 gives a PDF-embedded JBIG2 stream
		// a single page, so there is nothing to route TO -- but it is wrong in
		// the way this package cares about most. A region declaring page 2 under
		// a page information segment declaring page 1 is a region on a page this
		// stream does not describe. Compositing it onto page 1 anyway hands the
		// caller a raster that a decoder honouring the field would not produce,
		// and does it with no error anywhere: the silent accept this package's
		// doc treats as strictly worse than a refusal. Until this check existed,
		// this package's own region fixture with its page association changed
		// from 1 to 2 passed the entire suite, which is how it was found.
		//
		// PAGE 0 IS EXEMPT, and that is not laxity. In T.88 7.2.6 zero is not a
		// page number: it marks a segment as associated with no page, which is
		// what a global segment or an end-of-file segment carries. Refusing on it
		// would refuse a legal stream carrying one of those inline. What is
		// refused is two segments that both NAME a page and name different ones.
		// The value is not required to be 1 either -- AGREEMENT is the property
		// that makes compositing correct, and PDF's rule is about how many pages
		// a stream has, not what they are numbered.
		//
		// The multi-PAGE stream is a different hazard and not this check's to
		// catch: a real multi-page file carries one page information segment per
		// page, and planStream refuses the second one.
		//
		// This lives in parseSegments rather than in planStream for the same
		// reason rule 5 does, and it is a memory reason. planStream sees
		// segments, not headers, so checking it there means carrying the field in
		// the segment descriptor -- which grows it from 32 bytes to 40, a quarter
		// more on the one allocation rule 5 exists to bound, paid by every
		// stream, to hold a value that is read once. Two locals here cost
		// nothing and are exact.
		page := uint32(rest[pageOff])
		if pageLen == 4 {
			page = binary.BigEndian.Uint32(rest[pageOff : pageOff+4])
		}
		if page != 0 {
			if streamPage == 0 {
				streamPage, streamPageSeg = page, num
			} else if page != streamPage {
				return nil, fmt.Errorf("jbig2: segment %d is associated with page %d but segment %d "+
					"is associated with page %d; an embedded JBIG2 stream carries exactly one page "+
					"(ISO 32000-1:2008 7.4.7)", num, page, streamPageSeg, streamPage)
			}
		}

		dataLen := binary.BigEndian.Uint32(rest[i : i+4])
		i += 4
		if dataLen == 0xFFFFFFFF {
			return nil, fmt.Errorf("%w: segment %d has an unknown data length", ErrUnsupportedFeature, num)
		}
		if uint64(dataLen) > uint64(len(rest)-i) {
			return nil, fmt.Errorf("jbig2: segment %d declares %d bytes of data but only %d remain",
				num, dataLen, len(rest)-i)
		}
		// Rule 5, charged here because this append is the allocation it bounds.
		// What buys the bound is that the check is INSIDE this function, where
		// the slice is built, and not in planStream, which cannot look at a
		// stream until the slice already exists: the refusal costs the cap and
		// not the stream.
		//
		// Sitting BEFORE the append rather than after it is not what buys it,
		// and an earlier version of this comment said it was. append's growth
		// for this element type makes its last reallocation at len 58,625 (cap
		// 58,624 -> 73,472), so by the cap the backing array already holds
		// 73,472 elements with 7,936 spare and a 65,537th append would not grow
		// it. Measured both ways at 11,123,976 bytes for the one-past stream:
		// the same figure, not merely a close one. Nothing in the tree can
		// distinguish the two placements and nothing should try to.
		if len(out) == maxStreamSegments {
			return nil, fmt.Errorf("jbig2: stream carries more than %d segments; the limit is "+
				"one region per row of the tallest page these budgets admit, and no encoder "+
				"writes a page with more regions than it has rows", maxStreamSegments)
		}
		out = append(out, segment{number: num, typ: typ, data: rest[i : i+int(dataLen)]})
		off += i + int(dataLen)
	}
	if len(out) == 0 {
		return nil, errors.New("jbig2: stream contains no segments")
	}
	return out, nil
}

// regionInfo is the region segment information field of T.88 7.4.1: 17 bytes
// giving the region's size, its position on the page, and the operator by which
// it combines with what is already there.
type regionInfo struct {
	w, h int
	x, y int
	op   byte
}

func parseRegionInfo(d []byte) (regionInfo, error) {
	if len(d) < 17 {
		return regionInfo{}, fmt.Errorf("jbig2: region segment info is %d bytes; want 17", len(d))
	}
	w := binary.BigEndian.Uint32(d[0:4])
	h := binary.BigEndian.Uint32(d[4:8])
	if w == 0 || h == 0 {
		return regionInfo{}, fmt.Errorf("jbig2: region is %dx%d; dimensions must be positive", w, h)
	}
	if uint64(w)*uint64(h) > MaxPagePixels {
		return regionInfo{}, fmt.Errorf("jbig2: region is %dx%d, %d pixels; the limit is %d",
			w, h, uint64(w)*uint64(h), uint64(MaxPagePixels))
	}
	// Bits 0-2 of the flags byte are the external combination operator; bits
	// 3-7 are reserved. T.88 7.4.1.5 allows only 0-4 there, so 5-7 is a
	// malformed field rather than an operator this package has not implemented.
	op := d[16] & 0x07
	if op > 4 {
		return regionInfo{}, fmt.Errorf("jbig2: external combination operator %d; T.88 7.4.1.5 allows 0-4", op)
	}
	return regionInfo{
		w: int(w), h: int(h),
		x:  int(binary.BigEndian.Uint32(d[8:12])),
		y:  int(binary.BigEndian.Uint32(d[12:16])),
		op: op,
	}, nil
}

// decodeGenericRegionSegment decodes the body of an immediate generic region
// segment (T.88 7.4.6) and returns both the region's bitmap and where it goes.
//
// Everything this package cannot code for is rejected here, before any bit of
// the arithmetic stream is read, because the MQ decoder returns a decision for
// any input whatsoever: decoding an MMR region or a template-1 region as if it
// were template 0 does not fail, it produces noise. The rejections are the only
// thing standing between a stream from another producer and a wrong raster.
func decodeGenericRegionSegment(d []byte) (*Bitmap, regionInfo, error) {
	info, err := parseRegionInfo(d)
	if err != nil {
		return nil, regionInfo{}, err
	}
	if len(d) < 18 {
		return nil, regionInfo{}, fmt.Errorf("jbig2: generic region segment is %d bytes; want at least 18", len(d))
	}
	flags := d[17]
	mmr := flags&0x01 != 0
	template := (flags >> 1) & 0x03
	tpgdon := flags&0x08 != 0
	extTemplate := flags&0x10 != 0

	if mmr {
		return nil, regionInfo{}, fmt.Errorf("%w: MMR-coded generic region", ErrUnsupportedFeature)
	}
	if template != 0 {
		return nil, regionInfo{}, fmt.Errorf("%w: generic region GBTEMPLATE %d (only 0 is implemented)",
			ErrUnsupportedFeature, template)
	}
	if extTemplate {
		return nil, regionInfo{}, fmt.Errorf("%w: generic region EXTTEMPLATE", ErrUnsupportedFeature)
	}

	// AT pixels (T.88 7.4.6.3): four signed byte pairs for GBTEMPLATE 0.
	if len(d) < 26 {
		return nil, regionInfo{}, fmt.Errorf("jbig2: generic region segment is %d bytes; want at least 26 for the AT field", len(d))
	}
	at := d[18:26]
	for i := range at {
		if at[i] != nominalATTemplate0[i] {
			return nil, regionInfo{}, fmt.Errorf("%w: generic region AT pixels % 02X are not the nominal % 02X",
				ErrUnsupportedFeature, at, nominalATTemplate0)
		}
	}

	b, err := decodeGenericRegion(d[26:], info.w, info.h, tpgdon)
	if err != nil {
		return nil, regionInfo{}, err
	}
	return b, info, nil
}

// composite draws region onto page at (x0, y0) under one of the external
// combination operators of T.88 7.4.1 and Table 12. Pixels falling outside the
// page are dropped, which is what T.88 6.2.4 requires of a region that overruns
// its page. op has already been validated as 0-4 by parseRegionInfo.
//
// The NEGATIVE halves of the two clip tests -- px < 0 and py < 0 -- are DEAD ON
// A 64-BIT BUILD and are kept for the 32-bit one. A region's X and Y come from
// two uint32 fields (T.88 7.4.1) through int(...), so where int is 64 bits every
// value they can hold is positive and only the >= half can ever fire; where int
// is 32 bits, an X of 0xFFFFFFFF converts to -1 and page.Set would index
// backwards into the page. They are deliberately not given a test, because no
// test on this platform can fail without them. See parseSegments's "i < 0" and
// EmbeddedStream's uint64 comparison for the same hazard: the three are one
// decision, and dropping 32-bit support is what removes all of them.
func composite(page, region *Bitmap, x0, y0 int, op byte) {
	for y := 0; y < region.H; y++ {
		py := y0 + y
		if py < 0 || py >= page.H {
			continue
		}
		for x := 0; x < region.W; x++ {
			px := x0 + x
			if px < 0 || px >= page.W {
				continue
			}
			s, dst := region.Get(x, y), page.Get(px, py)
			var v int
			switch op {
			case 0: // OR
				v = dst | s
			case 1: // AND
				v = dst & s
			case 2: // XOR
				v = dst ^ s
			case 3: // XNOR
				v = 1 - (dst ^ s)
			default: // 4, REPLACE
				v = s
			}
			page.Set(px, py, v)
		}
	}
}

// DecodeEmbeddedStream decodes a JBIG2 bitstream in the embedded file
// organization -- the form ISO 32000-1:2008 7.4.7 requires of the PDF
// JBIG2Decode filter, and the form EmbeddedStream produces -- and returns the
// composed page bitmap. A set bit is ink, as everywhere else in this package.
//
// It is the inverse of EmbeddedStream and nothing more. It decodes immediate
// generic regions coded with GBTEMPLATE 0 and the nominal AT pixels, composed
// onto the page named by a page information segment. Everything else in JBIG2 --
// symbol dictionaries and text regions, refinement, halftones, MMR, the other
// three templates, non-nominal AT pixels, intermediate regions destined for an
// auxiliary buffer -- is reported as ErrUnsupportedFeature and decoded by
// nobody. That is deliberate: a stream this package half-understands would
// yield a raster that is wrong without being detectably wrong, which is a
// strictly worse outcome for a caller than an error it can route around.
//
// Page-0 (global) segments carried in a PDF /JBIG2Globals stream are not
// consulted. They only ever hold symbol dictionaries and pattern dictionaries,
// which nothing here can use.
func DecodeEmbeddedStream(s []byte) (*Bitmap, error) {
	p, err := planStream(s)
	if err != nil {
		return nil, err
	}

	type placed struct {
		b    *Bitmap
		info regionInfo
	}
	decoded := make([]placed, 0, len(p.regions))
	for _, sg := range p.regions {
		b, info, err := decodeGenericRegionSegment(sg.data)
		if err != nil {
			return nil, fmt.Errorf("jbig2: segment %d: %w", sg.number, err)
		}
		decoded = append(decoded, placed{b, info})
	}

	out := NewBitmap(p.pageW, p.pageH)
	if p.pageDefault == 1 {
		for i := range out.Pix {
			out.Pix[i] = 0xFF
		}
		out.MaskPadding()
	}
	for _, q := range decoded {
		composite(out, q.b, q.info.x, q.info.y, q.info.op)
	}
	return out, nil
}

// PageSize reports the page a stream resolves to, reading only its headers.
// Every refusal DecodeEmbeddedStream makes before decoding -- a malformed
// header, a segment type this package will not touch, the resource budgets --
// it makes here too, and at the same cost, because both run planStream.
//
// It exists for the PDF layer, which knows from the image dictionary what size
// the raster is supposed to be. Checking that BEFORE decoding is the difference
// between refusing a mismatched stream for the price of parsing 26-byte region
// headers and refusing it after having decoded the whole page.
func PageSize(s []byte) (w, h int, err error) {
	p, err := planStream(s)
	if err != nil {
		return 0, 0, err
	}
	return p.pageW, p.pageH, nil
}

// streamPlan is everything a stream's HEADERS determine: the page to compose
// onto, and the region segments that will be composed onto it. Producing one
// costs header parsing only -- no coded data is read and no bitmap is
// allocated -- which is what lets every refusal below happen before the
// expensive part rather than after it.
type streamPlan struct {
	pageW, pageH int
	pageDefault  int
	regions      []segment
}

func planStream(s []byte) (streamPlan, error) {
	segs, err := parseSegments(s)
	if err != nil {
		return streamPlan{}, err
	}

	// Region segments are collected rather than decoded so that a page
	// information segment declaring the unknown height 0xFFFFFFFF (T.88
	// 7.4.8.2, a striped page) can be sized from the regions that will land on
	// it. Their absolute Y coordinates make that exact, not a guess.
	var pageW, pageH int
	var pageDefault int
	sawPageInfo := false
	pageKnownHeight := true
	regions := make([]segment, 0, len(segs))

	for _, sg := range segs {
		switch sg.typ {
		case segTypePageInformation:
			if sawPageInfo {
				return streamPlan{}, fmt.Errorf("%w: more than one page information segment", ErrUnsupportedFeature)
			}
			sawPageInfo = true
			if len(sg.data) < 19 {
				return streamPlan{}, fmt.Errorf("jbig2: page information segment is %d bytes; want 19", len(sg.data))
			}
			w := binary.BigEndian.Uint32(sg.data[0:4])
			h := binary.BigEndian.Uint32(sg.data[4:8])
			// Bit 2 of the flags byte is the page's default pixel value.
			pageDefault = int(sg.data[16]>>2) & 1
			if h == 0xFFFFFFFF {
				pageKnownHeight = false
				h = 0
			}
			if w == 0 {
				return streamPlan{}, errors.New("jbig2: page information segment declares a width of 0")
			}
			if uint64(w) > MaxPagePixels {
				return streamPlan{}, fmt.Errorf("jbig2: page is %d pixels wide; the limit is %d", w, uint64(MaxPagePixels))
			}
			pageW, pageH = int(w), int(h)

		case segTypeImmediateGenericRegion, segTypeImmediateLosslessGenericRegion:
			regions = append(regions, sg)

		case segTypeEndOfPage, segTypeEndOfStripe, segTypeEndOfFile,
			segTypeProfiles, segTypeTables, segTypeExtension:
			// Carry no page content this decoder needs. End-of-stripe would
			// matter to a striped page, but region segments carry absolute Y
			// coordinates, so the page extent is already recoverable without it.

		case segTypeIntermediateGenericRegion:
			// Composed into an auxiliary buffer by a later refinement or text
			// region, never onto the page. Nothing here consumes one, so
			// treating it as immediate would put a region on the page that the
			// producer did not intend to be there.
			return streamPlan{}, fmt.Errorf("%w: intermediate generic region (segment %d)", ErrUnsupportedFeature, sg.number)

		default:
			return streamPlan{}, fmt.Errorf("%w: segment type %d (segment %d)", ErrUnsupportedFeature, sg.typ, sg.number)
		}
	}

	if !sawPageInfo {
		return streamPlan{}, errors.New("jbig2: stream has no page information segment")
	}
	if len(regions) == 0 {
		return streamPlan{}, errors.New("jbig2: stream has no immediate generic region segment")
	}

	// Everything below is read from region HEADERS. No coded data is touched
	// and no bitmap is allocated, so a stream that cannot be afforded is refused
	// for the price of parsing 26 bytes per region.
	//
	// Region info that does not parse is passed over rather than reported here.
	// The decode loop reaches it in stream order and says what is actually wrong
	// with it, which keeps a malformed stream's error the one it was before
	// these budgets existed.
	var regionPixels, bmBytes int64
	for _, sg := range regions {
		info, err := parseRegionInfo(sg.data)
		if err != nil {
			continue
		}
		regionPixels += int64(info.w) * int64(info.h)
		bmBytes += int64((info.w+7)/8) * int64(info.h)
		if regionPixels > MaxPagePixels || bmBytes > maxStreamBitmapBytes {
			return streamPlan{}, fmt.Errorf("jbig2: segment %d: the stream's %d regions want to "+
				"decode %d pixels into %d bytes; the budget for one stream is %d pixels in %d bytes",
				sg.number, len(regions), regionPixels, bmBytes, int64(MaxPagePixels), int64(maxStreamBitmapBytes))
		}
		// An unknown page height is resolved from the region headers, not from
		// the decoded regions: a region placed at y = 0xFFFFFFFF asks for a
		// four-billion-row page, and that has to be refused before the regions
		// under it are decoded rather than after.
		if !pageKnownHeight {
			if bottom := info.y + info.h; bottom > pageH {
				pageH = bottom
			}
		}
	}
	if pageH <= 0 {
		return streamPlan{}, fmt.Errorf("jbig2: page height resolves to %d", pageH)
	}

	// Rules 1 and 4. The page bitmap is held alongside every region bitmap and
	// is the only thing the caller gets back, so it is capped on its own pixels
	// and charged into the shared memory budget. It is charged LAST so that a
	// stream whose regions alone are unaffordable is reported against the region
	// that broke the budget.
	pagePixels := int64(pageW) * int64(pageH)
	bmBytes += int64((pageW+7)/8) * int64(pageH)
	if pagePixels > MaxPagePixels || bmBytes > maxStreamBitmapBytes {
		return streamPlan{}, fmt.Errorf("jbig2: a %dx%d page under %d region(s) wants %d page "+
			"pixels and %d bytes of bitmap; the budget for one stream is %d pixels in %d bytes",
			pageW, pageH, len(regions), pagePixels, bmBytes, int64(MaxPagePixels), int64(maxStreamBitmapBytes))
	}

	// Rule 3, and it runs here rather than in the loop above because a page of
	// unknown height is not sized until that loop has finished. Every region
	// pixel outside the page is decoded and then discarded by composite(), so a
	// stream asking to decode far more than the page can show is asking for work
	// with no output, and that is refusable from the headers.
	if regionPixels > maxRegionOverdraw*pagePixels+overdrawFloorPixels {
		return streamPlan{}, fmt.Errorf("jbig2: a %dx%d page under %d region(s) wants to decode "+
			"%d pixels onto a page that can show %d; the budget admits %dx the page plus %d "+
			"pixels, and everything past the page's edges is decoded and then discarded",
			pageW, pageH, len(regions), regionPixels, pagePixels,
			int64(maxRegionOverdraw), int64(overdrawFloorPixels))
	}

	return streamPlan{pageW: pageW, pageH: pageH, pageDefault: pageDefault, regions: regions}, nil
}
