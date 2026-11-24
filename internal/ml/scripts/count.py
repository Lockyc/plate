# /// script
# requires-python = ">=3.12"
# dependencies = ["torch", "transformers", "onnxruntime", "onnx", "onnx2torch", "huggingface_hub", "pillow", "numpy", "scipy"]
# [tool.uv]
# exclude-newer = "2026-11-15T00:00:00Z"
# ///
"""count.py -- count the people in photos and 360 equirect frames.

    count.py --out DIR --detector REPO --detector-revision SHA
             --density REPO --density-revision SHA --density-file FILE
             [--nadir-deg 35] [--zenith-deg 30] [--marked]
             (--photo PATH | --equirect PATH)...
    count.py --out DIR --detector ... --density ... --track
             --reid REPO --reid-revision SHA --reid-file FILE
             [--min-seconds 3] --pano PANO_TSV

A person is counted one of two ways, never both:
  - a detector person box scoring CONF or better;
  - a density-map peak (a local maximum over PEAK_WIN cells of at least
    PEAK) outside every counted box with at least CROWD other peaks within
    CROWD_R cells: someone too small or packed to box. An isolated peak is
    a sign or a patterned shirt, not a crowd.

An equirect frame is read as perspective views, HFOV wide and VIEW px
square, centred on RINGS. Each direction on the sphere is owned by the
nearest view centre, and a box counts in the view that owns its head (the
top tenth of the box), so a person two views see is counted once; two boxes
from different views whose heads are within DUP box heights of each other,
of heights within DUP_SIZE of each other, are one person on the line between
two owned regions, and the larger box is kept.
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

--track follows the boxed people through the equirect frames a `plate pano
--every` pano.tsv lists (--pano), in the order of its t column (seconds). Each frame's people are matched to the tracks alive in
it by where their heads are on the sphere: one assignment minimising, per
pair, the arc to the track's predicted place over the gate (GATE_MOVE body
heights a second since the track was last seen, at least GATE_MIN degrees;
a pair past the gate is never matched) plus APPEAR times how unlike their
re-ID appearance is (a pair less alike than SIM_MIN is never matched). A person unmatched starts a track; a track unseen for
LOST seconds ends. The density map is read every DENSITY_EVERY seconds and
its part carried to the frames between. Writes DIR/track.tsv (t, on_screen:
the estimate) and DIR/distinct.tsv:
  - high: tracks lasting --min-seconds or more;
  - low: those after joining any whose mean appearance is REID_SIM alike or
    more across a gap of up to MERGE_GAP seconds, most alike first, never
    joining tracks that overlap in time (one person is in one place);
  - tracks: every track;
  - untracked_share: the density part's share of the estimate over the
    frames it was read in -- people counted but never tracked, in neither
    low nor high.
Appearance features stay in memory and are never written.
"""

import argparse
import math
import os
import sys
import time

