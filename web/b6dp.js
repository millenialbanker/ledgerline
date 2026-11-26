'use strict';
/* Ledgerline console: round-6 gap work.

   Depository → Demat accounts & DIS: AMC refunds on closure, CDSL KYC
   deficiency cases, OTP transfer-cum-closure requests, reason-27 accounts
   ready to unfreeze, PoA/DDPI terms, nomination opt-out declarations.
   Compliance → Deceased clients: a death intimation received directly and
   the certificate's check. RMS & limits: blocked clients' pending orders
   (one click records Finspot's confirmation, a second person checks).
   Surveillance & AML: shares outstanding for the 0.5% bulk-deal check,
   BSE's CCM penalty files and the round-6 rule thresholds.

   Server: internal/ops/dpb6.go, ordercancel.go, survb6.go;
   internal/api/b6dp.go. Loaded after app.js and dpctl.js / dpsurv.js; it
   wraps V.dp, V.rms, V.surv and V.deceased. Names are prefixed b6. */

const b6DP = V.dp, b6RMS = V.rms, b6Surv = V.surv, b6Dec = V.deceased;
V.dp = {
  load: async () => { const d = await b6DP.load(); d.b6 = await get('/v1/dp/b6').catch(() => null); C.b6 = d.b6; return d; },
  html: d => b6DP.html(d) + (d.b6 && d.b6.dp !== 'No' ? b6DPPanel(d.b6) : '')
};
V.rms = {
  load: async () => { const d = await b6RMS.load(); d.b6oc = await get('/v1/rms/order-cancels').catch(() => []); return d; },
  html: d => b6RMS.html(d) + b6OCPanel(d.b6oc || [])
};
V.surv = {
  load: async () => { const d = await b6Surv.load(); d.b6s = await get('/v1/surveillance/b6').catch(() => null); C.b6s = d.b6s; return d; },
  html: d => b6Surv.html(d) + (d.b6s ? b6SurvPanel(d.b6s) : '')
};
if (b6Dec) V.deceased = {
  load: b6Dec.load,
  html: d => b6Dec.html(d) + `<div class="panel"><div class="hd"><h3>Death intimation received directly</h3><div class="tools">${has('Ops maker', 'Ops checker', 'Compliance officer') ? '<button class="btn sm pri" data-act="b6Intim">Death intimation received</button>' : ''}${has('Ops checker', 'Compliance officer') ? '<button class="btn sm" data-act="b6Cert">Record the certificate check</button>' : ''}</div><small>From a nominee, joint holder or legal heir, not on the KRAs’ list: debits are blocked at once and a debit freeze is raised on each demat account with the DP. Verify the certificate on the government site or with the registrar, and keep a signed and stamped copy.</small></div></div>`
};

