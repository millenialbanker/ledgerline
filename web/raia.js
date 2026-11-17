'use strict';
/* Ledgerline console: complaints by business, and impersonation.

   On Investor complaints:
   - the monthly disclosure for each business the firm has: the broker's
     (SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/22, Annexure B), the DP's, and the
     research analyst's and investment adviser's (master circulars of 6 Feb
     2026, Annexures E and C) with the impersonation line, as a CSV or as
     the section for the website (12-point type, for an IA's home page);
   - on each complaint, the business it concerns and whether it arose from
     impersonation; a complainant who is not a client for RA, IA and
     impersonation complaints;
   - the impersonation register (where it was found, who was impersonated,
     the FIR, takedown requests, the notice on the website, the clients
     alerted) and the monthly reports: BSE for the RA and IA (notices
     20250804-59 and 20250804-58), the depository for the DP
     (NSDL/POLICY/2024/0048), with the nil check.

   Server: internal/ops/helpdesk.go and impersonation.go;
   internal/api/apcomplaints.go. Names are prefixed hd and imp. */

Object.assign(UI, { hdBiz: 'Broking', impMonth: '' });
const IMP_ST = { Done: 'good', 'Nothing to report': 'nt', Due: 'warn', Overdue: 'bad', Upcoming: 'nt', Open: 'warn', Closed: 'good' };
const impCan = () => has('Ops maker', 'Ops checker', 'Compliance officer', 'Risk manager');
const impWrite = () => has('Ops maker', 'Ops checker', 'Compliance officer');

const hdLoad0 = V.helpdesk.load;
V.helpdesk.load = async () => {
  const d = await hdLoad0();
  const biz = UI.hdBiz || 'Broking', imp = UI.hdTab === 'imp' && impCan();
  const [dsb, cases, reports, raia] = await Promise.all([
    UI.hdTab === 'disc' && biz !== 'Broking' ? get(`/v1/complaints/disclosure?month=${d.m}&business=${encodeURIComponent(biz)}`).catch(e => ({ error: e.message })) : null,
    imp ? get('/v1/impersonation').catch(e => ({ error: e.message })) : null,
    imp ? get(`/v1/impersonation/reports${UI.impMonth ? '?month=' + UI.impMonth : ''}`).catch(e => ({ error: e.message })) : null,
    imp ? get('/v1/raia/settings').catch(() => null) : null]);
  return Object.assign(d, { dsb, imp: cases, impr: reports, raia });
};

