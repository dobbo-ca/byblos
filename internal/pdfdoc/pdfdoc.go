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
//     Every call here passes defaultConfig().
//   - model.NewDefaultConfiguration builds pdfcpu's package-level default
//     LAZILY, on first use, and writes it with no synchronisation, so two
//     goroutines opening documents at once race inside pdfcpu itself.
//     defaultConfig forces that init exactly once; see its comment (byb-a5z).
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
//   - pdfcpu's own page tree walk falls into that same trap: it reads /Kids with
//     ArrayEntry, so a /Pages node holding an indirect /Kids looks childless and
//     PageDict returns that node as the dictionary of every page, with no error.
//     Open repairs the tree before anything walks it; see normalizePageTree.
package pdfdoc

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

var configOnce sync.Once

// defaultConfig returns a FRESH pdfcpu configuration, having forced pdfcpu's
// lazy global initialisation to happen exactly once.
//
// Two separate facts make this the shape it is, and getting either wrong
// reintroduces a race (byb-a5z).
//
// First, the init is lazy and unsynchronised. model.NewDefaultConfiguration
// reaches EnsureDefaultConfigAt -> ensureConfigFileAt -> parseConfigFile, which
// WRITES a pdfcpu package-level variable on the first call in the process while
// a concurrent caller READS it. Measured with -race against pdfcpu v0.13.0: 9
// data races from 8 goroutines calling Open at once, and none once this
// serialises the first call.
//
// Second, the configuration itself is NOT shareable. pdfcpu mutates the
// *model.Configuration it is handed -- it carries per-operation state -- so
// returning one cached instance would trade the init race for a worse one on
// every field. Each caller still gets its own; only the global init is
// serialised.
func defaultConfig() *model.Configuration {
	configOnce.Do(func() { _ = model.NewDefaultConfiguration() })
	return model.NewDefaultConfiguration()
}

// ErrUnsupportedCodec reports an image stream whose compression filter pdfcpu
// will not render. It exists so that pdfcpu's nil-reader return becomes an
// error at this seam instead of a nil dereference in the caller.
//
// It is NOT returned for JBIG2 or JPX: those come back as real bytes with a
// file type naming the codec, and deciding what to do about them is extract.go's
// job, not this package's.
var ErrUnsupportedCodec = errors.New("byblos/pdfdoc: image codec cannot be rendered")

// ErrMalformed reports a document pdfcpu could not parse without panicking.
//
// pdfcpu indexes unchecked in several parsers — skipTJ walks off an empty
// operand slice on a malformed TJ array. Its own fault.Catch recovers only its
// own panic type, so nothing below this package stops it.
//
// skipTJ is reached from pdfcpu's parseContent, which runs only when PageDict
// is asked to consolidate resources. Page and Annots no longer ask (byb-ged),
// so that crash no longer arrives from what looks like a dictionary read — but
// Optimize still reaches it, because api.Optimize calls PageDict(i, true)
// itself.
//
// A library for processing archives cannot die on one damaged file out of five
// thousand, which is what this cost before byb-avp: a whole run lost, and not
// even a counter left behind to say a page had been skipped. Recovering at
// this seam is what makes a malformed page an outcome the caller can count
// rather than an exit. It is the same boundary ErrUnsupportedCodec sits on —
// pdfcpu's misbehaviour becomes a Byblos error here or nowhere.
var ErrMalformed = errors.New("byblos/pdfdoc: malformed document")

