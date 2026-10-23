// plate's clock: time in this document moves only when plate calls
// __plate.seek(ms). Installed before any page script runs, in every frame;
// the top frame's seek drives its same-origin child frames.
(() => {
  if (window.__plate) return;
  let now = 0;
  try {
    if (window !== window.parent && window.parent.__plate) now = window.parent.__plate.now();
  } catch {
    // A cross-origin parent: the top frame refuses this frame at its seek.
  }
  let seeks = 0;
  let budget = 5000; // ms a video may take to show a frame; settle sets it
  const realSetTimeout = window.setTimeout.bind(window);
  const realRAF = window.requestAnimationFrame.bind(window);
  const painted = () => new Promise((r) => realRAF(() => realRAF(r)));
  const report = (e) => window.reportError(e);

  // Time.
  const RealDate = Date;
  const epoch = RealDate.UTC(2026, 0, 1);
  function PlateDate(...args) {
    if (!new.target) return new RealDate(epoch + now).toString();
    return Reflect.construct(RealDate, args.length ? args : [epoch + now], new.target);
  }
  PlateDate.prototype = RealDate.prototype;
  PlateDate.now = () => epoch + now;
  PlateDate.parse = RealDate.parse;
  PlateDate.UTC = RealDate.UTC;
  window.Date = PlateDate;
  performance.now = () => now;

  // Math.random: mulberry32, seed 1.
  let seed = 1;
  Math.random = () => {
    seed = (seed + 0x6d2b79f5) | 0;
    let t = seed;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };

  // Animation frames.
  let frames = new Map();
  let frameID = 0;
  window.requestAnimationFrame = (cb) => {
    frames.set(++frameID, cb);
    return frameID;
  };
  window.cancelAnimationFrame = (id) => frames.delete(id);
  const runFrames = () => {
    const due = frames;
    frames = new Map();
    for (const cb of due.values()) {
      try {
        cb(now);
      } catch (e) {
        report(e);
      }
    }
  };

  // Timers, fired in due order (ties in creation order).
  const timers = new Map();
  let timerID = 0;
  const addTimer = (cb, ms, args, repeat) => {
    const id = ++timerID;
    const delay = Math.max(0, Number(ms) || 0);
    timers.set(id, { id, at: now + delay, cb, args, every: repeat ? Math.max(1, delay) : 0 });
    return id;
  };
  window.setTimeout = (cb, ms, ...args) => addTimer(cb, ms, args, false);
  window.setInterval = (cb, ms, ...args) => addTimer(cb, ms, args, true);
  window.clearTimeout = window.clearInterval = (id) => timers.delete(id);
  const maxTimerRuns = 100000;
  const fireTimers = () => {
    for (let runs = 0; ; runs++) {
      let next;
      for (const t of timers.values()) {
        if (t.at <= now && (!next || t.at < next.at || (t.at === next.at && t.id < next.id))) next = t;
      }
      if (!next) return;
      if (runs === maxTimerRuns) throw new Error(`timers still due after ${maxTimerRuns} runs at ${now} ms`);
      if (next.every) next.at += next.every;
      else timers.delete(next.id);
      try {
        if (typeof next.cb === "function") next.cb(...next.args);
      } catch (e) {
        report(e);
      }
    }
  };

  // Web Animations: each runs on the clock, held natively paused at its own
  // time. One the page pauses (pause(), or a CSS animation-play-state of
  // paused) keeps its time; one that reaches its end finishes natively, so
  // its finished promise and finish event come in that seek.
  const proto = Animation.prototype;
  const nativePause = proto.pause;
  const nativePlay = proto.play;
  const nativeFinish = proto.finish;
  // animation -> {start: clock ms its time 0 sits at, paused, heldAt, done}
  const recs = new WeakMap();
  const pagePaused = new WeakSet();
  const timeOf = (a, r) => (now - r.start) * a.playbackRate;
  proto.pause = function () {
    const r = recs.get(this);
    if (r && !r.paused) {
      r.heldAt = r.done ? this.currentTime : timeOf(this, r);
      r.paused = true;
      r.done = false;
    }
    pagePaused.add(this);
    return nativePause.call(this);
  };
  proto.play = function () {
    pagePaused.delete(this);
    // play() on a finished animation restarts it, as a new one.
    if (recs.get(this)?.done) recs.delete(this);
    return nativePlay.call(this);
  };
  proto.finish = function () {
    const r = recs.get(this);
    if (r) r.done = true;
    return nativeFinish.call(this);
  };
  const cssPaused = (a) => {
    if (!(a instanceof CSSAnimation) || !a.effect?.target) return false;
    const cs = getComputedStyle(a.effect.target, a.effect.pseudoElement);
    const i = cs.animationName.split(",").map((s) => s.trim()).indexOf(a.animationName);
    const states = cs.animationPlayState.split(",").map((s) => s.trim());
    return i >= 0 && states[i % states.length] === "paused";
  };
  // holdAnimations holds every animation at the clock's time and reports
  // whether any began or finished, which can run page code.
  const holdAnimations = async (first) => {
    const held = [];
    const entering = [];
    for (const a of document.getAnimations()) {
      let r = recs.get(a);
      const fresh = !r;
      if (fresh) {
        r = { start: first ? 0 : now, paused: false, heldAt: 0, done: false };
        recs.set(a, r);
      }
      if (r.done) continue;
      if (pagePaused.has(a) || cssPaused(a)) {
        if (!r.paused) {
          r.heldAt = fresh ? a.currentTime || 0 : timeOf(a, r);
          r.paused = true;
        }
        a.currentTime = r.heldAt;
        continue;
      }
      if (r.paused) {
        r.start = now - r.heldAt / (a.playbackRate || 1);
        r.paused = false;
        entering.push(a);
      } else if (fresh) {
        entering.push(a);
      }
      nativePause.call(a);
      held.push(a);
    }
    // Chrome's compositor keeps a newly paused animation where it was
    // running when the first time set is 0; a painted other time first
    // makes it take the real one.
    if (entering.length) {
      for (const a of entering) a.currentTime = timeOf(a, recs.get(a)) + 1;
      await painted();
    }
    const finishing = [];
    for (const a of held) {
      const r = recs.get(a);
      const t = timeOf(a, r);
      const end = a.effect ? a.effect.getComputedTiming().endTime : Infinity;
      if ((a.playbackRate > 0 && t >= end) || (a.playbackRate < 0 && t <= 0)) {
        r.done = true;
        finishing.push(a);
      } else {
        a.currentTime = t;
      }
    }
    for (const a of finishing) nativeFinish.call(a);
    // Finish events and finished promises are delivered at the next frame.
    if (finishing.length) await painted();
    return entering.length > 0 || finishing.length > 0;
  };

  // Media: a video is never playing in real time; its media time is kept
  // here and applied at each seek.
  const media = HTMLMediaElement.prototype;
  const desc = (name) => Object.getOwnPropertyDescriptor(media, name);
  const nativeTime = desc("currentTime");
  const nativeRate = desc("playbackRate");
  const nativeSrc = desc("src");
  const nativeSrcObject = desc("srcObject");
  const nativeMediaPause = media.pause;
  const nativeLoad = media.load;
  const states = new WeakMap();
  const newState = (el) => ({
    playing: false,
    anchorClock: now,
    anchorMedia: 0,
    rate: el.defaultPlaybackRate,
    autoplayed: false,
    ended: false,
  });
  const state = (el) => {
    let s = states.get(el);
    if (!s) {
      s = newState(el);
      states.set(el, s);
    }
    return s;
  };
  // The media load algorithm: a new source starts paused at 0.
  const reload = (el) => states.set(el, newState(el));
  const mediaTime = (el) => {
    const s = state(el);
    let m = s.anchorMedia;
    if (s.playing) m += ((now - s.anchorClock) * s.rate) / 1000;
    const d = el.duration;
    if (Number.isFinite(d) && d > 0) m = el.loop ? ((m % d) + d) % d : Math.min(Math.max(m, 0), d);
    return m;
  };
  const anchor = (el) => {
    const s = state(el);
    s.anchorMedia = mediaTime(el);
    s.anchorClock = now;
  };
  media.play = function () {
    const s = state(this);
    if (!s.playing) {
      anchor(this);
      const d = this.duration;
      if (!this.loop && Number.isFinite(d) && s.anchorMedia >= d) s.anchorMedia = 0;
      s.playing = true;
      s.ended = false;
      this.dispatchEvent(new Event("play"));
    }
    return Promise.resolve();
  };
  media.pause = function () {
    const s = state(this);
    if (s.playing) {
      anchor(this);
      s.playing = false;
      this.dispatchEvent(new Event("pause"));
    }
    nativeMediaPause.call(this);
  };
  media.load = function () {
    reload(this);
    return nativeLoad.call(this);
  };
  const define = (name, get, set) => Object.defineProperty(media, name, { configurable: true, enumerable: true, get, set });
  define(
    "currentTime",
    function () {
      return mediaTime(this);
    },
    function (v) {
      const s = state(this);
      s.anchorMedia = Number(v);
      s.anchorClock = now;
      s.ended = false;
    },
  );
  define(
    "playbackRate",
    function () {
      return state(this).rate;
    },
    function (v) {
      anchor(this);
      state(this).rate = Number(v);
    },
  );
  define("paused", function () {
    return !state(this).playing;
  });
  define("src", nativeSrc.get, function (v) {
    reload(this);
    nativeSrc.set.call(this, v);
  });
  define("srcObject", nativeSrcObject.get, function (v) {
    reload(this);
    nativeSrcObject.set.call(this, v);
  });
  const nativeSetAttribute = Element.prototype.setAttribute;
  const nativeRemoveAttribute = Element.prototype.removeAttribute;
  Element.prototype.setAttribute = function (name, value) {
    if (this instanceof HTMLMediaElement && String(name).toLowerCase() === "src") reload(this);
    return nativeSetAttribute.call(this, name, value);
  };
  Element.prototype.removeAttribute = function (name) {
    if (this instanceof HTMLMediaElement && String(name).toLowerCase() === "src") reload(this);
    return nativeRemoveAttribute.call(this, name);
  };

  const srcOf = (el) =>
    el.getAttribute("src") || el.querySelector("source")?.getAttribute("src") || el.currentSrc;

  // ready resolves true once el has a frame to show (or an error to
  // report), false after budget ms.
  const ready = (el) =>
    new Promise((resolve) => {
      const done = (ok) => {
        el.removeEventListener("loadeddata", yes);
        el.removeEventListener("error", yes);
        resolve(ok);
      };
      const yes = () => done(true);
      el.addEventListener("loadeddata", yes);
      el.addEventListener("error", yes);
      if (el.readyState >= 2 || el.error) return done(true);
      realSetTimeout(() => done(el.readyState >= 2 || !!el.error), budget);
    });

  // step advances el's media state to the clock: autoplay, then ended.
  const step = (el) => {
    const s = state(el);
    if (el.autoplay && !s.autoplayed) {
      s.autoplayed = true;
      if (!s.playing) {
        s.anchorClock = now;
        s.playing = true;
      }
    }
    const d = el.duration;
    if (s.playing && !el.loop && Number.isFinite(d) && mediaTime(el) >= d && !s.ended) {
      s.ended = true;
      s.anchorMedia = d;
      s.anchorClock = now;
      s.playing = false;
      el.dispatchEvent(new Event("ended"));
    }
  };

  // Half a frame at the fastest capture rate, 120 fps.
  const halfFrame = 1 / 240;
  const seekVideo = (el) => {
    nativeMediaPause.call(el);
    // Chrome truncates a seek to whole microseconds, which would put a time
    // a rounding error short of a frame boundary on the frame before.
    const want = Math.round(mediaTime(el) * 1e6) / 1e6;
    if (Math.abs(nativeTime.get.call(el) - want) <= halfFrame) return null;
    return new Promise((resolve) => {
      const done = () => {
        el.removeEventListener("seeked", done);
        el.removeEventListener("error", done);
        resolve();
      };
      el.addEventListener("seeked", done);
      el.addEventListener("error", done);
      nativeTime.set.call(el, want);
    });
  };

  // syncMedia puts every video at its media time, showing that frame, and
  // reports whether anything changed. A video that stays without a frame
  // past budget joins stalled.
  const syncMedia = async (stalled) => {
    const videos = [...document.querySelectorAll("video")].filter((v) => !stalled.has(v));
    const waiting = videos.filter((v) => v.readyState < 2 && !v.error);
    const ok = await Promise.all(waiting.map(ready));
    waiting.forEach((v, i) => ok[i] || stalled.add(v));
    const live = videos.filter((v) => !stalled.has(v) && !v.error);
    for (const v of live) step(v);
    const seeking = live.map(seekVideo).filter(Boolean);
    await Promise.all(seeking);
    const changed = waiting.length > 0 || seeking.length > 0;
    // A loaded or seeked frame reaches the compositor after its event.
    if (changed) await painted();
    return changed;
  };

  // SVG documents run on the clock from when each is first seen.
  const svgStart = new WeakMap();
  const syncSVGs = (first) => {
    for (const s of document.querySelectorAll("svg")) {
      if (s.ownerSVGElement) continue;
      if (!svgStart.has(s)) svgStart.set(s, first ? 0 : now);
      s.pauseAnimations();
      s.setCurrentTime((now - svgStart.get(s)) / 1000);
    }
  };

  // Page code a seek runs (timers, events, finished promises) can start
  // more media or animations, so the steps repeat until nothing changes.
  const maxPasses = 20;
  const seekFrame = async (t, first) => {
    now = t;
    fireTimers();
    runFrames();
    const stalled = new Set();
    for (let pass = 0; pass < maxPasses; pass++) {
      const moved = await syncMedia(stalled);
      syncSVGs(first);
      const began = await holdAnimations(first);
      if (!moved && !began) break;
    }
    const out = {
      errors: [...document.querySelectorAll("video")]
        .filter((v) => v.error)
        .map((v) => ({ src: srcOf(v), code: v.error.code })),
      stalled: [...stalled].map(srcOf),
      crossOrigin: [],
    };
    for (const f of document.querySelectorAll("iframe")) {
      let child;
      try {
        child = f.contentWindow?.__plate;
      } catch {
        out.crossOrigin.push(f.src);
        continue;
      }
      if (!child) continue;
      const r = await child.seekFrame(t, first);
      out.errors.push(...r.errors);
      out.stalled.push(...r.stalled);
      out.crossOrigin.push(...r.crossOrigin);
    }
    return out;
  };

  const pending = () => {
    const p = [];
    if (document.readyState !== "complete") p.push("load event");
    if (document.fonts.status !== "loaded") p.push("fonts");
    for (const img of document.images) if (!img.complete) p.push("img " + (img.currentSrc || img.src));
    for (const v of document.querySelectorAll("video")) {
      if (!v.error && v.readyState < 2) p.push("video " + srcOf(v));
    }
    for (const f of document.querySelectorAll("iframe")) {
      try {
        const child = f.contentWindow?.__plate;
        if (child) p.push(...child.pending().map((s) => `${s} in iframe ${f.src}`));
      } catch {
        // Cross-origin: seek refuses it.
      }
    }
    return p;
  };

  window.__plate = {
    now: () => now,
    seekFrame,
    pending,

    // seek moves the clock to t ms and resolves once every frame shows it:
    // media errors, videos without a frame, and cross-origin iframes.
    seek(t) {
      return seekFrame(t, seeks++ === 0);
    },

    // settle resolves with what is still loading: nothing once the page has
    // loaded, or what is pending after ms; ms is also how long a seek waits
    // for a video's frame.
    settle(ms) {
      budget = ms;
      const end = RealDate.now() + ms;
      return new Promise((resolve) => {
        const poll = () => {
          const p = pending();
          if (!p.length || RealDate.now() >= end) resolve(p);
          else realSetTimeout(poll, 10);
        };
        poll();
      });
    },
  };
})();