/* ---------- the monthly disclosure, by business ---------- */
const hdRAIA = b => b === 'RA' || b === 'IA';
function hdDiscHTML(d) {
  const bs = d.t.tables || [{ id: 'Broking', name: 'Stock broking' }], biz = bs.some(b => b.id === UI.hdBiz) ? UI.hdBiz : 'Broking';
  let ds = d.ds;
  if (biz !== 'Broking') {
    if (!d.dsb) { setTimeout(() => render({ keep: true })); return '<p class="loading pad">Loading…</p>'; }
    if (d.dsb.error) return `<div class="pad">${errHTML({ message: d.dsb.error })}</div>`;
    ds = d.dsb;
  }
  const ra = hdRAIA(biz);
  const head = ra ? [['Sr. No.'], ['Received from'], ['Pending at the end of last month', 'r'], ['Received', 'r'], ['Resolved*', 'r'], ['Total Pending#', 'r'], ['Pending complaints &gt; 3 months', 'r'], ['Average Resolution time^ (in days)', 'r']]
    : [['Sr. No.'], ['Received from'], ['Carried forward from previous month', 'r'], ['Received during the month', 'r'], ['Total Pending', 'r'], ['Resolved*', 'r'], ['Pending &lt; 3 months**', 'r'], ['Pending &gt; 3 months**', 'r'], ['Average Resolution time^ (in days)', 'r']];
  const cells = x => ra ? `<td class="r">${x.pending_at_start}</td><td class="r">${x.received}</td><td class="r">${x.resolved}</td><td class="r">${x.pending}</td><td class="r ${x.pending_over_3_months ? 'neg' : ''}">${x.pending_over_3_months}</td><td class="r">${x.avg_resolution_days}</td>`
    : `<td class="r">${x.pending_at_start}</td><td class="r">${x.received}</td><td class="r">${x.total_pending}</td><td class="r">${x.resolved}</td><td class="r">${x.pending_under_3_months}</td><td class="r ${x.pending_over_3_months ? 'neg' : ''}">${x.pending_over_3_months}</td><td class="r">${x.avg_resolution_days}</td>`;
  const trend = (rows, tot, first) => tbl([['Sr. No.'], [first], [first === 'Year' ? 'Carried forward from previous year' : 'Carried forward from previous month', 'r'], ['Received', 'r'], ['Resolved*', 'r'], ['Pending#', 'r']],
    rows.map((x, i) => `<tr><td>${i + 1}</td><td>${esc(first === 'Month' ? hdMonthName(x.month) : x.month)}</td><td class="r">${x.carried_forward}</td><td class="r">${x.received}</td><td class="r">${x.resolved}</td><td class="r">${x.pending}</td></tr>`),
    { foot: tot ? `<tfoot><tr><td></td><td>Grand Total</td><td class="r">${tot.carried_forward}</td><td class="r">${tot.received}</td><td class="r">${tot.resolved}</td><td class="r">${tot.pending}</td></tr></tfoot>` : '' });
  const q = `month=${encodeURIComponent(ds.month)}&business=${encodeURIComponent(biz)}`;
  return `<div class="bd"><div class="tools">${bs.length > 1 ? segBtns('hdBiz', bs.map(b => [b.id, b.name])) : ''}
      <button class="btn sm" data-act="dl" data-id="/v1/complaints/disclosure/file?${q}" data-name="ComplaintsData_${esc(biz)}_${esc(ds.month)}.csv">CSV</button>
      <button class="btn sm" data-act="dl" data-id="/v1/complaints/disclosure/file?${q}&format=html" data-name="ComplaintsData_${esc(biz)}_${esc(ds.month)}.html">For the website (HTML)</button></div>
    <h3 style="margin:10px 0 2px">${esc(ds.title || 'Investor complaints data')}</h3><div class="dim small">Data for the month ending ${fmtDate(addDaysJS(addMonthJS(ds.month + '-01', 1), -1))} · ${esc(ds.basis || '')}</div>
    ${ds.unconfirmed ? `<div class="note warn">${esc(ds.unconfirmed)}</div>` : ''}${(ds.notes || []).map(n => `<div class="note">${esc(n)}</div>`).join('')}</div>` +
    tbl(head, ds.rows.map((x, i) => `<tr><td>${i + 1}</td><td>${esc(x.source)}</td>${cells(x)}</tr>`), { foot: `<tfoot><tr><td></td><td>Grand Total</td>${cells(ds.total)}</tr></tfoot>` }) +
    (ds.impersonation != null ? `<div class="pad"><b>Number of complaints received during month against the ${esc(biz)} due to impersonation by some other entity: ${ds.impersonation}</b>${ds.adjusted ? '<div class="dim small">Left out of the tables above, as the annexure’s note allows (Impersonation → Reporting details).</div>' : ''}</div>` : '') +
    `<div class="grid2"><div><h4 class="pad" style="margin:0">Trend of monthly disposal of complaints</h4>${trend(ds.trend || [], ds.trend_total, 'Month')}</div><div><h4 class="pad" style="margin:0">Trend of annual disposal of complaints</h4>${trend(ds.annual || [], ds.annual_total, 'Year')}</div></div>
    <div class="pad"><span class="dim small">* Inclusive of complaints of previous months resolved in the current month. # Inclusive of complaints pending on the last day of the month. ^ The sum of the days taken to resolve each complaint resolved in the month, divided by the number resolved. Only complaints are counted, each in the table of the business it concerns; service requests and queries are not.${biz === 'IA' ? ' An investment adviser shows this on its home page without scrolling, in font size 12 or above, within 7 days of the month’s end (IA master circular para 2.4): the HTML is in 12-point type.' : ''}</span></div>`;
}
const hdMonthName = m => { const x = /^(\d{4})-(\d{2})$/.exec(m || ''); return x ? `${['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December'][+x[2] - 1]}, ${x[1]}` : m; };
const addMonthJS = (d, n) => { const t = new Date(d + 'T00:00:00Z'); t.setUTCMonth(t.getUTCMonth() + n); return t.toISOString().slice(0, 10); };

