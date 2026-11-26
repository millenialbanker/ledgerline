'use strict';
/* Ledgerline console: RMS & limits → Position limits.

   The end-of-day checks beyond margin: derivative position limits (client,
   the firm's own account, the member, connected clients), index futures and
   index options limits with the cover report to the clearing corporation,
   trades in the F&O ban period, expiry-day spreads, trade-for-trade breaches
   of the surveillance list, and the settings they use (MWPL shares, index
   limits, the early pay-in margin date, pay-in cut-offs, netting across
   exchanges, the split of one pay-in across exchanges).

   Server: internal/ops/rmslimits.go, rmspayin.go; internal/api/rmslimits.go.
   Loaded after app.js; it wraps V.rms. Names are prefixed rl. */

const rlBase = V.rms;
V.rms = {
  load: async () => {
    const [d, rl] = await Promise.all([rlBase.load(), get(`/v1/rms/limits?date=${S.date}`).catch(e => ({ error: e.message }))]);
    d.rl = rl;
    return d;
  },
  html: d => rlBase.html(d) + rlPanel(d.rl || {})
};

const rlSt = s => pill(s, s === 'Breach' ? 'bad' : s === 'Watch' ? 'warn' : s === 'Justified' ? 'warn' : 'good');
const rlQty = x => esc(Number(x || 0).toLocaleString('en-IN', { maximumFractionDigits: 0 }));

