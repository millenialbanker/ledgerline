'use strict';
/* Ledgerline console: Users & roles. Loaded after app.js and uses its
   helpers. Staff screens: users (the people who sign in, with their roles)
   and roles (the built-in roles, the firm's own, and the step-by-step
   editor). A change to a role, and giving someone roles here, take effect
   only when a second Admin approves (internal/ops/rolechange.go). The
   screens come from /screens.js, the list the server checks against. */

/* ---------- the console follows the person's roles (used by app.js) ---------- */
// rlSees: a screen a role the firm made or edited may open.
function rlSees(k) { const a = S.me.access; return a.admin || !SCREEN_KEYS.has(k) || !!a.screens?.[k]?.view; }
// rlHas: whether to offer a button that needs one of the built-in roles,
// on the screen the person is on. A tick the role's template also has
// keeps the template's limits; a tick it lacks gives the whole power.
function rlHas(roles) {
  const a = S.me.access;
  if (a.admin || (a.legacy || []).some(r => roles.includes(r))) return true;
  const p = a.screens?.[S.view];
  if (!p || !(p.do || p.approve)) return false;
  return roles.some(r => (a.bases || []).includes(r)) || !!a.beyond?.[S.view];
}
function rlPreviewBanner() {
  return S.preview ? `<div class="note warn rl-pv"><span><b>Preview as ${esc(S.preview)}.</b> You see only what this role would see. Nothing can be changed, and the preview is recorded in the audit trail.</span><button class="btn sm" data-act="rlEndPreview">End preview</button></div>` : '';
}

const RL_P = [['view', 'View'], ['do', 'Do'], ['approve', 'Approve']];
const rlRoles = u => [u.role, ...(u.more_roles || [])];
const rlKind = r => r.built_in ? (r.edited ? pill('Built-in · edited', 'warn') : pill('Built-in', 'nt')) : pill('Firm’s own', 'acc');

/* ---------- Users ---------- */
V.users = {
  load: async () => { const [users, roles] = await Promise.all([get('/v1/users'), has('Admin') ? get('/v1/roles').catch(() => null) : null]); return { users, roles }; },
  html: d => {
    const admin = has('Admin'), pend = new Set((d.roles?.changes || []).filter(c => c.status === 'Pending' && c.kind === 'assign').map(c => c.user));
    return ph('Users', 'The people who sign in, and their roles. Giving someone a role the firm made, an edited role or more than one role needs a second Admin’s approval.', admin ? '<button class="btn pri" data-act="addUser">Add user</button>' : '') +
    `<div class="panel">${admin ? '' : '<div class="bd"><small>Maker-checker: the person who creates an override, application or entry cannot approve it.</small></div>'}${tbl([['User'], ['Roles'], ['Sign-in'], ['Last sign-in'], ['Status'], ['']], d.users.map(u => {
      const rs = rlRoles(u), plain = rs.length === 1 && (S.me.roles || []).includes(u.role);
      const roleCell = admin && u.id !== S.me.actor && plain ? `<select class="in sm" data-urole="${esc(u.id)}">${(S.me.roles || []).map(r => `<option ${r === u.role ? 'selected' : ''}>${esc(r)}</option>`).join('')}</select>` : rs.map(esc).join(' + ');
      return `<tr><td><b style="font-weight:600">${esc(u.name)}</b><div class="mono dim" style="font-size:11.5px">${esc(u.id)}</div></td><td>${roleCell}${u.branch ? `<div class="dim small">branch ${esc(u.branch)}</div>` : ''}${u.partner ? `<div class="dim small">partner ${esc(u.partner)}</div>` : ''}${pend.has(u.id) ? `<div>${pill('Change waiting for approval', 'warn')}</div>` : ''}${admin && u.role !== 'Client' ? `<div><button class="linkish small" data-act="rlAssign" data-id="${esc(u.id)}">Change roles…</button></div>` : ''}</td>
      <td>${u.has_password ? '' : pill('No password', 'warn') + ' '}${u.totp_enabled ? pill('2FA on', 'good') : pill('2FA off', 'nt')}${u.must_change_password ? ' ' + pill('Temp password', 'warn') : ''}${u.locked ? ' ' + pill('Locked', 'bad') : ''}</td><td class="n dim">${u.last_login ? new Date(u.last_login).toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'short' }) : '—'}</td>
      <td>${u.active ? pill('Active', 'good') : pill('Inactive', 'nt')}</td><td>${admin && u.id !== S.me.actor ? `<div class="split"><button class="btn sm" data-act="resetPw" data-id="${esc(u.id)}">Reset password</button>${u.totp_enabled ? `<button class="btn sm" data-act="reset2fa" data-id="${esc(u.id)}">Reset 2FA</button>` : ''}<button class="btn sm ${u.active ? 'bad' : ''}" data-act="toggleUser" data-id="${esc(u.id)}">${u.active ? 'Deactivate' : 'Activate'}</button></div>` : ''}</td></tr>`;
    }))}</div>`;
  }
};

