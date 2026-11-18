'use strict';
/* Ledgerline console: debt deals on NSE's RFQ platform (screen "rfq",
   Investments → Bonds (RFQ deals)). Deals are read from NSE; the firm names
   the client of each (maker), a checker sends it; amendments of confirmed
   deals are proposed or decided here; confirmed deals wait to be settled by
   hand. Posting RFQs and negotiating stay on NSE's screen. Loaded after
   app.js. */

Object.assign(UI, { rfqTab: 'deals' });
const RFQ_ST = { A: 'warn', C: 'good', N: 'nt', R: 'bad', E: 'bad' };
const rfqMk = () => has('Ops maker', 'Ops checker');
const rfqCk = () => has('Ops checker');
const rfqEnc = encodeURIComponent;
const rfqMe = () => S.me?.actor || '';
const rfqDeal = id => (C.rfq?.deals || []).find(d => d.id === id);
const rfqSide = s => `${s.deal_type === 'D' ? 'Own account' : s.ucc ? `<button class="linkish mono" data-act="client" data-id="${esc(s.ucc)}">${esc(s.ucc)}</button> ${esc(s.name || '')}` : `<span class="mono">${esc(s.client_code || 'not named')}</span>`} <span class="pill ${s.side === 'B' ? 'acc' : 'warn'}">${s.side === 'B' ? 'Buys' : 'Sells'}</span>`;
const rfqWhen = s => { if (!s) return '—'; const t = new Date(s); return isNaN(t) ? esc(s) : esc(t.toLocaleString('en-GB', { timeZone: 'Asia/Kolkata', day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit', hour12: false }).replace(',', '')); };

V.rfq = {
  load: () => get(`/v1/rfq?date=${S.date}`),
  html: d => {
    const st = d.state || {}, ds = d.deals || [];
    const toDo = ds.filter(x => x.action === 'propose' || x.action === 'decide').length, waiting = ds.filter(x => x.proposal || x.amend).length;
    const settle = ds.filter(x => x.action === 'settle'), decide = (d.amendments || []).filter(a => a.ours === 'decide' && a.status === 'PP').length;
    const conn = !d.set || !d.usable ? `<div class="note warn">NSE RFQ (debt) is not set up. Admin enters the participant code, API user and password in <a href="#connections" data-act="go" data-id="connections">Connections</a>.</div>`
      : `<div class="note">${pill(d.live ? 'Live' : 'UAT', d.live ? 'good' : 'acc')} participant ${esc(d.member)} · ${d.read ? `reads every ${d.every} min while the market is open${st.open ? ` (${esc(st.open)}–${esc(st.close)})` : ''}` : 'reading on a schedule is off'} · last read ${rfqWhen(st.last_read)}${st.last_callback ? ` · last notification ${rfqWhen(st.last_callback)}` : ''}${st.last_error ? `<div class="neg small wrap">${esc(st.last_error)}</div>` : ''}</div>`;
    return ph('Bonds (RFQ deals)', 'Corporate bond, CP and CD deals on NSE’s RFQ platform. A traded deal needs its client named and the consideration agreed before NSE’s cut-off: the maker does it here and another checker sends it. Confirmed deals are settled through NSE Clearing and billed by hand for now.',
      `<button class="btn" data-act="dl" data-id="/v1/rfq/deals.csv?date=${S.date}" data-name="rfq_deals_${S.date}.csv">Deals settling ${fmtDate(S.date)} (CSV)</button>${rfqMk() && d.usable ? '<button class="btn pri" data-act="rfqRead">Read from NSE now</button>' : ''}`) + conn +
      `<div class="kpis">${kpi('To confirm', toDo, 'name the client and agree the consideration', toDo ? 'warn' : '')}${kpi('Waiting for a checker', waiting, 'confirmations and amendments', waiting ? 'warn' : '')}${kpi('To settle', settle.length, cmp(sum(settle, x => num(x.consideration))) + ' consideration', settle.length ? 'acc' : '')}${kpi('Amendments to decide', decide, 'from counterparties', decide ? 'warn' : '')}</div>
      <div class="panel"><div class="hd">${segBtns('rfqTab', [['deals', 'Deals'], ['amend', 'Amendments'], ['setup', 'Connection & timings']])}</div></div>` +
      ({ deals: rfqDeals, amend: rfqAmends, setup: rfqSetup }[UI.rfqTab] || rfqDeals)(d);
  }
};

function rfqActs(x) {
  const b = [], p = x.proposal, a = x.amend;
  if (p) {
    if (rfqCk() && p.maker !== rfqMe()) b.push(`<button class="btn sm pri" data-act="rfqSend" data-id="${esc(x.id)}">Send to NSE</button>`);
    if (rfqMk()) b.push(`<button class="btn sm" data-act="rfqDiscard" data-id="${esc(x.id)}">Discard</button>`);
  } else if (rfqMk() && x.action === 'propose') b.push(`<button class="btn sm pri" data-act="rfqPropose" data-id="${esc(x.id)}">Confirm…</button>`);
  else if (rfqMk() && x.action === 'decide') b.push(`<button class="btn sm pri" data-act="rfqAccept" data-id="${esc(x.id)}">Accept…</button><button class="btn sm" data-act="rfqReject" data-id="${esc(x.id)}">Reject…</button>`);
  if (a) {
    if (rfqCk() && a.maker !== rfqMe()) b.push(`<button class="btn sm pri" data-act="rfqAmendSend" data-id="${esc(x.id)}">Send amendment</button>`);
    if (rfqMk()) b.push(`<button class="btn sm" data-act="rfqAmendDiscard" data-id="${esc(x.id)}">Discard amendment</button>`);
  } else if (rfqMk() && x.status === 'C' && x.settle === 'To settle') b.push(`<button class="btn sm" data-act="rfqAmend" data-id="${esc(x.id)}">Amend…</button>`);
  if (rfqMk() && x.action === 'settle') b.push(`<button class="btn sm" data-act="rfqSettled" data-id="${esc(x.id)}">Mark settled</button>`);
  b.push(`<button class="btn sm" data-act="rfqHistory" data-id="${esc(x.id)}">History</button>`);
  return `<div class="split">${b.join('')}</div>`;
}

function rfqWaiting(x) {
  const p = x.proposal, a = x.amend, out = [];
  if (p) out.push(`<div class="small wrap"><b>${esc({ propose: 'Propose', accept: 'Accept', reject: 'Reject' }[p.action])}</b> by ${esc(p.maker)}: ${(p.allocs || []).map(l => `${esc(l.ucc)} ${nf(l.qty)}`).join(', ') || (p.action === 'reject' ? esc(p.remarks || '') : 'own account')}${p.action === 'propose' ? ` · ${esc(p.price)} · AI ${inr(p.accrued_interest, 2)} · ${inr(p.consideration, 2)}` : ''}${p.error ? `<div class="neg">${esc(p.error)}</div>` : ''}</div>`);
  if (a) out.push(`<div class="small wrap"><b>${a.type === 'C' ? 'Cancel' : 'Modify'}</b> by ${esc(a.maker)}: ${esc(a.reason)}${a.error ? `<div class="neg">${esc(a.error)}</div>` : ''}</div>`);
  return out.join('');
}

function rfqDeals(d) {
  const rows = (d.deals || []).map(x => `<tr>
    <td class="n">${esc(x.trade_no || '—')}<div class="dim small">${esc(x.rfq)} · ${esc(x.id)}</div><div class="dim small">${fmtDate(x.date)}</div></td>
    <td><span class="mono">${esc(x.isin)}</span>${x.category ? ' ' + pill(x.category, 'nt') : ''}<div class="dim small wrap">${esc(x.description || '')}</div></td>
    <td>${(x.sides || []).map(rfqSide).join('<br>')}<div class="dim small">with ${esc(x.counterparty || '—')}</div></td>
    <td class="r">${nf(x.qty)}<div class="dim small">₹${esc(x.value)} Cr</div></td>
    <td class="r">${esc(x.yield_type || '')} ${esc(x.yield)}%<div class="dim small">${num(x.price) ? 'price ' + esc(x.price) : 'no price yet'}</div></td>
    <td class="r">${num(x.consideration) ? inr(x.consideration, 2) : '—'}<div class="dim small">AI ${inr(x.accrued_interest, 2)}</div></td>
    <td class="n">${fmtDate(x.settlement_date)}<div class="dim small">T+${esc(x.settlement_type || '?')}</div></td>
    <td>${pill(x.status_text, RFQ_ST[x.status] || 'nt')}${x.confirm_text ? `<div class="dim small">${esc(x.confirm_text)}</div>` : ''}<div class="small wrap">${esc(x.needs)}</div>${x.settle_note ? `<div class="dim small wrap">${esc(x.settle_note)}</div>` : ''}${rfqWaiting(x)}</td>
    <td>${rfqActs(x)}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Deals</h3><small>dealt or settling ${fmtDate(d.date)}, and every deal still open</small></div>` +
    tbl([['Deal'], ['Bond'], ['Firm’s side'], ['Bonds', 'r'], ['Yield', 'r'], ['Consideration', 'r'], ['Settles'], ['Status'], ['']], rows, { tall: true, empty: d.usable ? 'No deals yet. Deals appear once traded on NSE’s RFQ platform.' : 'Set up the connection to read deals.' }) + '</div>';
}

function rfqAmends(d) {
  const ck = rfqCk();
  const rows = (d.amendments || []).map(a => `<tr>
    <td class="n">${esc(a.id)}<div class="dim small">${esc(a.trade_no)} · ${esc(a.deal)}</div></td>
    <td>${a.type === 'C' ? 'Cancellation' : 'Modification'}<div class="dim small">${esc(a.reason)} · ${esc(a.reason_text)}</div>${a.proposer_remarks ? `<div class="small wrap">“${esc(a.proposer_remarks)}”</div>` : ''}</td>
    <td><span class="mono">${esc(a.isin)}</span><div class="dim small">buyer ${esc(a.buyer_client || a.buyer_ae)} · seller ${esc(a.seller_client || a.seller_ae)}</div></td>
    <td class="small">${a.type === 'M' ? `AI ${inr(a.accrued_interest, 2)} → ${inr(a.mod_accrued_interest, 2)}<br>${inr(a.consideration, 2)} → ${inr(a.mod_consideration, 2)}<br>${fmtDate(a.settlement_date)} → ${fmtDate(a.mod_settlement_date)}` : `${inr(a.consideration, 2)} · ${fmtDate(a.settlement_date)}`}</td>
    <td class="small">${esc(a.proposer_ae)}${a.ours === 'proposed' ? ' (the firm)' : ''}<div class="dim">to ${esc(a.counter_ae)}${a.ours === 'decide' ? ' (the firm)' : ''}</div></td>
    <td>${pill(a.status_text, a.status === 'EA' ? 'good' : a.status === 'CR' ? 'bad' : 'warn')}${a.decided_by ? `<div class="dim small">${esc(a.decided_by)}</div>` : ''}${a.counter_remarks ? `<div class="small wrap">${esc(a.counter_remarks)}</div>` : ''}</td>
    <td>${ck && a.ours === 'decide' && a.status === 'PP' ? `<div class="split"><button class="btn sm pri" data-act="rfqDecide" data-id="${esc(a.id)}" data-a="1">Approve</button><button class="btn sm" data-act="rfqDecide" data-id="${esc(a.id)}" data-a="0">Reject…</button></div>` : ''}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Amendments</h3><small>modifications and cancellations of confirmed deals, the last 30 days and every one waiting</small></div>` +
    tbl([['Amendment'], ['What'], ['Deal'], ['Change'], ['Proposed by'], ['Status'], ['']], rows, { tall: true, empty: 'No amendments.' }) + '</div>';
}

function rfqSetup(d) {
  const st = d.state || {};
  return `<div class="grid g2"><div class="panel"><div class="hd"><h3>Connection</h3></div><div class="bd">
      <div class="kv"><div><small>Participant</small><b>${esc(d.member || '—')}</b></div><div><small>Environment</small><b>${d.set ? (d.live ? 'Live' : 'UAT') : '—'}</b></div><div><small>Broker on RFQ</small><b>${st.checked ? (st.broker ? 'Yes' + (st.broker_type ? ' (' + esc(st.broker_type) + ')' : '') : 'No') : 'not tested'}</b></div><div><small>Notifications</small><b>${st.callbacks || 0}</b></div></div>
      <p class="small">Callback address to give NSE: <span class="mono">https://&lt;this server&gt;${esc(d.callback)}</span>. NSE adds /v1/notification. ${d.callback_on ? (d.callback_key ? 'Signed notifications are checked with the signing key.' : 'No signing key entered: only the token in the address admits a notification.') : 'Enter a callback token in Connections to take notifications.'} A notification only makes Ledgerline read from NSE at the next minute.</p>
      <p class="dim small" style="margin-bottom:0">The API user must be the firm’s own: a second sign-in with the same login ends the first session on NSE.</p></div></div>
    <div class="panel"><div class="hd"><h3>Market timings ${st.timings_date ? fmtDate(st.timings_date) : ''}</h3></div>${tbl([['Issue type'], ['Opens (T+0 · T+1)'], ['RFQ and quotes until'], ['Consideration until']], (st.timings || []).map(t => `<tr><td>${esc(t.type)}</td><td class="n">${esc(t.start_t0)} · ${esc(t.start_t1)}</td><td class="n">${esc(t.yield_cut_t0)} · ${esc(t.yield_cut_t1)}</td><td class="n">${esc(t.price_cut_t0)} · ${esc(t.price_cut_t1)}</td></tr>`), { empty: 'Read once a day when the first read of the day runs.' })}</div></div>`;
}

// rfqAllocRows is the client rows of a confirmation: one client, or a split.
function rfqAllocRows(x, rows) {
  return rows.map((r, i) => `<div class="grid g3 rfq-al"><div class="fld"><label for="rfq-u${i}">Client (UCC)</label><input class="in mono" id="rfq-u${i}" value="${esc(r.ucc || '')}"></div>
    <div class="fld"><label for="rfq-q${i}">Bonds</label><input class="in" id="rfq-q${i}" type="number" min="1" value="${esc(r.qty || '')}"></div>
    <div class="fld"><label for="rfq-c${i}">RFQ client code (empty: as before, or the UCC)</label><input class="in mono" id="rfq-c${i}" value="${esc(r.code || (r.ucc && C.rfq.codes?.[r.ucc]) || '')}"></div></div>`).join('');
}

function rfqAllocsOf() {
  return [...document.querySelectorAll('.rfq-al')].map((_, i) => ({ ucc: $('rfq-u' + i).value.trim().toUpperCase(), qty: parseInt($('rfq-q' + i).value, 10) || 0, code: $('rfq-c' + i).value.trim() })).filter(a => a.ucc);
}

function rfqAllocBlock(x, own) {
  const first = (x.sides || [])[0] || {};
  return `<div id="rfq-als">${rfqAllocRows(x, [{ ucc: first.ucc || '', qty: x.qty }])}</div>
    <div class="tools"><button class="btn sm" type="button" id="rfq-add">Split: add a client</button>${own ? '<span class="dim small">Leave the client empty for the firm’s own deal.</span>' : ''}</div>`;
}

function rfqBindAdd(x) {
  const b = $('rfq-add'); if (!b) return;
  b.onclick = () => { const n = document.querySelectorAll('.rfq-al').length; const rows = [...Array(n)].map((_, i) => ({ ucc: $('rfq-u' + i).value, qty: $('rfq-q' + i).value, code: $('rfq-c' + i).value })); rows.push({}); $('rfq-als').innerHTML = rfqAllocRows(x, rows); };
}

Object.assign(A, {
  rfqRead: () => act(() => post('/v1/rfq/read', { date: S.date }), r => `${r.deals} deal(s) read, ${r.new} new, ${r.confirmed} confirmed · ${r.amendments} amendment(s)${(r.problems || []).length ? ' · ' + r.problems[0] : ''}`),
  rfqPropose: id => {
    const x = rfqDeal(id); if (!x) return;
    const both = (x.sides || []).length > 1, own = (x.sides || []).some(s => s.deal_type === 'D');
    confirmModal('Confirm the deal for the client', `${esc(x.trade_no || x.id)} · ${esc(x.isin)} · ${nf(x.qty)} bonds (₹${esc(x.value)} Cr) at ${esc(x.yield_type)} ${esc(x.yield)}%, settling ${fmtDate(x.settlement_date)}. The consideration is face value × price ÷ 100 + accrued interest. Another checker sends it to NSE.`,
      `${both ? `<div class="fld"><label for="rfq-role">The firm’s side this is for</label><select class="in" id="rfq-role">${x.sides.map(s => `<option value="${s.role}">${s.role === 'I' ? 'Initiator' : 'Responder'} (${s.side === 'B' ? 'buyer' : 'seller'})</option>`).join('')}</select></div>` : ''}
      ${rfqAllocBlock(x, own)}
      <div class="grid g3"><div class="fld"><label for="rfq-p">Price (per ₹100 face value)</label><input class="in mono" id="rfq-p" inputmode="decimal" value="${num(x.price) ? esc(x.price) : ''}"></div>
      <div class="fld"><label for="rfq-ai">Accrued interest (₹, whole deal)</label><input class="in mono" id="rfq-ai" inputmode="decimal"></div>
      <div class="fld"><label for="rfq-cn">Consideration (₹, empty: worked out)</label><input class="in mono" id="rfq-cn" inputmode="decimal"></div>
      <div class="fld"><label for="rfq-cm">Calculation method</label><select class="in" id="rfq-cm"><option value="O" ${x.calc_method !== 'M' ? 'selected' : ''}>O · other</option><option value="M" ${x.calc_method === 'M' ? 'selected' : ''}>M · money market</option></select></div>
      <div class="fld"><label for="rfq-pc">Put or call date (for YTP or YTC)</label><input class="in" type="date" id="rfq-pc" value="${esc(x.put_call_date || '')}"></div>
      <div class="fld"><label for="rfq-dcc">Day count for NSE’s calculator</label><select class="in" id="rfq-dcc"><option value="1">Actual/Actual</option><option value="2">30/360 European</option><option value="3">30/360 American</option><option value="4">Actual/365</option></select></div></div>
      <div class="tools"><button class="btn sm" type="button" id="rfq-calc">Price from NSE’s calculator</button><span class="dim small" id="rfq-calcout"></span></div>
      <div class="fld"><label for="rfq-rm">Remarks for NSE (optional)</label><input class="in" id="rfq-rm" maxlength="100"></div>`, 'Propose for a checker',
      () => act(() => post(`/v1/rfq/deals/${rfqEnc(id)}/proposal`, { action: 'propose', role: $('rfq-role')?.value || '', allocs: rfqAllocsOf(), price: $('rfq-p').value.trim(), accrued_interest: $('rfq-ai').value.trim(), consideration: $('rfq-cn').value.trim(), calc_method: $('rfq-cm').value, put_call_date: $('rfq-pc').value, remarks: $('rfq-rm').value.trim() }), 'Proposed: a checker sends it'));
    rfqBindAdd(x);
    $('rfq-calc').onclick = async () => { try { const r = await post(`/v1/rfq/deals/${rfqEnc(id)}/calc-price`, { ai_dcc: parseInt($('rfq-dcc').value, 10) }); $('rfq-p').value = r.price; if (r.put_call_date) $('rfq-pc').value = r.put_call_date; $('rfq-cm').value = r.calc_method === 'M' ? 'M' : 'O'; $('rfq-calcout').textContent = `NSE: price ${r.price}, accrued interest ${r.accrued_interest} (check its basis before entering it)`; } catch (e) { $('rfq-calcout').textContent = e.message; } };
  },
  rfqAccept: id => {
    const x = rfqDeal(id); if (!x) return;
    confirmModal('Accept the counterparty’s proposal', `${esc(x.counterparty)} proposed ${inr(x.consideration, 2)} (price ${esc(x.price)}, accrued interest ${inr(x.accrued_interest, 2)}) for ${nf(x.qty)} bonds of ${esc(x.isin)}. Name the client, or split it across clients. Another checker sends it.`,
      rfqAllocBlock(x, (x.sides || []).some(s => s.deal_type === 'D')), 'Accept for a checker',
      () => act(() => post(`/v1/rfq/deals/${rfqEnc(id)}/proposal`, { action: 'accept', allocs: rfqAllocsOf() }), 'Accepted: a checker sends it'));
    rfqBindAdd(x);
  },
  rfqReject: id => confirmModal('Reject the counterparty’s proposal', 'The counterparty proposes again. Another checker sends the rejection.', '<div class="fld"><label for="rfq-rr">Remarks (kept in Ledgerline)</label><input class="in" id="rfq-rr" maxlength="100"></div>', 'Reject for a checker',
    () => act(() => post(`/v1/rfq/deals/${rfqEnc(id)}/proposal`, { action: 'reject', remarks: $('rfq-rr').value.trim() }), 'Rejection waiting for a checker')),
  rfqSend: id => { const x = rfqDeal(id), p = x?.proposal; if (!p) return; confirmModal('Send to NSE', `${esc({ propose: 'Propose', accept: 'Accept', reject: 'Reject' }[p.action])} deal ${esc(x.trade_no || x.id)}, proposed by ${esc(p.maker)}. Ledgerline reads the deal from NSE again and checks each client (active, not debarred) first.`, rfqWaiting({ proposal: { ...p, error: '' } }), 'Send to NSE',
    () => act(() => post(`/v1/rfq/deals/${rfqEnc(id)}/send`), r => `Sent: ${r.status === 'C' ? 'confirmed' : 'NSE status ' + (r.confirm || r.status)}`)); },
  rfqDiscard: id => confirmModal('Discard the waiting confirmation', 'Nothing has gone to NSE. The deal waits to be confirmed again.', '<div class="fld"><label for="rfq-dn">Why</label><input class="in" id="rfq-dn"></div>', 'Discard',
    () => act(() => post(`/v1/rfq/deals/${rfqEnc(id)}/discard`, { note: $('rfq-dn').value.trim() }), 'Discarded'), true),
  rfqAmend: id => {
    const x = rfqDeal(id); if (!x) return;
    const both = (x.sides || []).length > 1, md = C.rfq.mod_reasons || [], cn = C.rfq.canc_reasons || [];
    const opts = t => (t === 'M' ? md : cn).map(r => `<option value="${esc(r.code)}">${esc(r.code)} · ${esc(r.text)}</option>`).join('');
    confirmModal('Amend a confirmed deal', `${esc(x.trade_no || x.id)}: a modification or cancellation goes to the counterparty to approve. Another checker sends it.`,
      `<div class="grid g2"><div class="fld"><label for="rfq-at">Change</label><select class="in" id="rfq-at"><option value="C">Cancel the deal</option><option value="M">Modify the deal</option></select></div>
      <div class="fld"><label for="rfq-ar">Reason</label><select class="in" id="rfq-ar">${opts('C')}</select></div></div>
      <div class="grid g3" id="rfq-am" hidden><div class="fld"><label for="rfq-ay">Yield type</label><select class="in" id="rfq-ay">${['YTM', 'YTP', 'YTC'].map(y => `<option ${x.yield_type === y ? 'selected' : ''}>${y}</option>`).join('')}</select></div>
      <div class="fld"><label for="rfq-aa">Accrued interest (₹)</label><input class="in mono" id="rfq-aa" value="${esc(x.accrued_interest)}"></div>
      <div class="fld"><label for="rfq-as">Settlement</label><select class="in" id="rfq-as"><option value="0" ${x.settlement_type === '0' ? 'selected' : ''}>T+0</option><option value="1" ${x.settlement_type === '1' ? 'selected' : ''}>T+1</option></select></div></div>
      ${both ? '<div class="fld"><label for="rfq-ab">Asked for by</label><select class="in" id="rfq-ab"><option value="B">The buyer client</option><option value="S">The seller client</option></select></div>' : ''}
      <div class="fld"><label for="rfq-ak">Remarks for the counterparty</label><input class="in" id="rfq-ak" maxlength="200"></div>`, 'Propose for a checker',
      () => act(() => post(`/v1/rfq/deals/${rfqEnc(id)}/amend`, { type: $('rfq-at').value, reason: $('rfq-ar').value, yield_type: $('rfq-ay').value, accrued_interest: $('rfq-aa').value.trim(), settlement_type: $('rfq-as').value, remarks: $('rfq-ak').value.trim(), proposed_by_side: $('rfq-ab')?.value || '' }), 'Amendment waiting for a checker'));
    $('rfq-at').onchange = () => { const t = $('rfq-at').value; $('rfq-ar').innerHTML = opts(t); $('rfq-am').hidden = t !== 'M'; };
  },
  rfqAmendSend: id => { const x = rfqDeal(id), a = x?.amend; if (!a) return; confirmModal('Send the amendment to NSE', `${a.type === 'C' ? 'Cancel' : 'Modify'} deal ${esc(x.trade_no || x.id)} (${esc(a.reason)}), proposed by ${esc(a.maker)}. The counterparty approves or rejects it on NSE.`, '', 'Send to NSE',
    () => act(() => post(`/v1/rfq/deals/${rfqEnc(id)}/amend/send`), r => `Amendment ${r.id} sent`)); },
  rfqAmendDiscard: id => act(() => post(`/v1/rfq/deals/${rfqEnc(id)}/discard`, { what: 'amend' }), 'Amendment discarded'),
  rfqDecide: (id, el) => {
    const ok = el.dataset.a === '1', a = (C.rfq.amendments || []).find(x => x.id === id); if (!a) return;
    confirmModal(ok ? 'Approve the amendment' : 'Reject the amendment', `${esc(a.proposer_ae)} asks to ${a.type === 'C' ? 'cancel' : 'modify'} deal ${esc(a.trade_no)}: ${esc(a.reason_text)}. ${ok ? (a.type === 'C' ? 'The deal is then cancelled: nothing is settled.' : 'The new accrued interest, consideration and settlement apply.') : 'The deal stays as it is.'}`,
      `<div class="fld"><label for="rfq-dr">Remarks for the counterparty${ok ? ' (optional)' : ''}</label><input class="in" id="rfq-dr" maxlength="200"></div>`, ok ? 'Approve on NSE' : 'Reject on NSE',
      () => act(() => post(`/v1/rfq/amendments/${rfqEnc(id)}/decide`, { approve: ok, remarks: $('rfq-dr').value.trim() }), ok ? 'Approved' : 'Rejected'), !ok);
  },
  rfqSettled: id => { const x = rfqDeal(id); if (!x) return; confirmModal('Mark the deal settled', `${esc(x.trade_no || x.id)}: ${inr(x.consideration, 2)} settling ${fmtDate(x.settlement_date)}. Record the pay-in and the bill first: Ledgerline does not post debt deals to the ledger.`, '<div class="fld"><label for="rfq-sn">How it was settled (pay-in reference, bill number)</label><input class="in" id="rfq-sn"></div>', 'Mark settled',
    () => act(() => post(`/v1/rfq/deals/${rfqEnc(id)}/settled`, { note: $('rfq-sn').value.trim() }), 'Marked settled')); },
  rfqHistory: id => { const x = rfqDeal(id); if (!x) return; modal(mhd('Deal ' + esc(x.trade_no || x.id), `${esc(x.isin)} · ${esc(x.status_text)}`) + `<div class="bd">${tbl([['When'], ['Who'], ['What']], (x.history || []).slice().reverse().map(h => `<tr><td class="n small">${rfqWhen(h.at)}</td><td class="small">${esc(h.by)}</td><td class="small wrap">${esc(h.what)}</td></tr>`), { empty: 'Nothing yet.' })}${(x.splits || []).length ? '<h4>Trade splits</h4>' + tbl([['Client'], ['Bonds', 'r'], ['Accrued interest', 'r'], ['Consideration', 'r']], x.splits.map(s => `<tr><td class="mono">${esc(s.ucc || s.client_code)}</td><td class="r">${nf(s.qty)}</td><td class="r">${inr(s.accrued_interest, 2)}</td><td class="r">${inr(s.consideration, 2)}</td></tr>`)) : ''}</div>`); },
});
