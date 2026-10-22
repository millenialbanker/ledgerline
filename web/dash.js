'use strict';
/* Ledgerline console: the dashboard and its widgets.
   Each user arranges their own dashboard: which widgets, in what order and at
   what size (columns of 12, rows of 52px). Layouts are saved on the server
   for the user; an Admin can save one as the firm default. Every number comes
   from /v1/dashboard. Loaded after app.js and uses its helpers. */

/* ============================== catalogue ============================== */
const DASH_ROW = 52, DASH_GAP = 14, DASH_COLS = 12;

// Settlement and bank statuses that need someone.
const SETT_EXC = ['Short delivery', 'Funds shortfall', 'Unpledge & pay-in'];

function dashAttention(d) {
  const open = d.recon.OPEN || 0, breach = d.risk.Breach || 0;
  const sett = d.settlement, exc = SETT_EXC.reduce((s, k) => s + (sett[k] || 0), 0);
  const bankAtt = (d.bank['Third-party'] || 0) + (d.bank.Unidentified || 0);
  const att = [];
  if (breach) att.push(['bad', 'rms', `${breach} client${breach > 1 ? 's' : ''} in margin breach`, `${cmp(d.extra.breach_amount)} uncovered · square off or collect funds`]);
  if (open) att.push(['bad', 'recon', `${open} open reconciliation break${open > 1 ? 's' : ''}`, 'Resolve before EOD']);
  if (!d.billed && d.trades) att.push(['warn', 'billing', 'Bills not posted', 'Run billing after reconciliation']);
  if (exc) att.push(['warn', 'settlement', `${exc} settlement exception${exc > 1 ? 's' : ''} for ${fmtDate(d.extra.pay_in)}`, 'Short deliveries, unpledge requests and funds shortfalls']);
  if (bankAtt) att.push(['warn', 'bank', `${bankAtt} bank credit${bankAtt > 1 ? 's' : ''} need attention`, 'Third-party or unidentified receipts']);
  if (d.pending_overrides) att.push(['acc', 'rms', `${d.pending_overrides} limit override${d.pending_overrides > 1 ? 's' : ''} awaiting checker`, 'Maker-checker approval required']);
  if (d.pending_pledges) att.push(['acc', 'collateral', `${d.pending_pledges} pledge request${d.pending_pledges > 1 ? 's' : ''} pending`, 'Accept or reject at the depository']);
  if (d.alerts.high) att.push(['bad', 'surv', `${d.alerts.high} high-severity surveillance alert${d.alerts.high > 1 ? 's' : ''}`, 'Review and respond']);
  if ((d.notes.total || 0) - (d.notes.Sent || 0) > 0) att.push(['acc', 'notes', `${d.notes.total - (d.notes.Sent || 0)} contract notes not delivered`, 'Must reach clients within 24 hours']);
  if (+d.extra.inactive_due) att.push(['warn', 'inactive', `${d.extra.inactive_due} account${+d.extra.inactive_due > 1 ? 's' : ''} due to be flagged inactive`, 'No activity for 24 months: flag in the UCC database']);
  if (+d.extra.reactivation_pending) att.push(['acc', 'inactive', `${d.extra.reactivation_pending} reactivation request${+d.extra.reactivation_pending > 1 ? 's' : ''} awaiting checker`, 'IPV and KRA checks recorded']);
  if (d.reports_due) att.push(['acc', 'reports', `${d.reports_due} daily report${d.reports_due > 1 ? 's' : ''} not uploaded`, 'Generate, validate and upload']);
  if (d.extra.backup_problem) att.unshift(['bad', 'backups', 'Backups need attention', d.extra.backup_problem]);
  if (+d.extra.holidays_pending) att.push(['acc', 'circulars:hol', `Exchange holiday list waiting for approval`, 'Fetched from the exchanges or uploaded; check it and approve so settlement dates follow it']);
  else if (d.extra.holiday_warning) att.push(['warn', 'circulars:hol', 'Exchange holiday calendar', d.extra.holiday_warning]);
  return att;
}

const dEmpty = t => `<div class="empty">${t}</div>`;
// A list of label / count rows with a proportional bar, each opening a screen.
function dRows(rows, view, color) {
  const max = Math.max(1, ...rows.map(r => r[1]));
  return rows.length ? `<div class="dw-rows">${rows.map(r => `<button data-act="go" data-id="${r[3] || view}"><span>${esc(r[0])}</span><i><b style="width:${(r[1] / max * 100).toFixed(1)}%;background:${r[2] || color || 'var(--accent)'}"></b></i><em>${nf(r[1])}</em></button>`).join('')}</div>` : '';
}

