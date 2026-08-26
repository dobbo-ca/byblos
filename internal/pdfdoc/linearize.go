package pdfdoc

// The pdfcpu half of Annex F linearization (byb-1y7).
//
// internal/linearize owns everything Annex F: the partition, the renumbering,
// the two cross-reference sections, the parameter dictionary and the bit-packed
// hint tables. It holds no PDF parser, so this file is the translation layer --
// it turns a pdfcpu context into the neutral representation that package's
// Graph describes, serializes each object under the renumbering that package
// decides, and hands the bytes back.
//
// Why the write path cannot be pdfcpu's. pdfcpu's writeObjects walks the
// catalog, then the page tree, then the info dictionary; object order falls out
// of that traversal and there is no hook to impose an Annex F layout, emit two
// cross-reference sections, or write a hint stream. Its optimize pass actively
// frees the hint-table objects of a file that arrives linearized
// (deleteRedundantObject, guarded by IsLinearizationObject).
//
// FOUR pdfcpu v0.15.0 behaviours are load-bearing here. The first three are
// already documented in write.go's package comment and apply unchanged; the
// fourth is specific to serializing objects by hand:
//
//   - types.NewIndirectRef returns a *types.IndirectRef, and
//     XRefTable.Dereference matches only the VALUE type (model/dereference.go:
//     `ir, ok := o.(types.IndirectRef); if !ok { return o, nil }`). Passing a
//     constructor result back gets the pointer returned unchanged with a nil
//     error. Every reference this file builds is the value form.
//
//   - Dict.PDFString and Array.PDFString silently DROP any entry whose concrete
//     type is outside their switch: the default arm is guarded by
//     log.InfoEnabled(), which is off (types/dict.go:517, types/array.go:189).
//     They accept nil, Dict, Array, IndirectRef, Name, Integer, Float, Boolean,
//     StringLiteral and HexLiteral -- and NOT *IndirectRef, StreamDict,
//     LazyObjectStreamObject or Rectangle. A dropped entry produces a file that
//     parses; in an array the drop also silently shortens it, so a /MediaBox of
//     four numbers becomes three. auditSerializable is the guard, and it is not
//     optional.

