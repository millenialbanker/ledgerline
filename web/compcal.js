'use strict';
/* Ledgerline console: the compliance calendar.

   On Calendar & circulars:
   - Compliance calendar: every duty of the firm's calendar (Trading, DP,
     RA & IA), with:
     - whether it applies, read from the firm profile;
     - when it is next due;
     - how it is done: by API, by Ledgerline, as a file for a portal, on a
       portal, by an auditor or by the firm;
     - the circulars behind it, and the steps those circulars list.
   - Month sheet: the month's duties with their dates, and a CSV download.

   Server: internal/ops/compduties.go and circulars.go;
   internal/api/compliancecal.go. Names are prefixed dc. */

const DC_HOW = { api: ['By API', 'good'], ledgerline: ['By Ledgerline', 'good'], upload: ['File for the portal', 'acc'], portal: ['On the portal', 'warn'], auditor: ['Independent auditor', 'nt'], firm: ['The firm', 'nt'] };
const DC_API = { built: ['API in Ledgerline', 'good'], available: ['API exists · not connected yet', 'acc'], file: ['No API · file upload', 'nt'], portal: ['No API · portal only', 'nt'], none: ['Nothing to send', 'nt'] };
const DC_ST = { Overdue: 'bad', 'Due today': 'warn', 'Due this week': 'warn', Upcoming: 'acc', Done: 'good', 'Done by Ledgerline': 'good', 'Ledgerline check failing': 'bad', 'Doesn’t apply': 'nt', "Doesn't apply": 'nt', 'Switched off': 'nt', 'When it happens': 'nt', 'As required': 'nt', 'To confirm': 'warn' };
const DC_GROUPS = ['Trading', 'DP', 'RA & IA'];
const DC_FREQ = ['Daily', 'Weekly', 'Fortnightly', 'Monthly', 'Quarterly', 'Half-yearly', 'Yearly', 'Every 2 years', 'Settlement day', 'Monthly / quarterly', 'On event', 'One time', 'As required'];
Object.assign(UI, { dcG: 'All', dcHow: 'All', dcSt: 'Open', dcF: 'All', dcQ: '', dcMonth: '', dcNA: false });

const dcNA = x => x.applicability?.verdict === 'no';
const dcOpenDuty = x => !dcNA(x) && x.active;
const dcHowPill = x => x.how ? pill(...(DC_HOW[x.how] || [x.how, 'nt'])) : '';
const dcAPIPill = x => x.api ? pill(...(DC_API[x.api.status] || [x.api.status, 'nt'])) : '';
const dcStPill = x => pill(x.status || '—', DC_ST[x.status] || 'nt');
const dcDaily = x => /^TDAY/.test(x.rule || '') || x.rule === 'DAILY';
const dcDuty = id => (C.circulars?.duties || []).find(y => y.id === id);
const dcCan = x => has(x.role, 'Compliance officer');
const dcClip = (t, n) => t && t.length > n ? t.slice(0, n - 1) + '…' : (t || '');