/* Each widget: title, what it shows (for the catalogue), default and minimum
   size, and either kpi(d) → [label, value, sub, tone] or body(d) → html
   (sub(d) for the header's small text, go for the screen it opens). */
const DW = {
  proc: { t: 'Day progress', about: 'Files, reconciliation, billing, RMS, contract notes and EOD for the day, step by step.', w: 12, h: 2, mw: 6, mh: 2, bare: true,
    body: d => {
      const open = d.recon.OPEN || 0, breach = d.risk.Breach || 0, sent = d.notes.Sent || 0;
      const st = (done, alert) => done ? (alert ? 'alert' : 'done') : 'pend';
      return `<div class="proc">
        <div class="done"><small>Files</small><b>${d.trades} trades loaded</b><span>${d.oms_fills} OMS fills</span></div>
        <div class="${st(d.recon.total, open)}"><small>Trade recon</small><b>${d.recon.total ? (open ? open + ' breaks open' : 'All resolved') : 'Not run'}</b><span>${d.recon.total ? (d.recon.total - open) + ' of ' + d.recon.total + ' resolved or cleared' : 'Run reconciliation'}</span></div>
        <div class="${st(d.billed)}"><small>Billing</small><b>${d.billed ? 'Bills posted' : 'Not billed'}</b><span>${d.billed ? 'Ledger updated' : 'After reconciliation'}</span></div>
        <div class="${breach ? 'alert' : 'live'}"><small>RMS</small><b>${breach ? breach + ' breaches' : 'Limits live'}</b><span>${d.risk.WithMargin || 0} clients with margin</span></div>
        <div class="${st(d.notes.total && sent === d.notes.total, false)}"><small>Contract notes</small><b>${d.notes.total ? sent + ' of ' + d.notes.total + ' sent' : 'Not generated'}</b><span>${d.notes.Bounced ? d.notes.Bounced + ' bounced' : 'Within 24 hours'}</span></div>
        <div class="${d.eod.closed ? 'done' : 'pend'}"><small>EOD</small><b>${d.eod.closed ? 'Closed ' + esc(d.eod.closed_at) : 'Open'}</b><span>Pay-in ${fmtDate(d.extra.pay_in)}</span></div></div>`;
    } },
  'kpi-trades': { t: 'Exchange trades', about: 'Trades in today’s exchange files and the turnover.', w: 2, h: 2, mw: 2, mh: 2, go: 'files',
    kpi: d => ['Exchange trades', nf(d.trades), cmp(d.turnover) + ' turnover'] },
  'kpi-clients': { t: 'Clients who traded', about: 'How many clients traded today.', w: 2, h: 2, mw: 2, mh: 2, go: 'clients',
    kpi: d => ['Clients who traded', nf(d.clients_traded), d.trades ? nf(d.trades / Math.max(1, d.clients_traded), 1) + ' trades each' : 'no trades'] },
  'kpi-breaks': { t: 'Open breaks', about: 'Reconciliation breaks still open today.', w: 2, h: 2, mw: 2, mh: 2, go: 'recon',
    kpi: d => { const o = d.recon.OPEN || 0; return ['Open breaks', o, d.recon.total ? d.recon.total + ' found today' : 'reconciliation not run', o ? 'bad' : d.recon.total ? 'good' : '']; } },
  'kpi-breach': { t: 'Margin breaches', about: 'Clients in margin breach and the short collection.', w: 2, h: 2, mw: 2, mh: 2, go: 'rms',
    kpi: d => { const b = d.risk.Breach || 0; return ['Margin breaches', b, 'Short collection ' + cmp(d.short), b ? 'bad' : 'good']; } },
  'kpi-cover': { t: 'Client funds cover', about: 'Client bank and clearing balances against what clients are owed.', w: 2, h: 2, mw: 2, mh: 2, go: 'bank',
    kpi: d => ['Client funds cover', num(d.funds.cover).toFixed(2) + '×', 'Bank + CC vs client credits', d.funds.ok ? 'good' : 'bad'] },
  'kpi-penalty': { t: 'Penalty exposure', about: 'Estimated short-collection penalty for the day.', w: 2, h: 2, mw: 2, mh: 2, go: 'margin',
    kpi: d => ['Penalty exposure', inr(d.penalty), 'Short-collection slab estimate', num(d.penalty) > 0 ? 'warn' : 'good'] },
  'kpi-alerts': { t: 'Surveillance alerts', about: 'Open surveillance alerts and how many are high severity.', w: 2, h: 2, mw: 2, mh: 2, go: 'surv',
    kpi: d => ['Open alerts', d.alerts.open || 0, (d.alerts.high || 0) + ' high severity', d.alerts.high ? 'bad' : ''] },
  attention: { t: 'Needs attention', about: 'Everything that needs someone today, most urgent first. Click one to open it.', w: 8, h: 6, mw: 3, mh: 3,
    sub: d => dashAttention(d).length + ' items',
    body: d => { const att = dashAttention(d); return `<div class="att">${att.map(a => `<button data-act="go" data-id="${a[1]}"><i class="${a[0]}"></i><div><b>${esc(a[2])}</b><span>${esc(a[3])}</span></div><span class="dim">›</span></button>`).join('') || '<div class="empty">Nothing needs attention</div>'}</div>`; }, flush: true },
  util: { t: 'Margin utilisation', about: 'Clients with positions, grouped by how much of their margin they use.', w: 4, h: 6, mw: 3, mh: 3, go: 'rms',
    sub: d => (d.risk.WithMargin || 0) + ' clients with positions',
    body: d => {
      const n = d.risk.WithMargin || 0, ub = d.util_buckets.map((u, k) => [u.label, u.count, ['var(--good)', 'var(--accent)', 'var(--warn)', 'var(--bad)'][k]]);
      return `<div class="stack">${ub.map(u => `<i style="width:${n ? u[1] / n * 100 : 0}%;background:${u[2]}"></i>`).join('')}</div>
        <div class="legend">${ub.map(u => `<span style="--c:${u[2]}">${u[0]} · <b class="mono">${u[1]}</b></span>`).join('')}</div>
        <div class="sum" style="margin-top:16px"><span>Required <b>${cmp(d.required)}</b></span><span>Collected <b>${cmp(d.collected)}</b></span></div>`;
    } },
  segments: { t: 'Turnover by segment', about: 'Notional turnover by exchange segment (options at premium).', w: 6, h: 5, mw: 3, mh: 3,
    sub: () => 'Notional; options at premium',
    body: d => { const c = ['var(--accent)', 'var(--good)', 'var(--warn)', 'var(--ink3)', 'var(--bad)'], m = Math.max(1, ...d.by_segment.map(x => num(x.value)));
      return d.by_segment.length ? `<div class="hbars">${d.by_segment.map((x, k) => `<div class="hb"><span>${esc(x.label)}</span><div class="track"><i style="width:${(num(x.value) / m * 100).toFixed(1)}%;background:${c[k % 5]}"></i></div><span class="v">${cmp(x.value)}</span></div>`).join('')}</div>` : dEmpty('No trades'); } },
  hours: { t: 'Trades by hour', about: 'Exchange trades in each hour of the session (IST).', w: 6, h: 5, mw: 3, mh: 3,
    sub: () => 'Exchange trade files, IST',
    body: d => { const m = Math.max(1, ...d.by_hour.map(x => x.count));
      return `<div class="cols">${d.by_hour.map(x => `<div class="c"><em>${x.count}</em><i style="height:${(x.count / m * 82).toFixed(1)}%"></i></div>`).join('')}</div><div class="cols-x">${d.by_hour.map(x => `<span>${esc(x.label)}</span>`).join('')}</div>`; } },
  breaks: { t: 'Open breaks by type', about: 'Today’s open reconciliation breaks by kind, so you know where to start.', w: 4, h: 4, mw: 3, mh: 2, go: 'recon',
    sub: d => (d.recon.OPEN || 0) + ' open',
    body: d => dRows((d.breaks_by_type || []).map(x => [label(x.label), x.count, 'var(--bad)']), 'recon') || dEmpty(d.recon.total ? 'All breaks resolved' : 'Reconciliation not run') },
  'top-util': { t: 'Closest to their margin', about: 'Clients using the most of their margin, with what is free.', w: 6, h: 5, mw: 4, mh: 3, go: 'rms',
    sub: () => 'Highest utilisation first',
    body: d => (d.top_util || []).length ? `<table class="tbl dw-tbl"><thead><tr><th>Client</th><th>Used</th><th class="n">Free</th></tr></thead><tbody>${d.top_util.map(c => `<tr><td><button class="linkish" data-act="client" data-id="${esc(c.ucc)}">${esc(c.name || c.ucc)}</button><div class="dim small mono">${esc(c.ucc)}</div></td><td style="min-width:90px">${bar(c.utilisation || 0)}<span class="small mono">${pct(c.utilisation || 0, 0)}</span></td><td class="n mono ${sgn(c.value)}">${cmp(c.value)}</td></tr>`).join('')}</tbody></table>` : dEmpty('No clients with positions'), flush: true },
  'top-turnover': { t: 'Top clients by turnover', about: 'Today’s most active clients by notional turnover.', w: 6, h: 5, mw: 4, mh: 3,
    sub: d => nf(d.clients_traded) + ' clients traded',
    body: d => (d.top_turnover || []).length ? `<table class="tbl dw-tbl"><thead><tr><th>Client</th><th class="n">Trades</th><th class="n">Turnover</th></tr></thead><tbody>${d.top_turnover.map(c => `<tr><td><button class="linkish" data-act="client" data-id="${esc(c.ucc)}">${esc(c.name || c.ucc)}</button><div class="dim small mono">${esc(c.ucc)}</div></td><td class="n mono">${nf(c.count)}</td><td class="n mono">${cmp(c.value)}</td></tr>`).join('')}</tbody></table>` : dEmpty('No trades'), flush: true },
  'top-contracts': { t: 'Most traded contracts', about: 'Instruments with the most turnover today.', w: 4, h: 5, mw: 3, mh: 3,
    sub: () => 'By turnover',
    body: d => { const r = d.top_contracts || [], m = Math.max(1, ...r.map(x => num(x.value)));
      return r.length ? `<div class="dw-rows">${r.map(x => `<div><span title="${esc(instName(x.label))}">${esc(instName(x.label))}</span><i><b style="width:${(num(x.value) / m * 100).toFixed(1)}%"></b></i><em>${cmp(x.value)}</em></div>`).join('')}</div>` : dEmpty('No trades'); } },
  sides: { t: 'Buy vs sell', about: 'Today’s turnover split between buying and selling.', w: 4, h: 3, mw: 3, mh: 2,
    body: d => { const s = d.sides || [], b = num(s[0]?.value), x = num(s[1]?.value), t = b + x;
      return t ? `<div class="stack" style="height:16px"><i style="width:${b / t * 100}%;background:var(--good)"></i><i style="width:${x / t * 100}%;background:var(--bad)"></i></div>
        <div class="sum" style="margin-top:12px"><span style="color:var(--good)">Buy <b>${cmp(b)}</b> · ${nf(s[0].count)}</span><span style="color:var(--bad)">Sell <b>${cmp(x)}</b> · ${nf(s[1].count)}</span></div>` : dEmpty('No trades'); } },
  settlement: { t: 'Settlement', about: 'Obligations for the next pay-in by status; exceptions in red.', w: 4, h: 4, mw: 3, mh: 2, go: 'settlement',
    sub: d => 'Pay-in ' + fmtDate(d.extra.pay_in),
    body: d => dRows(Object.entries(d.settlement).sort((a, b) => b[1] - a[1]).map(([k, v]) => [k, v, SETT_EXC.includes(k) ? 'var(--bad)' : 'var(--good)']), 'settlement') || dEmpty('No obligations') },
  notes: { t: 'Contract note delivery', about: 'Contract notes sent, waiting and bounced. They must reach clients within 24 hours.', w: 4, h: 3, mw: 3, mh: 2, go: 'notes',
    sub: d => (d.notes.total || 0) + ' notes',
    body: d => { const t = d.notes.total || 0, s = d.notes.Sent || 0, b = d.notes.Bounced || 0, w = Math.max(0, t - s - b);
      return t ? `<div class="stack" style="height:16px"><i style="width:${s / t * 100}%;background:var(--good)"></i><i style="width:${w / t * 100}%;background:var(--warn)"></i><i style="width:${b / t * 100}%;background:var(--bad)"></i></div>
        <div class="legend"><span style="--c:var(--good)">Sent · <b class="mono">${s}</b></span><span style="--c:var(--warn)">Waiting · <b class="mono">${w}</b></span><span style="--c:var(--bad)">Bounced · <b class="mono">${b}</b></span></div>` : dEmpty('Not generated yet'); } },
  approvals: { t: 'Waiting for a checker', about: 'Maker-checker items and requests waiting for someone to approve.', w: 4, h: 4, mw: 3, mh: 2,
    body: d => dRows([['Limit overrides', d.pending_overrides, '', 'rms'], ['Pledge requests', d.pending_pledges, '', 'collateral'], ['Reactivations', +d.extra.reactivation_pending || 0, '', 'inactive'], ['Clients being onboarded', d.onboarding, '', 'clients'], ['Daily reports not uploaded', d.reports_due, '', 'reports']].filter(r => r[1]), '') || dEmpty('Nothing waiting') },
  funds: { t: 'Client money segregation', about: 'Client bank and clearing corporation balances against what clients are owed.', w: 4, h: 4, mw: 3, mh: 3, go: 'bank',
    sub: d => d.funds.ok ? 'Covered' : 'Short',
    body: d => { const f = d.funds;
      return `<div class="dw-kv"><span>Client bank accounts</span><b>${cmp(f.client_bank)}</b><span>With clearing corporation</span><b>${cmp(f.with_cc)}</b><span>Owed to clients</span><b>${cmp(f.client_credits)}</b><span>Cover</span><b class="${f.ok ? 'pos' : 'neg'}">${num(f.cover).toFixed(2)}×</b></div>`; } },
  bank: { t: 'Bank receipts', about: 'Client bank credits by status; third-party and unidentified ones need attention.', w: 4, h: 4, mw: 3, mh: 2, go: 'bank',
    body: d => dRows(Object.entries(d.bank).sort((a, b) => b[1] - a[1]).map(([k, v]) => [k, v, k === 'Third-party' || k === 'Unidentified' ? 'var(--warn)' : 'var(--good)']), 'bank') || dEmpty('No bank credits') },
  work: { t: 'Work queue', about: 'Items Autopilot left for people, and how many are overdue.', w: 4, h: 2, mw: 2, mh: 2, go: 'autopilot', needs: () => edition() !== 'manual',
    kpi: d => ['Work queue', nf(+d.extra.work_open || 0), (+d.extra.work_overdue || 0) + ' overdue', +d.extra.work_overdue ? 'bad' : +d.extra.work_open ? 'warn' : 'good'] },
};
const DASH_DEFAULT = [['proc', 12, 2], ['kpi-trades', 2, 2], ['kpi-breaks', 2, 2], ['kpi-breach', 2, 2], ['kpi-cover', 2, 2], ['kpi-penalty', 2, 2], ['kpi-alerts', 2, 2],
  ['attention', 8, 6], ['util', 4, 6], ['segments', 6, 5], ['hours', 6, 5]].map(([id, w, h]) => ({ id, w, h }));

