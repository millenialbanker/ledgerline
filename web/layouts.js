'use strict';
/* Ledgerline console: screen layouts (ops/uilayout.go).
   The classic layout is app.js's own sidebar and top bar. The other layouts
   are the layout directions: A day runbook, B exceptions inbox, C
   workspaces, D control room, E supervised Autopilot, F role home,
   G spreadsheet workbench and H phone approvals. Each replaces the sidebar,
   the top bar and the home screen; every screen stays reachable, and opens
   inside the layout. The building blocks every layout shares are here too:
   the client page, the search and command bar (⌘K), the look, table
   density and colour theme. The server keeps the choices (/v1/me/ui) and
   says which apply (/v1/me → ui). Loaded after app.js and ledgy.js. */

/* ============================== state ============================== */
const LY = { d: null, ap: null, at: 0, date: '', boot: true, k: null };
const LYS = {}; // layout id → { side(), top(), pre(view), key(e) }: each ly-*.js adds its layout
Object.assign(UI, { rbTab: 'today', rbStep: '', lyInSel: '', lyInArea: 'All', lyInMine: '', lyInHelp: false, wsCur: '', rhRole: '', grSrc: 'clients', grView: '', grQ: '', grF: {}, grSort: '', grGroup: '', grCols: null, grSel: new Set(), grPeek: '', phTab: 'approve', phSeg: 'wait', phItem: '', phQ: '', cpUcc: '', lyMenuQ: '' });
const LY_NAMES = { classic: 'Classic', runbook: 'Day runbook', inbox: 'Exceptions inbox', workspaces: 'Workspaces', control: 'Control room', autopilot: 'Supervised Autopilot', rolehome: 'Role home', grid: 'Spreadsheet workbench', phone: 'Phone approvals' };
const LY_HOMEV = { inbox: 'lyInbox', workspaces: 'lyHub', control: 'lyControl', autopilot: 'lyPilot', rolehome: 'lyRole', grid: 'lyGrid', phone: 'lyPhone' };
const LY_OWN_VIEWS = [['lyInbox', 'Inbox'], ['lyHub', 'Workspace'], ['lyControl', 'Control room'], ['lyPilot', 'Supervised Autopilot'], ['lyRole', 'Home'], ['lyGrid', 'Workbench'], ['lyPhone', 'Approvals'], ['lyClient', 'Client page'], ['lyDay', 'Today']];
// Every layout can open these, so they join the screen list (links, the
// command bar and Ledgy's screen names). "Screen layout" is under Admin in
// app.js's NAV.
NAV_VIEWS.push(...LY_OWN_VIEWS);
VIEWS.push(...LY_OWN_VIEWS.map(v => v[0]));
SCOPED_VIEWS.push('layout', 'lyInbox', 'lyGrid', 'lyPhone', 'lyClient');
try { const h = location.hash.slice(1); if (VIEWS.includes(h)) S.view = h; } catch (e) {}

const uiE = () => S.me?.ui || { layout: 'classic', client_opens: 'drawer', command_bar: true, look: 'classic', density: 'comfortable', theme: 'system', choices: [] };
const lyPhoneSize = () => matchMedia('(max-width: 640px)').matches;
// lyCur: the layout drawn now.
function lyCur() {
  if (!S.me || clientUser() || document.body.classList.contains('signed-out')) return 'classic';
  const e = uiE();
  if (e.phone_auto && lyPhoneSize() && LYS.phone?.top) return 'phone';
  // A layout whose file didn't load falls back to the classic layout.
  return LYS[e.layout]?.top ? e.layout : 'classic';
}
// lyApply: the layout, look, density and theme as attributes the CSS keys on.
function lyApply() {
  const l = lyCur(), e = uiE(), root = document.documentElement;
  // A layout may set the header's height (--ly-top) for its rail; another
  // layout starts without it.
  if (document.body.dataset.layout !== l) document.body.style.removeProperty('--ly-top');
  document.body.dataset.layout = l;
  root.dataset.look = S.me ? e.look || 'classic' : 'classic';
  document.body.dataset.density = e.density || 'comfortable';
  // The control room is dark unless the user chose light.
  const th = l === 'control' && e.theme !== 'light' ? 'dark' : e.theme;
  if (th === 'light' || th === 'dark') root.dataset.theme = th; else delete root.dataset.theme;
}
matchMedia('(max-width: 640px)').addEventListener('change', () => { if (S.me && uiE().phone_auto) render(); });

// lyData: the day's dashboard and the Autopilot view, shared by the
// layouts' rails and homes; fetched again when older than maxAge ms.
async function lyData(maxAge = 1500) {
  if (LY.date === S.date && LY.ap && Date.now() - LY.at < maxAge) return LY;
  const [d, ap] = await Promise.all([scopedUser() ? null : get(`/v1/dashboard?date=${S.date}`).catch(() => null), get(`/v1/autopilot?date=${S.date}`).catch(() => null)]);
  Object.assign(LY, { d, ap, at: Date.now(), date: S.date });
  if (d) C.dashboard = d;
  return LY;
}
const lyCounts = d => ({ recon: d.recon.OPEN || 0, rms: d.risk.Breach || 0, collateral: d.pending_pledges || 0, bank: (d.bank['Third-party'] || 0) + (d.bank.Unidentified || 0),
  surv: d.alerts.open || 0, inactive: (+d.extra.inactive_due || 0) + (+d.extra.reactivation_pending || 0), clients: d.onboarding || 0, notes: (d.notes.total || 0) - (d.notes.Sent || 0), reports: d.reports_due || 0, autopilot: +d.extra.work_open || 0 });
const lyHHMM = t => t ? String(t).slice(11, 16) : '';
const lyPlural = (n, w, ws) => `${n} ${n === 1 ? w : (ws || w + 's')}`;
const lyCanAct = w => !w.role || has(w.role);
const lyWorkOpen = () => (LY.ap?.work || []).filter(w => w.status === 'Open');
const lyMine = w => role() === 'Admin' ? true : w.role === role() || !w.role;
const lyArea = w => (LY.ap?.policies || []).find(p => p.id === w.process)?.area || 'Other';
const lyProc = w => (LY.ap?.policies || []).find(p => p.id === w.process)?.name || w.process;
const lyDue = w => w.overdue ? pill('Overdue · ' + (w.due || '').slice(11), 'bad') : w.due ? pill('Due ' + (w.due.slice(0, 10) === S.date ? w.due.slice(11) : fmtDate(w.due.slice(0, 10)) + ' ' + w.due.slice(11)), 'warn') : '';
const lyNow = () => new Date(Date.now() + 330 * 60000).toISOString().slice(0, 16).replace('T', ' '); // IST

