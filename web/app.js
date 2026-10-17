'use strict';
/* Ledgerline console. Every number on screen comes from the server's API;
   this page holds no business data of its own. */

/* ============================== state ============================== */
const LS = 'ledgerline-console-v2';
const S = { tenant: '', date: '2026-10-16', view: 'dashboard', me: null, counts: {} };
try { Object.assign(S, JSON.parse(localStorage.getItem(LS) || '{}')); } catch (e) {}
const persist = () => { try { localStorage.setItem(LS, JSON.stringify({ tenant: S.tenant, date: S.date, view: S.view })); } catch (e) {} };
const UI = { inTab: 'due', inSel: new Set(), inQ: '', clientTag: '', clientSel: new Set(), bkQ: '', bkPlan: '', cr: { kind: 'ledger', ucc: '', preset: 'fy', from: '', to: '' }, crRes: null, crErr: null, crBusy: false, reconF: 'OPEN', reconX: 'All', reconQ: '', clientStage: 'All', clientQ: '', rmsF: 'All', rmsQ: '', settleTab: 'cm', noteF: 'All',
  bankF: 'All', rasF: 'due', auditQ: '', ledgerU: '', ledgerTab: 'statement', calc: { bucket: 'CM-DEL', exchange: 'NSE', side: 'B', qty: 100, price: '1412' },
  debTab: 'cd', caTab: 'ca', monTab: 'rcpt', monQ: '', debQ: '', intFrom: '', intTo: '', fileType: 'NSE-CM-TRADE', lastUpload: null, ob: null, search: '' };
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
function toast(m) { const t = $('toast'); t.textContent = m; t.hidden = false; clearTimeout(toast.t); toast.t = setTimeout(() => t.hidden = true, 3800); }
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
  return `<div class="errbox"><b>${esc(e.message)}</b>${extra}</div>`;
}
async function download(path, name) {
  try {
    const res = await fetch(path, { headers: { 'X-Requested-With': 'ledgerline' } });
    if (!res.ok) { let m = 'Download failed'; try { m = (await res.json()).error.message; } catch (e) {} toast(m); return; }
    const blob = await res.blob(); const url = URL.createObjectURL(blob);
    const a = document.createElement('a'); a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 4000); toast('Downloaded ' + name);
  } catch (e) { toast(e.message); }
}
async function openDoc(path) {
  const w = window.open('', '_blank');
  try {
    const res = await fetch(path, { headers: { 'X-Requested-With': 'ledgerline' } });
    const html = await res.text();
    if (!res.ok) throw new Error('Could not open the document');
    const url = URL.createObjectURL(new Blob([html], { type: 'text/html' }));
    if (w) w.location = url; else location.href = url;
  } catch (e) { if (w) w.close(); toast(e.message); }
}

/* ============================== shell ============================== */
const NAV = [
  ['Overview', [['dashboard', 'Dashboard']]],
  ['Operations', [['files', 'Files'], ['recon', 'Trade reconciliation'], ['billing', 'Billing'], ['settlement', 'Settlement'], ['notes', 'Contract notes'], ['clients', 'Clients & onboarding'], ['corp', 'Corporate actions'], ['statements', 'Client reports']]],
  ['Risk', [['rms', 'RMS & limits'], ['margin', 'Margin reporting'], ['collateral', 'Collateral & pledge'], ['debits', 'Debtors & interest']]],
  ['Accounts', [['ledger', 'Ledger'], ['bank', 'Bank reconciliation'], ['money', 'Receipts & payouts'], ['ras', 'Running account'], ['charges', 'Charges calculator'], ['brokerage', 'Brokerage plans']]],
  ['Compliance', [['reports', 'Regulatory reports'], ['surv', 'Surveillance & AML'], ['inactive', 'Inactive accounts'], ['ipo', 'IPO & mutual funds'], ['audit', 'Audit trail']]],
  ['Admin', [['settings', 'Settings']]]];
const VIEWS = NAV.flatMap(g => g[1].map(v => v[0]));
try { const h = location.hash.slice(1); if (VIEWS.includes(h)) S.view = h; } catch (e) {}

