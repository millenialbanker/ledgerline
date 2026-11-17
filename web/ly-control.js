'use strict';
/* Ledgerline console: the D · Control room layout (layouts.js).
   One dense screen for the whole day: a timeline of the day's steps, the
   day's numbers, margin, Autopilot by stage, contract notes, what needs a
   person and the close checks. The layout is dark unless the person chose
   light (layouts.js, lyApply); the screen itself uses the theme's colours,
   so it also reads well opened from another layout. Every block opens its
   screen. Numbers come from /v1/dashboard and /v1/autopilot (lyData). */

// The timeline runs from 06:55 to 23:55, IST.
const CTL_T0 = 6 * 60 + 55, CTL_SPAN = 17 * 60;
const CTL_BANDS = { bod: ['06:55', '09:15'], intraday: ['09:15', '15:30'], eod: ['15:30', '18:45'], commodity: ['23:30', '23:55'] };
const ctlMin = t => { const [h, m] = String(t).split(':').map(Number); return h * 60 + m; };
const ctlPos = t => Math.max(0, Math.min(100, (ctlMin(t) - CTL_T0) / CTL_SPAN * 100));
// The screen a work item or step opens; the inbox when another layout has one.
const ctlQueueView = () => typeof V.lyInbox === 'object' ? 'lyInbox' : viewAllowed('autopilot') ? 'autopilot' : 'dashboard';
const ctlStepView = st => st.views.find(viewAllowed) || 'dashboard';
const ctlWork = () => lyWorkOpen().slice().sort((a, b) => (b.overdue - a.overdue) || String(a.due || '9').localeCompare(String(b.due || '9')));

LYS.control = {
  side: () => '',
  top: () => `<div class="ly-top ctl-top">${lyBrand('Control room · ' + (S.me?.firm || ''))}
    <span class="mono ctl-clock" title="Now, India time">${esc(lyNow().slice(11))} IST</span>
    ${lyDateNav()}${lyPayIn()}<span class="ly-push"></span>${lySearch('Search or run a command')}${lyMenuBtn()}${lyUser(true)}</div>`,
};

