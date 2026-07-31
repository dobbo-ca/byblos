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
func TestWriteReadProvenanceRoundTrip(t *testing.T) {
	in := Provenance{
		Version:      Version,
		Capabilities: Capabilities(),
		ProcessedAt:  time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC),
		Pages: []PageProvenance{
			{Applied: []string{"extract-raster"}},
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
