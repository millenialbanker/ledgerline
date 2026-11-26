'use strict';
/* Ledgerline console: Compliance → Registers (screen "compregs"). The small
   registers the rules ask the firm to keep (TDS certificates, lower
   deduction certificates, IRNs, notices to clients, the R&O and attested
   CMLs given, CDSL's bill), the website's lists, the GSTR-2B match and the
   180-day credit check, AP payout holds, NSE user types, terminal turnover
   and CDSL's "to be closed" list.
   Server: internal/ops/compregs.go, taxgst.go, gstnotes.go, apweb.go,
   dpmisc.go; internal/api/compregs.go. Loaded after app.js; names are
   prefixed cr. */

Object.assign(UI, { crTab: 'tax' });
const crSoft = p => get(p).catch(e => ({ error: e.message }));
const crAreas = { tax: 'Tax and GST', partners: 'Partners and website', dp: 'Depository' };

V.compregs = {
  load: async () => {
    const [d, web, itc, pt, nse, tt] = await Promise.all([get('/v1/compregs'), crSoft('/v1/compregs/website'), crSoft(`/v1/compregs/itc?date=${S.date}`),
      crSoft('/v1/compregs/pass-through'), crSoft('/v1/compregs/nse-users'), crSoft(`/v1/compregs/terminal-turnover?date=${S.date}`)]);
    return Object.assign(d, { web, itc, pt, nse, tt });
  },
  html: d => ph('Registers', 'The records the rules ask the firm to keep, and the lists it publishes. Each line is kept for good; a wrong one is withdrawn with the reason. The self-inspection reads them.', '') +
    `<div class="panel"><div class="hd">${segBtns('crTab', [['tax', 'Tax and GST'], ['partners', 'Partners and website'], ['dp', 'Depository'], ['griev', 'Complaints from exchanges']])}</div></div>` +
    ({ tax: crTax, partners: crPartners, dp: crDP, griev: crGriev }[UI.crTab] || crTax)(d)
};

function crRegs(d, area) { return d.registers.filter(r => r.kind.area === area).map(crReg).join(''); }

