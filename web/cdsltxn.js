'use strict';
/* Ledgerline console: CDSL APIs for the DP. Staff tabs in the Depository
   group: "edis" (eDIS authorisations, TPIN for clients) and "cdsltxn"
   (Transactions (API): verified DIS by the common upload, DESTAT, report
   setup); and the client portal page "pedis" (authorise a sale with the
   CDSL TPIN). A redirect to CDSL is a form the browser posts: CDSL's page
   takes the TPIN or OTP and sends the browser back. Loaded after app.js. */

Object.assign(UI, { cdxTab: 'dis', edisTab: 'auth' });
const cdxPill = s => pill(s, { Authorised: 'good', 'Partly authorised': 'warn', 'Awaiting TPIN': 'warn', 'Awaiting OTP': 'warn', Failed: 'bad', Cancelled: 'nt', 'No answer': 'bad',
  Sent: 'acc', Processing: 'warn', Done: 'good', Accepted: 'good', Taken: 'good', Received: 'good', Refused: 'bad', Error: 'bad', 'Sent to the client': 'good', Executed: 'good', Verified: 'acc' }[s] || 'nt');
const cdxStamp = t => { const d = new Date(t); return isNaN(d) ? esc(t || '') : d.toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'short' }); };
const cdxFam = { api_upload: 'Common upload', api_destat: 'DESTAT', api_edis: 'eDIS', api_report: 'Report setup' };
const cdxShares = a => a.lines.map(l => `${esc(l.symbol || l.isin)} ${nf(l.qty)}${l.cancelled ? ` <span class="dim">(${nf(l.cancelled)} cancelled)</span>` : ''}`).join(', ');

// cdslRedirect posts CDSL's form from this page: the browser goes to CDSL.
function cdslRedirect(f) {
  const fm = document.createElement('form');
  fm.method = 'post'; fm.action = f.action; fm.style.display = 'none';
  for (const [k, v] of Object.entries(f.fields || {})) { const i = document.createElement('input'); i.type = 'hidden'; i.name = k; i.value = v; fm.appendChild(i); }
  document.body.appendChild(fm);
  fm.submit();
}
function cdxGo(f) {
  modal(mhd('Going to CDSL', esc(f.note || '')) + '<div class="bd"><p>You will come back here when CDSL has finished.</p></div>');
  setTimeout(() => cdslRedirect(f), 300);
}

/* ---------- Transactions (API) ---------- */
V.cdsltxn = {
  load: () => get('/v1/cdsl/txn'),
  html: d => {
    const on = d.families || {}, fams = Object.entries(cdxFam).map(([k, l]) => `${l} ${on[k] ? pill('On', 'good') : pill('Off', 'nt')}`).join(' · ');
    const conn = d.connection === 'Set' ? `<div class="note">CDSL APIs: <b>${esc(d.environment)}</b>, DP ${esc(d.dp_id)}. ${fams}${d.last_error ? `<div class="neg small">Last problem: ${esc(d.last_error)}</div>` : ''}</div>`
      : '<div class="note warn"><b>The CDSL APIs connection is not set.</b> The admin enters it under Connections → CDSL APIs (DP). Until then, use the upload file below.</div>';
    return ph('Transactions (API)', 'Delivery instructions a checker has verified go to CDSL through the common upload API; CDSL\'s response file marks each one executed or failed. DESTAT sends mutual fund units held in a statement of account to CDSL for demat, and report setup asks CDSL for DP reports.') + conn +
      `<div class="kpis">${kpi('Verified, to send', nf(d.to_send.length), d.last_send ? 'last sent ' + cdxStamp(d.last_send) : '', d.to_send.length ? 'warn' : '')}${kpi('Uploads', nf(d.uploads.length), nf(d.uploads.filter(u => u.status === 'Sent' || u.status === 'Processing').length) + ' waiting for CDSL')}${kpi('DESTAT to send', nf(d.destat_open.length))}${kpi('Reports set up', nf(d.reports.length), nf(d.report_subscriptions.filter(s => s.active).length) + ' daily')}</div>` +
      `<div class="panel"><div class="hd">${segBtns('cdxTab', [['dis', 'Delivery instructions'], ['destat', 'DESTAT'], ['reports', 'Reports']])}</div></div>` +
      ({ dis: cdxDIS, destat: cdxDestat, reports: cdxReports }[UI.cdxTab] || cdxDIS)(d);
  }
};

