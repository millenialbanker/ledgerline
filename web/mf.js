'use strict';
/* Ledgerline console: mutual funds. Orders, SIPs, mandates, units,
   trail commission and MF settings for staff; the client portal's
   Mutual funds page. Every figure comes from /v1/mf (internal/ops/mf*.go).
   Loaded after app.js. Client money never passes through the firm: the
   investor pays the clearing corporation through the platform. */

const MF = { tab: 'orders', st: 'All', q: '', ucc: '', month: '', prof: null, sipF: 'Active', imp: null };
const encM = encodeURIComponent;
const mfPill = s => pill(s, { Placed: 'acc', 'Pending at RTA': 'warn', 'Payment pending': 'warn', Allotted: 'good', Redeemed: 'good', Rejected: 'bad', Cancelled: 'nt',
  Active: 'good', Paused: 'warn', Completed: 'nt', 'Registration failed': 'bad', Pending: 'warn', Approved: 'good', Registered: 'good', Submitted: 'warn', Failed: 'bad',
  Matched: 'good', Short: 'bad', Excess: 'warn', 'Not received': 'bad', 'Not expected': 'nt' }[s] || 'nt');
const mfPlat = p => ({ NSE: 'NSE MF Invest', BSE: 'BSE StAR MF' }[p] || p || '—');
const mfUnits = u => num(u) ? nf(u, 3) : '—';
const mfClientCell = (ucc, name) => `<button class="linkish mono" data-act="client" data-id="${esc(ucc)}">${esc(ucc)}</button> ${esc(name || '')}`;
const mfMoneyNote = '<div class="note">Investors pay the clearing corporation directly, by the platform’s payment link or by a mandate in its favour. Nothing is taken from or paid into the trading account, and redemptions are paid by the fund house to the investor’s bank.</div>';

// A scheme search box that fills a list of matching schemes.
function mfSchemePick(id, opt = {}) {
  return `<div class="fld full"><label for="${id}-q">${esc(opt.label || 'Scheme')}</label><input class="in" id="${id}-q" data-mfsch="${id}" data-plat="${esc(opt.platform || '')}" data-plan="${esc(opt.plan || '')}" data-portal="${opt.portal ? 1 : ''}" placeholder="Type a fund house, scheme name or ISIN" autocomplete="off">
    <select class="in" id="${id}" style="margin-top:6px"><option value="">Type above to find schemes</option></select></div>`;
}
let mfSchT;
document.addEventListener('input', e => {
  const el = e.target; if (!el.dataset?.mfsch) return;
  clearTimeout(mfSchT);
  mfSchT = setTimeout(async () => {
    const sel = $(el.dataset.mfsch); if (!sel) return;
    const base = el.dataset.portal ? '/v1/portal/mf/schemes' : '/v1/mf/schemes';
    try {
      const plat = (el.dataset.plat && $(el.dataset.plat)?.value) || '';
      const rs = await get(`${base}?q=${encM(el.value.trim())}&plan=${encM(el.dataset.plan || '')}&platform=${encM(plat)}`);
      sel.innerHTML = rs.length ? rs.map(s => `<option value="${esc(s.isin)}">${esc(s.name)} · ${esc(s.plan)} · NAV ${nf(s.nav, 4)}${s.nav_date ? ' (' + fmtDate(s.nav_date) + ')' : ''}</option>`).join('') : '<option value="">No scheme matches (load the NAV file and the platform’s scheme file)</option>';
    } catch (err) { sel.innerHTML = `<option value="">${esc(err.message)}</option>`; }
  }, 250);
});

/* ---------- MF orders (and the investor profile) ---------- */
V.mforders = {
  load: async () => {
    const [orders, st] = await Promise.all([get('/v1/mf/orders'), get('/v1/mf/status'), clientCache()]);
    let prof = null;
    if (MF.ucc) prof = await get('/v1/mf/clients/' + encM(MF.ucc)).catch(e => ({ error: e.message }));
    return { orders, st, prof };
  },
  html: d => {
    const today = d.orders.filter(o => o.date === S.date || (o.at || '').startsWith(new Date().toISOString().slice(0, 10)));
    const off = !d.st.settings.route;
    return ph('Mutual fund orders', 'Purchases, redemptions and switches through BSE StAR MF and NSE MF Invest, placed here or loaded from the platform’s order file. Each investor’s family invests through one route only: Regular plans (the firm as distributor, with the employee’s EUIN) or Direct plans.',
      `${has('Ops maker', 'Ops checker') && !off ? '<button class="btn pri" data-act="mfOrder">Place order</button>' : ''}<button class="btn" data-act="go" data-id="files">Load MF-ORDERS file</button>`) +
    (off ? '<div class="note warn">Mutual funds are not switched on. Compliance chooses the route (Regular, Direct or both) in MF settings.</div>' : '') +
    `<div class="kpis">${kpi('Orders', d.orders.length, inr(sum(d.orders, x => num(x.amount))))}${kpi('Awaiting payment', d.orders.filter(x => x.status === 'Placed').length, 'investor authorises and pays on the platform', 'warn')}${kpi('With the RTA', d.orders.filter(x => x.status === 'Pending at RTA').length)}${kpi('Allotted / redeemed', d.orders.filter(x => ['Allotted', 'Redeemed'].includes(x.status)).length, '', 'good')}${kpi('Rejected', d.orders.filter(x => x.status === 'Rejected').length, '', d.orders.some(x => x.status === 'Rejected') ? 'bad' : '')}${kpi('Today', today.length)}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('mfOrdTab', [['orders', 'Orders'], ['investor', 'Investor profile']])}${UI.mfOrdTab === 'orders' ? segBtns('mfOrdF', ['All', 'Placed', 'Pending at RTA', 'Allotted', 'Redeemed', 'Rejected', 'Cancelled'], 'mf-ord') + `<input class="in" data-ui="mfOrdQ" data-part="mf-ord" placeholder="Filter client, scheme or order no." value="${esc(UI.mfOrdQ || '')}">` : ''}</div></div><div id="mf-ord">${P['mf-ord'](d)}</div></div>` + mfMoneyNote;
  }
};
UI.mfOrdTab = UI.mfOrdTab || 'orders';
UI.mfOrdF = UI.mfOrdF || 'All';
P['mf-ord'] = d => {
  if (UI.mfOrdTab === 'investor') return mfInvestorHTML(d);
  const mk = has('Ops maker', 'Ops checker');
  const rows = d.orders.filter(x => UI.mfOrdF === 'All' || x.status === UI.mfOrdF).filter(x => !UI.mfOrdQ || (x.ucc + ' ' + x.name + ' ' + x.scheme + ' ' + x.order_no).toLowerCase().includes(UI.mfOrdQ.toLowerCase()));
  return tbl([['Order'], ['Client'], ['Scheme'], ['Type'], ['Amount / units', 'r'], ['Platform'], ['Status'], ['']], rows.map(x => `<tr><td class="n">${esc(x.id)}${x.order_no ? `<div class="dim small mono">${esc(x.order_no)}</div>` : ''}<div class="dim small">${fmtDate(x.date)}${x.source === 'portal' ? ' · portal' : x.source === 'sip' ? ' · SIP' : x.source ? '' : ' · file'}</div></td>
      <td>${mfClientCell(x.ucc, x.name)}</td><td class="wrap">${esc(x.scheme)}${x.to_scheme ? `<div class="dim small">→ ${esc(x.to_scheme)}</div>` : ''}${x.plan ? ` <span class="tag">${esc(x.plan)}</span>` : ''}${x.euin ? `<div class="dim small">EUIN ${esc(x.euin)}</div>` : ''}</td>
      <td>${esc(x.type)}${x.buy_type ? `<div class="dim small">${esc(x.buy_type.toLowerCase())}</div>` : ''}</td><td class="r">${num(x.amount) ? inr(x.amount, 2) : ''}${num(x.units) || x.all_units ? `<div class="small">${x.all_units ? 'all units' : mfUnits(x.units) + ' u'}</div>` : ''}${num(x.allotted_units) ? `<div class="small pos">${mfUnits(x.allotted_units)} u @ ${nf(x.nav, 4)}</div>` : ''}</td>
      <td>${esc(mfPlat(x.platform))}</td><td>${mfPill(x.status)}${x.after_cutoff ? ' <span class="tag">after cut-off</span>' : ''}${x.platform_status ? `<div class="dim small">${esc(x.platform_status)}</div>` : ''}${x.remark ? `<div class="dim small wrap">${esc(x.remark)}</div>` : ''}${x.payment ? `<div class="dim small wrap">${esc(x.payment)}</div>` : ''}</td>
      <td><div class="split">${mk && x.source && ['Placed', 'Pending at RTA'].includes(x.status) ? `<button class="btn sm" data-act="mfAllot" data-id="${esc(x.id)}" data-type="${esc(x.type)}">${x.type === 'Redemption' ? 'Redeemed' : 'Allotted'}</button>` : ''}${mk && x.status === 'Placed' && x.source ? `<button class="btn sm" data-act="mfCancel" data-id="${esc(x.id)}">Cancel</button>` : ''}</div></td></tr>`), { tall: true, empty: 'No mutual fund orders.' });
};

