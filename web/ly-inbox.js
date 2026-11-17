'use strict';
/* Ledgerline console: the B · Exceptions inbox layout (layouts.js).
   Only what needs a person: the open work queue grouped by when it is due,
   filtered by area and by whose it is, with the selected item's detail, its
   prepared action and keyboard shortcuts. A narrow icon rail opens the
   rest of Ledgerline: each icon opens its area's screens. */

UI.lyInHelp = true; // the keyboard panel starts open; H hides it (? stays Ledgy's key)

// An item's area: its automation's area, else the menu group of its screen.
const INB_AREA_BY_GROUP = { Overview: 'Operations', Accounts: 'Money', Investments: 'Clients', Business: 'Clients', Depository: 'Operations', Admin: 'Other' };
const INB_SHORT = { Operations: 'Ops', Risk: 'Risk', Money: 'Money', Compliance: 'Comp', Clients: 'Client', Accounts: 'Acct', 'Client insights': 'Ins', Other: '·' };
const INB_TONE = { Operations: 'acc', Risk: 'bad', Money: 'good', Compliance: 'cmp', Clients: 'nt', Accounts: 'good', 'Client insights': 'cmp', Other: 'nt' };
function inbArea(w) {
  const a = lyArea(w); if (a !== 'Other') return a;
  const v = String(w.view || '').split(':')[0], g = NAV.find(([, items]) => items.some(([k]) => k === v || (k[0] === '@' && (GROUPS[k.slice(1)]?.[1] || []).some(([t]) => t === v))));
  return g ? (INB_AREA_BY_GROUP[g[0]] || g[0]) : 'Other';
}
const inbView = w => String(w.view || '').split(':')[0];
const inbCanOpen = w => !!w.view && (viewAllowed(inbView(w)) || SCOPED_VIEWS.includes(inbView(w))) && !!V[inbView(w)];
const inbMineDefault = () => UI.lyInMine === '' ? (role() === 'Admin' ? 'all' : 'mine') : UI.lyInMine;
// The items shown, in order: overdue, due on the trade date or before, later.
function inbItems() {
  const mine = inbMineDefault() === 'mine', area = UI.lyInArea;
  const ws = lyWorkOpen().filter(w => (!mine || lyMine(w)) && (area === 'All' || inbArea(w) === area));
  const grp = w => w.overdue ? 0 : w.due && w.due.slice(0, 10) <= S.date ? 1 : 2;
  return ws.map(w => ({ w, g: grp(w) })).sort((a, b) => a.g - b.g || String(a.w.due || '9').localeCompare(String(b.w.due || '9')) || a.w.title.localeCompare(b.w.title)).map(x => x.w);
}
function inbSelected(items) {
  let w = items.find(x => x.id === UI.lyInSel);
  if (!w && items.length) { w = items[Math.min(UI.inbIdx || 0, items.length - 1)]; UI.lyInSel = w.id; }
  if (w) UI.inbIdx = items.indexOf(w);
  return w;
}
const inbBadge = a => `<span class="inb-badge ${INB_TONE[a] || 'nt'}" aria-hidden="true">${esc(INB_SHORT[a] || a.slice(0, 4))}</span>`;
const inbWhen = w => w.overdue ? `<span class="neg">${esc(w.due.slice(0, 10) === S.date ? w.due.slice(11) : fmtDate(w.due.slice(0, 10)))}</span>` : w.due ? esc(w.due.slice(0, 10) === S.date ? w.due.slice(11) : fmtDate(w.due.slice(0, 10))) : '';

