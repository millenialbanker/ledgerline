'use strict';
/* Ledgerline Control: the provider's console. Who runs Ledgerline and what
   they use, AI credit, billing, and support requests from inside Ledgerline. */

const $ = id => document.getElementById(id);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const enc = encodeURIComponent;
const S = { me: null, csrf: '', view: 'overview', arg: '', data: null, ui: {} };

async function api(method, path, body) {
  const h = { 'Content-Type': 'application/json' };
  if (S.csrf) h['X-CSRF'] = S.csrf;
  const r = await fetch(path, { method, headers: h, body: body === undefined ? undefined : JSON.stringify(body), credentials: 'same-origin' });
  let j = null;
  try { j = await r.json(); } catch (e) {}
  if (r.status === 401 && path !== '/api/login') { showLogin(); throw new Error('Please sign in.'); }
  if (!r.ok) throw new Error(j?.error || 'Something went wrong (' + r.status + ').');
  return j;
}
const get = p => api('GET', p);
const post = (p, b) => api('POST', p, b || {});

// ---- formatting ----
function inr(v, dp = 2) {
  const n = Number(v || 0), neg = n < 0, a = Math.abs(n);
  const [i, f] = a.toFixed(dp).split('.');
  const last3 = i.slice(-3), rest = i.slice(0, -3).replace(/\B(?=(\d{2})+(?!\d))/g, ',');
  return (neg ? '−' : '') + '₹' + (rest ? rest + ',' + last3 : last3) + (dp ? '.' + f : '');
}
const paise = p => inr(Number(p || 0) / 100);
const int = n => Number(n || 0).toLocaleString('en-IN');
function ago(ts) {
  if (!ts) return '<span class="dim">never</span>';
  const s = (Date.now() - new Date(ts).getTime()) / 1000;
  const t = s < 90 ? 'just now' : s < 3600 ? Math.round(s / 60) + ' min ago' : s < 86400 ? Math.round(s / 3600) + ' h ago' : Math.round(s / 86400) + ' d ago';
  return `<span title="${esc(fmtStamp(ts))}">${t}</span>`;
}
function fmtStamp(ts) { if (!ts) return ''; const d = new Date(ts); return d.toLocaleString('en-IN', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' }); }
function fmtDate(s) { if (!s) return ''; const d = new Date(s + (s.length === 10 ? 'T00:00:00' : '')); return d.toLocaleDateString('en-IN', { day: 'numeric', month: 'short', year: 'numeric' }); }
function monthLabel(m) { const [y, mo] = m.split('-'); return new Date(+y, +mo - 1, 1).toLocaleDateString('en-IN', { month: 'long', year: 'numeric' }); }
function shiftMonth(m, k) { const [y, mo] = m.split('-').map(Number); const d = new Date(y, mo - 1 + k, 1); return d.getFullYear() + '-' + String(d.getMonth() + 1).padStart(2, '0'); }
const pill = (t, k = '') => `<span class="pill ${k}">${esc(t)}</span>`;
const EDN = { manual: 'Manual', autopilot: 'Autopilot', full: 'Full' };
const edPill = e => pill(EDN[e] || e || '—', e === 'full' ? 'acc' : e === 'autopilot' ? 'good' : '');
const custPill = s => pill(s, { Active: 'good', Trial: 'acc', Suspended: 'warn', Left: '' }[s] || '');
const tkPill = s => pill(s, { New: 'bad', Open: 'acc', 'Waiting on customer': 'warn', Resolved: 'good', Closed: '' }[s] || '');
const priPill = p => pill(p, { Urgent: 'bad', High: 'warn' }[p] || '');
const invPill = (s, od) => od ? pill('Overdue', 'bad') : pill(s, { Draft: '', Issued: 'acc', 'Part-paid': 'warn', Paid: 'good', Void: '' }[s] || '');
const onDot = on => `<span class="dot ${on ? 'on' : 'off'}"></span>${on ? 'Online' : 'Offline'}`;
const tkNo = t => { const m = (t.messages || []).find(x => x.id.startsWith('#')); return m ? 'T-' + m.id.slice(1) : t.local_id; };
const can = (...roles) => S.me && (S.me.role === 'Owner' || roles.includes(S.me.role));

function tbl(head, rows, o = {}) {
  return `<div class="tw"><table class="tbl"><thead><tr>${head.map(h => `<th class="${h[1] || ''}">${h[0]}</th>`).join('')}</tr></thead><tbody>${rows.length ? rows.join('') : `<tr><td colspan="${head.length}" class="empty">${o.empty || 'Nothing to show'}</td></tr>`}</tbody>${o.foot || ''}</table></div>`;
}
const ph = (t, sub, acts) => `<div class="ph"><div><h1>${t}</h1>${sub ? `<p>${sub}</p>` : ''}</div>${acts ? `<div class="acts">${acts}</div>` : ''}</div>`;
const kpi = (label, val, sub, o = {}) => `<div class="kpi ${o.alert ? 'alert' : ''} ${o.go ? 'click' : ''}" ${o.go ? `data-act="go" data-id="${esc(o.go)}"` : ''}><small>${label}</small><b>${val}</b>${sub ? `<div class="sub">${sub}</div>` : ''}</div>`;
const seg = (key, opts) => `<div class="seg">${opts.map(([v, l]) => `<button class="${(S.ui[key] ?? opts[0][0]) === v ? 'on' : ''}" data-act="ui" data-k="${key}" data-v="${esc(v)}">${l}</button>`).join('')}</div>`;

function toast(m, bad) { const t = $('toast'); t.textContent = m; t.className = 'toast' + (bad ? ' bad' : ''); t.hidden = false; clearTimeout(t._h); t._h = setTimeout(() => { t.hidden = true; }, bad ? 6000 : 3000); }
function modal(html) { $('mroot').innerHTML = `<div class="ov" data-act="ovclose"><div class="md" role="dialog" aria-modal="true">${html}</div></div>`; const f = document.querySelector('.md input:not([type=checkbox]),.md textarea,.md select'); if (f) setTimeout(() => f.focus(), 10); }
function closeModal() { $('mroot').innerHTML = ''; }
const mhd = (t, p) => `<div class="hd"><div><h2>${t}</h2>${p ? `<p>${p}</p>` : ''}</div><button class="x" data-act="close" aria-label="Close">×</button></div>`;
async function act(fn, ok, reload = true) {
  try { const r = await fn(); if (ok) toast(typeof ok === 'function' ? ok(r) : ok); closeModal(); if (reload) await render(); return r; }
  catch (e) { toast(e.message, true); throw e; }
}

// ---- sign-in ----
function showLogin(msg) {
  S.me = null; S.csrf = '';
  $('side').hidden = true; $('app').classList.add('solo');
  $('page').innerHTML = `<form class="login" id="login" autocomplete="on"><div class="brand"><div class="mark"><i></i><i></i><i></i></div><div><b>Ledgerline Control</b><small>For the Ledgerline team</small></div></div>
    <div class="fld"><label for="lg-id">User name</label><input class="in" id="lg-id" autocomplete="username" required></div>
    <div class="fld"><label for="lg-pw">Password</label><input class="in" id="lg-pw" type="password" autocomplete="current-password" required></div>
    <div class="err" id="lg-err">${esc(msg || '')}</div><button class="btn pri" style="width:100%;justify-content:center">Sign in</button></form>`;
  $('login').onsubmit = async e => {
    e.preventDefault();
    try { const r = await post('/api/login', { id: $('lg-id').value, password: $('lg-pw').value }); S.csrf = r.csrf; await boot(); }
    catch (err) { $('lg-err').textContent = err.message; }
  };
  $('lg-id').focus();
}
function showReset() {
  $('side').hidden = true; $('app').classList.add('solo');
  $('page').innerHTML = `<form class="login" id="rs"><div class="brand"><div class="mark"><i></i><i></i><i></i></div><div><b>Set your password</b><small>${esc(S.me.name)}: the one-time password works only once</small></div></div>
    <div class="fld"><label for="rs-old">One-time password</label><input class="in" id="rs-old" type="password" autocomplete="current-password"></div>
    <div class="fld"><label for="rs-new">New password (12 characters or more)</label><input class="in" id="rs-new" type="password" autocomplete="new-password" minlength="12"></div>
    <div class="err" id="rs-err"></div><button class="btn pri" style="width:100%;justify-content:center">Save password</button></form>`;
  $('rs').onsubmit = async e => { e.preventDefault(); try { await post('/api/password', { old: $('rs-old').value, new: $('rs-new').value }); toast('Password saved'); await boot(); } catch (err) { $('rs-err').textContent = err.message; } };
}

async function boot() {
  try { S.me = await get('/api/me'); S.csrf = S.me.csrf; } catch (e) { return; }
  if (S.me.must_reset) return showReset();
  $('side').hidden = false; $('app').classList.remove('solo');
  route();
}

// ---- navigation ----
const NAV = [['overview', 'Overview'], ['alerts', 'Alerts'], ['customers', 'Customers'], ['installs', 'Installations'], ['usage', 'Usage & AI'], ['billing', 'Billing', 'Billing'], ['support', 'Support'], ['notices', 'Notices'], ['settings', 'Settings']];
function renderSide(counts = {}) {
  $('side').innerHTML = `<div class="brand"><div class="mark"><i></i><i></i><i></i></div><div><b>Control</b><small>${esc(S.me.vendor || 'Ledgerline')}</small></div></div>
    <nav>${NAV.filter(n => !n[2] || can(n[2])).map(([k, l]) => `<button class="nav ${S.view === k || (S.view === 'customer' && k === 'customers') || (S.view === 'ticket' && k === 'support') || (S.view === 'invoice' && k === 'billing') ? 'on' : ''}" data-act="go" data-id="${k}"><span>${l}</span>${counts[k] ? `<span class="ct">${counts[k]}</span>` : ''}</button>`).join('')}</nav>
    <div class="side-foot"><span>${esc(S.me.name)} · ${esc(S.me.role)}</span><button data-act="pw">Change password</button><button data-act="logout">Sign out</button></div>`;
}
function route() {
  const [v, ...rest] = (location.hash.slice(1) || 'overview').split('/');
  S.view = V[v] ? v : 'overview'; S.arg = decodeURIComponent(rest.join('/'));
  render();
}
addEventListener('hashchange', route);
function go(id) { const h = '#' + id; if (location.hash === h) route(); else location.hash = h; window.scrollTo(0, 0); }

async function render() {
  const v = V[S.view];
  try {
    S.data = await v.load(S.arg);
    renderSide(S.counts || {});
    $('page').innerHTML = v.html(S.data);
    bind();
  } catch (e) { $('page').innerHTML = `<div class="note bad">${esc(e.message)}</div>`; }
  Promise.all([get('/api/tickets'), get('/api/alerts').catch(() => [])]).then(([ts, al]) => { S.counts = { support: ts.filter(t => t.status === 'New').length, alerts: al.filter(a => !a.acked && a.severity !== 'info').length }; renderSide(S.counts); }).catch(() => {});
}
function bind() {
  document.querySelectorAll('[data-change]').forEach(el => { el.onchange = () => A[el.dataset.change](el.value, el); });
}

// ---- screens ----
const V = {};

V.overview = {
  load: () => get('/api/overview'),
  html: o => {
    const money = can('Billing');
    return ph('Overview', `${monthLabel(o.month)} so far. Installations report every 15 minutes.`) +
      (o.installs_down.length ? `<div class="note bad"><b>${o.installs_down.length} installation${o.installs_down.length > 1 ? 's are' : ' is'} not reporting:</b> ${o.installs_down.map(i => `${esc(i.name)} (last ${ago(i.last_report)})`).join(', ')}.</div>` : '') +
      `<div class="kpis">${kpi('Customers', int(o.customers), Object.entries(o.by_status).map(([k, n]) => `${n} ${k.toLowerCase()}`).join(' · ') || '—', { go: 'customers' })}
      ${kpi('Firms running Ledgerline', `${int(o.firms_online)}<span class="dim" style="font-size:14px"> / ${int(o.firms)}</span>`, 'online now / all firms', { go: 'customers' })}
      ${kpi('Staff logins', int(o.staff_active), `${int(o.active_today)} active today`)}
      ${kpi('Clients who traded', int(o.traded_month), 'this month, all firms', { go: 'usage' })}
      ${kpi('Ledgy AI answers', int(o.ai_answers), `${paise(o.ai_charged)} charged · cost ${inr(o.ai_cost)}`, { go: 'usage' })}
      ${kpi('Open support requests', int(o.open_tickets), `${o.new_tickets} new${o.overdue_tickets ? ` · <b class="neg">${o.overdue_tickets} past first-reply target</b>` : ''}`, { alert: o.overdue_tickets > 0, go: 'support' })}
      ${money ? kpi('This month so far', inr(o.month_to_date), 'before GST, billable customers', { go: 'billing' }) : ''}
      ${money ? kpi('Last month billed', inr(o.last_billed), 'issued invoices, before GST', { go: 'billing' }) : ''}
      ${money ? kpi('Outstanding', inr(o.outstanding), o.overdue_invoices ? `<b class="neg">${o.overdue_invoices} overdue · ${inr(o.overdue_amount)}</b>` : 'nothing overdue', { alert: o.overdue_invoices > 0, go: 'billing' }) : ''}</div>
      <div class="grid g2"><div class="panel"><div class="hd"><h3>Open support requests</h3><button class="btn sm" data-act="go" data-id="support">All requests</button></div>${tbl([['Request'], ['Firm'], ['Status'], ['Updated', 'r']], o.recent_tickets.map(t => `<tr class="click" data-act="go" data-id="ticket/${enc(t.id)}"><td><b>${esc(t.subject)}</b><div class="dim small">${tkNo(t)} · ${esc(t.user_name)} ${priPill(t.priority)}</div></td><td>${esc(t.firm_name)}</td><td>${tkPill(t.status)}</td><td class="r small">${ago(t.updated)}</td></tr>`), { empty: 'No open requests.' })}</div>
      <div><div class="panel"><div class="hd"><h3>Editions</h3></div><div class="bd"><div class="bars">${['manual', 'autopilot', 'full'].map(e => { const n = o.by_edition[e] || 0, mx = Math.max(1, ...Object.values(o.by_edition)); return `<div class="bar"><span>${EDN[e]}</span><span class="tr"><i style="width:${n / mx * 100}%"></i></span><span class="mono r">${n}</span></div>`; }).join('')}</div></div></div>
      <div class="panel"><div class="hd"><h3>Needs attention</h3></div>${tbl([['Firm'], ['What']], [
        ...o.ai_low.map(f => `<tr class="click" data-act="go" data-id="customer/${enc(f.customer)}"><td>${esc(f.latest.firm)}</td><td>Ledgy AI credit low: ${paise(f.latest.ai.balance)} left</td></tr>`),
        ...o.problems.map(f => `<tr class="click" data-act="go" data-id="customer/${enc(f.customer)}"><td>${esc(f.latest.firm)}</td><td>${[f.latest.health.backup_problem && 'Backup: ' + esc(f.latest.health.backup_problem), f.latest.health.open_breaks && f.latest.health.open_breaks + ' open reconciliation breaks'].filter(Boolean).join(' · ')}</td></tr>`)], { empty: 'Nothing needs attention.' })}</div></div></div>`;
  }
};

V.customers = {
  load: async () => { const [cs, fs] = await Promise.all([get('/api/customers'), get('/api/firms')]); return { cs, fs }; },
  html: ({ cs, fs }) => {
    const q = (S.ui.cq || '').toLowerCase(), st = S.ui.cst || 'all';
    const rows = cs.filter(c => (st === 'all' || c.status === st) && (!q || (c.name + ' ' + c.legal_name + ' ' + c.gstin + ' ' + c.id).toLowerCase().includes(q)));
    return ph('Customers', 'Each broker you bill, the firms they run and on which server. A firm seen for the first time gets its own customer on trial; link it to an existing customer if it is the same broker.', can('Billing') ? '<button class="btn pri" data-act="custEdit">Add customer</button>' : '') +
      `<div class="tools"><input class="in" style="max-width:280px" placeholder="Search name, GSTIN" value="${esc(S.ui.cq || '')}" data-change="cq">${seg('cst', [['all', 'All'], ['Active', 'Active'], ['Trial', 'Trial'], ['Suspended', 'Suspended'], ['Left', 'Left']])}</div>` +
      `<div class="panel">${tbl([['Customer'], ['Status'], ['Firms'], ['Editions'], ['Staff', 'r'], ['Clients', 'r'], ['Traded this month', 'r'], ['AI answers', 'r'], ['Last active']], rows.map(c => {
        const mine = fs.filter(f => f.customer === c.id);
        const sum = k => mine.reduce((a, f) => a + (Number(k(f)) || 0), 0);
        const last = mine.map(f => f.latest.last_activity).filter(Boolean).sort().pop();
        return `<tr class="click" data-act="go" data-id="customer/${enc(c.id)}"><td><b>${esc(c.name)}</b><div class="dim small">${esc(c.id)}${c.gstin ? ' · ' + esc(c.gstin) : ''}</div></td><td>${custPill(c.status)}</td>
          <td>${mine.length ? mine.map(f => `<div class="small nowrap"><span class="dot ${f.online ? 'on' : 'off'}"></span>${esc(f.latest.firm || f.tenant)}</div>`).join('') : '<span class="dim">none yet</span>'}</td>
          <td>${[...new Set(mine.map(f => f.latest.edition))].map(edPill).join(' ')}</td><td class="r mono">${int(sum(f => f.latest.staff_active))}</td><td class="r mono">${int(sum(f => f.latest.clients))}</td>
          <td class="r mono">${int(sum(f => f.latest.traded_month))}</td><td class="r mono">${int(sum(f => f.latest.ai.answers))}</td><td class="small">${ago(last)}</td></tr>`;
      }), { empty: 'No customers yet. They appear when an installation reports in.' })}</div>`;
  }
};

V.customer = {
  load: id => Promise.all([get('/api/customers/' + enc(id)), get('/api/customers'), get('/api/customers/' + enc(id) + '/notes').catch(() => []), can('Billing') && S.ui.ctab === 'statement' ? get('/api/customers/' + enc(id) + '/statement') : null]).then(([d, all, notes, st]) => ({ ...d, all, notes, st })),
  html: d => {
    const c = d.customer, tab = S.ui.ctab || 'firms';
    const money = can('Billing');
    return ph(esc(c.name), `${esc(c.id)} · ${custPill(c.status)}${c.gstin ? ' · GSTIN ' + esc(c.gstin) : ''}${c.contact ? ' · ' + esc(c.contact) : ''}${c.email ? ' · ' + esc(c.email) : ''}`,
      `<button class="btn" data-act="go" data-id="customers">All customers</button>${money ? `<button class="btn" data-act="custEdit" data-id="${esc(c.id)}">Edit details</button>` : ''}`) +
      (d.bill && money ? `<div class="kpis">${kpi('This month so far', inr(d.bill.invoice.taxable), `before GST${d.bill.billable ? '' : ' · not billed: ' + esc(d.bill.why)}`)}${kpi('Setup fee', c.setup_fee > 0 ? inr(c.setup_fee) : '—', c.setup_billed ? 'billed on ' + esc(c.setup_billed) : c.setup_fee > 0 ? 'not yet billed' : '')}${kpi('Discount', (c.discount_pct || '0') + '%', 'on everything except AI credit')}${kpi('Open requests', d.tickets.filter(t => !['Resolved', 'Closed'].includes(t.status)).length, d.tickets.length + ' in all')}</div>` : '') +
      contractLine(c) +
      `<div class="tools">${seg('ctab', [['firms', `Firms (${d.firms.length})`], ['people', 'Who uses what'], ['usage', 'Usage by month'], ['support', `Support (${d.tickets.length})`], ['notes', `Notes (${d.notes.length})`], ...(money ? [['billing', `Invoices (${d.invoices.length})`], ['statement', 'Statement']] : [])])}</div>` +
      ({ firms: cFirms, people: cPeople, usage: cUsage, support: cSupport, billing: cBilling, notes: cNotes, statement: cStatement }[tab] || cFirms)(d);
  }
};
function cFirms(d) {
  return d.firms.map(f => {
    const r = f.latest, h = r.health || {};
    return `<div class="panel"><div class="hd"><h3>${esc(r.firm || f.tenant)} ${edPill(r.edition)}</h3><div class="acts">${can('Billing') ? `<button class="btn sm" data-act="edChange" data-id="${esc(f.id)}">Change edition</button><button class="btn sm" data-act="aiCredit" data-id="${esc(f.id)}">Top up AI credit</button><button class="btn sm" data-act="linkFirm" data-id="${esc(f.id)}">Move to another customer</button>` : ''}</div></div>
      <div class="bd grid g3"><dl class="kv"><dt>Server</dt><dd>${esc(f.install_name)} <span class="dim">(${f.kind === 'hosted' ? 'hosted by us' : 'their own'})</span></dd><dt>Status</dt><dd>${onDot(f.online)} · reported ${ago(f.last_report)}</dd><dt>Version</dt><dd class="mono">${esc(f.version || '—')}</dd><dt>Firm ID</dt><dd class="mono">${esc(f.tenant)}</dd><dt>SEBI reg.</dt><dd class="mono">${esc(r.sebi_reg || '—')}</dd><dt>Business date</dt><dd>${fmtDate(r.business_date)} · day ${esc(h.eod || '—')}</dd></dl>
      <dl class="kv"><dt>Staff logins</dt><dd>${int(r.staff_active)} · ${int(r.active_today)} today · ${int(r.active_30d)} in 30 days</dd><dt>Clients</dt><dd>${int(r.clients)} (${Object.entries(r.clients_by_stage || {}).map(([k, n]) => `${n} ${esc(k.toLowerCase())}`).join(', ') || '—'})</dd><dt>Traded this month</dt><dd>${int(r.traded_month)} clients · ${int(r.trades_month)} trades</dd><dt>Online accounts</dt><dd>${int(r.kyc_month)} verified this month</dd><dt>Automations</dt><dd>${int(r.bots.computers)} computers · ${int(r.bots.workflows)} workflows · ${int(r.bots.runs_month)} runs${r.bots.failed_month ? ` (<span class="neg">${r.bots.failed_month} failed</span>)` : ''}</dd></dl>
      <dl class="kv"><dt>Ledgy AI</dt><dd>${r.ai.on ? pill('On', 'good') : pill('Off')}</dd><dt>Answers this month</dt><dd>${int(r.ai.answers)}</dd><dt>Charged to firm</dt><dd>${paise(r.ai.charged)} <span class="dim">(our cost ${inr(r.ai.cost_inr)})</span></dd><dt>Credit left</dt><dd class="${r.ai.balance < 20000 ? 'neg' : ''}">${paise(r.ai.balance)}</dd><dt>Topped up this month</dt><dd>${paise(r.ai.topped_up)}</dd><dt>Backups</dt><dd>${h.last_backup ? ago(h.last_backup) : '<span class="neg">none</span>'}${h.backup_problem ? ` · <span class="neg">${esc(h.backup_problem)}</span>` : ''}</dd><dt>Open work · breaks</dt><dd>${int(h.open_work)} · ${int(h.open_breaks)}</dd></dl></div>${setupList(r.setup)}</div>`;
  }).join('') + (d.commands.length ? `<div class="panel"><div class="hd"><h3>Sent from Control</h3><span class="dim small">applied at the firm’s next report</span></div>${tbl([['Ref'], ['Firm'], ['What'], ['Amount', 'r'], ['Status'], ['Sent'], ['Invoice'], ['']], d.commands.map(x => `<tr><td class="mono">${esc(x.id)}</td><td>${esc((d.firms.find(f => f.id === x.firm) || {}).latest?.firm || x.firm)}</td><td class="small">${x.kind === 'ai_credit' ? 'AI credit top-up' : x.kind === 'edition' ? 'Edition → ' + esc(EDN[x.edition] || x.edition) : x.kind === 'notice' ? 'Notice: ' + esc(x.notice?.title || '') : x.kind === 'notice_withdraw' ? 'Notice withdrawn' : esc(x.kind)}</td><td class="r mono">${x.kind === 'ai_credit' ? paise(x.amount) : ''}</td><td>${pill(x.status, { Done: 'good', Failed: 'bad', Queued: 'warn' }[x.status] || '')}${x.error ? `<div class="small neg">${esc(x.error)}</div>` : ''}</td><td class="small">${fmtStamp(x.created)} · ${esc(x.by)}</td><td class="small">${x.kind !== 'ai_credit' ? '' : x.bill ? (x.invoice ? esc(x.invoice) : 'next invoice') : '<span class="dim">not billed</span>'}</td><td class="r">${x.status === 'Queued' && !x.sent && can('Billing') ? `<button class="btn sm" data-act="cancelCmd" data-id="${esc(x.id)}">Cancel</button>` : ''}</td></tr>`))}</div>` : '') || '<div class="note">No firms yet.</div>';
}
function cPeople(d) {
  return d.firms.map(f => {
    const feats = Object.entries(f.latest.features || {}).sort((a, b) => b[1] - a[1]), mx = Math.max(1, ...feats.map(x => x[1]));
    return `<div class="grid g2"><div class="panel"><div class="hd"><h3>${esc(f.latest.firm)}: logins</h3><span class="dim small">clients' portal logins aren't counted</span></div>${tbl([['Person'], ['Role'], ['Last sign-in'], ['Last active']], (f.latest.users || []).map(u => `<tr><td><b>${esc(u.name)}</b> ${u.active ? '' : pill('Inactive')}<div class="dim small mono">${esc(u.id)}</div></td><td>${esc(u.role)}</td><td class="small">${ago(u.last_login)}</td><td class="small">${ago(u.last_seen)}</td></tr>`))}</div>
      <div class="panel"><div class="hd"><h3>What they run</h3><span class="dim small">actions in the last 30 days · last ${ago(f.latest.last_activity)}</span></div><div class="bd"><div class="bars">${feats.length ? feats.map(([k, n]) => `<div class="bar"><span>${esc(k)}</span><span class="tr"><i style="width:${n / mx * 100}%"></i></span><span class="mono r">${int(n)}</span></div>`).join('') : '<span class="dim">No activity yet.</span>'}</div></div></div></div>`;
  }).join('') || '<div class="note">No firms yet.</div>';
}
function cUsage(d) {
  const name = id => (d.firms.find(f => f.id === id) || {}).latest?.firm || id;
  return `<div class="panel">${tbl([['Month'], ['Firm'], ['Edition'], ['Peak staff', 'r'], ['Traded clients', 'r'], ['Online accounts', 'r'], ['Bot computers', 'r'], ['AI answers', 'r'], ['AI charged', 'r'], ['AI topped up', 'r']], d.usage.map(u => `<tr><td>${monthLabel(u.month)}</td><td>${esc(name(u.firm))}</td><td>${edPill(u.edition || u.report.edition)}</td><td class="r mono">${u.peak_staff}</td><td class="r mono">${int(u.report.traded_month)}</td><td class="r mono">${int(u.report.kyc_month)}</td><td class="r mono">${u.peak_bots}</td><td class="r mono">${int(u.report.ai.answers)}</td><td class="r mono">${paise(u.report.ai.charged)}</td><td class="r mono">${paise(u.report.ai.topped_up)}</td></tr>`), { empty: 'No usage yet.' })}</div>`;
}
function cSupport(d) { return `<div class="panel">${ticketTable(d.tickets)}</div>`; }
function cBilling(d) {
  return (d.bill ? `<div class="panel"><div class="hd"><h3>${monthLabel(S.me.month)} so far</h3>${d.bill.billable ? `<button class="btn sm" data-act="genOne" data-id="${esc(d.customer.id)}">Make draft invoice</button>` : `<span class="dim small">Not billed: ${esc(d.bill.why)}</span>`}</div>${linesTable(d.bill.invoice)}</div>` : '') +
    `<div class="panel"><div class="hd"><h3>Invoices</h3></div>${invTable(d.invoices.map(i => ({ ...i, cust_name: d.customer.name })))}</div>`;
}

V.installs = {
  load: () => get('/api/installations'),
  html: ins => ph('Installations', 'Every Ledgerline server that reports in: the one you host, and brokers’ own servers. Each has its own key; a server is offline when it hasn’t reported for 35 minutes.', can('Billing') ? '<button class="btn pri" data-act="newInstall">Add installation</button>' : '') +
    `<div class="panel">${tbl([['Installation'], ['Kind'], ['Status'], ['Version'], ['Firms'], ['Region'], ['Server'], ['Key'], ['']], ins.map(i => `<tr><td><b>${esc(i.name)}</b><div class="dim small mono">${esc(i.id)}</div></td><td>${i.kind === 'hosted' ? pill('Hosted by us', 'acc') : pill('Their own')}</td>
      <td class="small">${i.revoked ? pill('Key revoked', 'bad') : i.last_report ? onDot(i.online) + '<br>' + ago(i.last_report) : '<span class="dim">waiting for first report</span>'}</td><td class="mono small">${esc(i.info.version || '—')}</td><td class="mono">${(i.firms || []).length}</td>
      <td class="small">${esc(i.info.region || '—')}${i.info.india_only ? ' ' + pill('India only') : ''}</td><td class="small">${esc(i.info.host || '')}<div class="dim">${esc([i.info.database, i.info.os].filter(Boolean).join(' · '))}</div></td><td class="mono small">…${esc(i.key_hint)}</td>
      <td class="r nowrap">${can('Billing') ? `<button class="btn sm" data-act="rekey" data-id="${esc(i.id)}">New key</button> ${i.revoked ? '' : `<button class="btn sm danger" data-act="revoke" data-id="${esc(i.id)}">Revoke</button>`}` : ''}</td></tr>`), { empty: 'No installations yet. Add one and give its key to the server.' })}</div>`
};

V.usage = {
  load: () => get('/api/usage?month=' + enc(S.ui.um || S.me.month)),
  html: rows => {
    const m = S.ui.um || S.me.month;
    const sum = k => rows.reduce((a, r) => a + (Number(k(r)) || 0), 0);
    const tot = `<tfoot><tr><td colspan="3">All firms</td><td class="r mono">${int(sum(r => r.peak_staff))}</td><td class="r mono">${int(sum(r => r.report.traded_month))}</td><td class="r mono">${int(sum(r => r.report.kyc_month))}</td><td class="r mono">${int(sum(r => r.peak_bots))}</td><td class="r mono">${int(sum(r => r.report.ai.answers))}</td><td class="r mono">${inr(sum(r => r.report.ai.cost_inr))}</td><td class="r mono">${paise(sum(r => r.report.ai.charged))}</td><td class="r mono">${paise(sum(r => r.report.ai.balance))}</td></tr></tfoot>`;
    return ph('Usage & AI', 'What each firm used in the month: the counts billing is based on, and Ledgy AI credit used against what it cost us.', `<button class="btn" data-act="um" data-id="-1">‹</button><span class="btn" style="cursor:default">${monthLabel(m)}</span><button class="btn" data-act="um" data-id="1" ${m >= S.me.month ? 'disabled' : ''}>›</button>`) +
      `<div class="panel">${tbl([['Firm'], ['Customer'], ['Edition'], ['Peak staff', 'r'], ['Traded clients', 'r'], ['Online accounts', 'r'], ['Bot computers', 'r'], ['AI answers', 'r'], ['AI cost to us', 'r'], ['AI charged', 'r'], ['AI credit left', 'r']], rows.map(r => `<tr class="click" data-act="go" data-id="customer/${enc(r.customer)}"><td><b>${esc(r.firm_name)}</b></td><td>${esc(r.cust_name)}</td><td>${edPill(r.edition || r.report.edition)}</td><td class="r mono">${r.peak_staff}</td><td class="r mono">${int(r.report.traded_month)}</td><td class="r mono">${int(r.report.kyc_month)}</td><td class="r mono">${r.peak_bots}</td><td class="r mono">${int(r.report.ai.answers)}</td><td class="r mono">${inr(r.report.ai.cost_inr)}</td><td class="r mono">${paise(r.report.ai.charged)}</td><td class="r mono ${r.report.ai.balance < 20000 && r.report.ai.on ? 'neg' : ''}">${paise(r.report.ai.balance)}</td></tr>`), { empty: 'No reports for this month.', foot: rows.length ? tot : '' })}</div>`;
  }
};

function linesTable(iv) {
  const gst = Number(iv.igst) ? `<tr><td colspan="4" class="r">IGST ${esc(iv.gst_pct)}%</td><td class="r mono">${inr(iv.igst)}</td></tr>` : `<tr><td colspan="4" class="r">CGST</td><td class="r mono">${inr(iv.cgst)}</td></tr><tr><td colspan="4" class="r">SGST</td><td class="r mono">${inr(iv.sgst)}</td></tr>`;
  return tbl([['Description'], ['SAC'], ['Qty', 'r'], ['Rate', 'r'], ['Amount', 'r']], (iv.lines || []).map((l, i) => `<tr><td>${esc(l.desc)}${iv.status === 'Draft' && iv.id && l.kind === 'other' ? ` <button class="btn sm" data-act="rmLine" data-id="${i}">Remove</button>` : ''}</td><td class="mono small">${esc(l.sac)}</td><td class="r mono">${int(l.qty)}</td><td class="r mono">${inr(l.rate)}</td><td class="r mono">${inr(l.amount)}</td></tr>`), {
    empty: 'Nothing to bill.', foot: (iv.lines || []).length ? `<tfoot><tr><td colspan="4" class="r">Subtotal</td><td class="r mono">${inr(iv.subtotal)}</td></tr>${Number(iv.discount) ? `<tr><td colspan="4" class="r">Discount</td><td class="r mono">−${inr(iv.discount)}</td></tr>` : ''}<tr><td colspan="4" class="r">Taxable value</td><td class="r mono">${inr(iv.taxable)}</td></tr>${gst}<tr><td colspan="4" class="r">Total</td><td class="r mono">${inr(iv.total)}</td></tr></tfoot>` : ''
  });
}
function invTable(ivs) {
  return tbl([['Invoice'], ['Customer'], ['Period'], ['Date'], ['Due'], ['Total', 'r'], ['Received', 'r'], ['Status']], ivs.map(i => `<tr class="click" data-act="go" data-id="invoice/${enc(i.id)}"><td><b class="mono">${esc(i.number || 'Draft')}</b><div class="dim small mono">${esc(i.id)}</div></td><td>${esc(i.cust_name || i.customer)}</td><td>${monthLabel(i.period)}</td><td class="small">${fmtDate(i.date)}</td><td class="small">${fmtDate(i.due)}</td><td class="r mono">${inr(i.total)}</td><td class="r mono">${inr(i.paid)}</td><td>${invPill(i.status, i.overdue)}</td></tr>`), { empty: 'No invoices yet.' });
}

V.billing = {
  load: async () => { const b = await get('/api/billing?month=' + enc(S.ui.bm || S.me.month)); if (['recv', 'tds', 'cns'].includes(S.ui.btab)) b.rv = await get('/api/receivables'); return b; },
  html: b => {
    const tab = S.ui.btab || 'month', m = b.month;
    if (b.rv) return billingHead(b) + ({ recv: recvTab, tds: tdsTab, cns: cnTab }[tab])(b.rv);
    const billable = b.preview.filter(x => x.billable && x.invoice.lines.length);
    const total = billable.reduce((a, x) => a + Number(x.invoice.taxable), 0);
    return ph('Billing', 'Monthly fee by edition, clients who traded (in slabs), extra staff logins, bot computers, online account opening, AI credit top-ups and setup fees, with GST. Make drafts at month end, check them, then issue.',
      `<button class="btn" data-act="go" data-id="settings">Prices</button>`) +
      billTabs(b) +
      (tab === 'invoices' ? `<div class="tools">${seg('ivf', [['open', 'Unpaid'], ['all', 'All'], ['Draft', 'Drafts'], ['Paid', 'Paid']])}</div><div class="panel">${invTable(b.invoices.filter(i => { const f = S.ui.ivf || 'open'; return f === 'all' || (f === 'open' ? ['Issued', 'Part-paid'].includes(i.status) : i.status === f); }))}</div>` :
        `<div class="tools"><button class="btn" data-act="bm" data-id="-1">‹</button><span class="btn" style="cursor:default">${monthLabel(m)}</span><button class="btn" data-act="bm" data-id="1" ${m >= S.me.month ? 'disabled' : ''}>›</button><span class="dim small">${m === S.me.month ? 'Month to date: counts keep changing until the month ends.' : ''}</span><span style="flex:1"></span><button class="btn pri" data-act="genAll" ${billable.length ? '' : 'disabled'}>Make draft invoices (${billable.length})</button></div>
        <div class="panel">${tbl([['Customer'], ['Lines'], ['Taxable', 'r'], ['GST', 'r'], ['Total', 'r'], ['Invoice']], b.preview.map(x => `<tr class="click" data-act="go" data-id="customer/${enc(x.customer.id)}"><td><b>${esc(x.customer.name)}</b> ${custPill(x.customer.status)}</td><td class="small">${x.invoice.lines.map(l => esc(l.desc.split(' — ')[0])).filter((v, i, a) => a.indexOf(v) === i).join(' · ') || '<span class="dim">no usage</span>'}</td>
          <td class="r mono">${inr(x.invoice.taxable)}</td><td class="r mono">${inr(Number(x.invoice.cgst) + Number(x.invoice.sgst) + Number(x.invoice.igst))}</td><td class="r mono">${inr(x.invoice.total)}</td><td>${x.existing ? `<button class="btn sm" data-act="go" data-id="invoice/${enc(x.existing)}">${esc(x.existing)}</button>` : x.billable ? '<span class="dim small">not made</span>' : `<span class="dim small">${esc(x.why)}</span>`}</td></tr>`), { empty: 'No customers yet.', foot: `<tfoot><tr><td colspan="2">Billable</td><td class="r mono">${inr(total)}</td><td colspan="3"></td></tr></tfoot>` })}</div>`);
  }
};

V.invoice = {
  load: async id => { const iv = await get('/api/invoices/' + enc(id)); iv.cns = await get('/api/invoices/' + enc(id) + '/credit-notes').catch(() => []); return iv; },
  html: iv => {
    const due = Number(iv.balance), live = ['Issued', 'Part-paid'].includes(iv.status), issued = !['Draft', 'Void'].includes(iv.status);
    return ph(iv.number ? 'Invoice ' + esc(iv.number) : 'Draft invoice', `${esc(iv.id)} · ${esc(iv.bill.name)} · ${monthLabel(iv.period)} · ${invPill(iv.status)}${iv.due ? ' · due ' + fmtDate(iv.due) : ''}`,
      `<button class="btn" data-act="go" data-id="billing">Billing</button><a class="btn" href="/api/invoices/${enc(iv.id)}/print" target="_blank" rel="noopener">${iv.status === 'Draft' ? 'Preview' : 'Print'}</a>
      ${iv.status === 'Draft' ? `<button class="btn" data-act="addLine">Add a line</button><button class="btn pri" data-act="issue">Issue</button>` : ''}
      ${issued ? `<a class="btn" href="/api/invoices/${enc(iv.id)}/pdf">PDF</a><button class="btn" data-act="ivEmail">${iv.emailed ? 'Email again' : 'Email to customer'}</button>` : ''}
      ${live ? `<button class="btn" data-act="remind">Send reminder</button>` : ''}
      ${issued ? `<button class="btn" data-act="creditNote">Credit note</button>` : ''}
      ${live ? `<button class="btn pri" data-act="payment">Record payment</button>` : ''}
      ${iv.status !== 'Void' && !Number(iv.paid) && !Number(iv.credited) ? `<button class="btn danger" data-act="void">Void</button>` : ''}`) +
      (iv.emailed ? `<div class="note">Emailed to ${esc(iv.emailed_to)} ${ago(iv.emailed)}.</div>` : '') +
      (iv.status === 'Draft' && (!iv.bill.address || !(iv.bill.legal_name || iv.bill.name)) ? `<div class="note warn">Add ${esc(iv.bill.name)}’s legal name and billing address (Edit details on the customer) before issuing.</div>` : '') +
      `<div class="grid g2"><div class="panel"><div class="hd"><h3>Bill to</h3></div><div class="bd"><b>${esc(iv.bill.legal_name || iv.bill.name)}</b><div class="small" style="white-space:pre-line">${esc(iv.bill.address)}</div><div class="small">${iv.bill.gstin ? 'GSTIN ' + esc(iv.bill.gstin) : '<span class="dim">No GSTIN</span>'}</div></div></div>
      <div class="panel"><div class="hd"><h3>Payments</h3>${due > 0 && issued ? `<span class="small">Due <b>${inr(due)}</b>${Number(iv.credited) ? ` · credited ${inr(iv.credited)}` : ''}</span>` : ''}</div>${tbl([['Date'], ['Mode'], ['Reference'], ['Amount', 'r'], ['TDS', 'r'], ['TDS certificate']], (iv.payments || []).map((p, i) => `<tr><td>${fmtDate(p.date)}</td><td>${esc(p.mode)}</td><td class="mono small">${esc(p.ref)}</td><td class="r mono">${inr(p.amount)}</td><td class="r mono">${Number(p.tds) ? inr(p.tds) : '—'}</td><td class="small">${Number(p.tds) ? (p.tds_cert ? esc(p.tds_cert) : `<button class="btn sm" data-act="tdsCert" data-id="${iv.id}|${i}">Record Form 16A</button>`) : ''}</td></tr>`), { empty: 'None yet.' })}</div></div>
      ${iv.cns.length ? `<div class="panel"><div class="hd"><h3>Credit notes</h3></div>${tbl([['Number'], ['Date'], ['Reason'], ['Before GST', 'r'], ['Total', 'r'], ['']], iv.cns.map(c => `<tr><td class="mono">${esc(c.number)}</td><td>${fmtDate(c.date)}</td><td class="small">${esc(c.reason)}</td><td class="r mono">${inr(c.taxable)}</td><td class="r mono">${inr(c.total)}</td><td class="r"><a class="btn sm" href="/api/credit-notes/${enc(c.id)}/pdf">PDF</a></td></tr>`))}</div>` : ''}
      ${(iv.reminders || []).length ? `<div class="panel"><div class="hd"><h3>Reminders sent</h3></div>${tbl([['When'], ['By'], ['How'], ['To'], ['Days late', 'r']], iv.reminders.map(r => `<tr><td class="small">${fmtStamp(r.at)}</td><td>${esc(r.by)}</td><td>${esc(r.via)}${r.result ? `<div class="small neg">${esc(r.result)}</div>` : ''}</td><td class="small">${esc(r.to || '—')}</td><td class="r">${r.days}</td></tr>`))}</div>` : ''}
      <div class="panel"><div class="hd"><h3>Lines</h3></div>${linesTable(iv)}</div>
      <div class="panel"><div class="hd"><h3>Notes on the invoice</h3>${iv.status === 'Draft' ? '<button class="btn sm" data-act="saveNotes">Save</button>' : ''}</div><div class="bd">${iv.status === 'Draft' ? `<textarea class="in" id="iv-notes" rows="2" maxlength="1000">${esc(iv.notes)}</textarea>` : `<div style="white-space:pre-line">${esc(iv.notes) || '<span class="dim">None</span>'}</div>`}</div></div>`;
  }
};

function ticketTable(ts) {
  return tbl([['Request'], ['Firm'], ['From'], ['Priority'], ['Status'], ['With'], ['First reply'], ['Updated', 'r']], ts.map(t => {
    const od = !t.first_response && !['Resolved', 'Closed'].includes(t.status) && new Date(t.respond_by) < new Date();
    return `<tr class="click" data-act="go" data-id="ticket/${enc(t.id)}"><td><b>${esc(t.subject)}</b><div class="dim small">${tkNo(t)} · ${esc(t.category)}${t.screen ? ' · on ' + esc(t.screen) : ''}</div></td><td>${esc(t.firm_name)}</td><td class="small">${esc(t.user_name)}<div class="dim">${esc(t.role)}</div></td><td>${priPill(t.priority)}</td><td>${tkPill(t.status)}</td><td class="small">${esc(t.assignee || '—')}</td>
      <td class="small">${t.first_response ? ago(t.first_response) : od ? '<b class="neg">overdue</b>' : 'by ' + fmtStamp(t.respond_by)}</td><td class="r small">${ago(t.updated)}</td></tr>`;
  }), { empty: 'No requests.' });
}

V.support = {
  load: async () => { const st = S.ui.stt || 'req'; if (st === 'num') return { num: await get('/api/support/stats?month=' + enc(S.ui.sm || S.me.month)) }; if (st === 'can') return { can: await get('/api/canned') }; return get('/api/tickets'); },
  html: ts => {
    if (ts.num) return supportHead() + supportNumbers(ts.num);
    if (ts.can) return supportHead() + cannedList(ts.can);
    const f = S.ui.tf || 'open', mine = S.ui.tm === '1';
    const rows = ts.filter(t => (f === 'all' || (f === 'open' ? !['Resolved', 'Closed'].includes(t.status) : t.status === f)) && (!mine || t.assignee === S.me.id));
    const n = s => ts.filter(t => t.status === s).length;
    return supportHead() +
      `<div class="kpis">${kpi('New', n('New'), 'not yet answered', { alert: n('New') > 0 })}${kpi('Open', n('Open'), '')}${kpi('Waiting on customer', n('Waiting on customer'), '')}${kpi('Resolved', n('Resolved'), 'customer can reopen by replying')}</div>
      <div class="tools">${seg('tf', [['open', 'Open'], ['New', 'New'], ['Waiting on customer', 'Waiting'], ['Resolved', 'Resolved'], ['Closed', 'Closed'], ['all', 'All']])}${seg('tm', [['0', 'Everyone’s'], ['1', 'Mine']])}</div>
      <div class="panel">${ticketTable(rows)}</div>`;
  }
};

V.ticket = {
  load: async id => { const [t, staff, canned] = await Promise.all([get('/api/tickets/' + enc(id)), get('/api/staff'), get('/api/canned').catch(() => [])]); return { t, staff, canned }; },
  html: ({ t, staff, canned }) => {
    const done = t.status === 'Closed';
    return ph(esc(t.subject), `${tkNo(t)} · ${esc(t.firm_name)} · ${tkPill(t.status)} ${priPill(t.priority)}`, `<button class="btn" data-act="go" data-id="support">All requests</button><button class="btn" data-act="go" data-id="customer/${enc(t.customer)}">Customer</button>`) +
      `<div class="grid" style="grid-template-columns:minmax(0,1fr) 280px;align-items:start"><div><div class="panel"><div class="bd thread">${t.messages.map(m => m.from === 'status' ? `<div class="status-line">${esc(m.text)} · ${esc(m.who)} · ${fmtStamp(m.at)}</div>` :
        `<div class="msg ${m.from}"><div class="who"><b>${esc(m.who)}</b> ${m.from === 'note' ? pill('Internal note', 'warn') : m.from === 'support' ? pill('Ledgerline support', 'acc') : pill(t.firm_name)} <span class="dim">${fmtStamp(m.at)}</span></div><div class="tx">${esc(m.text)}</div></div>`).join('')}</div></div>
      ${done ? '<div class="note">Closed. The customer can write a new request from Ledgerline.</div>' : `<div class="panel"><div class="bd"><div class="fld"><label for="tk-text">Reply</label><textarea class="in" id="tk-text" rows="5" maxlength="8000" placeholder="Sent to ${esc(t.user_name)} in Ledgerline"></textarea></div>
        <div class="tools" style="margin:0">${canned.filter(x => x.text).length ? `<select class="in" style="width:auto" data-change="cannedPick"><option value="">Insert a saved reply…</option>${canned.filter(x => x.text).map(x => `<option value="${esc(x.id)}">${esc(x.title)}</option>`).join('')}</select>` : ''}<label class="check" style="margin:0"><input type="checkbox" id="tk-note"> Internal note (not sent)</label><span style="flex:1"></span><select class="in" id="tk-status" style="width:auto"><option value="">Status after reply: automatic</option>${S.me.statuses.map(s => `<option value="${esc(s)}">${esc(s)}</option>`).join('')}</select><button class="btn pri" data-act="tkReply">Send</button></div></div></div>`}</div>
      <div><div class="panel"><div class="hd"><h3>Details</h3></div><div class="bd"><dl class="kv"><dt>From</dt><dd>${esc(t.user_name)}<div class="dim small">${esc(t.role)} · <span class="mono">${esc(t.user)}</span></div></dd><dt>Firm</dt><dd>${esc(t.firm_name)}</dd><dt>Screen</dt><dd>${esc(t.screen || '—')}</dd><dt>Version</dt><dd class="mono">${esc(t.version || '—')}</dd><dt>Category</dt><dd>${esc(t.category)}</dd><dt>Their ref.</dt><dd class="mono">${esc(t.local_id)}</dd><dt>Opened</dt><dd>${fmtStamp(t.created)}</dd><dt>First reply</dt><dd>${t.first_response ? fmtStamp(t.first_response) : 'due ' + fmtStamp(t.respond_by)}</dd>${t.resolved_at ? `<dt>Resolved</dt><dd>${fmtStamp(t.resolved_at)}</dd>` : ''}</dl></div></div>
      <div class="panel"><div class="bd"><div class="fld"><label for="tk-as">With</label><select class="in" id="tk-as" data-change="tkAssign"><option value="-">Nobody</option>${staff.filter(s => s.active).map(s => `<option value="${esc(s.id)}" ${t.assignee === s.id ? 'selected' : ''}>${esc(s.name)}</option>`).join('')}</select></div>
      <div class="fld"><label for="tk-pri">Priority</label><select class="in" id="tk-pri" data-change="tkPri">${['Low', 'Normal', 'High', 'Urgent'].map(p => `<option ${t.priority === p ? 'selected' : ''}>${p}</option>`).join('')}</select></div>
      ${done ? '' : `<div class="acts"><button class="btn sm" data-act="tkStatus" data-id="Resolved">Mark resolved</button><button class="btn sm" data-act="tkStatus" data-id="Closed">Close</button></div>`}</div></div></div></div>`;
  }
};

V.settings = {
  load: async () => { const [s, staff, log] = await Promise.all([get('/api/settings'), get('/api/staff'), can() ? get('/api/log').catch(() => []) : []]); return { s, staff, log }; },
  html: ({ s, staff, log }) => {
    const v = s.vendor, p = s.prices, own = can(), tab = S.ui.stab || 'prices', dis = own ? '' : 'disabled';
    const F = (id, label, val, o = {}) => `<div class="fld"><label for="${id}">${label}</label>${o.area ? `<textarea class="in" id="${id}" rows="3" ${dis}>${esc(val)}</textarea>` : `<input class="in ${o.mono ? 'mono' : ''}" id="${id}" value="${esc(val ?? '')}" ${o.type ? `type="${o.type}"` : ''} ${dis}>`}${o.hint ? `<span class="dim small">${o.hint}</span>` : ''}</div>`;
    const prices = `<div class="panel"><div class="hd"><h3>Monthly fee per firm, by edition</h3></div><div class="bd grid g3">${F('p-manual', 'Manual', p.edition.manual, { mono: 1 })}${F('p-autopilot', 'Autopilot', p.edition.autopilot, { mono: 1 })}${F('p-full', 'Full', p.edition.full, { mono: 1 })}</div></div>
      <div class="panel"><div class="hd"><h3>Per client who traded in the month (graduated slabs)</h3>${own ? '<button class="btn sm" data-act="addSlab">Add slab</button>' : ''}</div><div class="bd"><div id="slabs">${p.slabs.map((sl, i) => slabRow(sl, i, dis)).join('')}</div><span class="dim small">Each band is charged at its own rate: e.g. the first 500 free, the next 4,500 at ₹10. Leave “up to” empty on the last band.</span></div></div>
      <div class="panel"><div class="hd"><h3>Add-ons and one-time fees</h3></div><div class="bd grid g3">${F('p-incl', 'Staff logins included per firm', p.included_users, { type: 'number', mono: 1 })}${F('p-extra', 'Each extra staff login, per month', p.extra_user, { mono: 1 })}${F('p-bot', 'Each automation bot computer, per month', p.bot_computer, { mono: 1 })}${F('p-kyc', 'Each online account opening verified', p.kyc, { mono: 1 })}${F('p-setup', 'Setup fee for new customers', p.setup_fee, { mono: 1, hint: 'Each customer can have their own.' })}<div></div>${F('p-gst', 'GST %', p.gst_pct, { mono: 1 })}${F('p-sac', 'SAC code', p.sac, { mono: 1, hint: '997331: licensing the right to use software. Check with your CA.' })}</div><div class="bd dim small" style="padding-top:0">Ledgy AI credit topped up from Control is billed at the amount topped up, if you choose to bill it.</div></div>`;
    const company = `<div class="panel"><div class="hd"><h3>Your company, as printed on invoices</h3></div><div class="bd grid g2">${F('v-name', 'Name', v.name)}${F('v-gstin', 'GSTIN', v.gstin, { mono: 1 })}${F('v-addr', 'Address', v.address, { area: 1 })}<div>${F('v-pan', 'PAN', v.pan, { mono: 1 })}${F('v-email', 'Email', v.email)}${F('v-phone', 'Phone', v.phone)}</div>
      ${F('v-bank', 'Bank', v.bank)}${F('v-acct', 'Account number', v.account, { mono: 1 })}${F('v-ifsc', 'IFSC', v.ifsc, { mono: 1 })}${F('v-upi', 'UPI ID', v.upi, { mono: 1 })}${F('v-prefix', 'Invoice number prefix', v.prefix, { mono: 1, hint: 'Numbers run per financial year, e.g. LL/26-27/0001.' })}${F('v-due', 'Payment terms (days)', v.due_days, { type: 'number' })}${F('v-footer', 'Footer note', v.footer)}${F('v-hours', 'Support hours', v.support_hours)}</div></div>`;
    const people = `<div class="panel"><div class="hd"><h3>People who sign in to Control</h3>${own ? '<button class="btn sm" data-act="staffEdit">Add person</button>' : ''}</div>${tbl([['Person'], ['Role'], ['Status'], ['Last sign-in'], ['']], staff.map(x => `<tr><td><b>${esc(x.name)}</b><div class="dim small mono">${esc(x.id)}</div></td><td>${esc(x.role)}</td><td>${x.active ? pill('Active', 'good') : pill('Off')}</td><td class="small">${ago(x.last_login)}</td><td class="r">${own ? `<button class="btn sm" data-act="staffEdit" data-id="${esc(x.id)}">Edit</button>` : ''}</td></tr>`))}<div class="bd dim small">Owner: everything. Billing: customers, prices, invoices, AI credit and installations. Support: support requests; sees customers and installations.</div></div>`;
    const logT = `<div class="panel">${tbl([['When'], ['Who'], ['What'], ['On']], log.map(l => `<tr><td class="small nowrap">${fmtStamp(l.at)}</td><td>${esc(l.staff)}</td><td class="mono small">${esc(l.action)}</td><td class="small">${esc(l.target)} <span class="dim">${esc(l.note || '')}</span></td></tr>`))}</div>`;
    return ph('Settings', own ? '' : 'Only an Owner can change these.', own && (tab === 'prices' || tab === 'company' || tab === 'running') ? '<button class="btn pri" data-act="saveSettings">Save</button>' : '') +
      `<div class="tools">${seg('stab', [['prices', 'Prices'], ['company', 'Company & bank'], ['running', 'Reminders & versions'], ['people', 'People'], ...(own ? [['log', 'Activity']] : [])])}</div>` +
      `<div ${tab === 'prices' ? '' : 'hidden'}>${prices}</div><div ${tab === 'company' ? '' : 'hidden'}>${company}</div><div ${tab === 'running' ? '' : 'hidden'}>${runningPanel(s.ops || {}, dis)}</div>` + (tab === 'people' ? people : '') + (tab === 'log' ? logT : '');
  }
};
const slabRow = (sl, i, dis) => `<div class="grid g3 slab" style="align-items:end"><div class="fld"><label>Clients up to</label><input class="in mono" data-slab-up value="${sl.up_to || ''}" placeholder="and above" ${dis}></div><div class="fld"><label>Rate per client</label><input class="in mono" data-slab-rate value="${esc(sl.rate)}" ${dis}></div><div class="fld">${dis ? '' : `<button class="btn sm" data-act="rmSlab" data-id="${i}">Remove</button>`}</div></div>`;

// ---- actions ----
const A = {
  go: id => go(id),
  close: () => closeModal(),
  ovclose: (id, el, e) => { if (e.target === el) closeModal(); },
  ui: (id, el) => { S.ui[el.dataset.k] = el.dataset.v; render(); },
  cq: v => { S.ui.cq = v; $('page').innerHTML = V[S.view].html(S.data); bind(); },
  logout: async () => { try { await post('/api/logout'); } catch (e) {} showLogin('You have signed out.'); },
  pw: () => modal(mhd('Change password') + `<div class="bd"><div class="fld"><label for="pw-old">Current password</label><input class="in" id="pw-old" type="password" autocomplete="current-password"></div><div class="fld"><label for="pw-new">New password (12 characters or more)</label><input class="in" id="pw-new" type="password" autocomplete="new-password"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="pwSave">Save</button></div>`),
  pwSave: () => act(() => post('/api/password', { old: $('pw-old').value, new: $('pw-new').value }), 'Password changed', false),
  um: id => { S.ui.um = shiftMonth(S.ui.um || S.me.month, +id); render(); },
  bm: id => { S.ui.bm = shiftMonth(S.ui.bm || S.me.month, +id); render(); },

  custEdit: id => {
    const c = id ? S.data.customer : { status: 'Active', discount_pct: '0', setup_fee: S.me.setup_fee || '0' };
    const F = (k, label, o = {}) => `<div class="fld"><label for="cu-${k}">${label}</label>${o.area ? `<textarea class="in" id="cu-${k}" rows="3">${esc(c[k])}</textarea>` : `<input class="in ${o.mono ? 'mono' : ''}" id="cu-${k}" value="${esc(c[k] ?? '')}" placeholder="${esc(o.ph || '')}">`}</div>`;
    modal(mhd(id ? 'Edit ' + esc(c.name) : 'Add customer', 'The legal name, address and GSTIN are printed on invoices. The GSTIN’s state decides CGST + SGST or IGST.') +
      `<div class="bd"><div class="grid g2">${F('name', 'Name')}${F('legal_name', 'Legal name')}${F('gstin', 'GSTIN', { mono: 1 })}${F('state', 'State code (if no GSTIN)', { mono: 1, ph: 'e.g. 27' })}</div>${F('address', 'Billing address', { area: 1 })}
      <div class="grid g3">${F('contact', 'Contact person')}${F('email', 'Email')}${F('phone', 'Phone')}</div>
      <div class="grid g3"><div class="fld"><label for="cu-status">Status</label><select class="in" id="cu-status">${S.me.customer_statuses.map(s => `<option ${c.status === s ? 'selected' : ''}>${s}</option>`).join('')}</select></div>${F('bill_from', 'Billing starts (YYYY-MM)', { mono: 1, ph: S.me.month })}${F('discount_pct', 'Discount %', { mono: 1 })}</div>
      <div class="grid g3">${F('setup_fee', 'Setup fee ₹ (0 for none)', { mono: 1 })}${F('trial_ends', 'Trial ends', { ph: 'YYYY-MM-DD' })}${F('tds_pct', 'TDS they deduct %', { mono: 1, ph: 'e.g. 10' })}</div>
      <h4 style="margin:14px 0 6px">Contract</h4><div class="grid g3">${F('contract_start', 'Starts', { ph: 'YYYY-MM-DD' })}${F('contract_end', 'Ends / renews', { ph: 'YYYY-MM-DD' })}${F('min_monthly', 'Minimum monthly bill ₹', { mono: 1, ph: '0 = none' })}</div>
      <div class="grid g3">${['manual', 'autopilot', 'full'].map(e => `<div class="fld"><label for="cu-ef-${e}">Agreed ${EDN[e]} fee ₹</label><input class="in mono" id="cu-ef-${e}" value="${esc((c.edition_fee || {})[e] ?? '')}" placeholder="price book"></div>`).join('')}</div>
      <div class="grid g3">${F('included_users', 'Staff logins included', { mono: 1, ph: 'price book' })}</div>
      <h4 style="margin:14px 0 6px">Who to write to</h4><div class="grid g2">${F('billing_email', 'Billing email (invoices, reminders)', { ph: 'defaults to the email above' })}${F('cc_emails', 'Copy to (comma separated)')}${F('tech_contact', 'Technical contact')}${F('tech_email', 'Technical email')}</div>
      ${F('notes', 'Notes (not printed)', { area: 1 })}</div>
      <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="custSave" data-id="${esc(id || '')}">Save</button></div>`);
  },
  custSave: async id => {
    const v = k => $('cu-' + k).value.trim();
    const b = { id: id || '', name: v('name'), legal_name: v('legal_name'), gstin: v('gstin'), state: v('state'), address: v('address'), contact: v('contact'), email: v('email'), phone: v('phone'), status: v('status'), bill_from: v('bill_from'), discount_pct: v('discount_pct') || '0', setup_fee: v('setup_fee') || '0', notes: v('notes'),
      trial_ends: v('trial_ends'), tds_pct: v('tds_pct'), contract_start: v('contract_start'), contract_end: v('contract_end'), min_monthly: v('min_monthly') || '0', included_users: parseInt(v('included_users'), 10) || 0,
      billing_email: v('billing_email'), cc_emails: v('cc_emails'), tech_contact: v('tech_contact'), tech_email: v('tech_email') };
    const ef = {}; ['manual', 'autopilot', 'full'].forEach(e => { const x = $('cu-ef-' + e).value.trim(); if (x !== '') ef[e] = x; }); if (Object.keys(ef).length) b.edition_fee = ef;
    const r = await act(() => post('/api/customers', b), 'Saved', false).catch(() => null);
    if (r) go('customer/' + enc(r.id));
  },
  linkFirm: id => {
    const all = S.data.all.filter(c => c.id !== S.data.customer.id);
    modal(mhd('Move firm to another customer', 'Use this when the same broker runs firms on two servers, or a new firm belongs to an existing customer. Its usage and requests move with it.') +
      `<div class="bd"><div class="fld"><label for="lf-c">Customer</label><select class="in" id="lf-c">${all.map(c => `<option value="${esc(c.id)}">${esc(c.name)} (${esc(c.id)})</option>`).join('')}</select></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="linkSave" data-id="${esc(id)}" ${all.length ? '' : 'disabled'}>Move</button></div>`);
  },
  linkSave: id => act(() => post('/api/firms/' + enc(id) + '/customer', { customer: $('lf-c').value }), 'Moved'),
  aiCredit: id => {
    const f = S.data.firms.find(x => x.id === id);
    modal(mhd('Top up Ledgy AI credit', `For ${esc(f.latest.firm)}. Added at the server’s next report (within 15 minutes); the firm sees it straight away after that. Credit left now: ${paise(f.latest.ai.balance)}.`) +
      `<div class="bd"><div class="fld"><label for="ai-amt">Amount ₹</label><input class="in mono" id="ai-amt" placeholder="e.g. 5000"></div><div class="fld"><label for="ai-note">Note</label><input class="in" id="ai-note" maxlength="100" placeholder="e.g. paid by NEFT, ref …"></div>
      <label class="check"><input type="checkbox" id="ai-bill" checked> Put it on the customer’s next invoice</label></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="aiSend" data-id="${esc(id)}">Top up</button></div>`);
  },
  aiSend: id => act(() => post('/api/firms/' + enc(id) + '/ai-credit', { amount: $('ai-amt').value.trim() || '0', note: $('ai-note').value, bill: $('ai-bill').checked }), r => `${r.id} queued: added at the next report`),
  cancelCmd: id => act(() => post('/api/commands/' + enc(id) + '/cancel'), 'Cancelled'),

  newInstall: () => modal(mhd('Add installation', 'For the server you host, or a broker’s own server. You get a key to put in that server’s settings.') +
    `<div class="bd"><div class="fld"><label for="in-name">Name</label><input class="in" id="in-name" placeholder="e.g. Hosted (Mumbai) or Acme Securities’ server"></div><div class="fld"><label for="in-kind">Kind</label><select class="in" id="in-kind"><option value="own">The broker’s own server</option><option value="hosted">Hosted by us</option></select></div></div>
    <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="installSave">Add</button></div>`),
  installSave: async () => { try { const r = await post('/api/installations', { name: $('in-name').value, kind: $('in-kind').value }); showKey(r.installation, r.key); } catch (e) { toast(e.message, true); } },
  rekey: async id => { if (!confirmIn(id, 'new key')) return; try { const r = await post('/api/installations/' + enc(id), { rekey: true }); showKey(r.installation, r.key); } catch (e) { toast(e.message, true); } },
  revoke: id => { if (!confirmIn(id, 'revoke')) return; act(() => post('/api/installations/' + enc(id), { revoke: true }), 'Key revoked: that server can no longer report'); },

  genAll: () => act(() => post('/api/invoices/generate', { month: S.ui.bm || S.me.month }), r => `${r.length} draft invoice${r.length === 1 ? '' : 's'} ready to check`),
  genOne: id => act(() => post('/api/invoices/generate', { month: S.me.month, customer: id }), r => r.length ? 'Draft ' + r[0].id + ' made' : 'Nothing to bill'),
  addLine: () => modal(mhd('Add a line', 'For anything not worked out from usage: training, a custom report, a credit.') + `<div class="bd"><div class="fld"><label for="ln-d">Description</label><input class="in" id="ln-d" maxlength="200"></div><div class="grid g3"><div class="fld"><label for="ln-q">Quantity</label><input class="in mono" id="ln-q" value="1"></div><div class="fld"><label for="ln-r">Rate ₹ (negative for a credit)</label><input class="in mono" id="ln-r"></div></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="lineSave">Add</button></div>`),
  lineSave: () => act(() => post('/api/invoices/' + enc(S.arg) + '/edit', { add: { desc: $('ln-d').value, qty: parseInt($('ln-q').value, 10) || 0, rate: $('ln-r').value.trim() || '0' } }), 'Line added'),
  rmLine: i => act(() => post('/api/invoices/' + enc(S.arg) + '/edit', { remove: +i }), 'Line removed'),
  saveNotes: () => act(() => post('/api/invoices/' + enc(S.arg) + '/edit', { notes: $('iv-notes').value }), 'Saved'),
  issue: () => modal(mhd('Issue invoice', 'It gets the next number in this financial year’s series and can no longer be changed (only voided).') + `<div class="bd"><div class="fld"><label for="is-d">Invoice date</label><input class="in" type="date" id="is-d" value="${esc(S.me.today)}"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="issueGo">Issue</button></div>`),
  issueGo: () => act(() => post('/api/invoices/' + enc(S.arg) + '/issue', { date: $('is-d').value }), r => 'Issued as ' + r.number),
  void: () => modal(mhd('Void invoice', 'An issued invoice keeps its number and stays on record as void. Anything it billed (setup fee, AI credit) can be billed again.') + `<div class="bd"><div class="fld"><label for="vd-r">Reason</label><input class="in" id="vd-r" maxlength="300"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn danger" data-act="voidGo">Void</button></div>`),
  voidGo: () => act(() => post('/api/invoices/' + enc(S.arg) + '/void', { reason: $('vd-r').value }), 'Voided'),
  payment: () => { const iv = S.data, due = Number(iv.balance).toFixed(2); modal(mhd('Record payment', `Due: ${inr(due)}. If the customer deducted TDS (usually 10% or 2% of ${inr(iv.taxable)} under section 194J), enter it separately: it settles the invoice like money.`) + `<div class="bd"><div class="grid g2"><div class="fld"><label for="pm-a">Amount received ₹</label><input class="in mono" id="pm-a" value="${due}"></div><div class="fld"><label for="pm-t">TDS deducted ₹</label><input class="in mono" id="pm-t" value="0" data-taxable="${esc(iv.taxable)}"><span class="small"><button class="linkish" data-act="tdsFill" data-id="10">10%</button> · <button class="linkish" data-act="tdsFill" data-id="2">2%</button></span></div><div class="fld"><label for="pm-d">Date received</label><input class="in" type="date" id="pm-d" value="${esc(S.me.today)}"></div><div class="fld"><label for="pm-m">Mode</label><select class="in" id="pm-m"><option>Bank transfer</option><option>UPI</option><option>Cheque</option><option>TDS deducted</option><option>Other</option></select></div><div class="fld"><label for="pm-r">Reference (UTR, cheque no.)</label><input class="in mono" id="pm-r"></div></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="payGo">Record</button></div>`); },
  payGo: () => act(() => post('/api/invoices/' + enc(S.arg) + '/payment', { amount: $('pm-a').value.trim() || '0', tds: $('pm-t').value.trim() || '0', date: $('pm-d').value, mode: $('pm-m').value, ref: $('pm-r').value }), r => r.status === 'Paid' ? 'Paid in full' : 'Payment recorded'),
  tdsFill: pc => { const t = (Number($('pm-t').dataset.taxable) * pc / 100).toFixed(2), due = Number(S.data.balance); $('pm-t').value = t; $('pm-a').value = (due - t).toFixed(2); },

  tkReply: () => act(() => post('/api/tickets/' + enc(S.arg) + '/reply', { text: $('tk-text').value, internal: $('tk-note').checked, status: $('tk-status').value }), () => $('tk-note').checked ? 'Note added' : 'Reply sent to Ledgerline'),
  tkStatus: s => act(() => post('/api/tickets/' + enc(S.arg) + '/reply', { status: s }), 'Status: ' + s),
  tkAssign: v => act(() => post('/api/tickets/' + enc(S.arg), { assignee: v }), 'Saved'),
  tkPri: v => act(() => post('/api/tickets/' + enc(S.arg), { priority: v }), 'Priority: ' + v),

  addSlab: () => { const d = document.createElement('div'); d.innerHTML = slabRow({ up_to: 0, rate: '0' }, document.querySelectorAll('.slab').length, ''); $('slabs').appendChild(d.firstElementChild); },
  rmSlab: (i, el) => el.closest('.slab').remove(),
  saveSettings: () => {
    const val = id => $(id).value.trim();
    const slabs = [...document.querySelectorAll('.slab')].map(r => ({ up_to: parseInt(r.querySelector('[data-slab-up]').value, 10) || 0, rate: r.querySelector('[data-slab-rate]').value.trim() || '0' }));
    const b = {
      vendor: { name: val('v-name'), gstin: val('v-gstin'), address: $('v-addr').value.trim(), pan: val('v-pan'), email: val('v-email'), phone: val('v-phone'), bank: val('v-bank'), account: val('v-acct'), ifsc: val('v-ifsc'), upi: val('v-upi'), prefix: val('v-prefix'), due_days: parseInt(val('v-due'), 10) || 0, footer: val('v-footer'), support_hours: val('v-hours') },
      ops: { latest_version: val('o-ver'), auto_remind: $('o-auto').checked, remind_every: parseInt(val('o-every'), 10) || 7, notice_overdue: $('o-notice').checked, suspend_after: parseInt(val('o-susp'), 10) || 0 },
      prices: { edition: { manual: val('p-manual') || '0', autopilot: val('p-autopilot') || '0', full: val('p-full') || '0' }, slabs, included_users: parseInt(val('p-incl'), 10) || 0, extra_user: val('p-extra') || '0', bot_computer: val('p-bot') || '0', kyc: val('p-kyc') || '0', setup_fee: val('p-setup') || '0', gst_pct: val('p-gst') || '18', sac: val('p-sac') }
    };
    act(() => api('PUT', '/api/settings', b), 'Settings saved');
  },
  staffEdit: id => {
    const x = id ? S.data.staff.find(s => s.id === id) : { active: true, role: 'Support' };
    modal(mhd(id ? 'Edit ' + esc(x.name) : 'Add person', id ? '' : 'They get a one-time password to set their own at first sign-in.') +
      `<div class="bd"><div class="grid g2"><div class="fld"><label for="sf-id">User name</label><input class="in mono" id="sf-id" value="${esc(x.id || '')}" ${id ? 'disabled' : ''}></div><div class="fld"><label for="sf-name">Name</label><input class="in" id="sf-name" value="${esc(x.name || '')}"></div></div>
      <div class="fld"><label for="sf-role">Role</label><select class="in" id="sf-role">${S.me.roles.map(r => `<option ${x.role === r ? 'selected' : ''}>${r}</option>`).join('')}</select></div>
      <label class="check"><input type="checkbox" id="sf-act" ${x.active ? 'checked' : ''}> Can sign in</label>${id ? '<label class="check"><input type="checkbox" id="sf-reset"> Give a new one-time password</label>' : ''}</div>
      <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="staffSave">Save</button></div>`);
  },
  staffSave: async () => {
    try {
      const r = await post('/api/staff', { id: $('sf-id').value.trim(), name: $('sf-name').value, role: $('sf-role').value, active: $('sf-act').checked, reset: !!$('sf-reset')?.checked });
      if (r.password) modal(mhd('One-time password', `Give it to ${esc(r.staff.name)} privately. It is shown only once.`) + `<div class="bd"><div class="secret">${esc(r.password)}</div><p class="small dim">User name: <span class="mono">${esc(r.staff.id)}</span></p></div><div class="ft"><button class="btn pri" data-act="doneReload">I’ve noted it</button></div>`);
      else { closeModal(); toast('Saved'); render(); }
    } catch (e) { toast(e.message, true); }
  },
  doneReload: () => { closeModal(); render(); },
};
function confirmIn(id, what) { return window.confirm(what === 'revoke' ? `Revoke ${id}'s key? That server stops reporting until it is given a new key.` : `Give ${id} a new key? The old key stops working at once; put the new one in the server's settings.`); }
function showKey(inst, key) {
  modal(mhd('Installation key for ' + esc(inst.name), 'Shown only once. Put these in that Ledgerline server’s environment and restart it.') +
    `<div class="bd"><div class="secret">LEDGERLINE_CONTROL_URL=${esc(location.origin)}<br>LEDGERLINE_CONTROL_KEY=${esc(key)}</div><p class="small dim">The server reports what each firm uses every 15 minutes, and support requests at once. No client details, trades or money leave it: only counts and the firm’s staff logins.</p></div>
    <div class="ft"><button class="btn pri" data-act="doneReload">I’ve copied it</button></div>`);
}

// ---- billing: receivables, credit notes, TDS ----
const billTabs = b => `<div class="tools">${seg('btab', [['month', 'Bills for a month'], ['invoices', `Invoices (${b.invoices.length})`], ['recv', 'Receivables'], ['cns', 'Credit notes'], ['tds', 'TDS']])}</div>`;
const billingHead = b => ph('Billing', 'Monthly fee by edition, clients who traded (in slabs), extra staff logins, bot computers, online account opening, AI credit top-ups and setup fees, with GST. Make drafts at month end, check them, then issue.', `<button class="btn" data-act="go" data-id="settings">Prices</button>`) + billTabs(b);
function recvTab(rv) {
  const t = rv.total;
  return (rv.mail ? '' : '<div class="note warn">No mail server is set up for Control (LLCTL_SMTP_URL), so invoices and reminders can’t be emailed from here. Notices to the firm’s Admins still work.</div>') +
    `<div class="kpis">${kpi('Outstanding', inr(t.total), `${rv.ageing.length} customer${rv.ageing.length === 1 ? '' : 's'}`)}${kpi('Not yet due', inr(t.not_due), '')}${kpi('1–30 days late', inr(t.d1_30), '', { alert: Number(t.d1_30) > 0 })}${kpi('31–90 days late', inr(Number(t.d31_60) + Number(t.d61_90)), '', { alert: Number(t.d31_60) + Number(t.d61_90) > 0 })}${kpi('Over 90 days', inr(t.over_90), '', { alert: Number(t.over_90) > 0 })}</div>
    <div class="panel"><div class="hd"><h3>Reminders due</h3><span class="dim small">${rv.ops.auto_remind ? `Sent by themselves each morning, every ${rv.ops.remind_every || 7} days` : 'Automatic reminders are off (Settings → Reminders & versions)'}</span></div>${tbl([['Invoice'], ['Customer'], ['Due', 'r'], ['Days late', 'r'], ['Reminders'], ['']], rv.reminders.map(d => `<tr><td class="mono"><button class="linkish" data-act="go" data-id="invoice/${enc(d.invoice)}">${esc(d.number)}</button></td><td>${esc(d.name)}${d.suspend ? ' ' + pill('Past suspension limit', 'bad') : ''}</td><td class="r mono">${inr(d.balance)}</td><td class="r">${d.days}</td><td class="small">${d.count ? `${d.count} sent · last ${ago(d.last)}` : 'none yet'}</td><td class="r"><button class="btn sm" data-act="remindRow" data-id="${esc(d.invoice)}">Send reminder</button></td></tr>`), { empty: 'Nothing needs a reminder.' })}</div>
    <div class="panel"><div class="hd"><h3>By customer, by age</h3></div>${tbl([['Customer'], ['Not due', 'r'], ['1–30', 'r'], ['31–60', 'r'], ['61–90', 'r'], ['Over 90', 'r'], ['Total', 'r'], ['Last paid'], ['']], rv.ageing.map(a => `<tr><td><b>${esc(a.name)}</b> ${custPill(a.status)}</td><td class="r mono">${inr(a.not_due)}</td><td class="r mono">${inr(a.d1_30)}</td><td class="r mono">${inr(a.d31_60)}</td><td class="r mono">${inr(a.d61_90)}</td><td class="r mono ${Number(a.over_90) ? 'neg' : ''}">${inr(a.over_90)}</td><td class="r mono"><b>${inr(a.total)}</b></td><td class="small">${a.last_paid ? fmtDate(a.last_paid) : '<span class="dim">never</span>'}</td><td class="r"><a class="btn sm" href="/api/customers/${enc(a.customer)}/statement.pdf">Statement</a></td></tr>`), { empty: 'Nothing outstanding.' })}</div>`;
}
const cnTab = rv => `<div class="panel">${tbl([['Number'], ['Date'], ['Invoice'], ['Customer'], ['Reason'], ['Before GST', 'r'], ['Total', 'r'], ['']], rv.credit_notes.map(c => `<tr><td class="mono">${esc(c.number)}</td><td>${fmtDate(c.date)}</td><td class="mono"><button class="linkish" data-act="go" data-id="invoice/${enc(c.invoice)}">${esc(c.invoice_number)}</button></td><td>${esc(c.bill.name)}</td><td class="small">${esc(c.reason)}</td><td class="r mono">${inr(c.taxable)}</td><td class="r mono">${inr(c.total)}</td><td class="r"><a class="btn sm" href="/api/credit-notes/${enc(c.id)}/pdf">PDF</a></td></tr>`), { empty: 'No credit notes. Make one from an issued invoice.' })}<div class="bd dim small">A credit note lowers an issued invoice (GST section 34). Report it in GSTR-1 for the month it is issued.</div></div>`;
function tdsTab(rv) {
  const q = {}; rv.tds.forEach(r => { (q[r.quarter] ||= { amt: 0, n: 0, miss: 0 }); q[r.quarter].amt += Number(r.amount); q[r.quarter].n++; if (!r.cert) q[r.quarter].miss++; });
  return `<div class="kpis">${Object.entries(q).slice(0, 4).map(([k, v]) => kpi(k, inr(v.amt), v.miss ? `<b class="neg">${v.miss} of ${v.n} certificates awaited</b>` : `${v.n} certificates in`, { alert: v.miss > 0 })).join('') || kpi('TDS', inr(0), 'none deducted yet')}</div>
    <div class="panel">${tbl([['Date'], ['Quarter'], ['Customer'], ['Invoice'], ['TDS', 'r'], ['Form 16A']], rv.tds.map(r => `<tr><td>${fmtDate(r.date)}</td><td>${esc(r.quarter)}</td><td>${esc(r.name)}</td><td class="mono"><button class="linkish" data-act="go" data-id="invoice/${enc(r.invoice)}">${esc(r.invoice_number)}</button></td><td class="r mono">${inr(r.amount)}</td><td>${r.cert ? esc(r.cert) : `<button class="btn sm" data-act="tdsCert" data-id="${esc(r.invoice)}|${r.index}">Record</button>`}</td></tr>`), { empty: 'No TDS deducted yet.' })}<div class="bd dim small">Check each against Form 26AS before claiming it in your return. Ask customers for Form 16A after each quarter’s TDS return is filed.</div></div>`;
}

// ---- settings: reminders and versions ----
const runningPanel = (o, dis) => `<div class="panel"><div class="hd"><h3>Payment reminders</h3></div><div class="bd grid g2">
  <label class="check"><input type="checkbox" id="o-auto" ${o.auto_remind ? 'checked' : ''} ${dis}> Email reminders for overdue invoices by themselves (each morning after 10:00)</label>
  <label class="check"><input type="checkbox" id="o-notice" ${o.notice_overdue ? 'checked' : ''} ${dis}> Also show the reminder to the firm’s Admins inside Ledgerline</label>
  <div class="fld"><label for="o-every">Days between reminders</label><input class="in mono" id="o-every" type="number" min="1" max="60" value="${o.remind_every || 7}" ${dis}></div>
  <div class="fld"><label for="o-susp">Flag for suspension after (days overdue, 0 = never)</label><input class="in mono" id="o-susp" type="number" min="0" max="365" value="${o.suspend_after || 0}" ${dis}></div></div></div>
  <div class="panel"><div class="hd"><h3>Versions</h3></div><div class="bd grid g2"><div class="fld"><label for="o-ver">Latest Ledgerline version</label><input class="in mono" id="o-ver" value="${esc(o.latest_version || '')}" placeholder="e.g. 1.6.0" ${dis}><span class="dim small">Servers on an older version are listed in Alerts.</span></div></div></div>`;

// ---- customer: contract, go-live checklist, notes, statement ----
function contractLine(c) {
  const bits = [];
  if (c.trial_ends) bits.push('trial ends ' + fmtDate(c.trial_ends));
  if (c.contract_end) bits.push(`contract ${c.contract_start ? fmtDate(c.contract_start) + ' – ' : 'to '}${fmtDate(c.contract_end)}`);
  if (c.edition_fee && Object.keys(c.edition_fee).length) bits.push('agreed fees: ' + Object.entries(c.edition_fee).map(([e, a]) => `${EDN[e]} ${inr(a, 0)}`).join(', '));
  if (Number(c.min_monthly)) bits.push('minimum ' + inr(c.min_monthly, 0) + '/month');
  if (c.included_users) bits.push(c.included_users + ' logins included');
  if (c.tds_pct) bits.push('deducts TDS ' + c.tds_pct + '%');
  if (c.billing_email) bits.push('bills to ' + c.billing_email);
  return bits.length ? `<div class="note">${bits.map(esc).join(' · ')}</div>` : '';
}
function setupList(items) {
  if (!items || !items.length) return '';
  const done = items.filter(x => x.ok).length;
  return `<div class="bd" style="border-top:1px solid var(--line)"><b>Go-live checklist</b> <span class="dim small">${done} of ${items.length} done</span><div class="checks">${items.map(x => `<span class="pill ${x.ok ? (x.days && x.days <= 30 ? 'warn' : 'good') : 'bad'}" title="${esc(x.detail || '')}">${x.ok ? '✓' : '✗'} ${esc(x.label)}${x.days ? ` · ${x.days} d left` : ''}${!x.ok && x.detail ? ': ' + esc(x.detail) : ''}</span>`).join(' ')}</div></div>`;
}
function cNotes(d) {
  return `<div class="panel"><div class="hd"><h3>Add a note</h3></div><div class="bd"><div class="grid g3"><div class="fld"><label for="nt-k">Kind</label><select class="in" id="nt-k">${['Note', 'Call', 'Meeting', 'Email', 'Follow-up'].map(k => `<option>${k}</option>`).join('')}</select></div><div class="fld"><label for="nt-due">Follow up on (for a follow-up)</label><input class="in" type="date" id="nt-due"></div></div><div class="fld"><label for="nt-t">Note</label><textarea class="in" id="nt-t" rows="3" maxlength="4000" placeholder="What was said or agreed"></textarea></div><button class="btn pri" data-act="noteAdd" data-id="${esc(d.customer.id)}">Save note</button></div></div>
    <div class="panel">${tbl([['When'], ['Kind'], ['Note'], ['By'], ['']], d.notes.map(n => `<tr><td class="small nowrap">${fmtStamp(n.at)}</td><td>${n.kind === 'Follow-up' ? pill(n.done ? 'Follow-up done' : 'Follow-up ' + fmtDate(n.due), n.done ? 'good' : n.due && n.due < S.me.today ? 'bad' : 'warn') : esc(n.kind)}</td><td style="white-space:pre-line">${esc(n.text)}</td><td class="small">${esc(n.by)}</td><td class="r">${n.kind === 'Follow-up' && !n.done ? `<button class="btn sm" data-act="noteDone" data-id="${esc(n.id)}">Done</button>` : ''}</td></tr>`), { empty: 'No notes yet.' })}</div>`;
}
function cStatement(d) {
  const st = d.st;
  if (!st) return '<div class="note">Loading…</div>';
  const a = st.ageing;
  return `<div class="kpis">${kpi('Balance', inr(st.closing), Number(st.closing) < 0 ? 'in their favour' : '')}${kpi('Not yet due', inr(a.not_due), '')}${kpi('1–30 days late', inr(a.d1_30), '', { alert: Number(a.d1_30) > 0 })}${kpi('Over 30 days', inr(Number(a.d31_60) + Number(a.d61_90) + Number(a.over_90)), '', { alert: Number(a.d31_60) + Number(a.d61_90) + Number(a.over_90) > 0 })}</div>
    <div class="panel"><div class="hd"><h3>Statement of account</h3><a class="btn sm" href="/api/customers/${enc(st.customer.id)}/statement.pdf">PDF</a></div>${tbl([['Date'], ['Entry'], ['Reference'], ['Details'], ['Debit', 'r'], ['Credit', 'r'], ['Balance', 'r']], st.lines.map(l => `<tr><td>${fmtDate(l.date)}</td><td>${esc(l.kind)}</td><td class="mono small">${esc(l.ref)}</td><td class="small">${esc(l.note || '')}</td><td class="r mono">${Number(l.debit) ? inr(l.debit) : ''}</td><td class="r mono">${Number(l.credit) ? inr(l.credit) : ''}</td><td class="r mono">${inr(l.balance)}</td></tr>`), { empty: 'No invoices yet.' })}</div>`;
}

// ---- support: numbers and saved replies ----
const supportHead = () => ph('Support', 'Requests sent from inside Ledgerline, with the firm, person, role, screen and version. Replies go back to Ledgerline, where Ledgy tells the person. First-reply targets: Urgent 1 h, High 4 h, Normal 1 day, Low 3 days.') + `<div class="tools">${seg('stt', [['req', 'Requests'], ['num', 'Numbers'], ['can', 'Saved replies']])}</div>`;
function supportNumbers(n) {
  const bars = (rows) => { const mx = Math.max(1, ...rows.map(r => r.count)); return `<div class="bars">${rows.map(r => `<div class="bar"><span>${esc(r.label)}</span><span class="tr"><i style="width:${r.count / mx * 100}%"></i></span><span class="mono r">${r.count}</span></div>`).join('') || '<span class="dim">None.</span>'}</div>`; };
  const obj = o => Object.entries(o).map(([label, count]) => ({ label, count })).sort((a, b) => b.count - a.count);
  const pctOn = n.answered ? Math.round(n.on_time / n.answered * 100) : 0;
  return `<div class="tools"><button class="btn" data-act="sm" data-id="-1">‹</button><span class="btn" style="cursor:default">${monthLabel(n.month)}</span><button class="btn" data-act="sm" data-id="1" ${n.month >= S.me.month ? 'disabled' : ''}>›</button></div>
    <div class="kpis">${kpi('Requests', n.opened, `${n.resolved} resolved this month`)}${kpi('First reply on time', n.answered ? pctOn + '%' : '—', `${n.on_time} of ${n.answered} answered`, { alert: n.answered && pctOn < 90 })}${kpi('Median first reply', n.median_first_hours + ' h', '')}${kpi('Median time to resolve', n.median_resolve_hours + ' h', '')}${kpi('Open now', n.open_now, n.open_now ? `oldest ${n.oldest_open_hours} h` : '', { go: 'support' })}</div>
    <div class="grid g2"><div class="panel"><div class="hd"><h3>By category</h3></div><div class="bd">${bars(obj(n.by_category))}</div></div><div class="panel"><div class="hd"><h3>By firm</h3></div><div class="bd">${bars(n.by_firm)}</div></div>
    <div class="panel"><div class="hd"><h3>By priority</h3></div><div class="bd">${bars(obj(n.by_priority))}</div></div><div class="panel"><div class="hd"><h3>Handled by</h3></div><div class="bd">${bars(n.by_staff)}</div></div></div>`;
}
const cannedList = cs => `<div class="tools"><span style="flex:1"></span>${can('Support') ? '<button class="btn pri" data-act="cannedEdit">Add a saved reply</button>' : ''}</div><div class="panel">${tbl([['Title'], ['Reply'], ['Changed'], ['']], cs.filter(c => c.text).map(c => `<tr><td><b>${esc(c.title)}</b></td><td class="small" style="white-space:pre-line;max-width:560px">${esc(c.text)}</td><td class="small">${esc(c.by)} · ${ago(c.updated)}</td><td class="r">${can('Support') ? `<button class="btn sm" data-act="cannedEdit" data-id="${esc(c.id)}">Edit</button>` : ''}</td></tr>`), { empty: 'No saved replies yet. Save the answers you give often; insert them when replying.' })}</div>`;

// ---- alerts ----
V.alerts = {
  load: () => get('/api/alerts'),
  html: as => {
    const f = S.ui.af || 'open', areas = [...new Set(as.map(a => a.area))];
    const rows = as.filter(a => (f === 'open' ? !a.acked : f === 'all' ? true : a.area === f));
    const sev = s => pill({ bad: 'Act now', warn: 'Soon', info: 'Note' }[s], { bad: 'bad', warn: 'warn', info: '' }[s]);
    return ph('Alerts', 'Everything that needs a look, worked out from the latest reports: servers not reporting or behind, firms without backups or with a day left open, go-live gaps (signing certificate, mail server, member codes), certificates expiring, trials and contracts ending, overdue invoices and requests past their first-reply target. Setting one aside hides it for 7 days.') +
      `<div class="kpis">${kpi('Act now', as.filter(a => !a.acked && a.severity === 'bad').length, '', { alert: as.some(a => !a.acked && a.severity === 'bad') })}${kpi('Soon', as.filter(a => !a.acked && a.severity === 'warn').length, '')}${kpi('Notes', as.filter(a => !a.acked && a.severity === 'info').length, '')}${kpi('Set aside', as.filter(a => a.acked).length, 'for 7 days')}</div>
      <div class="tools">${seg('af', [['open', 'Open'], ...areas.map(a => [a, a]), ['all', 'All']])}</div>
      <div class="panel">${tbl([[''], ['Area'], ['Who'], ['What'], ['']], rows.map(a => `<tr class="${a.acked ? 'dim' : ''}"><td>${sev(a.severity)}</td><td class="small">${esc(a.area)}</td><td><button class="linkish" data-act="go" data-id="${esc(a.link)}"><b>${esc(a.subject)}</b></button></td><td class="small">${esc(a.detail)}</td><td class="r">${a.acked ? `<span class="small">set aside by ${esc(a.acked_by)}</span>` : `<button class="btn sm" data-act="alertAck" data-id="${esc(a.id)}">Set aside</button>`}</td></tr>`), { empty: 'Nothing needs attention.' })}</div>`;
  }
};

// ---- notices ----
V.notices = {
  load: async () => { const [ns, cs, fs] = await Promise.all([get('/api/notices'), get('/api/customers'), get('/api/firms')]); return { ns, cs, fs }; },
  html: ({ ns }) => ph('Notices', 'Banners shown inside Ledgerline: planned maintenance, a new release, a payment reminder. Pick who sees them (every staff login or Admins only) and for how long. Each firm gets it with its next report (within about 15 minutes).', can('Support', 'Billing') ? '<button class="btn pri" data-act="noticeNew">New notice</button>' : '') +
    `<div class="panel">${tbl([['Notice'], ['To'], ['Shown'], ['Reached'], ['State'], ['']], ns.map(n => `<tr><td><b>${esc(n.title)}</b> ${pill(n.level === 'info' ? 'Info' : n.level === 'warn' ? 'Warning' : 'Urgent', n.level === 'bad' ? 'bad' : n.level === 'warn' ? 'warn' : '')}<div class="small" style="white-space:pre-line">${esc(n.text)}</div><div class="dim small">${esc(n.id)} · ${esc(n.by)} · ${fmtStamp(n.created)}</div></td><td class="small">${esc(noticeTarget(n.target))}<div class="dim">${n.audience === 'admins' ? 'Admins only' : 'every staff login'}</div></td><td class="small">${fmtStamp(n.from)}<br>to ${fmtStamp(n.until)}</td><td class="small">${n.delivered} of ${n.firms.length} firms</td><td>${n.withdrawn ? pill('Withdrawn') : n.live ? pill('Showing', 'good') : new Date(n.from) > new Date() ? pill('Scheduled', 'acc') : pill('Ended')}</td><td class="r">${!n.withdrawn && can('Support', 'Billing') && new Date(n.until) > new Date() ? `<button class="btn sm" data-act="noticeWithdraw" data-id="${esc(n.id)}">Withdraw</button>` : ''}</td></tr>`), { empty: 'No notices sent yet.' })}</div>`
};
function noticeTarget(t) {
  if (!t || t === 'all') return 'Every firm';
  const [k, id] = t.split(':');
  if (k === 'customer') return 'Customer ' + ((S.data.cs || []).find(c => c.id === id)?.name || id);
  if (k === 'firm') return 'Firm ' + ((S.data.fs || []).find(f => f.id === id)?.latest?.firm || id);
  return 'Server ' + id;
}
const isoLocal = d => new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);

Object.assign(A, {
  ivEmail: () => act(() => post('/api/invoices/' + enc(S.arg) + '/email'), r => 'Emailed to ' + r.emailed_to),
  remind: () => modal(mhd('Send a payment reminder', 'By email to the billing contact, with the invoice attached.') + `<div class="bd"><label class="check"><input type="checkbox" id="rm-n"> Also show it to the firm’s Admins inside Ledgerline (for 14 days)</label>${S.me.mail ? '' : '<div class="note warn">No mail server is set up for Control, so only the notice can be sent.</div>'}</div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="remindGo">Send</button></div>`),
  remindGo: () => act(() => post('/api/invoices/' + enc(S.arg) + '/remind', { notice: $('rm-n').checked }), 'Reminder sent'),
  remindRow: id => act(() => post('/api/invoices/' + enc(id) + '/remind', { notice: !!(S.data.rv && S.data.rv.ops.notice_overdue) }), 'Reminder sent'),
  creditNote: () => { const iv = S.data, done = (iv.cns || []).reduce((a, c) => a + Number(c.taxable), 0), left = (Number(iv.taxable) - done).toFixed(2);
    modal(mhd('Credit note on ' + esc(iv.number), `Lowers the invoice; GST is credited at ${esc(iv.gst_pct)}% the same way as on the invoice. Up to ${inr(left)} before GST is left to credit.`) + `<div class="bd"><div class="fld"><label for="cn-a">Amount to credit, before GST ₹</label><input class="in mono" id="cn-a" value="${left}"></div><div class="fld"><label for="cn-r">Reason (printed on the credit note)</label><input class="in" id="cn-r" maxlength="300" placeholder="e.g. Service credit for the 12 Oct outage"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="cnGo">Issue credit note</button></div>`); },
  cnGo: () => act(() => post('/api/invoices/' + enc(S.arg) + '/credit', { amount: $('cn-a').value.trim(), reason: $('cn-r').value }), r => 'Credit note ' + r.number + ' issued'),
  tdsCert: id => { const [iv, i] = id.split('|'); modal(mhd('TDS certificate (Form 16A)', 'Record it once received; check it against Form 26AS.') + `<div class="bd"><div class="fld"><label for="tc-c">Certificate number</label><input class="in mono" id="tc-c"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="tdsCertGo" data-id="${esc(id)}">Save</button></div>`); },
  tdsCertGo: id => { const [iv, i] = id.split('|'); act(() => post('/api/invoices/' + enc(iv) + '/tds', { index: +i, cert: $('tc-c').value }), 'Saved'); },
  edChange: id => { const f = S.data.firms.find(x => x.id === id); modal(mhd('Change edition · ' + esc(f.latest.firm), 'Takes effect at the firm’s next report (within about 15 minutes). The highest edition used in a month is the one billed for that month.') + `<div class="bd"><div class="fld"><label for="ed-e">Edition</label><select class="in" id="ed-e">${['manual', 'autopilot', 'full'].map(e => `<option value="${e}" ${f.latest.edition === e ? 'selected' : ''}>${EDN[e]}</option>`).join('')}</select></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="edGo" data-id="${esc(id)}">Change</button></div>`); },
  edGo: id => act(() => post('/api/firms/' + enc(id) + '/edition', { edition: $('ed-e').value }), r => `Sent: ${EDN[r.edition]} from the next report`),
  noteAdd: id => act(() => post('/api/notes', { customer: id, kind: $('nt-k').value, text: $('nt-t').value, due: $('nt-due').value }), 'Note saved'),
  noteDone: id => act(() => post('/api/notes', { id, done: true }), 'Done'),
  cannedPick: (v, el) => { const c = (S.data.canned || []).find(x => x.id === v); if (c) { const t = $('tk-text'); t.value = (t.value ? t.value + '\n\n' : '') + c.text; t.focus(); } el.value = ''; },
  cannedEdit: id => { const c = id ? S.data.can.find(x => x.id === id) : { title: '', text: '' }; modal(mhd(id ? 'Edit saved reply' : 'Add a saved reply') + `<div class="bd"><div class="fld"><label for="cn-t">Title</label><input class="in" id="cn-t" value="${esc(c.title)}" maxlength="80"></div><div class="fld"><label for="cn-x">Reply</label><textarea class="in" id="cn-x" rows="8" maxlength="4000">${esc(c.text)}</textarea></div></div><div class="ft">${id ? `<button class="btn danger" data-act="cannedDel" data-id="${esc(id)}">Remove</button>` : ''}<span style="flex:1"></span><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="cannedSave" data-id="${esc(id || '')}">Save</button></div>`); },
  cannedSave: id => act(() => post('/api/canned', { id: id || '', title: $('cn-t').value, text: $('cn-x').value }), 'Saved'),
  cannedDel: id => act(() => post('/api/canned', { id, title: '', text: '' }), 'Removed'),
  sm: k => { S.ui.sm = shiftMonth(S.ui.sm || S.me.month, +k); render(); },
  alertAck: id => act(() => post('/api/alerts/ack', { id }), 'Set aside for 7 days'),
  noticeNew: () => {
    const now = new Date(), wk = new Date(now.getTime() + 7 * 864e5), d = S.data;
    modal(mhd('New notice', 'Shown as a banner on every screen in Ledgerline. People can hide it for themselves.') + `<div class="bd"><div class="fld"><label for="no-t">Title</label><input class="in" id="no-t" maxlength="120" placeholder="e.g. Planned maintenance: Sunday 2 Nov, 22:00–23:00"></div><div class="fld"><label for="no-x">Message</label><textarea class="in" id="no-x" rows="4" maxlength="2000"></textarea></div>
      <div class="grid g2"><div class="fld"><label for="no-to">Send to</label><select class="in" id="no-to"><option value="all">Every firm</option><optgroup label="Customer">${d.cs.map(c => `<option value="customer:${esc(c.id)}">${esc(c.name)}</option>`).join('')}</optgroup><optgroup label="One firm">${d.fs.map(f => `<option value="firm:${esc(f.id)}">${esc(f.latest.firm || f.tenant)}</option>`).join('')}</optgroup></select></div>
      <div class="fld"><label for="no-au">Who sees it</label><select class="in" id="no-au"><option value="all">Every staff login</option><option value="admins">Admins only</option></select></div>
      <div class="fld"><label for="no-l">Kind</label><select class="in" id="no-l"><option value="info">Information</option><option value="warn">Warning</option><option value="bad">Urgent</option></select></div><div></div>
      <div class="fld"><label for="no-f">Show from</label><input class="in" type="datetime-local" id="no-f" value="${isoLocal(now)}"></div><div class="fld"><label for="no-u">Until</label><input class="in" type="datetime-local" id="no-u" value="${isoLocal(wk)}"></div></div></div>
      <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="noticeGo">Send</button></div>`);
  },
  noticeGo: () => act(() => post('/api/notices', { title: $('no-t').value, text: $('no-x').value, target: $('no-to').value, audience: $('no-au').value, level: $('no-l').value, from: $('no-f').value, until: $('no-u').value }), r => `Queued for ${r.firms.length} firm${r.firms.length === 1 ? '' : 's'}`),
  noticeWithdraw: id => { if (window.confirm('Withdraw this notice from every firm?')) act(() => post('/api/notices/' + enc(id) + '/withdraw'), 'Withdrawn'); },
});

document.addEventListener('click', e => {
  const el = e.target.closest('[data-act]');
  if (!el) return;
  const fn = A[el.dataset.act];
  if (!fn) return;
  if (el.tagName === 'A') return;
  e.preventDefault();
  fn(el.dataset.id, el, e);
});
document.addEventListener('keydown', e => { if (e.key === 'Escape' && $('mroot').innerHTML) closeModal(); });

boot().then(() => { if (!S.me) showLogin(); });
