package pdfdoc

// The write half of the pdfdoc seam (byb-0he).
//
// Byblos' encoders produce encoded bytes; this is what puts them back into a
// document. The mechanism is deliberately narrow: replace an image XObject's
// stream payload and the dictionary entries that describe it, then serialize
// the same context that was read. pdfcpu's writeStream (writeObjects.go:465)
// emits StreamDict.Raw verbatim, so placement, the page tree, annotations and
// every untouched object survive without being re-derived.
//
// Three pdfcpu behaviours make this less obvious than it looks. All three are
// verified against v0.13.0 and each one fails silently or fatally if ignored:
//
//   - XRefTable.DereferenceStreamDict returns a pointer to a COPY. It ends in
//     `sd, ok := entry.Object.(types.StreamDict); return &sd`, and a type
//     assertion on an interface copies the value. The embedded Dict is a map,
//     so dictionary edits do reach the table, but Raw, StreamLength and
//     Content do not. Mutating the pointer alone writes the new dictionary
//     over the OLD pixels — a document that reads back cleanly and is wrong.
//     replaceStream therefore assigns the whole StreamDict back to the entry.
//
//   - writeStream checks len(Raw) against *StreamDict.StreamLength and fails
//     the write if they disagree. /Length in the dictionary is a separate
//     field, written from the dictionary. Both must be updated.
//
//   - The context must come from Open, not from a bare api.ReadContext.
//     normalizePageTree is load-bearing for writing, not just for reading: on
//     the 'indirect-kids' corpus document a bare read-then-write yields 594
//     bytes and zero pages, with a nil error from WriteContext and a clean
//     re-read. Because Write is a method on the doc Open returns, that is
//     structural here rather than a rule to remember.
//
// Not a constraint, despite byb-0he saying so: api.OptimizeContext does NOT
// cause that loss. Measured 2026-07-31 over the 2x2 --
//
//	                  no Optimize      + OptimizeContext
//	bare ReadContext  594 B, 0 pages   594 B, 0 pages
//	pdfdoc.Open       2901 B, 2 pages  2895 B, 2 pages
//
// The single factor is normalizePageTree. Write still does not optimize, but
// because that is not this seam's job -- see byb-b5 for the size policy.

