package byblos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

func TestCapabilitiesIsSortedAndStable(t *testing.T) {
	got := Capabilities()
	if len(got) == 0 {
		t.Fatal("Capabilities() is empty")
	}
	if !slices.IsSorted(got) {
		t.Errorf("Capabilities() = %v; want sorted", got)
	}
	// The caller must not be able to corrupt the build's capability list.
	got[0] = "tampered"
	if Capabilities()[0] == "tampered" {
		t.Error("Capabilities() returns the package's own slice; want a copy")
	}
}

// B0+B1 delivers exactly these two. Later epics append; this assertion is the
// tripwire that a capability was added without a rule (see upgrade_test.go).
func TestCapabilitiesContainsB1Set(t *testing.T) {
	got := Capabilities()
	for _, want := range []string{"extract-raster", "inspect"} {
		if !slices.Contains(got, want) {
			t.Errorf("Capabilities() = %v; missing %q", got, want)
		}
	}
}

// byb-b3 adds these three. TestEveryCapabilityHasARule only checks the
// buildCapabilities-to-capabilityRules direction (an added capability with no
// rule); it cannot catch one of these being silently dropped from
// buildCapabilities, since removing an entry there trivially leaves it a
// subset of capabilityRules. This is the tripwire for that direction.
func TestCapabilitiesContainsB3Set(t *testing.T) {
	got := Capabilities()
	for _, want := range []string{"quantize-png", "downsample", "jpeg-recompress"} {
		if !slices.Contains(got, want) {
			t.Errorf("Capabilities() = %v; missing %q", got, want)
		}
	}
}

