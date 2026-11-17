'use strict';
/* Ledgerline console: the E · Supervised Autopilot layout (layouts.js).
   Ledgerline does the work and people approve checkpoints. The header is a
   box to tell Ledgerline what to do (Ledgy plans it); the home has three
   columns: how much each automation does alone (Off, Suggest or Auto, within
   its cap), the close as a plan with the next checkpoint waiting for the
   user, and what Autopilot did today. Every other screen opens below the
   same header; All screens has the rest. */

Object.assign(UI, { pilSel: '' });
const PIL_RANK = { Off: 0, Human: 0, Suggest: 1, Auto: 2 };

LYS.autopilot = {
  side: () => '',
  top: () => { if (typeof grTopHeight === 'function') requestAnimationFrame(grTopHeight); return `<div class="ly-top pil-top">${lyBrand(S.me?.firm || '')}${lyDateNav()}
    ${lgAllowed() ? `<form class="pil-cmd" id="pil-cmd" role="search"><span class="pil-l" aria-hidden="true">L</span><input id="pil-cmd-q" aria-label="Tell Ledgerline what to do" placeholder="Tell Ledgerline what to do, e.g. which clients hold back the close?" autocomplete="off" maxlength="500"><button class="btn pri sm" type="submit">Plan it</button></form>` : '<span class="ly-push"></span>'}
    ${lySearch('Search')}${lyMenuBtn()}${lyUser(true)}</div>`; },
};
document.addEventListener('submit', e => {
  if (e.target.id !== 'pil-cmd') return;
  e.preventDefault();
  const q = ($('pil-cmd-q')?.value || '').trim(); if (!q || !lgAllowed()) return;
  LG.open = true; lgAsk(q); $('pil-cmd-q').value = '';
});

// pilPlan: the after-close steps as Autopilot's plan for the close.
function pilPlan() {
  const pol = Object.fromEntries((LY.ap?.policies || []).map(p => [p.id, p]));
  return lySteps().filter(s => s.s === 'eod').map(s => {
    const approve = s.items.some(w => w.proposed && lyCanAct(w));
    const auto = s.pol.some(id => pol[id]?.mode === 'Auto');
    const kind = s.state === 'done' ? 'done' : s.state === 'needs' ? (approve ? 'approve' : 'people') : 'later';
    const who = s.state === 'done' ? (s.runs.length && !s.items.length ? 'Autopilot' : s.who) : kind === 'later' && auto ? 'Autopilot' : s.who;
    return { ...s, kind, who };
  });
}
const PIL_KIND = { done: ['Done', 'good'], people: ['Waits on people', 'warn'], approve: ['Needs your approval', 'acc'], later: ['Still to run', 'nt'] };
// pilChecks: open items with a prepared action this user may take, overdue
// first, then by due time.
const pilChecks = () => lyWorkOpen().filter(w => w.proposed && lyCanAct(w)).sort((a, b) => (b.overdue - a.overdue) || String(a.due || '9').localeCompare(String(b.due || '9')));
const pilView = v => { const b = String(v || '').split(':')[0]; return viewAllowed(b) ? v : 'autopilot'; };
const pilStepOf = w => lySteps().find(s => s.items.some(x => x.id === w.id));

V.lyPilot = {
  load: async () => { await lyData(0); return LY; },
  html: () => {
    const ap = LY.ap;
    if (!ap) return ph('Supervised Autopilot', '') + '<div class="errbox"><b>The Autopilot view could not be loaded.</b></div>';
    const manual = ap.edition?.id === 'manual';
    return `<div class="pil">
      <aside class="pil-col pil-left">${pilLevels(ap, manual)}</aside>
      <main class="pil-col pil-mid">${manual ? `<div class="note warn"><b>${esc(ap.edition.name || 'Manual')} edition.</b> ${esc(ap.edition.about || 'Autopilot does not run; people do every step.')}</div>` : ''}${pilPlanCard(ap)}${pilCheckCard()}</main>
      <aside class="pil-col pil-right">${pilDid(ap)}<div class="pil-foot">Each job starts at <b>Suggest</b>. A firm moves it to <b>Auto</b> once it has run cleanly for long enough.</div></aside></div>`;
  },
};

