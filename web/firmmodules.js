'use strict';
/* Ledgerline console: Settings → Firm modules (internal/ops/firmmodules.go,
   internal/api/firmmodules.go). The broker says which exchange segments it
   trades and how each is cleared, whether it is a depository participant,
   the products and depository options it uses, facts about the firm and
   its client numbers; the compliance that applies changes with it. A
   change shows what it switches on and off before it is saved. Screens of
   modules the firm does not have are hidden from the menu (fmViewOK, from
   S.me.hidden_screens); an Admin can show them all. Loaded after app.js.
   Names are prefixed fm. */

// Show every module's screens, whatever the firm has (an Admin's choice,
// kept in this browser only).
const FM_LS = 'ledgerline-show-all-modules';
const fmShowAll = () => { try { return localStorage.getItem(FM_LS) === '1'; } catch (e) { return false; } };
// fmViewOK: whether a screen (or a part "screen.part") belongs to a module
// the firm has. Unknown answers show it.
function fmViewOK(k) {
  if (fmShowAll()) return true;
  return !(S.me?.hidden_screens || {})[k];
}
const fmPart = k => fmViewOK(k);

const FM_ROLES = [['none', 'Not traded'], ['tm', 'Trading member (another clearing member clears)'], ['self', 'Self-clearing member (own trades only)'], ['pcm', 'Clears for other members too (TM-CM / PCM)']];
const fmYN = (id, v, dis, cls = 'in sm fm-in') => `<select class="${cls}" id="${id}" ${dis}><option value="" ${!v ? 'selected' : ''}>Not answered</option><option value="yes" ${v === 'yes' ? 'selected' : ''}>Yes</option><option value="no" ${v === 'no' ? 'selected' : ''}>No</option></select>`;
const fmVerdict = v => pill({ yes: 'Yes', no: 'No', unsure: 'Not sure' }[v] || v, { yes: 'good', no: 'nt', unsure: 'warn' }[v] || 'nt');
const fmSafe = id => String(id).replace(/[^a-z0-9]/gi, '_');

function fmSegRows(d, dis) {
  return (d.segments || []).map(s => {
    const id = 'fm-seg-' + s.key, role = s.role === 'self?' ? '' : s.role;
    const roles = (s.role === 'self?' ? [['self?', 'Self-clearing (not answered whether it clears for others)']] : []).concat(FM_ROLES);
    return `<tr><td class="n">${esc(s.key)}</td><td><select class="in sm fm-in" id="${id}" data-seg="${esc(s.key)}" ${dis}>${roles.map(([v, l]) => `<option value="${v}" ${(s.role === v || role === v) ? 'selected' : ''}>${esc(l)}</option>`).join('')}</select></td>
      <td><select class="in sm fm-in" id="${id}-cc" ${dis}>${s.allowed.map(a => `<option value="${a}" ${(s.cc || (s.default !== 'NONE' ? s.default : s.allowed[0])) === a ? 'selected' : ''}>${esc(a)} · ${esc((d.clearing_corps || {})[a] || '')}</option>`).join('')}</select></td>
      <td><input class="in sm mono fm-in" id="${id}-code" placeholder="code" value="${esc(s.cm_code || '')}" ${dis}></td><td><input class="in sm fm-in" id="${id}-name" placeholder="clearing member name" value="${esc(s.cm_name || '')}" ${dis}></td></tr>`;
  });
}

function fmSwitchRows(list, dis) {
  return list.map(x => {
    const id = 'fm-sw-' + fmSafe(x.id);
    let ctl;
    if (x.store === 'segments') ctl = `<span class="dim small">From the segments above</span>`;
    else if (x.store === 'compliance') ctl = `<select class="in sm fm-in" id="${id}" ${dis}><option value="yes" ${x.answer === 'yes' ? 'selected' : ''}>Yes</option><option value="no" ${x.answer !== 'yes' ? 'selected' : ''}>No</option></select>`;
    else ctl = fmYN(id, x.answer, dis);
    const ex = (x.exchanges || []).length ? `<div class="small">${x.exchanges.map(e => `<label class="chk"><input type="checkbox" class="fm-in" data-fm-on="${esc(x.id)}" value="${esc(e)}" ${(x.on || []).includes(e) ? 'checked' : ''} ${dis}> ${esc(e)}</label>`).join(' ')}</div>` : '';
    return `<tr><td class="wrap"><b>${esc(x.label)}</b><div class="dim small">${esc(x.note || '')} <span class="mono">${esc(x.id)}</span></div></td><td>${ctl}${ex}</td><td>${esc(x.value)}<div class="dim small">${esc(x.from || '')}</div></td></tr>`;
  });
}