function rlPanel(v) {
  if (v.error) return `<div class="panel"><div class="hd"><h3>Position limits</h3></div><div class="bd">${errHTML({ message: v.error })}</div></div>`;
  const risk = has('Risk manager', 'Compliance officer');
  const near = (v.limits || []).filter(r => r.status !== 'OK');
  const lim = tbl([['Level'], ['Who'], ['Underlying'], ['Long', 'r'], ['Short', 'r'], ['Open', 'r'], ['Limit', 'r'], ['%', 'r'], ['Status'], ['Basis']], near.map(r => `<tr><td>${esc(r.level)}</td><td class="mono">${esc(r.entity)}${r.uccs && r.uccs.length > 1 ? `<div class="dim small">${esc(r.uccs.join(', '))}</div>` : ''}</td><td>${esc(r.symbol)} <span class="dim small">${esc(r.market)} ${esc(r.segment)}</span></td><td class="r">${rlQty(r.long)}</td><td class="r">${rlQty(r.short)}</td><td class="r">${rlQty(r.open)}</td><td class="r">${rlQty(r.limit)}</td><td class="r">${Number(r.pct).toFixed(0)}</td><td>${rlSt(r.status)}</td><td class="small wrap">${esc(r.basis)}${r.note ? `<div>${esc(r.note)}</div>` : ''}</td></tr>`), { empty: `Every position is under ${v.config ? v.config.warn_pct : 90}% of its limit.` });
  const idx = tbl([['Limit'], ['Who'], ['Index'], ['Value (₹)', 'r'], ['Limit (₹)', 'r'], ['Status'], ['Backing']], (v.index || []).filter(r => r.status !== 'OK').map(r => `<tr><td>${esc(r.kind)}</td><td class="mono">${esc(r.entity)}${r.uccs && r.uccs.length > 1 ? `<div class="dim small">${esc(r.uccs.join(', '))}</div>` : ''}</td><td>${esc(r.index)}</td><td class="r">${nf(r.value)}</td><td class="r">${nf(r.limit)}</td><td>${rlSt(r.status)}</td><td class="small wrap">${r.cover_kind ? `${esc(r.cover_kind)}: ${inr(r.cover)} against ${inr(r.excess)} over` : esc(r.basis)}</td></tr>`), { empty: 'No index futures or index options position near its limit.' });
  const ban = (v.ban || []).length ? tbl([['Client'], ['Security'], ['Net FutEq at start', 'r'], ['At end', 'r'], ['Trades', 'r'], ['']], v.ban.map(b => `<tr><td class="mono">${esc(b.ucc)}</td><td>${esc(b.symbol)} <span class="dim small">${esc(b.why)}</span></td><td class="r">${rlQty(b.start)}</td><td class="r">${rlQty(b.end)}</td><td class="r">${nf(b.trades)}</td><td>${b.increased ? pill('Increased', 'bad') : pill('Reduced', 'good')}</td></tr>`)) : `<p class="dim small">${(v.banned || []).length ? 'No client traded the banned securities: ' + esc(v.banned.join(', ')) + '.' : 'No security in the ban period on this date (load FO-BAN on Exchange files).'}</p>`;
  const spreads = (v.spreads || []).length ? `<h4>Expiry-day spreads (no spread benefit today)</h4>${tbl([['Client'], ['Stock'], ['Expiring'], ['Later']], v.spreads.map(s => `<tr><td class="mono">${esc(s.ucc)}</td><td>${esc(s.symbol)}</td><td class="small">${esc(s.expiring.join(', '))}</td><td class="small">${esc(s.other.join(', '))}</td></tr>`))}` : '';
  const surv = (v.surveillance || []).length ? `<h4>Trade-for-trade breaches</h4>${tbl([['Client'], ['Security'], ['Measure'], ['What']], v.surveillance.map(h => `<tr><td class="mono">${esc(h.ucc)}</td><td>${esc(h.exchange)} ${esc(h.symbol)}</td><td>${esc(h.measure)}</td><td class="small">${esc(h.why)}</td></tr>`))}` : '';
  const moi = (v.member_oi || []).filter(r => r.status !== 'OK');
  const ncl = moi.length ? `<h4>NSE Clearing member open interest</h4>${tbl([['Member'], ['Symbol'], ['Type'], ['OI', 'r'], ['Limit', 'r'], ['%', 'r'], ['Status']], moi.map(r => `<tr><td class="mono">${esc(r.tm)}</td><td>${esc(r.symbol)}</td><td>${esc(r.type)}</td><td class="r">${rlQty(r.oi)}</td><td class="r">${rlQty(r.limit)}</td><td class="r">${Number(r.pct).toFixed(0)}</td><td>${rlSt(r.status)}</td></tr>`))}` : '';
  const src = `<p class="dim small">From: ${(v.sources || []).length ? esc(v.sources.join('; ')) : 'no limit file loaded for this date (load POSITION-LIMITS on Exchange files)'}. Surveillance list: ${v.surv_as_of ? `${nf(v.surv_count)} securities in force, loaded ${fmtDate(v.surv_as_of)}` : 'not loaded (SURV-MEASURES)'}.${(v.missing || []).length ? ` No limit known for: ${esc(v.missing.slice(0, 12).join(', '))}${v.missing.length > 12 ? '…' : ''}.` : ''}</p>`;
  const notes = (v.notes || []).map(n => `<div class="note">${esc(n)}</div>`).join('');
  const tools = `<div class="split">${risk ? `<button class="btn pri" data-act="rlRun">Run checks now</button>` : ''}<button class="btn" data-act="dl" data-id="/v1/rms/index-cover-report?date=${S.date}" data-name="index-option-cover-${S.date}.csv">Index options cover report</button><button class="btn" data-act="dl" data-id="/v1/rms/surveillance-file?date=${S.date}" data-name="surveillance-margins-${S.date}.csv">Surveillance margins file</button>${has('Risk manager') ? `<button class="btn" data-act="rlCfg">Settings</button>` : ''}</div>`;
  return `<div class="panel"><div class="hd"><h3>Position limits</h3><small>end of ${fmtDate(v.date)} · ${v.breaches ? `${nf(v.breaches)} breaches` : 'no breach'} · positions on exchanges one clearing corporation clears are taken together</small></div><div class="bd">
    ${notes}${src}${tools}
    <h4>Derivative positions near or over their limit</h4>${lim}
    <h4>Index limits</h4>${idx}
    <h4>F&amp;O ban period</h4>${ban}
    ${spreads}${surv}${ncl}</div></div>`;
}

