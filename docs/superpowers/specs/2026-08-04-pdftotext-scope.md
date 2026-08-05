# `pdftotext`: what replacing it costs, and what to build instead

**Status:** scoping, decision proposed — not yet accepted
**Date:** 2026-08-04
**Bead:** `byb-lez` (parent `byb-js5`, the G1 audit epic)
**Repo:** `dobbo-ca/byblos`
**Also touches:** `dobbo-ca/kleio` (`internal/pipeline`)

`byb-lez` records the question as a binary: *"either byblos grows text
extraction, or Kleio keeps poppler for this one call and G1 is amended to say
so."* It is not binary. Kleio makes **three** `pdftotext` calls, they want three
different things, and two of them need no font decoding at all.

`byb-js5` already establishes that G1-as-an-absolute is refuted; this document
does not re-argue that. It argues about which of the residue is worth building.

---

## 1. Recommendation

**Build nothing font-related yet. Delete two of the three calls, keep the third
on poppler as a named G1 exception, and re-measure before spending a week on
font decoding.**

| Kleio call site | what it actually needs | verdict |
|---|---|---|
| `IsBornDigital` — `compress.go:82`, `ExtractText(ctx, pdf, 5)` | a **character count** over the first five pages, tested against 100 chars/page | **Replace now with `PageInfo.TextChars`.** Measured 99.96% verdict agreement on 4,822 govdocs1 files (§4). Zero new byblos API. |
| OCR read-back — `ocr.go:196`, `ExtractText(ctx, out, 0)` | the words byblos was just handed, plus the pre-existing layer | **Delete, do not reimplement.** In a cadmus/byblos pipeline Kleio already holds these words before it stamps them. Zero new byblos API. |
| the stash — `compress.go:579`, `ExtractText(ctx, in, 0)` | the **set of distinct lowercased alphanumeric tokens** of the original's text layer, ≥3 runes, capped at 100,000 (`gate.go:549` `tokenSet`) | **Keep poppler.** This is the only consumer that needs code→Unicode, and it is not yet established that it fires often enough to pay for it. |

The cut line inside the third row, if it is ever built: **code→Unicode yes,
layout analysis no.** `Coverage` (`gate.go:514`) is set recall over distinct
tokens — it never looks at order — so poppler's `TextOutputDev`-shaped work
(line assembly, column detection, reading order) buys nothing. That deletes the
single largest cost item. What it does *not* delete is word-boundary detection,
which needs the text matrix and glyph advance widths; see §2 C3 for why that
survives the cut.

**What loses.** "Byblos grows a general text extractor" loses on §3: the tail is
open-ended (Type1 `eexec` charstring names, predefined CJK CMaps, twenty years
of accumulated poppler fixes for broken producers) and no consumer asks for the
part that costs the most. "Leave it alone and say nothing" loses because the
design spec still claims poppler is eliminated, which `byb-lez` correctly calls
undefensible.

---

## 2. The gap, capability by capability

Byblos's entire text-related public surface is `PageInfo.TextChars`
(`inspect.go:63`), `TextLayer` (`stamp.go:51`), `PositionedWord`
(`stamp.go:43`), `StampTextLayer` (`stamp.go:166`) and `ErrUnstampableRune`
(`stamp.go:60`). The last four are **write-side only**: they describe text a
caller already recognized. Nothing in the package produces text from a PDF.

