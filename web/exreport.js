'use strict';
/* Ledgerline console: reports sent from Exchange files. The client funds
   report (SEGMON) gets its pre-upload check, the download and NSE
   Clearing's response file; the holding statement gets the NSE API
   submission and its history. Loaded after app.js; it takes over the
   Download button of those two rows on the "xfiles" screen. */

const xrPill = s => pill(s, { Accepted: 'good', 'Partly accepted': 'warn', Rejected: 'bad', Failed: 'bad', 'Not sent': 'warn' }[s] || 'nt');
const xrStamp = t => { const d = new Date(t); return isNaN(d) ? esc(t || '') : d.toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'short' }); };
const xrIssue = i => `<li>${i.ucc ? `<b>${esc(i.ucc)}</b>${i.segment ? ' ' + esc(i.segment) : ''}${i.indicator && i.indicator !== 'NA' ? ' · ' + esc(i.indicator) : ''}: ` : ''}${esc(i.message)}${i.column ? ` <span class="dim small">(column ${i.column})</span>` : ''}</li>`;

function xrSegmonHTML(d, chk, resps) {
  const t = chk.totals || {}, e = chk.errors || [], w = chk.warnings || [];
  const list = (xs, n) => xs.length ? `<ul class="small">${xs.slice(0, n).map(xrIssue).join('')}${xs.length > n ? `<li>…and ${xs.length - n} more</li>` : ''}</ul>` : '';
  return mhd('Client funds and collateral (SEGMON)', `For ${fmtDate(d)} · ${esc(chk.file || '')}`) + `<div class="bd">
    <div class="kpis">${kpi('Lines', nf(chk.rows), nf(chk.clients) + ' clients')}${kpi('Errors', nf(e.length), e.length ? 'the file is not made' : 'none', e.length ? 'bad' : 'good')}${kpi('Warnings', nf(w.length), 'check before uploading', w.length ? 'warn' : '')}</div>
    <div class="small">Ledger in the file ${inr(t.ledger)} · books ${inr(t.books)} · cash with NSE Clearing ${inr(t.cash_ncl)} (allocation ${inr(t.allocated)}) · securities ${inr(t.non_cash_ncl)} · uncleared ${inr(t.uncleared)} · early pay-in ${inr(t.epi)} · settlement ${inr(t.settlement)} · MTF cash collateral ${inr(t.mtf_cash)}</div>
    ${e.length ? `<div class="note bad" style="margin-top:10px"><b>Fix these first.</b>${list(e, 30)}</div>` : ''}
    ${w.length ? `<div class="note warn" style="margin-top:10px">${list(w, 15)}</div>` : ''}
    <div class="split" style="margin-top:12px"><button class="btn pri" data-act="dl" data-id="/v1/exchange-files/segmon-client-funds?date=${d}" data-name="${esc(chk.file || 'segmon.csv')}" ${e.length ? 'disabled' : ''}>Download the file</button>
      <button class="btn" data-act="xrSegResp" data-id="${d}">Load NSE Clearing's response</button></div>
    ${resps.length ? `<h3 style="margin-top:14px">Responses read</h3>${tbl([['File'], ['Lines', 'r'], ['Not accepted', 'r'], ['Read']], resps.map(r => `<tr><td class="mono small wrap">${esc(r.name)}</td><td class="r">${r.lines}</td><td class="r ${r.rejected ? 'neg' : ''}">${r.rejected}</td><td class="small">${esc(r.at)} · ${esc(r.by)}</td></tr>`))}
      ${resps[0].rejections.length ? `<ul class="small">${resps[0].rejections.slice(0, 20).map(x => `<li>${esc(x.plain)}</li>`).join('')}</ul>` : ''}<div class="dim small">${esc(resps[0].note)}</div>` : ''}
  </div>`;
}

