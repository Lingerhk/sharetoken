// The operator console: platform overview, users, relays, jobs, nodes, audits, disputes, ledger.
'use strict';
(async () => {
  const { api, h, clear, add, credit, signed, num, time, ago, peer, status, badge, toast, fail, modal, confirm, secret, table, kv, field, input, state, pageTitle, section, tile } = ST;
  const app = ST.$('app');

  await ST.loadInfo().catch(fail);
  while (!(await ST.loadMe())) await ST.renderLogin(app, { title: '管理控制台' });
  const me = () => state.me;
  if (!me().user.is_admin) {
    add(clear(app), ST.authFrame(h('div', { class: 'card login' }, h('h1', null, '管理控制台'), h('p', null, '账户 ', h('b', null, me().user.username), ' 不是管理员。'),
      h('div', { class: 'row' }, h('a', { href: '/app/' }, '返回用户门户'), h('button', { class: 'small', onclick: ST.logout }, '退出')))));
    return;
  }

  const mailBadge = h('span', { class: 'nav-count', id: 'mail-unread', hidden: true, title: '未读邮件', 'data-unit': '{n} 封未读' });
  const feedbackBadge = h('span', { class: 'nav-count', id: 'feedback-new', hidden: true });
  const { nav, main } = ST.shell(app, {
    title: '管理控制台', role: '管理员',
    items: [
      { route: 'dashboard', icon: 'chart', label: '仪表盘' }, { route: 'overview', icon: 'grid', label: '总览' }, { route: 'users', icon: 'users', label: '用户' }, { route: 'relays', icon: 'server', label: '中继' }, { route: 'versions', icon: 'cube', label: '版本' },
      { route: 'jobs', icon: 'bolt', label: '作业' }, { route: 'nodes', icon: 'cpu', label: '节点' }, { route: 'audits', icon: 'shield', label: '推理审计' }, { route: 'disputes', icon: 'warning', label: '争议' }, { route: 'pairings', icon: 'swap', label: '配对' }, { route: 'partners', icon: 'link', label: '共享伙伴' },
      { route: 'ledger', icon: 'database', label: '账本' }, { route: 'mail', icon: 'mail', label: '邮件', badge: mailBadge }, { route: 'feedback', icon: 'chat', label: '反馈', badge: feedbackBadge },
      { route: 'oplog', icon: 'book', label: '操作日志' }, { route: 'settings', icon: 'cog', label: '系统设置' },
    ],
    links: [{ href: '/docs/', icon: 'book', label: '文档' }, { href: '/app/', icon: 'home', label: '用户门户' }],
  });

  const userLink = name => name ? h('a', { href: '#/users/' + encodeURIComponent(name) }, name) : null;
  const acct = (a, user) => user ? [userLink(user), ' ', h('span', { class: 'muted mono' }, a)] : h('code', { class: 'mono' }, a);
  function act(path, body, okMsg, reload) {
    return async () => { try { await api('POST', path, body); toast(okMsg || '已完成'); await reload(); } catch (ex) { fail(ex); } };
  }

  // ---- dashboard ----
  const SVGNS = 'http://www.w3.org/2000/svg';
  function svg(tag, attrs, ...kids) {
    const el = document.createElementNS(SVGNS, tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (v === null || v === undefined) continue;
      if (k === 'style') ST.setStyle(el, v); // CSP drops style attributes
      else el.setAttribute(k, String(v));
    }
    for (const c of kids.flat()) if (c) el.append(c);
    return el;
  }
  // bars is one series per day: thin bars, rounded tops, a hover tooltip per bar.
  function bars(days, value, fmt) {
    const W = 300, H = 130, top = 18, bottom = 22, n = days.length, step = W / n, bw = Math.max(4, Math.min(22, step * 0.55));
    const max = Math.max(1, ...days.map(value));
    const plot = H - top - bottom;
    const tip = h('div', { class: 'dash-tip', hidden: true });
    const g = svg('svg', { viewBox: '0 0 ' + W + ' ' + H, class: 'dash-chart', role: 'img' });
    g.append(svg('line', { x1: 0, x2: W, y1: H - bottom + 0.5, y2: H - bottom + 0.5, class: 'dash-axis' }));
    days.forEach((d, i) => {
      const v = value(d), x = i * step + (step - bw) / 2, bh = v ? Math.max(3, v / max * plot) : 0, y = H - bottom - bh;
      if (bh) g.append(svg('path', { class: 'dash-bar', d: 'M' + x + ',' + (H - bottom) + 'V' + (y + 3) + 'q0,-3 3,-3h' + (bw - 6) + 'q3,0 3,3V' + (H - bottom) + 'Z' }));
      const hit = svg('rect', { x: i * step, y: 0, width: step, height: H, class: 'dash-hit' });
      hit.addEventListener('mouseenter', () => { tip.hidden = false; tip.textContent = d.day.slice(5) + '  ' + fmt(v); });
      hit.addEventListener('mouseleave', () => { tip.hidden = true; });
      g.append(hit);
      if (i === 0 || i === n - 1 || i === Math.floor(n / 2)) g.append(svg('text', { x: i * step + step / 2, y: H - 6, class: 'dash-label', 'text-anchor': 'middle' }, document.createTextNode(d.day.slice(5))));
    });
    const peak = days.reduce((a, d) => value(d) > value(a) ? d : a, days[0]);
    if (value(peak)) {
      const i = days.indexOf(peak);
      g.append(svg('text', { x: Math.min(W - 4, Math.max(4, i * step + step / 2)), y: H - bottom - value(peak) / max * plot - 4, class: 'dash-label strong', 'text-anchor': i === 0 ? 'start' : i === n - 1 ? 'end' : 'middle' }, document.createTextNode(fmt(value(peak)))));
    }
    if (!value(peak)) g.append(svg('text', { x: W / 2, y: (H - bottom) / 2, class: 'dash-label', 'text-anchor': 'middle' }, document.createTextNode('近 ' + n + ' 天没有数据')));
    return h('div', { class: 'dash-chart-wrap' }, g, tip);
  }
  // lineChart draws one line per relay over [from, to] (unix nanos). series: [{name, color, points: [{at, v, tip}]}].
  // A gap longer than 3 buckets breaks the line; hovering shows every relay's value at that moment.
  function lineChart(series, from, to, bucket, fmt, opts) {
    const W = 460, H = 170, L = 44, R = 8, T = 10, B = 22, pw = W - L - R, ph = H - T - B;
    const vals = series.flatMap(s => s.points.map(p => p.v));
    let max = Math.max(opts && opts.min || 0, ...vals); if (!(max > 0)) max = 1;
    if (opts && opts.cap) max = Math.min(max, opts.cap);
    max *= 1.1;
    const X = t => L + (t - from) / (to - from) * pw, Y = v => T + ph - Math.min(1, v / max) * ph;
    const g = svg('svg', { viewBox: '0 0 ' + W + ' ' + H, class: 'dash-chart', role: 'img' });
    for (let i = 0; i <= 3; i++) {
      const v = max / 1.1 * i / 3, y = Y(v);
      g.append(svg('line', { x1: L, x2: W - R, y1: y, y2: y, class: 'dash-axis' }),
        svg('text', { x: L - 5, y: y + 4, class: 'dash-label', 'text-anchor': 'end' }, document.createTextNode(fmt(v))));
    }
    const span = (to - from) / 1e9, fmtT = t => { const d = new Date(t / 1e6); return span > 86400 * 1.5 ? (d.getMonth() + 1) + '/' + d.getDate() + ' ' + String(d.getHours()).padStart(2, '0') + ':00' : d.toLocaleTimeString(undefined, { hour12: false, hour: '2-digit', minute: '2-digit' }); };
    [0, 1 / 3, 2 / 3, 1].forEach((f, i) => g.append(svg('text', { x: L + f * pw, y: H - 6, class: 'dash-label', 'text-anchor': i === 0 ? 'start' : i === 3 ? 'end' : 'middle' }, document.createTextNode(fmtT(from + f * (to - from))))));
    let any = false;
    for (const s of series) {
      let d = '', prev = null;
      for (const p of s.points) {
        any = true;
        d += (prev === null || p.at - prev > bucket * 3 ? 'M' : 'L') + X(p.at).toFixed(1) + ',' + Y(p.v).toFixed(1);
        prev = p.at;
      }
      if (d) g.append(svg('path', { d, class: 'dash-line', style: 'stroke:' + s.color }));
      if (s.points.length === 1) g.append(svg('circle', { cx: X(s.points[0].at), cy: Y(s.points[0].v), r: 2.5, style: 'fill:' + s.color }));
    }
    if (!any) g.append(svg('text', { x: L + pw / 2, y: T + ph / 2, class: 'dash-label', 'text-anchor': 'middle' }, document.createTextNode('这段时间没有数据')));
    const tip = h('div', { class: 'dash-tip', hidden: true }), cross = svg('line', { y1: T, y2: T + ph, class: 'dash-cross', visibility: 'hidden' });
    g.append(cross);
    const hit = svg('rect', { x: L, y: T, width: pw, height: ph, class: 'dash-hit-all' });
    hit.addEventListener('mousemove', e => {
      const r = g.getBoundingClientRect(), x = (e.clientX - r.left) / r.width * W, t = from + Math.min(1, Math.max(0, (x - L) / pw)) * (to - from);
      const rows = series.map(s => {
        let best = null;
        for (const p of s.points) if (!best || Math.abs(p.at - t) < Math.abs(best.at - t)) best = p;
        return best && Math.abs(best.at - t) <= bucket * 2 ? s.name + ' ' + (best.tip || fmt(best.v)) : null;
      }).filter(Boolean);
      cross.setAttribute('x1', x); cross.setAttribute('x2', x); cross.setAttribute('visibility', 'visible');
      tip.hidden = false; tip.textContent = fmtT(t) + (rows.length ? '  ' + rows.join(' · ') : '');
    });
    hit.addEventListener('mouseleave', () => { tip.hidden = true; cross.setAttribute('visibility', 'hidden'); });
    g.append(hit);
    return h('div', { class: 'dash-chart-wrap' }, g, tip);
  }
  const SERIES_COLORS = ['var(--accent)', 'var(--blue)', 'var(--violet)', 'var(--warn)', 'var(--bad)', 'var(--ok)', '#db2777', '#0891b2'];
  // meter is used / max as a thin bar; warn at 80%.
  function meter(used, max) {
    const pct = max ? Math.min(1, used / max) : 0;
    return h('span', { class: 'dash-meter' }, svg('svg', { viewBox: '0 0 60 6', width: 60, height: 6 },
      svg('rect', { x: 0, y: 0, width: 60, height: 6, rx: 3, class: 'dash-meter-bg' }),
      pct ? svg('rect', { x: 0, y: 0, width: Math.max(3, pct * 60), height: 6, rx: 3, class: 'dash-meter-fill' + (pct >= 0.8 ? ' warn' : '') }) : null),
    ' ', h('span', { class: 'mono' }, num(used) + ' / ' + (max ? num(max) : '∞')));
  }
  function bytes(n) {
    n = Number(n || 0); const u = ['B', 'KB', 'MB', 'GB', 'TB']; let i = 0;
    while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
    return (i ? n.toFixed(n < 10 ? 1 : 0) : String(Math.round(n))) + ' ' + u[i];
  }
  function uptime(nanos) {
    if (!nanos) return '—';
    const s = Math.max(0, Date.now() / 1000 - nanos / 1e9), d = Math.floor(s / 86400), hh = Math.floor(s % 86400 / 3600), mm = Math.floor(s % 3600 / 60);
    return d ? d + ' 天 ' + hh + ' 小时' : hh ? hh + ' 小时 ' + mm + ' 分' : mm + ' 分';
  }
  function relayHealth(x) {
    const mt = x.metrics;
    if (!x.enabled) return ['停用', ''];
    if (!x.coordinator_ok) return ['协调器不可达', 'bad'];
    if (mt && mt.error && (!mt.ok_at || Date.now() * 1e6 - mt.ok_at > 5 * 60e9)) return ['指标读取失败', 'warn'];
    if (mt && mt.ok_at && !mt.service_enabled) return ['服务未运行', 'bad'];
    if (mt && (mt.networks || []).some(n => n.coordinators_reserved < n.coordinators_expected)) return ['有网络未预约', 'warn'];
    return ['正常', 'ok'];
  }

  async function dashboard(m) {
    const stamp = h('small'), body = h('div');
    add(m, pageTitle('仪表盘', stamp), body);
    async function load() {
      const d = await api('GET', '/api/v1/admin/dashboard');
      const u = d.users, dv = d.devices, nd = d.nodes, day = d.day;
      const healthy = d.relays.filter(x => relayHealth(x)[1] === 'ok').length, enabled = d.relays.filter(x => x.enabled).length;
      const dayStatus = Object.entries(day.by_status || {}).map(([k, v]) => (ST.STATUS[k] ? ST.STATUS[k][0] : k) + ' ' + v).join('，');
      stamp.textContent = '每 30 秒刷新 · 更新于 ' + new Date(d.now / 1e6).toLocaleTimeString(undefined, { hour12: false }) + (d.relay_metrics ? ' · 中继指标经 SSH 每 ' + d.relay_metrics_interval_s + ' 秒采集' : ' · 未配置中继指标采集');
      add(clear(body),
        h('div', { class: 'tiles' },
          tile('用户', num(u.total), '24 小时新增 ' + u.new_24h + ' · 7 天 ' + u.new_7d + (u.disabled ? ' · 禁用 ' + u.disabled : ''), 'users'),
          tile('活跃用户（24 小时）', num(u.active_24h), '用过 token 或设备', 'user', 'blue'),
          tile('在线设备', num(dv.online) + ' / ' + num(dv.total), '10 分钟内在线；24 小时内 ' + dv.seen_24h, 'desktop', 'blue'),
          tile('接单节点（24 小时）', num(nd.active_24h), '共 ' + nd.total + ' 个' + (nd.suspended ? '，暂停 ' + nd.suspended : ''), 'cpu', 'violet', nd.suspended ? 'warn' : ''),
          tile('作业（24 小时）', num(day.jobs), dayStatus || '无', 'bolt', 'blue', (day.by_status || {}).disputed ? 'warn' : ''),
          tile('消费（24 小时）', credit(day.cost_micros, 2), '手续费 ' + credit(day.fee_micros) + ' · 买家 ' + day.buyers, 'dollar', 'ok'),
          tile('Token（24 小时）', num(day.tokens), '输入 + 输出', 'chart', 'violet'),
          tile('中继', healthy + ' / ' + enabled, '正常 / 启用（共 ' + d.relays.length + ' 个）', 'server', '', enabled && healthy < enabled ? (healthy ? 'warn' : 'bad') : 'ok')),
        h('div', { class: 'dash-grid' },
          h('div', { class: 'card' }, section('users', '每日新增用户'), bars(d.daily, x => x.users, num)),
          h('div', { class: 'card' }, section('bolt', '每日作业'), bars(d.daily, x => x.jobs, num)),
          h('div', { class: 'card' }, section('chart', '每日 Token'), bars(d.daily, x => x.tokens, num))),
        section('server', '中继状态', h('small', null, d.relay_metrics ? '协调器每 15 秒检查可达性；连接、预约、流量来自中继自己的 /metrics' : '只有协调器的可达性检查')),
        h('div', { class: 'card' }, table([
          { title: '中继', cell: x => [h('a', { href: '#/relays' }, x.name || '#' + x.id), h('div', { class: 'muted' }, x.region || '')] },
          { title: '状态', cell: x => { const [t, k] = relayHealth(x); return badge(t, k); } },
          { title: '版本', cell: x => (x.metrics && x.metrics.version) || x.version || '—' },
          { title: '延迟', num: true, cell: x => ST.rtt(x.rtt_ms) },
          { title: '连接', cell: x => x.metrics && x.metrics.ok_at ? meter(x.metrics.connections, x.metrics.max_connections) : '—' },
          { title: '预约', cell: x => x.metrics && x.metrics.ok_at ? meter(x.metrics.reservations, x.metrics.max_reservations) : '—' },
          { title: '电路', num: true, cell: x => x.metrics && x.metrics.ok_at ? num(x.metrics.circuits) : '—' },
          { title: '转发', num: true, cell: x => x.metrics && x.metrics.ok_at ? [x.metrics.relayed_bps >= 0 ? bytes(x.metrics.relayed_bps) + '/s' : '—', h('div', { class: 'muted' }, '累计 ' + bytes(x.metrics.relayed_bytes_total))] : '—' },
          { title: '拒绝预约', num: true, cell: x => x.metrics && x.metrics.ok_at ? [x.metrics.reservations_denied_per_min >= 0 ? h('span', { class: x.metrics.reservations_denied_per_min >= 60 ? 'warn' : '' }, x.metrics.reservations_denied_per_min.toFixed(1) + '/分') : '—', h('div', { class: 'muted' }, '累计 ' + num(x.metrics.reservations_denied_total))] : '—' },
          { title: 'CPU / 内存', num: true, cell: x => x.metrics && x.metrics.ok_at ? [(x.metrics.cpu_percent >= 0 ? x.metrics.cpu_percent.toFixed(1) + '%' : '—'), h('div', { class: 'muted' }, bytes(x.metrics.rss_bytes))] : '—' },
          { title: '运行', cell: x => x.metrics && x.metrics.ok_at ? uptime(x.metrics.started_at) : '—' },
          { title: '网络预约', cell: x => x.metrics && x.metrics.ok_at ? h('span', { class: 'row' }, (x.metrics.networks || []).map(n =>
            badge(n.network + ' ' + n.coordinators_reserved + '/' + n.coordinators_expected + (n.max_reservations ? ' · ' + n.reservations + '/' + n.max_reservations : ' · ' + n.reservations), n.coordinators_reserved < n.coordinators_expected ? 'warn' : 'ok'))) : '—' },
          { title: '采集', cell: x => !x.metrics ? h('span', { class: 'muted' }, d.relay_metrics ? '未配置' : '—')
            : [ago(x.metrics.ok_at || x.metrics.scraped_at), x.metrics.error ? h('div', { class: 'bad dash-err', title: x.metrics.error }, x.metrics.error) : h('div', { class: 'muted' }, x.metrics.scrape_ms + ' ms')] },
        ], d.relays, '还没有登记中继')),
        h('div', { class: 'dash-grid two' },
          h('div', { class: 'card' }, section('cube', '客户端版本'), table([
            { title: '版本', cell: x => x.version || '未上报' }, { title: '设备', num: true, cell: x => num(x.devices) },
          ], d.client_versions || [], '还没有设备')),
          h('div', { class: 'card' }, section('bolt', '作业（全部）'), table([
            { title: '状态', cell: x => status(x[0]) }, { title: '数量', num: true, cell: x => num(x[1]) },
          ], Object.entries(d.jobs || {}).sort((a, b) => b[1] - a[1]), '没有作业'),
            h('p', { class: 'muted' }, '协调器 ', d.coordinator ? h('code', { class: 'mono' }, ST.short(d.coordinator, 10)) : h('span', { class: 'warn' }, '尚未启动')))));
    }
    await load();
    const timer = setInterval(() => {
      if (!main.contains(body)) { clearInterval(timer); return; }
      if (document.hidden) return;
      load().catch(() => { stamp.textContent = '刷新失败，30 秒后重试'; });
    }, 30000);
  }

  // ---- overview ----
  async function overview(m) {
    const o = await api('GET', '/api/v1/admin/overview');
    const jobs = o.jobs || {};
    add(m, pageTitle('平台总览'),
      h('div', { class: 'tiles' },
        tile('用户', num(o.users), o.disabled_users ? o.disabled_users + ' 个已禁用' : '无禁用', 'users'),
        tile('设备', num(o.devices), null, 'desktop', 'blue'),
        tile('流通 credit', credit(o.circulating_micros), '已发放到网络中的额度', 'dollar', 'ok'),
        tile('平台手续费', credit(o.platform_fees_micros), '费率 ' + (o.fee_bps / 100).toFixed(2) + '%', 'chart', 'violet'),
        tile('冻结中', credit(o.open_holds_micros), '进行中作业的预留额', 'lock', 'warn'),
        tile('账本', o.books_balance ? '平衡' : '不平衡', o.books_error || '从流水回放各账户一致', 'database', '', o.books_balance ? 'ok' : 'bad'),
        tile('中继', o.relays_ok + ' / ' + o.relays_enabled, '可达 / 启用（共 ' + o.relays + ' 个）', 'server', '', o.relays_enabled && o.relays_ok === 0 ? 'bad' : ''),
        tile('作业', num(Object.values(jobs).reduce((a, b) => a + b, 0)), Object.entries(jobs).map(([k, v]) => (ST.STATUS[k] ? ST.STATUS[k][0] : k) + ' ' + v).join('，') || '无', 'bolt', 'blue', jobs.disputed ? 'warn' : '')),
      h('div', { class: 'card' }, kv([['协调器', o.coordinator ? h('code', { class: 'mono' }, o.coordinator) : h('span', { class: 'warn' }, '尚未启动')],
        ['注册', state.info.registration_open ? '开放（赠送 ' + credit(state.info.signup_credit_micros) + ' credit）' : '关闭，仅管理员创建用户'],
        ['中继凭证', state.info.passes_enabled ? '已启用' : '未启用（开放中继）'], ['浏览器会话有效期', state.info.session_hours + ' 小时']])));
    const a = await api('GET', '/api/v1/admin/accounts');
    add(m, section('creditCard', '账户余额', h('small', null, 'external 为负数表示已发放的 credit')),
      h('div', { class: 'card' }, table([
        { title: '账户', cell: r => acct(r.account, r.user) },
        { title: '余额', num: true, cell: r => h('span', { class: r.balance_micros < 0 ? 'bad' : '' }, credit(r.balance_micros)) },
        { title: '冻结', num: true, cell: r => credit(r.held_micros) },
      ], a.accounts)));
  }

  // ---- users ----
  async function users(m, rest) {
    if (rest && rest[0]) return userDetail(m, decodeURIComponent(rest[0]));
    const q = input({ type: 'search', id: 'users-search', placeholder: '搜索用户名' });
    const createBtn = h('button', { class: 'primary', onclick: createUser }, '新建用户');
    const list = ST.listView({ path: '/api/v1/admin/users/search', key: 'users', empty: '没有匹配的用户', params: () => ({ q: q.value.trim() }), tools: q, actions: createBtn, columns: [
      { title: '用户名', cell: u => [userLink(u.username), ' ', u.is_admin ? badge('管理员', 'accent') : null, ' ', u.disabled ? badge('已禁用', 'bad') : null] },
      { title: 'ID', num: true, cell: u => String(u.id) },
      { title: '余额', num: true, cell: u => credit(u.available_micros) },
      { title: '冻结', num: true, cell: u => credit(u.held_micros) },
      { title: '设备', num: true, cell: u => String(u.devices) },
      { title: 'Token', num: true, cell: u => String(u.tokens) },
      { title: '注册', cell: u => time(u.created_at) },
    ] });
    add(m, pageTitle('用户', h('small', null, '日期筛选按注册时间')), list.el);
    let timer; q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => list.reload(), 250); });
    async function createUser() {
      const name = input({ type: 'email', id: 'new-user-email', placeholder: 'name@example.com', autocomplete: 'off' });
      const admin = h('input', { type: 'checkbox', id: 'new-user-admin' });
      if (!await modal({ title: '新建用户', okText: '创建', body: h('div', { class: 'form' }, field('邮箱', name), h('label', { class: 'check' }, admin, '管理员'),
        h('p', { class: 'muted' }, '没有密码：用户用发到这个邮箱的验证码登录。管理员第一次登录时要绑定两步验证。')) })) return;
      try { const u = await api('POST', '/api/v1/admin/users', { username: name.value.trim(), is_admin: admin.checked }); toast('已创建 ' + u.username); location.hash = '#/users/' + encodeURIComponent(u.username); }
      catch (ex) { fail(ex); }
    }
    await list.reload();
  }

  async function userDetail(m, name) {
    const head = h('h1', { class: 'page-title' }); const body = h('div'); add(m, head, body);
    async function load() {
      const d = await api('GET', '/api/v1/admin/users/' + encodeURIComponent(name));
      const u = d.user, isMe = u.id === me().user.id;
      add(clear(head), ST.pageChip(), h('span', { class: 'page-title-text' }, h('a', { href: '#/users' }, '用户'), ' / ', u.username, ' ', u.is_admin ? badge('管理员', 'accent') : null, ' ', u.disabled ? badge('已禁用', 'bad') : null, ' ',
        u.mfa_enrolled_at ? badge('两步验证', 'ok') : (u.is_admin ? badge('未绑定两步验证', 'warn') : null)));
      const actions = h('div', { class: 'row' },
        h('button', { class: 'primary', onclick: creditDialog }, '发放 / 扣减 credit'),
        h('button', { disabled: !u.mfa_enrolled_at, onclick: async () => {
          if (!await confirm('重置两步验证', '清除 ' + u.username + ' 的验证器绑定并吊销其全部 token 和会话。' + (u.is_admin ? '管理员下次登录时重新绑定。' : '之后只用邮箱验证码登录。') + (isMe ? '这会立即登出你自己。' : ''), '重置')) return;
          await act('/api/v1/admin/users/' + encodeURIComponent(name) + '/mfa/reset', undefined, '已重置两步验证', isMe ? () => location.reload() : load)();
        } }, '重置两步验证'),
        h('button', { disabled: isMe, onclick: act('/api/v1/admin/users/' + encodeURIComponent(name) + '/admin', { is_admin: !u.is_admin }, u.is_admin ? '已撤销管理员' : '已设为管理员', load) }, u.is_admin ? '撤销管理员' : '设为管理员'),
        h('button', { class: u.disabled ? '' : 'danger', disabled: isMe, onclick: async () => {
          if (!u.disabled && !await confirm('禁用用户', '禁用后 ' + u.username + ' 无法登录，其 token 立即失效，设备被协调器拒绝。', '禁用')) return;
          await act('/api/v1/admin/users/' + encodeURIComponent(name) + (u.disabled ? '/enable' : '/disable'), undefined, u.disabled ? '已启用' : '已禁用', load)();
        } }, u.disabled ? '启用' : '禁用'),
        h('button', { class: 'danger', onclick: async () => {
          if (!await confirm('吊销全部 token', '该用户的所有 token 和浏览器会话都会失效，设备需要重新登录。', '全部吊销')) return;
          await act('/api/v1/admin/users/' + encodeURIComponent(name) + '/tokens/revoke', undefined, '已吊销', load)();
        } }, '吊销全部 token'));
      const b = d.balance, us = d.usage;
      add(clear(body), 
        h('div', { class: 'tiles' },
          tile('可用余额', credit(b.available_micros), b.account, 'creditCard', 'ok'), tile('冻结', credit(b.held_micros), null, 'lock', 'warn'),
          tile('买入', us.bought.jobs + ' 单', '花费 ' + credit(us.bought.micros), 'trendingUp', 'blue'), tile('卖出', us.sold.jobs + ' 单', '净收入 ' + credit(us.sold.micros), 'dollar', 'violet'),
          tile('注册', time(u.created_at), 'ID ' + u.id, 'clock', '', 'small')),
        h('div', { class: 'card' }, actions),
        section('desktop', '设备'), h('div', { class: 'card' }, table([
          { title: '名称', cell: x => x.name }, { title: 'Peer ID', cell: x => h('code', { class: 'mono' }, x.peer) },
          { title: '绑定', cell: x => time(x.created_at) }, { title: '最近活动', cell: x => ago(x.last_seen_at) },
          { title: '', cell: x => h('button', { class: 'small danger', onclick: async () => {
            if (!await confirm('解绑设备', '解绑 ' + x.name + '（' + ST.short(x.peer, 8) + '）后协调器立即拒绝它。', '解绑')) return;
            try { await api('DELETE', '/api/v1/admin/users/' + encodeURIComponent(name) + '/devices/' + encodeURIComponent(x.peer)); toast('已解绑'); await load(); } catch (ex) { fail(ex); }
          } }, '解绑') },
        ], d.devices, '没有设备')),
        section('key', 'Token'), h('div', { class: 'card' }, table([
          { title: '名称', cell: t => t.name }, { title: '前缀', cell: t => h('code', null, t.prefix + '…') }, { title: '创建', cell: t => time(t.created_at) },
          { title: '最近使用', cell: t => ago(t.last_used_at) }, { title: '过期', cell: t => t.expires_at ? time(t.expires_at) : '永不' },
          { title: '', cell: t => h('button', { class: 'small danger', onclick: async () => {
            if (!await confirm('吊销 token', '吊销「' + t.name + '」。', '吊销')) return;
            try { await api('DELETE', '/api/v1/admin/users/' + encodeURIComponent(name) + '/tokens/' + t.id); toast('已吊销'); await load(); } catch (ex) { fail(ex); }
          } }, '吊销') },
        ], d.tokens, '没有有效 token')),
        section('creditCard', '账单'), await ledgerOf('/api/v1/admin/users/' + encodeURIComponent(name) + '/ledger'),
        section('bolt', '作业'), await jobsOf('/api/v1/admin/users/' + encodeURIComponent(name) + '/jobs'));

      async function creditDialog() {
        const amount = input({ type: 'number', step: 'any', placeholder: '正数发放，负数扣减（单位 credit）' });
        const memo = input({ type: 'text', maxlength: 128, placeholder: '例如 充值、纠错' });
        if (!await modal({ title: '调整 ' + u.username + ' 的 credit', okText: '提交', body: h('div', { class: 'form' }, field('金额（credit）', amount), field('备注', memo), h('p', { class: 'muted' }, '当前可用 ' + credit(b.available_micros) + '；扣减不能超过可用余额。')) })) return;
        const micros = Math.round(Number(amount.value) * 1e6);
        if (!micros) { toast('金额无效', 'bad'); return; }
        try { const r = await api('POST', '/api/v1/admin/users/' + encodeURIComponent(name) + '/credit', { micros, memo: memo.value.trim() }); toast('已调整，可用余额 ' + credit(r.available_micros)); await load(); }
        catch (ex) { fail(ex); }
      }
    }
    await load();
  }

  async function ledgerOf(path) {
    const list = ST.listView({ path, key: 'entries', empty: '没有账单记录', columns: [
      { title: '时间', cell: e => time(e.at) }, { title: '摘要', cell: e => e.memo }, { title: '作业', cell: e => e.job_id ? peer(e.job_id) : '' },
      { title: '金额', num: true, cell: e => h('span', { class: e.amount_micros > 0 ? 'ok' : 'bad' }, signed(e.amount_micros)) },
    ] });
    await list.reload();
    return list.el;
  }
  async function jobsOf(path) {
    const list = ST.listView({ path, key: 'jobs', empty: '没有作业', columns: [
      { title: '时间', cell: j => time(j.created_at) }, { title: '角色', cell: j => badge(j.role === 'buy' ? '买入' : '卖出', j.role === 'buy' ? 'accent' : 'ok') },
      { title: '模型', cell: j => j.model }, { title: '状态', cell: j => status(j.status) }, { title: '作业', cell: j => peer(j.job_id) },
      { title: 'tokens', num: true, cell: j => num(j.prompt_tokens) + ' / ' + num(j.completion_tokens) },
      { title: '费用', num: true, cell: j => credit(j.cost_micros) }, { title: '手续费', num: true, cell: j => credit(j.fee_micros) },
    ] });
    await list.reload();
    return list.el;
  }

  // ---- versions ----
  async function versions(m) {
    const r = await api('GET', '/api/v1/admin/versions');
    const same = v => !v || v === r.server.version ? 'ok' : 'warn';
    const service = (name, v) => ({ name, v });
    add(m, pageTitle('版本', h('small', null, '各模块实际运行的版本，以及客户端版本策略')),
      h('div', { class: 'card' }, section('server', '服务'), table([
        { title: '模块', cell: x => x.name },
        { title: '版本', cell: x => x.v ? badge(x.v, same(x.v)) : badge('未上报', 'warn') },
      ], [service('门户', r.server.version), service('协调器', r.coordinator), service('邮件服务', r.mail)].concat(r.relays.map(x => service('中继 ' + (x.name || x.id), x.version))), ''),
      h('p', { class: 'muted' }, 'API v' + r.api + ' · 控制协议 ' + r.protocols.ctrl + ' · 推理协议 ' + r.protocols.infer + ' · 数据库 core ' + r.schema.core + ' / mail ' + r.schema.mail + ' / portal ' + r.schema.portal)),
      h('div', { class: 'card' }, section('chart', '客户端分布'),
        h('div', { class: 'tiles' }, [['Windows', 'windows', 'desktop'], ['Linux', 'linux', 'server'], ['macOS', 'macos', 'desktop'], ['未知', 'other', 'cube']].map(([label, k, ic]) =>
          tile(label, num(r.devices.reduce((n, x) => n + x[k], 0)), '设备（共 ' + num(r.devices.reduce((n, x) => n + x.devices, 0)) + '）', ic, k === 'other' ? '' : 'blue'))),
        table([
          { title: '版本', cell: x => x.version || '未上报' },
          { title: '设备数', num: true, cell: x => String(x.devices) },
          { title: 'Windows', num: true, cell: x => num(x.windows) },
          { title: 'Linux', num: true, cell: x => num(x.linux) },
          { title: 'macOS', num: true, cell: x => num(x.macos) },
          { title: '未知', num: true, cell: x => num(x.other) },
        ], r.devices, '还没有设备'),
        h('p', { class: 'muted' }, '系统由客户端随请求上报；旧版本客户端和只靠协调器上报的设备显示为“未知”，升级并联系门户后补齐。')));
    const min = input({ type: 'text', value: r.policy.min_client || '', placeholder: '1.0.0', maxlength: 32 });
    const rec = input({ type: 'text', value: r.policy.recommended_client || '', placeholder: '1.0.0', maxlength: 32 });
    const latest = input({ type: 'text', value: r.policy.latest_client || '', placeholder: '1.0.0', maxlength: 32 });
    const url = input({ type: 'text', value: r.policy.download_url || '', placeholder: 'https://sharetoken.app/#download', maxlength: 256 });
    const dev = input({ type: 'checkbox' });
    dev.checked = !!r.policy.allow_dev;
    add(m, h('div', { class: 'card' }, section('cog', '客户端版本策略'),
      h('p', { class: 'muted' }, '低于最低版本的客户端不能登录、绑定设备、发布报价或买入；已有作业的结算和争议不受影响。'),
      h('p', { class: 'muted', id: 'release-info' }, r.release
        ? '下载目录里的发布：' + r.release.version + (r.release.commit ? '（' + r.release.commit + '，构建于 ' + (r.release.build_time || '未知') + '）' : '') + '。最新版本留空时用它；客户端还会按提交和构建时间提示同版本号的新构建。'
        : '下载目录里还没有发布（RELEASE-<版本>.json，由 publish-packages.sh 生成）。'),
      field('最低版本', min), field('推荐版本', rec), field('最新版本', latest), field('下载地址', url),
      h('label', { class: 'check' }, dev, '允许开发构建'),
      h('button', { class: 'primary', onclick: async () => {
        try {
          await api('PUT', '/api/v1/admin/version-policy', { min_client: min.value.trim(), recommended_client: rec.value.trim(), latest_client: latest.value.trim(), download_url: url.value.trim(), allow_dev: dev.checked });
          toast('已保存');
        } catch (ex) { fail(ex); }
      } }, '保存')));
  }

  // ---- relays ----
  async function relays(m) {
    const list = h('div', { class: 'card' }), clients = h('div', { class: 'card', id: 'relay-clients' });
    const charts = h('div'), legend = h('div', { class: 'dash-legend' });
    let range = '24h', hs = null;
    // Clicking a relay in the legend picks it; with any picked, only those are drawn.
    const picked = new Set();
    const rangeBtns = ['24h', '3d', '7d'].map(k => h('button', { class: k === range ? 'active' : '', onclick: () => { range = k; rangeBtns.forEach(b => b.classList.toggle('active', b.dataset.range === k)); loadCharts().catch(fail); } }, { '24h': '1 天', '3d': '3 天', '7d': '7 天' }[k]));
    rangeBtns.forEach((b, i) => b.dataset.range = ['24h', '3d', '7d'][i]);
    add(m, pageTitle('中继', h('small', null, '协调器每 15 秒同步一次登记表，并回写是否可达')), h('div', { class: 'toolbar' }, h('button', { class: 'primary', onclick: addRelay }, '登记中继')),
      h('div', { class: 'card' }, section('chart', '资源监控', h('span', { class: 'tabs tabs-sm' }, rangeBtns)), legend, charts), clients, list);
    async function loadCharts() {
      hs = await api('GET', '/api/v1/admin/relay-history?range=' + range);
      drawCharts();
    }
    function drawCharts() {
      const to = hs.now, from = hs.from, all = hs.series.map((s, i) => ({ s, color: SERIES_COLORS[i % SERIES_COLORS.length] }));
      for (const r of [...picked]) if (!all.some(x => x.s.relay === r)) picked.delete(r);
      const shown = x => !picked.size || picked.has(x.s.relay);
      add(clear(legend), all.map(x => h('button', { type: 'button', class: 'dash-key' + (shown(x) ? '' : ' off'), title: picked.has(x.s.relay) ? '再点一次取消' : '只看这台（可多选）',
        onclick: () => { picked.has(x.s.relay) ? picked.delete(x.s.relay) : picked.add(x.s.relay); drawCharts(); } }, h('i', { style: 'background:' + x.color }), x.s.relay)),
        picked.size ? h('button', { type: 'button', class: 'dash-key dash-key-all', onclick: () => { picked.clear(); drawCharts(); } }, '显示全部') : null);
      const ser = all.filter(shown);
      const mk = (pick, fmt, opts) => ser.map(x => ({ name: x.s.relay, color: x.color, points: x.s.points.map(p => pick(p)).filter(Boolean) }));
      const pct = v => v.toFixed(v < 10 ? 1 : 0) + '%', rate = v => bytes(v) + '/s';
      const cpu = mk(p => p.cpu_percent >= 0 ? { at: p.at, v: p.cpu_percent } : null);
      const mem = mk(p => ({ at: p.at, v: p.rss_bytes }));
      const net = mk(p => p.rx_bps >= 0 && p.tx_bps >= 0 ? { at: p.at, v: p.rx_bps + p.tx_bps, tip: '↓' + rate(p.rx_bps) + ' ↑' + rate(p.tx_bps) } : null);
      if (!all.length) { add(clear(charts), h('p', { class: 'muted' }, '还没有历史数据：需要在门户配置中继指标采集，采集成功后每分钟记一个点。')); return; }
      add(clear(charts), h('div', { class: 'dash-grid' },
        h('div', null, h('h3', { class: 'dash-sub' }, '网络（收 + 发）'), lineChart(net, from, to, hs.bucket_ns, rate)),
        h('div', null, h('h3', { class: 'dash-sub' }, '内存（RSS）'), lineChart(mem, from, to, hs.bucket_ns, bytes)),
        h('div', null, h('h3', { class: 'dash-sub' }, 'CPU'), lineChart(cpu, from, to, hs.bucket_ns, pct, { min: 5 }))));
    }
    const chartTimer = setInterval(() => {
      if (!main.contains(charts)) { clearInterval(chartTimer); return; }
      if (!document.hidden) loadCharts().catch(() => {});
    }, 60000);
    loadCharts().catch(fail);
    async function load() {
      const r = await api('GET', '/api/v1/admin/relays');
      renderClients(r.relays);
      add(clear(list), table([
        { title: 'ID', num: true, cell: x => String(x.id) },
        { title: '名称 / 地区', cell: x => [x.name || '—', ' ', h('span', { class: 'muted' }, x.region || '')] },
        { title: '地址', cell: x => h('div', null, x.addr.split(',').map(a => h('div', null, h('code', { class: 'mono' }, a)))) },
        { title: '状态', cell: x => [badge(x.enabled ? '启用' : '停用', x.enabled ? 'ok' : ''), ' ', badge(x.coordinator_ok ? '协调器可达' : '不可达', x.coordinator_ok ? 'ok' : 'bad')] },
        { title: '版本', cell: x => x.version || '—' },
        { title: '延迟', num: true, cell: x => ST.rtt(x.rtt_ms) },
        { title: '检查', cell: x => [ago(x.checked_at), x.detail ? h('div', { class: 'muted' }, x.detail) : null] },
        { title: '', cell: x => h('span', { class: 'row' },
          h('button', { class: 'small', onclick: async () => {
            const nm = input({ type: 'text', value: x.name || '', maxlength: 64 }), rg = input({ type: 'text', value: x.region || '', maxlength: 64 });
            if (!await modal({ title: '编辑中继 #' + x.id, body: h('div', { class: 'form' }, field('名称', nm), field('地区', rg)) })) return;
            try { await api('PATCH', '/api/v1/admin/relays/' + x.id, { name: nm.value, region: rg.value }); toast('已保存'); await load(); } catch (ex) { fail(ex); }
          } }, '编辑'),
          h('button', { class: 'small', onclick: act('/api/v1/admin/relays/' + x.id + (x.enabled ? '/disable' : '/enable'), undefined, x.enabled ? '已停用' : '已启用', load) }, x.enabled ? '停用' : '启用'),
          h('button', { class: 'small danger', onclick: async () => {
            if (!await confirm('删除中继', '删除 #' + x.id + ' 后用户将不再拿到它；协调器会放弃其预约。', '删除')) return;
            try { await api('DELETE', '/api/v1/admin/relays/' + x.id); toast('已删除'); await load(); } catch (ex) { fail(ex); }
          } }, '删除')) },
      ], r.relays, '还没有登记中继'));
    }
    // Who is online, as the relays see it: devices holding a reservation by the
    // build their portal-signed pass names, and every connected peer by the
    // agent it announced in libp2p identify.
    function renderClients(rs) {
      const OS = [['Windows', 'windows'], ['Linux', 'linux'], ['macOS', 'darwin']];
      const osOf = c => OS.some(o => o[1] === c.os) ? c.os : 'other';
      const live = rs.filter(x => x.metrics && x.metrics.ok_at);
      const rows = [], total = { windows: 0, linux: 0, darwin: 0, other: 0, all: 0, mismatch: 0 };
      for (const x of live) for (const n of x.metrics.networks || []) {
        const cl = n.clients || [];
        if (!cl.length && !n.client_mismatches) continue;
        const row = { relay: x.name || '#' + x.id, network: n.network, windows: 0, linux: 0, darwin: 0, other: 0, all: 0, mismatch: n.client_mismatches || 0, builds: cl };
        for (const c of cl) { row[osOf(c)] += c.count; row.all += c.count; }
        for (const k of ['windows', 'linux', 'darwin', 'other', 'all', 'mismatch']) total[k] += row[k];
        rows.push(row);
      }
      const build = c => (c.version || '未知') + (c.os ? ' ' + c.os + '/' + (c.arch || '?') : '');
      const agentRows = live.map(x => {
        const a = x.metrics.agents || [], by = p => a.filter(c => c.program === p).reduce((n, c) => n + c.count, 0);
        return { relay: x.name || '#' + x.id, client: by('client'), coordinator: by('coordinator'), relay_: by('relay'), other: a.filter(c => !['client', 'coordinator', 'relay'].includes(c.program)).reduce((n, c) => n + c.count, 0),
          builds: a.filter(c => c.program === 'client') };
      });
      add(clear(clients), section('desktop', '在线客户端', h('small', null, '中继每次被采集时统计；系统和版本取自门户签名的通行证')),
        !live.length ? h('p', { class: 'muted' }, '没有可用的中继指标：需要在门户配置中继指标采集。') : [
          h('div', { class: 'tiles' }, OS.map(([label, k]) => tile(label, num(total[k]), '预约中的设备', 'desktop', 'blue')),
            tile('未知系统', num(total.other), '通行证里没有系统信息', 'cube'),
            tile('自报不一致', num(total.mismatch), 'libp2p 自报的版本或平台和通行证不同', 'shield', '', total.mismatch ? 'warn' : 'ok')),
          table([
            { title: '中继', cell: x => x.relay }, { title: '网络', cell: x => x.network },
            { title: '设备', num: true, cell: x => num(x.all) },
            ...OS.map(([label, k]) => ({ title: label, num: true, cell: x => num(x[k]) })),
            { title: '未知', num: true, cell: x => num(x.other) },
            { title: '版本', cell: x => h('span', { class: 'row' }, x.builds.slice(0, 6).map(c => badge(build(c) + ' ×' + c.count, ''))) },
            { title: '不一致', num: true, cell: x => x.mismatch ? badge(String(x.mismatch), 'warn') : '0' },
          ], rows, '没有设备在中继上预约'),
          h('h3', { class: 'dash-sub' }, '已连接的程序（libp2p 自报，未签名）'),
          table([
            { title: '中继', cell: x => x.relay },
            { title: '客户端', num: true, cell: x => num(x.client) }, { title: '协调器', num: true, cell: x => num(x.coordinator) },
            { title: '中继', num: true, cell: x => num(x.relay_) }, { title: '其他', num: true, cell: x => num(x.other) },
            { title: '客户端版本', cell: x => h('span', { class: 'row' }, x.builds.slice(0, 6).map(c => badge(build(c) + ' ×' + c.count, ''))) },
          ], agentRows, ''),
          h('p', { class: 'muted' }, '「设备」只算持有预约的客户端（卖家和接单设备）；只买不卖、没预约的客户端出现在「已连接的程序」里。「不一致」多半是旧客户端，或者被改过的客户端。')]);
    }
    async function addRelay() {
      const addr = h('textarea', { rows: 3, placeholder: '/ip4/203.0.113.7/tcp/8091/p2p/12D3Koo…,/ip6/2001:db8::7/tcp/8091/p2p/12D3Koo…' });
      const nm = input({ type: 'text', maxlength: 64, placeholder: '例如 relay-sh' }), rg = input({ type: 'text', maxlength: 64, placeholder: '例如 cn-east' });
      if (!await modal({ title: '登记中继', okText: '登记', body: h('div', { class: 'form' }, field('多地址（中继启动时打印；同一 peer 的多个地址用逗号连接）', addr), h('div', { class: 'row' }, field('名称', nm), field('地区', rg))) })) return;
      try { await api('POST', '/api/v1/admin/relays', { addr: addr.value.trim(), name: nm.value.trim(), region: rg.value.trim() }); toast('已登记'); await load(); } catch (ex) { fail(ex); }
    }
    await load();
  }

  // ---- jobs ----
  async function jobs(m) {
    const st = h('select', { id: 'jobs-status', onchange: () => list.reload() }, h('option', { value: '' }, '全部状态'), Object.entries(ST.STATUS).map(([k, v]) => h('option', { value: k }, v[0])));
    const user = input({ type: 'search', id: 'jobs-user', placeholder: '按用户名筛选' });
    const list = ST.listView({ path: '/api/v1/admin/jobs', key: 'jobs', empty: '没有作业', params: () => ({ status: st.value, user: user.value.trim() }), tools: [st, user],
      onError: ex => ex.code === 'not_found' ? '没有这个用户' : ex.message,
      columns: [
        { title: '时间', cell: j => time(j.created_at) }, { title: '作业', cell: j => peer(j.job_id) }, { title: '模型', cell: j => j.model },
        { title: '状态', cell: j => [status(j.status), j.dispute_outcome ? h('div', { class: 'muted' }, '裁决 ' + j.dispute_outcome) : null] },
        { title: '买家', cell: j => [peer(j.buyer), h('div', { class: 'muted mono' }, j.buyer_acct)] },
        { title: '节点', cell: j => [peer(j.node), h('div', { class: 'muted mono' }, j.node_acct)] },
        { title: 'tokens', num: true, cell: j => num(j.prompt_tokens) + ' / ' + num(j.completion_tokens) },
        { title: '费用', num: true, cell: j => credit(j.cost_micros) }, { title: '手续费', num: true, cell: j => credit(j.fee_micros) },
        { title: '完成', cell: j => time(j.done_at) },
      ] });
    add(m, pageTitle('作业', h('small', null, '协调器保留期内的作业（默认 30 天）')), list.el);
    let timer; user.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => list.reload(), 250); });
    await list.reload();
  }

  // ---- nodes / audits / disputes ----
  async function nodes(m) {
    const r = await api('GET', '/api/v1/admin/nodes');
    const now = Date.now() * 1e6;
    add(m, pageTitle('节点', h('small', null, '卖家设备的信誉记录；连续两次审计不符会被暂停撮合')), h('div', { class: 'card' }, table([
      { title: '节点', cell: n => [peer(n.node), n.user ? h('div', null, userLink(n.user)) : h('div', { class: 'muted' }, '未绑定')] },
      { title: '版本', cell: n => n.client_version || '—' },
      { title: '信誉', num: true, cell: n => String(n.reputation) },
      { title: '成功 / 失败', num: true, cell: n => n.successes + ' / ' + n.failures },
      { title: '争议败诉', num: true, cell: n => String(n.disputes_lost) },
      { title: '审计 通过 / 不符', num: true, cell: n => n.audits_passed + ' / ' + n.audits_failed + (n.audit_fail_streak ? '（连续 ' + n.audit_fail_streak + '）' : '') },
      { title: '状态', cell: n => n.suspended_until > now ? badge('暂停至 ' + time(n.suspended_until), 'bad') : badge('正常', 'ok') },
    ], r.nodes, '还没有节点记录')));
  }
  async function audits(m) {
    const V = { match: ['一致', 'ok'], mismatch: ['不符', 'bad'], inconclusive: ['无法判定', 'warn'] };
    const list = ST.listView({ path: '/api/v1/admin/audits', key: 'audits', empty: '还没有审计记录', columns: [
      { title: '时间', cell: a => time(a.at) }, { title: '节点', cell: a => peer(a.node) }, { title: '模型', cell: a => a.model },
      { title: '结论', cell: a => badge((V[a.verdict] || [a.verdict])[0], (V[a.verdict] || [])[1]) },
      { title: '相似度', num: true, cell: a => a.similarity.toFixed(2) }, { title: '详情', cell: a => a.detail }, { title: '作业', cell: a => peer(a.job_id) },
    ] });
    add(m, pageTitle('推理审计', h('small', null, '平台抽检：用参考模型复算节点的回答。管理员的操作记录在「操作日志」')), list.el);
    await list.reload();
  }
  async function disputes(m) {
    const list = ST.listView({ path: '/api/v1/admin/disputes', key: 'disputes', empty: '没有争议', columns: [
      { title: '时间', cell: d => time(d.created_at) }, { title: '作业', cell: d => peer(d.job_id) }, { title: '模型', cell: d => d.model },
      { title: '状态', cell: d => status(d.status) },
      { title: '买家 / 节点', cell: d => [peer(d.buyer), ' → ', peer(d.node)] },
      { title: '金额', num: true, cell: d => credit(d.cost) },
      { title: '原因', cell: d => [d.reason, d.outcome ? h('div', { class: 'muted' }, '裁决：' + d.outcome + (d.detail ? '，' + d.detail : '')) : null] },
      { title: '', cell: d => d.status !== 'disputed' ? '' : h('span', { class: 'row' },
        h('button', { class: 'small', onclick: async () => {
          if (!await confirm('判给节点', '作业 ' + ST.short(d.job_id, 8) + '：按原价 ' + credit(d.cost) + ' 结算给节点。', '付款给节点', false)) return;
          try { await api('POST', '/api/v1/admin/disputes/' + encodeURIComponent(d.job_id) + '/resolve', { decision: 'pay' }); toast('已结算'); await list.refresh(); } catch (ex) { fail(ex); }
        } }, '判给节点'),
        h('button', { class: 'small danger', onclick: async () => {
          if (!await confirm('判给买家', '作业 ' + ST.short(d.job_id, 8) + '：全额退款给买家，节点记一次争议败诉。', '退款')) return;
          try { await api('POST', '/api/v1/admin/disputes/' + encodeURIComponent(d.job_id) + '/resolve', { decision: 'refund' }); toast('已退款'); await list.refresh(); } catch (ex) { fail(ex); }
        } }, '退款给买家')) },
    ] });
    add(m, pageTitle('争议', h('small', null, '证据无法自动裁决的单需要人工裁决；判给节点即结算，判给买家即退款并扣节点信誉')), list.el);
    await list.reload();
  }

  // ---- ledger ----
  async function ledger(m) {
    const account = input({ type: 'search', id: 'ledger-account', placeholder: '按账户筛选，如 user:3、platform:fees' });
    const list = ST.listView({ path: '/api/v1/admin/ledger', key: 'entries', empty: '没有流水', params: () => ({ account: account.value.trim() }), tools: account, columns: [
      { title: '#', num: true, cell: e => String(e.seq) }, { title: '时间', cell: e => time(e.at) },
      { title: '从', cell: e => acct(e.from, e.from_user) }, { title: '到', cell: e => acct(e.to, e.to_user) },
      { title: '金额', num: true, cell: e => credit(e.amount) }, { title: '摘要', cell: e => e.memo }, { title: '作业', cell: e => e.job_id ? peer(e.job_id) : '' },
    ] });
    add(m, pageTitle('账本', h('small', null, '复式流水：每笔从一个账户转到另一个账户')), list.el);
    let timer; account.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => list.reload(), 250); });
    await list.reload();
  }

  // ---- mail ----
  // Mail tables keep Unix seconds; ST.time and ST.ago take nanoseconds.
  const secTime = s => time(s * 1e9), secAgo = s => ago(s * 1e9);
  const OUT_STATUS = { queued: ['排队中', 'accent'], sending: ['发送中', 'accent'], sent: ['对方已接受', 'ok'], failed: ['失败', 'bad'], cancelled: ['已取消', ''] };
  const OUT_KIND = { verify: '验证码', notice: '通知', reply: '回复', compose: '手写', test: '测试' };
  const size = n => n < 1024 ? n + ' B' : n < 1 << 20 ? (n / 1024).toFixed(1) + ' KB' : n < 1 << 30 ? (n / (1 << 20)).toFixed(1) + ' MB' : (n / (1 << 30)).toFixed(2) + ' GB';
  function setUnread(n) { mailBadge.hidden = !n; mailBadge.textContent = n ? String(n) : ''; }
  async function refreshUnread() { try { setUnread((await api('GET', '/api/v1/admin/mail/status')).overview.unread); } catch (e) { /* the page itself reports errors */ } }

  // Mail is written by strangers: bytes from it are only ever saved to a file,
  // and its text only goes through textContent (ST.h).
  async function saveFile(path, name) {
    try { await ST.download(path, name); } catch (ex) { fail(ex); }
  }

  async function mail(m, rest) {
    const sub = rest[0] || 'inbox';
    const tab = (key, label) => h('a', { href: '#/mail/' + key, class: 'tab-link' + (sub === key ? ' active' : '') }, label);
    add(m, pageTitle('邮件'),
      h('div', { class: 'toolbar' }, h('div', { class: 'tab-links' }, tab('inbox', '收件箱'), tab('archived', '已归档'), tab('bounces', '退信'), tab('outbox', '发件箱'), tab('status', '服务状态')),
        h('span', { class: 'spacer' }), h('button', { class: 'primary', id: 'mail-compose', onclick: () => composeMail() }, '写邮件')));
    const body = h('div'); add(m, body);
    if (sub === 'outbox') { await (rest[1] ? outMessage(body, rest[1]) : outbox(body)); return; }
    if (sub === 'status') { await mailStatus(body); return; }
    await (rest[1] ? inMessage(body, rest[1], sub) : inbox(body, sub));
    refreshUnread();
  }

  async function composeMail(to, subject) {
    const toI = input({ type: 'email', id: 'mail-to', value: to || '', placeholder: 'user@example.com' });
    const subj = input({ type: 'text', id: 'mail-subject', value: subject || '', maxlength: 200 });
    const text = h('textarea', { id: 'mail-text', rows: 12, class: 'wide' });
    const from = h('select', { id: 'mail-from' }, h('option', { value: 'support' }, '客服地址（可回复）'), h('option', { value: 'team' }, '团队地址'));
    if (!await modal({ title: '写邮件', okText: '发送', body: h('div', { class: 'form' }, field('收件人', toI), field('发件人', from), field('主题', subj), field('正文', text)) })) return;
    try {
      const r = await api('POST', '/api/v1/admin/mail/send', { to: toI.value.trim(), subject: subj.value, text: text.value, from: from.value });
      toast('已加入发送队列');
      location.hash = '#/mail/outbox/' + r.outbox_id;
    } catch (ex) { fail(ex); }
  }

  async function inbox(root, sub) {
    const q = input({ type: 'search', id: 'mail-search', placeholder: '搜索发件人、主题' });
    const unread = h('input', { type: 'checkbox', id: 'mail-unread-only' });
    const markRead = h('button', { type: 'button', class: 'small', id: 'mail-mark-read', disabled: true, onclick: () => mark(true) }, '标为已读');
    const markUnread = h('button', { type: 'button', class: 'small', id: 'mail-mark-unread', disabled: true, onclick: () => mark(false) }, '标为未读');
    const picked = h('span', { class: 'strong', id: 'mail-picked' });
    const bulk = h('div', { class: 'bulk-bar', hidden: true }, picked, markRead, markUnread);
    async function mark(read) {
      const ids = list.selected();
      if (!ids.length) return;
      markRead.disabled = markUnread.disabled = true;
      try { const r = await api('POST', '/api/v1/admin/mail/inbox/read', { ids, read }); toast((read ? '已标为已读 ' : '已标为未读 ') + r.updated + ' 封'); }
      catch (ex) { fail(ex); }
      await list.refresh(); refreshUnread();
    }
    const list = ST.listView({ path: '/api/v1/admin/mail/inbox', key: 'messages', empty: sub === 'bounces' ? '没有退信' : '没有邮件',
      select: { key: x => x.id, label: '选择这封邮件', allLabel: '全选本页', onChange: ids => { markRead.disabled = markUnread.disabled = !ids.length; bulk.hidden = !ids.length; picked.textContent = '已选 ' + ids.length + ' 封'; } },
      bulk,
      params: () => Object.assign({ q: q.value.trim(), unread: unread.checked ? '1' : '' }, sub === 'archived' ? { status: 'archived' } : sub === 'bounces' ? { status: 'all', bounces: 'only' } : {}),
      tools: [q, sub === 'inbox' ? h('label', { class: 'check' }, unread, '只看未读') : null],
      columns: [
        { title: '发件人', cell: x => h('span', { class: 'muted', title: x.from || x.mail_from || '' }, mailAddr(x.from) || x.mail_from || '（未知）') },
        { title: '主题', cell: x => h('a', { href: '#/mail/' + sub + '/' + x.id, class: x.read_at ? '' : 'strong' }, x.subject || '（无主题）') },
        { title: '收件地址', cell: x => h('span', { class: 'muted' }, x.mailbox) },
        { title: '发件人验证', cell: x => authBadge(x) },
        { title: '状态', cell: x => x.replied_at ? badge('已回复', 'ok') : x.read_at ? '' : badge('未读', 'accent') },
        { title: '大小', num: true, cell: x => size(x.size) },
        { title: '收到', cell: x => secTime(x.received_at) },
      ] });
    add(root, list.el);
    let timer; q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => list.reload(), 250); });
    unread.addEventListener('change', () => list.reload());
    await list.reload();
  }

  // mailAddr is the bare address of a From header: 'Name <a@b>' → 'a@b'.
  const mailAddr = from => { if (!from) return ''; const m = /<([^<>\s]+@[^<>\s]+)>\s*$/.exec(from); return m ? m[1] : from.trim(); };
  const authBadge = x => x.is_bounce ? badge('退信', 'warn') : x.verified ? badge('已验证', 'ok') : badge('未验证', 'warn');

  async function inMessage(root, id, sub) {
    const base = '/api/v1/admin/mail/inbox/' + encodeURIComponent(id);
    const d = await api('GET', base);
    const x = d.message, p = d.parsed;
    if (!x.read_at) await api('POST', base + '/read', { read: true }).catch(() => {});
    const back = () => { location.hash = '#/mail/' + sub; };
    const archived = x.status === 'archived';
    const actions = h('div', { class: 'row' },
      h('button', { class: 'primary', id: 'mail-reply', disabled: !d.reply_to || x.is_bounce, onclick: reply }, '回复'),
      h('button', { id: 'mail-archive', onclick: async () => {
        try { await api('POST', base + '/status', { status: archived ? 'inbox' : 'archived' }); toast(archived ? '已移回收件箱' : '已归档'); back(); } catch (ex) { fail(ex); }
      } }, archived ? '移回收件箱' : '归档'),
      h('button', { onclick: async () => { try { await api('POST', base + '/read', { read: false }); toast('已标为未读'); back(); } catch (ex) { fail(ex); } } }, '标为未读'),
      h('button', { onclick: () => saveFile(base + '/raw', 'message-' + x.id + '.eml') }, '下载原文'),
      h('button', { class: 'danger', onclick: async () => {
        if (!await confirm('删除邮件', '永久删除这封邮件，无法恢复。', '删除')) return;
        try { await api('DELETE', base); toast('已删除'); back(); } catch (ex) { fail(ex); }
      } }, '删除'));
    add(root, h('p', null, h('a', { href: '#/mail/' + sub }, '← 返回')),
      x.verified || x.is_bounce ? null : h('div', { class: 'card warn-card', id: 'mail-unverified' }, '这封邮件的发件人没有通过 SPF / DKIM 验证，发件人地址可能是伪造的。不要轻信其中的身份声明，也不要按邮件内容直接操作账户或打开附件。'),
      h('div', { class: 'card' }, h('h2', { class: 'mail-subject' }, p.subject || x.subject || '（无主题）'),
        kv([['发件人', [p.from || x.from || '（未知）', ' ', authBadge(x)]], ['回复到', d.reply_to || '（无可用地址）'], ['收件地址', x.mailbox], ['收到', secTime(x.received_at)],
          ['来源', x.remote_ip + '（' + x.helo + '），' + (x.tls ? 'TLS' : '明文') + '，SPF ' + x.spf + '，DKIM ' + x.dkim], ['信封发件人', x.mail_from || '（空）']]),
        actions),
      h('div', { class: 'card' }, p.from_html ? h('p', { class: 'muted' }, '这封邮件只有 HTML 版本，这里显示其中的文字；HTML 不会被加载。') : null,
        p.malformed ? h('p', { class: 'warn' }, '邮件结构有问题，内容可能不完整，可以下载原文查看。') : null,
        h('div', { class: 'mail-body', id: 'mail-body' }, p.text || '（没有文字内容）')),
      p.attachments.length ? h('div', { class: 'card' }, h('h3', null, '附件', h('small', null, '来自外部，打开前请确认来源')), table([
        { title: '文件', cell: a => a.filename || '（无文件名）' }, { title: '声明的类型', cell: a => h('span', { class: 'muted' }, a.type) }, { title: '大小', num: true, cell: a => size(a.size) },
        { title: '', cell: a => h('button', { class: 'small', onclick: () => saveFile(base + '/attachments/' + a.index, a.filename || 'attachment-' + a.index) }, '下载') },
      ], p.attachments)) : null);
    async function reply() {
      const quoted = '\n\n在 ' + secTime(x.received_at) + '，' + (p.from || x.from) + ' 写道：\n' + (p.text || '').split('\n').slice(0, 40).map(l => '> ' + l).join('\n');
      const text = h('textarea', { id: 'reply-text', rows: 14, class: 'wide' });
      text.value = quoted;
      const body = h('div', { class: 'form' }, h('p', { class: 'muted' }, '发给 ', h('b', null, d.reply_to), '，从客服地址发出'), field('正文', text));
      if (!await modal({ title: '回复', okText: '发送', body })) return;
      try {
        const r = await api('POST', base + '/reply', { text: text.value });
        toast('回复已加入发送队列'); location.hash = '#/mail/outbox/' + r.outbox_id;
      } catch (ex) { fail(ex); }
    }
    refreshUnread();
  }

  async function outbox(root) {
    const q = input({ type: 'search', id: 'out-search', placeholder: '搜索收件人、主题' });
    const st = h('select', { id: 'out-status' }, h('option', { value: '' }, '全部状态'), Object.entries(OUT_STATUS).map(([k, v]) => h('option', { value: k }, v[0])));
    const kind = h('select', { id: 'out-kind' }, h('option', { value: '' }, '全部类型'), Object.entries(OUT_KIND).map(([k, v]) => h('option', { value: k }, v)));
    const list = ST.listView({ path: '/api/v1/admin/mail/outbox', key: 'messages', empty: '没有发出的邮件',
      params: () => ({ q: q.value.trim(), status: st.value, kind: kind.value }), tools: [q, st, kind],
      columns: [
        { title: '收件人', cell: x => x.to },
        { title: '主题', cell: x => h('a', { href: '#/mail/outbox/' + x.id }, x.sensitive ? '（含验证码，不显示）' : x.subject || '（无主题）') },
        { title: '类型', cell: x => OUT_KIND[x.kind] || x.kind },
        { title: '状态', cell: x => outBadge(x) },
        { title: '尝试', num: true, cell: x => String(x.attempts) },
        { title: '创建', cell: x => secTime(x.created_at) },
      ] });
    add(root, list.el);
    let timer; q.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => list.reload(), 250); });
    st.addEventListener('change', () => list.reload()); kind.addEventListener('change', () => list.reload());
    await list.reload();
  }

  const outBadge = x => { const s = OUT_STATUS[x.status] || [x.status, '']; return badge(s[0], s[1]); };

  async function outMessage(root, id) {
    const base = '/api/v1/admin/mail/outbox/' + encodeURIComponent(id);
    const box = h('div'); add(root, h('p', null, h('a', { href: '#/mail/outbox' }, '← 返回')), box);
    async function load() {
      const x = (await api('GET', base)).message;
      add(clear(box), h('div', { class: 'card' }, h('h2', { class: 'mail-subject' }, x.subject || '（无主题）'),
        kv([['状态', outBadge(x)], ['收件人', x.to], ['发件人', x.from], ['类型', OUT_KIND[x.kind] || x.kind], ['创建', secTime(x.created_at)],
          ['完成', x.finished_at ? secTime(x.finished_at) : '—'], ['尝试次数', String(x.attempts)],
          ['下次尝试', x.status === 'queued' && x.next_attempt_at ? secTime(x.next_attempt_at) : '—'], ['接收服务器', x.remote || '—'], ['Message-ID', x.message_id || '—'],
          ['操作人', x.created_by || '系统'], ['最后错误', x.last_error ? h('span', { class: 'bad', id: 'out-error' }, x.last_error) : '—']]),
        h('div', { class: 'row' },
          h('button', { id: 'out-retry', disabled: x.sensitive || !['failed', 'cancelled'].includes(x.status), onclick: act(base + '/retry', undefined, '已重新排队', load) }, '重新发送'),
          h('button', { id: 'out-cancel', disabled: x.status !== 'queued', onclick: act(base + '/cancel', undefined, '已取消', load) }, '取消发送'))),
        h('div', { class: 'card' }, h('div', { class: 'mail-body' }, x.text || '（无内容）')));
    }
    await load();
  }

  async function mailStatus(root) {
    const d = await api('GET', '/api/v1/admin/mail/status');
    const o = d.overview, srv = o.server, out = o.outbox || {};
    setUnread(o.unread);
    add(root, h('div', { class: 'tiles' },
      tile('邮件服务', o.online ? '运行中' : '离线', o.last_seen ? '上次报告 ' + secAgo(o.last_seen) : '从未报告', 'server', '', o.online ? 'ok' : 'bad'),
      tile('未读', num(o.unread), '收件箱 ' + num(o.inbox) + '，已归档 ' + num(o.archived), 'inbox', 'blue'),
      tile('待发送', num((out.queued || 0) + (out.sending || 0)), '', 'swap', '', (out.queued || 0) > 20 ? 'warn' : ''),
      tile('对方已接受', num(out.sent || 0), '不代表已进入收件箱；失败 ' + num(out.failed || 0), 'checkCircle', 'ok', out.failed ? 'warn' : ''),
      tile('退信', num(o.bounces), '', 'warning', '', o.bounces ? 'warn' : ''),
      tile('24 小时验证码', num(o.codes_last_day), null, 'key', 'violet'),
      tile('邮件占用', size(o.inbox_bytes), null, 'database')),
      h('div', { class: 'card' }, kv([
        ['发件地址', d.from || h('span', { class: 'bad' }, '门户未配置 mail.from')], ['客服地址', d.support || h('span', { class: 'warn' }, '未配置')],
        ['注册', d.registration_open ? '开放：没有账户的邮箱第一次登录时自动注册' : '关闭，仅管理员创建用户'],
        ['发信方式', srv ? srv.delivery : '—'], ['主机名', srv ? srv.hostname : '—'], ['DKIM 选择器', srv ? srv.dkim_selector : '—'],
        ['收信', srv && srv.smtp_listen ? srv.smtp_listen + '：' + (srv.mailboxes || []).join('、') : '未启用'],
        ['SMTP 计数', srv ? '连接 ' + srv.counters.connections + '，接收 ' + srv.counters.accepted + '，拒绝 ' + srv.counters.rejected : '—'],
        ['启动于', srv ? secTime(srv.started_at) : '—']])),
      srv && srv.dns ? [section('globe', 'DNS 检查', h('small', null, srv.dns_checked_at ? secAgo(srv.dns_checked_at) + '检查' : '')), h('div', { class: 'card' }, table([
        { title: '记录', cell: c => c.name }, { title: '结果', cell: c => c.ok ? badge('正常', 'ok') : badge('异常', 'bad') }, { title: '详情', cell: c => h('span', { class: 'mono wrap' }, c.detail) },
      ], srv.dns))] : null);
  }

  // ---- feedback ----
  function setFeedbackNew(n) { feedbackBadge.hidden = !n; feedbackBadge.textContent = n ? String(n) : ''; }
  const FEEDBACK_KIND = { bug: '问题', idea: '建议', other: '其他' };

  async function feedback(m) {
    const st = h('select', { id: 'feedback-status', onchange: () => list.reload() }, h('option', { value: 'new' }, '待处理'), h('option', { value: 'done' }, '已处理'), h('option', { value: '' }, '全部'));
    const origin = f => f.source === 'client' ? ['客户端', f.app_version, f.os].filter(Boolean).join(' · ') : '门户';
    const list = ST.listView({ path: '/api/v1/admin/feedback', key: 'feedback', empty: '没有反馈', params: () => ({ status: st.value }), tools: [h('span', { class: 'muted' }, '状态'), st],
      onData: r => setFeedbackNew(r.new),
      columns: [
        { title: '时间', cell: f => ST.time(f.created_at) },
        { title: '用户', cell: f => userLink(f.username) },
        { title: '类型', cell: f => FEEDBACK_KIND[f.category] || f.category },
        { title: '内容', cell: f => h('div', { class: 'feedback-text', dir: 'auto' }, f.message) },
        { title: '来源', cell: f => h('span', { class: 'muted' }, [origin(f), f.lang].filter(Boolean).join(' · ')) },
        { title: '状态', cell: f => f.status === 'done' ? [badge('已处理', 'ok'), h('div', { class: 'muted' }, f.handled_by)] : badge('待处理', 'warn') },
        { title: '', cell: f => h('span', { class: 'row' },
          f.username.includes('@') ? h('button', { class: 'small', onclick: () => composeMail(f.username, 'Your ShareToken feedback') }, '回复') : null,
          h('button', { class: 'small', onclick: async () => {
            try { await api('POST', '/api/v1/admin/feedback/' + f.id + '/status', { status: f.status === 'done' ? 'new' : 'done' }); await list.refresh(); } catch (ex) { fail(ex); }
          } }, f.status === 'done' ? '重新打开' : '标记已处理')) },
      ] });
    add(m, pageTitle('反馈', h('small', null, '用户从门户和客户端提交的内容')), list.el);
    await list.reload();
  }
  api('GET', '/api/v1/admin/feedback?limit=1&status=new').then(r => setFeedbackNew(r.new)).catch(() => { /* the page itself reports errors */ });

  // ---- pairings ----
  // Who buys only from whom: every pairing, with both account names in full.
  async function pairings(m) {
    const user = input({ type: 'search', id: 'pairings-user', placeholder: '买家或卖家的账户名' });
    let timer;
    user.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => list.reload(), 300); });
    const list = ST.listView({ path: '/api/v1/admin/pairings', key: 'pairings', empty: '没有配对关系', params: () => ({ user: user.value.trim() }), tools: [user],
      columns: [
        { title: '配对时间', cell: p => ST.time(p.since) },
        { title: '买家', cell: p => userLink(p.buyer) },
        { title: '卖家', cell: p => [userLink(p.seller), p.seller_private ? [' ', badge('私有', 'warn')] : null] },
        { title: '配对码备注', cell: p => p.label || h('span', { class: 'muted' }, '—') },
      ] });
    add(m, pageTitle('配对', h('small', null, '买家只向和它配对的卖家购买；标记「私有」的卖家只为配对的买家服务')), list.el);
    await list.reload();
  }

  // ---- share partners ----
  // Who uses whose nodes for free: every share group, members in full.
  async function partners(m) {
    const user = input({ type: 'search', id: 'partners-user', placeholder: '成员的账户名' });
    let timer;
    user.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => list.reload(), 300); });
    const list = ST.listView({ path: '/api/v1/admin/share-groups', key: 'groups', empty: '没有共享伙伴', params: () => ({ user: user.value.trim() }), tools: [user],
      columns: [
        { title: '配对时间', cell: g => ST.time(g.created_at) },
        { title: '成员', cell: g => g.members.map((name, i) => [i ? '、' : null, userLink(name)]) },
      ] });
    add(m, pageTitle('共享伙伴', h('small', null, '组内成员优先使用彼此的节点，不扣费；每人可限制给出的 tokens 和并发比例')), list.el);
    await list.reload();
  }

  // ---- audit log ----
  const AUDIT_ACTIONS = {
    'auth.admin_login': '管理员登录', 'user.create': '创建用户', 'user.credit': '调整 credit', 'user.disable': '禁用用户', 'user.enable': '启用用户', 'user.set_admin': '变更管理员权限',
    'user.rename': '修改用户邮箱', 'user.login_code': '生成登录验证码', 'user.mfa_reset': '重置两步验证', 'user.token_revoke': '吊销 token', 'user.tokens_revoke_all': '吊销全部 token', 'user.device_unbind': '解绑设备',
    'relay.add': '登记中继', 'relay.update': '编辑中继', 'relay.enable': '启用中继', 'relay.disable': '停用中继', 'relay.remove': '删除中继', 'dispute.resolve': '裁决争议', 'version_policy.set': '修改版本策略',
    'mail.send': '写邮件', 'mail.reply': '回复邮件', 'mail.mark_read': '批量标记已读 / 未读', 'mail.delete': '删除邮件', 'mail.move': '归档 / 移回邮件', 'mail.retry': '重发邮件', 'mail.cancel': '取消发送', 'mail.test': '发送测试邮件',
    'feedback.status': '处理反馈', 'settings.update': '修改系统设置', 'settings.apply_blocklist': '禁用黑名单用户', 'invite.create': '创建邀请码', 'invite.disable': '停用邀请码',
    'export.users': '导出用户', 'export.jobs': '导出作业', 'export.journal': '导出账本', 'export.user-ledger': '导出用户账单', 'export.user-jobs': '导出用户作业', 'export.inference-audits': '导出推理审计', 'export.disputes': '导出争议',
    'export.mail-inbox': '导出收件箱', 'export.mail-outbox': '导出发件箱', 'export.feedback': '导出反馈', 'export.audit-log': '导出操作日志',
  };
  // Labels for the keys handlers note; anything else shows its raw key.
  const AUDIT_KEYS = { is_admin: '管理员', memo: '备注', micros: '金额（micro）', count: '数量', found: '找到', read: '已读', status: '状态', subject: '主题', to: '收件人',
    name: '名称', region: '地区', addr: '地址', note: '说明', decision: '裁决', disabled: '已禁用', dry_run: '仅预览', changed: '改动', max_uses: '可用次数',
    tokens_revoked: '吊销 token 数', min_client: '最低版本', recommended_client: '推荐版本', latest_client: '最新版本', allow_dev: '允许开发构建', rows: '行数', query: '筛选条件', id: 'ID', peer: '设备' };
  const AUDIT_REQ = ['method', 'path', 'error'];
  const auditVal = v => v === true ? '是' : v === false ? '否' : typeof v === 'object' && v !== null ? JSON.stringify(v) : String(v);
  const auditFields = e => Object.entries(e.detail || {}).filter(([k]) => !AUDIT_REQ.includes(k));
  const auditSummary = e => [e.detail && e.detail.error ? '原因：' + e.detail.error : null]
    .concat(auditFields(e).map(([k, v]) => (AUDIT_KEYS[k] || k) + '：' + auditVal(v))).filter(Boolean).join('；');
  // uaShort names the browser and system of a user agent, or the program for non-browsers.
  function uaShort(ua) {
    if (!ua) return '—';
    const os = /Windows/.test(ua) ? 'Windows' : /iPhone|iPad/.test(ua) ? 'iOS' : /Mac OS X|Macintosh/.test(ua) ? 'macOS' : /Android/.test(ua) ? 'Android' : /Linux/.test(ua) ? 'Linux' : '';
    let m = /Edg\/(\d+)/.exec(ua) || null, b = m ? 'Edge ' + m[1] : '';
    if (!b && (m = /Firefox\/(\d+)/.exec(ua))) b = 'Firefox ' + m[1];
    if (!b && (m = /Chrome\/(\d+)/.exec(ua))) b = 'Chrome ' + m[1];
    if (!b && /Safari\//.test(ua) && (m = /Version\/(\d+)/.exec(ua))) b = 'Safari ' + m[1];
    if (!b) b = ua.split(/[\s(]/)[0];
    return os ? b + ' · ' + os : b;
  }
  function auditDetail(e) {
    const d = e.detail || {};
    const ok = e.status < 400;
    const body = h('div', { class: 'audit-modal' }, kv([
      ['时间', new Date(e.at / 1e6).toLocaleString('zh-CN', { hour12: false })],
      ['操作人', e.actor === 'cli' ? '服务器命令行' : [e.actor, e.actor_id ? h('span', { class: 'muted' }, '（ID ' + e.actor_id + '）') : null]],
      ['操作', [AUDIT_ACTIONS[e.action] || e.action, ' ', h('code', { class: 'mono muted' }, e.action)]],
      ['对象', e.target ? h('code', { class: 'mono' }, e.target) : '—'],
      ['结果', [ok ? badge('成功', 'ok') : badge('失败', 'bad'), ' ', h('span', { class: 'muted' }, 'HTTP ' + e.status)]],
      ['原因', d.error ? h('span', { class: 'bad' }, d.error) : '—'],
      ['请求', d.method ? h('code', { class: 'mono' }, d.method + ' ' + (d.path || '')) : e.actor === 'cli' ? '服务器上的 portal 命令' : '—'],
      ['IP', e.ip ? h('code', { class: 'mono' }, e.ip) : '—'],
      ['客户端', e.user_agent ? [uaShort(e.user_agent), h('div', { class: 'muted audit-ua' }, e.user_agent)] : '—'],
      ['记录 ID', String(e.id)],
    ].concat(auditFields(e).map(([k, v]) => [AUDIT_KEYS[k] || k, h('span', { class: 'audit-val' }, auditVal(v))]))));
    modal({ title: '操作详情', body, okText: '关闭', cancel: false });
  }
  async function oplog(m) {
    const actor = input({ type: 'search', id: 'oplog-actor', placeholder: '操作人（完整用户名）' });
    const action = h('select', { id: 'oplog-action', onchange: () => list.reload() }, h('option', { value: '' }, '全部操作'), Object.entries(AUDIT_ACTIONS).map(([k, v]) => h('option', { value: k }, v)));
    const target = input({ type: 'search', id: 'oplog-target', placeholder: '对象（开头匹配）' });
    const list = ST.listView({ path: '/api/v1/admin/oplog', key: 'entries', empty: '没有操作记录', params: () => ({ actor: actor.value.trim(), action: action.value, target: target.value.trim() }), tools: [actor, action, target], columns: [
      { title: '时间', cell: e => h('span', { class: 'nowrap' }, time(e.at)) },
      { title: '操作人', cell: e => e.actor === 'cli' ? badge('服务器命令行') : userLink(e.actor) },
      { title: '操作', cell: e => h('a', { href: '#', class: 'nowrap audit-open', title: e.action + ' · 点击查看详情', onclick: ev => { ev.preventDefault(); auditDetail(e); } }, AUDIT_ACTIONS[e.action] || e.action) },
      { title: '对象', cell: e => e.target ? h('code', { class: 'mono audit-target', title: e.target }, e.target) : '' },
      { title: '结果', cell: e => e.status < 400 ? badge('成功', 'ok') : badge('失败 ' + e.status, 'bad') },
      { title: '详情', cell: e => { const t = auditSummary(e); return t ? h('span', { class: 'audit-summary', title: t }, t) : h('span', { class: 'muted' }, '—'); } },
      { title: '来源', cell: e => e.actor === 'cli' ? h('span', { class: 'muted' }, '服务器') : h('div', { class: 'nowrap', title: e.user_agent || '' },
        h('div', { class: 'mono' }, e.ip || '—'), h('div', { class: 'muted audit-ua' }, uaShort(e.user_agent))) },
    ] });
    add(m, pageTitle('操作日志', h('small', null, '谁在何时对什么做了什么。只追加，不能在控制台修改或删除')), list.el);
    let timer; for (const el of [actor, target]) el.addEventListener('input', () => { clearTimeout(timer); timer = setTimeout(() => list.reload(), 250); });
    await list.reload();
  }

  // ---- settings ----
  async function settings(m) {
    const view = await api('GET', '/api/v1/admin/settings');
    let st = view.settings;
    const text = (id, value, attrs) => input(Object.assign({ type: 'text', id: 'set-' + id, value: value == null ? '' : value }, attrs));
    const number = (id, value, attrs) => input(Object.assign({ type: 'number', id: 'set-' + id, value: value == null ? '' : String(value), min: 0, step: 1 }, attrs));
    const check = (id, value) => h('input', { type: 'checkbox', id: 'set-' + id, checked: !!value });
    const lines = a => (a || []).join('\n');
    const parseLines = el => el.value.split(/[\s,;]+/).map(x => x.trim()).filter(Boolean);
    const fieldHint = (label, el, hint) => h('div', { class: 'field' }, h('label', null, label), el, hint ? h('p', { class: 'hint' }, hint) : null);
    const checkHint = (el, label, hint) => h('div', { class: 'field' }, h('label', { class: 'check' }, el, label), hint ? h('p', { class: 'hint' }, hint) : null);

    const f = {
      mail_from: text('mail_from', st.mail_from, { placeholder: 'ShareToken <team@example.com>' }), mail_support: text('mail_support', st.mail_support, { placeholder: 'ShareToken Support <support@example.com>' }),
      open_registration: check('open_registration', st.open_registration), invite_required: check('invite_required', st.invite_required),
      email_domain_allow: h('textarea', { id: 'set-email_domain_allow', class: 'bulk', rows: 4, placeholder: 'example.com\n*.edu.cn' }), email_block: h('textarea', { id: 'set-email_block', class: 'bulk', rows: 6, placeholder: 'spam@example.com' }),
      signup_credit: input({ type: 'number', id: 'set-signup_credit', min: 0, step: 'any', value: String(st.signup_credit_micros / 1e6) }),
      web_session_hours: number('web_session_hours', st.web_session_hours, { max: 2160 }), login_token_hours: number('login_token_hours', st.login_token_hours, { max: 87600 }),
      default_page_size: h('select', { id: 'set-default_page_size' }, [10, 20, 50, 100].map(n => h('option', { value: n, selected: n === st.default_page_size }, n + ' 条'))),
      rate_user_rpm: number('rate_user_rpm', st.rate_user_rpm), rate_heavy_rpm: number('rate_heavy_rpm', st.rate_heavy_rpm), rate_public_rpm: number('rate_public_rpm', st.rate_public_rpm), rate_admin_exempt: check('rate_admin_exempt', st.rate_admin_exempt),
      audit_retention_days: number('audit_retention_days', st.audit_retention_days),
    };
    f.email_domain_allow.value = lines(st.email_domain_allow);
    f.email_block.value = lines(st.email_block);

    const updated = h('span', { class: 'muted', id: 'set-updated' });
    const showUpdated = v => { updated.textContent = v.updated_at ? '上次由 ' + v.updated_by + ' 修改于 ' + time(v.updated_at) : '还没有保存过：当前值来自配置文件'; };
    showUpdated(view);
    const err = h('span', { class: 'bad', id: 'set-error' });
    const saveBtn = h('button', { class: 'primary', id: 'set-save', onclick: save }, '保存设置');
    async function save() {
      err.textContent = ''; saveBtn.disabled = true;
      const int = el => Math.trunc(Number(el.value) || 0);
      try {
        const v = await api('PUT', '/api/v1/admin/settings', {
          mail_from: f.mail_from.value, mail_support: f.mail_support.value,
          open_registration: f.open_registration.checked, invite_required: f.invite_required.checked,
          email_domain_allow: parseLines(f.email_domain_allow), email_block: parseLines(f.email_block),
          signup_credit_micros: Math.round((Number(f.signup_credit.value) || 0) * 1e6),
          web_session_hours: int(f.web_session_hours), login_token_hours: int(f.login_token_hours),
          default_page_size: int(f.default_page_size), rate_user_rpm: int(f.rate_user_rpm), rate_heavy_rpm: int(f.rate_heavy_rpm), rate_public_rpm: int(f.rate_public_rpm), rate_admin_exempt: f.rate_admin_exempt.checked,
          audit_retention_days: int(f.audit_retention_days),
        });
        st = v.settings;
        f.email_domain_allow.value = lines(st.email_domain_allow);
        f.email_block.value = lines(st.email_block);
        showUpdated(v);
        const shown = state.info; state.info = null; await ST.loadInfo().catch(() => { state.info = shown; });
        toast('设置已保存，立即生效');
      } catch (ex) {
        err.textContent = ex.message;
        const el = ex.data && ex.data.field && document.getElementById('set-' + ex.data.field);
        if (el) el.focus();
      } finally { saveBtn.disabled = false; }
    }

    async function applyBlocklist() {
      try {
        const dry = await api('POST', '/api/v1/admin/settings/apply-blocklist', { dry_run: true });
        if (!dry.matched.length) { toast(dry.skipped_admins.length ? '只命中管理员账户，不会被禁用：' + dry.skipped_admins.join('、') : '已保存的黑名单没有命中任何启用中的用户'); return; }
        const body = h('div', null, h('p', null, '将禁用 ' + dry.matched.length + ' 个已注册用户（按已保存的黑名单）：'), h('pre', { class: 'audit-detail', id: 'set-block-matched' }, dry.matched.join('\n')),
          dry.skipped_admins.length ? h('p', { class: 'muted' }, '管理员不会被禁用：' + dry.skipped_admins.join('、')) : null);
        if (!await modal({ title: '禁用命中用户', okText: '禁用', danger: true, body })) return;
        const r = await api('POST', '/api/v1/admin/settings/apply-blocklist', { dry_run: false });
        toast('已禁用 ' + r.matched.length + ' 个用户');
      } catch (ex) { fail(ex); }
    }

    // Mail: what the mail server reports, and a test that shows what became of it.
    const o = view.mail, srv = o && o.server;
    const testTo = input({ type: 'email', id: 'set-test-to', placeholder: 'you@example.com', value: me().user.username.includes('@') ? me().user.username : '' });
    const testResult = h('div', { id: 'set-test-result' });
    const testBtn = h('button', { id: 'set-test-send', onclick: sendTest }, '发送测试邮件');
    async function sendTest() {
      testBtn.disabled = true;
      clear(testResult);
      try {
        const r = await api('POST', '/api/v1/admin/mail/test', { to: testTo.value.trim() });
        const line = h('p', { class: 'muted' }, '已加入发送队列，等待邮件服务投递…');
        const link = h('p', null, h('a', { href: '#/mail/outbox/' + r.outbox_id }, '在发件箱查看 #' + r.outbox_id));
        add(testResult, line, link);
        for (let i = 0; i < 30 && main.contains(testResult); i++) {
          await new Promise(res => setTimeout(res, 2000));
          const x = (await api('GET', '/api/v1/admin/mail/outbox/' + r.outbox_id)).message;
          if (x.status === 'sent') { add(clear(line), badge('对方已接受', 'ok'), ' 收件服务器 ' + (x.remote || '') + '。这不代表进了收件箱，请到邮箱确认（也看垃圾邮件）。'); return; }
          if (x.status === 'failed' || x.status === 'cancelled') { add(clear(line), badge('失败', 'bad'), ' ', x.last_error || x.status); return; }
          if (x.last_error) add(clear(line), badge('重试中', 'warn'), ' 第 ' + x.attempts + ' 次尝试：' + x.last_error);
        }
        if (main.contains(testResult)) add(clear(line), badge('仍在排队', 'warn'), o && o.online ? ' 邮件服务在线，但一分钟内还没投递完，稍后到发件箱查看。' : ' 邮件服务离线，恢复后才会投递。');
      } catch (ex) { add(clear(testResult), h('p', { class: 'bad' }, ex.message)); }
      finally { testBtn.disabled = false; }
    }

    // Invite codes take effect one by one, outside the settings form.
    const inviteList = h('div', { id: 'set-invites' });
    async function loadInvites() {
      const r = await api('GET', '/api/v1/admin/invites');
      add(clear(inviteList), table([
        { title: '邀请码', cell: i => h('span', { class: 'row' }, h('code', { class: 'mono invite-code' }, i.code), h('button', { type: 'button', class: 'small', onclick: () => ST.copy(i.code) }, '复制')) },
        { title: '备注', cell: i => i.note || '' },
        { title: '已用 / 上限', num: true, cell: i => i.used + ' / ' + (i.max_uses || '不限') },
        { title: '过期', cell: i => i.expires_at ? time(i.expires_at) : '永不' },
        { title: '创建', cell: i => [time(i.created_at), h('div', { class: 'muted' }, i.created_by)] },
        { title: '状态', cell: i => i.disabled ? badge('已停用') : (i.max_uses && i.used >= i.max_uses) ? badge('已用完', 'warn') : (i.expires_at && i.expires_at / 1e6 < Date.now()) ? badge('已过期', 'warn') : badge('可用', 'ok') },
        { title: '', cell: i => i.disabled ? '' : h('button', { type: 'button', class: 'small danger', onclick: async () => {
          if (!await confirm('停用邀请码', '停用 ' + i.code + ' 后不能再用它注册；已注册的用户不受影响。', '停用')) return;
          try { await api('DELETE', '/api/v1/admin/invites/' + i.id); toast('已停用'); await loadInvites(); } catch (ex) { fail(ex); }
        } }, '停用') },
      ], r.invites, '还没有邀请码'));
    }
    async function newInvite() {
      const note = input({ type: 'text', id: 'invite-note', maxlength: 128, placeholder: '给谁、什么用途' });
      const uses = input({ type: 'number', id: 'invite-uses', min: 0, value: '1' });
      const ttl = h('select', { id: 'invite-ttl' }, h('option', { value: '0' }, '永不过期'), h('option', { value: '24' }, '1 天'), h('option', { value: '168' }, '7 天'), h('option', { value: '720' }, '30 天'));
      if (!await modal({ title: '新建邀请码', okText: '创建', body: h('div', { class: 'form' }, field('备注', note), field('可用次数（0 为不限）', uses), field('有效期', ttl)) })) return;
      try { await api('POST', '/api/v1/admin/invites', { note: note.value.trim(), max_uses: Math.trunc(Number(uses.value) || 0), ttl_hours: Number(ttl.value) }); toast('已创建'); await loadInvites(); } catch (ex) { fail(ex); }
    }
    await loadInvites();

    add(m, pageTitle('系统设置', h('small', null, '保存后立即生效，不用重启；配置文件里的同名项只在第一次保存之前有效')),
      section('users', '注册管控'), h('div', { class: 'card' }, h('div', { class: 'settings-grid' },
        checkHint(f.open_registration, '开放注册', '开启后，没有账户的邮箱第一次登录时自动注册；关闭后只有管理员能创建用户。登录需要发件地址和运行中的邮件服务。'),
        checkHint(f.invite_required, '注册需要邀请码', '新邮箱第一次登录时要填写邀请码；已有账户不受影响。'),
        fieldHint('新用户赠送 credit', f.signup_credit)),
        h('div', { class: 'settings-grid' },
          fieldHint('邮箱域名白名单', f.email_domain_allow, '一行一个，如 example.com、*.edu.cn（后者匹配所有子域名，不含 edu.cn 本身）。留空不限制。只管新注册。'),
          h('div', { class: 'field' }, h('label', null, '邮箱黑名单（精确匹配）'), f.email_block, h('p', { class: 'hint' }, '一行一个。只阻止新注册；已注册的用户用下面的按钮处理。'),
            h('button', { type: 'button', class: 'small', id: 'set-block-apply', onclick: applyBlocklist }, '禁用已注册的命中用户'))),
        h('h3', null, '邀请码'), h('div', { class: 'row' }, h('button', { type: 'button', id: 'invite-new', onclick: newInvite }, '新建邀请码')), inviteList),
      section('mail', '邮件'), h('div', { class: 'card' }, h('div', { class: 'settings-grid' }, fieldHint('发件地址', f.mail_from, '验证码和通知从这里发出，域名要和 DKIM 一致。'), fieldHint('客服地址', f.mail_support, '用户看到的联系邮箱，回复邮件也从这里发出。')),
        kv([['邮件服务', o ? badge(o.online ? '运行中' : '离线', o.online ? 'ok' : 'bad') : '—'], ['发信方式', srv ? srv.delivery : '—'], ['主机名', srv ? srv.hostname : '—'], ['DKIM 选择器', srv ? srv.dkim_selector : '—']]),
        h('p', { class: 'hint' }, 'SMTP 投递方式（直投或 smarthost）和它的密码在邮件服务自己的 mail.yaml 里，这里只读：邮件服务只对外收信，不读门户的设置；控制台也不应该能把所有外发邮件（包括登录验证码）改道。详见 docs/mail.md。'),
        h('div', { class: 'row' }, testTo, testBtn), testResult),
      section('lock', '安全'), h('div', { class: 'card' }, h('div', { class: 'settings-grid' },
        fieldHint('浏览器会话有效期（小时）', f.web_session_hours, '用户门户和控制台。只影响之后的登录。0 用配置文件的值（默认 360，即 15 天）。'), fieldHint('客户端登录 token 有效期（小时）', f.login_token_hours, '桌面客户端和 CLI 登录时签发。0 用配置文件的值（默认 1440，即 60 天）。'),
        fieldHint('操作日志保留天数', f.audit_retention_days, '0 永久保留；否则至少 7 天。'))),
      section('bolt', 'API 限流'), h('div', { class: 'card' }, h('p', { class: 'hint' }, '每分钟请求数，0 为不限。门户重启后计数清零。'), h('div', { class: 'settings-grid' },
        fieldHint('每账号', f.rate_user_rpm, '所有登录后的接口。不为 0 时至少 60，打开一个页面就有好几个请求。'), fieldHint('每账号重查询', f.rate_heavy_rpm, 'CSV 导出和总览页的账本回放。'),
        fieldHint('每 IP 公开接口', f.rate_public_rpm, '发送和核对登录验证码、站点信息。不为 0 时至少 30。公司出口共用一个 IP 时要放宽。'), checkHint(f.rate_admin_exempt, '管理员不限流'))),
      section('grid', '界面'), h('div', { class: 'card' }, h('div', { class: 'settings-grid' }, fieldHint('默认每页条数', f.default_page_size, '用户自己选过的不受影响。'))),
      h('div', { class: 'settings-actions' }, saveBtn, updated, err));
  }

  const go = ST.router({ dashboard, overview, users, relays, versions, jobs, nodes, audits, disputes, pairings, partners, ledger, mail, feedback, oplog, settings }, nav, main, 'dashboard');
  refreshUnread();
  await go();
})();
