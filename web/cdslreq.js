'use strict';
/* Ledgerline console: CDSL requests (screen "cdslreq", a tab of the
   Depository group). Every call the DP makes to CDSL about a demat account
   (open, modify, BO setup upload, transfer cum closure, add beneficiary)
   with its status and CDSL's reference, the actions each one waits for,
   the DP's BO defaults, and the client drawer's active demat count.
   Loaded after app.js. */

Object.assign(UI, { crSt: 'open', crQ: '', crKind: '' });
const CR_PILL = { 'Needs details': 'warn', 'Needs file': 'warn', 'Waiting for checker': 'warn', Approved: 'acc', 'Sent to CDSL': 'acc', 'Waiting for client': 'acc', 'Confirmed by client': 'acc',
  Completed: 'good', 'Done in CDAS': 'good', 'Rejected by CDSL': 'bad', Failed: 'bad', Cancelled: 'nt', 'Do in CDAS': 'warn' };
const CR_OPEN = s => !['Completed', 'Done in CDAS', 'Cancelled'].includes(s);
const crBO = b => b ? `<span class="mono">${esc(b)}</span>` : '<span class="dim">—</span>';

V.cdslreq = {
  load: () => get('/v1/cdsl/requests'),
  html: d => {
    const c = d.counts || {}, mk = d.can_make, ck = d.can_check;
    const acts = mk ? '<button class="btn" data-act="crNew" data-id="Open account">Open an account</button><button class="btn" data-act="crNew" data-id="Add beneficiary">Add beneficiary</button><button class="btn" data-act="crSetupFile">BO setup file</button><input type="file" id="cr-up" hidden><button class="btn" data-act="crUpload" title="Upload a BO setup file made elsewhere">Attach a setup file</button><button class="btn" data-act="crDetails">Client’s CDSL details</button><button class="btn" data-act="crDelHolder">Deceased holder</button><button class="btn" data-act="crCdasFiles">Signature / closure file</button>' : '';
    const fam = (d.families || []).map(f => `<span class="tag">${esc(f.label)}: <b>${f.on ? 'On' : 'Off'}</b></span>`).join(' ');
    return ph('CDSL requests', 'Calls to CDSL’s APIs about demat accounts: account opening (BO online), modifications and BO setup files (BO uploads), transfer cum closure and off-market beneficiaries (confirmed by the client with CDSL’s OTP). A checker approves each before it goes; CDSL’s reference and every answer are kept. With an API switched off in Connections, the request says “Do in CDAS”: do it there and record it here.', acts) +
      (d.connection_set ? '' : '<div class="note">The CDSL APIs connection is not set up (Admin → Connections). Until it is, nothing goes to CDSL.</div>') +
      `<div class="kpis">${kpi('Open', nf(c.open || 0), 'not yet completed')}${kpi('For a checker', nf(c['Waiting for checker'] || 0), '', c['Waiting for checker'] ? 'warn' : '')}${kpi('Needs details or a file', nf((c['Needs details'] || 0) + (c['Needs file'] || 0)), '', (c['Needs details'] || 0) + (c['Needs file'] || 0) ? 'warn' : '')}
        ${kpi('With CDSL or the client', nf((c['Sent to CDSL'] || 0) + (c['Waiting for client'] || 0) + (c['Confirmed by client'] || 0)), '')}${kpi('Failed', nf((c.Failed || 0) + (c['Rejected by CDSL'] || 0)), 'retry once fixed', (c.Failed || 0) + (c['Rejected by CDSL'] || 0) ? 'bad' : '')}</div>
      <div class="panel"><div class="hd"><div class="tools">${segBtns('crSt', [['open', 'Open'], ['fail', 'Failed'], ['', 'All']], 'cr-t')}<select class="in sm" data-ui="crKind" data-part="cr-t"><option value="">Every kind</option>${(d.kinds || []).map(k => `<option ${UI.crKind === k ? 'selected' : ''}>${esc(k)}</option>`).join('')}</select></div><input class="in" data-ui="crQ" data-part="cr-t" placeholder="Filter by client, BO ID or reference" value="${esc(UI.crQ)}"></div><div id="cr-t">${P['cr-t'](d)}</div></div>
      <div class="panel"><div class="hd"><h3>APIs</h3><small>switched on or off in Connections · CDSL APIs (DP)${d.env ? ' · ' + esc(d.env) : ''}</small></div><div class="pad">${fam || '<span class="dim">Not set</span>'}</div></div>
      <div class="panel"><div class="hd"><h3>BO defaults</h3><small>CDSL’s codes used for every new account where the client has nothing to give</small>${ck ? '<button class="btn sm" data-act="crDefaults">Edit</button>' : ''}</div><div class="pad small">${Object.entries(d.bo_defaults?.values || {}).map(([k, v]) => `<span class="tag">${esc(k.replace(/_/g, ' '))}: <b class="mono">${esc(v)}</b></span>`).join(' ') || '<span class="dim">None set yet: set them before the first account opening.</span>'}</div></div>`;
  }
};