function mfInvestorHTML(d) {
  const mk = has('Ops maker', 'Ops checker'), co = has('Compliance officer');
  const pick = `<div class="bd"><div class="tools"><div style="min-width:320px">${cpick('mf-ucc', MF.ucc, { placeholder: 'Find the investor by name, UCC or PAN' })}</div><button class="btn" data-act="mfPickUCC">Open</button></div></div>`;
  if (!MF.ucc) return pick + '<div class="pad dim">Choose an investor to see the MF profile: KYC, FATCA, folio nomination, platform registrations, mandates and the family’s route.</div>';
  if (!d.prof || d.prof.error) return pick + `<div class="pad">${errHTML({ message: d.prof?.error || 'Not found' })}</div>`;
  const p = d.prof.profile, kraBad = /hold|reject/i.test(p.kra_status || '');
  const regs = d.st.platforms.filter(x => x.enabled).map(x => { const r = p.platforms[x.id] || {}; return `<tr><td>${esc(x.name)}</td><td>${r.status ? mfPill(r.status) : pill('Not registered', 'nt')}${r.remark ? `<div class="dim small wrap">${esc(r.remark)}</div>` : ''}${r.by ? `<div class="dim small">${esc(r.by)} · ${esc((r.at || '').slice(0, 10))}</div>` : ''}</td>
    <td><div class="split">${mk && x.api.some(a => /registration/i.test(a)) ? `<button class="btn sm" data-act="mfRegister" data-id="${x.id}">Send registration</button>` : ''}${mk ? `<button class="btn sm" data-act="mfMarkReg" data-id="${x.id}">Record status</button>` : ''}</div></td></tr>`; });
  return pick + `<div class="grid2"><div class="panel"><div class="hd"><h3>${esc(p.name)} · ${esc(p.ucc)}</h3>${mk || co ? '<button class="btn sm" data-act="mfProfile">Edit</button>' : ''}</div><div class="bd"><div class="kv">
      <div><small>KYC at the KRA</small><b>${kraBad ? pill(p.kra_status, 'bad') : esc(p.kra_status || '—')}</b></div><div><small>FATCA</small><b>${p.fatca ? 'Declared ' + fmtDate(p.fatca_on) : pill('Not declared', 'bad')}</b></div>
      <div><small>Units held in</small><b>${esc(p.mode)}</b></div><div><small>Folio nomination</small><b>${p.nomination_opt_out ? 'Opted out' : p.nominees.length ? p.nominees.map(n => esc(n.name) + ' ' + esc(n.share) + '%').join(', ') : pill('None', 'warn')}</b></div>
      <div><small>Family route</small><b>${p.route ? pill(p.route, 'acc') : '<span class="dim">fixed by the first order</span>'}</b>${co ? ' <button class="linkish" data-act="mfRoute">Change</button>' : ''}</div><div><small>Family (PAN)</small><b class="small mono">${(p.family || []).map(esc).join(', ')}</b></div>
      <div><small>Tax status · occupation · gender</small><b>${esc(p.tax_status || '—')} · ${esc(p.occupation || '—')} · ${esc(p.gender || '—')}</b></div><div><small>Address</small><b class="small">${esc(p.address || '—')} ${esc(p.state_code || '')} ${esc(p.pincode || '')}</b></div></div></div></div>
    <div class="panel"><div class="hd"><h3>Platform registration</h3></div>${tbl([['Platform'], ['Status'], ['']], regs, { empty: 'No platform enabled in MF settings.' })}</div></div>
    <div class="panel"><div class="hd"><h3>Holdings</h3><small class="dim">Value ${inr(d.prof.units.value, 2)}</small></div>${mfHoldTbl(d.prof.units.holdings, false)}</div>`;
}

function mfHoldTbl(hs, withClient) {
  return tbl([...(withClient ? [['Client']] : []), ['Scheme'], ['Folio / demat'], ['Units', 'r'], ['NAV', 'r'], ['Value', 'r'], ['Invested', 'r']], hs.map(h => `<tr>${withClient ? `<td>${mfClientCell(h.ucc, h.name)}</td>` : ''}<td class="wrap">${esc(h.scheme)} <span class="tag">${esc(h.plan || '')}</span><div class="dim small mono">${esc(h.isin)}</div></td><td>${h.mode === 'Demat' ? 'Demat' : 'Folio ' + esc(h.folio)}</td><td class="r">${mfUnits(h.units)}</td><td class="r">${nf(h.nav, 4)}<div class="dim small">${esc(fmtDate(h.nav_date))}</div></td><td class="r">${inr(h.value, 2)}</td><td class="r">${num(h.invested) ? inr(h.invested, 2) : '—'}</td></tr>`), { empty: 'No units on record.' });
}

/* ---------- SIPs ---------- */
V.mfsips = {
  load: async () => { const [sips, st] = await Promise.all([get('/v1/mf/sips'), get('/v1/mf/status')]); return { sips, st }; },
  html: d => {
    const act = d.sips.filter(s => s.status === 'Active'), soon = act.filter(s => s.next_due && s.display !== 'Paused' && s.next_due <= nextBD(nextBD(nextBD(nextBD(nextBD(S.date))))));
    const monthly = sum(act.filter(s => s.frequency === 'MONTHLY'), s => num(s.amount)) + sum(act.filter(s => s.frequency === 'WEEKLY'), s => num(s.amount) * 52 / 12) + sum(act.filter(s => s.frequency === 'QUARTERLY'), s => num(s.amount) / 3);
    return ph('SIPs', `XSIPs on BSE StAR MF and NSE MF Invest, each debited from the investor’s own bank account by a mandate in the clearing corporation’s favour. The bank is sent the debit the working day before an instalment; cancellations close two working days before it; an SIP can be paused twice; three failed instalments in a row cancel it. Clients are reminded ${esc(d.st.settings.sip_alert_days ?? 3)} days ahead.`,
      has('Ops maker', 'Ops checker') ? '<button class="btn pri" data-act="mfSIP">Start SIP</button>' : '') +
    `<div class="kpis">${kpi('Active SIPs', act.length, inr(monthly) + ' a month')}${kpi('Paused', d.sips.filter(s => s.display === 'Paused').length)}${kpi('Due within 5 working days', soon.length, inr(sum(soon, s => num(s.amount))), soon.length ? 'warn' : '')}${kpi('Failing', act.filter(s => s.failures > 0).length, 'one or two failed in a row', act.some(s => s.failures > 0) ? 'bad' : '')}${kpi('Cancelled', d.sips.filter(s => s.status === 'Cancelled').length)}</div>
    <div class="panel"><div class="hd"><div class="tools">${segBtns('mfSipF', ['Active', 'Paused', 'Cancelled', 'Completed', 'Registration failed', 'All'], 'mf-sip')}</div></div><div id="mf-sip">${P['mf-sip'](d)}</div></div>` + mfMoneyNote;
  }
};
UI.mfSipF = UI.mfSipF || 'Active';
P['mf-sip'] = d => {
  const mk = has('Ops maker', 'Ops checker');
  const rows = d.sips.filter(s => UI.mfSipF === 'All' || s.display === UI.mfSipF || (UI.mfSipF === 'Active' && s.status === 'Active' && s.display !== 'Paused'));
  return tbl([['SIP'], ['Client'], ['Scheme'], ['Instalment', 'r'], ['Next due'], ['Done / failed', 'r'], ['Status'], ['']], rows.map(s => `<tr><td class="n">${esc(s.id)}<div class="dim small mono">${esc(mfPlat(s.platform))} ${esc(s.reg_id || '')}</div></td><td>${mfClientCell(s.ucc, s.name)}</td>
    <td class="wrap">${esc(s.scheme)} <span class="tag">${esc(s.plan)}</span><div class="dim small">mandate ${esc(s.mandate_id)}${s.euin ? ' · EUIN ' + esc(s.euin) : ''}</div></td><td class="r">${inr(s.amount)}<div class="dim small">${esc(s.frequency.toLowerCase())} × ${s.installments}</div></td>
    <td class="n">${s.next_due ? fmtDate(s.next_due) : '—'}${(s.pauses || []).length ? `<div class="dim small">${s.pauses.map(p => 'paused ' + fmtDate(p.from) + '–' + fmtDate(p.to)).join('<br>')}</div>` : ''}</td><td class="r">${s.done} / <span class="${s.failures ? 'neg' : ''}">${s.failed}</span></td>
    <td>${mfPill(s.display || s.status)}${s.remark ? `<div class="dim small wrap">${esc(s.remark)}</div>` : ''}</td>
    <td><div class="split">${mk && s.status === 'Active' ? `<button class="btn sm" data-act="mfInst" data-id="${esc(s.id)}" data-due="${esc(s.next_due || '')}">Instalment</button>${(s.pauses || []).length < 2 ? `<button class="btn sm" data-act="mfPause" data-id="${esc(s.id)}">Pause</button>` : ''}<button class="btn sm" data-act="mfSIPCancel" data-id="${esc(s.id)}" data-plat="${esc(s.platform)}">Cancel</button>` : ''}<button class="btn sm" data-act="mfSIPLog" data-id="${esc(s.id)}">History</button></div></td></tr>`), { tall: true, empty: 'No SIPs here.' });
};

