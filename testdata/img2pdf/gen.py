# Regenerates testdata/img2pdf/ from the kleio toolchain image's img2pdf (the
# oracle the in-process ingest must match, spec row 9): the small input
# fixtures and oracle.json, what img2pdf made of each. Manual, never run in CI.
#   docker run --rm -v "$PWD":/w -w /w kleio-toolchain-test:dev python3 testdata/img2pdf/gen.py
import hashlib, io, json, struct, subprocess, tempfile, os, zlib
import img2pdf, pikepdf
from PIL import Image

here = os.path.dirname(os.path.abspath(__file__))
W, H = 16, 12


def grad(mode="RGB"):
    g = Image.linear_gradient("L").resize((W, H))
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


files = {}
buf = io.BytesIO(); grad("CMYK").save(buf, "JPEG", quality=90); files["cmyk.jpg"] = buf.getvalue()
files["cmyk-noadobe.jpg"] = strip_adobe(files["cmyk.jpg"])
buf = io.BytesIO(); grad("L").save(buf, "JPEG2000"); files["gray.jp2"] = buf.getvalue()
buf = io.BytesIO(); grad().save(buf, "JPEG2000"); files["rgb.jp2"] = buf.getvalue()
buf = io.BytesIO(); grad("CMYK").save(buf, "JPEG2000"); files["cmyk.jp2"] = buf.getvalue()
for o in list(range(1, 9)) + [0, 9]:
    ex = Image.Exif(); ex[0x0112] = o
    buf = io.BytesIO(); grad().save(buf, "JPEG", exif=ex); files["exif-%d.jpg" % o] = buf.getvalue()

out = {}
for name, data in sorted(files.items()):
    if not name.startswith("exif-") or name == "exif-6.jpg":
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