/* ============================== layout ============================== */
let DL = null; // the layout on screen: [{id, w, h}]
const dwOK = id => DW[id] && (!DW[id].needs || DW[id].needs());
const clamp = (v, lo, hi) => Math.max(lo, Math.min(hi, v));
function dashNormalize(ws) {
  const seen = new Set(), out = [];
  for (const x of ws || []) {
    if (!x || !dwOK(x.id) || seen.has(x.id)) continue;
    seen.add(x.id); const c = DW[x.id];
    out.push({ id: x.id, w: clamp(+x.w || c.w, c.mw, DASH_COLS), h: clamp(+x.h || c.h, c.mh, 12) });
  }
  return out;
}
function dashLayout() {
  const L = S.dashL || {};
  return dashNormalize(L.mine?.widgets || L.firm?.widgets || DASH_DEFAULT);
}
let dashSaveT = 0, dashDirty = false;
function dashSave(now) {
  dashDirty = true; clearTimeout(dashSaveT);
  const go = async () => {
    dashDirty = false;
    try { const r = await api('PUT', '/v1/me/dashboard', { widgets: DL }); S.dashL = { ...(S.dashL || {}), mine: r }; }
    catch (e) { toast('Layout not saved: ' + e.message, true); }
  };
  if (now) return go(); dashSaveT = setTimeout(go, 500);
}

