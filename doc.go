// Package byblos is a pure-Go PDF pipeline for scanned documents: no cgo, no
// shared libraries, no subprocesses.
//
// Byblos extracts rather than renders. Scanned pages are overwhelmingly one
// page-covering image per page, which requires extraction rather than
// rendering; pages that are not are detected and reported with
// ErrNotSingleRaster rather than guessed at. See docs/superpowers/specs.
//
// It does also render, at thumbnail fidelity and no further: RenderPage
// rasterises a page to a given long edge, recognisable rather than faithful.
// It is a separate tool, not a fallback — it does not rescue a page
// ExtractPageRaster refuses, and ErrNotSingleRaster still means what it
// says. The faithful renderer that would rescue those pages is the one
// FUTURE.md defers under the "render" capability, and it is not this.
//
// Licensing: Byblos is Apache-2.0 and is reimplemented from format
// specifications — principally ISO 32000-1:2008 — and from the documented
// behaviour of the tools it replaces. It is NOT a port of OCRmyPDF, which is
// MPL-2.0 file-level copyleft; no OCRmyPDF source is consulted or translated.
// See NOTICE.
package byblos