function pilLevels(ap, manual) {
  const edit = has('Admin', 'Compliance officer') && !manual;
  const areas = [...new Set(ap.policies.map(p => p.area))];
  return `<section class="pil-card"><div class="pil-h"><h2>How much Autopilot does alone</h2><span class="dim small">Per job: off, suggest (a person approves) or auto. ${edit ? 'Every change is kept in the audit trail.' : manual ? 'Not in the manual edition.' : 'An Admin or the compliance officer changes these.'}</span></div>
    ${areas.map(a => `<div class="pil-area"><h3>${esc(a)}</h3>${ap.policies.filter(p => p.area === a).map(p => {
      const cap = PIL_RANK[p.cap] ?? 2;
      return `<div class="pil-lv"><div class="pil-lv-n"><span title="${esc(p.about)}">${esc(p.name)}</span>${p.cap !== 'Auto' ? `<small class="dim pil-cap" title="${esc(p.cap_why || '')}">${p.cap === 'Human' ? 'Stays with people' : 'At most ' + esc(p.cap)}${p.cap_why ? ': ' + esc(p.cap_why) : ''}</small>` : ''}</div>
        <span class="pil-seg" role="radiogroup" aria-label="${esc(p.name)}">${['Off', 'Suggest', 'Auto'].map(m => {
          const ok = PIL_RANK[m] <= cap, on = p.mode === m || (m === 'Off' && p.mode === 'Human');
          return `<button role="radio" aria-checked="${on}" class="${on ? 'on' : ''}" ${edit && ok && !on ? `data-act="pilMode" data-id="${esc(p.id)}" data-m="${m}"` : 'disabled'} ${!ok ? `title="${esc(p.cap_why || 'Not allowed for this job')}"` : ''}>${m}</button>`;
        }).join('')}</span></div>`;
    }).join('')}</div>`).join('')}</section>`;
}

function pilPlanCard(ap) {
  const plan = pilPlan(), cnt = k => plan.filter(p => p.kind === k).length;
  const close = plan.find(p => p.id === 'close'), run = has('Ops checker');
  return `<section class="pil-card pil-plan"><div class="pil-ph"><div><span class="dim small">Plan prepared by Autopilot</span><h1>Equity close for ${fmtDate(S.date)}</h1></div>
    <div class="acts">${has('Admin') ? `<button class="btn" data-act="pilPause" data-on="${ap.scheduled ? 0 : 1}" title="${ap.scheduled ? 'Autopilot runs its stages on its own on business days' : 'Stages run only when someone runs them'}">${ap.scheduled ? 'Pause the schedule' : 'Resume the schedule'}</button>` : ''}
      ${run ? `<button class="btn pri" data-act="pilRunClose" ${close?.state === 'done' ? 'disabled title="The equity day is closed"' : ''}>Run the close</button>` : ''}</div></div>
    <div class="pil-counts">${['done', 'people', 'approve', 'later'].map(k => cnt(k) ? pill(`${cnt(k)} ${{ done: 'done', people: 'wait on people', approve: cnt(k) === 1 ? 'needs your approval' : 'need your approval', later: 'still to run' }[k]}`, PIL_KIND[k][1]) : '').join('')}</div>
    <ol class="pil-steps">${plan.map((p, i) => `<li class="pil-st ${p.kind}"><span class="mono dim pil-n">${i + 1}</span><span class="pil-dot ${p.kind}" aria-label="${PIL_KIND[p.kind][0]}"></span>
      <span class="pil-st-b"><button class="linkish pil-st-name" data-act="go" data-id="${esc(p.views.find(viewAllowed) || 'autopilot')}">${esc(p.name)}</button><span class="dim small">${esc(p.note)}${p.last && p.state === 'done' ? '' : p.last ? ` · last run ${lyHHMM(p.last)}` : ''}</span></span>
      <span class="pil-who ${p.who === 'Autopilot' ? 'auto' : ''}">${esc(p.who)}</span></li>`).join('')}</ol></section>`;
}

function pilCheckCard() {
  const cs = pilChecks();
  if (!cs.length) {
    const people = lyWorkOpen().filter(lyMine).length;
    return `<section class="pil-card pil-check none"><span class="pil-kick">Checkpoint</span><h2>Nothing waits for your approval</h2><p class="dim">${people ? `${lyPlural(people, 'item')} in the work queue ${people === 1 ? 'needs' : 'need'} someone in your role, without a prepared action.` : 'Autopilot has nothing prepared for your role.'}</p>${people ? '<div class="acts"><button class="btn" data-act="go" data-id="autopilot">Open the work queue</button></div>' : ''}</section>`;
  }
  const w = cs.find(x => x.id === UI.pilSel) || cs[0], st = pilStepOf(w), more = cs.filter(x => x.id !== w.id);
  const pn = st ? pilPlan().findIndex(p => p.id === st.id) : -1;
  return `<section class="pil-card pil-check"><div class="pil-ph"><div><span class="pil-kick">Checkpoint${st ? ` · ${pn >= 0 ? `step ${pn + 1} of the plan, ` : ''}${esc(st.name)}` : ''}</span><h2>${esc(w.title)}</h2></div>${lyDue(w)}</div>
    ${w.detail ? `<p class="pil-detail">${esc(w.detail)}</p>` : ''}
    <div class="pil-meta"><span><span class="dim">Prepared by </span>Autopilot · ${esc(lyProc(w))}</span>${w.ucc ? `<span><span class="dim">Client </span><button class="linkish mono" data-act="client" data-id="${esc(w.ucc)}">${esc(w.ucc)}</button></span>` : ''}<span><span class="dim">For </span>${esc(w.role || 'anyone')}</span></div>
    <div class="acts"><button class="btn pri" data-act="wDo" data-id="${esc(w.id)}">${esc(w.proposed.label || 'Approve')}</button><button class="btn" data-act="wDismiss" data-id="${esc(w.id)}">Reject</button><button class="btn" data-act="go" data-id="${esc(pilView(w.view))}">Show the working</button>${lgAllowed() ? `<button class="btn" data-act="pilWhy" data-id="${esc(w.id)}">Ask Ledgy why</button>` : ''}</div>
    ${more.length ? `<div class="pil-more"><b>${lyPlural(more.length, 'more checkpoint')}</b>${more.slice(0, 6).map(x => `<button class="pil-mi" data-act="pilSel" data-id="${esc(x.id)}"><span>${esc(x.title)}</span>${lyDue(x)}</button>`).join('')}${more.length > 6 ? `<button class="linkish small" data-act="go" data-id="autopilot">All ${more.length} in the work queue</button>` : ''}</div>` : ''}</section>`;
}

