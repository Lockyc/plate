// What a page is still loading, for plate to wait on before any capture.
// Installed before any page script runs, in every frame, so it holds the
// browser's own requestAnimationFrame whatever the page or plate's clock
// later puts in its place.
(() => {
  if (window.__plateSettle) return;
  const realRAF = window.requestAnimationFrame.bind(window);
  const srcOf = (el) =>
    el.getAttribute("src") || el.querySelector("source")?.getAttribute("src") || el.currentSrc;

  window.__plateSettle = {
    srcOf,

    // unready names a video without a frame, marking preload="none": such a
    // video never loads unless played.
    unready: (el) => srcOf(el) + (el.preload === "none" ? ' (preload="none")' : ""),

    // pending is everything this frame and its same-origin frames are still
    // loading: the load event, a font, an image not yet decoded, a video
    // without a frame.
    pending() {
      const p = [];
      if (document.readyState !== "complete") p.push("load event");
      if (document.fonts.status !== "loaded") p.push("fonts");
      for (const img of document.images) {
        if (!img.complete) p.push("img " + (img.currentSrc || img.src));
      }
      for (const v of document.querySelectorAll("video")) {
        if (!v.error && v.readyState < 2) p.push("video " + this.unready(v));
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

    // painted resolves once every image is decoded and two frames have
    // been drawn since, so the capture shows the layout the loaded fonts
    // and images give.
    painted() {
      const decoded = [...document.images].map((img) => img.decode().catch(() => {}));
      return Promise.all(decoded).then(() => new Promise((r) => realRAF(() => realRAF(() => r(true)))));
    },
  };
})();
