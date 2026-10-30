'use strict';
/* Ledgerline console: dealers and trading terminals (screen "dealers", a tab
   of Branches & partners). Who trades on each terminal or user ID, each
   dealer's NISM Series VIII certificate, dealer-wise business with the
   exceptions (trades no dealer is known for, trades by a dealer whose
   certificate had run out), the inspection list and BSE's dealer upload
   files. Loaded after app.js. */

Object.assign(UI, { dlTab: 'dealers', dlSt: 'Active', dlQ: '', dlFrom: '', dlTo: '' });
const dlCan = () => has('Ops maker', 'Compliance officer');
const dlLeaveCan = () => has('Compliance officer');
const dlNISMPill = r => pill(r.nism_state === 'Valid' ? 'Valid' : r.nism_state === 'Missing' ? 'No NISM' : r.nism_state === 'Expired' ? 'Expired' : `${r.nism_days} days left`,
  { Valid: 'good', Expiring: 'warn', Expired: 'bad', Missing: 'nt' }[r.nism_state] || 'nt');
const dlStPill = s => pill(s, s === 'Active' ? 'good' : 'nt');
const dlTerms = d => (d.terminals || []).map(t => `<span class="mono small">${esc(t.exchange)} ${esc(t.terminal_id)}</span>${t.closed_on && t.closed_on <= S.date ? ' <span class="dim small">closed</span>' : ''}`).join('<br>') || '<span class="dim">—</span>';
const dlWhere = r => r.partner ? `AP ${esc(r.partner)}${r.ap_name ? ` <span class="dim small">${esc(r.ap_name)}</span>` : ''}` : esc(r.branch || '');
const DL_FILES = [['bse-dealer', 'Dealer registration', 'Each dealer’s BSE terminals: A to add (no BSE record ID yet), M to modify; a closed terminal is sent as inactive, or as P (permanent deletion) when marked so.'],
  ['bse-nism', 'NISM certificates', 'Each active dealer’s NISM Series VIII certificate: A the first time, RN after a renewal (new certificate or date), M otherwise. Dates MM/DD/YYYY.'],
  ['bse-dealer-pan', 'Dealer PAN', 'PAN with the location ID and office address of each open BSE terminal.']];

function dlMonthStart(d) { return (d || S.date).slice(0, 8) + '01'; }

V.dealers = {
  load: async () => {
    const v = await get(`/v1/dealers?date=${S.date}`);
    if (UI.dlTab === 'report') {
      UI.dlFrom ||= dlMonthStart(); UI.dlTo ||= S.date;
      try { v.report = await get(`/v1/dealers/report?from=${UI.dlFrom}&to=${UI.dlTo}`); } catch (e) { v.reportErr = e; }
    }
    if (UI.dlTab === 'files' && v.can_edit) {
      v.files = await Promise.all(DL_FILES.map(([k]) => get(`/v1/dealers/files/${k}?check=1`).catch(e => ({ kind: k, error: e.message, problems: [] }))));
    }
    return v;
  },
  html: d => {
    const c = d.counts;
    const acts = (d.can_edit ? '<button class="btn pri" data-act="dlNew">Add dealer</button><input type="file" id="dl-import" accept=".csv,text/csv" hidden><button class="btn" data-act="dlImport">Import CSV</button><button class="btn" data-act="dl" data-id="/v1/dealers/export.csv" data-name="dealers.csv">Export CSV</button>' : '') +
      '<button class="btn" data-act="dl" data-id="/v1/dealers/inspection.csv" data-name="terminals_and_users.csv">Terminals and users (inspection)</button>';
    return ph('Dealers & terminals', 'Who trades on each terminal or user ID, on each exchange, with each dealer’s NISM Series VIII certificate. Trades carry the terminal ID, so business is credited to dealers from it; a terminal ID may be a pattern (* any characters). The compliance officer is reminded 60, 30 and 7 days before a certificate runs out and on the day, and when a dealer who left still has open terminals.', acts) +
      `<div class="kpis">${kpi('Active dealers', nf(c.active), c.left ? c.left + ' left' : '')}${kpi('Open terminals', nf(c.terminals), c.left_with_terminals ? `<span class="neg">${c.left_with_terminals} dealer(s) who left still have terminals</span>` : '')}
        ${kpi('NISM expiring', nf(c.expiring_60), 'within 60 days', c.expiring_60 ? 'warn' : '')}${kpi('NISM expired', nf(c.expired), c.nism_missing ? c.nism_missing + ' with no certificate' : 'block their terminals', c.expired ? 'bad' : '')}
        ${kpi('Trades with no dealer', nf(c.unattributed_trades), fmtDate(d.date), c.unattributed_trades ? 'warn' : '')}</div>` +
      `<div class="panel"><div class="hd"><div class="seg">${[['dealers', 'Dealers'], ['expiring', `Expiring · ${d.expiring.length}`], ['report', 'Report'], ['files', 'Exchange files']].map(([k, l]) => `<button class="${UI.dlTab === k ? 'on' : ''}" data-act="dlTab" data-id="${k}">${l}</button>`).join('')}</div></div></div>` +
      ({ dealers: dlList, expiring: dlExpiring, report: dlReport, files: dlFiles }[UI.dlTab] || dlList)(d);
  }
};

