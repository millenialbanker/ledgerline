'use strict';
/* Ledgerline console: boards, boardviews. Other ways to show a board's
   cards, registered into BOARDS (boards.js):
     table      one row per card: sort, choose and order columns, group,
                edit inline, CSV of the rows shown
     calendar   month or week; cards on their due date (or start to due);
                drag to another day; a tray of cards with no dates
     timeline   bars from start to due in swimlanes by list, member or
                label; drag a bar to shift it, drag an end to change it
     dashboard  cards by list, label, member, due status and checklist
                progress; a bar shows only its cards in these views
   and, on the boards home, "My cards": the cards I am a member of across
   every board I can see (GET /v1/boards/my-cards, internal/ops/boardviews.go).
   Card dates are IST wall-clock, "YYYY-MM-DD" or "YYYY-MM-DDTHH:MM"; a date
   alone is due at the end of that day. Weeks run Monday to Sunday.
   There is no map view: it would need an internet map and geocoding
   service, which would send card locations outside the firm. */

Object.assign(UI, { bvPrefs: {}, bvCal: {}, bvDrill: null, bvMy: null, bvMyAt: 0, bvMyAll: {}, bvScroll: {}, bvOT: {}, bvDashEdit: null });

/* ---------- dates (IST, as day numbers since 1970-01-01) ---------- */
const BV_DAY = 864e5, BV_IST = 198e5;
const BV_CLOCK = { now: () => Date.now() }; // a harness may set it
const bvWall = () => BV_CLOCK.now() + BV_IST; // IST wall clock, as UTC milliseconds
const bvToday = () => new Date(bvWall()).toISOString().slice(0, 10);
const bvDN = s => { const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(s || ''); return m ? Math.round(Date.UTC(+m[1], +m[2] - 1, +m[3]) / BV_DAY) : NaN; };
const bvDS = n => new Date(n * BV_DAY).toISOString().slice(0, 10);
const bvTm = s => (/T(\d{2}:\d{2})/.exec(s || '') || [])[1] || '';
const bvShift = (s, n) => s ? bvDS(bvDN(s) + n) + (bvTm(s) ? 'T' + bvTm(s) : '') : s; // keeps the time
const bvAt = (s, start) => { const n = bvDN(s); if (isNaN(n)) return NaN; const t = bvTm(s); return n * BV_DAY + (t ? (+t.slice(0, 2) * 60 + +t.slice(3)) * 6e4 : start ? 0 : 1439 * 6e4); };
const bvWd = n => ((n % 7) + 10) % 7; // 0 Monday … 6 Sunday
const bvMonday = n => n - bvWd(n);
const bvMonthStart = n => bvDN(bvDS(n).slice(0, 8) + '01');
const bvNextMonth = n => { const s = bvDS(n); return Math.round(Date.UTC(+s.slice(0, 4), +s.slice(5, 7), 1) / BV_DAY); };
const BV_STO = ['overdue', 'soon', 'later', 'none', 'done'];
const BV_ST = { overdue: 'Overdue', soon: 'Due soon', later: 'Due later', none: 'No due date', done: 'Complete' };
// A card's due status: complete, overdue, due within 24 hours, later, or no due date.
const bvStatus = c => { if (c.due_done) return 'done'; const a = bvAt(c.due); if (isNaN(a)) return 'none'; const now = bvWall(); return a < now ? 'overdue' : a - now <= BV_DAY ? 'soon' : 'later'; };
const BV_MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'];
const BV_MON = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const BV_WDS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
const bvFmt = (s, o = {}) => { const n = bvDN(s); if (isNaN(n)) return ''; const d = new Date(n * BV_DAY), y = d.getUTCFullYear(), t = bvTm(s); return `${o.wd ? BV_WDS[bvWd(n)] + ' ' : ''}${d.getUTCDate()} ${BV_MON[d.getUTCMonth()]}${o.year || y !== +bvToday().slice(0, 4) ? ' ' + y : ''}${t && !o.noTime ? ' ' + t : ''}`; };
const bvStamp = s => { const t = Date.parse(s || ''); if (isNaN(t)) return ''; const w = new Date(t + BV_IST).toISOString(); return w.slice(0, 10) + 'T' + w.slice(11, 16); }; // an RFC 3339 time in IST
const bvRange = c => c.start && c.due ? `${bvFmt(c.start)} – ${bvFmt(c.due)}` : c.due ? 'Due ' + bvFmt(c.due) : c.start ? 'Starts ' + bvFmt(c.start) : 'No dates';
// Where a card sits on a calendar or timeline: from its start (or due) day to its due (or start) day.
function bvItems(cards) {
  const items = [], none = [];
  for (const c of cards) {
    const s = bvDN(c.start), d = bvDN(c.due);
    if (isNaN(s) && isNaN(d)) { none.push(c); continue; }
    const z = isNaN(d) ? s : d, a = isNaN(s) ? d : Math.min(s, z);
    items.push({ c, a, z, kind: isNaN(s) ? 'due' : isNaN(d) ? 'start' : 'span' });
  }
  items.sort((x, y) => x.a - y.a || (y.z - y.a) - (x.z - x.a) || (bvAt(x.c.due) || 0) - (bvAt(y.c.due) || 0) || x.c.no - y.c.no);
  return { items, none };
}
// What dragging a card on the calendar changes: from the tray, a due date on
// that day; otherwise both dates move by the days dragged, keeping their times.
function bvCalPatch(c, grab, drop) {
  if (grab == null) return c.due ? null : { due: bvDS(drop) };
  const d = drop - grab;
  if (!d) return null;
  const p = {};
  if (c.due) p.due = bvShift(c.due, d);
  if (c.start) p.start = bvShift(c.start, d);
  return p;
}
// What dragging a timeline bar changes: the whole bar moves both dates; its
// start end sets the start (a card with only a due date gets one); its due
// end sets the due date (a card with only a start gets one).
function bvTlPatch(c, h, d) {
  if (!d) return null;
  if (h === 'move') { const p = {}; if (c.start) p.start = bvShift(c.start, d); if (c.due) p.due = bvShift(c.due, d); return p; }
  if (h === 'start') return { start: c.start ? bvShift(c.start, d) : bvDS(bvDN(c.due) + d) };
  return { due: c.due ? bvShift(c.due, d) : bvDS(bvDN(c.start) + d) };
}
// How far a timeline end may be dragged: a bar keeps at least one day.
const bvTlClamp = (h, d, days) => h === 'start' ? Math.min(d, days - 1) : h === 'end' ? Math.max(d, 1 - days) : d;

/* ---------- shared ---------- */
const bvSvg = d => `<svg class="bv-i" viewBox="0 0 16 16" aria-hidden="true" focusable="false">${d}</svg>`;
const BV_SK = 'fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"';
const BV_I = {
  pen: bvSvg(`<path d="M10.6 2.9l2.5 2.5-7.3 7.3H3.3v-2.5z" ${BV_SK}/>`),
  left: bvSvg(`<path d="M10 3.5L5.5 8l4.5 4.5" ${BV_SK}/>`),
  right: bvSvg(`<path d="M6 3.5L10.5 8 6 12.5" ${BV_SK}/>`),
  up: bvSvg(`<path d="M4 10l4-4 4 4" ${BV_SK}/>`),
  down: bvSvg(`<path d="M4 6l4 4 4-4" ${BV_SK}/>`),
  sort: bvSvg(`<path d="M5.5 6.5L8 4l2.5 2.5M5.5 9.5L8 12l2.5-2.5" ${BV_SK}/>`),
  check: bvSvg(`<path d="M3.5 8.5l3 3 6-7" ${BV_SK} stroke-width="1.9"/>`),
  alert: bvSvg(`<circle cx="8" cy="8" r="6.2" ${BV_SK} stroke-width="1.5"/><path d="M8 4.9v3.6" ${BV_SK} stroke-width="1.7"/><circle cx="8" cy="11.1" r="1" fill="currentColor"/>`),
  clock: bvSvg(`<circle cx="8" cy="8" r="6.2" ${BV_SK} stroke-width="1.5"/><path d="M8 4.8V8l2.3 1.5" ${BV_SK} stroke-width="1.5"/>`),
  dl: bvSvg(`<path d="M8 2.5v7.3M4.9 6.9L8 10l3.1-3.1M3 13.2h10" ${BV_SK}/>`),
  cols: bvSvg(`<rect x="2.3" y="3" width="11.4" height="10" rx="1.6" ${BV_SK} stroke-width="1.4"/><path d="M6.1 3v10M9.9 3v10" ${BV_SK} stroke-width="1.4"/>`),
  x: bvSvg(`<path d="M4.5 4.5l7 7M11.5 4.5l-7 7" ${BV_SK}/>`),
  cal: bvSvg(`<rect x="2.5" y="3.5" width="11" height="10" rx="1.8" ${BV_SK} stroke-width="1.4"/><path d="M2.5 6.8h11M5.5 2.2v2.6M10.5 2.2v2.6" ${BV_SK} stroke-width="1.4"/>`),
  dash: bvSvg(`<circle cx="8" cy="8" r="6.2" ${BV_SK} stroke-width="1.5" stroke-dasharray="2.4 2.2"/>`)
};
const bvStIcon = st => ({ done: BV_I.check, overdue: BV_I.alert, soon: BV_I.clock }[st] || '');
const bvStKey = st => ({ done: BV_I.check, overdue: BV_I.alert, soon: BV_I.clock, later: BV_I.cal, none: BV_I.dash }[st]);
// Label colours (the board's colour names).
const BV_LC = { green: '#1F845A', yellow: '#CF9F02', orange: '#E56910', red: '#C9372C', purple: '#6E5DC6', blue: '#0C66E4', sky: '#227D9B', lime: '#5B7F24', pink: '#AE4787', black: '#626F86' };
const bvLName = l => l ? (l.name || (l.color ? l.color[0].toUpperCase() + l.color.slice(1) : 'Label')) : '';
const bvLChip = l => `<span class="bv-lb" style="--c:${BV_LC[l.color] || '#98A2B3'}">${esc(bvLName(l))}</span>`;
const bvUser = (M, id) => M.userById[id]?.name || id;
const bvAv = (M, id) => { const n = bvUser(M, id); return `<span class="bv-av" role="img" aria-label="${esc(n)}" title="${esc(n)}">${esc(initials(n))}</span>`; };
const bvLabels = (c, M) => (c.labels || []).map(id => M.labelById[id]).filter(Boolean);
const bvChecks = c => { let n = 0, d = 0; (c.checklists || []).forEach(cl => (cl.items || []).forEach(i => { n++; if (i.done) d++; })); return [d, n]; };
const bvCheckState = c => { const [d, n] = bvChecks(c); return !n ? 'nolist' : d === n ? 'all' : d ? 'some' : 'none'; };
const bvDue = c => { const st = bvStatus(c); return `<span class="bv-due bv-st-${st}" title="${BV_ST[st]}">${bvStIcon(st)}${esc(bvFmt(c.due))}<span class="bv-sr">, ${BV_ST[st]}</span></span>`; };
const bvSeg = (act, opts, cur, label) => `<div class="seg bv-seg" role="group" aria-label="${esc(label)}">${opts.map(([v, l]) => `<button class="${v === cur ? 'on' : ''}" aria-pressed="${v === cur}" data-act="${act}" data-id="${v}">${l}</button>`).join('')}</div>`;
const bvPlural = (n, w) => `${nf(n)} ${w}${n === 1 ? '' : 's'}`;

function bvMeta(ctx) {
  const b = ctx.board, all = b.lists || [], lists = all.filter(l => !l.archived).sort((x, y) => x.pos - y.pos);
  return {
    b, lists, listById: Object.fromEntries(all.map(l => [l.id, l])), listIdx: Object.fromEntries(lists.map((l, i) => [l.id, i])),
    labels: b.labels || [], labelById: Object.fromEntries((b.labels || []).map(l => [l.id, l])),
    users: ctx.users || [], userById: Object.fromEntries((ctx.users || []).map(u => [u.id, u])), fields: b.fields || [],
    edit: ['admin', 'member'].includes(ctx.role) && !b.closed
  };
}
const bvCard = (ctx, id) => (ctx.cards || []).find(c => c.id === id) || (ctx.all || []).find(c => c.id === id);

// Settings kept per board on this computer.
const BV_LS = 'ledgerline-bv:';
function bvPref(ctx) {
  const id = ctx.board.id;
  if (!UI.bvPrefs[id]) { let p = {}; try { p = JSON.parse(localStorage.getItem(BV_LS + id) || '{}') || {}; } catch (e) {} UI.bvPrefs[id] = p; }
  return UI.bvPrefs[id];
}
function bvPrefSet(ctx, part, patch) {
  const p = bvPref(ctx);
  p[part] = { ...(p[part] || {}), ...patch };
  try { localStorage.setItem(BV_LS + ctx.board.id, JSON.stringify(p)); } catch (e) {}
}

// A dashboard bar narrows these views to its cards until cleared.
const BV_DRILL = { list: 'List', label: 'Label', member: 'Member', due: 'Due status', checks: 'Checklist' };
const bvDrillOn = ctx => UI.bvDrill && UI.bvDrill.board === ctx.board.id ? UI.bvDrill : null;
function bvMatch(c, d, M) {
  switch (d.kind) {
    case 'list': return c.list === d.id;
    case 'label': return d.id ? (c.labels || []).includes(d.id) : !bvLabels(c, M).length;
    case 'member': return d.id ? (c.members || []).includes(d.id) : !(c.members || []).length;
    case 'due': return bvStatus(c) === d.id;
    case 'checks': return bvCheckState(c) === d.id;
  }
  return true;
}
function bvCards(ctx) { const d = bvDrillOn(ctx), cs = ctx.cards || []; if (!d) return cs; const M = bvMeta(ctx); return cs.filter(c => bvMatch(c, d, M)); }
function bvDrillBar(ctx) {
  const d = bvDrillOn(ctx);
  if (!d) return '';
  const n = bvCards(ctx).length;
  return `<div class="bv-drill" role="status"><span>Showing ${bvPlural(n, 'card')} where <b>${esc(BV_DRILL[d.kind])}</b> is <b>${esc(d.text)}</b></span><button class="btn sm" data-act="bvDrillClear">Show all cards</button></div>`;
}

// Draws the open view again in place.
function bvRerender() {
  const ctx = BOARDS.ctx(), root = $('bv-root');
  if (!ctx || !root) return;
  const v = BOARDS.views[root.dataset.view];
  if (!v) return;
  root.outerHTML = v.render(ctx);
  if (v.after) v.after(ctx);
}
// Keeps where a view's scroll box was scrolled to, so a redraw (here, or
// boards.js reloading the board) puts it back; first() places a new one.
function bvKeepScroll(root, first) {
  const sc = root.querySelector('.bv-scroll'), k = root.dataset.key;
  if (!sc || !k) return;
  const at = UI.bvScroll[k];
  if (at) { sc.scrollLeft = at[0]; sc.scrollTop = at[1]; } else if (first) first(sc);
  sc.addEventListener('scroll', () => { UI.bvScroll[k] = [sc.scrollLeft, sc.scrollTop]; }, { passive: true });
}
function bvApply(card, c) { Object.keys(card).forEach(k => { if (!(k in c)) delete card[k]; }); Object.assign(card, c); }
// Saves a change to a card: shown at once, sent to the server, put back if refused.
async function bvSave(card, patch, move) {
  const ctx = BOARDS.ctx();
  if (!ctx || !card || !patch) return;
  const same = [...new Set([...(ctx.all || []), ...(ctx.cards || [])].filter(x => x.id === card.id))], before = same.map(x => ({ ...x }));
  const local = move ? { list: patch.list } : patch;
  same.forEach(x => { for (const [k, v] of Object.entries(local)) { if (v === '' || v == null) delete x[k]; else x[k] = v; } x.updated_at = new Date(BV_CLOCK.now()).toISOString(); });
  bvRerender();
  const path = `/v1/boards/${encodeURIComponent(ctx.board.id)}/cards/${encodeURIComponent(card.id)}`;
  try {
    const c = move ? await post(path + '/move', { list: patch.list }) : await api('PATCH', path, patch);
    if (c && c.id) same.forEach(x => bvApply(x, c));
    bvRerender();
    BOARDS.reload();
  } catch (e) {
    same.forEach((x, i) => bvApply(x, before[i]));
    bvRerender();
    toast(e.message, true);
  }
}
function bvDownload(text, name) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' })), a = document.createElement('a');
  a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 2000);
}
const bvSlug = s => String(s || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 60) || 'board';
// Enter or Space on a card drawn as role=button opens it.
function bvKeyOpen(root, ctx) {
  root.addEventListener('keydown', e => {
    const el = e.target.closest?.('[role=button][data-card]');
    if (!el || e.target !== el || (e.key !== 'Enter' && e.key !== ' ')) return;
    e.preventDefault();
    BOARDS.openCard(ctx.board.id, el.dataset.card);
  });
}

