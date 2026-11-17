'use strict';
/* Ledgerline console: clients' FATCA/CRS self-certifications.

   - KYC & PMLA → FATCA / CRS: each client's declaration (tax residency with
     TINs, birth, nationality, US person, controlling persons, the account's
     classification), the reasonableness warnings to resolve, where it stands
     at the KRA, the legacy backlog (clients onboarded before 1 August 2024)
     with its bulk file, and the KRA files to mark uploaded.
   - The declaration form, used by the New client window, the KYC desk's
     change request (a checker approves it) and the client portal.

   Server: internal/ops/fatca.go and fatcacodes.go; internal/api/fatca.go.
   Names are prefixed fa. */

let FA_CODES = null;
Object.assign(UI, { faF: 'todo' });
const FA_MAX = 4;
const FA_KIND = 'FATCA/CRS', FA_DOC = 'FATCA/CRS self-certification';
const faCodes = () => FA_CODES || { countries: [['IN', 'India']], no_tin_reasons: {}, controlling_person_types: [], account_types: [['CRS1102', 'Custodial account']], account_statuses: { CRS1201: 'New account', CRS1202: 'Pre-existing account' }, max_countries: FA_MAX };
async function faLoadCodes() { if (!FA_CODES) FA_CODES = await get('/v1/fatca/codes'); return FA_CODES; }
const faCountry = c => (faCodes().countries.find(x => x[0] === c) || [c, c || '—'])[1];
const faOpts = (list, v, blank) => (blank !== undefined ? `<option value="">${esc(blank)}</option>` : '') + list.map(([k, l]) => `<option value="${esc(k)}" ${k === v ? 'selected' : ''}>${esc(l)}</option>`).join('');
const faCountryOpts = (v, blank) => faOpts(faCodes().countries, v, blank);
const faReasonOpts = v => faOpts(Object.entries(faCodes().no_tin_reasons).map(([k, l]) => [k, k + ': ' + l]), v || '', 'There is a TIN');
const faNonInd = typ => typ === 'HUF' || typ === 'Corporate';
const faDefault = typ => faNonInd(typ) ? { other_than_india: 'N', country_of_birth: 'IN', us_person: 'N', controlling_persons: [] } : { other_than_india: 'N', country_of_birth: 'IN', nationality: 'IN', us_person: 'N' };

// A line about a declaration (TINs come masked from the server).
function faSummary(d) {
  if (!d) return '<span class="dim">No self-certification</span>';
  const res = d.other_than_india === 'N' ? 'Tax resident of India only' : 'Tax resident of ' + ((d.residences || []).map(r => `${esc(faCountry(r.country))}${r.tin ? ` <span class="mono">${esc(r.tin)}</span>` : r.no_tin ? ` (no TIN: ${esc(r.no_tin)})` : ''}`).join(', ') || 'other countries');
  const bits = [res];
  if (d.us_person === 'Y') bits.push('US person');
  if (d.place_of_birth || d.country_of_birth) bits.push('born ' + esc([d.place_of_birth, faCountry(d.country_of_birth)].filter(Boolean).join(', ')));
  if ((d.controlling_persons || []).length) bits.push(`${d.controlling_persons.length} controlling person${d.controlling_persons.length > 1 ? 's' : ''}`);
  return bits.join(' · ');
}

/* ---------- the declaration form ---------- */
const faResRow = r => `<div class="fa-res">
  <div class="fld"><label>Country of tax residency</label><select class="in fa-c">${faCountryOpts(r.country || '', 'Choose')}</select></div>
  <div class="fld"><label>TIN in that country</label><input class="in mono fa-tin" value="${esc(r.tin || '')}" autocapitalize="characters"></div>
  <div class="fld"><label>Or, there is no TIN because</label><select class="in fa-nt">${faReasonOpts(r.no_tin)}</select></div>
  <div class="fld fa-why" ${r.no_tin === 'B' ? '' : 'hidden'}><label>Why it has not been obtained</label><input class="in fa-w" value="${esc(r.no_tin_why || '')}"></div>
  <div class="fld"><label>TIN issued by (only if another country)</label><select class="in fa-tc">${faCountryOpts(r.tin_country || '', 'The same country')}</select></div>
  <div class="fld fa-del"><button type="button" class="linkish small" data-fadel>Remove this country</button></div></div>`;
