package byblos

import (
	"os/exec"
	"strings"
	"testing"
)

// TestLinearizePackageDependsOnlyOnTheStandardLibrary is the structural half of
// byb-1y7's design decision, and it exists because the alternative is so easy
// to reach for.
//
// arch_test.go already forbids any package but internal/pdfdoc from importing
// pdfcpu, on the grounds that pdfcpu must stay replaceable. That leaves an
// implementer two ways to write Annex F: put the linearizer inside
// internal/pdfdoc, where the bit-packed hint tables can only be tested by
// writing a PDF and reading it back, or put it in a package with no PDF parser
// at all and hand it a neutral representation. The design picked the second.
// Nothing in arch_test.go distinguishes them, because a linearizer living
// inside internal/pdfdoc is allowed to import pdfcpu.
//
// This test is what makes the choice stick: the moment internal/linearize needs
// a pdfcpu type -- to peek at a dictionary, to resolve one more reference -- the
// import appears here and fails, rather than quietly dissolving the seam that
// makes the hint tables unit-testable against fixed byte vectors
// (internal/linearize/hints_test.go).
//
// It checks TEST imports too. A test helper that reaches for pdfcpu to build a
// fixture defeats the point just as thoroughly as production code would, and
// arch_test.go's own comment says XTestImports is covered for exactly that
// reason.
func TestLinearizePackageDependsOnlyOnTheStandardLibrary(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	const pkg = "github.com/dobbo-ca/byblos/internal/linearize"

	out, err := exec.Command(goBin, "list",
		"-f", `{{.ImportPath}} {{.Standard}} {{join .Imports ","}} {{join .TestImports ","}} {{join .XTestImports ","}}`,
		pkg).Output()
	if err != nil {
		t.Fatalf("go list %s: %v", pkg, err)
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 2 {
		t.Fatalf("go list returned %q; cannot read the import lists", out)
	}
	var imports []string
	for _, group := range fields[2:] {
		imports = append(imports, strings.Split(group, ",")...)
	}

	// Resolve each import through the toolchain rather than guessing from the
	// path shape: "go list -f {{.Standard}}" is the toolchain's own answer to
	// "is this the standard library", and a heuristic on dots in the first path
	// element is not.
	for _, imp := range imports {
		if imp == "" || imp == pkg {
			continue
		}
		std, err := exec.Command(goBin, "list", "-f", "{{.Standard}}", imp).Output()
		if err != nil {
			t.Errorf("%s imports %s, which does not resolve: %v", pkg, imp, err)
			continue
		}
		if strings.TrimSpace(string(std)) != "true" {
			t.Errorf("%s imports %s, which is not in the standard library; Annex F needs "+
				"no PDF parser, and a dependency here is the first step back toward "+
				"testing the hint tables through a PDF round trip", pkg, imp)
		}
	}
}
