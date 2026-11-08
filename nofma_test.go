package byblos

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// fusedOp matches an arm64 fused multiply-add/subtract instruction in
// `go build -gcflags=-S` output and captures the source file and line.
var fusedOp = regexp.MustCompile(`\((/[^)]*\.go):(\d+)\)\s+F(?:N?M(?:ADD|SUB))[DS]\b`)

// Design spec section 3.3 (No-FMA guard): Go fuses x*y+z on arm64 unless the
// product is explicitly converted, so float code outside cmd/ must round
// every product, or its arm64 output differs from amd64. The cross-compile
// runs nothing. Fused ops in pdfcpu are third-party and cannot be rewritten:
// they are listed, version-pinned, in testdata/fused_external.txt, so a bump
// that adds fused code fails until someone reviews it. The same goes for
// stdlib files (keyed "std/<path>"), restricted to packages in go list -deps
// of the golden-producing packages.
func TestNoFusedOpsInReferences(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	list, err := os.ReadFile(filepath.Join("testdata", "fused_external.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(list), "\n") {
		if p, _, _ := strings.Cut(l, "\t"); p != "" {
			allowed[p] = true
		}
	}

	env := append(os.Environ(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0")

	// Only packages the golden-producing code links can change its bytes:
	// the root package and internal/..., with their whole dependency tree
	// (stdlib and third-party included). cmd/ is out of scope.
	ls := exec.Command(goBin, "list", "-deps", "-f", "{{.Dir}}", ".", "./internal/...")
	ls.Env = env
	dirOut, err := ls.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	linked := map[string]bool{}
	for _, d := range strings.Fields(string(dirOut)) {
		linked[d] = true
	}
	goroot, err := exec.Command(goBin, "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	stdSrc := filepath.Join(strings.TrimSpace(string(goroot)), "src") + string(filepath.Separator)

	cmd := exec.Command(goBin, "build", "-a", "-gcflags=all=-S", "./...")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil && !fusedOp.Match(out) {
		t.Fatalf("cross-compile: %v\n%.2000s", err, out)
	}

	bad := map[string]bool{}
	for _, m := range fusedOp.FindAllStringSubmatch(string(out), -1) {
		file, line := m[1], m[2]
		if !linked[filepath.Dir(file)] {
			continue
		}
		if rel, ok := strings.CutPrefix(file, root+string(filepath.Separator)); ok {
			if !strings.HasPrefix(rel, "cmd"+string(filepath.Separator)) {
				bad[rel+":"+line] = true
			}
		} else if std, ok := strings.CutPrefix(file, stdSrc); ok {
			if !allowed["std/"+std] {
				bad["std/"+std+":"+line+" (add the file to testdata/fused_external.txt with a reason)"] = true
			}
		} else if _, mod, ok := strings.Cut(file, "/pkg/mod/"); ok {
			if !allowed[mod] {
				bad[mod+":"+line+" (add the file to testdata/fused_external.txt with a reason)"] = true
			}
		} else {
			bad[file+":"+line] = true
		}
	}
	var sites []string
	for s := range bad {
		sites = append(sites, s)
	}
	sort.Strings(sites)
	if len(sites) > 0 {
		t.Errorf("fused multiply-add on arm64 (wrap each product in float64(...)):\n%s", strings.Join(sites, "\n"))
	}
}