const faCPRow = c => { const r = (c.residences || [])[0] || {}; return `<div class="fa-cp" data-extra="${esc(JSON.stringify((c.residences || []).slice(1)))}">
  <div class="fld"><label>Full name</label><input class="in fa-cpn" value="${esc(c.name || '')}"></div>
  <div class="fld"><label>Role</label><select class="in fa-cpt">${faOpts(faCodes().controlling_person_types, c.type || '', 'Choose')}</select></div>
  <div class="fld"><label>PAN (optional)</label><input class="in mono fa-cpp" value="${esc(c.pan || '')}" maxlength="10" autocapitalize="characters"></div>
  <div class="fld"><label>Date of birth (optional)</label><input type="date" class="in fa-cpd" value="${esc(c.dob || '')}"></div>
  <div class="fld"><label>Country of birth</label><select class="in fa-cpcob">${faCountryOpts(c.country_of_birth || 'IN')}</select></div>
  <div class="fld"><label>Nationality</label><select class="in fa-cpnat">${faCountryOpts(c.nationality || 'IN')}</select></div>
  <div class="fld"><label>Tax resident of</label><select class="in fa-cpc">${faCountryOpts(c.other_than_india === 'Y' ? r.country || '' : '', 'India only')}</select></div>
  <div class="fld"><label>TIN there</label><input class="in mono fa-cptin" value="${esc(r.tin || '')}"></div>
  <div class="fld"><label>Or, there is no TIN because</label><select class="in fa-cpnt">${faReasonOpts(r.no_tin)}</select></div>
  <div class="fld fa-del"><button type="button" class="linkish small" data-facpdel>Remove this person</button></div></div>`; };

