'use strict';
/* Ledgerline console: the G · Spreadsheet workbench layout (layouts.js).
   Spreadsheet-style tables of clients, margin (RMS), reconciliation breaks
   and the work queue: built-in and saved views (kept for the user on the
   server, /v1/me/ui), search, filters, grouping, chosen columns, sorting,
   totals of the rows shown, row selection with bulk actions, a peek panel,
   export to CSV and the keyboard. The rail lists the tables and each one's
   saved views; every other screen opens beside the same rail. */

const GR = { data: {}, mine: null, cur: 0, limit: 500, rows: [] };
// grTopHeight: the header's height, for the rail and the sticky parts under
// it (--ly-top; layouts.js clears it when the layout changes).
function grTopHeight() { const t = $('top'); if (t && t.offsetHeight && ['grid', 'autopilot'].includes(document.body.dataset.layout)) document.body.style.setProperty('--ly-top', t.offsetHeight + 'px'); }
addEventListener('resize', () => requestAnimationFrame(grTopHeight));
// UI.grBase: the built-in view whose rule applies (a saved view keeps it as
// the filter "_base").
Object.assign(UI, { grBase: '' });
const GR_PILL = { OK: 'good', Watch: 'warn', Breach: 'bad', Debit: 'warn', Active: 'good', Open: 'warn', OPEN: 'bad', RESOLVED: 'good', CLEARED: 'good', Done: 'good', Cleared: 'good', Dismissed: 'nt', Closed: 'nt', Blocked: 'bad' };
const grOnboarding = s => !['Active', 'Closed', 'Rejected', 'Inactive'].includes(s);
// Each table: where it comes from, its columns (key, label, type, getter)
// and its built-in views. Types: text, mono, num, pct, money, pill, date,
// bool, and list (several values, such as groups: a filter matches any).
const GR_SRC = {
  clients: {
    name: 'Clients', key: r => r.ucc, ok: () => viewAllowed('clients'),
    load: async () => (await clientCache(true)).rows,
    cols: [
      ['ucc', 'UCC', 'mono', r => r.ucc], ['name', 'Name', 'text', r => r.name], ['tags', 'Groups', 'list', r => (r.tags || []).join(', ')],
      ['type', 'Type', 'text', r => r.type], ['pan', 'PAN', 'mono', r => r.pan], ['city', 'City', 'text', r => r.city], ['branch', 'Branch', 'text', r => r.branch || ''],
      ['segments', 'Segments', 'list', r => (r.segments || []).join(', ')], ['funds', 'Ledger (₹)', 'money', r => r.funds], ['risk_status', 'RMS', 'pill', r => r.risk_status || ''],
      ['status', 'Stage', 'pill', r => r.status], ['risk', 'PMLA risk', 'text', r => r.risk || ''], ['kyc_date', 'KYC date', 'date', r => r.kyc_date || ''],
      ['last_trade', 'Last trade', 'date', r => r.last_trade || ''], ['kra_status', 'KRA', 'text', r => r.kra_status || ''], ['blocked', 'Blocked', 'bool', r => !!r.blocked], ['dp_only', 'DP only', 'bool', r => !!r.dp_only],
    ],
    def: ['ucc', 'name', 'tags', 'type', 'pan', 'segments', 'funds', 'risk_status', 'status'],
    views: [['all', 'All clients', () => true], ['active', 'Active', r => r.status === 'Active'], ['onb', 'Being onboarded', r => grOnboarding(r.status)], ['watch', 'In breach or on watch', r => ['Breach', 'Watch'].includes(r.risk_status)], ['debit', 'Debit balances', r => num(r.funds) < 0], ['hni', 'HNI', r => (r.tags || []).includes('HNI')]],
  },
  rms: {
    name: 'Margin', key: r => r.ucc, ok: () => viewAllowed('rms'),
    load: () => get(`/v1/rms?date=${S.date}`).then(d => d.rows || []),
    cols: [
      ['ucc', 'UCC', 'mono', r => r.ucc], ['name', 'Name', 'text', r => r.name], ['status', 'Status', 'pill', r => r.status], ['funds', 'Ledger (₹)', 'money', r => r.funds],
      ['collateral_usable', 'Collateral usable (₹)', 'money', r => r.collateral_usable], ['required', 'Margin required (₹)', 'money', r => r.required], ['free', 'Free (₹)', 'money', r => r.free],
      ['utilisation', 'Utilisation', 'pct', r => r.utilisation], ['limit', 'OMS limit (₹)', 'money', r => r.limit], ['positions', 'Positions', 'num', r => r.positions],
      ['upi', 'UPI blocked (₹)', 'money', r => r.upi], ['futeq', 'Futures equivalent (₹)', 'money', r => r.futeq], ['blocked', 'Blocked', 'bool', r => !!r.blocked], ['square_off_sent', 'Square-offs sent', 'num', r => r.square_off_sent],
    ],
    def: ['ucc', 'name', 'status', 'funds', 'collateral_usable', 'required', 'free', 'utilisation', 'limit', 'positions'],
    views: [['all', 'All clients', () => true], ['breach', 'Breach', r => r.status === 'Breach'], ['watch', 'Watch', r => r.status === 'Watch'], ['debit', 'Debit', r => r.status === 'Debit'], ['pos', 'With positions', r => r.positions > 0]],
  },
  breaks: {
    name: 'Reconciliation breaks', key: r => r.id, ok: () => viewAllowed('recon') && !scopedUser(),
    load: () => get(`/v1/recon/breaks?date=${S.date}`),
    cols: [
      ['type', 'Break', 'text', r => label(r.type)], ['status', 'Status', 'pill', r => r.status], ['exchange', 'Exchange', 'text', r => r.exchange + '-' + r.segment], ['ucc', 'UCC', 'mono', r => r.ucc],
      ['instrument', 'Contract', 'text', r => instName(r.instrument)], ['trade_no', 'Trade no.', 'mono', r => r.trade_no], ['value', 'Value (₹)', 'money', r => r.value],
      ['diff', 'Difference', 'text', r => (r.diffs || []).map(x => `${x.field} ${x.exchange || '—'} vs ${x.oms || '—'}`).join('; ')], ['suggestion', 'What to do', 'text', r => r.suggestion || ''], ['id', 'Break ID', 'mono', r => r.id],
    ],
    def: ['type', 'status', 'exchange', 'ucc', 'instrument', 'trade_no', 'value', 'diff'],
    views: [['open', 'Open', r => r.status === 'OPEN'], ['all', 'All breaks', () => true]],
  },
  work: {
    name: 'Work queue', key: r => r.id, ok: () => true,
    load: async () => { await lyData(0); return LY.ap?.work || []; },
    cols: [
      ['title', 'Item', 'text', r => r.title], ['process', 'Automation', 'text', r => lyProc(r)], ['area', 'Area', 'text', r => lyArea(r)], ['role', 'For', 'text', r => r.role || ''],
      ['ucc', 'UCC', 'mono', r => r.ucc || ''], ['due', 'Due', 'mono', r => r.due || ''], ['status', 'Status', 'pill', r => r.status], ['overdue', 'Overdue', 'bool', r => !!r.overdue],
      ['detail', 'Detail', 'text', r => r.detail || ''], ['proposed', 'Prepared action', 'text', r => r.proposed?.label || ''], ['closed_by', 'Closed by', 'text', r => r.closed_by || ''],
    ],
    def: ['title', 'area', 'role', 'ucc', 'due', 'status', 'proposed'],
    views: [['mine', 'For my role', r => r.status === 'Open' && lyMine(r)], ['open', 'Open', r => r.status === 'Open'], ['overdue', 'Overdue', r => r.status === 'Open' && r.overdue], ['closed', 'Closed', r => r.status !== 'Open']],
  },
};
const grSrcs = () => Object.keys(GR_SRC).filter(k => GR_SRC[k].ok());
const grSrc = () => { if (!GR_SRC[UI.grSrc]?.ok()) UI.grSrc = grSrcs()[0] || 'work'; return GR_SRC[UI.grSrc]; };
const grCol = k => grSrc().cols.find(c => c[0] === k);
const grShown = () => { const s = grSrc(), ks = (UI.grCols || s.def).filter(k => s.cols.some(c => c[0] === k)); return ks.length ? ks : s.def; };
const grMineViews = src => (GR.mine?.views || []).filter(v => v.source === (src || UI.grSrc));
const grBuiltin = () => grSrc().views.find(v => v[0] === UI.grBase) || grSrc().views[0];
const grSaved = () => (GR.mine?.views || []).find(v => v.id === UI.grView && v.source === UI.grSrc);
// grText: a cell's value as text (filters, search, grouping and export).
function grText(c, r) {
  const v = c[3](r);
  if (c[2] === 'bool') return v ? 'Yes' : 'No';
  if (c[2] === 'money') return num(v).toFixed(2);
  if (c[2] === 'pct') return (num(v) * 100).toFixed(1);
  return v == null ? '' : String(v);
}
const grList = (c, r) => grText(c, r).split(', ').filter(Boolean);
function grCell(c, r) {
  const v = c[3](r);
  switch (c[2]) {
    case 'money': return `<td class="r ${num(v) < 0 ? 'neg' : ''}">${nf(v, 0)}</td>`;
    case 'num': return `<td class="r">${nf(v)}</td>`;
    case 'pct': return `<td class="r">${pct(v)}</td>`;
    case 'pill': return `<td>${v ? pill(v, GR_PILL[v] || 'nt') : ''}</td>`;
    case 'bool': return `<td>${v ? pill(c[1], c[0] === 'overdue' || c[0] === 'blocked' ? 'bad' : 'nt') : ''}</td>`;
    case 'date': return `<td class="n">${v ? fmtDate(v) : ''}</td>`;
    case 'mono': return c[0] === 'ucc' && v ? `<td class="n"><button class="linkish mono" data-act="client" data-id="${esc(v)}">${esc(v)}</button></td>` : `<td class="n">${esc(v)}</td>`;
    default: return `<td class="${String(v).length > 60 ? 'wrap' : ''}">${esc(v)}</td>`;
  }
}
// grRows: the table's rows after the view, filters, search and sort.
function grRows(all) {
  const s = grSrc(), b = grBuiltin(), q = UI.grQ.trim().toLowerCase();
  let rows = all.filter(r => !b || b[2](r));
  for (const [k, v] of Object.entries(UI.grF || {})) { const c = k[0] !== '_' && grCol(k); if (c) rows = rows.filter(r => c[2] === 'list' ? grList(c, r).includes(v) || (!v && !grText(c, r)) : grText(c, r) === v); }
  if (q) { const cs = s.cols; rows = rows.filter(r => cs.some(c => grText(c, r).toLowerCase().includes(q))); }
  const sk = UI.grSort.replace(/^-/, ''), sc = grCol(sk), dir = UI.grSort.startsWith('-') ? -1 : 1;
  if (sc) {
    const numeric = ['money', 'num', 'pct'].includes(sc[2]);
    rows = [...rows].sort((a, z) => { const x = numeric ? num(sc[3](a)) : grText(sc, a), y = numeric ? num(sc[3](z)) : grText(sc, z); return (numeric ? x - y : String(x).localeCompare(String(y), 'en-IN', { numeric: true })) * dir; });
  }
  return rows;
}

