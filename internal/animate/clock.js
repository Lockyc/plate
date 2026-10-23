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
  const frames = new Map();
  let frameID = 0;
  window.requestAnimationFrame = (cb) => {
    frames.set(++frameID, cb);
    return frameID;
  };
  window.cancelAnimationFrame = (id) => frames.delete(id);
  // The frames queued when the batch starts run, less any cancelled on the
  // way; one queued during the batch waits for the next.
  const runFrames = () => {
    for (const id of [...frames.keys()]) {
      const cb = frames.get(id);
      if (!cb) continue;
      frames.delete(id);
      try {
        cb(now);
      } catch (e) {
        report(e);
      }
    }
  };

  // Timers, fired in due order (ties in creation order). As in a browser, a
  // timer set from a timer nested 4 deep waits at least 4 ms.
  const timers = new Map();
  let timerID = 0;
  let nesting = 0; // the running timer's nesting level, 0 outside one
  const addTimer = (name, cb, ms, args, repeat) => {
    if (typeof cb === "string") {
      report(new Error(`${name} with a code string: pass a function`));
      return 0;
    }
    const id = ++timerID;
    const level = nesting + 1;
    let delay = Math.max(0, Number(ms) || 0);
    if (level >= 5) delay = Math.max(4, delay);
    timers.set(id, { id, at: now + delay, cb, args, level, every: repeat ? Math.max(1, delay) : 0 });
    return id;
  };
  window.setTimeout = (cb, ms, ...args) => addTimer("setTimeout", cb, ms, args, false);
  window.setInterval = (cb, ms, ...args) => addTimer("setInterval", cb, ms, args, true);
  window.clearTimeout = window.clearInterval = (id) => timers.delete(id);
  const maxTimerRuns = 100000;
  // fireTimers runs every due timer and reports whether any ran, or
  // "overrun" when timers are still due after maxTimerRuns.
  // Each callback runs with the clock at its own due time, so a timer it
  // sets is due from then, not from the seek; an interval's every run
  // counts toward its nesting level.
  const fireTimers = () => {
    const seekAt = now;
    for (let runs = 0; ; runs++) {
      let next;
      for (const t of timers.values()) {
        if (t.at <= seekAt && (!next || t.at < next.at || (t.at === next.at && t.id < next.id))) next = t;
      }
      if (!next) return runs > 0;
      if (runs === maxTimerRuns) return "overrun";
      const due = next.at;
      if (next.every) {
        next.level++;
        if (next.level >= 5) next.every = Math.max(4, next.every);
        next.at += next.every;
      } else {
        timers.delete(next.id);
      }
      nesting = next.level;
      now = due;
      try {
        if (typeof next.cb === "function") next.cb(...next.args);
      } catch (e) {
        report(e);
      } finally {
        nesting = 0;
        now = seekAt;
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
  const nativeReverse = proto.reverse;
  const nativeUpdateRate = proto.updatePlaybackRate;
  const nativeCurrent = Object.getOwnPropertyDescriptor(proto, "currentTime");
  const nativeAnimRate = Object.getOwnPropertyDescriptor(proto, "playbackRate");
  const current = (a) => nativeCurrent.get.call(a) || 0;
  const setCurrent = (a, v) => nativeCurrent.set.call(a, v);
  // animation -> {anchorClock, anchorTime: its time at anchorClock, rate,
  // paused, heldAt: a paused one's time, done: finished natively, seen: held
  // before}. rate is kept here because Chrome defers a rate change on a
  // pending animation, reading the old one until it is ready.
  const recs = new WeakMap();
  // Every animation recorded, since getAnimations() leaves out one sitting
  // on its edge, such as a reversed one at its end, or one of no duration,
  // finished as it is made.
  const known = new Set();
  const pagePaused = new WeakSet();
  const record = (a, clock, time, rate = nativeAnimRate.get.call(a)) => {
    const r = { anchorClock: clock, anchorTime: time, rate, paused: false, heldAt: 0, done: false, seen: false };
    recs.set(a, r);
    known.add(a);
    return r;
  };
  const nativeAnimate = Element.prototype.animate;
  Element.prototype.animate = function (...args) {
    const a = nativeAnimate.apply(this, args);
    record(a, now, 0);
    return a;
  };
  const animations = () => {
    const all = new Set(document.getAnimations());
    for (const a of known) {
      // A finished one comes back through play() or a currentTime set.
      if (a.playState === "idle" || recs.get(a)?.done) known.delete(a);
      else all.add(a);
    }
    return all;
  };
  const timeOf = (r) => r.anchorTime + (now - r.anchorClock) * r.rate;
  // timeNow is a's time on the clock right now.
  const timeNow = (a, r) => (r.paused ? r.heldAt : r.done ? current(a) : timeOf(r));
  const reanchor = (a, r, time) => {
    r.anchorTime = time;
    r.anchorClock = now;
    if (r.paused) r.heldAt = time;
    if (r.done) {
      r.done = false;
      r.seen = false;
      known.add(a);
    }
  };
  const endOf = (a) => (a.effect ? a.effect.getComputedTiming().endTime : Infinity);
  proto.pause = function () {
    const r = recs.get(this) || record(this, now, current(this));
    if (!r.paused) {
      r.heldAt = timeNow(this, r);
      r.paused = true;
      r.done = false;
      known.add(this);
    }
    pagePaused.add(this);
    return nativePause.call(this);
  };
  proto.play = function () {
    pagePaused.delete(this);
    const r = recs.get(this);
    const out = nativePlay.call(this);
    // play() on a finished animation restarts it from its start (or its
    // end, reversed), which native play has just set.
    if (r?.done) record(this, now, current(this), r.rate);
    return out;
  };
  proto.finish = function () {
    const out = nativeFinish.call(this);
    const r = recs.get(this);
    if (r) r.done = true;
    return out;
  };
  // A cancelled animation is idle: a later play() starts it afresh.
  const nativeCancel = proto.cancel;
  proto.cancel = function () {
    const out = nativeCancel.call(this);
    recs.delete(this);
    known.delete(this);
    pagePaused.delete(this);
    return out;
  };
  proto.reverse = function () {
    const r = recs.get(this);
    const time = r ? timeNow(this, r) : current(this);
    const rate = -(r ? r.rate : nativeAnimRate.get.call(this));
    pagePaused.delete(this);
    const out = nativeReverse.call(this);
    // reverse() at an edge rewinds to the other one.
    const end = endOf(this);
    const from = rate < 0 && time <= 0 ? end : rate > 0 && time >= end ? 0 : time;
    if (r) {
      r.paused = false;
      reanchor(this, r, from);
      r.rate = rate;
    } else {
      record(this, now, from, rate);
    }
    return out;
  };
  const rateChange = (a, rate) => {
    const r = recs.get(a);
    if (!r) return;
    if (!r.paused && !r.done) reanchor(a, r, timeOf(r));
    r.rate = rate;
  };
  proto.updatePlaybackRate = function (rate) {
    rateChange(this, Number(rate));
    return nativeUpdateRate.call(this, rate);
  };
  Object.defineProperty(proto, "currentTime", {
    configurable: true,
    enumerable: true,
    get: nativeCurrent.get,
    set(v) {
      setCurrent(this, v); // first: a write Chrome refuses throws here
      if (v === null) return;
      const r = recs.get(this);
      if (r) reanchor(this, r, Number(v));
      else record(this, now, Number(v));
    },
  });
  Object.defineProperty(proto, "playbackRate", {
    configurable: true,
    enumerable: true,
    get: nativeAnimRate.get,
    set(v) {
      rateChange(this, Number(v));
      nativeAnimRate.set.call(this, v);
    },
  });
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
    for (const a of animations()) {
      const r = recs.get(a) || record(a, first ? 0 : now, 0);
      const fresh = !r.seen;
      r.seen = true;
      if (r.done) continue;
      if (pagePaused.has(a) || cssPaused(a)) {
        if (!r.paused) {
          r.heldAt = fresh ? current(a) : timeOf(r);
          r.paused = true;
        }
        setCurrent(a, r.heldAt);
        continue;
      }
      if (r.paused) {
        r.anchorTime = r.heldAt;
        r.anchorClock = now;
        r.paused = false;
        entering.push(a);
      } else if (fresh) {
        entering.push(a);
      }
      nativePause.call(a);
      if (nativeAnimRate.get.call(a) !== r.rate) nativeAnimRate.set.call(a, r.rate);
      held.push(a);
    }
    // Chrome's compositor keeps a newly paused animation where it was
    // running when the first time set is 0; a painted other time first
    // makes it take the real one.
    if (entering.length) {
      for (const a of entering) setCurrent(a, timeOf(recs.get(a)) + 1);
      await painted();
    }
    const finishing = [];
    for (const a of held) {
      const r = recs.get(a);
      const t = timeOf(r);
      const end = endOf(a);
      if ((r.rate > 0 && t >= end) || (r.rate < 0 && t <= 0)) {
        r.done = true;
        finishing.push(a);
      } else {
        setCurrent(a, t);
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
  const started = (el) => {
    el.dispatchEvent(new Event("play"));
    el.dispatchEvent(new Event("playing"));
  };
  media.play = function () {
    const s = state(this);
    if (!s.playing) {
      anchor(this);
      const d = this.duration;
      if (!this.loop && Number.isFinite(d) && s.anchorMedia >= d) s.anchorMedia = 0;
      s.playing = true;
      s.ended = false;
      started(this);
    }
    return Promise.resolve();
  };
  // pause() also cancels autoplay, as in a browser.
  media.pause = function () {
    const s = state(this);
    s.autoplayed = true;
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
  // unready names a video without a frame, marking preload="none": plate
  // never plays a video natively, so such a video never loads.
  const unready = (el) => srcOf(el) + (el.preload === "none" ? ' (preload="none")' : "");

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
        started(el);
      }
    }
    const d = el.duration;
    if (s.playing && !el.loop && Number.isFinite(d) && mediaTime(el) >= d && !s.ended) {
      s.ended = true;
      s.anchorMedia = d;
      s.anchorClock = now;
      s.playing = false;
      el.dispatchEvent(new Event("pause"));
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
  // more timers, media or animations, so the steps repeat until nothing
  // changes; a page still changing after maxPasses is restless.
  const maxPasses = 20;
  const seekFrame = async (t, first) => {
    now = t;
    let overrun = fireTimers() === "overrun";
    runFrames();
    const stalled = new Set();
    let restless = true;
    for (let pass = 0; pass < maxPasses && !overrun; pass++) {
      const moved = await syncMedia(stalled);
      syncSVGs(first);
      const began = await holdAnimations(first);
      const fired = fireTimers();
      overrun = fired === "overrun";
      if (!moved && !began && !fired) {
        restless = false;
        break;
      }
    }
    const out = {
      restless: restless && !overrun ? maxPasses : 0, // the passes run, when restless
      overrun: overrun ? maxTimerRuns : 0, // the timer runs, when timers are still due
      errors: [...document.querySelectorAll("video")]
        .filter((v) => v.error)
        .map((v) => ({ src: srcOf(v), code: v.error.code })),
      stalled: [...stalled].map(unready),
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
      out.restless = Math.max(out.restless, r.restless);
      out.overrun = Math.max(out.overrun, r.overrun);
    }
    return out;
  };

  const pending = () => {
    const p = [];
    if (document.readyState !== "complete") p.push("load event");
    if (document.fonts.status !== "loaded") p.push("fonts");
    for (const img of document.images) if (!img.complete) p.push("img " + (img.currentSrc || img.src));
    for (const v of document.querySelectorAll("video")) {
      if (!v.error && v.readyState < 2) p.push("video " + unready(v));
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