import numpy as np
import onnx
import onnxruntime as ort
import torch
import torch.nn.functional as F
from huggingface_hub import hf_hub_download
from onnx2torch import convert
from PIL import Image, ImageDraw, ImageOps
from plate_device import pick
from scipy.ndimage import maximum_filter, uniform_filter
from scipy.optimize import linear_sum_assignment
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
DENS_BATCH = 9  # density windows a run: a view's 3 x 3
DET_BATCH = 8  # views a detector run
# Fitted on plate's five quality crowd assets (the bake-off): the detector's
# confidence, the photo tile, and the density peak, its window, and the crowd
# a peak needs around it.
CONF, TILE = 0.5, 1280
PEAK, PEAK_WIN, CROWD, CROWD_R = 0.1, 5, 2, 16
DUP, DUP_SIZE = 0.15, 1.25
MEAN = np.array([0.485, 0.456, 0.406], np.float32)
STD = np.array([0.229, 0.224, 0.225], np.float32)
DET_DOT, DENS_DOT = (40, 220, 90), (235, 60, 220)
# Tracking (see the docstring). Where each value comes from:
#   LOST: the count-track-distinct scene's turning-rate walkers split a crossing
#     walker hidden 2.5 s at 2 s and not at 4; its straight-line walkers come
#     out exact (low 10, high 12) for LOST 2 to 6, GATE_MOVE 1.5 to 3 and
#     REID_SIM 0.6 to 0.8, so none of those three is decided by the case.
#   GATE_MOVE, LOST, REID_SIM on 12 minutes of real festival footage (about 65
#     boxed people a frame, no truth): every looser gate or longer LOST lowers
#     high, from 528 (LOST 2) to 92 (LOST 6, GATE_MOVE 3), as tracks start to
#     carry more than one person; 1.5 and 4 s sit mid-way, at 108 to 172.
#   SIM_MIN, REID_SIM: OSNet's cosine for one person frame to frame is 0.9 or
#     more and between two people 0.3 to 0.6 in that scene.
#   GATE_MIN, APPEAR, MERGE_GAP, DENSITY_EVERY: set, not fitted; MERGE_GAP is
#     the minute a returner is looked for, DENSITY_EVERY the speed budget.
GATE_MOVE, GATE_MIN, APPEAR, SIM_MIN, LOST = 1.5, 1.0, 1.0, 0.5, 4.0
REID_SIM, MERGE_GAP, DENSITY_EVERY = 0.7, 60.0, 2.0
REID_H, REID_W = 256, 128


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


def pixel_dir(yaw, pitch, x, y):
    """The unit direction of the view pixel holding (x, y): view_dirs at one pixel."""
    t = math.tan(math.radians(HFOV) / 2)
    xi, yi = min(max(int(x), 0), VIEW - 1), min(max(int(y), 0), VIEW - 1)
    d = np.array([((xi + 0.5) / VIEW * 2 - 1) * t, -((yi + 0.5) / VIEW * 2 - 1) * t, 1.0])
    return (d / np.linalg.norm(d)) @ rot(yaw, pitch).T


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


class Views:
    """The live views of an equirect, built once a run: each view's sampling grid, and the view
    pixels and density cells it owns."""

    def __init__(self, live, nadir, zenith, red, device):
        self.live, self.device = live, device
        grids, self.own, self.cells = [], [], []
        c = np.minimum((np.arange(math.ceil(VIEW / red)) + 0.5) * red, VIEW - 1).astype(int)
        for k in live:
            d = view_dirs(*RINGS[k])
            lon, lat = lonlat(d)
            grids.append(torch.from_numpy(np.stack([lon / 180.0, -lat / 90.0], -1)).float())
            o = owner(d, nadir, zenith) == k
            self.own.append(o)
            self.cells.append(o[c][:, c])
        self.grid = torch.stack(grids).to(device)

    def read(self, img):
        """Every live view of img, as uint8 (views, 3, VIEW, VIEW) on the device."""
        eq = torch.from_numpy(np.array(img, dtype=np.uint8)).to(self.device).permute(2, 0, 1)[None].float()
        out = [F.grid_sample(eq, self.grid[i : i + 1], mode="bilinear", padding_mode="border", align_corners=False) for i in range(len(self.live))]
        return torch.cat(out).clamp(0, 255).byte()

    def owns(self, i, x, y):
        return bool(self.own[i][min(max(int(y), 0), VIEW - 1), min(max(int(x), 0), VIEW - 1)])

    def lonlat(self, i, x, y):
        lon, lat = lonlat(pixel_dir(*RINGS[self.live[i]], x, y))
        return float(lon), float(lat)


