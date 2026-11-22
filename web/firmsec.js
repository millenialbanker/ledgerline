'use strict';
/* Ledgerline console: Accounts → Firm securities (screen "firmsec",
   ops/firmsec*.go). The firm's own investments and stock-in-trade: the
   holdings (portfolio → account → scrip → lot), the purchases and sales
   register (the PRO trade files and manual entries), the stock journal
   between the portfolios, the month-end and year-end valuation, the
   reports, and the reconciliation (demat check, pledges, dividends, net
   worth). How the firm keeps them is set in Firm practices. Loaded after
   app.js. */

Object.assign(UI, { fsTab: 'hold', fsOpen: {}, fsFrom: '', fsTo: '', fsValDate: '', fsValPort: '', fsRepKind: 'summary', fsRepPort: '', fsRepFrom: '', fsRepTo: '', fsRepHeads: false, fsRepRes: null, fsRecDate: '' });

const FS_ST = { Posted: 'good', Pending: 'warn', Rejected: 'bad', Cancelled: 'nt', Loaded: 'acc', Agreed: 'good', 'In transit': 'acc', Break: 'bad', Booked: 'acc', Received: 'good', Open: 'warn', Released: 'nt', Invoked: 'bad' };
const fsPort = p => ({ INV: 'Investments', SIT: 'Stock-in-trade' }[p] || p);
const fsMk = () => !!C.firmsec?.hold?.can_enter, fsCk = () => !!C.firmsec?.hold?.can_check;

V.firmsec = {
  load: async () => {
    const tab = UI.fsTab, d = S.date;
    const hold = await get(`/v1/firm-securities?date=${d}`);
    const out = { hold, tab };
    if (hold.settings.portfolios === 'none') return out;
    const ports = hold.portfolios.map(p => p.portfolio);
    if (tab === 'reg') out.reg = await get(`/v1/firm-securities/register?from=${UI.fsFrom || d.slice(0, 8) + '01'}&to=${UI.fsTo || d}`);
    if (tab === 'sj') out.sj = (await get('/v1/firm-securities/journals')).journals;
    if (tab === 'val') { const p = ports.includes(UI.fsValPort) ? UI.fsValPort : ports.includes('SIT') ? 'SIT' : ports[0]; UI.fsValPort = p; out.val = await get(`/v1/firm-securities/valuation?date=${UI.fsValDate || d}&portfolio=${p}`); }
    if (tab === 'rep') out.defs = (await get('/v1/firm-securities/reports')).reports;
    if (tab === 'rec') {
      const rd = UI.fsRecDate || d;
      const [rec, nw, pl] = await Promise.all([get(`/v1/firm-securities/recon?date=${rd}`), get(`/v1/firm-securities/net-worth?date=${rd}`), get(`/v1/firm-securities/reports/pledges?to=${rd}`)]);
      Object.assign(out, { rec, nw, pl });
    }
    return out;
  },
  html: d => {
    const h = d.hold, s = h.settings;
    const head = ph('Firm securities', 'The firm’s own securities: investments held as lots at cost, and stock-in-trade kept as a purchase and sale book. Trades under the firm’s own codes come in from the exchange trade files; everything else is entered by hand and approved by a second person as the firm’s practice says.',
      `${h.can_admin ? '<button class="btn" data-act="go" data-id="practices">Firm practices</button>' : ''}${h.can_enter ? `<button class="btn" data-act="fsSync">Bring in own trades</button><button class="btn pri" data-act="fsNew">New entry</button>` : ''}`);
    if (s.portfolios === 'none') {
      return ph('Firm securities', 'The firm’s own investments and stock-in-trade.') + `<div class="panel"><div class="empty">The firm keeps no securities of its own in Ledgerline yet. An Admin answers “Where does the firm keep its own securities?” in <button class="linkish" data-act="go" data-id="practices">Settings → Firm practices</button> (Firm securities), and the rest of the questions there.</div></div>`;
    }
    const kp = h.portfolios.map(p => kpi(esc(p.name), cmp(p.cost), `at cost · market ${cmp(p.market)}`)).join('') +
      kpi('Entries to approve', h.pending, h.journals_pending ? `${h.journals_pending} stock journal${h.journals_pending > 1 ? 's' : ''} too` : 'manual and off-market', h.pending + h.journals_pending ? 'warn' : 'good') +
      (h.loaded ? kpi('Opening lots to post', h.loaded, 'loaded from the old system', 'warn') : '');
    const warn = (h.warnings || []).map(w => `<div class="note warn">${esc(w)}</div>`).join('');
    const tabs = [['hold', 'Holdings'], ['reg', 'Purchases & sales'], ['sj', 'Stock journal'], ['val', 'Valuation'], ['rep', 'Reports'], ['rec', 'Reconciliation']];
    const seg = `<div class="seg" role="tablist">${tabs.map(([k, l]) => `<button role="tab" aria-selected="${UI.fsTab === k}" class="${UI.fsTab === k ? 'on' : ''}" data-act="fsTab" data-id="${k}">${l}</button>`).join('')}</div>`;
    const body = (P['fs-' + d.tab] || P['fs-hold'])(d);
    return head + `<div class="kpis">${kp}</div>` + warn + `<div class="panel"><div class="hd">${seg}<small class="dim">${esc(s.portfolios === 'both' ? 'Investments and stock-in-trade' : fsPort(s.portfolios.toUpperCase()))} · own codes ${esc((h.codes || []).join(', ') || 'none')}</small></div>${body}</div>`;
  }
};