function crReg(r) {
  const k = r.kind, cols = k.fields.map(f => [f.label]);
  const rows = r.records.map(x => `<tr class="${x.status === 'Active' ? '' : 'dim'}">${k.fields.map(f => `<td class="${f.type === 'amount' ? 'r' : ''} small">${esc(x.values[f.key] || '')}</td>`).join('')}
    <td class="small">${x.status === 'Active' ? '' : pill('Withdrawn', 'nt') + ' ' + esc(x.withdrawn_why || '')}<div class="dim">${esc(x.by)} · ${esc((x.at || '').slice(0, 10))}</div></td>
    <td>${x.status === 'Active' ? `<button class="btn sm" data-act="crWithdraw" data-id="${esc(x.id)}">Withdraw</button>` : ''}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>${esc(k.title)}</h3><button class="btn sm" data-act="crAdd" data-id="${esc(k.id)}">Record</button></div>
    <div class="bd"><p class="dim small" style="margin-top:0">${esc(k.help)} <span class="dim">${esc(k.rule)}</span></p></div>
    ${tbl([...cols, ['Status'], ['']], rows, { empty: 'Nothing recorded yet.' })}</div>`;
}

function crTax(d) {
  const itc = d.itc || {}, m = itc.match || { rows: [], periods: [], not_taken: [] }, u = itc.unpaid_180 || [];
  const due = (d.tds_due || []).filter(x => !x.issued && x.due <= S.date);
  const q = `from=${S.date.slice(0, 7)}-01&to=${S.date}`;
  return `<div class="kpis">${kpi('TDS certificates overdue', due.length, 'due date passed, none recorded', due.length ? 'bad' : 'good')}${kpi('GSTR-2B loaded', m.periods.length, m.periods.slice(-1)[0] || 'none')}${kpi('Credit not in 2B', m.rows.filter(r => r.status !== 'In GSTR-2B').length, 'bills with credit taken', m.rows.some(r => r.status !== 'In GSTR-2B') ? 'warn' : 'good')}${kpi('Unpaid 180 days', u.length, 'credit to reverse', u.length ? 'bad' : 'good')}</div>
    <div class="panel"><div class="hd"><h3>GSTR-2B and input tax credit</h3><div class="tools"><input class="in sm" id="cr-2bp" placeholder="YYYY-MM" value="${esc(S.date.slice(0, 7))}" style="width:90px"><input type="file" id="cr-2bf" class="in sm"><button class="btn sm pri" data-act="cr2B">Load GSTR-2B</button></div></div>
      <div class="bd"><p class="dim small" style="margin-top:0">The portal's GSTR-2B JSON download, or its B2B sheet saved as CSV. Bills with credit taken are matched by the supplier's GSTIN and the invoice number.</p></div>
      ${tbl([['Bill'], ['Vendor'], ['Bill no.'], ['Date'], ['GST', 'r'], ['In 2B', 'r'], ['Status']], m.rows.map(r => `<tr><td class="mono small">${esc(r.bill)}</td><td>${esc(r.vendor)}</td><td class="mono small">${esc(r.bill_no)}</td><td class="n">${fmtDate(r.bill_date)}</td><td class="r">${inr(r.gst, 2)}</td><td class="r">${r.period ? inr(r.gst_2b, 2) : ''}</td><td>${pill(r.status, r.status === 'In GSTR-2B' ? 'good' : 'warn')}${r.period ? ` <span class="dim small">${esc(r.period)}</span>` : ''}</td></tr>`), { empty: m.periods.length ? 'No bill with credit taken.' : 'Load a GSTR-2B to match.' })}</div>
    <div class="panel"><div class="hd"><h3>Credit to reverse: bills unpaid 180 days after their date</h3></div>${tbl([['Bill'], ['Vendor'], ['Bill date'], ['Limit'], ['Unpaid', 'r'], ['Credit to reverse', 'r']], u.map(r => `<tr><td class="mono small">${esc(r.bill)}</td><td>${esc(r.vendor)}</td><td class="n">${fmtDate(r.bill_date)}</td><td class="n">${fmtDate(r.limit)}</td><td class="r">${inr(r.unpaid, 2)}</td><td class="r"><b>${inr(r.reverse, 2)}</b></td></tr>`), { empty: 'None.' })}</div>
    <div class="panel"><div class="hd"><h3>GST credit and debit notes</h3><button class="btn sm pri" data-act="crNotes">Issue this month's notes</button></div><div class="bd"><p class="dim small" style="margin:0">A bill corrected after its month's GSTR-1 was due stays as filed; the change is a credit or debit note of the month the correction was made. The notes are listed on Firm accounts & GST with the GSTR-1 table 9B (CDNR) download.</p></div></div>
    <div class="panel"><div class="hd"><h3>TDS certificates due</h3><button class="btn sm" data-act="dl" data-id="/v1/compregs/tds-lower.csv?${q}" data-name="tds_lower_deduction.csv">Lower and nil deductions (this month) CSV</button></div>
      ${tbl([['Payee'], ['Year'], ['Quarter'], ['TDS', 'r'], ['Due'], ['Certificate']], (d.tds_due || []).slice().reverse().slice(0, 60).map(x => `<tr><td>${esc(x.payee_kind)} <span class="mono small">${esc(x.payee)}</span> ${esc(x.name || '')}</td><td>${esc(x.fy)}</td><td>${esc(x.quarter)}</td><td class="r">${inr(x.tds, 2)}</td><td class="n">${fmtDate(x.due)}</td><td>${x.issued ? `<span class="mono small">${esc(x.issued)}</span>` : x.due <= S.date ? pill('Not recorded', 'bad') : pill('Not due yet', 'nt')}</td></tr>`), { empty: 'No TDS deducted yet.' })}</div>` + crRegs(d, crAreas.tax);
}

function crPartners(d) {
  const web = Array.isArray(d.web) ? d.web : [], nse = d.nse || {}, tt = d.tt || {};
  const webPanel = l => `<div class="panel"><div class="hd"><h3>Website: ${esc(l.title)}</h3><div class="tools">${l.changed ? pill(l.published_on ? 'Changed since ' + fmtDate(l.published_on) : 'Not yet published', 'warn') : pill('On the website since ' + fmtDate(l.published_on), 'good')}<button class="btn sm" data-act="crCopy" data-id="${esc(l.title)}">Copy HTML</button>${l.changed ? `<button class="btn sm pri" data-act="crPublished" data-id="${esc(l.title)}">Mark the website updated</button>` : ''}</div></div>
    <div class="bd"><pre class="small" style="white-space:pre-wrap;max-height:220px;overflow:auto">${esc(l.text)}</pre><p class="dim small">${esc(l.to_confirm)}</p></div></div>`;
  const aps = (d.ap_status || []).map(a => { const s = a.status || {}; return `<tr><td class="mono small">${esc(a.code)}</td><td>${esc(a.name)}</td><td>${esc(a.branch || '')}</td><td>${s.hold ? pill('Payouts held', 'bad') + `<div class="dim small wrap">${esc(s.hold_reason)} · since ${fmtDate(s.hold_since)}</div>` : ''}${s.cancelled_on ? pill('Cancelled ' + fmtDate(s.cancelled_on), 'nt') + `<div class="dim small wrap">${esc(s.cancel_kind)}: ${esc(s.cancel_reason)}</div>` : ''}</td><td><button class="btn sm" data-act="crAP" data-id="${esc(a.code)}">Hold or cancel</button></td></tr>`; });
  const counts = (nse.counts || []).map(c => `${esc(c.type)} ${c.open}${c.limit ? ' of ' + esc(c.limit) : ' (limit not entered)'}${c.over ? ' ' + pill('Over', 'bad') : ''}`).join(' · ');
  return `<div class="panel"><div class="hd"><h3>Notices to clients</h3><div class="tools"><button class="btn sm" data-act="crAPNotice">Notify branch clients of an AP change</button><button class="btn sm" data-act="crPropNotice">Tell clients about proprietary trading</button></div></div><div class="bd"><p class="dim small" style="margin:0">Each client gets a signed letter by e-mail (or post), listed in the dispatch register; the notice is kept below.</p></div></div>
    <div class="panel"><div class="hd"><h3>Authorised persons: payout holds and cancellations</h3><small>a hold withholds the AP's whole payout until lifted</small></div>${tbl([['Code'], ['Name'], ['Branch'], ['Status'], ['']], aps, { empty: 'No authorised person.' })}</div>
    ${web.map(webPanel).join('')}
    <div class="panel"><div class="hd"><h3>NSE user types</h3><small>${counts || ''}</small></div>${nse.error ? `<div class="bd"><div class="note warn">${esc(nse.error)}</div></div>` : tbl([['Terminal'], ['User type'], ['']], (nse.terminals || []).map(t => `<tr><td class="mono">${esc(t)}</td><td>${esc((nse.types || {})[t.toUpperCase()] || '') || pill('Not set', 'warn')}</td><td><select class="in sm" data-cr-nse="${esc(t)}"><option value="">Choose</option>${['Corporate manager', 'Branch manager', 'Dealer'].map(x => `<option ${(nse.types || {})[t.toUpperCase()] === x ? 'selected' : ''}>${x}</option>`).join('')}</select> <button class="btn sm" data-act="crNSE" data-id="${esc(t)}">Save</button></td></tr>`), { empty: 'No open NSE terminal.' })}</div>
    <div class="panel"><div class="hd"><h3>Terminal-wise turnover</h3><button class="btn sm" data-act="dl" data-id="/v1/compregs/terminal-turnover.csv?date=${S.date}" data-name="terminal_turnover.csv">CSV</button></div><div class="bd"><p class="dim small" style="margin:0">${esc(tt.rule || '')}</p></div>
      ${tt.error ? `<div class="bd"><div class="note warn">${esc(tt.error)}</div></div>` : tbl([['Terminal'], ['Location'], ['Dealer'], ...(tt.months || []).map(m => [m, 'r']), ['Review']], (tt.rows || []).map(r => `<tr><td class="mono small">${esc(r.exchange)} ${esc(r.terminal)}</td><td class="small">${esc(r.location || '')}</td><td class="small">${esc(r.dealer || '')}</td>${r.months.map(a => `<td class="r small">${inr(a)}</td>`).join('')}<td>${r.review ? pill(r.review, 'warn') : ''}</td></tr>`), { tall: true, empty: 'No trades with terminal IDs.' })}</div>` + crRegs(d, crAreas.partners);
}

function crDP(d) {
  const pt = Array.isArray(d.pt) ? d.pt : [], l = d.to_be_closed || { bo_ids: [] };
  return `<div class="panel"><div class="hd"><h3>Depository charges recovered against CDSL's bill</h3><small>enter CDSL's bill below, head by head</small></div>
      ${tbl([['Month'], ['Head'], ['Recovered from clients', 'r'], ['CDSL billed', 'r'], ['']], pt.map(r => `<tr><td>${esc(r.month)}</td><td>${esc(r.head)}</td><td class="r">${inr(r.recovered, 2)}</td><td class="r">${r.entered ? inr(r.billed, 2) : '<span class="dim">not entered</span>'}</td><td>${num(r.over) > 1 ? pill('Recovered ' + inr(r.over, 2) + ' more', 'bad') : r.entered ? pill('Within', 'good') : ''}</td></tr>`), { empty: 'No DP bill yet.' })}</div>
    <div class="panel"><div class="hd"><h3>CDSL's "to be closed" accounts</h3><div class="tools"><input type="date" class="in sm" id="cr-tbd" value="${esc(S.date)}"><input type="file" id="cr-tbf" class="in sm"><button class="btn sm pri" data-act="crTBC">Load the list</button></div></div>
      <div class="bd"><p class="dim small" style="margin-top:0">Accounts holding only illiquid, suspended or delisted securities. No maintenance charge is billed on them while they are on the latest list.</p>
      <div>${l.list_date ? `List of ${fmtDate(l.list_date)}: <b>${l.bo_ids.length}</b> of the DP's accounts${l.file ? ' · ' + esc(l.file) : ''}` : '<span class="dim">No list loaded yet.</span>'}</div></div></div>
    <div class="panel"><div class="hd"><h3>Give a client a document</h3><div class="tools"><button class="btn sm" data-act="crRO">Send the Rights and Obligations</button><button class="btn sm" data-act="crCML">Issue an attested client master list</button><button class="btn sm" data-act="crDISDelay">Reason for a slip set up late</button></div></div></div>` + crRegs(d, crAreas.dp);
}

function crGriev() {
  return `<div class="panel"><div class="hd"><h3>A complaint an exchange forwarded</h3><button class="btn sm pri" data-act="crTicketEx">Record the exchange's due date</button></div><div class="bd"><p class="dim small" style="margin:0">For a complaint received from the stock exchanges: the exchange, the day it wants the redressal uploaded on its portal, and the amount the complainant claims (it counts in the check of complaints pending over 30 days against half the net worth). The dedicated e-mail given to BSE's investor services is recorded in Settings → Firm practices.</p></div></div>`;
}

function crFieldHTML(f, i) {
  const id = 'crf-' + i, lab = `<label for="${id}">${esc(f.label)}${f.optional ? ' (optional)' : ''}</label>`;
  if (f.type === 'choice') return `<div class="fld">${lab}<select class="in" id="${id}">${f.options.map(o => `<option>${esc(o)}</option>`).join('')}</select></div>`;
  const t = f.type === 'date' ? 'date' : 'text';
  return `<div class="fld">${lab}<input class="in ${['ucc', 'vendor', 'boid', 'amount', 'pct'].includes(f.type) ? 'mono' : ''}" type="${t}" id="${id}" ${f.type === 'date' ? `value="${esc(S.date)}"` : ''}></div>`;
}

const crKind = id => (C.compregs?.registers || []).find(r => r.kind.id === id)?.kind;
const crList = t => (Array.isArray(C.compregs?.web) ? C.compregs.web : []).find(x => x.title === t);

Object.assign(A, {
  crAdd: id => { const k = crKind(id); if (!k) return;
    confirmModal(k.title, k.help, `<div class="form">${k.fields.map(crFieldHTML).join('')}<div class="fld full"><label for="crf-note">Note (optional)</label><input class="in" id="crf-note"></div></div>`, 'Record', () => {
      const values = {}; k.fields.forEach((f, i) => { values[f.key] = $('crf-' + i).value.trim(); });
      return act(() => post('/v1/compregs/records', { kind: id, values, note: $('crf-note').value }), r => 'Recorded ' + r.id); }); },
  crWithdraw: id => confirmModal('Withdraw the record', 'It stays in the register, marked withdrawn, with the reason.', '<div class="fld"><label for="crw">Why</label><input class="in" id="crw"></div>', 'Withdraw', () => act(() => post(`/v1/compregs/records/${id}/withdraw`, { why: $('crw').value }), 'Withdrawn'), true),
  cr2B: async () => { const f = $('cr-2bf').files[0]; if (!f) { toast('Choose the GSTR-2B file', true); return; }
    await act(async () => api('POST', `/v1/compregs/gstr2b?period=${encodeURIComponent($('cr-2bp').value.trim())}&name=${encodeURIComponent(f.name)}`, await f.arrayBuffer(), true), r => `GSTR-2B ${r.period}: ${r.rows.length} invoices`); },
  crTBC: async () => { const f = $('cr-tbf').files[0]; if (!f) { toast('Choose CDSL’s list', true); return; }
    await act(async () => api('POST', `/v1/compregs/to-be-closed?date=${encodeURIComponent($('cr-tbd').value)}&name=${encodeURIComponent(f.name)}`, await f.arrayBuffer(), true), r => `${r.bo_ids.length} of the DP's accounts on the list`); },
  crNotes: () => act(() => post('/v1/compregs/gst-notes', { from: S.date.slice(0, 7) + '-01', to: S.date }), r => `${r.issued} note(s) issued`),
  crCopy: t => { const l = crList(t); if (!l) return; try { navigator.clipboard.writeText(l.html); toast('Copied'); } catch (e) { toast('Copy did not work: select the text instead', true); } },
  crPublished: t => confirmModal('Mark the website updated', `Records that the list "${t}" as shown here is now on the firm's website.`, '', 'Mark updated', () => act(() => post('/v1/compregs/website/published', { list: t }), 'Recorded')),
  crAPNotice: () => confirmModal('Notify branch clients of an AP change', 'Every active client of the branch gets a signed letter before the change takes effect.',
    `<div class="form"><div class="fld"><label for="cra-b">Branch code</label><input class="in mono" id="cra-b"></div><div class="fld"><label for="cra-o">AP leaving (code)</label><input class="in mono" id="cra-o"></div><div class="fld"><label for="cra-n">AP taking over (code)</label><input class="in mono" id="cra-n"></div><div class="fld"><label for="cra-e">Takes effect on</label><input class="in" type="date" id="cra-e"></div></div>`,
    'Send', () => act(() => post('/v1/compregs/ap-notice', { branch: $('cra-b').value.trim(), old_ap: $('cra-o').value.trim(), new_ap: $('cra-n').value.trim(), effective_on: $('cra-e').value }), r => `${r.values.clients} clients told`)),
  crPropNotice: () => confirmModal('Tell clients about proprietary trading', 'Every active client gets a signed letter that the firm will also trade on its own account from the date.',
    '<div class="fld"><label for="crp-s">Proprietary trading starts on</label><input class="in" type="date" id="crp-s"></div>', 'Send', () => act(() => post('/v1/compregs/prop-notice', { starts_on: $('crp-s').value }), r => `${r.values.clients} clients told`)),
  crAP: code => { const a = (C.compregs?.ap_status || []).find(x => x.code === code) || {}, s = a.status || {};
    confirmModal('AP ' + code + ': payout hold and cancellation', 'A hold withholds the whole payout until it is lifted (SEBI master circular 2025/90, para 33.7.7). A cancellation for disciplinary reasons is listed on the website.',
      `<div class="form"><div class="fld"><label for="crh">Payouts</label><select class="in" id="crh"><option value="">Paid as usual</option><option value="1" ${s.hold ? 'selected' : ''}>Held</option></select></div><div class="fld full"><label for="crh-r">Why held</label><input class="in" id="crh-r" value="${esc(s.hold_reason || '')}"></div>
      <div class="fld"><label for="crc-d">Cancelled on</label><input class="in" type="date" id="crc-d" value="${esc(s.cancelled_on || '')}"></div><div class="fld"><label for="crc-k">Cancelled for</label><select class="in" id="crc-k"><option value="">—</option>${['Disciplinary', 'Other (not disciplinary)'].map(x => `<option ${s.cancel_kind === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div><div class="fld full"><label for="crc-r">Reason for the cancellation</label><input class="in" id="crc-r" value="${esc(s.cancel_reason || '')}"></div></div>`,
      'Save', () => act(() => post('/v1/compregs/ap-status', { code, hold: !!$('crh').value, hold_reason: $('crh-r').value.trim(), cancelled_on: $('crc-d').value, cancel_kind: $('crc-k').value, cancel_reason: $('crc-r').value.trim() }), 'Saved')); },
  crNSE: t => { const el = document.querySelector(`[data-cr-nse="${CSS.escape(t)}"]`); act(() => post('/v1/compregs/nse-users', { terminal: t, type: el ? el.value : '' }), 'Saved'); },
  crRO: () => confirmModal('Send the Rights and Obligations', 'E-mails the client the link to the document on the firm’s website (Firm practices) and logs it.', '<div class="fld"><label for="cro-u">Client code</label><input class="in mono" id="cro-u"></div>', 'Send', () => act(() => post('/v1/compregs/ro-kit', { ucc: $('cro-u').value.trim() }), 'Sent and logged')),
  crCML: () => confirmModal('Issue an attested client master list', 'A signed copy of the account’s client master from CDSL’s last client master (not older than two months), sent to the client and logged.',
    `<div class="form"><div class="fld"><label for="crm-u">Client code</label><input class="in mono" id="crm-u"></div><div class="fld"><label for="crm-b">BO ID (blank: the primary account)</label><input class="in mono" id="crm-b"></div><div class="fld"><label for="crm-p">For</label><select class="in" id="crm-p">${['Service request to an RTA', 'Transfer or demat request', 'Other'].map(x => `<option>${x}</option>`).join('')}</select></div></div>`,
    'Issue', () => act(() => post('/v1/compregs/cml', { ucc: $('crm-u').value.trim(), bo_id: $('crm-b').value.trim(), purpose: $('crm-p').value }), 'Issued and logged')),
  crDISDelay: () => confirmModal('Reason for a slip set up late', 'A slip set up more than 2 days after it was received needs a justified reason (CDSL communiqués DP2025-543 and DP2025-641).',
    '<div class="form"><div class="fld"><label for="crd-i">Instruction (DIS-…)</label><input class="in mono" id="crd-i"></div><div class="fld full"><label for="crd-r">Reason</label><input class="in" id="crd-r"></div></div>', 'Save', () => act(() => post(`/v1/compregs/dis/${encodeURIComponent($('crd-i').value.trim())}/delay-reason`, { reason: $('crd-r').value.trim() }), 'Recorded')),
  crTicketEx: () => confirmModal('Complaint from an exchange', 'The exchange, its due date for the redressal on its portal, and the amount claimed.',
    `<div class="form"><div class="fld"><label for="crt-i">Ticket (TKT-…)</label><input class="in mono" id="crt-i"></div><div class="fld"><label for="crt-x">Exchange</label><select class="in" id="crt-x">${['BSE', 'NSE', 'MCX'].map(x => `<option>${x}</option>`).join('')}</select></div><div class="fld"><label for="crt-d">Exchange's due date</label><input class="in" type="date" id="crt-d"></div><div class="fld"><label for="crt-c">Amount claimed (₹)</label><input class="in" id="crt-c" inputmode="decimal"></div></div>`,
    'Save', () => act(() => post(`/v1/compregs/tickets/${encodeURIComponent($('crt-i').value.trim())}/exchange`, { exchange: $('crt-x').value, exchange_due: $('crt-d').value, claim: $('crt-c').value.trim() }), 'Saved')),
});
