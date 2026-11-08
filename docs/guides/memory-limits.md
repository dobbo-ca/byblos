# Memory limits: what byblos bounds, and what it will

This page states the resource-limit contract every dobbo-ca library meets
(section 9A.1 of the kleio in-process pipeline design) as it applies to
byblos, then lists exactly what the code enforces today and what is planned,
with the phase that lands it. Read the "Today" section before relying on any
number in the contract sections: the contract describes the target, and the
code does not meet all of it yet.

Planned values are the
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
| trailer `/Size` (objects) | 200 000 |
| one non-image stream, decoded | 64 MiB |
| non-image streams per page, decoded | 128 MiB (the decode slot, under a budget) |
| one image stream | its declared decoded size (width x height x components x bpc / 8, plus predictor bytes); any excess is refused |
| pages | 10 000 |
| object stream entries (`MaxObjectStreamEntries`), summed over one parse | the object cap above |
| bytes pdfcpu decodes over one parse (`MaxTotalDecodeBytes`) | 64 MiB (`MaxStreamBytes`) |
| estimated heap of the objects pdfcpu parses over one parse (`MaxParsedBytes`) | three quarters of the object table the reservation holds (`/Size` x 1536) plus `MaxTotalDecodeBytes` |
| images, inline images, unresolved `Do` names recorded per page (`MaxImages`, `MaxInlineImages`, `MaxUnresolved`) | 262 144 each (`MaxPageStreamBytes` / 512); past it the call fails with `*LimitError` (`images`, `inline-images`, `unresolved`) rather than dropping images. `InspectDocument` holds the images and inline images of all pages together to the sum of the two caps (`images`), since pages can share one content stream |
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

Line numbers in this section were taken at byblos `98656f8`; the entry points
that changed in 2.1 are named, not numbered.

### Added in 2.1 (inspect)

`SetMemoryLimit`, `Limits`, `LimitError`, `ErrLimit`, `ErrMemoryLimit` and
`InspectDocument` exist (`limits.go`, `internal/limits`). `InspectDocument` is
`InspectContext` under a `Limits`, and its `DocumentInfo` adds `Encrypted`
(opened with the empty password), `HasSignature` (`/AcroForm /SigFlags` bit 1)
and `NeedsRendering` (dynamic XFA). A document that needs a user password
returns an error wrapping `ErrPassword`.