function b6DPPanel(v) {
  const mk = has('Ops maker', 'Ops checker'), ck = has('Ops checker'), co = has('Compliance officer');
  const amc = v.amc_refunds.filter(r => r.status === 'To approve');
  const amcT = amc.length ? `<h4 class="pad">AMC to refund on closure</h4>` + tbl([['Account'], ['Closed'], ['Quarters', 'r'], ['Refund', 'r'], ['']], amc.map(r => `<tr><td class="mono">${esc(r.ucc)} <span class="dim">${esc(r.bo_id)}</span></td><td>${fmtDate(r.closed_on)}</td><td class="r">${r.quarters}</td><td class="r">${nf(r.total)}<div class="dim small">${esc(r.basis)}</div></td><td>${ck ? `<button class="btn sm pri" data-act="b6AMC" data-id="${esc(r.bo_id)}" data-a="1">Approve</button><button class="btn sm" data-act="b6AMC" data-id="${esc(r.bo_id)}" data-a="0">Reject</button>` : ''}</td></tr>`)) : '';
  const kd = v.kyc_deficiencies.filter(k => k.status !== 'Done' && k.status !== 'Rectified');
  const kdT = kd.length ? `<h4 class="pad">CDSL KYC deficiencies</h4>` + tbl([['Case'], ['Account'], ['Deficiency'], ['Stage'], ['']], kd.map(k => `<tr><td class="n">${esc(k.id)}<div class="dim small">${esc(k.cdsl_ref)}</div></td><td class="mono">${esc(k.ucc)}</td><td class="small wrap">${esc(k.deficiency)}</td><td>${pill(k.status, k.status.startsWith('Freeze') ? 'bad' : 'warn')}${k.due ? `<div class="dim small">rectify by ${fmtDate(k.due)}</div>` : ''}</td><td>${mk || co ? `<button class="btn sm" data-act="b6KDStep" data-id="${esc(k.id)}">Record</button>` : ''}</td></tr>`)) : '';
  const oc = v.otp_closures.filter(r => r.status === 'Pending');
  const ocT = oc.length ? `<h4 class="pad">OTP transfer-cum-closure requests in CDAS</h4>` + tbl([['Request'], ['Account'], ['Target'], ['Received'], ['Due'], ['']], oc.map(r => `<tr><td class="n">${esc(r.id)}</td><td class="mono">${esc(r.ucc)}</td><td class="mono">${esc(r.target_bo_id)}</td><td>${fmtDate(r.received)}</td><td>${fmtDate(r.due)}</td><td>${ck ? `<button class="btn sm pri" data-act="b6OTPDecide" data-id="${esc(r.id)}">Decide</button>` : ''}</td></tr>`)) : '';
  const uf = v.unfreeze_ready.length ? `<h4 class="pad">Reason-27 freezes: KYC now validated</h4>` + tbl([['Account'], ['Frozen since'], ['Unfreeze request']], v.unfreeze_ready.map(a => `<tr><td class="mono">${esc(a.ucc)} <span class="dim">${esc(a.bo_id)}</span></td><td>${esc(a.since || '')}</td><td>${a.request ? esc(a.request) : pill('not raised', 'warn')}</td></tr>`)) + (mk ? '<div class="tools pad"><button class="btn sm" data-act="b6Unfreeze">Raise unfreezes</button></div>' : '') : '';
  const poaWait = v.poa_checks.filter(p => p.status === 'To check');
  const poa = `<h4 class="pad">PoA / DDPI terms · ${v.poa_to_check.length} account${v.poa_to_check.length === 1 ? '' : 's'} without an approved check</h4>` + (poaWait.length ? tbl([['Account'], ['Document'], ['Terms met'], ['Ticked by'], ['']], poaWait.map(p => `<tr><td class="mono">${esc(p.ucc)} <span class="dim">${esc(p.bo_id)}</span></td><td>${esc(p.ref)}</td><td>${Object.values(p.terms).filter(x => x).length} of ${v.poa_terms.length}</td><td>${esc(p.maker)}</td><td>${ck || co ? `<button class="btn sm" data-act="b6PoAOK" data-id="${esc(p.bo_id)}">Approve</button>` : ''}</td></tr>`)) : '');
  const nc = v.no_nomination_choice.length ? `<div class="note warn pad">${v.no_nomination_choice.length} client${v.no_nomination_choice.length === 1 ? '' : 's'} with a demat account and neither a nomination nor a signed opt-out: ${v.no_nomination_choice.slice(0, 10).map(esc).join(', ')}${v.no_nomination_choice.length > 10 ? '…' : ''}</div>` : '';
  const tools = `<div class="tools pad">${mk || co ? '<button class="btn sm" data-act="b6KD">KYC deficiency (CDSL)</button>' : ''}${mk ? '<button class="btn sm" data-act="b6OTP">OTP closure request</button><button class="btn sm" data-act="b6OptOut">Opt-out declaration</button>' : ''}${mk || co ? '<button class="btn sm" data-act="b6PoA">PoA / DDPI terms</button>' : ''}</div>`;
  return `<div class="panel"><div class="hd"><h3>More DP controls</h3><small>Unused AMC refunded on closure; CDSL’s KYC deficiencies (letter, e-mail and phone, 30 days, then reason 20); OTP closure requests decided within 2 working days; reason-27 freezes lifted once the KRA validates; PoA and DDPI terms; no demat account without a nomination or a signed opt-out.</small></div>${tools}${nc}${amcT}${kdT}${ocT}${uf}${poa}</div>`;
}

