'use strict';
/* Ledgerline console: inspection packs (screen "inspack", under
   Compliance). The compliance officer picks the inspection's data request
   (a template), the period and the sample of clients, reviews the items
   and builds the pack: one ZIP with a cover sheet, an index and the files,
   built in the background with its progress shown. Only the compliance
   officer and Admin see this screen; every make and download is in the
   audit trail. Loaded after app.js. */

Object.assign(UI, { ipTab: 'packs', ipWiz: null });

// Only the compliance officer and Admin make or download packs.
const ipCan = () => has('Compliance officer');
{
  const allowed = viewAllowed;
  viewAllowed = k => allowed(k) && (k !== 'inspack' || ipCan());
}
try { if (location.hash.slice(1) === 'inspack') S.view = 'inspack'; } catch (e) {}

const IP_ST = { Queued: 'nt', Building: 'acc', Ready: 'good', Failed: 'bad', Interrupted: 'bad' };
const ipStamp = t => { const d = new Date(t); return isNaN(d) ? esc(t || '') : d.toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'short' }); };
const ipBytes = n => n >= 1 << 20 ? (n / (1 << 20)).toFixed(1) + ' MB' : n >= 1024 ? Math.round(n / 1024) + ' KB' : (n || 0) + ' bytes';
const ipSampleWords = s => s.mode === 'random' ? `${s.clients.length} at random · seed <span class="mono">${esc(s.seed)}</span>` : s.mode === 'list' ? `${s.clients.length} named by the inspector` : 'No sample';
const ipPeriodLabel = id => ({ period: 'The inspection period', end: 'As at the period end', current: 'The current version' }[id] || id);
const ipSecName = id => (C.inspack?.sections || []).find(s => s.id === id)?.name || id;
// Why an item cannot be produced, from the readiness check.
const ipWhy = (it, ready) => (ready || C.inspack?.ready || {})[it.section + '|' + (it.param || '')] || '';
function ipItemState(it, ready) {
  const why = ipWhy(it, ready);
  return (why ? `${pill('Not available', 'bad')} <span class="small">${esc(why)}</span>` : pill('From Ledgerline', 'good')) +
    (it.to_confirm ? `<div class="small">${pill('To confirm', 'warn')} ${esc(it.to_confirm)}</div>` : '');
}

V.inspack = {
  load: () => ipCan() ? get('/v1/inspection-packs') : Promise.resolve({ denied: true }),
  html: d => {
    if (d.denied) return ph('Inspection packs', '') + '<div class="note warn">Only the compliance officer and Admin make and download inspection packs, because a pack holds full client data.</div>';
    const ps = d.packs, live = ps.filter(p => !p.expired);
    const busy = live.filter(p => p.status === 'Queued' || p.status === 'Building').length;
    ipPoll(busy > 0);
    return ph('Inspection packs', 'When an exchange, depository or SEBI inspection (or the internal auditor) sends its data request, choose the inspection, the period and the sample, and Ledgerline puts the records into one pack: a ZIP with a cover sheet, an index and every file, each carrying “Prepared for …”.',
      '<button class="btn pri" data-act="ipNew">New pack</button>') +
      `<div class="kpis">${kpi('Ready to download', nf(live.filter(p => p.status === 'Ready').length), `kept ${d.settings.keep_days} days`, 'good')}${kpi('Being built', nf(busy), busy ? 'progress below' : '', busy ? 'warn' : '')}${kpi('Expired', nf(ps.filter(p => p.expired).length), 'record kept, files not served')}${kpi('Documents on file', nf(d.documents.filter(x => !x.retired).length), 'policies, audit reports, evidence')}</div>` +
      `<div class="panel"><div class="hd">${segBtns('ipTab', [['packs', `Packs · ${ps.length}`], ['tpl', `Templates · ${d.templates.length}`], ['docs', 'Documents']])}</div></div>` +
      ({ packs: ipPacks, tpl: ipTemplates, docs: ipDocs }[UI.ipTab] || ipPacks)(d);
  }
};

// While a pack is being built, its progress is fetched every 1.5 seconds.
let ipTimer = null;
function ipPoll(on) {
  clearTimeout(ipTimer); ipTimer = null;
  if (!on) return;
  ipTimer = setTimeout(async () => {
    if (S.view !== 'inspack' || UI.ipTab !== 'packs' || document.hidden) return;
    try { C.inspack = await get('/v1/inspection-packs'); } catch (e) { return; }
    if (S.view !== 'inspack') return;
    if ($('ip-packs')) rerenderPart('ip-packs');
    ipPoll(C.inspack.packs.some(p => !p.expired && (p.status === 'Queued' || p.status === 'Building')));
  }, 1500);
}

