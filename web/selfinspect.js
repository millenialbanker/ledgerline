'use strict';
/* Ledgerline console: Self-inspection (screen "selfinspect", Compliance).
   The tests an exchange inspector or internal auditor runs, run every
   night on the firm's own data: the score (pass rate weighted by
   severity), a 90-day trend, the open exceptions, and every test with its
   last result, history and rule. A failing test is a card on the
   Ledgerline board "Self-inspection"; this file also adds "Accept risk" to
   those cards. Server: internal/ops/selfinspect.go, selfinspect_lib.go,
   internal/api/selfinspect.go. Loaded after app.js and the board files.
   Names, actions and state are prefixed si. */

Object.assign(UI, { siArea: 'All', siShow: 'all', siFocus: '' });

const SI_ST = { pass: ['Passed', 'good'], fail: ['Failed', 'bad'], skipped: ['Not checked', 'nt'], muted: ['Muted', 'acc'], error: ['Could not run', 'warn'] };
const SI_EXC = { new: ['New', 'bad'], fixing: ['Being fixed', 'warn'], fixed: ['Fixed, waiting for the next run', 'acc'], accepted: ['Accepted risk', 'acc'], closed: ['Closed', 'good'] };
const SI_SEV = { high: ['High', 'bad'], medium: ['Medium', 'warn'], low: ['Low', 'nt'] };
const siPill = s => { const [l, k] = SI_ST[s] || [s, 'nt']; return pill(l, k); };
const siExcPill = s => { const [l, k] = SI_EXC[s] || [s, 'nt']; return pill(l, k); };
const siSev = s => { const [l, k] = SI_SEV[s] || [s, 'nt']; return pill(l, k); };
const siWhen = at => at ? `${fmtDate(at.slice(0, 10))} ${at.slice(11, 16)}` : '';
const siTrig = t => t === 'nightly' ? 'nightly run' : 'run now';

/* ---------- the screen ---------- */
V.selfinspect = {
  load: () => get('/v1/self-inspection'),
  html: d => {
    const L = d.last, can = d.can || {};
    if (UI.siFocus) { const f = UI.siFocus; UI.siFocus = ''; setTimeout(() => siExcModal(f), 0); }
    const acts = (can.run ? `<button class="btn pri" data-act="siRun">Run now</button>` : '') +
      (d.board ? `<button class="btn" data-act="siBoard" data-id="${esc(d.board)}">Open the board</button>` : can.set_up_board ? `<button class="btn" data-act="siSetUp">Set up the board</button>` : '') +
      (can.schedule ? `<button class="btn" data-act="siSched">Schedule</button>` : '') +
      (d.runs.length > 1 ? `<button class="btn" data-act="siPast">Past runs</button>` : '');
    const sched = d.settings.off ? '<span class="neg">The nightly run is switched off.</span>' : `Runs every business day at ${esc(d.settings.at)} (IST).`;
    let kp = '';
    if (L) {
      const sev = s => L.results.filter(r => r.status === 'fail' && r.severity === s).length;
      const tone = L.score >= 90 ? 'good' : L.score >= 70 ? 'warn' : 'bad';
      kp = `<div class="kpis">${kpi('Score', L.score.toFixed(1) + '%', `pass rate weighted by severity · ${L.pass_weight} of ${L.total_weight}`, tone)}${kpi('Failing', L.failed, `${sev('high')} high · ${sev('medium')} medium · ${sev('low')} low`, L.failed ? 'bad' : 'good')}${kpi('Passing', L.passed, `of ${L.passed + L.failed} counted`)}${kpi('Not counted', L.skipped + L.muted + L.errors, `${L.skipped} not checked · ${L.muted} muted${L.errors ? ' · ' + L.errors + ' could not run' : ''}`)}${kpi('Last run', fmtDate(L.date), `${siTrig(L.trigger)} · ${siWhen(L.at).slice(-5)} · ${esc(L.by)}`)}</div>`;
    }
    const open = d.exceptions.filter(x => x.status !== 'closed');
    return ph('Self-inspection', `The tests an exchange inspector or internal auditor runs, run on the firm’s own data after the day’s work, so a breach is found the next morning. ${sched} ${d.real} tests are real; ${d.waiting} ${d.waiting === 1 ? 'waits' : 'wait'} for data Ledgerline doesn’t have yet. A failing test becomes a card on the Self-inspection board and closes by itself when a later run passes.`, acts) +
      (L ? kp : `<div class="note">No run yet. ${can.run ? 'Press <b>Run now</b> to run every test on today’s data.' : 'The first nightly run will show here.'}</div>`) +
      `<div class="si-grid">
        <section class="panel si-trend" aria-labelledby="si-trend-h"><div class="hd"><h3 id="si-trend-h">Score, last 90 days</h3><small>One point a business day: that day’s last run</small></div><div class="bd">${siTrendHTML(d.trend)}</div></section>
        <section class="panel" aria-labelledby="si-lists-h"><div class="hd"><h3 id="si-lists-h">Exceptions by list</h3><small>As on the board</small></div><div class="bd">${siListsHTML(d)}</div></section>
      </div>
      <section class="panel si-exc" aria-labelledby="si-exc-h"><div class="hd"><h3 id="si-exc-h">Open exceptions · ${open.length}</h3><small>Owner, and due date by severity: high 3 days, medium 10, low 30</small></div>${siExcTable(d, open)}</section>
      <section class="panel" aria-labelledby="si-tests-h"><div class="hd"><h3 id="si-tests-h">Tests</h3><div class="tools">${segBtns('siArea', ['All', ...d.areas], 'si-t')}${segBtns('siShow', [['all', 'All'], ['fail', 'Failing'], ['waiting', 'Waiting for data']], 'si-t')}</div></div><div id="si-t">${P['si-t'](d)}</div></section>`;
  }
};

