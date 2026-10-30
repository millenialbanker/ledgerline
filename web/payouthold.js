'use strict';
/* Ledgerline console: pay-out holds for big sellers (screen "payouthold",
   RMS & margin). Sale money awaiting the clearing corporation's funds
   pay-out is backed by the broker's own funds; clients whose share is above
   the firm's threshold have part of it held back from their allocation and
   OMS limit until the pay-out lands. Loaded after app.js. */

Object.assign(UI, { phTab: 'holds' });
const PH_ST = { Proposed: 'warn', Approved: 'acc', Sending: 'acc', 'To do in N-MASS': 'warn', Applied: 'good', 'Done by hand': 'good', Failed: 'bad', Rejected: 'nt', 'Not needed': 'nt',
  Releasing: 'acc', 'To release in N-MASS': 'warn', 'Release failed': 'bad', 'Partly released': 'acc', Released: 'nt' };
const phPill = s => pill(s, PH_ST[s] || 'nt');
const phRisk = () => has('Risk manager');
const phCan = () => has('Risk manager', 'Ops checker');

V.payouthold = {
  load: () => get(`/v1/payout-holds?date=${S.date}`),
  html: d => {
    const st = d.settings, f = d.funds, day = d.day || {};
    const off = st.enabled ? '' : `<div class="note warn"><b>Pay-out holds are switched off.</b> The risk manager switches them on under Rules.</div>`;
    const src = (f.segments || []).map(s => `${esc(s.segment)} ${inr(s.amount)} <span class="dim">· ${esc(s.source)}</span>`).join('<br>') || '<span class="neg">No figure</span>';
    const miss = (f.missing || []).length ? `<div class="note warn">No starting broker funds for <b>${esc(f.missing.join(', '))}</b>: nothing is held until the figure is there. ${phRisk() ? '<button class="btn sm" data-act="phFunds">Enter the figure</button>' : 'The risk manager enters it.'}</div>` : '';
    const bod = day.applied_at ? `Holds worked out ${esc(day.applied_at.slice(11, 16))} (${esc(day.trigger || '')})` : d.bod_done ? `BOD files done: ${esc(d.bod_why)}` : `Waiting for the BOD files (start-of-day allocation sent and BOD limit file made), or ${esc(st.apply_at)}`;
    const paid = day.paid_all ? pill('Pay-out received', 'good') : (day.paid_keys || []).length ? pill('Pay-out received for ' + day.paid_keys.length + ' settlement(s)', 'acc') : pill('Pay-out not yet received', 'warn');
    return ph('Pay-out holds', `Sale money a client is owed is allocated to the client at the start of the day, but the clearing corporation pays it out only around 13:00: until then it is backed by the firm's own funds. A client whose net sale money awaiting pay-out is above ${esc(st.threshold_pct)}% of the starting broker funds has ${esc(st.hold_pct)}% of it held back from the allocation at NSE Clearing and from the OMS limit, and given back when the pay-out arrives.`,
      (phCan() ? `<button class="btn" data-act="phApply">Apply holds now</button><button class="btn pri" data-act="phPayout">Pay-out received</button>` : '') + (phRisk() ? '<button class="btn" data-act="phFunds">Broker funds</button>' : '')) + off + miss +
      `<div class="note">${bod} · ${paid} · ${d.api_live ? pill('Allocation API on: changes go to NSE Clearing', 'good') : pill('Allocation API off: N-MASS files', 'warn')} · ${st.mode === 'propose' ? 'the risk team approves each hold' : 'holds are made automatically'}</div>
      <div class="kpis">${kpi('Starting broker funds', cmp(f.total), esc((st.segments || []).join(' + ')))}${kpi('Threshold', cmp(d.threshold), esc(st.threshold_pct) + '% of broker funds')}${kpi('Total held', cmp(d.total_held), num(d.proposed) ? cmp(d.proposed) + ' proposed or waiting' : 'in effect now', num(d.proposed) ? 'warn' : '')}${kpi('Broker funds freed', cmp(d.freed), nf(d.freed_pct, 1) + '% of starting funds', num(d.freed) ? 'good' : '')}${kpi('Settlements paying out', nf(d.trade_dates.length), d.trade_dates.map(fmtDate).join(', ') || 'none')}</div>
      <div class="panel"><div class="hd">${segBtns('phTab', [['holds', 'Holds'], ['sellers', 'Sellers awaiting pay-out'], ['files', 'Files'], ['rules', 'Rules']])}</div></div>` +
      ({ holds: phHolds, sellers: phSellers, files: phFiles, rules: phRules }[UI.phTab] || phHolds)(d, src);
  }
};

