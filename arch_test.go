package byblos

import (
	"os/exec"
	"strings"
	"testing"
)

// Design spec section 3: pdfcpu is wrapped behind Byblos's own interfaces so
// that replacing it later is a swap rather than a rewrite. That is only true
// while exactly one package imports it.
func TestOnlyPdfdocImportsPdfcpu(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	// XTestImports covers a `package byblos_test` file, which would otherwise
	// slip past both this guard and the CI allowlist.
	out, err := exec.Command(goBin, "list",
		"-f", `{{.ImportPath}} {{join .Imports ","}} {{join .TestImports ","}} {{join .XTestImports ","}}`,
		"./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	const allowed = "github.com/dobbo-ca/byblos/internal/pdfdoc"
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, imports, _ := strings.Cut(line, " ")
		if pkg == allowed {
			continue
		}
		if strings.Contains(imports, "github.com/pdfcpu/pdfcpu") {
			t.Errorf("package %s imports pdfcpu; only %s may", pkg, allowed)
		}
	}
}
