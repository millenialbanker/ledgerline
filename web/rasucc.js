'use strict';
/* Ledgerline console: the UCC-wise report of each running account
   settlement, on Receipts & payouts → Running account (V.ras).

   For each settlement: its date and the 10 trading days to report it, the
   clients paid and the clients only retained, the file for the exchange's
   member portal (a ZIP named PAN_QS_DDMMYYYY_SeqNo with one CSV inside;
   each new file takes the next sequence number, never one used before),
   and the upload as recorded: the portal's reference, who, when, and
   whether it was late. A settlement date with no client settled is
   recorded as not applicable.

   Server: internal/ops/rasucc.go; internal/api/clientmoney.go. Loaded after
   app.js; it wraps V.ras. Names are prefixed ru. */

const RU_ST = { Upcoming: 'nt', 'To report': 'warn', Overdue: 'bad', Uploaded: 'good', 'Uploaded late': 'warn', 'Not applicable': 'good', 'Not applicable (late)': 'warn' };
const ruPill = s => pill(s, RU_ST[s] || 'nt');
const ruOpen = s => s.status === 'To report' || s.status === 'Overdue';

const ruBase = V.ras;
V.ras = {
  load: async () => {
    const [d, ucc] = await Promise.all([ruBase.load(), get(`/v1/ras/ucc-reports?date=${S.date}`).catch(e => ({ error: e.message, settlements: [] }))]);
    d.ucc = ucc;
    return d;
  },
  html: d => {
    const u = d.ucc || { settlements: [] }, open = u.settlements.filter(ruOpen), late = open.filter(s => s.status === 'Overdue');
    const k = kpi(`UCC-wise reports to ${esc(u.exchange || 'NSE')}`, open.length, late.length ? `${late.length} overdue · ₹10,000 a day` : open.length ? `next due ${fmtDate(open[open.length - 1].due)}` : 'all filed', late.length ? 'bad' : open.length ? 'warn' : 'good');
    return ruBase.html(d).replace('<div class="kpis">', '<div class="kpis">' + k) + ruPanel(u);
  }
};

