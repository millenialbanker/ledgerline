'use strict';
/* Ledgerline console: Exchange files → BSE Clearing (ICCL).

   For a firm that clears its own trades at ICCL (Settings → Cleared
   through): the day's margin reporting (EOD and peak collected, with the
   pre-delivery margin on in-the-money stock options), the client-level
   collateral segregation report (ICCL-only collateral, nil-balance clients
   included), the ICCLCOLL allocation with its caps, ICCL's short
   allocations with the reasons to report, ICCL's allocation mismatches,
   the trading limit, the pay-in cut-offs and accounts, and ICCL's layouts
   that are still to be confirmed. A firm that does not clear at ICCL sees
   nothing here.

   Server: internal/ops/iccl.go, icclview.go, icclrisk.go, icclsettle.go;
   internal/api/iccl.go. Loaded after app.js; it wraps V.xfiles. Names are
   prefixed ic. */

const icBase = V.xfiles;
V.xfiles = {
  load: async () => {
    const [fs, ic] = await Promise.all([icBase.load(), get(`/v1/iccl?date=${S.date}`).catch(() => null)]);
    fs.iccl = ic;
    UI.icLast = ic;
    return fs;
  },
  html: fs => icBase.html(fs) + icPanel(fs.iccl)
};

const icDl = (id, label, pri) => `<button class="btn sm ${pri ? 'pri' : ''}" data-act="dl" data-id="/v1/exchange-files/${id}?date=${S.date}" data-name="${id}_${S.date}.csv">${esc(label)}</button>`;

