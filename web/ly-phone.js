'use strict';
/* Ledgerline console: the H · Phone approvals layout (layouts.js).
   Made for a phone: the approvals waiting for you with their prepared
   actions, today at a glance and the day's steps, clients, and Ask Ledgy,
   with a bar of tabs at the bottom. Any other screen opens under a header
   that leads back to the approvals. */

const PH_TABS = [['approve', 'Approvals', 'approve'], ['today', 'Today', 'today'], ['clients', 'Clients', 'clients']];
const phDay = (short) => { const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(S.date); if (!m) return esc(S.date); const d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3])); return `${['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'][d.getUTCDay()]} ${+m[3]} ${MON[+m[2] - 1]}${short ? '' : ' ' + m[1]}`; };
const phToday = () => lyNow().slice(0, 10);
const phView = w => String(w.view || '').split(':')[0];
const phCanOpen = w => !!w.view && (viewAllowed(phView(w)) || SCOPED_VIEWS.includes(phView(w))) && !!V[phView(w)];
// Waiting: open items this person can act on, prepared actions first.
function phWaiting() {
  return lyWorkOpen().filter(w => lyCanAct(w) && lyMine(w))
    .sort((a, b) => (!!b.proposed - !!a.proposed) || (b.overdue - a.overdue) || String(a.due || '9').localeCompare(String(b.due || '9')));
}
// Done: items closed today (the clock's day) or for the trade date.
function phDone() {
  const t = phToday();
  return (LY.ap?.work || []).filter(w => w.status !== 'Open' && ((w.closed || '').slice(0, 10) === t || w.date === S.date))
    .sort((a, b) => String(b.closed || '').localeCompare(String(a.closed || '')));
}
const phName = v => (NAV_VIEWS.find(x => x[0] === v) || [, ''])[1] || 'Ledgerline';

LYS.phone = {
  top: () => S.view === 'lyPhone' ? `<div class="ly-top ph-top">
      <button class="ph-when" data-act="lyHomeGo"><b>${phDay(true)}</b>${S.date === phToday() ? ` · ${esc(lyNow().slice(11))}` : ''}<small>${esc(S.me?.firm || '')}</small></button>
      <span class="ly-push"></span>
      ${uiE().command_bar ? `<button class="ph-ib" data-act="lyK" aria-label="Search">${lySvg('search', 20)}</button>` : ''}
      ${lyUser(true)}</div>`
    : `<div class="ly-top ph-top"><button class="ph-back" data-act="go" data-id="lyPhone">‹ ${esc({ approve: 'Approvals', today: 'Today', clients: 'Clients' }[UI.phTab] || 'Approvals')}</button>
      <b class="ph-title">${esc(S.view === 'lyClient' ? 'Client' : phName(S.view))}</b>
      <span class="ly-push"></span>${uiE().command_bar ? `<button class="ph-ib" data-act="lyK" aria-label="Search">${lySvg('search', 20)}</button>` : ''}</div>`,
  side: () => {
    const n = LY.ap ? phWaiting().length : 0, here = S.view === 'lyPhone';
    return `<nav class="ph-tabs" aria-label="App">${PH_TABS.map(([id, l, ic]) => `<button class="ph-tab ${here && UI.phTab === id ? 'on' : ''}" data-act="phTab" data-id="${id}" ${here && UI.phTab === id ? 'aria-current="page"' : ''}>${lySvg(ic, 22)}<span>${l}</span>${id === 'approve' && n ? `<i>${n > 99 ? '99+' : n}</i>` : ''}</button>`).join('')}
      ${lgAllowed() ? `<button class="ph-tab" data-act="ledgyOpen">${lySvg('ledgy', 22)}<span>Ask Ledgy</span></button>` : ''}</nav>`;
  },
};

V.lyPhone = {
  load: async () => { await lyData(0); if (UI.phTab === 'clients') await clientCache(); return LY; },
  html: () => {
    if (UI.phTab === 'approve' && UI.phItem) { const w = lyWorkOpen().find(x => x.id === UI.phItem); if (w) return phItem(w); UI.phItem = ''; }
    return UI.phTab === 'today' ? phTodayHTML() : UI.phTab === 'clients' ? phClients() : phApprovals();
  },
};

