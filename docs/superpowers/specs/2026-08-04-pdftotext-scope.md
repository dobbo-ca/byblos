# `pdftotext`: what replacing it costs, and what to build instead

**Status:** scoping, decision proposed — not yet accepted
**Date:** 2026-08-04
**Bead:** `byb-lez` (parent `byb-js5`, the G1 audit epic)
**Repo:** `dobbo-ca/byblos`
**Also touches:** `dobbo-ca/kleio` (`internal/pipeline`)

`byb-lez` records the question as a binary: *"either byblos grows text
extraction, or Kleio keeps poppler for this one call and G1 is amended to say
so."* It is not binary. Kleio makes **three** `pdftotext` calls, they want three
different things, and only one of them needs no font decoding at all.

`byb-js5` already establishes that G1-as-an-absolute is refuted; this document
does not re-argue that. It argues about which of the residue is worth building.

---

## 1. Recommendation

**Build nothing font-related yet. Replace one of the three calls, keep the other
two on poppler as named G1 exceptions, and re-measure before spending a week on
font decoding.**

| Kleio call site | what it actually needs | verdict |
|---|---|---|
| `IsBornDigital` — `compress.go:82`, `ExtractText(ctx, pdf, 5)` | a **character count** over the first five pages, tested against 100 chars/page | **Replace now with `PageInfo.TextChars`.** Measured 99.96% verdict agreement on 4,822 govdocs1 files (§4). Zero new byblos API. |
| OCR read-back — `ocr.go:196`, `ExtractText(ctx, out, 0)` | a read of the **delivered artifact's** text layer — it is the fresh side of the OCR coverage gate (`validate.go:232`) as well as the `ocr_text` search index | **Keep poppler.** An earlier draft of this document called this a safe deletion. It is not; §5 is the correction. It needs the same code→Unicode capability as the row below. |
| the stash — `compress.go:579`, `ExtractText(ctx, in, 0)` | the **set of distinct lowercased alphanumeric tokens** of the original's text layer, ≥3 runes, capped at 100,000 (`gate.go:549` `tokenSet`) | **Keep poppler.** With the row above, one of **two** consumers that need code→Unicode, and it is not yet established that the gate they feed fires often enough to pay for it. |

The cut line inside those two rows, if either is ever built: **code→Unicode yes,
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
| C1 | code → Unicode | **nothing** | `walk.go:392,403` add `len(ops[i].Text)` to `TextChars` and discard the bytes. No `/ToUnicode`, `/Differences`, `/Encoding` or CMap **reader** exists anywhere, and every tree-wide match for those names is write-side or an assertion about the write side: `ToUnicode` and `Differences` have no match at all, `internal/pdfdoc/text.go:108` *writes* `/Encoding /WinAnsiEncoding` for the glyphless stamp font, `stamp.go:85` is the `Flags: 32` comment that has to accompany it, and `internal/glyphless/glyphless_test.go:357,377` assert on that emitted dict. |
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
| predefined CJK CMaps | 1–2 | `Adobe-Japan1-UCS2` and friends are megabytes of Adobe `cmap-resources` data plus a registry/ordering lookup. Only reachable through class F (§4.1) — 374 of 57,177 shown font dicts, **0.65%**. **That denominator cannot size CJK need and must not be read as doing so:** 89% of those font dicts are govdocs1's, a US-government English corpus with no CJK in it by construction, so the figure measures how rare `Type0`-without-`/ToUnicode` is *in English documents*. Sizing CJK needs a CJK-bearing corpus and none is present locally (§4.1's missing-corpora caveat). |

### Research projects — open-ended, do not estimate

| item | why it is not a week |
|---|---|
| line assembly, column detection, reading order | poppler's `TextOutputDev` is the accumulated answer to two decades of real documents. There is no specification to implement against; correctness is defined by agreement with a reference. **No Kleio consumer needs it** (§1). |
| Type1 / CFF glyph names | `eexec` decryption, charstring parsing, CFF `charset` INDEX resolution — to recover names the AGL then maps. Nothing in the dependency tree does any of it. |
| bidi and vertical writing | `pdftotext`'s own behaviour here is not a specification, so "parity" is undefined. No consumer needs it. |
| the broken-producer tail | `/ToUnicode` that maps every code to `U+0000`; Word's `U+F0xx` PUA output; subset fonts whose only names are `g17`. Perpetual maintenance, and the reason a from-scratch extractor is worse than poppler for years rather than months. |