class Detector:
    def __init__(self, repo, rev, device):
        self.proc = AutoImageProcessor.from_pretrained(repo, revision=rev)
        self.model = AutoModelForObjectDetection.from_pretrained(repo, revision=rev).to(device).eval()
        self.device = device
        self.person = next(int(k) for k, v in self.model.config.id2label.items() if v.lower() == "person")

    def _boxes(self, out, sizes):
        rs = self.proc.post_process_object_detection(out, threshold=CONF, target_sizes=sizes)
        return [[b for s, l, b in zip(r["scores"].tolist(), r["labels"].tolist(), r["boxes"].tolist()) if l == self.person] for r in rs]

    def __call__(self, rgb):
        h, w, _ = rgb.shape
        inp = self.proc(images=Image.fromarray(rgb), return_tensors="pt").to(self.device)
        with torch.no_grad():
            out = self.model(**inp)
        return self._boxes(out, [(h, w)])[0]

    def views(self, v):
        """The person boxes in each of a batch of uint8 views on the device, resized and scaled as
        the processor does."""
        size = (self.proc.size["height"], self.proc.size["width"])
        boxes = []
        for i in range(0, len(v), DET_BATCH):
            x = F.interpolate(v[i : i + DET_BATCH].float(), size=size, mode="bilinear", antialias=True, align_corners=False)
            x = x * self.proc.rescale_factor
            if self.proc.do_normalize:
                x = (x - torch.tensor(self.proc.image_mean, device=x.device).view(1, 3, 1, 1)) / torch.tensor(self.proc.image_std, device=x.device).view(1, 3, 1, 1)
            with torch.no_grad():
                out = self.model(pixel_values=x)
            boxes += self._boxes(out, [v.shape[-2:]] * len(x))
        return boxes