P['cr-t'] = d => {
  const q = (UI.crQ || '').trim().toLowerCase();
  const rows = d.requests.filter(r => UI.crSt === 'open' ? CR_OPEN(r.status) : UI.crSt === 'fail' ? ['Failed', 'Rejected by CDSL'].includes(r.status) : true)
    .filter(r => !UI.crKind || r.kind === UI.crKind)
    .filter(r => !q || [r.id, r.ucc, r.name, r.bo_id, r.ref, r.cdsl_ref, r.source].join(' ').toLowerCase().includes(q));
  return tbl([['Request'], ['Kind'], ['Client'], ['BO ID'], ['Status'], ['CDSL reference'], ['Updated'], ['']], rows.map(r => `<tr><td class="n"><button class="linkish mono" data-act="crOpen" data-id="${esc(r.id)}">${esc(r.id)}</button><div class="dim small">${esc(r.source || '')}</div></td>
    <td>${esc(r.kind)} <span class="dim small">${esc(r.section)}</span></td><td>${r.ucc ? `<button class="linkish mono" data-act="client" data-id="${esc(r.ucc)}">${esc(r.ucc)}</button> ${esc(r.name || '')}` : (r.file ? `<span class="mono small">${esc(r.file.name)}</span>` : '')}</td>
    <td>${crBO(r.bo_id)}</td><td>${pill(r.status, CR_PILL[r.status])}${r.error ? `<div class="small neg wrap">${esc(r.error)}</div>` : ''}${(r.problems || []).length ? `<div class="small neg">${r.problems.length} thing(s) to fix</div>` : ''}</td>
    <td class="mono small">${esc(r.cdsl_ref || '—')}${r.cdsl_status ? `<div class="dim">${esc(r.cdsl_status)}</div>` : ''}</td><td class="n dim small">${fmtDate((r.updated || '').slice(0, 10))}</td><td class="nowrap">${crActs(r, d)}</td></tr>`),
    { tall: true, empty: d.requests.length ? 'No requests match.' : 'No CDSL requests yet.' });
};

function crActs(r, d) {
  const b = (a, l, cls = '') => `<button class="btn sm ${cls}" data-act="crAct" data-id="${esc(r.id)}" data-a="${a}">${l}</button>`;
  const out = [];
  if (d.can_check && r.status === 'Waiting for checker') out.push(b('approve', 'Approve', 'pri'), b('reject', 'Reject'));
  if (d.can_make && ['Failed', 'Rejected by CDSL'].includes(r.status)) out.push(b('retry', 'Retry', 'pri'));
  if (d.can_make && r.kind === 'Open account' && ['Needs details', 'Waiting for checker', 'Failed', 'Rejected by CDSL'].includes(r.status)) out.push(`<button class="btn sm" data-act="crFields" data-id="${esc(r.id)}">Details</button>`);
  if (d.can_make && ['Modify account', 'BO setup upload'].includes(r.kind) && ['Needs file', 'Waiting for checker', 'Failed', 'Rejected by CDSL'].includes(r.status)) out.push(`<button class="btn sm" data-act="crAttach" data-id="${esc(r.id)}">${r.file ? 'Replace file' : 'Attach file'}</button>`);
  if (d.can_make && r.status === 'Sent to CDSL' && ['Modify account', 'BO setup upload'].includes(r.kind)) out.push(b('status', 'Check status'));
  if (d.can_make && r.status === 'Waiting for client') out.push(b('link', 'New link'));
  if (d.can_check && ['Do in CDAS', 'Failed', 'Rejected by CDSL', 'Confirmed by client'].includes(r.status)) out.push(b('done', 'Done in CDAS'));
  if (d.can_check && r.pending_activation && r.status === 'Completed') out.push(b('activated', 'Activated'));
  if (d.can_make && ['Modify account', 'BO setup upload'].includes(r.kind) && ['Needs file', 'Waiting for checker', 'Failed', 'Rejected by CDSL'].includes(r.status) && (r.ucc || (r.fields || {}).uccs)) out.push(b('regenerate', 'Make file again'));
  if (d.can_check && r.kind === 'BO setup upload' && r.status === 'Completed' && r.ucc && !r.bo_id && !(r.fields || {}).delete_holder) out.push(`<button class="btn sm" data-act="crBOID" data-id="${esc(r.id)}">Record BO ID</button>`);
  if (r.reply_file) out.push(`<button class="btn sm" data-act="dl" data-id="/v1/cdsl/requests/${esc(r.id)}/file/reply" data-name="${esc(r.reply_file.name)}">CDSL’s file</button>`);
  return out.join('');
}

