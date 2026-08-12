# Rebuilding the measurement sample

The B1 beads (byb-b1.1 through byb-b1.6) were each measured against a real
archive sample of 72,892 pages. That sample lived in a session scratchpad and is
gone; byb-b1.9 exists because of it. The scripts here are the half that should
have survived.

**Data lives outside this repo, at `~/work/dobbo-ca/.byblos-sample/`.** It is
several gigabytes and is not git's problem. What git carries is this directory:
the fetch scripts and, under `ids/`, the identifiers actually drawn. Those are
enough to rebuild the same sample; the PDFs are not.

    ~/work/dobbo-ca/.byblos-sample/
      anchors/pdfs/     the named files the beads quote — fetch these first
      govdocs1/pdfs/
      ia/pdfs/
      dc/pdfs/
      ids/              collection enumerations and the drawn samples
      manifest.tsv      path, source URL, bytes, sha256
      results/          divert and annotation output

## Order

Anchors first. They are ~100 MB, they take two minutes, and they are the only
part of the sample whose correctness can be checked rather than assumed —
every one of them is a file some bead quotes a measured fact about. If an
anchor's hash is right, the fetch path, the manifest and the extract path are
all working before the multi-gigabyte legs commit.

```sh
S=~/work/dobbo-ca/.byblos-sample
B=https://digitalcorpora.s3.amazonaws.com/corpora/files/govdocs1/zipfiles

# archive.org anchors, by name
tools/sample/fetch_ia_anchors.sh

# govdocs1 anchors, by ranged read of the containing zip
python3 tools/sample/govdocs1_pdfs.py --members $S/anchors/pdfs $B/005.zip 005/005393.pdf 005/005697.pdf
python3 tools/sample/govdocs1_pdfs.py --members $S/anchors/pdfs $B/004.zip 004/004513.pdf 004/004997.pdf
python3 tools/sample/govdocs1_pdfs.py --members $S/anchors/pdfs $B/003.zip 003/003158.pdf 003/003988.pdf
python3 tools/sample/govdocs1_pdfs.py --members $S/anchors/pdfs $B/001.zip 001/001029.pdf
```

Then check, before going further:

| Assertion | Value |
|---|---|
| `ia-DTIC_ADA383635.pdf` sha256 | `ce350011414e3b9a6de8f85c53857adf6a3e679d6d343e9941ba6aa7055da6b5` |
| `005393.pdf` md5 | `274320f88291c83c6fd775be9bb7d8fa` |
| `ia-DTIC_ADA383635.pdf` p40 raster box | `(0, 0)-(568.3708, 791.7616)` on a 612x792 page, `covers_page=false`, extracted |
| `005393.pdf` p91 raster box | `(14.97417, 15.80119)-(576.39272, 776.70321)` — the unit square under byb-b1.2's quoted CTM |
| `ia-revistadasocied03portgoog.pdf` | 762 pages, **0 failed** — byb-5kk's repair holding on the real file |

The p40 area ratio is **0.9284309**. byb-b1.3's headline quotes 0.9174 for that
page, which is a transcription slip: that figure belongs to `005393.pdf` p91.

## The bulk legs

```sh
PY=path/to/venv/bin/python          # needs `remotezip`
seq -f "$B/%03g.zip" 0 50 950 | xargs $PY tools/sample/govdocs1_pdfs.py $S/govdocs1/pdfs 5000000000 >> $S/manifest.tsv

tools/sample/ia_sample.sh dticarchive    150 $S/ia/pdfs 20260730 >> $S/manifest.tsv
tools/sample/ia_sample.sh ciareadingroom 150 $S/ia/pdfs 20260730 >> $S/manifest.tsv

tools/sample/dc_sample.sh 520 $S/dc/pdfs 20260730 >> $S/manifest.tsv   # credentials from .env
```

The seed alone does not reproduce a draw: archive.org's scrape API gives no
guarantee that a later enumeration returns the same population, so the drawn id
list is the record, not the seed. `ids/sample-<collection>.txt` here is that
record — the 150 identifiers actually taken — and re-fetching them needs
nothing else.

The enumerated populations stay on disk at `$S/ids/.ids-<collection>.txt`; the
CIA one is 941,229 lines and 52 MB, which is not git's problem. Their sizes and
hashes are in `ids/population.sha256`, which is enough to tell whether a later
enumeration drew from the same population without carrying it.

## What the rebuilt sample is not

