package pdfdoc

// DanglingRefs (byb-yul.6) is the one implementation of "does this written
// document reference an object it does not define", used by both
// TestBuildFromPagesWritesEveryObjectItReferences and the BYBLOS_SAMPLE
// harness (harness_sample_test.go). It used to be duplicated -- one copy
// inside assertNoDanglingRefs and a near-identical one in an earlier agent's
// throwaway harness -- which is exactly how a harness ends up reporting clean
// while the unit test reports dirty. There is now exactly one.

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// DanglingRef is one reference naming an object the document does not define.
type DanglingRef struct {
	Object int // the object holding the reference
	Target int // the object number it names
}

func (d DanglingRef) String() string {
	return fmt.Sprintf("%d->%d", d.Object, d.Target)
}

// DanglingRefs re-reads out and reports every DanglingRef in it, sorted for a
// deterministic diff. ISO 32000-1 7.3.10 makes a reference to an undefined
// object the null object, so nothing here fails to parse -- ReadContext and
// Validate both accept a document DanglingRefs reports on, which is exactly
// why the writer this package uses cannot rely on either to catch the bug
// byb-yul.6 is about.
func DanglingRefs(out []byte) ([]DanglingRef, error) {
	ctx, err := api.ReadContext(bytes.NewReader(out), defaultConfig())
	if err != nil {
		return nil, err
	}
	xt := ctx.XRefTable
	defined := map[int]bool{}
	for objNr, e := range xt.Table {
		if e != nil && !e.Free {
			defined[objNr] = true
		}
	}
	var hits []DanglingRef
	for objNr := range defined {
		if objNr == 0 {
			continue
		}
		o, err := xt.Dereference(types.IndirectRef{ObjectNumber: types.Integer(objNr)})
		if err != nil || o == nil {
			continue
		}
		for _, target := range refsOf(o) {
			if !defined[target] {
				hits = append(hits, DanglingRef{Object: objNr, Target: target})
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Object != hits[j].Object {
			return hits[i].Object < hits[j].Object
		}
		return hits[i].Target < hits[j].Target
	})
	return hits, nil
}