function cdxDIS(d) {
  const can = has('Ops maker', 'Ops checker'), api = d.families?.api_upload;
  return `<div class="panel"><div class="hd"><h3>Verified, not yet sent</h3><small>only slips a checker has verified</small></div>
    <div class="bd"><div class="tools">${can ? `<button class="btn ${api ? 'pri' : ''}" data-act="cdxSend" ${api && d.to_send.length ? '' : 'disabled'}>Send to CDSL by API</button><button class="btn" data-act="cdxFile" ${d.to_send.length ? '' : 'disabled'}>Upload file for CDAS</button>` : ''}</div>
    ${api ? '<div class="dim small" style="margin-top:6px">The scheduler also sends verified slips every five minutes on business days, 08:00–18:30, and reads back CDSL\'s results.</div>' : ''}</div>
    ${tbl([['Slip'], ['Client'], ['Kind'], ['To'], ['ISINs', 'r'], ['Execution'], ['Checker']], d.to_send.map(x => `<tr><td class="mono">${esc(x.id)}<div class="dim small">#${x.slip}</div></td><td><b>${esc(x.ucc)}</b><div class="dim small">${esc(x.name)}</div></td><td>${esc(x.kind)}${x.settlement ? `<div class="dim small">${esc(x.exchange)} ${esc(x.settlement)}</div>` : ''}</td><td class="mono small">${esc(x.to_bo)}</td><td class="r">${x.lines.length}</td><td class="n">${fmtDate(x.exec_date)}</td><td>${esc(x.checker || '')}</td></tr>`), { empty: 'Nothing verified is waiting.' })}</div>
    <div class="panel"><div class="hd"><h3>Uploads through the API</h3></div>${tbl([['Upload'], ['Sent'], ['File'], ['Slips', 'r'], ['CDSL ack'], ['Status'], ['Results'], ['']], d.uploads.map(u => `<tr><td class="mono">${esc(u.id)}<div class="dim small">${esc(u.environment)}</div></td><td class="small">${cdxStamp(u.sent_at)}<div class="dim">${esc(u.sent_by)}</div></td>
      <td class="mono small">${u.file_id ? `<a href="#" data-act="dl" data-id="/v1/periodic/files/${encodeURIComponent(u.file_id)}" data-name="${esc(u.file)}">${esc(u.file)}</a>` : esc(u.file)}${u.response ? `<div class="dim">${esc(u.response)}</div>` : ''}</td><td class="r">${u.dis.length}</td><td class="mono">${esc(u.ack || '')}</td>
      <td>${cdxPill(u.status)}${u.cdsl_status ? `<div class="dim small">${esc(u.cdsl_status)}</div>` : ''}${u.problem ? `<div class="neg small wrap">${esc(u.problem)}</div>` : ''}</td>
      <td class="small wrap">${Object.entries(u.results || {}).map(([k, v]) => `${esc(k)}: ${esc(v)}`).join('<br>')}${u.unread_result ? '<div class="neg">Some lines need reading by hand</div>' : ''}</td>
      <td>${can && (u.status === 'Sent' || u.status === 'Processing') ? `<button class="btn sm" data-act="cdxCheck" data-id="${esc(u.id)}">Ask CDSL</button>` : ''}</td></tr>`), { empty: 'Nothing sent through the API yet.' })}</div>`;
}

function cdxDestat(d) {
  const can = has('Ops checker');
  return `<div class="panel"><div class="hd"><h3>Demat requests for units in a statement of account</h3><small>pending requests with a folio</small></div>
    <div class="bd dim small">A checker sends the request; CDSL shows it to the investor, who confirms with the OTP CDSL sends. CDSL returns a DESTAT request number, and the request then waits for the RTA. Raise the request on Demat accounts & DIS → Requests with the folio number.</div>
    ${tbl([['Request'], ['Client'], ['ISIN'], ['Folio'], ['Qty', 'r'], ['DRF'], ['']], d.destat_open.map(r => `<tr><td class="mono">${esc(r.id)}</td><td><b>${esc(r.ucc)}</b><div class="dim small">${esc(r.name)}</div></td><td class="mono">${esc(r.detail.isin)}</td><td class="mono">${esc(r.detail.folio)}</td><td class="r">${esc(r.detail.qty || 'All')}</td><td class="mono">${esc(r.detail.drf)}</td>
      <td>${can && d.families?.api_destat ? `<button class="btn sm pri" data-act="cdxDestat" data-id="${esc(r.id)}">Send to CDSL</button>` : ''}</td></tr>`), { empty: 'No pending demat requests with a folio.' })}</div>
    <div class="panel"><div class="hd"><h3>Sent to DESTAT</h3></div>${tbl([['Sent'], ['Request'], ['Client'], ['ISIN'], ['Qty', 'r'], ['Status'], ['DRN'], ['Reason']], d.destat.map(x => `<tr><td class="small">${cdxStamp(x.at)}</td><td class="mono">${esc(x.dp_request)}</td><td>${esc(x.ucc)}<div class="dim small mono">${esc(x.bo_id)}</div></td><td class="mono">${esc(x.isin)}<div class="dim small">${esc(x.folio)}</div></td><td class="r">${esc(x.qty)}</td><td>${cdxPill(x.status)}</td><td class="mono">${esc(x.drn || '')}</td><td class="small wrap">${esc(x.error || '')}</td></tr>`), { empty: 'Nothing sent yet.' })}</div>`;
}

function cdxReports(d) {
  const can = has('Ops maker', 'Ops checker', 'Compliance officer');
  return `<div class="grid2"><div class="panel"><div class="hd"><h3>Ask CDSL for a report</h3></div><div class="bd">
      ${can ? `<div class="grid g2"><div class="fld"><label for="cdx-mod">Module ID</label><input class="in mono" id="cdx-mod" inputmode="numeric" placeholder="8"></div><div class="fld"><label for="cdx-rep">Report ID</label><input class="in mono" id="cdx-rep" placeholder="DP97, DPZ6"></div></div>
      <div class="fld"><label for="cdx-par">Parameters, ~ separated</label><input class="in mono" id="cdx-par" placeholder="01112024_14112024~F"></div>
      <div class="dim small">DP97: from_to~F or ~I. DPZ6: a BO ID and six blanks, or account status, status code, sub status, date type (M or S), from, to. In daily setups, {prev} and {today} become dates.</div>
      <div class="tools" style="margin-top:8px"><button class="btn pri" data-act="cdxReport">Set up now</button><input class="in mono" id="cdx-at" style="width:90px" placeholder="HH:MM" aria-label="Daily at"><button class="btn" data-act="cdxSub">Set up daily at this time</button></div>` : '<div class="dim">Ops and compliance set up reports.</div>'}</div></div>
    <div class="panel"><div class="hd"><h3>Daily setups</h3><small>SFTP: ${esc(d.sftp)}</small></div>${tbl([['Setup'], ['Report'], ['Parameters'], ['At'], ['']], d.report_subscriptions.map(s => `<tr><td class="mono">${esc(s.id)}</td><td>${esc(s.module)} · ${esc(s.report)}</td><td class="mono small">${esc(s.param)}</td><td class="n">${esc(s.at)}</td><td>${can ? `<button class="btn sm" data-act="cdxSubToggle" data-id="${esc(s.id)}">${s.active ? 'Stop' : 'Start'}</button>` : ''}</td></tr>`), { empty: 'No daily setups.' })}
      ${can && !String(d.sftp).startsWith('Not set') && !String(d.sftp).includes('no SFTP client') ? '<div class="pad"><button class="btn sm" data-act="cdxSFTP">Fetch from SFTP now</button></div>' : ''}</div></div>
    <div class="panel"><div class="hd"><h3>Setups sent</h3></div>${tbl([['Sent'], ['Report'], ['Parameters'], ['Status'], ['Arrived']], d.reports.map(r => `<tr><td class="small">${cdxStamp(r.at)}<div class="dim">${esc(r.by)}</div></td><td>${esc(r.module)} · ${esc(r.report)}</td><td class="mono small">${esc(r.param)}</td><td>${cdxPill(r.status)}${r.error ? `<div class="neg small wrap">${esc(r.error)}</div>` : ''}</td><td class="small">${r.arrived ? cdxStamp(r.arrived) : ''}</td></tr>`), { empty: 'No reports asked for yet.' })}</div>
    <div class="panel"><div class="hd"><h3>Received from CDSL</h3></div>${tbl([['When'], ['How'], ['File'], ['Loaded']], (d.report_received || []).map(r => `<tr><td class="small">${cdxStamp(r.at)}</td><td>${esc(r.via)}</td><td class="mono small">${esc(r.name || '')}</td><td class="small wrap">${esc(r.loaded)}</td></tr>`), { empty: 'Nothing received yet.' })}</div>`;
}

/* ---------- eDIS authorisations (staff) ---------- */
V.edis = {
  load: () => get(`/v1/cdsl/edis?date=${S.date}`),
  html: d => {
    const can = has('Ops maker', 'Ops checker', 'Branch staff');
    const off = d.on ? '' : '<div class="note warn"><b>eDIS is not switched on.</b> The admin sets it up in Connections → CDSL APIs (DP) once CDSL has registered the DP for eDIS.</div>';
    const cm = d.cm_missing?.length && d.on ? `<div class="note warn">No clearing member ID for eDIS on ${d.cm_missing.map(esc).join(' and ')}: clients cannot authorise sales there.</div>` : '';
    return ph('eDIS authorisations', `Clients without a DDPI authorise a sale with the CDSL TPIN (and OTP) on CDSL's page: from the client portal, or here with the client present. Live risk counts only the authorised quantity as deliverable for them. Version ${esc(d.version || '1.2')}${d.environment ? ', ' + esc(d.environment) : ''}.`,
      can ? '<button class="btn pri" data-act="edisNew">Authorise for a client</button><button class="btn" data-act="edisTPIN">Generate TPIN</button><button class="btn" data-act="edisTPINChange">Change TPIN</button>' : '') + off + cm +
      `<div class="kpis">${kpi('Today', nf(d.today))}${kpi('Waiting for the TPIN', nf(d.waiting), 'CDSL is asked after 10 minutes', d.waiting ? 'warn' : '')}${kpi('TPIN requests', nf(d.pins.length))}${kpi('Cancellations', nf(d.revocations.length))}</div>` +
      `<div class="panel"><div class="hd">${segBtns('edisTab', [['auth', 'Authorisations'], ['pin', 'TPIN requests'], ['revoc', 'Cancellations']])}</div>` +
      ({ auth: edisAuthTbl, pin: edisPinTbl, revoc: edisRevocTbl }[UI.edisTab] || edisAuthTbl)(d) + '</div>';
  }
};