/* ---------- the calendar tab ---------- */
function dcCalendarHTML(d) {
  const ds = d.duties || [], open = ds.filter(dcOpenDuty);
  const n = f => open.filter(f).length;
  const unsure = open.filter(x => x.applicability?.verdict === 'unsure');
  const qs = [...new Set(unsure.map(x => x.applicability.reason))];
  const howOpts = [['All', 'Any way'], ['api', 'By API (Ledgerline)'], ['apiavail', 'API exists, not connected'], ['ledgerline', 'By Ledgerline'], ['upload', 'File for the portal'], ['portal', 'On the portal'], ['auditor', 'Independent auditor'], ['firm', 'The firm']];
  return `<div class="bd dc-top">
    <div class="kpis">${kpi('On the calendar', nf(open.length), `${nf(ds.length - open.length)} don’t apply or are off`)}${kpi('Overdue', nf(n(x => x.overdue)), 'past their due date', n(x => x.overdue) ? 'bad' : 'good')}${kpi('Due in 7 days', nf(n(x => x.next_due && !x.overdue && x.next_due <= addDaysJS(S.date, 7))), 'with today’s', '')}${kpi('Done through an API', nf(n(x => x.api?.status === 'built')), 'Ledgerline sends them', 'good')}${kpi('API exists, not connected', nf(n(x => x.api?.status === 'available')), 'could move to an API', n(x => x.api?.status === 'available') ? 'acc' : '')}${kpi('By Ledgerline', nf(n(x => x.how === 'ledgerline')), 'statements, settlements, registers', '')}</div>
    ${qs.length ? `<div class="note warn"><b>So Ledgerline can tell what applies (${unsure.length} duties):</b> ${qs.slice(0, 4).map(esc).join('; ')}. <button class="linkish" data-act="go" data-id="settings">Settings → Firm profile</button></div>` : ''}
    <div class="tools dc-tools">${segBtns('dcG', [['All', 'All'], ...DC_GROUPS.map(g => [g, g])], 'dc-t')}
      ${segBtns('dcSt', [['Open', 'Applies'], ['Overdue', 'Overdue'], ['Week', 'Due in 7 days'], ['Daily', 'Daily checks'], ['Event', 'On event'], ['NA', 'Doesn’t apply'], ['All', 'All']], 'dc-t')}
      <select class="in" data-ui="dcHow" data-part="dc-t" aria-label="How it is done">${howOpts.map(([v, l]) => `<option value="${v}" ${UI.dcHow === v ? 'selected' : ''}>${l}</option>`).join('')}</select>
      <select class="in" data-ui="dcF" data-part="dc-t" aria-label="How often"><option value="All">Any frequency</option>${DC_FREQ.map(f => `<option ${UI.dcF === f ? 'selected' : ''}>${f}</option>`).join('')}</select>
      <input class="in" data-ui="dcQ" data-part="dc-t" placeholder="Filter: title, circular, portal" value="${esc(UI.dcQ)}" aria-label="Filter duties"></div></div>
    <div id="dc-t">${P['dc-t'](d)}</div>
    <div class="pad"><span class="dim small">Each duty carries the circulars that set it, the steps they list and how it is done. What doesn’t apply to the firm (from the firm profile) is not chased. Duties are queued for their owner a week before they fall due; the day’s checks go in one work item.</span></div>`;
}