import (
	"fmt"
	"io"
	"slices"

	"github.com/dobbo-ca/byblos/internal/linearize"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// inheritable are the page attributes ISO 32000-1 Table 30 lets a /Pages node
// hold on behalf of its descendants.
var inheritable = []string{"Resources", "MediaBox", "CropBox", "Rotate"}

// openDocKeys are the catalog entries a reader consults before it can display
// anything. Annex F.3.5 puts what they reach in part 4.
var openDocKeys = []string{"ViewerPreferences", "PageMode", "Threads", "OpenAction", "AcroForm"}

// Linearize writes a linearized ("fast web view") copy of rs's PDF to w.
//
// It is the last thing that may touch the bytes: anything that re-serializes
// the document afterwards -- including WriteProperties, which goes through
// pdfcpu's writer -- silently removes the linearization again.
func Linearize(rs io.ReadSeeker, w io.Writer) (err error) {
	defer catchPanic("linearize", &err)

	opened, err := Open(rs)
	if err != nil {
		return err
	}
	d, ok := opened.(*doc)
	if !ok {
		return fmt.Errorf("byblos/pdfdoc: linearize: unexpected document type %T", opened)
	}
	xt := d.ctx.XRefTable

	if xt.Encrypt != nil {
		// pdfcpu decrypts strings and streams on read. Re-emitting them under
		// the input's /Encrypt dictionary would produce a document that opens
		// as garbage, and stripping the encryption would change what the
		// document IS. Neither is Byblos's call to make silently.
		return fmt.Errorf("byblos/pdfdoc: linearize: the document is encrypted")
	}
	if xt.Root == nil {
		return fmt.Errorf("byblos/pdfdoc: linearize: the document has no catalog")
	}

	g, err := buildGraph(d)
	if err != nil {
		return err
	}
	plan, err := linearize.PlanLayout(g)
	if err != nil {
		return fmt.Errorf("byblos/pdfdoc: linearize: %w", err)
	}
	bodies, err := serializeAll(xt, plan.Renumber)
	if err != nil {
		return err
	}
	meta := linearize.Meta{Version: xt.VersionString(), ID: trailerID(xt)}
	if err := linearize.Write(w, plan, bodies, meta); err != nil {
		return fmt.Errorf("byblos/pdfdoc: linearize: %w", err)
	}
	return nil
}

// buildGraph turns the parsed context into linearize.Graph.
func buildGraph(d *doc) (linearize.Graph, error) {
	xt := d.ctx.XRefTable
	catalog, err := xt.Catalog()
	if err != nil || catalog == nil {
		return linearize.Graph{}, fmt.Errorf("byblos/pdfdoc: linearize: catalog: %w", err)
	}
	g := linearize.Graph{Catalog: xt.Root.ObjectNumber.Value()}
	if xt.Info != nil {
		g.Info = xt.Info.ObjectNumber.Value()
	}

	root, err := xt.Pages()
	if err != nil || root == nil {
		return linearize.Graph{}, fmt.Errorf("byblos/pdfdoc: linearize: page tree: %w", err)
	}
	pages, tree, err := walkPageTree(xt, *root, types.Dict{}, map[int]bool{})
	if err != nil {
		return linearize.Graph{}, err
	}
	if len(pages) == 0 {
		return linearize.Graph{}, fmt.Errorf("byblos/pdfdoc: linearize: the document has no pages")
	}
	g.Pages, g.PageTree = pages, tree

	if o, found := catalog.Find("Outlines"); found {
		if r, ok := indirectRefOf(o); ok {
			g.Outlines = r.ObjectNumber.Value()
		}
	}
	if m, found := catalog.Find("PageMode"); found {
		if n, ok := deref(xt, m).(types.Name); ok {
			g.UseOutlines = n.Value() == "UseOutlines"
		}
	}
	for key, val := range catalog {
		switch {
		case key == "Pages" || key == "Outlines" || key == "Type":
		case slices.Contains(openDocKeys, key):
			g.OpenDoc = append(g.OpenDoc, refsOf(val)...)
		default:
			g.Other = append(g.Other, refsOf(val)...)
		}
	}
	if g.Info != 0 {
		g.Other = append(g.Other, g.Info)
	}
	slices.Sort(g.OpenDoc)
	slices.Sort(g.Other)
	g.OpenDoc = slices.Compact(g.OpenDoc)
	g.Other = slices.Compact(g.Other)

	live, err := liveObjects(xt)
	if err != nil {
		return linearize.Graph{}, err
	}
	pageSet := map[int]bool{}
	for _, n := range pages {
		pageSet[n] = true
	}
	reach := reachable(live, g.Catalog, g.Info)

	g.Refs = make(map[int][]int, len(reach))
	for n := range reach {
		o := live[n]
		if err := auditSerializable(n, o); err != nil {
			return linearize.Graph{}, err
		}
		var out []int
		if pageSet[n] {
			// /Parent points back at the page tree, which Annex F.3.10 puts in
			// part 9, and /Thumb at a thumbnail no reader needs to show the
			// page. Following either from a page drags part-9 material into the
			// first-page section. Both objects are still written -- they are
			// reachable through liveObjects -- they just do not steer the
			// partition.
			if pd, ok := o.(types.Dict); ok {
				for key, val := range pd {
					if key == "Parent" || key == "Thumb" {
						continue
					}
					out = append(out, refsOf(val)...)
				}
			}
		} else {
			out = refsOf(o)
		}
		out = slices.DeleteFunc(out, func(m int) bool { return !reach[m] })
		slices.Sort(out)
		g.Refs[n] = slices.Compact(out)
	}
	return g, nil
}

// walkPageTree records the page tree in page order and pushes inherited
// attributes down onto the leaves.
//
// The push-down is required, not an optimization: Annex F.3.7 puts the page
// tree in part 9, past /E, so a first page whose /MediaBox lives on a /Pages
// node would need an object a progressive reader has not received yet. Nothing
// else in this package does it -- normalizePageTree only repairs an indirect
// /Kids -- and the corpus does exercise it: two pages of 'indirect-kids'
// inherit /Rotate.
//
// The raw types.Object is copied rather than anything from
// model.InheritedPageAttrs, whose MediaBox is a *types.Rectangle -- a type
// Dict.PDFString silently drops.
func walkPageTree(xt *model.XRefTable, ref types.IndirectRef, inh types.Dict, seen map[int]bool) (pages, tree []int, err error) {
	n := ref.ObjectNumber.Value()
	if seen[n] {
		return nil, nil, fmt.Errorf("byblos/pdfdoc: linearize: page tree revisits object %d", n)
	}
	seen[n] = true

	d, err := xt.DereferenceDict(ref)
	if err != nil || d == nil {
		return nil, nil, fmt.Errorf("byblos/pdfdoc: linearize: page tree node %d: %w", n, err)
	}
	kids, hasKids := d.Find("Kids")
	if !hasKids {
		for _, key := range inheritable {
			if _, ok := d.Find(key); !ok {
				if v, ok := inh[key]; ok {
					d[key] = v
				}
			}
		}
		return []int{n}, nil, nil
	}

	down := types.Dict{}
	for k, v := range inh {
		down[k] = v
	}
	for _, key := range inheritable {
		if v, ok := d.Find(key); ok {
			down[key] = v
			// And take it OFF the node. Leaving it would be redundant once
			// every leaf below carries its own copy, and the reference
			// implementation treats a leftover as an error: qpdf's
			// pushInheritedAttributesToPage runs in no-change mode while
			// checking a linearized file and warns "detected an inheritable
			// attribute" for any it finds, which sinks the whole check.
			delete(d, key)
		}
	}
	arr, err := xt.DereferenceArray(kids)
	if err != nil {
		return nil, nil, fmt.Errorf("byblos/pdfdoc: linearize: /Kids of %d: %w", n, err)
	}
	tree = append(tree, n)
	for _, k := range arr {
		kr, ok := indirectRefOf(k)
		if !ok {
			return nil, nil, fmt.Errorf("byblos/pdfdoc: linearize: /Kids of %d holds a direct object", n)
		}
		p, t, err := walkPageTree(xt, kr, down, seen)
		if err != nil {
			return nil, nil, err
		}
		pages = append(pages, p...)
		tree = append(tree, t...)
	}
	return pages, tree, nil
}

// liveObjects materializes every object the cross-reference table holds, minus
// the containers.
//
// An object stream and a cross-reference stream describe how the INPUT was
// stored; the objects they hold are already present as ordinary entries (a
// compressed object surfaces as a normal object once dereferenced), and
// carrying the containers into the output would write the input's storage
// layout into a file that no longer uses it.
func liveObjects(xt *model.XRefTable) (map[int]types.Object, error) {
	live := make(map[int]types.Object, len(xt.Table))
	for n, entry := range xt.Table {
		if n == 0 || entry == nil || entry.Free {
			continue
		}
		o, err := xt.Dereference(types.IndirectRef{ObjectNumber: types.Integer(n)})
		if err != nil {
			return nil, fmt.Errorf("byblos/pdfdoc: linearize: object %d: %w", n, err)
		}
		if o == nil {
			continue
		}
		switch o.(type) {
		case types.ObjectStreamDict, *types.ObjectStreamDict,
			types.XRefStreamDict, *types.XRefStreamDict:
			continue
		}
		live[n] = o
	}
	return live, nil
}

// reachable is the set of objects the catalog and the info dictionary can get
// to. Anything else is a leftover from an incremental update, or the input's
// own linearization dictionary and hint stream -- which are unreferenced by
// construction and must not be carried into the output.
func reachable(live map[int]types.Object, roots ...int) map[int]bool {
	seen := map[int]bool{}
	var stack []int
	for _, r := range roots {
		if r != 0 {
			stack = append(stack, r)
		}
	}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[n] {
			continue
		}
		o, ok := live[n]
		if !ok {
			continue
		}
		seen[n] = true
		stack = append(stack, refsOf(o)...)
	}
	return seen
}