/* ---------- Roles ---------- */
V.roles = {
  load: () => get('/v1/roles'),
  html: d => {
    const admin = has('Admin') && !S.preview, me = S.me.actor, pend = d.changes.filter(c => c.status === 'Pending'), done = d.changes.filter(c => c.status !== 'Pending').slice(0, 10);
    const one = admin && (d.admins || []).length < 2 ? `<div class="note warn">The firm has one Admin. A change to roles needs a second Admin to approve it: add one on Users first.</div>` : '';
    return ph('Roles', 'What each role may see and do, screen by screen. The built-in roles work as they always have until the firm edits them. Changes take effect when a second Admin approves.', admin ? '<button class="btn pri" data-act="rlNew">New role</button>' : '') + one +
      (pend.length ? `<div class="panel"><div class="hd"><h3>Waiting for a second Admin</h3><small>${pend.length}</small></div>${tbl([['Change'], ['What'], ['Proposed by'], ['']], pend.map(c => `<tr><td class="n">${esc(c.id)}</td><td>${rlChangeText(c)}${(c.warnings || []).length ? `<div class="dim small">${c.warnings.map(esc).join('<br>')}</div>` : ''}</td><td>${esc(c.by)}<div class="dim small">${esc(fmtDate(c.at))}</div></td>
        <td><div class="split">${c.role ? `<button class="btn sm" data-act="rlShowChange" data-id="${esc(c.id)}">Details</button>` : ''}${admin && c.by !== me ? `<button class="btn sm pri" data-act="rlDecide" data-id="${esc(c.id)}">Approve</button><button class="btn sm bad" data-act="rlReject" data-id="${esc(c.id)}">Reject</button>` : ''}${admin && c.by === me ? `<button class="btn sm" data-act="rlWithdraw" data-id="${esc(c.id)}">Withdraw</button><span class="dim small">Another Admin approves</span>` : ''}</div></td></tr>`))}</div>` : '') +
      `<div class="panel"><div class="hd"><h3>Roles</h3></div>${tbl([['Role'], ['What it does'], ['People', 'r'], ['']], d.roles.map(r => `<tr><td><b style="font-weight:600">${esc(r.name)}</b><div>${rlKind(r)}</div>${r.template && !r.built_in ? `<div class="dim small">from ${esc(r.template)}</div>` : ''}</td>
        <td class="small">${esc(r.about || '')}${r.approved_by ? `<div class="dim">Approved by ${esc(r.approved_by)} ${esc(fmtDate(r.approved_at))}</div>` : ''}</td><td class="r">${(r.holders || []).length}</td>
        <td><div class="split"><button class="btn sm" data-act="rlShow" data-id="${esc(r.name)}">Summary</button>${admin && r.name !== 'Admin' ? `<button class="btn sm" data-act="rlEdit" data-id="${esc(r.name)}">Edit</button>` : ''}${admin ? `<button class="btn sm" data-act="rlCopy" data-id="${esc(r.name)}">Copy</button>` : ''}${admin && r.edited ? `<button class="btn sm" data-act="rlReset" data-id="${esc(r.name)}">Reset to default</button>` : ''}${admin && !r.built_in ? `<button class="btn sm bad" data-act="rlRetire" data-id="${esc(r.name)}">Retire</button>` : ''}${admin && r.name !== 'Admin' ? `<button class="btn sm" data-act="rlPreviewRole" data-id="${esc(r.name)}">Preview</button>` : ''}</div></td></tr>`))}</div>` +
      `<div class="panel"><div class="hd"><h3>Who holds which roles</h3></div>${tbl([['Person'], ['Roles'], ['']], (d.people || []).map(p => `<tr><td>${esc(p.name)} <span class="mono dim small">${esc(p.id)}</span>${p.active ? '' : ' ' + pill('Inactive', 'nt')}</td><td>${p.roles.map(esc).join(' + ')}${p.branch ? ` <span class="dim small">· branch ${esc(p.branch)}</span>` : ''}${p.partner ? ` <span class="dim small">· partner ${esc(p.partner)}</span>` : ''}</td><td>${admin ? `<button class="btn sm" data-act="rlAssign" data-id="${esc(p.id)}">Change roles…</button>` : ''}</td></tr>`))}</div>` +
      (done.length ? `<div class="panel"><div class="hd"><h3>Recent changes</h3></div>${tbl([['Change'], ['What'], ['Proposed'], ['Decided']], done.map(c => `<tr><td class="n">${esc(c.id)}</td><td>${rlChangeText(c)}</td><td>${esc(c.by)}</td><td>${pill(c.status, c.status === 'Approved' ? 'good' : c.status === 'Rejected' ? 'bad' : 'nt')} ${esc(c.decided_by || '')}${c.note ? `<div class="dim small">${esc(c.note)}</div>` : ''}</td></tr>`))}</div>` : '');
  }
};