function ipPacks(d) {
  return `<div class="panel"><div class="hd"><h3>Packs</h3><small>Newest first. A pack holds full client data: only the compliance officer and Admin can download it, and every download is in the audit trail.</small>
      <div class="tools"><button class="btn sm" data-act="ipKeep">Keep packs ${d.settings.keep_days} days</button></div></div>
    <div id="ip-packs">${P['ip-packs'](d)}</div></div>`;
}
P['ip-packs'] = d => tbl([['Pack'], ['Period'], ['Sample'], ['State'], ['Prepared'], ['On the list until'], ['']], d.packs.map(ipPackRow),
  { empty: 'No packs yet. Choose New pack when a data request comes in.' });

function ipPackRow(p) {
  const prog = p.progress || {}, frac = prog.total ? prog.done / prog.total : 0;
  const na = (p.results || []).filter(r => r.status !== 'Included').length;
  let st = pill(p.status, IP_ST[p.status] || 'nt');
  if (p.status === 'Queued' || p.status === 'Building') st += `<div class="ip-prog" role="progressbar" aria-valuemin="0" aria-valuemax="${prog.total}" aria-valuenow="${prog.done}" aria-label="Items built">${bar(frac)}<span class="small">${prog.done} of ${prog.total}${prog.current ? ' · ' + esc(prog.current) : ''}</span></div>`;
  if (p.status === 'Ready') st += `<div class="small">${nf(p.files)} files · ${ipBytes(p.size)}${na ? ` · <span class="neg">${na} not available</span>` : ''}</div>`;
  if (p.error) st += `<div class="small neg wrap">${esc(p.error)}</div>`;
  if (p.expired) st = pill('Expired', 'nt') + `<div class="small dim">${fmtDate(p.expired_at)} · ${esc(p.expired_why)}</div>`;
  const ready = p.status === 'Ready' && !p.expired;
  return `<tr><td><b>${esc(p.title)}</b>${p.mask ? ' ' + pill('Masked copy', 'acc') : ''}<div class="dim small"><span class="mono">${esc(p.id)}</span>${p.ref ? ' · ' + esc(p.ref) : ''}${p.template_name ? ' · ' + esc(p.template_name) : ''}${p.copy_of ? ' · copy of ' + esc(p.copy_of) : ''}</div></td>
    <td class="nowrap">${fmtDate(p.from)} to ${fmtDate(p.to)}</td><td class="small">${ipSampleWords(p.sample)}</td><td style="min-width:180px">${st}</td>
    <td class="small">${esc(p.by_name)}<div class="dim">${ipStamp(p.at)}</div></td><td class="small nowrap">${p.expired ? '—' : fmtDate(p.expires_at)}</td>
    <td><div class="ip-acts">${ready ? `<button class="btn sm pri" data-act="ipDownload" data-id="${esc(p.id)}">Download</button><button class="btn sm" data-act="ipIndex" data-id="${esc(p.id)}">Open the index</button>${p.mask ? '' : `<button class="btn sm" data-act="ipMask" data-id="${esc(p.id)}">Masked copy</button>`}` : ''}
      <button class="btn sm" data-act="ipAudit" data-id="${esc(p.id)}">Who opened it</button>${ready ? `<button class="btn sm bad" data-act="ipExpire" data-id="${esc(p.id)}">Remove</button>` : ''}</div></td></tr>`;
}

function ipTemplates(d) {
  return d.templates.map(t => `<div class="panel ip-tplp"><div class="hd"><div><h3>${esc(t.name)}</h3><small>${t.standard ? 'Standard template' : 'The firm’s template'} · ${esc(t.authority || '')} · ${t.items.length} items${t.sample_n ? ` · sample of ${t.sample_n} clients` : ''}</small></div>
      <div class="tools"><button class="btn sm pri" data-act="ipNew" data-id="${esc(t.id)}">Make a pack</button><button class="btn sm" data-act="ipTplEdit" data-id="${esc(t.id)}">${t.standard ? 'Copy as the firm’s template' : 'Edit'}</button>${t.standard ? '' : `<button class="btn sm bad" data-act="ipTplDel" data-id="${esc(t.id)}">Delete</button>`}</div></div>
    <div class="bd">${t.about ? `<p class="ip-about">${esc(t.about)}</p>` : ''}${t.sources?.length ? `<p class="dim small">Sources: ${t.sources.map(esc).join(' · ')}</p>` : ''}
    ${tbl([['No.'], ['Item'], ['Period'], ['Produced by'], ['Now']], t.items.map((it, i) => `<tr><td>${i + 1}</td><td><b>${esc(it.title)}</b>${it.sampled ? ' <span class="dim small">· sampled clients</span>' : ''}${it.basis ? `<div class="dim small">${esc(it.basis)}</div>` : ''}</td><td class="small">${ipPeriodLabel(it.period)}</td><td class="small">${esc(ipSecName(it.section))}${it.param ? `<div class="dim">${esc(it.param)}</div>` : ''}</td><td class="small" style="min-width:200px">${ipItemState(it)}</td></tr>`))}</div></div>`).join('');
}