/* ============================== the day's steps ============================== */
// The day as steps, for the runbook, the control room's timeline, the
// workspaces' hubs, the role home and the Autopilot plan. A step needs
// someone while its numbers say so or the work queue holds items for it;
// it is done when its numbers say so, or Autopilot ran it today and left
// nothing for people.
const LY_SESS = [['bod', 'Before open', '06:55–09:15'], ['intraday', 'Market hours', '09:15–15:30'], ['eod', 'After close', '15:30–18:45'], ['commodity', 'Commodity close', 'after the MCX session']];
const LY_STEPS = [
  { id: 'files', s: 'bod', at: '07:30', name: 'Clearing and depository files', about: 'The day’s exchange, clearing and depository files: security master, margin parameters, CDSL holdings, NCL positions and bank statements.', views: ['xfiles', 'ccdata', 'files'], pol: ['intake'], area: 'Operations', who: 'Ops maker' },
  { id: 'blocks', s: 'bod', at: '08:15', name: 'Debit blocks and front-office limits', about: 'Clients in debit beyond the firm’s limit are blocked before the open, and limits and holdings go to the front office.', views: ['rms', 'live'], pol: ['debit_blocks'], area: 'Risk', who: 'Risk manager' },
  { id: 'alloc', s: 'bod', at: '08:30', name: 'Collateral and clearing deposits', about: 'Client collateral allocated to the clearing corporations, and deposits and guarantees nearing maturity.', views: ['allocation', 'collateral', 'deposits'], pol: ['deposits'], area: 'Risk', who: 'Risk manager' },
  { id: 'duties', s: 'bod', at: '08:45', name: 'Circulars, calendar and follow-ups', about: 'Circular actions and compliance dates due within seven days, and leads due for a follow-up.', views: ['circulars', 'periodic', 'crm'], pol: ['compliance', 'leads'], area: 'Compliance', who: 'Compliance officer' },
  { id: 'money', s: 'intraday', at: '11:00', name: 'Receipts, payouts and pledges', about: 'Bank credits matched to clients, third-party credits returned, payouts approved and margin pledges accepted.', views: ['money', 'bank', 'collateral'], pol: ['third_party', 'unidentified', 'payouts', 'pledges'], area: 'Money', who: 'Ops checker' },
  { id: 'margin', s: 'intraday', at: '13:30', name: 'Margin calls and square-off', about: 'Clients short of margin are called, and those still short at the cut-off are squared off as the risk policy allows.', views: ['rms', 'live', 'margin', 'mtf'], pol: ['margin_calls', 'square_off', 'mtf_liquidation'], area: 'Risk', who: 'Risk manager' },
  { id: 'recon', s: 'eod', at: '16:00', name: 'Trade import and reconciliation', about: 'The exchange’s trades matched against the front office’s fills. A break closes only when the next import matches.', views: ['recon', 'files'], pol: ['recon'], area: 'Operations', who: 'Ops checker' },
  { id: 'billing', s: 'eod', at: '16:15', name: 'Billing and ledger posting', about: 'Brokerage, STT, exchange charges, GST and stamp duty billed and posted to the ledger.', views: ['billing'], pol: ['billing', 'interest_daily'], area: 'Operations', who: 'Ops checker' },
  { id: 'settle', s: 'eod', at: '16:30', name: 'Settlement and unpaid securities', about: 'Obligations settled; short deliveries, unpledge-and-pay-in lines and buys without funds need someone.', views: ['settlement'], pol: ['settlement'], area: 'Operations', who: 'Ops checker' },
  { id: 'notes', s: 'eod', at: '17:00', name: 'Contract notes', about: 'Signed contract notes to every client within 24 hours; a bounced email needs a printed copy.', views: ['notes', 'dispatch'], pol: ['notes'], area: 'Operations', who: 'Ops maker' },
  { id: 'penalty', s: 'eod', at: '17:15', name: 'Margin penalties', about: 'The day’s short-collection penalties, prepared for approval. Nothing is debited to a client without it.', views: ['margin'], pol: ['penalties'], area: 'Risk', who: 'Risk manager' },
  { id: 'reports', s: 'eod', at: '17:45', name: 'Regulatory reports', about: 'Every daily report generated, validated and uploaded.', views: ['reports', 'exdisc'], pol: ['reports'], area: 'Compliance', who: 'Compliance officer' },
  { id: 'surv', s: 'eod', at: '18:00', name: 'Surveillance, KYC and NRI checks', about: 'Alerts triaged with their evidence, KYC reminders and registries, NRI rule checks and inactive accounts.', views: ['surv', 'kyc', 'nri', 'inactive'], pol: ['surveillance', 'kyc', 'nri', 'inactive'], area: 'Compliance', who: 'Compliance officer' },
  { id: 'payfile', s: 'eod', at: '16:05', name: 'Payout bank file', about: 'Approved payouts put in the bank file at the cut-off, and running accounts settled on their dates.', views: ['money', 'ras'], pol: ['payout_batch', 'ras'], area: 'Money', who: 'Ops checker' },
  { id: 'close', s: 'eod', at: '18:30', name: 'Equity close', about: 'The equity day closed and its positions carried forward once every check passes.', views: ['dashboard'], pol: ['day_close'], area: 'Operations', who: 'Ops checker' },
  { id: 'mcx', s: 'commodity', at: '23:45', name: 'Commodity close', about: 'The MCX session reconciled, billed and settled, its contract notes sent, and the commodity part closed.', views: ['dashboard'], pol: ['commodity_close'], area: 'Operations', who: 'Ops checker' },
];
const SETT_X = ['Short delivery', 'Funds shortfall', 'Unpledge & pay-in'];
const LY_CHECK = {
  files: d => d.trades ? { done: true, note: `${nf(d.trades)} trades, ${nf(d.oms_fills)} OMS fills` } : { note: 'No trades loaded yet' },
  money: d => { const n = (d.bank['Third-party'] || 0) + (d.bank.Unidentified || 0) + (d.pending_pledges || 0); return n ? { needs: n, note: `${lyPlural(n, 'credit or pledge', 'credits or pledges')} to look at` } : d.bank.Matched ? { done: true, note: `${d.bank.Matched} credits matched` } : {}; },
  margin: d => d.risk.Breach ? { needs: d.risk.Breach, note: `${lyPlural(d.risk.Breach, 'client')} in breach · ${cmp(d.extra.breach_amount)}` } : d.risk.WithMargin ? { done: true, note: 'No breaches' } : {},
  recon: d => d.recon.total ? (d.recon.OPEN ? { needs: d.recon.OPEN, note: `${lyPlural(d.recon.OPEN, 'break')} open` } : { done: true, note: `${d.recon.total} found, all cleared` }) : { note: d.trades ? 'Not run yet' : 'No trades loaded' },
  billing: d => d.billed ? { done: true, note: 'Bills posted' } : { note: d.trades ? 'After reconciliation' : 'No trades loaded' },
  settle: d => { const n = SETT_X.reduce((s, k) => s + (d.settlement[k] || 0), 0); return n ? { needs: n, note: `${lyPlural(n, 'exception')} · pay-in ${fmtDate(d.extra.pay_in)}` } : d.billed ? { done: true, note: 'Ready for pay-in' } : {}; },
  notes: d => { const t = d.notes.total || 0, s = d.notes.Sent || 0; return !t ? { note: 'Not generated yet' } : t > s ? { needs: t - s, note: `${s} of ${t} sent${d.notes.Bounced ? ` · ${d.notes.Bounced} bounced` : ''}` } : { done: true, note: `${t} sent` }; },
  // Prepared penalties wait for approval, so a run alone doesn't make the step done.
  penalty: d => num(d.penalty) > 0 ? { done: false, note: `${inr(d.penalty)} estimated · approval on Margin reporting` } : d.trades ? { done: true, note: 'No short collection' } : {},
  reports: d => d.reports_due ? { needs: d.reports_due, note: `${lyPlural(d.reports_due, 'report')} not uploaded` } : {},
  surv: d => d.alerts.open ? { needs: d.alerts.open, note: `${d.alerts.open} open alerts · ${d.alerts.high || 0} high` } : {},
  close: d => { const p = (d.eod.parts || []).find(x => x.id === 'equity'); if (!p) return d.eod.closed ? { done: true, note: 'Closed' } : {}; if (p.closed) return { done: true, note: 'Closed ' + (p.closed_at || '').slice(11, 16) }; const bad = (p.checks || []).filter(c => !c.ok); return { done: false, note: bad.length ? `Waits on ${lyPlural(bad.length, 'check')}` : 'Ready to close' }; },
  mcx: d => { const p = (d.eod.parts || []).find(x => x.id === 'commodity'); if (!p || !p.needed) return { na: true, note: 'No commodity session' }; return p.closed ? { done: true, note: 'Closed' } : { done: false, note: 'After the MCX session' }; },
};
function lySteps(L = LY) {
  const d = L.d, work = (L.ap?.work || []).filter(w => w.status === 'Open'), runs = (L.ap?.runs || []).filter(r => r.date === S.date && r.policy);
  const own = new Map();
  for (const w of work) { const st = LY_STEPS.find(s => s.pol.includes(w.process)) || LY_STEPS.find(s => s.views.includes(String(w.view).split(':')[0])); if (st) own.set(w.id, st.id); }
  return LY_STEPS.map((st, i) => {
    const items = work.filter(w => own.get(w.id) === st.id), rs = runs.filter(r => st.pol.includes(r.policy)), last = rs.map(r => r.at).sort().pop() || '';
    const x = d && LY_CHECK[st.id] ? LY_CHECK[st.id](d) : {};
    let state = 'wait', note = x.note || '';
    if (x.na) state = 'na';
    else if (x.needs || items.length) { state = 'needs'; if (!x.needs) note = `${lyPlural(items.length, 'item')} in the queue`; }
    else if (x.done || (x.done === undefined && rs.length)) { state = 'done'; if (!note) note = last ? 'Done ' + lyHHMM(last) : 'Done'; }
    else if (!note) note = st.s === 'bod' || st.s === 'intraday' ? 'Not run yet' : 'Not yet';
    return { ...st, n: i + 1, state, note, items, last, runs: rs };
  });
}
const LY_ST = { done: ['Done', 'good'], needs: ['Needs someone', 'warn'], wait: ['Not yet', 'nt'], na: ['Not today', 'nt'] };
const lyMark = (st, n) => `<span class="ly-mk ${st.state}" aria-label="${LY_ST[st.state][0]}">${st.state === 'done' ? '<svg viewBox="0 0 12 12" aria-hidden="true"><path d="m3 6.2 2 2 4-4.4"/></svg>' : st.state === 'needs' ? '' : n ?? st.n}</span>`;
const lyStepOf = v => LY_STEPS.find(s => s.id === UI.rbStep && s.views.includes(v)) || LY_STEPS.find(s => s.views[0] === v);