---

## 4. The denominator

Measurements were taken for this document on 2026-08-04, against the local
measurement sample described in `tools/sample/README.md`, which lives at
`~/work/dobbo-ca/.byblos-sample/` and is not in git. **§4.1 was re-measured from
scratch after its first run was refuted; §4.1a records what was wrong.**

**Population.** 5,672 files: `govdocs1` 4,840 (digitalcorpora govdocs1 zips),
`dc` 520 (DocumentCloud), `ia` 299 (archive.org), `anchors` 13 (the named files
beads quote facts about). Every probe was a throwaway program written under the
session scratchpad and deleted; §6 proposes the beads that would make them
permanent. **Every rate below names the denominator it is a rate of** — the
first §4.1 shipped percentages whose denominator was wrong by 2.7×, and it read
as authoritative anyway.

### 4.1 Font classes — what a decoder would meet

Every `/Type /Font` dict present in the xref table, classified by what a
code→Unicode decoder would have to do with it. `CIDFontType0`/`CIDFontType2`
descendants are excluded — they are never shown directly; their `Type0` parent
is. Counts are **font dicts, not text volume**: see the caveats below.

**Command.** A throwaway Go program under the session scratchpad, since deleted
(`byb-lez.8` proposes making it permanent), run once per corpus:

```
env CGO_ENABLED=0 go run ./census ~/work/dobbo-ca/.byblos-sample/<corpus>/pdfs
```

It reads each file **twice** with pdfcpu v0.13.0 — `api.ReadContextFile`, which
expands object streams, and `api.ReadContext` with `ValidationRelaxed`, which
does not — and keeps whichever read succeeded, preferring the larger count when
both did. Reading it twice is not belt-and-braces; it is the whole correction of
§4.1a, and neither read alone is a superset of the other. **Classification
precedence, since it decides the ambiguous cases:** `Type3` → G; then
`Type0` → A/F on `/ToUnicode`; then simple fonts → B on `/ToUnicode`, else C on
an `/Encoding` dict carrying `/Differences`, else E on `FontDescriptor /Flags`
bit 3 set and bit 6 clear, else D. So a symbolic font that also has
`/Differences` counts as C, not E.

| class | govdocs1 | dc | ia | anchors |
|---|---|---|---|---|
| A `Type0` with `/ToUnicode` | 2,511 | 1,596 | 0 | 1 |
| B simple font with `/ToUnicode` | 7,097 | 1,946 | 0 | 0 |
| C simple, `/Differences`, no `/ToUnicode` | 4,931 | 41 | 0 | 9 |
| D simple, named or absent encoding, no `/ToUnicode`, non-symbolic | 34,087 | 1,776 | 0 | 517 |
| **E** simple, **symbolic**, no `/ToUnicode` | 1,370 | 15 | 0 | 59 |
| **F** `Type0`, no `/ToUnicode` | 365 | 9 | 0 | 0 |
| **G** `Type3` | 822 | 25 | 0 | 0 |
| **shown font dicts** | **51,183** | **5,408** | **0** | **586** |
| files in corpus | 4,840 | 520 | 299 | 13 |
| files with ≥1 shown font | 4,733 | 516 | 0 | 7 |
| files no reader could open | 1 | 4 | 0 | 0 |

**The denominators, named.** A+B+C+D — the Tier-1 scope of §1 — is **95.00%** of
govdocs1's 51,183 shown font dicts (48,626 of them), **99.09%** of dc's 5,408
(5,359), and **89.93%** of anchors' 586 (527). E+F+G, the classes Tier 1 would
decline, is **5.00% / 0.91% / 10.07%** of those same three denominators. Pooled
over all four corpora the denominator is **57,177** shown font dicts and A–D is
**95.34%** (54,512), E+F+G **4.66%** (2,665).