function ipDocs(d) {
  const kinds = d.doc_kinds.map(k => `<option>${esc(k)}</option>`).join('');
  return `<div class="panel"><div class="hd"><h3>Add a document</h3><small>Policies, board minutes, CSCRF, VAPT and audit reports, net worth certificates: a pack takes the current versions of the kinds its items name.</small></div>
    <form class="bd" id="ip-docf"><div class="form">
      <div class="fld"><label for="ipd-kind">Kind</label><select class="in" id="ipd-kind">${kinds}</select></div>
      <div class="fld"><label for="ipd-title">Title</label><input class="in" id="ipd-title" placeholder="e.g. PMLA policy" required></div>
      <div class="fld"><label for="ipd-ver">Version or date <span class="dim">(optional)</span></label><input class="in" id="ipd-ver" placeholder="e.g. v4, approved 14-Apr-2026"></div>
      <div class="fld"><label for="ipd-file">File (PDF, picture, Word or Excel, up to 20 MB)</label><input class="in" type="file" id="ipd-file" required></div>
    </div><div id="ipd-err"></div><div class="tools"><button class="btn pri" type="submit">Add document</button></div></form></div>
    <div class="panel"><div class="hd"><h3>Documents on file</h3><small>A retired document stays on record but goes into no new pack.</small></div>
    ${tbl([['Document'], ['Kind'], ['Version'], ['Added'], ['SHA-256'], ['']], d.documents.map(x => `<tr class="${x.retired ? 'ip-retired' : ''}"><td><b>${esc(x.title)}</b><div class="dim small">${esc(x.name)} · ${ipBytes(x.size)}</div></td><td>${esc(x.kind)}</td><td class="small">${esc(x.version || '—')}</td>
      <td class="small">${esc(x.by)}<div class="dim">${ipStamp(x.at)}</div></td><td class="mono small ip-hash">${esc(x.sha256.slice(0, 16))}…</td>
      <td><div class="ip-acts"><button class="btn sm" data-act="dl" data-id="/v1/inspection-docs/${esc(x.id)}" data-name="${esc(x.name)}">Download</button>${x.retired ? `<span class="dim small">Retired ${fmtDate(x.retired_at)}: ${esc(x.retired_why)}</span>` : `<button class="btn sm" data-act="ipDocRetire" data-id="${esc(x.id)}">Retire</button>`}</div></td></tr>`),
      { empty: 'No documents yet.' })}</div>`;
}

document.addEventListener('submit', async e => {
  if (e.target.id !== 'ip-docf') return;
  e.preventDefault();
  const f = $('ipd-file').files[0];
  if (!f) { $('ipd-err').innerHTML = errHTML(new Error('Choose the file')); return; }
  const q = new URLSearchParams({ kind: $('ipd-kind').value, title: $('ipd-title').value.trim(), version: $('ipd-ver').value.trim(), name: f.name });
  try { await api('POST', '/v1/inspection-docs?' + q, f, true); toast('Document added'); render({ keep: true }); }
  catch (err) { $('ipd-err').innerHTML = errHTML(err); }
});

/* ---------- the new-pack wizard: template → period → sample → review items → build ---------- */
const IP_STEPS = ['Template', 'Period', 'Sample', 'Review items', 'Build'];

// Default periods: the half-year, financial year, quarter or month just ended.
function ipPreset(kind, today) {
  const [y, m] = today.split('-').map(Number), z = n => String(n).padStart(2, '0');
  const end = (yy, mm) => { const d = new Date(Date.UTC(yy, mm, 0)); return d.toISOString().slice(0, 10); };
  if (kind === 'half') return m >= 10 ? [`${y}-04-01`, `${y}-09-30`] : m >= 4 ? [`${y - 1}-10-01`, `${y}-03-31`] : [`${y - 1}-04-01`, `${y - 1}-09-30`];
  if (kind === 'fy') return m >= 4 ? [`${y - 1}-04-01`, `${y}-03-31`] : [`${y - 2}-04-01`, `${y - 1}-03-31`];
  if (kind === 'quarter') { const q = Math.floor((m - 1) / 3), qy = q === 0 ? y - 1 : y, qs = q === 0 ? 10 : (q - 1) * 3 + 1; return [`${qy}-${z(qs)}-01`, end(qy, qs + 2)]; }
  const py = m === 1 ? y - 1 : y, pm = m === 1 ? 12 : m - 1; return [`${py}-${z(pm)}-01`, end(py, pm)];
}