// The trend: one line (the score), 0–100, dates placed on the 90-day axis
// so gaps between runs show. Words are HTML, so they keep their size;
// hovering a day shows its numbers, and the table has them all.
function siTrendHTML(T) {
  if (!T || !T.length) return '<p class="dim small">The trend starts with the first run.</p>';
  const end = T[T.length - 1].date, t0 = Date.parse(end + 'T00:00:00Z') - 90 * 864e5, span = 90 * 864e5;
  const X = p => Math.max(0, Math.min(100, (Date.parse(p.date + 'T00:00:00Z') - t0) / span * 100)), Y = v => 100 - Math.max(0, Math.min(100, v));
  const line = T.length > 1 ? `<polyline points="${T.map(p => `${X(p).toFixed(2)},${Y(p.score).toFixed(2)}`).join(' ')}" vector-effect="non-scaling-stroke"/>` : '';
  const pts = T.map(p => `<i class="si-pt" style="left:${X(p).toFixed(2)}%;top:${Y(p.score).toFixed(2)}%"></i>`).join('');
  const w = Math.max(2, 100 / Math.max(T.length, 12));
  const cols = T.map((p, i) => `<span class="si-col" style="left:${Math.max(0, X(p) - w / 2).toFixed(2)}%;width:${w.toFixed(2)}%"><span class="si-tip${X(p) > 60 ? ' r' : ''}"><b>${esc(fmtDate(p.date))}</b><span>Score ${p.score.toFixed(1)}%</span><span>${p.failed} failing · ${p.passed} passing</span></span></span>`).join('');
  const lastP = T[T.length - 1], first = T[0];
  const ticks = [0, 30, 60, 90].map(k => { const dt = new Date(t0 + k * 864e5).toISOString().slice(0, 10); return `<span style="left:${(k / 90 * 100).toFixed(2)}%">${esc(fmtDate(dt).slice(0, 6))}</span>`; }).join('');
  const said = `Score ${first.score.toFixed(1)}% on ${fmtDate(first.date)}, ${lastP.score.toFixed(1)}% on ${fmtDate(lastP.date)}, ${T.length} days with a run`;
  return `<div class="si-line"><p class="si-now"><b class="mono">${lastP.score.toFixed(1)}%</b> <span class="dim">on ${esc(fmtDate(lastP.date))}${T.length > 1 ? ` · ${lastP.score >= first.score ? 'up' : 'down'} from ${first.score.toFixed(1)}% on ${esc(fmtDate(first.date))}` : ''}</span></p>
    <div class="si-plot" role="img" aria-label="${esc(said)}">
      <div class="si-area" aria-hidden="true"><span class="si-y" style="top:0">100</span><span class="si-y" style="top:50%">50</span><span class="si-y" style="top:100%">0</span><svg viewBox="0 0 100 100" preserveAspectRatio="none"><line x1="0" x2="100" y1="0" y2="0" vector-effect="non-scaling-stroke"/><line x1="0" x2="100" y1="50" y2="50" vector-effect="non-scaling-stroke"/><line class="si-base" x1="0" x2="100" y1="100" y2="100" vector-effect="non-scaling-stroke"/>${line}</svg>${pts}${cols}</div>
      <div class="si-x" aria-hidden="true">${ticks}</div></div>
    <details class="si-tbl"><summary>Show the numbers</summary><table class="si-mini"><caption class="si-sr">Score of each day's last run</caption><thead><tr><th scope="col">Day</th><th scope="col">Score</th><th scope="col">Failing</th><th scope="col">Passing</th><th scope="col">Run</th></tr></thead><tbody>${T.slice().reverse().map(p => `<tr><th scope="row">${esc(fmtDate(p.date))}</th><td>${p.score.toFixed(1)}%</td><td>${p.failed}</td><td>${p.passed}</td><td><button class="linkish mono" data-act="siShowRun" data-id="${esc(p.run)}">${esc(p.run)}</button></td></tr>`).join('')}</tbody></table></details></div>`;
}