// refsOf collects the object numbers o refers to, at any depth, in both the
// value and pointer forms pdfcpu's parse paths produce.
//
// A stream dictionary's /Length is deliberately NOT followed. It is the one
// entry this file rewrites: streamBody replaces it with a direct integer,
// because the only length that can be correct is the one measured off the bytes
// actually emitted. Following an indirect /Length would put the integer object
// it names into the write set -- it would take an object number, an xref entry,
// a slot in a page's or the shared table's object count -- and then the
// rewrite would delete the only reference to it, leaving an orphan the finished
// file never mentions.
//
// That is not cosmetic. qpdf refuses such a file, because its own length
// arithmetic (Lin::lengthNextN) walks consecutive object numbers and stops at
// one it never dereferenced. Measured on four documents from the pdfcpu module
// cache whose pdfcpu rewrite keeps an indirect /Length -- Walden.pdf,
// golang.pdf, OptimizeTest.pdf, annotTest.pdf -- every one produced
// "found unknown object while calculating length for linearization data" and,
// on three of them, an /E that disagreed with the computed value.
func refsOf(o types.Object) []int {
	var out []int
	var walk func(types.Object)
	walkDict := func(d types.Dict, isStream bool) {
		for key, val := range d {
			if isStream && key == "Length" {
				continue
			}
			walk(val)
		}
	}
	walk = func(o types.Object) {
		switch v := o.(type) {
		case types.IndirectRef:
			out = append(out, v.ObjectNumber.Value())
		case *types.IndirectRef:
			out = append(out, v.ObjectNumber.Value())
		case types.Dict:
			walkDict(v, false)
		case types.Array:
			for _, val := range v {
				walk(val)
			}
		case types.StreamDict:
			walkDict(v.Dict, true)
		case *types.StreamDict:
			walkDict(v.Dict, true)
		}
	}
	walk(o)
	return out
}