P['dc-t'] = d => {
  const q = (UI.dcQ || '').trim().toLowerCase(), wk = addDaysJS(S.date, 7);
  const st = {
    Open: dcOpenDuty, Overdue: x => dcOpenDuty(x) && x.overdue, Week: x => dcOpenDuty(x) && x.next_due && x.next_due <= wk, Daily: x => dcOpenDuty(x) && dcDaily(x),
    Event: x => dcOpenDuty(x) && x.rule === 'EVENT', NA: x => dcNA(x) || !x.active, All: () => true
  }[UI.dcSt] || (() => true);
  const how = x => UI.dcHow === 'All' || (UI.dcHow === 'apiavail' ? x.api?.status === 'available' : UI.dcHow === 'api' ? x.api?.status === 'built' || x.how === 'api' : x.how === UI.dcHow);
  const hay = x => [x.title, x.authority, x.filed_with, x.filing, x.group, x.frequency, x.due_text, (x.items || []).join(' '), (x.refs || []).map(r => r.issuer + ' ' + r.no + ' ' + (r.title || '')).join(' '), x.api?.name || ''].join(' ').toLowerCase();
  const rows = (d.duties || []).filter(x => (UI.dcG === 'All' || (x.group || 'Trading') === UI.dcG) && st(x) && how(x) && (UI.dcF === 'All' || x.frequency === UI.dcF) && (!q || hay(x).includes(q)))
    .sort((a, b) => (DC_GROUPS.indexOf(a.group || 'Trading') - DC_GROUPS.indexOf(b.group || 'Trading')) || ((a.next_due || '9') < (b.next_due || '9') ? -1 : (a.next_due || '9') > (b.next_due || '9') ? 1 : 0) || a.title.localeCompare(b.title));
  return tbl([['Duty'], ['How often'], ['Next due'], ['How it is done'], ['Owner'], ['Last done'], ['']], rows.map(x => {
    const last = (x.done || [])[x.done.length - 1], na = dcNA(x);
    const due = x.next_due ? `${fmtDate(x.next_due)}${x.due_at ? ' ' + esc(x.due_at) : ''}${x.overdue ? '<div class="small neg">overdue</div>' : ''}<div class="dim small">${esc(x.period || '')}</div>` : `<span class="dim">${esc(x.status || '—')}</span>`;
    const acts = na ? '' : x.rule === 'EVENT' && dcCan(x) ? `<button class="btn sm" data-act="dcEvent" data-id="${esc(x.id)}">Record an event</button>${x.next_due ? `<button class="btn sm" data-act="dcDone" data-id="${esc(x.id)}">Mark done</button>` : ''}` : dcCan(x) && x.rule !== 'AUTO' && (x.next_due || x.period === 'once' || x.rule === 'ASNEEDED') ? `<button class="btn sm" data-act="dcDone" data-id="${esc(x.id)}">Mark done</button>` : '';
    return `<tr class="${na || !x.active ? 'dim' : ''}"><td class="wrap dc-duty"><button class="linkish" data-act="dcOpen" data-id="${esc(x.id)}"><b>${esc(x.title)}</b></button>
      <div class="small">${x.group ? `<span class="tag">${esc(x.group)}</span> ` : ''}${dcStPill(x)}${x.steps?.length ? ` <span class="tag">${x.steps.length} steps from the circular</span>` : ''}</div>
      ${x.filed_with || x.filing ? `<div class="small" title="${esc(x.filing || x.filed_with)}">${esc(dcClip(x.filing || x.filed_with, 130))}</div>` : ''}
      ${x.applicability && x.applicability.verdict !== 'applies' ? `<div class="small ${na ? 'dim' : 'warn-t'}">${esc(x.applicability.verdict === 'no' ? 'Doesn’t apply: ' : 'Not sure: ')}${esc(x.applicability.reason)}</div>` : ''}
      ${x.warning ? `<div class="small neg">${esc(x.warning)}</div>` : ''}${x.check ? `<div class="small ${x.check.status === 'fail' ? 'neg' : 'dim'}">Ledgerline’s check (${fmtDate(x.check.date)}): ${esc(x.check.summary || x.check.status)}</div>` : ''}</td>
      <td class="small wrap dc-often" title="${esc(x.due_text || '')}">${esc(x.frequency)}<div class="dim">${esc(dcClip(x.due_text, 120))}</div></td>
      <td class="n ${x.overdue ? 'neg' : ''}">${due}</td>
      <td class="small wrap dc-howc">${dcHowPill(x)}<div style="margin-top:3px">${dcAPIPill(x)}</div>${x.api?.name && x.api.status !== 'none' ? `<div class="dim" title="${esc(x.api.name)}">${esc(dcClip(x.api.name, 70))}</div>` : ''}</td>
      <td class="small">${esc(x.role)}</td>
      <td class="small wrap dc-last">${last ?`${fmtDate(last.at.slice(0, 10))} · ${esc(last.by)}${last.late ? ' ' + pill('late', 'warn') : ''}<div class="dim">${esc(last.evidence)}</div>` : '<span class="dim">never</span>'}</td>
      <td><div class="split">${acts}</div></td></tr>`;
  }), { tall: true, empty: (d.duties || []).length ? 'Nothing matches.' : 'No duties yet.' });
};