| # | capability | byblos today | evidence |
|---|---|---|---|
| C1 | code → Unicode | **nothing** | `walk.go:392,403` add `len(ops[i].Text)` to `TextChars` and discard the bytes. No `/ToUnicode`, `/Differences`, `/Encoding` or CMap reader exists anywhere: the only tree-wide match for those names is `internal/pdfdoc/text.go:108`, which *writes* `/Encoding /WinAnsiEncoding` for the glyphless stamp font. |
| C2 | which font a byte belongs to | **nothing** | `Tf` has no case in the operator switch (`walk.go:315-426`), and `content.Env` (`walk.go:78`) resolves only `XObject` and `ExtGStateOpaque`. The walk cannot name the font in force, so it could not choose an encoding even if it had one. |
| C3 | glyph positions (text matrix, advances) | **nothing** | `gstate` (`walk.go:237-245`) carries `ctm, opaque, tr, lineWidth, fill, stroke, clip`. `Td`, `TD`, `Tm`, `T*`, `TL`, `Tc`, `Tw`, `Tz`, `Ts` are all unhandled keywords whose operands are silently dropped at `walk.go:427`. |
| C4 | word and line segmentation | **nothing** | depends entirely on C3. |
| C5 | reading order / layout reconstruction | **nothing** | depends on C3 and C4. |
| C6 | ligature and diacritic normalisation | **nothing** | no consumer of decoded text exists to normalise. `golang.org/x/text` is already an indirect dependency, so `unicode/norm` is a promotion, not a new module. |
| C7 | RTL and vertical writing | **nothing** | also not asked for by any of the three call sites: `tokenSet` is order-insensitive. |
| C8 | encoding edge cases (symbolic fonts, Type3, broken `/ToUnicode`, subset `gNN` names) | **nothing** | — |

`Tr` (`walk.go:331`) is the only text-state operator tracked, and only to feed
`inksGlyphs` (`walk.go:276`) — the invisible-OCR-layer test used by the divert
decision. It is not a step toward extraction.

`internal/glyphless` is not a head start either: it *generates* a synthetic sfnt
and holds a code-point→GID/width table (`glyphless.go:67,76`) for the font
`StampTextLayer` embeds. It parses nothing.

### What the dependency tree does and does not give

- **`pdfcpu` v0.13.0 has no reusable ToUnicode parser.** The only CMap-reading
  code in it is `usedGIDsFromCMap` (`pkg/pdfcpu/font/fontDict.go:677`),
  unexported and line-oriented: it requires `endcodespacerange` followed by
  `%d beginbfchar`, one `<xxxx>` per line, `endbfchar` alone on a line. It reads
  pdfcpu's *own* output and fails on anything else. Everything else touching
  `/ToUnicode` in pdfcpu writes it or validates its presence.
- **pdfcpu has no encoding tables.** `WinAnsiEncoding`/`MacRomanEncoding`/
  `StandardEncoding` appear only as validated name strings
  (`pkg/pdfcpu/validate/font.go:449-451`). No code→glyph-name array exists.
- **`golang.org/x/image/font/sfnt` helps for embedded TrueType/OpenType.** It
  exposes `GlyphAdvance`, `GlyphName` (post table) and `GlyphIndex(rune)`, and
  `cmap.go:26` shows it understands the `(3,0)` Windows Symbol subtable — so a
  symbolic font's map can plausibly be inverted by probing `GlyphIndex` over
  `U+F000..U+F0FF`. *Unverified:* which subtable sfnt selects when a font
  carries both `(3,1)` and `(3,0)`, and whether a subset font's `post` table is
  format 3.0 (no names at all).
- **sfnt does not parse Type1.** A bare Type1/PFB font program's glyph names sit
  behind `eexec` encryption; nothing in the tree reads them.

---

## 3. Sizing, per capability

Estimates are engineer-days for one focused implementer, tests included, at this
repo's evidenced standard (the walk is divert-critical and every change to it
has needed corpus regression evidence). They are **inference from the code read,
not measured** — no part of this has been prototyped.

### Days