// serializeAll renders every object under the renumbering, keyed by NEW number.
func serializeAll(xt *model.XRefTable, renumber map[int]int) (map[int][]byte, error) {
	out := make(map[int][]byte, len(renumber))
	for old, nw := range renumber {
		o, err := xt.Dereference(types.IndirectRef{ObjectNumber: types.Integer(old)})
		if err != nil {
			return nil, fmt.Errorf("byblos/pdfdoc: linearize: object %d: %w", old, err)
		}
		body, err := serialize(renumberObject(o, renumber))
		if err != nil {
			return nil, fmt.Errorf("byblos/pdfdoc: linearize: object %d: %w", old, err)
		}
		out[nw] = body
	}
	return out, nil
}

// renumberObject deep-copies o with every reference rewritten.
//
// A reference to an object that is not in the map becomes nil, which
// PDFString renders as "null". ISO 32000-1 7.3.10 says a reference to an
// undefined object shall be treated as the null object, so this loses nothing
// -- whereas leaving the reference in place would name an object number that no
// longer exists, which makes a reader stop with "no xref table entry".
func renumberObject(o types.Object, renumber map[int]int) types.Object {
	switch v := o.(type) {
	case types.IndirectRef:
		return renumberRef(v.ObjectNumber.Value(), renumber)
	case *types.IndirectRef:
		return renumberRef(v.ObjectNumber.Value(), renumber)
	case types.Dict:
		out := make(types.Dict, len(v))
		for k, val := range v {
			out[k] = renumberObject(val, renumber)
		}
		return out
	case types.Array:
		out := make(types.Array, len(v))
		for i, val := range v {
			out[i] = renumberObject(val, renumber)
		}
		return out
	case types.StreamDict:
		sd := v
		sd.Dict = renumberObject(v.Dict, renumber).(types.Dict)
		return sd
	case *types.StreamDict:
		sd := *v
		sd.Dict = renumberObject(v.Dict, renumber).(types.Dict)
		return sd
	}
	return o
}

