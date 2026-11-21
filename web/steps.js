'use strict';
/* Ledgerline console: Steps & reminders (screen "steps", under Automation).
   Every step of the day, week and month with its due-by time (IST) and
   owner, whether it is done, and what is pending from earlier days. A step
   not done by its time is reminded to its owner in Ledgy with a work item,
   then escalated (ops/stepremind.go). Here a person marks a step done by
   hand outside Ledgerline (with a note) or skips it with a reason; a step
   with exclusion criteria, such as the daily interest run, shows them and
   is confirmed "run with these exemptions" in one click. The Settings tab
   is where an Admin sets due times, owners, escalation and the firm's own
   steps. Loaded after app.js. */

Object.assign(UI, { stTab: 'today', stKind: '' });
const ST_STATE = { upcoming: ['Not yet', 'nt'], overdue: ['Overdue', 'warn'], reminded: ['Reminded', 'warn'], escalated: ['Escalated', 'bad'], done: ['Done', 'good'], by_hand: ['Done by hand', 'good'], skipped: ['Skipped', 'nt'] };
const ST_KIND = { autopilot: 'Autopilot', periodic: 'Periodic work', file: 'Daily files', manual: 'Manual' };
const ST_DAYS = [['business', 'Business days'], ['business+sat', 'Business days and Saturdays'], ...['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'].map(d => ['weekly:' + d, 'Every ' + d]), ...[1, 5, 7, 10, 15].map(n => ['monthly:' + n, `Monthly on the ${n}${n === 1 ? 'st' : 'th'}`])];
const stPill = s => pill(...(ST_STATE[s.state] || [s.status || '', 'nt']));
const stOpen = s => !['done', 'by_hand', 'skipped'].includes(s.state);
const stTime = due => !due ? '' : due.slice(0, 10) === S.date ? due.slice(11) : fmtDate(due.slice(0, 10)) + ' ' + due.slice(11);
const stItem = id => [...(C.steps?.steps || []), ...(C.steps?.pending || [])].find(x => x.id === id);
// stCan: the owner's role, the person named, or the Admin (the server checks too).
const stCan = s => has(s.role) || S.me?.actor === s.owner;
const stWho = (v, users) => { const u = (users || []).find(x => x.id === v); return u ? `${u.name} (${u.role})` : v; };

V.steps = {
  load: async () => {
    const [d, st] = await Promise.all([get(`/v1/steps?date=${S.date}`), get('/v1/steps/settings').catch(() => null)]);
    d.settings = st;
    UI.stCustom = (st?.custom || []).map(x => ({ ...x }));
    return d;
  },
  html: d => {
    const c = d.counts || {}, open = d.steps.filter(stOpen).length;
    const late = (c.overdue || 0) + (c.reminded || 0) + (c.escalated || 0);
    const acts = has('Ops checker', 'Risk manager', 'Compliance officer') ? '<button class="btn" data-act="stCheck" title="Remind and escalate what is late now, and close what is done">Check now</button>' : '';
    return ph('Steps & reminders', 'Every step of the day with its due-by time and owner. A step not done by its time is reminded to its owner, then escalated. What is not done stays pending until it is done, done by hand or skipped.', acts) +
      (d.off ? '<div class="note warn"><b>Step reminders are switched off.</b> An Admin switches them on in the Settings tab.</div>' : '') +
      (!d.working ? `<div class="note">${fmtDate(d.date)} is not a business day${d.holiday ? ' (' + esc(d.holiday) + ')' : ''}. Only steps that run on such days are listed, and nothing else is reminded.</div>` : '') +
      `<div class="kpis">${kpi('Steps today', d.steps.length, `${d.steps.length - open} done`)}${kpi('Late', late, `${c.escalated || 0} escalated`, late ? (c.escalated ? 'bad' : 'warn') : 'good')}${kpi('Still to do', open - late, 'not due yet')}${kpi('Pending from earlier days', d.pending.length, d.pending.length ? 'carried over until done or skipped' : 'nothing carried over', d.pending.length ? 'warn' : 'good')}</div>` +
      `<div class="panel"><div class="hd">${segBtns('stTab', [['today', 'Today'], ['pending', 'Pending from earlier days' + (d.pending.length ? ' · ' + d.pending.length : '')], ['settings', 'Settings']])}</div></div>` +
      ({ today: stToday, pending: stPending, settings: stSettings }[UI.stTab] || stToday)(d);
  }
};