function xrHoldingHTML(d, v) {
  const conn = v.connection === 'Not set' ? `<div class="note">The NSE holding statement API is not set up (Settings → Connections). Download the file and upload it on NSE's portal.</div>`
    : `<div class="small">Connection: ${pill(v.connection, CONN_ST[v.connection] || 'nt')} · automatic submission ${v.auto ? 'on' : 'off'}${v.state.last_ok ? ' · last accepted ' + fmtDate(v.state.last_ok) : ''}${v.problem ? ` · <span class="neg">${esc(v.problem)}</span>` : ''}</div>`;
  const can = has('Ops maker', 'Ops checker', 'Compliance officer');
  return mhd('Client holding statement', `Holdings for ${fmtDate(d)}, as reported to NSE (and BSE by their portal).`) + `<div class="bd">${conn}
    <div class="split" style="margin-top:12px"><button class="btn" data-act="dl" data-id="/v1/exchange-files/holding-statement-api?date=${d}" data-name="holding_${d}.json">Download the file</button>
      ${can && v.connection !== 'Not set' ? `<button class="btn pri" data-act="xrHoldSubmit" data-id="${d}">Submit to NSE now</button>` : ''}</div>
    <h3 style="margin-top:14px">Submissions for ${fmtDate(d)}</h3>
    ${tbl([['File'], ['Records', 'r'], ['Status'], ['NSE'], ['When']], v.submissions.map(s => `<tr><td class="mono small">${esc(s.file)}</td><td class="r">${nf(s.records)}</td><td>${xrPill(s.status)}${s.reason ? `<div class="small wrap">${esc(s.reason)}</div>` : ''}${(s.errors || []).length ? `<ul class="small">${s.errors.slice(0, 8).map(x => `<li>${esc([x.ucc, x.isin].filter(Boolean).join(' '))}${x.ucc ? ': ' : ''}${esc(x.plain)}</li>`).join('')}${s.errors.length > 8 ? `<li>…and ${s.errors.length - 8} more</li>` : ''}</ul>` : ''}</td>
      <td class="small">${s.ack_id ? 'ACK ' + esc(s.ack_id) : ''}${(s.hdr_code || []).length ? `<div class="dim">code ${esc(s.hdr_code.join(', '))}</div>` : ''}</td><td class="small">${xrStamp(s.at)}<div class="dim">${esc(s.by)}</div></td></tr>`), { empty: 'Nothing submitted for this date.' })}</div>`;
}

const xrPrevDl = A.xfDl;
Object.assign(A, {
  xfDl: async (id, el) => {
    const d = $('xf-date')?.value || S.date; UI.xfDate = d;
    try {
      if (id === 'segmon-client-funds') {
        const [chk, resps] = await Promise.all([get(`/v1/segmon/check?date=${d}`), get(`/v1/segmon/responses?date=${d}`).catch(() => [])]);
        modal(xrSegmonHTML(d, chk, resps));
        return;
      }
      if (id === 'holding-statement-api') {
        modal(xrHoldingHTML(d, await get(`/v1/holding-api?date=${d}`)));
        return;
      }
    } catch (e) { toast(e.message, true); return; }
    xrPrevDl(id, el);
  },
  xrSegResp: d => {
    const inp = document.createElement('input'); inp.type = 'file';
    inp.onchange = async () => {
      const f = inp.files[0]; if (!f) return;
      try {
        toast(`Reading ${f.name}…`);
        const r = await api('POST', `/v1/segmon/response?date=${d}&name=${encodeURIComponent(f.name)}`, f, true);
        toast(r.rejected ? `${r.rejected} line(s) not accepted: work items opened` : `All ${r.lines} line(s) accepted`);
        A.xfDl('segmon-client-funds');
      } catch (e) { toast(e.message, true); }
    };
    inp.click();
  },
  xrHoldSubmit: async d => {
    try {
      toast('Submitting to NSE…');
      const s = await post('/v1/holding-api/submit', { date: d });
      toast(s.status === 'Accepted' ? `Accepted by NSE${s.ack_id ? ', ACK ' + s.ack_id : ''}` : `${s.status}: ${s.reason || 'see the submission'}`, s.status !== 'Accepted');
      A.xfDl('holding-statement-api');
    } catch (e) { toast(e.message, true); }
  },
});
