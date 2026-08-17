package pdfdoc

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// TestBuildFromPagesWritesEveryObjectItReferences (byb-yul.6) pins the bug
// this bead fixes: pdfcpu's writer decides what gets written by TRAVERSING
// the catalog it is handed, and that traversal knows about a fixed set of
// keys. Anything a page dictionary holds outside that set is migrated by
// buildpages.go's walk -- it gets an output object number and its bytes sit
// in the in-memory context -- and then never reaches the file, because
// api.WriteContext's own walk never visits it. The reference is still there,
// so the output opens and validates; it just points at nothing.
//
// Four independent mechanisms produce this, and each subtest below is the
// smallest fixture that reproduces ONE of them, named in buildpages.go's
// package comment:
//
//	A. writePageDict (pdfcpu/pkg/pdfcpu/writePages.go:70-103) writes the page
//	   dict verbatim and then follows a HARDCODED key list. Anything else on
//	   the page dict is written but never followed.
//	B. writeDeepDict (writeObjects.go:641-649) skips any dict with /Type /Page
//	   unless entry.Valid, and a FRESH context this package builds is never
//	   run through pdfcpu's validator, so entry.Valid is false for every
//	   object it migrated.
//	C. writeObjects.go:608-612 makes any array under a key literally named D
//	   or Dest skip its element 0, on the theory that element 0 of a /Dest
//	   array is a page reference that has already been resolved elsewhere.
//	   /D on a rectilinear measure dictionary (ISO 32000-1 table 271) is a
//	   different thing that happens to share the key name.
//
// Every fixture is built BY HAND, not through this package's own writer,
// because the writer is exactly the thing under test.
//
// If ANY subtest passes at HEAD -- before the self-write swap this bead
// makes -- its fixture is not reproducing the mechanism it claims to. See the
// artifact_property_list case for the one that is easy to get wrong.
func TestBuildFromPagesWritesEveryObjectItReferences(t *testing.T) {
	t.Run("private_page_key", func(t *testing.T) {
		// Mechanism A. /CREO_Tools is not one of writePageDict's hardcoded
		// keys, so pdfcpu's writer emits the reference and never the object
		// behind it.
		src := assembleFixture([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /Contents 4 0 R /MediaBox [0 0 612 792] " +
				"/CREO_Tools 5 0 R >>",
			streamObj("", ""),
			"<< /Marker (creo-tools-payload) >>",
		})
		out := buildRaw(t, src)
		assertPageKeyResolvesTo(t, out, "CREO_Tools", "creo-tools-payload")
	})

	t.Run("prefixed_page_key", func(t *testing.T) {
		// Same mechanism as private_page_key, with a colon in the key name --
		// /AAPL:PPK -- which doubles as the name-escaping check: a name with
		// a delimiter-adjacent character inside it has to survive both the
		// migration walk (a Go map key) and re-serialization unmangled.
		src := assembleFixture([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /Contents 4 0 R /MediaBox [0 0 612 792] " +
				"/AAPL:PPK 5 0 R >>",
			streamObj("", ""),
			"<< /Marker (aapl-ppk-payload) >>",
		})
		out := buildRaw(t, src)
		assertPageKeyResolvesTo(t, out, "AAPL:PPK", "aapl-ppk-payload")
	})

	t.Run("artifact_property_list", func(t *testing.T) {
		// Mechanism B, and the fixture that is easy to get wrong. The
		// OBVIOUS marked-content property list is an optional content group,
		// << /Type /OCG /Name (Layer 1) >>, and measured, it does NOT
		// reproduce this bug: pdfcpu's writer emits it with zero dangling
		// refs, on both a page and a Form XObject. Real documents instead
		// mark an /Artifact span (ISO 32000-1 14.8.2.2, table 330), whose
		// property-list target is << /Type /Page >> -- and /Type /Page is
		// what trips writeDeepDict's skip. Do not "simplify" this fixture to
		// an OCG dict; it would stop testing anything.
		content := "/Artifact /MC0 BDC\nQ\nEMC"
		src := assembleFixture([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /Contents 4 0 R /MediaBox [0 0 612 792] " +
				"/Resources << /Properties << /MC0 5 0 R >> >> >>",
			streamObj("", content),
			"<< /Type /Page >>",
		})
		out := buildRaw(t, src)
		assertPropertyListResolves(t, out, "MC0")
	})

	t.Run("measure_number_format", func(t *testing.T) {
		// Mechanism C. /X, /D and /A are a rectilinear measure dictionary's
		// three unit arrays (ISO 32000-1 table 271); pdfcpu's writer treats
		// /D as a destination array purely by its key name and drops element
		// 0 of it. Asserting all three, rather than just /D, makes a
		// regression here diagnose itself: /X and /A passing while /D fails
		// is the signature of mechanism C specifically, not a generic
		// dangling reference.
		src := assembleFixture([]string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /Contents 4 0 R /MediaBox [0 0 612 792] /VP [5 0 R] >>",
			streamObj("", ""),
			"<< /Type /Viewport /BBox [0 0 612 792] /Measure 6 0 R >>",
			"<< /Type /Measure /Subtype /RL /R (1 in = 1 mi) /X [7 0 R] /D [8 0 R] /A [9 0 R] >>",
			"<< /U (in) /C 1 >>",
			"<< /U (mi) /C 63360 >>",
			"<< /U (sq mi) /C 1 >>",
		})
		out := buildRaw(t, src)
		assertMeasureUnitSurvives(t, out, "X", "in")
		assertMeasureUnitSurvives(t, out, "D", "mi")
		assertMeasureUnitSurvives(t, out, "A", "sq mi")
	})
}

