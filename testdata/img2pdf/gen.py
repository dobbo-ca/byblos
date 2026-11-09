# Regenerates testdata/img2pdf/ from the kleio toolchain image's img2pdf (the
# oracle the in-process ingest must match, spec row 9): the small input
# fixtures and oracle.json, what img2pdf made of each. Manual, never run in CI.
#   docker run --rm -v "$PWD":/w -w /w kleio-toolchain-test:dev python3 testdata/img2pdf/gen.py
import hashlib, io, json, struct, subprocess, tempfile, os, zlib
import img2pdf, pikepdf
from PIL import Image

here = os.path.dirname(os.path.abspath(__file__))
W, H = 16, 12


def grad(mode="RGB", size=(W, H)):
    g = Image.linear_gradient("L").resize(size)
    rgb = Image.merge("RGB", (g, g.transpose(Image.FLIP_LEFT_RIGHT), g.transpose(Image.FLIP_TOP_BOTTOM)))
    return rgb if mode == "RGB" else rgb.convert(mode)


def strip_adobe(b):  # drop the APP14 Adobe segment
    out, i = bytearray(b[:2]), 2
    while i < len(b) and b[i] == 0xFF:
        n = struct.unpack(">H", b[i + 2:i + 4])[0]
        if not (b[i + 1] == 0xEE and b[i + 4:i + 9] == b"Adobe"):
            out += b[i:i + 2 + n]
        if b[i + 1] == 0xDA:
            return bytes(out + b[i + 2 + n:])
        i += 2 + n
    return bytes(out)


def with_res(jp2, vdpi, hdpi):  # splice a res/resc box (metres, exponent 0) into the jp2h box
    n = lambda dpi: round(dpi / 0.0254)
    resc = struct.pack(">I4sHHHHBB", 18, b"resc", n(vdpi), 1, n(hdpi), 1, 0, 0)
    res = struct.pack(">I4s", 8 + len(resc), b"res ") + resc
    i = jp2.index(b"jp2h") - 4
    size = struct.unpack(">I", jp2[i:i + 4])[0]
    return jp2[:i] + struct.pack(">I", size + len(res)) + jp2[i + 4:i + size] + res + jp2[i + size:]


files = {}
buf = io.BytesIO(); grad("CMYK").save(buf, "JPEG", quality=90); files["cmyk.jpg"] = buf.getvalue()
files["cmyk-noadobe.jpg"] = strip_adobe(files["cmyk.jpg"])
buf = io.BytesIO(); grad("L").save(buf, "JPEG2000"); files["gray.jp2"] = buf.getvalue()
buf = io.BytesIO(); grad().save(buf, "JPEG2000"); files["rgb.jp2"] = buf.getvalue()
buf = io.BytesIO(); grad("CMYK").save(buf, "JPEG2000"); files["cmyk.jp2"] = buf.getvalue()
# 48x36 so a 300 dpi page is over img2pdf's 3-point minimum.
big = grad(size=(48, 36))
buf = io.BytesIO(); big.save(buf, "JPEG2000"); files["dpi-300x150.jp2"] = with_res(buf.getvalue(), 150, 300)
buf = io.BytesIO(); big.save(buf, "JPEG", dpi=(300, 150)); files["dpi-300x150.jpg"] = buf.getvalue()
for name, x, unit in (("exif-dpi-300.jpg", 300, 2), ("exif-dpi-cm.jpg", 100, 3)):
    ex = Image.Exif(); ex[0x011A] = ex[0x011B] = x; ex[0x0128] = unit
    buf = io.BytesIO(); big.save(buf, "JPEG", exif=ex); files[name] = buf.getvalue()
buf = io.BytesIO(); big.save(buf, "PNG", dpi=(300, 150)); files["dpi-300x150.png"] = buf.getvalue()
buf = io.BytesIO(); big.convert("RGBA").save(buf, "PNG", dpi=(300, 150)); files["rgba-300x150.png"] = buf.getvalue()
buf = io.BytesIO(); big.convert("RGBA").save(buf, "PNG", dpi=(72, 72)); files["rgba-72.png"] = buf.getvalue()
for o in list(range(1, 9)) + [0, 9]:
    ex = Image.Exif(); ex[0x0112] = o
    buf = io.BytesIO(); grad().save(buf, "JPEG", exif=ex); files["exif-%d.jpg" % o] = buf.getvalue()

