// The public landing page. No frameworks and no inline code (CSP). Every scene
// loops only while on screen, and shows its final state under reduced motion.
'use strict';
document.documentElement.classList.add('js');

document.addEventListener('DOMContentLoaded', () => {
  const $ = id => document.getElementById(id);
  const still = matchMedia('(prefers-reduced-motion: reduce)').matches;
  const sleep = ms => new Promise(r => setTimeout(r, ms));
  const hex = bytes => Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('');
  const subtle = window.crypto && crypto.subtle;
  const utf8 = s => new TextEncoder().encode(s);

  // ---- helpers ----
  function watch(el, onChange) {
    new IntersectionObserver(es => onChange(es[es.length - 1].isIntersecting), { threshold: 0.15 }).observe(el);
  }
  // loop(el, fn) keeps calling fn while el is visible.
  function loop(el, fn) {
    let visible = false, wake = null;
    watch(el, v => { visible = v; if (v && wake) { wake(); wake = null; } });
    (async () => {
      for (;;) {
        if (!visible) await new Promise(r => { wake = r; });
        await fn();
      }
    })();
  }
  const GLYPHS = '0123456789abcdef';
  // scramble morphs el's text into target, left to right.
  async function scramble(el, target, ms) {
    const steps = Math.max(1, Math.round(ms / 40));
    for (let i = 1; i <= steps; i++) {
      const done = Math.floor(target.length * i / steps);
      let s = target.slice(0, done);
      for (let j = done; j < target.length; j++) s += GLYPHS[Math.floor(Math.random() * 16)];
      el.textContent = s;
      await sleep(40);
    }
    el.textContent = target;
  }

  // ---- nav, reveals, counters ----
  const nav = document.querySelector('.nav');
  const onScroll = () => nav.classList.toggle('stuck', scrollY > 8);
  addEventListener('scroll', onScroll, { passive: true }); onScroll();

  const once = new IntersectionObserver(es => {
    for (const e of es) if (e.isIntersecting) { e.target.classList.add('in'); once.unobserve(e.target); }
  }, { threshold: 0.08 });
  document.querySelectorAll('.reveal').forEach(el => once.observe(el));

  if (!still) document.querySelectorAll('[data-count]').forEach(el => {
    const end = Number(el.dataset.count), suffix = el.dataset.suffix || '';
    const digits = el.dataset.count.includes('.') ? 2 : 0;
    const show = v => { el.textContent = v.toLocaleString('en-US', { minimumFractionDigits: digits, maximumFractionDigits: digits }) + suffix; };
    const ob = new IntersectionObserver(es => {
      if (!es[0].isIntersecting) return;
      ob.disconnect();
      const t0 = performance.now();
      const tick = t => {
        const k = Math.min(1, (t - t0) / 1100);
        show(end * (1 - Math.pow(1 - k, 3)));
        if (k < 1) requestAnimationFrame(tick); else show(end);
      };
      requestAnimationFrame(tick);
    });
    ob.observe(el);
  });

  // ---- hero: the mesh, with one encrypted route through a relay ----
  (function mesh() {
    const canvas = $('mesh'), ctx = canvas.getContext('2d');
    const LIME = '198,242,78', AMBER = '255,182,72';
    let W = 0, H = 0, nodes = [], route = [], running = false;
    const LABELS = ['买家', '中继', '卖家'];

    function layout() {
      const dpr = Math.min(devicePixelRatio || 1, 2);
      W = canvas.clientWidth; H = canvas.clientHeight;
      canvas.width = W * dpr; canvas.height = H * dpr;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      const narrow = W < 900;
      const count = narrow ? 26 : 62;
      nodes = Array.from({ length: count }, () => ({
        x: Math.random() * W, y: Math.random() * H,
        vx: (Math.random() - .5) * .16, vy: (Math.random() - .5) * .16, r: 1 + Math.random() * 1.3,
      }));
      route = narrow
        ? [[W * .14, H - 100], [W * .5, H - 62], [W * .86, H - 104]]
        : [[W * .6, H * .7], [W * .74, H * .27], [W * .91, H * .62]];
    }

    const LINK = 130;
    function drawMesh() {
      for (let i = 0; i < nodes.length; i++) {
        const a = nodes[i];
        for (let j = i + 1; j < nodes.length; j++) {
          const b = nodes[j], d = Math.hypot(a.x - b.x, a.y - b.y);
          if (d < LINK) {
            ctx.strokeStyle = `rgba(255,255,255,${.075 * (1 - d / LINK)})`;
            ctx.beginPath(); ctx.moveTo(a.x, a.y); ctx.lineTo(b.x, b.y); ctx.stroke();
          }
        }
      }
      ctx.fillStyle = 'rgba(255,255,255,.34)';
      for (const n of nodes) { ctx.beginPath(); ctx.arc(n.x, n.y, n.r, 0, 7); ctx.fill(); }
    }

    function pointAt(p) {
      const seg = Math.min(1, Math.floor(p * 2)), k = p * 2 - seg;
      const [a, b] = [route[seg], route[seg + 1]];
      return [a[0] + (b[0] - a[0]) * k, a[1] + (b[1] - a[1]) * k];
    }

    function drawRoute(p, trail) {
      ctx.strokeStyle = `rgba(${LIME},.32)`; ctx.setLineDash([4, 6]);
      ctx.beginPath(); ctx.moveTo(...route[0]); ctx.lineTo(...route[1]); ctx.lineTo(...route[2]); ctx.stroke();
      ctx.setLineDash([]);
      ctx.font = '11px ui-monospace, Menlo, monospace'; ctx.textAlign = 'center';
      for (const t of trail) {
        ctx.fillStyle = `rgba(${LIME},${t.a})`;
        ctx.fillText(t.c, t.x, t.y);
      }
      route.forEach((pt, i) => {
        const near = Math.abs(p - i / 2) < .05;
        const rgb = i === 1 ? LIME : AMBER;
        ctx.fillStyle = `rgba(${rgb},${near ? .28 : .1})`;
        ctx.beginPath(); ctx.arc(pt[0], pt[1], near ? 20 : 14, 0, 7); ctx.fill();
        ctx.fillStyle = `rgb(${rgb})`;
        ctx.beginPath(); ctx.arc(pt[0], pt[1], 5, 0, 7); ctx.fill();
        ctx.fillStyle = 'rgba(238,240,244,.82)';
        ctx.font = '13px -apple-system, "PingFang SC", "Microsoft YaHei", sans-serif';
        ctx.fillText(LABELS[i], pt[0], pt[1] + 34);
        ctx.font = '11px ui-monospace, Menlo, monospace';
      });
      if (p >= 0 && p <= 1) {
        const [x, y] = pointAt(p);
        ctx.shadowColor = `rgb(${LIME})`; ctx.shadowBlur = 16;
        ctx.fillStyle = `rgb(${LIME})`;
        ctx.beginPath(); ctx.arc(x, y, 4, 0, 7); ctx.fill();
        ctx.shadowBlur = 0;
      }
    }

    const PERIOD = 4200, trail = [];
    let t0 = 0, last = 0, lastGlyph = 0;
    function frame(t) {
      if (!running) return;
      const dt = Math.min(50, t - last); last = t;
      ctx.clearRect(0, 0, W, H);
      for (const n of nodes) {
        n.x += n.vx * dt / 16; n.y += n.vy * dt / 16;
        if (n.x < 0 || n.x > W) n.vx *= -1;
        if (n.y < 0 || n.y > H) n.vy *= -1;
      }
      drawMesh();
      const phase = ((t - t0) % PERIOD) / PERIOD, p = phase / .72;  // the rest of the period is a pause
      if (p <= 1 && t - lastGlyph > 70) {
        lastGlyph = t;
        const [x, y] = pointAt(p);
        trail.push({ x: x + (Math.random() - .5) * 16, y: y + (Math.random() - .5) * 16 - 8, c: GLYPHS[Math.floor(Math.random() * 16)] + GLYPHS[Math.floor(Math.random() * 16)], a: .9 });
      }
      for (const g of trail) g.a -= dt / 900;
      while (trail.length && trail[0].a <= 0) trail.shift();
      drawRoute(p, trail);
      requestAnimationFrame(frame);
    }
    const start = () => { if (!running) { running = true; last = t0 = performance.now(); requestAnimationFrame(frame); } };
    layout();
    let resizing;
    addEventListener('resize', () => {
      clearTimeout(resizing);
      resizing = setTimeout(() => { layout(); if (still) { ctx.clearRect(0, 0, W, H); drawMesh(); drawRoute(.5, []); } }, 150);
    });
    if (still) { drawMesh(); drawRoute(.5, []); return; }
    watch(canvas, v => { if (v) start(); else running = false; });
  })();

  // ---- 01 sealed box: a real AES-256-GCM ciphertext of the prompt ----
  (function seal() {
    const PROMPT = '帮我审查这份合同里的保密条款';
    const el = $('seal'), input = $('seal-in'), relay = $('seal-relay'), out = $('seal-out');
    const chips = Array.from($('seal-chips').children), packet = el.querySelector('.packet');
    async function ciphertext() {
      if (!subtle) return hex(crypto.getRandomValues(new Uint8Array(60)));
      const key = await subtle.generateKey({ name: 'AES-GCM', length: 256 }, false, ['encrypt']);
      const nonce = crypto.getRandomValues(new Uint8Array(12));
      const ct = await subtle.encrypt({ name: 'AES-GCM', iv: nonce }, key, utf8(PROMPT));
      return hex(nonce) + hex(new Uint8Array(ct));
    }
    if (still) {
      input.textContent = out.textContent = PROMPT;
      chips.forEach(c => c.classList.add('on'));
      ciphertext().then(c => { relay.textContent = c; });
      return;
    }
    loop(el, async () => {
      input.textContent = ''; out.textContent = ' '; relay.textContent = ' ';
      chips.forEach(c => c.classList.remove('on'));
      packet.classList.remove('go');
      await sleep(300);
      for (let i = 1; i <= PROMPT.length; i++) { input.textContent = PROMPT.slice(0, i); await sleep(70); }
      await sleep(350);
      for (const c of chips) { c.classList.add('on'); await sleep(380); }
      const ct = await ciphertext();
      void packet.offsetWidth;
      packet.classList.add('go');
      await sleep(900);
      await scramble(relay, ct, 700);
      await sleep(600);
      await scramble(out, PROMPT, 800);
      await sleep(2600);
    });
  })();

  // ---- 02 receipts: real SHA-256 over whatever the visitor leaves in the chunks ----
  (function receipts() {
    const WORDS = ['量子隧穿', '是指粒子', '能穿过', '经典力学', '下无法', '越过的势垒'];
    const FORGED = ['量子隧穿', '是指波子', '不能穿过', '经典力学', '下可以', '越过的势垒'];
    const box = $('chunks'), signedEl = $('hash-signed'), localEl = $('hash-local'), verdict = $('verdict');
    let signed = '', touched = false;
    const chunks = WORDS.map((w, i) => {
      const b = document.createElement('button');
      b.type = 'button'; b.className = 'chunk'; b.textContent = w; b.dataset.i = i;
      b.addEventListener('click', () => { touched = true; flip(b); update(); });
      box.appendChild(b);
      return b;
    });
    // A chunk whose forgery reads the same (the first and fourth) is still changed by a marker.
    function forged(i) { return FORGED[i] === WORDS[i] ? WORDS[i] + '！' : FORGED[i]; }
    function flip(b, to) {
      const i = Number(b.dataset.i), bad = to === undefined ? !b.classList.contains('bad') : to;
      b.classList.toggle('bad', bad);
      b.textContent = bad ? forged(i) : WORDS[i];
    }
    async function sha(s) {
      if (!subtle) return s;
      return hex(new Uint8Array(await subtle.digest('SHA-256', utf8(s))));
    }
    const joined = () => chunks.map(b => b.textContent).join('');
    async function update() {
      const local = await sha(joined());
      localEl.textContent = local;
      const ok = local === signed;
      localEl.className = ok ? 'lime' : 'red';
      verdict.classList.toggle('bad', !ok);
      verdict.textContent = ok ? '哈希一致 → 买家签 Ack，作业结算' : '哈希不一致 → 买家拒签，可凭证据发起争议';
    }
    sha(WORDS.join('')).then(h => {
      signed = h; signedEl.textContent = h;
      update();
      if (still) return;
      // Until the visitor takes over, show one forgery at a time.
      loop($('receipt'), async () => {
        await sleep(3000);
        if (touched) { await sleep(5000); return; }
        const b = chunks[1 + Math.floor(Math.random() * 2)];
        flip(b, true); await update();
        await sleep(2800);
        if (touched) return;
        flip(b, false); await update();
      });
    });
  })();

  // ---- 03 one job, one key ----
  (function dispute() {
    const el = $('dispute'), jobs = $('jobs'), steps = Array.from($('steps').children);
    const tiles = Array.from({ length: 28 }, () => {
      const d = document.createElement('div');
      d.className = 'job';
      d.textContent = hex(crypto.getRandomValues(new Uint8Array(2)));
      jobs.appendChild(d);
      return d;
    });
    if (still) { tiles[17].classList.add('open'); steps.forEach(s => s.classList.add('on')); return; }
    loop(el, async () => {
      const t = tiles[Math.floor(Math.random() * tiles.length)];
      const id = t.textContent;
      await sleep(700);
      t.classList.add('open'); steps[0].classList.add('on'); t.textContent = '已解开';
      for (const s of steps.slice(1)) { await sleep(850); s.classList.add('on'); }
      await sleep(2800);
      t.classList.remove('open'); t.textContent = id;
      steps.forEach(s => s.classList.remove('on'));
    });
  })();

  // ---- 04 the TEE switch ----
  (function tee() {
    const sw = $('tee'), why = $('node-why'), verdict = $('node-verdict'), formula = $('tee-formula');
    const off = why.textContent;
    sw.addEventListener('click', () => {
      const on = sw.getAttribute('aria-checked') !== 'true';
      sw.setAttribute('aria-checked', String(on));
      verdict.textContent = on ? '看不到' : '能看到';
      verdict.className = on ? 'no' : 'yes';
      why.textContent = on ? '收件钥只存在于机密虚拟机内，节点运营者也读不到内存' : off;
      formula.hidden = !on;
    });
  })();

  // ---- placeholders and sign-up ----
  document.querySelectorAll('[data-soon]').forEach(a => a.addEventListener('click', e => {
    e.preventDefault();
    $('soon-msg').textContent = a.querySelector('b').textContent + ' 版即将上线，敬请期待';
  }));

  // The call-to-action follows the portal's own setting: no sign-up button while registration is closed.
  fetch('/api/v1/info', { headers: { Accept: 'application/json' } }).then(r => r.ok ? r.json() : null).then(info => {
    if (!info || !info.registration_open) return;
    $('cta-register').hidden = false;
    if (info.signup_credit_micros > 0) {
      const note = $('cta-credit');
      note.textContent = '注册即赠送 ' + (info.signup_credit_micros / 1e6).toLocaleString('zh-CN') + ' credit';
      note.hidden = false;
    }
  }).catch(() => {});
});