/* ---------- mandates ---------- */
V.mfmandates = {
  load: () => get('/v1/mf/mandates'),
  html: ms => ph('MF mandates', 'Debit mandates for XSIPs and one-time purchases: always on the investor’s own registered bank account and in favour of the platform’s clearing corporation (NSE Clearing for NSE MF Invest, Indian Clearing Corporation for BSE StAR MF), at most ₹1 crore a debit. An e-mandate takes up to 5 working days to register, a physical NACH up to 20. Register the mandate on the platform, then record its number here.',
    has('Ops maker', 'Ops checker') ? '<button class="btn pri" data-act="mfMandate">Record mandate</button><button class="btn" data-act="mfImportOpen" data-id="mandate-status">Load status report</button>' : '') +
    `<div class="kpis">${kpi('Approved', ms.filter(m => m.status === 'Approved').length, '', 'good')}${kpi('Pending', ms.filter(m => m.status === 'Pending').length, ms.filter(m => m.overdue).length + ' past the usual time', ms.some(m => m.overdue) ? 'warn' : '')}${kpi('Rejected', ms.filter(m => m.status === 'Rejected').length, '', ms.some(m => m.status === 'Rejected') ? 'bad' : '')}${kpi('SIPs on mandates', sum(ms, m => m.sips))}</div>
    <div class="panel">${tbl([['Mandate'], ['Client'], ['Platform'], ['Type'], ['Limit a debit', 'r'], ['Bank'], ['In favour of'], ['Status'], ['']], ms.map(m => `<tr><td class="n">${esc(m.id)}<div class="dim small mono">${esc(m.platform_id || '')}${m.umrn ? ' · ' + esc(m.umrn) : ''}</div></td><td>${mfClientCell(m.ucc, m.name)}</td><td>${esc(mfPlat(m.platform))}</td><td>${esc(m.type)}<div class="dim small">${fmtDate(m.from)}${m.till ? ' – ' + fmtDate(m.till) : ' until cancelled'}</div></td>
      <td class="r">${inr(m.limit)}</td><td>${esc(m.bank)}<div class="dim small mono">${esc(m.account)} · ${esc(m.ifsc)}</div></td><td class="small">${esc(m.favour)}</td><td>${mfPill(m.status)}${m.overdue ? ' ' + pill('Overdue', 'bad') : ''}${m.status === 'Pending' ? `<div class="dim small">expected by ${fmtDate(m.expected_by)}</div>` : ''}${m.remark ? `<div class="dim small">${esc(m.remark)}</div>` : ''}<div class="dim small">${m.sips} SIP${m.sips === 1 ? '' : 's'}</div></td>
      <td>${has('Ops maker', 'Ops checker') ? `<button class="btn sm" data-act="mfMandateSt" data-id="${esc(m.id)}" data-ucc="${esc(m.ucc)}">Update</button>` : ''}</td></tr>`), { tall: true, empty: 'No mandates recorded.' })}</div>`
};

/* ---------- units ---------- */
V.mfunits = {
  load: async () => { await clientCache(); return get('/v1/mf/units' + (MF.unitsUCC ? '?ucc=' + encM(MF.unitsUCC) : '')); },
  html: d => ph('MF units', 'Units in investors’ demat accounts and folios, from allotments, the platforms’ statements and the RTAs’ transaction feeds. A record of what investors hold, valued at the latest NAV loaded: the units are the investor’s, held at the depository or the RTA, never by the firm.',
    `<div class="tools"><div style="min-width:260px">${cpick('mfu-ucc', MF.unitsUCC || '', { placeholder: 'All investors', none: 'All investors' })}</div><button class="btn" data-act="mfUnitsFor">Show</button></div>${has('Ops maker', 'Ops checker') ? '<button class="btn" data-act="mfAddUnits">Add units</button><button class="btn" data-act="mfImportOpen" data-id="rta-transactions">Load RTA feed</button>' : ''}`) +
    `<div class="kpis">${kpi('Value', inr(d.value, 2), d.holdings.length + ' holdings')}${kpi('Investors', new Set(d.holdings.map(h => h.ucc)).size)}${kpi('Regular plans', inr(sum(d.holdings.filter(h => h.plan === 'Regular'), h => num(h.value))), 'earn trail commission')}${kpi('Direct plans', inr(sum(d.holdings.filter(h => h.plan === 'Direct'), h => num(h.value))))}</div>
    <div class="panel"><div class="hd"><h3>Holdings</h3></div>${mfHoldTbl(d.holdings, true)}</div>
    <div class="panel"><div class="hd"><h3>Movements</h3><small class="dim">newest first</small></div>${tbl([['Date'], ['Client'], ['Scheme'], ['Folio / demat'], ['Movement'], ['Units', 'r'], ['NAV', 'r'], ['Source']], d.txns.slice(0, 300).map(t => `<tr><td class="n">${fmtDate(t.date)}</td><td class="mono">${esc(t.ucc)}</td><td class="wrap">${esc(t.scheme)}</td><td>${t.mode === 'Demat' ? 'Demat' : esc(t.folio)}</td><td>${esc(t.kind)}${t.order ? `<div class="dim small mono">${esc(t.order)}</div>` : ''}</td><td class="r ${num(t.units) < 0 ? 'neg' : 'pos'}">${nf(t.units, 3)}</td><td class="r">${num(t.nav) ? nf(t.nav, 4) : ''}</td><td class="dim small">${esc(t.source)} · ${esc(t.by)}</td></tr>`), { tall: true, empty: 'No movements.' })}</div>`
};

/* ---------- commission ---------- */
V.mfcomm = {
  load: () => { if (!MF.month) { const d = new Date(S.date + 'T00:00:00Z'); d.setUTCMonth(d.getUTCMonth() - 1); MF.month = d.toISOString().slice(0, 7); } return get('/v1/mf/commission?month=' + MF.month); },
  html: v => ph('MF trail commission', 'Trail expected on Regular plan holdings (units at the month’s end × the month-end NAV × the firm’s trail rate ÷ 12), against what the fund houses paid in the RTAs’ brokerage statements. Differences over 2% are flagged. Load KFintech’s and CAMS’s brokerage files with a saved column mapping.',
    `<div class="tools"><input class="in" type="month" id="mfc-m" value="${esc(v.month)}"><button class="btn" data-act="mfCommMonth">Show</button></div>${has('Ops checker', 'Compliance officer') ? '<button class="btn pri" data-act="mfAccrue">Work out accruals</button>' : ''}${has('Ops maker', 'Ops checker') ? '<button class="btn" data-act="mfImportOpen" data-id="rta-brokerage">Load brokerage statement</button>' : ''}`) +
    `<div class="kpis">${kpi('Expected', inr(v.expected, 2), v.accruals.length + ' holdings')}${kpi('Received', inr(v.received, 2), (v.files || []).length + ' statement file(s)')}${kpi('Difference', inr(num(v.received) - num(v.expected), 2), '', num(v.received) < num(v.expected) ? 'bad' : 'good')}${kpi('Not matched to a scheme', inr(v.unmapped, 2), 'rows without a known ISIN or scheme', num(v.unmapped) ? 'warn' : '')}</div>
    <div class="panel"><div class="hd"><h3>By scheme · ${esc(v.month)}</h3></div>${tbl([['Scheme'], ['Holdings', 'r'], ['Expected', 'r'], ['Received', 'r'], ['Difference', 'r'], ['Status']], v.lines.map(l => `<tr><td class="wrap">${esc(l.scheme)}<div class="dim small mono">${esc(l.isin)}${l.amc ? ' · ' + esc(l.amc) : ''}</div></td><td class="r">${l.holdings}</td><td class="r">${inr(l.expected, 2)}</td><td class="r">${inr(l.received, 2)}</td><td class="r ${num(l.difference) < 0 ? 'neg' : ''}">${inr(l.difference, 2)}</td><td>${mfPill(l.status)}</td></tr>`), { empty: 'Nothing for this month: work out the accruals and load the brokerage statements.' })}</div>
    <div class="panel"><div class="hd"><h3>Accruals</h3></div>${tbl([['Client'], ['Scheme'], ['Folio / demat'], ['Units', 'r'], ['Month-end NAV', 'r'], ['Value', 'r'], ['Rate'], ['Expected', 'r']], v.accruals.map(a => `<tr><td class="mono">${esc(a.ucc)}</td><td class="wrap">${esc(a.scheme)}</td><td>${a.folio ? esc(a.folio) : 'Demat'}</td><td class="r">${mfUnits(a.units)}</td><td class="r">${nf(a.nav, 4)}</td><td class="r">${inr(a.value, 2)}</td><td>${esc(a.rate_pct)}% a year</td><td class="r">${inr(a.expected, 2)}</td></tr>`), { tall: true, empty: 'No accruals.' })}</div>`
};