/* ---------- the complaint: its business and impersonation ---------- */
function hdTicketFields(c) {
  const bs = c.businesses || [];
  return `<div class="fld"><label for="tk-b">Business it concerns</label><select class="in" id="tk-b">${bs.map(b => `<option value="${esc(b.id)}">${esc(b.name)}</option>`).join('')}</select></div>
    <label class="check"><input type="checkbox" id="tk-imp"> Received because someone impersonated the firm</label>
    <div class="fld"><label for="tk-cn">Complainant who is not a client (RA, IA or impersonation)</label><input class="in" id="tk-cn" placeholder="Name (leave the client empty)"></div>
    <div class="fld"><label for="tk-cc">Their mobile or email</label><input class="in" id="tk-cc"></div>`;
}
function hdTicketBody() {
  const b = { business: $('tk-b')?.value || '', impersonation: !!$('tk-imp')?.checked }, n = ($('tk-cn')?.value || '').trim();
  if (n) Object.assign(b, { name: n, contact: ($('tk-cc')?.value || '').trim() });
  return b;
}
const hdTicketTags = t => `${t.business ? ` <span class="tag">${esc(t.business)}</span>` : ''}${t.impersonation ? ' <span class="tag">impersonation</span>' : ''}${!t.ucc && t.contact ? `<div class="dim small">${esc(t.contact)}</div>` : ''}`;
function hdTagTools(t) {
  if (!impWrite()) return '';
  const bs = C.helpdesk?.t?.businesses || [];
  return `<div class="tools" style="margin-top:6px"><label class="dim small" for="tg-b">Business</label><select class="in sm" id="tg-b">${bs.map(b => `<option value="${esc(b.id)}" ${(t.business || 'Broking') === b.id ? 'selected' : ''}>${esc(b.name)}</option>`).join('')}</select>
    <label class="check small"><input type="checkbox" id="tg-i" ${t.impersonation ? 'checked' : ''}> From impersonation</label><input class="in sm mono" id="tg-c" style="width:120px" placeholder="Case IMP-…" value="${esc(t.impersonation_case || '')}" aria-label="Impersonation case">
    <button class="btn sm" data-act="hdTag" data-id="${esc(t.id)}">Save</button></div>`;
}

