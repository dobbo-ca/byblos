# Memory limits: what byblos bounds, and what it will

This page states the resource-limit contract every dobbo-ca library meets
(section 9A.1 of the kleio in-process pipeline design) as it applies to
byblos, then lists exactly what the code enforces today and what is planned,
with the phase that lands it. Read the "Today" section before relying on any
number in the contract sections: the contract describes the target, and the
code does not meet all of it yet.

Line numbers are at byblos `0358581` (v0.5.0-1). Planned values are the
design's estimates and are final only when the phase named for them ships.

## The contract

### Memory budget

`byblos.SetMemoryLimit(n int64) int64` sets how many bytes all byblos
operations in flight may hold together. `n <= 0` means no limit. It returns
the previous value. The default is half of the Go memory limit (`GOMEMLIMIT`
or `debug.SetMemoryLimit`), else half of the cgroup `memory.max` on Linux,
else none. The other half is for the Go runtime and the rest of the program.

An operation computes its peak from headers and reserves it before it
allocates, so a hostile file is refused at the header and not after the
allocation. The estimate covers:

- the xref and object table, reserved from the trailer `/Size` at a measured
  per-object cost before the parse (pdfcpu v0.15.0 held 1512 to 1526 bytes
  live per object on a synthetic file of small dictionaries, against a
  96-byte `XRefTableEntry`; larger objects cost more);
- decoded non-image streams, up to the per-page total;
- the streaming page writer's current page;
- archival-raster bands and PNG transcode rows.

Not in byblos's budget, because the caller holds them and passes them in:

- the input. Every entry point takes an `io.ReadSeeker`, so byblos keeps no
  copy of the file;
- the whole output, which goes to the caller's `io.Writer`;
- page planes, image-stream decodes and the interlaced even-row buffer: the
  caller reserves these from its own planes or ingest budget.

### Per-call caps

A `Limits` struct, zero value safe, bounds one call. A cap is checked from
the header before allocation. The defaults are:

| Cap | Default |
|---|---|
| trailer `/Size` (objects) | 100 000 |
| one non-image stream, decoded | 64 MiB |
| non-image streams per page, decoded | 128 MiB |
| one image stream | its declared decoded size (width x height x components x bpc / 8, plus predictor bytes); any excess is refused |
| pages | 10 000 |
| whole-plane work | 1<<26 px; above it the call works by band |
| font program | 16 MiB |
| q-stack depth | 1024, and form-tree bounds |
| goroutines (`MaxGoroutines`) | `GOMAXPROCS` |

Each new cap is checked against what ocrmypdf accepts before it ships, so no
document today's tools accept is refused for size alone. Caps hold with no
budget set.

### Errors

- `errors.Is(err, byblos.ErrMemoryLimit)`: transient. Other operations hold
  the budget, and this one would fit alone. Retry, or answer HTTP 503.
- `errors.Is(err, byblos.ErrLimit)`, with `*byblos.LimitError{Name, Got, Max}`
  from `errors.As`: permanent. This input breaches a cap, or its own estimate
  is over the whole budget, and it never fits this configuration. It does not
  wrap `ErrMemoryLimit`. Answer 413 or 422, and do not retry.

A caller that runs one operation at a time under a static budget should not
see `ErrMemoryLimit`. If it does, count it as a bug and not as load.

### Time

Every band-level and larger entry point takes `ctx`. byblos checks it at each
page boundary and at every token of its own content walk
(`context.go:105` is the one check; the convention is explained in
`context.go:1-60`). Entry points smaller than a band (one row, one tile) take
no `ctx`; the caller checks `ctx` between calls.

Cancellation latency equals the longest unit byblos cannot interrupt. For
four of the nine document-scale entry points that unit is a whole pdfcpu
parse, because pdfcpu is not context-aware. The doc comment of each entry
point states its measured worst case. A caller sizing a deadline must use the
hostile-page number: one JBIG2 page near the cap decodes 67 million pixels
and takes seconds.

### Concurrency

byblos starts no goroutines of its own on the shipped paths. Its entry points
are safe to call concurrently; only `Inspect` and `ExtractPageRaster` are tested
for it (`concurrency_test.go:24`), the others are untested. Because nothing
inside byblos limits how many calls run at once, the caller bounds that; the
budget above bounds their memory together.

## Today

The code enforces the following now. None of it is configurable.

### Not present

There is no `SetMemoryLimit`, no `ErrMemoryLimit`, no `Limits`, no
`LimitError` and no `ErrLimit` in the module (a search of every non-test
`.go` file finds none). There is no cap on trailer `/Size`, on object count,
on pages, on the decoded size of a non-image stream, or on the per-page
total. A breach below returns a plain `fmt.Errorf`, which a caller cannot
tell apart from a malformed file.

### Enforced