// faForm draws the declaration's fields into a grid with the id p.
// o.staff adds the date, validity and classification; o.self speaks as the client.
function faForm(p, d, typ, o = {}) {
  const ni = faNonInd(typ), other = d.other_than_india === 'Y', who = o.self ? 'I am' : ni ? 'The entity is' : 'The client is', c = faCodes();
  const res = (d.residences || []).length ? d.residences : [{}];
  return `<div class="form fa-form" id="${p}">
    <fieldset class="fld full"><legend>Tax residency</legend>
      <label class="check"><input type="radio" name="${p}-otr" value="N" ${other ? '' : 'checked'}> <span><b>${who} a tax resident of India only</b>${ni ? '' : ', and not a US person'}</span></label>
      <label class="check"><input type="radio" name="${p}-otr" value="Y" ${other ? 'checked' : ''}> <span>${who} a tax resident of another country too (or only)${ni ? '' : ', or a US citizen or green card holder'}</span></label></fieldset>
    <div class="full fa-other" ${other ? '' : 'hidden'}><div class="fa-list">${res.map(faResRow).join('')}</div>
      <button type="button" class="btn sm fa-add">Add a country</button> <span class="dim small">Up to ${c.max_countries || FA_MAX} countries other than India, each with its TIN or the reason there is none.</span></div>
    <div class="fld"><label>Place of ${ni ? 'incorporation' : 'birth'} (town or city)</label><input class="in fa-pob" value="${esc(d.place_of_birth || '')}"></div>
    <div class="fld"><label>Country of ${ni ? 'incorporation' : 'birth'}</label><select class="in fa-cob">${faCountryOpts(d.country_of_birth || 'IN')}</select></div>
    ${ni ? '' : `<div class="fld"><label>Nationality</label><select class="in fa-nat">${faCountryOpts(d.nationality || 'IN')}</select></div>`}
    <div class="fld"><label>US person (a US citizen or resident for tax)</label><select class="in fa-us"><option value="N">No</option><option value="Y" ${d.us_person === 'Y' ? 'selected' : ''}>Yes</option></select></div>
    ${o.staff ? `<div class="fld"><label>Date of the self-certification</label><input type="date" class="in fa-date" value="${esc(d.declared_on || S.date)}" max="${esc(S.date)}"></div>
      <div class="fld"><label>Validity</label><select class="in fa-valid"><option value="Y">Valid: signed, dated and complete</option><option value="N" ${d.self_cert === 'N' ? 'selected' : ''}>Not valid</option></select></div>
      <div class="fld"><label>The account is (Form 166)</label><select class="in fa-st">${faOpts(Object.entries(c.account_statuses || {}), d.account_status || '', 'Worked out from the opening date')}</select></div>
      <div class="fld"><label>Account type (Form 166)</label><select class="in fa-at">${faOpts(c.account_types || [], d.account_type || 'CRS1102')}</select></div>` : ''}
    ${ni ? `<div class="full"><b>Controlling persons</b> <span class="dim small">The natural persons who own or control the ${typ === 'HUF' ? 'HUF (the karta and members)' : 'entity'}, with their role, tax residency and TIN.</span><div class="fa-cps">${(d.controlling_persons || []).map(faCPRow).join('')}</div><button type="button" class="btn sm fa-cpadd">Add a controlling person</button></div>` : ''}
  </div>`;
}
function faRead(p) {
  const root = $(p); if (!root) return null;
  const v = (el, s) => (el.querySelector(s)?.value || '').trim();
  const other = root.querySelector(`input[name="${p}-otr"]:checked`)?.value || 'N';
  const d = { other_than_india: other, place_of_birth: v(root, '.fa-pob'), country_of_birth: v(root, '.fa-cob'), us_person: v(root, '.fa-us') || 'N' };
  if (root.querySelector('.fa-nat')) d.nationality = v(root, '.fa-nat');
  if (other === 'Y') d.residences = [...root.querySelectorAll('.fa-res')].map(r => ({ country: v(r, '.fa-c'), tin: v(r, '.fa-tin'), no_tin: v(r, '.fa-nt'), no_tin_why: v(r, '.fa-w'), tin_country: v(r, '.fa-tc') })).filter(r => r.country || r.tin || r.no_tin);
  if (root.querySelector('.fa-date')) Object.assign(d, { declared_on: v(root, '.fa-date'), self_cert: v(root, '.fa-valid'), account_status: v(root, '.fa-st'), account_type: v(root, '.fa-at') });
  const cps = [...root.querySelectorAll('.fa-cp')];
  if (cps.length) d.controlling_persons = cps.map(r => { const c = v(r, '.fa-cpc'), extra = JSON.parse(r.dataset.extra || '[]'); return { name: v(r, '.fa-cpn'), type: v(r, '.fa-cpt'), pan: v(r, '.fa-cpp'), dob: v(r, '.fa-cpd'), country_of_birth: v(r, '.fa-cpcob'), nationality: v(r, '.fa-cpnat'),
    other_than_india: c ? 'Y' : 'N', residences: c ? [{ country: c, tin: v(r, '.fa-cptin'), no_tin: v(r, '.fa-cpnt') }, ...extra] : [] }; }).filter(c => c.name || c.type);
  return d;
}
// faCheck finds what is plainly missing before the server checks the rest.
function faCheck(d, typ) {
  if (!d) return 'Answer the tax residency question';
  if (!d.place_of_birth) return faNonInd(typ) ? 'Give the place of incorporation' : 'Give the place of birth';
  if (d.other_than_india === 'Y') {
    if (!(d.residences || []).length) return 'Give each country other than India where the client is resident for tax';
    const r = d.residences.find(x => !x.country || (!x.tin && !x.no_tin) || (x.no_tin === 'B' && !x.no_tin_why));
    if (r) return !r.country ? 'Choose the country of tax residency' : r.no_tin === 'B' ? 'Say why the TIN has not been obtained' : `Give the TIN for ${faCountry(r.country)}, or the reason there is none`;
  }
  if (d.us_person === 'Y' && !(d.residences || []).some(r => r.country === 'US')) return 'A US person is resident in the United States for tax: add the United States with the US TIN';
  const cp = (d.controlling_persons || []).find(c => !c.name || !c.type);
  if (cp) return 'Give each controlling person’s name and role';
  return '';
}
function faBind(p, onChange) {
  const root = $(p); if (!root) return;
  const changed = () => { if (onChange) onChange(faRead(p)); };
  root.querySelectorAll(`input[name="${p}-otr"]`).forEach(x => { x.onchange = () => { root.querySelector('.fa-other').hidden = root.querySelector(`input[name="${p}-otr"]:checked`)?.value !== 'Y'; changed(); }; });
  const add = root.querySelector('.fa-add');
  if (add) add.onclick = () => { const l = root.querySelector('.fa-list'); if (l.children.length >= (faCodes().max_countries || FA_MAX)) { toast(`At most ${faCodes().max_countries || FA_MAX} countries`); return; } l.insertAdjacentHTML('beforeend', faResRow({})); changed(); };
  const cpadd = root.querySelector('.fa-cpadd');
  if (cpadd) cpadd.onclick = () => { root.querySelector('.fa-cps').insertAdjacentHTML('beforeend', faCPRow({})); changed(); };
  root.onclick = e => {
    if (e.target.matches('[data-fadel]')) { const l = root.querySelector('.fa-list'); if (l.children.length > 1) e.target.closest('.fa-res').remove(); else e.target.closest('.fa-res').outerHTML = faResRow({}); changed(); }
    if (e.target.matches('[data-facpdel]')) { e.target.closest('.fa-cp').remove(); changed(); }
  };
  root.onchange = e => { if (e.target.classList.contains('fa-nt')) e.target.closest('.fa-res').querySelector('.fa-why').hidden = e.target.value !== 'B'; changed(); };
  root.oninput = changed;
}

