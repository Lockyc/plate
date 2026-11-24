# /// script
# requires-python = ">=3.12"
# dependencies = ["torch", "transformers", "onnxruntime", "huggingface_hub", "pillow", "numpy", "scipy"]
# [tool.uv]
# exclude-newer = "2026-11-15T00:00:00Z"
# ///
"""count.py -- count the people in photos and 360 equirect frames.

    count.py --out DIR --detector REPO --detector-revision SHA
             --density REPO --density-revision SHA --density-file FILE
             [--nadir-deg 35] [--zenith-deg 30] [--marked]
             (--photo PATH | --equirect PATH)...

A person is counted one of two ways, never both:
  - a detector person box scoring CONF or better;
  - a density-map peak (a local maximum over PEAK_WIN cells of at least
    PEAK) outside every counted box with at least CROWD other peaks within
    CROWD_R cells: someone too small or packed to box. An isolated peak is
    a sign or a patterned shirt, not a crowd.

An equirect frame is read as perspective views, HFOV wide and VIEW px
square, centred on RINGS. Each direction on the sphere is owned by the
nearest view centre, and a box counts in the view that owns its head (the
top tenth of the box), so a person two views see is counted once.
Directions within --nadir-deg of straight down (the camera's holder) and
--zenith-deg of straight up are owned by no view.

A photo is read whole, for the people large in it, and as TILE px tiles at
full resolution, for the small ones. A tile's box counts when its tile owns
its centre (the nearest tile centre), it is not cut by an inner tile edge
and its centre is outside every whole-photo box: a person large enough to
cross tiles is the whole-photo pass's.

The values are the bake-off's winner over plate's quality crowd assets,
chosen on count and on where the counted people are against the marked
heads.

Writes DIR/counts.tsv (frame, detected, density, estimate = detected +
density), DIR/points/<frame>.tsv (each counted person: lon, lat in degrees
for an equirect frame, else x, y in pixels; and which model counted them)
and with --marked DIR/marked/<frame>.jpg with each counted person dotted
(detector green, density magenta) over the density at 30%, an equirect
frame's views in DIR/marked/<frame>/view-NN.jpg with the region each view
does not own shaded.
"""

import argparse
import math
import os
import sys
import time

import numpy as np
import onnxruntime as ort
import torch
import torch.nn.functional as F
from huggingface_hub import hf_hub_download
from PIL import Image, ImageDraw, ImageOps
from plate_device import pick
from scipy.ndimage import maximum_filter, uniform_filter
from transformers import AutoImageProcessor, AutoModelForObjectDetection

Image.MAX_IMAGE_PIXELS = None

HFOV, VIEW = 75.0, 1280
# (yaw, pitch) of each equirect view: a ring at the horizon; a ring below it
# for the people standing close, whom the first ring cuts off at the chest; a
# ring above it; and straight down and up. Every direction then lies inside
# the view that owns it (geometry_gaps, checked before a run), and a view
# that owns nothing between the cuts is not read.
RINGS = (
    [(i * 30.0, -10.0) for i in range(12)]
    + [(15.0 + i * 60.0, -40.0) for i in range(6)]
    + [(15.0 + i * 60.0, 40.0) for i in range(6)]
    + [(0.0, -90.0), (0.0, 90.0)]
)
WIN, STRIDE = 512, 384  # density windows, px
# Fitted on plate's five quality crowd assets (the bake-off): the detector's
# confidence, the photo tile, and the density peak, its window, and the crowd
# a peak needs around it.
CONF, TILE = 0.5, 1280
PEAK, PEAK_WIN, CROWD, CROWD_R = 0.1, 5, 2, 16
MEAN = np.array([0.485, 0.456, 0.406], np.float32)
STD = np.array([0.229, 0.224, 0.225], np.float32)
DET_DOT, DENS_DOT = (40, 220, 90), (235, 60, 220)


