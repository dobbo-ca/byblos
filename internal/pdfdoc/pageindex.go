package pdfdoc

import (
	"context"
	"sync/atomic"

	"github.com/dobbo-ca/pdf-go/pkg/pdfcpu/model"
	"github.com/dobbo-ca/pdf-go/pkg/pdfcpu/types"
)

// PageTreeSteps counts the page-tree nodes read to find pages, for tests that
// pin the cost per page.
var PageTreeSteps atomic.Int64

// maxIndexDepth bounds the walk below; a deeper tree takes pdfcpu's own path.
const maxIndexDepth = 32

type pageNode struct {
	ref    types.IndirectRef
	parent int // index into pageIndex.nodes; -1 for the root
}

// pageIndex maps page n to its leaf and ancestors, so a lookup reads the
// path to the page (depth nodes) instead of scanning /Kids from the root,
// which is O(pages) per page on the flat tree scanners write.
type pageIndex struct {
	nodes  []pageNode
	leaves []int // nodes index of page n+1
}

// buildPageIndex walks the tree once. It returns nil for any tree it does not
// fully understand (untyped or odd nodes, a /Count that disagrees with the
// leaves, a cycle, too deep, or a different page count than Open's): lookups
// then use pdfcpu's own PageDict, so such files behave as before.
func (d *doc) buildPageIndex() *pageIndex {
	xt := d.ctx.XRefTable
	root, err := xt.Pages()
	if err != nil {
		return nil
	}
	idx := &pageIndex{}
	seen := map[int]bool{}
	var walk func(ref types.IndirectRef, parent, depth int) (int, bool)
	walk = func(ref types.IndirectRef, parent, depth int) (int, bool) {
		nr := ref.ObjectNumber.Value()
		if depth > maxIndexDepth || seen[nr] {
			return 0, false
		}
		seen[nr] = true
		PageTreeSteps.Add(1)
		dd, err := xt.DereferenceDict(ref)
		if err != nil || dd == nil {
			return 0, false
		}
		typ, _, err := xt.DereferenceNameEntry(dd, "Type")
		if err != nil || typ == nil {
			return 0, false
		}
		me := len(idx.nodes)
		idx.nodes = append(idx.nodes, pageNode{ref, parent})
		if _, hasKids := dd.Find("Kids"); !hasKids {
			if typ.Value() != "Page" {
				return 0, false
			}
			idx.leaves = append(idx.leaves, me)
			return 1, true
		}
		kids := dd.ArrayEntry("Kids") // direct: normalizePageTree
		if typ.Value() != "Pages" || kids == nil {
			return 0, false
		}
		sum := 0
		for _, k := range kids {
			kr, ok := k.(types.IndirectRef)
			if !ok {
				return 0, false
			}
			n, ok := walk(kr, me, depth+1)
			if !ok {
				return 0, false
			}
			sum += n
		}
		if c, _, err := xt.DereferenceIntegerEntry(dd, "Count"); err != nil || (c != nil && c.Value() != sum) {
			return 0, false
		}
		return sum, true
	}
	if n, ok := walk(*root, -1, 0); !ok || n != d.ctx.PageCount {
		return nil
	}
	return idx
}

// pageDict is xt.PageDict(n, consolidateRes=false) without the scan from the
// root: the same dictionary and inherited attributes, read down the page's own
// path. Anything it cannot answer exactly (an error, an odd attribute) is
// answered by pdfcpu, which also produces the error.
func (d *doc) pageDict(n int) (types.Dict, *model.InheritedPageAttrs, error) {
	xt := d.ctx.XRefTable
	if !d.pageIndexed {
		d.pageIndexed, d.pidx = true, d.buildPageIndex()
	}
	if d.pidx != nil {
		if pd, inh, ok := d.indexedPageDict(n); ok {
			return pd, inh, nil
		}
	}
	pd, _, inh, err := xt.PageDict(context.Background(), n, false)
	return pd, inh, err
}

func (d *doc) indexedPageDict(n int) (types.Dict, *model.InheritedPageAttrs, bool) {
	xt := d.ctx.XRefTable
	var path []int
	for i := d.pidx.leaves[n-1]; i >= 0; i = d.pidx.nodes[i].parent {
		path = append(path, i)
	}
	inh := &model.InheritedPageAttrs{}
	var dd types.Dict
	for i := len(path) - 1; i >= 0; i-- { // root first, the page last, as pdfcpu applies them
		PageTreeSteps.Add(1)
		var err error
		if dd, err = xt.DereferenceDict(d.pidx.nodes[path[i]].ref); err != nil || dd == nil {
			return nil, nil, false
		}
		if o, ok := dd.Find("MediaBox"); ok {
			if inh.MediaBox, ok = d.indexedRect(o); !ok {
				return nil, nil, false
			}
		}
		if o, ok := dd.Find("CropBox"); ok {
			if inh.CropBox, ok = d.indexedRect(o); !ok {
				return nil, nil, false
			}
		}
		if o, ok := dd.Find("Rotate"); ok {
			r, err := xt.Dereference(o)
			v, isInt := r.(types.Integer)
			if err != nil || !isInt { // a float or a mistake: pdfcpu's rules
				return nil, nil, false
			}
			inh.Rotate = v.Value()
		}
		if o, ok := dd.Find("Resources"); ok {
			if inh.Resources, err = xt.DereferenceDict(o); err != nil {
				return nil, nil, false
			}
		}
	}
	return dd, inh, true
}

func (d *doc) indexedRect(o types.Object) (*types.Rectangle, bool) {
	xt := d.ctx.XRefTable
	a, err := xt.DereferenceArray(o)
	if err != nil || len(a) < 4 {
		return nil, false
	}
	var v [4]float64
	for i := range v {
		if v[i], err = xt.DereferenceNumber(a[i]); err != nil {
			return nil, false
		}
	}
	return types.NewRectangle(v[0], v[1], v[2], v[3]), true
}