function crDetail(r) {
  const kv = (l, v) => v ? `<div><small>${l}</small><b>${v}</b></div>` : '';
  const list = (t, xs, cls = '') => (xs || []).length ? `<h4 style="margin:10px 0 4px">${t}</h4><ul class="small ${cls}">${xs.map(x => `<li>${esc(x)}</li>`).join('')}</ul>` : '';
  const ch = (r.changes || []).length ? `<h4 style="margin:10px 0 4px">Changes for CDSL</h4>${tbl([['Block'], ['Field'], ['What'], ['Was'], ['Now']], r.changes.map(c => `<tr><td class="small">${esc(c.section)}</td><td class="mono small">${esc(c.field)}</td><td>${esc(c.label)}</td><td class="small dim">${esc(c.old || '')}</td><td class="small"><b>${esc(c.new)}</b></td></tr>`))}` : '';
  const by = (r.buyers || []).length ? `<h4 style="margin:10px 0 4px">Buyer accounts</h4>${tbl([['BO ID'], ['Depository'], ['PAN exempt'], ['PAN'], ['Result'], ['CDSL reference']], r.buyers.map(x => `<tr><td class="mono">${esc(x.bo_id)}</td><td>${x.type === 'N' ? 'NSDL' : 'CDSL'}</td><td>${esc(x.exempt)}</td><td class="mono">${esc(x.pan || '')}</td><td>${x.status ? pill(x.status, x.status === 'Added' ? 'good' : 'bad') : '<span class="dim">—</span>'}${x.error ? `<div class="small neg">${esc(x.error)}</div>` : ''}</td><td class="mono small">${esc(x.cdsl_ref || '')}</td></tr>`))}` : '';
  const hist = tbl([['When'], ['Who'], ['What']], (r.history || []).slice().reverse().map(h => `<tr><td class="n small">${esc(h.at.replace('T', ' ').slice(0, 16))}</td><td class="small">${esc(h.by)}</td><td class="small wrap">${esc(h.what)}</td></tr>`));
  return `<div class="bd"><div class="tools">${pill(r.status, CR_PILL[r.status])}${r.pending_activation ? pill('Pending activation at CDSL', 'warn') : ''}</div>
    <div class="kv">${kv('Kind', esc(r.kind + ' ' + r.section))}${kv('Client', r.ucc ? esc(r.ucc + ' ' + (r.name || '')) : '')}${kv('BO ID', r.bo_id ? `<span class="mono">${esc(r.bo_id)}</span>` : '')}${kv('From', esc(r.source || ''))}
      ${kv('Our reference', r.ref ? `<span class="mono">${esc(r.ref)}</span>` : '')}${kv('CDSL reference', r.cdsl_ref ? `<span class="mono">${esc(r.cdsl_ref)}</span>` : '')}${kv('Maker', esc(r.maker))}${kv('Checker', esc(r.checker || ''))}${kv('Tries', r.tries ? String(r.tries) : '')}${kv('Link valid till', r.link_until && r.status === 'Waiting for client' ? esc(r.link_until.replace('T', ' ').slice(0, 16)) : '')}</div>
    ${r.error ? `<div class="errbox"><b>${esc(r.error)}</b></div>` : ''}${list('To fix before it can go', r.problems, 'neg')}${list('Worth knowing', r.warnings)}
    ${Object.keys(r.fields || {}).length ? `<h4 style="margin:10px 0 4px">Details given</h4><div class="small">${Object.entries(r.fields).map(([k, v]) => `<span class="tag">${esc(k)}: <b>${esc(v)}</b></span>`).join(' ')}</div>` : ''}
    ${ch}${by}${r.file ? `<p class="small">File: <button class="linkish mono" data-act="dl" data-id="/v1/cdsl/requests/${esc(r.id)}/file/sent" data-name="${esc(r.file.name)}">${esc(r.file.name)}</button> <span class="dim">${nf(r.file.size)} bytes</span></p>` : ''}
    ${r.request ? `<details><summary class="small">What was sent</summary><pre class="mono small wrap">${esc(r.request)}</pre></details>` : ''}${r.response ? `<details><summary class="small">CDSL’s last answer</summary><pre class="mono small wrap">${esc(r.response)}</pre></details>` : ''}
    <h4 style="margin:10px 0 4px">History</h4>${hist}</div><div class="ft"><button class="btn" data-act="close">Close</button></div>`;
}