// The exceptions per board list, with how many are overdue; closed ones
// count those of the last 30 days.
function siListsHTML(d) {
  const xs = d.exceptions, n = s => xs.filter(x => x.status === s).length, max = Math.max(1, ...Object.keys(SI_EXC).map(n));
  const od = xs.filter(x => x.status !== 'closed' && x.status !== 'accepted' && x.due && x.due < d.date).length;
  const high = xs.filter(x => x.status !== 'closed' && x.severity === 'high').length;
  const rows = Object.entries(SI_EXC).map(([k, [l, tone]]) => `<li><span class="si-ln">${esc(l)}${k === 'closed' ? ' <span class="dim">(30 days)</span>' : ''}</span><span class="si-lb"><i class="${tone}" style="width:${(n(k) / max * 100).toFixed(1)}%"></i></span><b class="mono">${n(k)}</b></li>`).join('');
  return `<ul class="si-lists">${rows}</ul><p class="small si-lsum">${od ? `<span class="neg"><b>${od}</b> overdue</span>` : 'None overdue'} · ${high} high severity open</p>`;
}

function siExcTable(d, open) {
  const can = d.can || {}, me = role();
  const mine = x => me === 'Admin' || me === 'Compliance officer' || me === x.owner;
  return tbl([['Test'], ['Found', 'r'], ['Status'], ['Owner'], ['Due'], ['']], open.map(x => {
    const od = x.status !== 'accepted' && x.due && x.due < d.date;
    const due = x.status === 'accepted' && x.accepted ? `review ${fmtDate(x.accepted.until)}` : fmtDate(x.due);
    const b = [];
    if (mine(x) && x.status !== 'fixing') b.push(`<button class="btn sm" data-act="siFixing" data-id="${esc(x.id)}">Being fixed</button>`);
    if (mine(x) && x.status !== 'fixed') b.push(`<button class="btn sm" data-act="siFixed" data-id="${esc(x.id)}">Fixed</button>`);
    if (can.accept && x.status !== 'accepted') b.push(`<button class="btn sm" data-act="siAccept" data-id="${esc(x.id)}">Accept risk</button>`);
    return `<tr><td class="wrap"><button class="linkish" data-act="siExc" data-id="${esc(x.id)}">${esc(x.title)}</button><div class="dim small">${siSev(x.severity)} ${esc(x.area)} · since ${esc(fmtDate(x.opened))}</div></td><td class="r mono">${nf(x.found)}</td><td>${siExcPill(x.status)}</td><td class="small">${esc(x.owner)}</td><td class="n ${od ? 'neg' : ''}">${due}${od ? '<div class="small">overdue</div>' : ''}</td><td><div class="tools si-acts">${b.join('')}</div></td></tr>`;
  }), { empty: 'No open exceptions.' });
}