It is **not** the sample the B1 beads measured, and their numbers are priors
rather than targets. Two of the five original sets, `localscans` (40 files, a
ScanSnap iX500 corpus) and `personal`, were private files and cannot be
rebuilt. Both contributed **zero** of byb-b1.3's 132 not-page-covering pages,
so every rate computed here runs pessimistic against the original mix.
`localscans` was also 100% diverted before byb-b1.1's fix, which was the single
largest concentration of the invisible-text shape; that whole population is
absent.

The dc leg is a random draw from the 6,000 most recently uploaded public
DocumentCloud documents, not from all 7,175,565 of them. The API paginates by
cursor, so the frame can only be walked forward from the newest; there is no
uniform draw available at this cost. Recency is the bias to hold in mind, and
it is not obviously neutral — upload cohorts cluster by uploader and by
scanning generation.

Two filters were deliberately NOT used to widen it. Querying `redacted` or
`subpoena` would have scattered the frame nicely and biased it straight toward
the thing being measured; a stamp rate drawn from documents selected for
carrying stamps is not a rate. `page_count:[N TO M]` does work and is the
neutral option if stratification is ever wanted (`created_at` ranges return
zero — wrong syntax, not an empty corpus).

A disagreement with a bead's number therefore means the sampling frame differs.
It is a reason to look at the sample, not to change the code.

## The bench set

`cmd/byblos-bench` measures cost against a fixed set of twelve documents,
pinned by name and sha256 in `ids/bench-v1.tsv`. They are the anchors: seven
govdocs1 files and five of the six archive.org files, about 89 MB.

```sh
tools/sample/bench_set.sh "$(mktemp -d)"     # writes bench-v1.tar.zst + .sha256
```

The script hashes every document against `ids/bench-v1.tsv` first and writes
nothing at all if one is missing or has moved. It stages each file with one
fixed timestamp, so two builds of the same twelve documents produce the same
archive — the baseline records that archive's sha256 as its bench-set
fingerprint, and a rebuild that moved those bytes would invalidate a baseline
that is otherwise still good. `bench_set_test.sh` covers both refusals and the
rebuild.

The archive is flat, because `byblos-bench` globs `DIR/*.pdf`:

```sh
mkdir -p bench-v1 && tar --zstd -xf bench-v1.tar.zst -C bench-v1
```

The sixth archive.org anchor, `ia-06043926.cn.pdf`, is **excluded and must not
be added back**. archive.org publishes no rights statement and no date for that
item, so there is nothing on which to base republication from a public
repository. It stays in the local sample. The cost is that the bench set has no
CJK document, which is a known gap for a later `bench-v2` rather than an
oversight — glyph density there is unlike Latin, and `jbig2-generic` is the
capability most likely to behave differently on it.

The archive is published as a GitHub release asset rather than through Git LFS.
LFS bandwidth is billed against every public clone; release asset bandwidth is
not.

## Measuring

```sh
go run ./cmd/byblos-divert -json $S/govdocs1/pdfs      # per-reason divert counts
go run ./cmd/byblos-annots -jsonl out.jsonl $S/govdocs1/pdfs
```

`byblos-annots` answers byb-b1.11. It reports three narrowing counts, because
they are different questions: how many extracted pages carry an annotation that
paints at all (the silent loss), how many of those had a raster that fell short
of the page box (what byb-b1.3 newly exposed), and how many have ink landing
outside the raster entirely (the blank strip). See the command's doc comment.

Measured 2026-07-30 over all three rebuildable sets — 166,423 pages, 22,050 of
them extracted: **7 / 1 / 0**.

| set | pages | extracted | A: paints | B: not covering | C: outside | diverted + paints |
|---|---|---|---|---|---|---|
| govdocs1 | 136,134 | 3,667 | 6 | 1 | 0 | 369 |
| ia | 14,943 | 14,943 | 0 | 0 | 0 | 0 |
| dc | 15,346 | 3,440 | 1 | 0 | 0 | 348 |

dc_random was the leg expected to move this — FOIA and court material is where
redaction stamps live, and it does carry them (35 Stamp, 43 Watermark, 66 Ink,
against govdocs1's near-pure Widget). It moved nothing. Those annotations land
on pages that were **already diverting for other reasons**: unsupported-codec
alone accounts for 5,431 of dc's 11,906 diverts. 717 diverted pages across the
sample carry painting ink and are safe today only because something else
declined them first — which is the reservoir to watch, not the current rate.