/* ============================== shared pieces ============================== */
const lyIcon = {
  today: '<path d="M10 3a7 7 0 1 0 0 14 7 7 0 0 0 0-14Z"/><path d="M10 6.5V10l2.5 2"/>',
  inbox: '<path d="M3 11 5 4h10l2 7v5H3z"/><path d="M3 11h4l1 2h4l1-2h4"/>',
  clients: '<circle cx="8" cy="7" r="3"/><path d="M2.5 16c.8-3 3-4.5 5.5-4.5s4.7 1.5 5.5 4.5M13 4.5a3 3 0 0 1 0 5.5M15 12c1.4.6 2.2 2 2.6 4"/>',
  money: '<rect x="2.5" y="5" width="15" height="10" rx="2"/><circle cx="10" cy="10" r="2.2"/>',
  risk: '<path d="M10 2.5 16 5v5c0 3.6-2.6 6.4-6 7.5-3.4-1.1-6-3.9-6-7.5V5z"/>',
  dp: '<ellipse cx="10" cy="5" rx="6" ry="2.5"/><path d="M4 5v10c0 1.4 2.7 2.5 6 2.5s6-1.1 6-2.5V5M4 10c0 1.4 2.7 2.5 6 2.5s6-1.1 6-2.5"/>',
  compliance: '<path d="M5 2.5h7l3 3v12H5z"/><path d="m7.5 11 2 2 3.5-4"/>',
  ops: '<path d="M4 4h12v12H4z"/><path d="M4 8h12M8 8v8"/>',
  reports: '<path d="M3 17V9M8 17V4M13 17v-6M18 17H2"/>',
  admin: '<circle cx="10" cy="10" r="2.5"/><path d="M10 2.5v2M10 15.5v2M2.5 10h2M15.5 10h2M4.7 4.7l1.4 1.4M13.9 13.9l1.4 1.4M4.7 15.3l1.4-1.4M13.9 6.1l1.4-1.4"/>',
  menu: '<path d="M3 5h14M3 10h14M3 15h14"/>',
  search: '<circle cx="9" cy="9" r="5.5"/><path d="m13.5 13.5 3.5 3.5"/>',
  ledgy: '<path d="M4 4h12v9H9l-4 3v-3H4z"/>',
  grid: '<rect x="3" y="3" width="14" height="14" rx="1.5"/><path d="M3 8h14M3 12.5h14M8 3v14"/>',
  approve: '<path d="M10 2.5a7.5 7.5 0 1 0 0 15 7.5 7.5 0 0 0 0-15Z"/><path d="m6.8 10.2 2.2 2.2 4.2-4.6"/>',
};
const lySvg = (k, s = 20) => `<svg width="${s}" height="${s}" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${lyIcon[k]}</svg>`;
const lyMarkLogo = () => '<span class="mark" aria-hidden="true"><i></i><i></i><i></i></span>';
const lyBrand = sub => `<button class="ly-brand" data-act="lyHomeGo" title="Home">${lyMarkLogo()}<span><b>Ledgerline</b>${sub ? `<small>${esc(sub)}</small>` : ''}</span></button>`;
const lyDateNav = () => `<div class="ly-date"><button class="btn sm" data-act="prevDay" aria-label="Previous trading day">‹</button><input class="in mono" type="date" id="date" value="${esc(S.date)}" aria-label="Trade date"><button class="btn sm" data-act="nextDay" aria-label="Next trading day">›</button></div>`;
const lyPayIn = () => C.dashboard?.extra?.pay_in ? `<span class="chip">Pay-in <b>${fmtDate(C.dashboard.extra.pay_in)}</b></span>` : '';
function lySearch(ph) {
  if (uiE().command_bar) return `<button class="ly-kbtn" data-act="lyK">${lySvg('search', 16)}<span>${esc(ph || 'Search clients, screens or actions')}</span><kbd>${/Mac|iPhone|iPad/.test(navigator.platform) ? '⌘' : 'Ctrl'} K</kbd></button>`;
  return `<div class="srch"><svg viewBox="0 0 24 24" fill="none" stroke-width="2"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/></svg><input id="gsearch" placeholder="Find client by name, UCC or PAN  ( / )" autocomplete="off" value="${esc(UI.search)}"><div class="res" id="gres" hidden></div></div>`;
}
const lyMenuBtn = () => `<button class="btn ly-menub" data-act="lyMenu" aria-label="All screens">${lySvg('menu', 16)}<span>All screens</span></button>`;
function lyUser(compact) {
  if (!S.me) return '';
  const users = S.me.users || [], name = meUser()?.name || S.me.actor;
  return `<div class="umenu"><button class="ubtn" data-act="umenu" aria-haspopup="menu"><span class="av">${esc(initials(name))}</span>${compact ? '' : `<span><b>${esc(name)}</b><small>${esc(S.me.role)}</small></span><span class="dim">▾</span>`}</button><div class="umenu-pop" id="umenu" hidden role="menu">
    <button data-act="go" data-id="layout" role="menuitem">Screen layout · ${esc(LY_NAMES[lyCur()])}</button>
    <button data-act="pwChange" role="menuitem">Change password</button>
    <button data-act="spMenu" role="menuitem">Contact support</button>
    <button data-act="totp" role="menuitem">Two-factor sign-in${S.me.auth?.totp_enabled ? ' · <span class="pos">On</span>' : ' · Off'}</button>
    ${S.me.dev ? `<div class="sep">Switch user (development)</div>${users.filter(u => u.id !== S.me.actor && u.active).map(u => `<button data-act="switchUser" data-id="${esc(u.id)}" role="menuitem">${esc(u.name)} <span class="dim">· ${esc(u.role)}</span></button>`).join('')}` : ''}
    <div class="sep"></div><button data-act="signOut" role="menuitem">Sign out</button></div></div>`;
}
// The screens this user may open, grouped as in the classic sidebar.
function lyNavGroups() {
  return NAV.map(([g, items]) => [g, items.flatMap(([k, l]) => k[0] === '@' ? groupTabs(k.slice(1)) : viewAllowed(k) ? [[k, l]] : [])]).filter(([, it]) => it.length);
}
const lyCt = v => +S.counts?.[v] || 0;
const lyCtB = v => lyCt(v) ? `<span class="ct">${lyCt(v)}</span>` : '';
function lyNavList(groups, cls) {
  return groups.map(([g, items]) => `<nav class="ly-navg ${cls || ''}" aria-label="${esc(g)}"><h6>${esc(g)}</h6>${items.map(([k, l]) => `<button class="ly-nav ${S.view === k ? 'on' : ''}" data-act="go" data-id="${k}"><span>${esc(l)}</span>${lyCtB(k)}</button>`).join('')}</nav>`).join('');
}

