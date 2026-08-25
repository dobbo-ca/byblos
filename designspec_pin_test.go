package byblos

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// The design spec is prose, and most of it cannot be machine-checked. Two
// pieces of it can, and both have drifted repeatedly (byb-a20): section 4's
// public-API block, which omitted QuantizeIndexed for a whole release, and the
// corpus counts quoted in section 8 and in doc comments across four Go files,
// which said 27 for three successive reconciliations while the corpus grew past
// 30.
//
// These tests pin exactly those two. They deliberately do NOT try to pin prose:
// a test that blocks a legitimate wording change gets deleted the first time it
// does so, and the claims byb-5e8 found (tense, scope, a factual claim about a
// different repository) are not mechanisable from here anyway.
//
// Everything below reads committed files with the standard library only. It
// does not shell out to `go doc`, and that is a deliberate choice rather than
// an omission: CI runs a second, oracle-free pass with a PATH that has no Go
// toolchain on it, and arch_test.go's `go list` guards SKIP in that pass. A
// spec pin that skipped there would be absent from the gate that matters most.
// go/parser is in the standard library and needs no toolchain, so these run
// everywhere.
const designSpecPath = "docs/superpowers/specs/2026-07-27-byblos-design.md"

// specSection returns the body of the design spec section whose heading line
// starts with prefix, up to the next heading of the same level.
func specSection(t *testing.T, prefix string) string {
	t.Helper()
	raw, err := os.ReadFile(designSpecPath)
	if err != nil {
		t.Fatalf("reading the design spec: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, prefix) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("the design spec has no section starting %q", prefix)
	}
	for i := start; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}

// specGoBlock returns the contents of the first ```go fence in section.
func specGoBlock(t *testing.T, section string) string {
	t.Helper()
	_, rest, ok := strings.Cut(section, "```go\n")
	if !ok {
		t.Fatal("the design spec section has no ```go block")
	}
	body, _, ok := strings.Cut(rest, "\n```")
	if !ok {
		t.Fatal("the design spec's ```go block is never closed")
	}
	return body
}

// exportedDecls records every exported top-level declaration in src by name,
// mapped to the kind of declaration it is. A method is keyed "Type.Method".
//
// The spec block is not compilable Go -- it has no imports and its functions
// have no bodies -- but it is PARSABLE Go, which is all this needs. A bodyless
// function declaration is syntactically legal (it is how assembly stubs are
// declared) and only the type checker objects to it.
func exportedDecls(t *testing.T, name, src string) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.GenDecl:
			kind := strings.ToLower(d.Tok.String())
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						k := kind
						if s.Assign.IsValid() {
							k = "alias"
						}
						out[s.Name.Name] = k
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.IsExported() {
							out[n.Name] = kind
						}
					}
				}
			}
		case *ast.FuncDecl:
			if !d.Name.IsExported() {
				continue
			}
			if d.Recv == nil {
				out[d.Name.Name] = "func"
				continue
			}
			// Methods on unexported types are not public API.
			if recv := receiverTypeName(d.Recv); ast.IsExported(recv) {
				out[recv+"."+d.Name.Name] = "method"
			}
		}
	}
	return out
}

// TestExportedDeclsGroupedAliasDoesNotLeakKind guards against a grouped
// GenDecl -- `type ( Alias = X; Real struct{...} )` -- where an alias spec
// earlier in the block mutates the kind used for a later, non-alias spec.
// exportedDecls computed "kind" once per GenDecl and reassigned it to "alias"
// inside the per-spec loop, so every later TypeSpec in the same block
// inherited "alias" even though it was a plain type declaration.
func TestExportedDeclsGroupedAliasDoesNotLeakKind(t *testing.T) {
	const src = `package p
type (
	Alias = int
	Real  struct{ X int }
)
`
	got := exportedDecls(t, "grouped.go", src)
	if got["Alias"] != "alias" {
		t.Errorf(`got["Alias"] = %q, want "alias"`, got["Alias"])
	}
	if got["Real"] != "type" {
		t.Errorf(`got["Real"] = %q, want "type"`, got["Real"])
	}
}

