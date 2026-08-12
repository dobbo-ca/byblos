# Font substitution at thumbnail size (byb-8b9.6)

**Status:** measured 2026-08-07. Answers byb-8b9.6, which blocks renderer stage
4f — the only stage that actually replaces `pdftoppm`.

**Answer: option (a).** Bundle metric-compatible open fonts. Option (b),
synthesise from metrics only, is trivially distinguishable at 400px and stays
distinguishable down to about 200px.

## 1. The question

47.8% of page-1 font uses across the corpus embed no font program. The PDF names
a font and expects the consumer to supply glyphs. byb-8b9.6 lists four options
and says the choice between (a) and (b) is settled by one cheap measurement: at
400px, can a reader tell a synthesised face from a substituted real one?

## 2. Method

Every arm goes through the **same rasteriser** (poppler 26.06.0), the **same
page**, at the **same size**. The only variable is which face fontconfig hands
poppler for a font the PDF does not embed.

This constraint is the whole design. Rendering arm (a) with poppler and arm (b)
with a Go rasteriser would have compared two rasterisers — different hinting,
different antialiasing — and attributed the difference to the font strategy.

The face is controlled by starvation: `FONTCONFIG_FILE` points at a config whose
only `<dir>` holds exactly one font, so every substitution resolves to it.

| arm | face fontconfig serves |
|---|---|
| `real` | host fontconfig, untouched — on macOS, genuine Helvetica |
| `box-filled` | solid rectangle, x-height, 10% side bearing |
| `box-hollow` | outlined rectangle, 60-unit wall |
| `box-narrow` | solid rectangle, 32.4% side bearing — ink-matched to `real` |
| `box-short` | solid rectangle, 230-unit height — ink-matched to `real` |
| `open-liberation` | Liberation Sans, SIL OFL 1.1 |

The box fonts come from `tools/fontmeasure/boxfont.go`, a fork of
`internal/glyphless/gen.go`. That generator already emits a valid sfnt carrying
the Helvetica width table, so arm (b) inherits exactly the advances arm (a) uses
and the comparison isolates glyph shape rather than layout. The only substantive
change is `buildLocaAndGlyf`, from an empty outline to a real one.

"Ink" below is mean darkness: 0 is a white page, 1 is solid black.

## 3. Result

`govdocs1/200614.pdf` page 1 — 9 non-embedded fonts, 4,565 characters, no image.

| arm | ink | vs real | pixels differing >10% |
|---|---|---|---|
| real | .0751 | — | — |
| box-filled | .1709 | ×2.28 | 25.2% |
| box-hollow | .0798 | ×1.06 | 19.7% |
| box-narrow | .0775 | ×1.03 | 20.3% |
| box-short | .0771 | ×1.03 | 20.8% |
| **open-liberation** | .0899 | ×1.20 | **12.0%** |

`govdocs1/150560.pdf` page 1 reproduces the ordering: every box arm lands
between 22.7% and 29.4%, `open-liberation` at 8.6%.

### 3.1 Ink coverage is not the discriminator — shape is

`box-hollow`, `box-narrow` and `box-short` all match real ink coverage to within
6%, and all three remain obvious at a glance. Filled reads as redaction, hollow
as tofu, narrow as a barcode, short as ruled strikethrough. None reads as text.

This matters because it kills the obvious objection to the result. Option (b)
did not lose by being drawn too dark; darkness was equalised and it still lost.

### 3.2 Size threshold

`200614.pdf`, arm `box-narrow`, ink matched:

| size | pixels differing >10% | verdict |
|---|---|---|
| 400px | 20.3% | obviously wrong |
| 200px | 14.8% | obviously wrong; the title reads as a bar |
| 100px | 4.4% | effectively indistinguishable |

The threshold sits between 100px and 200px. The stated consumer is 400px, well
above it.

### 3.3 Arm (a) with an open font

`open-liberation` reads as an ordinary page and is not distinguishable from
`real` at 400px without a side-by-side pixel comparison. Its 12.0% is sub-pixel
glyph-shape difference, not a structural one. So option (a) does not depend on
the host having genuine Helvetica, which is what makes it viable on a Linux
runner where `real` does not exist.

Evidence: `2026-08-07-font-substitution/{real,open-liberation,box-narrow,box-filled}.png`.

## 4. Population

Sampled every 7th manifest row: 811 of 5,672 sample files.

- 437/811 (**53.9%**) have at least one non-embedded page-1 font.
- 272/437 of those are pure vector text — page 1 carries no image at all.
- So **272/811 (33.5%)** of documents show visible vector text in a
  non-embedded font on page 1. That is the population option (b) degrades.

The 165 documents dropped by the second filter are scans. They declare
non-embedded fonts and paint no glyph, so the font strategy cannot show up in
their pixels either way.

## 5. Licences, against section 10

Section 10 prohibits translating OCRmyPDF, which is MPL-2.0. It does not speak
to fonts. The constraint on option (a) is Apache-2.0 compatibility:

| font | licence | fits beside Apache-2.0 |
|---|---|---|
| Liberation | SIL OFL 1.1 | yes, cleanly |
| Go fonts | BSD-3 | yes, cleanly |
| URW/Nimbus | AGPL + font exception | yes, but the exception carries the weight |

Liberation is the measured one and is what pdf.js ships for the same purpose.

## 6. What this does not settle

- Which faces to bundle for the full standard 14, and at what repo size cost.
- Bold, italic and monospace arms. Only the regular sans path was measured.
- Non-Latin coverage. Every arm here covers `0x20`–`0x7e` only.

## 7. Two instrument failures

Both produced a confident wrong answer before being caught. They are recorded
because each is invisible in the output it produces.

1. **macOS has no `timeout` and no `gtimeout`.** Wrapping poppler in
   `timeout 10 … || continue` failed on every document and wrote an empty result
   file that read as "the corpus has no non-embedded fonts".
2. **A page can declare non-embedded fonts and paint no glyph.** The first
   worst-case pick, `850793.pdf`, scored 12,030 page-1 characters and 12/12
   non-embedded fonts, and rendered **byte-identical** under a box font. It is a
   scanned newspaper whose text is an invisible OCR layer. Selecting on "has
   text" measured nothing.

`render.sh` now fails loudly when an arm is byte-identical to `real`, and
`filter.sh` drops the scans. Keep both.