/* ---------- the impersonation register ---------- */
function impHTML(d) {
  if (!impCan()) return '<div class="pad dim">The impersonation register is kept by head office.</div>';
  if (!d.imp || !d.impr) { setTimeout(() => render({ keep: true })); return '<p class="loading pad">Loading…</p>'; }
  if (d.imp.error) return `<div class="pad">${errHTML({ message: d.imp.error })}</div>`;
  const cs = d.imp.cases || [], open = cs.filter(c => c.status === 'Open'), gaps = open.filter(c => (c.missing || []).length);
  const step = (ok, l) => `<span class="${ok ? 'pos' : 'neg'}">${ok ? '✓' : '✗'} ${l}</span>`;
  const row = c => `<tr><td class="n">${esc(c.id)}<div class="dim small">${fmtDate(c.found_on)}</div></td><td>${c.businesses.map(b => `<span class="tag">${esc(b)}</span>`).join(' ')}<div class="small">${esc(c.who)}</div></td>
    <td class="small wrap">${esc(c.where)}${c.platform ? ' · ' + esc(c.platform) : ''}<div class="mono">${esc(c.handle)}</div>${c.impersonator ? `<div class="dim">as ${esc(c.impersonator)}</div>` : ''}</td>
    <td class="small">${step(c.fir.number, 'FIR' + (c.fir.number ? ' ' + esc(c.fir.number) : ''))}<br>${step((c.takedowns || []).length, 'takedown' + ((c.takedowns || []).length ? ': ' + c.takedowns.map(x => esc(x.status.toLowerCase())).join(', ') : ''))}<br>${step(c.notice.on, 'notice on the website')}<br>${step(c.alert.on, 'clients alerted')}</td>
    <td class="small">${(c.complaints || []).map(esc).join(', ') || '—'}<div class="dim">${(c.evidence || []).length} file${(c.evidence || []).length === 1 ? '' : 's'}</div></td><td>${pill(c.status, IMP_ST[c.status])}</td>
    <td>${impWrite() ? `<button class="btn sm" data-act="impEdit" data-id="${esc(c.id)}">Open</button>` : ''}</td></tr>`;
  return `<div class="bd"><div class="kpis">${kpi('Open cases', open.length, cs.length + ' on the register')}${kpi('Steps missing', gaps.length, 'FIR, takedown, notice or alert', gaps.length ? 'bad' : 'good')}</div>
      <div class="tools">${impWrite() ? '<button class="btn pri sm" data-act="impNew">Record a case</button>' : ''}<span class="dim small">Search the web, app stores and social media regularly for the firm's name. For each case: file an FIR (with the cyber police), have it taken down (the platform, app store or MeitY), put a notice on the website's home page naming the entity and the police station, and alert clients. BSE notices 20250804-58 and 20250804-59; NSDL/POLICY/2024/0048.</span></div></div>
    ${tbl([['Case'], ['Impersonated'], ['Where'], ['Steps'], ['Complaints'], ['Status'], ['']], cs.map(row), { empty: 'No case of impersonation recorded.' })}
    ${impReportsHTML(d.impr)}${impSettingsHTML(d.raia)}`;
}

function impReportsHTML(v) {
  if (!v || v.error) return v?.error ? `<div class="pad">${errHTML({ message: v.error })}</div>` : '';
  const co = has('Compliance officer');
  const card = b => {
    const r = b.recorded, n = (b.cases || []).length, q = `body=${encodeURIComponent(b.body)}&month=${encodeURIComponent(v.month)}`;
    return `<tr><td><b>${esc(b.name)}</b><div class="dim small">${esc(b.where)}</div>${b.note ? `<div class="small neg">${esc(b.note)}</div>` : ''}</td><td class="mono small">${esc(b.reg_no || '—')}</td>
      <td class="r">${n}</td><td class="r">${b.complaints}</td><td class="n">${fmtDate(b.due)}</td>
      <td>${pill(b.status, IMP_ST[b.status])}${r ? `<div class="small">${r.nil ? `Nil check ${fmtDate(r.searched_on)}: ${esc(r.searched)}${r.nil_entered ? ` · nil entered ${fmtDate(r.submitted_on)} ${esc(r.ref || '')}` : ''}` : `Submitted ${fmtDate(r.submitted_on)} · <span class="mono">${esc(r.ref)}</span>`}${r.late ? ' ' + pill('late', 'bad') : ''}</div>${r.file_id ? `<button class="linkish small" data-act="dl" data-id="/v1/periodic/files/${esc(r.file_id)}" data-name="${esc(r.file_name)}">${esc(r.file_name)}</button>` : ''}` : ''}</td>
      <td><div style="display:flex;gap:6px;flex-wrap:wrap;justify-content:flex-end">${n || b.complaints ? `<button class="btn sm" data-act="dl" data-id="/v1/impersonation/reports/file?${q}" data-name="Impersonation_${esc(b.body)}_${esc(v.month)}.csv">Report</button>` : ''}${!r && co && b.status !== 'Nothing to report' ? `<button class="btn sm pri" data-act="impMark" data-id="${esc(b.body)}">${n || b.complaints ? 'Record the submission' : 'Record the nil check'}</button>` : ''}</div></td></tr>`;
  };
  return `<div class="panel"><div class="hd"><h3>Monthly reports · ${esc(v.label)}</h3><small>BSE within 7 days of the month's end (RAIA Inspection → Report Impersonation); the depository by the 10th</small>
    <div class="tools"><input class="in sm" type="month" id="imp-m" value="${esc(v.month)}" aria-label="Month"><button class="btn sm" data-act="impMonth">Show</button></div></div>
    ${(v.notes || []).map(n => `<div class="pad"><span class="dim small">${esc(n)}</span></div>`).join('')}
    ${tbl([['To'], ['Registration'], ['Cases', 'r'], ['Complaints', 'r'], ['Due'], ['Status'], ['']], (v.bodies || []).map(card), { empty: 'Nothing to report to.' })}
    <div class="pad"><span class="dim small">BSE's form could not be read: the report lists every detail the notices ask for (the cases, the FIR, takedowns, the notice, client alerts, the impersonator's bank accounts, up to 5) to copy into it, with files of up to 5 MB. BSE does not say whether a month with no case needs a nil entry: record the nil check each month. NSDL needs no nil report.</span></div></div>`;
}