/* ============================== shell hooks ============================== */
// LYS (layout id → { side(), top(), pre(view), key(e) }) is declared with the state.
{
  const baseSide = renderSide, baseTop = renderTop, baseBar = groupBar, baseRender = render, baseCounts = refreshCounts, baseLogin = showLogin;
  renderSide = () => { const l = lyCur(); if (l === 'classic') { baseSide(); return; } $('side').innerHTML = LYS[l].side ? LYS[l].side() : ''; };
  renderTop = () => {
    const l = lyCur();
    if (l === 'classic') {
      baseTop();
      const m = $('umenu'); if (m && !clientUser()) m.insertAdjacentHTML('afterbegin', `<button data-act="go" data-id="layout" role="menuitem">Screen layout · Classic</button>`);
      if (uiE().command_bar && !clientUser() && $('gsearch')) $('gsearch').placeholder = `Find a client ( / ) · ${/Mac|iPhone|iPad/.test(navigator.platform) ? '⌘' : 'Ctrl'} K for everything`;
      return;
    }
    $('top').innerHTML = LYS[l].top();
  };
  groupBar = v => { const l = lyCur(); return (l !== 'classic' && LYS[l].pre ? LYS[l].pre(v) : '') + baseBar(v); };
  render = async (opts = {}) => {
    lyApply();
    const l = lyCur();
    if (S.me && !clientUser() && (S.view === 'lyHome' || (LY.boot && S.view === 'dashboard' && l !== 'classic'))) S.view = await lyHomeView();
    if (S.me) LY.boot = false;
    if (S.view === 'lyClient' && (uiE().client_opens !== 'page' || !UI.cpUcc)) S.view = l === 'classic' ? 'dashboard' : await lyHomeView();
    await baseRender(opts);
    if (S.view === 'lyClient' && UI.cpUcc) clientDrawer(UI.cpUcc);
  };
  refreshCounts = async () => {
    if (lyCur() === 'classic' || clientUser()) return baseCounts();
    try { await lyData(); if (LY.d) S.counts = lyCounts(LY.d); else S.counts = {}; } catch (e) {}
    renderSide(); renderTop();
  };
  showLogin = msg => { baseLogin(msg); lyApply(); };
}
// lyHomeView: the layout's home screen.
async function lyHomeView() {
  const l = lyCur();
  if (l === 'classic') return 'dashboard';
  if (l === 'runbook') {
    try { await lyData(0); } catch (e) {}
    const st = lySteps(), pick = st.find(s => s.state === 'needs' && s.views.some(viewAllowed)) || st.find(s => s.state === 'wait' && s.views.some(viewAllowed)) || st[0];
    UI.rbStep = pick.id; return pick.views.find(viewAllowed) || 'dashboard';
  }
  if (l === 'workspaces' && !UI.wsCur) UI.wsCur = { 'Risk manager': 'Risk', 'Compliance officer': 'Compliance' }[role()] || 'Operations';
  const v = LY_HOMEV[l];
  return viewAllowed(v) || SCOPED_VIEWS.includes(v) ? v : 'dashboard';
}

