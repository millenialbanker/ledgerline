'use strict';
/* Ledgerline console: Surveillance & AML, the case work.

   Each CDSL, DP and MCX alert is worked with its source's checklist and
   documents (Work), may carry an adverse observation that is due with CDSL
   or the exchange within 7 days, and has its own due date (45 days, 60 for
   CDSL's half-yearly alerts). Panels below the queue: CDSL's and MCX's
   alert files with the download log, the quarter's sample of closures,
   the quarterly status for CDSL, connected clients and the thresholds of
   the DP's own rules.

   Server: internal/ops/survcase.go, survimport.go, dpsurv.go;
   internal/api/dpsurv.go. Loaded after app.js; it wraps V.surv. Names are
   prefixed ds. */

const dsBase = V.surv;
V.surv = {
  load: async () => {
    const d = await dsBase.load();
    const c = has('Compliance officer');
    const [log, sample, cdsl, thr] = await Promise.all([
      get('/v1/surveillance/downloads').catch(() => []),
      c ? get('/v1/surveillance/sample').catch(() => null) : null,
      get('/v1/surveillance-return?for=CDSL').catch(() => null),
      get('/v1/surveillance/thresholds').catch(() => null)]);
    d.ds = { log, sample, cdsl, thr };
    C.dsAlerts = d;
    return d;
  },
  html: d => dsBase.html(d) + dsPanels(d.ds || {})
};

const dsKinds = [['cdsl-surv', 'CDSL surveillance alerts (CDAS)'], ['cdsl-fiu', 'CDSL FIU alerts (CDAS)'], ['mcx-mrs', 'MCX transactional alerts (MRS)']];

function dsWorkBtn(a) {
  if (!(a.checklist || []).length && !a.attachments && a.area !== 'DP') return '';
  const n = (a.checklist || []).filter(x => x.state).length, t = (a.checklist || []).length;
  return `<button class="btn sm" data-act="dsWork" data-id="${esc(a.id)}">Work${t ? ` · ${n}/${t}` : ''}</button>`;
}

function dsPanels(v) {
  const c = has('Compliance officer');
  const log = (v.log || []).slice(0, 15);
  const files = `<div class="panel"><div class="hd"><h3>CDSL and MCX alert files</h3><small>CDSL's surveillance and FIU alerts from CDAS, MCX's from MRS. Load each file, or record that there was nothing to download.</small></div><div class="bd">
    <div class="split"><select class="in" id="ds-kind">${dsKinds.map(k => `<option value="${k[0]}">${esc(k[1])}</option>`).join('')}</select>
    <label class="btn pri">Load a file<input type="file" id="ds-file" hidden></label><button class="btn" data-act="dsNil">Nothing to download</button></div>
    <p class="dim small">The layouts are not published to Ledgerline: the columns are found by their headings, and the whole row is kept with the alert. Each alert has 45 days (60 for CDSL's half-yearly multiple-account alerts).</p></div>
    ${tbl([['When'], ['List'], ['File'], ['Rows', 'r'], ['New', 'r'], ['Repeated', 'r'], ['No client', 'r'], ['By']], log.map(x => `<tr><td>${esc((x.at || '').slice(0, 16).replace('T', ' '))}</td><td>${esc(x.title)}</td><td>${x.nil ? '<span class="dim">' + esc(x.note || 'nothing to download') + '</span>' : esc(x.file || '')}</td><td class="r">${x.rows}</td><td class="r">${x.added}</td><td class="r">${x.duplicates}</td><td class="r">${x.unmatched ? pill(String(x.unmatched), 'warn') : 0}</td><td>${esc(x.by)}</td></tr>`), { empty: 'Nothing downloaded yet.' })}</div>`;
  const s = v.sample;
  const sample = s ? `<div class="panel"><div class="hd"><h3>Sample of closures · ${esc(s.quarter)}</h3><small>${s.closed} alerts closed; ${s.size} in the sample; ${s.checked} checked. The draw is fixed, so nobody picks which alerts are checked.</small></div>
    ${tbl([['Alert'], ['Client'], ['Closed'], ['Check']], s.sample.map(a => `<tr><td class="wrap"><b style="font-weight:600">${esc(a.type)}</b><div class="dim mono small">${esc(a.id)}</div></td><td class="mono">${esc(a.ucc || '')}</td><td>${fmtDate(a.closed_on || '')} ${esc(a.closed_by || '')}</td><td>${a.sample && a.sample.quarter === s.quarter ? pill(a.sample.result, a.sample.result === 'Agree' ? 'good' : 'bad') + (a.sample.note ? `<div class="dim small">${esc(a.sample.note)}</div>` : '') : `<button class="btn sm" data-act="dsSample" data-id="${esc(a.id)}" data-q="${esc(s.quarter)}">Check</button>`}</td></tr>`), { empty: 'No alert was closed in the quarter.' })}</div>` : '';
  const r = v.cdsl;
  const cdsl = r ? `<div class="panel"><div class="hd"><h3>Quarterly status for CDSL · ${esc(r.label)}</h3><small>CDSL's and the DP's own alerts, due within 15 days of the quarter's end (${fmtDate(r.due)}). Fill CDSL's form on its surveillance system from these counts; download from Regulatory reports → DP surveillance alert status for CDSL.</small></div>` +
    tbl([['Name of the alert'], ['At start', 'r'], ['New', 'r'], ['Closed', 'r'], ['Reported as adverse', 'r'], ['Pending at end', 'r']], r.format_i.map(f => `<tr><td>${esc(f.name)}</td><td class="r">${f.start}</td><td class="r">${f.new}</td><td class="r">${f.closed}</td><td class="r">${f.referred}</td><td class="r">${f.end}</td></tr>`), { empty: 'NIL: no DP alerts in the quarter.' }) + '</div>' : '';
  const more = `<div class="panel"><div class="hd"><h3>Connected clients and thresholds</h3><div class="tools"><button class="btn sm" data-act="dsGroups">Connected clients</button>${c ? '<button class="btn sm" data-act="dsThr">Thresholds</button>' : ''}</div></div><div class="bd"><p class="dim small">The DP's own rules (several accounts with the same PAN, mobile, e-mail, bank or address; bounced communications; frequent changes; off-market or pledges against income; transfers after a change; dormant accounts; one-to-many and many-to-one; reason codes), mule accounts, staff front running and connected clients trading against each other run with <b>Run internal rules</b> and every evening. Limits are scaled by the client's risk category.</p></div></div>`;
  return files + sample + cdsl + more;
}

