'use strict';
/* Ledgerline staff app (/m/): approvals waiting for me, my work queue, the
   day's progress with alerts and exceptions, a client look-up (masked as on
   the desktop) and Ask Ledgy. It follows the console's Phone approvals
   layout (ly-phone.js, ly-phone.css: the same cards, segments and facts)
   but stands alone and calls only /v1/m/* (each checks the staff app
   setting, the role and the branch scope on the server), /v1/ledgy/ask and
   /v1/mobile/*. Loaded after mshell.js. */

MS.title = 'Ledgerline';
MS.sub = () => MS.cfg?.firm || 'Staff app';
MS.tabs = [['approvals', 'Approvals', 'approve'], ['work', 'Work', 'work'], ['today', 'Today', 'today'], ['clients', 'Clients', 'clients'], ['more', 'More', 'more']];
const ST = { home: null, ap: null, work: null, cl: [], q: '', seg: 'wait', ledgy: [] };
MS.badge = k => k === 'approvals' ? ST.home?.approvals || 0 : k === 'work' ? ST.home?.overdue || 0 : 0;
MS.onReady = () => { homeCounts(); hrDetect(); };
// Whether the staff app is open to this person ('' if it is).
MS.access = async () => { try { ST.home = await get('/v1/m/home'); return ''; } catch (e) { return e.status === 403 ? String(e.message).replace(/^[^:]*: /, '').replace(/^./, c => c.toUpperCase()) + '.' : ''; } };
async function homeCounts() { try { ST.home = await get('/v1/m/home?date=' + MS.day()); const sc = MS.screens[String(MS.route).split(':')[0]]; if (sc && !MS.locked) renderChrome(sc, String(MS.route).split(':')[0]); } catch (e) {} }

const dueText = w => w.overdue ? 'overdue' : w.due ? (w.due.slice(0, 10) === MS.day() ? 'by ' + w.due.slice(11) : fmtDate(w.due.slice(0, 10))) : '';
function card(w, route, done) {
  return `<${done ? 'div' : 'button'} class="ph-card ${w.overdue && !done ? 'od' : ''}" ${done ? '' : `data-go="${esc(route)}"`}>
    <span class="ph-k">${esc([w.area === 'Other' ? '' : w.area, w.action ? 'prepared by Autopilot' : w.process].filter(Boolean).join(' · '))}<span>${done ? esc(w.status) : esc(dueText(w))}</span></span>
    <b>${esc(w.title)}</b>${w.amount && !w.title.includes(w.amount) ? `<span class="m-amt">${esc(w.amount)}</span>` : ''}
    <span class="ph-sub">${esc(done ? [w.closed_by && 'by ' + w.closed_by, w.closed && w.closed.slice(11, 16), w.note].filter(Boolean).join(' · ') : w.action ? w.action + ' · nothing happens until you approve' : w.detail || 'Open to deal with it')}</span></${done ? 'div' : 'button'}>`;
}
function itemPage(w, back) {
  const named = !w.ucc && /^[A-Z]{1,4}\d{3,}(,\s*[A-Z]{1,4}\d{3,})*$/.test(String(w.detail || '').trim());
  return `<div class="ph-pg ph-d">
    <span class="ph-k">${esc([w.area === 'Other' ? '' : w.area, w.action ? 'prepared by Autopilot' : w.process].filter(Boolean).join(' · '))}</span>
    <h1>${esc(w.title)}</h1>
    ${w.action ? `<div class="ph-big">${esc(w.action)}</div>` : ''}${w.amount ? `<div class="m-amt big">${esc(w.amount)}</div>` : ''}
    ${w.detail && !named ? `<p class="ph-p">${esc(w.detail)}</p>` : ''}
    ${facts([w.ucc && row('Client', `<button class="linkish mono" data-act="findClient" data-id="${esc(w.ucc)}">${esc(w.ucc)}</button>`), named && row('Clients', esc(w.detail), 'mono'),
      row('Due', w.overdue ? pill('Overdue · ' + (w.due || '').slice(11), 'bad') : esc(w.due || '—')), row('For', esc(w.role || 'Anyone') + (w.can_act ? '' : ' <span class="dim">(not you)</span>')), row('From', esc(w.process))])}
    <div class="ph-acts">
      ${w.can_act && w.action ? `<button class="btn pri ph-btn" data-act="approve" data-id="${esc(w.id)}">${/^approve\b/i.test(w.action) ? esc(w.action) : 'Approve: ' + esc(w.action)}</button><button class="btn ph-btn" data-act="reject" data-id="${esc(w.id)}">Reject, with a reason</button>` : ''}
      ${w.can_act && !w.action ? `<button class="btn ph-btn" data-act="closeItem" data-id="${esc(w.id)}">Close with a note</button><p class="dim small">Anything else is done on the ${esc(w.process)} screen on a computer.</p>` : ''}
      ${!w.can_act ? `<p class="dim">Waiting for ${esc(w.role)}.</p>` : ''}
      <button class="btn ph-btn" data-act="askWhy" data-id="${esc(w.id)}">${svg('ledgy', 16)} Ask Ledgy why</button>
    </div>
    <p class="dim small">${w.action ? 'Approving carries out the prepared action. ' : ''}Your name and the time go into the audit trail.</p></div>`;
}
const findItem = id => [...(ST.ap?.waiting || []), ...(ST.work || [])].find(x => x.id === id);

