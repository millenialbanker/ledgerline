'use strict';
/* Ledgerline console: CDSL's pledge and early pay-in APIs. Staff screens
   "cdslpledge" (Pledges (CDSL): margin pledges, re-pledge, pledges to
   lenders, unpledge and invocation, settings) and "cdslepi" (Early pay-in),
   in the Collateral group; client portal page "ppledge" (Pledge shares:
   ask, confirm with CDSL's OTP, release). Loaded after app.js. */

Object.assign(UI, { cplTab: 'margin', cplSel: new Set(), epiSel: new Set() });
const CPL_ST = { Pending: 'warn', 'Awaiting client': 'acc', Accepted: 'good', Done: 'good', Rejected: 'nt', Failed: 'bad', 'Partly done': 'warn', Sent: 'acc', 'No reply': 'bad',
  'Awaiting compliance': 'warn', 'Notice given': 'acc', Cancelled: 'nt', Abandoned: 'nt' };
const cplPill = s => pill(s, CPL_ST[s] || 'nt');
const cplStamp = t => { const d = new Date(t); return isNaN(d) ? esc(t || '') : d.toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'short' }); };
const cplChk = () => has('Ops checker', 'Risk manager');
const cplConn = d => d.connected ? `<div class="note">CDSL APIs: ${pill(d.env || 'Mock', d.env === 'Live' ? 'good' : 'warn')}</div>`
  : `<div class="note warn"><b>Not connected.</b> ${esc(d.problem || '')} Requests can be entered; nothing goes to CDSL until the connection and its switch are on.</div>`;

/* ---------- Pledges (CDSL) ---------- */
V.cdslpledge = {
  load: () => get(`/v1/cdsl-pledges?date=${S.date}`),
  html: d => {
    const pend = d.pledges.filter(p => p.status === 'Pending').length, wait = d.pledges.filter(p => p.status === 'Awaiting client').length;
    const rel = d.releases.filter(r => ['Pending', 'Awaiting compliance', 'Notice given', 'No reply'].includes(r.status)).length;
    return ph('Pledges (CDSL)', 'Margin pledges sent through CDSL’s API: the client asks, a checker approves, the client confirms on CDSL’s page with the OTP CDSL sends, and CDSL’s answer marks the shares pledged for collateral and RMS. Also re-pledge to the clearing corporation, pledges to lenders, unpledge and invocation.',
      cplChk() ? '<button class="btn pri" data-act="cplNew">New margin pledge</button>' : '') + cplConn(d) +
      `<div class="kpis">${kpi('To approve', nf(pend), 'margin pledge requests', pend ? 'warn' : '')}${kpi('With the client', nf(wait), 'awaiting CDSL OTP')}${kpi('PSNs', nf(d.repledgable.length), 'with quantity to re-pledge')}${kpi('Releases open', nf(rel), 'unpledge and invocation', rel ? 'warn' : '')}${kpi('May be invoked', nf(d.candidates.filter(c => !c.raised).length), 'overdue debits and MTF', d.candidates.length ? 'bad' : '')}</div>
      <div class="panel"><div class="hd">${segBtns('cplTab', [['margin', 'Margin pledges'], ['repledge', 'Re-pledge'], ['normal', 'Pledges to lenders'], ['release', 'Unpledge & invocation'], ['settings', 'Settings']])}</div></div>` +
      ({ margin: cplMargin, repledge: cplRepledge, normal: cplNormal, release: cplRelease, settings: cplSettings }[UI.cplTab] || cplMargin)(d);
  }
};

