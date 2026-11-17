'use strict';
/* Ledgerline console: the C · Workspaces layout (layouts.js).
   A hub per department across the top (the classic sidebar's groups:
   Today, Operations, Risk, Money, Compliance and so on), each with its own
   menu beside the page: its daily steps in order, its screens, and its
   weekly and monthly work. A workspace's hub (V.lyHub) shows its steps as
   cards with what is waiting and prepared, then its other screens. */

// NAV group → workspace label, in the order of the tabs.
const WS_LABEL = { Overview: 'Today', Operations: 'Operations', Risk: 'Risk', Accounts: 'Money', Compliance: 'Compliance', Investments: 'Investments', Depository: 'Depository', Business: 'Business', Admin: 'Admin' };
// A step's area → the workspace that runs it.
const WS_AREA = { Operations: 'Operations', Risk: 'Risk', Money: 'Accounts', Compliance: 'Compliance' };
const wsGroups = () => lyNavGroups().filter(([g]) => WS_LABEL[g]);
const wsLabel = g => WS_LABEL[g] || g;
// wsOf: the workspace a screen belongs to.
const wsOf = v => (wsGroups().find(([, items]) => items.some(([k]) => k === v)) || [])[0];
function wsCur() {
  const gs = wsGroups(), g = wsOf(S.view), st = lyStepOf(S.view);
  // A step's screen opened from a workspace's daily steps stays in that
  // workspace, even when the screen's menu group is another one.
  const held = st && UI.rbStep === st.id && (UI.wsCur === 'Overview' || WS_AREA[st.area] === UI.wsCur);
  if (g && !held) UI.wsCur = g;
  if (!gs.some(([x]) => x === UI.wsCur)) UI.wsCur = (gs.find(([x]) => x === 'Operations') || gs[0] || ['Overview'])[0];
  return UI.wsCur;
}
const wsSteps = (g, st) => st.filter(s => g === 'Overview' || WS_AREA[s.area] === g);
const wsSetTop = () => requestAnimationFrame(() => { const t = $('top'); if (t && lyCur() === 'workspaces') document.body.style.setProperty('--ly-top', t.offsetHeight + 'px'); });
const wsDate = () => { const d = new Date(S.date + 'T00:00:00'); return isNaN(d) ? fmtDate(S.date) : d.toLocaleDateString('en-IN', { weekday: 'long', day: 'numeric', month: 'short', year: 'numeric' }); };
const wsStepsNow = () => LY.ap && LY.date === S.date ? lySteps() : null;

LYS.workspaces = {
  top: () => {
    wsSetTop();
    const cur = wsCur();
    return `<div class="ly-top ws-top"><div class="ws-row">${lyBrand(S.me?.firm)}<span class="ly-push"></span>${lyDateNav()}${lySearch()}${lyUser(true)}</div>
      <nav class="ws-tabs" aria-label="Workspaces">${wsGroups().map(([g, items]) => {
        const att = items.some(([k]) => lyCt(k) > 0);
        return `<button class="ws-tab ${g === cur ? 'on' : ''}" data-act="wsTab" data-id="${esc(g)}" ${g === cur ? 'aria-current="page"' : ''}>${esc(wsLabel(g))}${att ? '<i class="ws-dot" aria-label="needs attention"></i>' : ''}</button>`;
      }).join('')}</nav></div>`;
  },
  side: () => {
    const g = wsCur(), grp = wsGroups().find(([x]) => x === g), st = wsStepsNow();
    const steps = st ? wsSteps(g, st) : [];
    const cur = lyStepOf(S.view)?.id;
    const daily = !st ? '<p class="loading small">Loading the day…</p>' : steps.length ? `<nav class="ly-navg ws-daily" aria-label="Daily, in order"><h6>Daily, in order</h6>${steps.map(s => `<button class="ly-nav ws-step ${s.id === cur && S.view !== 'lyHub' ? 'on' : ''}" data-act="wsStep" data-id="${s.id}" ${s.views.some(viewAllowed) ? '' : 'disabled'}>${lyMark(s, '')}<span class="ws-sn">${esc(s.name)}</span>${s.state === 'needs' ? `<span class="ct">${s.items.length || '!'}</span>` : ''}</button>`).join('')}</nav>` : '';
    const periodic = ['Overview', 'Operations', 'Compliance'].includes(g) ? [['periodic', 'Periodic work'], ['circulars', 'Circulars & duties']].filter(([v]) => viewAllowed(v)) : [];
    return `<div class="ws-rail">
      <button class="ly-nav ws-hub ${S.view === 'lyHub' ? 'on' : ''}" data-act="wsTab" data-id="${esc(g)}"><span>${esc(wsLabel(g))} overview</span></button>
      ${daily}
      ${grp ? lyNavList([[g === 'Overview' ? 'Screens' : 'Also here', grp[1]]]) : ''}
      ${periodic.length ? lyNavList([['Weekly and monthly', periodic]]) : ''}
      <span class="ws-fill"></span>
      <button class="btn sm ws-all" data-act="lyMenu">${lySvg('menu', 14)} All screens</button></div>`;
  },
};

