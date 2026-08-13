package byblos

// The gate byb-bjh exists to add.
//
// buildCapabilities (provenance.go) already had one enforced arrow --
// TestEveryCapabilityHasARule (upgrade_test.go) proves every shipped capability
// has a rule in capabilityRules (upgrade.go). The other direction had none: a
// capability string could sit in capabilityRules forever without being
// implemented and without naming any tracked work, and six of the seventeen
// had. They accumulated with the suite green throughout, which is the whole
// argument for a test rather than another paragraph in FUTURE.md.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/jbig2"
)

// futureMDPath is where the deferred capabilities are written up in prose.
// It is a constant for the same reason designSpecPath is: two tests read it.
const futureMDPath = "FUTURE.md"

// capabilityCounts are the sizes of the three lists this file cross-checks,
// written down rather than computed.
//
// THEY ARE HERE BECAUSE A SET COMPARISON CAN AGREE VACUOUSLY. Every other
// assertion below compares one list against another, and two lists that both
// went empty -- a parser that stopped matching, a map that lost its literal --
// agree perfectly. A count is the one claim that cannot be satisfied by having
// nothing to say. The identity at the bottom of
// TestFutureMDDeclaresExactlyTheCapabilitiesThisBuildLacks is the point: these
// three numbers are asserted separately and then have to add up.
const (
	wantRules       = 17 // keys in capabilityRules (upgrade.go)
	wantImplemented = 9  // entries in buildCapabilities (provenance.go)
	wantDeferred    = 8  // capability strings declared in FUTURE.md
)

// TestEveryCapabilityIsImplementedOrTracked is the gate. Every capability
// string byblos knows about is either something this build DOES, or something
// with a bead behind it -- never neither, and never both.
//
// "Never both" is not pedantry. decode-jbig2 is PARTLY built (byb-riy decoded
// generic regions, byb-9v0 symbol mode) and is deliberately NOT in
// buildCapabilities, because UpgradeCandidates skips any capability a document
// already records: claiming it would hide the upgrade from the 37 pages of the
// pinned sample that still want a fuller decoder. So "shipped" means "complete enough to
// claim", and a capability claiming both would mean that judgement was never
// made.
func TestEveryCapabilityIsImplementedOrTracked(t *testing.T) {
	implemented := make(map[string]bool, len(buildCapabilities))
	for _, c := range buildCapabilities {
		implemented[c] = true
	}

	for c := range capabilityRules {
		issue, tracked := capabilityIssue[c]
		switch {
		case implemented[c] && tracked:
			t.Errorf("capability %q is in buildCapabilities AND names issue %s. It is either "+
				"shipped or it is outstanding; claiming both means UpgradeCandidates will skip "+
				"it for documents that still want the missing part", c, issue)
		case implemented[c]:
			// Shipped. TestEveryCapabilityHasARule covers the other direction.
		case tracked:
			if !strings.HasPrefix(issue, "byb-") || len(issue) <= len("byb-") {
				t.Errorf("capability %q names %q, which is not a bead id. The id goes into "+
					"NotImplemented.Issue and reaches a caller's log; a placeholder there is "+
					"the same dead end as no id at all", c, issue)
			}
		default:
			t.Errorf("capability %q is in neither buildCapabilities nor capabilityIssue. "+
				"It is a promise with nothing behind it: no code does it, and no bead says "+
				"who will. File one and add it to capabilityIssue (upgrade.go)", c)
		}
	}
}

// A capabilityIssue entry for a capability no rule mentions is a bead pointing
// at nothing -- the register's own version of the stale reference byb-bjh was
// filed over. It fails here rather than sitting unread.
func TestCapabilityIssueHasNoEntriesWithoutRules(t *testing.T) {
	for c, issue := range capabilityIssue {
		if _, ok := capabilityRules[c]; !ok {
			t.Errorf("capabilityIssue maps %q to %s, but %q has no rule in capabilityRules. "+
				"Either the capability was renamed and this entry was left behind, or the "+
				"rule was deleted and the tracking outlived it", c, issue, c)
		}
	}
}