function impSettingsHTML(s) {
  if (!s) return '';
  const co = has('Compliance officer'), f = (k, l, ph) => `<div class="fld"><label for="rs-${k}">${l}</label><input class="in mono" id="rs-${k}" value="${esc(s[k] || '')}" placeholder="${ph}" ${co ? '' : 'readonly'}></div>`;
  return `<div class="panel"><div class="hd"><h3>Reporting details</h3><small>Compliance officer</small></div><div class="bd"><div class="form">
    ${f('ra_no', 'Research analyst registration', 'INH…')}${f('ia_no', 'Investment adviser registration', 'INA…')}${f('nsdl_dp_id', 'NSDL DP ID', 'IN3…')}${f('cdsl_dp_id', 'CDSL DP ID (else the CDSL connection’s)', '8 digits')}${f('dp_name', 'DP name for the depository report', 'the firm’s name')}
    <label class="check full"><input type="checkbox" id="rs-adj" ${s.adjust_impersonation ? 'checked' : ''} ${co ? '' : 'disabled'}> Leave complaints received because of impersonation out of the RA and IA tables (the annexures’ note allows it); the impersonation line still counts them</label></div>
    ${co ? '<div class="tools" style="margin-top:8px"><button class="btn pri sm" data-act="impSettings">Save</button></div>' : ''}</div></div>`;
}

