// Package pdfdoc is the only package in Byblos that imports pdfcpu.
//
// Everything above it speaks in the types declared here, so replacing the
// underlying PDF library is a change to this package alone (design spec
// section 3). arch_test.go in the repository root enforces that.
//
// pdfcpu API notes, verified against v0.13.0:
//
//   - api.ReadAndValidate dereferences conf.Cmd with no nil check and pdfcpu's
//     fault.Catch only recovers its own panic type, so passing a nil
//     *model.Configuration kills the process rather than returning an error.
//     Every call here passes model.NewDefaultConfiguration().
//   - ctx.PageCount is zero after ReadContext; ctx.EnsurePageCount() populates it.
//   - types.StreamDict.Content is empty until Decode() is called.
//   - model.Image.Width/Height/Bpc are zero unless ExtractImage is called with
//     stub=true, and a stub carries no pixels. Pixel dimensions therefore come
//     from the image XObject's own stream dictionary.
//   - pdfcpu cannot decode JBIG2Decode or JPXDecode and returns the raw opaque
//     bytes with FileType "jbig2"/"jpx" rather than erroring. Callers must check
//     the file type; extract.go does.
//   - pdfcpu.RenderImage returns (nil, "", nil) for any other unhandled filter,
//     and ExtractImage passes that straight through as a model.Image with a nil
//     embedded Reader. Reading it panics, so RawImage guards and returns
//     ErrUnsupportedCodec instead.
//   - types.Dict's typed accessors (IntEntry, DictEntry, ArrayEntry, ...) do NOT
//     dereference an indirect reference; they return zero. Real documents do use
//     an indirect /Resources on a Form XObject, so every dictionary read that
//     feeds a Byblos type goes through the deref helpers at the bottom of this
//     file.
package pdfdoc

import (
	"errors"
	"fmt"
	"io"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// ErrUnsupportedCodec reports an image stream whose compression filter pdfcpu
// will not render. It exists so that pdfcpu's nil-reader return becomes an
// error at this seam instead of a nil dereference in the caller.
//
// It is NOT returned for JBIG2 or JPX: those come back as real bytes with a
// file type naming the codec, and deciding what to do about them is extract.go's
// job, not this package's.
var ErrUnsupportedCodec = errors.New("byblos/pdfdoc: image codec cannot be rendered")

// Rect is a rectangle in PDF default user space: points, origin lower-left,
// y increasing upward.
type Rect struct{ LLX, LLY, URX, URY float64 }

// ImageInfo describes an image XObject as its own dictionary declares it.
//
// SMask, Mask and ImageMask are the three ways an image can fail to be an
// opaque rectangle of pixels. They are recorded as presence, not as content:
// what the mask actually does needs a renderer, and for deciding whether this
// image hides what is painted under it, "it has one" is the whole answer.
type ImageInfo struct {
	Name          string
	ObjNr         int
	Width, Height int  // pixels
	BPC           int  // /BitsPerComponent; 0 when absent
	ImageMask     bool // /ImageMask: a stencil painted in the fill colour
	SMask         bool // /SMask: a soft mask supplies per-pixel alpha
	Mask          bool // /Mask: a stencil mask or a colour-key range
}

// Page is one page's geometry, content, and resource scope.
type Page struct {
	Index    int // 1-based
	MediaBox Rect
	CropBox  Rect // equals MediaBox when the page declares none
	Rotate   int
	Content  []byte // decoded, concatenated
	Scope    int    // resource scope handle for content.Env
}

// Doc is a parsed PDF and the seam Byblos keeps between itself and pdfcpu.
type Doc interface {
	PageCount() int
	Page(n int) (*Page, error)
	// XObject and ExtGStateOpaque implement content.Env.
	XObject(scope int, name string) (content.XObject, bool)
	ExtGStateOpaque(scope int, name string) bool
	// ImageInfo returns the dictionary facts for an image resolved by XObject,
	// keyed by the ID that XObject returned.
	ImageInfo(id int) (ImageInfo, bool)
	// RawImage renders an image previously resolved by XObject and returns its
	// bytes and the file type pdfcpu inferred. The id is the one XObject
	// returned; an id this document has not resolved is an error.
	RawImage(id int) (data []byte, fileType string, err error)
}

type doc struct {
	ctx     *model.Context
	scopes  []scope
	images  map[int]ImageInfo
	streams map[int]*types.StreamDict // image stream dicts, keyed like images
	nextID  int                       // synthetic ids for direct (non-indirect) image objects
}

type scope struct {
	res    types.Dict
	parent int // -1 for a page scope
}

// Open parses rs. It does not run pdfcpu's validator: real scanner output
// exercises relaxed paths, and rejecting a readable file helps nobody. The
// validation gate belongs to Optimize (B5) and to the caller's policy.
//
// rs is read once, here. Nothing below re-reads it, so a file this function
// accepts cannot later be rejected by a validator Byblos opted out of.
func Open(rs io.ReadSeeker) (Doc, error) {
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: seek: %w", err)
	}
	ctx, err := api.ReadContext(rs, model.NewDefaultConfiguration())
	if err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: read: %w", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: page count: %w", err)
	}
	return &doc{
		ctx:     ctx,
		images:  map[int]ImageInfo{},
		streams: map[int]*types.StreamDict{},
		nextID:  -1,
	}, nil
}