class Density:
    """DM-Count: a person-density map at 1/8 scale whose sum is the count. The pinned graph reads
    one window and ends in a reshape to that one window's map; widening the reshape and the input
    to DENS_BATCH windows reads them in one run, each window's arithmetic unchanged."""

    def __init__(self, repo, rev, file, device):
        prov = {"mps": ["CoreMLExecutionProvider"], "cuda": ["CUDAExecutionProvider"]}.get(device, []) + ["CPUExecutionProvider"]
        prov = [p for p in prov if p in ort.get_available_providers()]
        m = onnx.load(hf_hub_download(repo, file, revision=rev))
        last = m.graph.node[-1]
        shape = next(i for i in m.graph.initializer if i.name == last.input[1]) if last.op_type == "Reshape" else None
        if shape is None or onnx.numpy_helper.to_array(shape)[0] != 1:
            sys.exit(f"count.py: {repo}'s graph does not end in a reshape to one window")
        shape.CopyFrom(onnx.numpy_helper.from_array(np.array([DENS_BATCH, *onnx.numpy_helper.to_array(shape)[1:]], np.int64), shape.name))
        for t in (m.graph.input[0], m.graph.output[0]):
            t.type.tensor_type.shape.dim[0].dim_value = DENS_BATCH
        self.s = ort.InferenceSession(m.SerializeToString(), providers=prov)
        self.name = self.s.get_inputs()[0].name
        self.red = 8

    def map(self, x):
        """The density map of x, a normalised (3, h, w) float32 array."""
        _, h, w = x.shape
        ph, pw = max(WIN, h), max(WIN, w)
        x = np.pad(x, ((0, 0), (0, ph - h), (0, pw - w)))

        def starts(n):
            return sorted({min(i * STRIDE, n - WIN) for i in range(math.ceil((n - WIN) / STRIDE) + 1)})

        wins = [(y0, x0) for y0 in starts(ph) for x0 in starts(pw)]
        r, acc = self.red, np.zeros((ph // self.red, pw // self.red), np.float32)
        cnt = np.zeros_like(acc)
        for i in range(0, len(wins), DENS_BATCH):
            part = wins[i : i + DENS_BATCH]
            b = np.zeros((DENS_BATCH, 3, WIN, WIN), np.float32)
            for j, (y0, x0) in enumerate(part):
                b[j] = x[:, y0 : y0 + WIN, x0 : x0 + WIN]
            out = self.s.run(None, {self.name: b})[0]
            for j, (y0, x0) in enumerate(part):
                o = out[j, 0]
                acc[y0 // r : y0 // r + o.shape[0], x0 // r : x0 // r + o.shape[1]] += o
                cnt[y0 // r : y0 // r + o.shape[0], x0 // r : x0 // r + o.shape[1]] += 1
        return (acc / np.maximum(cnt, 1))[: math.ceil(h / r), : math.ceil(w / r)]

    def __call__(self, rgb):
        return self.map(((rgb.astype(np.float32) / 255 - MEAN) / STD).transpose(2, 0, 1))

    def views(self, v):
        """The density map of each of a batch of uint8 views on the device."""
        mean = torch.from_numpy(MEAN).to(v.device).view(3, 1, 1)
        std = torch.from_numpy(STD).to(v.device).view(3, 1, 1)
        return [self.map(((v[i].float() / 255 - mean) / std).cpu().numpy()) for i in range(len(v))]


class ReID:
    """OSNet appearance: one unit vector per person crop, alike for the same person."""

    def __init__(self, repo, rev, file, device):
        self.model = convert(onnx.load(hf_hub_download(repo, file, revision=rev))).eval().to(device)
        self.mean = torch.from_numpy(MEAN).to(device).view(1, 3, 1, 1)
        self.std = torch.from_numpy(STD).to(device).view(1, 3, 1, 1)

    def __call__(self, crops):
        """crops: uint8 (3, h, w) tensors on the device."""
        if not crops:
            return np.zeros((0, 512), np.float32)
        x = torch.cat([F.interpolate(c[None].float(), size=(REID_H, REID_W), mode="bilinear", align_corners=False) for c in crops])
        with torch.no_grad():
            f = self.model((x / 255 - self.mean) / self.std).cpu().numpy()
        return f / np.maximum(np.linalg.norm(f, axis=1, keepdims=True), 1e-6)


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


def read_equirect(img, views, det, dens, density=True):
    """An equirect's counted people. Returns the views (uint8 on the device); per view, its counted
    people in view pixels (x, y, source), its counted boxes and, when density is read, its owned
    density map; and every counted person as (lon, lat, source)."""
    v = views.read(img)
    boxes = det.views(v)
    maps = dens.views(v) if density else [None] * len(v)
    # a person on the line between two views' owned regions can have their head on either side
    # of it in each view's box: keep the larger box of two from different views whose heads are
    # within DUP of the box height apart and whose heights differ by under DUP_SIZE
    found = sorted(
        ((size_deg(views, i, b), i, b, views.lonlat(i, *head(b)[:2])) for i in range(len(v)) for b in boxes[i] if views.owns(i, *head(b)[:2])),
        key=lambda f: -f[0],
    )
    kept = []
    for s, i, b, (lon, lat) in found:
        if not any(j != i and arc(lon, lat, lo, la) < DUP * s2 and s2 / s < DUP_SIZE for s2, j, _, (lo, la) in kept):
            kept.append((s, i, b, (lon, lat)))
    per_view, pts = [], []
    for i in range(len(v)):
        counted = [b for _, j, b, _ in kept if j == i]
        people = [head(b) for b in counted]
        if density:
            people += crowd(maps[i], views.cells[i], boxes[i], dens.red)
            maps[i] = np.where(views.cells[i], maps[i], 0)
        per_view.append((people, counted, maps[i]))
        pts += [(*views.lonlat(i, x, y), s) for x, y, s in people]
    return v, per_view, pts


def equirect(img, views, det, dens):
    W, H = img.size
    v, per_view, pts = read_equirect(img, views, det, dens)
    eq_pts = [((lon / 360 + 0.5) * W, (0.5 - lat / 180) * H, s) for lon, lat, s in pts]

    def marked():
        # the owned density, splatted back onto the equirect at 1/8 scale
        r = dens.red
        splat = np.zeros((math.ceil(H / r), math.ceil(W / r)), np.float32)
        for i, (_, _, m) in enumerate(per_view):
            ys, xs = np.nonzero(m)
            if len(ys):
                d = view_dirs(*RINGS[views.live[i]])
                lon, lat = lonlat(d[np.minimum((ys + 0.5) * r, VIEW - 1).astype(int), np.minimum((xs + 0.5) * r, VIEW - 1).astype(int)])
                ex = np.clip(((lon / 360 + 0.5) * W / r).astype(int), 0, splat.shape[1] - 1)
                ey = np.clip(((0.5 - lat / 180) * H / r).astype(int), 0, splat.shape[0] - 1)
                np.add.at(splat, (ey, ex), m[ys, xs])
        return dots(heat(img, uniform_filter(splat, 3)), eq_pts)

    def views_marked():
        for i, (people, _, m) in enumerate(per_view):
            out = heat(Image.fromarray(v[i].permute(1, 2, 0).cpu().numpy()), m)
            shade = Image.fromarray(np.where(views.own[i], 0, 110).astype(np.uint8))
            out.paste(Image.new("RGB", out.size, (0, 0, 0)), (0, 0), shade)
            yield views.live[i], dots(out, people)

    return pts, eq_pts, marked, views_marked


def wrap(d):
    return (d + 180.0) % 360.0 - 180.0


def arc(lon1, lat1, lon2, lat2):
    """Degrees of arc between directions (broadcasting)."""
    r = math.pi / 180
    c = np.sin(lat1 * r) * np.sin(lat2 * r) + np.cos(lat1 * r) * np.cos(lat2 * r) * np.cos((lon1 - lon2) * r)
    return np.degrees(np.arccos(np.clip(c, -1, 1)))


class Track:
    def __init__(self, t, lon, lat, size, feat):
        self.first = self.last = t
        self.lon, self.lat, self.size = lon, lat, size
        self.vlon = self.vlat = 0.0
        self.feat = feat.copy()

    def predict(self, t):
        dt = t - self.last
        return self.lon + self.vlon * dt, self.lat + self.vlat * dt

    def appearance(self):
        return self.feat / max(np.linalg.norm(self.feat), 1e-6)

    def update(self, t, lon, lat, size, feat):
        dt = t - self.last
        self.vlon = 0.5 * self.vlon + 0.5 * wrap(lon - self.lon) / dt
        self.vlat = 0.5 * self.vlat + 0.5 * (lat - self.lat) / dt
        self.lon, self.lat, self.size, self.last = lon, lat, size, t
        self.feat += feat


class Tracker:
    def __init__(self):
        self.alive, self.done = [], []

    def step(self, t, people):
        """people: (lon, lat, size in degrees, unit appearance) of each boxed person at time t."""
        self.done += [tr for tr in self.alive if t - tr.last > LOST]
        self.alive = [tr for tr in self.alive if t - tr.last <= LOST]
        matched = set()
        if self.alive and people:
            pred = np.array([tr.predict(t) for tr in self.alive])
            lon = np.array([p[0] for p in people])
            lat = np.array([p[1] for p in people])
            d = arc(pred[:, 0:1], pred[:, 1:2], lon[None], lat[None])
            size = np.maximum(np.array([tr.size for tr in self.alive])[:, None], np.array([p[2] for p in people])[None])
            gate = np.maximum(GATE_MIN, GATE_MOVE * size * np.array([t - tr.last for tr in self.alive])[:, None])
            sim = np.stack([tr.appearance() for tr in self.alive]) @ np.stack([p[3] for p in people]).T
            cost = np.where((d <= gate) & (sim >= SIM_MIN), d / gate + APPEAR * (1 - sim), 1e6)
            for a, b in zip(*linear_sum_assignment(cost)):
                if cost[a, b] < 1e6:
                    self.alive[a].update(t, *people[b])
                    matched.add(b)
        self.alive += [Track(t, *p) for i, p in enumerate(people) if i not in matched]

    def finish(self):
        return self.done + self.alive


def distinct(tracks, min_seconds):
    """low, high and every track (see the docstring)."""
    long = [tr for tr in tracks if tr.last - tr.first >= min_seconds - 1e-6]
    parent = list(range(len(long)))
    spans = [[(tr.first, tr.last)] for tr in long]

    def find(i):
        while parent[i] != i:
            parent[i] = parent[parent[i]]
            i = parent[i]
        return i

    feats = np.stack([tr.appearance() for tr in long]) if long else np.zeros((0, 512))
    sim = feats @ feats.T
    pairs = sorted(
        ((sim[a, b], a, b) for a in range(len(long)) for b in range(len(long)) if 0 < long[b].first - long[a].last <= MERGE_GAP and sim[a, b] >= REID_SIM),
        reverse=True,
    )
    for _, a, b in pairs:
        ga, gb = find(a), find(b)
        if ga == gb or any(f1 <= l2 and f2 <= l1 for f1, l1 in spans[ga] for f2, l2 in spans[gb]):
            continue
        parent[gb] = ga
        spans[ga] += spans[gb]
    return len({find(i) for i in range(len(long))}), len(long), len(tracks)


def size_deg(views, i, box):
    """A box's height in degrees of arc."""
    x0, y0, x1, y1 = box
    yaw, pitch = RINGS[views.live[i]]
    top, bottom = pixel_dir(yaw, pitch, (x0 + x1) / 2, y0), pixel_dir(yaw, pitch, (x0 + x1) / 2, y1 - 1)
    return float(np.degrees(np.arccos(np.clip(top @ bottom, -1, 1))))


def pano_frames(tsv):
    """The frames a `plate pano --every` pano.tsv lists: (path, t as written, t), in time order."""
    with open(tsv) as f:
        rows = [line.rstrip("\n").split("\t") for line in f if line.strip()]
    fc, tc = rows[0].index("file"), rows[0].index("t")
    frames = [(os.path.join(os.path.dirname(tsv), r[fc]), r[tc], float(r[tc])) for r in rows[1:]]
    return sorted(frames, key=lambda f: f[2])


def track(a, views, det, dens, device):
    """--track (the module docstring): writes track.tsv a row a frame and distinct.tsv last. Its
    constants, and what each was fitted on, are beside GATE_MOVE."""
    frames = pano_frames(a.pano)
    reid = ReID(a.reid, a.reid_revision, a.reid_file, device)
    tracker = Tracker()
    dens_part, dens_at, shown, untracked = 0, None, 0, 0
    t0 = time.time()
    # track.tsv is written a row a frame, so a run cut short keeps what it read
    out = open(os.path.join(a.out, "track.tsv"), "w")
    out.write("t\ton_screen\n")
    for n, (path, raw, t) in enumerate(frames):
        img = ImageOps.exif_transpose(Image.open(path)).convert("RGB")
        read_dens = dens_at is None or t - dens_at >= DENSITY_EVERY - 1e-6
        v, per_view, pts = read_equirect(img, views, det, dens, density=read_dens)
        boxed = [(i, b) for i, (_, counted, _) in enumerate(per_view) for b in counted]
        crops = [v[i, :, max(0, int(b[1])) : max(int(b[1]) + 1, int(b[3])), max(0, int(b[0])) : max(int(b[0]) + 1, int(b[2]))] for i, b in boxed]
        feats = reid(crops)
        people = []
        for (i, b), f in zip(boxed, feats):
            lon, lat = views.lonlat(i, *head(b)[:2])
            people.append((lon, lat, size_deg(views, i, b), f))
        tracker.step(t, people)
        if read_dens:
            dens_at, dens_part = t, sum(1 for p in pts if p[2] == "density")
            shown, untracked = shown + len(pts), untracked + dens_part
        out.write(f"{raw}\t{len(boxed) + dens_part}\n")
        out.flush()
        if (n + 1) % 50 == 0:
            print(f"count: {n + 1}/{len(frames)} frames, {(time.time() - t0) / (n + 1):.2f}s a frame", file=sys.stderr)
    out.close()
    low, high, tracks = distinct(tracker.finish(), a.min_seconds)
    share = untracked / shown if shown else 0.0
    # distinct.tsv last: its presence is the run's success
    tmp = os.path.join(a.out, ".distinct.tsv.part")
    with open(tmp, "w") as f:
        f.write(f"low\thigh\ttracks\tmin_seconds\tuntracked_share\n{low}\t{high}\t{tracks}\t{a.min_seconds:g}\t{share:.3f}\n")
    os.replace(tmp, os.path.join(a.out, "distinct.tsv"))
    print(f"count: {len(frames)} frames on {device}, {(time.time() - t0) / len(frames):.2f}s a frame; distinct {low} to {high} ({tracks} tracks)", file=sys.stderr)


def main() -> None:
    ap = argparse.ArgumentParser()
    for k in ("--out", "--detector", "--detector-revision", "--density", "--density-revision", "--density-file"):
        ap.add_argument(k, required=True)
    ap.add_argument("--nadir-deg", type=float, default=35)
    ap.add_argument("--zenith-deg", type=float, default=30)
    ap.add_argument("--marked", action="store_true")
    ap.add_argument("--photo", action=Frame)
    ap.add_argument("--equirect", action=Frame)
    ap.add_argument("--track", action="store_true")
    for k in ("--reid", "--reid-revision", "--reid-file", "--pano"):
        ap.add_argument(k)
    ap.add_argument("--min-seconds", type=float, default=3)
    ap.set_defaults(frames=[])
    a = ap.parse_args()
    if a.track != bool(a.pano) or (a.track and a.frames):
        sys.exit("count.py: --track takes its frames from --pano, and only --track takes --pano")
    if not a.frames and not a.track:
        sys.exit("count.py: no frames")
    gaps, live = geometry_gaps(a.nadir_deg, a.zenith_deg)
    if gaps:
        sys.exit(f"count.py: {gaps} directions between the cuts lie outside the view that owns them")
    t0 = time.time()
    device = pick()
    det = Detector(a.detector, a.detector_revision, device)
    dens = Density(a.density, a.density_revision, a.density_file, device)
    views = Views(live, a.nadir_deg, a.zenith_deg, dens.red, device) if a.track or any(k == "equirect" for k, _ in a.frames) else None
    if a.track:
        track(a, views, det, dens, device)
        sys.stdout.flush()
        sys.stderr.flush()
        os._exit(0)
    os.makedirs(os.path.join(a.out, "points"), exist_ok=True)
    rows = []
    for kind, path in a.frames:
        stem = os.path.splitext(os.path.basename(path))[0]
        img = ImageOps.exif_transpose(Image.open(path)).convert("RGB")
        if kind == "equirect":
            pts, _, marked, views_marked = equirect(img, views, det, dens)
            cols = "lon\tlat\tfrom"
        else:
            pts, _, marked, views_marked = photo(img, det, dens)
            cols = "x\ty\tfrom"
        nd = sum(1 for p in pts if p[2] == "detector")
        rows.append(f"{stem}\t{nd}\t{len(pts) - nd}\t{len(pts)}")
        with open(os.path.join(a.out, "points", stem + ".tsv"), "w") as f:
            f.write(cols + "\n" + "".join(f"{x:.2f}\t{y:.2f}\t{s}\n" for x, y, s in pts))
        if a.marked:
            os.makedirs(os.path.join(a.out, "marked"), exist_ok=True)
            marked().save(os.path.join(a.out, "marked", stem + ".jpg"), quality=88)
            if views_marked:
                vd = os.path.join(a.out, "marked", stem)
                os.makedirs(vd, exist_ok=True)
                for k, v in views_marked():
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