V.firmmodules = {
  load: () => get('/v1/firm-modules'),
  html: d => {
    const dis = d.can_edit ? '' : 'disabled', p = d.profile || {};
    const sw = (t, list) => `<div class="panel"><div class="hd"><h3>${t}</h3></div>${tbl([['What'], ['Answer'], ['Rules read']], fmSwitchRows(list, dis))}</div>`;
    const counts = (d.counts || []).map(c => `<tr><td class="wrap"><b>${esc(c.label)}</b><div class="dim small">${esc(c.how)}</div></td><td class="r">${c.counted != null ? nf(c.counted) : '—'}${c.partial ? `<div class="warn small">At least: ${esc(c.partial)}</div>` : ''}</td>
      <td><input class="in sm mono fm-in" id="fm-n-${c.key}" type="number" min="0" placeholder="Ledgerline's count" value="${c.override ? c.override.value : ''}" ${dis}><input class="in sm fm-in" id="fm-nn-${c.key}" placeholder="note (source)" value="${esc(c.override?.note || '')}" ${dis}>${c.override ? `<div class="dim small">${esc(c.override.by)} · ${fmtTS(c.override.at)}</div>` : ''}</td><td class="r"><b>${c.value != null ? nf(c.value) : 'Not known'}</b></td></tr>`);
    const derived = (d.derived || []).map(x => `<tr><td>${esc(x.label)}</td><td>${fmVerdict(x.verdict)} ${esc(x.value)}</td><td class="wrap dim small">${esc(x.why)}${x.warn ? `<div class="neg">${esc(x.warn)}</div>` : ''}</td></tr>`);
    const screens = (d.screens || []).map(g => `<tr><td class="mono">${esc(g.key)}</td><td>${esc(g.what)}</td><td class="mono small">${esc(g.cond)}</td><td>${g.hidden ? pill('Hidden', 'nt') : pill('Shown', 'good')}</td><td class="wrap dim small">${esc(g.why)}</td></tr>`);
    const conds = (d.conditions || []).map(c => `<tr><td class="mono">${esc(c.cond)}</td><td>${esc(c.means)}</td><td>${fmVerdict(c.verdict)}</td><td class="wrap dim small">${esc(c.why)}</td></tr>`);
    const admin = has('Admin');
    return ph('Firm modules', 'What the firm does: exchanges and how each segment is cleared, depository, products, depository options, the firm itself and its client numbers. The compliance calendar, self-inspection and the menu follow it. The compliance officer and an Admin change it; everyone can read it.',
      d.can_edit ? '<button class="btn" data-act="fmPreview">What changes</button><button class="btn pri" data-act="fmSave">Save</button>' : '') +
      ((d.unknown || []).length ? `<div class="note warn"><b>Still to answer (duties that depend on these show as Not sure):</b><ul>${d.unknown.slice(0, 12).map(u => `<li>${esc(u)}</li>`).join('')}</ul>${d.unknown.length > 12 ? `<div class="small">…and ${d.unknown.length - 12} more below.</div>` : ''}</div>` : '') +
      `<div class="panel" id="fm-diff-panel" hidden><div class="hd"><h3>What changes if you save</h3></div><div class="bd" id="fm-diff"></div></div>
      <div class="panel"><div class="hd"><h3>Exchanges and clearing</h3><small>Written to Settings → member codes and Cleared through: the same answer in both places</small></div><div class="bd">
        <div class="form">${(d.exchanges || []).map(x => `<div class="fld"><label for="fm-mc-${x}">${esc(x)} member code</label><input class="in mono fm-in" id="fm-mc-${x}" value="${esc((d.member_codes || {})[x] || '')}" ${dis}></div>`).join('')}</div>
        ${d.assumed ? '<p class="note">No member codes yet: Ledgerline assumes NSE, BSE and MCX. Give the codes so the rules can be sure.</p>' : ''}</div>
        ${tbl([['Segment'], ['Role'], ['Clearing corporation'], ['Clearing member code'], ['Clearing member name']], fmSegRows(d, dis))}</div>
      <div class="grid g2">${sw('Depository', d.depository || [])}${sw('Products', d.products || [])}</div>
      <div class="grid g2">${sw('Depository options', d.dp_options || [])}${sw('The firm', d.firm || [])}</div>
      <div class="panel"><div class="hd"><h3>More about the firm</h3></div><div class="bd"><div class="form">
        <div class="fld"><label for="fm-const">Constitution</label><select class="in fm-in" id="fm-const" ${dis}><option value="">Not answered</option>${(d.constitutions || []).map(c => `<option value="${c.ID}" ${d.constitution === c.ID ? 'selected' : ''}>${esc(c.Name)}</option>`).join('')}</select></div>
        <div class="fld"><label for="fm-qsb">Qualified stock broker (the exchanges' list)</label><select class="in fm-in" id="fm-qsb" ${dis}><option value="" ${!d.qsb ? 'selected' : ''}>Not answered</option><option value="yes" ${d.qsb === 'yes' ? 'selected' : ''}>Yes, designated QSB</option><option value="ex" ${d.qsb === 'ex' ? 'selected' : ''}>Was a QSB</option><option value="no" ${d.qsb === 'no' ? 'selected' : ''}>No</option></select></div>
        <div class="fld"><label for="fm-qsbend">Stopped being a QSB on</label><input class="in fm-in" type="date" id="fm-qsbend" value="${esc(d.qsb_ended_on || '')}" ${dis}><div class="dim small">${d.qsb_ex_until ? 'QSB obligations continue until ' + fmtDate(d.qsb_ex_until) + ' (three financial years).' : 'Ex-QSBs keep the obligations for three financial years.'}</div></div>
        <div class="fld"><label for="fm-spec">Specified stockbroker (NSE's top-20 list)</label>${fmYN('fm-spec', d.specified, dis, 'in fm-in')}</div>
        <div class="fld"><label for="fm-cmch">Last change of clearing member</label><input class="in fm-in" type="date" id="fm-cmch" value="${esc(d.cm_changed_on || '')}" ${dis}><div class="dim small">${(d.cm_changes || []).length ? 'Recorded from Settings: ' + d.cm_changes.slice(0, 3).map(c => `${fmtDate(c.date)} ${esc(c.segment)} ${esc(c.from)} → ${esc(c.to)}`).join('; ') : 'Changes made in Settings are recorded by themselves.'}</div></div>
        <div class="fld"><label for="fm-cscrf">CSCRF category (your answer wins)</label><select class="in fm-in" id="fm-cscrf" ${dis}><option value="">Work it out from the numbers</option>${(d.cscrf_categories || []).map(x => `<option value="${esc(x.ID)}" ${p.cscrf === x.ID ? 'selected' : ''}>${esc(x.Name)}</option>`).join('')}</select></div>
      </div></div></div>
      <div class="panel"><div class="hd"><h3>Client numbers</h3><small>${d.counted_at ? 'Counted ' + fmtTS(d.counted_at) : 'Not counted yet'}${d.can_edit ? ' · <button class="linkish" data-act="fmRecount">Count again</button>' : ''}</small></div>${tbl([['Number'], ['Ledgerline counts', 'r'], ['Your figure (blank: the count)'], ['Rules use', 'r']], counts)}</div>
      <div class="panel"><div class="hd"><h3>What the numbers and answers make of the firm</h3></div>${tbl([['Rule'], ['Result'], ['Why']], derived)}</div>
      <div class="panel"><div class="hd"><h3>Screens that belong to a module</h3><small>${admin ? `<label class="chk"><input type="checkbox" id="fm-all" ${fmShowAll() ? 'checked' : ''}> Show all modules in my menu</label>` : 'An Admin can show all modules'}</small></div>${tbl([['Screen'], ['What'], ['Condition'], ['Menu'], ['Why']], screens)}</div>
      <details class="panel"><summary class="hd"><h3>Every condition the rules read</h3></summary>${tbl([['Condition'], ['Means'], ['For the firm'], ['Why']], conds, { tall: true })}</details>
      ${d.by ? `<p class="dim small">Last changed by ${esc(d.by)} · ${fmtTS(d.at)}. Every change is in the audit trail with what it switched on and off.</p>` : ''}`;
  }
};