/* ---------- settings, platforms, feeds and files ---------- */
V.mfsettings = {
  load: () => get('/v1/mf/status'),
  html: d => {
    const s = d.settings, co = has('Compliance officer'), dis = co ? '' : 'disabled', mk = has('Ops maker', 'Ops checker');
    const cut = p => (s.cutoffs || {})[p] || d.default_cutoffs[p] || {};
    return ph('MF settings', 'How the firm offers mutual funds, the platforms it uses, the NAV feed and the platforms’ and RTAs’ files. The platforms’ logins are kept in Connections.', '<button class="btn" data-act="go" data-id="connections">Connections</button>') +
    `<div class="panel"><div class="hd"><h3>Route and registration</h3>${co ? '' : '<small class="dim">Compliance changes these</small>'}</div><form class="bd" id="mfsf"><div class="form">
      <div class="fld"><label for="mfs-route">Plans offered</label><select class="in" id="mfs-route" ${dis}>${[['', 'Not offered'], ['Regular', 'Regular plans (the firm as distributor)'], ['Direct', 'Direct plans (execution only)'], ['Both', 'Both (each family uses one)']].map(([k, l]) => `<option value="${k}" ${s.route === k ? 'selected' : ''}>${l}</option>`).join('')}</select></div>
      <div class="fld"><label for="mfs-arn">AMFI registration (ARN)</label><input class="in mono" id="mfs-arn" value="${esc(s.arn || '')}" placeholder="ARN-12345" ${dis}></div>
      <div class="fld"><label for="mfs-arnv">ARN valid till</label><input class="in" type="date" id="mfs-arnv" value="${esc(s.arn_valid_till || '')}" ${dis}></div>
      <div class="fld full"><label for="mfs-basis">Basis for Direct plans</label><select class="in" id="mfs-basis" ${dis}><option value="">—</option>${d.direct_bases.map(b => `<option ${s.direct_basis === b ? 'selected' : ''}>${esc(b)}</option>`).join('')}</select><div class="dim small" style="margin-top:4px">SEBI circular SEBI/HO/IMD/IMD-PoD-1/P/CIR/2023/86 (execution-only platforms). A broker’s platform for its own broking clients is exempt under para 1.2; compliance confirms this applies before Direct plans are offered.${s.direct_confirmed_by ? ` Recorded by ${esc(s.direct_confirmed_by)} on ${fmtDate(s.direct_confirmed_on)}.` : ''}</div></div>
      <div class="fld full"><label>Platforms</label><div>${d.platforms.map(p => `<label class="check"><input type="checkbox" data-mfplat="${p.id}" ${s.platforms.includes(p.id) ? 'checked' : ''} ${dis}> ${esc(p.name)}</label>`).join(' ')}</div></div>
      ${d.platforms.map(p => `<div class="fld"><label>${esc(p.name)} cut-offs (liquid · others)</label><div class="split"><input class="in mono" id="mfs-cl-${p.id}" value="${esc(cut(p.id).liquid || '')}" style="max-width:90px" ${dis}><input class="in mono" id="mfs-co-${p.id}" value="${esc(cut(p.id).other || '')}" style="max-width:90px" ${dis}></div></div>`).join('')}
      <div class="fld"><label for="mfs-trail">Trail commission, % a year</label><input class="in mono" id="mfs-trail" value="${esc((s.trail_pct || {})['*'] || '')}" placeholder="e.g. 0.75" ${dis}><div class="dim small">Any Regular plan; per scheme or fund house below</div></div>
      <div class="fld"><label for="mfs-trailx">By ISIN or fund house (KEY=% per line)</label><textarea class="in mono" id="mfs-trailx" rows="3" ${dis}>${esc(Object.entries(s.trail_pct || {}).filter(([k]) => k !== '*').map(([k, v]) => k + '=' + v).join('\n'))}</textarea></div>
      <div class="fld"><label for="mfs-days">SIP reminders, days ahead</label><input class="in mono" id="mfs-days" value="${esc(s.sip_alert_days ?? 3)}" ${dis}></div>
      <div class="fld"><label class="check"><input type="checkbox" id="mfs-nav" ${s.nav_fetch ? 'checked' : ''} ${dis}> Fetch AMFI’s NAV file every evening</label></div>
      <div class="fld full"><label>Employees’ EUINs (NISM Series V-A)</label><table class="tbl"><thead><tr><th>Employee</th><th>EUIN</th><th>Valid till</th><th>NISM certificate</th></tr></thead><tbody>${(d.users || []).map(u => { const e = s.euins.find(x => x.user === u.id) || {}; return `<tr><td>${esc(u.name)} <span class="dim small">${esc(u.role)}</span></td><td><input class="in mono" data-euin="${esc(u.id)}" value="${esc(e.euin || '')}" placeholder="E123456" style="max-width:120px" ${dis}></td><td><input class="in" type="date" data-euinv="${esc(u.id)}" value="${esc(e.valid_till || '')}" ${dis}></td><td><input class="in mono" data-euinn="${esc(u.id)}" value="${esc(e.nism || '')}" ${dis}></td></tr>`; }).join('')}</tbody></table></div>
    </div><div id="mfs-err"></div>${co ? '<button class="btn pri" type="submit">Save MF settings</button>' : ''}</form></div>
    <div class="grid2">${d.platforms.map(p => `<div class="panel"><div class="hd"><h3>${esc(p.name)}</h3>${p.enabled ? pill('Enabled', 'good') : pill('Off', 'nt')}</div><div class="bd"><div class="kv"><div><small>Connection</small><b>${mfPill(p.connection)} ${esc(p.env || '')}</b></div><div><small>Payments to</small><b>${esc(p.clearing)}</b></div><div><small>Last checked</small><b>${p.poll.last_try ? esc(p.poll.last_try.replace('T', ' ').slice(0, 16)) : '—'}</b></div></div>
      <p class="small" style="margin-bottom:4px"><b>Through the API:</b> ${p.api.map(esc).join(' · ')}</p><p class="small" style="margin-top:0"><b>On the platform’s site, recorded here:</b> ${p.manual.map(esc).join(' · ')}</p>
      ${Object.keys(p.poll.no_api || {}).length ? `<div class="dim small">${Object.entries(p.poll.no_api).map(([k, v]) => `${esc(k)}: ${esc(v)}`).join('<br>')}</div>` : ''}${p.poll.last_error ? `<div class="note bad small">${esc(p.poll.last_error)}</div>` : ''}</div></div>`).join('')}</div>
    <div class="grid2"><div class="panel"><div class="hd"><h3>NAV and scheme master</h3></div><div class="bd"><div class="kv"><div><small>Latest NAV date</small><b>${d.nav.nav_date ? fmtDate(d.nav.nav_date) : '—'}</b></div><div><small>Schemes</small><b>${nf(d.schemes)}</b></div><div><small>From</small><b>${esc(d.nav.source || '—')}</b></div></div>
      ${d.nav.last_error ? `<div class="note bad small">${esc(d.nav.last_error)}</div>` : ''}<p class="dim small">AMFI publishes every scheme’s NAV each business evening (amfiindia.com/spages/NAVAll.txt). Each platform’s scheme codes come from its scheme file (Platform scheme master below).</p>
      ${mk ? `<div class="tools"><button class="btn" data-act="mfNAVFetch">Fetch from AMFI now</button><label class="btn">Upload NAV file<input type="file" id="mf-navfile" hidden></label></div>` : ''}</div></div>
    <div class="panel"><div class="hd"><h3>Platform and RTA files</h3></div><div class="bd"><p class="small" style="margin-top:0">Files whose layouts the platforms and RTAs give only to members and distributors load through a column mapping, saved for next time: ${d.import_kinds.map(k => esc(k.name)).join(' · ')}. KFintech (MFSD) and CAMS (WBR) files sent as DBF are saved as CSV first.</p>
      ${mk ? '<button class="btn pri" data-act="mfImportOpen">Load a file</button>' : ''}${d.mappings.length ? `<div class="dim small" style="margin-top:8px">Saved mappings: ${d.mappings.map(m => esc(m.name) + ' (' + esc(m.kind) + (m.platform ? ', ' + esc(m.platform) : '') + ')').join(', ')}</div>` : ''}</div></div></div>
    <div class="panel"><div class="hd"><h3>Family routes on record</h3><small class="dim">${d.routes.length}</small></div>${tbl([['PAN'], ['Route'], ['Since'], ['Fixed by'], ['Note']], d.routes.map(r => `<tr><td class="mono">${esc(r.pan)}</td><td>${r.route ? pill(r.route, 'acc') : '<span class="dim">cleared</span>'}</td><td class="n">${fmtDate(r.since)}</td><td>${esc(r.ucc || '')} <span class="dim small">${esc(r.by)}</span></td><td class="wrap small">${esc(r.note || '')}</td></tr>`), { empty: 'No family has invested yet.' })}</div>`;
  }
};

document.addEventListener('submit', async e => {
  if (e.target.id !== 'mfsf') return;
  e.preventDefault();
  const v = id => ($(id)?.value || '').trim(), s = C.mfsettings.settings;
  const trail = {}; if (v('mfs-trail')) trail['*'] = v('mfs-trail');
  v('mfs-trailx').split('\n').map(l => l.split('=')).filter(x => x.length === 2 && x[0].trim()).forEach(([k, x]) => { trail[k.trim()] = x.trim(); });
  const cutoffs = {}; C.mfsettings.platforms.forEach(p => { if (v('mfs-cl-' + p.id) && v('mfs-co-' + p.id)) cutoffs[p.id] = { liquid: v('mfs-cl-' + p.id), other: v('mfs-co-' + p.id) }; });
  const euins = [...document.querySelectorAll('[data-euin]')].filter(x => x.value.trim()).map(x => ({ user: x.dataset.euin, euin: x.value.trim().toUpperCase(), valid_till: document.querySelector(`[data-euinv="${x.dataset.euin}"]`)?.value || '', nism: document.querySelector(`[data-euinn="${x.dataset.euin}"]`)?.value.trim() || '' }));
  const body = { route: v('mfs-route'), arn: v('mfs-arn'), arn_valid_till: v('mfs-arnv'), euins, direct_basis: v('mfs-basis'), platforms: [...document.querySelectorAll('[data-mfplat]:checked')].map(x => x.dataset.mfplat),
    cutoffs, trail_pct: trail, sip_alert_days: +v('mfs-days') || 0, nav_fetch: $('mfs-nav').checked, direct_confirmed_by: s.direct_confirmed_by || '', direct_confirmed_on: s.direct_confirmed_on || '' };
  try { await api('PUT', '/v1/mf/settings', body); toast('MF settings saved'); render({ keep: true }); } catch (err) { $('mfs-err').innerHTML = errHTML(err); }
});
document.addEventListener('change', e => {
  if (e.target.id === 'mf-navfile' && e.target.files.length) {
    const f = e.target.files[0];
    f.arrayBuffer().then(b => act(() => api('POST', '/v1/mf/nav?name=' + encM(f.name), b, true), r => `NAVs of ${fmtDate(r.nav_date)} loaded: ${nf(r.schemes)} schemes, ${nf(r.changed)} changed`)).catch(() => {});
  }
});

