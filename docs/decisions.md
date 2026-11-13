# Decisions

## OpenDocument keeps the parse unreserved while open (2026-11-13)

`OpenDocument` parses a PDF once and serves `Info` and every page's `PageRaster` from that parse.
While a `Document` is open, the parsed document's own memory (the object table and the file bytes)
is not reserved in the memory pool. Each `PageRaster` call reserves only that page's own need and
releases it when the page is done, exactly as the one-shot `ExtractPageRasterLimits` does, and
per-page decoded state is dropped after each page.

Why: the first version held the parse's memory plus a page slot until `Close`. A page that needed
more than the budget left beside that reservation waited for bytes that only its own `Close` could
return, and the pool's FIFO queue made every other budgeted call wait behind it. It also lowered
the caps under a budget, so its output could differ from the one-shot path.

Bound: the unreserved parse memory is limited by the existing object-count and file-size caps
(`Limits`), the same bound the one-shot path relies on; it only lives longer, for the life of the
`Document`. Accepted by the kleio primary review session on 2026-11-13, byblos#141.

## JBIG2 through jbig2-go: two accepted deviations (2026-11-13)

byblos decodes JBIG2 with `github.com/dobbo-ca/jbig2-go` v0.2.0 on byblos's own memory budget
(`jbig2.PageSize` before the reservation, then `Decode` with `Reserved` and `MaxBytes`). Two cases
differ from the decoder this replaced:

1. A striped page (page height 0xFFFFFFFF) has no size in its headers, so `PageSize` cannot answer.
   byblos does not refuse such a page for that reason: it takes the size from the image
   dictionary, reserves the decode bound, and checks the decoded size afterwards. A striped page
   whose size differs from its dictionary is therefore decoded and then refused, where the old
   decoder refused it from its regions' headers. The decode stays inside the existing
   `maxJBIG2Pixels` (1<<26 px) bound. No plan-based size API is added to jbig2-go for this now.
2. A malformed (not striped) stream also gets no size from `PageSize`, and byblos cannot tell it
   from a striped one, so it reserves the decode bound (about 38 MiB) before `Decode` refuses it.
   Under a normal budget the result is the old one (`unsupported-codec-jbig2`). Only under a byblos
   budget below the decode bound does it become a permanent `*LimitError` instead.

Accepted by the kleio primary review session on 2026-11-13, byblos#146.