const CR_FIELDS = [['address', 'Address (house, street, area)'], ['pin', 'PIN code'], ['city', 'City (if not the client’s)'], ['state', 'State'], ['father', 'Father’s or spouse’s name'], ['gender', 'Gender (Male / Female)'],
  ['divnd_bank_code', 'Bank MICR (CDSL dividend bank code)'], ['occupation', 'Occupation code'], ['annual_income_code', 'Annual income code'], ['nominee1_city', 'Nominee 1 city'], ['nominee1_pin', 'Nominee 1 PIN'],
  ['guardian1_address', 'Guardian of nominee 1: address'], ['guardian1_city', 'Guardian of nominee 1: city'], ['guardian1_pin', 'Guardian of nominee 1: PIN']];

function crFieldsForm(f) {
  return `<div class="form" style="grid-template-columns:repeat(2,1fr)">${CR_FIELDS.map(([k, l]) => `<div class="fld"><label for="crf-${k}">${l}</label><input class="in" id="crf-${k}" value="${esc((f || {})[k] || '')}"></div>`).join('')}</div>`;
}
const crFieldsRead = () => Object.fromEntries(CR_FIELDS.map(([k]) => [k, ($('crf-' + k)?.value || '').trim()]).filter(([, v]) => v));

function crBuyerRows(n) {
  return Array.from({ length: n }, (_, i) => `<div class="form" style="grid-template-columns:2fr 1fr 1fr 1.4fr;margin-bottom:4px"><input class="in mono" id="crb-id-${i}" placeholder="Buyer BO ID (16 digits, or IN… for NSDL)"><select class="in" id="crb-t-${i}"><option value="">Depository (from the ID)</option><option value="C">CDSL</option><option value="N">NSDL</option></select>
    <select class="in" id="crb-x-${i}"><option value="N">Has PAN</option><option value="Y">PAN exempt</option></select><input class="in mono" id="crb-p-${i}" placeholder="Buyer PAN (first holder)" maxlength="10"></div>`).join('');
}

