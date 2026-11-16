'use strict';
/* Ledgerline console: Boards (Trello-style boards, lists and cards).
   Staff screen "boards": the boards home, a board (lists and cards, drag
   and drop), the card window, filters, search and notifications.
   Server: internal/ops/boards.go, internal/api/boards.go.

   The other board files plug into BOARDS (they load after this one):
     BOARDS.views[id] = { label, order, render(ctx) -> html, after?(ctx) }
         Another way to show the board's cards (table, calendar, timeline,
         dashboard). ctx = BOARDS.ctx(): { board, cards (filtered), all
         (every open card), role, users, el }. The built-in view is "board".
     BOARDS.cardSections.push({ id, order, render(card, ctx) -> html|'' })
         A section in the card window's main column.
     BOARDS.cardButtons.push({ id, label, icon?, group: 'add'|'actions', order,
         show?(card, ctx) -> bool, run(card, ctx, el) })
         A button in the card window's sidebar. icon names one of BD_IC.
     BOARDS.cardBadges.push((card, ctx) -> html)
         Small badges on a card's front in a list.
     BOARDS.boardMenu.push({ id, label, icon?, order, show?(ctx) -> bool, run(ctx) })
         An entry in the board's menu. icon names one of BD_IC.
     BOARDS.headerButtons.push({ id, order, render(ctx) -> html })
         Buttons in the board header.
     BOARDS.composerButtons.push({ id, label, run(listId, ctx) })
         Extra buttons beside "Add card" in a list's composer.
     BOARDS.homeSections.push({ id, order, render(home) -> html, load?() -> Promise })
         Sections on the boards home page.
     BOARDS.bellTools.push(() -> html)
         Lines at the foot of the notifications panel (the bell).
     BOARDS.reload()          reloads the open board
     BOARDS.ctx()             the open board's context
     BOARDS.openCard(b, c)    opens a card window
     BOARDS.openBoard(id)     opens a board
   Actions use the global A object; each file prefixes its names (bv*,
   br*, bx*, bs*) and its UI state keys the same way. */

const BOARDS = {
  views: {}, cardSections: [], cardButtons: [], cardBadges: [], boardMenu: [], headerButtons: [], composerButtons: [], homeSections: [], bellTools: [],
  reload: () => {}, ctx: () => null, openCard: () => {}, openBoard: () => {}
};
/* Boards: core */

/* Notes for the other board files:
   - ctx also carries canEdit (admin or member, board open), me (the
     signed-in user) and filtering (a filter is on).
   - home, given to homeSections' render, is the array of boards the
     person can see (also as home.boards).
   - After changing something, call await BOARDS.reload() (a Promise): it
     refreshes the board and an open card window without losing typed text.
   - BOARDS.filterBy(kind, id) sets the board's own filter to one thing
     (list, label, member, due: overdue|soon|later|none|done, checks:
     all|some|none|nolist) and draws the current view again; the same call
     again clears it. ctx.el is the view's #bv-root once it is on the page.
   - A mirror card (card.mirror) has its title, description and dates
     read-only here; they are changed on the original.
   - The card window keeps location.hash at #boards/<board>/<card>.
   - boardux.js (quick edit, keyboard, undo, the editor and its markdown,
     bdMd and bdInline) hooks in with BOARDS.onApi (after each change this
     screen makes: method, path, body, result), BOARDS.onPaint (after each
     draw), BOARDS.compose(text, list) (the composer's #label @person ^top)
     and BOARDS.quickEdit(card, anchor).
   - Labels have 30 colours ("green_subtle", "green", "green_bold" …):
     BOARDS.labelColors lists them, BOARDS.labelName(c) names one
     ("Bold green") and BOARDS.labelHex(c) gives its light-theme colour.
     The class bdc-<colour> sets --c (fill) and --lt (text on it).
   - BD.v.collapsed is the lists the person collapsed (a narrow strip, no
     cards); BD.v.board.watchers and each list's watchers say who watches;
     BD.v.board.perms is { comments: off|members|observers, invite:
     admins|members, self_join }, and bdCanComment() follows it. */

/* ---------- state ---------- */
Object.assign(UI, { bdBoard: '', bdView: {}, bdF: bdF0(), bdNames: false, bdHideDone: {}, bdDetails: false });
const BD = {
  home: null, v: null, card: '', cd: null, cact: [], cw: {}, drafts: {}, pop: null, notices: [], unread: 0, deep: '', hover: '', drag: null,
  comp: '', addList: false, rename: '', scroll: null, soonT: 0, boards: null, mv: null, ac: null, painting: false, noClick: 0, last: '',
  inflight: 0, ver: 0, polling: false, sres: null, sresOpen: false, sresT: 0, fT: 0
};
const BD_COLORS = ['green', 'yellow', 'orange', 'red', 'purple', 'blue', 'sky', 'lime', 'pink', 'black'];
const BD_CN = { green: 'Green', yellow: 'Yellow', orange: 'Orange', red: 'Red', purple: 'Purple', blue: 'Blue', sky: 'Sky', lime: 'Lime', pink: 'Pink', black: 'Black' };
/* Labels have 30 colours: each of the ten in a subtle, normal and bold
   shade ("green_subtle", "green", "green_bold"), in the picker's order.
   Lists, covers and backgrounds keep the ten. BOARDS.labelColors,
   BOARDS.labelName(c) and BOARDS.labelHex(c) are for the other board files. */
const BD_LCOLORS = [...BD_COLORS.map(c => c + '_subtle'), ...BD_COLORS, ...BD_COLORS.map(c => c + '_bold')];
BD_COLORS.forEach(c => { BD_CN[c + '_subtle'] = 'Subtle ' + BD_CN[c].toLowerCase(); BD_CN[c + '_bold'] = 'Bold ' + BD_CN[c].toLowerCase(); });
const BD_LHEX = {
  green_subtle: '#AEE6C9', green: '#1F9D63', green_bold: '#106B41', yellow_subtle: '#F7E39A', yellow: '#D9A800', yellow_bold: '#8A6A00',
  orange_subtle: '#FCCB9F', orange: '#E8770F', orange_bold: '#A44C04', red_subtle: '#F6B9B4', red: '#C9372F', red_bold: '#8C1D18',
  purple_subtle: '#D3C8F6', purple: '#7356D6', purple_bold: '#4C34A3', blue_subtle: '#BEC8F4', blue: '#2F45C8', blue_bold: '#1B2A86',
  sky_subtle: '#ACDDF3', sky: '#1C8FC4', sky_bold: '#0C5D83', lime_subtle: '#CDE7A8', lime: '#6EA531', lime_bold: '#456E19',
  pink_subtle: '#F5C0DE', pink: '#D9529F', pink_bold: '#992D6B', black_subtle: '#D0D5DD', black: '#344054', black_bold: '#0C111D'
};
Object.assign(BOARDS, { labelColors: BD_LCOLORS, labelName: c => BD_CN[c] || c || '', labelHex: c => BD_LHEX[c] || '#98A2B3' });
const BD_RX = ['👍', '👀', '✅', '🎉', '❤️', '😂', '🙏', '❗'];
const BD_FIT = !!window.CSS?.supports?.('field-sizing', 'content'); // inputs that size to their text
const BD_AVC = ['#2F45C8', '#127A5B', '#9E4A06', '#6941C6', '#0E7090', '#C11574', '#344054', '#B42318'];

/* ---------- icons (no emoji in the chrome) ---------- */
const BD_IC = {
  star: '<path d="M12 3.6l2.6 5.2 5.8.9-4.2 4.1 1 5.8L12 16.9l-5.2 2.7 1-5.8-4.2-4.1 5.8-.9z"/>',
  lock: '<rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>',
  users: '<circle cx="9" cy="8.5" r="3.3"/><path d="M3 19.5a6 6 0 0 1 12 0"/><path d="M15.5 5.3a3.3 3.3 0 0 1 0 6.4M17.5 14.2a6 6 0 0 1 3.5 5.3"/>',
  bell: '<path d="M6.5 16v-5a5.5 5.5 0 0 1 11 0v5l1.5 2H5z"/><path d="M10 20.5a2.2 2.2 0 0 0 4 0"/>',
  search: '<circle cx="11" cy="11" r="6.5"/><path d="m20 20-4.2-4.2"/>',
  filter: '<path d="M4 5.5h16l-6.2 7.2v5.8l-3.6-1.8v-4z"/>',
  dots: '<circle class="f" cx="5.5" cy="12" r="1.5"/><circle class="f" cx="12" cy="12" r="1.5"/><circle class="f" cx="18.5" cy="12" r="1.5"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  x: '<path d="M6.5 6.5l11 11M17.5 6.5l-11 11"/>',
  clock: '<circle cx="12" cy="12" r="8.2"/><path d="M12 7.6V12l2.9 1.8"/>',
  desc: '<path d="M4.5 6.5h15M4.5 11h15M4.5 15.5h9"/>',
  comment: '<path d="M4.5 5.5h15v10.5H9.5l-5 3.8z"/>',
  clip: '<path d="M19.5 11.6l-7.6 7.6a4.8 4.8 0 0 1-6.8-6.8l8.3-8.3a3.2 3.2 0 0 1 4.5 4.5l-8.3 8.3a1.6 1.6 0 0 1-2.3-2.3l7.6-7.6"/>',
  check: '<rect x="4.2" y="4.2" width="15.6" height="15.6" rx="3"/><path d="m8.5 12.2 2.4 2.4 4.6-4.9"/>',
  eye: '<path d="M2.8 12S6.2 6 12 6s9.2 6 9.2 6-3.4 6-9.2 6-9.2-6-9.2-6z"/><circle cx="12" cy="12" r="2.6"/>',
  vote: '<path d="M7.5 10.5V19H4.5v-8.5zM7.5 10.5l3.8-6.2a1.9 1.9 0 0 1 3.4 1.4L14 9.5h5a1.9 1.9 0 0 1 1.9 2.3l-1.3 6A1.9 1.9 0 0 1 17.7 19H7.5"/>',
  link: '<path d="M10 14a4.2 4.2 0 0 0 6 0l3-3a4.2 4.2 0 0 0-6-6l-1 1"/><path d="M14 10a4.2 4.2 0 0 0-6 0l-3 3a4.2 4.2 0 0 0 6 6l1-1"/>',
  card: '<rect x="3.5" y="5" width="17" height="14" rx="2.2"/><path d="M7 9.5h6.5"/>',
  activity: '<path d="M3.5 12h4l2.5-6 3.5 12 2.5-6h4.5"/>',
  arrow: '<path d="M5 12h13.5M13 6.5l5.5 5.5-5.5 5.5"/>',
  copy: '<rect x="8.5" y="8.5" width="11" height="11" rx="2"/><path d="M15.5 8.5V5.5a1 1 0 0 0-1-1h-9a1 1 0 0 0-1 1v9a1 1 0 0 0 1 1h3"/>',
  archive: '<rect x="3.8" y="4.5" width="16.4" height="4" rx="1"/><path d="M5.5 8.5v9.8a1.2 1.2 0 0 0 1.2 1.2h10.6a1.2 1.2 0 0 0 1.2-1.2V8.5M10 12.5h4"/>',
  trash: '<path d="M4.5 7h15M9.5 7V4.6h5V7M6.5 7l1 12.4h9l1-12.4"/>',
  tag: '<path d="M3.8 12.2V4.6h7.6l8.8 8.8-7.6 7.6z"/><circle cx="8.2" cy="9" r="1.2"/>',
  user: '<circle cx="12" cy="8.2" r="3.8"/><path d="M4.8 20a7.2 7.2 0 0 1 14.4 0"/>',
  image: '<rect x="3.6" y="4.6" width="16.8" height="14.8" rx="2"/><circle cx="9" cy="10" r="1.7"/><path d="m20.4 16-4.9-4.9-8.8 8.3"/>',
  board: '<rect x="3.6" y="4" width="16.8" height="16" rx="2.2"/><path d="M8.4 8v7.5M12 8v4.5M15.6 8v9"/>',
  smile: '<circle cx="12" cy="12" r="8.2"/><path d="M8.7 14a3.9 3.9 0 0 0 6.6 0"/><circle class="f" cx="9.3" cy="9.8" r="1"/><circle class="f" cx="14.7" cy="9.8" r="1"/>',
  up: '<path d="M12 18.5v-13M6.5 11 12 5.5l5.5 5.5"/>',
  down: '<path d="M12 5.5v13M6.5 13l5.5 5.5 5.5-5.5"/>',
  back: '<path d="M14.5 6 8.5 12l6 6"/>',
  grip: '<circle class="f" cx="9" cy="7" r="1.3"/><circle class="f" cx="15" cy="7" r="1.3"/><circle class="f" cx="9" cy="12" r="1.3"/><circle class="f" cx="15" cy="12" r="1.3"/><circle class="f" cx="9" cy="17" r="1.3"/><circle class="f" cx="15" cy="17" r="1.3"/>',
  info: '<circle cx="12" cy="12" r="8.2"/><path d="M12 11v5.2"/><circle class="f" cx="12" cy="7.8" r="1"/>',
  bolt: '<path d="M13 2.5 4.5 13.5H11l-1 8 8.5-11H12z"/>',
  gear: '<circle cx="12" cy="12" r="3"/><path d="M12 3.5v2.6M12 17.9v2.6M3.5 12h2.6M17.9 12h2.6M6 6l1.8 1.8M16.2 16.2 18 18M6 18l1.8-1.8M16.2 7.8 18 6"/>',
  keys: '<rect x="3" y="6.5" width="18" height="11" rx="2"/><path d="M7 10h.01M10 10h.01M13 10h.01M16 10h.01M8 14h8"/>',
  collapse: '<path d="M12 4.5v15M3 12h6M6.4 9.2 9.2 12l-2.8 2.8M21 12h-6M17.6 9.2 14.8 12l2.8 2.8"/>',
  tick: '<path d="m5 12.5 4.3 4.3L19 7.2"/>',
  pencil: '<path d="M4.5 19.5l1-4.2L15.8 5a2 2 0 0 1 2.9 0l.3.3a2 2 0 0 1 0 2.9L8.7 18.5z"/><path d="M13.8 7l3.2 3.2"/>'
};
const bdI = (n, cls) => `<svg class="bd-i${cls ? ' ' + cls : ''}" viewBox="0 0 24 24" aria-hidden="true" focusable="false">${BD_IC[n] || ''}</svg>`;

/* ---------- small helpers ---------- */
const bdE = encodeURIComponent;
const bdURL = (p = '') => `/v1/boards/${bdE(UI.bdBoard)}${p}`;
const bdCURL = (c, p = '') => bdURL(`/cards/${bdE(c)}${p}`);
const bdMe = () => S.me?.actor || '';
const bdCan = () => !!BD.v && ['admin', 'member'].includes(BD.v.role) && !BD.v.board.closed;
const bdAdmin = () => BD.v?.role === 'admin';
const bdTeam = () => !['ledgerline', 'inbox'].includes(BD.v?.board.kind); // an Inbox (boardplanner.js) keeps its one list
/* The board's permissions (the server enforces them; this follows):
   comments off | members | observers, invite admins | members, self_join. */
const bdPerms = () => ({ comments: BD.v?.board.perms?.comments || 'observers', invite: BD.v?.board.perms?.invite || 'admins', self_join: !!BD.v?.board.perms?.self_join });
const bdCanComment = () => { if (!BD.v?.role || BD.v.board.closed) return false; const p = bdPerms().comments; return p === 'observers' || (p === 'members' && ['admin', 'member'].includes(BD.v.role)); };
const bdCanInvite = () => !!BD.v && !BD.v.board.closed && (bdAdmin() || (bdTeam() && BD.v.role === 'member' && bdPerms().invite === 'members'));
const bdWatching = () => (BD.v?.board.watchers || []).includes(bdMe());
const bdLWatching = l => (l?.watchers || []).includes(bdMe());
const bdCollapsed = id => (BD.v?.collapsed || []).includes(id);
const bdLists = (b = BD.v?.board) => (b?.lists || []).filter(l => !l.archived).sort((x, y) => x.pos - y.pos);
const bdOpenCards = () => (BD.v?.cards || []).filter(c => !c.archived);
const bdIn = (l, cs) => cs.filter(c => c.list === l).sort((a, b) => a.pos - b.pos);
const bdLabel = id => (BD.v?.board.labels || []).find(l => l.id === id);
const bdUsers = () => BD.v?.users || [];
const bdDraft = (k, d) => BD.drafts[k] ?? d;
const bdBg = bg => !bg ? 'blue' : bg.startsWith('file:') ? 'img' : BD_COLORS.includes(bg) ? bg : 'blue';
const bdBgStyle = bg => bg && bg.startsWith('file:') ? ` style="background-image:url('/v1/board-files/${bdE(bg.slice(5))}')"` : '';
const bdSr = t => `<span class="bd-sr">${t}</span>`;
const bdPl = (n, one, many) => `${n} ${n === 1 ? one : (many || one + 's')}`;
// Ledgerline's own actors: automation rules, and everything else it does.
const bdSys = id => String(id || '').startsWith('system:');
function bdName(id) {
  if (bdSys(id)) return id === 'system:automation' ? 'Automation' : 'Ledgerline';
  const u = bdUsers().find(x => x.id === id) || (S.me?.users || []).find(x => x.id === id);
  return u?.name || id || '';
}
function bdAvCol(id) { let h = 0; for (const ch of String(id)) h = (h * 31 + ch.charCodeAt(0)) >>> 0; return BD_AVC[h % BD_AVC.length]; }
function bdAv(id, cls) {
  const n = bdName(id);
  if (bdSys(id)) return `<span class="bd-av sys${cls ? ' ' + cls : ''}" title="${esc(n)}">${bdI(id === 'system:automation' ? 'gear' : 'board')}${bdSr(esc(n))}</span>`;
  return `<span class="bd-av${cls ? ' ' + cls : ''}" style="--av:${bdAvCol(id)}" title="${esc(n)}"><span aria-hidden="true">${esc(initials(n))}</span>${bdSr(esc(n))}</span>`;
}
function bdMid(a, b) {
  if (a == null && b == null) return 1024;
  if (a == null) return b > 2 ? b / 2 : b - 1024;
  if (b == null) return a + 1024;
  return (a + b) / 2;
}
// Hooks (boardux.js): onApi after each change made here, onPaint after each draw.
BOARDS.onApi = []; BOARDS.onPaint = [];
async function bdApi(method, path, body, raw) {
  BD.inflight++; BD.ver++;
  try { const r = await api(method, path, body, raw); BOARDS.onApi.forEach(f => bdSafe(() => { f(method, path, body, r); }, 'change hook')); return r; } finally { BD.inflight--; BD.ver++; }
}
const bdPost = (p, b) => bdApi('POST', p, b || {});
const bdPutBoard = patch => bdApi('PUT', bdURL(), { description: BD.v.board.description || '', ...patch });

/* dates: a card date is YYYY-MM-DD or YYYY-MM-DDTHH:MM in IST; a date
   alone is the end of that day */
function bdDueDate(s) { if (!s) return null; const d = new Date(s.length <= 10 ? s + 'T23:59:00+05:30' : s + ':00+05:30'); return isNaN(d) ? null : d; }
function bdDueState(c) { const d = bdDueDate(c.due); if (!d) return ''; if (c.due_done) return 'done'; const ms = d - Date.now(); return ms < 0 ? 'over' : ms < 864e5 ? 'soon' : ''; }
const BD_DUEST = { done: 'Complete', over: 'Overdue', soon: 'Due soon' };
function bdDateTxt(s) {
  const m = /^(\d{4})-(\d{2})-(\d{2})(?:T(\d{2}):(\d{2}))?/.exec(s || ''); if (!m) return s || '';
  const y = +m[1] !== new Date().getFullYear() ? ' ' + m[1] : '';
  return `${+m[3]} ${MON[+m[2] - 1]}${y}${m[4] ? `, ${m[4]}:${m[5]}` : ''}`;
}
function bdWhen(iso) {
  const t = new Date(iso); if (isNaN(t)) return '';
  const s = (Date.now() - t) / 1000;
  if (s < 45) return 'just now';
  if (s < 3600) return Math.round(s / 60) + ' min ago';
  const f = d => d.toLocaleDateString('en-GB', { timeZone: 'Asia/Kolkata', day: 'numeric', month: 'short', year: 'numeric' });
  const tm = t.toLocaleTimeString('en-GB', { timeZone: 'Asia/Kolkata', hour: '2-digit', minute: '2-digit', hour12: false });
  const d = f(t);
  if (d === f(new Date())) return 'today at ' + tm;
  if (d === f(new Date(Date.now() - 864e5))) return 'yesterday at ' + tm;
  return t.toLocaleDateString('en-GB', { timeZone: 'Asia/Kolkata', day: 'numeric', month: 'short', ...(t.getFullYear() === new Date().getFullYear() ? {} : { year: 'numeric' }) }) + ' at ' + tm;
}
const bdBytes = n => n >= 1 << 20 ? (n / (1 << 20)).toFixed(1) + ' MB' : Math.max(1, Math.round(n / 1024)) + ' KB';

/* The safe markdown (bdMd, bdInline) is in boardux.js, with the editor. */

/* Repaint without losing focus, the caret or the window's scroll. */
function bdFocusKey(a) {
  if (!a || a === document.body) return '';
  if (a.id) return '#' + CSS.escape(a.id);
  if (a.dataset?.act) return `[data-act="${CSS.escape(a.dataset.act)}"]${a.dataset.id ? `[data-id="${CSS.escape(a.dataset.id)}"]` : ''}`;
  return '';
}
function bdKeep(fn) {
  const a = document.activeElement, key = bdFocusKey(a);
  let s = null, e = null; try { s = a.selectionStart; e = a.selectionEnd; } catch (x) {}
  const ov = document.querySelector('#mroot .ov'), top = ov ? ov.scrollTop : 0;
  BD.painting = true;
  try { fn(); } finally { BD.painting = false; }
  const ov2 = document.querySelector('#mroot .ov'); if (ov2) ov2.scrollTop = top;
  if (key && !document.contains(a)) {
    const n = document.querySelector(key);
    if (n) { n.focus({ preventScroll: true }); try { if (s != null) n.setSelectionRange(s, e); } catch (x) {} }
  }
}

/* ---------- filters ---------- */
function bdF0() { return { q: '', labels: [], lmode: 'any', members: [], due: [], lists: [], checks: [] }; }
const bdFCount = () => { const f = UI.bdF; return (f.q.trim() ? 1 : 0) + f.labels.length + f.members.length + f.due.length + f.lists.length + f.checks.length; };
const BD_DUEF = [['none', 'No dates'], ['over', 'Overdue'], ['soon', 'Due soon (within a day)'], ['day', 'Due in the next day'], ['week', 'Due in the next week'], ['month', 'Due in the next month'], ['later', 'Due later than a day'], ['done', 'Marked as complete'], ['notdone', 'Not marked as complete']];
const BD_CHECKF = [['all', 'All items done'], ['some', 'In progress'], ['none', 'Not started'], ['nolist', 'No checklist']];
function bdCheckState(c) { let n = 0, d = 0; (c.checklists || []).forEach(cl => (cl.items || []).forEach(i => { n++; if (i.done) d++; })); return !n ? 'nolist' : d === n ? 'all' : d ? 'some' : 'none'; }
/* BOARDS.filterBy(kind, id): the board's filter set to one thing (the
   views' dashboard drills down with it); the same again clears it. kind is
   list, label, member, due (overdue, soon, later, none, done) or checks
   (all, some, none, nolist); an empty label or member id is "none". */
BOARDS.filterBy = (kind, id) => {
  if (!BD.v) return;
  const v = kind === 'due' ? ({ overdue: 'over' }[id] || id) : (kind === 'label' || kind === 'member') && !id ? '_none' : id;
  const key = { list: 'lists', label: 'labels', member: 'members', due: 'due', checks: 'checks' }[kind];
  if (!key) return;
  const f = UI.bdF, same = bdFCount() === 1 && f[key].length === 1 && f[key][0] === v;
  UI.bdF = bdF0();
  if (!same) UI.bdF[key] = [v];
  bdPaint(); if (BD.pop?.live) bdPopRefresh();
};
function bdMatch(c) {
  const f = UI.bdF;
  if (f.q.trim()) {
    const hay = [c.title, c.description, '#' + c.no, c.link?.label, c.link?.ucc, ...(c.labels || []).map(id => { const l = bdLabel(id); return l ? l.name + ' ' + l.color : ''; }), ...(c.members || []).map(m => m + ' ' + bdName(m))].join(' ').toLowerCase();
    if (!f.q.toLowerCase().split(/\s+/).filter(Boolean).every(w => hay.includes(w))) return false;
  }
  if (f.lists.length && !f.lists.includes(c.list)) return false;
  if (f.checks.length && !f.checks.includes(bdCheckState(c))) return false;
  if (f.labels.length) {
    const has = id => id === '_none' ? !(c.labels || []).some(bdLabel) : (c.labels || []).includes(id);
    if (f.lmode === 'all' ? !f.labels.every(has) : !f.labels.some(has)) return false;
  }
  if (f.members.length && !f.members.some(m => m === '_none' ? !(c.members || []).length : (c.members || []).includes(m))) return false;
  if (f.due.length) {
    const d = bdDueDate(c.due), now = Date.now();
    const time = f.due.filter(x => !['done', 'notdone'].includes(x)), stat = f.due.filter(x => ['done', 'notdone'].includes(x));
    const span = { day: 864e5, week: 7 * 864e5, month: 30 * 864e5 };
    const open = !!d && !c.due_done;
    const inTime = x => x === 'none' ? !d && !c.due_done : x === 'over' ? open && d < now : x === 'soon' ? open && d >= now && d - now <= 864e5 : x === 'later' ? open && d - now > 864e5 : !!d && d >= now && d - now <= span[x];
    if (time.length && !time.some(inTime)) return false;
    if (stat.length && !stat.some(x => x === 'done' ? !!c.due_done : !!d && !c.due_done)) return false;
  }
  return true;
}

