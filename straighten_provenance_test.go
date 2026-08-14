package byblos

import (
	"bytes"
	"encoding/json"
	"testing"
)

// byb-16j.4 section 7: PageStraighten is a pointer for the same reason
// PageGeometry is one -- a 0.0 degree correction is a real measurement, and
// omitzero on a value type would make it serialize identically to "never
// straightened". A nil pointer must be omitted; a &{Deg:0} must not.
func TestPageProvenanceStraightenedRoundTrips(t *testing.T) {
	raw, err := json.Marshal(PageProvenance{})
	if err != nil {
		t.Fatalf("Marshal(PageProvenance{}) error = %v", err)
	}
	if bytes.Contains(raw, []byte("straightened")) {
		t.Errorf("Marshal(PageProvenance{}) = %s; want no \"straightened\" key for a nil pointer", raw)
	}

	in := PageProvenance{Straightened: &PageStraighten{Deg: 0}}
	raw, err = json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !bytes.Contains(raw, []byte(`"straightened"`)) {
		t.Errorf("Marshal(%+v) = %s; want a \"straightened\" key present for &{Deg:0}", in, raw)
	}
	var out PageProvenance
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if out.Straightened == nil {
		t.Fatal("round-tripped Straightened = nil; want a non-nil &{Deg:0}, distinguishable from absent")
	}
	if out.Straightened.Deg != 0 {
		t.Errorf("Straightened.Deg = %v; want 0", out.Straightened.Deg)
	}

	in = PageProvenance{Straightened: &PageStraighten{Deg: -1.7}}
	raw, err = json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	out = PageProvenance{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if out.Straightened == nil || out.Straightened.Deg != -1.7 {
		t.Errorf("round trip = %+v; want Straightened.Deg = -1.7", out.Straightened)
	}
}

// A record written before this field existed must decode to nil, not to a
// zero-value &{Deg:0} -- the same backward-compatibility argument
// TestPageProvenanceDecodesLegacyRecordWithoutGeometry makes for Geometry.
func TestPageProvenanceDecodesLegacyRecordWithoutStraightened(t *testing.T) {
	const legacy = `{"applied":["extract-raster"]}`
	var out PageProvenance
	if err := json.Unmarshal([]byte(legacy), &out); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if out.Straightened != nil {
		t.Errorf("Straightened = %+v; want nil for a pre-byb-16j.4 record", out.Straightened)
	}
}

// provenance.go's RecordExtractionContext merges only Applied at the old
// record; every other field is rebuilt fresh by extractPage and the old value
// is dropped. Straightened is not derivable from the document -- extractPage
// has no way to know a prior byblos run rotated this content -- so it must be
// explicitly carried forward, the way Applied already is.
func TestRecordExtractionCarriesStraightenedForward(t *testing.T) {
	doc := corpusDoc(t, "scan")

	old := Provenance{
		Version: Version,
		Pages: []PageProvenance{
			{Straightened: &PageStraighten{Deg: 3.2}},
		},
	}
	var withRecord bytes.Buffer
	if err := WriteProvenance(bytes.NewReader(doc), &withRecord, old); err != nil {
		t.Fatalf("WriteProvenance() error = %v", err)
	}

	got, err := RecordExtractionContext(t.Context(), bytes.NewReader(withRecord.Bytes()))
	if err != nil {
		t.Fatalf("RecordExtractionContext() error = %v", err)
	}
	if len(got.Pages) != 1 {
		t.Fatalf("len(Pages) = %d; want 1", len(got.Pages))
	}
	if got.Pages[0].Straightened == nil || got.Pages[0].Straightened.Deg != 3.2 {
		t.Errorf("Pages[0].Straightened = %+v; want &{Deg:3.2} carried forward from the prior record",
			got.Pages[0].Straightened)
	}
}

// The carry-forward must survive a page that DIVERTS. The Applied merge is
// gated on rec.Diverted == "" (a diverted page's other fields are zeroed by
// extractPage, so unioning Applied into it would be wrong), but Straightened
// is not derivable from the document either way, and a page's own content
// skew is exactly the kind of thing a straighten correction changes -- so the
// marker must not be destroyed by the same divert it exists to explain.
func TestRecordExtractionCarriesStraightenedForwardOnDivertedPage(t *testing.T) {
	doc := corpusDoc(t, "tiled")

	old := Provenance{
		Version: Version,
		Pages: []PageProvenance{
			{Straightened: &PageStraighten{Deg: 1.5}},
		},
	}
	var withRecord bytes.Buffer
	if err := WriteProvenance(bytes.NewReader(doc), &withRecord, old); err != nil {
		t.Fatalf("WriteProvenance() error = %v", err)
	}

	got, err := RecordExtractionContext(t.Context(), bytes.NewReader(withRecord.Bytes()))
	if err != nil {
		t.Fatalf("RecordExtractionContext() error = %v", err)
	}
	if len(got.Pages) != 1 {
		t.Fatalf("len(Pages) = %d; want 1", len(got.Pages))
	}
	if got.Pages[0].Diverted == "" {
		t.Fatalf("Pages[0].Diverted = %q; want a divert reason (fixture %q is not a single raster)",
			got.Pages[0].Diverted, "tiled")
	}
	if got.Pages[0].Straightened == nil || got.Pages[0].Straightened.Deg != 1.5 {
		t.Errorf("Pages[0].Straightened = %+v; want &{Deg:1.5} carried forward even though the page diverted",
			got.Pages[0].Straightened)
	}
}

// clonePageProvenance is a shallow `out := in` with explicit deep copies per
// field; a missed field shares its pointer with the source record, so a
// mutation through the export reaches back into the source. This is the
// tripwire for Straightened specifically.
func TestClonePageProvenanceSharesNoStraightenedPointer(t *testing.T) {
	in := PageProvenance{Straightened: &PageStraighten{Deg: 2.0}}
	out := clonePageProvenance(in)
	if out.Straightened == nil {
		t.Fatal("clonePageProvenance() dropped Straightened")
	}
	if out.Straightened == in.Straightened {
		t.Fatal("clonePageProvenance() shares the Straightened pointer with its input; want a deep copy")
	}
	if out.Straightened.Deg != in.Straightened.Deg {
		t.Errorf("out.Straightened.Deg = %v; want %v", out.Straightened.Deg, in.Straightened.Deg)
	}
	out.Straightened.Deg = 99
	if in.Straightened.Deg == 99 {
		t.Error("mutating the clone's Straightened.Deg mutated the input's")
	}
}
