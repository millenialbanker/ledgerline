'use strict';
/* Ledgerline console: stock SIPs. Staff screen "stocksip" (register, the
   day's instalments, basket file to the OMS, fills, failures, top-ups,
   settings and scrip lists) and the client portal page "psip" (start,
   pause, skip or cancel a SIP, with the standing instruction). Loaded after
   app.js; the screens are opened from the menus that list them. */

Object.assign(UI, { ssipTab: 'today', ssipSt: 'live', ssipQ: '' });
const ssipPill = s => pill(s, { Active: 'good', Paused: 'warn', Cancelled: 'nt', Completed: 'acc', Pending: 'warn', Placed: 'acc', Filled: 'good', 'Partly filled': 'warn', Failed: 'bad', Lapsed: 'bad', Skipped: 'nt', 'Not sent': 'warn', 'Check fills': 'bad', Scheduled: 'acc', Success: 'good', 'Not raised': 'nt', 'Awaiting authorisation': 'warn' }[s] || 'nt');
const ssipStamp = t => { const d = new Date(t); return isNaN(d) ? esc(t || '') : d.toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'short' }); };
const ssipPlan = s => (s.basis === 'qty' ? `${nf(s.qty)} shares` : inr(s.amount)) + ' · ' + ({ daily: 'every trading day', weekly: 'every ' + ['', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri'][s.day], monthly: 'monthly on day ' + s.day }[s.frequency] || s.frequency);
const ssipCan = () => has('Ops maker', 'Ops checker');
const ssipDealer = () => has('Ops maker', 'Ops checker', 'Risk manager');

/* ---------- staff screen ---------- */
V.stocksip = {
  load: () => get(`/v1/stock-sips?date=${S.date}`),
  html: d => {
    const st = d.settings, c = d.counts || {};
    const active = d.sips.filter(s => s.status === 'Active').length;
    const off = st.enabled ? '' : `<div class="note warn"><b>Stock SIPs are switched off.</b> The compliance officer switches them on under Settings, after deciding the order type, the placement time and whether SIP orders are tagged as algo orders.</div>`;
    const router = d.router_ready ? `Orders go to the OMS by <b>${esc(d.router)}</b>.` : `<span class="neg">${esc(d.router_note || 'The OMS hand-off is not set up.')}</span>`;
    return ph('Stock SIPs', `Clients' standing instructions to buy a share or ETF for an amount or a number of shares, every day, week or month. Ledgerline works out each instalment at ${esc(st.place_at)} and hands the orders to the OMS; trades come back in the day's trade files. ${router}`,
      ssipCan() ? '<button class="btn pri" data-act="ssipNew">New SIP</button>' : '') + off +
      `<div class="kpis">${kpi('Active SIPs', nf(active), d.sips.length + ' registered')}${kpi('Due ' + fmtDate(d.date), nf(d.due.length + (d.orders || []).length), d.due.length ? d.due.length + ' not worked out yet' : 'worked out', d.due.length ? 'warn' : '')}${kpi('Waiting for the OMS', nf(c.Pending || 0), 'make the basket file', c.Pending ? 'warn' : '')}${kpi('Placed', nf((c.Placed || 0) + (c.Filled || 0) + (c['Partly filled'] || 0)), nf(c.Filled || 0) + ' filled')}${kpi('Failed today', nf((c.Failed || 0) + (c.Lapsed || 0)), d.failures.length + ' in 45 days', (c.Failed || c.Lapsed) ? 'bad' : '')}</div>` +
      `<div class="panel"><div class="hd">${segBtns('ssipTab', [['today', 'Today'], ['sips', 'SIPs'], ['fail', 'Failures'], ['report', 'Returns'], ['topups', 'Top-ups'], ['lists', 'Scrip lists'], ['settings', 'Settings']])}</div></div>` +
      ({ today: ssipToday, sips: ssipList, fail: ssipFailures, report: ssipReport, topups: ssipTopups, lists: ssipLists, settings: ssipSettings }[UI.ssipTab] || ssipToday)(d);
  }
};

function ssipOrderRows(xs) {
  return xs.map(x => `<tr><td class="mono small">${esc(x.ref)}</td><td><b>${esc(x.ucc)}</b><div class="dim small">${esc(x.name)}</div></td><td>${esc(x.symbol)} <span class="dim small">${esc(x.exchange)}</span></td>
    <td class="r">${nf(x.qty)}</td><td class="r">${num(x.price) ? nf(x.price, 2) : '—'}${x.order_type === 'LIMIT' && num(x.limit) ? `<div class="dim small">limit ${nf(x.limit, 2)}</div>` : ''}</td><td class="r">${num(x.estimate) ? inr(x.estimate) : '—'}</td>
    <td>${ssipPill(x.status)}${x.filled_qty ? `<div class="dim small">${nf(x.filled_qty)} at ${nf(x.avg_price, 2)}</div>` : ''}</td><td class="wrap small">${esc(x.reason || x.note || '')}${x.oms_order ? `<div class="dim">OMS ${esc(x.oms_order)}</div>` : ''}</td>
    <td>${ssipCan() && ['Placed', 'Partly filled', 'Check fills', 'Lapsed'].includes(x.status) ? `<button class="btn sm" data-act="ssipFill" data-id="${esc(x.id)}">Record fill</button>` : ''}</td></tr>`);
}

function ssipToday(d) {
  const run = d.run || {}, pend = (d.orders || []).filter(x => x.status === 'Pending').length;
  return `<div class="panel"><div class="hd"><h3>Instalments for ${fmtDate(d.date)}</h3><small>${run.ran_at ? 'worked out ' + ssipStamp(run.ran_at) + (run.result ? ' · ' + esc(run.result) : '') : 'the scheduler works them out at ' + esc(d.settings.place_at)}</small></div>
    <div class="bd"><div class="tools">${ssipDealer() ? `<button class="btn" data-act="ssipRun">Work out instalments now</button><button class="btn ${pend ? 'pri' : ''}" data-act="ssipSend" ${pend ? '' : 'disabled'}>${d.settings.router === 'basket' ? 'Make basket file' : 'Send to the OMS'} (${pend})</button><button class="btn" data-act="ssipMatch">Match trades</button><button class="btn" data-act="ssipMatch" data-id="final">Match and lapse unexecuted</button>` : ''}
      <button class="btn" data-act="dl" data-id="/v1/stock-sips/report.csv?date=${S.date}" data-name="stock_sips_${S.date}.csv">Download report</button></div>
    ${d.due.length ? `<div class="note warn" style="margin-top:10px">${d.due.length} SIP${d.due.length > 1 ? 's have' : ' has'} an instalment on this day not yet worked out: ${d.due.slice(0, 8).map(s => esc(s.ucc + ' ' + s.symbol)).join(', ')}${d.due.length > 8 ? '…' : ''}.</div>` : ''}</div>
    ${tbl([['Order'], ['Client'], ['Scrip'], ['Qty', 'r'], ['Price', 'r'], ['Needs', 'r'], ['Status'], ['Reason / note'], ['']], ssipOrderRows(d.orders || []), { empty: 'No instalments worked out for this day.' })}</div>
    <div class="panel"><div class="hd"><h3>Sent to the OMS</h3><small>basket files and API runs</small></div>${tbl([['Batch'], ['Date'], ['How'], ['Orders', 'r'], ['Refused', 'r'], ['Status'], ['']], (d.batches || []).map(b => `<tr><td class="mono">${esc(b.id)}</td><td class="n">${fmtDate(b.date)}</td><td>${esc(b.router)}${b.file_name ? `<div class="dim small mono">${esc(b.file_name)}</div>` : ''}</td><td class="r">${b.placed}</td><td class="r">${b.refused || ''}</td><td>${pill(b.status, b.status === 'Sent' ? 'good' : 'nt')}</td>
      <td>${b.file_name ? `<button class="btn sm" data-act="dl" data-id="/v1/stock-sips/batches/${encodeURIComponent(b.id)}/file" data-name="${esc(b.file_name)}">Download</button>${b.status === 'Sent' && b.date === S.date && ssipDealer() ? `<button class="btn sm" data-act="ssipWithdraw" data-id="${esc(b.id)}">Not uploaded</button>` : ''}` : ''}</td></tr>`), { empty: 'Nothing sent yet.' })}</div>`;
}

function ssipList(d) {
  return `<div class="panel"><div class="hd"><div class="tools">${segBtns('ssipSt', [['live', 'Active and paused'], ['all', 'All']], 'ssip-t')}</div><input class="in" data-ui="ssipQ" data-part="ssip-t" placeholder="Filter by client, scrip or SIP" value="${esc(UI.ssipQ)}"></div><div id="ssip-t">${P['ssip-t'](d)}</div></div>`;
}
P['ssip-t'] = d => {
  const q = (UI.ssipQ || '').trim().toLowerCase();
  const rows = d.sips.filter(s => UI.ssipSt === 'all' || ['Active', 'Paused'].includes(s.status)).filter(s => !q || (s.ucc + ' ' + s.name + ' ' + s.symbol + ' ' + s.id).toLowerCase().includes(q));
  return tbl([['SIP'], ['Client'], ['Scrip'], ['Plan'], ['Next'], ['Status'], ['Bought', 'r'], ['Failed in a row', 'r'], ['Consent'], ['']], rows.map(s => `<tr><td class="mono">${esc(s.id)}</td><td><b>${esc(s.ucc)}</b><div class="dim small">${esc(s.name)}</div></td><td>${esc(s.symbol)} <span class="dim small">${esc(s.exchange)}</span>${num(s.price) ? `<div class="dim small">${nf(s.price, 2)}</div>` : ''}</td>
    <td class="small">${ssipPlan(s)}<div class="dim">from ${fmtDate(s.start)}${s.end ? ' to ' + fmtDate(s.end) : ''}</div></td><td class="n">${s.next_trade ? fmtDate(s.next_trade) : '—'}</td><td>${ssipPill(s.status)}${s.paused_until ? `<div class="dim small">until ${fmtDate(s.paused_until)}</div>` : ''}${s.stop_reason ? `<div class="dim small wrap">${esc(s.stop_reason)}</div>` : ''}</td>
    <td class="r">${s.done}</td><td class="r ${s.failures ? 'neg' : ''}">${s.failures}</td><td class="small">v${s.consent.version} · ${esc(s.consent.source)}${s.consent.ref ? ' ' + esc(s.consent.ref) : ''}<div class="dim">${ssipStamp(s.consent.at)}</div></td>
    <td class="nowrap">${ssipCan() ? ssipActs(s) : ''}</td></tr>`), { tall: true, empty: 'No stock SIPs match.' });
};

const ssipActs = s => ['Active', 'Paused'].includes(s.status) ? (s.status === 'Active' ? `<button class="btn sm" data-act="ssipDo" data-id="${esc(s.id)}" data-v="pause">Pause</button>` : `<button class="btn sm" data-act="ssipDo" data-id="${esc(s.id)}" data-v="resume">Resume</button>`) +
  (s.next ? `<button class="btn sm" data-act="ssipDo" data-id="${esc(s.id)}" data-v="skip" title="Skip the instalment of ${esc(s.next)}">Skip next</button>` : '') + `<button class="btn sm bad" data-act="ssipDo" data-id="${esc(s.id)}" data-v="cancel">Cancel</button>` : '';

function ssipFailures(d) {
  return `<div class="panel"><div class="hd"><h3>Instalments not bought</h3><small>last 45 days · failures in a row stop a SIP after ${d.settings.max_failures}</small></div>${tbl([['Order'], ['Client'], ['Scrip'], ['Qty', 'r'], ['Price', 'r'], ['Needs', 'r'], ['Status'], ['Reason / note'], ['']], ssipOrderRows(d.failures || []).map((r, i) => r.replace('<td class="mono small">', `<td class="mono small"><span class="dim">${fmtDate(d.failures[i].date)}</span><br>`)), { tall: true, empty: 'No failed instalments.' })}</div>`;
}

function ssipReportRows(rows) {
  return rows.map(r => `<tr><td class="mono">${esc(r.sip)}</td><td><b>${esc(r.ucc)}</b><div class="dim small">${esc(r.name)}</div></td><td>${esc(r.symbol)}</td><td class="r">${r.bought}/${r.due}</td><td class="r">${nf(r.units)}</td><td class="r">${inr(r.invested, 2)}</td><td class="r">${num(r.avg_cost) ? nf(r.avg_cost, 2) : '—'}</td>
    <td class="r">${inr(r.value, 2)}</td><td class="r ${sgn(r.gain)}">${inr(r.gain, 2)}</td><td class="r ${r.xirr == null ? '' : sgn(r.xirr)}">${r.xirr == null ? '—' : nf(r.xirr, 2) + '%'}</td></tr>`);
}

function ssipReport(d) {
  return `<div class="panel"><div class="hd"><h3>Returns</h3><small>at the prices bought, before charges; XIRR from each purchase and today's value</small><button class="btn sm" data-act="dl" data-id="/v1/stock-sips/report.csv?date=${S.date}" data-name="stock_sips_${S.date}.csv">Download</button></div>
    ${tbl([['SIP'], ['Client'], ['Scrip'], ['Bought', 'r'], ['Units', 'r'], ['Invested', 'r'], ['Avg cost', 'r'], ['Value', 'r'], ['Gain', 'r'], ['XIRR', 'r']], ssipReportRows(d.report || []), { tall: true, empty: 'Nothing bought yet.' })}</div>`;
}

function ssipTopups(d) {
  const st = d.settings;
  return (st.topups ? '' : `<div class="note">Top-ups are off. With them on, a client's shortfall for the next instalment is debited the day before from their own bank account by UPI AutoPay or e-mandate (at most ₹15,000, with a day's notice) and credited to their ledger. Set up <b>Stock SIP top-ups</b> on <a href="#connections" data-act="go" data-id="connections">Connections</a>, then switch them on under Settings.</div>`) +
    `<div class="grid2"><div class="panel"><div class="hd"><h3>Mandates</h3>${st.topups && ssipCan() ? '<button class="btn sm" data-act="ssipMandate">New mandate</button>' : ''}</div>${tbl([['Client'], ['Subscription'], ['Status'], ['Up to', 'r']], (d.mandates || []).map(m => `<tr><td><b>${esc(m.ucc)}</b><div class="dim small">bank ••${esc(m.bank_last4)}</div></td><td class="mono small">${esc(m.subscription_id)}<div class="dim">${esc((m.methods || []).join(', '))}${m.live ? '' : ' · sandbox'}</div></td><td>${ssipPill(m.status)}</td><td class="r">${inr(m.max_amount)}</td></tr>`), { empty: 'No mandates.' })}</div>
    <div class="panel"><div class="hd"><h3>Raise debits</h3></div><div class="bd"><p class="dim small" style="margin-top:0">The scheduler raises them at ${esc(st.topup_at)} two days before each instalment day (the debit is the next day). To raise them now for an instalment day:</p>
    <div class="tools"><input class="in mono" type="date" id="ssip-td"><button class="btn" data-act="ssipRaise" ${st.topups && ssipCan() ? '' : 'disabled'}>Raise top-ups</button></div></div></div></div>
    <div class="panel"><div class="hd"><h3>Top-up debits</h3></div>${tbl([['Instalment day'], ['Client'], ['Debit on'], ['Needs', 'r'], ['Free', 'r'], ['Debit', 'r'], ['Status'], ['Reason / receipt']], (d.topups || []).map(t => `<tr><td class="n">${fmtDate(t.trade_date)}</td><td><b>${esc(t.ucc)}</b><div class="dim small">${esc((t.scrips || []).join(', '))}</div></td><td class="n">${fmtDate(t.debit_date)}</td><td class="r">${inr(t.need, 2)}</td><td class="r">${inr(t.free, 2)}</td><td class="r">${inr(t.amount, 2)}</td><td>${ssipPill(t.status)}</td><td class="wrap small">${esc(t.reason || '')}${t.receipt ? `<div class="mono">${esc(t.receipt)}</div>` : ''}</td></tr>`), { empty: 'No top-up debits.' })}</div>`;
}

function ssipLists(d) {
  const L = d.lists || {}, canUp = has('Compliance officer', 'Risk manager');
  const names = { eligible: 'Scrips allowed', restricted: 'Restricted scrips (ASM, GSM and others)', tokens: 'OMS instrument IDs' };
  return `<div class="note">The firm's rule: ${d.settings.eligibility === 'list' ? '<b>only scrips on the allowed list</b>' : '<b>any equity share or ETF</b>'}. Restricted scrips, trade-for-trade series (NSE BE/BZ, BSE T/TS/XT/Z) and suspended scrips are never bought. Upload the exchanges' ASM and GSM lists as the restricted list whenever they change.</div>` +
    `<div class="panel">${tbl([['List'], ['What it holds'], ['Scrips', 'r'], ['Uploaded'], ['']], ['eligible', 'restricted', 'tokens'].map(k => { const l = L[k] || {}; return `<tr><td><b>${names[k]}</b></td><td class="wrap small dim">${esc(l.about)}</td><td class="r">${nf(l.count)}</td><td class="small">${l.uploaded_at ? ssipStamp(l.uploaded_at) + '<div class="dim">' + esc(l.uploaded_by) + (l.file ? ' · ' + esc(l.file) : '') + '</div>' : '—'}</td>
      <td>${canUp ? `<input type="file" id="ssip-file-${k}" accept=".csv,text/csv" hidden><button class="btn sm" data-act="ssipUpload" data-id="${k}">Upload CSV</button>` : ''}</td></tr>`; }))}</div>`;
}

function ssipSettings(d) {
  const s = d.settings, can = has('Compliance officer');
  const f = (id, label, val, extra = '') => `<div class="fld"><label for="${id}">${label}</label><input class="in" id="${id}" value="${esc(val ?? '')}" ${extra} ${can ? '' : 'disabled'}></div>`;
  const sel = (id, label, val, opts) => `<div class="fld"><label for="${id}">${label}</label><select class="in" id="${id}" ${can ? '' : 'disabled'}>${opts.map(([v, l]) => `<option value="${esc(v)}" ${String(val) === String(v) ? 'selected' : ''}>${esc(l)}</option>`).join('')}</select></div>`;
  return `<div class="panel"><div class="hd"><h3>Rules for stock SIPs</h3><small>${s.decided_by ? 'order type, time and algo tagging decided by ' + esc(s.decided_by) + ' ' + ssipStamp(s.decided_at) : 'decided by the compliance officer'}</small></div><div class="bd">
    <div class="note">SEBI's retail algo framework (all brokers from 1 April 2026) treats any order generated by automated execution logic as an algo order, and NSE does not accept market orders from algos. Whether scheduled SIP orders are algo orders is the firm's decision: record it here.</div>
    <div class="grid g2">${sel('ss-on', 'Stock SIPs', s.enabled ? '1' : '', [['1', 'On'], ['', 'Off']])}${f('ss-at', 'Placement time (IST)', s.place_at, 'placeholder="11:00"')}
    ${sel('ss-type', 'Order type', s.order_type, [['LIMIT', 'Limit, at the last price plus the protection'], ['MARKET', 'Market, with market protection']])}${f('ss-prot', 'Protection / cushion (%)', s.protection_pct)}
    ${f('ss-tick', 'Tick size for limit prices (₹)', s.tick_size)}${sel('ss-algo', 'Tag SIP orders as algo orders', s.algo_tag ? '1' : '', [['', 'No'], ['1', 'Yes']])}
    ${f('ss-algoid', 'Algo ID approved by the exchange', s.algo_id)}${sel('ss-router', 'Orders go to the OMS by', s.router, [['basket', 'Basket file (the dealer uploads it)'], ['xts', 'Symphony XTS order API'], ['other', 'Another OMS API (not built: basket file meanwhile)']])}
    ${sel('ss-fmt', 'Basket file layout', s.basket_format, (d.basket_formats || [s.basket_format]).map(x => [x, x]))}${sel('ss-elig', 'Scrips allowed', s.eligibility, [['list', 'Only the firm’s list (e.g. top 250 and ETFs)'], ['any', 'Any equity share or ETF']])}
    ${f('ss-maxf', 'Stop a SIP after this many failed instalments in a row', s.max_failures, 'inputmode="numeric"')}${f('ss-min', 'Minimum order (₹)', s.min_amount)}
    ${sel('ss-chan', 'Brokerage plan channel for SIP orders', s.brokerage_channel || '', [['', 'The client’s usual plan'], ...(d.channels || []).map(c => [c, c])])}${f('ss-brok', 'Brokerage per instalment when no plan line applies (₹)', s.brokerage_per_instalment)}
    ${sel('ss-top', 'Top-ups by UPI AutoPay / e-mandate', s.topups ? '1' : '', [['', 'Off'], ['1', 'On']])}${f('ss-cap', 'Top-up limit per debit (₹, at most 15,000)', s.topup_cap)}${f('ss-topat', 'Raise top-up debits at (IST)', s.topup_at)}</div>
    <div class="fld"><label for="ss-consent">Standing instruction the client agrees to (version ${s.consent_version}; a change makes a new version)</label><textarea class="in" id="ss-consent" rows="7" ${can ? '' : 'disabled'}>${esc(s.consent_text)}</textarea><span class="dim small">Placeholders: {firm} {scrip} {ucc} {basis} {frequency} {start} {end} {place_at} {order_type} {brokerage} {max_failures}</span></div>
    ${can ? '<div class="ft" style="padding:0;border:0"><button class="btn pri" data-act="ssipSave">Save rules</button></div>' : '<div class="dim small">Only the compliance officer changes these.</div>'}</div></div>`;
}

/* ---------- client portal ---------- */
V.psip = {
  load: async () => { const v = await portalLoad(); v.sip = await get(`/v1/portal/stock-sips?date=${S.date}&ucc=${encodeURIComponent(v.account.ucc)}`); return v; },
  html: v => {
    const p = v.sip;
    if (!p.enabled) return pHead(v, 'Stock SIPs', 'Buy a share or ETF regularly.') + `<div class="note">${esc(v.firm.name)} does not offer stock SIPs at present.</div>`;
    const live = p.sips.filter(s => ['Active', 'Paused'].includes(s.status));
    return pHead(v, 'Stock SIPs', `Buy a share or ETF for a fixed amount or number of shares every day, week or month. Orders are placed at about ${esc(p.place_at)} on each instalment day from the funds in your trading account; an instalment is not placed if your funds are not enough. This is not investment advice.`) +
      `<div class="grid2"><div class="panel"><div class="hd"><h3>Start a SIP</h3></div><div class="bd">${ssipStartForm(p)}</div></div>
      <div class="panel"><div class="hd"><h3>Funding</h3></div><div class="bd"><p style="margin-top:0">Keep enough in your trading account before ${esc(p.place_at)} on each instalment day: the order and its charges are checked against your free funds.</p>
      ${p.topups_on ? (p.mandate ? `<div class="kv"><div><small>Top-up mandate</small><b>${ssipPill(p.mandate.status)}</b></div><div><small>Up to</small><b class="mono">${inr(p.mandate.max_amount)}</b></div><div><small>Bank</small><b class="mono">••${esc(p.mandate.bank_last4)}</b></div></div>` : '') +
        (!p.mandate || !['Active', 'Awaiting authorisation'].includes(p.mandate.status) ? `<p class="small">You can let us top up a shortfall the day before an instalment from your registered bank account by UPI AutoPay or e-mandate: at most ${inr(p.topup_cap)} a debit, each with a notice at least 24 hours before.</p><button class="btn" data-act="ssipPMandate">Set up a top-up mandate</button>` : '') : ''}</div></div></div>
      <div class="panel"><div class="hd"><h3>Your SIPs</h3></div>${tbl([['Scrip'], ['Plan'], ['Next instalment'], ['Status'], ['Bought', 'r'], ['']], p.sips.map(s => `<tr><td><b>${esc(s.symbol)}</b> <span class="dim small">${esc(s.exchange)}</span></td><td class="small">${ssipPlan(s)}</td><td class="n">${s.next_trade ? fmtDate(s.next_trade) : '—'}</td><td>${ssipPill(s.status)}${s.stop_reason ? `<div class="dim small wrap">${esc(s.stop_reason)}</div>` : ''}</td><td class="r">${s.done}</td><td class="nowrap">${ssipActs(s)}</td></tr>`), { empty: 'You have no stock SIPs.' })}${live.length ? '<div class="pad dim small">Pause, skip or cancel before the placement time on the instalment day.</div>' : ''}</div>
      <div class="panel"><div class="hd"><h3>Returns</h3></div>${tbl([['SIP'], ['Client'], ['Scrip'], ['Bought', 'r'], ['Units', 'r'], ['Invested', 'r'], ['Avg cost', 'r'], ['Value', 'r'], ['Gain', 'r'], ['XIRR', 'r']], ssipReportRows(p.report || []), { empty: 'Nothing bought yet.' })}</div>
      <div class="panel"><div class="hd"><h3>Instalments</h3></div>${tbl([['Date'], ['Scrip'], ['Qty', 'r'], ['Price', 'r'], ['Status'], ['Details']], p.orders.map(x => `<tr><td class="n">${fmtDate(x.date)}</td><td>${esc(x.symbol)}</td><td class="r">${nf(x.filled_qty || x.qty)}</td><td class="r">${num(x.avg_price) ? nf(x.avg_price, 2) : num(x.price) ? nf(x.price, 2) : '—'}</td><td>${ssipPill(x.status)}</td><td class="wrap small">${esc(x.reason || '')}</td></tr>`), { empty: 'No instalments yet.' })}</div>`;
  }
};

function ssipStartForm(p) {
  const el = p.eligible || [];
  return `<form id="ssip-pf" onsubmit="return false"><div class="grid g2">
    <div class="fld"><label for="ssip-sym">Scrip</label>${el.length ? `<select class="in" id="ssip-sym">${el.map(x => `<option value="${esc(x.symbol || '')}" data-isin="${esc(x.isin || '')}">${esc(x.symbol || x.isin)}${x.name ? ' · ' + esc(x.name) : ''}</option>`).join('')}</select>` : '<input class="in" id="ssip-sym" placeholder="NSE symbol, e.g. NIFTYBEES" autocapitalize="characters">'}</div>
    <div class="fld"><label for="ssip-basis">Buy</label><select class="in" id="ssip-basis"><option value="amount">For an amount</option><option value="qty">A number of shares</option></select></div>
    <div class="fld"><label for="ssip-amt">Amount (₹) or number of shares</label><input class="in mono" id="ssip-amt" inputmode="decimal" placeholder="e.g. 5000"></div>
    <div class="fld"><label for="ssip-freq">How often</label><select class="in" id="ssip-freq"><option value="monthly">Monthly</option><option value="weekly">Weekly</option><option value="daily">Every trading day</option></select></div>
    <div class="fld"><label for="ssip-day">Day (1–31 for monthly; 1 Mon … 5 Fri for weekly)</label><input class="in mono" id="ssip-day" inputmode="numeric" value="5"></div>
    <div class="fld"><label for="ssip-start">Start</label><input class="in mono" type="date" id="ssip-start" value="${esc(S.date)}"></div>
    <div class="fld"><label for="ssip-end">End (optional)</label><input class="in mono" type="date" id="ssip-end"></div></div>
    <div id="ssip-err"></div><button class="btn pri" data-act="ssipPreview">Review</button> <span class="dim small">Minimum order ${inr(p.min_amount)}. You will see the standing instruction before you agree.</span></form>`;
}

function ssipReq(ucc) {
  const basis = $('ssip-basis').value, n = $('ssip-amt').value.replace(/,/g, '').trim(), symEl = $('ssip-sym');
  const isin = symEl.tagName === 'SELECT' ? symEl.selectedOptions[0]?.dataset.isin || '' : '';
  const r = { ucc, symbol: symEl.value.trim().toUpperCase(), isin, basis, frequency: $('ssip-freq').value, day: parseInt($('ssip-day').value || '0', 10) || 0, start: $('ssip-start').value, end: $('ssip-end').value };
  if (basis === 'qty') r.qty = parseInt(n || '0', 10) || 0; else r.amount = n || '0';
  if (r.frequency === 'daily') r.day = 0;
  return r;
}

function ssipConsentModal(pv, req, staff) {
  modal(mhd('Standing instruction', `${esc(pv.sip.symbol)} · ${esc(ssipPlan(pv.sip))} · first instalment ${fmtDate(pv.next_trade || pv.next)}`) + `<div class="bd">
    <div class="kv"><div><small>Last price</small><b class="mono">${nf(pv.price, 2)}</b></div><div><small>Shares at this price</small><b class="mono">${nf(pv.qty_now)}</b></div><div><small>Funds needed with charges</small><b class="mono">${inr(pv.estimate)}</b></div></div>
    <div class="note" style="white-space:pre-wrap">${esc(pv.consent_text)}</div>
    ${staff ? `<div class="grid g2"><div class="fld"><label for="ssip-src">How the client gave it</label><select class="in" id="ssip-src">${(C.stocksip?.consent_sources || ['Signed form', 'Recorded call']).map(x => `<option>${esc(x)}</option>`).join('')}</select></div><div class="fld"><label for="ssip-ref">Form number or call recording reference</label><input class="in" id="ssip-ref"></div></div>`
      : '<label class="chk"><input type="checkbox" id="ssip-agree"> I have read this standing instruction and agree to it.</label>'}<div id="ssip-cerr"></div></div>
    <div class="ft"><button class="btn" data-act="close">Back</button><button class="btn pri" id="ssip-ok">Start the SIP</button></div>`);
  $('ssip-ok').onclick = async () => {
    const b = { ...req, consent_version: pv.consent_version };
    if (staff) { b.consent_source = $('ssip-src').value; b.consent_ref = $('ssip-ref').value; } else b.consent = $('ssip-agree').checked;
    try { const s = await post(staff ? '/v1/stock-sips' : '/v1/portal/stock-sips', b); closeModal(); toast(`SIP ${s.id} started in ${s.symbol}`); render({ keep: true }); }
    catch (e) { $('ssip-cerr').innerHTML = errHTML(e); }
  };
}

Object.assign(A, {
  ssipNew: () => {
    modal(mhd('New stock SIP', 'For a client who gave the standing instruction on a signed form or a recorded call.') + `<div class="bd"><div class="fld"><label for="ssip-ucc">Client code</label><input class="in mono" id="ssip-ucc" autocapitalize="characters"></div>${ssipStartForm({ min_amount: C.stocksip?.settings?.min_amount || 0, eligible: [] })}</div>`);
  },
  ssipPreview: async () => {
    const staff = S.view === 'stocksip', ucc = staff ? $('ssip-ucc').value.trim().toUpperCase() : UI.pAcct, req = ssipReq(ucc);
    try { const pv = await post(staff ? '/v1/stock-sips/preview' : '/v1/portal/stock-sips/preview', { ...req, date: S.date }); ssipConsentModal(pv, req, staff); }
    catch (e) { const el = $('ssip-err'); if (el) el.innerHTML = errHTML(e); else toast(e.message, true); }
  },
  ssipDo: (id, el) => {
    const v = el.dataset.v, staff = S.view === 'stocksip', path = (staff ? '/v1/stock-sips/sips/' : '/v1/portal/stock-sips/') + encodeURIComponent(id) + '/action';
    const msgs = { pause: 'SIP paused', resume: 'SIP resumed', skip: 'Next instalment skipped', cancel: 'SIP cancelled' };
    if (v === 'pause') {
      confirmModal('Pause this SIP', 'No instalments are placed while it is paused.', '<div class="fld"><label for="ssip-until">Until (leave empty to pause until resumed)</label><input class="in mono" type="date" id="ssip-until"></div>', 'Pause', () => act(() => post(path, { action: 'pause', until: $('ssip-until').value }), msgs.pause));
      return;
    }
    if (v === 'cancel') {
      confirmModal('Cancel this SIP', 'This revokes the standing instruction. A new SIP needs a new one.', '<div class="fld"><label for="ssip-why">Reason (optional)</label><input class="in" id="ssip-why"></div>', 'Cancel the SIP', () => act(() => post(path, { action: 'cancel', reason: $('ssip-why').value }), msgs.cancel), true);
      return;
    }
    act(() => post(path, { action: v }), msgs[v]);
  },
  ssipRun: () => act(() => post('/v1/stock-sips/run', { date: S.date }), r => r.message),
  ssipSend: async () => {
    const r = await act(() => post('/v1/stock-sips/send', { date: S.date }), r => r.message).catch(() => null);
    if (r && r.file_name) download(`/v1/stock-sips/batches/${encodeURIComponent(r.batch)}/file`, r.file_name);
  },
  ssipMatch: id => act(() => post('/v1/stock-sips/match', { date: S.date, final: id === 'final' }), r => `Trades from ${r.source}: ${r.changed.length} orders updated`),
  ssipWithdraw: id => confirmModal('Basket file not uploaded', 'Its orders without trades go back to waiting, so a new file can be made.', '<div class="fld"><label for="ssip-wr">What happened</label><input class="in" id="ssip-wr"></div>', 'Take back the file', () => act(() => post(`/v1/stock-sips/batches/${encodeURIComponent(id)}/withdraw`, { reason: $('ssip-wr').value }), 'Basket withdrawn')),
  ssipFill: id => {
    const x = [...(C.stocksip.orders || []), ...(C.stocksip.failures || [])].find(o => o.id === id) || {};
    confirmModal('Record the fill', `${esc(x.ref)} · ${esc(x.ucc)} · ${nf(x.qty)} ${esc(x.symbol)}`, `<div class="grid g2"><div class="fld"><label for="ssip-fq">Shares bought (0: not executed)</label><input class="in mono" id="ssip-fq" inputmode="numeric" value="${x.filled_qty || ''}"></div><div class="fld"><label for="ssip-fp">Average price</label><input class="in mono" id="ssip-fp" inputmode="decimal" value="${num(x.avg_price) ? x.avg_price : ''}"></div></div><div class="fld"><label for="ssip-fn">What you checked</label><input class="in" id="ssip-fn" placeholder="trade numbers, OMS order book"></div>`, 'Record',
      () => act(() => post(`/v1/stock-sips/orders/${encodeURIComponent(id)}/fill`, { qty: parseInt($('ssip-fq').value || '0', 10) || 0, price: $('ssip-fp').value || '0', note: $('ssip-fn').value }), 'Fill recorded'));
  },
  ssipRaise: () => act(() => post('/v1/stock-sips/topups', { trade_date: $('ssip-td').value }), r => r.message),
  ssipMandate: () => confirmModal('New top-up mandate', 'Creates the mandate on the client’s registered bank account; the client authorises it at the address shown.', '<div class="fld"><label for="ssip-mu">Client code</label><input class="in mono" id="ssip-mu"></div>', 'Create', async () => {
    const r = await post(`/v1/stock-sips/mandates/${encodeURIComponent($('ssip-mu').value.trim().toUpperCase())}`);
    modal(mhd('Mandate created', 'Send the client this address to authorise it with UPI AutoPay or e-mandate.') + `<div class="bd"><div class="secret mono" style="overflow-wrap:anywhere">${esc(r.auth_url)}</div></div><div class="ft"><button class="btn pri" data-act="close">Done</button></div>`);
  }),
  ssipPMandate: async () => {
    try { const r = await post('/v1/portal/stock-sips/mandate', { ucc: UI.pAcct }); window.open(r.auth_url, '_blank', 'noopener'); toast('Authorise the mandate in the page that opened'); render({ keep: true }); }
    catch (e) { toast(e.message, true); }
  },
  ssipUpload: id => {
    const f = $('ssip-file-' + id); if (!f) return;
    f.onchange = async () => { if (!f.files.length) return; try { const r = await api('POST', `/v1/stock-sips/lists/${id}?name=${encodeURIComponent(f.files[0].name)}`, await f.files[0].arrayBuffer(), true); toast(`${r.count} scrips loaded`); render({ keep: true }); } catch (e) { toast(e.message, true); } };
    f.click();
  },
  ssipSave: () => {
    const s = { ...C.stocksip.settings };
    Object.assign(s, { enabled: $('ss-on').value === '1', place_at: $('ss-at').value.trim(), order_type: $('ss-type').value, protection_pct: $('ss-prot').value.trim(), tick_size: $('ss-tick').value.trim(),
      algo_tag: $('ss-algo').value === '1', algo_id: $('ss-algoid').value.trim(), router: $('ss-router').value, basket_format: $('ss-fmt').value, eligibility: $('ss-elig').value,
      max_failures: parseInt($('ss-maxf').value || '0', 10) || 0, min_amount: $('ss-min').value.trim() || '0', brokerage_channel: $('ss-chan').value, brokerage_per_instalment: $('ss-brok').value.trim() || '0',
      topups: $('ss-top').value === '1', topup_cap: $('ss-cap').value.trim() || '0', topup_at: $('ss-topat').value.trim(), consent_text: $('ss-consent').value });
    act(() => api('PUT', '/v1/stock-sips/settings', s), 'SIP rules saved');
  },
});
