'use strict';
/* Ledgerline console: the F · Role home layout (layouts.js).
   A home of tiles chosen for the person's role. Each person can choose and
   order their own tiles (/v1/me/ui → tiles; none saved = the role's tiles).
   An Admin can look at the home each role sees. Every tile opens its
   screen. Numbers come from /v1/dashboard and /v1/autopilot (lyData). */

const RH_ROLES = [['Admin', 'Director'], ['Ops maker', 'Ops maker'], ['Ops checker', 'Ops checker'], ['Risk manager', 'Risk'], ['Compliance officer', 'Compliance']];
const RH_PRESET = {
  'Admin': ['today', 'needs', 'turnover', 'cover', 'margin', 'autopilot', 'clients', 'notes', 'compliance', 'backups'],
  'Ops maker': ['today', 'queue', 'recon', 'notes', 'settle', 'clients', 'bank', 'turnover'],
  'Ops checker': ['today', 'needs', 'recon', 'settle', 'notes', 'bank', 'eod', 'queue'],
  'Risk manager': ['needs', 'margin', 'breaches', 'penalty', 'cover', 'pledges', 'queue', 'turnover'],
  'Compliance officer': ['needs', 'compliance', 'reports', 'queue', 'clients', 'today'],
};
const rhRoleNow = () => (role() === 'Admin' && UI.rhRole) || role();
const rhRoleName = r => r === 'Admin' ? 'director' : r.toLowerCase();
// The open work items a role sees: its own and unassigned ones; a director
// sees those plus every prepared action waiting for approval.
const rhItems = r => lyWorkOpen().filter(w => r === 'Admin' ? !w.role || w.role === 'Admin' || w.proposed : w.role === r || !w.role)
  .sort((a, b) => (b.overdue - a.overdue) || String(a.due || '9').localeCompare(String(b.due || '9')));
const rhCanAct = (w, r) => r === 'Admin' || !w.role || w.role === r;
const rhGo = v => viewAllowed(v) || String(v).startsWith('ly') ? v : 'dashboard';
const rhQueue = () => typeof V.lyInbox === 'object' ? 'lyInbox' : viewAllowed('autopilot') ? 'autopilot' : 'dashboard';
const rhSum = (d, ks) => ks.reduce((s, k) => s + (d[k] || 0), 0);