/* ---------- the client portal: Mutual funds ---------- */
V.pmf = {
  load: async () => { const pv = await portalLoad(); const mf = await get('/v1/portal/mf?ucc=' + encM(pv.account.ucc)); return { pv, mf }; },
  html: ({ pv, mf }) => {
    const ready = mf.ready.length > 0 && !mf.missing.length, p = mf.profile;
    const plan = mf.route || (mf.offered === 'Both' ? '' : mf.offered);
    return pHead(pv, 'Mutual funds', `Invest in mutual funds through ${mf.platforms.map(mfPlat).map(esc).join(' and ') || 'the exchanges’ platforms'}. ${esc(mf.pay_note)}`) +
    (mf.missing.length ? `<div class="note warn"><b>Before you can invest:</b> ${mf.missing.map(esc).join(' · ')}</div>` : '') +
    (mf.nominate ? '<div class="note">A new folio needs your nomination (up to 3 nominees) or your choice to opt out.</div>' : '') +
    `<div class="kpis">${kpi('Value of your units', inr(mf.value, 2), mf.holdings.length + ' holdings')}${kpi('SIPs', mf.sips.filter(s => s.status === 'Active').length, inr(sum(mf.sips.filter(s => s.status === 'Active'), s => num(s.amount))) + ' an instalment')}${kpi('Plans', mf.route || (mf.offered === 'Both' ? 'Regular or Direct' : mf.offered || '—'), mf.route ? 'your family’s route' : 'your first order sets your family’s route')}</div>
    ${ready ? `<div class="panel"><div class="hd"><h3>Buy</h3></div><form class="bd" id="pmf-buy"><div class="form">${mfSchemePick('pmf-isin', { portal: true, plan, platform: 'pmf-plat' })}
      <div class="fld"><label for="pmf-plat">Platform</label><select class="in" id="pmf-plat">${mf.ready.map(x => `<option value="${x}">${esc(mfPlat(x))}</option>`).join('')}</select></div>
      <div class="fld"><label for="pmf-amt">Amount (₹)</label><input class="in mono" id="pmf-amt" inputmode="decimal"></div></div>
      <p class="dim small">Cut-offs: ${esc(mf.cutoffs)}. Orders after the cut-off get the next business day’s NAV. ${esc(mf.arn_note || '')}</p><div id="pmf-err"></div><button class="btn pri" type="submit">Place order</button></form></div>` : ''}
    <div class="panel"><div class="hd"><h3>Your units</h3></div>${tbl([['Scheme'], ['Units', 'r'], ['NAV', 'r'], ['Value', 'r'], ['']], mf.holdings.map(h => `<tr><td class="wrap">${esc(h.scheme)} <span class="tag">${esc(h.plan || '')}</span><div class="dim small">${h.mode === 'Demat' ? 'In your demat account' : 'Folio ' + esc(h.folio)}</div></td><td class="r">${mfUnits(h.units)}</td><td class="r">${nf(h.nav, 4)}</td><td class="r">${inr(h.value, 2)}</td><td>${ready ? `<button class="btn sm" data-act="pmfRedeem" data-id="${esc(h.isin)}" data-folio="${esc(h.folio || '')}" data-units="${esc(h.units)}">Redeem</button>` : ''}</td></tr>`), { empty: 'No units yet.' })}</div>
    <div class="panel"><div class="hd"><h3>SIPs</h3>${ready && p.mandates.some(m => ['Approved', 'Pending'].includes(m.status)) ? '<button class="btn sm pri" data-act="pmfSIP">Start an SIP</button>' : ''}</div>${tbl([['Scheme'], ['Instalment', 'r'], ['Next'], ['Status'], ['']], mf.sips.map(s => `<tr><td class="wrap">${esc(s.scheme)}</td><td class="r">${inr(s.amount)}<div class="dim small">${esc(s.frequency.toLowerCase())}</div></td><td class="n">${s.next_due ? fmtDate(s.next_due) : '—'}</td><td>${mfPill(s.display || s.status)}</td><td>${s.status === 'Active' ? `<button class="btn sm" data-act="pmfSIPCancel" data-id="${esc(s.id)}">Cancel</button>` : ''}</td></tr>`), { empty: p.mandates.length ? 'No SIPs.' : 'An SIP is debited by a mandate on your bank account in the clearing corporation’s favour. Ask us to set one up.' })}</div>
    <div class="panel"><div class="hd"><h3>Orders</h3></div>${tbl([['Order'], ['Scheme'], ['Type'], ['Amount / units', 'r'], ['Status']], mf.orders.map(x => `<tr><td class="n">${esc(x.order_no || x.id)}<div class="dim small">${fmtDate(x.date)}</div></td><td class="wrap">${esc(x.scheme)}</td><td>${esc(x.type)}</td><td class="r">${num(x.amount) ? inr(x.amount, 2) : mfUnits(x.units) + ' u'}</td><td>${mfPill(x.status)}${x.status === 'Placed' ? '<div class="dim small">Authorise and pay with the link the exchange sent you</div>' : ''}</td></tr>`), { empty: 'No orders yet.' })}</div>
    <div class="panel"><div class="hd"><h3>FATCA and nomination</h3><button class="btn sm" data-act="pmfProfile">Update</button></div><div class="bd"><div class="kv"><div><small>FATCA self-certification</small><b>${p.fatca ? 'Declared ' + fmtDate(p.fatca_on) : pill('Not declared', 'warn')}</b></div><div><small>Nominees on folios</small><b>${p.nomination_opt_out ? 'You opted out' : p.nominees.length ? p.nominees.map(n => esc(n.name) + ' (' + esc(n.share) + '%)').join(', ') : pill('None', 'warn')}</b></div></div></div></div>`;
  }
};
document.addEventListener('submit', async e => {
  if (e.target.id !== 'pmf-buy') return;
  e.preventDefault();
  const isin = $('pmf-isin').value; if (!isin) { $('pmf-err').innerHTML = errHTML({ message: 'Find and choose a scheme' }); return; }
  try { const r = await post('/v1/portal/mf/orders', { ucc: UI.pAcct, platform: $('pmf-plat').value, type: 'Purchase', isin, amount: $('pmf-amt').value.replace(/,/g, '') }); toast(`Order ${r.order_no || r.id} placed: authorise and pay with the exchange’s link`); render({ keep: true }); }
  catch (err) { $('pmf-err').innerHTML = errHTML(err); }
});

/* ---------- actions ---------- */
const mfOpts = (xs, v) => xs.map(x => { const [k, l] = Array.isArray(x) ? x : [x, x]; return `<option value="${esc(k)}" ${v === k ? 'selected' : ''}>${esc(l)}</option>`; }).join('');
async function mfReadyPlatforms() { const st = C.mfsettings || C.mforders?.st || C.mfsips?.st || await get('/v1/mf/status'); return (st.settings || st).platforms || []; }
function mfImportModal(kind) {
  const st = MF.imp, k = st.import_kinds.find(x => x.id === kind) || st.import_kinds[0];
  const maps = st.mappings.filter(m => m.kind === k.id);
  modal(mhd('Load an MF file', esc(k.about)) + `<form class="bd" id="mfif"><div class="form">
    <div class="fld"><label for="mfi-kind">File</label><select class="in" id="mfi-kind">${mfOpts(st.import_kinds.map(x => [x.id, x.name]), k.id)}</select></div>
    ${k.platform ? `<div class="fld"><label for="mfi-plat">From</label><select class="in" id="mfi-plat">${mfOpts([['NSE', 'NSE MF Invest'], ['BSE', 'BSE StAR MF']], '')}</select></div>` : ''}
    <div class="fld"><label for="mfi-map">Saved mapping</label><select class="in" id="mfi-map"><option value="">— map the columns below —</option>${mfOpts(maps.map(m => [m.name, m.name + (m.platform ? ' (' + m.platform + ')' : '')]), '')}</select></div>
    <div class="fld full"><label for="mfi-file">File (CSV, pipe, semicolon or tab separated, headings in the first line)</label><input class="in" type="file" id="mfi-file"></div>
    ${k.fields.map(f => `<div class="fld"><label for="mfi-c-${f.key}">${esc(f.label)}${f.required ? ' *' : ''}</label><input class="in" id="mfi-c-${f.key}" data-mfcol="${f.key}" placeholder="the file’s column heading"></div>`).join('')}
    <div class="fld"><label for="mfi-save">Save this mapping as</label><input class="in" id="mfi-save" placeholder="e.g. KFintech MFSD201"></div></div>
    <div id="mfi-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Load</button></div></form>`);
  $('mfi-kind').onchange = () => mfImportModal($('mfi-kind').value);
  $('mfif').onsubmit = async ev => {
    ev.preventDefault();
    const f = $('mfi-file').files[0]; if (!f) { $('mfi-err').innerHTML = errHTML({ message: 'Choose the file' }); return; }
    const cols = {}; document.querySelectorAll('[data-mfcol]').forEach(x => { if (x.value.trim()) cols[x.dataset.mfcol] = x.value.trim(); });
    const q = `kind=${encM(k.id)}&platform=${encM($('mfi-plat')?.value || '')}&mapping=${encM($('mfi-map').value)}&save_as=${encM($('mfi-save').value.trim())}&name=${encM(f.name)}&columns=${encM(JSON.stringify(cols))}`;
    try { const r = await api('POST', '/v1/mf/import?' + q, await f.arrayBuffer(), true); closeModal(); toast(`${r.applied} of ${r.rows} rows applied${r.skipped.length ? ', ' + r.skipped.length + ' skipped' : ''}`); if (r.skipped.length) setTimeout(() => modal(mhd('Rows not applied', esc(f.name)) + `<div class="bd"><ul>${r.skipped.map(s => `<li>${esc(s)}</li>`).join('')}</ul></div><div class="ft"><button class="btn pri" data-act="close">OK</button></div>`), 50); render({ keep: true }); }
    catch (err) { $('mfi-err').innerHTML = errHTML(err); }
  };
}