func receiverTypeName(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	e := fl.List[0].Type
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// packageGoFiles is the source of every non-test .go file in the package
// directory, by file name.
func packageGoFiles(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		src, err := os.ReadFile(n)
		if err != nil {
			t.Fatalf("reading %s: %v", n, err)
		}
		out[n] = string(src)
	}
	// Without this a mis-globbed directory read would compare an empty package
	// against an empty spec block and pass.
	if len(out) < 10 {
		t.Fatalf("read %d non-test .go files in the package directory; the package has many more", len(out))
	}
	return out
}

// packageAPI is every exported top-level declaration in the non-test files of
// package byblos, read from the source rather than from a running toolchain.
func packageAPI(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, src := range packageGoFiles(t) {
		for k, v := range exportedDecls(t, name, src) {
			out[k] = v
		}
	}
	return out
}

// TestDesignSpecPublicAPIBlockMatchesThePackage is byb-a20's first check.
//
// Section 4 of the design spec is a ```go block that enumerates the public API.
// It is the document a reader reaches for to answer "what does byblos export",
// and in #31 QuantizeIndexed shipped without being added to it -- a divergence
// nothing detected until a human read both. Identifiers are enumerable, so this
// is a set comparison and not a judgement call.
func TestDesignSpecPublicAPIBlockMatchesThePackage(t *testing.T) {
	block := specGoBlock(t, specSection(t, "## 4. Public API"))
	spec := exportedDecls(t, "spec-section-4.go", block)
	pkg := packageAPI(t)

	// A do-nothing extraction -- wrong heading, wrong fence, a parse that
	// silently yielded nothing -- would otherwise compare two empty sets.
	if len(spec) < 40 {
		t.Fatalf("the spec's section 4 block declares only %d exported identifiers; "+
			"it enumerates the whole public API and should declare far more", len(spec))
	}

	for name, kind := range pkg {
		if _, ok := spec[name]; !ok {
			t.Errorf("%s %s is exported by the package and missing from the design spec's "+
				"section 4 block", kind, name)
		}
	}
	for name, kind := range spec {
		if _, ok := pkg[name]; !ok {
			t.Errorf("the design spec's section 4 block declares %s %s, which the package "+
				"does not export", kind, name)
		}
	}
	for name, kind := range spec {
		if got, ok := pkg[name]; ok && got != kind {
			t.Errorf("%s is a %s in the design spec's section 4 block and a %s in the package",
				name, kind, got)
		}
	}
	t.Logf("design spec section 4 pins %d exported identifiers", len(spec))
}

// --- section 4's struct fields ----------------------------------------------

// exportedStructFields records every exported STRUCT type declared in src,
// mapped to its exported fields as field name -> rendered field type.
//
// exportedDecls above stops at the declaration: it records ImageRef as a "type"
// and never opens it. That is how byb-8u9 sat on main -- inspect.go declared
// ImageRef.ObjNr, the spec's section 4 block did not, and a set of names and
// kinds cannot tell the two apart. This descends one level further, into
// ast.StructType.Fields.
//
// Three normalisations, because the spec writes Go for a reader and the package
// writes it for a compiler:
//
//   - Comments are dropped. The source is parsed WITHOUT parser.ParseComments,
//     so a trailing gloss ("// placement on the page, in points") is never
//     attached to a field in the first place. Nothing here has to strip one.
//   - Grouped fields are expanded. "Width, Height int" yields two entries, so a
//     side that groups and a side that does not still compare equal.
//   - Types are rendered by go/types.ExprString. It is purely syntactic and runs
//     no type checker, which keeps this test toolchain-free like the rest of the
//     file, and it renders one spelling of "[6]float64" or "map[string]uint64"
//     however the source spaced it.
//
// Unexported fields are skipped on both sides: section 4 states the PUBLIC API,
// and a private field is not part of it.
//
// Fields compare as a SET and not as a sequence. See
// TestExportedStructFieldsIgnoresFieldOrder for why.
func exportedStructFields(t *testing.T, name, src string) map[string]map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	out := map[string]map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, s := range gd.Specs {
			ts, ok := s.(*ast.TypeSpec)
			if !ok || !ts.Name.IsExported() {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}
			fields := map[string]string{}
			for _, fld := range st.Fields.List {
				typ := types.ExprString(fld.Type)
				// An embedded field has no name of its own; Go names it after
				// the last identifier of its type, and so does this.
				if len(fld.Names) == 0 {
					if n := embeddedFieldName(fld.Type); ast.IsExported(n) {
						fields[n] = typ
					}
					continue
				}
				for _, n := range fld.Names {
					if n.IsExported() {
						fields[n.Name] = typ
					}
				}
			}
			out[ts.Name.Name] = fields
		}
	}
	return out
}