/* ============================== rendering ============================== */
function dwHTML(x, d) {
  const c = DW[x.id], ed = UI.dashEdit;
  let inner;
  try {
    if (c.kpi) { const [l, v, sub, k] = c.kpi(d); inner = `<div class="kpi dw-kpi ${k || ''}" ${c.go && !ed ? `data-act="go" data-id="${c.go}" role="link" tabindex="0"` : ''}><small>${l}</small><b>${v}</b>${sub ? `<span>${sub}</span>` : ''}</div>`; }
    else if (c.bare) inner = c.body(d);
    else inner = `<div class="panel dw-panel"><div class="hd"><h3>${c.go && !ed ? `<button class="linkish dw-title" data-act="go" data-id="${c.go}">${esc(c.t)}</button>` : esc(c.t)}</h3>${c.sub ? `<small>${esc(c.sub(d))}</small>` : ''}</div><div class="${c.flush ? 'dw-bd flush' : 'dw-bd'}">${c.body(d)}</div></div>`;
  } catch (e) { inner = `<div class="panel dw-panel"><div class="hd"><h3>${esc(c.t)}</h3></div><div class="dw-bd">${dEmpty('Could not show this widget')}</div></div>`; }
  const tools = ed ? `<div class="dw-tools"><button class="dw-drag" data-id="${x.id}" aria-label="Move ${esc(c.t)}: drag, or use the arrow keys; Shift and arrows resize" title="Drag to move · arrow keys move · Shift+arrows resize">⠿ <span>${esc(c.t)}</span></button><span class="dw-size mono">${x.w}×${x.h}</span><button class="dw-x" data-act="dashArrange" data-id="${x.id}" aria-label="Arrange ${esc(c.t)}" title="Size and position">⋯</button><button class="dw-x" data-act="dashRemove" data-id="${x.id}" aria-label="Remove ${esc(c.t)}" title="Remove">×</button></div><div class="dw-rs" data-id="${x.id}" title="Drag to resize" aria-hidden="true"></div>` : '';
  return `<section class="dw" data-w="${x.id}" ${x.w <= 3 ? 'data-small' : ''} style="grid-column:span ${x.w};grid-row:span ${x.h}" aria-label="${esc(c.t)}">${inner}${tools}</section>`;
}
function dashGridHTML(d) {
  return `<div class="dgrid ${UI.dashEdit ? 'editing' : ''}" id="dgrid" style="grid-auto-rows:${DASH_ROW}px;gap:${DASH_GAP}px">${DL.map(x => dwHTML(x, d)).join('')}${UI.dashEdit ? `<button class="dw-add" data-act="dashAdd" style="grid-column:span 3;grid-row:span 2">+ Add widget</button>` : ''}</div>` +
    (!DL.length && !UI.dashEdit ? `<div class="note">Your dashboard is empty. <button class="linkish" data-act="dashEdit">Customize</button> to add widgets.</div>` : '');
}
function dashRedraw() { const g = $('dgrid'); if (!g || !C.dashboard) return; g.outerHTML = dashGridHTML(C.dashboard); }
function dashHead() {
  const ed = UI.dashEdit;
  const acts = ed
    ? `<button class="btn" data-act="dashAdd">Add widget</button><button class="btn" data-act="dashReset">Reset</button>${has() ? '<button class="btn" data-act="dashFirm" title="New users and anyone who resets start from this">Save as firm default</button>' : ''}<button class="btn pri" data-act="dashDone">Done</button>`
    : `<button class="btn" data-act="dashEdit">Customize</button><button class="btn" data-act="go" data-id="recon">Open break queue</button><button class="btn pri" data-act="eod">Run EOD close</button>`;
  return ph('Operations dashboard', `${esc(S.me.firm)} · ${fmtDate(S.date)}`, acts);
}