function rlChangeText(c) {
  switch (c.kind) {
    case 'role': return `${c.role.built_in ? 'Edit' : 'Save'} the role <b>${esc(c.name)}</b>`;
    case 'reset': return `Reset <b>${esc(c.name)}</b> to its default`;
    case 'retire': return `Retire the role <b>${esc(c.name)}</b>`;
    case 'assign': return `Give <b>${esc(c.user)}</b> ${c.roles.map(esc).join(' + ')}${c.branch ? ` (branch ${esc(c.branch)})` : ''}${c.partner ? ` (partner ${esc(c.partner)})` : ''}<div class="dim small">Before: ${(c.from || []).map(esc).join(' + ')}</div>`;
  }
  return esc(c.kind);
}
const rlRole = name => (C.roles?.roles || []).find(r => r.name === name);
const rlCopyOf = r => JSON.parse(JSON.stringify({ name: r.name, about: r.about || '', template: r.template || '', screens: r.screens || {}, limits: r.limits || { scope: 'firm' } }));
function rlSummaryModal(title, lines, warnings) {
  modal(mhd(esc(title), 'What the role can and cannot do') + `<div class="bd"><ul class="rl-sum">${(lines || []).map(l => `<li>${esc(l)}</li>`).join('')}</ul>${(warnings || []).length ? `<div class="note warn">${warnings.map(esc).join('<br>')}</div>` : ''}</div><div class="ft"><button class="btn" data-act="close">Close</button></div>`);
}