# Orientation entries Pillow reads but a strict SHORT/count-1 reader would not
# (oracle only; the Go test patches exif-6.jpg the same way).
e6 = files["exif-6.jpg"]
ent = b"\x01\x12\x00\x03\x00\x00\x00\x01\x00\x06\x00\x00"
assert ent in e6
files["exif-long.jpg"] = e6.replace(ent, b"\x01\x12\x00\x04\x00\x00\x00\x01\x00\x00\x00\x06")
files["exif-short0.jpg"] = e6.replace(ent, b"\x01\x12\x00\x03\x00\x00\x00\x00\x00\x00\x00\x00")
files["exif-short2.jpg"] = e6.replace(ent, b"\x01\x12\x00\x03\x00\x00\x00\x02\x00\x06\x00\x01")

# Files whose Exif, JFIF or pHYs/resc bytes are laid out by hand, to hold the
# readers to what Pillow makes of malformed or unusual metadata. Written to
# testdata as they are (the exif-N and exif-long variants above are patched
# from exif-6.jpg by the Go test instead).
new = {}
buf = io.BytesIO(); grad(size=(16, 12)).save(buf, "JPEG"); small = buf.getvalue()
buf = io.BytesIO(); big.save(buf, "JPEG"); bigjpg = buf.getvalue()


def app(marker, payload):
    return b"\xff" + bytes([marker]) + struct.pack(">H", len(payload) + 2) + payload


def jfif(unit, x, y):
    return app(0xE0, b"JFIF\0\1\1" + bytes([unit]) + struct.pack(">HH", x, y) + b"\0\0")


def jpg_with(jpg, *segs):  # jpg with its JFIF segment replaced by segs
    return jpg[:2] + b"".join(segs) + jpg[4 + struct.unpack(">H", jpg[4:6])[0]:]


def tiff(entries, extra=b""):  # big-endian; entries are (tag, type, count, 4 value bytes)
    ifd = struct.pack(">H", len(entries)) + b"".join(struct.pack(">HHI", t, ty, c) + v for t, ty, c, v in entries)
    return b"MM\0*" + struct.pack(">I", 8) + ifd + b"\0\0\0\0" + extra


S = lambda v: struct.pack(">HH", v, 0)
L = lambda v: struct.pack(">I", v)


def ex(entries, extra=b""):
    return jpg_with(small, jfif(0, 1, 1), app(0xE1, b"Exif\0\0" + tiff(entries, extra)))


new["jfif-two-0then300.jpg"] = jpg_with(bigjpg, jfif(0, 1, 1), jfif(1, 300, 300))
new["jfif-two-72then300.jpg"] = jpg_with(bigjpg, jfif(1, 72, 72), jfif(1, 300, 300))
new["jfif-two-300then0.jpg"] = jpg_with(bigjpg, jfif(1, 300, 300), jfif(0, 1, 1))
new["jfif-300x0.jpg"] = jpg_with(bigjpg, jfif(1, 300, 0))
new["exif-sshort.jpg"] = ex([(0x112, 8, 1, S(6))])
new["exif-slong.jpg"] = ex([(0x112, 9, 1, L(6))])
new["exif-sbyte.jpg"] = ex([(0x112, 6, 1, bytes([6, 0, 0, 0]))])
new["exif-float.jpg"] = ex([(0x112, 11, 1, struct.pack(">f", 6.0))])
new["exif-rational.jpg"] = ex([(0x112, 5, 1, L(26))], struct.pack(">II", 12, 2))
new["exif-unknown-type.jpg"] = ex([(0x112, 99, 1, S(6))])
new["exif-short3-outside.jpg"] = ex([(0x112, 3, 3, L(5000))])
new["exif-dup-6then1.jpg"] = ex([(0x112, 3, 1, S(6)), (0x112, 3, 1, S(1))])
new["exif-dup-1then6.jpg"] = ex([(0x112, 3, 1, S(1)), (0x112, 3, 1, S(6))])
new["exif-bad-offset-first.jpg"] = ex([(0x11A, 5, 1, L(5000)), (0x112, 3, 1, S(6))])
new["exif-bad-offset-after.jpg"] = ex([(0x112, 3, 1, S(6)), (0x11A, 5, 1, L(5000))])
# The second Exif segment continues the first's block, so an offset past its end lands there.
new["exif-two-segments.jpg"] = jpg_with(small, jfif(0, 1, 1), app(0xE1, b"Exif\0\0" + tiff([(0x112, 5, 1, L(26))])), app(0xE1, b"Exif\0\0" + struct.pack(">II", 12, 2)))
new["exif-bad-header.jpg"] = jpg_with(small, jfif(0, 1, 1), app(0xE1, b"Exif\0\0" + b"X" * 16))
new["exif-ifd-outside.jpg"] = jpg_with(small, jfif(0, 1, 1), app(0xE1, b"Exif\0\0MM\0*" + L(9999) + b"\0" * 8))