// fmReq: the whole screen as a change request.
function fmReq() {
  const d = C.firmmodules || {}, v = id => $(id)?.value ?? '';
  const r = { segments: {}, member_codes: {}, switches: {}, on: {}, counts: {} };
  (d.exchanges || []).forEach(x => { r.member_codes[x] = v('fm-mc-' + x).trim(); });
  (d.segments || []).forEach(s => { const id = 'fm-seg-' + s.key; r.segments[s.key] = { role: v(id), cc: v(id + '-cc'), cm_code: v(id + '-code').trim(), cm_name: v(id + '-name').trim() }; });
  [...(d.depository || []), ...(d.products || []), ...(d.dp_options || []), ...(d.firm || [])].forEach(x => {
    const el = $('fm-sw-' + fmSafe(x.id)); if (!el) return;
    if (x.store === 'compliance') r.mtf = el.value === 'yes';
    else if (x.store === 'profile') r[x.id] = el.value;
    else r.switches[x.id] = el.value;
    if ((x.exchanges || []).length) r.on[x.id] = [...document.querySelectorAll(`[data-fm-on="${x.id}"]`)].filter(c => c.checked).map(c => c.value);
  });
  r.constitution = v('fm-const'); r.qsb = v('fm-qsb'); r.qsb_ended_on = v('fm-qsbend'); r.specified = v('fm-spec'); r.cm_changed_on = v('fm-cmch'); r.cscrf = v('fm-cscrf');
  (d.counts || []).forEach(c => { const n = v('fm-n-' + c.key).trim(); r.counts[c.key] = n === '' ? null : { value: parseInt(n, 10), note: v('fm-nn-' + c.key) }; });
  return r;
}