function rlCfgBody(c) {
  const n = (id, label, val, hint) => `<div class="fld"><label for="${id}">${label}</label><input class="in" id="${id}" type="number" min="0" value="${esc(val)}">${hint ? `<div class="dim small">${hint}</div>` : ''}</div>`;
  const t = (id, label, val, type) => `<div class="fld"><label for="${id}">${label}</label><input class="in" id="${id}" type="${type || 'text'}" value="${esc(val || '')}"></div>`;
  const ck = (id, label, on) => `<label class="small"><input type="checkbox" id="${id}" ${on ? 'checked' : ''}> ${label}</label>`;
  return `<div class="form">
    ${n('rl-cli', 'Client limit, % of MWPL', c.client_mwpl_pct, 'Used when the limit file gives no client limit.')}
    ${n('rl-prop', 'Own account limit, % of MWPL', c.prop_mwpl_pct)}
    ${n('rl-mem', 'Member limit, % of MWPL', c.member_mwpl_pct)}
    ${n('rl-warn', 'Watch from, % of a limit', c.warn_pct)}
    ${n('rl-ban', 'Ban period from, % of MWPL', c.ban_pct, 'Used with NSE Clearing’s MWPL when no ban list is loaded.')}
    ${n('rl-ifo', 'Index futures: % of open interest', c.index_fut_oi_pct)}
    ${n('rl-iff', 'Index futures: floor (₹ crore)', c.index_fut_floor_cr)}
    ${n('rl-ion', 'Index options net per PAN (₹ crore)', c.index_opt_net_cr)}
    ${n('rl-iog', 'Index options gross each side (₹ crore)', c.index_opt_gross_cr)}
    ${t('rl-prop-ucc', 'The firm’s own trading codes (comma separated)', (c.proprietary_uccs || []).join(', '))}
    ${t('rl-epi', 'Early pay-in sales need margin from', c.epi_margin_from, 'date')}
    ${t('rl-spr', 'No expiry-day spread benefit from', c.expiry_spread_from, 'date')}
    ${t('rl-fpi', 'Funds pay-in by', c.funds_payin_by, 'time')}
    ${t('rl-spi', 'Securities pay-in by', c.securities_payin_by, 'time')}
    ${t('rl-api', 'Auction pay-in by (next day)', c.auction_payin_by, 'time')}
    <div class="fld">${ck('rl-gross', 'Margin each exchange apart (do not net NSE and BSE)', c.gross_across_exchanges)}<br>${ck('rl-split', 'Post each pay-in to one segment (no split across exchanges)', c.cc_split_off)}<br>${ck('rl-surv', 'Do not apply surveillance margins', c.surv_margins_off)}<br>${ck('rl-rem', 'No pay-in reminders', c.payin_reminders_off)}<br>${ck('rl-off', 'No position limit checks (no derivatives traded)', c.position_limits_off)}</div>
  </div>`;
}

Object.assign(A, {
  rlRun: () => act(() => post('/v1/rms/limits/run', { date: S.date }), r => `${r.raised} work items raised or refreshed`),
  rlCfg: async () => {
    const c = await get('/v1/rms/limits/config');
    confirmModal('Position limit settings', 'Shares of the MWPL apply only where the exchange’s limit file gives no number of its own.', rlCfgBody(c), 'Save', () => {
      const num = id => parseInt($(id).value, 10) || 0;
      const body = { ...c, client_mwpl_pct: num('rl-cli'), prop_mwpl_pct: num('rl-prop'), member_mwpl_pct: num('rl-mem'), warn_pct: num('rl-warn'), ban_pct: num('rl-ban'),
        index_fut_oi_pct: num('rl-ifo'), index_fut_floor_cr: num('rl-iff'), index_opt_net_cr: num('rl-ion'), index_opt_gross_cr: num('rl-iog'),
        proprietary_uccs: $('rl-prop-ucc').value.split(',').map(x => x.trim().toUpperCase()).filter(Boolean), epi_margin_from: $('rl-epi').value, expiry_spread_from: $('rl-spr').value,
        funds_payin_by: $('rl-fpi').value, securities_payin_by: $('rl-spi').value, auction_payin_by: $('rl-api').value,
        gross_across_exchanges: $('rl-gross').checked, cc_split_off: $('rl-split').checked, surv_margins_off: $('rl-surv').checked, payin_reminders_off: $('rl-rem').checked, position_limits_off: $('rl-off').checked };
      return act(() => api('PUT', '/v1/rms/limits/config', body), 'Position limit settings saved');
    });
  },
});

// Live RMS: clients caught by the early pay-in margin rule.
const rlLive = V.live;
V.live = {
  load: rlLive.load,
  html: d => {
    const hit = ((d && d.rows) || []).filter(r => r.epi_rule);
    return rlLive.html(d) + (hit.length ? `<div class="panel"><div class="hd"><h3>Early pay-in sales squared off, or new positions, without margin</h3><small>such a trade needs the required margin actually with the firm</small></div><div class="bd">${hit.map(r => `<div class="note warn"><b class="mono">${esc(r.ucc)}</b> ${esc(r.epi_rule)}</div>`).join('')}</div></div>` : '');
  }
};
