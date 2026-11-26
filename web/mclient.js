'use strict';
/* Ledgerline client app (/app/): the broker's investors on their phones.
   Built on the client portal's calls (/v1/portal/*, /v1/payouts,
   /v1/changes, /v1/tickets), which limit every answer to the accounts of
   the signed-in client login (their own and their family's). The firm's
   name, colours and logo come from Settings → Client app; the regulatory
   details every Indian broker app shows are under More → About us.
   Loaded after mshell.js. */

const CA = { v: null, acct: '', cache: {} };
MS.tabs = [['home', 'Home', 'home'], ['holdings', 'Holdings', 'hold'], ['funds', 'Funds', 'money'], ['documents', 'Reports', 'doc'], ['more', 'More', 'more']];
MS.title = 'Investor';
MS.applyConfig = c => {
  const b = c.brand || {};
  MS.title = b.app_name || c.firm || 'Investor';
  document.title = MS.title;
  const r = document.documentElement.style;
  if (b.primary) { r.setProperty('--accent', b.primary); r.setProperty('--m-brand', b.primary); }
  if (b.accent) r.setProperty('--m-accent', b.accent);
};
MS.logo = () => MS.cfg?.brand?.logo ? `<img class="m-logo" src="${esc(MS.cfg.brand.logo)}" alt="">` : '<span class="mark" aria-hidden="true"><i></i><i></i><i></i></span>';
MS.sub = () => CA.v?.account ? `${CA.v.account.name} · ${CA.v.account.ucc}` : (MS.cfg?.firm || '');
MS.gateFoot = () => `<nav class="m-legal"><button class="linkish" data-go="about">About us · SEBI registration · investor charter</button></nav>`;

async function portal(force) {
  if (!force && CA.v && Date.now() - CA.at < 20000) return CA.v;
  CA.v = await get(`/v1/portal?date=${MS.day()}&ucc=${encodeURIComponent(CA.acct)}`);
  CA.acct = CA.v.account.ucc; CA.at = Date.now();
  // The pop-up on nominating, at the first login of the day of a demat
  // account without a nominee (internal/ops/contactletter.go).
  if (CA.v.nomination_nudge) setTimeout(() => sheet('Add a nominee', `<p>${esc(CA.v.nomination_nudge)}</p>`, 'OK', () => closeSheet()), 50);
  return CA.v;
}
const notTrading = v => v.account.dp_only;
// mcFYs are the financial years a client's tax papers are offered for:
// the running year and the one before; mcFYRange their first and last
// day (the last capped at today).
const mcFYs = () => { const [y, m] = MS.day().split('-').map(Number), s = m >= 4 ? y : y - 1; return [s, s - 1].map(x => `${x}-${String((x + 1) % 100).padStart(2, '0')}`); };
const mcFYRange = fy => { const y = +fy.slice(0, 4), t = `${y + 1}-03-31`; return [`${y}-04-01`, t < MS.day() ? t : MS.day()]; };
const docBtn = (path, name, label) => `<button class="btn sm" data-act="share" data-id="${esc(path)}" data-name="${esc(name)}">${esc(label || 'Open or share')}</button>`;
const PO_ST = { Requested: 'warn', Approved: 'acc', Paid: 'good', Rejected: 'bad', Failed: 'bad', Expired: 'nt', Cancelled: 'nt' };
const tile = (label, value, sub, cls = '') => `<div class="m-tile"><small>${esc(label)}</small><b class="mono ${cls}">${value}</b>${sub ? `<span>${sub}</span>` : ''}</div>`;
const acctBar = v => v.accounts.length > 1 ? `<button class="m-acct" data-go="accounts">${esc(v.account.name)} · <span class="mono">${esc(v.account.ucc)}</span> <span class="dim">· switch</span></button>` : '';