/* ---------- the context the other board files get ---------- */
BOARDS.ctx = () => {
  if (!BD.v) return null;
  const all = bdOpenCards();
  return { board: BD.v.board, cards: all.filter(bdMatch), all, role: BD.v.role, users: bdUsers(), el: $('bv-root') || $('bd-view') || $('bd-board'), canEdit: bdCan(), me: bdMe(), filtering: bdFCount() > 0, rev: BD.v.rev };
};
const bdViewId = () => { const v = UI.bdView[UI.bdBoard]; return v && v !== 'board' && BOARDS.views[v] ? v : 'board'; };
const bdSorted = xs => xs.slice().sort((a, b) => (a.order ?? 50) - (b.order ?? 50));
function bdSafe(fn, what) { try { return fn() || ''; } catch (e) { console.warn('Boards: ' + what + ' failed', e); return ''; } }

/* ---------- the screen ---------- */
(function bdDeepLink() {
  const m = /^#boards(?:\/([\w-]+)(?:\/([\w-]+))?)?$/.exec(location.hash || '');
  if (m) { S.view = 'boards'; UI.bdBoard = m[1] || ''; BD.deep = m[2] || ''; }
})();

V.boards = {
  load: async () => {
    if (UI.bdBoard) {
      try { BD.v = await get(bdURL()); }
      catch (e) { if (e.status === 404 || e.status === 403) { toast(e.message, true); UI.bdBoard = ''; BD.v = null; BD.deep = ''; } else throw e; }
    }
    if (BD.last !== UI.bdBoard) { UI.bdF = bdF0(); BD.comp = ''; BD.rename = ''; BD.addList = false; BD.last = UI.bdBoard; }
    if (!UI.bdBoard) {
      BD.v = null;
      const [home] = await Promise.all([get('/v1/boards'), ...BOARDS.homeSections.filter(s => s.load).map(s => Promise.resolve().then(() => s.load()).catch(e => console.warn('Boards: home section', s.id, e)))]);
      BD.home = home;
    }
    bdNotices();
    return {};
  },
  html: () => { bdSaveScroll(); queueMicrotask(bdAfter); return `<div id="bd-root" class="bd-root">${bdInner()}</div>`; }
};
const bdInner = () => UI.bdBoard && BD.v ? bdBoardInner() : bdHomeInner();

function bdPaint() {
  const root = $('bd-root'); if (!root || S.view !== 'boards') return;
  if (BD.drag?.on) { BD.pend = true; return; }
  bdSaveScroll();
  bdKeep(() => { root.innerHTML = bdInner(); });
  bdAfter();
}
function bdAfter() {
  if (S.view !== 'boards' || !$('bd-root')) return;
  bdHash(); bdFit(); bdRestoreScroll();
  const vid = bdViewId();
  if (UI.bdBoard && BD.v && vid !== 'board') bdSafe(() => BOARDS.views[vid].after?.(BOARDS.ctx()), 'view ' + vid);
  if (BD.card && BD.cd && document.querySelector('#mroot .bd-cw')) bdCwPaint();
  if (BD.deep && BD.v) { const c = BD.deep; BD.deep = ''; bdOpenCard(c); }
  document.querySelectorAll('#bd-root .bd-auto').forEach(bdGrow);
  BOARDS.onPaint.forEach(f => bdSafe(() => { f(); }, 'paint hook'));
}
function bdHash() {
  if (S.view !== 'boards') return;
  const h = '#boards' + (UI.bdBoard && BD.v ? '/' + UI.bdBoard + (BD.card ? '/' + BD.card : '') : '');
  if (location.hash !== h) { try { history.replaceState(null, '', h); } catch (e) {} }
}
function bdFit() {
  const w = document.querySelector('#bd-root .bd-wrap'); if (!w) return;
  if (bdViewId() !== 'board') { w.style.height = ''; return; }
  const top = w.getBoundingClientRect().top + scrollY;
  w.style.height = Math.max(440, innerHeight - top - 14) + 'px';
}
function bdSaveScroll() {
  const cv = $('bd-board'); if (!cv) return;
  BD.scroll = { board: UI.bdBoard, x: cv.scrollLeft, lists: Object.fromEntries([...cv.querySelectorAll('.bd-lbody')].map(e => [e.dataset.list, e.scrollTop])) };
}
function bdRestoreScroll() {
  const s = BD.scroll, cv = $('bd-board'); if (!s || !cv || s.board !== UI.bdBoard) return;
  cv.scrollLeft = s.x;
  cv.querySelectorAll('.bd-lbody').forEach(e => { if (s.lists[e.dataset.list] != null) e.scrollTop = s.lists[e.dataset.list]; });
}
function bdGrow(el) { if (!el || el.tagName !== 'TEXTAREA') return; el.style.height = 'auto'; el.style.height = Math.min(480, el.scrollHeight + 2) + 'px'; }

/* Refresh the open board (and card window) from the server. Background
   refreshes give way to changes being saved; a forced one (BOARDS.reload)
   waits for them and always applies what it read. */
async function bdReload(force) {
  if (S.view !== 'boards' || !UI.bdBoard) return;
  if (BD.inflight) {
    if (!force) { bdSoon(); return; }
    for (let i = 0; BD.inflight && i < 200; i++) await new Promise(r => setTimeout(r, 50));
  }
  const id = UI.bdBoard, ver = BD.ver;
  try {
    const v = await get(bdURL());
    let cr = null;
    if (BD.card) { try { cr = await get(bdCURL(BD.card)); } catch (e) { cr = { gone: e }; } }
    if (id !== UI.bdBoard || S.view !== 'boards') return;
    if (ver !== BD.ver || BD.inflight) { bdSoon(); if (!force) return; }
    BD.v = v;
    if (cr && BD.card) {
      if (cr.gone) { const m = cr.gone.message; closeModal(); toast(m, true); }
      else { BD.cd = cr.card; BD.cact = cr.activity || []; }
    }
    if (BD.drag?.on) { BD.pend = true; return; }
    bdPaint();
    if (BD.pop?.live) bdPopRefresh();
  } catch (e) {
    if (e.status === 404 && id === UI.bdBoard) { toast(e.message, true); UI.bdBoard = ''; BD.v = null; closeModal(); render(); }
  }
}
function bdSoon(ms) { clearTimeout(BD.soonT); BD.soonT = setTimeout(bdReload, ms || 600); }
// A Promise: on a board, refreshes it (and an open card window); on the
// boards home, renders the home again.
BOARDS.reload = () => S.view === 'boards' && !UI.bdBoard ? render({ keep: true }) : bdReload(true);
BOARDS.openBoard = id => { UI.bdBoard = id || ''; BD.card = ''; BD.deep = ''; S.view = 'boards'; bdPopClose(true); closeModal(); render(); window.scrollTo(0, 0); };
BOARDS.openCard = (b, c) => {
  if (S.view === 'boards' && UI.bdBoard === b && BD.v && document.querySelector('#bd-root')) { bdOpenCard(c); return; }
  UI.bdBoard = b; BD.deep = c; S.view = 'boards'; bdPopClose(true); closeModal(); render();
};

/* ---------- top bar: back, search, notifications ---------- */
function bdBar() {
  const n = BD.unread;
  return `<div class="bd-bar">
    ${UI.bdBoard ? `<button class="bd-crumb" data-act="bdHome">${bdI('back')}<span>All boards</span></button>` : `<h1 class="bd-h1">Boards</h1>`}
    <span class="bd-sp"></span>
    <div class="bd-srch" role="search">${bdI('search')}<label class="bd-sr" for="bd-search">Search cards on every board</label><input id="bd-search" type="search" placeholder="Search cards" autocomplete="off" data-bdd="search" value="${esc(bdDraft('search', ''))}" aria-controls="bd-sres" aria-expanded="${BD.sresOpen}"><kbd class="bd-kbd" aria-hidden="true">/</kbd><div class="bd-sres" id="bd-sres" ${BD.sresOpen ? '' : 'hidden'}>${bdSresHTML()}</div></div>
    <button class="bd-hb bd-bell${n ? ' on' : ''}" id="bd-bell" data-act="bdBell" data-popkey="bell" aria-label="Notifications${n ? `, ${n} unread` : ''}">${bdI('bell')}${n ? `<span class="bd-bell-n" aria-hidden="true">${n > 99 ? '99+' : n}</span>` : ''}</button>
    <button class="bd-hb" data-act="bdKeys" aria-label="Keyboard shortcuts" title="Keyboard shortcuts (?)">${bdI('keys')}</button>
    ${UI.bdBoard ? '' : `<button class="btn pri bd-new" data-act="bdNew">${bdI('plus')}Create board</button>`}
  </div>`;
}
function bdSresHTML() {
  const r = BD.sres;
  if (!r) return '';
  if (r.loading) return '<div class="bd-sres-m">Searching…</div>';
  if (!r.hits.length) return `<div class="bd-sres-m">No cards match “${esc(r.q)}”.</div>`;
  const where = { title: 'title', description: 'description', comment: 'a comment', checklist: 'a checklist', number: 'card number' };
  return `<div class="bd-sres-h">${bdPl(r.hits.length, 'card')}${r.hits.length >= 100 ? ' (first 100)' : ''}</div>` + r.hits.map(h => `<button class="bd-sr-hit" data-act="bdHit" data-id="${esc(h.board)}" data-card="${esc(h.card)}"><span class="bd-sr-t"><span class="bd-no">#${h.no}</span> ${esc(h.title)}${h.archived ? ' <span class="bd-tag">Archived</span>' : ''}</span><span class="bd-sr-m">${esc(h.board_name)} · ${esc(h.list)}${h.due ? ' · due ' + esc(bdDateTxt(h.due)) : ''} · in ${where[h.where] || esc(h.where)}</span></button>`).join('');
}
function bdSearchSoon(q) {
  clearTimeout(BD.sresT);
  q = q.trim();
  if (q.length < 2) { BD.sres = null; BD.sresOpen = false; const r = $('bd-sres'); if (r) { r.hidden = true; r.innerHTML = ''; } return; }
  BD.sresT = setTimeout(async () => {
    BD.sres = { q, loading: true, hits: [] }; BD.sresOpen = true; bdSresPaint();
    try { const hits = await get('/v1/boards/search?q=' + bdE(q)); if (BD.sres?.q === q) { BD.sres = { q, hits }; bdSresPaint(); } }
    catch (e) { BD.sres = null; toast(e.message, true); bdSresPaint(); }
  }, 250);
}
function bdSresPaint() { const r = $('bd-sres'); if (!r) return; r.innerHTML = bdSresHTML(); r.hidden = !BD.sresOpen || !BD.sres; $('bd-search')?.setAttribute('aria-expanded', String(!r.hidden)); }

async function bdNotices() {
  if (!S.me || clientUser()) return;
  try {
    const r = await get('/v1/boards/notices?limit=50');
    BD.notices = r.notices || []; BD.unread = r.unread || 0;
    const b = $('bd-bell');
    if (b) {
      b.classList.toggle('on', !!BD.unread);
      b.setAttribute('aria-label', `Notifications${BD.unread ? `, ${BD.unread} unread` : ''}`);
      b.querySelector('.bd-bell-n')?.remove();
      if (BD.unread) b.insertAdjacentHTML('beforeend', `<span class="bd-bell-n" aria-hidden="true">${BD.unread > 99 ? '99+' : BD.unread}</span>`);
    }
    if (BD.pop?.key === 'bell') bdPopRefresh();
  } catch (e) {}
}
function bdBellHTML() {
  const ns = BD.notices || [];
  const bname = id => (BD.home || BD.boards || []).find(b => b.id === id)?.name || (BD.v?.board.id === id ? BD.v.board.name : '');
  const text = n => n.by && n.text.startsWith(n.by + ' ') ? bdName(n.by) + n.text.slice(n.by.length) : n.text;
  return (BD.unread ? '<div class="bd-pop-tools"><button class="btn sm" data-act="bdReadAll">Mark all as read</button></div>' : '') +
    (ns.length ? `<ul class="bd-notes">${ns.map(n => `<li><button class="bd-note${n.read ? '' : ' unread'}" data-act="bdNote" data-id="${esc(n.id)}">${n.by ? bdAv(n.by) : `<span class="bd-av sys" aria-hidden="true">${bdI('bell')}</span>`}<span class="bd-note-b"><span class="bd-note-t">${esc(text(n))}</span><span class="bd-ft">${bdWhen(n.at)}${bname(n.board) ? ' · ' + esc(bname(n.board)) : ''}</span></span>${n.read ? '' : '<span class="bd-dot" aria-label="Unread"></span>'}</button></li>`).join('')}</ul>`
      : '<p class="bd-mute">Nothing yet. You will hear here when someone adds you to a card, mentions you, or moves or comments on a card you watch, and about cards on the lists and boards you watch.</p>') +
    BOARDS.bellTools.map(f => bdSafe(f, 'bell tool')).join('');
}

/* ---------- home ---------- */
function bdTile(b) {
  const bg = bdBg(b.background);
  return `<div class="bd-tile bdc-${bg}${b.closed ? ' closed' : ''}"${bdBgStyle(b.background)}>
    <button class="bd-tile-open" data-act="bdOpen" data-id="${esc(b.id)}"><span class="bd-tile-name">${esc(b.name)}</span>
      <span class="bd-tile-meta">${b.visibility === 'private' ? bdI('lock') + 'Private' : bdI('users') + 'Firm-wide'}${b.kind === 'ledgerline' ? ' · Ledgerline' : ''}${b.template ? ' · Template' : ''}${b.role === 'observer' ? ' · View only' : ''}</span></button>
    ${b.closed ? (b.role === 'admin' ? `<button class="btn sm bd-tile-re" data-act="bdReopen" data-id="${esc(b.id)}">Reopen</button>` : '') : `<button class="bd-star${b.starred ? ' on' : ''}" data-act="bdStar" data-id="${esc(b.id)}" aria-pressed="${!!b.starred}" aria-label="${b.starred ? 'Unstar' : 'Star'} ${esc(b.name)}" title="${b.starred ? 'Unstar' : 'Star'}">${bdI('star')}</button>`}
  </div>`;
}
function bdHomeInner() {
  const bs = BD.home || [], open = bs.filter(b => !b.closed);
  const home = Object.assign(bs.slice(), { boards: bs, notices: BD.notices });
  // Another board file may show a page in place of the sections (boardviews.js: All boards, Planner).
  const page = BOARDS.homePage ? bdSafe(() => BOARDS.homePage(home), 'home page') : '';
  if (page) return bdBar() + page;
  const sec = (id, title, sub, body) => `<section class="bd-sec" aria-labelledby="bd-sec-${id}"><div class="bd-sec-h"><h2 id="bd-sec-${id}">${title}</h2>${sub ? `<p>${sub}</p>` : ''}</div>${body}</section>`;
  const grid = (xs, extra = '') => `<div class="bd-tiles">${xs.map(bdTile).join('')}${extra}</div>`;
  const secs = [];
  const star = open.filter(b => b.starred);
  if (star.length) secs.push({ order: 10, html: sec('starred', 'Starred boards', '', grid(star)) });
  const team = open.filter(b => b.kind !== 'ledgerline');
  secs.push({ order: 20, html: sec('team', 'Team boards', scopedUser() ? 'Your private boards and the ones you were added to.' : 'Boards for any piece of work. Firm-wide boards are open to everyone at head office; private boards to their members.', grid(team, `<button class="bd-tile bd-tile-new" data-act="bdNew">${bdI('plus')}<span>Create board</span></button>`)) });
  const ll = open.filter(b => b.kind === 'ledgerline');
  // boardsync.js draws its own, richer "Ledgerline boards" section.
  if (ll.length && !BOARDS.homeSections.some(s => s.id === 'ledgerline')) secs.push({ order: 30, html: sec('ledgerline', 'Ledgerline boards', 'Kept in step with Ledgerline’s own records. Cards move when their record changes.', grid(ll)) });
  const closed = bs.filter(b => b.closed);
  if (closed.length) secs.push({ order: 90, html: sec('closed', 'Closed boards', 'Closed boards keep their cards. A board admin can reopen one.', grid(closed)) });
  BOARDS.homeSections.forEach(s => { const h = bdSafe(() => s.render(home), 'home section ' + s.id); if (h) secs.push({ order: s.order ?? 50, html: h }); });
  return bdBar() + secs.sort((a, b) => a.order - b.order).map(s => s.html).join('');
}

/* ---------- a board ---------- */
function bdBoardInner() {
  const b = BD.v.board, ctx = BOARDS.ctx(), vid = bdViewId(), n = bdFCount();
  let body;
  if (vid === 'board') {
    const lists = bdLists(b);
    body = `<div class="bd-canvas" id="bd-board" role="region" aria-label="Lists on ${esc(b.name)}">${lists.map(l => bdListHTML(l, bdIn(l.id, ctx.cards), ctx.all.filter(c => c.list === l.id).length)).join('')}${bdAddListHTML()}</div>`;
  } else {
    let h = '';
    try { h = BOARDS.views[vid].render(ctx) || ''; } catch (e) { console.warn('Boards: view ' + vid + ' failed', e); h = `<div class="note bad">This view could not be shown. ${esc(e.message)}</div>`; }
    body = `<div class="bd-viewwrap" id="bd-view">${h}</div>`;
  }
  const fnote = n ? `<div class="bd-fnote" role="status">Showing ${ctx.cards.length} of ${bdPl(ctx.all.length, 'card')} · <button class="bd-lnk" data-act="bdFClear">Clear filters</button></div>` : '';
  const closed = b.closed ? `<div class="bd-closed" role="status">${bdI('archive')}<span>This board is closed. ${bdAdmin() ? 'Reopen it to make changes.' : 'Only a board admin can reopen it.'}</span>${bdAdmin() ? '<button class="btn sm" data-act="bdReopen">Reopen board</button>' : ''}</div>` : '';
  return bdBar() + `<div class="bd-wrap bdc-${bdBg(b.background)}"${bdBgStyle(b.background)}>${bdHeadHTML(ctx)}${closed}${fnote}${body}</div>`;
}
function bdHeadHTML(ctx) {
  const b = BD.v.board, me = bdMe(), starred = (b.starred || []).includes(me), n = bdFCount(), vid = bdViewId();
  const nm = bdDraft('bname', b.name);
  const name = bdAdmin() && !b.closed
    ? `<input id="bd-bname" class="bd-bname" data-bdd="bname" value="${esc(nm)}" aria-label="Board name" maxlength="120"${BD_FIT ? '' : ` style="width:${Math.max(4, Math.min(42, nm.length + 1))}ch"`}>`
    : `<h1 class="bd-bname bd-bname-t">${esc(b.name)}</h1>`;
  const views = [['board', 'Board'], ...bdSorted(Object.entries(BOARDS.views).filter(([id]) => id !== 'board').map(([id, x]) => ({ id, label: x.label, order: x.order }))).map(x => [x.id, x.label])];
  const ms = b.members || [];
  return `<div class="bd-head">
    <div class="bd-hl">${name}
      <button class="bd-hb bd-hstar${starred ? ' on' : ''}" data-act="bdStar" data-id="${esc(b.id)}" aria-pressed="${starred}" aria-label="${starred ? 'Unstar this board' : 'Star this board'}" title="${starred ? 'Unstar' : 'Star'}">${bdI('star')}</button>
      ${b.kind === 'inbox' ? `<span class="bd-tag" title="No one else can see your Inbox">${bdI('lock')}Only you</span>` : `<button class="bd-hb" data-act="bdVis" data-popkey="vis" aria-label="Visibility: ${b.visibility === 'private' ? 'Private' : 'Firm-wide'}">${b.visibility === 'private' ? bdI('lock') + '<span>Private</span>' : bdI('users') + '<span>Firm-wide</span>'}</button>`}
      ${bdWatching() ? `<span class="bd-hwatch" title="You are watching this board" role="img" aria-label="You are watching this board">${bdI('eye')}</span>` : ''}
      ${b.kind === 'ledgerline' ? '<span class="bd-tag bd-tag-ll" title="Kept in step with Ledgerline’s records">Ledgerline board</span>' : ''}
      ${BD.v.role === 'observer' ? '<span class="bd-tag">View only</span>' : ''}
    </div>
    <div class="bd-hr">
      ${BD.v.can_join ? `<button class="btn pri sm bd-joinb" data-act="bdJoinBoard" title="Become a member so you can change its lists and cards">${bdI('user')}Join board</button>` : ''}
      ${views.length > 1 ? `<div class="bd-views" role="group" aria-label="Views">${views.map(([id, l]) => `<button class="${id === vid ? 'on' : ''}" aria-pressed="${id === vid}" data-act="bdView" data-id="${esc(id)}">${esc(l)}</button>`).join('')}</div>` : ''}
      ${bdSorted(BOARDS.headerButtons).map(h => bdSafe(() => h.render(ctx), 'header button ' + h.id)).join('')}
      <button class="bd-hb${n ? ' on' : ''}" id="bd-fbtn" data-act="bdFilter" data-popkey="filter" aria-label="Filter cards${n ? `, ${n} on` : ''}" title="Filter (f)">${bdI('filter')}<span>Filter</span>${n ? `<span class="bd-fc">${n}</span>` : ''}</button>
      ${n ? `<button class="bd-hb" data-act="bdFClear" aria-label="Clear filters" title="Clear filters (x)">${bdI('x')}</button>` : ''}
      <span class="bd-avs" aria-label="Board members">${ms.slice(0, 5).map(m => bdAv(m.user)).join('')}${ms.length > 5 ? `<span class="bd-av more">+${ms.length - 5}</span>` : ''}</span>
      ${b.kind === 'inbox' ? '' : `<button class="bd-hb bd-sharebtn" data-act="bdShare">${bdI('users')}<span>Share</span></button>`}
      <button class="bd-hb" data-act="bdMenu" data-popkey="menu" aria-label="Board menu">${bdI('dots')}</button>
    </div>
  </div>`;
}
function bdListHTML(l, cards, total) {
  const can = bdCan(), team = bdTeam(), over = l.limit && total > l.limit, watch = bdLWatching(l);
  const eye = watch ? `<span class="bd-lwatch" title="You are watching this list">${bdI('eye')}${bdSr('Watching')}</span>` : '';
  // A collapsed list (each person's own choice) is a narrow strip: its name and card count.
  if (bdCollapsed(l.id)) {
    return `<section class="bd-list collapsed${l.color ? ' has-c bdc-' + esc(l.color) : ''}" data-list="${esc(l.id)}" aria-label="${esc(l.name)}, collapsed">
      <button class="bd-lcol" data-act="bdListCollapse" data-id="${esc(l.id)}" aria-expanded="false" title="Expand the list" aria-label="Expand the list ${esc(l.name)}, ${bdPl(total, 'card')}">
        <span class="bd-lct${over ? ' over' : ''}" aria-hidden="true">${l.limit ? `${total}/${l.limit}` : total}</span>${eye}<span class="bd-lcol-t" aria-hidden="true">${esc(l.name)}</span></button>
    </section>`;
  }
  const nm = BD.rename === l.id
    ? `<input id="bd-ln-${esc(l.id)}" class="bd-ln-in" data-id="${esc(l.id)}" data-bdd="ln-${esc(l.id)}" data-enter="bdListRenameSave" data-esc="bdListRenameCancel" value="${esc(bdDraft('ln-' + l.id, l.name))}" aria-label="List name" maxlength="120">`
    : `<h3 class="bd-ln">${can && team ? `<button class="bd-ln-b" data-act="bdListRename" data-id="${esc(l.id)}" title="Rename">${esc(l.name)}</button>` : `<span class="bd-ln-b">${esc(l.name)}</span>`}</h3>`;
  const ct = l.limit ? `${total}/${l.limit}` : String(total);
  const comp = BD.comp === l.id && can ? bdCompHTML(l) : '';
  return `<section class="bd-list${l.color ? ' has-c bdc-' + esc(l.color) : ''}" data-list="${esc(l.id)}" aria-label="${esc(l.name)}">
    <header class="bd-lh">${nm}${eye}<span class="bd-lct${over ? ' over' : ''}" title="${over ? `Over the limit of ${l.limit} cards` : l.limit ? `Limit ${l.limit} cards` : bdPl(total, 'card')}${cards.length !== total ? ` · ${cards.length} shown` : ''}">${ct}</span>
      <button class="bd-ib bd-lcolb" data-act="bdListCollapse" data-id="${esc(l.id)}" aria-expanded="true" aria-label="Collapse the list ${esc(l.name)}" title="Collapse">${bdI('collapse')}</button>
      <button class="bd-ib" data-act="bdListMenu" data-id="${esc(l.id)}" data-popkey="list-${esc(l.id)}" aria-label="List actions for ${esc(l.name)}">${bdI('dots')}</button></header>
    <div class="bd-lbody" data-list="${esc(l.id)}"><div class="bd-cards" data-list="${esc(l.id)}">${cards.map(bdCardHTML).join('')}</div>${comp}</div>
    ${can && BD.comp !== l.id ? `<div class="bd-lft"><button class="bd-addc-b" data-act="bdComp" data-id="${esc(l.id)}">${bdI('plus')}Add a card</button></div>` : ''}
  </section>`;
}
function bdCompHTML(l) {
  const ext = BOARDS.composerButtons.map(x => `<button class="btn sm" data-act="bdCompExt" data-id="${esc(x.id)}" data-list="${esc(l.id)}">${esc(x.label)}</button>`).join('');
  return `<div class="bd-comp"><label class="bd-sr" for="bd-comp">Title of the new card in ${esc(l.name)}</label><textarea id="bd-comp" class="bd-comp-in bd-auto" data-bdd="comp" data-enter="bdCompAdd" data-esc="bdCompClose" placeholder="Enter a title for this card" rows="2" maxlength="500">${esc(bdDraft('comp', ''))}</textarea>
    <div class="bd-row"><button class="btn pri sm" data-act="bdCompAdd">Add card</button><button class="bd-ib" data-act="bdCompClose" aria-label="Close the composer">${bdI('x')}</button>${ext}</div></div>`;
}
function bdAddListHTML() {
  if (!bdCan() || !bdTeam()) return '<div class="bd-addl bd-addl-none" aria-hidden="true"></div>';
  if (!BD.addList) return `<div class="bd-addl"><button class="bd-addl-b" data-act="bdAddList">${bdI('plus')}Add another list</button></div>`;
  return `<div class="bd-addl open"><label class="bd-sr" for="bd-al">Name of the new list</label><input id="bd-al" class="in" data-bdd="al" data-enter="bdAddListGo" data-esc="bdAddListClose" placeholder="Enter list name" maxlength="120" value="${esc(bdDraft('al', ''))}">
    <div class="bd-row"><button class="btn pri sm" data-act="bdAddListGo">Add list</button><button class="bd-ib" data-act="bdAddListClose" aria-label="Cancel">${bdI('x')}</button></div></div>`;
}
function bdCardHTML(c) {
  const b = BD.v.board, me = bdMe(), ctx = BOARDS.ctx();
  const labels = (c.labels || []).map(bdLabel).filter(Boolean);
  const cov = b.covers_on && c.cover && (c.cover.color || c.cover.file) ? c.cover : null;
  const full = cov && cov.full;
  const st = bdDueState(c);
  const items = (c.checklists || []).flatMap(cl => cl.items || []), done = items.filter(i => i.done).length;
  const bd = [];
  if ((c.watchers || []).includes(me)) bd.push(`<span class="bd-bdg" title="You are watching this card">${bdI('eye')}${bdSr('Watching')}</span>`);
  if (c.due) bd.push(`<span class="bd-bdg bd-due ${st}" title="${esc(BD_DUEST[st] || 'Due')} ${esc(bdDateTxt(c.due))}">${bdI('clock')}<span>${esc(bdDateTxt(c.due))}</span>${st ? bdSr(', ' + BD_DUEST[st]) : ''}</span>`);
  else if (c.start) bd.push(`<span class="bd-bdg" title="Starts ${esc(bdDateTxt(c.start))}">${bdI('clock')}<span>Starts ${esc(bdDateTxt(c.start))}</span></span>`);
  if (c.description) bd.push(`<span class="bd-bdg" title="This card has a description">${bdI('desc')}${bdSr('Has a description')}</span>`);
  if ((c.comments || []).length) bd.push(`<span class="bd-bdg" title="${bdPl(c.comments.length, 'comment')}">${bdI('comment')}<span>${c.comments.length}</span>${bdSr(' comments')}</span>`);
  if ((c.attachments || []).length) bd.push(`<span class="bd-bdg" title="${bdPl(c.attachments.length, 'attachment')}">${bdI('clip')}<span>${c.attachments.length}</span>${bdSr(' attachments')}</span>`);
  if (items.length) bd.push(`<span class="bd-bdg${done === items.length ? ' bd-ok' : ''}" title="Checklist items done">${bdI('check')}<span>${done}/${items.length}</span>${bdSr(' checklist items done')}</span>`);
  if ((c.votes || []).length) bd.push(`<span class="bd-bdg${c.votes.includes(me) ? ' bd-mine' : ''}" title="${esc(bdPl(c.votes.length, 'vote') + ': ' + c.votes.map(bdName).join(', '))}">${bdI('vote')}<span>${c.votes.length}</span>${bdSr(' votes')}</span>`);
  const ext = BOARDS.cardBadges.map(f => bdSafe(() => f(c, ctx), 'card badge')).join('');
  const coverHTML = !cov || full ? '' : cov.file ? `<div class="bd-cover img"><img src="/v1/board-files/${bdE(cov.file)}" alt="" loading="lazy" draggable="false"></div>` : `<div class="bd-cover bdc-${esc(cov.color)}"></div>`;
  const fullStyle = full && cov.file ? ` style="background-image:url('/v1/board-files/${bdE(cov.file)}')"` : '';
  const lab = labels.length ? `<div class="bd-labs">${labels.map(l => `<span class="bd-lab bdc-${esc(l.color)}${UI.bdNames ? ' named' : ''}" data-act="bdLabNames" title="${esc(l.name || BD_CN[l.color])}">${UI.bdNames ? esc(l.name || '') : ''}</span>`).join('')}</div>` : '';
  const srLab = labels.length ? bdSr(', labels: ' + labels.map(l => esc(l.name || BD_CN[l.color])).join(', ')) : '';
  // Mark complete from the front: a round box on hover (always when complete, and on touch screens).
  const doneBox = c.due && bdCan() && !c.mirror ? `<input type="checkbox" class="bd-cdone" data-act="bdCardDone" data-id="${esc(c.id)}" ${c.due_done ? 'checked' : ''} aria-label="Complete: #${c.no} ${esc(c.title)}" title="${c.due_done ? 'Mark incomplete' : 'Mark complete'}">` : '';
  return `<article class="bd-card${full ? ' full' + (cov.file ? ' fimg' : ' bdc-' + esc(cov.color)) : ''}${st === 'over' ? ' late' : ''}${c.due_done && c.due ? ' done' : ''}" data-card="${esc(c.id)}" data-list="${esc(c.list)}"${fullStyle}>
    ${coverHTML}<div class="bd-cin">${full ? '' : lab}
      <h4 class="bd-ct">${doneBox}<button class="bd-copen" data-act="bdCard" data-id="${esc(c.id)}"><span class="bd-no">#${c.no}</span> ${esc(c.title)}${srLab}</button></h4>
      ${full ? '' : `${bd.length || (c.members || []).length ? `<div class="bd-bdgs">${bd.join('')}<span class="bd-sp"></span>${(c.members || []).map(m => bdAv(m, 'sm')).join('')}</div>` : ''}
      ${c.link ? `<div class="bd-linkc" title="Linked to ${esc(c.link.label)}">${bdI('link')}<span>${esc(c.link.label)}</span>${c.link.status ? `<span class="bd-linkst">${esc(c.link.status)}</span>` : ''}</div>` : ''}
      ${ext ? `<div class="bd-bext">${ext}</div>` : ''}`}
    </div>
    <button class="bd-cq bd-ib" data-act="bdCardMenu" data-id="${esc(c.id)}" data-popkey="cq-${esc(c.id)}" aria-label="Actions for card #${c.no}">${bdI('dots')}</button>
  </article>`;
}

