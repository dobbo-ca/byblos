package pdfdoc

import (
	"bytes"
	"compress/zlib"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// pageOf opens a fixture body and returns one page, failing the test on any
// error. The sibling tests use the named-corpus helper in pdfdoc_test.go; these
// fixtures are constructed rather than named, so they need this one.
func pageOf(t *testing.T, body []byte, n int) *Page {
	t.Helper()
	d, err := Open(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p, err := d.Page(n)
	if err != nil {
		t.Fatalf("Page(%d): %v", n, err)
	}
	return p
}

// pageErrOf opens a fixture body and returns the error the page produces,
// failing the test if it produces none.
func pageErrOf(t *testing.T, body []byte, n int) string {
	t.Helper()
	d, err := Open(bytes.NewReader(body))
	if err != nil {
		return err.Error()
	}
	p, err := d.Page(n)
	if err == nil {
		t.Fatalf("Page(%d) = %q, want an error", n, p.Content)
	}
	return err.Error()
}

// TestCatchPanicConvertsAPanicToErrMalformed covers catchPanic directly, and it
// exists because of what byb-3iw's verification measured.
//
// catchPanic guards 14 call sites, and its ONLY exercise was
// TestMalformedTJIsAnErrorNotAPanic, which reaches it through a pdfcpu panic in
// skipTJ. pdfcpu v0.15 fixed that panic -- it returns "corrupt TJ expression"
// now -- so that test had to be amended to accept either form. With the
// amendment applied and catchPanic's body replaced by a no-op, `go test ./...`
// EXITED 0: relaxing the assertion alone would have made a 14-call-site panic
// guard untestable, silently, as a side effect of a dependency bump.
//
// This test does not depend on any pdfcpu version panicking, so that cannot
// happen again.
func TestCatchPanicConvertsAPanicToErrMalformed(t *testing.T) {
	boom := func() (err error) {
		defer catchPanic("probe", &err)
		panic("pdfcpu exploded")
	}
	err := boom()
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	for _, want := range []string{"probe", "pdfcpu exploded"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
}

// TestCatchPanicDoesNotMaskAnOrdinaryError pins the other half of its contract:
// deferred with a named return, it may only turn a crash into an error and never
// overwrite one already being returned.
func TestCatchPanicDoesNotMaskAnOrdinaryError(t *testing.T) {
	sentinel := errors.New("the original error")
	quiet := func() (err error) {
		defer catchPanic("probe", &err)
		return sentinel
	}
	if err := quiet(); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the original error", err)
	}
}

// TestCleanContentsArrayHasNoSeparator pins the concatenation of a /Contents
// array, which became byblos's decision when pageContents replaced pdfcpu's
// PageContent (byb-3iw).
//
// ISO 32000-1 table 30 concatenates the elements as a single stream with no
// byte between them. Before this test the corpus had no clean multi-element
// array at all, and inserting a newline separator changed clean pages by one
// byte per element with the entire suite still green -- which would have put
// wrong content bytes into the bench baseline for every such page.
func TestCleanContentsArrayHasNoSeparator(t *testing.T) {
	got := string(pageOf(t, corpus.CleanContentsArray(), 1).Content)
	if got != corpus.CleanContentsArrayExpected {
		t.Errorf("content = %q (%d bytes)\nwant       %q (%d bytes)",
			got, len(got), corpus.CleanContentsArrayExpected, len(corpus.CleanContentsArrayExpected))
	}
	if strings.Contains(got, "ET\nBT") || strings.Contains(got, "ET BT") {
		t.Errorf("content has a separator between array elements: %q", got)
	}
}

// TestCleanContentsArrayIsNotFlaggedRecovered is ContentRecovered's negative
// case. Round 1 of byb-3iw could set the flag unconditionally with a green
// suite, so a flag that is always true must fail something.
func TestCleanContentsArrayIsNotFlaggedRecovered(t *testing.T) {
	if p := pageOf(t, corpus.CleanContentsArray(), 1); p.ContentRecovered {
		t.Error("ContentRecovered = true for an undamaged /Contents array")
	}
}

// TestNoCorpusPageIsFlaggedRecovered is ContentRecovered's broad negative case.
// Every undamaged and legally-blank page in the corpus must report false, so a
// flag that is set unconditionally -- which round 1 of byb-3iw could do with the
// whole suite green -- fails here.
func TestNoCorpusPageIsFlaggedRecovered(t *testing.T) {
	for _, doc := range corpus.All() {
		d, err := Open(bytes.NewReader(doc.Data))
		if err != nil {
			continue // a fixture that is deliberately unopenable, e.g. malformed
		}
		for n := 1; ; n++ {
			p, err := d.Page(n)
			if err != nil {
				break
			}
			if p.ContentRecovered {
				t.Errorf("%s page %d: ContentRecovered = true, want false", doc.Name, n)
			}
		}
	}
}

// TestBadAdler32IsRecoveredWhole is the easy damage case: only the Adler-32
// trailer is corrupt, so zlib raises ErrChecksum after consuming the final
// deflate block and the WHOLE stream is recovered. pdfcpu v0.13 kept these
// bytes silently; v0.15 refuses the page.
func TestBadAdler32IsRecoveredWhole(t *testing.T) {
	p := pageOf(t, corpus.BadAdler32ContentStream(), 1)
	const want = "BT /F1 12 Tf 50 750 Td (Recoverable text.) Tj ET"
	if string(p.Content) != want {
		t.Errorf("content = %q (%d bytes), want %q (%d bytes)",
			p.Content, len(p.Content), want, len(want))
	}
	if !p.ContentRecovered {
		t.Error("ContentRecovered = false, want true: the stream failed its checksum")
	}
}

// TestBadAdler32InArrayKeepsBothElements is the defect byb-3iw was filed for
// and the reason round 1 failed.
//
// pdfcpu's relaxed mode drops the damaged element and returns the sibling's
// bytes with NO error, so the page is reported as fine while 91.4% of its
// content is gone (corpus page 150277 p25, 5,452 bytes -> its last 469). The
// assertion is a BYTE COUNT, not an error, because the unfixed code returns no
// error at all -- asserting on an error string would pass on it.
func TestBadAdler32InArrayKeepsBothElements(t *testing.T) {
	p := pageOf(t, corpus.BadAdler32InArray(), 1)
	const bad = "BT /F1 12 Tf 50 730 Td (Bad Adler.) Tj ET"
	const good = "BT /F1 12 Tf 50 750 Td (Good sibling.) Tj ET"
	want := bad + good // the array orders the damaged element first
	if string(p.Content) != want {
		t.Errorf("content = %q (%d bytes)\nwant       %q (%d bytes)",
			p.Content, len(p.Content), want, len(want))
	}
	if !p.ContentRecovered {
		t.Error("ContentRecovered = false, want true")
	}
}

// TestMidBlockTruncationRecoversAPrefix is the damage class where bytes really
// are lost: the deflate payload stops mid-block, so the recovery is a genuine
// prefix and shorter than the original.
//
// It does NOT assert ContentRecovered. pdfcpu truncates this shape ITSELF and
// returns the short prefix with no error, so byblos keeps exactly the bytes
// v0.13 kept and has nothing to key the flag on -- see the KNOWN LIMIT on
// Page.ContentRecovered. What must hold is that the prefix survives.
func TestMidBlockTruncationRecoversAPrefix(t *testing.T) {
	const full = "BT /F1 12 Tf 50 750 Td (This text will be cut short mid-stream.) Tj ET"
	p := pageOf(t, corpus.MidBlockTruncation(), 1)
	got := string(p.Content)
	switch {
	case got == "":
		t.Fatal("content is empty; the prefix was not recovered")
	case len(got) >= len(full):
		t.Errorf("content = %q (%d bytes); a mid-block truncation cannot recover all %d bytes",
			got, len(got), len(full))
	case !strings.HasPrefix(full, got):
		t.Errorf("content = %q is not a prefix of %q", got, full)
	}
	t.Logf("recovered %d of %d bytes, ContentRecovered = %v", len(got), len(full), p.ContentRecovered)
}

// TestFilterChainBadAdlerIsRecoveredWhole is the regression that refuted this
// bead's second attempt, which REFUSED this shape.
//
// The reasoning for refusing was that keeping the bytes would hand a caller a
// still-ASCII85-encoded intermediate. That was wrong: pdfcpu v0.13 nilled the
// checksum error in filter/flateDecode.go BEFORE the remaining filters ran, so
// it returned the complete, correctly decoded content -- measured at 66 bytes,
// byte-identical to the same document with an intact checksum. Refusing lost
// content byblos used to read.
//
// repairChecksum recovers it by putting the stream back on pdfcpu's own decode
// path, so the ASCII85 layer is applied by pdfcpu rather than reimplemented.
func TestFilterChainBadAdlerIsRecoveredWhole(t *testing.T) {
	p := pageOf(t, corpus.FilterChainBadAdler(), 1)
	if string(p.Content) != corpus.FilterChainBadAdlerExpected {
		t.Errorf("content = %q (%d bytes)\nwant       %q (%d bytes)",
			p.Content, len(p.Content), corpus.FilterChainBadAdlerExpected,
			len(corpus.FilterChainBadAdlerExpected))
	}
	if !p.ContentRecovered {
		t.Error("ContentRecovered = false, want true")
	}
}

// TestPredictor12BadAdlerIsRecoveredWhole covers the same mechanism for a
// /DecodeParms predictor, where the recovered bytes need PNG row un-filtering
// after inflation.
//
// pdfcpu v0.13 REFUSED this shape, so recovering it is strictly better than the
// merge target rather than parity with it. It works for the same reason the chain
// does: pdfcpu applies the predictor itself once the trailer is repaired, which
// is exactly what an earlier attempt got wrong by inflating the stream by hand
// and handing back the still-filtered rows.
func TestPredictor12BadAdlerIsRecoveredWhole(t *testing.T) {
	p := pageOf(t, corpus.Predictor12BadAdler(), 1)
	const want = "BT /F1 12 Tf 50 750 Td (Text.) Tj ET"
	if got := strings.TrimRight(string(p.Content), " "); got != want {
		t.Errorf("content = %q, want %q (modulo the fixture's row padding)", got, want)
	}
	if !p.ContentRecovered {
		t.Error("ContentRecovered = false, want true")
	}
}

// TestBadAdlerWithTrailingByteIsRecoveredWhole and its array sibling are the
// fourth regression adversarial verification found, and the last of the same
// class: a per-element failure escalated into a lost page.
//
// The stream's /Length over-declares by one byte -- it swallowed the EOL before
// `endstream`, which producers do routinely -- so the Adler-32 trailer is NOT the
// last four bytes of Raw. A repair that assumed it was wrote the correct checksum
// to the wrong offsets and dropped the element. Measured against v0.13: the lone
// form became a refused page where v0.13 read the content, and the array form
// silently returned half its bytes.
//
// The array case asserts a BYTE COUNT rather than an error, because the broken
// version returned no error at all.
func TestBadAdlerWithTrailingByteIsRecoveredWhole(t *testing.T) {
	p := pageOf(t, corpus.BadAdlerWithTrailingByte(), 1)
	if string(p.Content) != corpus.BadAdlerTrailingExpected {
		t.Errorf("content = %q (%d bytes)\nwant       %q (%d bytes)",
			p.Content, len(p.Content), corpus.BadAdlerTrailingExpected,
			len(corpus.BadAdlerTrailingExpected))
	}
	if !p.ContentRecovered {
		t.Error("ContentRecovered = false, want true")
	}
}

func TestBadAdlerWithTrailingByteInArrayKeepsBothElements(t *testing.T) {
	p := pageOf(t, corpus.BadAdlerWithTrailingByteInArray(), 1)
	want := corpus.BadAdlerTrailingExpected + corpus.BadAdlerTrailingSiblingExpected
	if string(p.Content) != want {
		t.Errorf("content = %q (%d bytes)\nwant       %q (%d bytes)",
			p.Content, len(p.Content), want, len(want))
	}
	if !p.ContentRecovered {
		t.Error("ContentRecovered = false, want true")
	}
}

// TestDoubleFlateBadInnerIsRecoveredWhole and its array sibling are the FIFTH
// regression adversarial verification found, and the fifth instance of one
// mistake: a per-element decode failure escalated into a lost page.
//
// The stream is flate compressed twice and only the INNER layer's Adler-32 is
// corrupt, so the damaged flate layer is not the first one in the pipeline. A
// recovery that repairs the first flate layer it finds rewrites an already-correct
// trailer and drops the element. Measured against v0.13: the lone form became a
// refused page where v0.13 read 372 bytes, and the array form returned 19 of 391
// bytes with NO error -- which is why the array assertion is a byte comparison
// rather than an error check.
//
// Nothing in the suite covered two flate layers before this: the chain fixture is
// [/ASCII85Decode /FlateDecode], one flate only.
func TestDoubleFlateBadInnerIsRecoveredWhole(t *testing.T) {
	p := pageOf(t, corpus.DoubleFlateBadInner(), 1)
	if string(p.Content) != corpus.DoubleFlateExpected {
		t.Errorf("content = %d bytes, want %d", len(p.Content), len(corpus.DoubleFlateExpected))
	}
	if !p.ContentRecovered {
		t.Error("ContentRecovered = false, want true")
	}
}

func TestDoubleFlateBadInnerInArrayKeepsBothElements(t *testing.T) {
	p := pageOf(t, corpus.DoubleFlateBadInnerInArray(), 1)
	want := corpus.DoubleFlateExpected + corpus.DoubleFlateSiblingExpected
	if string(p.Content) != want {
		t.Errorf("content = %d bytes, want %d; returning only %d would be the "+
			"sibling alone, which is the silent regression this pins",
			len(p.Content), len(want), len(corpus.DoubleFlateSiblingExpected))
	}
	if !p.ContentRecovered {
		t.Error("ContentRecovered = false, want true")
	}
}

// TestDoubleFlateBothBadIsRecoveredWhole and its array sibling are the SIXTH
// regression adversarial verification found, and the sixth instance of the same
// mistake -- a per-element decode failure escalated into a lost page.
//
// BOTH flate layers carry a bad Adler-32. A recovery that repairs one layer and
// asks pdfcpu to decode the rest of the pipeline in a single call cannot express
// this: every single-layer repair is defeated by the other layer's damage, so the
// element was dropped. Measured against v0.13, which nils the checksum error on
// EVERY filter application and read the content in full: the lone form became a
// refused page, and the array form returned 46 of 92 bytes with no error.
//
// This is why decodeRecovering walks the pipeline one filter at a time. It also
// covers N damaged layers, not just two.
func TestDoubleFlateBothBadIsRecoveredWhole(t *testing.T) {
	p := pageOf(t, corpus.DoubleFlateBothBad(), 1)
	if string(p.Content) != corpus.DoubleFlateExpected {
		t.Errorf("content = %d bytes, want %d", len(p.Content), len(corpus.DoubleFlateExpected))
	}
	if !p.ContentRecovered {
		t.Error("ContentRecovered = false, want true")
	}
}

func TestDoubleFlateBothBadInArrayKeepsBothElements(t *testing.T) {
	p := pageOf(t, corpus.DoubleFlateBothBadInArray(), 1)
	want := corpus.DoubleFlateExpected + corpus.DoubleFlateSiblingExpected
	if string(p.Content) != want {
		t.Errorf("content = %d bytes, want %d; returning only %d would be the "+
			"sibling alone, which is the silent regression this pins",
			len(p.Content), len(want), len(corpus.DoubleFlateSiblingExpected))
	}
	if !p.ContentRecovered {
		t.Error("ContentRecovered = false, want true")
	}
}

// TestPartialPredictorRowCutTrailerDropsTheElement covers repairTrailer's
// trailer-length guard, which a comment in this package once called unreachable.
//
// It is reachable. pdfcpu swallows a truncated trailer on its passThru path, but
// WITH a /Predictor it uses decodePostProcessRows, which returns
// io.ErrUnexpectedEOF unswallowed when the final row is PARTIAL -- so the recovery
// is entered with fewer than four trailer bytes. Disabling the guard makes this
// document panic with "slice bounds out of range [:83] with capacity 81", turning
// a dropped element into a crash on a malformed file.
//
// The healthy sibling is what makes the outcome observable, and it is also a gain:
// pdfcpu v0.13 refused this page outright with "unexpected EOF".
func TestPartialPredictorRowCutTrailerDropsTheElement(t *testing.T) {
	p := pageOf(t, corpus.PartialPredictorRowCutTrailer(), 1)
	if string(p.Content) != corpus.PartialPredictorSiblingExpected {
		t.Errorf("content = %q, want the healthy sibling %q",
			p.Content, corpus.PartialPredictorSiblingExpected)
	}
	if !p.ContentRecovered {
		t.Error("ContentRecovered = false, want true: an element was dropped")
	}
}

// TestRepairDoesNotInflateABombTwice guards a cost fix that is otherwise invisible:
// none of it changes an output byte, so no behavioural test can catch a regression.
//
// A stream inflating past pdfcpu's 512 MiB cap (filter.DefaultMaxDecodeBytes) must
// be refused at the cost of ONE capped decode, which is what pdfcpu v0.13 costs.
// Recovery makes that easy to lose, because the recovery path decodes again. The
// figures below are real runs of this fixture:
//
//	6,145 MiB  no guards: pageContents decodes, the walk decodes again, and
//	           repairTrailer inflates a third time with io.ReadAll, holding it all
//	4,096 MiB  repairTrailer streams through adler32 instead of holding
//	2,048 MiB  a decode-limit error skips the recovery entirely -- ONE decode,
//	           matching pdfcpu v0.13's measured 2,050 MiB
//
// THE BOUND IS ABSOLUTE, and a relative one was tried first and does not work: the
// regression inflates the CONTROL as well as the subject, so measuring the same
// bomb with an intact trailer moves in step and the ratio never trips. That leaves
// the race detector, which roughly doubles allocation and read 4,097 MiB for a
// correct build -- hence raceEnabled rather than a threshold loose enough to cover
// both, which would have been loose enough to miss the regression too.
func TestRepairDoesNotInflateABombTwice(t *testing.T) {
	switch {
	case testing.Short():
		t.Skip("inflates past the 512 MiB decode cap")
	case raceEnabled:
		t.Skip("the race detector doubles allocation, so the absolute bound cannot hold")
	}
	const inflated = 600 << 20 // past filter.DefaultMaxDecodeBytes, which is 512 MiB

	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zeros := make([]byte, 1<<20)
	for written := 0; written < inflated; written += len(zeros) {
		if _, err := zw.Write(zeros); err != nil {
			t.Fatalf("deflate: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("deflate close: %v", err)
	}
	bomb := z.Bytes()
	bomb[len(bomb)-1] ^= 0xFF // the corrupt trailer that sends it through the recovery

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	d, err := Open(bytes.NewReader(corpus.OnePageRawStream("/Filter /FlateDecode", bomb)))
	if err == nil {
		if _, err := d.Page(1); err == nil {
			t.Error("a stream inflating past the decode limit must not be accepted")
		}
	}
	runtime.ReadMemStats(&after)

	grew := after.TotalAlloc - before.TotalAlloc
	const limit = 3 << 30 // one capped decode measures 2 GiB; half again over it
	t.Logf("TotalAlloc grew %d MiB while inflating %d MiB", grew>>20, inflated>>20)
	if grew > limit {
		t.Errorf("TotalAlloc grew %d MiB, want under %d MiB: a stream past the decode "+
			"limit is being inflated more than once", grew>>20, limit>>20)
	}
}

// TestTruncatedAdlerTrailerIsReadByPdfcpu pins a pdfcpu behaviour that byblos's
// recovery depends on NOT having to handle.
//
// A stream whose Adler-32 trailer is cut short still decodes: pdfcpu returns the
// complete content with no error, so repairChecksum is never called for it. That
// makes repairChecksum's `rest.Len() < adlerLen` guard unreachable through the
// public API today -- it is retained because the alternative, if this behaviour
// ever changes, is a slice-bounds panic rather than a refusal, and this test is
// what will notice the change.
func TestTruncatedAdlerTrailerIsReadByPdfcpu(t *testing.T) {
	p := pageOf(t, corpus.TruncatedAdlerTrailer(), 1)
	const want = "BT /F1 12 Tf 50 750 Td (Cut trailer.) Tj ET"
	if string(p.Content) != want {
		t.Errorf("content = %q, want %q; if this now fails or is refused, "+
			"repairChecksum's truncated-trailer guard has become reachable and needs "+
			"a test of its own", p.Content, want)
	}
}

// TestCorruptElementBesideHealthySiblingKeepsTheSibling is the other regression
// that refuted the second attempt: a per-element failure was escalated into a
// whole-page error.
//
// The damaged element inflates to nothing, so there is nothing to recover from
// it, and v0.13's relaxed mode simply dropped it and returned the sibling's
// bytes. Refusing the page instead loses a healthy stream. The existing
// CorruptContentStream fixture cannot see this, because it pairs the corrupt
// stream with an EMPTY one.
func TestCorruptElementBesideHealthySiblingKeepsTheSibling(t *testing.T) {
	p := pageOf(t, corpus.CorruptElementBesideHealthySibling(), 1)
	if string(p.Content) != corpus.CorruptElementSiblingExpected {
		t.Errorf("content = %q (%d bytes)\nwant       %q (%d bytes)",
			p.Content, len(p.Content), corpus.CorruptElementSiblingExpected,
			len(corpus.CorruptElementSiblingExpected))
	}
	if !p.ContentRecovered {
		t.Error("ContentRecovered = false, want true: an element was dropped")
	}
}

// TestNullContentsRefIsBlankNotAnError is the third such regression. ISO 32000-1
// 7.3.10 makes a reference to a nonexistent object null and 7.3.9 makes null
// equivalent to omitting the entry, so this page is legally blank. pdfcpu v0.13
// read it as blank; the second attempt refused it with "page content must be
// stream dict or array".
func TestNullContentsRefIsBlankNotAnError(t *testing.T) {
	p := pageOf(t, corpus.NullContentsRef(), 1)
	if len(p.Content) != 0 {
		t.Errorf("Content = %q, want empty", p.Content)
	}
	if p.ContentRecovered {
		t.Error("ContentRecovered = true; a null /Contents is blank, not recovered")
	}
}

// TestCorruptContentStreamIsStillRefused is the boundary the recovery must not
// cross. corpus.CorruptContentStreamPayload is a valid zlib HEADER followed by
// a RESERVED deflate block type, so it inflates to nothing at all.
//
// Zero recovered bytes is not a recovery, and this page stays refused. Note the
// mechanism: flatePrefix rejects it on len(prefix) == 0, not on the error kind
// -- a reserved block type surfaces as flate.CorruptInputError, which IS one of
// the kinds the recovery accepts.
func TestCorruptContentStreamIsStillRefused(t *testing.T) {
	got := pageErrOf(t, corpus.CorruptContentStream(), 2)
	if got == "" {
		t.Error("a content stream that inflates to zero bytes must be refused")
	}
}