/* ---------- holdings: portfolio → account → scrip → lot ---------- */
P['fs-hold'] = d => {
  const h = d.hold, open = k => !!UI.fsOpen[k];
  const tog = (k, label) => `<button type="button" class="bk-tog" data-act="fsTog" data-id="${esc(k)}" aria-expanded="${open(k)}" title="${open(k) ? 'Close' : 'Open'}">${open(k) ? '▾' : '▸'}</button>${label}`;
  let out = '';
  if (h.loaded) out += `<div class="note warn" style="margin:12px 14px 0"><b>${h.loaded} opening lots</b> loaded from the old system count in the holdings but are not in the books yet. ${h.can_check ? 'Check them, then post them.' : 'A checker posts them.'} ${h.can_check ? '<button class="btn sm pri" data-act="fsOpening">Post opening lots</button>' : ''}</div>`;
  for (const p of h.portfolios) {
    const rows = [];
    for (const a of p.accounts) {
      const ka = 'a:' + a.account;
      rows.push(`<tr class="bk-head lv2"><td>${tog(ka, `<b>${esc(a.name)}</b> <span class="dim small">${a.scrips.length} scrip${a.scrips.length === 1 ? '' : 's'}</span>`)}</td><td></td><td class="r">${inr(a.cost, 2)}</td><td class="r">${inr(a.market, 2)}</td><td class="r">${p.portfolio === 'INV' ? `<span class="${num(a.book) !== num(a.cost) ? 'neg' : 'dim'}">${inr(a.book, 2)}</span>` : '<span class="dim small">via closing stock</span>'}</td></tr>`);
      if (!open(ka)) continue;
      for (const sc of a.scrips) {
        const ks = 's:' + a.account + ':' + sc.isin;
        rows.push(`<tr class="bk-acct"><td style="padding-left:28px">${tog(ks, `${esc(sc.symbol)} <span class="dim small mono">${esc(sc.isin)}</span>${num(sc.pledged) ? ' ' + pill(nf(sc.pledged) + ' pledged', 'warn') : ''}`)}</td><td class="r">${nf(sc.qty, 4).replace(/\.?0+$/, '')}</td><td class="r">${inr(sc.cost, 2)}</td><td class="r">${inr(sc.market, 2)}${num(sc.price) ? `<div class="dim small">@ ${nf(sc.price, 2)}</div>` : ''}</td><td></td></tr>`);
        if (!open(ks)) continue;
        for (const l of sc.lots) {
          rows.push(`<tr><td style="padding-left:58px" class="small">${fmtDate(l.date)} <span class="dim">· ${esc(l.source)}${l.term_now ? ' · ' + (l.term_now === 'LT' ? 'long-term' : 'current') : ''}${l.converted ? ' · converted ' + fmtDate(l.converted.on) : ''}${l.tax_date ? ' · tax date ' + fmtDate(l.tax_date) : ''}</span> <span class="dim mono">${esc(l.txn)}</span></td><td class="r small">${nf(l.qty, 4).replace(/\.?0+$/, '')}</td><td class="r small">${inr(l.cost, 2)}</td><td class="r dim small">${l.days} days</td><td></td></tr>`);
        }
      }
    }
    out += `<div class="pad" style="padding-bottom:0"><h3 style="margin:0">${esc(p.name)}${p.demat ? ` <span class="dim small mono">demat ${esc(p.demat)}</span>` : ''}</h3></div>` +
      tbl([['Account / scrip / lot'], ['Qty', 'r'], ['Cost', 'r'], ['Market value', 'r'], ['In the books', 'r']], rows, { empty: 'Nothing held on this date.' });
  }
  const own = (h.kinds || []).filter(k => k.own);
  if (own.length) out += `<div class="pad"><h4 style="margin:0 0 6px">Instrument types of the firm’s own</h4>${tbl([['Type'], ['ICDS VIII category'], ['Capital gains'], ['']], own.map(k => `<tr><td>${esc(k.name)}${k.asked ? '' : ' ' + pill('Say how it is treated', 'warn')}</td><td>${esc(k.category)}</td><td>${esc(k.tax)}</td><td>${h.can_admin ? `<button class="btn sm" data-act="fsKind" data-id="${esc(k.id)}">Set treatment</button>` : ''}</td></tr>`))}</div>`;
  if ((h.events || []).length) out += `<div class="pad" style="padding-top:0"><h4 style="margin:0 0 6px">Corporate actions on the firm’s lots (last 90 days)</h4>${tbl([['Ex-date'], ['Portfolio'], ['Scrip'], ['Action'], ['Before', 'r'], ['After', 'r']], h.events.map(e => `<tr><td class="n">${fmtDate(e.date)}</td><td>${esc(fsPort(e.portfolio))}</td><td>${esc(e.symbol)}</td><td class="wrap small">${esc(e.detail)}</td><td class="r">${nf(e.before)}</td><td class="r">${nf(e.after)}</td></tr>`))}</div>`;
  out += `<div class="pad"><span class="dim small">Balances as at ${fmtDate(h.date)}, first in, first out. Investments: lots marked long-term or current. Stock-in-trade reaches the books through the closing stock entry (Valuation). Rates now: ${(h.tax_rates || []).map(r => esc(r.label) + ' ' + esc(r.value)).join(' · ')}.</span></div>`;
  return out;
};

/* ---------- purchases and sales ---------- */
P['fs-reg'] = d => {
  const r = d.reg, me = S.me.actor;
  const acts = l => {
    const b = [];
    if (l.changeable && r.can_enter) b.push(`<button class="btn sm" data-act="fsChange" data-id="${esc(l.id)}">Change</button>`);
    if (l.status === 'Pending' && r.can_check && l.maker !== me) b.push(`<button class="btn sm pri" data-act="fsDecide" data-id="${esc(l.id)}" data-a="approve">Approve</button><button class="btn sm bad" data-act="fsDecide" data-id="${esc(l.id)}" data-a="reject">Reject</button>`);
    if (l.status === 'Pending' && l.maker === me) b.push('<span class="dim small">another person approves</span>');
    if (l.status === 'Posted' && r.can_check && !['Trade file', 'Stock journal', 'Opening'].includes(l.type)) b.push(`<button class="btn sm" data-act="fsDecide" data-id="${esc(l.id)}" data-a="cancel">Cancel</button>`);
    return `<div class="split">${b.join('')}</div>`;
  };
  return `<div class="pad" style="padding-bottom:0"><div class="tools"><input class="in sm" type="date" id="fs-from" value="${esc(r.from)}" aria-label="From"><span class="dim">to</span><input class="in sm" type="date" id="fs-to" value="${esc(r.to)}" aria-label="To"><button class="btn sm" data-act="fsPeriod">Show</button>${r.pending ? pill(r.pending + ' waiting for approval', 'warn') : ''}</div></div>` +
    tbl([['Date'], ['Line'], ['Portfolio'], ['Scrip'], ['B/S'], ['Qty', 'r'], ['Value', 'r'], ['Charges', 'r'], ['Status'], ['']], r.lines.map(l => `<tr><td class="n">${fmtDate(l.date)}</td><td class="small" title="${esc(l.id)}">${esc(l.type)}<div class="dim">${l.code ? esc(l.code) + ' · ' + esc(l.exchange) : `<span class="mono">${esc(l.id)}</span>`}</div></td>
      <td>${esc(l.portfolio_name)}${l.default_portfolio && l.default_portfolio !== l.portfolio ? `<div class="dim small">code’s default: ${esc(fsPort(l.default_portfolio))}</div>` : ''}<div class="dim small">${esc(l.kind_name)}${l.term ? ' · ' + (l.term === 'LT' ? 'long-term' : 'current') : ''}</div></td>
      <td>${esc(l.symbol)}<div class="dim small mono">${esc(l.isin)}</div></td><td>${l.side === 'B' ? 'Buy' : 'Sell'}</td>
      <td class="r">${nf(l.qty)}${l.spec_qty ? `<div class="dim small">${nf(l.spec_qty)} intraday</div>` : ''}</td><td class="r">${inr(l.value, 2)}${l.gain ? `<div class="dim small ${sgn(l.gain)}">gain ${inr(l.gain, 2)}</div>` : ''}</td>
      <td class="r">${inr(sum(Object.values(l.charges || {})), 2)}</td><td>${pill(l.status, FS_ST[l.status])}${(l.warnings || []).map(w => `<div class="small" style="color:var(--warn,#9a6700)">${esc(w)}</div>`).join('')}${l.reason ? `<div class="dim small wrap">${esc(l.reason)}</div>` : ''}</td><td>${acts(l)}</td></tr>`), { tall: true, empty: 'Nothing in this period. Click Bring in own trades after billing, or New entry.' }) +
    (r.events.length ? `<div class="pad"><span class="dim small">${r.events.length} corporate action${r.events.length > 1 ? 's' : ''} changed the lots in this period (Holdings).</span></div>` : '');
};

