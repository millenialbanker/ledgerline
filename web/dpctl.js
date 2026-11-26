'use strict';
/* Ledgerline console: DP controls on Depository → Demat accounts & DIS, and
   clients' trading freeze requests on RMS & limits.

   DP controls: DIS books against the requisition slip (or a loss report),
   loose slips, the compliance officer's authorisation for dormant
   accounts, the call to the holder before a slip moves, the
   countersignature above ₹5 lakh, request duties and their dates, the
   daily reconciliation, the blank slip stock and late pool pay-outs.
   Freeze requests: intake with the time received, the 15-minute target,
   Finspot's cancellation of pending orders, open positions within an hour,
   and re-enabling after due diligence.

   Server: internal/ops/dpdis.go, dpreqtat.go, dpctl.go, tradefreeze.go;
   internal/api/dpctl.go. Loaded after app.js; it wraps V.dp and V.rms.
   Names are prefixed dc. */

const dcDP = V.dp, dcRMS = V.rms;
V.dp = {
  load: async () => {
    const d = await dcDP.load();
    d.dc = await get('/v1/dp/controls').catch(() => null);
    C.dc = d.dc;
    return d;
  },
  html: d => dcDP.html(d) + (d.dc ? dcPanel(d.dc) : '')
};
V.rms = {
  load: async () => {
    const d = await dcRMS.load();
    d.tf = await get('/v1/rms/freeze-requests').catch(() => []);
    C.tf = d.tf;
    return d;
  },
  html: d => dcRMS.html(d) + tfPanel(d.tf || [])
};