function dlRow(r, d) {
  const acts = (d.can_edit ? `<button class="btn sm" data-act="dlEdit" data-id="${esc(r.id)}">Edit</button>` : '') + (d.can_leave && r.status === 'Active' ? `<button class="btn sm" data-act="dlLeft" data-id="${esc(r.id)}">Left</button>` : '');
  return `<tr><td class="mono">${esc(r.id)}</td><td><b>${esc(r.name)}</b><div class="dim small mono">${esc(r.pan_masked)}</div></td><td>${esc(r.relationship)}<div class="dim small">${dlWhere(r)}</div></td>
    <td>${dlTerms(r)}</td><td class="mono small">${esc(r.nism.cert_no || '—')}<div>${r.nism.valid_till ? 'till ' + fmtDate(r.nism.valid_till) : ''}</div></td><td>${dlNISMPill(r)}</td>
    <td>${dlStPill(r.status)}${r.left_on ? `<div class="dim small">${fmtDate(r.left_on)}${r.open_terminals ? ` · <span class="neg">${r.open_terminals} open</span>` : ''}</div>` : ''}</td><td class="nowrap">${acts}</td></tr>`;
}

function dlList(d) {
  return `<div class="panel"><div class="hd"><div class="tools">${segBtns('dlSt', [['Active', 'Active'], ['Left', 'Left'], ['', 'All']], 'dl-t')}</div><input class="in" data-ui="dlQ" data-part="dl-t" placeholder="Filter by name, ID, terminal, branch or AP" value="${esc(UI.dlQ)}"></div><div id="dl-t">${P['dl-t'](d)}</div></div>`;
}
P['dl-t'] = d => {
  const q = (UI.dlQ || '').trim().toLowerCase();
  const rows = d.dealers.filter(r => !UI.dlSt || r.status === UI.dlSt).filter(r => !q || [r.id, r.name, r.branch, r.partner, r.ap_name, ...(r.terminals || []).map(t => t.terminal_id)].join(' ').toLowerCase().includes(q));
  return tbl([['ID'], ['Dealer'], ['Relationship'], ['Terminals'], ['NISM'], ['Validity'], ['Status'], ['']], rows.map(r => dlRow(r, d)), { tall: true, empty: d.dealers.length ? 'No dealers match.' : 'No dealers yet. Add them one by one or import a CSV.' });
};

function dlExpiring(d) {
  return `<div class="panel"><div class="hd"><h3>NISM certificates to renew</h3><small>active dealers: expired, expiring within 60 days, or with open terminals and no certificate</small></div>
    ${tbl([['ID'], ['Dealer'], ['Relationship'], ['Terminals'], ['NISM'], ['Validity'], ['Status'], ['']], d.expiring.map(r => dlRow(r, d)), { empty: 'Every active dealer’s certificate is valid for more than 60 days.' })}</div>`;
}

