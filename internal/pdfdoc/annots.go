package pdfdoc

import (
	"fmt"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// Annot is one annotation as its own dictionary declares it, in the same
// default user space as Page.MediaBox: points, origin lower-left.
//
// Like ImageInfo, this records declarations rather than content. Whether the
// annotation's appearance stream actually deposits ink where Rect says it does
// needs a renderer, and design spec section 2 puts that out of scope; for
// deciding whether Byblos is dropping something a viewer would show, "it
// declares a normal appearance and a box" is the whole answer.
type Annot struct {
	Subtype string // /Subtype; "" when absent
	Rect    Rect   // /Rect, normalised so LL really is the minimum corner
	HasRect bool   // a well-formed 4-number /Rect was present
	Flags   int    // /F; ISO 32000-1 Table 165. 0 when absent
	HasAP   bool   // /AP present
	HasAPN  bool   // /AP /N resolves to a stream, or /AS selects one that does
	HasOC   bool   // /OC present: visibility depends on optional-content state
}

// Annots returns the annotations declared by page n.
//
// /Annots is NOT inheritable: ISO 32000-1 Table 30 lists only /Resources,
// /MediaBox, /CropBox and /Rotate, so the page's own dictionary is the whole
// answer and InheritedPageAttrs carries nothing to merge.
//
// This is deliberately not a field on Page. ExtractPageRaster and inspectPage
// each call Page, so filling it there would dereference every annotation twice
// on every extract, forever, to serve a measurement. Move it onto Page when
// classification actually consumes it.
func (d *doc) Annots(n int) ([]Annot, error) {
	if n < 1 || n > d.ctx.PageCount {
		return nil, fmt.Errorf("byblos/pdfdoc: page %d out of range 1..%d", n, d.ctx.PageCount)
	}
	pd, _, _, err := d.ctx.XRefTable.PageDict(n, true)
	if err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: page %d dict: %w", n, err)
	}
	if pd == nil {
		return nil, fmt.Errorf("byblos/pdfdoc: page %d has no dictionary", n)
	}
	// arrayEntry reads through an indirect reference. A page whose /Annots is a
	// reference is legal and common, and types.Dict.ArrayEntry reports it as
	// absent — the same trap normalizePageTree exists to repair for /Kids.
	arr := d.arrayEntry(pd, "Annots")
	if len(arr) == 0 {
		return nil, nil
	}

	out := make([]Annot, 0, len(arr))
	for _, o := range arr {
		// An entry naming an object that is free in the xref dereferences to a
		// nil dictionary and a nil error, so presence has to be checked, not
		// just the error.
		ad, ok := d.deref(o).(types.Dict)
		if !ok || ad == nil {
			continue
		}
		var a Annot
		a.Subtype = d.nameEntry(ad, "Subtype")
		a.Flags = d.intEntry(ad, "F")
		_, a.HasOC = ad.Find("OC")

		if ra := d.arrayEntry(ad, "Rect"); len(ra) == 4 {
			// Not xt.RectForArray: it indexes a[0]..a[3] with no length check,
			// so a malformed /Rect panics rather than diverting.
			if r, ok := d.rectFromArray(ra); ok {
				a.Rect, a.HasRect = r, true
			}
		}
		if ap := d.dictEntry(ad, "AP"); ap != nil {
			a.HasAP = true
			// /N is either the appearance stream itself or a sub-dictionary of
			// named states, in which case /AS says which one is current.
			switch v := d.deref(ap["N"]).(type) {
			case types.StreamDict:
				a.HasAPN = true
			case types.Dict:
				if as := d.nameEntry(ad, "AS"); as != "" {
					_, a.HasAPN = d.deref(v[as]).(types.StreamDict)
				}
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// Paints reports whether this annotation puts marks on the page.
//
// It is deliberately conservative in one direction only: every reason to say
// no is a fact in the dictionary, and anything unrecognised counts as painting.
// Over-counting shows up as a page needlessly flagged; under-counting is the
// silent data loss this measurement exists to find.
//
// Reason returns the bucket name, so a caller can report why rather than only
// how many.
func (a Annot) Paints() bool { return a.Reason() == "" }

// Reason names why the annotation deposits nothing, or "" when it paints.
func (a Annot) Reason() string {
	switch {
	case a.Subtype == "":
		return "no-subtype"
	// A /Popup is the note window of its parent markup annotation. It is never
	// drawn as part of the page.
	case a.Subtype == "Popup":
		return "popup"
	// ISO 32000-1 Table 165. Bit 2 (value 2) Hidden: do not render at all.
	case a.Flags&2 != 0:
		return "hidden"
	// Bit 6 (value 32) NoView: rendered when printing, never on screen. It
	// still belongs in an archival raster, so it is separated rather than
	// dismissed.
	case a.Flags&32 != 0:
		return "noview-print-only"
	case !a.HasRect:
		return "no-rect"
	case a.Rect.URX <= a.Rect.LLX || a.Rect.URY <= a.Rect.LLY:
		return "zero-area-rect"
	case a.HasAPN:
		return "" // paints
	case a.HasAP:
		return "ap-without-n"
	// Without an appearance stream a viewer may synthesise one from the
	// annotation's own properties. Whether it does is a viewer decision, so
	// these are counted apart from the certain cases.
	case viewerSynthesises[a.Subtype]:
		return "no-ap-viewer-synthesised"
	}
	return "no-ap"
}

// viewerSynthesises lists the subtypes a viewer is expected to draw from the
// dictionary when /AP is missing. Link is absent on purpose: its visible border
// is legacy behaviour that modern viewers suppress.
var viewerSynthesises = map[string]bool{
	"Square": true, "Circle": true, "Line": true, "Polygon": true,
	"PolyLine": true, "Ink": true, "FreeText": true, "Text": true,
	"Highlight": true, "Underline": true, "StrikeOut": true,
	"Squiggly": true, "Caret": true,
}

// nameEntry reads a /Name through an indirect reference, which types.Dict's own
// NameEntry does not.
func (d *doc) nameEntry(dict types.Dict, key string) string {
	if v, ok := d.deref(dict[key]).(types.Name); ok {
		return v.Value()
	}
	return ""
}

// rectFromArray reads four numbers and normalises them.
//
// ISO 32000-1 7.9.5: a rectangle may be written with its corners in either
// order, so a legal /Rect can arrive with LL above UR. types.NewRectangle does
// not normalise, and an un-normalised read reports negative area, which
// zero-area-rect would then miscount as painting nothing.
func (d *doc) rectFromArray(a types.Array) (Rect, bool) {
	var v [4]float64
	for i := range v {
		f, ok := d.deref(a[i]).(types.Float)
		if ok {
			v[i] = f.Value()
			continue
		}
		n, ok := d.deref(a[i]).(types.Integer)
		if !ok {
			return Rect{}, false
		}
		v[i] = float64(n.Value())
	}
	return Rect{
		LLX: min(v[0], v[2]), LLY: min(v[1], v[3]),
		URX: max(v[0], v[2]), URY: max(v[1], v[3]),
	}, true
}
