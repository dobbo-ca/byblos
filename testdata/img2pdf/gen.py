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

out = {}
for name, data in sorted(files.items()):
    if not name.startswith("exif-") or name == "exif-6.jpg" or name.startswith("exif-dpi"):
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
