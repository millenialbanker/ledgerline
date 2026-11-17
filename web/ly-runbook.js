'use strict';
/* Ledgerline console: the A · Day runbook layout (layouts.js).
   The day as numbered steps from before the open to the commodity close,
   in a rail beside the page. Each step shows whether it is done, needs
   someone or is waiting, from the day's numbers and the work queue
   (lySteps); clicking it opens its screen, with a banner above the screen
   saying where the step stands, what is prepared, and the next step.
   Weekly, Monthly and All hold the rest of the menu. */

const rbViewName = v => (NAV_VIEWS.find(x => x[0] === v) || [, v])[1];
// rbSteps: the day's steps, or null while the day's data is loading.
const rbSteps = () => LY.ap && LY.date === S.date ? lySteps() : null;
const rbOpenView = st => st.views.find(viewAllowed);
// rbTop: the header's height, for the rail and the client page's sticky bars.
const rbSetTop = () => requestAnimationFrame(() => { const t = $('top'); if (t && ['runbook', 'workspaces'].includes(lyCur())) document.body.style.setProperty('--ly-top', t.offsetHeight + 'px'); });

// rbReveal: scroll the rail (or, on a phone, the strip of steps) to the
// current step, without moving the page.
function rbReveal() {
  requestAnimationFrame(() => {
    const el = document.querySelector('.rb-step.on'), side = $('side'); if (!el || !side) return;
    const strip = el.closest('.rb-steps'), r = el.getBoundingClientRect();
    if (strip && strip.scrollWidth > strip.clientWidth + 2) { const sr = strip.getBoundingClientRect(); if (r.left < sr.left || r.right > sr.right) strip.scrollLeft += r.left - sr.left - 12; return; }
    if (side.scrollHeight > side.clientHeight + 2) { const sr = side.getBoundingClientRect(); if (r.top < sr.top || r.bottom > sr.bottom) side.scrollTop += r.top - sr.top - sr.height / 3; }
  });
}
function rbProgress(st) {
  const day = st.filter(s => s.s !== 'commodity' && s.state !== 'na'), n = k => day.filter(s => s.state === k).length;
  const done = n('done'), needs = n('needs'), wait = n('wait'), tot = Math.max(1, day.length);
  return `<div class="rb-prog"><div class="rb-prog-h"><b>Equity day</b><span>${done} of ${day.length} done</span></div>
    <div class="rb-bar" role="img" aria-label="${done} done, ${needs} need someone, ${wait} not yet"><i class="done" style="width:${(done / tot * 100).toFixed(1)}%"></i><i class="needs" style="width:${(needs / tot * 100).toFixed(1)}%"></i></div>
    <div class="rb-legend"><span><i class="done"></i>${done} done</span><span><i class="needs"></i>${needs} need${needs === 1 ? 's' : ''} someone</span><span><i class="wait"></i>${wait} not yet</span></div></div>`;
}
function rbStepRow(s, cur) {
  const can = !!rbOpenView(s);
  return `<button class="rb-step ${s.state} ${cur ? 'on' : ''}" data-act="rbStep" data-id="${s.id}" ${can ? '' : 'disabled title="Your role can’t open this step’s screens"'} ${cur ? 'aria-current="step"' : ''}>
    <span class="rb-n">${s.n}</span>${lyMark(s, '')}<span class="rb-txt"><span class="rb-name">${esc(s.name)}</span><span class="rb-note">${esc(s.note)}</span></span>${s.state === 'needs' && s.items.length ? `<span class="rb-ct">${s.items.length}</span>` : ''}</button>`;
}
function rbToday() {
  const st = rbSteps();
  if (!st) return '<div class="rb-loading"><p class="loading">Loading the day…</p></div>';
  const cur = lyStepOf(S.view)?.id;
  return rbProgress(st) + `<div class="rb-steps">${LY_SESS.map(([k, name, time]) => {
    const ss = st.filter(s => s.s === k); if (!ss.length) return '';
    return `<section class="rb-sess" aria-label="${esc(name)}"><div class="rb-sh"><b>${esc(name)}</b><span>${esc(time)}</span></div>${ss.map(s => rbStepRow(s, s.id === cur)).join('')}</section>`;
  }).join('')}</div>
  <p class="rb-foot">Every screen that isn’t a daily step is under <button class="linkish" data-act="rbTab" data-id="all">All</button>${uiE().command_bar ? ' and in the search bar' : ''}.</p>`;
}
function rbLinks(vs) {
  return `<div class="rb-links">${vs.filter(([v]) => viewAllowed(v)).map(([v, about]) => `<button class="rb-link ${S.view === v ? 'on' : ''}" data-act="go" data-id="${v}"><b>${esc(rbViewName(v))}</b><span>${esc(about)}</span></button>`).join('')}</div>`;
}
function rbStage(id) {
  const s = (LY.ap?.stages || []).find(x => x.stage === id); if (!s) return '';
  return `<div class="note small">${esc(s.name)}: ${s.summary ? esc(s.summary) : s.status ? esc(s.status) : 'not run yet'}${s.last ? ` <span class="dim">· last run ${esc(fmtDate(s.last.slice(0, 10)))} ${esc(s.last.slice(11, 16))}</span>` : ''}</div>`;
}
function rbWeekly() {
  return `<p class="rb-intro">Weekly work: circular actions and compliance dates for the week, re-KYC reminders, leads to follow up, and the firm’s own boards.</p>` +
    rbLinks([['periodic', 'Recurring duties and their due dates'], ['circulars', 'Circulars, their actions and owners'], ['boards', 'The team’s boards and cards'], ['crm', 'Leads due for a follow-up'], ['backups', 'Backups and restore checks']]);
}
function rbMonthly() {
  return `<p class="rb-intro">Month end: interest on debit balances, MTF interest, DP bills and AP payouts are posted; running accounts are settled on their dates; inspection packs and the self-inspection are prepared.</p>` + rbStage('monthly') +
    rbLinks([['periodic', 'Month-end and quarterly duties'], ['ras', 'Running account settlement'], ['debits', 'Debtors and interest'], ['selfinspect', 'The nightly self-inspection'], ['inspack', 'Inspection packs'], ['backups', 'Backups and restore checks']]);
}
function rbBanner(v) {
  const def = lyStepOf(v), st = rbSteps();
  if (!def || !st) return '';
  const s = st.find(x => x.id === def.id), sess = LY_SESS.find(x => x[0] === s.s), N = st.length;
  const g = groupOf(v), others = s.views.filter(x => viewAllowed(x) && (x === v || !g || groupOf(x) !== g));
  const next = st.slice(s.n).find(x => rbOpenView(x));
  const items = s.items, prep = items.filter(w => w.proposed).slice(0, 3);
  const pol = (LY.ap?.policies || []).filter(p => s.pol.includes(p.id));
  const modes = pol.map(p => `${esc(p.name)}: ${esc(p.mode)}`).join(' · ');
  return `<section class="rb-head ${s.state}" aria-label="Step ${s.n}">
    <div class="rb-crumb"><span>${esc(sess ? sess[1] : '')}</span><span aria-hidden="true">›</span><span>Step ${s.n} of ${N}</span>${s.last ? `<span class="dim">· Autopilot ran ${esc(lyHHMM(s.last))}</span>` : ''}</div>
    <div class="rb-hrow"><h2>${esc(s.name)}</h2>${pill(LY_ST[s.state][0] + (s.note ? ' · ' + s.note : ''), LY_ST[s.state][1])}<span class="ly-push"></span>
      ${next ? `<button class="btn sm" data-act="rbStep" data-id="${next.id}" title="${esc(next.name)}">Next step › <span class="dim">${next.n}. ${esc(next.name)}</span></button>` : ''}</div>
    <p class="rb-about">${esc(s.about)}</p>
    <div class="rb-meta"><span>Owner <b>${esc(s.who)}</b></span>${modes ? `<span>Autopilot · ${modes}</span>` : ''}${items.length ? `<span>${lyPlural(items.length, 'item')} in the queue</span>` : ''}</div>
    ${others.length > 1 ? `<div class="rb-views" role="tablist" aria-label="Screens for this step">${others.map(x => `<button role="tab" aria-selected="${x === v}" class="${x === v ? 'on' : ''}" data-act="rbView" data-id="${x}">${esc(rbViewName(x))}${lyCtB(x)}</button>`).join('')}</div>` : ''}
    ${prep.length ? `<div class="rb-prep">${prep.map(w => `<div class="rb-pi"><span class="rb-pt"><b>${esc(w.title)}</b>${w.detail ? `<span class="dim small">${esc(w.detail)}</span>` : ''}</span>${lyDue(w)}${lyCanAct(w) ? `<button class="btn sm pri" data-act="wDo" data-id="${esc(w.id)}">${esc(w.proposed.label)}</button>` : `<span class="dim small">for ${esc(w.role)}</span>`}</div>`).join('')}${items.length > prep.length ? `<div class="dim small">and ${lyPlural(items.length - prep.length, 'more item')} on this screen and in the work queue</div>` : ''}</div>` : ''}
  </section>`;
}
LYS.runbook = {
  top: () => { rbSetTop(); return `<div class="ly-top rb-top">${lyBrand(S.me?.firm)}${lyDateNav()}${lyPayIn()}<span class="ly-push"></span>${lySearch()}${lyUser()}</div>`; },
  side: () => {
    const tabs = [['today', 'Today'], ['weekly', 'Weekly'], ['monthly', 'Monthly'], ['all', 'All']];
    const body = { today: rbToday, weekly: rbWeekly, monthly: rbMonthly, all: () => lyNavList(lyNavGroups()) }[UI.rbTab] || rbToday;
    if (UI.rbTab === 'today') rbReveal();
    requestAnimationFrame(() => { const el = $('rb-pre'); if (el) el.innerHTML = rbBanner(S.view); });
    return `<div class="rb-rail"><div class="rb-tabs" role="tablist" aria-label="Today's steps">${tabs.map(([k, l]) => `<button role="tab" aria-selected="${UI.rbTab === k}" class="${UI.rbTab === k ? 'on' : ''}" data-act="rbTab" data-id="${k}">${l}</button>`).join('')}</div>${body()}</div>`;
  },
  // pre: the step banner above a screen that belongs to a step. It is
  // drawn again with the rail, so it follows the day's data as it loads.
  pre: v => `<div id="rb-pre">${rbBanner(v)}</div>`,
  key: e => {
    if (e.key !== '[' && e.key !== ']') return;
    const st = rbSteps(); if (!st) return;
    const cur = lyStepOf(S.view), i = cur ? st.findIndex(s => s.id === cur.id) : -1;
    const seq = e.key === ']' ? st.slice(i + 1) : st.slice(0, Math.max(0, i)).reverse();
    const to = seq.find(s => rbOpenView(s));
    if (to) { e.preventDefault(); A.rbStep(to.id); }
  },
};

Object.assign(A, {
  rbTab: id => { UI.rbTab = id; renderSide(); },
  rbStep: id => { const s = LY_STEPS.find(x => x.id === id), v = s && rbOpenView(s); if (!v) return; UI.rbStep = id; UI.rbTab = 'today'; A.go(v); },
  rbView: v => A.go(v),
});