function phCard(w, done) {
  const area = lyArea(w) === 'Other' ? '' : lyArea(w);
  return `<${done ? 'div' : 'button'} class="ph-card ${w.overdue ? 'od' : ''}" ${done ? '' : `data-act="phOpen" data-id="${esc(w.id)}"`}>
    <span class="ph-k">${esc([area, w.proposed ? 'prepared by Autopilot' : lyProc(w)].filter(Boolean).join(' · '))}<span>${done ? esc(w.status) : w.overdue ? 'overdue' : esc(w.due ? (w.due.slice(0, 10) === S.date ? 'by ' + w.due.slice(11) : fmtDate(w.due.slice(0, 10))) : '')}</span></span>
    <b>${esc(w.title)}</b>
    <span class="ph-sub">${esc(done ? [w.closed_by && 'by ' + w.closed_by, w.closed && lyHHMM(w.closed), w.note].filter(Boolean).join(' · ') : w.proposed ? w.proposed.label + ' · nothing happens until you approve' : w.detail || 'Open to deal with it')}</span></${done ? 'div' : 'button'}>`;
}
function phGlance() {
  const d = LY.d; if (!d) return '';
  return `<section class="ph-glance" aria-label="Today at a glance"><h2>Today at a glance</h2><dl>
    <div><dt>Turnover</dt><dd>${cmp(d.turnover)}</dd></div>
    <div><dt>Margin collected / required</dt><dd>${cmp(d.collected)} / ${cmp(d.required)}</dd></div>
    <div><dt>Client funds cover</dt><dd class="${d.funds.ok ? 'pos' : 'neg'}">${num(d.funds.cover).toFixed(2)}×</dd></div>
    ${d.risk.Breach ? `<div><dt>Clients in margin breach</dt><dd class="neg">${d.risk.Breach}</dd></div>` : ''}</dl></section>`;
}
function phApprovals() {
  const wait = phWaiting(), done = phDone(), seg = UI.phSeg === 'done' ? 'done' : 'wait';
  const close = LY.d ? lySteps().find(s => s.id === 'close') : null;
  const list = seg === 'done' ? done : wait;
  return `<div class="ph-pg">
    <h1>Approvals</h1>
    <div class="ph-seg" role="tablist"><button role="tab" aria-selected="${seg === 'wait'}" class="${seg === 'wait' ? 'on' : ''}" data-act="phSeg" data-id="wait">Waiting · ${wait.length}</button><button role="tab" aria-selected="${seg === 'done'}" class="${seg === 'done' ? 'on' : ''}" data-act="phSeg" data-id="done">Done today${done.length ? ' · ' + done.length : ''}</button></div>
    <div class="ph-list">${list.length ? list.slice(0, 60).map(w => phCard(w, seg === 'done')).join('') : `<div class="ph-empty">${seg === 'done' ? 'Nothing closed today yet.' : '<b>Nothing is waiting for you.</b><br>Anything Autopilot prepares for your approval shows here.'}</div>`}
      ${list.length > 60 ? `<p class="dim small">and ${list.length - 60} more: open the inbox on a computer for the rest.</p>` : ''}
      ${seg === 'wait' && close && close.state !== 'done' && close.state !== 'na' ? `<div class="ph-card info"><span class="ph-k">Operations<span>by 18:45</span></span><b>Equity close</b><span class="ph-sub">${esc(close.note)}</span></div>` : ''}</div>
    ${phGlance()}</div>`;
}
function phItem(w) {
  const can = lyCanAct(w), open = phCanOpen(w);
  const named = !w.ucc && /^[A-Z]{1,4}\d{3,}(,\s*[A-Z]{1,4}\d{3,})*$/.test(String(w.detail || '').trim());
  return `<div class="ph-pg ph-d">
    <button class="ph-back" data-act="phBack">‹ Approvals</button>
    <span class="ph-k">${esc([lyArea(w) === 'Other' ? '' : lyArea(w), w.proposed ? 'prepared by Autopilot' : lyProc(w)].filter(Boolean).join(' · '))}</span>
    <h1>${esc(w.title)}</h1>
    ${w.proposed ? `<div class="ph-big">${esc(w.proposed.label)}</div>` : ''}
    ${w.detail && !named ? `<p class="ph-p">${esc(w.detail)}</p>` : ''}
    <dl class="ph-facts">
      ${w.ucc ? `<div><dt>Client</dt><dd><button class="linkish mono" data-act="client" data-id="${esc(w.ucc)}">${esc(w.ucc)}</button></dd></div>` : ''}
      ${named ? `<div><dt>Clients</dt><dd class="mono">${esc(w.detail)}</dd></div>` : ''}
      <div><dt>Due</dt><dd>${lyDue(w) || '—'}</dd></div>
      <div><dt>For</dt><dd>${esc(w.role || 'Anyone')}${can ? '' : ' <span class="dim">(not you)</span>'}</dd></div>
      <div><dt>From</dt><dd>${esc(lyProc(w))}</dd></div>
    </dl>
    ${open ? `<button class="ph-row" data-act="go" data-id="${esc(w.view)}">See the working, on ${esc(phName(phView(w)))}<span aria-hidden="true">›</span></button>` : ''}
    <div class="ph-acts">
      ${can && w.proposed ? `<button class="btn pri ph-btn" data-act="phDo" data-id="${esc(w.id)}">Approve: ${esc(w.proposed.label)}</button><button class="btn ph-btn" data-act="wDismiss" data-id="${esc(w.id)}">Reject, with a note</button>` : ''}
      ${can && !w.proposed ? `<button class="btn ph-btn" data-act="wDismiss" data-id="${esc(w.id)}">Close with a note</button>` : ''}
      ${!can ? `<p class="dim">Waiting for ${esc(w.role)}.</p>` : ''}
      ${lgAllowed() ? `<button class="btn ph-btn" data-act="phAsk" data-id="${esc(w.id)}">${lySvg('ledgy', 16)} Ask Ledgy why</button>` : ''}
    </div>
    <p class="dim small">${w.proposed ? 'Approving carries out the prepared action. ' : ''}Your name and the time go into the audit trail.</p></div>`;
}
function phTodayHTML() {
  const d = LY.d;
  if (!d) {
    const links = [['clients', 'Clients'], ['money', 'Receipts and payouts'], ['notes', 'Contract notes'], ['rms', 'Margin'], ['statements', 'Client reports'], ['helpdesk', 'Investor complaints']].filter(([k]) => viewAllowed(k));
    return `<div class="ph-pg"><h1>Today</h1><p class="dim">${phDay()} · your clients</p><div class="ph-list">${links.map(([k, l]) => `<button class="ph-row" data-act="go" data-id="${k}">${esc(l)}<span aria-hidden="true">›</span></button>`).join('')}</div></div>`;
  }
  const st = lySteps(), done = st.filter(s => s.state === 'done').length, needs = st.filter(s => s.state === 'needs').length, all = st.filter(s => s.state !== 'na').length;
  return `<div class="ph-pg"><h1>Today</h1><p class="dim">${phDay()} · ${done} of ${all} steps done${needs ? ` · ${needs} need someone` : ''}</p>
    ${phGlance()}
    ${LY_SESS.map(([s, name, when]) => { const xs = st.filter(x => x.s === s); return xs.length ? `<section class="ph-sess"><h2>${esc(name)} <span>${esc(when)}</span></h2>${xs.map(x => { const v = x.views.find(k => viewAllowed(k)); return `<${v ? `button data-act="go" data-id="${v}"` : 'div'} class="ph-step">${lyMark(x)}<span><b>${esc(x.name)}</b><small class="${x.state === 'needs' ? 'neg' : ''}">${esc(x.note)}</small></span>${v ? '<i aria-hidden="true">›</i>' : ''}</${v ? 'button' : 'div'}>`; }).join('')}</section>` : ''; }).join('')}</div>`;
}
function phClients() {
  return `<div class="ph-pg"><h1>Clients</h1><label class="ph-search">${lySvg('search', 18)}<input class="in" data-ui="phQ" data-part="ph-cl" placeholder="Name, UCC or PAN" value="${esc(UI.phQ)}" autocomplete="off" spellcheck="false" aria-label="Find a client"></label><div id="ph-cl">${P['ph-cl']()}</div></div>`;
}
P['ph-cl'] = () => {
  const q = UI.phQ.trim().toLowerCase(), st = { OK: 'good', Watch: 'warn', Breach: 'bad', Debit: 'warn' };
  const rows = CL.rows.filter(c => !q || (c.ucc + ' ' + c.name + ' ' + c.pan + ' ' + (c.tags || []).join(' ')).toLowerCase().includes(q));
  return `<div class="ph-list">${rows.slice(0, 50).map(c => `<button class="ph-cl" data-act="client" data-id="${esc(c.ucc)}"><span><b>${esc(c.name)}</b><small class="mono">${esc(c.ucc)}${(c.tags || []).length ? ' · ' + esc(c.tags.join(', ')) : ''}</small></span><span class="ph-cr"><span class="mono ${sgn(c.funds)}">${inr(c.funds)}</span>${c.risk_status ? pill(c.risk_status, st[c.risk_status] || 'nt') : pill(c.status, c.status === 'Active' ? 'good' : 'nt')}</span></button>`).join('') || '<div class="ph-empty">No client matches.</div>'}
    ${rows.length > 50 ? `<p class="dim small">${rows.length - 50} more: type more of the name.</p>` : ''}</div>`;
};