/* ---------- the editor: Start from → Screens → Limits → Review ---------- */
let RL = null; // { mode: 'new' | 'edit', step, d: role being edited, check }
const RL_STEPS = ['Start from', 'Screens', 'Limits', 'Review'];
function rlOpen(mode, d) { RL = { mode, step: 0, d, check: null, builtIn: !!rlRole(d.name)?.built_in && mode === 'edit' }; rlDraw(); }
function rlDraw() {
  const s = RL.step;
  wideModal(mhd(RL.mode === 'edit' ? 'Edit ' + esc(RL.d.name) : 'New role', RL.builtIn ? 'A built-in role. Editing it changes what everyone holding it may do; Reset to default puts it back.' : 'Build a role from a template or from blank. A second Admin approves it before it takes effect.') +
    `<div class="bd" id="rl-ed"><div class="steps rl-steps">${RL_STEPS.map((l, i) => `<span class="${i === s ? 'on' : i < s ? 'done' : ''}">${i + 1}. ${l}</span>`).join('')}</div><div id="rl-body">${[rlStart, rlScreens, rlLimits, rlReview][s]()}</div><div id="rl-err"></div></div>
    <div class="ft">${s > 0 ? '<button class="btn" data-act="rlBack">Back</button>' : '<button class="btn" data-act="close">Cancel</button>'}${s < 3 ? '<button class="btn pri" data-act="rlNext">Next</button>' : `<button class="btn" data-act="rlPreviewDraft">Preview as this role</button><button class="btn pri" data-act="rlSend">Send for approval</button>`}</div>`);
  rlMixed();
}
function rlStart() {
  const d = RL.d, tpl = (C.roles?.roles || []).filter(r => r.built_in && r.name !== 'Client');
  return `<div class="form"><div class="fld"><label for="rl-tpl">Start from</label><select class="in" id="rl-tpl" ${RL.mode === 'edit' ? 'disabled' : ''}><option value="">Blank: no screens</option>${tpl.map(r => `<option ${d.template === r.name ? 'selected' : ''} value="${esc(r.name)}">${esc(r.name)}</option>`).join('')}</select><span class="dim small">The role starts with this role’s screens and limits. Where you keep its ticks, the role does what that role does there today; a tick you add gives every action of that kind on the screen.</span></div>
    <div class="fld"><label for="rl-name">Name</label><input class="in" id="rl-name" ${RL.mode === 'edit' ? 'readonly' : ''} value="${esc(d.name)}" maxlength="40" placeholder="e.g. CRM desk"></div>
    <div class="fld full"><label for="rl-about">Short description</label><input class="in" id="rl-about" value="${esc(d.about || '')}" maxlength="300" placeholder="e.g. Works leads and follows up enquiries"></div></div>`;
}
function rlTick(keys, p, label) {
  const sc = RL.d.screens, on = keys.filter(k => sc[k]?.[p]).length;
  return `<label class="rl-cb"><input type="checkbox" data-rlkeys="${keys.join(' ')}" data-p="${p}" ${on && on === keys.length ? 'checked' : ''} ${on && on < keys.length ? 'data-mixed="1"' : ''} aria-label="${esc(label + ' · ' + p)}"></label>`;
}
function rlScreens() {
  const row = (cls, label, keys, extra) => `<div class="rl-row ${cls}"><span class="rl-l">${label}${extra || ''}</span>${RL_P.map(([p]) => rlTick(keys, p, label)).join('')}</div>`;
  const leaves = it => it.tabs ? it.tabs.map(t => t.key) : [it.key];
  return `<p class="dim small" style="margin-top:0">View: open the screen. Do: create and change (the maker’s power). Approve: approve what others entered (the checker’s power). Ticking a group ticks every screen in it; Do and Approve include View.</p>
    <div class="rl-tree"><div class="rl-row rl-head"><span class="rl-l">Screen</span>${RL_P.map(([, l]) => `<span class="rl-cb">${l}</span>`).join('')}</div>` +
    (C.roles?.screens || []).map(sec => row('rl-sec', esc(sec.name), sec.items.flatMap(leaves), sec.admin ? ' ' + pill('Admin-level', 'warn') : '') +
      sec.items.map(it => it.tabs ? row('rl-grp', esc(it.label), leaves(it)) + it.tabs.map(t => row('rl-tab', esc(t.label), [t.key])).join('') : row('rl-scr', esc(it.label), [it.key])).join('')).join('') + '</div>';
}
function rlLimits() {
  const l = RL.d.limits || {}, fixed = RL.builtIn;
  const sc = [['firm', 'The whole firm'], ['branch', 'Own branch only'], ['ap', 'Own authorised person only']];
  return `<div class="form"><div class="fld full"><label>Branch scope</label><div class="rl-radio">${sc.map(([v, t]) => `<label class="check"><input type="radio" name="rl-scope" value="${v}" ${(l.scope || 'firm') === v ? 'checked' : ''} ${fixed ? 'disabled' : ''}> ${t}</label>`).join('')}</div><span class="dim small">${fixed ? 'A built-in role keeps its reach; copy it into a new role for another.' : 'A role limited to a branch or an authorised person reaches only the client-level screens, each showing only those clients.'}</span></div>
    <label class="check full"><input type="checkbox" id="rl-brok" ${l.brokerage ? 'checked' : ''}> May see brokerage totals</label>
    <label class="check full"><input type="checkbox" id="rl-reveal" ${l.reveal_ids ? 'checked' : ''}> May reveal full PAN, Aadhaar and bank numbers (each reveal is in the audit trail)</label>
    <label class="check full"><input type="checkbox" id="rl-export" ${l.export ? 'checked' : ''}> May download files and exports</label>
    <div class="fld"><label for="rl-pl">Payout approval limit (₹)</label><input class="in mono" id="rl-pl" value="${esc(l.payout_limit || '')}" placeholder="blank: no limit"><span class="dim small">The largest payout this role may approve on Receipts & payouts.</span></div></div>`;
}
function rlReview() {
  const c = RL.check;
  if (!c) return '<p class="loading">Checking…</p>';
  return `<h4 style="margin:0 0 6px">${esc(c.role.name)}</h4>${c.role.about ? `<p class="dim" style="margin:0 0 8px">${esc(c.role.about)}</p>` : ''}<ul class="rl-sum">${c.summary.map(l => `<li>${esc(l)}</li>`).join('')}</ul>${(c.warnings || []).length ? `<div class="note warn"><b>Before you send it</b><ul>${c.warnings.map(w => `<li>${esc(w)}</li>`).join('')}</ul></div>` : ''}<p class="dim small">Preview as this role opens the console showing only what the role would see, read-only. Send for approval: another Admin approves it before it takes effect.</p>`;
}
// rlRead takes the open step's fields into RL.d.
function rlRead() {
  const d = RL.d;
  if (RL.step === 0) { d.name = $('rl-name').value.trim(); d.about = $('rl-about').value.trim(); }
  if (RL.step === 2) d.limits = { scope: document.querySelector('[name=rl-scope]:checked')?.value || 'firm', brokerage: $('rl-brok').checked, reveal_ids: $('rl-reveal').checked, export: $('rl-export').checked, payout_limit: $('rl-pl').value.trim() };
}
async function rlGo(step) {
  rlRead();
  if (RL.step === 0 && step > 0 && !RL.d.name) { $('rl-err').innerHTML = errHTML({ message: 'Give the role a name' }); return; }
  RL.step = step; RL.check = null; rlDraw();
  if (step === 3) { try { RL.check = await post('/v1/roles/check', { role: RL.d }); RL.d = { ...RL.d, ...RL.check.role }; } catch (e) { RL.step = 2; rlDraw(); $('rl-err').innerHTML = errHTML(e); return; } rlDraw(); }
}
document.addEventListener('change', e => {
  const el = e.target;
  if (!el.closest('#rl-ed')) return;
  if (el.id === 'rl-tpl') { const t = rlRole(el.value), name = $('rl-name').value, about = $('rl-about').value; RL.d = t ? { ...rlCopyOf(t), name, about, template: t.name } : { name, about, template: '', screens: {}, limits: { scope: 'firm', brokerage: true, export: true } }; return; }
  if (el.dataset.rlkeys) {
    const p = el.dataset.p, on = el.checked, sc = RL.d.screens;
    for (const k of el.dataset.rlkeys.split(' ')) {
      const x = { ...(sc[k] || {}) }; x[p] = on;
      if (on && p !== 'view') x.view = true;
      if (!on && p === 'view') x.do = x.approve = false;
      if (x.view || x.do || x.approve) sc[k] = x; else delete sc[k];
    }
    const body = $('rl-body'), top = body.querySelector('.rl-tree')?.scrollTop || 0;
    body.innerHTML = rlScreens(); rlMixed(); const t = body.querySelector('.rl-tree'); if (t) t.scrollTop = top;
  }
});
function rlMixed() { document.querySelectorAll('#rl-ed [data-mixed]').forEach(x => { x.indeterminate = true; }); }

