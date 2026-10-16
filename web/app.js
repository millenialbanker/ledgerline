'use strict';
/* Ledgerline console. Every number on screen comes from the server's API;
   this page holds no business data of its own. */

/* ============================== state ============================== */
const LS = 'ledgerline-console-v2';
const S = { key: '', actor: '', date: '2026-10-16', view: 'dashboard', me: null, counts: {} };
try { Object.assign(S, JSON.parse(localStorage.getItem(LS) || '{}')); } catch (e) {}
const persist = () => { try { localStorage.setItem(LS, JSON.stringify({ key: S.key, actor: S.actor, date: S.date, view: S.view })); } catch (e) {} };
const UI = { cr: { kind: 'ledger', ucc: '', preset: 'fy', from: '', to: '' }, crRes: null, crErr: null, crBusy: false, reconF: 'OPEN', reconX: 'All', reconQ: '', clientStage: 'All', clientQ: '', rmsF: 'All', rmsQ: '', settleTab: 'cm', noteF: 'All',
  bankF: 'All', rasF: 'due', auditQ: '', ledgerU: '', ledgerTab: 'statement', calc: { bucket: 'CM-DEL', exchange: 'NSE', side: 'B', qty: 100, price: '1412' },
  fileType: 'NSE-CM-TRADE', lastUpload: null, ob: null, search: '' };
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
  const h = { 'Authorization': 'Bearer ' + S.key };
  if (S.actor) h['X-Actor'] = S.actor;
  let b;
  if (raw) b = body; else if (body !== undefined) { b = JSON.stringify(body); h['Content-Type'] = 'application/json'; }
  const res = await fetch(path, { method, headers: h, body: b });
  const ct = res.headers.get('Content-Type') || '';
  let data = null;
  if (ct.includes('json')) { try { data = await res.json(); } catch (e) {} } else data = await res.text();
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
    const res = await fetch(path, { headers: { 'Authorization': 'Bearer ' + S.key, 'X-Actor': S.actor } });
    if (!res.ok) { let m = 'Download failed'; try { m = (await res.json()).error.message; } catch (e) {} toast(m); return; }
    const blob = await res.blob(); const url = URL.createObjectURL(blob);
    const a = document.createElement('a'); a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 4000); toast('Downloaded ' + name);
  } catch (e) { toast(e.message); }
}
async function openDoc(path) {
  const w = window.open('', '_blank');
  try {
    const res = await fetch(path, { headers: { 'Authorization': 'Bearer ' + S.key, 'X-Actor': S.actor } });
    const html = await res.text();
    if (!res.ok) throw new Error('Could not open the document');
    const url = URL.createObjectURL(new Blob([html], { type: 'text/html' }));
    if (w) w.location = url; else location.href = url;
  } catch (e) { if (w) w.close(); toast(e.message); }
}

/* ============================== shell ============================== */
const NAV = [
  ['Overview', [['dashboard', 'Dashboard']]],
  ['Operations', [['files', 'Files'], ['recon', 'Trade reconciliation'], ['billing', 'Billing'], ['settlement', 'Settlement'], ['notes', 'Contract notes'], ['clients', 'Clients & onboarding'], ['statements', 'Client reports']]],
  ['Risk', [['rms', 'RMS & limits'], ['margin', 'Margin reporting'], ['collateral', 'Collateral & pledge']]],
  ['Accounts', [['ledger', 'Ledger'], ['bank', 'Bank reconciliation'], ['ras', 'Running account'], ['charges', 'Charges calculator']]],
  ['Compliance', [['reports', 'Regulatory reports'], ['surv', 'Surveillance & AML'], ['ipo', 'IPO & mutual funds'], ['audit', 'Audit trail']]],
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
  $('top').innerHTML = `<div class="tdate"><small>Trade date</small><input class="in mono" type="date" id="date" value="${esc(S.date)}" aria-label="Trade date" style="height:30px"></div>
  <div class="chips">${d?.extra?.pay_in ? `<span class="chip">Pay-in <b>${fmtDate(d.extra.pay_in)}</b></span>` : ''}${S.me ? `<span class="chip">EOD <b>${d?.eod?.closed ? 'closed ' + esc(d.eod.closed_at) : 'open'}</b></span>` : ''}</div>
  <div class="srch"><svg viewBox="0 0 24 24" fill="none" stroke-width="2"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/></svg><input id="gsearch" placeholder="Find client by name, UCC or PAN  ( / )" autocomplete="off" value="${esc(UI.search)}"><div class="res" id="gres" hidden></div></div>
  <label class="who"><span class="dim" style="font-size:12px">Signed in as</span><select id="actor">${users.map(u => `<option value="${esc(u.id)}" ${u.id === S.actor ? 'selected' : ''}>${esc(u.name)} · ${esc(u.role)}</option>`).join('')}</select></label>`;
}
async function refreshCounts() {
  try {
    const d = await get(`/v1/dashboard?date=${S.date}`);
    C.dashboard = d;
    S.counts = { recon: d.recon.OPEN || 0, rms: d.risk.Breach || 0, collateral: d.pending_pledges || 0, bank: (d.bank['Third-party'] || 0) + (d.bank.Unidentified || 0),
      surv: d.alerts.open || 0, clients: d.onboarding || 0, notes: (d.notes.total || 0) - (d.notes.Sent || 0), reports: d.reports_due || 0 };
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
    if (e.status === 401) { signIn('Your API key was not accepted. Enter it again.'); return; }
    p.innerHTML = ph('Something went wrong', '') + errHTML(e);
  }
  refreshCounts();
}
function rerenderPart(part) { const el = $(part); if (el && P[part]) el.innerHTML = P[part](C[S.view]); bindPage(); }