class Frame(argparse.Action):
    def __call__(self, parser, ns, value, option=None):
        ns.frames.append((option.lstrip("-"), value))


def rot(yaw, pitch):
    y, p = math.radians(yaw), math.radians(pitch)
    rp = np.array([[1, 0, 0], [0, math.cos(p), math.sin(p)], [0, -math.sin(p), math.cos(p)]])
    ry = np.array([[math.cos(y), 0, math.sin(y)], [0, 1, 0], [-math.sin(y), 0, math.cos(y)]])
    return ry @ rp


def view_dirs(yaw, pitch):
    """Unit directions of a view's pixel centres: x right, y up, z forward at lon 0."""
    t = math.tan(math.radians(HFOV) / 2)
    c = ((np.arange(VIEW) + 0.5) / VIEW * 2 - 1) * t
    u, v = np.meshgrid(c, c)
    d = np.stack([u, -v, np.ones_like(u)], -1)
    d /= np.linalg.norm(d, axis=-1, keepdims=True)
    return (d @ rot(yaw, pitch).T).astype(np.float32)


def lonlat(d):
    return np.degrees(np.arctan2(d[..., 0], d[..., 2])), np.degrees(np.arcsin(np.clip(d[..., 1], -1, 1)))


CENTRES = np.stack([rot(y, p) @ np.array([0, 0, 1.0]) for y, p in RINGS])


def owner(d, nadir, zenith):
    """The view that owns each direction (its nearest view centre), or -1 inside a cut."""
    _, lat = lonlat(d)
    return np.where((lat > -90 + nadir) & (lat < 90 - zenith), np.argmax(d @ CENTRES.T, -1), -1)


def sphere(n=200_000):
    """n directions spread evenly over the sphere (a Fibonacci lattice)."""
    i = np.arange(n) + 0.5
    phi, th = np.arccos(1 - 2 * i / n), math.pi * (1 + 5**0.5) * i
    return np.stack([np.cos(th) * np.sin(phi), np.cos(phi), np.sin(th) * np.sin(phi)], -1)


def geometry_gaps(nadir, zenith):
    """How many of sphere()'s directions between the cuts lie outside the field of view of the
    view that owns them (0: every person is read by the view that counts them), and the views
    that own any direction."""
    d = sphere()
    own = owner(d, nadir, zenith)
    t = math.tan(math.radians(HFOV) / 2)
    gaps = 0
    for k, (y, p) in enumerate(RINGS):
        c = d[own == k] @ rot(y, p)
        gaps += int(((c[:, 2] <= 0) | (np.abs(c[:, 0]) > t * c[:, 2]) | (np.abs(c[:, 1]) > t * c[:, 2])).sum())
    return gaps, sorted(set(own[own >= 0].tolist()))


class Sphere:
    """The equirect's views and who owns each direction."""

    def __init__(self, img, nadir, zenith):
        self.eq = torch.from_numpy(np.asarray(img, dtype=np.float32)).permute(2, 0, 1)[None]
        self.nadir, self.zenith = nadir, zenith

    def view(self, k):
        d = view_dirs(*RINGS[k])
        lon, lat = lonlat(d)
        grid = torch.from_numpy(np.stack([lon / 180.0, -lat / 90.0], -1)[None]).float()
        rgb = F.grid_sample(self.eq, grid, mode="bilinear", padding_mode="border", align_corners=False)
        return rgb[0].permute(1, 2, 0).clamp(0, 255).byte().numpy(), d

    def owned(self, k, d):
        return owner(d, self.nadir, self.zenith) == k


class Detector:
    def __init__(self, repo, rev, device):
        self.proc = AutoImageProcessor.from_pretrained(repo, revision=rev)
        self.model = AutoModelForObjectDetection.from_pretrained(repo, revision=rev).to(device).eval()
        self.device = device
        self.person = next(int(k) for k, v in self.model.config.id2label.items() if v.lower() == "person")

    def __call__(self, rgb):
        h, w, _ = rgb.shape
        inp = self.proc(images=Image.fromarray(rgb), return_tensors="pt").to(self.device)
        with torch.no_grad():
            out = self.model(**inp)
        r = self.proc.post_process_object_detection(out, threshold=CONF, target_sizes=[(h, w)])[0]
        return [b for s, l, b in zip(r["scores"].tolist(), r["labels"].tolist(), r["boxes"].tolist()) if l == self.person]