function renderSide() {
  const ct = S.counts, q = { clients: 1, notes: 1, collateral: 1, reports: 1 };
  const codes = Object.entries(S.me?.member_codes || {}).map(([k, v]) => k + ' ' + v).join(' · ');
  $('side').innerHTML = `<div class="brand"><div class="mark"><i></i><i></i><i></i></div><div><b>Ledgerline</b><small>${esc(S.me?.firm || 'Back office')}</small></div></div>` +
    NAV.map(([g, items]) => `<nav class="navg" aria-label="${g}"><h6>${g}</h6>${items.map(([k, l]) => `<button class="nav ${S.view === k ? 'on' : ''}" data-act="go" data-id="${k}"><span>${l}</span>${ct[k] ? `<span class="ct ${q[k] ? 'q' : ''}">${ct[k]}</span>` : ''}</button>`).join('')}</nav>`).join('') +
    `<div class="side-foot">${codes ? 'Member codes ' + esc(codes) + '<br>' : ''}${S.me?.dev ? 'Development server. Data is held in memory and is lost when the server stops.' : 'Connected to ' + esc(location.host)}${S.me?.sample_rates ? '<br>Charges use the sample rate schedule.' : ''}</div>`;
}
function renderTop() {
  const d = C.dashboard;
  const users = S.me?.users || [];
  $('top').innerHTML = `<div class="tdate"><small>Trade date</small><div class="dnav"><button class="btn sm" data-act="prevDay" aria-label="Previous trading day">‹</button><input class="in mono" type="date" id="date" value="${esc(S.date)}" aria-label="Trade date" style="height:30px"><button class="btn sm" data-act="nextDay" aria-label="Next trading day">›</button></div></div>
  <div class="chips">${d?.extra?.pay_in ? `<span class="chip">Pay-in <b>${fmtDate(d.extra.pay_in)}</b></span>` : ''}${S.me ? `<span class="chip">EOD <b>${d?.eod?.closed ? 'closed ' + esc(d.eod.closed_at) : 'open'}</b></span>` : ''}</div>
  <div class="srch"><svg viewBox="0 0 24 24" fill="none" stroke-width="2"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/></svg><input id="gsearch" placeholder="Find client by name, UCC or PAN  ( / )" autocomplete="off" value="${esc(UI.search)}"><div class="res" id="gres" hidden></div></div>
  ${S.me ? `<div class="umenu"><button class="ubtn" data-act="umenu" aria-haspopup="menu"><span class="av">${esc(initials(meUser()?.name || S.me.actor))}</span><span><b>${esc(meUser()?.name || S.me.actor)}</b><small>${esc(S.me.role)}</small></span><span class="dim">▾</span></button><div class="umenu-pop" id="umenu" hidden role="menu">
    <button data-act="pwChange" role="menuitem">Change password</button>
    <button data-act="totp" role="menuitem">Two-factor sign-in${S.me.auth?.totp_enabled ? ' · <span class="pos">On</span>' : ' · Off'}</button>
    ${S.me.dev ? `<div class="sep">Switch user (development)</div>${users.filter(u => u.id !== S.me.actor && u.active).map(u => `<button data-act="switchUser" data-id="${esc(u.id)}" role="menuitem">${esc(u.name)} <span class="dim">· ${esc(u.role)}</span></button>`).join('')}` : ''}
    <div class="sep"></div><button data-act="signOut" role="menuitem">Sign out</button></div></div>` : ''}`;
}
async function refreshCounts() {
  try {
    const d = await get(`/v1/dashboard?date=${S.date}`);
    C.dashboard = d;
    S.counts = { recon: d.recon.OPEN || 0, rms: d.risk.Breach || 0, collateral: d.pending_pledges || 0, bank: (d.bank['Third-party'] || 0) + (d.bank.Unidentified || 0),
      surv: d.alerts.open || 0, inactive: (+d.extra.inactive_due || 0) + (+d.extra.reactivation_pending || 0), clients: d.onboarding || 0, notes: (d.notes.total || 0) - (d.notes.Sent || 0), reports: d.reports_due || 0 };
  } catch (e) {}
  renderSide(); renderTop();
}
let renderSeq = 0;
async function render(opts = {}) {
  persist();
  try { history.replaceState(null, '', '#' + S.view); } catch (e) {}
  renderSide(); renderTop();
  const p = $('page'), view = S.view, seq = ++renderSeq;
  if (!opts.keep) p.innerHTML = '<p class="loading">Loading…</p>';
  try {
    C[view] = await V[view].load();
    if (seq !== renderSeq) return;
    p.innerHTML = V[view].html(C[view]);
    bindPage();
  } catch (e) {
    if (seq !== renderSeq) return;
    if (e.status === 401 || e.code === 'setup_required') return;
    p.innerHTML = ph('Something went wrong', '') + errHTML(e);
  }
  refreshCounts();
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
  let d; try { d = await post('/v1/auth/2fa/setup'); } catch (e) { toast(e.message); return; }
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
V.dashboard = {
  load: async () => { const [d, sm] = await Promise.all([get(`/v1/dashboard?date=${S.date}`), S.me?.dev ? get('/v1/dev/sample').catch(() => null) : null]); S.sample = sm || {}; return d; },
  html: d => {
    if (!d.trades && !d.oms_fills) return ph('Operations dashboard', `${esc(S.me.firm)} · ${fmtDate(S.date)}`) + emptyDay();
    const segc = ['var(--accent)', 'var(--good)', 'var(--warn)', 'var(--ink3)', 'var(--bad)'];
    const tmax = Math.max(1, ...d.by_segment.map(x => num(x.value)));
    const hmax = Math.max(1, ...d.by_hour.map(x => x.count));
    const withReq = d.risk.WithMargin || 0;
    const ub = d.util_buckets.map((u, k) => [u.label, u.count, ['var(--good)', 'var(--accent)', 'var(--warn)', 'var(--bad)'][k]]);
    const open = d.recon.OPEN || 0, breach = d.risk.Breach || 0;
    const sett = d.settlement, exc = (sett['Short delivery'] || 0) + (sett['Funds shortfall'] || 0) + (sett['Unpledge & pay-in'] || 0);
    const bankAtt = (d.bank['Third-party'] || 0) + (d.bank.Unidentified || 0);
    const att = [];
    if (breach) att.push(['bad', 'rms', `${breach} client${breach > 1 ? 's' : ''} in margin breach`, `${cmp(d.extra.breach_amount)} uncovered · square off or collect funds`]);
    if (open) att.push(['bad', 'recon', `${open} open reconciliation break${open > 1 ? 's' : ''}`, 'Resolve before EOD']);
    if (!d.billed && d.trades) att.push(['warn', 'billing', 'Bills not posted', 'Run billing after reconciliation']);
    if (exc) att.push(['warn', 'settlement', `${exc} settlement exception${exc > 1 ? 's' : ''} for ${fmtDate(d.extra.pay_in)}`, 'Short deliveries, unpledge requests and funds shortfalls']);
    if (bankAtt) att.push(['warn', 'bank', `${bankAtt} bank credit${bankAtt > 1 ? 's' : ''} need attention`, 'Third-party or unidentified receipts']);
    if (d.pending_overrides) att.push(['acc', 'rms', `${d.pending_overrides} limit override${d.pending_overrides > 1 ? 's' : ''} awaiting checker`, 'Maker-checker approval required']);
    if (d.pending_pledges) att.push(['acc', 'collateral', `${d.pending_pledges} pledge request${d.pending_pledges > 1 ? 's' : ''} pending`, 'Accept or reject at the depository']);
    if (d.alerts.high) att.push(['bad', 'surv', `${d.alerts.high} high-severity surveillance alert${d.alerts.high > 1 ? 's' : ''}`, 'Review and respond']);
    if ((d.notes.total || 0) - (d.notes.Sent || 0) > 0) att.push(['acc', 'notes', `${d.notes.total - (d.notes.Sent || 0)} contract notes not delivered`, 'Must reach clients within 24 hours']);
    if (+d.extra.inactive_due) att.push(['warn', 'inactive', `${d.extra.inactive_due} account${+d.extra.inactive_due > 1 ? 's' : ''} due to be flagged inactive`, 'No activity for 24 months: flag in the UCC database']);
    if (+d.extra.reactivation_pending) att.push(['acc', 'inactive', `${d.extra.reactivation_pending} reactivation request${+d.extra.reactivation_pending > 1 ? 's' : ''} awaiting checker`, 'IPV and KRA checks recorded']);
    if (d.reports_due) att.push(['acc', 'reports', `${d.reports_due} daily report${d.reports_due > 1 ? 's' : ''} not uploaded`, 'Generate, validate and upload']);
    const stepCls = (done, alert) => done ? (alert ? 'alert' : 'done') : 'pend';
    const notesSent = d.notes.Sent || 0;
    return ph('Operations dashboard', `${esc(S.me.firm)} · ${fmtDate(S.date)}`, `<button class="btn" data-act="go" data-id="recon">Open break queue</button><button class="btn pri" data-act="eod">Run EOD close</button>`) +
    (d.eod.previous_open ? `<div class="note warn"><b>Previous day still open.</b> ${esc(d.eod.previous_open)}. <button class="linkish" data-act="gotoDate" data-id="${prevBD(S.date)}">Go to ${fmtDate(prevBD(S.date))}</button></div>` : '') +
    (d.eod.closed && S.me?.dev && S.sample?.next && S.sample.next > S.date ? `<div class="note"><b>Day closed${d.eod.rolled_positions ? `; ${d.eod.rolled_positions} open positions carried to ${fmtDate(d.eod.next_day)}` : ''}.</b> <button class="linkish" data-act="gotoDate" data-id="${S.sample.next}">Continue the sample on ${fmtDate(S.sample.next)}</button></div>` : '') +
    (d.warnings.length ? `<div class="note warn">${d.warnings.map(esc).join('<br>')}</div>` : '') +
    `<div class="proc">
      <div class="done"><small>Files</small><b>${d.trades} trades loaded</b><span>${d.oms_fills} OMS fills</span></div>
      <div class="${stepCls(d.recon.total, open)}"><small>Trade recon</small><b>${d.recon.total ? (open ? open + ' breaks open' : 'All resolved') : 'Not run'}</b><span>${d.recon.total ? (d.recon.total - open) + ' of ' + d.recon.total + ' resolved or cleared' : 'Run reconciliation'}</span></div>
      <div class="${stepCls(d.billed)}"><small>Billing</small><b>${d.billed ? 'Bills posted' : 'Not billed'}</b><span>${d.billed ? 'Ledger updated' : 'After reconciliation'}</span></div>
      <div class="${breach ? 'alert' : 'live'}"><small>RMS</small><b>${breach ? breach + ' breaches' : 'Limits live'}</b><span>${withReq} clients with margin</span></div>
      <div class="${stepCls(d.notes.total && notesSent === d.notes.total, false)}"><small>Contract notes</small><b>${d.notes.total ? notesSent + ' of ' + d.notes.total + ' sent' : 'Not generated'}</b><span>${d.notes.Bounced ? d.notes.Bounced + ' bounced' : 'Within 24 hours'}</span></div>
      <div class="${d.eod.closed ? 'done' : 'pend'}"><small>EOD</small><b>${d.eod.closed ? 'Closed ' + esc(d.eod.closed_at) : 'Open'}</b><span>Pay-in ${fmtDate(d.extra.pay_in)}</span></div>
    </div>
    <div class="kpis">
      ${kpi('Exchange trades', nf(d.trades), cmp(d.turnover) + ' turnover')}
      ${kpi('Open breaks', open, d.recon.total ? d.recon.total + ' found today' : 'reconciliation not run', open ? 'bad' : (d.recon.total ? 'good' : ''))}
      ${kpi('Margin breaches', breach, 'Short collection ' + cmp(d.short), breach ? 'bad' : 'good')}
      ${kpi('Client funds cover', num(d.funds.cover).toFixed(2) + '×', 'Bank + CC vs client credits', d.funds.ok ? 'good' : 'bad')}
      ${kpi('Penalty exposure', inr(d.penalty), 'Short-collection slab estimate', num(d.penalty) > 0 ? 'warn' : 'good')}
      ${kpi('Open alerts', d.alerts.open || 0, (d.alerts.high || 0) + ' high severity', d.alerts.high ? 'bad' : '')}
    </div>
    <div class="grid g21">
      <div class="panel"><div class="hd"><h3>Needs attention</h3><small>${att.length} items</small></div><div class="att">${att.map(a => `<button data-act="go" data-id="${a[1]}"><i class="${a[0]}"></i><div><b>${esc(a[2])}</b><span>${esc(a[3])}</span></div><span class="dim">›</span></button>`).join('') || '<div class="empty">Nothing needs attention</div>'}</div></div>
      <div class="panel"><div class="hd"><h3>Margin utilisation</h3><small>${withReq} clients with positions</small></div><div class="bd">
        <div class="stack">${ub.map(u => `<i style="width:${withReq ? u[1] / withReq * 100 : 0}%;background:${u[2]}"></i>`).join('')}</div>
        <div class="legend">${ub.map(u => `<span style="--c:${u[2]}">${u[0]} · <b class="mono">${u[1]}</b></span>`).join('')}</div>
        <div class="sum" style="margin-top:16px"><span>Required <b>${cmp(d.required)}</b></span><span>Collected <b>${cmp(d.collected)}</b></span></div>
      </div></div>
    </div>
    <div class="grid g2">
      <div class="panel"><div class="hd"><h3>Turnover by exchange segment</h3><small>Notional; options at premium</small></div><div class="bd"><div class="hbars">${d.by_segment.map((x, k) => `<div class="hb"><span>${esc(x.label)}</span><div class="track"><i style="width:${(num(x.value) / tmax * 100).toFixed(1)}%;background:${segc[k % 5]}"></i></div><span class="v">${cmp(x.value)}</span></div>`).join('')}</div></div></div>
      <div class="panel"><div class="hd"><h3>Trades by hour</h3><small>Exchange trade files, IST</small></div><div class="bd"><div class="cols">${d.by_hour.map(x => `<div class="c"><em>${x.count}</em><i style="height:${(x.count / hmax * 82).toFixed(1)}%"></i></div>`).join('')}</div><div class="cols-x">${d.by_hour.map(x => `<span>${esc(x.label)}</span>`).join('')}</div></div></div>
    </div>`;
  }
};
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
      <div class="dim" style="font-size:12.5px">${esc(t.when)}${t.needs_date ? ' · applied to the trade date above' : ''}${t.sample_layout ? ' · <span class="pill warn">Sample layout</span>' : ''}${t.columns ? `<div class="mono" style="font-size:11.5px;margin-top:4px;overflow-wrap:anywhere">${esc(t.columns.join(','))}</div>` : ''}</div>
      <label class="drop" id="drop" for="file"><b>Drop a file here</b> or click to choose<br><span class="dim" style="font-size:12px">CSV or text, up to 64 MB</span></label><input type="file" id="file" hidden>
      ${r ? (r.error ? errHTML(r.error) : `<div class="note good" style="background:var(--good-soft)"><b>${esc(r.name)}</b> loaded with ${esc(r.res.adapter)}: ${r.res.added} new row${r.res.added === 1 ? '' : 's'}${r.res.duplicate_rows ? `, ${r.res.duplicate_rows} already loaded` : ''}.${r.res.file.prior_id ? ` Identical to earlier upload ${esc(r.res.file.prior_id)}.` : ''}</div>`) : ''}
    </div></div>
    <div class="panel"><div class="hd"><h3>Daily file checklist</h3><small>What a normal day needs</small></div>${tbl([['When'], ['File'], ['Purpose']], [['BOD', 'MARKET-DATA', 'Prices, SPAN, VaR, haircuts'], ['BOD', 'DP-HOLDINGS', 'Client demat balances and pledges'], ['BOD', 'POSITIONS', 'Carried derivatives positions'], ['Intraday', 'PLEDGE-REQUESTS', 'Margin pledges to accept'], ['Intraday / EOD', 'BANK-STMT', 'Client receipts'], ['EOD', 'Exchange trade files', 'NSE CM/FO/CD, BSE CM, MCX'], ['EOD', 'OMS-TRADE', 'For reconciliation'], ['Daily', 'SURV-ALERTS', 'Exchange alerts']].map(x => `<tr><td>${x[0]}</td><td class="n">${x[1]}</td><td class="dim">${x[2]}</td></tr>`))}</div></div>
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
V.recon = {
  load: async () => { const [all, ex, oms, ccm] = await Promise.all([get(`/v1/recon/breaks?date=${S.date}`), get(`/v1/trades?date=${S.date}&origin=EXCHANGE`), get(`/v1/trades?date=${S.date}&origin=OMS`), get(`/v1/ccm?date=${S.date}`)]); return { all, ex, oms, ccm }; },
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
    <div class="note ${isOpen ? 'warn' : ''}"><b>Suggested fix.</b> ${esc(b.suggestion)}</div>
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
  const terms = c.type === 'Dividend' ? `₹${esc(c.dps)} a share` : c.type === 'Bonus' ? `${c.ratio_new}:${c.ratio_old} (new : held)` : `FV ${c.ratio_old} → ${c.ratio_new}`;
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
    const v = d.deb, risk = has('Risk manager'), long = v.rows.filter(r => r.days >= v.block_days).length;
    return ph('Debtors & interest', `Clients whose ledger is in debit after the pay-in date. A bill dated T is due on T+1, so it counts against the client from the next day. Clients in debit for ${v.block_days} days or more are blocked from buying in the trading front end until they clear the debit; interest (delayed payment charges) is charged on the overdue debit at the firm’s rate.`,
      `<button class="btn" data-act="dl" data-id="/v1/debits/block-file?date=${S.date}" data-name="debit_block_${S.date}.csv">Block / unblock file</button>${risk ? `<button class="btn pri" data-act="debitBlock" ${v.to_block + v.to_unblock ? '' : 'disabled'}>Apply blocks (${v.to_block} block · ${v.to_unblock} unblock)</button>` : ''}`) +
    `<div class="kpis">${kpi('Clients in debit', v.rows.filter(r => num(r.debit) > 0).length, inr(v.debit) + ' overdue', v.rows.length ? 'warn' : 'good')}${kpi(`In debit ≥ ${v.block_days} days`, long, 'continuous debit', long ? 'bad' : 'good')}${kpi('To block / unblock', `${v.to_block} / ${v.to_unblock}`, 'front-end buying', v.to_block ? 'bad' : '')}${kpi('Not covered by securities', inr(v.shortfall), 'debit above holdings after haircut', num(v.shortfall) ? 'bad' : 'good')}${kpi('Interest accrued', inr(v.accrued, 2), 'this month, not yet posted')}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('debTab', [['cd', 'Continuous debit'], ['age', 'Debit ageing'], ['stock', 'Debtors’ stock'], ['int', 'Interest']])}${UI.debTab !== 'int' ? `<input class="in" data-ui="debQ" data-part="deb-t" placeholder="Filter name, UCC or group" value="${esc(UI.debQ)}">` : ''}</div></div><div id="deb-t">${P['deb-t'](d)}</div></div>`;
  }
};
P['deb-t'] = d => {
  const v = d.deb, q = UI.debQ.trim().toLowerCase(), f = r => !q || (r.ucc + ' ' + r.name + ' ' + r.tags.join(' ')).toLowerCase().includes(q);
  const who = r => `<button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}${r.tags.length ? ' ' + r.tags.map(t => `<span class="gtag">${esc(t)}</span>`).join('') : ''}`;
  const rows = v.rows.filter(f);
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
    ${can && to.length ? `<button class="btn sm pri" data-act="postInterest">Post interest for ${to.length} client${to.length > 1 ? 's' : ''} (${inr(iv.to_post, 2)})</button>` : ''}</div></div>
    ${iv.error ? `<div class="bd">${errHTML({ message: iv.error })}</div>` : tbl([['Client'], ['Days charged', 'r'], ['Average debit', 'r'], ['Interest', 'r'], ['GST', 'r'], ['Total', 'r'], ['Status'], ['Invoice']], rows.map(r => `<tr><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}${r.tags.map(t => ` <span class="gtag">${esc(t)}</span>`).join('')}</td><td class="r">${r.debit_days}</td><td class="r">${nf(r.avg_debit, 2)}<div class="dim small">peak ${nf(r.max_debit)}</div></td><td class="r">${nf(r.interest, 2)}</td><td class="r">${nf(r.gst, 2)}</td><td class="r"><b>${nf(r.total, 2)}</b></td><td>${pill(r.status, r.status === 'To post' ? 'warn' : r.status.startsWith('Posted') ? 'good' : 'nt')}</td><td class="n dim">${esc(r.invoice || '')}</td></tr>`),
      { tall: true, empty: 'No overdue debit in this period.', foot: rows.length ? `<tfoot><tr><td colspan="3">Total</td><td class="r">${nf(sum(rows, r => num(r.interest)), 2)}</td><td class="r">${nf(sum(rows, r => num(r.gst)), 2)}</td><td class="r">${nf(sum(rows, r => num(r.total)), 2)}</td><td colspan="2"></td></tr></tfoot>` : '' })}
    <div class="pad"><span class="dim small">Simple interest on each day’s overdue debit (interest already charged does not earn interest). Posting debits the client, credits interest income and GST payable, and issues a tax invoice (SAC 997119). Periods already charged are never charged again.</span></div>`;
};

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
    return ph('Contract notes', 'One electronic contract note per client per trade date, with every trade and the full charge breakdown, to be delivered within 24 hours. Without a mail relay configured, messages go to the outbox; addresses that cannot receive mail show as bounced.',
      can ? `<button class="btn" data-act="genNotes">Generate notes</button><button class="btn pri" data-act="sendNotes" ${by('Generated') + by('Bounced') ? '' : 'disabled'}>Send pending</button>` : '') +
    `<div class="kpis">${kpi('Generated', d.length, 'clients who traded')}${kpi('Delivered', by('Sent'), 'accepted by mail relay', 'good')}${kpi('Pending', by('Generated'), 'not yet sent', by('Generated') ? 'warn' : '')}${kpi('Bounced', by('Bounced'), 'fix email, then re-send', by('Bounced') ? 'bad' : '')}</div>
    <div class="panel"><div class="hd">${segBtns('noteF', ['All', 'Sent', 'Generated', 'Bounced'], 'notes-t')}<small>${d.length ? '' : 'Run billing, then generate'}</small></div><div id="notes-t">${P['notes-t'](d)}</div></div>`;
  }
};
P['notes-t'] = d => tbl([['Contract note'], ['UCC'], ['Client'], ['Email'], ['Trades', 'r'], ['Net (₹)', 'r'], ['Status'], ['Sent'], ['']], d.filter(n => UI.noteF === 'All' || n.status === UI.noteF).map(n => `<tr><td class="n">${esc(n.no)}${n.revision ? ` <span title="Revision ${n.revision}: replaces ${esc(n.replaces)}">${pill('Revised', 'acc')}</span>` : ''}</td><td class="n">${n.ucc}</td><td>${esc(n.name)}</td><td class="dim">${esc(n.email)}</td><td class="r">${n.trades}</td><td class="r ${sgn(n.net)}">${inr(n.net, 2)}</td><td>${pill(n.status, n.status === 'Sent' ? 'good' : n.status === 'Bounced' ? 'bad' : 'warn')}</td><td class="n dim">${esc(n.sent_at || '—')}</td><td><button class="btn sm" data-act="noteDoc" data-id="${n.ucc}">Open</button>${has('Ops maker', 'Ops checker') ? `<button class="btn sm" data-act="sendNote" data-id="${n.ucc}">${n.status === 'Generated' ? 'Send' : 'Resend'}</button>` : ''}</td></tr>`), { tall: true, empty: 'No contract notes for this date.' });

/* ---------- clients ---------- */
const STAGES = ['Application', 'KYC verification', 'In-person verification', 'UCC upload', 'Active'];
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
    return ph('Clients & onboarding', 'Applications move through KYC, in-person verification and UCC upload. Activation needs an Ops checker who did not create the application. Groups (tags) such as HNI or STAFF drive brokerage plans and group reports.',
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
async function clientDrawer(ucc) {
  modal(`<div class="bd"><p class="loading">Loading ${esc(ucc)}…</p></div>`, true);
  let d, bp; try { [d, bp] = await Promise.all([get(`/v1/clients/${encodeURIComponent(ucc)}?date=${S.date}`), get(`/v1/brokerage/client/${encodeURIComponent(ucc)}?date=${S.date}`).catch(() => null)]); } catch (e) { modal(mhd('Client', '') + `<div class="bd">${errHTML(e)}</div>`, true); return; }
  const c = d.client, r = d.risk, st = { OK: 'good', Watch: 'warn', Breach: 'bad', Debit: 'warn' };
  const lines = d.statement.lines.slice(-12);
  const h3 = t => `<h3 style="margin:0 0 8px;font-family:var(--f-cond);font-size:15px">${t}</h3>`;
  modal(mhd(esc(c.name), `${c.ucc} · ${esc(c.type)} · ${esc(c.city)}`) + `<div class="bd">
    <div class="tools">${pill(c.status, c.status === 'Active' ? 'good' : 'acc')}${r ? pill('RMS: ' + r.status, st[r.status]) : ''}${c.blocked ? pill('Blocked: ' + (c.blocked_reason || ''), 'bad') : ''}${(c.segments || []).map(s => `<span class="tag">${esc(d.segment_names[s] || s)}</span>`).join('')}</div>
    <div class="grouprow"><div><small class="dim">Groups</small><div class="tools">${(c.tags || []).map(t => `<span class="gtag">${esc(t)}</span>`).join('') || '<span class="dim">None</span>'}${has('Ops maker', 'Ops checker') ? `<button class="btn sm" data-act="editTags" data-id="${c.ucc}">Edit groups</button>` : ''}</div></div>
    ${c.status === 'Active' ? `<div><small class="dim">Account activity</small><div>${c.inactive ? `${pill('Inactive', 'nt')} since ${fmtDate(c.inactive_since)}${has('Ops maker', 'Ops checker') ? ` <button class="btn sm" data-act="reactivate" data-id="${c.ucc}">Request reactivation</button>` : ''}` : `Active${c.reactivated_on ? ` · reactivated ${fmtDate(c.reactivated_on)}` : ''} <button class="linkish" data-act="go" data-id="inactive">Inactivity rules</button>`}${has('Ops maker', 'Ops checker') ? ` <button class="linkish" data-act="contact" data-id="${c.ucc}">Update contact details</button>` : ''}</div></div>` : ''}
    ${bp ? `<div><small class="dim">Brokerage plan</small><div><b>${esc(bp.plan.plan_name)}</b> <span class="dim">· ${esc(bp.plan.source)}${bp.plan.from ? ' since ' + fmtDate(bp.plan.from) : ''}</span> <button class="linkish" data-act="go" data-id="brokerage">Plans</button></div><div class="dim small">${['CM-DEL', 'CM-INTRA', 'FO-FUT', 'FO-OPT'].map(k => `${{ 'CM-DEL': 'Delivery', 'CM-INTRA': 'Intraday', 'FO-FUT': 'Futures', 'FO-OPT': 'Options' }[k]}: ${esc(bp.rates[k] || '—')}`).join(' · ')}</div></div>` : ''}</div>
    ${r ? `<div class="kv"><div><small>Ledger</small><b class="mono ${sgn(r.funds)}">${inr(r.funds, 2)}</b></div><div><small>UPI blocked</small><b class="mono">${inr(r.upi)}</b></div><div><small>Collateral (usable)</small><b class="mono">${inr(r.collateral_usable)}</b></div><div><small>Margin required</small><b class="mono">${inr(r.required)}</b></div><div><small>Free</small><b class="mono ${sgn(r.free)}">${inr(r.free)}</b></div><div><small>OMS limit</small><b class="mono">${inr(r.limit)}</b></div></div>` : '<div class="note">Not active yet. Limits and the ledger start at activation.</div>'}
    <div class="kv"><div><small>PAN</small><b class="mono">${esc(c.pan)}</b></div><div><small>Bank</small><b>${esc(c.bank.bank)} · ${esc(c.bank.account)}</b></div><div><small>IFSC</small><b class="mono">${esc(c.bank.ifsc)}</b></div><div><small>Demat</small><b class="mono">${esc(c.demat.depository)} ${esc(c.demat.id)}</b></div><div><small>Income</small><b>${esc(c.income)}</b></div><div><small>RAS</small><b>${esc(c.ras)}</b></div><div><small>Nominee</small><b>${esc(c.nominee)}${c.nominee_name ? ' · ' + esc(c.nominee_name) : ''}</b></div><div><small>KYC date</small><b>${fmtDate(c.kyc_date)}</b></div><div><small>Email</small><b>${esc(c.email)}</b></div><div><small>Last trade</small><b>${fmtDate(c.last_trade) || '—'}</b></div></div>
    ${d.positions.length ? `<div>${h3('Open positions')}${tbl([['Contract'], ['Net qty', 'r'], ['Avg', 'r'], ['LTP', 'r'], ['MTM', 'r'], ['Margin', 'r']], d.positions.map(p => `<tr><td>${esc(p.name)}</td><td class="r ${sgn(p.qty)}">${nf(p.qty)}</td><td class="r">${nf(p.avg_price, 2)}</td><td class="r">${nf(p.ltp, 2)}</td><td class="r ${sgn(p.mtm)}">${nf(p.mtm, 0)}</td><td class="r">${inr(p.margin)}</td></tr>`))}</div>` : ''}
    ${d.holdings.length ? `<div>${h3('Demat holdings')}${tbl([['Scrip'], ['Qty', 'r'], ['Pledged', 'r'], ['LTP', 'r'], ['Value', 'r']], d.holdings.map(h => `<tr><td>${esc(h.symbol)}</td><td class="r">${nf(h.qty)}</td><td class="r">${h.pledged ? nf(h.pledged) : '—'}</td><td class="r">${nf(h.ltp, 2)}</td><td class="r">${inr(h.value)}</td></tr>`))}</div>` : ''}
    ${d.trades.length ? `<div>${h3(`Trades on ${fmtDate(S.date)} · ${d.trades.length}`)}${tbl([['Time'], ['Contract'], ['Side'], ['Qty', 'r'], ['Price', 'r'], ['Product']], d.trades.slice(0, 40).map(t => `<tr><td class="n">${t.trade_time}</td><td>${esc(instName(`${t.symbol}|${t.instrument_type}|${t.expiry}|${t.strike}`))}</td><td>${t.side === 'B' ? 'Buy' : 'Sell'}</td><td class="r">${nf(t.qty)}</td><td class="r">${nf(t.price, 2)}</td><td><span class="tag">${esc(t.product)}</span></td></tr>`))}</div>` : ''}
    <div>${h3('Ledger (latest entries)')}${tbl([['Date'], ['Particulars'], ['Debit', 'r'], ['Credit', 'r'], ['Balance', 'r']], lines.map(l => `<tr><td class="n dim">${fmtDate(l.value_date)}</td><td class="wrap">${esc(l.memo || l.narration)}</td><td class="r">${num(l.debit) ? nf(l.debit, 2) : ''}</td><td class="r">${num(l.credit) ? nf(l.credit, 2) : ''}</td><td class="r">${drcr(l.balance)}</td></tr>`), { empty: 'No entries' })}</div>
    ${d.alerts.length ? `<div>${h3('Surveillance alerts')}${d.alerts.map(a => `<div class="note ${a.severity === 'High' ? 'bad' : 'warn'}" style="margin-bottom:6px"><b>${esc(a.type)}</b> · ${esc(a.status)}<br>${esc(a.detail)}</div>`).join('')}</div>` : ''}
  </div><div class="ft"><button class="btn" data-act="crFor" data-id="${c.ucc}">Statements &amp; reports</button><button class="btn" data-act="openLedger" data-id="${c.ucc}">Full ledger</button>${c.status === 'Active' && has('Risk manager') ? `<button class="btn ${c.blocked ? '' : 'bad'}" data-act="kill" data-id="${c.ucc}" data-b="${c.blocked ? 0 : 1}">${c.blocked ? 'Unblock' : 'Kill switch'}</button>` : ''}<button class="btn pri" data-act="close">Done</button></div>`, true);
}
const drcr = a => { const n = num(a); return n === 0 ? '0.00' : nf(Math.abs(n), 2) + (n < 0 ? ' Cr' : ' Dr'); };

/* ---------- onboarding wizard ---------- */
function obModal() {
  const o = UI.ob, d = o.data, st = ['Identity', 'Bank & demat', 'Segments & risk', 'Nominee & declarations', 'Review'], E = o.err || {};
  const f = (id, l, ph, type) => `<div class="fld"><label for="ob-${id}">${l}</label><input class="in ${E[id] ? 'bad' : ''}" id="ob-${id}" data-ob="${id}" value="${esc(d[id] || '')}" placeholder="${ph || ''}" type="${type || 'text'}">${E[id] ? `<span class="err">${esc(E[id])}</span>` : ''}</div>`;
  let body = '';
  if (o.step === 0) body = `<div class="form">${f('name', 'Full name as per PAN', 'e.g. Kavya Iyer')}${f('pan', 'PAN', 'ABCPE1234F')}<div class="fld"><label for="ob-type">Client type</label><select class="in" id="ob-type" data-ob="type">${['Individual', 'HUF', 'Corporate', 'NRI'].map(x => `<option ${d.type === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div>${f('mobile', 'Mobile', '10 digits')}${f('email', 'Email', 'name@example.com')}${f('city', 'City', '')}<div class="fld"><label for="ob-kyc">KYC route</label><select class="in" id="ob-kyc" data-ob="kyc_route">${['DigiLocker + Aadhaar e-KYC', 'KRA fetch (existing KYC)', 'CKYC download'].map(x => `<option ${d.kyc_route === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div></div>`;
  if (o.step === 1) body = `<div class="form">${f('bank', 'Bank name', 'e.g. HDFC Bank')}${f('ifsc', 'IFSC', 'HDFC0001234')}${f('account', 'Account number', '9 to 18 digits')}<div class="fld"><label for="ob-dp">Depository</label><select class="in" id="ob-dp" data-ob="depository">${['CDSL', 'NSDL'].map(x => `<option ${d.depository === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div>${f('demat_id', 'Demat account ID', '16 characters')}<div class="note full">The penny-drop check matches the account holder name with the PAN name before the account is saved.</div></div>`;
  if (o.step === 2) body = `<div class="form"><div class="fld full"><label>Segments</label><div class="tools">${[['CM', 'Equity cash'], ['FO', 'Equity F&O'], ['CD', 'Currency'], ['COM', 'Commodity']].map(([k, l]) => `<label class="check"><input type="checkbox" data-obseg="${k}" ${d.segments.includes(k) ? 'checked' : ''}> ${l}</label>`).join('')}</div>${E.segments ? `<span class="err">${esc(E.segments)}</span>` : ''}</div><div class="fld"><label for="ob-income">Annual income</label><select class="in" id="ob-income" data-ob="income">${['<1 L', '1-5 L', '5-10 L', '10-25 L', '25 L-1 Cr', '>1 Cr'].map(x => `<option ${d.income === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div><div class="fld"><label for="ob-ras">Running account settlement</label><select class="in" id="ob-ras" data-ob="ras">${['Quarterly', 'Monthly'].map(x => `<option ${d.ras === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div><label class="check full"><input type="checkbox" data-obchk="income_proof" ${d.income_proof ? 'checked' : ''}> Income proof received (required for derivatives)</label>${E.income_proof ? `<span class="err full">${esc(E.income_proof)}</span>` : ''}</div>`;
  if (o.step === 3) body = `<div class="form"><div class="fld"><label for="ob-nom">Nomination</label><select class="in" id="ob-nom" data-ob="nominee">${['Opted in', 'Opted out'].map(x => `<option ${d.nominee === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div>${d.nominee === 'Opted in' ? f('nominee_name', 'Nominee name', '') : ''}<label class="check full"><input type="checkbox" data-obchk="risk_disclosure" ${d.risk_disclosure ? 'checked' : ''}> Client has read the risk disclosure document and the derivatives risk statement</label><label class="check full"><input type="checkbox" data-obchk="ipv_scheduled" ${d.ipv_scheduled ? 'checked' : ''}> In-person verification by video scheduled</label>${E.risk_disclosure ? `<span class="err full">${esc(E.risk_disclosure)}</span>` : ''}</div>`;
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
    ${d.overrides.length ? `<div class="panel"><div class="hd"><h3>Limit overrides</h3><small>Maker requests, a different checker or risk manager approves. Valid for ${fmtDate(S.date)}.</small></div>${tbl([['ID'], ['UCC'], ['Amount (₹)', 'r'], ['Reason'], ['Maker'], ['Checker'], ['Status'], ['']], d.overrides.map(o => `<tr><td class="n">${o.id}</td><td class="n">${o.ucc}</td><td class="r">${inr(o.amount)}</td><td class="wrap">${esc(o.reason)}</td><td>${esc(o.maker)}</td><td>${esc(o.checker || '—')}</td><td>${pill(o.status, o.status === 'Approved' ? 'good' : o.status === 'Rejected' ? 'bad' : 'warn')}</td><td>${o.status === 'Pending' && has('Ops checker', 'Risk manager') && o.maker !== S.me.actor ? `<button class="btn sm" data-act="ovDecide" data-id="${o.id}" data-a="1">Approve</button><button class="btn sm bad" data-act="ovDecide" data-id="${o.id}" data-a="0">Reject</button>` : ''}</td></tr>`))}</div>` : ''}
    <div class="panel"><div class="hd"><div class="tools">${segBtns('rmsF', ['All', 'Breach', 'Watch', 'Debit', 'OK'], 'rms-t')}<input class="in" data-ui="rmsQ" data-part="rms-t" placeholder="Filter UCC or name" value="${esc(UI.rmsQ)}"></div><small>Sorted by utilisation</small></div><div id="rms-t">${P['rms-t'](d)}</div></div>`;
  }
};
P['rms-t'] = d => {
  let rows = d.rows.slice(); if (UI.rmsF !== 'All') rows = rows.filter(r => r.status === UI.rmsF);
  const q = UI.rmsQ.trim().toLowerCase(); if (q) rows = rows.filter(r => (r.ucc + ' ' + r.name).toLowerCase().includes(q));
  rows.sort((a, b) => (b.utilisation - a.utilisation) || (num(a.funds) - num(b.funds)));
  const st = { OK: 'good', Watch: 'warn', Breach: 'bad', Debit: 'warn' }, warn = d.warn_pct / 100;
  return tbl([['Client'], ['Ledger (₹)', 'r'], ['UPI + collateral', 'r'], ['Required', 'r'], ['Free / OMS limit', 'r'], ['Utilisation'], ['Status'], ['']], rows.map(r => `<tr><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button><div class="dim" style="font-size:11.5px">${esc(r.name)}</div></td><td class="r ${sgn(r.funds)}">${inr(r.funds)}</td><td class="r" title="Collateral after haircut ${inr(r.collateral)}; usable after 50:50 ${inr(r.collateral_usable)}">${num(r.upi) + num(r.collateral) ? inr(num(r.upi) + num(r.collateral_usable)) : '—'}${num(r.collateral_usable) < num(r.collateral) - 1 ? `<div class="dim" style="font-size:11px">capped from ${cmp(num(r.upi) + num(r.collateral))}</div>` : ''}</td><td class="r" title="${num(r.futeq) ? 'Futures equivalent ' + cmp(r.futeq) : ''}">${num(r.required) ? inr(r.required) : '—'}</td><td class="r ${sgn(r.free)}">${inr(r.free)}<div class="dim" style="font-size:11px">limit ${r.blocked ? '0 · blocked' : inr(r.limit)}</div></td><td><div class="split">${bar(r.utilisation, warn)}<span class="mono dim" style="font-size:11.5px">${num(r.required) ? (r.utilisation >= 9.99 ? '>999%' : pct(r.utilisation, 0)) : ''}</span></div></td><td>${pill(r.status, st[r.status])}${r.inactive ? ' ' + pill('Inactive', 'nt') : ''}${r.cash_short || r.square_off_sent ? `<div class="dim" style="font-size:11px;margin-top:2px">${[r.cash_short ? 'cash under 50%' : '', r.square_off_sent ? 'square-off sent' : ''].filter(Boolean).join(' · ')}</div>` : ''}</td><td><div class="split">${has('Ops maker', 'Risk manager') ? `<button class="btn sm" data-act="override" data-id="${r.ucc}">Override</button>` : ''}${has('Risk manager') ? `<button class="btn sm ${r.blocked ? '' : 'bad'}" data-act="kill" data-id="${r.ucc}" data-b="${r.blocked ? 0 : 1}">${r.blocked ? 'Unblock' : 'Kill'}</button>${r.positions ? `<button class="btn sm" data-act="sq" data-id="${r.ucc}">Square off</button>` : ''}` : ''}</div></td></tr>`), { tall: true });
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
P['bank-t'] = d => tbl([['Date'], ['Time'], ['Mode'], ['Reference'], ['Remitter'], ['From a/c'], ['Amount (₹)', 'r'], ['Client'], ['Status'], ['Action']], d.filter(b => UI.bankF === 'All' || b.status === UI.bankF).map(b => `<tr><td class="n dim">${fmtDate(b.date)}</td><td class="n dim">${esc(b.time)}</td><td>${esc(b.mode)}</td><td class="n">${esc(b.ref)}</td><td>${esc(b.remitter)}</td><td class="n">${esc(b.account)}${b.status === 'Third-party' ? ' ' + pill('Not registered', 'bad') : ''}</td><td class="r">${inr(b.amount)}</td><td>${b.ucc ? `<button class="linkish" data-act="client" data-id="${b.ucc}">${b.ucc}</button>` : '<span class="dim">—</span>'}</td><td>${pill(b.status, b.status === 'Matched' || b.status === 'Identified' ? 'good' : b.status === 'Returned' ? 'nt' : b.status === 'Third-party' ? 'bad' : 'warn')}</td><td>${(b.status === 'Third-party' || b.status === 'Unidentified') && has('Ops maker', 'Ops checker') ? `<button class="btn sm bad" data-act="bankReturn" data-id="${esc(b.id)}">Return</button>` : ''}${b.status === 'Unidentified' && has('Ops checker') ? `<button class="btn sm" data-act="identify" data-id="${esc(b.id)}">Identify</button>` : ''}</td></tr>`), { tall: true, empty: 'No statement credits. Upload BANK-STMT on the Files page.' });

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
const MODES = [['inherit', 'Use the charge schedule'], ['pct', '% of turnover'], ['flat', '₹ per order'], ['lower_of', 'Lower of ₹ per order or %'], ['per_lot', '₹ per lot'], ['zero', 'Free']];
V.brokerage = {
  load: async () => { const [d] = await Promise.all([get(`/v1/brokerage?date=${S.date}`), clientCache()]); return d; },
  html: d => {
    const can = has('Ops checker'), plan = id => d.plans.find(p => p.id === id);
    const def = d.current.default, groups = d.tags.map(t => ({ ...t, cur: d.current['tag:' + t.tag] }));
    const overrides = Object.entries(d.current).filter(([k, v]) => k.startsWith('ucc:') && v.plan).map(([k, v]) => ({ ucc: k.slice(4), ...v }));
    const onSched = d.effective.filter(e => !e.plan).length;
    const sched = b => { const r = d.schedule.filter(x => x.bucket === b); return r.length ? r.map(x => (x.exchange === '*' ? '' : x.exchange + ': ') + x.describe).join('; ') : '—'; };
    return ph('Brokerage plans', 'Each client pays brokerage from their own plan if one is set, else from their group’s plan (the most recently assigned, if they are in several groups), else the firm default, else the charge schedule. Brokerage is charged per executed order, from the effective date; days already billed change only if rebilled. Statutory charges always come from the schedule.',
      can ? '<button class="btn" data-act="planNew">New plan</button><button class="btn pri" data-act="assign">Assign a plan</button>' : '') +
    `<div class="kpis">${kpi('Plans', d.plans.length, 'with dated versions')}${kpi('Firm default', def?.plan ? esc(plan(def.plan)?.name || def.plan) : 'Schedule', def?.from ? 'since ' + fmtDate(def.from) : 'no default plan')}${kpi('Groups on a plan', groups.filter(g => g.cur?.plan).length, d.tags.length + ' groups in use')}${kpi('Client overrides', overrides.length, 'set per client')}${kpi('On the schedule', onSched, 'no plan applies', onSched ? 'warn' : '')}</div>
    <div class="plans">${d.plans.map(p => `<div class="panel plan"><div class="hd"><div><h3>${esc(p.name)}</h3><small>${esc(p.note || '')}</small></div><div class="split"><span class="pill acc">${p.clients} client${p.clients === 1 ? '' : 's'}</span>${can ? `<button class="btn sm" data-act="planEdit" data-id="${esc(p.id)}">Edit rates</button>` : ''}</div></div>
      ${tbl([['Product'], ['Brokerage']], d.products.map(x => `<tr><td>${esc(x.name)}</td><td>${p.describe[x.bucket] ? esc(p.describe[x.bucket]) : `<span class="dim">Schedule: ${esc(sched(x.bucket))}</span>`}</td></tr>`))}
      <div class="bd small dim">${p.current ? `In force since ${fmtDate(p.current.from)} · set by ${esc(p.current.by)}` : 'Not yet in force'}${p.upcoming ? ` · <b>new rates from ${fmtDate(p.upcoming.from)}</b>` : ''} · ${p.versions.length} version${p.versions.length === 1 ? '' : 's'}</div></div>`).join('') || '<div class="panel"><div class="empty">No plans yet. Create one, then assign it to the firm, a group or a client.</div></div>'}</div>
    <div class="grid g2">
      <div class="panel"><div class="hd"><h3>Firm default and groups</h3><small>Plan in force on ${fmtDate(S.date)}</small></div>${tbl([['Applies to'], ['Clients', 'r'], ['Plan'], ['Since'], ['']],
        [`<tr><td><b>Firm default</b></td><td class="r">${d.effective.length}</td><td>${def?.plan ? esc(plan(def.plan)?.name || def.plan) : '<span class="dim">Charge schedule</span>'}</td><td class="n dim">${def ? fmtDate(def.from) : ''}</td><td>${can ? `<button class="btn sm" data-act="assign" data-id="default">Change</button>` : ''}</td></tr>`].concat(groups.map(g => `<tr><td><button class="gtag" data-act="filterTag" data-id="${esc(g.tag)}">${esc(g.tag)}</button></td><td class="r">${g.clients}</td><td>${g.cur?.plan ? esc(plan(g.cur.plan)?.name || g.cur.plan) : '<span class="dim">Firm default</span>'}</td><td class="n dim">${g.cur?.plan ? fmtDate(g.cur.from) : ''}</td><td><div class="split">${can ? `<button class="btn sm" data-act="assign" data-id="tag:${esc(g.tag)}">${g.cur?.plan ? 'Change' : 'Set plan'}</button>${g.cur?.plan ? `<button class="btn sm" data-act="unassign" data-id="tag:${esc(g.tag)}">Remove</button>` : ''}` : ''}</div></td></tr>`)), { empty: 'No groups' })}</div>
      <div class="panel"><div class="hd"><h3>Client overrides</h3>${can ? '<button class="btn sm" data-act="assign" data-id="ucc:">Set for a client</button>' : ''}</div>${tbl([['Client'], ['Plan'], ['Since'], ['']], overrides.map(o => `<tr><td><button class="linkish mono" data-act="client" data-id="${o.ucc}">${o.ucc}</button> <span class="dim">${esc(CL.rows.find(c => c.ucc === o.ucc)?.name || '')}</span></td><td>${esc(plan(o.plan)?.name || o.plan)}</td><td class="n dim">${fmtDate(o.from)}</td><td>${can ? `<div class="split"><button class="btn sm" data-act="assign" data-id="ucc:${o.ucc}">Change</button><button class="btn sm" data-act="unassign" data-id="ucc:${o.ucc}">Remove</button></div>` : ''}</td></tr>`), { empty: 'No client-specific plans. Clients follow their group or the firm default.' })}</div>
    </div>
    <div class="panel"><div class="hd"><div class="tools"><h3 style="margin-right:8px">Who pays what</h3><input class="in" data-ui="bkQ" data-part="bk-t" placeholder="Filter by name, UCC or group" value="${esc(UI.bkQ)}"><select class="in" id="bkPlan"><option value="">All plans</option>${d.plans.map(p => `<option value="${esc(p.id)}" ${UI.bkPlan === p.id ? 'selected' : ''}>${esc(p.name)}</option>`).join('')}<option value="-" ${UI.bkPlan === '-' ? 'selected' : ''}>Charge schedule</option></select></div><small>${d.effective.length} active clients on ${fmtDate(S.date)}</small></div><div id="bk-t">${P['bk-t'](d)}</div></div>
    <div class="panel"><div class="hd"><h3>Change history</h3><small>Every assignment is kept; the latest one on or before a date applies</small></div>${tbl([['ID'], ['Applies to'], ['Plan'], ['From'], ['By'], ['At']], d.assignments.slice().reverse().map(a => `<tr><td class="n dim">${a.id}</td><td>${a.target === 'default' ? 'Firm default' : a.target.startsWith('tag:') ? 'Group ' + esc(a.target.slice(4)) : 'Client ' + esc(a.target.slice(4))}</td><td>${a.plan ? esc(plan(a.plan)?.name || a.plan) : '<span class="dim">Removed</span>'}</td><td class="n">${fmtDate(a.from)}</td><td>${esc(a.by)}</td><td class="n dim">${esc(a.at)}</td></tr>`), { tall: true, empty: 'No assignments yet' })}</div>`;
  }
};
P['bk-t'] = d => {
  const q = UI.bkQ.trim().toLowerCase();
  const rows = d.effective.filter(e => (!q || (e.ucc + ' ' + e.name + ' ' + e.tags.join(' ')).toLowerCase().includes(q)) && (!UI.bkPlan || (UI.bkPlan === '-' ? !e.plan : e.plan === UI.bkPlan)));
  return tbl([['Client'], ['Groups'], ['Plan'], ['Because'], ['Since']], rows.map(e => `<tr><td><button class="linkish mono" data-act="client" data-id="${e.ucc}">${e.ucc}</button> ${esc(e.name)}</td><td>${e.tags.map(t => `<span class="gtag">${esc(t)}</span>`).join(' ') || '<span class="dim">—</span>'}</td><td><b style="font-weight:600">${esc(e.plan_name)}</b></td><td>${esc(e.source)}</td><td class="n dim">${e.from ? fmtDate(e.from) : ''}</td></tr>`), { tall: true, empty: 'No clients match' });
};
function planModal(id) {
  const d = C.brokerage, p = d.plans.find(x => x.id === id);
  const cur = p ? (p.upcoming && !p.current ? p.upcoming : p.current) : null, rules = cur?.rules || {};
  const row = x => { const r = rules[x.bucket] || { mode: 'inherit' }, deriv = !x.bucket.startsWith('CM');
    return `<tr data-b="${x.bucket}"><td>${esc(x.name)}</td><td><select class="in sm pm-mode">${MODES.filter(m => deriv || m[0] !== 'per_lot').map(([k, l]) => `<option value="${k}" ${r.mode === k ? 'selected' : ''}>${l}</option>`).join('')}</select></td>${['pct', 'flat', 'per_lot', 'min', 'max'].map(f => `<td><input class="in sm pm-${f}" inputmode="decimal" value="${esc(r[f] && r[f] !== '0' ? r[f] : '')}" aria-label="${f}"></td>`).join('')}</tr>`; };
  modal(mhd(p ? 'Edit plan · ' + esc(p.name) : 'New brokerage plan', 'Rates apply to orders from the effective date. Earlier versions stay on record; a version on the same date is replaced.') + `<form class="bd" id="planf">
    <div class="form"><div class="fld"><label for="pm-name">Name</label><input class="in" id="pm-name" value="${esc(p?.name || '')}" required placeholder="e.g. Active trader"></div><div class="fld"><label for="pm-from">Effective from</label><input class="in" type="date" id="pm-from" value="${esc(p ? S.date : '2026-04-01')}" required></div>
    <div class="fld full"><label for="pm-note">Note</label><input class="in" id="pm-note" value="${esc(p?.note || '')}" placeholder="Who this plan is for"></div></div>
    <div class="tw"><table class="tbl pmt"><thead><tr><th>Product</th><th>Charge</th><th class="r">%</th><th class="r">₹ / order</th><th class="r">₹ / lot</th><th class="r">Min ₹</th><th class="r">Max ₹</th></tr></thead><tbody>${d.products.map(row).join('')}</tbody></table></div>
    <p class="dim small">Fill only what the charge uses: % for “% of turnover”; ₹/order for flat; both for “lower of”; ₹/lot for per lot. Min and max bound each order. Cash-market brokerage never exceeds 2.5% of the order value.</p>
    <div id="pm-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save plan</button></div></form>`);
  $('planf').onsubmit = async e => {
    e.preventDefault();
    const rules = {};
    document.querySelectorAll('.pmt tbody tr').forEach(tr => { const m = tr.querySelector('.pm-mode').value; if (m === 'inherit') return; const r = { mode: m };
      ['pct', 'flat', 'per_lot', 'min', 'max'].forEach(f => { const v = tr.querySelector('.pm-' + f).value.trim(); if (v) r[f] = v; }); rules[tr.dataset.b] = r; });
    try { await post('/v1/brokerage/plans', { id: id || '', name: $('pm-name').value, note: $('pm-note').value, from: $('pm-from').value, rules }); closeModal(); toast('Plan saved'); render({ keep: true }); }
    catch (err) { $('pm-err').innerHTML = errHTML(err); }
  };
}
function assignModal(target) {
  const d = C.brokerage; let kind = target === 'default' ? 'default' : target.startsWith('tag:') ? 'tag' : target.startsWith('ucc:') ? 'ucc' : 'tag';
  const tagV = target.startsWith('tag:') ? target.slice(4) : '', uccV = target.startsWith('ucc:') ? target.slice(4) : '';
  const cur = d.current[target]?.plan || '';
  modal(mhd('Assign a brokerage plan', 'Applies from the effective date. Earlier bills keep their brokerage unless the day is rebilled.') + `<form class="bd" id="asf">
    <div class="fld"><label>Apply to</label><div class="seg" id="as-kind">${[['default', 'Firm default'], ['tag', 'A group'], ['ucc', 'A client']].map(([k, l]) => `<button type="button" class="${kind === k ? 'on' : ''}" data-k="${k}">${l}</button>`).join('')}</div></div>
    <div class="fld as-tag" ${kind === 'tag' ? '' : 'hidden'}><label for="as-tag">Group</label><input class="in" id="as-tag" list="as-tags" value="${esc(tagV)}" placeholder="e.g. HNI"><datalist id="as-tags">${d.tags.map(t => `<option value="${esc(t.tag)}">${t.clients} clients</option>`).join('')}</datalist></div>
    <div class="fld as-ucc" ${kind === 'ucc' ? '' : 'hidden'}><label for="as-ucc-q">Client</label>${cpick('as-ucc', uccV)}</div>
    <div class="form"><div class="fld"><label for="as-plan">Plan</label><select class="in" id="as-plan">${d.plans.map(p => `<option value="${esc(p.id)}" ${cur === p.id ? 'selected' : ''}>${esc(p.name)}</option>`).join('')}</select></div>
    <div class="fld"><label for="as-from">Effective from</label><input class="in" type="date" id="as-from" value="${esc(S.date)}"></div></div>
    <div id="as-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Assign</button></div></form>`);
  $('as-kind').onclick = e => { const b = e.target.closest('button'); if (!b) return; kind = b.dataset.k; $('as-kind').querySelectorAll('button').forEach(x => x.classList.toggle('on', x === b));
    document.querySelector('.as-tag').hidden = kind !== 'tag'; document.querySelector('.as-ucc').hidden = kind !== 'ucc'; };
  $('asf').onsubmit = async e => {
    e.preventDefault();
    const t = kind === 'default' ? 'default' : kind === 'tag' ? 'tag:' + $('as-tag').value.trim() : 'ucc:' + $('as-ucc').value;
    if (t === 'tag:' || t === 'ucc:') { $('as-err').innerHTML = errHTML({ message: kind === 'tag' ? 'Choose the group' : 'Choose the client' }); return; }
    try { await post('/v1/brokerage/assignments', { target: t, plan: $('as-plan').value, from: $('as-from').value }); closeModal(); toast('Plan assigned'); render({ keep: true }); }
    catch (err) { $('as-err').innerHTML = errHTML(err); }
  };
}

/* ---------- reports ---------- */
V.reports = {
  load: () => get(`/v1/reports?date=${S.date}`),
  html: d => {
    const rs = d.reports, k = { Uploaded: 'good', Validated: 'acc', Generated: 'warn', 'Not generated': 'nt' };
    const canGen = has('Ops maker', 'Ops checker', 'Compliance officer', 'Risk manager'), canUp = has('Compliance officer', 'Ops checker');
    return ph('Regulatory reports', 'Each report is generated from the ledger and records, validated, downloaded for upload to the exchange portal, then marked uploaded with the acknowledgement number. Every step and download is in the audit trail. Column layouts follow sample formats until each exchange’s specification is loaded.') +
    `<div class="kpis">${kpi('Daily reports outstanding', rs.filter(r => r.frequency === 'Daily' && r.status !== 'Uploaded').length, 'for ' + fmtDate(S.date), 'warn')}${kpi('Uploaded', rs.filter(r => r.status === 'Uploaded').length, 'this date', 'good')}${kpi('Validated', rs.filter(r => r.status === 'Validated').length, 'ready to upload')}</div>
    <div class="panel">${tbl([['Report'], ['Submitted to'], ['When due'], ['Rows', 'r'], ['Status'], ['Actions']], rs.map(r => `<tr><td><b style="font-weight:600">${esc(r.name)}</b><div class="dim" style="font-size:11.5px">${esc(r.format)}</div></td><td>${esc(r.to)}</td><td>${esc(r.frequency)}<div class="dim" style="font-size:11.5px">${esc(r.due)}</div></td><td class="r">${r.status !== 'Not generated' ? r.rows : '—'}</td><td>${pill(r.status, k[r.status])}${r.ack ? ` <span class="tag">ACK ${esc(r.ack)}</span>` : ''}${r.by ? `<div class="dim" style="font-size:11.5px">${esc(r.by)} · ${esc(r.at)}</div>` : ''}</td><td><div class="split"><button class="btn sm" data-act="dl" data-id="/v1/reports/${r.code}/download?date=${S.date}" data-name="${r.code}_${S.date}.csv">Download</button>${canGen && r.status !== 'Uploaded' ? `<button class="btn sm" data-act="rstep" data-id="${r.code}" data-a="validate">Validate</button>` : ''}${canUp && r.status === 'Validated' ? `<button class="btn sm pri" data-act="rup" data-id="${r.code}">Mark uploaded</button>` : ''}</div></td></tr>`))}</div>
    <div class="panel"><div class="hd"><h3>Compliance calendar</h3><small>Maintained in Settings</small></div>${tbl([['Date'], ['Obligation'], ['Owner']], (d.calendar || []).map(c => `<tr><td class="n">${fmtDate(c.date)}</td><td>${esc(c.obligation)}</td><td>${esc(c.owner)}</td></tr>`), { empty: 'No calendar items.' })}</div>`;
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
    <div class="panel"><div class="hd"><h3>IPO applications</h3></div>${tbl([['Application'], ['UCC'], ['Client'], ['Issue'], ['Lots', 'r'], ['Amount (₹)', 'r'], ['UPI ID'], ['Mandate'], ['Allotment']], d.ipo.map(x => `<tr><td class="n">${esc(x.id)}</td><td class="n">${x.ucc}</td><td>${esc(x.name)}</td><td>${esc(x.issue)}</td><td class="r">${x.lots}</td><td class="r">${inr(x.amount)}</td><td class="n dim">${esc(x.upi)}</td><td>${pill(x.mandate, x.mandate === 'Accepted' ? 'good' : x.mandate === 'Declined' ? 'bad' : 'warn')}</td><td>${pill(x.allotment, x.allotted_lots > 0 ? 'good' : x.allotment === 'Awaiting' ? 'warn' : x.allotment.startsWith('Rejected') ? 'bad' : 'nt')}</td></tr>`), { empty: 'No IPO bids. Upload IPO-BIDS.' })}</div>
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
    const presets = [['day', 'Today'], ['month', 'This month'], ['q', 'This quarter'], ['fy', 'This FY'], ['lastfy', 'Last FY'], ['custom', 'Custom']];
    return ph('Client reports', 'Statements and reports for any client and period: on screen, as a printable page (save as PDF), or as CSV. Every number comes from the ledger, trade records and holdings; every print and download is in the audit trail.') +
    `<div class="rtypes">${d.defs.map(x => `<button class="${x.kind === def.kind ? 'on' : ''}" data-act="crKind" data-id="${x.kind}"><b>${esc(x.name)}</b><span>${esc(x.about)}</span></button>`).join('')}</div>
    <div class="panel"><div class="bd"><div class="crform">
      <div class="fld cr-client"><label for="cr-ucc-q">Client${def.all_ucc ? ' or group' : ''}</label>${cpick('cr-ucc', cr.ucc, { all: def.all_ucc, groups: def.all_ucc, placeholder: def.all_ucc ? 'Search name, UCC, PAN or group' : undefined, on: v => { UI.cr.ucc = v; } })}</div>
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
  html: d => ph('Audit trail', 'Every action with the user and time. The database refuses edits and deletes to this log.') + `<div class="panel"><div class="hd"><input class="in" data-ui="auditQ" data-part="audit-t" placeholder="Filter by user, action or object" value="${esc(UI.auditQ)}"><small>${d.length} entries</small></div><div id="audit-t">${P['audit-t'](d)}</div></div>`
};
P['audit-t'] = d => { const q = UI.auditQ.toLowerCase(); return tbl([['Time'], ['User'], ['Action'], ['Object'], ['Details']], d.filter(a => (a.actor + ' ' + a.action + ' ' + a.object + ' ' + JSON.stringify(a.detail || {})).toLowerCase().includes(q)).slice(0, 500).map(a => `<tr><td class="n dim">${new Date(a.at).toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'medium' })}</td><td>${esc(a.actor)}</td><td>${pill(a.action, /rejected|kill\.on|str/.test(a.action) ? 'bad' : 'acc')}</td><td class="n">${esc(a.object)}</td><td class="wrap dim">${esc(Object.entries(a.detail || {}).map(([k, v]) => k + ': ' + (typeof v === 'object' ? JSON.stringify(v) : v)).join(' · '))}</td></tr>`), { tall: true, empty: 'No actions yet' }); };

/* ---------- settings ---------- */
V.settings = {
  load: async () => { const [s, users] = await Promise.all([get('/v1/settings'), get('/v1/users')]); return { ...s, users }; },
  html: d => {
    const s = d.settings, sc = d.schedule, admin = has('Admin');
    return ph('Settings', admin ? 'Firm details, risk thresholds, calendar and users. Changes apply to new calculations; posted entries are never rewritten.' : 'Only an Admin can change settings.', admin ? '<button class="btn pri" data-act="saveSettings">Save settings</button>' : '') +
    `<div class="grid g2"><div class="panel"><div class="hd"><h3>Firm</h3></div><div class="bd"><div class="form">
      <div class="fld full"><label for="st-firm">Firm name</label><input class="in" id="st-firm" value="${esc(s.firm)}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-sebi">SEBI registration</label><input class="in" id="st-sebi" value="${esc(s.sebi_reg)}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-comp">Compliance officer email</label><input class="in" id="st-comp" value="${esc(s.compliance_email)}" ${admin ? '' : 'disabled'}></div>
    </div></div></div>
    <div class="panel"><div class="hd"><h3>Risk and settlement</h3></div><div class="bd"><div class="form">
      <div class="fld"><label for="st-warn">Watch threshold (% utilisation)</label><input class="in" id="st-warn" type="number" value="${s.warn_util_pct}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-buf">RAS retention buffer (%)</label><input class="in" id="st-buf" type="number" value="${s.ras_buffer_pct}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-rasm">Next monthly RAS date</label><input class="in" id="st-rasm" type="date" value="${esc(s.ras_monthly)}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-rasq">Next quarterly RAS date</label><input class="in" id="st-rasq" type="date" value="${esc(s.ras_quarterly)}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-inm">Inactive after (months without activity)</label><input class="in" id="st-inm" type="number" min="12" max="60" value="${s.inactive_months || 24}" ${admin ? '' : 'disabled'}></div>
      <div class="fld full"><label for="st-snap">Margin snapshot times (HH:MM:SS, comma separated)</label><input class="in mono" id="st-snap" value="${esc((s.snapshot_times || []).join(', '))}" ${admin ? '' : 'disabled'}></div>
      <div class="fld full"><label for="st-hol">Exchange holidays (YYYY-MM-DD, comma separated)</label><input class="in mono" id="st-hol" value="${esc((s.holidays || []).join(', '))}" ${admin ? '' : 'disabled'}></div>
      <label class="check full"><input type="checkbox" id="st-pen" ${s.penalty_pass_through ? 'checked' : ''} ${admin ? '' : 'disabled'}> Pass short-collection penalties through to client ledgers</label>
    </div></div></div></div>
    <div class="grid g2"><div class="panel"><div class="hd"><h3>Debit balances and interest</h3></div><div class="bd"><div class="form">
      <div class="fld"><label for="st-int">Interest on overdue debit (% a year)</label><input class="in" id="st-int" value="${esc(s.interest_pct_pa || '18')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-grace">Grace days at the start of a debit</label><input class="in" id="st-grace" type="number" min="0" max="30" value="${s.interest_grace_days || 0}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-imin">Do not charge below (₹)</label><input class="in" id="st-imin" value="${esc(s.interest_minimum || '1.00')}" ${admin ? '' : 'disabled'}></div>
      <div class="fld"><label for="st-gst">GST on charges (%)</label><input class="in" id="st-gst" value="${esc(s.gst_pct || '18')}" ${admin ? '' : 'disabled'}></div>
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
      <div class="fld full"><label for="st-gws">Payment gateway webhook secret</label><input class="in mono" id="st-gws" type="password" autocomplete="off" value="${esc(s.gateway_secret || '')}" ${admin ? '' : 'disabled'}><span class="dim small">Webhook address: <span class="mono">${esc(location.origin)}/hooks/${esc(S.me.tenant)}/gateway</span>. Payments must carry the client code in notes.ucc.</span></div>
    </div></div></div>
    <div class="panel"><div class="hd"><h3>Sign-in security</h3></div><div class="bd"><div class="form">
      <div class="fld"><label for="st-2fa">Two-factor sign-in</label><select class="in" id="st-2fa" ${admin ? '' : 'disabled'}><option value="required" ${s.two_factor !== 'optional' ? 'selected' : ''}>Required for every user</option><option value="optional" ${s.two_factor === 'optional' ? 'selected' : ''}>Optional</option></select></div>
      <div class="fld"><label for="st-idle">Sign out after inactivity (minutes)</label><input class="in" id="st-idle" type="number" min="5" max="240" value="${s.session_idle_minutes || 30}" ${admin ? '' : 'disabled'}></div>
      <p class="dim full" style="margin:0;font-size:12.5px">Passwords: at least 12 characters, stored as PBKDF2-SHA256 hashes. Five wrong attempts lock a user for 15 minutes. Sessions also end after 12 hours, at sign-out, and when an administrator resets the user.</p>
    </div></div></div>
    <div class="panel"><div class="hd"><h3>Users and roles</h3>${admin ? '<button class="btn sm pri" data-act="addUser">Add user</button>' : '<small>Maker-checker: the person who creates an override, application or entry cannot approve it</small>'}</div>${tbl([['User'], ['Role'], ['Sign-in'], ['Last sign-in'], ['Status'], ['']], d.users.map(u => `<tr><td><b style="font-weight:600">${esc(u.name)}</b><div class="mono dim" style="font-size:11.5px">${esc(u.id)}</div></td><td>${admin && u.id !== S.me.actor ? `<select class="in sm" data-urole="${esc(u.id)}">${(S.me.roles || []).map(r => `<option ${r === u.role ? 'selected' : ''}>${esc(r)}</option>`).join('')}</select>` : esc(u.role)}</td>
      <td>${u.has_password ? '' : pill('No password', 'warn') + ' '}${u.totp_enabled ? pill('2FA on', 'good') : pill('2FA off', 'nt')}${u.must_change_password ? ' ' + pill('Temp password', 'warn') : ''}${u.locked ? ' ' + pill('Locked', 'bad') : ''}</td><td class="n dim">${u.last_login ? new Date(u.last_login).toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'short' }) : '—'}</td>
      <td>${u.active ? pill('Active', 'good') : pill('Inactive', 'nt')}</td><td>${admin && u.id !== S.me.actor ? `<div class="split"><button class="btn sm" data-act="resetPw" data-id="${esc(u.id)}">Reset password</button>${u.totp_enabled ? `<button class="btn sm" data-act="reset2fa" data-id="${esc(u.id)}">Reset 2FA</button>` : ''}<button class="btn sm ${u.active ? 'bad' : ''}" data-act="toggleUser" data-id="${esc(u.id)}">${u.active ? 'Deactivate' : 'Activate'}</button></div>` : ''}</td></tr>`))}</div>
    <div class="panel"><div class="hd"><h3>Charge rate schedule</h3><small>${esc(sc.version)}${sc.sample ? ' · ' + pill('Sample rates', 'warn') : ''}</small></div>${sc.sample ? `<div class="bd" style="padding-bottom:0"><div class="note warn">${esc(sc.note)}</div></div>` : ''}${tbl([['Product'], ['Exchange'], ['From'], ['Brokerage'], ['STT buy %', 'r'], ['STT sell %', 'r'], ['Txn %', 'r'], ['Stamp buy %', 'r']], sc.rules.map(r => `<tr><td class="n">${r.bucket}</td><td>${r.exchange}</td><td class="n">${r.effective_from}</td><td>${r.brokerage_mode === 'none' ? 'Nil' : r.brokerage_mode === 'flat' ? '₹' + r.brokerage_flat + ' per order' : r.brokerage_mode === 'pct' ? r.brokerage_pct + '%' : `lower of ₹${r.brokerage_flat} or ${r.brokerage_pct}%`}</td><td class="r">${r.stt_buy_pct}</td><td class="r">${r.stt_sell_pct}</td><td class="r">${r.txn_pct}</td><td class="r">${r.stamp_buy_pct}</td></tr>`))}<div class="bd"><span class="dim" style="font-size:12px">GST ${sc.gst_pct}% · SEBI fee ${sc.sebi_fee_pct}% · The schedule is a versioned file on the server (config/charges); a new circular means a new version with an effective date.</span></div></div>`;
  }
};

/* ============================== modals ============================== */
function modal(html, drawer) { $('mroot').innerHTML = `<div class="ov ${drawer ? 'drawer' : ''}" data-act="ovclose"><div class="md" role="dialog" aria-modal="true">${html}</div></div>`; const f = document.querySelector('.md textarea,.md input:not([type=checkbox]),.md select'); if (f) setTimeout(() => f.focus(), 10); }
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
  catch (e) { toast(e.message); throw e; }
}
const A = {
  shortSettle: id => { const x = C.settlement.shortages.find(s => s.id === id); confirmModal('Close out ' + id, `${x.ucc} short ${x.qty} ${x.symbol} · ${x.internal} internal, ${x.auction} exchange auction`, `<div class="form"><div class="fld"><label for="ss-p">Auction / close-out price (₹)</label><input class="in" id="ss-p" value="${esc(x.suggested)}"></div><div class="fld"><label for="ss-d">Date</label><input class="in" type="date" id="ss-d" value="${esc(S.date)}"></div></div><div class="note">Debits ${x.ucc} the price × ${x.qty}. ${x.buyers.length ? 'Credits ' + x.buyers.map(b => b.ucc + ' for ' + b.short).join(', ') + ' and pays out the rest of their purchase.' : 'The clearing corporation recovers the auction cost.'}</div>`, 'Close out', () => act(() => post(`/v1/shortages/${id}/settle`, { date: $('ss-d').value, price: $('ss-p').value.trim() }), 'Short delivery closed out')); },
  newCA: () => { modal(mhd('Record a corporate action', 'From the exchange or registrar announcement. A checker applies it on the record date.') + `<form class="bd" id="caf"><div class="form">
      <div class="fld"><label for="ca-t">Type</label><select class="in" id="ca-t"><option>Dividend</option><option>Bonus</option><option>Split</option></select></div>
      <div class="fld"><label for="ca-s">Symbol</label><input class="in mono" id="ca-s" required></div><div class="fld"><label for="ca-i">ISIN</label><input class="in mono" id="ca-i" required></div>
      <div class="fld"><label for="ca-x">Ex-date</label><input class="in" type="date" id="ca-x" value="${esc(S.date)}"></div><div class="fld"><label for="ca-r">Record date</label><input class="in" type="date" id="ca-r" value="${esc(S.date)}"></div>
      <div class="fld"><label for="ca-p">Payment / allotment date</label><input class="in" type="date" id="ca-p"></div>
      <div class="fld"><label for="ca-d">Dividend per share (₹)</label><input class="in" id="ca-d" placeholder="dividend only"></div>
      <div class="fld"><label for="ca-n">Bonus: new shares · Split: new face value</label><input class="in" id="ca-n" type="number" min="1"></div><div class="fld"><label for="ca-o">Bonus: per shares held · Split: old face value</label><input class="in" id="ca-o" type="number" min="1"></div>
    </div><div id="ca-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Record</button></div></form>`);
    $('caf').onsubmit = async e => { e.preventDefault(); try { await post('/v1/corporate-actions', { type: $('ca-t').value, symbol: $('ca-s').value, isin: $('ca-i').value, ex_date: $('ca-x').value, record_date: $('ca-r').value, pay_date: $('ca-p').value, dps: $('ca-d').value || '0', ratio_new: parseInt($('ca-n').value, 10) || 0, ratio_old: parseInt($('ca-o').value, 10) || 0 }); closeModal(); toast('Corporate action recorded'); render({ keep: true }); } catch (err) { $('ca-err').innerHTML = errHTML(err); } }; },
  caApply: id => { const c = C.corp.ca.find(x => x.id === id); confirmModal('Apply ' + id, `${c.type} · ${c.symbol}`, `${tbl([['Client'], ['Demat', 'r'], ['With firm', 'r'], [c.type === 'Dividend' ? 'Dividend' : 'New shares', 'r']], c.lines.map(l => `<tr><td>${l.ucc} ${esc(l.name)}</td><td class="r">${l.held}</td><td class="r">${l.broker_held}</td><td class="r">${c.type === 'Dividend' ? nf(num(l.dividend) + num(l.via_broker), 2) : l.new_shares}</td></tr>`), { empty: 'No client holds this scrip.' })}<div class="note">${c.type === 'Dividend' ? 'Freezes the entitlements. Pass on the dividend on broker-held shares once it reaches the bank.' : 'Adjusts every client’s holding and average cost now; purchase lots are adjusted from the ex-date.'}</div>`, 'Apply', () => act(() => post(`/v1/corporate-actions/${id}/apply`, {}), 'Corporate action applied')); },
  caDiv: id => { const c = C.corp.ca.find(x => x.id === id); confirmModal('Dividend received · ' + c.symbol, `Owed to clients on broker-held shares: ${inr(c.recon.expected, 2)}`, `<div class="form"><div class="fld"><label for="dv-a">Amount received (₹)</label><input class="in" id="dv-a" value="${esc(c.recon.expected)}"></div><div class="fld"><label for="dv-r">Bank reference</label><input class="in mono" id="dv-r"></div><div class="fld"><label for="dv-d">Date</label><input class="in" type="date" id="dv-d" value="${esc(S.date)}"></div></div>`, 'Credit clients', () => act(() => post(`/v1/corporate-actions/${id}/dividend`, { date: $('dv-d').value, ref: $('dv-r').value.trim(), received: $('dv-a').value.trim() }), 'Dividend credited to clients')); },
  newBB: () => { modal(mhd('Record a buy-back offer', 'Tender offer through the exchange acquisition window.') + `<form class="bd" id="bbf"><div class="form"><div class="fld"><label for="bb-s">Symbol</label><input class="in mono" id="bb-s" required></div><div class="fld"><label for="bb-i">ISIN</label><input class="in mono" id="bb-i" required></div><div class="fld full"><label for="bb-c">Company</label><input class="in" id="bb-c"></div><div class="fld"><label for="bb-p">Offer price (₹)</label><input class="in" id="bb-p" required></div><div class="fld"><label for="bb-r">Record date</label><input class="in" type="date" id="bb-r" value="${esc(S.date)}"></div><div class="fld"><label for="bb-o">Tenders open</label><input class="in" type="date" id="bb-o" value="${esc(S.date)}"></div><div class="fld"><label for="bb-x">Tenders close</label><input class="in" type="date" id="bb-x"></div></div><div id="bb-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Record</button></div></form>`);
    $('bbf').onsubmit = async e => { e.preventDefault(); try { await post('/v1/buybacks', { symbol: $('bb-s').value, isin: $('bb-i').value, company: $('bb-c').value, price: $('bb-p').value, record_date: $('bb-r').value, open: $('bb-o').value, close: $('bb-x').value }); closeModal(); UI.caTab = 'bb'; toast('Buy-back recorded'); render({ keep: true }); } catch (err) { $('bb-err').innerHTML = errHTML(err); } }; },
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
  selAll: (id, el) => { const on = el.checked; document.querySelectorAll('[data-act=selOne]').forEach(x => on ? UI.clientSel.add(x.dataset.id) : UI.clientSel.delete(x.dataset.id)); rerenderPart('clients-t'); },
  bulkClear: () => { UI.clientSel.clear(); rerenderPart('clients-t'); },
  filterTag: t => { UI.clientTag = t; if (S.view !== 'clients') { A.go('clients'); return; } closeModal(); render({ keep: true }); },
  bulkTag: async op => {
    const t = ($('bulk-tag')?.value || '').trim(); if (!t) { toast('Type a group name'); $('bulk-tag')?.focus(); return; }
    const uccs = [...UI.clientSel];
    try { const r = await post('/v1/clients/tags', { uccs, add: op === 'add' ? [t] : [], remove: op === 'remove' ? [t] : [] }); toast(`${r.changed} client${r.changed === 1 ? '' : 's'} ${op === 'add' ? 'added to' : 'removed from'} ${t.toUpperCase()}`); UI.clientSel.clear(); render({ keep: true }); }
    catch (e) { toast(e.message); }
  },
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
  assign: (target, el) => assignModal(target || '', el?.dataset.plan || ''),
  unassign: target => confirmModal('Remove plan · ' + target.replace(/^(ucc|tag):/, ''), 'From the date below this ' + (target.startsWith('ucc:') ? 'client' : 'group') + ' falls back to the next rule (group, then firm default).', `<div class="fld"><label for="un-from">Effective from</label><input class="in" type="date" id="un-from" value="${esc(S.date)}"></div>`,
    'Remove', () => act(() => post('/v1/brokerage/assignments', { target, plan: '', from: $('un-from').value }), 'Plan removed'), true),
  umenu: (id, el, e) => { const m = $('umenu'); m.hidden = !m.hidden; },
  signOut: async () => { try { await api('POST', '/v1/auth/logout', {}); } catch (e) {} showLogin('You have signed out.'); },
  switchUser: async id => { try { await api('POST', '/v1/auth/logout', {}); } catch (e) {} await doLogin(id, CFG.dev_password, ''); },
  devLogin: id => doLogin(id, CFG.dev_password, ''),
  loginBack: () => { loginState = { otp: false, user: '', msg: '' }; loginPw = ''; $('page').innerHTML = loginHTML(); },
  pwChange: () => pwModal(false),
  tempDone: () => { closeModal(); render({ keep: true }); },
  totp: () => totpModal(false),
  addUser: () => confirmModal('Add user', 'They get a one-time password to change at first sign-in.', `<div class="form"><div class="fld"><label for="nu-id">User ID</label><input class="in" id="nu-id" placeholder="e.g. meena.r" autocapitalize="none"></div><div class="fld"><label for="nu-name">Full name</label><input class="in" id="nu-name"></div><div class="fld"><label for="nu-role">Role</label><select class="in" id="nu-role">${(S.me.roles || []).map(r => `<option>${esc(r)}</option>`).join('')}</select></div></div>`,
    'Add user', async () => { const r = await post('/v1/users', { id: $('nu-id').value.trim(), name: $('nu-name').value.trim(), role: $('nu-role').value }); showTempPw(r.user, r.temp_password, 'created'); }),
  resetPw: id => confirmModal('Reset password · ' + id, 'Gives a new one-time password and signs them out everywhere.', '', 'Reset password', async () => { const r = await post(`/v1/users/${encodeURIComponent(id)}/reset-password`); showTempPw(id, r.temp_password, 'reset'); }, true),
  reset2fa: id => confirmModal('Reset two-factor · ' + id, 'For a lost phone. Signs them out; they set it up again at next sign-in.', '', 'Reset 2FA', () => act(() => post(`/v1/users/${encodeURIComponent(id)}/reset-2fa`), 'Two-factor sign-in reset'), true),
  toggleUser: id => { const u = C.settings.users.find(x => x.id === id); act(() => api('PUT', `/v1/users/${encodeURIComponent(id)}`, { id, name: u.name, role: u.role, active: !u.active }), u.active ? id + ' deactivated' : id + ' activated'); },
  crKind: id => { crSync(); UI.cr.kind = id; UI.crRes = null; UI.crErr = null; $('page').innerHTML = V.statements.html(C.statements); bindPage(); },
  crPreset: id => { crSync(); UI.cr.preset = id; $('page').innerHTML = V.statements.html(C.statements); bindPage(); },
  crRun: () => { crSync(); crRun(); },
  crPrint: () => { crSync(); openDoc(crURL('html')); },
  crCSV: () => { crSync(); const r = UI.cr; download(crURL('csv'), `${r.kind}_${r.ucc}_${r.from ? r.from + '_' : ''}${r.to}.csv`); },
  crFor: id => { UI.cr.ucc = id; UI.crRes = null; A.go('statements'); },
  go: id => { S.view = id; closeModal(); render(); window.scrollTo(0, 0); },
  close: () => closeModal(),
  ovclose: (id, el, e) => { if (e.target === el && !modalLocked) closeModal(); },
  ui: (id, el) => { UI[el.dataset.k] = el.dataset.v; const part = el.dataset.part; if (part && $(part)) { rerenderPart(part); el.parentElement.querySelectorAll('button').forEach(b => b.classList.toggle('on', b === el)); } else { $('page').innerHTML = V[S.view].html(C[S.view]); bindPage(); } },
  dl: (path, el) => download(path, el.dataset.name),
  client: id => clientDrawer(id),
  openLedger: id => { UI.ledgerU = id; A.go('ledger'); },
  sample: async (id, el) => { if (el) el.disabled = true; toast('Loading the sample trading day…'); try { const r = await post('/v1/dev/sample', { date: S.date }); toast(`Loaded ${r.files} sample files for ${fmtDate(S.date)}`); } catch (e) { toast(e.message); } render({ keep: true }); },
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
    } catch (e) { toast(e.message); }
    render({ keep: true });
  },
  gotoDate: id => { S.date = id; C.billResult = null; closeModal(); render(); },
  prevDay: () => A.gotoDate(prevBD(S.date)),
  nextDay: () => A.gotoDate(nextBD(S.date)),
  allotSample: () => act(() => post('/v1/dev/sample', { date: S.date, later: true }), 'Allotment file applied'),
  runRecon: () => act(() => post('/v1/recon/runs', { date: S.date }), r => `Reconciled: ${r.result.matched} matched, ${r.open} open breaks`),
  brk: id => breakModal(id),
  resolve: id => { const note = ($('bnote')?.value || '').trim(); if (!note) { toast('Write what you checked and fixed first'); $('bnote')?.focus(); return; } act(() => post(`/v1/recon/breaks/${encodeURIComponent(id)}/resolve`, { resolution: note }), 'Break resolved'); },
  ccm: id => act(() => post(`/v1/recon/breaks/${encodeURIComponent(id)}/ccm`, { reason: ($('bnote')?.value || '').trim() }), c => `Client code modified ${c.from} → ${c.to}. Rebill the day to move the charges.`),
  bill: async id => { try { C.billResult = await post('/v1/billing/runs', { date: S.date, rebill: id === '1' }); toast(`${C.billResult.posted.length} bills posted`); } catch (e) { C.billResult = { error: e }; toast('Billing did not run'); } render({ keep: true }); },
  settle: (key, el) => act(() => post('/v1/settlement/action', { date: S.date, key, action: el.dataset.a }), o => `${o.ucc} ${o.symbol}: ${o.status}`),
  settleReady: () => act(() => post('/v1/settlement/settle-ready', { date: S.date }), r => `${r.settled} lines settled`),
  cuspaRel: id => act(() => post(`/v1/cuspa/${id}/release`), 'Released to the client’s demat'),
  genNotes: () => act(() => post('/v1/notes/generate', { date: S.date }), r => `${r.generated} contract notes generated`),
  sendNotes: () => act(() => post('/v1/notes/send', { date: S.date }), r => `${r.sent} sent, ${r.bounced} bounced`),
  sendNote: id => act(() => post('/v1/notes/send', { date: S.date, ucc: id }), r => r.sent ? 'Sent' : 'Bounced: check the email address'),
  noteDoc: id => openDoc(`/v1/notes/${S.date}/${id}/document`),
  newClient: () => { UI.ob = { step: 0, data: { type: 'Individual', kyc_route: 'DigiLocker + Aadhaar e-KYC', depository: 'CDSL', segments: ['CM'], income: '5-10 L', ras: 'Quarterly', nominee: 'Opted in' }, err: {} }; obModal(); },
  obBack: () => { UI.ob.step--; UI.ob.err = {}; obModal(); },
  obNext: async () => {
    if (!obValidate()) { obModal(); return; }
    if (UI.ob.step < 4) { UI.ob.step++; obModal(); return; }
    const d = UI.ob.data;
    const body = { name: d.name, type: d.type, pan: d.pan, mobile: d.mobile, email: d.email, city: d.city, kyc_route: d.kyc_route, segments: d.segments, income: d.income, ras: d.ras,
      bank: { bank: d.bank, ifsc: d.ifsc, account: d.account }, demat: { depository: d.depository, id: d.demat_id || '' }, nominee: d.nominee, nominee_name: d.nominee_name || '',
      income_proof: !!d.income_proof, risk_disclosure: !!d.risk_disclosure, ipv_scheduled: !!d.ipv_scheduled };
    try { const c = await post('/v1/clients', body); UI.ob = null; closeModal(); UI.clientStage = 'KYC verification'; toast(`Application ${c.ucc} created`); render({ keep: true }); }
    catch (e) { UI.ob.err = { _server: e }; obModal(); }
  },
  advance: id => act(() => post(`/v1/clients/${id}/advance`), c => `${c.ucc} moved to ${c.status}`),
  kill: (id, el) => {
    const on = el.dataset.b === '1';
    if (!on) { act(() => post(`/v1/clients/${id}/block`, { blocked: false }), id + ' unblocked'); return; }
    confirmModal('Kill switch · ' + id, 'Sets the OMS limit to zero until unblocked.', `<div class="fld"><label for="ks-r">Reason</label><input class="in" id="ks-r" placeholder="e.g. Margin breach unresolved after call"></div>`, 'Block client', () => act(() => post(`/v1/clients/${id}/block`, { blocked: true, reason: $('ks-r').value }), id + ' blocked'), true);
  },
  override: id => confirmModal('Limit override · ' + id, 'Temporary extra limit for ' + fmtDate(S.date) + '. A checker or risk manager who is not you must approve it.',
    `<div class="form"><div class="fld"><label for="ov-amt">Amount (₹)</label><input class="in" id="ov-amt" type="number" min="1000" step="1000" value="50000"></div><div class="fld"><label for="ov-r">Reason</label><select class="in" id="ov-r"><option>Funds in transit (UTR shared)</option><option>Pledge request pending at depository</option><option>Relationship manager guarantee</option><option>Hedged position, margin benefit pending</option></select></div></div>`,
    'Submit for approval', () => act(() => post('/v1/rms/overrides', { date: S.date, ucc: id, amount: $('ov-amt').value, reason: $('ov-r').value }), 'Override sent for approval')),
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
  saveSettings: () => {
    const s = { ...C.settings.settings, firm: $('st-firm').value, sebi_reg: $('st-sebi').value, compliance_email: $('st-comp').value, warn_util_pct: parseInt($('st-warn').value, 10), ras_buffer_pct: parseInt($('st-buf').value, 10), ras_monthly: $('st-rasm').value, ras_quarterly: $('st-rasq').value,
      two_factor: $('st-2fa').value, inactive_months: parseInt($('st-inm').value, 10) || 24, session_idle_minutes: parseInt($('st-idle').value, 10) || 30, snapshot_times: $('st-snap').value.split(',').map(x => x.trim()).filter(Boolean), holidays: $('st-hol').value.split(',').map(x => x.trim()).filter(Boolean), penalty_pass_through: $('st-pen').checked,
      interest_pct_pa: $('st-int').value.trim(), interest_grace_days: parseInt($('st-grace').value, 10) || 0, interest_minimum: $('st-imin').value.trim(), gst_pct: $('st-gst').value.trim(), interest_exempt_tags: $('st-iex').value.split(',').map(x => x.trim().toUpperCase()).filter(Boolean), debit_block_days: parseInt($('st-dbd').value, 10) || 7, locked_up_to: $('st-lock').value, cheque_bounce_charge: $('st-bnc').value.trim(), payout_debit_account: $('st-pda').value.trim(), gateway_secret: $('st-gws').value, auto_lock_days: parseInt($('st-alock').value, 10) || 0 };
    act(() => api('PUT', '/v1/settings', s), 'Settings saved').then(loadMe);
  },
  eod: async () => {
    let st; try { st = await get(`/v1/eod?date=${S.date}`); } catch (e) { toast(e.message); return; }
    const okAll = st.checks.every(c => c.ok);
    if (st.closed) { modal(mhd('EOD · ' + fmtDate(S.date), `Closed at ${esc(st.closed_at)} by ${esc(st.closed_by)}`) + `<div class="bd">${st.next_day ? `<div class="note"><b>${st.rolled_positions} open position${st.rolled_positions === 1 ? '' : 's'} carried to ${fmtDate(st.next_day)}.</b> Futures at today's settlement price; options at their average premium; expired contracts dropped.</div>` : ''}${(st.exceptions || []).length ? `<div class="note warn"><b>Closed with exceptions</b><ul style="margin:6px 0 0;padding-left:18px">${st.exceptions.map(x => `<li>${esc(x)}</li>`).join('')}</ul></div>` : '<div class="note">Closed clean.</div>'}</div><div class="ft"><button class="btn" data-act="close">Close</button></div>`); return; }
    confirmModal('EOD close · ' + fmtDate(S.date), okAll ? 'All checks passed' : 'Resolve what you can; closing with exceptions needs a note', `${st.previous_open ? `<div class="note warn">${esc(st.previous_open)}</div>` : ''}<div class="checks">${st.checks.map(c => `<div><span>${esc(c.name)}<br><span class="dim" style="font-size:12px">${esc(c.detail)}</span></span>${c.ok ? pill('Done', 'good') : `<button class="btn sm" data-act="go" data-id="${c.view}">Open</button>`}</div>`).join('')}</div>${okAll ? '' : '<div class="fld"><label for="eod-n">Note for closing with exceptions</label><textarea class="in" id="eod-n"></textarea></div>'}${has('Ops checker') ? '' : '<div class="note">Closing the day needs an Ops checker.</div>'}`,
      okAll ? 'Close the day' : 'Close with exceptions', () => act(() => post('/v1/eod/close', { date: S.date, note: $('eod-n')?.value || '' }), r => `Day closed; ${r.rolled_positions} positions carried to ${fmtDate(r.next_day)}`), !okAll);
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
document.addEventListener('change', e => {
  const el = e.target;
  if (el.id === 'date' && el.value) { S.date = el.value; C.billResult = null; render(); return; }
  if (el.dataset.urole) { const id = el.dataset.urole, u = C.settings.users.find(x => x.id === id); act(() => api('PUT', `/v1/users/${encodeURIComponent(id)}`, { id, name: u.name, role: el.value, active: u.active }), `${id} is now ${el.value}`); return; }
  if (el.id === 'ftype') { UI.fileType = el.value; $('page').innerHTML = V.files.html(C.files); bindPage(); return; }
  if (el.id === 'cr-from' || el.id === 'cr-to') { UI.cr.preset = 'custom'; crSync(); document.querySelectorAll('[data-act=crPreset]').forEach(b => b.classList.toggle('on', b.dataset.id === 'custom')); return; }
  if (el.id === 'bkPlan') { UI.bkPlan = el.value; rerenderPart('bk-t'); return; }
  if (el.id === 'clientTag') { UI.clientTag = el.value; rerenderPart('clients-t'); return; }
  if (el.id === 'ledgerU') { UI.ledgerU = el.value; render({ keep: true }); return; }
  if (el.dataset.calc) { UI.calc[el.dataset.calc] = el.value; if (el.dataset.calc === 'bucket') { $('page').innerHTML = V.charges.html(); bindPage(); } else calc(); return; }
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

/* ============================== start ============================== */
async function loadMe() { S.me = await get('/v1/me'); document.body.classList.remove('signed-out'); persist(); }
async function boot() {
  try { CFG = await (await fetch('/ui/config')).json(); } catch (e) { CFG = {}; }
  try { await loadMe(); } catch (e) { if (e.status !== 401 && e.code !== 'setup_required') showLogin(e.message); return; }
  render();
}
boot();