P['si-t'] = d => {
  let ts = d.tests;
  if (UI.siArea !== 'All') ts = ts.filter(t => t.area === UI.siArea);
  if (UI.siShow === 'fail') ts = ts.filter(t => t.last && t.last.status === 'fail');
  if (UI.siShow === 'waiting') ts = ts.filter(t => !t.real);
  return tbl([['Test'], ['Severity'], ['Owner'], ['Last result'], ['Last 30 runs'], ['Rule']], ts.map(t => {
    const r = t.last;
    const res = !t.real ? pill('Waiting for data', 'nt') : r ? `${siPill(r.status)}${r.status === 'fail' || r.muted_as === 'fail' ? ` <span class="mono small">${nf(r.found)} found</span>` : ''}${t.mute ? `<div class="dim small">muted to ${esc(fmtDate(t.mute.until))}</div>` : ''}` : '<span class="dim">not run yet</span>';
    return `<tr><td class="wrap si-tname"><button class="linkish" data-act="siTest" data-id="${esc(t.id)}"><b>${esc(t.title)}</b></button><div class="dim small">${esc(t.area)} · ${esc(t.checks)}</div></td><td>${siSev(t.severity)}</td><td class="small">${esc(t.owner)}</td><td>${res}${t.exception_status ? `<div class="small">${siExcPill(t.exception_status)}</div>` : ''}</td><td>${siStrip(t.history)}</td><td class="small wrap si-src">${esc(t.source)}${t.to_confirm ? `<div class="si-tc"><b>To confirm:</b> ${esc(t.to_confirm)}</div>` : ''}</td></tr>`;
  }), { tall: true, empty: 'No test matches.' });
};

// A run's results as small squares, oldest first, each with its words.
function siStrip(h) {
  if (!h || !h.length) return '<span class="dim small">—</span>';
  return `<span class="si-strip" role="img" aria-label="${esc(h.filter(x => x.status === 'fail').length + ' failed of the last ' + h.length + ' runs')}">${h.map(x => `<i class="si-sq ${esc(x.status)}" title="${esc(fmtDate(x.date) + ': ' + (SI_ST[x.status] || [x.status])[0] + (x.found ? ', ' + x.found + ' found' : ''))}"></i>`).join('')}</span>`;
}

/* ---------- windows ---------- */
const siFind = id => (C.selfinspect?.exceptions || []).find(x => x.id === id);

async function siTestModal(id) {
  let d;
  try { d = await get(`/v1/self-inspection/tests/${encodeURIComponent(id)}`); } catch (e) { toast(e.message, true); return; }
  const t = d.test, r = t.last, can = C.selfinspect?.can || {};
  const mute = t.mute || (d.mutes || []).find(m => !m.ended_at && m.from <= (C.selfinspect?.date || '') && (C.selfinspect?.date || '') <= m.until);
  const ex = r ? `<div class="si-res"><div class="tools">${siPill(r.status)}${r.status === 'muted' ? ` <span class="small">(would have ${r.muted_as === 'fail' ? 'failed' : 'passed'})</span>` : ''}<span class="dim small">run ${esc(t.last_run)}</span></div><p>${esc(r.summary || '')}</p>${(r.examples || []).length ? `<ul class="si-ex">${r.examples.map(x => `<li class="mono small">${esc(x)}</li>`).join('')}</ul>` : ''}${(r.inputs || []).length ? `<p class="dim small">Read: ${r.inputs.map(i => `${esc(i.name)} of ${esc(fmtDate(i.date))}`).join(' · ')}</p>` : ''}</div>` : '';
  modal(mhd(esc(t.title), `${esc(t.area)} · ${esc(t.owner)} · ${(SI_SEV[t.severity] || [t.severity])[0]} severity`) + `<div class="bd">
    ${t.real ? '' : `<div class="note">${esc(t.waiting)}</div>`}
    <dl class="si-dl"><dt>What it checks</dt><dd>${esc(t.checks)}</dd><dt>Rule</dt><dd>${esc(t.source)}${t.to_confirm ? `<div class="si-tc"><b>To confirm:</b> ${esc(t.to_confirm)}</div>` : ''}</dd><dt>How to fix</dt><dd>${esc(t.fix)} <button class="linkish" data-act="siGo" data-id="${esc(t.screen)}">Open ${esc(t.screen_name)}</button></dd></dl>
    ${ex}
    <h4 class="si-h4">Last 90 days</h4>${tbl([['Day'], ['Result'], ['Found', 'r'], ['Run']], d.results.map(x => `<tr><td class="n">${esc(fmtDate(x.date))}</td><td>${siPill(x.status)}</td><td class="r mono">${x.found ? nf(x.found) : '—'}</td><td><button class="linkish mono" data-act="siShowRun" data-id="${esc(x.run)}">${esc(x.run)}</button></td></tr>`), { empty: 'Not run in the last 90 days.' })}
    ${d.exceptions.length ? `<h4 class="si-h4">Exceptions</h4>${tbl([['Exception'], ['Status'], ['How it was handled']], d.exceptions.map(x => `<tr><td class="mono small"><button class="linkish" data-act="siExc" data-id="${esc(x.id)}">${esc(x.id)}</button></td><td>${siExcPill(x.status)}</td><td class="small wrap">${esc(x.handled || '')}</td></tr>`))}` : ''}
    <h4 class="si-h4">Muted</h4>${mute ? `<p>Muted until <b>${esc(fmtDate(mute.until))}</b> by ${esc(mute.by)}: ${esc(mute.reason)}</p>` : '<p class="dim small">Not muted. A muted test still runs; its result is kept as muted and not counted.</p>'}
    </div><div class="ft"><button class="btn" data-act="close">Close</button>${can.mute && t.real ? (mute ? `<button class="btn" data-act="siUnmute" data-id="${esc(t.id)}">End the mute</button>` : `<button class="btn" data-act="siMute" data-id="${esc(t.id)}">Mute this test</button>`) : ''}</div>`);
}