function phHolds(d, src) {
  const rows = d.holds.map(h => `<tr><td><button class="linkish mono" data-act="client" data-id="${h.ucc}">${h.ucc}</button><div class="dim small">${esc(h.name)}</div></td>
    <td class="r">${inr(h.sold)}${num(h.bought) ? `<div class="dim small">bought ${inr(h.bought)}</div>` : ''}</td><td class="r">${inr(h.awaiting)}<div class="dim small">charges ${inr(h.charges)}</div></td>
    <td class="r"><b>${inr(h.amount)}</b>${h.amount !== h.proposed ? `<div class="dim small">rule ${inr(h.proposed)}</div>` : ''}${(h.legs || []).map(l => `<div class="dim small">${esc(l.segment)} ${inr(l.before)} → ${inr(l.target)}</div>`).join('')}</td>
    <td>${phPill(h.status)}${h.waiting ? `<div class="dim small wrap">${esc(h.waiting)}</div>` : ''}${h.error ? `<div class="small neg wrap">${esc(h.error)}</div>` : ''}${h.note ? `<div class="dim small wrap">${esc(h.note)}</div>` : ''}${h.parts.filter(p => p.due).map(p => `<div class="dim small wrap">${esc(p.due_why)}</div>`).slice(0, 1).join('')}</td>
    <td class="mono small">${esc(h.msg_id || '—')}${h.release_msg_id ? `<div>${esc(h.release_msg_id)}</div>` : ''}</td>
    <td class="small">${h.parts.map(p => `${esc(p.exchange)} ${fmtDate(p.trade_date)}${p.sett_no ? ' · ' + esc(p.sett_no) : ''}${p.released ? ' ✓' : ''}`).join('<br>')}</td>
    <td>${h.status === 'Proposed' && phRisk() ? `<button class="btn sm pri" data-act="phDecide" data-id="${esc(h.id)}">Approve / edit</button>` : ''}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Holds for ${fmtDate(d.date)}</h3><small>Starting broker funds: ${src}</small></div>
    ${(d.to_nmass || d.release_nmass) && phCan() ? `<div class="bd"><div class="note warn">${d.to_nmass ? `${d.to_nmass} hold(s) to make in N-MASS. ` : ''}${d.release_nmass ? `${d.release_nmass} release(s) to make in N-MASS. ` : ''}Download the N-MASS file under Files, upload it, then <button class="btn sm pri" data-act="phNMASS">Done in N-MASS</button></div></div>` : ''}
    ${tbl([['Client'], ['Sold', 'r'], ['Net awaiting pay-out', 'r'], ['Hold', 'r'], ['Status'], ['N-MASS request'], ['Settlements'], ['']], rows, { tall: true, empty: d.day.applied_at ? 'No client is above the threshold.' : 'Holds are worked out once the BOD files are done.' })}</div>`;
}

function phSellers(d) {
  return `<div class="panel"><div class="hd"><h3>Sale money awaiting pay-out</h3><small>trades of ${d.trade_dates.map(fmtDate).join(', ') || '—'}, net of purchases and charges${d.settings.basis === 'gross' ? ' (gross basis: sale less charges)' : ''}</small></div>
    ${d.error ? `<div class="bd"><div class="note warn">${esc(d.error)}</div></div>` : ''}
    ${tbl([['Client'], ['Sold', 'r'], ['Bought', 'r'], ['Charges', 'r'], ['Awaiting pay-out', 'r'], ['']], d.candidates.map(c => `<tr><td><button class="linkish mono" data-act="client" data-id="${c.ucc}">${c.ucc}</button> ${esc(c.name)}</td><td class="r">${inr(c.sold)}</td><td class="r">${inr(c.bought)}</td><td class="r">${inr(c.charges, 2)}</td><td class="r"><b>${inr(c.awaiting)}</b></td><td>${c.over ? pill('Above threshold', 'warn') : ''}</td></tr>`), { tall: true, empty: 'No client has sale money awaiting pay-out.' })}</div>`;
}

function phFiles(d) {
  const dl = (kind, label, name) => `<button class="btn sm" data-act="dl" data-id="/v1/payout-holds/file?date=${S.date}&kind=${kind}" data-name="${name}">${label}</button>`;
  const c = S.date.replaceAll('-', '');
  return `<div class="grid g2"><div class="panel"><div class="hd"><h3>OMS limit files</h3><small>there is no OMS API: upload to the trading front end</small></div><div class="bd">
      <p class="dim small" style="margin-top:0">${esc(d.oms_note)}</p><div class="split">${dl('oms-hold', 'Hold file', `OMS_HOLD_${c}.csv`)}${dl('oms-release', 'Release file', `OMS_RELEASE_${c}.csv`)}</div></div></div>
    <div class="panel"><div class="hd"><h3>N-MASS allocation files</h3><small>when the allocation API is off</small></div><div class="bd">
      <p class="dim small" style="margin-top:0">Each line is the client's final allocation (not a change), in NSE Clearing's allocation upload layout. After uploading, click Done in N-MASS.</p>
      <div class="split">${dl('nmass-hold', 'Hold file', `ALLOC_HOLD_${c}.txt`)}${dl('nmass-release', 'Release file', `ALLOC_RELEASE_${c}.txt`)}${phCan() && (d.to_nmass || d.release_nmass) ? '<button class="btn sm pri" data-act="phNMASS">Done in N-MASS</button>' : ''}</div></div></div></div>`;
}

function phRules(d) {
  const s = d.settings, can = phRisk();
  const f = (id, label, val, extra = '') => `<div class="fld"><label for="${id}">${label}</label><input class="in" id="${id}" value="${esc(val ?? '')}" ${extra} ${can ? '' : 'disabled'}></div>`;
  const sel = (id, label, val, opts) => `<div class="fld"><label for="${id}">${label}</label><select class="in" id="${id}" ${can ? '' : 'disabled'}>${opts.map(([v, l]) => `<option value="${esc(v)}" ${String(val) === String(v) ? 'selected' : ''}>${esc(l)}</option>`).join('')}</select></div>`;
  return `<div class="panel"><div class="hd"><h3>Rules</h3><small>${s.updated_by ? 'last changed by ' + esc(s.updated_by) + ' ' + esc((s.updated_at || '').slice(0, 16).replace('T', ' ')) : 'Admin or the risk manager'}</small></div><div class="bd">
    <div class="grid g2">${sel('phs-on', 'Pay-out holds', s.enabled ? '1' : '', [['1', 'On'], ['', 'Off']])}${sel('phs-mode', 'Mode', s.mode, [['auto', 'Automatic: holds are made at once'], ['propose', 'Propose: the risk team approves or edits each hold']])}
    ${f('phs-th', 'Threshold (% of starting broker funds)', s.threshold_pct, 'inputmode="decimal"')}${f('phs-hp', 'Hold (% of net sale money awaiting pay-out)', s.hold_pct, 'inputmode="decimal"')}
    ${sel('phs-basis', 'Sale money awaiting pay-out', s.basis, [['net', 'Net: sale − purchases − charges'], ['gross', 'Gross: sale − charges']])}${f('phs-seg', 'Broker funds of segments (comma-separated)', (s.segments || []).join(', '))}
    ${f('phs-min', 'Minimum hold (₹)', s.min_hold, 'inputmode="decimal"')}${f('phs-at', 'Apply at (when the BOD files are not detected)', s.apply_at, 'placeholder="08:45"')}
    ${f('phs-pat', 'Pay-out credit: remitter or narration pattern', s.payout_pattern, 'placeholder="NSE CLEARING|NSCCL"')}${f('phs-acct', 'Pay-out credit: settlement account (part of the number)', s.payout_account)}
    ${f('phs-rel', 'Assume paid out at (empty: off; e.g. 13:30)', s.release_at, 'placeholder="13:30"')}</div>
    ${can ? '<div class="ft" style="padding:0;border:0"><button class="btn pri" data-act="phSave">Save rules</button></div>' : '<div class="dim small">Only the risk manager or Admin changes these.</div>'}</div></div>`;
}

Object.assign(A, {
  phApply: () => act(() => post('/v1/payout-holds/apply', { date: S.date }), v => `${v.holds.length} hold(s) on ${fmtDate(v.date)}`),
  phPayout: () => {
    const ks = (C.payouthold?.settlements || []).filter(p => !p.due);
    confirmModal('Pay-out received', 'Gives back the held allocation and OMS limit for the settlements paid out. Tick only those whose money has arrived.',
      (ks.length ? ks.map(p => `<label class="check"><input type="checkbox" class="ph-k" value="${esc(p.key)}" checked> ${esc(p.exchange)} trades of ${fmtDate(p.trade_date)}${p.sett_no ? ' · settlement ' + esc(p.sett_no) : ''} (${esc(p.clearing)})</label>`).join('') : '<p class="dim">No held settlement is waiting; this records the day as paid out.</p>') +
      '<div class="fld"><label for="ph-pn">How you know</label><input class="in" id="ph-pn" placeholder="bank portal, NSE Clearing mail"></div>', 'Release holds',
      () => { const all = [...document.querySelectorAll('.ph-k')], on = all.filter(x => x.checked).map(x => x.value); return act(() => post('/v1/payout-holds/payout', { date: S.date, settlements: on.length === all.length ? [] : on, note: $('ph-pn').value.trim() }), 'Pay-out recorded'); });
  },
  phFunds: () => {
    const e = C.payouthold?.funds_entry?.segments || {};
    confirmModal('Starting broker funds', 'Free broker funds at NSE Clearing this morning, from N-MASS or NSE Clearing\'s screen. Used only when the margins API and the N-MASS file give no figure.',
      `<div class="grid g2">${['CM', 'FO', 'CD'].map(s => `<div class="fld"><label for="ph-f-${s}">${s} (₹)</label><input class="in mono" id="ph-f-${s}" inputmode="decimal" value="${esc(e[s] || '')}"></div>`).join('')}</div><div class="fld"><label for="ph-fn">Where the figure comes from</label><input class="in" id="ph-fn"></div>`, 'Save',
      () => act(() => post('/v1/payout-holds/funds', { date: S.date, segments: Object.fromEntries(['CM', 'FO', 'CD'].map(s => [s, $('ph-f-' + s).value.trim()]).filter(x => x[1])), note: $('ph-fn').value.trim() }), 'Broker funds saved'));
  },
  phDecide: id => {
    const h = C.payouthold.holds.find(x => x.id === id); if (!h) return;
    modal(mhd('Pay-out hold · ' + esc(h.ucc), `${esc(h.name)} · sold ${inr(h.sold)} · awaiting pay-out ${inr(h.awaiting)}`) + `<div class="bd">
      <div class="fld"><label for="ph-amt">Hold (₹, at most ${inr(h.awaiting)})</label><input class="in mono" id="ph-amt" inputmode="decimal" value="${esc(String(h.amount))}"></div>
      <div class="fld"><label for="ph-dn">Note (needed to reject)</label><input class="in" id="ph-dn"></div><div id="ph-derr"></div></div>
      <div class="ft"><button class="btn" id="ph-rej">Do not hold</button><button class="btn pri" id="ph-ok">Approve and send</button></div>`);
    const go = approve => act(() => post(`/v1/payout-holds/holds/${encodeURIComponent(id)}/decide`, { approve, amount: approve ? $('ph-amt').value.trim() : '', note: $('ph-dn').value.trim() }), approve ? 'Hold approved' : 'Hold rejected').catch(e => { $('ph-derr').innerHTML = errHTML(e); });
    $('ph-ok').onclick = () => go(true); $('ph-rej').onclick = () => go(false);
  },
  phNMASS: () => confirmModal('Done in N-MASS', 'Records that the N-MASS file was uploaded: each client\'s allocation is taken as the figure in the file.', '<div class="fld"><label for="ph-nn">Note</label><input class="in" id="ph-nn" placeholder="uploaded at 09:02"></div>', 'Done',
    () => act(() => post('/v1/payout-holds/nmass-done', { date: S.date, ids: [], note: $('ph-nn').value.trim() }), 'Recorded')),
  phSave: () => {
    const s = { ...C.payouthold.settings };
    Object.assign(s, { enabled: $('phs-on').value === '1', mode: $('phs-mode').value, threshold_pct: $('phs-th').value.trim(), hold_pct: $('phs-hp').value.trim(), basis: $('phs-basis').value,
      segments: $('phs-seg').value.split(',').map(x => x.trim().toUpperCase()).filter(Boolean), min_hold: $('phs-min').value.trim() || '0', apply_at: $('phs-at').value.trim(),
      payout_pattern: $('phs-pat').value.trim(), payout_account: $('phs-acct').value.trim(), release_at: $('phs-rel').value.trim() });
    delete s.updated_by; delete s.updated_at;
    act(() => api('PUT', '/v1/payout-holds/settings', s), 'Rules saved');
  },
});