// The case: everything the steps and the reports need.
function impModal(c) {
  const d = C.helpdesk, o = d.imp, bs = d.t.businesses || [];
  c = c || { businesses: [], fir: {}, notice: {}, alert: {}, takedowns: [], banks: [], status: 'Open', found_on: S.date, found_by: 'Our own monitoring', where: 'Social media' };
  const sel = (id, opts, v) => `<select class="in" id="${id}">${opts.map(x => `<option ${x === v ? 'selected' : ''}>${esc(x)}</option>`).join('')}</select>`;
  const td = (x, i) => `<div class="form imp-row" data-td="${i}" style="grid-template-columns:2fr 1fr 1fr 1fr 1fr"><input class="in" data-k="platform" value="${esc(x.platform || '')}" placeholder="Platform, app store or authority" aria-label="Platform"><input class="in" type="date" data-k="requested_on" value="${esc(x.requested_on || '')}" aria-label="Requested on"><input class="in" data-k="ref" value="${esc(x.ref || '')}" placeholder="Reference" aria-label="Reference"><select class="in" data-k="status" aria-label="Status">${o.takedown_statuses.map(s => `<option ${s === (x.status || 'Requested') ? 'selected' : ''}>${esc(s)}</option>`).join('')}</select><input class="in" type="date" data-k="status_on" value="${esc(x.status_on || '')}" aria-label="Status date"></div>`;
  const bk = (x, i) => `<div class="form imp-bank" data-bk="${i}" style="grid-template-columns:2fr 2fr 1fr 2fr"><input class="in" data-k="bank" value="${esc(x.bank || '')}" placeholder="Bank" aria-label="Bank"><input class="in mono" data-k="account" value="${esc(x.account || '')}" placeholder="Account number" aria-label="Account number"><input class="in mono" data-k="ifsc" value="${esc(x.ifsc || '')}" placeholder="IFSC" aria-label="IFSC"><input class="in" data-k="holder" value="${esc(x.holder || '')}" placeholder="Account holder" aria-label="Account holder"></div>`;
  modal(mhd(c.id ? 'Impersonation case ' + esc(c.id) : 'Record a case of impersonation', 'Who was impersonated, where, and each step taken') + `<form class="bd" id="impf"><div class="form">
    <fieldset class="full"><legend class="small">Which of the firm's businesses was impersonated</legend>${bs.map(b => `<label class="check"><input type="checkbox" data-impb="${esc(b.id)}" ${c.businesses.includes(b.id) ? 'checked' : ''}> ${esc(b.name)}</label>`).join(' ')}</fieldset>
    <div class="fld full"><label for="im-who">Who or what was impersonated</label><input class="in" id="im-who" value="${esc(c.who || '')}" placeholder="The firm's name, its research or advisory name, an officer"></div>
    <div class="fld"><label for="im-on">Found on</label><input class="in" type="date" id="im-on" value="${esc(c.found_on || '')}"></div><div class="fld"><label for="im-by">How it was found</label>${sel('im-by', o.found_by, c.found_by)}</div>
    <div class="fld"><label for="im-where">Where</label>${sel('im-where', o.where, c.where)}</div><div class="fld"><label for="im-pl">Platform</label><input class="in" id="im-pl" value="${esc(c.platform || '')}" placeholder="e.g. the social media platform or app store"></div>
    <div class="fld full"><label for="im-h">Link, handle, app or group name</label><input class="in mono" id="im-h" value="${esc(c.handle || '')}"></div>
    <div class="fld"><label for="im-imp">Name the impersonator used</label><input class="in" id="im-imp" value="${esc(c.impersonator || '')}"></div>
    <div class="fld full"><label for="im-d">What it did</label><textarea class="in" id="im-d" rows="2">${esc(c.details || '')}</textarea></div>
    <fieldset class="full"><legend class="small">FIR with the police (including the cyber police)</legend><div class="form"><div class="fld"><label for="im-fs">Police station</label><input class="in" id="im-fs" value="${esc(c.fir.police_station || '')}"></div><div class="fld"><label for="im-fn">FIR number</label><input class="in mono" id="im-fn" value="${esc(c.fir.number || '')}"></div><div class="fld"><label for="im-fd">FIR date</label><input class="in" type="date" id="im-fd" value="${esc(c.fir.date || '')}"></div><div class="fld"><label for="im-fp">Cyber crime portal reference</label><input class="in mono" id="im-fp" value="${esc(c.fir.portal_ref || '')}"></div></div></fieldset>
    <fieldset class="full"><legend class="small">Takedown requests</legend><div id="im-tds">${(c.takedowns || []).map(td).join('')}</div><button class="btn sm" type="button" data-act="impAddTD">Add a request</button></fieldset>
    <fieldset class="full"><legend class="small">Notice on the website's home page</legend><div class="form"><div class="fld"><label for="im-no">Shown from</label><input class="in" type="date" id="im-no" value="${esc(c.notice.on || '')}"></div><div class="fld"><label for="im-nu">Web address</label><input class="in mono" id="im-nu" value="${esc(c.notice.url || '')}"></div><div class="fld full"><label for="im-nt">Text (naming the entity and the police station)</label><input class="in" id="im-nt" value="${esc(c.notice.text || '')}"></div></div></fieldset>
    <fieldset class="full"><legend class="small">Clients alerted</legend><div class="form"><div class="fld"><label for="im-ao">On</label><input class="in" type="date" id="im-ao" value="${esc(c.alert.on || '')}"></div><div class="fld"><label for="im-ab">By</label><select class="in" id="im-ab"><option value="">—</option>${o.alert_by.map(x => `<option ${x === c.alert.by ? 'selected' : ''}>${esc(x)}</option>`).join('')}</select></div><div class="fld"><label for="im-an">Clients</label><input class="in" id="im-an" inputmode="numeric" value="${esc(c.alert.clients || '')}"></div></div></fieldset>
    <fieldset class="full"><legend class="small">The impersonator's bank accounts (up to 5)</legend><div id="im-bks">${(c.banks || []).map(bk).join('')}</div><button class="btn sm" type="button" data-act="impAddBank">Add an account</button></fieldset>
    <div class="fld"><label for="im-st">Status</label>${sel('im-st', ['Open', 'Closed'], c.status)}</div><div class="fld"><label for="im-sc">Closed on</label><input class="in" type="date" id="im-sc" value="${esc(c.closed_on || '')}"></div>
    ${c.id ? `<fieldset class="full"><legend class="small">Files kept (FIR copy, screenshots; PDF, PNG or JPEG up to 5 MB)</legend>${(c.evidence || []).map(f => `<button class="linkish small" type="button" data-act="dl" data-id="/v1/impersonation/evidence/${esc(f.id)}" data-name="${esc(f.name)}">${esc(f.name)}</button>`).join(' · ') || '<span class="dim small">None yet</span>'}<div class="tools" style="margin-top:4px"><input type="file" id="im-file" accept="application/pdf,image/png,image/jpeg" aria-label="File to keep"><button class="btn sm" type="button" data-act="impFile" data-id="${esc(c.id)}">Keep the file</button></div></fieldset>` : ''}
    </div><div id="im-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`, true);
  IMPTD = td; IMPBK = bk;
  $('impf').onsubmit = async e => {
    e.preventDefault();
    const rows = (sel2, keys) => [...document.querySelectorAll(sel2)].map(r => Object.fromEntries(keys.map(k => [k, r.querySelector(`[data-k="${k}"]`).value.trim()]))).filter(x => Object.values(x).some(Boolean));
    const v = id => $(id).value.trim();
    const b = { id: c.id || '', businesses: [...document.querySelectorAll('[data-impb]')].filter(x => x.checked).map(x => x.dataset.impb), who: v('im-who'), found_on: v('im-on'), found_by: v('im-by'), where: v('im-where'), platform: v('im-pl'), handle: v('im-h'), impersonator: v('im-imp'), details: v('im-d'),
      fir: { police_station: v('im-fs'), number: v('im-fn'), date: v('im-fd'), portal_ref: v('im-fp') }, takedowns: rows('#im-tds .imp-row', ['platform', 'requested_on', 'ref', 'status', 'status_on']).filter(x => x.platform || x.requested_on),
      notice: { on: v('im-no'), url: v('im-nu'), text: v('im-nt') }, alert: { on: v('im-ao'), by: v('im-ab'), clients: +v('im-an') || 0 }, banks: rows('#im-bks .imp-bank', ['bank', 'account', 'ifsc', 'holder']), status: v('im-st'), closed_on: v('im-sc') };
    try { const r = await post('/v1/impersonation', b); closeModal(); toast(`${r.id} saved${(r.missing || []).length ? ' · still to record: ' + r.missing.join(', ') : ''}`); render({ keep: true }); } catch (err) { $('im-err').innerHTML = errHTML(err); }
  };
}
let IMPTD = null, IMPBK = null;