function ruPanel(u) {
  if (u.error) return `<div class="panel"><div class="hd"><h3>UCC-wise settlement reports</h3></div><div class="bd">${errHTML({ message: u.error })}</div></div>`;
  const rows = u.settlements.map(s => {
    const files = s.record.files || [], last = files[files.length - 1], filed = s.record.not_applicable || (s.record.uploads || [])[0];
    const due = `${fmtDate(s.due)}${ruOpen(s) && s.status !== 'Overdue' ? `<div class="dim small">${s.days_left} trading day${s.days_left === 1 ? '' : 's'} left</div>` : ''}${s.days_late ? `<div class="small neg">${s.days_late} day${s.days_late === 1 ? '' : 's'} late${num(s.penalty) ? ' · ' + inr(s.penalty) : ''}</div>` : ''}`;
    const who = s.settled ? `${s.paid} paid <span class="dim">${inr(s.paid_amount)}</span><div>${s.retained} retained only <span class="dim">${inr(s.retained_amount)}</span></div>${s.held ? `<div class="small warn-t">${s.held} held: bank account incomplete</div>` : ''}` : `<span class="dim">${s.date > S.date ? 'not yet' : 'no settlement recorded'}</span>`;
    const file = last ? `<button class="linkish mono small" data-act="ruDl" data-id="${esc(s.date)}" data-seq="${last.seq}" title="The ZIP to upload">${esc(last.name)}</button><div class="dim small">${last.rows} clients · made ${fmtDate(last.at.slice(0, 10))} by ${esc(last.by)}${files.length > 1 ? ` · ${files.length} files` : ''}</div>` : s.next_file ? `<span class="dim small mono">${esc(s.next_file)}</span>` : '<span class="dim">—</span>';
    const rec = filed ? `<b>${esc(filed.ref)}</b>${filed.file ? `<div class="mono small">${esc(filed.file)}</div>` : ''}<div class="dim small">${fmtDate(filed.on)} · ${esc(filed.by)}${filed.late ? ' ' + pill(filed.days_late + ' days late', 'warn') : ''}</div>` : '<span class="dim">—</span>';
    const acts = [];
    if (s.settled && u.can_file && !filed) acts.push(`<button class="btn sm ${last ? '' : 'pri'}" data-act="ruMake" data-id="${esc(s.date)}">${last ? 'Make again' : 'Make the file'}</button>`);
    if (last) acts.push(`<button class="btn sm" data-act="ruDl" data-id="${esc(s.date)}" data-seq="${last.seq}" data-csv="1">CSV</button>`);
    if (s.settled) acts.push(`<button class="btn sm" data-act="ruView" data-id="${esc(s.date)}">Clients</button>`);
    if (u.can_record && !filed && s.date <= S.date && s.settled) acts.push(`<button class="btn sm pri" data-act="ruUpload" data-id="${esc(s.date)}">Record upload</button>`);
    if (u.can_record && s.can_na) acts.push(`<button class="btn sm" data-act="ruNA" data-id="${esc(s.date)}">Not applicable</button>`);
    if (u.can_record && filed && !s.record.not_applicable && s.settled) acts.push(`<button class="btn sm" data-act="ruUpload" data-id="${esc(s.date)}" title="A corrected file uploaded again">Record another upload</button>`);
    return `<tr class="${s.status === 'Upcoming' ? 'dim' : ''}"><td class="n"><b>${fmtDate(s.date)}</b><div class="dim small">${esc(s.kind)}${(s.window || []).length > 1 ? ' · ' + s.window.map(fmtDate).join(' / ') : ''}</div></td>
      <td class="n">${due}</td><td class="small">${who}</td><td>${file}</td><td class="small wrap">${rec}</td>
      <td>${ruPill(s.status)}${s.escalation ? `<div class="small neg wrap">${esc(s.escalation)}</div>` : ''}${(s.problems || []).slice(0, 4).map(p => `<div class="small warn-t wrap">${esc(p)}</div>`).join('')}</td>
      <td><div class="split" style="flex-wrap:wrap">${acts.join('')}</div></td></tr>`;
  });
  return `<div class="panel"><div class="hd"><h3>UCC-wise settlement reports</h3><small>File with: ${esc(u.filed_with || 'NSE')} · TM code ${esc(u.tm_code || '—')} · PAN ${esc(u.pan || '—')}</small></div>
    <div class="bd"><p class="dim small" style="margin:0">After each running account settlement, upload what each client was paid and retained on the member portal (Inspection › Statement Upload › Settlement Statement › Submit Data › Submit UCC wise Details) within 10 trading days, and record the portal's reference here. Late reports cost ₹10,000 a day; after a week no new client may be registered, after two weeks trading is disabled in all segments (NSE/INSP/56261). Clients neither retained nor paid are left out; clients only retained have NA for the bank details (NSE/INSP/56148).</p>
    ${(u.warnings || []).map(w => `<div class="note warn" style="margin-top:8px">${esc(w)}</div>`).join('')}</div>
    ${tbl([['Settlement'], ['Due'], ['Clients'], ['File'], ['Filed'], ['Status'], ['']], rows, { empty: 'No settlement to report yet. The exchanges’ dates are on Circulars & calendar → RAS dates & profile.' })}
    <div class="bd"><details><summary class="small">The file’s ${(u.fields || []).length} fields, and what is not confirmed</summary><ol class="small">${(u.fields || []).map(f => `<li>${esc(f)}</li>`).join('')}</ol><ul class="small">${(u.unconfirmed || []).map(x => `<li>${esc(x)}</li>`).join('')}</ul><div class="dim small">${esc(u.source || '')}</div></details></div></div>`;
}