function edisAuthTbl(d) {
  const can = has('Ops maker', 'Ops checker', 'Risk manager');
  return tbl([['Request'], ['Client'], ['Type'], ['Shares'], ['Valid till'], ['Status'], ['CDSL'], ['']], d.auths.map(a => `<tr><td class="mono">${esc(a.req_id)}<div class="dim small">${cdxStamp(a.at)} · ${esc(a.via)}</div></td><td><b>${esc(a.ucc)}</b><div class="dim small">${esc(a.name)}</div></td>
    <td>${{ D: 'Pre-trade', E: 'Post-trade', F: 'Off-market' }[a.req_type] || esc(a.req_type)}${a.exchange ? ` <span class="dim small">${esc(a.exchange)}</span>` : ''}</td><td class="small">${cdxShares(a)}</td><td class="n">${fmtDate(a.valid_till)}</td>
    <td>${cdxPill(a.status)}${a.lines.filter(l => l.error).map(l => `<div class="neg small wrap">${esc(l.symbol || l.isin)}: ${esc(l.error)}</div>`).join('')}</td><td class="mono small">${a.lines.map(l => esc(l.txn_id || '')).filter(Boolean).join('<br>')}${a.answered_by ? `<div class="dim">by ${esc(a.answered_by.toLowerCase())}</div>` : ''}</td>
    <td class="nowrap">${can && a.status === 'Awaiting TPIN' && a.at.slice(0, 10) === S.date ? `<button class="btn sm" data-act="edisInquire" data-id="${esc(a.req_id)}">Ask CDSL</button>` : ''}${has('Ops maker', 'Ops checker', 'Branch staff') && a.req_type === 'D' && ['Authorised', 'Partly authorised'].includes(a.status) ? `<button class="btn sm" data-act="edisCancel" data-id="${esc(a.req_id)}">Cancel</button>` : ''}</td></tr>`), { tall: true, empty: 'No eDIS authorisations yet.' });
}
const edisPinTbl = d => tbl([['Request'], ['Client'], ['How'], ['Status'], ['Note']], d.pins.map(p => `<tr><td class="mono">${esc(p.req_id)}<div class="dim small">${cdxStamp(p.at)}</div></td><td>${esc(p.ucc)}</td><td>${esc(p.via)} · ${esc(p.by)}</td><td>${cdxPill(p.status)}${p.error ? `<div class="neg small wrap">${esc(p.error)}</div>` : ''}</td><td class="small wrap">${esc(p.note || '')}</td></tr>`), { empty: 'No TPIN requests.' });
const edisRevocTbl = d => tbl([['Cancellation'], ['Authorisation'], ['Client'], ['Status'], ['Result']], d.revocations.map(r => `<tr><td class="mono small">${esc(r.revoc_req_id)}<div class="dim">${cdxStamp(r.at)}</div></td><td class="mono">${esc(r.req_id)}</td><td>${esc(r.ucc)}</td><td>${cdxPill(r.status)}</td><td class="small wrap">${esc(r.result || '')}</td></tr>`), { empty: 'No cancellations.' });