// catchPanic converts a panic from pdfcpu into *err.
//
// Deferred with a named return, so it can only turn a crash into an error and
// never mask one that was already being returned.
func catchPanic(what string, err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("%w: %s: %v", ErrMalformed, what, r)
	}
}

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
	Decode        bool // /Decode is present: the samples need a remap

	// DecodeArray is /Decode's elements when every one of them is a number,
	// and nil otherwise — either the entry is absent, or it is present in a
	// shape this cannot read.
	//
	// Decode and this field answer different questions and a caller must pick
	// the one it means. "Is there a remap at all" is Decode, and an entry whose
	// numbers could not be read is still a remap; optimize.go's eligibility
	// test reads it that way. "What is the remap" is this, and nil means there
	// is none to be had — never that the samples pass through unchanged.
	//
	// The values are as written. This does not supply the default array for an
	// absent entry, because the default depends on the colour space and the
	// bit depth (ISO 32000-1 table 39) and ColorSpace is deliberately not
	// resolved here.
	DecodeArray []float64

	// Filter is the image codec the stream declares: /Filter when it is a
	// name, the LAST entry when it is an array, and "" when /Filter is absent
	// (an unencoded stream) or is neither of those shapes.
	//
	// Last, not first, because a filter array is the DECODE order (ISO 32000-1
	// section 7.4): in /Filter [/ASCII85Decode /JBIG2Decode] the ASCII85 stage
	// only unwraps the transport encoding and the image codec is JBIG2Decode.
	// Reading the first entry would report a chained JBIG2 image as ASCII85 and
	// undercount exactly the class this field exists to find.
	//
	// It is a declaration, like everything else here — RawImage still reports
	// what pdfcpu actually made of the bytes. The two disagree when a file lies
	// about its own encoding.
	Filter string

	// ColorSpace is /ColorSpace when it is a plain name ("DeviceGray",
	// "DeviceRGB", "DeviceCMYK"), and "" when it is an array or an indirect
	// reference -- Indexed, ICCBased, Separation, DeviceN. Recompression uses
	// it as an eligibility test, not as colour management: replacing a
	// /Separation with /DeviceGray inverts the ink convention (in Separation,
	// 0 is no ink and therefore white; in DeviceGray, 0 is black), so
	// anything that is not a device name is left alone.
	ColorSpace string
}

// Page is one page's geometry, content, and resource scope.
type Page struct {
	Index    int // 1-based
	MediaBox Rect
	CropBox  Rect // equals MediaBox when the page declares none
	Rotate   int
	Content  []byte // decoded, concatenated
	Scope    int    // resource scope handle for content.Env
	// MediaBoxDefaulted reports that the document declared no /MediaBox
	// anywhere in this page's inheritance chain, so MediaBox is the US Letter
	// convention rather than anything the file states. See Page(), and byb-8ly
	// for the nine govdocs1 files that made it necessary.
	MediaBoxDefaulted bool
}

// defaultPageWidthPt, defaultPageHeightPt are US Letter, the box every reader
// falls back to for a page that declares none. Poppler reports exactly this for
// the byb-8ly documents, which is a convention it applies and not a fact it
// read out of them.
const (
	defaultPageWidthPt  = 612
	defaultPageHeightPt = 792
)

// Doc is a parsed PDF and the seam Byblos keeps between itself and pdfcpu.
type Doc interface {
	PageCount() int
	Page(n int) (*Page, error)
	// Annots returns page n's annotations. They are not part of Page because
	// nothing in classification reads them yet; see annots.go.
	Annots(n int) ([]Annot, error)
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
	// ReplaceImage and Write are the write half; see write.go. They are on the
	// same interface because writing requires the context Open normalized, so
	// there is no way to reach them from a document Byblos did not read.
	ReplaceImage(id int, img EncodedImage) error
	Write(w io.Writer) error
	// AddFontResource and AppendContent are the invisible-text write half; see
	// text.go. Same reason as ReplaceImage/Write: they need Open's normalized
	// context.
	AddFontResource(n int, f TrueTypeFont) (name string, err error)
	AppendContent(n int, ops []byte) error
}

type doc struct {
	ctx      *model.Context
	scopes   []scope
	images   map[int]ImageInfo
	streams  map[int]*types.StreamDict    // image stream dicts, keyed like images
	refs     map[int]types.IndirectRef    // xref identity of those streams, for writing
	nextID   int                          // synthetic ids for direct (non-indirect) image objects
	fontRefs map[string]types.IndirectRef // embedded fonts, keyed by TrueTypeFont.BaseFont
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
func Open(rs io.ReadSeeker) (d Doc, err error) {
	defer catchPanic("open", &err)
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: seek: %w", err)
	}
	ctx, err := api.ReadContext(rs, defaultConfig())
	if err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: read: %w", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: page count: %w", err)
	}
	if root, err := ctx.XRefTable.Pages(); err == nil && root != nil {
		normalizePageTree(ctx.XRefTable, *root, map[int]bool{})
	}
	return &doc{
		ctx:     ctx,
		images:  map[int]ImageInfo{},
		streams: map[int]*types.StreamDict{},
		refs:    map[int]types.IndirectRef{},
		nextID:  -1,
	}, nil
}

