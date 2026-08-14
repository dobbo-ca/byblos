package pdfdoc

// Step 1 of the straighten design (docs/superpowers/specs/2026-08-14-straighten-design.md
// section 5): WrapContent and its q/Q guard.
//
// These tests build the four /Contents shapes directly on an opened *doc,
// using pdfcpu types the same way TestAddFontResourceEmbedsFontFile2Hermetically
// already does in text_test.go -- there is no corpus fixture for the
// indirect-ref-to-array shape, and building one here keeps the test able to
// see exactly which object /Contents resolves to before and after.

import (
	"bytes"
	"errors"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// pageDict returns page n's own dictionary on d, for tests that need to
// reshape /Contents directly.
func pageDict(t *testing.T, d Doc, n int) (types.Dict, *model.XRefTable) {
	t.Helper()
	dd := d.(*doc)
	xt := dd.ctx.XRefTable
	pd, _, _, err := xt.PageDict(n, false)
	if err != nil {
		t.Fatalf("PageDict(%d): %v", n, err)
	}
	if pd == nil {
		t.Fatalf("page %d has no dictionary", n)
	}
	return pd, xt
}

// newStream writes ops as a new, encoded, indirect content stream object and
// returns its reference -- the same construction WrapContent itself uses,
// reimplemented here so the tests can set up /Contents shapes WrapContent
// does not itself produce.
func newStream(t *testing.T, xt *model.XRefTable, ops []byte) types.IndirectRef {
	t.Helper()
	sd, err := xt.NewStreamDictForBuf(ops)
	if err != nil {
		t.Fatalf("NewStreamDictForBuf: %v", err)
	}
	if err := sd.Encode(); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	ref, err := xt.IndRefForNewObject(*sd)
	if err != nil {
		t.Fatalf("IndRefForNewObject: %v", err)
	}
	return *ref
}

// decodedStream dereferences and decodes an indirect stream reference,
// returning its content bytes.
func decodedStream(t *testing.T, xt *model.XRefTable, ref types.IndirectRef) []byte {
	t.Helper()
	sd, _, err := xt.DereferenceStreamDict(ref)
	if err != nil || sd == nil {
		t.Fatalf("dereference stream %v: %v", ref, err)
	}
	if err := sd.Decode(); err != nil {
		t.Fatalf("decode stream %v: %v", ref, err)
	}
	return sd.Content
}

// setContentsArray points page n's /Contents directly at a types.Array of
// the given streams -- the "array" shape from the spec's table.
func setContentsArray(t *testing.T, d Doc, n int, refs ...types.IndirectRef) {
	t.Helper()
	pd, _ := pageDict(t, d, n)
	arr := make(types.Array, len(refs))
	for i, r := range refs {
		arr[i] = r
	}
	pd["Contents"] = arr
}

// setContentsIndirectStream points /Contents at a single indirect reference
// to a stream -- the "IndirectRef to a stream" shape.
func setContentsIndirectStream(t *testing.T, d Doc, n int, ref types.IndirectRef) {
	t.Helper()
	pd, _ := pageDict(t, d, n)
	pd["Contents"] = ref
}

// setContentsIndirectArray points /Contents at an indirect reference to an
// ARRAY of streams -- the "IndirectRef to an array" shape, the one the spec
// calls a known trap.
func setContentsIndirectArray(t *testing.T, d Doc, n int, refs ...types.IndirectRef) types.IndirectRef {
	t.Helper()
	pd, xt := pageDict(t, d, n)
	arr := make(types.Array, len(refs))
	for i, r := range refs {
		arr[i] = r
	}
	arrRef, err := xt.IndRefForNewObject(arr)
	if err != nil {
		t.Fatalf("IndRefForNewObject(array): %v", err)
	}
	pd["Contents"] = *arrRef
	return *arrRef
}

// clearContents removes /Contents from page n's dictionary entirely -- the
// "nil" shape.
func clearContents(t *testing.T, d Doc, n int) {
	t.Helper()
	pd, _ := pageDict(t, d, n)
	delete(pd, "Contents")
}

const beforeOps = "q 1 0 0 1 5 5 cm"
const afterOps = "Q"

// TestWrapContentWrapsAContentsArray covers all four /Contents shapes from
// the spec's table (design spec section 5), asserting the resulting shape
// AND that the existing stream(s) survive unwrapped and byte-identical.
func TestWrapContentWrapsAContentsArray(t *testing.T) {
	orig := []byte("q 306 0 0 396 0 0 cm /Im0 Do Q\n")

	t.Run("array", func(t *testing.T) {
		d := openCorpus(t, "scan")
		s1 := newStream(t, d.(*doc).ctx.XRefTable, orig)
		setContentsArray(t, d, 1, s1)

		if err := d.WrapContent(1, []byte(beforeOps), []byte(afterOps)); err != nil {
			t.Fatalf("WrapContent: %v", err)
		}

		pd, xt := pageDict(t, d, 1)
		arr, ok := pd["Contents"].(types.Array)
		if !ok {
			t.Fatalf("/Contents is %T, want types.Array", pd["Contents"])
		}
		if len(arr) != 3 {
			t.Fatalf("/Contents array has %d entries, want 3 (before, original, after)", len(arr))
		}
		if got := decodedStream(t, xt, arr[0].(types.IndirectRef)); string(got) != beforeOps {
			t.Errorf("first entry = %q, want %q", got, beforeOps)
		}
		if got := decodedStream(t, xt, arr[1].(types.IndirectRef)); !bytes.Equal(got, orig) {
			t.Errorf("middle entry = %q, want the original unchanged %q", got, orig)
		}
		if got := decodedStream(t, xt, arr[2].(types.IndirectRef)); string(got) != afterOps {
			t.Errorf("last entry = %q, want %q", got, afterOps)
		}
	})

	t.Run("indirect ref to a stream", func(t *testing.T) {
		d := openCorpus(t, "scan") // scan's own /Contents is already this shape
		pd, xt := pageDict(t, d, 1)
		origRef, ok := pd["Contents"].(types.IndirectRef)
		if !ok {
			t.Fatalf("precondition: scan's /Contents is %T, want types.IndirectRef", pd["Contents"])
		}
		origBytes := decodedStream(t, xt, origRef)

		if err := d.WrapContent(1, []byte(beforeOps), []byte(afterOps)); err != nil {
			t.Fatalf("WrapContent: %v", err)
		}

		pd, xt = pageDict(t, d, 1)
		arr, ok := pd["Contents"].(types.Array)
		if !ok {
			t.Fatalf("/Contents is %T, want types.Array", pd["Contents"])
		}
		if len(arr) != 3 {
			t.Fatalf("/Contents array has %d entries, want 3", len(arr))
		}
		if got := decodedStream(t, xt, arr[0].(types.IndirectRef)); string(got) != beforeOps {
			t.Errorf("first entry = %q, want %q", got, beforeOps)
		}
		if arr[1].(types.IndirectRef) != origRef {
			t.Errorf("middle entry = %v, want the untouched original reference %v", arr[1], origRef)
		}
		if got := decodedStream(t, xt, arr[1].(types.IndirectRef)); !bytes.Equal(got, origBytes) {
			t.Errorf("middle entry decoded = %q, want unchanged %q", got, origBytes)
		}
		if got := decodedStream(t, xt, arr[2].(types.IndirectRef)); string(got) != afterOps {
			t.Errorf("last entry = %q, want %q", got, afterOps)
		}
	})

	t.Run("indirect ref to an array", func(t *testing.T) {
		d := openCorpus(t, "scan")
		s1 := newStream(t, d.(*doc).ctx.XRefTable, orig)
		arrRef := setContentsIndirectArray(t, d, 1, s1)

		if err := d.WrapContent(1, []byte(beforeOps), []byte(afterOps)); err != nil {
			t.Fatalf("WrapContent: %v", err)
		}

		pd, xt := pageDict(t, d, 1)
		// /Contents must still be the SAME indirect reference to the array
		// object -- WrapContent extends the array in place rather than
		// wrapping it in a new outer array, which would point /Contents at
		// an array containing that array and silently discard the whole
		// page (spec section 5, "the indirect-ref-to-array case").
		gotRef, ok := pd["Contents"].(types.IndirectRef)
		if !ok || gotRef != arrRef {
			t.Fatalf("/Contents = %v (%T), want the untouched array reference %v", pd["Contents"], pd["Contents"], arrRef)
		}
		target, err := xt.Dereference(gotRef)
		if err != nil {
			t.Fatalf("dereference /Contents: %v", err)
		}
		arr, ok := target.(types.Array)
		if !ok {
			t.Fatalf("/Contents resolves to %T, not types.Array -- NOT an array-in-an-array, but also not the expected shape", target)
		}
		if len(arr) != 3 {
			t.Fatalf("array has %d entries, want 3", len(arr))
		}
		// Every entry must itself be a stream reference, never an array --
		// this is the explicit "not array-in-an-array" assertion.
		for i, e := range arr {
			ref, ok := e.(types.IndirectRef)
			if !ok {
				t.Fatalf("entry %d is %T, not types.IndirectRef (array-in-an-array?)", i, e)
			}
			if _, isArr := mustDeref(t, xt, ref).(types.Array); isArr {
				t.Fatalf("entry %d resolves to an array -- /Contents points at an array containing an array", i)
			}
		}
		if got := decodedStream(t, xt, arr[0].(types.IndirectRef)); string(got) != beforeOps {
			t.Errorf("first entry = %q, want %q", got, beforeOps)
		}
		if got := decodedStream(t, xt, arr[1].(types.IndirectRef)); !bytes.Equal(got, orig) {
			t.Errorf("middle entry = %q, want the original unchanged %q", got, orig)
		}
		if got := decodedStream(t, xt, arr[2].(types.IndirectRef)); string(got) != afterOps {
			t.Errorf("last entry = %q, want %q", got, afterOps)
		}
	})

	t.Run("nil", func(t *testing.T) {
		d := openCorpus(t, "scan")
		clearContents(t, d, 1)

		if err := d.WrapContent(1, []byte(beforeOps), []byte(afterOps)); err != nil {
			t.Fatalf("WrapContent: %v", err)
		}

		pd, xt := pageDict(t, d, 1)
		arr, ok := pd["Contents"].(types.Array)
		if !ok {
			t.Fatalf("/Contents is %T, want types.Array", pd["Contents"])
		}
		if len(arr) != 2 {
			t.Fatalf("/Contents array has %d entries, want 2 (before, after)", len(arr))
		}
		if got := decodedStream(t, xt, arr[0].(types.IndirectRef)); string(got) != beforeOps {
			t.Errorf("first entry = %q, want %q", got, beforeOps)
		}
		if got := decodedStream(t, xt, arr[1].(types.IndirectRef)); string(got) != afterOps {
			t.Errorf("last entry = %q, want %q", got, afterOps)
		}
	})
}

func mustDeref(t *testing.T, xt *model.XRefTable, ref types.IndirectRef) types.Object {
	t.Helper()
	o, err := xt.Dereference(ref)
	if err != nil {
		t.Fatalf("dereference %v: %v", ref, err)
	}
	return o
}

// A direct types.StreamDict in /Contents is malformed (ISO 32000-1 7.3.8.1)
// and WrapContent must refuse it, matching AppendContent's posture.
func TestWrapContentRefusesADirectContentsStream(t *testing.T) {
	d := openCorpus(t, "scan")
	pd, xt := pageDict(t, d, 1)
	sd, err := xt.NewStreamDictForBuf([]byte("q Q"))
	if err != nil {
		t.Fatalf("NewStreamDictForBuf: %v", err)
	}
	if err := sd.Encode(); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	pd["Contents"] = *sd

	if err := d.WrapContent(1, []byte(beforeOps), []byte(afterOps)); err == nil {
		t.Fatal("WrapContent on a direct /Contents stream: want an error, got nil")
	}
}

// TestWrapContentRefusesUnbalancedContent pins the q/Q guard in both
// directions, and confirms a balanced stream is unaffected.
func TestWrapContentRefusesUnbalancedContent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ops        string
		wantRefuse bool
	}{
		{"balanced", "q 1 0 0 1 0 0 cm Q", false},
		{"balanced nested", "q q Q Q", false},
		{"surplus Q", "q Q Q", true},
		{"surplus Q then balances numerically", "Q q", true}, // net zero depth, but a Q popped state this stream never pushed
		{"surplus q", "q q Q", true},
		{"no q/Q at all", "1 0 0 1 0 0 cm", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := openCorpus(t, "scan")
			s := newStream(t, d.(*doc).ctx.XRefTable, []byte(tc.ops))
			setContentsIndirectStream(t, d, 1, s)

			err := d.WrapContent(1, []byte(beforeOps), []byte(afterOps))
			if tc.wantRefuse {
				if err == nil {
					t.Fatalf("WrapContent on %q: want an error, got nil", tc.ops)
				}
				if !errors.Is(err, ErrUnbalancedContent) {
					t.Fatalf("WrapContent on %q: err = %v, want ErrUnbalancedContent", tc.ops, err)
				}
			} else if err != nil {
				t.Fatalf("WrapContent on %q: want no error, got %v", tc.ops, err)
			}
		})
	}
}

