'use strict';
/* Ledgerline console: public issues (IPOs, SME IPOs, NCDs, offers for sale).
   Loaded after app.js and uses its helpers. Staff screen: ipo (replaces the
   earlier IPO & mutual funds screen, with mutual fund orders kept as a tab).
   Client portal page: pipo. */

const IPO_ST = { Draft: 'nt', Uploaded: 'acc', 'Mandate pending': 'warn', Accepted: 'good', Declined: 'bad', Failed: 'bad', Rejected: 'bad', Cancelled: 'nt', Allotted: 'good', 'Not allotted': 'nt', Unblocked: 'acc', Credited: 'good' };
const ISS_ST = { Open: 'good', Upcoming: 'acc', Closed: 'warn', Allotment: 'acc', Listed: 'nt', Withdrawn: 'bad' };
const ipoPill = s => pill(s || '—', IPO_ST[s] || 'nt');
const ipoEnc = encodeURIComponent;
const ipoNow = () => { const d = new Date(Date.now() + 5.5 * 3600e3); return d.toISOString().slice(0, 16); };
const ipoTime = s => { if (!s) return '—'; const t = new Date(s); return isNaN(t) ? esc(s) : esc(t.toLocaleString('en-GB', { timeZone: 'Asia/Kolkata', day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit', hour12: false }).replace(',', '')); };
const ipoOpts = b => (b.options || []).map(o => `${o.lots} lot${o.lots > 1 ? 's' : ''} @ ${o.cut_off ? 'cut-off' : '₹' + esc(o.price)}${o.series ? ' · ' + esc(o.series) : ''}`).join('<br>') || `${b.lots} lot(s)`;
const ipoIssueOf = id => (C.ipo?.is?.issues || []).find(i => i.id === id);
const ipoCanBid = () => has('Ops maker', 'Ops checker', 'Branch staff', 'Authorised person');
const ipoMk = () => has('Ops maker', 'Ops checker');

/* ---------- staff screen ---------- */
V.ipo = {
  load: async () => {
    const tab = UI.ipoTab || 'bids', iq = UI.ipoIssue ? `issue=${ipoEnc(UI.ipoIssue)}` : '';
    const [is, bids] = await Promise.all([get('/v1/ipo/issues'), get('/v1/ipo/bids?' + iq)]);
    const d = { is, bids };
    if (!UI.ipoIssue && is.issues.length && (tab === 'allot')) UI.ipoIssue = is.issues[0].id;
    if (tab === 'mandates') d.mandates = await get('/v1/ipo/mandates');
    if (tab === 'allot' && UI.ipoIssue) d.recon = await get(`/v1/ipo/issues/${ipoEnc(UI.ipoIssue)}/reconciliation`).catch(e => ({ error: e.message }));
    if (tab === 'files') [d.batches, d.eipo] = await Promise.all([get('/v1/ipo/batches?' + iq), get('/v1/ipo/eipo')]);
    if (tab === 'log') d.log = await get(`/v1/ipo/instructions?from=${ipoEnc(UI.ipoFrom || '')}&to=${ipoEnc(UI.ipoTo || '')}`);
    if (tab === 'mf') [d.mf, d.legacy] = await Promise.all([get('/v1/mf'), get('/v1/ipo')]);
    return d;
  },
  html: d => {
    const tab = UI.ipoTab || 'bids', iss = d.is.issues, open = iss.filter(i => i.status === 'Open'), c = d.bids.counts || {};
    const acts = `${ipoMk() ? '<button class="btn" data-act="ipoIssueEdit">New issue</button>' : ''}${ipoCanBid() && iss.some(i => ['Open', 'Upcoming'].includes(i.status)) ? '<button class="btn pri" data-act="ipoBidNew">New bid</button>' : ''}`;
    const tabs = [['bids', 'Bids'], ['issues', 'Issues'], ['mandates', 'UPI mandates'], ['allot', 'Allotment & refunds'], ['files', 'Files & eIPO API'], ['log', 'Consent log'], ['mf', 'Mutual funds & older bids']];
    return ph('IPO & public issues', 'IPOs, SME IPOs, NCDs and offers for sale: the issue’s rules, each bid on the client’s prior instruction with an acknowledgement, the day’s bid file (or NSE’s eIPO API), UPI mandates to the 5 pm cut-off, then allotment, release of blocked money and the demat credit by T+2.', acts) +
      `<div class="kpis">${kpi('Issues open', open.length, open.map(i => esc(i.name)).join(', ') || 'none today', open.length ? 'good' : '')}${kpi('Bids', d.bids.bids.length, `${c.Draft || 0} not yet sent`)}${kpi('Mandates pending', (c['Mandate pending'] || 0) + (c.Uploaded || 0), `${c.Accepted || 0} accepted · ${(c.Declined || 0) + (c.Failed || 0)} declined or failed`, (c['Mandate pending'] || 0) ? 'warn' : '')}${kpi('Allotted', (c.Allotted || 0) + (c.Unblocked || 0) + (c.Credited || 0), `${c.Credited || 0} credited to demat`)}</div>` +
      `<div class="gtabs" role="tablist">${tabs.map(([k, l]) => `<button role="tab" class="${tab === k ? 'on' : ''}" aria-selected="${tab === k}" data-act="ipoTab" data-id="${k}">${l}</button>`).join('')}</div>` +
      (P['ipo_' + tab] ? P['ipo_' + tab](d) : '');
  }
};

function ipoIssuePick(d, all) {
  return `<select class="in sm" id="ipo-iss" aria-label="Issue">${all ? '<option value="">All issues</option>' : ''}${d.is.issues.map(i => `<option value="${esc(i.id)}" ${UI.ipoIssue === i.id ? 'selected' : ''}>${esc(i.name)} · ${esc(i.status)}</option>`).join('')}</select>`;
}

P.ipo_bids = d => {
  const f = UI.ipoSt || '', rows = d.bids.bids.filter(b => !f || b.status === f);
  return `<div class="panel"><div class="hd"><h3>Bids</h3><div class="tools">${ipoIssuePick(d, true)}<select class="in sm" id="ipo-st" aria-label="Status"><option value="">Every status</option>${d.bids.statuses.map(s => `<option ${f === s ? 'selected' : ''}>${s}${d.bids.counts[s] ? ' (' + d.bids.counts[s] + ')' : ''}</option>`).join('')}</select></div></div>` +
    tbl([['Bid'], ['Client'], ['Issue'], ['Category'], ['Options'], ['Amount', 'r'], ['Pays by'], ['Status'], ['Exchange'], ['']], rows.map(b => `<tr>
      <td class="n">${esc(b.id)}${b.app_no ? `<div class="dim small">${esc(b.app_no)}</div>` : ''}</td>
      <td><button class="linkish mono" data-act="client" data-id="${esc(b.ucc)}">${esc(b.ucc)}</button> ${esc(b.name || '')}</td>
      <td>${esc(b.issue)}</td><td>${esc(b.category || '—')}</td><td class="small">${ipoOpts(b)}</td><td class="r">${inr(b.amount)}</td>
      <td class="small">${b.mode === 'UPI' || !b.mode ? esc(b.upi || '—') : esc(b.mode)}</td>
      <td>${ipoPill(b.status)}${b.pending ? `<div class="dim small">${esc(b.pending)} to send</div>` : b.sent ? `<div class="dim small">${esc(b.sent)} sent ${ipoTime(b.sent_at)}</div>` : ''}${b.dp_check === 'Mismatch' ? '<div>' + pill('PAN/DP mismatch', 'bad') + '</div>' : ''}</td>
      <td>${esc(b.exchange || '—')}</td><td>${b.issue_id ? `<button class="btn sm" data-act="ipoBidOpen" data-id="${esc(b.id)}">Open</button>` : '<span class="dim small">from file</span>'}</td></tr>`), { tall: true, empty: 'No bids yet.' }) + '</div>';
};

P.ipo_issues = d => `<div class="panel"><div class="hd"><h3>Issues</h3>${ipoMk() ? '<div class="tools"><button class="btn sm" data-act="ipoImport">Import issues (CSV)</button></div>' : ''}</div>` +
  tbl([['Issue'], ['Type'], ['Bidding'], ['Price', 'r'], ['Lot', 'r'], ['T+1 · T+2 · T+3'], ['Status'], ['']], d.is.issues.map(i => `<tr>
    <td><b>${esc(i.name)}</b><div class="dim small">${esc(i.symbol)} · ${esc((i.exchanges || []).join(', '))}${i.registrar ? ' · ' + esc(i.registrar) : ''}${i.isin ? ' · ' + esc(i.isin) : ''}</div></td>
    <td>${esc(i.type)}</td><td class="n small">${fmtDate(i.open)} – ${fmtDate(i.close)}<div class="dim">${esc(i.open_time)}–${esc(i.day_close)}, ${esc(i.close_time)} on T</div></td>
    <td class="r">${i.type === 'NCD' ? '₹' + esc(i.price_low) + ' FV' : `₹${esc(i.price_low)}${num(i.price_high) && i.price_high !== i.price_low ? '–' + esc(i.price_high) : ''}`}${num(i.issue_price) ? `<div class="dim small">final ₹${esc(i.issue_price)}</div>` : ''}</td>
    <td class="r">${nf(i.lot_size)}</td><td class="n small">${fmtDate(i.timeline.allotment)} · ${fmtDate(i.timeline.unblock_by)} · ${fmtDate(i.timeline.listing) || '—'}</td>
    <td>${pill(i.status, ISS_ST[i.status] || 'nt')}</td>
    <td><div class="tools">${ipoMk() ? `<button class="btn sm" data-act="ipoIssueEdit" data-id="${esc(i.id)}">Edit</button>` : ''}${ipoMk() && i.type !== 'OFS' && i.status === 'Open' ? (i.exchanges || []).map(x => `<button class="btn sm" data-act="ipoFile" data-id="${esc(i.id)}" data-x="${x}">${x} bid file</button>`).join('') : ''}</div></td></tr>`), { empty: 'No issues set up. New issue, or import them.' }) +
  '<div class="pad dim small">Categories, limits and the commission come from the red herring prospectus. SME issues: an individual bids exactly 2 lots, no cut-off, no cancellation or lower revision. Offers for sale: non-retail bids on T, retail on T+1, 100% cash margin from the client’s ledger.</div></div>';

P.ipo_mandates = d => (d.mandates.length ? d.mandates.map(m => {
  const i = m.issue, al = m.alerts || {};
  return `<div class="panel"><div class="hd"><h3>${esc(i.name)} ${pill(i.status, ISS_ST[i.status] || 'nt')}</h3><span class="dim small">Mandates accepted until ${esc(fmtDate(m.cutoff.slice(0, 10)))} ${esc(m.cutoff.slice(11))}</span></div>
    <div class="bd"><div class="kv"><div><small>Accepted</small><b>${m.accepted}</b></div><div><small>Pending</small><b class="${m.pending.length ? 'warn' : ''}">${m.pending.length}</b></div><div><small>Declined or failed</small><b>${m.failed.length}</b></div><div><small>Not yet sent</small><b>${m.unsent}</b></div><div><small>PAN/DP mismatch</small><b>${m.dp_mismatch}</b></div>
    <div><small>4 pm reminder</small><b>${al['close:' + i.close] ? ipoTime(al['close:' + i.close]) : 'not yet'}</b></div><div><small>5 pm cut-off</small><b>${al['cutoff:' + i.close] ? ipoTime(al['cutoff:' + i.close]) : 'not yet'}</b></div></div></div>` +
    tbl([['Bid'], ['Client'], ['Amount', 'r'], ['UPI ID'], ['Status'], ['Since']], m.pending.map(b => `<tr><td class="n"><button class="linkish mono" data-act="ipoBidOpen" data-id="${esc(b.id)}">${esc(b.id)}</button></td><td>${esc(b.ucc)} ${esc(b.name)}</td><td class="r">${inr(b.amount)}</td><td class="small">${esc(b.upi)}</td><td>${ipoPill(b.status)}</td><td class="small">${ipoTime(b.sent_at || b.updated)}</td></tr>`), { empty: 'No mandate waiting.' }) +
    (m.failed.length ? `<div class="pad"><b>Declined or failed</b> <span class="dim small">${m.resend_open ? 'can be sent again once now (after the day’s close)' : 'can be sent again once, after the day’s bidding closes, before the last day'}</span></div>` +
      tbl([['Bid'], ['Client'], ['Why'], ['']], m.failed.map(b => `<tr><td class="n">${esc(b.id)}</td><td>${esc(b.ucc)} ${esc(b.name)}</td><td class="small wrap">${esc(b.mandate_reason || b.status)}</td><td>${m.resend_open && !b.reinitiated && ipoCanBid() ? `<button class="btn sm" data-act="ipoResend" data-id="${esc(b.id)}">Send again</button>` : b.reinitiated ? '<span class="dim small">sent again</span>' : ''}</td></tr>`)) : '') + '</div>';
}).join('') : '<div class="panel"><div class="bd dim">No issue is bidding or just closed.</div></div>');

P.ipo_allot = d => {
  const r = d.recon, i = r && !r.error ? r.issue : null;
  const head = `<div class="panel"><div class="hd"><h3>Allotment, release and demat credit</h3><div class="tools">${ipoIssuePick(d)}</div></div>`;
  if (!r) return head + '<div class="bd dim">Set up an issue first.</div></div>';
  if (r.error) return head + `<div class="bd">${errHTML({ message: r.error })}</div></div>`;
  const cm = r.commission || {}, ofs = i.type === 'OFS';
  const tools = ipoMk() ? `<div class="tools">${ofs ? '<button class="btn sm pri" data-act="ipoOFS">Record allocation</button>' : '<button class="btn sm pri" data-act="ipoAllot">Enter allotment</button><button class="btn sm" data-act="ipoAllotFile">Load allotment file</button>'}${!ofs ? '<button class="btn sm" data-act="ipoCredits">Check demat credits</button><button class="btn sm" data-act="ipoPostAllot">Post as purchases</button>' : ''}</div>` : '';
  const ub = x => x.unblock === 'Not needed' ? '<span class="dim small">—</span>' : pill(x.unblock, { Done: 'good', Due: 'acc', 'Done late': 'warn', Late: 'bad' }[x.unblock]) + (x.late_days ? `<div class="small">${x.late_days} day(s) late · ₹${nf(x.compensation, 2)}${x.comp ? ' · ' + esc(x.comp.status) : ''}</div>` : '') + (x.unblocked_on ? `<div class="dim small">${fmtDate(x.unblocked_on)}</div>` : '');
  const cr = x => pill(x.credit, { Credited: 'good', Due: 'acc', Late: 'bad', Short: 'bad', 'ISIN needed': 'warn' }[x.credit] || 'nt') + (x.credit_source ? `<div class="dim small wrap">${esc(x.credit_source)}</div>` : '');
  return head + `<div class="bd"><div class="kv"><div><small>Allotment (T+1)</small><b>${fmtDate(i.timeline.allotment)}</b></div><div><small>Release and credit by (T+2)</small><b>${fmtDate(i.timeline.unblock_by)}</b></div><div><small>Listing (T+3)</small><b>${fmtDate(i.timeline.listing) || '—'}</b></div>
    <div><small>Allotted</small><b>${r.allotted} of ${r.bids} · ${nf(r.shares)} shares</b></div><div><small>Credited</small><b>${r.credited}${r.credit_late ? ` · <span class="neg">${r.credit_late} late</span>` : ''}</b></div><div><small>Released late</small><b class="${r.unblock_late ? 'neg' : ''}">${r.unblock_late}${num(r.compensation_due) ? ' · ₹' + nf(r.compensation_due, 2) + ' due' : ''}</b></div><div><small>Posted as purchases</small><b>${r.posted}</b></div></div>${tools}</div>` +
    tbl([['Bid'], ['Client'], ['Status'], ['Blocked', 'r'], ['Allotted'], ['Value', 'r'], ['To release', 'r'], ['Release'], ['Demat credit'], ['']], r.rows.map(x => `<tr>
      <td class="n">${esc(x.bid)}</td><td>${esc(x.ucc)} ${esc(x.name)}</td><td>${ipoPill(x.status)}${(x.problems || []).map(p => `<div class="small neg">${esc(p)}</div>`).join('')}${x.obligation ? `<div class="dim small wrap">${esc(x.obligation)}</div>` : ''}</td>
      <td class="r">${inr(x.amount)}</td><td class="n">${x.allotted_lots ? `${x.allotted_lots} lot(s) · ${nf(x.shares)}` : x.shares ? nf(x.shares) : '—'}</td><td class="r">${num(x.allotted_value) ? inr(x.allotted_value) : '—'}</td><td class="r">${num(x.to_release) ? inr(x.to_release) : '—'}</td>
      <td>${ub(x)}</td><td>${cr(x)}</td><td><div class="tools">${ipoMk() && num(x.to_release) && !x.unblocked_on ? `<button class="btn sm" data-act="ipoUnblock" data-id="${esc(x.bid)}">Released</button>` : ''}${num(x.compensation) && has('Ops checker', 'Compliance officer') && (!x.comp || x.comp.status === 'Claimed') ? `<button class="btn sm" data-act="ipoComp" data-id="${esc(x.bid)}" data-amt="${esc(x.compensation)}">Compensation</button>` : ''}</div></td></tr>`), { tall: true, empty: 'No bids in this issue.' }) +
    (!ofs ? `<div class="pad"><b>Commission from the issuer</b> <span class="dim small">${num(i.commission_per_app) ? '₹' + esc(i.commission_per_app) + ' an application' : ''}${num(i.commission_pct) ? ' · ' + esc(i.commission_pct) + '% of the value allotted' : ''} · on ${esc(i.commission_on || 'Allotted')} applications</span>
      <div class="kv" style="margin-top:8px"><div><small>Applications</small><b>${cm.applications || 0}</b></div><div><small>Commission</small><b>₹${nf(cm.amount, 2)}</b></div><div><small>GST</small><b>₹${nf(cm.gst, 2)}</b></div><div><small>Total due</small><b>₹${nf(cm.total, 2)}</b></div><div><small>Claimed</small><b>${cm.claimed_on ? fmtDate(cm.claimed_on) + (cm.ref ? ' · ' + esc(cm.ref) : '') : 'not yet'}</b></div><div><small>Received</small><b>₹${nf(cm.received, 2)}${num(cm.tds) ? ' + TDS ₹' + nf(cm.tds, 2) : ''}</b></div></div>
      ${has('Ops checker') ? `<div class="tools" style="margin-top:8px">${!cm.claimed_on && num(cm.amount) ? '<button class="btn sm" data-act="ipoClaim">Claim commission</button>' : ''}${cm.claimed_on && num(cm.total) > num(cm.received) + num(cm.tds) ? '<button class="btn sm" data-act="ipoCommRcv">Commission received</button>' : ''}</div>` : ''}</div>` : '') + '</div>';
};

P.ipo_files = d => {
  const e = d.eipo;
  return `<div class="grid2"><div class="panel"><div class="hd"><h3>Exchange files</h3>${ipoMk() ? '<div class="tools"><button class="btn sm pri" data-act="ipoResp">Load a response file</button></div>' : ''}</div><div class="bd">
      <div class="note warn"><b>Provisional layouts.</b> The bid upload and response layouts (NSE eIPO, BSE iBBS) are not public; Ledgerline’s are provisional and marked so on Exchange files. Before the first live upload, correct them from eIPO (Misc &gt; File Format; NSE circular NSE/IPO/58442 Annexure 2) and BSE notice 20230110-44 Annexure A.</div>
      <p class="dim small">Bid files are written from the Issues tab during bidding hours, one per issue and exchange. eIPO answers each upload with a success file and an error file; load them here or on Exchange files, and the bids move on.</p></div></div>
    <div class="panel"><div class="hd"><h3>NSE eIPO API</h3>${e.set ? pill(e.usable ? (e.send ? 'Sending' : 'Ready, sending off') : 'Needs NSE’s document', e.usable ? 'good' : 'warn') : pill('Not set up', 'nt')}</div><div class="bd">
      ${e.needs ? `<div class="note warn">${esc(e.needs)}</div>` : ''}
      <div class="kv" style="margin-top:8px"><div><small>Callback address to give NSE</small><b class="mono small">${esc(location.origin + e.callback)}</b></div><div><small>Callback token</small><b>${e.callback_token_set ? 'set' : 'not set'}</b></div><div><small>Last send</small><b>${ipoTime(e.state.last_send)}</b></div><div><small>Last inquiry</small><b>${ipoTime(e.state.last_inquiry)}</b></div><div><small>Callbacks</small><b>${e.state.callbacks || 0}${e.state.last_callback ? ' · last ' + ipoTime(e.state.last_callback) : ''}</b></div></div>
      ${e.state.last_error ? `<div class="note bad" style="margin-top:8px">${esc(e.state.last_error)}</div>` : ''}
      <p class="dim small">Set it up in Settings → Connections (NSE eIPO API). Access: email msm@nse.co.in with the member code and user ID (API access and login without OTP), and the callback address to whitelist.</p>
      ${e.usable && ipoMk() ? `<div class="tools">${d.is.issues.filter(i => i.status === 'Open' && i.exchanges.includes('NSE') && i.type !== 'OFS').map(i => `<button class="btn sm" data-act="ipoApi" data-id="${esc(i.id)}">Send ${esc(i.symbol)} by API</button><button class="btn sm" data-act="ipoInq" data-id="${esc(i.id)}">Ask status</button>`).join('')}</div>` : ''}</div></div></div>
    <div class="panel"><div class="hd"><h3>Sent to the exchanges</h3><div class="tools">${ipoIssuePick(d, true)}</div></div>` +
    tbl([['Batch'], ['Issue'], ['Exchange'], ['Via'], ['Bids', 'r'], ['Lines', 'r'], ['When'], ['By'], ['']], d.batches.map(b => `<tr><td class="n">${esc(b.id)}</td><td>${esc((ipoIssueOf(b.issue) || {}).name || b.issue)}</td><td>${esc(b.exchange)}</td><td>${esc(b.via)}</td><td class="r">${Object.keys(b.bids || {}).length}</td><td class="r">${b.lines}</td><td class="small">${ipoTime(b.created)}</td><td class="dim">${esc(b.by)}</td><td>${b.file && ipoMk() ? `<button class="btn sm" data-act="dl" data-id="/v1/ipo/batches/${esc(b.id)}/file" data-name="${esc(b.name)}">${esc(b.name)}</button>` : esc(b.note || '')}</td></tr>`), { empty: 'Nothing sent yet.' }) + '</div>';
};

P.ipo_log = d => `<div class="panel"><div class="hd"><h3>Consent log</h3><div class="tools"><input class="in sm" type="date" id="ipo-from" value="${esc(UI.ipoFrom || '')}" aria-label="From"><input class="in sm" type="date" id="ipo-to" value="${esc(UI.ipoTo || '')}" aria-label="To">${has('Ops maker', 'Ops checker', 'Compliance officer') ? `<button class="btn sm" data-act="dl" data-id="/v1/ipo/instructions.csv?from=${ipoEnc(UI.ipoFrom || '')}&to=${ipoEnc(UI.ipoTo || '')}" data-name="ipo_consent_log.csv">Download</button>` : ''}</div></div>` +
  tbl([['Recorded'], ['Client gave it'], ['Client'], ['Bid'], ['Action'], ['How'], ['Instruction'], ['By'], ['Acknowledgement']], d.log.map(x => `<tr><td class="small">${ipoTime(x.at)}</td><td class="small">${ipoTime(x.received)}</td><td>${esc(x.ucc)} ${esc(x.name || '')}</td><td class="n">${esc(x.bid)}</td><td>${esc(x.action)}</td><td class="small">${esc(x.channel)}<div class="dim">${esc(x.ref)}</div></td><td class="small wrap">${esc(x.text)}</td><td class="dim">${esc(x.by)}</td><td class="n">${esc(x.ack)}</td></tr>`), { tall: true, empty: 'No instructions in this period.' }) +
  '<div class="pad dim small">Every instruction is kept as recorded, with the acknowledgement sent to the client. Records are never deleted (at least 3 years: NSE circular 65/2025).</div></div>';

P.ipo_mf = d => `<div class="panel"><div class="hd"><h3>Applications loaded from the exchange platform (IPO-BIDS)</h3><div class="tools">${S.me?.dev && d.legacy.some(x => x.allotment === 'Awaiting' && !x.issue_id) ? '<button class="btn sm" data-act="allotSample">Load sample allotment file</button>' : ''}${has('Ops maker', 'Ops checker') && d.legacy.some(x => x.allotted_lots > 0 && !x.issue_id) ? '<button class="btn sm" data-act="ipoPost">Post allotments as purchases</button>' : ''}</div></div>` +
  tbl([['Application'], ['Client'], ['Issue'], ['Lots', 'r'], ['Amount', 'r'], ['UPI ID'], ['Status'], ['Allotment']], d.legacy.filter(x => !x.issue_id).map(x => `<tr><td class="n">${esc(x.id)}</td><td>${esc(x.ucc)} ${esc(x.name || '')}</td><td>${esc(x.issue)}</td><td class="r">${x.lots}</td><td class="r">${inr(x.amount)}</td><td class="n dim">${esc(x.upi)}</td><td>${ipoPill(x.status)}</td><td class="small">${esc(x.allotment)}</td></tr>`), { empty: 'None. Upload IPO-BIDS on Other uploads for bids placed outside Ledgerline.' }) + '</div>' +
  `<div class="panel"><div class="hd"><h3>Mutual fund orders</h3></div>${tbl([['Order'], ['Client'], ['Scheme'], ['Type'], ['Amount', 'r'], ['Platform'], ['Status']], d.mf.map(x => `<tr><td class="n">${esc(x.id)}</td><td>${esc(x.ucc)} ${esc(x.name || '')}</td><td>${esc(x.scheme)}</td><td>${esc(x.type)}</td><td class="r">${inr(x.amount)}</td><td>${esc(x.platform)}</td><td>${pill(x.status, x.status === 'Allotted' ? 'good' : x.status === 'Payment pending' ? 'warn' : 'acc')}</td></tr>`), { empty: 'No mutual fund orders.' })}</div>`;

document.addEventListener('change', e => {
  if (S.view !== 'ipo') return;
  const id = e.target.id;
  if (id === 'ipo-iss') { UI.ipoIssue = e.target.value; render({ keep: true }); }
  if (id === 'ipo-st') { UI.ipoSt = e.target.value; $('page').innerHTML = groupBar(S.view) + V.ipo.html(C.ipo); }
  if (id === 'ipo-from' || id === 'ipo-to') { UI[id === 'ipo-from' ? 'ipoFrom' : 'ipoTo'] = e.target.value; render({ keep: true }); }
});

/* ---------- forms ---------- */
function ipoInstrFields(p) {
  return `<div class="fld"><label for="${p}-ch">How the client instructed</label><select class="in" id="${p}-ch">${(C.ipo?.bids?.channels || []).map(c => `<option>${esc(c)}</option>`).join('')}</select></div>
    <div class="fld"><label for="${p}-ref">Reference (call recording, email, form no.)</label><input class="in" id="${p}-ref" required></div>
    <div class="fld"><label for="${p}-rcv">When the client gave it</label><input class="in" type="datetime-local" id="${p}-rcv" value="${ipoNow()}"></div>
    <div class="fld"><label for="${p}-txt">What the client said (optional)</label><input class="in" id="${p}-txt"></div>`;
}
const ipoInstr = p => ({ channel: $(p + '-ch').value, ref: $(p + '-ref').value.trim(), received: $(p + '-rcv').value, text: $(p + '-txt').value.trim() });
function ipoOptRows(iss, ops) {
  ops = ops && ops.length ? ops : [{ lots: '', price: '', cut_off: false }];
  const ncd = iss && iss.type === 'NCD', rows = [0, 1, 2].map(n => {
    const o = ops[n] || {};
    return `<tr class="${n && !ops[n] ? 'ipo-more' : ''}" ${n && !ops[n] ? 'hidden' : ''}><td>${n + 1}</td><td><input class="in sm mono ipo-lots" inputmode="numeric" value="${esc(o.lots || '')}"></td>${ncd ? `<td><select class="in sm ipo-ser">${(iss.series || ['']).map(s => `<option ${o.series === s ? 'selected' : ''}>${esc(s)}</option>`).join('')}</select></td>` : `<td><input class="in sm mono ipo-px" inputmode="decimal" value="${o.cut_off ? '' : esc(o.price || '')}"></td><td><label class="check"><input type="checkbox" class="ipo-cut" ${o.cut_off ? 'checked' : ''}> cut-off</label></td>`}</tr>`;
  }).join('');
  return `<table class="tbl"><thead><tr><th>#</th><th>Lots</th>${ncd ? '<th>Series</th>' : '<th>Price (₹)</th><th></th>'}</tr></thead><tbody>${rows}</tbody></table>${iss && iss.type !== 'OFS' && !ncd ? '<button class="linkish small" type="button" data-act="ipoMoreOpt">Add an option (up to three)</button>' : ''}`;
}
function ipoReadOpts(root) {
  return [...root.querySelectorAll('tbody tr')].filter(tr => !tr.hidden && tr.querySelector('.ipo-lots').value.trim()).map(tr => {
    const cut = tr.querySelector('.ipo-cut')?.checked || false, px = tr.querySelector('.ipo-px')?.value.trim() || '';
    return { lots: parseInt(tr.querySelector('.ipo-lots').value, 10) || 0, cut_off: cut, price: cut || !px ? '0' : px, series: tr.querySelector('.ipo-ser')?.value || '' };
  });
}

Object.assign(A, {
  ipoTab: id => { UI.ipoTab = id; render({ keep: true }); },
  ipoMoreOpt: () => { const tr = document.querySelector('.md tr.ipo-more[hidden]'); if (tr) tr.hidden = false; },
  ipoBidNew: () => {
    const open = C.ipo.is.issues.filter(i => ['Open', 'Upcoming'].includes(i.status));
    const iss = open.find(i => i.id === UI.ipoIssue) || open[0];
    modal(mhd('New bid', 'On the client’s prior instruction. The client gets an acknowledgement; UPI bids need the client’s own UPI ID, approved in their UPI app by 5 pm on the closing day.') + `<form class="bd" id="ipo-bf"><div class="form">
      <div class="fld"><label for="ib-iss">Issue</label><select class="in" id="ib-iss">${open.map(i => `<option value="${esc(i.id)}" ${i === iss ? 'selected' : ''}>${esc(i.name)} · ${esc(i.type)} · ${esc(i.status)}</option>`).join('')}</select></div>
      <div class="fld"><label for="ib-ucc">Client (UCC)</label><input class="in mono" id="ib-ucc" required autocomplete="off"></div>
      <div class="fld"><label for="ib-cat">Category</label><select class="in" id="ib-cat"></select></div>
      <div class="fld"><label for="ib-x">Exchange</label><select class="in" id="ib-x"></select></div>
      <div class="full" id="ib-opts"></div>
      <div class="fld"><label for="ib-upi">Client’s UPI ID</label><input class="in mono" id="ib-upi" list="ib-upis" autocomplete="off"><datalist id="ib-upis"></datalist><span class="dim small" id="ib-upinote">Above ₹5 lakh, and for non-individuals, the bid is blocked in the client’s own bank account (ASBA) instead.</span></div>
      <div class="fld" id="ib-decl-f" hidden><label class="check"><input type="checkbox" id="ib-decl"> <span id="ib-decl-t"></span></label></div>
      <div class="full"><b>The client’s instruction</b></div>${ipoInstrFields('ib')}
    </div><div id="ib-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Take the bid</button></div></form>`);
    const fill = () => {
      const i = C.ipo.is.issues.find(x => x.id === $('ib-iss').value); if (!i) return;
      $('ib-cat').innerHTML = i.categories.map(c => `<option value="${esc(c.code)}">${esc(c.code)} · ${esc(c.name)}</option>`).join('');
      $('ib-x').innerHTML = i.exchanges.map(x => `<option>${x}</option>`).join('');
      $('ib-opts').innerHTML = ipoOptRows(i);
      const decl = () => { const c = i.categories.find(c => c.code === $('ib-cat').value) || {}; $('ib-decl-f').hidden = !c.declare || !!c.eligible_isin; $('ib-decl-t').textContent = 'The client declared they are ' + (c.declare || ''); };
      $('ib-cat').onchange = decl; decl();
    };
    $('ib-iss').onchange = fill; fill();
    $('ib-ucc').onchange = async () => { const u = $('ib-ucc').value.trim().toUpperCase(); if (!u) return; try { const ids = await get(`/v1/ipo/upi/${ipoEnc(u)}`); $('ib-upis').innerHTML = ids.map(x => `<option value="${esc(x.vpa)}">${esc(x.source)}</option>`).join(''); if (ids.length && !$('ib-upi').value) $('ib-upi').value = ids[0].vpa; $('ib-upinote').textContent = ids.length ? 'On record: ' + ids.map(x => x.vpa).join(', ') : 'No UPI ID on record for this client: record one first (the bid modal of an earlier bid, or Clients’ UPI IDs below).'; } catch (e) { $('ib-upinote').textContent = e.message; } };
    $('ipo-bf').onsubmit = async e => {
      e.preventDefault();
      try {
        const b = await post('/v1/ipo/bids', { issue: $('ib-iss').value, ucc: $('ib-ucc').value.trim().toUpperCase(), category: $('ib-cat').value, exchange: $('ib-x').value, options: ipoReadOpts($('ib-opts')), upi: $('ib-upi').value.trim(), declared: $('ib-decl').checked, own_upi: false, instruction: ipoInstr('ib') });
        closeModal(); toast(`${b.id} taken (acknowledgement ${b.ack})`); render({ keep: true });
      } catch (err) { $('ib-err').innerHTML = errHTML(err); }
    };
  },
  ipoBidOpen: async id => {
    let d; try { d = await get(`/v1/ipo/bids/${ipoEnc(id)}`); } catch (e) { toast(e.message, true); return; }
    const b = d.bid, i = d.issue || {}, live = ['Draft', 'Uploaded', 'Mandate pending', 'Accepted', 'Declined', 'Failed'].includes(b.status) && b.pending !== 'Cancel' && b.sent !== 'Cancel';
    const cat = (i.categories || []).find(c => c.code === b.category) || {};
    modal(mhd(`${esc(b.id)} · ${esc(b.issue)}`, `${esc(b.ucc)} ${esc(b.name)} · ${esc(b.category)} on ${esc(b.exchange)} · ${b.mode === 'UPI' ? 'UPI ' + esc(b.upi) : esc(b.mode)}`) + `<div class="bd">
      <div class="kv"><div><small>Status</small><b>${ipoPill(b.status)}</b></div><div><small>Amount</small><b>${inr(b.amount, 2)}</b></div><div><small>Options</small><b class="small">${ipoOpts(b)}</b></div><div><small>Application no.</small><b class="mono">${esc(b.app_no || '—')}</b></div>
      <div><small>Mandate</small><b>${esc(b.mandate || '—')}${b.mandate_reason ? `<span class="dim small"> · ${esc(b.mandate_reason)}</span>` : ''}</b></div><div><small>Depository check</small><b>${esc(b.dp_check || '—')}</b></div><div><small>PAN · demat</small><b class="mono small">${esc(b.pan)} · ${esc(b.depository)} ${esc(b.dp_id)}${esc(b.ben_id)}</b></div><div><small>Waiting to send</small><b>${esc(b.pending || b.sent ? (b.pending || b.sent + ' (sent)') : '—')}</b></div>
      ${b.margin && num(b.margin) ? `<div><small>Margin held</small><b>${inr(b.margin, 2)}${b.margin_entry ? '' : ' (released)'}</b></div>` : ''}${b.exch_reason ? `<div><small>Exchange said</small><b class="small">${esc(b.exch_reason)}</b></div>` : ''}${(b.tickets || []).length ? `<div><small>Complaints</small><b>${b.tickets.map(esc).join(', ')}</b></div>` : ''}</div>
      <h4 style="margin:14px 0 6px">Instructions</h4>${tbl([['When'], ['Action'], ['How'], ['Ack']], d.instructions.map(x => `<tr><td class="small">${ipoTime(x.received)}</td><td>${esc(x.action)}</td><td class="small">${esc(x.channel)} · ${esc(x.ref)}</td><td class="n">${esc(x.ack)}</td></tr>`))}
      <h4 style="margin:14px 0 6px">History</h4><div class="small">${(b.history || []).slice().reverse().map(h => `<div><span class="dim">${ipoTime(h.at)} · ${esc(h.by)}</span> ${esc(h.what)}</div>`).join('') || '—'}</div>
      <div id="ipb-form"></div><div id="ipb-err"></div></div>
      <div class="ft"><button class="btn" data-act="close">Close</button>${live && ipoCanBid() ? `<button class="btn" data-act="ipoModify" data-id="${esc(b.id)}">Modify</button>${cat.modify === 'any' || b.status === 'Draft' && !b.sent ? `<button class="btn bad" data-act="ipoCancel" data-id="${esc(b.id)}">Cancel bid</button>` : ''}` : ''}${ipoMk() && b.issue_id ? `<button class="btn" data-act="ipoRecord" data-id="${esc(b.id)}">Record platform status</button>` : ''}${ipoCanBid() ? `<button class="btn" data-act="ipoComplain" data-id="${esc(b.id)}">Complaint</button>` : ''}</div>`);
    C.ipoBid = d;
  },
  ipoModify: id => {
    const d = C.ipoBid, b = d.bid;
    $('ipb-form').innerHTML = `<h4 style="margin:14px 0 6px">Modify</h4><form id="ipb-mf"><div class="form"><div class="full" id="im-opts">${ipoOptRows(d.issue, b.options)}</div>${b.mode === 'UPI' ? `<div class="fld"><label for="im-upi">UPI ID</label><input class="in mono" id="im-upi" value="${esc(b.upi)}" list="im-upis"><datalist id="im-upis">${d.upis.map(x => `<option value="${esc(x.vpa)}">`).join('')}</datalist></div>` : ''}${ipoInstrFields('im')}</div><div class="tools" style="margin-top:8px"><button class="btn pri" type="submit">Save the revision</button></div></form>`;
    $('ipb-mf').onsubmit = async e => { e.preventDefault(); try { const r = await post(`/v1/ipo/bids/${ipoEnc(id)}/modify`, { options: ipoReadOpts($('im-opts')), upi: $('im-upi')?.value.trim() || '', own_upi: false, instruction: ipoInstr('im') }); closeModal(); toast(`${r.id} revised (acknowledgement ${r.ack})`); render({ keep: true }); } catch (err) { $('ipb-err').innerHTML = errHTML(err); } };
  },
  ipoCancel: id => {
    $('ipb-form').innerHTML = `<h4 style="margin:14px 0 6px">Cancel the bid</h4><form id="ipb-cf"><div class="form">${ipoInstrFields('ic')}</div><div class="tools" style="margin-top:8px"><button class="btn bad" type="submit">Cancel on the client’s instruction</button></div></form>`;
    $('ipb-cf').onsubmit = async e => { e.preventDefault(); try { const r = await post(`/v1/ipo/bids/${ipoEnc(id)}/cancel`, ipoInstr('ic')); closeModal(); toast(r.status === 'Cancelled' ? `${r.id} cancelled` : `${r.id}: cancellation goes with the next file`); render({ keep: true }); } catch (err) { $('ipb-err').innerHTML = errHTML(err); } };
  },
  ipoResend: id => {
    modal(mhd('Send the UPI mandate again', 'Once in the issue, after the day’s bidding closes, on the client’s request. Then resend it on the platform (bidding inquiry → resend UPI).') + `<form class="bd" id="ipo-rf"><div class="form">${ipoInstrFields('ir')}</div><div id="ir-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Record</button></div></form>`);
    $('ipo-rf').onsubmit = async e => { e.preventDefault(); try { await post(`/v1/ipo/bids/${ipoEnc(id)}/resend-mandate`, ipoInstr('ir')); closeModal(); toast('Mandate sent again'); render({ keep: true }); } catch (err) { $('ir-err').innerHTML = errHTML(err); } };
  },
  ipoRecord: id => {
    $('ipb-form').innerHTML = `<h4 style="margin:14px 0 6px">What the platform shows</h4><form id="ipb-rf"><div class="form">
      <div class="fld"><label for="is-st">Bid</label><select class="in" id="is-st"><option value="">(no change)</option><option>Accepted</option><option>Rejected</option><option>Cancelled</option></select></div>
      <div class="fld"><label for="is-app">Application or order number</label><input class="in mono" id="is-app"></div>
      <div class="fld"><label for="is-pay">UPI mandate</label><select class="in" id="is-pay"><option value="">(no change)</option><option value="10">Sent to the client’s UPI app</option><option value="100">Approved: money blocked</option><option value="31">Declined by the client</option><option value="12">Failed</option><option value="110">Blocked money released</option></select></div>
      <div class="fld"><label for="is-dp">Depository check</label><select class="in" id="is-dp"><option value="">(no change)</option><option value="Y">Verified</option><option value="N">Mismatch</option></select></div>
      <div class="fld full"><label for="is-why">Reason shown</label><input class="in" id="is-why"></div></div><div class="tools" style="margin-top:8px"><button class="btn pri" type="submit">Record</button></div></form>`;
    $('ipb-rf').onsubmit = async e => { e.preventDefault(); try { await post(`/v1/ipo/bids/${ipoEnc(id)}/status`, { status: $('is-st').value, app_no: $('is-app').value.trim(), pay_status: $('is-pay').value, dp_status: $('is-dp').value, reason: $('is-why').value.trim() }); closeModal(); toast('Recorded'); render({ keep: true }); } catch (err) { $('ipb-err').innerHTML = errHTML(err); } };
  },
  ipoComplain: id => {
    $('ipb-form').innerHTML = `<h4 style="margin:14px 0 6px">Investor complaint</h4><form id="ipb-tf"><div class="form"><div class="fld"><label for="it-src">Received from</label><select class="in" id="it-src"><option>Directly from investors</option><option>SEBI (SCORES)</option><option>Stock exchanges</option><option>Other sources</option></select></div><div class="fld"><label for="it-ref">SCORES or exchange reference</label><input class="in" id="it-ref"></div><div class="fld full"><label for="it-txt">Complaint</label><textarea class="in" id="it-txt" rows="3" required></textarea></div></div><div class="tools" style="margin-top:8px"><button class="btn pri" type="submit">Open the complaint (21 days)</button></div></form>`;
    $('ipb-tf').onsubmit = async e => { e.preventDefault(); try { const t = await post(`/v1/ipo/bids/${ipoEnc(id)}/complaint`, { source: $('it-src').value, ref: $('it-ref').value.trim(), text: $('it-txt').value.trim() }); closeModal(); toast(`Complaint ${t.id} opened; due ${fmtDate(t.due)}`); } catch (err) { $('ipb-err').innerHTML = errHTML(err); } };
  },
  ipoIssueEdit: id => {
    const i = (id && ipoIssueOf(id)) || { type: 'Mainboard', exchanges: ['NSE', 'BSE'], categories: [] };
    const defs = C.ipo.is.defaults || {}, cats = i.categories.length ? i.categories : defs[i.type] || [];
    const catRow = c => `<tr><td><input class="in sm mono ic-code" value="${esc(c.code || '')}"></td><td><input class="in sm ic-name" value="${esc(c.name || '')}"></td><td><input class="in sm mono ic-above" value="${num(c.above) ? esc(c.above) : ''}"></td><td><input class="in sm mono ic-upto" value="${num(c.upto) ? esc(c.upto) : ''}"></td><td><input class="in sm mono ic-min" value="${num(c.min_amount) ? esc(c.min_amount) : ''}"></td><td><input class="in sm mono ic-minl" value="${c.min_lots || ''}"></td><td><input class="in sm mono ic-exl" value="${c.exact_lots || ''}"></td><td><input type="checkbox" class="ic-cut" ${c.cut_off ? 'checked' : ''}></td><td><input type="checkbox" class="ic-ind" ${c.individual ? 'checked' : ''}></td><td><select class="in sm ic-mod"><option value="any" ${c.modify === 'any' ? 'selected' : ''}>any</option><option value="up" ${c.modify !== 'any' ? 'selected' : ''}>up only</option></select></td><td><input class="in sm mono ic-disc" value="${num(c.discount) ? esc(c.discount) : ''}"></td><td><input class="in sm mono ic-nse" value="${esc((c.exch_code || {}).NSE || '')}"></td><td><input class="in sm mono ic-bse" value="${esc((c.exch_code || {}).BSE || '')}"></td><td><input class="in sm ic-decl" value="${esc(c.declare || '')}"></td><td><input class="in sm mono ic-day" value="${esc(c.day || '')}"></td><td><input class="in sm mono ic-isin" value="${esc(c.eligible_isin || '')}"></td></tr>`;
    modal(mhd(id ? 'Edit ' + esc(i.name) : 'New issue', 'From the red herring prospectus and the exchange’s notice. Amounts in rupees.') + `<form class="bd" id="ipo-if"><div class="form">
      <div class="fld"><label for="ii-sym">Symbol (as on the platform)</label><input class="in mono" id="ii-sym" value="${esc(i.symbol || '')}" required ${id ? 'readonly' : ''}></div>
      <div class="fld"><label for="ii-name">Name</label><input class="in" id="ii-name" value="${esc(i.name || '')}"></div>
      <div class="fld"><label for="ii-co">Company</label><input class="in" id="ii-co" value="${esc(i.company || '')}"></div>
      <div class="fld"><label for="ii-type">Type</label><select class="in" id="ii-type" ${id ? 'disabled' : ''}>${C.ipo.is.types.map(t => `<option ${i.type === t ? 'selected' : ''}>${t}</option>`).join('')}</select></div>
      <div class="fld"><label>Exchanges</label><div class="tools">${['NSE', 'BSE'].map(x => `<label class="check"><input type="checkbox" class="ii-x" value="${x}" ${(i.exchanges || []).includes(x) ? 'checked' : ''}> ${x}</label>`).join('')}</div></div>
      <div class="fld"><label for="ii-isin">ISIN (when known)</label><input class="in mono" id="ii-isin" value="${esc(i.isin || '')}"></div>
      <div class="fld"><label for="ii-open">Opens</label><input class="in" type="date" id="ii-open" value="${esc(i.open || '')}"></div>
      <div class="fld"><label for="ii-close">Closes (T)</label><input class="in" type="date" id="ii-close" value="${esc(i.close || '')}"></div>
      <div class="fld"><label for="ii-pre">Clients may apply from (optional)</label><input class="in" type="date" id="ii-pre" value="${esc(i.pre_apply || '')}"></div>
      <div class="fld"><label for="ii-times">Bidding hours · on T · mandates until</label><div class="tools"><input class="in sm mono" id="ii-ot" value="${esc(i.open_time || '')}" placeholder="10:00" style="width:64px">–<input class="in sm mono" id="ii-dc" value="${esc(i.day_close || '')}" placeholder="17:00" style="width:64px"><input class="in sm mono" id="ii-ct" value="${esc(i.close_time || '')}" placeholder="16:00" style="width:64px"><input class="in sm mono" id="ii-mc" value="${esc(i.mandate_cutoff || '')}" placeholder="17:00" style="width:64px"></div></div>
      <div class="fld"><label for="ii-lo">Price band from (floor; NCD face value)</label><input class="in mono" id="ii-lo" value="${esc(num(i.price_low) ? i.price_low : '')}"></div>
      <div class="fld"><label for="ii-hi">to (cap; OFS: retail cut-off price once known)</label><input class="in mono" id="ii-hi" value="${esc(num(i.price_high) ? i.price_high : '')}"></div>
      <div class="fld"><label for="ii-lot">Lot size</label><input class="in mono" id="ii-lot" inputmode="numeric" value="${esc(i.lot_size || '')}"></div>
      <div class="fld"><label for="ii-fp">Final issue price (after the basis of allotment)</label><input class="in mono" id="ii-fp" value="${esc(num(i.issue_price) ? i.issue_price : '')}"></div>
      <div class="fld"><label for="ii-reg">Registrar</label><input class="in" id="ii-reg" list="ii-regs" value="${esc(i.registrar || '')}"><datalist id="ii-regs"><option>KFin Technologies</option><option>MUFG Intime India</option><option>Bigshare Services</option><option>Cameo Corporate Services</option><option>Skyline Financial Services</option><option>Purva Sharegistry</option></datalist></div>
      <div class="fld"><label for="ii-sb">Sponsor bank</label><input class="in" id="ii-sb" value="${esc(i.sponsor_bank || '')}"></div>
      <div class="fld"><label for="ii-cpa">Commission per application (₹)</label><input class="in mono" id="ii-cpa" value="${esc(num(i.commission_per_app) ? i.commission_per_app : '')}"></div>
      <div class="fld"><label for="ii-cpc">Commission, % of value allotted</label><input class="in mono" id="ii-cpc" value="${esc(num(i.commission_pct) ? i.commission_pct : '')}"></div>
      <div class="fld"><label for="ii-con">Commission paid on</label><select class="in" id="ii-con"><option ${i.commission_on !== 'Valid' ? 'selected' : ''}>Allotted</option><option ${i.commission_on === 'Valid' ? 'selected' : ''}>Valid</option></select></div>
      <div class="fld"><label for="ii-ser">NCD series (comma separated)</label><input class="in mono" id="ii-ser" value="${esc((i.series || []).join(', '))}"></div>
      <div class="fld"><label class="check"><input type="checkbox" id="ii-wd" ${i.withdrawn ? 'checked' : ''}> Withdrawn</label></div>
      <div class="full"><b>Categories</b> <span class="dim small">above / up to / minimum in ₹ (blank: none); platform codes per exchange if they differ; day (OFS): T or T+1</span><div class="tw"><table class="tbl ret" id="ii-cats"><thead><tr><th>Code</th><th>Name</th><th>Above</th><th>Up to</th><th>Min ₹</th><th>Min lots</th><th>Exact lots</th><th>Cut-off</th><th>Individuals</th><th>Revise</th><th>Discount</th><th>NSE code</th><th>BSE code</th><th>Declare</th><th>Day</th><th>Eligible ISIN</th></tr></thead><tbody>${cats.map(catRow).join('')}</tbody></table></div><button class="linkish small" type="button" id="ii-addcat">Add a category</button></div>
    </div><div id="ii-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
    $('ii-addcat').onclick = () => $('ii-cats').querySelector('tbody').insertAdjacentHTML('beforeend', catRow({ modify: 'up' }));
    if (!id) $('ii-type').onchange = e => { $('ii-cats').querySelector('tbody').innerHTML = (defs[e.target.value] || []).map(catRow).join(''); };
    const amt = v => v.trim() || '0';
    $('ipo-if').onsubmit = async e => {
      e.preventDefault();
      const cats = [...$('ii-cats').querySelectorAll('tbody tr')].map(tr => { const g = c => tr.querySelector(c); const ex = {}; if (g('.ic-nse').value.trim()) ex.NSE = g('.ic-nse').value.trim(); if (g('.ic-bse').value.trim()) ex.BSE = g('.ic-bse').value.trim();
        return { code: g('.ic-code').value.trim(), name: g('.ic-name').value.trim(), above: amt(g('.ic-above').value), upto: amt(g('.ic-upto').value), min_amount: amt(g('.ic-min').value), min_lots: parseInt(g('.ic-minl').value, 10) || 0, exact_lots: parseInt(g('.ic-exl').value, 10) || 0, cut_off: g('.ic-cut').checked, individual: g('.ic-ind').checked, modify: g('.ic-mod').value, discount: amt(g('.ic-disc').value), exch_code: ex, declare: g('.ic-decl').value.trim(), day: g('.ic-day').value.trim(), eligible_isin: g('.ic-isin').value.trim() }; }).filter(c => c.code);
      const body = { id: id || '', symbol: $('ii-sym').value.trim(), name: $('ii-name').value.trim(), company: $('ii-co').value.trim(), type: $('ii-type').value, exchanges: [...document.querySelectorAll('.ii-x:checked')].map(x => x.value), isin: $('ii-isin').value.trim(),
        open: $('ii-open').value, close: $('ii-close').value, pre_apply: $('ii-pre').value, open_time: $('ii-ot').value.trim(), day_close: $('ii-dc').value.trim(), close_time: $('ii-ct').value.trim(), mandate_cutoff: $('ii-mc').value.trim(),
        price_low: amt($('ii-lo').value), price_high: amt($('ii-hi').value), lot_size: parseInt($('ii-lot').value, 10) || 0, issue_price: amt($('ii-fp').value), registrar: $('ii-reg').value.trim(), sponsor_bank: $('ii-sb').value.trim(),
        commission_per_app: amt($('ii-cpa').value), commission_pct: amt($('ii-cpc').value), commission_on: $('ii-con').value, series: $('ii-ser').value.split(',').map(s => s.trim()).filter(Boolean), withdrawn: $('ii-wd').checked, categories: cats,
        allotment_date: i.allotment_date || '', listing_date: i.listing_date || '' };
      try { const r = await post('/v1/ipo/issues', body); UI.ipoIssue = r.id; closeModal(); toast(`${r.name} saved`); render({ keep: true }); } catch (err) { $('ii-err').innerHTML = errHTML(err); }
    };
  },
  ipoImport: () => {
    const inp = document.createElement('input'); inp.type = 'file'; inp.accept = '.csv,text/csv';
    inp.onchange = async () => { try { const r = await api('POST', '/v1/ipo/issues/import', await inp.files[0].text(), true); toast(`${r.length} issue(s) imported`); render({ keep: true }); } catch (e) { toast(e.message, true); } };
    inp.click();
  },
  ipoFile: async (id, el) => {
    try { const b = await post(`/v1/ipo/issues/${ipoEnc(id)}/bid-file`, { exchange: el.dataset.x }); toast(`${b.name}: ${Object.keys(b.bids).length} bid(s), ${b.lines} line(s)`); await download(`/v1/ipo/batches/${ipoEnc(b.id)}/file`, b.name); render({ keep: true }); }
    catch (e) { toast(e.message, true); }
  },
  ipoApi: id => act(() => post(`/v1/ipo/issues/${ipoEnc(id)}/eipo-send`), b => `${b.lines} bid(s) sent to NSE · ${b.note || ''}`),
  ipoInq: id => act(() => post(`/v1/ipo/issues/${ipoEnc(id)}/eipo-inquire`), r => `${r.updated} bid(s) updated`),
  ipoResp: () => {
    const inp = document.createElement('input'); inp.type = 'file'; inp.multiple = true;
    inp.onchange = async () => { const out = []; for (const f of inp.files) { try { out.push(...await api('POST', `/v1/ipo/responses?name=${ipoEnc(f.name)}`, f, true)); } catch (e) { out.push({ name: f.name, loaded: e.message }); } }
      toast(out.map(r => `${r.name}: ${r.loaded || r.note || 'not recognised'}${r.added ? ` (${r.added} bid(s))` : ''}`).join(' · '), out.some(r => !r.added)); render({ keep: true }); };
    inp.click();
  },
  ipoAllot: () => {
    const r = C.ipo.recon, rows = r.rows.filter(x => ['Accepted', 'Allotted', 'Not allotted', 'Mandate pending', 'Uploaded', 'Declined', 'Failed'].includes(x.status));
    modal(mhd('Enter the allotment of ' + esc(r.issue.name), 'From the registrar’s basis of allotment. Bids left at 0 are not allotted. Set the final issue price on the issue first.') + `<form class="bd" id="ipo-af"><div class="fld"><label for="ia-d">Allotment date</label><input class="in" type="date" id="ia-d" value="${esc(r.issue.timeline.allotment || S.date)}"></div>` +
      tbl([['Bid'], ['Client'], ['Lots bid', 'r'], ['Allotted lots']], rows.map(x => `<tr><td class="n">${esc(x.bid)}</td><td>${esc(x.ucc)} ${esc(x.name)}</td><td class="r">${x.lots}</td><td><input class="in sm mono ia-l" data-id="${esc(x.bid)}" inputmode="numeric" value="${x.allotted_lots || 0}" style="width:70px" ${x.status === 'Accepted' || x.allotted_lots || x.status === 'Allotted' || x.status === 'Not allotted' ? '' : 'disabled'}></td></tr>`)) +
      `<div id="ia-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Record</button></div></form>`);
    $('ipo-af').onsubmit = async e => { e.preventDefault(); const lots = {}; document.querySelectorAll('.ia-l:not([disabled])').forEach(x => { const n = parseInt(x.value, 10) || 0; if (n) lots[x.dataset.id] = n; });
      try { const res = await post(`/v1/ipo/issues/${ipoEnc(r.issue.id)}/allotment`, { date: $('ia-d').value, lots }); closeModal(); toast(`${res.allotted} allotted, ${res.not_allotted} not allotted`); render({ keep: true }); } catch (err) { $('ia-err').innerHTML = errHTML(err); } };
  },
  ipoAllotFile: () => {
    const r = C.ipo.recon, inp = document.createElement('input'); inp.type = 'file'; inp.accept = '.csv,text/csv';
    inp.onchange = async () => { try { const res = await api('POST', `/v1/ipo/issues/${ipoEnc(r.issue.id)}/allotment-file?date=${ipoEnc(r.issue.timeline.allotment || S.date)}`, await inp.files[0].text(), true); toast(`${res.allotted} allotted, ${res.not_allotted} not allotted`); render({ keep: true }); } catch (e) { toast(e.message, true); } };
    inp.click();
  },
  ipoCredits: () => act(() => post(`/v1/ipo/issues/${ipoEnc(C.ipo.recon.issue.id)}/credits`), r => `${r.credited} bid(s) confirmed credited`),
  ipoPostAllot: () => act(() => post(`/v1/ipo/issues/${ipoEnc(C.ipo.recon.issue.id)}/post-allotments`, { date: '' }), r => `${r.posted} allotment(s) posted (${r.shares} shares)${r.skipped ? ', ' + r.skipped + ' already posted' : ''}`),
  ipoUnblock: id => {
    modal(mhd('Blocked money released', 'From the client, their bank or the sponsor bank’s report.') + `<form class="bd" id="ipo-uf"><div class="form"><div class="fld"><label for="iu-d">Released on</label><input class="in" type="date" id="iu-d" value="${esc(S.date)}"></div><div class="fld"><label for="iu-a">Amount (₹, blank: all due)</label><input class="in mono" id="iu-a"></div><div class="fld full"><label for="iu-n">How it was confirmed</label><input class="in" id="iu-n"></div></div><div id="iu-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Record</button></div></form>`);
    $('ipo-uf').onsubmit = async e => { e.preventDefault(); try { await post(`/v1/ipo/bids/${ipoEnc(id)}/unblock`, { date: $('iu-d').value, amount: $('iu-a').value.trim() || '0', note: $('iu-n').value.trim() }); closeModal(); toast('Recorded'); render({ keep: true }); } catch (err) { $('iu-err').innerHTML = errHTML(err); } };
  },
  ipoComp: (id, el) => {
    modal(mhd('Compensation for late release', `₹100 a day or 15% a year of the bid amount, whichever is higher: ₹${esc(el.dataset.amt)} so far. Payable by the bank responsible; if the firm pays, it is credited to the client’s ledger.`) + `<form class="bd" id="ipo-cf"><div class="form"><div class="fld"><label for="icp-s">Status</label><select class="in" id="icp-s"><option>Claimed</option><option>Paid</option><option>Waived</option></select></div><div class="fld"><label for="icp-p">Paid by</label><select class="in" id="icp-p"><option>SCSB</option><option>Sponsor bank</option><option>Firm</option></select></div><div class="fld"><label for="icp-a">Amount paid (₹)</label><input class="in mono" id="icp-a" value="${esc(el.dataset.amt)}"></div><div class="fld"><label for="icp-r">Reference</label><input class="in" id="icp-r"></div><div class="fld full"><label for="icp-n">Note (why waived)</label><input class="in" id="icp-n"></div></div><div id="icp-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Record</button></div></form>`);
    $('ipo-cf').onsubmit = async e => { e.preventDefault(); const s = $('icp-s').value; try { await post(`/v1/ipo/bids/${ipoEnc(id)}/compensation`, { status: s, party: $('icp-p').value, paid: s === 'Paid' ? $('icp-a').value.trim() : '0', ref: $('icp-r').value.trim(), note: $('icp-n').value.trim() }); closeModal(); toast('Recorded'); render({ keep: true }); } catch (err) { $('icp-err').innerHTML = errHTML(err); } };
  },
  ipoClaim: () => {
    const i = C.ipo.recon.issue;
    confirmModal('Claim the commission', `Posts the commission due from the issuer on ${esc(i.name)} as a receivable, with its income and GST.`, `<div class="form"><div class="fld"><label for="icl-d">Date</label><input class="in" type="date" id="icl-d" value="${esc(S.date)}"></div><div class="fld"><label for="icl-r">Invoice or claim number</label><input class="in" id="icl-r"></div></div>`, 'Claim',
      () => act(() => post(`/v1/ipo/issues/${ipoEnc(i.id)}/commission`, { action: 'claim', date: $('icl-d').value, ref: $('icl-r').value.trim(), received: '0', tds: '0' }), r => `Claimed ₹${nf(r.commission.total, 2)}`));
  },
  ipoCommRcv: () => {
    const i = C.ipo.recon.issue;
    confirmModal('Commission received', 'Into the firm’s own bank account, with any TDS (section 194H) the payer deducted.', `<div class="form"><div class="fld"><label for="icr-d">Date</label><input class="in" type="date" id="icr-d" value="${esc(S.date)}"></div><div class="fld"><label for="icr-a">Received (₹)</label><input class="in mono" id="icr-a"></div><div class="fld"><label for="icr-t">TDS deducted (₹)</label><input class="in mono" id="icr-t" value="0"></div><div class="fld"><label for="icr-r">Bank reference</label><input class="in" id="icr-r"></div></div>`, 'Record',
      () => act(() => post(`/v1/ipo/issues/${ipoEnc(i.id)}/commission`, { action: 'received', date: $('icr-d').value, received: $('icr-a').value.trim() || '0', tds: $('icr-t').value.trim() || '0', ref: $('icr-r').value.trim() }), 'Recorded'));
  },
  ipoOFS: () => {
    const r = C.ipo.recon, rows = r.rows.filter(x => ['Accepted', 'Draft', 'Uploaded'].includes(x.status));
    modal(mhd('Record the allocation of ' + esc(r.issue.name), 'Margins come back to the clients’ ledgers. Allotments the day’s trade file carries are billed from it; tick the box to add the others to the day’s exchange trades so the day’s billing and contract notes include them.') + `<form class="bd" id="ipo-of"><div class="fld"><label for="io-d">Allocation date</label><input class="in" type="date" id="io-d" value="${esc(r.issue.close)}"></div>` +
      tbl([['Bid'], ['Client'], ['Shares bid', 'r'], ['Allotted shares'], ['Price (₹)']], rows.map(x => `<tr><td class="n">${esc(x.bid)}</td><td>${esc(x.ucc)} ${esc(x.name)}</td><td class="r">${nf(x.lots * r.issue.lot_size)}</td><td><input class="in sm mono io-q" data-id="${esc(x.bid)}" inputmode="numeric" value="0" style="width:90px"></td><td><input class="in sm mono io-p" data-id="${esc(x.bid)}" value="${esc(r.issue.price_high || r.issue.price_low)}" style="width:90px"></td></tr>`)) +
      `<label class="check" style="margin-top:8px"><input type="checkbox" id="io-add" checked> Add allotments the trade file does not carry to the day’s exchange trades</label><div id="io-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Record</button></div></form>`);
    $('ipo-of').onsubmit = async e => { e.preventDefault(); const al = [...document.querySelectorAll('.io-q')].map(q => ({ bid: q.dataset.id, qty: parseInt(q.value, 10) || 0, price: document.querySelector(`.io-p[data-id="${q.dataset.id}"]`).value.trim() || '0' }));
      try { const res = await post(`/v1/ipo/issues/${ipoEnc(r.issue.id)}/ofs-allocation`, { date: $('io-d').value, allocations: al, add_trades: $('io-add').checked }); closeModal(); toast(`${res.allotted} allotted, ${res.margins_released} margin(s) released${(res.notes || []).length ? ' · ' + res.notes.join(' · ') : ''}`); render({ keep: true }); } catch (err) { $('io-err').innerHTML = errHTML(err); } };
  },
  // Older applications loaded from IPO-BIDS: post their allotments by issue.
  ipoPost: () => {
    const issues = [...new Set((C.ipo.legacy || []).filter(x => x.allotted_lots > 0 && !x.issue_id).map(x => x.issue))];
    confirmModal('Post IPO allotments', 'Records the allotted shares as purchases at the issue price on the allotment date, for holdings cost and capital gains.', `<div class="form"><div class="fld"><label for="ip-i">Issue</label><select class="in" id="ip-i">${issues.map(i => `<option>${esc(i)}</option>`).join('')}</select></div><div class="fld"><label for="ip-isin">ISIN</label><input class="in mono" id="ip-isin"></div><div class="fld"><label for="ip-s">Symbol</label><input class="in mono" id="ip-s"></div><div class="fld"><label for="ip-p">Issue price (₹)</label><input class="in" id="ip-p"></div><div class="fld"><label for="ip-l">Shares per lot</label><input class="in" id="ip-l" type="number" min="1"></div><div class="fld"><label for="ip-d">Allotment date</label><input class="in" type="date" id="ip-d" value="${esc(S.date)}"></div></div>`, 'Post',
      () => act(() => post('/v1/ipo/allotments', { issue: $('ip-i').value, isin: $('ip-isin').value, symbol: $('ip-s').value, price: $('ip-p').value.trim(), lot_size: parseInt($('ip-l').value, 10) || 0, date: $('ip-d').value }), r => `${r.posted} allotments posted (${r.shares} shares)${r.skipped ? ', ' + r.skipped + ' already posted' : ''}`));
  },
});

/* ---------- client portal: apply for public issues ---------- */
V.pipo = {
  load: async () => { const v = await portalLoad(); v.ipo = await get(`/v1/portal/ipo?ucc=${ipoEnc(v.account.ucc)}`); return v; },
  html: v => {
    const d = v.ipo;
    const card = i => `<div class="panel"><div class="hd"><h3>${esc(i.name)} ${pill(i.status, ISS_ST[i.status] || 'nt')}</h3><span class="dim small">${esc(i.type)} · ${esc(i.exchanges.join(', '))}</span></div><div class="bd">
      <div class="kv"><div><small>Bidding</small><b>${fmtDate(i.open)} – ${fmtDate(i.close)}</b></div><div><small>Closes</small><b>${esc(i.close_time)} on ${fmtDate(i.close)}</b></div><div><small>Price</small><b>₹${esc(i.price_low)}${i.price_high !== i.price_low && num(i.price_high) ? '–' + esc(i.price_high) : ''}</b></div><div><small>Lot</small><b>${nf(i.lot_size)}</b></div><div><small>Allotment · credit · listing</small><b>${fmtDate(i.timeline.allotment)} · ${fmtDate(i.timeline.credit_by)} · ${fmtDate(i.timeline.listing) || '—'}</b></div></div>
      ${i.bid ? `<div class="note" style="margin-top:10px">You applied: <b>${esc(i.bid.id)}</b> · ${ipoOpts(i.bid)} · ${inr(i.bid.amount)} · ${ipoPill(i.bid.status)}${i.bid.status === 'Mandate pending' ? ' — approve the request in your UPI app before 5 pm on the closing day.' : ''}</div>` : ''}
      ${i.can_apply ? `<form class="pipo-f" data-id="${esc(i.id)}" style="margin-top:10px"><div class="form">
        <div class="fld"><label>Category</label><select class="in pi-cat">${i.choices.map(c => `<option value="${esc(c.code)}" data-min="${c.min_lots}" data-max="${c.max_lots}" data-cut="${c.cut_off ? 1 : 0}" data-decl="${esc(c.declare || '')}">${esc(c.name)} (${c.min_lots}${c.max_lots ? '–' + c.max_lots : '+'} lots)</option>`).join('')}</select></div>
        <div class="fld"><label>Exchange</label><select class="in pi-x">${i.exchanges.map(x => `<option>${x}</option>`).join('')}</select></div>
        <div class="fld"><label>Lots</label><input class="in mono pi-lots" inputmode="numeric" value="${(i.choices[0] || {}).min_lots || 1}"></div>
        ${i.type === 'NCD' ? `<div class="fld"><label>Series</label><select class="in pi-ser">${(i.series || ['']).map(s => `<option>${esc(s)}</option>`).join('')}</select></div>` : `<div class="fld"><label>Price (₹)</label><div class="tools"><input class="in mono pi-px" inputmode="decimal" style="max-width:120px" value="${esc(i.price_high || i.price_low)}"><label class="check"><input type="checkbox" class="pi-cut" ${(i.choices[0] || {}).cut_off ? 'checked' : ''}> at cut-off</label></div></div>`}
        <div class="fld"><label>Your UPI ID</label><input class="in mono pi-upi" list="pi-upis" value="${esc((d.upis[0] || {}).vpa || '')}"></div>
        <div class="fld"><label class="check"><input type="checkbox" class="pi-own"> This UPI ID is linked to my own bank account</label><label class="check pi-decl-f" hidden><input type="checkbox" class="pi-decl"> <span class="pi-decl-t"></span></label></div>
      </div><div class="pi-err"></div><div class="tools" style="margin-top:8px"><button class="btn pri" type="submit">Apply</button><span class="dim small">You will get an acknowledgement, then a mandate request in your UPI app to approve.</span></div></form>` : i.why ? `<p class="dim small" style="margin:10px 0 0">${esc(i.why)}</p>` : ''}</div></div>`;
    return pHead(v, 'IPOs & public issues', 'Apply for public issues from your own account. Up to ₹5 lakh you pay by UPI mandate from your own bank account: the money is only blocked, and released if shares are not allotted. One application per PAN in an issue.') +
      `<datalist id="pi-upis">${d.upis.map(x => `<option value="${esc(x.vpa)}">`).join('')}</datalist>` +
      (d.issues.length ? d.issues.map(card).join('') : '<div class="panel"><div class="bd dim">No issue is open for applications now.</div></div>') +
      `<div class="panel"><div class="hd"><h3>Your applications</h3></div>${tbl([['Application'], ['Issue'], ['Bid'], ['Amount', 'r'], ['Status'], ['Allotment'], ['']], d.bids.map(b => `<tr><td class="n">${esc(b.id)}${b.ack ? `<div class="dim small">ack ${esc(b.ack)}</div>` : ''}</td><td>${esc(b.issue)}</td><td class="small">${ipoOpts(b)}</td><td class="r">${inr(b.amount)}</td><td>${ipoPill(b.status)}${b.mandate_reason ? `<div class="dim small">${esc(b.mandate_reason)}</div>` : ''}</td><td class="small">${b.allotted_shares ? nf(b.allotted_shares) + ' shares' : esc(b.allotment || '')}</td><td>${d.own && b.issue_id && ['Draft', 'Uploaded', 'Mandate pending', 'Accepted'].includes(b.status) && !b.pending?.startsWith('Cancel') && !b.sent?.startsWith('Cancel') ? `<button class="btn sm" data-act="pIpoCancel" data-id="${esc(b.id)}">Withdraw</button>` : ''}</td></tr>`), { empty: 'No applications yet.' })}</div>
      <div class="note">Bids close at 4 pm on the closing day; approve the UPI mandate by 5 pm or the bid is not considered. Allotment is the next working day; money not used is released and shares credited the day after. Bids above ₹2 lakh cannot be withdrawn, and SME bids cannot be withdrawn or lowered. If the money is not released on time, tell us from the Help desk: the bank responsible pays compensation.</div>`;
  }
};
document.addEventListener('change', e => {
  const f = e.target.closest?.('.pipo-f'); if (!f || !e.target.classList.contains('pi-cat')) return;
  const o = e.target.selectedOptions[0]; f.querySelector('.pi-lots').value = o.dataset.min; const cut = f.querySelector('.pi-cut'); if (cut) { cut.disabled = o.dataset.cut !== '1'; if (cut.disabled) cut.checked = false; }
  const df = f.querySelector('.pi-decl-f'); df.hidden = !o.dataset.decl; f.querySelector('.pi-decl-t').textContent = 'I am ' + o.dataset.decl;
});
document.addEventListener('submit', async e => {
  const f = e.target; if (!f.classList?.contains('pipo-f')) return; e.preventDefault();
  const q = c => f.querySelector(c), cut = q('.pi-cut')?.checked || false;
  try {
    const b = await post('/v1/portal/ipo/bids', { issue: f.dataset.id, ucc: UI.pAcct, category: q('.pi-cat').value, exchange: q('.pi-x').value, options: [{ lots: parseInt(q('.pi-lots').value, 10) || 0, cut_off: cut, price: cut ? '0' : (q('.pi-px')?.value.trim() || '0'), series: q('.pi-ser')?.value || '' }], upi: q('.pi-upi').value.trim(), own_upi: q('.pi-own').checked, declared: q('.pi-decl').checked });
    toast(`Applied: ${b.id}, acknowledgement ${b.ack}`); render({ keep: true });
  } catch (err) { q('.pi-err').innerHTML = errHTML(err); }
});
Object.assign(A, {
  pIpoCancel: id => confirmModal('Withdraw your application', 'The blocked money is released by your bank once the exchange confirms.', '', 'Withdraw', () => act(() => post(`/v1/portal/ipo/bids/${ipoEnc(id)}/cancel`), r => r.status === 'Cancelled' ? 'Withdrawn' : 'Withdrawal sent to the exchange'), true),
});