// buildRaw runs BuildFromPages over one page of src and returns the written
// bytes, without asserting anything about them -- unlike build() (in
// buildpages_test.go), which already fails the whole test on the first
// dangling reference and would stop these subtests short of the specific,
// diagnostic assertion each one wants to make.
func buildRaw(t *testing.T, src []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := BuildFromPages(&buf, []PageSource{{Source: bytes.NewReader(src), Page: 1}}); err != nil {
		t.Fatalf("BuildFromPages: %v", err)
	}
	return buf.Bytes()
}

// reopenXt re-reads a written document through pdfcpu directly (not this
// package's own Open, which does not validate and has no reason to expose
// arbitrary private keys) so a test can dereference a key nothing in Doc's
// public surface reaches.
func reopenXt(t *testing.T, out []byte) *model.XRefTable {
	t.Helper()
	ctx, err := api.ReadContext(bytes.NewReader(out), defaultConfig())
	if err != nil {
		t.Fatalf("re-reading the built document: %v", err)
	}
	return ctx.XRefTable
}

func firstPageDict(t *testing.T, xt *model.XRefTable) types.Dict {
	t.Helper()
	root, err := xt.Pages()
	if err != nil || root == nil {
		t.Fatalf("page tree: %v", err)
	}
	pagesDict, err := xt.DereferenceDict(*root)
	if err != nil || pagesDict == nil {
		t.Fatalf("pages dict: %v", err)
	}
	kids, err := xt.DereferenceArray(pagesDict["Kids"])
	if err != nil || len(kids) == 0 {
		t.Fatalf("/Kids: %v", err)
	}
	ref, ok := kids[0].(types.IndirectRef)
	if !ok {
		t.Fatalf("/Kids[0] is a %T, not an indirect reference", kids[0])
	}
	pd, err := xt.DereferenceDict(ref)
	if err != nil || pd == nil {
		t.Fatalf("page dict: %v", err)
	}
	return pd
}