// embeddedFieldName is the field name Go gives an embedded field: the last
// identifier of its type, with any pointer, qualifier or type argument removed.
func embeddedFieldName(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.StarExpr:
			e = x.X
		case *ast.SelectorExpr:
			return x.Sel.Name
		case *ast.IndexExpr: // an embedded generic type, Foo[T]
			e = x.X
		case *ast.IndexListExpr: // Foo[T, U]
			e = x.X
		default:
			return ""
		}
	}
}

// packageStructFields is exportedStructFields over the whole package. It also
// returns how many package files contributed an exported struct, which is the
// only evidence the caller has that this side of the comparison was read off
// the disk at all; see the guard in
// TestDesignSpecPublicAPIBlockMatchesStructFields.
func packageStructFields(t *testing.T) (map[string]map[string]string, int) {
	t.Helper()
	out := map[string]map[string]string{}
	files := 0
	for name, src := range packageGoFiles(t) {
		found := exportedStructFields(t, name, src)
		if len(found) > 0 {
			files++
		}
		for k, v := range found {
			out[k] = v
		}
	}
	return out, files
}

// TestExportedStructFieldsNormalisesTheSpecsDialect pins the three
// normalisations against the two dialects they exist to reconcile: section 4
// writes Go for a reader, with a trailing gloss on most fields, related fields
// grouped onto one line, and columns aligned by hand; the package writes it for
// a compiler, one field per line, gofmt-spaced, carrying the struct tags that
// section 4 has never quoted.
//
// Ignoring struct tags is the one deliberate hole here, and it is worth naming:
// PageGeometry's `json:"clip_box,omitempty"` is a wire-format fact, the spec
// states the wire format in section 6 rather than in section 4's block, and a
// pin that demanded tags would fail on every serialized struct on the day it
// was written.
func TestExportedStructFieldsNormalisesTheSpecsDialect(t *testing.T) {
	const specDialect = `package p
type T struct {
    Width, Height int             // pixel dimensions
    Placement     [6]float64      // paint matrix, [a b c d e f]
    Reasons       map[string]uint64 // divert reason -> count
}
`
	const pkgDialect = "package p\n" +
		"type T struct {\n" +
		"\tWidth   int\n" +
		"\tHeight  int\n" +
		"\tPlacement [6]float64\n" +
		"\tReasons map[string]uint64 `json:\"reasons,omitempty\"`\n" +
		"}\n"

	want := map[string]string{
		"Width":     "int",
		"Height":    "int",
		"Placement": "[6]float64",
		"Reasons":   "map[string]uint64",
	}
	for _, c := range []struct{ name, src string }{
		{"spec-dialect.go", specDialect},
		{"package-dialect.go", pkgDialect},
	} {
		got := exportedStructFields(t, c.name, c.src)["T"]
		if !maps.Equal(got, want) {
			t.Errorf("%s: exportedStructFields gave %v, want %v", c.name, got, want)
		}
	}
}