/* ============================== all screens, area menus ============================== */
function lyMenu(only) {
  UI.lyMenuQ = '';
  const groups = lyNavGroups().filter(([g]) => !only || only.includes(g));
  modal(mhd(only ? esc(only.join(' · ')) : 'All screens', only ? '' : `Every screen you can open. The ${esc(LY_NAMES[lyCur()])} layout keeps the menu short; this has the rest.`) + `<div class="bd">
    ${only ? '' : '<input class="in" id="ly-mq" placeholder="Filter screens" autocomplete="off" style="width:100%;margin-bottom:10px">'}
    <div class="ly-menu" id="ly-menu">${groups.map(([g, items]) => `<div class="ly-mg"><h6>${esc(g)}</h6>${items.map(([k, l]) => `<button class="ly-nav ${S.view === k ? 'on' : ''}" data-act="go" data-id="${k}" data-l="${esc(l.toLowerCase())}"><span>${esc(l)}</span>${lyCtB(k)}</button>`).join('')}</div>`).join('')}</div></div>`);
  const q = $('ly-mq');
  if (q) { setTimeout(() => q.focus(), 20); q.oninput = () => { const t = q.value.trim().toLowerCase(); document.querySelectorAll('#ly-menu .ly-nav').forEach(b => { b.hidden = t && !b.dataset.l.includes(t); }); document.querySelectorAll('#ly-menu .ly-mg').forEach(m => { m.hidden = ![...m.querySelectorAll('.ly-nav')].some(b => !b.hidden); }); }; }
}

/* ============================== ⌘K: search and command bar ============================== */
function lyKRoot() { let r = $('kroot'); if (!r) { r = document.createElement('div'); r.id = 'kroot'; document.body.appendChild(r); } return r; }
async function lyKOpen(q = '') {
  if (!S.me || clientUser()) return;
  LY.k = { q, i: 0, client: '', items: [] };
  lyKRoot().innerHTML = `<div class="kp-ov" data-act="lyKClose"><div class="kp" role="dialog" aria-label="Search and commands" aria-modal="true">
    <label class="kp-in">${lySvg('search', 18)}<input id="kp-q" placeholder="Clients, screens, work items, actions, or a question for Ledgy" autocomplete="off" spellcheck="false" value="${esc(q)}" aria-label="Search clients, screens or actions"><kbd>Esc</kbd></label>
    <div class="kp-res" id="kp-res" role="listbox"></div>
    <div class="kp-ft"><span><kbd>↑</kbd><kbd>↓</kbd> move</span><span><kbd>↵</kbd> open</span><span><kbd>Tab</kbd> actions for the client</span><span class="dim">Finds clients, screens and work items</span></div></div></div>`;
  const inp = $('kp-q'); setTimeout(() => inp.focus(), 10);
  inp.oninput = () => { LY.k.q = inp.value; LY.k.i = 0; LY.k.client = ''; lyKDraw(); };
  try { await clientCache(); } catch (e) {}
  if (!LY.ap) try { await lyData(); } catch (e) {}
  lyKDraw();
}
function lyKItems() {
  const K = LY.k, q = K.q.trim().toLowerCase(), out = [];
  if (K.client) {
    const c = CL.rows.find(x => x.ucc === K.client) || { ucc: K.client, name: '' };
    const a = (l, f) => out.push({ sec: `Do it for ${c.ucc}`, l, s: '', run: f });
    a('Open the client', () => clientDrawer(c.ucc));
    if (viewAllowed('ledger')) a('Open the ledger', () => A.openLedger(c.ucc));
    a('Statements and reports', () => A.crFor(c.ucc));
    if (viewAllowed('money')) a('Record a receipt or payout', () => A.go('money'));
    if (viewAllowed('messages')) a('Send a message', () => A.go('messages'));
    if (lgAllowed()) a(`Ask Ledgy about ${c.ucc}`, () => { LG.open = true; lgAsk(`Tell me about client ${c.ucc}${c.name ? ' (' + c.name + ')' : ''}: why is it in its current state?`); });
    return out;
  }
  if (q) {
    CL.rows.filter(c => (c.ucc + ' ' + c.name + ' ' + c.pan + ' ' + (c.tags || []).join(' ')).toLowerCase().includes(q)).slice(0, 5)
      .forEach(c => out.push({ sec: 'Clients', l: `<span class="mono">${esc(c.ucc)}</span> ${esc(c.name)}`, s: `${esc(c.type || '')}${(c.tags || []).length ? ' · ' + esc(c.tags.join(', ')) : ''}${c.risk_status && c.risk_status !== 'OK' ? ' · ' + esc(c.risk_status) : ''}`, ucc: c.ucc, run: () => clientDrawer(c.ucc), html: true }));
  }
  const seen = new Set();
  [...NAV_VIEWS, ...LY_OWN_VIEWS].filter(([k, l]) => !seen.has(k) && seen.add(k) && (viewAllowed(k) || SCOPED_VIEWS.includes(k)) && !['lyClient', 'lyDay', 'lyHub'].includes(k) && (!q || l.toLowerCase().includes(q) || k.includes(q)))
    .slice(0, q ? 6 : 8).forEach(([k, l]) => out.push({ sec: 'Screens', l: esc(l), s: lyCt(k) ? lyPlural(lyCt(k), 'item') : '', run: () => A.go(k), html: true }));
  lyWorkOpen().filter(w => q && (w.title + ' ' + (w.detail || '') + ' ' + (w.ucc || '')).toLowerCase().includes(q)).slice(0, 4)
    .forEach(w => out.push({ sec: 'Work items', l: esc(w.title), s: esc(w.role || ''), run: () => { UI.lyInSel = w.id; A.go(viewAllowed(String(w.view).split(':')[0]) ? String(w.view) : 'autopilot'); }, html: true }));
  if (q && lgAllowed()) out.push({ sec: 'Ask', l: `Ask Ledgy: ${esc(K.q.trim())}`, s: '', run: () => { LG.open = true; lgAsk(K.q.trim()); }, html: true });
  if (q && /layout|theme|dark|light|compact|density/.test(q)) out.push({ sec: 'Settings', l: 'Screen layout, density and theme', s: '', run: () => A.go('layout'), html: true });
  return out;
}
function lyKDraw() {
  const K = LY.k; if (!K || !$('kp-res')) return;
  K.items = lyKItems(); if (K.i >= K.items.length) K.i = Math.max(0, K.items.length - 1);
  let sec = '';
  $('kp-res').innerHTML = K.items.length ? K.items.map((it, i) => { const h = it.sec !== sec ? `<div class="kp-sec">${esc(it.sec)}</div>` : ''; sec = it.sec; return `${h}<button class="kp-it ${i === K.i ? 'hl' : ''}" role="option" aria-selected="${i === K.i}" data-act="lyKRun" data-id="${i}"><span>${it.html ? it.l : esc(it.l)}</span><span class="dim small">${it.s || ''}</span>${i === K.i ? '<kbd>↵</kbd>' : ''}</button>`; }).join('') : '<div class="kp-empty">Nothing matches. Press Enter to ask Ledgy.</div>';
  $('kp-res').querySelector('.hl')?.scrollIntoView({ block: 'nearest' });
}
function lyKClose() { LY.k = null; const r = $('kroot'); if (r) r.innerHTML = ''; }
function lyKRun(i) { const it = LY.k?.items[i]; lyKClose(); if (it) it.run(); }

