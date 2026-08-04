package byblos

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
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
						if s.Assign.IsValid() {
							kind = "alias"
						}
						out[s.Name.Name] = kind
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

// packageAPI is every exported top-level declaration in the non-test files of
// package byblos, read from the source rather than from a running toolchain.
func packageAPI(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	out := map[string]string{}
	files := 0
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		src, err := os.ReadFile(n)
		if err != nil {
			t.Fatalf("reading %s: %v", n, err)
		}
		files++
		for k, v := range exportedDecls(t, n, string(src)) {
			out[k] = v
		}
	}
	// Without this a mis-globbed directory read would compare an empty package
	// against an empty spec block and pass.
	if files < 10 {
		t.Fatalf("read %d non-test .go files in the package directory; the package has many more", files)
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
	//   corpus     testdata/corpus is generated output (make corpus), gitignored.
	skipDir := map[string]bool{".git": true, "plans": true, "corpus": true}

	claims := 0
	files := map[string]bool{}
	walkErr := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
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
