// The walkthrough's drawn layers: a cursor and a caption bar, over every page
// the recording visits. plate installs this before any page script runs and
// sets window.__plateState ({caption, x, y}) the same way, so a new document
// starts with the caption and cursor the last one had.
(() => {
  if (window.__plate) return;

  const top = 2147483647;
  const arrow =
    '<svg xmlns="http://www.w3.org/2000/svg" width="24" height="30" viewBox="0 0 24 30">' +
    '<path d="M2 2 L2 24 L8 18.5 L12 28 L16 26.3 L12 17 L20 17 Z" fill="#111" stroke="#fff" stroke-width="2" stroke-linejoin="round"/>' +
    "</svg>";
  let cursor = null;
  let bar = null;

  const state = () => window.__plateState || { caption: "", x: null, y: null };

  function place(x, y) {
    cursor.style.transform = `translate(${x}px, ${y}px)`;
    cursor.style.display = "block";
  }

  function show(text) {
    bar.textContent = text;
    bar.style.display = text ? "flex" : "none";
  }

  function mount() {
    if (cursor && cursor.isConnected) return;
    cursor = document.createElement("div");
    cursor.setAttribute("data-plate-cursor", "");
    cursor.style.cssText =
      "all:initial;position:fixed;left:-2px;top:-2px;width:24px;height:30px;display:none;" +
      "pointer-events:none;will-change:transform;z-index:" + top;
    cursor.innerHTML = arrow;
    bar = document.createElement("div");
    bar.setAttribute("data-plate-caption", "");
    bar.style.cssText =
      "all:initial;position:fixed;left:0;right:0;bottom:0;height:12vh;box-sizing:border-box;" +
      "display:none;align-items:center;justify-content:center;padding:0 4vw;text-align:center;" +
      "background:rgba(0,0,0,0.75);color:#fff;font:600 3vh/1.3 system-ui,-apple-system,sans-serif;" +
      "pointer-events:none;z-index:" + (top - 1);
    document.documentElement.append(bar, cursor);
    const s = state();
    show(s.caption || "");
    if (s.x !== null && s.y !== null) place(s.x, s.y);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", mount);
  } else {
    mount();
  }

  window.__plate = {
    caption(text) {
      mount();
      show(text);
    },

    // glide moves the cursor to (x, y) over ms, from where it stands or,
    // the first time, from the viewport's centre.
    glide(x, y, ms) {
      mount();
      const s = state();
      const from = s.x !== null && s.y !== null ? [s.x, s.y] : [innerWidth / 2, innerHeight / 2];
      cursor.style.transition = "none";
      place(from[0], from[1]);
      cursor.getBoundingClientRect();
      cursor.style.transition = `transform ${ms}ms ease-in-out`;
      place(x, y);
      return new Promise((done) => setTimeout(done, ms));
    },

    // find reports how many of the page's elements match sel (the layers'
    // own never count) and, for exactly one that is visible, scrolls it to
    // the viewport's centre and returns its centre.
    find(sel) {
      const all = [...document.querySelectorAll(sel)].filter(
        (el) => !el.closest("[data-plate-cursor], [data-plate-caption]"),
      );
      if (all.length !== 1) return { count: all.length, visible: false };
      const el = all[0];
      const r0 = el.getBoundingClientRect();
      const visible =
        r0.width > 0 && r0.height > 0 &&
        el.checkVisibility({ opacityProperty: true, visibilityProperty: true });
      if (!visible) return { count: 1, visible: false };
      el.scrollIntoView({ block: "center", inline: "center", behavior: "instant" });
      const r = el.getBoundingClientRect();
      return { count: 1, visible: true, x: r.left + r.width / 2, y: r.top + r.height / 2 };
    },
  };
})();