function stRow(s, withDate) {
  const ex = s.exceptions, users = C.steps?.settings?.users;
  const state = `${stPill(s)}${s.reminded_at ? `<div class="dim small">reminded ${esc(stTime(s.reminded_at))}</div>` : ''}${s.escalated_at ? `<div class="dim small">to ${esc(stWho(s.escalated_to, users))} ${esc(stTime(s.escalated_at))}</div>` : ''}` +
    `${s.done_note ? `<div class="dim small wrap">${esc(s.done_note)}</div>` : ''}${s.note ? `<div class="dim small wrap">${esc(s.note)}</div>` : ''}${s.reason ? `<div class="dim small wrap">Skipped: ${esc(s.reason)}</div>` : ''}${s.by && !String(s.by).includes(':') ? `<div class="dim small">by ${esc(s.by)}</div>` : ''}`;
  const can = stOpen(s) && stCan(s);
  const btns = [
    s.view ? `<button class="btn sm" data-act="go" data-id="${esc(s.view)}">Open</button>` : '',
    can && s.has_exceptions && ex ? `<button class="btn sm pri" data-act="stExc" data-id="${esc(s.id)}">Check exemptions</button>` : '',
    can ? `<button class="btn sm" data-act="stDone" data-id="${esc(s.id)}">Done by hand</button>` : '',
    can ? `<button class="btn sm" data-act="stSkip" data-id="${esc(s.id)}">Skip</button>` : '',
  ].join('');
  return `<tr>${withDate ? `<td class="n">${fmtDate(s.date)}</td>` : ''}<td class="n mono">${esc(stTime(s.due))}</td>
    <td><b>${esc(s.name)}</b><div class="dim small">${esc(ST_KIND[s.kind] || s.kind)} · ${esc(s.area)}${s.kind === 'periodic' ? ' · ' + esc(s.key) : ''}</div>${ex && stOpen(s) ? `<div class="small ${ex.rows.length ? 'warn' : 'dim'} wrap" style="margin-top:4px">${esc(ex.summary)}</div>` : ''}${s.confirmed ? `<div class="small dim wrap">Confirmed with ${s.confirmed.clients.length} exemption${s.confirmed.clients.length === 1 ? '' : 's'} by ${esc(s.confirmed.by)}</div>` : ''}</td>
    <td>${esc(stWho(s.owner, users))}</td><td>${state}</td><td><div class="split">${btns}</div></td></tr>`;
}

function stToday(d) {
  const rows = d.steps.filter(s => !UI.stKind || s.kind === UI.stKind);
  return `<div class="panel"><div class="hd"><h3>Steps for ${fmtDate(d.date)}</h3>${stKindBtns()}</div>
    ${tbl([['Due (IST)', 'n'], ['Step'], ['Owner'], ['State'], ['']], rows.map(s => stRow(s, false)), { tall: true, empty: 'No steps on this day.' })}</div>`;
}

function stPending(d) {
  return `<div class="panel"><div class="hd"><h3>Pending from earlier days</h3><small>Each stays here until it is done, done by hand or skipped with a reason</small></div>
    ${tbl([['Day', 'n'], ['Due (IST)', 'n'], ['Step'], ['Owner'], ['State'], ['']], d.pending.map(s => stRow(s, true)), { tall: true, empty: 'Nothing is pending from earlier days.' })}</div>`;
}

function stKindBtns() {
  return `<div class="seg">${[['', 'All'], ...Object.entries(ST_KIND)].map(([k, l]) => `<button class="${UI.stKind === k ? 'on' : ''}" data-act="stKind" data-id="${k}">${l}</button>`).join('')}</div>`;
}

function stOwnerSel(id, val, st, dis) {
  const opts = [...st.roles.map(r => [r, r]), ...st.users.map(u => [u.id, `${u.name} (${u.role})`])];
  return `<select class="in sm" id="${id}" ${dis}>${opts.map(([v, l]) => `<option value="${esc(v)}" ${v === val ? 'selected' : ''}>${esc(l)}</option>`).join('')}</select>`;
}