/* ============================== client page ============================== */
// lyClientShow: when the firm opens clients as a page, the client's
// details (the side panel's content) fill the page, with a section bar and
// the client's open work items beside them.
function lyClientShow(html, ucc) {
  if (uiE().client_opens !== 'page' || clientUser() || !S.me) return false;
  if (S.view !== 'lyClient') UI.cpBack = S.view;
  closeModal(); lyKClose();
  S.view = 'lyClient'; UI.cpUcc = ucc; persist();
  try { history.replaceState(null, '', '#lyClient'); } catch (e) {}
  renderSide(); renderTop();
  const back = UI.cpBack && UI.cpBack !== 'lyClient' ? UI.cpBack : 'clients';
  const backName = (NAV_VIEWS.find(v => v[0] === back) || [, 'Back'])[1];
  html = html.replace(/<button class="btn pri" data-act="close">Done<\/button>/, `<button class="btn pri" data-act="go" data-id="${back}">Back to ${esc(backName)}</button>`);
  const items = lyWorkOpen().filter(w => w.ucc === ucc || String(w.detail || '').split(/[\s,]+/).includes(ucc));
  $('page').innerHTML = `<div class="cpage"><button class="linkish" data-act="go" data-id="${back}">← ${esc(backName)}</button>
    <div class="cpage-grid"><div class="cpage-main panel">${html}</div>
    <aside class="cpage-side"><div class="panel"><div class="hd"><h3>Open items for this client</h3><small>${items.length}</small></div><div class="bd">${items.length ? items.map(w => `<div class="cp-item"><b>${esc(w.title)}</b><div class="dim small">${esc(w.role || '')} ${lyDue(w)}</div>${w.proposed && lyCanAct(w) ? `<button class="btn sm pri" data-act="wDo" data-id="${esc(w.id)}">${esc(w.proposed.label)}</button>` : ''}<button class="btn sm" data-act="go" data-id="${esc(w.view)}">Open</button></div>`).join('') : '<div class="dim small">Nothing waiting: no work items name this client.</div>'}</div></div>
    <div class="panel"><div class="hd"><h3>Do something</h3></div><div class="bd ly-do">
      <button class="linkish" data-act="crFor" data-id="${esc(ucc)}">Statements and reports</button>
      ${viewAllowed('ledger') ? `<button class="linkish" data-act="openLedger" data-id="${esc(ucc)}">Full ledger</button>` : ''}
      ${viewAllowed('money') ? '<button class="linkish" data-act="go" data-id="money">Receipts and payouts</button>' : ''}
      ${viewAllowed('messages') ? '<button class="linkish" data-act="go" data-id="messages">Send a message</button>' : ''}
      <span class="dim small">Every client code on every screen opens this page: ledger, holdings, money, documents and KYC are sections of it.</span></div></div></aside></div></div>`;
  // The section bar: one entry per heading of the details.
  const hs = [...document.querySelectorAll('.cpage-main .bd h3')];
  if (hs.length > 2) {
    hs.forEach((h, i) => { h.id = 'cps-' + i; h.classList.add('cps-h'); });
    document.querySelector('.cpage-main .hd')?.insertAdjacentHTML('afterend', `<nav class="cpage-nav" aria-label="Client sections"><button data-act="lyCpSec" data-id="top">Overview</button>${hs.map((h, i) => `<button data-act="lyCpSec" data-id="cps-${i}">${esc(h.textContent.replace(/\s*·.*$/, ''))}</button>`).join('')}</nav>`);
  }
  bindPage();
  return true;
}
V.lyClient = { load: async () => ({}), html: () => '<p class="loading">Loading the client…</p>' };
V.lyDay = { load: async () => ({}), html: () => '<p class="loading">Loading…</p>' };