/* ---------- a duty's window ---------- */
function dcModal(id) {
  const x = dcDuty(id); if (!x) return;
  const na = dcNA(x), ap = x.applicability;
  const refs = (x.refs || []).map(r => `<li><b>${esc(r.issuer)} ${esc(r.no)}</b>${r.date ? ` · ${fmtDate(r.date)}` : ''}${r.title ? `<div>${esc(r.title)}</div>` : ''}${r.says ? `<div class="dim">${esc(r.says)}</div>` : ''}${r.url ? `<a class="small" href="${esc(r.url)}" target="_blank" rel="noopener noreferrer">Open the circular</a>` : ''}</li>`).join('');
  const steps = (x.steps || []).length ? `<h3 class="cc-h">Steps in the circular</h3><ol class="dc-steps">${x.steps.map(s => `<li>${esc(s)}</li>`).join('')}</ol><div class="dim small">From ${esc(x.steps_from)}</div>` : '';
  const practice = (x.practice || []).length ? `<h3 class="cc-h">${x.steps?.length ? 'Also' : 'How it is usually done'}</h3><div class="dim small">${x.steps?.length ? '' : 'The circulars set the requirement but list no steps; this is the usual practice, not the regulation.'}</div><ol class="dc-steps">${x.practice.map(s => `<li>${esc(s)}</li>`).join('')}</ol>` : '';
  const ll = x.ledgerline;
  const how = `<div class="dc-how"><div class="dc-pills">${dcHowPill(x)} ${dcAPIPill(x)}${x.api?.name ? ` <b>${esc(x.api.name)}</b>` : ''}</div>${x.api?.detail ? `<div class="small">${esc(x.api.detail)}</div>` : ''}${x.api?.url ? `<a class="small" href="${esc(x.api.url)}" target="_blank" rel="noopener noreferrer">API documentation</a>` : ''}
    ${ll && (ll.what || ll.screen) ? `<div class="note"><b>In Ledgerline:</b> ${esc(ll.what || '')}${ll.screen ? ` <button class="btn sm" data-act="go" data-id="${esc(ll.screen)}">Open ${esc(ll.screen_name || ll.screen)}</button>` : ''}</div>` : ''}
    ${x.check ? `<div class="small ${x.check.status === 'fail' ? 'neg' : ''}">Ledgerline’s nightly self-inspection (${esc(x.check.title || x.check.test)}) on ${fmtDate(x.check.date)}: <b>${esc(x.check.status)}</b>${x.check.summary ? ' · ' + esc(x.check.summary) : ''}</div>` : ''}</div>`;
  const events = x.rule === 'EVENT' ? `<h3 class="cc-h">Events</h3>${tbl([['Happened'], ['What'], ['Due'], ['Done']], (x.events || []).slice().reverse().map(e => `<tr><td class="n">${fmtDate(e.date)}${e.time ? ' ' + esc(e.time) : ''}</td><td class="small wrap">${esc(e.what)}<div class="dim">${esc(e.by)}</div></td><td class="n ${!e.done && e.due < S.date ? 'neg' : ''}">${fmtDate(e.due)}${e.due_at ? ' ' + esc(e.due_at) : ''}</td><td class="small wrap">${e.done ? `${fmtDate(e.done.at.slice(0, 10))} · ${esc(e.done.by)}<div class="dim">${esc(e.done.evidence)}</div>` : pill('Open', 'warn')}</td></tr>`), { empty: 'Nothing recorded yet.' })}` : '';
  const hist = tbl([['For'], ['Due'], ['Done'], ['Evidence']], (x.done || []).slice().reverse().slice(0, 20).map(r => `<tr><td class="small">${esc(r.period)}</td><td class="n">${r.due ? fmtDate(r.due) : '—'}</td><td class="small">${fmtDate(r.at.slice(0, 10))} · ${esc(r.by)}${r.late ? ' ' + pill('late', 'warn') : ''}</td><td class="small wrap">${esc(r.evidence)}</td></tr>`), { empty: 'Not done yet.' });
  const canDone = !na && dcCan(x) && x.rule !== 'AUTO' && (x.next_due || x.period === 'once' || x.rule === 'ASNEEDED');
  modal(mhd(esc(x.title), `${esc(x.group || 'Trading')} · ${esc(x.frequency)}${x.authority ? ' · ' + esc(dcClip(x.authority, 90)) : ''}`) + `<div class="bd cc-md dc-md">
    <div class="tools">${dcStPill(x)} ${x.next_due ? `<span class="tag">Next due ${fmtDate(x.next_due)}${x.due_at ? ' ' + esc(x.due_at) : ''} · ${esc(x.period || '')}</span>` : ''} ${(x.items || []).map(i => `<span class="tag" title="Item on the source calendar">${esc(i)}</span>`).join(' ')}</div>
    ${ap ? `<div class="note ${ap.verdict === 'no' ? '' : ap.verdict === 'unsure' ? 'warn' : ''}"><b>${esc({ applies: 'Applies', no: 'Doesn’t apply', unsure: 'Not sure' }[ap.verdict] || '')}:</b> ${esc(ap.reason)}${ap.overridden && ap.rules ? `<div class="dim small">Ledgerline’s rules said: ${esc({ applies: 'applies', no: 'doesn’t apply', unsure: 'not sure' }[ap.rules] || ap.rules)}${ap.rules_why ? ' (' + esc(ap.rules_why) + ')' : ''}</div>` : ''}${ap.verdict !== 'applies' && !ap.overridden ? ' <button class="linkish" data-act="go" data-id="settings">Firm profile</button>' : ''}</div>` : ''}
    ${(x.na_history || []).length ? `<details class="small"><summary>Decisions on whether it applies (${x.na_history.length})</summary><ul>${x.na_history.slice().reverse().map(h => `<li>${fmtDate(h.at.slice(0, 10))} · ${esc(h.by)}: not applicable, ${esc(h.reason)}${h.ended ? ` <span class="dim">· taken back ${fmtDate(h.ended.slice(0, 10))} by ${esc(h.ended_by)}</span>` : ''}</li>`).join('')}</ul></details>` : ''}
    ${x.changed ? `<div class="note warn"><b>Changed since the old calendar:</b> ${esc(x.changed)}</div>` : ''}
    ${x.warning ? `<div class="note warn">${esc(x.warning)}</div>` : ''}
    <div class="dc-grid"><div><small>Due</small><b>${esc(x.due_text || x.frequency)}</b></div><div><small>Filed with</small><b>${esc(x.filing || x.filed_with || '—')}</b></div><div><small>Owner</small><b>${esc(x.role)}</b></div><div><small>Keep as evidence</small><b>${esc(x.evidence)}</b></div></div>
    <h3 class="cc-h">How it is done</h3>${how}
    ${steps}${practice}
    <h3 class="cc-h">Circulars</h3><ul class="dc-refs">${refs || '<li class="dim">None recorded.</li>'}</ul>
    ${events}
    <h3 class="cc-h">Done</h3>${hist}
  </div><div class="ft">${has('Compliance officer') ? x.not_applicable ? `<button class="btn" data-act="dcApplies" data-id="${esc(x.id)}">It applies after all</button>` : `<button class="btn" data-act="dcNotApplies" data-id="${esc(x.id)}">Doesn’t apply to us</button>` : ''}${x.rule === 'EVENT' && dcCan(x) && !na ? `<button class="btn" data-act="dcEvent" data-id="${esc(x.id)}">Record an event</button>` : ''}${canDone ? `<button class="btn pri" data-act="dcDone" data-id="${esc(x.id)}">Mark done</button>` : ''}<button class="btn" data-act="close">Close</button></div>`);
}

