'use strict';
/* Ledgerline console: Ledgerline support access (screen "supportaccess",
   Admin → Security). Ledgerline's support people see nothing of the firm
   until the firm approves a request here: waiting requests, open access
   with End now, the history, the log (and its CSV), and the switch that
   blocks all support access. Also the banner a support session shows on
   every screen. Loaded after app.js. */

Object.assign(UI, { saTab: 'now' });
const SA_ST = { Pending: 'warn', Approved: 'acc', Refused: 'nt', Withdrawn: 'nt', Ended: 'nt', Expired: 'nt' };
const saPill = s => pill(s === 'Approved' ? 'Open' : s, SA_ST[s] || 'nt');
const saAreaName = (d, id) => (d.areas.find(a => a.id === id) || { name: id }).name;
const saScope = (d, r) => `${r.write ? pill('Read and write', 'warn') : pill('Read-only', 'good')} ${r.areas.map(a => esc(saAreaName(d, a))).join(', ')}`;
const saWho = r => `<b>${esc(r.operator_name || r.operator)}</b><div class="dim small mono">${esc(r.actor)}</div>`;

V.supportaccess = {
  load: () => get('/v1/support-access'),
  html: d => {
    const p = d.policy;
    const block = p.blocked
      ? `<div class="note bad"><b>All Ledgerline support access is blocked.</b> New requests are refused and nobody from Ledgerline can open a session. ${d.can_change ? '<button class="btn sm" data-act="saBlock" data-on="0">Allow requests again</button>' : ''}</div>`
      : `<div class="note">Nobody from Ledgerline can see your firm's data unless you approve a request here. ${d.can_change ? '<button class="btn sm bad" data-act="saBlock" data-on="1">Block all Ledgerline support access</button>' : ''}</div>`;
    return ph('Support access', `Ledgerline's support team asks here before looking at your data. You see who asks, why, for which ticket, which areas, whether they may change anything and for how long (at most ${d.max_hours} hours). PAN, Aadhaar and bank account numbers stay masked for them, and every screen they open is recorded.`,
      `<button class="btn" data-act="saCSV">Download log (CSV)</button>`) + block +
      `<div class="panel"><div class="hd">${segBtns('saTab', [['now', `Waiting and open${d.pending.length + d.active.length ? ` (${d.pending.length + d.active.length})` : ''}`], ['history', 'History'], ['log', 'Log'], ['rules', 'Rules']])}</div></div>` +
      ({ now: saNow, history: saHistory, log: saLogView, rules: saRules }[UI.saTab] || saNow)(d);
  }
};

