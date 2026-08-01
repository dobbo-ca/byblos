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