function ipWizOpen(tplId) {
  const d = C.inspack, t = d.templates.find(x => x.id === tplId) || d.templates[0];
  const [from, to] = ipPreset(t.kind === 'internal_audit' ? 'half' : 'fy', d.today);
  UI.ipWiz = { step: tplId ? 2 : 1, tpl: t.id, title: t.name, ref: '', from, to, mode: t.sample_n ? 'random' : 'none', n: t.sample_n || 25, seed: '', given: '', sample: null, items: t.items.map(x => ({ ...x })), mask: false, ready: null, err: '' };
  ipWizShow();
}

function ipWizShow() {
  const w = UI.ipWiz;
  const steps = `<ol class="ip-steps" aria-label="Steps">${IP_STEPS.map((s, i) => `<li class="${i + 1 === w.step ? 'on' : i + 1 < w.step ? 'done' : ''}" ${i + 1 === w.step ? 'aria-current="step"' : ''}><span>${i + 1}</span>${s}</li>`).join('')}</ol>`;
  const body = [ipStepTemplate, ipStepPeriod, ipStepSample, ipStepItems, ipStepBuild][w.step - 1]();
  const back = w.step > 1 ? '<button class="btn" data-act="ipBack">Back</button>' : '';
  const next = w.step < 5 ? '<button class="btn pri" data-act="ipNext">Next</button>' : '<button class="btn pri" data-act="ipBuild" id="ip-build">Build the pack</button>';
  wideModal(mhd('New inspection pack', 'The pack is built in the background; you can close this screen while it runs.') + `<div class="bd ip-wiz">${steps}${body}<div id="ip-err">${w.err ? errHTML({ message: w.err }) : ''}</div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><span style="flex:1"></span>${back}${next}</div>`);
}

function ipStepTemplate() {
  const w = UI.ipWiz, d = C.inspack;
  return `<fieldset class="ip-tpls"><legend>Which inspection is it?</legend>${d.templates.map(t => `<label class="ip-tpl ${w.tpl === t.id ? 'on' : ''}"><input type="radio" name="ip-tpl" value="${esc(t.id)}" ${w.tpl === t.id ? 'checked' : ''}>
    <span><b>${esc(t.name)}</b><small>${esc(t.authority || '')}</small><small>${t.items.length} items${t.sample_n ? ` · sample of ${t.sample_n}` : ''}${t.standard ? '' : ' · the firm’s own'}</small></span></label>`).join('')}</fieldset>`;
}

function ipStepPeriod() {
  const w = UI.ipWiz;
  return `<div class="form">
    <div class="fld full"><label for="ip-title">Inspection (shown on every file: “Prepared for …”)</label><input class="in" id="ip-title" value="${esc(w.title)}" maxlength="120"></div>
    <div class="fld"><label for="ip-ref">Reference of the request <span class="dim">(optional)</span></label><input class="in" id="ip-ref" value="${esc(w.ref)}" maxlength="80" placeholder="e.g. the inspection letter’s number"></div><div></div>
    <div class="fld"><label for="ip-from">From</label><input class="in" type="date" id="ip-from" value="${esc(w.from)}" max="${esc(C.inspack.today)}"></div>
    <div class="fld"><label for="ip-to">To</label><input class="in" type="date" id="ip-to" value="${esc(w.to)}" max="${esc(C.inspack.today)}"></div>
    <div class="full tools"><span class="dim small">The period just ended:</span>${[['half', 'Half-year'], ['fy', 'Financial year'], ['quarter', 'Quarter'], ['month', 'Month']].map(([k, l]) => `<button class="btn sm" data-act="ipPreset" data-id="${k}">${l}</button>`).join('')}</div></div>`;
}

function ipStepSample() {
  const w = UI.ipWiz, s = w.sample;
  const opt = (v, l, sub) => `<label class="ip-opt ${w.mode === v ? 'on' : ''}"><input type="radio" name="ip-mode" value="${v}" ${w.mode === v ? 'checked' : ''}><span><b>${l}</b><small>${sub}</small></span></label>`;
  let detail = '';
  if (w.mode === 'random') detail = `<div class="form"><div class="fld"><label for="ip-n">How many clients</label><input class="in" type="number" id="ip-n" min="1" max="500" value="${esc(w.n)}"></div>
      <div class="fld"><label for="ip-seed">Seed <span class="dim">(blank: a new one)</span></label><input class="in mono" id="ip-seed" inputmode="numeric" value="${esc(w.seed)}" placeholder="e.g. 482913307715"></div></div>
      <div class="tools"><button class="btn" data-act="ipDraw">Draw the sample</button><span class="dim small">The seed is recorded on the cover sheet: the same seed and period draw the same clients again.</span></div>`;
  if (w.mode === 'list') detail = `<div class="fld"><label for="ip-given">Client codes the inspector gave (any separator)</label><textarea class="in mono" id="ip-given" rows="4">${esc(w.given)}</textarea></div><div class="tools"><button class="btn" data-act="ipDraw">Check the list</button></div>`;
  const shown = s && w.mode !== 'none' ? `<div class="note good ip-sample"><b>${s.clients.length} clients</b>${s.mode === 'random' ? ` drawn from ${nf(s.population)} with seed <span class="mono">${esc(s.seed)}</span>` : ''}<div class="mono small">${s.clients.map(esc).join(' · ')}</div></div>` : '';
  return `<fieldset class="ip-opts"><legend>Which clients?</legend>${opt('random', 'At random', 'N clients whose account was open by the period end, drawn with a recorded seed')}${opt('list', 'The inspector’s list', 'The client codes the request names')}${opt('none', 'No sample', 'Items for sampled clients are taken off')}</fieldset>${detail}${shown}`;
}