LYS.grid = {
  side: () => {
    const srcs = grSrcs();
    return `<nav class="ly-navg gr-rail" aria-label="Tables"><h6>Tables</h6>${srcs.map(k => {
      const n = GR.data[k]?.length, saved = grMineViews(k);
      return `<button class="ly-nav ${S.view === 'lyGrid' && UI.grSrc === k && !grSaved() ? 'on' : ''}" data-act="grSrc" data-id="${k}"><span>${esc(GR_SRC[k].name)}</span>${n != null ? `<span class="dim small mono">${n}</span>` : ''}</button>
        ${saved.map(v => `<button class="ly-nav gr-sv ${S.view === 'lyGrid' && UI.grView === v.id ? 'on' : ''}" data-act="grOpenView" data-id="${esc(v.id)}"><span>${esc(v.name)}</span></button>`).join('')}`;
    }).join('')}</nav>
    <nav class="ly-navg" aria-label="Screens"><h6>Screens</h6>
      ${viewAllowed('dashboard') ? `<button class="ly-nav ${S.view === 'dashboard' ? 'on' : ''}" data-act="go" data-id="dashboard"><span>Dashboard</span></button>` : ''}
      ${viewAllowed('autopilot') ? `<button class="ly-nav ${S.view === 'autopilot' ? 'on' : ''}" data-act="go" data-id="autopilot"><span>Autopilot &amp; work queue</span>${lyCtB('autopilot')}</button>` : ''}
      <button class="ly-nav" data-act="lyMenu"><span>All screens…</span></button></nav>
    <div class="gr-tip dim small">Space opens the peek for the row under the cursor; Enter opens the row; / searches.</div>`;
  },
  top: () => { requestAnimationFrame(grTopHeight); return `<div class="ly-top">${lyBrand()}<span class="gr-crumb"><span class="dim">/</span> ${esc(S.view === 'lyGrid' ? grSrc().name : (NAV_VIEWS.find(v => v[0] === S.view) || [, 'Workbench'])[1])}</span>${lyDateNav()}<span class="ly-push"></span>${lySearch()}${lyMenuBtn()}${lyUser(true)}</div>`; },
  key: e => {
    if (S.view !== 'lyGrid') return;
    const n = GR.rows.length;
    if (e.key === '/') { e.preventDefault(); $('gr-q')?.focus(); return; }
    if (!n) return;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'j' || e.key === 'k') { e.preventDefault(); GR.cur = Math.max(0, Math.min(Math.min(n, GR.limit) - 1, GR.cur + (e.key === 'ArrowDown' || e.key === 'j' ? 1 : -1))); if (UI.grPeek) UI.grPeek = grSrc().key(GR.rows[GR.cur]); grDraw(); document.querySelector('.gr-tbl tr.cur')?.scrollIntoView({ block: 'nearest' }); return; }
    if (e.key === ' ') { e.preventDefault(); const k = grSrc().key(GR.rows[GR.cur]); UI.grPeek = UI.grPeek === k ? '' : k; grDraw(); return; }
    if (e.key === 'Enter') { e.preventDefault(); grOpen(GR.rows[GR.cur]); return; }
    if (e.key === 'Escape' && UI.grPeek) { UI.grPeek = ''; grDraw(); }
  },
};