/* ---------- the rail ---------- */
// [icon, label, either a screen or the menu groups it opens]
const INB_RAIL = [
  ['inbox', 'Inbox', 'lyInbox'],
  ['today', 'Today', 'dashboard'],
  ['ops', 'Ops', ['Operations']],
  ['clients', 'Clients', 'clients'],
  ['money', 'Money', ['Accounts']],
  ['risk', 'Risk', ['Risk']],
  ['dp', 'Depository', ['Depository']],
  ['compliance', 'Compliance', ['Compliance']],
  ['reports', 'Reports', 'statements'],
];
function inbRailOn(target) {
  if (typeof target === 'string') return S.view === target || (target === 'clients' && S.view === 'lyClient');
  return lyNavGroups().some(([g, items]) => target.includes(g) && items.some(([k]) => k === S.view)) && !['clients', 'statements', 'dashboard'].includes(S.view);
}
function inbRailBtn([ic, label, target]) {
  const groups = lyNavGroups(), names = groups.map(g => g[0]);
  if (typeof target === 'string') { if (!(viewAllowed(target) || SCOPED_VIEWS.includes(target))) return ''; }
  else if (!target.some(g => names.includes(g))) return '';
  const n = target === 'lyInbox' ? inbItems.countFor() : typeof target === 'string' ? lyCt(target) : 0;
  const act = typeof target === 'string' ? `data-act="go" data-id="${target}"` : `data-act="lyArea" data-id="${esc(target.filter(g => names.includes(g)).join('|'))}"`;
  return `<button class="inb-rb ${inbRailOn(target) ? 'on' : ''}" ${act} ${inbRailOn(target) ? 'aria-current="page"' : ''}>${lySvg(ic, 20)}<span>${esc(label)}</span>${n ? `<i class="inb-ct">${n > 99 ? '99+' : n}</i>` : ''}</button>`;
}
inbItems.countFor = () => lyWorkOpen().filter(w => inbMineDefault() !== 'mine' || lyMine(w)).length;

/* ---------- the header: the day by phase ---------- */
function inbPhases() {
  if (!LY.d) return '';
  const st = lySteps();
  return `<div class="inb-phases" aria-label="The day">${LY_SESS.map(([s, name]) => {
    const xs = st.filter(x => x.s === s && x.state !== 'na'); if (!xs.length) return '';
    const done = xs.filter(x => x.state === 'done').length, needs = xs.filter(x => x.state === 'needs').length;
    const [t, k] = needs ? [`${name} · ${needs} need${needs === 1 ? 's' : ''} someone`, 'warn'] : done === xs.length ? [`${name} · ${done} of ${xs.length}`, 'good'] : [`${name} · ${done} of ${xs.length} done`, 'nt'];
    return `<button class="pill ${k} inb-ph" data-act="inbPhase" data-id="${s}" title="${esc(xs.map(x => x.name + ': ' + x.note).join('\n'))}">${esc(t)}</button>`;
  }).filter(Boolean).join('<span class="inb-sep" aria-hidden="true"></span>')}</div>`;
}
const inbDay = () => { const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(S.date); if (!m) return esc(S.date); const d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3])); return `${['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][d.getUTCDay()]} ${+m[3]} ${MON[+m[2] - 1]} ${m[1]}`; };

