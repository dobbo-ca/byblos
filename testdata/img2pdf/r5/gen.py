import io, struct, zlib, json, re, sys, logging, warnings
warnings.simplefilter("ignore"); logging.disable(logging.CRITICAL)
from PIL import Image
import img2pdf
OUT = sys.argv[1]
def jpg(dpi=None):
    im = Image.new("RGB", (48, 36), (200, 50, 50)); b = io.BytesIO()
    kw = {"dpi": dpi} if dpi else {}
    im.save(b, "JPEG", **kw); return b.getvalue()
def png(mode="RGB"):
    im = Image.new(mode, (48, 36), (20, 120, 200, 128)[:len(mode)]); b = io.BytesIO(); im.save(b, "PNG"); return b.getvalue()
def ins_jpg(d, seg):  # after SOI and APP0 if present
    i = 2
    if d[2:4] == b"\xff\xe0": i = 4 + struct.unpack(">H", d[4:6])[0]
    return d[:i] + seg + d[i:]
def app(m, p): return bytes([0xff, m]) + struct.pack(">H", len(p) + 2) + p
def tiff(entries, bo="<"):  # entries: (tag, typ, count, value4bytes)
    h = (b"II*\x00" if bo == "<" else b"MM\x00*") + struct.pack(bo + "I", 8)
    ifd = struct.pack(bo + "H", len(entries)) + b"".join(struct.pack(bo + "HHI", t, ty, c) + v for t, ty, c, v in entries) + b"\0\0\0\0"
    return h + ifd
def orient(v): return tiff([(0x112, 3, 1, struct.pack("<HH", v, 0))])
def chunk(t, b): return struct.pack(">I", len(b)) + t + b + struct.pack(">I", zlib.crc32(t + b) & 0xffffffff)
def ins_png(d, c, before=b"IDAT"):
    i = d.index(before) - 4; return d[:i] + c + d[i:]
def xmp(v, elem=False):
    o = f"<tiff:Orientation>{v}</tiff:Orientation>" if elem else f'tiff:Orientation="{v}"'
    if elem: return f'<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description xmlns:tiff="http://ns.adobe.com/tiff/1.0/">{o}</rdf:Description></rdf:RDF></x:xmpmeta>'.encode()
    return f'<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"><rdf:Description xmlns:tiff="http://ns.adobe.com/tiff/1.0/" {o}/></rdf:RDF></x:xmpmeta>'.encode()
XAP = b"http://ns.adobe.com/xap/1.0/\x00"
def rawprofile(ex):
    h = ex.hex(); lines = "\n".join(h[i:i+72] for i in range(0, len(h), 72))
    return f"\nexif\n{len(ex):8d}\n{lines}\n".encode()
F = {}
def badcrc(t, b): return struct.pack(">I", len(b)) + t + b + b"\0\0\0\0"
def itxt(k, v, lang=b"", comp=False, raw=None):
    body = zlib.compress(v) if comp else v
    if raw is not None: body = raw
    return chunk(b"iTXt", k + b"\0" + (b"\1\0" if comp or raw is not None else b"\0\0") + lang + b"\0\0" + body)
E6 = b"Exif\0\0" + orient(6); E1 = b"Exif\0\0" + orient(1)
NOOR = tiff([(0x131, 2, 4, b"abc\0")])
F = {}
for pre, mode in (("", "RGB"), ("rgba-", "RGBA")):
    p = png(mode)
    F[pre + "pre-exif6-post-exif1.png"] = ins_png(ins_png(p, chunk(b"eXIf", orient(6))), chunk(b"eXIf", orient(1)), b"IEND")
    F[pre + "pre-exif1-post-exif6.png"] = ins_png(ins_png(p, chunk(b"eXIf", orient(1))), chunk(b"eXIf", orient(6)), b"IEND")
    F[pre + "pre-exifnoor-post-xmp6.png"] = ins_png(ins_png(p, chunk(b"eXIf", NOOR)), itxt(b"XML:com.adobe.xmp", xmp(6)), b"IEND")
    F[pre + "pre-exifnoor-post-text-exif6.png"] = ins_png(ins_png(p, chunk(b"eXIf", NOOR)), chunk(b"tEXt", b"exif\0" + E6), b"IEND")
    F[pre + "ztxt-exif6.png"] = ins_png(p, chunk(b"zTXt", b"exif\0\0" + zlib.compress(E6)))
    F[pre + "itxt-exif6.png"] = ins_png(p, itxt(b"exif", E6))
    F[pre + "xmp6-then-text-empty.png"] = ins_png(ins_png(p, itxt(b"XML:com.adobe.xmp", xmp(6))), chunk(b"tEXt", b"XML:com.adobe.xmp\0"))
    F[pre + "text-xmp-noor-then-itxt-badutf8-6.png"] = ins_png(ins_png(p, chunk(b"tEXt", b"XML:com.adobe.xmp\0<x/>")), itxt(b"XML:com.adobe.xmp", xmp(6) + b"\xff"))
    F[pre + "xmp6-then-itxt-corrupt-zlib.png"] = ins_png(ins_png(p, itxt(b"XML:com.adobe.xmp", xmp(6))), itxt(b"XML:com.adobe.xmp", b"", raw=b"\x78\x9cgarbagegarbage"))
    F[pre + "itxt-rawprof-badlang6.png"] = ins_png(p, itxt(b"Raw profile type exif", rawprofile(E6), lang=b"\xff"))
    F[pre + "post-badtype-then-exif6.png"] = ins_png(ins_png(p, chunk(b"a!cd", b"x")), chunk(b"eXIf", orient(6)), b"IEND") if False else None
    q = ins_png(p, chunk(b"eXIf", orient(6)), b"IEND"); i = q.index(b"eXIf") - 4
    F[pre + "post-badtype-then-exif6.png"] = q[:i] + chunk(b"a1cd", b"x") + q[i:]
    F[pre + "post-exif6-badcrc.png"] = ins_png(p, badcrc(b"eXIf", orient(6)), b"IEND")
    F[pre + "pre-exif6-badcrc.png"] = ins_png(p, badcrc(b"eXIf", orient(6)))
    F[pre + "ztxt-comment-1mib.png"] = ins_png(p, chunk(b"zTXt", b"Comment\0\0" + zlib.compress(b"a" * (1 << 20))))
    F[pre + "text-memory-70mb.png"] = ins_png(p, b"".join(chunk(b"zTXt", b"C%d\0\0" % k + zlib.compress(b"a" * 1000000, 9)) for k in range(70)))
oracle = {}
for name, data in F.items():
    open(f"{OUT}/{name}", "wb").write(data)
    try:
        pdf = img2pdf.convert(data)
        mb = re.search(rb"/MediaBox \[([^\]]*)\]", pdf).group(1).split()
        rot = re.search(rb"/Rotate (\d+)", pdf)
        oracle[name] = {"mediabox": [float(x) for x in mb], "rotate": int(rot.group(1)) if rot else 0}
    except Exception as e:
        oracle[name] = {"error": f"{type(e).__name__}: {str(e)[:80]}"}
json.dump(oracle, open(f"{OUT}/oracle.json", "w"), indent=1)
for k, v in oracle.items(): print(k, v)