/* ---------- client portal: authorise a sale ---------- */
V.pedis = {
  load: async () => { const v = await portalLoad(); v.edis = await get(`/v1/portal/edis?ucc=${encodeURIComponent(v.account.ucc)}`); return v; },
  html: v => {
    const p = v.edis;
    if (!p.on) return pHead(v, 'Authorise sale (CDSL TPIN)', 'Authorise the sale of shares from your demat account.') + `<div class="note">${esc(p.problem || 'Not available.')}</div>`;
    const ddpi = p.ddpi ? '<div class="note">You have given a DDPI, so you need not authorise each sale. You can still do it here.</div>' : '';
    const prob = p.problem ? `<div class="note warn">${esc(p.problem)}</div>` : '';
    return pHead(v, 'Authorise sale (CDSL TPIN)', `Before you sell shares from your demat account (${esc(p.bo_id)}), authorise the sale on CDSL's page with your CDSL TPIN and the OTP CDSL sends. A pre-trade authorisation lasts ${p.valid_days} day${p.valid_days > 1 ? 's' : ''}.`) + ddpi + prob +
      `<div class="panel"><div class="hd"><h3>Shares you can authorise</h3><div class="tools">${p.exchanges.length > 1 ? `<select class="in" id="pedis-ex" aria-label="Exchange">${p.exchanges.map(e => `<option>${e}</option>`).join('')}</select>` : `<span class="chip">${esc(p.exchanges[0] || 'NSE')}</span>`}</div></div>
      ${tbl([['Share'], ['ISIN'], ['Free', 'r'], ['Already authorised', 'r'], ['Quantity to authorise', 'r']], p.holdings.map(h => `<tr><td><b>${esc(h.symbol || h.isin)}</b></td><td class="mono small">${esc(h.isin)}</td><td class="r">${nf(h.free)}</td><td class="r">${h.authorised ? nf(h.authorised) : ''}</td>
        <td class="r"><input class="in mono" style="width:110px;text-align:right" inputmode="numeric" data-edis-isin="${esc(h.isin)}" data-max="${h.free}" aria-label="Quantity of ${esc(h.symbol || h.isin)}"></td></tr>`), { empty: 'No shares in your demat account to authorise.' })}
      <div class="pad"><div id="pedis-err"></div><button class="btn pri" data-act="pedisGo" ${p.problem ? 'disabled' : ''}>Authorise on CDSL</button> <span class="dim small">Up to 10 shares at a time.</span></div></div>
      <div class="grid2"><div class="panel"><div class="hd"><h3>Your CDSL TPIN</h3></div><div class="bd"><p style="margin-top:0">CDSL sends the TPIN to the mobile number and e-mail registered with your demat account.</p><button class="btn" data-act="pedisTPIN">Get a new TPIN</button> <button class="btn" data-act="pedisTPINChange">Change my TPIN</button></div></div>
      <div class="panel"><div class="hd"><h3>Settlement</h3></div><div class="bd"><div class="kv"><div><small>Sales today settle on</small><b>${fmtDate(p.settle_date)}</b></div></div></div></div></div>
      <div class="panel"><div class="hd"><h3>Your authorisations</h3></div>${tbl([['When'], ['Shares'], ['Valid till'], ['Status'], ['']], p.auths.map(a => `<tr><td class="small">${cdxStamp(a.at)}<div class="dim mono">${esc(a.req_id)}</div></td><td class="small">${cdxShares(a)}</td><td class="n">${fmtDate(a.valid_till)}</td>
        <td>${cdxPill(a.status)}${a.lines.filter(l => l.error).map(l => `<div class="neg small wrap">${esc(l.symbol || l.isin)}: ${esc(l.error)}</div>`).join('')}</td><td>${a.req_type === 'D' && ['Authorised', 'Partly authorised'].includes(a.status) ? `<button class="btn sm" data-act="edisCancel" data-id="${esc(a.req_id)}">Cancel unused</button>` : ''}</td></tr>`), { empty: 'No authorisations yet.' })}</div>`;
  }
};