function siMuteModal(id) {
  const t = (C.selfinspect?.tests || []).find(x => x.id === id);
  confirmModal('Mute this test', `${esc(t ? t.title : id)}: it still runs every night, but its result is kept as muted and does not count in the score or open a card until the end date. Your name and reason go to the audit trail.`,
    `<div class="fld"><label for="si-mr">Reason</label><input class="in" id="si-mr" placeholder="Why this test should not count for now"></div><div class="fld"><label for="si-mu">Until</label><input class="in" type="date" id="si-mu" value="${esc(addDaysJS(C.selfinspect?.date || S.date, 7))}"></div>`,
    'Mute', async () => {
      await post(`/v1/self-inspection/tests/${encodeURIComponent(id)}/mute`, { reason: $('si-mr').value.trim(), until: $('si-mu').value });
      closeModal(); toast('Muted; the audit trail has the reason'); await render({ keep: true });
    });
}

function siUnmuteModal(id) {
  confirmModal('End the mute', 'The test counts again from the next run. Your name and reason go to the audit trail.',
    `<div class="fld"><label for="si-ur">Why the mute ends early</label><input class="in" id="si-ur" placeholder="e.g. the batch went through"></div>`, 'End the mute', async () => {
      await post(`/v1/self-inspection/tests/${encodeURIComponent(id)}/unmute`, { reason: $('si-ur').value.trim() });
      closeModal(); toast('The test counts again'); await render({ keep: true });
    });
}

