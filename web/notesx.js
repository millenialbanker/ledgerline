'use strict';
/* Ledgerline console: registers that feed the contract notes, on
   Operations → Contract notes, and the nomination pop-up in the client
   portal.

   Contract notes: trades the exchange cancelled (RTCM, SGTCM), recorded by
   an Ops checker from the exchange's message, which drop out of bills and
   notes; the register of rights entitlement securities, whose purchases
   carry the RE disclaimer; a client's electronic contract note
   authorisation (with the signed document); the account opening kit.
   Client portal: the pop-up on the benefits of nominating, at the first
   login of the day of a demat account without a nominee.

   Server: internal/ops/notesx.go, onboardkit.go, contactletter.go;
   internal/api/notesx.go. Loaded after app.js; it wraps V.notes and
   V.portal. Names are prefixed nx. */

const nxNotes = V.notes, nxPortal = V.portal;
V.notes = {
  load: async () => {
    const d = await nxNotes.load();
    const [cx, re] = await Promise.all([get('/v1/notes/cancellations').catch(() => []), get('/v1/notes/rights-entitlements').catch(() => [])]);
    C.nx = { cx: cx || [], re: re || [] };
    return d;
  },
  html: d => nxNotes.html(d) + nxPanel(C.nx || { cx: [], re: [] })
};
V.portal = {
  load: nxPortal.load,
  html: v => {
    if (v.nomination_nudge && UI.nxNudged !== v.account.ucc) {
      UI.nxNudged = v.account.ucc;
      setTimeout(() => confirmModal('Add a nominee', '', `<p>${esc(v.nomination_nudge)}</p>`, 'Add a nominee', () => { A.go('pprofile'); }), 50);
    }
    return nxPortal.html(v);
  }
};

function nxPanel(x) {
  const chk = has('Ops checker'), mk = has('Ops maker', 'Ops checker');
  return `<div class="panel"><div class="hd"><h3>Trades cancelled by the exchange</h3><small>They drop out of obligations, bills and notes; the client gets a revised note.</small>${chk ? '<button class="btn sm" data-act="nxCancel">Record a cancellation</button>' : ''}</div>
    ${tbl([['Trade date'], ['Trade'], ['Client'], ['Contract'], ['B/S'], ['Qty', 'r'], ['From'], ['Reference'], ['Bill and note']], x.cx.map(c => `<tr><td class="n">${fmtDate(c.date)}</td><td class="mono">${esc(c.exchange)}-${esc(c.segment)} ${esc(c.trade_no)}</td><td class="mono">${esc(c.ucc)}</td><td>${esc(c.contract)}</td><td>${c.side === 'B' ? 'Buy' : 'Sell'}</td><td class="r">${c.qty}</td><td class="small">${esc(c.source)}</td><td class="small">${esc(c.ref)}</td><td class="small">${esc(c.rebilled || '')}</td></tr>`), { empty: 'No trade cancelled by an exchange.' })}</div>
  <div class="panel"><div class="hd"><h3>Rights entitlements</h3><small>A purchase of these prints the RE disclaimer on the contract note.</small>${mk ? '<button class="btn sm" data-act="nxRE">Add an RE</button>' : ''}</div>
    ${tbl([['Exchange'], ['Symbol'], ['ISIN'], ['Company'], ['Issue closes'], ['Notice']], x.re.map(r => `<tr><td>${esc(r.exchange)}</td><td class="mono">${esc(r.symbol)}</td><td class="mono">${esc(r.isin || '')}</td><td>${esc(r.company || '')}</td><td class="n">${fmtDate(r.issue_closes)}</td><td class="small">${esc(r.ref || '')}</td></tr>`), { empty: 'No rights entitlements recorded. A symbol ending -RE is treated as one too.' })}</div>
  ${mk ? `<div class="panel"><div class="hd"><h3>Client documents</h3><small>Electronic contract notes need the client's signed authorisation; the kit goes to every new client.</small><button class="btn sm" data-act="nxECN">Electronic contract note authorisation</button><button class="btn sm" data-act="nxKit">Send account opening kit</button></div></div>` : ''}`;
}