// The tiles: title, size (columns of 12 × rows), the screen it opens, and
// its body. when(c) hides a tile that has nothing to say (backups).
const RH_TILES = {
  today: { t: 'Today', w: 5, h: 2, dark: true, go: 'dashboard', about: 'The day’s steps: how many are done and which need someone.',
    body: c => { const st = c.steps.filter(s => s.s !== 'commodity' && s.state !== 'na'), n = k => st.filter(s => s.state === k).length, nd = st.filter(s => s.state === 'needs');
      return `<div class="rh-big"><b>${n('done')} of ${st.length}</b><span>steps of the equity day done</span></div>
        <div class="rh-seg" aria-hidden="true"><i class="good" style="flex:${n('done')}"></i><i class="warn" style="flex:${n('needs')}"></i><i class="wait" style="flex:${n('wait')}"></i></div>
        <div class="rh-list">${nd.slice(0, 4).map(s => `<button data-act="go" data-id="${rhGo(s.views[0])}"><span>${esc(s.name)}: ${esc(s.note)}</span><em>${esc(s.who.replace(/^Ops (maker|checker)$/, 'Ops').replace(' manager', '').replace(' officer', ''))}</em></button>`).join('') || '<span class="rh-dim">No step needs anyone.</span>'}${nd.length > 4 ? `<span class="rh-dim">and ${nd.length - 4} more</span>` : ''}</div>`; } },
  needs: { t: 'Needs you', w: 4, h: 2, ring: true, go: () => rhQueue(), about: 'Prepared actions waiting for your approval, then the rest of your queue.',
    body: c => { const pr = c.items.filter(w => w.proposed && rhCanAct(w, c.role)).slice(0, 2), rest = c.items.filter(w => !pr.includes(w)).slice(0, pr.length ? 2 : 4);
      return `${pr.map(w => `<div class="rh-prop"><div><b>${esc(w.title)}</b><span>${esc(w.detail || lyProc(w))}</span></div><div class="rh-btns">${lyCanAct(w) ? `<button class="btn sm pri" data-act="wDo" data-id="${esc(w.id)}">${esc(w.proposed.label || 'Approve')}</button>` : ''}<button class="btn sm" data-act="go" data-id="${esc(w.view)}">Review</button></div></div>`).join('')}
        <div class="rh-list">${rest.map(w => `<button data-act="go" data-id="${esc(w.view)}"><span>${esc(w.title)}</span><em class="${w.overdue ? 'bad' : ''}">${w.overdue ? 'overdue' : w.due ? esc(w.due.slice(11)) : ''}</em></button>`).join('')}</div>
        ${!c.items.length ? '<span class="rh-dim">Nothing waiting for you.</span>' : c.items.length > pr.length + rest.length ? `<button class="linkish small" data-act="go" data-id="${rhQueue()}">All ${c.items.length} in your queue →</button>` : ''}`; } },
  turnover: { t: 'Turnover today', w: 3, go: 'dashboard', about: 'Turnover and exchange trades for the day.', body: c => `<b class="rh-num">${cmp(c.d.turnover)}</b><span class="rh-sub">${nf(c.d.trades)} exchange trades</span>` },
  cover: { t: 'Client funds cover', w: 3, go: 'bank', about: 'Bank and clearing balances against client credits.', body: c => `<b class="rh-num ${c.d.funds.ok ? 'good' : 'bad'}">${num(c.d.funds.cover).toFixed(2)}×</b><span class="rh-sub">bank and clearing balances vs client credits</span>` },
  margin: { t: 'Margin', w: 5, h: 2, go: 'rms', about: 'Required against collected, and clients by margin used.',
    body: c => { const d = c.d, bk = d.util_buckets || [], cls = ['good', 'acc', 'warn', 'bad'];
      return `<div class="rh-pair"><span><b class="rh-num">${cmp(d.required)}</b><small>required</small></span><span><b class="rh-num">${cmp(d.collected)}</b><small>collected</small></span></div>
        <div class="rh-seg tall">${bk.map((b, i) => b.count ? `<i class="${cls[i]}" style="flex:${b.count}"></i>` : '').join('')}</div>
        <div class="rh-legend">${bk.map((b, i) => `<span class="${i === 3 && b.count ? 'bad' : ''}">${esc(b.label)} · ${b.count}</span>`).join('')}</div>
        <span class="rh-sub">${lyPlural(d.risk.WithMargin || 0, 'client')} with positions${(d.top_util || []).length ? ' · most used:' : ''}</span>
        <div class="rh-list">${(d.top_util || []).slice(0, 3).map(x => `<button data-act="client" data-id="${esc(x.ucc)}"><span><span class="mono">${esc(x.ucc)}</span> ${esc(x.name)}</span><em class="${x.status === 'Breach' ? 'bad' : ''}">${x.utilisation >= 9.99 ? '>999' : Math.round(x.utilisation * 100)}% · ${esc(x.status || '')}</em></button>`).join('')}</div>`; } },
  autopilot: { t: 'Autopilot', w: 4, go: () => viewAllowed('autopilot') ? 'autopilot' : rhQueue(), about: 'What Autopilot did by itself today.',
    body: c => c.ap ? `<div class="rh-inline"><b class="rh-num">${nf(c.ap.done_today)}</b><span>actions today · ${Math.round(c.ap.stp_pct || 0)}% needed no one · ${((c.ap.minutes_saved_today || 0) / 60).toFixed(1)} h saved</span></div>` : '<span class="rh-dim">Not available</span>' },
  clients: { t: 'Clients', w: 3, go: 'clients', about: 'Active clients and those being onboarded.', body: c => `<b class="rh-num">${nf(rhSum(c.d.risk, ['OK', 'Watch', 'Breach', 'Debit']))}</b><span class="rh-sub">active · ${c.d.onboarding || 0} being onboarded · ${c.d.clients_traded} traded today</span>` },
  notes: { t: 'Contract notes', w: 4, go: 'notes', about: 'Contract notes sent, and any that bounced.',
    body: c => { const t = c.d.notes.total || 0, s = c.d.notes.Sent || 0; return t ? `<div class="rh-inline"><b class="rh-num">${s}<small> of ${t} sent</small></b>${c.d.notes.Bounced ? pill(c.d.notes.Bounced + ' bounced', 'warn') : t > s ? pill(t - s + ' not delivered', 'warn') : pill('All delivered', 'good')}</div>` : '<span class="rh-dim">Not generated yet</span>'; } },
  compliance: { t: 'Compliance', w: 3, go: 'surv', about: 'Open surveillance alerts and reports due.', body: c => `<b class="rh-num ${c.d.alerts.high ? 'bad' : ''}">${c.d.alerts.open || 0}</b><span class="rh-sub">open alerts · ${c.d.alerts.high || 0} high · ${c.d.reports_due || 0} reports due</span>` },
  backups: { t: 'Backups', w: 5, warnTile: true, go: 'backups', when: c => !!c.d.extra?.backup_problem, about: 'Shown only when backups need attention.', body: c => `<b class="rh-line">Backups need attention</b><span class="rh-sub">${esc(c.d.extra.backup_problem)}</span>` },
  recon: { t: 'Reconciliation', w: 3, go: 'recon', about: 'Reconciliation breaks still open.', body: c => `<b class="rh-num ${c.d.recon.OPEN ? 'bad' : c.d.recon.total ? 'good' : ''}">${c.d.recon.OPEN || 0}</b><span class="rh-sub">${c.d.recon.total ? `open breaks of ${c.d.recon.total} found today` : 'not reconciled yet'}</span>` },
  breaches: { t: 'Margin breaches', w: 3, go: 'rms', about: 'Clients in margin breach and the amount uncovered.', body: c => `<b class="rh-num ${c.d.risk.Breach ? 'bad' : 'good'}">${c.d.risk.Breach || 0}</b><span class="rh-sub">${c.d.risk.Breach ? cmp(c.d.extra.breach_amount) + ' uncovered' : 'no client in breach'}</span>` },
  settle: { t: 'Settlement', w: 3, go: 'settlement', about: 'Settlement exceptions for the next pay-in.', body: c => { const n = rhSum(c.d.settlement, SETT_X); return `<b class="rh-num ${n ? 'warn' : ''}">${n}</b><span class="rh-sub">exceptions · pay-in ${fmtDate(c.d.extra.pay_in)}</span>`; } },
  bank: { t: 'Bank credits', w: 3, go: 'bank', about: 'Third-party and unidentified credits to look at.', body: c => { const n = rhSum(c.d.bank, ['Third-party', 'Unidentified']); return `<b class="rh-num ${n ? 'warn' : ''}">${n}</b><span class="rh-sub">to look at · ${c.d.bank.Matched || 0} matched</span>`; } },
  reports: { t: 'Regulatory reports', w: 3, go: 'reports', about: 'Daily reports not yet uploaded.', body: c => `<b class="rh-num ${c.d.reports_due ? 'warn' : 'good'}">${c.d.reports_due || 0}</b><span class="rh-sub">not uploaded</span>` },
  penalty: { t: 'Penalty exposure', w: 3, go: 'margin', about: 'The day’s estimated short-collection penalty.', body: c => `<b class="rh-num ${num(c.d.penalty) > 0 ? 'warn' : ''}">${inr(c.d.penalty)}</b><span class="rh-sub">short-collection estimate</span>` },
  queue: { t: 'Your queue', w: 4, go: () => rhQueue(), about: 'Your open work items by when they are due.',
    body: c => { const o = c.items.filter(w => w.overdue).length, t = c.items.filter(w => !w.overdue && String(w.due || '').startsWith(S.date)).length;
      return `<div class="rh-trio"><span><b class="rh-num ${o ? 'bad' : ''}">${o}</b><small>overdue</small></span><span><b class="rh-num">${t}</b><small>due today</small></span><span><b class="rh-num">${c.items.length - o - t}</b><small>later</small></span></div>`; } },
  eod: { t: 'The close', w: 4, go: 'dashboard', about: 'The equity close’s checks and what it waits on.',
    body: c => { const p = (c.d.eod.parts || []).find(x => x.id === 'equity'); if (!p) return `<span class="rh-line">${esc(eodStatus(c.d.eod))}</span>`; const ch = p.checks || [], bad = ch.filter(x => !x.ok);
      return p.closed ? `<b class="rh-line good">Equity closed</b>` : `<div class="rh-inline"><b class="rh-num">${ch.length - bad.length}<small> of ${ch.length} checks pass</small></b></div><span class="rh-sub">${bad.slice(0, 2).map(x => esc(x.name)).join(' · ')}${bad.length > 2 ? ` and ${bad.length - 2} more` : ''}</span>`; } },
  pledges: { t: 'Margin pledges', w: 3, go: 'collateral', about: 'Pledge requests waiting to be accepted.', body: c => `<b class="rh-num ${c.d.pending_pledges ? 'warn' : ''}">${c.d.pending_pledges || 0}</b><span class="rh-sub">pledge requests pending</span>` },
};
const RH_NEED_DAY = new Set(Object.keys(RH_TILES).filter(k => !['needs', 'queue', 'autopilot'].includes(k)));