function ipStepItems() {
  const w = UI.ipWiz, d = C.inspack;
  const secs = d.sections.map(s => `<option value="${esc(s.id)}">${esc(s.name)}</option>`).join('');
  const per = d.periods.map(p => `<option value="${esc(p.id)}">${esc(p.label)}</option>`).join('');
  const sel = (html, v) => html.replace(`value="${esc(v)}"`, `value="${esc(v)}" selected`);
  const counts = w.items.reduce((a, it) => { ipWhy(it, w.ready) ? a.na++ : a.ok++; if (it.to_confirm) a.tc++; return a; }, { ok: 0, na: 0, tc: 0 });
  return `<div class="tools"><span>${pill(counts.ok + ' from Ledgerline', 'good')} ${pill(counts.na + ' not available', counts.na ? 'bad' : 'nt')} ${pill(counts.tc + ' to confirm', counts.tc ? 'warn' : 'nt')}</span><span style="flex:1"></span><button class="btn sm" data-act="ipItemAdd">Add an item</button></div>
    <div class="ip-items">${w.items.map((it, i) => `<div class="ip-item" data-i="${i}"><div class="ip-no">${i + 1}</div>
      <div class="ip-main"><label class="sr" for="ipi-t-${i}">Item ${i + 1}</label><input class="in" id="ipi-t-${i}" data-ipi="title" value="${esc(it.title)}">
        <div class="ip-row"><div class="fld"><label for="ipi-p-${i}">Period</label><select class="in" id="ipi-p-${i}" data-ipi="period">${sel(per, it.period)}</select></div>
        <div class="fld ip-sec"><label for="ipi-s-${i}">Produced by</label><select class="in" id="ipi-s-${i}" data-ipi="section">${sel(secs, it.section)}</select></div>
        ${['documents', 'complaints', 'not_held'].includes(it.section) ? `<div class="fld"><label for="ipi-x-${i}">${it.section === 'documents' ? 'Kinds of document' : it.section === 'complaints' ? 'Only (dp: demat account)' : 'Where it is kept'}</label><input class="in" id="ipi-x-${i}" data-ipi="${it.section === 'not_held' ? 'note' : 'param'}" value="${esc(it.section === 'not_held' ? it.note || '' : it.param || '')}"></div>` : ''}
        <label class="check ip-chk"><input type="checkbox" data-ipi="sampled" ${it.sampled ? 'checked' : ''}> Sampled clients only</label></div>
        <div class="ip-state">${ipItemState(it, w.ready)}</div></div>
      <button class="btn sm" data-act="ipItemDel" data-id="${i}" aria-label="Take item ${i + 1} off">Take off</button></div>`).join('')}</div>
    <label class="check"><input type="checkbox" id="ip-mask" ${w.mask ? 'checked' : ''}> <span><b>Mask personal details</b> · a copy for outside advisers: client names, codes, PANs, phones, emails and account numbers become tokens, and KYC images are left out</span></label>
    <div class="tools"><label class="sr" for="ip-saveas">Template name</label><input class="in" id="ip-saveas" placeholder="Save these items as the firm’s template: name" style="flex:1;min-width:200px"><button class="btn sm" data-act="ipSaveTpl">Save as a template</button></div>`;
}

function ipStepBuild() {
  const w = UI.ipWiz, d = C.inspack, t = d.templates.find(x => x.id === w.tpl);
  const na = w.items.filter(it => ipWhy(it, w.ready)).length, tc = w.items.filter(it => it.to_confirm).length;
  const me = (S.me?.users || []).find(u => u.id === S.me?.actor)?.name || S.me?.actor || '';
  const wm = `Prepared for ${w.title}${w.ref ? ` (${w.ref})` : ''}, ${fmtDate(d.today)}, by ${me}${w.mask ? ' · masked copy for outside advisers' : ''}`;
  const s = w.sample;
  return `<div class="ip-sum"><div><small>Inspection</small><b>${esc(w.title)}</b>${w.ref ? `<span>${esc(w.ref)}</span>` : ''}</div><div><small>Template</small><b>${esc(t?.name || '—')}</b></div>
    <div><small>Period</small><b>${fmtDate(w.from)} to ${fmtDate(w.to)}</b></div><div><small>Sample</small><b>${w.mode === 'none' || !s ? 'No sample' : ipSampleWords(s)}</b></div>
    <div><small>Items</small><b>${w.items.length}</b><span>${na} not available · ${tc} to confirm</span></div><div><small>Copy</small><b>${w.mask ? 'Masked, for outside advisers' : 'Full client data, for the inspector'}</b></div></div>
    <div class="note">Every file Ledgerline writes will carry: <b>${esc(wm)}</b></div>
    <p class="dim small">Making the pack is recorded in the audit trail. It stays on the download list for ${d.settings.keep_days} days; the record of it stays after that.</p>`;
}