Object.assign(A, {
  phTab: id => { UI.phTab = id; UI.phItem = ''; if (S.view === 'lyPhone' && id !== 'clients') { $('page').innerHTML = groupBar('lyPhone') + V.lyPhone.html(); bindPage(); renderSide(); window.scrollTo(0, 0); return; } A.go('lyPhone'); },
  phSeg: id => { UI.phSeg = id; $('page').innerHTML = groupBar('lyPhone') + V.lyPhone.html(); bindPage(); },
  phOpen: id => { UI.phTab = 'approve'; UI.phItem = id; $('page').innerHTML = groupBar('lyPhone') + V.lyPhone.html(); bindPage(); window.scrollTo(0, 0); },
  phBack: () => { UI.phItem = ''; $('page').innerHTML = groupBar('lyPhone') + V.lyPhone.html(); bindPage(); window.scrollTo(0, 0); },
  phDo: id => { const w = lyWorkOpen().find(x => x.id === id); if (!w?.proposed) return; confirmModal(esc(w.proposed.label), esc(w.title), '<p class="dim small">Prepared by Autopilot. Your name and the time go into the audit trail.</p>', 'Approve', async () => { await act(() => post(`/v1/work/${encodeURIComponent(id)}`, { do: true }), r => 'Approved: ' + r.title); }); },
  phAsk: id => { const w = lyWorkOpen().find(x => x.id === id); if (!w) return; LG.open = true; lgAsk(`Why is this waiting for approval, and what happens if I approve it: "${w.title}"${w.detail ? ` (${w.detail})` : ''}?`); },
});