func (d *doc) PageCount() int { return d.ctx.PageCount }

func (d *doc) Page(n int) (*Page, error) {
	if n < 1 || n > d.ctx.PageCount {
		return nil, fmt.Errorf("byblos/pdfdoc: page %d out of range 1..%d", n, d.ctx.PageCount)
	}
	xt := d.ctx.XRefTable
	pd, _, inh, err := xt.PageDict(n, true)
	if err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: page %d dict: %w", n, err)
	}
	if pd == nil || inh == nil || inh.MediaBox == nil {
		return nil, fmt.Errorf("byblos/pdfdoc: page %d has no dictionary or no MediaBox", n)
	}

	p := &Page{
		Index:    n,
		MediaBox: rectOf(inh.MediaBox),
		Rotate:   ((inh.Rotate % 360) + 360) % 360,
		Scope:    d.addScope(inh.Resources, -1),
	}
	p.CropBox = p.MediaBox
	if inh.CropBox != nil {
		p.CropBox = rectOf(inh.CropBox)
	}

	// A page with no /Contents is legal and empty. Check the dictionary rather
	// than matching on pdfcpu's error text: PageContent returns a
	// github.com/pkg/errors value that errors.Is cannot match against a sentinel.
	if _, ok := pd.Find("Contents"); ok {
		c, err := xt.PageContent(pd, n)
		if err != nil {
			return nil, fmt.Errorf("byblos/pdfdoc: page %d content: %w", n, err)
		}
		p.Content = c
	}
	return p, nil
}

// addScope appends a resource scope and returns its handle.
//
// Known simplification: scopes are never reused, so calling Page twice or
// resolving the same form twice appends duplicate entries. They are equivalent,
// so this is a small waste rather than a bug, and a document has at most a few
// thousand of them. Memoize per page index and per (scope, name) if a real
// archive ever shows it matters.
func (d *doc) addScope(res types.Dict, parent int) int {
	d.scopes = append(d.scopes, scope{res: res, parent: parent})
	return len(d.scopes) - 1
}

