# Trace a flat mark into an SVG with potrace: one layer per palette colour,
# each from a clean mask of the pixels nearest that colour, painted bottom up
# (the first colour fills the whole silhouette).
import sys, numpy as np, potrace
from PIL import Image, ImageFilter
SRC, OUT = sys.argv[1], sys.argv[2]
# Bottom up: ink (whole silhouette), hair highlights, skin, orange, lens glint.
PALETTE = [("#010d22", (1, 13, 34)), ("#1e3a66", (30, 58, 102)), ("#fbf5e6", (251, 245, 230)),
           ("#fb5f02", (251, 95, 2)), ("#ebb68e", (235, 182, 142))]
im = Image.open(SRC).convert("RGBA")
x0, y0, x1, y1 = im.getchannel("A").point(lambda a: 255 if a > 200 else 0).filter(ImageFilter.MinFilter(5)).getbbox()
pad = 8
im = im.crop((x0 - pad, y0 - pad, x1 + pad, y1 + pad)); W, H = im.size
a = np.asarray(im).astype(int)
d = np.stack([((a[..., :3] - np.array(c)) ** 2).sum(-1) for _, c in PALETTE], -1)
label = d.argmin(-1); label[a[..., 3] < 128] = -1
def layer(keep):
    m = np.isin(label, keep)
    m = np.asarray(Image.fromarray((m * 255).astype("uint8")).filter(ImageFilter.MedianFilter(3))) > 127
    # This potrace traces False as the shape.
    plist = potrace.Bitmap(~m).trace(turdsize=12, alphamax=1.0, opticurve=True, opttolerance=0.2)
    parts = []
    for curve in plist:
        s = curve.start_point; seg = [f"M{s.x:.1f},{s.y:.1f}"]
        for c in curve.segments:
            if c.is_corner: seg.append(f"L{c.c.x:.1f},{c.c.y:.1f}L{c.end_point.x:.1f},{c.end_point.y:.1f}")
            else: seg.append(f"C{c.c1.x:.1f},{c.c1.y:.1f} {c.c2.x:.1f},{c.c2.y:.1f} {c.end_point.x:.1f},{c.end_point.y:.1f}")
        parts.append("".join(seg) + "Z")
    return "".join(parts)
# The base is every colour; each layer above is its own colour only.
layers = [(PALETTE[0][0], layer(list(range(len(PALETTE)))))] + [(hexc, layer([i])) for i, (hexc, _) in enumerate(PALETTE) if i > 0]
body = "".join(f'<path fill="{f}" fill-rule="evenodd" d="{p}"/>' for f, p in layers if p)
S = max(W, H); ox, oy = (S - W) / 2, (S - H) / 2
open(OUT, "w").write(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{-ox:.0f} {-oy:.0f} {S} {S}">{body}</svg>\n')
print(W, H, S, {f: p.count("M") for f, p in layers})
