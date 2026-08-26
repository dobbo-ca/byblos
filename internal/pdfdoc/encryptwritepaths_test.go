package pdfdoc_test

// byb-cvx's census (cmd/byblos-encrypt-census) found the population an
// /Encrypt refusal at pdfdoc.Open would break -- 198 documents in the pinned
// sample that open with NO password (owner-password-only, or an empty user
// password) and read correctly today -- and found zero documents that need a
// refusal to stop a silent strip: every re-serializing write path this test
// enumerates PRESERVES /Encrypt.
//
// MECHANISM: pdfcpu re-encrypts a stream or string on write whenever
// ctx.XRefTable.EncKey != nil (pkg/pdfcpu/writeObjects.go:551-556 for
// streams, :425-426 for strings, v0.15.0), and EncKey stays set on the
// context after api.ReadContext/pdfdoc.Open successfully opens an
// owner-password-only document -- no password was needed to OPEN it, but the
// key was still derived and stays on the context. Every write path below
// reopens the source once and writes from that SAME context (directly, or by
// handing it to pdfcpu's writer through Doc.Write), so every one of them
// inherits the re-encryption for free without asking for it.
//
// THE DANGEROUS SHAPE is a write path that does NOT re-serialize the
// context Open produced, but instead builds a FRESH *model.Context/XRefTable
// (EncKey == nil) and copies object values into it that Open already
// decrypted in memory. pdfcpu has no way to know those plaintext values came
// from an encrypted source, so it writes them out as plaintext with a nil
// error -- reproduced directly with pdfcpu's own api.Trim: 1758 bytes, nil
// error, /Encrypt ABSENT (a source that, unmodified, is 2882 bytes with
// /Encrypt present). internal/pdfdoc.BuildFromPages has exactly that shape --
// buildContext (buildpages.go) assembles a fresh context and migrates each
// object across one at a time -- which is why it is the one seam below that
// refuses an encrypted source outright (openSources, buildpages.go:210-228)
// instead of relying on pdfcpu's re-encryption. A FUTURE write path built the
// buildContext way needs the same refusal; one built the re-serialize way
// (open, mutate the SAME context, write THAT context back out) inherits the
// re-encryption and needs none. Which kind a new path is is the question a
// reader must answer before shipping it -- this test is what makes shipping
// it without an answer visible, via the enumeration below.
//
// FIXTURE TRAP: api.Encrypt refuses a UserPW with no OwnerPW, so an
// owner-password-only fixture must set BOTH -- OwnerPW non-empty and UserPW
// "". A document with a non-empty UserPW is the WRONG fixture for a write
// path: pdfdoc.Open never reaches one, because pdfcpu cannot read it without
// the password, so none of the paths below would even get a chance to
// answer. Bucket 2 of byb-cvx's census (owner-password-only or an empty user
// password) is the population this guards, and this fixture is exactly that
// shape.
import (
	"bytes"
	"compress/zlib"
	"image"
	"testing"

	"github.com/dobbo-ca/byblos"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// plaintextFixture is a one-page, one-image PDF built entirely in Go (no
// external fixture file needed): BuildPDF is the only byblos entry point that
// can hand api.Encrypt something to encrypt.
func plaintextFixture(t *testing.T) []byte {
	t.Helper()
	img := byblos.EncodedImage{
		Width:      4,
		Height:     4,
		BPC:        8,
		ColorSpace: byblos.ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       flateBytes(t, []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}),
	}
	var buf bytes.Buffer
	if err := byblos.BuildPDF(&buf, []byblos.BuildPage{{Image: img, DPI: 72}}); err != nil {
		t.Fatalf("BuildPDF: %v", err)
	}
	return buf.Bytes()
}

// ownerPasswordOnlyFixture encrypts plaintextFixture with an owner password
// and NO user password -- the FIXTURE TRAP above. AES-128 matches two of the
// census's 198 (the other 196 are RC4).
func ownerPasswordOnlyFixture(t *testing.T) []byte {
	t.Helper()
	conf := model.NewAESConfiguration("", "o", 128)
	var out bytes.Buffer
	if err := api.Encrypt(bytes.NewReader(plaintextFixture(t)), &out, conf); err != nil {
		t.Fatalf("api.Encrypt: %v", err)
	}
	return out.Bytes()
}

