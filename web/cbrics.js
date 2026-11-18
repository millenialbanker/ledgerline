'use strict';
/* Ledgerline console: corporate bond deals through NSE CBRICS. Staff screen
   "cbrics" (Investments → Corporate bonds (CBRICS)): clients' OTC deals
   entered by a maker and reported by a checker, the sales reported to a
   client as buyer, settlements with their accounts, and the CBRICS
   participants the firm keeps. Loaded after app.js and uses its helpers. */

Object.assign(UI, { cbTab: 'deals', cbF: 'open' });
const CB_ST = { Draft: 'warn', Returned: 'warn', Refused: 'bad', Reported: 'acc', 'Instruction pending': 'acc', 'Awaiting confirmation': 'warn', Confirmed: 'good', 'Rejected by buyer': 'bad', Cancelled: 'nt', Settling: 'acc', Settled: 'good', 'Settlement failed': 'bad',
  Approved: 'good', Sent: 'acc', Rejected: 'bad', Deleted: 'nt', Active: 'good', Inactive: 'nt', Suspended: 'bad' };
const cbPill = s => pill(s || '—', CB_ST[s] || 'nt');
const cbEnc = encodeURIComponent;
const cbMk = () => has('Ops maker', 'Ops checker');
const cbCk = () => has('Ops checker');
const cbWhen = s => { if (!s) return '—'; const t = new Date(s); return isNaN(t) ? esc(s) : esc(t.toLocaleString('en-GB', { timeZone: 'Asia/Kolkata', day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit', hour12: false }).replace(',', '')); };
const cbDeal = id => (C.cbrics?.deals || []).find(x => x.id === id);
const cbParty = code => (C.cbrics?.parties || []).find(x => x.code === code);
const CB_YT = { M: 'YTM', C: 'YTC', P: 'YTP' };
const CB_OPEN = ['Draft', 'Returned', 'Refused', 'Reported', 'Instruction pending', 'Awaiting confirmation', 'Confirmed', 'Settling', 'Settlement failed'];

V.cbrics = {
  load: () => get('/v1/cbrics'),
  html: d => {
    const c = d.counts || {}, st = d.state || {}, tab = UI.cbTab;
    const waiting = d.deals.filter(x => x.pending).length + d.parties.filter(p => p.pending).length;
    const acts = `${d.set ? '<button class="btn" data-act="cbSync">Read CBRICS now</button>' : ''}${d.set && cbMk() ? '<button class="btn pri" data-act="cbNew">New deal</button>' : ''}`;
    const off = d.set ? (d.problem ? `<div class="note warn">${esc(d.problem)}</div>` : '') : `<div class="note warn"><b>NSE CBRICS is not connected.</b> The Admin sets it up in <a href="#connections" data-act="go" data-id="connections">Connections</a> (NSE CBRICS (corporate bonds)): the participant code and the API user NSE enabled.</div>`;
    const tabs = [['deals', 'Deals'], ['settle', 'Settlement'], ['parties', 'Participants'], ['conn', 'CBRICS link']];
    return ph('Corporate bonds (CBRICS)', `Clients’ over-the-counter corporate bond deals, reported to NSE’s CBRICS within ${d.minutes} minutes of the deal and settled through NSE Clearing. One person enters each deal, another reports it. Ledgerline reads the deals and settlements back, gives each settlement the client’s bank and demat accounts, and raises a work item for the money.`, acts) + off +
      `<div class="kpis">${kpi('Waiting for a checker', waiting, 'deals, changes and participants', waiting ? 'warn' : '')}${kpi('Not reported yet', (c.Draft || 0) + (c.Returned || 0) + (c.Refused || 0), (c.Refused || 0) + ' refused by CBRICS', c.Refused ? 'bad' : '')}${kpi('Reported late', d.late, 'last 30 days, past the ' + d.minutes + '-minute limit', d.late ? 'bad' : 'good')}${kpi('Awaiting the client', c['Awaiting confirmation'] || 0, 'sales reported to a client as buyer', c['Awaiting confirmation'] ? 'warn' : '')}${kpi('Settling', c.Settling || 0, (c.Settled || 0) + ' settled')}</div>` +
      `<div class="gtabs" role="tablist">${tabs.map(([k, l]) => `<button role="tab" class="${tab === k ? 'on' : ''}" aria-selected="${tab === k}" data-act="cbTab" data-id="${k}">${l}</button>`).join('')}</div>` +
      ({ deals: cbDeals, settle: cbSettle, parties: cbParties, conn: cbConn }[tab] || cbDeals)(d);
  }
};

function cbDealRow(x) {
  const late = x.late ? ' ' + pill(x.late_minutes ? `late ${x.late_minutes} min` : 'late', 'bad') : '';
  return `<tr><td class="n">${esc(x.id)}${x.source === 'CBRICS' ? '<div class="dim small">found at CBRICS</div>' : ''}</td>
    <td><button class="linkish mono" data-act="client" data-id="${esc(x.ucc)}">${esc(x.ucc)}</button> ${esc(x.name || '')}</td>
    <td>${x.side === 'Buy' ? 'Buys' : 'Sells'}</td><td class="mono small">${esc(x.isin)}</td><td>${esc(x.counterparty)}<div class="dim small">${esc(x.counterparty_name || '')}</div></td>
    <td class="r">${nf(x.quantity)}<div class="dim small">${inr(x.value)}</div></td><td class="r">${nf(x.price, 4)}<div class="dim small">${nf(x.yield, 4)}% ${CB_YT[x.yield_type] || ''}</div></td>
    <td class="small">${fmtDate(x.trade_date)} ${esc(x.trade_time || '')}<div class="dim">T+${x.settle}</div></td>
    <td>${cbPill(x.status)}${late}${x.pending ? `<div class="dim small">${esc(x.pending)} waiting for a checker</div>` : ''}</td>
    <td><button class="btn sm" data-act="cbOpen" data-id="${esc(x.id)}">Open</button></td></tr>`;
}

function cbDeals(d) {
  const f = UI.cbF, rows = d.deals.filter(x => f === 'all' || (f === 'open' ? CB_OPEN.includes(x.status) : x.pending));
  return `<div class="panel"><div class="hd"><h3>Deals</h3><div class="tools">${segBtns('cbF', [['open', 'Open'], ['wait', 'Waiting for a checker'], ['all', 'All']])}</div></div>` +
    tbl([['Deal'], ['Client'], ['Side'], ['ISIN'], ['Counterparty'], ['Bonds · face value', 'r'], ['Price · yield', 'r'], ['Dealt'], ['Status'], ['']], rows.map(cbDealRow), { tall: true, empty: 'No deals here.' }) + '</div>';
}

function cbSettle(d) {
  const rows = d.deals.filter(x => x.settlement);
  return `<div class="panel"><div class="hd"><h3>Settlement</h3><small>Delivery against payment with NSE Clearing. The money moves between each side’s own bank account and NSE Clearing, so nothing is posted to the client’s ledger.</small></div>` +
    tbl([['Settlement'], ['Deal'], ['Client'], ['Date'], ['Bonds', 'r'], ['Consideration', 'r'], ['Stamp duty', 'r'], ['Buyer pays in', 'r'], ['Seller paid', 'r'], ['Accounts'], ['Status']], rows.map(x => { const s = x.settlement; return `<tr>
      <td class="n">${esc(s.settlement_no || '—')}<div class="dim small">${esc(s.source || '')}</div></td><td class="n"><button class="linkish" data-act="cbOpen" data-id="${esc(x.id)}">${esc(x.id)}</button></td>
      <td>${esc(x.ucc)} <span class="dim small">${x.side === 'Buy' ? 'buys' : 'sells'}</span></td><td>${fmtDate(s.date)}</td><td class="r">${nf(s.quantity)}</td><td class="r">${inr(s.consideration, 2)}</td>
      <td class="r">${inr(s.stamp_duty, 2)}<div class="dim small">${esc(s.stamp_bearer || '')}</div></td><td class="r">${inr(s.buyer_payin, 2)}</td><td class="r">${inr(s.seller_payout, 2)}</td>
      <td class="small">${esc(s.ifsc || '')} ${esc(s.account || '')}<div class="mono">${esc((s.dp_id || '') + (s.ben_id || ''))}</div>${x.pending === 'Account' ? pill('Change waiting', 'warn') : ''}</td>
      <td>${pill(s.status_text || '—', s.status === '4' ? 'good' : ['5', '6', '7', '8', '9'].includes(s.status) ? 'bad' : 'acc')}${s.utr ? `<div class="dim small">UTR ${esc(s.utr)}</div>` : ''}</td></tr>`; }), { tall: true, empty: 'No orders for settlement yet.' }) + '</div>';
}

function cbParties(d) {
  const mk = cbMk();
  return `<div class="panel"><div class="hd"><h3>Participants</h3><small>Every deal names a CBRICS participant code for each side. A client without its own CBRICS account is kept as an unregistered participant, filled in from the client’s record with the client’s own bank and demat accounts.</small>
    ${mk ? '<div class="tools"><button class="btn sm" data-act="cbClientParty">Add a client</button><button class="btn sm" data-act="cbPartyEdit">Add a counterparty</button><button class="btn sm" data-act="cbFind">Find a registered participant</button>' + (d.set ? '<button class="btn sm" data-act="cbImport">Take in from CBRICS</button>' : '') + '</div>' : ''}</div>` +
    tbl([['Code'], ['Kind'], ['Name'], ['Client'], ['PAN'], ['Status'], ['Accounts'], ['']], d.parties.map(p => `<tr><td class="mono">${esc(p.code)}</td><td>${esc(p.kind)}</td><td>${esc(p.name)}</td>
      <td>${p.ucc ? `<button class="linkish mono" data-act="client" data-id="${esc(p.ucc)}">${esc(p.ucc)}</button>` : '<span class="dim">—</span>'}</td><td class="mono small">${esc(p.pan || '')}</td>
      <td>${cbPill(p.state)}${p.pending ? `<div class="dim small">${esc(cbReqLabel(p.pending.action))} waiting for a checker</div>` : ''}${p.remarks ? `<div class="dim small wrap">${esc(p.remarks)}</div>` : ''}</td>
      <td class="small">${(p.banks || []).length} bank · ${(p.dps || []).length} demat</td><td>${p.kind === 'Unregistered' ? `<button class="btn sm" data-act="cbPartyOpen" data-id="${esc(p.code)}">Open</button>` : ''}</td></tr>`), { tall: true, empty: 'No participants kept yet.' }) + '</div>';
}

function cbConn(d) {
  const st = d.state || {};
  return `<div class="panel"><div class="hd"><h3>CBRICS link</h3></div><div class="pad"><div class="kv">
    <div><small>Connection</small><b>${d.set ? (d.live ? 'Live' : 'Testing (UAT)') : 'Not set'}</b></div><div><small>Reads</small><b>${d.auto ? 'Every 10 minutes, 08:00 to 20:00' : 'Only when asked'}</b></div>
    <div><small>Last read</small><b>${cbWhen(st.last_run)}</b></div><div><small>Last without problems</small><b>${cbWhen(st.last_ok)}</b></div>
    <div><small>Reporting limit</small><b>${d.minutes} minutes after the deal</b></div><div><small>Notifications</small><b>${st.notices || 0}${st.last_notice ? ' · last ' + cbWhen(st.last_notice) : ''}</b></div></div>
    ${st.last_error ? `<div class="note warn">Last read: ${esc(st.last_error)}</div>` : ''}
    <p class="small" style="margin-top:12px">Give NSE this callback base address (NSE adds <span class="mono">/v1/notification</span>): <span class="mono">${esc(location.origin + d.callback)}</span>. ${d.callback_key_set ? 'The callback key is set: each notification’s token is checked against it.' : '<b>The callback key from NSE is not set</b>: notifications are refused until the Admin enters it in Connections.'} A notification has Ledgerline read CBRICS again within a minute.</p>
    ${(st.unmatched || []).length ? `<p class="small">CBRICS transactions of no client kept here (last read): <span class="mono">${st.unmatched.map(esc).join(', ')}</span></p>` : ''}</div></div>`;
}

const cbReqLabel = a => ({ add: 'New participant', update: 'Correction', contact: 'Contact change', delete: 'Deletion', add_bank: 'New bank account', add_dp: 'New demat account', default_bank: 'Default bank account', default_dp: 'Default demat account', bank_status: 'Bank account status', dp_status: 'Demat account status' }[a] || a);

/* ---------- deal form ---------- */
function cbDealForm(x) {
  const d = C.cbrics, o = x || {};
  const cps = d.parties.filter(p => p.state !== 'Deleted' && (p.kind === 'Registered' || p.state === 'Approved'));
  const f = (id, label, val, extra) => `<div class="fld"><label for="${id}">${label}</label><input class="in ${extra || ''}" id="${id}" value="${esc(val ?? '')}"></div>`;
  modal(mhd(x ? `Change ${esc(x.id)}` : 'New corporate bond deal', x ? 'A change to a reported deal goes to CBRICS as a modification (before the buyer confirms it); a checker sends it.' : `The client’s over-the-counter deal. A checker reports it to CBRICS: within ${d.minutes} minutes of the deal.`) + `<form class="bd" id="cb-f"><div class="form">
    ${f('cb-ucc', 'Client (UCC)', o.ucc, 'mono')}
    <div class="fld"><label for="cb-side">The client</label><select class="in" id="cb-side"><option value="Sell" ${o.side === 'Sell' ? 'selected' : ''}>Sells</option><option value="Buy" ${o.side === 'Buy' ? 'selected' : ''}>Buys</option></select></div>
    <div class="fld"><label for="cb-isin">ISIN</label><input class="in mono" id="cb-isin" value="${esc(o.isin || '')}" list="cb-held" autocomplete="off"><datalist id="cb-held"></datalist><button class="linkish small" type="button" id="cb-look">Today’s price and yield on CBRICS</button></div>
    <div class="fld"><label for="cb-cp">Counterparty</label><select class="in" id="cb-cp">${cps.map(p => `<option value="${esc(p.code)}" ${o.counterparty === p.code ? 'selected' : ''}>${esc(p.code)} · ${esc(p.name)}${p.ucc ? ' (client ' + esc(p.ucc) + ')' : ''}</option>`).join('')}</select></div>
    ${f('cb-qty', 'Number of bonds', o.quantity, 'mono')}${f('cb-fv', 'Face value of a bond (₹)', o.face_value, 'mono')}
    ${f('cb-px', 'Price (per ₹100 face value)', o.price, 'mono')}${f('cb-ai', 'Accrued interest (₹)', o.accr_int || '0', 'mono')}
    <div class="fld"><label for="cb-yt">Yield type</label><select class="in" id="cb-yt">${Object.entries(CB_YT).map(([k, l]) => `<option value="${k}" ${o.yield_type === k ? 'selected' : ''}>${l}</option>`).join('')}</select></div>
    ${f('cb-y', 'Yield (%)', o.yield, 'mono')}
    <div class="fld"><label for="cb-pc">Put or call date (YTP or YTC)</label><input class="in" type="date" id="cb-pc" value="${esc(o.put_call_date || '')}"></div>
    <div class="fld"><label for="cb-yr">Yield outside CBRICS’s band because of</label><select class="in" id="cb-yr"><option value="">— (within the band)</option><option value="1" ${o.yield_reason === '1' ? 'selected' : ''}>Credit</option><option value="2" ${o.yield_reason === '2' ? 'selected' : ''}>Liquidity</option></select></div>
    <div class="fld"><label for="cb-st">Settlement</label><select class="in" id="cb-st">${[0, 1, 2].map(n => `<option value="${n}" ${(o.settle ?? 1) === n ? 'selected' : ''}>${n ? 'T+' + n : 'Same day'}</option>`).join('')}</select></div>
    <div class="fld"><label for="cb-td">Deal date</label><input class="in" type="date" id="cb-td" value="${esc(o.trade_date || '')}"></div>
    ${f('cb-tt', 'Deal time (HH:MM)', o.trade_time, 'mono')}
    <div class="full dim small" id="cb-note"></div>
  </div><div id="cb-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">${x ? 'Save the change' : 'Save for a checker'}</button></div></form>`);
  if (x) { $('cb-ucc').readOnly = true; $('cb-side').disabled = true; }
  const propose = async (withIsin, init) => {
    const u = $('cb-ucc').value.trim().toUpperCase(); if (!u && !withIsin && !init) return;
    try {
      const p = await get(`/v1/cbrics/propose?ucc=${cbEnc(u)}&isin=${withIsin ? cbEnc($('cb-isin').value.trim().toUpperCase()) : ''}`);
      $('cb-held').innerHTML = p.holdings.map(h => `<option value="${esc(h.isin)}">${esc(h.symbol || '')} · ${esc(String(h.qty))} held</option>`).join('');
      if (!$('cb-tt').value) $('cb-tt').value = p.trade_time; if (!$('cb-td').value) $('cb-td').value = p.trade_date;
      const bits = [];
      if (u) bits.push(p.client_code ? `CBRICS code ${esc(p.client_code)} (${esc(p.code_state)})` : '<span class="neg">No CBRICS participant code yet: add the client under Participants first.</span>');
      if (withIsin && p.issue) { if (!$('cb-px').value && p.price) $('cb-px').value = p.price; if (!$('cb-y').value && p.yield) $('cb-y').value = p.yield; if (!$('cb-pc').value && $('cb-yt').value === 'M' && p.maturity) $('cb-pc').value = p.maturity; bits.push(`${esc(p.issue.description || p.issue.symbol)}: ${esc(p.issue.tot_no_of_trades ?? p.issue.totNoOfTrades ?? '')} trades today, average price ${esc(p.price)}, yield ${esc(p.yield)}%, maturing ${fmtDate(p.maturity)}`); }
      if (withIsin && p.note) bits.push(esc(p.note));
      if (num(p.held)) bits.push(`${qf(p.held)} free in the client’s demat accounts`);
      $('cb-note').innerHTML = bits.join(' · ');
    } catch (e) { $('cb-note').textContent = e.message; }
  };
  $('cb-ucc').onchange = () => propose(false); $('cb-look').onclick = () => propose(true);
  propose(false, true);
  $('cb-f').onsubmit = async e => {
    e.preventDefault();
    const b = { ucc: $('cb-ucc').value.trim().toUpperCase(), side: $('cb-side').value, isin: $('cb-isin').value.trim().toUpperCase(), counterparty: $('cb-cp').value, quantity: parseInt($('cb-qty').value, 10) || 0,
      face_value: $('cb-fv').value.trim() || '0', price: $('cb-px').value.trim() || '0', accr_int: $('cb-ai').value.trim() || '0', yield_type: $('cb-yt').value, yield: $('cb-y').value.trim() || '0',
      put_call_date: $('cb-pc').value, yield_reason: $('cb-yr').value, settle: +$('cb-st').value, trade_date: $('cb-td').value, trade_time: $('cb-tt').value.trim() };
    if (x) b.id = x.id;
    try { const r = await post('/v1/cbrics/deals', b); closeModal(); toast(`${r.id} saved: a checker ${r.pending === 'Change' ? 'sends the change' : 'reports it'}${(r.warnings || []).length ? ' · ' + r.warnings.join('; ') : ''}`); render({ keep: true }); }
    catch (err) { $('cb-err').innerHTML = errHTML(err); }
  };
}

function cbDealModal(x) {
  const s = x.settlement, mk = cbMk(), ck = cbCk() && x.pending && x.pending_by !== S.me?.actor;
  const unrep = ['Draft', 'Returned', 'Refused'].includes(x.status) && x.source !== 'CBRICS';
  const btns = [];
  if (ck) btns.push(`<button class="btn" data-act="cbReturn" data-id="${esc(x.id)}">Return</button><button class="btn pri" data-act="cbSend" data-id="${esc(x.id)}">${{ Report: 'Report to CBRICS', Change: 'Send the change', Accept: 'Send: accept', Reject: 'Send: reject', Account: 'Send the accounts' }[x.pending]}</button>`);
  if (mk && !x.pending && x.source !== 'CBRICS' && (unrep || ['Reported', 'Instruction pending'].includes(x.status))) btns.push(`<button class="btn" data-act="cbEdit" data-id="${esc(x.id)}">${unrep ? 'Correct' : 'Change'}</button>`);
  if (mk && unrep) btns.push(`<button class="btn bad" data-act="cbCancel" data-id="${esc(x.id)}">Cancel the deal</button>`);
  if (mk && !x.pending && x.status === 'Awaiting confirmation') btns.push(`<button class="btn" data-act="cbAnswer" data-id="${esc(x.id)}" data-v="0">Client rejects</button><button class="btn pri" data-act="cbAnswer" data-id="${esc(x.id)}" data-v="1">Client accepts</button>`);
  if (mk && !x.pending && s && s.status === '0') btns.push(`<button class="btn" data-act="cbAccounts" data-id="${esc(x.id)}">Settlement accounts</button>`);
  const pend = x.pending ? `<div class="note warn"><b>${esc(x.pending)}</b> waiting for a checker (entered by ${esc(x.pending_by === 'system:scheduler' ? 'Ledgerline' : x.pending_by)}).${x.change ? ` New terms: ${nf(x.change.quantity)} bonds at ${nf(x.change.price, 4)}, yield ${nf(x.change.yield, 4)}%, dealt ${esc(x.change.trade_time)}.` : ''}${x.account ? ` ${x.account.ifsc ? 'Seller’s payout to ' + esc(x.account.bank + ' ' + x.account.ifsc + ' ' + x.account.account) + '. ' : ''}${x.account.demat ? 'Buyer’s bonds to ' + esc(x.account.demat) + '.' : ''}${x.account.why ? ' (' + esc(x.account.why) + ')' : ''}` : ''}${x.pending_by === S.me?.actor ? ' A different person sends it.' : ''}</div>` : '';
  modal(mhd(`${esc(x.id)} · ${esc(x.ucc)} ${x.side === 'Buy' ? 'buys' : 'sells'} ${esc(x.isin)}`, `${esc(x.name || '')} · counterparty ${esc(x.counterparty)} ${esc(x.counterparty_name || '')}`) + `<div class="bd">${pend}
    ${(x.warnings || []).length ? `<div class="note">${x.warnings.map(esc).join('<br>')}</div>` : ''}${x.reason ? `<div class="note warn">${esc(x.reason)}</div>` : ''}
    <div class="kv"><div><small>Status</small><b>${cbPill(x.status)}${x.late ? ' ' + pill('late' + (x.late_minutes ? ' ' + x.late_minutes + ' min' : ''), 'bad') : ''}</b></div><div><small>Report by</small><b>${cbWhen(x.due)}</b></div>
    <div><small>Bonds · face value</small><b>${nf(x.quantity)} · ${inr(x.value, 2)}</b></div><div><small>Price · yield</small><b>${nf(x.price, 4)} · ${nf(x.yield, 4)}% ${CB_YT[x.yield_type] || ''}${x.put_call_date ? ' to ' + fmtDate(x.put_call_date) : ''}</b></div>
    <div><small>Accrued interest</small><b>${inr(x.accr_int, 2)}</b></div><div><small>Consideration</small><b>${inr(num(x.consideration) ? x.consideration : x.estimate, 2)}${num(x.consideration) ? '' : ' <span class="dim small">(estimate)</span>'}</b></div>
    <div><small>Dealt · settlement</small><b>${fmtDate(x.trade_date)} ${esc(x.trade_time)} · ${x.settle ? 'T+' + x.settle : 'same day'}</b></div><div><small>Deal type · buyer confirmation</small><b>${{ B: 'Brokered', D: 'Direct', I: 'Inter-scheme' }[x.deal_type] || esc(x.deal_type)} · ${x.send_for === 'C' ? 'optional' : 'mandatory'}</b></div>
    <div><small>CBRICS transaction</small><b class="mono small">${esc(x.order_no || '—')}${x.counter_order_no ? ' · seller’s ' + esc(x.counter_order_no) : ''}</b></div><div><small>Reported</small><b>${cbWhen(x.reported_at)} ${esc(x.reported_by || '')}</b></div>
    ${s ? `<div><small>Settlement</small><b>${esc(s.settlement_no)} on ${fmtDate(s.date)} · ${esc(s.status_text)}</b></div><div><small>Buyer pays in · seller paid</small><b>${inr(s.buyer_payin, 2)} · ${inr(s.seller_payout, 2)}</b></div>
    <div><small>Payout account</small><b class="small">${esc(s.ifsc || '—')} ${esc(s.account || '')}</b></div><div><small>Buyer’s demat account</small><b class="mono small">${esc((s.dp_id || '') + (s.ben_id || '')) || '—'}</b></div>` : ''}</div>
    <h4 style="margin:14px 0 6px">History</h4>${tbl([['When'], ['Who'], ['What']], (x.history || []).slice().reverse().map(h => `<tr><td class="small">${cbWhen(h.at)}</td><td class="small">${esc(h.by === 'system:scheduler' ? 'Ledgerline' : h.by)}</td><td class="small wrap">${esc(h.what)}</td></tr>`))}
    <div id="cb-err"></div></div><div class="ft"><button class="btn" data-act="close">Close</button>${btns.join('')}</div>`);
}

/* ---------- participant forms ---------- */
function cbPartyForm(p, fromClient) {
  const d = C.cbrics, o = p || { kind: 'Unregistered', banks: [{ default: true }], dps: [{ default: true }] }, client = !!o.ucc;
  const ed = !o.state || ['Draft', 'Returned', 'Approved'].includes(o.state), contactOnly = o.state === 'Approved';
  const f = (id, label, val, extra, ro) => `<div class="fld"><label for="${id}">${label}</label><input class="in ${extra || ''}" id="${id}" value="${esc(val ?? '')}" ${ro ? 'readonly' : ''}></div>`;
  const acc = client || contactOnly ? `<div class="full"><b>Accounts</b> <span class="dim small">${client ? 'the client’s active registered accounts, from the client’s record (the primary and the pay-out demat account as default)' : 'changed one at a time after approval'}</span>${tbl([['Bank'], ['IFSC'], ['Account'], ['Default']], (o.banks || []).map(b => `<tr><td>${esc(b.bank)}</td><td class="mono">${esc(b.ifsc)}</td><td class="mono">${esc(b.account || 'interbank')}</td><td>${b.default ? '✓' : ''}</td></tr>`))}${tbl([['Depository'], ['Account'], ['Default']], (o.dps || []).map(x => `<tr><td>${esc(x.depository)}</td><td class="mono">${esc((x.dp_id || '') + x.ben_id)}</td><td>${x.default ? '✓' : ''}</td></tr>`))}</div>`
    : `<div class="full"><b>Bank account</b> <span class="dim small">(more after approval)</span></div>${f('cp-bn', 'Bank', o.banks?.[0]?.bank)}${f('cp-bi', 'IFSC', o.banks?.[0]?.ifsc, 'mono')}${f('cp-ba', 'Account number (empty: interbank)', o.banks?.[0]?.account, 'mono')}
       <div class="full"><b>Demat account</b></div>${f('cp-dm', 'Demat account (16 characters: NSDL IN… or CDSL BO ID)', (o.dps?.[0]?.dp_id || '') + (o.dps?.[0]?.ben_id || ''), 'mono')}`;
  modal(mhd(p && p.state ? `${esc(o.code)} · ${esc(o.name)}` : fromClient ? `Keep ${esc(o.ucc)} at CBRICS` : 'New unregistered counterparty', contactOnly ? 'Approved at CBRICS: only the contact details change here (CBRICS’s contact update); a checker sends it.' : 'An unregistered participant for CBRICS. A checker sends it; NSE approves it.') + `<form class="bd" id="cp-f"><div class="form">
    ${f('cp-code', 'Participant code (up to 30 characters)', o.code, 'mono', !!o.state && o.state !== 'Draft')}${f('cp-name', 'Name', o.name, '', contactOnly || client)}
    ${f('cp-pan', 'PAN (or PAN_EXEMPT)', o.pan, 'mono', contactOnly || client)}${f('cp-dob', 'Date of birth or incorporation (YYYY-MM-DD)', o.dob, 'mono', contactOnly)}
    ${f('cp-ct', 'Contact person', o.contact_person)}${f('cp-tel', 'Telephone', o.telephone, 'mono')}
    ${f('cp-mob', 'Mobile numbers (up to 3, comma separated)', (o.mobiles || []).join(', '), 'mono')}${f('cp-em', 'Email addresses (up to 3, comma separated)', (o.emails || []).join(', '))}
    ${f('cp-a1', 'Address line 1', o.address1)}${f('cp-a2', 'Address line 2', o.address2)}${f('cp-a3', 'Address line 3', o.address3)}
    <div class="fld"><label for="cp-sc">Registered office or domicile state</label><select class="in" id="cp-sc" ${contactOnly ? 'disabled' : ''}><option value="">—</option>${Object.entries(d.state_codes).sort((a, b) => a[1].localeCompare(b[1])).map(([k, l]) => `<option value="${k}" ${o.state_code === k ? 'selected' : ''}>${esc(l)} (${k})</option>`).join('')}</select></div>
    <div class="fld full"><label for="cp-ra">Registered office or domicile address</label><input class="in" id="cp-ra" value="${esc(o.reg_address || '')}" ${contactOnly ? 'readonly' : ''}></div>
    ${f('cp-lei', 'LEI (if any)', o.lei, 'mono', contactOnly)}${f('cp-lx', 'LEI expiry (YYYY-MM-DD)', o.lei_expiry, 'mono', contactOnly)}${f('cp-cu', 'Custodian code (if any)', o.custodian, 'mono', contactOnly)}
    ${acc}
  </div><div id="cp-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button>${ed ? '<button class="btn pri" type="submit">Save for a checker</button>' : ''}</div></form>`);
  $('cp-f').onsubmit = async e => {
    e.preventDefault();
    const list = id => $(id).value.split(',').map(x => x.trim()).filter(Boolean);
    const b = { ...o, code: $('cp-code').value.trim().toUpperCase(), kind: 'Unregistered', name: $('cp-name').value.trim(), pan: $('cp-pan').value.trim().toUpperCase(), dob: $('cp-dob').value.trim(),
      contact_person: $('cp-ct').value.trim(), telephone: $('cp-tel').value.trim(), mobiles: list('cp-mob'), emails: list('cp-em'), address1: $('cp-a1').value.trim(), address2: $('cp-a2').value.trim(), address3: $('cp-a3').value.trim(),
      state_code: $('cp-sc').value, reg_address: $('cp-ra').value.trim(), lei: $('cp-lei').value.trim(), lei_expiry: $('cp-lx').value.trim(), custodian: $('cp-cu').value.trim() };
    if (!client && !contactOnly) {
      const dm = $('cp-dm').value.replace(/\s|-/g, '').toUpperCase();
      b.banks = [{ bank: $('cp-bn').value.trim(), ifsc: $('cp-bi').value.trim().toUpperCase(), account: $('cp-ba').value.trim(), default: true }];
      b.dps = [dm.startsWith('IN') ? { depository: 'NSDL', dp_id: dm.slice(0, 8), ben_id: dm.slice(8), default: true } : { depository: 'CDSL', ben_id: dm, default: true }];
    }
    delete b.pending; delete b.history;
    try { await post('/v1/cbrics/participants', b); closeModal(); toast(`${b.code} saved: a checker sends it to CBRICS`); render({ keep: true }); }
    catch (err) { $('cp-err').innerHTML = errHTML(err); }
  };
}

function cbPartyModal(p) {
  const mk = cbMk(), ck = cbCk() && p.pending && p.pending.by !== S.me?.actor, appr = p.state === 'Approved';
  const req = p.pending ? `<div class="note warn"><b>${esc(cbReqLabel(p.pending.action))}</b> waiting for a checker (entered by ${esc(p.pending.by)})${p.pending.bank ? ': ' + esc(p.pending.bank.ifsc + ' ' + (p.pending.bank.account || '')) : ''}${p.pending.dp ? ': ' + esc((p.pending.dp.dp_id || '') + p.pending.dp.ben_id) : ''}${p.pending.status ? ' → ' + esc(p.pending.status) : ''}.</div>` : '';
  const acct = (kind, a) => mk && appr && !p.pending ? `<button class="linkish small" data-act="cbAcct" data-id="${esc(p.code)}" data-k="${kind}" data-v="${esc(kind === 'bank' ? a.ifsc + '|' + (a.account || '') : (a.dp_id || '') + '|' + a.ben_id)}">Change</button>` : '';
  modal(mhd(`${esc(p.code)} · ${esc(p.name)}`, `${esc(p.kind)} participant${p.ucc ? ' · client ' + esc(p.ucc) : ''} · ${esc(p.state)}${p.cbrics_id ? ' · CBRICS id ' + esc(p.cbrics_id) : ''}`) + `<div class="bd">${req}
    ${p.remarks ? `<div class="note">${esc(p.remarks)}</div>` : ''}<div class="kv"><div><small>PAN</small><b class="mono">${esc(p.pan || '—')}</b>${p.pan_check ? `<span class="dim small">${esc(p.pan_check)}</span>` : ''}</div><div><small>Contact</small><b>${esc(p.contact_person || '—')}</b><span class="dim small">${esc((p.mobiles || []).join(', '))} · ${esc((p.emails || []).join(', '))}</span></div>
    <div><small>State</small><b>${esc(C.cbrics.state_codes[p.state_code] || p.state_code || '—')}</b></div><div><small>Registered address</small><b class="small">${esc(p.reg_address || '—')}</b></div></div>
    <h4 style="margin:14px 0 6px">Bank accounts</h4>${tbl([['Bank'], ['IFSC'], ['Account'], ['Default'], ['Status'], ['CBRICS'], ['']], (p.banks || []).map(b => `<tr><td>${esc(b.bank)}</td><td class="mono">${esc(b.ifsc)}</td><td class="mono">${esc(b.account || 'interbank')}</td><td>${b.default ? '✓' : ''}</td><td>${esc(b.status || '')}</td><td class="small">${esc(cbWF(b.workflow))}</td><td>${acct('bank', b)}</td></tr>`))}
    <h4 style="margin:14px 0 6px">Demat accounts</h4>${tbl([['Depository'], ['Account'], ['Default'], ['Status'], ['CBRICS'], ['']], (p.dps || []).map(x => `<tr><td>${esc(x.depository)}</td><td class="mono">${esc((x.dp_id || '') + x.ben_id)}</td><td>${x.default ? '✓' : ''}</td><td>${esc(x.status || '')}</td><td class="small">${esc(cbWF(x.workflow))}</td><td>${acct('dp', x)}</td></tr>`))}
    <h4 style="margin:14px 0 6px">History</h4>${tbl([['When'], ['Who'], ['What']], (p.history || []).slice().reverse().map(h => `<tr><td class="small">${cbWhen(h.at)}</td><td class="small">${esc(h.by === 'system:scheduler' ? 'Ledgerline' : h.by)}</td><td class="small wrap">${esc(h.what)}</td></tr>`))}
    </div><div class="ft"><button class="btn" data-act="close">Close</button>${ck ? `<button class="btn" data-act="cbPartyReturn" data-id="${esc(p.code)}">Return</button><button class="btn pri" data-act="cbPartySend" data-id="${esc(p.code)}">Send to CBRICS</button>` : ''}
    ${mk && !p.pending && ['Draft', 'Returned', 'Approved'].includes(p.state) ? `<button class="btn" data-act="cbPartyEdit" data-id="${esc(p.code)}">${appr ? 'Change contact details' : 'Correct'}</button>` : ''}
    ${mk && appr && !p.pending ? `<button class="btn" data-act="cbAddAcct" data-id="${esc(p.code)}" data-k="bank">Add a bank account</button><button class="btn" data-act="cbAddAcct" data-id="${esc(p.code)}" data-k="dp">Add a demat account</button><button class="btn bad" data-act="cbPartyDelete" data-id="${esc(p.code)}">Delete at CBRICS</button>` : ''}</div>`);
}
const CB_WF = { 100: 'With the firm’s CBRICS checker', 16: 'Returned by the firm’s CBRICS checker', 15: 'Rejected by the firm’s CBRICS checker', 0: 'With NSE', 10: 'With NSE', 1: 'Approved', 5: 'Rejected by NSE', 6: 'Returned by NSE' };
const cbWF = w => (w === undefined || w === '' || w === null) ? '' : (CB_WF[w] || w);

const cbReq = (code, b) => act(() => post(`/v1/cbrics/participants/${cbEnc(code)}/request`, b), 'Saved: a checker sends it to CBRICS');

Object.assign(A, {
  cbTab: id => { UI.cbTab = id; render({ keep: true }); },
  cbSync: () => act(() => post('/v1/cbrics/sync', {}), r => `Read ${r.orders} deals and ${r.settlements} settlements: ${r.matched} matched, ${r.added} found at CBRICS${r.problems?.length ? ' · ' + r.problems[0] : ''}`),
  cbNew: () => cbDealForm(null),
  cbOpen: id => { const x = cbDeal(id); if (x) cbDealModal(x); },
  cbEdit: id => { const x = cbDeal(id); if (x) cbDealForm(x); },
  cbSend: id => confirmModal('Send to CBRICS', id, `<div class="note">${esc({ Report: 'The deal goes to CBRICS as reported (a sale as the seller’s report; a purchase as the buyer’s standing instruction).', Change: 'CBRICS cancels the reported order and takes the new terms as a new one.', Accept: 'The client accepts the sale reported to them.', Reject: 'The client rejects the sale reported to them.', Account: 'The settlement pays out to the accounts proposed.' }[cbDeal(id)?.pending] || '')}</div>`, 'Send', () => act(() => post(`/v1/cbrics/deals/${cbEnc(id)}/decide`, { approve: true }), r => `${r.id}: ${r.status}`)),
  cbReturn: id => confirmModal('Return to the maker', id, '<div class="fld full"><label for="cb-why">Why</label><input class="in" id="cb-why"></div>', 'Return', () => act(() => post(`/v1/cbrics/deals/${cbEnc(id)}/decide`, { approve: false, reason: $('cb-why').value.trim() }), 'Returned')),
  cbCancel: id => confirmModal('Cancel the deal', id, '<div class="fld full"><label for="cb-why">Why</label><input class="in" id="cb-why"></div><div class="note">Only a deal not yet at CBRICS is cancelled here.</div>', 'Cancel the deal', () => act(() => post(`/v1/cbrics/deals/${cbEnc(id)}/cancel`, { reason: $('cb-why').value.trim() }), 'Cancelled'), true),
  cbAnswer: (id, el) => { const ok = el.dataset.v === '1'; confirmModal(ok ? 'The client accepts' : 'The client rejects', id, `<div class="fld full"><label for="cb-yr2">Yield outside CBRICS’s band because of (if CBRICS asks)</label><select class="in" id="cb-yr2"><option value="">—</option><option value="1">Credit</option><option value="2">Liquidity</option></select></div><div class="note">Record the client’s answer as they gave it. A checker sends it to CBRICS.</div>`, 'Save for a checker', () => act(() => post(`/v1/cbrics/deals/${cbEnc(id)}/respond`, { accept: ok, yield_reason: $('cb-yr2').value }), 'Saved: a checker sends it')); },
  cbAccounts: async id => {
    const x = cbDeal(id); if (!x) return;
    let p; try { p = await get(`/v1/cbrics/propose?ucc=${cbEnc(x.ucc)}`); } catch (e) { toast(e.message, true); return; }
    const seller = x.side === 'Sell', banks = seller ? p.banks : [], dps = seller ? [] : p.dps;
    confirmModal('Settlement accounts', `${x.id} · settlement ${esc(x.settlement.settlement_no)}`, `${seller ? `<div class="fld full"><label for="cb-bk">The client’s payout bank account</label><select class="in" id="cb-bk">${banks.map(b => `<option value="${esc(b.ifsc + '-' + String(b.account).slice(-4))}" ${b.default ? 'selected' : ''}>${esc(b.bank)} ${esc(b.ifsc)} ${esc(b.account)}${b.default ? ' (primary)' : ''}</option>`).join('')}</select></div>`
      : `<div class="fld full"><label for="cb-dm">The client’s demat account for the bonds</label><select class="in" id="cb-dm">${dps.map(d => `<option value="${esc((d.dp_id || '') + d.ben_id)}" ${d.default ? 'selected' : ''}>${esc(d.depository)} ${esc((d.dp_id || '') + d.ben_id)}${d.default ? ' (pay-out account)' : ''}</option>`).join('')}</select></div>`}
      <div class="fld full"><label for="cb-aw">Why (optional)</label><input class="in" id="cb-aw"></div><div class="note">Only the client’s own registered accounts. A checker sends the change before the settlement starts. The counterparty’s accounts change here only when the firm keeps it as an unregistered participant (Ledgerline proposes its default when CBRICS has another).</div>`,
      'Save for a checker', () => act(() => post(`/v1/cbrics/deals/${cbEnc(id)}/accounts`, seller ? { bank_key: $('cb-bk').value, why: $('cb-aw').value.trim() } : { demat: $('cb-dm').value, why: $('cb-aw').value.trim() }), 'Saved: a checker sends it'));
  },
  cbClientParty: () => confirmModal('Keep a client at CBRICS', 'An unregistered participant filled in from the client’s record', '<div class="fld full"><label for="cb-pu">Client (UCC)</label><input class="in mono" id="cb-pu"></div>', 'Fill in', async () => { const p = await get(`/v1/cbrics/participants/from-client/${cbEnc($('cb-pu').value.trim().toUpperCase())}`); closeModal(); cbPartyForm(p, true); }),
  cbPartyEdit: id => cbPartyForm(id ? cbParty(id) : null, false),
  cbPartyOpen: id => { const p = cbParty(id); if (p) cbPartyModal(p); },
  cbPartySend: id => confirmModal('Send to CBRICS', id, `<div class="note">${esc(cbReqLabel(cbParty(id)?.pending?.action))} goes to CBRICS; NSE approves it.</div>`, 'Send', () => act(() => post(`/v1/cbrics/participants/${cbEnc(id)}/decide`, { approve: true }), r => `${r.code}: ${r.state}`)),
  cbPartyReturn: id => confirmModal('Return to the maker', id, '<div class="fld full"><label for="cb-why">Why</label><input class="in" id="cb-why"></div>', 'Return', () => act(() => post(`/v1/cbrics/participants/${cbEnc(id)}/decide`, { approve: false, reason: $('cb-why').value.trim() }), 'Returned')),
  cbPartyDelete: id => confirmModal('Delete at CBRICS', id, '<div class="note">CBRICS deletes the participant and all its active and suspended accounts. A checker sends it.</div>', 'Save for a checker', () => cbReq(id, { action: 'delete' }), true),
  cbAcct: (id, el) => {
    const [a, b] = el.dataset.v.split('|'), bank = el.dataset.k === 'bank';
    confirmModal(bank ? 'Bank account' : 'Demat account', `${id} · ${esc(a + (bank ? ' ' : '') + b)}`, `<div class="fld full"><label for="cb-ac">Change</label><select class="in" id="cb-ac"><option value="default">Make it the default for payouts</option><option value="A">Active</option><option value="S">Suspended</option><option value="D">Deleted</option></select></div>`, 'Save for a checker', () => {
      const v = $('cb-ac').value, def = v === 'default';
      return cbReq(id, bank ? { action: def ? 'default_bank' : 'bank_status', status: def ? '' : v, bank: { bank: '', ifsc: a, account: b } } : { action: def ? 'default_dp' : 'dp_status', status: def ? '' : v, dp: { depository: '', dp_id: a, ben_id: b } });
    });
  },
  cbAddAcct: async (id, el) => {
    const p = cbParty(id), bank = el.dataset.k === 'bank';
    let opts = '';
    if (p.ucc) { try { const pr = await get(`/v1/cbrics/propose?ucc=${cbEnc(p.ucc)}`); opts = bank ? pr.banks.map(b => `<option value="${esc(b.ifsc + '-' + String(b.account).slice(-4))}">${esc(b.bank)} ${esc(b.ifsc)} ${esc(b.account)}</option>`).join('') : pr.dps.map(d => `<option value="${esc((d.dp_id || '') + d.ben_id)}">${esc(d.depository)} ${esc((d.dp_id || '') + d.ben_id)}</option>`).join(''); } catch (e) { toast(e.message, true); return; } }
    const body = p.ucc ? `<div class="fld full"><label for="cb-na">From the client’s record</label><select class="in" id="cb-na">${opts}</select></div>`
      : bank ? '<div class="fld"><label for="cb-nb">Bank</label><input class="in" id="cb-nb"></div><div class="fld"><label for="cb-ni">IFSC</label><input class="in mono" id="cb-ni"></div><div class="fld"><label for="cb-nn">Account number (empty: interbank)</label><input class="in mono" id="cb-nn"></div>'
        : '<div class="fld full"><label for="cb-na">Demat account (16 characters)</label><input class="in mono" id="cb-na"></div>';
    confirmModal(bank ? 'Add a bank account' : 'Add a demat account', id, body + '<div class="fld full"><label class="check"><input type="checkbox" id="cb-nd"> Default for payouts</label></div>', 'Save for a checker', () => {
      const def = $('cb-nd').checked;
      if (bank) return cbReq(id, p.ucc ? { action: 'add_bank', bank_key: $('cb-na').value, bank: { bank: '', ifsc: '', default: def } } : { action: 'add_bank', bank: { bank: $('cb-nb').value.trim(), ifsc: $('cb-ni').value.trim().toUpperCase(), account: $('cb-nn').value.trim(), default: def } });
      return cbReq(id, { action: 'add_dp', demat: $('cb-na').value.trim().toUpperCase(), dp: { depository: '', ben_id: '', default: def } });
    });
  },
  cbFind: () => {
    modal(mhd('Find a registered participant', 'CBRICS’s participant list, by code or PAN. Keep the one the deal is with; link it to a client if the client has its own CBRICS account.') + `<div class="bd"><div class="form"><div class="fld"><label for="cf-c">Participant code</label><input class="in mono" id="cf-c"></div><div class="fld"><label for="cf-p">PAN</label><input class="in mono" id="cf-p"></div></div><div class="pad"><button class="btn" id="cf-go">Find</button></div><div id="cf-res"></div></div>`);
    $('cf-go').onclick = async () => {
      try {
        const fs = await post('/v1/cbrics/participants/find', { code: $('cf-c').value.trim(), pan: $('cf-p').value.trim() });
        $('cf-res').innerHTML = tbl([['Code'], ['Name'], ['PAN'], ['Status'], ['Client (if it is one)'], ['']], fs.map((x, i) => `<tr><td class="mono">${esc(x.loginId)}</td><td>${esc(x.firstName)}</td><td class="mono">${esc(x.panNo || '')}</td><td>${esc({ 1: 'Active', 2: 'Inactive', 3: 'Suspended', 4: 'Unregistered' }[x.actualStatus] || x.actualStatus)}</td><td><input class="in sm mono" id="cf-u${i}" placeholder="UCC"></td><td><button class="btn sm" data-i="${i}">Keep</button></td></tr>`), { empty: 'None found.' });
        $('cf-res').querySelectorAll('button[data-i]').forEach(b => b.onclick = async () => { const x = fs[+b.dataset.i]; try { await post('/v1/cbrics/participants', { code: x.loginId, kind: 'Registered', name: x.firstName, pan: x.panNo || '', ucc: $('cf-u' + b.dataset.i).value.trim().toUpperCase(), state: { 1: 'Active', 2: 'Inactive', 3: 'Suspended' }[x.actualStatus] || 'Active' }); toast(x.loginId + ' kept'); closeModal(); render({ keep: true }); } catch (e) { toast(e.message, true); } });
      } catch (e) { $('cf-res').innerHTML = errHTML(e); }
    };
  },
  cbImport: () => act(() => post('/v1/cbrics/participants/import', {}), r => `${r.added} participant${r.added === 1 ? '' : 's'} taken in from CBRICS`),
});