/* ============================== screen layout (settings) ============================== */
// A small drawing of each layout for the choosers.
function lyThumb(id) {
  const r = (x, y, w, h, k = 'b') => `<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="1.5" class="t-${k}"/>`;
  const top = r(0, 0, 120, 9, 'h'), body = {
    classic: r(0, 0, 26, 76, 'd') + r(26, 0, 94, 9, 'h') + r(31, 14, 84, 10) + r(31, 28, 40, 22) + r(75, 28, 40, 22) + r(31, 54, 84, 18),
    runbook: top + r(0, 9, 32, 67, 'p') + [14, 22, 30, 38, 46, 54].map((y, i) => r(4, y, 24, 5, i === 2 ? 'a' : 'l')).join('') + r(37, 14, 78, 12, 'a2') + r(37, 30, 78, 42),
    inbox: r(0, 0, 12, 76, 'd') + r(12, 0, 108, 9, 'h') + r(12, 9, 44, 67, 'p') + [13, 23, 33, 43, 53].map((y, i) => r(15, y, 38, 8, i === 0 ? 'a' : 'l')).join('') + r(61, 14, 54, 10) + r(61, 28, 54, 26),
    workspaces: top + [6, 22, 38, 54, 70].map((x, i) => r(x, 3, 12, 3, i === 1 ? 'a' : 'l')).join('') + r(0, 9, 26, 67, 'p') + r(31, 14, 84, 9) + r(31, 27, 40, 20) + r(75, 27, 40, 20) + r(31, 51, 40, 20) + r(75, 51, 40, 20),
    control: r(0, 0, 120, 76, 'd') + r(4, 4, 112, 14, 'dk') + [4, 23, 42, 61, 80, 99].map(x => r(x, 22, 17, 11, 'dk')).join('') + r(4, 37, 54, 35, 'dk') + r(62, 37, 54, 35, 'dk'),
    autopilot: top + r(0, 9, 28, 67, 'p') + r(32, 14, 56, 30) + r(32, 48, 56, 24, 'a2') + r(92, 14, 28, 58, 'p'),
    rolehome: top + r(4, 13, 46, 26, 'd') + r(53, 13, 36, 26, 'a2') + r(92, 13, 24, 12) + r(92, 28, 24, 11) + r(4, 43, 30, 14) + r(37, 43, 52, 30) + r(92, 43, 24, 14) + r(4, 60, 30, 13) + r(92, 60, 24, 13),
    grid: top + r(0, 9, 22, 67, 'p') + r(26, 12, 64, 6, 'l') + [21, 28, 35, 42, 49, 56, 63, 70].map(y => r(26, y, 64, 5, y === 28 ? 'a2' : 'b')).join('') + r(94, 12, 26, 64, 'p'),
    phone: r(40, 0, 40, 76, 'd') + r(43, 4, 34, 6, 'dk') + r(43, 13, 34, 16, 'a2') + r(43, 32, 34, 16) + r(43, 51, 34, 12) + r(40, 68, 40, 8, 'dk'),
  }[id] || '';
  return `<svg class="ly-thumb" viewBox="0 0 120 76" aria-hidden="true">${body}</svg>`;
}
V.layout = {
  load: () => get('/v1/me/ui'),
  html: d => {
    const e = d.effective, p = d.policy, admin = has('Admin'), defs = Object.fromEntries(d.layouts.map(l => [l.id, l]));
    const lname = id => `${defs[id].code ? defs[id].code + ' · ' : ''}${esc(defs[id].name)}`;
    const src = { you: 'your own choice', role: `the firm’s layout for ${esc(S.me.role)}`, firm: 'the firm’s layout', scoped: 'the layout for your login' }[e.source];
    const card = (id, on, act, extra) => `<button class="ly-card ${on ? 'on' : ''}" data-act="${act}" data-id="${id}" aria-pressed="${on}">${lyThumb(id)}<b>${lname(id)}</b><span>${esc(defs[id].about)}</span>${extra || ''}</button>`;
    const seg = (key, val, opts, act) => `<div class="seg">${opts.map(([v, l]) => `<button class="${val === v ? 'on' : ''}" data-act="${act}" data-k="${key}" data-id="${v}">${l}</button>`).join('')}</div>`;
    const mine = `<div class="panel"><div class="hd"><h3>Your screen</h3><small>Now: ${lname(e.layout)}, ${src}</small></div><div class="bd">
      ${e.note ? `<div class="note warn">${esc(e.note)}</div>` : ''}
      ${e.can_choose ? `<p class="dim small">Pick the layout you work in. Every layout reaches every screen you can open; only the way round changes.${admin ? ' As an Admin you can try any layout, even one the firm doesn’t offer staff.' : ''}</p>
        <div class="ly-cards">${d.mine.layout ? `<button class="ly-card follow" data-act="lyMine" data-k="layout" data-id=""><b>Follow the firm</b><span>Use ${lname(p.roles?.[S.me.role] || p.default)}, and any change the firm makes.</span></button>` : ''}${e.choices.map(id => card(id, d.mine.layout === id, 'lyMinePick')).join('')}</div>`
        : `<p class="dim">Your firm sets the layout: ${lname(e.layout)}. ${admin ? '' : 'An Admin can let staff pick their own.'}</p>`}
      <div class="form ly-prefs"><div class="fld"><label>Tables</label>${seg('density', d.mine.density || '', [['', `Firm’s (${esc(p.density)})`], ['comfortable', 'Comfortable'], ['compact', 'Compact']], 'lyMine')}</div>
      <div class="fld"><label>Colours</label>${seg('theme', d.mine.theme || '', [['', `Firm’s (${esc(p.theme)})`], ['system', 'Follow the computer'], ['light', 'Light'], ['dark', 'Dark']], 'lyMine')}</div></div>
      ${e.phone_auto ? '<p class="dim small">On a phone-sized screen Ledgerline switches to phone approvals.</p>' : ''}</div></div>`;
    if (!admin) return ph('Screen layout', 'How Ledgerline is laid out for you.') + mine;
    UI.lyPol = UI.lyPol && UI.lyPolFor === p.saved ? UI.lyPol : JSON.parse(JSON.stringify(p)); UI.lyPolFor = p.saved;
    const P2 = UI.lyPol, allowed = P2.allowed?.length ? P2.allowed : d.layouts.map(l => l.id);
    const scopedRoles = ['Branch staff', 'Authorised person'];
    return ph('Screen layout', 'Choose how Ledgerline is laid out for the firm: one layout for everyone, a layout per role, and whether staff may pick their own. Every layout reaches every screen; only the way round changes.', '<button class="btn pri" data-act="lySavePol">Save the firm’s layout</button>') +
    `<div class="panel"><div class="hd"><h3>The firm’s layout</h3><small>${p.saved ? `Saved by ${esc(p.by)} · ${esc(p.saved.slice(0, 16).replace('T', ' '))}` : 'Not changed yet: everyone has the classic layout'}</small></div><div class="bd">
      <div class="ly-cards">${d.layouts.map(l => card(l.id, P2.default === l.id, 'lyPolDef', l.scoped ? '' : '<em>Branch staff and APs use Classic</em>')).join('')}</div></div></div>
    <div class="grid g2"><div class="panel"><div class="hd"><h3>A layout per role</h3><small>overrides the firm’s layout</small></div><div class="bd">${tbl([['Role'], ['Layout']], d.roles.map(r => `<tr><td>${esc(r)}</td><td><select class="in sm" data-lyrole="${esc(r)}"><option value="">The firm’s layout</option>${d.layouts.filter(l => !scopedRoles.includes(r) || l.scoped).map(l => `<option value="${l.id}" ${P2.roles?.[r] === l.id ? 'selected' : ''}>${lname(l.id)}</option>`).join('')}</select></td></tr>`))}
      <p class="dim small">Branch staff and authorised persons see only their own clients, so they can use Classic, the Exceptions inbox, the Spreadsheet workbench or Phone approvals.</p></div></div>
    <div class="panel"><div class="hd"><h3>Staff choice</h3></div><div class="bd">
      <label class="ck"><input type="checkbox" id="lyp-choose" ${P2.staff_choose ? 'checked' : ''}> Staff may pick their own layout</label>
      <div class="ly-allowed" ${P2.staff_choose ? '' : 'hidden'} id="lyp-allowed"><small class="dim">From these:</small>${d.layouts.map(l => `<label class="ck"><input type="checkbox" data-lyallow="${l.id}" ${allowed.includes(l.id) ? 'checked' : ''}> ${lname(l.id)}</label>`).join('')}</div>
      <label class="ck"><input type="checkbox" id="lyp-phone" ${P2.phone_auto ? 'checked' : ''}> On a phone-sized screen, switch to Phone approvals</label></div></div></div>
    <div class="panel"><div class="hd"><h3>Building blocks every layout uses</h3></div><div class="bd"><div class="form ly-prefs">
      <div class="fld"><label>A client opens</label>${seg('client_opens', P2.client_opens, [['drawer', 'In the side panel (as today)'], ['page', 'As a page, with sections']], 'lyPolSet')}</div>
      <div class="fld"><label>Search and command bar</label>${seg('command_bar', String(!!P2.command_bar), [['true', `On: ${/Mac|iPhone|iPad/.test(navigator.platform) ? '⌘' : 'Ctrl'} K finds clients, screens, work and actions`], ['false', 'Off: client search only']], 'lyPolSet')}</div>
      <div class="fld"><label>Look</label>${seg('look', P2.look, [['classic', 'Classic: IBM Plex, warm greys'], ['new', 'New: Inter, cool greys, one accent, status shapes']], 'lyPolSet')}</div>
      <div class="fld"><label>Tables, for those who haven’t chosen</label>${seg('density', P2.density, [['comfortable', 'Comfortable'], ['compact', 'Compact']], 'lyPolSet')}</div>
      <div class="fld"><label>Colours, for those who haven’t chosen</label>${seg('theme', P2.theme, [['system', 'Follow the computer'], ['light', 'Light'], ['dark', 'Dark']], 'lyPolSet')}</div></div>
      <p class="dim small">The new look uses Inter where the computer has it installed, otherwise the system font. The control room is always dark unless a person chooses light.</p></div></div>` + mine;
  },
};
async function lyPutMine(patch) {
  try { await api('PUT', '/v1/me/ui', patch); await loadMe(); S.view = 'layout'; toast(patch.layout !== undefined ? `Layout: ${LY_NAMES[lyCur()]}` : 'Saved'); render({ keep: true }); }
  catch (e) { toast(e.message, true); }
}
document.addEventListener('change', e => {
  const el = e.target, P2 = UI.lyPol; if (!P2) return;
  if (el.dataset.lyrole !== undefined) { P2.roles = { ...(P2.roles || {}) }; if (el.value) P2.roles[el.dataset.lyrole] = el.value; else delete P2.roles[el.dataset.lyrole]; }
  if (el.id === 'lyp-choose') { P2.staff_choose = el.checked; const a = $('lyp-allowed'); if (a) a.hidden = !el.checked; }
  if (el.id === 'lyp-phone') P2.phone_auto = el.checked;
  if (el.dataset.lyallow) { const all = [...document.querySelectorAll('[data-lyallow]')]; P2.allowed = all.filter(x => x.checked).map(x => x.dataset.lyallow); }
});

