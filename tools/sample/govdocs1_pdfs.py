#!/usr/bin/env python3
"""Extract PDFs from the govdocs1 zip archives without downloading them whole.

Digital Corpora publishes govdocs1 as 1000 zip files of mixed-format documents.
Only the PDFs are wanted and they are a minority of each archive, so this reads
each zip's central directory over HTTP and pulls just the PDF members by byte
range.

Every extracted file is reported on stdout as a manifest row:

    <path>\t<zip-url>#<member>\t<bytes>\t<sha256>

The source column names the zip and the member, not the file, because that pair
is what makes the sample reproducible: re-running against the same zips yields
the same bytes, and the hash proves it.

    govdocs1_pdfs.py <outdir> <budget-bytes> <zip-url>...
    govdocs1_pdfs.py --members <outdir> <zip-url> <member>...
"""

import hashlib
import os
import sys

from remotezip import RemoteZip


def emit(outdir, url, zf, member, seen):
    """Extract one member. Returns bytes written, or 0 if skipped."""
    name = os.path.basename(member)
    dest = os.path.join(outdir, name)
    if name in seen or os.path.exists(dest):
        return 0
    try:
        data = zf.read(member)
    except Exception as e:                      # a truncated or corrupt member
        print(f"SKIP {member}: {e}", file=sys.stderr)
        return 0

    # govdocs1 carries a handful of members whose payload does not start at byte
    # zero -- MacBinary II wrappers and one with a leading CRLF. Byblos is not
    # the thing under test here, so the container is stripped rather than
    # letting it show up as an unreadable file in the divert numbers.
    if not data.startswith(b"%PDF-"):
        i = data.find(b"%PDF-")
        if i < 0:
            print(f"SKIP {member}: no %PDF- header", file=sys.stderr)
            return 0
        print(f"STRIP {member}: {i} leading bytes", file=sys.stderr)
        data = data[i:]

    with open(dest, "wb") as f:
        f.write(data)
    seen.add(name)
    print(f"{dest}\t{url}#{member}\t{len(data)}\t{hashlib.sha256(data).hexdigest()}",
          flush=True)
    return len(data)


def main():
    args = sys.argv[1:]
    if args and args[0] == "--members":
        outdir, url, members = args[1], args[2], args[3:]
        os.makedirs(outdir, exist_ok=True)
        seen = set()
        with RemoteZip(url) as zf:
            for m in members:
                emit(outdir, url, zf, m, seen)
        return

    if len(args) < 3:
        print(__doc__, file=sys.stderr)
        sys.exit(2)
    outdir, budget, urls = args[0], int(args[1]), args[2:]
    os.makedirs(outdir, exist_ok=True)

    total, seen = 0, set()
    for url in urls:
        if total >= budget:
            break
        try:
            zf = RemoteZip(url)
        except Exception as e:
            print(f"SKIP {url}: {e}", file=sys.stderr)
            continue
        with zf:
            pdfs = [n for n in zf.namelist() if n.lower().endswith(".pdf")]
            print(f"{url}: {len(pdfs)} pdfs of {len(zf.namelist())} members",
                  file=sys.stderr)
            for m in pdfs:
                if total >= budget:
                    break
                total += emit(outdir, url, zf, m, seen)
    print(f"total {total} bytes in {len(seen)} files", file=sys.stderr)


if __name__ == "__main__":
    main()