function ruUploadModal(date, na) {
  const s = (C.ras?.ucc?.settlements || []).find(x => x.date === date); if (!s) return;
  const files = s.record.files || [];
  const fileFld = na ? '' : `<div class="fld full"><label for="ru-file">File uploaded</label>${files.length ? `<select class="in" id="ru-file">${files.slice().reverse().map(f => `<option value="${esc(f.name)}">${esc(f.name)} · ${f.rows} clients</option>`).join('')}</select>` : '<input class="in mono" id="ru-file" placeholder="The name of the file uploaded">'}</div>`;
  confirmModal(na ? `Not applicable · settlement of ${fmtDate(date)}` : `Record the upload · settlement of ${fmtDate(date)}`,
    na ? 'No client had funds to settle on this date: record the not-applicable declaration made on the portal.' : `Due ${fmtDate(s.due)}. Record it once the portal has accepted the file; check the result under UCC wise view submission.`,
    `<div class="form"><div class="fld"><label for="ru-ref">Portal's reference or acknowledgement</label><input class="in mono" id="ru-ref"></div><div class="fld"><label for="ru-on">Uploaded on</label><input class="in" type="date" id="ru-on" value="${esc(S.date)}" min="${esc(date)}" max="${esc(S.date)}"></div>${fileFld}<div class="fld full"><label for="ru-note">Note (optional)</label><input class="in" id="ru-note"></div></div>${!na && S.date > s.due ? `<div class="note warn" style="margin-top:8px">This is after the due date: it is recorded as late.</div>` : ''}`,
    na ? 'Record not applicable' : 'Record upload',
    () => act(() => post(`/v1/ras/ucc-reports/${date}/filed`, { ref: $('ru-ref').value, on: $('ru-on').value, file: $('ru-file')?.value || '', note: $('ru-note').value, not_applicable: !!na }), na ? 'Recorded as not applicable' : 'Upload recorded'));
}

async function ruClients(date) {
  try {
    const d = await get(`/v1/ras/ucc-reports/${date}`);
    const v = r => r.values;
    modal(mhd(`Settlement of ${fmtDate(date)} · client by client`, `${d.rows.length} clients · the file's fields, with bank accounts masked here (the file has them in full)`) + `<div class="bd">${tbl([['Client'], ['Retained', 'r'], ['Paid', 'r'], ['TM account'], ['Client account'], ['Electronic'], ['Statement sent'], ['Email'], ['SMS']], d.rows.map(r => `<tr><td><b class="mono">${esc(r.ucc)}</b> ${esc(r.name)}<div class="dim small">${esc(r.kind)}</div>${(r.problems || []).map(p => `<div class="small warn-t">${esc(p)}</div>`).join('')}</td><td class="r">${inr(v(r).retained, 2)}</td><td class="r">${inr(v(r).paid, 2)}</td><td class="mono small">${esc(v(r).tm_account)}<div class="dim">${esc(v(r).tm_ifsc)}</div></td><td class="mono small">${esc(v(r).client_account)}<div class="dim">${esc(v(r).client_ifsc)}</div></td><td>${esc(v(r).electronic)}</td><td class="small">${esc(v(r).statement_sent)}${v(r).statement_date ? ' · ' + fmtDate(v(r).statement_date) : ''}</td><td class="small">${v(r).email_date ? fmtDate(v(r).email_date) : '—'}</td><td class="small">${v(r).sms_date ? fmtDate(v(r).sms_date) : '—'}</td></tr>`), { tall: true, empty: 'No client was paid or retained.' })}</div><div class="ft"><button class="btn" data-act="close">Close</button></div>`, true);
  } catch (e) { toast(e.message, true); }
}

Object.assign(A, {
  ruMake: date => {
    const s = (C.ras?.ucc?.settlements || []).find(x => x.date === date), again = (s?.record?.files || []).length > 0;
    const go = fresh => act(() => post(`/v1/ras/ucc-reports/${date}/file`, { new: fresh }), f => `${f.name}: ${f.rows} clients`);
    if (!again) { go(false); return; }
    confirmModal(`Make the file again · ${fmtDate(date)}`, 'If nothing in the settlement changed, the last file is given again. A new file takes the next sequence number: a name the portal accepted once cannot be used again.',
      '<label class="check"><input type="checkbox" id="ru-fresh"> Make a new file with the next sequence number even if nothing changed (for example, the portal rejected the last one)</label>', 'Make the file', () => go(!!$('ru-fresh').checked));
  },
  ruDl: (date, el) => download(`/v1/ras/ucc-reports/${date}/file?seq=${el.dataset.seq || 0}${el.dataset.csv ? '&csv=1' : ''}`),
  ruView: date => ruClients(date),
  ruUpload: date => ruUploadModal(date, false),
  ruNA: date => ruUploadModal(date, true),
});