Object.assign(A, {
  nxCancel: () => confirmModal('Trade cancelled by the exchange', 'From the exchange’s RTCM or SGTCM message. The client’s bill is corrected and a revised contract note sent.',
    `<div class="fld"><label for="nx-d">Trade date</label><input class="in" type="date" id="nx-d" value="${esc(S.date)}"></div><div class="fld"><label for="nx-x">Exchange</label><select class="in" id="nx-x">${['NSE', 'BSE', 'MCX'].map(e => `<option>${e}</option>`).join('')}</select></div><div class="fld"><label for="nx-s">Segment</label><select class="in" id="nx-s"><option value="">Any</option>${['CM', 'FO', 'CD', 'COM'].map(e => `<option>${e}</option>`).join('')}</select></div><div class="fld"><label for="nx-t">Trade number</label><input class="in mono" id="nx-t"></div><div class="fld"><label for="nx-src">From</label><select class="in" id="nx-src">${['RTCM (reversal trade cancellation)', 'SGTCM', 'Exchange trade cancellation message', 'Exchange circular or e-mail'].map(e => `<option>${e}</option>`).join('')}</select></div><div class="fld"><label for="nx-r">The exchange’s reference</label><input class="in" id="nx-r"></div><div class="fld"><label for="nx-n">Note</label><input class="in" id="nx-n"></div>`, 'Record',
    () => act(() => post('/v1/notes/cancellations', { date: $('nx-d').value, exchange: $('nx-x').value, segment: $('nx-s').value, trade_no: $('nx-t').value, source: $('nx-src').value, ref: $('nx-r').value, note: $('nx-n').value }), c => `Trade ${c.trade_no} cancelled. ${c.rebilled || ''}`), true),
  nxRE: () => confirmModal('Rights entitlement', 'From the exchange’s notice of RE trading.',
    `<div class="fld"><label for="nx-x">Exchange</label><select class="in" id="nx-x"><option>NSE</option><option>BSE</option></select></div><div class="fld"><label for="nx-sym">Symbol as in the trade file</label><input class="in mono" id="nx-sym"></div><div class="fld"><label for="nx-i">ISIN</label><input class="in mono" id="nx-i"></div><div class="fld"><label for="nx-c">Company</label><input class="in" id="nx-c"></div><div class="fld"><label for="nx-cl">Rights issue closes on</label><input class="in" type="date" id="nx-cl"></div><div class="fld"><label for="nx-r">Exchange notice</label><input class="in" id="nx-r"></div>`, 'Save',
    () => act(() => post('/v1/notes/rights-entitlements', { exchange: $('nx-x').value, symbol: $('nx-sym').value, isin: $('nx-i').value, company: $('nx-c').value, issue_closes: $('nx-cl').value, ref: $('nx-r').value }), 'Saved')),
  nxECN: () => clientCache().then(() => confirmModal('Electronic contract note authorisation', 'Electronic notes need the client’s signed authorisation, added first as a client document; printed notes on the client’s request.',
    `<div class="fld"><label for="nx-u-q">Client</label>${cpick('nx-u', '', {})}</div><div class="fld"><label for="nx-on">Contract notes</label><select class="in" id="nx-on"><option value="1">Electronic (signed authorisation attached)</option><option value="">Printed, by post</option></select></div><div class="fld"><label for="nx-doc">Signed authorisation (client document ID, DOC-…)</label><input class="in mono" id="nx-doc"></div>`, 'Record',
    () => act(() => post(`/v1/clients/${encodeURIComponent($('nx-u').value)}/ecn`, { on: !!$('nx-on').value, doc: $('nx-doc').value.trim() }), 'Recorded'))),
  nxKit: () => clientCache().then(() => confirmModal('Send the account opening kit', 'Signed, through the dispatch register; by post for a client on printed notes.',
    `<div class="fld"><label for="nx-k-q">Client</label>${cpick('nx-k', '', {})}</div>`, 'Send',
    () => act(() => post(`/v1/clients/${encodeURIComponent($('nx-k').value)}/kit`, {}), d => `Kit ${d.status === 'Sent' ? 'sent' : (d.status || '').toLowerCase()}`)))
});