LYS.inbox = {
  side: () => `<button class="inb-logo" data-act="lyHomeGo" title="Inbox" aria-label="Ledgerline: inbox">${lyMarkLogo()}</button>
    <nav class="inb-rail" aria-label="Main">${INB_RAIL.map(inbRailBtn).join('')}</nav>
    <span class="inb-fill"></span>
    <button class="inb-rb" data-act="lyMenu">${lySvg('menu', 20)}<span>All screens</span></button>
    ${lyNavGroups().some(([g]) => g === 'Admin') ? `<button class="inb-rb ${inbRailOn(['Admin']) ? 'on' : ''}" data-act="lyArea" data-id="Admin">${lySvg('admin', 20)}<span>Admin</span></button>` : ''}`,
  top: () => `<div class="ly-top inb-top">
    <div class="inb-when"><b>${inbDay()}</b><small>${esc(S.me?.firm || '')}</small></div>
    ${lyDateNav()}${lyPayIn()}
    ${inbPhases()}
    <span class="ly-push"></span>${lySearch('Jump to a client, screen or action')}${lyUser()}</div>`,
  key: e => {
    if (S.view !== 'lyInbox') return;
    const k = e.key, items = inbItems(), w = inbSelected(items);
    if (k === 'j' || k === 'k' || k === 'ArrowDown' && e.shiftKey || k === 'ArrowUp' && e.shiftKey) {
      if (!items.length) return; e.preventDefault();
      const i = Math.max(0, Math.min(items.length - 1, (UI.inbIdx || 0) + (k === 'j' || k === 'ArrowDown' ? 1 : -1)));
      inbSelect(items[i].id, true); return;
    }
    if (k === 'h') { e.preventDefault(); UI.lyInHelp = !UI.lyInHelp; inbRedraw(); return; }
    if (!w) return;
    if (k === 'o') { if (inbCanOpen(w)) { e.preventDefault(); A.go(w.view); } return; }
    if (k === 'x') { if (lyCanAct(w)) { e.preventDefault(); A.wDismiss(w.id); } return; }
    if (k === 'p') { if (w.proposed && lyCanAct(w)) { e.preventDefault(); A.inbDo(w.id); } return; }
    if (k === 'c' && w.ucc) { e.preventDefault(); clientDrawer(w.ucc); }
  },
};