function cplMargin(d) {
  const rows = d.pledges.map(p => `<tr><td class="mono">${esc(p.id)}</td><td><b>${esc(p.ucc)}</b></td><td>${esc(p.symbol)} <span class="dim small mono">${esc(p.isin)}</span></td><td class="r">${nf(p.qty)}</td><td>${esc(p.kind || 'MP')}${p.mtf_id ? `<div class="dim small">${esc(p.mtf_id)}</div>` : ''}</td>
    <td>${cplPill(p.status)}${p.reason ? `<div class="dim small wrap">${esc(p.reason)}</div>` : ''}${p.err_code ? `<div class="small neg">${esc(p.err_code)}</div>` : ''}</td><td class="mono">${esc(p.psn || '')}</td><td class="small">${esc(p.time)}</td>
    <td class="nowrap">${p.status === 'Pending' && cplChk() ? `<button class="btn sm pri" data-act="cplDecide" data-id="${esc(p.id)}" data-a="1">Approve</button><button class="btn sm" data-act="cplDecide" data-id="${esc(p.id)}">Reject</button>` : ''}</td></tr>`);
  const calls = d.calls.map(c => `<tr><td class="mono small">${esc(c.req_id)}</td><td>${esc(c.kind)}</td><td>${esc(c.ucc || '')}</td><td class="small">${esc(c.ids.join(', '))}</td><td>${cplPill(c.status)}<div class="dim small">${esc(c.result || '')}</div></td><td class="small">${cplStamp(c.at)}${c.inquiries ? `<div class="dim">${c.inquiries} inquiries</div>` : ''}</td>
    <td>${c.status === 'Sent' && cplChk() ? `<button class="btn sm" data-act="cplInquire" data-id="${esc(c.req_id)}">Ask CDSL</button>` : ''}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Margin pledge requests</h3><small>approval, then the client’s OTP at CDSL</small></div>${tbl([['Request'], ['Client'], ['Scrip'], ['Qty', 'r'], ['Type'], ['Status'], ['PSN'], ['Asked'], ['']], rows, { tall: true, empty: 'No margin pledges through CDSL yet.' })}</div>
    <div class="panel"><div class="hd"><h3>Hand-overs to CDSL</h3><small>each one the client opened; unanswered ones are asked about every few minutes</small></div>${tbl([['Request ID'], ['Type'], ['Client'], ['Requests'], ['Status'], ['Sent'], ['']], calls, { empty: 'Nothing sent yet.' })}</div>`;
}

function cplRepledge(d) {
  const av = d.repledgable.map(a => `<tr><td><input type="checkbox" data-cplsel="${esc(a.psn)}" ${UI.cplSel.has(a.psn) ? 'checked' : ''}></td><td class="mono">${esc(a.psn)}</td><td><b>${esc(a.ucc)}</b></td><td>${esc(a.symbol)}</td><td class="r">${nf(a.free)}</td><td class="r">${nf(a.repledged)}</td><td class="r"><b>${nf(a.free - a.repledged)}</b></td></tr>`);
  const bs = d.repledges.map(b => `<tr><td class="mono">${esc(b.id)}</td><td class="r">${b.lines.length}</td><td class="r">${nf(sum(b.lines, l => l.qty))}</td><td>${cplPill(b.status)}<div class="dim small wrap">${esc(b.result || b.reason || '')}</div>${b.lines.filter(l => l.err_msg).map(l => `<div class="small neg">${esc(l.psn)}: ${esc(l.err_msg)}</div>`).join('')}</td><td class="small">${esc(b.maker)}${b.checker ? ' / ' + esc(b.checker) : ''}</td>
    <td class="nowrap">${b.status === 'Pending' && cplChk() ? `<button class="btn sm pri" data-act="cplRpDecide" data-id="${esc(b.id)}" data-a="1">Send</button><button class="btn sm" data-act="cplRpDecide" data-id="${esc(b.id)}">Reject</button>` : ''}${b.status === 'No reply' && cplChk() ? `<button class="btn sm" data-act="cplResolve" data-id="${esc(b.id)}">Record result</button>` : ''}</td></tr>`);
  const rc = d.recon.map(r => `<tr><td><b>${esc(r.ucc)}</b></td><td>${esc(r.symbol)} <span class="dim small mono">${esc(r.isin)}</span></td><td class="r">${nf(r.pledged)}</td><td class="r">${nf(r.repledged)}</td><td class="r">${r.accepted < 0 ? '—' : nf(r.accepted)}</td><td class="r ${r.diff ? 'neg' : ''}">${r.accepted < 0 ? '' : nf(r.diff)}</td><td class="small wrap">${esc(r.note || '')}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Margin pledges to re-pledge</h3><div class="tools">${has('Risk manager', 'Ops maker') ? '<button class="btn pri" data-act="cplRpNew">Re-pledge selected</button>' : ''}</div></div>${tbl([[''], ['PSN'], ['Client'], ['Scrip'], ['Pledged', 'r'], ['Re-pledged', 'r'], ['Left', 'r']], av, { empty: 'Nothing left to re-pledge.' })}</div>
    <div class="panel"><div class="hd"><h3>Re-pledges</h3><small>maker, then a checker sends</small></div>${tbl([['Batch'], ['PSNs', 'r'], ['Qty', 'r'], ['Status'], ['By'], ['']], bs, { empty: 'No re-pledges yet.' })}</div>
    <div class="panel"><div class="hd"><h3>Against NSE Clearing’s re-pledge status file</h3><small>${d.recon_loaded ? 'file of ' + fmtDate(d.date) + ' loaded' : 'load the day’s re-pledge status file (Files) to compare'}</small></div>${tbl([['Client'], ['Scrip'], ['Pledged to firm', 'r'], ['Re-pledged (API)', 'r'], ['Accepted (NCL)', 'r'], ['Difference', 'r'], ['']], rc, { empty: 'Nothing re-pledged through the API.' })}</div>`;
}

function cplNormal(d) {
  const rows = d.normal.map(n => `<tr><td class="mono">${esc(n.id)}</td><td><b>${esc(n.ucc)}</b><div class="dim small">${esc(n.name)}</div></td><td>${esc(n.lender)}<div class="dim small mono">${esc(n.pledgee_bo_id)}</div></td>
    <td class="small">${n.lines.map(l => `${nf(l.qty)} ${esc(l.symbol)}${l.psn ? ' · PSN ' + esc(l.psn) : ''}${l.err_msg ? ` <span class="neg">${esc(l.err_msg)}</span>` : ''}`).join('<br>')}</td><td class="n">${fmtDate(n.expiry)}</td><td>${cplPill(n.status)}<div class="dim small wrap">${esc(n.result || n.reason || '')}</div></td><td class="small">${esc(n.maker)}${n.checker ? ' / ' + esc(n.checker) : ''}</td>
    <td class="nowrap">${n.status === 'Pending' && has('Ops checker') ? `<button class="btn sm pri" data-act="cplNpDecide" data-id="${esc(n.id)}" data-a="1">Send</button><button class="btn sm" data-act="cplNpDecide" data-id="${esc(n.id)}">Reject</button>` : ''}${n.status === 'No reply' && cplChk() ? `<button class="btn sm" data-act="cplResolve" data-id="${esc(n.id)}">Record result</button>` : ''}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Pledges to lenders</h3><div class="tools">${has('Ops maker', 'Ops checker') ? '<button class="btn pri" data-act="cplNpNew">New pledge to a lender</button>' : ''}</div></div>
    <div class="bd"><span class="dim small">Loan against shares: the client’s signed pledge form, entered by a DP maker and sent by a DP checker. These shares are not margin with the firm.</span></div>${tbl([['Pledge'], ['Client'], ['Lender'], ['Securities'], ['Expiry'], ['Status'], ['By'], ['']], rows, { tall: true, empty: 'No pledges to lenders.' })}</div>`;
}

function cplRelease(d) {
  const rows = d.releases.map(r => {
    let b = '';
    if (r.kind === 'UN' && r.status === 'Pending' && cplChk()) b = `<button class="btn sm pri" data-act="cplUnDecide" data-id="${esc(r.id)}" data-a="1">Send</button><button class="btn sm" data-act="cplUnDecide" data-id="${esc(r.id)}">Reject</button>`;
    if (r.kind === 'CF' && r.status === 'Awaiting compliance' && has('Compliance officer')) b = `<button class="btn sm pri" data-act="cplCfDecide" data-id="${esc(r.id)}" data-a="1">Approve, send notice</button><button class="btn sm" data-act="cplCfDecide" data-id="${esc(r.id)}">Reject</button>`;
    if (r.kind === 'CF' && r.status === 'Notice given' && has('Risk manager') && new Date(r.due_at) <= new Date()) b = `<button class="btn sm bad" data-act="cplInvoke" data-id="${esc(r.id)}">Invoke now</button>`;
    if (r.status === 'No reply' && cplChk()) b = `<button class="btn sm" data-act="cplResolve" data-id="${esc(r.id)}">Record result</button>`;
    return `<tr><td class="mono">${esc(r.id)}</td><td>${r.kind === 'UN' ? 'Unpledge' : 'Invocation'}<div class="dim small">${esc(r.source)}</div></td><td><b>${esc(r.ucc)}</b><div class="dim small">${esc(r.name)}</div></td><td class="mono">${esc(r.psn)}</td><td>${nf(r.qty)} ${esc(r.symbol)}</td>
      <td class="small wrap">${esc(r.reason)}${r.debit && num(r.debit) ? `<div class="dim">₹${esc(r.debit)}</div>` : ''}</td><td>${cplPill(r.status)}${r.due_at && r.status === 'Notice given' ? `<div class="dim small">until ${cplStamp(r.due_at)}</div>` : ''}${r.err_msg ? `<div class="small neg wrap">${esc(r.err_msg)}</div>` : ''}${r.note ? `<div class="dim small wrap">${esc(r.note)}</div>` : ''}</td><td class="nowrap">${b}</td></tr>`;
  });
  const cs = d.candidates.map(c => `<tr><td><b>${esc(c.ucc)}</b><div class="dim small">${esc(c.name)}</div></td><td>${esc(c.source)}</td><td class="small wrap">${esc(c.why)}</td><td class="mono">${esc(c.psn)}</td><td>${esc(c.symbol)}</td><td class="r">${nf(c.free)}</td><td class="r">${nf(c.qty)}<div class="dim small">₹${esc(c.value)}</div></td>
    <td>${c.raised ? `<span class="dim">${esc(c.raised)}</span>` : has('Risk manager') ? `<button class="btn sm bad" data-act="cplCfNew" data-id="${esc(c.psn)}" data-q="${c.qty}">Raise invocation</button>` : ''}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Pledges that may be invoked</h3><small>overdue debits past the blocking period, and MTF positions to liquidate</small></div>${tbl([['Client'], ['From'], ['Why'], ['PSN'], ['Scrip'], ['Pledged', 'r'], ['Suggested', 'r'], ['']], cs, { empty: 'None today.' })}
    <div class="bd"><span class="dim small">Invocation: risk raises it, compliance approves it and the client is sent notice; when the notice runs out and the dues are still unpaid it goes to CDSL, and the shares move to the firm’s account to be sold.</span></div></div>
    <div class="panel"><div class="hd"><h3>Unpledge and invocation</h3><div class="tools">${cplChk() ? '<button class="btn" data-act="cplUnNew">Unpledge for a client</button>' : ''}</div></div>${tbl([['Ref'], ['Kind'], ['Client'], ['PSN'], ['Qty'], ['Reason'], ['Status'], ['']], rows, { tall: true, empty: 'No releases yet.' })}</div>`;
}

const CPL_FIELDS = [['pledgee_bo_id', 'Margin pledgee BO ID (client securities margin pledge account)'], ['mtf_pledgee_bo_id', 'MTF pledgee BO ID (client securities under margin funding)'], ['entity', 'Entity (TM or CP)'], ['tm_id', 'TM ID'], ['cm_id', 'CM ID'],
  ['ex_id', 'Exchange ID (CDSL’s 2 digits)'], ['segment_id', 'Segment ID'], ['reason_code', 'Pledge reason code'], ['expiry_days', 'Pledge expiry (days)'], ['repledgee_bo_id', 'Re-pledge pledgee BO ID'], ['repledge_cm_id', 'Re-pledge CM ID'], ['repledge_reason', 'Re-pledge reason code'],
  ['repledge_type', 'Pledge type re-pledged (MP or MR)'], ['normal_reason', 'Normal pledge reason code'], ['notice_days', 'Invocation notice (days)'], ['epi_bo_id', 'Early pay-in BO ID'], ['epi_cm_id', 'Early pay-in CM ID'], ['epi_entity', 'Early pay-in entity (TM, CP, PM, NA)'],
  ['epi_ex_id', 'Early pay-in exchange ID'], ['epi_segment', 'Early pay-in segment ID'], ['epi_tmcp_id', 'Early pay-in TM/CP ID'], ['ddpi_poa_id', 'Firm’s DDPI POA ID']];
function cplSettings(d) {
  const s = d.settings, can = has('Compliance officer', 'Risk manager');
  return `<div class="panel"><div class="hd"><h3>The firm’s details in CDSL’s pledge and early pay-in APIs</h3><small>${s.updated_by ? 'changed by ' + esc(s.updated_by) + ' ' + cplStamp(s.updated_at) : 'not saved yet'}</small></div>
    <div class="bd"><div class="form" style="grid-template-columns:repeat(3,1fr)">${CPL_FIELDS.map(([k, l]) => `<div class="fld"><label for="cpl-${k}">${esc(l)}</label><input class="in mono" id="cpl-${k}" value="${esc(s[k] ?? '')}" ${can ? '' : 'disabled'}></div>`).join('')}</div>
    <div class="note">The DP ID, environment, API key and the switch for each API family are on Connections → CDSL APIs (DP).</div>${can ? '<div class="tools"><button class="btn pri" data-act="cplSave">Save</button></div>' : ''}</div></div>`;
}

/* ---------- Early pay-in ---------- */
V.cdslepi = {
  load: () => get(`/v1/cdsl-epi?date=${S.date}`),
  html: d => {
    const pend = d.records.filter(r => r.status === 'Pending'), acc = d.records.filter(r => r.status === 'Accepted');
    const sells = d.sells.map(x => `<tr><td><b>${esc(x.ucc)}</b><div class="dim small">${esc(x.name)}</div></td><td>${esc(x.symbol)} <span class="dim small">${esc(x.exchange)}</span></td><td class="r">${nf(x.sold)}</td><td class="r">${nf(x.entered)}</td><td class="r">${nf(x.free)}</td><td>${x.ddpi ? pill('DDPI', 'good') : pill('DIS needed', 'warn')}</td>
      <td>${has('Ops maker', 'Ops checker') && x.sold > x.entered ? `<button class="btn sm" data-act="epiNew" data-id="${esc(x.ucc)}|${esc(x.isin)}|${Math.min(x.sold - x.entered, x.free)}|${esc(x.exchange)}|${x.ddpi ? 1 : 0}">Enter</button>` : ''}</td></tr>`);
    const recs = d.records.map(r => `<tr><td>${r.status === 'Pending' ? `<input type="checkbox" data-episel="${esc(r.id)}" ${UI.epiSel.has(r.id) ? 'checked' : ''}>` : ''}</td><td><b>${esc(r.ucc)}</b><div class="dim small mono">${esc(r.bo_id)}</div></td><td>${esc(r.symbol)} <span class="dim small mono">${esc(r.isin)}</span></td><td class="r">${nf(r.qty)}</td><td class="mono small">${esc(r.sett_id)}<div class="dim">${esc(r.exchange)}</div></td>
      <td>${esc(r.flag)}${r.dis_no ? `<div class="dim small">${esc(r.dis_no)}</div>` : ''}</td><td>${cplPill(r.status)}${r.err_msg ? `<div class="small neg wrap">${esc(r.err_code || '')} ${esc(r.err_msg)}</div>` : ''}</td><td class="mono">${esc(r.ep_txn_id || '')}${r.txn_status ? `<div class="dim small">${esc(r.txn_status)}</div>` : ''}</td>
      <td class="small">${esc(r.maker)}${r.checker ? ' / ' + esc(r.checker) : ''}</td><td>${['Sent', 'No reply'].includes(r.status) && r.bo_req_seq ? `<button class="btn sm" data-act="epiInquire" data-id="${esc(r.bo_req_seq)}">Ask CDSL</button>` : ''}</td></tr>`);
    return ph('Early pay-in', 'Shares a client sells, paid in from the demat account to the clearing member’s early pay-in account on the trade day through CDSL’s API. Accepted early pay-ins are not short delivery in live risk and feed credit against sale and SEGMON. A DP maker enters them, a DP checker sends them; CDSL answers at once, or is asked again.',
      has('Ops maker', 'Ops checker') ? '<button class="btn" data-act="epiNew">Enter early pay-in</button>' : '') + cplConn(d) +
      `<div class="kpis">${kpi('Delivery sells', nf(d.sells.length), 'client-scrip lines today')}${kpi('To send', nf(pend.length), 'entered, awaiting a checker', pend.length ? 'warn' : '')}${kpi('Accepted', nf(acc.length), nf(sum(acc, r => r.qty)) + ' shares', acc.length ? 'good' : '')}${kpi('Settlements', nf(d.settlements.length), esc(d.settlements.join(', ') || 'load CDSL’s calendar'))}</div>
      <div class="panel"><div class="hd"><h3>Today’s delivery sells</h3><small>net of buys</small></div>${tbl([['Client'], ['Scrip'], ['Sold', 'r'], ['Entered', 'r'], ['Free in demat', 'r'], ['Authority'], ['']], sells, { empty: 'No delivery sells on this day.' })}</div>
      <div class="panel"><div class="hd"><h3>Early pay-ins</h3><div class="tools">${has('Ops checker') && pend.length ? '<button class="btn pri" data-act="epiSend">Send selected</button><button class="btn" data-act="epiReject">Reject selected</button>' : ''}</div></div>${tbl([[''], ['Client'], ['Scrip'], ['Qty', 'r'], ['Settlement'], ['Flag'], ['Status'], ['EP txn'], ['By'], ['']], recs, { tall: true, empty: 'No early pay-ins on this day.' })}</div>`;
  }
};

/* ---------- client portal: Pledge shares ---------- */
V.ppledge = {
  load: async () => { const v = await portalLoad(); v.pl = await get(`/v1/portal/pledges?ucc=${encodeURIComponent(v.account.ucc)}`); return v; },
  html: v => {
    const d = v.pl;
    const hs = d.holdings.map(h => `<tr><td><b>${esc(h.symbol)}</b><div class="dim small mono">${esc(h.isin)}</div></td><td class="r">${nf(h.qty)}</td><td class="r">${nf(h.pledged)}</td><td class="r">${nf(h.free)}</td><td>${h.free > 0 && d.connected ? `<button class="btn sm" data-act="pplAsk" data-id="${esc(h.isin)}" data-q="${h.free}">Pledge</button>` : ''}</td></tr>`);
    const ps = d.psns.map(a => `<tr><td class="mono">${esc(a.psn)}</td><td>${esc(a.symbol)}</td><td class="r">${nf(a.free)}</td><td><button class="btn sm" data-act="pplRelease" data-id="${esc(a.psn)}" data-q="${a.free}">Release</button></td></tr>`);
    const rq = d.requests.map(p => `<tr><td class="mono">${esc(p.id)}</td><td>${nf(p.qty)} ${esc(p.symbol)}</td><td>${cplPill(p.status === 'Awaiting client' ? 'Confirm with CDSL' : p.status)}${p.reason ? `<div class="dim small wrap">${esc(p.reason)}</div>` : ''}</td><td class="mono">${esc(p.psn || '')}</td><td class="small">${esc(p.time)}</td></tr>`);
    return pHead(v, 'Pledge shares', 'Pledge shares from your demat account as margin with us. The shares stay in your account. After we approve a request, you confirm it on CDSL’s own page with the one-time password CDSL sends to your registered mobile and email.') +
      (d.connected ? '' : '<div class="note warn">Pledging through CDSL is not available just now.</div>') +
      (d.awaiting ? `<div class="note"><b>${d.awaiting} approved pledge${d.awaiting > 1 ? 's' : ''} waiting for you.</b> <button class="btn pri" data-act="pplConfirm">Confirm with CDSL</button></div>` : '') +
      `<div class="panel"><div class="hd"><h3>Your shares</h3></div>${tbl([['Scrip'], ['Held', 'r'], ['Pledged', 'r'], ['Free', 'r'], ['']], hs, { empty: 'No shares in your demat account.' })}</div>
      <div class="panel"><div class="hd"><h3>Pledged as margin</h3><small>release shares you no longer need as margin</small></div>${tbl([['PSN'], ['Scrip'], ['Pledged', 'r'], ['']], ps, { empty: 'Nothing pledged through CDSL.' })}</div>
      <div class="panel"><div class="hd"><h3>Your requests</h3></div>${tbl([['Request'], ['Shares'], ['Status'], ['PSN'], ['Asked']], rq, { empty: 'No requests yet.' })}</div>`;
  }
};

const cplReason = (id, title, sub, fn) => confirmModal(title, sub, '<div class="fld"><label for="cpl-r">Reason</label><input class="in" id="cpl-r"></div>', 'Reject', () => act(() => fn($('cpl-r').value), 'Rejected'), true);
const cplQtyField = (q, label) => `<div class="fld"><label for="cpl-q">${label || 'Quantity'}</label><input class="in mono" id="cpl-q" inputmode="numeric" value="${q || ''}"></div>`;

Object.assign(A, {
  cplDecide: (id, el) => el.dataset.a === '1' ? act(() => post(`/v1/cdsl-pledges/margin/${encodeURIComponent(id)}/decide`, { accept: true }), 'Approved; the client confirms with CDSL’s OTP')
    : cplReason(id, 'Reject pledge ' + id, '', r => post(`/v1/cdsl-pledges/margin/${encodeURIComponent(id)}/decide`, { accept: false, reason: r })),
  cplNew: () => confirmModal('New margin pledge', 'For a client who asked at the desk. A checker approves it; the client then confirms with CDSL’s OTP from the portal.',
    `<div class="grid g2"><div class="fld"><label for="cpl-u">Client code</label><input class="in mono" id="cpl-u"></div><div class="fld"><label for="cpl-i">ISIN</label><input class="in mono" id="cpl-i"></div></div>${cplQtyField()}
     <div class="grid g2"><div class="fld"><label for="cpl-k">Type</label><select class="in" id="cpl-k"><option value="MP">Margin pledge</option><option value="MF">MTF pledge</option></select></div><div class="fld"><label for="cpl-m">MTF position (for an MTF pledge)</label><input class="in mono" id="cpl-m"></div></div>`, 'Enter',
    () => act(() => post('/v1/cdsl-pledges/margin', { ucc: $('cpl-u').value.trim().toUpperCase(), kind: $('cpl-k').value, mtf_id: $('cpl-m').value.trim(), lines: [{ isin: $('cpl-i').value.trim().toUpperCase(), qty: parseInt($('cpl-q').value || '0', 10) }] }), 'Pledge request entered')),
  cplInquire: id => act(() => post(`/v1/cdsl-pledges/calls/${encodeURIComponent(id)}/inquire`), c => 'CDSL: ' + (c.result || c.status)),
  cplRpNew: () => { if (!UI.cplSel.size) { toast('Tick the pledges to re-pledge', true); return; } act(() => post('/v1/cdsl-pledges/repledges', { lines: [...UI.cplSel].map(psn => ({ psn })) }), b => `${b.id}: ${b.lines.length} pledges for a checker`).then(() => UI.cplSel.clear()).catch(() => {}); },
  cplRpDecide: (id, el) => el.dataset.a === '1' ? act(() => post(`/v1/cdsl-pledges/repledges/${encodeURIComponent(id)}/decide`, { accept: true }), b => `${b.id}: ${b.result || b.status}`)
    : cplReason(id, 'Reject re-pledge ' + id, '', r => post(`/v1/cdsl-pledges/repledges/${encodeURIComponent(id)}/decide`, { accept: false, reason: r })),
  cplNpNew: () => confirmModal('Pledge to a lender', 'From the client’s signed pledge form. A DP checker sends it to CDSL.',
    `<div class="grid g2"><div class="fld"><label for="np-u">Client code</label><input class="in mono" id="np-u"></div><div class="fld"><label for="np-l">Lender</label><input class="in" id="np-l"></div>
     <div class="fld"><label for="np-b">Lender’s (pledgee) BO ID</label><input class="in mono" id="np-b" maxlength="16"></div><div class="fld"><label for="np-a">Agreement number</label><input class="in mono" id="np-a" maxlength="16"></div>
     <div class="fld"><label for="np-e">Expiry</label><input class="in mono" type="date" id="np-e"></div></div>
     <div class="fld"><label for="np-x">Securities, one per line: ISIN quantity</label><textarea class="in mono" id="np-x" rows="4" placeholder="INE000A01000 100"></textarea></div>`, 'Enter',
    () => act(() => post('/v1/cdsl-pledges/normal', { ucc: $('np-u').value.trim().toUpperCase(), lender: $('np-l').value, pledgee_bo_id: $('np-b').value.trim(), agreement: $('np-a').value.trim(), expiry: $('np-e').value,
      lines: $('np-x').value.split('\n').map(s => s.trim().split(/[\s,]+/)).filter(p => p[0]).map(p => ({ isin: p[0].toUpperCase(), qty: parseInt(p[1] || '0', 10) })) }), n => `${n.id} entered for a checker`)),
  cplNpDecide: (id, el) => el.dataset.a === '1' ? act(() => post(`/v1/cdsl-pledges/normal/${encodeURIComponent(id)}/decide`, { accept: true }), n => `${n.id}: ${n.result || n.status}`)
    : cplReason(id, 'Reject pledge ' + id, '', r => post(`/v1/cdsl-pledges/normal/${encodeURIComponent(id)}/decide`, { accept: false, reason: r })),
  cplUnNew: () => confirmModal('Unpledge for a client', 'On the client’s request. A checker sends it.', `<div class="grid g2"><div class="fld"><label for="un-u">Client code</label><input class="in mono" id="un-u"></div><div class="fld"><label for="un-p">PSN</label><input class="in mono" id="un-p"></div></div>${cplQtyField('', 'Quantity (empty: all)')}`, 'Enter',
    () => act(() => post('/v1/cdsl-pledges/unpledges', { ucc: $('un-u').value.trim().toUpperCase(), psn: $('un-p').value.trim(), qty: parseInt($('cpl-q').value || '0', 10) }), r => `${r.id} entered for a checker`)),
  cplUnDecide: (id, el) => el.dataset.a === '1' ? act(() => post(`/v1/cdsl-pledges/unpledges/${encodeURIComponent(id)}/decide`, { accept: true }), r => `${r.id}: ${r.status}`)
    : cplReason(id, 'Reject unpledge ' + id, '', r => post(`/v1/cdsl-pledges/unpledges/${encodeURIComponent(id)}/decide`, { accept: false, reason: r })),
  cplCfNew: (psn, el) => confirmModal('Raise invocation · PSN ' + psn, 'Compliance approves it and the client is sent notice before anything goes to CDSL.', cplQtyField(el.dataset.q) + '<div class="fld"><label for="cpl-r">Reason</label><input class="in" id="cpl-r"></div>', 'Raise',
    () => act(() => post('/v1/cdsl-pledges/invocations', { date: S.date, psn, qty: parseInt($('cpl-q').value || '0', 10), reason: $('cpl-r').value }), r => `${r.id} raised for compliance`), true),
  cplCfDecide: (id, el) => el.dataset.a === '1' ? act(() => post(`/v1/cdsl-pledges/invocations/${encodeURIComponent(id)}/decide`, { accept: true }), 'Approved; notice sent to the client')
    : cplReason(id, 'Reject invocation ' + id, '', r => post(`/v1/cdsl-pledges/invocations/${encodeURIComponent(id)}/decide`, { accept: false, reason: r })),
  cplInvoke: id => confirmModal('Invoke ' + id, 'The notice period is over. The pledged shares move to the firm’s account.', '', 'Invoke', () => act(() => post(`/v1/cdsl-pledges/invocations/${encodeURIComponent(id)}/invoke`), r => `${r.id}: ${r.status}`), true),
  cplResolve: id => confirmModal('Record CDSL’s result · ' + id, 'CDSL did not answer. Look the request up in CDSL first.', `<div class="fld"><label for="rs-d">At CDSL it is</label><select class="in" id="rs-d"><option value="1">Done</option><option value="0">Not done</option></select></div>
    <div class="fld"><label for="rs-p">PSN (pledge to a lender)</label><input class="in mono" id="rs-p"></div><div class="fld"><label for="rs-n">Where it was checked</label><input class="in" id="rs-n"></div>`, 'Record',
    () => act(() => post(`/v1/cdsl-pledges/resolve/${encodeURIComponent(id)}`, { done: $('rs-d').value === '1', psn: $('rs-p').value.trim(), note: $('rs-n').value }), 'Recorded')),
  cplSave: () => { const s = { ...C.cdslpledge.settings }; CPL_FIELDS.forEach(([k]) => { const v = $('cpl-' + k).value.trim(); s[k] = /_days$/.test(k) ? parseInt(v || '0', 10) : v; }); act(() => api('PUT', '/v1/cdsl-pledges/settings', s), 'Saved'); },
  epiNew: (id) => {
    const [ucc, isin, q, ex, ddpi] = (id || '||||').split('|'), st = C.cdslepi?.settlements || [];
    confirmModal('Enter early pay-in', 'A DP checker sends it to CDSL.', `<div class="grid g2"><div class="fld"><label for="ep-u">Client code</label><input class="in mono" id="ep-u" value="${esc(ucc)}"></div><div class="fld"><label for="ep-i">ISIN</label><input class="in mono" id="ep-i" value="${esc(isin)}"></div>
      <div class="fld"><label for="cpl-q">Quantity</label><input class="in mono" id="cpl-q" value="${esc(q)}"></div><div class="fld"><label for="ep-x">Exchange</label><input class="in mono" id="ep-x" value="${esc(ex || 'NSE')}"></div>
      <div class="fld"><label for="ep-s">CDSL settlement ID (13 digits)</label><input class="in mono" id="ep-s" list="ep-sl" value="${st.length === 1 ? esc(st[0]) : ''}"><datalist id="ep-sl">${st.map(s => `<option value="${esc(s)}">`).join('')}</datalist></div>
      <div class="fld"><label for="ep-f">Authority</label><select class="in" id="ep-f"><option value="">${ddpi === '1' ? 'DDPI (D)' : 'Slip or eDIS'}</option><option>D</option><option>N</option><option>Y</option><option>E</option><option>G</option></select></div>
      <div class="fld"><label for="ep-d">DIS / eDIS number (without DDPI)</label><input class="in mono" id="ep-d" maxlength="16"></div></div>`, 'Enter',
      () => act(() => post('/v1/cdsl-epi', { lines: [{ date: S.date, ucc: $('ep-u').value.trim().toUpperCase(), isin: $('ep-i').value.trim().toUpperCase(), qty: parseInt($('cpl-q').value || '0', 10), exchange: $('ep-x').value.trim(), sett_id: $('ep-s').value.trim(), flag: $('ep-f').value, dis_no: $('ep-d').value.trim() }] }), 'Entered for a checker'));
  },
  epiSend: () => { if (!UI.epiSel.size) { toast('Tick the early pay-ins to send', true); return; } act(() => post('/v1/cdsl-epi/decide', { ids: [...UI.epiSel], accept: true }), rs => `${rs.filter(r => r.status === 'Accepted').length} of ${rs.length} accepted by CDSL`).then(() => UI.epiSel.clear()).catch(() => {}); },
  epiReject: () => { if (!UI.epiSel.size) { toast('Tick the early pay-ins to reject', true); return; } cplReason('', 'Reject early pay-ins', '', r => post('/v1/cdsl-epi/decide', { ids: [...UI.epiSel], accept: false, reason: r }).then(x => { UI.epiSel.clear(); return x; })); },
  epiInquire: seq => act(() => post('/v1/cdsl-epi/inquire', { seq }), r => `${r.answered} answered by CDSL`),
  pplAsk: (isin, el) => confirmModal('Pledge as margin', 'We check the request; then you confirm it with CDSL’s OTP.', cplQtyField(el.dataset.q, `Shares to pledge (up to ${el.dataset.q})`), 'Ask',
    () => act(() => post('/v1/portal/pledges', { ucc: UI.pAcct, kind: 'MP', lines: [{ isin, qty: parseInt($('cpl-q').value || '0', 10) }] }), 'Request sent; we will tell you when to confirm it')),
  pplConfirm: async () => { try { const r = await post('/v1/portal/pledges/confirm', { ucc: UI.pAcct }); window.location.assign(r.url); } catch (e) { toast(e.message, true); } },
  pplRelease: (psn, el) => confirmModal('Release from pledge · PSN ' + psn, 'Shares still needed as margin may not be released.', cplQtyField(el.dataset.q, `Shares to release (up to ${el.dataset.q})`), 'Ask',
    () => act(() => post('/v1/portal/pledges/unpledge', { ucc: UI.pAcct, psn, qty: parseInt($('cpl-q').value || '0', 10) }), 'Release requested')),
});

document.addEventListener('change', e => {
  const t = e.target;
  if (t.dataset && t.dataset.cplsel) { t.checked ? UI.cplSel.add(t.dataset.cplsel) : UI.cplSel.delete(t.dataset.cplsel); }
  if (t.dataset && t.dataset.episel) { t.checked ? UI.epiSel.add(t.dataset.episel) : UI.epiSel.delete(t.dataset.episel); }
});