/* ---------- New client: the tax residency step ---------- */
function faObStep(d, E) {
  if (!FA_CODES) { faLoadCodes().then(() => { if (UI.ob && UI.ob.step === 3) obModal(); }).catch(e => toast(e.message, true)); return '<div class="dim">Loading the country list…</div>'; }
  d.fatca ||= faDefault(d.type);
  return `${faForm('ob-fa', d.fatca, d.type)}${E.fatca ? `<div class="err" style="margin-top:8px">${esc(E.fatca)}</div>` : ''}
    <div class="note" style="margin-top:10px">The client’s FATCA/CRS self-certification, from the signed account opening form. It is checked against the KYC details and goes to the KRA with the KYC upload (SEBI circular of 20 February 2024).</div>`;
}
const faObBind = d => faBind('ob-fa', v => { d.fatca = v; });
const faObCheck = d => faCheck(d.fatca, d.type);

/* ---------- KYC & PMLA → FATCA / CRS ---------- */
const FA_KRA = { none: 'nt', 'not-sent': 'warn', uploaded: 'acc', accepted: 'good', rejected: 'bad' };
function faTab(d) {
  const f = d.fatca;
  if (!f || f.error) return `<div class="bd">${errHTML({ message: f?.error || 'The FATCA/CRS tab could not be loaded.' })}</div>`;
  FA_CODES = f.codes;
  const n = k => f.counts[k] || 0, q = (UI.kycQ || '').trim().toLowerCase(), mk = has('Ops maker', 'Ops checker', 'Compliance officer'), ck = has('Ops checker', 'Compliance officer');
  const keep = { todo: r => !r.has || r.open > 0 || (r.has && !['accepted', 'uploaded'].includes(r.kra_state)) || (r.legacy && r.legacy_state !== 'Done'), warn: r => r.open > 0, kra: r => r.has && !['accepted', 'uploaded'].includes(r.kra_state), legacy: r => r.legacy, none: r => !r.has, all: () => true }[UI.faF] || (() => true);
  const rows = f.rows.filter(r => keep(r) && (!q || (r.ucc + ' ' + r.name).toLowerCase().includes(q)));
  const warnList = r => r.warnings.length ? r.warnings.map(w => `<div class="fa-warn ${w.resolved ? 'dim' : ''}">${pill(w.resolved ? 'Resolved' : 'Warning', w.resolved ? 'good' : 'warn')} ${esc(w.text)}${w.resolved ? `<div class="small">${esc(w.resolved.note)} · ${esc(w.resolved.by)}</div>` : w.resolvable && ck ? ` <button class="linkish small" data-act="faResolve" data-id="${esc(r.ucc)}" data-code="${esc(w.code)}">Resolve</button>` : ''}</div>`).join('') : '<span class="dim">None</span>';
  const legacy = UI.faF === 'legacy' ? `<div class="note" style="margin:10px 12px">Clients onboarded before 1 August 2024 (or migrated without an opening date). Their FATCA/CRS certifications were to be uploaded to the KRA as modifications (KFintech KRA: individuals by 31 October 2024; non-individuals as the KRA notifies). Collect the missing ones (or load them on Files with the FATCA-CRS file), then make the bulk file, upload it at the KRA and mark it uploaded below. ${mk ? '<button class="btn sm pri" data-act="faLegacy">Legacy bulk file</button>' : ''}</div>` : '';
  const batches = (f.batches || []).length ? `<div class="panel" style="margin:12px"><div class="hd"><h3>KRA files</h3><small>Mark each file uploaded once the KRA has taken it: its certifications then count as at the KRA until the KRA’s status file says more.</small></div>${tbl([['File'], ['Kind'], ['Made'], ['Clients', 'r'], ['Uploaded'], ['']], f.batches.map(b => `<tr><td class="mono small">${esc(b.name)}</td><td>${esc(b.kind)}${b.no_cert ? `<div class="small neg">${b.no_cert} without a self-certification</div>` : ''}</td><td class="n">${fmtDate(b.made.slice(0, 10))}<div class="dim small">${esc(b.by)}</div></td><td class="r">${b.clients || '—'}</td><td>${b.uploaded ? `${fmtDate(b.uploaded)} <span class="mono small">${esc(b.ref)}</span>` : pill('Not marked', 'warn')}</td><td><div class="split">${!b.uploaded && mk ? `<button class="btn sm" data-act="faUploaded" data-id="${esc(b.id)}">Mark uploaded</button>` : ''}<button class="btn sm" data-act="dl" data-id="/v1/compliance/kra-batches/${esc(b.id)}/controlling-persons" data-name="kra_fatca_controlling_persons_${esc(b.id)}.csv">Controlling persons file</button></div></td></tr>`))}</div>` : '';
  return `<div class="pad" style="padding-bottom:0"><div class="kpis">${kpi('No self-certification', n('No self-certification'), 'collect or load one', n('No self-certification') ? 'warn' : 'good')}${kpi('Warnings to resolve', n('Warnings to resolve'), 'against the KYC details', n('Warnings to resolve') ? 'warn' : 'good')}${kpi('Not at the KRA', n('Not at the KRA'), 'certifications to upload', n('Not at the KRA') ? 'warn' : 'good')}${kpi('Legacy to do', n('Legacy to do'), 'onboarded before Aug 2024', n('Legacy to do') ? 'bad' : 'good')}${kpi('Foreign tax residents', n('Foreign tax residents'), n('US persons') + ' US persons')}</div>
    ${segBtns('faF', [['todo', 'Needs action'], ['warn', 'Warnings'], ['kra', 'Not at the KRA'], ['legacy', 'Legacy (before Aug 2024)'], ['none', 'No self-certification'], ['all', 'All']], 'kyc-t')}</div>${legacy}
    ${tbl([['Client'], ['Self-certification'], ['Checks'], ['KRA'], ['']], rows.map(r => `<tr><td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}<div class="dim small">${esc(r.type)} · ${esc(r.status)}${r.opened ? ' · opened ' + fmtDate(r.opened) : ''}${r.legacy ? ' · legacy' : ''}</div></td>
      <td class="wrap small">${faSummary(r.decl)}${r.decl ? `<div class="dim">${esc(r.decl.mode || '')}${r.decl.declared_on ? ' · ' + fmtDate(r.decl.declared_on) : ''}${r.decl.self_cert === 'N' ? ' · ' + pill('Not valid', 'bad') : ''}${r.decl.account_status ? ' · ' + esc((f.codes.account_statuses || {})[r.decl.account_status] || r.decl.account_status) : ''}</div>` : ''}${r.legacy && r.legacy_state ? `<div class="small">${esc(r.legacy_state)}</div>` : ''}</td>
      <td class="wrap small fa-checks">${r.has ? warnList(r) : '<span class="dim">—</span>'}</td>
      <td class="small">${r.has ? pill(r.kra, FA_KRA[r.kra_state] || 'nt') : '<span class="dim">—</span>'}</td>
      <td>${r.pending ? `<button class="btn sm" data-act="go" data-id="changes">${esc(r.pending)} waiting</button>` : mk ? `<button class="btn sm" data-act="faEdit" data-id="${r.ucc}">${r.has ? 'Change' : 'Record'}</button>` : ''}</td></tr>`), { tall: true, empty: 'No clients here.' })}
    ${batches}
    <div class="pad"><span class="dim small">${esc(f.layout)} A change is a change request a checker approves; it then goes in the next KRA upload file as a modification.</span></div>`;
}