function stSettings(d) {
  const st = d.settings;
  if (!st) return '<div class="note">Step settings could not be loaded.</div>';
  const admin = has(), dis = admin ? '' : 'disabled';
  const rows = st.rows.filter(r => !r.custom).map((r, i) => `<tr data-strow="${esc(r.id)}" ${UI.stKind && r.kind !== UI.stKind ? 'hidden' : ''} data-kind="${r.kind}">
    <td><b>${esc(r.name)}</b><div class="dim small">${esc(ST_KIND[r.kind] || r.kind)} · ${esc(r.area)} · ${esc((ST_DAYS.find(x => x[0] === r.days) || [, r.days])[1])}${r.exceptions ? ' · shows its exemptions' : ''}</div></td>
    <td><input class="in sm mono" type="time" id="st-due-${i}" value="${esc(r.eff_due)}" ${dis} aria-label="Due by"><div class="dim small">default ${esc(r.due)}</div></td>
    <td>${stOwnerSel('st-own-' + i, r.owner, st, dis)}<div class="dim small">default ${esc(r.role)}</div></td>
    <td><input class="in sm" type="number" min="1" max="600" id="st-esc-${i}" value="${r.escalate_min}" ${dis} style="width:80px" aria-label="Escalate after minutes"></td>
    <td><label class="check"><input type="checkbox" id="st-on-${i}" ${r.on ? 'checked' : ''} ${dis}> Remind</label></td></tr>`);
  const custom = (UI.stCustom || []).map((c, i) => `<tr><td><b>${esc(c.name)}</b>${c.about ? `<div class="dim small wrap">${esc(c.about)}</div>` : ''}</td><td class="mono">${esc(c.due)}</td><td>${esc((ST_DAYS.find(x => x[0] === c.days) || [, c.days])[1])}</td><td>${esc(stWho(c.role, st.users))}</td><td>${admin ? `<button class="btn sm" data-act="stRemove" data-id="${i}">Remove</button>` : ''}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Reminders</h3><small>${st.updated_by ? 'last changed by ' + esc(st.updated_by) + ' ' + esc(fmtTS(st.updated_at)) : 'defaults'}</small></div><div class="bd"><div class="form">
      <label class="check full"><input type="checkbox" id="st-off" ${st.off ? 'checked' : ''} ${dis}> Switch every step reminder off</label>
      <div class="fld"><label for="st-escto">Escalate to (the Admin or the head of the desk)</label>${stOwnerSel('st-escto', st.escalate_to || 'Admin', st, dis)}</div>
    </div><p class="dim small">Times are IST. A step is reminded once a day at most, only on the days it runs: business days of the exchange holiday calendar and the firm's holidays, and Saturdays for the steps that run on Saturdays. Escalation follows the reminder after the number of minutes set for the step.</p></div></div>
    <div class="panel"><div class="hd"><h3>Due times and owners</h3>${stKindBtns()}</div>${tbl([['Step'], ['Due by'], ['Owner'], ['Escalate after (min)'], ['']], rows, { tall: true })}</div>
    <div class="panel"><div class="hd"><h3>The firm's own steps</h3><small>done by hand: Ledgerline reminds, the owner marks them done</small>${admin ? '<button class="btn sm" data-act="stAdd">Add a step</button>' : ''}</div>${tbl([['Step'], ['Due by'], ['Days'], ['Owner'], ['']], custom, { empty: 'None yet.' })}</div>
    ${admin ? '<div class="tools"><button class="btn pri" data-act="stSave">Save settings</button></div>' : '<div class="dim small">Only an Admin changes these.</div>'}`;
}

/* The Settings screen: where step reminders are set. */
function stepSettingsPanel() {
  return `<div class="panel"><div class="hd"><h3>Step reminders</h3><small>due-by times, owners and escalation</small></div><div class="bd">
    <p class="small" style="margin-top:0">Every step of the day, week and month has a due-by time and an owner. Change them, the escalation and the firm's own steps on Steps &amp; reminders.</p>
    <button class="btn sm" data-act="stSettingsGo">Open step settings</button></div></div>`;
}

function stExcModal(s) {
  const ex = s.exceptions;
  const rows = ex.rows.map(r => `<tr><td><button class="linkish mono" data-act="client" data-id="${esc(r.ucc)}">${esc(r.ucc)}</button><div class="dim small">${esc(r.name)}</div></td><td class="small wrap">${esc(r.why)}</td><td class="r">${inr(r.debit)}</td><td class="n">${r.days}</td><td class="r"><b>${inr(r.amount)}</b></td></tr>`);
  confirmModal(esc(ex.title), esc(s.name) + ' · ' + (ex.from === ex.to ? fmtDate(ex.to) : fmtDate(ex.from) + ' to ' + fmtDate(ex.to)),
    `<p class="small" style="margin-top:0">${esc(ex.summary)}</p>${ex.rows.length ? tbl([['Client'], ['Why'], ['Overdue debit', 'r'], ['Days', 'n'], ['Not charged', 'r']], rows, { foot: `<tfoot><tr><td colspan="4">Total (plus ₹${esc(String(ex.gst))} GST)</td><td class="r"><b>${inr(ex.amount)}</b></td></tr></tfoot>` }) : ''}
     <p class="dim small">Every other client in overdue debit is charged. To change who is exempt, change the client's interest terms or the exempt groups in Settings first, then come back.</p>
     <div class="fld"><label for="st-cn">Note (optional)</label><input class="in" id="st-cn" placeholder="e.g. As agreed with the MD"></div>`,
    esc(ex.confirm), () => act(() => post('/v1/steps/confirm', { id: s.id, fingerprint: ex.fingerprint, note: $('st-cn').value }), r => 'Done: ' + (r.done_note || r.name)));
}

Object.assign(A, {
  stCheck: () => act(() => post('/v1/steps/check', {}), r => r.skipped ? r.skipped : `${r.reminded.length} reminded, ${r.escalated.length} escalated, ${r.closed.length} closed as done`),
  stKind: id => {
    UI.stKind = id;
    if (UI.stTab === 'settings') {
      document.querySelectorAll('[data-strow]').forEach(tr => { tr.hidden = !!id && tr.dataset.kind !== id; });
      document.querySelectorAll('.seg [data-act="stKind"]').forEach(b => b.classList.toggle('on', b.dataset.id === id));
      return;
    }
    $('page').innerHTML = groupBar(S.view) + V.steps.html(C.steps); bindPage();
  },
  stDone: id => {
    const s = stItem(id); if (!s) return;
    confirmModal('Done by hand outside Ledgerline', esc(s.name) + ' · ' + fmtDate(s.date), '<div class="fld"><label for="st-dn">How was it done?</label><input class="in" id="st-dn" placeholder="e.g. Uploaded on the NSE portal at 22:10; acknowledgement 88213"></div>',
      'Mark done', () => act(() => post('/v1/steps/done', { id, note: $('st-dn').value }), 'Marked done by hand'));
  },
  stSkip: id => {
    const s = stItem(id); if (!s) return;
    confirmModal('Skip this step', esc(s.name) + ' · ' + fmtDate(s.date) + '. It leaves the pending list. The reason is kept in the audit trail.', '<div class="fld"><label for="st-sr">Why is it skipped?</label><input class="in" id="st-sr" placeholder="e.g. No payouts today: nothing to upload"></div>',
      'Skip', () => act(() => post('/v1/steps/skip', { id, reason: $('st-sr').value }), 'Skipped'));
  },
  stExc: id => { const s = stItem(id); if (s?.exceptions) stExcModal(s); },
  stPendingGo: () => { UI.stTab = 'pending'; A.go('steps'); },
  stSettingsGo: () => { UI.stTab = 'settings'; A.go('steps'); },
  stAdd: () => {
    const st = C.steps?.settings; if (!st) return;
    confirmModal('Add the firm’s own step', 'Ledgerline reminds the owner at the due time; the owner marks it done by hand.',
      `<div class="form"><div class="fld full"><label for="sa-n">Step</label><input class="in" id="sa-n" placeholder="e.g. Count the petty cash"></div>
       <div class="fld full"><label for="sa-a">What to do (optional)</label><input class="in" id="sa-a"></div>
       <div class="fld"><label for="sa-d">Due by (IST)</label><input class="in mono" type="time" id="sa-d" value="10:00"></div>
       <div class="fld"><label for="sa-y">Days</label><select class="in" id="sa-y">${ST_DAYS.map(([v, l]) => `<option value="${v}">${esc(l)}</option>`).join('')}</select></div>
       <div class="fld full"><label for="sa-o">Owner</label>${stOwnerSel('sa-o', 'Ops maker', st, '')}</div></div>`,
      'Add', () => {
        const name = $('sa-n').value.trim(); if (!name) throw new Error('Give the step a name');
        UI.stCustom.push({ name, about: $('sa-a').value.trim(), due: $('sa-d').value, days: $('sa-y').value, role: $('sa-o').value });
        closeModal(); $('page').innerHTML = groupBar(S.view) + V.steps.html(C.steps); bindPage(); toast('Added: save the settings to keep it');
      });
  },
  stRemove: i => { UI.stCustom.splice(+i, 1); $('page').innerHTML = groupBar(S.view) + V.steps.html(C.steps); bindPage(); },
  stSave: () => {
    const st = C.steps?.settings; if (!st) return;
    const steps = {};
    st.rows.filter(r => !r.custom).forEach((r, i) => {
      const due = $('st-due-' + i)?.value || r.eff_due, owner = $('st-own-' + i)?.value || r.owner, esc_ = parseInt($('st-esc-' + i)?.value, 10) || 30, on = !!$('st-on-' + i)?.checked;
      const x = {};
      if (due !== r.due) x.due = due;
      if (owner !== r.role) x.owner = owner;
      if (esc_ !== 30) x.escalate_min = esc_;
      if (on === !!r.quiet) x.off = !on;
      if (Object.keys(x).length) steps[r.id] = x;
    });
    const esc2 = $('st-escto').value;
    act(() => api('PUT', '/v1/steps/settings', { off: $('st-off').checked, escalate_to: esc2 === 'Admin' ? '' : esc2, steps, custom: UI.stCustom }), 'Step settings saved');
  },
});