// TestExportedStructFieldsIgnoresFieldOrder records a decision rather than
// discovering a bug, because the alternative is to leave it decided by
// omission.
//
// Field order is NOT pinned. When byb-8u9 was fixed all fourteen structs in
// section 4 already listed their fields in the package's order, so an
// order-sensitive comparison would have cost nothing that day -- which is
// exactly why the choice had to be argued instead of measured. Three reasons to
// leave order out:
//
//   - The drift the pin exists for is a field that is absent, added, or
//     retyped. A field that merely moved is still documented, still the right
//     type, and still findable by the reader who went looking for it.
//   - A reorder is not the API break it looks like. `go vet`'s composites check
//     already tells callers not to write an unkeyed literal of another package's
//     struct, so nothing supported depends on the position of a field here.
//   - Section 4 is a document first. Order-sensitivity would let this test veto
//     a rewrite that grouped a struct's fields for a reader, and the header of
//     this file says what happens to a test that blocks a legitimate wording
//     change: it gets deleted the first time it does so.
//
// The honest cost: a package that reorders its fields drifts from the order
// section 4 shows, and nothing here says so.
func TestExportedStructFieldsIgnoresFieldOrder(t *testing.T) {
	const declared = `package p
type T struct {
	A int
	B string
}
`
	const reordered = `package p
type T struct {
	B string
	A int
}
`
	a := exportedStructFields(t, "declared.go", declared)["T"]
	b := exportedStructFields(t, "reordered.go", reordered)["T"]
	if !maps.Equal(a, b) {
		t.Errorf("a reordered struct compared unequal (%v vs %v); field order is deliberately "+
			"not pinned, so this is a behaviour change and not a failure to fix", a, b)
	}
}

// TestExportedStructFieldsSkipsWhatIsNotPublicAPI keeps the pin from demanding
// that section 4 document things section 4 is not about. An unexported field or
// an unexported type is not public API, and section 4 has never listed one.
//
// The embedded cases are here because an embedded field carries no name of its
// own: skipping ast.Field entries with no Names -- the obvious reading of the
// AST -- would silently exempt every embedded field from the whole pin.
func TestExportedStructFieldsSkipsWhatIsNotPublicAPI(t *testing.T) {
	const src = `package p
type Exported struct {
	Public   int
	private  string
	Embedded
	*Pointer
	pkg.Qualified
	hidden
}
type unexported struct {
	Public int
}
`
	got := exportedStructFields(t, "skip.go", src)
	want := map[string]string{
		"Public":    "int",
		"Embedded":  "Embedded",
		"Pointer":   "*Pointer",
		"Qualified": "pkg.Qualified",
	}
	if !maps.Equal(got["Exported"], want) {
		t.Errorf(`got["Exported"] = %v, want %v`, got["Exported"], want)
	}
	if _, ok := got["unexported"]; ok {
		t.Errorf("an unexported type was recorded: %v", got)
	}
}