func (d *doc) XObject(sc int, name string) (content.XObject, bool) {
	obj, ok := d.lookupResource(sc, "XObject", name)
	if !ok {
		return content.XObject{}, false
	}
	id := d.identify(obj)
	sd, _, err := d.ctx.XRefTable.DereferenceStreamDict(obj)
	if err != nil || sd == nil {
		return content.XObject{}, false
	}
	sub := sd.Dict.Subtype()
	if sub == nil {
		return content.XObject{}, false
	}

	switch *sub {
	case "Image":
		d.images[id] = ImageInfo{
			Name:      name,
			ObjNr:     id,
			Width:     d.intEntry(sd.Dict, "Width"),
			Height:    d.intEntry(sd.Dict, "Height"),
			BPC:       d.intEntry(sd.Dict, "BitsPerComponent"),
			ImageMask: d.boolEntry(sd.Dict, "ImageMask"),
			SMask:     hasEntry(sd.Dict, "SMask"),
			Mask:      hasEntry(sd.Dict, "Mask"),
		}
		// Keep the stream dictionary. RawImage renders from this rather than
		// asking pdfcpu which objects a page uses, because that answer comes
		// from the optimize pass, which deduplicates identical rasters.
		d.streams[id] = sd
		return content.XObject{Image: true, ID: id}, true

	case "Form":
		if err := sd.Decode(); err != nil {
			return content.XObject{}, false
		}
		m := content.Identity
		if arr := d.arrayEntry(sd.Dict, "Matrix"); len(arr) == 6 {
			for i := 0; i < 6; i++ {
				v, ok := d.number(arr[i])
				if !ok {
					m = content.Identity
					break
				}
				m[i] = v
			}
		}
		// A form without its own /Resources inherits the enclosing resource
		// dictionary (ISO 32000-1 section 8.10.2), which the scope's parent
		// chain provides.
		formScope := d.addScope(d.dictEntry(sd.Dict, "Resources"), sc)
		return content.XObject{Content: sd.Content, Matrix: m, Scope: formScope}, true
	}
	return content.XObject{}, false
}

// lookupResource walks the scope chain so a form that declares only /Font still
// resolves images through its parent. category is a resource dictionary key:
// "XObject", "ExtGState".
func (d *doc) lookupResource(sc int, category, name string) (types.Object, bool) {
	for i := sc; i >= 0 && i < len(d.scopes); {
		res := d.scopes[i].res
		if res != nil {
			if cat, err := d.ctx.XRefTable.DereferenceDict(res[category]); err == nil && cat != nil {
				if o, ok := cat.Find(name); ok {
					return o, true
				}
			}
		}
		i = d.scopes[i].parent
		if i < 0 {
			break
		}
	}
	return nil, false
}

// ExtGStateOpaque reports whether the named graphics state leaves painting
// fully opaque.
//
// A name that does not resolve, or a value that is not a dictionary, is
// reported as not opaque. That is the direction that costs a divert rather than
// a wrong document: the caller uses this to decide whether an image hides what
// is painted beneath it.
func (d *doc) ExtGStateOpaque(sc int, name string) bool {
	obj, ok := d.lookupResource(sc, "ExtGState", name)
	if !ok {
		return false
	}
	gs, ok := d.deref(obj).(types.Dict)
	if !ok {
		return false
	}
	// /ca is the non-stroking alpha and /CA the stroking one. An image is
	// painted with /ca, but a state that sets either is not one to reason about
	// occlusion from.
	for _, key := range []string{"ca", "CA"} {
		if v, ok := d.number(gs[key]); ok && v < 1 {
			return false
		}
	}
	// /SMask /None is how a graphics state says it has no soft mask. Anything
	// else is a mask, and reading it would mean rendering it.
	if sm, ok := gs.Find("SMask"); ok {
		if n, isName := d.deref(sm).(types.Name); !isName || n.Value() != "None" {
			return false
		}
	}
	return true
}

// identify returns a stable id for an XObject: its PDF object number when it is
// an indirect reference, and a negative synthetic id otherwise.
func (d *doc) identify(o types.Object) int {
	switch v := o.(type) {
	case types.IndirectRef:
		return v.ObjectNumber.Value()
	case *types.IndirectRef:
		return v.ObjectNumber.Value()
	}
	id := d.nextID
	d.nextID--
	return id
}

func (d *doc) ImageInfo(id int) (ImageInfo, bool) {
	info, ok := d.images[id]
	return info, ok
}