function saNow(d) {
  const can = d.can_approve;
  const pend = d.pending.map(r => `<tr><td class="mono">${esc(r.id)}</td><td>${saWho(r)}</td><td class="wrap">${esc(r.reason)}<div class="dim small">Ticket ${esc(r.ticket)}${r.outage ? ' · outage' : ''}</div></td>
    <td class="wrap">${saScope(d, r)}</td><td class="r">${r.hours} h</td><td class="small nowrap">${fmtStamp(r.requested)}</td>
    <td class="nowrap">${can ? `<button class="btn sm pri" data-act="saApprove" data-id="${esc(r.id)}">Approve…</button> <button class="btn sm" data-act="saRefuse" data-id="${esc(r.id)}">Refuse</button>` : '<span class="dim small">The Admin decides</span>'}</td></tr>`);
  const act = d.active.map(r => `<tr><td class="mono">${esc(r.id)}</td><td>${saWho(r)}</td><td class="wrap">${esc(r.reason)}<div class="dim small">Ticket ${esc(r.ticket)} · approved by ${esc(r.decided_by)}</div></td>
    <td class="wrap">${saScope(d, r)}</td><td class="small nowrap">${fmtStamp(r.expires)}</td><td class="small">${r.started ? `In use since ${fmtStamp(r.started)}${r.sessions > 1 ? ` · ${r.sessions} sessions` : ''}` : 'Not used yet'}</td>
    <td>${can ? `<button class="btn sm bad" data-act="saEnd" data-id="${esc(r.id)}">End now</button>` : ''}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Waiting for a decision</h3><small>Unanswered requests lapse after 24 hours</small></div>${tbl([['Request'], ['Who'], ['Why'], ['Asks for'], ['Hours', 'r'], ['Asked'], ['']], pend, { empty: 'No requests waiting.' })}</div>
    <div class="panel"><div class="hd"><h3>Open access</h3><small>End now takes effect on their next click</small></div>${tbl([['Request'], ['Who'], ['Why'], ['May'], ['Ends'], ['Use'], ['']], act, { empty: 'Nobody from Ledgerline has access now.' })}</div>`;
}

function saHistory(d) {
  return `<div class="panel">${tbl([['Request'], ['Who'], ['Why'], ['Asked for'], ['Result'], ['Decided'], ['Ended']], d.history.map(r => `<tr><td class="mono">${esc(r.id)}</td><td>${saWho(r)}</td>
    <td class="wrap">${esc(r.reason)}<div class="dim small">Ticket ${esc(r.ticket)}</div></td><td class="wrap">${saScope(d, r)} · ${r.hours} h</td>
    <td>${saPill(r.status)}${r.note ? `<div class="dim small wrap">${esc(r.note)}</div>` : ''}</td><td class="small">${r.decided_by ? esc(r.decided_by) + '<br>' + fmtStamp(r.decided_at) : '—'}</td>
    <td class="small wrap">${r.ended_at ? fmtStamp(r.ended_at) + (r.end_reason ? '<br>' + esc(r.end_reason) : '') : '—'}</td></tr>`), { empty: 'No earlier requests.' })}</div>`;
}

function saLogView(d) {
  if (!UI.saLog) { get('/v1/support-access/log').then(r => { UI.saLog = r.lines; if (S.view === 'supportaccess' && UI.saTab === 'log') render({ keep: true }); }).catch(e => toast(e.message, true)); return '<p class="loading">Loading…</p>'; }
  const ls = UI.saLog; UI.saLog = null; // read afresh next time
  return `<div class="panel"><div class="hd"><h3>Support access log</h3><small>From the audit trail: every request, decision, session and screen support opened</small></div>${tbl([['Time'], ['By'], ['Request'], ['What']], ls.map(l => `<tr><td class="small nowrap">${fmtStamp(l.at)}</td><td class="mono small">${esc(l.actor)}</td><td class="mono">${esc(l.request || '')}</td><td class="wrap small">${esc(l.what)}</td></tr>`), { empty: 'Nothing yet.', tall: true })}</div>`;
}

function saRules(d) {
  const p = d.policy, dis = d.can_change ? '' : 'disabled';
  return `<div class="panel"><div class="hd"><h3>Rules</h3><small>${p.changed ? `Changed ${fmtStamp(p.changed)} by ${esc(p.changed_by)}` : 'Not changed yet'}</small></div><div class="bd"><div class="form">
    <div class="fld"><label for="sa-appr">Who approves besides the Admin</label><select class="in" id="sa-appr" ${dis}>${d.approver_roles.map(r => `<option value="${r === 'Admin' ? '' : esc(r)}" ${(p.approver_role || '') === (r === 'Admin' ? '' : r) ? 'selected' : ''}>${r === 'Admin' ? 'Only the Admin' : esc(r)}</option>`).join('')}</select></div>
    <div class="fld full"><label><input type="checkbox" id="sa-out" ${p.outage_read_only ? 'checked' : ''} ${dis}> Let Ledgerline support in at once, read-only, when they mark a request as an outage</label>
      <div class="dim small">Off unless you turn it on. Never for write or for full access. You still get the e-mail and a work item, and can end it at any time.</div></div>
    <div class="fld"><label for="sa-oh">Outage access lasts (hours)</label><input class="in mono" id="sa-oh" type="number" min="1" max="${d.max_hours}" value="${p.outage_hours || 2}" ${dis}></div>
  </div>${d.can_change ? '<div class="ft" style="padding:0;border:0"><button class="btn pri" data-act="saRules">Save</button></div>' : '<div class="dim small">Only the Admin changes these.</div>'}</div></div>`;
}

const saSavePolicy = (patch, msg) => act(() => api('PUT', '/v1/support-access/policy', { ...C.supportaccess.policy, changed: undefined, changed_by: undefined, ...patch }), msg);

Object.assign(A, {
  saApprove: id => {
    const d = C.supportaccess, r = d.pending.find(x => x.id === id); if (!r) return;
    const areas = r.areas.includes('full') ? d.areas.map(a => a.id) : r.areas;
    modal(mhd('Approve support access · ' + esc(id), `${esc(r.operator_name)} asks: ${esc(r.reason)} (ticket ${esc(r.ticket)}). You can give less than was asked.`) + `<div class="bd"><div class="form">
      <div class="fld full"><label>Areas</label>${areas.map(a => `<label class="small" style="display:block"><input type="checkbox" data-saarea="${esc(a)}" ${r.areas.includes(a) || r.areas.includes('full') && a === 'full' ? 'checked' : ''}> ${esc(saAreaName(d, a))}</label>`).join('')}</div>
      <div class="fld"><label for="sa-w">May change things</label><select class="in" id="sa-w"><option value="0">No: read-only</option>${r.write ? '<option value="1" selected>Yes, as asked</option>' : ''}</select></div>
      <div class="fld"><label for="sa-h">Hours</label><input class="in mono" id="sa-h" type="number" min="1" max="${r.hours}" value="${r.hours}"></div>
      <div class="fld full"><label for="sa-n">Note (optional)</label><input class="in" id="sa-n" maxlength="500"></div></div>
      <div class="dim small">Access starts now and ends by itself when the hours run out. PAN, Aadhaar and bank account numbers stay masked. You can end it at any time.</div><div id="sa-err"></div></div>
      <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="sa-ok">Approve</button></div>`);
    $('sa-ok').onclick = async () => {
      const as = [...document.querySelectorAll('[data-saarea]')].filter(x => x.checked).map(x => x.dataset.saarea);
      try { await post(`/v1/support-access/${encodeURIComponent(id)}/approve`, { areas: as, write: $('sa-w').value === '1', hours: +$('sa-h').value, note: $('sa-n').value }); closeModal(); toast(id + ' approved'); render({ keep: true }); }
      catch (e) { $('sa-err').innerHTML = errHTML(e); }
    };
  },
  saRefuse: id => confirmModal('Refuse support access · ' + esc(id), 'Ledgerline support sees that you refused and your note.', '<div class="fld"><label for="sa-rn">Note (optional)</label><input class="in" id="sa-rn" maxlength="500"></div>', 'Refuse',
    () => act(() => post(`/v1/support-access/${encodeURIComponent(id)}/refuse`, { note: $('sa-rn').value }), id + ' refused'), true),
  saEnd: id => act(() => post(`/v1/support-access/${encodeURIComponent(id)}/end`), id + ' ended: their next click is refused'),
  saBlock: (id, el) => el.dataset.on === '1'
    ? confirmModal('Block all Ledgerline support access', 'Open access ends at once, waiting requests are refused, and new requests are refused until you allow them again.', '', 'Block', () => saSavePolicy({ blocked: true }, 'All Ledgerline support access blocked'), true)
    : saSavePolicy({ blocked: false }, 'Ledgerline support can ask for access again'),
  saRules: () => saSavePolicy({ approver_role: $('sa-appr').value, outage_read_only: $('sa-out').checked, outage_hours: +$('sa-oh').value }, 'Saved'),
  saCSV: () => download('/v1/support-access/log.csv', 'support-access-log.csv'),
  saSelfEnd: () => act(async () => { await post('/v1/support-access/session/end'); await api('POST', '/v1/auth/logout'); location.href = '/support-desk'; }, 'Support session ended', false),
});

/* ---- the banner a Ledgerline support session shows on every screen ---- */
const SA_VIEW = { clients: 'clients', ledger: 'ledger', exchange_files: 'xfiles', settings: 'settings', full: 'dashboard' };
const saSession = () => S.me?.auth?.support || null;
function supportBanner() {
  const s = saSession(); if (!s) return '';
  const areas = s.areas.includes('full') ? 'every screen' : s.areas.map(a => a.replace('_', ' ')).join(', ');
  return `<div class="note warn sa-banner" role="status"><b>Ledgerline support session</b> · ${esc(s.name)} for ${esc(s.firm)} · ticket ${esc(s.ticket)} · ${s.write ? 'read and write' : 'read-only'} · ${esc(areas)} · ends ${fmtStamp(s.expires)}.
    PAN, Aadhaar and bank account numbers are masked, files can't be downloaded, and everything you open is recorded for the firm. <button class="btn sm" data-act="saSelfEnd">End session</button></div>`;
}
// A support session starts on a screen it may open.
function supportStartView() {
  const s = saSession(); if (!s || s.areas.includes('full')) return;
  const ok = s.areas.map(a => SA_VIEW[a]).filter(Boolean);
  if (!ok.includes(S.view)) S.view = ok[0] || S.view;
}