// TestDesignSpecPublicAPIBlockMatchesStructFields is byb-8u9.
//
// TestDesignSpecPublicAPIBlockMatchesThePackage compares two sets of top-level
// declarations, so it catches a type that appears or disappears and misses
// every change INSIDE one -- which is the change the API actually makes most
// often. ImageRef.ObjNr was exported by the package and absent from the spec for
// a whole release under a green test, and ObjNr is not incidental: it is the
// handle ReplaceImages takes, and the signal that tells a caller to re-encode a
// shared raster once rather than once per page. A reader who went to section 4
// to find that handle did not find it. byb-dng added ImageRef.Filter and did
// remember the spec line; nothing would have failed if it had not.
func TestDesignSpecPublicAPIBlockMatchesStructFields(t *testing.T) {
	block := specGoBlock(t, specSection(t, "## 4. Public API"))
	spec := exportedStructFields(t, "spec-section-4.go", block)
	pkg, pkgFiles := packageStructFields(t)
	decls := packageAPI(t)

	// The failure this test is here to catch is a field that quietly stops being
	// compared. A block that stopped being found, a fence that stopped being
	// parsed, or a walk that stopped descending would all report a clean pass
	// over zero fields -- so count what was actually compared, the same way
	// TestCorpusCountClaimsMatchTheCorpus counts the claims it found.
	specFieldCount := 0
	for _, fields := range spec {
		specFieldCount += len(fields)
	}
	if len(spec) < 10 || specFieldCount < 40 {
		t.Fatalf("the spec's section 4 block declares %d struct types holding %d exported "+
			"fields between them; it enumerates the whole public API and holds far more",
			len(spec), specFieldCount)
	}
	// Counting only the spec side leaves the worse failure open: a comparison
	// wired to the same source twice passes over 55 fields and 14 types while
	// proving nothing. Mutation-testing this file found exactly that -- pointing
	// pkg at the spec block left every count above healthy and the test green.
	// Nine package files declare an exported struct, and the spec block is one
	// synthetic file, so this separates them.
	if pkgFiles < 5 {
		t.Fatalf("exported structs were found in %d package files; nine declare one, and a "+
			"package side that collapsed to a single source is the shape of a test "+
			"comparing the spec against itself", pkgFiles)
	}

	compared := 0
	for name, specFields := range spec {
		pkgFields, ok := pkg[name]
		if !ok {
			// Either the package does not export the type at all, which
			// TestDesignSpecPublicAPIBlockMatchesThePackage reports, or it
			// exports it as something other than a struct, which nothing else
			// would.
			if _, exported := decls[name]; exported {
				t.Errorf("the design spec's section 4 block declares %s as a struct; the "+
					"package exports %s but not as a struct type", name, name)
			}
			continue
		}
		compared++
		for f, want := range specFields {
			got, ok := pkgFields[f]
			if !ok {
				t.Errorf("the design spec's section 4 block declares %s.%s %s, which the "+
					"package's %s does not have", name, f, want, name)
				continue
			}
			if got != want {
				t.Errorf("%s.%s is %s in the design spec's section 4 block and %s in the "+
					"package", name, f, want, got)
			}
		}
		for f, got := range pkgFields {
			if _, ok := specFields[f]; !ok {
				t.Errorf("%s.%s %s is exported by the package and missing from the design "+
					"spec's section 4 block", name, f, got)
			}
		}
	}
	// The mirror of the check above: a type the spec writes as something other
	// than a struct while the package writes it as one.
	specDecls := exportedDecls(t, "spec-section-4.go", block)
	for name := range pkg {
		if _, ok := spec[name]; ok {
			continue
		}
		if _, declared := specDecls[name]; declared {
			t.Errorf("the package declares %s as a struct; the design spec's section 4 block "+
				"declares %s but not as a struct type", name, name)
		}
	}
	t.Logf("design spec section 4 pins %d exported fields across %d struct types",
		specFieldCount, compared)
}

// --- the corpus counts -------------------------------------------------------

// TestCorpusReadableCountIsWhatTheCorpusDeclares measures the readable count
// rather than trusting it. internal/corpus cannot do this itself: "readable"
// means a PDF reader opens it, and internal/corpus deliberately holds no PDF
// parser, so the measurement belongs in the package that has one.
//
// It asserts the identity of the unreadable document, not just how many there
// are. "malformed" is a truncation, and a second document that silently stopped
// parsing would otherwise be absorbed by a compensating edit to ReadableCount.
func TestCorpusReadableCountIsWhatTheCorpusDeclares(t *testing.T) {
	var unreadable []string
	for _, d := range corpus.All() {
		if _, err := Inspect(bytes.NewReader(d.Data)); err != nil {
			unreadable = append(unreadable, d.Name)
		}
	}
	readable := len(corpus.All()) - len(unreadable)
	if readable != corpus.ReadableCount {
		t.Errorf("%d of the %d documents in the corpus open; corpus.ReadableCount says %d "+
			"(unreadable: %v)", readable, len(corpus.All()), corpus.ReadableCount, unreadable)
	}
	if want := []string{"malformed"}; !slices.Equal(unreadable, want) {
		t.Errorf("the documents that do not open are %v; the corpus carries exactly one "+
			"deliberately broken document, %v. A document that stopped parsing by accident "+
			"is a regression, not a new ReadableCount.", unreadable, want)
	}
}