MS.screens = {
  approvals: {
    load: async () => { ST.ap = await get('/v1/m/approvals?date=' + MS.day()); return ST.ap; },
    html: d => {
      const seg = ST.seg, list = seg === 'done' ? d.done : d.waiting;
      return `<div class="ph-pg"><h1>Approvals</h1>${hrBar()}
        <div class="ph-seg" role="tablist"><button role="tab" aria-selected="${seg === 'wait'}" class="${seg === 'wait' ? 'on' : ''}" data-act="seg" data-id="wait">Waiting · ${d.waiting.length}</button><button role="tab" aria-selected="${seg === 'done'}" class="${seg === 'done' ? 'on' : ''}" data-act="seg" data-id="done">Done today${d.done.length ? ' · ' + d.done.length : ''}</button></div>
        <div class="ph-list">${list.length ? list.slice(0, 80).map(w => card(w, 'approval:' + w.id, seg === 'done')).join('') : empty(seg === 'done' ? 'Nothing approved or rejected today yet.' : '<b>Nothing is waiting for you.</b><br>Anything Autopilot prepares for your approval shows here, and your phone tells you when it comes.')}</div></div>`;
    },
  },
  approval: { tab: 'approvals', back: 'approvals', load: async id => findItem(id) || (await MS.screens.approvals.load()).waiting.find(x => x.id === id), html: w => w ? itemPage(w) : empty('This item was dealt with already.') },
  work: {
    load: async () => { ST.work = (await get('/v1/m/work')).items; return ST.work; },
    html: d => `<div class="ph-pg"><h1>My work</h1><p class="dim">Items for your role that need doing on a screen. Overdue first.</p>
      <div class="ph-list">${d.length ? d.slice(0, 80).map(w => card(w, 'item:' + w.id)).join('') : empty('<b>Your work queue is empty.</b>')}</div></div>`,
  },
  item: { tab: 'work', back: 'work', load: async id => findItem(id) || (await MS.screens.work.load()).find(x => x.id === id), html: w => w ? itemPage(w) : empty('This item was dealt with already.') },
  today: {
    load: async () => { const [day, al] = await Promise.all([get('/v1/m/day?date=' + MS.day()), get('/v1/m/alerts?date=' + MS.day())]); return { day, al: al.alerts }; },
    html: ({ day, al }) => {
      const ex = al.filter(a => a.kind !== 'alert'), sv = al.filter(a => a.kind === 'alert');
      const alerts = `<section class="ph-glance" aria-label="Alerts and exceptions"><h2>Alerts and exceptions</h2>${ex.length ? `<dl>${ex.map(a => row(esc(a.title), a.count ? `<span class="${a.severity === 'High' ? 'neg' : a.severity === 'Medium' ? 'warnc' : ''}">${nf(a.count)}</span>` : '', '')).join('')}</dl>` : '<p class="dim" style="margin:4px 0">Nothing needs anyone right now.</p>'}
        ${sv.length ? linkRow(`Surveillance alerts · ${sv.length} open`, 'alerts') : ''}</section>`;
      if (day.scoped) return `<div class="ph-pg"><h1>Today</h1><p class="dim">${fmtDate(day.date)} · your clients</p>${alerts}</div>`;
      return `<div class="ph-pg"><h1>Today</h1><p class="dim">${fmtDate(day.date)} · ${day.done} of ${day.all} steps done${day.needs ? ` · ${day.needs} need someone` : ''}</p>${alerts}
        ${day.glance ? `<section class="ph-glance" aria-label="Today at a glance"><h2>Today at a glance</h2><dl>${day.glance.map(([k, v]) => row(esc(k), esc(v))).join('')}</dl></section>` : ''}
        ${day.sessions.map(([s, name, when]) => { const xs = day.steps.filter(x => x.session === s); return xs.length ? `<section class="ph-sess"><h2>${esc(name)} <span>${esc(when)}</span></h2>${xs.map(x => `<div class="ph-step"><span class="ly-mk ${x.state}" aria-label="${esc({ done: 'Done', needs: 'Needs someone', wait: 'Not yet', na: 'Not today' }[x.state])}">${x.state === 'done' ? '<svg viewBox="0 0 12 12" aria-hidden="true"><path d="m3 6.2 2 2 4-4.4"/></svg>' : ''}</span><span><b>${esc(x.name)}</b><small class="${x.state === 'needs' ? 'neg' : ''}">${esc(x.note)}</small></span><i class="dim small">${esc(x.at)}</i></div>`).join('')}</section>` : ''; }).join('')}</div>`;
    },
  },
  alerts: {
    tab: 'more', back: 'today',
    load: async () => (await get('/v1/m/alerts?date=' + MS.day())).alerts.filter(a => a.kind === 'alert'),
    html: d => `<div class="ph-pg"><h1>Surveillance alerts</h1><p class="dim">Open and under review. Review, close or file an STR on the Surveillance screen on a computer.</p>
      <div class="ph-list">${d.map(a => `<div class="ph-card info"><span class="ph-k">${esc(a.severity)}<span>${fmtDate(a.date)}</span></span><b>${esc(a.title)}</b><span class="ph-sub">${a.ucc ? `<span class="mono">${esc(a.ucc)}</span> · ` : ''}${esc(a.detail)}</span></div>`).join('') || empty('No open alerts.')}</div></div>`,
  },
  clients: {
    html: () => `<div class="ph-pg"><h1>Clients</h1><label class="ph-search">${svg('clients', 18)}<input class="in" id="m-q" placeholder="Name, UCC or PAN" value="${esc(ST.q)}" autocomplete="off" spellcheck="false" aria-label="Find a client"></label><div id="m-cl">${clientRows()}</div>
      <p class="dim small">You see the clients you would see on the desktop. PAN, mobile number and email are masked.</p></div>`,
    after: () => { const q = $('m-q'); q.oninput = () => { ST.q = q.value; clearTimeout(ST.t); ST.t = setTimeout(findClients, 300); }; if (!ST.q) q.focus(); },
  },
  client: {
    tab: 'clients', back: 'clients',
    load: async ucc => ST.cl.find(c => c.ucc === ucc) || (await get('/v1/m/clients?date=' + MS.day() + '&q=' + encodeURIComponent(ucc))).clients.find(c => c.ucc === ucc),
    html: c => !c ? empty('No such client, or not one of yours.') : `<div class="ph-pg ph-d"><span class="ph-k mono">${esc(c.ucc)}</span><h1>${esc(c.name)}</h1>
      ${facts([row('Status', pill(c.status, c.status === 'Active' ? 'good' : 'nt') + (c.blocked ? ' ' + pill('Blocked', 'bad') : '')), c.risk_status && row('Margin', pill(c.risk_status, { OK: 'good', Watch: 'warn', Breach: 'bad', Debit: 'warn' }[c.risk_status] || 'nt')),
        row('Ledger', `<span class="mono ${sgn(c.funds)}">${esc(c.funds)}</span>`), row('PAN', esc(c.pan), 'mono'), c.mobile && row('Mobile', esc(c.mobile), 'mono'), c.email && row('Email', esc(c.email)),
        c.branch && row('Branch', esc(c.branch)), c.partner && row('Partner', esc(c.partner)), (c.segments || []).length && row('Segments', esc(c.segments.join(', ')))])}
      <p class="dim small">Open the client on a computer for the ledger, holdings and documents.</p></div>`,
  },
  ledgy: {
    tab: 'more', back: 'more',
    html: () => `<div class="ph-pg"><h1>Ask Ledgy</h1><p class="dim">Ask how to do something, or why a step is waiting. Ledgy answers from the help articles.</p>
      <div class="m-chat">${ST.ledgy.map(x => `<div class="m-q">${esc(x.q)}</div><div class="m-a">${x.a}</div>`).join('')}</div>
      <form id="m-lg" class="m-askf"><input class="in" name="q" placeholder="Your question" autocomplete="off" required><button class="btn pri">Ask</button></form></div>`,
    after: () => { $('m-lg').onsubmit = e => { e.preventDefault(); askLedgy(new FormData(e.target).get('q')); }; },
  },
  more: {
    html: () => `<div class="ph-pg"><h1>More</h1>
      <div class="m-who"><b>${esc(MS.me?.users?.find(u => u.id === MS.me.actor)?.name || MS.me?.actor || '')}</b><small>${esc(MS.me?.role || '')} · ${esc(MS.cfg?.firm || '')}</small></div>
      <div class="ph-list">${linkRow('Surveillance alerts', 'alerts')}${linkRow('Ask Ledgy', 'ledgy')}${linkRow('My phones', 'phones', 'phones that unlock with a fingerprint or face')}
        <a class="ph-row" href="/">Open the full console<span aria-hidden="true">›</span></a></div>
      <button class="btn m-wide" data-act="signOut">Sign out</button>${MS.device ? '<button class="btn bad m-wide" data-act="removePhone">Remove this phone</button>' : ''}${MS.native ? '<button class="btn m-wide" data-act="changeServer">Use another server or firm</button>' : ''}
      <p class="dim small">The app locks after ${esc(MS.cfg?.idle_minutes)} minutes without use. Screens cannot be captured, and notifications never name a client.</p></div>`,
  },
  phones: MS.phonesScreen(),
  attendance: attendanceScreen(),
};