// Read the open step's fields into the wizard state.
function ipWizRead() {
  const w = UI.ipWiz, v = id => $(id)?.value;
  if (w.step === 1) { const c = document.querySelector('input[name=ip-tpl]:checked'); if (c && c.value !== w.tpl) { const t = C.inspack.templates.find(x => x.id === c.value); Object.assign(w, { tpl: t.id, title: t.name, items: t.items.map(x => ({ ...x })), mode: t.sample_n ? 'random' : 'none', n: t.sample_n || w.n, sample: null, ready: null }); if (t.kind === 'internal_audit') [w.from, w.to] = ipPreset('half', C.inspack.today); } }
  if (w.step === 2) { w.title = v('ip-title').trim(); w.ref = v('ip-ref').trim(); if (v('ip-from') !== w.from || v('ip-to') !== w.to) w.sample = null; w.from = v('ip-from'); w.to = v('ip-to'); }
  if (w.step === 3) { const m = document.querySelector('input[name=ip-mode]:checked')?.value || w.mode; if (m !== w.mode) w.sample = null; w.mode = m; if ($('ip-n')) { if (+v('ip-n') !== +w.n || v('ip-seed').trim() !== String(w.seed)) w.sample = null; w.n = v('ip-n'); w.seed = v('ip-seed').trim(); } if ($('ip-given')) { if (v('ip-given') !== w.given) w.sample = null; w.given = v('ip-given'); } }
  if (w.step === 4) {
    document.querySelectorAll('.ip-item').forEach(el => {
      const it = w.items[+el.dataset.i];
      el.querySelectorAll('[data-ipi]').forEach(f => { it[f.dataset.ipi] = f.type === 'checkbox' ? f.checked : f.value.trim(); });
    });
    w.mask = $('ip-mask').checked;
  }
}

async function ipDraw() {
  const w = UI.ipWiz;
  const sample = w.mode === 'random' ? { mode: 'random', n: parseInt(w.n, 10) || 0, seed: w.seed ? Number(w.seed) : 0 } : w.mode === 'list' ? { mode: 'list', given: [w.given] } : { mode: 'none' };
  w.sample = await post('/v1/inspection-packs/sample', { to: w.to, sample });
  if (w.mode === 'random') w.seed = String(w.sample.seed);
}

async function ipReady() {
  const w = UI.ipWiz;
  try { w.ready = await post('/v1/inspection-packs/readiness', { items: w.items }); } catch (e) { w.ready = null; }
}

