'use strict';
/* Ledgerline console. Every number on screen comes from the server's API;
   this page holds no business data of its own. */

/* ============================== state ============================== */
const LS = 'ledgerline-console-v2';
const S = { tenant: '', date: '2026-10-16', view: 'dashboard', me: null, counts: {} };
try { Object.assign(S, JSON.parse(localStorage.getItem(LS) || '{}')); } catch (e) {}
const persist = () => { try { localStorage.setItem(LS, JSON.stringify({ tenant: S.tenant, date: S.date, view: S.view })); } catch (e) {} };
const UI = { lgTab: 'open', lgSel: new Set(), lgDays: 180, lgQ: '', inTab: 'due', inSel: new Set(), inQ: '', clientTag: '', clientSel: new Set(), bkQ: '', bkPlan: '', cr: { kind: 'ledger', ucc: '', preset: 'fy', from: '', to: '' }, crRes: null, crErr: null, crBusy: false, reconF: 'OPEN', reconX: 'All', reconQ: '', clientStage: 'All', clientQ: '', rmsF: 'All', rmsQ: '', settleTab: 'cm', noteF: 'All',
  bankF: 'All', rasF: 'due', auditQ: '', ledgerU: '', ledgerTab: 'statement', calc: { bucket: 'CM-DEL', exchange: 'NSE', side: 'B', qty: 100, price: '1412' },
  debTab: 'cd', apF: 'Submitted', ciTab: 'circ', ciF: 'Open', ciQ: '', yeFY: '', yeQ: '', insKind: 'All', insSt: 'live', insQ: '', apTab: 'work', apMine: 'mine', nriTab: 'cl', nriFrom: '', nriTo: '', liveAt: '', liveF: 'All', liveQ: '', liveAuto: false, mtfTab: 'pos', dpTab: 'acc', dpQ: '', msgTab: 'log', msgQ: '', pAcct: '', hdTab: 'open', hdMonth: '', chTab: 'pending', crmTab: 'pipe', crmStage: 'All', crmQ: '', crmFrom: '', crmTo: '', kycTab: 'risk', kycQ: '', kycF: 'All', ptTab: 'pt', ptFrom: '', ptTo: '', bkTab: 'vch', finFrom: '', finTo: '', finBranch: '', gstFrom: '', gstTo: '', caTab: 'ca', monTab: 'rcpt', monQ: '', debQ: '', intFrom: '', intTo: '', fileType: 'NSE-CM-TRADE', lastUpload: null, ob: null, search: '' };
const C = {}; // per-view data from the server
let CFG = {};

/* ============================== helpers ============================== */
const $ = id => document.getElementById(id);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const num = s => { const n = parseFloat(s); return isNaN(n) ? 0 : n; };
const nf = (n, d = 0) => (+num(n)).toLocaleString('en-IN', { minimumFractionDigits: d, maximumFractionDigits: d });
const inr = (n, d = 0) => { const x = num(n); return (x < 0 ? '−' : '') + '₹' + nf(Math.abs(x), d); };
const cmp = n => { const x = num(n), a = Math.abs(x), s = x < 0 ? '−' : ''; if (a >= 1e7) return s + '₹' + (a / 1e7).toFixed(2) + ' Cr'; if (a >= 1e5) return s + '₹' + (a / 1e5).toFixed(2) + ' L'; return s + '₹' + nf(a); };
const pct = (n, d = 1) => (num(n) * 100).toFixed(d) + '%';
const sgn = n => num(n) < 0 ? 'neg' : num(n) > 0 ? 'pos' : '';
const pill = (t, k = 'nt') => `<span class="pill ${k}">${esc(t)}</span>`;
const sum = (a, f) => a.reduce((s, x) => s + (f ? f(x) : num(x)), 0);
const MON = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const fmtDate = d => { const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(d || ''); return m ? `${m[3]}-${MON[+m[2] - 1]}-${m[1]}` : (d || ''); };
const instName = k => { const [s, t, e, x] = String(k).split('|'); if (!t || t === 'EQ') return s; const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(e || ''); const ex = m ? m[3] + MON[+m[2] - 1].toUpperCase() + m[1].slice(2) : e; return t === 'FUT' ? `${s} ${ex} FUT` : `${s} ${ex} ${x} ${t}`; };
const LABELS = { MISSING_IN_OMS: 'Missing in OMS', MISSING_IN_EXCHANGE: 'Missing in exchange', TRADE_NO_MISMATCH: 'Trade no. mismatch', UCC_MISMATCH: 'UCC mismatch', SIDE_MISMATCH: 'Side mismatch', QTY_MISMATCH: 'Qty mismatch', PRICE_MISMATCH: 'Price mismatch', INSTRUMENT_MISMATCH: 'Contract mismatch', ALGO_ID_MISSING: 'Algo ID missing' };
const label = t => LABELS[t] || String(t).replace(/_/g, ' ').toLowerCase().replace(/^\w/, c => c.toUpperCase());
function toast(m, ask) {
  const t = $('toast'); clearTimeout(toast.t); t.hidden = false;
  // Errors offer "Ask Ledgy" and stay a little longer.
  if (ask && typeof lgAllowed === 'function' && lgAllowed()) { t.innerHTML = `${esc(m)} <button class="linkish lg-toastask" data-act="ledgyAsk" data-q="${esc(m)}">Ask Ledgy</button>`; toast.t = setTimeout(() => t.hidden = true, 8000); return; }
  t.textContent = m; toast.t = setTimeout(() => t.hidden = true, 3800);
}
function tbl(head, rows, o = {}) { return `<div class="tw ${o.tall ? 'tall' : ''}"><table class="tbl"><thead><tr>${head.map(h => `<th class="${h[1] || ''}">${h[0]}</th>`).join('')}</tr></thead><tbody>${rows.length ? rows.join('') : `<tr><td colspan="${head.length}" class="empty">${o.empty || 'Nothing to show'}</td></tr>`}</tbody>${o.foot || ''}</table></div>`; }
function ph(title, sub, acts) { return `<div class="ph"><div><h1>${title}</h1>${sub ? `<p>${sub}</p>` : ''}</div>${acts ? `<div class="acts">${acts}</div>` : ''}</div>`; }
function kpi(l, v, sub, k) { return `<div class="kpi ${k || ''}"><small>${l}</small><b>${v}</b>${sub ? `<span>${sub}</span>` : ''}</div>`; }
function segBtns(key, opts, part) { return `<div class="seg">${opts.map(o => { const [v, l] = Array.isArray(o) ? o : [o, o]; return `<button class="${UI[key] === v ? 'on' : ''}" data-act="ui" data-k="${key}" data-v="${esc(v)}" ${part ? `data-part="${part}"` : ''}>${l}</button>`; }).join('')}</div>`; }
function bar(u, warn) { const k = u >= 1 ? 'bad' : u >= (warn || .8) ? 'warn' : 'good'; return `<div class="bar ${k}" title="${pct(u)}"><i style="width:${Math.min(100, u * 100).toFixed(1)}%"></i></div>`; }
const role = () => S.me?.role || '';
const has = (...roles) => role() === 'Admin' || roles.includes(role());

/* ============================== API ============================== */
class ApiError extends Error { constructor(status, body) { super(body?.error?.message || ('HTTP ' + status)); this.status = status; this.code = body?.error?.code; this.details = body?.error?.details; } }
async function api(method, path, body, raw) {
  const h = { 'X-Requested-With': 'ledgerline' };
  let b;
  if (raw) b = body; else if (body !== undefined) { b = JSON.stringify(body); h['Content-Type'] = 'application/json'; }
  const res = await fetch(path, { method, headers: h, body: b });
  const ct = res.headers.get('Content-Type') || '';
  let data = null;
  if (ct.includes('json')) { try { data = await res.json(); } catch (e) {} } else data = await res.text();
  if (res.status === 401 && !path.startsWith('/v1/auth/login')) { showLogin(S.me ? 'Your session has ended. Sign in again.' : ''); throw new ApiError(401, data); }
  if (res.status === 403 && data?.error?.code === 'setup_required') { finishSetup(data.error.details || {}); throw new ApiError(403, data); }
  if (!res.ok && res.status !== 207) throw new ApiError(res.status, data);
  return data;
}
const get = p => api('GET', p);
const post = (p, b) => api('POST', p, b || {});
function errHTML(e) {
  let extra = '';
  const errs = e.details?.errors, fields = e.details?.fields;
  if (Array.isArray(errs) && errs.length) extra = `<ul>${errs.slice(0, 25).map(x => `<li>Line ${x.line}${x.field ? ' · ' + esc(x.field) : ''}: ${esc(x.message)}</li>`).join('')}${errs.length > 25 ? `<li>…and ${errs.length - 25} more</li>` : ''}</ul>`;
  if (Array.isArray(fields)) extra = `<ul>${fields.map(f => `<li>${esc(f.message)}</li>`).join('')}</ul>`;
  const ask = typeof lgAllowed === 'function' && lgAllowed() && e.message ? `<button class="linkish small lg-askerr" data-act="ledgyAsk" data-q="${esc(e.message)}">Ask Ledgy about this</button>` : '';
  return `<div class="errbox"><b>${esc(e.message)}</b>${extra}${ask}</div>`;
}
async function download(path, name) {
  try {
    const res = await fetch(path, { headers: { 'X-Requested-With': 'ledgerline' } });
    if (!res.ok) { let m = 'Download failed'; try { m = (await res.json()).error.message; } catch (e) {} toast(m); return; }
    name = (res.headers.get('Content-Disposition') || '').match(/filename="([^"]+)"/)?.[1] || name || 'download';
    const blob = await res.blob(); const url = URL.createObjectURL(blob);
    const a = document.createElement('a'); a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 4000); toast('Downloaded ' + name);
  } catch (e) { toast(e.message, true); }
}
async function openBlob(path) {
  const w = window.open('', '_blank');
  try {
    const res = await fetch(path, { headers: { 'X-Requested-With': 'ledgerline' } });
    if (!res.ok) throw new Error('Could not open the document');
    const url = URL.createObjectURL(await res.blob());
    if (w) w.location = url; else location.href = url;
  } catch (e) { if (w) w.close(); toast(e.message, true); }
}
async function openDoc(path) {
  const w = window.open('', '_blank');
  try {
    const res = await fetch(path, { headers: { 'X-Requested-With': 'ledgerline' } });
    const html = await res.text();
    if (!res.ok) throw new Error('Could not open the document');
    const url = URL.createObjectURL(new Blob([html], { type: 'text/html' }));
    if (w) w.location = url; else location.href = url;
  } catch (e) { if (w) w.close(); toast(e.message, true); }
}

// "Closed 07:00", or each needed part's state while the day is open.
function eodStatus(e) {
  if (!e) return 'open';
  if (e.closed) return 'closed ' + esc(e.closed_at || '');
  const ps = (e.parts || []).filter(p => p.needed);
  if (ps.length < 2) return 'open';
  return ps.map(p => `${esc(p.name)} ${p.closed ? '✓' : 'open'}`).join(' · ');
}

/* ============================== shell ============================== */
// Screens that do one job are grouped: the sidebar shows the group once and
// the screen opens with a tab for each of its parts. Every screen keeps its
// own name (links, help articles and bookmarks to it still work).
const GROUPS = {
  automation: ['Automation', [['autopilot', 'Autopilot & work queue'], ['jobs', 'Scheduled jobs'], ['rpa', 'Browser automations']]],
  calendar: ['Calendar & circulars', [['periodic', 'Periodic work'], ['circulars', 'Circulars & duties']]],
  filesg: ['Files', [['xfiles', 'Exchange files'], ['ccdata', 'Clearing data'], ['files', 'Other uploads & migration']]],
  clientsg: ['Clients & onboarding', [['clients', 'Clients'], ['apps', 'Online applications'], ['changes', 'Change requests'], ['uccapi', 'Exchange UCC']]],
  docsg: ['Client documents', [['statements', 'Client reports'], ['dispatch', 'Dispatch register'], ['messages', 'Client messages'], ['yearend', 'Year-end packs']]],
  riskg: ['RMS & margin', [['rms', 'RMS & limits'], ['live', 'Live (intraday)'], ['margin', 'Margin reporting'], ['payouthold', 'Pay-out holds']]],
  collg: ['Collateral', [['collateral', 'Client collateral & pledge'], ['cdslpledge', 'Pledges (CDSL)'], ['cdslepi', 'Early pay-in'], ['allocation', 'Clearing allocation'], ['deposits', 'Clearing deposits']]],
  moneyg: ['Receipts & payouts', [['money', 'Receipts & payouts'], ['bank', 'Bank reconciliation'], ['ras', 'Running account']]],
  debtg: ['Debtors', [['debits', 'Debtors & interest'], ['legacy', 'Legacy debts']]],
  mfg: ['Mutual funds', [['mforders', 'MF orders'], ['mfsips', 'SIPs'], ['mfmandates', 'MF mandates'], ['mfunits', 'MF units'], ['mfcomm', 'MF commission'], ['mfsettings', 'MF settings']]],
  brokg: ['Brokerage & charges', [['brokerage', 'Brokerage plans'], ['charges', 'Charges calculator'], ['prepaid', 'Prepaid schemes']]],
  partnersg: ['Branches & partners', [['partners', 'Branches & partners'], ['dealers', 'Dealers & terminals']]],
};
const NAV = [
  ['Overview', [['dashboard', 'Dashboard'], ['chat', 'Team messages'], ['@automation'], ['@calendar']]],
  ['Operations', [['@filesg'], ['recon', 'Trade reconciliation'], ['billing', 'Billing'], ['settlement', 'Settlement'], ['notes', 'Contract notes'], ['@clientsg'], ['corp', 'Corporate actions'], ['@docsg']]],
  ['Risk', [['@riskg'], ['@collg'], ['@debtg'], ['mtf', 'Margin trading (MTF)']]],
  ['Accounts', [['ledger', 'Ledger'], ['@moneyg'], ['books', 'Firm accounts & GST'], ['@brokg']]],
  ['Compliance', [['reports', 'Regulatory reports'], ['kyc', 'KYC & PMLA'], ['helpdesk', 'Investor complaints'], ['nri', 'NRI (PIS)'], ['surv', 'Surveillance & AML'], ['inactive', 'Inactive accounts'], ['audit', 'Audit trail']]],
  ['Investments', [['ipo', 'IPO & public issues'], ['@mfg'], ['stocksip', 'Stock SIPs']]],
  ['Depository', [['dp', 'Demat accounts & DIS']]],
  ['Business', [['insights', 'Client insights'], ['crm', 'Leads & CRM'], ['@partnersg']]],
  ['Admin', [['connections', 'Connections'], ['backups', 'Backups'], ['settings', 'Settings']]]];
// Every staff screen with its name, groups opened up.
const NAV_VIEWS = NAV.flatMap(g => g[1].flatMap(([k, l]) => k[0] === '@' ? GROUPS[k.slice(1)][1] : [[k, l]]));
const groupOf = v => Object.keys(GROUPS).find(g => GROUPS[g][1].some(([k]) => k === v));
const PORTAL_NAV = [['My account', [['portal', 'Overview'], ['pnotes', 'Contract notes'], ['pdocs', 'Signed statements'], ['statements', 'Reports & statements'], ['pinsights', 'Tax & insights'], ['pfunds', 'Funds & payouts'], ['pipo', 'IPOs & public issues'], ['pmf', 'Mutual funds'], ['psip', 'Stock SIPs'], ['ppledge', 'Pledge shares'], ['pprofile', 'Profile & documents'], ['phelp', 'Help desk'], ['pext', 'Trades elsewhere']]]];
const PORTAL_VIEWS = PORTAL_NAV[0][1].map(v => v[0]);
const clientUser = () => S.me?.role === 'Client';
const VIEWS = [...NAV_VIEWS.map(v => v[0]), ...PORTAL_VIEWS, 'support'];
try { const h = location.hash.slice(1); if (VIEWS.includes(h)) S.view = h; } catch (e) {}

const SCOPED_VIEWS = ['chat', 'rpa', 'support', 'yearend', 'insights', 'clients', 'statements', 'notes', 'money', 'debits', 'rms', 'partners', 'prepaid', 'changes', 'helpdesk', 'crm', 'ipo'];
const scopedUser = () => ['Branch staff', 'Authorised person'].includes(S.me?.role);
const edition = () => S.me?.edition?.id || 'autopilot';
function editionNote(d) {
  const e = d || S.me?.edition; if (!e) return '';
  const hu = e.id === 'full' && e.human_only?.length ? `<div class="small" style="margin-top:6px">Still done by people, because the rules require it: ${e.human_only.map(esc).join(' · ')}.</div>` : '';
  return `<div class="note ed-note"><b>${esc(e.name)} edition.</b> ${esc(e.about)}${hu}</div>`;
}
// viewAllowed: whether this user sees a screen at all.
function viewAllowed(k) {
  if (scopedUser() && !SCOPED_VIEWS.includes(k)) return false;
  if (edition() === 'manual' && k === 'autopilot') return false;
  if (role() === 'Authorised person' && k === 'rpa') return false; // automations are for the firm's own staff
  return true;
}
// groupTabs: the group's tabs this user may open.
const groupTabs = g => GROUPS[g][1].filter(([k]) => viewAllowed(k));
function renderSide() {
  const ct = S.counts, q = { clients: 1, notes: 1, collateral: 1, reports: 1, autopilot: 1 };
  // A group shows as one entry (its count the sum of its tabs'); with a
  // single tab left for this user it shows as that screen.
  const expand = ([k, l]) => {
    if (k[0] !== '@') return viewAllowed(k) ? [[k, l, [k]]] : [];
    const g = k.slice(1), tabs = groupTabs(g);
    if (!tabs.length) return [];
    if (tabs.length === 1) return [[tabs[0][0], tabs[0][1], [tabs[0][0]]]];
    return [[(UI.lastTab || {})[g] && tabs.some(t => t[0] === UI.lastTab[g]) ? UI.lastTab[g] : tabs[0][0], GROUPS[g][0], tabs.map(t => t[0])]];
  };
  let nav = clientUser() ? PORTAL_NAV.map(([g, items]) => [g, items.map(([k, l]) => [k, l, [k]])]) : NAV.map(([g, items]) => [g, items.flatMap(expand)]).filter(([, items]) => items.length);
  const codes = Object.entries(S.me?.member_codes || {}).map(([k, v]) => k + ' ' + v).join(' · ');
  $('side').innerHTML = `<div class="brand"><div class="mark"><i></i><i></i><i></i></div><div><b>Ledgerline</b><small>${esc(S.me?.firm || 'Back office')}</small>${!clientUser() && S.me?.edition ? `<small class="ed-chip ed-${S.me.edition.id}">${esc(S.me.edition.name)}</small>` : ''}</div></div>` +
    nav.map(([g, items]) => `<nav class="navg" aria-label="${g}"><h6>${g}</h6>${items.map(([k, l, vs]) => { const n = vs.reduce((a, v) => a + (+ct[v] || 0), 0), qq = vs.some(v => q[v] && ct[v]); return `<button class="nav ${vs.includes(S.view) ? 'on' : ''}" data-act="go" data-id="${k}"><span>${l}</span>${n ? `<span class="ct ${qq ? 'q' : ''}">${n}</span>` : ''}</button>`; }).join('')}</nav>`).join('') +
    `<div class="side-foot">${codes ? 'Member codes ' + esc(codes) + '<br>' : ''}${S.me?.dev ? 'Development server. Data is held in memory and is lost when the server stops.' : 'Connected to ' + esc(location.host)}${S.me?.sample_rates ? '<br>Charges use the sample rate schedule.' : ''}</div>`;
}
function renderTop() {
  const d = C.dashboard;
  const users = S.me?.users || [];
  $('top').innerHTML = `<div class="tdate"><small>Trade date</small><div class="dnav"><button class="btn sm" data-act="prevDay" aria-label="Previous trading day">‹</button><input class="in mono" type="date" id="date" value="${esc(S.date)}" aria-label="Trade date" style="height:30px"><button class="btn sm" data-act="nextDay" aria-label="Next trading day">›</button></div></div>
  <div class="chips">${d?.extra?.pay_in ? `<span class="chip">Pay-in <b>${fmtDate(d.extra.pay_in)}</b></span>` : ''}${S.me ? `<span class="chip">EOD <b>${eodStatus(d?.eod)}</b></span>` : ''}</div>
  ${clientUser() ? '<div style="flex:1"></div>' : `<div class="srch"><svg viewBox="0 0 24 24" fill="none" stroke-width="2"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/></svg><input id="gsearch" placeholder="Find client by name, UCC or PAN  ( / )" autocomplete="off" value="${esc(UI.search)}"><div class="res" id="gres" hidden></div></div>`}
  ${S.me ? `<div class="umenu"><button class="ubtn" data-act="umenu" aria-haspopup="menu"><span class="av">${esc(initials(meUser()?.name || S.me.actor))}</span><span><b>${esc(meUser()?.name || S.me.actor)}</b><small>${esc(S.me.role)}</small></span><span class="dim">▾</span></button><div class="umenu-pop" id="umenu" hidden role="menu">
    <button data-act="pwChange" role="menuitem">Change password</button>
    ${clientUser() ? '' : '<button data-act="spMenu" role="menuitem">Contact support</button>'}
    <button data-act="totp" role="menuitem">Two-factor sign-in${S.me.auth?.totp_enabled ? ' · <span class="pos">On</span>' : ' · Off'}</button>
    ${S.me.dev ? `<div class="sep">Switch user (development)</div>${users.filter(u => u.id !== S.me.actor && u.active).map(u => `<button data-act="switchUser" data-id="${esc(u.id)}" role="menuitem">${esc(u.name)} <span class="dim">· ${esc(u.role)}</span></button>`).join('')}` : ''}
    <div class="sep"></div><button data-act="signOut" role="menuitem">Sign out</button></div></div>` : ''}`;
}
async function refreshCounts() {
  if (scopedUser() || clientUser()) { S.counts = {}; C.dashboard = null; renderSide(); renderTop(); return; }
  try {
    const d = await get(`/v1/dashboard?date=${S.date}`);
    C.dashboard = d;
    S.counts = { recon: d.recon.OPEN || 0, rms: d.risk.Breach || 0, collateral: d.pending_pledges || 0, bank: (d.bank['Third-party'] || 0) + (d.bank.Unidentified || 0),
      surv: d.alerts.open || 0, inactive: (+d.extra.inactive_due || 0) + (+d.extra.reactivation_pending || 0), clients: d.onboarding || 0, notes: (d.notes.total || 0) - (d.notes.Sent || 0), reports: d.reports_due || 0, autopilot: +d.extra.work_open || 0 };
  } catch (e) {}
  renderSide(); renderTop();
}
let renderSeq = 0;
async function render(opts = {}) {
  if (clientUser() && !PORTAL_VIEWS.includes(S.view)) S.view = 'portal';
  else if (!clientUser() && PORTAL_VIEWS.includes(S.view) && S.view !== 'statements') S.view = 'dashboard';
  if (scopedUser() && !SCOPED_VIEWS.includes(S.view)) S.view = 'clients';
  persist();
  try { history.replaceState(null, '', '#' + S.view); } catch (e) {}
  renderSide(); renderTop();
  const p = $('page'), view = S.view, seq = ++renderSeq;
  if (!opts.keep) p.innerHTML = '<p class="loading">Loading…</p>';
  try {
    C[view] = await V[view].load();
    if (seq !== renderSeq) return;
    p.innerHTML = groupBar(view) + V[view].html(C[view]);
    bindPage();
    showNotices();
  } catch (e) {
    if (seq !== renderSeq) return;
    if (e.status === 401 || e.code === 'setup_required') return;
    p.innerHTML = ph('Something went wrong', '') + errHTML(e);
  }
  refreshCounts();
}
// groupBar: the tabs above a screen that is part of a group.
function groupBar(view) {
  const g = groupOf(view);
  if (!g || clientUser()) return '';
  UI.lastTab = { ...(UI.lastTab || {}), [g]: view };
  const tabs = groupTabs(g);
  if (tabs.length < 2) return '';
  return `<div class="gtabs" role="tablist" aria-label="${esc(GROUPS[g][0])}">${tabs.map(([k, l]) => `<button role="tab" aria-selected="${k === view}" class="${k === view ? 'on' : ''}" data-act="go" data-id="${k}">${esc(l)}${+S.counts?.[k] ? ` <span class="ct">${S.counts[k]}</span>` : ''}</button>`).join('')}</div>`;
}
function rerenderPart(part) { const el = $(part); if (el && P[part]) el.innerHTML = P[part](C[S.view]); bindPage(); }

/* ============================== sign-in ============================== */
const initials = n => String(n || '?').split(/[\s.]+/).filter(Boolean).slice(0, 2).map(x => x[0].toUpperCase()).join('');
const meUser = () => (S.me?.users || []).find(u => u.id === S.me?.actor);
let loginState = { otp: false, user: '', msg: '' };
function showLogin(msg) {
  document.body.classList.add('signed-out');
  S.me = null; setupOpen = false; closeModal();
  loginState = { otp: false, user: loginState.user || '', msg: msg || '' };
  $('side').innerHTML = ''; $('top').innerHTML = '';
  $('page').innerHTML = loginHTML();
  setTimeout(() => ($('li-user')?.value ? $('li-pw') : $('li-user'))?.focus(), 20);
}
function loginHTML(err) {
  const L = loginState, dev = CFG.dev && CFG.dev_users?.length;
  return `<div class="login"><div class="login-card"><div class="brand big"><div class="mark"><i></i><i></i><i></i></div><div><b>Ledgerline</b><small>Back office</small></div></div>
    <h1>Sign in</h1>${L.msg ? `<p class="dim">${esc(L.msg)}</p>` : ''}
    <form id="loginf" autocomplete="on">
      ${CFG.tenant ? '' : `<div class="fld"><label for="li-firm">Firm code</label><input class="in" id="li-firm" autocomplete="organization" required value="${esc(S.tenant)}"></div>`}
      <div class="fld"><label for="li-user">User ID</label><input class="in" id="li-user" autocomplete="username" autocapitalize="none" spellcheck="false" required value="${esc(L.user)}" ${L.otp ? 'readonly' : ''}></div>
      <div class="fld" ${L.otp ? 'hidden' : ''}><label for="li-pw">Password</label><input class="in" id="li-pw" type="password" autocomplete="current-password" ${L.otp ? '' : 'required'}></div>
      ${L.otp ? `<div class="fld"><label for="li-otp">Code from your authenticator app</label><input class="in mono otp" id="li-otp" inputmode="numeric" autocomplete="one-time-code" maxlength="7" pattern="[0-9 ]{6,7}" required></div>` : ''}
      ${err ? `<div class="errbox"><b>${esc(err)}</b></div>` : ''}
      <button class="btn pri wide" type="submit">${L.otp ? 'Verify' : 'Sign in'}</button>
      ${L.otp ? '<button class="btn wide" type="button" data-act="loginBack">Use a different user</button>' : ''}
    </form>
    ${dev ? `<div class="devlogin"><div class="dim">Development server · every user's password is <span class="mono">${esc(CFG.dev_password)}</span></div><div class="devusers">${CFG.dev_users.map(u => `<button class="btn sm" data-act="devLogin" data-id="${esc(u.id)}">${esc(u.name)}<span class="dim"> · ${esc(u.role)}</span></button>`).join('')}</div></div>` : ''}
    <p class="dim small">Five wrong attempts lock the user for 15 minutes. Sessions end after ${'30'} minutes without activity.</p>
  </div></div>`;
}
let loginPw = '';
async function doLogin(user, pw, otp) {
  const tenant = CFG.tenant || ($('li-firm')?.value || '').trim().toLowerCase();
  try {
    const r = await api('POST', '/v1/auth/login', { tenant, user, password: pw, otp: otp || '' });
    S.tenant = r.tenant; loginState = { otp: false, user, msg: '' }; loginPw = ''; persist();
    await afterLogin(r.auth);
  } catch (e) {
    if (e.code === 'otp_required') { loginState = { otp: true, user, msg: 'Enter the 6-digit code from your authenticator app.' }; loginPw = pw; $('page').innerHTML = loginHTML(); setTimeout(() => $('li-otp')?.focus(), 20); return; }
    $('page').innerHTML = loginHTML(e.message); setTimeout(() => $(loginState.otp ? 'li-otp' : 'li-pw')?.focus(), 20);
  }
}
async function afterLogin(auth) {
  if (auth?.must_change_password) { setupOpen = true; pwModal(true); return; }
  if (auth?.totp_setup_required) { setupOpen = true; totpModal(true); return; }
  await loadMe(); render();
}
let setupOpen = false;
function finishSetup(st) { if (setupOpen) return; setupOpen = true; if (st.must_change_password) pwModal(true); else if (st.totp_setup_required) totpModal(true); }
function pwModal(forced) {
  setTimeout(() => { modalLocked = !!forced; });
  modal(mhd(forced ? 'Choose a new password' : 'Change password', forced ? 'You signed in with a temporary password. Choose your own before continuing.' : 'At least 12 characters. A short sentence works well.').replace(forced ? /<button class="x"[^>]*>×<\/button>/ : /$^/, '') + `<form class="bd" id="pwf">
    <div class="fld"><label for="pw-cur">${forced ? 'Temporary password' : 'Current password'}</label><input class="in" id="pw-cur" type="password" autocomplete="current-password" required></div>
    <div class="fld"><label for="pw-new">New password</label><input class="in" id="pw-new" type="password" autocomplete="new-password" minlength="12" required></div>
    <div class="fld"><label for="pw-new2">Repeat new password</label><input class="in" id="pw-new2" type="password" autocomplete="new-password" minlength="12" required></div>
    <div id="pw-err"></div><div class="ft" style="padding:0;border:0">${forced ? '<button class="btn" type="button" data-act="signOut">Sign out</button>' : '<button class="btn" type="button" data-act="close">Cancel</button>'}<button class="btn pri" type="submit">Save password</button></div></form>`);
  $('pwf').onsubmit = async e => {
    e.preventDefault();
    if ($('pw-new').value !== $('pw-new2').value) { $('pw-err').innerHTML = errHTML({ message: 'The two new passwords differ.' }); return; }
    try { await post('/v1/auth/password', { current: $('pw-cur').value, new: $('pw-new').value }); setupOpen = false; closeModal(); toast('Password changed');
      const me = await get('/v1/me'); if (me.auth?.totp_setup_required) { setupOpen = true; totpModal(true); return; } await loadMe(); render(); }
    catch (err) { if (err.status !== 401) $('pw-err').innerHTML = errHTML(err); }
  };
}
async function totpModal(forced) {
  if (!forced && S.me?.auth?.totp_enabled) {
    modal(mhd('Two-factor sign-in', 'On for your user') + `<div class="bd"><div class="note">Signing in needs your password and a code from your authenticator app. If you lose your phone, ask an administrator to reset two-factor sign-in for you.</div></div><div class="ft"><button class="btn pri" data-act="close">Done</button></div>`);
    return;
  }
  let d; try { d = await post('/v1/auth/2fa/setup'); } catch (e) { toast(e.message, true); return; }
  setTimeout(() => { modalLocked = !!forced; });
  modal(mhd('Set up two-factor sign-in', forced ? 'Your firm requires a code from an authenticator app at every sign-in.' : 'Adds a code from your phone to every sign-in.').replace(forced ? /<button class="x"[^>]*>×<\/button>/ : /$^/, '') + `<form class="bd" id="totpf">
    <ol class="steps-list"><li>Open an authenticator app (Google Authenticator, Microsoft Authenticator, 1Password, Authy).</li>
    <li>Add an account by entering this setup key${matchMedia('(pointer:coarse)').matches ? `, or <a href="${esc(d.uri)}">open it in your app</a>` : ''}:<div class="secret mono">${esc(d.secret)}</div><span class="dim small">Account: ${esc(S.me?.actor || loginState.user)} · type: time-based</span></li>
    <li>Enter the 6-digit code the app shows.</li></ol>
    <div class="fld"><label for="totp-code">Code</label><input class="in mono otp" id="totp-code" inputmode="numeric" autocomplete="one-time-code" maxlength="7" required></div>
    <div id="totp-err"></div><div class="ft" style="padding:0;border:0">${forced ? '<button class="btn" type="button" data-act="signOut">Sign out</button>' : '<button class="btn" type="button" data-act="close">Cancel</button>'}<button class="btn pri" type="submit">Turn on</button></div></form>`);
  $('totpf').onsubmit = async e => {
    e.preventDefault();
    try { await post('/v1/auth/2fa/enable', { code: $('totp-code').value }); setupOpen = false; closeModal(); toast('Two-factor sign-in is on'); await loadMe(); render(); }
    catch (err) { if (err.status !== 401) $('totp-err').innerHTML = errHTML(err); }
  };
}

/* ============================== views ============================== */
const V = {}, P = {};

/* ---------- dashboard ---------- */
// The dashboard view and its widgets are in dash.js.
function emptyDay() {
  const sm = S.sample || {}, first = !(sm.loaded || []).length, isNext = sm.next === S.date;
  const dev = S.me?.dev;
  let devText = '', devBtns = '';
  if (dev && first) {
    devText = ' Or start the sample: about 60 clients and 350 trades a day across NSE, BSE and MCX, with holdings, carried positions, bank statements and exchange alerts, all as real files through the normal loaders. You can load one day and work it yourself, or run a whole week end to end.';
    devBtns = '<button class="btn" data-act="sample">Load sample day</button><button class="btn pri" data-act="sampleRun" data-id="5">Run a sample week</button>';
  } else if (dev && isNext) {
    devText = ` The sample continues from ${fmtDate((sm.loaded || []).slice(-1)[0])}: load this day's market data, trades and bank statement, or let the system work it end to end.`;
    devBtns = '<button class="btn" data-act="sample">Load sample day</button><button class="btn pri" data-act="sampleRun" data-id="1">Run this day end to end</button>';
  } else if (dev && sm.next) {
    devText = ` The next sample day is ${fmtDate(sm.next)}.`;
    devBtns = `<button class="btn pri" data-act="gotoDate" data-id="${sm.next}">Go to ${fmtDate(sm.next)}</button>`;
  }
  return `<div class="panel"><div class="bd"><div class="samplebar"><div><b>No trades loaded for ${fmtDate(S.date)}.</b><div class="dim">Upload the day’s files on the Files page.${devText}</div></div>
  <div class="acts"><button class="btn" data-act="go" data-id="files">Upload files</button>${devBtns}</div></div></div></div>`;
}

/* ---------- files ---------- */
V.files = {
  load: async () => { const [types, audit] = await Promise.all([get('/v1/file-types'), get('/v1/audit?limit=300')]); return { types, files: audit.filter(a => a.action.startsWith('file.')) }; },
  html: d => {
    const t = d.types.find(x => x.name === UI.fileType) || d.types[0];
    const r = UI.lastUpload;
    return ph('Files', 'Every file is stored exactly as received with its SHA-256, checked line by line, then applied. A file with any bad line is rejected whole.', S.me?.dev ? '<button class="btn" data-act="sample">Load sample trading day</button>' : '') +
    `<div class="grid g2"><div class="panel"><div class="hd"><h3>Upload a file</h3><small>for ${fmtDate(S.date)}</small></div><div class="bd">
      <div class="fld"><label for="ftype">File type</label><select class="in" id="ftype">${['trades', 'master'].map(k => `<optgroup label="${k === 'trades' ? 'Trade files' : 'Masters, balances and statements'}">${d.types.filter(x => x.kind === k).map(x => `<option value="${esc(x.name)}" ${x.name === t.name ? 'selected' : ''}>${esc(x.name)} · ${esc(x.purpose)}</option>`).join('')}</optgroup>`).join('')}</select></div>
      <div class="dim" style="font-size:12.5px">${esc(t.when)}${t.needs_date ? ' · applied to the trade date above' : ''}${t.sample_layout ? ' · <span class="pill warn">Sample layout</span>' : ''}${(t.instead || []).length ? `<div class="note" style="margin-top:6px">The exchanges' and depository's own files carry this data: load <b>${t.instead.map(esc).join(', ')}</b> on <a href="#xfiles" data-act="go" data-id="xfiles">Exchange files</a>. This CSV is for data from another back office and for migration.</div>` : ''}${t.columns ? `<div class="mono" style="font-size:11.5px;margin-top:4px;overflow-wrap:anywhere">${esc(t.columns.join(','))}</div>` : ''}</div>
      <label class="drop" id="drop" for="file"><b>Drop a file here</b> or click to choose<br><span class="dim" style="font-size:12px">CSV or text, up to 64 MB</span></label><input type="file" id="file" hidden>
      ${r ? (r.error ? errHTML(r.error) : `<div class="note good" style="background:var(--good-soft)"><b>${esc(r.name)}</b> loaded with ${esc(r.res.adapter)}: ${r.res.added} new row${r.res.added === 1 ? '' : 's'}${r.res.duplicate_rows ? `, ${r.res.duplicate_rows} already loaded` : ''}.${r.res.file.prior_id ? ` Identical to earlier upload ${esc(r.res.file.prior_id)}.` : ''}</div>`) : ''}
    </div></div>
    <div class="panel"><div class="hd"><h3>Inbox: drop several files</h3><small>type worked out from the columns</small></div><div class="bd"><div class="tools"><input type="file" id="inbox-files" multiple class="in sm"><button class="btn pri" data-act="inbox">Load all</button></div><span class="dim small">Masters load first, then trade files, then the rest; files already loaded are skipped. Pledge requests and bank statements are processed by the autopilot straight away.</span>${UI.inboxRes ? tbl([['File'], ['Recognised as'], ['Rows', 'r'], ['Result']], UI.inboxRes.map(f => `<tr><td class="wrap small">${esc(f.name)}</td><td class="mono small">${esc(f.source || '—')}</td><td class="r">${f.rows}</td><td>${pill(f.status, f.status === 'Loaded' ? 'good' : f.status === 'Already loaded' ? 'nt' : 'bad')}${f.message ? `<div class="dim small wrap">${esc(f.message)}</div>` : ''}</td></tr>`)) : ''}</div></div>
    <div class="panel"><div class="hd"><h3>Daily file checklist</h3><small>What a normal day needs</small></div>${tbl([['When'], ['File'], ['Purpose']], [['BOD', 'Security master, VaR, SPAN, closing prices (Exchange files)', 'Prices and risk parameters'], ['BOD', 'CDSL holding file (Exchange files)', 'Client demat balances and pledges'], ['BOD', 'NCL position file (Exchange files)', 'Carried derivatives positions'], ['Intraday', 'PLEDGE-REQUESTS', 'Margin pledges to accept'], ['Intraday / EOD', 'BANK-STMT', 'Client receipts'], ['EOD', 'Exchange trade files', 'NSE CM/FO/CD, BSE CM, MCX'], ['EOD', 'OMS-TRADE', 'For reconciliation'], ['Daily', 'SURV-ALERTS', 'Exchange alerts']].map(x => `<tr><td>${x[0]}</td><td class="n">${x[1]}</td><td class="dim">${x[2]}</td></tr>`))}</div></div>
    <div class="panel"><div class="hd"><h3>Upload history</h3><small>${d.files.length} files</small></div>${tbl([['Time'], ['File'], ['Type'], ['Result'], ['Rows', 'r'], ['By']], d.files.map(f => { const x = f.detail || {}; return `<tr><td class="n dim">${new Date(f.at).toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'short' })}</td><td>${esc(x.name)} <span class="tag">${esc(f.object)}</span></td><td class="n">${esc(x.source)}</td><td>${f.action === 'file.ingested' ? pill('Loaded', 'good') : pill('Rejected', 'bad') + ` <span class="dim">${esc(x.error || '')}</span>`}</td><td class="r">${x.added ?? 0}${x.duplicates ? ` <span class="dim">+${x.duplicates} dup</span>` : ''}</td><td>${esc(f.actor)}</td></tr>`; }), { tall: true, empty: 'No files uploaded yet' })}</div>`;
  }
};
async function upload(file) {
  const src = UI.fileType;
  try {
    const res = await api('POST', `/v1/files?source=${encodeURIComponent(src)}&name=${encodeURIComponent(file.name)}&date=${S.date}`, await file.arrayBuffer(), true);
    UI.lastUpload = { name: file.name, res }; toast(`${file.name}: ${res.added} rows loaded`);
  } catch (e) { UI.lastUpload = { name: file.name, error: e }; toast(`${file.name} was rejected`); }
  render({ keep: true });
}

/* ---------- reconciliation ---------- */
let PB = null, PBN = [];
const PB_FIELD = { UCC_MISMATCH: 'ucc', SIDE_MISMATCH: 'side', INSTRUMENT_MISMATCH: 'instrument', QTY_MISMATCH: 'qty', PRICE_MISMATCH: 'price', ALGO_ID_MISSING: 'algo_id' };
function playbookHTML(b, t, o) {
  const p = (PB || []).find(x => x.type === b.type); if (!p) return '';
  const d = (b.diffs || []).find(x => x.field === PB_FIELD[b.type]) || (b.diffs || [])[0] || {};
  const v = { trade: b.trade_no, ucc: b.ucc, exchange: d.exchange || '', oms: d.oms || '', order: (t && t.order_no) || (o && o.order_no) || '', terminal: (t && t.terminal) || '' };
  const fill = x => x.replace(/\{(\w+)\}/g, (m, k) => (k in v && v[k] !== '') ? v[k] : m);
  const open = b.status === 'OPEN';
  PBN = p.outcomes.map(x => fill(x.note));
  return `<div class="pb"><h4>1 · Check what happened</h4><ol>${p.check.map(x => `<li>${esc(fill(x))}</li>`).join('')}</ol>
    <h4>2 · Then fix it, depending on what you found</h4>${p.outcomes.map((x, i) => `<div class="pbo"><b>If ${esc(x.if.charAt(0).toLowerCase() + x.if.slice(1))}</b><ol>${x.fix.map(f => `<li>${esc(fill(f))}</li>`).join('')}</ol>${open ? `<button class="btn sm" data-act="pbNote" data-id="${i}">Use this note</button>` : ''}</div>`).join('')}
    ${open ? '<h4>3 · Record what you did</h4>' : ''}</div>`;
}
V.recon = {
  load: async () => { const [all, ex, oms, ccm] = await Promise.all([get(`/v1/recon/breaks?date=${S.date}`), get(`/v1/trades?date=${S.date}&origin=EXCHANGE`), get(`/v1/trades?date=${S.date}&origin=OMS`), get(`/v1/ccm?date=${S.date}`)]); if (!PB) { try { PB = await get('/v1/recon/playbooks'); } catch (e) { PB = []; } } return { all, ex, oms, ccm }; },
  html: d => {
    const open = d.all.filter(b => b.status === 'OPEN');
    const types = ['MISSING_IN_OMS', 'MISSING_IN_EXCHANGE', 'TRADE_NO_MISMATCH', 'UCC_MISMATCH', 'SIDE_MISMATCH', 'QTY_MISMATCH', 'PRICE_MISMATCH', 'INSTRUMENT_MISMATCH', 'ALGO_ID_MISSING'];
    return ph('Trade reconciliation', 'Exchange trade files matched against the OMS trade book. The exchange record is the source of truth; every difference becomes a break with a suggested fix.', `<button class="btn" data-act="dl" data-id="/v1/reports/trades/download?date=${S.date}" data-name="trades_${S.date}.csv">Download trade register</button>${has('Ops maker', 'Ops checker') ? `<button class="btn pri" data-act="runRecon">Run reconciliation</button>` : ''}`) +
    `<div class="kpis">${kpi('Exchange trades', nf(d.ex.length), 'all exchanges')}${kpi('OMS fills', nf(d.oms.length), 'trade book')}${kpi('Breaks found', d.all.length, d.all.length ? (d.all.length - open.length) + ' resolved or cleared' : 'run reconciliation', d.all.length ? '' : 'warn')}${kpi('Open breaks', open.length, cmp(sum(open, b => num(b.value))) + ' trade value', open.length ? 'bad' : 'good')}${kpi('Client code changes', d.ccm.length, 'today', d.ccm.length > 2 ? 'warn' : '')}</div>
    <div class="panel"><div class="hd"><h3>Break types</h3><small>Open count</small></div><div class="bd"><div class="tools">${types.map(t => { const n = open.filter(b => b.type === t).length; return n || ['MISSING_IN_OMS', 'QTY_MISMATCH', 'PRICE_MISMATCH', 'UCC_MISMATCH'].includes(t) ? `<span class="pill ${n ? 'bad' : 'nt'}">${label(t)} · ${n}</span>` : ''; }).join('')}</div></div></div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('reconF', [['OPEN', 'Open'], ['RESOLVED', 'Resolved'], ['CLEARED', 'Cleared'], ['', 'All']], 'recon-t')}${segBtns('reconX', ['All', 'NSE', 'BSE', 'MCX'], 'recon-t')}</div><input class="in" data-ui="reconQ" data-part="recon-t" placeholder="Filter by UCC, trade no. or contract" value="${esc(UI.reconQ)}"></div><div id="recon-t">${P['recon-t'](d)}</div></div>`;
  }
};
P['recon-t'] = d => {
  let rows = d.all;
  if (UI.reconF) rows = rows.filter(b => b.status === UI.reconF);
  if (UI.reconX !== 'All') rows = rows.filter(b => b.exchange === UI.reconX);
  const q = UI.reconQ.trim().toLowerCase();
  if (q) rows = rows.filter(b => (b.ucc + ' ' + b.trade_no + ' ' + instName(b.instrument)).toLowerCase().includes(q));
  return tbl([['Break'], ['Trade no.'], ['Exch'], ['UCC'], ['Contract'], ['Value', 'r'], ['Difference'], ['Status']], rows.map(b => `<tr>
    <td>${pill(label(b.type), b.status === 'OPEN' ? 'bad' : 'good')}</td><td class="n">${esc(b.trade_no)}${b.oms_trade_no ? `<div class="dim">OMS ${esc(b.oms_trade_no)}</div>` : ''}</td><td>${esc(b.exchange)}-${esc(b.segment)}</td><td class="n">${esc(b.ucc)}</td><td>${esc(instName(b.instrument))}</td><td class="r">${inr(b.value, 2)}</td>
    <td>${(b.diffs || []).map(x => `${esc(x.field)} <span class="diff">${esc(x.exchange || '—')}</span> vs <span class="diff">${esc(x.oms || '—')}</span>`).join('<br>') || '<span class="dim">—</span>'}</td>
    <td>${b.status === 'OPEN' ? `<button class="btn sm" data-act="brk" data-id="${esc(b.id)}">Resolve</button>` : `<button class="linkish" data-act="brk" data-id="${esc(b.id)}">${label(b.status)}</button>`}</td></tr>`),
    { tall: true, empty: d.all.length ? 'No breaks match this filter' : `Reconciliation has not been run for ${fmtDate(S.date)}.` });
};
function breakModal(id) {
  const b = C.recon.all.find(x => x.id === id);
  const t = C.recon.ex.find(x => x.trade_no === b.trade_no && x.exchange === b.exchange);
  const o = C.recon.oms.find(x => x.trade_no === (b.oms_trade_no || b.trade_no) && x.exchange === b.exchange);
  const f = [['UCC', 'ucc'], ['Contract', 'ins'], ['Side', 'side'], ['Quantity', 'qty'], ['Price', 'price'], ['Time', 'trade_time'], ['Order no.', 'order_no'], ['Algo ID', 'algo_id'], ['Terminal', 'terminal']];
  const val = (r, k) => { if (!r) return null; if (k === 'ins') return instName(`${r.symbol}|${r.instrument_type}|${r.expiry}|${r.strike}`); if (k === 'side') return r.side === 'B' ? 'Buy' : 'Sell'; if (k === 'qty') return nf(r.qty); return r[k]; };
  const cell = (r, k) => { const v = val(r, k); if (v === null) return '<span class="dim">absent</span>'; const diff = t && o && val(t, k) !== val(o, k); return diff ? `<span class="diff">${esc(v || '—')}</span>` : esc(v || '—'); };
  const isOpen = b.status === 'OPEN', canAct = has('Ops maker', 'Ops checker');
  modal(mhd(label(b.type) + ' · ' + esc(b.trade_no), `${esc(b.exchange)}-${esc(b.segment)} · ${esc(b.ucc)} · value ${inr(b.value, 2)}`) + `<div class="bd">
    <div class="cmp"><div class="h">Field</div><div class="h">Exchange file</div><div class="h">OMS</div>${f.map(([l, k]) => `<div class="k">${l}</div><div class="v">${cell(t, k)}</div><div class="v">${cell(o, k)}</div>`).join('')}</div>
    ${playbookHTML(b, t, o) || `<div class="note ${isOpen ? 'warn' : ''}"><b>Suggested fix.</b> ${esc(b.suggestion)}</div>`}
    ${b.status === 'RESOLVED' ? `<div class="kv"><div><small>Resolved by</small><b>${esc(b.resolved_by)}</b></div><div><small>Resolution</small><b>${esc(b.resolution)}</b></div></div>` : ''}
    ${isOpen && canAct ? `<div class="fld"><label for="bnote">What you checked and fixed (recorded in the audit trail)</label><textarea class="in" id="bnote"></textarea></div>` : ''}
    ${isOpen && !canAct ? `<div class="note">Resolving breaks needs an Ops maker or checker. You are signed in as ${esc(role())}.</div>` : ''}
  </div><div class="ft">${isOpen && canAct ? `${b.type === 'UCC_MISMATCH' ? `<button class="btn" data-act="ccm" data-id="${esc(b.id)}">Client code modification → ${esc((b.diffs.find(x => x.field === 'ucc') || {}).oms || '')}</button>` : ''}<button class="btn pri" data-act="resolve" data-id="${esc(b.id)}">Mark resolved</button>` : '<button class="btn" data-act="close">Close</button>'}</div>`);
}

/* ---------- billing ---------- */
V.billing = {
  load: async () => { const [journal, ex, market] = await Promise.all([get(`/v1/journal?date=${S.date}`), get(`/v1/trades?date=${S.date}&origin=EXCHANGE`), get(`/v1/market?date=${S.date}`).catch(() => null)]); C.billData = { journal }; return { journal, ex, market }; },
  html: d => {
    const rev = new Set(d.journal.filter(e => e.reversal_of).map(e => e.reversal_of));
    const bills = d.journal.filter(e => e.source.kind === 'BILL' && !rev.has(e.id));
    const charges = { brok: 0, stt: 0, txn: 0, stamp: 0, gst: 0, sebi: 0 };
    const rows = bills.map(e => { const [, ucc, seg] = e.source.id.split('/'); const p = e.postings; const g = a => sum(p.filter(x => x.account === a || x.account.startsWith(a)), x => num(x.credit)); const tv = sum(p.filter(x => x.memo === 'Trade value / MTM' && x.account.startsWith('CLIENT:')), x => num(x.credit) - num(x.debit)); const ch = sum(p.filter(x => x.memo === 'Brokerage, taxes and charges'), x => num(x.debit));
      const c = { brok: g('INCOME:BROKERAGE'), stt: g('PAYABLE:STT'), txn: g('PAYABLE:EXCH_TXN'), stamp: g('PAYABLE:STAMP'), gst: g('PAYABLE:GST'), sebi: g('PAYABLE:SEBI_FEE') }; for (const k in c) charges[k] += c[k];
      return { e, ucc, seg, tv, ch, c, trades: (e.narration.match(/· (\d+) trade/) || [])[1] || '' }; }).sort((a, b) => a.ucc.localeCompare(b.ucc) || a.seg.localeCompare(b.seg));
    const futs = d.ex.filter(t => t.instrument_type === 'FUT').length, reversed = d.journal.filter(e => e.reversal_of && e.source.kind === 'REVERSAL').length;
    const canBill = has('Ops maker', 'Ops checker'), canRebill = has('Ops checker');
    return ph('Billing', `One bill per client per exchange segment for ${fmtDate(S.date)}, from exchange trades. Futures are marked to market at the settlement price from the market data file. Re-running is safe; changed trades need a rebill, which reverses the old bill and posts a revised one.`,
      `${canBill ? `<button class="btn" data-act="bill" data-id="0" ${d.ex.length ? '' : 'disabled'}>Run billing</button>` : ''}${canRebill ? `<button class="btn pri" data-act="bill" data-id="1" ${d.ex.length ? '' : 'disabled'}>Rebill changed trades</button>` : ''}`) +
    (C.billResult ? billNote(C.billResult) : '') +
    `<div class="kpis">${kpi('Exchange trades', nf(d.ex.length), futs + ' futures trades')}${kpi('Bills in force', bills.length, reversed ? reversed + ' reversed by rebills' : 'client segments', bills.length ? 'good' : 'warn')}${kpi('Brokerage', inr(charges.brok, 2), 'income')}${kpi('STT / CTT', inr(charges.stt, 2), 'payable')}${kpi('Exchange charges', inr(charges.txn, 2), 'payable')}${kpi('GST + stamp', inr(charges.gst + charges.stamp, 2), 'payable')}</div>
    ${d.market ? '' : '<div class="note warn">Market data is not loaded for this date, so futures cannot be marked to market. Upload MARKET-DATA first.</div>'}
    <div class="panel"><div class="hd"><h3>Bills</h3><div class="split"><small>Negative = client owes the broker</small>${canRebill && rows.length ? '<button class="btn sm" data-act="correctBill">Correct a client’s bill</button>' : ''}</div></div>${tbl([['UCC'], ['Segment'], ['Trades', 'r'], ['Trade value / MTM', 'r'], ['Brokerage', 'r'], ['STT', 'r'], ['Exch txn', 'r'], ['Stamp', 'r'], ['GST', 'r'], ['Net to client', 'r'], ['Entry'], ['']],
      rows.map(r => `<tr><td class="n"><button class="linkish" data-act="client" data-id="${r.ucc}">${r.ucc}</button></td><td>${esc(r.seg)}</td><td class="r">${r.trades}</td><td class="r ${sgn(r.tv)}">${nf(r.tv, 2)}</td><td class="r">${nf(r.c.brok, 2)}</td><td class="r">${nf(r.c.stt, 2)}</td><td class="r">${nf(r.c.txn, 2)}</td><td class="r">${nf(r.c.stamp, 2)}</td><td class="r">${nf(r.c.gst, 2)}</td><td class="r ${sgn(r.tv - r.ch)}">${nf(r.tv - r.ch, 2)}</td><td class="n dim" title="${esc(r.e.id)}">${esc(r.e.id.replace(/^JE-0+/, 'JE-'))}${r.e.key.includes('#r') ? ' ' + pill('revised', 'acc') : ''}</td><td>${canRebill ? `<button class="btn sm" data-act="correctBill" data-id="${r.ucc}">Correct</button>` : ''}</td></tr>`),
      { tall: true, empty: 'No bills posted for this date yet.', foot: rows.length ? `<tfoot><tr><td colspan="3">Total</td><td class="r">${nf(sum(rows, r => r.tv), 2)}</td><td class="r">${nf(charges.brok, 2)}</td><td class="r">${nf(charges.stt, 2)}</td><td class="r">${nf(charges.txn, 2)}</td><td class="r">${nf(charges.stamp, 2)}</td><td class="r">${nf(charges.gst, 2)}</td><td class="r">${nf(sum(rows, r => r.tv - r.ch), 2)}</td><td></td><td></td></tr></tfoot>` : '' })}</div>`;
  }
};
function billNote(r) {
  if (r.error) return errHTML(r.error);
  const dv = r.derivatives;
  return `<div class="note ${r.conflicts.length ? 'warn' : ''}"><b>${r.posted.length} bill${r.posted.length === 1 ? '' : 's'} posted, ${r.existing.length} unchanged${r.reversed.length ? `, ${r.reversed.length} reversed` : ''}${r.conflicts.length ? `, ${r.conflicts.length} changed since billing (use Rebill)` : ''}.</b>${dv ? ` Carried futures marked to market: ${dv.mtm.length} new${dv.existing ? `, ${dv.existing} already posted` : ''}${dv.expiry.length ? `; option exercise and assignment: ${dv.expiry.length}` : ''}.` : ''}${r.warnings.length ? `<ul style="margin:6px 0 0;padding-left:18px">${r.warnings.map(w => `<li>${esc(w)}</li>`).join('')}</ul>` : ''}</div>`;
}

async function correctModal(ucc) {
  await clientCache();
  const plans = (await get(`/v1/brokerage?date=${S.date}`).catch(() => ({ plans: [] }))).plans;
  let kind = 'plan';
  const billSummary = u => { const rows = (C.billData?.journal || []).filter(e => e.source.kind === 'BILL' && e.source.id.split('/')[1] === u && !(C.billData.journal.some(x => x.reversal_of === e.id)));
    if (!u) return ''; if (!rows.length) return '<div class="note warn">No bill for this client on this date.</div>';
    const brok = sum(rows, e => sum(e.postings.filter(p => p.account === 'INCOME:BROKERAGE'), p => num(p.credit))), net = sum(rows, e => sum(e.postings.filter(p => p.account.startsWith('CLIENT:')), p => num(p.credit) - num(p.debit)));
    return `<div class="kv"><div><small>Bills now</small><b>${rows.length} segment${rows.length > 1 ? 's' : ''}</b></div><div><small>Brokerage now</small><b class="mono">${inr(brok, 2)}</b></div><div><small>Net to client now</small><b class="mono ${sgn(net)}">${inr(net, 2)}</b></div></div>`; };
  const planInfo = async u => { if (!u) return ''; try { const p = await get(`/v1/brokerage/client/${encodeURIComponent(u)}?date=${S.date}`); return `<span class="dim small">On <b>${esc(p.plan.plan_name)}</b> (${esc(p.plan.source)})</span>`; } catch (e) { return ''; } };
  modal(mhd('Correct a bill · ' + fmtDate(S.date), 'Reverses the client’s bill for this date, posts the corrected one, and issues a revised contract note that replaces the one already sent. Other clients are not touched.') + `<form class="bd" id="cbf">
    <div class="fld"><label for="cb-u-q">Client</label>${cpick('cb-u', ucc || '', { on: async v => { $('cb-sum').innerHTML = billSummary(v); $('cb-plan-now').innerHTML = await planInfo(v); } })}<div id="cb-plan-now">${await planInfo(ucc)}</div></div>
    <div id="cb-sum">${billSummary(ucc)}</div>
    <div class="fld"><label>What is being corrected?</label><div class="seg" id="cb-kind"><button type="button" class="on" data-k="plan">Brokerage rate</button><button type="button" data-k="trades">Trades already corrected</button></div></div>
    <div class="form cb-plan"><div class="fld"><label for="cb-plan">Charge brokerage under</label><select class="in" id="cb-plan">${plans.map(p => `<option value="${esc(p.id)}">${esc(p.name)}</option>`).join('')}</select></div>
      <div class="fld"><label for="cb-scope">For</label><select class="in" id="cb-scope"><option value="day">This trade date only</option><option value="onward">This date onwards</option></select></div>
      <p class="dim small full" style="margin:0">Need a different rate? Create a plan on the Brokerage plans page first (for a waiver, a plan with “Free”).</p></div>
    <div class="note cb-trades" hidden>Use this after a client code modification, a corrected trade file, or a change in settlement prices: the bill is recomputed from the trades as they stand now.</div>
    <div class="fld"><label for="cb-reason">Reason (printed on the revised contract note)</label><input class="in" id="cb-reason" placeholder="e.g. Brokerage charged at the wrong rate; corrected to the agreed plan" required></div>
    <label class="check"><input type="checkbox" id="cb-send" checked> Send the revised contract note to the client now</label>
    <div id="cb-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Rebill and revise note</button></div></form>`);
  if (ucc) setTimeout(() => { $('cb-reason').focus(); cpClose('cb-u'); }, 30);
  $('cb-kind').onclick = e => { const b = e.target.closest('button'); if (!b) return; kind = b.dataset.k; $('cb-kind').querySelectorAll('button').forEach(x => x.classList.toggle('on', x === b)); document.querySelector('.cb-plan').hidden = kind !== 'plan'; document.querySelector('.cb-trades').hidden = kind !== 'trades'; };
  $('cbf').onsubmit = async e => {
    e.preventDefault(); const u = $('cb-u').value;
    if (!u) { $('cb-err').innerHTML = errHTML({ message: 'Choose the client' }); return; }
    $('cbf').querySelector('button[type=submit]').disabled = true;
    try {
      const r = await post('/v1/billing/correct', { date: S.date, ucc: u, reason: $('cb-reason').value, plan: kind === 'plan' ? $('cb-plan').value : '', scope: $('cb-scope').value, send: $('cb-send').checked });
      const g = r.billing.groups || [], brok = sum(g, x => num(x.charges.brokerage)), net = sum(g, x => num(x.net));
      modal(mhd('Bill corrected · ' + esc(u), fmtDate(S.date)) + `<div class="bd"><div class="kv"><div><small>Old bills reversed</small><b>${r.billing.reversed.length}</b></div><div><small>New bills posted</small><b>${r.billing.posted.length}</b></div><div><small>Brokerage now</small><b class="mono">${inr(brok, 2)}</b></div><div><small>Net to client now</small><b class="mono ${sgn(net)}">${inr(net, 2)}</b></div></div>
        ${r.note ? `<div class="note ${r.sent === 'Bounced' ? 'warn' : ''}">Revised contract note <b>${esc(r.note.no)}</b> replaces ${esc(r.note.replaces)}. ${r.sent === 'Sent' ? 'Sent to ' + esc(r.note.email) + '.' : r.sent === 'Bounced' ? 'The email bounced; fix the address and resend from Contract notes.' : 'Not sent yet: send it from Contract notes.'}</div>` : '<div class="note">No contract note had been generated for this client yet; generate notes as usual.</div>'}</div>
        <div class="ft">${r.note ? `<button class="btn" data-act="noteDoc" data-id="${esc(u)}">Open revised note</button>` : ''}<button class="btn pri" data-act="close">Done</button></div>`);
      render({ keep: true });
    } catch (err) { $('cb-err').innerHTML = errHTML(err); $('cbf').querySelector('button[type=submit]').disabled = false; }
  };
}

/* ---------- branches, partners and prepaid ---------- */
V.partners = {
  load: async () => {
    const from = UI.ptFrom || S.date.slice(0, 8) + '01', to = UI.ptTo || S.date, sc = scopedUser();
    const [br, pt, ap, rf] = await Promise.all([sc ? [] : get('/v1/branches'), get(`/v1/partners?date=${S.date}`), sc ? [] : get(`/v1/ap-payouts?from=${from}&to=${to}`), sc ? [] : get(`/v1/referrals?from=${from}&to=${to}`)]);
    return { br, pt, ap, rf, from, to };
  },
  html: d => {
    const ck = has('Ops checker', 'Admin'), sc = scopedUser(), toPost = d.ap.filter(a => a.status === 'Computed' && num(a.share) > 0);
    return ph('Branches & partners', 'Branches, authorised persons (APs) and relationship managers with their reporting lines and the clients mapped to them. An AP earns a share of brokerage on its clients; the firm deducts TDS under section 194H, adds GST if the AP is registered, and holds back the payout to the extent of the AP’s clients’ overdue debits. Clients referred by other clients earn the referrer a share of brokerage for a period.',
      ck && !sc ? '<button class="btn" data-act="newBranch">New branch</button><button class="btn" data-act="newPartner">New partner</button>' : '') +
    (sc ? '' : `<div class="kpis">${kpi('Branches', d.br.length, d.br.filter(b => b.active).length + ' active')}${kpi('Authorised persons', d.pt.filter(p => p.kind === 'Authorised person').length, sum(d.pt.filter(p => p.kind === 'Authorised person'), p => p.clients) + ' clients')}${kpi('AP share this period', inr(sum(d.ap, a => num(a.share)), 2), `${fmtDate(d.from)} – ${fmtDate(d.to)}`)}${kpi('Held back', inr(sum(d.ap, a => num(a.withheld)), 2), 'against clients’ debits', sum(d.ap, a => num(a.withheld)) ? 'warn' : '')}${kpi('Referral rewards', inr(sum(d.rf, r => num(r.reward)), 2), d.rf.length + ' referrers')}</div>`) +
    `<div class="panel"><div class="hd">${segBtns('ptTab', sc ? [['pt', 'Partners']] : [['pt', 'Partners'], ['br', 'Branches'], ['ap', 'AP payouts'], ['rf', 'Referrals']])}${['ap', 'rf'].includes(UI.ptTab) ? `<div class="tools"><input class="in sm" type="date" id="pt-from" value="${esc(d.from)}"><span class="dim">to</span><input class="in sm" type="date" id="pt-to" value="${esc(d.to)}"><button class="btn sm" data-act="ptPeriod">Show</button>${UI.ptTab === 'ap' && has('Ops checker') && toPost.length ? `<button class="btn sm pri" data-act="postAP">Post ${toPost.length} payout${toPost.length > 1 ? 's' : ''}</button>` : ''}${UI.ptTab === 'rf' && has('Ops checker') && d.rf.some(r => r.status === 'To post') ? '<button class="btn sm pri" data-act="postRef">Post rewards</button>' : ''}</div>` : ''}</div>${P['pt-' + (sc ? 'pt' : UI.ptTab)](d)}</div>`;
  }
};
P['pt-pt'] = d => { const by = {}; d.pt.forEach(p => by[p.code] = p); const roots = d.pt.filter(p => !by[p.parent]);
  const row = (p, depth) => [`<tr><td style="padding-left:${12 + depth * 22}px">${depth ? '<span class="dim">└ </span>' : ''}<b>${esc(p.name)}</b> <span class="mono dim">${esc(p.code)}</span></td><td>${esc(p.kind)}</td><td>${esc(p.branch || '—')}</td><td class="r">${num(p.share_pct) ? esc(p.share_pct) + '%' : '—'}</td><td class="r">${p.clients}</td><td class="r">${nf(p.brokerage, 2)}</td><td class="r">${nf(p.team_brokerage, 2)}</td><td class="small dim">${p.gstin ? 'GSTIN ' + esc(p.gstin) : ''}${p.reg_no ? '<br>Reg ' + esc(p.reg_no) : ''}</td><td>${has('Ops checker', 'Admin') && !scopedUser() ? `<button class="btn sm" data-act="editPartner" data-id="${p.code}">Edit</button>` : ''}</td></tr>`, ...p.downline.map(c => by[c]).filter(Boolean).flatMap(c => row(c, depth + 1))];
  return tbl([['Partner'], ['Kind'], ['Branch'], ['Share', 'r'], ['Clients', 'r'], ['Brokerage FY', 'r'], ['With team', 'r'], ['Registration'], ['']], roots.flatMap(p => row(p, 0)), { empty: 'No partners yet.' }); };
const brDepth = (bs, b) => { let n = 0, p = b.parent; while (p && n < 8) { n++; p = (bs.find(x => x.code === p) || {}).parent; } return n; };
const brOrder = bs => { const out = [], add = (p, seen) => bs.filter(b => (b.parent || '') === p).sort((a, b) => a.code.localeCompare(b.code)).forEach(b => { if (!seen.has(b.code)) { seen.add(b.code); out.push(b); add(b.code, seen); } }); const seen = new Set(); add('', seen); bs.forEach(b => { if (!seen.has(b.code)) out.push(b); }); return out; };
P['pt-br'] = d => tbl([['Office'], ['Code'], ['City'], ['Manager'], ['Status'], ['']], brOrder(d.br).map(b => `<tr><td style="padding-left:${12 + 18 * brDepth(d.br, b)}px"><b>${esc(b.name)}</b> <span class="tag">${esc(b.kind || 'Branch')}</span></td><td class="n">${esc(b.code)}</td><td>${esc(b.city)}</td><td>${esc(b.manager || '—')}</td><td>${pill(b.active ? 'Active' : 'Closed', b.active ? 'good' : 'nt')}</td><td>${has('Ops checker', 'Admin') ? `<button class="btn sm" data-act="editBranch" data-id="${esc(b.code)}">Edit</button>` : ''}</td></tr>`), { empty: 'No branches yet.' });
P['pt-ap'] = d => tbl([['Authorised person'], ['Brokerage', 'r'], ['Share', 'r'], ['GST', 'r'], ['TDS', 'r'], ['Held back', 'r'], ['Payable now', 'r'], ['Status'], ['Clients']], d.ap.map(a => `<tr><td><b>${esc(a.name)}</b> <span class="mono dim">${esc(a.partner)}</span><div class="dim small">${esc(a.share_pct)}% share</div></td><td class="r">${nf(a.brokerage, 2)}</td><td class="r">${nf(a.share, 2)}</td><td class="r">${nf(a.gst, 2)}</td><td class="r">${nf(a.tds, 2)}</td><td class="r ${num(a.withheld) ? 'neg' : ''}">${num(a.withheld) ? nf(a.withheld, 2) : '—'}</td><td class="r"><b>${nf(a.payable, 2)}</b></td><td>${pill(a.status, a.status === 'Posted' ? 'good' : 'warn')}</td><td class="small wrap">${a.clients.map(c => `${c.ucc}${num(c.debit) ? ` <span class="neg">Dr ${nf(c.debit)}${c.debit_days ? ' · ' + c.debit_days + 'd' : ''}</span>` : ''}`).join(', ') || '—'}</td></tr>`), { empty: 'No authorised persons, or no brokerage in the period.' }) + '<div class="pad"><span class="dim small">Posting books the share as an expense, input GST where the AP is registered and TDS payable, and credits the AP’s account. Pay the AP the “payable now” amount with a payment voucher; the held-back part is released once its clients clear their debits.</span></div>';
P['pt-rf'] = d => tbl([['Referrer'], ['Referred clients'], ['Their brokerage', 'r'], ['Reward', 'r'], ['Status']], d.rf.map(r => `<tr><td><button class="linkish mono" data-act="client" data-id="${r.referrer}">${r.referrer}</button> ${esc(r.name)}</td><td>${r.referred.join(', ')}</td><td class="r">${nf(r.brokerage, 2)}</td><td class="r"><b>${nf(r.reward, 2)}</b></td><td>${pill(r.status, r.status === 'Posted' ? 'good' : 'warn')}</td></tr>`), { empty: 'No referred clients traded in this period.' }) + '<div class="pad"><span class="dim small">Rewards are credited to the referrer’s trading ledger. Set the percentage and how many months a referral earns in Settings.</span></div>';
V.prepaid = {
  load: () => get('/v1/prepaid'),
  html: d => {
    const act = d.subscriptions.filter(x => x.status === 'Active');
    return ph('Prepaid schemes', 'Brokerage paid in advance. A <b>credit</b> scheme takes an amount up front and adjusts each day’s brokerage against it until it is used or expires (GST on adjusted brokerage is refunded, as GST was paid on the advance; unused credit lapses). A <b>plan</b> scheme charges a fee for a period in which a cheaper brokerage plan applies.',
      `${has('Ops checker', 'Admin') && !scopedUser() ? '<button class="btn" data-act="newScheme">New scheme</button>' : ''}${has('Ops maker', 'Ops checker', 'Branch staff') ? '<button class="btn" data-act="sellScheme">Sell a scheme</button>' : ''}${has('Ops checker') ? `<button class="btn pri" data-act="runPrepaid">Adjust brokerage for ${fmtDate(S.date)}</button>` : ''}`) +
    `<div class="kpis">${kpi('Active subscriptions', act.length, act.filter(x => x.auto_renew).length + ' renew automatically')}${kpi('Prepaid credit unused', inr(sum(act.filter(x => x.kind === 'Credit'), x => num(x.credit) - num(x.used)), 2), 'liability to clients')}${kpi('Collected', inr(sum(d.subscriptions, x => num(x.price)), 2), 'including GST')}</div>
    <div class="panel"><div class="hd"><h3>Schemes</h3></div>${tbl([['Scheme'], ['Kind'], ['Price (incl. GST)', 'r'], ['Validity'], ['Plan'], ['Status']], d.schemes.map(x => `<tr><td><b>${esc(x.name)}</b> <span class="mono dim">${esc(x.id)}</span></td><td>${x.kind}</td><td class="r">${nf(x.price, 2)}</td><td>${x.days} days</td><td>${esc(x.plan || '—')}</td><td>${pill(x.active ? 'On sale' : 'Withdrawn', x.active ? 'good' : 'nt')}</td></tr>`), { empty: 'No schemes yet.' })}</div>
    <div class="panel"><div class="hd"><h3>Subscriptions</h3></div>${tbl([['Subscription'], ['Client'], ['Scheme'], ['Period'], ['Credit / used', 'r'], ['Invoice'], ['Status']], d.subscriptions.map(x => `<tr><td class="n">${x.id}</td><td><button class="linkish mono" data-act="client" data-id="${x.ucc}">${x.ucc}</button> ${esc(x.name)}</td><td>${esc(x.scheme)} <span class="dim">${x.kind}</span></td><td class="n">${fmtDate(x.start)} – ${fmtDate(x.expiry)}${x.auto_renew ? ' <span class="dim">· renews</span>' : ''}</td><td class="r">${x.kind === 'Credit' ? `${nf(x.credit, 2)} / ${nf(x.used, 2)}` : '—'}</td><td class="n dim">${esc(x.invoice || '')}</td><td>${pill(x.status, { Active: 'good', Exhausted: 'warn', Expired: 'nt', Renewed: 'acc' }[x.status] || 'nt')}</td></tr>`), { tall: true, empty: 'No subscriptions yet.' })}</div>`;
  }
};
async function partnerModal(p) {
  const br = C.partners.br, pts = C.partners.pt;
  p = p || { code: '', name: '', kind: 'Authorised person', branch: br[0]?.code || '', parent: '', share_pct: '0', active: true };
  modal(mhd(p.code ? 'Edit ' + esc(p.code) : 'New partner', 'Authorised persons must be registered with each exchange they deal on.') + `<form class="bd" id="ptf"><div class="form">
    <div class="fld"><label for="pt-c">Code</label><input class="in mono" id="pt-c" value="${esc(p.code)}" ${p.code ? 'readonly' : ''} required></div><div class="fld"><label for="pt-n">Name</label><input class="in" id="pt-n" value="${esc(p.name)}" required></div>
    <div class="fld"><label for="pt-k">Kind</label><select class="in" id="pt-k">${['Authorised person', 'Relationship manager'].map(k => `<option ${k === p.kind ? 'selected' : ''}>${k}</option>`).join('')}</select></div>
    <div class="fld"><label for="pt-b">Branch</label><select class="in" id="pt-b">${br.map(b => `<option value="${esc(b.code)}" ${b.code === p.branch ? 'selected' : ''}>${esc(b.name)}</option>`).join('')}</select></div>
    <div class="fld"><label for="pt-p">Reports to</label><select class="in" id="pt-p"><option value="">—</option>${pts.filter(x => x.code !== p.code).map(x => `<option value="${x.code}" ${x.code === p.parent ? 'selected' : ''}>${esc(x.name)} (${x.code})</option>`).join('')}</select></div>
    <div class="fld"><label for="pt-s">Brokerage share (%)</label><input class="in" id="pt-s" value="${esc(p.share_pct)}"></div>
    <div class="fld"><label for="pt-pan">PAN</label><input class="in mono" id="pt-pan" value="${esc(p.pan || '')}"></div><div class="fld"><label for="pt-g">GSTIN (if registered)</label><input class="in mono" id="pt-g" value="${esc(p.gstin || '')}"></div>
    <div class="fld"><label for="pt-r">Exchange registration</label><input class="in mono" id="pt-r" value="${esc(p.reg_no || '')}"></div><div class="fld"><label for="pt-e">Email</label><input class="in" id="pt-e" value="${esc(p.email || '')}"></div>
    <label class="check full"><input type="checkbox" id="pt-a" ${p.active ? 'checked' : ''}> Active</label></div>
    <div id="pt-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
  $('ptf').onsubmit = async e => { e.preventDefault(); try { await post('/v1/partners', { code: $('pt-c').value.trim(), name: $('pt-n').value.trim(), kind: $('pt-k').value, branch: $('pt-b').value, parent: $('pt-p').value, share_pct: $('pt-s').value.trim() || '0', pan: $('pt-pan').value.trim(), gstin: $('pt-g').value.trim(), reg_no: $('pt-r').value.trim(), email: $('pt-e').value.trim(), active: $('pt-a').checked }); closeModal(); toast('Partner saved'); render({ keep: true }); } catch (err) { $('pt-err').innerHTML = errHTML(err); } };
}
async function mapModal(ucc) {
  await clientCache();
  const [c, br, pts] = await Promise.all([get(`/v1/clients/${encodeURIComponent(ucc)}?date=${S.date}`), get('/v1/branches'), get(`/v1/partners?date=${S.date}`)]); const cl = c.client;
  const opt = (list, cur) => `<option value="">—</option>` + list.map(x => `<option value="${esc(x.code)}" ${x.code === cur ? 'selected' : ''}>${esc(x.name)} (${esc(x.code)})</option>`).join('');
  modal(mhd('Branch, partner and referral · ' + esc(ucc), esc(cl.name)) + `<form class="bd" id="mpf"><div class="form">
    <div class="fld"><label for="mp-b">Branch</label><select class="in" id="mp-b">${opt(br, cl.branch)}</select></div>
    <div class="fld"><label for="mp-a">Authorised person</label><select class="in" id="mp-a">${opt(pts.filter(p => p.kind === 'Authorised person'), cl.partner)}</select></div>
    <div class="fld"><label for="mp-r">Relationship manager</label><select class="in" id="mp-r">${opt(pts.filter(p => p.kind === 'Relationship manager'), cl.rm)}</select></div>
    <div class="fld"><label for="mp-f-q">Referred by (client)</label>${cpick('mp-f', cl.referred_by || '', { none: true })}</div>
    <div class="fld"><label for="mp-d">Referral earns from</label><input class="in" type="date" id="mp-d" value="${esc(cl.referred_from || S.date)}"></div></div>
    <div id="mp-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
  $('mpf').onsubmit = async e => { e.preventDefault(); try { await post(`/v1/clients/${encodeURIComponent(ucc)}/mapping`, { branch: $('mp-b').value, partner: $('mp-a').value, rm: $('mp-r').value, referrer: $('mp-f').value, referred_from: $('mp-d').value }); toast('Mapping saved'); clientDrawer(ucc); } catch (err) { $('mp-err').innerHTML = errHTML(err); } };
}

/* ---------- firm accounts and GST ---------- */
V.books = {
  load: async () => {
    const from = UI.finFrom || fyStart(S.date), to = UI.finTo || S.date, gf = UI.gstFrom || S.date.slice(0, 8) + '01', gt = UI.gstTo || S.date;
    const vb = has('Ops maker', 'Ops checker', 'Compliance officer'), none = Promise.resolve(null);
    const [vch, acc, fin, gst, bills, vend, scans] = await Promise.all([get('/v1/vouchers'), get(`/v1/accounts?date=${S.date}`), get(`/v1/financials?from=${from}&to=${to}&branch=${encodeURIComponent(UI.finBranch)}`), get(`/v1/gst?from=${gf}&to=${gt}`),
      vb ? get(`/v1/vendor-bills?from=${UI.vbFrom || ''}&to=${UI.vbTo || ''}`) : none, vb ? get('/v1/vendors') : none, vb ? get('/v1/bill-scans') : none]);
    return { vch, acc, fin, gst, bills, vend: vend?.vendors || [], tds: vend?.tds_sections || [], scans: scans || [] };
  },
  html: d => {
    const pend = d.vch.filter(v => v.status === 'Pending'), mk = has('Ops maker', 'Ops checker'), ck = has('Ops checker');
    return ph('Firm accounts & GST', 'The firm’s own books: payment, receipt, journal and contra vouchers (made by one person, approved by another), the chart of accounts, profit and loss for the firm or a branch, the balance sheet, and GST on everything billed to clients with GSTR-1 tables and the GSTR-3B summary.',
      `${ck ? '<button class="btn" data-act="newAccount">New account</button>' : ''}${mk ? '<button class="btn" data-act="newVendor">New vendor</button><button class="btn" data-act="scanBill">Scan a bill</button><button class="btn" data-act="newBill">New bill</button><button class="btn pri" data-act="newVoucher">New voucher</button>' : ''}`) +
    `<div class="kpis">${kpi('Vouchers to approve', pend.length, inr(sum(pend, v => num(v.total)), 2), pend.length ? 'warn' : 'good')}${kpi('Income', cmp(d.fin.income.total), `${fmtDate(d.fin.from)} – ${fmtDate(d.fin.to)}`)}${kpi('Expenses', cmp(d.fin.expenses.total), d.fin.branch || 'whole firm')}${kpi('Profit', cmp(d.fin.profit), 'before tax', num(d.fin.profit) >= 0 ? 'good' : 'bad')}${kpi('GST output', inr(num(d.gst.outward.igst) + num(d.gst.outward.cgst) + num(d.gst.outward.sgst), 2), `${fmtDate(d.gst.from)} – ${fmtDate(d.gst.to)}`)}${kpi('GST payable after ITC', inr(num(d.gst.payable.igst) + num(d.gst.payable.cgst) + num(d.gst.payable.sgst), 2), 'GSTR-3B')}</div>
    <div class="panel"><div class="hd">${segBtns('bkTab', [['vch', `Vouchers · ${d.vch.length}`], ...(d.bills ? [['bills', `Vendor bills${d.scans.length ? ' · ' + d.scans.length + ' to book' : ''}`], ['vend', `Vendors · ${d.vend.length}`]] : []), ['coa', 'Chart of accounts'], ['pl', 'Profit & loss'], ['bs', 'Balance sheet'], ['gst', 'GST']])}</div>${(P['bk-' + UI.bkTab] && (d.bills || !['bills', 'vend'].includes(UI.bkTab)) ? P['bk-' + UI.bkTab] : P['bk-vch'])(d)}</div>`;
  }
};
P['bk-vch'] = d => { const ck = has('Ops checker'), me = S.me.actor;
  return tbl([['Voucher'], ['Date'], ['Party / narration'], ['Lines'], ['Amount', 'r'], ['Branch'], ['Maker / checker'], ['Status'], ['']], d.vch.map(v => `<tr><td class="n">${esc(v.no)}<div class="dim small">${v.type}${v.ref ? ' · ' + esc(v.ref) : ''}</div></td><td class="n">${fmtDate(v.date)}</td><td class="wrap">${v.party ? `<b>${esc(v.party)}</b><br>` : ''}${esc(v.narration)}</td><td class="small mono">${v.lines.map(l => `${esc(l.account)} ${num(l.debit) ? 'Dr ' + nf(l.debit, 2) : 'Cr ' + nf(l.credit, 2)}`).join('<br>')}</td><td class="r">${nf(v.total, 2)}</td><td>${esc(v.branch || 'Head office')}</td><td class="dim small">${esc(v.maker)}${v.checker ? ' · ' + esc(v.checker) : ''}</td><td>${pill(v.status, { Posted: 'good', Pending: 'warn', Rejected: 'bad', Cancelled: 'nt' }[v.status])}${v.reason ? `<div class="dim small wrap">${esc(v.reason)}</div>` : ''}</td>
    <td>${v.status === 'Pending' && ck && v.maker !== me ? `<div class="split"><button class="btn sm pri" data-act="vchDo" data-id="${v.no.replaceAll('/', '-')}" data-a="approve">Post</button><button class="btn sm bad" data-act="vchDo" data-id="${v.no.replaceAll('/', '-')}" data-a="reject">Reject</button></div>` : v.status === 'Posted' && ck ? `<button class="btn sm" data-act="vchDo" data-id="${v.no.replaceAll('/', '-')}" data-a="cancel">Cancel</button>` : ''}</td></tr>`), { tall: true, empty: 'No vouchers yet.' }); };
P['bk-coa'] = d => { const groups = {}; d.acc.forEach(a => (groups[a.group || 'Other'] ||= []).push(a));
  return tbl([['Account'], ['Code'], ['Type'], ['Balance', 'r'], ['']], Object.keys(groups).flatMap(g => [`<tr class="grp"><td colspan="5"><b>${esc(g)}</b></td></tr>`, ...groups[g].map(a => `<tr><td>${esc(a.name)}${a.restricted ? ' ' + pill('Restricted', 'warn') + `<div class="dim small">visible to ${a.allowed_users.map(esc).join(', ')} and admins</div>` : ''}</td><td class="n">${esc(a.code)}</td><td class="dim">${esc(a.type)}</td><td class="r ${num(a.balance) < 0 ? '' : ''}">${num(a.balance) >= 0 ? nf(a.balance, 2) + ' Dr' : nf(-num(a.balance), 2) + ' Cr'}</td><td>${!a.system && has('Ops checker') ? `<button class="btn sm" data-act="editAccount" data-id="${esc(a.code)}">Edit</button>` : a.code.includes('*') ? '' : `<button class="btn sm" data-act="acctStmt" data-id="${esc(a.code)}">Statement</button>`}</td></tr>`)]), { tall: true }) + `<div class="pad"><span class="dim small">Balances as at ${fmtDate(S.date)}; debit (Dr) or credit (Cr). Client ledgers are summed in one line; see Ledger for each client.</span></div>`; };
const finPeriod = (d, withBranch) => `<div class="pad" style="padding-bottom:0"><div class="tools"><label class="dim small">Period</label><input class="in sm" type="date" id="fin-from" value="${esc(d.fin.from)}"><span class="dim">to</span><input class="in sm" type="date" id="fin-to" value="${esc(d.fin.to)}">${withBranch ? `<select class="in sm" id="fin-br"><option value="">Whole firm</option>${d.fin.branches.map(b => `<option ${b === UI.finBranch ? 'selected' : ''}>${esc(b)}</option>`).join('')}</select>` : ''}<button class="btn sm" data-act="finPeriod">Show</button></div></div>`;
const finSec = s => `<tr class="grp"><td colspan="2"><b>${esc(s.title)}</b></td></tr>${s.lines.map(l => `<tr><td>${esc(l.name)} <span class="dim small mono">${esc(l.code)}</span></td><td class="r">${nf(l.amount, 2)}</td></tr>`).join('')}<tr><td class="dim">Total ${esc(s.title.toLowerCase())}</td><td class="r"><b>${nf(s.total, 2)}</b></td></tr>`;
P['bk-pl'] = d => finPeriod(d, true) + `<div class="tw"><table class="tbl"><tbody>${finSec(d.fin.income)}${finSec(d.fin.expenses)}<tr class="grp"><td><b>${num(d.fin.profit) >= 0 ? 'Profit' : 'Loss'} for the period</b></td><td class="r"><b class="${sgn(d.fin.profit)}">${nf(Math.abs(num(d.fin.profit)), 2)}</b></td></tr></tbody></table></div><div class="pad"><span class="dim small">${d.fin.branch ? `Branch ${esc(d.fin.branch)}: income from its clients’ bills, interest and charges, and vouchers entered for the branch.` : 'Whole firm. Choose a branch to see its own income and expenses.'}</span></div>`;
P['bk-bs'] = d => d.fin.branch ? finPeriod(d, true) + '<div class="pad dim">A branch has no balance sheet of its own; choose Whole firm.</div>' : finPeriod(d, true) + `<div class="grid g2" style="padding:0 14px 14px"><div class="tw"><table class="tbl"><thead><tr><th>Liabilities and capital</th><th class="r">₹</th></tr></thead><tbody>${d.fin.liabilities.map(finSec).join('')}</tbody><tfoot><tr><td>Total</td><td class="r">${nf(d.fin.total_liabilities, 2)}</td></tr></tfoot></table></div><div class="tw"><table class="tbl"><thead><tr><th>Assets</th><th class="r">₹</th></tr></thead><tbody>${d.fin.assets.map(finSec).join('')}</tbody><tfoot><tr><td>Total</td><td class="r">${nf(d.fin.total_assets, 2)}</td></tr></tfoot></table></div></div><div class="pad"><span class="dim small">As at ${fmtDate(d.fin.to)}. ${d.fin.balanced ? pill('Balances', 'good') : pill('Does not balance', 'bad')} Clients in debit are shown as debtors; clients in credit as a liability.</span></div>`;
P['bk-gst'] = d => { const g = d.gst, q = `from=${g.from}&to=${g.to}`, t3 = x => `<td class="r">${nf(x.igst, 2)}</td><td class="r">${nf(x.cgst, 2)}</td><td class="r">${nf(x.sgst, 2)}</td>`;
  return `<div class="pad" style="padding-bottom:0"><div class="tools"><label class="dim small">Return period</label><input class="in sm" type="date" id="gst-from" value="${esc(g.from)}"><span class="dim">to</span><input class="in sm" type="date" id="gst-to" value="${esc(g.to)}"><button class="btn sm" data-act="gstPeriod">Show</button><span class="dim small" style="margin-left:auto">GSTIN ${esc(g.firm_gstin || 'not set')} · ${esc(g.firm_state || 'state not set')}</span></div>${g.warnings.map(w => `<div class="note warn" style="margin-top:10px">${esc(w)}</div>`).join('')}</div>
    <div class="grid g2" style="padding:14px"><div><h4 style="margin:0 0 6px">GSTR-3B</h4><div class="tw"><table class="tbl"><thead><tr><th></th><th class="r">Taxable</th><th class="r">IGST</th><th class="r">CGST</th><th class="r">SGST</th></tr></thead><tbody><tr><td>3.1(a) Outward taxable supplies</td><td class="r">${nf(g.outward.taxable, 2)}</td>${t3(g.outward)}</tr><tr><td>4(A)(5) Input tax credit</td><td></td>${t3(g.itc)}</tr><tr><td><b>Tax payable in cash</b><div class="dim small">after set-off: IGST credit first, then CGST and SGST</div></td><td></td>${t3(g.payable)}</tr><tr><td class="dim">Credit carried forward</td><td></td>${t3(g.carry_forward)}</tr></tbody></table></div></div>
    <div><h4 style="margin:0 0 6px">GSTR-1 tables</h4><div class="tw"><table class="tbl"><tbody><tr><td>4A B2B (clients with a GSTIN)</td><td class="r">${g.b2b.length} invoices</td><td><button class="btn sm" data-act="dl" data-id="/v1/gst/gstr1/b2b?${q}" data-name="gstr1_b2b.csv">CSV</button></td></tr><tr><td>7 B2C small, by state and rate</td><td class="r">${g.b2cs.length} rows</td><td><button class="btn sm" data-act="dl" data-id="/v1/gst/gstr1/b2cs?${q}" data-name="gstr1_b2cs.csv">CSV</button></td></tr><tr><td>12 HSN / SAC summary</td><td class="r">${g.hsn.length} codes</td><td><button class="btn sm" data-act="dl" data-id="/v1/gst/gstr1/hsn?${q}" data-name="gstr1_hsn.csv">CSV</button></td></tr><tr><td>13 Documents issued</td><td class="r">${g.docs.reduce((a, x) => a + x.count, 0)} documents</td><td><button class="btn sm" data-act="dl" data-id="/v1/gst/gstr1/docs?${q}" data-name="gstr1_docs.csv">CSV</button></td></tr></tbody></table></div></div></div>` +
    tbl([['Invoice'], ['Date'], ['Client'], ['Place of supply'], ['Taxable', 'r'], ['IGST', 'r'], ['CGST', 'r'], ['SGST', 'r'], ['']], g.lines.map(l => `<tr><td class="n">${esc(l.invoice)}<div class="dim small">${esc(l.kind)} · SAC ${esc(l.sac)}</div></td><td class="n">${fmtDate(l.date)}</td><td>${esc(l.ucc)} ${esc(l.name)}${l.gstin ? `<div class="dim small mono">${esc(l.gstin)}</div>` : ''}</td><td>${esc(l.pos)}</td><td class="r">${nf(l.taxable, 2)}</td>${t3(l)}<td>${l.kind !== 'Contract note' ? `<button class="btn sm" data-act="invDoc" data-id="${esc(l.invoice.replaceAll('/', '-'))}">Open</button>` : ''}</td></tr>`), { tall: true, empty: 'Nothing billed in this period.' }); };
async function voucherModal() {
  const acc = C.books.acc.filter(a => !a.code.includes('*')).map(a => a.code), dl = `<datalist id="acc-dl">${acc.map(a => `<option value="${esc(a)}">`).join('')}</datalist>`;
  const line = () => `<div class="form vline" style="grid-template-columns:2fr 1fr 1fr;margin-bottom:6px"><input class="in mono" list="acc-dl" placeholder="Account code" data-vl="account"><input class="in" placeholder="Debit" data-vl="debit" inputmode="decimal"><input class="in" placeholder="Credit" data-vl="credit" inputmode="decimal"></div>`;
  modal(mhd('New voucher', 'An Ops checker posts it. Client money is not moved by vouchers; it moves through Receipts & payouts.') + `<form class="bd" id="vf">${dl}<div class="form">
    <div class="fld"><label for="v-t">Type</label><select class="in" id="v-t"><option>Payment</option><option>Receipt</option><option>Journal</option><option>Contra</option></select></div>
    <div class="fld"><label for="v-d">Date</label><input class="in" type="date" id="v-d" value="${esc(S.date)}"></div>
    <div class="fld"><label for="v-b">Branch</label><input class="in" id="v-b" placeholder="Head office"></div><div class="fld"><label for="v-p">Party</label><input class="in" id="v-p"></div>
    <div class="fld"><label for="v-r">Cheque / UTR</label><input class="in mono" id="v-r"></div><div class="fld"><label for="v-n">Narration</label><input class="in" id="v-n" required></div></div>
    <div class="fld"><label>Lines <span class="dim small">(input GST: RECEIVABLE:GST_INPUT:IGST, :CGST or :SGST)</span></label><div id="v-lines">${line()}${line()}</div><button class="btn sm" type="button" id="v-add">Add line</button></div>
    <div id="v-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Send for approval</button></div></form>`);
  $('v-add').onclick = () => $('v-lines').insertAdjacentHTML('beforeend', line());
  $('vf').onsubmit = async e => { e.preventDefault();
    const lines = [...document.querySelectorAll('.vline')].map(r => { const g = k => r.querySelector(`[data-vl=${k}]`).value.trim(); return { account: g('account'), debit: g('debit') || '0', credit: g('credit') || '0' }; }).filter(l => l.account);
    try { const v = await post('/v1/vouchers', { type: $('v-t').value, date: $('v-d').value, branch: $('v-b').value.trim(), party: $('v-p').value.trim(), ref: $('v-r').value.trim(), narration: $('v-n').value.trim(), lines }); closeModal(); toast(`Voucher ${v.no} sent for approval`); UI.bkTab = 'vch'; render({ keep: true }); }
    catch (err) { $('v-err').innerHTML = errHTML(err); } };
}
function accountModal(a) {
  a = a || { code: '', name: '', restricted: false, allowed_users: [] };
  modal(mhd(a.code ? 'Edit ' + esc(a.code) : 'New account', 'The code’s prefix sets its type: EXPENSE:, INCOME:, BANK:, CASH:, ASSET:, LOAN:, PAYABLE:, RECEIVABLE:, AP: or EQUITY:.') + `<form class="bd" id="af"><div class="form"><div class="fld"><label for="a-c">Code</label><input class="in mono" id="a-c" value="${esc(a.code)}" ${a.code ? 'readonly' : ''} placeholder="EXPENSE:RENT" required></div><div class="fld"><label for="a-n">Name</label><input class="in" id="a-n" value="${esc(a.name)}" required></div>
    <label class="check full"><input type="checkbox" id="a-r" ${a.restricted ? 'checked' : ''}> Restricted: only these users (and admins) can see or post to it</label><div class="fld full"><label for="a-u">Users (comma separated)</label><input class="in mono" id="a-u" value="${esc(a.allowed_users.join(', '))}"></div></div>
    <div id="a-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
  $('af').onsubmit = async e => { e.preventDefault(); try { await post('/v1/accounts', { code: $('a-c').value.trim(), name: $('a-n').value.trim(), restricted: $('a-r').checked, allowed_users: $('a-u').value.split(',').map(x => x.trim()).filter(Boolean) }); closeModal(); toast('Account saved'); render({ keep: true }); } catch (err) { $('a-err').innerHTML = errHTML(err); } };
}

/* ---------- vendors, vendor bills and bill scanning ---------- */
const BILL_ST = { 'Awaiting approval': 'warn', Unpaid: 'acc', 'Part paid': 'acc', Paid: 'good', Rejected: 'bad', Cancelled: 'nt' };
const tail4 = a => a ? '…' + String(a).slice(-4) : '—';
P['bk-bills'] = d => { const r = d.bills, mk = has('Ops maker', 'Ops checker'), today = S.date;
  const scans = d.scans.length ? `<div class="pad" style="padding-bottom:0"><h4 style="margin:0 0 6px">Scanned, not yet booked</h4>${tbl([['Scan'], ['File'], ['Read'], ['Vendor'], ['Total', 'r'], ['']], d.scans.map(x => `<tr><td class="n">${esc(x.id)}<div class="dim small">${fmtDate(x.at.slice(0, 10))} · ${esc(x.by)}</div></td><td class="wrap">${esc(x.name)}${x.duplicate ? `<div>${pill('Uploaded before: ' + x.duplicate, 'warn')}</div>` : ''}</td><td>${pill({ text: 'Read from PDF', ai: 'Read by AI', none: 'Not read' }[x.method], x.method === 'none' ? 'nt' : 'good')}</td><td>${esc(x.fields.vendor_name || '—')}${x.fields.vendor_gstin ? `<div class="dim small mono">${esc(x.fields.vendor_gstin)}</div>` : ''}</td><td class="r">${num(x.fields.total) ? nf(x.fields.total, 2) : '—'}</td><td>${mk ? `<button class="btn sm pri" data-act="bookScan" data-id="${x.id}">Check and book</button>` : `<button class="btn sm" data-act="openBlob" data-id="/v1/bill-scans/${x.id}/file">Open</button>`}</td></tr>`))}</div>` : '';
  return `<div class="pad" style="padding-bottom:0"><div class="tools"><label class="dim small">Bills dated</label><input class="in sm" type="date" id="vb-from" value="${esc(r.from)}"><span class="dim">to</span><input class="in sm" type="date" id="vb-to" value="${esc(r.to)}"><button class="btn sm" data-act="vbPeriod">Show</button><button class="btn sm" data-act="dl" data-id="/v1/vendor-bills/csv?from=${r.from}&to=${r.to}" data-name="vendor_bills.csv">Download CSV</button><span class="dim small" style="margin-left:auto">Unpaid bills are listed whatever their date.</span></div></div>
    <div class="kpis" style="padding:12px 14px 0">${kpi('To pay', inr(r.unpaid, 2), r.overdue && num(r.overdue) ? inr(r.overdue, 2) + ' overdue' : 'nothing overdue', num(r.overdue) ? 'warn' : 'good')}${kpi('MSME bills past 43B(h) limit', r.msme_late, r.msme_soon ? r.msme_soon + ' due within 7 days' : 'pay micro and small enterprises on time', r.msme_late ? 'bad' : r.msme_soon ? 'warn' : 'good')}${kpi('Awaiting approval', r.pending, 'bills booked, not yet posted', r.pending ? 'warn' : '')}${kpi('Input GST', inr(r.itc, 2), 'credit claimed in the period')}${kpi('TDS deducted', inr(r.tds, 2), r.tds_by_section.map(x => x.section + ' ' + inr(x.tds)).join(' · ') || 'none')}</div>${scans}
    <div class="pad" style="padding-bottom:0"><h4 style="margin:0">Bill register</h4></div>` +
    tbl([['Bill'], ['Vendor'], ['Date'], ['Pay by'], ['Taxable', 'r'], ['GST', 'r'], ['TDS', 'r'], ['Payable', 'r'], ['Status'], ['']], r.bills.map(b => { const by = b.msme_pay_by && (!b.due_date || b.msme_pay_by < b.due_date) ? b.msme_pay_by : b.due_date, open = b.status === 'Unpaid' || b.status === 'Part paid', late = open && by && by < today;
      return `<tr><td class="n">${esc(b.bill_no)}<div class="dim small">${esc(b.id)} · ${esc(b.voucher)}</div></td><td>${esc(b.vendor_name)}${b.vendor_gstin ? `<div class="dim small mono">${esc(b.vendor_gstin)}</div>` : ''}</td><td class="n">${fmtDate(b.bill_date)}</td><td class="n ${late ? 'neg' : ''}">${by ? fmtDate(by) : '—'}${b.msme_pay_by ? `<div>${pill('MSME', late ? 'bad' : 'warn')}</div>` : ''}</td><td class="r">${nf(b.taxable, 2)}</td><td class="r">${nf(num(b.cgst) + num(b.sgst) + num(b.igst), 2)}${b.rcm ? '<div class="dim small">reverse charge</div>' : !b.itc && num(b.cgst) + num(b.igst) ? '<div class="dim small">no ITC</div>' : ''}</td><td class="r">${num(b.tds) ? nf(b.tds, 2) + `<div class="dim small">${esc(b.tds_section)}</div>` : '—'}</td><td class="r">${nf(b.payable, 2)}${num(b.paid) && b.status !== 'Paid' ? `<div class="dim small">paid ${nf(b.paid, 2)}</div>` : ''}</td>
      <td>${pill(b.status, BILL_ST[b.status])}${b.payments.filter(p => p.status === 'Pending').length ? '<div class="dim small">payment awaiting approval</div>' : ''}${b.warnings.length ? `<div class="dim small wrap" title="${esc(b.warnings.join('\n'))}">⚠ ${b.warnings.length} check${b.warnings.length > 1 ? 's' : ''}</div>` : ''}</td>
      <td><div class="split">${open && mk ? `<button class="btn sm pri" data-act="payBill" data-id="${b.id}">Pay</button>` : ''}${b.scan ? `<button class="btn sm" data-act="openBlob" data-id="/v1/bill-scans/${b.scan}/file">Bill</button>` : ''}</div></td></tr>`; }), { tall: true, empty: 'No vendor bills in this period. Scan a bill or enter one.' });
};
P['bk-vend'] = d => { const mk = has('Ops maker', 'Ops checker');
  return tbl([['Vendor'], ['GSTIN / PAN'], ['TDS'], ['MSME'], ['Bank'], ['Bills', 'r'], ['Billed (12 months)', 'r'], ['Owed', 'r'], ['']], d.vend.map(v => `<tr><td><b>${esc(v.name)}</b>${v.active ? '' : ' ' + pill('Inactive', 'nt')}<div class="dim small">${esc(v.id)}${v.state ? ' · ' + esc(v.state) : ''}</div></td><td class="mono small">${esc(v.gstin || '—')}<div class="dim">${esc(v.pan || 'no PAN')}</div></td><td>${esc(v.tds_section || '—')}${v.tds_rate ? ` <span class="dim small">${esc(v.tds_rate)}%</span>` : ''}</td><td>${v.msme ? pill(v.msme_type, v.msme_type === 'Medium' ? 'nt' : 'warn') + `<div class="dim small">${v.terms_days || 15} days</div>` : '—'}</td><td class="small">${v.bank_account ? `${esc(v.bank_name || '')} ${tail4(v.bank_account)}<div class="dim mono">${esc(v.ifsc)}</div>` : '<span class="dim">not given</span>'}</td><td class="r">${v.bills}</td><td class="r">${nf(v.billed12, 2)}</td><td class="r">${nf(v.balance, 2)}${num(v.overdue) ? `<div class="neg small">${nf(v.overdue, 2)} overdue</div>` : ''}</td>
    <td><div class="split">${mk ? `<button class="btn sm" data-act="editVendor" data-id="${v.id}">Edit</button>` : ''}<button class="btn sm" data-act="acctStmt" data-id="${esc(v.ledger)}">Ledger</button></div></td></tr>`), { tall: true, empty: 'No vendors yet. Add one, or scan a bill and add the vendor from it.' });
};
function wideModal(html) { modal(html); document.querySelector('#mroot .md')?.classList.add('wide'); }
function vendorModal(v, after) {
  const tds = C.books.tds, isNew = !v.id, acc = C.books.acc.filter(a => /^(EXPENSE|ASSET):/.test(a.code)).map(a => a.code);
  v = Object.assign({ name: '', gstin: '', pan: '', address: '', email: '', phone: '', contact: '', bank_name: '', ifsc: '', bank_account: '', tds_section: '', tds_rate: '', msme: '', msme_type: '', terms_days: 30, expense: '', active: true, notes: '' }, v);
  const f = (id, l, val, o = {}) => `<div class="fld ${o.full ? 'full' : ''}"><label for="vn-${id}">${l}</label><input class="in ${o.mono ? 'mono' : ''} ${o.read ? 'read' : ''}" id="vn-${id}" value="${esc(val ?? '')}" ${o.ph ? `placeholder="${esc(o.ph)}"` : ''} ${o.list ? `list="${o.list}"` : ''}></div>`;
  modal(mhd(isNew ? 'New vendor' : 'Edit ' + esc(v.name), after ? 'Filled in from the bill: check each detail against the bill and the vendor’s GST certificate.' : 'The GSTIN is checked (check digit, state); the PAN and state come from it.') + `<form class="bd" id="vnf"><datalist id="vn-acc">${acc.map(a => `<option value="${esc(a)}">`).join('')}</datalist><div class="form">
    ${f('name', 'Name', v.name, { read: after })}${f('gstin', 'GSTIN', v.gstin, { mono: true, ph: 'blank if not registered', read: after && v.gstin })}${f('pan', 'PAN', v.pan, { mono: true, ph: 'from the GSTIN' })}${f('address', 'Address', v.address, { full: true, read: after && v.address })}
    ${f('contact', 'Contact person', v.contact)}${f('phone', 'Phone', v.phone)}${f('email', 'Email', v.email)}${f('expense', 'Usual expense account', v.expense, { mono: true, list: 'vn-acc', ph: 'EXPENSE:OFFICE' })}
    ${f('bank', 'Bank', v.bank_name)}${f('ifsc', 'IFSC', v.ifsc, { mono: true })}${f('acct', 'Account number', v.bank_account, { mono: true })}
    <div class="fld"><label for="vn-tds">TDS section</label><select class="in" id="vn-tds">${tds.map(t => `<option value="${t.code}" ${t.code === v.tds_section ? 'selected' : ''}>${esc(t.label)}</option>`).join('')}</select></div>${f('rate', 'TDS rate % (blank: usual)', v.tds_rate)}
    ${f('msme', 'Udyam registration (MSME)', v.msme, { mono: true, ph: 'UDYAM-MH-01-0001234' })}<div class="fld"><label for="vn-mt">Enterprise</label><select class="in" id="vn-mt">${['', 'Micro', 'Small', 'Medium'].map(x => `<option ${x === v.msme_type ? 'selected' : ''}>${x}</option>`).join('')}</select></div>${f('terms', 'Payment terms (days)', v.terms_days)}
    ${isNew ? '' : `<label class="check"><input type="checkbox" id="vn-act" ${v.active ? 'checked' : ''}> Active</label>`}</div>
    ${after && v.bank_account ? '<div class="note warn">The bank account was read from the bill. Before the first payment, confirm it with the vendor on a number you already have (a cancelled cheque or a letter on their letterhead).</div>' : ''}
    <p class="dim small">Micro and small enterprises must be paid within the agreed days, at most 45 (15 without an agreement), or the expense is disallowed until paid (section 43B(h)).</p>
    <div id="vn-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save vendor</button></div></form>`);
  $('vnf').onsubmit = async e => { e.preventDefault(); const g = k => $('vn-' + k).value.trim();
    try { const out = await post('/v1/vendors', { id: v.id || '', name: g('name'), gstin: g('gstin'), pan: g('pan'), address: g('address'), contact: g('contact'), phone: g('phone'), email: g('email'), expense: g('expense'), bank_name: g('bank'), ifsc: g('ifsc'), bank_account: g('acct'), tds_section: $('vn-tds').value, tds_rate: g('rate'), msme: g('msme'), msme_type: $('vn-mt').value, terms_days: parseInt(g('terms'), 10) || 0, active: isNew ? true : $('vn-act').checked, notes: v.notes || '' });
      toast('Vendor ' + out.name + ' saved'); C.books.vend = (await get('/v1/vendors')).vendors; if (after) after(out); else { closeModal(); render({ keep: true }); } }
    catch (err) { $('vn-err').innerHTML = errHTML(err); } };
}
async function scanBillPick() {
  const inp = document.createElement('input'); inp.type = 'file'; inp.accept = 'application/pdf,image/jpeg,image/png';
  inp.onchange = async () => { const file = inp.files[0]; if (!file) return; if (file.size > 10 << 20) { toast('Bills must be under 10 MB'); return; }
    toast('Reading ' + file.name + '…');
    try { const sc = await api('POST', `/v1/bill-scans?name=${encodeURIComponent(file.name)}&ai=1`, file, true); $('toast').hidden = true; C.books.scans = [sc, ...C.books.scans]; billModal(sc); }
    catch (e) { toast(e.message, true); } };
  inp.click();
}
async function billModal(sc, keep) {
  const d = C.books, f = sc?.fields || {}, found = new Set(f.found || []), mk = k => found.has(k) ? 'read' : '';
  const vendors = d.vend.filter(v => v.active), acc = d.acc.filter(a => /^(EXPENSE|ASSET):/.test(a.code)).map(a => a.code);
  const k = Object.assign({ vendor: sc?.vendor || '', bill_no: f.bill_no || '', bill_date: f.bill_date || '', due_date: f.due_date || '', taxable: num(f.taxable) ? f.taxable : '', cgst: num(f.cgst) ? f.cgst : '', sgst: num(f.sgst) ? f.sgst : '', igst: num(f.igst) ? f.igst : '', narration: '', branch: '', expense: '', rcm: false, tds_section: null, tds: '' }, keep || {});
  const vd = id => d.vend.find(v => v.id === id);
  const readBox = sc ? `<div class="readbox"><div class="small"><b>Read from the bill</b> ${pill({ text: 'from the PDF’s text', ai: 'by AI', none: 'nothing read' }[sc.method], sc.method === 'none' ? 'nt' : 'good')}</div>
      ${sc.method !== 'none' ? `<div class="small" style="margin-top:6px">${esc(f.vendor_name || 'Vendor name not read')}${f.vendor_gstin ? ` · <span class="mono">${esc(f.vendor_gstin)}</span>` : ''}${f.vendor_state ? ' · ' + esc(f.vendor_state) : ''}${f.vendor_address ? `<div class="dim">${esc(f.vendor_address)}</div>` : ''}${f.bank_account ? `<div>Bank: ${esc(f.bank_name || '')} a/c <span class="mono">${esc(f.bank_account)}</span> <span class="mono">${esc(f.bank_ifsc || '')}</span></div>` : ''}${num(f.total) ? `<div>Bill total <b>${inr(f.total, 2)}</b></div>` : ''}</div>` : ''}
      ${sc.note ? `<div class="dim small" style="margin-top:6px">${esc(sc.note)}</div>` : ''}${sc.ai_note ? `<div class="dim small">${esc(sc.ai_note)}</div>` : ''}
      ${[...(sc.duplicate ? ['This file was uploaded before (' + sc.duplicate + ').'] : []), ...(sc.checks || []), ...(f.warnings || [])].map(w => `<div class="note warn" style="margin-top:6px">${esc(w)}</div>`).join('')}
      ${sc.ai_offer && sc.method !== 'ai' ? '<button class="btn sm" type="button" id="bm-ai" style="margin-top:8px">Read with AI</button> <span class="dim small">sends this bill to the AI provider; charged from AI credit</span>' : ''}</div>` : '';
  wideModal(mhd(sc ? 'Check and book bill' : 'New vendor bill', 'Fields read from the bill are marked green. Check each against the bill; an Ops checker posts the voucher.') + `<form class="bd" id="bmf"><datalist id="bm-acc">${acc.map(a => `<option value="${esc(a)}">`).join('')}</datalist>
    <div class="${sc ? 'billgrid' : ''}">${sc ? `<div class="billprev" id="bm-prev"><div class="dim small pad">Loading the bill…</div></div>` : ''}<div>${readBox}<div class="form">
    <div class="fld full"><label for="bm-v">Vendor</label><div class="split"><select class="in" id="bm-v"><option value="">Choose a vendor</option>${vendors.map(v => `<option value="${v.id}" ${v.id === k.vendor ? 'selected' : ''}>${esc(v.name)}${v.gstin ? ' · ' + esc(v.gstin) : ''}</option>`).join('')}</select><button class="btn sm" type="button" id="bm-nv">${sc && !sc.vendor && (f.vendor_name || f.vendor_gstin) ? 'Add vendor from bill' : 'New vendor'}</button></div>${sc?.vendor ? `<div class="dim small">Matched by ${{ gstin: 'GSTIN', pan: 'PAN', name: 'name' }[sc.vendor_match]}</div>` : ''}</div>
    <div class="fld"><label for="bm-no">Bill number</label><input class="in mono ${mk('bill_no')}" id="bm-no" value="${esc(k.bill_no)}" required></div><div class="fld"><label for="bm-dt">Bill date</label><input class="in ${mk('bill_date')}" type="date" id="bm-dt" value="${esc(k.bill_date)}" required></div>
    <div class="fld"><label for="bm-due">Due date</label><input class="in ${mk('due_date')}" type="date" id="bm-due" value="${esc(k.due_date)}"></div><div class="fld"><label for="bm-exp">Expense account</label><input class="in mono" list="bm-acc" id="bm-exp" value="${esc(k.expense)}" placeholder="the vendor’s usual"></div>
    <div class="fld"><label for="bm-tx">Taxable value</label><input class="in ${mk('taxable')}" id="bm-tx" inputmode="decimal" value="${esc(k.taxable)}" required></div><div class="fld"><label for="bm-ig">IGST</label><input class="in ${mk('igst')}" id="bm-ig" inputmode="decimal" value="${esc(k.igst)}"></div>
    <div class="fld"><label for="bm-cg">CGST</label><input class="in ${mk('cgst')}" id="bm-cg" inputmode="decimal" value="${esc(k.cgst)}"></div><div class="fld"><label for="bm-sg">SGST / UTGST</label><input class="in ${mk('sgst')}" id="bm-sg" inputmode="decimal" value="${esc(k.sgst)}"></div>
    <label class="check"><input type="checkbox" id="bm-itc" checked> Claim input tax credit</label><label class="check"><input type="checkbox" id="bm-rcm" ${k.rcm ? 'checked' : ''}> Reverse charge (the firm pays the GST)</label>
    <div class="fld"><label for="bm-ts">TDS section</label><select class="in" id="bm-ts">${d.tds.map(t => `<option value="${t.code}">${esc(t.label)}</option>`).join('')}</select></div><div class="fld"><label for="bm-td">TDS</label><input class="in" id="bm-td" inputmode="decimal" value="${esc(k.tds)}"></div>
    <div class="fld"><label for="bm-br">Branch</label><input class="in" id="bm-br" value="${esc(k.branch)}" placeholder="Head office"></div><div class="fld"><label for="bm-n">Narration</label><input class="in" id="bm-n" value="${esc(k.narration)}" placeholder="what was bought"></div></div>
    <div class="billsum" id="bm-sum"></div></div></div>
    <div id="bm-err"></div><div class="ft" style="padding:0;border:0">${sc ? '<button class="btn bad" type="button" id="bm-del" style="margin-right:auto">Discard upload</button>' : ''}<button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Book bill</button></div></form>`);
  const val = id => $(id).value.trim(), state = () => ({ vendor: $('bm-v').value, bill_no: val('bm-no'), bill_date: val('bm-dt'), due_date: val('bm-due'), expense: val('bm-exp'), taxable: val('bm-tx'), cgst: val('bm-cg'), sgst: val('bm-sg'), igst: val('bm-ig'), rcm: $('bm-rcm').checked, tds_section: $('bm-ts').value, tds: val('bm-td'), branch: val('bm-br'), narration: val('bm-n') });
  const rate = () => { const v = vd($('bm-v').value), sec = $('bm-ts').value; if (!sec) return 0; const t = d.tds.find(x => x.code === sec); if (v && v.tds_rate && v.tds_section === sec) return num(v.tds_rate); if (v && !v.pan) return 20; if (sec === '194C' && v && /^...[PH]/.test(v.pan)) return 1; return num(t?.rate); };
  let tdsTouched = !!k.tds;
  const recalc = () => { const tx = num(val('bm-tx')), g = num(val('bm-cg')) + num(val('bm-sg')) + num(val('bm-ig')), rcm = $('bm-rcm').checked;
    if (!tdsTouched) $('bm-td').value = $('bm-ts').value ? Math.round(tx * rate() / 100) : '';
    const tds = num(val('bm-td')), total = rcm ? tx : tx + g, v = vd($('bm-v').value);
    const diff = sc && num(f.total) && !rcm ? total - num(f.total) : 0;
    $('bm-sum').innerHTML = `<div>Bill value <b>${inr(total, 2)}</b>${rcm && g ? ` · GST ${inr(g, 2)} paid by the firm` : ''}${tds ? ` · TDS ${inr(tds, 2)}` : ''} · <b>pay the vendor ${inr(total - tds, 2)}</b></div>${Math.abs(diff) >= 1 ? `<div class="note warn" style="margin-top:6px">This is ${inr(Math.abs(diff), 2)} ${diff > 0 ? 'more' : 'less'} than the bill total read from the bill (${inr(f.total, 2)}).</div>` : ''}${v && v.msme && v.msme_type !== 'Medium' ? `<div class="dim small" style="margin-top:4px">MSME (${esc(v.msme_type)}): pay within ${Math.min(v.terms_days || 15, 45)} days of the bill date.</div>` : ''}`; };
  const pickVendor = () => { const v = vd($('bm-v').value); if (!v) { recalc(); return; } if (!$('bm-exp').value) $('bm-exp').value = v.expense || ''; if (!tdsTouched) $('bm-ts').value = v.tds_section || ''; recalc(); };
  if (k.tds_section !== null) $('bm-ts').value = k.tds_section;
  $('bm-v').onchange = pickVendor; ['bm-tx', 'bm-cg', 'bm-sg', 'bm-ig'].forEach(id => $(id).oninput = recalc); $('bm-rcm').onchange = recalc; $('bm-ts').onchange = () => { tdsTouched = false; recalc(); }; $('bm-td').oninput = () => { tdsTouched = true; recalc(); };
  if (k.tds_section === null) pickVendor(); else recalc();
  $('bm-nv').onclick = () => { const cur = state(); vendorModal(sc && !sc.vendor ? { name: f.vendor_name, gstin: f.vendor_gstin, pan: f.vendor_pan, address: f.vendor_address, email: f.vendor_email, phone: f.vendor_phone, bank_name: f.bank_name, ifsc: f.bank_ifsc, bank_account: f.bank_account } : {}, v => { if (sc) { sc.vendor = v.id; sc.vendor_match = 'gstin'; } billModal(sc, Object.assign(cur, { vendor: v.id, tds_section: null })); }); };
  if ($('bm-ai')) $('bm-ai').onclick = async () => { $('bm-ai').disabled = true; $('bm-ai').textContent = 'Reading…'; try { const s2 = await post(`/v1/bill-scans/${sc.id}/ai`); C.books.scans = C.books.scans.map(x => x.id === s2.id ? s2 : x); billModal(s2); } catch (e) { $('bm-err').innerHTML = errHTML(e); $('bm-ai').disabled = false; $('bm-ai').textContent = 'Read with AI'; } };
  if ($('bm-del')) $('bm-del').onclick = () => confirmModal('Discard ' + esc(sc.name) + '?', 'It leaves the list of bills to book. The file stays in the firm’s records.', '', 'Discard', () => act(() => post(`/v1/bill-scans/${sc.id}/discard`), 'Upload discarded'), true);
  $('bmf').onsubmit = async e => { e.preventDefault(); const st = state();
    try { const b = await post('/v1/vendor-bills', Object.assign(st, { taxable: st.taxable || '0', cgst: st.cgst || '0', sgst: st.sgst || '0', igst: st.igst || '0', tds: st.tds || '0', itc: $('bm-itc').checked, scan: sc?.id || '' }));
      closeModal(); toast(`Bill ${b.bill_no} booked as ${b.voucher}${b.warnings.length ? ' · ' + b.warnings.length + ' check' + (b.warnings.length > 1 ? 's' : '') : ''}`); UI.bkTab = 'bills'; render({ keep: true }); }
    catch (err) { $('bm-err').innerHTML = errHTML(err); } };
  if (sc) { try { const res = await fetch(`/v1/bill-scans/${sc.id}/file`, { headers: { 'X-Requested-With': 'ledgerline' } }); if (!res.ok) throw new Error(); const url = URL.createObjectURL(await res.blob()); const el = $('bm-prev'); if (el) el.innerHTML = sc.content_type === 'application/pdf' ? `<iframe src="${url}" title="The bill"></iframe>` : `<img src="${url}" alt="The bill">`; } catch (e) { const el = $('bm-prev'); if (el) el.innerHTML = '<div class="dim small pad">The bill could not be shown.</div>'; } }
}
function payBillModal(b) {
  const v = C.books.vend.find(x => x.id === b.vendor) || {}, banks = [...new Set(['BANK:OWN', ...C.books.acc.filter(a => /^(BANK|CASH):/.test(a.code) && a.code !== 'BANK:CLIENT').map(a => a.code)])];
  const pend = b.payments.filter(p => p.status === 'Pending').reduce((a, p) => a + num(p.amount), 0), left = num(b.payable) - num(b.paid) - pend;
  modal(mhd('Pay ' + esc(b.vendor_name), `Bill ${esc(b.bill_no)} of ${fmtDate(b.bill_date)} · still to pay ${inr(left, 2)}`) + `<form class="bd" id="pbf"><div class="form">
    <div class="fld"><label for="pb-a">Amount</label><input class="in" id="pb-a" inputmode="decimal" value="${left.toFixed(2)}"></div><div class="fld"><label for="pb-d">Date</label><input class="in" type="date" id="pb-d" value="${esc(S.date)}"></div>
    <div class="fld"><label for="pb-b">From</label><select class="in mono" id="pb-b">${banks.map(x => `<option ${x === 'BANK:OWN' ? 'selected' : ''}>${esc(x)}</option>`).join('')}</select></div><div class="fld"><label for="pb-r">UTR / cheque no.</label><input class="in mono" id="pb-r"></div></div>
    <div class="readbox small">${v.bank_account ? `Pay to <b>${esc(v.name)}</b> · ${esc(v.bank_name || '')} a/c <span class="mono">${esc(v.bank_account)}</span> · IFSC <span class="mono">${esc(v.ifsc)}</span>` : 'The vendor master has no bank account for this vendor: add it before paying by bank.'}${b.msme_pay_by ? `<div>${pill('MSME', 'warn')} pay by ${fmtDate(b.msme_pay_by)}</div>` : ''}</div>
    <div id="pb-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Send payment for approval</button></div></form>`);
  $('pbf').onsubmit = async e => { e.preventDefault(); try { const out = await post(`/v1/vendor-bills/${b.id}/pay`, { amount: $('pb-a').value.trim() || '0', date: $('pb-d').value, bank: $('pb-b').value, ref: $('pb-r').value.trim() }); closeModal(); toast(`Payment ${out.payments[out.payments.length - 1].voucher} sent for approval`); render({ keep: true }); } catch (err) { $('pb-err').innerHTML = errHTML(err); } };
}

/* ---------- corporate actions and buy-backs ---------- */
V.corp = {
  load: async () => { const [ca, bb] = await Promise.all([get('/v1/corporate-actions'), get('/v1/buybacks')]); return { ca, bb }; },
  html: d => {
    const mk = has('Ops maker', 'Ops checker'), pend = d.ca.filter(c => c.status === 'Announced');
    return ph('Corporate actions', 'Dividends, bonus issues, splits and buy-back tender offers. A bonus or split changes every holding and purchase lot of the scrip (bonus shares cost nil and are acquired on allotment; a split keeps the acquisition date), so capital gains and portfolio reports stay right. Dividends on shares the firm still held for clients on the record date (CUSPA) are received by the firm and passed on.',
      mk ? `<button class="btn" data-act="newCA">Record a corporate action</button><button class="btn" data-act="newBB">Record a buy-back offer</button>` : '') +
    `<div class="kpis">${kpi('Announced', pend.length, 'to apply on the record date', pend.length ? 'warn' : '')}${kpi('Applied', d.ca.filter(c => c.status !== 'Announced').length, 'holdings and lots adjusted')}${kpi('Dividends to pass on', inr(sum(d.ca.filter(c => c.type === 'Dividend' && c.status !== 'Dividend received'), c => num(c.recon.expected)), 2), 'on broker-held shares')}${kpi('Buy-backs open', d.bb.filter(b => b.status === 'Open').length, sum(d.bb.filter(b => b.status === 'Open'), b => b.bids.filter(x => x.status === 'Tendered').length) + ' tenders')}</div>
    <div class="panel"><div class="hd">${segBtns('caTab', [['ca', 'Corporate actions'], ['bb', 'Buy-backs']])}</div>${UI.caTab === 'bb' ? P.bb(d) : P.ca(d)}</div>`;
  }
};
P.ca = d => tbl([['Action'], ['Scrip'], ['Type'], ['Terms'], ['Ex / record'], ['Clients', 'r'], ['Effect'], ['Status'], ['']], d.ca.map(c => {
  const terms = c.type === 'Dividend' ? `₹${esc(c.dps)} a share` : c.type === 'Bonus' ? `${c.ratio_new}:${c.ratio_old} (new : held)` : c.type === 'Split' ? `FV ${c.ratio_old} → ${c.ratio_new}` : c.type === 'Symbol change' ? `→ ${esc(c.new_symbol)} ${esc(c.new_isin)}` : `${c.ratio_new}:${c.ratio_old} of ${esc(c.new_symbol)} ${esc(c.new_isin)}`;
  const eff = c.type === 'Dividend' ? `${inr(sum(c.lines, l => num(l.dividend)), 2)} paid by company · ${inr(c.recon.expected, 2)} via firm` : `+${nf(sum(c.lines, l => l.new_shares))} shares`;
  const act = c.status === 'Announced' && has('Ops checker') ? `<button class="btn sm pri" data-act="caApply" data-id="${c.id}">Apply</button>` : c.type === 'Dividend' && c.status === 'Applied' && has('Ops checker') ? `<button class="btn sm pri" data-act="caDiv" data-id="${c.id}">Dividend received</button>` : '';
  return `<tr><td class="n">${c.id}</td><td>${esc(c.symbol)} <span class="tag">${esc(c.isin)}</span></td><td>${c.type}</td><td>${terms}</td><td class="n">${fmtDate(c.ex_date)}<div class="dim small">rec ${fmtDate(c.record_date)}</div></td><td class="r">${c.lines.length}</td><td class="small">${eff}${c.recon && c.status === 'Dividend received' ? `<div class="dim">received ${inr(c.recon.received, 2)} · credited ${inr(c.recon.credited, 2)}${num(c.recon.excess) ? ' · excess ' + inr(c.recon.excess, 2) + ' in suspense' : ''}</div>` : ''}</td><td>${pill(c.status, c.status === 'Announced' ? 'warn' : 'good')}</td><td>${act}</td></tr>`; }), { empty: 'No corporate actions recorded.' });
P.bb = d => d.bb.length ? d.bb.map(b => `<div class="pad" style="border-top:1px solid var(--line)"><div class="split"><div><b>${esc(b.symbol)}</b> ${esc(b.company || '')} <span class="tag">${esc(b.isin)}</span> · ₹${esc(b.price)} a share · tenders ${fmtDate(b.open)} – ${fmtDate(b.close)} ${pill(b.status, b.status === 'Open' ? 'warn' : 'good')}</div>
  <div class="split">${b.status === 'Open' && has('Ops maker', 'Ops checker') ? `<button class="btn sm" data-act="bbTender" data-id="${b.id}">Tender for a client</button>` : ''}<button class="btn sm" data-act="dl" data-id="/v1/buybacks/${b.id}/file" data-name="buyback_bids_${b.id}.csv">Bid file</button>${b.status === 'Open' && has('Ops checker') && b.bids.some(x => x.status === 'Tendered') ? `<button class="btn sm pri" data-act="bbSettle" data-id="${b.id}">Enter acceptance</button>` : ''}</div></div></div>` +
  tbl([['Client'], ['Tendered', 'r'], ['Date'], ['By'], ['Accepted', 'r'], ['Proceeds', 'r'], ['Status']], b.bids.map(x => `<tr><td><button class="linkish mono" data-act="client" data-id="${x.ucc}">${x.ucc}</button> ${esc(x.name)}</td><td class="r">${nf(x.qty)}</td><td class="n">${fmtDate(x.date)}</td><td class="dim">${esc(x.by)}</td><td class="r">${x.accepted || '—'}</td><td class="r">${num(x.amount) ? nf(x.amount, 2) : '—'}</td><td>${pill(x.status, { Tendered: 'warn', Accepted: 'good', 'Partly accepted': 'good', Cancelled: 'nt', 'Not accepted': 'nt' }[x.status])}</td></tr>`), { empty: 'No tenders yet.' })).join('') : '<div class="pad dim">No buy-back offers recorded.</div>';

/* ---------- receipts and payouts ---------- */
V.money = {
  load: async () => { const [rc, po] = await Promise.all([get('/v1/receipts'), get(`/v1/payouts?date=${S.date}`)]); return { rc, ...po }; },
  html: d => {
    const pend = d.rc.filter(r => r.status === 'Pending'), unc = d.rc.filter(r => r.status === 'Uncleared'), gw = d.rc.filter(r => r.mode === 'Gateway' && !r.settled && r.status === 'Credited');
    const req = d.requests.filter(r => r.status === 'Requested'), appr = d.requests.filter(r => r.status === 'Approved' && !r.batch), open = d.batches.filter(b => b.status !== 'Confirmed');
    const mk = has('Ops maker', 'Ops checker'), ck = has('Ops checker');
    return ph('Receipts & payouts', 'Money that reaches the firm outside the bank-statement match (cheques, transfers advised by the client, online payments) and money paid out to clients on request. Only the client’s registered bank account may pay in. Uncleared cheques do not count towards limits or payouts. Payouts are checked against the ledger across all segments, margin not covered by collateral, uncleared cheques, unposted interest and other approved payouts.',
      `${mk ? '<button class="btn" data-act="newReceipt">Record a receipt</button><button class="btn" data-act="newPayout">Payout request</button>' : ''}${ck && appr.length ? `<button class="btn pri" data-act="payoutBatch">Release ${appr.length} payout${appr.length > 1 ? 's' : ''} to the bank</button>` : ''}`) +
    `<div class="kpis">${kpi('Receipts to approve', pend.length, inr(sum(pend, r => num(r.amount))), pend.length ? 'warn' : 'good')}${kpi('Uncleared cheques', unc.length, inr(sum(unc, r => num(r.amount))) + ' not usable yet', unc.length ? 'warn' : '')}${kpi('Online payments to settle', gw.length, inr(sum(gw, r => num(r.amount))) + ' with the gateway')}${kpi('Payout requests', req.length, inr(sum(req, r => num(r.amount))) + ' awaiting approval', req.length ? 'warn' : 'good')}${kpi('Approved, not released', appr.length, inr(sum(appr, r => num(r.amount))))}${kpi('Batches at the bank', open.length, open.length ? 'confirm with UTRs' : 'none open', open.length ? 'warn' : '')}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('monTab', [['rcpt', `Receipts · ${d.rc.length}`], ['chq', `Cheques to clear · ${unc.length}`], ['gw', 'Online payments'], ['req', `Payout requests · ${d.requests.length}`], ['batch', `Payout batches · ${d.batches.length}`]])}<input class="in" data-ui="monQ" data-part="mon-t" placeholder="Filter name, UCC or reference" value="${esc(UI.monQ)}"></div>
      ${UI.monTab === 'gw' ? `<div class="split">${CFG.dev ? '<button class="btn sm" data-act="devGateway">Simulate an online payment</button>' : ''}${ck && gw.length ? '<button class="btn sm pri" data-act="settleGateway">Record gateway settlement</button>' : ''}</div>` : ''}</div><div id="mon-t">${P['mon-t'](d)}</div></div>`;
  }
};
P['mon-t'] = d => {
  const q = UI.monQ.trim().toLowerCase(), f = r => !q || [r.ucc, r.name, r.ref, r.id].join(' ').toLowerCase().includes(q);
  const ck = has('Ops checker'), mk = has('Ops maker', 'Ops checker'), me = S.me.actor;
  const who = r => `<button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}`;
  const rst = s => pill(s, { Credited: 'good', Paid: 'good', Approved: 'good', Pending: 'warn', Requested: 'warn', Uncleared: 'warn', Bounced: 'bad', Rejected: 'bad', Failed: 'bad', Expired: 'nt', 'Sent to bank': 'warn', Confirmed: 'good' }[s] || 'nt');
  if (UI.monTab === 'req') return tbl([['Request'], ['Client'], ['Amount', 'r'], ['Available then', 'r'], ['Entered by'], ['Status'], ['']], d.requests.filter(f).map(r => `<tr><td class="n">${r.id}<div class="dim small">${fmtDate(r.date)} · valid to ${fmtDate(r.expires_on)}</div></td><td>${who(r)}</td><td class="r">${nf(r.amount, 2)}</td><td class="r dim">${nf(r.available, 2)}</td><td>${esc(r.source)}<div class="dim small">${esc(r.maker)}</div></td><td>${rst(r.status)}${r.batch ? `<div class="dim small">${esc(r.batch)}${r.utr ? ' · UTR ' + esc(r.utr) : ''}</div>` : ''}${r.reason ? `<div class="dim small wrap">${esc(r.reason)}</div>` : ''}</td><td>${r.status === 'Requested' && has('Ops checker', 'Risk manager') && r.maker !== me ? `<div class="split"><button class="btn sm pri" data-act="poDecide" data-id="${r.id}" data-a="1">Approve</button><button class="btn sm bad" data-act="poDecide" data-id="${r.id}" data-a="0">Reject</button></div>` : ''}</td></tr>`), { tall: true, empty: 'No payout requests' });
  if (UI.monTab === 'batch') return tbl([['Batch'], ['Date'], ['Transfers', 'r'], ['Total', 'r'], ['Status'], ['']], d.batches.filter(b => !q || b.id.toLowerCase().includes(q) || b.items.some(f)).map(b => `<tr><td class="n">${b.id}<div class="dim small">${esc(b.by)}</div></td><td class="n">${fmtDate(b.date)}</td><td class="r">${b.items.length}<div class="dim small">${b.items.filter(i => i.mode === 'RTGS').length} RTGS</div></td><td class="r">${nf(b.total, 2)}</td><td>${rst(b.status)}${b.results ? `<div class="dim small">${b.results.Paid} paid · ${b.results.Failed} failed</div>` : ''}</td><td><div class="split"><button class="btn sm" data-act="dl" data-id="/v1/payouts/batches/${b.id}/file" data-name="payout_${b.id}.csv">Bank file</button>${b.status !== 'Confirmed' && mk ? `<button class="btn sm pri" data-act="poConfirm" data-id="${b.id}">Enter bank result</button>` : ''}</div></td></tr>`), { empty: 'No payout batches yet' });
  let rows = d.rc.filter(f);
  if (UI.monTab === 'chq') rows = rows.filter(r => r.status === 'Uncleared');
  if (UI.monTab === 'gw') rows = rows.filter(r => r.mode === 'Gateway');
  return tbl([['Receipt'], ['Client'], ['Mode'], ['Reference'], ['Amount', 'r'], ['Maker / checker'], ['Status'], ['']], rows.map(r => `<tr><td class="n">${r.id}<div class="dim small">${fmtDate(r.date)}</div></td><td>${who(r)}</td><td>${esc(r.mode)}</td><td class="n">${esc(r.ref)}<div class="dim small">${esc(r.drawee_bank || '')} ${esc(r.from_account || '')}</div></td><td class="r">${nf(r.amount, 2)}</td><td class="dim small">${esc(r.maker)}${r.checker ? ' · ' + esc(r.checker) : ''}</td><td>${rst(r.status)}${r.cleared_on ? `<div class="dim small">${fmtDate(r.cleared_on)}</div>` : ''}${r.settled ? `<div class="dim small">settled ${esc(r.settled)}</div>` : ''}${r.note ? `<div class="dim small wrap">${esc(r.note)}</div>` : ''}</td><td>${r.status === 'Pending' && ck && r.maker !== me ? `<div class="split"><button class="btn sm pri" data-act="rcDecide" data-id="${r.id}" data-a="1">Approve</button><button class="btn sm bad" data-act="rcDecide" data-id="${r.id}" data-a="0">Reject</button></div>` : ''}${r.status === 'Uncleared' && mk ? `<div class="split"><button class="btn sm" data-act="chqClear" data-id="${r.id}" data-a="0">Cleared</button><button class="btn sm bad" data-act="chqClear" data-id="${r.id}" data-a="1">Returned</button></div>` : ''}</td></tr>`),
    { tall: true, empty: UI.monTab === 'chq' ? 'No cheques waiting to clear' : UI.monTab === 'gw' ? 'No online payments yet' : 'No receipts recorded' });
};
async function receiptModal() {
  await clientCache();
  modal(mhd('Record a receipt', 'A different Ops checker approves it before the client is credited. Cash is not accepted.') + `<form class="bd" id="rcf"><div class="form">
    <div class="fld full"><label for="rc-u-q">Client</label>${cpick('rc-u', '', {})}</div>
    <div class="fld"><label for="rc-mode">Mode</label><select class="in" id="rc-mode">${['Cheque', 'NEFT', 'RTGS', 'IMPS', 'UPI'].map(m => `<option>${m}</option>`).join('')}</select></div>
    <div class="fld"><label for="rc-date">Date</label><input class="in" type="date" id="rc-date" value="${esc(S.date)}"></div>
    <div class="fld"><label for="rc-ref">Cheque number / UTR</label><input class="in mono" id="rc-ref" required></div>
    <div class="fld"><label for="rc-amt">Amount (₹)</label><input class="in" id="rc-amt" inputmode="decimal" required></div>
    <div class="fld"><label for="rc-bank">Drawee bank</label><input class="in" id="rc-bank" placeholder="for cheques"></div>
    <div class="fld"><label for="rc-acct">From account / UPI ID</label><input class="in mono" id="rc-acct" placeholder="blank = registered account"></div>
    </div><div id="rc-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Send for approval</button></div></form>`);
  $('rcf').onsubmit = async e => { e.preventDefault();
    try { await post('/v1/receipts', { ucc: $('rc-u').value, mode: $('rc-mode').value, date: $('rc-date').value, ref: $('rc-ref').value.trim(), amount: $('rc-amt').value.trim(), drawee_bank: $('rc-bank').value.trim(), from_account: $('rc-acct').value.trim() }); closeModal(); toast('Receipt recorded; awaiting approval'); render({ keep: true }); }
    catch (err) { $('rc-err').innerHTML = errHTML(err); } };
}
async function payoutModal() {
  await clientCache();
  const show = async u => { if (!u) { $('po-w').innerHTML = ''; return; } try { const w = await get(`/v1/payouts/withdrawable?date=${S.date}&ucc=${encodeURIComponent(u)}`);
    $('po-w').innerHTML = `<div class="kv"><div><small>Ledger</small><b class="mono ${sgn(w.ledger)}">${inr(w.ledger, 2)}</b></div><div><small>Margin kept</small><b class="mono">${inr(w.margin, 2)}</b></div><div><small>Uncleared</small><b class="mono">${inr(w.uncleared, 2)}</b></div><div><small>Interest accrued</small><b class="mono">${inr(w.accrued, 2)}</b></div><div><small>Already approved</small><b class="mono">${inr(w.pending, 2)}</b></div><div><small>Available</small><b class="mono pos">${inr(w.available, 2)}</b></div></div>${w.note ? `<div class="note warn">${esc(w.note)}</div>` : ''}`; $('po-amt').value = num(w.available) ? num(w.available).toFixed(2) : ''; } catch (e) { $('po-w').innerHTML = errHTML(e); } };
  modal(mhd('Payout request · ' + fmtDate(S.date), 'Entered for a client who asked by phone, email or at a branch. Valid for two business days; an Ops checker or Risk manager approves it after a fresh check.') + `<form class="bd" id="pof">
    <div class="fld"><label for="po-u-q">Client</label>${cpick('po-u', '', { on: show })}</div><div id="po-w"></div>
    <div class="fld"><label for="po-amt">Amount (₹)</label><input class="in" id="po-amt" inputmode="decimal" required></div>
    <div id="po-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Enter request</button></div></form>`);
  $('pof').onsubmit = async e => { e.preventDefault();
    try { await post('/v1/payouts', { date: S.date, ucc: $('po-u').value, amount: $('po-amt').value.trim() }); closeModal(); toast('Payout request entered'); UI.monTab = 'req'; render({ keep: true }); }
    catch (err) { $('po-err').innerHTML = errHTML(err); } };
}

/* ---------- debtors and interest ---------- */
V.debits = {
  load: async () => {
    const from = UI.intFrom || S.date.slice(0, 8) + '01', to = UI.intTo || S.date;
    const [deb, intr] = await Promise.all([get(`/v1/debits?date=${S.date}`), get(`/v1/interest?from=${from}&to=${to}`).catch(e => ({ error: e.message, rows: [], from, to }))]);
    return { deb, intr };
  },
  html: d => {
    const v = d.deb, risk = has('Risk manager'), long = v.rows.filter(r => r.days >= v.block_days && !r.legacy).length;
    return ph('Debtors & interest', `Clients whose ledger is in debit after the pay-in date. A bill dated T is due on T+1, so it counts against the client from the next day. Clients in debit for ${v.block_days} days or more are blocked from buying in the trading front end until they clear the debit; interest (delayed payment charges) is charged on the overdue debit at the firm’s rate.`,
      `<button class="btn" data-act="dl" data-id="/v1/debits/block-file?date=${S.date}" data-name="debit_block_${S.date}.csv">Block / unblock file</button>${risk ? `<button class="btn pri" data-act="debitBlock" ${v.to_block + v.to_unblock ? '' : 'disabled'}>Apply blocks (${v.to_block} block · ${v.to_unblock} unblock)</button>` : ''}`) +
    `<div class="kpis">${kpi('Clients in debit', v.rows.filter(r => num(r.debit) > 0 && !r.legacy).length, inr(v.debit) + ' overdue', v.rows.length ? 'warn' : 'good')}${kpi(`In debit ≥ ${v.block_days} days`, long, 'continuous debit', long ? 'bad' : 'good')}${kpi('To block / unblock', `${v.to_block} / ${v.to_unblock}`, 'front-end buying', v.to_block ? 'bad' : '')}${kpi('Not covered by securities', inr(v.shortfall), 'debit above holdings after haircut', num(v.shortfall) ? 'bad' : 'good')}${kpi('Interest accrued', inr(v.accrued, 2), 'this month, not yet posted')}</div>
    ${v.legacy_count ? `<div class="note">${v.legacy_count} legacy debt${v.legacy_count === 1 ? '' : 's'} (${inr(v.legacy_debit)}) are kept out of these figures, the interest and the reminders, and stay blocked for buying: see <a href="#legacy" data-act="go" data-id="legacy">Legacy debts</a>.</div>` : ''}
    <div class="panel"><div class="hd"><div class="tools">${segBtns('debTab', [['cd', 'Continuous debit'], ['age', 'Debit ageing'], ['stock', 'Debtors’ stock'], ['int', 'Interest']])}${UI.debTab !== 'int' ? `<input class="in" data-ui="debQ" data-part="deb-t" placeholder="Filter name, UCC or group" value="${esc(UI.debQ)}">` : ''}</div></div><div id="deb-t">${P['deb-t'](d)}</div></div>`;
  }
};
P['deb-t'] = d => {
  const v = d.deb, q = UI.debQ.trim().toLowerCase(), f = r => !q || (r.ucc + ' ' + r.name + ' ' + r.tags.join(' ')).toLowerCase().includes(q);
  const who = r => `<button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}${r.tags.length ? ' ' + r.tags.map(t => `<span class="gtag">${esc(t)}</span>`).join('') : ''}`;
  const rows = v.rows.filter(r => !r.legacy).filter(f);
  if (UI.debTab === 'age') return tbl([['Client'], ['Overdue debit', 'r'], ...v.buckets.map(b => [b, 'r'])], rows.filter(r => num(r.debit) > 0).map(r => `<tr><td>${who(r)}</td><td class="r">${nf(r.debit, 2)}</td>${r.ageing.map((a, i) => `<td class="r ${num(a) && i >= 2 ? 'neg' : ''}">${num(a) ? nf(a, 2) : '<span class="dim">—</span>'}</td>`).join('')}</tr>`),
    { tall: true, empty: 'No client is in debit.', foot: rows.length ? `<tfoot><tr><td>Total</td><td class="r">${nf(v.debit, 2)}</td>${v.totals.map(a => `<td class="r">${nf(a, 2)}</td>`).join('')}</tr></tfoot>` : '' }) + '<div class="pad"><span class="dim small">Ageing is first in, first out: each receipt clears the oldest debit first, so the buckets show how old the money still owed is.</span></div>';
  if (UI.debTab === 'stock') return tbl([['Client'], ['Overdue debit', 'r'], ['Pledged', 'r'], ['Other holdings', 'r'], ['Value after haircut', 'r'], ['Cover', 'r'], ['Not covered', 'r'], ['Days', 'r']], rows.filter(r => num(r.debit) > 0).sort((a, b) => num(b.shortfall) - num(a.shortfall)).map(r => `<tr><td>${who(r)}</td><td class="r">${nf(r.debit, 2)}</td><td class="r">${nf(r.pledged)}</td><td class="r">${nf(r.unpledged)}</td><td class="r">${nf(r.collateral)}</td><td class="r ${num(r.cover_pct) < 100 ? 'neg' : 'pos'}">${nf(r.cover_pct)}%</td><td class="r ${num(r.shortfall) ? 'neg' : ''}">${num(r.shortfall) ? nf(r.shortfall, 2) : '—'}</td><td class="r">${r.days}</td></tr>`),
    { tall: true, empty: 'No client is in debit.' }) + '<div class="pad"><span class="dim small">Holdings valued at the last price less the VaR haircut. Only pledged securities (or those under a DDPI/POA) can be sold to recover a debit; “Other holdings” shows what more the client holds.</span></div>';
  if (UI.debTab === 'int') return P['int-t'](d.intr);
  return tbl([['Client'], ['In debit since'], ['Days', 'r'], ['Overdue debit', 'r'], ['Ledger now', 'r'], ['Interest this month', 'r'], ['Front end'], ['Action']], rows.map(r => `<tr><td>${who(r)}</td><td class="n">${r.since ? fmtDate(r.since) : '—'}</td><td class="r ${r.days >= v.block_days ? 'neg' : ''}">${r.days || '—'}</td><td class="r">${num(r.debit) ? nf(r.debit, 2) : '—'}</td><td class="r ${sgn(r.ledger)}">${nf(r.ledger, 2)}</td><td class="r">${num(r.accrued) ? nf(r.accrued, 2) : '—'}</td><td>${r.blocked ? pill('Buy blocked', 'bad') + `<div class="dim small wrap">${esc(r.blocked_by)}</div>` : pill('Open', 'good')}</td><td>${r.action ? pill(r.action, r.action === 'Block' ? 'bad' : 'good') : ''}</td></tr>`),
    { tall: true, empty: 'No client is in debit after pay-in.' });
};
P['int-t'] = iv => {
  const can = has('Ops checker', 'Risk manager'), rows = iv.rows || [], to = rows.filter(r => r.status === 'To post');
  return `<div class="pad" style="padding-bottom:0"><div class="tools"><label class="dim small">Period</label><input class="in sm" type="date" id="int-from" value="${esc(iv.from)}"><span class="dim">to</span><input class="in sm" type="date" id="int-to" value="${esc(iv.to)}"><button class="btn sm" data-act="intPeriod">Show</button>
    <span class="dim small" style="margin-left:auto">${iv.error ? '' : `${esc(iv.rate_pct)}% a year · ${iv.grace_days || 0} grace days · GST ${esc(iv.gst_pct)}% · minimum ${inr(iv.minimum, 2)}${(iv.exempt_tags || []).length ? ' · exempt: ' + iv.exempt_tags.map(esc).join(', ') : ''}`}</span>
    ${can && to.length ? `<button class="btn sm pri" data-act="postInterest">Post interest for ${to.length} client${to.length > 1 ? 's' : ''} (${inr(iv.to_post, 2)})</button>` : ''}${can && iv.posting === 'daily' ? `<button class="btn sm pri" data-act="postDailyInterest">Post daily interest up to ${fmtDate(S.date)}</button>` : ''}</div></div>
    ${iv.error ? `<div class="bd">${errHTML({ message: iv.error })}</div>` : tbl([['Client'], ['Days charged', 'r'], ['Average debit', 'r'], ['Interest', 'r'], ['GST', 'r'], ['Total', 'r'], ['Status'], ['Invoice']], rows.map(r => `<tr><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}${r.tags.map(t => ` <span class="gtag">${esc(t)}</span>`).join('')}</td><td class="r">${r.debit_days}</td><td class="r">${nf(r.avg_debit, 2)}<div class="dim small">peak ${nf(r.max_debit)}</div></td><td class="r">${nf(r.interest, 2)}</td><td class="r">${nf(r.gst, 2)}</td><td class="r"><b>${nf(r.total, 2)}</b></td><td>${pill(r.status, r.status === 'To post' ? 'warn' : r.status.startsWith('Posted') ? 'good' : 'nt')}</td><td class="n dim">${esc(r.invoice || '')}</td></tr>`),
      { tall: true, empty: 'No overdue debit in this period.', foot: rows.length ? `<tfoot><tr><td colspan="3">Total</td><td class="r">${nf(sum(rows, r => num(r.interest)), 2)}</td><td class="r">${nf(sum(rows, r => num(r.gst)), 2)}</td><td class="r">${nf(sum(rows, r => num(r.total)), 2)}</td><td colspan="2"></td></tr></tfoot>` : '' })}
    <div class="pad"><span class="dim small">${iv.posting === 'daily' ? 'Posted every day on that day’s overdue debit, which includes the interest and GST already posted, so interest earns interest from the second day. Autopilot posts it at the end of each day (weekends with the next run); month end issues each client one tax invoice (SAC 997119) for the month.' : 'Simple interest on each day’s overdue debit (interest already charged does not earn interest). Posting debits the client, credits interest income and GST payable, and issues a tax invoice (SAC 997119). Periods already charged are never charged again.'}</span></div>`;
};

/* ---------- KYC & PMLA ---------- */
V.kyc = {
  load: async () => { const [v, stt] = await Promise.all([get(`/v1/compliance?date=${S.date}`), get(`/v1/compliance/stt?date=${S.date}`).catch(e => ({ error: e.message, rows: [] }))]); return { v, stt }; },
  html: d => {
    const v = d.v, c = v.counts, n = k => c[k] || 0;
    return ph('KYC & PMLA', 'Client risk under the PMLA rules and the exchange KYC circulars, periodic KYC updation (every 2 years for High risk, 8 for Medium, 10 for Low), KRA and CKYC status, and screening against SEBI and exchange debarment orders and UN sanctions lists. A PAN match blocks the client at once; a name match is raised for review. Lists, KRA status and CKYC numbers are loaded on the Files page.',
      `<button class="btn" data-act="go" data-id="files">Load a list or status file</button><button class="btn" data-act="dl" data-id="/v1/compliance/ckyc-request" data-name="ckyc_bulk_request.csv">CKYC bulk request</button><button class="btn pri" data-act="kraFile">KRA upload file</button>`) +
    `<div class="kpis">${kpi('High risk', n('Risk High'), `${n('Risk Medium')} medium · ${n('Risk Low')} low`, n('Risk High') ? 'warn' : '')}${kpi('Risk to review', n('Risk to review'), 'rules point to a higher category', n('Risk to review') ? 'warn' : 'good')}${kpi('List matches', n('List matches'), `${v.lists.length} entries screened`, n('List matches') ? 'bad' : 'good')}${kpi('Re-KYC overdue', n('Re-KYC Overdue'), `${n('Re-KYC Due soon')} due in 90 days`, n('Re-KYC Overdue') ? 'bad' : 'good')}${kpi('KRA not validated', n('KRA not validated'), 'not Validated / Registered', n('KRA not validated') ? 'warn' : 'good')}${kpi('No CKYC number', n('No CKYC number'), 'request in bulk', n('No CKYC number') ? 'warn' : 'good')}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('kycTab', [['risk', 'Client risk'], ['rekyc', 'Periodic KYC'], ['reg', 'KRA & CKYC'], ['lists', 'Debarred & sanctions'], ['stt', 'Turnover & STT recon']])}${['risk', 'rekyc', 'reg'].includes(UI.kycTab) ? `<input class="in" data-ui="kycQ" data-part="kyc-t" placeholder="Filter name or UCC" value="${esc(UI.kycQ)}">` : ''}</div></div><div id="kyc-t">${P['kyc-t'](d)}</div></div>`;
  }
};
P['kyc-t'] = d => {
  const v = d.v, q = UI.kycQ.trim().toLowerCase(), can = has('Compliance officer'), mk = has('Ops maker', 'Ops checker', 'Compliance officer');
  const rows = v.rows.filter(r => !q || (r.ucc + ' ' + r.name).toLowerCase().includes(q));
  const who = r => `<button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}`;
  const rk = x => pill(x || '—', x === 'High' ? 'bad' : x === 'Medium' ? 'warn' : 'good');
  if (UI.kycTab === 'rekyc') {
    const due = rows.filter(r => r.rekyc_state !== 'Current').sort((a, b) => (a.rekyc_due || '').localeCompare(b.rekyc_due || ''));
    return tbl([['Client'], ['Risk'], ['Last KYC'], ['Due'], ['State'], ['']], due.map(r => `<tr><td>${who(r)}</td><td>${rk(r.risk)}</td><td class="n">${fmtDate(r.kyc_date) || '—'}</td><td class="n">${fmtDate(r.rekyc_due) || '—'}</td><td>${pill(r.rekyc_state || 'No KYC date', r.rekyc_state === 'Overdue' ? 'bad' : 'warn')}</td><td>${mk ? `<button class="btn sm" data-act="reKYC" data-id="${r.ucc}">Record re-KYC</button>` : ''}</td></tr>`), { tall: true, empty: 'No client is due for periodic KYC updation in the next 90 days.' }) + '<div class="pad"><span class="dim small">Recording re-KYC restarts the clock from that date. Done online (KRA re-fetch or DigiLocker), by email confirmation, or in person; raising the risk category shortens the cycle.</span></div>';
  }
  if (UI.kycTab === 'reg') return tbl([['Client'], ['PAN'], ['KRA status'], ['CKYC number'], ['KYC date']], rows.map(r => `<tr><td>${who(r)}</td><td class="n">${esc(r.pan)}</td><td>${pill(r.kra_status || 'Not loaded', ['Validated', 'Registered'].includes(r.kra_status) ? 'good' : r.kra_status ? 'bad' : 'nt')}</td><td class="n">${r.ckyc ? esc(r.ckyc) : '<span class="dim">—</span>'}</td><td class="n">${fmtDate(r.kyc_date) || '—'}</td></tr>`), { tall: true, empty: 'No active clients.' }) + '<div class="pad"><span class="dim small">KRA upload file: clients opened or changed since a date, for upload to the KRA. CKYC bulk request: clients without a CKYC number, for download from CERSAI. Load the KRA status file and CKYC numbers back on the Files page.</span></div>';
  if (UI.kycTab === 'lists') {
    const hits = v.rows.filter(r => r.debarred.length);
    return `${hits.length ? `<div class="bd">${hits.map(r => `<div class="note ${r.blocked ? 'bad' : 'warn'}" style="margin-bottom:6px">${who(r)} · ${esc(r.pan)} matches ${r.debarred.map(esc).join('; ')}${r.blocked ? ' · <b>blocked</b>' : ' · name only: review and confirm or clear'}</div>`).join('')}</div>` : '<div class="bd"><div class="note">No active client matches the lists in force.</div></div>'}` +
      tbl([['Name'], ['PAN'], ['Regulator'], ['Order'], ['Dated'], ['Till']], v.lists.map(l => `<tr><td>${esc(l.name)}</td><td class="n">${esc(l.pan || '—')}</td><td>${esc(l.regulator)}</td><td class="n">${esc(l.order_no)}</td><td class="n">${fmtDate(l.order_date)}</td><td class="n">${l.till ? fmtDate(l.till) : 'Further orders'}</td></tr>`), { tall: true, empty: 'No list loaded. Load a DEBARRED-LIST file on the Files page.' });
  }
  if (UI.kycTab === 'stt') {
    const t = d.stt;
    return t.error ? `<div class="bd">${errHTML({ message: t.error })}</div>` : tbl([['Exchange segment'], ['Turnover billed', 'r'], ['Turnover per exchange', 'r'], ['Difference', 'r'], ['STT billed', 'r'], ['STT per exchange', 'r'], ['Difference', 'r'], ['Status']], t.rows.map(r => `<tr><td>${esc(r.exchange)}-${esc(r.segment)}</td><td class="r">${nf(r.our_turnover, 2)}</td><td class="r">${r.has_statement ? nf(r.exchange_turnover, 2) : '—'}</td><td class="r ${num(r.diff_turnover) ? 'neg' : ''}">${r.has_statement ? nf(r.diff_turnover, 2) : '—'}</td><td class="r">${nf(r.our_stt, 2)}</td><td class="r">${r.has_statement ? nf(r.exchange_stt, 2) : '—'}</td><td class="r ${num(r.diff_stt) ? 'neg' : ''}">${r.has_statement ? nf(r.diff_stt, 2) : '—'}</td><td>${pill(r.status, r.status === 'Matched' ? 'good' : r.status === 'Difference' ? 'bad' : 'nt')}</td></tr>`), { empty: 'No trades on this date.' }) + '<div class="pad"><span class="dim small">Billed figures are from bills in force for the date (rebills included). Load the exchange’s STT-STATEMENT file to compare; a difference of more than ₹1 is flagged.</span></div>';
  }
  const f = UI.kycF;
  const shown = rows.filter(r => f === 'All' || (f === 'Review' ? r.review : r.risk === f));
  return `<div class="pad" style="padding-bottom:0">${segBtns('kycF', ['All', 'Review', 'High', 'Medium', 'Low'], 'kyc-t')}</div>` + tbl([['Client'], ['Type'], ['Risk'], ['Rules suggest'], ['Special category'], ['Turnover this FY', 'r'], ['']], shown.map(r => `<tr><td>${who(r)}${r.blocked ? ' ' + pill('Blocked', 'bad') : ''}</td><td>${esc(r.type)}</td><td>${rk(r.risk)}</td><td>${r.review ? rk(r.suggested) + `<div class="dim small wrap">${r.why.map(esc).join('; ')}</div>` : `<span class="dim">${r.suggested === r.risk ? 'Same' : 'Not higher'}</span>`}</td><td class="wrap">${r.csc.map(x => `<span class="tag">${esc(x)}</span>`).join(' ') || '<span class="dim">—</span>'}</td><td class="r">${inr(r.turnover)}</td><td>${can ? `<button class="btn sm" data-act="setRisk" data-id="${r.ucc}">Set risk</button>` : ''}</td></tr>`), { tall: true, empty: 'No clients match.' });
};
function riskModal(ucc) {
  const d = C.kyc, r = d.v.rows.find(x => x.ucc === ucc), csc = new Set(r.csc);
  modal(mhd('Client risk · ' + esc(r.name), `${r.ucc} · rules suggest ${r.suggested}${r.why.length ? ': ' + r.why.map(esc).join('; ') : ''}`) + `<form class="bd" id="rkf">
    <div class="fld"><label>Risk category</label><div class="seg" id="rk-r">${['Low', 'Medium', 'High'].map(x => `<button type="button" class="${x === r.risk ? 'on' : ''}" data-v="${x}">${x}</button>`).join('')}</div></div>
    <div class="fld"><label>Client of special category (CSC)</label><div class="tools">${d.v.csc.map(x => `<label class="check"><input type="checkbox" data-csc="${esc(x)}" ${csc.has(x) ? 'checked' : ''}> ${esc(x)}</label>`).join('')}</div></div>
    <div class="fld"><label for="rk-note">Basis for the decision</label><input class="in" id="rk-note" required placeholder="e.g. Reviewed income proof and source of funds"></div>
    <div id="rk-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
  let risk = r.risk;
  $('rk-r').onclick = e => { const b = e.target.closest('button'); if (!b) return; risk = b.dataset.v; $('rk-r').querySelectorAll('button').forEach(x => x.classList.toggle('on', x === b)); };
  $('rkf').onsubmit = async e => { e.preventDefault(); try { await post(`/v1/clients/${encodeURIComponent(ucc)}/risk`, { risk, csc: [...document.querySelectorAll('[data-csc]:checked')].map(x => x.dataset.csc), note: $('rk-note').value }); closeModal(); toast('Risk saved'); render({ keep: true }); } catch (err) { $('rk-err').innerHTML = errHTML(err); } };
}
function reKYCModal(ucc) {
  const r = C.kyc.v.rows.find(x => x.ucc === ucc);
  modal(mhd('Record periodic KYC · ' + esc(r.name), `${r.ucc} · due ${fmtDate(r.rekyc_due)}`) + `<form class="bd" id="rkyf"><div class="form"><div class="fld"><label for="rky-d">Completed on</label><input class="in" type="date" id="rky-d" value="${S.date}"></div><div class="fld"><label for="rky-m">How</label><select class="in" id="rky-m">${['KRA re-fetch', 'DigiLocker / Aadhaar e-KYC', 'Email confirmation of no change', 'In person'].map(x => `<option>${x}</option>`).join('')}</select></div></div><div id="rky-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Record</button></div></form>`);
  $('rkyf').onsubmit = async e => { e.preventDefault(); try { await post(`/v1/clients/${encodeURIComponent(ucc)}/rekyc`, { date: $('rky-d').value, mode: $('rky-m').value }); closeModal(); toast('Re-KYC recorded'); render({ keep: true }); } catch (err) { $('rky-err').innerHTML = errHTML(err); } };
}
function kraModal() {
  modal(mhd('KRA upload file', 'Clients opened, or whose KYC details changed, on or after a date, in the KRA bulk upload layout.') + `<div class="bd"><div class="fld"><label for="kra-since">Since</label><input class="in" type="date" id="kra-since" value="${S.date.slice(0, 8)}01"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="kra-go">Download</button></div>`);
  $('kra-go').onclick = () => { const s = $('kra-since').value; closeModal(); download(`/v1/compliance/kra-upload?since=${s}`, `kra_upload_${s}.csv`); };
}


const waConsentLine = c => { const w = c.wa_consent; if (!w) return c.whatsapp ? `${pill('Opted in', 'good')} <span class="dim small">no consent record</span>` : `${pill('Not asked', 'nt')}`; return `${pill(w.on ? 'Opted in' : 'Opted out', w.on ? 'good' : 'nt')} <span class="dim small">${fmtDate((w.at || '').slice(0, 10))} · ${esc(w.source)}${w.ref ? ' · ' + esc(w.ref) : ''}</span>`; };
function waConsentModal(ucc, on) {
  confirmModal(on ? 'Record WhatsApp consent' : 'Record WhatsApp opt-out', `${ucc}. Record only what the client said, with a reference you can show later.`, `<div class="form"><div class="fld"><label for="wc-src">How the client told us</label><select class="in" id="wc-src">${['Recorded call', 'Signed form', 'Account opening form'].map(x => `<option>${x}</option>`).join('')}</select></div><div class="fld"><label for="wc-ref">Reference</label><input class="in" id="wc-ref" placeholder="Call recording ID, form number"></div></div>`, on ? 'Record consent' : 'Record opt-out', async () => { await post(`/v1/clients/${encodeURIComponent(ucc)}/whatsapp-consent`, { on, source: $('wc-src').value, ref: $('wc-ref').value }); closeModal(); toast(on ? 'Consent recorded' : 'Opt-out recorded'); if (S.view === 'messages') render({ keep: true }); else clientDrawer(ucc); });
}

/* ---------- online applications ---------- */
const APP_ST = { Draft: 'nt', Submitted: 'acc', Rejected: 'bad' };
V.apps = {
  load: () => get('/v1/applications'),
  html: d => {
    const link = `${location.origin}/open/${encodeURIComponent(S.me.tenant)}`, f = UI.apF, cnt = s => d.filter(a => a.status === s).length;
    const rows = d.filter(a => f === 'All' || a.status === f);
    return ph('Online applications', 'Individuals open an account from a link on their phone: mobile and email confirmed, PAN checked and the KRA searched, Aadhaar and PAN fetched from DigiLocker with their consent (so no in-person or video verification), bank account confirmed by a penny drop, photograph, signature, and the form signed by e-Sign. Applications whose checks all pass are activated by Autopilot when the Activate online applications policy is on Auto; the rest wait at KYC verification for a checker.', '') +
    `<div class="note">Share this link on your website, in emails and with authorised persons: <span class="mono">${esc(link)}</span>. Add <span class="mono">?ap=CODE</span> to map new clients to an authorised person.</div>
    <div class="kpis">${kpi('Submitted', cnt('Submitted'), 'waiting for verification', cnt('Submitted') ? 'warn' : '')}${kpi('In progress', cnt('Draft'), 'started, not yet signed')}${kpi('Rejected', cnt('Rejected'), 'with the reason sent')}${kpi('All', d.length, 'online applications')}</div>
    <div class="panel"><div class="hd">${segBtns('apF', ['Submitted', 'Draft', 'Rejected', 'All'])}</div><div id="apps-t">${tbl([['Application'], ['Applicant'], ['Started'], ['Steps'], ['Checks'], ['Status'], ['']], rows.map(a => `<tr><td class="n">${a.id}${a.ucc ? `<div><button class="linkish mono" data-act="client" data-id="${a.ucc}">${a.ucc}</button></div>` : ''}</td><td>${esc(a.name || '—')}<div class="dim small">${esc(a.mobile)} · ${esc(a.email)}${a.partner ? ' · AP ' + esc(a.partner) : ''}</div></td><td class="n">${fmtDate(a.created.slice(0, 10))}</td><td class="r">${(a.steps || []).length} of 9</td><td class="small">${['Passed', 'Failed', 'Check manually'].map(s => { const n = (a.checks || []).filter(c => c.status === s).length; return n ? pill(n + ' ' + s.toLowerCase(), s === 'Passed' ? 'good' : s === 'Failed' ? 'bad' : 'warn') : ''; }).join(' ')}</td><td>${pill(a.status, APP_ST[a.status])}</td><td><button class="btn sm" data-act="appOpen" data-id="${a.id}">Open</button></td></tr>`), { tall: true, empty: 'No applications here.' })}</div></div>`;
  }
};
function appModal(id) {
  const a = C.apps.find(x => x.id === id), ck = has('Ops checker', 'Compliance officer');
  modal(mhd(`${esc(a.name || 'Applicant')} · ${a.id}`, `${esc(a.status)}${a.ucc ? ' · client ' + a.ucc : ''} · started ${fmtDate(a.created.slice(0, 10))}`) + `<div class="bd">
    <div class="kv"><div><small>PAN</small><b class="mono">${esc(a.pan || '—')}</b></div><div><small>Date of birth</small><b>${esc(a.dob || '—')}</b></div><div><small>Mobile</small><b>${esc(a.mobile)} ${a.mobile_ok ? '✓' : ''}</b></div><div><small>Email</small><b>${esc(a.email)} ${a.email_ok ? '✓' : ''}</b></div><div><small>Address (${esc(a.addr_source || '—')})</small><b>${esc([a.address, a.city, a.state, a.pin].filter(Boolean).join(', ') || '—')}</b></div><div><small>Bank</small><b>${esc(a.bank || '—')} ${esc(a.ifsc || '')}</b></div><div><small>Segments</small><b>${esc((a.segments || []).join(', ') || '—')}</b></div><div><small>e-Signed</small><b>${a.esigned ? fmtDate(a.esigned.slice(0, 10)) + ' · ' + esc(a.esign_ref) : 'not yet'}</b></div></div>
    ${tbl([['Check'], ['Result'], ['Detail'], ['By']], (a.checks || []).map(c => `<tr><td>${esc(c.name)}</td><td>${pill(c.status, c.status === 'Passed' ? 'good' : c.status === 'Failed' ? 'bad' : 'warn')}</td><td class="wrap small">${esc(c.detail)}</td><td class="small">${esc(c.provider)}</td></tr>`), { empty: 'No checks yet.' })}
    ${tbl([['Document'], ['File'], ['Size', 'r'], ['']], (a.docs || []).map((x, i) => `<tr><td>${esc(x.kind)}</td><td class="small">${esc(x.name)}</td><td class="r">${nf(x.size / 1024)} KB</td><td><button class="btn sm" data-act="openBlob" data-id="/v1/applications/${a.id}/docs/${i}">Open</button></td></tr>`), { empty: 'No documents yet.' })}
    ${a.reason ? `<div class="note warn">Rejected: ${esc(a.reason)}</div>` : ''}
    ${ck && a.status !== 'Rejected' ? '<div class="fld"><label for="app-r">Reason, if rejecting (sent to the applicant)</label><input class="in" id="app-r"></div>' : ''}
  </div><div class="ft">${a.ucc ? `<button class="btn" data-act="client" data-id="${a.ucc}">Open client</button>` : ''}${ck && a.status !== 'Rejected' ? `<button class="btn bad" data-act="appReject" data-id="${a.id}">Reject</button>` : ''}<button class="btn" data-act="close">Close</button></div>`);
}

/* ---------- circulars and compliance calendar ---------- */
const CI_PRI = { High: 'bad', Medium: 'warn', Low: 'nt', Information: 'nt' }, CI_ST = { New: 'warn', 'Action needed': 'acc', Done: 'good', 'Not applicable': 'nt' };
V.circulars = {
  load: async () => { const [c, h] = await Promise.all([get(`/v1/circulars?date=${S.date}`), get('/v1/holidays').catch(() => null)]); c.hol = h; return c; },
  html: d => {
    const cs = d.circulars, open = cs.filter(c => c.status === 'New' || c.status === 'Action needed');
    const tasks = cs.flatMap(c => c.tasks.filter(t => t.status === 'Open').map(t => ({ ...t, c })));
    const role = S.me?.role, mine = tasks.filter(t => t.role === role || role === 'Admin' || role === 'Compliance officer');
    const due7 = d.duties.filter(x => x.active && x.next_due && (x.overdue || x.next_due <= addDaysJS(S.date, 7)));
    return ph('Circulars & calendar', 'Circulars from SEBI, the exchanges, clearing corporations and depositories, each sorted by area and priority with the actions it usually needs, an owner and a due date; and the firm’s recurring obligations with the evidence of each time they were met. Load circulars on Files or below; once the exchange connection is set up they arrive by themselves.',
      has('Compliance officer', 'Ops maker', 'Ops checker') ? '<input type="file" id="ci-file" accept=".csv,text/csv" hidden><button class="btn" data-act="ciUpload">Load circulars file</button>' : '') +
    `<div class="kpis">${kpi('To review', nf(cs.filter(c => c.status === 'New').length), 'new circulars', cs.some(c => c.status === 'New' && c.priority === 'High') ? 'bad' : '')}${kpi('Open actions', nf(tasks.length), nf(tasks.filter(t => t.due && t.due < S.date).length) + ' overdue', tasks.some(t => t.due && t.due < S.date) ? 'bad' : '')}${kpi('Your actions', nf(mine.length), esc(role || ''))}${kpi('Due in 7 days', nf(due7.length), nf(d.duties.filter(x => x.overdue).length) + ' overdue obligations', d.duties.some(x => x.overdue) ? 'bad' : due7.length ? 'warn' : 'good')}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('ciTab', [['circ', `Circulars · ${open.length} open`], ['tasks', `Actions · ${tasks.length}`], ['cal', 'Compliance calendar'], ['hol', `Exchange holidays${d.hol && d.hol.years.some(y => y.pending) ? ' · to approve' : ''}`]])}</div></div><div id="ci-t">${P['ci-t'](d)}</div></div>`;
  }
};
const addDaysJS = (d, n) => { const t = new Date(d + 'T00:00:00Z'); t.setUTCDate(t.getUTCDate() + n); return t.toISOString().slice(0, 10); };
P['ci-t'] = d => {
  if (UI.ciTab === 'hol') return holidaysHTML(d.hol);
  const comp = has('Compliance officer');
  if (UI.ciTab === 'cal') return tbl([['Obligation'], ['Authority'], ['How often'], ['Next due'], ['Owner'], ['Last done'], ['']], d.duties.filter(x => x.active).map(x => { const last = x.done[x.done.length - 1]; return `<tr><td style="max-width:420px"><b>${esc(x.title)}</b><div class="dim small">Keep: ${esc(x.evidence)}</div></td><td>${esc(x.authority)}</td><td>${esc(x.frequency)}</td><td class="n ${x.overdue ? 'neg' : ''}">${x.next_due ? fmtDate(x.next_due) : '—'}${x.overdue ? '<div class="small">overdue</div>' : ''}<div class="dim small">for ${esc(x.period || '')}</div></td><td class="small">${esc(x.role)}</td><td class="small wrap">${last ? `${fmtDate(last.at.slice(0, 10))} · ${esc(last.by)}${last.late ? ' ' + pill('late', 'warn') : ''}<div class="dim">${esc(last.evidence)}</div>` : '<span class="dim">never</span>'}</td><td>${has(x.role, 'Compliance officer') ? `<button class="btn sm" data-act="dutyDone" data-id="${x.id}">Mark done</button>` : ''}</td></tr>`; }), { tall: true }) + '<div class="pad"><span class="dim small">A starting list for a stockbroker: check each due date against the current circulars. Items are queued for their owner a week before they fall due.</span></div>';
  if (UI.ciTab === 'tasks') { const ts = d.circulars.flatMap(c => c.tasks.filter(t => t.status === 'Open').map(t => ({ ...t, c }))).sort((a, b) => (a.due || '9').localeCompare(b.due || '9'));
    return tbl([['Action'], ['Circular'], ['Owner'], ['Due'], ['']], ts.map(t => `<tr><td style="max-width:420px"><b>${esc(t.title)}</b></td><td class="small"><button class="linkish" data-act="ciOpen" data-id="${esc(t.c.id)}">${esc(t.c.source)} ${esc(t.c.ref)}</button><div class="dim">${esc(t.c.subject)}</div></td><td class="small">${esc(t.role)}</td><td class="n ${t.due && t.due < S.date ? 'neg' : ''}">${t.due ? fmtDate(t.due) : '—'}</td><td>${has(t.role, 'Compliance officer') ? `<button class="btn sm" data-act="ciTaskDone" data-id="${esc(t.c.id)}" data-t="${t.id}">Done</button>` : ''}</td></tr>`), { tall: true, empty: 'No open actions.' }); }
  const f = UI.ciF, q = UI.ciQ.trim().toLowerCase();
  const cs = d.circulars.filter(c => (f === 'All' || (f === 'Open' ? ['New', 'Action needed'].includes(c.status) : c.priority === f)) && (!q || (c.source + ' ' + c.ref + ' ' + c.subject + ' ' + c.areas.join(' ')).toLowerCase().includes(q)));
  return `<div class="hd"><div class="tools">${segBtns('ciF', ['Open', 'High', 'Medium', 'Information', 'All'], 'ci-t')}<input class="in" data-ui="ciQ" data-part="ci-t" placeholder="Filter subject, number, area" value="${esc(UI.ciQ)}"></div></div>` +
    tbl([['Circular'], ['Date'], ['Areas'], ['Priority'], ['Actions'], ['Status']], cs.map(c => { const op = c.tasks.filter(t => t.status === 'Open').length; return `<tr><td style="max-width:520px"><button class="linkish" data-act="ciOpen" data-id="${esc(c.id)}"><b>${esc(c.subject)}</b></button><div class="dim small">${esc(c.source)} ${esc(c.ref)}${c.department ? ' · ' + esc(c.department) : ''}${c.effective ? ' · effective ' + fmtDate(c.effective) : ''}</div></td><td class="n">${fmtDate(c.date)}</td><td class="small">${c.areas.map(a => `<span class="tag">${esc(a)}</span>`).join(' ')}</td><td>${pill(c.priority, CI_PRI[c.priority])}</td><td class="r">${c.tasks.length ? `${op} open of ${c.tasks.length}` : '—'}</td><td>${pill(c.status, CI_ST[c.status])}</td></tr>`; }), { tall: true, empty: d.circulars.length ? 'Nothing matches.' : 'No circulars yet. Load a CIRCULARS file (Source, Ref, Date, Subject, with optional Department, URL and Text).' });
};
function ciModal(id) {
  const c = C.circulars.circulars.find(x => x.id === id); if (!c) return; const comp = has('Compliance officer');
  modal(mhd(esc(c.subject), `${esc(c.source)} ${esc(c.ref)} · ${fmtDate(c.date)}${c.department ? ' · ' + esc(c.department) : ''}`) + `<div class="bd">
    <div class="tools">${pill(c.priority, CI_PRI[c.priority])} ${pill(c.status, CI_ST[c.status])} ${c.areas.map(a => `<span class="tag">${esc(a)}</span>`).join(' ')}${c.url ? ` <a class="btn sm" href="${esc(c.url)}" target="_blank" rel="noopener noreferrer">Open the circular</a>` : ''}</div>
    <div class="note"><b>Summary.</b> ${esc(c.summary)}${c.effective ? `<br><b>Effective:</b> ${fmtDate(c.effective)}` : ''}</div>
    ${c.note ? `<div class="dim small">Compliance note (${esc(c.by || '')}): ${esc(c.note)}</div>` : ''}
    ${tbl([['Action'], ['Owner'], ['Due'], ['Status'], ['']], c.tasks.map(t => `<tr><td class="wrap">${esc(t.title)}${t.evidence ? `<div class="dim small">${esc(t.by)}: ${esc(t.evidence)}</div>` : ''}</td><td class="small">${esc(t.role)}</td><td class="n">${t.due ? fmtDate(t.due) : '—'}</td><td>${pill(t.status, t.status === 'Done' ? 'good' : t.status === 'Open' ? 'warn' : 'nt')}</td><td>${t.status === 'Open' && has(t.role, 'Compliance officer') ? `<button class="btn sm" data-act="ciTaskDone" data-id="${esc(c.id)}" data-t="${t.id}">Done</button>${comp ? `<button class="btn sm" data-act="ciTaskDrop" data-id="${esc(c.id)}" data-t="${t.id}">Not needed</button>` : ''}` : ''}</td></tr>`), { empty: 'No actions.' })}
    ${comp ? `<div class="form"><div class="fld full"><label for="ci-title">Add an action</label><input class="in" id="ci-title" placeholder="What has to be done"></div><div class="fld"><label for="ci-role">Owner</label><select class="in" id="ci-role">${['Compliance officer', 'Ops checker', 'Ops maker', 'Risk manager', 'Admin'].map(r => `<option>${r}</option>`).join('')}</select></div><div class="fld"><label for="ci-due">Due</label><input class="in" type="date" id="ci-due" value="${esc(c.effective || addDaysJS(S.date, 14))}"></div><div class="fld full"><label for="ci-note">Note</label><input class="in" id="ci-note" value="${esc(c.note || '')}" placeholder="How it applies to the firm, or why it does not"></div></div>` : ''}
  </div><div class="ft">${comp ? `<button class="btn" data-act="ciNA" data-id="${esc(c.id)}">Not applicable</button><button class="btn" data-act="ciAdd" data-id="${esc(c.id)}">Add action</button><button class="btn pri" data-act="ciReview" data-id="${esc(c.id)}">${c.status === 'New' ? 'Mark reviewed' : 'Save note'}</button>` : '<button class="btn" data-act="close">Close</button>'}</div>`);
}

/* ---------- clearing deposits ---------- */
V.allocation = {
  load: () => get(`/v1/allocation?date=${S.date}`),
  html: d => {
    const can = has('Ops checker', 'Risk manager'), mk = has('Ops maker', 'Ops checker', 'Risk manager');
    const st = { 'Awaiting approval': 'warn', Ready: 'acc', Sent: 'acc', Done: 'good', Failed: 'bad', Rejected: 'nt', Superseded: 'nt' };
    const ok = m => m.records.filter(r => r.code === '0100').length, bad = m => m.records.filter(r => r.code && r.code !== '0100').length;
    const conn = d.connection === 'Not set' ? pill('Key not set', 'bad') : pill('Key ' + d.connection.toLowerCase(), d.connection === 'Problem' ? 'bad' : 'good');
    return ph('Clearing allocation', 'Each client’s collateral allocation with NSE Clearing, through its allocation API. The amount sent is the client’s final allocation, not an addition, so Ledgerline builds every request on the level NSE Clearing last confirmed: money that comes in during the day is added to it and sent up, one request at a time per client; reductions go only in the start- or end-of-day batch that a second person approves. A partly accepted request stops further requests for that client until the level is confirmed.',
      (mk ? `<button class="btn" data-act="allocBatch" data-id="${AllocKinds[0]}">Prepare start-of-day batch</button><button class="btn" data-act="allocBatch" data-id="${AllocKinds[1]}">Prepare end-of-day batch</button>` : '') + (can ? '<button class="btn pri" data-act="allocCheck">Check results and send</button>' : '')) +
    `<div class="note">${conn} ${d.live ? pill('Sending on', 'good') : pill('Sending off: requests are prepared, not sent', 'warn')} ${d.auto_push ? pill('Money in sent as it is credited (' + esc(d.segment) + ')', 'acc') : pill('Money in not sent automatically', 'nt')} <span class="dim small">The consumer key and secret are in <a href="#connections" data-act="go" data-id="connections">Connections</a>; sending is switched on in <a href="#settings" data-act="go" data-id="settings">Settings</a>.</span></div>
    <div class="kpis">${kpi('Requests today', d.messages.filter(m => m.date === S.date).length, fmtDate(S.date))}${kpi('Awaiting approval', d.messages.filter(m => m.status === 'Awaiting approval').length, 'a second person approves', d.messages.some(m => m.status === 'Awaiting approval') ? 'warn' : '')}${kpi('Waiting for NSE Clearing', d.messages.filter(m => m.status === 'Sent').length, 'result not in yet')}${kpi('Money not yet sent', d.pending.length, inr(sum(d.pending, p => num(p.amount))), d.pending.some(p => p.held && !p.held.startsWith('Waiting')) ? 'warn' : '')}${kpi('Unconfirmed levels', d.unconfirmed, 'partly accepted', d.unconfirmed ? 'bad' : 'good')}</div>
    <div class="panel"><div class="hd"><h3>Requests</h3><small>Every request and NSE Clearing’s reply, kept by message ID</small></div>${tbl([['Message ID'], ['Kind'], ['Type'], ['Clients', 'r'], ['Accepted', 'r'], ['Not accepted', 'r'], ['Status'], ['Prepared / approved'], ['']], d.messages.map(m => `<tr><td class="mono"><button class="linkish mono" data-act="allocShow" data-id="${esc(m.id)}">${esc(m.id)}</button>${m.retry_of ? `<div class="dim small">resend of ${esc(m.retry_of)}</div>` : ''}</td><td>${esc(m.kind)}</td><td>${m.request_type === 'E' ? 'At end of day' : 'Immediate'}</td><td class="r">${m.records.length}</td><td class="r">${m.status === 'Done' ? ok(m) : '—'}</td><td class="r">${m.status === 'Done' ? bad(m) : '—'}</td><td>${pill(m.status, st[m.status])}${m.error ? `<div class="dim small">${esc(m.error)}</div>` : ''}</td><td class="small">${esc(m.prepared_by)}${m.approved_by ? ' · ' + esc(m.approved_by) : ''}</td><td><div class="split">${m.status === 'Awaiting approval' && can && m.prepared_by !== S.me.actor ? `<button class="btn sm pri" data-act="allocDecide" data-id="${esc(m.id)}" data-a="1">Approve and send</button><button class="btn sm" data-act="allocDecide" data-id="${esc(m.id)}" data-a="0">Reject</button>` : ''}<button class="btn sm" data-act="dl" data-id="/v1/allocation/${encodeURIComponent(m.id)}/file" data-name="NSCCL_${esc(m.id)}.json">JSON</button></div></td></tr>`), { tall: true, empty: 'No requests yet.' })}</div>
    <div class="grid g2"><div class="panel"><div class="hd"><h3>Money not yet sent</h3></div>${tbl([['Client'], ['Segment'], ['Amount', 'r'], ['Why']], d.pending.map(p => `<tr><td><button class="linkish mono" data-act="client" data-id="${p.ucc}">${p.ucc}</button></td><td>${esc(p.segment)}</td><td class="r">${inr(p.amount, 2)}</td><td class="small wrap">${esc(p.held || 'Goes in the next request')}</td></tr>`), { empty: 'Nothing waiting.' })}</div>
    <div class="panel"><div class="hd"><div class="tools"><h3 style="margin-right:8px">Confirmed levels</h3><input class="in" data-ui="alQ" data-part="al-t" placeholder="Filter client" value="${esc(UI.alQ || '')}"></div>${can ? '<button class="btn sm" data-act="allocLevel">Confirm a level</button>' : ''}</div><div id="al-t">${P['al-t'](d)}</div></div></div>`;
  }
};
const AllocKinds = ['Start of day', 'End of day'];
P['al-t'] = d => { const q = (UI.alQ || '').trim().toUpperCase(); const ls = d.levels.filter(l => !q || l.ucc.includes(q)).sort((a, b) => (b.unconfirmed - a.unconfirmed) || a.ucc.localeCompare(b.ucc));
  return tbl([['Client'], ['Segment'], ['Allocated', 'r'], ['As of'], ['Source']], ls.slice(0, 300).map(l => `<tr><td><button class="linkish mono" data-act="client" data-id="${l.ucc}">${l.ucc}</button></td><td>${esc(l.segment)}</td><td class="r">${inr(l.amount, 2)}</td><td class="n dim">${esc((l.as_of || '').replace('T', ' ').slice(0, 16))}</td><td class="small">${l.unconfirmed ? pill('Unconfirmed', 'bad') + ' ' : ''}${esc(l.source)}${l.note ? `<div class="dim">${esc(l.note)}</div>` : ''}</td></tr>`), { tall: true, empty: 'No levels yet: load the N-MASS file on Exchange files, or send the start-of-day batch.' }); };
V.deposits = {
  load: () => get(`/v1/deposits?date=${S.date}`),
  html: d => {
    const ck = has('Ops checker'), act = d.deposits.filter(x => x.status === 'Active'), alerts = act.filter(x => x.alert);
    const exp = sum(d.by_cc, c => num(c.expiring_30d)), short = d.by_cc.filter(c => num(c.short) > 0);
    return ph('Clearing deposits', 'The firm’s collateral with each clearing corporation: cash, fixed deposits lien-marked to it, bank guarantees and securities, valued after haircut. At least half must be cash or cash equivalents. Maturities and expiries are queued 30 days ahead so the trading limit never lapses.',
      `${ck ? '<button class="btn pri" data-act="depEdit" data-id="">Record a deposit</button>' : ''}${has('Admin') ? '<button class="btn" data-act="depBMC">Base capital required</button>' : ''}`) +
    `<div class="kpis">${kpi('Collateral', cmp(d.total), 'after haircut, active')}${kpi('Client margin today', cmp(d.client_margin), 'required across clients')}${kpi('Maturing in 30 days', cmp(exp), 'renew or replace', exp ? 'warn' : 'good')}${kpi('Needs attention', nf(alerts.length + short.length), short.length ? short.map(c => c.cc).join(', ') + ' below base capital' : 'expiries, liens, shortfalls', alerts.length + short.length ? 'bad' : 'good')}</div>
    ${d.by_cc.length ? `<div class="panel"><div class="hd"><h3>By clearing corporation</h3></div>${tbl([['Clearing corporation'], ['Base capital required', 'r'], ['Collateral', 'r'], ['Cash and equivalents'], ['Shortfall', 'r'], ['Maturing in 30 days', 'r']], d.by_cc.map(c => `<tr><td><b>${esc(c.cc)}</b></td><td class="r">${num(c.bmc) ? inr(c.bmc) : '<span class="dim">not set</span>'}</td><td class="r">${inr(c.value)}</td><td>${num(c.value) ? pill(c.cash_pct.toFixed(0) + '%', c.cash_pct >= 50 ? 'good' : 'bad') : '—'}</td><td class="r ${num(c.short) ? 'neg' : ''}">${num(c.short) ? inr(c.short) : '—'}</td><td class="r">${num(c.expiring_30d) ? inr(c.expiring_30d) : '—'}</td></tr>`))}</div>` : ''}
    <div class="panel"><div class="hd"><h3>Deposits</h3></div>${tbl([['Deposit'], ['Clearing corporation'], ['Amount', 'r'], ['Haircut', 'r'], ['Value', 'r'], ['Matures / expires'], ['Status'], ['']], d.deposits.map(x => `<tr><td><b>${esc(x.kind)}</b> <span class="mono small">${esc(x.ref || '')}</span><div class="dim small">${esc(x.issuer || '')} · ${esc(x.purpose)}${x.kind === 'Fixed deposit' ? (x.lien ? ' · lien marked' : ' · <span class="neg">no lien</span>') : ''}</div>${x.alert && x.status === 'Active' ? `<div class="small neg">${esc(x.alert)}</div>` : ''}</td><td>${esc(x.cc)} <span class="tag">${esc(x.segment)}</span></td><td class="r">${inr(x.amount)}</td><td class="r">${num(x.haircut_pct) ? nf(x.haircut_pct, 1) + '%' : '—'}</td><td class="r">${inr(x.value)}</td><td class="n ${x.status === 'Active' && x.days_left < 0 ? 'neg' : ''}">${x.expiry ? fmtDate(x.expiry) : '—'}${x.status === 'Active' && x.expiry ? `<div class="dim small">${x.days_left} days</div>` : ''}</td><td>${pill(x.status, x.status === 'Active' ? (x.alert ? 'warn' : 'good') : 'nt')}${x.note ? `<div class="dim small wrap">${esc(x.note)}</div>` : ''}</td><td>${ck && x.status === 'Active' ? `<button class="btn sm" data-act="depEdit" data-id="${x.id}">Edit</button><button class="btn sm" data-act="depRelease" data-id="${x.id}">Renewed / released</button>` : ''}</td></tr>`), { tall: true, empty: 'No deposits recorded yet.' })}</div>`;
  }
};
function depModal(id) {
  const x = C.deposits.deposits.find(y => y.id === id) || { cc: 'NSE Clearing', segment: 'ALL', kind: 'Fixed deposit', purpose: 'Additional collateral', issuer: '', ref: '', amount: '', haircut_pct: '0', issued: S.date, expiry: '', lien: true };
  confirmModal(id ? 'Edit deposit ' + id : 'Record a deposit', 'Fixed deposits and bank guarantees need the bank, the number and the maturity or expiry date.', `<div class="form">
    <div class="fld"><label for="dp-cc">Clearing corporation</label><select class="in" id="dp-cc">${C.deposits.ccs.map(c => `<option ${c === x.cc ? 'selected' : ''}>${c}</option>`).join('')}</select></div>
    <div class="fld"><label for="dp-seg">Segment</label><select class="in" id="dp-seg">${['ALL', 'CM', 'FO', 'CD', 'COM'].map(c => `<option ${c === x.segment ? 'selected' : ''}>${c}</option>`).join('')}</select></div>
    <div class="fld"><label for="dp-k">Kind</label><select class="in" id="dp-k">${C.deposits.kinds.map(c => `<option ${c === x.kind ? 'selected' : ''}>${c}</option>`).join('')}</select></div>
    <div class="fld"><label for="dp-p">Purpose</label><select class="in" id="dp-p">${['Base minimum capital', 'Additional collateral'].map(c => `<option ${c === x.purpose ? 'selected' : ''}>${c}</option>`).join('')}</select></div>
    <div class="fld"><label for="dp-i">Bank or issuer</label><input class="in" id="dp-i" value="${esc(x.issuer)}"></div>
    <div class="fld"><label for="dp-r">FD / BG number or ISIN</label><input class="in mono" id="dp-r" value="${esc(x.ref)}"></div>
    <div class="fld"><label for="dp-a">Amount (₹)</label><input class="in mono" id="dp-a" inputmode="decimal" value="${esc(String(x.amount))}"></div>
    <div class="fld"><label for="dp-h">Haircut (%)</label><input class="in mono" id="dp-h" inputmode="decimal" value="${esc(String(x.haircut_pct))}"></div>
    <div class="fld"><label for="dp-is">Issued</label><input class="in" type="date" id="dp-is" value="${esc(x.issued || '')}"></div>
    <div class="fld"><label for="dp-x">Matures / expires</label><input class="in" type="date" id="dp-x" value="${esc(x.expiry || '')}"></div>
    <label class="check full"><input type="checkbox" id="dp-l" ${x.lien ? 'checked' : ''}> Lien marked in favour of the clearing corporation</label></div>`, 'Save',
    () => act(() => post('/v1/deposits', { id: id || '', cc: $('dp-cc').value, segment: $('dp-seg').value, kind: $('dp-k').value, purpose: $('dp-p').value, issuer: $('dp-i').value.trim(), ref: $('dp-r').value.trim(), amount: $('dp-a').value.trim(), haircut_pct: $('dp-h').value.trim() || '0', issued: $('dp-is').value, expiry: $('dp-x').value, lien: $('dp-l').checked }), 'Deposit saved'));
}

/* ---------- year-end packs ---------- */
const fyLabel = d => { let y = +d.slice(0, 4); if (d.slice(5, 7) < '04') y--; return `${y}-${String((y + 1) % 100).padStart(2, '0')}`; };
V.yearend = {
  load: () => get(`/v1/year-end?date=${S.date}${UI.yeFY ? '&fy=' + UI.yeFY : ''}`),
  html: d => {
    const can = has('Ops maker', 'Ops checker', 'Compliance officer');
    return ph('Year-end packs', 'One statement per client and financial year for their tax return: capital gains, futures and options result, charges paid, closing balance and holdings, and the securities transaction tax particulars for Form No. 10DB (rule 20AB), one form per exchange. Clients open it in the portal; emailing tells them it is ready.',
      `<select class="in sm" id="ye-fy">${d.years.map(y => `<option ${y === d.fy ? 'selected' : ''}>${y}</option>`).join('')}</select>${can ? `<button class="btn pri" data-act="yeSendAll" ${d.running ? 'disabled title="After 31 March"' : ''}>Email all clients</button>` : ''}`) +
    (d.running ? `<div class="note">FY ${esc(d.fy)} is still running: packs show figures to ${fmtDate(d.to)} and can be emailed after 31 March.</div>` : '') +
    `<div class="kpis">${kpi('Clients', nf(d.rows.length), 'active')}${kpi('STT collected', cmp(d.stt), `FY ${esc(d.fy)}${d.running ? ' to date' : ''}`)}${kpi('Packs emailed', nf(d.sent), `of ${d.rows.length}`, d.sent && d.sent === d.rows.length ? 'good' : '')}${kpi('No email address', nf(d.rows.filter(r => !r.email).length), 'see the pack in the portal only', d.rows.some(r => !r.email) ? 'warn' : '')}</div>
    <div class="panel"><div class="hd"><input class="in" data-ui="yeQ" data-part="ye-t" placeholder="Filter client" value="${esc(UI.yeQ)}"></div><div id="ye-t">${P['ye-t'](d)}</div></div>`;
  }
};
P['ye-t'] = d => { const q = UI.yeQ.trim().toLowerCase(), can = has('Ops maker', 'Ops checker', 'Compliance officer');
  return tbl([['Client'], ['STT trades', 'r'], ['Value', 'r'], ['STT', 'r'], ['Emailed'], ['']], d.rows.filter(r => !q || (r.ucc + ' ' + r.name).toLowerCase().includes(q)).map(r => `<tr><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}</td><td class="r">${nf(r.trades)}</td><td class="r">${cmp(r.value)}</td><td class="r">${inr(r.stt, 2)}</td><td>${r.sent_at ? fmtDate(r.sent_at.slice(0, 10)) : r.email ? '<span class="dim">not yet</span>' : '<span class="dim">no email</span>'}</td><td><button class="btn sm" data-act="yeOpen" data-id="${r.ucc}">Open</button>${can && !d.running && r.email ? `<button class="btn sm" data-act="yeSend" data-id="${r.ucc}">Email</button>` : ''}</td></tr>`), { tall: true, empty: 'No active clients.' }); };

/* ---------- client insights ---------- */
const INS_KINDS = { tax_harvest: 'Tax-loss harvesting', buyback: 'Buy-back', fo_expiry: 'Physical settlement', advance_tax: 'Advance tax', corp_action: 'Dividends & bonuses', idle_funds: 'Idle funds', pledge_margin: 'Margin from holdings', dormant: 'Turning inactive' };
const INS_ST = { Open: 'warn', Sent: 'acc', Called: 'acc', Acted: 'good', Dismissed: 'nt', Expired: 'nt' };
const insLive = x => ['Open', 'Sent', 'Called'].includes(x.status);
function taxPlanHTML(p) {
  const lots = (xs, gain) => tbl([['Scrip'], ['Qty', 'r'], ['Bought'], ['Cost', 'r'], ['Price', 'r'], [gain ? 'Gain' : 'Loss', 'r'], ['Term']], xs.map(l => `<tr><td>${esc(l.symbol)}</td><td class="r">${nf(l.qty)}</td><td class="n">${fmtDate(l.buy_date)}</td><td class="r">${nf(l.buy_price, 2)}</td><td class="r">${nf(l.ltp, 2)}</td><td class="r ${sgn(l.pnl)}">${inr(l.pnl)}</td><td>${l.term === 'Long' ? 'Long-term' : 'Short-term'}${l.long_on ? `<div class="dim small">long-term from ${fmtDate(l.long_on)}</div>` : ''}</td></tr>`), { empty: gain ? 'No unrealised gains.' : 'No holdings below their cost.' });
  const soon = p.gains.filter(g => g.turns_long), losses = num(p.st_loss) + num(p.lt_loss);
  return `<div class="kpis">${kpi('Short-term gains booked', inr(p.stcg), 'FY ' + esc(p.fy) + ' so far')}${kpi('Long-term gains booked', inr(p.ltcg), inr(p.exempt_left) + ' of the ₹1.25 L exemption unused')}${kpi('Losses that could be booked', inr(losses), `short-term ${inr(p.st_loss)} · long-term ${inr(p.lt_loss)}`, losses ? 'warn' : '')}${kpi('Estimated tax saving', inr(p.saving), `on this year’s gains: ${inr(p.tax_now)} → ${inr(p.tax_after)}`, num(p.saving) > 0 ? 'good' : '')}${kpi('Last day to sell', fmtDate(p.last_day), p.days_left >= 0 ? `${p.days_left} days left · settles by 31 Mar` : 'settles in the next year')}</div>
    ${num(p.gain_harvest) > 0 ? `<div class="note">Long-term gains of up to <b>${inr(p.gain_harvest)}</b> could still be booked this year within the ₹1.25 lakh exemption. Buying back resets the cost to the new price.</div>` : ''}
    ${num(p.carry_forward) > 0 ? `<div class="note">Losses beyond this year’s gains, about <b>${inr(p.carry_forward)}</b>, can be carried forward for 8 years if the return is filed on time.</div>` : ''}
    <h3 class="sub">Holdings below cost</h3>${lots(p.losses)}
    ${soon.length ? `<h3 class="sub">Short-term gains turning long-term soon</h3><p class="dim small" style="margin:0 0 8px">Taxed at 20% if sold now; at 12.5% above the exemption from the date shown.</p>${lots(soon, true)}` : ''}
    <ul class="dim small notes">${p.notes.map(n => `<li>${esc(n)}</li>`).join('')}</ul>`;
}
V.insights = {
  load: () => get(`/v1/insights?date=${S.date}`),
  html: d => {
    const live = d.items.filter(insLive), rate = d.sent ? Math.round(100 * d.acted / d.sent) : 0;
    const finder = has('Ops maker', 'Ops checker', 'Compliance officer');
    return ph('Client insights', 'Opportunities found in each client’s own account every evening: losses that could cut tax before the year ends, buy-backs above the market on shares they hold, stock derivatives that would settle by delivery, advance tax, dividends, idle funds, margin from holdings and accounts about to turn inactive. In Auto the client gets the approved message; in Suggest the client waits here for a call. Messages are information about the client’s account, never advice.',
      `${finder ? '<button class="btn" data-act="insFind">Find opportunities now</button>' : ''}${has('Compliance officer') ? '<button class="btn" data-act="insPolicies">Automations</button><button class="btn" data-act="go" data-id="messages">Message templates</button>' : ''}`) +
    `<div class="kpis">${kpi('Open opportunities', nf(live.length), cmp(d.value) + ' worth to clients', live.length ? 'warn' : '')}${kpi('Clients reached', nf(d.sent), 'messaged or called')}${kpi('Acted on', nf(d.acted), d.sent ? rate + '% of those reached' : 'within 15 days', d.acted ? 'good' : '')}${kpi('Turnover after insights', cmp(d.turnover), 'trades within 15 days')}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('insKind', [['All', `All · ${live.length}`], ...d.kinds.map(k => [k.id, `${INS_KINDS[k.id] || k.name} · ${d.counts['live:' + k.id] || 0}`])], 'ins-t')}</div></div>
      <div class="hd"><div class="tools">${segBtns('insSt', [['live', 'To work'], ['acted', 'Acted on'], ['closed', 'Dismissed or expired'], ['all', 'All']], 'ins-t')}<input class="in" data-ui="insQ" data-part="ins-t" placeholder="Filter client, scrip" value="${esc(UI.insQ)}"></div></div><div id="ins-t">${P['ins-t'](d)}</div></div>`;
  }
};
P['ins-t'] = d => {
  const q = UI.insQ.trim().toLowerCase(), can = has('Ops maker', 'Ops checker', 'Compliance officer', 'Branch staff', 'Authorised person');
  const st = { live: insLive, acted: x => x.status === 'Acted', closed: x => ['Dismissed', 'Expired'].includes(x.status), all: () => true }[UI.insSt];
  const xs = d.items.filter(x => st(x) && (UI.insKind === 'All' || x.kind === UI.insKind) && (!q || (x.ucc + ' ' + x.name + ' ' + x.title + ' ' + (x.symbols || []).join(' ')).toLowerCase().includes(q)));
  const mode = k => d.policies[k] === 'Auto' ? 'sent to the client automatically at day end' : d.policies[k] === 'Suggest' ? 'call list only' : 'switched off';
  return tbl([['Client'], ['Insight'], ['Worth to client', 'r'], ['Act by'], ['Status'], ['']], xs.map(x => `<tr><td><button class="linkish mono" data-act="client" data-id="${x.ucc}">${x.ucc}</button><div class="small">${esc(x.name)}</div></td>
    <td style="max-width:520px"><button class="linkish" data-act="insOpen" data-id="${esc(x.id)}"><b>${esc(x.title)}</b></button><div class="dim small wrap">${esc(x.detail)}</div><div class="dim small">${esc(INS_KINDS[x.kind] || x.kind)} · ${mode(x.kind)}</div></td>
    <td class="r">${num(x.value) ? inr(x.value) : '—'}</td><td class="n">${x.due ? fmtDate(x.due) : '—'}</td>
    <td>${pill(x.status, INS_ST[x.status])}${x.channels?.length ? `<div class="dim small">${esc(x.channels.join(', '))}${x.sent_on ? ' · ' + fmtDate(x.sent_on) : ''}</div>` : ''}${x.held && x.status === 'Open' ? `<div class="dim small wrap">Not sent: ${esc(x.held)}</div>` : ''}${x.status === 'Acted' ? `<div class="dim small">${fmtDate(x.acted_on)} · ${x.trades} trade${x.trades === 1 ? '' : 's'}, ${cmp(x.turnover)}</div>` : ''}${x.calls?.length ? `<div class="dim small">Last call: ${esc(x.calls[x.calls.length - 1].outcome)}</div>` : ''}</td>
    <td>${can && insLive(x) ? `${x.status === 'Open' ? `<button class="btn sm pri" data-act="insSend" data-id="${esc(x.id)}">Send</button>` : ''}<button class="btn sm" data-act="insOpen" data-id="${esc(x.id)}">Call</button>` : `<button class="btn sm" data-act="insOpen" data-id="${esc(x.id)}">Open</button>`}</td></tr>`), { tall: true, empty: d.items.length ? 'Nothing matches this filter.' : 'No insights yet. They are found every evening, or now with Find opportunities now.' });
};
async function insModal(id) {
  const x = C.insights.items.find(i => i.id === id); if (!x) return;
  const can = has('Ops maker', 'Ops checker', 'Compliance officer', 'Branch staff', 'Authorised person') && insLive(x);
  modal(mhd(esc(x.title), `${x.ucc} · ${esc(x.name)} · ${esc(INS_KINDS[x.kind] || x.kind)}`) + `<div class="bd">
    <p style="margin:0">${esc(x.detail)}</p>
    <div class="kv"><div><small>Worth to client</small><b>${num(x.value) ? inr(x.value) : '—'}</b></div><div><small>Act by</small><b>${x.due ? fmtDate(x.due) : '—'}</b></div><div><small>Status</small><b>${esc(x.status)}</b></div><div><small>Found</small><b>${fmtDate(x.found)}</b></div><div><small>Messaged</small><b>${x.sent_on ? fmtDate(x.sent_on) + ' · ' + esc((x.channels || []).join(', ')) : 'not yet'}</b></div>${x.status === 'Acted' ? `<div><small>Acted on</small><b>${fmtDate(x.acted_on)} · ${cmp(x.turnover)}</b></div>` : ''}</div>
    ${x.held && x.status === 'Open' ? `<div class="note warn">Not sent automatically: ${esc(x.held)}.</div>` : ''}
    ${x.calls?.length ? tbl([['When'], ['By'], ['Outcome'], ['Note']], x.calls.map(c => `<tr><td class="n">${esc(c.at.slice(0, 16).replace('T', ' '))}</td><td>${esc(c.by)}</td><td>${esc(c.outcome)}</td><td class="wrap">${esc(c.text || '')}</td></tr>`)) : ''}
    ${x.kind === 'tax_harvest' ? '<div id="ins-tax" class="dim small">Loading the client’s tax working…</div>' : ''}
    ${can ? `<div class="note">Talk about the client’s own figures. Do not recommend buying or selling a particular security unless the firm is registered as a research analyst or investment adviser.</div>
    <div class="form"><div class="fld"><label for="ic-o">Call outcome</label><select class="in" id="ic-o">${['Interested', 'Will act', 'No answer', 'Not interested', 'Already done'].map(o => `<option>${o}</option>`).join('')}</select></div><div class="fld full"><label for="ic-t">Note</label><input class="in" id="ic-t" placeholder="What the client said"></div></div>` : ''}
  </div><div class="ft">${can ? `<button class="btn" data-act="insDismiss" data-id="${esc(x.id)}">Dismiss</button>${x.status !== 'Acted' ? `<button class="btn" data-act="insSend" data-id="${esc(x.id)}">${x.sent_on ? 'Send again' : 'Send message'}</button>` : ''}<button class="btn pri" data-act="insCall" data-id="${esc(x.id)}">Log call</button>` : '<button class="btn" data-act="close">Close</button>'}</div>`);
  if (x.kind === 'tax_harvest') { try { const p = await get(`/v1/tax-plan?ucc=${x.ucc}&date=${S.date}`); const el = $('ins-tax'); if (el) { el.className = ''; el.innerHTML = taxPlanHTML(p); } } catch (e) { const el = $('ins-tax'); if (el) el.textContent = e.message; } }
}
V.pinsights = {
  load: async () => { const v = await portalLoad(); v.ins = await get(`/v1/portal/insights?date=${S.date}&ucc=${encodeURIComponent(v.account.ucc)}`); return v; },
  html: v => {
    const d = v.ins;
    return pHead(v, 'Tax & insights', 'Your capital gains this year, losses you could book before the year ends, offers on shares you hold and dates to watch. This is information from your account, not investment or tax advice.') +
    (d.items.length ? `<div class="panel"><div class="hd"><h3>For your attention</h3></div><div class="bd ins-list">${d.items.map(i => `<div class="ins"><small>${esc(INS_KINDS[i.kind] || i.kind)}${i.due ? ' · by ' + fmtDate(i.due) : ''}</small><b>${esc(i.title)}</b><span class="dim small">${esc(i.detail)}</span></div>`).join('')}</div></div>` : '') +
    (d.buybacks.length ? `<div class="panel"><div class="hd"><h3>Buy-back offers on your shares</h3></div>${tbl([['Company'], ['Offer price', 'r'], ['Market price', 'r'], ['Your free shares', 'r'], ['Category'], ['Entitlement', 'r'], ['Closes'], ['Tender']], d.buybacks.map(b => `<tr><td><b>${esc(b.symbol)}</b><div class="dim small">${esc(b.company)}</div></td><td class="r">${nf(b.price, 2)}</td><td class="r">${num(b.ltp) ? nf(b.ltp, 2) : '—'}</td><td class="r">${nf(b.free)}</td><td class="small">${esc(b.category)}</td><td class="r">${b.entitlement ? nf(b.entitlement) : '—'}</td><td class="n">${fmtDate(b.close)}</td><td>${b.open ? `<div class="tools"><input class="in sm mono" id="pt-${esc(b.id)}" inputmode="numeric" style="width:90px" value="${b.tendered || b.entitlement || ''}"><button class="btn sm pri" data-act="pTender" data-id="${esc(b.id)}">${b.tendered ? 'Change' : 'Tender'}</button></div>${b.tendered ? `<div class="dim small">${nf(b.tendered)} tendered; 0 cancels</div>` : ''}` : '<span class="dim small">opens later</span>'}</td></tr>`))}<div class="pad"><span class="dim small">Shares not accepted are returned to your demat. Since 1 October 2024 the buy-back amount is taxed as dividend in your hands and the cost of the shares accepted becomes a capital loss.</span></div></div>` : '') +
    `<div class="panel"><div class="hd"><h3>Capital gains and tax-loss harvesting · FY ${esc(d.tax.fy)}</h3><button class="btn sm" data-act="go" data-id="statements">Capital gains statement</button></div><div class="bd">${taxPlanHTML(d.tax)}</div></div>
    <div class="panel"><div class="hd"><h3>Year-end statements</h3></div><div class="bd"><p class="dim small" style="margin-top:0">For your tax return: capital gains, F&amp;O result, charges, closing balance and holdings, and the STT particulars for Form No. 10DB.</p><div class="tools">${[fyLabel(S.date), fyLabel(String(+S.date.slice(0, 4) - 1) + S.date.slice(4))].map(y => `<button class="btn" data-act="pYearEnd" data-id="${y}">FY ${y}${y === fyLabel(S.date) ? ' (to date)' : ''}</button>`).join('')}</div></div></div>
    <div class="panel"><div class="bd"><label class="check"><input type="checkbox" id="pins-off" ${d.off ? '' : 'checked'}> Send me insights about my account by email and WhatsApp</label><div class="dim small">Contract notes, statements and margin alerts are always sent.</div></div></div>`;
  }
};

/* ============================== client portal ============================== */
const pAcct = () => UI.pAcct || '';
const portalLoad = async () => { const v = await get(`/v1/portal?date=${S.date}&ucc=${encodeURIComponent(pAcct())}`); UI.pAcct = v.account.ucc; return v; };
function pHead(v, title, sub) {
  const sw = v.accounts.length > 1 ? `<div class="fld" style="min-width:240px"><label for="p-acct">Account</label><select class="in" id="p-acct">${v.accounts.map(a => `<option value="${a.ucc}" ${a.ucc === v.account.ucc ? 'selected' : ''}>${esc(a.name)} · ${a.ucc}</option>`).join('')}</select></div>` : `<span class="chip">${esc(v.account.name)} · <b>${v.account.ucc}</b></span>`;
  return ph(title, sub, sw);
}
const noteRow = n => `<tr><td class="n">${esc(n.no)}${n.revision ? ' ' + pill('Revised', 'acc') : ''}</td><td class="n">${fmtDate(n.date)}</td><td class="r">${n.trades}</td><td class="r ${sgn(n.net)}">${inr(n.net, 2)}</td><td>${pill(n.status === 'Sent' ? 'Emailed' : n.status === 'Bounced' ? 'Email bounced' : n.status === 'Paper' ? 'Sent by post' : 'Ready', n.status === 'Sent' || n.status === 'Paper' ? 'good' : n.status === 'Bounced' ? 'bad' : 'nt')}</td><td><button class="btn sm" data-act="pNote" data-id="${n.date}|${n.ucc}|${n.part || ''}">Open</button>${n.dispatch ? `<button class="btn sm" data-act="dl" data-id="/v1/portal/documents/${n.dispatch}" data-name="ContractNote_${esc(n.no.replace(/[\/ ]/g, '-'))}.pdf" title="The digitally signed copy">Signed PDF</button>` : ''}</td></tr>`;
V.portal = {
  load: portalLoad,
  html: v => {
    const w = v.withdrawable || {}, m = v.margin;
    return pHead(v, 'Hello, ' + esc(v.account.name.split(' ')[0]), `Your account with ${esc(v.firm.name)} as at ${fmtDate(S.date)}. Figures include today’s trades once the bills are posted.`) +
    (v.account.inactive ? '<div class="note warn">This account is marked inactive because there was no activity for a long time. To trade again, raise a reactivation request from the Help desk; you will be asked to confirm your details.</div>' : '') +
    `<div class="kpis">${kpi('Ledger balance', inr(v.ledger, 2), num(v.ledger) < 0 ? 'you owe this amount' : 'credit with us', num(v.ledger) < 0 ? 'bad' : '')}${kpi('Available to withdraw', inr(w.available, 2), w.blocked ? esc(w.blocked_why) : 'after margin and pending items', num(w.available) > 0 ? 'good' : '')}${kpi('Holdings value', inr(v.holdings_value), v.holdings.length + ' scrips in your demat')}${m ? kpi('Margin used', inr(m.required), 'of ' + inr(num(m.funds) + num(m.collateral_usable)) + ' available') : ''}${kpi('Open positions', v.positions.length, 'derivatives and intraday')}</div>
    <div class="panel"><div class="hd"><h3>Holdings</h3><button class="btn sm" data-act="go" data-id="statements">Holding statement</button></div>${tbl([['Scrip'], ['Qty', 'r'], ['Pledged', 'r'], ['Price', 'r'], ['Value', 'r']], v.holdings.map(h => `<tr><td>${esc(h.symbol)} <span class="dim small">${esc(h.isin)}</span></td><td class="r">${nf(h.qty)}</td><td class="r">${h.pledged ? nf(h.pledged) : '—'}</td><td class="r">${nf(h.ltp, 2)}</td><td class="r">${inr(h.value)}</td></tr>`), { empty: 'No holdings.' })}</div>
    <div class="panel"><div class="hd"><h3>Recent ledger</h3><button class="btn sm" data-act="go" data-id="statements">Full statement</button></div>${tbl([['Date'], ['Particulars'], ['Debit', 'r'], ['Credit', 'r'], ['Balance', 'r']], v.ledger_lines.slice().reverse().map(l => `<tr><td class="n dim">${fmtDate(l.value_date)}</td><td class="wrap">${esc(l.memo || l.narration)}</td><td class="r">${num(l.debit) ? nf(l.debit, 2) : ''}</td><td class="r">${num(l.credit) ? nf(l.credit, 2) : ''}</td><td class="r">${drcr(l.balance)}</td></tr>`), { empty: 'No entries.' })}</div>
    ${v.positions.length ? `<div class="panel"><div class="hd"><h3>Open positions</h3></div>${tbl([['Contract'], ['Net qty', 'r'], ['Avg', 'r'], ['LTP', 'r'], ['MTM', 'r']], v.positions.map(p => `<tr><td>${esc(p.name)}</td><td class="r ${sgn(p.qty)}">${nf(p.qty)}</td><td class="r">${nf(p.avg_price, 2)}</td><td class="r">${nf(p.ltp, 2)}</td><td class="r ${sgn(p.mtm)}">${nf(p.mtm)}</td></tr>`))}</div>` : ''}
    <div class="panel"><div class="hd"><h3>Latest contract notes</h3><button class="btn sm" data-act="go" data-id="pnotes">All notes</button></div>${tbl([['Contract note'], ['Trade date'], ['Trades', 'r'], ['Net', 'r'], ['Status'], ['']], v.notes.slice(0, 5).map(noteRow), { empty: 'No contract notes yet.' })}</div>
    <div class="note">Grievances: write to us from the Help desk (we reply within 21 days), or to ${esc(v.firm.compliance_email || 'our compliance officer')}. If you are not satisfied you can approach SEBI through SCORES (scores.sebi.gov.in) or the exchanges. ${esc(v.firm.name)} · SEBI registration ${esc(v.firm.sebi_reg || '—')}.</div>`;
  }
};
V.pdocs = { load: portalLoad, html: v => pHead(v, 'Statements', 'Your contract notes and statements as digitally signed PDFs, newest first: daily margin statements, running account settlements, demat (DP) statements and monthly statements of account. Open a PDF in Adobe Reader to check the firm’s signature.') + `<div class="panel">${tbl([['Document'], ['For'], ['Date'], ['Delivery'], ['']], (v.documents || []).map(x => `<tr><td><b>${esc(x.title)}</b></td><td class="n">${esc(x.ref)}</td><td class="n">${fmtDate(x.date)}</td><td>${pill(x.status === 'Sent' ? 'Emailed' : x.status.startsWith('Physical') ? 'By post' : x.status, x.status === 'Bounced' ? 'bad' : 'good')}</td><td>${x.signed ? `<button class="btn sm" data-act="dl" data-id="/v1/portal/documents/${x.id}" data-name="${esc(x.kind)}_${esc(x.ref.replace(/[^0-9A-Za-z-]/g, '_'))}.pdf">Signed PDF</button>` : ''}</td></tr>`), { tall: true, empty: 'No signed documents yet.' })}</div>` };
V.pnotes = { load: portalLoad, html: v => pHead(v, 'Contract notes', 'Every contract note issued to you, newest first. A revised note replaces the earlier note for that day; both are kept.') + `<div class="panel">${tbl([['Contract note'], ['Trade date'], ['Trades', 'r'], ['Net (₹)', 'r'], ['Status'], ['']], v.notes.map(noteRow), { tall: true, empty: 'No contract notes yet.' })}</div>` };
V.pfunds = {
  load: portalLoad,
  html: v => {
    const w = v.withdrawable || {}, st = { Requested: 'warn', Approved: 'acc', Paid: 'good', Rejected: 'bad', Failed: 'bad', Expired: 'nt', Cancelled: 'nt' };
    return pHead(v, 'Funds & payouts', 'Withdraw your free credit to your registered bank account. Requests received by the cut-off are paid the next working day. Add funds only from your registered bank account; payments from other accounts are returned.') +
    `<div class="grid2"><div class="panel"><div class="hd"><h3>Withdraw</h3></div><div class="bd"><div class="kv"><div><small>Ledger credit</small><b class="mono">${inr(w.ledger, 2)}</b></div><div><small>Margin kept</small><b class="mono">${inr(w.margin, 2)}</b></div><div><small>Uncleared cheques</small><b class="mono">${inr(w.uncleared, 2)}</b></div><div><small>Payouts in process</small><b class="mono">${inr(w.pending, 2)}</b></div><div><small>Available</small><b class="mono pos">${inr(w.available, 2)}</b></div></div>
      ${w.blocked ? `<div class="note warn">${esc(w.blocked_why)}</div>` : `<form id="pof" class="tools" style="margin-top:10px"><input class="in mono" id="po-amt" inputmode="decimal" placeholder="Amount in ₹" style="max-width:180px"><button class="btn pri" type="submit">Request payout</button><span class="dim small">to ${esc(v.account.bank.bank)} · ${esc(v.account.bank.account)}</span></form><div id="po-err"></div>`}</div></div>
    <div class="panel"><div class="hd"><h3>Add funds</h3></div><div class="bd"><p style="margin-top:0">Transfer by NEFT, RTGS, IMPS or UPI from <b>${esc(v.account.bank.bank)} · ${esc(v.account.bank.account)}</b> to:</p><div class="kv"><div><small>Account name</small><b>${esc(v.firm.name)} – Client Account</b></div><div><small>Account</small><b class="mono">${esc(v.firm.payin_account || 'shown on your welcome letter')}</b></div><div><small>Reference</small><b class="mono">${v.account.ucc}</b></div></div><p class="dim small">Credits are matched to your account automatically from our bank statement. Cheques are credited only once cleared.</p></div></div></div>
    <div class="panel"><div class="hd"><h3>Payout requests</h3></div>${tbl([['Request'], ['Date'], ['Amount', 'r'], ['Status'], ['Bank reference']], v.payouts.map(p => `<tr><td class="n">${p.id}</td><td class="n">${fmtDate(p.date)}</td><td class="r">${inr(p.amount, 2)}</td><td>${pill(p.status, st[p.status])}${p.reason ? `<div class="dim small">${esc(p.reason)}</div>` : ''}</td><td class="n">${esc(p.utr || '—')}</td></tr>`), { empty: 'No payout requests.' })}</div>`;
  }
};
V.pprofile = {
  load: async () => { const v = await portalLoad(); v.docs = await get(`/v1/clients/${v.account.ucc}/documents`).catch(() => []); return v; },
  html: v => {
    const a = v.account, pend = k => v.changes.find(c => c.kind === k && c.status === 'Pending');
    const row = (k, label, val) => `<tr><td>${label}</td><td>${val}</td><td>${pend(k) ? pill('Change pending', 'warn') : `<button class="btn sm" data-act="pChange" data-id="${k}">Change</button>`}</td></tr>`;
    return pHead(v, 'Profile & documents', 'Changes to your contact, address, bank or nomination details are checked by our head office before they take effect, and are reported to the KYC registration agency.') +
    `<div class="grid2"><div class="panel"><div class="hd"><h3>Your details</h3></div>${tbl([['Detail'], ['On record'], ['']], [
      `<tr><td>PAN</td><td class="mono">${esc(a.pan)}</td><td></td></tr>`, row('Email', 'Email', esc(a.email)), row('Mobile', 'Mobile', esc(a.mobile)), row('Address', 'City', esc(a.city)),
      row('Bank account', 'Bank account', `${esc(a.bank.bank)} · ${esc(a.bank.account)} · ${esc(a.bank.ifsc)}`), `<tr><td>Demat</td><td class="mono">${esc(a.demat.depository)} ${esc(a.demat.id)}</td><td></td></tr>`,
      row('Nomination', 'Nomination', nomSummary(a)), `<tr><td>Segments</td><td>${a.segments.map(s => `<span class="tag">${esc(a.segment_names[s] || s)}</span>`).join(' ')}</td><td></td></tr>`, `<tr><td>WhatsApp alerts</td><td>${a.whatsapp ? 'On' : 'Off'} <span class="dim small">· or message us BAL, HOLD, CN</span></td><td><button class="btn sm" data-act="pWhatsApp" data-on="${a.whatsapp ? 0 : 1}">${a.whatsapp ? 'Turn off' : 'Turn on'}</button></td></tr>`])}</div>
    <div class="panel"><div class="hd"><h3>Documents</h3></div>${tbl([['Kind'], ['File'], ['']], v.docs.map(x => `<tr><td>${esc(x.kind)}</td><td class="wrap">${esc(x.name)}<div class="dim small">${fmtDate(x.at.slice(0, 10))}</div></td><td><button class="btn sm" data-act="openBlob" data-id="/v1/clients/${a.ucc}/documents/${x.id}">Open</button></td></tr>`), { empty: 'No documents.' })}</div></div>
    <div class="panel"><div class="hd"><h3>Change requests</h3></div>${tbl([['Request'], ['Change'], ['New value'], ['Status'], ['Raised']], v.changes.map(c => `<tr><td class="n">${c.id}</td><td>${esc(c.kind)}</td><td class="wrap">${Object.entries(c.new).filter(([k]) => k !== 'account_masked').map(([k, x]) => esc(x)).join(' · ')}</td><td>${pill(c.status, c.status === 'Approved' ? 'good' : c.status === 'Rejected' ? 'bad' : 'warn')}${c.needs_otp && !c.verified_at && c.status === 'Pending' ? ` <button class="btn sm" data-act="pVerify" data-id="${c.id}">Enter code</button>` : ''}${c.reason ? `<div class="dim small">${esc(c.reason)}</div>` : ''}</td><td class="n dim">${fmtDate(c.at.slice(0, 10))}</td></tr>`), { empty: 'No change requests.' })}</div>`;
  }
};
const NOM_REL = ['Spouse', 'Son', 'Daughter', 'Father', 'Mother', 'Brother', 'Sister', 'Grandchild', 'Other'];
const NOM_ID = [['PAN', 'PAN'], ['DL', 'Driving licence'], ['AADHAAR', 'Aadhaar (last 4 digits)'], ['PASSPORT', 'Passport']];
function nomRow(n) {
  const o = (opts, v) => opts.map(x => { const [k, l] = Array.isArray(x) ? x : [x, x]; return `<option value="${esc(k)}" ${v === k ? 'selected' : ''}>${esc(l)}</option>`; }).join('');
  return `<div class="form nomrow" style="grid-template-columns:repeat(4,minmax(0,1fr));border:1px solid var(--line);border-radius:8px;padding:10px;margin-bottom:8px">
    <div class="fld" style="grid-column:span 2"><label>Full name</label><input class="in nm-name" value="${esc(n.name || '')}"></div>
    <div class="fld"><label>Relationship</label><select class="in nm-rel">${o(NOM_REL, n.relation)}</select></div>
    <div class="fld"><label>Share %</label><input class="in nm-share" inputmode="decimal" placeholder="equal" value="${esc(n.share || '')}"></div>
    <div class="fld"><label>Identity</label><select class="in nm-idt">${o(NOM_ID, n.id_type)}</select></div>
    <div class="fld"><label>Number</label><input class="in mono nm-id" value="${esc(n.id && !/X/.test(n.id) ? n.id : '')}" placeholder="${n.id ? esc(n.id) : ''}"></div>
    <div class="fld"><label>Date of birth (if under 18)</label><input class="in nm-dob" type="date" value="${esc(n.dob || '')}"></div>
    <div class="fld" style="align-self:end"><button type="button" class="linkish" data-nomdel>Remove</button></div>
    <div class="fld" style="grid-column:span 2"><label>Guardian (minor only)</label><input class="in nm-g" value="${esc(n.guardian || '')}"></div>
    <div class="fld" style="grid-column:span 2"><label>Guardian's relationship to the nominee</label><input class="in nm-gr" value="${esc(n.guardian_relation || '')}"></div></div>`;
}
function nomEditor(list) { return `<div class="full"><div id="nom-list">${(list.length ? list : [{}]).map(nomRow).join('')}</div><button type="button" class="btn sm" id="nom-add">Add a nominee</button> <span class="dim small">Up to 10. Leave the shares blank for equal shares. Identity numbers already on record must be typed again when changing the nomination.</span></div>`; }
function nomBind() {
  const add = $('nom-add'); if (!add) return;
  add.onclick = () => { const l = $('nom-list'); if (l.children.length >= 10) { toast('At most 10 nominees'); return; } l.insertAdjacentHTML('beforeend', nomRow({})); nomBind(); };
  document.querySelectorAll('[data-nomdel]').forEach(b => { b.onclick = () => { if (document.querySelectorAll('.nomrow').length > 1) b.closest('.nomrow').remove(); }; });
}
function nomRead() { return [...document.querySelectorAll('.nomrow')].map(r => { const g = c => r.querySelector('.nm-' + c).value.trim(); return { name: g('name'), relation: g('rel'), share: g('share'), id_type: g('idt'), id: g('id'), dob: g('dob'), guardian: g('g'), guardian_relation: g('gr') }; }); }
const nomState = c => (c.nominees || []).length ? 'Nominated' : c.nominee === 'Opted in' ? 'Nominee details missing' : c.nomination_on ? 'Opted out (declared)' : 'No declaration';
const nomSummary = c => (c.nominees || []).length ? c.nominees.map(n => `${esc(n.name)} <span class="dim">(${esc(n.relation)}, ${esc(n.share)}%)</span>`).join(', ') : esc(c.nominee) + (c.nominee_name ? ' · ' + esc(c.nominee_name) + ' <span class="dim">(details missing)</span>' : '');
const CHG_FIELDS = { Email: [['email', 'New email', 'email']], Mobile: [['mobile', 'New mobile', 'tel']], Address: [['city', 'City'], ['state', 'State']], 'Bank account': [['bank', 'Bank name'], ['ifsc', 'IFSC'], ['account', 'Account number']], Nomination: [] };
function changeModal(kind, ucc, staff) {
  const f = CHG_FIELDS[kind], proof = { 'Bank account': 'Bank proof', Address: 'Address proof' }[kind];
  modal(mhd('Change ' + kind.toLowerCase(), `${ucc} · checked by head office before it takes effect`) + `<form class="bd" id="chf"><div class="form">${f.map(([k, l, t]) => `<div class="fld"><label for="ch-${k}">${l}</label><input class="in" id="ch-${k}" type="${t || 'text'}" required></div>`).join('')}
    ${kind === 'Nomination' ? `<div class="fld"><label for="ch-nominee">Nomination</label><select class="in" id="ch-nominee"><option>Opted in</option><option>Opted out</option></select></div>${staff ? `<div class="fld"><label for="ch-mode">Declared by</label><select class="in" id="ch-mode"><option>Signed form</option><option>Video recorded</option><option>Online with e-Sign</option></select></div>` : '<div class="fld"></div>'}<div id="ch-noms" class="full">${nomEditor(UI.chNoms || [])}</div>` : ''}
    ${proof ? `<div class="fld full"><label for="ch-doc">${proof} (${kind === 'Bank account' ? 'cancelled cheque or bank statement' : 'Aadhaar, passport, utility bill'}) · PDF, JPEG or PNG</label><input class="in" type="file" id="ch-doc" accept="application/pdf,image/jpeg,image/png" required></div>` : ''}</div>
    <div id="ch-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Submit request</button></div></form>`);
  nomBind(); if ($('ch-nominee')) $('ch-nominee').onchange = e => { $('ch-noms').hidden = e.target.value !== 'Opted in'; };
  $('chf').onsubmit = async e => {
    e.preventDefault(); const values = {}; f.forEach(([k]) => values[k] = $('ch-' + k).value);
    if (kind === 'Nomination') { values.nominee = $('ch-nominee').value; if (values.nominee === 'Opted in') values.nominees = JSON.stringify(nomRead()); values.mode = staff ? $('ch-mode').value : 'Online with e-Sign'; }
    try {
      let doc = '';
      if (proof) { const file = $('ch-doc').files[0]; const d = await api('POST', `/v1/clients/${ucc}/documents?kind=${encodeURIComponent(proof)}&name=${encodeURIComponent(file.name)}`, file, true); doc = d.id; }
      const r = await post('/v1/changes', { ucc, kind, values, doc }); closeModal(); if (r.needs_otp) { (UI.devCodes ||= {})[r.id] = r.dev_code; render({ keep: true }); A.pVerify(r.id); return; } toast(`Request ${r.id} submitted`); render({ keep: true });
    } catch (err) { $('ch-err').innerHTML = errHTML(err); }
  };
}
V.phelp = {
  load: async () => { const [v, t] = await Promise.all([portalLoad(), get(`/v1/tickets?date=${S.date}`)]); v.t = t; return v; },
  html: v => pHead(v, 'Help desk', 'Raise a complaint, a service request or a question. Complaints are answered within 21 days; requests and questions within 7. You will be told by email when we reply.') +
    `<div class="tools" style="margin-bottom:12px"><button class="btn pri" data-act="pTicket">New request or complaint</button></div>` +
    (v.t.tickets.length ? v.t.tickets.map(t => ticketCard(t, true)).join('') : '<div class="panel"><div class="bd"><div class="note">You have not raised anything yet.</div></div></div>')
};
function ticketCard(t, client) {
  const st = { Open: 'warn', 'In progress': 'acc', 'Awaiting client': 'warn', Resolved: 'good', Closed: 'nt' };
  return `<div class="panel"><div class="hd"><div><h3 style="margin:0">${esc(t.subject)}</h3><small class="dim">${t.id} · ${esc(t.kind)} · ${esc(t.category)} · ${t.ucc} · raised ${fmtDate(t.opened_on)} · reply due ${fmtDate(t.due)}</small></div>${pill(t.status, st[t.status])}</div><div class="bd">
    ${t.messages.map(m => `<div class="msg ${m.client ? 'me' : ''}"><small class="dim">${m.client ? (client ? 'You' : 'Client') : (client ? 'Support' : esc(m.by))} · ${esc(m.at.slice(0, 16).replace('T', ' '))}</small><div>${esc(m.text)}</div></div>`).join('')}
    ${!client ? (t.internal || []).map(m => `<div class="msg int"><small class="dim">Internal note · ${esc(m.by)} · ${esc(m.at.slice(0, 16).replace('T', ' '))}</small><div>${esc(m.text)}</div></div>`).join('') : ''}
    ${t.status !== 'Closed' ? `<div class="tools" style="margin-top:8px"><input class="in" id="tr-${t.id}" placeholder="${client ? 'Write a reply' : 'Reply to the client, or an internal note'}" style="flex:1">${client ? `<button class="btn sm pri" data-act="tReply" data-id="${t.id}">Send</button>${t.status === 'Resolved' ? `<button class="btn sm" data-act="tClose" data-id="${t.id}">Close – resolved</button>` : ''}` : `<button class="btn sm" data-act="tReply" data-id="${t.id}" data-int="1">Internal note</button><button class="btn sm pri" data-act="tReply" data-id="${t.id}">Reply</button><button class="btn sm" data-act="tResolve" data-id="${t.id}">Reply &amp; resolve</button>`}</div>` : ''}
  </div></div>`;
}
function ticketModal(staff, kinds) {
  const ucc = staff ? '' : pAcct(), c = (staff ? C.helpdesk : C.phelp).t || C.helpdesk;
  modal(mhd(staff ? 'Record a complaint or request' : 'New request or complaint', staff ? 'Received by email, phone, SCORES, the exchanges or at a branch.' : 'Tell us what happened; include dates, amounts and reference numbers if you have them.') + `<form class="bd" id="tkf"><div class="form">
    ${staff ? `<div class="fld full"><label for="tk-u-q">Client</label>${cpick('tk-u', '', {})}</div>` : (C.phelp.accounts.length > 1 ? `<div class="fld"><label for="tk-u">Account</label><select class="in" id="tk-u">${C.phelp.accounts.map(a => `<option value="${a.ucc}" ${a.ucc === ucc ? 'selected' : ''}>${esc(a.name)} · ${a.ucc}</option>`).join('')}</select></div>` : `<input type="hidden" id="tk-u" value="${ucc}">`)}
    <div class="fld"><label for="tk-k">Type</label><select class="in" id="tk-k"><option>Complaint</option><option>Service request</option><option>Query</option></select></div>
    <div class="fld"><label for="tk-c">Category</label><select class="in" id="tk-c">${c.categories.map(x => `<option>${esc(x)}</option>`).join('')}</select></div>
    ${staff ? `<div class="fld"><label for="tk-s">Received from</label><select class="in" id="tk-s">${c.sources.map(x => `<option>${esc(x)}</option>`).join('')}</select></div><div class="fld"><label for="tk-ref">SCORES / exchange reference</label><input class="in" id="tk-ref"></div><div class="fld"><label for="tk-d">Received on</label><input class="in" type="date" id="tk-d" value="${S.date}"></div>` : ''}
    <div class="fld full"><label for="tk-sub">Subject</label><input class="in" id="tk-sub" required></div>
    <div class="fld full"><label for="tk-t">Details</label><textarea class="in" id="tk-t" rows="4" required></textarea></div></div>
    <div id="tk-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Submit</button></div></form>`);
  $('tkf').onsubmit = async e => {
    e.preventDefault();
    const b = { ucc: $('tk-u').value, kind: $('tk-k').value, category: $('tk-c').value, subject: $('tk-sub').value, text: $('tk-t').value };
    if (staff) Object.assign(b, { source: $('tk-s').value, ref: $('tk-ref').value, opened_on: $('tk-d').value });
    try { const t = await post('/v1/tickets', b); closeModal(); toast(`${t.id} raised · reply due ${fmtDate(t.due)}`); render({ keep: true }); } catch (err) { $('tk-err').innerHTML = errHTML(err); }
  };
}
V.pext = {
  load: async () => { const v = await portalLoad(); v.ext = await get(`/v1/external-trades?ucc=${v.account.ucc}`); return v; },
  html: v => pHead(v, 'Trades through other brokers', 'Add equity buys and sells you made through other brokers, so your capital gains report here covers all your trades. Buys made before your account opened here also give a cost to shares you transferred in.') +
    `<div class="panel"><div class="hd"><h3>Add a trade</h3></div><form class="bd" id="exf"><div class="form" style="grid-template-columns:repeat(4,1fr)">
      <div class="fld"><label for="ex-d">Trade date</label><input class="in" type="date" id="ex-d" required></div><div class="fld"><label for="ex-s">Scrip</label><input class="in" id="ex-s" placeholder="e.g. INFY" required></div><div class="fld"><label for="ex-i">ISIN (optional)</label><input class="in mono" id="ex-i"></div><div class="fld"><label for="ex-side">Buy / sell</label><select class="in" id="ex-side"><option value="B">Buy</option><option value="S">Sell</option></select></div>
      <div class="fld"><label for="ex-q">Quantity</label><input class="in" id="ex-q" inputmode="numeric" required></div><div class="fld"><label for="ex-p">Price (₹)</label><input class="in" id="ex-p" inputmode="decimal" required></div><div class="fld"><label for="ex-b">Broker</label><input class="in" id="ex-b" required></div><div class="fld" style="align-self:end"><button class="btn pri" type="submit">Add</button></div></div><div id="ex-err"></div></form></div>
    <div class="panel">${tbl([['Date'], ['Scrip'], ['Side'], ['Qty', 'r'], ['Price', 'r'], ['Value', 'r'], ['Broker'], ['']], v.ext.map(x => `<tr><td class="n">${fmtDate(x.date)}</td><td>${esc(x.symbol)} <span class="dim small">${esc(x.isin)}</span></td><td>${x.side === 'B' ? 'Buy' : 'Sell'}</td><td class="r">${nf(x.qty)}</td><td class="r">${nf(x.price, 2)}</td><td class="r">${nf(num(x.price) * x.qty, 2)}</td><td>${esc(x.broker)}</td><td><button class="btn sm" data-act="exDel" data-id="${x.id}">Remove</button></td></tr>`), { empty: 'No outside trades added.' })}</div>`
};

/* ---------- service desk (staff) ---------- */
V.helpdesk = {
  load: async () => { const m = UI.hdMonth || S.date.slice(0, 7); const [t, ds] = await Promise.all([get(`/v1/tickets?date=${S.date}`), get(`/v1/complaints/disclosure?month=${m}`)]); return { t, ds, m }; },
  html: d => {
    const ts = d.t.tickets, open = ts.filter(t => !['Resolved', 'Closed'].includes(t.status)), od = open.filter(t => t.overdue);
    return ph('Investor complaints', 'Complaints, service requests and queries from the portal, email, phone, branches, SEBI SCORES and the exchanges. Complaints are due within 21 calendar days, requests within 7. The monthly disclosure is the table brokers publish on their website by the 7th of each month.', has('Ops maker', 'Ops checker', 'Compliance officer') ? '<button class="btn pri" data-act="sTicket">Record a complaint</button>' : '') +
    `<div class="kpis">${kpi('Open', open.length, open.filter(t => t.kind === 'Complaint').length + ' complaints')}${kpi('Past due date', od.length, 'reply overdue', od.length ? 'bad' : 'good')}${kpi('From SCORES / exchanges', open.filter(t => t.source !== 'Directly from investors' && t.source !== 'Other sources').length, 'open')}${kpi('Received this month', d.ds.total.received, 'complaints')}${kpi('Average resolution', d.ds.total.avg_resolution_days + ' days', 'complaints resolved this month')}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('hdTab', [['open', 'Open'], ['all', 'All'], ['disc', 'Monthly disclosure']])}${UI.hdTab === 'disc' ? `<input class="in sm" type="month" id="hd-m" value="${d.m}"><button class="btn sm" data-act="hdMonth">Show</button>` : ''}</div></div>${P['hd-t'](d)}</div>`;
  }
};
P['hd-t'] = d => {
  if (UI.hdTab === 'disc') {
    const ds = d.ds, r = x => `<td class="r">${x.pending_at_start}</td><td class="r">${x.received}</td><td class="r">${x.resolved}</td><td class="r">${x.pending}</td><td class="r ${x.pending_over_3_months ? 'neg' : ''}">${x.pending_over_3_months}</td><td class="r">${x.avg_resolution_days}</td>`;
    return tbl([['Received from'], ['Pending at end of last month', 'r'], ['Received', 'r'], ['Resolved', 'r'], ['Total pending', 'r'], ['Pending > 3 months', 'r'], ['Average resolution (days)', 'r']], ds.rows.map(x => `<tr><td>${esc(x.source)}</td>${r(x)}</tr>`), { foot: `<tfoot><tr><td>Grand total</td>${r(ds.total)}</tr></tfoot>` }) +
      `<div class="grid2"><div>${tbl([['Month'], ['Carried forward', 'r'], ['Received', 'r'], ['Resolved', 'r'], ['Pending', 'r']], ds.trend.map(x => `<tr><td>${esc(x.month)}</td><td class="r">${x.carried_forward}</td><td class="r">${x.received}</td><td class="r">${x.resolved}</td><td class="r">${x.pending}</td></tr>`))}</div><div>${tbl([['Year'], ['Carried forward', 'r'], ['Received', 'r'], ['Resolved', 'r'], ['Pending', 'r']], ds.annual.map(x => `<tr><td>${esc(x.month)}</td><td class="r">${x.carried_forward}</td><td class="r">${x.received}</td><td class="r">${x.resolved}</td><td class="r">${x.pending}</td></tr>`))}</div></div>
      <div class="pad"><span class="dim small">Average resolution time is the mean of days from receipt to resolution of complaints resolved in the month. Only complaints are counted; service requests and queries are not.</span></div>`;
  }
  const ts = d.t.tickets.filter(t => UI.hdTab === 'all' || !['Resolved', 'Closed'].includes(t.status)), st = { Open: 'warn', 'In progress': 'acc', 'Awaiting client': 'warn', Resolved: 'good', Closed: 'nt' };
  return tbl([['Ticket'], ['Client'], ['Type'], ['Subject'], ['From'], ['Received'], ['Due'], ['Status'], ['With']], ts.map(t => `<tr><td class="n"><button class="linkish" data-act="tOpen" data-id="${t.id}">${t.id}</button></td><td><button class="linkish mono" data-act="client" data-id="${t.ucc}">${t.ucc}</button> ${esc(t.name)}</td><td>${esc(t.kind)}</td><td class="wrap">${esc(t.subject)}<div class="dim small">${esc(t.category)}</div></td><td class="small">${esc(t.source)}${t.ref ? `<div class="mono dim">${esc(t.ref)}</div>` : ''}</td><td class="n">${fmtDate(t.opened_on)}</td><td class="n ${t.overdue ? 'neg' : ''}">${fmtDate(t.due)}${t.overdue ? '<div class="small">overdue</div>' : ''}</td><td>${pill(t.status, st[t.status])}</td><td class="dim">${esc(t.assignee || '—')}</td></tr>`), { tall: true, empty: 'Nothing open.' });
};
function ticketDrawer(id) {
  const d = C.helpdesk, t = d.t.tickets.find(x => x.id === id);
  modal(mhd(esc(t.subject), `${t.id} · ${t.ucc} ${esc(t.name)}`) + `<div class="bd"><div class="tools"><label class="dim small">Status</label><select class="in sm" id="td-st">${d.t.statuses.map(s => `<option ${s === t.status ? 'selected' : ''}>${s}</option>`).join('')}</select><label class="dim small">With</label><select class="in sm" id="td-as"><option value="">—</option>${d.t.staff.map(s => `<option ${s === t.assignee ? 'selected' : ''}>${esc(s)}</option>`).join('')}</select><button class="btn sm" data-act="tSet" data-id="${t.id}">Update</button></div>${ticketCard(t, false)}</div>`, true);
}
V.changes = {
  load: () => get('/v1/changes'),
  html: d => {
    const pend = d.requests.filter(r => r.status === 'Pending'), rs = UI.chTab === 'all' ? d.requests : pend, ck = has('Ops checker');
    return ph('Change requests', 'Changes to client contact, address, bank and nomination details raised from the client portal, branches or head office. A checker who did not raise a request approves it; approved changes are applied, recorded in the audit trail, and go to the KRA in the next upload.', '') +
    `<div class="panel"><div class="hd"><div class="tools">${segBtns('chTab', [['pending', `Pending (${pend.length})`], ['all', 'All']])}</div></div>${tbl([['Request'], ['Client'], ['Change'], ['Now'], ['New'], ['Proof'], ['From'], ['Status'], ['']], rs.map(r => `<tr><td class="n">${r.id}</td><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}</td><td>${esc(r.kind)}</td><td class="wrap dim">${Object.values(r.old).filter(Boolean).map(esc).join(' · ')}</td><td class="wrap"><b>${Object.entries(r.new).filter(([k]) => k !== 'account_masked').map(([, x]) => esc(x)).join(' · ')}</b></td><td>${r.doc ? `<button class="btn sm" data-act="openBlob" data-id="/v1/clients/${r.ucc}/documents/${r.doc}">View</button>` : '—'}</td><td class="small">${esc(r.source)}<div class="dim">${esc(r.by)}</div></td><td>${pill(r.status, r.status === 'Approved' ? 'good' : r.status === 'Rejected' ? 'bad' : 'warn')}${r.reason ? `<div class="dim small">${esc(r.reason)}</div>` : ''}</td><td>${r.status === 'Pending' && ck ? `<button class="btn sm pri" data-act="chDecide" data-id="${r.id}" data-a="1">Approve</button><button class="btn sm" data-act="chDecide" data-id="${r.id}" data-a="0">Reject</button>` : ''}</td></tr>`), { tall: true, empty: 'No change requests.' })}</div>`;
  }
};

/* ---------- leads & CRM ---------- */
V.crm = {
  load: () => get(`/v1/crm?date=${S.date}&from=${UI.crmFrom || S.date.slice(0, 8) + '01'}&to=${UI.crmTo || S.date}`),
  html: d => {
    const q = UI.crmQ.trim().toLowerCase(), f = UI.crmStage;
    const ls = d.leads.filter(l => (f === 'All' || l.stage === f) && (!q || (l.name + ' ' + l.city + ' ' + l.id).toLowerCase().includes(q)));
    const sc = { New: 'nt', Contacted: 'acc', Interested: 'acc', 'Documents pending': 'warn', Converted: 'good', Lost: 'bad' };
    return ph('Leads & CRM', `Prospects from the website, referrals, walk-ins, campaigns and authorised persons, with calls, notes and appointments. Mobile numbers are masked; calls go through the telephony bridge. Each converted lead activated as a client earns its owner ${inr(d.incentive_per_client)}.`, '<button class="btn pri" data-act="newLead">New lead</button>') +
    `<div class="kpis">${['New', 'Contacted', 'Interested', 'Documents pending', 'Converted'].map(s => kpi(s, d.stages[s] || 0, s === 'Converted' ? 'all time' : 'open', s === 'Converted' ? 'good' : '')).join('')}${kpi('Follow-ups due', d.follow_ups_due, 'today or earlier', d.follow_ups_due ? 'warn' : 'good')}</div>
    ${d.today.length ? `<div class="panel"><div class="hd"><h3>Appointments today</h3></div>${tbl([['Time'], ['With'], ['Purpose'], ['Place']], d.today.map(a => `<tr><td class="n">${esc(a.at.slice(11))}</td><td>${esc(a.with)}</td><td>${esc(a.purpose)}</td><td>${esc(a.place || '—')}</td></tr>`))}</div>` : ''}
    <div class="panel"><div class="hd"><div class="tools">${segBtns('crmTab', [['pipe', 'Pipeline'], ['inc', 'Conversions & incentives']])}${UI.crmTab === 'pipe' ? `<select class="in sm" id="crm-stage">${['All', ...d.stage_list].map(s => `<option ${s === f ? 'selected' : ''}>${s}</option>`).join('')}</select><input class="in" data-ui="crmQ" placeholder="Filter name, city or ID" value="${esc(UI.crmQ)}">` : `<input class="in sm" type="date" id="crm-from" value="${esc(d.From)}"><span class="dim">to</span><input class="in sm" type="date" id="crm-to" value="${esc(d.To)}"><button class="btn sm" data-act="crmPeriod">Show</button>`}</div></div>
    ${UI.crmTab === 'inc' ? tbl([['Owner'], ['Leads', 'r'], ['Calls', 'r'], ['Appointments', 'r'], ['Converted', 'r'], ['Conversion', 'r'], ['Activated', 'r'], ['Incentive', 'r']], d.incentives.map(r => `<tr><td>${esc(r.owner)}</td><td class="r">${r.leads}</td><td class="r">${r.calls}</td><td class="r">${r.appointments}</td><td class="r">${r.converted}</td><td class="r">${esc(r.conversion_pct || '0.0')}%</td><td class="r">${r.activated}</td><td class="r"><b>${inr(r.incentive)}</b></td></tr>`), { empty: 'No leads created in this period.' }) + '<div class="pad"><span class="dim small">Leads created in the period. Incentive is earned when the converted client is activated.</span></div>'
      : tbl([['Lead'], ['Mobile'], ['City'], ['Source'], ['Owner'], ['Stage'], ['Next follow-up'], ['']], ls.map(l => `<tr><td><button class="linkish" data-act="leadOpen" data-id="${l.id}"><b>${esc(l.name)}</b></button> <span class="mono dim small">${l.id}</span>${l.interest.length ? `<div class="small dim">${l.interest.map(esc).join(', ')}</div>` : ''}</td><td class="mono">${esc(l.mobile)}</td><td>${esc(l.city || '—')}</td><td>${esc(l.source)}</td><td>${esc(l.owner)}</td><td>${pill(l.stage, sc[l.stage])}${l.ucc ? ` <button class="linkish mono" data-act="client" data-id="${l.ucc}">${l.ucc}</button>` : ''}</td><td class="n ${l.next_action && l.next_action <= S.date && !['Converted', 'Lost'].includes(l.stage) ? 'neg' : ''}">${l.next_action ? fmtDate(l.next_action) : '—'}</td><td>${!['Converted', 'Lost'].includes(l.stage) ? `<button class="btn sm" data-act="leadCall" data-id="${l.id}">Call</button>` : ''}</td></tr>`), { tall: true, empty: 'No leads.' })}</div>`;
  }
};
function leadModal() {
  modal(mhd('New lead', '') + `<form class="bd" id="ldf"><div class="form"><div class="fld"><label for="ld-n">Name</label><input class="in" id="ld-n" required></div><div class="fld"><label for="ld-m">Mobile</label><input class="in" id="ld-m" inputmode="tel" required></div><div class="fld"><label for="ld-e">Email</label><input class="in" id="ld-e" type="email"></div><div class="fld"><label for="ld-c">City</label><input class="in" id="ld-c"></div>
    <div class="fld"><label for="ld-s">Source</label><select class="in" id="ld-s">${C.crm.sources.map(s => `<option>${s}</option>`).join('')}</select></div><div class="fld"><label for="ld-r-q">Referred by client (optional)</label>${cpick('ld-r', '', { none: 'No referrer' })}</div>
    <div class="fld full"><label>Interested in</label><div class="tools">${['Equity', 'F&O', 'Commodity', 'Currency', 'Mutual funds', 'IPO', 'MTF'].map(x => `<label class="check"><input type="checkbox" data-int="${x}"> ${x}</label>`).join('')}</div></div><div class="fld"><label for="ld-nx">Follow up on</label><input class="in" type="date" id="ld-nx" value="${S.date}"></div></div>
    <div id="ld-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Add lead</button></div></form>`);
  $('ldf').onsubmit = async e => { e.preventDefault(); try { const l = await post('/v1/leads', { name: $('ld-n').value, mobile: $('ld-m').value, email: $('ld-e').value, city: $('ld-c').value, source: $('ld-s').value, referred_by: $('ld-r').value, interest: [...document.querySelectorAll('[data-int]:checked')].map(x => x.dataset.int), next_action: $('ld-nx').value }); closeModal(); toast(`${l.id} added`); render({ keep: true }); } catch (err) { $('ld-err').innerHTML = errHTML(err); } };
}
function leadDrawer(id) {
  const l = C.crm.leads.find(x => x.id === id), done = ['Converted', 'Lost'].includes(l.stage);
  modal(mhd(esc(l.name), `${l.id} · ${esc(l.source)} · owner ${esc(l.owner)}`) + `<div class="bd">
    <div class="kv"><div><small>Mobile</small><b class="mono">${esc(l.mobile)}</b></div><div><small>Email</small><b>${esc(l.email || '—')}</b></div><div><small>City</small><b>${esc(l.city || '—')}</b></div><div><small>Stage</small><b>${esc(l.stage)}</b></div><div><small>Next follow-up</small><b>${l.next_action ? fmtDate(l.next_action) : '—'}</b></div>${l.referred_by ? `<div><small>Referred by</small><b>${l.referred_by}</b></div>` : ''}</div>
    ${!done ? `<div class="tools"><button class="btn sm" data-act="leadCall" data-id="${l.id}">Call</button><button class="btn sm" data-act="leadAppt" data-id="${l.id}">Book appointment</button><select class="in sm" id="lead-stage">${C.crm.stage_list.filter(s => s !== 'Converted').map(s => `<option ${s === l.stage ? 'selected' : ''}>${s}</option>`).join('')}</select><button class="btn sm" data-act="leadStage" data-id="${l.id}">Set stage</button><button class="btn sm pri" data-act="leadConvert" data-id="${l.id}">Open account</button></div>
    <div class="tools"><input class="in" id="lead-note" placeholder="Add a note" style="flex:1"><input class="in sm" type="date" id="lead-next" title="Next follow-up"><button class="btn sm" data-act="leadNote" data-id="${l.id}">Save</button></div>` : ''}
    ${l.appointments.length ? `<div><h3 style="margin:0 0 8px;font-size:15px">Appointments</h3>${tbl([['When'], ['With'], ['Purpose'], ['Status'], ['']], l.appointments.map(a => `<tr><td class="n">${esc(a.at.replace('T', ' '))}</td><td>${esc(a.with)}</td><td>${esc(a.purpose)}</td><td>${pill(a.status, a.status === 'Done' ? 'good' : a.status === 'Scheduled' ? 'acc' : 'nt')}</td><td>${a.status === 'Scheduled' ? `<button class="btn sm" data-act="apptSet" data-id="${l.id}" data-ap="${a.id}" data-st="Done">Done</button><button class="btn sm" data-act="apptSet" data-id="${l.id}" data-ap="${a.id}" data-st="No show">No show</button>` : ''}</td></tr>`))}</div>` : ''}
    <div><h3 style="margin:0 0 8px;font-size:15px">Activity</h3>${l.notes.slice().reverse().map(n => `<div class="msg"><small class="dim">${esc(n.kind)} · ${esc(n.by)} · ${esc(n.at.slice(0, 16).replace('T', ' '))}${n.outcome ? ' · ' + esc(n.outcome) : ''}</small><div>${esc(n.text || '')}</div></div>`).join('')}</div>
  </div>`, true);
}
const leadAct = async (id, body, msg) => { try { const r = await post(`/v1/leads/${id}/act`, body); toast(r.message || msg); C.crm.leads = C.crm.leads.map(x => x.id === id ? r.lead : x); if (document.querySelector('.drawer') || body.kind === 'appointment') leadDrawer(id); $('page').innerHTML = groupBar('crm') + V.crm.html(C.crm); bindPage(); } catch (e) { toast(e.message, true); } };
function portalModal(ucc) {
  modal(mhd('Client portal access · ' + ucc, 'The client signs in with their own user ID and a one-time password, then sets a password and two-factor sign-in. They see only their own accounts.') + `<form class="bd" id="pgf">
    <div class="fld"><label for="pg-f">Family accounts on the same login (UCCs, comma separated)</label><input class="in mono" id="pg-f" placeholder="e.g. BH1002, BH1007"></div>
    <label class="check"><input type="checkbox" id="pg-d"> Family declaration received (spouse, dependent children or dependent parents) for accounts that do not share this client’s email or mobile</label>
    <div id="pg-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Give access</button></div></form>`);
  $('pgf').onsubmit = async e => { e.preventDefault(); try { const r = await post(`/v1/clients/${ucc}/portal`, { family: $('pg-f').value.split(',').map(x => x.trim()).filter(Boolean), declared: $('pg-d').checked });
    modal(mhd('Portal access ready', ucc) + `<div class="bd"><div class="kv"><div><small>User ID</small><b class="mono">${esc(r.user)}</b></div>${r.password ? `<div><small>One-time password</small><b class="mono">${esc(r.password)}</b></div>` : '<div><small>Password</small><b>unchanged (existing login)</b></div>'}</div><div class="note">Send the user ID and the one-time password to the client separately (for example the ID by email and the password by SMS). It must be changed at first sign-in.</div></div><div class="ft"><button class="btn pri" data-act="close">Done</button></div>`); } catch (err) { $('pg-err').innerHTML = errHTML(err); } };
}

/* ---------- messages & templates ---------- */
V.messages = {
  load: async () => { const [ms, tp, wa, wc] = await Promise.all([get('/v1/messages'), get('/v1/templates'), get('/v1/whatsapp/chats'), get('/v1/whatsapp/consents')]); return { ms, tp, wa, wc }; },
  html: d => {
    const ms = d.ms, cnt = s => ms.filter(m => m.status === s).length, ck = has('Ops checker', 'Compliance officer');
    return ph('Messages', 'Email, SMS and WhatsApp to clients, sent from templates when something happens on their account (receipts, payouts, margin shortfalls, contract notes, complaints, KYC changes), in bulk, or by scheduled jobs. SMS templates carry their DLT template ID; promotional SMS is held outside 9 am–9 pm; WhatsApp goes only to clients who opted in. Until a provider is connected, messages are kept here as the outbox.',
      ck ? '<button class="btn" data-act="bulkMsg">Bulk message</button><button class="btn pri" data-act="editTpl" data-id="">New template</button>' : '') +
    `<div class="kpis">${kpi('Sent', cnt('Sent'), 'last 300 messages', 'good')}${kpi('Bounced / failed', cnt('Bounced') + cnt('Failed'), 'fix contact details', cnt('Bounced') + cnt('Failed') ? 'bad' : '')}${kpi('Held', cnt('Held'), 'promotional, after 9 pm')}${kpi('Not opted in', cnt('Not opted in'), 'WhatsApp')}${kpi('Templates on', d.tp.templates.filter(t => t.active).length, 'of ' + d.tp.templates.length)}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('msgTab', [['log', 'Sent messages'], ['tpl', 'Templates'], ['wa', 'WhatsApp bot'], ['wc', `WhatsApp consent · ${d.wc.counts['Opted in'] || 0} in`]])}${UI.msgTab === 'log' ? `<input class="in" data-ui="msgQ" data-part="msg-t" placeholder="Filter UCC, event, status" value="${esc(UI.msgQ)}">` : ''}</div></div><div id="msg-t">${P['msg-t'](d)}</div></div>`;
  }
};
P['msg-t'] = d => {
  const st = { Sent: 'good', Bounced: 'bad', Failed: 'bad', Held: 'warn', 'Not opted in': 'nt' };
  if (UI.msgTab === 'tpl') return tbl([['Event'], ['Channel'], ['Category'], ['Message'], ['DLT / WA template'], ['On'], ['']], d.tp.templates.map(t => `<tr><td class="n">${esc(t.event)}</td><td>${esc(t.channel)}</td><td>${esc(t.category)}</td><td class="wrap small">${t.subject ? `<b>${esc(t.subject)}</b><br>` : ''}${esc(t.body.slice(0, 160))}${t.body.length > 160 ? '…' : ''}</td><td class="n dim">${esc(t.dlt_template_id || t.wa_template || '—')}</td><td>${pill(t.active ? 'On' : 'Off', t.active ? 'good' : 'nt')}</td><td>${has('Ops checker', 'Compliance officer') ? `<button class="btn sm" data-act="editTpl" data-id="${t.id}">Edit</button><button class="btn sm" data-act="testTpl" data-id="${t.id}">Test</button>` : ''}</td></tr>`), { tall: true });
  if (UI.msgTab === 'wc') {
    const w = d.wc, ck = has('Ops checker', 'Compliance officer'), rec = has('Ops maker', 'Ops checker', 'Compliance officer');
    const sc = { 'Opted in': 'good', 'Opted out': 'nt', Invited: 'acc', 'Not asked': 'warn' };
    return `<div class="bd"><div class="note">WhatsApp lets a business start a conversation only with clients who agreed to hear from it there, and each consent is kept with when, how and by whom. Clients opt in by the consent link, the client portal, replying YES or START on WhatsApp, the account opening form, or on a recorded call; they opt out by STOP or in the portal. Clients without consent still get email and SMS.</div>
      <div class="kpis" style="margin-top:12px">${['Opted in', 'Invited', 'Not asked', 'Opted out'].map(k => kpi(k, nf(w.counts[k] || 0), { 'Opted in': 'get WhatsApp messages', Invited: 'link sent, no answer yet', 'Not asked': 'never asked', 'Opted out': 'said no or STOP' }[k], k === 'Not asked' && w.counts[k] ? 'warn' : '')).join('')}</div>
      ${ck ? `<div class="tools"><button class="btn pri" data-act="waCampaign" data-id="Email">Ask by email</button><button class="btn" data-act="waCampaign" data-id="SMS">Ask by SMS</button><span class="dim small">Sends a one-tap link to clients not asked yet (or not answering for 30 days). Those who said no are not asked again.</span></div>` : ''}</div>` +
      tbl([['Client'], ['Mobile'], ['WhatsApp'], ['Since'], ['How'], ['']], w.rows.map(r => `<tr><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}</td><td class="mono">${esc(r.mobile)}</td><td>${pill(r.state, sc[r.state])}${r.invited && r.state === 'Invited' ? `<div class="dim small">asked ${fmtDate(r.invited.slice(0, 10))}</div>` : ''}</td><td class="n">${r.at ? fmtDate(r.at.slice(0, 10)) : '—'}</td><td class="small wrap">${esc(r.source || '')}${r.by ? ` · ${esc(r.by)}` : ''}${r.ref ? `<div class="dim">${esc(r.ref)}</div>` : ''}</td><td>${rec ? `<button class="btn sm" data-act="waConsent" data-id="${r.ucc}" data-on="${r.state === 'Opted in' ? 0 : 1}">${r.state === 'Opted in' ? 'Record opt-out' : 'Record consent'}</button>` : ''}</td></tr>`), { tall: true });
  }
  if (UI.msgTab === 'wa') return `<div class="bd"><div class="tools"><input class="in mono" id="wa-from" placeholder="Registered mobile, e.g. 98…" style="max-width:200px"><input class="in" id="wa-text" placeholder="BAL, HOLD, LEDGER, CN, MARGIN, TAX, OFFERS, YES, STOP" style="flex:1"><button class="btn pri" data-act="waSend">Send to the bot</button></div><div id="wa-out"></div><p class="dim small">This simulates a client writing to the firm’s WhatsApp number. The live number posts to <span class="mono">/hooks/&lt;firm&gt;/whatsapp</span>, signed with the WhatsApp secret in Settings. A client is identified only by their registered mobile.</p></div>` +
    tbl([['When'], ['Mobile'], ['Client'], ['Asked'], ['Bot replied']], d.wa.map(c => `<tr><td class="n dim">${esc(c.at.slice(5, 16).replace('T', ' '))}</td><td class="mono">${esc(c.mobile.slice(0, 2) + '••••' + c.mobile.slice(-4))}</td><td class="n">${c.ucc || '—'}</td><td>${esc(c.in)}</td><td class="wrap small">${esc(c.out).replace(/\n/g, '<br>')}</td></tr>`), { tall: true, empty: 'No conversations yet.' });
  const q = UI.msgQ.trim().toLowerCase(), ms = d.ms.filter(m => !q || (m.ucc + ' ' + m.event + ' ' + m.status + ' ' + m.channel).toLowerCase().includes(q));
  return tbl([['Message'], ['When'], ['Client'], ['Event'], ['Channel'], ['To'], ['Text'], ['Status']], ms.map(m => `<tr><td class="n dim">${m.id}</td><td class="n">${esc(m.at.slice(5, 16).replace('T', ' '))}</td><td class="n"><button class="linkish" data-act="client" data-id="${m.ucc}">${m.ucc}</button></td><td class="n">${esc(m.event)}</td><td>${esc(m.channel)}</td><td class="mono small">${esc(m.to)}</td><td class="wrap small">${m.subject ? `<b>${esc(m.subject)}</b><br>` : ''}${esc(m.body.slice(0, 140))}${m.body.length > 140 ? '…' : ''}</td><td>${pill(m.status, st[m.status])}${m.note ? `<div class="dim small">${esc(m.note)}</div>` : ''}</td></tr>`), { tall: true, empty: 'No messages yet.' });
};
function tplModal(id) {
  const d = C.messages, t = d.tp.templates.find(x => x.id === id) || { event: 'bulk', channel: 'Email', category: 'Service', subject: '', body: '', active: true }, ev = d.tp.events;
  const vars = e => ['name', 'ucc', 'firm', ...(ev[e] || [])].map(v => `<code>{${v}}</code>`).join(' ');
  modal(mhd(id ? 'Edit template ' + id : 'New template', 'Placeholders in braces are filled from the event.') + `<form class="bd" id="tpf"><div class="form">
    <div class="fld"><label for="tp-ev">Event</label><select class="in" id="tp-ev">${Object.keys(ev).sort().map(e => `<option ${e === t.event ? 'selected' : ''}>${e}</option>`).join('')}</select></div>
    <div class="fld"><label for="tp-ch">Channel</label><select class="in" id="tp-ch">${['Email', 'SMS', 'WhatsApp'].map(c => `<option ${c === t.channel ? 'selected' : ''}>${c}</option>`).join('')}</select></div>
    <div class="fld"><label for="tp-cat">Category</label><select class="in" id="tp-cat">${['Transactional', 'Service', 'Promotional'].map(c => `<option ${c === t.category ? 'selected' : ''}>${c}</option>`).join('')}</select></div>
    <div class="fld"><label for="tp-dlt">DLT template ID (SMS) / approved template (WhatsApp)</label><input class="in mono" id="tp-dlt" value="${esc(t.dlt_template_id || t.wa_template || '')}"></div>
    <div class="fld full"><label for="tp-sub">Subject (email)</label><input class="in" id="tp-sub" value="${esc(t.subject || '')}"></div>
    <div class="fld full"><label for="tp-body">Message</label><textarea class="in" id="tp-body" rows="6">${esc(t.body)}</textarea><span class="dim small" id="tp-vars">Available: ${vars(t.event)}</span></div>
    <label class="check full"><input type="checkbox" id="tp-on" ${t.active ? 'checked' : ''}> Send this template when the event happens</label></div>
    <div id="tp-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
  $('tp-ev').onchange = () => { $('tp-vars').innerHTML = 'Available: ' + vars($('tp-ev').value); };
  $('tpf').onsubmit = async e => { e.preventDefault(); const ch = $('tp-ch').value; try { await post('/v1/templates', { id: id || '', event: $('tp-ev').value, channel: ch, category: $('tp-cat').value, subject: $('tp-sub').value, body: $('tp-body').value, dlt_template_id: ch === 'SMS' ? $('tp-dlt').value : '', wa_template: ch === 'WhatsApp' ? $('tp-dlt').value : '', active: $('tp-on').checked }); closeModal(); toast('Template saved'); render({ keep: true }); } catch (err) { $('tp-err').innerHTML = errHTML(err); } };
}
function bulkModal() {
  const d = C.messages, bt = d.tp.templates.filter(t => t.active && t.event === 'bulk');
  if (!bt.length) { toast('Make a template for the event "bulk" first'); return; }
  clientCache().then(() => {
    modal(mhd('Bulk message', 'Sends a "bulk" template to everyone, a client group, or chosen clients.') + `<form class="bd" id="bkmf"><div class="form">
      <div class="fld full"><label for="bm-t">Template</label><select class="in" id="bm-t">${bt.map(t => `<option value="${t.id}">${esc(t.channel)} · ${esc(t.subject || t.body.slice(0, 60))}</option>`).join('')}</select></div>
      <div class="fld full"><label for="bm-to">Send to</label><select class="in" id="bm-to"><option value="all">All active clients</option>${(CL.tags || []).map(g => `<option value="TAG:${esc(g.tag || g)}">Group ${esc(g.tag || g)}</option>`).join('')}<option value="list">These UCCs…</option></select></div>
      <div class="fld full"><label for="bm-u">UCCs (comma separated, for "These UCCs")</label><input class="in mono" id="bm-u"></div></div>
      <div id="bm-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Send</button></div></form>`);
    $('bkmf').onsubmit = async e => { e.preventDefault(); const to = $('bm-to').value; try { const r = await post('/v1/messages/bulk', { template: $('bm-t').value, target: to === 'list' ? '' : to, uccs: to === 'list' ? $('bm-u').value.split(',').map(x => x.trim().toUpperCase()).filter(Boolean) : [] }); closeModal(); toast(Object.entries(r).map(([k, v]) => `${v} ${k.toLowerCase()}`).join(', ')); render({ keep: true }); } catch (err) { $('bm-err').innerHTML = errHTML(err); } };
  });
}

/* ---------- scheduled jobs ---------- */
V.jobs = {
  load: async () => { const [d, mail] = await Promise.all([get('/v1/jobs'), get('/v1/outbox').catch(() => [])]); d.mail = mail; return d; },
  html: d => {
    const adm = has('Admin'), run = has('Admin', 'Ops checker'), kn = Object.fromEntries(d.kinds.map(k => [k.kind, k]));
    const when = w => w === 'business' ? 'Business days' : w === 'daily' ? 'Every day' : w.startsWith('weekly:') ? 'Every ' + w.slice(7) : 'Monthly on day ' + w.slice(8);
    return ph('Scheduled jobs', 'Routine work run automatically at a set time (IST): the Autopilot stages, Periodic work (weekly, monthly and DP statements and the rest), debit reminders, daily margin statements, KRA files and reports to management. Contract notes, margin calls, KYC reminders, inactive flagging and interest are run by Autopilot (set them on the Autopilot screen), so they are not separate jobs. Each run is recorded, and management is emailed when a job succeeds or fails. Jobs run as the system, with every action in the audit trail.', adm ? '<button class="btn pri" data-act="editJob" data-id="">New job</button>' : '') +
    (edition() === 'manual' ? '<div class="note"><b>Manual edition.</b> Nothing runs on a timer: use <b>Run now</b> when you want a job done.</div>' : '') + `<div class="panel">${tbl([['Job'], ['Schedule'], ['Next run'], ['Last run'], ['']], d.jobs.map(j => `<tr><td><b>${esc(j.name)}</b> ${pill(j.active ? 'On' : 'Off', j.active ? 'good' : 'nt')}<div class="dim small">${Object.keys(j.params || {}).length ? Object.entries(j.params).map(([k, v]) => esc(k + ': ' + v)).join(', ') : esc(kn[j.kind]?.name || j.kind)}</div></td><td>${when(j.when)} at ${esc(j.at)}${j.only_failure ? '<div class="dim small">management told only on failure</div>' : ''}</td><td class="n">${j.active && j.next ? esc(j.next) : '—'}</td><td class="wrap small">${j.last_status ? pill(j.last_status, j.last_status === 'Succeeded' ? 'good' : 'bad') + ' ' + esc(j.last_summary) + `<div class="dim">${esc(j.last_run.slice(0, 16).replace('T', ' '))}</div>` : '<span class="dim">never</span>'}</td><td>${run ? `<button class="btn sm" data-act="runJob" data-id="${j.id}">Run now</button>` : ''}${adm ? `<button class="btn sm" data-act="editJob" data-id="${j.id}">Edit</button>` : ''}</td></tr>`), { empty: 'No jobs.' })}</div>
    ${(d.automations || []).length ? `<div class="panel"><div class="hd"><h3>Browser automations on a schedule</h3><small>set on <a href="#rpa" data-act="go" data-id="rpa">Browser automations</a>; they run on the owner's computer</small></div>${tbl([['Automation'], ['Schedule'], ['Owner'], ['Last run']], d.automations.map(a => `<tr><td><b>${esc(a.name)}</b> ${pill(a.active ? 'On' : 'Off', a.active ? 'good' : 'nt')}</td><td>${when(a.when)} at ${esc(a.at)}</td><td>${esc(a.owner)}</td><td class="small">${a.last_status ? pill(a.last_status, /Succe|Done/.test(a.last_status) ? 'good' : 'bad') + ' ' + esc(fmtDate(a.last_date)) : '<span class="dim">never</span>'}</td></tr>`))}</div>` : ''}
    <div class="panel"><div class="hd"><h3>Recent runs</h3></div>${tbl([['Run'], ['Job'], ['For date'], ['Started'], ['Took', 'r'], ['Result'], ['Told']], d.runs.map(r => `<tr><td class="n dim">${r.id}</td><td>${esc(r.name)}</td><td class="n">${fmtDate(r.date)}</td><td class="n">${esc(r.started.slice(0, 16).replace('T', ' '))}<div class="dim small">${esc(r.by)}</div></td><td class="r">${r.ms} ms</td><td class="wrap small">${pill(r.status, r.status === 'Succeeded' ? 'good' : 'bad')} ${esc(r.summary)}</td><td class="small dim">${esc(r.notified || '—')}</td></tr>`), { tall: true, empty: 'No runs yet.' })}</div>
    ${(d.mail || []).length ? `<div class="panel"><div class="hd"><h3>Mail to staff</h3><small>job results, backup alerts and emailed reports</small></div>${tbl([['When'], ['To'], ['Subject'], ['Result']], d.mail.slice(0, 50).map(m => `<tr><td class="n small">${esc((m.at || '').slice(0, 16).replace('T', ' '))}</td><td class="small">${esc(m.to)}</td><td class="wrap small">${esc(m.subject)}</td><td class="small">${pill(m.result, m.result.startsWith('Sent') ? 'good' : 'bad')}</td></tr>`), { tall: true })}</div>` : ''}`;
  }
};
function jobModal(id) {
  const d = C.jobs, j = d.jobs.find(x => x.id === id) || { name: '', kind: 'margin_statements', at: '21:30', when: 'business', params: {}, notify: [], active: true }, kinds = d.kinds;
  const params = k => (kinds.find(x => x.kind === k)?.params || []).map(p => `<div class="fld"><label for="jp-${p}">${p === 'to' ? 'Email to (comma separated)' : p === 'report' ? 'Report code (e.g. ledger_balances)' : p === 'min_days' ? 'Minimum days in debit' : p}</label><input class="in" id="jp-${p}" data-jp="${p}" value="${esc(j.params?.[p] || '')}"></div>`).join('');
  modal(mhd(id ? 'Edit job' : 'New job', '') + `<form class="bd" id="jbf"><div class="form">
    <div class="fld full"><label for="jb-n">Name</label><input class="in" id="jb-n" value="${esc(j.name)}" required></div>
    <div class="fld full"><label for="jb-k">Job</label><select class="in" id="jb-k">${kinds.map(k => `<option value="${k.kind}" ${k.kind === j.kind ? 'selected' : ''}>${esc(k.name)}</option>`).join('')}</select><span class="dim small" id="jb-about">${esc(kinds.find(k => k.kind === j.kind)?.about || '')}</span></div>
    <div id="jb-params" class="full form" style="padding:0">${params(j.kind)}</div>
    <div class="fld"><label for="jb-w">Runs</label><select class="in" id="jb-w">${[['business', 'Business days'], ['daily', 'Every day'], ...['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'].map(x => ['weekly:' + x, 'Every ' + x]), ...[1, 2, 5, 7, 10, 15, 25].map(x => ['monthly:' + x, 'Monthly on day ' + x])].map(([v, l]) => `<option value="${v}" ${v === j.when ? 'selected' : ''}>${l}</option>`).join('')}</select></div>
    <div class="fld"><label for="jb-at">At (IST)</label><input class="in" type="time" id="jb-at" value="${esc(j.at)}"></div>
    <div class="fld full"><label for="jb-to">Tell (emails; empty for the management list in Settings)</label><input class="in" id="jb-to" value="${esc((j.notify || []).join(', '))}"></div>
    <label class="check"><input type="checkbox" id="jb-of" ${j.only_failure ? 'checked' : ''}> Only when it fails</label><label class="check"><input type="checkbox" id="jb-on" ${j.active ? 'checked' : ''}> Switched on</label></div>
    <div id="jb-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
  $('jb-k').onchange = () => { const k = $('jb-k').value; $('jb-params').innerHTML = params(k); $('jb-about').textContent = kinds.find(x => x.kind === k)?.about || ''; };
  $('jbf').onsubmit = async e => { e.preventDefault(); const p = {}; document.querySelectorAll('[data-jp]').forEach(x => p[x.dataset.jp] = x.value);
    try { await post('/v1/jobs', { id: id || '', name: $('jb-n').value, kind: $('jb-k').value, params: p, when: $('jb-w').value, at: $('jb-at').value, notify: $('jb-to').value.split(',').map(x => x.trim()).filter(Boolean), only_failure: $('jb-of').checked, active: $('jb-on').checked }); closeModal(); toast('Job saved'); render({ keep: true }); } catch (err) { $('jb-err').innerHTML = errHTML(err); } };
}

/* ---------- depository (DP) ---------- */
V.dp = {
  load: () => Promise.all([get(`/v1/dp?date=${S.date}`), get('/v1/dp/bsda-reviews').catch(() => [])]).then(([d, r]) => Object.assign(d, { bsda_reviews: r })),
  html: d => {
    const c = d.counts, n = k => c[k] || 0;
    return ph('Depository', 'The firm’s DP desk: demat (BO) accounts with their status and BSDA eligibility, delivery instruction slips (issued books, maker-checker entry and verification against the slip register and free balance, upload batch, execution), demat, freeze, transmission and closure requests, and monthly DP billing to the trading ledger with GST.',
      `${has('Ops maker', 'Ops checker') ? '<button class="btn" data-act="dpBook">Issue DIS book</button><button class="btn" data-act="dpReq">New request</button><button class="btn pri" data-act="dpDIS">Enter a slip</button>' : ''}`) +
    `<div class="kpis">${kpi('Active accounts', n('Active'), n('BSDA') + ' BSDA')}${kpi('Frozen', n('Frozen'), n('Closed') + ' closed', n('Frozen') ? 'warn' : '')}${kpi('Could be BSDA', n('Could be BSDA'), 'holdings within ₹' + nf(num(d.tariff.bsda_limit) / 100000) + ' lakh')}${kpi('Slips to verify', n('DIS to verify'), n('DIS to upload') + ' to upload', n('DIS to verify') ? 'warn' : 'good')}${kpi('Requests open', n('Requests open'), 'demat, freeze, transmission, closure')}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('dpTab', [['acc', 'Accounts'], ['dis', 'Delivery instructions'], ['req', 'Requests'], ['bill', 'Billing & tariff'], ['bsda', 'BSDA review']])}${UI.dpTab === 'acc' ? `<input class="in" data-ui="dpQ" data-part="dp-t" placeholder="Filter name, UCC or BO ID" value="${esc(UI.dpQ)}">` : ''}${UI.dpTab === 'dis' && has('Ops maker', 'Ops checker') ? `<button class="btn sm" data-act="dpBatch">Upload batch for ${fmtDate(S.date)}</button>` : ''}</div></div><div id="dp-t">${P['dp-t'](d)}</div></div>`;
  }
};
P['dp-t'] = d => {
  const ck = has('Ops checker'), mk = has('Ops maker', 'Ops checker');
  if (UI.dpTab === 'bsda') {
    const r = d.bsda_reviews[0], line = l => `<tr><td><button class="linkish mono" data-act="client" data-id="${l.ucc}">${l.ucc}</button> ${esc(l.name)}</td><td class="mono">${esc(l.bo_id)}</td><td class="r">${inr(l.value)}</td><td>${l.to ? `${esc(l.from)} → <b>${esc(l.to)}</b>` : '—'}</td><td class="small wrap">${esc(l.reason)}</td></tr>`;
    const head = `<div class="tools pad">${mk ? `<label class="dim small">As on</label><input class="in sm" type="date" id="bsda-asof" value="${esc(r && r.status !== 'Applied' ? r.as_of : S.date)}"><button class="btn" data-act="bsdaRun">Review now</button>` : ''}${r && r.status === 'Proposed' && ck ? `<button class="btn pri" data-act="bsdaApply" data-id="${r.id}">Apply ${r.lines.length} change${r.lines.length === 1 ? '' : 's'}</button>` : ''}${r && r.file_id ? `<button class="btn" data-act="dl" data-id="/v1/periodic/files/${r.file_id}" data-name="${esc(r.file_name)}">Modification file</button>` : ''}
      <span class="dim small">Every quarter: eligible individual accounts (one demat account, holdings within ₹${nf(num(d.tariff.bsda_limit) / 100000)} lakh) become BSDA unless the client chose a regular account; BSDAs above the limit become regular. A different checker applies the review; clients are told.</span></div>`;
    if (!r) return head + '<div class="pad dim">No review yet.</div>';
    return head + `<div class="pad">${pill(r.id + ' · ' + r.status, r.status === 'Applied' ? 'good' : 'warn')} <span class="dim small">as on ${fmtDate(r.as_of)} · ${r.accounts} accounts reviewed · ${r.bsda} already BSDA · proposed by ${esc(r.by)}${r.applied_by ? ' · applied by ' + esc(r.applied_by) + ' ' + esc(r.applied_at) : ''}</span></div>` +
      tbl([['Client'], ['BO ID'], ['Holding value', 'r'], ['Change'], ['Why']], r.lines.map(line), { empty: 'No account needs to change.' }) +
      (r.kept.length ? `<div class="pad"><b class="small">Eligible but kept regular</b></div>` + tbl([['Client'], ['BO ID'], ['Holding value', 'r'], ['Change'], ['Why']], r.kept.map(line)) : '');
  }
  if (UI.dpTab === 'dis') {
    const st = { Entered: 'warn', Verified: 'acc', Uploaded: 'acc', Executed: 'good', Rejected: 'bad', Failed: 'bad' };
    return tbl([['Instruction'], ['Client'], ['Slip'], ['To'], ['Reason'], ['ISINs'], ['Value', 'r'], ['Execute on'], ['Status'], ['']], d.dis.map(x => `<tr><td class="n">${x.id}<div class="dim small">${esc(x.maker)}${x.checker ? ' · ' + esc(x.checker) : ''}</div></td><td><button class="linkish mono" data-act="client" data-id="${x.ucc}">${x.ucc}</button> ${esc(x.name)}</td><td class="n">${x.slip}</td><td class="n small">${esc(x.kind)}<div class="mono">${esc(x.to_bo)}</div></td><td class="small">${esc(x.reason)}${num(x.consideration) ? '<div>₹' + nf(x.consideration, 2) + '</div>' : ''}</td><td class="small">${x.lines.map(l => `${esc(l.symbol || l.isin)} × ${nf(l.qty)}`).join('<br>')}</td><td class="r ${num(x.value) > 500000 ? 'neg' : ''}">${nf(x.value)}</td><td class="n">${fmtDate(x.exec_date)}</td><td>${pill(x.status, st[x.status])}${x.note ? `<div class="dim small">${esc(x.note)}</div>` : ''}${x.client_confirmation ? `<div class="dim small">confirmed: ${esc(x.client_confirmation)}</div>` : ''}</td><td>${x.status === 'Entered' && ck ? `<button class="btn sm pri" data-act="dpVerify" data-id="${x.id}" data-a="1" data-hv="${num(x.value) > 500000 ? 1 : 0}">Verify</button><button class="btn sm" data-act="dpVerify" data-id="${x.id}" data-a="0">Reject</button>` : ''}${x.status === 'Uploaded' && mk ? `<button class="btn sm pri" data-act="dpExec" data-id="${x.id}" data-ok="1">Executed</button><button class="btn sm" data-act="dpExec" data-id="${x.id}" data-ok="0">Failed</button>` : ''}</td></tr>`), { tall: true, empty: 'No delivery instructions yet.' }) +
      `<div class="pad"><span class="dim small">Slips issued: ${d.books.map(b => `${b.ucc} ${b.from}–${b.to} (${b.used.length} used)`).join(' · ') || 'none'}. Market deliveries for pay-in go through DDPI or eDIS on the Settlement page; slips here are off-market and inter-depository transfers.</span></div>`;
  }
  if (UI.dpTab === 'req') {
    const st = { Pending: 'warn', 'Sent to RTA': 'acc', Completed: 'good', Rejected: 'bad' };
    return tbl([['Request'], ['Kind'], ['Client'], ['Details'], ['Raised'], ['Status'], ['']], d.requests.map(r => `<tr><td class="n">${r.id}</td><td>${esc(r.kind)}</td><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}</td><td class="small wrap">${Object.entries(r.detail).map(([k, v]) => `${esc(k.replace(/_/g, ' '))}: <b>${esc(v)}</b>`).join(' · ')}</td><td class="n dim">${esc(r.maker)}<div>${fmtDate(r.at.slice(0, 10))}</div></td><td>${pill(r.status, st[r.status])}${r.note ? `<div class="dim small">${esc(r.note)}</div>` : ''}</td><td>${r.kind === 'Demat' && r.status === 'Pending' && mk ? `<button class="btn sm" data-act="dpAct" data-id="${r.id}" data-a="send">Sent to RTA</button>` : ''}${ck && ((r.kind === 'Demat' && r.status === 'Sent to RTA') || (r.kind !== 'Demat' && r.status === 'Pending')) ? `<button class="btn sm pri" data-act="dpAct" data-id="${r.id}" data-a="approve">${r.kind === 'Demat' ? 'Credit shares' : 'Approve'}</button>` : ''}${ck && ['Pending', 'Sent to RTA'].includes(r.status) ? `<button class="btn sm" data-act="dpAct" data-id="${r.id}" data-a="reject">Reject</button>` : ''}</td></tr>`), { tall: true, empty: 'No requests.' });
  }
  if (UI.dpTab === 'bill') {
    const t = d.tariff, f = (id, l, v) => `<div class="fld"><label for="dt-${id}">${l}</label><input class="in mono" id="dt-${id}" value="${esc(v)}" ${has('Admin', 'Ops checker') ? '' : 'disabled'}></div>`;
    return `<div class="bd"><div class="form" style="grid-template-columns:repeat(4,1fr)">${f('amc', 'AMC per year (₹)', t.amc_year)}${f('bsda0', 'BSDA nil AMC up to (₹)', t.bsda_nil_upto)}${f('bsdal', 'BSDA holding limit (₹)', t.bsda_limit)}${f('bsdaamc', 'BSDA AMC above nil slab (₹/yr)', t.bsda_amc_year)}${f('debit', 'Debit charge per ISIN (₹)', t.debit_charge)}${f('share', 'of which depository (₹)', t.depository_share)}${f('pledge', 'Pledge per ISIN (₹)', t.pledge_charge)}${f('demat', 'Demat per certificate (₹)', t.demat_per_cert)}${f('om', 'Off-market % of value', t.offmarket_pct)}${f('ommin', 'Off-market minimum (₹)', t.offmarket_min)}${f('credit', 'Credit charge per ISIN (₹)', t.credit_charge)}${f('from', 'Applies from', t.from)}
      <div class="fld" style="grid-column:1/-1"><label for="dt-free">Charge-free accounts: transfers to or from these carry no debit or credit charge (the firm’s own pool accounts). One per line: CDSL or NSDL, the account ID (CDSL BO ID, or NSDL DP ID + client ID), then a label</label><textarea class="in mono" id="dt-free" rows="3" placeholder="CDSL 1203320000000001 Pool account&#10;NSDL IN30012310000001 Pool account" ${has('Admin', 'Ops checker') ? '' : 'disabled'}>${esc((t.charge_free || []).map(a => [a.depository, a.id, a.label].filter(Boolean).join(' ')).join('\n'))}</textarea></div>
      <div class="fld" style="grid-column:1/-1"><label for="dt-cms">The firm’s clearing member codes: pay-ins and pay-outs settled for these carry no charge (on-market pay-ins go straight to the clearing corporation, not through the pool)</label><input class="in mono" id="dt-cms" value="${esc((t.charge_free_cms || []).join(', '))}" placeholder="as the CDSL transaction file shows them" ${has('Admin', 'Ops checker') ? '' : 'disabled'}>
      ${(d.clearing_members || []).length ? `<div class="small dim" style="margin-top:4px">Seen in loaded DP transactions: ${d.clearing_members.map(c => `<span class="mono">${esc(c.code)}</span> (${c.txns} · ${c.kinds.map(esc).join(', ')})${c.free ? ' ✓' : ''}`).join(' · ')}</div>` : ''}</div></div>
      <div class="tools" style="margin-top:8px">${has('Admin', 'Ops checker') ? '<button class="btn" data-act="dpTariff">Save tariff</button>' : ''}${has('Ops checker') ? `<input class="in sm" type="month" id="dp-month" value="${S.date.slice(0, 7)}"><button class="btn pri" data-act="dpBill">Run DP billing</button>` : ''}<span class="dim small">GST at the firm’s rate is added. Bill a month after it ends; each month is billed once. Transfers to or from the charge-free accounts are counted on the bill but not charged.</span></div></div>` +
      tbl([['Bill'], ['Client'], ['Charges'], ['Taxable', 'r'], ['GST', 'r'], ['Total', 'r'], ['Invoice']], d.bills.map(b => `<tr><td class="n">${esc(b.month)}</td><td><button class="linkish mono" data-act="client" data-id="${b.ucc}">${b.ucc}</button> ${esc(b.name)}</td><td class="small">${b.lines.map(l => `${esc(l.what)}${l.count > 1 ? ' × ' + l.count : ''}: ₹${nf(l.amount, 2)}`).join('<br>')}${b.free ? `<div class="dim">${b.free} pool transfer${b.free > 1 ? 's' : ''} not charged</div>` : ''}</td><td class="r">${nf(b.taxable, 2)}</td><td class="r">${nf(b.gst, 2)}</td><td class="r"><b>${nf(b.total, 2)}</b></td><td class="n">${esc(b.invoice)}</td></tr>`), { tall: true, empty: 'No DP bills yet.' });
  }
  const q = UI.dpQ.trim().toLowerCase(), rows = d.rows.filter(r => !q || (r.ucc + ' ' + r.name + ' ' + r.bo_id).toLowerCase().includes(q));
  return tbl([['Client'], ['BO ID'], ['Status'], ['ISINs', 'r'], ['Holding value', 'r'], ['Pledged', 'r'], ['Type'], ['AMC / year', 'r'], ['']], rows.map(r => `<tr><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}</td><td class="mono">${esc(r.depository)} ${esc(r.bo_id)}</td><td>${pill(r.status + (r.freeze ? ' · ' + r.freeze : ''), r.status === 'Active' ? 'good' : r.status === 'Frozen' ? 'warn' : 'nt')}${r.freeze_reason ? `<div class="dim small">${esc(r.freeze_reason)}</div>` : ''}</td><td class="r">${r.isins}</td><td class="r">${inr(r.value)}</td><td class="r">${r.pledged_qty ? nf(r.pledged_qty) : '—'}</td><td>${r.bsda ? pill('BSDA', 'acc') : r.bsda_eligible ? '<span class="dim small">Regular · BSDA eligible</span>' : 'Regular'}</td><td class="r">${inr(r.amc_year, 2)}</td><td>${ck && r.status === 'Active' ? `<button class="btn sm" data-act="dpBSDA" data-id="${r.ucc}" data-on="${r.bsda ? 0 : 1}">${r.bsda ? 'Make regular' : r.bsda_eligible ? 'Make BSDA' : ''}</button>` : ''}<button class="btn sm" data-act="dpTxns" data-id="${r.ucc}">Transactions</button></td></tr>`.replace('<button class="btn sm" data-act="dpBSDA" data-id="' + r.ucc + '" data-on="1"></button>', '')), { tall: true, empty: 'No demat accounts.' });
};
function disModal() {
  clientCache().then(() => {
    modal(mhd('Enter a delivery instruction slip', 'Off-market or inter-depository transfer out of a client’s demat account.') + `<form class="bd" id="disf"><div class="form">
      <div class="fld full"><label for="ds-u-q">Client</label>${cpick('ds-u', '', {})}</div>
      <div class="fld"><label for="ds-slip">Slip number</label><input class="in mono" id="ds-slip" inputmode="numeric" required></div>
      <div class="fld"><label for="ds-k">Kind</label><select class="in" id="ds-k"><option>Off-market</option><option>Inter-depository</option></select></div>
      <div class="fld"><label for="ds-to">Target BO ID (16 digits) / DP and client ID</label><input class="in mono" id="ds-to" required></div>
      <div class="fld"><label for="ds-r">Reason</label><select class="in" id="ds-r">${C.dp.reasons.map(r => `<option>${esc(r)}</option>`).join('')}</select></div>
      <div class="fld"><label for="ds-c">Consideration (₹, for a sale)</label><input class="in mono" id="ds-c"></div>
      <div class="fld"><label for="ds-rcv">Received on</label><input class="in" type="date" id="ds-rcv" value="${S.date}"></div>
      <div class="fld"><label for="ds-ex">Execute on</label><input class="in" type="date" id="ds-ex" value="${S.date}"></div>
      ${[1, 2, 3].map(i => `<div class="fld"><label for="ds-isin${i}">ISIN ${i}</label><input class="in mono" id="ds-isin${i}" ${i === 1 ? 'required' : ''}></div><div class="fld"><label for="ds-q${i}">Quantity ${i}</label><input class="in mono" id="ds-q${i}" inputmode="numeric" ${i === 1 ? 'required' : ''}></div>`).join('')}</div>
      <div id="ds-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Enter slip</button></div></form>`);
    $('disf').onsubmit = async e => { e.preventDefault(); const lines = [1, 2, 3].map(i => ({ isin: $('ds-isin' + i).value.trim(), qty: +$('ds-q' + i).value })).filter(l => l.isin);
      try { const r = await post('/v1/dp/dis', { ucc: $('ds-u').value, slip: +$('ds-slip').value, kind: $('ds-k').value, to_bo: $('ds-to').value, reason: $('ds-r').value, consideration: $('ds-c').value || '0', received: $('ds-rcv').value, exec_date: $('ds-ex').value, lines }); closeModal(); toast(`${r.id} entered · ₹${nf(r.value)}; a checker verifies it`); UI.dpTab = 'dis'; render({ keep: true }); } catch (err) { $('ds-err').innerHTML = errHTML(err); } };
  });
}
function dpReqModal() {
  clientCache().then(() => {
    const fields = { Demat: [['drf', 'DRF number'], ['isin', 'ISIN'], ['symbol', 'Scrip'], ['qty', 'Quantity'], ['certificates', 'Certificates']], Freeze: [['type', 'Freeze (Debit, Credit or Both)'], ['reason', 'Reason']], Unfreeze: [['reason', 'Reason']], Transmission: [['to_ucc', 'Claimant’s UCC'], ['death_certificate', 'Death certificate reference'], ['claimant', 'Claimant name']], Closure: [['reason', 'Reason'], ['transfer_to', 'Transfer holdings to BO ID (if any)']] };
    const body = k => fields[k].map(([id, l]) => `<div class="fld"><label for="dq-${id}">${l}</label><input class="in" id="dq-${id}" data-dq="${id}"></div>`).join('');
    modal(mhd('DP request', 'Raised by a maker, completed by a checker who did not raise it.') + `<form class="bd" id="dqf"><div class="form"><div class="fld full"><label for="dq-u-q">Client</label>${cpick('dq-u', '', {})}</div><div class="fld full"><label for="dq-k">Request</label><select class="in" id="dq-k">${Object.keys(fields).map(k => `<option>${k}</option>`).join('')}</select></div></div><div class="form" id="dq-body">${body('Demat')}</div>
      <div id="dq-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Raise</button></div></form>`);
    $('dq-k').onchange = () => { $('dq-body').innerHTML = body($('dq-k').value); };
    $('dqf').onsubmit = async e => { e.preventDefault(); const detail = {}; document.querySelectorAll('[data-dq]').forEach(x => { if (x.value.trim()) detail[x.dataset.dq] = x.value.trim(); });
      try { const r = await post('/v1/dp/requests', { kind: $('dq-k').value, ucc: $('dq-u').value, detail }); closeModal(); toast(`${r.id} raised`); UI.dpTab = 'req'; render({ keep: true }); } catch (err) { $('dq-err').innerHTML = errHTML(err); } };
  });
}

/* ---------- MTF ---------- */
V.mtf = {
  load: () => get(`/v1/mtf?date=${S.date}`),
  html: d => {
    const rk = has('Risk manager', 'Ops checker'), util = num(d.book_limit) ? num(d.book) / num(d.book_limit) * 100 : 0;
    return ph('Margin trading (MTF)', `Funding of delivery buys for enrolled clients from the firm’s own funds. Initial margin: VaR + 3×ELM for F&O stocks, VaR + 5×ELM otherwise, at least ${esc(d.config.min_margin_pct)}%. Funded shares are pledged to the firm by T+1; interest accrues at ${esc(d.config.rate_pa)}% a year (no GST); a call is raised when equity falls below ${esc(d.config.maintain_pct)}% of the initial margin, with ${d.config.call_days} business day to meet it.`,
      `<button class="btn" data-act="dl" data-id="/v1/mtf/report?date=${S.date}" data-name="mtf_report_${S.date}.csv">Exchange MTF report</button>${rk ? `<button class="btn" data-act="mtfEnrol">Enrol client</button><button class="btn" data-act="mtfCalls">Raise margin calls</button><button class="btn" data-act="mtfInterest">Post interest</button>` : ''}${d.liquidate ? `<button class="btn bad" data-act="dl" data-id="/v1/mtf/liquidation?date=${S.date}" data-name="mtf_liquidation_${S.date}.csv">Liquidation file (${d.liquidate})</button>` : ''}`) +
    `<div class="kpis">${kpi('Funded book', inr(d.book), nf(util, 1) + '% of ' + d.config.max_times_net_worth + '× net worth', util > 90 ? 'bad' : '')}${kpi('Open positions', d.positions.length, d.enrolled.length + ' clients enrolled')}${kpi('Margin calls', d.calls, 'equity below maintenance', d.calls ? 'warn' : 'good')}${kpi('To liquidate', d.liquidate, 'call unmet or pledge missing', d.liquidate ? 'bad' : 'good')}${kpi('Interest accrued', inr(d.accrued, 2), 'not yet posted')}${kpi('Buys to fund', d.candidates.length, 'enrolled clients, eligible scrips')}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('mtfTab', [['pos', 'Positions'], ['fund', `Fund today’s buys (${d.candidates.length})`], ['enr', 'Enrolled clients'], ['list', 'Eligible securities'], ['pol', 'Policy']])}</div></div><div id="mtf-t">${P['mtf-t'](d)}</div></div>`;
  }
};
P['mtf-t'] = d => {
  const rk = has('Risk manager', 'Ops checker'), st = { OK: 'good', 'Margin call': 'warn', Liquidate: 'bad', 'Pledge pending': 'warn' };
  if (UI.mtfTab === 'fund') return tbl([['Client'], ['Scrip'], ['Bought (net)', 'r'], ['Avg price', 'r'], ['Value', 'r'], ['Margin %', 'r'], ['Can fund', 'r'], ['Already funded', 'r'], ['']], d.candidates.map(c => `<tr><td><button class="linkish mono" data-act="client" data-id="${c.ucc}">${c.ucc}</button> ${esc(c.name)}</td><td>${esc(c.symbol)} <span class="dim small">${esc(c.isin)}</span></td><td class="r">${nf(c.qty)}</td><td class="r">${nf(c.price, 2)}</td><td class="r">${inr(c.value)}</td><td class="r">${nf(c.margin_pct, 2)}%</td><td class="r">${inr(c.fundable)}</td><td class="r">${c.funded_qty || '—'}</td><td>${rk ? `<button class="btn sm pri" data-act="mtfFund" data-id="${c.ucc}" data-isin="${c.isin}" data-q="${c.qty - c.funded_qty}">Fund</button>` : ''}</td></tr>`), { empty: 'No delivery buys today by enrolled clients in eligible securities.' });
  if (UI.mtfTab === 'enr') return tbl([['Client'], ['Agreement'], ['Limit', 'r'], ['Funded now', 'r'], ['Rate'], ['By']], d.enrolled.map(e => `<tr><td><button class="linkish mono" data-act="client" data-id="${e.ucc}">${e.ucc}</button> ${esc(e.name)}</td><td class="n">${fmtDate(e.agreed_on)}</td><td class="r">${inr(e.limit)}</td><td class="r">${inr(sum(d.positions.filter(p => p.ucc === e.ucc), p => num(p.funded)))}</td><td>${esc(e.rate_pa)}% p.a.</td><td class="dim">${esc(e.by)}</td></tr>`), { empty: 'No client enrolled yet.' });
  if (UI.mtfTab === 'list') return tbl([['Scrip'], ['ISIN'], ['In F&O'], ['Last price', 'r'], ['Initial margin', 'r']], d.eligible.map(e => `<tr><td>${esc(e.symbol)}</td><td class="mono">${esc(e.isin)}</td><td>${e.in_fo ? 'Yes' : 'No'}</td><td class="r">${nf(e.ltp, 2)}</td><td class="r">${nf(e.margin_pct, 2)}%</td></tr>`), { tall: true }) + '<div class="pad"><span class="dim small">From the MTF-ELIGIBLE file when loaded (the exchanges’ Group I list with the firm’s own margins); otherwise every priced equity in the market data file with its VaR and ELM.</span></div>';
  if (UI.mtfTab === 'pol') { const c = d.config, f = (id, l, v) => `<div class="fld"><label for="mp-${id}">${l}</label><input class="in mono" id="mp-${id}" value="${esc(v)}" ${has('Admin', 'Risk manager') ? '' : 'disabled'}></div>`;
    return `<div class="bd"><div class="form" style="grid-template-columns:repeat(4,1fr)">${f('rate', 'Interest % a year', c.rate_pa)}${f('min', 'Minimum initial margin %', c.min_margin_pct)}${f('maint', 'Maintenance, % of initial margin', c.maintain_pct)}${f('days', 'Business days to meet a call', c.call_days)}${f('nw', 'Net worth (₹)', c.net_worth)}${f('bor', 'Borrowed funds used (₹)', c.borrowed)}${f('times', 'Book limit (× net worth)', c.max_times_net_worth)}</div>${has('Admin', 'Risk manager') ? '<button class="btn pri" data-act="mtfPolicy" style="margin-top:8px">Save policy</button>' : ''}</div>`; }
  return tbl([['Position'], ['Client'], ['Scrip'], ['Qty', 'r'], ['Cost / value', 'r'], ['Funded', 'r'], ['Equity / maint.', 'r'], ['State'], ['']], d.positions.map(p => `<tr><td class="n">${p.id}<div class="dim small">${fmtDate(p.buy_date)} @ ${nf(p.price, 2)}</div></td><td><button class="linkish mono" data-act="client" data-id="${p.ucc}">${p.ucc}</button> ${esc(p.name)}</td><td>${esc(p.symbol)}</td><td class="r">${nf(p.qty)}</td><td class="r">${nf(p.cost)}<div class="dim small">${nf(p.value)}</div></td><td class="r">${nf(p.funded)}<div class="dim small">+${nf(p.accrued, 2)} interest</div></td><td class="r ${num(p.equity) < num(p.required) ? 'neg' : ''}">${nf(p.equity)}<div class="dim small">${nf(p.required)}</div></td><td>${pill(p.state, st[p.state])} ${p.pledge === 'Pledged' ? `<span class="dim small mono">${esc(p.pledge_ref)}</span>` : ''}${p.why ? `<div class="dim small wrap">${esc(p.why)}</div>` : ''}</td><td>${p.pledge !== 'Pledged' && has('Ops maker', 'Ops checker', 'Risk manager') ? `<button class="btn sm" data-act="mtfPledge" data-id="${p.id}">Pledged</button>` : ''}${rk ? `<button class="btn sm" data-act="mtfClose" data-id="${p.id}" data-q="${p.qty}">Repay</button>` : ''}</td></tr>`), { tall: true, empty: 'No funded positions.' });
};

/* ---------- live RMS ---------- */
V.live = {
  load: () => get(`/v1/live?date=${S.date}${UI.liveAt ? '&until=' + UI.liveAt + ':00' : ''}`),
  html: d => {
    return ph('Live RMS', `Intraday risk from trades loaded so far (${d.source === 'OMS' ? 'the OMS trade book; exchange files not loaded yet' : 'exchange trade files'}) at the latest LTP snapshot: derivative and intraday margins, losses (profits are not given intraday), charges on trades not yet billed, and a ${'20'}% auction reserve on delivery sells not covered by holdings. Load the OMS trade book and LTP-SNAPSHOT files as often as needed on the Files page.`,
      `<div class="tools"><label class="dim small">As at</label><input class="in sm" type="time" id="live-at" value="${esc(UI.liveAt)}"><button class="btn sm" data-act="liveAt">Show</button><button class="btn sm" data-act="liveNow">Latest</button><label class="check small"><input type="checkbox" id="live-auto" ${UI.liveAuto ? 'checked' : ''}> Refresh every minute</label></div>${d.breaches ? `<button class="btn bad" data-act="dl" data-id="/v1/live/liquidation?date=${S.date}${UI.liveAt ? '&until=' + UI.liveAt + ':00' : ''}" data-name="liquidation_${S.date}.csv">Liquidation file (${d.breaches})</button>` : ''}`) +
    `<div class="kpis">${kpi('Trades loaded', nf(d.trades), d.last_trade ? 'last at ' + esc(d.last_trade) : 'none yet')}${kpi('Prices', d.prices_at ? 'at ' + esc(d.prices_at) : 'close', d.prices_at ? 'LTP snapshot' : 'market data file')}${kpi('Breaches', d.breaches, 'free below zero', d.breaches ? 'bad' : 'good')}${kpi('Margin in use', inr(sum(d.rows, r => num(r.margin))), 'all clients')}${kpi('MTM', inr(sum(d.rows, r => num(r.mtm))), 'derivatives and intraday')}${kpi('Unbilled charges', inr(sum(d.rows, r => num(r.unbilled)), 2), 'today’s trades')}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('liveF', ['All', 'Breach', 'Debit', 'OK'], 'live-t')}<input class="in" data-ui="liveQ" data-part="live-t" placeholder="Filter name or UCC" value="${esc(UI.liveQ)}"></div><small class="dim">Free = cash + usable collateral − margin + losses − unbilled charges − auction reserve</small></div>
    <div id="live-t">${P['live-t'](d)}</div></div>
    <div class="panel"><div class="hd"><h3>Front-office files</h3></div><div class="bd"><div class="tools"><button class="btn" data-act="dl" data-id="/v1/frontoffice/limits?date=${S.date}" data-name="bod_limits_${S.date}.csv">BOD limit file</button><button class="btn" data-act="dl" data-id="/v1/frontoffice/holdings?date=${S.date}" data-name="bod_holdings_${S.date}.csv">BOD holdings file (with T1)</button><button class="btn" data-act="dl" data-id="/v1/frontoffice/allocation?date=${S.date}" data-name="eod_allocation_${S.date}.csv">EOD client allocation to CC</button></div><p class="dim small" style="margin-bottom:0">Limits come from the previous business day’s closing risk (cash, usable collateral, overrides; inactive and blocked accounts get no buying). Holdings include yesterday’s delivery buys not yet received so they can be sold. The allocation splits each client’s funds and collateral across segments, covering each segment’s margin first, for the clearing corporation’s client-level allocation.</p></div></div>`;
  }
};

P['live-t'] = d => {
  const q = UI.liveQ.trim().toLowerCase(), f = UI.liveF, st = { Breach: 'bad', Debit: 'warn', OK: 'good', Idle: 'nt' };
  const rows = d.rows.filter(r => (f === 'All' ? r.status !== 'Idle' : r.status === f) && (!q || (r.ucc + ' ' + r.name).toLowerCase().includes(q)));
  return tbl([['Client'], ['Ledger (BOD)', 'r'], ['Collateral', 'r'], ['Margin', 'r'], ['MTM', 'r'], ['Unbilled', 'r'], ['Auction reserve', 'r'], ['Free', 'r'], ['Status']], rows.map(r => `<tr><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}<div class="dim small">${r.trades} trades · turnover ${inr(r.turnover)}</div></td><td class="r ${sgn(r.funds)}">${nf(r.funds)}</td><td class="r">${nf(r.collateral)}</td><td class="r">${nf(r.margin)}</td><td class="r ${sgn(r.mtm)}">${nf(r.mtm)}</td><td class="r">${nf(r.unbilled, 2)}</td><td class="r ${num(r.auction) ? 'neg' : ''}">${num(r.auction) ? nf(r.auction) : '—'}${r.short_delivery.length ? `<div class="dim small">${r.short_delivery.map(esc).join(', ')}</div>` : ''}</td><td class="r ${sgn(r.free)}"><b>${nf(r.free)}</b></td><td>${pill(r.status, st[r.status])}${r.blocked ? ' ' + pill('Blocked', 'bad') : ''}</td></tr>`), { tall: true, empty: 'No clients to show.' });
};
/* ---------- NRI ---------- */
V.nri = {
  load: async () => { const from = UI.nriFrom || fyStart(S.date), to = UI.nriTo || S.date; const [v, tds] = await Promise.all([get(`/v1/nri?date=${S.date}`), get(`/v1/nri/tds?from=${from}&to=${to}`)]); return { ...v, tdsp: tds, from, to }; },
  html: d => {
    const ck = has('Ops checker', 'Compliance officer');
    return ph('NRI (portfolio investment scheme)', 'Non-resident clients trade listed equity through a PIS account at a designated bank (NRE repatriable, NRO not). No intraday or short selling in the cash market; derivatives only with a custodial participant code and NRO funds; no purchases of companies on the RBI ban list (caution list needs the bank’s approval). Each day’s trades go to the PIS banks, and tax is deducted on capital gains: short-term 20%, long-term 12.5% above ₹1.25 lakh a year, plus 4% cess.',
      `<button class="btn" data-act="dl" data-id="/v1/nri/pis-file?date=${S.date}" data-name="pis_trades_${S.date}.csv">PIS bank file</button>${ck ? '<button class="btn pri" data-act="nriSet" data-id="">Register NRI</button>' : ''}`) +
    `<div class="kpis">${kpi('NRI clients', d.rows.length, d.rows.filter(r => r.details_missing).length + ' without PIS details', d.rows.some(r => r.details_missing) ? 'warn' : '')}${kpi('Rule breaks today', d.violations.length, 'raised as alerts', d.violations.length ? 'bad' : 'good')}${kpi('Trades today', sum(d.rows, r => r.trades), inr(sum(d.rows, r => num(r.bought) + num(r.sold))) + ' turnover')}${kpi('TDS in period', inr(sum(d.tdsp, x => num(x.tds)), 2), fmtDate(d.from) + ' to ' + fmtDate(d.to))}${kpi('RBI list', d.caution.length, d.caution_as_of ? 'as of ' + fmtDate(d.caution_as_of) : 'not loaded')}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('nriTab', [['cl', 'Clients'], ['chk', `Rule checks (${d.violations.length})`], ['tds', 'TDS on capital gains'], ['rbi', 'RBI caution / ban list']])}${UI.nriTab === 'tds' ? `<input class="in sm" type="date" id="nri-from" value="${d.from}"><span class="dim">to</span><input class="in sm" type="date" id="nri-to" value="${d.to}"><button class="btn sm" data-act="nriPeriod">Show</button>` : ''}</div></div>${P['nri-t'](d)}</div>`;
  }
};
P['nri-t'] = d => {
  if (UI.nriTab === 'chk') return tbl([['Client'], ['Trade'], ['Time'], ['Trade'], ['Rule']], d.violations.map(x => `<tr><td><button class="linkish mono" data-act="client" data-id="${x.ucc}">${x.ucc}</button> ${esc(x.name)}</td><td class="n">${esc(x.trade_no)}</td><td class="n">${esc(x.time)}</td><td>${esc(x.what)}</td><td class="neg">${esc(x.rule)}</td></tr>`), { tall: true, empty: 'No NRI trade breaks a rule today.' });
  if (UI.nriTab === 'tds') return tbl([['Client'], ['Sold on'], ['Scrip'], ['Qty', 'r'], ['Category'], ['Gain', 'r'], ['Exempt', 'r'], ['Rate', 'r'], ['Tax', 'r'], ['Cess', 'r'], ['TDS', 'r']], d.tdsp.map(x => `<tr><td><button class="linkish mono" data-act="client" data-id="${x.ucc}">${x.ucc}</button> ${esc(x.name)}</td><td class="n">${fmtDate(x.date)}</td><td>${esc(x.symbol)}</td><td class="r">${nf(x.qty)}</td><td class="small">${esc(x.category)}</td><td class="r ${sgn(x.gain)}">${nf(x.gain, 2)}</td><td class="r">${num(x.exempt) ? nf(x.exempt, 2) : '—'}</td><td class="r">${esc(x.rate_pct)}%</td><td class="r">${nf(x.tax, 2)}</td><td class="r">${nf(x.cess, 2)}</td><td class="r"><b>${nf(x.tds, 2)}</b></td></tr>`), { tall: true, empty: 'No NRI sales in this period.', foot: d.tdsp.length ? `<tfoot><tr><td colspan="10">Total</td><td class="r">${nf(sum(d.tdsp, x => num(x.tds)), 2)}</td></tr></tfoot>` : '' }) + '<div class="pad"><span class="dim small">Gains from the same first-in first-out purchase lots as the capital gains report. Where the purchase cost is not known, the whole sale value is treated as gain until the cost is entered (Trades elsewhere or the HOLDING-LOTS file). Surcharge depends on total income and is applied by the PIS bank.</span></div>';
  if (UI.nriTab === 'rbi') return tbl([['Company'], ['ISIN'], ['Status']], d.caution.map(x => `<tr><td>${esc(x.symbol)}</td><td class="mono">${esc(x.isin || '—')}</td><td>${pill(x.status, x.status === 'Ban' ? 'bad' : 'warn')}</td></tr>`), { empty: 'Load the RBI list as an NRI-CAUTION file on the Files page.' });
  return tbl([['Client'], ['Account'], ['PIS bank'], ['PIS account'], ['Permission'], ['Country'], ['CP code'], ['Holdings', 'r'], ['Today', 'r'], ['']], d.rows.map(r => `<tr><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}</td><td>${r.details_missing ? pill('Details missing', 'bad') : pill(r.account_type, r.account_type === 'NRE' ? 'acc' : 'nt')}</td><td>${esc(r.pis_bank || '—')}<div class="dim small">${esc(r.pis_branch || '')}</div></td><td class="mono">${esc(r.pis_account || '—')}</td><td class="small">${esc(r.pis_permission || '—')}${r.pis_permission_date ? `<div class="dim">${fmtDate(r.pis_permission_date)}</div>` : ''}</td><td>${esc(r.country || '—')}</td><td class="mono">${esc(r.cp_code || '—')}</td><td class="r">${inr(r.holdings_value)}</td><td class="r">${r.trades ? r.trades + ' trades' : '—'}</td><td>${has('Ops checker', 'Compliance officer') ? `<button class="btn sm" data-act="nriSet" data-id="${r.ucc}">Edit</button>` : ''}</td></tr>`), { tall: true, empty: 'No NRI clients. Register one with “Register NRI”.' });
};
function nriModal(ucc) {
  const r = (C.nri.rows.find(x => x.ucc === ucc)) || {};
  clientCache().then(() => {
    const f = (id, l, v) => `<div class="fld"><label for="nr-${id}">${l}</label><input class="in" id="nr-${id}" value="${esc(v || '')}"></div>`;
    modal(mhd(ucc ? 'NRI details · ' + ucc : 'Register an NRI client', 'Marks the client as NRI (a PMLA special category) and records the PIS details.') + `<form class="bd" id="nrf"><div class="form">
      ${ucc ? `<input type="hidden" id="nr-u" value="${ucc}">` : `<div class="fld full"><label for="nr-u-q">Client</label>${cpick('nr-u', '', {})}</div>`}
      <div class="fld"><label for="nr-acc">Account</label><select class="in" id="nr-acc"><option ${r.account_type === 'NRE' ? 'selected' : ''}>NRE</option><option ${r.account_type === 'NRO' ? 'selected' : ''}>NRO</option></select></div>
      ${f('bank', 'Designated PIS bank', r.pis_bank)}${f('branch', 'Branch', r.pis_branch)}${f('pis', 'PIS account number', r.pis_account)}${f('perm', 'PIS permission reference', r.pis_permission)}<div class="fld"><label for="nr-pd">Permission date</label><input class="in" type="date" id="nr-pd" value="${esc(r.pis_permission_date || '')}"></div>${f('ctry', 'Country of residence', r.country)}${f('cp', 'Custodial participant code (for F&O)', r.cp_code)}</div>
      <div id="nr-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
    $('nrf').onsubmit = async e => { e.preventDefault(); try { await post('/v1/nri', { ucc: $('nr-u').value, account_type: $('nr-acc').value, pis_bank: $('nr-bank').value, pis_branch: $('nr-branch').value, pis_account: $('nr-pis').value, pis_permission: $('nr-perm').value, pis_permission_date: $('nr-pd').value, country: $('nr-ctry').value, cp_code: $('nr-cp').value }); closeModal(); toast('NRI details saved'); render({ keep: true }); } catch (err) { $('nr-err').innerHTML = errHTML(err); } };
  });
}

/* ---------- autopilot & work queue ---------- */
const STAGE_NAMES = { bod: 'Start of day', intraday: 'Intraday', preclose: 'Pre-close', eod: 'End of day', monthly: 'Month end', event: 'When it happens' };
V.autopilot = {
  load: () => get(`/v1/autopilot?date=${S.date}`),
  html: d => {
    const adm = has('Admin'), run = has('Admin', 'Ops checker');
    return ph('Autopilot', 'Routine work runs by itself and records what it did; people get only the exceptions, in the work queue, each with the role who should act, a due time and, where possible, the action already prepared. Where the rules need a person’s judgement or signature, automation stops at preparing the work.',
      `${S.me?.dev ? '<button class="btn" data-act="apDay">Run a sample day on autopilot</button>' : ''}${run ? `<select class="in sm" id="ap-stage">${['bod', 'intraday', 'preclose', 'eod', 'monthly'].map(s => `<option value="${s}">${STAGE_NAMES[s]}</option>`).join('')}</select><button class="btn" data-act="apRun">Run stage now</button>` : ''}${adm ? `<button class="btn ${d.scheduled ? '' : 'pri'}" data-act="apSchedule" data-on="${d.scheduled ? 0 : 1}">${d.scheduled ? 'Switch schedule off' : 'Switch schedule on'}</button>` : ''}`) +
    editionNote(d.edition) + `<div class="kpis">${kpi('Done automatically', nf(d.done_today), 'actions on ' + fmtDate(S.date), d.done_today ? 'good' : '')}${kpi('Straight-through', nf(d.stp_pct, 0) + '%', 'of today’s work needed no one', d.stp_pct >= 80 ? 'good' : d.stp_pct ? 'warn' : '')}${kpi('Time saved', nf(d.minutes_saved_today / 60, 1) + ' h', 'estimated, today')}${kpi('Work queue', d.open, d.overdue + ' overdue', d.overdue ? 'bad' : d.open ? 'warn' : 'good')}${kpi('Schedule', d.scheduled ? 'On' : 'Off', d.scheduled ? 'runs on business days' : 'run stages by hand', d.scheduled ? 'good' : '')}</div>
    <div class="stages">${d.stages.map(s => `<div class="stage ${s.status === 'Failed' ? 'bad' : s.status ? 'ok' : ''}"><small>${esc(s.at || '')}</small><b>${esc(s.name)}</b><span class="dim small">${s.last ? esc(s.summary || s.status) : 'not run yet'}</span></div>`).join('')}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('apTab', [['work', `Work queue (${d.open})`], ['pol', 'Automations'], ['log', 'Run log'], ['human', 'Stays with people']])}${UI.apTab === 'work' ? segBtns('apMine', [['mine', 'For my role'], ['all', 'Everyone'], ['closed', 'Closed']], 'ap-t') : ''}</div></div><div id="ap-t">${P['ap-t'](d)}</div></div>`;
  }
};
P['ap-t'] = d => {
  if (UI.apTab === 'pol') {
    const areas = [...new Set(d.policies.map(p => p.area))], edit = has('Admin', 'Compliance officer');
    return areas.map(a => `<div class="pad" style="padding-bottom:0"><h3 style="margin:0;font-size:15px">${esc(a)}</h3></div>` + tbl([['Automation'], ['Runs'], ['Mode'], ['Settings'], ['Last run']], d.policies.filter(p => p.area === a).map(p => {
      const modes = ['Auto', 'Suggest', 'Off'].filter(m => ({ Off: 0, Suggest: 1, Auto: 2 })[m] <= ({ Human: 0, Off: 0, Suggest: 1, Auto: 2 })[p.cap]);
      const lr = p.last_run;
      return `<tr><td style="max-width:420px"><b>${esc(p.name)}</b><div class="dim small wrap">${esc(p.about)}</div>${p.cap !== 'Auto' ? `<div class="small"><span class="pill warn">At most ${esc(p.cap)}</span> <span class="dim">${esc(p.cap_why)}</span></div>` : ''}</td><td class="small">${STAGE_NAMES[p.stage]}</td>
        <td>${edit ? `<select class="in sm" data-appol="${p.id}">${modes.map(m => `<option ${m === p.mode ? 'selected' : ''}>${m}</option>`).join('')}</select>` : pill(p.mode, p.mode === 'Auto' ? 'good' : p.mode === 'Suggest' ? 'acc' : 'nt')}</td>
        <td class="small">${Object.entries(p.settings || {}).map(([k, v]) => `<label class="dim">${esc(PARAM_LABELS[k] || k)}</label> <input class="in sm mono" style="width:100px" data-apparam="${p.id}" data-k="${k}" value="${esc(v)}" ${edit ? '' : 'disabled'}>`).join('<br>') || '<span class="dim">—</span>'}</td>
        <td class="small wrap">${lr ? `${pill(lr.error ? 'Failed' : `${lr.done} done`, lr.error ? 'bad' : 'good')}${lr.queued ? ' ' + pill(lr.queued + ' queued', 'warn') : ''}<div class="dim">${esc(lr.error || lr.note || '')}</div><div class="dim">${esc(lr.at.slice(0, 16).replace('T', ' '))}</div>` : '<span class="dim">not run yet</span>'}${has('Admin', 'Ops checker') && autoFnIds.includes(p.id) ? ` <button class="btn sm" data-act="apRunOne" data-id="${p.id}">Run</button>` : ''}</td></tr>`;
    }))).join('') + (edit ? '<div class="pad"><button class="btn pri" data-act="apSave">Save automation settings</button> <span class="dim small">Changes are recorded in the audit trail.</span></div>' : '');
  }
  if (UI.apTab === 'log') return tbl([['When'], ['For'], ['Automation'], ['Mode'], ['Done', 'r'], ['Queued', 'r'], ['Result']], d.runs.filter(r => r.policy).map(r => `<tr><td class="n">${esc(r.at.slice(5, 16).replace('T', ' '))}</td><td class="n">${fmtDate(r.date)}</td><td>${esc((d.policies.find(p => p.id === r.policy) || {}).name || r.policy)}</td><td>${esc(r.mode)}</td><td class="r">${r.done}</td><td class="r">${r.queued}</td><td class="wrap small">${r.error ? `<span class="neg">${esc(r.error)}</span>` : esc(r.note || '')}</td></tr>`), { tall: true, empty: 'Nothing has run yet.' });
  if (UI.apTab === 'human') return tbl([['Stays with people'], ['Why']], d.human_only.map(h => `<tr><td><b>${esc(h.process)}</b></td><td class="wrap">${esc(h.why)}</td></tr>`)) + '<div class="pad"><span class="dim small">For these the system still prepares everything it can (evidence, documents, suggested decision) and the item appears in the work queue.</span></div>';
  const role = S.me?.role, mine = UI.apMine;
  const ws = d.work.filter(w => mine === 'closed' ? w.status !== 'Open' : w.status === 'Open' && (mine === 'all' || !w.role || w.role === role || role === 'Admin'));
  const st = { Done: 'good', Dismissed: 'nt', Cleared: 'nt', Open: 'warn' };
  return tbl([['Item'], ['For'], ['Due'], ['Status'], ['']], ws.map(w => `<tr><td style="max-width:560px"><b>${esc(w.title)}</b>${w.ucc ? ` <button class="linkish mono" data-act="client" data-id="${w.ucc}">${w.ucc}</button>` : ''}<div class="dim small wrap">${esc(w.detail)}</div><div class="dim small">${esc((d.policies.find(p => p.id === w.process) || {}).name || w.process)} · ${fmtDate(w.date)}</div></td><td class="small">${esc(w.role || 'Anyone')}</td><td class="n ${w.overdue ? 'neg' : ''}">${esc(w.due)}${w.overdue ? '<div class="small">overdue</div>' : ''}</td><td>${pill(w.status, st[w.status])}${w.closed_by ? `<div class="dim small">${esc(w.closed_by)}${w.note ? ': ' + esc(w.note) : ''}</div>` : ''}</td>
    <td>${w.status === 'Open' ? `${w.proposed ? `<button class="btn sm pri" data-act="wDo" data-id="${w.id}">${esc(w.proposed.label)}</button>` : ''}<button class="btn sm" data-act="wOpen" data-id="${w.id}">Open</button><button class="btn sm" data-act="wDismiss" data-id="${w.id}">Done elsewhere</button>` : ''}</td></tr>`), { tall: true, empty: mine === 'closed' ? 'Nothing closed yet.' : 'Nothing waiting. Everything else ran by itself.' });
};
const PARAM_LABELS = { limit: 'up to ₹', at: 'at', from: 'from (MM-DD)', to: 'to (MM-DD)', min_saving: 'min. saving ₹', remind_days: 'remind, days before', days: 'days', min_gain: 'min. gains ₹', min: 'min. ₹' };
const autoFnIds = ['ins_tax_harvest', 'ins_buyback', 'ins_fo_expiry', 'ins_advance_tax', 'ins_corp_action', 'ins_idle_funds', 'ins_pledge_margin', 'ins_dormant', 'ins_outcomes', 'debit_blocks', 'pledges', 'third_party', 'unidentified', 'payouts', 'payout_batch', 'recon', 'billing', 'settlement', 'notes', 'margin_calls', 'square_off', 'mtf_liquidation', 'penalties', 'reports', 'surveillance', 'nri', 'kyc', 'inactive', 'service', 'leads', 'day_close', 'monthly', 'ras'];

/* ---------- settlement ---------- */
V.settlement = {
  load: async () => { const [st, sh] = await Promise.all([get(`/v1/settlement?date=${S.date}`), get('/v1/shortages')]); return { ...st, shortages: sh }; },
  html: d => {
    const exc = d.obligations.filter(o => ['Short delivery', 'Funds shortfall', 'Unpledge & pay-in'].includes(o.status));
    const ready = d.obligations.filter(o => o.action === 'payin' || o.action === 'payout').length;
    return ph('Settlement', `Cash-market delivery obligations for pay-in on ${fmtDate(d.pay_in)} (T+1). Securities pay-in from the client’s demat; payout credited to the client’s demat; unpaid purchases held in the client unpaid securities pledgee account.`,
      `<button class="btn" data-act="dl" data-id="/v1/reports/trades/download?date=${S.date}" data-name="trades_${S.date}.csv">Trade register</button>${has('Ops maker', 'Ops checker') ? `<button class="btn pri" data-act="settleReady" ${ready ? '' : 'disabled'}>Settle ${ready} ready line${ready === 1 ? '' : 's'}</button>` : ''}`) +
    `<div class="kpis">${kpi('Funds pay-in (buys)', cmp(d.totals.funds_pay_in), d.obligations.filter(o => o.net_qty > 0).length + ' client-scrip lines')}${kpi('Funds payout (sells)', cmp(d.totals.funds_pay_out), d.obligations.filter(o => o.net_qty < 0).length + ' lines')}${kpi('Net to clearing', cmp(d.totals.net_to_cc), 'before charges')}${kpi('Exceptions', exc.length, 'short, unpledge, funds', exc.length ? 'bad' : 'good')}${kpi('Held in CUSPA', d.cuspa.filter(c => c.status === 'Held in CUSPA').length, 'unpaid securities', d.cuspa.some(c => c.status === 'Held in CUSPA') ? 'warn' : '')}</div>
    <div class="panel"><div class="hd">${segBtns('settleTab', [['cm', 'Cash market obligations'], ['mtm', 'Derivatives MTM'], ['cuspa', 'Unpaid securities'], ['short', `Short deliveries · ${d.shortages.filter(x => x.status === 'Open').length}`]], 'settle-t')}</div><div id="settle-t">${P['settle-t'](d)}</div></div>`;
  }
};
P['settle-t'] = d => {
  if (UI.settleTab === 'short') return `<div class="pad"><div class="note">A short delivery is closed out <b>internally</b> when another client of the firm bought the same scrip for the same settlement: the seller is debited and the buyer credited at the close-out price instead of receiving the shares. The rest goes to the <b>exchange auction</b>, whose cost the clearing corporation recovers from the firm. Suggested price: the higher of the highest close since the trade date and today’s close plus 20%.</div></div>` +
    tbl([['Shortage'], ['Seller'], ['Scrip'], ['Short', 'r'], ['Sold at', 'r'], ['Internal / auction'], ['Buyers hit'], ['Price', 'r'], ['Status'], ['']], d.shortages.map(x => `<tr><td class="n">${x.id}<div class="dim small">T ${fmtDate(x.date)}</div></td><td><button class="linkish mono" data-act="client" data-id="${x.ucc}">${x.ucc}</button> ${esc(x.name)}</td><td>${x.exchange} ${esc(x.symbol)}</td><td class="r">${nf(x.qty)}</td><td class="r">${nf(x.sell_price, 2)}</td><td>${x.internal} / ${x.auction}</td><td class="wrap small">${x.buyers.map(b => `${b.ucc} ×${b.short}${num(b.credit) ? ' · ' + inr(b.credit, 2) : ''}`).join('<br>') || '—'}</td><td class="r">${x.status === 'Open' ? `<span class="dim">suggest</span> ${nf(x.suggested, 2)}` : nf(x.price, 2)}</td><td>${pill(x.status, x.status === 'Open' ? 'bad' : 'good')}${x.status !== 'Open' ? `<div class="dim small">debit ${inr(x.debit, 2)} · loss ${inr(x.loss, 2)}</div>` : ''}</td><td>${x.status === 'Open' && has('Ops checker') ? `<button class="btn sm pri" data-act="shortSettle" data-id="${x.id}">Close out</button>` : ''}</td></tr>`), { empty: 'No short deliveries reported.' });
  if (UI.settleTab === 'mtm') return tbl([['UCC'], ['Client'], ['Segment'], ['MTM / premium (₹)', 'r'], ['Settles']], d.mtm.map(m => `<tr><td class="n">${m.ucc}</td><td>${esc(m.name)}</td><td>${esc(m.segment)}</td><td class="r ${sgn(m.amount)}">${inr(m.amount, 2)}</td><td class="dim">${fmtDate(d.pay_in)}</td></tr>`), { tall: true, empty: 'No derivatives bills posted yet.', foot: d.mtm.length ? `<tfoot><tr><td colspan="3">Net</td><td class="r">${inr(sum(d.mtm, m => num(m.amount)), 2)}</td><td></td></tr></tfoot>` : '' });
  if (UI.settleTab === 'cuspa') return `<div class="bd"><div class="note">Securities bought by clients who have not paid are held in the client unpaid securities pledgee account. Release them once the client’s ledger is no longer in debit.</div></div>` + tbl([['ID'], ['UCC'], ['Scrip'], ['Qty', 'r'], ['Value (₹)', 'r'], ['Status'], ['']], d.cuspa.map(c => `<tr><td class="n">${c.id}</td><td class="n">${c.ucc}</td><td>${esc(c.symbol)} <span class="tag">${c.isin}</span></td><td class="r">${nf(c.qty)}</td><td class="r">${inr(c.value)}</td><td>${pill(c.status, c.status === 'Released' ? 'good' : 'warn')}</td><td>${c.status === 'Held in CUSPA' && has('Ops maker', 'Ops checker') ? `<button class="btn sm" data-act="cuspaRel" data-id="${c.id}">Release</button>` : ''}</td></tr>`), { empty: 'Nothing held. Funds-shortfall buy lines can be moved here from the obligations tab.' });
  const k = { 'Ready for pay-in': 'acc', Funded: 'acc', 'Paid in': 'good', 'Paid out': 'good', 'Held in CUSPA': 'warn', 'Short delivery': 'bad', 'Short reported': 'nt', 'Funds shortfall': 'bad', 'Unpledge & pay-in': 'warn', Netted: 'nt' };
  const btn = { payin: 'Pay in', payout: 'Pay out', unpledge_payin: 'Unpledge', report_short: 'Report short', cuspa: 'To CUSPA' };
  const can = has('Ops maker', 'Ops checker');
  return tbl([['Client'], ['Exch'], ['Scrip'], ['Buy', 'r'], ['Sell', 'r'], ['Net qty', 'r'], ['Funds (₹)', 'r'], ['Free / held', 'r'], ['Status'], ['Action']], d.obligations.map(o => `<tr><td><button class="linkish mono" data-act="client" data-id="${o.ucc}">${o.ucc}</button><div class="dim" style="font-size:11.5px">${esc(o.name)}</div></td><td>${o.exchange}</td><td title="${esc(o.isin)}">${esc(o.symbol)}</td><td class="r">${nf(o.buy_qty)}</td><td class="r">${nf(o.sell_qty)}</td><td class="r ${sgn(o.net_qty)}">${nf(o.net_qty)}</td><td class="r ${sgn(o.funds)}">${inr(o.funds, 2)}</td><td class="r">${o.net_qty < 0 ? nf(o.free) + (o.holding !== o.free ? ` <span class="dim">/ ${nf(o.holding)}</span>` : '') : '—'}</td><td>${pill(o.status, k[o.status] || 'nt')}</td><td>${o.action && can ? `<button class="btn sm ${o.action === 'report_short' ? 'bad' : ''}" data-act="settle" data-id="${esc(o.key)}" data-a="${o.action}">${btn[o.action]}</button>` : ''}</td></tr>`), { tall: true, empty: 'No delivery trades for this date.' });
};

/* ---------- contract notes ---------- */
V.notes = {
  load: () => get(`/v1/notes?date=${S.date}`),
  html: d => {
    const by = s => d.filter(n => n.status === s).length;
    const can = has('Ops maker', 'Ops checker');
    return ph('Contract notes', 'One electronic contract note per client per trade date for equity (NSE and BSE), and a separate one for commodity trades (MCX), issued after the commodity close. Each has every trade and the full charge breakdown, and must be delivered within 24 hours. The copy sent is a PDF digitally signed with the firm’s certificate; without a certificate or a mail server, notes are held, never sent unsigned. Every delivery, bounce and printed copy is in the <button type="button" class="linkish" data-act="go" data-id="dispatch">Dispatch register</button>.',
      can ? `<button class="btn" data-act="genNotes">Generate notes</button><button class="btn pri" data-act="sendNotes" ${by('Generated') + by('Bounced') + by('Held') + by('Failed') ? '' : 'disabled'}>Send pending</button>` : '') +
    `<div class="kpis">${kpi('Generated', d.length, 'clients who traded')}${kpi('Delivered', by('Sent'), 'accepted by mail relay', 'good')}${kpi('Pending', by('Generated'), 'not yet sent', by('Generated') ? 'warn' : '')}${kpi('Bounced', by('Bounced'), 'fix email, then re-send', by('Bounced') ? 'bad' : '')}</div>
    <div class="panel"><div class="hd">${segBtns('noteF', ['All', 'Sent', 'Generated', 'Bounced', 'Held', 'Paper'], 'notes-t')}<small>${d.length ? '' : 'Run billing, then generate'}</small></div><div id="notes-t">${P['notes-t'](d)}</div></div>`;
  }
};
P['notes-t'] = d => tbl([['Contract note'], ['UCC'], ['Client'], ['Email'], ['Trades', 'r'], ['Net (₹)', 'r'], ['Status'], ['Sent'], ['']], d.filter(n => UI.noteF === 'All' || n.status === UI.noteF).map(n => `<tr><td class="n">${esc(n.no)}${n.part === 'commodity' ? ' ' + pill('Commodity', 'warn') : ''}${n.revision ? ` <span title="Revision ${n.revision}: replaces ${esc(n.replaces)}">${pill('Revised', 'acc')}</span>` : ''}</td><td class="n">${n.ucc}</td><td>${esc(n.name)}</td><td class="dim">${esc(n.email)}</td><td class="r">${n.trades}</td><td class="r ${sgn(n.net)}">${inr(n.net, 2)}</td><td>${pill(n.status === 'Paper' ? 'Paper copy due' : n.status, n.status === 'Sent' ? 'good' : n.status === 'Bounced' || n.status === 'Failed' ? 'bad' : 'warn')}</td><td class="n dim">${esc(n.sent_at || '—')}</td><td><button class="btn sm" data-act="noteDoc" data-id="${n.ucc}|${n.part || ''}">Open</button>${n.dispatch ? `<button class="btn sm" data-act="dspOpen" data-id="${n.dispatch}">Signed PDF</button>` : ''}${has('Ops maker', 'Ops checker') ? `<button class="btn sm" data-act="sendNote" data-id="${n.ucc}">${n.status === 'Generated' ? 'Send' : 'Resend'}</button>` : ''}</td></tr>`), { tall: true, empty: 'No contract notes for this date.' });

/* ---------- clients ---------- */
const STAGES = ['Application', 'KYC verification', 'UCC upload', 'Active'];
/* ---------- client picker (search by name, UCC, PAN or group) ---------- */
const CL = { date: '', rows: [], tags: [] };
async function clientCache(force) {
  if (force || CL.date !== S.date || !CL.rows.length) {
    const [rows, tags] = await Promise.all([get(`/v1/clients?date=${S.date}`), get('/v1/tags').catch(() => [])]);
    Object.assign(CL, { date: S.date, rows, tags });
  }
  return CL;
}
const CP = {};
function cpLabel(v) {
  if (!v) return '';
  if (v === 'ALL') return 'All clients';
  if (v.startsWith('TAG:')) return 'Group ' + v.slice(4);
  const c = CL.rows.find(x => x.ucc === v);
  return c ? `${c.ucc} · ${c.name}` : v;
}
function cpick(id, val, opt = {}) {
  CP[id] = opt;
  return `<div class="cpick"><input class="in cp-q" data-cp="${id}" id="${id}-q" placeholder="${esc(opt.placeholder || 'Search name, UCC or PAN')}" autocomplete="off" spellcheck="false" value="${esc(cpLabel(val))}" role="combobox" aria-expanded="false"><input type="hidden" id="${id}" value="${esc(val || '')}"><div class="res cp-res" id="${id}-res" hidden role="listbox"></div></div>`;
}
function cpMatches(id, q) {
  const opt = CP[id] || {}; q = q.trim().toLowerCase();
  const fixed = [];
  if (opt.none) fixed.push({ v: '', l: opt.none, s: '' });
  if (opt.all) fixed.push({ v: 'ALL', l: 'All clients', s: CL.rows.filter(c => c.status === 'Active').length + ' clients' });
  if (opt.groups) CL.tags.forEach(t => fixed.push({ v: 'TAG:' + t.tag, l: 'Group ' + t.tag, s: t.clients + ' client' + (t.clients === 1 ? '' : 's') }));
  const fx = fixed.filter(o => !q || o.l.toLowerCase().includes(q));
  const cs = CL.rows.filter(c => (opt.inactive || c.status === 'Active') && (!q || (c.ucc + ' ' + c.name + ' ' + c.pan + ' ' + (c.tags || []).join(' ')).toLowerCase().includes(q)));
  return fx.concat(cs.slice(0, 40).map(c => ({ v: c.ucc, l: c.name, s: c.ucc + ((c.tags || []).length ? ' · ' + c.tags.join(', ') : '') })));
}
function cpOpen(id) {
  const q = $(id + '-q'), r = $(id + '-res'); if (!q || !r) return;
  const m = cpMatches(id, q.value === cpLabel($(id).value) ? '' : q.value);
  r.innerHTML = m.length ? m.map((o, i) => `<button type="button" role="option" class="${i === 0 ? 'hl' : ''}" data-act="cpPick" data-id="${id}" data-v="${esc(o.v)}"><span>${esc(o.l)}</span><span class="mono dim">${esc(o.s)}</span></button>`).join('') : '<div class="empty">No client matches</div>';
  r.hidden = false; q.setAttribute('aria-expanded', 'true');
}
function cpClose(id) { const r = $(id + '-res'), q = $(id + '-q'); if (r) r.hidden = true; if (q) { q.setAttribute('aria-expanded', 'false'); q.value = cpLabel($(id).value); } }
function cpSet(id, v) { $(id).value = v; cpClose(id); const o = CP[id]; if (o?.on) o.on(v); }

/* ---------- clients ---------- */
V.clients = {
  load: async () => { const [rows] = await Promise.all([get(`/v1/clients?date=${S.date}`), clientCache(true)]); return { rows, tags: CL.tags }; },
  html: d => {
    const cnt = s => d.rows.filter(c => c.status === s).length;
    return ph('Clients & onboarding', 'Applications move through KYC verification and UCC upload. Activation needs an Ops checker who did not create the application. Groups (tags) such as HNI or STAFF drive brokerage plans and group reports.',
      `<button class="btn" data-act="dl" data-id="/v1/reports/ucc/download?date=${S.date}" data-name="ucc_${S.date}.csv">UCC upload file</button>${has('Ops maker', 'Ops checker') ? '<button class="btn pri" data-act="newClient">New client</button>' : ''}`) +
    `<div class="pipe">${STAGES.map(s => `<button class="${UI.clientStage === s ? 'on' : ''}" data-act="ui" data-k="clientStage" data-v="${s}"><small>${s}</small><b>${cnt(s)}</b></button>`).join('')}</div>
    <div class="panel"><div class="hd"><div class="tools"><button class="btn sm ${UI.clientStage === 'All' ? 'pri' : ''}" data-act="ui" data-k="clientStage" data-v="All">All clients</button><input class="in" data-ui="clientQ" data-part="clients-t" placeholder="Search name, UCC, PAN, city" value="${esc(UI.clientQ)}">
      <select class="in" id="clientTag" aria-label="Group"><option value="">All groups</option><option value="-" ${UI.clientTag === '-' ? 'selected' : ''}>No group</option>${d.tags.map(t => `<option value="${esc(t.tag)}" ${UI.clientTag === t.tag ? 'selected' : ''}>${esc(t.tag)} · ${t.clients}</option>`).join('')}</select></div><small>${d.rows.length} records</small></div>
      <div id="clients-t">${P['clients-t'](d)}</div></div>`;
  }
};
P['clients-t'] = d => {
  let rows = d.rows; if (UI.clientStage !== 'All') rows = rows.filter(c => c.status === UI.clientStage);
  const q = UI.clientQ.trim().toLowerCase(); if (q) rows = rows.filter(c => (c.name + ' ' + c.ucc + ' ' + c.city + ' ' + c.pan + ' ' + (c.tags || []).join(' ')).toLowerCase().includes(q));
  if (UI.clientTag === '-') rows = rows.filter(c => !(c.tags || []).length); else if (UI.clientTag) rows = rows.filter(c => (c.tags || []).includes(UI.clientTag));
  const st = { OK: 'good', Watch: 'warn', Breach: 'bad', Debit: 'warn' };
  const canTag = has('Ops maker', 'Ops checker');
  for (const u of [...UI.clientSel]) if (!d.rows.some(c => c.ucc === u)) UI.clientSel.delete(u);
  const sel = UI.clientSel, allOn = rows.length && rows.every(c => sel.has(c.ucc));
  const bulk = canTag && sel.size ? `<div class="bulkbar"><b>${sel.size} selected</b><input class="in" id="bulk-tag" list="tag-list" placeholder="Group, e.g. HNI"><datalist id="tag-list">${d.tags.map(t => `<option value="${esc(t.tag)}">`).join('')}</datalist><button class="btn sm pri" data-act="bulkTag" data-id="add">Add to group</button><button class="btn sm" data-act="bulkTag" data-id="remove">Remove from group</button><button class="btn sm" data-act="bulkClear">Clear selection</button></div>` : '';
  return bulk + tbl([...(canTag ? [[`<input type="checkbox" data-act="selAll" aria-label="Select all shown" ${allOn ? 'checked' : ''}>`]] : []), ['UCC'], ['Name'], ['Groups'], ['Type'], ['PAN'], ['Segments'], ['Ledger (₹)', 'r'], ['RMS'], ['Stage'], ['']], rows.map(c => `<tr class="${sel.has(c.ucc) ? 'sel' : ''}">${canTag ? `<td><input type="checkbox" data-act="selOne" data-id="${c.ucc}" aria-label="Select ${c.ucc}" ${sel.has(c.ucc) ? 'checked' : ''}></td>` : ''}<td class="n"><button class="linkish" data-act="client" data-id="${c.ucc}">${c.ucc}</button></td><td>${esc(c.name)}</td><td>${(c.tags || []).map(t => `<button class="gtag" data-act="filterTag" data-id="${esc(t)}">${esc(t)}</button>`).join(' ') || '<span class="dim">—</span>'}</td><td>${esc(c.type)}</td><td class="n">${esc(c.pan)}</td><td>${(c.segments || []).map(s => `<span class="tag">${s}</span>`).join(' ')}</td><td class="r ${sgn(c.funds)}">${c.status === 'Active' ? inr(c.funds) : '—'}</td><td>${c.inactive ? pill('Inactive', 'nt') : c.risk_status ? pill(c.risk_status, st[c.risk_status]) : ''}${c.blocked ? ' ' + pill('Blocked', 'bad') : ''}</td><td>${pill(c.status, c.status === 'Active' ? 'good' : 'acc')}</td><td>${c.status !== 'Active' && (c.status === 'UCC upload' ? has('Ops checker') : has('Ops maker', 'Ops checker')) ? `<button class="btn sm" data-act="advance" data-id="${c.ucc}">${c.status === 'UCC upload' ? 'Activate' : 'Advance'}</button>` : ''}</td></tr>`), { tall: true, empty: 'No clients match' });
};
// Full PAN and bank account are masked; checkers, compliance and admins can
// see one with a reason, which goes into the audit trail.
const revealBtn = (ucc, f) => has('Ops checker', 'Compliance officer') ? ` <button class="linkish small" data-act="reveal" data-id="${esc(ucc)}" data-f="${f}">Show</button>` : '';
async function clientDrawer(ucc) {
  modal(`<div class="bd"><p class="loading">Loading ${esc(ucc)}…</p></div>`, true);
  let d, bp, docs; try { [d, bp, docs] = await Promise.all([get(`/v1/clients/${encodeURIComponent(ucc)}?date=${S.date}`), get(`/v1/brokerage/client/${encodeURIComponent(ucc)}?date=${S.date}`).catch(() => null), get(`/v1/clients/${encodeURIComponent(ucc)}/documents`).catch(() => [])]); } catch (e) { modal(mhd('Client', '') + `<div class="bd">${errHTML(e)}</div>`, true); return; }
  const c = d.client, r = d.risk, st = { OK: 'good', Watch: 'warn', Breach: 'bad', Debit: 'warn' };
  const lines = d.statement.lines.slice(-12);
  const h3 = t => `<h3 style="margin:0 0 8px;font-family:var(--f-cond);font-size:15px">${t}</h3>`;
  modal(mhd(esc(c.name), `${c.ucc} · ${esc(c.type)} · ${esc(c.city)}`) + `<div class="bd">
    <div class="tools">${pill(c.status, c.status === 'Active' ? 'good' : 'acc')}${r ? pill('RMS: ' + r.status, st[r.status]) : ''}${c.blocked ? pill('Blocked: ' + (c.blocked_reason || ''), 'bad') : ''}${(c.segments || []).map(s => `<span class="tag">${esc(d.segment_names[s] || s)}</span>`).join('')}</div>
    <div class="grouprow"><div><small class="dim">Groups</small><div class="tools">${(c.tags || []).map(t => `<span class="gtag">${esc(t)}</span>`).join('') || '<span class="dim">None</span>'}${has('Ops maker', 'Ops checker') ? `<button class="btn sm" data-act="editTags" data-id="${c.ucc}">Edit groups</button>` : ''}</div></div>
    ${c.status === 'Active' ? `<div><small class="dim">Account activity</small><div>${c.inactive ? `${pill('Inactive', 'nt')} since ${fmtDate(c.inactive_since)}${has('Ops maker', 'Ops checker') ? ` <button class="btn sm" data-act="reactivate" data-id="${c.ucc}">Request reactivation</button>` : ''}` : `Active${c.reactivated_on ? ` · reactivated ${fmtDate(c.reactivated_on)}` : ''} <button class="linkish" data-act="go" data-id="inactive">Inactivity rules</button>`}${has('Ops maker', 'Ops checker') ? ` <button class="linkish" data-act="contact" data-id="${c.ucc}">Update contact details</button>` : ''}</div></div>` : ''}
    <div><small class="dim">Branch and partner</small><div>${esc(c.branch || 'No branch')}${c.partner ? ' · AP ' + esc(c.partner) : ''}${c.rm ? ' · RM ' + esc(c.rm) : ''}${c.referred_by ? ` · referred by <button class="linkish mono" data-act="client" data-id="${c.referred_by}">${c.referred_by}</button>` : ''}${has('Ops checker', 'Admin') ? ` <button class="btn sm" data-act="mapClient" data-id="${c.ucc}">Change</button>` : ''}${c.private ? ' ' + pill('Private', 'warn') : ''}${has('Admin', 'Compliance officer') ? ` <button class="linkish" data-act="privacy" data-id="${c.ucc}" data-p="${c.private ? 0 : 1}">${c.private ? 'Make visible to all' : 'Make private'}</button>` : ''}</div></div>
    <div><small class="dim">WhatsApp</small><div>${waConsentLine(c)}${has('Ops maker', 'Ops checker', 'Compliance officer', 'Branch staff', 'Authorised person') ? ` <button class="btn sm" data-act="waConsent" data-id="${c.ucc}" data-on="${c.whatsapp ? 0 : 1}">${c.whatsapp ? 'Record opt-out' : 'Record consent'}</button>` : ''}</div></div>
    ${bp ? `<div><small class="dim">Brokerage plan</small><div><b>${esc(bp.plan.plan_name)}</b> <span class="dim">· ${esc(bp.plan.source)}${bp.plan.from ? ' since ' + fmtDate(bp.plan.from) : ''}</span>${(bp.plan.segments || []).map(x => ` <span class="tag">${esc(scopeLabel(x.scope))}: ${esc(x.plan_name)}</span>`).join('')}${bp.plan.override ? ' <span class="tag">own rates</span>' : ''} <button class="linkish" data-act="go" data-id="brokerage">Plans</button></div><div class="dim small">${['CM-DEL', 'CM-INTRA', 'FO-FUT', 'FO-OPT'].map(k => `${{ 'CM-DEL': 'Delivery', 'CM-INTRA': 'Intraday', 'FO-FUT': 'Futures', 'FO-OPT': 'Options' }[k]}: ${esc(bp.rates[k] || '—')}`).join(' · ')}</div></div>` : ''}</div>
    ${r ? `<div class="kv"><div><small>Ledger</small><b class="mono ${sgn(r.funds)}">${inr(r.funds, 2)}</b></div><div><small>UPI blocked</small><b class="mono">${inr(r.upi)}</b></div><div><small>Collateral (usable)</small><b class="mono">${inr(r.collateral_usable)}</b></div><div><small>Margin required</small><b class="mono">${inr(r.required)}</b></div><div><small>Free</small><b class="mono ${sgn(r.free)}">${inr(r.free)}</b></div><div><small>OMS limit</small><b class="mono">${inr(r.limit)}</b></div></div>` : '<div class="note">Not active yet. Limits and the ledger start at activation.</div>'}
    <div class="kv"><div><small>PAN</small><b class="mono"><span id="rv-pan">${esc(c.pan)}</span>${revealBtn(c.ucc, 'pan')}</b></div><div><small>Bank</small><b>${esc(c.bank.bank)} · <span id="rv-account">${esc(c.bank.account)}</span>${revealBtn(c.ucc, 'account')}</b></div><div><small>IFSC</small><b class="mono">${esc(c.bank.ifsc)}</b></div><div><small>Demat</small><b class="mono">${esc(c.demat.depository)} ${esc(c.demat.id)}</b></div><div><small>Income</small><b>${esc(c.income)}</b></div><div><small>RAS</small><b>${esc(c.ras)}</b></div><div><small>Contract notes</small><b>${c.physical_notes ? 'Printed, by courier' : 'Electronic (signed PDF)'}</b></div><div><small>KYC date</small><b>${fmtDate(c.kyc_date)}</b></div><div><small>Email</small><b>${esc(c.email)}</b></div><div><small>Last trade</small><b>${fmtDate(c.last_trade) || '—'}</b></div></div>
    <div>${h3('Segments')}${tbl([['Segment'], ['Status'], ['Since'], ['Reason'], ['']], ['CM', 'FO', 'CD', 'COM'].filter(g => (c.segments || []).includes(g) || (c.seg_status || {})[g]).map(g => { const x = (c.seg_status || {})[g] || { status: 'Active' }; return `<tr><td>${esc(d.segment_names[g] || g)}</td><td>${pill(x.status, { Active: 'good', Reactivated: 'good', Suspended: 'bad', Dormant: 'warn', Closed: 'nt' }[x.status])}</td><td class="n">${x.since ? fmtDate(x.since) : '—'}</td><td class="small wrap">${esc(x.reason || '')}</td><td>${has('Ops checker', 'Compliance officer') ? `<button class="btn sm" data-act="segSet" data-id="${c.ucc}" data-seg="${g}">Change</button>` : ''}</td></tr>`; }))}${(c.seg_history || []).length ? `<details style="margin-top:6px"><summary class="dim small">History · ${c.seg_history.length} change${c.seg_history.length === 1 ? "" : "s"}</summary>${tbl([['Effective'], ['Segment'], ['Change'], ['Reason'], ['By']], c.seg_history.slice().reverse().map(h => `<tr><td class="n">${fmtDate(h.on)}</td><td>${h.segment}</td><td>${esc(h.from)} → <b>${esc(h.to)}</b></td><td class="small wrap">${esc(h.reason || '')}</td><td class="small">${esc(h.by)}${h.source === 'Import' ? ' <span class="dim">(import)</span>' : ''}</td></tr>`))}</details>` : ''}</div>
    <div>${h3('Nomination')}<div class="tools">${pill(nomState(c), nomState(c) === 'Nominated' || nomState(c) === 'Opted out (declared)' ? 'good' : 'warn')}${c.nomination_on ? `<span class="dim small">declared ${fmtDate(c.nomination_on)}${c.nomination_mode ? ' · ' + esc(c.nomination_mode) : ''}</span>` : ''}${has('Ops maker', 'Ops checker', 'Compliance officer', 'Branch staff') ? `<button class="btn sm" data-act="nomChange" data-id="${c.ucc}">Change nomination</button>` : ''}</div>
      ${(c.nominees || []).length ? tbl([['Nominee'], ['Relationship'], ['Share', 'r'], ['Identity'], ['Minor']], c.nominees.map(n => `<tr><td>${esc(n.name)}</td><td>${esc(n.relation)}</td><td class="r">${esc(n.share)}%</td><td class="mono small">${esc(n.id_type)} ${esc(n.id)}</td><td class="small">${n.guardian ? 'Yes · guardian ' + esc(n.guardian) : ''}</td></tr>`)) : `<div class="dim small" style="margin-top:6px">${c.nominee === 'Opted in' ? 'Only a name is on record (' + esc(c.nominee_name || '') + '): collect each nominee’s share and identity detail.' : c.nomination_on ? 'The client declared that they do not wish to nominate.' : 'No nomination or opt-out declaration on record: a sole holder must make one.'}</div>`}</div>
    ${d.positions.length ? `<div>${h3('Open positions')}${tbl([['Contract'], ['Net qty', 'r'], ['Avg', 'r'], ['LTP', 'r'], ['MTM', 'r'], ['Margin', 'r']], d.positions.map(p => `<tr><td>${esc(p.name)}</td><td class="r ${sgn(p.qty)}">${nf(p.qty)}</td><td class="r">${nf(p.avg_price, 2)}</td><td class="r">${nf(p.ltp, 2)}</td><td class="r ${sgn(p.mtm)}">${nf(p.mtm, 0)}</td><td class="r">${inr(p.margin)}</td></tr>`))}</div>` : ''}
    ${d.holdings.length ? `<div>${h3('Demat holdings')}${tbl([['Scrip'], ['Qty', 'r'], ['Pledged', 'r'], ['LTP', 'r'], ['Value', 'r']], d.holdings.map(h => `<tr><td>${esc(h.symbol)}</td><td class="r">${nf(h.qty)}</td><td class="r">${h.pledged ? nf(h.pledged) : '—'}</td><td class="r">${nf(h.ltp, 2)}</td><td class="r">${inr(h.value)}</td></tr>`))}</div>` : ''}
    ${d.trades.length ? `<div>${h3(`Trades on ${fmtDate(S.date)} · ${d.trades.length}`)}${tbl([['Time'], ['Contract'], ['Side'], ['Qty', 'r'], ['Price', 'r'], ['Product']], d.trades.slice(0, 40).map(t => `<tr><td class="n">${t.trade_time}</td><td>${esc(instName(`${t.symbol}|${t.instrument_type}|${t.expiry}|${t.strike}`))}</td><td>${t.side === 'B' ? 'Buy' : 'Sell'}</td><td class="r">${nf(t.qty)}</td><td class="r">${nf(t.price, 2)}</td><td><span class="tag">${esc(t.product)}</span></td></tr>`))}</div>` : ''}
    <div>${h3('Ledger (latest entries)')}${tbl([['Date'], ['Particulars'], ['Debit', 'r'], ['Credit', 'r'], ['Balance', 'r']], lines.map(l => `<tr><td class="n dim">${fmtDate(l.value_date)}</td><td class="wrap">${esc(l.memo || l.narration)}</td><td class="r">${num(l.debit) ? nf(l.debit, 2) : ''}</td><td class="r">${num(l.credit) ? nf(l.credit, 2) : ''}</td><td class="r">${drcr(l.balance)}</td></tr>`), { empty: 'No entries' })}</div>
    <div>${h3('Documents')}${(docs || []).length ? tbl([['Kind'], ['File'], ['Size', 'r'], ['']], docs.map(x => `<tr><td>${esc(x.kind)}</td><td class="wrap">${esc(x.name)}<div class="dim small">${esc(x.by)} · ${fmtDate(x.at.slice(0, 10))}</div></td><td class="r">${x.size < 1024 ? x.size + " B" : nf(x.size / 1024) + " KB"}</td><td><button class="btn sm" data-act="openBlob" data-id="/v1/clients/${encodeURIComponent(c.ucc)}/documents/${x.id}">Open</button></td></tr>`)) : '<div class="dim small">No scanned documents yet.</div>'}
      ${has('Ops maker', 'Ops checker', 'Compliance officer', 'Branch staff') ? `<div class="tools" style="margin-top:8px"><select class="in sm" id="doc-kind">${['PAN card', 'Address proof', 'Photograph', 'Signature', 'Bank proof', 'Income proof', 'KYC form', 'Account opening agreement', 'DDPI / POA', 'Nomination form', 'IPV / VIPV record', 'Other'].map(k => `<option>${k}</option>`).join('')}</select><input type="file" id="doc-file" accept="application/pdf,image/jpeg,image/png" class="in sm"><button class="btn sm" data-act="addDoc" data-id="${c.ucc}">Add document</button><span class="dim small">PDF, JPEG or PNG, up to 5 MB</span></div>` : ''}</div>
    ${d.alerts.length ? `<div>${h3('Surveillance alerts')}${d.alerts.map(a => `<div class="note ${a.severity === 'High' ? 'bad' : 'warn'}" style="margin-bottom:6px"><b>${esc(a.type)}</b> · ${esc(a.status)}<br>${esc(a.detail)}</div>`).join('')}</div>` : ''}
  </div><div class="ft"><button class="btn" data-act="crFor" data-id="${c.ucc}">Statements &amp; reports</button><button class="btn" data-act="openLedger" data-id="${c.ucc}">Full ledger</button>${c.status === 'Active' && has('Ops checker') ? `<button class="btn" data-act="portalAccess" data-id="${c.ucc}">Portal access</button>` : ''}${c.status === 'Active' && has('Risk manager') ? `<button class="btn ${c.blocked ? '' : 'bad'}" data-act="kill" data-id="${c.ucc}" data-b="${c.blocked ? 0 : 1}">${c.blocked ? 'Unblock' : 'Kill switch'}</button>` : ''}<button class="btn pri" data-act="close">Done</button></div>`, true);
}
const drcr = a => { const n = num(a); return n === 0 ? '0.00' : nf(Math.abs(n), 2) + (n < 0 ? ' Cr' : ' Dr'); };

/* ---------- onboarding wizard ---------- */
function obModal() {
  const o = UI.ob, d = o.data, st = ['Identity', 'Bank & demat', 'Segments & risk', 'Nominee & declarations', 'Review'], E = o.err || {};
  const f = (id, l, ph, type) => `<div class="fld"><label for="ob-${id}">${l}</label><input class="in ${E[id] ? 'bad' : ''}" id="ob-${id}" data-ob="${id}" value="${esc(d[id] || '')}" placeholder="${ph || ''}" type="${type || 'text'}">${E[id] ? `<span class="err">${esc(E[id])}</span>` : ''}</div>`;
  let body = '';
  if (o.step === 0) body = `<div class="form">${f('name', 'Full name as per PAN', 'e.g. Kavya Iyer')}${f('pan', 'PAN', 'ABCPE1234F')}<div class="fld"><label for="ob-type">Client type</label><select class="in" id="ob-type" data-ob="type">${['Individual', 'HUF', 'Corporate', 'NRI'].map(x => `<option ${d.type === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div>${f('mobile', 'Mobile', '10 digits')}${f('email', 'Email', 'name@example.com')}${f('city', 'City', '')}<div class="fld"><label for="ob-kyc">KYC route</label><select class="in" id="ob-kyc" data-ob="kyc_route">${['KRA fetch (existing KYC)', 'CKYC download', 'In person (physical documents)'].map(x => `<option ${d.kyc_route === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div></div><div class="note small" style="margin-top:10px">Individuals with Aadhaar and PAN open faster on their own phone: send them the online account opening link from <b>Online applications</b>. Use this form for walk-in, HUF, corporate and NRI clients.</div>`;
  if (o.step === 1) body = `<div class="form">${f('bank', 'Bank name', 'e.g. HDFC Bank')}${f('ifsc', 'IFSC', 'HDFC0001234')}${f('account', 'Account number', '9 to 18 digits')}<div class="fld"><label for="ob-dp">Depository</label><select class="in" id="ob-dp" data-ob="depository">${['CDSL', 'NSDL'].map(x => `<option ${d.depository === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div>${f('demat_id', 'Demat account ID', '16 characters')}<div class="note full">The penny-drop check matches the account holder name with the PAN name before the account is saved.</div></div>`;
  if (o.step === 2) body = `<div class="form"><div class="fld full"><label>Segments</label><div class="tools">${[['CM', 'Equity cash'], ['FO', 'Equity F&O'], ['CD', 'Currency'], ['COM', 'Commodity']].map(([k, l]) => `<label class="check"><input type="checkbox" data-obseg="${k}" ${d.segments.includes(k) ? 'checked' : ''}> ${l}</label>`).join('')}</div>${E.segments ? `<span class="err">${esc(E.segments)}</span>` : ''}</div><div class="fld"><label for="ob-income">Annual income</label><select class="in" id="ob-income" data-ob="income">${['<1 L', '1-5 L', '5-10 L', '10-25 L', '25 L-1 Cr', '>1 Cr'].map(x => `<option ${d.income === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div><div class="fld"><label for="ob-ras">Running account settlement</label><select class="in" id="ob-ras" data-ob="ras">${['Quarterly', 'Monthly'].map(x => `<option ${d.ras === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div><label class="check full"><input type="checkbox" data-obchk="income_proof" ${d.income_proof ? 'checked' : ''}> Income proof received (required for derivatives)</label>${E.income_proof ? `<span class="err full">${esc(E.income_proof)}</span>` : ''}</div>`;
  if (o.step === 3) body = `<div class="form"><div class="fld"><label for="ob-nom">Nomination</label><select class="in" id="ob-nom" data-ob="nominee">${['Opted in', 'Opted out'].map(x => `<option ${d.nominee === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div>${d.nominee === 'Opted in' ? f('nominee_name', 'Nominee name', '') : ''}<label class="check full"><input type="checkbox" data-obchk="risk_disclosure" ${d.risk_disclosure ? 'checked' : ''}> Client has read the risk disclosure document and the derivatives risk statement</label><label class="check full"><input type="checkbox" data-obchk="whatsapp" ${d.whatsapp ? 'checked' : ''}> Client agrees to receive account messages on WhatsApp (tick only if the client ticked it on the form)</label>${E.risk_disclosure ? `<span class="err full">${esc(E.risk_disclosure)}</span>` : ''}</div>`;
  if (o.step === 4) body = `<div class="kv">${[['Name', d.name], ['PAN', d.pan], ['Type', d.type], ['Mobile', d.mobile], ['Email', d.email], ['City', d.city], ['KYC route', d.kyc_route], ['Bank', d.bank + ' · ' + d.ifsc], ['Depository', d.depository + ' ' + (d.demat_id || '')], ['Segments', d.segments.join(', ')], ['Income', d.income], ['RAS', d.ras], ['Nomination', d.nominee + (d.nominee_name ? ' · ' + d.nominee_name : '')]].map(x => `<div><small>${x[0]}</small><b>${esc(x[1])}</b></div>`).join('')}</div>${E._server ? errHTML(E._server) : ''}<div class="note">Submitting creates the application on the server with a UCC, records it in the audit trail, and puts it in KYC verification. Activation needs an Ops checker.</div>`;
  modal(mhd('New client', 'Step ' + (o.step + 1) + ' of 5 · ' + st[o.step]) + `<div class="bd"><div class="steps">${st.map((s, k) => `<span class="${k === o.step ? 'on' : k < o.step ? 'done' : ''}">${s}</span>`).join('')}</div>${body}</div><div class="ft"><button class="btn" data-act="${o.step ? 'obBack' : 'close'}">${o.step ? 'Back' : 'Cancel'}</button><button class="btn pri" data-act="obNext">${o.step === 4 ? 'Submit application' : 'Continue'}</button></div>`);
}
function obValidate() {
  const o = UI.ob, d = o.data, E = {};
  if (o.step === 0) { if (!d.name || d.name.trim().length < 3) E.name = 'Enter the full name'; if (!/^[A-Z]{5}[0-9]{4}[A-Z]$/.test((d.pan || '').toUpperCase())) E.pan = 'PAN is 5 letters, 4 digits, 1 letter'; if (!/^[6-9][0-9]{9}$/.test(d.mobile || '')) E.mobile = 'Enter a 10-digit Indian mobile number'; if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(d.email || '')) E.email = 'Enter a valid email'; if (!d.city) E.city = 'Enter the city'; }
  if (o.step === 1) { if (!d.bank) E.bank = 'Enter the bank name'; if (!/^[A-Z]{4}0[A-Z0-9]{6}$/.test((d.ifsc || '').toUpperCase())) E.ifsc = 'IFSC is 4 letters, 0, then 6 characters'; if (!/^[0-9]{9,18}$/.test(d.account || '')) E.account = 'Account number is 9 to 18 digits'; }
  if (o.step === 2) { if (!d.segments.length) E.segments = 'Choose at least one segment'; if (d.segments.some(s => s !== 'CM') && !d.income_proof) E.income_proof = 'Derivatives segments need income proof'; }
  if (o.step === 3) { if (!d.risk_disclosure) E.risk_disclosure = 'Risk disclosure acknowledgement is required'; if (d.nominee === 'Opted in' && !d.nominee_name) E.nominee_name = 'Enter the nominee name'; }
  o.err = E; return !Object.keys(E).length;
}

/* ---------- RMS ---------- */
V.rms = {
  load: () => get(`/v1/rms?date=${S.date}`),
  html: d => {
    const rows = d.rows, breach = rows.filter(r => r.status === 'Breach');
    const canRisk = has('Risk manager');
    return ph('RMS & limits', `Limits from the ledger, UPI blocks and pledged collateral after haircut. Non-cash collateral counts only up to the cash component (50:50). Watch threshold ${d.warn_pct}%. Short options are margined on the underlying; long options are premium-paid.`,
      `${canRisk ? `<button class="btn pri" data-act="sqAll" ${breach.some(r => r.positions) ? '' : 'disabled'}>Square off all breaches</button>` : ''}`) +
    (d.warning ? `<div class="note warn">${esc(d.warning)}</div>` : '') +
    `<div class="kpis">${kpi('Margin required', cmp(sum(rows, r => num(r.required))), rows.filter(r => num(r.required) > 0).length + ' clients with positions')}${kpi('Usable collateral', cmp(sum(rows, r => num(r.cash) + num(r.collateral_usable))), 'cash + eligible non-cash')}${kpi('Breaches', breach.length, cmp(-sum(breach, r => num(r.free))) + ' uncovered', breach.length ? 'bad' : 'good')}${kpi('Watch list', rows.filter(r => r.status === 'Watch').length, 'above ' + d.warn_pct + '%', 'warn')}${kpi('Debit balances', rows.filter(r => num(r.funds) < 0).length, cmp(sum(rows.filter(r => num(r.funds) < 0), r => num(r.funds))) + ' receivable', 'warn')}${kpi('Blocked', rows.filter(r => r.blocked).length, 'kill switch on')}</div>
    ${d.overrides.length ? `<div class="panel"><div class="hd"><h3>Limit overrides and branch requests</h3><small>Maker or branch requests; a different checker or risk manager approves, and branch requests are decided by head-office risk. Valid for ${fmtDate(S.date)}.</small></div>${tbl([['ID'], ['UCC'], ['Amount (₹)', 'r'], ['Reason'], ['Maker'], ['Checker'], ['Status'], ['']], d.overrides.map(o => `<tr><td class="n">${o.id}</td><td class="n">${o.ucc}</td><td class="r">${inr(o.amount)}</td><td class="wrap">${esc(o.reason)}${o.by_branch ? `<div><span class="tag">Branch request · ${esc(o.branch || '')}</span></div>` : ''}</td><td>${esc(o.maker)}</td><td>${esc(o.checker || '—')}</td><td>${pill(o.status, o.status === 'Approved' ? 'good' : o.status === 'Rejected' ? 'bad' : 'warn')}</td><td>${o.status === 'Pending' && (o.by_branch ? has('Risk manager') : has('Ops checker', 'Risk manager')) && o.maker !== S.me.actor ? `<button class="btn sm" data-act="ovDecide" data-id="${o.id}" data-a="1">Approve</button><button class="btn sm bad" data-act="ovDecide" data-id="${o.id}" data-a="0">Reject</button>` : ''}</td></tr>`))}</div>` : ''}
    <div class="panel"><div class="hd"><div class="tools">${segBtns('rmsF', ['All', 'Breach', 'Watch', 'Debit', 'OK'], 'rms-t')}<input class="in" data-ui="rmsQ" data-part="rms-t" placeholder="Filter UCC or name" value="${esc(UI.rmsQ)}"></div><small>Sorted by utilisation</small></div><div id="rms-t">${P['rms-t'](d)}</div></div>`;
  }
};
P['rms-t'] = d => {
  let rows = d.rows.slice(); if (UI.rmsF !== 'All') rows = rows.filter(r => r.status === UI.rmsF);
  const q = UI.rmsQ.trim().toLowerCase(); if (q) rows = rows.filter(r => (r.ucc + ' ' + r.name).toLowerCase().includes(q));
  rows.sort((a, b) => (b.utilisation - a.utilisation) || (num(a.funds) - num(b.funds)));
  const st = { OK: 'good', Watch: 'warn', Breach: 'bad', Debit: 'warn' }, warn = d.warn_pct / 100;
  return tbl([['Client'], ['Ledger (₹)', 'r'], ['UPI + collateral', 'r'], ['Required', 'r'], ['Free / OMS limit', 'r'], ['Utilisation'], ['Status'], ['']], rows.map(r => `<tr><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button><div class="dim" style="font-size:11.5px">${esc(r.name)}</div></td><td class="r ${sgn(r.funds)}">${inr(r.funds)}</td><td class="r" title="Collateral after haircut ${inr(r.collateral)}; usable after 50:50 ${inr(r.collateral_usable)}">${num(r.upi) + num(r.collateral) ? inr(num(r.upi) + num(r.collateral_usable)) : '—'}${num(r.collateral_usable) < num(r.collateral) - 1 ? `<div class="dim" style="font-size:11px">capped from ${cmp(num(r.upi) + num(r.collateral))}</div>` : ''}</td><td class="r" title="${num(r.futeq) ? 'Futures equivalent ' + cmp(r.futeq) : ''}">${num(r.required) ? inr(r.required) : '—'}</td><td class="r ${sgn(r.free)}">${inr(r.free)}<div class="dim" style="font-size:11px">limit ${r.blocked ? '0 · blocked' : inr(r.limit)}</div></td><td><div class="split">${bar(r.utilisation, warn)}<span class="mono dim" style="font-size:11.5px">${num(r.required) ? (r.utilisation >= 9.99 ? '>999%' : pct(r.utilisation, 0)) : ''}</span></div></td><td>${pill(r.status, st[r.status])}${r.inactive ? ' ' + pill('Inactive', 'nt') : ''}${r.cash_short || r.square_off_sent ? `<div class="dim" style="font-size:11px;margin-top:2px">${[r.cash_short ? 'cash under 50%' : '', r.square_off_sent ? 'square-off sent' : ''].filter(Boolean).join(' · ')}</div>` : ''}</td><td><div class="split">${has('Ops maker', 'Risk manager', 'Branch staff', 'Authorised person') ? `<button class="btn sm" data-act="override" data-id="${r.ucc}">${has('Branch staff', 'Authorised person') && !has('Ops maker') ? 'Request limit' : 'Override'}</button>` : ''}${has('Risk manager') ? `<button class="btn sm ${r.blocked ? '' : 'bad'}" data-act="kill" data-id="${r.ucc}" data-b="${r.blocked ? 0 : 1}">${r.blocked ? 'Unblock' : 'Kill'}</button>${r.positions ? `<button class="btn sm" data-act="sq" data-id="${r.ucc}">Square off</button>` : ''}` : ''}</div></td></tr>`), { tall: true });
};

/* ---------- margin ---------- */
V.margin = {
  load: () => get(`/v1/margin?date=${S.date}`),
  html: d => {
    const rows = d.rows;
    return ph('Margin reporting', 'Peak margin is rebuilt from trade timestamps at each clearing snapshot time; collection is the client’s funds before the day’s bills, plus UPI blocks and usable collateral. Penalty slab: 0.5% if the shortfall is under ₹1 lakh and under 10% of the requirement, otherwise 1%.',
      `<button class="btn" data-act="dl" data-id="/v1/reports/margin/download?date=${S.date}" data-name="margin_${S.date}.csv">Download CC upload file</button>${has('Risk manager', 'Ops checker') ? `<button class="btn pri" data-act="penalties" ${d.penalties_posted ? 'disabled' : ''}>${d.penalties_posted ? 'Penalties posted' : 'Post penalties to clients'}</button>` : ''}`) +
    `<div class="kpis">${kpi('Peak requirement', cmp(sum(rows, r => num(r.peak))), rows.length + ' clients')}${kpi('Collected upfront', cmp(sum(rows, r => num(r.collected))), 'cash + eligible collateral')}${kpi('Short collection', cmp(sum(rows, r => num(r.short))), rows.filter(r => num(r.short) > 0).length + ' clients short', rows.some(r => num(r.short) > 0) ? 'bad' : 'good')}${kpi('Penalty', inr(sum(rows, r => num(r.penalty)), 2), d.penalties_posted ? 'posted to client ledgers' : 'estimate', rows.some(r => num(r.penalty) > 0) ? 'warn' : '')}</div>
    <div class="panel"><div class="hd"><h3>Snapshot times</h3><small>From Settings</small></div><div class="bd"><div class="tools">${d.snapshots.map(t => `<span class="tag">${esc(t)}</span>`).join('')}<span class="tag">EOD</span></div></div></div>
    <div class="panel">${tbl([['UCC'], ['Client'], ['Peak required', 'r'], ['At'], ['EOD required', 'r'], ['Collected', 'r'], ['Short', 'r'], ['% short', 'r'], ['Slab', 'r'], ['Penalty (₹)', 'r'], ['Status']], rows.map(r => `<tr><td class="n">${r.ucc}</td><td>${esc(r.name)}</td><td class="r">${inr(r.peak)}</td><td class="n dim">${esc(r.peak_at)}</td><td class="r">${inr(r.eod)}</td><td class="r">${inr(r.collected)}</td><td class="r ${num(r.short) > 0 ? 'neg' : ''}">${num(r.short) > 0 ? inr(r.short) : '—'}</td><td class="r">${num(r.short) > 0 ? pct(r.short_pct) : '—'}</td><td class="r">${num(r.slab_pct) ? r.slab_pct + '%' : '—'}</td><td class="r">${num(r.penalty) ? inr(r.penalty, 2) : '—'}</td><td>${num(r.short) > 0 ? pill('Short', 'bad') : pill('Collected', 'good')}</td></tr>`), { tall: true, empty: 'No margin requirement on this date.',
      foot: rows.length ? `<tfoot><tr><td colspan="2">Total</td><td class="r">${inr(sum(rows, r => num(r.peak)))}</td><td></td><td class="r">${inr(sum(rows, r => num(r.eod)))}</td><td class="r">${inr(sum(rows, r => num(r.collected)))}</td><td class="r">${inr(sum(rows, r => num(r.short)))}</td><td></td><td></td><td class="r">${inr(sum(rows, r => num(r.penalty)), 2)}</td><td></td></tr></tfoot>` : '' })}</div>`;
  }
};

/* ---------- collateral ---------- */
V.collateral = {
  load: async () => { const [lines, pledges] = await Promise.all([get(`/v1/collateral?date=${S.date}`), get('/v1/pledges')]); return { lines, pledges }; },
  html: d => {
    const pend = d.pledges.filter(p => p.status === 'Pending'), can = has('Ops maker', 'Ops checker', 'Risk manager');
    return ph('Collateral & pledge', 'Margin pledge and re-pledge through the depository. Securities stay in the client’s demat; haircuts come from the day’s market data file.') +
    `<div class="kpis">${kpi('Pledged market value', cmp(sum(d.lines, l => num(l.value))), d.lines.length + ' ISINs')}${kpi('Collateral after haircut', cmp(sum(d.lines, l => num(l.collateral))), 'before 50:50 cap')}${kpi('Pending requests', pend.length, 'from the depository', pend.length ? 'warn' : '')}</div>
    <div class="panel"><div class="hd"><h3>Pledge requests</h3><small>Client-initiated at the depository; accepting marks the shares as collateral</small></div>${tbl([['Request'], ['Time'], ['UCC'], ['Scrip'], ['Qty', 'r'], ['Status'], ['By'], ['']], d.pledges.map(p => `<tr><td class="n">${esc(p.id)}</td><td class="n dim">${esc(p.time)}</td><td class="n">${p.ucc}</td><td>${esc(p.symbol)} <span class="tag">${p.isin}</span></td><td class="r">${nf(p.qty)}</td><td>${pill(p.status, p.status === 'Accepted' ? 'good' : p.status === 'Rejected' ? 'bad' : 'warn')}${p.psn ? ` <span class="tag">PSN ${p.psn}</span>` : ''}${p.reason ? ` <span class="dim">${esc(p.reason)}</span>` : ''}</td><td>${esc(p.decided_by || '')}</td><td>${p.status === 'Pending' && can ? `<button class="btn sm" data-act="pledge" data-id="${esc(p.id)}" data-a="1">Accept</button><button class="btn sm bad" data-act="pledge" data-id="${esc(p.id)}" data-a="0">Reject</button>` : ''}</td></tr>`), { empty: 'No pledge requests. Upload PLEDGE-REQUESTS on the Files page.' })}</div>
    <div class="panel"><div class="hd"><h3>Pledged securities by ISIN</h3><small>Valued at LTP</small></div>${tbl([['Scrip'], ['ISIN'], ['Clients', 'r'], ['Qty pledged', 'r'], ['LTP', 'r'], ['Market value (₹)', 'r'], ['Haircut', 'r'], ['Collateral (₹)', 'r']], d.lines.map(l => `<tr><td>${esc(l.symbol)}</td><td class="n">${l.isin}</td><td class="r">${l.clients}</td><td class="r">${nf(l.qty)}</td><td class="r">${nf(l.ltp, 2)}</td><td class="r">${inr(l.value)}</td><td class="r">${num(l.haircut_pct)}%</td><td class="r">${inr(l.collateral)}</td></tr>`), { empty: 'No pledged securities.' })}</div>`;
  }
};

/* ---------- ledger ---------- */
V.ledger = {
  load: async () => {
    const [tb, funds] = await Promise.all([get(`/v1/ledger/trial-balance?as_of=${S.date}`), get(`/v1/funds?date=${S.date}`), clientCache()]);
    const act = CL.rows.filter(c => c.status === 'Active');
    if (!UI.ledgerU || !act.some(c => c.ucc === UI.ledgerU)) UI.ledgerU = act[0]?.ucc || '';
    const stmt = UI.ledgerU ? await get(`/v1/ledger/statement?ucc=${encodeURIComponent(UI.ledgerU)}&to=${S.date}`) : null;
    return { tb, funds, clients: act, stmt };
  },
  html: d => {
    const tb = d.tb, f = d.funds, g = p => tb.rows.filter(r => r.account.startsWith(p));
    const cr = num(f.client_credits), dr = num(f.client_debits);
    const canPost = has('Ops maker', 'Ops checker'), canRev = has('Ops checker');
    return ph('Ledger', 'Double-entry, append-only journal. Every bill, receipt, payout and charge is an entry; corrections are reversing entries. The database rejects unbalanced entries and edits to history.',
      `<button class="btn" data-act="dl" data-id="/v1/reports/bills/download?date=${S.date}" data-name="bills_${S.date}.csv">Bills register</button>${canPost ? '<button class="btn pri" data-act="journal">Post journal entry</button>' : ''}`) +
    `<div class="kpis">${kpi('Client credit balances', cmp(cr), 'owed to clients')}${kpi('Client debit balances', cmp(dr), 'receivable', 'warn')}${kpi('Client bank', cmp(f.client_bank), 'client account')}${kpi('With clearing corporations', cmp(f.with_cc), 'upstreamed collateral')}${kpi('Suspense', cmp(f.suspense), 'unmatched receipts', num(f.suspense) ? 'warn' : '')}${kpi('Segregation cover', num(f.cover).toFixed(2) + '×', f.ok ? 'client money fully covered' : 'SHORTFALL: escalate', f.ok ? 'good' : 'bad')}</div>
    <div class="grid g21"><div class="panel"><div class="hd"><div class="tools"><label class="dim" for="ledgerU-q" style="font-size:12px">Client</label>${cpick('ledgerU', UI.ledgerU, { on: v => { if (v) { UI.ledgerU = v; render({ keep: true }); } } })}</div>${d.stmt ? `<small>Closing ${drcr(d.stmt.closing)}</small>` : ''}</div>
      ${tbl([['Date'], ['Particulars'], ['Debit', 'r'], ['Credit', 'r'], ['Balance', 'r']], (d.stmt?.lines || []).map(l => `<tr><td class="n dim">${fmtDate(l.value_date)}</td><td class="wrap">${esc(l.memo || '')}<div class="dim" style="font-size:11.5px">${esc(l.narration)} · <span class="mono">${esc(l.entry_id)}</span>${canRev && !/^Reversal/.test(l.narration) ? ` · <button class="linkish" data-act="reverse" data-id="${esc(l.entry_id)}">Reverse</button>` : ''}</div></td><td class="r">${num(l.debit) ? nf(l.debit, 2) : ''}</td><td class="r">${num(l.credit) ? nf(l.credit, 2) : ''}</td><td class="r ${num(l.balance) > 0 ? 'neg' : ''}">${drcr(l.balance)}</td></tr>`), { tall: true, empty: 'No entries' })}</div>
    <div class="panel"><div class="hd"><h3>Trial balance</h3>${tb.rows.length ? (tb.balanced ? pill('Debits equal credits', 'good') : pill('Does not balance', 'bad')) : ''}</div>
      ${tbl([['Head'], ['Debit (₹)', 'r'], ['Credit (₹)', 'r']], [['Clients · debit balances', g('CLIENT:'), 1], ['Clients · credit balances', g('CLIENT:'), -1], ['Bank · client account', g('BANK:CLIENT')], ['Clearing corporations', g('CC:')], ['Brokerage income', g('INCOME:')], ['Exchange charges payable', g('PAYABLE:EXCH_TXN')], ['STT / CTT payable', g('PAYABLE:STT')], ['Stamp duty payable', g('PAYABLE:STAMP')], ['SEBI fee payable', g('PAYABLE:SEBI_FEE')], ['GST payable', g('PAYABLE:GST')], ['Margin penalties payable', g('PAYABLE:CC_PENALTY')], ['Suspense · bank', g('SUSPENSE:')], ['Opening equity', g('EQUITY:')]].map(([n, rs, side]) => { const D = sum(rs, r => side === -1 ? 0 : num(r.debit)), Cc = sum(rs, r => side === 1 ? 0 : num(r.credit)); return (D || Cc) ? `<tr><td>${n}</td><td class="r">${D ? nf(D, 2) : '—'}</td><td class="r">${Cc ? nf(Cc, 2) : '—'}</td></tr>` : ''; }).filter(Boolean), { empty: 'No entries yet', foot: tb.rows.length ? `<tfoot><tr><td>Total</td><td class="r">${nf(tb.total_debit, 2)}</td><td class="r">${nf(tb.total_credit, 2)}</td></tr></tfoot>` : '' })}
      <div class="bd"><div class="note ${f.ok ? '' : 'bad'}">${f.ok ? 'Segregation check passed: client bank plus funds with clearing corporations, less unmatched receipts, cover client credit balances.' : 'Segregation check failed: client money held is below client credit balances. Escalate to the compliance officer.'}</div></div></div></div>`;
  }
};

/* ---------- bank ---------- */
V.bank = {
  load: () => get('/v1/bank'),
  html: d => {
    const by = s => d.filter(b => b.status === s);
    return ph('Bank reconciliation', 'Credits on the client bank statement. A credit from the client’s registered account posts a receipt. Anything else is parked in suspense and must be returned, or identified with written bank confirmation.', has('Ops checker') ? '<button class="btn" data-act="upstream">Upstream funds to CC</button>' : '') +
    `<div class="kpis">${kpi('Credits', d.length, cmp(sum(d, b => num(b.amount))))}${kpi('Matched', by('Matched').length, 'posted to client ledgers', 'good')}${kpi('Third-party', by('Third-party').length, 'must be returned', by('Third-party').length ? 'bad' : '')}${kpi('Unidentified', by('Unidentified').length, 'investigate remitter', by('Unidentified').length ? 'warn' : '')}${kpi('Returned / identified', by('Returned').length + by('Identified').length, 'cleared from suspense')}</div>
    <div class="panel"><div class="hd">${segBtns('bankF', ['All', 'Matched', 'Third-party', 'Unidentified', 'Returned', 'Identified'], 'bank-t')}</div><div id="bank-t">${P['bank-t'](d)}</div></div>`;
  }
};
P['bank-t'] = d => tbl([['Date'], ['Time'], ['Mode'], ['Reference'], ['Remitter'], ['From a/c'], ['Amount (₹)', 'r'], ['Client'], ['Status'], ['Action']], d.filter(b => UI.bankF === 'All' || b.status === UI.bankF).map(b => `<tr><td class="n dim">${fmtDate(b.date)}</td><td class="n dim">${esc(b.time)}</td><td>${esc(b.mode)}${b.source ? ` <span class="dim small" title="${esc(b.source)}">live</span>` : ''}</td><td class="n">${esc(b.ref)}</td><td>${esc(b.remitter)}</td><td class="n">${esc(b.account)}${b.status === 'Third-party' ? ' ' + pill('Not registered', 'bad') : ''}</td><td class="r">${inr(b.amount)}</td><td>${b.ucc ? `<button class="linkish" data-act="client" data-id="${b.ucc}">${b.ucc}</button>` : '<span class="dim">—</span>'}</td><td>${pill(b.status, b.status === 'Matched' || b.status === 'Identified' ? 'good' : b.status === 'Returned' ? 'nt' : b.status === 'Third-party' ? 'bad' : 'warn')}</td><td>${(b.status === 'Third-party' || b.status === 'Unidentified') && has('Ops maker', 'Ops checker') ? `<button class="btn sm bad" data-act="bankReturn" data-id="${esc(b.id)}">Return</button>` : ''}${b.status === 'Unidentified' && has('Ops checker') ? `<button class="btn sm" data-act="identify" data-id="${esc(b.id)}">Identify</button>` : ''}</td></tr>`), { tall: true, empty: 'No statement credits. Upload BANK-STMT on the Files page, or connect live bank credits in Settings → Connections.' });

/* ---------- RAS ---------- */
V.ras = {
  load: () => get(`/v1/ras?date=${S.date}`),
  html: d => {
    const due = d.rows.filter(r => r.settles_on === d.monthly_date && !r.settled && num(r.payout) > 0);
    return ph('Running account settlement', `Unused client funds returned to the registered bank account. Monthly clients and anyone inactive 30+ days settle on ${fmtDate(d.monthly_date)}; quarterly clients on ${fmtDate(d.quarterly_date)}. Retention = margin requirement + ${d.buffer_pct}% buffer.`,
      `${d.batches.length ? `<button class="btn" data-act="dl" data-id="/v1/ras/payout-file?settle_date=${d.monthly_date}" data-name="ras_payout_${d.monthly_date}.csv">Bank payout file</button>` : ''}${has('Ops checker') ? `<button class="btn pri" data-act="runRas" ${due.length ? '' : 'disabled'}>Run RAS for ${fmtDate(d.monthly_date)}</button>` : ''}`) +
    `<div class="kpis">${kpi('Due ' + fmtDate(d.monthly_date), due.length, cmp(sum(due, r => num(r.payout))) + ' to pay out')}${kpi('Inactive 30+ days', d.rows.filter(r => r.inactive).length, 'settle regardless of preference', 'warn')}${kpi('Quarterly clients', d.rows.filter(r => r.settles_on === d.quarterly_date).length, fmtDate(d.quarterly_date))}${kpi('Batches posted', d.batches.length, d.batches.length ? inr(sum(d.batches, b => num(b.total))) + ' paid' : 'none yet', d.batches.length ? 'good' : '')}</div>
    <div class="panel"><div class="hd">${segBtns('rasF', [['due', 'Due ' + fmtDate(d.monthly_date)], ['q', 'Quarterly'], ['all', 'All credit balances']], 'ras-t')}</div><div id="ras-t">${P['ras-t'](d)}</div></div>`;
  }
};
P['ras-t'] = d => {
  let rows = d.rows; if (UI.rasF === 'due') rows = rows.filter(r => r.settles_on === d.monthly_date); if (UI.rasF === 'q') rows = rows.filter(r => r.settles_on === d.quarterly_date);
  return tbl([['UCC'], ['Client'], ['Preference'], ['Last trade'], ['Credit balance', 'r'], ['Retention', 'r'], ['Payout (₹)', 'r'], ['Bank'], ['Settles on'], ['Status']], rows.map(r => `<tr><td class="n">${r.ucc}</td><td>${esc(r.name)}</td><td>${r.preference}</td><td class="n">${fmtDate(r.last_trade) || '—'} ${r.inactive ? pill('Inactive', 'warn') : ''}</td><td class="r">${inr(r.credit)}</td><td class="r">${num(r.retention) ? inr(r.retention) : '—'}</td><td class="r">${inr(r.payout)}</td><td class="dim">${esc(r.bank)}</td><td class="n">${fmtDate(r.settles_on)}</td><td>${r.settled ? pill('Settled', 'good') : pill('Scheduled', 'nt')}</td></tr>`), { tall: true,
    foot: rows.length ? `<tfoot><tr><td colspan="4">Total</td><td class="r">${inr(sum(rows, r => num(r.credit)))}</td><td class="r">${inr(sum(rows, r => num(r.retention)))}</td><td class="r">${inr(sum(rows, r => num(r.payout)))}</td><td colspan="3"></td></tr></tfoot>` : '' });
};

/* ---------- charges ---------- */
V.charges = {
  load: async () => { await clientCache(); return {}; },
  html: () => { const c = UI.calc; return ph('Charges calculator', 'Charge breakdown for one order, the same calculation billing uses: statutory charges from the rate schedule, brokerage from the client’s plan (or the schedule when no client is chosen).') + `<div class="grid g2"><div class="panel"><div class="hd"><h3>Order</h3></div><div class="bd"><div class="form">
    <div class="fld full"><label for="calc-ucc-q">Client (optional)</label>${cpick('calc-ucc', c.ucc || '', { none: 'No client: firm charge schedule', on: v => { UI.calc.ucc = v; calc(); } })}</div>
    <div class="fld full"><label for="cb">Product</label><select class="in" id="cb" data-calc="bucket">${[['CM-DEL', 'Equity delivery'], ['CM-INTRA', 'Equity intraday'], ['FO-FUT', 'Equity futures'], ['FO-OPT', 'Equity options (premium)'], ['CD-FUT', 'Currency futures'], ['CD-OPT', 'Currency options'], ['COM-FUT', 'Commodity futures'], ['COM-OPT', 'Commodity options']].map(([k, l]) => `<option value="${k}" ${c.bucket === k ? 'selected' : ''}>${l}</option>`).join('')}</select></div>
    <div class="fld"><label for="cx">Exchange</label><select class="in" id="cx" data-calc="exchange">${['NSE', 'BSE', 'MCX'].map(x => `<option ${c.exchange === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div>
    <div class="fld"><label for="cs">Side</label><select class="in" id="cs" data-calc="side"><option value="B" ${c.side === 'B' ? 'selected' : ''}>Buy</option><option value="S" ${c.side === 'S' ? 'selected' : ''}>Sell</option></select></div>
    <div class="fld"><label for="cq">Quantity</label><input class="in" id="cq" type="number" min="1" data-calc="qty" value="${esc(c.qty)}"></div>
    <div class="fld"><label for="cp">Price (₹)</label><input class="in" id="cp" inputmode="decimal" data-calc="price" value="${esc(c.price)}"></div>
    ${/OPT|FUT/.test(c.bucket) ? `<div class="fld"><label for="cl">Lot size</label><input class="in" id="cl" type="number" min="1" data-calc="lot" value="${esc(c.lot || 1)}"></div>` : ''}
  </div></div></div><div class="panel"><div class="hd"><h3>Breakdown</h3><small>${S.me?.sample_rates ? 'Sample rates' : esc(S.me?.schedule || '')}</small></div><div id="calc-out"><p class="loading" style="padding:14px">Calculating…</p></div></div></div>`; }
};
let calcT;
async function calc() {
  const c = UI.calc, out = $('calc-out'); if (!out) return;
  try {
    const r = await post('/v1/charges/quote', { bucket: c.bucket, exchange: c.exchange, side: c.side, qty: parseInt(c.qty, 10) || 0, price: String(c.price || '0'), date: S.date, ucc: c.ucc || '', lot: parseInt(c.lot, 10) || 1 });
    const b = r.breakdown, total = num(b.total), v = num(b.turnover);
    out.innerHTML = tbl([['Component'], ['₹', 'r']], [['Turnover', b.turnover], ['Brokerage', b.brokerage], ['STT / CTT', b.stt], ['Exchange transaction charge', b.exchange_txn], ['SEBI turnover fee', b.sebi_fee], ['Stamp duty', b.stamp_duty], ['GST (18%)', b.gst]].map(x => `<tr><td>${x[0]}</td><td class="r">${nf(x[1], 2)}</td></tr>`),
      { foot: `<tfoot><tr><td>Total charges</td><td class="r">${nf(total, 2)}</td></tr><tr><td>${c.side === 'B' ? 'Net payable' : 'Net receivable'}</td><td class="r">${nf(c.side === 'B' ? v + total : v - total, 2)}</td></tr><tr><td>Breakeven move per unit</td><td class="r">${nf(total * 2 / Math.max(1, +c.qty), 4)}</td></tr></tfoot>` }) +
      `<div class="bd">${r.plan ? `<div><b>Plan: ${esc(r.plan)}</b></div>` : (c.ucc ? '<div class="dim small">No plan sets this product for the client; the schedule applies.</div>' : '')}<span class="dim" style="font-size:12px">Rule ${esc(b.rule)}</span></div>`;
  } catch (e) { out.innerHTML = `<div class="bd">${errHTML(e)}</div>`; }
}

/* ---------- brokerage plans ---------- */
const MODES = [['pct', '% of turnover'], ['flat', '₹ per order'], ['lower_of', 'Lower of ₹ per order or %'], ['per_lot', '₹ per lot'], ['per_unit', '₹ per share'], ['zero', 'Free']];
const RATE_FIELDS = [['pct', '%'], ['flat', '₹ / order'], ['per_lot', '₹ / lot'], ['per_unit', '₹ / share'], ['min_per_unit', 'Min ₹ / share'], ['min_per_lot', 'Min ₹ / lot'], ['min', 'Min ₹ / order'], ['max', 'Max ₹ / order']];
const BK_EXCH = ['NSE', 'BSE', 'MCX', 'NCDEX'];
// A rate key is PRODUCT or PRODUCT|EXCHANGE|CHANNEL|CASE ("*" = all).
const rk = k => { const p = k.split('|'); return { b: p[0], x: p[1] || '*', ch: p[2] || '*', cs: p[3] || '*' }; };
const rkJoin = (b, x, ch, cs) => (x === '*' && ch === '*' && cs === '*') ? b : [b, x, ch, cs].join('|');
function rkLabel(d, k) {
  const r = rk(k), name = d.products.find(p => p.bucket === r.b)?.name || r.b, bits = [];
  if (r.x !== '*') bits.push(r.x);
  if (r.ch !== '*') bits.push(r.ch);
  if (r.cs !== '*') bits.push(d.cases.find(c => c.case === r.cs)?.name || r.cs);
  return `${esc(name)}${bits.length ? ` <span class="dim">· ${bits.map(esc).join(' · ')}</span>` : ''}`;
}
function rkSort(d, keys) { const o = b => d.products.findIndex(p => p.bucket === b); return keys.slice().sort((a, b) => { const x = rk(a), y = rk(b); return o(x.b) - o(y.b) || (x.cs + x.ch + x.x).localeCompare(y.cs + y.ch + y.x); }); }
const scopeLabel = s => !s ? 'All segments' : s.replace('-', ' on ');
V.brokerage = {
  load: async () => { const [d] = await Promise.all([get(`/v1/brokerage?date=${S.date}`), clientCache()]); return d; },
  html: d => {
    const can = has('Ops checker'), plan = id => d.plans.find(p => p.id === id), cur = (t, s) => d.current[t + '#' + (s || '')];
    const def = cur('default'), groups = d.tags.map(t => ({ ...t, cur: cur('tag:' + t.tag) }));
    const scoped = Object.entries(d.current).filter(([k, v]) => v.plan).map(([k, v]) => { const [t, s] = k.split('#'); return { target: t, scope: s, ...v }; });
    const clientCards = scoped.filter(x => x.target.startsWith('ucc:')), segCards = scoped.filter(x => x.scope && !x.target.startsWith('ucc:'));
    const onSched = d.effective.filter(e => !e.plan).length, who = t => t === 'default' ? 'Firm default' : t.startsWith('tag:') ? 'Group ' + esc(t.slice(4)) : 'Client ' + esc(t.slice(4));
    const sched = b => { const r = d.schedule.filter(x => x.bucket === b); return r.length ? r.map(x => (x.exchange === '*' ? '' : x.exchange + ': ') + x.describe).join('; ') : '—'; };
    const lines = (desc, empty) => { const ks = rkSort(d, Object.keys(desc || {})); return tbl([['Rate line'], ['Brokerage']], ks.map(k => `<tr><td>${rkLabel(d, k)}</td><td>${esc(desc[k])}</td></tr>`), { empty }); };
    const cl = u => `<button class="linkish mono" data-act="client" data-id="${u}">${u}</button> <span class="dim">${esc(CL.rows.find(c => c.ucc === u)?.name || '')}</span>`;
    return ph('Brokerage plans', 'A plan (rate card) holds rate lines: a rate for a product, optionally only on one exchange, one trading channel, or one case (auction, rollover, futures expiry, option exercise or assignment, index or stock derivatives). For an order the most specific line wins: case, then channel, then exchange. Each client pays from their own card if one is set, else their group’s (the most recently assigned), else the firm default, else the charge schedule; a card can be set for one segment or one exchange only, and a client’s own rate lines win over their card. Brokerage is charged per executed order from the effective date; days already billed change only if rebilled. Statutory charges always come from the schedule.',
      can ? '<button class="btn" data-act="bkSetup">Channels</button><button class="btn" data-act="clientRates">Client rates</button><button class="btn" data-act="planNew">New plan</button><button class="btn pri" data-act="assign">Assign a plan</button>' : '') +
    `<div class="kpis">${kpi('Plans', d.plans.length, 'with dated versions')}${kpi('Firm default', def?.plan ? esc(plan(def.plan)?.name || def.plan) : 'Schedule', def?.from ? 'since ' + fmtDate(def.from) : 'no default plan')}${kpi('Groups on a plan', groups.filter(g => g.cur?.plan).length, d.tags.length + ' groups in use')}${kpi('Client cards', clientCards.length, 'set per client or segment')}${kpi('Client rates', d.overrides.filter(o => o.current && Object.keys(o.current.rules).length).length, 'clients with their own lines')}${kpi('On the schedule', onSched, 'no plan applies', onSched ? 'warn' : '')}</div>
    <div class="plans">${d.plans.map(p => `<div class="panel plan"><div class="hd"><div><h3>${esc(p.name)}</h3><small>${esc(p.note || '')}${p.note ? ' · ' : ''}<span class="mono">${esc(p.id)}</span></small></div><div class="split"><span class="pill acc">${p.clients} client${p.clients === 1 ? '' : 's'}</span>${can ? `<button class="btn sm" data-act="planEdit" data-id="${esc(p.id)}">Edit rates</button>` : ''}</div></div>
      ${lines(p.describe, 'No rate lines: every product uses the charge schedule')}
      ${d.products.filter(x => !Object.keys(p.describe).some(k => rk(k).b === x.bucket)).length ? `<div class="bd small dim">Charge schedule for ${d.products.filter(x => !Object.keys(p.describe).some(k => rk(k).b === x.bucket)).map(x => esc(x.name) + ' (' + esc(sched(x.bucket)) + ')').join(', ')}</div>` : ''}
      <div class="bd small dim">${p.current ? `In force since ${fmtDate(p.current.from)} · set by ${esc(p.current.by)}` : 'Not yet in force'}${p.upcoming ? ` · <b>new rates from ${fmtDate(p.upcoming.from)}</b>` : ''} · ${p.versions.length} version${p.versions.length === 1 ? '' : 's'}</div></div>`).join('') || '<div class="panel"><div class="empty">No plans yet. Create one, or load BROKERAGE-CARDS on Files, then assign it to the firm, a group or a client.</div></div>'}</div>
    <div class="grid g2">
      <div class="panel"><div class="hd"><h3>Firm default and groups</h3><small>Plan in force on ${fmtDate(S.date)}</small></div>${tbl([['Applies to'], ['Clients', 'r'], ['Plan'], ['Since'], ['']],
        [`<tr><td><b>Firm default</b></td><td class="r">${d.effective.length}</td><td>${def?.plan ? esc(plan(def.plan)?.name || def.plan) : '<span class="dim">Charge schedule</span>'}</td><td class="n dim">${def ? fmtDate(def.from) : ''}</td><td>${can ? `<button class="btn sm" data-act="assign" data-id="default">Change</button>` : ''}</td></tr>`].concat(groups.map(g => `<tr><td><button class="gtag" data-act="filterTag" data-id="${esc(g.tag)}">${esc(g.tag)}</button></td><td class="r">${g.clients}</td><td>${g.cur?.plan ? esc(plan(g.cur.plan)?.name || g.cur.plan) : '<span class="dim">Firm default</span>'}</td><td class="n dim">${g.cur?.plan ? fmtDate(g.cur.from) : ''}</td><td><div class="split">${can ? `<button class="btn sm" data-act="assign" data-id="tag:${esc(g.tag)}">${g.cur?.plan ? 'Change' : 'Set plan'}</button>${g.cur?.plan ? `<button class="btn sm" data-act="unassign" data-id="tag:${esc(g.tag)}">Remove</button>` : ''}` : ''}</div></td></tr>`)).concat(segCards.map(x => `<tr><td>${who(x.target)} <span class="tag">${esc(scopeLabel(x.scope))}</span></td><td class="r"></td><td>${esc(plan(x.plan)?.name || x.plan)}</td><td class="n dim">${fmtDate(x.from)}</td><td>${can ? `<button class="btn sm" data-act="unassign" data-id="${esc(x.target)}" data-scope="${esc(x.scope)}">Remove</button>` : ''}</td></tr>`)), { empty: 'No groups' })}</div>
      <div class="panel"><div class="hd"><h3>Client cards</h3>${can ? '<button class="btn sm" data-act="assign" data-id="ucc:">Set for a client</button>' : ''}</div>${tbl([['Client'], ['Segment'], ['Plan'], ['Since'], ['']], clientCards.map(o => `<tr><td>${cl(o.target.slice(4))}</td><td>${esc(scopeLabel(o.scope))}</td><td>${esc(plan(o.plan)?.name || o.plan)}</td><td class="n dim">${fmtDate(o.from)}</td><td>${can ? `<div class="split"><button class="btn sm" data-act="assign" data-id="${esc(o.target)}" data-scope="${esc(o.scope)}">Change</button><button class="btn sm" data-act="unassign" data-id="${esc(o.target)}" data-scope="${esc(o.scope)}">Remove</button></div>` : ''}</td></tr>`), { tall: true, empty: 'No client-specific plans. Clients follow their group or the firm default.' })}</div>
    </div>
    <div class="panel"><div class="hd"><h3>Client rates</h3><small>A client’s own rate lines win over their card for the orders they fit</small>${can ? '<button class="btn sm" data-act="clientRates">Set for a client</button>' : ''}</div>${tbl([['Client'], ['Status'], ['Rate lines'], ['Since'], ['']], d.overrides.filter(o => o.current && Object.keys(o.current.rules).length).map(o => `<tr><td>${cl(o.ucc)}</td><td>${pill(o.status || '—', o.status === 'Active' ? 'good' : 'nt')}</td><td class="wrap">${rkSort(d, Object.keys(o.describe)).map(k => `${rkLabel(d, k)}: ${esc(o.describe[k])}`).join('<br>')}${o.current.note ? `<div class="dim small">${esc(o.current.note)}</div>` : ''}</td><td class="n dim">${fmtDate(o.current.from)}</td><td>${can ? `<button class="btn sm" data-act="clientRates" data-id="${o.ucc}">Change</button>` : ''}</td></tr>`), { tall: true, empty: 'No client has their own rates.' })}</div>
    <div class="panel"><div class="hd"><h3>Trading channels</h3><small>Default ${esc(d.setup.default_channel)} · ${d.setup.terminals.length} terminal rule${d.setup.terminals.length === 1 ? '' : 's'}</small>${can ? '<button class="btn sm" data-act="bkSetup">Change</button>' : ''}</div><div class="bd small">Channels: ${d.setup.channels.map(c => `<span class="tag">${esc(c)}</span>`).join(' ')}. Trades are given a channel from their terminal (CTCL) ID in the exchange trade file; no match means ${esc(d.setup.default_channel)}. Auction trades are those with settlement type ${d.setup.auction_settlements.map(esc).join(', ') || '—'}.${d.setup.terminals.length ? '<br>' + d.setup.terminals.slice(0, 12).map(t => `<span class="mono">${esc(t.terminal)}</span> → ${esc(t.channel)}`).join(' · ') + (d.setup.terminals.length > 12 ? ` · and ${d.setup.terminals.length - 12} more` : '') : ''}</div></div>
    <div class="panel"><div class="hd"><div class="tools"><h3 style="margin-right:8px">Who pays what</h3><input class="in" data-ui="bkQ" data-part="bk-t" placeholder="Filter by name, UCC or group" value="${esc(UI.bkQ)}"><select class="in" id="bkPlan"><option value="">All plans</option>${d.plans.map(p => `<option value="${esc(p.id)}" ${UI.bkPlan === p.id ? 'selected' : ''}>${esc(p.name)}</option>`).join('')}<option value="-" ${UI.bkPlan === '-' ? 'selected' : ''}>Charge schedule</option></select></div><small>${d.effective.length} active clients on ${fmtDate(S.date)}</small></div><div id="bk-t">${P['bk-t'](d)}</div></div>
    <div class="panel"><div class="hd"><h3>Change history</h3><small>Every assignment is kept; the latest one on or before a date applies</small></div>${tbl([['ID'], ['Applies to'], ['Segment'], ['Plan'], ['From'], ['By'], ['At']], d.assignments.slice().reverse().map(a => `<tr><td class="n dim">${a.id}</td><td>${who(a.target)}</td><td>${esc(scopeLabel(a.scope))}${a.exclusive ? ' <span class="tag">scheme</span>' : ''}</td><td>${a.plan ? esc(plan(a.plan)?.name || a.plan) : '<span class="dim">Removed</span>'}</td><td class="n">${fmtDate(a.from)}</td><td>${esc(a.by)}</td><td class="n dim">${esc(a.at)}</td></tr>`), { tall: true, empty: 'No assignments yet' })}</div>`;
  }
};
P['bk-t'] = d => {
  const q = UI.bkQ.trim().toLowerCase();
  const rows = d.effective.filter(e => (!q || (e.ucc + ' ' + e.name + ' ' + e.tags.join(' ')).toLowerCase().includes(q)) && (!UI.bkPlan || (UI.bkPlan === '-' ? !e.plan : e.plan === UI.bkPlan)));
  return tbl([['Client'], ['Groups'], ['Plan'], ['Because'], ['Since'], ['Segment cards']], rows.map(e => `<tr><td><button class="linkish mono" data-act="client" data-id="${e.ucc}">${e.ucc}</button> ${esc(e.name)}</td><td>${e.tags.map(t => `<span class="gtag">${esc(t)}</span>`).join(' ') || '<span class="dim">—</span>'}</td><td><b style="font-weight:600">${esc(e.plan_name)}</b>${e.override ? ' <span class="tag">own rates</span>' : ''}</td><td>${esc(e.source)}</td><td class="n dim">${e.from ? fmtDate(e.from) : ''}</td><td class="small">${(e.segments || []).map(s => `${esc(scopeLabel(s.scope))}: ${esc(s.plan_name)}`).join('<br>') || '<span class="dim">—</span>'}</td></tr>`), { tall: true, empty: 'No clients match' });
};
// rateEditor is the rate-line table used for plans and client rates.
function rateEditor(d, rules) {
  const opts = (xs, v) => xs.map(([k, l]) => `<option value="${esc(k)}" ${v === k ? 'selected' : ''}>${esc(l)}</option>`).join('');
  const row = (k, r) => { const x = rk(k || (d.products[0].bucket));
    return `<tr><td><select class="in sm re-b">${opts(d.products.map(p => [p.bucket, p.name]), x.b)}</select></td><td><select class="in sm re-x">${opts([['*', 'All']].concat(BK_EXCH.map(e => [e, e])), x.x)}</select></td><td><select class="in sm re-ch">${opts([['*', 'All']].concat(d.setup.channels.map(c => [c, c])), x.ch)}</select></td><td><select class="in sm re-cs">${opts([['*', 'All']].concat(d.cases.map(c => [c.case, c.name])), x.cs)}</select></td><td><select class="in sm re-mode">${opts(MODES, r.mode)}</select></td>${RATE_FIELDS.map(([f]) => `<td><input class="in sm re-${f}" inputmode="decimal" value="${esc(r[f] && r[f] !== '0' ? r[f] : '')}" aria-label="${f}"></td>`).join('')}<td><button class="btn sm" type="button" data-re="del" aria-label="Remove line">×</button></td></tr>`; };
  const ks = rkSort(d, Object.keys(rules || {}));
  return `<div class="tw"><table class="tbl ret"><thead><tr><th>Product</th><th>Exchange</th><th>Channel</th><th>Case</th><th>Charge</th>${RATE_FIELDS.map(([, l]) => `<th class="r">${l}</th>`).join('')}<th></th></tr></thead><tbody>${ks.map(k => row(k, rules[k])).join('')}</tbody></table></div>
    <div class="split" style="margin:8px 0"><button class="btn sm" type="button" data-re="add">Add a rate line</button></div><template id="re-blank">${row('', { mode: 'pct' })}</template>
    <p class="dim small">Fill only what the charge uses: % for “% of turnover”, ₹/order for flat, both for “lower of”, ₹/lot or ₹/share for those. Minimums per share and per lot grow with the order; min and max per order bound it. For an order the most specific line wins: case, then channel, then exchange. Expiry, exercise and assignment are charged only where a line for them exists. Products with no line use the charge schedule. Cash-market brokerage never exceeds 2.5% of the order value.</p>`;
}
function rateEditorWire(form) {
  form.addEventListener('click', e => { const b = e.target.closest('[data-re]'); if (!b) return;
    if (b.dataset.re === 'del') b.closest('tr').remove();
    if (b.dataset.re === 'add') form.querySelector('.ret tbody').insertAdjacentHTML('beforeend', $('re-blank').innerHTML); });
}
function rateEditorRead(form) {
  const rules = {};
  for (const tr of form.querySelectorAll('.ret tbody tr')) {
    const k = rkJoin(tr.querySelector('.re-b').value, tr.querySelector('.re-x').value, tr.querySelector('.re-ch').value, tr.querySelector('.re-cs').value);
    if (rules[k]) throw { message: 'Two lines are for the same product, exchange, channel and case' };
    const r = { mode: tr.querySelector('.re-mode').value };
    RATE_FIELDS.forEach(([f]) => { const v = tr.querySelector('.re-' + f).value.trim(); if (v) r[f] = v; });
    rules[k] = r;
  }
  return rules;
}
function planModal(id) {
  const d = C.brokerage, p = d.plans.find(x => x.id === id);
  const cur = p ? (p.upcoming && !p.current ? p.upcoming : p.current) : null;
  wideModal(mhd(p ? 'Edit plan · ' + esc(p.name) : 'New brokerage plan', 'Rates apply to orders from the effective date. Earlier versions stay on record; a version on the same date is replaced.') + `<form class="bd" id="planf">
    <div class="form"><div class="fld"><label for="pm-name">Name</label><input class="in" id="pm-name" value="${esc(p?.name || '')}" required placeholder="e.g. Active trader"></div><div class="fld"><label for="pm-from">Effective from</label><input class="in" type="date" id="pm-from" value="${esc(p ? S.date : '2026-04-01')}" required></div>
    <div class="fld full"><label for="pm-note">Note</label><input class="in" id="pm-note" value="${esc(p?.note || '')}" placeholder="Who this plan is for"></div></div>
    ${rateEditor(d, cur?.rules || {})}
    <div id="pm-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save plan</button></div></form>`);
  rateEditorWire($('planf'));
  $('planf').onsubmit = async e => {
    e.preventDefault();
    try { await post('/v1/brokerage/plans', { id: id || '', name: $('pm-name').value, note: $('pm-note').value, from: $('pm-from').value, rules: rateEditorRead($('planf')) }); closeModal(); toast('Plan saved'); render({ keep: true }); }
    catch (err) { $('pm-err').innerHTML = errHTML(err); }
  };
}
function clientRatesModal(ucc) {
  const d = C.brokerage, ov = d.overrides.find(o => o.ucc === ucc), cur = ov?.current;
  wideModal(mhd('Client rates' + (ucc ? ' · ' + esc(ucc) : ''), 'A client’s own rate lines win over their card for the orders they fit; anything else follows the card. From the effective date; remove every line to end them.') + `<form class="bd" id="crf">
    <div class="form"><div class="fld"><label for="cr-ucc-q">Client</label>${cpick('cr-ucc', ucc || '')}</div><div class="fld"><label for="cr-from">Effective from</label><input class="in" type="date" id="cr-from" value="${esc(S.date)}" required></div>
    <div class="fld full"><label for="cr-note">Note</label><input class="in" id="cr-note" value="${esc(cur?.note || '')}" placeholder="Why this client has their own rates"></div></div>
    ${rateEditor(d, cur?.rules || {})}
    <div id="cr-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save client rates</button></div></form>`);
  rateEditorWire($('crf'));
  $('crf').onsubmit = async e => {
    e.preventDefault();
    if (!$('cr-ucc').value) { $('cr-err').innerHTML = errHTML({ message: 'Choose the client' }); return; }
    try { await post('/v1/brokerage/client-rates', { ucc: $('cr-ucc').value, from: $('cr-from').value, note: $('cr-note').value, rules: rateEditorRead($('crf')) }); closeModal(); toast('Client rates saved'); render({ keep: true }); }
    catch (err) { $('cr-err').innerHTML = errHTML(err); }
  };
}
function bkSetupModal() {
  const s = C.brokerage.setup;
  modal(mhd('Trading channels', 'Name the channels you price separately. Each trade gets a channel from its terminal (CTCL) ID: the first rule that matches, else the default. * matches any run of characters. Terminal rules can also be loaded as TERMINAL-CHANNELS on Files.') + `<form class="bd" id="bsf">
    <div class="form"><div class="fld"><label for="bs-ch">Channels</label><input class="in" id="bs-ch" value="${esc(s.channels.join(', '))}" placeholder="dealer, online, mobile"></div><div class="fld"><label for="bs-def">Default channel</label><input class="in" id="bs-def" value="${esc(s.default_channel)}"></div>
    <div class="fld full"><label for="bs-t">Terminal rules, one per line: terminal = channel</label><textarea class="in mono" id="bs-t" rows="6" placeholder="1234567*=online">${esc(s.terminals.map(t => t.terminal + ' = ' + t.channel).join('\n'))}</textarea></div>
    <div class="fld"><label for="bs-auc">Auction settlement types</label><input class="in" id="bs-auc" value="${esc(s.auction_settlements.join(', '))}"></div><div class="fld"><label for="bs-idx">More index underlyings</label><input class="in" id="bs-idx" value="${esc(s.index_underlyings.join(', '))}" placeholder="NIFTY, BANKNIFTY, SENSEX and others are built in"></div></div>
    <div id="bs-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
  const list = v => v.split(',').map(x => x.trim()).filter(Boolean);
  $('bsf').onsubmit = async e => {
    e.preventDefault();
    const terminals = $('bs-t').value.split('\n').map(l => l.trim()).filter(Boolean).map(l => { const [t, c] = l.split('='); return { terminal: (t || '').trim(), channel: (c || '').trim() }; });
    try { await post('/v1/brokerage/setup', { channels: list($('bs-ch').value), default_channel: $('bs-def').value, terminals, auction_settlements: list($('bs-auc').value), index_underlyings: list($('bs-idx').value) }); closeModal(); toast('Channels saved'); render({ keep: true }); }
    catch (err) { $('bs-err').innerHTML = errHTML(err); }
  };
}
function assignModal(target, scope) {
  const d = C.brokerage; let kind = target === 'default' ? 'default' : target.startsWith('tag:') ? 'tag' : target.startsWith('ucc:') ? 'ucc' : 'tag';
  const tagV = target.startsWith('tag:') ? target.slice(4) : '', uccV = target.startsWith('ucc:') ? target.slice(4) : '';
  const cur = d.current[target + '#' + (scope || '')]?.plan || '', [seg0, ex0] = (scope || '').split('-');
  modal(mhd('Assign a brokerage plan', 'Applies from the effective date. Earlier bills keep their brokerage unless the day is rebilled. A plan for one segment or exchange is tried before the all-segments plan of the same client, group or firm.') + `<form class="bd" id="asf">
    <div class="fld"><label>Apply to</label><div class="seg" id="as-kind">${[['default', 'Firm default'], ['tag', 'A group'], ['ucc', 'A client']].map(([k, l]) => `<button type="button" class="${kind === k ? 'on' : ''}" data-k="${k}">${l}</button>`).join('')}</div></div>
    <div class="fld as-tag" ${kind === 'tag' ? '' : 'hidden'}><label for="as-tag">Group</label><input class="in" id="as-tag" list="as-tags" value="${esc(tagV)}" placeholder="e.g. HNI"><datalist id="as-tags">${d.tags.map(t => `<option value="${esc(t.tag)}">${t.clients} clients</option>`).join('')}</datalist></div>
    <div class="fld as-ucc" ${kind === 'ucc' ? '' : 'hidden'}><label for="as-ucc-q">Client</label>${cpick('as-ucc', uccV)}</div>
    <div class="form"><div class="fld"><label for="as-plan">Plan</label><select class="in" id="as-plan">${d.plans.map(p => `<option value="${esc(p.id)}" ${cur === p.id ? 'selected' : ''}>${esc(p.name)}</option>`).join('')}</select></div>
    <div class="fld"><label for="as-from">Effective from</label><input class="in" type="date" id="as-from" value="${esc(S.date)}"></div>
    <div class="fld"><label for="as-seg">Segment</label><select class="in" id="as-seg"><option value="">All segments</option>${d.segments.map(s => `<option ${seg0 === s ? 'selected' : ''}>${s}</option>`).join('')}</select></div>
    <div class="fld"><label for="as-ex">Exchange</label><select class="in" id="as-ex"><option value="">All exchanges</option>${BK_EXCH.map(x => `<option ${ex0 === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div></div>
    <div id="as-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Assign</button></div></form>`);
  $('as-kind').onclick = e => { const b = e.target.closest('button'); if (!b) return; kind = b.dataset.k; $('as-kind').querySelectorAll('button').forEach(x => x.classList.toggle('on', x === b));
    document.querySelector('.as-tag').hidden = kind !== 'tag'; document.querySelector('.as-ucc').hidden = kind !== 'ucc'; };
  $('asf').onsubmit = async e => {
    e.preventDefault();
    const t = kind === 'default' ? 'default' : kind === 'tag' ? 'tag:' + $('as-tag').value.trim() : 'ucc:' + $('as-ucc').value;
    if (t === 'tag:' || t === 'ucc:') { $('as-err').innerHTML = errHTML({ message: kind === 'tag' ? 'Choose the group' : 'Choose the client' }); return; }
    const seg = $('as-seg').value, ex = $('as-ex').value;
    if (ex && !seg) { $('as-err').innerHTML = errHTML({ message: 'Choose the segment for the exchange' }); return; }
    try { await post('/v1/brokerage/assignments', { target: t, scope: seg ? seg + (ex ? '-' + ex : '') : '', plan: $('as-plan').value, from: $('as-from').value }); closeModal(); toast('Plan assigned'); render({ keep: true }); }
    catch (err) { $('as-err').innerHTML = errHTML(err); }
  };
}

/* ---------- reports ---------- */
V.reports = {
  load: () => get(`/v1/reports?date=${S.date}`),
  html: d => {
    const rs = d.reports, k = { Uploaded: 'good', Validated: 'acc', Generated: 'warn', 'Not generated': 'nt' };
    const canGen = has('Ops maker', 'Ops checker', 'Compliance officer', 'Risk manager'), canUp = has('Compliance officer', 'Ops checker');
    return ph('Regulatory reports', 'Each report is generated from the ledger and records, validated, downloaded for upload to the exchange portal, then marked uploaded with the acknowledgement number. Every step and download is in the audit trail. Where the exchange’s exact upload layout is known, the report links to it on Exchange files; the CSV here stays as the firm’s own register of the same figures.') +
    `<div class="kpis">${kpi('Daily reports outstanding', rs.filter(r => r.frequency === 'Daily' && r.status !== 'Uploaded').length, 'for ' + fmtDate(S.date), 'warn')}${kpi('Uploaded', rs.filter(r => r.status === 'Uploaded').length, 'this date', 'good')}${kpi('Validated', rs.filter(r => r.status === 'Validated').length, 'ready to upload')}</div>
    <div class="panel">${tbl([['Report'], ['Submitted to'], ['When due'], ['Rows', 'r'], ['Status'], ['Actions']], rs.map(r => `<tr><td><b style="font-weight:600">${esc(r.name)}</b><div class="dim" style="font-size:11.5px">${esc(r.format)}</div>${(r.upload || []).length ? `<div style="font-size:11.5px">The exchange's own upload file: <a href="#xfiles" data-act="go" data-id="xfiles">${r.upload.map(esc).join(', ')}</a> on Exchange files</div>` : ''}</td><td>${esc(r.to)}</td><td>${esc(r.frequency)}<div class="dim" style="font-size:11.5px">${esc(r.due)}</div></td><td class="r">${r.status !== 'Not generated' ? r.rows : '—'}</td><td>${pill(r.status, k[r.status])}${r.ack ? ` <span class="tag">ACK ${esc(r.ack)}</span>` : ''}${r.by ? `<div class="dim" style="font-size:11.5px">${esc(r.by)} · ${esc(r.at)}</div>` : ''}</td><td><div class="split"><button class="btn sm" data-act="dl" data-id="/v1/reports/${r.code}/download?date=${S.date}" data-name="${r.code}_${S.date}.csv">${r.code === 'cc01' ? 'Review' : 'Download'}</button>${r.code === 'cc01' && canGen ? ['CM', 'FO', 'CD'].map(g => `<button class="btn sm" data-act="dl" data-id="/v1/collateral-file?date=${S.date}&segment=${g}" data-name="CC_${g}_${S.date}.csv.gz" title="Upload file for ${g}">${g}</button>`).join('') : ''}${canGen && r.status !== 'Uploaded' ? `<button class="btn sm" data-act="rstep" data-id="${r.code}" data-a="validate">Validate</button>` : ''}${canUp && r.status === 'Validated' ? `<button class="btn sm pri" data-act="rup" data-id="${r.code}">Mark uploaded</button>` : ''}</div></td></tr>`))}</div>
    <div class="panel"><div class="hd"><h3>Due in the next month</h3><small>Periodic work and the compliance calendar</small></div>${tbl([['Due'], ['Obligation'], ['Owner'], ['Status']], (d.calendar || []).map(c => `<tr><td class="n">${fmtDate(c.date)}</td><td><a href="#${c.view}" data-act="go" data-id="${c.view}">${esc(c.obligation)}</a></td><td>${esc(c.owner)}</td><td>${pill(c.status, c.status === 'Overdue' ? 'bad' : 'warn')}</td></tr>`), { empty: 'Nothing due.' })}</div>`;
  }
};

/* ---------- surveillance ---------- */
V.surv = {
  load: () => get('/v1/alerts'),
  html: d => {
    const by = s => d.filter(a => a.status === s).length, isC = has('Compliance officer'), canRev = has('Compliance officer', 'Ops checker');
    return ph('Surveillance & AML', 'Exchange alerts and internal rules in one case queue: turnover against declared income, third-party funds, frequent client code changes, dormant accounts. Closing or filing an STR needs the compliance officer and a written rationale.', isC || has('Ops checker') ? '<button class="btn pri" data-act="rules">Run internal rules</button>' : '') +
    `<div class="kpis">${kpi('Open', by('Open'), '', by('Open') ? 'bad' : 'good')}${kpi('Under review', by('Under review'), '', 'warn')}${kpi('STR filed', by('STR filed'), 'with FIU-IND')}${kpi('Closed', by('Closed'), 'with rationale', 'good')}</div>
    <div class="panel">${tbl([['Alert'], ['Client'], ['Detail'], ['Severity'], ['Status'], ['Actions']], d.map(a => `<tr><td class="wrap"><b style="font-weight:600">${esc(a.type)}</b><div class="dim mono" style="font-size:11px">${esc(a.source)} · ${esc(a.id)}</div></td><td>${a.ucc ? `<button class="linkish mono" data-act="client" data-id="${a.ucc}">${a.ucc}</button><div class="dim" style="font-size:11.5px">${esc(a.name || '')}</div>` : ''}</td><td class="wrap">${esc(a.detail)}${(a.notes || []).length ? `<div class="dim" style="font-size:11.5px;margin-top:3px">${a.notes.map(esc).join('<br>')}</div>` : ''}</td><td>${pill(a.severity, a.severity === 'High' ? 'bad' : a.severity === 'Medium' ? 'warn' : 'nt')}</td><td>${pill(a.status, a.status === 'Closed' ? 'good' : a.status === 'STR filed' ? 'acc' : a.status === 'Under review' ? 'warn' : 'bad')}</td><td><div class="split">${a.status === 'Open' && canRev ? `<button class="btn sm" data-act="alert" data-id="${esc(a.id)}" data-a="review">Review</button>` : ''}${(a.status === 'Open' || a.status === 'Under review') && isC ? `<button class="btn sm" data-act="alert" data-id="${esc(a.id)}" data-a="close">Close</button><button class="btn sm bad" data-act="alert" data-id="${esc(a.id)}" data-a="str">File STR</button>` : ''}</div></td></tr>`), { tall: true, empty: 'No alerts.' })}</div>`;
  }
};

/* ---------- inactive accounts ---------- */
V.inactive = {
  load: () => get(`/v1/inactive?date=${S.date}`),
  html: d => {
    const c = d.counts, can = has('Ops checker', 'Compliance officer'), mk = has('Ops maker', 'Ops checker');
    const pend = d.requests.filter(r => r.status === 'Pending');
    return ph('Inactive accounts', `A trading account with no activity through the firm for ${d.months} months is flagged <b>Inactive</b> in every exchange’s UCC database and cannot trade until reactivated (NSE circular NSE/INSP/64718 of 25-Oct-2024 and the matching BSE and MCX circulars). Activity means a trade on any exchange or segment, an OFS, buy-back or open-offer bid, an IPO bid not cancelled, an SGB application, a mutual fund lumpsum or SIP purchase on an exchange platform, or a change of email, mobile or address uploaded to the KRA.`) +
    `<div class="kpis">${kpi('Active', c.Active || 0, `activity within ${d.months} months`, 'good')}${kpi('Approaching', c.Approaching || 0, 'inactive within 60 days', c.Approaching ? 'warn' : '')}${kpi('Due to be flagged', c.Due || 0, 'last activity on or before ' + fmtDate(d.cutoff), c.Due ? 'bad' : 'good')}${kpi('Flagged inactive', (c.Inactive || 0) + (c['Reactivation pending'] || 0), (c['Inactive with assets'] || 0) + ' still hold funds or securities')}${kpi('Reactivation pending', pend.length, 'awaiting a checker', pend.length ? 'warn' : '')}</div>
    <div class="note">You may tell clients that their account will become inactive, but you must not ask them to trade to avoid it. Funds and securities of inactive clients are still returned through running account settlement. Inactive clients with nil balances drop out of the holding and collateral reporting.</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('inTab', [['due', `Due to flag · ${c.Due || 0}`], ['approaching', `Approaching · ${c.Approaching || 0}`], ['inactive', `Inactive · ${(c.Inactive || 0) + (c['Reactivation pending'] || 0)}`], ['requests', `Reactivations · ${d.requests.length}`], ['all', 'All accounts']])}<input class="in" data-ui="inQ" data-part="in-t" placeholder="Filter name, UCC or group" value="${esc(UI.inQ)}"></div>
      ${UI.inTab === 'due' && can && c.Due ? `<button class="btn sm pri" data-act="flagInactive">Flag selected inactive</button>` : ''}</div><div id="in-t">${P['in-t'](d)}</div></div>`;
  }
};
P['in-t'] = d => {
  const q = UI.inQ.trim().toLowerCase(), f = r => !q || (r.ucc + ' ' + r.name + ' ' + r.tags.join(' ')).toLowerCase().includes(q);
  const can = has('Ops checker', 'Compliance officer'), mk = has('Ops maker', 'Ops checker');
  const last = r => r.last_activity ? `${fmtDate(r.last_activity)} <span class="dim">· ${esc(r.activity_kind)}</span>` : '<span class="dim">None recorded</span>';
  const who = r => `<button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}${r.tags.length ? ' ' + r.tags.map(t => `<span class="gtag">${esc(t)}</span>`).join('') : ''}`;
  const assets = r => `${num(r.funds) ? inr(r.funds) : '—'}${r.holdings ? ` <span class="dim">· ${r.holdings} scrip${r.holdings > 1 ? 's' : ''}, ${cmp(r.securities)}</span>` : ''}`;
  if (UI.inTab === 'requests') return tbl([['Request'], ['Client'], ['Verification'], ['Details'], ['KRA'], ['Maker'], ['Status'], ['']], d.requests.filter(r => f({ ...r, tags: [] })).map(r => `<tr><td class="n">${r.id}<div class="dim small">${fmtDate(r.date)}</div></td><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}</td><td>${r.ipv_mode} on ${fmtDate(r.ipv_date)}</td><td class="wrap">${r.details_changed ? 'Updated: ' + [r.email && 'email ' + esc(r.email), r.mobile && 'mobile ' + esc(r.mobile), r.city && 'address ' + esc(r.city)].filter(Boolean).join(', ') : 'Confirmed unchanged'}${r.note ? `<div class="dim small">${esc(r.note)}</div>` : ''}</td><td>${pill(r.kra_status, 'good')}</td><td>${esc(r.maker)}</td><td>${pill(r.status, r.status === 'Approved' ? 'good' : r.status === 'Rejected' ? 'bad' : 'warn')}${r.checker ? `<div class="dim small">${esc(r.checker)}${r.reason ? ' · ' + esc(r.reason) : ''}</div>` : ''}</td><td>${r.status === 'Pending' && can && r.maker !== S.me.actor ? `<div class="split"><button class="btn sm pri" data-act="reactDecide" data-id="${r.id}" data-a="1">Approve</button><button class="btn sm bad" data-act="reactDecide" data-id="${r.id}" data-a="0">Reject</button></div>` : ''}</td></tr>`), { empty: 'No reactivation requests' });
  const st = { due: ['Due'], approaching: ['Approaching'], inactive: ['Inactive', 'Reactivation pending'], all: ['Active', 'Approaching', 'Due', 'Inactive', 'Reactivation pending'] }[UI.inTab];
  const rows = d.rows.filter(r => st.includes(r.state) && f(r));
  if (UI.inTab === 'due') {
    for (const u of [...UI.inSel]) if (!rows.some(r => r.ucc === u)) UI.inSel.delete(u);
    const allOn = rows.length && rows.every(r => UI.inSel.has(r.ucc));
    return tbl([...(can ? [[`<input type="checkbox" data-act="inSelAll" ${allOn ? 'checked' : ''} aria-label="Select all">`]] : []), ['Client'], ['Last activity'], ['Inactive from'], ['Funds / securities']], rows.map(r => `<tr class="${UI.inSel.has(r.ucc) ? 'sel' : ''}">${can ? `<td><input type="checkbox" data-act="inSelOne" data-id="${r.ucc}" ${UI.inSel.has(r.ucc) ? 'checked' : ''} aria-label="Select ${r.ucc}"></td>` : ''}<td>${who(r)}</td><td>${last(r)}</td><td class="n">${r.inactive_from ? fmtDate(r.inactive_from) : '—'}</td><td>${assets(r)}</td></tr>`), { empty: 'No accounts are due. Every active client has had activity in the period.' });
  }
  if (UI.inTab === 'inactive') return tbl([['Client'], ['Inactive since'], ['Last activity'], ['Funds / securities'], ['Status'], ['']], rows.map(r => `<tr><td>${who(r)}</td><td class="n">${fmtDate(r.inactive_since)}</td><td>${last(r)}</td><td>${assets(r)}</td><td>${pill(r.state, r.state === 'Inactive' ? 'nt' : 'warn')}</td><td>${r.state === 'Inactive' && mk ? `<button class="btn sm" data-act="reactivate" data-id="${r.ucc}">Request reactivation</button>` : ''}</td></tr>`), { empty: 'No inactive accounts' });
  return tbl([['Client'], ['Last activity'], ['Inactive from'], ['Days left', 'r'], ['State']], rows.map(r => `<tr><td>${who(r)}</td><td>${last(r)}</td><td class="n">${r.inactive_from ? fmtDate(r.inactive_from) : '—'}</td><td class="r">${['Active', 'Approaching'].includes(r.state) ? nf(r.days_left) : '—'}</td><td>${pill(r.state, { Active: 'good', Approaching: 'warn', Due: 'bad', Inactive: 'nt' }[r.state] || 'warn')}</td></tr>`), { tall: true, empty: 'No accounts' });
};
async function reactivateModal(ucc) {
  const [cd] = await Promise.all([get(`/v1/clients/${encodeURIComponent(ucc)}?date=${S.date}`)]); const c = cd.client;
  modal(mhd('Reactivate ' + esc(ucc), esc(c.name) + ' · inactive since ' + fmtDate(c.inactive_since)) + `<form class="bd" id="raf">
    <div class="form"><div class="fld"><label for="ra-mode">Verification</label><select class="in" id="ra-mode"><option value="VIPV">Video in-person verification (VIPV)</option><option value="IPV">In-person verification (IPV)</option></select></div>
    <div class="fld"><label for="ra-date">Done on</label><input class="in" type="date" id="ra-date" value="${esc(S.date)}" max="${esc(S.date)}"></div></div>
    <div class="fld"><label>Address, mobile, email, bank, demat and income</label><div class="seg" id="ra-ch"><button type="button" class="on" data-v="0">Confirmed unchanged</button><button type="button" data-v="1">Some changed</button></div></div>
    <div class="form ra-changes" hidden><div class="fld"><label for="ra-email">New email</label><input class="in" id="ra-email" placeholder="${esc(c.email)}"></div><div class="fld"><label for="ra-mobile">New mobile</label><input class="in" id="ra-mobile" placeholder="${esc(c.mobile)}"></div><div class="fld full"><label for="ra-city">New address (city)</label><input class="in" id="ra-city" placeholder="${esc(c.city)}"></div><p class="dim small full">Bank, demat or income changes go through the usual modification forms; note them below.</p></div>
    <div class="fld"><label for="ra-kra">KRA status checked today</label><select class="in" id="ra-kra"><option>Validated</option><option>Registered</option><option>On hold</option><option>Rejected</option><option>Not available</option></select><span class="dim small">Only Validated or Registered allows reactivation; otherwise collect documents and upload to the KRA first.</span></div>
    <div class="fld"><label for="ra-note">Note</label><textarea class="in" id="ra-note" placeholder="e.g. VIPV recording ref, documents collected"></textarea></div>
    <div id="ra-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Send for approval</button></div></form>`);
  let changed = false;
  $('ra-ch').onclick = e => { const b = e.target.closest('button'); if (!b) return; changed = b.dataset.v === '1'; $('ra-ch').querySelectorAll('button').forEach(x => x.classList.toggle('on', x === b)); document.querySelector('.ra-changes').hidden = !changed; };
  $('raf').onsubmit = async e => {
    e.preventDefault();
    try { await post('/v1/inactive/reactivations', { date: S.date, ucc, ipv_mode: $('ra-mode').value, ipv_date: $('ra-date').value, details_changed: changed, email: changed ? $('ra-email').value : '', mobile: changed ? $('ra-mobile').value : '', city: changed ? $('ra-city').value : '', kra_status: $('ra-kra').value, note: $('ra-note').value });
      closeModal(); toast('Reactivation sent for approval'); if (S.view === 'inactive') { UI.inTab = 'requests'; render({ keep: true }); } }
    catch (err) { $('ra-err').innerHTML = errHTML(err); }
  };
}

/* ---------- IPO / MF ---------- */
V.ipo = {
  load: async () => { const [ipo, mf] = await Promise.all([get('/v1/ipo'), get('/v1/mf')]); return { ipo, mf }; },
  html: d => ph('IPO & mutual funds', 'IPO bids through UPI mandates (ASBA) from the exchange bidding platform, and mutual fund orders through exchange platforms. Allotment results come from the registrar file.', S.me?.dev && d.ipo.some(x => x.allotment === 'Awaiting') ? '<button class="btn pri" data-act="allotSample">Load sample allotment file</button>' : '<button class="btn" data-act="go" data-id="files">Upload allotment file</button>') +
    `<div class="kpis">${kpi('IPO applications', d.ipo.length, inr(sum(d.ipo, x => num(x.amount))) + ' bid')}${kpi('Mandates accepted', d.ipo.filter(x => x.mandate === 'Accepted').length, d.ipo.filter(x => x.mandate !== 'Accepted').length + ' pending or declined')}${kpi('Allotted', d.ipo.filter(x => x.allotted_lots > 0).length, d.ipo.filter(x => x.allotment === 'Awaiting').length + ' awaiting results', 'good')}${kpi('MF orders', d.mf.length, inr(sum(d.mf, x => num(x.amount))))}</div>
    <div class="panel"><div class="hd"><h3>IPO applications</h3>${has('Ops maker', 'Ops checker') && d.ipo.some(x => x.allotted_lots > 0) ? '<div class="tools"><button class="btn sm" data-act="ipoPost">Post allotments as purchases</button></div>' : ''}</div>${tbl([['Application'], ['UCC'], ['Client'], ['Issue'], ['Lots', 'r'], ['Amount (₹)', 'r'], ['UPI ID'], ['Mandate'], ['Allotment']], d.ipo.map(x => `<tr><td class="n">${esc(x.id)}</td><td class="n">${x.ucc}</td><td>${esc(x.name)}</td><td>${esc(x.issue)}</td><td class="r">${x.lots}</td><td class="r">${inr(x.amount)}</td><td class="n dim">${esc(x.upi)}</td><td>${pill(x.mandate, x.mandate === 'Accepted' ? 'good' : x.mandate === 'Declined' ? 'bad' : 'warn')}</td><td>${pill(x.allotment, x.allotted_lots > 0 ? 'good' : x.allotment === 'Awaiting' ? 'warn' : x.allotment.startsWith('Rejected') ? 'bad' : 'nt')}</td></tr>`), { empty: 'No IPO bids. Upload IPO-BIDS.' })}</div>
    <div class="panel"><div class="hd"><h3>Mutual fund orders</h3></div>${tbl([['Order'], ['UCC'], ['Client'], ['Scheme'], ['Type'], ['Amount (₹)', 'r'], ['Platform'], ['Status']], d.mf.map(x => `<tr><td class="n">${esc(x.id)}</td><td class="n">${x.ucc}</td><td>${esc(x.name)}</td><td>${esc(x.scheme)}</td><td>${esc(x.type)}</td><td class="r">${inr(x.amount)}</td><td>${esc(x.platform)}</td><td>${pill(x.status, x.status === 'Allotted' ? 'good' : x.status === 'Payment pending' ? 'warn' : 'acc')}</td></tr>`), { empty: 'No MF orders. Upload MF-ORDERS.' })}</div>`
};

/* ---------- client reports ---------- */
function shiftBD(d, n) { const t = new Date(d + 'T00:00:00Z'); const hol = new Set(S.me?.holidays || []); do { t.setUTCDate(t.getUTCDate() + n); } while ([0, 6].includes(t.getUTCDay()) || hol.has(t.toISOString().slice(0, 10))); return t.toISOString().slice(0, 10); }
const prevBD = d => shiftBD(d, -1), nextBD = d => shiftBD(d, 1);
const fyStart = d => { const [y, m] = d.split('-').map(Number); return `${m >= 4 ? y : y - 1}-04-01`; };
function crRange(preset, date) {
  const fy = fyStart(date), y = +fy.slice(0, 4);
  switch (preset) {
    case 'day': return [date, date];
    case 'week': { const t = new Date(date + 'T00:00:00Z'); const back = (t.getUTCDay() + 2) % 7; t.setUTCDate(t.getUTCDate() - back); const fri = t.toISOString().slice(0, 10); t.setUTCDate(t.getUTCDate() - 6); return [t.toISOString().slice(0, 10), fri]; }
    case 'month': return [date.slice(0, 8) + '01', date];
    case 'fy': return [fy, date];
    case 'lastfy': return [`${y - 1}-04-01`, `${y}-03-31`];
    case 'q': { const m = +date.slice(5, 7), qs = m >= 4 ? Math.floor((m - 4) / 3) * 3 + 4 : 1; return [`${date.slice(0, 4)}-${String(qs).padStart(2, '0')}-01`, date]; }
  }
  return [UI.cr.from || fy, UI.cr.to || date];
}
V.statements = {
  load: async () => {
    const [defs] = await Promise.all([get('/v1/client-reports'), clientCache()]);
    const act = CL.rows.filter(c => c.status === 'Active');
    if (!UI.cr.ucc || (UI.cr.ucc !== 'ALL' && !UI.cr.ucc.startsWith('TAG:') && !act.some(c => c.ucc === UI.cr.ucc))) UI.cr.ucc = act[0]?.ucc || '';
    return { defs, clients: act };
  },
  html: d => {
    const cr = UI.cr, def = d.defs.find(x => x.kind === cr.kind) || d.defs[0];
    if ((cr.ucc === 'ALL' || cr.ucc.startsWith('TAG:')) && !def.all_ucc) cr.ucc = d.clients[0]?.ucc || '';
    const [from, to] = def.range ? crRange(cr.preset, S.date) : ['', cr.preset === 'custom' && cr.to ? cr.to : S.date];
    cr.from = from; cr.to = to;
    const presets = [['day', 'Today'], ['week', 'Last week'], ['month', 'This month'], ['q', 'This quarter'], ['fy', 'This FY'], ['lastfy', 'Last FY'], ['custom', 'Custom']];
    return ph(clientUser() ? 'Reports & statements' : 'Client reports', clientUser() ? 'Your statements and reports for any period: on screen, as a printable page (save as PDF), or as CSV.' : 'Statements and reports for any client and period: on screen, as a printable page (save as PDF), or as CSV. Every number comes from the ledger, trade records and holdings; every print and download is in the audit trail.') +
    `<div class="rtypes">${d.defs.map(x => `<button class="${x.kind === def.kind ? 'on' : ''}" data-act="crKind" data-id="${x.kind}"><b>${esc(x.name)}</b><span>${esc(x.about)}</span></button>`).join('')}</div>
    <div class="panel"><div class="bd"><div class="crform">
      <div class="fld cr-client"><label for="cr-ucc-q">${clientUser() ? 'Account' : 'Client' + (def.all_ucc ? ' or group' : '')}</label>${cpick('cr-ucc', cr.ucc, { all: def.all_ucc && !clientUser(), groups: def.all_ucc && !clientUser(), placeholder: def.all_ucc ? 'Search name, UCC, PAN or group' : undefined, on: v => { UI.cr.ucc = v; } })}</div>
      ${def.range ? `<div class="fld"><label>Period</label><div class="seg">${presets.map(([k, l]) => `<button class="${cr.preset === k ? 'on' : ''}" data-act="crPreset" data-id="${k}">${l}</button>`).join('')}</div></div>
      <div class="fld"><label for="cr-from">From</label><input class="in" type="date" id="cr-from" value="${from}"></div><div class="fld"><label for="cr-to">To</label><input class="in" type="date" id="cr-to" value="${to}"></div>`
      : `<div class="fld"><label for="cr-to">As on</label><input class="in" type="date" id="cr-to" value="${to}"></div>`}
      <div class="acts"><button class="btn pri" data-act="crRun">View report</button><button class="btn" data-act="crPrint">Print / PDF</button><button class="btn" data-act="crCSV">Download CSV</button></div>
    </div></div></div>
    <div id="cr-out">${crOut()}</div>`;
  }
};
function crURL(fmt) { const c = UI.cr; return `/v1/client-reports/${c.kind}?ucc=${encodeURIComponent(c.ucc)}&to=${c.to}${c.from ? '&from=' + c.from : ''}${fmt ? '&format=' + fmt : ''}`; }
function crCell(t, v) {
  if (v === '' || v == null) return '';
  switch (t) {
    case 'money': return `<span class="${num(v) < 0 ? 'neg' : ''}">${nf(v, 2)}</span>`;
    case 'drcr': return drcr(v);
    case 'qty': return nf(v);
    case 'pct': return `<span class="${num(v) < 0 ? 'neg' : ''}">${esc(v)}%</span>`;
    case 'date': return fmtDate(v);
  }
  return esc(v);
}
const crR = t => ['money', 'drcr', 'qty', 'pct'].includes(t) ? 'r' : '';
function crOut() {
  if (UI.crBusy) return '<p class="loading">Preparing report…</p>';
  if (UI.crErr) return errHTML(UI.crErr);
  const r = UI.crRes; if (r) r.sections = r.sections || []; if (!r) return '<div class="panel"><div class="empty">Choose a report, client and period, then View report.</div></div>';
  const sv = s => { if (s.type === 'money') return inr(s.value, 2); if (s.type === 'drcr') return '₹' + drcr(s.value); return nf(s.value); };
  return `<div class="panel"><div class="hd"><div><h3>${esc(r.title)} · ${esc(r.name)}${r.ucc !== 'ALL' && !r.ucc.startsWith('TAG:') ? ` <span class="mono dim">${esc(r.ucc)}</span>` : ''}</h3></div><small>${esc(r.period)} · generated ${esc(r.generated)}</small></div>
    <div class="bd"><div class="kpis" style="margin:0">${(r.summary || []).map(s => kpi(esc(s.label), sv(s), '', s.tone === 'good' ? 'good' : s.tone === 'bad' ? 'bad' : '')).join('')}</div></div></div>` +
    r.sections.map(sec => ({ ...sec, rows: sec.rows || [], columns: sec.columns || [] })).map(sec => `<div class="panel"><div class="hd"><h3>${esc(sec.title)}</h3><small>${sec.rows.length} row${sec.rows.length === 1 ? '' : 's'}</small></div>
      ${tbl(sec.columns.map(c => [esc(c.label), crR(c.type)]), sec.rows.map(row => `<tr>${row.map((v, i) => `<td class="${crR(sec.columns[i]?.type)}${sec.columns[i]?.type === 'text' && v.length > 40 ? ' wrap' : ''}">${crCell(sec.columns[i]?.type, v)}</td>`).join('')}</tr>`),
        { tall: sec.rows.length > 14, empty: esc(sec.empty || 'Nothing to show'), foot: sec.totals ? `<tfoot><tr>${sec.totals.map((v, i) => `<td class="${crR(sec.columns[i]?.type)}">${i === 0 ? esc(v) : crCell(sec.columns[i]?.type, v)}</td>`).join('')}</tr></tfoot>` : '' })}
      ${sec.note ? `<div class="bd" style="padding-top:8px"><span class="dim" style="font-size:12px">${esc(sec.note)}</span></div>` : ''}</div>`).join('') +
    (r.notes?.length ? `<div class="note">${r.notes.map(esc).join('<br>')}</div>` : '');
}
let crSeq = 0;
async function crRun() {
  const seq = ++crSeq, url = crURL();
  UI.crBusy = true; UI.crErr = null; const o0 = $('cr-out'); if (o0) o0.innerHTML = crOut();
  let res = null, err = null;
  try { res = await get(url); } catch (e) { err = e; }
  if (seq !== crSeq) return;
  UI.crRes = res; UI.crErr = err; UI.crBusy = false;
  const o = $('cr-out'); if (o) o.innerHTML = crOut();
}
function crSync() {
  const f = $('cr-from'), t = $('cr-to'), u = $('cr-ucc');
  if (u) UI.cr.ucc = u.value; if (f) UI.cr.from = f.value; if (t) UI.cr.to = t.value;
}

/* ---------- audit ---------- */
V.audit = {
  load: () => get('/v1/audit?limit=1000'),
  html: d => ph('Audit trail', 'Every action with the user and time. The database refuses edits and deletes to this log, and each entry is sealed to the one before it with a key the database never sees, so a changed, removed or inserted entry shows up when the seals are checked.', has('Compliance officer') ? '<button class="btn" data-act="sealCheck">Check seals</button>' : '') + (UI.seal ? sealHTML(UI.seal) : '') + `<div class="panel"><div class="hd"><input class="in" data-ui="auditQ" data-part="audit-t" placeholder="Filter by user, action or object" value="${esc(UI.auditQ)}"><small>${d.length} entries</small></div><div id="audit-t">${P['audit-t'](d)}</div></div>`
};
const fmtTS = t => t ? new Date(t).toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'medium' }) : '';
function aiUsageHTML(v) {
  const w = v.wallet || {}, low = v.enabled && num(w.balance) < num(v.per_answer_max) * 20;
  return `<div class="panel"><div class="hd"><h3>AI credit and usage</h3><small>${v.enabled ? esc(v.provider + ' · ' + v.model) : 'AI not configured on this server'}</small></div><div class="bd">
    <div class="kpis">${kpi('Credit left', inr(w.balance, 2), low ? 'Running low: ask your provider to add credit' : 'prepaid', low ? 'warn' : 'good')}${kpi('AI answers', nf(v.answers), 'in ' + v.month)}${kpi('Charged', inr(v.charged, 2), 'plus GST ' + inr(v.gst, 2))}${kpi('Most an answer can cost', inr(v.per_answer_max, 2), 'usually much less')}</div>
    <div class="tools" style="margin:10px 0"><label class="small dim" for="ai-month">Month</label><input class="in sm" type="month" id="ai-month" value="${esc(v.month)}"></div>
    ${tbl([['Time'], ['User'], ['Model'], ['Tokens', 'r'], ['Charge (₹)', 'r'], ['Credit after (₹)', 'r']], v.uses.slice(0, 50).map(u => `<tr><td class="n dim">${esc(fmtTS(u.at))}</td><td>${esc(u.actor)}</td><td class="mono small">${esc(u.model)}</td><td class="r">${nf(u.input_tokens + u.output_tokens)}</td><td class="r">${nf(num(u.charge), 2)}</td><td class="r">${nf(num(u.balance_after), 2)}</td></tr>`), { empty: 'No AI answers this month.' })}
    ${v.top_ups.length ? `<h4 style="margin:14px 0 6px">Credit added</h4>` + tbl([['Date'], ['Amount (₹)', 'r'], ['Reference']], v.top_ups.slice(0, 20).map(t => `<tr><td class="n">${esc(fmtTS(t.at))}</td><td class="r">${nf(num(t.amount), 2)}</td><td>${esc(t.ref)}</td></tr>`)) : ''}
    <p class="dim small" style="margin:8px 0 0">Each AI answer is charged from this prepaid credit by the tokens it used. Questions are not stored here. When credit runs out, Ledgy keeps answering from its help articles without AI.</p>
  </div></div>`;
}
function sealHTML(s) {
  const a = s.audit;
  const head = !s.encrypted ? `<div class="note bad">Client data is not encrypted on this server: it was started without data keys.</div>` :
    a ? (a.ok ? `<div class="note good"><b>Audit trail intact.</b> ${nf(a.sealed)} sealed ${a.sealed === 1 ? 'entry' : 'entries'} checked${a.first ? ` from ${esc(fmtTS(a.first))} to ${esc(fmtTS(a.last))}` : ''}; every seal matches and every entry links to the one before.${a.unsealed ? ` ${nf(a.unsealed)} earlier entries were written before sealing was switched on.` : ''}<br><span class="small dim">Latest seal <span class="mono">${esc((a.last_hash || '').slice(0, 26))}…</span> · record it outside the system (for example in the IT committee minutes) so removal of the newest entries can also be detected.</span></div>`
      : `<div class="note bad"><b>Audit trail problems found: ${a.problems.length}.</b> Report this to the IT committee and treat it as a security incident.</div>` + tbl([['#'], ['Time'], ['Action'], ['Object'], ['Problem']], a.problems.map(p => `<tr><td class="n">${p.seq}</td><td class="n">${esc(fmtTS(p.at))}</td><td>${esc(p.action)}</td><td class="n">${esc(p.object)}</td><td class="wrap">${esc(p.issue)}</td></tr>`))) : '';
  return `<div class="panel"><div class="hd"><h3>Data protection</h3><small>${s.encrypted ? 'Encrypted' : 'Not encrypted'}</small></div><div class="bd">${head}
    ${s.encrypted ? `<div class="kv"><div><small>Encryption</small><b>${esc(s.cipher)}</b></div><div><small>Keys</small><b>${esc(s.key_source)}</b></div><div><small>Key version in use</small><b>${s.key_version}${s.key_versions.length > 1 ? ` (also holds ${s.key_versions.filter(v => v !== s.key_version).join(', ')} for older data)` : ''}</b></div></div>
    <div class="grid2" style="margin-top:10px"><div><small class="dim">Encrypted at rest</small><ul class="small">${s.covers.map(x => `<li>${esc(x)}</li>`).join('')}</ul></div><div><small class="dim">Kept in clear (no personal data)</small><ul class="small">${s.in_clear.map(x => `<li>${esc(x)}</li>`).join('')}</ul></div></div>` : ''}
    <h4 style="margin:14px 0 6px">Where data is kept and sent ${s.india_only ? pill('India-only mode on', 'good') : pill('India-only mode off', 'nt')}</h4>
    ${tbl([['What'], ['Where'], ['In India?'], ['Note']], (s.residency || []).map(r => `<tr><td>${esc(r.what)}</td><td>${esc(r.where)}</td><td>${{ india: pill('Yes', 'good'), outside: pill('No', 'bad'), none: pill('Nothing sent', 'nt'), check: pill('Check', 'warn') }[r.status] || ''}</td><td class="wrap dim">${esc(r.note || '')}</td></tr>`))}</div></div>`;
}
P['audit-t'] = d => { const q = UI.auditQ.toLowerCase(); return tbl([['Time'], ['User'], ['Action'], ['Object'], ['Details']], d.filter(a => (a.actor + ' ' + a.action + ' ' + a.object + ' ' + JSON.stringify(a.detail || {})).toLowerCase().includes(q)).slice(0, 500).map(a => `<tr><td class="n dim">${new Date(a.at).toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'medium' })}</td><td>${esc(a.actor)}</td><td>${pill(a.action, /rejected|kill\.on|str/.test(a.action) ? 'bad' : 'acc')}</td><td class="n">${esc(a.object)}</td><td class="wrap dim">${esc(Object.entries(a.detail || {}).map(([k, v]) => k + ': ' + (typeof v === 'object' ? JSON.stringify(v) : v)).join(' · '))}</td></tr>`), { tall: true, empty: 'No actions yet' }); };

/* ---------- settings ---------- */
// Cleared through: one row per exchange segment, from the server's table of allowed clearing corporations.
function clearingPanel(d, admin) {
  const s = d.settings, segs = d.clearing || [], corps = d.clearing_corps || {}, cf = s.clearing_for || {}, nm = s.clearing_member_names || {};
  const row = c => { const v = cf[c.key] || c.default, cm = v.startsWith('CM:'), id = 'st-clr-' + c.key;
    return `<tr><td class="n">${esc(c.key)}</td><td><select class="in sm" id="${id}" data-key="${esc(c.key)}" ${admin ? '' : 'disabled'}>${c.allowed.map(a => `<option value="${a}" ${v === a ? 'selected' : ''}>${esc(a)} · ${esc(corps[a] || '')}</option>`).join('')}<option value="CM" ${cm ? 'selected' : ''}>Another clearing member (we are trading member only)</option><option value="NONE" ${v === 'NONE' ? 'selected' : ''}>Not traded</option></select></td>
      <td><input class="in sm mono" id="${id}-code" placeholder="clearing member code" value="${esc(cm ? v.slice(3) : '')}" ${admin ? '' : 'disabled'}></td><td><input class="in sm" id="${id}-name" placeholder="clearing member name" value="${esc(nm[c.key] || '')}" ${admin ? '' : 'disabled'}></td></tr>`; };
  return `<div class="panel"><div class="hd"><h3>Cleared through</h3><small>Clearing corporation of each exchange segment (SEBI interoperability)</small></div><div class="bd"><p class="dim small" style="margin-top:0">Decides which clearing-corporation files appear on the daily file checklist on Exchange files. A segment cleared by another clearing member has no clearing-corporation files: they come from that member. Every change is in the audit trail.</p>${tbl([['Segment'], ['Cleared through'], ['Clearing member code'], ['Clearing member name']], segs.map(row))}</div></div>`;
}
function clearingValues() {
  const cf = {}, nm = {};
  document.querySelectorAll('select[id^="st-clr-"]').forEach(el => { const k = el.dataset.key; let v = el.value;
    if (v === 'CM') { v = 'CM:' + ($(el.id + '-code').value || '').trim().toUpperCase(); nm[k] = ($(el.id + '-name').value || '').trim(); }
    cf[k] = v; });
  return { clearing_for: cf, clearing_member_names: nm };
}
V.settings = {
  load: async () => { const [s, users, ai, alloc] = await Promise.all([get('/v1/settings'), get('/v1/users'), has('Compliance officer') ? get(`/v1/ai/usage?month=${UI.aiMonth || ''}`).catch(() => null) : null, get(`/v1/allocation?date=${S.date}`).catch(() => null)]); return { ...s, users, ai, alloc }; },
  html: d => {
    const s = d.settings, sc = d.schedule, admin = has('Admin');
    return ph('Settings', admin ? 'Firm details, risk thresholds, calendar and users. Changes apply to new calculations; posted entries are never rewritten.' : 'Only an Admin can change settings.', admin ? '<button class="btn pri" data-act="saveSettings">Save settings</button>' : '') +
    `<div class="grid g2"><div class="panel"><div class="hd"><h3>Firm</h3></div><div class="bd"><div class="form">
      <div class="fld full"><label for="st-firm">Firm name</label><input class="in" id="st-firm" value="${esc(s.firm)}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-sebi">SEBI registration</label><input class="in" id="st-sebi" value="${esc(s.sebi_reg)}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-comp">Compliance officer email</label><input class="in" id="st-comp" value="${esc(s.compliance_email)}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-gstin">GSTIN</label><input class="in mono" id="st-gstin" value="${esc(s.firm_gstin || '')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-fpan">Firm PAN <span class="dim">(blank: from the GSTIN)</span></label><input class="in mono" id="st-fpan" value="${esc(s.firm_pan || '')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-cmpan">Clearing member PAN <span class="dim">(blank: self-clearing)</span></label><input class="in mono" id="st-cmpan" value="${esc(s.clearing_member_pan || '')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-state">State of registration</label><input class="in" id="st-state" value="${esc(s.firm_state || '')}" ${admin ? '' : 'disabled'}></div>
    </div></div></div>
    <div class="panel"><div class="hd"><h3>Risk and settlement</h3></div><div class="bd"><div class="form">
      <div class="fld"><label for="st-warn">Watch threshold (% utilisation)</label><input class="in" id="st-warn" type="number" value="${s.warn_util_pct}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-buf">RAS retention buffer (%)</label><input class="in" id="st-buf" type="number" value="${s.ras_buffer_pct}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-rasm">Next monthly RAS date</label><input class="in" id="st-rasm" type="date" value="${esc(s.ras_monthly)}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-rasq">Next quarterly RAS date</label><input class="in" id="st-rasq" type="date" value="${esc(s.ras_quarterly)}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-inm">Inactive after (months without activity)</label><input class="in" id="st-inm" type="number" min="12" max="60" value="${s.inactive_months || 24}" ${admin ? '' : 'disabled'}></div>
      <div class="fld full"><label for="st-snap">Margin snapshot times (HH:MM:SS, comma separated)</label><input class="in mono" id="st-snap" value="${esc((s.snapshot_times || []).join(', '))}" ${admin ? '' : 'disabled'}></div>
      <div class="fld full"><label for="st-hol">Extra holidays (YYYY-MM-DD, comma separated)</label><input class="in mono" id="st-hol" value="${esc((s.holidays || []).join(', '))}" ${admin ? '' : 'disabled'}><div class="dim small" style="margin-top:4px">Exchange trading and settlement holidays come from the approved exchange calendar (<button type="button" class="linkish" data-act="go" data-id="circulars:hol">Circulars & calendar → Exchange holidays</button>). Add a day here only for a closure that is not in it.</div></div>
      <label class="check full"><input type="checkbox" id="st-pen" ${s.penalty_pass_through ? 'checked' : ''} ${admin ? '' : 'disabled'}> Pass short-collection penalties through to client ledgers</label>
    </div></div></div></div>
    ${clearingPanel(d, admin)}
    <div class="grid g2"><div class="panel"><div class="hd"><h3>Debit balances and interest</h3></div><div class="bd"><div class="form">
      <div class="fld"><label for="st-int">Interest on overdue debit (% a year)</label><input class="in" id="st-int" value="${esc(s.interest_pct_pa || '18')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-grace">Grace days at the start of a debit</label><input class="in" id="st-grace" type="number" min="0" max="30" value="${s.interest_grace_days || 0}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-imin">Do not charge below (₹)</label><input class="in" id="st-imin" value="${esc(s.interest_minimum || '1.00')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-gst">GST on charges (%)</label><input class="in" id="st-gst" value="${esc(s.gst_pct || '18')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-ipost">Interest posting</label><select class="in" id="st-ipost" ${admin ? '' : 'disabled'}><option value="monthly" ${s.interest_posting !== 'daily' ? 'selected' : ''}>Monthly: simple interest at month end</option><option value="daily" ${s.interest_posting === 'daily' ? 'selected' : ''}>Daily: posted every day, earns interest</option></select></div>
      <div class="fld"><label for="st-idfrom">Daily posting starts on</label><input class="in mono" type="date" id="st-idfrom" value="${esc(s.interest_daily_from || '')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-iex">Groups not charged interest (comma separated)</label><input class="in mono" id="st-iex" value="${esc((s.interest_exempt_tags || []).join(', '))}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-dbd">Block buying after days in debit</label><input class="in" id="st-dbd" type="number" min="1" max="90" value="${s.debit_block_days || 7}" ${admin ? '' : 'disabled'}></div>
    </div></div></div>
    <div class="panel"><div class="hd"><h3>Period locking</h3></div><div class="bd"><div class="form">
      <div class="fld"><label for="st-lock">Books locked up to</label><input class="in" id="st-lock" type="date" value="${esc(s.locked_up_to || '')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-alock">Also lock automatically (business days back, 0 = off)</label><input class="in" id="st-alock" type="number" min="0" max="30" value="${s.auto_lock_days || 0}" ${admin ? '' : 'disabled'}></div>
      <p class="dim full" style="margin:0;font-size:12.5px">Nothing can be posted, reversed or rebilled on a locked date. Lock a month once it is audited; 2 business days locks T-2 automatically.</p>
    </div></div></div></div>
    <div class="panel"><div class="hd"><h3>Receipts and payouts</h3></div><div class="bd"><div class="form">
      <div class="fld"><label for="st-bnc">Cheque return charge (₹, before GST)</label><input class="in" id="st-bnc" value="${esc(s.cheque_bounce_charge || '350.00')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-pda">Client bank account payouts are made from</label><input class="in mono" id="st-pda" value="${esc(s.payout_debit_account || '')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-tds">TDS on AP commission, section 194H (%)</label><input class="in" id="st-tds" value="${esc(s.tds_commission_pct || '2')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-refp">Referral reward (% of referred clients’ brokerage)</label><input class="in" id="st-refp" value="${esc(s.referral_pct || '10')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-refm">Referral earns for (months)</label><input class="in" id="st-refm" type="number" value="${s.referral_months || 12}" ${admin ? '' : 'disabled'}></div>
      <div class="fld full"><label for="st-gws">Payment gateway webhook secret</label><input class="in mono" id="st-gws" type="password" autocomplete="off" value="${esc(s.gateway_secret || '')}" ${admin ? '' : 'disabled'}><span class="dim small">Webhook address: <span class="mono">${esc(location.origin)}/hooks/${esc(S.me.tenant)}/gateway</span>. Payments must carry the client code in notes.ucc.</span></div>
      <div class="fld full"><label for="st-was">WhatsApp webhook secret</label><input class="in mono" id="st-was" type="password" autocomplete="off" value="${esc(s.whatsapp_secret || '')}" ${admin ? '' : 'disabled'}><span class="dim small">Inbound messages: <span class="mono">${esc(location.origin)}/hooks/${esc(S.me.tenant)}/whatsapp</span>, signed with HMAC-SHA256 in X-Signature.</span></div>
      <div class="fld full"><label for="st-mgmt">Management emails (job results and alerts)</label><input class="in" id="st-mgmt" value="${esc((s.management_emails || []).join(', '))}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-linc">Incentive per converted lead (₹)</label><input class="in mono" id="st-linc" value="${esc(s.lead_incentive || '500')}" ${admin ? '' : 'disabled'}></div>
    </div></div></div>
    <div class="panel"><div class="hd"><h3>NSE Clearing allocation</h3><small>Client collateral allocation through NSE Clearing’s API</small></div><div class="bd"><div class="form">
      <div class="fld full"><label>Consumer key and secret</label><div>${!d.alloc ? '' : d.alloc.connection === 'Not set' ? pill('Not set', 'bad') : pill(d.alloc.connection, d.alloc.connection === 'Problem' ? 'bad' : 'good')} <button class="btn sm" type="button" data-act="go" data-id="connections">${d.alloc && d.alloc.connection !== 'Not set' ? 'Change in Connections' : 'Enter in Connections'}</button></div><div class="dim small" style="margin-top:4px">NSE Clearing emails a consumer key and secret after the clearing member registers (email collaterals_ops@nsccl.co.in with the member code, name, email, phone and this server’s IP addresses). They are kept encrypted in Connections → NSE Clearing allocation API and never shown again.</div></div>
      <label class="check full"><input type="checkbox" id="st-alive" ${s.alloc_api_live ? 'checked' : ''} ${admin ? '' : 'disabled'}> Send allocation requests to NSE Clearing (there is no test system: the first request is live)</label>
      <label class="check full"><input type="checkbox" id="st-apush" ${s.alloc_auto_push ? 'checked' : ''} ${admin ? '' : 'disabled'}> Allocate money that comes in during the day as soon as it is credited</label>
      <div class="fld"><label for="st-aseg">Allocate money in to</label><select class="in" id="st-aseg" ${admin ? '' : 'disabled'}>${['CM', 'FO', 'CD'].map(x => `<option ${(s.alloc_push_segment || 'CM') === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div>
    </div></div></div>
    <div class="panel"><div class="hd"><h3>Sign-in security</h3></div><div class="bd"><div class="form">
      <div class="fld"><label for="st-2fa">Two-factor sign-in</label><select class="in" id="st-2fa" ${admin ? '' : 'disabled'}><option value="required" ${s.two_factor !== 'optional' ? 'selected' : ''}>Required for every user</option><option value="optional" ${s.two_factor === 'optional' ? 'selected' : ''}>Optional</option></select></div>
      <div class="fld"><label for="st-idle">Sign out after inactivity (minutes)</label><input class="in" id="st-idle" type="number" min="5" max="240" value="${s.session_idle_minutes || 30}" ${admin ? '' : 'disabled'}></div>
      <p class="dim full" style="margin:0;font-size:12.5px">Passwords: at least 12 characters, stored as PBKDF2-SHA256 hashes. Five wrong attempts lock a user for 15 minutes. Sessions also end after 12 hours, at sign-out, and when an administrator resets the user.</p>
    </div></div></div>
    <div class="panel"><div class="hd"><h3>Edition</h3><small>Set by your Ledgerline provider</small></div><div class="bd">${editionNote()}${S.me?.dev && admin ? `<div class="tools" style="margin-top:10px"><span class="dim small">Development server: try another edition</span>${(S.me.edition?.editions || []).map(e => `<button class="btn sm ${e.id === edition() ? 'pri' : ''}" data-act="devEdition" data-id="${e.id}">${esc(e.name)}</button>`).join('')}</div>` : ''}</div></div>
    <div class="panel"><div class="hd"><h3>Ledgy (help)</h3></div><div class="bd">
      <label class="check"><input type="checkbox" id="st-lai" ${s.ledgy_ai ? 'checked' : ''} ${admin && S.me?.ledgy_ai ? '' : 'disabled'}> Let Ledgy write answers with AI (charged from AI credit)</label>
      <p class="dim" style="margin:6px 0 0;font-size:12.5px">${S.me?.ledgy_ai ? 'Ledgy answers from its help articles either way. With this on, the question (with PAN, numbers and emails masked) and the matching articles are sent to the AI provider to phrase the answer; client data never is. Leave it off if your policy keeps all processing in India.' : 'Not available: no AI provider is connected on this server (LEDGERLINE_ANTHROPIC_KEY, LEDGERLINE_OPENAI_KEY or LEDGERLINE_GEMINI_KEY). Ledgy answers from its help articles.'}</p>
      <label class="check" style="margin-top:10px"><input type="checkbox" id="st-bai" ${s.bill_ai ? 'checked' : ''} ${admin && S.me?.bill_ai ? '' : 'disabled'}> Read scanned bills with AI (charged from AI credit)</label>
      <p class="dim" style="margin:6px 0 0;font-size:12.5px">${S.me?.bill_ai ? 'Vendor bills made as PDFs are read on this server either way. With this on, a scanned or photographed bill can be sent to the AI provider to read its details, when someone presses Read with AI. Vendor bills are the firm’s own purchases, not client data; leave it off if your policy keeps all processing in India.' : 'Not available: no AI provider is connected on this server, or the server runs in India-only mode. PDF bills are still read on the server.'}</p>
    </div></div>
    ${d.ai ? aiUsageHTML(d.ai) : ''}
    <div class="panel"><div class="hd"><h3>Users and roles</h3>${admin ? '<button class="btn sm pri" data-act="addUser">Add user</button>' : '<small>Maker-checker: the person who creates an override, application or entry cannot approve it</small>'}</div>${tbl([['User'], ['Role'], ['Sign-in'], ['Last sign-in'], ['Status'], ['']], d.users.map(u => `<tr><td><b style="font-weight:600">${esc(u.name)}</b><div class="mono dim" style="font-size:11.5px">${esc(u.id)}</div></td><td>${admin && u.id !== S.me.actor ? `<select class="in sm" data-urole="${esc(u.id)}">${(S.me.roles || []).map(r => `<option ${r === u.role ? 'selected' : ''}>${esc(r)}</option>`).join('')}</select>` : esc(u.role)}${u.branch ? `<div class="dim small">branch ${esc(u.branch)}</div>` : ''}${u.partner ? `<div class="dim small">partner ${esc(u.partner)}</div>` : ''}</td>
      <td>${u.has_password ? '' : pill('No password', 'warn') + ' '}${u.totp_enabled ? pill('2FA on', 'good') : pill('2FA off', 'nt')}${u.must_change_password ? ' ' + pill('Temp password', 'warn') : ''}${u.locked ? ' ' + pill('Locked', 'bad') : ''}</td><td class="n dim">${u.last_login ? new Date(u.last_login).toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'short' }) : '—'}</td>
      <td>${u.active ? pill('Active', 'good') : pill('Inactive', 'nt')}</td><td>${admin && u.id !== S.me.actor ? `<div class="split"><button class="btn sm" data-act="resetPw" data-id="${esc(u.id)}">Reset password</button>${u.totp_enabled ? `<button class="btn sm" data-act="reset2fa" data-id="${esc(u.id)}">Reset 2FA</button>` : ''}<button class="btn sm ${u.active ? 'bad' : ''}" data-act="toggleUser" data-id="${esc(u.id)}">${u.active ? 'Deactivate' : 'Activate'}</button></div>` : ''}</td></tr>`))}</div>
    <div class="panel"><div class="hd"><h3>Charge rate schedule</h3><small>${esc(sc.version)}${sc.sample ? ' · ' + pill('Sample rates', 'warn') : ''}</small></div>${sc.sample ? `<div class="bd" style="padding-bottom:0"><div class="note warn">${esc(sc.note)}</div></div>` : ''}${tbl([['Product'], ['Exchange'], ['From'], ['Brokerage'], ['STT buy %', 'r'], ['STT sell %', 'r'], ['Txn %', 'r'], ['Stamp buy %', 'r']], sc.rules.map(r => `<tr><td class="n">${r.bucket}</td><td>${r.exchange}</td><td class="n">${r.effective_from}</td><td>${r.brokerage_mode === 'none' ? 'Nil' : r.brokerage_mode === 'flat' ? '₹' + r.brokerage_flat + ' per order' : r.brokerage_mode === 'pct' ? r.brokerage_pct + '%' : `lower of ₹${r.brokerage_flat} or ${r.brokerage_pct}%`}</td><td class="r">${r.stt_buy_pct}</td><td class="r">${r.stt_sell_pct}</td><td class="r">${r.txn_pct}</td><td class="r">${r.stamp_buy_pct}</td></tr>`))}<div class="bd"><span class="dim" style="font-size:12px">GST ${sc.gst_pct}% · SEBI fee ${sc.sebi_fee_pct}% · The schedule is a versioned file on the server (config/charges); a new circular means a new version with an effective date.</span></div></div>`;
  }
};

/* ---------- legacy debts ---------- */
V.legacy = {
  load: async () => {
    const [v, cand] = await Promise.all([get(`/v1/legacy-debts?date=${S.date}`), UI.lgTab === 'find' && !scopedUser() ? get(`/v1/legacy-debts/candidates?date=${S.date}&days=${UI.lgDays}`) : Promise.resolve(null)]);
    return { v, cand };
  },
  html: d => {
    const v = d.v;
    return ph('Legacy debts', 'Old client debits kept apart from the daily debit work: no interest is charged on them, they are left out of debit reminders and ageing, and the client stays blocked for buying and payouts. An inactive account with one cannot be reactivated until it is paid or written off. One person proposes a debit as legacy and another approves it (debits marked Legacy in the opening balances arrive as proposals). Record each recovery step here; a checker writes off what will not be recovered, to bad debts.') +
    `<div class="kpis">${kpi('Open', v.open, inr(v.owed) + ' owed', v.open ? 'warn' : 'good')}${kpi('Recovered', inr(v.recovered), 'paid since marked', num(v.recovered) ? 'good' : '')}${kpi('Written off', inr(v.written_off), 'to bad debts')}${kpi('Waiting for approval', v.proposed, 'proposed as legacy', v.proposed ? 'warn' : '')}</div>
    ${v.open ? `<div class="panel"><div class="hd"><h3>Open legacy debts by size</h3><small>large ones first: most of the money is usually in a few accounts</small></div>${tbl([['Size'], ['Accounts', 'r'], ['Owed', 'r']], v.bands.map(b => `<tr><td>${esc(b.label)}</td><td class="r">${b.count}</td><td class="r">${nf(b.amount, 2)}</td></tr>`))}</div>` : ''}
    <div class="panel"><div class="hd"><div class="tools">${segBtns('lgTab', [['open', `Open · ${v.open}`], ['prop', `Proposed · ${v.proposed}`], ['closed', 'Cleared and closed'], ...(scopedUser() ? [] : [['find', 'Find frozen debits']])])}<input class="in" data-ui="lgQ" data-part="lg-t" placeholder="Filter name or UCC" value="${esc(UI.lgQ)}"></div></div><div id="lg-t">${P['lg-t'](d)}</div></div>`;
  }
};
P['lg-t'] = d => {
  const v = d.v, q = UI.lgQ.trim().toLowerCase(), f = r => !q || (r.ucc + ' ' + r.name).toLowerCase().includes(q);
  const who = r => `<button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}`;
  const sel = r => `<input type="checkbox" data-act="lgSel" data-id="${esc(r.ucc)}" ${UI.lgSel.has(r.ucc) ? 'checked' : ''} aria-label="Select ${esc(r.ucc)}">`;
  const last = r => { const n = (r.notes || []).filter(x => !['Proposed', 'Approved'].includes(x.kind)).slice(-1)[0]; return n ? `${esc(n.kind)} <span class="dim">${fmtDate(n.date)}</span>` : '<span class="dim">—</span>'; };
  const work = has('Ops maker', 'Ops checker', 'Risk manager', 'Compliance officer'), chk = has('Ops checker'), dec = has('Ops checker', 'Compliance officer');
  if (UI.lgTab === 'find') {
    const c = (d.cand || []).filter(f);
    return `<div class="pad tools"><label class="dim small" for="lg-days">Debit not moved for at least</label><select class="in sm" id="lg-days">${[90, 180, 365, 730].map(n => `<option value="${n}" ${UI.lgDays === n ? 'selected' : ''}>${n} days</option>`).join('')}</select>${work ? `<button class="btn pri sm" data-act="lgPropose" ${UI.lgSel.size ? '' : 'disabled'}>Propose selected as legacy (${UI.lgSel.size})</button>` : ''}<span class="dim small">${c.length} clients · ${inr(sum(c, x => num(x.debit)))}</span></div>` +
      tbl([[`<input type="checkbox" data-act="lgSelAll" aria-label="Select all shown">`], ['Client'], ['Debit', 'r'], ['In debit since'], ['Last movement'], ['Idle days', 'r'], ['Account']], c.map(r => `<tr><td>${sel(r)}</td><td>${who(r)}</td><td class="r">${nf(r.debit, 2)}</td><td class="n">${fmtDate(r.since)}</td><td class="n">${fmtDate(r.last_move)}</td><td class="r">${r.idle_days}</td><td class="small">${esc(r.client_status || '')}${r.inactive ? ' · inactive' : ''}</td></tr>`), { tall: true, empty: 'No client debit has been idle that long.' });
  }
  if (UI.lgTab === 'prop') {
    const rs = v.rows.filter(r => r.state === 'Proposed').filter(f);
    return (dec ? `<div class="pad tools"><button class="btn pri sm" data-act="lgDecide" data-id="1" ${UI.lgSel.size ? '' : 'disabled'}>Approve selected (${UI.lgSel.size})</button><button class="btn sm" data-act="lgDecide" data-id="0" ${UI.lgSel.size ? '' : 'disabled'}>Reject selected</button><span class="dim small">The person who proposed a debit cannot approve it.</span></div>` : '') +
      tbl([[`<input type="checkbox" data-act="lgSelAll" aria-label="Select all shown">`], ['Client'], ['Owed now', 'r'], ['Debit when proposed', 'r'], ['In debit since'], ['Why'], ['Proposed by']], rs.map(r => `<tr><td>${sel(r)}</td><td>${who(r)}</td><td class="r">${nf(r.debit, 2)}</td><td class="r">${nf(r.amount, 2)}</td><td class="n">${r.debit_since ? fmtDate(r.debit_since) : '—'}</td><td class="wrap small">${esc(r.reason)}</td><td class="small">${esc(r.proposed_by)} <span class="dim">${fmtDate(r.proposed_on)}</span></td></tr>`), { tall: true, empty: 'Nothing waiting for approval.' });
  }
  if (UI.lgTab === 'closed') {
    const rs = v.rows.filter(r => !['Open', 'Proposed'].includes(r.state)).filter(f);
    return tbl([['Client'], ['State'], ['When marked', 'r'], ['Recovered', 'r'], ['Written off', 'r'], ['History']], rs.map(r => `<tr><td>${who(r)}</td><td>${pill(r.state, r.state === 'Cleared' ? 'good' : 'nt')}</td><td class="r">${nf(r.amount, 2)}</td><td class="r">${nf(r.recovered, 2)}</td><td class="r">${nf(r.written_off, 2)}</td><td><button class="btn sm" data-act="lgHist" data-id="${esc(r.ucc)}">History</button></td></tr>`), { tall: true, empty: 'None yet.' });
  }
  const rs = v.rows.filter(r => r.state === 'Open').filter(f);
  return (work ? `<div class="pad tools"><button class="btn sm" data-act="lgRemind" ${rs.length ? '' : 'disabled'}>Send reminder to ${UI.lgSel.size ? `selected (${UI.lgSel.size})` : 'all open'}</button><span class="dim small">Reminders go through Client messages (template “legacy_debt.reminder”). A balance confirmation from the client acknowledges the debt: record it with <b>Note</b>.</span></div>` : '') +
    tbl([[`<input type="checkbox" data-act="lgSelAll" aria-label="Select all shown">`], ['Client'], ['Owed now', 'r'], ['When marked', 'r'], ['Recovered', 'r'], ['In debit since'], ['Last payment or confirmation'], ['3 years from that'], ['Last step'], ['']],
      rs.map(r => `<tr><td>${sel(r)}</td><td>${who(r)}</td><td class="r">${nf(r.debit, 2)}</td><td class="r">${nf(r.amount, 2)}</td><td class="r">${num(r.recovered) ? nf(r.recovered, 2) : '—'}</td><td class="n">${r.debit_since ? fmtDate(r.debit_since) : '—'}</td><td class="n">${r.last_ack ? fmtDate(r.last_ack) : '—'}</td><td class="n ${r.three_years && r.three_years < S.date ? 'neg' : ''}">${r.three_years ? fmtDate(r.three_years) : '—'}</td><td class="small">${last(r)}</td><td><div class="split">${work ? `<button class="btn sm" data-act="lgNote" data-id="${esc(r.ucc)}">Note</button>` : ''}${chk ? `<button class="btn sm" data-act="lgWriteOff" data-id="${esc(r.ucc)}">Write off</button><button class="btn sm" data-act="lgUnmark" data-id="${esc(r.ucc)}">Unmark</button>` : ''}<button class="btn sm" data-act="lgHist" data-id="${esc(r.ucc)}">History</button></div></td></tr>`),
      { tall: true, empty: 'No open legacy debts.' }) + '<div class="pad"><span class="dim small">“3 years from that” is a reminder to have a lawyer look at the larger debts before then: recovery claims are generally time-barred three years after the money fell due unless the client has since paid or acknowledged the debt. It is not legal advice.</span></div>';
};

/* ============================== modals ============================== */
function modal(html, drawer) { $('mroot').innerHTML = `<div class="ov ${drawer ? 'drawer' : ''}" data-act="ovclose"><div class="md" role="dialog" aria-modal="true">${html}</div></div>`; const f = !drawer && document.querySelector('.md textarea,.md input:not([type=checkbox]),.md select'); if (f) setTimeout(() => f.focus(), 10); }
let modalLocked = false;
function closeModal() { modalLocked = false; $('mroot').innerHTML = ''; }
function mhd(t, s) { return `<div class="hd"><div><h2>${t}</h2>${s ? `<p>${s}</p>` : ''}</div><button class="x" data-act="close" aria-label="Close">×</button></div>`; }
function confirmModal(title, sub, body, okLabel, onOk, danger) {
  modal(mhd(title, sub) + `<div class="bd">${body || ''}<div id="cm-err"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn ${danger ? 'bad' : 'pri'}" id="cm-ok">${okLabel}</button></div>`);
  $('cm-ok').onclick = async () => { $('cm-ok').disabled = true; try { await onOk(); } catch (e) { $('cm-err').innerHTML = errHTML(e); $('cm-ok').disabled = false; } };
}

function showTempPw(user, pw, what) {
  modal(mhd(`User ${what}`, `Give ${user} this one-time password through a separate channel. It is shown only once; they must change it at first sign-in.`) +
    `<div class="bd"><div class="secret mono">${esc(pw)}</div></div><div class="ft"><button class="btn pri" data-act="tempDone">I have noted it</button></div>`);
  setTimeout(() => { modalLocked = true; });
}

/* ============================== actions ============================== */
async function act(fn, okMsg, reload = true) {
  try { const r = await fn(); if (okMsg) toast(typeof okMsg === 'function' ? okMsg(r) : okMsg); closeModal(); if (reload) await render({ keep: true }); return r; }
  catch (e) { toast(e.message, true); throw e; }
}
const A = {
  apRun: () => act(() => post('/v1/autopilot/run', { date: S.date, stage: $('ap-stage').value }), r => { const d = sum(r.runs, x => x.done), q = sum(r.runs, x => x.queued); return `${r.runs.length} automations: ${d} done, ${q} for people`; }),
  apRunOne: id => act(() => post('/v1/autopilot/run', { date: S.date, only: id }), r => r.runs[0] ? `${r.runs[0].done} done, ${r.runs[0].queued} for people${r.runs[0].error ? ' · ' + r.runs[0].error : ''}` : 'Off'),
  apSchedule: (id, el) => act(() => post('/v1/autopilot/schedule', { on: el.dataset.on === '1' }), r => r.on ? 'Autopilot runs on its own on business days' : 'Schedule switched off'),
  apDay: async (id, el) => { el.disabled = true; toast('Loading the next sample day and running it on autopilot…'); try { const r = await post('/v1/dev/sample', { autopilot: true }); const d = sum(r.runs, x => x.done), q = sum(r.runs, x => x.queued); S.date = r.date; toast(`${fmtDate(r.date)}: ${d} actions done automatically, ${q} items for people`); UI.apTab = 'work'; render(); } catch (e) { toast(e.message, true); el.disabled = false; } },
  apSave: async () => { const pol = {}; document.querySelectorAll('[data-appol]').forEach(x => { pol[x.dataset.appol] = { id: x.dataset.appol, mode: x.value, params: {} }; }); document.querySelectorAll('[data-apparam]').forEach(x => { (pol[x.dataset.apparam] ||= { id: x.dataset.apparam, params: {} }).params[x.dataset.k] = x.value.trim(); });
    const cur = Object.fromEntries(C.autopilot.policies.map(p => [p.id, p])); let n = 0;
    try { for (const p of Object.values(pol)) { const c = cur[p.id]; if (c.mode === p.mode && JSON.stringify(c.settings || {}) === JSON.stringify({ ...(c.settings || {}), ...p.params })) continue; await post('/v1/autopilot/policies', { id: p.id, mode: p.mode || c.mode, params: p.params }); n++; } toast(n ? `${n} automation${n > 1 ? 's' : ''} changed` : 'Nothing changed'); render({ keep: true }); } catch (e) { toast(e.message, true); } },
  wDo: id => act(() => post(`/v1/work/${id}`, { do: true }), w => 'Done: ' + w.title),
  wDismiss: id => confirmModal('Close item', 'Say what was done, so the record is complete.', '<div class="fld"><label for="wd-n">Note</label><input class="in" id="wd-n" placeholder="e.g. Resolved with the client by phone"></div>', 'Close', () => act(() => post(`/v1/work/${id}`, { do: false, note: $('wd-n').value }), 'Closed')),
  inbox: async () => { const fs = $('inbox-files').files; if (!fs.length) { toast('Choose files first'); return; } const fd = new FormData(); [...fs].forEach(f => fd.append('files', f)); try { const r = await api('POST', `/v1/inbox?date=${S.date}`, fd, true); UI.inboxRes = r.files; toast(`${r.files.filter(f => f.status === 'Loaded').length} of ${r.files.length} files loaded`); render({ keep: true }); } catch (e) { toast(e.message, true); } },
  pVerify: id => { modal(mhd('Enter the code', 'We sent a 6-digit code to your new contact. It is valid for 15 minutes.') + `<form class="bd" id="otpf"><div class="fld"><label for="otp-c">Code</label><input class="in mono" id="otp-c" inputmode="numeric" maxlength="6" required>${UI.devCodes?.[id] ? `<span class="dim small">Development server: the code is ${UI.devCodes[id]}</span>` : ''}</div><div id="otp-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Later</button><button class="btn pri" type="submit">Confirm</button></div></form>`);
    $('otpf').onsubmit = async e => { e.preventDefault(); try { const r = await post(`/v1/changes/${id}/verify`, { code: $('otp-c').value }); closeModal(); toast(r.status === 'Approved' ? 'Verified and updated' : 'Verified; head office will confirm'); render({ keep: true }); } catch (err) { $('otp-err').innerHTML = errHTML(err); } }; },
  nriSet: id => nriModal(id),
  nriPeriod: () => { UI.nriFrom = $('nri-from').value; UI.nriTo = $('nri-to').value; render({ keep: true }); },
  liveAt: () => { UI.liveAt = $('live-at').value; render({ keep: true }); },
  liveNow: () => { UI.liveAt = ''; render({ keep: true }); },
  mtfEnrol: () => clientCache().then(() => { modal(mhd('Enrol a client for MTF', 'Record the signed MTF agreement (rights and obligations) and the funding limit.') + `<form class="bd" id="mef"><div class="form"><div class="fld full"><label for="me-u-q">Client</label>${cpick('me-u', '', {})}</div><div class="fld"><label for="me-d">Agreement signed on</label><input class="in" type="date" id="me-d" value="${S.date}"></div><div class="fld"><label for="me-l">Funding limit (₹)</label><input class="in mono" id="me-l" value="500000"></div><div class="fld"><label for="me-r">Interest % a year (blank: firm rate)</label><input class="in mono" id="me-r"></div></div><div id="me-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Enrol</button></div></form>`);
    $('mef').onsubmit = async e => { e.preventDefault(); try { await post('/v1/mtf/enrol', { ucc: $('me-u').value, agreed_on: $('me-d').value, limit: $('me-l').value, rate_pa: $('me-r').value || '0' }); closeModal(); toast('Enrolled'); UI.mtfTab = 'fund'; render({ keep: true }); } catch (err) { $('me-err').innerHTML = errHTML(err); } }; }),
  mtfFund: (id, el) => confirmModal('Fund under MTF · ' + id, 'The client keeps the initial margin; the rest is funded from the firm’s own bank account and becomes an MTF loan. The shares must be pledged to the firm by T+1.', `<div class="fld"><label for="mf-q">Quantity to fund</label><input class="in mono" id="mf-q" value="${el.dataset.q}"></div>`, 'Fund', () => act(() => post('/v1/mtf/fund', { date: S.date, ucc: id, isin: el.dataset.isin, qty: +$('mf-q').value }), p => `${p.id}: ₹${nf(p.funded, 2)} funded`)),
  mtfPledge: id => confirmModal('MTF pledge · ' + id, '', '<div class="fld"><label for="mpl">Pledge sequence number (PSN)</label><input class="in mono" id="mpl"></div>', 'Record', () => act(() => post(`/v1/mtf/${id}/pledge`, { ref: $('mpl').value }), id + ' pledged')),
  mtfClose: (id, el) => confirmModal('Repay / close ' + id, 'Repays the funding from the client’s ledger credit (after they add funds or sell the shares) and posts interest to date.', `<div class="form"><div class="fld"><label for="mc-q">Quantity</label><input class="in mono" id="mc-q" value="${el.dataset.q}"></div><div class="fld"><label for="mc-h">How</label><select class="in" id="mc-h"><option value="client paid">Client paid in full</option><option value="shares sold">Shares sold</option><option value="liquidated">Liquidated by the firm</option></select></div></div>`, 'Repay', () => act(() => post(`/v1/mtf/${id}/close`, { date: S.date, qty: +$('mc-q').value, how: $('mc-h').value }), p => `${p.id}: ${p.status.toLowerCase()}`)),
  mtfCalls: () => act(() => post('/v1/mtf/calls', { date: S.date }), r => `${r.raised} margin call${r.raised === 1 ? '' : 's'} raised and sent`),
  mtfInterest: () => act(() => post('/v1/mtf/interest', { date: S.date }), r => `Interest of ₹${nf(r.posted, 2)} posted`),
  mtfPolicy: () => { const v = id => $('mp-' + id).value.trim(); act(() => post('/v1/mtf/config', { rate_pa: v('rate'), min_margin_pct: v('min'), maintain_pct: v('maint'), call_days: +v('days'), net_worth: v('nw'), borrowed: v('bor'), max_times_net_worth: +v('times') }), 'Policy saved'); },
  dpDIS: () => disModal(),
  dpReq: () => dpReqModal(),
  dpBook: () => clientCache().then(() => { modal(mhd('Issue a DIS book', 'Slips are numbered on from the last book issued.') + `<div class="bd"><div class="form"><div class="fld full"><label for="bk-u-q">Client</label>${cpick('bk-u', '', {})}</div><div class="fld"><label for="bk-n">Slips</label><input class="in" id="bk-n" value="20"></div></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="bk-go">Issue</button></div>`); $('bk-go').onclick = () => act(() => post('/v1/dp/books', { ucc: $('bk-u').value, slips: +$('bk-n').value }), b => `Book ${b.id}: slips ${b.from} to ${b.to} issued to ${b.ucc}`); }),
  dpVerify: (id, el) => { const ok = el.dataset.a === '1', hv = el.dataset.hv === '1';
    if (ok && !hv) { act(() => post(`/v1/dp/dis/${id}/verify`, { approve: true }), id + ' verified'); return; }
    confirmModal(ok ? 'Verify ' + id : 'Reject ' + id, ok ? 'Above ₹5 lakh: record how the client confirmed the instruction.' : 'The reason is kept with the slip.', `<div class="fld"><label for="dv-t">${ok ? 'Client confirmation (call reference, email)' : 'Reason'}</label><input class="in" id="dv-t"></div>`, ok ? 'Verify' : 'Reject', () => act(() => post(`/v1/dp/dis/${id}/verify`, ok ? { approve: true, confirmation: $('dv-t').value } : { approve: false, note: $('dv-t').value }), id + (ok ? ' verified' : ' rejected')), !ok); },
  dpExec: (id, el) => { const ok = el.dataset.ok === '1'; if (ok) { act(() => post(`/v1/dp/dis/${id}/execute`, { ok: true, date: S.date }), id + ' executed; holdings updated'); return; } confirmModal('Failed · ' + id, '', '<div class="fld"><label for="dx-r">Rejection reason from the DP system</label><input class="in" id="dx-r"></div>', 'Mark failed', () => act(() => post(`/v1/dp/dis/${id}/execute`, { ok: false, reason: $('dx-r').value }), id + ' marked failed'), true); },
  dpBatch: async () => { try { const r = await post('/v1/dp/dis/batch', { date: S.date }); const url = URL.createObjectURL(new Blob([r.file], { type: 'text/csv' })); const a = document.createElement('a'); a.href = url; a.download = `dis_upload_${S.date}.csv`; document.body.appendChild(a); a.click(); a.remove(); toast(`${r.instructions} instruction${r.instructions > 1 ? 's' : ''} in the upload batch`); render({ keep: true }); } catch (e) { toast(e.message, true); } },
  dpAct: (id, el) => { const a = el.dataset.a; if (a === 'reject') { confirmModal('Reject ' + id, '', '<div class="fld"><label for="da-r">Reason</label><input class="in" id="da-r"></div>', 'Reject', () => act(() => post(`/v1/dp/requests/${id}`, { action: 'reject', note: $('da-r').value }), id + ' rejected'), true); return; } act(() => post(`/v1/dp/requests/${id}`, { action: a }), r => `${r.id}: ${r.status.toLowerCase()}`); },
  bsdaRun: () => act(() => post('/v1/dp/bsda-reviews', { as_of: $('bsda-asof').value }), r => `${r.lines.length} change${r.lines.length === 1 ? '' : 's'} proposed for ${r.id}`),
  bsdaApply: id => { if (window.confirm('Apply the BSDA review: change the account types, keep the depository file and tell the clients?')) act(() => post(`/v1/dp/bsda-reviews/${id}/apply`), r => `${r.lines.length} accounts changed; the modification file is ready`); },
  dpBSDA: (id, el) => act(() => post(`/v1/dp/accounts/${id}/bsda`, { on: el.dataset.on === '1', date: S.date }), b => `${b.ucc} is now ${b.bsda ? 'a BSDA' : 'a regular account'}`),
  dpTariff: () => { const v = id => $('dt-' + id).value.trim(); act(() => post('/v1/dp/tariff', { amc_year: v('amc'), bsda_nil_upto: v('bsda0'), bsda_limit: v('bsdal'), bsda_amc_year: v('bsdaamc'), debit_charge: v('debit'), depository_share: v('share'), pledge_charge: v('pledge'), demat_per_cert: v('demat'), offmarket_pct: v('om'), offmarket_min: v('ommin'), credit_charge: v('credit') || '0', charge_free_cms: v('cms').split(/[\s,]+/).filter(Boolean), charge_free: v('free').split('\n').map(l => l.trim()).filter(Boolean).map(l => { const p = l.split(/\s+/), dep = /^(CDSL|NSDL)$/i.test(p[0]) ? p.shift() : ''; let id = p.shift() || ''; if (/^IN\d{6}$/i.test(id) && /^\d{8}$/.test(p[0] || '')) id += p.shift(); return { depository: dep, id, label: p.join(' ') }; }), from: v('from') }), 'Tariff saved'); },
  dpBill: () => { const m = $('dp-month').value; confirmModal('DP billing for ' + m, 'Posts each client’s DP charges for the month to their trading ledger with GST and an invoice. A month is billed once, so run it after the month ends.', '', 'Run billing', () => act(() => post('/v1/dp/billing', { month: m }), r => `${r.bills.length} DP bills posted`)); },
  dpTxns: async id => { try { const ts = await get(`/v1/dp/transactions?ucc=${id}`); modal(mhd('DP transactions · ' + id, '') + `<div class="bd">${tbl([['Date'], ['Scrip'], ['Kind'], ['Qty', 'r'], ['Reference']], ts.map(t => `<tr><td class="n">${fmtDate(t.date)}</td><td>${esc(t.symbol || t.isin)}</td><td>${esc(t.kind)}</td><td class="r ${sgn(t.qty)}">${nf(t.qty)}</td><td class="n">${esc(t.ref)}</td></tr>`), { empty: 'No DP transactions recorded here yet (pay-in and pay-out movements are in Settlement).' })}</div>`, true); } catch (e) { toast(e.message, true); } },
  editTpl: id => tplModal(id),
  testTpl: id => clientCache().then(() => { modal(mhd('Test template ' + id, 'Sends it once to a client with sample values, through the same delivery rules.') + `<div class="bd"><div class="fld"><label for="tt-u-q">Client</label>${cpick('tt-u', '', {})}</div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="tt-go">Send test</button></div>`); $('tt-go').onclick = () => act(() => post(`/v1/templates/${id}/test`, { ucc: $('tt-u').value }), r => `${r[0].channel} to ${r[0].to}: ${r[0].status}${r[0].note ? ' (' + r[0].note + ')' : ''}`); }),
  bulkMsg: () => bulkModal(),
  waSend: async () => { try { const r = await post('/v1/whatsapp/simulate', { from: $('wa-from').value, text: $('wa-text').value, date: S.date }); $('wa-out').innerHTML = `<div class="msg"><small class="dim">Bot</small><div>${esc(r.reply)}</div></div>`; const from = $('wa-from').value; C.messages.wa = await get('/v1/whatsapp/chats'); $('msg-t').innerHTML = P['msg-t'](C.messages); $('wa-from').value = from; $('wa-out').innerHTML = `<div class="msg"><small class="dim">Bot</small><div>${esc(r.reply)}</div></div>`; } catch (e) { toast(e.message, true); } },
  editJob: id => jobModal(id),
  runJob: id => act(() => post(`/v1/jobs/${id}/run`, { date: S.date }), r => `${r.name}: ${r.status.toLowerCase()} · ${r.summary}`),
  pWhatsApp: (id, el) => act(() => post('/v1/portal/whatsapp', { ucc: UI.pAcct, on: el.dataset.on === '1' }), r => r.whatsapp ? 'WhatsApp alerts on' : 'WhatsApp alerts off'),
  pAcct: (id, el) => { UI.pAcct = el.value; render(); },
  pNote: id => { const [d, u, p] = id.split('|'); openDoc(`/v1/notes/${d}/${u}/document${p ? '?part=' + p : ''}`); },
  pChange: id => { UI.chNoms = C[S.view]?.account?.nominees || []; changeModal(id, UI.pAcct); },
  pTicket: () => ticketModal(false),
  sTicket: () => ticketModal(true),
  tOpen: id => ticketDrawer(id),
  tReply: async (id, el) => { const t = $('tr-' + id).value; if (!t.trim()) { toast('Write something first'); return; } try { await post(`/v1/tickets/${id}`, { text: t, internal: el.dataset.int === '1' }); toast(el.dataset.int === '1' ? 'Note saved' : 'Reply sent'); closeModal(); render({ keep: true }); } catch (e) { toast(e.message, true); } },
  tResolve: async id => { const t = $('tr-' + id).value; try { await post(`/v1/tickets/${id}`, { text: t, status: 'Resolved' }); toast('Resolved'); closeModal(); render({ keep: true }); } catch (e) { toast(e.message, true); } },
  tClose: id => act(() => post(`/v1/tickets/${id}`, { status: 'Closed' }), 'Closed. Thank you.'),
  tSet: id => act(() => post(`/v1/tickets/${id}`, { status: $('td-st').value, assignee: $('td-as').value }), 'Updated'),
  hdMonth: () => { UI.hdMonth = $('hd-m').value; render({ keep: true }); },
  chDecide: (id, el) => { const a = el.dataset.a === '1'; if (a) { act(() => post(`/v1/changes/${id}/decide`, { approve: true }), id + ' approved and applied'); return; } confirmModal('Reject ' + id, 'The client sees the reason.', '<div class="fld"><label for="cr-r">Reason</label><input class="in" id="cr-r"></div>', 'Reject', () => act(() => post(`/v1/changes/${id}/decide`, { approve: false, reason: $('cr-r').value }), id + ' rejected'), true); },
  exDel: id => act(() => post(`/v1/external-trades/${UI.pAcct}/${id}/delete`), 'Removed'),
  newLead: () => leadModal(),
  leadOpen: id => leadDrawer(id),
  leadCall: id => leadAct(id, { kind: 'call', outcome: 'Connected' }, 'Call logged'),
  leadNote: id => leadAct(id, { kind: 'note', text: $('lead-note').value, next_action: $('lead-next').value }, 'Saved'),
  leadStage: id => { const s = $('lead-stage').value; if (s === 'Lost') { const r = prompt('Why was the lead lost?'); if (!r) return; leadAct(id, { kind: 'stage', stage: s, text: r }, 'Marked lost'); return; } leadAct(id, { kind: 'stage', stage: s }, 'Stage set'); },
  leadAppt: id => { modal(mhd('Book appointment', '') + `<form class="bd" id="apf"><div class="form"><div class="fld"><label for="ap-at">When</label><input class="in" type="datetime-local" id="ap-at" required></div><div class="fld"><label for="ap-w">With</label><input class="in" id="ap-w" value="${esc(S.me.actor)}"></div><div class="fld"><label for="ap-p">Purpose</label><input class="in" id="ap-p" value="Account opening"></div><div class="fld"><label for="ap-pl">Place</label><input class="in" id="ap-pl" placeholder="Branch, video call…"></div></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Book</button></div></form>`); $('apf').onsubmit = e => { e.preventDefault(); leadAct(id, { kind: 'appointment', at: $('ap-at').value, with: $('ap-w').value, text: $('ap-p').value, place: $('ap-pl').value }, 'Appointment booked'); }; },
  apptSet: (id, el) => leadAct(id, { kind: 'appointment_status', appointment: el.dataset.ap, status: el.dataset.st }, 'Updated'),
  leadConvert: id => { const l = C.crm.leads.find(x => x.id === id); A.newClient(); Object.assign(UI.ob.data, { name: l.name, email: l.email || '', city: l.city || '' }); UI.ob.lead = id; UI.ob.leadMasked = l.masked; if (!l.masked) UI.ob.data.mobile = l.mobile; obModal(); },
  crmStage: (id, el) => { UI.crmStage = el.value; $('page').innerHTML = groupBar('crm') + V.crm.html(C.crm); bindPage(); },
  crmPeriod: () => { UI.crmFrom = $('crm-from').value; UI.crmTo = $('crm-to').value; render({ keep: true }); },
  portalAccess: id => portalModal(id),
  openBlob: id => openBlob(id),
  addDoc: async id => { const f = $('doc-file').files[0]; if (!f) { toast('Choose a file first'); return; } try { await api('POST', `/v1/clients/${encodeURIComponent(id)}/documents?kind=${encodeURIComponent($('doc-kind').value)}&name=${encodeURIComponent(f.name)}`, f, true); toast('Document added'); clientDrawer(id); } catch (e) { toast(e.message, true); } },
  setRisk: id => riskModal(id),
  reKYC: id => reKYCModal(id),
  kraFile: () => kraModal(),
  newBranch: () => A.editBranch(''),
  editBranch: code => { const b = C.partners.br.find(x => x.code === code) || { code: '', name: '', city: '', manager: '', active: true };
    confirmModal(code ? 'Edit branch ' + esc(code) : 'New branch', '', `<div class="form"><div class="fld"><label for="bx-c">Code</label><input class="in" id="bx-c" value="${esc(b.code)}" ${code ? 'readonly' : ''}></div><div class="fld"><label for="bx-n">Name</label><input class="in" id="bx-n" value="${esc(b.name)}"></div><div class="fld"><label for="bx-t">City</label><input class="in" id="bx-t" value="${esc(b.city)}"></div><div class="fld"><label for="bx-m">Manager (user ID)</label><input class="in" id="bx-m" value="${esc(b.manager || '')}"></div><div class="fld"><label for="bx-k">Level</label><select class="in" id="bx-k">${['Zone', 'Region', 'Branch', 'Franchise'].map(k => `<option ${k === (b.kind || 'Branch') ? 'selected' : ''}>${k}</option>`).join('')}</select></div><div class="fld"><label for="bx-p">Reports to</label><select class="in" id="bx-p"><option value="">None (top level)</option>${C.partners.br.filter(x => x.code !== b.code).map(x => `<option value="${esc(x.code)}" ${x.code === b.parent ? 'selected' : ''}>${esc(x.name)} · ${esc(x.code)}</option>`).join('')}</select></div><p class="dim small full" style="margin:0">Staff of an office see the clients of every office under it.</p><label class="check full"><input type="checkbox" id="bx-a" ${b.active ? 'checked' : ''}> Open</label></div>`, 'Save', () => act(() => post('/v1/branches', { code: $('bx-c').value.trim(), name: $('bx-n').value.trim(), city: $('bx-t').value.trim(), manager: $('bx-m').value.trim(), kind: $('bx-k').value, parent: $('bx-p').value, active: $('bx-a').checked }), 'Branch saved')); },
  newPartner: () => partnerModal(),
  editPartner: code => partnerModal(C.partners.pt.find(p => p.code === code)),
  ptPeriod: () => { UI.ptFrom = $('pt-from').value; UI.ptTo = $('pt-to').value; render({ keep: true }); },
  postAP: () => confirmModal('Post AP payouts', `${fmtDate(C.partners.from)} – ${fmtDate(C.partners.to)}`, '<div class="note">Books each AP’s share, GST and TDS and credits the AP’s account. A period is posted once.</div>', 'Post', () => act(() => post('/v1/ap-payouts/post', { from: C.partners.from, to: C.partners.to }), r => `${r.posted} payouts posted`)),
  postRef: () => confirmModal('Post referral rewards', `${fmtDate(C.partners.from)} – ${fmtDate(C.partners.to)}`, '<div class="note">Credits each referrer’s trading ledger.</div>', 'Post', () => act(() => post('/v1/referrals/post', { from: C.partners.from, to: C.partners.to }), r => `${r.posted} rewards posted`)),
  mapClient: ucc => mapModal(ucc),
  privacy: (ucc, el) => { const on = el.dataset.p === '1'; confirmModal(on ? 'Make ' + ucc + ' private' : 'Make ' + ucc + ' visible', on ? 'Only admins, compliance and the users you name will see this client.' : 'Every user will see this client again.', on ? '<div class="fld"><label for="pv-u">Also visible to (user IDs, comma separated)</label><input class="in mono" id="pv-u"></div>' : '', 'Save', async () => { await post(`/v1/clients/${encodeURIComponent(ucc)}/privacy`, { private: on, visible_to: ($('pv-u')?.value || '').split(',').map(x => x.trim()).filter(Boolean) }); toast('Saved'); clientDrawer(ucc); }); },
  newScheme: () => confirmModal('New prepaid scheme', '', `<div class="form"><div class="fld"><label for="sc-i">ID</label><input class="in mono" id="sc-i" placeholder="credit-25k"></div><div class="fld"><label for="sc-n">Name</label><input class="in" id="sc-n"></div><div class="fld"><label for="sc-k">Kind</label><select class="in" id="sc-k"><option>Credit</option><option>Plan</option></select></div><div class="fld"><label for="sc-p">Price incl. GST (₹)</label><input class="in" id="sc-p"></div><div class="fld"><label for="sc-d">Validity (days)</label><input class="in" type="number" id="sc-d" value="365"></div><div class="fld"><label for="sc-l">Brokerage plan (Plan kind)</label><input class="in mono" id="sc-l" placeholder="active-trader"></div></div>`, 'Save', () => act(() => post('/v1/prepaid/schemes', { id: $('sc-i').value.trim(), name: $('sc-n').value.trim(), kind: $('sc-k').value, price: $('sc-p').value.trim(), days: parseInt($('sc-d').value, 10) || 0, plan: $('sc-l').value.trim(), active: true }), 'Scheme saved')),
  sellScheme: async () => { await clientCache(); const sc = C.prepaid.schemes.filter(x => x.active); confirmModal('Sell a prepaid scheme', 'The price is debited to the client’s ledger now; the client must have that much free.', `<div class="fld"><label for="ss-u-q">Client</label>${cpick('ss-u', '', {})}</div><div class="fld"><label for="ss-s">Scheme</label><select class="in" id="ss-s">${sc.map(x => `<option value="${esc(x.id)}">${esc(x.name)} · ₹${nf(x.price)}</option>`).join('')}</select></div><label class="check"><input type="checkbox" id="ss-r"> Renew automatically on expiry</label>`, 'Sell', () => act(() => post('/v1/prepaid/subscriptions', { date: S.date, ucc: $('ss-u').value, scheme: $('ss-s').value, auto_renew: $('ss-r').checked }), r => `${r.id} started; invoice ${r.invoice}`)); },
  runPrepaid: () => confirmModal('Adjust prepaid brokerage', fmtDate(S.date), '<div class="note">Refunds the day’s brokerage (with its GST) to clients with prepaid credit, expires schemes that ended and renews those set to renew.</div>', 'Run', () => act(() => post('/v1/prepaid/run', { date: S.date }), r => `${r.adjusted} adjusted, ${r.expired} expired, ${r.renewed} renewed`)),
  newVoucher: () => voucherModal(),
  newAccount: () => accountModal(),
  editAccount: code => accountModal(C.books.acc.find(a => a.code === code)),
  acctStmt: async code => { try { const st = await get(`/v1/ledger/statement?account=${encodeURIComponent(code)}&from=${fyStart(S.date)}&to=${S.date}`);
    const dc = a => num(a) >= 0 ? nf(a, 2) + ' Dr' : nf(-num(a), 2) + ' Cr';
    modal(mhd('Account statement · ' + esc(code), `${fmtDate(st.from)} – ${fmtDate(st.to)} · opening ${dc(st.opening)} · closing ${dc(st.closing)}`) + `<div class="bd">${tbl([['Date'], ['Entry'], ['Narration'], ['Debit', 'r'], ['Credit', 'r'], ['Balance', 'r']], st.lines.map(l => `<tr><td class="n">${fmtDate(l.value_date)}</td><td class="n dim">${esc(l.entry_id)}</td><td class="wrap">${esc(l.narration)}</td><td class="r">${num(l.debit) ? nf(l.debit, 2) : ''}</td><td class="r">${num(l.credit) ? nf(l.credit, 2) : ''}</td><td class="r">${dc(l.balance)}</td></tr>`), { tall: true, empty: 'No movements in this financial year.' })}</div>`); } catch (e) { toast(e.message, true); } },
  vchDo: (id, el) => { const a = el.dataset.a, v = C.books.vch.find(x => x.no.replaceAll('/', '-') === id); confirmModal({ approve: 'Post ', reject: 'Reject ', cancel: 'Cancel ' }[a] + v.no, v.narration, a === 'approve' ? '' : '<div class="fld"><label for="vd-r">Reason</label><input class="in" id="vd-r"></div>', { approve: 'Post', reject: 'Reject', cancel: 'Cancel voucher' }[a], () => act(() => post(`/v1/vouchers/${id}/${a}`, { reason: $('vd-r')?.value || '' }), { approve: 'Voucher posted', reject: 'Voucher rejected', cancel: 'Voucher cancelled (reversed)' }[a]), a !== 'approve'); },
  newVendor: () => vendorModal({}),
  editVendor: id => vendorModal(C.books.vend.find(v => v.id === id)),
  scanBill: () => scanBillPick(),
  newBill: () => billModal(null),
  bookScan: id => billModal(C.books.scans.find(x => x.id === id)),
  payBill: id => payBillModal(C.books.bills.bills.find(b => b.id === id)),
  vbPeriod: () => { UI.vbFrom = $('vb-from').value; UI.vbTo = $('vb-to').value; render({ keep: true }); },
  finPeriod: () => { UI.finFrom = $('fin-from').value; UI.finTo = $('fin-to').value; UI.finBranch = $('fin-br')?.value || ''; render({ keep: true }); },
  gstPeriod: () => { UI.gstFrom = $('gst-from').value; UI.gstTo = $('gst-to').value; render({ keep: true }); },
  invDoc: id => openDoc(`/v1/invoices/${id}/document`),
  shortSettle: id => { const x = C.settlement.shortages.find(s => s.id === id); confirmModal('Close out ' + id, `${x.ucc} short ${x.qty} ${x.symbol} · ${x.internal} internal, ${x.auction} exchange auction`, `<div class="form"><div class="fld"><label for="ss-p">Auction / close-out price (₹)</label><input class="in" id="ss-p" value="${esc(x.suggested)}"></div><div class="fld"><label for="ss-d">Date</label><input class="in" type="date" id="ss-d" value="${esc(S.date)}"></div></div><div class="note">Debits ${x.ucc} the price × ${x.qty}. ${x.buyers.length ? 'Credits ' + x.buyers.map(b => b.ucc + ' for ' + b.short).join(', ') + ' and pays out the rest of their purchase.' : 'The clearing corporation recovers the auction cost.'}</div>`, 'Close out', () => act(() => post(`/v1/shortages/${id}/settle`, { date: $('ss-d').value, price: $('ss-p').value.trim() }), 'Short delivery closed out')); },
  ipoPost: () => {
    const issues = [...new Set(C.ipo.ipo.filter(x => x.allotted_lots > 0).map(x => x.issue))];
    confirmModal('Post IPO allotments', 'Records the allotted shares as purchases at the issue price on the allotment date, for holdings cost and capital gains. The shares arrive through the depository holding file.', `<div class="form"><div class="fld"><label for="ip-i">Issue</label><select class="in" id="ip-i">${issues.map(i => `<option>${esc(i)}</option>`).join('')}</select></div><div class="fld"><label for="ip-isin">ISIN</label><input class="in mono" id="ip-isin"></div><div class="fld"><label for="ip-s">Symbol</label><input class="in mono" id="ip-s"></div><div class="fld"><label for="ip-p">Issue price (₹)</label><input class="in" id="ip-p"></div><div class="fld"><label for="ip-l">Shares per lot</label><input class="in" id="ip-l" type="number" min="1"></div><div class="fld"><label for="ip-d">Allotment date</label><input class="in" type="date" id="ip-d" value="${esc(S.date)}"></div></div>`, 'Post', () => act(() => post('/v1/ipo/allotments', { issue: $('ip-i').value, isin: $('ip-isin').value, symbol: $('ip-s').value, price: $('ip-p').value.trim(), lot_size: parseInt($('ip-l').value, 10) || 0, date: $('ip-d').value }), r => `${r.posted} allotments posted (${r.shares} shares)${r.skipped ? ', ' + r.skipped + ' already posted' : ''}`));
  },
  newCA: () => { modal(mhd('Record a corporate action', 'From the exchange or registrar announcement. A checker applies it on the record date.') + `<form class="bd" id="caf"><div class="form">
      <div class="fld"><label for="ca-t">Type</label><select class="in" id="ca-t"><option>Dividend</option><option>Bonus</option><option>Split</option><option>Merger</option><option>Symbol change</option><option>Rights</option></select></div>
      <div class="fld"><label for="ca-s">Symbol</label><input class="in mono" id="ca-s" required></div><div class="fld"><label for="ca-i">ISIN</label><input class="in mono" id="ca-i" required></div>
      <div class="fld"><label for="ca-x">Ex-date</label><input class="in" type="date" id="ca-x" value="${esc(S.date)}"></div><div class="fld"><label for="ca-r">Record date</label><input class="in" type="date" id="ca-r" value="${esc(S.date)}"></div>
      <div class="fld"><label for="ca-p">Payment / allotment date</label><input class="in" type="date" id="ca-p"></div>
      <div class="fld"><label for="ca-d">Dividend per share (₹)</label><input class="in" id="ca-d" placeholder="dividend only"></div>
      <div class="fld"><label for="ca-ni">New ISIN (merger, ISIN change, rights entitlement)</label><input class="in mono" id="ca-ni"></div><div class="fld"><label for="ca-ns">New symbol</label><input class="in mono" id="ca-ns"></div>
      <div class="fld"><label for="ca-n">Bonus, merger, rights: new shares · Split: new face value</label><input class="in" id="ca-n" type="number" min="1"></div><div class="fld"><label for="ca-o">Bonus, merger, rights: per shares held · Split: old face value</label><input class="in" id="ca-o" type="number" min="1"></div>
    </div><div id="ca-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Record</button></div></form>`);
    $('caf').onsubmit = async e => { e.preventDefault(); try { await post('/v1/corporate-actions', { type: $('ca-t').value, symbol: $('ca-s').value, isin: $('ca-i').value, ex_date: $('ca-x').value, record_date: $('ca-r').value, pay_date: $('ca-p').value, dps: $('ca-d').value || '0', ratio_new: parseInt($('ca-n').value, 10) || 0, ratio_old: parseInt($('ca-o').value, 10) || 0, new_isin: $('ca-ni').value, new_symbol: $('ca-ns').value }); closeModal(); toast('Corporate action recorded'); render({ keep: true }); } catch (err) { $('ca-err').innerHTML = errHTML(err); } }; },
  caApply: id => { const c = C.corp.ca.find(x => x.id === id); confirmModal('Apply ' + id, `${c.type} · ${c.symbol}`, `${tbl([['Client'], ['Demat', 'r'], ['With firm', 'r'], [c.type === 'Dividend' ? 'Dividend' : 'New shares', 'r']], c.lines.map(l => `<tr><td>${l.ucc} ${esc(l.name)}</td><td class="r">${l.held}</td><td class="r">${l.broker_held}</td><td class="r">${c.type === 'Dividend' ? nf(num(l.dividend) + num(l.via_broker), 2) : l.new_shares}</td></tr>`), { empty: 'No client holds this scrip.' })}<div class="note">${c.type === 'Dividend' ? 'Freezes the entitlements. Pass on the dividend on broker-held shares once it reaches the bank.' : 'Adjusts every client’s holding and average cost now; purchase lots are adjusted from the ex-date.'}</div>`, 'Apply', () => act(() => post(`/v1/corporate-actions/${id}/apply`, {}), 'Corporate action applied')); },
  caDiv: id => { const c = C.corp.ca.find(x => x.id === id); confirmModal('Dividend received · ' + c.symbol, `Owed to clients on broker-held shares: ${inr(c.recon.expected, 2)}`, `<div class="form"><div class="fld"><label for="dv-a">Amount received (₹)</label><input class="in" id="dv-a" value="${esc(c.recon.expected)}"></div><div class="fld"><label for="dv-r">Bank reference</label><input class="in mono" id="dv-r"></div><div class="fld"><label for="dv-d">Date</label><input class="in" type="date" id="dv-d" value="${esc(S.date)}"></div></div>`, 'Credit clients', () => act(() => post(`/v1/corporate-actions/${id}/dividend`, { date: $('dv-d').value, ref: $('dv-r').value.trim(), received: $('dv-a').value.trim() }), 'Dividend credited to clients')); },
  newBB: () => { modal(mhd('Record a buy-back offer', 'Tender offer through the exchange acquisition window.') + `<form class="bd" id="bbf"><div class="form"><div class="fld"><label for="bb-s">Symbol</label><input class="in mono" id="bb-s" required></div><div class="fld"><label for="bb-i">ISIN</label><input class="in mono" id="bb-i" required></div><div class="fld full"><label for="bb-c">Company</label><input class="in" id="bb-c"></div><div class="fld"><label for="bb-p">Offer price (₹)</label><input class="in" id="bb-p" required></div><div class="fld"><label for="bb-r">Record date</label><input class="in" type="date" id="bb-r" value="${esc(S.date)}"></div><div class="fld"><label for="bb-o">Tenders open</label><input class="in" type="date" id="bb-o" value="${esc(S.date)}"></div><div class="fld"><label for="bb-x">Tenders close</label><input class="in" type="date" id="bb-x"></div><div class="fld"><label for="bb-sr">Entitlement, small shareholders</label><input class="in mono" id="bb-sr" placeholder="e.g. 13/100"></div><div class="fld"><label for="bb-gr">Entitlement, general category</label><input class="in mono" id="bb-gr" placeholder="e.g. 1/50"></div></div><p class="dim small">Ratios from the letter of offer: shares accepted for every shares held on the record date. They give each holder's entitlement in Client insights.</p><div id="bb-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Record</button></div></form>`);
    $('bbf').onsubmit = async e => { e.preventDefault(); try { await post('/v1/buybacks', { symbol: $('bb-s').value, isin: $('bb-i').value, company: $('bb-c').value, price: $('bb-p').value, record_date: $('bb-r').value, open: $('bb-o').value, close: $('bb-x').value, small_ratio: $('bb-sr').value.trim(), general_ratio: $('bb-gr').value.trim() }); closeModal(); UI.caTab = 'bb'; toast('Buy-back recorded'); render({ keep: true }); } catch (err) { $('bb-err').innerHTML = errHTML(err); } }; },
  bbTender: async id => { await clientCache(); confirmModal('Tender for a client', 'Shares must be free (not pledged) in the client’s demat. A new tender replaces the earlier one; 0 cancels.', `<div class="fld"><label for="bt-u-q">Client</label>${cpick('bt-u', '', {})}</div><div class="fld"><label for="bt-q">Shares to tender</label><input class="in" id="bt-q" type="number" min="0"></div>`, 'Tender', () => act(() => post(`/v1/buybacks/${id}/tender`, { date: S.date, ucc: $('bt-u').value, qty: parseInt($('bt-q').value, 10) || 0 }), 'Tender recorded')); },
  bbSettle: id => { const b = C.corp.bb.find(x => x.id === id); confirmModal('Buy-back acceptance · ' + b.symbol, 'From the registrar’s basis of acceptance. Accepted shares leave the demat; proceeds are credited through the clearing corporation.', `${tbl([['Client'], ['Tendered', 'r'], ['Accepted', 'r']], b.bids.filter(x => x.status === 'Tendered').map(x => `<tr><td>${x.ucc} ${esc(x.name)}</td><td class="r">${x.qty}</td><td class="r"><input class="in sm" style="width:90px" type="number" min="0" max="${x.qty}" data-bbacc="${x.ucc}" value="${x.qty}"></td></tr>`))}<div class="fld"><label for="bs-d">Settlement date</label><input class="in" type="date" id="bs-d" value="${esc(S.date)}"></div>`, 'Settle', () => { const acc = {}; document.querySelectorAll('[data-bbacc]').forEach(x => acc[x.dataset.bbacc] = parseInt(x.value, 10) || 0); return act(() => post(`/v1/buybacks/${id}/settle`, { date: $('bs-d').value, exchange: 'NSE', accepted: acc }), 'Buy-back settled'); }); },
  newReceipt: () => receiptModal(),
  newPayout: () => payoutModal(),
  rcDecide: (id, el) => { const ok = el.dataset.a === '1'; confirmModal((ok ? 'Approve ' : 'Reject ') + id, ok ? 'Credits the client now (cheques stay uncleared until the bank clears them).' : 'The client is not credited.', ok ? '' : '<div class="fld"><label for="rd-n">Reason</label><input class="in" id="rd-n"></div>', ok ? 'Approve' : 'Reject', () => act(() => post(`/v1/receipts/${id}/decide`, { approve: ok, note: $('rd-n')?.value || '' }), ok ? 'Receipt approved' : 'Receipt rejected'), !ok); },
  chqClear: (id, el) => { const bounced = el.dataset.a === '1'; confirmModal(bounced ? 'Cheque returned · ' + id : 'Cheque cleared · ' + id, bounced ? 'Reverses the credit to the client.' : 'The amount becomes usable for limits and payouts.', `<div class="form"><div class="fld"><label for="cq-d">Date</label><input class="in" type="date" id="cq-d" value="${esc(S.date)}"></div>${bounced ? '<div class="fld"><label for="cq-n">Bank’s return reason</label><input class="in" id="cq-n" placeholder="e.g. Funds insufficient"></div><label class="check full"><input type="checkbox" id="cq-c" checked> Charge the cheque return fee (with GST) and issue an invoice</label>' : ''}</div>`,
    bounced ? 'Mark returned' : 'Mark cleared', () => act(() => post(`/v1/receipts/${id}/clear`, { date: $('cq-d').value, bounced, charge: !!$('cq-c')?.checked, note: $('cq-n')?.value || '' }), bounced ? 'Cheque marked returned' : 'Cheque cleared'), bounced); },
  settleGateway: () => confirmModal('Gateway settlement', 'The gateway has paid collected money into the client bank account.', '<div class="form"><div class="fld"><label for="gs-r">Settlement reference</label><input class="in mono" id="gs-r"></div><div class="fld"><label for="gs-f">Gateway fee deducted (₹)</label><input class="in" id="gs-f" value="0"></div><p class="dim small full" style="margin:0">The fee is the firm’s cost: it is booked as an expense and made good to the client account from the firm’s own bank.</p></div>', 'Record', () => act(() => post('/v1/gateway/settle', { date: S.date, ref: $('gs-r').value.trim(), fee: $('gs-f').value.trim() || '0' }), r => `${r.receipts} payments settled`)),
  devGateway: async () => { await clientCache(); confirmModal('Simulate an online payment', 'Sends a signed payment.captured webhook, as the gateway would.', `<div class="fld"><label for="dg-u-q">Client</label>${cpick('dg-u', '', {})}</div><div class="fld"><label for="dg-a">Amount (₹)</label><input class="in" id="dg-a" value="25000"></div>`, 'Send webhook', () => act(() => post('/v1/dev/gateway-payment', { ucc: $('dg-u').value, amount: $('dg-a').value, date: S.date }), r => `Online payment ${r.receipt.ref} credited to ${r.receipt.ucc}`)); },
  poDecide: (id, el) => { const ok = el.dataset.a === '1'; confirmModal((ok ? 'Approve payout ' : 'Reject payout ') + id, ok ? 'Funds are checked again across all segments before approval.' : 'Tell the client why.', ok ? '' : '<div class="fld"><label for="pd-r">Reason</label><input class="in" id="pd-r"></div>', ok ? 'Approve' : 'Reject', () => act(() => post(`/v1/payouts/${id}/decide`, { date: S.date, approve: ok, reason: $('pd-r')?.value || '' }), ok ? 'Payout approved' : 'Payout rejected'), !ok); },
  payoutBatch: () => confirmModal('Release payouts', 'Debits each client and produces the bank upload (NEFT; RTGS from ₹2 lakh).', '', 'Release', () => act(() => post('/v1/payouts/batches', { date: S.date }), r => { UI.monTab = 'batch'; return `Batch ${r.id}: ${r.items.length} transfers, ${inr(r.total, 2)}`; })),
  poConfirm: id => { const b = C.money.batches.find(x => x.id === id);
    modal(mhd('Bank result · ' + id, 'Enter the UTR for each paid transfer, or mark it failed: a failed transfer is credited back to the client.') + `<div class="bd">${tbl([['Request'], ['Client'], ['Amount', 'r'], ['UTR'], ['Failed?']], b.items.map(i => `<tr><td class="n">${i.request}</td><td>${i.ucc} ${esc(i.name)}</td><td class="r">${nf(i.amount, 2)}</td><td><input class="in sm mono" data-utr="${i.request}" placeholder="UTR"></td><td><input class="in sm" data-fail="${i.request}" placeholder="reason if failed"></td></tr>`))}<div id="pc-err"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="pc-ok">Confirm</button></div>`);
    $('pc-ok').onclick = async () => { const utrs = {}, failed = {}; document.querySelectorAll('[data-utr]').forEach(x => { if (x.value.trim()) utrs[x.dataset.utr] = x.value.trim(); }); document.querySelectorAll('[data-fail]').forEach(x => { if (x.value.trim()) failed[x.dataset.fail] = x.value.trim(); });
      try { await act(() => post(`/v1/payouts/batches/${id}/confirm`, { utrs, failed }), r => `${r.results.Paid} paid, ${r.results.Failed} failed`); } catch (e) { $('pc-err').innerHTML = errHTML(e); } }; },
  intPeriod: () => { UI.intFrom = $('int-from').value; UI.intTo = $('int-to').value; render({ keep: true }); },
  postInterest: () => { const iv = C.debits.intr; confirmModal('Post interest', `${fmtDate(iv.from)} to ${fmtDate(iv.to)} at ${iv.rate_pct}% a year`, `<div class="note">Debits ${iv.rows.filter(r => r.status === 'To post').length} clients ${inr(iv.to_post, 2)} including GST and issues a tax invoice to each.</div>`, 'Post interest', () => act(() => post('/v1/interest/post', { from: iv.from, to: iv.to }), r => `Interest posted for ${r.posted} client${r.posted === 1 ? '' : 's'}`)); },
  debitBlock: () => { const v = C.debits.deb; confirmModal('Apply continuous-debit blocks', `${v.to_block} to block, ${v.to_unblock} to unblock`, '<div class="note">Blocked clients cannot buy until the debit is cleared; selling stays open. Send the block/unblock file to the trading front end after applying.</div>', 'Apply', () => act(() => post('/v1/debits/block', { date: S.date }), r => `${r.blocked} blocked, ${r.unblocked} unblocked`)); },
  correctBill: u => correctModal(u || ''),
  inSelOne: (u, el) => { el.checked ? UI.inSel.add(u) : UI.inSel.delete(u); rerenderPart('in-t'); },
  inSelAll: (id, el) => { document.querySelectorAll('[data-act=inSelOne]').forEach(x => el.checked ? UI.inSel.add(x.dataset.id) : UI.inSel.delete(x.dataset.id)); rerenderPart('in-t'); },
  flagInactive: () => { const u = [...UI.inSel]; if (!u.length) { toast('Select the accounts to flag'); return; }
    confirmModal(`Flag ${u.length} account${u.length > 1 ? 's' : ''} inactive`, 'They will be marked Inactive in today’s UCC upload batch and cannot trade until reactivated.', `<div class="mono small">${u.map(esc).join(', ')}</div>`, 'Flag inactive', () => act(() => post('/v1/inactive/flag', { date: S.date, uccs: u }), r => { UI.inSel.clear(); return `${r.flagged} account${r.flagged > 1 ? 's' : ''} flagged inactive`; }), true); },
  reactivate: ucc => reactivateModal(ucc),
  reactDecide: (id, el) => { const ok = el.dataset.a === '1';
    confirmModal(ok ? 'Approve reactivation ' + id : 'Reject reactivation ' + id, ok ? 'Clears the Inactive flag in the next UCC batch and restarts the inactivity period from today.' : 'Give the reason; the account stays inactive.', ok ? '' : '<div class="fld"><label for="rd-r">Reason</label><input class="in" id="rd-r"></div>',
      ok ? 'Approve' : 'Reject', () => act(() => post(`/v1/inactive/reactivations/${id}/decide`, { date: S.date, approve: ok, reason: $('rd-r')?.value || '' }), ok ? 'Account reactivated' : 'Reactivation rejected'), !ok); },
  contact: async ucc => {
    const c = (await get(`/v1/clients/${encodeURIComponent(ucc)}?date=${S.date}`)).client;
    confirmModal('Update contact details · ' + ucc, 'A change uploaded to the KRA and shown there as validated or registered counts as account activity.', `<div class="form"><div class="fld"><label for="ct-email">Email</label><input class="in" id="ct-email" value="${esc(c.email)}"></div><div class="fld"><label for="ct-mobile">Mobile</label><input class="in" id="ct-mobile" value="${esc(c.mobile)}"></div><div class="fld"><label for="ct-city">Address (city)</label><input class="in" id="ct-city" value="${esc(c.city)}"></div><div class="fld"><label for="ct-kra">KRA status after upload</label><select class="in" id="ct-kra"><option>Validated</option><option>Registered</option><option>Under process</option><option>On hold</option></select></div></div>`,
      'Save', () => act(() => post(`/v1/clients/${encodeURIComponent(ucc)}/contact`, { date: S.date, email: $('ct-email').value, mobile: $('ct-mobile').value, city: $('ct-city').value, kra_status: $('ct-kra').value }), 'Details updated'));
  },
  cpPick: (id, el) => cpSet(id, el.dataset.v),
  selOne: (u, el) => { el.checked ? UI.clientSel.add(u) : UI.clientSel.delete(u); rerenderPart('clients-t'); },
  lgSel: (id, el) => { el.checked ? UI.lgSel.add(id) : UI.lgSel.delete(id); rerenderPart('lg-t'); },
  lgSelAll: (id, el) => { const on = el.checked; document.querySelectorAll('[data-act=lgSel]').forEach(x => on ? UI.lgSel.add(x.dataset.id) : UI.lgSel.delete(x.dataset.id)); rerenderPart('lg-t'); },
  lgPropose: () => confirmModal('Propose as legacy debts', `${UI.lgSel.size} client${UI.lgSel.size === 1 ? '' : 's'}`, '<div class="fld full"><label for="lg-why">Why these debits are legacy</label><input class="in" id="lg-why" placeholder="For example: no movement for over six months, accounts dormant"></div><div class="note">A different person approves them. Until then nothing changes.</div>', 'Propose', () => act(() => post('/v1/legacy-debts/propose', { date: S.date, uccs: [...UI.lgSel], reason: $('lg-why').value }), r => { UI.lgSel.clear(); return `${r.proposed} proposed`; })),
  lgDecide: id => { const ok = id === '1'; confirmModal(ok ? 'Approve legacy debts' : 'Reject legacy debts', `${UI.lgSel.size} selected`, `<div class="fld full"><label for="lg-why">${ok ? 'Note (optional)' : 'Reason for rejecting'}</label><input class="in" id="lg-why"></div>${ok ? '<div class="note">From now on no interest is charged on them and they leave the daily debit work. The clients stay blocked for buying.</div>' : ''}`, ok ? 'Approve' : 'Reject', () => act(() => post('/v1/legacy-debts/decide', { date: S.date, uccs: [...UI.lgSel], approve: ok, reason: $('lg-why').value }), r => { UI.lgSel.clear(); return `${r.decided} ${ok ? 'approved' : 'rejected'}`; }), !ok); },
  lgRemind: () => { const n = UI.lgSel.size; confirmModal('Send reminders', n ? `${n} selected` : 'every open legacy debt', '<div class="note">Each client gets the “legacy_debt.reminder” message with the amount due, and the reminder is recorded against the debt.</div>', 'Send', () => act(() => post('/v1/legacy-debts/remind', { date: S.date, uccs: [...UI.lgSel] }), r => { UI.lgSel.clear(); return `${r.reminded} reminded`; })); },
  lgNote: id => confirmModal('Record a recovery step', id, `<div class="form"><div class="fld"><label for="lg-kind">What</label><select class="in" id="lg-kind">${C.legacy.v.note_kinds.map(k => `<option>${esc(k)}</option>`).join('')}</select></div><div class="fld"><label for="lg-date">Date</label><input class="in mono" type="date" id="lg-date" value="${esc(S.date)}"></div><div class="fld full"><label for="lg-text">Details</label><input class="in" id="lg-text" placeholder="Who, what was said or sent, the reference"></div></div>`, 'Record', () => act(() => post(`/v1/legacy-debts/${encodeURIComponent(id)}/note`, { date: $('lg-date').value, kind: $('lg-kind').value, text: $('lg-text').value }), 'Recorded')),
  lgWriteOff: id => { const r = C.legacy.v.rows.find(x => x.ucc === id); confirmModal('Write off', `${id} · ${inr(r.debit, 2)} owed`, `<div class="form"><div class="fld"><label for="lg-amt">Amount (₹)</label><input class="in mono" id="lg-amt" value="${esc(String(r.debit))}"></div><div class="fld"><label for="lg-date">Date</label><input class="in mono" type="date" id="lg-date" value="${esc(S.date)}"></div><div class="fld full"><label for="lg-why">Reason and approval</label><input class="in" id="lg-why" placeholder="For example: not recoverable; written off as approved by the directors on …"></div></div><div class="note">Credits the client and debits Bad debts written off. Take your CA’s advice on the tax treatment.</div>`, 'Write off', () => act(() => post(`/v1/legacy-debts/${encodeURIComponent(id)}/write-off`, { date: $('lg-date').value, amount: $('lg-amt').value, reason: $('lg-why').value }), 'Written off'), true); },
  lgUnmark: id => confirmModal('Return to the daily debit work', id, '<div class="fld full"><label for="lg-why">Why</label><input class="in" id="lg-why"></div><div class="note">Interest, reminders and ageing apply to this debit again from the next run.</div>', 'Unmark', () => act(() => post(`/v1/legacy-debts/${encodeURIComponent(id)}/unmark`, { date: S.date, reason: $('lg-why').value }), 'Unmarked')),
  lgHist: id => { const r = C.legacy.v.rows.find(x => x.ucc === id); modal(mhd('History', `${id} · ${esc(r.name)}`) + `<div class="bd">${tbl([['Date'], ['Step'], ['Details'], ['By']], (r.notes || []).map(n => `<tr><td class="n">${fmtDate(n.date)}</td><td>${esc(n.kind)}${num(n.amount) ? ' ' + inr(n.amount, 2) : ''}</td><td class="wrap small">${esc(n.text || '')}</td><td class="small">${esc(n.by)}</td></tr>`), { empty: 'Nothing recorded.' })}</div><div class="ft"><button class="btn" data-act="close">Close</button></div>`); },
  postDailyInterest: () => confirmModal('Post daily interest', `up to ${fmtDate(S.date)}`, '<div class="note">Each day not yet charged, from the day after the last posting, is posted on that day’s overdue debit including interest already posted.</div>', 'Post', () => act(() => post('/v1/interest/daily', { date: S.date }), r => `${r.entries} daily entries for ${r.clients} clients · interest ${inr(r.interest, 2)}`)),
  selAll: (id, el) => { const on = el.checked; document.querySelectorAll('[data-act=selOne]').forEach(x => on ? UI.clientSel.add(x.dataset.id) : UI.clientSel.delete(x.dataset.id)); rerenderPart('clients-t'); },
  bulkClear: () => { UI.clientSel.clear(); rerenderPart('clients-t'); },
  filterTag: t => { UI.clientTag = t; if (S.view !== 'clients') { A.go('clients'); return; } closeModal(); render({ keep: true }); },
  bulkTag: async op => {
    const t = ($('bulk-tag')?.value || '').trim(); if (!t) { toast('Type a group name'); $('bulk-tag')?.focus(); return; }
    const uccs = [...UI.clientSel];
    try { const r = await post('/v1/clients/tags', { uccs, add: op === 'add' ? [t] : [], remove: op === 'remove' ? [t] : [] }); toast(`${r.changed} client${r.changed === 1 ? '' : 's'} ${op === 'add' ? 'added to' : 'removed from'} ${t.toUpperCase()}`); UI.clientSel.clear(); render({ keep: true }); }
    catch (e) { toast(e.message, true); }
  },
  reveal: (ucc, el) => { const f = el.dataset.f, label = f === 'pan' ? 'PAN' : 'bank account number';
    confirmModal('See the full ' + label, `${ucc}. Your reason is recorded in the audit trail with your name and the time.`, '<div class="fld"><label for="rv-why">Reason</label><input class="in" id="rv-why" placeholder="e.g. KRA mismatch on upload; checking with the PAN card"></div>', 'Show', async () => {
      const r = await post(`/v1/clients/${encodeURIComponent(ucc)}/reveal`, { field: f, reason: $('rv-why').value });
      await clientDrawer(ucc); // the reason box replaced the drawer; reopen it with the value shown
      const t = $('rv-' + f); if (t) { t.textContent = r.value; t.classList.add('revealed'); t.nextElementSibling?.remove(); t.scrollIntoView({ block: 'center' }); }
      toast('Shown and recorded in the audit trail'); }); },
  devEdition: async id => { try { await post('/v1/dev/edition', { edition: id }); await loadMe(); toast('Edition: ' + (S.me.edition?.name || id)); if (id === 'manual' && S.view === 'autopilot') S.view = 'dashboard'; render(); } catch (e) { toast(e.message, true); } },
  sealCheck: async (id, el) => { el.disabled = true; el.textContent = 'Checking…'; try { UI.seal = await get('/v1/security?verify=1'); } catch (e) { toast(e.message, true); } await render({ keep: true }); },
  editTags: async ucc => {
    const [c] = await Promise.all([get(`/v1/clients/${encodeURIComponent(ucc)}?date=${S.date}`), clientCache(true)]);
    const cur = c.client.tags || [];
    confirmModal('Groups · ' + ucc, esc(c.client.name), `<div class="fld"><label for="tg-in">Groups, separated by commas</label><input class="in" id="tg-in" list="tag-list2" value="${esc(cur.join(', '))}" placeholder="e.g. HNI, NRI"><datalist id="tag-list2">${CL.tags.map(t => `<option value="${esc(t.tag)}">`).join('')}</datalist></div><div class="tools">${CL.tags.map(t => `<button type="button" class="gtag" data-act="tagAppend" data-id="${esc(t.tag)}">+ ${esc(t.tag)}</button>`).join('')}</div><p class="dim small">Groups decide brokerage plans and group reports. Letters, digits, - or _.</p>`,
      'Save groups', async () => {
        const next = $('tg-in').value.split(',').map(x => x.trim().toUpperCase().replace(/\s+/g, '-')).filter(Boolean);
        const add = next.filter(t => !cur.includes(t)), remove = cur.filter(t => !next.includes(t));
        if (!add.length && !remove.length) { closeModal(); return; }
        await post('/v1/clients/tags', { uccs: [ucc], add, remove }); toast('Groups saved'); closeModal(); CL.date = ''; clientDrawer(ucc); if (S.view === 'clients') render({ keep: true });
      });
  },
  tagAppend: t => { const i = $('tg-in'); const cur = i.value.split(',').map(x => x.trim()).filter(Boolean); if (!cur.map(x => x.toUpperCase()).includes(t)) cur.push(t); i.value = cur.join(', '); },
  planEdit: id => planModal(id),
  planNew: () => planModal(''),
  assign: (target, el) => assignModal(target || '', el?.dataset.scope || ''),
  clientRates: ucc => clientRatesModal(ucc || ''),
  bkSetup: () => bkSetupModal(),
  unassign: (target, el) => { const scope = el?.dataset.scope || ''; confirmModal('Remove plan · ' + target.replace(/^(ucc|tag):/, '') + (scope ? ' · ' + scopeLabel(scope) : ''), 'From the date below this ' + (target === 'default' ? 'firm-wide card' : target.startsWith('ucc:') ? 'client' : 'group') + ' falls back to the next rule' + (scope ? ' (the all-segments plan, then group, then firm default).' : ' (group, then firm default).'), `<div class="fld"><label for="un-from">Effective from</label><input class="in" type="date" id="un-from" value="${esc(S.date)}"></div>`,
    'Remove', () => act(() => post('/v1/brokerage/assignments', { target, scope, plan: '', from: $('un-from').value }), 'Plan removed'), true); },
  umenu: (id, el, e) => { const m = $('umenu'); m.hidden = !m.hidden; },
  signOut: async () => { try { await api('POST', '/v1/auth/logout', {}); } catch (e) {} showLogin('You have signed out.'); },
  switchUser: async id => { try { await api('POST', '/v1/auth/logout', {}); } catch (e) {} await doLogin(id, CFG.dev_password, ''); },
  devLogin: id => doLogin(id, CFG.dev_password, ''),
  loginBack: () => { loginState = { otp: false, user: '', msg: '' }; loginPw = ''; $('page').innerHTML = loginHTML(); },
  pwChange: () => pwModal(false),
  tempDone: () => { closeModal(); render({ keep: true }); },
  totp: () => totpModal(false),
  addUser: () => confirmModal('Add user', 'They get a one-time password to change at first sign-in.', `<div class="form"><div class="fld"><label for="nu-id">User ID</label><input class="in" id="nu-id" placeholder="e.g. meena.r" autocapitalize="none"></div><div class="fld"><label for="nu-name">Full name</label><input class="in" id="nu-name"></div><div class="fld"><label for="nu-role">Role</label><select class="in" id="nu-role">${(S.me.roles || []).map(r => `<option>${esc(r)}</option>`).join('')}</select></div><div class="fld"><label for="nu-br">Branch (branch staff)</label><input class="in" id="nu-br"></div><div class="fld"><label for="nu-pt">Partner code (authorised person)</label><input class="in mono" id="nu-pt"></div></div>`,
    'Add user', async () => { const r = await post('/v1/users', { id: $('nu-id').value.trim(), name: $('nu-name').value.trim(), role: $('nu-role').value, branch: $('nu-br').value.trim(), partner: $('nu-pt').value.trim().toUpperCase() }); showTempPw(r.user, r.temp_password, 'created'); }),
  resetPw: id => confirmModal('Reset password · ' + id, 'Gives a new one-time password and signs them out everywhere.', '', 'Reset password', async () => { const r = await post(`/v1/users/${encodeURIComponent(id)}/reset-password`); showTempPw(id, r.temp_password, 'reset'); }, true),
  reset2fa: id => confirmModal('Reset two-factor · ' + id, 'For a lost phone. Signs them out; they set it up again at next sign-in.', '', 'Reset 2FA', () => act(() => post(`/v1/users/${encodeURIComponent(id)}/reset-2fa`), 'Two-factor sign-in reset'), true),
  toggleUser: id => { const u = C.settings.users.find(x => x.id === id); act(() => api('PUT', `/v1/users/${encodeURIComponent(id)}`, { id, name: u.name, role: u.role, active: !u.active }), u.active ? id + ' deactivated' : id + ' activated'); },
  crKind: id => { crSync(); UI.cr.kind = id; { const df = (C.statements?.defs || []).find(x => x.kind === id)?.default; if (df) UI.cr.preset = df; } UI.crRes = null; UI.crErr = null; $('page').innerHTML = groupBar('statements') + V.statements.html(C.statements); bindPage(); },
  crPreset: id => { crSync(); UI.cr.preset = id; $('page').innerHTML = groupBar('statements') + V.statements.html(C.statements); bindPage(); },
  crRun: () => { crSync(); crRun(); },
  crPrint: () => { crSync(); openDoc(crURL('html')); },
  crCSV: () => { crSync(); const r = UI.cr; download(crURL('csv'), `${r.kind}_${r.ucc}_${r.from ? r.from + '_' : ''}${r.to}.csv`); },
  crFor: id => { UI.cr.ucc = id; UI.crRes = null; A.go('statements'); },
  wOpen: async id => {
    const w = (C.autopilot?.work || []).find(x => x.id === id); if (!w) return;
    const [wv, wid] = String(w.view).split(':'); S.view = wv; closeModal(); await render(); window.scrollTo(0, 0);
    if (wv === 'dispatch' && wid) return dspModal(wid);
    const k = String(w.key || '').split(':').pop(); if (w.view === 'recon' && C.recon?.all?.some(b => b.id === k)) breakModal(k);
  },
  go: id => { const [v, tab] = String(id).split(':'); if (v === 'circulars' && tab) UI.ciTab = tab; S.view = v; UI.dashEdit = false; closeModal(); render(); window.scrollTo(0, 0); },
  close: () => closeModal(),
  ovclose: (id, el, e) => { if (e.target === el && !modalLocked) closeModal(); },
  ui: (id, el) => { UI[el.dataset.k] = el.dataset.v; if (el.dataset.k === 'lgTab') { UI.lgSel.clear(); render({ keep: true }); return; } const part = el.dataset.part; if (part && $(part)) { rerenderPart(part); el.parentElement.querySelectorAll('button').forEach(b => b.classList.toggle('on', b === el)); } else { $('page').innerHTML = groupBar(S.view) + V[S.view].html(C[S.view]); bindPage(); } },
  dl: (path, el) => download(path, el.dataset.name),
  client: id => clientDrawer(id),
  nomChange: async id => { try { const d = await get(`/v1/clients/${encodeURIComponent(id)}?date=${S.date}`); UI.chNoms = d.client.nominees || []; changeModal('Nomination', id, true); } catch (e) { toast(e.message, true); } },
  segSet: (id, el) => { const g = el.dataset.seg; modal(mhd(`Change ${g} status · ${id}`, 'The change is kept in the client’s segment history and goes to the exchanges with the UCC upload for the effective date.') + `<div class="bd"><div class="form"><div class="fld"><label for="sg-st">New status</label><select class="in" id="sg-st">${['Active', 'Reactivated', 'Suspended', 'Dormant', 'Closed'].map(x => `<option>${x}</option>`).join('')}</select></div><div class="fld"><label for="sg-on">Effective from</label><input class="in" type="date" id="sg-on" value="${S.date}"></div><div class="fld full"><label for="sg-why">Reason</label><input class="in" id="sg-why" placeholder="e.g. Income proof for derivatives has lapsed"></div></div><div id="sg-err"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="sg-go">Save</button></div>`);
    $('sg-go').onclick = async () => { try { await post(`/v1/clients/${encodeURIComponent(id)}/segment-status`, { segment: g, status: $('sg-st').value, on: $('sg-on').value, reason: $('sg-why').value }); toast(`${g} is now ${$('sg-st').value}`); clientDrawer(id); } catch (e) { $('sg-err').innerHTML = errHTML(e); } }; },
  openLedger: id => { UI.ledgerU = id; A.go('ledger'); },
  sample: async (id, el) => { if (el) el.disabled = true; toast('Loading the sample trading day…'); try { const r = await post('/v1/dev/sample', { date: S.date }); toast(`Loaded ${r.files} sample files for ${fmtDate(S.date)}`); } catch (e) { toast(e.message, true); } render({ keep: true }); },
  sampleRun: async (id, el) => {
    if (el) el.disabled = true; const n = +id || 1;
    toast(n > 1 ? `Working ${n} trading days end to end…` : 'Working the day end to end…');
    try {
      const r = await post('/v1/dev/sample', { date: S.date, run: true, days: n });
      const last = r.days[r.days.length - 1];
      S.date = last.date; persist();
      modal(mhd(`${r.days.length} sample day${r.days.length > 1 ? 's' : ''} worked end to end`, 'Loaded, reconciled, breaks resolved, billed, derivatives settled, obligations settled, contract notes sent, day closed and positions rolled forward.') +
        `<div class="bd">${tbl([['Date'], ['Trades', 'r'], ['Breaks', 'r'], ['Bills', 'r'], ['Carried MTM', 'r'], ['Expiry', 'r'], ['Settled', 'r'], ['Notes', 'r'], ['Positions rolled', 'r']], r.days.map(x => `<tr><td class="n">${fmtDate(x.date)}</td><td class="r">${x.trades}</td><td class="r">${x.breaks}</td><td class="r">${x.bills}</td><td class="r">${x.carried_mtm}</td><td class="r">${x.expiry}</td><td class="r">${x.settled}</td><td class="r">${x.notes}</td><td class="r">${x.rolled_positions}</td></tr>`))}
        <p class="dim small" style="margin-top:10px">Now showing ${fmtDate(last.date)}. Client reports cover the whole period; the next sample day is ${fmtDate(last.next_day)}.</p></div><div class="ft"><button class="btn" data-act="crFor" data-id="BH1042">Open client reports</button><button class="btn pri" data-act="close">Done</button></div>`);
    } catch (e) { toast(e.message, true); }
    render({ keep: true });
  },
  gotoDate: id => { S.date = id; C.billResult = null; closeModal(); render(); },
  prevDay: () => A.gotoDate(prevBD(S.date)),
  nextDay: () => A.gotoDate(nextBD(S.date)),
  allotSample: () => act(() => post('/v1/dev/sample', { date: S.date, later: true }), 'Allotment file applied'),
  runRecon: () => act(() => post('/v1/recon/runs', { date: S.date }), r => `Reconciled: ${r.result.matched} matched, ${r.open} open breaks`),
  depEdit: id => depModal(id),
  depRelease: id => confirmModal('Deposit renewed, released or invoked · ' + id, 'Give the clearing corporation’s release reference, or the new deposit that replaces it.', `<div class="form"><div class="fld"><label for="dr-s">What happened</label><select class="in" id="dr-s">${['Renewed', 'Released', 'Invoked'].map(x => `<option>${x}</option>`).join('')}</select></div><div class="fld full"><label for="dr-n">Reference</label><input class="in" id="dr-n"></div></div>`, 'Save', () => act(() => post(`/v1/deposits/${id}/release`, { status: $('dr-s').value, note: $('dr-n').value }), 'Saved')),
  depBMC: async () => { const st = await get('/v1/settings'); const b = st.settings?.clearing_bmc || st.clearing_bmc || {}; confirmModal('Base minimum capital', 'What each clearing corporation requires the firm to keep, in rupees. Leave blank if it does not apply.', `<div class="form">${C.deposits.ccs.map((c, i) => `<div class="fld"><label for="bmc-${i}">${c}</label><input class="in mono" id="bmc-${i}" inputmode="decimal" value="${esc(b[c] || '')}"></div>`).join('')}</div>`, 'Save', () => { const m = {}; C.deposits.ccs.forEach((c, i) => { m[c] = $('bmc-' + i).value.trim().replace(/,/g, ''); }); return act(() => api('PUT', '/v1/settings', { clearing_bmc: m }), 'Saved'); }); },
  appOpen: id => appModal(id),
  appReject: id => { const r = ($('app-r')?.value || '').trim(); if (!r) { toast('Write the reason first'); $('app-r')?.focus(); return; } act(() => post(`/v1/applications/${id}/reject`, { reason: r }), 'Application rejected; the applicant was told'); },
  ciOpen: id => ciModal(id),
  ciUpload: () => $('ci-file').click(),
  ciReview: id => act(() => post(`/v1/circulars/${encodeURIComponent(id)}`, { kind: 'review', note: $('ci-note')?.value || '' }), 'Saved'),
  ciNA: id => { const n = ($('ci-note')?.value || '').trim(); if (!n) { toast('Write why it does not apply in the note'); $('ci-note')?.focus(); return; } act(() => post(`/v1/circulars/${encodeURIComponent(id)}`, { kind: 'not_applicable', note: n }), 'Marked not applicable'); },
  ciAdd: id => act(() => post(`/v1/circulars/${encodeURIComponent(id)}`, { kind: 'add_task', title: $('ci-title').value, role: $('ci-role').value, due: $('ci-due').value }), 'Action added'),
  ciTaskDone: (id, el) => confirmModal('Action done', 'Say what was done and where the evidence is; it stays with the circular.', '<div class="fld"><label for="ct-ev">Evidence</label><input class="in" id="ct-ev" placeholder="e.g. Risk policy v4 approved, board note 12"></div>', 'Mark done', () => act(() => post(`/v1/circulars/${encodeURIComponent(id)}`, { kind: 'done_task', task: el.dataset.t, evidence: $('ct-ev').value }), 'Action done')),
  ciTaskDrop: (id, el) => confirmModal('Action not needed', 'Say why it is not needed.', '<div class="fld"><label for="ct-ev">Reason</label><input class="in" id="ct-ev"></div>', 'Drop the action', () => act(() => post(`/v1/circulars/${encodeURIComponent(id)}`, { kind: 'drop_task', task: el.dataset.t, note: $('ct-ev').value }), 'Action dropped')),
  dutyDone: id => { const x = C.circulars.duties.find(y => y.id === id); confirmModal('Mark done · ' + esc(x.title), `For ${esc(x.period)}, due ${fmtDate(x.next_due)}. Keep: ${esc(x.evidence)}.`, '<div class="fld"><label for="du-ev">What was done and where the evidence is</label><input class="in" id="du-ev"></div>', 'Mark done', () => act(() => post(`/v1/duties/${id}/done`, { date: S.date, evidence: $('du-ev').value }), 'Recorded')); },
  insOpen: id => insModal(id),
  yeOpen: id => openDoc(`/v1/year-end/${C.yearend.fy}/${encodeURIComponent(id)}/document?date=${S.date}`),
  pYearEnd: fy => openDoc(`/v1/year-end/${fy}/${encodeURIComponent(UI.pAcct)}/document?date=${S.date}`),
  yeSend: id => act(() => post(`/v1/year-end/${C.yearend.fy}/send`, { date: S.date, uccs: [id] }), r => r.sent ? `Emailed ${id}` : 'Not sent: check the email address'),
  yeSendAll: () => confirmModal('Email year-end packs', `Tells every active client with an email that their FY ${C.yearend.fy} statement is ready in the portal.`, '', 'Email all', async () => { const r = await post(`/v1/year-end/${C.yearend.fy}/send`, { date: S.date }); closeModal(); toast(`${r.sent} emailed${r.failed ? `, ${r.failed} not sent` : ''}`); render({ keep: true }); }),
  waConsent: (id, el) => waConsentModal(id, el.dataset.on === '1'),
  waCampaign: ch => confirmModal('Ask clients for WhatsApp consent', `By ${ch}, to active clients not asked yet. Each gets a one-tap link to say yes or no.`, '', 'Send', async () => { const r = await post('/v1/whatsapp/consent-campaign', { channel: ch }); closeModal(); toast(`${r.invited} asked${r.skipped ? `, ${r.skipped} not sent` : ''}${r.remaining ? `, ${r.remaining} left for the next run` : ''}`); render({ keep: true }); }),
  insFind: () => act(() => post('/v1/insights/find', { date: S.date }), r => `${r.found} opportunit${r.found === 1 ? 'y' : 'ies'} found; nothing was sent`),
  insPolicies: () => { UI.apTab = 'pol'; A.go('autopilot'); },
  insSend: id => act(() => post(`/v1/insights/${encodeURIComponent(id)}`, { action: 'send' }), x => `Sent to ${x.ucc} by ${(x.channels || []).join(', ')}`),
  insCall: id => act(() => post(`/v1/insights/${encodeURIComponent(id)}`, { action: 'call', outcome: $('ic-o').value, text: $('ic-t').value }), 'Call logged'),
  insDismiss: id => { const t = ($('ic-t')?.value || '').trim(); if (!t) { toast('Write why in the note first'); $('ic-t')?.focus(); return; } act(() => post(`/v1/insights/${encodeURIComponent(id)}`, { action: 'dismiss', text: t }), 'Dismissed'); },
  pTender: id => { const q = parseInt($('pt-' + id)?.value || '0', 10); if (isNaN(q) || q < 0) { toast('Enter the number of shares'); return; } act(() => post(`/v1/portal/buyback/${encodeURIComponent(id)}/tender`, { ucc: UI.pAcct, qty: q, date: S.date }), q ? `${q} shares tendered` : 'Tender cancelled'); },
  brk: id => breakModal(id),
  pbNote: id => { const el = $('bnote'); if (!el) return; el.value = PBN[+id] || ''; el.focus(); const i = el.value.indexOf('___'); if (i >= 0) el.setSelectionRange(i, i + 3); },
  resolve: id => { const note = ($('bnote')?.value || '').trim(); if (!note) { toast('Write what you checked and fixed first'); $('bnote')?.focus(); return; } act(() => post(`/v1/recon/breaks/${encodeURIComponent(id)}/resolve`, { resolution: note }), 'Break resolved'); },
  ccm: id => act(() => post(`/v1/recon/breaks/${encodeURIComponent(id)}/ccm`, { reason: ($('bnote')?.value || '').trim() }), c => `Client code modified ${c.from} → ${c.to}. Rebill the day to move the charges.`),
  bill: async id => { try { C.billResult = await post('/v1/billing/runs', { date: S.date, rebill: id === '1' }); toast(`${C.billResult.posted.length} bills posted`); } catch (e) { C.billResult = { error: e }; toast('Billing did not run'); } render({ keep: true }); },
  settle: (key, el) => act(() => post('/v1/settlement/action', { date: S.date, key, action: el.dataset.a }), o => `${o.ucc} ${o.symbol}: ${o.status}`),
  settleReady: () => act(() => post('/v1/settlement/settle-ready', { date: S.date }), r => `${r.settled} lines settled`),
  cuspaRel: id => act(() => post(`/v1/cuspa/${id}/release`), 'Released to the client’s demat'),
  genNotes: () => act(() => post('/v1/notes/generate', { date: S.date }), r => `${r.generated} contract notes generated`),
  sendNotes: () => act(() => post('/v1/notes/send', { date: S.date }), r => `${r.sent} sent, ${r.bounced} bounced`),
  sendNote: id => act(() => post('/v1/notes/send', { date: S.date, ucc: id }), r => r.sent ? 'Sent' : 'Bounced: check the email address'),
  noteDoc: id => { const [u, p] = String(id).split('|'); openDoc(`/v1/notes/${S.date}/${u}/document${p ? '?part=' + p : ''}`); },
  newClient: () => { UI.ob = { step: 0, data: { type: 'Individual', kyc_route: 'KRA fetch (existing KYC)', depository: 'CDSL', segments: ['CM'], income: '5-10 L', ras: 'Quarterly', nominee: 'Opted in' }, err: {} }; obModal(); },
  obBack: () => { UI.ob.step--; UI.ob.err = {}; obModal(); },
  obNext: async () => {
    if (!obValidate()) { obModal(); return; }
    if (UI.ob.step < 4) { UI.ob.step++; obModal(); return; }
    const d = UI.ob.data;
    const body = { name: d.name, type: d.type, pan: d.pan, mobile: d.mobile, email: d.email, city: d.city, kyc_route: d.kyc_route, segments: d.segments, income: d.income, ras: d.ras,
      bank: { bank: d.bank, ifsc: d.ifsc, account: d.account }, demat: { depository: d.depository, id: d.demat_id || '' }, nominee: d.nominee, nominee_name: d.nominee_name || '',
      income_proof: !!d.income_proof, risk_disclosure: !!d.risk_disclosure, whatsapp: !!d.whatsapp };
    try { const c = await post('/v1/clients', body); const lead = UI.ob.lead; UI.ob = null; closeModal(); if (lead) await post(`/v1/leads/${lead}/act`, { kind: 'stage', stage: 'Converted', ucc: c.ucc }).catch(() => {}); UI.clientStage = 'KYC verification'; toast(`Application ${c.ucc} created${lead ? ' from lead ' + lead : ''}`); render({ keep: true }); }
    catch (e) { UI.ob.err = { _server: e }; obModal(); }
  },
  advance: id => act(() => post(`/v1/clients/${id}/advance`), c => `${c.ucc} moved to ${c.status}`),
  kill: (id, el) => {
    const on = el.dataset.b === '1';
    if (!on) { act(() => post(`/v1/clients/${id}/block`, { blocked: false }), id + ' unblocked'); return; }
    confirmModal('Kill switch · ' + id, 'Sets the OMS limit to zero until unblocked.', `<div class="fld"><label for="ks-r">Reason</label><input class="in" id="ks-r" placeholder="e.g. Margin breach unresolved after call"></div>`, 'Block client', () => act(() => post(`/v1/clients/${id}/block`, { blocked: true, reason: $('ks-r').value }), id + ' blocked'), true);
  },
  override: id => confirmModal((has('Branch staff', 'Authorised person') && !has('Ops maker') ? 'Request extra limit · ' : 'Limit override · ') + id, 'Temporary extra limit for ' + fmtDate(S.date) + (has('Branch staff', 'Authorised person') && !has('Ops maker') ? '. Head-office risk approves or rejects it; you see the decision here.' : '. A checker or risk manager who is not you must approve it.'),
    `<div class="form"><div class="fld"><label for="ov-amt">Amount (₹)</label><input class="in" id="ov-amt" type="number" min="1000" step="1000" value="50000"></div><div class="fld"><label for="ov-r">Reason</label><select class="in" id="ov-r"><option>Funds in transit (UTR shared)</option><option>Pledge request pending at depository</option><option>Relationship manager guarantee</option><option>Hedged position, margin benefit pending</option></select></div></div>`,
    'Submit for approval', () => act(() => post('/v1/rms/overrides', { date: S.date, ucc: id, amount: $('ov-amt').value, reason: $('ov-r').value }), o => o.by_branch ? 'Request sent to head-office risk' : 'Override sent for approval')),
  ovDecide: (id, el) => act(() => post(`/v1/rms/overrides/${id}/decide`, { date: S.date, approve: el.dataset.a === '1' }), o => `Override ${o.status.toLowerCase()}`),
  sq: id => confirmModal('Square off ' + id, 'Sends close-out orders for every open derivatives position.', S.me?.dev ? '<div class="note">Development server: a simulated OMS fills the orders at the last price. Rebill afterwards to bill the closing trades.</div>' : '', 'Square off', () => act(() => post('/v1/rms/squareoff', { date: S.date, uccs: [id] }), r => `${r.instructions.length} orders sent${r.filled ? ', ' + r.filled + ' filled' : ''}`), true),
  sqAll: () => confirmModal('Square off all breaches', 'Sends close-out orders for every breached client with open derivatives positions.', S.me?.dev ? '<div class="note">Development server: a simulated OMS fills the orders at the last price. Rebill afterwards to bill the closing trades.</div>' : '', 'Square off all', () => act(() => post('/v1/rms/squareoff', { date: S.date, uccs: [] }), r => `${r.instructions.length} orders sent${r.filled ? ', ' + r.filled + ' filled' : ''}`), true),
  penalties: () => confirmModal('Post margin penalties', 'Debits each short client with their slab penalty, payable to the clearing corporation.', '', 'Post penalties', () => act(() => post('/v1/margin/penalties', { date: S.date }), r => `${r.posted} penalties posted`)),
  pledge: (id, el) => { if (el.dataset.a === '1') { act(() => post(`/v1/pledges/${encodeURIComponent(id)}/decide`, { accept: true }), 'Pledge accepted; collateral updated'); return; }
    confirmModal('Reject pledge ' + id, '', `<div class="fld"><label for="pl-r">Reason</label><input class="in" id="pl-r" placeholder="e.g. Scrip not on approved collateral list"></div>`, 'Reject', () => act(() => post(`/v1/pledges/${encodeURIComponent(id)}/decide`, { accept: false, reason: $('pl-r').value }), 'Pledge rejected'), true); },
  journal: async () => {
    await clientCache();
    confirmModal('Post journal entry', 'Manual entries are recorded with your name. A checker can reverse them.', `<div class="form"><div class="fld"><label for="jn-u-q">Client</label>${cpick('jn-u', UI.ledgerU)}</div><div class="fld"><label for="jn-t">Type</label><select class="in" id="jn-t"><option value="RECEIPT">Receipt into client bank (credit client)</option><option value="PAYMENT">Payment to client (debit client)</option><option value="INTEREST">Delayed payment interest (debit client)</option></select></div><div class="fld"><label for="jn-a">Amount (₹)</label><input class="in" id="jn-a" inputmode="decimal" value="10000"></div><div class="fld"><label for="jn-r">Reference</label><input class="in" id="jn-r" placeholder="UTR or voucher no."></div><div class="fld full"><label for="jn-n">Narration</label><input class="in" id="jn-n" placeholder="What this entry is for"></div></div>`,
      'Post entry', async () => {
        if (!$('jn-u').value) throw new Error('Choose the client');
        const u = $('jn-u').value, t = $('jn-t').value, a = $('jn-a').value.trim(), n = $('jn-n').value.trim(), ref = $('jn-r').value.trim();
        if (!n) throw new Error('Add a narration');
        const client = 'CLIENT:' + u;
        const p = t === 'RECEIPT' ? [{ account: 'BANK:CLIENT', debit: a }, { account: client, credit: a, memo: n }] : t === 'PAYMENT' ? [{ account: client, debit: a, memo: n }, { account: 'BANK:CLIENT', credit: a }] : [{ account: client, debit: a, memo: n }, { account: 'INCOME:INTEREST', credit: a }];
        await act(() => post('/v1/journal', { key: 'MAN:' + (ref || Date.now()) + ':' + u, value_date: S.date, narration: n + (ref ? ' · ' + ref : ''), postings: p }), 'Entry posted');
        UI.ledgerU = u;
      });
  },
  reverse: id => confirmModal('Reverse ' + id, 'Posts the mirror entry. The original stays in the journal.', `<div class="fld"><label for="rv-r">Reason</label><input class="in" id="rv-r" placeholder="e.g. Credited to the wrong client"></div>`, 'Reverse entry', () => act(() => post(`/v1/journal/${id}/reverse`, { reason: $('rv-r').value }), 'Entry reversed'), true),
  bankReturn: id => confirmModal('Return credit', 'Refunds the amount to the remitting account and clears it from suspense.', `<div class="fld"><label for="br-n">Note</label><input class="in" id="br-n" value="Remitting account not registered to the client"></div>`, 'Return to remitter', () => act(() => post(`/v1/bank/${encodeURIComponent(id)}/return`, { note: $('br-n').value }), 'Refund posted'), true),
  identify: async id => {
    await clientCache();
    confirmModal('Identify credit', 'Only when the bank confirms the remitting account belongs to the client. Otherwise return the funds.', `<div class="fld"><label for="id-u-q">Client</label>${cpick('id-u', '')}</div><label class="check"><input type="checkbox" id="id-ok"> The bank confirmed in writing that the remitting account belongs to this client</label>`,
      'Credit client', () => { if (!$('id-u').value) throw new Error('Choose the client'); return act(() => post(`/v1/bank/${encodeURIComponent(id)}/identify`, { ucc: $('id-u').value, bank_confirmed: $('id-ok').checked }), 'Receipt posted'); });
  },
  upstream: () => confirmModal('Upstream client funds', 'Moves money from the client bank account to the clearing corporation as collateral.', `<div class="form"><div class="fld"><label for="up-a">Amount (₹)</label><input class="in" id="up-a" inputmode="decimal"></div><div class="fld"><label for="up-r">Transfer reference</label><input class="in" id="up-r"></div></div>`, 'Record transfer', () => act(() => post('/v1/bank/upstream', { date: S.date, amount: $('up-a').value, ref: $('up-r').value }), 'Transfer recorded')),
  runRas: () => { const d = C.ras, due = d.rows.filter(r => r.settles_on === d.monthly_date && !r.settled && num(r.payout) > 0);
    confirmModal('Run account settlement for ' + fmtDate(d.monthly_date), `${due.length} clients · ${inr(sum(due, r => num(r.payout)), 2)}`, '<div class="note">Each payout is a ledger entry against the client’s registered bank account. Download the bank payout file afterwards.</div>', `Post ${due.length} payouts`, () => act(() => post('/v1/ras/run', { date: S.date, settle_date: d.monthly_date }), b => `${b.items.length} payouts posted`)); },
  rstep: (code, el) => act(() => post(`/v1/reports/${code}/${el.dataset.a}`, { date: S.date }), r => `${r.name}: ${r.status.toLowerCase()} (${r.rows} rows)`),
  rup: code => confirmModal('Mark uploaded', 'Record the acknowledgement number from the exchange portal.', `<div class="fld"><label for="rp-a">Acknowledgement number</label><input class="in" id="rp-a"></div>`, 'Mark uploaded', () => act(() => post(`/v1/reports/${code}/upload`, { date: S.date, ack: $('rp-a').value }), 'Marked uploaded')),
  alert: (id, el) => { const a = el.dataset.a; if (a === 'review') { act(() => post(`/v1/alerts/${encodeURIComponent(id)}/review`, {}), 'Alert under review'); return; }
    confirmModal(a === 'str' ? 'File STR' : 'Close alert', a === 'str' ? 'Records that a suspicious transaction report was filed with FIU-IND.' : 'Closing needs the rationale for the file.', `<div class="fld"><label for="al-n">${a === 'str' ? 'STR reference and summary' : 'Rationale'}</label><textarea class="in" id="al-n"></textarea></div>`, a === 'str' ? 'Record STR' : 'Close alert', () => act(() => post(`/v1/alerts/${encodeURIComponent(id)}/${a}`, { note: $('al-n').value }), a === 'str' ? 'STR recorded' : 'Alert closed'), a === 'str'); },
  rules: () => act(() => post('/v1/surveillance/run', { date: S.date }), r => `${r.raised} new alert${r.raised === 1 ? '' : 's'}`),
  allocBatch: kind => confirmModal('Prepare the ' + kind.toLowerCase() + ' batch', 'Brings each client’s allocation with NSE Clearing to Ledgerline’s figure, only where it differs, reductions first. ' + (kind === 'End of day' ? 'Sent as end-of-day (E): applied at the end-of-day release.' : 'Sent as immediate (I).') + ' A second person approves it before it is sent.', '', 'Prepare', () => act(() => post('/v1/allocation/batch', { date: S.date, kind }), ms => `${ms.reduce((a, m) => a + m.records.length, 0)} clients prepared; waiting for approval`)),
  allocCheck: () => act(() => post('/v1/allocation/check', {}), r => `${r.results} result${r.results === 1 ? '' : 's'} applied`),
  allocDecide: (id, el) => el.dataset.a === '1' ? confirmModal('Approve and send ' + id, 'The request goes to NSE Clearing now (if sending is on). Results arrive within a few minutes.', '', 'Approve and send', () => act(() => post(`/v1/allocation/${encodeURIComponent(id)}/decide`, { approve: true }), m => 'Request ' + m.status.toLowerCase())) :
    confirmModal('Reject ' + id, 'Nothing is sent. Say why.', '<div class="fld"><label for="ad-n">Reason</label><input class="in" id="ad-n"></div>', 'Reject', () => act(() => post(`/v1/allocation/${encodeURIComponent(id)}/decide`, { approve: false, note: $('ad-n').value }), 'Rejected'), true),
  allocShow: id => { const m = C.allocation.messages.find(x => x.id === id); if (!m) return;
    modal(mhd('Request ' + esc(id), `${esc(m.kind)} · ${m.request_type === 'E' ? 'applied at end of day' : 'immediate'} · ${esc(m.status)}${m.ack ? ' · ' + esc(m.ack) : ''}`) + `<div class="bd">${tbl([['Client'], ['Segment'], ['Was', 'r'], ['Asked for', 'r'], ['Direction'], ['Result']], m.records.map(r => `<tr><td class="mono">${esc(r.ucc || 'Firm (proprietary)')}</td><td>${esc(r.segment)}</td><td class="r">${inr(r.previous, 2)}</td><td class="r">${inr(r.amount, 2)}</td><td>${r.action === 'D' ? 'Down' : 'Up'}</td><td>${r.code ? pill(r.result, r.code === '0100' ? 'good' : r.code === '0104' ? 'warn' : 'bad') : '<span class="dim">—</span>'}</td></tr>`), { tall: true })}</div>`); },
  allocLevel: () => confirmModal('Confirm a client’s allocation', 'Record the allocation NSE Clearing shows for the client (N-MASS or its reply), for example after a partly accepted request. Money waiting for the client is then sent on top of it.', `<div class="form"><div class="fld"><label for="al-u-q">Client</label>${cpick('al-u', '')}</div><div class="fld"><label for="al-s">Segment</label><select class="in" id="al-s"><option>CM</option><option>FO</option><option>CD</option><option>SLB</option></select></div><div class="fld"><label for="al-a">Allocated (₹)</label><input class="in mono" id="al-a" inputmode="decimal"></div><div class="fld full"><label for="al-n">Where the figure comes from</label><input class="in" id="al-n" placeholder="N-MASS screen at 11:40"></div></div>`, 'Confirm', () => act(() => post('/v1/allocation/level', { segment: $('al-s').value, ucc: $('al-u').value, amount: $('al-a').value.trim(), note: $('al-n').value }), 'Level confirmed')),
  saveSettings: () => {
    const s = { ...C.settings.settings, firm: $('st-firm').value, sebi_reg: $('st-sebi').value, compliance_email: $('st-comp').value, warn_util_pct: parseInt($('st-warn').value, 10), ras_buffer_pct: parseInt($('st-buf').value, 10), ras_monthly: $('st-rasm').value, ras_quarterly: $('st-rasq').value,
      alloc_api_live: !!$('st-alive')?.checked, alloc_auto_push: !!$('st-apush')?.checked, alloc_push_segment: $('st-aseg')?.value || 'CM', two_factor: $('st-2fa').value, ledgy_ai: !!$('st-lai')?.checked, bill_ai: !!$('st-bai')?.checked, inactive_months: parseInt($('st-inm').value, 10) || 24, session_idle_minutes: parseInt($('st-idle').value, 10) || 30, snapshot_times: $('st-snap').value.split(',').map(x => x.trim()).filter(Boolean), holidays: $('st-hol').value.split(',').map(x => x.trim()).filter(Boolean), penalty_pass_through: $('st-pen').checked,
      interest_pct_pa: $('st-int').value.trim(), interest_grace_days: parseInt($('st-grace').value, 10) || 0, interest_minimum: $('st-imin').value.trim(), gst_pct: $('st-gst').value.trim(), interest_exempt_tags: $('st-iex').value.split(',').map(x => x.trim().toUpperCase()).filter(Boolean), interest_posting: $('st-ipost').value, interest_daily_from: $('st-idfrom').value, debit_block_days: parseInt($('st-dbd').value, 10) || 7, locked_up_to: $('st-lock').value, firm_pan: ($('st-fpan')?.value || '').trim().toUpperCase(), clearing_member_pan: ($('st-cmpan')?.value || '').trim().toUpperCase(), firm_gstin: $('st-gstin').value.trim().toUpperCase(), firm_state: $('st-state').value.trim(), cheque_bounce_charge: $('st-bnc').value.trim(), tds_commission_pct: $('st-tds').value.trim(), referral_pct: $('st-refp').value.trim(), referral_months: parseInt($('st-refm').value, 10) || 12, payout_debit_account: $('st-pda').value.trim(), gateway_secret: $('st-gws').value, whatsapp_secret: $('st-was').value, management_emails: $('st-mgmt').value.split(',').map(x => x.trim()).filter(Boolean), lead_incentive: $('st-linc').value.trim(), auto_lock_days: parseInt($('st-alock').value, 10) || 0, ...clearingValues() };
    act(() => api('PUT', '/v1/settings', s), 'Settings saved').then(loadMe);
  },
  eod: async () => {
    let st; try { st = await get(`/v1/eod?date=${S.date}`); } catch (e) { toast(e.message, true); return; }
    const chk = can => (p) => `<div class="checks">${p.checks.map(c => `<div><span>${esc(c.name)}<br><span class="dim" style="font-size:12px">${esc(c.detail)}</span></span>${c.ok ? pill('Done', 'good') : `<button class="btn sm" data-act="go" data-id="${c.view}">Open</button>`}</div>`).join('')}</div>`;
    const checker = has('Ops checker');
    const part = p => {
      const ok = p.checks.every(c => c.ok);
      const state = !p.needed ? pill('Nothing to close', 'nt') : p.closed ? pill('Closed ' + p.closed_at, 'good') : pill('Open', 'warn');
      let body;
      if (!p.needed) body = `<div class="dim small">No ${esc(p.name.toLowerCase())} trades and nothing carried on ${fmtDate(S.date)}.</div>`;
      else if (p.closed) body = `<div class="small">Closed by ${esc(p.closed_by)}${p.next_day ? `; ${p.rolled_positions} open position${p.rolled_positions === 1 ? '' : 's'} carried to ${fmtDate(p.next_day)}` : ''}. A backup followed.</div>${(p.exceptions || []).length ? `<div class="note warn" style="margin-top:8px"><b>Closed with exceptions</b><ul style="margin:6px 0 0;padding-left:18px">${p.exceptions.map(x => `<li>${esc(x)}</li>`).join('')}</ul></div>` : ''}`;
      else body = `${p.previous_open ? `<div class="note warn">${esc(p.previous_open)}</div>` : ''}${chk()(p)}${checker ? `${ok ? '' : `<div class="fld"><label for="eod-n-${p.id}">Note for closing with exceptions</label><textarea class="in" id="eod-n-${p.id}"></textarea></div>`}<div class="tools" style="margin-top:8px"><button class="btn ${ok ? 'pri' : 'bad'}" data-act="eodPart" data-id="${p.id}">${ok ? 'Close ' : 'Close with exceptions: '}${esc(p.name.toLowerCase())}</button></div>` : ''}`;
      return `<section class="eod-part ${p.needed && !p.closed ? 'open' : ''}"><div class="eod-ph"><div><b>${esc(p.name)}</b><span class="dim small"> · ${esc(p.about)}</span></div>${state}</div>${body}</section>`;
    };
    const sub = st.closed ? `Closed at ${esc(st.closed_at)} by ${esc(st.closed_by)}` : 'Equity closes in the evening; commodity after the MCX session, usually the next morning. Each close carries its own positions forward and is followed by a backup.';
    modal(mhd('EOD · ' + fmtDate(S.date), sub) + `<div class="bd">${(st.parts || []).map(part).join('')}${checker || st.closed ? '' : '<div class="note">Closing needs an Ops checker.</div>'}</div><div class="ft"><button class="btn" data-act="close">Close</button></div>`, true);
  },
  eodPart: async id => {
    const p = { equity: 'Equity', commodity: 'Commodity' }[id] || id;
    try {
      const r = await post('/v1/eod/close', { date: S.date, part: id, note: $('eod-n-' + id)?.value || '' });
      const x = (r.parts || []).find(q => q.id === id) || {};
      closeModal(); toast(`${p} closed${x.next_day ? `; ${x.rolled_positions} positions carried to ${fmtDate(x.next_day)}` : ''}${r.closed ? '. The day is closed.' : ''}`);
      await render({ keep: true }); await refreshCounts?.();
    } catch (e) { toast(e.message, true); }
  },
};

/* ============================== events ============================== */
function bindPage() {
  const drop = $('drop'), file = $('file');
  if (drop) {
    file.onchange = () => file.files[0] && upload(file.files[0]);
    ['dragenter', 'dragover'].forEach(t => drop.addEventListener(t, e => { e.preventDefault(); drop.classList.add('over'); }));
    ['dragleave', 'drop'].forEach(t => drop.addEventListener(t, e => { e.preventDefault(); drop.classList.remove('over'); }));
    drop.addEventListener('drop', e => e.dataTransfer.files[0] && upload(e.dataTransfer.files[0]));
  }
  if (S.view === 'charges') calc();
  if (S.view === 'statements' && !UI.crRes && !UI.crBusy && !UI.crErr && UI.cr.ucc) crRun();
}
document.addEventListener('submit', e => {
  if (e.target.id !== 'loginf') return;
  e.preventDefault();
  if (loginState.otp) doLogin(loginState.user, loginPw, $('li-otp').value); else doLogin($('li-user').value.trim(), $('li-pw').value, '');
});
document.addEventListener('focusin', e => { const el = e.target; if (el.classList?.contains('cp-q')) { el.select(); cpOpen(el.dataset.cp); } });
document.addEventListener('click', e => {
  document.querySelectorAll('.cp-res:not([hidden])').forEach(r => { if (!r.parentElement.contains(e.target)) cpClose(r.id.slice(0, -4)); });
  const um = $('umenu'); if (um && !um.hidden && !e.target.closest('.umenu')) um.hidden = true;
  const a = e.target.closest('[data-act]');
  if (!a) { const r = $('gres'); if (r && !e.target.closest('.srch')) r.hidden = true; return; }
  const fn = A[a.dataset.act]; if (!fn) return;
  if (a.tagName === 'A' || a.tagName === 'BUTTON') e.preventDefault();
  e.stopPropagation(); fn(a.dataset.id, a, e);
});
document.addEventListener('input', e => {
  const el = e.target;
  if (el.classList.contains('cp-q')) { cpOpen(el.dataset.cp); return; }
  if (el.id === 'gsearch') { UI.search = el.value; searchClients(el.value); return; }
  if (el.dataset.ui) { UI[el.dataset.ui] = el.value; if (el.dataset.part) { const el2 = $(el.dataset.part); if (el2) el2.innerHTML = P[el.dataset.part](C[S.view]); } return; }
  if (el.dataset.calc) { UI.calc[el.dataset.calc] = el.value; clearTimeout(calcT); calcT = setTimeout(calc, 250); return; }
  if (el.dataset.ob) { UI.ob.data[el.dataset.ob] = el.value; }
});
document.addEventListener('submit', async e => {
  const f = e.target;
  if (f.id === 'pof') { e.preventDefault(); try { const r = await post('/v1/payouts', { date: S.date, ucc: UI.pAcct, amount: $('po-amt').value.replace(/,/g, '') }); toast(`Payout ${r.id} requested for ${inr(r.amount, 2)}`); render({ keep: true }); } catch (err) { $('po-err').innerHTML = errHTML(err); } }
  if (f.id === 'exf') { e.preventDefault(); try { await post('/v1/external-trades', { ucc: UI.pAcct, date: $('ex-d').value, symbol: $('ex-s').value, isin: $('ex-i').value, side: $('ex-side').value, qty: +$('ex-q').value, price: $('ex-p').value, broker: $('ex-b').value }); toast('Trade added'); render({ keep: true }); } catch (err) { $('ex-err').innerHTML = errHTML(err); } }
});
document.addEventListener('change', e => {
  const el = e.target;
  if (el.id === 'date' && el.value) { S.date = el.value; C.billResult = null; render(); return; }
  if (el.dataset.urole) { const id = el.dataset.urole, u = C.settings.users.find(x => x.id === id), role = el.value;
    let branch = u.branch || '', partner = u.partner || '';
    if (role === 'Branch staff' && !branch) branch = (prompt('Branch for ' + id) || '').trim();
    if (role === 'Authorised person' && !partner) partner = (prompt('Partner code for ' + id) || '').trim().toUpperCase();
    act(() => api('PUT', `/v1/users/${encodeURIComponent(id)}`, { id, name: u.name, role, active: u.active, branch, partner }), `${id} is now ${role}`).catch(() => render({ keep: true })); return; }
  if (el.id === 'lg-days') { UI.lgDays = +el.value; UI.lgSel.clear(); render({ keep: true }); return; }
  if (el.id === 'live-auto') { UI.liveAuto = el.checked; return; }
  if (el.id === 'p-acct') { UI.pAcct = el.value; render(); return; }
  if (el.id === 'ye-fy') { UI.yeFY = el.value; render({ keep: true }); return; }
  if (el.id === 'ci-file') { const f = el; if (!f.files.length) return; const fd = new FormData(); fd.append('files', f.files[0]); api('POST', `/v1/inbox?date=${S.date}`, fd, true).then(r => { const x = r.files[0] || {}; toast(x.status === 'Loaded' ? `${x.rows} circular${x.rows === 1 ? '' : 's'} loaded and sorted` : `${x.status}: ${x.message || ''}`); render({ keep: true }); }).catch(e => toast(e.message, true)); return; }
  if (el.id === 'pins-off') { act(() => post('/v1/portal/insights/prefs', { ucc: UI.pAcct, off: !el.checked }), el.checked ? 'Insights turned on' : 'Insights turned off').catch(() => render({ keep: true })); return; }
  if (el.id === 'crm-stage') { UI.crmStage = el.value; $('page').innerHTML = groupBar('crm') + V.crm.html(C.crm); bindPage(); return; }
  if (el.id === 'ftype') { UI.fileType = el.value; $('page').innerHTML = groupBar('files') + V.files.html(C.files); bindPage(); return; }
  if (el.id === 'cr-from' || el.id === 'cr-to') { UI.cr.preset = 'custom'; crSync(); document.querySelectorAll('[data-act=crPreset]').forEach(b => b.classList.toggle('on', b.dataset.id === 'custom')); return; }
  if (el.id === 'bkPlan') { UI.bkPlan = el.value; rerenderPart('bk-t'); return; }
  if (el.id === 'clientTag') { UI.clientTag = el.value; rerenderPart('clients-t'); return; }
  if (el.id === 'ledgerU') { UI.ledgerU = el.value; render({ keep: true }); return; }
  if (el.dataset.calc) { UI.calc[el.dataset.calc] = el.value; if (el.dataset.calc === 'bucket') { $('page').innerHTML = groupBar('charges') + V.charges.html(); bindPage(); } else calc(); return; }
  if (el.dataset.ob && el.tagName === 'SELECT') { UI.ob.data[el.dataset.ob] = el.value; if (el.dataset.ob === 'nominee') obModal(); return; }
  if (el.dataset.obseg) { const d = UI.ob.data; d.segments = el.checked ? [...new Set([...d.segments, el.dataset.obseg])] : d.segments.filter(s => s !== el.dataset.obseg); return; }
  if (el.dataset.obchk) { UI.ob.data[el.dataset.obchk] = el.checked; }
});
document.addEventListener('keydown', e => {
  if (e.target.classList?.contains('cp-q')) {
    const id = e.target.dataset.cp, r = $(id + '-res');
    if (r && !r.hidden) {
      const items = [...r.querySelectorAll('button')]; let i = items.findIndex(b => b.classList.contains('hl'));
      if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); if (items.length) { items[i]?.classList.remove('hl'); i = (i + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length; items[i].classList.add('hl'); items[i].scrollIntoView({ block: 'nearest' }); } return; }
      if (e.key === 'Enter') { e.preventDefault(); if (items[i]) cpSet(id, items[i].dataset.v); return; }
      if (e.key === 'Escape') { e.stopPropagation(); cpClose(id); return; }
      if (e.key === 'Tab') cpClose(id);
    }
  }
  if (e.key === 'Escape') { if (!modalLocked) closeModal(); const r = $('gres'); if (r) r.hidden = true; }
  if (e.key === '/' && !/INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName)) { e.preventDefault(); $('gsearch')?.focus(); }
});
let searchCache = { date: '', rows: [] };
async function searchClients(q) {
  const r = $('gres'); q = q.trim().toLowerCase(); if (!q) { r.hidden = true; return; }
  if (searchCache.date !== S.date || !searchCache.rows.length) { try { searchCache = { date: S.date, rows: await get(`/v1/clients?date=${S.date}`) }; } catch (e) { return; } }
  const m = searchCache.rows.filter(c => (c.name + ' ' + c.ucc + ' ' + c.pan + ' ' + (c.tags || []).join(' ')).toLowerCase().includes(q)).slice(0, 8);
  r.innerHTML = m.length ? m.map(c => `<button data-act="client" data-id="${c.ucc}"><span>${esc(c.name)}</span><span class="mono dim">${c.ucc}</span></button>`).join('') : '<div class="empty">No client matches</div>';
  r.hidden = false;
}

/* ---------- dispatch register and digital signature ---------- */
Object.assign(UI, { dspTab: '', dspQ: '', dspKind: '', dspFrom: '', dspTo: '' });
const DSP_ST = { Sent: 'good', 'Physical sent': 'good', 'Physical pending': 'warn', Held: 'warn', Bounced: 'bad', Failed: 'bad' };
V.dispatch = {
  load: () => { const f = UI.dspFrom || addDaysJS(S.date, -30), t = UI.dspTo || S.date; return get(`/v1/dispatch?from=${f}&to=${t}${UI.dspKind ? '&kind=' + UI.dspKind : ''}`); },
  html: d => {
    const c = d.by_status, sg = d.signing, mk = has('Ops maker', 'Ops checker'), from = UI.dspFrom || addDaysJS(S.date, -30), to = UI.dspTo || S.date;
    const sigTxt = sg.mode === 'off' ? 'No certificate' : sg.test ? 'Test certificate' : sg.ready ? esc(sg.info.name) : 'Not usable';
    return ph('Dispatch register', 'Every contract note and statement sent to clients: the signed PDF exactly as sent with its SHA-256 fingerprint, where and when it went, and what happened to it. SEBI treats this log as proof of delivery. A bounced email needs a printed copy by courier; clients who chose paper get one every time. Keep the register and the documents for the retention period.',
      `${mk ? '<button class="btn" data-act="dspRun">Send statements</button><input type="file" id="dsp-bfile" accept=".csv,text/csv" hidden><button class="btn" data-act="dspBounces">Load bounce report</button>' : ''}<button class="btn" data-act="dspCSV">Download register</button>${has('Compliance officer', 'Ops checker') ? '<button class="btn" data-act="sigOpen">Digital signature</button>' : ''}`) +
    (!sg.ready || sg.test ? `<div class="note ${sg.test ? 'warn' : 'bad'}">${sg.test ? 'This development server signs with a <b>test certificate</b>. Documents are marked as test copies and are not valid for clients.' : `<b>Documents are being held:</b> ${esc(sg.problem || 'no digital signature is set up')}. Contract notes and statements are never sent unsigned.`} ${has('Admin') ? '<button class="linkish" data-act="sigOpen">Set up the firm’s certificate</button>' : 'Ask an Admin to set up the firm’s certificate.'}</div>` : '') +
    (d.mail === 'none' ? `<div class="note bad"><b>No mail server is set up:</b> emailed documents are held until one is. ${has('Admin') ? '<button class="linkish" data-act="go" data-id="connections">Set it up in Connections</button>' : 'Ask an Admin to set it up in Connections.'}</div>` : d.mail === 'simulated' ? '<div class="note">Development server: emails are simulated; addresses ending in .invalid bounce.</div>' : '') +
    `<div class="kpis">${kpi('Delivered', nf((c.Sent || 0) + (c['Physical sent'] || 0)), 'email and courier', 'good')}${kpi('Bounced', nf(c.Bounced || 0), 'printed copy needed', c.Bounced ? 'bad' : '')}${kpi('Paper due', nf(c['Physical pending'] || 0), 'clients who chose paper', c['Physical pending'] ? 'warn' : '')}${kpi('Held', nf((c.Held || 0) + (c.Failed || 0)), 'not signed or not sent', c.Held || c.Failed ? 'bad' : '')}${kpi('Signature', sg.mode === 'off' ? 'None' : sg.test ? 'Test' : sg.ready ? 'Ready' : 'Problem', sg.ready && !sg.test ? `${esc(sg.info.name)} · ${sg.expires_in_days} days left` : sigTxt, !sg.ready ? 'bad' : sg.test || sg.expires_in_days < 30 ? 'warn' : 'good')}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('dspTab', [['', 'All'], ['Sent', 'Emailed'], ['Bounced', `Bounced · ${c.Bounced || 0}`], ['Physical pending', `Paper due · ${c['Physical pending'] || 0}`], ['Held', `Held · ${c.Held || 0}`], ['Failed', `Failed · ${c.Failed || 0}`], ['Physical sent', 'By courier']], 'dsp-t')}
      <select class="in" id="dsp-kind"><option value="">Every document</option>${Object.entries(d.kinds).map(([k, v]) => `<option value="${k}" ${UI.dspKind === k ? 'selected' : ''}>${esc(v)}</option>`).join('')}</select>
      <input class="in" type="date" id="dsp-from" value="${from}"><input class="in" type="date" id="dsp-to" value="${to}">
      <input class="in" data-ui="dspQ" data-part="dsp-t" placeholder="Filter client, UCC, reference or email" value="${esc(UI.dspQ)}"></div></div><div id="dsp-t">${P['dsp-t'](d)}</div></div>`;
  }
};
P['dsp-t'] = d => {
  const q = UI.dspQ.trim().toLowerCase();
  const rows = d.rows.filter(x => (!UI.dspTab || x.status === UI.dspTab) && (!q || (x.ucc + ' ' + x.name + ' ' + x.ref + ' ' + x.to + ' ' + x.id).toLowerCase().includes(q)));
  return tbl([['Entry'], ['Document'], ['Client'], ['Sent to'], ['Status'], ['Signed'], ['']], rows.slice(0, 500).map(x => { const f = x.files[0];
    return `<tr><td class="n"><button class="linkish mono" data-act="dspOpen" data-id="${x.id}">${x.id}</button><div class="dim small">sent ${fmtDate(x.created.slice(0, 10))} ${x.created.slice(11, 16)}${x.attempt > 1 ? ' · attempt ' + x.attempt : ''}</div></td><td><b>${esc(d.kinds[x.kind] || x.kind)}</b><div class="dim small mono">${esc(x.ref)}${x.date && x.date !== x.ref ? ' · ' + fmtDate(x.date) : ''}</div></td><td><button class="linkish mono" data-act="client" data-id="${x.ucc}">${x.ucc}</button> ${esc(x.name)}</td><td class="small">${x.channel === 'physical' ? 'Paper (client’s choice)' : esc(x.to || '—')}${x.courier ? `<div class="dim">${esc(x.courier)}</div>` : ''}</td><td>${pill(x.status + (x.simulated ? ' (simulated)' : ''), DSP_ST[x.status] || 'nt')}${x.reason ? `<div class="dim small wrap">${esc(x.reason)}</div>` : ''}</td><td class="small">${f ? `${pill('Signed', 'good')}<div class="dim mono" title="SHA-256 of the file">${f.sha256.slice(0, 12)}…</div>` : '<span class="dim">—</span>'}</td><td>${f ? `<button class="btn sm" data-act="dl" data-id="/v1/dispatch/${x.id}/file" data-name="${esc(f.name)}">PDF</button>` : ''}</td></tr>`; }),
    { tall: true, empty: d.rows.length ? 'Nothing matches.' : 'Nothing sent in this period. Contract notes are recorded here when they are sent; statements when they are run.' }) + (rows.length > 500 ? `<div class="pad dim small">Showing 500 of ${nf(rows.length)}. Narrow the dates or download the register.</div>` : '');
};
async function dspModal(id) {
  let x = C.dispatch?.rows?.find(r => r.id === id);
  if (!x) { try { x = (await get('/v1/dispatch')).rows.find(r => r.id === id); } catch (e) { toast(e.message, true); return; } }
  if (!x) { toast('No such dispatch entry'); return; }
  const f = x.files[0], mk = has('Ops maker', 'Ops checker'), kinds = C.dispatch?.kinds || {};
  const kv = (k, v) => v ? `<div><small>${k}</small><b>${v}</b></div>` : '';
  modal(mhd(`${esc(kinds[x.kind] || x.kind)} · ${esc(x.ref)}`, `${x.id} · ${esc(x.name)} (${x.ucc})`) + `<div class="bd">
    <div class="kv">${kv('Status', pill(x.status + (x.simulated ? ' (simulated)' : ''), DSP_ST[x.status] || 'nt'))}${kv('Reason', esc(x.reason))}${kv('Channel', x.channel === 'physical' ? 'Paper, as the client chose' : 'Email to ' + esc(x.to))}${kv('Subject', esc(x.subject))}${kv('Created', esc(x.created) + ' by ' + esc(x.by))}${kv('Accepted by mail server', esc(x.sent_at))}${kv('Message ID', x.message_id ? `<span class="mono small">${esc(x.message_id)}</span>` : '')}${kv('Bounced', esc(x.bounced_at))}${kv('Courier', x.courier ? esc(x.courier) + ' · ' + esc(x.courier_at) : '')}${kv('Attempt', x.attempt)}</div>
    ${f ? `<h4 style="margin:16px 0 8px">Signed file</h4><div class="kv">${kv('File', esc(f.name) + ` <span class="dim">(${nf(f.bytes / 1024, 1)} KB)</span>`)}${kv('SHA-256', `<span class="mono small">${f.sha256}</span>`)}${kv('Signed by', esc(f.signer))}${kv('Certificate', [f.serial, f.issuer].filter(Boolean).map(esc).join(' · '))}${kv('Signed at', esc(f.signed_at))}</div><div id="dsp-ver"></div>` : '<div class="note bad">No signed file: the document was held before signing.</div>'}
    ${mk && (x.status === 'Bounced' || x.status === 'Physical pending') ? `<h4 style="margin:16px 0 8px">Record the printed copy</h4><div class="form"><div class="fld full"><label for="dsp-cour">Courier and reference number</label><input class="in" id="dsp-cour" placeholder="e.g. Blue Dart 7788123456, sent 26-Oct"></div></div>` : ''}
    ${mk && x.status === 'Sent' && x.channel === 'email' ? `<h4 style="margin:16px 0 8px">Bounced later?</h4><div class="form"><div class="fld full"><label for="dsp-why">Bounce reason from the mail server</label><input class="in" id="dsp-why" placeholder="e.g. 550 mailbox does not exist"></div></div>` : ''}
  </div><div class="ft"><button class="btn" data-act="close">Close</button>${f ? `<button class="btn" data-act="dspVerify" data-id="${x.id}">Check signature</button><button class="btn" data-act="dl" data-id="/v1/dispatch/${x.id}/file" data-name="${esc(f.name)}">Download PDF</button>` : ''}${mk && x.status === 'Sent' && x.channel === 'email' ? `<button class="btn bad" data-act="dspBounce" data-id="${x.id}">Mark bounced</button>` : ''}${mk && ['Held', 'Failed', 'Bounced'].includes(x.status) ? `<button class="btn" data-act="dspResend" data-id="${x.id}">Send again</button>` : ''}${mk && (x.status === 'Bounced' || x.status === 'Physical pending') ? `<button class="btn pri" data-act="dspPhysical" data-id="${x.id}">Printed copy sent</button>` : ''}</div>`);
}
function sigModal() {
  get('/v1/signing').then(s => {
    const admin = has('Admin'), i = s.info || {};
    const kv = (k, v) => v ? `<div><small>${k}</small><b>${v}</b></div>` : '';
    modal(mhd('Digital signature', 'Contract notes and statements must be digitally signed under the IT Act (SEBI circular on electronic contract notes). Use the firm’s Class 3 document signer certificate, either as a .pfx file or through a signing service or HSM that signs on request.') + `<div class="bd">
      <div class="kv">${kv('Mode', { file: 'Certificate file', remote: 'Signing service', test: 'Test certificate (development)', off: 'Not set up' }[s.mode] || s.mode)}${kv('State', s.ready ? pill(s.test ? 'Test only' : 'Ready', s.test ? 'warn' : 'good') : pill(s.problem || 'Not set up', 'bad'))}${kv('Signer', esc(i.name))}${kv('Issued by', esc(i.issuer))}${kv('Serial', esc(i.serial))}${kv('Valid', i.from ? `${fmtDate(i.from.slice(0, 10))} to ${fmtDate(i.until.slice(0, 10))}${s.ready && !s.test ? ` <span class="dim">(${s.expires_in_days} days left)</span>` : ''}` : '')}${kv('Fingerprint', i.sha256 ? `<span class="mono small">${esc(i.sha256)}</span>` : '')}${kv('Signing service', esc(s.remote_url))}${kv('Location', esc(s.location))}${kv('Changed', s.updated_by ? esc(s.updated_by) + ' · ' + esc(s.updated_at) : '')}</div>
      ${admin ? `<h4 style="margin:16px 0 8px">Change</h4>${segBtns('sigMode', [['file', 'Certificate file'], ['remote', 'Signing service'], ['off', 'Remove']], 'sig-f')}
      <div class="form" id="sig-f"></div><div id="sig-err"></div>` : '<div class="dim small">Only an Admin can change the certificate.</div>'}
    </div><div class="ft"><button class="btn" data-act="close">Close</button>${admin ? '<button class="btn pri" data-act="sigSave">Save</button>' : ''}</div>`);
    if (admin) sigForm();
  }).catch(e => toast(e.message, true));
}
UI.sigMode = 'file';
function sigForm() { const el = $('sig-f'); if (el) el.innerHTML = P['sig-f'](); }
P['sig-f'] = () => {
  return UI.sigMode === 'file' ? `<div class="fld full"><label for="sig-file">Certificate file (.pfx, .p12 or PEM with the private key)</label><input class="in" type="file" id="sig-file" accept=".pfx,.p12,.pem,.crt,.key"></div><div class="fld"><label for="sig-pw">File password</label><input class="in" type="password" id="sig-pw" autocomplete="off"></div><div class="fld"><label for="sig-loc">Place of signing</label><input class="in" id="sig-loc" placeholder="e.g. Mumbai"></div><div class="dim small full">The file and its password are stored encrypted with the firm’s data key and are never shown again. A USB token cannot be used from a server; use a signing service for one.</div>`
    : UI.sigMode === 'remote' ? `<div class="fld full"><label for="sig-url">Signing service address (https)</label><input class="in mono" id="sig-url" placeholder="https://sign.firm.internal/v1/sign"></div><div class="fld"><label for="sig-key">API key</label><input class="in" type="password" id="sig-key" autocomplete="off"></div><div class="fld"><label for="sig-loc">Place of signing</label><input class="in" id="sig-loc" placeholder="e.g. Mumbai"></div><div class="dim small full">Ledgerline sends only the SHA-256 of each document and checks every signature that comes back against the certificate fixed when you save.</div>`
    : '<div class="note bad full">Removing the certificate holds every contract note and statement until a new one is set up.</div>';
};
Object.assign(A, {
  dspOpen: id => dspModal(id),
  dspCSV: () => { const f = UI.dspFrom || addDaysJS(S.date, -30), t = UI.dspTo || S.date; download(`/v1/dispatch.csv?from=${f}&to=${t}${UI.dspKind ? '&kind=' + UI.dspKind : ''}${UI.dspTab ? '&status=' + encodeURIComponent(UI.dspTab) : ''}`, `dispatch-register_${f}_${t}.csv`); },
  dspBounces: () => $('dsp-bfile').click(),
  dspVerify: async id => { const el = $('dsp-ver'); try { const r = await get(`/v1/dispatch/${id}/verify`); el.innerHTML = r.ok ? `<div class="note good">Signature valid: signed by <b>${esc(r.signer.name)}</b> at ${esc(r.signing_time)}; it covers the whole file and the file matches its fingerprint.</div>` : `<div class="note bad">${esc(r.problem || 'The signature does not cover the whole file.')}</div>`; } catch (e) { el.innerHTML = errHTML(e); } },
  dspBounce: id => act(() => post(`/v1/dispatch/${id}/bounce`, { reason: $('dsp-why').value }), 'Marked bounced: a printed copy is queued'),
  dspPhysical: id => act(() => post(`/v1/dispatch/${id}/physical`, { courier: $('dsp-cour').value }), 'Printed copy recorded'),
  dspResend: id => act(() => post(`/v1/dispatch/${id}/resend`), r => r.status === 'Sent' ? 'Sent' : `${r.status}: ${r.reason || ''}`),
  dspRun: () => {
    const prev = addDaysJS(S.date.slice(0, 8) + '01', -1).slice(0, 7);
    modal(mhd('Send statements', 'Signs and sends one statement to every client it applies to. Clients already sent the same statement are skipped; paper clients are queued for printing.') + `<div class="bd"><div class="form">
      <div class="fld"><label for="sr-kind">Statement</label><select class="in" id="sr-kind"><option value="margin_statement">Daily margin statement</option><option value="ras_statement">Running account settlement</option><option value="dp_statement">DP transactions and holdings</option><option value="ledger_statement">Monthly statement of account</option><option value="funds_securities_statement">Weekly funds and securities (week to the given Friday)</option></select></div>
      <div class="fld"><label for="sr-per">Date or month</label><input class="in mono" id="sr-per" value="${S.date}"></div>
      <div class="dim small full">Margin and running account settlement take a date (YYYY-MM-DD); DP and monthly statements take a month (YYYY-MM).</div></div><div id="sr-err"></div></div>
      <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="sr-go">Sign and send</button></div>`);
    $('sr-kind').onchange = e => { $('sr-per').value = ['dp_statement', 'ledger_statement'].includes(e.target.value) ? prev : S.date; };
    $('sr-go').onclick = async () => { $('sr-go').disabled = true; try { const r = await post('/v1/dispatch/run', { kind: $('sr-kind').value, period: $('sr-per').value.trim() }); closeModal(); toast(`${r.sent} sent, ${r.bounced} bounced, ${r.paper} for printing, ${r.held} held${r.skipped ? `, ${r.skipped} already sent` : ''}${r.errors?.length ? ` · ${r.errors.length} errors` : ''}`, !!r.errors?.length); render({ keep: true }); } catch (e) { $('sr-err').innerHTML = errHTML(e); $('sr-go').disabled = false; } };
  },
  sigOpen: () => sigModal(),
  sigSave: async () => {
    const err = $('sig-err'); err.innerHTML = '';
    try {
      if (UI.sigMode === 'file') {
        const f = $('sig-file').files[0]; if (!f) { toast('Choose the certificate file'); return; }
        const buf = new Uint8Array(await f.arrayBuffer()); let bin = ''; for (let i = 0; i < buf.length; i += 8192) bin += String.fromCharCode(...buf.subarray(i, i + 8192));
        await post('/v1/signing/file', { file: btoa(bin), password: $('sig-pw').value, location: $('sig-loc').value });
      } else if (UI.sigMode === 'remote') await post('/v1/signing/remote', { url: $('sig-url').value, key: $('sig-key').value, location: $('sig-loc').value });
      else await post('/v1/signing/off');
      toast(UI.sigMode === 'off' ? 'Certificate removed: documents will be held' : 'Signing certificate saved'); closeModal(); if (S.view === 'dispatch') render({ keep: true });
    } catch (e) { err.innerHTML = errHTML(e); }
  },
});
document.addEventListener('change', e => {
  const id = e.target.id;
  if (id === 'dsp-kind') { UI.dspKind = e.target.value; render({ keep: true }); }
  if (id === 'dsp-from' || id === 'dsp-to') { UI[id === 'dsp-from' ? 'dspFrom' : 'dspTo'] = e.target.value; render({ keep: true }); }
  if (id === 'dsp-bfile' && e.target.files.length) { e.target.files[0].text().then(t => api('POST', '/v1/dispatch/bounces', t, true)).then(r => { toast(`${r.bounced} bounce${r.bounced === 1 ? '' : 's'} recorded; printed copies queued`); render({ keep: true }); }).catch(x => toast(x.message, true)); }
});

/* ---------- notices from Ledgerline (maintenance, releases, billing) ---------- */
let noticeCache = { at: 0, list: [] };
function noticeHidden() { try { return JSON.parse(localStorage.getItem('ll-notices-hidden') || '[]'); } catch (e) { return []; } }
async function showNotices() {
  if (clientUser() || !S.me) return;
  if (Date.now() - noticeCache.at > 300000) { try { noticeCache = { at: Date.now(), list: await get('/v1/notices') }; } catch (e) { noticeCache = { at: Date.now(), list: [] }; } }
  const hid = noticeHidden(), ns = noticeCache.list.filter(n => !hid.includes(n.id)), p = $('page');
  if (!ns.length || !p || $('ll-notices')) return;
  p.insertAdjacentHTML('afterbegin', `<div id="ll-notices">${ns.map(n => `<div class="note ${n.level === 'info' ? '' : esc(n.level)}" style="display:flex;gap:12px;align-items:flex-start;margin-bottom:10px"><div style="flex:1"><b>${esc(n.title)}</b>${n.text ? `<div style="white-space:pre-line">${esc(n.text)}</div>` : ''}<div class="dim small">From Ledgerline${n.until ? ' · until ' + esc(new Date(n.until).toLocaleString('en-IN', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })) : ''}</div></div><button class="linkish small" data-act="noticeHide" data-id="${esc(n.id)}">Hide</button></div>`).join('')}</div>`);
}
A.noticeHide = id => { const h = noticeHidden(); h.push(id); try { localStorage.setItem('ll-notices-hidden', JSON.stringify(h.slice(-50))); } catch (e) {} $('ll-notices')?.remove(); showNotices(); };

/* ---------- exchange files: the exact files of exchanges, CCs, depositories, front end and bank ---------- */
/* ---------- clearing data: the day's clearing and depository files at work ---------- */
V.ccdata = {
  load: () => get(`/v1/clearing-data?date=${S.date}`),
  html: d => {
    const secs = d.sections, mk = has('Ops maker', 'Ops checker', 'Risk manager');
    const panel = s => {
      const sa = s.id === 'short_allocation';
      const cols = s.columns.map(c => [esc(c), /Difference|margin|Cash|cash|Clearing|Ledgerline|Short|Collateral|Buy|Sell|Quantity|Accepted|Pledged|value|%/.test(c) ? 'r' : '']);
      const rows = s.rows.map(r => `<tr>${r.map((v, i) => `<td class="${cols[i][1]} ${/Difference/.test(s.columns[i]) && v && !/^-?0(\.0+)?$/.test(v) ? 'neg' : ''}">${esc(v)}</td>`).join('')}${sa && mk ? `<td><button class="btn sm" data-act="saWhy" data-seg="${esc(r[0])}" data-id="${esc(r[2])}" data-short="${esc(r[6])}">Reason</button></td>` : ''}</tr>`);
      return `<div class="panel"><div class="hd"><h3>${esc(s.title)}</h3><div class="tools">${s.loaded ? (s.differences ? pill(s.differences + (sa ? ' without a reason' : ' differences'), 'warn') : pill(sa ? 'All reported' : 'Matches', 'good')) : pill('File not loaded for this date', 'nt')}</div></div>
        <div class="pad dim small">${esc(s.about)}${s.file ? ` <span class="mono">${esc(s.file)}</span>` : ''}</div>${s.loaded ? tbl(sa && mk ? [...cols, ['']] : cols, rows, { empty: 'Nothing in the file.' }) : ''}</div>`;
    };
    return ph('Clearing data', 'The clearing corporations’ and CDSL’s daily files at work: their client margins, collateral, short allocations, stamp duty, obligations, re-pledged securities and direct pay-outs, set against Ledgerline’s own figures. Load the files on Exchange files; the reporting files built from them (margin reporting, short allocation) are downloaded there.', '<button class="btn" data-act="go" data-id="xfiles">Exchange files</button>') +
      `<div class="kpis">${kpi('Files loaded', secs.filter(s => s.loaded).length + ' of ' + secs.length, 'for ' + fmtDate(S.date))}${kpi('Differences', secs.reduce((a, s) => a + s.differences, 0), 'to look into', secs.some(s => s.differences) ? 'warn' : 'good')}</div>` + secs.map(panel).join('');
  }
};
Object.assign(A, {
  saWhy: (id, el) => {
    const codes = C.ccdata.reason_codes;
    confirmModal('Short allocation reason · ' + id, `${el.dataset.seg} short ₹${esc(el.dataset.short)}. One line per reason; recording the same reason again replaces it.`, `<div class="form"><div class="fld full"><label for="sa-c">Reason</label><select class="in" id="sa-c">${Object.keys(codes).sort().map(k => `<option value="${k}">${k} · ${esc(codes[k])}</option>`).join('')}</select></div><div class="fld"><label for="sa-a">Collateral available (₹)</label><input class="in" id="sa-a" value="${esc(el.dataset.short)}"></div><div class="fld"><label for="sa-cc">Other clearing corporation code (reason 01)</label><input class="in mono" id="sa-cc"></div></div>`,
      'Record', () => act(() => post('/v1/clearing-data/short-allocation-reasons', { date: S.date, segment: el.dataset.seg, ucc: id, code: $('sa-c').value, amount: $('sa-a').value.trim(), cc_code: $('sa-cc').value.trim() }), 'Reason recorded'));
  },
});
/* ---------- periodic work: weekly, monthly, quarterly ---------- */
const PER_ST = { Done: 'good', Due: 'warn', Overdue: 'bad', Failed: 'bad', Upcoming: 'nt', 'Waiting for approval': 'acc', 'Waiting for a file': 'warn', 'Nothing to do': 'nt' };
V.periodic = {
  load: () => get(`/v1/periodic?date=${S.date}`),
  html: ts => {
    const n = st => ts.filter(t => st.includes(t.status)).length, mk = has('Ops maker', 'Ops checker'), groups = [...new Set(ts.map(t => t.freq))];
    const row = t => `<tr><td><b>${esc(t.name)}</b><div class="dim small wrap">${esc(t.about)}</div>${t.basis ? `<div class="small dim">${esc(t.basis)}</div>` : ''}</td><td class="small">${esc(t.label)}<div class="dim">${esc(t.due)}</div></td><td class="small">${t.due_on ? fmtDate(t.due_on) : '—'}</td>
      <td>${pill(t.status, PER_ST[t.status] || 'nt')}${t.note ? `<div class="small wrap">${esc(t.note)}</div>` : ''}${t.run ? `<div class="dim small">${esc(t.run.by)} · ${esc(t.run.at)}</div>` : ''}${t.run && t.run.files ? t.run.files.map(f => `<div><button class="linkish small" data-act="dl" data-id="/v1/periodic/files/${f.id}" data-name="${esc(f.name)}">${esc(f.name)}</button></div>`).join('') : ''}</td>
      <td class="r"><div style="display:flex;gap:6px;justify-content:flex-end;flex-wrap:wrap">${mk && t.status !== 'Done' && t.status !== 'Nothing to do' ? `<button class="btn sm ${['Due', 'Overdue', 'Failed'].includes(t.status) ? 'pri' : ''}" data-act="perRun" data-id="${t.id}" data-p="${esc(t.period)}">${t.status === 'Failed' ? 'Try again' : t.auto ? 'Run now' : 'Start'}</button>` : ''}${t.view ? `<button class="btn sm" data-act="go" data-id="${t.view}">Open</button>` : ''}</div></td></tr>`;
    return ph('Periodic work', 'Weekly, monthly and quarterly work: client statements, month-end postings, running account settlement, the BSDA review, the client funds monitoring return, disclosures and the periodic exchange files. Each task shows the period it covers, when it is due and whether it is done, counting work done on its own screen or by Autopilot. The “Periodic work” scheduled job runs the ones that can run by themselves every morning and puts the rest in the work queue.') +
      `<div class="kpis">${kpi('Overdue', n(['Overdue', 'Failed']), 'past the due date or failed', n(['Overdue', 'Failed']) ? 'bad' : 'good')}${kpi('Due', n(['Due']), 'due today')}${kpi('Waiting', n(['Waiting for approval', 'Waiting for a file']), 'for a checker or a file')}${kpi('Done', n(['Done']), 'for the current period', 'good')}</div>` +
      groups.map(g => `<div class="panel"><div class="hd"><h3>${esc(g)}</h3></div>${tbl([['Task'], ['Period'], ['Due'], ['Status'], ['']], ts.filter(t => t.freq === g).map(row))}</div>`).join('');
  }
};
Object.assign(A, {
  perRun: (id, el) => act(() => post(`/v1/periodic/${id}/run`, { period: el.dataset.p, date: S.date }), r => `${r.status}: ${r.summary}`),
});
V.xfiles = {
  load: async () => { const [fs, cl] = await Promise.all([get('/v1/exchange-files/formats'), get(`/v1/exchange-files-checklist?date=${S.date}`).catch(() => null)]); fs.checklist = cl; return fs; },
  html: fs => {
    const inn = fs.filter(f => f.direction === 'in'), out = fs.filter(f => f.direction === 'out'), mk = has('Ops maker', 'Ops checker', 'Risk manager', 'Compliance officer');
    const res = UI.xfRes ? `<div class="panel"><div class="hd"><h3>Last upload</h3></div>${tbl([['File'], ['Recognised as'], ['Rows', 'r'], ['Check'], ['Result']], UI.xfRes.map(r => `<tr><td class="mono small wrap">${esc(r.name)}</td><td>${r.format ? `<b>${esc(r.title)}</b><div class="dim small mono">${esc(r.format)}</div>` : '<span class="dim">Not recognised</span>'}</td><td class="r">${r.rows || '—'}</td><td>${!r.format ? '' : r.problems ? pill(r.problems + ' problem' + (r.problems > 1 ? 's' : ''), 'bad') + `<div class="small wrap">${r.issues.slice(0, 5).map(i => `line ${i.line}${i.column ? ' · ' + esc(i.column) : ''}: ${esc(i.message)}`).join('<br>')}</div>` : pill('Matches the layout', 'good')}</td><td class="small wrap">${esc(r.loaded || '')}${r.added ? ` · ${r.added} added` : ''}${r.skipped ? ` · ${r.skipped} skipped` : ''}${r.note ? `<div class="dim">${esc(r.note)}</div>` : ''}</td></tr>`))}</div>` : '';
    const st = f => f.writes ? pill('Ledgerline writes it', 'good') : f.linked ? pill('Read and used', 'good') : pill(f.confirmed ? 'Read and checked' : 'Layout known', f.confirmed ? 'acc' : 'nt');
    const row = f => `<tr><td><b>${esc(f.title)}</b><div class="dim small wrap">${esc(f.purpose)}</div>${f.linked ? `<div class="small wrap">${esc(f.linked)}</div>` : ''}</td><td>${esc(f.party)}</td><td class="small">${esc(f.when)}</td><td class="r">${f.fields}</td><td>${st(f)}${f.confirmed ? '' : '<div class="dim small">meaning of some fields to confirm</div>'}</td>
      <td>${f.writes && mk ? `<button class="btn sm" data-act="xfDl" data-id="${f.id}">Download</button>` : ''}</td></tr>`;
    return ph('Exchange files', 'The files the firm receives from and sends to the exchanges, clearing corporations, CDSL, the trading front end and the bank, in their exact layouts. Drop a file as downloaded (zip or gz is fine): Ledgerline recognises it, checks every field against the layout, and loads the ones it uses.',
      mk ? '<button class="btn" data-act="xfPick" data-load="0">Check a file</button><button class="btn pri" data-act="xfPick" data-load="1">Load files</button>' : '') +
      `<div class="kpis">${kpi('Layouts', fs.length, `${inn.length} received · ${out.length} sent`)}${kpi('Used by Ledgerline', fs.filter(f => f.linked || f.writes).length, 'read into Ledgerline or written by it', 'good')}${kpi('Checked only', fs.filter(f => !f.linked && !f.writes).length, 'recognised and validated, not used yet')}</div>` + res +
      xfChecklist(fs.checklist) +
      `<div class="panel"><div class="hd"><h3>Received</h3></div>${tbl([['File'], ['From'], ['When'], ['Fields', 'r'], ['Status'], ['']], inn.map(row), { tall: true })}</div>
      <div class="panel"><div class="hd"><h3>Sent</h3><div class="tools"><label class="dim small">For</label><input class="in sm" type="date" id="xf-date" value="${esc(UI.xfDate || S.date)}"></div></div>${tbl([['File'], ['To'], ['When'], ['Fields', 'r'], ['Status'], ['']], out.map(row), { tall: true })}</div>`;
  }
};
// The day's expected files, filtered by the clearing corporations chosen in Settings.
function xfChecklist(cl) {
  if (!cl) return '';
  const tone = { Received: 'good', Written: 'good', Due: 'acc', Missing: 'bad', Intraday: 'nt', 'Check by hand': 'warn' };
  return `<div class="panel"><div class="hd"><h3>Daily file checklist · ${fmtDate(cl.date)}</h3><small>${cl.missing ? pill(cl.missing + ' missing', 'bad') : pill('Nothing missing', 'good')} · ${esc(cl.clearing.map(c => c.key + ' ' + c.through).join(' · '))}</small></div>${cl.notes.length ? `<div class="bd" style="padding-bottom:0">${cl.notes.map(n => `<div class="note">${esc(n)}</div>`).join('')}</div>` : ''}
    ${tbl([['File'], ['From / to'], ['For'], ['Due'], ['Status']], cl.items.map(i => `<tr><td><b>${esc(i.title)}</b>${(i.files || []).length ? `<div class="dim small mono wrap">${esc(i.files.join(', '))}</div>` : ''}</td><td>${esc(i.party)} ${i.direction === 'out' ? '(sent)' : ''}</td><td class="small">${esc(i.for.join(', '))}</td><td class="n small">${esc(i.due)}</td><td>${pill(i.status, tone[i.status] || 'nt')}</td></tr>`), { tall: true })}</div>`;
}
Object.assign(A, {
  xfPick: (id, el) => {
    const inp = document.createElement('input'); inp.type = 'file'; inp.multiple = true; const load = el.dataset.load === '1';
    inp.onchange = async () => {
      const all = [];
      for (const f of inp.files) {
        try { toast(`${load ? 'Loading' : 'Checking'} ${f.name}…`); all.push(...await api('POST', `/v1/exchange-files?name=${encodeURIComponent(f.name)}&load=${load ? 1 : 0}`, f, true)); }
        catch (e) { all.push({ name: f.name, issues: [], note: e.message }); }
      }
      UI.xfRes = all; $('toast').hidden = true; render({ keep: true });
    };
    inp.click();
  },
  xfDl: id => {
    const d = $('xf-date')?.value || S.date; UI.xfDate = d;
    if (id === 'ncl-client-collateral') {
      modal(mhd('Client collateral file', 'One file per segment for NSE Clearing.') + `<div class="bd"><div class="split">${['CM', 'FO', 'CD'].map(g => `<button class="btn" data-act="dl" data-id="/v1/exchange-files/${id}?date=${d}&segment=${g}" data-name="CC_${g}_${d}.csv.gz">${g}</button>`).join('')}</div></div>`);
      return;
    }
    if (id === 'ncl-allocation' || id === 'ncl-allocation-api') {
      modal(mhd(id === 'ncl-allocation' ? 'Client allocation file' : 'Client allocation (API message)', 'NSE Clearing for cash, F&O and currency; MCXCCL for commodities.') + `<div class="bd"><div class="split"><button class="btn" data-act="dl" data-id="/v1/exchange-files/${id}?date=${d}&exchange=NSE" data-name="allocation_${d}">NSE Clearing</button>${id === 'ncl-allocation' ? `<button class="btn" data-act="dl" data-id="/v1/exchange-files/${id}?date=${d}&exchange=MCX" data-name="allocation_mcx_${d}">MCXCCL</button>` : ''}</div></div>`);
      return;
    }
    download(`/v1/exchange-files/${id}?date=${d}`, id + '_' + d);
  },
});
/* ---------- connections: the firm's accounts with outside services ---------- */
const CONN_ST = { Working: 'good', Saved: 'acc', Problem: 'bad', 'Not set': 'nt' };
V.connections = {
  load: () => get('/v1/connections'),
  html: d => {
    const admin = has('Admin'), groups = [...new Set(d.connections.map(c => c.group))];
    return ph('Connections', 'The firm’s own accounts with the services Ledgerline works with: KRA and CKYC, the trading front end, exchanges, the depository, the bank, SMS, WhatsApp, e-Sign and the mail server. Passwords and keys are kept encrypted with the firm’s keys, never shown again and never sent to anyone; every use is recorded. A test checks what can be checked without side effects: that the address answers securely, and for the mail server that the login works.') +
      `<div class="kpis">${kpi('Needed to go live', `${d.ready} of ${d.required}`, 'set up', d.ready < d.required ? 'warn' : 'good')}${kpi('Working', d.connections.filter(c => c.status === 'Working').length, 'tested and answering', 'good')}${kpi('Problems', d.connections.filter(c => c.status === 'Problem').length, '', d.connections.some(c => c.status === 'Problem') ? 'bad' : '')}</div>` +
      (d.server_ip ? `<div class="note">Providers that only accept listed addresses (KRA, OMS vendors, banks) need this server’s address: <b class="mono">${esc(d.server_ip)}</b></div>` : '<div class="note">Some providers only accept listed addresses. Ask your Ledgerline contact for this server’s address (set LEDGERLINE_SERVER_IP to show it here).</div>') +
      groups.map(g => `<div class="panel"><div class="hd"><h3>${esc(g)}</h3></div>${tbl([['Service'], ['Account'], ['State'], ['Last checked'], ['']], d.connections.filter(c => c.group === g).map(c => {
        const acct = [c.values.provider, c.values.code && 'code ' + c.values.code, c.values.member && 'member ' + c.values.member, c.values.dp_id && 'DP ' + c.values.dp_id, c.values.bank, c.values.account && '…' + c.values.account, c.values.user, c.values.host, c.values.sender && 'sender ' + c.values.sender, c.values.number].filter(Boolean).map(esc).join(' · ');
        return `<tr><td><b>${esc(c.name)}</b>${c.required ? ' ' + pill('Needed to go live', 'acc') : ''}<div class="dim small">${esc(c.purpose)}</div></td><td class="small wrap" style="max-width:260px">${c.system ? `<span class="dim">${esc(c.system)}</span>` : acct || '<span class="dim">—</span>'}</td><td>${pill(c.status, CONN_ST[c.status] || 'nt')}${c.problem ? `<div class="small ${c.status === 'Problem' ? 'neg' : 'dim'} wrap">${esc(c.problem)}</div>` : ''}</td><td class="small">${c.tested_at ? `${fmtDate(c.tested_at.slice(0, 10))} ${c.tested_at.slice(11, 16)}<div class="dim">${esc(c.tested_by)}</div>` : '—'}</td>
          <td class="r"><div style="display:flex;gap:6px;justify-content:flex-end;flex-wrap:wrap">${c.system ? (c.id === 'signing' ? '<button class="btn sm" data-act="go" data-id="dispatch">Open</button>' : '') : admin ? `${c.status !== 'Not set' && c.test !== 'none' ? `<button class="btn sm" data-act="connTest" data-id="${c.id}">Test</button>` : ''}${c.id === 'ncl-margins' && c.status !== 'Not set' ? `<button class="btn sm" data-act="nclmKey" title="Make the RSA key pair and the certificate to upload on NMASS">Make key</button>` : ''}<button class="btn sm ${c.status === 'Not set' ? 'pri' : ''}" data-act="connEdit" data-id="${c.id}">${c.status === 'Not set' ? 'Set up' : 'Change'}</button>` : ''}</div></td></tr>`;
      }))}</div>`).join('');
  }
};
Object.assign(A, {
  connEdit: id => {
    const c = C.connections.connections.find(x => x.id === id);
    const fld = f => {
      const v = c.values[f.key] || '', lab = `<label for="cn-${f.key}">${esc(f.label)}${f.optional ? ' <span class="dim">(optional)</span>' : ''}</label>`;
      if (f.options) return `<div class="fld">${lab}<select class="in" id="cn-${f.key}"><option value="">Choose</option>${f.options.map(o => `<option ${o === v ? 'selected' : ''}>${esc(o)}</option>`).join('')}</select></div>`;
      if (f.secret && f.key === 'private_key') return `<div class="fld full">${lab}<textarea class="in mono" rows="4" id="cn-${f.key}" autocomplete="off" spellcheck="false" placeholder="${c.secret_set[f.key] ? 'Saved: leave blank to keep' : '-----BEGIN PRIVATE KEY-----'}"></textarea></div>`;
      if (f.secret) return `<div class="fld">${lab}<input class="in" type="password" id="cn-${f.key}" autocomplete="new-password" placeholder="${c.secret_set[f.key] ? 'Saved: leave blank to keep' : ''}"></div>`;
      return `<div class="fld ${f.key === 'notes' || f.key === 'url' ? 'full' : ''}">${lab}<input class="in ${f.key === 'url' || f.key === 'host' ? 'mono' : ''}" id="cn-${f.key}" value="${esc(v)}" placeholder="${esc(f.placeholder || '')}"></div>`;
    };
    modal(mhd(esc(c.name), esc(c.purpose) + '. Saved encrypted; passwords and keys are never shown again.') + `<div class="bd"><div class="form">${c.fields.map(fld).join('')}</div><div id="cn-err"></div></div>
      <div class="ft">${c.status !== 'Not set' ? `<button class="btn danger" data-act="connRemove" data-id="${id}">Remove</button>` : ''}<span style="flex:1"></span><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="connSave" data-id="${id}">Save${c.test !== 'none' ? ' and test' : ''}</button></div>`);
  },
  connSave: async id => {
    const c = C.connections.connections.find(x => x.id === id), values = {}, secrets = {};
    c.fields.forEach(f => { const v = $('cn-' + f.key).value; if (f.secret) { if (v) secrets[f.key] = v; } else values[f.key] = v.trim(); });
    try {
      await api('PUT', '/v1/connections/' + id, { values, secrets });
      const r = c.test !== 'none' ? await post('/v1/connections/' + id + '/test') : null;
      closeModal(); toast(r ? `${c.name}: ${r.status}${r.problem ? ' · ' + r.problem : ''}` : 'Saved', r && r.status === 'Problem'); render({ keep: true });
    } catch (e) { $('cn-err').innerHTML = errHTML(e); }
  },
  nclmKey: () => {
    modal(mhd('Make the NSE Clearing key', 'Ledgerline makes a 2048-bit RSA key pair, keeps the private key in its vault, and gives you the certificate to upload on NMASS → API Access Management. A new key replaces the old one: upload the new certificate before the next login.') + `<div class="bd"><div class="form">
      <div class="fld"><label for="nk-org">Organisation</label><input class="in" id="nk-org" placeholder="Firm's registered name"></div>
      <div class="fld"><label for="nk-unit">Department <span class="dim">(optional)</span></label><input class="in" id="nk-unit" placeholder="Risk"></div>
      <div class="fld"><label for="nk-city">City</label><input class="in" id="nk-city"></div>
      <div class="fld"><label for="nk-state">State</label><input class="in" id="nk-state"></div>
      <div class="fld full"><label for="nk-cn">Server name (common name)</label><input class="in mono" id="nk-cn" placeholder="backoffice.firm.in"></div></div><div id="nk-err"></div></div>
      <div class="ft"><span style="flex:1"></span><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="nk-go">Make key</button></div>`);
    $('nk-go').onclick = async () => {
      try {
        const r = await post('/v1/ncl-margins/key', { organisation: $('nk-org').value.trim(), department: $('nk-unit').value.trim(), city: $('nk-city').value.trim(), state: $('nk-state').value.trim(), country: 'IN', common_name: $('nk-cn').value.trim() });
        const save = (text, name) => { const url = URL.createObjectURL(new Blob([text], { type: 'application/x-pem-file' })); const a = document.createElement('a'); a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove(); };
        modal(mhd('Upload this certificate to NMASS', `Valid until ${esc(fmtDate(r.expires))}. Upload it on NMASS → API Access Management with your server IPs. The private key stays in Ledgerline.`) + `<div class="bd"><div class="fld full"><label for="nk-pem">${esc(r.file_name)}</label><textarea class="in mono" rows="8" id="nk-pem" readonly>${esc(r.certificate)}</textarea></div>
          <div class="dim small">NSE Clearing recommends a certificate signed by a certificate authority. To get one, give the authority the signing request and upload the certificate it returns instead.</div></div>
          <div class="ft"><button class="btn" id="nk-csr">Download signing request</button><span style="flex:1"></span><button class="btn pri" id="nk-dl">Download ${esc(r.file_name)}</button></div>`);
        $('nk-dl').onclick = () => save(r.certificate, r.file_name);
        $('nk-csr').onclick = () => save(r.signing_request, r.file_name.replace(/\.pem$/, '.csr'));
      } catch (e) { $('nk-err').innerHTML = errHTML(e); }
    };
  },
  connTest: id => act(() => post('/v1/connections/' + id + '/test'), r => `${r.name}: ${r.status}${r.problem ? ' · ' + r.problem : ''}`),
  connRemove: id => { if (window.confirm('Remove this connection and its saved password or key?')) act(() => post('/v1/connections/' + id + '/remove'), 'Removed'); },
});

/* ============================== start ============================== */
async function loadMe() { S.me = await get('/v1/me'); document.body.classList.remove('signed-out'); persist(); }
async function boot() {
  try { CFG = await (await fetch('/ui/config')).json(); } catch (e) { CFG = {}; }
  try { await loadMe(); } catch (e) { if (e.status !== 401 && e.code !== 'setup_required') showLogin(e.message); return; }
  render();
}
boot();

setInterval(() => { if (UI.liveAuto && S.view === 'live' && !document.querySelector('.md') && !document.hidden) render({ keep: true }); }, 60000);

document.addEventListener('change', e => { if (e.target.id === 'ai-month') { UI.aiMonth = e.target.value; render({ keep: true }); } });