function clientRows() {
  if (ST.q.trim().length < 2) return '<div class="ph-empty">Type at least two letters of the name, the UCC or the PAN.</div>';
  return `<div class="ph-list">${ST.cl.map(c => `<button class="ph-cl" data-go="client:${esc(c.ucc)}"><span><b>${esc(c.name)}</b><small class="mono">${esc(c.ucc)} · ${esc(c.pan)}</small></span><span class="ph-cr"><span class="mono ${sgn(c.funds)}">${esc(c.funds)}</span>${c.risk_status ? pill(c.risk_status, { OK: 'good', Watch: 'warn', Breach: 'bad', Debit: 'warn' }[c.risk_status] || 'nt') : pill(c.status, c.status === 'Active' ? 'good' : 'nt')}</span></button>`).join('') || '<div class="ph-empty">No client matches.</div>'}</div>`;
}
async function findClients() {
  const q = ST.q.trim();
  if (q.length < 2) { ST.cl = []; } else { try { ST.cl = (await get('/v1/m/clients?date=' + MS.day() + '&q=' + encodeURIComponent(q))).clients; } catch (e) { $('m-cl').innerHTML = errBox(e); return; } }
  if ($('m-cl') && ST.q.trim() === q) $('m-cl').innerHTML = clientRows();
}
// Ledgy's answer as plain text paragraphs: **bold** kept, links to
// screens shown as their names.
const lgText = t => esc(t).replace(/\[\[[a-z]+\|([^\]]+)\]\]/g, '<b>$1</b>').replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>').split(/\n{2,}/).map(p => `<p>${p.replace(/\n/g, '<br>')}</p>`).join('');
async function askLedgy(q) {
  if (!q) return;
  const entry = { q, a: '<p class="dim">Thinking…</p>' };
  ST.ledgy.push(entry); go('ledgy', { keep: true });
  try {
    const a = await post('/v1/ledgy/ask', { q, view: '', date: MS.day() });
    entry.a = a.ai ? lgText(a.ai) : a.article ? `<p><b>${esc(a.article.title)}</b></p>${lgText(a.article.body)}` : '<p>Ledgy has no article for that yet. Ask your supervisor, or open the console.</p>';
  } catch (e) { entry.a = errBox(e); }
  if (MS.route === 'ledgy') go('ledgy', { keep: true });
}