// rhTiles: the tiles to show for the role on screen.
function rhTiles(r, mine) {
  if (r === role() && (mine || []).length) return mine.filter(id => RH_TILES[id]);
  return RH_PRESET[r] || RH_PRESET.Admin;
}
// rhPack places the tiles on twelve columns in order, each in the first
// place it fits (as the grid's dense flow would), then widens tiles into any
// space left on their right, so a row never ends in a hole.
function rhPack(ts) {
  const occ = [], at = (y, x) => occ[y]?.[x];
  const fits = (r, c, w, h) => { if (c + w > 12) return false; for (let y = r; y < r + h; y++) for (let x = c; x < c + w; x++) if (at(y, x)) return false; return true; };
  const take = (r, c, w, h) => { for (let y = r; y < r + h; y++) { occ[y] = occ[y] || Array(12).fill(false); for (let x = c; x < c + w; x++) occ[y][x] = true; } };
  for (const t of ts) { let done = false; for (let r = 0; !done; r++) for (let c = 0; c + t.w <= 12 && !done; c++) if (fits(r, c, t.w, t.h)) { t.r = r; t.c = c; take(r, c, t.w, t.h); done = true; } }
  for (const t of ts) while (t.c + t.w < 12 && fits(t.r, t.c + t.w, 1, t.h)) { take(t.r, t.c + t.w, 1, t.h); t.w++; }
  return ts;
}
// Each tile's place on a wide screen and on a medium one (three tiles of
// four columns, or two of six, a row).
const RH_MED = { 3: 4, 4: 6, 5: 6, 7: 6 };
function rhPlace(list) {
  const wide = rhPack(list.map(t => ({ ...t }))), med = rhPack(list.map(t => ({ ...t, w: RH_MED[t.w] || 6 })));
  return wide.map((t, i) => ({ ...t, place: `--gc:${t.c + 1} / span ${t.w};--gr:${t.r + 1} / span ${t.h};--gcm:${med[i].c + 1} / span ${med[i].w};--grm:${med[i].r + 1} / span ${med[i].h}` }));
}
function rhTile(t, c) {
  if (t.id === 'add') return `<button class="rh-tile rh-add" style="${t.place}" data-act="rhEdit">+ Add or move tiles</button>`;
  const T = RH_TILES[t.id], go = typeof T.go === 'function' ? T.go() : T.go;
  const cls = `rh-tile rh-w${T.w} ${T.h === 2 ? 'rh-h2' : ''} ${T.dark ? 'rh-dark' : ''} ${T.ring ? 'rh-ring' : ''} ${T.warnTile ? 'rh-warn' : ''}`;
  const head = go && (viewAllowed(rhGo(go)) || String(go).startsWith('ly')) ? `<h2 class="rh-lab"><button class="linkish" data-act="go" data-id="${rhGo(go)}">${esc(T.t)}</button></h2>` : `<h2 class="rh-lab">${esc(T.t)}</h2>`;
  return `<section class="${cls}" style="${t.place}" aria-label="${esc(T.t)}">${head}${T.body(c)}</section>`;
}
const rhShown = (id, c) => { const T = RH_TILES[id]; return T && !(RH_NEED_DAY.has(id) && !c.d) && !(T.when && c.d && !T.when(c)); };

