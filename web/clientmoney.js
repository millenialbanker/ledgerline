'use strict';
/* Ledgerline console: the daily reconciliation of client money (screen
   "clientmoney", Receipts & payouts).

   - Daily statement: every movement of the day between clients, the
     USCNBA, the clearing corporation, the DSCNBA, the settlement and own
     accounts, with totals, the charges debited to clients that back
     transfers to the own account, each account's balance, the bank
     statement lines and what needs explaining. A maker prepares it; a
     checker who did not prepare it signs it off. CSV and print.
   - Transfers: money moved between the firm's accounts (upstreaming,
     funds back from the clearing corporation, brokerage and dues to the own
     account, own funds in), recorded by a maker, approved by a checker.
   - Firm bank accounts: which account is which (USCNBA, DSCNBA, settlement,
     own, the clearing corporation), for which exchanges, with the rules on
     each: SEBI's name, the 7 working days to report it to each exchange,
     the bank undertaking, its @valid UPI ID, overdraft accounts.
   - Retained funds: client money kept back from the clearing corporation
     on the day, by NSE Clearing's reason codes RC01-RC09, with the firm's
     cut-off and the reasons people record (clientretention.go).

   Server: internal/ops/clientmoney.go, firmbanks.go, firmbankrules.go,
   clientretention.go;
   internal/api/clientmoney.go. Loaded after app.js. Names are prefixed cm. */

Object.assign(UI, { cmTab: 'stmt' });
const CM_ST = { 'Not prepared': 'warn', Prepared: 'acc', 'Signed off': 'good', 'Sent back': 'bad' };
const CM_XST = { Pending: 'warn', Posted: 'good', Recorded: 'good', Rejected: 'nt' };
const CM_KINDS = ['USCNBA', 'DSCNBA', 'Settlement', 'Own', 'Clearing corporation'];
const cmMaker = () => has('Ops maker', 'Ops checker');
const cmChecker = () => has('Ops checker');

V.clientmoney = {
  load: async () => {
    const [r, banks, xf, keep] = await Promise.all([get(`/v1/money-recon?date=${S.date}`), get('/v1/firm-banks'), get(`/v1/money-transfers?from=${addDaysJS(S.date, -31)}&to=${S.date}`), get(`/v1/money-retention?date=${S.date}`)]);
    return { r, banks, xf, keep };
  },
  html: d => {
    const r = d.r, tot = g => (r.totals || []).find(t => t.group === g) || { count: 0, amount: 0 };
    const pend = (d.xf.transfers || []).filter(t => t.status === 'Pending').length;
    const st = r.status + (r.changed ? ' · changed since' : '');
    const acts = `<button class="btn" data-act="dl" data-id="/v1/money-recon/${S.date}/file" data-name="ClientMoneyReconciliation_${S.date}.csv">CSV</button><button class="btn" data-act="cmPrint">Print</button>` +
      (r.can_prepare && cmMaker() ? `<button class="btn ${r.status === 'Prepared' && !r.changed ? '' : 'pri'}" data-act="cmPrepare">${r.status === 'Not prepared' ? 'Prepare' : 'Prepare again'}</button>` : '') +
      (r.can_sign ? '<button class="btn" data-act="cmReturn">Send back</button><button class="btn pri" data-act="cmSign">Sign off</button>' : '');
    return ph('Client money reconciliation', `The day's statement of money moved between clients, the USCNBA, the clearing corporation, the DSCNBA, the settlement and own accounts, as SEBI asks every broker to keep daily (master circular 2025/90, para 15.4.4.2). A maker prepares it; a checker who did not prepare it signs it off. ${r.settlement ? `${fmtDate(r.date)} is a running account settlement day.` : ''}`, acts) +
      `<div class="kpis">${kpi('Statement', esc(st), r.signed ? `signed off by ${esc(r.signed.by)}` : r.prepared ? `prepared by ${esc(r.prepared.by)}` : 'for ' + fmtDate(r.date), r.changed ? 'warn' : { 'Not prepared': 'warn', 'Sent back': 'bad', 'Signed off': 'good' }[r.status] || '')}${kpi('Received from clients', cmp(tot('receipt').amount), `${tot('receipt').count} receipts`)}${kpi('To the clearing corporation', cmp(tot('upstream').amount), `back ${cmp(tot('from_cc').amount)}`)}${kpi('Paid to clients', cmp(tot('payout').amount), `${tot('payout').count} payouts`)}${kpi('To the own account', cmp(tot('to_own').amount), `still backed ${cmp(r.pool.available)}`, num(r.pool.available) < 0 ? 'bad' : '')}${kpi('Flagged', (r.flags || []).length, (r.flags || []).length ? 'to explain' : 'nothing', (r.flags || []).length ? 'bad' : 'good')}</div>
      <div class="panel"><div class="hd"><div class="tools">${segBtns('cmTab', [['stmt', 'Daily statement'], ['xfer', `Transfers${pend ? ' · ' + pend + ' to approve' : ''}`], ['keep', `Retained funds${(d.keep.report.codes || []).length ? ' · ' + d.keep.report.codes.length : ''}`], ['banks', `Firm bank accounts · ${(d.banks.accounts || []).length}`]])}</div></div></div>` +
      ({ stmt: cmStatement, xfer: cmTransfers, keep: cmKeep, banks: cmBanks }[UI.cmTab] || cmStatement)(d);
  }
};