func renumberRef(old int, renumber map[int]int) types.Object {
	n, ok := renumber[old]
	if !ok {
		return nil
	}
	// The value form, deliberately: Dict.PDFString drops a *IndirectRef.
	return types.IndirectRef{ObjectNumber: types.Integer(n), GenerationNumber: types.Integer(0)}
}

// serialize renders one object's body, without the "N 0 obj" / "endobj"
// wrapper.
//
// The stream shape mirrors pdfcpu's writeStreamObject exactly -- dictionary,
// "\nstream\n", the raw (still encoded) payload, "\nendstream" -- so an object
// that goes through here is byte-identical to one pdfcpu wrote itself.
func serialize(o types.Object) ([]byte, error) {
	switch v := o.(type) {
	case nil:
		return []byte("null"), nil
	case types.StreamDict:
		return streamBody(v)
	case *types.StreamDict:
		return streamBody(*v)
	}
	s, ok := o.(interface{ PDFString() string })
	if !ok {
		return nil, fmt.Errorf("%T cannot be serialized", o)
	}
	return []byte(s.PDFString()), nil
}

func streamBody(sd types.StreamDict) ([]byte, error) {
	// /Length must be direct and must match the bytes actually written. An
	// indirect /Length would name an object number the renumbering has moved,
	// and pdfcpu's own writer refuses a stream whose payload disagrees with it.
	sd.Dict["Length"] = types.Integer(len(sd.Raw))
	out := []byte(sd.Dict.PDFString())
	out = append(out, "\nstream\n"...)
	out = append(out, sd.Raw...)
	out = append(out, "\nendstream"...)
	return out, nil
}

// auditSerializable fails on any value Dict.PDFString or Array.PDFString would
// drop on the floor.
//
// This is the guard for the second trap in this file's package comment. The
// drop is silent -- the default arm of both switches is behind a logger that is
// off by default -- so without this the failure is a document that reads back
// cleanly and has lost a /MediaBox, or an array that is one element short. An
// error naming the object and the Go type is worth far more than a file that
// validates.
func auditSerializable(objNr int, o types.Object) error {
	var walk func(path string, o types.Object) error
	walk = func(path string, o types.Object) error {
		switch v := o.(type) {
		case nil, types.Name, types.Integer, types.Float, types.Boolean,
			types.StringLiteral, types.HexLiteral, types.IndirectRef:
			return nil
		case *types.IndirectRef:
			// Normalized by renumberObject before serialization; harmless here.
			return nil
		case types.Dict:
			for k, val := range v {
				if err := walk(path+"/"+k, val); err != nil {
					return err
				}
			}
			return nil
		case types.Array:
			for i, val := range v {
				if err := walk(fmt.Sprintf("%s[%d]", path, i), val); err != nil {
					return err
				}
			}
			return nil
		case types.StreamDict:
			return walk(path, v.Dict)
		case *types.StreamDict:
			return walk(path, v.Dict)
		}
		return fmt.Errorf("byblos/pdfdoc: linearize: object %d%s holds a %T, which "+
			"pdfcpu's serializer discards silently", objNr, path, o)
	}
	switch v := o.(type) {
	case types.StreamDict:
		return walk("", v.Dict)
	case *types.StreamDict:
		return walk("", v.Dict)
	}
	return walk("", o)
}

func deref(xt *model.XRefTable, o types.Object) types.Object {
	v, err := xt.Dereference(o)
	if err != nil {
		return nil
	}
	return v
}

// trailerID carries the input's /ID forward. It is not regenerated: nothing in
// this pass changes what the document IS, and a reader that has cached the file
// by /ID should keep matching it.
func trailerID(xt *model.XRefTable) [2][]byte {
	var out [2][]byte
	for i := 0; i < len(xt.ID) && i < 2; i++ {
		switch v := xt.ID[i].(type) {
		case types.HexLiteral:
			if b, err := v.Bytes(); err == nil {
				out[i] = b
			}
		case types.StringLiteral:
			out[i] = []byte(v.Value())
		}
	}
	return out
}