// assertPageKeyResolvesTo dereferences the written page's pageKey and checks
// it holds a dictionary whose /Marker matches want -- proof that the object
// the source named actually made it into the output, not just the reference
// to it.
func assertPageKeyResolvesTo(t *testing.T, out []byte, pageKey, want string) {
	t.Helper()
	xt := reopenXt(t, out)
	pd := firstPageDict(t, xt)
	v, ok := pd.Find(pageKey)
	if !ok {
		t.Fatalf("the written page dict has no /%s", pageKey)
	}
	resolved, err := xt.Dereference(v)
	if err != nil {
		t.Fatalf("dereferencing /%s: %v", pageKey, err)
	}
	d, ok := resolved.(types.Dict)
	if !ok {
		t.Fatalf("/%s resolved to a %T (nil means the reference dangles): the object "+
			"byblos migrated was never written", pageKey, resolved)
	}
	m, ok := d.Find("Marker")
	sl, isStr := m.(types.StringLiteral)
	if !ok || !isStr || sl.Value() != want {
		t.Fatalf("/%s/Marker = %v, want %q", pageKey, m, want)
	}
}

func assertPropertyListResolves(t *testing.T, out []byte, propKey string) {
	t.Helper()
	xt := reopenXt(t, out)
	pd := firstPageDict(t, xt)
	res, err := xt.DereferenceDict(pd["Resources"])
	if err != nil || res == nil {
		t.Fatalf("/Resources: %v", err)
	}
	props, err := xt.DereferenceDict(res["Properties"])
	if err != nil || props == nil {
		t.Fatalf("/Resources/Properties: %v", err)
	}
	v, ok := props.Find(propKey)
	if !ok {
		t.Fatalf("/Resources/Properties has no /%s", propKey)
	}
	resolved, err := xt.Dereference(v)
	if err != nil {
		t.Fatalf("dereferencing /Resources/Properties/%s: %v", propKey, err)
	}
	d, ok := resolved.(types.Dict)
	if !ok {
		t.Fatalf("/Resources/Properties/%s resolved to a %T (nil means the reference dangles)",
			propKey, resolved)
	}
	if ty, _ := d.Find("Type"); ty != types.Name("Page") {
		t.Fatalf("/Resources/Properties/%s /Type = %v, want /Page -- the artifact-property-list "+
			"trap did not reach the writer", propKey, ty)
	}
}

func assertMeasureUnitSurvives(t *testing.T, out []byte, arrayKey, wantUnit string) {
	t.Helper()
	xt := reopenXt(t, out)
	pd := firstPageDict(t, xt)
	vp, err := xt.DereferenceArray(pd["VP"])
	if err != nil || len(vp) == 0 {
		t.Fatalf("/VP: %v", err)
	}
	viewport, err := xt.DereferenceDict(vp[0])
	if err != nil || viewport == nil {
		t.Fatalf("/VP[0]: %v", err)
	}
	measure, err := xt.DereferenceDict(viewport["Measure"])
	if err != nil || measure == nil {
		t.Fatalf("/VP[0]/Measure: %v", err)
	}
	arr, err := xt.DereferenceArray(measure[arrayKey])
	if err != nil || len(arr) == 0 {
		t.Fatalf("/VP[0]/Measure/%s: %v", arrayKey, err)
	}
	d, err := xt.DereferenceDict(arr[0])
	if err != nil || d == nil {
		// This is the exact shape mechanism C leaves behind: /D0->null,
		// nothing else, because the writer's traversal skips element 0 of
		// any array named D or Dest regardless of what it holds.
		t.Errorf("/VP[0]/Measure/%s[0] did not resolve (mechanism C: the writer treats any "+
			"array under a key literally named D or Dest as a destination and skips element 0)",
			arrayKey)
		return
	}
	u, _ := d.Find("U")
	if s, ok := u.(types.StringLiteral); !ok || s.Value() != wantUnit {
		t.Errorf("/VP[0]/Measure/%s[0]/U = %v, want %q", arrayKey, u, wantUnit)
	}
}

// assembleFixture writes a minimal PDF from raw object bodies (object 1 is
// the implicit /Root). Written by hand, the same idiom contract_test.go's
// blankPageDoc and buildpages_test.go's inheritedCropBoxDoc use, because the
// package under test is the writer and these fixtures cannot go through it.
func assembleFixture(objs []string) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, start)
	return buf.Bytes()
}

func streamObj(dict, payload string) string {
	if dict != "" {
		dict += " "
	}
	return fmt.Sprintf("<< %s/Length %d >>\nstream\n%s\nendstream", dict, len(payload), payload)
}