V.lyHub = {
  load: async () => { await lyData(0); return {}; },
  html: () => {
    const g = wsCur(), grp = wsGroups().find(([x]) => x === g) || [g, []], st = lySteps(), steps = wsSteps(g, st);
    const shown = g === 'Overview' ? steps.filter(s => s.state === 'needs') : steps;
    const payIn = C.dashboard?.extra?.pay_in;
    const acts = g === 'Operations' ? [viewAllowed('files') ? '<button class="btn" data-act="go" data-id="files">Import the day’s files</button>' : '', viewAllowed('dashboard') ? '<button class="btn" data-act="go" data-id="dashboard">Close the day</button>' : ''].join('') : '';
    const strip = steps.length ? `<ol class="ws-strip" aria-label="Daily steps" style="grid-template-columns:repeat(${Math.min(steps.length, 8)},minmax(0,1fr))">${steps.map(s => `<li><button data-act="wsStep" data-id="${s.id}" ${s.views.some(viewAllowed) ? '' : 'disabled'}><i class="ws-bar ${s.state}"></i><b>${s.n}. ${esc(s.name)}</b><span class="${s.state === 'needs' ? 'neg' : ''}">${esc(s.note)}</span></button></li>`).join('')}</ol>` : '';
    const card = s => {
      const v = s.views.find(viewAllowed), items = s.items.slice(0, 3);
      return `<section class="ws-card ${s.state}"><div class="ws-ch"><h2>${esc(s.name)}</h2>${pill(LY_ST[s.state][0], LY_ST[s.state][1])}</div>
        <p class="ws-note">${esc(s.note)}</p>
        ${items.length ? `<div class="ws-items">${items.map(w => `<div class="ws-it"><span class="ws-it-t"><b>${esc(w.title)}</b>${w.ucc ? ` <button class="linkish mono small" data-act="client" data-id="${esc(w.ucc)}">${esc(w.ucc)}</button>` : ''}<span class="dim small">${esc(w.role || '')}</span></span>${lyDue(w)}${w.proposed && lyCanAct(w) ? `<button class="btn sm pri" data-act="wDo" data-id="${esc(w.id)}">${esc(w.proposed.label)}</button>` : ''}</div>`).join('')}${s.items.length > 3 ? `<div class="dim small">and ${lyPlural(s.items.length - 3, 'more item')}</div>` : ''}</div>` : `<p class="ws-about">${esc(s.about)}</p>`}
        ${v ? `<button class="linkish ws-open" data-act="wsStep" data-id="${s.id}">Open ${esc((NAV_VIEWS.find(x => x[0] === v) || [, v])[1])} →</button>` : ''}</section>`;
    };
    const stepViews = new Set(steps.map(s => s.views.find(viewAllowed)));
    const tiles = grp[1].filter(([k]) => g !== 'Overview' || !stepViews.has(k));
    return ph(esc(wsLabel(g)), `${esc(wsDate())}${payIn ? ` · pay-in ${esc(fmtDate(payIn))}` : ''}`, acts) +
      strip +
      (shown.length ? `<div class="ws-cards">${shown.map(card).join('')}</div>` : g === 'Overview' && steps.length ? '<div class="note">Nothing in the day needs someone right now.</div>' : '') +
      (tiles.length ? `<div class="ws-tiles-h"><h3>${steps.length ? 'Also in ' : ''}${esc(wsLabel(g))}</h3></div><div class="ws-tiles">${tiles.map(([k, l]) => `<button class="ws-tile" data-act="go" data-id="${k}"><b>${esc(l)}</b>${lyCt(k) ? `<span class="ws-tc">${lyPlural(lyCt(k), 'item')}</span>` : '<span class="dim small">Open</span>'}</button>`).join('')}</div>` : '');
  },
};

Object.assign(A, {
  wsTab: g => { UI.wsCur = g; A.go('lyHub'); },
  wsStep: id => { const s = LY_STEPS.find(x => x.id === id), v = s && s.views.find(viewAllowed); if (!v) return; UI.rbStep = id; A.go(v); },
});
