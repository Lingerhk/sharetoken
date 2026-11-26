// The blog at /blog/: the list of articles and one article. The portal lists
// them (GET /api/v1/blog) and hands out each one's Markdown, which is drawn
// here into the DOM with textContent only, never as HTML (see the portal's CSP).
// Articles are written in English and Simplified Chinese; a reader of another
// language gets English, a reader of Traditional Chinese the Simplified one.
//
// Markdown: headings (#, ##, ###, ####), paragraphs, lists (nested by indent),
// tables, code fences, > quotes and > [!NOTE] / [!TIP] / [!WARNING] callouts,
// ---, images ![alt](file "caption") (a picture alone on a line is a figure),
// `code`, **bold**, *italic*, [links](url). Relative picture paths are the
// article's own files under /blog/<slug>/.
'use strict';

I18N.ready.then(async () => {
  document.getElementById('theme-toggle').addEventListener('click', THEME.toggle);
  const tr = I18N.t;
  const main = document.getElementById('main');
  const lang = I18N.lang === 'zh-CN' || I18N.lang === 'zh-TW' ? 'zh-CN' : 'en';

  function mk(tag, attrs, ...kids) {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v === undefined || v === null || v === false) continue;
      if (k === 'class') e.className = v; else e.setAttribute(k, v);
    }
    for (const c of kids.flat()) if (c !== null && c !== undefined && c !== false) e.append(c);
    return e;
  }
  const date = d => {
    const t = new Date(d + 'T00:00:00');
    return isNaN(t) ? d : t.toLocaleDateString(I18N.lang, { year: 'numeric', month: 'long', day: 'numeric' });
  };
  const minutes = n => tr('{n} 分钟阅读', { n });
  async function getJSON(url) {
    const r = await fetch(url, { headers: { Accept: 'application/json' } });
    if (!r.ok) { const e = new Error('http ' + r.status); e.status = r.status; throw e; }
    return r.json();
  }

  // ---- Markdown ----
  let base = '/';
  const resolve = url => (/^(https?:)?\/\//.test(url) || /^[/#]/.test(url) ? url : base + url.replace(/^\.\//, ''));
  function inline(text) {
    const out = [];
    const re = /`([^`]+)`|!\[([^\]]*)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)|\*\*(.+?)\*\*|\*([^*\s][^*]*?)\*|\[([^\]]+)\]\(([^)\s]+)\)/g;
    let at = 0, m;
    while ((m = re.exec(text))) {
      if (m.index > at) out.push(text.slice(at, m.index));
      if (m[1] !== undefined) out.push(mk('code', null, m[1]));
      else if (m[3] !== undefined) out.push(picture(m[2], m[3], m[4], true));
      else if (m[5] !== undefined) out.push(mk('strong', null, inline(m[5])));
      else if (m[6] !== undefined) out.push(mk('em', null, inline(m[6])));
      else out.push(link(m[7], m[8]));
      at = re.lastIndex;
    }
    if (at < text.length) out.push(text.slice(at));
    return out;
  }
  function link(text, url) {
    const external = /^https?:\/\//.test(url);
    if (!external && !/^[/#]/.test(url)) return inline(text);
    return mk('a', external ? { href: url, target: '_blank', rel: 'noopener noreferrer' } : { href: url }, inline(text));
  }
  function picture(alt, src, caption, small) {
    if (!/^(https?:\/\/|\/)/.test(src) && !/^[\w./-]+$/.test(src)) return alt;
    const img = mk('img', { src: resolve(src), alt, loading: 'lazy', decoding: 'async' });
    if (small) return img;
    img.addEventListener('click', () => zoom(img));
    img.classList.add('zoomable');
    return mk('figure', null, img, caption || alt ? mk('figcaption', null, inline(caption || alt)) : null);
  }
  function codeBlock(lang, body) {
    const code = mk('code', null);
    const commentMark = /^(json|text|xml|)$/.test(lang) ? null : '#';
    const lines = body.split('\n');
    lines.forEach((line, i) => {
      const text = line + (i < lines.length - 1 ? '\n' : '');
      if (commentMark && line.trimStart().startsWith(commentMark)) code.append(mk('span', { class: 'comment' }, text));
      else if (/^(shell|bash|sh|console|zsh|powershell)$/.test(lang) && /^\$ /.test(line)) code.append(mk('span', { class: 'prompt' }, '$ '), text.slice(2));
      else code.append(text);
    });
    const copy = mk('button', { class: 'copy', type: 'button' }, tr('复制'));
    copy.addEventListener('click', async () => {
      const plain = lines.map(l => l.replace(/^\$ /, '')).filter(l => !(commentMark && l.trimStart().startsWith(commentMark))).join('\n').trim();
      let ok = false;
      try { await navigator.clipboard.writeText(plain); ok = true; } catch (e) { /* not a secure context */ }
      copy.textContent = ok ? tr('已复制') : tr('复制失败');
      copy.classList.toggle('done', ok);
      setTimeout(() => { copy.textContent = tr('复制'); copy.classList.remove('done'); }, 1600);
    });
    return mk('div', { class: 'code' }, mk('div', { class: 'code-head' }, mk('span', null, lang || ''), copy), mk('pre', null, code));
  }
  const splitRow = line => line.trim().replace(/^\|/, '').replace(/\|$/, '').split(/(?<!\\)\|/).map(c => c.trim().replace(/\\\|/g, '|'));
  const usedIds = new Set();
  function anchorId(text) {
    let id = text.toLowerCase().replace(/[`*_[\]()]/g, '').replace(/[^\p{L}\p{N}]+/gu, '-').replace(/^-+|-+$/g, '').slice(0, 60) || 'section';
    let n = 2, out = id;
    while (usedIds.has(out)) out = id + '-' + n++;
    usedIds.add(out);
    return out;
  }
  function parse(lines, parent, toc) {
    let i = 0;
    const isBlockStart = l => /^(#{1,4} |```|> ?|\s*[-*] |\s*\d+\. |\||!\[|---\s*$)/.test(l);
    while (i < lines.length) {
      const line = lines[i];
      if (!line.trim()) { i++; continue; }
      let m;
      if ((m = line.match(/^(#{1,4}) (.+)$/))) {
        const level = Math.max(2, m[1].length);
        const id = anchorId(m[2]);
        const h = mk('h' + level, { id }, inline(m[2]), mk('a', { class: 'anchor', href: '#' + id, 'aria-hidden': 'true', tabindex: '-1' }, '#'));
        if (toc && level <= 3) toc.push({ level, id, text: h.firstChild ? [...h.childNodes].slice(0, -1).map(n => n.textContent).join('') : '' });
        parent.append(h);
        i++;
        continue;
      }
      if ((m = line.match(/^```([\w-]*)\s*$/))) {
        let j = i + 1;
        while (j < lines.length && !/^```\s*$/.test(lines[j])) j++;
        parent.append(codeBlock(m[1], lines.slice(i + 1, j).join('\n')));
        i = j + 1;
        continue;
      }
      if (/^(---|\*\*\*)\s*$/.test(line)) { parent.append(mk('hr')); i++; continue; }
      if ((m = line.match(/^!\[([^\]]*)\]\(([^)\s]+)(?:\s+"([^"]*)")?\)\s*$/))) { parent.append(picture(m[1], m[2], m[3])); i++; continue; }
      if (/^> ?/.test(line)) {
        let j = i;
        while (j < lines.length && /^>/.test(lines[j])) j++;
        const body = lines.slice(i, j).map(l => l.replace(/^> ?/, ''));
        const km = body[0].match(/^\[!(NOTE|TIP|WARNING)\]\s*$/);
        if (km) {
          const kind = km[1].toLowerCase();
          const box = mk('div', { class: 'callout ' + kind }, mk('b', null, { note: tr('说明'), tip: tr('提示'), warning: tr('注意') }[kind]));
          parse(body.slice(1), box);
          parent.append(box);
        } else {
          const q = mk('blockquote');
          parse(body, q);
          parent.append(q);
        }
        i = j;
        continue;
      }
      if (line.startsWith('|')) {
        let j = i;
        while (j < lines.length && lines[j].startsWith('|')) j++;
        const head = splitRow(lines[i]), rows = lines.slice(i + 2, j).map(splitRow);
        parent.append(mk('div', { class: 'table' }, mk('table', null,
          mk('thead', null, mk('tr', null, head.map(c => mk('th', { scope: 'col' }, inline(c))))),
          mk('tbody', null, rows.map(r => mk('tr', null, r.map(c => mk('td', null, inline(c)))))))));
        i = j;
        continue;
      }
      if ((m = line.match(/^(\s*)([-*]|\d+\.) /))) {
        const indent = m[1].length, ordered = /\d/.test(m[2]);
        const itemRe = ordered ? /^(\s*)(\d+)\. (.*)$/ : /^(\s*)[-*] (.*)$/;
        const list = mk(ordered ? 'ol' : 'ul');
        if (ordered) { const start = Number(line.match(/(\d+)\./)[1]); if (start !== 1) list.setAttribute('start', start); }
        while (i < lines.length) {
          const im = lines[i].match(itemRe);
          if (!im || im[1].length !== indent) break;
          const body = [ordered ? im[3] : im[2]];
          i++;
          // An item runs on over lines indented deeper than its marker, blank lines included.
          while (i < lines.length) {
            const l = lines[i];
            if (!l.trim()) {
              let k = i + 1;
              while (k < lines.length && !lines[k].trim()) k++;
              if (k < lines.length && lines[k].match(/^\s*/)[0].length > indent) { for (; i < k; i++) body.push(''); continue; }
              break;
            }
            if (l.match(/^\s*/)[0].length <= indent) break;
            body.push(l.slice(Math.min(l.match(/^\s*/)[0].length, indent + (ordered ? 3 : 2))));
            i++;
          }
          const li = mk('li');
          parse(body, li);
          if (li.firstElementChild && li.firstElementChild.tagName === 'P' && !body.some(b => !b.trim())) li.firstElementChild.replaceWith(...li.firstElementChild.childNodes);
          list.append(li);
          while (i < lines.length && !lines[i].trim() && i + 1 < lines.length && itemRe.test(lines[i + 1]) && lines[i + 1].match(/^\s*/)[0].length === indent) i++;
        }
        parent.append(list);
        continue;
      }
      let j = i + 1;
      while (j < lines.length && lines[j].trim() && !isBlockStart(lines[j])) j++;
      const p = mk('p');
      lines.slice(i, j).forEach((l, k) => {
        if (k) p.append(/ {2}$/.test(lines[i + k - 1]) ? mk('br') : ' ');
        p.append(...inline(l.trim()));
      });
      parent.append(p);
      i = j;
    }
  }

  // ---- picture zoom ----
  const box = document.getElementById('lightbox'), boxImg = document.getElementById('lightbox-img');
  function zoom(img) {
    boxImg.src = img.currentSrc || img.src;
    boxImg.alt = img.alt;
    box.hidden = false;
    document.body.classList.add('locked');
    document.getElementById('lightbox-close').focus();
  }
  function unzoom() { box.hidden = true; document.body.classList.remove('locked'); }
  box.addEventListener('click', unzoom);
  addEventListener('keydown', e => { if (e.key === 'Escape' && !box.hidden) unzoom(); });

  // ---- pieces ----
  function tags(p) {
    return p.tags && p.tags.length ? mk('ul', { class: 'tags' }, p.tags.map(t => mk('li', null, t))) : null;
  }
  function meta(p) {
    return mk('p', { class: 'meta' }, mk('time', { datetime: p.date }, date(p.date)), mk('span', { class: 'sep', 'aria-hidden': 'true' }, '·'), minutes(p.minutes),
      p.author ? [mk('span', { class: 'sep', 'aria-hidden': 'true' }, '·'), p.author] : null);
  }
  function cover(p, cls) {
    return mk('div', { class: 'cover ' + (cls || '') }, p.cover ? mk('img', { src: p.cover, alt: '', loading: 'lazy', decoding: 'async' }) : mk('span', { class: 'cover-fallback', 'aria-hidden': 'true' }, p.title.slice(0, 1)));
  }
  function card(p, featured) {
    return mk('a', { class: 'card' + (featured ? ' featured' : ''), href: '/blog/' + p.slug },
      cover(p),
      mk('div', { class: 'card-body' }, tags(p), mk(featured ? 'h2' : 'h3', null, p.title), p.summary ? mk('p', { class: 'summary' }, p.summary) : null, meta(p),
        mk('span', { class: 'more' }, tr('阅读全文'), mk('span', { 'aria-hidden': 'true' }, ' →'))));
  }
  function failure(text) {
    return mk('section', { class: 'empty' }, mk('p', null, text), mk('a', { class: 'btn', href: '/blog/' }, tr('返回全部文章')));
  }

  // ---- the list ----
  async function showList() {
    document.title = tr('最佳实践') + ' · ShareToken';
    const head = mk('section', { class: 'list-head' },
      mk('p', { class: 'eyebrow' }, 'BLOG'),
      mk('h1', null, tr('最佳实践')),
      mk('p', { class: 'lead' }, tr('一步一步的实战教程：照着做，就能把 ShareToken 用起来。')));
    main.replaceChildren(head);
    let data;
    try { data = await getJSON('/api/v1/blog?lang=' + lang); } catch (e) { main.append(failure(tr('加载失败，请稍后再试。'))); return; }
    if (!data.posts.length) { main.append(mk('section', { class: 'empty' }, mk('p', null, tr('暂时还没有文章。')))); return; }
    const [first, ...rest] = data.posts;
    main.append(mk('section', { class: 'list' }, card(first, true), rest.length ? mk('div', { class: 'grid' }, rest.map(p => card(p))) : null));
  }

  // ---- one article ----
  async function showPost(slug) {
    let a;
    try { a = await getJSON('/api/v1/blog/' + encodeURIComponent(slug) + '?lang=' + lang); } catch (e) {
      main.replaceChildren(failure(e.status === 404 ? tr('文章不存在或已下线。') : tr('加载失败，请稍后再试。')));
      return;
    }
    const p = a.post;
    document.title = p.title + ' · ShareToken';
    base = '/blog/' + p.slug + '/';
    const toc = [];
    const body = mk('div', { class: 'prose' });
    parse(a.markdown.split('\n'), body, toc);

    const langLinks = p.langs.length > 1 ? mk('div', { class: 'langs', role: 'group', 'aria-label': tr('文章语言') },
      p.langs.map(l => mk('a', { href: '?lang=' + l, 'aria-current': l === p.lang ? 'true' : null }, l === 'en' ? 'English' : '\u7b80\u4f53\u4e2d\u6587'))) : null;
    const only = p.lang !== lang ? mk('p', { class: 'lang-note' }, p.lang === 'en' ? tr('这篇文章目前只有英文版本。') : tr('这篇文章目前只有中文版本。')) : null;

    const header = mk('header', { class: 'post-head' },
      mk('a', { class: 'crumb', href: '/blog/' }, mk('span', { 'aria-hidden': 'true' }, '← '), tr('最佳实践')),
      tags(p), mk('h1', null, p.title), p.summary ? mk('p', { class: 'lead' }, p.summary) : null,
      mk('div', { class: 'post-meta' }, meta(p), langLinks), only,
      p.cover ? mk('div', { class: 'hero-cover' }, mk('img', { src: p.cover, alt: '' })) : null);

    const tocBox = toc.length > 2 ? mk('aside', { class: 'toc', 'aria-label': tr('本文目录') },
      mk('p', { class: 'toc-title' }, tr('本文目录')),
      mk('ol', null, toc.map(t => mk('li', t.level === 3 ? { class: 'sub' } : null, mk('a', { href: '#' + t.id, 'data-id': t.id }, t.text))))) : null;

    const pager = mk('nav', { class: 'pager', 'aria-label': tr('更多文章') },
      a.newer ? mk('a', { class: 'newer', href: '/blog/' + a.newer.slug }, mk('small', null, tr('较新的文章')), mk('b', null, a.newer.title)) : mk('span'),
      a.older ? mk('a', { class: 'older', href: '/blog/' + a.older.slug }, mk('small', null, tr('较早的文章')), mk('b', null, a.older.title)) : mk('span'));

    main.replaceChildren(mk('article', { class: 'post' }, header,
      mk('div', { class: 'post-body' + (tocBox ? ' with-toc' : '') }, body, tocBox),
      pager, mk('p', { class: 'back' }, mk('a', { class: 'btn', href: '/blog/' }, tr('返回全部文章')))));

    if (location.hash) { const t = document.getElementById(decodeURIComponent(location.hash.slice(1))); if (t) t.scrollIntoView(); }
    if (tocBox && 'IntersectionObserver' in window) {
      const heads = toc.map(t => document.getElementById(t.id)), links = [...tocBox.querySelectorAll('a')], seen = new Set();
      const ob = new IntersectionObserver(es => {
        for (const e of es) { if (e.isIntersecting) seen.add(e.target); else seen.delete(e.target); }
        const cur = heads.find(h => seen.has(h));
        if (cur) links.forEach(l => l.classList.toggle('on', l.dataset.id === cur.id));
      }, { rootMargin: '-90px 0px -70% 0px' });
      heads.forEach(h => ob.observe(h));
    }
  }

  const m = location.pathname.match(/^\/blog\/([a-z0-9-]+)\/?$/);
  if (m) await showPost(m[1]); else await showList();
});