function icPanel(v) {
  if (!v || !v.on) return '';
  const mk = has('Ops maker', 'Ops checker', 'Risk manager', 'Compliance officer');
  const canReason = has('Ops maker', 'Ops checker', 'Risk manager');
  const notes = (v.notes || []).map(n => `<div class="note warn">${esc(n)}</div>`).join('') + (v.accounts || []).map(n => `<div class="note bad">${esc(n)}</div>`).join('');
  const short = (v.margins || []).filter(m => num(m.short));
  const mg = tbl([['Segment'], ['Client'], ['Required', 'r'], ['Pre-delivery', 'r'], ['EOD collected', 'r'], ['Peak collected', 'r'], ['Short', 'r']], short.map(m => `<tr><td>${esc(m.segment)}</td><td class="mono">${esc(m.ucc)}<div class="dim small">${esc(m.name)}</div></td><td class="r">${inr(m.required)}</td><td class="r">${num(m.pre_delivery) ? inr(m.pre_delivery) : '—'}</td><td class="r">${inr(m.eod_collected)}</td><td class="r">${inr(m.peak_collected)}</td><td class="r neg">${inr(m.short)}</td></tr>`), { empty: `Every client's margin at ICCL is covered (${nf((v.margins || []).length)} client rows).` });
  const al = (v.alloc || []).filter(a => a.problem);
  const alloc = al.length ? tbl([['Segment'], ['Client'], ['Allocated', 'r'], ['Cash given', 'r'], ['Reported collected', 'r'], ['Problem']], al.map(a => `<tr><td>${esc(a.segment)}</td><td class="mono">${esc(a.ucc)}</td><td class="r">${inr(a.cash)}</td><td class="r">${inr(a.received)}</td><td class="r">${inr(a.reported)}</td><td class="small wrap">${esc(a.problem)}</td></tr>`)) : '<p class="dim small">Every allocation is within the caps: no more than the cash the client gave, not below the margin reported as collected.</p>';
  const codes = Object.keys(v.reason_codes || {}).sort().map(c => `<option value="${c}">${c} ${esc(v.reason_codes[c])}</option>`).join('');
  const sa = (v.short || []).length ? tbl([['Segment'], ['Snapshot'], ['Client'], ['Margin', 'r'], ['Collateral', 'r'], ['Short', 'r'], ['Reasons'], ['']], v.short.map((r, i) => `<tr><td>${esc(r.segment)}</td><td>${esc(r.snapshot)}</td><td class="mono">${esc(r.ucc)}<div class="dim small">${esc(r.name)}</div></td><td class="r">${inr(r.required)}</td><td class="r">${inr(r.collateral)}</td><td class="r">${inr(r.short)}</td><td class="small">${r.reasons.length ? r.reasons.map(x => `${esc(x.code)} ${inr(x.amount)}`).join(', ') : pill('None', num(r.short) ? 'bad' : 'nt')}</td><td>${canReason ? `<button class="btn sm" data-act="icReason" data-id="${i}">Add reason</button>` : ''}</td></tr>`)) : `<p class="dim small">No SHORTALLOCATION_CM file loaded for ${fmtDate(v.date)}. Load it on Exchange files above.</p>`;
  const df = (v.diffs || []).length ? tbl([['Segment'], ['Client'], ['Allocated', 'r'], ['Reported', 'r'], ['Difference', 'r']], v.diffs.map(d => `<tr><td>${esc(d.segment)}</td><td class="mono">${esc(d.ucc)}</td><td class="r">${inr(d.allocated)}</td><td class="r">${inr(d.reported)}</td><td class="r neg">${inr(d.diff)}</td></tr>`)) : '<p class="dim small">No ALLOCATIONDIFF mismatch loaded for this date.</p>';
  const pd = (v.pre_delivery || []).length ? tbl([['Client'], ['Contract'], ['Days to expiry', 'r'], ['%', 'r'], ['Margin', 'r'], ['']], v.pre_delivery.map(p => `<tr><td class="mono">${esc(p.ucc)}</td><td>${esc(p.contract)}</td><td class="r">${p.days_left}</td><td class="r">${p.pct}</td><td class="r">${num(p.margin) ? inr(p.margin) : '—'}</td><td class="small wrap">${esc(p.note || '')}</td></tr>`)) : '<p class="dim small">No long in-the-money BSE stock option within four trading days of expiry.</p>';
  const l = v.trading_limit || {};
  const lim = `<div class="kpis">${kpi('Trading limit (estimate)', inr(l.limit), `deposits ${inr(l.deposits)} · clients ${inr(l.clients)}`)}${kpi('Used', inr(l.used), `${Number(l.pct || 0).toFixed(0)}%`, l.status === 'Breach' ? 'bad' : l.status === 'Watch' ? 'warn' : 'good')}</div><p class="dim small">${esc(l.basis || '')}</p>`;
  const c = v.cutoffs || {};
  const lay = tbl([['Layout'], ['Direction'], ['Built from']], (v.layouts || []).map(f => `<tr><td><b>${esc(f.title)}</b><div class="dim small mono">${esc(f.id)}</div></td><td>${f.direction === 'in' ? 'Received' : 'Sent'}</td><td class="small">${esc(f.notice || '')}</td></tr>`));
  return `<div class="panel"><div class="hd"><h3>BSE Clearing (ICCL)</h3><small>${esc(v.why)} · segments ${esc((v.segments || []).join(', ') || 'none')}${v.codes && v.codes.cm ? ` · clearing code ${esc(v.codes.cm)}` : ''}</small></div><div class="bd">
    ${notes}
    ${mk ? `<div class="split">${icDl('iccl-margin-reporting', 'Margin reporting file', true)}${icDl('iccl-collateral-segregation', 'Collateral segregation report')}${icDl('iccl-collateral-allocation', 'ICCLCOLL allocation')}${icDl('iccl-short-allocation-reporting', 'Short allocation reasons')}${icDl('iccl-early-payin-securities', 'EPN (securities)')}${icDl('iccl-early-payin-funds', 'EPF (funds)')}</div>` : ''}
    <p class="dim small">Segregation report: ${nf(v.segregation_rows)} rows, ${nf(v.nil_balance)} of them nil-balance clients who traded in the last 12 months. Pay-in at ICCL by ${esc(c.funds)} (funds) and ${esc(c.securities)} (securities) on T+1; auction pay-in by ${esc(c.auction)}, instructions by ${esc(c.auction_instr)}; internal shortages funded by ${esc(c.internal_shortage)}.</p>
    <h4>Margin short at ICCL</h4>${mg}
    <h4>ICCLCOLL allocation caps</h4>${alloc}
    <h4>Short allocations (SHORTALLOCATION_CM)${v.short_file ? ` <span class="dim small">${esc(v.short_file)}</span>` : ''}</h4>${sa}
    <h4>Allocation mismatches (ALLOCATIONDIFF)</h4>${df}
    <h4>Pre-delivery margin, ITM stock options</h4>${pd}
    <h4>Trading limit</h4>${lim}
    <details class="small"><summary>ICCL layouts to confirm (${(v.layouts || []).length})</summary><p class="dim">ICCL has not published these layouts. Each was built from the notice named; confirm it against ICCL's file description before the first upload.</p>${lay}</details>
    </div></div>
    <select id="ic-codes" hidden>${codes}</select>`;
}

Object.assign(A, {
  icReason: id => {
    const v = UI.icLast;
    const r = v && v.short[Number(id)];
    if (!r) return;
    confirmModal('Reason for short allocation', `${r.ucc} · ${r.segment} · snapshot ${r.snapshot}`,
      `<div class="form"><div class="fld"><label for="ic-code">Reason code</label><select class="in" id="ic-code">${$('ic-codes').innerHTML}</select></div>
       <div class="fld"><label for="ic-amt">Collateral available (₹)</label><input class="in" id="ic-amt" inputmode="decimal" value="${esc(String(r.short))}"></div>
       <p class="dim small">Give the collateral that was in fact available for this reason. Several reasons may be given; ICCL adds them up. A zero amount removes the reason.</p></div>`,
      'Save', () => act(() => post('/v1/iccl/short-reasons', { date: v.date, snapshot: r.snapshot, segment: r.segment, ucc: r.ucc, code: $('ic-code').value, amount: $('ic-amt').value.trim() || '0' }), 'Reason saved'));
  },
});

