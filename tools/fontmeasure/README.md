# fontmeasure — the byb-8b9.6 harness

Answers one question with pixels instead of argument: **at thumbnail size, is a
synthesised face distinguishable from a substituted real one?** 47.8% of page-1
font uses across the corpus embed no font program, so the answer decides whether
byblos must bundle font binaries.

Result: **yes, distinguishable at 400px.** See
`docs/superpowers/specs/2026-08-07-font-substitution.md`.

## The design constraint that matters

Every arm goes through the **same rasteriser** (poppler), the **same page**, at
the **same size**. The only variable is which face fontconfig hands poppler when
the PDF names a font it does not embed. Rendering one arm in Go and one in
poppler measures two rasterisers, not two font strategies — do not do that.

Starvation is how the face gets controlled: `FONTCONFIG_FILE` points at a config
whose only `<dir>` holds exactly one font, so every substitution resolves to it.

## Running it

```bash
make fontmeasure                       # builds the box fonts into tools/fontmeasure/faces/
tools/fontmeasure/render.sh <pdf> <outdir> 400
CGO_ENABLED=0 go run tools/fontmeasure/ink.go <outdir>
```

`faces/` is generated and not committed. `make fontmeasure` rebuilds the four
box variants. To reproduce arm (a) you also need one metric-compatible open
font in `faces/` named `open-*.ttf`; the measurement used Liberation Sans
(SIL OFL 1.1), which ships inside pdfjs-dist and is not vendored here.

Corpus selection, for picking pages where the answer can even show up:

```bash
tools/fontmeasure/scan.sh   candidates.tsv   # page-1 fonts with no embedded program
tools/fontmeasure/filter.sh candidates.tsv vector.tsv   # drop scans
```

## Two traps, both of which produced a confident wrong answer first

1. **macOS has no `timeout` and no `gtimeout`.** Wrapping poppler in
   `timeout 10 … || continue` failed on every document and wrote an empty result
   that read as "the corpus has no non-embedded fonts". Check the binary exists
   before wrapping anything in it.
2. **A page can declare non-embedded fonts and paint no glyph at all.** The
   first worst-case pick scored 12,030 page-1 characters and 12/12 non-embedded
   fonts, and rendered byte-identical under a box font: it is a scanned
   newspaper whose text is an invisible OCR layer. `filter.sh` drops these, and
   `render.sh` fails loudly when an arm is byte-identical to `real`. Keep both
   checks.