// The day so far: the sessions as bands, each step as a mark at the time
// Autopilot last ran it today (on this trade date), else at its usual time.
function ctlTimeline(steps) {
  const today = S.date === lyNow().slice(0, 10), now = lyNow().slice(11);
  const at = st => st.last && st.last.slice(0, 10) === S.date ? lyHHMM(st.last) : st.at;
  const bands = LY_SESS.map(([k, name]) => { const [a, b] = CTL_BANDS[k]; return `<div class="ctl-band ${ctlPos(b) > 96 ? 'end' : ''}" style="left:${ctlPos(a).toFixed(2)}%;width:${(ctlPos(b) - ctlPos(a)).toFixed(2)}%"><span>${esc(name)}</span><small class="mono">${a}</small></div>`; }).join('');
  // Marks closer than a mark's width go on a second lane below the track.
  let lastPos = -9, lane = 0;
  const marks = steps.filter(st => st.state !== 'na').map(st => ({ st, t: at(st) })).sort((x, y) => ctlPos(x.t) - ctlPos(y.t)).map(({ st, t }) => {
    const p = ctlPos(t); lane = p - lastPos < 1.3 ? 1 - lane : 0; lastPos = p;
    return `<button class="ctl-mark ${st.state} ${lane ? 'low' : ''}" style="left:${p.toFixed(2)}%" data-act="go" data-id="${ctlStepView(st)}" title="${esc(`${t} · ${st.n}. ${st.name}: ${st.note}`)}" aria-label="${esc(`Step ${st.n}, ${st.name}: ${LY_ST[st.state][0]}, ${st.note}`)}"></button>`;
  }).join('');
  const nowLine = today && ctlMin(now) >= CTL_T0 ? `<div class="ctl-now" style="left:${ctlPos(now).toFixed(2)}%"><span class="mono">now ${esc(now)}</span></div>` : '';
  const cnt = s => steps.filter(x => x.state === s).length;
  return `<section class="ctl-pn ctl-day" aria-label="The day so far">
    <div class="ctl-row"><h1>The day so far</h1><span class="dim small">${cnt('done')} done · ${cnt('needs')} need a person · ${cnt('wait')} not yet</span>
      <div class="ctl-legend"><span><i class="ctl-k done"></i>Done</span><span><i class="ctl-k needs"></i>Needs a person</span><span><i class="ctl-k wait"></i>Not yet</span></div></div>
    <div class="ctl-tl"><div class="ctl-track"></div>${bands}${marks}${nowLine}</div>
    <div class="ctl-steps">${steps.filter(st => st.state === 'needs').map(st => `<button class="ctl-chip" data-act="go" data-id="${ctlStepView(st)}"><i class="ctl-k needs"></i><b>${st.n}. ${esc(st.name)}</b><span>${esc(st.note)}</span></button>`).join('') || '<span class="dim small">No step needs a person.</span>'}</div></section>`;
}
function ctlKpis(d, ap) {
  const open = d.recon.OPEN || 0, br = d.risk.Breach || 0;
  const tiles = [
    ['Turnover', cmp(d.turnover), `${nf(d.clients_traded)} clients traded`, '', 'dashboard'],
    ['Exchange trades', nf(d.trades), `${nf(d.oms_fills)} front-office fills`, '', 'files'],
    ['Open breaks', open, d.recon.total ? `${d.recon.total} found today` : 'not reconciled yet', open ? 'bad' : d.recon.total ? 'good' : '', 'recon'],
    ['Margin breaches', br, `short ${cmp(d.short)}`, br ? 'bad' : 'good', 'rms'],
    ['Client funds cover', num(d.funds.cover).toFixed(2) + '×', 'bank and clearing vs client credits', d.funds.ok ? 'good' : 'bad', 'bank'],
    ['Penalty exposure', inr(d.penalty), 'short-collection estimate', num(d.penalty) > 0 ? 'warn' : 'good', 'margin'],
    ['Open alerts', d.alerts.open || 0, `${d.alerts.high || 0} high severity`, d.alerts.high ? 'bad' : '', 'surv'],
    ['Work queue', ap ? ap.open : '—', ap ? `${ap.overdue} overdue` : '', ap?.overdue ? 'warn' : '', ctlQueueView()],
  ];
  return `<div class="ctl-kpis">${tiles.map(([l, v, s, tone, go]) => `<button class="ctl-pn ctl-kpi" ${viewAllowed(go) || go.startsWith('ly') ? `data-act="go" data-id="${go}"` : 'disabled'}><span>${esc(l)}</span><b class="${tone}">${v}</b><small>${esc(s)}</small></button>`).join('')}</div>`;
}
function ctlMargin(d) {
  const bk = d.util_buckets || [], tot = Math.max(1, sum(bk, b => b.count)), cls = ['good', 'acc', 'warn', 'bad'];
  return `<button class="ctl-pn ctl-span5" data-act="go" data-id="rms"><h2 class="ctl-pt">Margin utilisation · ${lyPlural(d.risk.WithMargin || 0, 'client')} with positions</h2>
    <div class="ctl-bar">${bk.map((b, i) => b.count ? `<i class="${cls[i]}" style="flex:${b.count / tot}"></i>` : '').join('')}</div>
    <div class="ctl-buckets">${bk.map((b, i) => `<span><b class="${i === 3 && b.count ? 'bad' : ''}">${b.count}</b><small>${esc(b.label)}</small></span>`).join('')}</div>
    <div class="ctl-foot">Required <b class="mono">${cmp(d.required)}</b> Collected <b class="mono">${cmp(d.collected)}</b></div></button>`;
}
function ctlStages(ap) {
  const runs = (ap.runs || []).filter(r => r.date === S.date && r.policy);
  const rows = (ap.stages || []).map(s => { const rs = runs.filter(r => r.stage === s.stage), dn = sum(rs, r => r.done || 0), q = sum(rs, r => r.queued || 0), t = Math.max(1, dn + q); return { name: s.name, dn, q, t, ran: rs.length }; });
  return `<button class="ctl-pn ctl-span4" data-act="go" data-id="${viewAllowed('autopilot') ? 'autopilot' : ctlQueueView()}"><h2 class="ctl-pt">Autopilot by stage</h2>
    ${rows.map(r => `<div class="ctl-stage"><div><span>${esc(r.name)}</span><small>${r.ran ? `${r.dn} done · ${r.q} for people` : 'not run'}</small></div><div class="ctl-bar thin">${r.ran ? `<i class="good" style="flex:${r.dn / r.t}"></i><i class="warn" style="flex:${r.q / r.t}"></i>` : ''}</div></div>`).join('')}
    <small class="ctl-sub">${nf(ap.done_today)} actions done by itself today · ${Math.round(ap.stp_pct || 0)}% needed no one · ${((ap.minutes_saved_today || 0) / 60).toFixed(1)} h saved</small></button>`;
}
function ctlNotes(d) {
  const t = d.notes.total || 0, s = d.notes.Sent || 0, b = d.notes.Bounced || 0, c = 2 * Math.PI * 26, f = t ? s / t : 0;
  return `<button class="ctl-pn ctl-span3" data-act="go" data-id="notes"><h2 class="ctl-pt">Contract notes</h2>
    <div class="ctl-notes"><svg viewBox="0 0 64 64" width="64" height="64" role="img" aria-label="${s} of ${t} sent"><circle cx="32" cy="32" r="26" class="ctl-ring"/><circle cx="32" cy="32" r="26" class="ctl-ring on" stroke-dasharray="${(c * f).toFixed(1)} ${c.toFixed(1)}" transform="rotate(-90 32 32)"/></svg>
    <div>${t ? `<span><b>${s}</b> of ${t} sent</span>${b ? `<span class="warn">${b} bounced · paper copy due</span>` : t > s ? `<span class="warn">${t - s} not delivered</span>` : '<span class="good">All delivered</span>'}` : '<span class="dim">Not generated yet</span>'}</div></div></button>`;
}
function ctlNeeds() {
  const w = ctlWork(), top = w.slice(0, 7);
  return `<section class="ctl-pn ctl-span7"><div class="ctl-row"><h2 class="ctl-pt">Needs a person · ${w.length}</h2><button class="linkish small" data-act="go" data-id="${ctlQueueView()}">Open the queue →</button></div>
    ${top.map(x => `<button class="ctl-att" data-act="go" data-id="${esc(x.view)}"><i class="ctl-k needs ${x.overdue ? 'over' : ''}"></i><span><b>${esc(x.title)}</b><small>${esc(x.detail || lyProc(x))}${x.overdue ? ' · overdue' : x.due ? ' · due ' + esc(x.due.slice(11)) : ''}</small></span><em>${esc(x.role || 'Anyone')}</em></button>`).join('') || '<div class="dim small">Nothing waiting for a person.</div>'}
    ${w.length > top.length ? `<small class="ctl-sub">and ${w.length - top.length} more in the queue</small>` : ''}</section>`;
}
function ctlChecks(d) {
  const parts = (d.eod.parts || []).filter(p => p.needed);
  // Checks that fail are listed; those that pass fold into one line.
  const part = p => { const ch = p.checks || [], bad = ch.filter(c => !c.ok), ok = ch.filter(c => c.ok);
    return `<div class="ctl-part"><b>${esc(p.name)}</b> ${p.closed ? pill('Closed ' + esc((p.closed_at || '').slice(11, 16)), 'good') : pill(`${ok.length} of ${ch.length} checks pass`, bad.length ? 'warn' : 'acc')}</div>
      ${p.closed ? '' : bad.map(c => `<button class="ctl-check" data-act="go" data-id="${esc(viewAllowed(c.view) ? c.view : 'dashboard')}"><i class="ctl-k needs"></i><span>${esc(c.name)}<small>${esc(c.detail || '')}</small></span></button>`).join('')}
      ${ok.length && !p.closed ? `<div class="ctl-check ok"><i class="ctl-k done"></i><span>${esc(ok.map(c => c.name).join(' · '))}</span></div>` : ''}`; };
  return `<section class="ctl-pn ctl-span5"><div class="ctl-row"><h2 class="ctl-pt">The close</h2><button class="linkish small" data-act="go" data-id="dashboard">${esc(eodStatus(d.eod))}</button></div>
    ${parts.map(part).join('') || '<div class="dim small">No close needed for this date.</div>'}</section>`;
}