/* ============================== sign-in ============================== */
function signIn(msg) {
  modal(`<div class="hd"><div><h2>Connect to Ledgerline</h2><p>${esc(msg || 'Enter your firm’s API key.')}</p></div></div>
  <form class="bd" id="signin"><div class="fld"><label for="si-key">API key</label><input class="in" id="si-key" required autocomplete="off" value="${esc(S.key || CFG.dev_key || '')}"></div>
  ${CFG.dev ? '<p class="dim" style="margin:0;font-size:12.5px">Development server: the demo key is filled in for you.</p>' : ''}
  <div class="ft" style="padding:0;border:0"><button class="btn pri" type="submit">Connect</button></div></form>`);
  $('signin').onsubmit = async e => { e.preventDefault(); S.key = $('si-key').value.trim(); closeModal(); await boot(); };
}

/* ============================== views ============================== */
const V = {}, P = {};

/* ---------- dashboard ---------- */
V.dashboard = {
  load: () => get(`/v1/dashboard?date=${S.date}`),
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
    if (d.reports_due) att.push(['acc', 'reports', `${d.reports_due} daily report${d.reports_due > 1 ? 's' : ''} not uploaded`, 'Generate, validate and upload']);
    const stepCls = (done, alert) => done ? (alert ? 'alert' : 'done') : 'pend';
    const notesSent = d.notes.Sent || 0;
    return ph('Operations dashboard', `${esc(S.me.firm)} · ${fmtDate(S.date)}`, `<button class="btn" data-act="go" data-id="recon">Open break queue</button><button class="btn pri" data-act="eod">Run EOD close</button>`) +
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
  return `<div class="panel"><div class="bd"><div class="samplebar"><div><b>No trades loaded for ${fmtDate(S.date)}.</b><div class="dim">Upload the day’s files on the Files page${S.me?.dev ? ', or load the sample trading day: about 60 clients, 350 trades across NSE, BSE and MCX, holdings, positions, a bank statement and exchange alerts, all as real files through the normal loaders.' : '.'}</div></div>
  <div class="acts"><button class="btn" data-act="go" data-id="files">Upload files</button>${S.me?.dev ? '<button class="btn pri" data-act="sample">Load sample trading day</button>' : ''}</div></div></div></div>`;
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
  load: async () => { const [journal, ex, market] = await Promise.all([get(`/v1/journal?date=${S.date}`), get(`/v1/trades?date=${S.date}&origin=EXCHANGE`), get(`/v1/market?date=${S.date}`).catch(() => null)]); return { journal, ex, market }; },
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
    <div class="panel"><div class="hd"><h3>Bills</h3><small>Negative = client owes the broker</small></div>${tbl([['UCC'], ['Segment'], ['Trades', 'r'], ['Trade value / MTM', 'r'], ['Brokerage', 'r'], ['STT', 'r'], ['Exch txn', 'r'], ['Stamp', 'r'], ['GST', 'r'], ['Net to client', 'r'], ['Entry']],
      rows.map(r => `<tr><td class="n"><button class="linkish" data-act="client" data-id="${r.ucc}">${r.ucc}</button></td><td>${esc(r.seg)}</td><td class="r">${r.trades}</td><td class="r ${sgn(r.tv)}">${nf(r.tv, 2)}</td><td class="r">${nf(r.c.brok, 2)}</td><td class="r">${nf(r.c.stt, 2)}</td><td class="r">${nf(r.c.txn, 2)}</td><td class="r">${nf(r.c.stamp, 2)}</td><td class="r">${nf(r.c.gst, 2)}</td><td class="r ${sgn(r.tv - r.ch)}">${nf(r.tv - r.ch, 2)}</td><td class="n dim">${esc(r.e.id)}${r.e.key.includes('#r') ? ' ' + pill('revised', 'acc') : ''}</td></tr>`),
      { tall: true, empty: 'No bills posted for this date yet.', foot: rows.length ? `<tfoot><tr><td colspan="3">Total</td><td class="r">${nf(sum(rows, r => r.tv), 2)}</td><td class="r">${nf(charges.brok, 2)}</td><td class="r">${nf(charges.stt, 2)}</td><td class="r">${nf(charges.txn, 2)}</td><td class="r">${nf(charges.stamp, 2)}</td><td class="r">${nf(charges.gst, 2)}</td><td class="r">${nf(sum(rows, r => r.tv - r.ch), 2)}</td><td></td></tr></tfoot>` : '' })}</div>`;
  }
};
function billNote(r) {
  if (r.error) return errHTML(r.error);
  return `<div class="note ${r.conflicts.length ? 'warn' : ''}"><b>${r.posted.length} bill${r.posted.length === 1 ? '' : 's'} posted, ${r.existing.length} unchanged${r.reversed.length ? `, ${r.reversed.length} reversed` : ''}${r.conflicts.length ? `, ${r.conflicts.length} changed since billing (use Rebill)` : ''}.</b>${r.warnings.length ? `<ul style="margin:6px 0 0;padding-left:18px">${r.warnings.map(w => `<li>${esc(w)}</li>`).join('')}</ul>` : ''}</div>`;
}

/* ---------- settlement ---------- */
V.settlement = {
  load: () => get(`/v1/settlement?date=${S.date}`),
  html: d => {
    const exc = d.obligations.filter(o => ['Short delivery', 'Funds shortfall', 'Unpledge & pay-in'].includes(o.status));
    const ready = d.obligations.filter(o => o.action === 'payin' || o.action === 'payout').length;
    return ph('Settlement', `Cash-market delivery obligations for pay-in on ${fmtDate(d.pay_in)} (T+1). Securities pay-in from the client’s demat; payout credited to the client’s demat; unpaid purchases held in the client unpaid securities pledgee account.`,
      `<button class="btn" data-act="dl" data-id="/v1/reports/trades/download?date=${S.date}" data-name="trades_${S.date}.csv">Trade register</button>${has('Ops maker', 'Ops checker') ? `<button class="btn pri" data-act="settleReady" ${ready ? '' : 'disabled'}>Settle ${ready} ready line${ready === 1 ? '' : 's'}</button>` : ''}`) +
    `<div class="kpis">${kpi('Funds pay-in (buys)', cmp(d.totals.funds_pay_in), d.obligations.filter(o => o.net_qty > 0).length + ' client-scrip lines')}${kpi('Funds payout (sells)', cmp(d.totals.funds_pay_out), d.obligations.filter(o => o.net_qty < 0).length + ' lines')}${kpi('Net to clearing', cmp(d.totals.net_to_cc), 'before charges')}${kpi('Exceptions', exc.length, 'short, unpledge, funds', exc.length ? 'bad' : 'good')}${kpi('Held in CUSPA', d.cuspa.filter(c => c.status === 'Held in CUSPA').length, 'unpaid securities', d.cuspa.some(c => c.status === 'Held in CUSPA') ? 'warn' : '')}</div>
    <div class="panel"><div class="hd">${segBtns('settleTab', [['cm', 'Cash market obligations'], ['mtm', 'Derivatives MTM'], ['cuspa', 'Unpaid securities']], 'settle-t')}</div><div id="settle-t">${P['settle-t'](d)}</div></div>`;
  }
};
P['settle-t'] = d => {
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
P['notes-t'] = d => tbl([['Contract note'], ['UCC'], ['Client'], ['Email'], ['Trades', 'r'], ['Net (₹)', 'r'], ['Status'], ['Sent'], ['']], d.filter(n => UI.noteF === 'All' || n.status === UI.noteF).map(n => `<tr><td class="n">${esc(n.no)}</td><td class="n">${n.ucc}</td><td>${esc(n.name)}</td><td class="dim">${esc(n.email)}</td><td class="r">${n.trades}</td><td class="r ${sgn(n.net)}">${inr(n.net, 2)}</td><td>${pill(n.status, n.status === 'Sent' ? 'good' : n.status === 'Bounced' ? 'bad' : 'warn')}</td><td class="n dim">${esc(n.sent_at || '—')}</td><td><button class="btn sm" data-act="noteDoc" data-id="${n.ucc}">Open</button>${n.status !== 'Sent' && has('Ops maker', 'Ops checker') ? `<button class="btn sm" data-act="sendNote" data-id="${n.ucc}">${n.status === 'Bounced' ? 'Re-send' : 'Send'}</button>` : ''}</td></tr>`), { tall: true, empty: 'No contract notes for this date.' });

/* ---------- clients ---------- */
const STAGES = ['Application', 'KYC verification', 'In-person verification', 'UCC upload', 'Active'];
V.clients = {
  load: () => get(`/v1/clients?date=${S.date}`),
  html: d => {
    const cnt = s => d.filter(c => c.status === s).length;
    return ph('Clients & onboarding', 'Applications move through KYC, in-person verification and UCC upload. Activation needs an Ops checker who did not create the application.',
      `<button class="btn" data-act="dl" data-id="/v1/reports/ucc/download?date=${S.date}" data-name="ucc_${S.date}.csv">UCC upload file</button>${has('Ops maker', 'Ops checker') ? '<button class="btn pri" data-act="newClient">New client</button>' : ''}`) +
    `<div class="pipe">${STAGES.map(s => `<button class="${UI.clientStage === s ? 'on' : ''}" data-act="ui" data-k="clientStage" data-v="${s}"><small>${s}</small><b>${cnt(s)}</b></button>`).join('')}</div>
    <div class="panel"><div class="hd"><div class="tools"><button class="btn sm ${UI.clientStage === 'All' ? 'pri' : ''}" data-act="ui" data-k="clientStage" data-v="All">All clients</button><input class="in" data-ui="clientQ" data-part="clients-t" placeholder="Search name, UCC, city" value="${esc(UI.clientQ)}"></div><small>${d.length} records</small></div><div id="clients-t">${P['clients-t'](d)}</div></div>`;
  }
};
P['clients-t'] = d => {
  let rows = d; if (UI.clientStage !== 'All') rows = rows.filter(c => c.status === UI.clientStage);
  const q = UI.clientQ.trim().toLowerCase(); if (q) rows = rows.filter(c => (c.name + ' ' + c.ucc + ' ' + c.city).toLowerCase().includes(q));
  const st = { OK: 'good', Watch: 'warn', Breach: 'bad', Debit: 'warn' };
  return tbl([['UCC'], ['Name'], ['Type'], ['PAN'], ['Segments'], ['Risk'], ['Ledger (₹)', 'r'], ['RMS'], ['RAS'], ['Stage'], ['']], rows.map(c => `<tr><td class="n"><button class="linkish" data-act="client" data-id="${c.ucc}">${c.ucc}</button></td><td>${esc(c.name)}</td><td>${esc(c.type)}</td><td class="n">${esc(c.pan)}</td><td>${(c.segments || []).map(s => `<span class="tag">${s}</span>`).join(' ')}</td><td>${pill(c.risk, c.risk === 'High' ? 'bad' : c.risk === 'Medium' ? 'warn' : 'nt')}</td><td class="r ${sgn(c.funds)}">${c.status === 'Active' ? inr(c.funds) : '—'}</td><td>${c.risk_status ? pill(c.risk_status, st[c.risk_status]) : ''}${c.blocked ? ' ' + pill('Blocked', 'bad') : ''}</td><td>${esc(c.ras)}</td><td>${pill(c.status, c.status === 'Active' ? 'good' : 'acc')}</td><td>${c.status !== 'Active' && (c.status === 'UCC upload' ? has('Ops checker') : has('Ops maker', 'Ops checker')) ? `<button class="btn sm" data-act="advance" data-id="${c.ucc}">${c.status === 'UCC upload' ? 'Activate' : 'Advance'}</button>` : ''}</td></tr>`), { tall: true });
};
async function clientDrawer(ucc) {
  modal(`<div class="bd"><p class="loading">Loading ${esc(ucc)}…</p></div>`, true);
  let d; try { d = await get(`/v1/clients/${encodeURIComponent(ucc)}?date=${S.date}`); } catch (e) { modal(mhd('Client', '') + `<div class="bd">${errHTML(e)}</div>`, true); return; }
  const c = d.client, r = d.risk, st = { OK: 'good', Watch: 'warn', Breach: 'bad', Debit: 'warn' };
  const lines = d.statement.lines.slice(-12);
  const h3 = t => `<h3 style="margin:0 0 8px;font-family:var(--f-cond);font-size:15px">${t}</h3>`;
  modal(mhd(esc(c.name), `${c.ucc} · ${esc(c.type)} · ${esc(c.city)}`) + `<div class="bd">
    <div class="tools">${pill(c.status, c.status === 'Active' ? 'good' : 'acc')}${r ? pill('RMS: ' + r.status, st[r.status]) : ''}${c.blocked ? pill('Blocked: ' + (c.blocked_reason || ''), 'bad') : ''}${(c.segments || []).map(s => `<span class="tag">${esc(d.segment_names[s] || s)}</span>`).join('')}</div>
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
    ${d.overrides.length ? `<div class="panel"><div class="hd"><h3>Limit overrides</h3><small>Maker requests, a different checker or risk manager approves. Valid for ${fmtDate(S.date)}.</small></div>${tbl([['ID'], ['UCC'], ['Amount (₹)', 'r'], ['Reason'], ['Maker'], ['Checker'], ['Status'], ['']], d.overrides.map(o => `<tr><td class="n">${o.id}</td><td class="n">${o.ucc}</td><td class="r">${inr(o.amount)}</td><td class="wrap">${esc(o.reason)}</td><td>${esc(o.maker)}</td><td>${esc(o.checker || '—')}</td><td>${pill(o.status, o.status === 'Approved' ? 'good' : o.status === 'Rejected' ? 'bad' : 'warn')}</td><td>${o.status === 'Pending' && has('Ops checker', 'Risk manager') && o.maker !== S.actor ? `<button class="btn sm" data-act="ovDecide" data-id="${o.id}" data-a="1">Approve</button><button class="btn sm bad" data-act="ovDecide" data-id="${o.id}" data-a="0">Reject</button>` : ''}</td></tr>`))}</div>` : ''}
    <div class="panel"><div class="hd"><div class="tools">${segBtns('rmsF', ['All', 'Breach', 'Watch', 'Debit', 'OK'], 'rms-t')}<input class="in" data-ui="rmsQ" data-part="rms-t" placeholder="Filter UCC or name" value="${esc(UI.rmsQ)}"></div><small>Sorted by utilisation</small></div><div id="rms-t">${P['rms-t'](d)}</div></div>`;
  }
};
P['rms-t'] = d => {
  let rows = d.rows.slice(); if (UI.rmsF !== 'All') rows = rows.filter(r => r.status === UI.rmsF);
  const q = UI.rmsQ.trim().toLowerCase(); if (q) rows = rows.filter(r => (r.ucc + ' ' + r.name).toLowerCase().includes(q));
  rows.sort((a, b) => (b.utilisation - a.utilisation) || (num(a.funds) - num(b.funds)));
  const st = { OK: 'good', Watch: 'warn', Breach: 'bad', Debit: 'warn' }, warn = d.warn_pct / 100;
  return tbl([['Client'], ['Ledger (₹)', 'r'], ['UPI + collateral', 'r'], ['Required', 'r'], ['Free / OMS limit', 'r'], ['Utilisation'], ['Status'], ['']], rows.map(r => `<tr><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button><div class="dim" style="font-size:11.5px">${esc(r.name)}</div></td><td class="r ${sgn(r.funds)}">${inr(r.funds)}</td><td class="r" title="Collateral after haircut ${inr(r.collateral)}; usable after 50:50 ${inr(r.collateral_usable)}">${num(r.upi) + num(r.collateral) ? inr(num(r.upi) + num(r.collateral_usable)) : '—'}${num(r.collateral_usable) < num(r.collateral) - 1 ? `<div class="dim" style="font-size:11px">capped from ${cmp(num(r.upi) + num(r.collateral))}</div>` : ''}</td><td class="r" title="${num(r.futeq) ? 'Futures equivalent ' + cmp(r.futeq) : ''}">${num(r.required) ? inr(r.required) : '—'}</td><td class="r ${sgn(r.free)}">${inr(r.free)}<div class="dim" style="font-size:11px">limit ${r.blocked ? '0 · blocked' : inr(r.limit)}</div></td><td><div class="split">${bar(r.utilisation, warn)}<span class="mono dim" style="font-size:11.5px">${num(r.required) ? (r.utilisation >= 9.99 ? '>999%' : pct(r.utilisation, 0)) : ''}</span></div></td><td>${pill(r.status, st[r.status])}${r.cash_short || r.square_off_sent ? `<div class="dim" style="font-size:11px;margin-top:2px">${[r.cash_short ? 'cash under 50%' : '', r.square_off_sent ? 'square-off sent' : ''].filter(Boolean).join(' · ')}</div>` : ''}</td><td><div class="split">${has('Ops maker', 'Risk manager') ? `<button class="btn sm" data-act="override" data-id="${r.ucc}">Override</button>` : ''}${has('Risk manager') ? `<button class="btn sm ${r.blocked ? '' : 'bad'}" data-act="kill" data-id="${r.ucc}" data-b="${r.blocked ? 0 : 1}">${r.blocked ? 'Unblock' : 'Kill'}</button>${r.positions ? `<button class="btn sm" data-act="sq" data-id="${r.ucc}">Square off</button>` : ''}` : ''}</div></td></tr>`), { tall: true });
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
    const [tb, funds, clients] = await Promise.all([get(`/v1/ledger/trial-balance?as_of=${S.date}`), get(`/v1/funds?date=${S.date}`), get(`/v1/clients?date=${S.date}`)]);
    const act = clients.filter(c => c.status === 'Active');
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
    <div class="grid g21"><div class="panel"><div class="hd"><div class="tools"><label class="dim" for="ledgerU" style="font-size:12px">Client</label><select class="in" id="ledgerU">${d.clients.map(c => `<option value="${c.ucc}" ${UI.ledgerU === c.ucc ? 'selected' : ''}>${c.ucc} · ${esc(c.name)}</option>`).join('')}</select></div>${d.stmt ? `<small>Closing ${drcr(d.stmt.closing)}</small>` : ''}</div>
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
  load: async () => ({}),
  html: () => { const c = UI.calc; return ph('Charges calculator', 'Charge breakdown for one order from the server’s active rate schedule, the same calculation billing uses.') + `<div class="grid g2"><div class="panel"><div class="hd"><h3>Order</h3></div><div class="bd"><div class="form">
    <div class="fld full"><label for="cb">Product</label><select class="in" id="cb" data-calc="bucket">${[['CM-DEL', 'Equity delivery'], ['CM-INTRA', 'Equity intraday'], ['FO-FUT', 'Equity futures'], ['FO-OPT', 'Equity options (premium)'], ['CD-FUT', 'Currency futures'], ['CD-OPT', 'Currency options'], ['COM-FUT', 'Commodity futures'], ['COM-OPT', 'Commodity options']].map(([k, l]) => `<option value="${k}" ${c.bucket === k ? 'selected' : ''}>${l}</option>`).join('')}</select></div>
    <div class="fld"><label for="cx">Exchange</label><select class="in" id="cx" data-calc="exchange">${['NSE', 'BSE', 'MCX'].map(x => `<option ${c.exchange === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div>
    <div class="fld"><label for="cs">Side</label><select class="in" id="cs" data-calc="side"><option value="B" ${c.side === 'B' ? 'selected' : ''}>Buy</option><option value="S" ${c.side === 'S' ? 'selected' : ''}>Sell</option></select></div>
    <div class="fld"><label for="cq">Quantity</label><input class="in" id="cq" type="number" min="1" data-calc="qty" value="${esc(c.qty)}"></div>
    <div class="fld"><label for="cp">Price (₹)</label><input class="in" id="cp" inputmode="decimal" data-calc="price" value="${esc(c.price)}"></div>
  </div></div></div><div class="panel"><div class="hd"><h3>Breakdown</h3><small>${S.me?.sample_rates ? 'Sample rates' : esc(S.me?.schedule || '')}</small></div><div id="calc-out"><p class="loading" style="padding:14px">Calculating…</p></div></div></div>`; }
};
let calcT;
async function calc() {
  const c = UI.calc, out = $('calc-out'); if (!out) return;
  try {
    const r = await post('/v1/charges/quote', { bucket: c.bucket, exchange: c.exchange, side: c.side, qty: parseInt(c.qty, 10) || 0, price: String(c.price || '0'), date: S.date });
    const b = r.breakdown, total = num(b.total), v = num(b.turnover);
    out.innerHTML = tbl([['Component'], ['₹', 'r']], [['Turnover', b.turnover], ['Brokerage', b.brokerage], ['STT / CTT', b.stt], ['Exchange transaction charge', b.exchange_txn], ['SEBI turnover fee', b.sebi_fee], ['Stamp duty', b.stamp_duty], ['GST (18%)', b.gst]].map(x => `<tr><td>${x[0]}</td><td class="r">${nf(x[1], 2)}</td></tr>`),
      { foot: `<tfoot><tr><td>Total charges</td><td class="r">${nf(total, 2)}</td></tr><tr><td>${c.side === 'B' ? 'Net payable' : 'Net receivable'}</td><td class="r">${nf(c.side === 'B' ? v + total : v - total, 2)}</td></tr><tr><td>Breakeven move per unit</td><td class="r">${nf(total * 2 / Math.max(1, +c.qty), 4)}</td></tr></tfoot>` }) +
      `<div class="bd"><span class="dim" style="font-size:12px">Rule ${esc(b.rule)}</span></div>`;
  } catch (e) { out.innerHTML = `<div class="bd">${errHTML(e)}</div>`; }
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

/* ---------- IPO / MF ---------- */
V.ipo = {
  load: async () => { const [ipo, mf] = await Promise.all([get('/v1/ipo'), get('/v1/mf')]); return { ipo, mf }; },
  html: d => ph('IPO & mutual funds', 'IPO bids through UPI mandates (ASBA) from the exchange bidding platform, and mutual fund orders through exchange platforms. Allotment results come from the registrar file.', S.me?.dev && d.ipo.some(x => x.allotment === 'Awaiting') ? '<button class="btn pri" data-act="allotSample">Load sample allotment file</button>' : '<button class="btn" data-act="go" data-id="files">Upload allotment file</button>') +
    `<div class="kpis">${kpi('IPO applications', d.ipo.length, inr(sum(d.ipo, x => num(x.amount))) + ' bid')}${kpi('Mandates accepted', d.ipo.filter(x => x.mandate === 'Accepted').length, d.ipo.filter(x => x.mandate !== 'Accepted').length + ' pending or declined')}${kpi('Allotted', d.ipo.filter(x => x.allotted_lots > 0).length, d.ipo.filter(x => x.allotment === 'Awaiting').length + ' awaiting results', 'good')}${kpi('MF orders', d.mf.length, inr(sum(d.mf, x => num(x.amount))))}</div>
    <div class="panel"><div class="hd"><h3>IPO applications</h3></div>${tbl([['Application'], ['UCC'], ['Client'], ['Issue'], ['Lots', 'r'], ['Amount (₹)', 'r'], ['UPI ID'], ['Mandate'], ['Allotment']], d.ipo.map(x => `<tr><td class="n">${esc(x.id)}</td><td class="n">${x.ucc}</td><td>${esc(x.name)}</td><td>${esc(x.issue)}</td><td class="r">${x.lots}</td><td class="r">${inr(x.amount)}</td><td class="n dim">${esc(x.upi)}</td><td>${pill(x.mandate, x.mandate === 'Accepted' ? 'good' : x.mandate === 'Declined' ? 'bad' : 'warn')}</td><td>${pill(x.allotment, x.allotted_lots > 0 ? 'good' : x.allotment === 'Awaiting' ? 'warn' : x.allotment.startsWith('Rejected') ? 'bad' : 'nt')}</td></tr>`), { empty: 'No IPO bids. Upload IPO-BIDS.' })}</div>
    <div class="panel"><div class="hd"><h3>Mutual fund orders</h3></div>${tbl([['Order'], ['UCC'], ['Client'], ['Scheme'], ['Type'], ['Amount (₹)', 'r'], ['Platform'], ['Status']], d.mf.map(x => `<tr><td class="n">${esc(x.id)}</td><td class="n">${x.ucc}</td><td>${esc(x.name)}</td><td>${esc(x.scheme)}</td><td>${esc(x.type)}</td><td class="r">${inr(x.amount)}</td><td>${esc(x.platform)}</td><td>${pill(x.status, x.status === 'Allotted' ? 'good' : x.status === 'Payment pending' ? 'warn' : 'acc')}</td></tr>`), { empty: 'No MF orders. Upload MF-ORDERS.' })}</div>`
};

/* ---------- client reports ---------- */
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
    const [defs, clients] = await Promise.all([get('/v1/client-reports'), get(`/v1/clients?date=${S.date}`)]);
    const act = clients.filter(c => c.status === 'Active');
    if (!UI.cr.ucc || (UI.cr.ucc !== 'ALL' && !act.some(c => c.ucc === UI.cr.ucc))) UI.cr.ucc = act[0]?.ucc || '';
    return { defs, clients: act };
  },
  html: d => {
    const cr = UI.cr, def = d.defs.find(x => x.kind === cr.kind) || d.defs[0];
    if (cr.ucc === 'ALL' && !def.all_ucc) cr.ucc = d.clients[0]?.ucc || '';
    const [from, to] = def.range ? crRange(cr.preset, S.date) : ['', cr.preset === 'custom' && cr.to ? cr.to : S.date];
    cr.from = from; cr.to = to;
    const presets = [['day', 'Today'], ['month', 'This month'], ['q', 'This quarter'], ['fy', 'This FY'], ['lastfy', 'Last FY'], ['custom', 'Custom']];
    return ph('Client reports', 'Statements and reports for any client and period: on screen, as a printable page (save as PDF), or as CSV. Every number comes from the ledger, trade records and holdings; every print and download is in the audit trail.') +
    `<div class="rtypes">${d.defs.map(x => `<button class="${x.kind === def.kind ? 'on' : ''}" data-act="crKind" data-id="${x.kind}"><b>${esc(x.name)}</b><span>${esc(x.about)}</span></button>`).join('')}</div>
    <div class="panel"><div class="bd"><div class="crform">
      <div class="fld"><label for="cr-ucc">Client</label><select class="in" id="cr-ucc">${def.all_ucc ? `<option value="ALL" ${cr.ucc === 'ALL' ? 'selected' : ''}>All clients</option>` : ''}${d.clients.map(c => `<option value="${c.ucc}" ${cr.ucc === c.ucc ? 'selected' : ''}>${c.ucc} · ${esc(c.name)}</option>`).join('')}</select></div>
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
  return `<div class="panel"><div class="hd"><div><h3>${esc(r.title)} · ${esc(r.name)}${r.ucc !== 'ALL' ? ` <span class="mono dim">${esc(r.ucc)}</span>` : ''}</h3></div><small>${esc(r.period)} · generated ${esc(r.generated)}</small></div>
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
      <div class="fld full"><label for="st-snap">Margin snapshot times (HH:MM:SS, comma separated)</label><input class="in mono" id="st-snap" value="${esc((s.snapshot_times || []).join(', '))}" ${admin ? '' : 'disabled'}></div>
      <div class="fld full"><label for="st-hol">Exchange holidays (YYYY-MM-DD, comma separated)</label><input class="in mono" id="st-hol" value="${esc((s.holidays || []).join(', '))}" ${admin ? '' : 'disabled'}></div>
      <label class="check full"><input type="checkbox" id="st-pen" ${s.penalty_pass_through ? 'checked' : ''} ${admin ? '' : 'disabled'}> Pass short-collection penalties through to client ledgers</label>
    </div></div></div></div>
    <div class="panel"><div class="hd"><h3>Users and roles</h3><small>Maker-checker: the person who creates an override, application or entry cannot approve it</small></div>${tbl([['User'], ['Name'], ['Role'], ['Can do'], ['Status']], d.users.map(u => `<tr><td class="n">${esc(u.id)}</td><td>${esc(u.name)}</td><td>${esc(u.role)}</td><td class="wrap dim">${{ 'Ops maker': 'Upload files, reconcile, bill, create clients and overrides, settlement steps, notes, post journals', 'Ops checker': 'Everything a maker does, plus approvals, activation, rebill, reversals, RAS, identify receipts, close the day', 'Risk manager': 'Square-off, kill switch, approve overrides, post penalties', 'Compliance officer': 'Close alerts, file STRs, mark reports uploaded', Admin: 'Everything, including settings and users' }[u.role] || ''}</td><td>${u.active ? pill('Active', 'good') : pill('Inactive', 'nt')}</td></tr>`))}</div>
    <div class="panel"><div class="hd"><h3>Charge rate schedule</h3><small>${esc(sc.version)}${sc.sample ? ' · ' + pill('Sample rates', 'warn') : ''}</small></div>${sc.sample ? `<div class="bd" style="padding-bottom:0"><div class="note warn">${esc(sc.note)}</div></div>` : ''}${tbl([['Product'], ['Exchange'], ['From'], ['Brokerage'], ['STT buy %', 'r'], ['STT sell %', 'r'], ['Txn %', 'r'], ['Stamp buy %', 'r']], sc.rules.map(r => `<tr><td class="n">${r.bucket}</td><td>${r.exchange}</td><td class="n">${r.effective_from}</td><td>${r.brokerage_mode === 'none' ? 'Nil' : r.brokerage_mode === 'flat' ? '₹' + r.brokerage_flat + ' per order' : r.brokerage_mode === 'pct' ? r.brokerage_pct + '%' : `lower of ₹${r.brokerage_flat} or ${r.brokerage_pct}%`}</td><td class="r">${r.stt_buy_pct}</td><td class="r">${r.stt_sell_pct}</td><td class="r">${r.txn_pct}</td><td class="r">${r.stamp_buy_pct}</td></tr>`))}<div class="bd"><span class="dim" style="font-size:12px">GST ${sc.gst_pct}% · SEBI fee ${sc.sebi_fee_pct}% · The schedule is a versioned file on the server (config/charges); a new circular means a new version with an effective date.</span></div></div>`;
  }
};

/* ============================== modals ============================== */
function modal(html, drawer) { $('mroot').innerHTML = `<div class="ov ${drawer ? 'drawer' : ''}" data-act="ovclose"><div class="md" role="dialog" aria-modal="true">${html}</div></div>`; const f = document.querySelector('.md textarea,.md input:not([type=checkbox]),.md select'); if (f) setTimeout(() => f.focus(), 10); }
function closeModal() { $('mroot').innerHTML = ''; }
function mhd(t, s) { return `<div class="hd"><div><h2>${t}</h2>${s ? `<p>${s}</p>` : ''}</div><button class="x" data-act="close" aria-label="Close">×</button></div>`; }
function confirmModal(title, sub, body, okLabel, onOk, danger) {
  modal(mhd(title, sub) + `<div class="bd">${body || ''}<div id="cm-err"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn ${danger ? 'bad' : 'pri'}" id="cm-ok">${okLabel}</button></div>`);
  $('cm-ok').onclick = async () => { $('cm-ok').disabled = true; try { await onOk(); } catch (e) { $('cm-err').innerHTML = errHTML(e); $('cm-ok').disabled = false; } };
}

/* ============================== actions ============================== */
async function act(fn, okMsg, reload = true) {
  try { const r = await fn(); if (okMsg) toast(typeof okMsg === 'function' ? okMsg(r) : okMsg); closeModal(); if (reload) await render({ keep: true }); return r; }
  catch (e) { toast(e.message); throw e; }
}
const A = {
  crKind: id => { crSync(); UI.cr.kind = id; UI.crRes = null; UI.crErr = null; $('page').innerHTML = V.statements.html(C.statements); bindPage(); },
  crPreset: id => { crSync(); UI.cr.preset = id; $('page').innerHTML = V.statements.html(C.statements); bindPage(); },
  crRun: () => { crSync(); crRun(); },
  crPrint: () => { crSync(); openDoc(crURL('html')); },
  crCSV: () => { crSync(); const r = UI.cr; download(crURL('csv'), `${r.kind}_${r.ucc}_${r.from ? r.from + '_' : ''}${r.to}.csv`); },
  crFor: id => { UI.cr.ucc = id; UI.crRes = null; A.go('statements'); },
  go: id => { S.view = id; closeModal(); render(); window.scrollTo(0, 0); },
  close: () => closeModal(),
  ovclose: (id, el, e) => { if (e.target === el) closeModal(); },
  ui: (id, el) => { UI[el.dataset.k] = el.dataset.v; const part = el.dataset.part; if (part && $(part)) { rerenderPart(part); el.parentElement.querySelectorAll('button').forEach(b => b.classList.toggle('on', b === el)); } else { $('page').innerHTML = V[S.view].html(C[S.view]); bindPage(); } },
  dl: (path, el) => download(path, el.dataset.name),
  client: id => clientDrawer(id),
  openLedger: id => { UI.ledgerU = id; A.go('ledger'); },
  sample: async (id, el) => { if (el) el.disabled = true; toast('Loading the sample trading day…'); try { const r = await post('/v1/dev/sample', { date: S.date }); toast(`Loaded ${r.files} sample files for ${fmtDate(S.date)}`); } catch (e) { toast(e.message); } render({ keep: true }); },
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
    const cl = C.ledger?.clients || [];
    confirmModal('Post journal entry', 'Manual entries are recorded with your name. A checker can reverse them.', `<div class="form"><div class="fld"><label for="jn-u">Client</label><select class="in" id="jn-u">${cl.map(c => `<option value="${c.ucc}" ${UI.ledgerU === c.ucc ? 'selected' : ''}>${c.ucc} · ${esc(c.name)}</option>`).join('')}</select></div><div class="fld"><label for="jn-t">Type</label><select class="in" id="jn-t"><option value="RECEIPT">Receipt into client bank (credit client)</option><option value="PAYMENT">Payment to client (debit client)</option><option value="INTEREST">Delayed payment interest (debit client)</option></select></div><div class="fld"><label for="jn-a">Amount (₹)</label><input class="in" id="jn-a" inputmode="decimal" value="10000"></div><div class="fld"><label for="jn-r">Reference</label><input class="in" id="jn-r" placeholder="UTR or voucher no."></div><div class="fld full"><label for="jn-n">Narration</label><input class="in" id="jn-n" placeholder="What this entry is for"></div></div>`,
      'Post entry', async () => {
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
    const cl = (await get(`/v1/clients?date=${S.date}`)).filter(c => c.status === 'Active');
    confirmModal('Identify credit', 'Only when the bank confirms the remitting account belongs to the client. Otherwise return the funds.', `<div class="fld"><label for="id-u">Client</label><select class="in" id="id-u">${cl.map(c => `<option value="${c.ucc}">${c.ucc} · ${esc(c.name)}</option>`).join('')}</select></div><label class="check"><input type="checkbox" id="id-ok"> The bank confirmed in writing that the remitting account belongs to this client</label>`,
      'Credit client', () => act(() => post(`/v1/bank/${encodeURIComponent(id)}/identify`, { ucc: $('id-u').value, bank_confirmed: $('id-ok').checked }), 'Receipt posted'));
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
      snapshot_times: $('st-snap').value.split(',').map(x => x.trim()).filter(Boolean), holidays: $('st-hol').value.split(',').map(x => x.trim()).filter(Boolean), penalty_pass_through: $('st-pen').checked };
    act(() => api('PUT', '/v1/settings', s), 'Settings saved').then(loadMe);
  },
  eod: async () => {
    let st; try { st = await get(`/v1/eod?date=${S.date}`); } catch (e) { toast(e.message); return; }
    const okAll = st.checks.every(c => c.ok);
    if (st.closed) { modal(mhd('EOD · ' + fmtDate(S.date), `Closed at ${esc(st.closed_at)} by ${esc(st.closed_by)}`) + `<div class="bd">${(st.exceptions || []).length ? `<div class="note warn"><b>Closed with exceptions</b><ul style="margin:6px 0 0;padding-left:18px">${st.exceptions.map(x => `<li>${esc(x)}</li>`).join('')}</ul></div>` : '<div class="note">Closed clean.</div>'}</div><div class="ft"><button class="btn" data-act="close">Close</button></div>`); return; }
    confirmModal('EOD close · ' + fmtDate(S.date), okAll ? 'All checks passed' : 'Resolve what you can; closing with exceptions needs a note', `<div class="checks">${st.checks.map(c => `<div><span>${esc(c.name)}<br><span class="dim" style="font-size:12px">${esc(c.detail)}</span></span>${c.ok ? pill('Done', 'good') : `<button class="btn sm" data-act="go" data-id="${c.view}">Open</button>`}</div>`).join('')}</div>${okAll ? '' : '<div class="fld"><label for="eod-n">Note for closing with exceptions</label><textarea class="in" id="eod-n"></textarea></div>'}${has('Ops checker') ? '' : '<div class="note">Closing the day needs an Ops checker.</div>'}`,
      okAll ? 'Close the day' : 'Close with exceptions', () => act(() => post('/v1/eod/close', { date: S.date, note: $('eod-n')?.value || '' }), 'Day closed'), !okAll);
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
document.addEventListener('click', e => {
  const a = e.target.closest('[data-act]');
  if (!a) { const r = $('gres'); if (r && !e.target.closest('.srch')) r.hidden = true; return; }
  const fn = A[a.dataset.act]; if (!fn) return;
  if (a.tagName === 'A' || a.tagName === 'BUTTON') e.preventDefault();
  e.stopPropagation(); fn(a.dataset.id, a, e);
});
document.addEventListener('input', e => {
  const el = e.target;
  if (el.id === 'gsearch') { UI.search = el.value; searchClients(el.value); return; }
  if (el.dataset.ui) { UI[el.dataset.ui] = el.value; if (el.dataset.part) { const el2 = $(el.dataset.part); if (el2) el2.innerHTML = P[el.dataset.part](C[S.view]); } return; }
  if (el.dataset.calc) { UI.calc[el.dataset.calc] = el.value; clearTimeout(calcT); calcT = setTimeout(calc, 250); return; }
  if (el.dataset.ob) { UI.ob.data[el.dataset.ob] = el.value; }
});
document.addEventListener('change', e => {
  const el = e.target;
  if (el.id === 'date' && el.value) { S.date = el.value; C.billResult = null; render(); return; }
  if (el.id === 'actor') { S.actor = el.value; loadMe().then(() => render({ keep: true })); return; }
  if (el.id === 'ftype') { UI.fileType = el.value; $('page').innerHTML = V.files.html(C.files); bindPage(); return; }
  if (el.id === 'cr-from' || el.id === 'cr-to') { UI.cr.preset = 'custom'; crSync(); document.querySelectorAll('[data-act=crPreset]').forEach(b => b.classList.toggle('on', b.dataset.id === 'custom')); return; }
  if (el.id === 'cr-ucc') { crSync(); return; }
  if (el.id === 'ledgerU') { UI.ledgerU = el.value; render({ keep: true }); return; }
  if (el.dataset.calc) { UI.calc[el.dataset.calc] = el.value; calc(); return; }
  if (el.dataset.ob && el.tagName === 'SELECT') { UI.ob.data[el.dataset.ob] = el.value; if (el.dataset.ob === 'nominee') obModal(); return; }
  if (el.dataset.obseg) { const d = UI.ob.data; d.segments = el.checked ? [...new Set([...d.segments, el.dataset.obseg])] : d.segments.filter(s => s !== el.dataset.obseg); return; }
  if (el.dataset.obchk) { UI.ob.data[el.dataset.obchk] = el.checked; }
});
document.addEventListener('keydown', e => {
  if (e.key === 'Escape') { closeModal(); const r = $('gres'); if (r) r.hidden = true; }
  if (e.key === '/' && !/INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName)) { e.preventDefault(); $('gsearch')?.focus(); }
});
let searchCache = { date: '', rows: [] };
async function searchClients(q) {
  const r = $('gres'); q = q.trim().toLowerCase(); if (!q) { r.hidden = true; return; }
  if (searchCache.date !== S.date || !searchCache.rows.length) { try { searchCache = { date: S.date, rows: await get(`/v1/clients?date=${S.date}`) }; } catch (e) { return; } }
  const m = searchCache.rows.filter(c => (c.name + ' ' + c.ucc).toLowerCase().includes(q)).slice(0, 7);
  r.innerHTML = m.length ? m.map(c => `<button data-act="client" data-id="${c.ucc}"><span>${esc(c.name)}</span><span class="mono dim">${c.ucc}</span></button>`).join('') : '<div class="empty">No client matches</div>';
  r.hidden = false;
}

/* ============================== start ============================== */
async function loadMe() {
  S.me = await get('/v1/me');
  const users = S.me.users || [];
  if (users.length && !users.some(u => u.id === S.actor)) { S.actor = users[0].id; S.me = await get('/v1/me'); }
  persist();
}
async function boot() {
  try { CFG = await (await fetch('/ui/config')).json(); } catch (e) { CFG = {}; }
  if (!S.key && CFG.dev) S.key = CFG.dev_key;
  if (!S.key) { renderSide(); signIn(); return; }
  try { await loadMe(); } catch (e) { renderSide(); signIn(e.status === 401 ? 'That API key was not accepted.' : e.message); return; }
  render();
}
boot();
