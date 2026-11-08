package pdfdoc

import "github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

// CatalogInfo is what the document catalog says about signatures and dynamic
// XFA, the two inputs OCRmyPDF refuses (_pipeline.py validate_pdfinfo_options).
type CatalogInfo struct {
	// HasSignature: /AcroForm /SigFlags has bit 1 (SignaturesExist) set.
	HasSignature bool
	// NeedsRendering: the catalog's /NeedsRendering is true (dynamic XFA).
	NeedsRendering bool
}

// Catalog reads the catalog flags from the context Open already parsed.
func (d *doc) Catalog() CatalogInfo {
	cat, err := d.ctx.XRefTable.Catalog()
	if err != nil || cat == nil {
		return CatalogInfo{}
	}
	var info CatalogInfo
	if b, ok := d.deref(cat["NeedsRendering"]).(types.Boolean); ok {
		info.NeedsRendering = bool(b)
	}
	if af := d.dictEntry(cat, "AcroForm"); af != nil {
		if v, ok := d.deref(af["SigFlags"]).(types.Integer); ok {
			info.HasSignature = int(v)&1 != 0
		}
	}
	return info
}