/* ---------- table ---------- */
const bvCols = M => [['no', '#', 'Card number'], ['title', 'Card'], ['list', 'List'], ['labels', 'Labels'], ['members', 'Members'], ['start', 'Start'], ['due', 'Due'], ['checks', 'Checklist'], ['location', 'Location'],
  ...M.fields.map(f => ['f:' + f.id, f.name, f.name, f]), ['updated', 'Updated']].map(([id, label, name, field]) => ({ id, label, name: name || label, field }));
function bvTCols(ctx, M) {
  const all = bvCols(M), p = bvPref(ctx).table || {}, order = (p.order || []).filter(id => all.some(c => c.id === id));
  all.forEach(c => { if (!order.includes(c.id)) order.push(c.id); });
  const hidden = new Set((p.hidden || ['location']).filter(id => id !== 'title')); // Location starts hidden
  const sort = all.some(c => c.id === p.sort) ? p.sort : '';
  return { all, order, hidden, cols: order.filter(id => !hidden.has(id)).map(id => all.find(c => c.id === id)), sort, dir: p.dir === -1 ? -1 : 1, group: ['list', 'label', 'member', 'due'].includes(p.group) ? p.group : '' };
}
function bvSortVal(c, k, M) {
  switch (k) {
    case 'no': return c.no;
    case 'title': return (c.title || '').toLowerCase();
    case 'list': return M.listIdx[c.list] ?? 1e6;
    case 'labels': { const i = bvLabels(c, M).map(l => M.labels.indexOf(l)); return i.length ? Math.min(...i) : null; }
    case 'members': { const n = (c.members || []).map(m => bvUser(M, m).toLowerCase()).sort(); return n.length ? n[0] : null; }
    case 'start': case 'due': { const a = bvAt(c[k], k === 'start'); return isNaN(a) ? null : a; }
    case 'checks': { const [d, n] = bvChecks(c); return n ? d / n : null; }
    case 'updated': return c.updated_at || null;
    case 'location': return c.location ? c.location.toLowerCase() : null;
  }
  const f = M.fields.find(x => 'f:' + x.id === k), v = f && (c.fields || {})[f.id];
  if (v == null || v === '') return null;
  if (f.type === 'number') return parseFloat(v);
  if (f.type === 'list') return (f.options || []).indexOf(v);
  if (f.type === 'checkbox') return v === 'true' ? 0 : null;
  return String(v).toLowerCase();
}
// Empty values go last whichever way the column is sorted.
const bvCmp = (a, b, dir) => a == null ? (b == null ? 0 : 1) : b == null ? -1 : (typeof a === 'number' && typeof b === 'number' ? a - b : String(a).localeCompare(String(b), 'en-IN', { numeric: true })) * dir;
function bvGroupBy(cards, by, M) {
  if (!by) return [{ key: '', head: '', text: '', cards }];
  const g = new Map(), put = (k, c) => { if (!g.has(k)) g.set(k, []); g.get(k).push(c); };
  for (const c of cards) {
    if (by === 'list') put(c.list, c);
    else if (by === 'label') { const ls = bvLabels(c, M); (ls.length ? ls.map(l => l.id) : ['']).forEach(k => put(k, c)); }
    else if (by === 'member') ((c.members || []).length ? c.members : ['']).forEach(k => put(k, c));
    else put(bvStatus(c), c);
  }
  const keys = by === 'list' ? [...M.lists.map(l => l.id), ...[...g.keys()].filter(k => !(k in M.listIdx))]
    : by === 'label' ? [...M.labels.map(l => l.id), '']
    : by === 'member' ? [...[...g.keys()].filter(Boolean).sort((a, b) => bvUser(M, a).localeCompare(bvUser(M, b))), '']
    : BV_STO;
  return keys.filter(k => g.has(k)).map(k => {
    let head, text;
    if (by === 'list') { text = M.listById[k]?.name || 'Another list'; head = esc(text); }
    else if (by === 'label') { const l = M.labelById[k]; text = l ? bvLName(l) : 'No label'; head = l ? bvLChip(l) : 'No label'; }
    else if (by === 'member') { text = k ? bvUser(M, k) : 'No member'; head = k ? `${bvAv(M, k)}<span>${esc(text)}</span>` : 'No member'; }
    else { text = BV_ST[k]; head = `<span class="bv-due bv-st-${k}">${bvStIcon(k)}${esc(text)}</span>`; }
    return { key: k, head, text, cards: g.get(k) };
  });
}
function bvTModel(ctx) {
  const M = bvMeta(ctx), T = bvTCols(ctx, M), cards = bvCards(ctx).slice();
  if (T.sort) { const v = new Map(cards.map(c => [c.id, bvSortVal(c, T.sort, M)])); cards.sort((a, b) => bvCmp(v.get(a.id), v.get(b.id), T.dir) || a.no - b.no); }
  else cards.sort((a, b) => (M.listIdx[a.list] ?? 1e6) - (M.listIdx[b.list] ?? 1e6) || a.pos - b.pos || a.no - b.no);
  return { M, T, n: cards.length, groups: bvGroupBy(cards, T.group, M) };
}
const bvNone = '<span class="bv-none" aria-label="none">—</span>';
// A number field as entered, with Indian digit grouping (1250.50 → 1,250.50).
const bvNum = v => { const m = /^(-?)(\d+)(\.\d+)?$/.exec(String(v)); return m ? m[1] + nf(m[2]) + (m[3] || '') : String(v); };
function bvCell(c, col, M, sc) {
  const id = col.id, ed = M.edit, no = c.no;
  const btn = (inner, label) => ed ? `<button class="bv-cb" data-bv-edit="${esc(id)}" aria-label="${esc(label)}">${inner}</button>` : inner;
  switch (id) {
    case 'no': return `<td class="bv-c-no${sc}">${no}</td>`;
    case 'title': return `<td class="bv-c-title${sc}"><div class="bv-tt"><button class="bv-open" data-act="bvOpen" data-id="${esc(c.id)}">${esc(c.title)}</button>${ed ? `<button class="bv-pen" data-bv-edit="title" aria-label="Rename card ${no}" title="Rename">${BV_I.pen}</button>` : ''}</div>${c.link?.label ? `<div class="bv-sub">${esc(c.link.label)}</div>` : ''}</td>`;
    case 'list': { const n = M.listById[c.list]?.name || '—'; return `<td class="bv-c-list${sc}">${btn(`<span>${esc(n)}</span>${BV_I.down}`, `List of card ${no}: ${n}. Move the card`)}</td>`; }
    case 'labels': { const ls = bvLabels(c, M); return `<td class="bv-c-labels${sc}">${ls.length ? `<div class="bv-chips">${ls.map(bvLChip).join('')}</div>` : bvNone}</td>`; }
    case 'members': { const ms = c.members || []; return `<td class="bv-c-members${sc}">${ms.length ? `<div class="bv-avs">${ms.map(m => bvAv(M, m)).join('')}${ms.length === 1 ? `<span class="bv-avn">${esc(bvUser(M, ms[0]))}</span>` : ''}</div>` : bvNone}</td>`; }
    case 'start': case 'due': { const what = id === 'due' ? 'Due date' : 'Start date', v = c[id]; return `<td class="bv-c-date${sc}">${btn(!v ? bvNone : id === 'due' ? bvDue(c) : `<span class="bv-date">${esc(bvFmt(v))}</span>`, `${what} of card ${no}: ${v ? bvFmt(v) : 'none'}. Change it`)}</td>`; }
    case 'checks': { const [d, n] = bvChecks(c); return `<td class="bv-c-checks${sc}">${n ? `<span class="bv-ck${d === n ? ' all' : ''}" title="${d} of ${n} items done"><svg width="44" height="6" aria-hidden="true" focusable="false"><rect width="44" height="6" rx="3" class="bv-ck-tr"/>${d ? `<rect width="${Math.max(6, 44 * d / n).toFixed(1)}" height="6" rx="3" class="bv-ck-v"/>` : ''}</svg>${d}/${n}</span>` : bvNone}</td>`; }
    case 'updated': return `<td class="bv-c-upd${sc}">${esc(bvFmt(bvStamp(c.updated_at)))}</td>`;
    case 'location': return `<td class="bv-c-fld${sc}">${btn(c.location ? esc(c.location) : bvNone, `Location of card ${no}: ${c.location || 'none'}. Change it`)}</td>`;
  }
  const f = col.field, v = (c.fields || {})[f.id] ?? '';
  if (f.type === 'checkbox') return `<td class="bv-c-chk${sc}"><input type="checkbox" data-bv-chk="${esc(f.id)}" ${v === 'true' ? 'checked' : ''} ${ed ? '' : 'disabled'} aria-label="${esc(f.name)}, card ${no}"></td>`;
  const show = v === '' ? bvNone : f.type === 'number' ? esc(bvNum(v)) : f.type === 'date' ? esc(bvFmt(v)) : esc(v);
  return `<td class="${f.type === 'number' ? 'bv-c-num' : 'bv-c-fld'}${sc}">${btn(show, `${f.name} of card ${no}: ${v === '' ? 'empty' : v}. Change it`)}</td>`;
}
function bvTableHTML(ctx) {
  const { M, T, n, groups } = bvTModel(ctx), cols = T.cols, two = cols[0]?.id === 'no' && cols.length > 1;
  const sc = i => i === 0 ? ' bv-s1' : i === 1 && two ? ' bv-s2' : '';
  const head = cols.map((c, i) => {
    const on = T.sort === c.id;
    return `<th scope="col" class="bv-c-${c.id.startsWith('f:') ? (c.field.type === 'number' ? 'num' : 'fld') : c.id === 'start' || c.id === 'due' ? 'date' : c.id}${sc(i)}" aria-sort="${on ? (T.dir > 0 ? 'ascending' : 'descending') : 'none'}"><button class="bv-sort${on ? ' on' : ''}" data-act="bvSort" data-id="${esc(c.id)}" title="Sort by ${esc(c.name.toLowerCase())}">${c.id === 'no' ? '<span aria-hidden="true">#</span><span class="bv-sr">Card number</span>' : esc(c.label)}${on ? (T.dir > 0 ? BV_I.up : BV_I.down) : BV_I.sort}</button></th>`;
  }).join('');
  const body = groups.map(g => (T.group ? `<tr class="bv-grp"><th colspan="${cols.length}" scope="colgroup"><span class="bv-grp-in">${g.head}<span class="bv-ct">${g.cards.length}</span></span></th></tr>` : '') +
    g.cards.map(c => `<tr data-card="${esc(c.id)}">${cols.map((col, i) => bvCell(c, col, M, sc(i))).join('')}</tr>`).join('')).join('');
  const groupOpts = [['', 'Nothing'], ['list', 'List'], ['label', 'Label'], ['member', 'Member'], ['due', 'Due status']];
  return `<div class="bv bv-table" id="bv-root" data-view="table" data-key="${esc(ctx.board.id)}:table">
    <div class="bv-tools"><div class="bv-tools-l"><b class="bv-count">${bvPlural(n, 'card')}</b>${T.group && n ? `<span class="bv-dim">in ${bvPlural(groups.length, 'group')}${T.group === 'label' || T.group === 'member' ? '; a card with two shows under both' : ''}</span>` : ''}</div>
      <div class="bv-tools-r"><label class="bv-fld"><span>Group by</span><select class="in sm" id="bv-group">${groupOpts.map(([v, l]) => `<option value="${v}" ${v === T.group ? 'selected' : ''}>${l}</option>`).join('')}</select></label>
        <button class="btn sm" data-act="bvCols">${BV_I.cols}Columns</button><button class="btn sm" data-act="bvCsv" ${n ? '' : 'disabled'}>${BV_I.dl}Export CSV</button></div></div>
    ${bvDrillBar(ctx)}
    <div class="bv-scroll bv-tw" role="region" aria-label="Cards table" tabindex="0"><table class="bv-tbl"><thead><tr>${head}</tr></thead><tbody>${body || `<tr><td colspan="${cols.length}" class="bv-empty">No cards to show. Cards the filter leaves out are not listed.</td></tr>`}</tbody></table></div>
  </div>`;
}
function bvTableAfter(ctx) {
  const root = $('bv-root');
  if (!root) return;
  const M = bvMeta(ctx), t = root.querySelector('.bv-tbl'), s1 = t.querySelector('thead th.bv-s1');
  bvKeepScroll(root);
  if (s1 && t.querySelector('.bv-s2')) t.style.setProperty('--bv-l2', s1.getBoundingClientRect().width + 'px');
  root.querySelector('#bv-group').addEventListener('change', e => { bvPrefSet(ctx, 'table', { group: e.target.value }); bvRerender(); });
  t.addEventListener('click', e => {
    const ed = e.target.closest('[data-bv-edit]');
    if (ed) { e.stopPropagation(); if (M.edit) bvEdit(ed, ctx, M); return; }
    if (e.target.closest('button,input,select,a,label,.bv-ed')) return;
    const tr = e.target.closest('tr[data-card]');
    if (tr) BOARDS.openCard(ctx.board.id, tr.dataset.card);
  });
  t.addEventListener('change', e => {
    const f = e.target.dataset.bvChk;
    if (!f) return;
    const c = bvCard(ctx, e.target.closest('tr').dataset.card);
    if (c) bvSave(c, bvFieldsPatch(M, c, f, e.target.checked ? 'true' : ''));
  });
}
// The custom fields patch replaces them all, so it carries the others too.
function bvFieldsPatch(M, c, fid, v) {
  const m = {};
  for (const f of M.fields) { const x = (c.fields || {})[f.id]; if (x != null && x !== '') m[f.id] = x; }
  if (v === '' || v == null) delete m[fid]; else m[fid] = v;
  return { fields: m };
}
// Turns a cell into an editor; Enter or leaving it saves, Escape cancels.
function bvEdit(ed, ctx, M) {
  const td = ed.closest('td'), c = bvCard(ctx, ed.closest('tr').dataset.card), col = ed.dataset.bvEdit;
  if (!c || td.querySelector('.bv-ed')) return;
  const f = col.startsWith('f:') ? M.fields.find(x => 'f:' + x.id === col) : null, no = c.no;
  let html, read, onChange = false;
  if (col === 'title') {
    html = `<input class="in sm" value="${esc(c.title)}" maxlength="500" aria-label="Title of card ${no}">`;
    read = box => { const v = box.querySelector('input').value.trim(); return v && v !== c.title ? { title: v } : null; };
  } else if (col === 'start' || col === 'due') {
    const v = c[col] || '', what = col === 'due' ? 'Due' : 'Start';
    html = `<input class="in sm" type="date" value="${esc(v.slice(0, 10))}" aria-label="${what} date of card ${no}"><input class="in sm" type="time" value="${esc(bvTm(v))}" aria-label="${what} time (optional)"><button type="button" class="bv-ib bv-clear" aria-label="Remove the ${what.toLowerCase()} date">${BV_I.x}</button>`;
    read = box => { const [d, t] = box.querySelectorAll('input'), nv = d.value ? d.value + (t.value ? 'T' + t.value : '') : ''; return nv !== v ? { [col]: nv } : null; };
  } else if (col === 'location') {
    html = `<input class="in sm" type="text" maxlength="300" value="${esc(c.location || '')}" aria-label="Location of card ${no}">`;
    read = box => { const v = box.querySelector('input').value.trim(); return v !== (c.location || '') ? { location: v } : null; };
  } else if (col === 'list') {
    html = `<select class="in sm" aria-label="List of card ${no}">${M.lists.map(l => `<option value="${esc(l.id)}" ${l.id === c.list ? 'selected' : ''}>${esc(l.name)}</option>`).join('')}</select>`;
    read = box => { const v = box.querySelector('select').value; return v !== c.list ? { list: v } : null; };
    onChange = true;
  } else if (f) {
    const v = (c.fields || {})[f.id] ?? '';
    html = f.type === 'list' ? `<select class="in sm" aria-label="${esc(f.name)} of card ${no}"><option value="">—</option>${(f.options || []).map(o => `<option ${o === v ? 'selected' : ''}>${esc(o)}</option>`).join('')}</select>`
      : `<input class="in sm" ${f.type === 'number' ? 'type="text" inputmode="decimal"' : f.type === 'date' ? 'type="date"' : 'type="text" maxlength="500"'} value="${esc(f.type === 'date' ? v.slice(0, 10) : v)}" aria-label="${esc(f.name)} of card ${no}">`;
    read = box => { const nv = box.querySelector('input,select').value.trim().replace(f.type === 'number' ? /,/g : /^$/, ''); return nv !== v ? bvFieldsPatch(M, c, f.id, nv) : null; };
    onChange = f.type === 'list';
  } else return;
  td.innerHTML = `<div class="bv-ed">${html}</div>`;
  const box = td.firstChild, first = box.querySelector('input,select');
  let done = false;
  const fin = p => { if (done) return; done = true; if (p) bvSave(c, p, col === 'list'); else bvRerender(); };
  first.focus();
  if (first.select && first.type === 'text') first.select();
  box.addEventListener('keydown', e => {
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); fin(null); }
    else if (e.key === 'Enter') { e.preventDefault(); fin(read(box)); }
  });
  box.addEventListener('focusout', e => { if (!box.contains(e.relatedTarget)) setTimeout(() => { if (!box.contains(document.activeElement)) fin(read(box)); }, 0); });
  if (onChange) box.querySelector('select').addEventListener('change', () => fin(read(box)));
  const clr = box.querySelector('.bv-clear');
  if (clr) { clr.addEventListener('mousedown', e => e.preventDefault()); clr.addEventListener('click', () => fin(c[col] ? { [col]: '' } : null)); }
}
function bvCsvText(c, col, M) {
  switch (col.id) {
    case 'no': return String(c.no);
    case 'title': return c.title || '';
    case 'list': return M.listById[c.list]?.name || '';
    case 'labels': return bvLabels(c, M).map(bvLName).join('; ');
    case 'members': return (c.members || []).map(m => bvUser(M, m)).join('; ');
    case 'start': case 'due': return (c[col.id] || '').replace('T', ' ');
    case 'checks': { const [d, n] = bvChecks(c); return n ? `${d} of ${n}` : ''; }
    case 'updated': return bvStamp(c.updated_at).replace('T', ' ');
    case 'location': return c.location || '';
  }
  const v = (c.fields || {})[col.field.id] ?? '';
  return col.field.type === 'checkbox' ? (v === 'true' ? 'Yes' : '') : v;
}
// A cell that a spreadsheet would read as a formula is written as text.
const bvCsvCell = v => { let s = String(v ?? ''); if (/^[=+\-@\t\r]/.test(s) && !/^-?\d+(\.\d+)?$/.test(s)) s = "'" + s; return /[",\r\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s; };
function bvCsv(ctx) {
  const { M, T, groups } = bvTModel(ctx), cols = T.cols;
  const rows = [[...(T.group ? ['Group'] : []), ...cols.flatMap(c => c.id === 'due' ? ['Due', 'Due status'] : [c.name])]];
  groups.forEach(g => g.cards.forEach(c => rows.push([...(T.group ? [g.text] : []), ...cols.flatMap(col => col.id === 'due' ? [bvCsvText(c, col, M), c.due || c.due_done ? BV_ST[bvStatus(c)] : ''] : [bvCsvText(c, col, M)])])));
  return '﻿' + rows.map(r => r.map(bvCsvCell).join(',')).join('\r\n') + '\r\n';
}
function bvColsModal() {
  const ctx = BOARDS.ctx();
  if (!ctx) return;
  const T = bvTCols(ctx, bvMeta(ctx)), last = T.order.length - 1;
  modal(mhd('Columns', 'Choose the table’s columns and their order. Kept for this board on this computer.') + `<div class="bd"><ul class="bv-colpick">${T.order.map((id, i) => {
    const c = T.all.find(x => x.id === id), nm = esc(c.name);
    return `<li><label class="check"><input type="checkbox" data-act="bvColShow" data-id="${esc(id)}" ${T.hidden.has(id) ? '' : 'checked'} ${id === 'title' ? 'disabled' : ''}> <span>${nm}${id === 'title' ? ' <span class="bv-dim">(always shown)</span>' : ''}</span></label>
      <span class="bv-pair"><button class="btn sm bv-ib" data-act="bvColMove" data-id="${esc(id)}" data-d="-1" ${i ? '' : 'disabled'} aria-label="Move ${nm} up">${BV_I.up}</button><button class="btn sm bv-ib" data-act="bvColMove" data-id="${esc(id)}" data-d="1" ${i < last ? '' : 'disabled'} aria-label="Move ${nm} down">${BV_I.down}</button></span></li>`;
  }).join('')}</ul></div><div class="ft"><button class="btn" data-act="bvColReset">Reset</button><button class="btn pri" data-act="close">Done</button></div>`);
}

/* ---------- calendar ---------- */
function bvCalState(ctx) {
  const s = UI.bvCal[ctx.board.id] ||= { at: bvToday() };
  s.mode = (bvPref(ctx).cal || {}).mode === 'week' ? 'week' : 'month';
  return s;
}
function bvCalRange(s) {
  const at = bvDN(s.at);
  if (s.mode === 'week') { const a = bvMonday(at); return { a, z: a + 6, weeks: 1, first: a, last: a + 6 }; }
  const first = bvMonthStart(at), last = bvNextMonth(at) - 1, a = bvMonday(first), weeks = Math.ceil((last - a + 1) / 7);
  return { a, z: a + weeks * 7 - 1, weeks, first, last };
}
function bvCalTitle(s, R) {
  if (s.mode === 'month') { const d = bvDS(R.first); return `${BV_MONTHS[+d.slice(5, 7) - 1]} ${d.slice(0, 4)}`; }
  const a = bvDS(R.a), z = bvDS(R.z), sameM = a.slice(0, 7) === z.slice(0, 7), sameY = a.slice(0, 4) === z.slice(0, 4);
  return `${+a.slice(8)}${sameM ? '' : ' ' + BV_MON[+a.slice(5, 7) - 1]}${sameY ? '' : ' ' + a.slice(0, 4)} – ${+z.slice(8)} ${BV_MON[+z.slice(5, 7) - 1]} ${z.slice(0, 4)}`;
}
function bvEvHTML(ev, w0, M, week) {
  const c = ev.c, st = bvStatus(c), cls = (ev.a < w0 ? ' bv-l' : '') + (ev.z > w0 + 6 ? ' bv-r' : '');
  const t = ev.kind === 'start' ? 'Starts' : bvTm(c.due) && ev.z <= w0 + 6 ? bvTm(c.due) : '', lab = `${c.title}, card ${c.no}, ${bvRange(c)}, ${BV_ST[st]}`;
  return `<div class="bv-ev bv-st-${st} bv-k-${ev.kind}${cls}" role="button" tabindex="0" ${M.edit ? 'draggable="true"' : ''} data-card="${esc(c.id)}"${c._board ? ` data-board="${esc(c._board)}"` : ''} style="grid-column:${ev.s + 1}/${ev.e + 2};grid-row:${ev.lane + 1}" title="${esc(lab)}" aria-label="${esc(lab)}">${bvStIcon(st)}${t ? `<b>${t}</b>` : ''}<span>${esc(c.title)}</span>${week && M.listById[c.list] ? `<small>${esc(M.listById[c.list].name)}</small>` : ''}</div>`;
}
// One week: the days, then the cards laid in lanes so spans do not overlap.
function bvWeekHTML(w0, items, M, o) {
  const today = bvDN(bvToday()), evs = items.filter(x => x.z >= w0 && x.a <= w0 + 6).map(x => ({ ...x, s: Math.max(x.a, w0) - w0, e: Math.min(x.z, w0 + 6) - w0 }));
  const ends = [];
  evs.forEach(ev => { let i = ends.findIndex(end => end < ev.s); if (i < 0) { i = ends.length; ends.push(0); } ends[i] = ev.e; ev.lane = i; });
  const max = o.week ? Infinity : 3, more = [0, 0, 0, 0, 0, 0, 0];
  evs.forEach(ev => { if (ev.lane >= max) for (let d = ev.s; d <= ev.e; d++) more[d]++; });
  const days = BV_WDS.map((wd, i) => {
    const n = w0 + i, ds = bvDS(n), dd = +ds.slice(8), out = n < o.first || n > o.last;
    return `<div class="bv-day${n === today ? ' bv-today' : ''}${out ? ' bv-out' : ''}${i > 4 ? ' bv-we' : ''}" data-dn="${n}"><span class="bv-dn">${o.week ? `<small>${wd}</small>` : ''}<b>${dd}</b>${dd === 1 && !o.week ? ` <small>${BV_MON[+ds.slice(5, 7) - 1]}</small>` : ''}${n === today ? '<span class="bv-sr"> (today)</span>' : ''}</span></div>`;
  }).join('');
  const shown = evs.filter(ev => ev.lane < max).map(ev => bvEvHTML(ev, w0, M, o.week)).join('');
  const lanes = Math.min(ends.length, max);
  const mores = more.map((m, d) => m ? `<button class="bv-more" data-act="bvCalMore" data-id="${bvDS(w0 + d)}" style="grid-column:${d + 1};grid-row:${lanes + 1}" aria-label="${m} more on ${esc(bvFmt(bvDS(w0 + d), { wd: 1 }))}; show the week">+${m} more</button>` : '').join('');
  return `<div class="bv-wk${o.week ? ' bv-wk-full' : ''}" data-w0="${w0}"><div class="bv-wk-bg">${days}</div><div class="bv-evs">${shown}${mores}</div></div>`;
}
/* The week view: the days by hour. A card due at a time sits at that hour
   (from its start, when it starts that day at a time); cards due on a day
   with no time, spanning days or with only a start sit in the all-day row. */
const BV_HH = 44; // pixels an hour
const bvMin = s => { const t = bvTm(s); return t ? +t.slice(0, 2) * 60 + +t.slice(3) : NaN; };
const bvHHMM = m => `${String(Math.floor(m / 60)).padStart(2, '0')}:${String(m % 60).padStart(2, '0')}`;
function bvTimedSpan(x) {
  const c = x.c, d = bvMin(c.due);
  if (isNaN(d) || x.z !== x.a || x.kind === 'start') return null;
  const s = bvMin(c.start), from = c.start && bvDN(c.start) === x.z && !isNaN(s) && s < d ? s : d;
  return [from, Math.min(1440, Math.max(from + 60, d))]; // a due time alone shows as an hour, room for its title
}
function bvHourWeekHTML(w0, items, M, o) {
  const today = bvDN(bvToday()), timed = [], allday = [];
  items.filter(x => x.z >= w0 && x.a <= w0 + 6).forEach(x => { const t = bvTimedSpan(x); if (t) timed.push({ ...x, t0: t[0], t1: t[1] }); else allday.push(x); });
  const evs = allday.map(x => ({ ...x, s: Math.max(x.a, w0) - w0, e: Math.min(x.z, w0 + 6) - w0 })), ends = [];
  evs.forEach(ev => { let i = ends.findIndex(end => end < ev.s); if (i < 0) { i = ends.length; ends.push(0); } ends[i] = ev.e; ev.lane = i; });
  const head = BV_WDS.map((wd, i) => { const n = w0 + i; return `<div class="bv-hw-dh${n === today ? ' bv-today' : ''}${i > 4 ? ' bv-we' : ''}"><small>${wd}</small><b>${+bvDS(n).slice(8)}</b>${n === today ? '<span class="bv-sr"> (today)</span>' : ''}</div>`; }).join('');
  const bg = BV_WDS.map((_, i) => `<div class="bv-day${w0 + i === today ? ' bv-today' : ''}${i > 4 ? ' bv-we' : ''}" data-dn="${w0 + i}"></div>`).join('');
  const cols = BV_WDS.map((_, i) => {
    const n = w0 + i, xs = timed.filter(x => x.z === n).sort((p, q) => p.t0 - q.t0 || q.t1 - p.t1);
    // Cards at the same time sit side by side.
    let group = [], end = -1;
    const flush = () => { const k = Math.max(...group.map(g => g.col)) + 1; group.forEach(g => { g.cols = k; }); group = []; };
    xs.forEach(x => { if (x.t0 >= end && group.length) flush(); const used = group.filter(g => g.t1 > x.t0).map(g => g.col); let c = 0; while (used.includes(c)) c++; x.col = c; group.push(x); end = Math.max(end, x.t1); });
    if (group.length) flush();
    const cards = xs.map(x => {
      const c = x.c, st = bvStatus(c), lab = `${c.title}, card ${c.no}, ${bvRange(c)}, ${BV_ST[st]}`, h = (x.t1 - x.t0) / 60 * BV_HH;
      return `<div class="bv-ev bv-hev bv-st-${st}${h < 30 ? ' bv-short' : ''}" role="button" tabindex="0" ${M.edit ? 'draggable="true"' : ''} data-card="${esc(c.id)}"${c._board ? ` data-board="${esc(c._board)}"` : ''} style="top:${x.t0 / 60 * BV_HH}px;height:${Math.max(20, h - 2)}px;left:calc(${x.col / x.cols * 100}% + 2px);width:calc(${100 / x.cols}% - 4px)" title="${esc(lab)}" aria-label="${esc(lab)}"><b>${bvStIcon(st)}${bvMin(c.due) === x.t0 ? bvHHMM(x.t0) : `${bvHHMM(x.t0)}–${bvHHMM(x.t1)}`}</b><span>${esc(c.title)}</span>${M.listById[c.list] && h >= 64 ? `<small>${esc(M.listById[c.list].name)}</small>` : ''}</div>`;
    }).join('');
    const now = n === today ? `<i class="bv-hw-now" style="top:${(bvWall() % BV_DAY) / 36e5 * BV_HH}px" aria-hidden="true"></i>` : '';
    return `<div class="bv-hw-col${i > 4 ? ' bv-we' : ''}${n === today ? ' bv-today' : ''}" data-dn="${n}">${now}${cards}</div>`;
  }).join('');
  const hours = [...Array(24)].map((_, h) => `<span style="top:${h * BV_HH}px">${h ? bvHHMM(h * 60) : ''}</span>`).join('');
  return `<div class="bv-hw"><div class="bv-scroll bv-hw-scroll" tabindex="-1"><div class="bv-hw-top"><div class="bv-hw-head" aria-hidden="true"><span class="bv-hw-g"></span>${head}</div>
    <div class="bv-hw-ad-row"><span class="bv-hw-g">All day</span><div class="bv-wk bv-hw-ad" data-w0="${w0}"><div class="bv-wk-bg">${bg}</div><div class="bv-evs">${evs.map(ev => bvEvHTML(ev, w0, M, false)).join('')}</div></div></div></div>
    <div class="bv-hw-body" style="height:${24 * BV_HH}px;--bv-hh:${BV_HH}px"><div class="bv-hw-hours" aria-hidden="true">${hours}</div><div class="bv-hw-cols" data-w0="${w0}">${cols}<i class="bv-hw-slot" hidden></i></div></div></div></div>`;
}
// Where the week view scrolls to first: 08:00, or the earliest card before it.
function bvHourFirst(items, w0) {
  let m = 8 * 60;
  items.forEach(x => { if (x.z >= w0 && x.a <= w0 + 6) { const t = bvTimedSpan(x); if (t) m = Math.min(m, t[0]); } });
  return sc => { sc.scrollTop = Math.max(0, m / 60 * BV_HH - 10); };
}
// The month or week grid, and the phone's list of days.
function bvCalGrid(items, R, wk, M) {
  const weeks = wk ? bvHourWeekHTML(R.a, items, M, {}) : [...Array(R.weeks)].map((_, i) => bvWeekHTML(R.a + i * 7, items, M, { week: false, first: R.first, last: R.last })).join('');
  return `<div class="bv-grid${wk ? ' bv-grid-wk' : ''}"><div class="bv-wds" aria-hidden="true">${BV_WDS.map(d => `<span>${d}</span>`).join('')}</div>${weeks}</div>${bvAgendaHTML(items, R, M)}`;
}
// On a phone the calendar is a list of days with their cards.
function bvAgendaHTML(items, R, M) {
  const a = R.first, z = R.last, today = bvDN(bvToday()), by = new Map();
  items.filter(x => x.z >= a && x.a <= z).forEach(x => { const d = x.z <= z ? x.z : x.a >= a ? x.a : a; if (!by.has(d)) by.set(d, []); by.get(d).push(x); });
  const days = [...by.keys()].sort((p, q) => p - q);
  if (!days.length) return `<p class="bv-agenda bv-empty">No cards are due in this ${R.weeks === 1 ? 'week' : 'month'}.</p>`;
  return `<ol class="bv-agenda" aria-label="Cards by day">${days.map(d => `<li class="bv-agd${d === today ? ' bv-today' : ''}"><h4>${esc(bvFmt(bvDS(d), { wd: 1, year: false }))}${d === today ? ' <span class="bv-tag">Today</span>' : ''}</h4><ul>${by.get(d).map(x => {
    const c = x.c, st = bvStatus(c);
    return `<li><div class="bv-agc bv-st-${st}" role="button" tabindex="0" data-card="${esc(c.id)}"><span class="bv-agc-t">${bvStIcon(st)}<span>${esc(c.title)}</span></span><span class="bv-agc-m">${esc(M.listById[c.list]?.name || '')} · ${esc(bvRange(c))}<span class="bv-sr">, ${BV_ST[st]}</span></span></div></li>`;
  }).join('')}</ul></li>`).join('')}</ol>`;
}
function bvCalHTML(ctx) {
  const M = bvMeta(ctx), s = bvCalState(ctx), R = bvCalRange(s), { items, none } = bvItems(bvCards(ctx)), wk = s.mode === 'week';
  const inView = items.filter(x => x.z >= R.first && x.a <= R.last).length;
  return `<div class="bv bv-cal" id="bv-root" data-view="calendar"${wk ? ` data-key="${esc(ctx.board.id)}:calweek" data-w0="${R.a}"` : ''}>
    <div class="bv-tools"><div class="bv-tools-l"><button class="btn sm" data-act="bvCalGo" data-id="0">Today</button><span class="bv-pair"><button class="btn sm bv-ib" data-act="bvCalGo" data-id="-1" aria-label="Previous ${s.mode}">${BV_I.left}</button><button class="btn sm bv-ib" data-act="bvCalGo" data-id="1" aria-label="Next ${s.mode}">${BV_I.right}</button></span>
      <h3 class="bv-title" aria-live="polite">${bvCalTitle(s, R)}</h3><span class="bv-dim bv-small">${bvPlural(inView, 'card')} · times in IST</span></div>
      <div class="bv-tools-r">${bvSeg('bvCalMode', [['month', 'Month'], ['week', 'Week']], s.mode, 'Calendar period')}</div></div>
    ${bvDrillBar(ctx)}
    <div class="bv-cal-wrap"><div class="bv-cal-main">${bvCalGrid(items, R, wk, M)}</div>
      <aside class="bv-tray" aria-label="Cards with no dates"><h4>No dates <span class="bv-ct">${none.length}</span></h4><p class="bv-dim bv-small">${M.edit ? `Drag a card onto a day${wk ? ', or an hour,' : ''} to give it a due date.` : 'Cards with no start or due date.'}</p>
        <div class="bv-tray-l">${none.map(c => `<div class="bv-ev bv-trayc bv-st-none" role="button" tabindex="0" ${M.edit ? 'draggable="true"' : ''} data-card="${esc(c.id)}" title="${esc(c.title)}" aria-label="${esc(c.title)}, card ${c.no}, no dates"><span>${esc(c.title)}</span><small>${esc(M.listById[c.list]?.name || '')}</small></div>`).join('') || '<p class="bv-dim bv-small">Every card shown has a date.</p>'}</div></aside></div>
  </div>`;
}
// What dropping a card on an hour of the week view changes: its due date
// and time (a card from the tray gets one; a card with only a start moves
// its start). A start moves with the due date by the days moved.
function bvCalPatchAt(c, grab, day, mins) {
  const at = bvDS(day) + 'T' + bvHHMM(mins);
  if (grab == null) return c.due ? null : { due: at };
  if (!c.due) return at === c.start ? null : { start: at };
  if (at === c.due) return null;
  const p = { due: at }, dd = day - bvDN(c.due);
  if (c.start && dd) p.start = bvShift(c.start, dd);
  return p;
}
function bvCalAfter(ctx) {
  const root = $('bv-root');
  if (!root) return;
  const M = bvMeta(ctx), w0 = +root.dataset.w0;
  root.addEventListener('click', e => { const el = e.target.closest('[data-card]'); if (el && !e.target.closest('[data-act]')) BOARDS.openCard(ctx.board.id, el.dataset.card); });
  bvKeyOpen(root, ctx);
  if (root.dataset.key) bvKeepScroll(root, bvHourFirst(bvItems(bvCards(ctx)).items, w0));
  if (!M.edit) return;
  let drag = null;
  const dayAt = (wk, x) => { const r = wk.getBoundingClientRect(); return +wk.dataset.w0 + Math.max(0, Math.min(6, Math.floor((x - r.left) / (r.width / 7)))); };
  // On the hour grid: the day under the pointer and the half hour it is in.
  const slotAt = (cols, x, y) => { const r = cols.getBoundingClientRect(); return { day: dayAt(cols, x), mins: Math.max(0, Math.min(1410, Math.floor((y - r.top) / BV_HH * 2) * 30)) }; };
  const slot = root.querySelector('.bv-hw-slot');
  const mark = (n, at) => {
    root.querySelectorAll('.bv-day,.bv-hw-col').forEach(d => d.classList.toggle('bv-over', !at && +d.dataset.dn === n));
    if (!slot) return;
    slot.hidden = !at;
    if (at) { slot.style.top = at.mins / 60 * BV_HH + 'px'; slot.style.left = (at.day - w0) / 7 * 100 + '%'; slot.textContent = bvHHMM(at.mins); }
  };
  const over = e => e.target.closest?.('.bv-wk,.bv-hw-cols');
  root.addEventListener('dragstart', e => {
    const el = e.target.closest?.('[data-card][draggable=true]');
    if (!el) return;
    const wk = el.closest('.bv-wk'), col = el.closest('.bv-hw-col');
    drag = { id: el.dataset.card, grab: col ? +col.dataset.dn : wk ? dayAt(wk, e.clientX) : null, el };
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', el.dataset.card);
    setTimeout(() => { el.classList.add('bv-dragging'); root.classList.add('bv-isdrag'); });
  });
  const stop = () => { if (drag) drag.el.classList.remove('bv-dragging'); drag = null; mark(NaN); root.classList.remove('bv-isdrag'); };
  root.addEventListener('dragend', stop);
  root.addEventListener('dragover', e => {
    const box = over(e);
    if (!drag || !box) return;
    e.preventDefault(); e.dataTransfer.dropEffect = 'move';
    if (box.classList.contains('bv-hw-cols')) mark(NaN, slotAt(box, e.clientX, e.clientY)); else mark(dayAt(box, e.clientX));
  });
  root.addEventListener('drop', e => {
    const box = over(e);
    if (!drag || !box) return;
    e.preventDefault();
    const c = bvCard(ctx, drag.id), grab = drag.grab;
    let p = null;
    if (box.classList.contains('bv-hw-cols')) { const at = slotAt(box, e.clientX, e.clientY); p = c && bvCalPatchAt(c, grab, at.day, at.mins); }
    else p = c && bvCalPatch(c, grab, dayAt(box, e.clientX));
    stop();
    if (p) bvSave(c, p);
  });
}

/* ---------- timeline ---------- */
let bvClickHold = 0;
const BV_ZPX = { day: 36, week: 14, month: 4 }, BV_ROW = 30;
function bvTlState(ctx) { const p = bvPref(ctx).tl || {}; return { zoom: BV_ZPX[p.zoom] ? p.zoom : 'week', lane: ['list', 'member', 'label', 'none'].includes(p.lane) ? p.lane : 'list' }; }
// The days shown: every dated card and today, with room either side, at
// most three years around today.
function bvTlRange(items, zoom) {
  const t = bvDN(bvToday());
  let a = t, z = t;
  for (const x of items) { a = Math.min(a, x.a); z = Math.max(z, x.z); }
  const pad = { day: 10, week: 28, month: 75 }[zoom];
  a = Math.max(a - pad, t - 550); z = Math.min(z + pad, t + 550);
  return zoom === 'month' ? [bvMonthStart(a), bvNextMonth(z) - 1] : [bvMonday(a), bvMonday(z) + 6];
}
function bvTlScale(a, z, zoom, px) {
  const seg = (s, e, label, cls = '') => `<span class="bv-tl-seg ${cls}" style="left:${(s - a) * px}px;width:${(e - s) * px}px"><span>${label}</span></span>`;
  let top = '', bot = '';
  for (let n = a; n <= z;) {
    const ds = bvDS(n), e = Math.min(z + 1, zoom === 'month' ? bvDN(`${+ds.slice(0, 4) + 1}-01-01`) : bvNextMonth(n));
    top += seg(n, e, zoom === 'month' ? ds.slice(0, 4) : `${BV_MONTHS[+ds.slice(5, 7) - 1]} ${ds.slice(0, 4)}`);
    n = e;
  }
  const t = bvDN(bvToday());
  if (zoom === 'day') for (let n = a; n <= z; n++) bot += seg(n, n + 1, `<small>${BV_WDS[bvWd(n)][0]}</small>${+bvDS(n).slice(8)}`, `bv-tl-d${bvWd(n) > 4 ? ' we' : ''}${n === t ? ' now' : ''}`);
  else if (zoom === 'week') for (let n = a; n <= z; n += 7) bot += seg(n, n + 7, `${+bvDS(n).slice(8)} ${BV_MON[+bvDS(n).slice(5, 7) - 1]}`);
  else for (let n = a; n <= z;) { const e = bvNextMonth(n); bot += seg(n, e, BV_MON[+bvDS(n).slice(5, 7) - 1]); n = e; }
  return `<div class="bv-tl-top">${top}</div><div class="bv-tl-bot">${bot}</div>`;
}
function bvTlLanes(items, M, by) {
  const lanes = [], ofMember = id => items.filter(x => (x.c.members || []).includes(id));
  if (by === 'none') {
    if (items.length) lanes.push({ head: '<span>All cards</span>', items });
  } else if (by === 'list') {
    M.lists.forEach(l => lanes.push({ head: `<span>${esc(l.name)}</span>`, items: items.filter(x => x.c.list === l.id) }));
    const other = items.filter(x => !(x.c.list in M.listIdx));
    if (other.length) lanes.push({ head: '<span>Another list</span>', items: other });
  } else if (by === 'member') {
    [...new Set(items.flatMap(x => x.c.members || []))].sort((p, q) => bvUser(M, p).localeCompare(bvUser(M, q))).forEach(id => lanes.push({ head: `${bvAv(M, id)}<span>${esc(bvUser(M, id))}</span>`, items: ofMember(id) }));
    const no = items.filter(x => !(x.c.members || []).length);
    if (no.length) lanes.push({ head: '<span>No member</span>', items: no });
  } else {
    M.labels.forEach(l => { const xs = items.filter(x => (x.c.labels || []).includes(l.id)); if (xs.length) lanes.push({ head: bvLChip(l), items: xs }); });
    const no = items.filter(x => !bvLabels(x.c, M).length);
    if (no.length) lanes.push({ head: '<span>No label</span>', items: no });
  }
  return lanes;
}
// A lane's bars, packed into rows; a title too long for its bar sits after it.
function bvTlLaneHTML(lane, a, z, px, M) {
  const ends = [];
  const bars = lane.items.filter(x => x.z >= a && x.a <= z).map(x => {
    const x1 = (Math.max(x.a, a) - a) * px, w = (Math.min(x.z, z) + 1 - a) * px - x1, inside = w >= 110;
    const tw = inside ? 0 : Math.min(240, 12 + (x.c.title || '').length * 6.6);
    let r = ends.findIndex(e => e <= x1);
    if (r < 0) { r = ends.length; ends.push(0); }
    ends[r] = x1 + w + tw + 10;
    return { x, x1, w, r, inside };
  });
  const bar = b => {
    const c = b.x.c, st = bvStatus(c), top = b.r * BV_ROW + 6, lab = `${c.title}, card ${c.no}, ${bvRange(c)}, ${BV_ST[st]}`;
    return `<div class="bv-tbar bv-st-${st} bv-k-${b.x.kind}${b.w < 40 ? ' bv-narrow' : ''}" role="button" tabindex="0" data-card="${esc(c.id)}" style="left:${b.x1}px;top:${top}px;width:${b.w}px" title="${esc(lab)}" aria-label="${esc(lab)}">${M.edit ? '<span class="bv-h bv-h-s" data-h="start"></span>' : ''}${bvStIcon(st)}<span class="bv-tbar-t">${b.inside ? esc(c.title) : ''}</span>${M.edit ? '<span class="bv-h bv-h-e" data-h="end"></span>' : ''}</div>` +
      (b.inside ? '' : `<span class="bv-tout" data-open="${esc(c.id)}" style="left:${b.x1 + b.w + 6}px;top:${top}px" aria-hidden="true">${esc(c.title)}</span>`);
  };
  return `<div class="bv-tl-lane"><div class="bv-tl-name">${lane.head}<span class="bv-ct">${lane.items.length}</span></div><div class="bv-tl-track" style="height:${Math.max(1, ends.length) * BV_ROW + 12}px">${bars.map(bar).join('')}</div></div>`;
}
function bvTlHTML(ctx) {
  const M = bvMeta(ctx), s = bvTlState(ctx), px = BV_ZPX[s.zoom], { items, none } = bvItems(bvCards(ctx)), [a, z] = bvTlRange(items, s.zoom);
  const W = (z - a + 1) * px, tx = Math.round((bvDN(bvToday()) - a + (bvWall() % BV_DAY) / BV_DAY) * px);
  const lanes = bvTlLanes(items, M, s.lane), laneOpts = [['list', 'List'], ['label', 'Label'], ['member', 'Member'], ['none', 'Nothing']];
  let grid = '';
  if (s.zoom === 'month') for (let n = bvNextMonth(a); n <= z; n = bvNextMonth(n)) grid += `<i class="bv-tl-m" style="left:${(n - a) * px}px"></i>`;
  return `<div class="bv bv-tline" id="bv-root" data-view="timeline" ${M.edit ? 'data-edit' : ''} data-px="${px}" data-tx="${tx}" data-key="${esc(ctx.board.id)}:timeline:${s.zoom}">
    <div class="bv-tools"><div class="bv-tools-l"><button class="btn sm" data-act="bvTlToday">Today</button>${bvSeg('bvTlZoom', [['day', 'Days'], ['week', 'Weeks'], ['month', 'Months']], s.zoom, 'Zoom')}<span class="bv-dim bv-small">${bvPlural(items.length, 'card')} with dates · IST</span></div>
      <div class="bv-tools-r">${s.lane === 'label' || s.lane === 'member' ? `<span class="bv-dim bv-small">A card with two ${s.lane}s shows in each</span>` : ''}<label class="bv-fld"><span>Group by</span><select class="in sm" id="bv-lane">${laneOpts.map(([v, l]) => `<option value="${v}" ${v === s.lane ? 'selected' : ''}>${l}</option>`).join('')}</select></label></div></div>
    ${bvDrillBar(ctx)}
    <div class="bv-scroll bv-tl" role="region" aria-label="Timeline" tabindex="0" style="--px:${px}px">
      <div class="bv-tl-in" style="width:calc(var(--bv-lw) + ${W}px)">
        <div class="bv-tl-head"><div class="bv-tl-corner">${s.lane === 'none' ? 'Cards' : laneOpts.find(o => o[0] === s.lane)[1]}</div><div class="bv-tl-scale z-${s.zoom}" style="width:${W}px">${bvTlScale(a, z, s.zoom, px)}<span class="bv-now-h" style="left:${tx}px">Today</span></div></div>
        <div class="bv-tl-body"><div class="bv-tl-bg z-${s.zoom}" style="width:${W}px" aria-hidden="true">${grid}<i class="bv-now" style="left:${tx}px"></i></div>
          ${lanes.map(l => bvTlLaneHTML(l, a, z, px, M)).join('') || `<div class="bv-tl-lane"><div class="bv-tl-name"><span class="bv-dim">No cards</span></div><div class="bv-tl-track" style="height:${BV_ROW + 12}px"></div></div>`}
          <div class="bv-tip" hidden></div></div>
      </div>
    </div>
    <section class="bv-undated" aria-label="Cards without dates"><h4>Cards without dates <span class="bv-ct">${none.length}</span></h4>${none.length ? `<ul>${none.map(c => `<li><button class="bv-chipbtn" data-act="bvOpen" data-id="${esc(c.id)}"><span class="bv-dim">${c.no}</span> ${esc(c.title)}</button></li>`).join('')}</ul>${M.edit ? '<p class="bv-dim bv-small">Open a card to give it dates, or drag it onto a day on the Calendar.</p>' : ''}` : '<p class="bv-dim bv-small">Every card shown has a start or due date.</p>'}</section>
  </div>`;
}
function bvTlAfter(ctx) {
  const root = $('bv-root'), sc = root?.querySelector('.bv-scroll');
  if (!sc) return;
  const M = bvMeta(ctx), px = +root.dataset.px, tip = root.querySelector('.bv-tip');
  const lw = () => root.querySelector('.bv-tl-corner').getBoundingClientRect().width;
  bvKeepScroll(root, s => { s.scrollLeft = Math.max(0, +root.dataset.tx - (s.clientWidth - lw()) / 3); }); // a new one opens on today
  root.querySelector('#bv-lane').addEventListener('change', e => { bvPrefSet(ctx, 'tl', { lane: e.target.value }); bvRerender(); });
  bvKeyOpen(root, ctx);
  let st = null;
  root.addEventListener('click', e => {
    if (Date.now() < bvClickHold) return; // the click that ends a drag
    const el = e.target.closest('.bv-tbar[data-card],.bv-tout[data-open]');
    if (el) BOARDS.openCard(ctx.board.id, el.dataset.card || el.dataset.open);
  });
  if (!M.edit) return;
  const dates = (c, p) => bvRange({ start: p.start ?? c.start, due: p.due ?? c.due });
  root.addEventListener('pointerdown', e => {
    const bar = e.target.closest('.bv-tbar');
    if (!bar || e.button > 0) return;
    st = { bar, id: bar.dataset.card, h: e.target.closest('[data-h]')?.dataset.h || 'move', x0: e.clientX, left: parseFloat(bar.style.left), w: parseFloat(bar.style.width), d: 0, moved: false, pid: e.pointerId };
  });
  root.addEventListener('pointermove', e => {
    if (!st || e.pointerId !== st.pid) return;
    const dx = e.clientX - st.x0;
    if (!st.moved) {
      if (Math.abs(dx) < 5) return;
      st.moved = true;
      try { st.bar.setPointerCapture(st.pid); } catch (er) {}
      st.bar.classList.add('bv-drag');
      const out = st.bar.nextElementSibling;
      if (out?.classList.contains('bv-tout')) out.hidden = true;
    }
    const d = bvTlClamp(st.h, Math.round(dx / px), Math.round(st.w / px));
    st.d = d;
    if (st.h !== 'end') st.bar.style.left = st.left + d * px + 'px';
    if (st.h === 'start') st.bar.style.width = st.w - d * px + 'px';
    if (st.h === 'end') st.bar.style.width = st.w + d * px + 'px';
    const c = bvCard(ctx, st.id), r = st.bar.getBoundingClientRect(), br = tip.parentElement.getBoundingClientRect();
    tip.textContent = c ? dates(c, bvTlPatch(c, st.h, d) || {}) : '';
    tip.hidden = false;
    tip.style.left = r.left - br.left + 'px';
    tip.style.top = r.top - br.top - 30 + 'px';
  });
  const end = e => {
    if (!st || e.pointerId !== st.pid) return;
    const s = st;
    st = null;
    tip.hidden = true;
    if (!s.moved) return;
    bvClickHold = Date.now() + 150;
    const c = bvCard(ctx, s.id), p = c && e.type === 'pointerup' && bvTlPatch(c, s.h, s.d);
    if (p) bvSave(c, p); else bvRerender();
  };
  root.addEventListener('pointerup', end);
  root.addEventListener('pointercancel', end);
}

/* ---------- dashboard ---------- */
// Horizontal bars: name, bar, count; each is a button that shows its cards.
function bvBars(rows, d, empty) {
  if (!rows.length) return `<p class="bv-dim bv-small">${empty}</p>`;
  const max = Math.max(1, ...rows.map(r => r.n));
  return `<ul class="bv-hbars">${rows.map(r => {
    const on = d && d.kind === r.kind ? (d.id === r.id ? ' on' : ' off') : '', w = (r.n / max).toFixed(4);
    const key = r.icon ? `<span class="bv-hb-ic" style="color:${r.color}">${r.icon}</span>` : r.av || (r.sw ? `<i class="bv-sw" style="background:${r.color}"></i>` : '');
    return `<li><button class="bv-hb${on}" data-act="bvDrill" data-id="${esc(r.kind)}" data-v="${esc(r.id)}" data-t="${esc(r.label)}" aria-pressed="${on === ' on'}" aria-label="${esc(r.label)}: ${bvPlural(r.n, 'card')}. ${on === ' on' ? 'Shown; press to show all cards' : 'Show these cards'}">
      <span class="bv-hb-l">${key}<span class="bv-hb-n">${esc(r.label)}</span></span>
      <span class="bv-hb-t">${r.n ? `<svg class="bv-hb-b" style="width:calc((100% - 40px) * ${w})" height="14" aria-hidden="true" focusable="false"><rect width="100%" height="14" rx="4" style="fill:${r.color}"/><rect width="4" height="14" style="fill:${r.color}"/></svg>` : ''}<b>${nf(r.n)}</b></span></button></li>`;
  }).join('')}</ul>`;
}
const bvTile = (title, sub, body, cls = '') => `<section class="bv-tile ${cls}"><header><h4>${title}</h4>${sub ? `<p>${sub}</p>` : ''}</header>${body}</section>`;
/* Tiles: what each shows and how it is drawn, kept on the board
   (settings "views.tiles"; the server checks them). */
const BV_TDATA = { list: 'Cards per list', label: 'Cards per label', member: 'Cards per member', due: 'Due status', checks: 'Checklist completion', time: 'Cards over time' };
const BV_TCHARTS = { list: ['bar', 'pie'], label: ['bar', 'pie'], member: ['bar', 'pie'], due: ['bar', 'pie'], checks: ['bar', 'pie'], time: ['line'] };
const BV_CHART = { bar: 'Bar', pie: 'Pie', line: 'Line' };
const BV_TILES0 = ['list', 'due', 'label', 'member', 'checks'].map(k => ({ id: k, data: k, chart: 'bar' }));
const BV_CATS = 8; // categorical colours, --bv-c1 … --bv-c8, in this order; more fold into "Other"
function bvTilesOf(b) {
  let ts = null;
  try { ts = JSON.parse((b.settings || {})['views.tiles'] || 'null'); } catch (e) {}
  if (!Array.isArray(ts)) return BV_TILES0.map(t => ({ ...t }));
  return ts.filter(t => t && BV_TCHARTS[t.data]?.includes(t.chart)).slice(0, 12).map(t => ({ id: String(t.id || ''), data: t.data, chart: t.chart }));
}
const bvDashEditing = ctx => UI.bvDashEdit && UI.bvDashEdit.board === ctx.board.id ? UI.bvDashEdit : null;

// A tile's rows: one per list, label, member, due status or checklist state.
function bvDashRows(ctx, M, d, data) {
  const all = ctx.cards || [], cards = bvCards(ctx);
  // The tile whose bar was pressed keeps every bar, the chosen one marked; the others show only its cards.
  const base = kind => d && d.kind === kind ? all : cards, count = (cs, f) => cs.reduce((n, c) => n + (f(c) ? 1 : 0), 0);
  if (data === 'list') {
    const rows = M.lists.map((l, i) => ({ kind: 'list', id: l.id, label: l.name, color: 'var(--bv-later)', cat: i, n: count(base('list'), c => c.list === l.id) }));
    return { rows, sub: bvPlural(rows.reduce((s, r) => s + r.n, 0), 'card'), empty: 'No lists.' };
  }
  if (data === 'label') {
    const lb = base('label'), rows = M.labels.map(l => ({ kind: 'label', id: l.id, label: bvLName(l), color: BV_LC[l.color] || 'var(--bv-none)', sw: true, n: count(lb, c => (c.labels || []).includes(l.id)) })).filter(r => r.n || (d && d.kind === 'label' && d.id === r.id));
    const no = count(lb, c => !bvLabels(c, M).length);
    if (no) rows.push({ kind: 'label', id: '', label: 'No label', color: 'var(--bv-none)', sw: true, n: no });
    return { rows, sub: 'A card with two labels counts under both', empty: 'No cards.' };
  }
  if (data === 'member') {
    const mb = base('member'), ids = [...new Set(mb.flatMap(c => c.members || []))], order = ids.slice().sort((p, q) => bvUser(M, p).localeCompare(bvUser(M, q)));
    const rows = ids.map(id => ({ kind: 'member', id, label: bvUser(M, id), color: 'var(--bv-later)', cat: order.indexOf(id), av: bvAv(M, id), n: count(mb, c => (c.members || []).includes(id)) })).sort((p, q) => q.n - p.n || p.label.localeCompare(q.label));
    const no = count(mb, c => !(c.members || []).length);
    if (no) rows.push({ kind: 'member', id: '', label: 'No member', color: 'var(--bv-none)', n: no, grey: true });
    return { rows, sub: 'A card with two members counts under both', empty: 'No cards.' };
  }
  if (data === 'due') return { rows: BV_STO.map(k => ({ kind: 'due', id: k, label: BV_ST[k], color: `var(--bv-${k})`, icon: bvStKey(k), n: count(base('due'), c => bvStatus(c) === k) })), sub: 'Due soon: within 24 hours', empty: '' };
  const cb = base('checks');
  let items = 0, done = 0;
  cb.forEach(c => { const [x, n] = bvChecks(c); items += n; done += x; });
  const rows = [['all', 'All items done', 'var(--bv-done)'], ['some', 'In progress', 'var(--bv-later)'], ['none', 'Not started', 'var(--bv-none)']].map(([id, label, color]) => ({ kind: 'checks', id, label, color, sw: true, n: count(cb, c => bvCheckState(c) === id) }));
  return { rows, items, done, pc: items ? Math.round(done / items * 100) : 0, noList: count(cb, c => bvCheckState(c) === 'nolist'), sub: items ? `${nf(done)} of ${bvPlural(items, 'item')} done` : 'No checklist items yet', empty: '' };
}

// A pie (a ring): each slice and its legend line show their cards; past
// eight slices the smallest fold into "Other". The legend gives every
// number in words, and the ring's label reads them out.
function bvDonut(rows, d, o = {}) {
  let rs = rows.filter(r => r.n > 0);
  const total = rs.reduce((s, r) => s + r.n, 0);
  if (!total) return `<p class="bv-dim bv-small">${o.empty || 'No cards.'}</p>`;
  if (o.cat) { // lists and members: categorical colours, by the thing's own order (not its count)
    const keep = rs.length > BV_CATS ? rs.slice().sort((p, q) => q.n - p.n).slice(0, BV_CATS - 1) : rs;
    const other = rs.filter(r => !keep.includes(r));
    let i = 0;
    rs = rs.filter(r => keep.includes(r)).map(r => ({ ...r, color: r.grey ? 'var(--bv-none)' : `var(--bv-c${(r.cat ?? i++) % BV_CATS + 1})` }));
    if (other.length) rs.push({ kind: '', id: '', label: `Other (${other.length})`, color: 'var(--bv-none)', n: other.reduce((s, r) => s + r.n, 0), other: true });
  }
  const R = 42, C = 2 * Math.PI * R, gap = rs.length > 1 ? 2 : 0;
  let at = 0;
  const pct = n => Math.round(n / total * 100);
  const segs = rs.map(r => {
    const len = r.n / total * C, on = d && d.kind === r.kind && d.id === r.id;
    const s = `<circle class="bv-slice${on ? ' on' : ''}${d && d.kind === r.kind && !on ? ' off' : ''}" r="${R}" cx="60" cy="60" style="stroke:${r.color}" stroke-dasharray="${Math.max(0.5, len - gap).toFixed(2)} ${(C - Math.max(0.5, len - gap)).toFixed(2)}" stroke-dashoffset="${(-at).toFixed(2)}"${r.other ? '' : ` data-act="bvDrill" data-id="${esc(r.kind)}" data-v="${esc(r.id)}" data-t="${esc(r.label)}"`}><title>${esc(r.label)}: ${bvPlural(r.n, 'card')} (${pct(r.n)}%)</title></circle>`;
    at += len;
    return s;
  }).join('');
  const said = rs.map(r => `${r.label} ${r.n}`).join(', ');
  const legend = rs.map(r => {
    const on = d && d.kind === r.kind ? (d.id === r.id ? ' on' : ' off') : '';
    const key = r.icon ? `<span class="bv-hb-ic" style="color:${r.color}">${r.icon}</span>` : `<i class="bv-sw" style="background:${r.color}"></i>`;
    const inner = `${key}<span class="bv-lg-n">${esc(r.label)}</span><b>${nf(r.n)}</b><span class="bv-lg-p">${pct(r.n)}%</span>`;
    return `<li>${r.other ? `<span class="bv-lg">${inner}</span>` : `<button class="bv-lg${on}" data-act="bvDrill" data-id="${esc(r.kind)}" data-v="${esc(r.id)}" data-t="${esc(r.label)}" aria-pressed="${on === ' on'}" aria-label="${esc(r.label)}: ${bvPlural(r.n, 'card')}, ${pct(r.n)}%. ${on === ' on' ? 'Shown; press to show all cards' : 'Show these cards'}">${inner}</button>`}</li>`;
  }).join('');
  return `<div class="bv-pie"><div class="bv-ring"><svg viewBox="0 0 120 120" width="132" height="132" role="img" aria-label="${esc(o.title || 'Pie chart')}: ${esc(said)}"><circle class="bv-ring-tr" r="${R}" cx="60" cy="60"/>${segs}</svg>
    <span class="bv-ring-c" aria-hidden="true"><b>${o.center ?? nf(total)}</b><small>${o.centerSub ?? (total === 1 ? 'card' : 'cards')}</small></span></div><ul class="bv-legend">${legend}</ul></div>`;
}

// Cards over time: from the server, kept a minute (or until the board changes).
function bvOT(ctx) {
  const id = ctx.board.id, s = UI.bvOT[id];
  if (s && (s.busy || (s.rev === ctx.rev && Date.now() - s.at < 60000))) return s;
  UI.bvOT[id] = { busy: true, rev: ctx.rev, at: Date.now(), data: s?.data };
  get(`/v1/boards/${encodeURIComponent(id)}/views/over-time`)
    .then(r => { UI.bvOT[id] = { rev: ctx.rev, at: Date.now(), data: r }; })
    .catch(e => { UI.bvOT[id] = { rev: ctx.rev, at: Date.now(), error: e.message || 'Could not be loaded.' }; })
    .finally(() => { const c = BOARDS.ctx(); if (c && c.board.id === id && $('bv-root')?.dataset.view === 'dashboard') bvRerender(); });
  return UI.bvOT[id];
}
const bvNice = v => { const p = Math.pow(10, Math.floor(Math.log10(Math.max(1, v)))); return [1, 2, 5, 10].map(m => m * p).find(x => x >= v) || v; };
const BV_OTS = [['added', 'Added', 'var(--bv-acc)'], ['done', 'Completed', 'var(--bv-done)']];
// Two lines, added and completed per week. The plot stretches with the
// tile; its words are HTML so they keep their size. Hovering a week shows
// its numbers, and the table below has them all.
function bvLineHTML(s) {
  if (!s || (!s.data && s.busy)) return '<p class="bv-dim bv-small" aria-busy="true">Loading…</p>';
  if (!s.data) return `<p class="note bad">${esc(s.error || 'Could not be loaded.')}</p>`;
  const W = s.data.weeks || [], n = W.length || 1, max = bvNice(Math.max(1, ...W.flatMap(w => [w.added, w.done])));
  const X = i => (i + 0.5) / n * 100, Y = v => 100 - v / max * 100, wk = w => bvFmt(w.week, { year: false });
  const lines = BV_OTS.map(([k, , c]) => `<polyline points="${W.map((w, i) => `${X(i).toFixed(2)},${Y(w[k]).toFixed(2)}`).join(' ')}" style="stroke:${c}" vector-effect="non-scaling-stroke"/>`).join('');
  const pts = BV_OTS.map(([k, , c]) => W.map((w, i) => `<i class="bv-ln-pt" style="left:${X(i)}%;top:${Y(w[k])}%;--c:${c}"></i>`).join('')).join('');
  const cols = W.map((w, i) => `<span class="bv-ln-col" style="left:${i / n * 100}%;width:${100 / n}%"><span class="bv-ln-tip${i > n / 2 ? ' r' : ''}"><b>Week of ${esc(wk(w))}</b>${BV_OTS.map(([k, l, c]) => `<span><i class="bv-sw" style="background:${c}"></i>${l}: ${nf(w[k])}</span>`).join('')}</span></span>`).join('');
  // Every other week is named (the last always); on a phone every fourth.
  const xl = W.map((w, i) => (n - 1 - i) % 2 ? '' : `<span class="${(n - 1 - i) % 4 ? 'bv-ln-x2' : ''}" style="left:${X(i)}%">${esc(wk(w))}</span>`).join('');
  const sum = k => W.reduce((t, w) => t + w[k], 0);
  return `<div class="bv-line"><ul class="bv-legend bv-legend-row">${BV_OTS.map(([k, l, c]) => `<li><span class="bv-lg"><i class="bv-sw bv-sw-ln" style="background:${c}"></i><span class="bv-lg-n">${l}</span><b>${nf(sum(k))}</b><span class="bv-lg-p">in 12 weeks</span></span></li>`).join('')}</ul>
    <div class="bv-ln-plot" aria-hidden="true">
      <div class="bv-ln-area"><span class="bv-ln-y" style="top:0">${nf(max)}</span>${max % 2 ? '' : `<span class="bv-ln-y" style="top:50%">${nf(max / 2)}</span>`}<span class="bv-ln-y" style="top:100%">0</span><svg viewBox="0 0 100 100" preserveAspectRatio="none"><line x1="0" x2="100" y1="0" y2="0" vector-effect="non-scaling-stroke"/><line x1="0" x2="100" y1="50" y2="50" vector-effect="non-scaling-stroke"/><line class="bv-ln-base" x1="0" x2="100" y1="100" y2="100" vector-effect="non-scaling-stroke"/>${lines}</svg>${pts}${cols}</div>
      <div class="bv-ln-x">${xl}</div></div>
    <details class="bv-ln-tbl"><summary>Show the numbers</summary><table class="bv-mini"><caption class="bv-sr">Cards added and completed each week</caption><thead><tr><th scope="col">Week of</th><th scope="col">Added</th><th scope="col">Completed</th></tr></thead><tbody>${W.map(w => `<tr><th scope="row">${esc(bvFmt(w.week, { year: true }))}</th><td>${nf(w.added)}</td><td>${nf(w.done)}</td></tr>`).join('')}</tbody></table></details>
    ${s.data.capped ? '<p class="bv-dim bv-small">Only the newest 25,000 changes were read, so the first weeks may be short.</p>' : ''}</div>`;
}

// One tile: its chart, or in edit mode its controls too.
function bvTileHTML(ctx, M, d, t, i, n, ed) {
  const title = BV_TDATA[t.data], hid = `bv-th-${i}`;
  let body, sub;
  if (t.data === 'time') { sub = 'Cards added and marked complete each week, Monday to Sunday'; body = bvLineHTML(bvOT(ctx)); }
  else {
    const R = bvDashRows(ctx, M, d, t.data);
    sub = R.sub;
    if (t.chart === 'pie') body = bvDonut(R.rows, d, { title: `${title}, pie chart`, empty: R.empty || 'No cards.', cat: t.data === 'list' || t.data === 'member', ...(t.data === 'checks' && R.items ? { center: R.pc + '%', centerSub: 'items done' } : {}) });
    else body = bvBars(R.rows, d, R.empty);
    if (t.data === 'checks') body = (t.chart === 'bar' ? `<div class="bv-hero"><b>${R.pc}%</b><span>of checklist items done</span></div>
        <svg class="bv-meter" width="100%" height="8" role="img" aria-label="${R.pc}% of checklist items done"><rect width="100%" height="8" rx="4" class="bv-meter-tr"/>${R.done ? `<rect width="${Math.max(1, R.pc)}%" height="8" rx="4" style="fill:var(--bv-done)"/>` : ''}</svg>` : '') + body + (R.noList ? `<p class="bv-dim bv-small">${bvPlural(R.noList, 'card')} with no checklist.</p>` : '');
  }
  const controls = ed ? `<div class="bv-tedit" role="group" aria-label="Tile ${i + 1} of ${n}: ${esc(title)}">
      <label class="bv-fld"><span>Shows</span><select class="in sm" data-bv-tile="${i}" aria-label="What tile ${i + 1} shows">${Object.entries(BV_TDATA).map(([k, l]) => `<option value="${k}" ${k === t.data ? 'selected' : ''}>${l}</option>`).join('')}</select></label>
      ${BV_TCHARTS[t.data].length > 1 ? `<div class="seg bv-seg" role="group" aria-label="Chart">${BV_TCHARTS[t.data].map(c => `<button class="${c === t.chart ? 'on' : ''}" aria-pressed="${c === t.chart}" data-act="bvTileChart" data-id="${i}" data-v="${c}">${BV_CHART[c]}</button>`).join('')}</div>` : `<span class="bv-dim bv-small">${BV_CHART[t.chart]} chart</span>`}
      <span class="bv-tedit-r"><span class="bv-pair"><button class="btn sm bv-ib" data-act="bvTileMove" data-id="${i}" data-d="-1" ${i ? '' : 'disabled'} aria-label="Move ${esc(title)} earlier">${BV_I.left}</button><button class="btn sm bv-ib" data-act="bvTileMove" data-id="${i}" data-d="1" ${i < n - 1 ? '' : 'disabled'} aria-label="Move ${esc(title)} later">${BV_I.right}</button></span>
      <button class="btn sm bv-ib" data-act="bvTileDel" data-id="${i}" aria-label="Remove the ${esc(title)} tile">${BV_I.x}</button></span></div>` : '';
  return `<section class="bv-tile${ed ? ' bv-tile-ed' : ''}" aria-labelledby="${hid}" data-tile="${i}"><header><h4 id="${hid}">${title}</h4>${sub ? `<p>${sub}</p>` : ''}</header>${controls}${body}</section>`;
}

function bvDashHTML(ctx) {
  const M = bvMeta(ctx), d = bvDrillOn(ctx), cards = bvCards(ctx), E = bvDashEditing(ctx), ed = !!E && M.edit;
  const tiles = ed ? E.tiles : bvTilesOf(ctx.board), count = (cs, f) => cs.reduce((n, c) => n + (f(c) ? 1 : 0), 0);
  const stat = (label, n, kind, id, icon, tone) => `<button class="bv-stat${tone && n ? ' ' + tone : ''}${d && d.kind === kind && d.id === id ? ' on' : ''}" data-act="${kind ? 'bvDrill' : 'bvDrillClear'}" data-id="${kind}" data-v="${id}" data-t="${esc(label)}" aria-label="${esc(label)}: ${nf(n)}"><small>${icon || ''}${esc(label)}</small><b>${nf(n)}</b></button>`;
  const ofList = c => M.listById[c.list]?.name || '';
  const drilled = d ? bvTile(`${bvPlural(cards.length, 'card')} where ${esc(BV_DRILL[d.kind].toLowerCase())} is ${esc(d.text)}`, '', cards.length ? `<ul class="bv-dlist">${cards.slice(0, 60).map(c => `<li><button class="bv-dl" data-act="bvOpen" data-id="${esc(c.id)}"><span class="bv-dl-no">${c.no}</span><span class="bv-dl-t">${esc(c.title)}</span><span class="bv-dl-m">${esc(ofList(c))}</span>${c.due ? bvDue(c) : '<span></span>'}</button></li>`).join('')}</ul>${cards.length > 60 ? `<p class="bv-dim bv-small">and ${nf(cards.length - 60)} more: the Table view lists them all.</p>` : ''}` : '<p class="bv-dim bv-small">No cards.</p>', 'bv-wide') : '';
  const bar = ed ? `<div class="bv-dedit" role="region" aria-label="Editing the dashboard"><span><b>Editing the dashboard.</b> Add, remove and reorder tiles; each shows a bar or pie chart, or cards over time as a line. Saved for everyone on this board.</span>
      <span class="bv-dedit-b"><button class="btn sm" data-act="bvTileAdd" ${tiles.length >= 12 ? 'disabled title="A dashboard has at most 12 tiles"' : ''}>Add tile</button><button class="btn sm" data-act="bvTileReset">Default tiles</button><button class="btn sm" data-act="bvDashCancel">Cancel</button><button class="btn sm pri" data-act="bvDashSave">Save dashboard</button></span></div>`
    : M.edit ? `<div class="bv-tools"><div class="bv-tools-l"></div><div class="bv-tools-r"><button class="btn sm" data-act="bvDashEdit">${BV_I.pen}Edit dashboard</button></div></div>` : '';
  return `<div class="bv bv-dash" id="bv-root" data-view="dashboard">
    ${bar}${bvDrillBar(ctx)}
    <div class="bv-stats">${stat('Cards', cards.length, '', '', '', '')}${stat('Overdue', count(cards, c => bvStatus(c) === 'overdue'), 'due', 'overdue', BV_I.alert, 'bad')}${stat('Due soon', count(cards, c => bvStatus(c) === 'soon'), 'due', 'soon', BV_I.clock, 'warn')}${stat('Complete', count(cards, c => bvStatus(c) === 'done'), 'due', 'done', BV_I.check, 'good')}</div>
    ${drilled}
    <div class="bv-tiles">${tiles.map((t, i) => bvTileHTML(ctx, M, d, t, i, tiles.length, ed)).join('') || `<p class="bv-my-empty">No tiles. ${M.edit ? (ed ? 'Add a tile to chart this board’s cards.' : 'Choose <b>Edit dashboard</b> to add some.') : ''}</p>`}</div>
  </div>`;
}
async function bvDashSave() {
  const ctx = BOARDS.ctx(), E = ctx && bvDashEditing(ctx);
  if (!E) return;
  try {
    const r = await api('PUT', `/v1/boards/${encodeURIComponent(ctx.board.id)}/views/tiles`, E.reset ? { reset: true } : { tiles: E.tiles });
    ctx.board.settings = { ...(ctx.board.settings || {}), 'views.tiles': JSON.stringify(r.tiles) };
    if (E.reset) delete ctx.board.settings['views.tiles'];
    UI.bvDashEdit = null;
    bvRerender();
    toast('Dashboard saved');
    BOARDS.reload();
  } catch (e) { toast(e.message, true); }
}
// The tile's controls keep focus when the dashboard is drawn again.
function bvDashFocus(sel) { setTimeout(() => document.querySelector('#bv-root ' + sel)?.focus(), 0); }
document.addEventListener('change', e => {
  const i = e.target.dataset?.bvTile;
  if (i == null) return;
  const ctx = BOARDS.ctx(), E = ctx && bvDashEditing(ctx), t = E?.tiles[+i];
  if (!t) return;
  t.data = e.target.value;
  if (!BV_TCHARTS[t.data].includes(t.chart)) t.chart = BV_TCHARTS[t.data][0];
  E.reset = false;
  bvRerender(); bvDashFocus(`[data-bv-tile="${i}"]`);
});

/* ---------- my cards (boards home) ---------- */
const BV_WHEN = [['overdue', 'Overdue'], ['today', 'Today'], ['week', 'This week'], ['later', 'Later'], ['none', 'No due date']];
async function bvMyLoad() {
  UI.bvMyBusy = true;
  try { UI.bvMy = await get('/v1/boards/my-cards'); } catch (e) { UI.bvMy = { error: e.message || 'Your cards could not be loaded.' }; }
  UI.bvMyBusy = false;
  UI.bvMyAt = Date.now();
  return UI.bvMy;
}
const bvMyPaint = () => { const el = $('bv-my'); if (el) el.outerHTML = bvMyHTML(UI.bvMy); };
function bvMyRender() {
  if (!UI.bvMyBusy && (!UI.bvMy || Date.now() - UI.bvMyAt > 60000)) bvMyLoad().then(bvMyPaint);
  return bvMyHTML(UI.bvMy);
}
function bvMyCard(c) {
  const st = bvStatus(c), ls = c.labels || [];
  return `<li><button class="bv-mc bv-st-${st}" data-act="bvMyOpen" data-id="${esc(c.board)}" data-c="${esc(c.card)}"><span class="bv-mc-t">${esc(c.title)}</span><span class="bv-mc-m">${esc(c.board_name)} · ${esc(c.list_name)}</span>
    ${c.due || ls.length || c.items ? `<span class="bv-mc-f">${c.due ? bvDue(c) : ''}${ls.map(l => `<i class="bv-sw" style="background:${BV_LC[l.color] || '#98A2B3'}" title="${esc(bvLName(l))}"></i>`).join('')}${ls.length ? `<span class="bv-sr">Labels: ${esc(ls.map(bvLName).join(', '))}</span>` : ''}${c.items ? `<span class="bv-ck${c.items_done === c.items ? ' all' : ''}">${BV_I.check}${c.items_done}/${c.items}</span>` : ''}</span>` : ''}</button></li>`;
}
function bvMyHTML(d) {
  const top = extra => `<div class="bv-my-hd"><div><h3>My cards</h3><p>Cards you are a member of, on every board you can see.</p></div>${extra || ''}</div>`;
  if (!d) return `<section class="bv bv-my" id="bv-my" aria-busy="true">${top()}<p class="bv-dim bv-small">Loading your cards…</p></section>`;
  if (d.error) return `<section class="bv bv-my" id="bv-my">${top()}<div class="note bad">${esc(d.error)} <button class="btn sm" data-act="bvMyReload">Try again</button></div></section>`;
  const cards = d.cards || [], n = cards.length;
  const sum = `<p class="bv-my-sum">${bvPlural(n, 'open card')}${d.done ? ` · ${nf(d.done)} complete` : ''}${d.more ? ' · the soonest 500 shown' : ''}</p>`;
  if (!n) return `<section class="bv bv-my" id="bv-my">${top(sum)}<p class="bv-my-empty">No open cards are assigned to you. When someone adds you to a card on any board, it shows here by when it is due.</p></section>`;
  return `<section class="bv bv-my" id="bv-my">${top(sum)}<div class="bv-my-cols">${BV_WHEN.map(([k, l]) => {
    const cs = cards.filter(c => c.when === k), open = UI.bvMyAll[k], show = open ? cs : cs.slice(0, 5);
    return `<section class="bv-my-col bv-w-${k}" aria-labelledby="bv-my-${k}"><h4 id="bv-my-${k}">${l}<span class="bv-ct">${cs.length}</span></h4>${cs.length ? `<ul>${show.map(bvMyCard).join('')}</ul>${cs.length > 5 ? `<button class="bv-more" data-act="bvMyMore" data-id="${k}" aria-expanded="${!!open}">${open ? 'Show fewer' : `Show all ${cs.length}`}</button>` : ''}` : '<p class="bv-dim bv-small">Nothing here.</p>'}</section>`;
  }).join('')}</div></section>`;
}

/* ---------- the boards home's pages ----------
   The home has pages: Boards (the core's sections), All boards' Table and
   Calendar (here) and the Planner (boardplanner.js). boards.js shows
   BOARDS.homePage(home) in place of its sections when it gives a page. */
const BV_HOME = { pages: [], buttons: [] }; // pages: { id, label, order, render(home) }; buttons: () -> html, beside the tabs
const BV_PAGE_SS = 'ledgerline-bv-page';
UI.bvPage = (() => { try { return sessionStorage.getItem(BV_PAGE_SS) || 'boards'; } catch (e) { return 'boards'; } })();
function bvPageNav() {
  const pages = [{ id: 'boards', label: 'Boards', order: 0 }, ...BV_HOME.pages].sort((a, b) => (a.order ?? 50) - (b.order ?? 50));
  const cur = pages.some(p => p.id === UI.bvPage) ? UI.bvPage : 'boards';
  return `<nav class="bv-pages" aria-label="Boards home pages"><div class="bd-views" role="group">${pages.map(p => `<button class="${p.id === cur ? 'on' : ''}" aria-pressed="${p.id === cur}" data-act="bvPage" data-id="${esc(p.id)}">${esc(p.label)}</button>`).join('')}</div>
    <span class="bv-pages-r">${BV_HOME.buttons.map(f => { try { return f() || ''; } catch (e) { return ''; } }).join('')}</span></nav>`;
}
BOARDS.homePage = home => {
  const p = BV_HOME.pages.find(x => x.id === UI.bvPage);
  if (!p) return '';
  setTimeout(() => { const el = $('bv-page'); if (el && !el.dataset.after) { el.dataset.after = '1'; p.after?.(el); } });
  return bvPageNav() + p.render(home);
};
BOARDS.homeSections.push({ id: 'bv-nav', order: 1, render: () => bvPageNav() });
// Draws the open home page again in place (after its data comes in).
function bvPagePaint() {
  const el = $('bv-page'), p = BV_HOME.pages.find(x => x.id === UI.bvPage);
  if (!el || !p || S.view !== 'boards' || UI.bdBoard) return;
  const at = document.activeElement?.id, keep = el.querySelector('.bv-scroll');
  const pos = keep ? [keep.scrollLeft, keep.scrollTop] : null;
  el.outerHTML = p.render(BD.home || []);
  const nel = $('bv-page'), sc = nel?.querySelector('.bv-scroll');
  if (sc && pos && nel.dataset.key && UI.bvScroll[nel.dataset.key]) { sc.scrollLeft = pos[0]; sc.scrollTop = pos[1]; }
  if (nel) { nel.dataset.after = '1'; p.after?.(nel); }
  if (at) $(at)?.focus();
}

/* ---------- all boards: one table and calendar of every board's cards ---------- */
// Filters are kept on this computer.
const BV_ALL_LS = 'ledgerline-bv-all';
const BV_AF = { boards: 'Boards', lists: 'Lists', members: 'Members', labels: 'Labels', due: 'Due' };
const BV_AF_Q = { boards: 'board', lists: 'list', members: 'member', labels: 'label', due: 'due' };
function bvAllPref() {
  if (!UI.bvAllP) {
    let p = {};
    try { p = JSON.parse(localStorage.getItem(BV_ALL_LS) || '{}') || {}; } catch (e) {}
    const f = {};
    Object.keys(BV_AF).forEach(k => { f[k] = Array.isArray(p.f?.[k]) ? p.f[k].filter(x => typeof x === 'string').slice(0, 200) : []; });
    UI.bvAllP = { f, mode: p.mode === 'week' ? 'week' : 'month', sort: p.sort || '', dir: p.dir === -1 ? -1 : 1 };
  }
  return UI.bvAllP;
}
function bvAllSave() { const p = bvAllPref(); try { localStorage.setItem(BV_ALL_LS, JSON.stringify({ f: p.f, mode: p.mode, sort: p.sort, dir: p.dir })); } catch (e) {} }
const bvAllN = () => Object.values(bvAllPref().f).reduce((n, xs) => n + xs.length, 0);
function bvAllQuery(extra) {
  const f = bvAllPref().f, q = new URLSearchParams();
  Object.entries(f).forEach(([k, xs]) => xs.forEach(x => q.append(BV_AF_Q[k], x)));
  Object.entries(extra || {}).forEach(([k, v]) => { if (v !== '' && v != null) q.set(k, v); });
  return q.toString();
}
// The page's cards: fetched for the filters (and dates), kept a minute.
function bvAllData(which, extra) {
  const key = which + '?' + bvAllQuery(extra), s = UI.bvAll || {};
  if (s.key === key && (s.busy || Date.now() - s.at < 60000)) return s;
  UI.bvAll = { key, busy: true, at: Date.now(), data: s.which === which ? s.data : null, which };
  get('/v1/board-cards?' + bvAllQuery(extra))
    .then(d => { if (UI.bvAll.key === key) UI.bvAll = { key, at: Date.now(), data: d, which }; })
    .catch(e => { if (UI.bvAll.key === key) UI.bvAll = { key, at: Date.now(), error: e.message || 'The cards could not be loaded.', which }; })
    .finally(() => { if (UI.bvAll.key === key) bvPagePaint(); });
  return UI.bvAll;
}
function bvAllFilters(d) {
  const f = bvAllPref().f, n = bvAllN();
  return `<div class="bv-afs" role="group" aria-label="Filters">${Object.entries(BV_AF).map(([k, l]) => `<button class="btn sm bv-af${f[k].length ? ' on' : ''}" data-act="bvAllF" data-id="${k}" data-popkey="bvall-${k}" aria-haspopup="dialog" aria-label="${l}${f[k].length ? `, ${f[k].length} chosen` : ''}">${l}${f[k].length ? `<span class="bv-ct">${f[k].length}</span>` : ''}${BV_I.down}</button>`).join('')}
    ${n ? '<button class="btn sm" data-act="bvAllClear">Clear filters</button>' : ''}</div>`;
}
// The choices of one filter, from what the cards have.
function bvAllChoices(kind) {
  const d = UI.bvAll?.data || {};
  switch (kind) {
    case 'boards': return (d.boards || []).map(b => [b.id, b.name, b.kind === 'ledgerline' ? 'Ledgerline board' : '']);
    case 'lists': return (d.lists || []).map(l => [l, l]);
    case 'members': return [['_none', 'No member'], ...(d.members || []).map(m => [m.id, m.name])];
    case 'labels': return [['_none', 'No label'], ...(d.labels || []).map(l => [l.name, l.name, '', l.color])];
    case 'due': return BV_STO.map(k => [k, BV_ST[k]]);
  }
  return [];
}
function bvAllPop(kind) {
  const on = bvAllPref().f[kind], xs = bvAllChoices(kind);
  // A filter that is set stays offered even if no card has it now.
  on.forEach(v => { if (!xs.some(x => x[0] === v)) xs.push([v, kind === 'boards' ? 'A board no longer shown' : v]); });
  if (!xs.length) return '<p class="bd-mute">Nothing to choose yet.</p>';
  return `<div class="bv-afp" role="group" aria-label="${esc(BV_AF[kind])}">${xs.map(([v, l, sub, color]) => `<label class="bv-afo"><input type="checkbox" data-act="bvAllPick" data-id="${esc(kind)}" data-v="${esc(v)}" ${on.includes(v) ? 'checked' : ''}>${color ? `<i class="bv-sw" style="background:${BV_LC[color] || '#98A2B3'}"></i>` : kind === 'due' ? `<span class="bv-st-${esc(v)} bv-hb-ic" style="color:var(--st)">${bvStKey(v)}</span>` : ''}<span>${esc(l)}${sub ? ` <small class="bv-dim">${esc(sub)}</small>` : ''}</span></label>`).join('')}</div>
    ${on.length ? `<div class="bd-pop-tools"><button class="btn sm" data-act="bvAllNone" data-id="${esc(kind)}">Clear ${esc(BV_AF[kind].toLowerCase())}</button></div>` : ''}`;
}
const bvAllUser = (d, id) => (d.members || []).find(m => m.id === id)?.name || id;
function bvAllSortVal(c, k) {
  switch (k) {
    case 'title': return (c.title || '').toLowerCase();
    case 'board': return (c.board_name || '').toLowerCase();
    case 'list': return (c.list_name || '').toLowerCase();
    case 'due': { const a = bvAt(c.due); return isNaN(a) ? null : a; }
    case 'start': { const a = bvAt(c.start, true); return isNaN(a) ? null : a; }
  }
  return null;
}
function bvAllTableHTML() {
  const P = bvAllPref(), s = bvAllData('table', { offset: P.offset || 0 }), d = s.data;
  const head = `<div class="bv-tools"><div class="bv-tools-l"><h2 class="bv-title" id="bv-all-h">All boards</h2>${d ? `<span class="bv-dim bv-small">${bvPlural(d.total, 'card')} on ${bvPlural(d.boards.length, 'board')} you can see${s.busy ? ' · updating…' : ''}</span>` : ''}</div><div class="bv-tools-r">${bvAllFilters(d)}</div></div>`;
  if (!d) return `<section class="bv bv-all" id="bv-page" aria-labelledby="bv-all-h" ${s.busy ? 'aria-busy="true"' : ''}>${head}${s.error ? `<div class="note bad">${esc(s.error)} <button class="btn sm" data-act="bvAllRetry">Try again</button></div>` : '<p class="bv-dim bv-small">Loading the cards…</p>'}</section>`;
  const cards = d.cards.slice();
  if (P.sort) { const v = new Map(cards.map(c => [c.board + c.card, bvAllSortVal(c, P.sort)])); cards.sort((a, b) => bvCmp(v.get(a.board + a.card), v.get(b.board + b.card), P.dir)); }
  const th = (k, l) => { const on = P.sort === k; return `<th scope="col" aria-sort="${on ? (P.dir > 0 ? 'ascending' : 'descending') : 'none'}"><button class="bv-sort${on ? ' on' : ''}" data-act="bvAllSort" data-id="${k}" title="Sort by ${l.toLowerCase()}">${l}${on ? (P.dir > 0 ? BV_I.up : BV_I.down) : BV_I.sort}</button></th>`; };
  const plain = l => `<th scope="col"><span class="bv-th">${l}</span></th>`;
  const rows = cards.map(c => {
    const st = c.status || 'none', ls = c.labels || [], ms = c.members || [];
    return `<tr data-card="${esc(c.card)}" data-board="${esc(c.board)}"><td class="bv-c-title"><div class="bv-tt"><button class="bv-open" data-act="bvAllOpen" data-id="${esc(c.board)}" data-c="${esc(c.card)}">${esc(c.title)}</button></div><div class="bv-sub">#${c.no}</div></td>
      <td>${esc(c.board_name)}${c.role === 'observer' ? ' <span class="bv-tag bv-tag-dim" title="You can look and comment, not change cards">View only</span>' : ''}</td><td>${esc(c.list_name)}</td>
      <td>${ls.length ? `<div class="bv-chips">${ls.map(bvLChip).join('')}</div>` : bvNone}</td>
      <td>${ms.length ? `<div class="bv-avs">${ms.slice(0, 4).map(m => { const n = bvAllUser(d, m); return `<span class="bv-av" role="img" aria-label="${esc(n)}" title="${esc(n)}">${esc(initials(n))}</span>`; }).join('')}${ms.length > 4 ? `<span class="bv-avn">+${ms.length - 4}</span>` : ms.length === 1 ? `<span class="bv-avn">${esc(bvAllUser(d, ms[0]))}</span>` : ''}</div>` : bvNone}</td>
      <td class="bv-c-date">${c.due ? `<span class="bv-due bv-st-${st}" title="${BV_ST[st]}">${bvStIcon(st)}${esc(bvFmt(c.due))}<span class="bv-sr">, ${BV_ST[st]}</span></span>` : c.due_done ? `<span class="bv-due bv-st-done">${BV_I.check}Complete</span>` : bvNone}</td>
      <td class="bv-c-date">${c.start ? `<span class="bv-date">${esc(bvFmt(c.start))}</span>` : bvNone}</td>
      <td class="bv-c-checks">${c.items ? `<span class="bv-ck${c.items_done === c.items ? ' all' : ''}">${BV_I.check}${c.items_done}/${c.items}</span>` : bvNone}</td></tr>`;
  }).join('');
  const from = d.total ? d.offset + 1 : 0, to = d.offset + d.cards.length;
  return `<section class="bv bv-all" id="bv-page" aria-labelledby="bv-all-h" data-key="all:table">${head}
    ${d.capped ? '<div class="note warn">There are more cards than are read at once. Choose some boards to see the rest.</div>' : ''}
    <div class="bv-scroll bv-tw" role="region" aria-label="Cards on all boards" tabindex="0"><table class="bv-tbl bv-alltbl"><thead><tr>${th('title', 'Card')}${th('board', 'Board')}${th('list', 'List')}${plain('Labels')}${plain('Members')}${th('due', 'Due')}${th('start', 'Start')}${plain('Checklist')}</tr></thead>
      <tbody>${rows || `<tr><td colspan="8" class="bv-empty">${bvAllN() ? 'No cards match these filters.' : 'No open cards on the boards you can see.'}</td></tr>`}</tbody></table></div>
    <div class="bv-pager"><span class="bv-dim bv-small" aria-live="polite">${d.total ? `Showing ${nf(from)}–${nf(to)} of ${nf(d.total)}${P.sort ? ' · sorted on this page' : ' · soonest due first'}` : ''}</span>
      <span class="bv-pair"><button class="btn sm" data-act="bvAllPage" data-id="-1" ${d.offset > 0 ? '' : 'disabled'}>${BV_I.left}Previous</button><button class="btn sm" data-act="bvAllPage" data-id="1" ${d.more ? '' : 'disabled'}>Next${BV_I.right}</button></span></div>
  </section>`;
}
function bvAllCalState() { const P = bvAllPref(); UI.bvAllCal ||= { at: bvToday() }; UI.bvAllCal.mode = P.mode; return UI.bvAllCal; }
function bvAllCalHTML() {
  const st = bvAllCalState(), R = bvCalRange(st), wk = st.mode === 'week';
  const s = bvAllData('calendar', { from: bvDS(R.a), to: bvDS(R.z) }), d = s.data;
  const head = `<div class="bv-tools"><div class="bv-tools-l"><button class="btn sm" data-act="bvAllCalGo" data-id="0">Today</button><span class="bv-pair"><button class="btn sm bv-ib" data-act="bvAllCalGo" data-id="-1" aria-label="Previous ${st.mode}">${BV_I.left}</button><button class="btn sm bv-ib" data-act="bvAllCalGo" data-id="1" aria-label="Next ${st.mode}">${BV_I.right}</button></span>
      <h2 class="bv-title" id="bv-all-h" aria-live="polite">${bvCalTitle(st, R)}</h2><span class="bv-dim bv-small">${d ? `${bvPlural(d.total, 'card')} on all boards · ` : ''}times in IST${s.busy && d ? ' · updating…' : ''}</span></div>
    <div class="bv-tools-r">${bvAllFilters(d)}${bvSeg('bvAllCalMode', [['month', 'Month'], ['week', 'Week']], st.mode, 'Calendar period')}</div></div>`;
  if (!d) return `<section class="bv bv-cal bv-all" id="bv-page" aria-labelledby="bv-all-h">${head}${s.error ? `<div class="note bad">${esc(s.error)} <button class="btn sm" data-act="bvAllRetry">Try again</button></div>` : '<p class="bv-dim bv-small">Loading the cards…</p>'}</section>`;
  const listById = {}, cards = d.cards.map(x => { listById[x.list] = { name: `${x.board_name} · ${x.list_name}` }; return { id: x.card, _board: x.board, no: x.no, title: x.title, list: x.list, start: x.start, due: x.due, due_done: x.due_done }; });
  const M = { edit: false, listById }, { items } = bvItems(cards);
  return `<section class="bv bv-cal bv-all" id="bv-page" aria-labelledby="bv-all-h"${wk ? ` data-key="all:calweek" data-w0="${R.a}"` : ''}>${head}
    ${d.more || d.capped ? `<div class="note warn">Only the first ${nf(d.cards.length)} cards are shown. Filter by board or list to see the rest.</div>` : ''}
    <div class="bv-cal-main">${bvCalGrid(items, R, wk, M)}</div></section>`;
}
// Opening a card from these pages; Enter or Space on a calendar card too.
document.addEventListener('click', e => {
  const el = e.target.closest?.('#bv-page [data-card][data-board]');
  if (!el || e.target.closest('[data-act]') || e.target.closest('.bp-root')) return;
  BOARDS.openCard(el.dataset.board, el.dataset.card);
});
document.addEventListener('keydown', e => {
  const el = e.target.closest?.('#bv-page [role=button][data-card][data-board]');
  if (!el || e.target !== el || (e.key !== 'Enter' && e.key !== ' ') || el.closest('.bp-root')) return;
  e.preventDefault();
  BOARDS.openCard(el.dataset.board, el.dataset.card);
});
const bvAllAfter = el => { if (el?.dataset.key) bvKeepScroll(el, el.dataset.key === 'all:calweek' ? bvHourFirst(bvItems((UI.bvAll?.data?.cards || []).map(x => ({ ...x, id: x.card }))).items, +el.dataset.w0) : null); };
BV_HOME.pages.push({ id: 'table', label: 'Table', order: 20, render: bvAllTableHTML, after: bvAllAfter }, { id: 'calendar', label: 'Calendar', order: 30, render: bvAllCalHTML, after: bvAllAfter });

/* ---------- actions ---------- */
Object.assign(A, {
  bvOpen: id => { const ctx = BOARDS.ctx(); if (ctx) BOARDS.openCard(ctx.board.id, id); },
  bvSort: id => { const ctx = BOARDS.ctx(); if (!ctx) return; const T = bvTCols(ctx, bvMeta(ctx)); bvPrefSet(ctx, 'table', T.sort === id ? (T.dir > 0 ? { dir: -1 } : { sort: '', dir: 1 }) : { sort: id, dir: 1 }); bvRerender(); },
  bvCols: () => bvColsModal(),
  bvColShow: (id, el) => { const ctx = BOARDS.ctx(); if (!ctx || id === 'title') return; const T = bvTCols(ctx, bvMeta(ctx)); if (el.checked) T.hidden.delete(id); else T.hidden.add(id); bvPrefSet(ctx, 'table', { hidden: [...T.hidden] }); bvRerender(); },
  bvColMove: (id, el) => {
    const ctx = BOARDS.ctx(); if (!ctx) return;
    const T = bvTCols(ctx, bvMeta(ctx)), o = T.order.slice(), i = o.indexOf(id), j = i + +el.dataset.d;
    if (i < 0 || j < 0 || j >= o.length) return;
    [o[i], o[j]] = [o[j], o[i]];
    bvPrefSet(ctx, 'table', { order: o }); bvRerender(); bvColsModal();
    document.querySelector(`#mroot [data-act=bvColMove][data-id="${CSS.escape(id)}"][data-d="${el.dataset.d}"]:not([disabled])`)?.focus();
  },
  bvColReset: () => { const ctx = BOARDS.ctx(); if (!ctx) return; bvPrefSet(ctx, 'table', { order: undefined, hidden: undefined }); bvRerender(); bvColsModal(); },
  bvCsv: () => { const ctx = BOARDS.ctx(); if (!ctx) return; bvDownload(bvCsv(ctx), `${bvSlug(ctx.board.name)}-cards-${bvToday()}.csv`); toast('CSV of the rows shown downloaded'); },
  bvCalGo: id => {
    const ctx = BOARDS.ctx(); if (!ctx) return;
    const s = bvCalState(ctx), d = +id;
    if (!d) s.at = bvToday();
    else if (s.mode === 'week') s.at = bvShift(s.at, 7 * d);
    else { const n = bvDN(s.at); s.at = bvDS(d > 0 ? bvNextMonth(n) : bvMonthStart(bvMonthStart(n) - 1)); }
    bvRerender();
  },
  bvCalMode: id => { const ctx = BOARDS.ctx(); if (!ctx) return; bvPrefSet(ctx, 'cal', { mode: id }); bvRerender(); },
  bvCalMore: id => {
    const ctx = BOARDS.ctx();
    if (!ctx) { if (UI.bvPage === 'calendar') { bvAllPref().mode = 'week'; bvAllSave(); bvAllCalState().at = id; bvPagePaint(); } return; }
    bvPrefSet(ctx, 'cal', { mode: 'week' }); bvCalState(ctx).at = id; bvRerender();
  },
  bvTlZoom: id => { const ctx = BOARDS.ctx(); if (!ctx) return; bvPrefSet(ctx, 'tl', { zoom: id }); bvRerender(); },
  bvTlToday: () => { const root = $('bv-root'), sc = root?.querySelector('.bv-scroll'); if (!sc) return; const lw = root.querySelector('.bv-tl-corner').getBoundingClientRect().width; sc.scrollTo({ left: Math.max(0, +root.dataset.tx - (sc.clientWidth - lw) / 3), behavior: 'smooth' }); },
  bvDrill: (kind, el) => {
    const ctx = BOARDS.ctx(); if (!ctx) return;
    const id = el.dataset.v || '', cur = bvDrillOn(ctx);
    if (typeof BOARDS.filterBy === 'function') { BOARDS.filterBy(kind, id); return; } // the board's own filter, when boards.js offers one
    UI.bvDrill = cur && cur.kind === kind && cur.id === id ? null : { board: ctx.board.id, kind, id, text: el.dataset.t || '' };
    bvRerender();
  },
  bvDrillClear: () => { UI.bvDrill = null; bvRerender(); },
  bvDashEdit: () => { const ctx = BOARDS.ctx(); if (!ctx) return; UI.bvDashEdit = { board: ctx.board.id, tiles: bvTilesOf(ctx.board) }; bvRerender(); bvDashFocus('[data-act=bvTileAdd]'); },
  bvDashCancel: () => { UI.bvDashEdit = null; bvRerender(); bvDashFocus('[data-act=bvDashEdit]'); },
  bvDashSave: () => bvDashSave(),
  bvTileAdd: () => {
    const ctx = BOARDS.ctx(), E = ctx && bvDashEditing(ctx); if (!E || E.tiles.length >= 12) return;
    const data = Object.keys(BV_TDATA).find(k => !E.tiles.some(t => t.data === k)) || 'list';
    E.tiles.push({ id: '', data, chart: BV_TCHARTS[data][0] }); E.reset = false;
    bvRerender(); bvDashFocus(`[data-bv-tile="${E.tiles.length - 1}"]`);
  },
  bvTileDel: id => { const ctx = BOARDS.ctx(), E = ctx && bvDashEditing(ctx); if (!E) return; E.tiles.splice(+id, 1); E.reset = false; bvRerender(); bvDashFocus(E.tiles.length ? `[data-bv-tile="${Math.min(+id, E.tiles.length - 1)}"]` : '[data-act=bvTileAdd]'); },
  bvTileMove: (id, el) => {
    const ctx = BOARDS.ctx(), E = ctx && bvDashEditing(ctx); if (!E) return;
    const i = +id, j = i + +el.dataset.d; if (j < 0 || j >= E.tiles.length) return;
    [E.tiles[i], E.tiles[j]] = [E.tiles[j], E.tiles[i]]; E.reset = false;
    bvRerender(); bvDashFocus(`[data-act=bvTileMove][data-id="${j}"][data-d="${el.dataset.d}"]:not([disabled])`);
  },
  bvTileChart: (id, el) => { const ctx = BOARDS.ctx(), E = ctx && bvDashEditing(ctx), t = E?.tiles[+id]; if (!t) return; t.chart = el.dataset.v; E.reset = false; bvRerender(); bvDashFocus(`[data-act=bvTileChart][data-id="${id}"][data-v="${el.dataset.v}"]`); },
  bvTileReset: () => { const ctx = BOARDS.ctx(), E = ctx && bvDashEditing(ctx); if (!E) return; E.tiles = BV_TILES0.map(t => ({ ...t })); E.reset = true; bvRerender(); },
  bvMyOpen: (board, el) => BOARDS.openCard(board, el.dataset.c),
  bvPage: id => {
    UI.bvPage = id; try { sessionStorage.setItem(BV_PAGE_SS, id); } catch (e) {}
    bdPopClose?.(true);
    Promise.resolve(BOARDS.reload()).finally(() => document.querySelector(`.bv-pages [data-act=bvPage][data-id="${CSS.escape(id)}"]`)?.focus());
  },
  bvAllF: (kind, el) => bdPop('bvall-' + kind, el, BV_AF[kind], () => bvAllPop(kind)),
  bvAllPick: (kind, el) => {
    const P = bvAllPref(), xs = P.f[kind], v = el.dataset.v;
    P.f[kind] = el.checked ? [...new Set([...xs, v])] : xs.filter(x => x !== v);
    if (kind === 'boards') P.f.lists = P.f.lists.slice(); // lists, labels and members on offer follow the boards
    P.offset = 0; bvAllSave(); bvPagePaint(); bdPopRefresh();
  },
  bvAllNone: kind => { const P = bvAllPref(); P.f[kind] = []; P.offset = 0; bvAllSave(); bvPagePaint(); bdPopRefresh(); },
  bvAllClear: () => { const P = bvAllPref(); Object.keys(P.f).forEach(k => { P.f[k] = []; }); P.offset = 0; bvAllSave(); bdPopClose(true); bvPagePaint(); },
  bvAllRetry: () => { UI.bvAll = null; bvPagePaint(); },
  bvAllSort: id => { const P = bvAllPref(); if (P.sort === id) { if (P.dir > 0) P.dir = -1; else { P.sort = ''; P.dir = 1; } } else { P.sort = id; P.dir = 1; } bvAllSave(); bvPagePaint(); },
  bvAllPage: id => { const P = bvAllPref(), d = UI.bvAll?.data; P.offset = Math.max(0, (P.offset || 0) + +id * (d?.limit || 500)); bvPagePaint(); $('bv-page')?.scrollIntoView({ block: 'start' }); },
  bvAllOpen: (board, el) => BOARDS.openCard(board, el.dataset.c),
  bvAllCalGo: id => {
    const st = bvAllCalState(), d = +id;
    if (!d) st.at = bvToday();
    else if (st.mode === 'week') st.at = bvShift(st.at, 7 * d);
    else { const n = bvDN(st.at); st.at = bvDS(d > 0 ? bvNextMonth(n) : bvMonthStart(bvMonthStart(n) - 1)); }
    bvPagePaint();
  },
  bvAllCalMode: id => { bvAllPref().mode = id === 'week' ? 'week' : 'month'; bvAllSave(); bvPagePaint(); },
  bvMyMore: id => { UI.bvMyAll[id] = !UI.bvMyAll[id]; bvMyPaint(); },
  bvMyReload: () => { UI.bvMy = null; bvMyPaint(); bvMyLoad().then(bvMyPaint); }
});

/* ---------- registration ---------- */
BOARDS.views.table = { label: 'Table', order: 10, render: bvTableHTML, after: bvTableAfter };
BOARDS.views.calendar = { label: 'Calendar', order: 20, render: bvCalHTML, after: bvCalAfter };
BOARDS.views.timeline = { label: 'Timeline', order: 30, render: bvTlHTML, after: bvTlAfter };
BOARDS.views.dashboard = { label: 'Dashboard', order: 40, render: bvDashHTML };
BOARDS.homeSections.push({ id: 'bv-mycards', order: 5, load: bvMyLoad, render: bvMyRender });