Object.assign(A, {
  crOpen: async id => { try { wideModal(mhd('CDSL request ' + esc(id), '') + crDetail(await get('/v1/cdsl/requests/' + encodeURIComponent(id)))); } catch (e) { toast(e.message, true); } },
  crNew: async kind => {
    await clientCache();
    if (kind === 'Open account') {
      confirmModal('Open a demat account at CDSL', 'For an active client with no demat account. The client master (and the online application) give most details; add what is missing. A checker approves it, then it goes to CDSL’s BO online API.',
        `<div class="fld"><label for="crn-u-q">Client</label>${cpick('crn-u', '', {})}</div>${crFieldsForm({})}`, 'Raise', () => act(() => post('/v1/cdsl/requests', { kind, ucc: $('crn-u').value, fields: crFieldsRead() }), r => `${r.id}: ${r.status.toLowerCase()}`));
      return;
    }
    confirmModal('Add off-market beneficiaries', 'Up to 10 accounts the client may transfer to off-market. A checker approves; the client then confirms each on CDSL’s page with the OTP CDSL sends.',
      `<div class="fld"><label for="crn-u-q">Client (seller)</label>${cpick('crn-u', '', {})}</div><div id="crb">${crBuyerRows(3)}</div><p class="dim small">Leave unused rows empty.</p>`, 'Raise', () => {
        const buyers = [0, 1, 2].map(i => ({ bo_id: $('crb-id-' + i).value.trim(), type: $('crb-t-' + i).value, exempt: $('crb-x-' + i).value, pan: $('crb-p-' + i).value.trim() })).filter(b => b.bo_id);
        return act(() => post('/v1/cdsl/requests', { kind, ucc: $('crn-u').value, buyers }), r => `${r.id} raised for a checker`);
      });
  },
  crUpload: () => {
    const f = $('cr-up'); if (!f) return;
    f.onchange = async () => {
      if (!f.files.length) return;
      const file = f.files[0];
      try { const r = await api('POST', '/v1/cdsl/uploads?name=' + encodeURIComponent(file.name), await file.arrayBuffer(), true); toast(`${r.id}: waiting for a checker`); render({ keep: true }); } catch (e) { toast(e.message, true); }
      f.value = '';
    };
    f.click();
  },
  crAttach: id => {
    const f = $('cr-up'); if (!f) return;
    f.onchange = async () => {
      if (!f.files.length) return;
      const file = f.files[0];
      try { await api('POST', `/v1/cdsl/requests/${encodeURIComponent(id)}/file?name=` + encodeURIComponent(file.name), await file.arrayBuffer(), true); toast('File attached; waiting for a checker'); render({ keep: true }); } catch (e) { toast(e.message, true); }
      f.value = '';
    };
    f.click();
  },
  crFields: async id => {
    const r = await get('/v1/cdsl/requests/' + encodeURIComponent(id));
    confirmModal('Details for ' + esc(id), `${esc(r.ucc)} ${esc(r.name || '')}` + ((r.problems || []).length ? '' : ''),
      ((r.problems || []).length ? `<div class="errbox"><b>To fix</b><ul>${r.problems.map(p => `<li>${esc(p)}</li>`).join('')}</ul></div>` : '') + crFieldsForm(r.fields), 'Save', () => act(() => api('PUT', `/v1/cdsl/requests/${encodeURIComponent(id)}/fields`, { fields: crFieldsRead() }), x => x.problems?.length ? `${x.problems.length} thing(s) still to fix` : 'Ready for a checker'));
  },
  crAct: async (id, el) => {
    const a = el.dataset.a;
    const done = r => { if (r.link) { modal(mhd('Link for the client', 'Sent to the client by email and SMS. If the client is with you, they can open it now; it is shown only this once.') + `<div class="bd"><div class="secret mono small">${esc(r.link)}</div></div><div class="ft"><button class="btn pri" data-act="close">Done</button></div>`); render({ keep: true }); return; } };
    if (a === 'reject' || a === 'done' || a === 'cancel') {
      const r = C.cdslreq.requests.find(x => x.id === id) || {};
      const bo = a === 'done' && r.kind === 'Open account' ? '<div class="fld"><label for="cra-v">BO ID CDAS gave the account</label><input class="in mono" id="cra-v" maxlength="16" inputmode="numeric"></div>' : '';
      confirmModal({ reject: 'Reject ', done: 'Done in CDAS: ', cancel: 'Cancel ' }[a] + esc(id), esc(r.kind || ''), `${bo}<div class="fld"><label for="cra-n">${a === 'done' ? 'What was done (CDAS reference)' : 'Reason'}</label><input class="in" id="cra-n"></div>`, 'Confirm',
        () => act(() => post(`/v1/cdsl/requests/${encodeURIComponent(id)}/action`, { action: a, note: $('cra-n').value, value: $('cra-v')?.value || '' }), 'Recorded'), a !== 'done');
      return;
    }
    try { const r = await post(`/v1/cdsl/requests/${encodeURIComponent(id)}/action`, { action: a }); toast(`${id}: ${r.request.status.toLowerCase()}`); if (r.link) done(r); else render({ keep: true }); } catch (e) { toast(e.message, true); render({ keep: true }); }
  },
  crDefaults: async () => {
    const v = await get('/v1/cdsl/bo-defaults');
    confirmModal('BO defaults', 'CDSL’s product number and code values the DP uses for new accounts (from CDSL’s code list, as set up in CDAS).',
      `<div class="form" style="grid-template-columns:repeat(2,1fr)">${v.fields.map(f => `<div class="fld"><label for="crd-${f.key}">${esc(f.label)}</label><input class="in mono" id="crd-${f.key}" value="${esc(v.defaults.values[f.key] || '')}"></div>`).join('')}</div>`, 'Save',
      () => act(() => api('PUT', '/v1/cdsl/bo-defaults', { values: Object.fromEntries(v.fields.map(f => [f.key, $('crd-' + f.key).value.trim()])) }), 'BO defaults saved'));
  },
  crSetupFile: async () => {
    await clientCache();
    confirmModal('BO setup file', 'Makes CDSL’s BO setup upload file (08, fixed width) for an active client with no demat account, from the client master, the UCC details and the client’s CDSL details. A checker approves it, then it is uploaded to CDSL.',
      `<div class="fld"><label for="crs-u-q">Client</label>${cpick('crs-u', '', {})}</div>${crFieldsForm({})}`, 'Make the file', () => act(() => post('/v1/cdsl/requests', { kind: 'BO setup upload', ucc: $('crs-u').value, fields: crFieldsRead() }), r => r.problems?.length ? `${r.id}: ${r.problems.length} thing(s) to fix` : `${r.id}: ${r.file.name} made`));
  },
  crBOID: id => confirmModal('Record the BO ID', 'From CDSL’s response file for this BO setup upload.', '<div class="fld"><label for="crb-v">BO ID</label><input class="in mono" id="crb-v" maxlength="16" inputmode="numeric"></div>', 'Record',
    () => act(() => post(`/v1/cdsl/requests/${encodeURIComponent(id)}/action`, { action: 'boid', value: $('crb-v').value.trim() }), 'BO ID recorded')),
  crDetails: async () => {
    await clientCache();
    confirmModal('Client’s CDSL details', 'Choose the client.', `<div class="fld"><label for="crd-u-q">Client</label>${cpick('crd-u', '', {})}</div>`, 'Open', async () => {
      const ucc = $('crd-u').value, d = await get('/v1/cdsl/bo-details/' + encodeURIComponent(ucc));
      const h = i => (d.holders || [])[i] || {}, f = (id, l, v, x = '') => `<div class="fld"><label for="${id}">${l}</label><input class="in" id="${id}" value="${esc(v || '')}" ${x}></div>`;
      const codes = Object.entries(d.codes || {}).map(([k, v]) => `${k}=${v}`).join(', ');
      modal(mhd('CDSL details · ' + esc(ucc), 'What CDSL holds that the client master does not: joint holders, mode of operation, DDPI, and CDSL’s codes for this client.') + `<div class="bd">
        ${[0, 1].map(i => `<h4 style="margin:6px 0">${i ? 'Third' : 'Second'} holder</h4><div class="form" style="grid-template-columns:repeat(3,1fr)">${f('cdh-n' + i, 'Name', h(i).name)}${f('cdh-p' + i, 'PAN', h(i).pan, 'class="in mono"')}${f('cdh-f' + i, 'Father’s or spouse’s name', h(i).father)}${f('cdh-m' + i, 'Mobile', h(i).mobile)}${f('cdh-e' + i, 'Email', h(i).email)}<label class="chk"><input type="checkbox" id="cdh-v${i}" ${h(i).pan_verified ? 'checked' : ''}> PAN verified, Aadhaar-linked</label></div>`).join('')}
        <div class="form" style="grid-template-columns:repeat(3,1fr)">${f('cdd-mo', 'Mode of operation (0 sole, 1 jointly, 2 anyone or survivor)', d.mode_of_operation)}${f('cdd-cp', 'Communication (1 first holder, 2 all)', d.comm_pref)}<label class="chk"><input type="checkbox" id="cdd-pv" ${d.pan_verified ? 'checked' : ''}> First holder’s PAN verified, Aadhaar-linked</label>
        ${f('cdd-dr', 'DDPI registration number', d.ddpi_reg_no, 'class="in mono"')}${f('cdd-dd', 'DDPI date', d.ddpi_date, 'type="date"')}${f('cdd-dp', 'DDPI purposes (S P M T)', (d.ddpi_purposes || []).join(' '))}</div>
        ${f('cdd-codes', 'CDSL codes for this client (key=value, comma separated: occupation, geo, education, language, staff, staff_code, city_seq…)', codes)}<div id="cdd-err"></div></div>
        <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="cdd-save">Save</button></div>`);
      $('cdd-save').onclick = async () => {
        const holders = [0, 1].map(i => ({ name: $('cdh-n' + i).value.trim(), pan: $('cdh-p' + i).value.trim(), father: $('cdh-f' + i).value.trim(), mobile: $('cdh-m' + i).value.trim(), email: $('cdh-e' + i).value.trim(), pan_verified: $('cdh-v' + i).checked })).filter(x => x.name);
        const cm = Object.fromEntries($('cdd-codes').value.split(',').map(x => x.split('=').map(y => y.trim())).filter(x => x[0] && x[1]));
        try { await api('PUT', '/v1/cdsl/bo-details/' + encodeURIComponent(ucc), { ...d, holders, mode_of_operation: $('cdd-mo').value.trim(), comm_pref: $('cdd-cp').value.trim(), pan_verified: $('cdd-pv').checked,
          ddpi_reg_no: $('cdd-dr').value.trim(), ddpi_date: $('cdd-dd').value, ddpi_purposes: $('cdd-dp').value.split(/\s+/).filter(Boolean), codes: cm }); closeModal(); toast('CDSL details saved'); }
        catch (e) { $('cdd-err').innerHTML = errHTML(e); }
      };
    });
  },
  crDelHolder: async () => {
    await clientCache();
    confirmModal('Delete a deceased holder’s name', 'For a joint account, within 365 days of the death (CDSL communiqué 2023/404). The first holder is deleted by a BO setup record with the survivors in order; the second or third by a BO modify record. Older deaths go through transmission.',
      `<div class="fld"><label for="cdx-u-q">Client</label>${cpick('cdx-u', '', {})}</div><div class="form"><div class="fld"><label for="cdx-h">Holder</label><select class="in" id="cdx-h"><option value="1">First</option><option value="2">Second</option><option value="3">Third</option></select></div><div class="fld"><label for="cdx-d">Date of death (as on the certificate)</label><input class="in" type="date" id="cdx-d"></div></div>`,
      'Raise', () => act(() => post('/v1/cdsl/delete-holder', { ucc: $('cdx-u').value, holder: +$('cdx-h').value, died_on: $('cdx-d').value }), r => `${r.id}: ${r.status.toLowerCase()}`));
  },
  crCdasFiles: async () => {
    await clientCache();
    confirmModal('Files for CDAS', 'The BO signature upload (06) and BO closure upload (13) are uploaded in CDAS, not through the API.',
      `<div class="fld"><label for="cdf-u-q">Client (signature file)</label>${cpick('cdf-u', '', {})}</div><div class="fld"><label for="cdf-t">Signature</label><select class="in" id="cdf-t"><option value="S">Set up</option><option value="R">Replace</option><option value="D">Delete</option></select></div>
       <div class="fld"><label for="cdf-r">or a closure request (DPR-…)</label><input class="in mono" id="cdf-r"></div>`, 'Download', async () => {
        const r = $('cdf-r').value.trim();
        if (r) await download('/v1/cdsl/files/closure/' + encodeURIComponent(r), 'closure.txt');
        else await download(`/v1/cdsl/files/signature/${encodeURIComponent($('cdf-u').value)}?txn=${$('cdf-t').value}`, 'signature.zip');
        closeModal();
      });
  },
  crCount: async ucc => { try { await post('/v1/cdsl/demat-check/' + encodeURIComponent(ucc)); toast('Checked at CDSL'); cdslDematCheck(ucc); } catch (e) { toast(e.message, true); } },
});