V.dashboard = {
  load: async () => {
    const [d, sm, L] = await Promise.all([get(`/v1/dashboard?date=${S.date}`), S.me?.dev ? get('/v1/dev/sample').catch(() => null) : null, get('/v1/me/dashboard').catch(() => null)]);
    S.sample = sm || {}; if (L) S.dashL = L;
    return d;
  },
  html: d => {
    if (!d.trades && !d.oms_fills) return ph('Operations dashboard', `${esc(S.me.firm)} · ${fmtDate(S.date)}`) + emptyDay();
    if (!UI.dashEdit || !DL) DL = dashLayout();
    return dashHead() +
      (UI.dashEdit ? `<div class="note dw-help"><b>Customizing.</b> Drag a widget by its title bar to move it, drag its bottom-right corner to resize it, or use <b>⋯</b>. Changes save as you go.</div>` : '') +
      (d.eod.previous_open ? `<div class="note warn"><b>Previous day still open.</b> ${esc(d.eod.previous_open)}. <button class="linkish" data-act="gotoDate" data-id="${prevBD(S.date)}">Go to ${fmtDate(prevBD(S.date))}</button></div>` : '') +
      (d.eod.closed && S.me?.dev && S.sample?.next && S.sample.next > S.date ? `<div class="note"><b>Day closed${d.eod.rolled_positions ? `; ${d.eod.rolled_positions} open positions carried to ${fmtDate(d.eod.next_day)}` : ''}.</b> <button class="linkish" data-act="gotoDate" data-id="${S.sample.next}">Continue the sample on ${fmtDate(S.sample.next)}</button></div>` : '') +
      (d.warnings.length ? `<div class="note warn">${d.warnings.map(esc).join('<br>')}</div>` : '') +
      dashGridHTML(d);
  }
};

