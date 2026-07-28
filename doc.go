// Package byblos is a pure-Go PDF pipeline for scanned documents: no cgo, no
// shared libraries, no subprocesses.
//
// Byblos does not render PDFs. Scanned pages are overwhelmingly one
// page-covering image per page, which requires extraction rather than
// rendering; pages that are not are detected and reported with
// ErrNotSingleRaster rather than guessed at. See docs/superpowers/specs.
//
// Licensing: Byblos is Apache-2.0 and is reimplemented from format
// specifications — principally ISO 32000-1:2008 — and from the documented
// behaviour of the tools it replaces. It is NOT a port of OCRmyPDF, which is
// MPL-2.0 file-level copyleft; no OCRmyPDF source is consulted or translated.
// See NOTICE.
package byblos