// corpusCountClaims is the set of phrasings a corpus figure has to be written
// in, and what each one must equal.
//
// This is a convention, and the honest cost of it is that a figure written some
// other way is invisible here. That is a deliberate trade: the alternative --
// hunting for any number that happens to sit near the word "corpus" -- flags
// every unrelated measurement in the tree and gets deleted the first time it
// does. Two fixed phrasings that everything in the tree already uses cost one
// rewording per claim and cannot false-positive.
//
// The patterns are disjoint by construction: "readable" between the digits and
// "corpus documents" is what tells the two apart, so a claim matches exactly
// one row.
var corpusCountClaims = []struct {
	re   *regexp.Regexp
	want func() int
	what string
}{
	{regexp.MustCompile(`(\d+) corpus documents`), func() int { return corpus.Count },
		"documents in the corpus (corpus.Count)"},
	{regexp.MustCompile(`(\d+) readable corpus documents`), func() int { return corpus.ReadableCount },
		"readable documents in the corpus (corpus.ReadableCount)"},
	{regexp.MustCompile(`(\d+) readable documents`), func() int { return corpus.ReadableCount },
		"readable documents in the corpus (corpus.ReadableCount)"},
}

// TestCorpusCountClaimsMatchTheCorpus is byb-a20's second check, and the one
// that closes all three recorded drifts at once.
//
// The corpus count is quoted in the design spec's section 8 acceptance row and
// in doc comments across four Go files. It read 27 in every one of them through
// three successive documentation reconciliations, while internal/corpus grew
// past 30. The third reconciliation left the spec hedging that its own figure
// was stale while the code went on presenting the same figure as current -- the
// two disagreeing about whether they disagreed.
//
// Nothing here pins prose. It pins numbers that are already written down, to
// the corpus they are already about.
func TestCorpusCountClaimsMatchTheCorpus(t *testing.T) {
	// Skipped by name, everywhere in the tree:
	//   .git       not source
	//   plans      docs/superpowers/plans holds dated implementation records,
	//              including bd notes quoting the corpus size on the day they
	//              were written. Those are history and must not be rewritten.
	//   testdata/corpus  generated output (make corpus), gitignored.
	//   .claude    holds .claude/worktrees, the per-session git worktrees other
	//              sessions check out INSIDE this repo. Those are copies of the
	//              tree at some other commit; scanning them fails this test on
	//              figures that are correct on main. Same reason ci.yml scopes
	//              its gofmt gate to git ls-files.
	skipDir := map[string]bool{".git": true, "plans": true, ".claude": true}

	claims := 0
	files := map[string]bool{}
	walkErr := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] || path == "testdata/corpus" {
				return fs.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(path); ext != ".go" && ext != ".md" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(raw), "\n") {
			for _, c := range corpusCountClaims {
				for _, m := range c.re.FindAllStringSubmatch(line, -1) {
					claims++
					files[path] = true
					got, err := strconv.Atoi(m[1])
					if err != nil {
						t.Errorf("%s:%d: %q: %v", path, i+1, m[0], err)
						continue
					}
					if got != c.want() {
						t.Errorf("%s:%d says %q; there are %d %s. Update the figure here, "+
							"not the constant in internal/corpus.",
							path, i+1, m[0], c.want(), c.what)
					}
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walking the tree: %v", walkErr)
	}

	// Without these, a scanner that walked the wrong tree, matched nothing, or
	// had its patterns quietly reworded out of existence would report a clean
	// pass over zero claims -- which is exactly the shape of the drift it is
	// here to catch.
	if !files[designSpecPath] {
		t.Errorf("no corpus figure was found in %s; the design spec's section 8 acceptance "+
			"row quotes one, and this test exists because it went stale three times",
			designSpecPath)
	}
	goFiles := 0
	for f := range files {
		if strings.HasSuffix(f, ".go") {
			goFiles++
		}
	}
	if goFiles < 3 {
		t.Errorf("corpus figures were found in only %d Go files; the doc comments in "+
			"optimize.go, optimize_test.go, linearize_test.go and stamp_test.go all quote "+
			"one, and a fix that reaches only the spec is what byb-5e8 had to leave behind",
			goFiles)
	}
	t.Logf("%d corpus figures pinned across %d files", claims, len(files))
}