// hasEncryptDict re-reads data independently of every path under test and
// reports whether its xref table carries an /Encrypt dictionary. pdfdoc.Doc
// exposes no accessor for this (byb-cvx's census hit the same wall), so this
// is the same second-read api.ReadContext the census tool uses.
func hasEncryptDict(t *testing.T, data []byte) bool {
	t.Helper()
	ctx, err := api.ReadContext(bytes.NewReader(data), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatalf("re-read output: %v", err)
	}
	return ctx.XRefTable.Encrypt != nil
}

func assertPreserves(t *testing.T, name string, out []byte, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: refused an owner-password-only source (%v); it must either preserve /Encrypt or be added to the refuses list", name, err)
	}
	if !hasEncryptDict(t, out) {
		t.Fatalf("%s: /Encrypt is ABSENT from the output -- this is the silent strip byb-cvx's buildContext shape produces; the source's encryption did not survive", name)
	}
}

func assertRefuses(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: succeeded on an owner-password-only source with no /Encrypt check; either it must refuse (like BuildFromPages) or preserve /Encrypt and move to the preserves list", name)
	}
}

// TestEveryWritePathAnswersForEncryptedSource is byb-cvx's deliverable: an
// enumeration of every function in this module that serializes a PDF read
// from an existing source, each one asserted to either refuse an
// owner-password-only source or preserve its /Encrypt dictionary.
//
// These subtests are NOT independent guarantees of one mechanism each --
// several share the exact same underlying seam and rise or fall together:
// ReplaceImages, StampTextLayer and pdfdoc.Doc.Write all terminate in
// pdfdoc's own doc.Write (write.go); WriteProvenance is a pure delegation to
// pdfdoc.WriteProperties; and byblos.Optimize's default-options case wraps
// internal/pdfdoc.Optimize (tested directly, its own row below) behind a
// size-fallback that on a small fixture may just hand the input straight
// back rather than exercise the rewrite. Kept as separate subtests anyway
// because each is a real, independently reachable entry point from outside
// this package -- a future change to any one of them, even one that leaves
// the others' source untouched, should still be caught at its own call site.
//
// The enumeration was built by reading every non-test .go file for an
// io.Writer parameter (root package and internal/pdfdoc), then tracing each
// hit to its underlying serialization call. Two hits are deliberately absent
// from the table below:
//
//   - BuildPDF (byblos, build.go) and internal/pdfbuild's writer: neither
//     reads an existing PDF at all -- BuildPDF's only input is raw image
//     bytes -- so there is no source whose /Encrypt could be lost. It is the
//     fixture builder above, not a case here.
//   - Doc.Validate (internal/pdfdoc/write.go) takes no io.Writer; it reports
//     structural validity and writes nothing.
//   - internal/pdfdoc.BuildFromPages and internal/linearize's writer are
//     exercised THROUGH byblos.BuildFromPages and pdfdoc.Linearize below --
//     both are thin wrappers with no logic between the wrapper and the
//     guarded/re-serializing call, so testing the reachable entry point
//     covers the internal one too.
func TestEveryWritePathAnswersForEncryptedSource(t *testing.T) {
	enc := ownerPasswordOnlyFixture(t)
	if !hasEncryptDict(t, enc) {
		t.Fatal("fixture setup: encrypted source has no /Encrypt dictionary of its own")
	}

	t.Run("byblos.ReplaceImages", func(t *testing.T) {
		infos, err := byblos.Inspect(bytes.NewReader(enc))
		if err != nil || len(infos) == 0 || len(infos[0].Images) == 0 {
			t.Fatalf("fixture has no substitutable image: infos=%v err=%v", infos, err)
		}
		objNr := infos[0].Images[0].ObjNr
		var out bytes.Buffer
		err = byblos.ReplaceImages(&out, bytes.NewReader(enc), map[int]byblos.EncodedImage{
			objNr: {
				Width: 2, Height: 2, BPC: 8,
				ColorSpace: byblos.ColorSpace{Name: "DeviceGray"},
				Filter:     "FlateDecode",
				Data:       flateBytes(t, []byte{0, 1, 2, 3}),
			},
		})
		assertPreserves(t, "byblos.ReplaceImages", out.Bytes(), err)
	})

	t.Run("byblos.Optimize", func(t *testing.T) {
		// byblos.Optimize's default options pick whichever of {rewritten
		// candidate, original input} is not larger (optimize.go); on this
		// tiny synthetic fixture the candidate never beats the input, so this
		// subtest exercises the "hand the input straight back" branch, not
		// the rewrite -- preservation is trivial there because the bytes ARE
		// the (already-verified-encrypted) input. internal/pdfdoc.Optimize
		// below is the actual rewrite byblos.Optimize wraps, tested directly
		// so the rewrite branch is exercised unconditionally.
		var out bytes.Buffer
		err := byblos.Optimize(&out, bytes.NewReader(enc), byblos.OptimizeOptions{})
		assertPreserves(t, "byblos.Optimize", out.Bytes(), err)
	})

	t.Run("internal/pdfdoc.Optimize", func(t *testing.T) {
		var out bytes.Buffer
		err := pdfdoc.Optimize(bytes.NewReader(enc), &out)
		assertPreserves(t, "internal/pdfdoc.Optimize", out.Bytes(), err)
	})

	t.Run("byblos.Optimize{Linearize:true}", func(t *testing.T) {
		// Linearize:true routes through pdfdoc.Linearize (optimize.go), which
		// refuses an encrypted source outright -- the SAME refusal as the
		// pdfdoc.Linearize row below, reached through Optimize's own option.
		// byblos.Optimize is NOT unconditionally a "preserves" seam: which
		// column it belongs in depends on opts.
		var out bytes.Buffer
		err := byblos.Optimize(&out, bytes.NewReader(enc), byblos.OptimizeOptions{Linearize: true})
		assertRefuses(t, "byblos.Optimize{Linearize:true}", err)
	})

	t.Run("byblos.StampTextLayer", func(t *testing.T) {
		// A non-empty TextLayer, not TextLayer{}: an empty one skips the
		// AddFontResource/AppendContent loop entirely (stamp.go), which would
		// leave this indistinguishable from the pdfdoc.Doc.Write case below.
		tl := byblos.TextLayer{Pages: [][]byblos.PositionedWord{
			{{Text: "Scanned", Bounds: image.Rect(72, 700, 149, 712)}},
		}}
		var out bytes.Buffer
		err := byblos.StampTextLayer(&out, bytes.NewReader(enc), tl)
		assertPreserves(t, "byblos.StampTextLayer", out.Bytes(), err)
	})

	t.Run("byblos.WriteProvenance", func(t *testing.T) {
		var out bytes.Buffer
		err := byblos.WriteProvenance(bytes.NewReader(enc), &out, byblos.Provenance{})
		assertPreserves(t, "byblos.WriteProvenance", out.Bytes(), err)
	})

	t.Run("pdfdoc.WriteProperties", func(t *testing.T) {
		var out bytes.Buffer
		err := pdfdoc.WriteProperties(bytes.NewReader(enc), &out, map[string]string{"x": "y"})
		assertPreserves(t, "pdfdoc.WriteProperties", out.Bytes(), err)
	})

	t.Run("pdfdoc.Doc.Write", func(t *testing.T) {
		d, err := pdfdoc.Open(bytes.NewReader(enc))
		if err != nil {
			t.Fatalf("pdfdoc.Open: %v", err)
		}
		var out bytes.Buffer
		err = d.Write(&out)
		assertPreserves(t, "pdfdoc.Doc.Write", out.Bytes(), err)
	})

	t.Run("byblos.BuildPDF", func(t *testing.T) {
		t.Skip("N/A: BuildPDF takes raw image bytes, not an existing PDF -- there is no source /Encrypt to lose")
	})

	t.Run("byblos.BuildFromPages", func(t *testing.T) {
		var out bytes.Buffer
		err := byblos.BuildFromPages(&out, []byblos.PageSource{{Source: bytes.NewReader(enc), Page: 1}})
		assertRefuses(t, "byblos.BuildFromPages", err)
	})

	t.Run("pdfdoc.Linearize", func(t *testing.T) {
		var out bytes.Buffer
		err := pdfdoc.Linearize(bytes.NewReader(enc), &out)
		assertRefuses(t, "pdfdoc.Linearize", err)
	})
}

func flateBytes(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	return buf.Bytes()
}