function pilDid(ap) {
  const pol = Object.fromEntries(ap.policies.map(p => [p.id, p]));
  const g = {};
  for (const r of ap.runs.filter(r => r.date === S.date && r.policy)) {
    const x = g[r.policy] ||= { id: r.policy, done: 0, queued: 0, at: '', note: '', error: '' };
    x.done += r.done; x.queued += r.queued;
    if (r.at > x.at) { x.at = r.at; x.note = r.note || ''; x.error = r.error || ''; }
  }
  const rows = Object.values(g).filter(x => x.done || x.queued || x.error).sort((a, b) => b.at.localeCompare(a.at) || b.done - a.done);
  const quiet = Object.keys(g).length - rows.length;
  const viewOf = id => { const s = LY_STEPS.find(x => x.pol.includes(id)); const v = s?.views.find(viewAllowed) || (id.startsWith('ins_') && viewAllowed('insights') ? 'insights' : ''); return v || 'autopilot'; };
  return `<section class="pil-card"><div class="pil-h row"><h2>What Autopilot did</h2><span class="dim small">${nf(ap.done_today)} today${ap.minutes_saved_today ? ` · ${nf(ap.minutes_saved_today / 60, 1)} h saved` : ''}</span></div>
    ${rows.length ? rows.map(x => `<div class="pil-did"><span class="pil-dot ${x.error ? 'people' : 'done'}"></span><span class="pil-did-b"><b>${esc(pol[x.id]?.name || x.id)}</b>
      <span class="dim small pil-note">${x.error ? `<span class="neg">${esc(x.error)}</span>` : `${nf(x.done)} done${x.queued ? `, ${nf(x.queued)} for people` : ''}${x.note ? ' · ' + esc(x.note) : ''}`} · ${lyHHMM(x.at)}</span>
      <button class="linkish small" data-act="go" data-id="${esc(viewOf(x.id))}">Open ${esc((NAV_VIEWS.find(v => v[0] === viewOf(x.id)) || [, 'the screen'])[1])}</button></span></div>`).join('') : `<p class="dim small">Nothing has run for ${fmtDate(S.date)} yet.${has('Admin', 'Ops checker') ? ' Run a stage from the Autopilot screen, or switch the schedule on.' : ''}</p>`}
    ${quiet ? `<p class="dim small">${lyPlural(quiet, 'other automation')} ran and found nothing to do.</p>` : ''}
    ${ap.stp_pct ? `<div class="pil-stp">${bar(ap.stp_pct / 100)}<span class="small">${nf(ap.stp_pct, 0)}% of today’s work needed no one</span></div>` : ''}</section>`;
}

Object.assign(A, {
  pilMode: async (id, el) => {
    const p = (LY.ap?.policies || []).find(x => x.id === id); if (!p) return;
    try { await post('/v1/autopilot/policies', { id, mode: el.dataset.m, params: p.settings || {} }); toast(`${p.name}: ${el.dataset.m}`); LY.at = 0; render({ keep: true }); }
    catch (e) { toast(e.message, true); }
  },
  pilSel: id => { UI.pilSel = id; const y = window.scrollY; render({ keep: true }).then(() => window.scrollTo(0, y)); },
  pilRunClose: () => act(() => post('/v1/autopilot/run', { date: S.date, only: 'day_close' }), r => { LY.at = 0; return r.runs[0] ? `Equity close: ${r.runs[0].done} done, ${r.runs[0].queued} for people${r.runs[0].error ? ' · ' + r.runs[0].error : ''}` : 'The close is off'; }),
  pilPause: (id, el) => act(() => post('/v1/autopilot/schedule', { on: el.dataset.on === '1' }), r => { LY.at = 0; return r.on ? 'Autopilot runs on its own on business days' : 'Schedule paused: stages run only when someone runs them'; }),
  pilWhy: id => { const w = lyWorkOpen().find(x => x.id === id); if (!w) return; LG.open = true; lgAsk(`Why has Autopilot prepared "${w.title}"${w.ucc ? ' for ' + w.ucc : ''}, and what happens if I approve it?`); },
});
