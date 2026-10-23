// plate's clock: time in this document moves only when plate calls
// __plate.seek(ms). Installed before any page script runs.
(() => {
  if (window.__plate) return;
  let now = 0;
  let seeks = 0;
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

  // Web Animations: each is held paused at its own time on the clock.
  const proto = Animation.prototype;
  const nativePause = proto.pause;
  const nativePlay = proto.play;
  const started = new WeakMap(); // animation -> clock ms its time 0 sits at
  const pagePaused = new WeakSet();
  proto.pause = function () {
    pagePaused.add(this);
    return nativePause.call(this);
  };
  proto.play = function () {
    if (pagePaused.delete(this) && started.has(this)) {
      started.set(this, now - (this.currentTime || 0) / (this.playbackRate || 1));
    }
    return nativePlay.call(this);
  };
  const holdAnimations = async (first) => {
    const held = [];
    const fresh = [];
    for (const a of document.getAnimations()) {
      if (!started.has(a)) {
        started.set(a, first ? 0 : now);
        if (!pagePaused.has(a)) fresh.push(a);
      }
      if (pagePaused.has(a)) continue;
      nativePause.call(a);
      held.push(a);
    }
    const at = (a) => (now - started.get(a)) * a.playbackRate;
    // Chrome's compositor keeps a newly paused animation where it was
    // running when the first time set is 0; a painted other time first
    // makes it take the real one.
    if (fresh.length) {
      for (const a of fresh) a.currentTime = at(a) + 1;
      await painted();
    }
    for (const a of held) a.currentTime = at(a);
  };

  // Media: a video is never playing in real time; its media time is kept
  // here and applied at each seek.
  const media = HTMLMediaElement.prototype;
  const desc = (name) => Object.getOwnPropertyDescriptor(media, name);
  const nativeTime = desc("currentTime");
  const nativeRate = desc("playbackRate");
  const nativePaused = desc("paused");
  const nativeMediaPause = media.pause;
  const states = new WeakMap();
  const state = (el) => {
    let s = states.get(el);
    if (!s) {
      s = {
        playing: false,
        anchorClock: now,
        anchorMedia: nativeTime.get.call(el),
        rate: nativeRate.get.call(el),
        autoplayed: false,
        ended: false,
      };
      states.set(el, s);
    }
    return s;
  };
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
  Object.defineProperty(media, "currentTime", {
    configurable: true,
    enumerable: true,
    get() {
      return mediaTime(this);
    },
    set(v) {
      const s = state(this);
      s.anchorMedia = Number(v);
      s.anchorClock = now;
      s.ended = false;
    },
  });
  Object.defineProperty(media, "playbackRate", {
    configurable: true,
    enumerable: true,
    get() {
      return state(this).rate;
    },
    set(v) {
      anchor(this);
      state(this).rate = Number(v);
    },
  });
  Object.defineProperty(media, "paused", {
    configurable: true,
    enumerable: true,
    get() {
      return !state(this).playing;
    },
  });

  // Half a frame at the fastest capture rate, 120 fps.
  const halfFrame = 1 / 240;
  const syncVideo = (el) => {
    const s = state(el);
    if (el.error) return null;
    if (el.autoplay && !s.autoplayed && el.readyState >= 2) {
      s.autoplayed = true;
      if (!s.playing) {
        s.anchorMedia = 0;
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

  const srcOf = (el) =>
    el.getAttribute("src") || el.querySelector("source")?.getAttribute("src") || el.currentSrc;

  const rootSVGs = () => [...document.querySelectorAll("svg")].filter((s) => !s.ownerSVGElement);

  let fontsReady = false;
  document.fonts.ready.then(() => {
    fontsReady = true;
  });

  window.__plate = {
    // seek moves the clock to t ms and resolves once every video shows its
    // frame, with the media errors the page's videos report.
    async seek(t) {
      now = t;
      const first = seeks++ === 0;
      fireTimers();
      const due = frames;
      frames = new Map();
      for (const cb of due.values()) {
        try {
          cb(now);
        } catch (e) {
          report(e);
        }
      }
      await holdAnimations(first);
      for (const s of rootSVGs()) {
        s.pauseAnimations();
        s.setCurrentTime(now / 1000);
      }
      const videos = [...document.querySelectorAll("video")];
      const seeking = videos.map(syncVideo).filter(Boolean);
      if (seeking.length) {
        // seeked fires before the frame reaches the compositor.
        await Promise.all(seeking);
        await painted();
      }
      return videos.filter((v) => v.error).map((v) => ({ src: srcOf(v), code: v.error.code }));
    },

    // settle resolves with what is still loading: nothing once the page has
    // loaded, or what is pending after budget ms.
    settle(budget) {
      const pending = () => {
        const p = [];
        if (document.readyState !== "complete") p.push("load event");
        if (!fontsReady) p.push("fonts");
        for (const img of document.images) if (!img.complete) p.push("img " + (img.currentSrc || img.src));
        for (const v of document.querySelectorAll("video")) {
          if (!v.error && v.readyState < 2) p.push("video " + srcOf(v));
        }
        return p;
      };
      const end = RealDate.now() + budget;
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