// RawImage renders the image XObject that XObject previously resolved as id.
//
// It renders from the stream dictionary this document already holds, on the
// already-parsed context. Two consequences worth stating, because the obvious
// alternative (api.ExtractImagesRaw with a page number) gets both wrong:
//
//   - It is per-image, not per-file. ExtractImagesRaw runs
//     ReadValidateAndOptimize on every call, so extracting an N-page document
//     would re-read, re-validate and re-optimize it N times — and could reject
//     on the validator that Open deliberately skipped.
//   - It is per-object, not per-deduplicated-object. pdfcpu's optimize pass
//     collapses byte-identical image XObjects, so the map ExtractImagesRaw
//     returns for page 2 of a duplex scan is keyed by page 1's object number.
//     See the dup-raster corpus document.
func (d *doc) RawImage(id int) ([]byte, string, error) {
	sd, ok := d.streams[id]
	if !ok {
		return nil, "", fmt.Errorf("byblos/pdfdoc: image %d has not been resolved on this document", id)
	}
	im, err := pdfcpu.ExtractImage(d.ctx, sd, false, d.images[id].Name, id, false)
	if err != nil {
		return nil, "", fmt.Errorf("byblos/pdfdoc: rendering image %d: %w", id, err)
	}
	// pdfcpu signals "I will not render this filter" by returning a nil reader
	// and an empty file type, with a nil error. Reading that panics.
	if im == nil || im.Reader == nil || im.FileType == "" {
		return nil, "", fmt.Errorf("byblos/pdfdoc: image %d: %w", id, ErrUnsupportedCodec)
	}
	b, err := io.ReadAll(im)
	if err != nil {
		return nil, "", fmt.Errorf("byblos/pdfdoc: reading image %d: %w", id, err)
	}
	return b, im.FileType, nil
}

func (d *doc) number(o types.Object) (float64, bool) {
	o, err := d.ctx.XRefTable.Dereference(o)
	if err != nil {
		return 0, false
	}
	switch v := o.(type) {
	case types.Integer:
		return float64(v.Value()), true
	case types.Float:
		return v.Value(), true
	}
	return 0, false
}

func rectOf(r *types.Rectangle) Rect {
	return Rect{LLX: r.LL.X, LLY: r.LL.Y, URX: r.UR.X, URY: r.UR.Y}
}

// --- dereferencing dictionary readers ---------------------------------------
//
// types.Dict's own IntEntry / BooleanEntry / DictEntry / ArrayEntry return the
// zero value when the entry is an types.IndirectRef instead of a direct object.
// An indirect /Resources on a Form XObject is common in real documents and an
// indirect /Width is legal, and in both cases the failure would be silent wrong
// data — an empty form scope, or Width 0 flowing into PageInfo — rather than an
// error. These read through the reference.

func (d *doc) deref(o types.Object) types.Object {
	v, err := d.ctx.XRefTable.Dereference(o)
	if err != nil {
		return nil
	}
	return v
}

func (d *doc) intEntry(dict types.Dict, key string) int {
	if v, ok := d.deref(dict[key]).(types.Integer); ok {
		return v.Value()
	}
	return 0
}

func (d *doc) boolEntry(dict types.Dict, key string) bool {
	if v, ok := d.deref(dict[key]).(types.Boolean); ok {
		return v.Value()
	}
	return false
}

func (d *doc) dictEntry(dict types.Dict, key string) types.Dict {
	if v, ok := d.deref(dict[key]).(types.Dict); ok {
		return v
	}
	return nil
}

// hasEntry reports whether dict declares key at all. /SMask and /Mask are read
// this way on purpose: an indirect reference, a stream and an array are all
// "there is a mask", and dereferencing to learn which would say nothing more.
func hasEntry(dict types.Dict, key string) bool {
	_, ok := dict.Find(key)
	return ok
}

func (d *doc) arrayEntry(dict types.Dict, key string) types.Array {
	if v, ok := d.deref(dict[key]).(types.Array); ok {
		return v
	}
	return nil
}