/* ---------- stock journal ---------- */
P['fs-sj'] = d => {
  const me = S.me.actor, ck = fsCk();
  return `<div class="pad" style="padding-bottom:0"><div class="tools">${fsMk() && d.hold.settings.moves === 'journal' ? '<button class="btn sm pri" data-act="fsJournal">New stock journal</button>' : ''}<span class="dim small">${d.hold.settings.moves === 'journal' ? 'Out at cost, in at the day’s market value; a second person approves.' : 'The firm’s practice is that holdings do not move between the portfolios.'}</span></div></div>` +
    tbl([['Journal'], ['Date'], ['From → to'], ['Scrip'], ['Qty', 'r'], ['Out at cost', 'r'], ['In at market', 'r'], ['Difference', 'r'], ['Tax'], ['Status'], ['']], d.sj.map(j => `<tr><td class="mono">${esc(j.id)}<div class="dim small wrap">${esc(j.reason)}</div></td><td class="n">${fmtDate(j.date)}</td><td>${esc(fsPort(j.from))} → ${esc(fsPort(j.to))}</td><td>${esc(j.symbol)}<div class="dim small mono">${esc(j.isin)}</div></td><td class="r">${nf(j.qty)}</td><td class="r">${inr(j.out_cost, 2)}</td><td class="r">${inr(j.in_value, 2)}<div class="dim small">${esc(j.price_from)}</div></td><td class="r ${sgn(j.difference)}">${inr(j.difference, 2)}</td><td class="small wrap">${esc(j.tax_note)}<div class="dim">${fmtDate(j.tax_date)} · ${inr(j.tax_value, 2)}</div></td><td>${pill(j.status, FS_ST[j.status])}<div class="dim small">${esc(j.maker)}${j.checker ? ' · ' + esc(j.checker) : ''}</div></td><td>${j.status === 'Pending' && ck && j.maker !== me ? `<div class="split"><button class="btn sm pri" data-act="fsSJDecide" data-id="${esc(j.id)}" data-a="1">Approve</button><button class="btn sm bad" data-act="fsSJDecide" data-id="${esc(j.id)}" data-a="0">Reject</button></div>` : ''}</td></tr>`), { empty: 'No stock journals yet.' });
};

/* ---------- valuation ---------- */
P['fs-val'] = d => {
  const v = d.val, ports = d.hold.portfolios.map(p => p.portfolio), me = S.me.actor;
  const sit = v.portfolio === 'SIT';
  return `<div class="pad" style="padding-bottom:0"><div class="tools"><input class="in sm" type="date" id="fs-vdate" value="${esc(v.date)}" aria-label="Valuation date">${ports.length > 1 ? `<select class="in sm" id="fs-vport" aria-label="Portfolio">${ports.map(p => `<option value="${p}" ${p === v.portfolio ? 'selected' : ''}>${fsPort(p)}</option>`).join('')}</select>` : ''}<button class="btn sm" data-act="fsVal">Show</button>${fsMk() && v.proposed.length ? '<button class="btn sm pri" data-act="fsValPropose">Propose the entry</button>' : ''}</div>
    <p class="small">Taken by the books: <b>${esc(v.basis_name)}</b>.${v.no_price ? ` ${v.no_price} listed scrip${v.no_price > 1 ? 's have' : ' has'} no price on this date and stay${v.no_price > 1 ? '' : 's'} at cost.` : ''}</p>${v.warnings.map(w => `<div class="note warn">${esc(w)}</div>`).join('')}</div>
    <div class="kpis" style="padding:0 14px">${kpi('At cost', inr(v.at_cost, 2))}${kpi('Market value', inr(v.market, 2))}${sit ? kpi('Lower, scrip by scrip', inr(v.lcm_scrip, 2)) + kpi('Lower, category by category', inr(v.lcm_category, 2), 'ICDS VIII') : ''}${kpi('Value taken', inr(v.value, 2), '', 'good')}</div>` +
    tbl([['Scrip'], ['Kind'], ...(sit ? [] : [['Term']]), ['Qty', 'r'], ['Cost', 'r'], ['Price', 'r'], ['Market', 'r'], ['Lower', 'r'], ['Value', 'r']], v.rows.map(r => `<tr><td>${esc(r.symbol)}<div class="dim small mono">${esc(r.isin)}</div></td><td class="small">${esc(r.kind_name)}</td>${sit ? '' : `<td>${r.term === 'LT' ? 'Long-term' : 'Current'}</td>`}<td class="r">${nf(r.qty)}</td><td class="r">${inr(r.cost, 2)}</td><td class="r">${r.has_price ? nf(r.price, 2) : `<span class="dim small">${r.listed ? 'no price' : 'unlisted'}</span>`}</td><td class="r">${inr(r.market, 2)}</td><td class="r">${inr(r.lcm, 2)}</td><td class="r"><b>${inr(r.value, 2)}</b></td></tr>`), { empty: 'Nothing held on this date.' }) +
    (sit && v.categories?.length ? `<div class="pad" style="padding-bottom:0"><h4 style="margin:0 0 6px">Category by category (ICDS VIII)</h4>${tbl([['Category'], ['Cost (quoted)', 'r'], ['Market (quoted)', 'r'], ['Not quoted: at cost', 'r'], ['Value', 'r']], v.categories.map(c => `<tr><td>${esc(c.name)}</td><td class="r">${inr(c.cost, 2)}</td><td class="r">${inr(c.market, 2)}</td><td class="r">${inr(c.at_cost, 2)}</td><td class="r"><b>${inr(c.value, 2)}</b></td></tr>`))}</div>` : '') +
    `<div class="pad"><h4 style="margin:0 0 6px">Proposed entry</h4>${tbl([['Account'], ['In the books', 'r'], ['Should be', 'r'], ['Debit', 'r'], ['Credit', 'r']], v.proposed.map(p => `<tr><td>${esc(p.name)}</td><td class="r">${inr(p.balance, 2)}</td><td class="r">${inr(p.target, 2)}</td><td class="r">${num(p.change) > 0 ? inr(p.change, 2) : ''}</td><td class="r">${num(p.change) < 0 ? inr(-num(p.change), 2) : ''}</td></tr>`), { empty: 'The books already carry this value.' })}
    ${v.saved.length ? `<h4 style="margin:14px 0 6px">Valuation entries</h4>${tbl([['Entry'], ['Date'], ['Value', 'r'], ['Status'], ['']], v.saved.map(x => `<tr><td class="mono small">${esc(x.id)}</td><td class="n">${fmtDate(x.date)}</td><td class="r">${inr(x.value, 2)}</td><td>${pill(x.status, FS_ST[x.status])}<div class="dim small">${esc(x.maker)}${x.checker ? ' · ' + esc(x.checker) : ''}</div></td><td>${x.status === 'Pending' && fsCk() && x.maker !== me ? `<div class="split"><button class="btn sm pri" data-act="fsValDecide" data-id="${esc(x.id)}" data-a="1">Post</button><button class="btn sm bad" data-act="fsValDecide" data-id="${esc(x.id)}" data-a="0">Reject</button></div>` : ''}</td></tr>`))}` : ''}</div>`;
};