// cdslDematCheck fills the client drawer's line on CDSL's count of active
// demat accounts (CDSL API §5.3).
async function cdslDematCheck(ucc) {
  const el = $('cdsl-dc'); if (!el) return;
  let x; try { x = await get('/v1/cdsl/demat-check/' + encodeURIComponent(ucc)); } catch (e) { el.innerHTML = ''; return; }
  const run = has('Ops maker', 'Ops checker', 'Compliance officer') ? ` <button class="btn sm" data-act="crCount" data-id="${esc(ucc)}">Check now</button>` : '';
  if (x.status === 'Not checked') { el.innerHTML = `<small class="dim">Active demat accounts at CDSL</small><div><span class="dim">Not checked yet</span>${run}</div>`; return; }
  const n = (v, w) => `<b class="${v > 1 ? 'neg' : ''}">${v}</b> ${w}`;
  el.innerHTML = `<small class="dim">Active demat accounts at CDSL</small><div>${x.status === 'Failed' ? `<span class="neg">${esc(x.error)}</span>` : `${n(x.pan_count, 'for the PAN')} · ${n(x.email_count, 'for the email')} · ${n(x.mobile_count, 'for the mobile')}`} <span class="dim small">${fmtDate((x.at || '').slice(0, 10))}</span>${run}${(x.notes || []).map(t => `<div class="small warn">${esc(t)}</div>`).join('')}</div>`;
}