// TestFutureMDDeclaresExactlyTheCapabilitiesThisBuildLacks is the both-sides
// half, and the reason it reads a file rather than a second literal.
//
// A gate that took its expected vocabulary from the same map it was checking
// would iterate seventeen strings, report seventeen passes, and prove nothing.
// FUTURE.md is written by hand, in prose, in a different file, for a different
// audience -- so agreeing with it is evidence. It is also the exact document
// byb-bjh's complaint was about ("prose is not a tracker"): this closes that
// gap from the other end, by making the code fail when the prose disagrees.
//
// It REPLACES TestFutureCapabilitiesHaveRules, which hardcoded the same eight
// names in upgrade_test.go and could only catch a MISSING rule. Set equality
// catches a rule with no FUTURE.md entry too, which is the direction six
// untracked strings actually drifted in.
func TestFutureMDDeclaresExactlyTheCapabilitiesThisBuildLacks(t *testing.T) {
	fromDoc := futureCapabilities(t)

	implemented := make(map[string]bool, len(buildCapabilities))
	for _, c := range buildCapabilities {
		implemented[c] = true
	}
	var fromCode []string
	for c := range capabilityRules {
		if !implemented[c] {
			fromCode = append(fromCode, c)
		}
	}
	slices.Sort(fromCode)

	if !slices.Equal(fromDoc, fromCode) {
		t.Errorf("FUTURE.md declares %v; capabilityRules minus buildCapabilities is %v.\n"+
			"A capability in the code and not the document is a gap nobody wrote down; one in "+
			"the document and not the code cannot be nominated by UpgradeCandidates at all",
			fromDoc, fromCode)
	}

	if len(capabilityRules) != wantRules {
		t.Errorf("capabilityRules has %d keys; want %d. Adding one is the moment to give it a "+
			"bead or an implementation, so the count is deliberately not computed",
			len(capabilityRules), wantRules)
	}
	if len(buildCapabilities) != wantImplemented {
		t.Errorf("buildCapabilities has %d entries; want %d", len(buildCapabilities), wantImplemented)
	}
	if len(fromDoc) != wantDeferred {
		t.Errorf("FUTURE.md declares %d capability strings; want %d", len(fromDoc), wantDeferred)
	}
	if wantImplemented+wantDeferred != wantRules {
		t.Errorf("%d implemented + %d deferred != %d rules; the three counts above cannot all "+
			"be right", wantImplemented, wantDeferred, wantRules)
	}
}

// futureCapabilities reads the capability strings FUTURE.md declares.
//
// It follows specSection's shape (designspec_pin_test.go): read the file, split
// on lines, match a prefix, no regexp. Two details in the document decide the
// parse and both are load-bearing:
//
//   - ONE ENTRY DECLARES THREE NAMES and says "Capability strings:", plural.
//     Matching only the singular form silently drops decode-jbig2, decode-jpx
//     and decode-tiff -- three of the eight -- and the set comparison would then
//     fail for the wrong reason.
//   - TWO ENTRIES DECLARE NONE, in two different wordings ("none. Do not assign
//     one." for lossy symbol matching, "none — this is a storage-format change"
//     for XMP). Neither is a capability and neither may become one: taking the
//     names from BACKTICKS rather than from the text after the colon handles
//     both without matching on the word "none".
func futureCapabilities(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(futureMDPath)
	if err != nil {
		t.Fatalf("reading %s: %v", futureMDPath, err)
	}
	var out []string
	declarations := 0
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "**Capability string") {
			continue
		}
		declarations++
		rest := line
		for {
			_, after, ok := strings.Cut(rest, "`")
			if !ok {
				break
			}
			name, tail, ok := strings.Cut(after, "`")
			if !ok {
				break
			}
			out = append(out, name)
			rest = tail
		}
	}
	// A reformat that stops the prefix matching would return an empty list and
	// look like a document with nothing deferred in it. It is not this test's
	// job to know how many entries FUTURE.md has, but it is its job to notice
	// that it parsed none of them.
	if declarations == 0 {
		t.Fatalf("%s has no line starting with \"**Capability string\"; the parser is reading "+
			"a format the document no longer uses", futureMDPath)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// --- the sites that construct one ---------------------------------------

// TestEveryNotImplementedSiteNamesTheRegister is the guard notimplemented.go's
// doc comment promises, and it is a REPLACEMENT rather than a restoration.
//
// byb-b3 deleted TestEveryNotImplementedNamesAKnownCapability because its table
// lost its only row when RecompressJPEG stopped returning a *NotImplemented,
// leaving a test that would pass vacuously forever. The doc comment went on
// naming it for two beads (the tombstone is in optimize_test.go). This version
// cannot go vacuous the same way: it does not hold a table of errors, it drives
// the extract path with real documents and reads what comes back, so a site that
// stops returning a *NotImplemented fails here instead of emptying a list.
func TestEveryNotImplementedSiteNamesTheRegister(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pdf        []byte
		capability string
	}{
		{"jpx", codecPDF("JPXDecode", "DeviceRGB", 8, 101, 73, codecFiller(64)), "decode-jpx"},

		// BOTH JBIG2 CASES ARE HERE BECAUSE decodeJBIG2Placement HAS TWO PLACES
		// TO LOSE THE SENTINEL, and one of them does not look like a decode at
		// all. jbig2.PageSizeWithGlobals reads the page size, but it runs the
		// same header walk the decoder does, so some coding modes are refused
		// from the headers and never reach the decode call at all.
		//
		// MUTATION TESTING IS WHY THERE ARE TWO, AND IT TOOK TWO ROUNDS. With
		// only a halftone fixture, deleting markUnsupportedJBIG2 from the
		// PageSizeWithGlobals call left this test green. Adding a second
		// halftone-shaped fixture did not help: BOTH were refused walking the
		// headers, so the decode-time wrap was still never exercised and
		// deleting IT also left the test green. Only a refusal that comes out of
		// the region DATA -- GBTEMPLATE 1 -- reaches the second call.
		//
		// Neither arm is hypothetical. After byb-9v0, 33 of the 37 refusals in
		// the pinned sample are a refinement text region, caught walking the
		// headers; the rest of what byblos declines (MMR, the other templates,
		// non-nominal AT pixels) is decided from the data.
		{"jbig2-refused-walking-the-headers", codecPDF("JBIG2Decode", "DeviceGray", 1, 101, 73,
			refusedJBIG2Stream(t, refusedWalkingHeaders)), "decode-jbig2"},
		{"jbig2-refused-decoding", codecPDF("JBIG2Decode", "DeviceGray", 1, 101, 73,
			refusedJBIG2Stream(t, refusedDecoding)), "decode-jbig2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ExtractPageRaster(bytes.NewReader(tc.pdf), 1)
			if err == nil {
				t.Fatal("ExtractPageRaster returned a raster; want a divert")
			}
			if !errors.Is(err, ErrUnsupportedImageCodec) {
				t.Fatalf("error = %v; want ErrUnsupportedImageCodec", err)
			}
			// The point of the whole bead: a caller can ask, in code, whether
			// this build is the limitation.
			if !errors.Is(err, ErrNotImplemented) {
				t.Fatalf("errors.Is(err, ErrNotImplemented) = false for %v; a caller cannot tell "+
					"a missing capability from a damaged document", err)
			}
			var ni *NotImplemented
			if !errors.As(err, &ni) {
				t.Fatalf("errors.As(err, *NotImplemented) = false for %v", err)
			}

			if ni.Capability != tc.capability {
				t.Errorf("Capability = %q; want %q", ni.Capability, tc.capability)
			}
			// Not free text: the string has to be one UpgradeCandidates
			// understands, or a caller cannot ask whether a newer build would
			// now handle the documents it fell back on.
			if _, ok := capabilityRules[ni.Capability]; !ok {
				t.Errorf("Capability %q has no rule in capabilityRules, so UpgradeCandidates "+
					"cannot answer for it", ni.Capability)
			}
			if want := capabilityIssue[ni.Capability]; ni.Issue != want {
				t.Errorf("Issue = %q; want %q from capabilityIssue. An error that reports a "+
					"different bead than the register tracks sends its reader to the wrong "+
					"place", ni.Issue, want)
			}
			if ni.Why == "" {
				t.Error("Why is empty; the message is all that reaches most logs")
			}
		})
	}
}