/* ---------- the month's sheet ---------- */
function dcSheetHTML(d) {
  const sh = d.sheet; if (!sh) return '<div class="bd dim">The month sheet is not available to your role.</div>';
  const month = UI.dcMonth || S.date.slice(0, 7);
  const howL = r => r.how ? (DC_HOW[r.how] || [r.how])[0] : '';
  const parts = DC_GROUPS.map(g => {
    const rs = sh.rows.filter(r => r.group === g && (UI.dcNA || r.verdict !== 'no'));
    if (!rs.length) return '';
    return `<div class="panel dc-sheet"><div class="hd"><h3>${esc(g)} · compliance sheet, ${esc(sh.label)}</h3><small>${rs.length} duties</small></div>${tbl([['Sl', 'r'], ['Compliance'], ['Frequency'], ['Due (as the circular says)'], [`Dates in ${esc(sh.label)}`], ['Filed with'], ['How'], ['Owner'], ['Status']], rs.map((r, i) => `<tr class="${r.verdict === 'no' ? 'dim' : ''}"><td class="r">${i + 1}</td><td class="wrap dc-comp"><button class="linkish" data-act="dcOpen" data-id="${esc(r.id)}">${esc(r.title)}</button>${r.steps ? ` <span class="tag">${r.steps} steps</span>` : ''}${r.verdict && r.verdict !== 'applies' ? `<div class="dim small">${esc(r.applies)}</div>` : ''}</td><td class="small">${esc(r.frequency)}</td><td class="small wrap dc-due" title="${esc(r.due_text)}">${esc(dcClip(r.due_text, 120))}</td><td class="small wrap dc-dates">${esc(r.every || '')}${r.dues.length ? (r.every ? '<br>' : '') + r.dues.map(fmtDate).join(', ') : r.every ? '' : '<span class="dim">—</span>'}${r.done.length ? `<div class="dim">done ${r.done.map(x => fmtDate(x.at.slice(0, 10))).join(', ')}</div>` : ''}</td><td class="small wrap dc-fw" title="${esc(r.filed_with || '')}">${esc(dcClip(r.filed_with, 100))}</td><td class="small">${esc(howL(r))}${r.api === 'built' ? '<div>' + pill('API', 'good') + '</div>' : r.api === 'available' ? '<div>' + pill('API exists', 'acc') + '</div>' : ''}</td><td class="small">${esc(r.role)}</td><td>${pill(r.status || '—', DC_ST[r.status] || 'nt')}</td></tr>`))}</div>`;
  }).join('');
  return `<div class="bd"><div class="tools"><label class="small" for="dc-month">Month</label><input class="in" type="month" id="dc-month" value="${esc(month)}">
    <label class="check small"><input type="checkbox" id="dc-na" ${UI.dcNA ? 'checked' : ''}> Show what doesn’t apply</label>
    <button class="btn" data-act="dcCSV">Download CSV</button>
    <span class="dim small">${nf(sh.counts.applies || 0)} duties apply · ${nf(sh.counts.due_in_month || 0)} fall due this month · ${nf(sh.counts.api_built || 0)} by API in Ledgerline · ${nf(sh.counts.api_available || 0)} with an API not yet connected</span></div></div>${parts}`;
}

