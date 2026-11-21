'use strict';
/* Ledgerline console: Finspot by its Noren admin API (finspotapi.go).
   A panel on Live RMS (RMS & margin): each client's Finspot limit now and
   what was last pushed, the start-of-day parts done today, the latest
   pushes with Finspot's refusals, Push to Finspot now (all parts or one),
   and reading a client's limit and holdings back from Finspot.
   Loaded after app.js and bowpush.js; it wraps the Live RMS screen. */

Object.assign(UI, { fnQ: '', fnF: 'All' });
const FN_PART = { blocks: 'Blocks', limits: 'Limits', holdings: 'ISIN holdings', t1: 'T1 holdings', baskets: 'Margin baskets', clients: 'Client logins' };
const FN_ST = { Pushed: 'good', Unchanged: 'good', Failed: 'bad' };
const fnRisk = () => has('Risk manager');
const fnStamp = t => t ? `${fmtDate(t.slice(0, 10))} ${t.slice(11, 16)}` : '—';
const fnPartSum = p => `${esc(p.name)}: ${p.pushed} sent${p.cleared ? `, ${p.cleared} cleared` : ''}${p.unchanged ? `, ${p.unchanged} unchanged` : ''}${p.failed ? `, <span class="neg">${p.failed} refused</span>` : ''}${p.skipped ? `, ${p.skipped} not sent` : ''}${p.stopped ? ` <span class="neg">(stopped: ${esc(p.stopped)})</span>` : ''}`;

{
  const load = V.live.load, html = V.live.html;
  V.live.load = async () => {
    const d = await load();
    try { d.fnapi = await get(`/v1/finspot-api?date=${S.date}`); }
    catch (e) { d.fnapi = e.status === 409 || e.status === 404 || e.status === 403 ? null : { error: e.message }; }
    return d;
  };
  V.live.html = d => html(d) + fnPanel(d.fnapi);
}

function fnPanel(f) {
  if (!f) return '';
  if (f.error) return `<div class="panel"><div class="hd"><h3>Finspot by API</h3></div><div class="bd"><div class="note warn">${esc(f.error)}</div></div></div>`;
  const s = f.session || {}, parts = f.parts || [], last = (f.runs || [])[0];
  const bod = (f.bod_parts || parts.filter(p => p !== 'clients')).map(p => `${FN_PART[p]}${f.bod_done[p] ? ' ✓' : ''}`).join(', ') || 'nothing by API: the morning files';
  const diff = f.rows.filter(r => r.differs).length, failed = f.rows.filter(r => r.last.status === 'Failed').length;
  return `<div class="panel"><div class="hd"><h3>Finspot by API</h3><small>Noren WC Admin ${esc(f.env)} · ${parts.map(p => FN_PART[p]).join(', ')}</small>
      <div class="tools">${fnRisk() ? parts.map(p => `<button class="btn sm" data-act="fnPush" data-id="${p}">${FN_PART[p]}</button>`).join('') + '<button class="btn sm pri" data-act="fnPush" data-id="">Push to Finspot now</button>' : ''}</div></div>
    <div class="bd"><div class="note">${pill('Connection: ' + f.status, f.status === 'Working' ? 'good' : f.status === 'Problem' ? 'bad' : 'nt')}${f.problem ? ` <span class="small ${f.status === 'Problem' ? 'neg' : 'dim'}">${esc(f.problem)}</span>` : ''} ·
      ${s.signed_in ? pill('Signed in ' + fnStamp(s.at) + (s.broker ? ' · broker ' + esc(s.broker) : ''), 'good') : pill('Signs in at the next push', 'nt')} ·
      ${f.auto ? pill(`Automatic: start of day ${esc(f.bod_at)} (${bod}), then changes every ${f.every} min to 20:00`, 'acc') : pill('Automatic pushes off (Connections)', 'warn')}
      · notional cash: ${esc(f.notional)} · blocks: ${esc(f.blocks)}</div>
      ${(f.notes || []).map(n => `<div class="note small dim">${esc(n)}</div>`).join('')}
      ${(f.runs || []).length ? `<div class="small"><b>Latest pushes</b>${f.runs.slice(0, 5).map(r => `<div><button class="linkish mono" data-act="fnRun" data-id="${esc(r.id)}">${fnStamp(r.at)} ${esc(r.kind)}</button>${!r.by.startsWith('system') ? ` <span class="dim">by ${esc(r.by)}</span>` : ''} · ${(r.parts || []).map(fnPartSum).join(' · ') || '<span class="dim">nothing to send</span>'}${r.stopped ? ` <span class="neg">stopped: ${esc(r.stopped)}</span>` : ''}</div>`).join('')}</div>` : last ? '' : '<div class="small dim">Nothing pushed yet.</div>'}
      <div class="tools">${segBtns('fnF', [['All', 'All'], ['Differs', `Not yet in Finspot · ${diff}`], ['Failed', `Refused · ${failed}`]], 'fn-t')}<input class="in" data-ui="fnQ" data-part="fn-t" placeholder="Filter name or UCC" value="${esc(UI.fnQ)}"></div></div>
    <div id="fn-t">${P['fn-t']({ fnapi: f })}</div></div>`;
}