function dlReport(d) {
  const r = d.report;
  const head = `<div class="pad" style="padding-bottom:0"><div class="tools"><label class="dim small" for="dl-from">Trades from</label><input class="in sm" type="date" id="dl-from" value="${esc(UI.dlFrom)}"><span class="dim">to</span><input class="in sm" type="date" id="dl-to" value="${esc(UI.dlTo)}"><button class="btn sm" data-act="dlPeriod">Show</button></div></div>`;
  if (d.reportErr) return `<div class="panel">${head}<div class="bd">${errHTML(d.reportErr)}</div></div>`;
  if (!r) return `<div class="panel">${head}</div>`;
  const brok = r.brokerage_from_bills;
  return `<div class="panel">${head}<div class="bd"><div class="kv"><div><small>Trades</small><b class="mono">${nf(r.trades)}</b></div><div><small>Credited to a dealer</small><b class="mono">${nf(r.attributed)}</b></div><div><small>Turnover</small><b class="mono">${cmp(r.turnover)}</b></div>${brok ? `<div><small>Brokerage billed</small><b class="mono">${inr(r.brokerage, 2)}</b></div>` : ''}</div>
      ${brok ? '<p class="dim small">Brokerage is billed per client, exchange segment and day; each bill’s brokerage is shared among the dealers who traded in it by turnover.</p>' : '<p class="dim small">No bills in the period, so no brokerage is shown.</p>'}</div>
    ${tbl([['Dealer'], ['Relationship'], ['Trades', 'r'], ['Clients', 'r'], ['Turnover', 'r'], ...(brok ? [['Brokerage', 'r']] : [])], r.rows.map(x => `<tr><td><b>${esc(x.name)}</b> <span class="dim mono small">${esc(x.dealer_id)}</span>${x.status === 'Left' ? ' ' + pill('Left') : ''}</td><td>${esc(x.relationship)}<div class="dim small">${x.partner ? 'AP ' + esc(x.partner) : esc(x.branch || '')}</div></td>
      <td class="r">${nf(x.trades)}</td><td class="r">${nf(x.clients)}</td><td class="r">${inr(x.turnover)}</td>${brok ? `<td class="r">${inr(x.brokerage, 2)}</td>` : ''}</tr>`), { empty: 'No trades credited to a dealer in the period.' })}</div>
    <div class="panel"><div class="hd"><h3>Trades from terminals with no dealer</h3><small>add the terminal to a dealer, or a pattern that covers it</small></div>
      ${tbl([['Exchange'], ['Segment'], ['Terminal'], ['Trades', 'r'], ['Clients', 'r'], ['Turnover', 'r'], ['Dates']], r.unattributed.map(x => `<tr><td>${esc(x.exchange)}</td><td>${esc(x.segment)}</td><td class="mono">${x.terminal ? esc(x.terminal) : '<span class="dim">none on the trade</span>'}</td><td class="r">${nf(x.trades)}</td><td class="r">${nf(x.clients)}</td><td class="r">${inr(x.turnover)}</td><td class="small">${fmtDate(x.first)}${x.last !== x.first ? ' – ' + fmtDate(x.last) : ''}</td></tr>`), { empty: 'Every trade came from a known terminal.' })}</div>
    <div class="panel"><div class="hd"><h3>Trades by a dealer whose NISM certificate had expired</h3><small>on the trade date, earlier certificates included</small></div>
      ${tbl([['Date'], ['Dealer'], ['Exchange'], ['Terminal'], ['Why'], ['Trades', 'r'], ['Clients', 'r'], ['Turnover', 'r']], r.expired_nism.map(x => `<tr><td class="n">${fmtDate(x.date)}</td><td><b>${esc(x.name)}</b> <span class="dim mono small">${esc(x.dealer_id)}</span></td><td>${esc(x.exchange)}</td><td class="mono">${esc(x.terminal)}</td><td class="small neg">${esc(x.reason)}</td><td class="r">${nf(x.trades)}</td><td class="r">${nf(x.clients)}</td><td class="r">${inr(x.turnover)}</td></tr>`), { empty: 'None: every dealer had a valid certificate on the days they traded.' })}</div>`;
}