/* ---------- popovers ---------- */
function bdPop(key, anchor, title, renderFn, opts = {}) {
  if (BD.pop?.key === key) { bdPopClose(); return; }
  bdPopClose(true);
  const el = document.createElement('div');
  el.className = 'bd-pop' + (opts.wide ? ' wide' : '');
  el.id = 'bd-pop'; el.setAttribute('role', 'dialog'); el.setAttribute('aria-label', title);
  // Live popovers (pickers, filter, bell, menus) are drawn again when the
  // board refreshes; forms are not, so nothing typed in them is lost.
  const live = opts.live ?? ['labels', 'members', 'filter', 'bell', 'cover', 'menu', 'vis'].includes(key);
  BD.pop = { key, el, anchor, render: renderFn, title, ret: anchor || document.activeElement, back: opts.back, live };
  el.innerHTML = bdPopInner();
  document.body.appendChild(el);
  bdPopPlace();
  setTimeout(() => { if (BD.pop?.el !== el) return; (el.querySelector('[data-autofocus]') || el.querySelector('.bd-pop-bd input:not([type=checkbox]):not([type=radio]),.bd-pop-bd textarea,.bd-pop-bd select') || el.querySelector('.bd-pop-bd button,.bd-pop-bd input') || el.querySelector('button'))?.focus({ preventScroll: true }); }, 0);
}
function bdPopInner() {
  const p = BD.pop;
  return `<div class="bd-pop-hd">${p.back ? `<button class="bd-ib" data-act="${p.back}" aria-label="Back">${bdI('back')}</button>` : '<span class="bd-ib-sp"></span>'}<h3>${esc(p.title)}</h3><button class="bd-ib" data-act="bdPopClose" aria-label="Close">${bdI('x')}</button></div><div class="bd-pop-bd">${bdSafe(p.render, 'popover')}</div>`;
}
function bdPopRefresh() { const p = BD.pop; if (!p) return; bdKeep(() => { p.el.innerHTML = bdPopInner(); p.el.setAttribute('aria-label', p.title); }); bdPopPlace(); }
function bdPopSub(title, back) { const p = BD.pop; if (!p) return; p.title = title; p.back = back || ''; bdPopRefresh(); setTimeout(() => (p.el.querySelector('.bd-pop-bd input:not([type=checkbox]),.bd-pop-bd select,.bd-pop-bd button') || p.el.querySelector('button'))?.focus({ preventScroll: true }), 0); }
function bdPopClose(silent) {
  const p = BD.pop; if (!p) return;
  BD.pop = null; p.el.remove(); BD.mv = null;
  if (!silent) { const r = p.ret && document.contains(p.ret) ? p.ret : document.querySelector(`[data-popkey="${CSS.escape(p.key)}"]`); r?.focus({ preventScroll: true }); }
}
function bdPopPlace() {
  const p = BD.pop; if (!p) return;
  const el = p.el;
  if (innerWidth <= 600) { el.classList.add('sheet'); el.style.left = ''; el.style.top = ''; return; }
  el.classList.remove('sheet');
  const a = p.anchor && document.contains(p.anchor) ? p.anchor : document.querySelector(`[data-popkey="${CSS.escape(p.key)}"]`);
  const r = a ? a.getBoundingClientRect() : { left: innerWidth / 2 - 150, top: 90, bottom: 90 };
  el.style.maxHeight = '';
  const w = el.offsetWidth, h = el.offsetHeight, below = innerHeight - r.bottom - 14, above = r.top - 14;
  const left = Math.min(Math.max(8, r.left), innerWidth - w - 8);
  // Below the button if it fits; otherwise on the roomier side, scrolling inside.
  let top = r.bottom + 6;
  if (h > below) {
    if (below >= above || below >= 320) el.style.maxHeight = Math.max(160, below) + 'px';
    else { el.style.maxHeight = above + 'px'; top = r.top - Math.min(h, above) - 6; }
  }
  el.style.left = left + 'px'; el.style.top = Math.max(8, top) + 'px';
}

/* ---------- the card window ---------- */
async function bdOpenCard(id) {
  if (!BD.v) return;
  const fresh = BD.card !== id;
  BD.card = id;
  if (fresh) { BD.cw = {}; BD.cact = []; }
  const c0 = (BD.v.cards || []).find(x => x.id === id);
  if (c0) { BD.cd = c0; bdCwShow(); }
  try {
    const r = await get(bdCURL(id));
    if (BD.card !== id) return;
    BD.cd = r.card; BD.cact = r.activity || [];
    bdCwShow();
  } catch (e) {
    // A card that went to another board (with its list, or alone): follow it.
    const mt = e.details?.moved_to;
    if (mt?.board && mt.card && BD.card === id) { BD.card = ''; BD.cd = null; toast('This card moved to another board'); BOARDS.openCard(mt.board, mt.card); return; }
    toast(e.message, true);
    if (BD.card === id && !c0) { BD.card = ''; BD.cd = null; bdHash(); }
  }
}
function bdCwShow() {
  if (document.querySelector('#mroot .bd-cw')) { bdCwPaint(); return; }
  BD.cwRet = document.activeElement;
  $('mroot').innerHTML = `<div class="ov bd-ov" data-act="ovclose"><div class="md bd-cw" role="dialog" aria-modal="true" aria-labelledby="cw-h" tabindex="-1"><div class="bd-cw-in" id="bd-cw-in">${bdCwHTML()}</div></div></div>`;
  bdHash();
  document.querySelectorAll('.bd-cw .bd-auto').forEach(bdGrow);
  setTimeout(() => document.querySelector('#mroot .bd-cw')?.focus({ preventScroll: true }), 0);
}
function bdCwPaint() {
  const el = $('bd-cw-in'); if (!el || !BD.cd) return;
  bdKeep(() => { el.innerHTML = bdCwHTML(); });
  el.querySelectorAll('.bd-auto').forEach(bdGrow);
  if (BD.ac && !$(BD.ac.id)) bdAcClose();
  bdPopPlace();
}
function bdCwClosed() {
  const id = BD.card;
  BD.card = ''; BD.cd = null; BD.cw = {}; BD.cact = [];
  bdPopClose(true); bdAcClose();
  if (S.view === 'boards') {
    bdHash();
    const r = BD.cwRet && document.contains(BD.cwRet) ? BD.cwRet : document.querySelector(`.bd-card[data-card="${CSS.escape(id)}"] .bd-copen`);
    r?.focus({ preventScroll: true });
  }
}
new MutationObserver(() => { if (BD.card && !document.querySelector('#mroot .bd-cw')) bdCwClosed(); }).observe($('mroot'), { childList: true });

function bdBlock(title, html) { return `<div class="bd-mb"><h4>${title}</h4><div class="bd-mb-b">${html}</div></div>`; }
function bdCwHTML() {
  const c = BD.cd, b = BD.v.board, me = bdMe(), can = bdCan(), ctx = BOARDS.ctx();
  // A mirror shows another card: its title, description and dates are
  // changed on the original (the extras' mirror section links to it).
  const own = can && !c.mirror;
  const l = b.lists.find(x => x.id === c.list);
  const watching = (c.watchers || []).includes(me), voted = (c.votes || []).includes(me), member = (c.members || []).includes(me);
  const cover = c.cover && (c.cover.file || c.cover.color) ? (c.cover.file ? `<div class="bd-cw-cover img"><img src="/v1/board-files/${bdE(c.cover.file)}" alt=""></div>` : `<div class="bd-cw-cover bdc-${esc(c.cover.color)}"></div>`) : '';
  const title = BD.cw.title && own
    ? `<textarea id="cw-title" class="bd-cw-tin bd-auto" data-bdd="cw-title" data-enter="bdTitleSave" data-esc="bdTitleCancel" rows="1" aria-label="Card title" maxlength="500">${esc(bdDraft('cw-title', c.title))}</textarea>`
    : `<h2 class="bd-cw-t" id="cw-h">${own ? `<button class="bd-cw-tb" data-act="bdTitleEdit" title="Edit the title">${esc(c.title)}</button>` : esc(c.title)}</h2>`;
  const st = bdDueState(c);
  const labels = (c.labels || []).map(bdLabel).filter(Boolean);
  const meta = [];
  if ((c.members || []).length || can) meta.push(bdBlock('Members', `${(c.members || []).map(m => bdAv(m)).join('')}${can ? `<button class="bd-addc" data-act="bdPickMembers" data-popkey="members" aria-label="Change members">${bdI('plus')}</button>` : ''}`));
  if (labels.length || can) meta.push(bdBlock('Labels', `${labels.map(x => `<span class="bd-pill bdc-${esc(x.color)}">${esc(x.name || BD_CN[x.color])}</span>`).join('')}${can ? `<button class="bd-addc" data-act="bdPickLabels" data-popkey="labels" aria-label="Change labels">${bdI('plus')}</button>` : ''}`));
  meta.push(bdBlock('Notifications', `<button class="btn sm${watching ? ' on' : ''}" id="cw-watch" data-act="bdWatch" aria-pressed="${watching}">${bdI('eye')}${watching ? 'Watching' : 'Watch'}</button>`));
  if (c.start || c.due) {
    const txt = [c.start ? bdDateTxt(c.start) : '', c.due ? bdDateTxt(c.due) : ''].filter(Boolean).join(' – ');
    meta.push(bdBlock(c.start && c.due ? 'Dates' : c.due ? 'Due date' : 'Start date', `${c.due ? `<input type="checkbox" class="bd-chk" id="cw-done" data-act="bdDueDone" ${c.due_done ? 'checked' : ''} ${own ? '' : 'disabled'} aria-label="Mark complete">` : ''}<button class="btn sm bd-datebtn" data-act="bdPickDates" data-popkey="dates" ${own ? '' : 'disabled'}>${esc(txt)}${st ? `<span class="bd-st ${st}">${BD_DUEST[st]}</span>` : ''}${c.reminder ? bdSr(', reminder set') : ''}</button>`));
  }
  const voters = (c.votes || []).map(v => v === me ? 'you' : bdName(v));
  meta.push(bdBlock('Votes', `<button class="btn sm${voted ? ' on' : ''}" id="cw-vote" data-act="bdVote" aria-pressed="${voted}" ${BD.v.board.closed ? 'disabled' : ''}>${bdI('vote')}${voted ? 'Voted' : 'Vote'}${voters.length ? ` · ${voters.length}` : ''}</button>${voters.length ? `<span class="bd-voters">${esc(voters.join(', '))}</span>` : ''}`));
  if (c.link) meta.push(bdBlock('Linked record', `<button class="btn sm" data-act="bdLinkGo" ${c.link.view && viewAllowed(c.link.view) ? '' : 'disabled'} title="${c.link.view ? 'Open the record in Ledgerline' : ''}">${bdI('link')}${esc(c.link.label)}${c.link.status ? ` · ${esc(c.link.status)}` : ''}</button>`));

  const secs = [
    { order: 10, html: bdDescHTML(c, own) },
    { order: 30, html: (c.attachments || []).length ? bdAttsHTML(c, can) : '' },
    { order: 40, html: bdChecklistsHTML(c, can) },
    { order: 90, html: bdFeedHTML(c) },
    ...BOARDS.cardSections.map(s => ({ order: s.order ?? 50, html: bdSafe(() => s.render(c, ctx), 'card section ' + s.id), id: s.id }))
  ].filter(s => s.html).sort((a, b) => a.order - b.order);

  const sb = (id, act, icon, label, extra = '') => `<button class="bd-sb" id="cw-sb-${id}" data-act="${act}" ${extra}>${bdI(icon)}<span>${label}</span></button>`;
  const ext = g => BOARDS.cardButtons.filter(x => x.group === g && (x.show ? bdSafe(() => x.show(c, ctx) ? 'y' : '', 'card button ' + x.id) : (g === 'actions' || can))).map(x => ({ order: x.order ?? 70, html: `<button class="bd-sb" data-act="bdExtBtn" data-id="${esc(x.id)}">${bdI(BD_IC[x.icon] ? x.icon : g === 'add' ? 'plus' : 'arrow')}<span>${esc(x.label)}</span></button>` }));
  const add = [
    ...(can ? [
      { order: 5, html: member ? '' : sb('join', 'bdJoin', 'user', 'Join') },
      { order: 10, html: sb('members', 'bdPickMembers', 'user', 'Members', 'data-popkey="members"') },
      { order: 20, html: sb('labels', 'bdPickLabels', 'tag', 'Labels', 'data-popkey="labels"') },
      { order: 30, html: sb('checklist', 'bdPickChecklist', 'check', 'Checklist', 'data-popkey="checklist"') },
      { order: 40, html: own ? sb('dates', 'bdPickDates', 'clock', 'Dates', 'data-popkey="dates"') : '' },
      { order: 50, html: sb('attach', 'bdPickAttach', 'clip', 'Attachment', 'data-popkey="attach"') },
      { order: 60, html: sb('cover', 'bdPickCover', 'image', 'Cover', 'data-popkey="cover"') }] : []),
    ...ext('add')].filter(x => x.html);
  const acts = [
    { order: 10, html: can ? sb('move', 'bdMoveCard', 'arrow', 'Move', `data-id="${esc(c.id)}" data-popkey="move-${esc(c.id)}"`) : '' },
    { order: 20, html: bdCan() ? sb('copy', 'bdCopyCard', 'copy', 'Copy', `data-id="${esc(c.id)}" data-popkey="copy-${esc(c.id)}"`) : '' },
    { order: 30, html: sb('watch', 'bdWatch', 'eye', watching ? 'Stop watching' : 'Watch', `aria-pressed="${watching}"`) },
    { order: 40, html: bdCan() ? (c.archived ? sb('restore', 'bdRestoreCard', 'archive', 'Send to board') : sb('archive', 'bdArchiveCard', 'archive', 'Archive')) : '' },
    { order: 50, html: c.archived && bdAdmin() && !BD.v.board.closed ? sb('delete', 'bdDeleteCard', 'trash', 'Delete', 'data-popkey="cdel"') : '' },
    { order: 60, html: sb('link', 'bdCopyLink', 'link', 'Copy link') },
    ...ext('actions')].filter(x => x.html);

  return `${cover}<div class="bd-cw-hd">${bdI('card', 'bd-cw-ic')}<div class="bd-cw-hm">${title}
      <div class="bd-cw-sub"><span class="bd-no">#${c.no}</span> in list ${can ? `<button class="bd-lnk" data-act="bdMoveCard" data-id="${esc(c.id)}" data-popkey="move-${esc(c.id)}">${esc(l?.name || '')}</button>` : `<b>${esc(l?.name || '')}</b>`}${b.closed ? ' · board closed' : ''}${BD.v.role === 'observer' && bdCanComment() ? ' · you can comment' : ''}</div></div>
    <button class="x bd-cw-x" data-act="close" aria-label="Close card">${bdI('x')}</button></div>
    ${c.archived ? `<div class="bd-cw-arch" role="status">${bdI('archive')}<span>This card is archived.</span></div>` : ''}
    <div class="bd-cw-grid"><div class="bd-cw-main"><div class="bd-mbs">${meta.join('')}</div>${secs.map(s => s.id ? `<div class="bd-cs-ext" data-sec="${esc(s.id)}">${s.html}</div>` : s.html).join('')}</div>
    <aside class="bd-cw-side" aria-label="Card actions">${add.length ? `<h4>Add to card</h4>${bdSorted(add).map(x => x.html).join('')}` : ''}<h4>Actions</h4>${bdSorted(acts).map(x => x.html).join('')}</aside></div>`;
}
function bdDescHTML(c, can) {
  const ed = BD.cw.desc && can;
  const body = ed
    ? `<label class="bd-sr" for="cw-desc">Description</label>${buEdBar('cw-desc')}<textarea id="cw-desc" class="in bd-auto bd-desc-in" data-bdd="desc-${esc(c.id)}" data-center="bdDescSave" data-esc="bdDescCancel" data-mention="1" data-bued="1" rows="5" maxlength="20000" placeholder="Add a more detailed description…">${esc(bdDraft('desc-' + c.id, c.description || ''))}</textarea>
      <div class="bd-row"><button class="btn pri sm" data-act="bdDescSave">Save</button><button class="btn sm" data-act="bdDescCancel">Cancel</button><span class="bd-hint">Markdown: **bold**, _italic_, ~~strike~~, # headings, &gt; quotes, \`\`\` code, | tables, :smile: emoji. Ctrl+Enter saves.</span></div>`
    : c.description ? `<div class="bd-md">${bdMd(c.description, true)}</div>` : can ? `<button class="bd-desc-empty" data-act="bdDescEdit">Add a more detailed description…</button>` : '<p class="bd-mute">No description.</p>';
  return `<section class="bd-cs"><div class="bd-cs-hd">${bdI('desc')}<h3>Description</h3><span class="bd-sp"></span>${can && !ed && c.description ? '<button class="btn sm" data-act="bdDescEdit">Edit</button>' : ''}</div>${body}</section>`;
}
function bdAttsHTML(c, can) {
  const cov = c.cover?.file;
  return `<section class="bd-cs"><div class="bd-cs-hd">${bdI('clip')}<h3>Attachments</h3><span class="bd-sp"></span>${can ? '<button class="btn sm" data-act="bdPickAttach" data-popkey="attach">Add</button>' : ''}</div>
    <ul class="bd-atts">${c.attachments.map(a => {
      const url = a.file ? `/v1/board-files/${bdE(a.file)}` : a.url;
      const ext = ((a.name || '').includes('.') ? a.name.split('.').pop() : '').slice(0, 4).toUpperCase();
      let host = ''; try { host = a.url ? new URL(a.url).host : ''; } catch (e) {}
      const thumb = a.image ? `<a class="bd-ath img" href="${esc(url)}" target="_blank" rel="noopener" aria-label="Open ${esc(a.name)}"><img src="${esc(url)}" alt="" loading="lazy"></a>`
        : a.url ? `<a class="bd-ath" href="${esc(url)}" target="_blank" rel="noopener noreferrer" aria-label="Open the link ${esc(a.name)}">${bdI('link')}</a>`
          : `<a class="bd-ath" href="${esc(url)}" download="${esc(a.name)}" aria-label="Download ${esc(a.name)}"><b>${esc(ext || 'FILE')}</b></a>`;
      return `<li class="bd-att">${thumb}<div class="bd-att-b"><a class="bd-att-n" href="${esc(url)}" ${a.url ? 'target="_blank" rel="noopener noreferrer"' : a.image ? 'target="_blank" rel="noopener"' : `download="${esc(a.name)}"`}>${esc(a.name)}</a>
        <div class="bd-ft">Added ${bdWhen(a.at)} by ${esc(bdName(a.by))}${a.size ? ' · ' + bdBytes(a.size) : ''}${host ? ' · ' + esc(host) : ''}${cov && cov === a.file ? ' · <b>Cover</b>' : ''}</div>
        ${can ? `<div class="bd-att-a">${a.image ? (cov === a.file ? `<button class="bd-lnk" data-act="bdCoverOff">Remove cover</button>` : `<button class="bd-lnk" data-act="bdCoverImg" data-id="${esc(a.file)}">Make cover</button>`) : ''}<button class="bd-lnk" data-act="bdAttDel" data-id="${esc(a.id)}" data-popkey="attdel-${esc(a.id)}">Delete</button></div>` : ''}</div></li>`;
    }).join('')}</ul>${can ? '<p class="bd-hint">Drop files on this window, or paste an image, to attach them.</p>' : ''}</section>`;
}
function bdChecklistsHTML(c, can) {
  return (c.checklists || []).map(cl => {
    const items = cl.items || [], done = items.filter(i => i.done).length, pct = items.length ? Math.round(done * 100 / items.length) : 0;
    const hide = !!UI.bdHideDone[cl.id], shown = items.filter(i => !hide || !i.done);
    const nm = BD.cw.clName === cl.id && can
      ? `<input id="clnm-${esc(cl.id)}" class="in bd-cl-in" data-id="${esc(cl.id)}" data-bdd="clnm-${esc(cl.id)}" data-enter="bdClRenameSave" data-esc="bdClRenameCancel" value="${esc(bdDraft('clnm-' + cl.id, cl.name))}" aria-label="Checklist name" maxlength="200">`
      : `<h3>${can ? `<button class="bd-cl-nb" data-act="bdClRename" data-id="${esc(cl.id)}" title="Rename">${esc(cl.name)}</button>` : esc(cl.name)}</h3>`;
    return `<section class="bd-cs bd-cl" aria-label="Checklist ${esc(cl.name)}"><div class="bd-cs-hd">${bdI('check')}${nm}<span class="bd-sp"></span>
        ${done ? `<button class="btn sm" data-act="bdClHide" data-id="${esc(cl.id)}" aria-pressed="${hide}">${hide ? `Show checked items (${done})` : 'Hide checked items'}</button>` : ''}
        ${can ? `<button class="btn sm" data-act="bdClDel" data-id="${esc(cl.id)}" data-popkey="cldel-${esc(cl.id)}">Delete</button>` : ''}</div>
      <div class="bd-prog"><span class="bd-prog-n">${pct}%</span><div class="bd-pbar${pct === 100 ? ' full' : ''}" role="progressbar" aria-valuenow="${pct}" aria-valuemin="0" aria-valuemax="100" aria-label="${done} of ${items.length} done"><i style="width:${pct}%"></i></div></div>
      <ul class="bd-its" data-cl="${esc(cl.id)}">${shown.map(it => bdItemHTML(cl, it, can)).join('')}</ul>
      ${hide && items.length && done === items.length ? '<p class="bd-mute">Everything in this checklist is done.</p>' : ''}
      ${can ? (BD.cw.addItem === cl.id
        ? `<div class="bd-it-add"><label class="bd-sr" for="itadd-${esc(cl.id)}">New item</label><textarea id="itadd-${esc(cl.id)}" class="in bd-auto" rows="1" data-id="${esc(cl.id)}" data-bdd="itadd-${esc(cl.id)}" data-enter="bdItAdd" data-esc="bdItAddClose" placeholder="Add an item" maxlength="500">${esc(bdDraft('itadd-' + cl.id, ''))}</textarea><div class="bd-row"><button class="btn pri sm" data-act="bdItAdd" data-id="${esc(cl.id)}">Add</button><button class="btn sm" data-act="bdItAddClose">Cancel</button></div></div>`
        : `<button class="btn sm bd-it-addb" data-act="bdItAddOpen" data-id="${esc(cl.id)}">Add an item</button>`) : ''}
    </section>`;
  }).join('');
}
function bdItemHTML(cl, it, can) {
  const items = cl.items || [], idx = items.indexOf(it), ed = BD.cw.itEdit === it.id && can;
  const a = `data-id="${esc(it.id)}" data-cl="${esc(cl.id)}"`;
  const due = it.due ? `<button class="bd-chip${!it.done && bdDueDate(it.due) < Date.now() ? ' over' : ''}" data-act="bdItDue" ${a} data-popkey="itdue-${esc(it.id)}" ${can ? '' : 'disabled'} aria-label="Due ${esc(bdDateTxt(it.due))}${can ? ', change' : ''}">${bdI('clock')}${esc(bdDateTxt(it.due))}</button>` : '';
  const mem = it.member ? `<button class="bd-chip bd-chip-av" data-act="bdItMember" ${a} data-popkey="itmem-${esc(it.id)}" ${can ? '' : 'disabled'} aria-label="Assigned to ${esc(bdName(it.member))}${can ? ', change' : ''}">${bdAv(it.member, 'sm')}</button>` : '';
  return `<li class="bd-it${it.done ? ' done' : ''}" data-it="${esc(it.id)}" data-cl="${esc(cl.id)}">
    ${can ? `<span class="bd-it-grip" title="Drag to reorder" aria-hidden="true">${bdI('grip')}</span>` : ''}
    <input type="checkbox" class="bd-chk" id="it-${esc(it.id)}" data-act="bdItTick" ${a} ${it.done ? 'checked' : ''} ${can ? '' : 'disabled'} aria-label="${esc(it.text)}">
    ${ed ? `<div class="bd-it-ed"><textarea id="ited-${esc(it.id)}" class="in bd-auto" rows="1" ${a} data-bdd="ited-${esc(it.id)}" data-enter="bdItSave" data-esc="bdItCancel" aria-label="Item text" maxlength="500">${esc(bdDraft('ited-' + it.id, it.text))}</textarea>
        <div class="bd-row"><button class="btn pri sm" data-act="bdItSave" ${a}>Save</button><button class="btn sm" data-act="bdItCancel">Cancel</button><span class="bd-sp"></span><button class="btn sm bad" data-act="bdItDel" ${a}>Delete</button></div></div>`
      : `<span class="bd-it-t">${can ? `<button class="bd-it-tb" data-act="bdItEdit" ${a}>${esc(it.text)}</button>` : esc(it.text)}</span>`}
    <span class="bd-it-meta">${due}${mem}${can && !ed ? `<span class="bd-it-acts">
      ${it.due ? '' : `<button class="bd-ib" data-act="bdItDue" ${a} data-popkey="itdue-${esc(it.id)}" aria-label="Set a due date for ${esc(it.text)}" title="Due date">${bdI('clock')}</button>`}
      ${it.member ? '' : `<button class="bd-ib" data-act="bdItMember" ${a} data-popkey="itmem-${esc(it.id)}" aria-label="Assign ${esc(it.text)} to someone" title="Assign">${bdI('user')}</button>`}
      <button class="bd-ib" data-act="bdItUp" ${a} ${idx <= 0 ? 'disabled' : ''} aria-label="Move ${esc(it.text)} up" title="Move up">${bdI('up')}</button>
      <button class="bd-ib" data-act="bdItDown" ${a} ${idx >= items.length - 1 ? 'disabled' : ''} aria-label="Move ${esc(it.text)} down" title="Move down">${bdI('down')}</button>
      <button class="bd-ib" data-act="bdItConvert" ${a} aria-label="Convert ${esc(it.text)} to a card" title="Convert to card">${bdI('card')}</button></span>` : ''}</span>
  </li>`;
}
function bdFeedHTML(c) {
  const me = bdMe(), canC = bdCanComment(), why = !canC && BD.v.role && !BD.v.board.closed ? (bdPerms().comments === 'off' ? 'Commenting is switched off on this board.' : 'Only the board’s members can comment on it.') : '';
  const items = (c.comments || []).map(cm => ({ t: new Date(cm.at).getTime(), html: bdCommentHTML(cm, canC) }));
  if (UI.bdDetails) (BD.cact || []).filter(a => a.what !== 'comment.added').forEach(a => items.push({ t: new Date(a.at).getTime(), html: `<li class="bd-fa">${bdAv(a.by)}<div><span><b>${esc(bdName(a.by))}</b> ${esc(a.text)}</span><div class="bd-ft">${bdWhen(a.at)}</div></div></li>` }));
  items.sort((a, b) => b.t - a.t);
  return `<section class="bd-cs" aria-label="Comments and activity"><div class="bd-cs-hd">${bdI('activity')}<h3>Comments and activity</h3><span class="bd-sp"></span><button class="btn sm" data-act="bdDetails" aria-pressed="${!!UI.bdDetails}">${UI.bdDetails ? 'Hide details' : 'Show details'}</button></div>
    ${canC ? `<div class="bd-cmc">${bdAv(me)}<div class="bd-cmc-b"><label class="bd-sr" for="cw-cm">Write a comment</label><textarea id="cw-cm" class="in bd-auto" rows="2" data-bdd="cm-${esc(c.id)}" data-center="bdCmAdd" data-mention="1" placeholder="Write a comment… Type @ to mention someone" maxlength="5000">${esc(bdDraft('cm-' + c.id, ''))}</textarea>
      <div class="bd-row"><button class="btn pri sm" data-act="bdCmAdd">Save</button><span class="bd-hint">Ctrl+Enter saves. @card tells the card’s members and watchers${['admin', 'member'].includes(BD.v.role) ? '; @board everyone on the board' : ''}.</span></div></div></div>` : why ? `<p class="bd-mute bd-cmno">${why}</p>` : ''}
    <ul class="bd-feed">${items.map(x => x.html).join('') || '<li class="bd-mute">No comments yet.</li>'}</ul></section>`;
}
/* An @board or @card that told people (the server lists them in groups)
   shows as a mention; an observer's @board stays plain text. */
