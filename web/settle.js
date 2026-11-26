'use strict';
/* Ledgerline console: on Operations → Settlement (V.settlement), the
   morning's internal shortages and the funds to keep by 12:00 noon for
   NSE Clearing's auction, account closures on the client's request (3
   working days) and securities transfer requests (1 or 3 working days).

   Server: internal/ops/nclshortage.go, acctclose.go, payoutrecon.go;
   internal/api/settle.go. Loaded after app.js; it wraps V.settlement and
   replaces the close-out of a short delivery (A.shortSettle). Names are
   prefixed sx. */

const sxBase = V.settlement;
V.settlement = {
  load: async () => {
    const soft = p => get(p).catch(e => ({ error: e.message }));
    const [d, is, cl, tr] = await Promise.all([sxBase.load(), soft(`/v1/settlement/internal-shortages?date=${S.date}`), soft('/v1/settlement/closures'), soft('/v1/settlement/transfers')]);
    d.intshort = is; d.closures = Array.isArray(cl) ? cl : []; d.transfers = Array.isArray(tr) ? tr : [];
    return d;
  },
  html: d => {
    const is = d.intshort || {}, open = d.closures.filter(x => x.status === 'Open' || x.status === 'Waiting for client'), late = open.filter(x => x.due < S.date);
    const trOpen = d.transfers.filter(x => x.status === 'Open');
    const k = kpi('Internal shortages', (is.rows || []).length ? inr(is.fund) : '—', (is.rows || []).length ? `keep by 12:00 on ${fmtDate(is.pay_in)}${is.ncl_loaded ? ' · NSE Clearing' : ' · estimate'}` : 'none found', (is.rows || []).length ? 'warn' : 'good') +
      kpi('Account closures', open.length, late.length ? `${late.length} past 3 working days` : 'open', late.length ? 'bad' : open.length ? 'warn' : 'good');
    return sxBase.html(d).replace('<div class="kpis">', '<div class="kpis">' + k) + sxIntShort(is) + sxClosures(d.closures) + sxTransfers(d.transfers, trOpen);
  }
};