Per file, "contains at least one class E/F/G font" is **677 of the 4,733**
govdocs1 files carrying any shown font (**14.30%**), **14 of 516** in dc
(**2.71%**), and 2 of 7 in anchors.

**Class D is two populations and Tier 1 can only serve one of them.** D is
"named **or absent** encoding". A font with a *named* encoding
(`/WinAnsiEncoding` and friends, or a `/BaseEncoding` inside an encoding dict) is
a table lookup. A font with **no** `/Encoding` at all needs the font program's
own built-in encoding — Type1 `eexec` charstring names, or a TrueType
`cmap`/`post` table — which §3 files under "research projects, do not estimate".
That split was never measured before; it is measured now, the same way:

| | D total | named | absent | absent, as % of that corpus's shown font dicts |
|---|---|---|---|---|
| govdocs1 | 34,087 | 32,392 | 1,695 | 3.31% of 51,183 |
| dc | 1,776 | 1,572 | 204 | 3.77% of 5,408 |
| anchors | 517 | 512 | 5 | 0.85% of 586 |
| **pooled** | **36,380** | **34,476** | **1,904** | **3.33% of 57,177** |

So **the 95% Tier-1 headline is a ceiling, not a capability.** Deducting
D-absent, the share a Tier-1 decoder could actually serve is **91.69%** of
govdocs1's font dicts, **95.32%** of dc's, **89.08%** of anchors' and **92.01%**
of the pooled 57,177. 95.34% is what Tier 1 covers *if* built-in encodings turn
out to be free; 92.01% is what it covers if they are not. Which applies to a
given font depends on whether its program is a TrueType with a usable `cmap`
(cheap — §3's `sfnt` row) or a Type1 (open-ended — §3's research row), and
**that third split is not measured.** 92.01% is a conservative floor for a
second reason too: the D-absent bucket also holds *non-embedded* standard-14
fonts, whose built-in encoding is `StandardEncoding` and therefore a table
lookup Tier 1 already has. How much of the 1,904 that is, is likewise
unmeasured. Any bead that cites 95% for scope must
cite 92% for capability; `byb-lez.5` is written that way.

`ia`: **299 files, 0 font dicts, 0 files with any shown font**, and every one of
the 299 opened. Sampled by hand,
`ia/pdfs/ia-cia-readingroom-document-0000107295.pdf` contains no `/Font` token at
all and `pdftotext` returns nothing; 25 sampled files, none containing `/Font`.
That population — raw archive scans — has no text layer to extract.

**Independent cross-check.** `pdffonts` (poppler 26.06.0) walks *page resources*
rather than the xref table, so it is a different definition run by a different
codebase:

```
for f in *.pdf; do pdffonts "$f" | tail -n +3 | grep -c .; done   # summed
```

- `dc`: **520 files, 520 with fonts, 5,276 font rows** — against the census's
  5,408 over 516 files, i.e. the census runs **2.50% high**.
- `govdocs1`: **4,840 files, 4,719 with fonts, 48,565 rows** — against 51,183
  over 4,733, i.e. **5.39% high**.

Exact agreement is not expected and would be suspicious: the xref census counts
font dicts no page ever references, while `pdffonts` sees fonts in the four dc
files pdfcpu refuses entirely. The census being modestly high on both corpora, in
the same direction, is what a superset should look like. The refuted first run
was **2.7× low on dc and correct on govdocs1**, which is not.

**Caveats, stated because the table invites over-reading.**
- A font dict is not a page of text. One symbolic `Symbol` font used for bullets
  puts a file in the "contains a hard font" bucket while costing it nothing.
  **Text volume attributed per font is unmeasured**, and it is the number that
  actually sizes the damage. Measuring it needs the C2+C3 machinery of §3, i.e.
  you cannot cheaply measure the thing that decides whether to build it — but a
  cheap proxy exists: `Tj`/`TJ` operand bytes attributed to the font in force,
  which needs only `Tf` tracking (0.5 day), not the text matrix.