// The two points inside decodeJBIG2Placement where an internal/jbig2 refusal
// can arrive, and therefore the two places the sentinel can be dropped.
const (
	refusedWalkingHeaders = "walking the headers" // jbig2.PageSizeWithGlobals
	refusedDecoding       = "decoding"            // jbig2.DecodeEmbeddedStreamWithGlobals
)

// refusedJBIG2Stream is a WELL-FORMED JBIG2 stream byblos declines for its
// coding mode, refused at the named stage. Only such a stream can reach the arm
// under test.
//
// It is built the way jbig2_decode_test.go builds one -- encode a generic
// region, then corrupt one field into a mode byblos does not implement -- and
// which field decides the stage:
//
//   - The SEGMENT TYPE lives in the segment header, so the header walk sees it.
//     Type 36 is an intermediate generic region, which byblos refuses.
//   - GBTEMPLATE lives in the region's DATA, past everything the header walk
//     reads, so only the decoder sees it. Template 1 is refused; only template
//     0 is implemented.
//
// IT CANNOT BE FILLER BYTES. Filler fails the header PARSE, which is damage
// rather than a missing feature, and TestDamagedJBIG2DoesNotClaimAMissingCapability
// asserts filler takes the other arm -- so a fixture that quietly became damage
// would make these two tests contradict rather than complement each other.
//
// THE STAGE IS ASSERTED, NOT ASSUMED, and that assertion is the whole reason
// this helper takes an argument. Both original fixtures turned out to be
// refused walking the headers, so the pair looked like coverage of two paths
// while exercising one, and deleting the decode-time wrap changed nothing. A
// fixture whose stage silently moves fails here rather than in six months.
func refusedJBIG2Stream(t *testing.T, stage string) []byte {
	t.Helper()
	s, err := EncodeJBIG2Generic(jbig2TestBitmap())
	if err != nil {
		t.Fatalf("EncodeJBIG2Generic: %v", err)
	}
	switch stage {
	case refusedWalkingHeaders:
		// The generic region segment's type byte: past an 11-byte segment
		// header and a 19-byte page information body, then 4 bytes of segment
		// number. jbig2_decode_test.go pins the same offset.
		const regionTypeAt = 11 + 19 + 4
		if s[regionTypeAt] != 39 {
			t.Fatalf("offset assumption broken: segment type byte = %d, want 39", s[regionTypeAt])
		}
		s[regionTypeAt] = 36 // intermediate generic region
	case refusedDecoding:
		// The generic region flags byte: past both segment headers and the
		// 17-byte region segment information field.
		const genericFlagsAt = 11 + 19 + 11 + 17
		s[genericFlagsAt] |= 0x02 // GBTEMPLATE 1
	default:
		t.Fatalf("unknown stage %q", stage)
	}

	if _, err := DecodeJBIG2Generic(s); !errors.Is(err, ErrUnsupportedJBIG2Feature) {
		t.Fatalf("the %s fixture decodes as %v; it has to be a stream byblos refuses for its "+
			"CODING MODE, or this test proves nothing about the arm it exercises", stage, err)
	}
	_, _, perr := jbig2.PageSizeWithGlobals(nil, s)
	refusedEarly := perr != nil
	if want := stage == refusedWalkingHeaders; refusedEarly != want {
		t.Fatalf("the %s fixture is refused by the header walk = %v (%v); want %v. The two "+
			"fixtures exist to reach DIFFERENT calls inside decodeJBIG2Placement, and one that "+
			"drifts to the other stage leaves a wrap untested while the test still passes",
			stage, refusedEarly, perr, want)
	}
	return s
}

