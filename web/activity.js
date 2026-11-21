'use strict';
/* Ledgerline console: the Activity feed. A panel on the right of every
   screen, opened from the top bar, listing everything done this trading
   week (Monday 00:00 to Saturday 23:59 IST) and who did it, newest first,
   grouped by day. It reads GET /v1/activity, which turns the audit trail
   into sentences and shows each person only what they may see; while the
   panel is open and the tab is in view it asks every 10 seconds for what
   is newer (?after=<cursor>). Whether the panel is open, and the last entry
   the person looked at (the "new since you last looked" line), are kept
   per user on the server (PUT /v1/activity/state).
   Loaded after app.js and the layouts; uses $, esc, get, api, A, S, has. */

const AF = { boot: false, open: false, d: null, entries: [], cursor: 0, mark: 0, unseen: 0, more: false, before: 0, counts: [],
  f: { person: '', role: '', area: '', only: '', q: '' }, timer: 0, busy: false, saveT: 0, qT: 0, err: '', week: null };
const AF_POLL_MS = 10000;

// afAllowed: staff and branch/AP logins; not the client portal, not while
// sign-in setup is unfinished, not a support session limited to some areas.
function afAllowed() {
  const sup = S.me?.auth?.support;
  return !!S.me && !clientUser() && !(typeof setupOpen !== 'undefined' && setupOpen) && !S.me.auth?.must_change_password && !S.me.auth?.totp_setup_required && !(sup && !(sup.areas || []).includes('full'));
}
function afQS(extra) {
  const p = new URLSearchParams();
  Object.entries(AF.f).forEach(([k, v]) => { if (v) p.set(k, v); });
  Object.entries(extra || {}).forEach(([k, v]) => { if (v) p.set(k, String(v)); });
  const s = p.toString();
  return '/v1/activity' + (s ? '?' + s : '');
}
const afFiltered = () => Object.values(AF.f).some(Boolean);
// The IST date, from this computer's clock (headings only).
const afToday = () => new Date(Date.now() + 19800000).toISOString().slice(0, 10);
const afYesterday = () => new Date(Date.now() + 19800000 - 86400000).toISOString().slice(0, 10);
const AF_DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
function afDayName(d) {
  if (d === afToday()) return 'Today';
  if (d === afYesterday()) return 'Yesterday';
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(d); if (!m) return d;
  const w = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3])).getUTCDay();
  return `${AF_DAYS[w]} ${+m[3]} ${MON[+m[2] - 1]}`;
}

/* ---------- loading ---------- */
async function afLoad() {
  AF.busy = true; AF.err = ''; afRender();
  try {
    const d = await get(afQS({ limit: 150 }));
    AF.d = d; AF.week = d.week; AF.entries = d.entries || []; AF.cursor = d.cursor || 0; AF.more = d.more; AF.before = d.before || 0;
    AF.counts = d.counts || []; AF.unseen = d.unseen || 0;
    if (!AF.boot) { AF.boot = true; AF.open = !!d.open; }
    if (AF.open && !AF.markSet) { AF.mark = d.seen || 0; AF.markSet = true; }
  } catch (e) { AF.err = e.message; AF.boot = true; } // shown in the panel; not asked again on every screen
  AF.busy = false; afRender(); afTop(); afSchedule();
  if (AF.open) afSaveSeen();
}
async function afPoll() {
  if (!AF.open || document.hidden || AF.busy || !AF.cursor) return;
  try {
    const d = await get(afQS({ after: AF.cursor, limit: 200 }));
    if (AF.week && d.week.from !== AF.week.from) { AF.markSet = false; return afLoad(); } // a new week began
    AF.cursor = d.cursor || AF.cursor;
    const known = new Set(AF.entries.map(e => e.seq)), fresh = (d.entries || []).filter(e => !known.has(e.seq));
    if (fresh.length) {
      AF.entries = [...fresh, ...AF.entries];
      if (!afFiltered()) { // the day's counts move with what arrives
        const today = afToday();
        fresh.filter(e => e.day === today).forEach(e => { const c = AF.counts.find(x => x.actor === e.actor); if (c) c.n++; else if (AF.d?.counts) AF.counts.push({ actor: e.actor, who: e.who, kind: e.kind, role: e.role, n: 1 }); });
        AF.counts.sort((a, b) => b.n - a.n || a.who.localeCompare(b.who));
      }
      afRender(); afSaveSeen();
    }
    if (d.more_new) setTimeout(afPoll, 50);
  } catch (e) { /* the next tick tries again */ }
}
function afSchedule() {
  clearInterval(AF.timer); AF.timer = 0;
  if (AF.open && !document.hidden) AF.timer = setInterval(afPoll, AF_POLL_MS);
}
async function afEarlier() {
  if (!AF.more || AF.busy) return;
  AF.busy = true; afRender();
  try {
    const d = await get(afQS({ before: AF.before, limit: 150 }));
    const known = new Set(AF.entries.map(e => e.seq));
    AF.entries = [...AF.entries, ...(d.entries || []).filter(e => !known.has(e.seq))];
    AF.more = d.more; AF.before = d.before || 0;
  } catch (e) { toast(e.message, true); }
  AF.busy = false; afRender();
}
// afSaveSeen: the person has now looked at everything up to the cursor
// (sent a few seconds after the last change, while the panel is in view).
function afSaveSeen() {
  clearTimeout(AF.saveT);
  AF.saveT = setTimeout(() => { if (AF.open && !document.hidden && AF.cursor) api('PUT', '/v1/activity/state', { seen: AF.cursor }).catch(() => {}); AF.unseen = 0; afTop(); }, 3000);
}