function b6OCPanel(xs) {
  const open = xs.filter(x => x.status === 'To cancel' || x.status === 'Confirmed');
  const mk = has('Risk manager', 'Ops maker', 'Ops checker'), ck = has('Ops checker', 'Risk manager');
  const rows = xs.slice(0, 30).map(x => `<tr><td class="n">${esc(x.id)}</td><td class="mono">${esc(x.ucc)} <span class="dim">${esc(x.name)}</span></td><td class="small wrap">${esc(x.why)}</td><td class="small">${dcHM(x.due)}${x.status === 'To cancel' && new Date() > new Date(x.due) ? ' ' + pill('late', 'bad') : ''}</td><td>${pill(x.status, x.status === 'To cancel' ? 'bad' : x.status === 'Confirmed' ? 'warn' : 'nt')}${x.ref ? `<div class="dim small">${esc(x.ref)} · ${esc(x.maker)}</div>` : ''}</td><td>${x.status === 'To cancel' && mk ? `<button class="btn sm pri" data-act="b6OCDone" data-id="${esc(x.id)}">Orders cancelled</button>` : ''}${x.status === 'Confirmed' && ck ? `<button class="btn sm" data-act="b6OCCheck" data-id="${esc(x.id)}">Checked on Finspot</button>` : ''}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Blocked clients’ pending orders · ${open.length} open</h3><small>A block stops fresh orders; pending orders stay until cancelled on Finspot (its admin API has no cancel call). Record Finspot’s confirmation, then a second person checks the order book.</small></div>${tbl([['Item'], ['Client'], ['Why blocked'], ['Due'], ['Status'], ['']], rows, { empty: 'No blocked clients' })}</div>`;
}

function b6SurvPanel(v) {
  const co = has('Compliance officer'), ld = has('Compliance officer', 'Ops checker');
  const c = v.config;
  const pen = v.ccm_penalties.length ? tbl([['Month'], ['File'], ['Modifications', 'r'], ['Not on register', 'r'], ['Penalty', 'r']], v.ccm_penalties.map(p => `<tr><td>${esc(p.id)}</td><td class="small">${esc(p.file)}</td><td class="r">${p.rows.length}</td><td class="r">${p.missing ? pill(p.missing, 'bad') : 0}</td><td class="r">${nf(p.total)}</td></tr>`)) : '<div class="dim small pad">No BSE CCM penalty file loaded (Exchange files).</div>';
  return `<div class="panel"><div class="hd"><h3>Bulk deals, self and wash trades, abnormal patterns</h3><div class="tools">${ld ? '<input type="file" id="b6-so" class="in sm" accept=".csv,.txt"><button class="btn sm" data-act="b6Shares">Load shares outstanding</button><button class="btn sm" data-act="b6Run">Run for the day</button>' : ''}${co ? '<button class="btn sm" data-act="b6Thr">Thresholds</button>' : ''}</div><small>Shares outstanding on record: ${v.shares_outstanding}${v.shares_as_of ? ' (latest ' + fmtDate(v.shares_as_of) + ')' : ''}. Trades above 0.5% of the listed shares are disclosed to the exchange. Same client (or one PAN) on both sides within ${c.wash_minutes} minutes; trades ${c.far_pct_options}% (options) or ${c.far_pct_other}% from the close; reversals on ${c.reversal_days} of 10 days; ₹${nf(c.illiquid_value)} a day in the ILLIQUID scrip group; expiry-day round trips of ₹${nf(c.expiry_pl_rupees)}. Counterparty matches with other members’ clients need the exchange’s alerts.</small></div><h4 class="pad">BSE CCM penalties</h4>${pen}</div>`;
}

const b6Val = id => ($(id) || {}).value || '';
Object.assign(A, {
  b6AMC: (bo, el) => {
    const ok = el.dataset.a === '1';
    confirmModal(ok ? 'Refund unused AMC' : 'Do not refund', ok ? 'The refund and its GST go back to the client’s ledger, with a credit note.' : 'The reason is kept.', ok ? '' : '<div class="fld"><label for="b6-n">Reason</label><input class="in" id="b6-n"></div>', ok ? 'Refund' : 'Reject',
      () => act(() => post(`/v1/dp/amc-refunds/${encodeURIComponent(bo)}`, { approve: ok, note: b6Val('b6-n') }), ok ? 'Refunded' : 'Recorded'), !ok);
  },
  b6Intim: () => clientCache().then(() => confirmModal('Death intimation received', 'Debits are blocked at once; a debit freeze request is raised on each demat account with the DP.', `<div class="fld"><label for="b6i-u-q">Client</label>${cpick('b6i-u', '', {})}</div><div class="fld"><label for="b6i-n">From (name)</label><input class="in" id="b6i-n"></div><div class="fld"><label for="b6i-r">Relation to the holder</label><input class="in" id="b6i-r"></div><div class="fld"><label for="b6i-d">Received on</label><input class="in" type="date" id="b6i-d" value="${esc(S.date)}"></div><div class="fld"><label for="b6i-dd">Date of death</label><input class="in" type="date" id="b6i-dd"></div><div class="fld"><label for="b6i-c">Death certificate number</label><input class="in" id="b6i-c"></div>`, 'Record',
    () => act(() => post('/v1/deceased/intimations', { ucc: b6Val('b6i-u'), notifier: b6Val('b6i-n'), relation: b6Val('b6i-r'), received: b6Val('b6i-d'), date_of_death: b6Val('b6i-dd'), certificate: b6Val('b6i-c') }), d => `${d.id}: debits blocked`))),
  b6Cert: () => confirmModal('Death certificate check', 'Where the certificate was verified; the copy kept is signed and stamped by the person who checked it.', `<div class="fld"><label for="b6c-id">Case (DC-…)</label><input class="in mono" id="b6c-id"></div><div class="fld"><label for="b6c-v">Checked on</label><select class="in" id="b6c-v"><option>Government site</option><option>Registrar of births and deaths</option><option>Original seen</option></select></div><div class="fld"><label for="b6c-w">Site or registrar</label><input class="in" id="b6c-w"></div><div class="fld"><label><input type="checkbox" id="b6c-s"> The copy kept is signed and stamped</label></div>`, 'Record',
    () => act(() => post(`/v1/deceased/${encodeURIComponent(b6Val('b6c-id').trim())}/certificate`, { via: b6Val('b6c-v'), where: b6Val('b6c-w'), signed_stamped: $('b6c-s').checked }), 'Check recorded')),
  b6KD: () => clientCache().then(() => confirmModal('CDSL: KYC deficiency of a BO', 'Tell the BO by letter, e-mail and phone; 30 days to rectify, then the freeze under reason 20.', `<div class="fld"><label for="b6k-u-q">Client</label>${cpick('b6k-u', '', { on: v => fillDemats('b6k-bo', v) })}</div><div class="fld"><label for="b6k-bo">Demat account</label><select class="in mono" id="b6k-bo"><option value="">Primary account</option></select></div><div class="fld"><label for="b6k-r">CDSL’s reference</label><input class="in" id="b6k-r"></div><div class="fld"><label for="b6k-d">Deficiency</label><input class="in" id="b6k-d"></div><div class="fld"><label for="b6k-f">Freeze CDSL asks for</label><select class="in" id="b6k-f"><option>Both</option><option>Debit</option><option>Credit</option></select></div>`, 'Record',
    () => act(() => post('/v1/dp/kyc-deficiencies', { ucc: b6Val('b6k-u'), bo_id: b6Val('b6k-bo'), cdsl_ref: b6Val('b6k-r'), deficiency: b6Val('b6k-d'), freeze_type: b6Val('b6k-f') }), k => k.id + ' recorded'))),
  b6KDStep: id => confirmModal('Record on ' + id, '', `<div class="fld"><label for="b6s-s">Step</label><select class="in" id="b6s-s"><option value="letter">Letter sent (dispatch proof)</option><option value="email">E-mail sent (log)</option><option value="phone">Phone call (note)</option><option value="rectified">Rectified</option><option value="freeze">Freeze under reason 20</option><option value="informed">CDSL informed</option><option value="ack">Acknowledgement sent</option></select></div><div class="fld"><label for="b6s-d">On</label><input class="in" type="date" id="b6s-d" value="${esc(S.date)}"></div><div class="fld"><label for="b6s-r">Reference</label><input class="in" id="b6s-r"></div>`, 'Record',
    () => act(() => post(`/v1/dp/kyc-deficiencies/${encodeURIComponent(id)}`, { step: b6Val('b6s-s'), on: b6Val('b6s-d'), ref: b6Val('b6s-r') }), 'Recorded')),
  b6OTP: () => clientCache().then(() => confirmModal('OTP transfer-cum-closure request', 'As seen in CDAS. A checker decides it within 2 working days of receipt.', `<div class="fld"><label for="b6o-u-q">Client</label>${cpick('b6o-u', '', { on: v => fillDemats('b6o-bo', v) })}</div><div class="fld"><label for="b6o-bo">Demat account</label><select class="in mono" id="b6o-bo"><option value="">Primary account</option></select></div><div class="fld"><label for="b6o-t">Target BO ID</label><input class="in mono" id="b6o-t"></div><div class="fld"><label for="b6o-r">CDAS reference</label><input class="in" id="b6o-r"></div><div class="fld"><label for="b6o-d">Received in CDAS on</label><input class="in" type="date" id="b6o-d" value="${esc(S.date)}"></div>`, 'Record',
    () => act(() => post('/v1/dp/otp-closures', { ucc: b6Val('b6o-u'), bo_id: b6Val('b6o-bo'), target_bo_id: b6Val('b6o-t'), cdsl_ref: b6Val('b6o-r'), received: b6Val('b6o-d') }), r => `${r.id}: due ${fmtDate(r.due)}`))),
  b6OTPDecide: id => {
    const ch = (C.b6 && C.b6.otp_checks) || {};
    confirmModal('Decide ' + id, 'Approve or reject in CDAS, then record it here.', Object.entries(ch).map(([k, l]) => `<div class="fld"><label><input type="checkbox" data-b6c="${k}"> ${esc(l)}</label></div>`).join('') + '<div class="fld"><label for="b6d-a">Decision</label><select class="in" id="b6d-a"><option value="1">Approved</option><option value="0">Rejected</option></select></div><div class="fld"><label for="b6d-r">Reason (when rejected)</label><input class="in" id="b6d-r"></div>', 'Record',
      () => { const checks = {}; document.querySelectorAll('[data-b6c]').forEach(x => { checks[x.dataset.b6c] = x.checked; }); return act(() => post(`/v1/dp/otp-closures/${encodeURIComponent(id)}`, { approve: b6Val('b6d-a') === '1', checks, reason: b6Val('b6d-r') }), 'Recorded'); });
  },
  b6Unfreeze: () => act(() => post('/v1/dp/kyc-unfreezes', {}), r => `${r.raised} unfreeze request${r.raised === 1 ? '' : 's'} raised`),
  b6OptOut: () => clientCache().then(() => confirmModal('Signed opt-out declaration', 'The client declares not to nominate. Needed before the demat account opens when there is no nomination.', `<div class="fld"><label for="b6n-u-q">Client</label>${cpick('b6n-u', '', {})}</div><div class="fld"><label for="b6n-d">Signed on</label><input class="in" type="date" id="b6n-d" value="${esc(S.date)}"></div><div class="fld"><label for="b6n-m">How</label><select class="in" id="b6n-m">${['Signed form', 'Online with e-Sign', 'Online with digital signature', 'Online with OTP', 'Thumb impression with two witnesses'].map(m => `<option>${m}</option>`).join('')}</select></div><div class="fld"><label for="b6n-r">Reference of the copy kept</label><input class="in" id="b6n-r"></div><div class="fld"><label for="b6n-w1">Witness 1 (thumb impression)</label><input class="in" id="b6n-w1"></div><div class="fld"><label for="b6n-w2">Witness 2</label><input class="in" id="b6n-w2"></div>`, 'Record',
    () => act(() => post(`/v1/clients/${encodeURIComponent(b6Val('b6n-u'))}/nomination-optout`, { on: b6Val('b6n-d'), mode: b6Val('b6n-m'), ref: b6Val('b6n-r'), witness_1: b6Val('b6n-w1'), witness_2: b6Val('b6n-w2') }), 'Declaration recorded'))),
  b6PoA: () => clientCache().then(() => { const ts = (C.b6 && C.b6.poa_terms) || [];
    confirmModal('PoA / DDPI document terms', 'Tick each term the document meets. A checker approves.', `<div class="fld"><label for="b6p-u-q">Client</label>${cpick('b6p-u', '', { on: v => fillDemats('b6p-bo', v) })}</div><div class="fld"><label for="b6p-bo">Demat account</label><select class="in mono" id="b6p-bo"><option value="">Primary account</option></select></div><div class="fld"><label for="b6p-r">Document reference</label><input class="in" id="b6p-r"></div>` + ts.map(x => `<div class="fld"><label><input type="checkbox" data-b6t="${x.key}"> ${esc(x.label)}</label></div>`).join('') + '<div class="fld"><label for="b6p-n">Note (a term not met)</label><input class="in" id="b6p-n"></div>', 'Record',
      () => { const terms = {}; document.querySelectorAll('[data-b6t]').forEach(x => { terms[x.dataset.b6t] = x.checked; }); return act(() => post('/v1/dp/poa-checks', { ucc: b6Val('b6p-u'), bo_id: b6Val('b6p-bo'), ref: b6Val('b6p-r'), note: b6Val('b6p-n'), terms }), 'Recorded'); }); }),
  b6PoAOK: bo => act(() => post(`/v1/dp/poa-checks/${encodeURIComponent(bo)}/approve`, {}), p => 'PoA check ' + p.status.toLowerCase()),
  b6OCDone: id => confirmModal('Orders cancelled on Finspot', 'Finspot’s confirmation, or “No pending orders”.', '<div class="fld"><label for="b6x-r">Confirmation</label><input class="in" id="b6x-r" value="No pending orders"></div>', 'Record',
    () => act(() => post(`/v1/rms/order-cancels/${encodeURIComponent(id)}`, { ref: b6Val('b6x-r') }), 'Recorded')),
  b6OCCheck: id => act(() => post(`/v1/rms/order-cancels/${encodeURIComponent(id)}/check`, {}), 'Checked'),
  b6Shares: async () => {
    const f = ($('b6-so') || {}).files && $('b6-so').files[0];
    if (!f) { toast('Choose the shares outstanding file first'); return; }
    const buf = await f.arrayBuffer();
    await act(() => api('POST', '/v1/surveillance/shares-outstanding?source=' + encodeURIComponent(f.name), buf, true), r => `${r.loaded} scrips loaded`);
  },
  b6Run: () => act(() => post('/v1/surveillance/trade-patterns', { date: S.date }), r => `${r.raised} new alert${r.raised === 1 ? '' : 's'}${r.bulk_not_checked.length ? `; ${r.bulk_not_checked.length} scrips with no shares outstanding` : ''}`),
  b6Thr: () => { const c = (C.b6s && C.b6s.config) || {};
    const f = [['wash_minutes', 'Buy and sell within (minutes)'], ['wash_qty_pct', 'Quantities within (%)'], ['wash_price_pct', 'Prices within (%)'], ['far_pct_options', 'Option away from the close (%)'], ['far_pct_other', 'Other contracts away from the close (%)'], ['reversal_days', 'Reversal days of the last 10'], ['illiquid_value', 'Illiquid scrip, a day (₹)'], ['expiry_pl_rupees', 'Expiry-day round trip (₹)']];
    confirmModal('Thresholds', 'Risk-based: the firm’s own choice.', f.map(([k, l]) => `<div class="fld"><label for="b6t-${k}">${l}</label><input class="in mono" id="b6t-${k}" value="${esc(String(c[k] ?? ''))}"></div>`).join(''), 'Save',
      () => { const b = {}; f.forEach(([k]) => { const v = b6Val('b6t-' + k).trim(); b[k] = k === 'wash_price_pct' ? v : (+v || 0); }); return act(() => post('/v1/surveillance/b6/settings', b), 'Saved'); }); }
});