| What | Behaviour |
|---|---|
| Budget | `SetMemoryLimit` and the FIFO wait under `ctx` as in the contract. Default: half of `GOMEMLIMIT`, else half of the cgroup `memory.max`, else none. |
| Object table | Reserved before the parse as `/Size` x 1536 bytes. `TestEstimateCoversAllocation` holds the estimate to between 1.0x and 1.25x of the live heap pdfcpu's parse leaves (measured 1512 to 1519 bytes per object at 20 000 and 100 000 objects). |
| `/Size` and object bound | The object count is the larger of the trailer `/Size` and the count of `obj` header tokens (`endobj` excepted), found by one fixed-buffer pass over the file before pdfcpu reads anything. The trailer `/Size` is read from the trailer or cross-reference stream dictionary the last `startxref` points to (found by a backwards chunked search, so junk after `%%EOF` does not hide it). Cap 200 000 (`objects`). The count sizes the reservation and pdfcpu's object-count cap (that count plus an eighth, since a linearized file's last trailer can sit a little under the real count), so a trailer that lies low, or a corrupt xref that makes pdfcpu rebuild its table by scanning (which ignores that cap), is refused as `objects` before the parse. The pass kept is the header count, because that is the one thing pdfcpu's own limits cannot bound before it allocates: the rebuild scan inserts a table entry per header without consulting a cap. Objects packed in object streams have no header and are no longer estimated here (see the next row). Cost of the pass on a 250 MiB input (Apple M-series Mac, warm cache): about 0.4 s, 5 allocations, 175 KB. |
| Object streams | pdfcpu counts them itself, per parse. `Limits.MaxObjectStreamEntries` caps the objects packed in object streams (their `/N`) summed over all object streams of one parse; the default is the object cap the reservation covers (`MaxObjects` after the header count above), since a packed object has a table entry like any other. `Limits.MaxTotalDecodeBytes` caps the bytes pdfcpu decodes over one parse (decoding a stream twice counts it twice); the default is `MaxStreamBytes`, so what the parse retains fits the one decode slot the reservation holds, and a budget lowers it with the stream cap. Both are set at every parse, `Open` and `OpenBudgeted` alike. Past the first, `*LimitError{objects}`; past the second, `*LimitError{stream-bytes}`. Object streams are decoded when their first object is read, so the second can end a call at a page (`Doc.Page`) as well as at the open. This replaces the earlier scan of every `/ObjStm` dictionary for its `/N`, which pdfcpu's own tokenizer could read differently (a form feed, a comment or a reference between `/First` and its value) and a decoy comment could inflate. |
| Parsed objects | `Limits.MaxParsedBytes` caps the heap of the array elements and dictionary entries pdfcpu parses over one parse, charged as each is parsed (64 bytes an array element, 96 a dictionary entry), so one huge object is refused while it grows, not after. The decoded-byte limit cannot do this: 2 bytes of `[]` parse into about 55 of heap, so a 6 KB file packing 3 000 000 empty arrays held 167 MiB live under a 32 MiB budget. The default is three quarters of the object table already reserved plus `MaxTotalDecodeBytes`: an object costs about 260 of its 1536 bytes even when it is `null` (measured), so that share is spent and a huge parsed object cannot have it too, and the charges run above the measured cost (64 against about 55 for an array element), so what the parse holds stays inside the reservation: 5.5 million `[]` elements under 300 MiB peak at 192 MiB live and are refused. A dictionary is charged its map (256 bytes) as well as its entries, since a `<</a 1>>` array element holds about 360 bytes live. Past it, `*LimitError{parsed-bytes}`, at the open or, for a packed object, at `Doc.Page`. |
| Pages | 10 000 (`pages`), checked after pdfcpu reads the page count. |
| Non-image streams | Content streams, form XObject streams and `/JBIG2Globals` decode under `DecodeWithLimit`: 64 MiB each (`stream-bytes`); content and form streams of one page together 128 MiB (`page-stream-bytes`). A form invoked many times on a page counts once. `OpenBudgeted` reserves, with the object table and for the document's life, four times the stream cap for the decodes to run in one at a time (pdfcpu's doubling buffer plus its copy peaks at about 3.7x; `TestDecodeReservationCoversPeak`). Decodes reserve nothing of their own: one that did would wait on its own document's table, alone or against other documents doing the same. |
| Image streams | `RawImage` checks width x height against 500 000 000 px (`image-pixels`) from the dictionary, refuses a header with a width or height of 0 or less (pdfcpu reads a limit of 0 as its 512 MiB default and a negative one as none), then sets pdfcpu's per-stream decode limit (`Configuration.Limits.MaxDecodeBytes`) to the image's declared decoded size, four components at the declared depth plus one predictor byte per row (`image-bytes`; at least 1 MiB when the last filter is DCT, JPX or JBIG2, whose Flate layer yields compressed bytes). pdfcpu's default for the same decode is 512 MiB. byblos has no decoder of its own for these streams. The decoded bytes are the caller's to reserve. |
| Font program | 16 MiB (`font-program-bytes`); a font over the cap is skipped, as before. |
| pdfcpu | Object streams are decoded under 64 MiB (`MaxDecodeBytes`); `MaxObjectCount` is the `/Size` cap. `defaultConfig()` carries these and the two cumulative limits at their defaults, so Optimize, Properties and the writer's validator are bounded too. pdfcpu's typed refusals (`ErrLimitExceeded` for the cumulative limits, `filter.ErrDecodeLimitExceeded` for a per-stream decode) come back as `*LimitError` (`objects`, `stream-bytes`); its untyped object-range error on a trailer that understates `/Size` comes back as `*LimitError{objects}`. The cumulative limits exist only in the dobbo-ca fork of pdfcpu (below). |
| Decode slot | The table and one decode are reserved together, once, for the document's life: 4 x the stream cap, which is the measured peak of pdfcpu's doubling output buffer. The slot is held whether or not the file has a large stream, because a deflate stream's size cannot be known before it is inflated. Where the budget cannot hold 4 x 64 MiB after the table, the stream cap is lowered to a quarter of what the table leaves (at least 1 MiB) and the document still opens (`MaxTotalDecodeBytes` is lowered with it, and a value above the stream cap is reserved for in full). Under a budget the page total (`page-stream-bytes`) is lowered to that slot as well, since a page's decoded streams are held while the next one decodes and the slot is all the reservation covers: one 8 MB stream named 16 times in `/Contents` was 128 MB of content under a 32 MiB budget. Without a budget nothing is reserved and the 128 MiB default stands; a stream past the lowered cap is refused as `stream-bytes`. Under kleio's 300 MiB, 40 000 objects leave a cap of about 59 MiB; a table that alone exceeds the budget is `memory-budget`, permanent. |

