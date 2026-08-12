package pdfdoc

// byb-9v0: the page-0 segments a JBIG2 image keeps in /DecodeParms.
//
// Nothing in byblos read this entry before, because the generic-region decoder
// had no use for a symbol dictionary and that is the only thing the entry ever
// carries. A text region, though, is meaningless without the dictionary it
// refers to, and a bulk scanner writes the dictionary ONCE for a whole document
// and puts it here. So on the shape this entry exists for, the image stream
// alone is not the stream: it is half of it.
//
// Every case below is a way of reading it wrongly that yields no error --
// missing globals look exactly like a page with none, and a page with none is
// legal.

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// globalsPDF builds a one-page document whose /Im0 is a JBIG2 image with
// filterEntry as its /Filter and parmsEntry as its /DecodeParms, with extra
// objects appended from object 6 onwards. Neither stream has to be real JBIG2:
// nothing on this path decodes either of them.
func globalsPDF(filterEntry, parmsEntry string, extra ...string) []byte {
	const payload = "JBIG2-IMAGE-BYTES"
	img := fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width 4 /Height 4"+
		" /ColorSpace /DeviceGray /BitsPerComponent 1 /Filter %s%s /Length %d >>\nstream\n%s\nendstream",
		filterEntry, parmsEntry, len(payload), payload)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 4 4]" +
			" /Resources << /XObject << /Im0 5 0 R >> >> /Contents 4 0 R >>",
		"<< /Length 24 >>\nstream\nq 4 0 0 4 0 0 cm /Im0 Do Q\nendstream",
		img,
	}
	return buildPDF(append(objs, extra...))
}

// rawStreamObj is an object body holding payload as an unfiltered stream.
func rawStreamObj(payload string) string {
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(payload), payload)
}

func globalsOf(t *testing.T, pdf []byte) []byte {
	t.Helper()
	d, err := Open(bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	id := image0(t, d, 1)
	g, err := d.RawImageGlobals(id)
	if err != nil {
		t.Fatalf("RawImageGlobals error = %v", err)
	}
	return g
}

const globalSegments = "GLOBAL-SEGMENT-BYTES"

// The plain shape, and the one jbig2enc's PDF output writes: /Filter is a name
// and /DecodeParms is the one dictionary that goes with it.
func TestRawImageGlobalsReadsASingleFilterDecodeParms(t *testing.T) {
	pdf := globalsPDF("/JBIG2Decode", " /DecodeParms << /JBIG2Globals 6 0 R >>",
		rawStreamObj(globalSegments))
	if got := string(globalsOf(t, pdf)); got != globalSegments {
		t.Errorf("globals = %q; want %q", got, globalSegments)
	}
}

// A filter ARRAY makes /DecodeParms an array in the same order (ISO 32000-1
// 7.4.1 table 5). Reading the first entry, or reading the array as though it
// were a dictionary, both yield "no globals" -- which is indistinguishable from
// a page that legitimately has none, so it would divert a page instead of
// erroring on it. ImageInfo.Filter already takes the LAST filter entry for the
// same reason; this is that rule one field over.
func TestRawImageGlobalsReadsTheParallelDecodeParmsArray(t *testing.T) {
	pdf := globalsPDF("[/ASCII85Decode /JBIG2Decode]", " /DecodeParms [null << /JBIG2Globals 6 0 R >>]",
		rawStreamObj(globalSegments))
	if got := string(globalsOf(t, pdf)); got != globalSegments {
		t.Errorf("globals = %q; want %q", got, globalSegments)
	}
}

// A single dictionary against a filter array is not what the specification
// says, and files do it anyway. There is exactly one dictionary and exactly one
// filter that takes parameters, so what it means is not in doubt.
func TestRawImageGlobalsReadsALoneDictionaryAgainstAFilterArray(t *testing.T) {
	pdf := globalsPDF("[/ASCII85Decode /JBIG2Decode]", " /DecodeParms << /JBIG2Globals 6 0 R >>",
		rawStreamObj(globalSegments))
	if got := string(globalsOf(t, pdf)); got != globalSegments {
		t.Errorf("globals = %q; want %q", got, globalSegments)
	}
}

// The globals stream is a stream like any other and may be compressed. Handing
// back the raw bytes of a /FlateDecode stream would hand the decoder deflate
// output and it would report a malformed segment header -- damage, on an intact
// file.
func TestRawImageGlobalsDecodesACompressedGlobalsStream(t *testing.T) {
	pdf := globalsPDF("/JBIG2Decode", " /DecodeParms << /JBIG2Globals 6 0 R >>",
		fmt.Sprintf("<< /Filter /ASCIIHexDecode /Length %d >>\nstream\n%s\nendstream",
			len(hexOf(globalSegments)), hexOf(globalSegments)))
	if got := string(globalsOf(t, pdf)); got != globalSegments {
		t.Errorf("globals = %q; want %q", got, globalSegments)
	}
}

func hexOf(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		fmt.Fprintf(&b, "%02X", s[i])
	}
	b.WriteString(">")
	return b.String()
}

// Absence is not an error: generic-region JBIG2 needs no globals and byblos's
// own encoder writes none.
func TestRawImageGlobalsReportsAbsenceAsNothing(t *testing.T) {
	for _, tc := range []struct{ name, filter, parms string }{
		{"no DecodeParms", "/JBIG2Decode", ""},
		{"DecodeParms without JBIG2Globals", "/JBIG2Decode", " /DecodeParms << >>"},
		{"null DecodeParms", "/JBIG2Decode", " /DecodeParms null"},
		// The array form needs a filter array beside it: pdfcpu rejects the
		// stream dictionary outright when /Filter is a name and /DecodeParms an
		// array, so that shape never reaches this method.
		{"array with no dictionary", "[/ASCII85Decode /JBIG2Decode]", " /DecodeParms [null null]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := globalsOf(t, globalsPDF(tc.filter, tc.parms)); got != nil {
				t.Errorf("globals = %q; want nil", got)
			}
		})
	}
}

// A /JBIG2Globals that does not resolve to a stream is a broken file, and it is
// reported rather than treated as absent: the page needs those segments, so
// silently decoding without them would compose a text region against an empty
// symbol list and hand back a blank page.
func TestRawImageGlobalsReportsAnUnreadableEntry(t *testing.T) {
	pdf := globalsPDF("/JBIG2Decode", " /DecodeParms << /JBIG2Globals 6 0 R >>", "<< /NotAStream true >>")
	d, err := Open(bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	if g, err := d.RawImageGlobals(image0(t, d, 1)); err == nil {
		t.Errorf("RawImageGlobals returned %q and no error for a /JBIG2Globals that is not a stream", g)
	}
}

// The same guard RawImage carries: an id this document never resolved is a
// programming error in the caller, not an image with no globals.
func TestRawImageGlobalsUnknownID(t *testing.T) {
	d, err := Open(bytes.NewReader(globalsPDF("/JBIG2Decode", "")))
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	if _, err := d.RawImageGlobals(9999); err == nil {
		t.Error("RawImageGlobals(9999) error = nil; want an error for an unresolved id")
	}
}