| item | days | why |
|---|---|---|
| `/ToUnicode` CMap parser | 3–5 | `begincodespacerange` / `beginbfchar` / `beginbfrange` / `usecmap`, one- and two-byte codes, array-valued `bfrange` destinations, multi-rune destinations (a ligature maps one code to `"ffi"`), surrogate pairs. The syntax is PostScript-ish and **not** the content-stream syntax, so `internal/content/lexer.go` is a model, not a component. |
| base encoding tables + AGL | 3–5 | `StandardEncoding`, `WinAnsiEncoding`, `MacRomanEncoding`, `PDFDocEncoding`, plus Symbol and ZapfDingbats built-ins; the Adobe Glyph List for glyph-name→Unicode; the `uniXXXX` / `uXXXXXX` / `Cnn` / `gNN` name conventions. Data-heavy, low risk. **Blocker to check first:** the AGL's licence and whether it may be vendored under Apache-2.0 (spec §10 forbids porting MPL source; the AGL is a different question and has not been checked here). |
| `/Differences` overlay | 0.5 | trivial once the AGL exists. |
| Standard-14 metrics | 2–3 | needed for advances when no `/Widths` is present. Pure data. |
| ligature / diacritic normalisation | 2–3 | promote `golang.org/x/text/unicode/norm` to a direct dependency; decide NFC vs NFKC against what `tokenSet` needs. |

### Weeks

| item | weeks | why |
|---|---|---|
| text state machine in `content.Walk` | 1–1.5 | ten new operators, a text matrix and a line matrix, plus `Tf` and a new `Env.Font(scope, name)`. The cost is not the code — it is that `Walk` decides diverts, and `byb-b1.12`, `byb-7aq` and `byb-8ly` are all records of a walk change moving corpus classification. This needs a before/after `byblos-divert` run over the whole local sample as evidence, not a unit test. |
| glyph advance widths | 0.5–1 | `/FirstChar` + `/Widths` + `/MissingWidth` for simple fonts, `/W` + `/DW` for CID fonts, `sfnt.GlyphAdvance` as fallback, and the standard-14 tables when a font is neither embedded nor width-bearing. |
| word-gap segmentation | 1–2 | the tuning problem, not the coding one. See §2 C3 note: this survives the "no layout" cut because a boundary disagreement is a `Coverage` penalty on a document that lost nothing. |
| symbolic TrueType with no `/ToUnicode` | 2–3 | invert the embedded `(3,0)` cmap, fall back to the `post` table, fall back to `MacRomanEncoding` on the `(1,0)` subtable. Every step is a heuristic that some producer breaks. |
| predefined CJK CMaps | 1–2 | `Adobe-Japan1-UCS2` and friends are megabytes of Adobe `cmap-resources` data plus a registry/ordering lookup. Only reachable through `F_type0_no_tounicode` (§4), which is 0.7% of the sample's shown fonts. |

### Research projects — open-ended, do not estimate

| item | why it is not a week |
|---|---|
| line assembly, column detection, reading order | poppler's `TextOutputDev` is the accumulated answer to two decades of real documents. There is no specification to implement against; correctness is defined by agreement with a reference. **No Kleio consumer needs it** (§1). |
| Type1 / CFF glyph names | `eexec` decryption, charstring parsing, CFF `charset` INDEX resolution — to recover names the AGL then maps. Nothing in the dependency tree does any of it. |
| bidi and vertical writing | `pdftotext`'s own behaviour here is not a specification, so "parity" is undefined. No consumer needs it. |
| the broken-producer tail | `/ToUnicode` that maps every code to `U+0000`; Word's `U+F0xx` PUA output; subset fonts whose only names are `g17`. Perpetual maintenance, and the reason a from-scratch extractor is worse than poppler for years rather than months. |

---

## 4. The denominator

Two measurements were taken for this document on 2026-08-04, against the local
measurement sample described in `tools/sample/README.md`, which lives at
`~/work/dobbo-ca/.byblos-sample/` and is not in git.

**Population.** 5,672 files: `govdocs1` 4,840 (digitalcorpora govdocs1 zips),
`dc` 520 (DocumentCloud), `ia` 299 (archive.org), `anchors` 13 (the named files
beads quote facts about). Both probes were throwaway programs written under the
session scratchpad and deleted; §6 proposes the beads that would make them
permanent.

### 4.1 Font classes — what a decoder would meet

Every `/Type /Font` dict reachable through the xref table, classified by what a
code→Unicode decoder would have to do with it. `CIDFontType0`/`CIDFontType2`
descendants are excluded — they are never shown directly; their `Type0` parent
is. Counts are **font dicts, not text volume**: see the caveats below.