class Density:
    """DM-Count: a person-density map at 1/8 scale whose sum is the count."""

    def __init__(self, repo, rev, file, device):
        prov = {"mps": ["CoreMLExecutionProvider"], "cuda": ["CUDAExecutionProvider"]}.get(device, []) + ["CPUExecutionProvider"]
        prov = [p for p in prov if p in ort.get_available_providers()]
        self.s = ort.InferenceSession(hf_hub_download(repo, file, revision=rev), providers=prov)
        self.name = self.s.get_inputs()[0].name
        self.red = 8

    def __call__(self, rgb):
        h, w, _ = rgb.shape
        x = ((rgb.astype(np.float32) / 255 - MEAN) / STD).transpose(2, 0, 1)
        ph, pw = max(WIN, h), max(WIN, w)
        x = np.pad(x, ((0, 0), (0, ph - h), (0, pw - w)))

        def starts(n):
            return sorted({min(i * STRIDE, n - WIN) for i in range(math.ceil((n - WIN) / STRIDE) + 1)})

        r, acc = self.red, np.zeros((ph // self.red, pw // self.red), np.float32)
        cnt = np.zeros_like(acc)
        for y0 in starts(ph):
            for x0 in starts(pw):
                o = self.s.run(None, {self.name: np.ascontiguousarray(x[None, :, y0 : y0 + WIN, x0 : x0 + WIN])})[0]
                o = o.reshape(o.shape[-2:]) if o.shape[1] == 1 else o[0, ..., 0]
                acc[y0 // r : y0 // r + o.shape[0], x0 // r : x0 // r + o.shape[1]] += o
                cnt[y0 // r : y0 // r + o.shape[0], x0 // r : x0 // r + o.shape[1]] += 1
        return (acc / np.maximum(cnt, 1))[: math.ceil(h / r), : math.ceil(w / r)]


def head(box):
    """Where a person box's head is: its top tenth, centred."""
    x0, y0, x1, y1 = box
    return (x0 + x1) / 2, y0 + 0.1 * (y1 - y0), "detector"


def crowd(m, own, boxes, r):
    """The density map m's counted peaks, at pixel scale: owned, outside every box, among other peaks."""
    keep = own.copy()
    for x0, y0, x1, y1 in boxes:
        keep[max(0, int(y0 / r)) : int(y1 / r) + 1, max(0, int(x0 / r)) : int(x1 / r) + 1] = False
    peaks = (m == maximum_filter(m, PEAK_WIN)) & (m >= PEAK)
    side = 2 * CROWD_R + 1
    near = uniform_filter(peaks.astype(np.float32), side, mode="constant") * side * side - 1
    return [((cx + 0.5) * r, (cy + 0.5) * r, "density") for cy, cx in zip(*np.nonzero(peaks & keep & (near >= CROWD - 0.01)))]


def count_view(rgb, owned_px, owned_cells, det, dens):
    """One equirect view: its counted people in view pixels, and its owned density."""
    boxes = det(rgb)
    m = dens(rgb)
    own = owned_cells(m.shape, dens.red)
    people = [p for p in map(head, boxes) if owned_px(p[0], p[1])]
    return people + crowd(m, own, boxes, dens.red), np.where(own, m, 0)


def tiles(n):
    """Tile starts along a side of n px: TILE px tiles, evenly spread, overlapping by a quarter or more."""
    if n <= TILE:
        return [0]
    k = math.ceil((n - TILE) / (TILE * 0.75)) + 1
    return [round(i * (n - TILE) / (k - 1)) for i in range(k)]


def heat(img, m, alpha=0.3):
    """img with the density map m laid over it in red, at alpha where densest."""
    if m.max() <= 0:
        return img.copy()
    a = np.clip(m / max(m.max(), 1e-6), 0, 1)
    layer = Image.new("RGB", img.size, (230, 30, 30))
    mask = Image.fromarray((a * 255 * alpha).astype(np.uint8)).resize(img.size, Image.BILINEAR)
    out = img.copy()
    out.paste(layer, (0, 0), mask)
    return out


def dots(img, pts):
    d = ImageDraw.Draw(img)
    rad = max(4, round(max(img.size) / 400))
    for x, y, src in pts:
        d.ellipse([x - rad, y - rad, x + rad, y + rad], fill=DET_DOT if src == "detector" else DENS_DOT, outline=(0, 0, 0), width=max(1, rad // 3))
    return img


def photo(img, det, dens):
    w, h = img.size
    rgb = np.asarray(img)
    whole = det(rgb)
    boxes = list(whole)
    offs = [(x, y) for y in tiles(h) for x in tiles(w)]
    centres = np.array([(x + min(TILE, w) / 2, y + min(TILE, h) / 2) for x, y in offs])
    for k, (ox, oy) in enumerate(offs):
        tw, th = min(TILE, w), min(TILE, h)
        if (tw, th) == (w, h):
            break  # the photo is one tile: the whole pass has read it
        for x0, y0, x1, y1 in det(rgb[oy : oy + th, ox : ox + tw]):
            cx, cy = (x0 + x1) / 2 + ox, (y0 + y1) / 2 + oy
            mine = np.argmin(((centres - (cx, cy)) ** 2).sum(-1)) == k
            cut = (x0 < 3 and ox > 0) or (y0 < 3 and oy > 0) or (x1 > tw - 3 and ox + tw < w) or (y1 > th - 3 and oy + th < h)
            if mine and not cut and not any(b[0] <= cx <= b[2] and b[1] <= cy <= b[3] for b in whole):
                boxes.append((x0 + ox, y0 + oy, x1 + ox, y1 + oy))
    m = dens(rgb)
    people = [head(b) for b in boxes] + crowd(m, np.ones(m.shape, bool), boxes, dens.red)
    return people, people, lambda: dots(heat(img, m), people), None


def equirect(img, det, dens, nadir, zenith, live):
    W, H = img.size
    sp = Sphere(img, nadir, zenith)
    pts, marked_views, splat = [], [], np.zeros((math.ceil(H / dens.red), math.ceil(W / dens.red)), np.float32)
    for k in live:
        rgb, d = sp.view(k)

        def owned_px(x, y, k=k, d=d):
            return bool(sp.owned(k, d[min(int(y), VIEW - 1), min(int(x), VIEW - 1)][None])[0])

        def cells(shape, r, k=k, d=d):
            ys = np.minimum((np.arange(shape[0]) + 0.5) * r, VIEW - 1).astype(int)
            xs = np.minimum((np.arange(shape[1]) + 0.5) * r, VIEW - 1).astype(int)
            return sp.owned(k, d[ys][:, xs])

        people, m = count_view(rgb, owned_px, cells, det, dens)
        for x, y, s in people:
            lon, lat = lonlat(d[min(int(y), VIEW - 1), min(int(x), VIEW - 1)])
            pts.append((float(lon), float(lat), s))
        # the owned density, splatted back onto the equirect at 1/8 scale
        ys, xs = np.nonzero(m)
        if len(ys):
            r = dens.red
            lon, lat = lonlat(d[np.minimum((ys + 0.5) * r, VIEW - 1).astype(int), np.minimum((xs + 0.5) * r, VIEW - 1).astype(int)])
            ex = np.clip(((lon / 360 + 0.5) * W / r).astype(int), 0, splat.shape[1] - 1)
            ey = np.clip(((0.5 - lat / 180) * H / r).astype(int), 0, splat.shape[0] - 1)
            np.add.at(splat, (ey, ex), m[ys, xs])
        marked_views.append((k, rgb, m, people, cells))
    eq_pts = [((lon / 360 + 0.5) * W, (0.5 - lat / 180) * H, s) for lon, lat, s in pts]

    def views():
        for k, rgb, m, people, cells in marked_views:
            v = heat(Image.fromarray(rgb), m)
            own = cells((VIEW // 8, VIEW // 8), 8)
            shade = Image.fromarray(np.where(own, 0, 110).astype(np.uint8)).resize(v.size, Image.NEAREST)
            v.paste(Image.new("RGB", v.size, (0, 0, 0)), (0, 0), shade)
            yield k, dots(v, people)

    return pts, eq_pts, lambda: dots(heat(img, uniform_filter(splat, 3)), eq_pts), views


def main() -> None:
    ap = argparse.ArgumentParser()
    for k in ("--out", "--detector", "--detector-revision", "--density", "--density-revision", "--density-file"):
        ap.add_argument(k, required=True)
    ap.add_argument("--nadir-deg", type=float, default=35)
    ap.add_argument("--zenith-deg", type=float, default=30)
    ap.add_argument("--marked", action="store_true")
    ap.add_argument("--photo", action=Frame)
    ap.add_argument("--equirect", action=Frame)
    ap.set_defaults(frames=[])
    a = ap.parse_args()
    if not a.frames:
        sys.exit("count.py: no frames")
    gaps, live = geometry_gaps(a.nadir_deg, a.zenith_deg)
    if gaps:
        sys.exit(f"count.py: {gaps} directions between the cuts lie outside the view that owns them")
    t0 = time.time()
    device = pick()
    det = Detector(a.detector, a.detector_revision, device)
    dens = Density(a.density, a.density_revision, a.density_file, device)
    os.makedirs(os.path.join(a.out, "points"), exist_ok=True)
    rows = []
    for kind, path in a.frames:
        stem = os.path.splitext(os.path.basename(path))[0]
        img = ImageOps.exif_transpose(Image.open(path)).convert("RGB")
        if kind == "equirect":
            pts, _, marked, views = equirect(img, det, dens, a.nadir_deg, a.zenith_deg, live)
            cols = "lon\tlat\tfrom"
        else:
            pts, _, marked, views = photo(img, det, dens)
            cols = "x\ty\tfrom"
        nd = sum(1 for p in pts if p[2] == "detector")
        rows.append(f"{stem}\t{nd}\t{len(pts) - nd}\t{len(pts)}")
        with open(os.path.join(a.out, "points", stem + ".tsv"), "w") as f:
            f.write(cols + "\n" + "".join(f"{x:.2f}\t{y:.2f}\t{s}\n" for x, y, s in pts))
        if a.marked:
            os.makedirs(os.path.join(a.out, "marked"), exist_ok=True)
            marked().save(os.path.join(a.out, "marked", stem + ".jpg"), quality=88)
            if views:
                vd = os.path.join(a.out, "marked", stem)
                os.makedirs(vd, exist_ok=True)
                for k, v in views():
                    v.save(os.path.join(vd, f"view-{k:02d}.jpg"), quality=88)
        print(f"count: {stem} {kind} {len(pts)} ({nd} detected)", file=sys.stderr)
    # counts.tsv last: its presence is the run's success
    tmp = os.path.join(a.out, ".counts.tsv.part")
    with open(tmp, "w") as f:
        f.write("frame\tdetected\tdensity\testimate\n" + "".join(r + "\n" for r in rows))
    os.replace(tmp, os.path.join(a.out, "counts.tsv"))
    print(f"count: {len(rows)} frames on {device}, {time.time() - t0:.1f}s", file=sys.stderr)
    sys.stdout.flush()
    sys.stderr.flush()
    os._exit(0)


if __name__ == "__main__":
    main()
