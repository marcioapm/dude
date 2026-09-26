# Trace the flat mark into an SVG with potrace: one layer per colour, each
# from a clean two-tone mask, painted navy (the whole silhouette), then
# cream, then orange.
import numpy as np, potrace
from PIL import Image, ImageFilter
SRC = "/home/marcio/git/dude/docs/design/icon/mascot3/source.png"
im = Image.open(SRC).convert("RGBA")
x0, y0, x1, y1 = im.getchannel("A").point(lambda a: 255 if a > 200 else 0).filter(ImageFilter.MinFilter(5)).getbbox()
pad = 8
im = im.crop((x0 - pad, y0 - pad, x1 + pad, y1 + pad))
W, H = im.size
a = np.asarray(im).astype(int)
pal = {"navy": (6, 39, 83), "cream": (251, 245, 230), "orange": (255, 107, 26)}
names = list(pal)
d = np.stack([((a[..., :3] - np.array(c)) ** 2).sum(-1) for c in pal.values()], -1)
label = d.argmin(-1); label[a[..., 3] < 128] = -1
def layer(keep):
    m = np.isin(label, [names.index(k) for k in keep])
    m = np.asarray(Image.fromarray((m * 255).astype("uint8")).filter(ImageFilter.MedianFilter(5))) > 127
    # This potrace traces False as the shape.
    plist = potrace.Bitmap(~m).trace(turdsize=20, alphamax=1.0, opticurve=True, opttolerance=0.2)
    parts = []
    for curve in plist:
        s = curve.start_point; seg = [f"M{s.x:.1f},{s.y:.1f}"]
        for c in curve.segments:
            if c.is_corner: seg.append(f"L{c.c.x:.1f},{c.c.y:.1f}L{c.end_point.x:.1f},{c.end_point.y:.1f}")
            else: seg.append(f"C{c.c1.x:.1f},{c.c1.y:.1f} {c.c2.x:.1f},{c.c2.y:.1f} {c.end_point.x:.1f},{c.end_point.y:.1f}")
        parts.append("".join(seg) + "Z")
    return "".join(parts)
layers = {"navy": layer(["navy", "cream", "orange"]), "cream": layer(["cream"]), "orange": layer(["orange"])}
fill = {"navy": "#062753", "cream": "#fbf5e6", "orange": "#ff6b1a"}
body = "".join(f'<path fill="{fill[k]}" fill-rule="evenodd" d="{v}"/>' for k, v in layers.items())
# Square viewBox, centred: it drops into any icon slot.
S = max(W, H); ox, oy = (S - W) / 2, (S - H) / 2
svg = f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{-ox:.0f} {-oy:.0f} {S} {S}">{body}</svg>\n'
open("dude.svg", "w").write(svg)
print(W, H, S, len(svg), {k: v.count("M") for k, v in layers.items()})