| class | govdocs1 | dc | anchors |
|---|---|---|---|
| A `Type0` with `/ToUnicode` | 2,498 | 714 | 1 |
| B simple font with `/ToUnicode` | 7,012 | 562 | 0 |
| C simple, `/Differences`, no `/ToUnicode` | 4,930 | 12 | 9 |
| D simple, named or absent encoding, no `/ToUnicode`, non-symbolic | 34,050 | 664 | 517 |
| **E** simple, **symbolic**, no `/ToUnicode` | 1,370 | 6 | 59 |
| **F** `Type0`, no `/ToUnicode` | 364 | 1 | 0 |
| **G** `Type3` | 822 | 22 | 0 |
| **shown font dicts** | **51,046** | **1,981** | **586** |

A+B+C+D — the Tier-1 scope of §1 — is **95.0%** of govdocs1's shown font dicts
(48,490 of 51,046), **99.5%** of dc's, and **89.9%** of anchors'. E+F+G, the
classes Tier 1 would decline, is 5.0% / 0.5% / 10.1%.

Per file, "contains at least one class E/F/G font" is 676 of 4,722 govdocs1 files
carrying any shown font (14.3%), and 9 of 374 in dc (2.4%).

`ia`: **299 files, 0 font dicts, 0 files with any shown font.** Sampled by hand,
`ia/pdfs/ia-cia-readingroom-document-0000107295.pdf` contains no `/Font` token at
all and `pdftotext` returns nothing; 25 sampled files, none containing `/Font`.
That population — raw archive scans — has no text layer to extract.

**Caveats, stated because the table invites over-reading.**
- A font dict is not a page of text. One symbolic `Symbol` font used for bullets
  puts a file in the "contains a hard font" bucket while costing it nothing.
  **Text volume attributed per font is unmeasured**, and it is the number that
  actually sizes the damage. Measuring it needs the C2+C3 machinery of §3, i.e.
  you cannot cheaply measure the thing that decides whether to build it — but a
  cheap proxy exists: `Tj`/`TJ` operand bytes attributed to the font in force,
  which needs only `Tf` tracking (0.5 day), not the text matrix.
- `pdfcpu` failed to read 1 of the 4,840 govdocs1 files; `byb-8ly` records 18
  govdocs1 files byblos itself refuses.
- `localscans`, `personal` and `commons` — three of the six corpora
  `FUTURE.md:99` reports codec mixes for — are **not present locally** and were
  not measured. `commons` is the largest missing population.

### 4.2 Born-digital verdict agreement — the number that settles call site 1

Kleio's `IsBornDigital` (`compress.go:81-99`) computes
`len(pdftotextOutput)/min(pages,5) >= 100`. The probe recomputed the same
predicate from `sum(PageInfo.TextChars over the first min(n,5) pages)/min(n,5)`
and compared verdicts.

| corpus | compared | agree | pdftotext-only | TextChars-only | skipped |
|---|---|---|---|---|---|
| govdocs1 | 4,822 | 4,820 (99.96%) | 2 | 0 | 18 |
| dc | 520 | 515 (99.04%) | 1 | 4 | 0 |
| ia | 299 | 299 (100%) | 0 | 0 | 0 |
| anchors | 13 | 13 (100%) | 0 | 0 | 0 |
| **total** | **5,654** | **5,647 (99.88%)** | **3** | **4** | **18** |

"Skipped" is a file `pdftotext` or `byblos.Inspect` refused; the byblos side of
that is `byb-8ly`, already open. The seven disagreements were not individually
examined — `byb-lez.1` should name them.