// normalizePageTree replaces an indirect /Kids with the array it refers to,
// throughout the page tree.
//
// ISO 32000-1 section 7.3.10 allows any object that is not a stream to be
// indirect, and Google Books PDF Converter writes /Kids that way. pdfcpu reads
// /Kids with types.Dict.ArrayEntry, which returns nil for an indirect reference
// instead of following it, so such a node has no children as far as the walk is
// concerned. The walk treats a childless node as the page it was looking for:
// PageDict then returns the /Pages node itself for every page number, with no
// error, and that dictionary has neither /MediaBox nor /Contents.
//
// Repairing at Open rather than reacting to the symptom in Page is deliberate.
// A missing /MediaBox is only how the damage shows when the bad node is the
// root; an intermediate /Pages node that happens to carry inheritable
// attributes yields a page dict that looks entirely plausible and is silently
// the wrong page.
//
// Rewriting the parsed dictionary is enough, and it is confined to this
// in-memory context: pdfcpu re-reads the object from the same xref table on
// every call, and Byblos never writes this context back out.
func normalizePageTree(xt *model.XRefTable, ref types.IndirectRef, seen map[int]bool) {
	objNr := ref.ObjectNumber.Value()
	if seen[objNr] {
		// A cycle, or a node reachable twice. pdfcpu rejects both when it walks
		// the tree; this pass only has to avoid looping before it gets there.
		return
	}
	seen[objNr] = true

	d, err := xt.DereferenceDict(ref)
	if err != nil || d == nil {
		return
	}
	kids, ok := d.Find("Kids")
	if !ok {
		return // a leaf page
	}
	arr, err := xt.DereferenceArray(kids)
	if err != nil || arr == nil {
		return
	}
	d["Kids"] = arr
	for _, k := range arr {
		if kr, ok := k.(types.IndirectRef); ok {
			normalizePageTree(xt, kr, seen)
		}
	}
}

func (d *doc) PageCount() int { return d.ctx.PageCount }