Object.assign(A, {
  mfPickUCC: () => { MF.ucc = $('mf-ucc')?.value || ''; UI.mfOrdTab = 'investor'; render({ keep: true }); },
  mfUnitsFor: () => { MF.unitsUCC = $('mfu-ucc')?.value || ''; render({ keep: true }); },
  mfCommMonth: () => { MF.month = $('mfc-m').value; render({ keep: true }); },
  mfAccrue: () => act(() => post('/v1/mf/commission/accrue', { month: MF.month }), v => `Accruals for ${v.month}: ${inr(v.expected, 2)} expected`),
  mfNAVFetch: () => act(() => post('/v1/mf/nav/fetch'), r => `NAVs of ${fmtDate(r.nav_date)} loaded: ${nf(r.schemes)} schemes`),
  mfImportOpen: async kind => { try { MF.imp = await get('/v1/mf/status'); mfImportModal(kind || 'scheme-codes'); } catch (e) { toast(e.message, true); } },
  mfOrder: () => clientCache().then(async () => {
    const plats = await mfReadyPlatforms();
    modal(mhd('Place a mutual fund order', 'The investor must be registered on the platform, with KYC validated and FATCA declared. Regular plan orders carry your EUIN.') + `<form class="bd" id="mfof"><div class="form">
      <div class="fld full"><label for="mfo-u-q">Investor</label>${cpick('mfo-u', MF.ucc || '', {})}</div>
      <div class="fld"><label for="mfo-plat">Platform</label><select class="in" id="mfo-plat">${mfOpts(plats.map(p => [p, mfPlat(p)]), '')}</select></div>
      <div class="fld"><label for="mfo-type">Order</label><select class="in" id="mfo-type">${mfOpts(['Purchase', 'Redemption', 'Switch'], 'Purchase')}</select></div>
      ${mfSchemePick('mfo-isin', { platform: 'mfo-plat' })}
      <div class="fld full" id="mfo-tobox" hidden>${mfSchemePick('mfo-to', { label: 'Switch into', platform: 'mfo-plat' }).replace('class="fld full"', 'class="fld"')}</div>
      <div class="fld"><label for="mfo-amt">Amount (₹)</label><input class="in mono" id="mfo-amt" inputmode="decimal"></div>
      <div class="fld"><label for="mfo-units">Units (redemption, switch)</label><input class="in mono" id="mfo-units" inputmode="decimal"></div>
      <div class="fld"><label class="check"><input type="checkbox" id="mfo-all"> All units</label></div>
      <div class="fld"><label for="mfo-folio">Folio (physical, additional purchase)</label><input class="in mono" id="mfo-folio"></div>
      <div class="fld"><label for="mfo-mdt">Pay by mandate (optional)</label><input class="in mono" id="mfo-mdt" placeholder="MDT-000001; empty: the platform’s payment link"></div></div>
      <div id="mfo-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Place order</button></div></form>`);
    $('mfo-type').onchange = () => { $('mfo-tobox').hidden = $('mfo-type').value !== 'Switch'; };
    $('mfof').onsubmit = async e => { e.preventDefault(); try { const r = await post('/v1/mf/orders', { ucc: $('mfo-u').value, platform: $('mfo-plat').value, type: $('mfo-type').value, isin: $('mfo-isin').value, to_isin: $('mfo-to')?.value || '', amount: $('mfo-amt').value.replace(/,/g, ''), units: $('mfo-units').value, all_units: $('mfo-all').checked, folio: $('mfo-folio').value.trim(), mandate_id: $('mfo-mdt').value.trim() }); closeModal(); toast(`${r.id} placed on ${mfPlat(r.platform)}: order ${r.order_no}`); render({ keep: true }); } catch (err) { $('mfo-err').innerHTML = errHTML(err); } };
  }),
  mfCancel: id => confirmModal('Cancel ' + id, 'Cancels the order on the platform (NSE MF Invest). BSE StAR MF no longer takes cancellations of lumpsum orders.', '<div class="fld"><label for="mfc-n">Note</label><input class="in" id="mfc-n"></div>', 'Cancel order', () => act(() => post(`/v1/mf/orders/${encM(id)}/cancel`, { note: $('mfc-n').value }), id + ' cancelled'), true),
  mfAllot: (id, el) => { const red = el.dataset.type === 'Redemption', sw = el.dataset.type === 'Switch';
    confirmModal((red ? 'Redemption of ' : 'Allotment of ') + id, 'From the platform’s or RTA’s statement. Moves the units ledger.', `<div class="form"><div class="fld"><label for="mfa-u">${sw ? 'Units switched in' : red ? 'Units redeemed' : 'Units allotted'}</label><input class="in mono" id="mfa-u"></div>${sw ? '<div class="fld"><label for="mfa-o">Units switched out</label><input class="in mono" id="mfa-o"></div>' : ''}<div class="fld"><label for="mfa-n">NAV</label><input class="in mono" id="mfa-n"></div><div class="fld"><label for="mfa-d">Date</label><input class="in" type="date" id="mfa-d" value="${S.date}"></div><div class="fld"><label for="mfa-f">Folio</label><input class="in mono" id="mfa-f"></div>${red ? '<div class="fld"><label for="mfa-a">Proceeds (₹, paid by the AMC to the investor)</label><input class="in mono" id="mfa-a"></div>' : ''}</div>`, 'Record',
      () => act(() => post(`/v1/mf/orders/${encM(id)}/allot`, { units: $('mfa-u').value, out_units: $('mfa-o')?.value || '', nav: $('mfa-n').value, date: $('mfa-d').value, folio: $('mfa-f').value.trim(), amount: $('mfa-a')?.value || '' }), id + ' recorded')); },
  mfProfile: () => { const p = C.mforders.prof.profile, noms = p.nominees.length ? p.nominees : [{}];
    const nrow = (n, i) => `<div class="form" data-mfnom style="grid-template-columns:2fr 1fr 1fr 1fr 1.5fr;border:1px solid var(--line);border-radius:8px;padding:8px;margin-bottom:6px"><div class="fld"><label>Nominee ${i + 1}</label><input class="in" data-k="name" value="${esc(n.name || '')}"></div><div class="fld"><label>Relationship</label><input class="in" data-k="relation" value="${esc(n.relation || '')}"></div><div class="fld"><label>Share %</label><input class="in mono" data-k="share" value="${esc(n.share || '')}"></div><div class="fld"><label>Date of birth</label><input class="in" type="date" data-k="dob" value="${esc(n.dob || '')}"></div><div class="fld"><label>ID (1 PAN, 2 Aadhaar, 3 DL, 4 passport)</label><div class="split"><input class="in mono" data-k="id_type" value="${esc(n.id_type || '')}" style="max-width:44px"><input class="in mono" data-k="id" value="${esc(n.id || '')}"></div></div></div>`;
    modal(mhd('MF profile · ' + esc(p.ucc), 'What the platforms need beyond the trading account. Codes are the platform’s (tax status 01 individual, 04 company; occupation 01 business … 08 others).') + `<form class="bd" id="mfpf"><div class="form">
      <div class="fld"><label for="mfp-mode">Units held in</label><select class="in" id="mfp-mode">${mfOpts(['Demat', 'Physical'], p.mode)}</select></div>
      <div class="fld"><label for="mfp-tax">Tax status</label><input class="in mono" id="mfp-tax" value="${esc(p.tax_status || '')}"></div><div class="fld"><label for="mfp-occ">Occupation</label><input class="in mono" id="mfp-occ" value="${esc(p.occupation || '')}"></div>
      <div class="fld"><label for="mfp-g">Gender</label><select class="in" id="mfp-g">${mfOpts([['', '—'], ['M', 'Male'], ['F', 'Female'], ['O', 'Other'], ['T', 'Transgender']], p.gender || '')}</select></div>
      <div class="fld full"><label for="mfp-addr">Address</label><input class="in" id="mfp-addr" value="${esc(p.address || '')}"></div><div class="fld"><label for="mfp-st">State code</label><input class="in mono" id="mfp-st" value="${esc(p.state_code || '')}"></div><div class="fld"><label for="mfp-pin">PIN code</label><input class="in mono" id="mfp-pin" value="${esc(p.pincode || '')}"></div>
      <div class="fld"><label for="mfp-acct">Bank account type</label><select class="in" id="mfp-acct">${mfOpts(['SB', 'CB', 'NE', 'NO'], p.account_type || 'SB')}</select></div>
      <div class="fld"><label for="mfp-fam">Family head (PAN)</label><input class="in mono" id="mfp-fam" value="${esc(p.family_head || '')}"></div>
      <div class="fld"><label class="check"><input type="checkbox" id="mfp-fatca" ${p.fatca ? 'checked' : ''}> FATCA self-certification received</label><label class="check"><input type="checkbox" id="mfp-out" ${p.tax_resident_outside_india ? 'checked' : ''}> Tax resident outside India</label></div>
      <div class="fld"><label class="check"><input type="checkbox" id="mfp-opt" ${p.nomination_opt_out ? 'checked' : ''}> Opts out of nomination</label><select class="in" id="mfp-auth">${mfOpts([['O', 'Declared by OTP'], ['E', 'eSign'], ['W', 'Wet signature']], p.nomination_auth || 'O')}</select></div>
      <div class="fld full" id="mfp-noms">${noms.slice(0, 3).map(nrow).join('')}${noms.length < 3 ? [...Array(3 - noms.length)].map((_, i) => nrow({}, noms.length + i)).join('') : ''}</div></div>
      <div id="mfp-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
    $('mfpf').onsubmit = async e => { e.preventDefault();
      const nominees = [...document.querySelectorAll('[data-mfnom]')].map(r => { const o = {}; r.querySelectorAll('[data-k]').forEach(x => { o[x.dataset.k] = x.value.trim(); }); return o; }).filter(n => n.name);
      try { await api('PUT', '/v1/mf/clients/' + encM(p.ucc), { mode: $('mfp-mode').value, tax_status: $('mfp-tax').value.trim(), occupation: $('mfp-occ').value.trim(), gender: $('mfp-g').value, address: $('mfp-addr').value.trim(), pincode: $('mfp-pin').value.trim(), state_code: $('mfp-st').value.trim(), account_type: $('mfp-acct').value, fatca: $('mfp-fatca').checked, tax_resident_outside_india: $('mfp-out').checked, nominees, nomination_opt_out: $('mfp-opt').checked, nomination_auth: $('mfp-auth').value, family_head: $('mfp-fam').value.trim() }); closeModal(); toast('MF profile saved'); render({ keep: true }); }
      catch (err) { $('mfp-err').innerHTML = errHTML(err); } }; },
  mfRegister: id => act(() => post(`/v1/mf/clients/${encM(MF.ucc)}/register`, { platform: id }), `Registration sent to ${mfPlat(id)}: the investor authorises it from the exchange’s link`),
  mfMarkReg: id => confirmModal('Registration on ' + mfPlat(id), 'Record the status the platform shows (Registered once the investor has authorised it).', `<div class="form"><div class="fld"><label for="mfr-s">Status</label><select class="in" id="mfr-s">${mfOpts(['Registered', 'Submitted', 'Failed'], 'Registered')}</select></div><div class="fld"><label for="mfr-r">Remark</label><input class="in" id="mfr-r"></div></div>`, 'Record', () => act(() => post(`/v1/mf/clients/${encM(MF.ucc)}/registration`, { platform: id, status: $('mfr-s').value, remark: $('mfr-r').value }), 'Recorded')),
  mfRoute: () => confirmModal('Change the family’s route', 'Every PAN in the family moves. Do this only once the existing units are dealt with (for example, redeemed or switched), and record why.', `<div class="form"><div class="fld"><label for="mfrt-r">Route</label><select class="in" id="mfrt-r">${mfOpts([['Regular', 'Regular'], ['Direct', 'Direct'], ['', 'Clear (the next order sets it)']], '')}</select></div><div class="fld full"><label for="mfrt-n">Reason</label><input class="in" id="mfrt-n"></div></div>`, 'Change', () => act(() => post(`/v1/mf/clients/${encM(MF.ucc)}/route`, { route: $('mfrt-r').value, note: $('mfrt-n').value }), 'Route changed')),
  mfSIP: () => clientCache().then(async () => {
    const plats = await mfReadyPlatforms();
    modal(mhd('Start an SIP', 'An XSIP: each instalment is debited by the investor’s mandate on the platform. Monthly and quarterly instalments fall on the 1st to the 28th.') + `<form class="bd" id="mfsipf"><div class="form">
      <div class="fld full"><label for="mfs-u-q">Investor</label>${cpick('mfs-u', MF.ucc || '', {})}</div>
      <div class="fld"><label for="mfsp-plat">Platform</label><select class="in" id="mfsp-plat">${mfOpts(plats.map(p => [p, mfPlat(p)]), '')}</select></div>
      ${mfSchemePick('mfsp-isin', { platform: 'mfsp-plat' })}
      <div class="fld"><label for="mfsp-amt">Instalment (₹)</label><input class="in mono" id="mfsp-amt"></div><div class="fld"><label for="mfsp-f">Frequency</label><select class="in" id="mfsp-f">${mfOpts(['MONTHLY', 'QUARTERLY', 'WEEKLY'], 'MONTHLY')}</select></div>
      <div class="fld"><label for="mfsp-d">First instalment</label><input class="in" type="date" id="mfsp-d"></div><div class="fld"><label for="mfsp-n">Instalments</label><input class="in mono" id="mfsp-n" value="12"></div>
      <div class="fld"><label for="mfsp-m">Mandate</label><input class="in mono" id="mfsp-m" placeholder="MDT-000001"></div><div class="fld"><label class="check"><input type="checkbox" id="mfsp-fo"> First instalment today (by the platform’s payment link)</label></div></div>
      <div id="mfsp-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Register SIP</button></div></form>`);
    $('mfsipf').onsubmit = async e => { e.preventDefault(); try { const r = await post('/v1/mf/sips', { ucc: $('mfs-u').value, platform: $('mfsp-plat').value, isin: $('mfsp-isin').value, amount: $('mfsp-amt').value.replace(/,/g, ''), frequency: $('mfsp-f').value, start_date: $('mfsp-d').value, installments: +$('mfsp-n').value, mandate_id: $('mfsp-m').value.trim(), first_order_today: $('mfsp-fo').checked }); closeModal(); toast(`${r.id} registered (${r.reg_id})`); render({ keep: true }); } catch (err) { $('mfsp-err').innerHTML = errHTML(err); } };
  }),
  mfSIPCancel: (id, el) => confirmModal('Cancel ' + id, el.dataset.plat === 'BSE' ? 'Cancelled on BSE StAR MF through its API.' : 'Cancel it on NSE MF Invest’s site first (its cancellation API is not used here), then record it.', `<div class="fld"><label for="mfsc-r">Reason</label><input class="in" id="mfsc-r"></div>${el.dataset.plat === 'BSE' ? '' : '<label class="check"><input type="checkbox" id="mfsc-p" checked> Already cancelled on the platform</label>'}`, 'Cancel SIP', () => act(() => post(`/v1/mf/sips/${encM(id)}/cancel`, { reason: $('mfsc-r').value, on_platform: !!$('mfsc-p')?.checked }), id + ' cancelled'), true),
  mfPause: id => { const s = C.mfsips.sips.find(x => x.id === id), dues = []; for (let k = 0; k < s.installments && dues.length < 12; k++) { const d = new Date(s.start_date + 'T00:00:00Z'); if (s.frequency === 'WEEKLY') d.setUTCDate(d.getUTCDate() + 7 * k); else d.setUTCMonth(d.getUTCMonth() + (s.frequency === 'QUARTERLY' ? 3 : 1) * k); const x = d.toISOString().slice(0, 10); if (x >= (s.next_due || '')) dues.push(x); }
    confirmModal('Pause ' + id, 'Pause it on the platform’s site, then record it here. An SIP can be paused twice.', `<div class="form"><div class="fld"><label for="mfpz-f">From the instalment of</label><select class="in" id="mfpz-f">${mfOpts(dues.map(d => [d, fmtDate(d)]), '')}</select></div><div class="fld"><label for="mfpz-n">Instalments to skip</label><input class="in mono" id="mfpz-n" value="1"></div></div><label class="check"><input type="checkbox" id="mfpz-p"> Paused on the platform</label>`, 'Record pause', () => act(() => post(`/v1/mf/sips/${encM(id)}/pause`, { from: $('mfpz-f').value, count: +$('mfpz-n').value, on_platform: $('mfpz-p').checked }), id + ' paused')); },
  mfInst: (id, el) => confirmModal('Instalment · ' + id, 'From the platform’s or bank’s report. Three failures in a row cancel the SIP.', `<div class="form"><div class="fld"><label for="mfi-d">Instalment date</label><input class="in" type="date" id="mfi-d" value="${esc(el.dataset.due)}"></div><div class="fld"><label for="mfi-ok">Result</label><select class="in" id="mfi-ok"><option value="1">Debited</option><option value="0">Failed</option></select></div><div class="fld"><label for="mfi-o">Order number</label><input class="in mono" id="mfi-o"></div><div class="fld"><label for="mfi-r">Reason (failed)</label><input class="in" id="mfi-r" placeholder="e.g. insufficient funds"></div></div>`, 'Record', () => act(() => post(`/v1/mf/sips/${encM(id)}/instalment`, { date: $('mfi-d').value, ok: $('mfi-ok').value === '1', order_no: $('mfi-o').value.trim(), reason: $('mfi-r').value }), 'Recorded')),
  mfSIPLog: id => { const s = C.mfsips.sips.find(x => x.id === id); modal(mhd(esc(id) + ' · ' + esc(s.scheme), esc(s.ucc) + ' · ' + esc(mfPlat(s.platform))) + `<div class="bd">${tbl([['When'], ['What'], ['By']], (s.events || []).slice().reverse().map(e => `<tr><td class="n">${esc(e.at.replace('T', ' ').slice(0, 16))}</td><td class="wrap">${esc(e.what)}</td><td class="dim">${esc(e.by)}</td></tr>`))}</div><div class="ft"><button class="btn pri" data-act="close">Close</button></div>`); },
  mfMandate: () => clientCache().then(() => {
    modal(mhd('Record a mandate', 'Register it on the platform first (the investor authorises an e-mandate online); it is on the investor’s registered bank account, in the clearing corporation’s favour.') + `<form class="bd" id="mfmf"><div class="form">
      <div class="fld full"><label for="mfm-u-q">Investor</label>${cpick('mfm-u', MF.ucc || '', {})}</div>
      <div class="fld"><label for="mfm-p">Platform</label><select class="in" id="mfm-p">${mfOpts([['NSE', 'NSE MF Invest'], ['BSE', 'BSE StAR MF']], '')}</select></div><div class="fld"><label for="mfm-t">Type</label><select class="in" id="mfm-t">${mfOpts(['E-mandate', 'Physical NACH'], 'E-mandate')}</select></div>
      <div class="fld"><label for="mfm-l">Limit a debit (₹, at most 1 crore)</label><input class="in mono" id="mfm-l"></div><div class="fld"><label for="mfm-id">Platform’s mandate number</label><input class="in mono" id="mfm-id"></div>
      <div class="fld"><label for="mfm-umrn">UMRN (once the bank registers it)</label><input class="in mono" id="mfm-umrn"></div><div class="fld"><label for="mfm-s">Status</label><select class="in" id="mfm-s">${mfOpts(['Pending', 'Approved'], 'Pending')}</select></div>
      <div class="fld"><label for="mfm-f">From</label><input class="in" type="date" id="mfm-f" value="${S.date}"></div><div class="fld"><label for="mfm-to">Till (empty: until cancelled)</label><input class="in" type="date" id="mfm-to"></div></div>
      <div id="mfm-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Record</button></div></form>`);
    $('mfmf').onsubmit = async e => { e.preventDefault(); try { const r = await post(`/v1/mf/clients/${encM($('mfm-u').value)}/mandates`, { platform: $('mfm-p').value, type: $('mfm-t').value, limit: $('mfm-l').value.replace(/,/g, ''), platform_id: $('mfm-id').value.trim(), umrn: $('mfm-umrn').value.trim(), status: $('mfm-s').value, from: $('mfm-f').value, till: $('mfm-to').value }); closeModal(); toast(r.id + ' recorded'); render({ keep: true }); } catch (err) { $('mfm-err').innerHTML = errHTML(err); } };
  }),
  mfMandateSt: (id, el) => confirmModal('Mandate ' + id, 'As the platform or bank reports it.', `<div class="form"><div class="fld"><label for="mfms-s">Status</label><select class="in" id="mfms-s">${mfOpts(['Pending', 'Approved', 'Rejected', 'Cancelled'], 'Approved')}</select></div><div class="fld"><label for="mfms-u">UMRN</label><input class="in mono" id="mfms-u"></div><div class="fld full"><label for="mfms-r">Remark</label><input class="in" id="mfms-r"></div></div>`, 'Save', () => act(() => post(`/v1/mf/clients/${encM(el.dataset.ucc)}/mandates/${encM(id)}`, { status: $('mfms-s').value, umrn: $('mfms-u').value.trim(), remark: $('mfms-r').value }), id + ' updated')),
  mfAddUnits: () => clientCache().then(() => {
    modal(mhd('Add units', 'An opening balance at go-live, or a movement from the RTA’s statement. Negative units for units out.') + `<form class="bd" id="mfuf"><div class="form"><div class="fld full"><label for="mfa-uc-q">Investor</label>${cpick('mfa-uc', '', {})}</div>${mfSchemePick('mfa-isin', {})}
      <div class="fld"><label for="mfa-k">Movement</label><select class="in" id="mfa-k">${mfOpts(['Opening', 'Purchase', 'Redemption', 'Switch in', 'Switch out', 'SIP', 'Dividend reinvestment', 'RTA'], 'Opening')}</select></div><div class="fld"><label for="mfa-m">Held in</label><select class="in" id="mfa-m">${mfOpts(['Demat', 'Physical'], 'Demat')}</select></div>
      <div class="fld"><label for="mfa-fo">Folio</label><input class="in mono" id="mfa-fo"></div><div class="fld"><label for="mfa-un">Units</label><input class="in mono" id="mfa-un"></div><div class="fld"><label for="mfa-nv">NAV</label><input class="in mono" id="mfa-nv"></div><div class="fld"><label for="mfa-dt">Date</label><input class="in" type="date" id="mfa-dt" value="${S.date}"></div></div>
      <div id="mfa-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Add</button></div></form>`);
    $('mfuf').onsubmit = async e => { e.preventDefault(); try { await post('/v1/mf/units', { ucc: $('mfa-uc').value, isin: $('mfa-isin').value, kind: $('mfa-k').value, mode: $('mfa-m').value, folio: $('mfa-fo').value.trim(), units: $('mfa-un').value, nav: $('mfa-nv').value, date: $('mfa-dt').value }); closeModal(); toast('Units added'); render({ keep: true }); } catch (err) { $('mfa-err').innerHTML = errHTML(err); } };
  }),
  // The client portal.
  pmfRedeem: (isin, el) => confirmModal('Redeem', 'The fund house pays the proceeds to your registered bank account. Units in demat are redeemed by number of units.', `<div class="form"><div class="fld"><label for="pmr-u">Units (you hold ${esc(nf(el.dataset.units, 3))})</label><input class="in mono" id="pmr-u"></div><div class="fld"><label class="check"><input type="checkbox" id="pmr-all"> All units</label></div><div class="fld"><label for="pmr-p">Platform</label><select class="in" id="pmr-p">${mfOpts(C.pmf.mf.ready.map(p => [p, mfPlat(p)]), '')}</select></div></div>`, 'Redeem', () => act(() => post('/v1/portal/mf/orders', { ucc: UI.pAcct, platform: $('pmr-p').value, type: 'Redemption', isin, units: $('pmr-u').value, all_units: $('pmr-all').checked, folio: el.dataset.folio }), r => `Redemption ${r.order_no || r.id} placed`)),
  pmfSIP: () => { const mf = C.pmf.mf, ms = mf.profile.mandates.filter(m => ['Approved', 'Pending'].includes(m.status));
    modal(mhd('Start an SIP', 'Each instalment is debited from your bank account by your mandate, in the clearing corporation’s favour.') + `<form class="bd" id="pmfsf"><div class="form">${mfSchemePick('pms-isin', { portal: true, plan: mf.route || (mf.offered === 'Both' ? '' : mf.offered), platform: 'pms-plat' })}
      <div class="fld"><label for="pms-plat">Platform</label><select class="in" id="pms-plat">${mfOpts(mf.ready.map(p => [p, mfPlat(p)]), '')}</select></div><div class="fld"><label for="pms-m">Mandate</label><select class="in" id="pms-m">${mfOpts(ms.map(m => [m.id, `${mfPlat(m.platform)} · up to ${inr(m.limit)} · ${m.status}`]), '')}</select></div>
      <div class="fld"><label for="pms-a">Instalment (₹)</label><input class="in mono" id="pms-a"></div><div class="fld"><label for="pms-f">Frequency</label><select class="in" id="pms-f">${mfOpts([['MONTHLY', 'Monthly'], ['QUARTERLY', 'Quarterly'], ['WEEKLY', 'Weekly']], 'MONTHLY')}</select></div>
      <div class="fld"><label for="pms-d">First instalment</label><input class="in" type="date" id="pms-d"></div><div class="fld"><label for="pms-n">Number of instalments</label><input class="in mono" id="pms-n" value="12"></div></div>
      <div id="pms-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Start SIP</button></div></form>`);
    $('pmfsf').onsubmit = async e => { e.preventDefault(); try { await post('/v1/portal/mf/sips', { ucc: UI.pAcct, platform: $('pms-plat').value, isin: $('pms-isin').value, mandate_id: $('pms-m').value, amount: $('pms-a').value.replace(/,/g, ''), frequency: $('pms-f').value, start_date: $('pms-d').value, installments: +$('pms-n').value }); closeModal(); toast('SIP registered'); render({ keep: true }); } catch (err) { $('pms-err').innerHTML = errHTML(err); } }; },
  pmfSIPCancel: id => confirmModal('Cancel this SIP', 'Instalments stop; the units you hold stay in your account. Cancellation closes two working days before an instalment.', '', 'Cancel SIP', () => act(() => post(`/v1/portal/mf/sips/${encM(id)}/cancel`), 'SIP cancelled'), true),
  pmfProfile: () => { const p = C.pmf.mf.profile, noms = p.nominees.length ? p.nominees : [{}];
    modal(mhd('FATCA and nomination', 'FATCA: you confirm you are a tax resident of India only, or tell us otherwise. Nomination: up to 3 nominees for new folios, with shares adding to 100%, or opt out.') + `<form class="bd" id="pmfpf"><label class="check"><input type="checkbox" id="pmp-fatca" ${p.fatca ? 'checked' : ''}> I confirm my FATCA self-declaration: I am a tax resident of India only</label>
      <label class="check"><input type="checkbox" id="pmp-opt" ${p.nomination_opt_out ? 'checked' : ''}> I do not wish to nominate</label>
      ${[0, 1, 2].map(i => { const n = noms[i] || {}; return `<div class="form" data-pmnom style="grid-template-columns:2fr 1fr 1fr;margin-top:6px"><div class="fld"><label>Nominee ${i + 1}</label><input class="in" data-k="name" value="${esc(n.name || '')}"></div><div class="fld"><label>Relationship</label><input class="in" data-k="relation" value="${esc(n.relation || '')}"></div><div class="fld"><label>Share %</label><input class="in mono" data-k="share" value="${esc(n.share || '')}"></div></div>`; }).join('')}
      <div id="pmp-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
    $('pmfpf').onsubmit = async e => { e.preventDefault(); const nominees = [...document.querySelectorAll('[data-pmnom]')].map(r => { const o = {}; r.querySelectorAll('[data-k]').forEach(x => { o[x.dataset.k] = x.value.trim(); }); return o; }).filter(n => n.name);
      try { await post('/v1/portal/mf/profile', { ucc: UI.pAcct, fatca: $('pmp-fatca').checked, nominees: $('pmp-opt').checked ? [] : nominees, nomination_opt_out: $('pmp-opt').checked }); closeModal(); toast('Saved'); render({ keep: true }); } catch (err) { $('pmp-err').innerHTML = errHTML(err); } }; }
});