Object.assign(A, {
  lyHomeGo: async () => { S.view = await lyHomeView(); closeModal(); render(); window.scrollTo(0, 0); },
  lyMenu: () => lyMenu(),
  lyArea: g => lyMenu(g.split('|')),
  lyK: () => lyKOpen(),
  lyKClose: (id, el, e) => { if (e && e.target.closest('.kp') && el.classList.contains('kp-ov')) return; lyKClose(); },
  lyKRun: i => lyKRun(+i),
  lyCpSec: id => { const el = id === 'top' ? document.querySelector('.cpage') : $(id); el?.scrollIntoView({ behavior: 'smooth', block: 'start' }); },
  lyMinePick: id => lyPutMine({ layout: id }),
  lyMine: (id, el) => lyPutMine({ [el.dataset.k]: id }),
  lyPolDef: id => { UI.lyPol.default = id; rerenderLayoutCards(); },
  lyPolSet: (id, el) => { UI.lyPol[el.dataset.k] = el.dataset.k === 'command_bar' ? id === 'true' : id; rerenderLayoutCards(); },
  lySavePol: async () => {
    const P2 = UI.lyPol;
    try { await api('PUT', '/v1/ui/policy', { default: P2.default, roles: P2.roles || {}, staff_choose: !!P2.staff_choose, allowed: P2.allowed || [], phone_auto: !!P2.phone_auto, client_opens: P2.client_opens, command_bar: !!P2.command_bar, look: P2.look, density: P2.density, theme: P2.theme });
      UI.lyPol = null; await loadMe(); LY.boot = false; toast('The firm’s layout is saved'); render({ keep: true }); }
    catch (e) { toast(e.message, true); }
  },
});
// Keep the unsaved firm choices while the cards redraw.
function rerenderLayoutCards() { const y = window.scrollY; $('page').innerHTML = groupBar('layout') + V.layout.html(C.layout); bindPage(); window.scrollTo(0, y); }

/* ============================== keyboard ============================== */
document.addEventListener('keydown', e => {
  const typing = /INPUT|TEXTAREA|SELECT/.test(document.activeElement?.tagName || '') || document.activeElement?.isContentEditable;
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k' && S.me && !clientUser() && uiE().command_bar) { e.preventDefault(); if (LY.k) lyKClose(); else lyKOpen(); return; }
  if (LY.k) {
    const K = LY.k;
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); if (K.client) { K.client = ''; K.i = 0; lyKDraw(); } else lyKClose(); return; }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); if (K.items.length) { K.i = (K.i + (e.key === 'ArrowDown' ? 1 : -1) + K.items.length) % K.items.length; lyKDraw(); } return; }
    if (e.key === 'Enter') { e.preventDefault(); if (K.items.length) lyKRun(K.i); else if (K.q.trim() && lgAllowed()) { const q = K.q.trim(); lyKClose(); LG.open = true; lgAsk(q); } return; }
    if (e.key === 'Tab') { e.preventDefault(); const it = K.items[K.i]; if (it?.ucc) { K.client = it.ucc; K.i = 0; lyKDraw(); } else if (K.client) { K.client = ''; lyKDraw(); } return; }
    return;
  }
  if (typing || document.querySelector('.md') || e.metaKey || e.ctrlKey || e.altKey) return;
  const l = lyCur();
  if (l !== 'classic' && LYS[l].key) LYS[l].key(e);
}, true);