- **5 of the 5,672 files could not be read at all** and contribute zero to every
  count above: 1 govdocs1 file (`700620.pdf`, "xrefsection: missing trailer
  dict") and 4 dc files whose object streams only the validating reader can
  expand and which that reader then rejects (`dc-28522465`, `dc-28522472`,
  `dc-28522520`, `dc-28522579`). `pdffonts` reads fonts out of all four dc files,
  so those font dicts exist and are simply not in this table. `byb-8ly` records
  18 govdocs1 files byblos itself refuses.
- `localscans`, `personal` and `commons` — three of the six corpora
  `FUTURE.md:99` reports codec mixes for — are **not present locally** and were
  not measured. `commons` is the largest missing population, and it is also the
  only plausible source of the non-Latin scripts §3's CJK row cannot size.

### 4.1a The first run of §4.1, and why it was wrong

Kept rather than quietly replaced, because the failure mode is one any successor
tool inherits.

The first run of this section reported **1,981 shown font dicts over 374 dc
files** and claimed A–D was 99.5% of them. Three separate things were wrong.

1. **Arithmetic.** Its own dc column summed to 714 + 562 + 12 + 664 = 1,952 of a
   stated 1,981, i.e. A–D 98.54% and E+F+G 1.46% — roughly 3× the 0.5% it
   printed.
2. **The denominator.** 1,981 over 374 files is a **2.7× undercount**. The probe
   read each file with pdfcpu's `api.ReadContext`, which builds the xref table
   but does **not decompress object streams**, so every font dict stored inside
   an `ObjStm` was invisible and the file looked font-free. Reproduced exactly:
   replaying that read path returns 1,981 over 374 files to the object, and
   switching to `api.ReadContextFile` returns 5,408 over 516. **142 dc files
   change from "no fonts" to "fonts"** (374 → 516 of 520). `dc-28522262.pdf` alone goes from 0 to
   247 font dicts — and `pdffonts`, independently, reports 247 for it.
3. **Why govdocs1 hid it.** govdocs1 is 2000s-era Distiller output with classic
   cross-reference tables. The same bug costs it 137 font dicts of 51,183
   (0.27%) and 11 files of 4,733, so its column looked right and vouched for the
   dc column beside it. dc is modern producer output and is almost entirely
   cross-reference streams. **A read-path bug that is invisible on your largest
   corpus is not a small bug**, and pooling made it smaller still.

The 374-file figure was refutable without parsing a single PDF: **512 of 520 dc
files emit more than 50 alphanumeric characters through `pdftotext`**
(`pdftotext -q f - | tr -cd '[:alnum:]' | wc -c`), and a file that emits text
shows fonts. `byb-lez.8`'s acceptance criteria are written to catch all three
failures.

### 4.2 Born-digital verdict agreement — the number that settles call site 1

Kleio's `IsBornDigital` (`compress.go:81-99`) is two clauses ANDed:
`len(pdftotextOutput)/min(pages,5) >= 100` **and** `!HasFullPageImage`. Only the
first is a candidate for replacement and only the first was measured — the probe
recomputed it from
`sum(PageInfo.TextChars over the first min(n,5) pages)/min(n,5)` and compared
verdicts. The `HasFullPageImage` clause (`tools.go:342`, `pdfimages -list`
against page boxes) is untouched by this document and is a separate poppler
call that `byb-0gm` covers.

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
  decides whether §3's week is ever worth spending. The **predicate** is
  computable offline today — `bornDigital` is `IsBornDigital`'s two clauses and
  `hasStash` is "whole-document `pdftotext` output is not blank"
  (`validate.go:231`) — so what byblos's corpus cannot supply is not the
  measurement but the **intake mix**: 4,840 of its 5,672 files (85%) are
  govdocs1, which is not what a family DMS receives. There are no production
  counts to fall back on either. **Kleio has no metrics or counter
  instrumentation of any kind** — the only `otel` entries in its `go.mod` are
  indirect, pulled by the AWS SDK — and `byb-js5` records that "nothing here has
  ever run against the real pipeline". So the method is an offline replay over a
  named corpus plus a scan-shaped sample drawn the way `byb-divert` drew one.
  See `byb-lez.4`.
- **Token recall under a Tier-1-only decoder.** Whether classes A–D reproduce
  enough of `tokenSet` to keep `Coverage` above `passCoverage` (0.90) is
  unmeasured and cannot be measured without building the decoder. The cheapest
  partial answer is the `Tf`-attribution proxy above.
- **Whether byblos's word boundaries would agree with cadmus's.** Unmeasured;
  see §2 C3 and `byb-lez.7`.

---

## 5. Why call site 2 is **not** a deletion

**An earlier draft of this document proposed deleting `ocr.go:196`. That was
wrong.** It is corrected here rather than quietly removed, because the argument
that produced it is sound right up to the point where it fails, and a successor
will reconstruct it.

**The argument that was made.** `ocr.go:191-197` reads the OCR stage's *own
output* back through `pdftotext`, and its comment gives two reasons: `ocrmypdf
--sidecar` becomes a skip marker on redelivery, and `--skip-text` leaves a
born-digital document's real text alone so the read-back returns real words
instead of a placeholder. Neither reason survives the cadmus migration — cadmus
produces the words, Kleio converts them to a `byblos.TextLayer` and hands them
to `StampTextLayer`, the sidecar Kleio wants is that word list and it never left
the process, and the born-digital half is exactly `stash.txt`. So the sidecar
becomes `union(words handed to byblos, stash tokens)` and no extraction happens
at all.

**What it missed: `sidecar.txt` has a second consumer, and that one is a gate.**
`sidecar.txt` is not only `ocr_text`. `validate.go:194` reads it and
`validate.go:232` computes `Coverage(stash, sidecar)` — it is the **fresh side
of the OCR coverage gate**. `Coverage` (`gate.go:514`) is stash-token recall in
fresh: it materialises `tokenSet(stashed)` and strikes tokens off that set as it
streams `fresh`, returning `(total - remaining) / total`.

Redefine `fresh` as a union that contains the stash and it is a superset of the
stash by construction. Then:

- `Coverage` returns exactly **1.0 for every document**;
- `coverageOK` at `gate.go:631` (`!hasStash || coverage > passCoverage`) is
  **unconditionally true**;
- the `hasStash && coverage < floorCoverage` reject at `gate.go:635`
  (`floorCoverage = 0.75`, `gate.go:38`) becomes **unreachable**.

Kleio's own code already warns against exactly this shape. `compress.go:579`
stashes the whole text layer unconditionally, and its comment says stashing
selectively "would make coverage trivially 1.0 and the check meaningless". The
withdrawn proposal reached the same end by the other door.

**The population it disables is precisely the one `byb-lez.4` exists to size.**
`Decide` (`gate.go:597`) returns `VerdictPass` for `bornDigital` before it looks
at coverage, and `hasStash` gates every coverage clause, so the gate fires only
on `bornDigital == false && hasStash == true` — a scan whose original already
carried a text layer. Under `retention_policy = 'discard'`, the policy
`gate.go`'s own `VerdictNoText` comment names, that gate is what stops Kleio
deleting an original whose text the compression pass destroyed.

**The narrower substitution does not work either.** Suppose the union omits the
stash and uses only the words cadmus recognised. On the population that matters,
those words are mostly absent: a scan carrying a scanner-OCR text layer is
exactly what `--skip-text` (`ocr.go:77`, `compress.go:195`) deliberately skips,
so the deliverable's text on those pages is the compressed *original's* text and
the recognised-word list is empty for them. Coverage would under-report and fail
documents nothing is wrong with. One substitution produces false passes, the
other false failures. *(This paragraph is inference from reading `ocr.go:46-77`,
`compress.go:189-195` and `Decide`; the pipeline has never run, so it is not
observed behaviour. The paragraph above it is not inference — it follows from
`Coverage`'s definition alone.)*

The invariant, stated so it can be pinned by a test rather than remembered:

> **The coverage gate's fresh side must be a read of the delivered artifact.**
> Any value derived from the stash makes the gate return 1.0; any value derived
> only from words held in memory misses the text `--skip-text` carried over.

So call site 2 keeps poppler, and it needs the same code→Unicode capability as
call site 3 — the two collapse into **one** build decision rather than two, which
is why §1's third row no longer says "the only consumer". What *is* still safe is
enriching the **search** text: if `ocr_text` should also carry stash words for
born-digital redeliveries, that is a second object, never `sidecar.txt`.

Three things about the withdrawn argument remain live regardless of it. Kleio
does not import byblos yet (design spec §9 says so explicitly, and `byb-js5`
records that "nothing here has ever run against the real pipeline").
`byb-c0b` records that the spec's `cadmus.Page → byblos.TextLayer` conversion
names a type that does not exist. And the behaviour change it identified still
applies to any union: `ocr_text` feeds a Postgres `to_tsvector` index queried
with `websearch_to_tsquery` (`internal/store/acl.go:107`), which supports quoted
phrase search, so token *positions* are indexed — a word list unioned from two
sources has no meaningful phrase order. Phrase queries would degrade;
single-term search would not.

---

## 6. Proposed sub-beads

Not created — `byb-lez` is a scoping bead and these are its proposed children.
Each names an acceptance test that could actually be written.

**`byb-lez.1` — Replace `IsBornDigital`'s `pdftotext` call with `PageInfo.TextChars`.**
Repo: kleio (byblos unchanged). Accept: a differential harness over
`~/work/dobbo-ca/.byblos-sample` reproduces §4.2 at **two separately stated
bars — ≥99.8% verdict agreement over the pooled 5,654 compared files, and ≥99.0%
on each corpus taken alone** — and **enumerates every disagreeing file by name in
the bead** (7 in the §4.2 run). The two bars are separate on purpose. An earlier
draft asked for "≥99.5% across all four corpora", which is ambiguous between the
two readings and fails under the per-corpus one: dc measures **99.04%** (515 of
520), against a pooled 99.88% (5,647 of 5,654). **dc is the known exception**;
its 4 `TextChars`-only and 1 `pdftotext`-only disagreements are the ones the bead
must name, and a bar that dc cannot clear is a bar that gets waived rather than
met. Scope note: `IsBornDigital` is `chars/page ≥ 100` **and**
`!HasFullPageImage` (`compress.go:81-99`); only the first clause is replaced and
only it was measured. Do *not* pin the 100 chars/page threshold in a byblos test:
`corpus.BornDigitalTextChars` is 44 (`internal/corpus/corpus.go:84`), so the
synthetic fixtures sit on the wrong side of it by design.
Size: 1–2 days. Depends on: nothing.

**`byb-lez.2` — Do *not* delete the OCR read-back; pin the constraint that stops the next person deleting it.**
Repo: kleio. **Hard precondition, and the reason the earlier "delete it" scoping
was withdrawn (§5): `sidecar.txt` is the fresh side of the OCR coverage gate
(`validate.go:232`), not only `ocr_text`. Any redefinition of it that is a
superset of `stash.txt` makes `Coverage` (`gate.go:514`) return 1.0 for every
document, `coverageOK` (`gate.go:631`) unconditionally true, and the
`floorCoverage = 0.75` reject (`gate.go:635`) unreachable — on exactly the
`bornDigital == false && hasStash == true` population `byb-lez.4` exists to
size, and under `retention_policy = 'discard'` that is the gate standing between
a compression that destroyed the text and a deleted original.** The deletion is
therefore off the table until something replaces the gate's fresh side, and
nothing in the cadmus migration does. The narrower substitution — recognised
words only, no stash — fails from the other direction; see §5.

What this bead actually does, all of it defensive:
(a) a regression test that the gate is *enforceable* — a fixture whose candidate
text layer is destroyed after the stash was taken must score
`coverage < floorCoverage` and reach `VerdictFail`, so a future change that makes
`Coverage` identically 1 fails a test instead of silently passing every document;
(b) a comment at `ocr.go:196` naming `validate.go:232` as its second consumer, in
the style `compress.go:579` already uses for the same hazard;
(c) if `ocr_text` should carry stash words for search, a **second** object —
never `sidecar.txt`.
Size: **0.5–1 day.** This is one test and two comments. The withdrawn deletion
was priced at 2–3 days, and that price was set without the gate: a version of it
that actually worked would first have to specify, build and validate a
replacement for the coverage gate's fresh side, which is a gate redesign of
**unmeasured** size and is deliberately not scoped here.
Depends on: nothing. (The cadmus migration and `byb-c0b` were dependencies of the
withdrawn deletion, not of this.)

**`byb-lez.3` — Amend the design spec: poppler is not eliminated.**
Repo: byblos. §1's G1 row and §2's "two of `poppler`'s four roles" sentence both
have to name the **two** surviving `pdftotext` calls and why each survives: the
stash (`compress.go:579`, §1 row 3) and the OCR read-back (`ocr.go:196`, §5).
Naming only one would repeat this document's own first mistake. Keep it distinct
from §8's oracle carve-out, which is not a G1 violation and must not be
conflated with a runtime dependency. Accept: doc change only; follow the
`byb-0gm` amendment pattern at §2 (state what the section previously asserted,
and that it was wrong).
Size: 0.5 day. Depends on: this document being accepted.

**`byb-lez.4` — Measure how often the coverage check consults a non-empty stash.**
Repo: kleio. This is the experiment that decides whether any font work is ever
justified. **The method is not "from production counters".** Kleio has no
metrics, counter or expvar instrumentation of any kind — the only `otel` entries
in `go.mod` are indirect, pulled by the AWS SDK — and `byb-js5` records that
"nothing here has ever run against the real pipeline", so there is no production
to count. Accept, both halves:
(a) *Offline, available today.* A rate with an explicit denominator for
"documents reaching `Decide` with `bornDigital == false` and `hasStash == true`",
computed per file by replaying the two predicates directly rather than by running
the pipeline: `bornDigital` is `IsBornDigital`'s two clauses
(`compress.go:81-99`), `hasStash` is "whole-document `pdftotext` output is not
blank" (`validate.go:231`). Report it for
`~/work/dobbo-ca/.byblos-sample` **and separately** for a scan-shaped sample
drawn the way `byb-divert` drew one — the local sample is 85% govdocs1 by file
count and is not a family DMS's intake, so one number over both would be the
same denominator error §4.1a is about.
(b) *Online, for when there is production.* Add `born_digital` and `has_stash`
fields to the existing `slog.Info("validated", …)` at `validate.go:257`, which
already emits `verdict`, `coverage` and `words`. That is the counter, and it
costs two fields rather than a metrics stack.
A rate near zero closes `byb-lez.5` through `.7` permanently.
Size: 1–2 days. Depends on: nothing. **Blocks: `.5`, `.6`, `.7`.**

**`byb-lez.5` — Tier-1 code→Unicode decoder (conditional on `.4`).**
Repo: byblos. Scope is classes A–D of §4.1 **with named encodings**:
`/ToUnicode` CMaps, the four base encodings plus Symbol/ZapfDingbats, the AGL,
`/Differences`, and `Identity-H`. Explicitly out: symbolic-without-`/ToUnicode`,
`Type0`-without-`/ToUnicode`, `Type3`, all of §3's research row, **and the
"absent encoding" half of class D — 1,904 of 57,177 shown font dicts, 3.33%
(§4.1) — which needs the font program's own built-in encoding and is a §3
research item, not a Tier-1 one.** State the capability figure, not the scope
figure, anywhere this bead is quoted: A–D is 95.34% of the pooled 57,177 shown
font dicts, but Tier 1 as scoped here serves **92.01%**.
Accept: (a) on files whose shown fonts are **all class A–D with named
encodings**, byblos's token set achieves ≥0.95 recall of `pdftotext`'s,
denominator named — that file list is produced by `.8`'s census tool and the
token boundaries by `.7`, which is why both are dependencies; (b) **the API
reports per-page undecodable code units**, so a caller can tell "this page has no
text" from "this page has text I could not read" — the same discipline
`ErrNotSingleRaster` and `ErrUnsupportedImageCodec` already enforce on the raster
path, and the only thing that keeps the D-absent population visible rather than
silently empty; (c) the AGL licence question of §3 is answered in writing before
any table is vendored.
Size: 2–3 weeks. Depends on: `.4`, `.6`, `.7`, `.8`.

**`byb-lez.6` — Track text state in `content.Walk`.**
Repo: byblos. **`BT` and `ET` first.** Neither has a case in the operator switch
today — the switch runs `walk.go:316-425` and the only `BT`/`ET` occurrences in
`internal/content` are test inputs and the comment at `walk.go:224` — so their
operands fall through and are dropped. `BT` resets the text **and** line matrices
to identity, which means acceptance (a) below was **unsatisfiable as previously
scoped**: with no `BT` case there is nothing to reset the matrices between text
objects and no hand-computed origin can be matched across two of them. Then
`Tf`, `Td`, `TD`, `Tm`, `T*`, `TL`, `Tc`, `Tw`, `Tz`, `Ts`, a text matrix and
line matrix on `gstate`, and a new `Env.Font(scope, name)`.
**Also re-scope the two show operators that are not only show operators:** `'`
advances to the next line *before* showing, and `"` sets `Tw` and `Tc` and then
advances and shows (ISO 32000-1 9.4.3). `walk.go:392` handles `Tj`, `'` and `"`
in a single case as text-showing only — correct for `TextChars`, wrong the moment
a line matrix exists.
Accept: (a) `TestWalkTracksTextPositions` — a fixture with hand-computed
`Tm`/`Td`/`TJ` origins matched exactly, **including at least one `BT` reset
between two text objects and one `"` with non-zero `Tw`/`Tc`**; (b) **a
before/after `byblos-divert -json` run over the whole local sample showing
identical divert and unhandled rates** — this walk decides diverts and
`byb-b1.12`/`byb-7aq` are the record of what happens when that is assumed rather
than measured.
Size: **1.5–2 weeks** — twelve operators rather than ten, and `'`/`"` change a
case three existing tests already assert on (`walk_test.go:131,391`, plus the
`Tr` interaction at `walk_test.go:391`).
Depends on: `.4`.

**`byb-lez.7` — Word segmentation that agrees with an OCR engine's boundaries.**
Repo: byblos. Produces **boundaries, not text**, so it does not depend on `.5`
and `.5` depends on it. Accept: (a) on born-digital fixtures, agreement with
`pdftotext -bbox` word boxes at a stated tolerance, reusing the harness at
`stamp_test.go:119`; (b) round-trip — for every corpus fixture `StampTextLayer`
wrote, the segmenter recovers the same word *count and boxes* as the `TextLayer`
that was stamped. (The earlier scoping made (b) an assertion about *token sets*,
which needs the decoder of `.5` and made `.5` and `.7` mutually dependent; the
token-set form of it now lives in `.5` acceptance (a), where the decoder exists.)
Explicitly **not** in scope: line order, column detection, reading order.
Size: 1–2 weeks. Depends on: `.6`.

**`byb-lez.8` — Make the font-class census a repeatable tool.**
Repo: byblos. The §4.1 numbers came from a scratch program that no longer exists,
and §4.1a is what that cost. A `cmd/byblos-fonts` alongside `byblos-divert` and
`byblos-annots` would make them reproducible and let `.4`'s answer be re-checked
on a new corpus. Accept, and each clause is one of §4.1a's three failures:
(a) running it over `~/work/dobbo-ca/.byblos-sample/govdocs1` reproduces the
govdocs1 column of §4.1 exactly **and over `.../dc` reproduces the dc column
exactly** — dc is the load-bearing half, because it is the corpus the
object-stream defect was visible on and govdocs1 is the corpus that hid it;
(b) it **reports per corpus the number of files no reader could open, and refuses
to count a file it could not parse as a file with zero fonts** — that silent zero
is the whole of §4.1a defect 2;
(c) a `--pdffonts` differential mode, since poppler's page-resource walk is the
independent definition that caught it, with the expected direction of the gap
(census high, by single-digit percent) stated in the tool's own output;
(d) it prints class totals **and** the class-D named/absent split, so no caller
can quote 95% where 92% is the honest number.
Size: 1–2 days. Depends on: nothing. Worth doing regardless of the `.4` outcome,
and **`.5` depends on it.**