LYS.rolehome = {
  side: () => '',
  top: () => `<div class="ly-top rh-top">${lyBrand(S.me?.firm || '')}
    ${role() === 'Admin' ? `<div class="rh-roles" role="group" aria-label="Home for"><span class="dim small">Home for</span><div class="seg">${RH_ROLES.map(([r, l]) => `<button class="${rhRoleNow() === r && S.view === 'lyRole' ? 'on' : ''}" data-act="rhRole" data-id="${esc(r)}" aria-pressed="${rhRoleNow() === r}">${esc(l)}</button>`).join('')}</div></div>` : ''}
    ${lyDateNav()}<span class="ly-push"></span>${lySearch()}<button class="btn" data-act="rhEdit">Edit tiles</button>${lyMenuBtn()}${lyUser(true)}</div>`,
};

V.lyRole = {
  load: async () => { const [, ui] = await Promise.all([lyData(0), get('/v1/me/ui').catch(() => null)]); UI.rhMine = ui?.mine?.tiles || []; return { L: LY }; },
  html: () => {
    const r = rhRoleNow(), d = LY.d, items = rhItems(r);
    const c = { d, ap: LY.ap, steps: lySteps(), items, role: r };
    const h = +lyNow().slice(11, 13), hello = h < 12 ? 'Good morning' : h < 17 ? 'Good afternoon' : 'Good evening';
    const n = items.filter(w => rhCanAct(w, r)).length, words = ['No', 'One', 'Two', 'Three', 'Four', 'Five', 'Six', 'Seven', 'Eight', 'Nine'];
    const ids = rhTiles(r, UI.rhMine), own = r === role();
    return `<div class="rh">
      <div class="rh-hello"><h1>${hello}. ${n ? `${n < 10 ? words[n] : n} thing${n === 1 ? '' : 's'} need${n === 1 ? 's' : ''}` : 'Nothing needs'} ${own ? 'you' : 'the ' + esc(rhRoleName(r).toLowerCase())}.</h1>
        <span class="dim small">${esc(S.me?.firm || '')} · ${fmtDate(S.date)} · ${own ? (UI.rhMine.length ? 'your own tiles' : `tiles chosen for the ${esc(rhRoleName(r).toLowerCase())}`) : `previewing the ${esc(rhRoleName(r).toLowerCase())}’s home`}</span></div>
      ${d ? '' : '<div class="note warn">The firm’s numbers for the day aren’t available to your login, so only your work tiles show.</div>'}
      <div class="rh-grid">${rhPlace([...ids.filter(id => rhShown(id, c)).map(id => ({ id, w: RH_TILES[id].w, h: RH_TILES[id].h || 1 })), ...(own ? [{ id: 'add', w: 3, h: 1 }] : [])]).map(t => rhTile(t, c)).join('')}</div></div>`;
  },
};