/* ---------- the statement ---------- */
function cmStatement(d) {
  const r = d.r;
  const steps = [r.prepared ? `Prepared by <b>${esc(r.prepared.by)}</b> ${esc((r.prepared.at || '').slice(0, 16).replace('T', ' '))}${r.prepared.note ? ` · ${esc(r.prepared.note)}` : ''}` : '', r.signed ? `Signed off by <b>${esc(r.signed.by)}</b> ${esc((r.signed.at || '').slice(0, 16).replace('T', ' '))}${r.signed.note ? ` · ${esc(r.signed.note)}` : ''}` : '', r.returned && r.status === 'Sent back' ? `Sent back by <b>${esc(r.returned.by)}</b>: ${esc(r.returned.note)}` : ''].filter(Boolean);
  const head = `${pill(r.status, CM_ST[r.status] || 'nt')} ${steps.join(' · ')}${r.changed ? `<div class="note warn" style="margin-top:8px">The records of ${fmtDate(r.date)} changed after the statement was prepared: prepare it again before it is signed off.</div>` : ''}`;
  const flags = (r.flags || []).length ? `<div class="note bad"><b>To explain (${r.flags.length})</b><ul class="cm-flags">${r.flags.map(f => `<li>${esc(f.text)}<div class="dim small">${esc(f.rule)}</div></li>`).join('')}</ul></div>` : '<div class="note good">Nothing flagged.</div>';
  const notes = (r.notes || []).map(n => `<div class="note warn" style="margin-top:8px">${esc(n)}${/not named yet/.test(n) ? ' <button class="linkish" data-act="ui" data-k="cmTab" data-v="banks">Name them</button>' : ''}</div>`).join('');
  const moves = tbl([['From'], ['To'], ['Amount', 'r'], ['Purpose'], ['Reference'], ['Entry']], (r.moves || []).map(m => `<tr class="${(m.flags || []).length ? 'cm-flagged' : ''}"><td class="small">${esc(m.from)}</td><td class="small">${esc(m.to)}${m.assumed ? ' <span class="dim" title="Nothing recorded says which account: the usual one of its kind">(usual)</span>' : ''}</td><td class="r">${inr(m.amount, 2)}</td><td class="small wrap">${esc(m.purpose)}${(m.flags || []).map(f => `<div class="small neg">${esc(f)}</div>`).join('')}</td><td class="mono small">${esc(m.ref || '')}</td><td class="mono small dim">${esc(m.entry || '')}</td></tr>`), { tall: true, empty: 'No money moved through the client money accounts on this day.' });
  const totals = tbl([['Movements'], ['Count', 'r'], ['Amount', 'r']], (r.totals || []).map(t => `<tr><td>${esc(t.name)}</td><td class="r">${t.count}</td><td class="r">${inr(t.amount, 2)}</td></tr>`), { empty: 'None' });
  const c = r.charges, p = r.pool;
  const backing = tbl([['Transfers to the own account'], ['₹', 'r']], [['Brokerage debited to clients today', c.brokerage], ['Other charges debited today (interest, DP, others)', c.other], ['Statutory dues and exchange charges debited today', c.statutory], ['To the end of the day: charges debited to clients', p.charges], ['Own funds put into client money accounts', p.own_in], ['Taken to the own account', p.to_own]].map(([l, v]) => `<tr><td>${l}</td><td class="r">${nf(v, 2)}</td></tr>`),
    { foot: `<tfoot><tr><td>Still backed</td><td class="r ${num(p.available) < 0 ? 'neg' : ''}">${nf(p.available, 2)}</td></tr></tfoot>` });
  const accts = tbl([['Account'], ['Opening', 'r'], ['In', 'r'], ['Out', 'r'], ['Closing (book)', 'r'], ['Bank statement', 'r'], ['Difference', 'r']], (r.accounts || []).map(a => `<tr><td class="small"><b>${esc(a.label)}</b></td><td class="r">${nf(a.opening, 2)}</td><td class="r">${nf(a.in, 2)}</td><td class="r">${nf(a.out, 2)}</td><td class="r ${num(a.closing) < 0 ? 'neg' : ''}">${nf(a.closing, 2)}</td><td class="r">${a.statement ? nf(a.statement, 2) : '<span class="dim">—</span>'}</td><td class="r ${num(a.difference) ? 'neg' : ''}">${a.difference ? nf(a.difference, 2) : ''}</td></tr>`), { empty: 'Name the firm’s bank accounts to see each one’s balance.' });
  const items = (r.unmatched || []).length ? `<div class="panel"><div class="hd"><h3>Reconciling items</h3><small>bank statement lines and Ledgerline's records that do not match by reference and amount</small></div>${tbl([['Item'], ['Account'], ['Reference'], ['Dr/Cr'], ['Amount', 'r'], ['What']], r.unmatched.map(u => `<tr><td class="small">${esc(u.side)}</td><td class="small">${esc(u.account)}</td><td class="mono small">${esc(u.ref)}</td><td>${esc(u.dr_cr)}</td><td class="r">${inr(u.amount, 2)}</td><td class="small wrap">${esc(u.what)}</td></tr>`))}</div>` : '';
  const lines = (r.lines || []).length ? `<div class="panel"><div class="hd"><h3>Bank statement lines</h3><small>${r.lines.length} lines loaded for the day</small></div>${tbl([['Account'], ['Time'], ['Mode'], ['Reference'], ['Party'], ['Dr/Cr'], ['Amount', 'r'], ['Balance', 'r']], r.lines.map(l => `<tr><td class="mono small">${esc(l.account || 'client account')}</td><td class="n dim">${esc(l.time || '')}</td><td>${esc(l.mode || '')}</td><td class="mono small">${esc(l.ref)}</td><td class="small">${esc(l.party || '')} <span class="mono dim">${esc(l.party_account || '')}</span></td><td>${esc(l.dr_cr)}</td><td class="r">${inr(l.amount, 2)}</td><td class="r">${l.balance ? nf(l.balance, 2) : ''}</td></tr>`), { tall: true })}</div>` : '';
  return `<div class="panel"><div class="bd">${head}<div style="margin-top:10px">${flags}</div>${notes}</div></div>
    <div class="panel"><div class="hd"><h3>Movements of ${fmtDate(r.date)}</h3><small>${(r.moves || []).length} · from the ledger, receipts, payouts, settlements and recorded transfers</small></div>${moves}</div>
    <div class="grid g2"><div class="panel"><div class="hd"><h3>Totals</h3></div>${totals}</div><div class="panel"><div class="hd"><h3>Charges and transfers to the own account</h3></div>${backing}</div></div>
    <div class="panel"><div class="hd"><h3>Accounts</h3><small>book balances from Ledgerline's records${r.settlement ? '; a settlement day: the DSCNBA may keep a balance if the bank instructions were given' : '; the DSCNBA must end the day at nil'}</small></div>${accts}</div>${items}${lines}`;
}