function dlFiles(d) {
  if (!d.can_edit) return '<div class="note">BSE’s dealer files are made by the ops maker or the compliance officer.</div>';
  const pick = `<div class="fld" style="max-width:520px"><label for="dl-ids">Dealers (IDs separated by commas; leave empty for all)</label><input class="in mono" id="dl-ids" placeholder="DL-0001, DL-0004" value="${esc(UI.dlIds || '')}"></div>`;
  return `<div class="note">BSE takes three upload files for dealers; each is pipe-delimited with no header and exactly one newline at the end. Upload them on BSE’s member portal, then download BSE’s records and enter each terminal’s <b>BSE record ID</b> on the dealer: later changes go as modifications with that ID. <b>NSE</b> publishes no dealer file layout, so NSE terminals are registered on NSE’s own portal.</div>
    <div class="panel"><div class="bd">${pick}</div>${tbl([['File'], ['What it holds'], ['Rows', 'r'], ['Check'], ['']], (d.files || []).map((f, i) => { const [k, l, about] = DL_FILES[i]; const probs = f.problems || [];
      return `<tr><td><b>${l}</b><div class="dim small mono">${esc(f.name || '')}</div></td><td class="wrap small dim">${about}</td><td class="r">${f.error ? '—' : nf(f.rows)}</td>
        <td class="wrap small">${f.error ? `<span class="neg">${esc(f.error)}</span>` : probs.length ? `<span class="neg">${probs.length} problem(s)</span><ul>${probs.slice(0, 8).map(p => `<li>${esc(p)}</li>`).join('')}${probs.length > 8 ? `<li>…and ${probs.length - 8} more</li>` : ''}</ul>` : f.rows ? pill('Ready', 'good') : '<span class="dim">Nothing to send</span>'}</td>
        <td><button class="btn sm" data-act="dlFile" data-id="${k}" ${f.error ? 'disabled' : ''}>Download</button></td></tr>`; }))}</div>`;
}

/* ---------- add / edit ---------- */
const DL_BLANK_T = () => ({ exchange: 'NSE', segments: ['CM'], terminal_id: '', location_id: '', ctcl_id: '', algo: false, algo_code: '', connection_mode: '', branch_id: '', vendor: '', trading_id: '', active_from: S.date, closed_on: '', purge: false, exchange_record_id: '' });
const DL_KEYS = ['id', 'first_name', 'middle_name', 'last_name', 'pan', 'dob', 'mobile', 'email', 'branch', 'partner', 'relationship', 'office', 'residence', 'nism', 'nism_history', 'nism_filed', 'terminals', 'status', 'left_on', 'notes', 'created_by', 'created_at', 'updated_by', 'updated_at'];