import (
	"fmt"
	"io"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// ColorSpace is the colour space of a substituted image.
//
// Name is a device space ("DeviceGray", "DeviceRGB", "DeviceCMYK") or
// "Indexed", in which case Base, HiVal and Lookup describe the palette. Indexed
// is here because a quantized image is the shape B3 produces, and a seam that
// only spoke DeviceGray would have to be reopened for it (byb-0he).
type ColorSpace struct {
	Name string

	// Indexed only.
	Base   string // the base space the palette entries are expressed in
	HiVal  int    // the highest palette index; the palette has HiVal+1 entries
	Lookup []byte // (HiVal+1) * components(Base) bytes
}

// DecodeParms is the subset of /DecodeParms Byblos' encoders emit.
//
// A zero Predictor means no predictor, which is also how PDF reads an absent
// /Predictor (ISO 32000-1 table 10 gives it default 1, "no prediction").
type DecodeParms struct {
	Predictor        int
	Colors           int
	BitsPerComponent int
	Columns          int
}

// EncodedImage is a fully encoded image ready to be stored verbatim.
//
// Data is the ENCODED stream payload: whatever /Filter names has already been
// applied to it. Nothing here compresses, and nothing here checks that Data
// actually decodes to Width x Height samples — that is the encoder's contract.
type EncodedImage struct {
	Width, Height int
	BPC           int // /BitsPerComponent
	ColorSpace    ColorSpace
	Filter        string // e.g. "JBIG2Decode", "FlateDecode", "DCTDecode"
	DecodeParms   *DecodeParms
	Data          []byte
}

func componentsOf(space string) (int, bool) {
	switch space {
	case "DeviceGray":
		return 1, true
	case "DeviceRGB":
		return 3, true
	case "DeviceCMYK":
		return 4, true
	}
	return 0, false
}

// object renders the colour space as the PDF object /ColorSpace takes.
func (cs ColorSpace) object() (types.Object, error) {
	if cs.Name == "Indexed" {
		n, ok := componentsOf(cs.Base)
		if !ok {
			return nil, fmt.Errorf("indexed base %q is not a device colour space", cs.Base)
		}
		if cs.HiVal < 0 || cs.HiVal > 255 {
			return nil, fmt.Errorf("indexed hival %d is outside 0..255", cs.HiVal)
		}
		if want := (cs.HiVal + 1) * n; len(cs.Lookup) != want {
			return nil, fmt.Errorf("indexed lookup is %d bytes, want %d for hival %d over %s",
				len(cs.Lookup), want, cs.HiVal, cs.Base)
		}
		return types.Array{
			types.Name("Indexed"),
			types.Name(cs.Base),
			types.Integer(cs.HiVal),
			types.NewHexLiteral(cs.Lookup),
		}, nil
	}
	if _, ok := componentsOf(cs.Name); !ok {
		return nil, fmt.Errorf("colour space %q is not supported", cs.Name)
	}
	return types.Name(cs.Name), nil
}

func (img EncodedImage) validate() error {
	if img.Width <= 0 || img.Height <= 0 {
		return fmt.Errorf("dimensions %dx%d are not positive", img.Width, img.Height)
	}
	switch img.BPC {
	case 1, 2, 4, 8, 16:
	default:
		return fmt.Errorf("bits per component %d is not one of 1, 2, 4, 8, 16", img.BPC)
	}
	if img.Filter == "" {
		return fmt.Errorf("no filter named")
	}
	if len(img.Data) == 0 {
		return fmt.Errorf("no data")
	}
	if _, err := img.ColorSpace.object(); err != nil {
		return err
	}
	return nil
}

// dict renders the /DecodeParms dictionary, or nil when there is nothing to say.
func (p *DecodeParms) dict() types.Dict {
	if p == nil {
		return nil
	}
	d := types.Dict{}
	for k, v := range map[string]int{
		"Predictor":        p.Predictor,
		"Colors":           p.Colors,
		"BitsPerComponent": p.BitsPerComponent,
		"Columns":          p.Columns,
	} {
		if v != 0 {
			d[k] = types.Integer(v)
		}
	}
	if len(d) == 0 {
		return nil
	}
	return d
}

// ReplaceImage substitutes the encoded bytes and dictionary of the image
// XObject that XObject previously resolved as id.
//
// The substitution is in memory; Write serializes it. ImageInfo(id) reflects
// the new image afterwards. /Width and /Height are rewritten to img's
// dimensions, which need not match the original raster's — placement is a
// CTM on the unit square, so it is unaffected by the raster shrinking or
// growing underneath it.
//
// It refuses an image carrying /SMask or /Mask. Those describe transparency
// keyed to the samples being replaced, and neither dropping them nor keeping
// them against unrelated pixels is defensible — the caller has to decide, and
// classification already diverts such pages. It also refuses /ImageMask, whose
// dictionary is a different shape (no /ColorSpace, samples are a stencil); no
// corpus document exercises one and pdfcpu cannot extract them at all, so
// supporting it here would be untested code (see corpus.jbig2's note).
func (d *doc) ReplaceImage(id int, img EncodedImage) (err error) {
	defer catchPanic(fmt.Sprintf("replace image %d", id), &err)

	sd, ok := d.streams[id]
	if !ok {
		return fmt.Errorf("byblos/pdfdoc: image %d has not been resolved on this document", id)
	}
	ref, ok := d.refs[id]
	if !ok {
		// ISO 32000-1 section 7.3.8.1: every stream shall be an indirect object.
		// A direct image stream has no xref entry to write back to, and is
		// malformed anyway.
		return fmt.Errorf("byblos/pdfdoc: image %d is a direct object and cannot be replaced", id)
	}
	info := d.images[id]
	switch {
	case info.SMask:
		return fmt.Errorf("byblos/pdfdoc: image %d carries an /SMask", id)
	case info.Mask:
		return fmt.Errorf("byblos/pdfdoc: image %d carries a /Mask", id)
	case info.ImageMask:
		return fmt.Errorf("byblos/pdfdoc: image %d is an /ImageMask stencil", id)
	}
	if err := img.validate(); err != nil {
		return fmt.Errorf("byblos/pdfdoc: replacing image %d: %w", id, err)
	}
	cs, err := img.ColorSpace.object()
	if err != nil {
		return fmt.Errorf("byblos/pdfdoc: replacing image %d: %w", id, err)
	}

	entry, found := d.ctx.XRefTable.FindTableEntry(ref.ObjectNumber.Value(), ref.GenerationNumber.Value())
	if !found || entry == nil {
		return fmt.Errorf("byblos/pdfdoc: image %d has no cross-reference entry", id)
	}

	sd.Dict["Width"] = types.Integer(img.Width)
	sd.Dict["Height"] = types.Integer(img.Height)
	sd.Dict["BitsPerComponent"] = types.Integer(img.BPC)
	sd.Dict["ColorSpace"] = cs
	sd.Dict["Filter"] = types.Name(img.Filter)
	sd.Dict["Length"] = types.Integer(len(img.Data))

	// Entries whose meaning is tied to the samples being replaced. /Decode in
	// particular is not inert: a leftover [1 0] silently inverts the new image.
	parms := img.DecodeParms.dict()
	if parms != nil {
		sd.Dict["DecodeParms"] = parms
	} else {
		delete(sd.Dict, "DecodeParms")
	}
	delete(sd.Dict, "Decode")

	n := int64(len(img.Data))
	sd.Raw = img.Data
	sd.StreamLength = &n
	sd.StreamLengthObjNr = nil
	sd.Content = nil // the decoded cache now describes the old samples
	sd.FilterPipeline = []types.PDFFilter{{Name: img.Filter, DecodeParms: parms}}

	// The copy trap: this, not the mutations above, is what reaches the writer.
	entry.Object = *sd

	info.Width, info.Height, info.BPC = img.Width, img.Height, img.BPC
	d.images[id] = info
	return nil
}

// Write serializes the document, including any substitution ReplaceImage made.
//
// It does not optimize and does not validate. Optimize is byb-b5's, and Open
// deliberately skips validation (see its doc comment); a file Byblos accepted
// on the way in must not be rejected on the way out.
func (d *doc) Write(w io.Writer) (err error) {
	defer catchPanic("write", &err)
	if err := api.WriteContext(d.ctx, w); err != nil {
		return fmt.Errorf("byblos/pdfdoc: write: %w", err)
	}
	return nil
}

// Validate reports whether r parses as a structurally valid PDF: xref offsets
// resolve and the page tree's /Count agrees with its /Kids. It checks
// structure only, not pixel content — a stream whose filter claims a codec
// its bytes do not actually hold still validates, and so does a stream whose
// /Length is wrong but still parses (pdfcpu trusts the declared /Length when
// reading; it does not recompute it from the bytes between `stream` and
// `endstream`).
//
// This exists for BuildPDF (byb-c3o): a hand-rolled writer has no reader of
// its own to round-trip through, and pdfcpu's validator is the independent
// check that the bytes it emits are a PDF at all. The one property this
// cannot check — /Length matching the stream — is instead guaranteed by
// construction in pdfbuild's writer: fillStream writes /Length from
// len(payload) immediately before writing payload itself
// (internal/pdfbuild/pdfbuild.go), so the two cannot diverge without editing
// that one function.
func Validate(r io.ReadSeeker) (err error) {
	defer catchPanic("validate", &err)
	if err := api.Validate(r, model.NewDefaultConfiguration()); err != nil {
		return fmt.Errorf("byblos/pdfdoc: validate: %w", err)
	}
	return nil
}