/* ---------- transfers ---------- */
function cmEnds(d, kinds) {
  const opts = (d.banks.accounts || []).filter(b => b.active && kinds.includes(b.kind)).map(b => `<option value="${esc(b.id)}">${esc(b.kind)} · ${esc(b.bank)} ${esc(b.account)}</option>`);
  if (kinds.includes('Clearing corporation')) opts.push('<option value="CC">The clearing corporation</option>');
  return opts.join('');
}
function cmTransfers(d) {
  const ts = d.xf.transfers || [], P = Object.fromEntries((d.xf.purposes || []).map(p => [p.id, p]));
  const end = id => { if (id === 'CC') return 'Clearing corporation'; const b = (d.banks.accounts || []).find(x => x.id === id); return b ? `${b.kind} ${b.bank} ${b.account}` : id; };
  const rows = ts.map(t => `<tr><td class="mono small">${esc(t.id)}</td><td class="n">${fmtDate(t.date)}</td><td class="small wrap">${esc(P[t.purpose]?.name || t.purpose)}${t.segment ? ` · ${esc(t.segment)}` : ''}${t.warning ? `<div class="small neg">${esc(t.warning)}</div>` : ''}${t.note ? `<div class="dim small">${esc(t.note)}</div>` : ''}</td><td class="small">${esc(end(t.from))} → ${esc(end(t.to))}</td><td class="r">${inr(t.amount, 2)}</td><td class="mono small">${esc(t.ref)}</td>
    <td>${pill(t.status, CM_XST[t.status] || 'nt')}<div class="dim small">${esc(t.maker)}${t.checker ? ' · ' + esc(t.checker) : ''}</div>${t.reason ? `<div class="small">${esc(t.reason)}</div>` : ''}</td>
    <td>${t.status === 'Pending' && cmChecker() && t.maker !== S.me?.actor ? `<div class="split"><button class="btn sm pri" data-act="cmXDecide" data-id="${esc(t.id)}" data-ok="1">Approve</button><button class="btn sm" data-act="cmXDecide" data-id="${esc(t.id)}">Reject</button></div>` : ''}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Transfers between the firm's accounts</h3><small>the last 31 days</small>${cmMaker() ? '<button class="btn sm pri" data-act="cmXNew">Record a transfer</button>' : ''}</div>
    <div class="bd"><p class="dim small" style="margin:0">Client money goes from the USCNBA to the clearing corporation (through the settlement account), comes back to the DSCNBA or the settlement account, and is paid to clients only from the DSCNBA; never from a USCNBA straight to a DSCNBA (NSE/INSP/57959, FAQs 17, 21 and 30). Only brokerage, charges and statutory dues debited to clients, or a client's debit the firm met, go to the own account (SEBI master circular 2025/90, para 15.4.4.2). Upstreaming is also recorded with <b>Upstream funds to CC</b> on Bank reconciliation.</p></div>
    ${tbl([['ID'], ['Date'], ['Why'], ['From → to'], ['Amount', 'r'], ['Reference'], ['Status'], ['']], rows, { empty: 'No transfer recorded in the last 31 days.' })}</div>`;
}

function cmXModal() {
  const d = C.clientmoney, ps = d.xf.purposes || [];
  if (!(d.banks.accounts || []).length) { toast('Name the firm’s bank accounts first (Firm bank accounts)'); UI.cmTab = 'banks'; render({ keep: true }); return; }
  const segs = d.banks.segments || [];
  confirmModal('Record a transfer', 'Money that moved between the firm’s accounts, as the bank shows it. A checker approves it; approving posts it in the ledger where the accounts differ there.',
    `<div class="form"><div class="fld full"><label for="cm-p">Why the money moved</label><select class="in" id="cm-p">${ps.map(p => `<option value="${esc(p.id)}">${esc(p.name)}</option>`).join('')}</select><span class="dim small" id="cm-pd"></span></div>
      <div class="fld"><label for="cm-f">From</label><select class="in" id="cm-f"></select></div><div class="fld"><label for="cm-t">To</label><select class="in" id="cm-t"></select></div>
      <div class="fld full" id="cm-ww" hidden><label for="cm-w">Why the funds were taken back (SEBI's list)</label><select class="in" id="cm-w">${(d.xf.withdraw_reasons || []).map(w => `<option value="${esc(w.id)}" data-to="${esc(w.to)}">${esc(w.name)}</option>`).join('')}</select></div>
      <div class="fld" id="cm-sw" hidden><label for="cm-s">Exchange segment</label><select class="in" id="cm-s">${segs.map(s => `<option>${esc(s)}</option>`).join('')}</select></div>
      <div class="fld"><label for="cm-d">Date</label><input class="in" type="date" id="cm-d" value="${esc(S.date)}" max="${esc(S.date)}"></div>
      <div class="fld"><label for="cm-a">Amount (₹)</label><input class="in" id="cm-a" inputmode="decimal"></div><div class="fld"><label for="cm-r">Bank reference (UTR)</label><input class="in mono" id="cm-r"></div>
      <div class="fld full"><label for="cm-n">Note (optional)</label><input class="in" id="cm-n"></div></div>`,
    'Record', () => act(() => post('/v1/money-transfers', { date: $('cm-d').value, purpose: $('cm-p').value, from: $('cm-f').value, to: $('cm-t').value, segment: $('cm-sw').hidden ? '' : $('cm-s').value, withdraw: $('cm-ww').hidden ? '' : $('cm-w').value, amount: $('cm-a').value.replace(/,/g, '').trim() || '0', ref: $('cm-r').value, note: $('cm-n').value }), t => `${t.id} recorded for a checker${t.warning ? ' · ' + t.warning : ''}`));
  const fill = () => {
    const p = ps.find(x => x.id === $('cm-p').value); if (!p) return;
    const rel = p.id === 'release', w = rel ? $('cm-w').selectedOptions[0]?.dataset.to : '';
    $('cm-f').innerHTML = cmEnds(d, p.from); $('cm-t').innerHTML = cmEnds(d, w ? [w] : p.to); $('cm-sw').hidden = !p.segment; $('cm-ww').hidden = !rel; $('cm-pd').textContent = 'Posts: ' + p.posts;
  };
  $('cm-p').onchange = fill; $('cm-w').onchange = fill; fill();
}

/* ---------- client money kept back, by reason ---------- */
function cmKeep(d) {
  const k = d.keep, r = k.report, rules = k.rules || {};
  const codes = (r.codes || []).map(c => `<tr class="${c.code === 'RC09' ? 'cm-flagged' : ''}"><td class="mono"><b>${esc(c.code)}</b></td><td class="small wrap">${esc(c.name)}${(c.lines || []).slice(0, 8).map(l => `<div class="dim small">${l.ucc ? esc(l.ucc) + ' · ' : ''}${esc(l.what)}${l.ref ? ' · ' + esc(l.ref) : ''} · ${inr(l.amount, 2)}</div>`).join('')}${(c.lines || []).length > 8 ? `<div class="dim small">and ${c.lines.length - 8} more</div>` : ''}</td><td class="small">${esc(c.account || '')}</td><td class="r">${inr(c.amount, 2)}</td></tr>`);
  const entries = (r.entries || []).map(e => `<tr class="${e.status === 'Recorded' ? '' : 'dim'}"><td class="mono small">${esc(e.id)}</td><td class="mono">${esc(e.code)}</td><td class="r">${inr(e.amount, 2)}</td><td class="small wrap">${esc(e.ref || '')}${e.evidence ? `<div class="dim small">Evidence: ${esc(e.evidence)}</div>` : ''}${e.note ? `<div class="dim small">${esc(e.note)}</div>` : ''}</td><td>${pill(e.status, e.status === 'Recorded' ? 'good' : 'nt')}<div class="dim small">${esc(e.by)}</div></td><td>${e.status === 'Recorded' && r.can_record ? `<button class="btn sm" data-act="cmKeepWithdraw" data-id="${esc(e.id)}">Withdraw</button>` : ''}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>Client money kept back on ${fmtDate(r.date)}</h3><small>by NSE Clearing's reason codes (NCL/CMPL/61186); each is a line of its own in the SEGMON file</small>${r.can_record ? '<button class="btn sm pri" data-act="cmKeepNew">Record a reason</button>' : ''}${r.can_rules ? '<button class="btn sm" data-act="cmKeepRules">Cut-off time</button>' : ''}</div>
    <div class="bd"><p class="dim small" style="margin:0">Client money goes to the clearing corporation every day. Money kept back is reported by reason; money a client paid after the cut-off (${rules.funds_cutoff ? esc(rules.funds_cutoff) : 'not set'}) first meets the client's debit, and only the rest is RC01. RC09 means no stipulated reason: upstream it, or record the reason.</p>
    ${(r.warnings || []).map(w => `<div class="note warn" style="margin-top:6px">${esc(w)}</div>`).join('')}</div>
    ${tbl([['Code'], ['Reason and what it is'], ['Lies in'], ['Amount', 'r']], codes, { empty: 'Nothing kept back on this day.', foot: (r.codes || []).length ? `<tfoot><tr><td colspan="3">Total (stipulated reasons ${inr(r.stipulated, 2)})</td><td class="r">${inr(r.total, 2)}</td></tr></tfoot>` : '' })}</div>
    <div class="panel"><div class="hd"><h3>Reasons recorded by people</h3><small>for what Ledgerline cannot see: MFOS in transit, OFS refunds, a bank's failure, a payout failed at the bank</small></div>${tbl([['ID'], ['Code'], ['Amount', 'r'], ['Reference'], ['Status'], ['']], entries, { empty: 'None recorded for this day.' })}</div>`;
}

/* ---------- the firm's bank accounts ---------- */
function cmBanks(d) {
  const v = d.banks, ed = v.can_edit;
  const kinds = Object.fromEntries((v.kinds || []).map(k => [k.ID || k.id, k]));
  const rows = (v.accounts || []).map(b => `<tr class="${b.active ? '' : 'dim'}"><td class="mono small">${esc(b.id)}</td><td><b>${esc(b.kind)}</b>${b.cc ? ` · ${esc(v.ccs?.[b.cc] || b.cc)}` : ''}${b.name ? `<div class="dim small">${esc(b.name)}</div>` : ''}</td><td>${esc(b.bank)}${b.branch ? `<div class="dim small">${esc(b.branch)}</div>` : ''}</td><td class="mono">${esc(b.account)}<div class="dim small">${esc(b.ifsc)}</div></td><td class="small">${(b.serves || []).length ? b.serves.map(esc).join(', ') : '<span class="dim">all</span>'}</td><td>${b.reported ? pill('Reported', 'good') : b.kind === 'DSCNBA' || b.kind === 'USCNBA' ? pill('Not reported', 'warn') : ''}${Object.entries(b.reported_on || {}).map(([x, dt]) => `<div class="dim small">${esc(x)} ${fmtDate(dt)}</div>`).join('')}${b.upi_id ? `<div class="dim small mono">${esc(b.upi_id)}</div>` : ''}${b.overdraft ? `<div class="small">${pill('Overdraft', 'nt')}</div>` : ''}</td><td class="r small">${num(b.opening) ? inr(b.opening, 2) : ''}${b.opening_on ? `<div class="dim">${fmtDate(b.opening_on)}</div>` : ''}</td><td>${b.active ? pill('Active', 'good') : pill('Closed ' + fmtDate(b.closed_on), 'nt')}</td><td>${ed ? `<button class="btn sm" data-act="cmBank" data-id="${esc(b.id)}">Change</button>` : ''}</td></tr>`);
  const sug = (v.suggest || []).length ? `<div class="note" style="margin-top:8px"><b>Named in Settings, not in the register yet:</b> ${v.suggest.map((s, i) => `<span class="tag">${esc(s.kind)} ${esc(s.account)}</span> <span class="dim small">${esc(s.from)}</span>${ed ? ` <button class="linkish" data-act="cmBank" data-id="" data-sug="${i}">Add</button>` : ''}`).join(' · ')}</div>` : '';
  return `<div class="panel"><div class="hd"><h3>Firm bank accounts</h3><small>which of the firm's accounts is which</small>${ed ? '<button class="btn sm pri" data-act="cmBank" data-id="">Add an account</button>' : ''}</div>
    <div class="bd">${(v.warnings || []).map(w => `<div class="note warn" style="margin-bottom:6px">${esc(w)}</div>`).join('')}${sug}</div>
    ${(v.reports || []).length ? `<div class="bd"><b class="small">To report or record</b>${tbl([['Account'], ['What'], ['Exchange'], ['From'], ['Due']], v.reports.map(it => `<tr><td class="small">${esc(it.label)}</td><td>${esc({ opening: 'New account', closure: 'Closure', undertaking: 'Bank undertaking' }[it.what] || it.what)}</td><td>${esc(it.exchange || '—')}</td><td class="n">${fmtDate(it.from)}</td><td class="n ${it.due < S.date ? 'neg' : ''}">${fmtDate(it.due)}</td></tr>`))}</div>` : ''}
    ${tbl([['ID'], ['Kind'], ['Bank'], ['Account'], ['Exchanges'], ['Reported'], ['Opening', 'r'], ['Status'], ['']], rows, { empty: 'No account named yet. Add the USCNBA, the DSCNBA, the settlement and own accounts, and the clearing corporation’s account.' })}
    <div class="bd"><ul class="small cm-kinds">${CM_KINDS.map(k => kinds[k] ? `<li><b>${esc(kinds[k].Name || kinds[k].name)}</b>: ${esc(kinds[k].About || kinds[k].about)}</li>` : '').join('')}</ul></div></div>`;
}

function cmBankModal(id, sugIdx) {
  const v = C.clientmoney.banks, b = (v.accounts || []).find(x => x.id === id) || {}, s = sugIdx !== undefined && sugIdx !== '' ? v.suggest[+sugIdx] : null;
  const kind = b.kind || s?.kind || 'USCNBA', serves = b.serves || s?.serves || [];
  const opts = [...new Set([...(v.segments || []).map(x => x.split('-')[0]), ...(v.segments || [])])];
  confirmModal(id ? `Change ${esc(id)}` : 'Add an account', s ? `From ${esc(s.from)}: the full account number is taken from there.` : 'Its kind decides what the daily statement checks: receipts only in a USCNBA, payments only from a DSCNBA, and nothing left in the DSCNBA at the end of the day.',
    `<div class="form"><div class="fld"><label for="cb-k">Kind</label><select class="in" id="cb-k">${CM_KINDS.map(k => `<option ${k === kind ? 'selected' : ''}>${k}</option>`).join('')}</select></div>
      <div class="fld" id="cb-ccw"><label for="cb-cc">Clearing corporation</label><select class="in" id="cb-cc"><option value="">—</option>${Object.entries(v.ccs || {}).map(([k, n]) => `<option value="${esc(k)}" ${b.cc === k ? 'selected' : ''}>${esc(n)}</option>`).join('')}</select></div>
      <div class="fld"><label for="cb-b">Bank</label><input class="in" id="cb-b" value="${esc(b.bank || '')}"></div><div class="fld"><label for="cb-br">Branch (optional)</label><input class="in" id="cb-br" value="${esc(b.branch || '')}"></div>
      <div class="fld"><label for="cb-a">Account number</label><input class="in mono" id="cb-a" value="${esc(s ? s.account : b.account || '')}" ${s ? 'disabled' : ''} placeholder="${id ? 'leave as it is to keep it' : '9 to 18 digits'}"></div><div class="fld"><label for="cb-i">IFSC</label><input class="in mono" id="cb-i" value="${esc(b.ifsc || '')}"></div>
      <div class="fld full"><label for="cb-n">Name of the account as the bank has it (for a USCNBA, DSCNBA or settlement account: firm name – USCNB account, – DSCNB account, – Settlement Account)</label><input class="in" id="cb-n" value="${esc(b.name || '')}" placeholder="e.g. firm name – USCNB account"></div>
      <fieldset class="fld full"><legend>For these exchanges or segments (none ticked: all)</legend><div class="tools">${opts.map(o => `<label class="check"><input type="checkbox" data-cbs="${esc(o)}" ${serves.includes(o) ? 'checked' : ''}> ${esc(o)}</label>`).join('')}</div></fieldset>
      <label class="check full"><input type="checkbox" id="cb-r" ${b.reported ? 'checked' : ''}> Reported to the exchanges under enhanced supervision</label>
      <div class="fld"><label for="cb-op">Opened on</label><input class="in" type="date" id="cb-op" value="${esc(b.opened_on || '')}"></div><div class="fld"><label for="cb-u">Bank undertaking submitted on</label><input class="in" type="date" id="cb-u" value="${esc(b.undertaking_on || '')}"></div>
      <fieldset class="fld full"><legend>Reported to each exchange on (within 7 working days of opening)</legend><div class="tools">${(v.exchanges || []).map(x => `<label class="small">${esc(x)} <input class="in" type="date" data-cbrep="${esc(x)}" value="${esc((b.reported_on || {})[x] || '')}"></label>`).join('')}</div></fieldset>
      ${b.closed_on ? `<fieldset class="fld full"><legend>Closure reported to each exchange on (within 7 working days of closing)</legend><div class="tools">${(v.exchanges || []).map(x => `<label class="small">${esc(x)} <input class="in" type="date" data-cbclo="${esc(x)}" value="${esc((b.closure_reported_on || {})[x] || '')}"></label>`).join('')}</div></fieldset>` : ''}
      <div class="fld"><label for="cb-upi">Its @valid UPI ID (SEBI Check)</label><input class="in mono" id="cb-upi" value="${esc(b.upi_id || '')}" placeholder="abc.brk@validbank"></div>
      <label class="check"><input type="checkbox" id="cb-if" ${b.investor_facing ? 'checked' : ''}> Given to clients to pay into (a USCNBA always is)</label>
      <label class="check full"><input type="checkbox" id="cb-od2" ${b.overdraft ? 'checked' : ''}> An overdraft, cash credit or loan-against-securities account (never client money)</label>
      <div class="fld"><label for="cb-o">Opening balance (₹, optional)</label><input class="in" id="cb-o" inputmode="decimal" value="${num(b.opening) ? esc(b.opening) : ''}"></div><div class="fld"><label for="cb-od">Balance on</label><input class="in" type="date" id="cb-od" value="${esc(b.opening_on || '')}"></div>
      ${id ? `<div class="fld"><label for="cb-c">Closed on (optional)</label><input class="in" type="date" id="cb-c" value="${esc(b.closed_on || '')}"></div>` : ''}<div class="fld full"><label for="cb-note">Note (optional)</label><input class="in" id="cb-note" value="${esc(b.note || '')}"></div></div>`,
    'Save', () => act(() => post('/v1/firm-banks', { id: id || '', kind: $('cb-k').value, cc: $('cb-cc').value, bank: $('cb-b').value, branch: $('cb-br').value, account: s ? '' : $('cb-a').value.trim(), ifsc: $('cb-i').value, name: $('cb-n').value,
      serves: [...document.querySelectorAll('[data-cbs]')].filter(x => x.checked).map(x => x.dataset.cbs), reported: $('cb-r').checked, opened_on: $('cb-op').value, undertaking_on: $('cb-u').value, upi_id: $('cb-upi').value.trim(), investor_facing: $('cb-if').checked, overdraft: $('cb-od2').checked,
      reported_on: Object.fromEntries([...document.querySelectorAll('[data-cbrep]')].filter(x => x.value).map(x => [x.dataset.cbrep, x.value])),
      closure_reported_on: Object.fromEntries([...document.querySelectorAll('[data-cbclo]')].filter(x => x.value).map(x => [x.dataset.cbclo, x.value])), opening: ($('cb-o').value || '0').replace(/,/g, '').trim(), opening_on: $('cb-od').value, active: !$('cb-c')?.value, closed_on: $('cb-c')?.value || '', note: $('cb-note').value, suggestion: s ? s.key : '' }), r => `${r.id} saved`));
  const cc = () => { $('cb-ccw').hidden = $('cb-k').value !== 'Clearing corporation'; };
  $('cb-k').onchange = cc; cc();
}

Object.assign(A, {
  cmPrepare: () => confirmModal(`Prepare the statement of ${fmtDate(S.date)}`, 'It is kept as it stands now, with its CSV, for a checker to sign off. Explain anything flagged in the note.', '<div class="fld"><label for="cm-pn">Note (optional)</label><input class="in" id="cm-pn"></div>', 'Prepare', () => act(() => post(`/v1/money-recon/${S.date}/prepare`, { note: $('cm-pn').value }), 'Prepared for sign-off')),
  cmSign: () => confirmModal(`Sign off the statement of ${fmtDate(S.date)}`, `${(C.clientmoney.r.flags || []).length} flagged. You confirm you have checked the movements against the bank statements and the explanations given.`, '<div class="fld"><label for="cm-sn">Note (optional)</label><input class="in" id="cm-sn"></div>', 'Sign off', () => act(() => post(`/v1/money-recon/${S.date}/sign`, { note: $('cm-sn').value }), 'Signed off')),
  cmReturn: () => confirmModal('Send it back', 'Say what is to be done before you sign it off; the one who prepared it gets a work item.', '<div class="fld"><label for="cm-rr">Reason</label><input class="in" id="cm-rr"></div>', 'Send back', () => act(() => post(`/v1/money-recon/${S.date}/return`, { reason: $('cm-rr').value }), 'Sent back')),
  cmPrint: () => openDoc(`/v1/money-recon/${S.date}/document`),
  cmXNew: () => cmXModal(),
  cmXDecide: (id, el) => {
    if (el.dataset.ok) { act(() => post(`/v1/money-transfers/${id}/decide`, { approve: true }), t => `${t.id} ${t.status.toLowerCase()}`); return; }
    confirmModal(`Reject ${esc(id)}`, 'Say why, so the maker can correct it.', '<div class="fld"><label for="cm-xr">Reason</label><input class="in" id="cm-xr"></div>', 'Reject', () => act(() => post(`/v1/money-transfers/${id}/decide`, { approve: false, reason: $('cm-xr').value }), 'Rejected'), true);
  },
  cmBank: (id, el) => cmBankModal(id, el.dataset.sug),
  cmKeepNew: () => {
    const d = C.clientmoney, codes = (d.keep.report.all_codes || []).filter(c => c.code !== 'RC09');
    confirmModal(`Record money kept back on ${fmtDate(S.date)}`, 'Only for a stipulated reason. RC04 and RC07 need the bank’s evidence.',
      `<div class="form"><div class="fld full"><label for="ck-c">Reason</label><select class="in" id="ck-c">${codes.map(c => `<option value="${esc(c.code)}">${esc(c.code)} · ${esc(c.name)}</option>`).join('')}</select></div>
        <div class="fld"><label for="ck-a">Amount (₹)</label><input class="in" id="ck-a" inputmode="decimal"></div>
        <div class="fld"><label for="ck-acct">In the account</label><select class="in" id="ck-acct"><option value="">—</option>${(d.banks.accounts || []).filter(b => b.active && ['USCNBA', 'DSCNBA', 'Settlement'].includes(b.kind)).map(b => `<option value="${esc(b.id)}">${esc(b.kind)} · ${esc(b.bank)} ${esc(b.account)}</option>`).join('')}</select></div>
        <div class="fld"><label for="ck-r">Reference (FDR, folio, bank reference)</label><input class="in mono" id="ck-r"></div><div class="fld"><label for="ck-e">The bank’s evidence</label><input class="in" id="ck-e" placeholder="letter, e-mail or statement"></div>
        <div class="fld full"><label for="ck-n">Note</label><input class="in" id="ck-n"></div></div>`,
      'Record', () => act(() => post('/v1/money-retention', { date: S.date, code: $('ck-c').value, amount: $('ck-a').value.replace(/,/g, '').trim() || '0', account: $('ck-acct').value, ref: $('ck-r').value, evidence: $('ck-e').value, note: $('ck-n').value }), e => `${e.id} recorded`));
  },
  cmKeepWithdraw: id => confirmModal(`Withdraw ${esc(id)}`, 'Say why it was recorded in error.', '<div class="fld"><label for="ck-w">Reason</label><input class="in" id="ck-w"></div>', 'Withdraw', () => act(() => post(`/v1/money-retention/${id}/withdraw`, { reason: $('ck-w').value }), 'Withdrawn'), true),
  cmKeepRules: () => confirmModal('The firm’s cut-off for client money', 'Money a client pays after it cannot reach the clearing corporation that day and is reported as retained under RC01. The clearing member sets it, not earlier than an hour before its own cut-off (not earlier than 6 pm); stop taking fresh client money 30 minutes before it.',
    `<div class="fld"><label for="ck-t">Cut-off (HH:MM)</label><input class="in mono" id="ck-t" value="${esc(C.clientmoney.keep.rules?.funds_cutoff || '')}" placeholder="18:00"></div>`, 'Save', () => act(() => post('/v1/money-rules', { funds_cutoff: $('ck-t').value.trim() }), 'Saved')),
});