MS.screens = {
  home: {
    load: () => portal(true),
    html: v => {
      const mtm = (v.positions || []).reduce((s, p) => s + num(p.mtm), 0), w = v.withdrawable || {};
      return `<div class="ph-pg">${acctBar(v)}<h1>Hello, ${esc(v.account.name.split(' ')[0])}</h1>
        ${v.account.blocked ? '<div class="m-warn">Your account is blocked for new trades. Ask us from the Help desk.</div>' : ''}${v.account.inactive ? '<div class="m-warn">Your account is inactive. Ask us from the Help desk to start it again.</div>' : ''}
        <div class="m-tiles">${tile('Ledger balance', inr(v.ledger, 2), num(v.ledger) < 0 ? 'you owe us' : 'credit with us', sgn(v.ledger))}
          ${tile('Holdings value', inr(v.holdings_value), nf(v.holdings.length) + ' holdings')}
          ${(v.positions || []).length ? tile('Today’s P&L (open positions)', inr(mtm, 2), 'marked to market', sgn(mtm)) : ''}
          ${tile('You can withdraw', inr(w.available, 2), w.blocked ? esc(w.blocked_why || '') : 'to your bank', 'pos')}</div>
        <div class="m-quick"><button class="btn pri" data-go="addmoney">Add money</button><button class="btn" data-go="funds">Withdraw</button><button class="btn" data-go="documents">Contract notes</button></div>
        ${v.notes.length ? `<section class="ph-glance"><h2>Latest contract notes</h2><dl>${v.notes.slice(0, 3).map(n => row(`${fmtDate(n.date)} <span class="dim mono small">${esc(n.no)}</span>`, `<span class="mono ${sgn(n.net)}">${inr(n.net, 2)}</span>`)).join('')}</dl></section>` : ''}
        ${v.payouts.filter(p => p.status === 'Requested' || p.status === 'Approved').map(p => `<div class="ph-card info"><span class="ph-k">Payout ${esc(p.id)}<span>${pill(p.status, PO_ST[p.status])}</span></span><b class="mono">${inr(p.amount, 2)}</b></div>`).join('')}
        <div class="ph-list">${[[MS.cfg?.scores_url, 'Complain to SEBI on SCORES'], [MS.cfg?.odr_url, 'Online dispute resolution (SMART ODR)']].filter(x => x[0]).map(([u, l]) => `<a class="ph-row" href="${esc(u)}" target="_blank" rel="noopener">${esc(l)}<span aria-hidden="true">↗</span></a>`).join('')}</div>
      </div>`;
    },
  },
  holdings: {
    load: () => portal(),
    html: v => `<div class="ph-pg">${acctBar(v)}<h1>Holdings</h1><p class="dim">At ${fmtDate(MS.day())} prices · ${inr(v.holdings_value)} in all</p>
      <div class="ph-list">${v.holdings.map(h => `<div class="ph-cl"><span><b>${esc(h.symbol || h.isin)}</b><small class="mono">${nf(h.qty)} × ${inr(h.ltp, 2)}${num(h.pledged) ? ' · ' + nf(h.pledged) + ' pledged' : ''}${v.holding_totals?.length > 0 && (v.account.demats || []).length > 1 ? ' · ' + esc(h.account || '') : ''}</small></span><span class="ph-cr"><span class="mono">${inr(h.value)}</span></span></div>`).join('') || empty('No shares in your demat account yet.')}</div>
      ${(v.positions || []).length ? `<h2 class="m-h2">Open positions</h2><div class="ph-list">${v.positions.map(p => `<div class="ph-cl"><span><b>${esc(p.name || p.instrument)}</b><small class="mono">${esc(p.exchange)} ${esc(p.segment)} · ${nf(p.qty)} @ ${inr(p.avg_price, 2)}</small></span><span class="ph-cr"><span class="mono ${sgn(p.mtm)}">${inr(p.mtm, 2)}</span><small class="dim">MTM</small></span></div>`).join('')}</div>` : ''}
      <div class="ph-list">${notTrading(v) ? '' : linkRow('Pledge shares as margin', 'pledge') + linkRow('Authorise a sale (CDSL TPIN)', 'edis')}</div></div>`,
  },
  ledger: {
    tab: 'more', back: 'more', load: () => portal(),
    html: v => `<div class="ph-pg">${acctBar(v)}<h1>Ledger</h1><p class="dim">Balance ${inr(v.ledger, 2)} · the last ${v.ledger_lines.length} entries</p>
      <div class="ph-list">${v.ledger_lines.slice().reverse().map(l => `<div class="ph-cl"><span><b>${esc(l.narration || l.kind || '')}</b><small>${fmtDate(l.date || l.value_date)}</small></span><span class="ph-cr"><span class="mono ${num(l.credit) > 0 ? 'pos' : ''}">${num(l.credit) > 0 ? '+' + inr(l.credit, 2) : '−' + inr(l.debit, 2).replace('−', '')}</span></span></div>`).join('') || empty('No entries yet.')}</div>
      <p class="dim small">Your full statement of account is under Reports, as a signed PDF.</p></div>`,
  },
  funds: {
    load: () => portal(true),
    html: v => {
      const w = v.withdrawable || {}, bl = v.balances, banks = (v.account.banks || [v.account.bank]).filter(x => (x.status || 'Active') === 'Active');
      return `<div class="ph-pg">${acctBar(v)}<h1>Funds</h1>
        ${facts([row('Ledger credit', inr(w.ledger, 2), 'mono'), row('Margin kept', inr(w.margin, 2), 'mono'), num(w.uncleared) ? row('Uncleared cheques', inr(w.uncleared, 2), 'mono') : '', num(w.pending) ? row('Payouts in process', inr(w.pending, 2), 'mono') : '', row('<b>You can withdraw</b>', `<b>${inr(w.available, 2)}</b>`, 'mono pos')])}
        ${w.blocked ? `<div class="m-warn">${esc(w.blocked_why)}</div>` : `<form id="m-po" class="m-form"><h2 class="m-h2">Withdraw to your bank</h2>
          ${bl ? `<label class="m-f">From<select class="in" name="ledger"><option value="">Equity money (${inr(bl.equity, 2)})</option><option value="MCX">MCX money (${inr(bl.mcx, 2)})</option></select></label>` : ''}
          <label class="m-f">Amount (₹)<input class="in mono" name="amount" inputmode="decimal" required></label>
          ${banks.length > 1 ? `<label class="m-f">To<select class="in" name="to">${banks.map((x, i) => `<option value="${i}">${x.primary ? 'Primary: ' : ''}${esc(x.bank)} · ${esc(x.account)}</option>`).join('')}</select></label>` : `<p class="dim small">To ${esc(v.account.bank.bank)} · ${esc(v.account.bank.account)}</p>`}
          <div id="m-poerr"></div><button class="btn pri m-wide">Request payout</button><p class="dim small">Requests received by the cut-off are paid the next working day.</p></form>`}
        <div class="ph-list">${linkRow('Add money', 'addmoney', 'UPI or bank transfer from your own account')}</div>
        <h2 class="m-h2">Payout requests</h2><div class="ph-list">${v.payouts.map(p => `<div class="ph-cl"><span><b class="mono">${inr(p.amount, 2)}</b><small>${esc(p.id)} · ${fmtDate(p.date)}${p.utr ? ' · UTR ' + esc(p.utr) : ''}${p.reason ? ' · ' + esc(p.reason) : ''}</small></span><span class="ph-cr">${pill(p.status, PO_ST[p.status])}</span></div>`).join('') || empty('No payout requests.')}</div></div>`;
    },
    after: v => {
      const f = $('m-po'); if (!f) return;
      f.onsubmit = async e => {
        e.preventDefault(); const x = Object.fromEntries(new FormData(f)), banks = (v.account.banks || [v.account.bank]).filter(b => (b.status || 'Active') === 'Active'), to = banks[+x.to || 0];
        try {
          const r = await post('/v1/payouts', { date: MS.day(), ucc: v.account.ucc, amount: String(x.amount).replace(/,/g, ''), ...(x.ledger ? { ledger: x.ledger } : {}), ...(x.to && +x.to > 0 ? { to_account: to.account, to_ifsc: to.ifsc, to_reason: 'Chosen by the client in the app' } : {}) });
          toast(`Payout ${r.id} requested for ${inr(r.amount, 2)}`); portal(true); go('funds', { keep: true });
        } catch (err) { $('m-poerr').innerHTML = errBox(err); }
      };
    },
  },
  addmoney: {
    tab: 'funds', back: 'funds', load: () => portal(),
    html: v => { const f = v.firm || {}, b = v.account.bank; return `<div class="ph-pg"><h1>Add money</h1><p>Pay only from your registered bank account <b>${esc(b.bank)} · ${esc(b.account)}</b>. Money from any other account is returned.</p>
      ${f.upi_ids ? `<section class="ph-glance"><h2>Pay by UPI</h2>${f.upi_qr ? `<img class="m-qr" src="${esc(f.upi_qr)}" alt="SEBI-validated UPI QR code of ${esc(f.name)}">` : ''}${f.upi_ids.split(',').map(id => `<p class="mono m-upi"><b>${esc(id)}</b> <button class="linkish small" data-act="copy" data-id="${esc(id)}">Copy</button></p>`).join('')}
        <p class="dim small">Check that the ID ends in @valid with our bank’s name and shows the green thumbs-up (SEBI Check) in your UPI app. Do not pay to any other UPI ID in our name.</p></section>` : ''}
      ${f.upi_dp_ids ? `<section class="ph-glance"><h2>DP charges</h2><p class="small">Pay your demat account’s bills to our depository participant’s ID:</p>${f.upi_dp_ids.split(',').map(id => `<p class="mono"><b>${esc(id)}</b></p>`).join('')}</section>` : ''}
      <section class="ph-glance"><h2>Or transfer by NEFT, RTGS or IMPS</h2><dl>${row('Account name', esc(f.name) + ' – Client Account', 'm-plain')}${row('Account', esc(f.payin_account || 'on your welcome letter'), 'mono')}${row('Reference', esc(v.account.ucc), 'mono')}</dl></section>
      <p class="dim small">Credits are matched to your account from our bank statement. Cheques are credited once cleared.</p></div>`; },
  },
  documents: {
    load: () => portal(true),
    html: v => `<div class="ph-pg">${acctBar(v)}<h1>Reports</h1>
      <h2 class="m-h2">Contract notes</h2><div class="ph-list">${v.notes.slice(0, 40).map(n => `<div class="ph-cl"><span><b>${fmtDate(n.date)}</b><small class="mono">${esc(n.no)} · ${n.trades} trades · ${inr(n.net, 2)}</small></span><span class="ph-cr">${n.dispatch ? docBtn('/v1/portal/documents/' + n.dispatch, 'ContractNote_' + n.no.replace(/[\/ ]/g, '-') + '.pdf', 'PDF') : '<small class="dim">signed copy soon</small>'}</span></div>`).join('') || empty('No contract notes yet.')}</div>
      <h2 class="m-h2">Statements</h2><div class="ph-list">${v.documents.filter(d => d.kind !== 'CN').slice(0, 40).map(d => `<div class="ph-cl"><span><b>${esc(d.title)}</b><small>${esc(d.ref)} · ${fmtDate(d.date)}</small></span><span class="ph-cr">${d.signed ? docBtn('/v1/portal/documents/' + d.id, d.kind + '_' + d.ref.replace(/[^0-9A-Za-z-]/g, '_') + '.pdf', 'PDF') : ''}</span></div>`).join('') || empty('No statements yet.')}</div>
      <h2 class="m-h2">Tax</h2><div class="ph-list">${mcFYs().map(fy => { const [f, t] = mcFYRange(fy), u = encodeURIComponent(v.account.ucc); return `<div class="ph-cl"><span><b>Year-end statement ${esc(fy)}</b><small>STT paid, capital gains with the rates, Schedule 112A</small></span><span class="ph-cr">${docBtn('/v1/year-end/' + fy + '/' + u + '/document?date=' + MS.day(), 'YearEnd_' + fy + '.html', 'Open')}</span></div><div class="ph-cl"><span><b>Capital gains ${esc(fy)}</b><small>Lot by lot, grandfathered to 31 Jan 2018; shares, ETFs, bonds and fund units</small></span><span class="ph-cr">${docBtn('/v1/client-reports/pnl?ucc=' + u + '&from=' + f + '&to=' + t + '&format=html', 'CapitalGains_' + fy + '.html', 'Open')}</span></div>`; }).join('')}</div>
      <p class="dim small">PDFs are digitally signed by us. Share saves them to Files, mail or WhatsApp. Earlier years and tax reports are on the website.</p>
      <a class="ph-row" href="/#statements">Reports & statements on the website<span aria-hidden="true">›</span></a></div>`,
  },
  ipo: {
    tab: 'more', back: 'more',
    load: async () => { const v = await portal(); return { v, d: await get('/v1/portal/ipo?ucc=' + encodeURIComponent(v.account.ucc)) }; },
    html: ({ v, d }) => `<div class="ph-pg">${acctBar(v)}<h1>IPOs</h1><p class="dim">Apply with a UPI mandate from your own bank account: the money is only blocked, and released if shares are not allotted.</p>
      ${d.issues.map(i => `<div class="ph-card info"><span class="ph-k">${esc(i.type)} · ${esc(i.exchanges.join(', '))}<span>closes ${fmtDate(i.close)} ${esc(i.close_time)}</span></span><b>${esc(i.name)}</b><span class="ph-sub">₹${esc(i.price_low)}${i.price_high !== i.price_low && num(i.price_high) ? '–' + esc(i.price_high) : ''} · lot ${nf(i.lot_size)}</span>
        ${i.bid ? `<span class="ph-sub">You applied: ${esc(i.bid.id)} · ${inr(i.bid.amount)} · ${esc(i.bid.status)}</span>` : i.can_apply ? `<button class="btn pri sm" data-act="ipoApply" data-id="${esc(i.id)}">Apply</button>` : i.why ? `<span class="ph-sub dim">${esc(i.why)}</span>` : ''}</div>`).join('') || empty('No issue is open for applications now.')}
      <h2 class="m-h2">Your applications</h2><div class="ph-list">${d.bids.map(b => `<div class="ph-cl"><span><b>${esc(b.issue)}</b><small>${esc(b.id)} · ${inr(b.amount)}${b.allotted_shares ? ' · ' + nf(b.allotted_shares) + ' allotted' : ''}</small></span><span class="ph-cr">${pill(b.status)}${d.own && b.issue_id && ['Draft', 'Uploaded', 'Mandate pending', 'Accepted'].includes(b.status) && !b.pending?.startsWith('Cancel') && !b.sent?.startsWith('Cancel') ? `<button class="linkish small" data-act="ipoCancel" data-id="${esc(b.id)}">Withdraw</button>` : ''}</span></div>`).join('') || empty('No applications yet.')}</div>
      <p class="dim small">Bids close at 4 pm on the closing day; approve the UPI mandate by 5 pm.</p></div>`,
    after: x => { CA.ipo = x.d; },
  },
  mf: {
    tab: 'more', back: 'more',
    load: async () => { const v = await portal(); return { v, mf: await get('/v1/portal/mf?ucc=' + encodeURIComponent(v.account.ucc)) }; },
    html: ({ v, mf }) => { const ready = mf.ready.length > 0 && !mf.missing.length; return `<div class="ph-pg">${acctBar(v)}<h1>Mutual funds</h1>
      ${mf.missing.length ? `<div class="m-warn"><b>Before you can invest:</b> ${mf.missing.map(esc).join(' · ')}</div>` : ''}
      <div class="m-tiles">${tile('Value of your units', inr(mf.value, 2), mf.holdings.length + ' holdings')}${tile('SIPs', nf(mf.sips.filter(s => s.status === 'Active').length), 'active')}</div>
      ${ready ? `<form id="m-mf" class="m-form"><h2 class="m-h2">Buy</h2><label class="m-f">Scheme<input class="in" name="q" id="m-mfq" placeholder="Type a fund’s name" autocomplete="off"></label><div id="m-mfres"></div><input type="hidden" name="isin" id="m-mfisin">
        <label class="m-f">Platform<select class="in" name="platform">${mf.ready.map(x => `<option>${esc(x)}</option>`).join('')}</select></label><label class="m-f">Amount (₹)<input class="in mono" name="amount" inputmode="decimal" required></label><div id="m-mferr"></div><button class="btn pri m-wide">Place order</button><p class="dim small">Cut-offs: ${esc(mf.cutoffs)}.</p></form>` : ''}
      <h2 class="m-h2">Your units</h2><div class="ph-list">${mf.holdings.map(h => `<div class="ph-cl"><span><b>${esc(h.scheme)}</b><small class="mono">${esc(h.units)} units · NAV ${esc(h.nav)}</small></span><span class="ph-cr"><span class="mono">${inr(h.value, 2)}</span></span></div>`).join('') || empty('No units yet.')}</div>
      <h2 class="m-h2">Orders and SIPs</h2><div class="ph-list">${[...mf.orders.slice(0, 10).map(o => `<div class="ph-cl"><span><b>${esc(o.scheme)}</b><small>${esc(o.type)} · ${fmtDate(o.date)}</small></span><span class="ph-cr">${pill(o.status)}</span></div>`), ...mf.sips.map(s => `<div class="ph-cl"><span><b>${esc(s.scheme)}</b><small>SIP ${inr(s.amount)} ${esc(String(s.frequency || '').toLowerCase())}</small></span><span class="ph-cr">${pill(s.display || s.status)}</span></div>`)].join('') || empty('No orders yet.')}</div>
      <a class="ph-row" href="/#pmf">SIPs, redemptions and nominations on the website<span aria-hidden="true">›</span></a></div>`; },
    after: ({ v }) => {
      const q = $('m-mfq'); if (!q) return;
      q.oninput = () => { clearTimeout(CA.mft); CA.mft = setTimeout(async () => { const t = q.value.trim(); if (t.length < 3) { $('m-mfres').innerHTML = ''; return; } try { const xs = await get('/v1/portal/mf/schemes?q=' + encodeURIComponent(t)); $('m-mfres').innerHTML = `<div class="m-pick">${xs.slice(0, 8).map(s => `<button type="button" data-act="mfPick" data-id="${esc(s.isin)}" data-name="${esc(s.name)}">${esc(s.name)} <small class="dim">${esc(s.plan)}</small></button>`).join('') || '<p class="dim small">No scheme matches.</p>'}</div>`; } catch (e) {} }, 300); };
      $('m-mf').onsubmit = async e => { e.preventDefault(); const f = Object.fromEntries(new FormData(e.target)); if (!f.isin) { $('m-mferr').innerHTML = errBox('Find and choose a scheme'); return; }
        try { const r = await post('/v1/portal/mf/orders', { ucc: v.account.ucc, platform: f.platform, type: 'Purchase', isin: f.isin, amount: String(f.amount).replace(/,/g, '') }); toast(`Order ${r.order_no || r.id} placed: authorise and pay with the exchange’s link`); go('mf', { keep: true }); } catch (err) { $('m-mferr').innerHTML = errBox(err); } };
    },
  },
  pledge: {
    tab: 'holdings', back: 'holdings',
    load: async () => { const v = await portal(); return { v, d: await get('/v1/portal/pledges?ucc=' + encodeURIComponent(v.account.ucc)) }; },
    html: ({ v, d }) => `<div class="ph-pg">${acctBar(v)}<h1>Pledge shares</h1><p class="dim">Pledge shares as margin with us. They stay in your account. After we approve, you confirm on CDSL’s page with the OTP CDSL sends.</p>
      ${d.connected ? '' : '<div class="m-warn">Pledging through CDSL is not available just now.</div>'}
      ${d.awaiting ? `<div class="ph-card info"><b>${d.awaiting} approved pledge${d.awaiting > 1 ? 's' : ''} waiting for you</b><button class="btn pri" data-act="pledgeConfirm">Confirm with CDSL</button></div>` : ''}
      <div class="ph-list">${d.holdings.map(h => `<div class="ph-cl"><span><b>${esc(h.symbol)}</b><small class="mono">${nf(h.qty)} held · ${nf(h.pledged)} pledged · ${nf(h.free)} free</small></span><span class="ph-cr">${Math.floor(num(h.free)) > 0 && d.connected ? `<button class="btn sm" data-act="pledgeAsk" data-id="${esc(h.isin)}" data-name="${esc(h.bo_id || '')}|${Math.floor(num(h.free))}">Pledge</button>` : ''}</span></div>`).join('') || empty('No shares in your demat account.')}</div>
      ${d.psns.length ? `<h2 class="m-h2">Pledged as margin</h2><div class="ph-list">${d.psns.map(a => `<div class="ph-cl"><span><b>${esc(a.symbol)}</b><small class="mono">PSN ${esc(a.psn)} · ${nf(a.free)}</small></span><span class="ph-cr"><button class="btn sm" data-act="unpledge" data-id="${esc(a.psn)}" data-name="${a.free}">Release</button></span></div>`).join('')}</div>` : ''}
      ${d.requests.length ? `<h2 class="m-h2">Your requests</h2><div class="ph-list">${d.requests.map(p => `<div class="ph-cl"><span><b>${nf(p.qty)} ${esc(p.symbol)}</b><small>${esc(p.id)}${p.reason ? ' · ' + esc(p.reason) : ''}</small></span><span class="ph-cr">${pill(p.status)}</span></div>`).join('')}</div>` : ''}</div>`,
  },
  edis: {
    tab: 'holdings', back: 'holdings',
    load: async () => { const v = await portal(); return { v, p: await get('/v1/portal/edis?ucc=' + encodeURIComponent(v.account.ucc)) }; },
    html: ({ v, p }) => !p.on ? `<div class="ph-pg"><h1>Authorise a sale</h1><div class="m-warn">${esc(p.problem || 'Not available.')}</div></div>` : `<div class="ph-pg">${acctBar(v)}<h1>Authorise a sale</h1><p class="dim">Before you sell shares from your demat account, authorise the sale on CDSL’s page with your CDSL TPIN and the OTP CDSL sends. It lasts ${p.valid_days} day${p.valid_days > 1 ? 's' : ''}.</p>
      ${p.ddpi ? '<div class="m-msg">You have given a DDPI, so you need not authorise each sale.</div>' : ''}${p.problem ? `<div class="m-warn">${esc(p.problem)}</div>` : ''}
      <form id="m-edis" class="m-form">${p.exchanges.length > 1 ? `<label class="m-f">Exchange<select class="in" name="exchange">${p.exchanges.map(e => `<option>${esc(e)}</option>`).join('')}</select></label>` : `<input type="hidden" name="exchange" value="${esc(p.exchanges[0] || 'NSE')}">`}
        ${p.holdings.map(h => `<label class="m-f m-fr"><span><b>${esc(h.symbol || h.isin)}</b><small class="dim">${nf(h.free)} free${h.authorised ? ' · ' + nf(h.authorised) + ' authorised' : ''}</small></span><input class="in mono" name="q:${esc(h.isin)}" inputmode="numeric" placeholder="0" aria-label="Quantity of ${esc(h.symbol || h.isin)}"></label>`).join('') || empty('No shares to authorise.')}
        <div id="m-edierr"></div><button class="btn pri m-wide" ${p.problem ? 'disabled' : ''}>Authorise on CDSL</button></form>
      <div class="ph-list">${linkRow('Get a new CDSL TPIN', 'edis:tpin')}</div></div>`,
    after: ({ v, p }, arg) => {
      if (arg === 'tpin') { ACT.tpin(); }
      const f = $('m-edis'); if (!f) return;
      f.onsubmit = async e => { e.preventDefault(); const x = Object.fromEntries(new FormData(f)); const lines = Object.entries(x).filter(([k, q]) => k.startsWith('q:') && parseInt(q, 10) > 0).map(([k, q]) => ({ isin: k.slice(2), qty: parseInt(q, 10) }));
        if (!lines.length) { $('m-edierr').innerHTML = errBox('Enter the quantity of at least one share.'); return; }
        try { const r = await post('/v1/portal/edis', { ucc: v.account.ucc, exchange: x.exchange, lines }); toCDSL(r.form); } catch (err) { $('m-edierr').innerHTML = errBox(err); } };
    },
  },
  profile: {
    tab: 'more', back: 'more', load: () => portal(true),
    html: v => { const a = v.account, pend = k => v.changes.find(c => c.kind === k && (c.status === 'Pending' || c.status === 'Waiting for the client'));
      const r = (k, label, val) => row(label, `${val}<br>${pend(k) ? pill('Change pending', 'warn') : `<button class="linkish small" data-act="change" data-id="${k}">Change</button>`}`);
      return `<div class="ph-pg">${acctBar(v)}<h1>Profile</h1>${facts([row('Name', esc(a.name)), row('PAN', esc(a.pan), 'mono'), r('Email', 'Email', esc(a.email)), r('Mobile', 'Mobile', esc(a.mobile)), r('Address', 'City', esc(a.city)),
        row('Bank', (a.banks || [a.bank]).filter(x => (x.status || 'Active') === 'Active').map(x => `${esc(x.bank)} · <span class="mono">${esc(x.account)}</span>`).join('<br>')), row('Demat', esc(a.demat.depository) + ' <span class="mono">' + esc(a.demat.id) + '</span>'), row('Nomination', esc(a.nominee || '—'))])}
        <p class="dim small">Changes are checked by our head office before they take effect and are reported to the KYC registration agency. Change bank, demat, nomination or tax residency on the website.</p>
        <a class="ph-row" href="/#pprofile">Profile and documents on the website<span aria-hidden="true">›</span></a>
        <h2 class="m-h2">Your change requests</h2><div class="ph-list">${v.changes.map(c => `<div class="ph-cl"><span><b>${esc(c.kind)}</b><small>${esc(c.id)} · ${fmtDate(c.at)}${c.reason ? ' · ' + esc(c.reason) : ''}</small></span><span class="ph-cr">${pill(c.status, c.status === 'Approved' ? 'good' : c.status === 'Rejected' ? 'bad' : 'warn')}${c.needs_otp && !c.verified_at && c.status === 'Pending' ? `<button class="linkish small" data-act="verify" data-id="${esc(c.id)}">Enter code</button>` : ''}</span></div>`).join('') || empty('No change requests.')}</div></div>`; },
  },
  help: {
    tab: 'more', back: 'more',
    load: async () => { const v = await portal(); return { v, t: await get('/v1/tickets?date=' + MS.day()) }; },
    html: ({ v, t }) => `<div class="ph-pg"><h1>Help desk</h1><p class="dim">Complaints are answered within 21 days; requests and questions within 7.</p><button class="btn pri m-wide" data-act="newTicket">New request or complaint</button>
      <div class="ph-list">${t.tickets.map(k => `<div class="ph-card info"><span class="ph-k">${esc(k.kind)} · ${esc(k.id)}<span>${pill(k.status, k.status === 'Resolved' || k.status === 'Closed' ? 'good' : 'warn')}</span></span><b>${esc(k.subject)}</b>
        ${k.messages.slice(-3).map(m => `<span class="ph-sub"><b>${m.client ? 'You' : 'Support'}:</b> ${esc(m.text)}</span>`).join('')}${k.status !== 'Closed' ? `<button class="btn sm" data-act="reply" data-id="${esc(k.id)}">Reply</button>` : ''}</div>`).join('') || empty('You have not raised anything yet.')}</div>
      <p class="dim small">Not resolved? You can complain to SEBI on SCORES, or start online dispute resolution, from More → About us.</p></div>`,
    after: d => { CA.tk = d.t; },
  },
  notifications: {
    tab: 'more', back: 'more', load: () => get('/v1/mobile/notifications'),
    html: d => `<div class="ph-pg"><h1>Notifications</h1><p class="dim">Notifications say only that there is something to see; open the app for the details.</p>
      <div class="ph-list">${d.notifications.map(n => `<button class="ph-card" data-go="${esc(n.route)}"><span class="ph-k">${esc(n.title)}<span>${fmtDate(n.at)} ${esc((n.at || '').slice(11, 16))}</span></span><span class="ph-sub">${esc(n.body)}</span></button>`).join('') || empty('No notifications yet.')}</div></div>`,
  },
  accounts: {
    tab: 'more', back: 'more', load: () => portal(),
    html: v => `<div class="ph-pg"><h1>Accounts</h1><p class="dim">The accounts you may see with this login: your own and your family’s.</p>
      <div class="ph-list">${v.accounts.map(a => `<button class="ph-cl" data-act="switchAcct" data-id="${esc(a.ucc)}"><span><b>${esc(a.name)}</b><small class="mono">${esc(a.ucc)}</small></span><span class="ph-cr">${a.ucc === v.account.ucc ? pill('Showing', 'good') : ''}</span></button>`).join('')}</div></div>`,
  },
  about: {
    tab: 'more', back: 'more',
    html: () => { const c = MS.cfg || {}, b = c.brand || {}, codes = Object.entries(c.member_codes || {}).map(([k, v]) => `${esc(k)} ${esc(v)}`).join(' · '), link = (u, l) => u ? `<a class="ph-row" href="${esc(u)}" target="_blank" rel="noopener">${esc(l)}<span aria-hidden="true">↗</span></a>` : '';
      return `<div class="ph-pg"><h1>About us</h1>${facts([row('Name', esc(c.firm)), c.sebi_reg && row('SEBI registration (stock broker)', esc(c.sebi_reg), 'mono'), b.dp_reg && row('SEBI registration (depository participant)', esc(b.dp_reg), 'mono'), codes && row('Exchange member codes', codes, 'mono'), b.registered_office && row('Registered office', esc(b.registered_office)),
          b.support_email && row('Support', `${esc(b.support_email)}${b.support_phone ? '<br>' + esc(b.support_phone) : ''}`), (b.grievance_email || c.compliance_email) && row('Grievances', `${esc(b.grievance_officer || '')}${b.grievance_officer ? '<br>' : ''}${esc(b.grievance_email || c.compliance_email)}${b.grievance_phone ? '<br>' + esc(b.grievance_phone) : ''}`), b.compliance_officer && row('Compliance officer', esc(b.compliance_officer))])}
        <div class="ph-list">${link(b.investor_charter_url, 'Investor charter')}${link(b.risk_disclosure_url, 'Risk disclosures')}${link(b.website_url, 'Our website')}
          ${link(c.scores_url, 'SEBI SCORES: complain to SEBI')}${link(c.odr_url, 'Online dispute resolution (SMART ODR)')}</div>
        <div class="m-msg small"><b>Risk disclosure.</b> 9 out of 10 individual traders in equity futures and options made losses (SEBI study, FY 2021-22 to 2023-24). Investments in securities are subject to market risks; read all related documents carefully before investing.</div>
        <p class="dim small">If you are not satisfied with our answer to a complaint, you can raise it with SEBI on SCORES, and then start online conciliation or arbitration on the SMART ODR portal.</p></div>`; },
  },
  more: {
    html: () => `<div class="ph-pg"><h1>More</h1><div class="m-who"><b>${esc(CA.v?.account?.name || MS.me?.actor || '')}</b><small class="mono">${esc(CA.v?.account?.ucc || '')}</small></div>
      <div class="ph-list">${linkRow('Ledger', 'ledger')}${CA.v && notTrading(CA.v) ? '' : linkRow('IPOs', 'ipo') + linkRow('Mutual funds', 'mf') + linkRow('Pledge shares', 'pledge') + linkRow('Authorise a sale (CDSL TPIN)', 'edis')}
        ${linkRow('Profile', 'profile')}${linkRow('Help desk', 'help')}${linkRow('Notifications', 'notifications')}${(CA.v?.accounts || []).length > 1 ? linkRow('Family accounts', 'accounts') : ''}${linkRow('About us', 'about', 'SEBI registration, investor charter, grievances, SCORES, ODR')}${linkRow('My phones', 'phones')}
        <a class="ph-row" href="/">The full website<span aria-hidden="true">›</span></a></div>
      <button class="btn m-wide" data-act="signOut">Sign out</button>${MS.device ? '<button class="btn bad m-wide" data-act="removePhone">Remove this phone</button>' : ''}
      <p class="dim small">The app locks after ${esc(MS.cfg?.idle_minutes)} minutes without use. Screens cannot be captured, and notifications never show your account details.</p></div>`,
  },
  phones: MS.phonesScreen(),
};
// "About us" is open before sign-in too (the disclosures must be reachable).
const baseGo = go;
go = async (route, opts) => { if (MS.locked && String(route).startsWith('about')) { MS.locked = false; await baseGo(route, opts); MS.locked = true; $('m-tabs').hidden = true; $('m-top').innerHTML = '<button class="ph-back" data-act="reload">‹ Back to sign-in</button>'; return; } return baseGo(route, opts); };