func (d *doc) Page(n int) (p *Page, err error) {
	defer catchPanic(fmt.Sprintf("page %d", n), &err)
	if n < 1 || n > d.ctx.PageCount {
		return nil, fmt.Errorf("byblos/pdfdoc: page %d out of range 1..%d", n, d.ctx.PageCount)
	}
	xt := d.ctx.XRefTable
	// consolidateRes=false, for the reasons text.go's AddFontResource already
	// gives, plus one it could not see (byb-ged). true does two things, and
	// Byblos wants neither:
	//
	//   - It PRUNES the returned resource dict to the names pdfcpu found in the
	//     page's OWN content stream, never descending into a form's content to
	//     do it (pdfcpu's own TODO on consolidateResourcesWithContent). This
	//     dict becomes Page.Scope, so an image only a Form XObject paints is
	//     deleted from the scope the form resolves through, and Inspect reports
	//     a page with fewer images and no error at all. Measured: govdocs1
	//     500973.pdf page 1 reported 1 image where pdfimages lists 5.
	//   - It rejects the page outright when the content names a resource
	//     category the dict does not declare ("missing required resource
	//     subdict"), and, via its own content parse, on "corrupt BI expression"
	//     and "corrupt page content". Because Inspect walks every page, that
	//     loses the WHOLE document: 8 of 4,840 govdocs1 files, all of which
	//     poppler reads.
	//
	// Neither loss buys anything, because pdfcpu is parsing bytes Byblos parses
	// itself: content.Walk produces its own, better-located errors on the same
	// documents. And nothing here reads a page-level colour space — ImageInfo
	// takes /ColorSpace off the image XObject's own dictionary — so the subdict
	// this used to insist on cannot change a number Inspect reports.
	//
	// What false gives up is pdfcpu's non-standard MERGE of a page's own
	// /Resources with an ancestor /Pages node's. ISO 32000-1 7.7.3.4 makes
	// /Resources inheritable, not additive: a page that declares one gets its
	// own, and poppler agrees. Measured over the same 4,840 documents, no file
	// changed answer because of it.
	pd, _, inh, err := xt.PageDict(n, false)
	if err != nil {
		return nil, fmt.Errorf("byblos/pdfdoc: page %d dict: %w", n, err)
	}
	if pd == nil || inh == nil {
		return nil, fmt.Errorf("byblos/pdfdoc: page %d has no dictionary", n)
	}

	// A page with no /MediaBox anywhere in its inheritance chain is malformed:
	// ISO 32000-1 7.7.3.3 makes it a required inheritable attribute. Byblos used
	// to refuse the whole document, and byb-8ly measured what that cost: nine of the
	// 4,840 govdocs1 sample files, every one PDF 1.0, none using object streams, with
	// the string "MediaBox" absent from the file's bytes altogether. Poppler
	// reads all 9.
	//
	// It defaults instead, to the US Letter box poppler defaults to, and SAYS SO
	// through MediaBoxDefaulted. That flag is not decoration: 612x792 is a
	// convention, not a measurement of the document, and a caller deciding
	// whether a raster covers the page is entitled to know its page box was
	// supplied rather than read. Refusing nine readable files to avoid stating
	// a convention is the worse trade for an archive; stating one silently is
	// worse than either.
	//
	// The word "nine" is spelled out on purpose: byb-a20's pin scans this tree
	// for a digit followed by "readable documents" and reads it as a claim
	// about the CORPUS, which this is not.
	defaulted := inh.MediaBox == nil
	mb := inh.MediaBox
	if defaulted {
		mb = types.RectForDim(defaultPageWidthPt, defaultPageHeightPt)
	}

	p = &Page{
		Index:             n,
		MediaBox:          rectOf(mb),
		MediaBoxDefaulted: defaulted,
		Rotate:            ((inh.Rotate % 360) + 360) % 360,
		Scope:             d.addScope(inh.Resources, -1),
	}
	p.CropBox = p.MediaBox
	if inh.CropBox != nil {
		p.CropBox = rectOf(inh.CropBox)
	}

	// A page with no /Contents is legal and empty, and so is a page whose
	// /Contents is there and resolves to zero bytes: an ordinary blank page,
	// which is a duplex scan's back side, a section separator, or a form page
	// left deliberately empty. pdfcpu reports both with model.ErrNoContent.
	//
	// Guarding on the presence of the KEY alone -- which is all this did before
	// byb-cqs -- makes the second case an error and, because Inspect walks every
	// page, fails the WHOLE document over one page that contains nothing.
	// byb-uxb measured that against poppler over 200 govdocs1 files: six of the
	// seven disagreements were this, 3% of the sample lost outright.
	if _, ok := pd.Find("Contents"); ok {
		c, err := xt.PageContent(pd, n)
		switch {
		case errors.Is(err, model.ErrNoContent):
			if err := d.verifyEmptyContent(pd["Contents"]); err != nil {
				return nil, fmt.Errorf("byblos/pdfdoc: page %d content: %w", n, err)
			}
		case err != nil:
			return nil, fmt.Errorf("byblos/pdfdoc: page %d content: %w", n, err)
		default:
			p.Content = c
		}
	}
	return p, nil
}