/* ---------- drawing ---------- */
function afRoot() {
  let r = $('afpanel');
  if (!r) { r = document.createElement('aside'); r.id = 'afpanel'; r.className = 'af-panel'; r.setAttribute('aria-label', 'Activity this week'); document.body.appendChild(r); }
  return r;
}
function afAvatar(e) {
  if (e.kind === 'ledgerline') return '<span class="af-av af-sys" aria-hidden="true"><i></i><i></i><i></i></span>';
  return `<span class="af-av" aria-hidden="true">${esc(initials(e.who))}</span>`;
}
function afItem(e) {
  const go = e.view ? `data-act="afGo" data-id="${e.seq}"` : '';
  return `<li><button class="af-it ${e.kind === 'ledgerline' ? 'sys' : ''}" ${go} ${go ? '' : 'disabled'}>${afAvatar(e)}<span class="af-body">
    <span class="af-line"><b>${esc(e.who)}</b>${e.via ? ` <span class="af-via">${esc(e.via)}</span>` : e.role && e.kind === 'person' ? ` <span class="af-via">${esc(e.role)}</span>` : ''}<time datetime="${esc(e.at)}">${esc(e.at.slice(11, 16))}</time></span>
    <span class="af-what">${esc(e.what)}</span>
    <span class="af-meta"><span class="af-area">${esc(e.area)}</span>${e.ucc ? ` · <span class="mono">${esc(e.ucc)}</span>` : ''}${e.secure ? ' · <span class="af-sec">security</span>' : ''}</span>
  </span></button></li>`;
}
function afListHTML() {
  if (AF.err) return errHTML({ message: AF.err });
  if (!AF.entries.length) return `<p class="af-empty">${AF.busy ? 'Loading…' : afFiltered() ? 'Nothing this week matches these filters.' : 'Nothing has been done this week yet.'}</p>`;
  let html = '', day = '', marked = false;
  const showMark = AF.mark > 0 && AF.entries.some(e => e.seq > AF.mark);
  for (const e of AF.entries) {
    if (showMark && !marked && e.seq <= AF.mark) { marked = true; html += `${day ? '</ol>' : ''}<div class="af-mark" role="separator"><span>New since you last looked</span></div>${day ? '<ol class="af-list">' : ''}`; }
    if (e.day !== day) { html += `${day ? '</ol>' : ''}<h4 class="af-day">${esc(afDayName(e.day))}</h4><ol class="af-list">`; day = e.day; }
    html += afItem(e);
  }
  return html + '</ol>';
}
function afCountsHTML() {
  if (!AF.counts.length) return '';
  const top = AF.counts.slice(0, 6), rest = AF.counts.length - top.length;
  return `<div class="af-counts" aria-label="Today, by person"><small>Today</small>${top.map(c => `<button class="af-cnt ${AF.f.person === c.actor ? 'on' : ''}" data-act="afPerson" data-id="${esc(c.actor)}" title="${esc(c.role)}">${esc(c.who)} <b>${c.n}</b></button>`).join('')}${rest > 0 ? `<span class="dim small">+${rest} more</span>` : ''}</div>`;
}
function afRender() {
  const r = afRoot();
  document.body.classList.toggle('af-open', AF.open && afAllowed());
  if (!AF.open || !afAllowed()) { r.hidden = true; r.innerHTML = ''; return; }
  r.hidden = false;
  const d = AF.d || {}, w = AF.week;
  const opt = (v, l, cur) => `<option value="${esc(v)}" ${v === cur ? 'selected' : ''}>${esc(l)}</option>`;
  const only = [['', 'All'], ['people', 'People'], ['ledgerline', 'Ledgerline'], ['mine', 'Mine']];
  const focus = document.activeElement?.id === 'af-q';
  r.innerHTML = `<header class="af-head"><div><h3>Activity</h3><small>${w ? esc(w.label) : 'This week'}${w?.ended ? ' · week ended' : ''}</small></div>
      <button class="af-x" data-act="afToggle" aria-label="Close the activity feed">×</button></header>
    ${afCountsHTML()}
    <div class="af-filters">
      <input class="in" id="af-q" type="search" placeholder="Search this week" value="${esc(AF.f.q)}" autocomplete="off" aria-label="Search the activity">
      <div class="af-sel">
        <select class="in" id="af-person" aria-label="Person">${opt('', 'Everyone', AF.f.person)}${(d.people || []).map(p => opt(p.id, p.name, AF.f.person)).join('')}</select>
        <select class="in" id="af-role" aria-label="Desk or role">${opt('', 'Every desk', AF.f.role)}${(d.roles || []).map(x => opt(x, x, AF.f.role)).join('')}</select>
        <select class="in" id="af-area" aria-label="Area">${opt('', 'Every area', AF.f.area)}${(d.areas || []).map(x => opt(x, x, AF.f.area)).join('')}</select>
      </div>
      <div class="seg af-only" role="group" aria-label="Show">${only.map(([v, l]) => `<button class="${AF.f.only === v ? 'on' : ''}" data-act="afOnly" data-id="${v}" aria-pressed="${AF.f.only === v}">${l}</button>`).join('')}</div>
    </div>
    <div class="af-scroll" id="af-scroll">${afListHTML()}
      ${AF.more ? `<div class="af-more"><button class="btn sm" data-act="afEarlier" ${AF.busy ? 'disabled' : ''}>${AF.busy ? 'Loading…' : 'Show earlier this week'}</button></div>` : ''}
      <p class="af-foot">${w?.ended ? 'The trading week ended on Saturday; a new one starts on Monday at 00:00 IST.' : 'The feed starts again every Monday at 00:00 IST.'} ${d.audit_link ? 'Earlier weeks are in the <button class="linkish" data-act="go" data-id="audit">Audit trail</button>.' : 'Earlier weeks are not shown here.'}</p>
    </div>`;
  const q = $('af-q');
  q.oninput = () => { clearTimeout(AF.qT); AF.qT = setTimeout(() => { AF.f.q = q.value.trim(); afLoad(); }, 350); };
  if (focus) { q.focus(); q.setSelectionRange(q.value.length, q.value.length); }
  [['af-person', 'person'], ['af-role', 'role'], ['af-area', 'area']].forEach(([id, k]) => { $(id).onchange = e => { AF.f[k] = e.target.value; afLoad(); }; });
}
// afTop: the toggle on the top bar, on every screen and in every layout.
function afTop() {
  const t = $('top');
  if (!t) return;
  t.querySelector('.af-tog')?.remove();
  if (!afAllowed()) return;
  const n = AF.open ? 0 : AF.unseen;
  const b = document.createElement('button');
  b.className = 'btn sm af-tog' + (AF.open ? ' on' : ''); b.dataset.act = 'afToggle';
  b.setAttribute('aria-pressed', String(AF.open)); b.setAttribute('aria-controls', 'afpanel');
  b.title = AF.open ? 'Hide the activity feed' : 'Show what was done this week';
  b.setAttribute('aria-label', 'Activity' + (n ? `, ${n} new` : ''));
  b.innerHTML = `<svg viewBox="0 0 24 24" fill="none" stroke-width="2" aria-hidden="true"><path d="M3 12h4l3-8 4 16 3-8h4"/></svg><span>Activity</span>${n ? `<span class="ct">${n > 98 ? '99+' : n}</span>` : ''}`;
  const um = t.querySelector('.umenu');
  if (um) t.insertBefore(b, um); else t.appendChild(b);
}

