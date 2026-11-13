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
`Document`. Accepted by the owner (through the kleio primary) on 2026-11-13, byblos#141.