Note what this does *not* say. `TextChars` counts **stored code-unit bytes** and
`len(pdftotext output)` counts **UTF-8 bytes of decoded text plus inserted
layout whitespace**. These are different quantities that happen to fall on the
same side of 100/page in 99.88% of this sample. A two-byte CID font inflates the
first; a page of Latin text with heavy `-layout` whitespace inflates the second.
The agreement is empirical, corpus-bound, and the corpora above are 85% govdocs1
— which `byb-divert` already records is Acrobat Distiller output end to end, not
a scanned corpus (design spec §2).

### 4.3 Unmeasured, and the command that would measure it

- **How often the stash actually decides anything.** `Decide` (`gate.go:597`)
  short-circuits `VerdictPass` for born-digital *before* consulting coverage, and
  `hasStash` means "the stash carried text", not "the object exists"
  (`gate.go:584-593`). So call site 3 matters only for **non-born-digital
  documents whose original already carried a text layer** — scanner-OCR'd or
  hybrid scans. On `ia` that population is empty (§4.1). This is the number that
  decides whether §3's week is ever worth spending, and it is not measurable from
  byblos's corpus: it needs Kleio production counts, or a scan-shaped sample
  drawn the way `byb-divert` drew one. See `byb-lez.4`.
- **Token recall under a Tier-1-only decoder.** Whether classes A–D reproduce
  enough of `tokenSet` to keep `Coverage` above `passCoverage` (0.90) is
  unmeasured and cannot be measured without building the decoder. The cheapest
  partial answer is the `Tf`-attribution proxy above.
- **Whether byblos's word boundaries would agree with cadmus's.** Unmeasured;
  see §2 C3 and `byb-lez.7`.

---

## 5. Why call site 2 is a deletion, not a port

`ocr.go:191-197` reads the OCR stage's *own output* back through `pdftotext`,
and its comment gives two reasons: `ocrmypdf --sidecar` becomes a skip marker on
redelivery, and `--skip-text` leaves a born-digital document's real text alone so
the read-back returns real words instead of a placeholder.

Neither reason survives the cadmus migration. Cadmus produces the words; Kleio
converts them to a `byblos.TextLayer` and hands them to `StampTextLayer`. The
sidecar Kleio wants is exactly that word list — it never left the process — and
the born-digital half is exactly `stash.txt`, which the compress stage already
stored. So the sidecar becomes `union(words handed to byblos, stash tokens)` and
no extraction happens at all.

*This is inference, not verified behaviour.* Two things gate it: Kleio does not
import byblos yet (design spec §9 says so explicitly), and `byb-c0b` records that
the spec's `cadmus.Page → byblos.TextLayer` conversion names a type that does not
exist. There is also a behaviour change to accept deliberately: `ocr_text` feeds
a Postgres `to_tsvector` index queried with `websearch_to_tsquery`
(`internal/store/acl.go:107`), which supports quoted phrase search, so token
*positions* are indexed. A word list unioned from two sources has no meaningful
phrase order. Phrase queries would degrade; single-term search would not.

---

## 6. Proposed sub-beads

Not created — `byb-lez` is a scoping bead and these are its proposed children.
Each names an acceptance test that could actually be written.

**`byb-lez.1` — Replace `IsBornDigital`'s `pdftotext` call with `PageInfo.TextChars`.**
Repo: kleio (byblos unchanged). Accept: a differential harness over
`~/work/dobbo-ca/.byblos-sample` reports ≥99.5% verdict agreement across all four
corpora, and **enumerates every disagreeing file by name in the bead** (7 in the
§4.2 run). Do *not* pin the 100 chars/page threshold in a byblos test:
`corpus.BornDigitalTextChars` is 44 (`internal/corpus/corpus.go:84`), so the
synthetic fixtures sit on the wrong side of it by design.
Size: 1–2 days. Depends on: nothing.

**`byb-lez.2` — Delete the OCR read-back; build the sidecar from the words Kleio already holds.**
Repo: kleio. Accept: for a born-digital fixture, `ocr_text` still contains the
document's own words (sourced from `stash.txt` rather than the read-back); for a
scan fixture, `ocr_text` equals the stamped `TextLayer`'s words exactly. Plus an
explicit test that a redelivery does not empty it.
Size: 2–3 days. Depends on: the cadmus migration, and `byb-c0b`.