document.addEventListener('change', e => {
  const el = e.target;
  if (el.id === 'dc-month' && /^\d{4}-\d{2}$/.test(el.value)) { UI.dcMonth = el.value; render({ keep: true }); }
  if (el.id === 'dc-na') { UI.dcNA = el.checked; rerenderPart('ci-t'); }
});

/* ---------- actions ---------- */
Object.assign(A, {
  dcOpen: id => dcModal(id),
  dcDone: id => {
    const x = dcDuty(id); if (!x) return;
    const daily = dcDaily(x), what = x.rule === 'EVENT' ? `The open event: ${esc((x.events || []).find(e => 'event ' + e.id === x.period)?.what || '')}` : x.period === 'once' ? 'Once.' : x.rule === 'ASNEEDED' ? `Done on ${fmtDate(S.date)}.` : `For ${esc(x.period)}, due ${fmtDate(x.next_due)}.`;
    confirmModal('Mark done · ' + esc(x.title), `${what} Keep: ${esc(x.evidence)}.`,
      `<div class="fld"><label for="dc-ev">What was done and where the evidence is</label><input class="in" id="dc-ev"></div>${daily ? '<label class="check"><input type="checkbox" id="dc-all" checked> Every day still open up to today</label>' : ''}`,
      'Mark done', () => act(() => post(`/v1/duties/${encodeURIComponent(id)}/done`, { date: S.date, evidence: $('dc-ev').value, all: !!$('dc-all')?.checked }), 'Recorded'));
  },
  dcEvent: id => {
    const x = dcDuty(id); if (!x) return;
    const hours = /h$/.test(x.event_within || '') && !/wd$/.test(x.event_within || '');
    confirmModal('Record an event · ' + esc(x.title), `It is due ${esc(x.due_text || '')}; Ledgerline dates it from when it happened${hours ? ' (the hour counts)' : ''} and puts it on the owner’s work queue.`,
      `<div class="form"><div class="fld"><label for="dc-ed">When it happened (or was noticed)</label><input class="in" type="date" id="dc-ed" value="${esc(S.date)}" max="${esc(S.date)}"></div>${hours ? '<div class="fld"><label for="dc-et">Time</label><input class="in" type="time" id="dc-et"></div>' : ''}<div class="fld"><label for="dc-edue">Due by (only if the notice gives a date)</label><input class="in" type="date" id="dc-edue"></div><div class="fld full"><label for="dc-ew">What happened</label><input class="in" id="dc-ew" placeholder="For example: a new director appointed on the board"></div></div>`,
      'Record', () => act(() => post(`/v1/duties/${encodeURIComponent(id)}/events`, { on: S.date, date: $('dc-ed').value, time: $('dc-et')?.value || '', due: $('dc-edue').value || '', what: $('dc-ew').value }), 'Recorded: it is on the work queue'));
  },
  dcNotApplies: id => {
    const x = dcDuty(id); if (!x) return;
    confirmModal('Doesn’t apply to us · ' + esc(x.title), 'The duty stays in Ledgerline for other firms. For your firm it is shown as not applicable and is not chased. Your name, the date and the reason are kept with the duty and in the audit trail.',
      '<div class="fld"><label for="dc-na-why">Why it doesn’t apply (for example the circular that withdrew it)</label><input class="in" id="dc-na-why"></div>',
      'Mark not applicable', () => act(() => post(`/v1/duties/${encodeURIComponent(id)}/applies`, { on: S.date, applies: false, reason: $('dc-na-why').value }), 'Marked not applicable'));
  },
  dcApplies: id => {
    const x = dcDuty(id); if (!x) return;
    confirmModal('It applies after all · ' + esc(x.title), 'Ledgerline’s rules decide again whether the duty applies, and it is chased when due. The earlier decision stays in the duty’s history.', '',
      'It applies', () => act(() => post(`/v1/duties/${encodeURIComponent(id)}/applies`, { on: S.date, applies: true }), 'Back on the calendar'));
  },
  dcCSV: () => download(`/v1/duties/sheet?month=${encodeURIComponent(UI.dcMonth || S.date.slice(0, 7))}&date=${S.date}&format=csv`, `compliance-sheet-${UI.dcMonth || S.date.slice(0, 7)}.csv`),
});