function dlForm(x) {
  const d = C.dealers, v = (k, def = '') => esc(x[k] ?? def);
  const f = (id, label, val, extra = '') => `<div class="fld"><label for="${id}">${label}</label><input class="in" id="${id}" value="${esc(val ?? '')}" ${extra}></div>`;
  const sel = (id, label, val, opts) => `<div class="fld"><label for="${id}">${label}</label><select class="in" id="${id}">${opts.map(([k, l]) => `<option value="${esc(k)}" ${String(val) === String(k) ? 'selected' : ''}>${esc(l)}</option>`).join('')}</select></div>`;
  const o = x.office || {}, r = x.residence || {}, n = x.nism || {};
  const terms = (x.terminals || []).map((t, i) => `<div class="panel" style="margin:8px 0;padding:10px"><div class="grid g3">
      ${sel(`dlt-ex-${i}`, 'Exchange', t.exchange, d.exchanges.map(e => [e, e]))}${f(`dlt-id-${i}`, 'Terminal / user ID (pattern allowed: 333*)', t.terminal_id, 'class="in mono"')}
      <div class="fld"><label>Segments</label><div>${d.segments.map(s => `<label class="chk" style="display:inline-flex;margin-right:8px"><input type="checkbox" data-dlseg="${i}" value="${s}" ${(t.segments || []).includes(s) ? 'checked' : ''}> ${s}</label>`).join('')}</div></div>
      ${f(`dlt-loc-${i}`, 'Location ID', t.location_id)}${f(`dlt-ctcl-${i}`, 'CTCL ID', t.ctcl_id)}
      <div class="fld"><label>Algo</label><label class="chk"><input type="checkbox" id="dlt-algo-${i}" ${t.algo ? 'checked' : ''}> Algo terminal</label></div>
      ${sel(`dlt-ac-${i}`, 'BSE order type code (14th digit)', t.algo_code, [['', 'From the algo flag'], ...Object.entries(d.algo_codes).map(([k, l]) => [k, k + ' · ' + l])])}${f(`dlt-cm-${i}`, 'Connection mode (code)', t.connection_mode)}${f(`dlt-br-${i}`, 'BSE TWS / branch ID', t.branch_id)}
      ${f(`dlt-vn-${i}`, 'CTCL vendor code', t.vendor)}${f(`dlt-tid-${i}`, 'Exchange-allotted trading ID', t.trading_id)}${f(`dlt-rec-${i}`, 'BSE record ID (for changes)', t.exchange_record_id)}
      ${f(`dlt-from-${i}`, 'Active from', t.active_from, 'type="date"')}${f(`dlt-to-${i}`, 'Closed on (blank while in use)', t.closed_on, 'type="date"')}
      <div class="fld"><label>At BSE</label><label class="chk"><input type="checkbox" id="dlt-purge-${i}" ${t.purge ? 'checked' : ''}> Delete permanently once closed</label></div></div>
      <button class="btn sm bad" data-act="dlDelTerm" data-id="${i}">Remove terminal</button></div>`).join('');
  return `<div class="bd"><h4 style="margin:0 0 6px">Dealer</h4><div class="grid g3">${f('dl-fn', 'First name', x.first_name)}${f('dl-mn', 'Middle name', x.middle_name)}${f('dl-ln', 'Last name', x.last_name)}
      ${f('dl-pan', 'PAN', x.pan, 'class="in mono" autocapitalize="characters" maxlength="10"')}${f('dl-dob', 'Date of birth', x.dob, 'type="date"')}${f('dl-mob', 'Mobile', x.mobile, 'inputmode="numeric"')}${f('dl-em', 'Email', x.email, 'type="email"')}
      ${sel('dl-rel', 'Relationship', x.relationship || 'Employee', d.relationships.map(k => [k, k]))}${sel('dl-br', 'Branch', x.branch, [['', '—'], ...d.branches.map(b => [b, b])])}${sel('dl-ap', 'AP', x.partner, [['', '—'], ...d.aps.map(a => [a.code, a.code + ' · ' + a.name])])}</div>
    <h4 style="margin:12px 0 6px">Office</h4><div class="grid g3">${f('dl-oa', 'Address', o.address)}${f('dl-oar', 'Area', o.area)}${f('dl-oc', 'City', o.city)}${f('dl-os', 'State', o.state)}${f('dl-op', 'Pincode', o.pincode, 'inputmode="numeric" maxlength="6"')}</div>
    <h4 style="margin:12px 0 6px">Residence <span class="dim small">(for BSE’s registration file)</span></h4><div class="grid g3">${f('dl-ra', 'Address', r.address)}${f('dl-rar', 'Area', r.area)}${f('dl-rc', 'City', r.city)}${f('dl-rp', 'Pincode', r.pincode, 'inputmode="numeric" maxlength="6"')}</div>
    <h4 style="margin:12px 0 6px">NISM Series VIII</h4><div class="grid g3">${f('dl-nc', 'Certificate (enrolment) number', n.cert_no, 'class="in mono"')}${f('dl-ne', 'Exam date', n.exam_date, 'type="date"')}${f('dl-nv', 'Valid till', n.valid_till, 'type="date"')}</div>
    <h4 style="margin:12px 0 6px">Terminals</h4>${terms || '<p class="dim small">No terminals. A dealer with an active terminal needs a NISM certificate.</p>'}<button class="btn sm" data-act="dlAddTerm">Add terminal</button>
    <div class="fld" style="margin-top:12px"><label for="dl-notes">Notes</label><textarea class="in" id="dl-notes" rows="2">${v('notes')}</textarea></div><div id="dl-err"></div></div>
    <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="dlSave">${x.id ? 'Save changes' : 'Add dealer'}</button></div>`;
}

function dlOpen(x) {
  UI.dlEdit = x;
  wideModal(mhd(x.id ? `Edit ${esc(x.id)}` : 'Add dealer', x.id ? esc([x.first_name, x.middle_name, x.last_name].filter(Boolean).join(' ')) : 'A person who places orders on the firm’s terminals: an employee, an AP or an AP’s employee.') + dlForm(x));
}

