package byblos

import (
	"bytes"
	"image"
	"os/exec"
	"strings"
	"testing"
)

// TestNoFormatRegisteringImageDecoderIsImported is byb-b3's structural half,
// and the reason it is a Go test rather than only a CI step is the same
// reason linearize_arch_test.go is: the CI allowlist runs in CI. This runs
// on every `make test`, on the machine where someone is about to add the
// import -- for that machine's GOOS/GOARCH/build tags. `go list`'s
// .Imports/.TestImports/.XTestImports only reflect files satisfying the
// CURRENT build constraints, so a forbidden import gated behind a build tag
// for a GOOS other than the local one is invisible here; CI (which runs on
// ubuntu) is the authoritative check for that case.
//
// What it protects: gate byb-1tj was discharged without re-measuring 5,659
// documents, on the argument that B3 is ENCODE-side only and cannot change
// what the classification path decodes. golang.org/x/image/bmp, /tiff and
// /webp each call image.RegisterFormat in init() (bmp/reader.go:266,
// tiff/reader.go:787-788, webp/decode.go:281). Importing any of them from
// anywhere in this module widens image.Decode in extract.go, moves pages
// from DIVERTED to EXTRACTED, and retires the gate's evidence -- silently,
// with no test failing and no behaviour visibly changing except on
// documents this suite does not contain.
//
// It is an ALLOWLIST of x/image subpackages, not a denylist of the three
// that register. A denylist is correct only until x/image ships a fourth
// format package; an allowlist fails closed and makes adding one a decision
// someone has to write down.
//
// TestImports and XTestImports are covered for the reason arch_test.go
// gives: a `package byblos_test` file would otherwise slip past both this
// guard and the CI allowlist, and a test helper that reaches for a TIFF
// decoder to build a fixture links the same init() that production code
// would.
//
// This test is necessary and NOT sufficient -- see
// TestImageDecodeAcceptsOnlyTheFormatsTheGateWasMeasuredWith below.
func TestNoFormatRegisteringImageDecoderIsImported(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}
	out, err := exec.Command(goBin, "list",
		"-f", `{{.ImportPath}} {{join .Imports ","}} {{join .TestImports ","}} {{join .XTestImports ","}}`,
		"./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	// ccitt is a filter, not a format: it registers nothing and
	// internal/jbig2/fixtures_test.go already uses it. draw is the resampler
	// byb-b3 needs; `go list -deps golang.org/x/image/draw` is the standard
	// library plus golang.org/x/image/math/f64, and neither registers.
	// font/sfnt and math/fixed are the TrueType parser stage 4c (byb-8b9.3)
	// needs: checked by grep for image.RegisterFormat over font/ and math/ in
	// x/image v0.41.0 -- zero hits, and sfnt's transitive deps beyond the
	// stdlib are x/image/font, math/fixed and x/text/encoding/charmap, none
	// of which register either.
	allowed := map[string]bool{
		"golang.org/x/image/ccitt":      true,
		"golang.org/x/image/draw":       true,
		"golang.org/x/image/font/sfnt":  true,
		"golang.org/x/image/math/fixed": true,
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, rest, _ := strings.Cut(line, " ")
		for _, group := range strings.Fields(rest) {
			for _, imp := range strings.Split(group, ",") {
				if !strings.HasPrefix(imp, "golang.org/x/image") || allowed[imp] {
					continue
				}
				t.Errorf("package %s imports %s. Only %v are permitted: an "+
					"x/image package that calls image.RegisterFormat in init() "+
					"widens image.Decode on the classification path and retires "+
					"gate byb-1tj's evidence (see this test's doc comment). If "+
					"the new import genuinely registers nothing, add it here and "+
					"to the ci.yml allowlist, and say which you checked.",
					pkg, imp, allowed)
			}
		}
	}
}

// TestImageDecodeAcceptsOnlyTheFormatsTheGateWasMeasuredWith pins the actual
// invariant byb-1tj rests on, which the import guard above only
// approximates: what image.Decode accepts is a property of the TRANSITIVE
// graph, and byblos imports none of the packages that put the current set
// there. golang.org/x/image/webp is already linked and already registered --
// via internal/pdfdoc -> pdfcpu/pkg/pdfcpu/model -> golang.org/x/image/webp
// -- so a pdfcpu upgrade that picks up x/image/bmp would widen the
// classification path with no change to any byblos import list and no
// failure from the test above.
//
// A failure here is not necessarily a bug -- it is a demand that whoever
// widened the set re-run byb-1tj's measurement, or say why the new format
// cannot appear in a PDF image XObject.
func TestImageDecodeAcceptsOnlyTheFormatsTheGateWasMeasuredWith(t *testing.T) {
	for _, tc := range []struct{ name, magic, want string }{
		{"jpeg", "\xff\xd8\xff\xe0", "jpeg"},
		{"png", "\x89PNG\r\n\x1a\n", "png"},
		{"tiff-le", "II*\x00", "tiff"},
		{"tiff-be", "MM\x00*", "tiff"},
		{"webp", "RIFF\x00\x00\x00\x00WEBPVP8 ", "webp"},
		{"gif", "GIF89a", ""},
		{"bmp", "BM\x00\x00\x00\x00", ""},
	} {
		buf := append([]byte(tc.magic), make([]byte, 64)...)
		_, format, _ := image.DecodeConfig(bytes.NewReader(buf))
		if format != tc.want {
			t.Errorf("image.DecodeConfig on %s magic reports format %q; want %q",
				tc.name, format, tc.want)
		}
	}
}