function fmDiffHTML(x) {
  const list = (t, xs) => xs.length ? `<h4>${t}</h4>${tbl([['What'], ['Before'], ['After'], ['Why']], xs.map(i => `<tr><td class="wrap"><span class="mono small">${esc(i.id)}</span> ${esc(i.title)}</td><td>${fmVerdict(i.from)}</td><td>${fmVerdict(i.to)}</td><td class="wrap dim small">${esc(i.why)}</td></tr>`))}` : '';
  if (!(x.changes || []).length) return '<p class="dim">Nothing changed yet.</p>';
  return `<p><b>You changed:</b> ${x.changes.map(esc).join(' · ')}</p>` + (list('Calendar duties', x.duties || []) + list('Self-inspection tests', x.tests || []) + list('Screens in the menu', x.screens || []) || '<p class="dim">No duty, test or screen switches on or off.</p>');
}

let fmTimer = 0;
async function fmPreviewNow() {
  try { const x = await post('/v1/firm-modules/preview', fmReq()); const pn = $('fm-diff-panel'); if (pn) { pn.hidden = false; $('fm-diff').innerHTML = fmDiffHTML(x); } return x; }
  catch (e) { const pn = $('fm-diff-panel'); if (pn) { pn.hidden = false; $('fm-diff').innerHTML = errHTML(e); } return null; }
}
document.addEventListener('change', e => {
  const el = e.target;
  if (el.id === 'fm-all') { try { localStorage.setItem(FM_LS, el.checked ? '1' : '0'); } catch (err) {} renderSide(); toast(el.checked ? 'Every module’s screens are in your menu' : 'Screens of modules the firm does not have are hidden'); return; }
  if (S.view === 'firmmodules' && el.classList?.contains('fm-in') && C.firmmodules?.can_edit) { clearTimeout(fmTimer); fmTimer = setTimeout(fmPreviewNow, 300); }
});

Object.assign(A, {
  fmPreview: () => fmPreviewNow(),
  fmRecount: () => act(() => post('/v1/firm-modules/recount'), 'Counted again'),
  fmSave: async () => {
    const x = await fmPreviewNow(); if (!x) return;
    confirmModal('Save the firm modules', 'The calendar, self-inspection and the menu follow the new answers. The change is kept in the audit trail.', `<div class="small">${fmDiffHTML(x)}</div>`, 'Save', async () => {
      await api('PUT', '/v1/firm-modules', fmReq()); closeModal(); toast('Firm modules saved');
      try { await loadMe(); } catch (e) {}
      render({ keep: true });
    });
  },
});