// dlCollect reads the form back into the dealer being edited.
function dlCollect() {
  const x = UI.dlEdit, val = id => ($(id)?.value ?? '').trim();
  Object.assign(x, { first_name: val('dl-fn'), middle_name: val('dl-mn'), last_name: val('dl-ln'), pan: val('dl-pan').toUpperCase(), dob: val('dl-dob'), mobile: val('dl-mob'), email: val('dl-em'),
    relationship: val('dl-rel'), branch: val('dl-br'), partner: val('dl-ap'), notes: $('dl-notes').value.trim(),
    office: { address: val('dl-oa'), area: val('dl-oar'), city: val('dl-oc'), state: val('dl-os'), pincode: val('dl-op') },
    residence: { address: val('dl-ra'), area: val('dl-rar'), city: val('dl-rc'), pincode: val('dl-rp') },
    nism: { ...(x.nism || {}), cert_no: val('dl-nc'), series: 'VIII', exam_date: val('dl-ne'), valid_till: val('dl-nv') } });
  x.terminals = (x.terminals || []).map((t, i) => ({ ...t, exchange: val(`dlt-ex-${i}`), terminal_id: val(`dlt-id-${i}`), location_id: val(`dlt-loc-${i}`), ctcl_id: val(`dlt-ctcl-${i}`),
    segments: [...document.querySelectorAll(`[data-dlseg="${i}"]:checked`)].map(c => c.value), algo: $(`dlt-algo-${i}`).checked, algo_code: val(`dlt-ac-${i}`), connection_mode: val(`dlt-cm-${i}`),
    branch_id: val(`dlt-br-${i}`), vendor: val(`dlt-vn-${i}`), trading_id: val(`dlt-tid-${i}`), exchange_record_id: val(`dlt-rec-${i}`), active_from: val(`dlt-from-${i}`), closed_on: val(`dlt-to-${i}`), purge: $(`dlt-purge-${i}`).checked }));
  return Object.fromEntries(DL_KEYS.filter(k => k in x).map(k => [k, x[k]]));
}

Object.assign(A, {
  dlTab: id => { UI.dlTab = id; render({ keep: true }); },
  dlPeriod: () => { UI.dlFrom = $('dl-from').value; UI.dlTo = $('dl-to').value; render({ keep: true }); },
  dlNew: () => dlOpen({ relationship: 'Employee', office: {}, residence: {}, nism: { series: 'VIII' }, terminals: [] }),
  dlEdit: async id => { try { dlOpen(await get(`/v1/dealers/${encodeURIComponent(id)}`)); } catch (e) { toast(e.message, true); } },
  dlAddTerm: () => { dlCollect(); UI.dlEdit.terminals.push(DL_BLANK_T()); dlOpen(UI.dlEdit); },
  dlDelTerm: id => { dlCollect(); UI.dlEdit.terminals.splice(+id, 1); dlOpen(UI.dlEdit); },
  dlSave: async () => {
    const b = dlCollect();
    try {
      const r = b.id ? await api('PUT', `/v1/dealers/${encodeURIComponent(b.id)}`, b) : await post('/v1/dealers', b);
      closeModal(); toast(`${r.id} saved`); render({ keep: true });
    } catch (e) { $('dl-err').innerHTML = errHTML(e); }
  },
  dlLeft: id => {
    const r = C.dealers.dealers.find(x => x.id === id) || {};
    confirmModal('Dealer left', `${esc(r.name || id)} · ${r.open_terminals || 0} open terminal(s)`, `<div class="fld"><label for="dl-lo">Left on</label><input class="in mono" type="date" id="dl-lo" value="${esc(S.date)}"></div>
      <label class="chk"><input type="checkbox" id="dl-lc" checked> Close the open terminals from the next day</label><div class="fld"><label for="dl-ln2">Note</label><input class="in" id="dl-ln2" placeholder="e.g. resigned; terminals disabled at NSE and BSE"></div>`,
      'Mark as left', () => act(() => post(`/v1/dealers/${encodeURIComponent(id)}/left`, { left_on: $('dl-lo').value, close_terminals: $('dl-lc').checked, note: $('dl-ln2').value }), 'Dealer marked as left'), true);
  },
  dlImport: () => {
    const f = $('dl-import'); if (!f) return;
    f.onchange = async () => {
      if (!f.files.length) return;
      try { const r = await api('POST', '/v1/dealers/import', await f.files[0].arrayBuffer(), true); toast(`${r.added} added, ${r.updated} updated`); render({ keep: true }); }
      catch (e) { modal(mhd('Dealer file not loaded', 'Nothing was changed. Correct these lines and load the file again.') + `<div class="bd">${errHTML(e)}<p class="dim small">Columns: ${esc((C.dealers.import_columns || []).join(', '))}. PAN, FirstName and Relationship are required; one row per terminal.</p></div><div class="ft"><button class="btn pri" data-act="close">Close</button></div>`); }
      f.value = '';
    };
    f.click();
  },
  dlFile: async id => {
    UI.dlIds = ($('dl-ids')?.value || '').trim();
    const q = UI.dlIds ? '?ids=' + encodeURIComponent(UI.dlIds) : '';
    await download(`/v1/dealers/files/${id}${q}`, id + '.txt');
    if (id === 'bse-nism') render({ keep: true });
  },
});
