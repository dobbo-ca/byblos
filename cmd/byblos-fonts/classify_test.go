package main

import (
	"bytes"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// ctxForDeref returns a context used only as a dereferencer. Every dict in
// these tests is direct, so no xref lookup happens; the context is needed
// because classify takes one, not because the fixtures live in it.
func ctxForDeref(t *testing.T) *model.Context {
	t.Helper()
	data, ok := corpus.ByName("born-digital")
	if !ok {
		t.Fatal("corpus is missing born-digital")
	}
	ctx, err := api.ReadContext(bytes.NewReader(data), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatalf("reading a corpus document for a dereferencer: %v", err)
	}
	return ctx
}

func name(s string) *types.Name { n := types.Name(s); return &n }

// TestClassifyPrecedence pins section 4.1's ordering. The order is not
// cosmetic: it decides the ambiguous cases, and the published columns depend on
// it. A symbolic font that ALSO carries /Differences counts as C, not E,
// because the /Differences test runs first — get that backwards and dc's C
// column moves.
func TestClassifyPrecedence(t *testing.T) {
	ctx := ctxForDeref(t)

	symbolic := types.Dict{"Flags": types.Integer(4)}       // bit 3 set, bit 6 clear
	nonsymbolic := types.Dict{"Flags": types.Integer(32)}   // bit 6 set
	both := types.Dict{"Flags": types.Integer(4 | 32)}      // contradictory
	diffEnc := types.Dict{"Differences": types.Array{}}     // an encoding dict
	baseEnc := types.Dict{"BaseEncoding": *name("WinAnsiEncoding")}
	emptyEnc := types.Dict{} // an encoding dict carrying neither

	cases := []struct {
		desc      string
		font      types.Dict
		want      string
		wantNamed bool
	}{
		{"Type3 beats everything, even /ToUnicode",
			types.Dict{"Subtype": *name("Type3"), "ToUnicode": types.Integer(1)}, classG, false},
		{"Type0 with /ToUnicode is A",
			types.Dict{"Subtype": *name("Type0"), "ToUnicode": types.Integer(1)}, classA, false},
		{"Type0 without /ToUnicode is F",
			types.Dict{"Subtype": *name("Type0")}, classF, false},
		{"simple with /ToUnicode is B, whatever its encoding",
			types.Dict{"Subtype": *name("Type1"), "ToUnicode": types.Integer(1), "Encoding": diffEnc}, classB, false},
		{"simple with /Differences is C",
			types.Dict{"Subtype": *name("Type1"), "Encoding": diffEnc}, classC, false},
		{"SYMBOLIC with /Differences is C, not E: /Differences runs first",
			types.Dict{"Subtype": *name("Type1"), "Encoding": diffEnc, "FontDescriptor": symbolic}, classC, false},
		{"simple, symbolic, no /ToUnicode, no /Differences is E",
			types.Dict{"Subtype": *name("Type1"), "FontDescriptor": symbolic}, classE, false},
		{"a named encoding is D, and named",
			types.Dict{"Subtype": *name("Type1"), "Encoding": *name("WinAnsiEncoding")}, classD, true},
		{"an encoding dict with /BaseEncoding is D, and named",
			types.Dict{"Subtype": *name("Type1"), "Encoding": baseEnc}, classD, true},
		{"no /Encoding at all is D, and ABSENT",
			types.Dict{"Subtype": *name("Type1")}, classD, false},
		{"an encoding dict with neither is D and ABSENT: no better than none",
			types.Dict{"Subtype": *name("Type1"), "Encoding": emptyEnc}, classD, false},
		{"non-symbolic flags do not reach E",
			types.Dict{"Subtype": *name("Type1"), "FontDescriptor": nonsymbolic}, classD, false},
		{"symbolic AND non-symbolic is contradictory, and is not E",
			types.Dict{"Subtype": *name("Type1"), "FontDescriptor": both}, classD, false},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got, named := classify(ctx, c.font)
			if got != c.want {
				t.Errorf("class = %s, want %s", got, c.want)
			}
			if got == classD && named != c.wantNamed {
				t.Errorf("D named = %v, want %v", named, c.wantNamed)
			}
		})
	}
}

// TestForEachFontExcludesCIDDescendants pins the exclusion section 4.1 states.
// CIDFontType0 and CIDFontType2 are never shown directly — their Type0 parent
// is — so counting them would double every composite font and inflate the
// published totals.
func TestForEachFontExcludesCIDDescendants(t *testing.T) {
	ctx := ctxForDeref(t)
	ctx.XRefTable.Table = map[int]*model.XRefTableEntry{}
	add := func(i int, d types.Dict) {
		o := types.Object(d)
		ctx.XRefTable.Table[i] = &model.XRefTableEntry{Object: o}
	}
	add(1, types.Dict{"Type": *name("Font"), "Subtype": *name("Type0")})
	add(2, types.Dict{"Type": *name("Font"), "Subtype": *name("CIDFontType0")})
	add(3, types.Dict{"Type": *name("Font"), "Subtype": *name("CIDFontType2")})
	add(4, types.Dict{"Type": *name("Font"), "Subtype": *name("TrueType")})
	add(5, types.Dict{"Type": *name("Page")}) // not a font at all

	var seen []string
	forEachFont(ctx, func(d types.Dict) {
		seen = append(seen, string(*d.NameEntry("Subtype")))
	})
	if len(seen) != 2 {
		t.Fatalf("visited %d fonts (%v); want 2, the Type0 and the TrueType", len(seen), seen)
	}
	for _, s := range seen {
		if s == "CIDFontType0" || s == "CIDFontType2" {
			t.Errorf("visited a CID descendant %q; it would double-count its Type0 parent", s)
		}
	}
}