// The CDSL page (eDIS and pledge confirmations) is a form posted to CDSL,
// which returns to this app.
function toCDSL(f) {
  const fm = document.createElement('form'); fm.method = 'post'; fm.action = f.action; fm.style.display = 'none';
  for (const [k, v] of Object.entries(f.fields || {})) { const i = document.createElement('input'); i.type = 'hidden'; i.name = k; i.value = v; fm.appendChild(i); }
  document.body.appendChild(fm); fm.submit();
}

const CHG = { Email: [['email', 'New email', 'email']], Mobile: [['mobile', 'New mobile', 'tel']], Address: [['city', 'City'], ['state', 'State']] };
Object.assign(ACT, {
  share: (path, el) => Shell.share(path, el.dataset.name).catch(e => toast(e.message, true)),
  copy: id => { navigator.clipboard?.writeText(id).then(() => toast('Copied'), () => toast(id)); },
  switchAcct: ucc => { CA.acct = ucc; CA.v = null; go('home'); },
  mfPick: (isin, el) => { $('m-mfisin').value = isin; $('m-mfq').value = el.dataset.name; $('m-mfres').innerHTML = ''; },
  ipoApply: id => {
    const i = CA.ipo.issues.find(x => x.id === id); if (!i) return;
    sheet('Apply · ' + i.name, `<label class="m-f">Category<select class="in" name="category">${i.choices.map(c => `<option value="${esc(c.code)}">${esc(c.name)} (${c.min_lots}${c.max_lots ? '–' + c.max_lots : '+'} lots)</option>`).join('')}</select></label>
      <label class="m-f">Exchange<select class="in" name="exchange">${i.exchanges.map(x => `<option>${esc(x)}</option>`).join('')}</select></label><label class="m-f">Lots<input class="in mono" name="lots" inputmode="numeric" value="${(i.choices[0] || {}).min_lots || 1}"></label>
      ${i.type === 'NCD' ? `<label class="m-f">Series<select class="in" name="series">${(i.series || ['']).map(s => `<option>${esc(s)}</option>`).join('')}</select></label>` : `<label class="m-f">Price (₹)<input class="in mono" name="price" inputmode="decimal" value="${esc(i.price_high || i.price_low)}"></label><label class="m-check"><input type="checkbox" name="cut" ${(i.choices[0] || {}).cut_off ? 'checked' : ''}> Bid at the cut-off price</label>`}
      <label class="m-f">Your UPI ID<input class="in mono" name="upi" value="${esc((CA.ipo.upis[0] || {}).vpa || '')}" autocapitalize="none" required></label><label class="m-check"><input type="checkbox" name="own" required> This UPI ID is linked to my own bank account</label>`, 'Apply', async f => {
      const b = await post('/v1/portal/ipo/bids', { issue: id, ucc: CA.v.account.ucc, category: f.category, exchange: f.exchange, options: [{ lots: parseInt(f.lots, 10) || 0, cut_off: !!f.cut, price: f.cut ? '0' : (f.price || '0'), series: f.series || '' }], upi: f.upi.trim(), own_upi: !!f.own, declared: false });
      toast(`Applied: ${b.id}. Approve the mandate in your UPI app.`); go('ipo', { keep: true });
    });
  },
  ipoCancel: id => sheet('Withdraw your application', '<p>The blocked money is released by your bank once the exchange confirms.</p>', 'Withdraw', async () => { await post(`/v1/portal/ipo/bids/${encodeURIComponent(id)}/cancel`); toast('Withdrawal sent'); go('ipo', { keep: true }); }, true),
  pledgeAsk: (isin, el) => { const [bo, max] = el.dataset.name.split('|'); sheet('Pledge as margin', `<label class="m-f">Shares to pledge (up to ${esc(max)})<input class="in mono" name="qty" inputmode="numeric" value="${esc(max)}" required></label><p class="dim small">We check the request; then you confirm it with CDSL’s OTP.</p>`, 'Ask', async f => { await post('/v1/portal/pledges', { ucc: CA.v.account.ucc, kind: 'MP', lines: [{ isin, qty: parseInt(f.qty, 10) || 0, bo_id: bo }] }); toast('Request sent; we will tell you when to confirm it'); go('pledge', { keep: true }); }); },
  pledgeConfirm: async () => { try { const r = await post('/v1/portal/pledges/confirm', { ucc: CA.v.account.ucc }); window.location.assign(r.url); } catch (e) { toast(e.message, true); } },
  unpledge: (psn, el) => sheet('Release from pledge', `<label class="m-f">Shares to release (up to ${esc(el.dataset.name)})<input class="in mono" name="qty" inputmode="numeric" required></label><p class="dim small">Shares still needed as margin may not be released.</p>`, 'Ask', async f => { await post('/v1/portal/pledges/unpledge', { ucc: CA.v.account.ucc, psn, qty: parseInt(f.qty, 10) || 0 }); toast('Release requested'); go('pledge', { keep: true }); }),
  tpin: () => sheet('Get a new CDSL TPIN', '<p>CDSL sends it to the mobile number and email registered with your demat account.</p>', 'Send me a TPIN', async () => { await post('/v1/portal/edis/tpin', { ucc: CA.v.account.ucc }); toast('CDSL is sending your TPIN'); }),
  change: kind => sheet('Change ' + kind.toLowerCase(), CHG[kind].map(([k, l, t]) => `<label class="m-f">${esc(l)}<input class="in" name="${k}" type="${t || 'text'}" required></label>`).join('') + (kind === 'Address' ? '<label class="m-f">Address proof (Aadhaar, passport, utility bill) · PDF, JPEG or PNG<input class="in" type="file" name="doc" accept="application/pdf,image/jpeg,image/png" required></label>' : '') + '<p class="dim small">Checked by our head office before it takes effect.</p>', 'Submit', async f => {
    const ucc = CA.v.account.ucc, values = {}; CHG[kind].forEach(([k]) => { values[k] = f[k]; });
    let doc = '';
    if (f.doc && f.doc.size) { const d = await api('POST', `/v1/clients/${encodeURIComponent(ucc)}/documents?kind=${encodeURIComponent('Address proof')}&name=${encodeURIComponent(f.doc.name)}`, f.doc, true); doc = d.id; }
    const r = await post('/v1/changes', { ucc, kind, values, doc });
    CA.v = null; if (r.needs_otp) { setTimeout(() => ACT.verify(r.id, null, r.dev_code), 50); } else toast(`Request ${r.id} submitted`);
    go('profile', { keep: true });
  }),
  verify: (id, el, dev) => sheet('Enter the code', `<p>We sent a 6-digit code to your new contact. It is valid for 15 minutes.</p><label class="m-f">Code<input class="in mono" name="code" inputmode="numeric" maxlength="6" required></label>${dev ? `<p class="dim small">Development server: the code is ${esc(dev)}</p>` : ''}`, 'Confirm', async f => { await post(`/v1/changes/${encodeURIComponent(id)}/verify`, { code: f.code }); toast('Confirmed'); CA.v = null; go('profile', { keep: true }); }),
  newTicket: () => { const c = CA.tk; sheet('New request or complaint', `${CA.v.accounts.length > 1 ? `<label class="m-f">Account<select class="in" name="ucc">${CA.v.accounts.map(a => `<option value="${esc(a.ucc)}" ${a.ucc === CA.v.account.ucc ? 'selected' : ''}>${esc(a.name)} · ${esc(a.ucc)}</option>`).join('')}</select></label>` : ''}
    <label class="m-f">Type<select class="in" name="kind"><option>Complaint</option><option>Service request</option><option>Query</option></select></label><label class="m-f">Category<select class="in" name="category">${(c.categories || []).map(x => `<option>${esc(x)}</option>`).join('')}</select></label>
    <label class="m-f">Subject<input class="in" name="subject" required></label><label class="m-f">Details<textarea class="in" name="text" rows="4" required></textarea></label>`, 'Submit', async f => { const t = await post('/v1/tickets', { ucc: f.ucc || CA.v.account.ucc, kind: f.kind, category: f.category, subject: f.subject, text: f.text }); toast(`${t.id} raised · reply due ${fmtDate(t.due)}`); go('help', { keep: true }); }); },
  reply: id => sheet('Reply', '<label class="m-f">Your reply<textarea class="in" name="text" rows="4" required></textarea></label>', 'Send', async f => { await post(`/v1/tickets/${encodeURIComponent(id)}`, { text: f.text }); toast('Sent'); go('help', { keep: true }); }),
});

MS.start();