function faEditModal(ucc) {
  const r = C.kyc.fatca.rows.find(x => x.ucc === ucc); if (!r) return;
  const d = r.decl || faDefault(r.type);
  modal(mhd(`${r.has ? 'Change' : 'Record'} the FATCA/CRS self-certification · ${esc(r.name)}`, `${esc(r.ucc)} · ${esc(r.type)} · a checker who did not raise it approves it; it then goes to the KRA as a modification`) +
    `<form class="bd" id="fae">${faForm('fa-e', d, r.type, { staff: true })}
      <div class="form" style="margin-top:12px"><div class="fld full"><label for="fa-doc">The client’s signed and dated self-certification · PDF, JPEG or PNG</label><input class="in" type="file" id="fa-doc" accept="application/pdf,image/jpeg,image/png" required></div>
      ${r.has ? '<div class="note full">TINs and PANs on record are shown masked: leave them as they are to keep them, or type the new one.</div>' : ''}</div>
      <div id="fa-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Submit for approval</button></div></form>`);
  faBind('fa-e');
  $('fae').onsubmit = async e => {
    e.preventDefault(); const decl = faRead('fa-e'), m = faCheck(decl, r.type);
    if (m) { $('fa-err').innerHTML = errHTML(new Error(m)); return; }
    try {
      const file = $('fa-doc').files[0]; const doc = await api('POST', `/v1/clients/${encodeURIComponent(ucc)}/documents?kind=${encodeURIComponent(FA_DOC)}&name=${encodeURIComponent(file.name)}`, file, true);
      const res = await post('/v1/changes', { ucc, kind: FA_KIND, values: { fatca: JSON.stringify(decl), mode: 'Signed form' }, doc: doc.id });
      closeModal(); toast(`Request ${res.id} submitted for approval`); render({ keep: true });
    } catch (err) { $('fa-err').innerHTML = errHTML(err); }
  };
}

