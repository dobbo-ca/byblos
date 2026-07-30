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

DOCUMENTCLOUD_USER=you@example.com DOCUMENTCLOUD_PASS=... tools/sample/dc_sample.sh 520 $S/dc/pdfs 20260730 >> $S/manifest.tsv
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

A disagreement with a bead's number therefore means the sampling frame differs.
It is a reason to look at the sample, not to change the code.

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

Measured 2026-07-30 over govdocs1 + ia, 151,077 pages, 18,610 of them
extracted: **6 / 1 / 0**.