// The PDF carries provenance as JSON under a custom Info-dictionary key
// (design spec section 6), so the round trip must be exact.
func TestProvenanceJSONRoundTrip(t *testing.T) {
	in := &Provenance{
		Version:      Version,
		Capabilities: Capabilities(),
		ProcessedAt:  time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
		Pages: []PageProvenance{
			{Applied: []string{"jbig2-generic", "downsample-150"}},
			{Diverted: "not-single-raster"},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var out Provenance
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if out.Version != in.Version || !slices.Equal(out.Capabilities, in.Capabilities) {
		t.Errorf("round trip = %+v; want %+v", out, in)
	}
	if !out.ProcessedAt.Equal(in.ProcessedAt) {
		t.Errorf("ProcessedAt = %v; want %v", out.ProcessedAt, in.ProcessedAt)
	}
	if len(out.Pages) != 2 || !slices.Equal(out.Pages[0].Applied, in.Pages[0].Applied) {
		t.Errorf("Pages = %+v; want %+v", out.Pages, in.Pages)
	}
	if out.Pages[1].Diverted != "not-single-raster" {
		t.Errorf("Pages[1].Diverted = %q; want \"not-single-raster\"", out.Pages[1].Diverted)
	}
}

// byb-b1.2: a page whose raster was stored skewed keeps its placement matrix in
// the record, because the raster is re-embedded as stored rather than
// straightened. Six numbers, in PDF matrix order, through the same JSON the PDF
// carries.
func TestPageProvenanceCarriesThePlacement(t *testing.T) {
	m := corpus.DeskewPlacement
	in := PageProvenance{Applied: []string{"extract-raster"}, Placement: m[:]}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var out PageProvenance
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !slices.Equal(out.Placement, in.Placement) {
		t.Errorf("Placement = %v; want %v (from %s)", out.Placement, in.Placement, raw)
	}
}

// A page that was handled normally must not emit noise into the record.
func TestPageProvenanceOmitsEmptyFields(t *testing.T) {
	raw, err := json.Marshal(PageProvenance{})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(raw) != "{}" {
		t.Errorf("Marshal(PageProvenance{}) = %s; want {}", raw)
	}
}

// byb-0dz: the serialization half of B5. WriteProvenance/ReadProvenance must
// round-trip a Provenance through a real PDF's Info dictionary, with no
// dependency on extraction (b1) or the text layer (b4) -- born-digital is a
// document neither of those touches.
//
// byb-b5.1 extends this to Geometry: this is the only test in the package that
// goes through a real PDF, so without asserting both boxes here, the new field
// has no coverage on the path that actually matters -- an in-memory
// json.Marshal/Unmarshal round trip would not catch a pdfdoc/pdfcpu-level
// mangling of the property string.
func TestWriteReadProvenanceRoundTrip(t *testing.T) {
	in := Provenance{
		Version:      Version,
		Capabilities: Capabilities(),
		ProcessedAt:  time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC),
		Pages: []PageProvenance{
			{
				Applied: []string{"extract-raster"},
				Geometry: &PageGeometry{
					RasterBox: [4]float64{0, 0, 568.3708, 791.7616},
					PageBox:   [4]float64{0, 0, 612, 792},
				},
			},
			{Diverted: "unsupported-codec-jbig2"},
		},
	}
	var out bytes.Buffer
	if err := WriteProvenance(bytes.NewReader(corpusDoc(t, "born-digital")), &out, in); err != nil {
		t.Fatalf("WriteProvenance() error = %v", err)
	}
	got, err := ReadProvenance(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("ReadProvenance() error = %v", err)
	}
	if got == nil {
		t.Fatal("ReadProvenance() = nil; want the record just written")
	}
	if got.Version != in.Version || !slices.Equal(got.Capabilities, in.Capabilities) {
		t.Errorf("round trip = %+v; want %+v", got, in)
	}
	if !got.ProcessedAt.Equal(in.ProcessedAt) {
		t.Errorf("ProcessedAt = %v; want %v", got.ProcessedAt, in.ProcessedAt)
	}
	if len(got.Pages) != 2 ||
		!slices.Equal(got.Pages[0].Applied, in.Pages[0].Applied) ||
		got.Pages[1].Diverted != in.Pages[1].Diverted {
		t.Errorf("Pages = %+v; want %+v", got.Pages, in.Pages)
	}
	if got.Pages[0].Geometry == nil {
		t.Fatal("Pages[0].Geometry = nil; want the geometry just written")
	}
	if got.Pages[0].Geometry.RasterBox != in.Pages[0].Geometry.RasterBox {
		t.Errorf("Pages[0].Geometry.RasterBox = %v; want %v",
			got.Pages[0].Geometry.RasterBox, in.Pages[0].Geometry.RasterBox)
	}
	if got.Pages[0].Geometry.PageBox != in.Pages[0].Geometry.PageBox {
		t.Errorf("Pages[0].Geometry.PageBox = %v; want %v",
			got.Pages[0].Geometry.PageBox, in.Pages[0].Geometry.PageBox)
	}
	// Pin the on-disk key names, not just the round trip: marshal and
	// unmarshal share the same struct tags, so a struct-only round-trip check
	// cannot see the raster_box/page_box keys transposed or renamed -- it
	// would still round-trip symmetrically. cmd/byblos-annots/main.go declares
	// an independent RasterBox/PageBox pair with these same tags; nothing
	// else ties the two schemas together.
	raw, err := json.Marshal(in.Pages[0].Geometry)
	if err != nil {
		t.Fatalf("json.Marshal(Geometry) error = %v", err)
	}
	var wire struct {
		RasterBox [4]float64 `json:"raster_box"`
		PageBox   [4]float64 `json:"page_box"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("json.Unmarshal(wire) error = %v", err)
	}
	if wire.RasterBox != in.Pages[0].Geometry.RasterBox {
		t.Errorf("wire \"raster_box\" = %v; want %v (the RasterBox value)",
			wire.RasterBox, in.Pages[0].Geometry.RasterBox)
	}
	if wire.PageBox != in.Pages[0].Geometry.PageBox {
		t.Errorf("wire \"page_box\" = %v; want %v (the PageBox value)",
			wire.PageBox, in.Pages[0].Geometry.PageBox)
	}
}

// byb-b5.1: the first backward-compatibility test in this package. Every prior
// round-trip test constructs a Provenance in Go and marshals it; none starts
// from a JSON literal representing what an OLD build actually wrote to disk.
// This one does, because Geometry did not exist before byb-b5.1 and every
// pre-byb-b5.1 record on a real archive looks exactly like this: no "geometry"
// key at all. Unmarshaling it must leave Geometry nil -- nil is the "this
// build recorded no geometry" signal, not "covered the page" -- while
// Placement and DroppedAnnots, which DID exist, still round-trip.
func TestPageProvenanceDecodesLegacyRecordWithoutGeometry(t *testing.T) {
	const legacy = `{"applied":["extract-raster"],"placement":[1,0,0,1,0,0],"dropped_annots":2}`
	var out PageProvenance
	if err := json.Unmarshal([]byte(legacy), &out); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if out.Geometry != nil {
		t.Errorf("Geometry = %+v; want nil for a pre-byb-b5.1 record", out.Geometry)
	}
	if !slices.Equal(out.Placement, []float64{1, 0, 0, 1, 0, 0}) {
		t.Errorf("Placement = %v; want [1 0 0 1 0 0]", out.Placement)
	}
	if out.DroppedAnnots != 2 {
		t.Errorf("DroppedAnnots = %d; want 2", out.DroppedAnnots)
	}
}

// byb-b5.1: the tripwire against a future "tidying" of Geometry from a pointer
// to a value type using Go 1.26's omitzero. A pointer is the only
// representation where "never measured" (nil) and "measured, all-zero box"
// (non-nil, zero boxes) serialize differently. If someone switches
// `*PageGeometry` to `PageGeometry` with `omitzero`, this test starts failing
// because the all-zero geometry below would vanish from the JSON exactly like
// a nil one does -- collapsing a real (if degenerate) measurement into
// indistinguishable-from-absent.
func TestPageProvenanceGeometryZeroValueIsNotOmitted(t *testing.T) {
	raw, err := json.Marshal(PageProvenance{Geometry: &PageGeometry{}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !bytes.Contains(raw, []byte(`"geometry"`)) {
		t.Errorf("Marshal(PageProvenance{Geometry: &PageGeometry{}}) = %s; want a \"geometry\" key present", raw)
	}
	var out PageProvenance
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if out.Geometry == nil {
		t.Errorf("round-tripped Geometry = nil; want a non-nil zero-box PageGeometry, distinguishable from absent")
	}
}

// --- byb-b1.12: PageGeometry gains a clip box -------------------------------
//
// PageGeometry has no ClipBox field today. Every test below references one
// that does not exist yet. THIS IS A DELIBERATE RED-BY-COMPILE-ERROR STATE:
// there is no way to write "a clip box round-trips" without somewhere on the
// struct to put it. `go vet .` at this state must fail with a compile error
// naming the missing ClipBox field.
//
// The chosen shape is `ClipBox *[4]float64`, following RasterBox/PageBox's own
// doc comment at provenance.go ~203 to the letter: "it must carry its OWN
// presence bit -- *[4]float64, or a sibling bool -- and must not be a bare
// [4]float64". A bare [4]float64 would zero-fill on every record written
// before this field existed, and a zero box inside a non-nil Geometry already
// means "measured, and the measurement was degenerate" (the doc comment two
// paragraphs above pins that for RasterBox/PageBox) -- so a value-typed
// ClipBox would make every pre-byb-b1.12 record lie that it measured a
// [0 0 0 0] clip.
//
// SCOPE DECISION, made here because the bead asks for it explicitly: ClipBox
// is populated ONLY when a clip actually narrowed the placement below its
// unclipped raster box -- not on every measured page. The alternative --
// record it whenever Geometry is written, using "no narrowing happened" as one
// of its own values -- has no honest value to use for "unbounded, nothing
// clipped this". Using RasterBox itself as that sentinel collides with the
// real, ordinary case of a clip that happens to land exactly on the raster's
// own edge (a form BBox sized to match its image, say), which is not
// distinguishable from "no clip" if both write the same numbers. Recording
// ClipBox only when it differs from RasterBox keeps non-nil meaning what
// every other presence bit in this struct already means here: "this was
// actually measured, distinctly from its neighbour," not "this field exists."
// A page with no clip in effect leaves ClipBox nil, the same way Geometry
// itself is nil for a page/build that measured nothing.

// A byb-b5.1-era record -- Geometry present, written before ClipBox existed
// -- must decode with ClipBox nil, not a zero-filled [4]float64 that would
// read as a measured empty clip.
func TestPageProvenanceDecodesGeometryWithoutClipBox(t *testing.T) {
	const legacy = `{"applied":["extract-raster"],"geometry":{"raster_box":[0,0,612,792],"page_box":[0,0,612,792]}}`
	var out PageProvenance
	if err := json.Unmarshal([]byte(legacy), &out); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if out.Geometry == nil {
		t.Fatal("Geometry = nil; want the byb-b5.1-era geometry present")
	}
	if out.Geometry.ClipBox != nil {
		t.Errorf("ClipBox = %v; want nil for a record written before byb-b1.12", out.Geometry.ClipBox)
	}
	if out.Geometry.RasterBox != [4]float64{0, 0, 612, 792} {
		t.Errorf("RasterBox = %v; want the pre-existing field to still round-trip", out.Geometry.RasterBox)
	}
}

// A measured clip round-trips through JSON like RasterBox/PageBox do.
func TestPageProvenanceGeometryClipBoxRoundTrips(t *testing.T) {
	in := PageProvenance{Geometry: &PageGeometry{
		RasterBox: [4]float64{0, 0, 612, 792},
		PageBox:   [4]float64{0, 0, 612, 792},
		ClipBox:   &[4]float64{0, 0, 100, 100},
	}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var out PageProvenance
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if out.Geometry == nil || out.Geometry.ClipBox == nil {
		t.Fatalf("Geometry = %+v; want a non-nil ClipBox", out.Geometry)
	}
	if *out.Geometry.ClipBox != *in.Geometry.ClipBox {
		t.Errorf("ClipBox = %v; want %v", *out.Geometry.ClipBox, *in.Geometry.ClipBox)
	}
}

// The wire key is "clip_box", pinned the same way the existing block above
// pins "raster_box"/"page_box": a struct-only round trip cannot see a
// transposed or renamed key, because it would still round-trip symmetrically.
func TestPageProvenanceGeometryClipBoxWireKeyIsClipBox(t *testing.T) {
	in := &PageGeometry{ClipBox: &[4]float64{1, 2, 3, 4}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var wire struct {
		ClipBox *[4]float64 `json:"clip_box"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("Unmarshal(wire) error = %v", err)
	}
	if wire.ClipBox == nil || *wire.ClipBox != *in.ClipBox {
		t.Errorf("wire \"clip_box\" = %v; want %v", wire.ClipBox, in.ClipBox)
	}
}

// When a page's clip did not narrow anything -- the ordinary case, almost
// every page -- ClipBox stays nil and the "clip_box" key must be absent from
// the JSON entirely, exactly like Geometry's own omitempty at the
// PageProvenance level. This is what makes the SCOPE DECISION above legible
// on disk: a record with no "clip_box" key means "nothing clipped this page",
// not "we forgot to check".
func TestPageProvenanceGeometryOmitsClipBoxWhenUnmeasured(t *testing.T) {
	raw, err := json.Marshal(&PageGeometry{RasterBox: [4]float64{0, 0, 612, 792}, PageBox: [4]float64{0, 0, 612, 792}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if bytes.Contains(raw, []byte(`"clip_box"`)) {
		t.Errorf("Marshal(PageGeometry{ClipBox: nil}) = %s; want no \"clip_box\" key", raw)
	}
}

// The tripwire for ClipBox's own presence bit, mirroring
// TestPageProvenanceGeometryZeroValueIsNotOmitted: a genuinely disjoint clip
// is a real, if degenerate, all-zero-width measurement (see
// TestWalkDisjointClipReportsAZeroAreaBoxNotAnInvertedOne in
// internal/content), and it must marshal present, not vanish as though it
// were never measured. If ClipBox is ever "tidied" from *[4]float64 to a bare
// [4]float64 with omitzero, this starts failing.
func TestPageProvenanceGeometryClipBoxZeroValueIsNotOmitted(t *testing.T) {
	raw, err := json.Marshal(&PageGeometry{ClipBox: &[4]float64{}})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !bytes.Contains(raw, []byte(`"clip_box"`)) {
		t.Errorf("Marshal(PageGeometry{ClipBox: &[4]float64{}}) = %s; want a \"clip_box\" key present", raw)
	}
	var out PageGeometry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if out.ClipBox == nil {
		t.Errorf("round-tripped ClipBox = nil; want a non-nil zero-box ClipBox, distinguishable from absent")
	}
}

// PageGeometry.CoversPage applies its own 1.0pt tolerance over the exact
// floats a writer measured, and mirrors PageRaster.CoversPage's guard against
// a degenerate box -- but PageRaster.CoversPage itself uses no tolerance; see
// the PageGeometry doc comment for the fork. byb-b1.3's natural-DPI page is
// the real example: 568.3708 x 791.7616 pt on a 612x792 MediaBox, 92.84% of
// the box.
func TestPageGeometryCoversPage(t *testing.T) {
	tests := []struct {
		name      string
		rasterBox [4]float64
		pageBox   [4]float64
		want      bool
	}{
		{
			name:      "natural-DPI raster leaves a strip: not covered",
			rasterBox: [4]float64{0, 0, 568.3708, 791.7616},
			pageBox:   [4]float64{0, 0, 612, 792},
			want:      false,
		},
		{
			name:      "exactly-filling box: covered",
			rasterBox: [4]float64{0, 0, 612, 792},
			pageBox:   [4]float64{0, 0, 612, 792},
			want:      true,
		},
		{
			name:      "short by less than coverTolerancePt: covered",
			rasterBox: [4]float64{0.4, 0.4, 611.6, 791.6},
			pageBox:   [4]float64{0, 0, 612, 792},
			want:      true,
		},
		{
			name:      "zero PageBox: not covered by anything",
			rasterBox: [4]float64{0, 0, 612, 792},
			pageBox:   [4]float64{0, 0, 0, 0},
			want:      false,
		},
		{
			name:      "degenerate non-zero PageBox (zero width): not covered by anything",
			rasterBox: [4]float64{0, 0, 612, 792},
			pageBox:   [4]float64{100, 100, 100, 100},
			want:      false,
		},
		{
			name:      "inverted PageBox corners: not covered by anything",
			rasterBox: [4]float64{0, 0, 1, 1},
			pageBox:   [4]float64{612, 792, 0, 0},
			want:      false,
		},
		{
			name:      "exactly at the coverTolerancePt boundary: covered",
			rasterBox: [4]float64{1, 1, 611, 791},
			pageBox:   [4]float64{0, 0, 612, 792},
			want:      true,
		},
		{
			name:      "just past the coverTolerancePt boundary: not covered",
			rasterBox: [4]float64{1.01, 1.01, 610.99, 790.99},
			pageBox:   [4]float64{0, 0, 612, 792},
			want:      false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := PageGeometry{RasterBox: tc.rasterBox, PageBox: tc.pageBox}
			if got := g.CoversPage(); got != tc.want {
				t.Errorf("CoversPage() = %v; want %v", got, tc.want)
			}
		})
	}
}

// A document no Byblos build has processed carries no provenance key at all.
// ReadProvenance must report that as "nothing known" (nil, nil), not an error
// -- UpgradeCandidates already treats a nil *Provenance as every capability
// being a candidate, so this is the natural way to describe a never-seen file.
func TestReadProvenanceAbsent(t *testing.T) {
	got, err := ReadProvenance(bytes.NewReader(corpusDoc(t, "born-digital")))
	if err != nil {
		t.Fatalf("ReadProvenance() error = %v", err)
	}
	if got != nil {
		t.Errorf("ReadProvenance() = %+v; want nil", got)
	}
}

func TestVersionIsSemver(t *testing.T) {
	if Version == "" {
		t.Fatal("Version is empty")
	}
	var maj, min, pat int
	if n, err := fmtSscan(Version, &maj, &min, &pat); err != nil || n != 3 {
		t.Errorf("Version = %q; want a MAJOR.MINOR.PATCH semver", Version)
	}
}

func fmtSscan(s string, a, b, c *int) (int, error) {
	return fmt.Sscanf(s, "%d.%d.%d", a, b, c)
}