Object.assign(A, {
  faEdit: id => faLoadCodes().then(() => faEditModal(id)).catch(e => toast(e.message, true)),
  faResolve: (id, el) => confirmModal('Resolve a warning · ' + esc(id), esc(el.closest('.fa-warn')?.textContent.replace('Warning', '').replace('Resolve', '').trim() || ''),
    '<div class="fld"><label for="fa-rn">What was checked, and what it showed</label><textarea class="in" id="fa-rn" placeholder="e.g. Client confirmed by signed letter: working abroad on a short assignment, tax resident of India only; passport and letter on file"></textarea></div>',
    'Record', () => act(() => post(`/v1/fatca/${encodeURIComponent(id)}/resolve`, { code: el.dataset.code, note: $('fa-rn').value }), 'Recorded')),
  faUploaded: id => confirmModal('Mark uploaded · ' + esc(id), 'Record that this file was uploaded at the KRA, with the KRA’s reference. Certifications recorded after the file was made are not marked: they go in a later file.',
    `<div class="form"><div class="fld"><label for="fa-ud">Uploaded on</label><input class="in" type="date" id="fa-ud" value="${esc(S.date)}" max="${esc(S.date)}"></div><div class="fld"><label for="fa-ur">KRA reference (batch or acknowledgement number)</label><input class="in mono" id="fa-ur"></div></div>`,
    'Mark uploaded', () => act(() => post(`/v1/compliance/kra-batches/${encodeURIComponent(id)}/uploaded`, { date: $('fa-ud').value, ref: $('fa-ur').value }), b => `${b.id} marked uploaded`)),
  faLegacy: async () => { await download('/v1/compliance/fatca-legacy', 'kra_fatca_legacy.csv'); render({ keep: true }); },
  // The client portal: the client reports a change of tax residency.
  pFatca: () => {
    const v = C[S.view], a = v.account; if (v.fatca_codes) FA_CODES = v.fatca_codes;
    const d = a.fatca || faDefault(a.type);
    modal(mhd('Your tax residency (FATCA/CRS)', 'Tell us whenever it changes. Head office checks it before it takes effect, and it is reported to the KYC registration agency.') +
      `<form class="bd" id="paf">${faForm('fa-p', d, a.type, { self: true })}
        ${a.fatca ? '<div class="note" style="margin-top:10px">Tax numbers we hold are shown masked: leave them as they are to keep them.</div>' : ''}
        <label class="check" style="margin-top:10px"><input type="checkbox" id="fa-pd"> <span>This declaration is true and complete. I will tell you within 30 days if it changes.</span></label>
        <div id="fa-perr"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Submit</button></div></form>`);
    faBind('fa-p');
    $('paf').onsubmit = async e => {
      e.preventDefault(); const decl = faRead('fa-p'), m = !$('fa-pd').checked ? 'Tick the declaration to submit it' : faCheck(decl, a.type);
      if (m) { $('fa-perr').innerHTML = errHTML(new Error(m)); return; }
      try { const r = await post('/v1/changes', { ucc: a.ucc, kind: FA_KIND, values: { fatca: JSON.stringify(decl) } }); closeModal(); toast(`Request ${r.id} submitted`); render({ keep: true }); }
      catch (err) { $('fa-perr').innerHTML = errHTML(err); }
    };
  },
  // Bank reconciliation: a research or advisory fee that came into the client bank account.
  bankMoveFee: id => confirmModal('Move the fee to the firm’s account', 'A research or advisory fee is the firm’s money, not client money: transfer it from the client bank account to the firm’s own account, then record the transfer here.',
    '<div class="fld"><label for="mf-r">Transfer reference (UTR)</label><input class="in mono" id="mf-r"></div>', 'Record the transfer', () => act(() => post(`/v1/bank/${encodeURIComponent(id)}/move-fee`, { ref: $('mf-r').value }), 'Recorded')),
});