/* ---------- the inbox ---------- */
V.lyInbox = {
  load: async () => { await lyData(0); return LY; },
  html: () => {
    const all = lyWorkOpen(), mineAll = inbMineDefault() === 'mine' ? all.filter(lyMine) : all;
    const areas = [...new Set(mineAll.map(inbArea))].sort();
    if (UI.lyInArea !== 'All' && !areas.includes(UI.lyInArea)) UI.lyInArea = 'All';
    const items = inbItems();
    return `<div class="inb">
      <section class="inb-list panel" aria-label="Work items">
        <div class="inb-lhd"><div class="inb-ttl"><h1>Inbox</h1><span class="dim small">${lyPlural(items.length, 'item')}${inbMineDefault() === 'mine' ? ` for ${esc(role())}` : ' for everyone'}</span></div>
          <div class="inb-filters"><div class="seg" role="group" aria-label="Whose items">${[['mine', 'Mine'], ['all', 'Everyone']].map(([v, l]) => `<button class="${inbMineDefault() === v ? 'on' : ''}" data-act="inbMine" data-id="${v}" aria-pressed="${inbMineDefault() === v}">${l}</button>`).join('')}</div>
          <div class="inb-chips" role="group" aria-label="Area">${['All', ...areas].map(a => `<button class="inb-chip ${UI.lyInArea === a ? 'on' : ''}" data-act="inbArea" data-id="${esc(a)}" aria-pressed="${UI.lyInArea === a}">${esc(a)}${a === 'All' ? '' : ` <b>${mineAll.filter(w => inbArea(w) === a).length}</b>`}</button>`).join('')}</div></div></div>
        <div id="inb-body" class="inb-scroll">${P['inb-body']()}</div>
      </section>
      <section class="inb-detail" id="inb-detail" aria-label="Selected item" aria-live="polite">${inbDetail(inbSelected(items))}</section>
    </div>`;
  },
};
// The list, regrouped (filters redraw it through the app's data-part).
P['inb-body'] = () => {
  const items = inbItems(), sel = inbSelected(items);
  if (!items.length) { setTimeout(inbFit, 0); return `<div class="inb-empty">${lyWorkOpen().length ? 'Nothing here with these filters.' : '<b>Nothing needs a person.</b><br>Autopilot will put anything that does here, with its due time.'}</div>`; }
  const groups = [['Overdue', items.filter(w => w.overdue)], ['Due today', items.filter(w => !w.overdue && w.due && w.due.slice(0, 10) <= S.date)], ['Later', items.filter(w => !w.overdue && !(w.due && w.due.slice(0, 10) <= S.date))]];
  // The detail follows the selection when the filters change it.
  setTimeout(() => { const d = $('inb-detail'); if (d && d.firstElementChild?.dataset.id !== (sel?.id || undefined)) d.innerHTML = inbDetail(sel); inbFit(); }, 0);
  return groups.filter(g => g[1].length).map(([name, ws]) => `<div class="inb-gh">${esc(name)} <span>${ws.length}</span></div>${ws.map(w => `<button class="inb-row ${w.id === sel?.id ? 'on' : ''}" data-act="inbSel" data-id="${esc(w.id)}" aria-current="${w.id === sel?.id}">
      ${inbBadge(inbArea(w))}<span class="inb-rt"><b>${esc(w.title)}</b><span>${esc(w.ucc ? w.ucc + ' · ' : '')}${esc(w.detail || lyProc(w))}</span></span><span class="inb-due">${inbWhen(w)}${w.proposed ? `<i title="An action is prepared">${lySvg('approve', 14)}</i>` : ''}</span></button>`).join('')}`).join('');
};
function inbDetail(w) {
  if (!w) return `<div class="inb-none">${lySvg('inbox', 40)}<h2>Nothing selected</h2><p class="dim">Pick an item on the left, or press <kbd>J</kbd> and <kbd>K</kbd> to move through them.</p>${inbKeys()}</div>`;
  const area = inbArea(w), can = lyCanAct(w), open = inbCanOpen(w);
  const clientLink = w.ucc ? `<button class="linkish mono" data-act="client" data-id="${esc(w.ucc)}">${esc(w.ucc)}</button>` : '';
  const named = !w.ucc && /^[A-Z]{1,4}\d{3,}(,\s*[A-Z]{1,4}\d{3,})*$/.test(String(w.detail || '').trim()) ? String(w.detail).split(/,\s*/).slice(0, 12).map(u => `<button class="linkish mono" data-act="client" data-id="${esc(u)}">${esc(u)}</button>`).join(', ') : '';
  return `<div class="inb-d" data-id="${esc(w.id)}">
    <div class="inb-meta">${inbBadge(area)}<span>${esc(area)}</span><span aria-hidden="true">·</span><span>${esc(lyProc(w))}</span><span aria-hidden="true">·</span><span class="mono">${esc(w.id)}</span></div>
    <div class="inb-h"><h2>${esc(w.title)}</h2>${lyDue(w)}</div>
    <dl class="inb-facts">${clientLink ? `<div><dt>Client</dt><dd>${clientLink}</dd></div>` : ''}<div><dt>Owner</dt><dd>${esc(w.role || 'Anyone')}${can ? '' : ' <span class="dim">(not you)</span>'}</dd></div><div><dt>Raised</dt><dd>${esc(fmtDate(w.date))}${w.created ? ` <span class="dim">· ${esc(lyHHMM(w.created))}</span>` : ''}</dd></div>${open ? `<div><dt>Screen</dt><dd>${esc((NAV_VIEWS.find(v => v[0] === inbView(w)) || [, inbView(w)])[1])}</dd></div>` : ''}</dl>
    <div class="panel inb-card"><div class="bd">
      ${named ? `<p class="inb-txt">Clients: ${named}</p>` : w.detail ? `<p class="inb-txt">${esc(w.detail)}</p>` : ''}
      ${w.proposed ? `<div class="inb-prep"><span class="inb-prep-l">${lySvg('approve', 16)} Prepared by Autopilot</span><b>${esc(w.proposed.label)}</b>${can ? '<span class="dim small">Nothing happens until someone approves it.</span>' : `<span class="dim small">Waiting for ${esc(w.role)}.</span>`}</div>` : `<p class="dim small">Nothing is prepared for this one: open the screen to deal with it${can ? ', then close it here with a note if it doesn’t close itself' : ''}.</p>`}
      <div class="inb-acts">
        ${w.proposed && can ? `<button class="btn pri" data-act="inbDo" data-id="${esc(w.id)}">${esc(w.proposed.label)} <kbd>P</kbd></button>` : ''}
        ${open ? `<button class="btn ${w.proposed && can ? '' : 'pri'}" data-act="go" data-id="${esc(w.view)}">Open the screen <kbd>O</kbd></button>` : ''}
        ${can ? `<button class="btn" data-act="wDismiss" data-id="${esc(w.id)}">Close with a note <kbd>X</kbd></button>` : ''}
        ${lgAllowed() ? `<button class="btn" data-act="inbAsk" data-id="${esc(w.id)}">${lySvg('ledgy', 15)} Ask Ledgy</button>` : ''}
      </div></div></div>
    <p class="dim small">Items close themselves when what raised them clears; an item closed by hand keeps its note in the record.</p>
    ${inbKeys()}</div>`;
}
function inbKeys() {
  return `<div class="panel inb-keys"><div class="hd"><h3>Keyboard</h3><button class="linkish small" data-act="inbHelp">${UI.lyInHelp ? 'Hide' : 'Show all'}</button></div>${UI.lyInHelp ? `<div class="bd inb-kgrid">
    <span><kbd>J</kbd> <kbd>K</kbd> next, previous</span><span><kbd>P</kbd> do the prepared action</span><span><kbd>O</kbd> open the screen</span><span><kbd>X</kbd> close with a note</span><span><kbd>C</kbd> open the client</span><span><kbd>${/Mac|iPhone|iPad/.test(navigator.platform) ? '⌘' : 'Ctrl'}</kbd> <kbd>K</kbd> jump anywhere</span><span><kbd>H</kbd> show or hide this</span><span><kbd>?</kbd> ask Ledgy</span></div>` : ''}</div>`;
}
// inbRepaint: the whole inbox again from the data already loaded.
function inbRepaint() { if (S.view !== 'lyInbox') return; const sc = document.querySelector('.inb-scroll')?.scrollTop || 0; $('page').innerHTML = groupBar('lyInbox') + V.lyInbox.html(); bindPage(); renderSide(); const b = document.querySelector('.inb-scroll'); if (b) b.scrollTop = sc; }
// inbFit: the list stays in view under the header, however tall it wraps.
function inbFit() { const b = document.querySelector('.inb'), t = $('top'); if (b && t) b.style.setProperty('--inb-top', t.offsetHeight + 'px'); }
addEventListener('resize', () => { if (S.view === 'lyInbox') inbFit(); });
function inbRedraw() { const d = $('inb-detail'); if (d) d.innerHTML = inbDetail(inbSelected(inbItems())); }
function inbSelect(id, scroll) {
  UI.lyInSel = id;
  document.querySelectorAll('.inb-row').forEach(b => { const on = b.dataset.id === id; b.classList.toggle('on', on); b.setAttribute('aria-current', on); if (on && scroll) b.scrollIntoView({ block: 'nearest' }); });
  inbRedraw();
}
Object.assign(A, {
  inbSel: id => inbSelect(id),
  inbArea: id => { UI.lyInArea = id; inbRepaint(); },
  inbMine: id => { UI.lyInMine = id; inbRepaint(); },
  inbHelp: () => { UI.lyInHelp = !UI.lyInHelp; inbRedraw(); },
  inbPhase: s => { if (lyCur() === 'inbox') lyMenu(); else A.go('lyInbox'); },
  inbAsk: id => { const w = lyWorkOpen().find(x => x.id === id); if (!w) return; LG.open = true; lgAsk(`What should I do about this work item: "${w.title}"${w.detail ? ` (${w.detail})` : ''}? It is for ${w.role || 'anyone'} and comes from ${lyProc(w)}.`); },
  inbDo: id => {
    const w = lyWorkOpen().find(x => x.id === id); if (!w?.proposed) return;
    confirmModal(esc(w.proposed.label), esc(w.title), `<p>${esc(w.detail || '')}</p><p class="dim small">Prepared by Autopilot. Your name and the time go into the audit trail.</p>`, esc(w.proposed.label), async () => { await act(() => post(`/v1/work/${encodeURIComponent(id)}`, { do: true }), r => 'Done: ' + r.title); });
  },
});