// The tile editor: every tile, the chosen ones first in their order.
function rhEditor() {
  const E = UI.rhEdit;
  modal(mhd('Edit your tiles', `Choose the tiles on your home and their order. Nothing chosen means the tiles for the ${esc(rhRoleName(role()).toLowerCase())}.`) + `<div class="bd"><ol class="rh-ed">${E.map((x, i) => `<li class="${x.on ? '' : 'off'}">
      <label class="ck"><input type="checkbox" data-rhon="${i}" ${x.on ? 'checked' : ''}> <b>${esc(RH_TILES[x.id].t)}</b></label><span class="dim small">${esc(RH_TILES[x.id].about)}</span>
      <span class="rh-ord"><button class="btn sm" data-act="rhMove" data-id="${i}" data-d="-1" aria-label="Move ${esc(RH_TILES[x.id].t)} up" ${i === 0 ? 'disabled' : ''}>↑</button><button class="btn sm" data-act="rhMove" data-id="${i}" data-d="1" aria-label="Move ${esc(RH_TILES[x.id].t)} down" ${i === E.length - 1 ? 'disabled' : ''}>↓</button></span></li>`).join('')}</ol></div>
    <div class="ft"><button class="btn" data-act="rhReset">Use my role’s tiles</button><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="rhSave">Save</button></div>`);
  document.querySelectorAll('[data-rhon]').forEach(el => { el.onchange = () => { UI.rhEdit[+el.dataset.rhon].on = el.checked; el.closest('li').classList.toggle('off', !el.checked); }; });
}
async function rhPut(tiles, msg) {
  try { await api('PUT', '/v1/me/ui', { tiles }); UI.rhRole = ''; closeModal(); toast(msg); S.view = 'lyRole'; render({ keep: true }); }
  catch (e) { toast(e.message, true); }
}
Object.assign(A, {
  rhRole: r => { UI.rhRole = r === role() ? '' : r; A.go('lyRole'); },
  rhEdit: async () => {
    if (!UI.rhMine) { try { UI.rhMine = (await get('/v1/me/ui')).mine.tiles || []; } catch (e) { UI.rhMine = []; } }
    const cur = rhTiles(role(), UI.rhMine);
    UI.rhEdit = [...cur.map(id => ({ id, on: true })), ...Object.keys(RH_TILES).filter(id => !cur.includes(id)).map(id => ({ id, on: false }))];
    rhEditor();
  },
  rhMove: (i, el) => { const E = UI.rhEdit, a = +i, b = a + +el.dataset.d; if (b < 0 || b >= E.length) return; [E[a], E[b]] = [E[b], E[a]]; rhEditor(); },
  rhSave: () => { const on = UI.rhEdit.filter(x => x.on).map(x => x.id); if (!on.length) { toast('Choose at least one tile, or use your role’s tiles'); return; } rhPut(on, 'Your tiles are saved'); },
  rhReset: () => rhPut([], 'Your home uses your role’s tiles'),
});