/* ============================== actions ============================== */
function dashRerender() { $('page').innerHTML = V.dashboard.html(C.dashboard); bindPage(); }
Object.assign(A, {
  dashEdit: () => { UI.dashEdit = true; DL = dashLayout(); dashRerender(); },
  dashDone: async () => { UI.dashEdit = false; if (dashDirty) await dashSave(true); toast('Dashboard saved'); dashRerender(); },
  dashRemove: id => { DL = DL.filter(x => x.id !== id); dashSave(); dashRedraw(); },
  dashAdd: () => {
    const on = new Set(DL.map(x => x.id));
    const list = Object.entries(DW).filter(([id]) => dwOK(id));
    modal(mhd('Add widgets', `${list.length} widgets · ${on.size} on your dashboard`) + `<div class="bd dw-cat">${list.map(([id, c]) => `<div class="dw-cat-i"><div><b>${esc(c.t)}</b><span>${esc(c.about)}</span><small class="mono dim">${c.w}×${c.h}</small></div>${on.has(id) ? `<button class="btn sm" data-act="dashCatRemove" data-id="${id}">Remove</button>` : `<button class="btn sm pri" data-act="dashCatAdd" data-id="${id}">Add</button>`}</div>`).join('')}</div><div class="ft"><button class="btn pri" data-act="close">Done</button></div>`, true);
  },
  dashCatAdd: id => { if (!dwOK(id) || DL.some(x => x.id === id)) return; const c = DW[id]; DL.push({ id, w: c.w, h: c.h }); dashSave(); dashRedraw(); A.dashAdd(); },
  dashCatRemove: id => { A.dashRemove(id); A.dashAdd(); },
  dashReset: () => {
    const firm = S.dashL?.firm?.widgets;
    confirmModal('Reset your dashboard?', firm ? 'You’ll get the firm’s default layout.' : 'You’ll get the standard layout.', '', 'Reset', async () => {
      clearTimeout(dashSaveT); dashDirty = false;
      await api('PUT', '/v1/me/dashboard', { widgets: null }); S.dashL = { ...(S.dashL || {}), mine: {} }; DL = dashLayout(); closeModal(); toast('Dashboard reset'); dashRerender();
    });
  },
  dashFirm: () => confirmModal('Save as the firm’s default?', 'Users who haven’t arranged their own dashboard, and anyone who resets, will see this layout. Their own layouts are not changed.', '', 'Save as firm default', async () => {
    const r = await api('PUT', '/v1/dashboard/default', { widgets: DL }); S.dashL = { ...(S.dashL || {}), firm: r }; closeModal(); toast('Saved as the firm’s default dashboard');
  }),
  dashArrange: id => {
    const i = DL.findIndex(x => x.id === id); if (i < 0) { closeModal(); return; }
    const x = DL[i], c = DW[id];
    modal(mhd('Arrange: ' + esc(c.t), `Position ${i + 1} of ${DL.length} · ${x.w} columns wide, ${x.h} rows tall`) + `<div class="bd dw-arr">
      <div><span>Position</span><button class="btn sm" data-act="dashMove" data-id="${id}" data-v="-1" ${i ? '' : 'disabled'}>Earlier</button><button class="btn sm" data-act="dashMove" data-id="${id}" data-v="1" ${i < DL.length - 1 ? '' : 'disabled'}>Later</button></div>
      <div><span>Width</span><button class="btn sm" data-act="dashSize" data-id="${id}" data-v="w-1" ${x.w > c.mw ? '' : 'disabled'}>Narrower</button><button class="btn sm" data-act="dashSize" data-id="${id}" data-v="w1" ${x.w < DASH_COLS ? '' : 'disabled'}>Wider</button></div>
      <div><span>Height</span><button class="btn sm" data-act="dashSize" data-id="${id}" data-v="h-1" ${x.h > c.mh ? '' : 'disabled'}>Shorter</button><button class="btn sm" data-act="dashSize" data-id="${id}" data-v="h1" ${x.h < 12 ? '' : 'disabled'}>Taller</button></div>
    </div><div class="ft"><button class="btn" data-act="dashRemove2" data-id="${id}">Remove</button><button class="btn pri" data-act="close">Done</button></div>`);
  },
  dashMove: (id, el) => { dashMoveBy(id, +el.dataset.v); A.dashArrange(id); },
  dashSize: (id, el) => { const m = /^([wh])(-?1)$/.exec(el.dataset.v); if (m) dashResizeBy(id, m[1], +m[2]); A.dashArrange(id); },
  dashRemove2: id => { closeModal(); A.dashRemove(id); },
});
function dashMoveBy(id, by) {
  const i = DL.findIndex(x => x.id === id), j = i + by; if (i < 0 || j < 0 || j >= DL.length) return;
  const [x] = DL.splice(i, 1); DL.splice(j, 0, x); dashSave(); dashRedraw();
}
function dashResizeBy(id, dim, by) {
  const x = DL.find(y => y.id === id); if (!x) return; const c = DW[id];
  if (dim === 'w') x.w = clamp(x.w + by, c.mw, DASH_COLS); else x.h = clamp(x.h + by, c.mh, 12);
  dashSave(); dashRedraw();
}