Object.assign(ACT, {
  seg: id => { ST.seg = id; go('approvals', { keep: true }); },
  findClient: ucc => { ST.q = ucc; go('client:' + ucc); },
  approve: id => { const w = findItem(id); if (!w) return; sheet('Approve', `<p><b>${esc(w.action)}</b></p><p>${esc(w.title)}${w.amount ? ' · <b>' + esc(w.amount) + '</b>' : ''}</p><p class="dim small">Prepared by Autopilot. Your name and the time go into the audit trail.</p>`, 'Approve', async () => { await post(`/v1/m/approvals/${encodeURIComponent(id)}`, { decision: 'approve' }); toast('Approved: ' + w.title); homeCounts(); go('approvals'); }); },
  reject: id => { const w = findItem(id); if (!w) return; sheet('Reject', `<p>${esc(w.title)}</p><label class="m-f">Reason (the maker sees it)<textarea class="in" name="reason" rows="3" required></textarea></label>`, 'Reject', async f => { await post(`/v1/m/approvals/${encodeURIComponent(id)}`, { decision: 'reject', reason: f.reason }); toast('Rejected'); homeCounts(); go('approvals'); }, true); },
  closeItem: id => { const w = findItem(id); if (!w) return; sheet('Close with a note', `<p>${esc(w.title)}</p><label class="m-f">What was done, or why it is not needed<textarea class="in" name="reason" rows="3" required></textarea></label>`, 'Close', async f => { await post(`/v1/m/approvals/${encodeURIComponent(id)}`, { decision: 'close', reason: f.reason }); toast('Closed'); homeCounts(); go('work'); }); },
  changeServer: () => sheet('Use another server or firm', '<p>The app goes back to its set-up screen. This phone stays registered here until you remove it.</p>', 'Continue', async () => { await post('/v1/auth/logout').catch(() => {}); await plug('LedgerlineShell').resetServer(); }),
  askWhy: id => { const w = findItem(id); if (!w) return; go('ledgy'); askLedgy(`Why is this waiting, and what happens if I approve it: "${w.title}"?`); },
});