V.lyControl = {
  load: async () => { await lyData(0); return LY; },
  html: L => {
    const d = L.d, ap = L.ap;
    const head = `<div class="ctl-head"><div><h1>Control room</h1><p>${esc(S.me?.firm || '')} · ${fmtDate(S.date)}${d?.extra?.pay_in ? ` · pay-in ${fmtDate(d.extra.pay_in)}` : ''}</p></div><span class="dim small">Refreshes every minute</span></div>`;
    if (!d) return `<div class="ctl">${head}<div class="note warn">The firm’s numbers for the day aren’t available to your login. ${ap ? 'Your work queue is below.' : ''}</div>${ap ? `<div class="ctl-grid">${ctlNeeds()}</div>` : ''}</div>`;
    const steps = lySteps(L);
    return `<div class="ctl">${head}${ctlTimeline(steps)}${ctlKpis(d, ap)}
      <div class="ctl-grid">${ctlMargin(d)}${ap ? ctlStages(ap) : ''}${ctlNotes(d)}${ctlNeeds()}${ctlChecks(d)}</div></div>`;
  },
};

// The control room keeps itself current while it is on screen.
setInterval(() => { if (S.view === 'lyControl' && S.me && !document.querySelector('.md') && !LY.k && !document.hidden) render({ keep: true }); }, 60000);