function dsAlert(id) { return ((C.dsAlerts || [])).find(a => a.id === id); }

function dsWorkHTML(a) {
  const isC = has('Compliance officer'), open = a.status !== 'Closed' && a.status !== 'STR filed';
  const rows = (a.checklist || []).map((x, i) => `<tr><td class="wrap">${esc(x.label)}</td><td>${x.key === 'in_time' ? (x.state ? pill('Done', 'good') : '<span class="dim small">recorded on answering</span>') : `<select class="in" id="ds-st-${i}" data-key="${esc(x.key)}" ${open ? '' : 'disabled'}><option value="" ${!x.state ? 'selected' : ''}>To do</option><option ${x.state === 'Done' ? 'selected' : ''}>Done</option><option ${x.state === 'Not applicable' ? 'selected' : ''}>Not applicable</option></select>`}</td><td>${x.key === 'in_time' ? esc(x.note || '') : `<input class="in" id="ds-nt-${i}" value="${esc(x.note || '')}" ${open ? '' : 'disabled'} placeholder="Note">`}</td></tr>`);
  const docs = (a.attachments || []).map(d => `<li><button class="linkish" data-act="dsDoc" data-id="${esc(a.id)}" data-doc="${esc(d.id)}">${esc(d.name)}</button> <span class="dim small">${esc(d.by)} · ${esc((d.at || '').slice(0, 10))}</span></li>`).join('');
  const facts = [a.ref && 'Ref ' + esc(a.ref), a.isin && 'ISIN ' + esc(a.isin), a.qty && 'Qty ' + esc(a.qty), a.value && '₹' + esc(a.value), (a.txn_from || a.txn_to) && `Transactions ${fmtDate(a.txn_from || '')} to ${fmtDate(a.txn_to || '')}`].filter(Boolean).join(' · ');
  const adv = a.adverse ? `<div class="note warn"><b>Adverse observation</b> (${fmtDate(a.adverse.decided_on)}, ${esc(a.adverse.decided_by)}): ${esc(a.adverse.observation)}${a.adverse.late ? `<br>Late because: ${esc(a.adverse.late)}` : ''}<br>${a.adverse.reported_on ? `Reported to ${esc(a.adverse.to)} on ${fmtDate(a.adverse.reported_on)} (${esc(a.adverse.report_ref)})` : `Report to ${esc(a.adverse.to)} by ${fmtDate(a.adverse.due)}`}</div>` : '';
  return `<p class="small">${esc(a.detail || '')}</p>${facts ? `<p class="dim small">${facts}</p>` : ''}<p class="small">Due ${fmtDate(a.due || '')}${a.half_yearly ? ' (half-yearly alert: 60 days)' : ''}</p>${adv}
    ${rows.length ? tbl([['Checklist point'], ['State'], ['Note']], rows) : '<p class="dim small">No checklist for this alert: record the review in its notes.</p>'}
    <div class="fld"><label>Documents</label><ul class="small">${docs || '<li class="dim">None yet</li>'}</ul>${open ? `<label class="btn sm">Attach a PDF, JPEG or PNG<input type="file" id="ds-doc" data-id="${esc(a.id)}" accept=".pdf,.jpg,.jpeg,.png" hidden></label>` : ''}</div>
    ${open ? `<div class="split">${rows.length ? `<button class="btn pri" data-act="dsSaveCheck" data-id="${esc(a.id)}">Save the checklist</button>` : ''}${isC && !a.adverse ? `<button class="btn bad" data-act="dsAdverse" data-id="${esc(a.id)}">Adverse observation</button>` : ''}${isC && a.adverse && !a.adverse.reported_on ? `<button class="btn" data-act="dsReported" data-id="${esc(a.id)}">Record the report</button>` : ''}</div>` : ''}`;
}