The caps are checked in `Open`, so every entry point that parses a document
enforces them with their defaults. Only `InspectDocument` takes a `Limits` and
reserves from the budget; the others reserve nothing yet (see Planned).

### The pdfcpu fork

The three cumulative limits are `MaxObjectStreamTotalCount`, `MaxTotalDecodeBytes` and `MaxParsedBytes` in
`github.com/dobbo-ca/pergamon-go` (a fork of pdfcpu; its module path is still
`github.com/pdfcpu/pdfcpu`). byblos's `go.mod` carries

    replace github.com/pdfcpu/pdfcpu => github.com/dobbo-ca/pergamon-go v0.16.4-pergamon.4

**A `replace` in byblos's `go.mod` does not apply to a program that imports byblos.**
Every consumer carries the same line in its own `go.mod` (kleio does), or it
builds against upstream pdfcpu v0.16.4, which does not have the fields, and
fails to compile.

### Not yet moved into `Limits`

The PNG embed, `EncodedImage`, `Downsample`, `RenderPage` raster, flattened
path, scanline, q-stack and operand caps, the content-walk caps and the JBIG2
decoder budget below are still package constants. The content-walk caps are on
the inspect path (they bound what `InspectDocument` retains per page) and are
not in the budget: the walk's retained records are bounded by them, not
reserved. Making them configurable is the last row of the Planned table.

### Enforced, still constants

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
| Content-walk recorded paints | 1<<18 | `internal/content/walk.go` (`maxPaints`) |
| Content-walk form invocations | 4096 | `internal/content/walk.go:422` |
| JBIG2 page pixels | 1<<26 | `internal/jbig2/segment_decode.go:397`, checked at `:1173` (page width at `:1057`) |
| JBIG2 retained bitmap bytes | 16 MiB | `internal/jbig2/segment_decode.go:398` |
| JBIG2 segments per stream | 1<<16 | `internal/jbig2/segment_decode.go:401` |
| JBIG2 references per stream | 1<<18 | `internal/jbig2/segment_decode.go:402` |
| JBIG2 region overdraw | 4x | `internal/jbig2/segment_decode.go:399` |
| JBIG2 dictionary check in `DecodeJBIG2Generic` | 1<<26 px | `jbig2.go:287` |

The JBIG2 limits are a decoder budget, with the rules and their reasoning
written at `internal/jbig2/segment_decode.go:72-320`. They bound what a
stream can cost whatever the stream says; they are the only part of byblos
that already meets the "refuse before allocating" rule end to end.

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
| Budget reservations for the other entry points' object tables and decoded streams | each stage that calls byblos (2.1 did `InspectDocument`) |
| Budget reservations for the streaming writer's page and PNG transcode rows | 2.2 |
| Budget reservations for archival-raster bands | 3.6 |
| Limit tests of 9A.4 for the other entry points | each stage that calls byblos adds its own (2.1 did `InspectDocument`) |
| Banded `ExtractPageRaster` and ceiling refusal before allocation (the 1<<26 px design point decides banding, not refusal) | 3.5 |
| Making the remaining caps (PNG, `EncodedImage`, `Downsample`, render, q-stack, content walk) configurable through `Limits` and returning `*LimitError` | follow-up to 2.1, kleio bead kleio-1eb.74.23.1 |

## Cost of the limiter

`InspectDocument` takes one reservation per document, for the object table and
one decode together, and releases it when the call returns. Uncontended, with a
budget that never binds, a reservation and its release cost 12 ns at 1, 2 and 4
CPUs (`BenchmarkReserve`, `internal/limits`). Contended, with a budget
that holds two reservations while every CPU asks for one, so callers queue
(`BenchmarkReserveContended`, benchtime 200ms, count 3, Apple M4 Pro, hold time
zero so this is the limiter alone):

| CPUs | ns per reservation (throughput) | p99 wait |
|---|---|---|
| 1 | 72 | 42 ns |
| 2 | 111 | 209 ns |
| 4 | 409 | 7.6 us |

The medians of three runs; the runs agreed within 3%. Against a decode that
takes milliseconds this is noise, and the p99 is the wait to be woken, not
work. This is jp2k-go's method reduced to the limiter: it does not measure
decodes, containers or forced GCs, which jp2k-go found to be most of its cost.

## Handling the errors

- `ErrMemoryLimit`: answer 503 with `Retry-After: 1`, or retry once.
- `*LimitError` or `ErrLimit`: park the document with the cap's name and do
  not retry.
- Put a deadline on the context. Waiting for the budget has no timeout of its
  own.
- Do not rely on cancellation to stop a pdfcpu parse early; size the
  deadline for the worst case in the doc comment, and keep a watchdog outside
  the call.
