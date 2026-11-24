// What a page is still loading, for plate to wait on before any capture.
// Installed before any page script runs, in every frame, so it holds the
// browser's own requestAnimationFrame whatever the page or plate's clock
// later puts in its place.
(() => {
  if (window.__plateSettle) return;
  const realRAF = window.requestAnimationFrame.bind(window);
  const srcOf = (el) =>
    el.getAttribute("src") || el.querySelector("source")?.getAttribute("src") || el.currentSrc;

  // deferred reports whether a lazy image is held back until it nears the
  // viewport, which nothing in a capture scrolls it to: it has no box, or
  // its box lies wholly outside the viewport.
  const deferred = (img) => {
    if (img.loading !== "lazy") return false;
    if (!img.getClientRects().length) return true;
    const r = img.getBoundingClientRect();
    return r.bottom <= 0 || r.right <= 0 || r.top >= innerHeight || r.left >= innerWidth;
  };

  window.__plateSettle = {
    srcOf,

    // unready names a video without a frame, marking preload="none": such a
    // video never loads unless played.
    unready: (el) => srcOf(el) + (el.preload === "none" ? ' (preload="none")' : ""),

    // pending is everything this frame and its same-origin frames have
    // started loading and not finished: the load event, a font, an image,
    // a video still fetching its first frame. A load the page defers by
    // design (a lazy image out of view, a video the browser is not
    // fetching, as with preload="none") is not pending: nothing in a
    // capture starts it.
    pending() {
      const p = [];
      if (document.readyState !== "complete") p.push("load event");
      if (document.fonts.status !== "loaded") p.push("fonts");
      for (const img of document.images) {
        if (!img.complete && !deferred(img)) p.push("img " + (img.currentSrc || img.src));
      }
      for (const v of document.querySelectorAll("video")) {
        if (!v.error && v.readyState < 2 && v.networkState === HTMLMediaElement.NETWORK_LOADING) {
          p.push("video " + this.unready(v));
        }
      }
      for (const f of document.querySelectorAll("iframe")) {
        try {
          const child = f.contentWindow?.__plateSettle;
          if (child) p.push(...child.pending().map((s) => `${s} in iframe ${f.src}`));
        } catch {
          // Cross-origin: nothing in it can be waited on.
        }
      }
      return p;
    },

    // painted resolves once every loaded image is decoded and two frames
    // have been drawn since, so the capture shows the layout the loaded
    // fonts and images give. An image not loaded is left alone: decoding
    // a deferred one would wait for a load that never starts.
    painted() {
      const decoded = [...document.images]
        .filter((img) => img.complete)
        .map((img) => img.decode().catch(() => {}));
      return Promise.all(decoded).then(() => new Promise((r) => realRAF(() => realRAF(() => r(true)))));
    },
  };
})();