// verifyEmptyContent reports the distinction pdfcpu does not: whether a page
// that came back as model.ErrNoContent is BLANK or BROKEN.
//
// PageContent returns that sentinel when the page's content streams
// concatenate to zero bytes, which is exactly what a legal blank page looks
// like. But under the relaxed validation mode Open uses, decodeContentStream
// swallows a "flate: corrupt input before offset" failure -- it logs "skipped"
// and returns nil, leaving that stream's content empty -- so a shredded
// content stream arrives here as the identical sentinel and the identical zero
// bytes. Keying the blank-page decision on the sentinel alone would trade a
// false failure for a silent one, and a page reported as valid and empty when
// its content did not decode is the worse of the two.
//
// So the streams are decoded again here, on copies, purely to read the error
// pdfcpu discarded. Their bytes are of no interest: PageContent has already
// established there are none.
func (d *doc) verifyEmptyContent(contents types.Object) error {
	xt := d.ctx.XRefTable
	o, err := xt.Dereference(contents)
	if err != nil {
		return err
	}
	// /Contents is one stream or an array of them (ISO 32000-1 table 30).
	objs := types.Array{o}
	if arr, ok := o.(types.Array); ok {
		objs = arr
	}
	for _, obj := range objs {
		sd, err := xt.Dereference(obj)
		if err != nil {
			return err
		}
		// A null entry, or a shape PageContent already rejected with an error
		// other than ErrNoContent. Neither is this function's to judge.
		s, ok := sd.(types.StreamDict)
		if !ok {
			continue
		}
		if err := s.Decode(); err != nil {
			return err
		}
	}
	return nil
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
		csName := ""
		if raw, ok := sd.Dict.Find("ColorSpace"); ok {
			if n, ok := raw.(types.Name); ok {
				csName = string(n)
			}
		}
		d.images[id] = ImageInfo{
			Name:        name,
			ObjNr:       id,
			Width:       d.intEntry(sd.Dict, "Width"),
			Height:      d.intEntry(sd.Dict, "Height"),
			BPC:         d.intEntry(sd.Dict, "BitsPerComponent"),
			ImageMask:   d.boolEntry(sd.Dict, "ImageMask"),
			SMask:       hasEntry(sd.Dict, "SMask"),
			Mask:        hasEntry(sd.Dict, "Mask"),
			Decode:      hasEntry(sd.Dict, "Decode"),
			DecodeArray: d.numberArray(sd.Dict, "Decode"),
			Filter:      d.filterName(sd.Dict),
			ColorSpace:  csName,
		}
		// Keep the stream dictionary. RawImage renders from this rather than
		// asking pdfcpu which objects a page uses, because that answer comes
		// from the optimize pass, which deduplicates identical rasters.
		d.streams[id] = sd
		// And its xref identity, which ReplaceImage needs to write the
		// substituted stream back: sd is a copy, not a handle (see write.go).
		if r, ok := indirectRefOf(obj); ok {
			d.refs[id] = r
		}
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
		var bbox *content.Box
		if arr := d.arrayEntry(sd.Dict, "BBox"); len(arr) == 4 {
			var vals [4]float64
			ok := true
			for i := 0; i < 4; i++ {
				v, vok := d.number(arr[i])
				if !vok {
					ok = false
					break
				}
				vals[i] = v
			}
			if ok {
				bbox = &content.Box{LLX: vals[0], LLY: vals[1], URX: vals[2], URY: vals[3]}
			}
		}
		// A form without its own /Resources inherits the enclosing resource
		// dictionary (ISO 32000-1 section 8.10.2), which the scope's parent
		// chain provides.
		formScope := d.addScope(d.dictEntry(sd.Dict, "Resources"), sc)
		return content.XObject{Content: sd.Content, Matrix: m, Scope: formScope, BBox: bbox}, true
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
	if r, ok := indirectRefOf(o); ok {
		return r.ObjectNumber.Value()
	}
	id := d.nextID
	d.nextID--
	return id
}

// indirectRefOf reports the indirect reference o is, if it is one. pdfcpu
// yields both the value and the pointer form depending on the parse path.
func indirectRefOf(o types.Object) (types.IndirectRef, bool) {
	switch v := o.(type) {
	case types.IndirectRef:
		return v, true
	case *types.IndirectRef:
		return *v, true
	}
	return types.IndirectRef{}, false
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

// filterName reports the image codec dict declares; see ImageInfo.Filter for
// why an array yields its last entry rather than its first.
func (d *doc) filterName(dict types.Dict) string {
	switch v := d.deref(dict["Filter"]).(type) {
	case types.Name:
		return string(v)
	case types.Array:
		if len(v) == 0 {
			return ""
		}
		if n, ok := d.deref(v[len(v)-1]).(types.Name); ok {
			return string(n)
		}
	}
	return ""
}

// numberArray reads key as an array of numbers, and returns nil unless every
// element is one. All-or-nothing on purpose: a partly-read array is a remap a
// caller cannot apply, and returning it with holes in it invites applying it
// anyway.
func (d *doc) numberArray(dict types.Dict, key string) []float64 {
	arr := d.arrayEntry(dict, key)
	if len(arr) == 0 {
		return nil
	}
	out := make([]float64, len(arr))
	for i, o := range arr {
		v, ok := d.number(o)
		if !ok {
			return nil
		}
		out[i] = v
	}
	return out
}

func (d *doc) arrayEntry(dict types.Dict, key string) types.Array {
	if v, ok := d.deref(dict[key]).(types.Array); ok {
		return v
	}
	return nil
}