function siExcModal(id) {
  const x = siFind(id);
  if (!x) { toast('That exception is closed or no longer shown here.'); return; }
  const can = C.selfinspect?.can || {}, me = role(), mine = me === 'Admin' || me === 'Compliance officer' || me === x.owner;
  modal(mhd(esc(x.title), `${esc(x.id)} · ${(SI_SEV[x.severity] || [x.severity])[0]} severity · owner ${esc(x.owner)}`) + `<div class="bd">
    <div class="tools">${siExcPill(x.status)}<span class="small">${x.status === 'accepted' && x.accepted ? 'review by ' + esc(fmtDate(x.accepted.until)) : 'due ' + esc(fmtDate(x.due))}</span><span class="dim small">found first ${esc(fmtDate(x.opened))} (run ${esc(x.opened_run)}), last ${esc(fmtDate(x.last_date))}</span></div>
    <p>${esc(x.summary || '')}</p>${x.found ? `<p class="small"><b>${nf(x.found)}</b> found of ${nf(x.checked)} checked.</p>` : ''}
    ${(x.examples || []).length ? `<ul class="si-ex">${x.examples.map(e => `<li class="mono small">${esc(e)}</li>`).join('')}</ul>` : ''}
    <dl class="si-dl"><dt>How to fix</dt><dd>${esc(x.fix)} <button class="linkish" data-act="siGo" data-id="${esc(x.screen)}">Open the screen</button></dd><dt>Rule</dt><dd>${esc(x.source)}</dd><dt>So far</dt><dd>${esc(x.handled || '')}</dd></dl>
    <h4 class="si-h4">History</h4><ol class="si-hist">${(x.history || []).slice().reverse().map(h => `<li><b>${esc(h.what)}</b> <span class="dim small">${esc(siWhen(h.at))} · ${esc(h.by)}${h.role ? ' (' + esc(h.role) + ')' : ''}${h.run ? ' · ' + esc(h.run) : ''}</span>${h.note ? `<div class="small">${esc(h.note)}</div>` : ''}</li>`).join('')}</ol>
    </div><div class="ft"><button class="btn" data-act="close">Close</button>${x.status !== 'closed' && mine && x.status !== 'fixing' ? `<button class="btn" data-act="siFixing" data-id="${esc(x.id)}">Being fixed</button>` : ''}${x.status !== 'closed' && mine && x.status !== 'fixed' ? `<button class="btn" data-act="siFixed" data-id="${esc(x.id)}">Fixed</button>` : ''}${x.status !== 'closed' && can.accept && x.status !== 'accepted' ? `<button class="btn" data-act="siAccept" data-id="${esc(x.id)}">Accept risk</button>` : ''}</div>`);
}

function siAcceptModal(id, title, after) {
  confirmModal('Accept the risk', `${esc(title || '')}: the card moves to Accepted risk until the review date. If the test still fails then, it opens again. Your name, role and reason go to the audit trail.`,
    `<div class="fld"><label for="si-ar">Reason</label><textarea class="in" id="si-ar" rows="3" placeholder="Why the firm accepts this for now, and what it does meanwhile"></textarea></div><div class="fld"><label for="si-au">Review by</label><input class="in" type="date" id="si-au" value="${esc(addDaysJS(S.date, 90))}"></div>`,
    'Accept risk', async () => {
      await post(`/v1/self-inspection/exceptions/${encodeURIComponent(id)}/accept`, { reason: $('si-ar').value.trim(), until: $('si-au').value });
      closeModal(); toast('Risk accepted'); await (after ? after() : render({ keep: true }));
    });
}

function siFixedModal(id, after) {
  confirmModal('Mark it fixed', 'The card waits in “Fixed, waiting for the next run”. The next run checks it: it closes if the test passes, and goes back to Being fixed if not.',
    `<div class="fld"><label for="si-fn">What was fixed</label><input class="in" id="si-fn" placeholder="e.g. KRA statuses updated for the three clients"></div>`, 'Mark fixed', async () => {
      await post(`/v1/self-inspection/exceptions/${encodeURIComponent(id)}/status`, { status: 'fixed', note: $('si-fn').value.trim() });
      closeModal(); toast('Marked fixed; the next run checks it'); await (after ? after() : render({ keep: true }));
    });
}

async function siRunModal(id) {
  let r;
  try { r = await get(`/v1/self-inspection/runs/${encodeURIComponent(id)}`); } catch (e) { toast(e.message, true); return; }
  modal(mhd(`Run ${esc(r.id)}`, `${esc(fmtDate(r.date))} · ${siTrig(r.trigger)} at ${esc(siWhen(r.at))} by ${esc(r.by)} · score ${r.score.toFixed(1)}% · as it was then`) + `<div class="bd">` +
    tbl([['Test'], ['Severity'], ['Result'], ['Found', 'r'], ['What it found']], r.results.map(x => `<tr><td class="wrap"><b>${esc(x.title)}</b><div class="dim small">${esc(x.owner)}${(x.inputs || []).length ? ' · read ' + x.inputs.map(i => esc(fmtDate(i.date))).filter((v, i, a) => a.indexOf(v) === i).join(', ') : ''}</div></td><td>${siSev(x.severity)}</td><td>${siPill(x.status)}</td><td class="r mono">${x.found ? nf(x.found) : '—'}</td><td class="small wrap">${esc(x.summary || '')}${(x.examples || []).length ? `<div class="dim mono">${x.examples.slice(0, 2).map(esc).join('<br>')}</div>` : ''}</td></tr>`), { tall: true }) +
    `</div><div class="ft"><button class="btn" data-act="close">Close</button></div>`);
}