const edisStaffForm = () => `<div class="grid g2"><div class="fld"><label for="edis-ucc">Client code</label><input class="in mono" id="edis-ucc" autocapitalize="characters"></div>
  <div class="fld"><label for="edis-ex">Exchange</label><select class="in" id="edis-ex"><option>NSE</option><option>BSE</option></select></div>
  <div class="fld"><label for="edis-isin">ISIN</label><input class="in mono" id="edis-isin" autocapitalize="characters"></div><div class="fld"><label for="edis-qty">Quantity</label><input class="in mono" id="edis-qty" inputmode="numeric"></div></div>
  <div class="dim small">The client must be present: CDSL's page asks for the client's TPIN and the OTP sent to the client's mobile.</div>`;

Object.assign(A, {
  cdxSend: () => act(() => post('/v1/cdsl/txn/send', { date: S.date }), r => `${r.dis.length} slip${r.dis.length > 1 ? 's' : ''} sent to CDSL · ack ${r.ack}`),
  cdxFile: async () => {
    try { const r = await post('/v1/cdsl/txn/file', { date: S.date }); const url = URL.createObjectURL(new Blob([r.file], { type: 'text/csv' })); const a = document.createElement('a'); a.href = url; a.download = r.name; document.body.appendChild(a); a.click(); a.remove(); toast(`${r.instructions} slip${r.instructions > 1 ? 's' : ''} in ${r.name}: upload it in CDAS`); render({ keep: true }); }
    catch (e) { toast(e.message, true); }
  },
  cdxCheck: id => act(() => post(`/v1/cdsl/txn/uploads/${encodeURIComponent(id)}/check`), r => `${r.id}: ${r.status}`),
  cdxDestat: async id => { try { cdxGo(await post(`/v1/cdsl/destat/${encodeURIComponent(id)}`)); } catch (e) { toast(e.message, true); } },
  cdxReport: () => act(() => post('/v1/cdsl/reports', { module: $('cdx-mod').value.trim(), report: $('cdx-rep').value.trim(), param: $('cdx-par').value.trim() }), r => `CDSL took the ${r.report} setup`),
  cdxSub: () => act(() => post('/v1/cdsl/reports/subscriptions', { module: $('cdx-mod').value.trim(), report: $('cdx-rep').value.trim(), param: $('cdx-par').value.trim(), at: $('cdx-at').value.trim(), active: true }), r => `Daily setup ${r.id} saved`),
  cdxSubToggle: id => { const s = C.cdsltxn.report_subscriptions.find(x => x.id === id); act(() => post('/v1/cdsl/reports/subscriptions', { ...s, active: !s.active }), s.active ? 'Daily setup stopped' : 'Daily setup started'); },
  cdxSFTP: () => act(() => post('/v1/cdsl/reports/sftp'), r => `${(r.loaded || []).length} new file(s)`),
  edisNew: () => confirmModal('Authorise a sale for a client', 'Pre-trade authorisation through CDSL eDIS.', edisStaffForm(), 'Go to CDSL', async () => {
    const r = await post('/v1/cdsl/edis', { ucc: $('edis-ucc').value.trim().toUpperCase(), exchange: $('edis-ex').value, lines: [{ isin: $('edis-isin').value.trim().toUpperCase(), qty: parseInt($('edis-qty').value || '0', 10) || 0 }] });
    cdxGo(r.form);
  }),
  edisTPIN: () => confirmModal('Generate a TPIN', 'CDSL sends the new TPIN to the mobile and e-mail it has for the account. Only when the client asked for it.', '<div class="fld"><label for="edis-pu">Client code</label><input class="in mono" id="edis-pu"></div><div class="fld"><label for="edis-pn">How the client asked (call reference, letter, visit)</label><input class="in" id="edis-pn"></div>', 'Ask CDSL',
    () => act(() => post('/v1/cdsl/edis/tpin', { ucc: $('edis-pu').value.trim().toUpperCase(), note: $('edis-pn').value }), 'CDSL is sending the TPIN to the client')),
  edisTPINChange: () => confirmModal('Change a TPIN', 'Opens CDSL\'s change-PIN page with the BO ID and PAN filled in. The client completes it.', '<div class="fld"><label for="edis-cu">Client code</label><input class="in mono" id="edis-cu"></div>', 'Go to CDSL',
    async () => cdxGo(await post('/v1/cdsl/edis/tpin-change', { ucc: $('edis-cu').value.trim().toUpperCase() }))),
  edisInquire: id => act(() => post(`/v1/cdsl/edis/${encodeURIComponent(id)}/inquire`), r => `${r.req_id}: ${r.status}`),
  edisCancel: id => confirmModal('Cancel the unused quantity', 'CDSL asks for an OTP on its page. Allowed 20 minutes after the authorisation.', '', 'Go to CDSL', async () => cdxGo(await post((clientUser() ? '/v1/portal/edis/' : '/v1/cdsl/edis/') + encodeURIComponent(id) + '/cancel'))),
  pedisGo: async () => {
    const lines = [...document.querySelectorAll('[data-edis-isin]')].map(i => ({ isin: i.dataset.edisIsin, qty: parseInt(i.value.replace(/,/g, '') || '0', 10) || 0 })).filter(l => l.qty > 0);
    const err = $('pedis-err');
    if (!lines.length) { err.innerHTML = '<div class="note warn">Enter the quantity of at least one share.</div>'; return; }
    try { const r = await post('/v1/portal/edis', { ucc: UI.pAcct, exchange: $('pedis-ex')?.value || C.pedis?.edis?.exchanges?.[0] || 'NSE', lines }); cdxGo(r.form); }
    catch (e) { err.innerHTML = errHTML(e); }
  },
  pedisTPIN: () => confirmModal('Get a new CDSL TPIN', 'CDSL sends it to the mobile number and e-mail registered with your demat account.', '', 'Send me a TPIN', () => act(() => post('/v1/portal/edis/tpin', { ucc: UI.pAcct }), 'CDSL is sending your TPIN')),
  pedisTPINChange: async () => { try { cdxGo(await post('/v1/portal/edis/tpin-change', { ucc: UI.pAcct })); } catch (e) { toast(e.message, true); } },
});