/* ---------- actions ---------- */
Object.assign(A, {
  afToggle: () => {
    AF.open = !AF.open;
    api('PUT', '/v1/activity/state', AF.open ? { open: true } : { open: false, seen: AF.cursor }).catch(() => {});
    if (AF.open) { AF.markSet = false; afLoad(); } else { AF.mark = AF.cursor; AF.unseen = 0; afSchedule(); afRender(); afTop(); }
  },
  afOnly: v => { AF.f.only = v || ''; afLoad(); },
  afPerson: id => { AF.f.person = AF.f.person === id ? '' : id; afLoad(); },
  afEarlier: () => afEarlier(),
  afGo: seq => {
    const e = AF.entries.find(x => String(x.seq) === String(seq));
    if (!e) return;
    const v = e.view && viewAllowed(e.view) ? e.view : '';
    if (v && S.view !== v) A.go(v);
    if (e.ucc && typeof clientDrawer === 'function') setTimeout(() => clientDrawer(e.ucc), v && S.view !== v ? 300 : 0);
    if (window.matchMedia('(max-width: 1279px)').matches) { AF.open = false; afSchedule(); afRender(); afTop(); }
  },
});

/* ---------- shell hooks ---------- */
{
  const baseTop = renderTop, baseLogin = showLogin;
  renderTop = () => {
    baseTop();
    if (S.me && !AF.boot && !AF.busy && afAllowed()) afLoad();
    afTop();
  };
  showLogin = msg => {
    baseLogin(msg);
    clearInterval(AF.timer); clearTimeout(AF.saveT);
    Object.assign(AF, { boot: false, open: false, d: null, entries: [], cursor: 0, mark: 0, markSet: false, unseen: 0, counts: [], week: null, timer: 0, f: { person: '', role: '', area: '', only: '', q: '' } });
    afRender();
  };
}
document.addEventListener('visibilitychange', () => {
  if (!AF.open) return;
  afSchedule();
  if (!document.hidden) { afPoll(); afSaveSeen(); }
});