V.lyGrid = {
  load: async () => {
    const s = grSrc();
    const [rows, mine] = await Promise.all([s.load(), get('/v1/me/ui').catch(() => null)]);
    GR.data[UI.grSrc] = rows || [];
    if (mine) GR.mine = mine.mine || {};
    if (!UI.grView || (UI.grView.startsWith('b:') && !s.views.some(v => 'b:' + v[0] === UI.grView)) || (!UI.grView.startsWith('b:') && !grSaved())) { UI.grView = 'b:' + s.views[0][0]; UI.grBase = s.views[0][0]; }
    return { rows: GR.data[UI.grSrc] };
  },
  html: d => {
    const s = grSrc(), sv = grSaved(), dirty = sv && grDirty(sv);
    const fcols = s.cols.filter(c => ['text', 'list', 'mono', 'pill', 'bool', 'date'].includes(c[2]));
    return `<div class="gr">
      <nav class="gr-views" aria-label="Views of ${esc(s.name)}">${s.views.map(v => `<button class="gr-vt ${UI.grView === 'b:' + v[0] ? 'on' : ''}" data-act="grView" data-id="b:${v[0]}">${esc(v[1])} <span class="dim mono">${d.rows.filter(v[2]).length}</span></button>`).join('')}
        ${grMineViews().map(v => `<span class="gr-vt mine ${UI.grView === v.id ? 'on' : ''}"><button data-act="grOpenView" data-id="${esc(v.id)}">${esc(v.name)}</button><button class="gr-x" data-act="grDelView" data-id="${esc(v.id)}" aria-label="Delete the view ${esc(v.name)}">×</button></span>`).join('')}
        <button class="gr-vt add" data-act="grSaveView">+ Save this view</button>${dirty ? `<button class="btn sm pri" data-act="grUpdView">Save changes to “${esc(sv.name)}”</button>` : ''}</nav>
      <div class="gr-tools">
        <label class="gr-search">${lySvg('search', 15)}<input id="gr-q" class="in" data-ui="grQ" data-part="gr-t" placeholder="Search every column" value="${esc(UI.grQ)}" autocomplete="off" aria-label="Search the table"></label>
        ${!UI.grView.startsWith('b:') && grBuiltin() !== s.views[0] ? `<span class="gr-chip base">${esc(grBuiltin()[1])}</span>` : ''}${Object.entries(UI.grF || {}).filter(([k]) => k[0] !== '_').map(([k, v]) => `<span class="gr-chip">${esc(grCol(k)?.[1] || k)} is <b>${esc(v || '(blank)')}</b><button data-act="grUnfilter" data-id="${esc(k)}" aria-label="Remove filter">×</button></span>`).join('')}
        <button class="btn sm" data-act="grFilter">+ Filter</button>
        <label class="gr-sel">Group by <select class="in sm" id="gr-group"><option value="">none</option>${fcols.filter(c => c[2] !== 'mono' || c[0] === 'ucc').map(c => `<option value="${c[0]}" ${UI.grGroup === c[0] ? 'selected' : ''}>${esc(c[1])}</option>`).join('')}</select></label>
        <button class="btn sm" data-act="grCols">Columns: ${grShown().length} of ${s.cols.length}</button>
        <button class="btn sm ${uiE().density === 'compact' ? 'on' : ''}" data-act="grDensity" aria-pressed="${uiE().density === 'compact'}">Compact</button>
        <span class="ly-push"></span><button class="btn sm" data-act="grExport">Export to CSV</button></div>
      <div class="gr-body ${UI.grPeek ? 'peeking' : ''}"><div class="gr-main panel" id="gr-t">${P['gr-t'](d)}</div><aside class="gr-peek panel" id="gr-peek" aria-label="Peek">${grPeekHTML(d)}</aside></div></div>`;
  },
};
// grDirty: the saved view's settings differ from what is on screen.
function grDirty(v) {
  const same = (a, b) => JSON.stringify(a || null) === JSON.stringify(b || null);
  const strip = o => { const x = { ...(o || {}) }; delete x._base; return Object.keys(x).length ? x : null; };
  const f = strip(UI.grF), vf = strip(v.filters);
  return (v.filters?._base || grSrc().views[0][0]) !== (UI.grBase || grSrc().views[0][0]) || (v.q || '') !== UI.grQ || !same(vf, f) || (v.sort || '') !== UI.grSort || (v.group || '') !== UI.grGroup || !same(v.columns?.length ? v.columns : null, UI.grCols);
}
P['gr-t'] = d => {
  const s = grSrc(), rows = grRows(d.rows), shown = grShown().map(grCol), canSel = true;
  GR.rows = rows; if (GR.cur >= rows.length) GR.cur = Math.max(0, rows.length - 1);
  const sel = UI.grSel, bulkTags = UI.grSrc === 'clients' && has('Ops maker', 'Ops checker');
  for (const k of [...sel]) if (!d.rows.some(r => s.key(r) === k)) sel.delete(k);
  const lim = Math.min(rows.length, GR.limit), money = shown.filter(c => c[2] === 'money');
  const allOn = lim && rows.slice(0, lim).every(r => sel.has(s.key(r)));
  const th = c => { const on = UI.grSort.replace(/^-/, '') === c[0]; return `<th class="${['money', 'num', 'pct'].includes(c[2]) ? 'r' : ''}" aria-sort="${on ? (UI.grSort.startsWith('-') ? 'descending' : 'ascending') : 'none'}"><button class="srt" data-act="grSort" data-id="${c[0]}">${esc(c[1])}${on ? (UI.grSort.startsWith('-') ? ' ▼' : ' ▲') : ''}</button></th>`; };
  const tr = (r, i) => { const k = s.key(r); return `<tr class="${sel.has(k) ? 'sel' : ''} ${i === GR.cur ? 'cur' : ''} ${UI.grPeek === k ? 'peek' : ''}" data-act="grRow" data-id="${i}">${canSel ? `<td class="gr-ck"><input type="checkbox" data-act="grSelOne" data-id="${esc(k)}" aria-label="Select ${esc(k)}" ${sel.has(k) ? 'checked' : ''}></td>` : ''}${shown.map(c => grCell(c, r)).join('')}</tr>`; };
  let body = '';
  const page = rows.slice(0, lim);
  if (UI.grGroup && grCol(UI.grGroup)) {
    const gc = grCol(UI.grGroup), groups = new Map();
    page.forEach((r, i) => { const g = grText(gc, r) || '(blank)'; if (!groups.has(g)) groups.set(g, []); groups.get(g).push([r, i]); });
    for (const [g, rs] of [...groups].sort((a, b) => a[0].localeCompare(b[0], 'en-IN', { numeric: true }))) {
      body += `<tr class="gr-grp"><td colspan="${shown.length + 1}"><b>${esc(gc[1])}: ${esc(g)}</b> <span class="dim">· ${rs.length}</span>${money.map(c => ` <span class="dim">· ${esc(c[1].replace(/ \(₹\)/, ''))} ${inr(sum(rs, x => num(c[3](x[0]))))}</span>`).join('')}</td></tr>` + rs.map(([r, i]) => tr(r, i)).join('');
    }
  } else body = page.map(tr).join('');
  const bulk = sel.size ? `<div class="bulkbar gr-bulk"><b>${sel.size} selected</b>${bulkTags ? `<input class="in" id="bulk-tag" list="gr-tag-list" placeholder="Group, e.g. HNI" aria-label="Group"><datalist id="gr-tag-list">${(CL.tags || []).map(t => `<option value="${esc(t.tag)}">`).join('')}</datalist><button class="btn sm pri" data-act="grBulk" data-id="add">Add to group</button><button class="btn sm" data-act="grBulk" data-id="remove">Remove from group</button>` : ''}<button class="btn sm" data-act="grExport" data-id="sel">Export the selected</button><button class="btn sm" data-act="grClear">Clear</button></div>` : '';
  const foot = `<tfoot><tr><td></td>${shown.map((c, i) => i === 0 ? `<td colspan="1" class="gr-count">${lim < rows.length ? `${lim} shown of ${rows.length}` : `${rows.length} of ${d.rows.length}`}</td>` : c[2] === 'money' ? `<td class="r">${nf(sum(rows, r => num(c[3](r))), 0)}</td>` : '<td></td>').join('')}</tr></tfoot>`;
  return bulk + `<div class="tw gr-tw"><table class="tbl gr-tbl"><thead><tr><th class="gr-ck"><input type="checkbox" data-act="grSelAll" aria-label="Select all shown" ${allOn ? 'checked' : ''}></th>${shown.map(th).join('')}</tr></thead><tbody>${body || `<tr><td colspan="${shown.length + 1}" class="empty">${d.rows.length ? 'No rows match this view, filter or search.' : 'Nothing here for ' + fmtDate(S.date) + '.'}</td></tr>`}</tbody>${rows.length ? foot : ''}</table></div>
    ${rows.length > lim ? `<div class="gr-more"><button class="btn sm" data-act="grMore">Show ${Math.min(500, rows.length - lim)} more</button> <span class="dim small">Totals cover all ${rows.length} rows.</span></div>` : ''}
    ${money.length && rows.length ? '<div class="gr-note dim small">The footer adds up the money columns of every row in this view.</div>' : ''}`;
};
function grPeekHTML(d) {
  const s = grSrc(), r = UI.grPeek && d.rows.find(x => s.key(x) === UI.grPeek);
  if (!r) return '';
  const kv = s.cols.map(c => [c[1].replace(' (₹)', ''), grText(c, r), c]).filter(x => x[1] !== '' && x[1] !== 'No');
  const fmt = (v, c) => c[2] === 'money' ? inr(v, 2) : c[2] === 'pct' ? v + '%' : c[2] === 'date' ? fmtDate(v) : c[2] === 'pill' ? pill(v, GR_PILL[v] || 'nt') : c[2] === 'bool' ? 'Yes' : esc(v);
  let head = '', acts = '';
  if (UI.grSrc === 'clients' || UI.grSrc === 'rms') {
    head = `<b class="gr-pk-t">${esc(r.name)}</b><span class="mono dim">${esc(r.ucc)}</span>`;
    acts = `<button class="btn sm pri" data-act="client" data-id="${esc(r.ucc)}">Open client</button><button class="btn sm" data-act="crFor" data-id="${esc(r.ucc)}">Statement</button>${viewAllowed('ledger') ? `<button class="btn sm" data-act="openLedger" data-id="${esc(r.ucc)}">Ledger</button>` : ''}`;
  } else if (UI.grSrc === 'breaks') {
    head = `<b class="gr-pk-t">${esc(label(r.type))}</b><span class="mono dim">${esc(r.trade_no)} · ${esc(r.ucc)}</span>`;
    acts = `<button class="btn sm pri" data-act="grOpenRow">Open in reconciliation</button>`;
  } else {
    head = `<b class="gr-pk-t">${esc(r.title)}</b><span class="dim small">${esc(lyProc(r))}</span>`;
    acts = `${r.status === 'Open' && r.proposed && lyCanAct(r) ? `<button class="btn sm pri" data-act="wDo" data-id="${esc(r.id)}">${esc(r.proposed.label)}</button>` : ''}<button class="btn sm" data-act="grOpenRow">Open its screen</button>${r.status === 'Open' && lyCanAct(r) ? `<button class="btn sm" data-act="wDismiss" data-id="${esc(r.id)}">Close with a note</button>` : ''}`;
  }
  return `<div class="gr-pk-h"><span class="dim small">Peek</span><button class="gr-x" data-act="grPeekClose" aria-label="Close peek">×</button></div><div class="gr-pk-name">${head}</div>
    <dl class="gr-dl">${kv.map(([l, v, c]) => `<dt>${esc(l)}</dt><dd class="${['mono', 'money', 'num'].includes(c[2]) ? 'mono' : ''}">${fmt(v, c)}</dd>`).join('')}</dl><div class="gr-pk-a">${acts}</div>
    <div class="dim small">Space opens the peek for the row under the cursor; Enter opens the full page.</div>`;
}
// grDraw: the table and the peek, without loading again.
function grDraw() {
  const d = C.lyGrid; if (!d || S.view !== 'lyGrid') return;
  const t = $('gr-t'), p = $('gr-peek'); if (!t) return;
  t.innerHTML = P['gr-t'](d); p.innerHTML = grPeekHTML(d);
  document.querySelector('.gr-body')?.classList.toggle('peeking', !!UI.grPeek);
}
function grOpen(r) {
  if (!r) return;
  if (UI.grSrc === 'clients' || UI.grSrc === 'rms') { clientDrawer(r.ucc); return; }
  if (UI.grSrc === 'breaks') { UI.reconQ = r.trade_no; UI.reconF = r.status === 'OPEN' ? 'OPEN' : ''; UI.reconX = 'All'; A.go('recon'); return; }
  const v = String(r.view || ''), b = v.split(':')[0]; A.go(viewAllowed(b) ? v : 'autopilot');
}
function grSetView(v) {
  const f = { ...(v?.filters || {}) }; if (v) UI.grBase = f._base || grSrc().views[0][0]; delete f._base;
  UI.grQ = v?.q || ''; UI.grF = f; UI.grSort = v?.sort || ''; UI.grGroup = v?.group || ''; UI.grCols = v?.columns?.length ? [...v.columns] : null;
  UI.grSel.clear(); UI.grPeek = ''; GR.cur = 0; GR.limit = 500;
}
async function grPutViews(views, msg) {
  try { const r = await api('PUT', '/v1/me/ui', { views }); GR.mine = r.mine || {}; toast(msg); render({ keep: true }); return true; }
  catch (e) { toast(e.message, true); return false; }
}
const grViewNow = (id, name) => ({ id, name, source: UI.grSrc, q: UI.grQ.trim(), filters: { ...(UI.grF || {}), ...(grBuiltin() !== grSrc().views[0] ? { _base: grBuiltin()[0] } : {}) }, sort: UI.grSort, group: UI.grGroup, columns: UI.grCols ? [...UI.grCols] : [] });
function grCSV(rows, cols) {
  const cell = (c, r) => { let v = grText(c, r); if (c[2] === 'pct') v = (num(c[3](r)) * 100).toFixed(2); if (/^[=+@\t\r]/.test(v) || (/^-/.test(v) && !['money', 'num', 'pct'].includes(c[2]))) v = "'" + v; return /[",\n;]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v; };
  return '﻿' + [cols.map(c => cell(['', '', 'text', () => c[1]], {})).join(','), ...rows.map(r => cols.map(c => cell(c, r)).join(','))].join('\r\n') + '\r\n';
}

document.addEventListener('input', e => { if (e.target.id === 'gr-q') { GR.cur = 0; setTimeout(() => { A.grRefreshViews(); const p = $('gr-peek'); if (p && C.lyGrid) p.innerHTML = grPeekHTML(C.lyGrid); }); } });
document.addEventListener('change', e => {
  if (e.target.id === 'gr-group' && S.view === 'lyGrid') { UI.grGroup = e.target.value; grDraw(); A.grRefreshViews(); }
});

Object.assign(A, {
  grSrc: id => { if (!GR_SRC[id]) return; UI.grSrc = id; UI.grView = ''; UI.grBase = ''; grSetView(null); A.go('lyGrid'); },
  grView: id => { UI.grView = id; UI.grBase = id.slice(2); grSetView(null); render({ keep: true }); },
  grOpenView: id => { const v = (GR.mine?.views || []).find(x => x.id === id); if (!v) return; UI.grSrc = v.source; UI.grView = id; grSetView(v); A.go('lyGrid'); },
  grRefreshViews: () => { const sv = grSaved(), nav = document.querySelector('.gr-views'); if (!nav || !sv) return; const dirty = grDirty(sv), b = nav.querySelector('[data-act=grUpdView]'); if (dirty && !b) nav.insertAdjacentHTML('beforeend', `<button class="btn sm pri" data-act="grUpdView">Save changes to “${esc(sv.name)}”</button>`); if (!dirty && b) b.remove(); },
  grSaveView: () => {
    const b = grBuiltin(), name = b ? `${b[1]}${UI.grQ ? ' · ' + UI.grQ : ''}` : '';
    confirmModal('Save this view', `${esc(grSrc().name)}: its search, filters, sort, grouping and columns, kept for you.`, `<div class="fld"><label for="gr-vn">Name</label><input class="in" id="gr-vn" maxlength="60" value="${esc(name === 'All clients' ? '' : name)}" placeholder="e.g. HNI clients in Delhi"></div>`, 'Save view', async () => {
      const n = $('gr-vn').value.trim(); if (!n) throw new Error('Give the view a name.');
      const views = [...(GR.mine?.views || [])]; if (views.length >= 30) throw new Error('Keep at most 30 saved views: delete one first.');
      const id = 'v' + Date.now().toString(36), was = UI.grView;
      closeModal(); UI.grView = id;
      if (!await grPutViews([...views, grViewNow(id, n)], `Saved “${n}”`)) UI.grView = was;
    });
  },
  grUpdView: () => { const sv = grSaved(); if (!sv) return; grPutViews((GR.mine?.views || []).map(v => v.id === sv.id ? grViewNow(sv.id, sv.name) : v), `“${sv.name}” updated`); },
  grDelView: id => { const v = (GR.mine?.views || []).find(x => x.id === id); if (!v) return; confirmModal('Delete this view', `“${esc(v.name)}” is deleted for you. The data isn’t touched.`, '', 'Delete', async () => { closeModal(); if (UI.grView === id) { UI.grView = ''; grSetView(null); } await grPutViews((GR.mine?.views || []).filter(x => x.id !== id), `Deleted “${v.name}”`); }, true); },
  grSort: id => { UI.grSort = UI.grSort === id ? '-' + id : UI.grSort === '-' + id ? '' : id; grDraw(); A.grRefreshViews(); },
  grUnfilter: id => { const f = { ...UI.grF }; delete f[id]; UI.grF = f; render({ keep: true }); },
  grFilter: () => {
    const s = grSrc(), d = C.lyGrid, cols = s.cols.filter(c => ['text', 'list', 'mono', 'pill', 'bool', 'date'].includes(c[2]));
    const vals = k => { const c = grCol(k), m = new Map(); for (const r of d.rows) for (const v of c[2] === 'list' ? (grList(c, r).length ? grList(c, r) : ['']) : [grText(c, r)]) m.set(v, (m.get(v) || 0) + 1); return [...m].sort((a, b) => b[1] - a[1]).slice(0, 200); };
    const opts = k => vals(k).map(([v, n]) => `<option value="${esc(v)}">${esc(v || '(blank)')} · ${n}</option>`).join('');
    confirmModal('Add a filter', 'Keep the rows whose column has this value (for groups and segments, any of them).', `<div class="form"><div class="fld"><label for="gr-fc">Column</label><select class="in" id="gr-fc">${cols.map(c => `<option value="${c[0]}">${esc(c[1])}</option>`).join('')}</select></div><div class="fld"><label for="gr-fv">Value</label><select class="in" id="gr-fv">${opts(cols[0][0])}</select></div></div>`, 'Filter', async () => { UI.grF = { ...UI.grF, [$('gr-fc').value]: $('gr-fv').value }; GR.cur = 0; closeModal(); render({ keep: true }); });
    $('gr-fc').onchange = () => { $('gr-fv').innerHTML = opts($('gr-fc').value); };
  },
  grCols: () => {
    const s = grSrc(), on = grShown();
    confirmModal('Columns', `Choose the columns of ${esc(s.name)}, in this order.`, `<div class="gr-colpick">${s.cols.map(c => `<label class="ck"><input type="checkbox" data-grcol="${c[0]}" ${on.includes(c[0]) ? 'checked' : ''}> ${esc(c[1])}</label>`).join('')}</div><button class="linkish small" type="button" id="gr-coldef">Back to the standard columns</button>`, 'Show these', async () => {
      const ks = [...document.querySelectorAll('[data-grcol]')].filter(x => x.checked).map(x => x.dataset.grcol);
      if (!ks.length) throw new Error('Choose at least one column.');
      UI.grCols = ks.join() === s.def.join() ? null : ks; closeModal(); render({ keep: true });
    });
    $('gr-coldef').onclick = () => document.querySelectorAll('[data-grcol]').forEach(x => { x.checked = s.def.includes(x.dataset.grcol); });
  },
  grDensity: async () => { try { await api('PUT', '/v1/me/ui', { density: uiE().density === 'compact' ? 'comfortable' : 'compact' }); await loadMe(); render({ keep: true }); } catch (e) { toast(e.message, true); } },
  grExport: id => {
    const s = grSrc(), d = C.lyGrid; if (!d) return;
    let rows = GR.rows; if (id === 'sel') rows = rows.filter(r => UI.grSel.has(s.key(r)));
    const name = `${UI.grSrc}_${S.date}.csv`, url = URL.createObjectURL(new Blob([grCSV(rows, grShown().map(grCol))], { type: 'text/csv;charset=utf-8' }));
    const a = document.createElement('a'); a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(url), 4000);
    toast(`Exported ${lyPlural(rows.length, 'row')} to ${name}`);
  },
  grRow: (i, el, e) => { if (e?.target.closest('button,input,a,select')) return; GR.cur = +i; const r = GR.rows[GR.cur]; if (!r) return; const k = grSrc().key(r); UI.grPeek = UI.grPeek === k ? '' : k; grDraw(); },
  grOpenRow: () => { const s = grSrc(), r = C.lyGrid?.rows.find(x => s.key(x) === UI.grPeek); grOpen(r); },
  grPeekClose: () => { UI.grPeek = ''; grDraw(); },
  grSelOne: (k, el) => { el.checked ? UI.grSel.add(k) : UI.grSel.delete(k); grDraw(); },
  grSelAll: (id, el) => { const s = grSrc(); GR.rows.slice(0, GR.limit).forEach(r => el.checked ? UI.grSel.add(s.key(r)) : UI.grSel.delete(s.key(r))); grDraw(); },
  grClear: () => { UI.grSel.clear(); grDraw(); },
  grMore: () => { GR.limit += 500; grDraw(); },
  grBulk: async op => { UI.clientSel = new Set(UI.grSel); await A.bulkTag(op); if (!UI.clientSel.size) UI.grSel.clear(); },
});