const dcHM = s => { const t = s ? new Date(s) : null; return t && !isNaN(t) ? t.toLocaleString('en-IN', { timeZone: 'Asia/Kolkata', day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit', hour12: false }) : ''; };

function dcPanel(v) {
  const mk = has('Ops maker', 'Ops checker'), ck = has('Ops checker'), co = has('Compliance officer');
  const books = v.books_awaiting_co.length ? `<h4 class="pad">DIS books for dormant accounts</h4>` + tbl([['Book'], ['Client'], ['Slips'], ['Why'], ['']], v.books_awaiting_co.map(b => `<tr><td class="n">${esc(b.id)}</td><td class="mono">${esc(b.ucc)}</td><td class="n">${b.from}–${b.to}</td><td class="small">${esc(b.awaiting_co)} · to the registered address</td><td>${co ? `<button class="btn sm pri" data-act="dcAuth" data-id="${esc(b.id)}">Authorise</button>` : '<span class="dim small">compliance officer</span>'}</td></tr>`)) : '';
  const calls = v.calls.length ? `<h4 class="pad">Slips that need a call to the holder</h4>` + tbl([['Slip'], ['Client'], ['Why'], ['Call'], ['']], v.calls.map(d => `<tr><td class="n">${esc(d.id)}</td><td class="mono">${esc(d.ucc)}</td><td class="small wrap">${d.call_needed.map(esc).join('; ')}</td><td class="small">${d.call ? `${esc(d.call.ref)} · ${esc(d.call.number)} · ${esc(d.call.by)}` : '<span class="dim">not recorded</span>'}</td><td>${!d.call && mk ? `<button class="btn sm" data-act="dcCall" data-id="${esc(d.id)}">Record the call</button>` : ''}${d.call && co ? `<button class="btn sm pri" data-act="dcCallOK" data-id="${esc(d.id)}">Approve the call</button>` : ''}</td></tr>`)) : '';
  const counter = v.countersign.length ? `<h4 class="pad">Slips above ₹5 lakh to countersign</h4>` + tbl([['Slip'], ['Client'], ['Value', 'r'], ['Verified by'], ['']], v.countersign.map(d => `<tr><td class="n">${esc(d.id)}</td><td class="mono">${esc(d.ucc)}</td><td class="r">${nf(d.value)}</td><td>${esc(d.checker || '')}${d.senior_verified ? ' <span class="tag">senior official</span>' : ''}</td><td>${has('Ops checker', 'Compliance officer') ? `<button class="btn sm pri" data-act="dcCounter" data-id="${esc(d.id)}">Countersign</button>` : ''}</td></tr>`)) : '';
  const reqs = `<h4 class="pad">Requests with something due</h4>` + tbl([['Request'], ['Client'], ['Status'], ['Due'], ['']], v.requests.map(r => `<tr><td class="n">${esc(r.id)}<div class="dim small">${esc(r.kind)}</div></td><td class="mono">${esc(r.ucc)}</td><td>${pill(r.status, r.late ? 'bad' : 'warn')}</td><td class="small wrap">${r.duties.filter(x => !x.done).map(x => `${x.late ? pill('late', 'bad') + ' ' : ''}${esc(x.what)} · by ${fmtDate(x.due)}`).join('<br>')}</td><td>${Object.keys((v.step_names || {})[r.kind] || {}).length ? `<button class="btn sm" data-act="dcStep" data-id="${esc(r.id)}" data-k="${esc(r.kind)}">Record</button>` : ''}</td></tr>`), { empty: 'Nothing due.' });
  const rc = v.recon;
  const recon = `<h4 class="pad">Daily reconciliation · ${fmtDate(rc.date)} ${rc.signed_by ? pill('signed by ' + rc.signed_by, 'good') : pill('not signed off', 'warn')}</h4>` + tbl([['Line'], ['Pending at start', 'r'], ['Received', 'r'], ['Executed', 'r'], ['Rejected', 'r'], ['Pending at end', 'r'], ['CDSL executed', 'r']], rc.lines.map(l => `<tr><td>${esc(l.kind)}</td><td class="r">${l.carried}</td><td class="r">${l.received}</td><td class="r">${l.executed}</td><td class="r">${l.rejected}</td><td class="r">${l.pending}</td><td class="r">${l.cdsl < 0 ? '<span class="dim">—</span>' : l.diff ? pill(String(l.cdsl), 'bad') : l.cdsl}</td></tr>`)) +
    (ck && !rc.signed_by ? `<div class="tools pad"><button class="btn pri" data-act="dcRecon" data-id="${esc(rc.date)}">Check against CDAS and sign off</button></div>` : '');
  const stock = `<h4 class="pad">Blank DIS stock</h4><div class="tools pad">${(rc.stock || []).map(s => `<span class="tag">${esc(s.series || 'Regular')}: ${s.balance} slips${s.counted ? ' · counted ' + fmtDate(s.counted) : ''}</span>`).join(' ') || '<span class="dim small">Not kept in Ledgerline yet.</span>'}${mk ? '<button class="btn sm" data-act="dcStock" data-id="Received">Delivery from the printer</button><button class="btn sm" data-act="dcStock" data-id="Count">Count</button><button class="btn sm" data-act="dcStock" data-id="Spoilt">Spoilt slips</button>' : ''}</div>`;
  const pool = v.pool_late.length ? `<h4 class="pad">Pay-outs late from the pool</h4>` + tbl([['Client'], ['Security'], ['Pay-out'], ['Due'], ['Moved'], ['CDSL charge (est.)', 'r']], v.pool_late.map(p => `<tr><td class="mono">${esc(p.ucc)}</td><td>${esc(p.symbol)} × ${p.qty}</td><td>${fmtDate(p.pay_out)}</td><td>${fmtDate(p.due)}</td><td>${p.paid ? fmtDate(p.paid) : pill('not yet', 'bad')}</td><td class="r">₹${nf(p.charge, 2)}</td></tr>`)) : '';
  const tools = `<div class="tools pad">${mk ? '<button class="btn sm" data-act="dcLost">Book lost</button><button class="btn sm" data-act="dcWaiver">DIS book waived</button>' : ''}${ck ? '<button class="btn sm" data-act="dcJoint">Joint account: mode of operation</button>' : ''}${co ? '<button class="btn sm" data-act="dcSettings">Settings</button>' : ''}</div>`;
  return `<div class="panel"><div class="hd"><h3>DP controls</h3><small>A new DIS book only against the previous book's requisition slip (or a loss report); dormant accounts' books to the registered address with the compliance officer's authorisation; a recorded call to the holder before a slip moves from a dormant account or empties 5 ISINs or every balance; a senior official and a second signature above ₹5 lakh; closures in 2 working days; the day reconciled with CDSL.</small></div>${tools}${books}${calls}${counter}${reqs}${recon}${stock}${pool}</div>`;
}

function tfPanel(fs) {
  const mk = has('Ops maker', 'Ops checker', 'Risk manager', 'Branch staff'), rk = has('Risk manager', 'Ops checker');
  const open = fs.filter(f => f.status === 'Received' || (f.status === 'Frozen' && (!f.orders_cancelled_at || !f.positions_sent_at || !f.acknowledged_at)));
  const rows = fs.slice(0, 30).map(f => {
    const late = f.status === 'Received' && new Date() > new Date(f.due);
    const btn = (st, l) => `<button class="btn sm" data-act="tfStep" data-id="${esc(f.id)}" data-k="${st}">${l}</button>`;
    const acts = !mk ? '' : f.status === 'Received' ? btn('validate', 'It is the client: freeze') + btn('reject', 'Not the client') :
      f.status === 'Frozen' ? (!f.acknowledged_at ? btn('ack', 'Acknowledged') : '') + (!f.orders_cancelled_at ? btn('cancelled', 'Orders cancelled on Finspot') : '') + (!f.positions_sent_at ? btn('positions', 'Positions sent') : '') + (rk ? `<button class="btn sm" data-act="tfReenable" data-id="${esc(f.id)}">Re-enable</button>` : '') : '';
    return `<tr><td class="n">${esc(f.id)}</td><td class="mono">${esc(f.ucc)} <span class="dim">${esc(f.name)}</span></td><td class="small">${esc(f.mode)}${f.from ? ' · ' + esc(f.from) : ''}<div>${dcHM(f.received_at)}</div></td><td class="small">${dcHM(f.due)}${late ? ' ' + pill('late', 'bad') : ''}</td><td>${pill(f.status, f.status === 'Frozen' ? 'warn' : f.status === 'Received' ? 'bad' : 'nt')}<div class="dim small">${f.frozen_at ? 'frozen ' + dcHM(f.frozen_at) : ''}${f.orders_cancelled_at ? ' · orders: ' + esc(f.cancel_ref) : ''}${f.positions_sent_at ? ' · positions ' + dcHM(f.positions_sent_at) : ''}</div></td><td>${acts}</td></tr>`;
  });
  return `<div class="panel"><div class="hd"><h3>Trading freeze requests · ${open.length} open</h3><div class="tools">${mk ? '<button class="btn sm pri" data-act="tfNew">Record a request</button>' : ''}</div><small>A client may ask to freeze online trading. Freeze within 15 minutes in trading hours (before the next session otherwise), cancel the pending orders on Finspot and record its confirmation, send the open positions within an hour. A request from the registered e-mail or mobile is frozen at once.</small></div>${tbl([['Request'], ['Client'], ['Came by'], ['Freeze by'], ['Status'], ['']], rows, { empty: 'No freeze requests.' })}</div>`;
}

Object.assign(A, {
  // The DIS book: against the previous book's requisition slip, a loss
  // report, or loose slips given in person (internal/ops/dpdis.go).
  dpBook: () => clientCache().then(() => {
    modal(mhd('Issue a DIS book', 'A second book is issued only against the requisition slip of the previous one, or the holder’s written report that it was lost. Accounts with a POA or DDPI get the separate series.') + `<div class="bd"><div class="form"><div class="fld full"><label for="bk-u-q">Client</label>${cpick('bk-u', '', { on: v => fillDemats('bk-bo', v) })}</div><div class="fld full"><label for="bk-bo">For demat account</label><select class="in mono" id="bk-bo"><option value="">Primary account</option></select></div>
      <div class="fld"><label for="bk-n">Slips</label><input class="in" id="bk-n" value="20"></div><div class="fld"><label for="bk-del">Delivery</label><select class="in" id="bk-del"><option>In person</option><option>Registered address</option><option>Collected by authorised person</option></select></div>
      <div class="fld"><label for="bk-req">Requisition slip of the previous book</label><input class="in mono" id="bk-req" inputmode="numeric"></div><div class="fld"><label for="bk-loss">Or: the loss report's reference</label><input class="in" id="bk-loss"></div>
      <div class="fld full"><label><input type="checkbox" id="bk-loose"> Loose slips, given to the holder in person (at most 10 a year)</label></div><div class="fld full"><label for="bk-reg">Register entry the holder signed (loose slips)</label><input class="in" id="bk-reg"></div></div><div id="bk-err"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="bk-go">Issue</button></div>`);
    $('bk-go').onclick = async () => { try { await act(() => post('/v1/dp/books', { ucc: $('bk-u').value, bo_id: $('bk-bo').value, slips: +$('bk-n').value, delivery: $('bk-del').value, requisition: +$('bk-req').value || 0, loss_ref: $('bk-loss').value, loose: $('bk-loose').checked, register: $('bk-reg').value }), b => `Book ${b.id}: slips ${b.from} to ${b.to} issued to ${b.ucc}${b.awaiting_co ? '; the compliance officer authorises it' : ''}`); } catch (e) { $('bk-err').innerHTML = errHTML(e); } };
  }),
  // Verification: the client's confirmation above ₹5 lakh, and every
  // holder's signature on a joint account operated jointly.
  dpVerify: (id, el) => {
    const ok = el.dataset.a === '1', hv = el.dataset.hv === '1';
    if (!ok) { confirmModal('Reject ' + id, 'The reason is kept with the slip.', '<div class="fld"><label for="dv-t">Reason</label><input class="in" id="dv-t"></div>', 'Reject', () => act(() => post(`/v1/dp/dis/${id}/verify`, { approve: false, note: $('dv-t').value }), id + ' rejected'), true); return; }
    confirmModal('Verify ' + id, 'The slip belongs to the holder’s series, every holder signed, and the signatures match.', `${hv ? '<div class="fld"><label for="dv-t">Client confirmation (call reference, email): above ₹5 lakh</label><input class="in" id="dv-t"></div>' : ''}<div class="fld"><label><input type="checkbox" id="dv-all"> Every holder signed and the signatures match</label></div>`, 'Verify',
      () => act(() => post(`/v1/dp/dis/${id}/verify`, { approve: true, confirmation: hv ? $('dv-t').value : '', all_signed: $('dv-all').checked }), id + ' verified'));
  },
  // DP requests, with the fields the checks need (internal/ops/dpreqtat.go).
  dpReq: () => clientCache().then(() => {
    const fields = { Demat: [['drf', 'DRF number'], ['isin', 'ISIN'], ['symbol', 'Scrip'], ['qty', 'Quantity'], ['certificates', 'Certificates']],
      Freeze: [['type', 'Freeze (Debit, Credit or Both)'], ['reason', 'Reason'], ['by', 'Asked by (Client, DP or Authority)'], ['signed_request', 'Holder’s signed request (when the client asks)'], ['order_ref', 'Order reference (lien or attachment)']],
      Unfreeze: [['reason', 'Reason']],
      Transmission: [['to_ucc', 'Claimant’s UCC'], ['death_certificate', 'Death certificate reference'], ['claimant', 'Claimant name'], ['survivors', 'Surviving joint holders continue (yes)']],
      Closure: [['reason', 'Reason'], ['transfer_to', 'Transfer holdings to BO ID (if any)'], ['target_pan', 'PANs of the target account’s holders (from its client master)'], ['dis_slip', 'DIS slip number (holders not the same)'], ['otp_ref', 'OTP reference (holders not the same)'], ['initiated_by', 'Initiated by (Client or DP)'], ['notice_sent', 'DP’s notice sent on (YYYY-MM-DD)']] };
    const body = k => fields[k].map(([id, l]) => `<div class="fld"><label for="dq-${id}">${l}</label><input class="in" id="dq-${id}" data-dq="${id}"></div>`).join('');
    modal(mhd('DP request', 'Raised by a maker, completed by a checker who did not raise it. A closure the DP starts is approved by the compliance officer after 30 days’ notice.') + `<form class="bd" id="dqf"><div class="form"><div class="fld full"><label for="dq-u-q">Client</label>${cpick('dq-u', '', { on: v => fillDemats('dq-bo', v) })}</div><div class="fld full"><label for="dq-bo">Demat account</label><select class="in mono" id="dq-bo"><option value="">Primary account</option></select></div><div class="fld full"><label for="dq-k">Request</label><select class="in" id="dq-k">${Object.keys(fields).map(k => `<option>${k}</option>`).join('')}</select></div></div><div class="form" id="dq-body">${body('Demat')}</div>
      <div id="dq-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Raise</button></div></form>`);
    $('dq-k').onchange = () => { $('dq-body').innerHTML = body($('dq-k').value); };
    $('dqf').onsubmit = async e => { e.preventDefault(); const detail = {}; document.querySelectorAll('[data-dq]').forEach(x => { if (x.value.trim()) detail[x.dataset.dq] = x.value.trim(); });
      try { const r = await post('/v1/dp/requests', { kind: $('dq-k').value, ucc: $('dq-u').value, bo_id: $('dq-bo').value, detail }); closeModal(); toast(`${r.id} raised`); UI.dpTab = 'req'; render({ keep: true }); } catch (err) { $('dq-err').innerHTML = errHTML(err); } };
  }),
  dcAuth: id => confirmModal('Authorise ' + id, 'The account had no transaction for 12 months: the book goes only to the registered address.', '<div class="fld"><label for="dc-n">Note</label><input class="in" id="dc-n"></div>', 'Authorise', () => act(() => post(`/v1/dp/books/${encodeURIComponent(id)}/authorise`, { note: $('dc-n').value }), id + ' authorised')),
  dcCall: id => confirmModal('Call the holder · ' + id, 'Call on the registered number and note the recording’s reference. The compliance officer then approves it.', '<div class="fld"><label for="dc-r">Recording reference</label><input class="in" id="dc-r"></div><div class="fld"><label for="dc-no">Number called (blank: the registered mobile)</label><input class="in mono" id="dc-no"></div><div class="fld"><label for="dc-n">What the holder said</label><input class="in" id="dc-n"></div>', 'Record',
    () => act(() => post(`/v1/dp/dis/${encodeURIComponent(id)}/call`, { ref: $('dc-r').value, number: $('dc-no').value, note: $('dc-n').value }), 'Call recorded')),
  dcCallOK: id => act(() => post(`/v1/dp/dis/${encodeURIComponent(id)}/call-approve`, {}), 'Call approved'),
  dcCounter: id => act(() => post(`/v1/dp/dis/${encodeURIComponent(id)}/countersign`, {}), id + ' countersigned'),
  dcStep: (id, el) => {
    const names = (C.dc && C.dc.step_names[el.dataset.k]) || {};
    confirmModal('Record on ' + id, 'A notice or letter to the holder, a payment, documents complete or returned.', `<div class="fld"><label for="dc-s">What</label><select class="in" id="dc-s">${Object.entries(names).map(([k, l]) => `<option value="${k}">${esc(l)}</option>`).join('')}</select></div><div class="fld"><label for="dc-d">On</label><input class="in" type="date" id="dc-d" value="${esc(S.date)}"></div><div class="fld"><label for="dc-r">Reference (letter, e-mail, courier, form)</label><input class="in" id="dc-r"></div>`, 'Record',
      () => act(() => post(`/v1/dp/requests/${encodeURIComponent(id)}/steps`, { step: $('dc-s').value, on: $('dc-d').value, ref: $('dc-r').value }), 'Recorded'));
  },
  dcRecon: date => {
    const ls = (C.dc && C.dc.recon.lines) || [];
    confirmModal('Sign off ' + fmtDate(date), 'Enter CDSL’s count of what was executed where it differs from Ledgerline’s, and explain any difference.', ls.map((l, i) => `<div class="fld"><label for="dc-c${i}">${esc(l.kind)} · Ledgerline ${l.executed}</label><input class="in mono" id="dc-c${i}" inputmode="numeric" placeholder="${l.executed}"></div>`).join('') + '<div class="fld"><label for="dc-n">Note</label><input class="in" id="dc-n"></div>', 'Sign off',
      () => { const cdsl = {}; ls.forEach((l, i) => { const v = $('dc-c' + i).value.trim(); if (v !== '') cdsl[l.kind] = +v; }); return act(() => post('/v1/dp/recon', { date, cdsl, note: $('dc-n').value }), 'Signed off'); });
  },
  dcStock: kind => confirmModal({ Received: 'Blank slips from the printer', Count: 'Count of blank slips', Spoilt: 'Spoilt slips' }[kind], kind === 'Count' ? 'Count the blank slips in hand; give the reason if it differs from the book balance.' : '', `<div class="fld"><label for="dc-ser">Series</label><select class="in" id="dc-ser"><option value="">Regular</option><option>POA/DDPI</option></select></div><div class="fld"><label for="dc-q">${kind === 'Count' ? 'Slips counted' : 'Slips'}</label><input class="in mono" id="dc-q" inputmode="numeric"></div>${kind === 'Received' ? '<div class="fld"><label for="dc-r">Printer’s delivery reference</label><input class="in" id="dc-r"></div>' : ''}<div class="fld"><label for="dc-n">Note</label><input class="in" id="dc-n"></div>`, 'Record',
    () => act(() => post('/v1/dp/stock', { kind, series: $('dc-ser').value, slips: kind === 'Count' ? 0 : +$('dc-q').value, counted: kind === 'Count' ? +$('dc-q').value : 0, ref: ($('dc-r') || {}).value || '', note: $('dc-n').value }), 'Recorded')),
  dcLost: () => confirmModal('A DIS book lost', 'On the holder’s written report, every unused slip of the book is cancelled.', '<div class="fld"><label for="dc-b">Book (DB-…)</label><input class="in mono" id="dc-b"></div><div class="fld"><label for="dc-r">Loss report reference</label><input class="in" id="dc-r"></div>', 'Cancel the unused slips',
    () => act(() => post(`/v1/dp/books/${encodeURIComponent($('dc-b').value.trim())}/lost`, { ref: $('dc-r').value }), 'Unused slips cancelled'), true),
  dcWaiver: () => clientCache().then(() => confirmModal('DIS book waived', 'The holder waived a DIS book at opening, in writing.', `<div class="fld"><label for="dw-u-q">Client</label>${cpick('dw-u', '', { on: v => fillDemats('dw-bo', v) })}</div><div class="fld"><label for="dw-bo">Demat account</label><select class="in mono" id="dw-bo"><option value="">Primary account</option></select></div><div class="fld"><label for="dc-r">Waiver reference</label><input class="in" id="dc-r"></div>`, 'Record',
    () => act(() => post(`/v1/dp/accounts/${encodeURIComponent($('dw-u').value)}/dis-waiver`, { bo_id: $('dw-bo').value, ref: $('dc-r').value }), 'Waiver recorded'))),
  dcJoint: () => clientCache().then(() => confirmModal('Joint account: mode of operation', 'Jointly unless every holder asks otherwise; back to Jointly on any one holder’s request.', `<div class="fld"><label for="dj-u-q">Client</label>${cpick('dj-u', '', { on: v => fillDemats('dj-bo', v) })}</div><div class="fld"><label for="dj-bo">Demat account</label><select class="in mono" id="dj-bo"><option value="">Primary account</option></select></div><div class="fld"><label for="dj-m">Mode</label><select class="in" id="dj-m"><option>Jointly</option><option>Anyone or survivor</option></select></div><div class="fld"><label for="dj-h">Holders who signed the request</label><input class="in" id="dj-h" inputmode="numeric" value="1"></div><div class="fld"><label for="dc-r">Request reference</label><input class="in" id="dc-r"></div>`, 'Change',
    () => act(() => post(`/v1/dp/accounts/${encodeURIComponent($('dj-u').value)}/joint-operation`, { bo_id: $('dj-bo').value, mode: $('dj-m').value, holders: +$('dj-h').value, ref: $('dc-r').value }), 'Mode of operation changed'))),
  dcSettings: () => {
    const c = (C.dc && C.dc.config) || {};
    confirmModal('DP controls: settings', 'The firm’s own facts; the rules stay as the circulars say.', `<div class="fld"><label for="dc-so">Senior officials for slips above ₹5 lakh (user names, comma separated; blank: compliance officers)</label><input class="in" id="dc-so" value="${esc((c.senior_officials || []).join(', '))}"></div><div class="fld"><label for="dc-th">Ledgerline has every demat transaction of the DP from</label><input class="in" type="date" id="dc-th" value="${esc(c.txn_history_from || '')}"></div><div class="fld"><label for="dc-sd">Count the blank slip stock every (days)</label><input class="in" id="dc-sd" inputmode="numeric" value="${c.stock_count_days || 30}"></div><div class="fld"><label for="dc-fm">Ways clients can ask to freeze online trading (comma separated, at least two)</label><input class="in" id="dc-fm" value="${esc((c.freeze_modes || []).join(', '))}"></div><div class="fld"><label for="dc-fp">Where the freeze policy is published</label><input class="in" id="dc-fp" value="${esc(c.freeze_policy || '')}"></div>`, 'Save',
      () => act(() => post('/v1/dp/controls/settings', { senior_officials: $('dc-so').value.split(','), txn_history_from: $('dc-th').value, stock_count_days: +$('dc-sd').value || 0, freeze_modes: $('dc-fm').value.split(','), freeze_policy: $('dc-fp').value }), 'Saved'));
  },
  tfNew: () => clientCache().then(() => confirmModal('Client’s request to freeze online trading', 'Record when it came and from where. A request from the registered e-mail or mobile freezes trading at once.', `<div class="fld"><label for="tf-u-q">Client</label>${cpick('tf-u', '', {})}</div><div class="fld"><label for="tf-m">Came by</label><select class="in" id="tf-m">${['Email', 'SMS', 'App', 'Website', 'Phone (recorded)', 'In person', 'Letter'].map(m => `<option>${m}</option>`).join('')}</select></div><div class="fld"><label for="tf-f">From (e-mail address or number)</label><input class="in" id="tf-f"></div><div class="fld"><label for="tf-at">Received at (blank: now; YYYY-MM-DD HH:MM)</label><input class="in mono" id="tf-at"></div><div class="fld"><label for="tf-n">Note</label><input class="in" id="tf-n"></div>`, 'Record',
    () => act(() => post('/v1/rms/freeze-requests', { ucc: $('tf-u').value, mode: $('tf-m').value, from: $('tf-f').value, received_at: $('tf-at').value, note: $('tf-n').value }), f => f.status === 'Frozen' ? `${f.id}: trading frozen` : `${f.id}: check it is the client, then freeze`))),
  tfStep: (id, el) => {
    const k = el.dataset.k, l = { validate: 'How it was checked to be the client', reject: 'Why it is not the client’s', ack: 'Acknowledgement sent (message reference)', cancelled: 'Finspot’s confirmation of the cancellation (or “No pending orders”)', positions: 'Open positions sent (message reference)' }[k];
    confirmModal(el.textContent, '', `<div class="fld"><label for="tf-r">${esc(l)}</label><input class="in" id="tf-r"></div>`, 'Record', () => act(() => post(`/v1/rms/freeze-requests/${encodeURIComponent(id)}`, { step: k, ref: $('tf-r').value }), 'Recorded'), k === 'reject');
  },
  tfReenable: id => confirmModal('Re-enable trading · ' + id, 'Only on the client’s request, after due diligence.', `<div class="fld"><label for="tf-m">The client asked by</label><select class="in" id="tf-m">${['Email', 'SMS', 'App', 'Website', 'Phone (recorded)', 'In person', 'Letter'].map(m => `<option>${m}</option>`).join('')}</select></div><div class="fld"><label for="tf-d">Due diligence done</label><textarea class="in" id="tf-d"></textarea></div>`, 'Re-enable',
    () => act(() => post(`/v1/rms/freeze-requests/${encodeURIComponent(id)}/reenable`, { mode: $('tf-m').value, due_diligence: $('tf-d').value }), 'Trading re-enabled'))
});