// TestWrapContentRefusesWhenContentFailsToLex pins that a content stream the
// lexer cannot fully parse is REFUSED rather than silently accepted.
// contentQDepth (text.go) used to return on the first lexer error exactly as
// it does on a clean io.EOF, reporting whatever depth it had read so far as
// though the stream ended there -- so a genuinely malformed stream (here, one
// starting with a stray ')' that internal/content's lexer cannot tokenize at
// all) read as depth 0, balanced, and WrapContent wrapped it. A wrapper that
// only half-applies is worse than a refusal (text.go's own ErrUnbalancedContent
// doc comment).
func TestWrapContentRefusesWhenContentFailsToLex(t *testing.T) {
	d := openCorpus(t, "scan")
	// A stray ')' is not valid content-stream syntax (lexer.go:124) and the
	// lexer reports an error on the very first token, before ever seeing
	// whether the rest of the stream is balanced.
	ops := []byte(") Q\n612 0 0 792 0 0 cm /Im0 Do\n")
	s := newStream(t, d.(*doc).ctx.XRefTable, ops)
	setContentsIndirectStream(t, d, 1, s)

	if err := d.WrapContent(1, []byte(beforeOps), []byte(afterOps)); err == nil {
		t.Fatal("WrapContent on unparseable content: want an error, got nil")
	}
}