Object.assign(A, {
  ipNew: id => { if (!C.inspack?.templates) return; ipWizOpen(id); },
  ipBack: () => { ipWizRead(); UI.ipWiz.err = ''; UI.ipWiz.step--; ipWizShow(); },
  ipNext: async () => {
    const w = UI.ipWiz; ipWizRead(); w.err = '';
    try {
      if (w.step === 2) {
        if (!w.title) throw new Error('Name the inspection: it goes on every file');
        if (!w.from || !w.to || w.from > w.to) throw new Error('Choose a period that starts before it ends');
      }
      if (w.step === 3) {
        if (w.mode !== 'none' && !w.sample) await ipDraw();
        if (w.mode === 'none') { w.sample = null; w.items = w.items.filter(it => !it.sampled); }
        await ipReady();
      }
      w.step++;
    } catch (e) { w.err = e.message; }
    ipWizShow();
  },
  ipDraw: async () => { ipWizRead(); UI.ipWiz.err = ''; try { await ipDraw(); } catch (e) { UI.ipWiz.err = e.message; } ipWizShow(); },
  ipPreset: id => { ipWizRead(); [UI.ipWiz.from, UI.ipWiz.to] = ipPreset(id, C.inspack.today); UI.ipWiz.sample = null; ipWizShow(); },
  ipItemAdd: () => { ipWizRead(); UI.ipWiz.items.push({ title: 'New item', period: 'period', section: 'not_held', note: '' }); ipWizShow(); setTimeout(() => $(`ipi-t-${UI.ipWiz.items.length - 1}`)?.select(), 20); },
  ipItemDel: id => { ipWizRead(); UI.ipWiz.items.splice(+id, 1); ipWizShow(); },
  ipSaveTpl: async () => {
    ipWizRead(); const name = $('ip-saveas').value.trim(), w = UI.ipWiz;
    if (!name) { toast('Give the template a name'); $('ip-saveas').focus(); return; }
    const t = C.inspack.templates.find(x => x.id === w.tpl) || {};
    try { const saved = await post('/v1/inspection-templates', { name, kind: t.kind === 'firm' ? 'firm' : t.kind, authority: t.authority || '', about: t.about || '', sources: t.sources || [], sample_n: parseInt(w.n, 10) || 0, items: w.items }); C.inspack.templates.push(saved); w.tpl = saved.id; toast('Saved as “' + name + '”'); }
    catch (e) { w.err = e.message; }
    ipWizShow();
  },
  ipBuild: async () => {
    const w = UI.ipWiz; $('ip-build').disabled = true;
    const sample = w.mode === 'random' && w.sample ? { mode: 'random', n: w.sample.clients.length, seed: w.sample.seed } : w.mode === 'list' ? { mode: 'list', given: [w.given] } : { mode: 'none' };
    try {
      const p = await post('/v1/inspection-packs', { template: w.tpl, title: w.title, ref: w.ref, from: w.from, to: w.to, sample, items: w.items, mask: w.mask });
      closeModal(); UI.ipWiz = null; UI.ipTab = 'packs'; toast(`${p.id} is being built`); render({ keep: true });
    } catch (e) { w.err = e.message; ipWizShow(); }
  },
  ipDownload: id => { const p = C.inspack.packs.find(x => x.id === id); download(`/v1/inspection-packs/${id}/download`, p?.file_name || id + '.zip').then(() => render({ keep: true })); },
  ipIndex: id => { openDoc(`/v1/inspection-packs/${id}/index`); setTimeout(() => render({ keep: true }), 800); },
  ipMask: id => confirmModal('Make a masked copy', 'For outside advisers. The same template, period, items and sample, built again with client names, codes, PANs, phones, emails and account numbers as tokens such as [CLIENT-3], and without KYC images.', '', 'Make the masked copy',
    () => act(() => post(`/v1/inspection-packs/${id}/masked-copy`), p => `${p.id} is being built`)),
  ipExpire: id => confirmModal('Remove from the download list', 'The files are no longer served; the record of the pack, its contents and who opened it stay.', '<div class="fld"><label for="ipx-why">Why</label><input class="in" id="ipx-why" placeholder="e.g. inspection closed"></div>', 'Remove',
    () => act(() => post(`/v1/inspection-packs/${id}/expire`, { why: $('ipx-why').value.trim() }), 'Removed from the download list'), true),
  ipAudit: id => {
    const p = C.inspack.packs.find(x => x.id === id);
    modal(mhd('Who opened ' + esc(p.id), esc(p.title) + ' · the same entries are in the sealed audit trail') + `<div class="bd">${tbl([['When'], ['Who'], ['What'], ['Detail']], (p.access || []).slice().reverse().map(a => `<tr><td class="small nowrap">${ipStamp(a.at)}</td><td>${esc(a.by)}</td><td>${esc(a.what)}</td><td class="small wrap">${esc(a.detail || '')}</td></tr>`))}
      ${p.sha256 ? `<p class="dim small">SHA-256 of the ZIP: <span class="mono ip-hash">${esc(p.sha256)}</span></p>` : ''}</div>`);
  },
  ipKeep: () => {
    const s = C.inspack.settings;
    modal(mhd('How long packs stay on the download list', 'After that the files are no longer served. The record of each pack and who opened it stay.') + `<div class="bd"><div class="fld"><label for="ipk-d">Days</label><input class="in" type="number" id="ipk-d" min="1" max="365" value="${esc(s.keep_days)}"></div><div id="ipk-err"></div></div>
      <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="ipKeepSave">Save</button></div>`);
  },
  ipKeepSave: () => act(() => api('PUT', '/v1/inspection-packs/settings', { keep_days: parseInt($('ipk-d').value, 10) || 0 }), 'Saved: new packs use it'),
  ipDocRetire: id => confirmModal('Retire the document', 'It stays on record but goes into no new pack.', '<div class="fld"><label for="ipr-why">Why</label><input class="in" id="ipr-why" placeholder="e.g. replaced by the 2027 version"></div>', 'Retire',
    () => act(() => post(`/v1/inspection-docs/${id}/retire`, { why: $('ipr-why').value.trim() }), 'Retired')),
  ipTplDel: id => confirmModal('Delete the template', 'Packs made from it keep their items.', '', 'Delete', () => act(() => post(`/v1/inspection-templates/${id}/delete`), 'Deleted'), true),
  ipTplEdit: id => {
    const t = C.inspack.templates.find(x => x.id === id);
    UI.ipTpl = { ...t, id: t.standard ? '' : t.id, name: t.standard ? t.name + ' (the firm’s copy)' : t.name, items: t.items.map(x => ({ ...x })) };
    ipTplShow();
  },
  ipTplItemAdd: () => { ipTplRead(); UI.ipTpl.items.push({ title: 'New item', period: 'period', section: 'not_held' }); ipTplShow(); },
  ipTplItemDel: id => { ipTplRead(); UI.ipTpl.items.splice(+id, 1); ipTplShow(); },
  ipTplSave: async () => {
    ipTplRead(); const t = UI.ipTpl;
    try { await post('/v1/inspection-templates', { id: t.id, name: t.name, kind: t.kind, authority: t.authority || '', about: t.about || '', sources: t.sources || [], sample_n: parseInt(t.sample_n, 10) || 0, items: t.items }); closeModal(); toast('Template saved'); UI.ipTab = 'tpl'; render({ keep: true }); }
    catch (e) { $('ipt-err').innerHTML = errHTML(e); }
  },
});