/* ---------- reports ---------- */
function fsRepURL(fmt) {
  const def = (C.firmsec.defs || []).find(x => x.kind === UI.fsRepKind) || {};
  const q = new URLSearchParams({ to: UI.fsRepTo || S.date });
  if (def.range) q.set('from', UI.fsRepFrom || fyStart(S.date));
  if (UI.fsRepPort) q.set('portfolio', UI.fsRepPort);
  if (UI.fsRepHeads) q.set('heads', '1');
  if (fmt) q.set('format', fmt);
  return `/v1/firm-securities/reports/${UI.fsRepKind}?${q}`;
}
function fsRepOut(r) {
  if (!r) return '<div class="empty">Choose a report and period, then View.</div>';
  if (r.error) return errHTML(r.error);
  const sv = s => s.type === 'money' ? inr(s.value, 2) : esc(s.value);
  return `<div class="pad" style="padding-bottom:0"><h3 style="margin:0">${esc(r.title)}</h3><span class="dim small">${esc(r.name)} · ${esc(r.period)} · generated ${esc(r.generated)}</span></div>
    <div class="kpis" style="padding:0 14px">${(r.summary || []).map(s => kpi(esc(s.label), sv(s), '', s.tone === 'good' ? 'good' : s.tone === 'bad' ? 'bad' : '')).join('')}</div>` +
    (r.sections || []).map(sec => `<div class="pad" style="padding-bottom:0"><h4 style="margin:0 0 6px">${esc(sec.title)}</h4>${tbl(sec.columns.map(c => [esc(c.label), crR(c.type)]), (sec.rows || []).map(row => `<tr>${row.map((v, i) => `<td class="${crR(sec.columns[i]?.type)}${sec.columns[i]?.type === 'text' && v.length > 40 ? ' wrap' : ''}">${crCell(sec.columns[i]?.type, v)}</td>`).join('')}</tr>`),
      { tall: (sec.rows || []).length > 14, empty: esc(sec.empty || 'Nothing to show'), foot: sec.totals?.length ? `<tfoot><tr>${sec.totals.map((v, i) => `<td class="${crR(sec.columns[i]?.type)}">${i === 0 ? esc(v) : crCell(sec.columns[i]?.type, v)}</td>`).join('')}</tr></tfoot>` : '' })}${sec.note ? `<div class="dim small" style="margin-top:6px">${esc(sec.note)}</div>` : ''}</div>`).join('') +
    ((r.notes || []).length ? `<div class="pad"><div class="note small">${r.notes.map(esc).join('<br>')}</div></div>` : '<div class="pad"></div>');
}
P['fs-rep'] = d => {
  const defs = d.defs || [], def = defs.find(x => x.kind === UI.fsRepKind) || defs[0] || {}, ports = d.hold.portfolios.map(p => p.portfolio);
  return `<div class="pad" style="padding-bottom:0"><div class="form" style="grid-template-columns:repeat(auto-fit,minmax(160px,1fr))">
    <div class="fld"><label for="fs-rk">Report</label><select class="in" id="fs-rk">${defs.map(x => `<option value="${x.kind}" ${x.kind === def.kind ? 'selected' : ''}>${esc(x.name)}</option>`).join('')}</select></div>
    ${ports.length > 1 && !['pledges', 'cg'].includes(def.kind) ? `<div class="fld"><label for="fs-rp">Portfolio</label><select class="in" id="fs-rp">${ports.map(p => `<option value="${p}" ${p === (UI.fsRepPort || (ports.includes('SIT') ? 'SIT' : ports[0])) ? 'selected' : ''}>${fsPort(p)}</option>`).join('')}</select></div>` : ''}
    ${def.range ? `<div class="fld"><label for="fs-rf">From</label><input class="in" type="date" id="fs-rf" value="${esc(UI.fsRepFrom || fyStart(S.date))}"></div>` : ''}
    <div class="fld"><label for="fs-rt">${def.range ? 'To' : 'As on'}</label><input class="in" type="date" id="fs-rt" value="${esc(UI.fsRepTo || S.date)}"></div></div>
    <p class="dim small" style="margin:4px 0 8px">${esc(def.about || '')}</p>
    <div class="tools">${['summary', 'register'].includes(def.kind) ? `<label class="check"><input type="checkbox" id="fs-rh" ${UI.fsRepHeads ? 'checked' : ''}> Show each cost head as its own column</label>` : ''}<button class="btn pri sm" data-act="fsRep">View</button><button class="btn sm" data-act="fsRepCSV">CSV</button><button class="btn sm" data-act="fsRepPrint">Print</button></div></div>
    <div id="fs-rep-out">${fsRepOut(UI.fsRepRes)}</div>`;
};