Object.assign(A, {
  rlNew: () => rlOpen('new', { name: '', about: '', template: '', screens: {}, limits: { scope: 'firm', brokerage: true, export: true } }),
  rlEdit: name => { const r = rlRole(name); if (r) { rlOpen('edit', rlCopyOf(r)); } },
  rlCopy: name => { const r = rlRole(name); if (!r) return; const d = rlCopyOf(r); d.name = 'Copy of ' + r.name; d.template = r.built_in ? r.name : (r.template || ''); rlOpen('new', d); },
  rlBack: () => rlGo(RL.step - 1),
  rlNext: () => rlGo(RL.step + 1),
  rlSend: () => act(() => post('/v1/roles', { role: RL.d }), c => `${c.id} sent: another Admin approves it before it takes effect`),
  rlPreviewDraft: () => rlStartPreview(RL.d),
  rlPreviewRole: name => rlStartPreview({ name }),
  rlEndPreview: async () => { S.preview = null; try { await post('/v1/roles/preview/end'); } catch (e) { toast(e.message, true); } await loadMe(); S.view = 'roles'; render(); },
  rlShow: name => { const r = rlRole(name); if (r) rlSummaryModal(r.name, r.summary, []); },
  rlShowChange: id => { const c = (C.roles?.changes || []).find(x => x.id === id); if (c) rlSummaryModal(c.name, c.summary, c.warnings); },
  rlReset: name => confirmModal('Reset ' + name + ' to its default', 'Everyone holding it goes back to what the built-in role does as shipped. Another Admin approves it first.', '', 'Send for approval', () => act(() => post('/v1/roles/reset', { name }), c => `${c.id} sent for approval`)),
  rlRetire: name => confirmModal('Retire ' + name, 'It can no longer be given. Nobody may hold it when it is retired. Another Admin approves it first.', '', 'Send for approval', () => act(() => post('/v1/roles/retire', { name }), c => `${c.id} sent for approval`), true),
  rlDecide: id => confirmModal('Approve ' + id, 'It takes effect at once and is recorded in the audit trail.', `<div class="fld"><label for="rl-note">Note (optional)</label><input class="in" id="rl-note"></div>`, 'Approve', () => act(() => post(`/v1/role-changes/${encodeURIComponent(id)}/decide`, { approve: true, note: $('rl-note').value }), c => `${c.id} approved`)),
  rlReject: id => confirmModal('Reject ' + id, 'Say why, so the Admin who proposed it knows.', `<div class="fld"><label for="rl-note">Reason</label><input class="in" id="rl-note"></div>`, 'Reject', () => act(() => post(`/v1/role-changes/${encodeURIComponent(id)}/decide`, { approve: false, note: $('rl-note').value }), c => `${c.id} rejected`), true),
  rlWithdraw: id => act(() => post(`/v1/role-changes/${encodeURIComponent(id)}/withdraw`), c => `${c.id} withdrawn`),
  rlAssign: async id => {
    let d = C.roles; if (!d) { try { d = C.roles = await get('/v1/roles'); } catch (e) { toast(e.message, true); return; } }
    const users = C.users?.users || d.people || [], u = users.find(x => x.id === id) || d.people.find(x => x.id === id); if (!u) return;
    const held = u.roles || rlRoles(u);
    confirmModal('Roles of ' + (u.name || id), 'What the person may do is everything any of their roles allows. The first ticked role is their main role. Another Admin approves the change before it takes effect.',
      `<div class="rl-assign">${d.roles.filter(r => r.name !== 'Client').map(r => `<label class="check"><input type="checkbox" data-rlrole="${esc(r.name)}" ${held.includes(r.name) ? 'checked' : ''}> ${esc(r.name)} <span class="dim small">${r.limits?.scope === 'branch' ? '· own branch' : r.limits?.scope === 'ap' ? '· own authorised person' : ''}</span></label>`).join('')}</div>
      <div class="form"><div class="fld"><label for="rl-br">Branch (roles limited to a branch)</label><input class="in" id="rl-br" value="${esc(u.branch || '')}"></div><div class="fld"><label for="rl-pt">Partner code (roles limited to an authorised person)</label><input class="in mono" id="rl-pt" value="${esc(u.partner || '')}"></div></div>`,
      'Send for approval', () => { const rs = [...document.querySelectorAll('[data-rlrole]:checked')].map(x => x.dataset.rlrole); const main = held.find(r => rs.includes(r)); if (main) rs.sort((a, b) => (b === main) - (a === main)); return act(() => post(`/v1/users/${encodeURIComponent(id)}/roles`, { roles: rs, branch: $('rl-br').value.trim(), partner: $('rl-pt').value.trim() }), c => `${c.id} sent: another Admin approves it`); });
  },
});

async function rlStartPreview(role) {
  try {
    const d = await post('/v1/roles/preview', { role });
    closeModal(); S.preview = d.name; await loadMe();
    toast('Previewing as ' + d.name + ': read-only');
    S.view = 'dashboard'; render();
  } catch (e) { const el = $('rl-err'); if (el) el.innerHTML = errHTML(e); else toast(e.message, true); }
}