P['fn-t'] = d => {
  const f = d.fnapi; if (!f || !f.rows) return '';
  const q = UI.fnQ.trim().toLowerCase();
  const rows = f.rows.filter(r => (UI.fnF === 'All' || (UI.fnF === 'Differs' ? r.differs : r.last.status === 'Failed')) && (!q || (r.ucc + ' ' + r.name).toLowerCase().includes(q)));
  return tbl([['Client'], ['Finspot account'], ['Cash now', 'r'], ['Notional now', 'r'], ['Last pushed', 'r'], ['Holdings sent', 'r'], ['Status'], ['']], rows.map(r => `<tr>
    <td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}${r.limit.why ? `<div class="dim small">${esc(r.limit.why)}</div>` : ''}<div class="dim small">${r.login ? 'Finspot login: ' + esc(r.login) : 'No Finspot login known'}</div></td>
    <td class="mono small">${esc(r.limit.act_id)}</td>
    <td class="r">${inr(r.limit.cash)}</td><td class="r">${inr(r.limit.notional)}</td>
    <td class="r small">${r.last.status ? `${inr(r.last.cash)} / ${inr(r.last.notional)}<div class="dim">${fnStamp(r.last.at)}</div>` : '<span class="dim">never</span>'}</td>
    <td class="r small">${r.holdings || r.t1 ? `${r.holdings} ISIN${r.holdings === 1 ? '' : 's'}${r.t1 ? ` + ${r.t1} T1` : ''}<div class="dim">${r.hold_on ? fmtDate(r.hold_on) : ''}</div>` : '<span class="dim">—</span>'}</td>
    <td>${r.last.status ? pill(r.last.status, FN_ST[r.last.status] || 'nt') : pill('Not pushed', 'nt')}${r.queued ? ' ' + pill('change queued', 'acc') : r.differs && r.last.status ? ' ' + pill('changed', 'warn') : ''}${r.last.error ? `<div class="small neg wrap">${esc(r.last.error)}</div>` : ''}</td>
    <td>${fnRisk() ? `<button class="btn sm" data-act="fnPushOne" data-id="${esc(r.ucc)}">Push</button><button class="btn sm" data-act="fnCheck" data-id="${esc(r.ucc)}">Check Finspot</button>` : ''}</td></tr>`), { tall: true, empty: 'No active clients.' });
};

const fnRunMsg = v => v.stopped ? 'Stopped: ' + v.stopped : (v.parts || []).map(p => `${p.name}: ${p.pushed} sent${p.failed ? `, ${p.failed} refused` : ''}`).join(' · ') || 'Nothing to send';