// The firm's template editor: name, sample size and items.
function ipTplShow() {
  const t = UI.ipTpl, d = C.inspack;
  const secs = d.sections.map(s => `<option value="${esc(s.id)}">${esc(s.name)}</option>`).join('');
  const per = d.periods.map(p => `<option value="${esc(p.id)}">${esc(p.label)}</option>`).join('');
  const sel = (html, v) => html.replace(`value="${esc(v)}"`, `value="${esc(v)}" selected`);
  wideModal(mhd(t.id ? 'Edit the template' : 'The firm’s copy of the template', 'Items, their period and the Ledgerline report that produces each.') + `<div class="bd">
    <div class="form"><div class="fld"><label for="ipt-name">Name</label><input class="in" id="ipt-name" value="${esc(t.name)}"></div><div class="fld"><label for="ipt-n">Sample of clients</label><input class="in" type="number" id="ipt-n" min="0" max="500" value="${esc(t.sample_n || 0)}"></div></div>
    <div class="ip-items">${t.items.map((it, i) => `<div class="ip-item ipt-item" data-i="${i}"><div class="ip-no">${i + 1}</div><div class="ip-main"><label class="sr" for="ipt-t-${i}">Item ${i + 1}</label><input class="in" id="ipt-t-${i}" data-ipt="title" value="${esc(it.title)}">
      <div class="ip-row"><div class="fld"><label for="ipt-p-${i}">Period</label><select class="in" id="ipt-p-${i}" data-ipt="period">${sel(per, it.period)}</select></div><div class="fld ip-sec"><label for="ipt-s-${i}">Produced by</label><select class="in" id="ipt-s-${i}" data-ipt="section">${sel(secs, it.section)}</select></div>
      <div class="fld"><label for="ipt-x-${i}">Option or note</label><input class="in" id="ipt-x-${i}" data-ipt="${it.section === 'not_held' ? 'note' : 'param'}" value="${esc(it.section === 'not_held' ? it.note || '' : it.param || '')}"></div>
      <label class="check ip-chk"><input type="checkbox" data-ipt="sampled" ${it.sampled ? 'checked' : ''}> Sampled clients only</label></div>${it.to_confirm ? `<div class="small">${pill('To confirm', 'warn')} ${esc(it.to_confirm)}</div>` : ''}</div>
      <button class="btn sm" data-act="ipTplItemDel" data-id="${i}" aria-label="Take item ${i + 1} off">Take off</button></div>`).join('')}</div>
    <div class="tools"><button class="btn sm" data-act="ipTplItemAdd">Add an item</button></div><div id="ipt-err"></div></div>
    <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="ipTplSave">Save template</button></div>`);
}
function ipTplRead() {
  const t = UI.ipTpl; t.name = $('ipt-name').value.trim(); t.sample_n = $('ipt-n').value;
  document.querySelectorAll('.ipt-item').forEach(el => { const it = t.items[+el.dataset.i]; el.querySelectorAll('[data-ipt]').forEach(f => { it[f.dataset.ipt] = f.type === 'checkbox' ? f.checked : f.value.trim(); }); });
}

// Picking a template, a sample mode or a report redraws the step.
document.addEventListener('change', e => {
  const el = e.target;
  if (UI.ipTpl && el.dataset.ipt === 'section' && el.closest?.('.ipt-item')) { ipTplRead(); ipTplShow(); return; }
  if (!UI.ipWiz || !el.closest?.('.ip-wiz')) return;
  if (el.name === 'ip-tpl' || el.name === 'ip-mode') { ipWizRead(); ipWizShow(); }
  else if (el.dataset.ipi === 'section') { ipWizRead(); ipReady().then(ipWizShow); }
});