/* ---------- reconciliation: demat, pledges, dividends, net worth ---------- */
P['fs-rec'] = d => {
  const r = d.rec, nw = d.nw, pl = d.pl, h = d.hold, mk = fsMk();
  const pledges = pl.sections[0];
  const demat = `<div class="pad" style="padding-bottom:0"><div class="tools"><input class="in sm" type="date" id="fs-rdate" value="${esc(r.date)}" aria-label="Date"><button class="btn sm" data-act="fsRec">Show</button>${mk ? '<button class="btn sm" data-act="fsDemat">Enter demat holdings</button>' : ''}${r.breaks ? pill(r.breaks + ' difference' + (r.breaks > 1 ? 's' : ''), 'bad') : pill('No differences', 'good')}${r.transit ? pill(r.transit + ' in transit', 'acc') : ''}</div>
    <h4 style="margin:12px 0 6px">Demat check</h4><p class="dim small" style="margin:0 0 6px">${Object.entries(r.sources).map(([k, v]) => `${esc(k)}: ${esc(v)}`).join(' · ')}. A purchase counts in the demat from its pay-out date, a sale until its pay-in date.</p></div>` +
    tbl([['Demat'], ['Scrip'], ['Register', 'r'], ['Coming in', 'r'], ['Going out', 'r'], ['Expected', 'r'], ['In the demat', 'r'], ['Difference', 'r'], ['Status']], r.rows.map(x => `<tr><td class="mono small">${esc(x.demat)}</td><td>${esc(x.symbol)}<div class="dim small mono">${esc(x.isin)}</div></td><td class="r">${nf(x.register)}</td><td class="r">${num(x.transit_in) ? nf(x.transit_in) : ''}</td><td class="r">${num(x.transit_out) ? nf(x.transit_out) : ''}</td><td class="r">${nf(x.expected)}</td><td class="r">${nf(x.demat_qty)}${num(x.demat_pledged) ? `<div class="dim small">${nf(x.demat_pledged)} pledged</div>` : ''}</td><td class="r ${sgn(x.difference)}">${num(x.difference) ? nf(x.difference) : ''}</td><td>${pill(x.status, FS_ST[x.status])}</td></tr>`), { empty: r.missing.length ? 'No demat holdings for the firm’s accounts yet: enter them, or load the DP holdings.' : 'Nothing held.' });
  const pled = `<div class="pad" style="padding-bottom:0"><div class="tools"><h4 style="margin:0">Pledges of own securities</h4>${mk || has('Risk manager') ? '<button class="btn sm" data-act="fsPledge">New pledge</button>' : ''}<span class="dim small">Tracked, not blocked: a sale into pledged units goes through with a warning.</span></div></div>` +
    tbl([['Pledge'], ['Portfolio'], ['Scrip'], ['Qty', 'r'], ['Pledgee'], ['At cost', 'r'], ['At market', 'r'], ['']], pledges.rows.map(x => `<tr><td class="mono small">${esc(x[0])}<div class="dim">${fmtDate(x[1])}</div></td><td>${esc(x[2])}</td><td>${esc(x[3])}</td><td class="r">${nf(x[5])}<div class="dim small">of ${nf(x[6])} held</div></td><td>${esc(x[7])}<div class="dim small">${esc(x[8])}${x[9] ? ' · ' + esc(x[9]) : ''}</div></td><td class="r">${inr(x[11], 2)}</td><td class="r">${inr(x[12], 2)}</td><td>${mk || has('Risk manager') ? `<div class="split"><button class="btn sm" data-act="fsPledgeClose" data-id="${esc(x[0])}" data-a="release">Release</button><button class="btn sm bad" data-act="fsPledgeClose" data-id="${esc(x[0])}" data-a="invoke">Invoked</button></div>` : ''}</td></tr>`), { empty: 'Nothing pledged on this date.' });
  const inc = `<div class="pad" style="padding-bottom:0"><div class="tools"><h4 style="margin:0">Dividends and interest</h4>${mk ? '<button class="btn sm" data-act="fsIncome">Enter dividend or interest</button><button class="btn sm pri" data-act="fsMatch">Match bank credits</button>' : ''}<span class="dim small">${h.settings.income === 'auto' ? 'Dividends are booked on the record date with TDS as receivable.' : 'Entered by hand (the firm’s practice).'}</span></div></div>` +
    tbl([['Booked'], ['Scrip'], ['Kind'], ['Qty', 'r'], ['Gross', 'r'], ['TDS', 'r'], ['Net', 'r'], ['Expected'], ['Status']], (h.income || []).map(x => `<tr><td class="n">${fmtDate(x.date)}</td><td>${esc(x.symbol)}<div class="dim small">${esc(fsPort(x.portfolio))}</div></td><td>${esc(x.kind)}</td><td class="r">${nf(x.qty)}</td><td class="r">${inr(x.gross, 2)}</td><td class="r">${inr(x.tds, 2)}</td><td class="r">${inr(x.net, 2)}</td><td class="n">${fmtDate(x.pay_date)}</td><td>${pill(x.status, FS_ST[x.status])}${x.received ? `<div class="dim small">${fmtDate(x.received)} · ${esc(x.bank_ref)}</div>` : ''}</td></tr>`), { empty: 'No dividends or interest booked yet.' });
  const nwT = `<div class="pad" style="padding-bottom:0"><div class="tools"><h4 style="margin:0">Net worth on ${fmtDate(nw.date)}</h4>${pill('Provisional layout', 'warn')}${has('Ops checker', 'Compliance officer') ? '<button class="btn sm" data-act="fsNWInputs">Figures not in the books</button>' : ''}</div></div>` +
    tbl([['Line'], ['Amount', 'r'], ['From']], nw.lines.map(l => `<tr class="${l.sign === 0 ? 'bk-head' : ''}"><td>${l.sign === 0 ? `<b>${esc(l.label)}</b>` : (l.sign < 0 ? 'Less: ' : '') + esc(l.label)}</td><td class="r">${l.sign === 0 ? `<b>${inr(l.amount, 2)}</b>` : inr(l.amount, 2)}</td><td class="dim small">${esc(l.source)}</td></tr>`)) +
    `<div class="pad"><div class="kpis" style="margin:0">${kpi('Net worth', inr(nw.net_worth, 2))}${kpi('Required', inr(nw.required, 2), 'higher of base and variable')}${kpi('Surplus', inr(nw.surplus, 2), '', num(nw.surplus) >= 0 ? 'good' : 'bad')}</div><div class="note small">${nw.notes.map(esc).join('<br>')}</div></div>`;
  return demat + pled + inc + nwT;
};

/* ---------- actions ---------- */
const fsCharges = () => `<div class="form" style="grid-template-columns:repeat(auto-fit,minmax(120px,1fr))">${[['brokerage', 'Brokerage'], ['stt', 'STT'], ['exchange', 'Exchange and SEBI'], ['stamp', 'Stamp duty'], ['gst', 'GST'], ['other', 'Other']].map(([k, l]) => `<div class="fld"><label for="fsc-${k}">${l}</label><input class="in" id="fsc-${k}" inputmode="decimal" placeholder="0"></div>`).join('')}</div>`;
const fsPortSel = (id, def) => { const ps = C.firmsec.hold.portfolios.map(p => p.portfolio); return `<select class="in" id="${id}">${ps.map(p => `<option value="${p}" ${p === def ? 'selected' : ''}>${fsPort(p)}</option>`).join('')}</select>`; };
const fsVal = id => ($(id)?.value || '').trim();

