# Byblos

A pure-Go PDF pipeline for scanned documents. No cgo, no shared libraries, no
subprocesses.

Byblos replaces the PDF-side tools a scan pipeline normally shells out to —
`ghostscript`, `jbig2enc`, `pngquant`, `poppler`, `img2pdf` — with a Go library
that compiles into your binary.

> Byblos was the port that traded papyrus to Greece; "biblion", and eventually
> "book", comes from its name. This library handles the paper.

**Status:** under active implementation. Inspection, raster extraction, JBIG2
generic-region encoding, the invisible text layer, PDF assembly from images,
structural optimization (including our own Annex F linearizer), and image
recompression (quantization, downsampling, JPEG recompression) all exist and
are exported. See
[the design spec](docs/superpowers/specs/2026-07-27-byblos-design.md) and
[FUTURE.md](FUTURE.md) for what remains.

## What makes it tractable

Byblos does **not** render PDFs. Rendering arbitrary PDF means a content-stream
interpreter and full font rasterization — plausibly more work than an OCR engine.

Scanned pages don't need it. They are, overwhelmingly, one page-covering image per
page: that requires *extraction*, not rendering. Born-digital PDFs shouldn't be
rasterized at all. Pages that are neither are detected and reported
(`ErrNotSingleRaster`) rather than guessed at.

## Design commitments

- **Lossless bitonal compression only.** JBIG2 generic-region coding, not lossy
  symbol matching. Lossy JBIG2 can silently substitute characters in scanned
  documents — the 2013 Xerox scanner defect — and produces output that looks
  *cleaner* while being wrong. See [FUTURE.md](FUTURE.md) for the full reasoning.
- **Capability-based provenance.** Every output records what Byblos could do and
  what each page received, so a later version can identify exactly which stored
  documents would benefit from re-processing — and skip the ones that wouldn't.
- **Policy stays with the caller.** Byblos exposes primitives: inspect, extract,
  compress, stamp, optimize. Preset ladders and validation rules belong to the
  application.

## Relationship to other projects

- [cadmus](https://github.com/dobbo-ca/cadmus) — the OCR engine. Neither library
  imports the other; the application converts between them.
- **OCRmyPDF** — a design reference, not a port target. It is MPL-2.0, and
  translating it would make those files MPL-2.0. It is also *orchestration over
  the very binaries this project removes*, so porting it would not have helped.

## License

Apache-2.0. Reimplemented from format specifications, not ported.