/* ---------- attendance check-in (the HRMS module) ----------
   Shown only when the firm has the HRMS module on and the person's login is
   linked to an employee: GET /v1/hr/me (the person's own HR records)
   answers 404 when the module is off. A punch is POST
   /v1/hr/attendance/punch (branch hrms, internal/ops/hrtime.go PunchReq):
     { kind: in|out, mode: office|home|field, has_location, lat, lng,
       device_id, biometric_unlocked, selfie (base64 JPEG), note }
   and answers { at, kind, mode, site, status } or an error that says why it
   was refused (not on the office network or at the office, home not
   allowed today, a field punch without a note, not the registered phone).
   The HRMS server decides; this page only asks. Inside the app every punch
   first asks for the face or fingerprint, and device_id is the phone's
   install ID (Capacitor Device.getId), so HR can hold one phone per person. */
async function hrDetect() {
  let me = null;
  try {
    const r = await fetch('/v1/hr/me', { headers: { 'X-Requested-With': 'ledgerline' }, credentials: 'same-origin' });
    me = r.ok ? await r.json() : null;
  } catch (e) { me = null; }
  const was = !!ST.hr;
  ST.hr = me && me.linked ? hrState(me.attendance || {}) : null;
  if (!!ST.hr !== was && !MS.locked && String(MS.route || 'approvals').split(':')[0] === 'approvals') refresh();
  return ST.hr;
}
// The day as the HRMS module keeps it: in and out times (IST), the mode,
// and whether the last punch was an in.
function hrState(a) {
  const day = (a.days || []).find(d => d.date === todayIST()) || {}, ps = day.punches || [];
  return { modes: (a.policy || {}).modes || [], policy: a.policy || {}, today: { in: day.in || '', out: day.out || '', mode: day.mode || '', status: day.status || '', last: ps.length ? ps[ps.length - 1].kind : '' } };
}
const HR_MODES = [['office', 'Office'], ['home', 'Home'], ['field', 'Field']];
const hrIn = () => ST.hr?.today?.last === 'in';
const istHM = t => { const d = new Date(t); return isNaN(d) ? '' : new Date(d.getTime() + 330 * 60000).toISOString().slice(11, 16); };
const modeName = m => (HR_MODES.find(x => x[0] === m) || [m, m])[1];
function hrBar() {
  if (!ST.hr) return '';
  const t = ST.hr.today;
  return `<div class="m-hr" id="m-hr"><span><b>${hrIn() ? 'Checked in' : t.out ? 'Checked out' : 'Not checked in'}</b><small>${t.in ? 'in ' + esc(t.in) + (t.mode ? ' · ' + esc(modeName(t.mode)) : '') : 'Attendance'}${t.out && !hrIn() ? ' · out ' + esc(t.out) : ''}</small></span>
    <button class="btn ${hrIn() ? '' : 'pri'}" data-go="attendance">${hrIn() ? 'Check out' : 'Check in'}</button></div>`;
}
function attendanceScreen() {
  return {
    tab: 'approvals', back: 'approvals',
    load: () => hrDetect(),
    html: hr => {
      if (!hr) return '<div class="ph-pg"><h1>Attendance</h1><div class="ph-empty">Attendance is not switched on for you. Your HR team links your login to your employee record.</div></div>';
      const modes = HR_MODES.filter(([k]) => !hr.modes.length || hr.modes.includes(k)), kind = hrIn() ? 'out' : 'in';
      const sel = modes.some(m => m[0] === ST.hrMode) ? ST.hrMode : modes.some(m => m[0] === hr.today.mode) ? hr.today.mode : modes[0]?.[0];
      const selfieReq = hr.policy.selfie === 'required';
      return `<div class="ph-pg"><h1>${kind === 'in' ? 'Check in' : 'Check out'}</h1><p class="dim">${fmtDate(todayIST())}${hr.today.in ? ' · in at ' + esc(hr.today.in) : ''}${hr.today.status ? ' · ' + esc(hr.today.status) : ''}</p>
        ${ST.hrResult || ''}
        ${modes.length ? `<div class="ph-seg m-seg3" role="radiogroup" aria-label="Where you work today">${modes.map(([k, l]) => `<button role="radio" aria-checked="${sel === k}" class="${sel === k ? 'on' : ''}" data-act="hrMode" data-id="${k}">${l}</button>`).join('')}</div>
        <p class="dim small">${esc({ office: 'At your office or branch. The office network or the office’s location confirms it.', home: 'Working from home, on the days your work policy allows.', field: 'Out on work: meetings, client visits, the exchange. Say where.' }[sel] || '')}</p>` : '<div class="m-warn">Your work policy allows no check-in from the phone. Ask HR.</div>'}
        <label class="m-f">${sel === 'field' ? 'Whom or where you are visiting' : 'Note (optional)'}<input class="in" id="m-hrnote" maxlength="300" value="${esc(ST.hrNote || '')}" ${sel === 'field' ? 'required' : ''}></label>
        <label class="btn m-wide">${svg('face', 18)} ${selfieReq ? 'Take a selfie (your firm asks for one)' : 'Add a selfie (optional)'}<input id="m-selfie" type="file" accept="image/jpeg,image/png,image/*" capture="user" hidden></label><img id="m-selfie-pre" class="m-selfie" alt="Your selfie" ${ST.selfie ? `src="${ST.selfie}"` : 'hidden'}>
        <label class="m-check"><input type="checkbox" id="m-loc" ${ST.hrNoLoc ? '' : 'checked'}> Send my location with this ${kind === 'in' ? 'check-in' : 'check-out'}</label>
        <div id="m-hrerr"></div>
        <button class="btn pri ph-btn" data-act="punch" data-id="${kind}" ${modes.length ? '' : 'disabled'}>${MS.native ? svg('face', 18) + ' ' : ''}${kind === 'in' ? 'Check in' : 'Check out'}</button>
        <p class="dim small">${MS.native ? 'You confirm each check-in and check-out with your face or fingerprint. ' : ''}Attendance from a phone is taken from your registered phone only. Your location is sent only with a check-in or check-out, never in between.</p></div>`;
    },
    after: () => {
      const f = $('m-selfie'), n = $('m-hrnote'), l = $('m-loc');
      if (n) n.oninput = () => { ST.hrNote = n.value; };
      if (l) l.onchange = () => { ST.hrNoLoc = !l.checked; };
      if (f) f.onchange = async () => { try { ST.selfie = f.files[0] ? await shrink(f.files[0]) : ''; } catch (e) { ST.selfie = ''; $('m-hrerr').innerHTML = errBox(e); } const im = $('m-selfie-pre'); im.hidden = !ST.selfie; if (ST.selfie) im.src = ST.selfie; };
    },
  };
}
// A selfie, made small: a 480 px JPEG data URL.
function shrink(file) {
  return new Promise((res, rej) => {
    const u = URL.createObjectURL(file), im = new Image();
    im.onload = () => { const s = Math.min(1, 480 / Math.max(im.width, im.height)), c = document.createElement('canvas'); c.width = Math.round(im.width * s); c.height = Math.round(im.height * s); c.getContext('2d').drawImage(im, 0, 0, c.width, c.height); URL.revokeObjectURL(u); res(c.toDataURL('image/jpeg', 0.8)); };
    im.onerror = () => { URL.revokeObjectURL(u); rej(new Error('That photo could not be read')); };
    im.src = u;
  });
}
function where() {
  return new Promise(res => {
    if (!navigator.geolocation) { res(null); return; }
    navigator.geolocation.getCurrentPosition(p => res({ lat: p.coords.latitude, lng: p.coords.longitude }), () => res(null), { enableHighAccuracy: true, timeout: 10000, maximumAge: 30000 });
  });
}
// The phone's install ID: stable while the app stays installed.
async function installID() {
  const D = plug('Device');
  try { const r = D ? await D.getId() : null; return r?.identifier || MS.device?.id || ''; } catch (e) { return MS.device?.id || ''; }
}
async function punch(kind) {
  const err = $('m-hrerr'); err.innerHTML = '';
  const mode = document.querySelector('[data-act="hrMode"].on')?.dataset.id || 'office', note = ($('m-hrnote')?.value || '').trim();
  if (mode === 'field' && !note) { err.innerHTML = errBox('Say whom or where you are visiting.'); return; }
  if (ST.hr?.policy?.selfie === 'required' && !ST.selfie) { err.innerHTML = errBox('Your firm asks for a selfie with this check-in.'); return; }
  const body = { kind, mode, note };
  // In the app: the face or fingerprint right before each punch, and the
  // phone's install ID.
  if (MS.native) {
    if (!MS.device) { err.innerHTML = errBox('Set up fingerprint or face unlock on this phone first: sign out, sign in with your password and choose to use it.'); return; }
    try { await Shell.readSecret(kind === 'in' ? 'Check in' : 'Check out'); } catch (e) { err.innerHTML = errBox('Not confirmed. Try again with your face or fingerprint.'); return; }
    body.device_id = await installID(); body.biometric_unlocked = true;
  }
  const loc = $('m-loc')?.checked ? await where() : null;
  if (loc) Object.assign(body, { has_location: true, lat: loc.lat, lng: loc.lng });
  if (ST.selfie) body.selfie = ST.selfie.split(',')[1];
  const bt = document.querySelector('[data-act="punch"]'); if (bt) bt.disabled = true;
  try {
    const r = await post('/v1/hr/attendance/punch', body);
    ST.selfie = ''; ST.hrMode = ''; ST.hrNote = '';
    ST.hrResult = `<div class="m-ok" role="status"><b>${kind === 'in' ? 'Checked in' : 'Checked out'} at ${esc(istHM(r?.at) || istHM(new Date()))}</b> · ${esc(modeName(r?.mode || mode))}${r?.site ? ' · ' + esc(r.site) : ''}${loc ? '' : ' · no location sent'}${r?.status ? `<br><small>Today: ${esc(r.status)}</small>` : ''}</div>`;
    await hrDetect(); homeCounts(); go('attendance', { keep: true });
  } catch (e) {
    if (bt) bt.disabled = false;
    if (e.status === 401) return;
    ST.hrResult = '';
    err.innerHTML = `<div class="m-err" role="alert"><b>${kind === 'in' ? 'Check-in' : 'Check-out'} refused.</b> ${esc(String(e.message || e).replace(/^(not permitted for this role|invalid|invalid state): /i, '').replace(/^./, c => c.toUpperCase()))}${loc || !$('m-loc')?.checked ? '' : '<br><small>Your location could not be read: allow location for the app and try again.</small>'}</div>`;
  }
}
Object.assign(ACT, {
  hrMode: id => { ST.hrMode = id; ST.hrResult = ''; go('attendance', { keep: true }); },
  punch: kind => punch(kind),
});

MS.start();
