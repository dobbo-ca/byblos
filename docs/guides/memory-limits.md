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
- the file itself: pdfcpu keeps the raw bytes of every stream it reads,
  referenced or not (a 200 MB unreferenced stream held 191 MiB live under a
  32 MiB reservation), so the reservation holds the file's size. pdfcpu holds
  a stream once for every xref entry or overlapping `/Length` that reads it
  (8 entries on one 25 MB stream held 191 MB live), so under a budget the
  encoded stream bytes read over one parse are capped at the file's size
  (`file-stream-bytes`, permanent);
- the image records the content walk keeps for all pages together (144 bytes
  each);
- the content walk's retained-bytes account of one page (below);
- decoded non-image streams, up to the per-page total;
- PNG transcode rows and its two compressed outputs (below);
- archival-raster bands.

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
| one indirect object read into memory, stream payload excluded (`MaxObjectBytes`) | `MaxStreamBytes`; under a budget never more than the decode slot |
| estimated heap of the objects pdfcpu parses over one parse (`MaxParsedBytes`) | three quarters of the object table the reservation holds (`/Size` x 1536) plus `MaxTotalDecodeBytes` |
| images, inline images, unresolved `Do` names recorded per page (`MaxImages`, `MaxInlineImages`, `MaxUnresolved`) | 262 144 each (`MaxPageStreamBytes` / 512); past it the call fails with `*LimitError` (`images`, `inline-images`, `unresolved`) rather than dropping images. `InspectDocument` holds the images and inline images of all pages together to the sum of the two caps (`images`), since pages can share one content stream |
| whole-plane work | 1<<26 px; above it the call works by band |
| font program | 16 MiB |
| PNG and `EncodedImage` pixels, render canvas and work, q-stack depth, form depth and invocations | see "Moved into `Limits`" under Today |
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
| Parsed objects | `Limits.MaxParsedBytes` caps the heap of the array elements, dictionary entries and scalars pdfcpu parses over one parse, charged as each is parsed (64 bytes an array element, 96 a dictionary entry, and the length of every string, hex string, name and dictionary key), so one huge object is refused while it grows, not after. The decoded-byte limit cannot do this: 2 bytes of `[]` parse into about 55 of heap, so a 6 KB file packing 3 000 000 empty arrays held 167 MiB live under a 32 MiB budget. The default is three quarters of the object table already reserved plus `MaxTotalDecodeBytes`: an object costs about 260 of its 1536 bytes even when it is `null` (measured), so that share is spent and a huge parsed object cannot have it too, and the charges run above the measured cost (64 against about 55 for an array element), so what the parse holds stays inside the reservation: 5.5 million `[]` elements under 300 MiB peak at 192 MiB live and are refused. A dictionary is charged its map (256 bytes) as well as its entries, since a `<</a 1>>` array element holds about 360 bytes live. Past it, `*LimitError{parsed-bytes}`, at the open or, for a packed object, at `Doc.Page`. The scalar charge is what stops many objects that each fit `MaxObjectBytes`: four 65 MB strings held 313 MB live against a 256 MiB reservation before it. |
| Object buffer | `Limits.MaxObjectBytes` caps one indirect object read into memory, stream payload excluded. pdfcpu reads it in a buffer that grows by copying, so it peaks at about 2.25 times its size, and a string parsed from it aliases it: one 60 MB string held 126 MiB live under a 32 MiB budget. Under a budget the cap is lowered to the decode slot (a quarter of what the table leaves) and the reservation covers 4 times it. Past it, `*LimitError{object-bytes}`, permanent: the object is not skipped. |
| Pages | 10 000 (`pages`), checked after pdfcpu reads the page count. |
| Non-image streams | Content streams, form XObject streams and `/JBIG2Globals` decode under `DecodeWithLimit`: 64 MiB each (`stream-bytes`); content and form streams of one page together 128 MiB (`page-stream-bytes`). A form invoked many times on a page, a self-drawing one included, is decoded once and counts once: every invocation shares the one buffer. `OpenBudgeted` reserves, with the object table and for the document's life, four times the stream cap for the decodes to run in one at a time (pdfcpu's doubling buffer plus its copy peaks at about 3.7x; `TestDecodeReservationCoversPeak`). Decodes reserve nothing of their own: one that did would wait on its own document's table, alone or against other documents doing the same. |
| Image streams | `RawImage` checks width x height against 500 000 000 px (`image-pixels`) from the dictionary, refuses a header with a width or height of 0 or less (pdfcpu reads a limit of 0 as its 512 MiB default and a negative one as none), then sets pdfcpu's per-stream decode limit (`Configuration.Limits.MaxDecodeBytes`) to the image's declared decoded size, four components at the declared depth plus one predictor byte per row (`image-bytes`; at least 1 MiB when the last filter is DCT, JPX or JBIG2, whose Flate layer yields compressed bytes). pdfcpu's default for the same decode is 512 MiB. byblos has no decoder of its own for these streams. The decoded bytes are the caller's to reserve. |
| Font program | 16 MiB (`font-program-bytes`); a font over the cap is skipped, as before. |
| pdfcpu | Object streams are decoded under 64 MiB (`MaxDecodeBytes`); `MaxObjectCount` is the `/Size` cap. `defaultConfig()` carries these and the two cumulative limits at their defaults, so Optimize, Properties and the writer's validator are bounded too. pdfcpu's typed refusals (`ErrLimitExceeded` for the cumulative limits, `filter.ErrDecodeLimitExceeded` for a per-stream decode) come back as `*LimitError` (`objects`, `stream-bytes`); its untyped object-range error on a trailer that understates `/Size` comes back as `*LimitError{objects}`. The cumulative limits exist only in the dobbo-ca fork of pdfcpu (below). |
| Decode slot | The table, the file's size, the image records and one decode are reserved together, once, for the document's life: the decode is 4 x the stream cap, which is the measured peak of pdfcpu's doubling output buffer. The slot is held whether or not the file has a large stream, because a deflate stream's size cannot be known before it is inflated. After the table and the file, an eighth of what is left is the image records and a quarter of the rest the slot. Where the budget cannot hold 4 x 64 MiB, the stream cap is lowered to that quarter (at least 1 MiB) and the document still opens (`MaxTotalDecodeBytes` is lowered with it, and a value above the stream cap is reserved for in full). Under a budget the page total (`page-stream-bytes`) is lowered to that slot as well, since a page's decoded streams are held while the next one decodes and the slot is all the reservation covers: one 8 MB stream named 16 times in `/Contents` was 128 MB of content under a 32 MiB budget. Without a budget nothing is reserved and the 128 MiB default stands; a stream past the lowered cap is refused as `stream-bytes`. Under kleio's 300 MiB, 40 000 objects leave a cap of about 59 MiB; a table plus file that alone exceed the budget is `memory-budget`, permanent: a 20 MB file with a 20 MB unreferenced stream is refused under a 16 MiB budget before pdfcpu reads it. |
| Retained bytes | One account per page walk (`Limits.MaxRetainedBytes`, `retained-bytes`, permanent). Everything the walk keeps is charged to it before it is kept: each `Paint` (192 bytes), `TextShow` (152) plus the length of its font name (`Tf` copies the name on every call), image and inline-image `Placement` (136), plus a 32-byte clip box when the record has a clip; plus, for a `Paint`, the length of each colour's space name and 8 bytes per colour component of both its fill and stroke (`cs`, `sc` and the colour operators build these fresh on every walk of a form); each unresolved `Do` name (16 plus its length), each image placement's name length; and a text string's length unless it aliases the stream. A string with no escape is a slice of the buffer being walked, so every draw of one form shares the form's one decoded buffer and costs only its record; an escaped or hex string is a fresh copy and is charged. Sizes are `unsafe.Sizeof`, not measurements; slice growth slack is in the margin of the share below, not in the charge. Default 128 MiB, which holds the record caps at their maximum (65 536 text shows and 262 144 paints, about 66 MiB). Under a budget it is reserved with the document: a fifth of what the table and file leave, so the decode slot is a fifth smaller than before, and a page needing more is refused (300 MiB holds the maximum). It is the walk's cost per page only; so are the resource scopes a form `Do` appends, which `Page` resets (a `Page.Scope` is valid until the next `Page` call), so they never grow with the page count; the document-level lists `InspectDocument` keeps (the image records of all pages) stay under the `images` cap and the image-record reservation below. Before it, a 4.6 KB file drawing a form of one 1 MiB string 4096 times held 1 GB live at 32 MiB, and 300 000 paints plus 70 000 text shows kept about 110 MiB whatever the budget. |
| Image records | `InspectDocument` records every image of every page until it returns, 144 bytes each (`TestImageRecordBytes`). Under a budget the records are reserved: an eighth of what the table and file leave, and the images and inline images of all pages together are held to half of that count each, so pages sharing one content stream cannot reach the unlowered 524 288 (75 MB) under a 32 MiB budget; about 14 500 each there. Past it, `*LimitError{images}`. Each record also keeps its `/Decode` numbers: a direct array is part of the image's own dictionary, which the parse cap already counts, and an indirect array is converted once per object (`numberArray`) and shared by every image naming it, so K images over one M-entry array hold one 8*M copy, not K. Before that, 1 000 images over one 100 000-entry array held 770 MiB at a 300 MiB budget. |