function siPastModal() {
  const d = C.selfinspect;
  modal(mhd('Past runs', 'Each run as it was: its results, and the dates of the data each test read.') + `<div class="bd">` +
    tbl([['Run'], ['Day'], ['How'], ['Score', 'r'], ['Failed', 'r'], ['Passed', 'r']], d.runs.map(r => `<tr><td><button class="linkish mono" data-act="siShowRun" data-id="${esc(r.id)}">${esc(r.id)}</button></td><td class="n">${esc(fmtDate(r.date))}</td><td class="small">${siTrig(r.trigger)} · ${esc(siWhen(r.at).slice(-5))} · ${esc(r.by)}</td><td class="r mono">${r.score.toFixed(1)}%</td><td class="r mono">${r.failed}</td><td class="r mono">${r.passed}</td></tr>`), { tall: true }) +
    `</div><div class="ft"><button class="btn" data-act="close">Close</button></div>`);
}

function siSchedModal() {
  const s = C.selfinspect.settings;
  confirmModal('Nightly self-inspection', 'Every test runs on business days at this time, after the day’s work, in every edition: it only finds what needs fixing. The compliance officer gets an email with the counts and a link, never a test or a client.',
    `<div class="form"><div class="fld"><label for="si-at">Time (IST)</label><input class="in mono" type="time" id="si-at" value="${esc(s.at)}"></div><div class="fld"><label class="chk"><input type="checkbox" id="si-on" ${s.off ? '' : 'checked'}> Run every night</label></div></div>`,
    'Save', async () => {
      await api('PUT', '/v1/self-inspection/settings', { at: $('si-at').value, off: !$('si-on').checked });
      closeModal(); toast('Schedule saved'); await render({ keep: true });
    });
}

/* ---------- actions ---------- */
Object.assign(A, {
  siRun: (id, el) => { el.disabled = true; act(() => post('/v1/self-inspection/run', {}), r => `Run ${r.id}: ${r.failed} failing, ${r.passed} passing · score ${r.score.toFixed(1)}%`).catch(() => { el.disabled = false; }); },
  siSched: () => siSchedModal(),
  siPast: () => siPastModal(),
  siShowRun: id => siRunModal(id),
  siTest: id => siTestModal(id),
  siExc: id => siExcModal(id),
  siGo: id => { closeModal(); if (!V[id]) { toast('That screen is not open to you.', true); return; } S.view = id; render(); window.scrollTo(0, 0); },
  siBoard: id => { closeModal(); S.view = 'boards'; render().then(() => { try { BOARDS.openBoard(id); } catch (e) {} }); },
  siSetUp: () => act(() => post('/v1/boards/ledgerline', { sources: ['inspection'] }), 'The Self-inspection board is set up'),
  siFixing: id => act(() => post(`/v1/self-inspection/exceptions/${encodeURIComponent(id)}/status`, { status: 'fixing', note: '' }), 'Moved to Being fixed'),
  siFixed: id => siFixedModal(id),
  siAccept: id => siAcceptModal(id, siFind(id)?.title),
  siMute: id => siMuteModal(id),
  siUnmute: id => siUnmuteModal(id),
});

/* ---------- the board's cards ---------- */
if (typeof BS_KIND !== 'undefined') { BS_KIND.inspection = 'Self-inspection'; BS_VIEW.inspection = 'selfinspect'; }
if (typeof A.bsOpen === 'function') {
  const open = A.bsOpen;
  A.bsOpen = (k, el) => { if (String(k).startsWith('inspection|')) UI.siFocus = k.slice(11); return open(k, el); };
}
if (typeof BOARDS !== 'undefined') {
  BOARDS.cardButtons.push({
    id: 'siAccept', icon: 'check', label: 'Accept risk', group: 'actions', order: 15,
    show: card => card.link?.kind === 'inspection' && role() === 'Compliance officer' && !['Accepted risk', 'Closed'].includes(card.link.status),
    run: card => siAcceptModal(card.link.id, card.title, () => BOARDS.reload()),
  });
}