Object.assign(A, {
  fnPush: part => act(() => post('/v1/finspot-api/push', { date: S.date, parts: part ? [part] : [] }), fnRunMsg),
  fnPushOne: id => act(() => post('/v1/finspot-api/push', { date: S.date, uccs: [id] }), fnRunMsg),
  fnRun: async id => {
    let r;
    try { r = await get(`/v1/finspot-api/runs/${encodeURIComponent(id)}`); } catch (e) { toast(e.message, true); return; }
    const calls = r.calls || [];
    modal(mhd(`Finspot push ${esc(r.id)}`, `${esc(r.kind)} · ${fnStamp(r.at)}${!r.by.startsWith('system') ? ' · by ' + esc(r.by) : ''}`) + `<div class="bd">
      ${(r.parts || []).map(p => `<div>${fnPartSum(p)}${p.note ? ` <span class="dim small">${esc(p.note)}</span>` : ''}</div>`).join('')}
      ${r.stopped ? `<div class="note warn">${esc(r.stopped)}</div>` : ''}
      ${(r.failures || []).length ? `<h4>Refused</h4><div class="small mono wrap">${r.failures.map(esc).join('<br>')}${r.more_failures ? `<br>and ${r.more_failures} more` : ''}</div>` : ''}
      <h4>Requests</h4>${tbl([['Request'], ['Client'], ['What'], ['Answer']], calls.map(c => `<tr><td class="mono small">${esc(c.function)}</td><td class="mono small">${esc(c.ucc || '')}</td><td class="small">${esc(c.request)}</td><td class="small ${c.error ? 'neg' : ''}">${esc(c.error || c.response || '')}</td></tr>`), { empty: 'No requests.' })}
      ${r.more_calls ? `<div class="dim small">and ${r.more_calls} more requests that went through</div>` : ''}</div><div class="ft"><button class="btn" data-act="close">Close</button></div>`);
  },
  fnCheck: async id => {
    let c;
    try { c = await post(`/v1/finspot-api/check/${encodeURIComponent(id)}`, {}); } catch (e) { toast(e.message, true); return; }
    const bad = (c.holdings || []).filter(h => !h.match);
    modal(mhd(`Finspot has · ${esc(c.act_id)}`, `Read from Finspot ${fnStamp(c.at)}; nothing read is kept.`) + `<div class="bd">
      <div>${pill(c.limit_match ? 'Limit as pushed' : 'Limit differs', c.limit_match ? 'good' : 'bad')} ${pill(c.hold_match ? 'Holdings as pushed' : `${bad.length} holding(s) differ`, c.hold_match ? 'good' : 'bad')}</div>
      ${tbl([[''], ['Finspot', 'r'], ['Last pushed', 'r']], [
        `<tr><td>Cash</td><td class="r">${esc(c.cash || '—')}</td><td class="r">${esc(c.sent_cash)}</td></tr>`,
        `<tr><td>Notional cash</td><td class="r">${esc(c.notional || '—')}</td><td class="r">${esc(c.sent_notional)}</td></tr>`,
        `<tr><td class="dim">Pay-in · pay-out today</td><td class="r dim">${esc(c.payin || '0')} · ${esc(c.payout || '0')}</td><td></td></tr>`,
        `<tr><td class="dim">Margin used · collateral</td><td class="r dim">${esc(c.margin_used || '0')} · ${esc(c.collateral || '0')}</td><td></td></tr>`])}
      <h4>Holdings (CNC)</h4>${tbl([['ISIN'], ['Finspot', 'r'], ['Last pushed', 'r'], ['']], (c.holdings || []).map(h => `<tr><td class="mono small">${esc(h.isin)}</td><td class="r">${h.finspot}</td><td class="r">${h.sent}</td><td>${h.match ? '' : pill('differs', 'bad')}</td></tr>`), { empty: 'No holdings on either side.' })}
      </div><div class="ft"><button class="btn" data-act="close">Close</button></div>`);
  },
});