Object.assign(A, {
  fsTab: id => { UI.fsTab = id; render({ keep: true }); },
  fsTog: id => { UI.fsOpen[id] = !UI.fsOpen[id]; $('page').innerHTML = groupBar(S.view) + V.firmsec.html(C.firmsec); bindPage(); },
  fsPeriod: () => { UI.fsFrom = fsVal('fs-from'); UI.fsTo = fsVal('fs-to'); render({ keep: true }); },
  fsVal: () => { UI.fsValDate = fsVal('fs-vdate'); UI.fsValPort = $('fs-vport')?.value || UI.fsValPort; render({ keep: true }); },
  fsRec: () => { UI.fsRecDate = fsVal('fs-rdate'); render({ keep: true }); },
  fsSync: () => confirmModal('Bring in the firm’s own trades', `Brings the trades of ${fmtDate(S.date)} under the firm’s own codes (${esc((C.firmsec.hold.codes || []).join(', ') || 'none set')}) from the exchange trade files into the register, with the bill’s charges. Safe to run again.`, '', 'Bring them in',
    () => act(() => post('/v1/firm-securities/sync', { date: S.date }), r => `${r.lines} lines: ${r.new} new, ${r.changed} changed${r.cancelled ? ', ' + r.cancelled + ' cancelled' : ''}${r.warnings.length ? ' · ' + r.warnings[0] : ''}`)),
  fsNew: () => {
    modal(mhd('New entry', 'A purchase or sale outside the trade files: off-market, a buy-back accepted, or a manual correction. It waits for a second person as the firm’s practice says.') + `<form class="bd" id="fsnf"><div class="form">
      <div class="fld"><label for="fsn-type">Type</label><select class="in" id="fsn-type"><option>Manual</option><option>Off-market</option><option>Buy-back</option></select></div>
      <div class="fld"><label for="fsn-side">Purchase or sale</label><select class="in" id="fsn-side"><option value="B">Purchase</option><option value="S">Sale</option></select></div>
      <div class="fld"><label for="fsn-date">Date</label><input class="in" type="date" id="fsn-date" value="${S.date}" required></div>
      <div class="fld"><label for="fsn-port">Portfolio</label>${fsPortSel('fsn-port')}</div>
      <div class="fld"><label for="fsn-isin">ISIN</label><input class="in mono" id="fsn-isin" maxlength="12" required></div>
      <div class="fld"><label for="fsn-sym">Scrip</label><input class="in" id="fsn-sym" required></div>
      <div class="fld"><label for="fsn-qty">Quantity</label><input class="in" id="fsn-qty" inputmode="decimal" required></div>
      <div class="fld"><label for="fsn-price">Price per unit (₹)</label><input class="in" id="fsn-price" inputmode="decimal" required></div>
      <div class="fld"><label for="fsn-kind">Kind</label><select class="in" id="fsn-kind"><option value="">From the ISIN</option>${(C.firmsec.hold.kinds || []).map(k => `<option value="${esc(k.id)}">${esc(k.name)}</option>`).join('')}</select></div>
      <div class="fld"><label for="fsn-term">Investment is</label><select class="in" id="fsn-term"><option value="LT">Long-term</option><option value="CUR">Current</option></select></div>
      <div class="fld"><label for="fsn-pay">Reaches or leaves the demat on</label><input class="in" type="date" id="fsn-pay"></div>
      <div class="fld"><label for="fsn-tds">TDS (buy-back)</label><input class="in" id="fsn-tds" inputmode="decimal" placeholder="0"></div>
      <div class="fld full"><label for="fsn-note">Note</label><input class="in" id="fsn-note" maxlength="300"></div></div>
      <h4 style="margin:10px 0 4px">Charges</h4>${fsCharges()}<div id="fsn-err"></div>
      <div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save entry</button></div></form>`);
    $('fsnf').onsubmit = async e => {
      e.preventDefault();
      const ch = {}; ['brokerage', 'stt', 'exchange', 'stamp', 'gst', 'other'].forEach(k => { ch[k] = fsVal('fsc-' + k) || '0'; });
      try {
        const r = await post('/v1/firm-securities/entries', { type: $('fsn-type').value, side: $('fsn-side').value, date: fsVal('fsn-date'), portfolio: $('fsn-port').value, isin: fsVal('fsn-isin'), symbol: fsVal('fsn-sym'), qty: fsVal('fsn-qty'), price: fsVal('fsn-price'), kind: $('fsn-kind').value, term: $('fsn-term').value, pay_date: fsVal('fsn-pay'), tds: fsVal('fsn-tds'), note: fsVal('fsn-note'), charges: ch, counter: 'BANK:OWN' });
        closeModal(); toast(`${r.id}: ${r.status === 'Pending' ? 'waits for a second person' : 'posted'}${(r.warnings || []).length ? ' · ' + r.warnings.join('; ') : ''}`, (r.warnings || []).length > 0); UI.fsTab = 'reg'; render({ keep: true });
      } catch (err) { $('fsn-err').innerHTML = errHTML(err); }
    };
  },
  fsChange: id => {
    const l = C.firmsec.reg.lines.find(x => x.id === id); if (!l) return;
    confirmModal('Change the line', `${esc(l.symbol)} ${l.side === 'B' ? 'bought' : 'sold'} on ${fmtDate(l.date)} under ${esc(l.code)}. It can be changed until the day is closed; the day’s entries are worked out again.`,
      `<div class="form"><div class="fld"><label for="fsc-port">Portfolio</label>${fsPortSel('fsc-port', l.portfolio)}</div><div class="fld"><label for="fsc-kind">Kind</label><select class="in" id="fsc-kind">${(C.firmsec.hold.kinds || []).map(k => `<option value="${esc(k.id)}" ${k.id === l.kind ? 'selected' : ''}>${esc(k.name)}</option>`).join('')}</select></div><div class="fld"><label for="fsc-term">Investment is</label><select class="in" id="fsc-term"><option value="LT" ${l.term !== 'CUR' ? 'selected' : ''}>Long-term</option><option value="CUR" ${l.term === 'CUR' ? 'selected' : ''}>Current</option></select></div></div>`, 'Change',
      () => act(() => post(`/v1/firm-securities/entries/${encodeURIComponent(id)}/change`, { portfolio: $('fsc-port').value, kind: $('fsc-kind').value, term: $('fsc-term').value }), 'Changed: entries worked out again'));
  },
  fsDecide: (id, el) => {
    const a = el.dataset.a;
    if (a === 'approve') return act(() => post(`/v1/firm-securities/entries/${encodeURIComponent(id)}/decide`, { action: 'approve' }), 'Approved and posted').catch(() => {});
    confirmModal(a === 'reject' ? 'Reject the entry' : 'Cancel the entry', a === 'cancel' ? 'Its entry is reversed. Later lines of the scrip must be cancelled first.' : 'The person who made it is told why.', '<div class="fld"><label for="fsd-r">Reason</label><input class="in" id="fsd-r" required></div>', a === 'reject' ? 'Reject' : 'Cancel the entry',
      () => act(() => post(`/v1/firm-securities/entries/${encodeURIComponent(id)}/decide`, { action: a, reason: fsVal('fsd-r') }), a === 'reject' ? 'Rejected' : 'Cancelled'), true);
  },
  fsOpening: () => confirmModal('Post the opening lots', 'Posts the lots loaded from the old system to the securities accounts, against the account the opening balances were carried in.', '<div class="fld"><label for="fso-c">Against the account</label><input class="in mono" id="fso-c" value="EQUITY:OPENING"></div>', 'Post',
    () => act(() => post('/v1/firm-securities/opening/post', { counter: fsVal('fso-c') }), r => `${r.posted} lots posted`)),
  fsJournal: () => {
    const ps = C.firmsec.hold.portfolios.map(p => p.portfolio);
    confirmModal('New stock journal', 'Moves a holding between the portfolios: out at cost (the oldest lots first), in at the day’s closing price. A second person approves it.',
      `<div class="form"><div class="fld"><label for="fsj-date">Date</label><input class="in" type="date" id="fsj-date" value="${S.date}"></div><div class="fld"><label for="fsj-from">From</label>${fsPortSel('fsj-from', ps[0])}</div><div class="fld"><label for="fsj-to">To</label>${fsPortSel('fsj-to', ps[1] || ps[0])}</div>
      <div class="fld"><label for="fsj-isin">ISIN</label><input class="in mono" id="fsj-isin" maxlength="12"></div><div class="fld"><label for="fsj-qty">Quantity</label><input class="in" id="fsj-qty" inputmode="decimal"></div><div class="fld"><label for="fsj-price">Market value per unit (only if there is no closing price)</label><input class="in" id="fsj-price" inputmode="decimal"></div>
      <div class="fld full"><label for="fsj-why">Why it moves</label><input class="in" id="fsj-why" maxlength="300"></div></div>`, 'Save for approval',
      () => act(() => post('/v1/firm-securities/journals', { date: fsVal('fsj-date'), from: $('fsj-from').value, to: $('fsj-to').value, isin: fsVal('fsj-isin'), qty: fsVal('fsj-qty'), price: fsVal('fsj-price'), reason: fsVal('fsj-why') }), j => `${j.id}: in at ${inr(j.in_value, 2)}, out at ${inr(j.out_cost, 2)}; waits for approval`));
  },
  fsSJDecide: (id, el) => {
    const ok = el.dataset.a === '1';
    if (ok) return act(() => post(`/v1/firm-securities/journals/${encodeURIComponent(id)}/decide`, { approve: true }), 'Stock journal posted').catch(() => {});
    confirmModal('Reject the stock journal', '', '<div class="fld"><label for="fsj-n">Reason</label><input class="in" id="fsj-n"></div>', 'Reject', () => act(() => post(`/v1/firm-securities/journals/${encodeURIComponent(id)}/decide`, { approve: false, note: fsVal('fsj-n') }), 'Rejected'), true);
  },
  fsValPropose: () => act(() => post('/v1/firm-securities/valuation', { date: C.firmsec.val.date, portfolio: C.firmsec.val.portfolio }), 'Proposed: a second person posts it').catch(() => {}),
  fsValDecide: (id, el) => {
    const ok = el.dataset.a === '1';
    if (ok) return act(() => post(`/v1/firm-securities/valuation/${encodeURIComponent(id)}/decide`, { approve: true }), 'Valuation entry posted').catch(() => {});
    confirmModal('Reject the valuation entry', '', '<div class="fld"><label for="fsv-n">Reason</label><input class="in" id="fsv-n"></div>', 'Reject', () => act(() => post(`/v1/firm-securities/valuation/${encodeURIComponent(id)}/decide`, { approve: false, note: fsVal('fsv-n') }), 'Rejected'), true);
  },
  fsRep: async () => {
    UI.fsRepKind = $('fs-rk').value; UI.fsRepPort = $('fs-rp')?.value || ''; UI.fsRepFrom = $('fs-rf')?.value || ''; UI.fsRepTo = $('fs-rt')?.value || ''; UI.fsRepHeads = !!$('fs-rh')?.checked;
    $('fs-rep-out').innerHTML = '<p class="loading">Preparing report…</p>';
    try { UI.fsRepRes = await get(fsRepURL()); } catch (e) { UI.fsRepRes = { error: e }; }
    $('fs-rep-out').innerHTML = fsRepOut(UI.fsRepRes);
  },
  fsRepCSV: () => { UI.fsRepKind = $('fs-rk').value; UI.fsRepPort = $('fs-rp')?.value || ''; UI.fsRepFrom = $('fs-rf')?.value || ''; UI.fsRepTo = $('fs-rt')?.value || ''; UI.fsRepHeads = !!$('fs-rh')?.checked; download(fsRepURL('csv'), `firm-securities-${UI.fsRepKind}.csv`); },
  fsRepPrint: () => { UI.fsRepKind = $('fs-rk').value; UI.fsRepPort = $('fs-rp')?.value || ''; UI.fsRepFrom = $('fs-rf')?.value || ''; UI.fsRepTo = $('fs-rt')?.value || ''; UI.fsRepHeads = !!$('fs-rh')?.checked; openDoc(fsRepURL('html')); },
  fsDemat: () => {
    const ds = Object.keys(C.firmsec.rec.sources);
    confirmModal('Enter demat holdings', 'From the DP’s holding statement of the firm’s own account: one line per ISIN as ISIN, quantity, pledged.',
      `<div class="form"><div class="fld"><label for="fsdm-d">Demat account</label><select class="in" id="fsdm-d">${ds.map(x => `<option>${esc(x)}</option>`).join('')}</select></div><div class="fld"><label for="fsdm-dt">As on</label><input class="in" type="date" id="fsdm-dt" value="${esc(C.firmsec.rec.date)}"></div><div class="fld full"><label for="fsdm-l">Holdings</label><textarea class="in mono" id="fsdm-l" rows="6" placeholder="INE000X01011, 100, 0"></textarea></div></div>`, 'Save and check',
      () => {
        const lines = fsVal('fsdm-l').split('\n').map(x => x.split(',').map(y => y.trim())).filter(x => x[0]).map(([isin, qty, pl]) => ({ isin, qty: qty || '0', pledged: pl || '0' }));
        return act(() => post('/v1/firm-securities/demat', { demat: $('fsdm-d').value, date: fsVal('fsdm-dt'), lines, source: 'Entered from the DP statement' }), 'Saved: the register is checked against it');
      });
  },
  fsPledge: () => confirmModal('New pledge', 'Pledges are tracked, not blocked: Ledgerline warns before pledged units are sold.',
    `<div class="form"><div class="fld"><label for="fsp-date">Pledged on</label><input class="in" type="date" id="fsp-date" value="${S.date}"></div><div class="fld"><label for="fsp-port">Portfolio</label>${fsPortSel('fsp-port')}</div><div class="fld"><label for="fsp-isin">ISIN</label><input class="in mono" id="fsp-isin" maxlength="12"></div><div class="fld"><label for="fsp-qty">Quantity</label><input class="in" id="fsp-qty" inputmode="decimal"></div>
    <div class="fld"><label for="fsp-to">Pledgee</label><input class="in" id="fsp-to" placeholder="Clearing corporation, bank or NBFC"></div><div class="fld"><label for="fsp-type">Pledgee type</label><select class="in" id="fsp-type">${['Clearing corporation (margin)', 'Bank', 'NBFC', 'Other'].map(x => `<option>${x}</option>`).join('')}</select></div><div class="fld"><label for="fsp-purpose">Purpose</label><input class="in" id="fsp-purpose"></div><div class="fld"><label for="fsp-ref">Pledge number</label><input class="in mono" id="fsp-ref"></div></div>`, 'Record pledge',
    () => act(() => post('/v1/firm-securities/pledges', { date: fsVal('fsp-date'), portfolio: $('fsp-port').value, isin: fsVal('fsp-isin'), qty: fsVal('fsp-qty'), pledgee: fsVal('fsp-to'), pledgee_type: $('fsp-type').value, purpose: fsVal('fsp-purpose'), ref: fsVal('fsp-ref') }), r => `${r.pledge.id} recorded${r.warnings.length ? ' · ' + r.warnings[0] : ''}`)),
  fsPledgeClose: (id, el) => {
    const inv = el.dataset.a === 'invoke';
    confirmModal(inv ? 'The pledgee invoked the pledge' : 'Release the pledge', inv ? 'The units leave the firm: an off-market sale is entered at the price given, for a second person to approve.' : '',
      `<div class="form"><div class="fld"><label for="fsq-d">${inv ? 'Invoked on' : 'Released on'}</label><input class="in" type="date" id="fsq-d" value="${S.date}"></div>${inv ? '<div class="fld"><label for="fsq-p">Price per unit</label><input class="in" id="fsq-p" inputmode="decimal"></div>' : ''}</div>`, inv ? 'Record invocation' : 'Release',
      () => act(() => post(`/v1/firm-securities/pledges/${encodeURIComponent(id)}/close`, { action: el.dataset.a, date: fsVal('fsq-d'), price: fsVal('fsq-p') }), inv ? 'Invocation recorded; the sale waits for approval' : 'Released'), inv);
  },
  fsIncome: () => confirmModal('Enter a dividend or interest', 'For interest on bonds, or a dividend the corporate actions do not have. Booked with the TDS as receivable.',
    `<div class="form"><div class="fld"><label for="fsi-k">Kind</label><select class="in" id="fsi-k"><option>Dividend</option><option>Interest</option></select></div><div class="fld"><label for="fsi-d">Date (record date)</label><input class="in" type="date" id="fsi-d" value="${S.date}"></div><div class="fld"><label for="fsi-pd">Expected in the bank on</label><input class="in" type="date" id="fsi-pd"></div><div class="fld"><label for="fsi-port">Portfolio</label>${fsPortSel('fsi-port')}</div>
    <div class="fld"><label for="fsi-isin">ISIN</label><input class="in mono" id="fsi-isin" maxlength="12"></div><div class="fld"><label for="fsi-g">Gross (₹)</label><input class="in" id="fsi-g" inputmode="decimal"></div><div class="fld"><label for="fsi-t">TDS (₹)</label><input class="in" id="fsi-t" inputmode="decimal" placeholder="0"></div></div>`, 'Book it',
    () => act(() => post('/v1/firm-securities/income', { kind: $('fsi-k').value, date: fsVal('fsi-d'), pay_date: fsVal('fsi-pd'), portfolio: $('fsi-port').value, isin: fsVal('fsi-isin'), gross: fsVal('fsi-g'), tds: fsVal('fsi-t') }), x => `${x.id} booked: net ${inr(x.net, 2)} receivable`)),
  fsMatch: () => confirmModal('Match bank credits', 'Paste the credits from the firm’s own bank statement, one per line: date (YYYY-MM-DD), amount, reference, narration. Each credit with the net amount of a booked dividend or interest, within 15 days of its expected date, clears the receivable.',
    `<div class="form"><div class="fld"><label for="fsm-b">Bank account</label><input class="in mono" id="fsm-b" value="BANK:OWN"></div><div class="fld full"><label for="fsm-l">Credits</label><textarea class="in mono" id="fsm-l" rows="6" placeholder="2026-10-06, 10800.00, NEFT123, XCO LTD DIVIDEND"></textarea></div></div><div id="fsm-res"></div>`, 'Match',
    () => {
      const credits = fsVal('fsm-l').split('\n').map(x => x.split(',').map(y => y.trim())).filter(x => x[0]).map(([date, amount, ref, ...t]) => ({ date, amount, ref: ref || '', text: t.join(', ') }));
      return act(() => post('/v1/firm-securities/income/match', { bank: fsVal('fsm-b'), credits }), r => `${r.matched.length} matched${r.unmatched.length ? `, ${r.unmatched.length} not matched` : ''}`);
    }),
  fsNWInputs: () => {
    const i = C.firmsec.nw.inputs || {};
    const f = (k, l) => `<div class="fld"><label for="fsw-${k}">${l}</label><input class="in" id="fsw-${k}" inputmode="decimal" value="${esc(i[k] || '0')}"></div>`;
    confirmModal('Figures not in the books', 'Used in the net worth computation. The required net worth is the higher of the base for the firm’s category and the variable net worth.',
      `<div class="form">${f('card', 'Value of the membership card')}${f('bad_deliveries', 'Bad deliveries')}${f('prepaid', 'Prepaid expenses (not in the books)')}${f('intangible', 'Intangible assets')}${f('other_deduction', 'Other deductions')}${f('base_required', 'Base net worth required')}${f('variable', 'Variable net worth (10% of average client funds)')}</div>`, 'Save',
      () => { const b = {}; ['card', 'bad_deliveries', 'prepaid', 'intangible', 'other_deduction', 'base_required', 'variable'].forEach(k => { b[k] = fsVal('fsw-' + k) || '0'; }); return act(() => post('/v1/firm-securities/net-worth', b), 'Saved'); });
  },
  fsKind: id => confirmModal('How this instrument type is treated', 'Sets the ICDS VIII category its closing stock is compared in, and its capital gains class.',
    `<div class="form"><div class="fld"><label for="fsk-c">ICDS VIII category</label><select class="in" id="fsk-c"><option value="shares">Shares</option><option value="debt">Debt securities</option><option value="convertible">Convertible securities</option><option value="other" selected>Any other securities</option></select></div>
    <div class="fld"><label for="fsk-t">Capital gains</label><select class="in" id="fsk-t"><option value="equity">Listed equity (STT paid): 12 months</option><option value="listed">Other listed: 12 months</option><option value="sgb">Sovereign gold bonds</option><option value="unlisted" selected>Unlisted and others: 24 months</option><option value="mf-other">Other fund units: 24 months</option></select></div>
    <label class="check"><input type="checkbox" id="fsk-l"> Listed and regularly quoted</label></div>`, 'Save',
    () => act(() => post('/v1/firm-securities/kinds', { id, category: $('fsk-c').value, tax: $('fsk-t').value, listed: $('fsk-l').checked }), 'Saved')),
});