Object.assign(A, {
  hdTag: id => act(() => post(`/v1/tickets/${encodeURIComponent(id)}/tag`, { business: $('tg-b').value, impersonation: $('tg-i').checked, impersonation_case: $('tg-c').value.trim() }), 'Complaint marked'),
  impNew: () => impModal(),
  impEdit: id => impModal(JSON.parse(JSON.stringify((C.helpdesk.imp.cases || []).find(x => x.id === id)))),
  impAddTD: () => { const w = $('im-tds'); w.insertAdjacentHTML('beforeend', IMPTD({ status: 'Requested', requested_on: S.date }, w.children.length)); },
  impAddBank: () => { const w = $('im-bks'); if (w.children.length >= 5) { toast('BSE’s form takes at most 5 bank accounts'); return; } w.insertAdjacentHTML('beforeend', IMPBK({}, w.children.length)); },
  impFile: async id => {
    const f = $('im-file').files[0]; if (!f) { toast('Choose the file first'); return; }
    try { const r = await api('POST', `/v1/impersonation/${encodeURIComponent(id)}/evidence?name=${encodeURIComponent(f.name)}`, f, true); toast('File kept'); C.helpdesk.imp.cases = C.helpdesk.imp.cases.map(x => x.id === r.id ? r : x); impModal(JSON.parse(JSON.stringify(r))); } catch (e) { toast(e.message, true); }
  },
  impMonth: () => { UI.impMonth = $('imp-m').value; render({ keep: true }); },
  impMark: body => {
    const v = C.helpdesk.impr, b = v.bodies.find(x => x.body === body), cases = (b.cases || []).length || b.complaints;
    const form = cases ? `<div class="form"><div class="fld"><label for="ir-d">Submitted on</label><input class="in" type="date" id="ir-d" value="${esc(S.date)}"></div><div class="fld"><label for="ir-r">Acknowledgement or reference</label><input class="in mono" id="ir-r"></div></div><div class="note">The report as made now is kept with it.</div>`
      : `<div class="form"><div class="fld full"><label for="ir-s">Where the firm looked for impersonation</label><input class="in" id="ir-s" placeholder="e.g. web search, app stores, the main social media"></div><div class="fld"><label for="ir-o">Checked on</label><input class="in" type="date" id="ir-o" value="${esc(S.date)}"></div>
        ${b.nil_needed ? `<label class="check full"><input type="checkbox" id="ir-n"> A nil entry was also made on BSE's portal</label><div class="fld"><label for="ir-d">Entered on</label><input class="in" type="date" id="ir-d" value="${esc(S.date)}"></div><div class="fld"><label for="ir-r">Reference</label><input class="in mono" id="ir-r"></div>` : ''}</div>`;
    confirmModal(`${b.name} · ${v.label}`, cases ? `Enter the report on ${b.where}, then record it here.` : 'No case is on the register for the month: record the nil check.', form, 'Record',
      () => act(() => post('/v1/impersonation/reports', { body, month: v.month, submitted_on: $('ir-d')?.value || '', ref: ($('ir-r')?.value || '').trim(), searched: ($('ir-s')?.value || '').trim(), searched_on: $('ir-o')?.value || '', nil_entered: !!$('ir-n')?.checked }), 'Recorded'));
  },
  impSettings: () => act(() => api('PUT', '/v1/raia/settings', { ra_no: $('rs-ra_no').value, ia_no: $('rs-ia_no').value, nsdl_dp_id: $('rs-nsdl_dp_id').value, cdsl_dp_id: $('rs-cdsl_dp_id').value, dp_name: $('rs-dp_name').value, adjust_impersonation: $('rs-adj').checked }), 'Reporting details saved'),
});