function bdGroupsHTML(h, groups) {
  if (!(groups || []).length) return h;
  const t = { board: 'Everyone on this board', card: 'The card’s members and watchers' };
  return h.replace(/(^|[\s(>])@(board|card)(?![a-z0-9._-]*[a-z0-9])/g, (all, p, g) => groups.includes(g) ? `${p}<span class="bd-mention bd-mgroup" title="${t[g]}">@${g}</span>` : all);
}
function bdCommentHTML(cm, canC) {
  const me = bdMe(), mine = cm.by === me, ed = BD.cw.edit === cm.id && mine && canC;
  const rx = Object.entries(cm.reactions || {}).map(([e, us]) => `<button class="bd-rx${us.includes(me) ? ' on' : ''}" data-act="bdReact" data-id="${esc(cm.id)}" data-e="${esc(e)}" aria-pressed="${us.includes(me)}" ${canC ? '' : 'disabled'} title="${esc(us.map(bdName).join(', '))}"><span aria-hidden="true">${e}</span> ${us.length}${bdSr(' reacted ' + e)}</button>`).join('');
  return `<li class="bd-cm" id="cm-${esc(cm.id)}">${bdAv(cm.by)}<div class="bd-cm-b">
    <div class="bd-cm-h"><b>${esc(bdName(cm.by))}</b><span class="bd-ft">${bdWhen(cm.at)}${cm.edited ? ' · edited' : ''}</span></div>
    ${ed ? `<label class="bd-sr" for="cmed-${esc(cm.id)}">Edit comment</label><textarea id="cmed-${esc(cm.id)}" class="in bd-auto" rows="2" data-id="${esc(cm.id)}" data-bdd="cmed-${esc(cm.id)}" data-center="bdCmSave" data-esc="bdCmCancel" data-mention="1" maxlength="5000">${esc(bdDraft('cmed-' + cm.id, cm.text))}</textarea><div class="bd-row"><button class="btn pri sm" data-act="bdCmSave" data-id="${esc(cm.id)}">Save</button><button class="btn sm" data-act="bdCmCancel">Cancel</button></div>`
      : `<div class="bd-md bd-cm-t">${bdGroupsHTML(bdMd(cm.text, true), cm.groups)}</div>`}
    <div class="bd-cm-f">${rx}${canC ? `<button class="bd-rxadd" data-act="bdReactPick" data-id="${esc(cm.id)}" data-popkey="rx-${esc(cm.id)}" aria-label="Add a reaction" title="Add a reaction">${bdI('smile')}</button>` : ''}
      ${mine && canC && !ed ? `<button class="bd-lnk" data-act="bdCmEdit" data-id="${esc(cm.id)}">Edit</button>` : ''}
      ${(mine || bdAdmin()) && canC ? `<button class="bd-lnk" data-act="bdCmDel" data-id="${esc(cm.id)}" data-popkey="cmdel-${esc(cm.id)}">Delete</button>` : ''}</div>
  </div></li>`;
}

/* A card came back from the server: put it in place and repaint. */
function bdGot(c) {
  if (!c || !BD.v) return;
  const i = BD.v.cards.findIndex(x => x.id === c.id);
  if (c.board !== UI.bdBoard || c.archived || c.deleted) { if (i >= 0) BD.v.cards.splice(i, 1); }
  else if (i >= 0) BD.v.cards[i] = c; else BD.v.cards.push(c);
  if (BD.cd && BD.cd.id === c.id) BD.cd = c;
  if ($('bd-root')) bdPaint(); else bdCwPaint();
  if (BD.pop?.live) bdPopRefresh();
  bdSoon();
}
// The card being changed: the open card, or (quick edit) BD.cd without a window.
const bdCid = () => BD.card || BD.cd?.id || '';
async function bdPatch(p, id) { try { bdGot(await bdApi('PATCH', bdCURL(id || bdCid()), p)); return true; } catch (e) { toast(e.message, true); bdCwPaint(); return false; } }
async function bdToggle(what, on) { try { bdGot(await bdPost(bdCURL(bdCid(), '/toggle'), { what, on })); } catch (e) { toast(e.message, true); bdCwPaint(); } }
async function bdCl(op) { try { bdGot(await bdPost(bdCURL(BD.card, '/checklists'), op)); return true; } catch (e) { toast(e.message, true); bdCwPaint(); return false; } }
async function bdUpload(files, cid) {
  const id = cid || BD.card; if (!id) return;
  for (const f of files) {
    if (f.size > 15 << 20) { toast(`${f.name} is over 15 MB`, true); continue; }
    const name = f.name && f.name !== 'image.png' ? f.name : `pasted-${new Date().toISOString().slice(0, 19).replace(/[-:T]/g, '')}.${(f.type.split('/')[1] || 'png').replace('jpeg', 'jpg')}`;
    try { toast(`Uploading ${name}…`); bdGot(await bdApi('POST', bdCURL(id, `/files?name=${bdE(name)}`), f, true)); toast(`Attached ${name}`); }
    catch (e) { toast(e.message, true); }
  }
}

/* ---------- pickers (popovers) ---------- */
function bdPickFilter(id, ph) { return `<label class="bd-sr" for="${id}">${ph}</label><input class="in bd-pop-q" id="${id}" data-bdd="${id}" data-popq="1" placeholder="${ph}" autocomplete="off" value="${esc(bdDraft(id, ''))}">`; }
function bdLabelsPop() {
  const c = BD.cd, q = bdDraft('lp-q', '').toLowerCase().trim(), can = bdCan(), adm = bdAdmin() && !BD.v.board.closed;
  if (BD.lbEdit) return bdLabelEditHTML();
  const ls = BD.v.board.labels.filter(l => !q || (l.name + ' ' + (BD_CN[l.color] || '')).toLowerCase().includes(q));
  return bdPickFilter('lp-q', 'Search labels') + `<div class="bd-plist" role="group" aria-label="Labels">${ls.map(l => `<div class="bd-prow bd-lrow"><input type="checkbox" class="bd-chk" id="lp-${esc(l.id)}" data-act="bdCardLabel" data-id="${esc(l.id)}" ${(c.labels || []).includes(l.id) ? 'checked' : ''} ${can ? '' : 'disabled'}><label class="bd-pill wide bdc-${esc(l.color)}" for="lp-${esc(l.id)}">${esc(l.name || BD_CN[l.color] || '')}</label>${adm ? `<button class="bd-ib" data-act="bdLabEdit" data-id="${esc(l.id)}" aria-label="Edit the label ${esc(l.name || BD_CN[l.color] || '')}" title="Edit">${bdI('pencil')}</button>` : ''}</div>`).join('') || '<p class="bd-mute">No labels match.</p>'}</div>` +
    (adm ? `<div class="bd-pop-tools"><button class="btn sm" data-act="bdLabEdit" data-id="">${bdI('plus')}Create a new label</button><button class="bd-lnk" data-act="bdLabelsModal">All the board’s labels</button></div>` : '');
}
/* The 30 label colours as a grid: a row of subtle, normal and bold shades
   for each five hues, like Trello's picker. */
function bdLabGrid(sel, act, label) {
  return `<div class="bd-lgrid" role="group" aria-label="${esc(label || 'Colour')}">${BD_LCOLORS.map(x => `<button type="button" class="bd-lsw bdc-${x}${x === sel ? ' on' : ''}" aria-pressed="${x === sel}" data-act="${act}" data-id="${x}" aria-label="${BD_CN[x]}" title="${BD_CN[x]}">${x === sel ? bdI('tick') : ''}</button>`).join('')}</div>`;
}
function bdLabelEditHTML() {
  const e = BD.lbEdit, name = bdDraft('le-name', e.name);
  if (e.del) return bdConfirmPop('Delete this label? It comes off every card that has it. This cannot be undone.', 'bdLabDelGo', e.id, 'Delete label');
  return `<div class="bd-form1"><div class="bd-lprev"><span class="bd-pill wide bdc-${esc(e.color)}" id="le-pv">${esc(name.trim() || BD_CN[e.color])}</span></div>
    <div class="fld"><label for="le-name">Title</label><input class="in" id="le-name" data-bdd="le-name" maxlength="60" value="${esc(name)}" placeholder="Name (optional)" data-enter="bdLabSaveOne"></div>
    <span class="bd-flab">Colour · ${esc(BD_CN[e.color])}</span>${bdLabGrid(e.color, 'bdLabColorPick', 'Label colour')}
    <div class="bd-row"><button class="btn pri sm" data-act="bdLabSaveOne">${e.id ? 'Save' : 'Create'}</button>${e.id ? '<span class="bd-sp"></span><button class="btn sm bad" data-act="bdLabDelAsk">Delete</button>' : ''}</div></div>`;
}
function bdMembersPop() {
  const c = BD.cd, q = bdDraft('mp-q', '').toLowerCase().trim();
  const us = bdUsers().filter(u => !q || (u.name + ' ' + u.id).toLowerCase().includes(q));
  return bdPickFilter('mp-q', 'Search people') + `<div class="bd-plist" role="group" aria-label="People on this board">${us.map(u => `<label class="bd-prow"><input type="checkbox" class="bd-chk" id="mp-${esc(u.id)}" data-act="bdCardMember" data-id="${esc(u.id)}" ${(c.members || []).includes(u.id) ? 'checked' : ''}>${bdAv(u.id)}<span class="bd-prow-t">${esc(u.name)}<small>${esc(u.role)}</small></span></label>`).join('') || '<p class="bd-mute">No one matches.</p>'}</div>`;
}
function bdDatesPop() {
  const c = BD.cd, [dd, dt] = (c.due || '').split('T');
  const rem = [[0, 'None'], [5, '5 minutes before'], [15, '15 minutes before'], [60, '1 hour before'], [120, '2 hours before'], [1440, '1 day before'], [2880, '2 days before']];
  return `<div class="bd-form1">
    <div class="fld"><label for="dp-start">Start date</label><input class="in" type="date" id="dp-start" value="${esc(c.start ? c.start.slice(0, 10) : '')}"></div>
    <div class="bd-row2"><div class="fld"><label for="dp-due">Due date</label><input class="in" type="date" id="dp-due" value="${esc(dd || '')}"></div><div class="fld"><label for="dp-time">Time <span class="dim">(IST)</span></label><input class="in" type="time" id="dp-time" value="${esc(dt || '')}"></div></div>
    <div class="fld"><label for="dp-rem">Reminder</label><select class="in" id="dp-rem">${rem.map(([v, l]) => `<option value="${v}" ${+(c.reminder || 0) === v ? 'selected' : ''}>${l}</option>`).join('')}</select><span class="bd-hint">Members and watchers are told when the reminder time comes.</span></div>
    <div class="bd-row"><button class="btn pri sm" data-act="bdDatesSave">Save</button>${c.due || c.start ? '<button class="btn sm" data-act="bdDatesRemove">Remove dates</button>' : ''}</div></div>`;
}
function bdChecklistPop() {
  // Copy items from any checklist on any card of this board, this card first.
  const here = BD.cd, cards = [here, ...bdOpenCards().filter(c => c.id !== here.id).sort((a, b) => a.no - b.no)].filter(c => (c.checklists || []).some(cl => (cl.items || []).length));
  const opt = (c, cl) => `<option value="${esc(c.id + '|' + cl.id)}">${esc(cl.name)} (${bdPl((cl.items || []).length, 'item')})</option>`;
  return `<div class="bd-form1"><div class="fld"><label for="ck-name">Title</label><input class="in" id="ck-name" value="Checklist" maxlength="200" data-enter="bdChecklistAdd"></div>
    ${cards.length ? `<div class="fld"><label for="ck-copy">Copy items from…</label><select class="in" id="ck-copy"><option value="">(none)</option>${cards.map(c => `<optgroup label="${esc(c.id === here.id ? 'This card' : `#${c.no} ${bdClip(c.title, 48)}`)}">${(c.checklists || []).filter(cl => (cl.items || []).length).map(cl => opt(c, cl)).join('')}</optgroup>`).join('')}</select><span class="bd-hint">Any checklist on a card of this board. The items come unticked.</span></div>` : ''}
    <div class="bd-row"><button class="btn pri sm" data-act="bdChecklistAdd">Add checklist</button></div></div>`;
}
const bdClip = (s, n) => { s = String(s || ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; };
function bdAttachPop() {
  return `<div class="bd-form1"><div class="fld"><span class="bd-flab">From your computer</span><label class="btn sm bd-filebtn" for="bd-attfile">${bdI('clip')}Choose files</label><input type="file" id="bd-attfile" multiple class="bd-sr"><span class="bd-hint">Up to 15 MB each. You can also drop files on the card or paste an image.</span></div>
    <hr class="bd-sep">
    <div class="fld"><label for="at-url">Attach a link</label><input class="in" id="at-url" type="url" placeholder="https://" data-enter="bdLinkAdd"></div>
    <div class="fld"><label for="at-name">Link name <span class="dim">(optional)</span></label><input class="in" id="at-name" maxlength="200" data-enter="bdLinkAdd"></div>
    <div class="bd-row"><button class="btn pri sm" data-act="bdLinkAdd">Attach link</button></div></div>`;
}
function bdCoverPop() {
  const c = BD.cd, cv = c.cover || {}, imgs = (c.attachments || []).filter(a => a.image);
  return `<div class="bd-form1"><span class="bd-flab">Size</span><div class="bd-row"><button class="btn sm${!cv.full ? ' on' : ''}" data-act="bdCoverFull" data-id="0" aria-pressed="${!cv.full}" ${cv.color || cv.file ? '' : 'disabled'}>Above the title</button><button class="btn sm${cv.full ? ' on' : ''}" data-act="bdCoverFull" data-id="1" aria-pressed="${!!cv.full}" ${cv.color || cv.file ? '' : 'disabled'}>Full card</button></div>
    <span class="bd-flab">Colours</span><div class="bd-sws">${BD_COLORS.map(x => `<button class="bd-swb bdc-${x}${cv.color === x ? ' on' : ''}" data-act="bdCoverColor" data-id="${x}" aria-pressed="${cv.color === x}" aria-label="${BD_CN[x]}" title="${BD_CN[x]}"></button>`).join('')}</div>
    ${imgs.length ? `<span class="bd-flab">Images on this card</span><div class="bd-covimgs">${imgs.map(a => `<button class="bd-covimg${cv.file === a.file ? ' on' : ''}" data-act="bdCoverImg" data-id="${esc(a.file)}" aria-pressed="${cv.file === a.file}" aria-label="Use ${esc(a.name)}"><img src="/v1/board-files/${bdE(a.file)}" alt=""></button>`).join('')}</div>` : '<p class="bd-hint">Attach an image to use it as the cover.</p>'}
    ${!BD.v.board.covers_on ? '<p class="bd-hint">Covers are switched off for this board, so they are not shown in the lists.</p>' : ''}
    ${cv.color || cv.file ? '<div class="bd-row"><button class="btn sm" data-act="bdCoverOff">Remove cover</button></div>' : ''}</div>`;
}
function bdConfirmPop(text, act, id, label) { return `<p class="bd-pop-p">${text}</p><div class="bd-row"><button class="btn bad sm" data-act="${act}" data-id="${esc(id || '')}" data-autofocus>${label}</button><button class="btn sm" data-act="bdPopClose">Cancel</button></div>`; }
function bdUserPop(sel, act, a) {
  const q = bdDraft('up-q', '').toLowerCase().trim();
  const us = bdUsers().filter(u => !q || (u.name + ' ' + u.id).toLowerCase().includes(q));
  return bdPickFilter('up-q', 'Search people') + `<div class="bd-plist">${us.map(u => `<button class="bd-prow${u.id === sel ? ' on' : ''}" data-act="${act}" data-id="${esc(a)}" data-u="${esc(u.id)}" aria-pressed="${u.id === sel}">${bdAv(u.id)}<span class="bd-prow-t">${esc(u.name)}<small>${esc(u.role)}</small></span></button>`).join('')}</div>${sel ? `<div class="bd-pop-tools"><button class="btn sm" data-act="${act}" data-id="${esc(a)}" data-u="">Remove</button></div>` : ''}`;
}
function bdFilterPop() {
  const f = UI.bdF, me = bdMe(), labs = BD.v.board.labels;
  const chk = (grp, v, label, on) => `<label class="bd-prow"><input type="checkbox" class="bd-chk" id="bf-${grp}-${esc(v)}" data-act="bdFTog" data-id="${esc(v)}" data-g="${grp}" ${on ? 'checked' : ''}>${label}</label>`;
  return `<div class="fld"><label for="bd-fq">Keyword</label><input class="in" id="bd-fq" value="${esc(f.q)}" placeholder="Words on the card, #number, a label or a person" autocomplete="off"></div>
    <fieldset class="bd-fs"><legend>Members</legend>
      ${chk('members', '_none', `<span class="bd-av none" aria-hidden="true">${bdI('user')}</span><span class="bd-prow-t">No members</span>`, f.members.includes('_none'))}
      ${chk('members', me, `${bdAv(me)}<span class="bd-prow-t">Cards assigned to me <small>q</small></span>`, f.members.includes(me))}
      ${bdUsers().filter(u => u.id !== me).map(u => chk('members', u.id, `${bdAv(u.id)}<span class="bd-prow-t">${esc(u.name)}</span>`, f.members.includes(u.id))).join('')}</fieldset>
    <fieldset class="bd-fs"><legend>Due date</legend>${BD_DUEF.filter(([v]) => !['soon', 'later'].includes(v) || f.due.includes(v)).map(([v, l]) => chk('due', v, `<span class="bd-prow-t">${l}</span>`, f.due.includes(v))).join('')}</fieldset>
    <fieldset class="bd-fs"><legend>Labels</legend>
      <div class="bd-seg" role="group" aria-label="Match"><button class="${f.lmode === 'any' ? 'on' : ''}" data-act="bdFMode" data-id="any" aria-pressed="${f.lmode === 'any'}">Any label</button><button class="${f.lmode === 'all' ? 'on' : ''}" data-act="bdFMode" data-id="all" aria-pressed="${f.lmode === 'all'}">All labels</button></div>
      ${chk('labels', '_none', '<span class="bd-pill wide none">No labels</span>', f.labels.includes('_none'))}
      ${labs.map(l => chk('labels', l.id, `<span class="bd-pill wide bdc-${esc(l.color)}">${esc(l.name || BD_CN[l.color])}</span>`, f.labels.includes(l.id))).join('')}</fieldset>
    <fieldset class="bd-fs"><legend>Checklists</legend>${BD_CHECKF.map(([v, l]) => chk('checks', v, `<span class="bd-prow-t">${l}</span>`, f.checks.includes(v))).join('')}</fieldset>
    <fieldset class="bd-fs"><legend>Lists</legend>${bdLists().map(l => chk('lists', l.id, `<span class="bd-prow-t">${esc(l.name)}</span>`, f.lists.includes(l.id))).join('')}</fieldset>
    <div class="bd-pop-tools"><button class="btn sm" data-act="bdFClear" ${bdFCount() ? '' : 'disabled'}>Clear filters</button><span class="bd-hint">f opens, x clears</span></div>`;
}
function bdMoveHTML() {
  const m = BD.mv; if (!m) return '';
  const src = BD.v.board, copy = m.mode === 'copy';
  const mine = (BD.boards || []).filter(b => !b.closed && ['admin', 'member'].includes(b.role));
  let boards = mine.filter(b => b.id === UI.bdBoard ? !copy || src.kind !== 'ledgerline' : b.kind !== 'ledgerline' && (copy || src.kind !== 'ledgerline'));
  if (!boards.some(b => b.id === UI.bdBoard) && !(copy && src.kind === 'ledgerline')) boards.unshift({ id: UI.bdBoard, name: src.name });
  const bv = m.bv, lists = bv ? bdLists(bv.board) : [];
  const cards = bv ? bv.cards.filter(x => x.list === m.list && !x.archived && x.id !== m.card).sort((a, b) => a.pos - b.pos) : [];
  const card = BD.v.cards.find(x => x.id === m.card) || BD.cd;
  const here = !copy && m.board === UI.bdBoard && card && m.list === card.list;
  const cur = here ? bdIn(card.list, bdOpenCards()).findIndex(x => x.id === card.id) : -1;
  const pos = m.pos >= 0 ? Math.min(m.pos, cards.length) : here ? cur : cards.length;
  return `<div class="bd-form1">
    ${copy ? `<div class="fld"><label for="mv-title">Title</label><textarea id="mv-title" class="in bd-auto" rows="2" data-bdd="mv-title" maxlength="500">${esc(bdDraft('mv-title', m.title))}</textarea></div>
      <fieldset class="bd-fs"><legend>Keep</legend>${[['checklists', 'Checklists'], ['labels', 'Labels'], ['members', 'Members']].map(([k, l]) => `<label class="check"><input type="checkbox" class="bd-chk" id="mv-k-${k}" ${m.keep[k] ? 'checked' : ''}> ${l}${k === 'labels' ? ' <span class="dim small">(this board only)</span>' : ''}</label>`).join('')}</fieldset>` : ''}
    <div class="fld"><label for="mv-board">Board</label><select id="mv-board" class="in">${boards.map(b => `<option value="${esc(b.id)}" ${b.id === m.board ? 'selected' : ''}>${esc(b.name)}${b.id === UI.bdBoard ? ' (this board)' : ''}</option>`).join('')}</select></div>
    <div class="bd-row2"><div class="fld"><label for="mv-list">List</label><select id="mv-list" class="in" ${bv ? '' : 'disabled'}>${lists.map(l => `<option value="${esc(l.id)}" ${l.id === m.list ? 'selected' : ''}>${esc(l.name)}</option>`).join('')}</select></div>
      <div class="fld"><label for="mv-pos">Position</label><select id="mv-pos" class="in" ${bv ? '' : 'disabled'}>${Array.from({ length: cards.length + 1 }, (_, i) => `<option value="${i}" ${i === pos ? 'selected' : ''}>${i + 1}${i === cur ? ' (current)' : ''}</option>`).join('')}</select></div></div>
    ${!bv ? '<p class="bd-mute">Loading the board…</p>' : ''}
    <div class="bd-row"><button class="btn pri sm" data-act="bdMoveGo" ${bv && lists.length ? '' : 'disabled'}>${copy ? 'Create card' : 'Move'}</button></div></div>`;
}
async function bdMoveOpen(id, mode, anchor) {
  const c = BD.v.cards.find(x => x.id === id) || (BD.cd?.id === id ? BD.cd : null); if (!c) return;
  if (BD.pop?.key === mode + '-' + id) { bdPopClose(); return; }
  delete BD.drafts['mv-title'];
  bdPop(mode + '-' + id, anchor, mode === 'copy' ? 'Copy card' : 'Move card', bdMoveHTML);
  BD.mv = { card: id, mode, board: UI.bdBoard, list: c.list, pos: -1, title: c.title, keep: { checklists: true, labels: true, members: true }, bv: BD.v };
  bdPopRefresh();
  try { BD.boards = await get('/v1/boards'); if (BD.mv?.card === id) bdPopRefresh(); } catch (e) {}
}

/* ---------- board-level modals ---------- */
function bdNewModal() {
  const scoped = scopedUser();
  modal(mhd('Create board', 'A board holds lists of cards. Make one for any piece of work.') + `<div class="bd"><div class="form">
    <div class="fld full"><label for="nb-name">Board name</label><input class="in" id="nb-name" maxlength="120" placeholder="For example, Month-end close" data-enter="bdNewGo"></div>
    <fieldset class="full bd-fs"><legend>Who can see it</legend>
      <label class="check"><input type="radio" name="nb-vis" value="firm" ${scoped ? 'disabled' : 'checked'}><span><b>Firm-wide</b><br><span class="dim small">Everyone at head office can see it. People you add can change it.</span></span></label>
      <label class="check"><input type="radio" name="nb-vis" value="private" ${scoped ? 'checked' : ''}><span><b>Private</b><br><span class="dim small">Only the people you add can see it.</span></span></label>
      ${scoped ? '<p class="dim small">Branch and partner staff keep their boards private.</p>' : ''}</fieldset>
    <fieldset class="full bd-fs"><legend>Background</legend><div class="bd-sws">${BD_COLORS.map(x => `<label class="bd-sw"><input type="radio" name="nb-bg" value="${x}" ${x === 'blue' ? 'checked' : ''}><span class="bd-swc bdc-${x}" aria-hidden="true"></span><span class="bd-sr">${BD_CN[x]}</span></label>`).join('')}</div></fieldset>
    <div class="fld full"><label for="nb-lists">Starting lists <span class="dim">(one a line)</span></label><textarea class="in" id="nb-lists" rows="3">To do\nDoing\nDone</textarea></div>
  </div><div id="nb-err"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="bdNewGo">Create board</button></div>`);
}
function bdShareModal() {
  const b = BD.v.board, can = bdCanInvite();
  BD.share = (b.members || []).map(m => ({ ...m, was: true }));
  modal(mhd('Share board', esc(b.name)) + `<div class="bd" id="bd-share"></div><div class="ft"><button class="btn" data-act="close">${can ? 'Cancel' : 'Close'}</button>${can ? '<button class="btn pri" data-act="bdShareSave">Save</button>' : ''}</div>`);
  bdSharePaint();
}
/* Admins change anyone's role. When the board lets all members add and
   remove members, a member adds people as members or observers and removes
   anyone but an admin; the server holds to the same. */
function bdSharePaint() {
  const el = $('bd-share'); if (!el) return;
  const b = BD.v.board, adm = bdAdmin() && !b.closed, can = bdCanInvite(), ms = BD.share;
  const cands = (S.me?.users || []).filter(u => u.active !== false && u.role !== 'Client' && !ms.some(m => m.user === u.id));
  const roles = adm ? [['admin', 'Admin'], ['member', 'Member'], ['observer', 'Observer']] : [['member', 'Member'], ['observer', 'Observer']];
  const roleSel = (id, v, i) => `<select class="in sm" id="${id}" data-share="${i}" aria-label="Role">${roles.map(([k, l]) => `<option value="${k}" ${v === k ? 'selected' : ''}>${l}</option>`).join('')}</select>`;
  const row = (m, i) => {
    const tag = `<span class="bd-tag">${esc(m.role)}</span>`;
    if (!can) return tag;
    const del = `<button class="bd-ib" data-act="bdShareDel" data-id="${i}" aria-label="Remove ${esc(bdName(m.user))}">${bdI('x')}</button>`;
    if (adm || !m.was) return roleSel('sh-r-' + i, m.role, i) + del;
    return tag + (m.role === 'admin' ? '<span class="bd-ib-sp"></span>' : del);
  };
  const cm = bdPerms().comments === 'observers' ? ' and comment' : '';
  const how = bdTeam() ? (bdPerms().invite === 'members' ? ' Any member can add and remove members; only admins change roles.' : ' Only admins add and remove members.') : '';
  bdKeep(() => {
    el.innerHTML = `<p class="dim small" style="margin:0">${b.visibility === 'firm' ? `This board is firm-wide: everyone at head office can see it${cm}. Add people as members so they can change it.` : 'This board is private: only the people below can see it.'} Admins change the board’s settings and members; members change lists and cards; observers read${cm}.${how}</p>
      <ul class="bd-share">${ms.map((m, i) => `<li>${bdAv(m.user)}<span class="bd-prow-t">${esc(bdName(m.user))}<small>${esc(m.user)}${m.user === bdMe() ? ' · you' : ''}</small></span>${row(m, i)}</li>`).join('') || '<li class="bd-mute">No members yet.</li>'}</ul>
      ${can && cands.length ? `<div class="bd-row bd-share-add"><label class="bd-sr" for="sh-u">Person to add</label><select class="in" id="sh-u">${cands.map(u => `<option value="${esc(u.id)}">${esc(u.name)} · ${esc(u.role)}</option>`).join('')}</select><label class="bd-sr" for="sh-role">Role</label><select class="in" id="sh-role">${[...roles.filter(r => r[0] !== 'admin'), ...roles.filter(r => r[0] === 'admin')].map(([k, l]) => `<option value="${k}">${l}</option>`).join('')}</select><button class="btn" data-act="bdShareAdd">Add</button></div>` : ''}
      <div id="sh-err"></div>`;
  });
}
function bdAboutModal() {
  const b = BD.v.board, adm = bdAdmin() && !b.closed;
  modal(mhd('About this board', esc(b.name)) + `<div class="bd">
    <div class="kv"><div><small>Made by</small><b>${esc(bdName(b.created_by))}</b></div><div><small>Made</small><b>${esc(fmtDate(b.created_at))}</b></div><div><small>Kind</small><b>${b.kind === 'ledgerline' ? 'Ledgerline board' : 'Team board'}</b></div><div><small>Cards</small><b>${bdOpenCards().length} open</b></div></div>
    ${adm ? `<div class="fld"><label for="ab-desc">Description</label><textarea class="in" id="ab-desc" rows="6" maxlength="4000" placeholder="What this board is for, and how the team uses it">${esc(b.description || '')}</textarea><span class="bd-hint">Formatting: **bold**, _italic_, headings, lists, quotes, code, tables and links</span></div>` : b.description ? `<div class="bd-md">${bdMd(b.description)}</div>` : '<p class="bd-mute">No description.</p>'}
    <div id="ab-err"></div></div>${adm ? '<div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="bdAboutSave">Save</button></div>' : ''}`);
}
async function bdActivityModal() {
  BD.actList = [];
  modal(mhd('Activity', esc(BD.v.board.name)) + '<div class="bd"><ul class="bd-feed" id="bd-actl"><li class="bd-mute">Loading…</li></ul><div id="bd-actm"></div></div>');
  await A.bdActMore();
}
async function bdArchivedModal() {
  BD.arch = { tab: 'cards', cards: null, del: '' };
  delete BD.drafts['ar-q'];
  modal(mhd('Archived items', esc(BD.v.board.name)) + '<div class="bd" id="bd-arch"><p class="bd-mute">Loading…</p></div>');
  try { BD.arch.cards = (await get(bdURL('?archived=1'))).cards || []; } catch (e) { BD.arch.cards = []; toast(e.message, true); }
  bdArchPaint();
  // Lists that moved to another board say where they went.
  if (BD.v.board.lists.some(l => l.moved_to)) get('/v1/boards').then(bs => { BD.boards = bs; bdArchPaint(); }).catch(() => {});
}
function bdArchPaint() {
  const el = $('bd-arch'), a = BD.arch; if (!el || !a) return;
  const can = bdCan(), adm = bdAdmin() && !BD.v.board.closed, q = bdDraft('ar-q', '').toLowerCase().trim();
  const all = BD.v.board.lists.filter(l => l.archived), lname = id => BD.v.board.lists.find(l => l.id === id)?.name || '';
  const lists = all.filter(l => !q || l.name.toLowerCase().includes(q));
  const cards = (a.cards || []).filter(c => !q || (c.title + ' #' + c.no + ' ' + lname(c.list) + ' ' + (c.description || '')).toLowerCase().includes(q));
  const bname = id => (BD.boards || BD.home || []).find(b => b.id === id)?.name || '';
  const what = a.tab === 'cards' ? 'archived cards' : 'archived lists';
  bdKeep(() => {
    el.innerHTML = `<div class="bd-row bd-arch-hd"><div class="bd-seg" role="group" aria-label="Show"><button class="${a.tab === 'cards' ? 'on' : ''}" data-act="bdArchTab" data-id="cards" aria-pressed="${a.tab === 'cards'}">Cards (${(a.cards || []).length})</button><button class="${a.tab === 'lists' ? 'on' : ''}" data-act="bdArchTab" data-id="lists" aria-pressed="${a.tab === 'lists'}">Lists (${all.length})</button></div>
      <span class="bd-sp"></span><div class="bd-arch-q">${bdI('search')}<label class="bd-sr" for="ar-q">Search ${what}</label><input class="in" id="ar-q" type="search" data-bdd="ar-q" data-arq="1" placeholder="Search ${what}" autocomplete="off" value="${esc(bdDraft('ar-q', ''))}"></div></div>
      ${q ? `<p class="bd-hint" role="status">${a.tab === 'cards' ? bdPl(cards.length, 'card') : bdPl(lists.length, 'list')} ${(a.tab === 'cards' ? cards : lists).length === 1 ? 'matches' : 'match'} “${esc(q)}”.</p>` : ''}
      ${a.tab === 'cards' ? `<ul class="bd-arch">${cards.map(c => `<li><button class="bd-lnk bd-arch-t" data-act="bdArchOpen" data-id="${esc(c.id)}"><span class="bd-no">#${c.no}</span> ${esc(c.title)}</button><span class="bd-ft">in ${esc(lname(c.list))}</span><span class="bd-sp"></span>
          ${a.del === c.id ? `<span class="bd-ft">Delete for good?</span><button class="btn sm bad" data-act="bdArchDelGo" data-id="${esc(c.id)}">Delete</button><button class="btn sm" data-act="bdArchDel" data-id="">Cancel</button>`
          : `${can ? `<button class="btn sm" data-act="bdArchRestore" data-id="${esc(c.id)}">Restore</button>` : ''}${adm ? `<button class="btn sm bad" data-act="bdArchDel" data-id="${esc(c.id)}">Delete</button>` : ''}`}</li>`).join('') || `<li class="bd-mute">${q ? 'No archived cards match.' : 'No archived cards.'}</li>`}</ul>`
        : `<ul class="bd-arch">${lists.map(l => `<li><b>${esc(l.name)}</b>${l.moved_to ? `<span class="bd-ft">Moved to ${bname(l.moved_to) ? `<button class="bd-lnk" data-act="bdOpen" data-id="${esc(l.moved_to)}">${esc(bname(l.moved_to))}</button>` : 'another board'}${(a.cards || []).some(c => c.list === l.id) ? '; its archived cards stayed here' : ''}</span>` : ''}<span class="bd-sp"></span>${can && !l.moved_to ? `<button class="btn sm" data-act="bdListRestore" data-id="${esc(l.id)}">Restore</button>` : ''}</li>`).join('') || `<li class="bd-mute">${q ? 'No archived lists match.' : 'No archived lists.'}</li>`}</ul>`}`;
  });
}
function bdLabelsModal() {
  bdPopClose(true);
  const adm = bdAdmin() && !BD.v.board.closed;
  BD.lab = BD.v.board.labels.map(l => ({ ...l }));
  const keepCard = BD.card;
  modal(mhd('Labels', adm ? 'Name the board’s labels, change their colours, add or remove them.' : 'The board’s labels. A board admin can change them.') + `<div class="bd" id="bd-lab"></div>${adm ? '<div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="bdLabSave">Save</button></div>' : ''}`);
  BD.labCard = keepCard;
  bdLabPaint();
}
function bdLabPaint() {
  const el = $('bd-lab'); if (!el) return;
  const adm = bdAdmin() && !BD.v.board.closed;
  const used = id => bdOpenCards().filter(c => (c.labels || []).includes(id)).length;
  bdKeep(() => {
    el.innerHTML = `<ul class="bd-labs-ed">${BD.lab.map((l, i) => `<li><span class="bd-pill wide bdc-${esc(l.color)}" id="lab-pv-${i}">${esc(l.name || BD_CN[l.color] || '')}</span>
      ${adm ? `<button class="btn sm bd-lcbtn" id="lab-c-${i}" data-act="bdLabColor" data-id="${i}" data-popkey="labc-${i}" aria-label="Colour: ${esc(BD_CN[l.color] || '')}. Change it"><span class="bd-lcdot bdc-${esc(l.color)}" aria-hidden="true"></span><span>${esc(BD_CN[l.color] || '')}</span></button>
        <label class="bd-sr" for="lab-n-${i}">Name</label><input class="in sm" id="lab-n-${i}" data-lab="${i}" data-k="name" value="${esc(l.name)}" placeholder="Name (optional)" maxlength="60">
        <span class="bd-ft">${l.id ? bdPl(used(l.id), 'card') : 'new'}</span><button class="bd-ib" data-act="bdLabDel" data-id="${i}" aria-label="Remove this label">${bdI('trash')}</button>` : `<span class="bd-ft">${bdPl(used(l.id), 'card')}</span>`}</li>`).join('') || '<li class="bd-mute">No labels.</li>'}</ul>
      ${adm ? '<button class="btn sm" data-act="bdLabAdd">Add a label</button><p class="bd-hint">30 colours: each in a subtle, normal and bold shade.</p>' : ''}<div id="lab-err"></div>`;
  });
}
function bdSettingsModal() {
  const b = BD.v.board, adm = bdAdmin() && !b.closed, scoped = scopedUser(), ll = b.kind === 'ledgerline', p = bdPerms();
  BD.set = { bg: b.background || 'blue' };
  const radio = (name, v, on, t, d) => `<label class="check"><input type="radio" name="${name}" value="${v}" ${on ? 'checked' : ''} ${adm ? '' : 'disabled'}><span><b>${t}</b><br><span class="dim small">${d}</span></span></label>`;
  modal(mhd('Settings', esc(b.name)) + `<div class="bd"><div class="form">
    <fieldset class="full bd-fs"><legend>Who can see it</legend>
      <label class="check"><input type="radio" name="st-vis" value="firm" ${b.visibility === 'firm' ? 'checked' : ''} ${adm && !scoped ? '' : 'disabled'}><span><b>Firm-wide</b><br><span class="dim small">Everyone at head office can see it.</span></span></label>
      <label class="check"><input type="radio" name="st-vis" value="private" ${b.visibility === 'private' ? 'checked' : ''} ${adm && !ll ? '' : 'disabled'}><span><b>Private</b><br><span class="dim small">Only its members can see it.</span></span></label>
      ${ll ? '<p class="dim small">A Ledgerline board stays firm-wide: its work belongs to the roles that do it.</p>' : scoped ? '<p class="dim small">Branch and partner staff keep their boards private.</p>' : ''}</fieldset>
    <fieldset class="full bd-fs"><legend>Commenting</legend>
      ${radio('st-cm', 'off', p.comments === 'off', 'Disabled', 'No one can comment on the cards.')}
      ${radio('st-cm', 'members', p.comments === 'members', 'Members', 'The board’s admins and members.')}
      ${radio('st-cm', 'observers', p.comments === 'observers', 'Members and observers', `Also its observers${ll || b.visibility === 'firm' ? ', which on a firm-wide board is everyone at head office' : ''}.`)}</fieldset>
    ${ll ? '<p class="dim small full">A Ledgerline board’s members come from their roles, so who can add members or join does not apply.</p>' : `<fieldset class="full bd-fs"><legend>Adding and removing members</legend>
      ${radio('st-inv', 'admins', p.invite === 'admins', 'Admins', 'Only the board’s admins.')}
      ${radio('st-inv', 'members', p.invite === 'members', 'All members', 'Admins and members. Only admins change roles or remove an admin.')}</fieldset>
    <label class="check full"><input type="checkbox" id="st-join" ${p.self_join && b.visibility === 'firm' ? 'checked' : ''} ${adm && b.visibility === 'firm' ? '' : 'disabled'}><span><b>Anyone at head office can join</b><br><span class="dim small" id="st-join-d">${b.visibility === 'firm' ? 'People who are not members see a Join board button that makes them a member.' : 'For firm-wide boards only.'}</span></span></label>`}
    <label class="check full"><input type="checkbox" id="st-covers" ${b.covers_on ? 'checked' : ''} ${adm ? '' : 'disabled'}><span><b>Show card covers</b><br><span class="dim small">Cover colours and images appear on the cards in the lists.</span></span></label>
    <fieldset class="full bd-fs"><legend>Background</legend><div class="bd-sws">${BD_COLORS.map(x => `<label class="bd-sw"><input type="radio" name="st-bg" value="${x}" ${b.background === x ? 'checked' : ''} ${adm ? '' : 'disabled'}><span class="bd-swc bdc-${x}" aria-hidden="true"></span><span class="bd-sr">${BD_CN[x]}</span></label>`).join('')}
      ${b.background?.startsWith('file:') ? `<label class="bd-sw"><input type="radio" name="st-bg" value="${esc(b.background)}" checked ${adm ? '' : 'disabled'}><span class="bd-swc img" style="background-image:url('/v1/board-files/${bdE(b.background.slice(5))}')" aria-hidden="true"></span><span class="bd-sr">Uploaded image</span></label>` : ''}</div>
      ${adm ? '<div class="bd-row"><label class="btn sm bd-filebtn" for="bd-bgfile">' + bdI('image') + 'Upload an image</label><input type="file" id="bd-bgfile" accept="image/png,image/jpeg,image/gif,image/webp" class="bd-sr"><span class="bd-hint">JPEG, PNG, GIF or WebP, up to 15 MB. It is saved straight away.</span></div>' : ''}</fieldset>
  </div><div id="st-err"></div></div>${adm ? '<div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="bdSettingsSave">Save</button></div>' : ''}`);
}
function bdCopyModal() {
  const b = BD.v.board;
  modal(mhd('Copy board', 'Makes a new board with the same lists, labels and settings. You are its admin.') + `<div class="bd"><div class="form">
    <div class="fld full"><label for="cb-name">Name</label><input class="in" id="cb-name" value="${esc(b.name + ' (copy)')}" maxlength="120" data-enter="bdCopyGo"></div>
    <label class="check full"><input type="checkbox" id="cb-cards" checked><span>Keep the cards <span class="dim small">(open cards with their checklists and labels; not comments or attachments)</span></span></label></div><div id="cb-err"></div></div>
    <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="bdCopyGo">Copy board</button></div>`);
}
/* The shortcuts window (bdKeysModal) is in boardux.js, with the keys. */

/* ---------- actions ---------- */
Object.assign(A, {
  bdHome: () => BOARDS.openBoard(''),
  bdOpen: id => BOARDS.openBoard(id),
  bdNew: () => bdNewModal(),
  bdNewGo: async () => {
    const name = $('nb-name').value.trim(), vis = document.querySelector('input[name=nb-vis]:checked')?.value || 'private', bg = document.querySelector('input[name=nb-bg]:checked')?.value || 'blue';
    const lists = $('nb-lists').value.split('\n').map(x => x.trim()).filter(Boolean);
    if (!name) { $('nb-err').innerHTML = '<div class="note bad">Give the board a name.</div>'; $('nb-name').focus(); return; }
    try { const b = await bdPost('/v1/boards', { name, visibility: vis, background: bg, lists }); closeModal(); toast('Board created'); BOARDS.openBoard(b.id); }
    catch (e) { $('nb-err').innerHTML = errHTML(e); }
  },
  bdStar: async (id, el) => {
    const on = el.getAttribute('aria-pressed') !== 'true';
    try {
      await bdPost(`/v1/boards/${bdE(id)}/star`, { on });
      (BD.home || []).forEach(b => { if (b.id === id) b.starred = on; });
      if (BD.v?.board.id === id) { const s = (BD.v.board.starred || []).filter(x => x !== bdMe()); if (on) s.push(bdMe()); BD.v.board.starred = s; }
      bdPaint(); toast(on ? 'Starred' : 'Unstarred');
    } catch (e) { toast(e.message, true); }
  },
  bdReopen: async id => {
    try { await bdPost(`/v1/boards/${bdE(id || UI.bdBoard)}/reopen`); toast('Board reopened'); render({ keep: true }); } catch (e) { toast(e.message, true); }
  },
  bdView: id => { UI.bdView[UI.bdBoard] = id; bdPopClose(true); bdPaint(); window.scrollTo(0, 0); },
  bdHit: (b, el) => { BD.sresOpen = false; bdSresPaint(); BOARDS.openCard(b, el.dataset.card); },
  bdBell: (id, el) => {
    bdPop('bell', el, 'Notifications', bdBellHTML, { wide: true });
    if (!BD.home && !BD.boards) get('/v1/boards').then(bs => { BD.boards = bs; bdPopRefresh(); }).catch(() => {});
    bdNotices();
  },
  bdReadAll: async () => { try { await bdPost('/v1/boards/notices/read', { id: '' }); BD.notices.forEach(n => { n.read = true; }); BD.unread = 0; bdNotices(); bdPopRefresh(); } catch (e) { toast(e.message, true); } },
  bdNote: async id => {
    const n = BD.notices.find(x => x.id === id); if (!n) return;
    bdPopClose(true);
    if (!n.read) { n.read = true; BD.unread = Math.max(0, BD.unread - 1); bdPost('/v1/boards/notices/read', { id }).then(bdNotices).catch(() => {}); }
    if (n.card) BOARDS.openCard(n.board, n.card); else BOARDS.openBoard(n.board);
  },
  bdKeys: () => bdKeysModal(),
  bdPopClose: () => bdPopClose(),
  bdLabNames: () => { UI.bdNames = !UI.bdNames; bdPaint(); },

  /* board header and menu */
  bdVis: (id, el) => bdPop('vis', el, 'Visibility', () => {
    const b = BD.v.board, adm = bdAdmin() && !b.closed, ll = b.kind === 'ledgerline', scoped = scopedUser();
    const opt = (v, icon, t, d, ok) => `<button class="bd-mi${b.visibility === v ? ' on' : ''}" data-act="bdVisSet" data-id="${v}" aria-pressed="${b.visibility === v}" ${adm && ok ? '' : 'disabled'}>${bdI(icon)}<span><b>${t}</b><small>${d}</small></span></button>`;
    return opt('firm', 'users', 'Firm-wide', 'Everyone at head office can see it and comment. Members change it.', !scoped) + opt('private', 'lock', 'Private', 'Only its members can see it.', !ll) +
      (adm ? '' : '<p class="bd-hint">Only a board admin can change this.</p>');
  }),
  bdVisSet: async id => { try { BD.v.board = await bdPutBoard({ visibility: id }); bdPopClose(); bdPaint(); toast(id === 'firm' ? 'The board is firm-wide' : 'The board is private'); bdSoon(); } catch (e) { toast(e.message, true); } },
  bdMenu: (id, el) => bdPop('menu', el, 'Menu', () => {
    const ctx = BOARDS.ctx(), b = BD.v.board, adm = bdAdmin();
    const items = [
      { id: 'about', label: 'About this board', icon: 'info', order: 10 },
      { id: 'watch', label: 'Watch', icon: 'eye', order: 15, checked: bdWatching(), hint: 'Hear about cards added, moved, completed, commented on or given a due date' },
      { id: 'activity', label: 'Activity', icon: 'activity', order: 20 },
      { id: 'archived', label: 'Archived items', icon: 'archive', order: 30 },
      { id: 'labels', label: 'Labels', icon: 'tag', order: 40 },
      { id: 'settings', label: 'Settings', icon: 'gear', order: 50 },
      { id: 'copy', label: 'Copy board', icon: 'copy', order: 60, show: bdTeam() },
      { id: 'close', label: b.closed ? 'Reopen board' : 'Close board', icon: 'archive', order: 80, show: adm && b.kind !== 'inbox' },
      { id: 'delete', label: 'Delete board', icon: 'trash', order: 90, show: !!(adm && b.closed && bdTeam()) },
      ...BOARDS.boardMenu.filter(x => !x.show || bdSafe(() => x.show(ctx) ? 'y' : '', 'menu ' + x.id)).map(x => ({ id: 'x:' + x.id, label: x.label, icon: BD_IC[x.icon] ? x.icon : 'arrow', order: x.order ?? 70 }))
    ].filter(x => x.show !== false);
    return `<div class="bd-menu" role="menu">${bdSorted(items).map(x => `<button class="bd-mi${x.id === 'delete' ? ' danger' : ''}${x.checked ? ' checked' : ''}" ${x.checked != null ? `role="menuitemcheckbox" aria-checked="${!!x.checked}"` : 'role="menuitem"'} data-act="bdMenuRun" data-id="${esc(x.id)}">${bdI(x.icon)}<span>${esc(x.label)}${x.hint ? `<small>${esc(x.hint)}</small>` : ''}</span>${x.checked ? bdI('tick', 'bd-mi-tick') : ''}</button>`).join('')}</div>`;
  }),
  bdMenuRun: id => {
    if (id === 'watch') { A.bdBoardWatch(); return; }
    bdPopClose(true);
    if (id.startsWith('x:')) { const x = BOARDS.boardMenu.find(m => m.id === id.slice(2)); if (x) bdSafe(() => { x.run(BOARDS.ctx()); }, 'menu ' + x.id); return; }
    const b = BD.v.board;
    ({
      about: bdAboutModal, activity: bdActivityModal, archived: bdArchivedModal, labels: bdLabelsModal, settings: bdSettingsModal, copy: bdCopyModal,
      close: () => b.closed ? A.bdReopen() : confirmModal('Close board', esc(b.name), '<p style="margin:0">A closed board keeps its cards and can be reopened. While it is closed nobody can change it, and its reminders stop.</p>', 'Close board', async () => { await bdPost(bdURL('/close')); closeModal(); toast('Board closed'); render({ keep: true }); }, true),
      delete: () => confirmModal('Delete board', esc(b.name), '<p style="margin:0">This deletes the board and its cards for everyone. It cannot be undone here.</p>', 'Delete board', async () => { await bdPost(bdURL('/delete')); closeModal(); toast('Board deleted'); BOARDS.openBoard(''); }, true)
    })[id]?.();
  },
  bdShare: () => bdShareModal(),
  bdShareAdd: () => { const u = $('sh-u')?.value; if (!u) return; BD.share.push({ user: u, role: $('sh-role').value }); bdSharePaint(); $('sh-u')?.focus(); },
  bdShareDel: i => { BD.share.splice(+i, 1); bdSharePaint(); },
  bdShareSave: async () => {
    document.querySelectorAll('[data-share]').forEach(s => { BD.share[+s.dataset.share].role = s.value; });
    try { await bdApi('PUT', bdURL('/members'), { members: BD.share.map(m => ({ user: m.user, role: m.role })) }); closeModal(); toast('Members saved'); bdReload(true); } catch (e) { $('sh-err').innerHTML = errHTML(e); }
  },
  bdBoardWatch: async () => {
    const on = !bdWatching(), me = bdMe(), b = BD.v.board;
    b.watchers = (b.watchers || []).filter(x => x !== me); if (on) b.watchers.push(me);
    bdPaint(); if (BD.pop?.key === 'menu') bdPopRefresh();
    try { await bdPost(bdURL('/watch'), { on }); toast(on ? 'You are watching this board' : 'You stopped watching this board'); }
    catch (e) { b.watchers = (b.watchers || []).filter(x => x !== me); if (!on) b.watchers.push(me); bdPaint(); toast(e.message, true); }
  },
  bdJoinBoard: async () => {
    try { await bdPost(bdURL('/join')); toast('You joined the board'); await bdReload(true); } catch (e) { toast(e.message, true); }
  },
  bdAboutSave: async () => { try { BD.v.board = await bdApi('PUT', bdURL(), { description: $('ab-desc').value }); closeModal(); toast('Saved'); bdPaint(); bdSoon(); } catch (e) { $('ab-err').innerHTML = errHTML(e); } },
  bdActMore: async () => {
    const last = BD.actList[BD.actList.length - 1];
    try {
      const r = await get(bdURL(`/activity?limit=50${last ? '&before=' + last.seq : ''}`));
      BD.actList.push(...r);
      const el = $('bd-actl'); if (!el) return;
      el.innerHTML = BD.actList.map(a => `<li class="bd-fa">${bdAv(a.by)}<div><span><b>${esc(bdName(a.by))}</b> ${esc(a.text)}</span>${a.card ? ` <button class="bd-lnk" data-act="bdActCard" data-id="${esc(a.card)}">Open card</button>` : ''}<div class="bd-ft">${bdWhen(a.at)}</div></div></li>`).join('') || '<li class="bd-mute">Nothing yet.</li>';
      $('bd-actm').innerHTML = r.length === 50 ? '<button class="btn" data-act="bdActMore">Show more</button>' : '';
    } catch (e) { toast(e.message, true); }
  },
  bdActCard: id => { closeModal(); bdOpenCard(id); },
  bdArchTab: id => { BD.arch.tab = id; bdArchPaint(); },
  bdArchOpen: id => { closeModal(); bdOpenCard(id); },
  bdArchRestore: async id => {
    try { const c = await bdApi('PATCH', bdCURL(id), { archived: false }); BD.arch.cards = BD.arch.cards.filter(x => x.id !== id); BD.v.cards.push(c); bdArchPaint(); bdPaint(); toast(`Restored #${c.no}`); bdSoon(); } catch (e) { toast(e.message, true); }
  },
  bdArchDel: id => { BD.arch.del = id || ''; bdArchPaint(); },
  bdArchDelGo: async id => { try { await bdPost(bdCURL(id, '/delete')); BD.arch.cards = BD.arch.cards.filter(x => x.id !== id); BD.arch.del = ''; bdArchPaint(); toast('Card deleted'); bdSoon(); } catch (e) { toast(e.message, true); } },
  bdListRestore: async id => {
    try { await bdApi('PUT', bdURL(`/lists/${bdE(id)}`), { archived: false }); const l = BD.v.board.lists.find(x => x.id === id); if (l) l.archived = false; if ($('bd-arch')) bdArchPaint(); bdPaint(); toast('List restored'); bdSoon(); } catch (e) { toast(e.message, true); }
  },
  bdLabelsModal: () => bdLabelsModal(),
  bdLabAdd: () => { const used = BD.lab.map(l => l.color); BD.lab.push({ id: '', name: '', color: BD_COLORS.find(c => !used.includes(c)) || 'green' }); bdLabPaint(); document.querySelector(`#lab-n-${BD.lab.length - 1}`)?.focus(); },
  bdLabDel: i => { BD.lab.splice(+i, 1); bdLabPaint(); },
  bdLabColor: (i, el) => { BD.labI = +i; bdPop('labc-' + i, el, 'Label colour', () => bdLabGrid(BD.lab[BD.labI]?.color, 'bdLabColorSet', 'Label colour')); },
  bdLabColorSet: c => { const l = BD.lab[BD.labI]; if (!l) return; l.color = c; const i = BD.labI; bdPopClose(true); bdLabPaint(); $('lab-c-' + i)?.focus(); },
  /* the card's label picker: create or edit one label */
  bdLabEdit: id => {
    const l = BD.v.board.labels.find(x => x.id === id), used = BD.v.board.labels.map(x => x.color);
    delete BD.drafts['le-name'];
    BD.lbEdit = l ? { id: l.id, name: l.name || '', color: l.color } : { id: '', name: bdDraft('lp-q', '').trim(), color: BD_LCOLORS.find(c => !used.includes(c)) || 'green' };
    BD.pop.render = bdLabelsPop; bdPopSub(l ? 'Edit label' : 'Create a label', 'bdLabBack');
  },
  bdLabBack: () => { BD.lbEdit = null; delete BD.drafts['le-name']; if (BD.pop) bdPopSub('Labels', ''); },
  bdLabColorPick: c => { if (!BD.lbEdit) return; BD.lbEdit.color = c; bdPopRefresh(); document.querySelector(`#bd-pop .bd-lsw[data-id="${c}"]`)?.focus(); },
  bdLabDelAsk: () => { if (BD.lbEdit) { BD.lbEdit.del = true; bdPopRefresh(); } },
  bdLabDelGo: async id => {
    try { BD.v.board = await bdPutBoard({ labels: BD.v.board.labels.filter(l => l.id !== id) }); A.bdLabBack(); bdPaint(); toast('Label deleted'); bdSoon(); } catch (e) { toast(e.message, true); }
  },
  bdLabSaveOne: async () => {
    const e = BD.lbEdit; if (!e) return;
    const name = ($('le-name')?.value ?? e.name).trim(), old = BD.v.board.labels.map(l => l.id);
    const labels = e.id ? BD.v.board.labels.map(l => l.id === e.id ? { ...l, name, color: e.color } : l) : [...BD.v.board.labels, { id: '', name, color: e.color }];
    try {
      BD.v.board = await bdPutBoard({ labels });
      const made = !e.id && BD.v.board.labels.find(l => !old.includes(l.id));
      A.bdLabBack(); bdPaint(); toast(e.id ? 'Label saved' : 'Label created');
      // A label made from a card's picker goes on the card.
      if (made && BD.cd && bdCan()) await bdPatch({ labels: [...(BD.cd.labels || []).filter(bdLabel), made.id] });
      bdSoon();
    } catch (x) { toast(x.message, true); }
  },
  bdLabSave: async () => {
    try { BD.v.board = await bdPutBoard({ labels: BD.lab.map(l => ({ id: l.id, name: l.name.trim(), color: l.color })) }); const c = BD.labCard; closeModal(); toast('Labels saved'); bdPaint(); if (c) bdOpenCard(c); bdSoon(); }
    catch (e) { $('lab-err').innerHTML = errHTML(e); }
  },
  bdSettingsSave: async () => {
    const vis = document.querySelector('input[name=st-vis]:checked')?.value, bg = document.querySelector('input[name=st-bg]:checked')?.value;
    const p = bdPerms(), perms = { comments: document.querySelector('input[name=st-cm]:checked')?.value || p.comments, invite: document.querySelector('input[name=st-inv]:checked')?.value || p.invite, self_join: !!$('st-join')?.checked && vis === 'firm' };
    try { BD.v.board = await bdPutBoard({ visibility: vis, background: bg, covers_on: $('st-covers').checked, perms }); closeModal(); toast('Settings saved'); await bdReload(true); }
    catch (e) { $('st-err').innerHTML = errHTML(e); }
  },
  bdCopyGo: async () => {
    try { const b = await bdPost(bdURL('/copy'), { name: $('cb-name').value.trim(), cards: $('cb-cards').checked }); closeModal(); toast('Board copied'); BOARDS.openBoard(b.id); }
    catch (e) { $('cb-err').innerHTML = errHTML(e); }
  },

  /* filters */
  bdFilter: (id, el) => bdPop('filter', el || $('bd-fbtn'), 'Filter', bdFilterPop, { wide: true }),
  bdFTog: (v, el) => { const g = el.dataset.g, a = UI.bdF[g]; UI.bdF[g] = el.checked ? [...new Set([...a, v])] : a.filter(x => x !== v); bdPaint(); bdPopRefresh(); },
  bdFMode: id => { UI.bdF.lmode = id; bdPaint(); bdPopRefresh(); },
  bdFClear: () => { UI.bdF = bdF0(); bdPaint(); bdPopRefresh(); toast('Filters cleared'); },

  /* lists */
  bdAddList: () => { BD.addList = true; bdPaint(); const cv = $('bd-board'); if (cv) cv.scrollLeft = cv.scrollWidth; $('bd-al')?.focus(); },
  bdAddListClose: () => { BD.addList = false; delete BD.drafts.al; bdPaint(); },
  bdAddListGo: async () => {
    const i = $('bd-al'), name = (i?.value || '').trim(); if (!name) { i?.focus(); return; }
    delete BD.drafts.al; i.value = '';
    try { const l = await bdPost(bdURL('/lists'), { name }); BD.v.board.lists.push(l); bdPaint(); const cv = $('bd-board'); if (cv) cv.scrollLeft = cv.scrollWidth; $('bd-al')?.focus(); bdSoon(); }
    catch (e) { BD.drafts.al = name; bdPaint(); toast(e.message, true); }
  },
  bdListRename: id => { if (!bdCan() || !bdTeam()) return; BD.rename = id; delete BD.drafts['ln-' + id]; bdPaint(); const i = $('bd-ln-' + id); if (i) { i.focus(); i.select(); } },
  bdListRenameCancel: id => { BD.rename = ''; delete BD.drafts['ln-' + id]; bdPaint(); document.querySelector(`[data-act="bdListRename"][data-id="${CSS.escape(id)}"]`)?.focus(); },
  bdListRenameSave: async id => {
    const l = BD.v.board.lists.find(x => x.id === id), i = $('bd-ln-' + id), v = (i?.value || '').trim();
    BD.rename = ''; delete BD.drafts['ln-' + id];
    if (!l || !v || v === l.name) { bdPaint(); return; }
    const old = l.name; l.name = v; bdPaint();
    try { await bdApi('PUT', bdURL(`/lists/${bdE(id)}`), { name: v }); bdSoon(); } catch (e) { l.name = old; bdPaint(); toast(e.message, true); }
  },
  bdListMenu: (id, el) => { BD.lm = id; bdPop('list-' + id, el, 'List actions', () => bdListMenuHTML('')); },
  bdLm: sub => {
    const l = BD.v.board.lists.find(x => x.id === BD.lm); if (!l || !BD.pop) return;
    BD.pop.render = () => bdListMenuHTML(sub);
    if (sub === 'copy' || sub === 'move') bdLmvOpen(sub, l);
    bdPopSub({ '': 'List actions', moveall: 'Move all cards in this list', sort: 'Sort the list', color: 'List colour', limit: 'Card limit', archiveall: 'Archive all cards in this list', copy: 'Copy list', move: 'Move list to another board' }[sub || ''], sub ? 'bdLmBack' : '');
  },
  bdListWatch: async () => {
    const l = BD.v.board.lists.find(x => x.id === BD.lm); if (!l) return;
    const on = !bdLWatching(l);
    try { const nl = await bdPost(bdURL(`/lists/${bdE(l.id)}/watch`), { on }); l.watchers = nl.watchers || []; bdPaint(); bdPopRefresh(); toast(on ? `You are watching the list ${l.name}` : `You stopped watching the list ${l.name}`); }
    catch (e) { toast(e.message, true); }
  },
  bdListCollapse: async id => {
    id = id || BD.lm; const l = BD.v.board.lists.find(x => x.id === id); if (!l) return;
    const on = !bdCollapsed(id), was = (BD.v.collapsed || []).slice();
    BD.v.collapsed = on ? [...was, id] : was.filter(x => x !== id);
    if (BD.pop?.key === 'list-' + id) bdPopClose(true);
    bdPaint();
    // Keep the keyboard where it was: on the strip, or on the list's collapse button.
    document.querySelector(`#bd-board [data-act="bdListCollapse"][data-id="${CSS.escape(id)}"]`)?.focus({ preventScroll: true });
    try { const r = await bdPost(bdURL(`/lists/${bdE(id)}/collapse`), { on }); BD.v.collapsed = r.collapsed || []; }
    catch (e) { BD.v.collapsed = was; bdPaint(); toast(e.message, true); }
  },
  bdLmvGo: async () => {
    const m = BD.lmv, l = BD.v.board.lists.find(x => x.id === BD.lm); if (!m || !m.bv || !l) return;
    const ls = bdLists(m.bv.board), i = Math.min(+($('lmv-pos')?.value ?? ls.length), ls.length);
    const pos = bdMid(ls[i - 1]?.pos, ls[i]?.pos), other = m.board !== UI.bdBoard, bname = m.bv.board.name;
    try {
      if (m.mode === 'copy') {
        const name = ($('lmv-name')?.value || '').trim();
        if (!name) { $('lmv-name')?.focus(); return; }
        const r = await bdPost(bdURL(`/lists/${bdE(l.id)}/copy`), { board: other ? m.board : '', name, pos });
        bdPopClose(true);
        toast(`${other ? `Copied to ${bname}` : 'List copied'}: ${bdPl(r.cards, 'card')}${r.skipped ? `. ${bdPl(r.skipped, 'linked card')} stayed, as linked cards follow their record` : ''}`);
      } else {
        if (!other) return;
        const r = await bdPost(bdURL(`/lists/${bdE(l.id)}/move`), { board: m.board, pos });
        bdPopClose(true);
        toast(`Moved the list ${l.name} and ${bdPl(r.cards, 'card')} to ${bname}`);
      }
      BD.lmv = null; bdReload(true);
    } catch (e) { toast(e.message, true); }
  },
  bdLmBack: () => A.bdLm(''),
  bdListAddCard: () => { const id = BD.lm; bdPopClose(true); A.bdComp(id); },
  bdListMoveAll: async to => {
    const from = BD.lm; bdPopClose(true);
    try { const r = await bdPost(bdURL(`/lists/${bdE(from)}/move-cards`), { to }); toast(`${bdPl(r.moved, 'card')} moved`); bdReload(); } catch (e) { toast(e.message, true); bdReload(); }
  },
  bdListSort: async by => { const id = BD.lm; bdPopClose(true); try { await bdPost(bdURL(`/lists/${bdE(id)}/sort`), { by }); toast('List sorted'); bdReload(); } catch (e) { toast(e.message, true); } },
  bdListColor: async color => { const l = BD.v.board.lists.find(x => x.id === BD.lm); try { await bdApi('PUT', bdURL(`/lists/${bdE(l.id)}`), { color }); l.color = color; bdPaint(); bdPopRefresh(); bdSoon(); } catch (e) { toast(e.message, true); } },
  bdListLimit: async clear => {
    const l = BD.v.board.lists.find(x => x.id === BD.lm), n = clear === '0' ? 0 : parseInt($('lm-limit')?.value || '0', 10) || 0;
    try { await bdApi('PUT', bdURL(`/lists/${bdE(l.id)}`), { limit: n }); l.limit = n; bdPopClose(true); bdPaint(); toast(n ? `Limit set to ${n} cards` : 'Limit removed'); bdSoon(); } catch (e) { toast(e.message, true); }
  },
  bdListShift: async dir => {
    const ls = bdLists(), i = ls.findIndex(x => x.id === BD.lm), j = i + (+dir); if (i < 0 || j < 0 || j >= ls.length) return;
    const rest = ls.filter((_, k) => k !== i), pos = bdMid(rest[j - 1]?.pos, rest[j]?.pos), l = ls[i], old = l.pos;
    l.pos = pos; bdPaint(); bdPopPlace();
    try { await bdApi('PUT', bdURL(`/lists/${bdE(l.id)}`), { pos }); bdSoon(); } catch (e) { l.pos = old; bdPaint(); toast(e.message, true); }
  },
  bdListArchiveAll: async () => { const id = BD.lm; bdPopClose(true); try { const r = await bdPost(bdURL(`/lists/${bdE(id)}/archive-cards`)); toast(`${bdPl(r.archived, 'card')} archived`); bdReload(); } catch (e) { toast(e.message, true); bdReload(); } },
  bdListArchive: async () => {
    const l = BD.v.board.lists.find(x => x.id === BD.lm); bdPopClose(true);
    try { await bdApi('PUT', bdURL(`/lists/${bdE(l.id)}`), { archived: true }); l.archived = true; bdPaint(); toast(`Archived the list ${l.name}. Restore it from Archived items.`); bdSoon(); } catch (e) { toast(e.message, true); }
  },

  /* composer */
  bdComp: id => { BD.comp = id; bdPaint(); const t = $('bd-comp'); if (t) { t.focus(); t.scrollIntoView({ block: 'nearest' }); } },
  bdCompClose: () => { BD.comp = ''; delete BD.drafts.comp; bdPaint(); },
  bdCompAdd: async () => {
    const t = $('bd-comp'), title = (t?.value || '').trim(), list = BD.comp;
    if (!title || !list) { t?.focus(); return; }
    BD.drafts.comp = ''; t.value = '';
    try {
      const c = await bdPost(bdURL('/cards'), { list, ...(BOARDS.compose ? BOARDS.compose(title, list) : { title }) });
      BD.v.cards.push(c); bdPaint();
      const body = document.querySelector(`.bd-lbody[data-list="${CSS.escape(list)}"]`); if (body) body.scrollTop = body.scrollHeight;
      $('bd-comp')?.focus(); bdSoon();
    } catch (e) { BD.drafts.comp = title; bdPaint(); toast(e.message, true); }
  },
  bdCompExt: (id, el) => { const x = BOARDS.composerButtons.find(b => b.id === id); if (x) bdSafe(() => { x.run(el.dataset.list, BOARDS.ctx()); }, 'composer button ' + id); },

  /* cards in lists */
  bdCard: id => bdOpenCard(id),
  // The card's ⋯ opens quick edit (boardux.js).
  bdCardMenu: (id, el) => { bdPopClose(true); BOARDS.quickEdit?.(id, el); },

  /* card window */
  bdTitleEdit: () => { if (BD.cd?.mirror) return; BD.cw.title = true; delete BD.drafts['cw-title']; bdCwPaint(); const t = $('cw-title'); if (t) { t.focus(); t.select(); } },
  bdTitleCancel: () => { BD.cw.title = false; delete BD.drafts['cw-title']; bdCwPaint(); document.querySelector('.bd-cw-tb')?.focus(); },
  bdTitleSave: async () => {
    const t = ($('cw-title')?.value || '').replace(/\s*\n\s*/g, ' ').trim();
    BD.cw.title = false; delete BD.drafts['cw-title'];
    if (!t || t === BD.cd.title) { bdCwPaint(); return; }
    if (await bdPatch({ title: t })) document.querySelector('.bd-cw-tb')?.focus();
  },
  bdDescEdit: () => { if (BD.cd?.mirror) return; BD.cw.desc = true; bdCwPaint(); const t = $('cw-desc'); if (t) { t.focus(); t.setSelectionRange(t.value.length, t.value.length); } },
  bdDescCancel: () => { BD.cw.desc = false; delete BD.drafts['desc-' + BD.card]; bdCwPaint(); },
  bdDescSave: async () => {
    const v = $('cw-desc')?.value ?? '';
    const ok = await bdPatch({ description: v });
    if (ok) { BD.cw.desc = false; delete BD.drafts['desc-' + BD.card]; bdCwPaint(); }
  },
  bdPickLabels: (id, el) => { delete BD.drafts['lp-q']; BD.lbEdit = null; bdPop('labels', el, 'Labels', bdLabelsPop); },
  bdCardLabel: (id, el) => { const ls = (BD.cd.labels || []).filter(x => x !== id && bdLabel(x)); if (el.checked) ls.push(id); bdPatch({ labels: ls }); },
  bdPickMembers: (id, el) => { delete BD.drafts['mp-q']; bdPop('members', el, 'Members', bdMembersPop); },
  bdCardMember: (id, el) => { const ms = (BD.cd.members || []).filter(x => x !== id); if (el.checked) ms.push(id); bdPatch({ members: ms }); },
  bdJoin: () => bdToggle('member', !(BD.cd.members || []).includes(bdMe())),
  bdWatch: () => bdToggle('watch', !(BD.cd.watchers || []).includes(bdMe())),
  bdVote: () => bdToggle('vote', !(BD.cd.votes || []).includes(bdMe())),
  bdPickDates: (id, el) => { if (!BD.cd?.mirror) bdPop('dates', el, 'Dates', bdDatesPop); },
  bdDatesSave: async () => {
    const s = $('dp-start').value, d = $('dp-due').value, t = $('dp-time').value, rem = +$('dp-rem').value;
    if (t && !d) { toast('Give a due date for the time', true); $('dp-due').focus(); return; }
    if (await bdPatch({ start: s, due: d ? d + (t ? 'T' + t : '') : '', reminder: d ? rem : 0 })) bdPopClose();
  },
  bdDatesRemove: async () => { if (await bdPatch({ start: '', due: '', reminder: 0, due_done: false })) bdPopClose(); },
  bdDueDone: (id, el) => { if (BD.cd?.mirror) { el.checked = !el.checked; return; } bdPatch({ due_done: el.checked }); },
  bdPickChecklist: (id, el) => bdPop('checklist', el, 'Add checklist', bdChecklistPop),
  bdChecklistAdd: async () => {
    const name = ($('ck-name')?.value || '').trim() || 'Checklist', [card, from] = ($('ck-copy')?.value || '').split('|');
    bdPopClose(true);
    if (await bdCl({ op: 'add_list', name, copy_from: from || '', copy_card: from && card !== BD.card ? card : '' })) {
      const cl = BD.cd.checklists[BD.cd.checklists.length - 1];
      if (cl && !(cl.items || []).length) { BD.cw.addItem = cl.id; bdCwPaint(); $('itadd-' + cl.id)?.focus(); }
    }
  },
  bdClRename: id => { BD.cw.clName = id; delete BD.drafts['clnm-' + id]; bdCwPaint(); const i = $('clnm-' + id); if (i) { i.focus(); i.select(); } },
  bdClRenameCancel: () => { BD.cw.clName = ''; bdCwPaint(); },
  bdClRenameSave: async id => {
    const v = ($('clnm-' + id)?.value || '').trim(), cl = (BD.cd.checklists || []).find(x => x.id === id);
    BD.cw.clName = ''; delete BD.drafts['clnm-' + id];
    if (!v || !cl || v === cl.name) { bdCwPaint(); return; }
    bdCl({ op: 'rename_list', checklist: id, name: v });
  },
  bdClHide: id => { UI.bdHideDone[id] = !UI.bdHideDone[id]; bdCwPaint(); },
  bdClDel: (id, el) => { const cl = (BD.cd.checklists || []).find(x => x.id === id); bdPop('cldel-' + id, el, 'Delete checklist', () => bdConfirmPop(`Delete the checklist ${esc(cl?.name || '')} and its ${bdPl((cl?.items || []).length, 'item')}?`, 'bdClDelGo', id, 'Delete checklist')); },
  bdClDelGo: id => { bdPopClose(true); bdCl({ op: 'delete_list', checklist: id }); },
  bdItAddOpen: id => { BD.cw.addItem = id; bdCwPaint(); $('itadd-' + id)?.focus(); },
  bdItAddClose: () => { const id = BD.cw.addItem; BD.cw.addItem = ''; delete BD.drafts['itadd-' + id]; bdCwPaint(); },
  bdItAdd: async id => {
    id = id || BD.cw.addItem; const t = $('itadd-' + id), v = (t?.value || '').trim();
    if (!v) { t?.focus(); return; }
    BD.drafts['itadd-' + id] = ''; t.value = '';
    if (!await bdCl({ op: 'add_item', checklist: id, name: v })) { BD.drafts['itadd-' + id] = v; bdCwPaint(); }
    $('itadd-' + id)?.focus();
  },
  bdItTick: (id, el) => bdCl({ op: 'update_item', checklist: el.dataset.cl, item: id, done: el.checked }),
  bdItEdit: id => { BD.cw.itEdit = id; delete BD.drafts['ited-' + id]; bdCwPaint(); const t = $('ited-' + id); if (t) { t.focus(); t.setSelectionRange(t.value.length, t.value.length); } },
  bdItCancel: () => { const id = BD.cw.itEdit; BD.cw.itEdit = ''; delete BD.drafts['ited-' + id]; bdCwPaint(); },
  bdItSave: async (id, el) => {
    const v = ($('ited-' + id)?.value || '').replace(/\s*\n\s*/g, ' ').trim();
    BD.cw.itEdit = ''; delete BD.drafts['ited-' + id];
    if (!v) { bdCwPaint(); return; }
    bdCl({ op: 'update_item', checklist: el.dataset.cl, item: id, name: v });
  },
  bdItDel: (id, el) => { BD.cw.itEdit = ''; bdCl({ op: 'delete_item', checklist: el.dataset.cl, item: id }); },
  bdItConvert: async (id, el) => {
    try {
      const r = await bdPost(bdCURL(BD.card, '/checklists/convert'), { checklist: el.dataset.cl, item: id });
      bdGot(r.new); bdGot(r.card); toast(`Made the card #${r.new.no} below this one`);
    } catch (e) { toast(e.message, true); bdCwPaint(); }
  },
  /* the round box on a card's front */
  bdCardDone: async (id, el) => {
    const c = BD.v.cards.find(x => x.id === id); if (!c) return;
    const on = el.checked, old = !!c.due_done;
    c.due_done = on; bdPaint();
    try { bdGot(await bdApi('PATCH', bdCURL(id), { due_done: on })); document.querySelector(`.bd-cdone[data-id="${CSS.escape(id)}"]`)?.focus({ preventScroll: true }); toast(on ? `#${c.no} marked complete` : `#${c.no} marked incomplete`); }
    catch (e) { c.due_done = old; bdPaint(); toast(e.message, true); }
  },
  bdItUp: (id, el) => A.bdItMove(id, el, -1),
  bdItDown: (id, el) => A.bdItMove(id, el, 1),
  bdItMove: (id, el, d) => {
    const cl = (BD.cd.checklists || []).find(x => x.id === el.dataset.cl); if (!cl) return;
    const i = cl.items.findIndex(x => x.id === id), j = i + d; if (i < 0 || j < 0 || j >= cl.items.length) return;
    const [it] = cl.items.splice(i, 1); cl.items.splice(j, 0, it); bdCwPaint();
    bdCl({ op: 'move_item', checklist: cl.id, item: id, index: j }).then(() => document.querySelector(`[data-act="${d < 0 ? 'bdItUp' : 'bdItDown'}"][data-id="${CSS.escape(id)}"]:not([disabled])`)?.focus());
  },
  bdItDue: (id, el) => {
    const cl = (BD.cd.checklists || []).find(x => x.id === el.dataset.cl), it = cl?.items.find(x => x.id === id); if (!it) return;
    BD.itCl = cl.id;
    bdPop('itdue-' + id, el, 'Item due date', () => `<div class="bd-form1"><div class="fld"><label for="itd-d">Due date</label><input class="in" type="date" id="itd-d" value="${esc((it.due || '').slice(0, 10))}"></div><div class="bd-row"><button class="btn pri sm" data-act="bdItDueSave" data-id="${esc(id)}">Save</button>${it.due ? `<button class="btn sm" data-act="bdItDueSave" data-id="${esc(id)}" data-clear="1">Remove</button>` : ''}</div></div>`);
  },
  bdItDueSave: (id, el) => { const v = el.dataset.clear ? '' : $('itd-d').value; bdPopClose(); bdCl({ op: 'update_item', checklist: BD.itCl, item: id, due: v }); },
  bdItMember: (id, el) => {
    const cl = (BD.cd.checklists || []).find(x => x.id === el.dataset.cl), it = cl?.items.find(x => x.id === id); if (!it) return;
    BD.itCl = cl.id; delete BD.drafts['up-q'];
    bdPop('itmem-' + id, el, 'Assign item', () => bdUserPop(it.member, 'bdItMemberSet', id));
  },
  bdItMemberSet: (id, el) => { bdPopClose(); bdCl({ op: 'update_item', checklist: BD.itCl, item: id, member: el.dataset.u }); },
  bdPickAttach: (id, el) => bdPop('attach', el, 'Attach', bdAttachPop),
  bdLinkAdd: async () => {
    const url = ($('at-url')?.value || '').trim(), name = ($('at-name')?.value || '').trim();
    if (!/^https?:\/\//i.test(url)) { toast('Give a web address starting https://', true); $('at-url')?.focus(); return; }
    try { bdGot(await bdPost(bdCURL(BD.card, '/links'), { url, name })); bdPopClose(); toast('Link attached'); } catch (e) { toast(e.message, true); }
  },
  bdAttDel: (id, el) => { const a = (BD.cd.attachments || []).find(x => x.id === id); bdPop('attdel-' + id, el, 'Delete attachment', () => bdConfirmPop(`Delete ${esc(a?.name || 'this attachment')} from the card?`, 'bdAttDelGo', id, 'Delete')); },
  bdAttDelGo: async id => { bdPopClose(true); try { bdGot(await bdPost(bdCURL(BD.card, `/attachments/${bdE(id)}/delete`))); toast('Attachment deleted'); } catch (e) { toast(e.message, true); } },
  bdPickCover: (id, el) => bdPop('cover', el, 'Cover', bdCoverPop),
  bdCoverColor: color => bdPatch({ cover: { color, full: !!BD.cd.cover?.full } }),
  bdCoverImg: file => bdPatch({ cover: { file, full: !!BD.cd.cover?.full } }),
  bdCoverFull: v => { const cv = BD.cd.cover; if (cv) bdPatch({ cover: { color: cv.color || '', file: cv.file || '', full: v === '1' } }); },
  bdCoverOff: () => bdPatch({ cover: { color: '', file: '' } }),
  bdMoveCard: (id, el) => bdMoveOpen(id || BD.card, 'move', el),
  bdCopyCard: (id, el) => bdMoveOpen(id || BD.card, 'copy', el),
  bdMoveGo: async () => {
    const m = BD.mv; if (!m || !m.bv) return;
    const cards = m.bv.cards.filter(x => x.list === m.list && !x.archived && x.id !== m.card).sort((a, b) => a.pos - b.pos);
    const sel = $('mv-pos'), i = sel ? +sel.value : cards.length;
    const pos = bdMid(cards[i - 1]?.pos, cards[i]?.pos), other = m.board !== UI.bdBoard;
    const body = { list: m.list, pos, ...(other ? { board: m.board } : {}) };
    const bname = (BD.boards || []).find(b => b.id === m.board)?.name || '';
    try {
      if (m.mode === 'copy') {
        const keep = ['checklists', 'labels', 'members'].filter(k => $('mv-k-' + k)?.checked);
        const c = await bdPost(bdCURL(m.card, '/copy'), { ...body, title: ($('mv-title')?.value || '').trim(), keep });
        bdPopClose(true); toast(other ? `Copied to ${bname}` : `Copied as #${c.no}`);
        if (!other) { BD.v.cards.push(c); bdPaint(); }
      } else {
        const c = BD.v.cards.find(x => x.id === m.card), old = c && { list: c.list, pos: c.pos };
        if (c && !other) { c.list = m.list; c.pos = pos; bdPaint(); }
        bdPopClose(true);
        try {
          const nc = await bdPost(bdCURL(m.card, '/move'), body);
          if (other) { BD.v.cards = BD.v.cards.filter(x => x.id !== m.card); if (BD.card === m.card) closeModal(); bdPaint(); toast(`Moved to ${bname}`); }
          else { bdGot(nc); toast('Card moved'); }
        } catch (e) { if (c && old) { Object.assign(c, old); bdPaint(); } throw e; }
      }
      bdSoon();
    } catch (e) { toast(e.message, true); }
  },
  bdArchiveCard: async () => { const c = BD.cd; try { const nc = await bdApi('PATCH', bdCURL(c.id), { archived: true }); bdGot(nc); BD.cd = nc; bdCwPaint(); toast(`Archived #${nc.no}`); } catch (e) { toast(e.message, true); } },
  bdRestoreCard: async () => { const c = BD.cd; try { const nc = await bdApi('PATCH', bdCURL(c.id), { archived: false }); bdGot(nc); toast(`Restored #${nc.no}`); } catch (e) { toast(e.message, true); } },
  bdDeleteCard: (id, el) => bdPop('cdel', el, 'Delete card', () => bdConfirmPop('Delete this card for good? Its comments and attachments go with it.', 'bdDeleteCardGo', BD.card, 'Delete card')),
  bdDeleteCardGo: async id => { bdPopClose(true); try { await bdPost(bdCURL(id, '/delete')); BD.v.cards = BD.v.cards.filter(x => x.id !== id); closeModal(); bdPaint(); toast('Card deleted'); bdSoon(); } catch (e) { toast(e.message, true); } },
  bdCopyLink: async () => {
    const url = location.origin + location.pathname + '#boards/' + UI.bdBoard + '/' + BD.card;
    try { await navigator.clipboard.writeText(url); toast('Link copied'); }
    catch (e) { bdPop('clink', $('cw-sb-link'), 'Link to this card', () => `<div class="fld"><label for="cl-url">Copy this link</label><input class="in" id="cl-url" readonly value="${esc(url)}" data-autofocus></div>`); setTimeout(() => $('cl-url')?.select(), 20); }
  },
  bdLinkGo: () => { const l = BD.cd?.link; if (l?.view && viewAllowed(l.view)) A.go(l.view); },
  bdExtBtn: (id, el) => { const x = BOARDS.cardButtons.find(b => b.id === id); if (x && BD.cd) bdSafe(() => { x.run(BD.cd, BOARDS.ctx(), el); }, 'card button ' + id); },
  bdDetails: () => { UI.bdDetails = !UI.bdDetails; bdCwPaint(); },
  bdCmAdd: async () => {
    const t = $('cw-cm'), v = (t?.value || '').trim(), key = 'cm-' + BD.card;
    if (!v) { t?.focus(); return; }
    try { bdGot(await bdPost(bdCURL(BD.card, '/comments'), { text: v })); delete BD.drafts[key]; const t2 = $('cw-cm'); if (t2) { t2.value = ''; bdGrow(t2); } bdCwPaint(); }
    catch (e) { toast(e.message, true); }
  },
  bdCmEdit: id => { BD.cw.edit = id; delete BD.drafts['cmed-' + id]; bdCwPaint(); const t = $('cmed-' + id); if (t) { t.focus(); t.setSelectionRange(t.value.length, t.value.length); } },
  bdCmCancel: () => { const id = BD.cw.edit; BD.cw.edit = ''; delete BD.drafts['cmed-' + id]; bdCwPaint(); },
  bdCmSave: async id => {
    id = id || BD.cw.edit; const v = ($('cmed-' + id)?.value || '').trim(); if (!v) return;
    try { bdGot(await bdApi('PUT', bdCURL(BD.card, `/comments/${bdE(id)}`), { text: v })); BD.cw.edit = ''; delete BD.drafts['cmed-' + id]; bdCwPaint(); } catch (e) { toast(e.message, true); }
  },
  bdCmDel: (id, el) => bdPop('cmdel-' + id, el, 'Delete comment', () => bdConfirmPop('Delete this comment? This cannot be undone.', 'bdCmDelGo', id, 'Delete comment')),
  bdCmDelGo: async id => { bdPopClose(true); try { bdGot(await bdPost(bdCURL(BD.card, `/comments/${bdE(id)}/delete`))); toast('Comment deleted'); } catch (e) { toast(e.message, true); } },
  bdReact: async (id, el) => { try { bdGot(await bdPost(bdCURL(BD.card, `/comments/${bdE(id)}/react`), { emoji: el.dataset.e })); } catch (e) { toast(e.message, true); } },
  bdReactPick: (id, el) => bdPop('rx-' + id, el, 'React', () => `<div class="bd-rxs">${BD_RX.map(e => `<button class="bd-rxb" data-act="bdReactGo" data-id="${esc(id)}" data-e="${e}" aria-label="React ${e}">${e}</button>`).join('')}</div>`),
  bdReactGo: (id, el) => { bdPopClose(true); A.bdReact(id, el); },
  bdAcPick: i => bdAcPick(+i)
});
function bdListMenuHTML(sub) {
  const l = BD.v.board.lists.find(x => x.id === BD.lm), can = bdCan(), team = bdTeam();
  if (!l) return '';
  const others = bdLists().filter(x => x.id !== l.id), ls = bdLists(), i = ls.findIndex(x => x.id === l.id);
  const n = bdOpenCards().filter(c => c.list === l.id).length, watch = bdLWatching(l);
  const mi = (act, icon, label, id = '', extra = '') => `<button class="bd-mi" role="menuitem" data-act="${act}" data-id="${esc(id)}" ${extra}>${bdI(icon)}<span>${label}</span></button>`;
  if (sub === 'moveall') return others.length ? `<div class="bd-menu">${others.map(x => mi('bdListMoveAll', 'arrow', esc(x.name), x.id)).join('')}</div>` : '<p class="bd-mute">There is no other list.</p>';
  if (sub === 'sort') return `<div class="bd-menu">${mi('bdListSort', 'clock', 'Due date, soonest first', 'due')}${mi('bdListSort', 'desc', 'Card title, A to Z', 'title')}${mi('bdListSort', 'up', 'Newest first', 'newest')}${mi('bdListSort', 'down', 'Oldest first', 'oldest')}</div>`;
  if (sub === 'color') return `<div class="bd-sws">${BD_COLORS.map(x => `<button class="bd-swb bdc-${x}${l.color === x ? ' on' : ''}" data-act="bdListColor" data-id="${x}" aria-pressed="${l.color === x}" aria-label="${BD_CN[x]}" title="${BD_CN[x]}"></button>`).join('')}</div><div class="bd-pop-tools"><button class="btn sm" data-act="bdListColor" data-id="" ${l.color ? '' : 'disabled'}>No colour</button></div>`;
  if (sub === 'limit') return `<div class="bd-form1"><div class="fld"><label for="lm-limit">Most cards this list should hold</label><input class="in" type="number" min="0" max="1000" id="lm-limit" value="${l.limit || ''}" placeholder="No limit" data-enter="bdListLimit"><span class="bd-hint">The count turns orange when the list holds more. Cards are never refused.</span></div><div class="bd-row"><button class="btn pri sm" data-act="bdListLimit">Save</button>${l.limit ? '<button class="btn sm" data-act="bdListLimit" data-id="0">Remove limit</button>' : ''}</div></div>`;
  if (sub === 'archiveall') return bdConfirmPop(`Archive all ${bdPl(n, 'card')} in ${esc(l.name)}? You can restore them from Archived items.`, 'bdListArchiveAll', l.id, 'Archive all');
  if (sub === 'copy' || sub === 'move') return bdLmvHTML();
  // Anyone who can see the board can watch or collapse a list.
  const mine = `<button class="bd-mi${watch ? ' checked' : ''}" role="menuitemcheckbox" aria-checked="${watch}" data-act="bdListWatch">${bdI('eye')}<span>Watch<small>Hear when a card is added to this list or moved into it</small></span>${watch ? bdI('tick', 'bd-mi-tick') : ''}</button>${mi('bdListCollapse', 'collapse', 'Collapse list', l.id)}`;
  if (!can) return `<div class="bd-menu" role="menu">${mine}</div><p class="bd-mute">You can see this board but not change it.</p>`;
  return `<div class="bd-menu" role="menu">${mi('bdListAddCard', 'plus', 'Add a card')}
    ${team ? mi('bdLm', 'copy', 'Copy list…', 'copy') + mi('bdLm', 'board', 'Move list to another board…', 'move') : ''}
    ${n ? mi('bdLm', 'arrow', 'Move all cards in this list…', 'moveall') : ''}${n > 1 ? mi('bdLm', 'desc', 'Sort by…', 'sort') : ''}
    ${mine}
    <hr class="bd-sep">${mi('bdLm', 'tag', 'Colour…', 'color')}${mi('bdLm', 'filter', 'Card limit…', 'limit')}
    ${i > 0 ? mi('bdListShift', 'back', 'Move list left', '-1') : ''}${i < ls.length - 1 ? mi('bdListShift', 'arrow', 'Move list right', '1') : ''}
    <hr class="bd-sep">${n ? mi('bdLm', 'archive', 'Archive all cards in this list…', 'archiveall') : ''}${team ? mi('bdListArchive', 'archive', 'Archive this list') : ''}</div>`;
}
/* Copy list and Move list: the board (this one, or another team board the
   person can change), the place among its lists and, for a copy, a name. */
async function bdLmvOpen(mode, l) {
  BD.lmv = { mode, list: l.id, board: mode === 'copy' ? UI.bdBoard : '', bv: mode === 'copy' ? BD.v : null, pos: -1 };
  delete BD.drafts['lmv-name'];
  try {
    BD.boards = await get('/v1/boards');
    const m = BD.lmv; if (!m || m.list !== l.id) return;
    if (!m.board) { const t = bdLmvBoards()[0]; if (t) { m.board = t.id; bdLmvLoad(t.id); } }
    bdPopRefresh();
  } catch (e) { toast(e.message, true); }
}
function bdLmvBoards() {
  const m = BD.lmv;
  const bs = (BD.boards || []).filter(b => !b.closed && b.kind !== 'ledgerline' && ['admin', 'member'].includes(b.role) && (m.mode === 'copy' || b.id !== UI.bdBoard));
  if (m.mode === 'copy' && !bs.some(b => b.id === UI.bdBoard)) bs.unshift({ id: UI.bdBoard, name: BD.v.board.name });
  return bs;
}
function bdLmvLoad(id) {
  const m = BD.lmv; if (!m) return;
  m.board = id; m.pos = -1; m.bv = id === UI.bdBoard ? BD.v : null;
  if (!m.bv) get(`/v1/boards/${bdE(id)}`).then(v => { if (BD.lmv === m && m.board === id) { m.bv = v; bdPopRefresh(); } }).catch(x => toast(x.message, true));
}
function bdLmvHTML() {
  const m = BD.lmv, l = BD.v.board.lists.find(x => x.id === BD.lm); if (!m || !l) return '';
  const copy = m.mode === 'copy', bs = bdLmvBoards(), bv = m.bv, ls = bv ? bdLists(bv.board) : [];
  const here = bv && bv.board.id === UI.bdBoard, cur = here ? ls.findIndex(x => x.id === l.id) : -1;
  const pos = m.pos >= 0 ? Math.min(m.pos, ls.length) : here ? cur + 1 : ls.length;
  const n = bdOpenCards().filter(c => c.list === l.id).length;
  if (!copy && BD.boards && !bs.length) return '<p class="bd-mute">There is no other team board you can change. A list moves only between team boards.</p>';
  const posName = i => i === 0 ? 'First' : i === ls.length ? 'Last' : `After ${ls[i - 1].name}`;
  return `<div class="bd-form1">
    ${copy ? `<div class="fld"><label for="lmv-name">Name</label><input class="in" id="lmv-name" data-bdd="lmv-name" maxlength="120" value="${esc(bdDraft('lmv-name', l.name))}" data-enter="bdLmvGo"></div>` : ''}
    <div class="fld"><label for="lmv-board">Board</label><select id="lmv-board" class="in" ${BD.boards ? '' : 'disabled'}>${bs.map(b => `<option value="${esc(b.id)}" ${b.id === m.board ? 'selected' : ''}>${esc(b.name)}${b.id === UI.bdBoard ? ' (this board)' : ''}</option>`).join('')}</select></div>
    <div class="fld"><label for="lmv-pos">Position</label><select id="lmv-pos" class="in" ${bv ? '' : 'disabled'}>${Array.from({ length: ls.length + 1 }, (_, i) => `<option value="${i}" ${i === pos ? 'selected' : ''}>${i + 1} · ${esc(posName(i))}</option>`).join('')}</select></div>
    <p class="bd-hint">${copy ? `Its ${bdPl(n, 'open card')} come with their checklists, labels, dates, custom fields and the members who are on that board; not comments or attachments.` : `Its ${bdPl(n, 'open card')} go with it and take that board’s next numbers. Links to them still work. Archived cards stay here.`}</p>
    ${!bv ? '<p class="bd-mute">Loading the board…</p>' : ''}
    <div class="bd-row"><button class="btn pri sm" data-act="bdLmvGo" ${bv ? '' : 'disabled'}>${copy ? 'Create list' : 'Move list'}</button></div></div>`;
}
/* ---------- @mentions ---------- */
function bdAcUpdate(el) {
  const pos = el.selectionStart, m = /(^|[\s(])@([a-z0-9._-]*)$/i.exec(el.value.slice(0, pos));
  if (!m) { bdAcClose(); return; }
  const q = m[2].toLowerCase();
  // @card and (for the board's admins and members) @board come first.
  const groups = [...(['admin', 'member'].includes(BD.v?.role) ? [{ id: 'board', name: 'Everyone on this board', group: 'users' }] : []), ...(BD.card ? [{ id: 'card', name: 'The card’s members and watchers', group: 'card' }] : [])]
    .filter(g => g.id.startsWith(q) || g.name.toLowerCase().includes(q));
  const opts = [...groups, ...bdUsers().filter(u => u.id.toLowerCase().includes(q) || u.name.toLowerCase().includes(q)).slice(0, 6)];
  if (!opts.length) { bdAcClose(); return; }
  BD.ac = { id: el.id, start: pos - m[2].length - 1, end: pos, opts, i: 0 };
  let box = $('bd-ac');
  if (!box) {
    box = document.createElement('div'); box.id = 'bd-ac'; box.className = 'bd-ac'; box.setAttribute('role', 'listbox'); box.setAttribute('aria-label', 'People to mention');
    box.addEventListener('mousedown', e => e.preventDefault());
    document.body.appendChild(box);
  }
  bdAcPaint();
  const r = el.getBoundingClientRect();
  box.style.left = Math.min(r.left, innerWidth - 290) + 'px'; box.style.top = Math.min(r.bottom + 4, innerHeight - box.offsetHeight - 8) + 'px';
}
function bdAcPaint() {
  const a = BD.ac, box = $('bd-ac'); if (!a || !box) return;
  box.innerHTML = a.opts.map((u, i) => `<button type="button" role="option" id="bd-ac-${i}" class="${i === a.i ? 'on' : ''}${u.group ? ' grp' : ''}" aria-selected="${i === a.i}" data-act="bdAcPick" data-id="${i}">${u.group ? `<span class="bd-av sys" aria-hidden="true">${bdI(u.group)}</span>` : bdAv(u.id)}<span class="bd-prow-t">${u.group ? `@${esc(u.id)}<small>${esc(u.name)}</small>` : `${esc(u.name)}<small>@${esc(u.id)}</small>`}</span></button>`).join('');
  box.hidden = false;
  const el = $(a.id); if (el) { el.setAttribute('aria-activedescendant', 'bd-ac-' + a.i); el.setAttribute('aria-controls', 'bd-ac'); }
}
function bdAcPick(i) {
  const a = BD.ac, el = a && $(a.id); if (!a || !el) { bdAcClose(); return; }
  const u = a.opts[i]; if (!u) return;
  const v = el.value; el.value = v.slice(0, a.start) + '@' + u.id + ' ' + v.slice(a.end);
  const p = a.start + u.id.length + 2; el.focus(); el.setSelectionRange(p, p);
  if (el.dataset.bdd) BD.drafts[el.dataset.bdd] = el.value;
  bdAcClose();
}
function bdAcClose() { const a = BD.ac; BD.ac = null; const box = $('bd-ac'); if (box) box.hidden = true; if (a) $(a.id)?.removeAttribute('aria-activedescendant'); }

/* ---------- drag and drop (pointer events: mouse, pen and touch) ---------- */
function bdDragDown(e) {
  if ((e.pointerType === 'mouse' && e.button !== 0) || S.view !== 'boards' || !BD.v || BD.drag || !bdCan()) return;
  const t = e.target;
  let kind = '', el = null;
  if (bdViewId() === 'board') {
    const card = t.closest('#bd-board .bd-card');
    if (card && !t.closest('.bd-cq,.bd-lab,input,textarea,select,a')) { kind = 'card'; el = card; }
    else { const lh = t.closest('#bd-board .bd-lh'); if (lh && !t.closest('input,textarea,.bd-ib')) { kind = 'list'; el = lh.closest('.bd-list'); } }
  }
  if (!el) { const g = t.closest('.bd-cw .bd-it-grip'); if (g) { kind = 'item'; el = g.closest('.bd-it'); e.preventDefault(); } }
  if (!el) return;
  const d = BD.drag = { kind, el, id: el.dataset[kind === 'card' ? 'card' : kind === 'list' ? 'list' : 'it'], x0: e.clientX, y0: e.clientY, x: e.clientX, y: e.clientY, on: false, pid: e.pointerId, touch: e.pointerType === 'touch' };
  if (kind === 'card') { d.p0 = bdSib(el, 'previousElementSibling', 'card'); d.n0 = bdSib(el, 'nextElementSibling', 'card'); d.l0 = el.dataset.list; }
  if (d.touch && kind !== 'item') d.timer = setTimeout(() => { if (BD.drag === d && !d.on) bdDragStart(); }, 320);
  document.addEventListener('pointermove', bdDragMove);
  document.addEventListener('pointerup', bdDragUp);
  document.addEventListener('pointercancel', bdDragCancel);
}
function bdSib(el, dir, k) { let n = el[dir]; while (n && (n.classList.contains('bd-src') || n.classList.contains('bd-ph') || !n.dataset[k])) n = n[dir]; return n ? n.dataset[k] : ''; }
function bdDragMove(e) {
  const d = BD.drag; if (!d || e.pointerId !== d.pid) return;
  d.x = e.clientX; d.y = e.clientY;
  if (!d.on) {
    const dist = Math.hypot(d.x - d.x0, d.y - d.y0);
    if (d.touch && d.kind !== 'item') { if (dist > 10) bdDragEnd(); return; }
    if (dist < 5) return;
    bdDragStart();
  }
  e.preventDefault();
  bdDragTrack();
}
function bdDragStart() {
  const d = BD.drag, el = d.el, r = el.getBoundingClientRect();
  d.on = true; d.dx = d.x0 - r.left; d.dy = d.y0 - r.top;
  const g = el.cloneNode(true);
  g.removeAttribute('id'); g.querySelectorAll('[id]').forEach(x => x.removeAttribute('id'));
  g.classList.add('bd-ghost', 'bd-ghost-' + d.kind); g.setAttribute('aria-hidden', 'true');
  g.style.width = r.width + 'px'; if (d.kind !== 'list') g.style.height = r.height + 'px';
  document.body.appendChild(g); d.g = g;
  const ph = document.createElement(d.kind === 'item' ? 'li' : 'div');
  ph.className = 'bd-ph bd-ph-' + d.kind; ph.style.height = Math.min(r.height, d.kind === 'list' ? r.height : 400) + 'px'; if (d.kind === 'list') ph.style.width = r.width + 'px';
  el.parentNode.insertBefore(ph, el); el.classList.add('bd-src'); d.ph = ph;
  document.body.classList.add('bd-dragging');
  bdPopClose(true);
  if (d.touch && navigator.vibrate) { try { navigator.vibrate(8); } catch (x) {} }
  d.raf = requestAnimationFrame(bdDragTick);
  bdDragTrack();
}
function bdDragTrack() {
  const d = BD.drag; if (!d?.on) return;
  d.g.style.transform = `translate(${d.x - d.dx}px,${d.y - d.dy}px) rotate(${d.kind === 'card' ? 2.5 : d.kind === 'list' ? 1.2 : 0}deg)`;
  if (d.kind === 'card') {
    const lists = [...document.querySelectorAll('#bd-board .bd-list:not(.bd-src):not(.collapsed)')]; if (!lists.length) return;
    let tl = lists.find(l => { const r = l.getBoundingClientRect(); return d.x >= r.left && d.x <= r.right; });
    if (!tl) tl = lists.reduce((best, l) => { const r = l.getBoundingClientRect(), dist = Math.abs(d.x - (r.left + r.width / 2)); return !best || dist < best.dist ? { l, dist } : best; }, null).l;
    const box = tl.querySelector('.bd-cards');
    const before = [...box.querySelectorAll('.bd-card:not(.bd-src)')].find(c => { const r = c.getBoundingClientRect(); return d.y < r.top + r.height / 2; });
    if (before) { if (d.ph.nextElementSibling !== before) box.insertBefore(d.ph, before); } else if (box.lastElementChild !== d.ph) box.appendChild(d.ph);
    d.box = box;
  } else if (d.kind === 'list') {
    const cv = $('bd-board'); if (!cv) return;
    const before = [...cv.querySelectorAll('.bd-list:not(.bd-src)')].find(l => { const r = l.getBoundingClientRect(); return d.x < r.left + r.width / 2; }) || cv.querySelector('.bd-addl');
    if (before && d.ph.nextElementSibling !== before) cv.insertBefore(d.ph, before);
  } else {
    const ul = d.el.parentNode; if (!ul) return;
    const before = [...ul.querySelectorAll('.bd-it:not(.bd-src)')].find(c => { const r = c.getBoundingClientRect(); return d.y < r.top + r.height / 2; });
    if (before) { if (d.ph.nextElementSibling !== before) ul.insertBefore(d.ph, before); } else if (ul.lastElementChild !== d.ph) ul.appendChild(d.ph);
  }
}
function bdDragTick() {
  const d = BD.drag; if (!d?.on) return;
  let moved = false;
  const edge = (lo, hi, v, e) => v < lo + e ? -Math.ceil((lo + e - v) / 5) : v > hi - e ? Math.ceil((v - hi + e) / 5) : 0;
  const cv = $('bd-board');
  if (cv && d.kind !== 'item') { const r = cv.getBoundingClientRect(), s = edge(r.left, r.right, d.x, 70); if (s) { cv.scrollLeft += s; moved = true; } }
  if (d.kind === 'card' && d.box) { const b = d.box.closest('.bd-lbody'), r = b.getBoundingClientRect(), s = edge(r.top, r.bottom, d.y, 48); if (s) { b.scrollTop += s; moved = true; } }
  if (d.kind === 'item') { const ov = document.querySelector('#mroot .ov'); if (ov) { const s = edge(0, innerHeight, d.y, 60); if (s) { ov.scrollTop += s; moved = true; } } }
  if (moved) bdDragTrack();
  d.raf = requestAnimationFrame(bdDragTick);
}
function bdDragUp(e) {
  const d = BD.drag; if (!d || (e && e.pointerId !== d.pid)) return;
  if (!d.on) { bdDragEnd(); return; }
  BD.noClick = Date.now();
  const k = d.kind === 'card' ? 'card' : d.kind === 'list' ? 'list' : 'it';
  const res = { kind: d.kind, id: d.id, prev: bdSib(d.ph, 'previousElementSibling', k), next: bdSib(d.ph, 'nextElementSibling', k), list: d.ph.parentNode?.dataset?.list || '', cl: d.el.dataset.cl, p0: d.p0, n0: d.n0, l0: d.l0 };
  bdDragEnd();
  bdDrop(res);
}
function bdDragCancel(e) { const d = BD.drag; if (!d || (e && e.pointerId != null && e.pointerId !== d.pid)) return; if (d.on) BD.noClick = Date.now(); bdDragEnd(); }
function bdDragEnd() {
  const d = BD.drag; if (!d) return;
  clearTimeout(d.timer); if (d.raf) cancelAnimationFrame(d.raf);
  document.removeEventListener('pointermove', bdDragMove); document.removeEventListener('pointerup', bdDragUp); document.removeEventListener('pointercancel', bdDragCancel);
  d.g?.remove(); d.ph?.remove(); d.el.classList.remove('bd-src'); document.body.classList.remove('bd-dragging');
  BD.drag = null;
  if (BD.pend) { BD.pend = false; bdPaint(); }
}
async function bdDrop(r) {
  if (r.kind === 'card') {
    const c = BD.v.cards.find(x => x.id === r.id); if (!c || !r.list) return;
    if (r.list === r.l0 && r.prev === r.p0 && r.next === r.n0) { bdPaint(); return; }
    const P = r.prev && BD.v.cards.find(x => x.id === r.prev), N = r.next && BD.v.cards.find(x => x.id === r.next);
    const pos = bdMid(P?.pos, N?.pos), old = { list: c.list, pos: c.pos };
    c.list = r.list; c.pos = pos; bdPaint();
    try { bdGot(await bdPost(bdCURL(c.id, '/move'), { list: r.list, pos })); }
    catch (e) { Object.assign(c, old); bdPaint(); toast(e.message, true); }
  } else if (r.kind === 'list') {
    const ls = BD.v.board.lists, l = ls.find(x => x.id === r.id); if (!l) return;
    const P = r.prev && ls.find(x => x.id === r.prev), N = r.next && ls.find(x => x.id === r.next);
    const ord = bdLists(), i = ord.indexOf(l);
    if ((ord[i - 1]?.id || '') === (r.prev || '') && (ord[i + 1]?.id || '') === (r.next || '')) { bdPaint(); return; }
    const pos = bdMid(P?.pos, N?.pos), old = l.pos;
    l.pos = pos; bdPaint();
    try { await bdApi('PUT', bdURL(`/lists/${bdE(l.id)}`), { pos }); bdSoon(); }
    catch (e) { l.pos = old; bdPaint(); toast(e.message, true); }
  } else {
    const cl = (BD.cd?.checklists || []).find(x => x.id === r.cl); if (!cl) return;
    const it = cl.items.find(x => x.id === r.id); if (!it) return;
    const rest = cl.items.filter(x => x.id !== r.id);
    const idx = r.next ? rest.findIndex(x => x.id === r.next) : r.prev ? rest.findIndex(x => x.id === r.prev) + 1 : 0;
    if (idx < 0 || cl.items.indexOf(it) === idx) { bdCwPaint(); return; }
    rest.splice(idx, 0, it); cl.items = rest; bdCwPaint();
    bdCl({ op: 'move_item', checklist: cl.id, item: it.id, index: idx });
  }
}

/* ---------- events ---------- */
document.addEventListener('pointerdown', e => {
  if (BD.pop && !BD.pop.el.contains(e.target) && !e.target.closest?.(`[data-popkey="${CSS.escape(BD.pop.key)}"]`)) bdPopClose(true);
  if (BD.ac && !e.target.closest?.('#bd-ac') && e.target.id !== BD.ac.id) bdAcClose();
  if (BD.sresOpen && !e.target.closest?.('.bd-srch')) { BD.sresOpen = false; bdSresPaint(); }
}, true);
document.addEventListener('pointerdown', bdDragDown);
document.addEventListener('touchmove', e => { if (BD.drag?.on) e.preventDefault(); }, { passive: false });
document.addEventListener('contextmenu', e => { if (BD.drag) e.preventDefault(); });
document.addEventListener('dragstart', e => { if (e.target.closest?.('#bd-board,.bd-cw .bd-its')) e.preventDefault(); });
window.addEventListener('click', e => {
  if (Date.now() - BD.noClick < 400) { e.stopImmediatePropagation(); e.preventDefault(); return; }
  // The Boards entry in the menu always opens the boards home.
  if (e.target.closest?.('[data-act="go"][data-id="boards"]')) { UI.bdBoard = ''; BD.card = ''; BD.deep = ''; }
}, true);
document.addEventListener('pointerover', e => { const l = e.target.closest?.('#bd-board .bd-list'); if (l) BD.hover = l.dataset.list; });
document.addEventListener('focusin', e => {
  const l = e.target.closest?.('#bd-board .bd-list'); if (l) BD.hover = l.dataset.list;
  if (e.target.id === 'bd-search' && BD.sres) { BD.sresOpen = true; bdSresPaint(); }
});
document.addEventListener('focusout', e => {
  const el = e.target; if (BD.painting || !el.classList) return;
  if (el.classList.contains('bd-ln-in') && BD.rename === el.dataset.id) setTimeout(() => { if (BD.rename === el.dataset.id && document.activeElement !== el) A.bdListRenameSave(el.dataset.id); }, 0);
});
document.addEventListener('input', e => {
  const el = e.target;
  if (!el.dataset) return;
  if (el.dataset.bdd) BD.drafts[el.dataset.bdd] = el.value;
  if (el.id === 'bd-search') { bdSearchSoon(el.value); return; }
  if (el.id === 'bd-bname' && !BD_FIT) el.style.width = Math.max(4, Math.min(42, el.value.length + 1)) + 'ch';
  if (el.id === 'bd-fq') { UI.bdF.q = el.value; clearTimeout(BD.fT); BD.fT = setTimeout(bdPaint, 160); return; }
  if (el.dataset.popq) { bdPopRefresh(); return; }
  if (el.dataset.arq) { bdArchPaint(); return; }
  if (el.id === 'le-name' && BD.lbEdit) { const pv = $('le-pv'); if (pv) pv.textContent = el.value.trim() || BD_CN[BD.lbEdit.color]; return; }
  if (el.id === 'ck-name') delete el.dataset.auto;
  if (el.dataset.lab != null && BD.lab) { BD.lab[+el.dataset.lab][el.dataset.k] = el.value; const pv = $('lab-pv-' + el.dataset.lab), l = BD.lab[+el.dataset.lab]; if (pv) pv.textContent = l.name || BD_CN[l.color]; }
  if (el.classList.contains('bd-auto')) bdGrow(el);
  if (el.dataset.mention) bdAcUpdate(el);
});
document.addEventListener('change', e => {
  const el = e.target;
  if (el.id === 'bd-bname') {
    const v = el.value.trim(); delete BD.drafts.bname;
    if (!v || !BD.v || v === BD.v.board.name) { el.value = BD.v?.board.name || ''; return; }
    bdPutBoard({ name: v }).then(b => { BD.v.board = b; toast('Board renamed'); bdSoon(); }).catch(x => { toast(x.message, true); bdPaint(); });
    return;
  }
  if (el.id === 'mv-board' && BD.mv) {
    const m = BD.mv, id = el.value;
    m.board = id; m.pos = -1; m.bv = id === UI.bdBoard ? BD.v : null; m.list = m.bv ? bdLists(m.bv.board)[0]?.id || '' : '';
    if (id === UI.bdBoard) { const c = BD.v.cards.find(x => x.id === m.card); if (c) m.list = c.list; }
    bdPopRefresh();
    if (!m.bv) get(`/v1/boards/${bdE(id)}`).then(v => { if (BD.mv === m && m.board === id) { m.bv = v; m.list = bdLists(v.board)[0]?.id || ''; bdPopRefresh(); } }).catch(x => toast(x.message, true));
    return;
  }
  if (el.id === 'mv-list' && BD.mv) { BD.mv.list = el.value; BD.mv.pos = -1; bdPopRefresh(); return; }
  if (el.id === 'lmv-board' && BD.lmv) { bdLmvLoad(el.value); bdPopRefresh(); return; }
  if (el.id === 'lmv-pos' && BD.lmv) { BD.lmv.pos = +el.value; return; }
  if (el.name === 'st-vis') { const j = $('st-join'), d = $('st-join-d'), firm = el.value === 'firm'; if (j) { j.disabled = !firm || !bdAdmin(); if (!firm) j.checked = false; } if (d) d.textContent = firm ? 'People who are not members see a Join board button that makes them a member.' : 'For firm-wide boards only.'; return; }
  if (el.id === 'ck-copy') { const n = $('ck-name'), [cid, clid] = el.value.split('|'), cl = ((cid === BD.card ? BD.cd : BD.v.cards.find(x => x.id === cid))?.checklists || []).find(x => x.id === clid); if (n && cl && (!n.value.trim() || n.value === 'Checklist' || n.dataset.auto)) { n.value = cl.name; n.dataset.auto = '1'; } return; }
  if (el.id === 'mv-pos' && BD.mv) { BD.mv.pos = +el.value; return; }
  if (el.id && el.id.startsWith('mv-k-') && BD.mv) { BD.mv.keep[el.id.slice(5)] = el.checked; return; }
  if (el.dataset.lab != null && el.dataset.k === 'color' && BD.lab) { BD.lab[+el.dataset.lab].color = el.value; bdLabPaint(); return; }
  if (el.id === 'bd-attfile') { const fs = [...el.files]; el.value = ''; bdPopClose(true); bdUpload(fs); return; }
  if (el.id === 'bd-bgfile' && el.files[0]) {
    const f = el.files[0]; el.value = '';
    toast('Uploading the background…');
    bdApi('POST', bdURL(`/background?name=${bdE(f.name)}`), f, true).then(b => { BD.v.board = b; closeModal(); toast('Background changed'); bdPaint(); bdSoon(); }).catch(x => { const er = $('st-err'); if (er) er.innerHTML = errHTML(x); else toast(x.message, true); });
  }
});
document.addEventListener('keydown', e => {
  const el = e.target;
  if (BD.ac && el.id === BD.ac.id) {
    const a = BD.ac;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); a.i = (a.i + (e.key === 'ArrowDown' ? 1 : a.opts.length - 1)) % a.opts.length; bdAcPaint(); return; }
    if (e.key === 'Enter' || e.key === 'Tab') { e.preventDefault(); bdAcPick(a.i); return; }
  }
  if (!el.dataset || e.isComposing) return;
  if (e.key === 'Enter' && el.dataset.center && (e.ctrlKey || e.metaKey)) { e.preventDefault(); A[el.dataset.center]?.(el.dataset.id, el, e); return; }
  if (e.key === 'Enter' && el.dataset.enter && !e.shiftKey) { e.preventDefault(); A[el.dataset.enter]?.(el.dataset.id, el, e); return; }
  if (e.key === 'Enter' && el.id === 'bd-bname') { e.preventDefault(); el.blur(); }
  if (e.key === 'Enter' && el.id === 'bd-search' && BD.sres?.hits?.length) { e.preventDefault(); const h = BD.sres.hits[0]; BD.sresOpen = false; bdSresPaint(); BOARDS.openCard(h.board, h.card); }
  if (e.key === 'ArrowDown' && el.id === 'bd-search' && BD.sresOpen) { e.preventDefault(); document.querySelector('#bd-sres .bd-sr-hit')?.focus(); }
  if ((e.key === 'ArrowDown' || e.key === 'ArrowUp') && el.classList?.contains('bd-sr-hit')) { e.preventDefault(); const sib = e.key === 'ArrowDown' ? el.nextElementSibling : el.previousElementSibling; (sib?.classList.contains('bd-sr-hit') ? sib : e.key === 'ArrowUp' ? $('bd-search') : null)?.focus(); }
});
/* Shortcuts, like Trello's: boardux.js (with the selection, quick edit and undo). */
/* Files dropped on, or pasted into, the card window are attached to the card. */
const bdFileDrag = e => BD.card && bdCan() && [...(e.dataTransfer?.types || [])].includes('Files') && e.target.closest?.('#mroot .bd-ov');
document.addEventListener('dragover', e => { if (bdFileDrag(e)) { e.preventDefault(); document.querySelector('.bd-cw')?.classList.add('drop'); } });
document.addEventListener('dragleave', e => { if (BD.card && (!e.relatedTarget || !e.relatedTarget.closest?.('.bd-cw'))) document.querySelector('.bd-cw')?.classList.remove('drop'); });
document.addEventListener('drop', e => { if (bdFileDrag(e)) { e.preventDefault(); document.querySelector('.bd-cw')?.classList.remove('drop'); if (e.dataTransfer.files.length) bdUpload([...e.dataTransfer.files]); } });
document.addEventListener('paste', e => {
  if (S.view !== 'boards' || !BD.card || !bdCan() || !document.querySelector('#mroot .bd-cw')) return;
  const fs = [...(e.clipboardData?.files || [])]; if (!fs.length) return;
  e.preventDefault(); bdUpload(fs);
});
window.addEventListener('resize', () => { if (S.view === 'boards') { bdFit(); bdPopPlace(); } });
window.addEventListener('hashchange', () => {
  const m = /^#boards(?:\/([\w-]+)(?:\/([\w-]+))?)?$/.exec(location.hash || ''); if (!m) return;
  const b = m[1] || '', c = m[2] || '';
  if (S.view === 'boards' && b === UI.bdBoard && BD.v) { if (c && c !== BD.card) bdOpenCard(c); else if (!c && BD.card) closeModal(); return; }
  if (b) BOARDS.openCard(b, c); else BOARDS.openBoard('');
});
/* Live refresh: the board's change counter every 5 seconds while it is on
   screen; notifications every 30 seconds. */
setInterval(async () => {
  if (S.view !== 'boards' || !UI.bdBoard || !BD.v || document.hidden || BD.polling || BD.inflight) return;
  BD.polling = true;
  try { const r = await get(bdURL('/rev')); if (BD.v && r.rev !== BD.v.rev && !BD.inflight) await bdReload(); } catch (e) {} finally { BD.polling = false; }
}, 5000);
setInterval(() => { if (S.view === 'boards' && S.me && !document.hidden) bdNotices(); }, 30000);
document.addEventListener('visibilitychange', () => { if (!document.hidden && S.view === 'boards' && UI.bdBoard && BD.v) bdSoon(100); });