| Bound | Value | Where |
|---|---|---|
| PNG embed: declared width x height | 1<<26 px (67 108 864) | `embed.go:34`, checked at `embed.go:67` |
| `EncodedImage` width x height, and each axis | 1<<26 px | `internal/pdfbuild/pdfbuild.go:146`, checked at `:263-267` |
| `Downsample` source bounds | 500 000 000 px | `downsample.go:101`, checked at `:102` |
| Page raster for `RenderPage` | 20 000 px a side and 1<<26 px | `internal/render/render.go:78-79`, checked at `:222` |
| Font bytes retained per render | 16 MiB | `internal/render/render.go:125` |
| Flattened path points | 1<<21 | `internal/render/render.go:98` |
| Scanline fill work | 1<<28 | `internal/render/render.go:108` |
| Render q-stack depth | 1024 | `internal/render/render.go:84` |
| Render operands | 8192 | `internal/render/render.go:721` |
| Content-walk q-stack depth | 1024 | `internal/content/walk.go:408` |
| Content-walk form nesting | 8 | `internal/content/walk.go:387` |
| Content-walk operands | 8192 | `internal/content/walk.go:391` |
| Content-walk text shows | 1<<16 | `internal/content/walk.go:397` |
| Content-walk form invocations | 4096 | `internal/content/walk.go:422` |
| Font program (`/FontFile*`) decoded | 16 MiB | `internal/pdfdoc/pdfdoc.go:965`, applied at `:1076-1079` |
| JBIG2 page pixels | 1<<26 | `internal/jbig2/segment_decode.go:397`, checked at `:1173` (page width at `:1057`) |
| JBIG2 retained bitmap bytes | 16 MiB | `internal/jbig2/segment_decode.go:398` |
| JBIG2 segments per stream | 1<<16 | `internal/jbig2/segment_decode.go:401` |
| JBIG2 references per stream | 1<<18 | `internal/jbig2/segment_decode.go:402` |
| JBIG2 region overdraw | 4x | `internal/jbig2/segment_decode.go:399` |
| JBIG2 dictionary check in `DecodeJBIG2Generic` | 1<<26 px | `jbig2.go:287` |
| Any one decoded stream, via pdfcpu | 512 MiB | pdfcpu v0.16.4 `pkg/filter/filter.go:49`; relied on at `internal/pdfdoc/pdfdoc.go:563` |

The JBIG2 limits are a decoder budget, with the rules and their reasoning
written at `internal/jbig2/segment_decode.go:72-320`. They bound what a
stream can cost whatever the stream says; they are the only part of byblos
that already meets the "refuse before allocating" rule end to end.

The pdfcpu bound is the only one on an ordinary stream decode, and it is
generous: a single stream may inflate to 512 MiB.

`ctx` entry points exist today for the nine document-scale primitives,
`BuildFromPagesContext` and `RenderPageContext`: `InspectContext` (`inspect.go:213`),
`ExtractPageRasterContext` (`extract.go:437`), `OptimizeContext`
(`optimize.go:129`), `StampTextLayerContext` (`stamp.go:185`),
`BuildPDFContext` (`build.go:70`), `BuildFromPagesContext`
(`editpages.go:143`), `ReplaceImagesContext` (`substitute.go:99`),
`RecordExtractionContext` (`provenance.go:450`), `WriteProvenanceContext`
(`provenance.go:366`), `ReadProvenanceContext` (`provenance.go:531`) and
`RenderPageContext` (`render.go:50`). `Sauvola`, `QuantizePNG`,
`QuantizeIndexed`, `Downsample`, `DownsampleDeclaredBPC`,
`EncodeJBIG2Generic`, `DecodeJBIG2Generic` and `EmbedPNG` take no `ctx`; the
design reviewed these as image-scale calls bounded by one in-memory image the
caller chose to pass (`context.go:27-35`), and that decision is revisited
against owner decision D2 (ctx on every band-level and larger entry point)
when the budget lands.

### Planned

| What | Phase |
|---|---|
| `SetMemoryLimit`, `ErrMemoryLimit`, the budget reservations for the object table, decoded streams | 2.1 (inspect), red first on the bomb fixture |
| Budget reservations for the streaming writer's page and PNG transcode rows | 2.2 |
| Budget reservations for archival-raster bands | 3.6 |
| `Limits`, `LimitError`, `ErrLimit` | 2.1 |
| Trailer `/Size` cap (100 000), 64 MiB per non-image stream, 128 MiB per page total, image streams capped at their declared size, 10 000 pages | 2.1 |
| Limit tests of 9A.4: `TestEstimateCoversAllocation`, `TestRefusesBeforeAllocating`, `TestBudgetHolds`, `TestDefaultsAreSafe`, cancellation, goroutines | 2.1, then each stage that calls byblos adds its own |
| Measured cost of the limiter, by jp2k-go's method, replacing the note below | 2.1 exit |
| Banded `ExtractPageRaster` and ceiling refusal before allocation (the 1<<26 px design point decides banding, not refusal) | 3.5 |
| Making the existing caps (PNG, `EncodedImage`, render, font, q-stack) configurable through `Limits` and returning `*LimitError` | 2.1 |

## Cost of the limiter

Not measured yet, because the limiter does not exist. The 2.1 exit measures
it with the method of jp2k-go's memory-limits guide (throughput and p99 at 1,
2 and 4 CPUs, a budget that never binds against none, forced GCs) and this
section then holds the numbers.

## Handling the errors

- `ErrMemoryLimit`: answer 503 with `Retry-After: 1`, or retry once.
- `*LimitError` or `ErrLimit`: park the document with the cap's name and do
  not retry.
- Put a deadline on the context. Waiting for the budget has no timeout of its
  own.
- Do not rely on cancellation to stop a pdfcpu parse early; size the
  deadline for the worst case in the doc comment, and keep a watchdog outside
  the call.