// api.Validate must accept every shape WrapContent produces.
func TestWrapContentValidates(t *testing.T) {
	for _, shape := range []string{"array", "indirect stream", "indirect array", "nil"} {
		t.Run(shape, func(t *testing.T) {
			d := openCorpus(t, "scan")
			orig := []byte("q 306 0 0 396 0 0 cm /Im0 Do Q\n")
			switch shape {
			case "array":
				s := newStream(t, d.(*doc).ctx.XRefTable, orig)
				setContentsArray(t, d, 1, s)
			case "indirect stream":
				// scan's own shape; nothing to do.
			case "indirect array":
				s := newStream(t, d.(*doc).ctx.XRefTable, orig)
				setContentsIndirectArray(t, d, 1, s)
			case "nil":
				clearContents(t, d, 1)
			}

			if err := d.WrapContent(1, []byte(beforeOps), []byte(afterOps)); err != nil {
				t.Fatalf("WrapContent: %v", err)
			}
			out := writeDoc(t, d)

			relaxed := model.NewDefaultConfiguration()
			relaxed.ValidationMode = model.ValidationRelaxed
			if err := api.Validate(bytes.NewReader(out), relaxed); err != nil {
				t.Errorf("api.Validate(relaxed) = %v, want nil", err)
			}
		})
	}
}