// codecFiller is deterministic bytes that are not a valid stream in any codec.
// corpus.jbig2Payload uses the same shape for the same reason: what drives the
// extract path here is the /Filter name, not the payload.
func codecFiller(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte((i*13 + 7) % 251)
	}
	return p
}

// codecPDF is one page holding one page-covering image with the given filter.
//
// It is a near-copy of jbig2GlobalsPDF (jbig2_symbol_test.go) with the filter,
// colour space and depth made parameters and the /JBIG2Globals entry dropped.
// A jpx document could not come from internal/corpus instead: byb-ybu tracks
// adding one there and records why it is not free -- a new corpus document
// means regenerating the committed testdata/oracle/poppler.json with poppler
// installed. A PDF built inside this file needs none of that, and byb-ybu's own
// scope (a document in All(), reachable by every corpus-wide test) is untouched.
func codecPDF(filter, colorSpace string, bpc, w, h int, payload []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, 0, 8)
	reserve := func() int { offsets = append(offsets, -1); return len(offsets) }
	fill := func(n int, body string) {
		offsets[n-1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	fillStream := func(n int, dict string, p []byte) {
		offsets[n-1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<< %s /Length %d >>\nstream\n", n, dict, len(p))
		buf.Write(p)
		buf.WriteString("\nendstream\nendobj\n")
	}

	cat, pages, pg, cont, img := reserve(), reserve(), reserve(), reserve(), reserve()
	fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", pg))
	fill(pg, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>", pages, w, h, img, cont))
	fillStream(cont, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", w, h)))
	fillStream(img, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /%s /BitsPerComponent %d /Filter /%s", w, h, colorSpace, bpc, filter), payload)

	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets)+1, cat, start)
	return buf.Bytes()
}

// A JBIG2 page whose bytes are DAMAGED must not claim a missing capability.
// This is the negative half of the split extractPage now makes, and without it
// the jbig2 arm could return *NotImplemented unconditionally and still pass
// every assertion above.
func TestDamagedJBIG2DoesNotClaimAMissingCapability(t *testing.T) {
	// corpus.jbig2Payload is 64 bytes of filler, not a JBIG2 stream at all, so
	// internal/jbig2 refuses it from the headers WITHOUT ErrUnsupportedFeature.
	data := corpusDoc(t, "jbig2")
	_, err := ExtractPageRaster(bytes.NewReader(data), 1)
	if err == nil {
		t.Fatal("ExtractPageRaster returned a raster for a page of filler bytes")
	}
	if !errors.Is(err, ErrUnsupportedImageCodec) {
		t.Fatalf("error = %v; want ErrUnsupportedImageCodec", err)
	}
	if errors.Is(err, ErrUnsupportedJBIG2Feature) {
		t.Errorf("error = %v; 64 bytes of filler is damage, not a coding mode byblos has yet "+
			"to implement", err)
	}
	if errors.Is(err, ErrNotImplemented) {
		t.Errorf("error = %v; a damaged stream reported as a missing capability tells an "+
			"archive to re-process a page no future build can read", err)
	}
}