Object.assign(A, {
  dsWork: id => {
    const a = dsAlert(id);
    if (!a) return;
    modal(mhd(a.type, `${a.source}${a.area ? ' · ' + a.area : ''} · ${a.id}${a.ucc ? ' · ' + a.ucc : ''}`) + `<div class="bd" id="ds-work">${dsWorkHTML(a)}</div><div class="ft"><button class="btn" data-act="close">Close</button></div>`);
  },
  dsSaveCheck: id => {
    const items = [];
    document.querySelectorAll('[id^="ds-st-"]').forEach(el => { const i = el.id.slice(6); items.push({ key: el.dataset.key, state: el.value, note: ($('ds-nt-' + i) || {}).value || '' }); });
    act(() => api('PUT', `/v1/surveillance/alerts/${encodeURIComponent(id)}/checklist`, { items }), 'Checklist saved');
  },
  dsDoc: (id, el) => download(`/v1/surveillance/alerts/${encodeURIComponent(id)}/docs/${encodeURIComponent(el.dataset.doc)}`, 'document'),
  dsAdverse: id => {
    const a = dsAlert(id) || {};
    confirmModal('Adverse observation', a.source === 'CDSL' ? 'Answer CDSL’s alert as adverse on CDSL’s surveillance system within 7 days; do not report it again as the DP’s own.' : 'Due with CDSL (or the exchange) within 7 days. The DP’s own observations are made within two months of the transactions.',
      '<div class="fld"><label for="ds-ob">Analysis, rationale and action taken</label><textarea class="in" id="ds-ob"></textarea></div><div class="fld"><label for="ds-late">If the transactions are more than two months old, why the observation is late</label><input class="in" id="ds-late"></div>',
      'Record', () => act(() => post(`/v1/surveillance/alerts/${encodeURIComponent(id)}/adverse`, { observation: $('ds-ob').value, late: $('ds-late').value }), 'Adverse observation recorded: report it within 7 days'), true);
  },
  dsReported: id => confirmModal('Adverse observation reported', 'The date it was reported and the reference CDSL or the exchange gave.',
    `<div class="fld"><label for="ds-rd">Date reported</label><input class="in" type="date" id="ds-rd" value="${esc(S.date)}"></div><div class="fld"><label for="ds-rr">Reference</label><input class="in" id="ds-rr"></div>`,
    'Record', () => act(() => post(`/v1/surveillance/alerts/${encodeURIComponent(id)}/adverse-reported`, { date: $('ds-rd').value, ref: $('ds-rr').value }), 'Recorded')),
  dsNil: () => {
    const k = $('ds-kind').value;
    act(() => post(`/v1/surveillance/downloads/${k}/nil`, { note: '' }), 'Recorded: nothing to download');
  },
  dsSample: (id, el) => confirmModal('Check the closure', 'Agree with how the alert was closed, or say what is wrong and what is to be done.',
    '<div class="fld"><label for="ds-sr">Result</label><select class="in" id="ds-sr"><option>Agree</option><option>Disagree</option></select></div><div class="fld"><label for="ds-sn">Note</label><textarea class="in" id="ds-sn"></textarea></div>',
    'Record', () => act(() => post(`/v1/surveillance/alerts/${encodeURIComponent(id)}/sample`, { quarter: el.dataset.q, result: $('ds-sr').value, note: $('ds-sn').value }), 'Recorded')),
  dsGroups: async () => {
    try {
      const gs = await get('/v1/surveillance/connected');
      modal(mhd('Connected clients', 'Clients joined by a shared mobile, e-mail, bank account or address.') + `<div class="bd">${tbl([['Group'], ['Clients'], ['What they share']], gs.map(g => `<tr><td class="mono">${esc(g.id)}</td><td class="wrap mono">${g.members.map(esc).join(', ')}</td><td class="wrap small">${g.links.map(esc).join('<br>')}</td></tr>`), { empty: 'No connected clients.' })}</div><div class="ft"><button class="btn" data-act="close">Close</button></div>`);
    } catch (e) { toast(e.message, true); }
  },
  dsThr: () => {
    const t = C.dsAlerts && C.dsAlerts.ds && C.dsAlerts.ds.thr;
    if (!t) return;
    const f = [['income_times', 'Off-market and pledges in 30 days above this many times the declared income'], ['mods_per_month', 'Changes of details in a month above'], ['after_mod_days', 'Days after a change a transfer is flagged'], ['dormant_days', 'Days without a transaction: dormant'], ['fan_count', 'Accounts sent to (or from) in 30 days'], ['cluster_size', 'Cluster: more accounts sharing a detail than'], ['pass_min', 'Pass-through: receipts of at least (₹)'], ['pass_days', 'Pass-through window (days)'], ['pass_pct', 'Pass-through: share paid back out (%)'], ['trade_pct', 'Pass-through: trades under (% of receipts)'], ['front_minutes', 'Staff trade within minutes before a client order'], ['front_value', 'Client order of at least (₹)'], ['download_days', 'CDSL lists downloaded at least every (days)'], ['sample_pct', 'Sample of closures (%)'], ['sample_min', 'Sample of closures: at least']];
    const rk = ['Low', 'Medium', 'High'];
    confirmModal('Surveillance thresholds', 'Value and count limits are scaled by the client’s risk category. Set what the firm’s surveillance policy approves.',
      `<div class="form">${f.map(x => `<div class="fld"><label for="ds-t-${x[0]}">${esc(x[1])}</label><input class="in" type="number" id="ds-t-${x[0]}" value="${esc(String(t[x[0]] || ''))}"></div>`).join('')}${rk.map(r => `<div class="fld"><label for="ds-r-${r}">Scale for ${r} risk (%)</label><input class="in" type="number" id="ds-r-${r}" value="${esc(String((t.risk_pct || {})[r] || ''))}"></div>`).join('')}</div>`,
      'Save', () => {
        const b = { risk_pct: {} };
        f.forEach(x => { b[x[0]] = +$('ds-t-' + x[0]).value || 0; });
        rk.forEach(r => { b.risk_pct[r] = +$('ds-r-' + r).value || 0; });
        return act(() => api('PUT', '/v1/surveillance/thresholds', b), 'Thresholds saved');
      });
  },
});

document.addEventListener('change', async e => {
  const el = e.target;
  if (!el.files || !el.files.length || (el.id !== 'ds-file' && el.id !== 'ds-doc')) return;
  const f = el.files[0];
  try {
    if (el.id === 'ds-file') {
      const k = $('ds-kind').value;
      const r = await api('POST', `/v1/surveillance/import/${k}?name=` + encodeURIComponent(f.name), await f.arrayBuffer(), true);
      toast(`${r.download.added} new alert${r.download.added === 1 ? '' : 's'}, ${r.download.duplicates} already loaded${r.unmatched.length ? `, ${r.unmatched.length} with no client on record` : ''}`);
    } else {
      await api('POST', `/v1/surveillance/alerts/${encodeURIComponent(el.dataset.id)}/docs?name=` + encodeURIComponent(f.name), await f.arrayBuffer(), true);
      toast('Document attached'); closeModal();
    }
    render({ keep: true });
  } catch (err) { toast(err.message, true); }
});