**`byb-lez.3` — Amend the design spec: poppler is not eliminated.**
Repo: byblos. §1's G1 row and §2's "two of `poppler`'s four roles" sentence both
have to name the surviving `pdftotext` call and why. Keep it distinct from §8's
oracle carve-out, which is not a G1 violation and must not be conflated with a
runtime dependency. Accept: doc change only; follow the `byb-0gm` amendment
pattern at §2 (state what the section previously asserted, and that it was
wrong).
Size: 0.5 day. Depends on: this document being accepted.

**`byb-lez.4` — Measure how often the coverage check consults a non-empty stash.**
Repo: kleio. This is the experiment that decides whether any font work is ever
justified. Accept: a rate with an explicit denominator for "documents reaching
`Decide` with `bornDigital == false` and `hasStash == true`", from production
counters or from a scan-shaped sample drawn as `byb-divert` drew one. A rate near
zero closes `byb-lez.5` through `.7` permanently.
Size: 1–2 days. Depends on: nothing. **Blocks: `.5`, `.6`, `.7`.**

**`byb-lez.5` — Tier-1 code→Unicode decoder (conditional on `.4`).**
Repo: byblos. Scope is exactly classes A–D of §4.1: `/ToUnicode` CMaps, the four
base encodings plus Symbol/ZapfDingbats, the AGL, `/Differences`, and
`Identity-H`. Explicitly out: symbolic-without-`/ToUnicode`,
`Type0`-without-`/ToUnicode`, `Type3`, and all of §3's research row. Accept: (a)
on files whose shown fonts are all class A–D, byblos's token set achieves ≥0.95
recall of `pdftotext`'s, denominator named; (b) **the API reports per-page
undecodable code units**, so a caller can tell "this page has no text" from "this
page has text I could not read" — the same discipline `ErrNotSingleRaster` and
`ErrUnsupportedImageCodec` already enforce on the raster path; (c) the AGL
licence question of §3 is answered in writing before any table is vendored.
Size: 2–3 weeks. Depends on: `.4`, `.6`.

**`byb-lez.6` — Track text state in `content.Walk`.**
Repo: byblos. `Tf`, `Td`, `TD`, `Tm`, `T*`, `TL`, `Tc`, `Tw`, `Tz`, `Ts`, a text
matrix and line matrix on `gstate`, and a new `Env.Font(scope, name)`. Accept:
(a) `TestWalkTracksTextPositions` — a fixture with hand-computed `Tm`/`Td`/`TJ`
origins matched exactly; (b) **a before/after `byblos-divert -json` run over the
whole local sample showing identical divert and unhandled rates** — this walk
decides diverts and `byb-b1.12`/`byb-7aq` are the record of what happens when
that is assumed rather than measured.
Size: 1–1.5 weeks. Depends on: `.4`.

**`byb-lez.7` — Word segmentation that agrees with an OCR engine's boundaries.**
Repo: byblos. Accept: (a) round-trip identity — for every corpus fixture that
`StampTextLayer` wrote, the extractor's token set equals the `TextLayer`'s token
set that was stamped; (b) on born-digital fixtures, agreement with
`pdftotext -bbox` word boxes at a stated tolerance, reusing the harness at
`stamp_test.go:119`. Explicitly **not** in scope: line order, column detection,
reading order.
Size: 1–2 weeks. Depends on: `.6`.

**`byb-lez.8` — Make the font-class census a repeatable tool.**
Repo: byblos. The §4.1 numbers came from a scratch program that no longer exists.
A `cmd/byblos-fonts` alongside `byblos-divert` and `byblos-annots` would make
them reproducible and let `.4`'s answer be re-checked on a new corpus. Accept:
running it over `~/work/dobbo-ca/.byblos-sample/govdocs1` reproduces the
govdocs1 column of §4.1 exactly.
Size: 1 day. Depends on: nothing. Worth doing regardless of the `.4` outcome.