def with_resc(jp2, body):  # a res box holding a resc box with this body, spliced into jp2h
    resc = struct.pack(">I4s", 8 + len(body), b"resc") + body
    res = struct.pack(">I4s", 8 + len(resc), b"res ") + resc
    i = jp2.index(b"jp2h") - 4
    size = struct.unpack(">I", jp2[i:i + 4])[0]
    return jp2[:i] + struct.pack(">I", size + len(res)) + jp2[i + 4:i + size] + res + jp2[i + size:]


buf = io.BytesIO(); big.save(buf, "JPEG2000"); jp2 = buf.getvalue()
resc = lambda *f: struct.pack(">HHHHBB", *f)
new["jp2-hden0.jp2"] = with_resc(jp2, resc(11811, 1, 11811, 0, 0, 0))
new["jp2-hnum0.jp2"] = with_resc(jp2, resc(11811, 1, 0, 1, 0, 0))
new["jp2-resc12.jp2"] = with_resc(jp2, resc(11811, 1, 11811, 1, 0, 0) + b"\0\0")
new["jp2-3.5.jp2"] = with_resc(jp2, resc(17500, 127, 17500, 127, 0, 0))


def png_with(img, *chunks):  # the PNG of img with these chunks before IDAT
    buf = io.BytesIO(); img.save(buf, "PNG"); d = buf.getvalue()
    i = d.index(b"IDAT") - 4
    return d[:i] + b"".join(struct.pack(">I", len(c)) + t + c + struct.pack(">I", zlib.crc32(t + c)) for t, c in chunks) + d[i:]


phys = lambda x, y, u: (b"pHYs", struct.pack(">IIB", x, y, u))
for mode, pre in (("RGB", ""), ("RGBA", "rgba-")):
    img = big.convert(mode)
    new[pre + "png-aspect-2x1.png"] = png_with(img, phys(2, 1, 0))
    new[pre + "png-aspect-1x3.png"] = png_with(img, phys(1, 3, 0))
    new[pre + "png-aspect-1x1.png"] = png_with(img, phys(7, 7, 0))
    new[pre + "png-x0.png"] = png_with(img, phys(0, 11811, 1))
    new[pre + "png-exif6.png"] = png_with(img, (b"eXIf", tiff([(0x112, 3, 1, S(6))])))
    new[pre + "png-exif2.png"] = png_with(img, (b"eXIf", tiff([(0x112, 3, 1, S(2))])))
files.update(new)

out = {}
for name, data in sorted(files.items()):
    if not name.startswith("exif-") or name == "exif-6.jpg" or name.startswith("exif-dpi") or name in new:
        open(os.path.join(here, name), "wb").write(data)
    ent = {}
    try:
        pdf = pikepdf.open(io.BytesIO(img2pdf.convert(data)))
        pg = pdf.pages[0]
        im = list(pg.images.values())[0]
        ent = {
            "mediabox": [float(x) for x in pg.MediaBox],
            "rotate": int(pg.get("/Rotate", 0)),
            "width": int(im.Width), "height": int(im.Height), "bpc": int(im.BitsPerComponent),
            "filter": str(im.Filter), "colorspace": str(im.ColorSpace),
            "decode": [float(x) for x in im.get("/Decode", [])],
            "stream_is_input": im.read_raw_bytes() == data,
        }
    except Exception as e:
        ent = {"error": str(e).splitlines()[0]}
    out[name] = ent
json.dump(out, open(os.path.join(here, "oracle.json"), "w"), indent=1, sort_keys=True)
print(json.dumps(out, indent=1, sort_keys=True))