The caps are checked in `Open`, so every entry point that opens a document
through it enforces them, at their defaults or at the `Limits` its `...Limits`
form takes. `ReadProvenance`, `WriteProvenance` and the rewrite inside
`Optimize` parse through pdfcpu directly and enforce only pdfcpu's own caps
(the fork's, below); `OptimizeLimits` runs an `OpenLimits` gate first, so the
byblos caps still apply to its input. Only `InspectDocument` reserves from the
budget; the others reserve nothing yet (see Planned).

### The pdf-go fork

The three cumulative limits are `MaxObjectStreamTotalCount`, `MaxTotalDecodeBytes` and `MaxParsedBytes` in
`github.com/dobbo-ca/pdf-go` (a dobbo-ca-owned hard fork of pdfcpu). byblos and its
consumers import it directly; no `replace` is needed.

### Moved into `Limits` (owner decision D2)

Each cap below is a `Limits` field; zero takes the default, which is the old
constant, so nothing changes for a caller that passes `Limits{}`. A breach is a
permanent `*LimitError` with the name shown, checked from the header or the
page box before the allocation it guards. The content-walk caps reach the walk
through the `Limits` of `InspectDocument`, `ExtractPageRasterLimits` and
`OptimizeLimits` (its JPEG pass); `BuildFromPagesLimits` opens every source under the parse caps; the other entry points that walk a page use
the defaults. `InspectDocument` keeps a q-depth, form-depth or form-invocations
breach at the default a page diagnostic, as before; once the caller sets the
field, a breach fails the call.

| Cap (field) | Default | Error name | Entry point |
|---|---|---|---|
| PNG and `EncodedImage` width x height, and each axis of an `EncodedImage` (`MaxEncodedImagePixels`) | 1<<26 px | `encoded-image-pixels` | `EmbedPNGLimits`, `EmbedJP2`, `TranscodePNG`, `BuildPDFLimits`, `NewBuildWriter` |
| Text decoded from a PNG's text chunks by `EXIFRotationLimits` (`MaxPNGTextBytes`; every zTXt and compressed iTXt is inflated and counted, only the Exif, XMP and exif ones are kept) | 64 MiB | `png-text-bytes` | `EXIFRotationLimits` |
| `Downsample` source bounds (`MaxImagePixels`, the image cap above) | 500 000 000 px | `image-pixels` | `DownsampleLimits`, `DownsampleDeclaredBPCLimits` |
| `RenderPage` canvas, a side (`MaxRasterSide`) and total (`MaxRasterPixels`) | 20 000 px, 1<<26 px | `raster-side`, `raster-pixels` | `RenderPageLimits` |
| Flattened path points (`MaxPathPoints`) | 1<<21 | `path-points` | `RenderPageLimits` |
| Scanline fill work (`MaxFillWork`) | 1<<28 | `fill-work` | `RenderPageLimits` |
| q-stack depth, render and content walk (`MaxGStateDepth`) | 1024 | `q-depth` | `RenderPageLimits`, `InspectDocument`, `ExtractPageRasterLimits`, `ReplaceImagesLimits`, `RecordExtractionLimits`, `OptimizeLimits` |
| Content-walk form nesting (`MaxFormDepth`) | 8 | `form-depth` | `InspectDocument`, `ExtractPageRasterLimits`, `ReplaceImagesLimits`, `RecordExtractionLimits`, `OptimizeLimits` |
| Content-walk form invocations (`MaxFormInvocations`) | 4096 | `form-invocations` | `InspectDocument`, `ExtractPageRasterLimits`, `ReplaceImagesLimits`, `RecordExtractionLimits`, `OptimizeLimits` |
| Font bytes retained per render (`MaxRenderFontBytes`) | 16 MiB | none: further fonts are skipped | `RenderPageLimits` |
| Pending operands, render and content walk (`MaxOperands`) | 8192 | none: further operands are dropped | `RenderPageLimits`, `InspectDocument`, `ExtractPageRasterLimits`, `ReplaceImagesLimits`, `RecordExtractionLimits`, `OptimizeLimits` (its JPEG pass only) |
| Recorded text shows (`MaxTextShows`) | 1<<16 | none: the walk stops recording and carries on | `InspectDocument`, `ExtractPageRasterLimits`, `ReplaceImagesLimits`, `RecordExtractionLimits`, `OptimizeLimits` (its JPEG pass only) |
| Recorded paints (`MaxPaints`) | 1<<18 | none: the walk stops recording and carries on | `InspectDocument`, `ExtractPageRasterLimits`, `ReplaceImagesLimits`, `RecordExtractionLimits`, `OptimizeLimits` (its JPEG pass only) |

The last four are caps that truncate by design, not refuse: a page past them
still classifies, and refusing would turn a dense vector map that passes today
into an error. They are configurable and nothing else.

`EmbedPNG`, `BuildPDFContext`, `RenderPageContext`, `Downsample` and
`DownsampleDeclaredBPC` stay as thin wrappers over the `...Limits` forms with
`Limits{}`; kleio and the tests call them.

### Enforced, still constants

| Bound | Value | Where |
|---|---|---|
| JBIG2 page pixels | 1<<26 | `jbig2.go` (`maxJBIG2Pixels`): the image dictionary's declared size is checked against it before any decode |

JBIG2 is decoded by `github.com/dobbo-ca/jbig2-go` (v0.2.0), and byblos's budget
is the only pool the decode draws on, as for DCT. Before anything is reserved
or decoded, `jbig2.PageSize` reads the page size from the headers and a page
that disagrees with the image dictionary is refused with no pixel decoded. Then
`decodeJBIG2Placement` reserves, from `SetMemoryLimit`'s budget, the
one-byte-per-pixel raster plus the most jbig2-go can estimate for a decode under
these caps (`jbig2DecodeBound`: the 16 MiB packed-bitmap cap, 96 bytes a declared
symbol up to 1<<16, 256 a segment up to 1<<16, the stream and 256 KiB of context
arrays; about 38 MiB), and calls `jbig2.Decode` with `Reserved: true` and
`MaxBytes` set to the decode's part of it, so jbig2-go's own process-wide pool
(`jbig2.SetMemoryLimit`) is skipped. Caps handed to it are the old decoder's:
`maxJBIG2Pixels` for pages, regions and symbols together (including a globals
dictionary), 1<<16 symbols and 1<<16 segments, so what is refused as
`unsupported-codec-jbig2` is unchanged.

A reservation larger than the whole budget, and a `*jbig2.LimitError` named
`MaxBytes` or `MemoryLimit`, are a `*LimitError` (permanent). A budget held by
others waits until ctx ends and is `ErrMemoryLimit` (transient), the same as a
DCT decode. A cancelled ctx comes back as the context's error and moves no
counter.

A striped page (height 0xFFFFFFFF, sized from its regions) has no size in its
headers, so `jbig2.PageSize` returns `ErrNoPageSize` for it, as it does for a
stream with no page-information segment or malformed headers. byblos does not
refuse any of them for that: the reservation above does not depend on the
page's size, `jbig2.Decode` refuses what is malformed or unsupported, and the
decoded size is compared with the dictionary afterwards.

`ctx` entry points exist today for the nine document-scale primitives,
`BuildFromPagesContext`, `RenderPageContext` and the `...Limits` forms above: `InspectContext` (`inspect.go:213`),
`ExtractPageRasterContext` (`extract.go:437`), `OptimizeContext`
(`optimize.go:129`), `StampTextLayerContext` (`stamp.go:185`, `StampTextLayerLimits` for caps),
`BuildPDFContext` (`build.go:70`), `BuildFromPagesContext`
(`editpages.go:143`, `BuildFromPagesLimits` for caps), `ReplaceImagesContext` (`substitute.go:99`),
`RecordExtractionContext` (`provenance.go:450`, `RecordExtractionLimits` for caps), `WriteProvenanceContext`
(`provenance.go:366`), `ReadProvenanceContext` (`provenance.go:531`) and
`RenderPageContext` (`render.go:50`). `Sauvola`, `QuantizePNG`,
`QuantizeIndexed` and `EmbedPNG` take no `ctx`; the
design reviewed these as image-scale calls bounded by one in-memory image the
caller chose to pass (`context.go:27-35`), and that decision is revisited
against owner decision D2 (ctx on every band-level and larger entry point)
when the budget lands.

### PNG transcode and the streaming page writer (2.2)

`TranscodePNG` reserves, from the header and before allocating: 2 x (row + 1)
bytes of row buffers (a row is the image width at the PNG's bits per pixel), the
colour and mask rows, and 1 MiB per zlib writer (two with a mask). Its two
compressed outputs grow under the budget as they are built (twice the bytes held,
since `append` doubles), and are released on return; the returned image is the
caller's to account for. An Adam7 file also needs (height + 1) / 2 rows at native
sample size, the even rows that passes 1 to 6 fill; the caller's `scratch`
function supplies them, else they are reserved here. The pixel cap
(`MaxEncodedImagePixels`) is checked first, each axis and the product.

`BuildWriter` holds nothing of its own beyond one number per page object; the
page being added is the caller's bytes (TranscodePNG's output, for instance),
so the budget it was built under is the one to size. `MaxPages` caps the pages.

### Planned

| What | Phase |
|---|---|
| Budget reservations for the other entry points' object tables and decoded streams | each stage that calls byblos (2.1 did `InspectDocument`) |
| Budget reservations for archival-raster bands | 3.6 |
| Limit tests of 9A.4 for the other entry points | each stage that calls byblos adds its own (2.1 did `InspectDocument`) |
| Banded `ExtractPageRaster` and ceiling refusal before allocation (the 1<<26 px design point decides banding, not refusal) | 3.5 |

## Cost of the limiter

`InspectDocument` takes one reservation per document, for the object table, the
file, the image records and one decode together, and releases it when the call returns. Uncontended, with a
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