/* ============================== drag and resize ============================== */
// Pointer events, so it works with a mouse, a pen or a finger.
document.addEventListener('pointerdown', e => {
  if (!UI.dashEdit || e.button > 0) return;
  const rs = e.target.closest('.dw-rs'), hd = e.target.closest('.dw-drag');
  if (!rs && !hd) return;
  const el = e.target.closest('.dw'); if (!el) return;
  e.preventDefault();
  (rs ? dashResizeStart : dashDragStart)(el, e);
});
function dashDragStart(el, e) {
  const g = $('dgrid'), r = el.getBoundingClientRect(), ox = e.clientX - r.left, oy = e.clientY - r.top;
  const ghost = el.cloneNode(true);
  ghost.classList.add('dw-ghost'); Object.assign(ghost.style, { width: r.width + 'px', height: r.height + 'px', left: r.left + 'px', top: r.top + 'px' });
  document.body.appendChild(ghost); el.classList.add('dw-ph'); document.body.classList.add('dw-dragging');
  let last = '', scrollV = 0, raf = 0;
  const scroller = () => { if (scrollV) { window.scrollBy(0, scrollV); raf = requestAnimationFrame(scroller); } else raf = 0; };
  const move = ev => {
    ghost.style.left = ev.clientX - ox + 'px'; ghost.style.top = ev.clientY - oy + 'px';
    scrollV = ev.clientY < 70 ? -12 : ev.clientY > innerHeight - 70 ? 12 : 0; if (scrollV && !raf) raf = requestAnimationFrame(scroller);
    const t = document.elementFromPoint(ev.clientX, ev.clientY)?.closest('.dw');
    if (!t || t === el || t.parentElement !== g) return;
    const tr = t.getBoundingClientRect(), wide = tr.width > g.clientWidth * .7;
    const after = wide ? ev.clientY > tr.top + tr.height / 2 : ev.clientX > tr.left + tr.width / 2;
    const key = t.dataset.w + (after ? '>' : '<'); if (key === last) return; last = key;
    g.insertBefore(el, after ? t.nextSibling : t);
  };
  const up = () => {
    document.removeEventListener('pointermove', move); document.removeEventListener('pointerup', up); document.removeEventListener('pointercancel', up);
    scrollV = 0; ghost.remove(); el.classList.remove('dw-ph'); document.body.classList.remove('dw-dragging');
    const order = [...g.querySelectorAll(':scope > .dw')].map(x => x.dataset.w);
    const next = order.map(id => DL.find(x => x.id === id)).filter(Boolean);
    if (next.map(x => x.id).join() !== DL.map(x => x.id).join()) { DL = next; dashSave(); }
  };
  document.addEventListener('pointermove', move); document.addEventListener('pointerup', up); document.addEventListener('pointercancel', up);
}
function dashResizeStart(el, e) {
  const g = $('dgrid'), x = DL.find(y => y.id === el.dataset.w), c = DW[x.id];
  const colW = (g.clientWidth - (DASH_COLS - 1) * DASH_GAP) / DASH_COLS;
  const sx = e.clientX, sy = e.clientY, w0 = x.w, h0 = x.h, badge = el.querySelector('.dw-size');
  el.classList.add('dw-resizing'); document.body.classList.add('dw-sizing');
  const move = ev => {
    const w = clamp(w0 + Math.round((ev.clientX - sx) / (colW + DASH_GAP)), c.mw, DASH_COLS);
    const h = clamp(h0 + Math.round((ev.clientY - sy) / (DASH_ROW + DASH_GAP)), c.mh, 12);
    if (w === x.w && h === x.h) return;
    x.w = w; x.h = h; el.style.gridColumn = 'span ' + w; el.style.gridRow = 'span ' + h; if (badge) badge.textContent = w + '×' + h;
  };
  const up = () => {
    document.removeEventListener('pointermove', move); document.removeEventListener('pointerup', up); document.removeEventListener('pointercancel', up);
    el.classList.remove('dw-resizing'); document.body.classList.remove('dw-sizing');
    if (x.w !== w0 || x.h !== h0) dashSave();
  };
  document.addEventListener('pointermove', move); document.addEventListener('pointerup', up); document.addEventListener('pointercancel', up);
}
// Keyboard: arrows move the focused widget, Shift+arrows resize it.
document.addEventListener('keydown', e => {
  const h = e.target.closest?.('.dw-drag'); if (!h || !UI.dashEdit) return;
  const id = h.dataset.id, k = e.key;
  if (!['ArrowLeft', 'ArrowRight', 'ArrowUp', 'ArrowDown'].includes(k)) return;
  e.preventDefault();
  if (e.shiftKey) dashResizeBy(id, k === 'ArrowLeft' || k === 'ArrowRight' ? 'w' : 'h', k === 'ArrowRight' || k === 'ArrowDown' ? 1 : -1);
  else dashMoveBy(id, k === 'ArrowLeft' || k === 'ArrowUp' ? -1 : 1);
  document.querySelector(`.dw-drag[data-id="${id}"]`)?.focus();
});
// Enter on a KPI tile opens its screen, like a click.
document.addEventListener('keydown', e => { const t = e.target; if (e.key === 'Enter' && t.classList?.contains('dw-kpi') && t.dataset.act) t.click(); });