function sxIntShort(v) {
  if (v.error) return '';
  const rows = (v.rows || []).map(r => `<tr><td><b>${esc(r.symbol || r.isin)}</b><div class="dim small mono">${esc(r.isin)} · ${esc(r.clearing)}</div></td><td class="small wrap">${(r.sellers || []).map(esc).join('<br>') || '—'}</td><td class="small wrap">${(r.buyers || []).map(esc).join('<br>') || '—'}</td><td class="r">${nf(r.internal)}</td><td class="r">${nf(r.price, 2)}</td><td class="r">${inr(r.valuation, 2)}</td><td class="r">${r.ncl_qty ? nf(r.ncl_qty) + ' · ' + inr(r.ncl_amount, 2) : '<span class="dim">—</span>'}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Internal shortages for NSE Clearing's auction</h3><small>Trades of ${fmtDate(v.trade_date)} · due ${esc(v.due || '')}</small></div>
    <div class="bd"><p class="dim small" style="margin:0">A client's short delivery that another client's purchase of the same security hides (on either exchange cleared at NSE Clearing). NSE Clearing debits the valuation amount (settlement price plus 20%) at 12:00 noon on the settlement day and auctions the shares; the buyers get the auctioned shares, not cash. Without the funds there is no auction. Load NSE Clearing's CM_SA_S report on Exchange files to replace the estimate.</p>
    ${(v.notes || []).map(n => `<div class="note warn" style="margin-top:8px">${esc(n)}</div>`).join('')}</div>
    ${tbl([['Security'], ['Short sellers'], ['Buyers it hides'], ['Internal', 'r'], ['Settlement price', 'r'], ['Valuation (+20%)', 'r'], ['NSE Clearing', 'r']], rows, { empty: 'No internal shortage found for this trade date.', foot: rows.length ? `<tr><td colspan="5"><b>Keep in the settlement account by 12:00</b></td><td class="r"><b>${inr(v.fund, 2)}</b></td><td class="r">${v.ncl_loaded ? esc(v.ncl_file || '') : '<span class="dim">estimate</span>'}</td></tr>` : '' })}</div>`;
}

const SX_ST = { Open: 'warn', 'Waiting for client': 'nt', Closed: 'good', Rejected: 'nt', Done: 'good' };

function sxClosures(xs) {
  const can = has('Ops maker', 'Ops checker', 'Compliance officer'), chk = has('Ops checker', 'Compliance officer');
  const rows = xs.slice(0, 50).map(x => {
    const acts = [];
    if (x.status === 'Open' || x.status === 'Waiting for client') {
      if (can) acts.push(`<button class="btn sm" data-act="sxClCheck" data-id="${esc(x.id)}">Check again</button>`);
      if (chk && !(x.holds || []).length) acts.push(`<button class="btn sm pri" data-act="sxClDone" data-id="${esc(x.id)}">Close the account</button>`);
      if (chk) acts.push(`<button class="btn sm" data-act="sxClReject" data-id="${esc(x.id)}">Reject</button>`);
    }
    const steps = `<div class="small">Paid out: ${x.paid_out_on ? fmtDate(x.paid_out_on) : '<span class="dim">not yet</span>'}</div><div class="small">Released: ${x.released_on ? fmtDate(x.released_on) : '<span class="dim">not yet</span>'}</div><div class="small">Closed: ${x.closed_on ? fmtDate(x.closed_on) : '<span class="dim">not yet</span>'}</div>`;
    return `<tr><td class="n"><b>${esc(x.id)}</b><div class="dim small">${fmtDate(x.received)} · ${esc(x.source)}</div></td><td><button class="linkish mono" data-act="client" data-id="${esc(x.ucc)}">${esc(x.ucc)}</button> ${esc(x.name)}</td>
      <td class="n ${x.due < S.date && x.status === 'Open' ? 'neg' : ''}">${fmtDate(x.due)}</td><td>${steps}</td><td class="small wrap">${(x.holds || []).map(h => `<div class="warn-t">${esc(h)}</div>`).join('') || (x.reason ? esc(x.reason) : '<span class="dim">—</span>')}</td>
      <td>${pill(x.status, SX_ST[x.status] || 'nt')}</td><td><div class="split" style="flex-wrap:wrap">${acts.join('')}</div></td></tr>`;
  });
  return `<div class="panel"><div class="hd"><h3>Account closures</h3><small>3 working days from the request</small>${can ? `<button class="btn sm pri" data-act="sxClNew" style="margin-left:auto">Closure request</button>` : ''}</div>
    <div class="bd"><p class="dim small" style="margin:0">When a client asks to close the account, record it here: the client is blocked from trading at once and gets an acknowledgement. Pay out the credit balance, release every pledge, unpaid-securities line and clearing allocation, then close. The closure letter goes only once the books show the pay-out and the release done (NSE/INSP/73976 item 1.15).</p></div>
    ${tbl([['Request'], ['Client'], ['Due'], ['Steps'], ['Still to do'], ['Status'], ['']], rows, { empty: 'No closure requests.' })}</div>`;
}

function sxTransfers(xs, open) {
  const can = has('Ops maker', 'Ops checker'), chk = has('Ops checker');
  const rows = xs.slice(0, 50).map(x => `<tr><td class="n"><b>${esc(x.id)}</b><div class="dim small">${fmtDate(x.date)} · ${esc(x.source)}</div></td><td><button class="linkish mono" data-act="client" data-id="${esc(x.ucc)}">${esc(x.ucc)}</button> ${esc(x.name)}</td>
    <td>${esc(x.symbol)} <span class="dim small mono">${esc(x.isin)}</span></td><td class="r">${nf(x.qty)}</td><td class="small">${x.where === 'Clearing' ? 'With the clearing corporation (3 days)' : 'In the firm’s accounts (1 day)'}</td>
    <td class="n ${x.due < S.date && x.status === 'Open' ? 'neg' : ''}">${fmtDate(x.due)}</td><td>${pill(x.status, SX_ST[x.status] || 'nt')}${x.ref ? `<div class="dim small mono">${esc(x.ref)}</div>` : ''}${x.reason ? `<div class="dim small">${esc(x.reason)}</div>` : ''}</td>
    <td>${x.status === 'Open' && chk ? `<button class="btn sm pri" data-act="sxTrDone" data-id="${esc(x.id)}">Done</button> <button class="btn sm" data-act="sxTrReject" data-id="${esc(x.id)}">Reject</button>` : ''}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Transfer requests</h3><small>${open.length} open</small>${can ? `<button class="btn sm pri" data-act="sxTrNew" style="margin-left:auto">Transfer request</button>` : ''}</div>
    <div class="bd"><p class="dim small" style="margin:0">A client may ask for the securities the firm holds for them to be transferred to their demat account: within one working day when they are in the firm's accounts, three when they lie with the clearing member or corporation (SEBI master circular for stock brokers, para 23.1.1(i)).</p></div>
    ${tbl([['Request'], ['Client'], ['Security'], ['Qty', 'r'], ['Where'], ['Due'], ['Status'], ['']], rows, { empty: 'No transfer requests.' })}</div>`;
}

Object.assign(A, {
  shortSettle: id => {
    const x = C.settlement.shortages.find(s => s.id === id);
    confirmModal('Close out ' + id, `${x.ucc} short ${x.qty} ${x.symbol} · ${x.internal} internal, ${x.auction} exchange auction`,
      `<div class="form"><div class="fld"><label for="ss-r">Shares received in the auction</label><input class="in" id="ss-r" type="number" min="0" max="${x.qty}" value="0"></div><div class="fld"><label for="ss-a">Auction price (₹)</label><input class="in" id="ss-a" placeholder="from NSE Clearing's auction rate file"></div>
      <div class="fld"><label for="ss-p">Close-out rate for the rest (₹)</label><input class="in" id="ss-p" value="${esc(x.suggested)}"></div><div class="fld"><label for="ss-d">Date</label><input class="in" type="date" id="ss-d" value="${esc(S.date)}"></div></div>
      <div class="note">Use NSE Clearing's rates (auction rate file, close-out rate file). The firm's buyers get the shares received in the auction; only what was not received is closed out and credited to them. ${esc(x.ucc)} is debited at least the settlement price${num(x.settle_price) ? ' (₹' + nf(x.settle_price, 2) + ')' : ''}: no gain from a short delivery, never more than NSE Clearing charges.</div>`,
      'Close out', () => act(() => post(`/v1/shortages/${id}/settle`, { date: $('ss-d').value, price: $('ss-p').value.trim() || '0', received: +$('ss-r').value || 0, auction_price: $('ss-a').value.trim() || '0' }), 'Short delivery closed out'));
  },
  sxClNew: () => confirmModal('Account closure request', 'The client is blocked from trading at once and gets an acknowledgement. The account is closed within 3 working days.',
    `<div class="form"><div class="fld"><label for="sx-u">Client code</label><input class="in mono" id="sx-u"></div><div class="fld"><label for="sx-d">Received on</label><input class="in" type="date" id="sx-d" value="${esc(S.date)}"></div>
    <div class="fld"><label for="sx-s">How it came</label><select class="in" id="sx-s"><option value="Client">Signed request from the client</option><option value="Online">Online: portal or app with two-factor login</option><option>Registered e-mail</option><option value="Branch">Signed, at a branch</option><option value="Head office">Signed, at head office</option></select></div><div class="fld"><label for="sx-e">E-mailed from (registered e-mail only)</label><input class="in" id="sx-e"></div><div class="fld full"><label for="sx-n">Note (optional)</label><input class="in" id="sx-n"></div></div>
    <div class="note">Requests by SMS, WhatsApp or other messaging apps are not accepted (NSE/INSP/73976 item 1.15(iii)).</div>`,
    'Record', () => act(() => post('/v1/settlement/closures', { ucc: $('sx-u').value.trim(), received: $('sx-d').value, source: $('sx-s').value, note: $('sx-n').value, from_email: $('sx-e').value.trim() }), x => `${x.id} due ${fmtDate(x.due)}`)),
  sxClCheck: id => act(() => post(`/v1/settlement/closures/${id}/check`, {}), 'Checked against the books'),
  sxClDone: id => confirmModal('Close the account · ' + id, 'Every segment is closed and the closure letter is sent, saying the pay-out and the release are done.', '', 'Close the account', () => act(() => post(`/v1/settlement/closures/${id}/complete`, {}), 'Account closed')),
  sxClReject: id => confirmModal('Reject the closure · ' + id, 'The client is told why, and can trade again.', '<div class="form"><div class="fld full"><label for="sx-r">Reason</label><input class="in" id="sx-r"></div></div>', 'Reject', () => act(() => post(`/v1/settlement/closures/${id}/reject`, { reason: $('sx-r').value }), 'Closure rejected')),
  sxTrNew: () => confirmModal('Transfer request', 'The securities go to the client within one working day (three when they lie with the clearing corporation).',
    `<div class="form"><div class="fld"><label for="st-u">Client code</label><input class="in mono" id="st-u"></div><div class="fld"><label for="st-i">ISIN</label><input class="in mono" id="st-i"></div><div class="fld"><label for="st-q">Quantity</label><input class="in" type="number" min="1" id="st-q"></div>
    <div class="fld"><label for="st-w">Where they lie</label><select class="in" id="st-w"><option value="Firm">In the firm's accounts</option><option value="Clearing">With the clearing member or corporation</option></select></div><div class="fld"><label for="st-d">Received on</label><input class="in" type="date" id="st-d" value="${esc(S.date)}"></div>
    <div class="fld full"><label for="st-e">How the client asked (email, letter, call recording reference)</label><input class="in" id="st-e"></div></div>`,
    'Record', () => act(() => post('/v1/settlement/transfers', { ucc: $('st-u').value.trim(), isin: $('st-i').value.trim(), qty: +$('st-q').value || 0, where: $('st-w').value, date: $('st-d').value, evidence: $('st-e').value, source: 'Client' }), x => `${x.id} due ${fmtDate(x.due)}`)),
  sxTrDone: id => confirmModal('Transfer done · ' + id, 'Give the depository transfer’s reference.', '<div class="form"><div class="fld full"><label for="st-ref">Reference</label><input class="in mono" id="st-ref"></div></div>', 'Mark done', () => act(() => post(`/v1/settlement/transfers/${id}/finish`, { done: true, ref: $('st-ref').value }), 'Transfer recorded')),
  sxTrReject: id => confirmModal('Reject the request · ' + id, 'Say why it cannot be met; tell the client.', '<div class="form"><div class="fld full"><label for="st-why">Reason</label><input class="in" id="st-why"></div></div>', 'Reject', () => act(() => post(`/v1/settlement/transfers/${id}/finish`, { done: false, reason: $('st-why').value }), 'Request rejected')),
});
