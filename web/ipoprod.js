'use strict';
/* Ledgerline console: NSE eIPO's other products on the IPO & public issues
   screen: non-competitive bids in G-secs, T-bills and SDLs (NCB), sovereign
   gold bonds (SGB) and tender offers (buy-back, open offer, delisting); and
   an open issue's demand from NSE's eIPO query server. Loaded after ipo.js
   and uses its helpers. Staff only: the client portal does not offer these. */

const PROD_ST = { Draft: 'nt', Placed: 'acc', Refused: 'bad', Cancelled: 'nt', Rejected: 'bad', Allotted: 'good', Accepted: 'good', 'Partly accepted': 'warn', 'Not accepted': 'nt' };
const prodPill = s => pill(s || '—', PROD_ST[s] || 'nt');
const prodUnit = t => ({ NCB: '₹ of face value', SGB: 'grams', Tender: 'shares' })[t] || 'quantity';
const prodQty = x => x.product === 'NCB' ? inr(x.qty) + ' FV' : `${nf(x.qty)} ${x.product === 'SGB' ? 'g' : 'sh'}`;
const prodClearing = { FP: 'funds pending', FS: 'funds sent to NSCCL', RJ: 'rejected', AL: 'allotted', FR: 'funds received', RS: 'sent to RBI', RA: 'allotted by RBI' };
const prodIssues = () => (C.ipo?.prod?.issues || []);
const prodIssueOf = id => prodIssues().find(i => i.id === id) || ipoIssueOf(id);

P.ipo_prod = d => {
  const v = d.prod, s = v.settings || {}, f = UI.prodSt || '', fi = UI.prodIss || '';
  const rows = v.orders.filter(x => (!f || x.status === f) && (!fi || x.issue_id === fi));
  const chk = (k, l) => `<label class="check"><input type="checkbox" class="pr-on" data-k="${k}" ${s[k] ? 'checked' : ''} ${has('Ops checker') ? '' : 'disabled'}> ${l}</label>`;
  const px = i => i.type === 'NCB' ? `₹${esc(i.price_low)} FV` : i.type === 'SGB' ? (num(i.price_low) ? `₹${esc(i.price_low)} online · ₹${esc(i.price_high)} offline` : '<span class="dim">not announced</span>') : `₹${esc(i.price_low)}${num(i.price_high) && i.price_high !== i.price_low ? '–' + esc(i.price_high) : ''}`;
  const lim = i => { const p = i.product || {}; return `${nf(i.lot_size)}${p.min_qty ? ' · min ' + nf(p.min_qty) : ''}${p.max_qty ? ' · max ' + nf(p.max_qty) : ''}`; };
  const flags = x => `${x.mismatch ? '<div>' + pill('Mismatch', 'bad') + ` <span class="small">${esc(x.mismatch)}</span></div>` : ''}${(x.incomplete || []).length ? '<div>' + pill('Details missing', 'warn') + '</div>' : ''}${x.verification === 'Failed' ? '<div>' + pill('Verification failed', 'bad') + '</div>' : ''}`;
  return `<div class="panel"><div class="hd"><h3>Products the firm offers</h3>${v.usable ? pill(v.send ? 'eIPO API sending' : 'eIPO API ready, sending off', v.send ? 'good' : 'warn') : pill('eIPO API not set up', 'nt')}</div><div class="bd">
      <div class="tools">${chk('ncb', 'G-secs, T-bills, SDLs (NCB)')}${chk('sgb', 'Sovereign gold bonds')}${chk('tender', 'Tender offers')}${has('Ops checker') ? '<button class="btn sm" data-act="prodSettings">Save</button>' : ''}${v.usable && ipoMk() ? '<button class="btn sm" data-act="prodMaster">Read NSE’s masters</button>' : ''}</div>
      <p class="dim small">Issues of the products ticked are set up from NSE’s NCB, SGB and tender masters each morning; their orders go to NSE every 5 minutes while bidding is open (with sending on in Settings → Connections → NSE eIPO API) and their status is asked every 15 minutes. NCB and SGB money is taken from the client’s ledger when the order is taken and held until it is paid in to NSCCL. Last master read: ${v.state.last_master ? ipoTime(v.state.last_master) + ' · ' + (v.state.master_new || 0) + ' issue(s) set up' : '—'}.</p>
      ${v.state.last_error ? `<div class="note bad">${esc(v.state.last_error)}</div>` : ''}</div></div>
    <div class="panel"><div class="hd"><h3>Issues and offers</h3>${ipoMk() ? '<div class="tools"><button class="btn sm" data-act="prodIssueEdit">New</button></div>' : ''}</div>` +
    tbl([['Issue'], ['Type'], ['Bidding'], ['Price', 'r'], ['Lot · limits', 'r'], ['Allotment'], ['Status'], ['']], v.issues.map(i => `<tr>
      <td><b>${esc(i.name)}</b><div class="dim small">${esc(i.symbol)}${i.product?.series ? ' · ' + esc(i.product.series) : ''}${i.isin ? ' · ' + esc(i.isin) : ''}${i.registrar ? ' · ' + esc(i.registrar) : ''}</div></td>
      <td>${esc(i.type)}${i.product?.kind ? `<div class="dim small">${esc(i.product.kind)}</div>` : ''}</td>
      <td class="n small">${fmtDate(i.open)} – ${fmtDate(i.close)}<div class="dim">${esc(i.open_time)}–${esc(i.day_close)}, ${esc(i.close_time)} on the last day${i.product?.t1_from ? `<br>T1 ${esc(i.product.t1_from)} – ${esc(i.product.t1_to)}` : ''}</div></td>
      <td class="r small">${px(i)}</td><td class="r small">${lim(i)}</td><td class="n small">${fmtDate(i.timeline.allotment) || '—'}</td><td>${pill(i.status, ISS_ST[i.status] || 'nt')}</td>
      <td><div class="tools">${ipoMk() ? `<button class="btn sm" data-act="prodIssueEdit" data-id="${esc(i.id)}">Edit</button>` : ''}${ipoMk() && v.usable ? `<button class="btn sm" data-act="prodSend" data-id="${esc(i.id)}">Send by API</button><button class="btn sm" data-act="prodInq" data-id="${esc(i.id)}">Ask status</button>` : ''}${i.status === 'Open' ? `<button class="btn sm" data-act="ipoDemand" data-id="${esc(i.id)}">Demand</button>` : ''}${i.type === 'Tender' && i.status !== 'Open' && i.status !== 'Upcoming' && has('Ops checker') ? `<button class="btn sm pri" data-act="prodAccept" data-id="${esc(i.id)}">Record acceptance</button>` : ''}</div></td></tr>`), { empty: 'None yet. Tick the products the firm offers and read NSE’s masters, or set one up.' }) + '</div>' +
    `<div class="panel"><div class="hd"><h3>Orders</h3><div class="tools"><select class="in sm" id="pr-iss" aria-label="Issue"><option value="">All issues</option>${v.issues.map(i => `<option value="${esc(i.id)}" ${fi === i.id ? 'selected' : ''}>${esc(i.name)}</option>`).join('')}</select><select class="in sm" id="pr-st" aria-label="Status"><option value="">Every status</option>${v.statuses.map(x => `<option ${f === x ? 'selected' : ''}>${x}${v.counts[x] ? ' (' + v.counts[x] + ')' : ''}</option>`).join('')}</select>${ipoCanBid() && v.issues.some(i => ['Open', 'Upcoming'].includes(i.status)) ? '<button class="btn sm pri" data-act="prodNew">New order</button>' : ''}</div></div>` +
    tbl([['Order'], ['Client'], ['Issue'], ['Quantity', 'r'], ['Price', 'r'], ['Amount', 'r'], ['Status'], ['NSE'], ['']], rows.map(x => `<tr>
      <td class="n">${esc(x.id)}${x.app_no ? `<div class="dim small">${esc(x.app_no)}</div>` : ''}</td>
      <td><button class="linkish mono" data-act="client" data-id="${esc(x.ucc)}">${esc(x.ucc)}</button> ${esc(x.name || '')}</td>
      <td>${esc(x.issue)}<div class="dim small">${x.physical ? (x.product === 'SGB' ? 'certificate' : 'physical') : 'demat'}</div></td>
      <td class="r">${prodQty(x)}</td><td class="r">${num(x.price) ? '₹' + esc(x.price) : '—'}</td>
      <td class="r">${inr(x.amount)}${x.funds_entry ? '<div class="dim small">held</div>' : x.released_on ? '<div class="dim small">released</div>' : ''}</td>
      <td>${prodPill(x.status)}${x.pending ? `<div class="dim small">${esc(x.pending)} to send</div>` : ''}${flags(x)}</td>
      <td class="small">${x.order_no ? esc(x.order_no) : '—'}${x.clearing ? `<div class="dim">${esc(prodClearing[x.clearing] || x.clearing)}</div>` : ''}${x.reason ? `<div class="neg wrap">${esc(x.reason)}</div>` : ''}</td>
      <td><button class="btn sm" data-act="prodOpen" data-id="${esc(x.id)}">Open</button></td></tr>`), { tall: true, empty: 'No orders.' }) + '</div>';
};

document.addEventListener('change', e => {
  if (S.view !== 'ipo') return;
  if (e.target.id === 'pr-st') { UI.prodSt = e.target.value; $('page').innerHTML = groupBar(S.view) + V.ipo.html(C.ipo); }
  if (e.target.id === 'pr-iss') { UI.prodIss = e.target.value; $('page').innerHTML = groupBar(S.view) + V.ipo.html(C.ipo); }
});

function prodCertRows(cs) {
  return [0, 1, 2].map(n => { const c = (cs || [])[n] || {}; return `<tr><td>${n + 1}</td><td><input class="in sm mono pc-no" maxlength="16" value="${esc(c.no || '')}"></td><td><input class="in sm mono pc-from" maxlength="16" value="${esc(c.from || '')}"></td><td><input class="in sm mono pc-to" maxlength="16" value="${esc(c.to || '')}"></td></tr>`; }).join('');
}
const prodCerts = () => [...document.querySelectorAll('#po-certs tbody tr')].map(tr => ({ no: tr.querySelector('.pc-no').value.trim(), from: tr.querySelector('.pc-from').value.trim(), to: tr.querySelector('.pc-to').value.trim() })).filter(c => c.no || c.from || c.to);

Object.assign(A, {
  prodSettings: () => { const b = {}; document.querySelectorAll('.pr-on').forEach(x => { b[x.dataset.k] = x.checked; }); act(() => api('PUT', '/v1/ipo/products/settings', b), 'Saved'); },
  prodMaster: () => act(() => post('/v1/ipo/products/master'), r => `${r.added} set up and ${r.changed} updated from NSE’s masters`),
  prodSend: id => act(() => post(`/v1/ipo/issues/${ipoEnc(id)}/products-send`), r => `${r.sent} order(s) sent: ${r.placed} taken, ${r.refused} refused${(r.problems || []).length ? ' · ' + r.problems[0] : ''}`),
  prodInq: id => act(() => post(`/v1/ipo/issues/${ipoEnc(id)}/products-inquire`), r => `${r.updated} order(s) updated${r.mismatches ? ', ' + r.mismatches + ' mismatch(es)' : ''}${r.incomplete ? ', ' + r.incomplete + ' incomplete' : ''}${(r.problems || []).length ? ' · ' + r.problems[0] : ''}`),
  prodNew: () => {
    const open = prodIssues().filter(i => ['Open', 'Upcoming'].includes(i.status));
    modal(mhd('New order', 'On the client’s prior instruction. NCB and SGB money is taken from the client’s ledger now; a tender is checked against the shares free in the client’s demat account.') + `<form class="bd" id="po-f"><div class="form">
      <div class="fld"><label for="po-iss">Issue or offer</label><select class="in" id="po-iss">${open.map(i => `<option value="${esc(i.id)}">${esc(i.name)} · ${esc(i.type)} · ${esc(i.status)}</option>`).join('')}</select></div>
      <div class="fld"><label for="po-ucc">Client (UCC)</label><input class="in mono" id="po-ucc" required autocomplete="off"></div>
      <div class="fld"><label for="po-qty" id="po-qtyl">Quantity</label><input class="in mono" id="po-qty" inputmode="numeric" required><span class="dim small" id="po-qtyn"></span></div>
      <div class="fld" id="po-cat-f"><label for="po-cat">Category</label><select class="in" id="po-cat"></select></div>
      <div class="fld" id="po-px-f"><label for="po-px">Price (₹, empty: the offer price)</label><input class="in mono" id="po-px" inputmode="decimal"></div>
      <div class="fld" id="po-on-f"><label class="check"><input type="checkbox" id="po-on"> At the online price (the client applied online and pays digitally)</label></div>
      <div class="fld" id="po-ph-f"><label class="check"><input type="checkbox" id="po-ph"> <span id="po-pht">Physical</span></label></div>
      <div class="fld" id="po-dm-f"><label for="po-dm">Demat account (BO ID; empty: the client’s usual one)</label><input class="in mono" id="po-dm"></div>
      <div class="full" id="po-phys" hidden><div class="fld"><label for="po-folio">Folio number</label><input class="in mono" id="po-folio" maxlength="16"></div><table class="tbl" id="po-certs"><thead><tr><th>#</th><th>Certificate</th><th>Distinctive from</th><th>to</th></tr></thead><tbody>${prodCertRows()}</tbody></table></div>
      <div class="full"><b>The client’s instruction</b></div>${ipoInstrFields('po')}
    </div><div id="po-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Take the order</button></div></form>`);
    $('po-ch').innerHTML = (C.ipo?.prod?.channels || []).map(c => `<option>${esc(c)}</option>`).join('');
    const fill = () => {
      const i = prodIssueOf($('po-iss').value); if (!i) return; const p = i.product || {};
      $('po-qtyl').textContent = 'Quantity (' + prodUnit(i.type) + ')';
      $('po-qtyn').textContent = `Lots of ${nf(i.lot_size)}${p.min_qty ? ', at least ' + nf(p.min_qty) : ''}${p.max_qty ? ', at most ' + nf(p.max_qty) : ''}`;
      $('po-cat-f').hidden = i.type !== 'Tender'; $('po-px-f').hidden = i.type !== 'Tender'; $('po-on-f').hidden = i.type !== 'SGB';
      $('po-ph-f').hidden = i.type === 'NCB'; $('po-pht').textContent = i.type === 'SGB' ? 'As a certificate (no demat account)' : 'Physical shares (folio and certificates)';
      $('po-cat').innerHTML = (i.categories || []).map(c => `<option value="${esc(c.code)}">${esc(c.code)} · ${esc(c.name)}</option>`).join('');
      const ph = () => { $('po-phys').hidden = !($('po-ph').checked && i.type === 'Tender'); $('po-dm-f').hidden = $('po-ph').checked; };
      $('po-ph').onchange = ph; ph();
    };
    $('po-iss').onchange = fill; fill();
    $('po-f').onsubmit = async e => {
      e.preventDefault();
      try {
        const x = await post('/v1/ipo/products/orders', { issue: $('po-iss').value, ucc: $('po-ucc').value.trim().toUpperCase(), qty: parseInt($('po-qty').value, 10) || 0, category: $('po-cat').value || '', price: $('po-px').value.trim() || '0',
          online: $('po-on').checked, physical: $('po-ph').checked, demat: $('po-dm').value.trim(), folio: $('po-folio').value.trim(), certificates: prodCerts(), instruction: ipoInstr('po') });
        closeModal(); toast(`${x.id} taken (acknowledgement ${x.ack})`); render({ keep: true });
      } catch (err) { $('po-err').innerHTML = errHTML(err); }
    };
  },
  prodOpen: async id => {
    let d; try { d = await get(`/v1/ipo/products/orders/${ipoEnc(id)}`); } catch (e) { toast(e.message, true); return; }
    const x = d.order, live = ['Draft', 'Placed', 'Refused'].includes(x.status) && x.pending !== 'Cancel';
    modal(mhd(`${esc(x.id)} · ${esc(x.issue)}`, `${esc(x.ucc)} ${esc(x.name || '')} · ${esc(x.product)} · ${x.physical ? (x.product === 'SGB' ? 'certificate' : 'physical shares') : 'demat ' + esc(x.depository) + ' ' + esc(x.dp_id || '') + esc(x.ben_id || '')}`) + `<div class="bd">
      <div class="kv"><div><small>Status</small><b>${prodPill(x.status)}</b></div><div><small>Quantity</small><b>${prodQty(x)}</b></div><div><small>Price</small><b>${num(x.price) ? '₹' + esc(x.price) + (x.price_kind ? ' (' + esc(x.price_kind.toLowerCase()) + ')' : '') : '—'}</b></div><div><small>Amount</small><b>${inr(x.amount, 2)}</b></div>
      <div><small>Money held</small><b>${x.funds_entry ? inr(x.funds, 2) : x.released_on ? 'released ' + fmtDate(x.released_on) : '—'}</b></div><div><small>Application · order</small><b class="mono small">${esc(x.app_no || '—')} · ${esc(x.order_no || '—')}</b></div>
      <div><small>NSE</small><b class="small">${esc(x.order_status || '—')}${x.clearing ? ' · ' + esc(prodClearing[x.clearing] || x.clearing) : ''}${x.verification ? ' · depository ' + esc(x.verification.toLowerCase()) : ''}</b></div>${num(x.exch_amount) ? `<div><small>NSE amount</small><b>${inr(x.exch_amount, 2)}</b></div>` : ''}
      ${x.reason ? `<div><small>NSE said</small><b class="small">${esc(x.reason)}</b></div>` : ''}${x.mismatch ? `<div><small>Mismatch</small><b class="small neg">${esc(x.mismatch)}</b></div>` : ''}${x.rta_status ? `<div><small>Registrar · custodian</small><b class="small">${esc(x.rta_status)}${x.rta_reason ? ' (' + esc(x.rta_reason) + ')' : ''} · ${esc(x.custodian_status || '—')}</b></div>` : ''}
      ${x.accepted_on ? `<div><small>Accepted</small><b>${nf(x.accepted_qty)} of ${nf(x.qty)} at ₹${esc(x.accepted_price)} · ${inr(x.proceeds, 2)}</b></div>` : ''}${x.folio ? `<div><small>Folio · certificates</small><b class="small">${esc(x.folio)} · ${(x.certificates || []).map(c => esc(c.no)).join(', ')}</b></div>` : ''}</div>
      <h4 style="margin:14px 0 6px">Instructions</h4>${tbl([['When'], ['Action'], ['How'], ['Ack']], d.instructions.map(y => `<tr><td class="small">${ipoTime(y.received)}</td><td>${esc(y.action)}</td><td class="small">${esc(y.channel)} · ${esc(y.ref)}</td><td class="n">${esc(y.ack)}</td></tr>`))}
      <h4 style="margin:14px 0 6px">History</h4><div class="small">${(x.history || []).slice().reverse().map(h => `<div><span class="dim">${ipoTime(h.at)} · ${esc(h.by)}</span> ${esc(h.what)}</div>`).join('') || '—'}</div>
      <div id="pd-form"></div><div id="pd-err"></div></div>
      <div class="ft"><button class="btn" data-act="close">Close</button>${live && ipoCanBid() ? `<button class="btn" data-act="prodModify" data-id="${esc(x.id)}">Modify</button><button class="btn bad" data-act="prodCancel" data-id="${esc(x.id)}">Cancel order</button>` : ''}${d.kyc && ipoMk() ? `<button class="btn pri" data-act="prodKYC" data-id="${esc(x.id)}">Send missing details</button>` : ''}</div>`);
    C.prodOrder = d;
  },
  prodModify: id => {
    const x = C.prodOrder.order;
    $('pd-form').innerHTML = `<h4 style="margin:14px 0 6px">Modify</h4><form id="pd-mf"><div class="form"><div class="fld"><label for="pm-q">Quantity (${prodUnit(x.product)})</label><input class="in mono" id="pm-q" value="${esc(x.qty)}"></div>${x.product === 'Tender' ? `<div class="fld"><label for="pm-p">Price (₹, empty: the offer price)</label><input class="in mono" id="pm-p" value="${esc(x.price || '')}"></div>` : ''}${x.product === 'SGB' ? `<div class="fld"><label class="check"><input type="checkbox" id="pm-on" ${x.price_kind === 'Online' ? 'checked' : ''}> At the online price</label></div>` : ''}${ipoInstrFields('pm')}</div><p class="dim small">The client’s demat details are read again from the client master: correct them there first to fix a mismatch.</p><div class="tools" style="margin-top:8px"><button class="btn pri" type="submit">Save the change</button></div></form>`;
    $('pm-ch').innerHTML = (C.ipo?.prod?.channels || []).map(c => `<option>${esc(c)}</option>`).join('');
    $('pd-mf').onsubmit = async e => { e.preventDefault(); try { const r = await post(`/v1/ipo/products/orders/${ipoEnc(id)}/modify`, { qty: parseInt($('pm-q').value, 10) || 0, price: $('pm-p')?.value.trim() || '0', online: $('pm-on')?.checked || false, instruction: ipoInstr('pm') }); closeModal(); toast(`${r.id} changed (acknowledgement ${r.ack})`); render({ keep: true }); } catch (err) { $('pd-err').innerHTML = errHTML(err); } };
  },
  prodCancel: id => {
    $('pd-form').innerHTML = `<h4 style="margin:14px 0 6px">Cancel the order</h4><form id="pd-cf"><div class="form">${ipoInstrFields('pc')}</div><div class="tools" style="margin-top:8px"><button class="btn bad" type="submit">Cancel on the client’s instruction</button></div></form>`;
    $('pc-ch').innerHTML = (C.ipo?.prod?.channels || []).map(c => `<option>${esc(c)}</option>`).join('');
    $('pd-cf').onsubmit = async e => { e.preventDefault(); try { const r = await post(`/v1/ipo/products/orders/${ipoEnc(id)}/cancel`, ipoInstr('pc')); closeModal(); toast(r.status === 'Cancelled' ? `${r.id} cancelled` : `${r.id}: cancellation goes to NSE with the next send`); render({ keep: true }); } catch (err) { $('pd-err').innerHTML = errHTML(err); } };
  },
  prodKYC: id => {
    const k = C.prodOrder.kyc || {}, f = (key, label, v, type) => `<div class="fld"><label for="pk-${key}">${label}</label><input class="in ${type === 'date' ? '' : 'mono'}" ${type ? 'type="' + type + '"' : ''} id="pk-${key}" value="${esc(v || '')}"></div>`;
    $('pd-form').innerHTML = `<h4 style="margin:14px 0 6px">Investor details NSE lists as missing</h4><p class="small">${esc((k.codes || []).join(', '))}: ${esc(C.prodOrder.order.history.slice().reverse().find(h => h.what.startsWith('NSE lists the application as incomplete'))?.what || '')}</p><form id="pd-kf"><div class="form">
      ${f('dob', 'First holder’s date of birth', k.dob, 'date')}${f('state', 'First holder’s state', k.state)}${f('ifsc', 'Bank IFSC', k.bank_ifsc)}${f('acc', 'Bank account number', k.bank_account)}
      ${f('p2', 'Second holder’s PAN', '')}${f('d2', 'Second holder’s date of birth', '', 'date')}${f('p3', 'Third holder’s PAN', '')}${f('d3', 'Third holder’s date of birth', '', 'date')}
      ${f('gn', 'Guardian’s first name', '')}${f('gr', 'Guardian’s relation', '')}${f('gd', 'Guardian’s date of birth', '', 'date')}${f('gp', 'Guardian’s PAN', '')}</div>
      <p class="dim small">Filled in from the client master where it has the detail; check each before sending.</p><div class="tools" style="margin-top:8px"><button class="btn pri" type="submit">Send to NSE</button></div></form>`;
    $('pd-kf').onsubmit = async e => { e.preventDefault(); const v = s => $('pk-' + s).value.trim();
      try { await post(`/v1/ipo/products/orders/${ipoEnc(id)}/sgb-details`, { dob: v('dob'), state: v('state'), bank_ifsc: v('ifsc'), bank_account: v('acc'), second_pan: v('p2'), second_dob: v('d2'), third_pan: v('p3'), third_dob: v('d3'), guardian_name: v('gn'), guardian_relation: v('gr'), guardian_dob: v('gd'), guardian_pan: v('gp') }); closeModal(); toast('Details sent to NSE'); render({ keep: true }); } catch (err) { $('pd-err').innerHTML = errHTML(err); } };
  },
  prodAccept: id => {
    const i = prodIssueOf(id), rows = (C.ipo.prod.orders || []).filter(x => x.issue_id === id && x.status === 'Placed');
    if (!rows.length) { toast('No placed tenders in this offer'); return; }
    modal(mhd('Record the acceptance of ' + esc(i.name), 'From the registrar’s basis of acceptance. Tenders left at 0 are not accepted. Accepted demat shares leave the client’s holding and the proceeds are credited to the client’s ledger against the clearing corporation.') + `<form class="bd" id="pa-f"><div class="form"><div class="fld"><label for="pa-d">Acceptance date</label><input class="in" type="date" id="pa-d" value="${esc(S.date)}"></div><div class="fld"><label for="pa-p">Price (₹, empty: each tender’s; a delisting’s exit price)</label><input class="in mono" id="pa-p"></div></div>` +
      tbl([['Order'], ['Client'], ['Tendered', 'r'], ['Accepted shares'], ['Proceeds (₹, empty: shares × price)']], rows.map(x => `<tr><td class="n">${esc(x.id)}</td><td>${esc(x.ucc)} ${esc(x.name || '')}${x.physical ? ' <span class="dim small">physical</span>' : ''}</td><td class="r">${nf(x.qty)}</td><td><input class="in sm mono pa-q" data-id="${esc(x.id)}" inputmode="numeric" value="0" style="width:90px"></td><td><input class="in sm mono pa-a" data-id="${esc(x.id)}" style="width:110px"></td></tr>`)) +
      `<p class="dim small">A buy-back’s proceeds can be entered net of the tax the company deducted, as the clearing corporation pays them.</p><div id="pa-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Record and post</button></div></form>`);
    $('pa-f').onsubmit = async e => { e.preventDefault(); const r = [...document.querySelectorAll('.pa-q')].map(q => ({ order: q.dataset.id, qty: parseInt(q.value, 10) || 0, proceeds: document.querySelector(`.pa-a[data-id="${q.dataset.id}"]`).value.trim() || '0' }));
      try { const res = await post(`/v1/ipo/issues/${ipoEnc(id)}/tender-acceptance`, { date: $('pa-d').value, price: $('pa-p').value.trim() || '0', rows: r }); closeModal(); toast(`${res.accepted} accepted, ${res.partly} partly, ${res.not_accepted} not accepted · ₹${nf(res.proceeds, 2)} credited`); render({ keep: true }); } catch (err) { $('pa-err').innerHTML = errHTML(err); } };
  },
  prodIssueEdit: id => {
    const i = (id && prodIssueOf(id)) || { type: 'NCB', exchanges: ['NSE'], categories: [], product: {} }, p = i.product || {}, kinds = C.ipo?.prod?.kinds || [];
    const fld = (k, l, v, x) => `<div class="fld"><label for="pi-${k}">${l}</label><input class="in ${x || ''}" id="pi-${k}" value="${esc(v ?? '')}"></div>`;
    modal(mhd(id ? 'Edit ' + esc(i.name) : 'New NCB, SGB or tender offer', 'Usually set up from NSE’s masters. Amounts in rupees; NCB quantities in rupees of face value, SGB in grams.') + `<form class="bd" id="pi-f"><div class="form">
      ${fld('sym', 'Symbol (as on eIPO)', i.symbol, 'mono')}${fld('name', 'Name', i.name)}
      <div class="fld"><label for="pi-type">Type</label><select class="in" id="pi-type" ${id ? 'disabled' : ''}>${ipoProdTypes.map(t => `<option ${i.type === t ? 'selected' : ''}>${t}</option>`).join('')}</select></div>
      <div class="fld"><label for="pi-kind">Kind (tender offers)</label><select class="in" id="pi-kind"><option value="">—</option>${kinds.map(k => `<option ${p.kind === k ? 'selected' : ''}>${k}</option>`).join('')}</select></div>
      ${fld('ser', 'Series', p.series, 'mono')}${fld('isin', 'ISIN', i.isin, 'mono')}
      <div class="fld"><label for="pi-open">Opens</label><input class="in" type="date" id="pi-open" value="${esc(i.open || '')}"></div><div class="fld"><label for="pi-close">Closes</label><input class="in" type="date" id="pi-close" value="${esc(i.close || '')}"></div>
      <div class="fld"><label>Bidding hours · on the last day until</label><div class="tools"><input class="in sm mono" id="pi-ot" value="${esc(i.open_time || '')}" placeholder="10:00" style="width:64px">–<input class="in sm mono" id="pi-dc" value="${esc(i.day_close || '')}" placeholder="17:00" style="width:64px"><input class="in sm mono" id="pi-ct" value="${esc(i.close_time || '')}" placeholder="16:00" style="width:64px"></div></div>
      ${fld('lo', 'Price from (NCB: face value; SGB: online price; tender: floor)', num(i.price_low) ? i.price_low : '', 'mono')}${fld('hi', 'Price to (SGB: offline price; tender: offer or cap price)', num(i.price_high) ? i.price_high : '', 'mono')}
      ${fld('tick', 'Tick size', num(i.tick) ? i.tick : '', 'mono')}${fld('lot', 'Lot size', i.lot_size || '', 'mono')}${fld('min', 'Minimum quantity', p.min_qty || '', 'mono')}${fld('max', 'Maximum quantity', p.max_qty || '', 'mono')}
      ${fld('buf', 'NCB: % above face value held on the ledger', num(p.funds_buffer_pct) ? p.funds_buffer_pct : '', 'mono')}${fld('rec', 'Record date (tender, yyyy-mm-dd)', p.record_date, 'mono')}${fld('al', 'Allotment date (yyyy-mm-dd)', p.allot_date || i.allotment_date, 'mono')}
      ${fld('reg', 'Registrar', i.registrar)}${fld('cats', 'Tender categories (codes, comma separated)', (i.categories || []).map(c => c.code).join(', '), 'mono')}
      <div class="fld"><label class="check"><input type="checkbox" id="pi-wd" ${i.withdrawn ? 'checked' : ''}> Withdrawn</label></div>
    </div><div id="pi-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
    $('pi-f').onsubmit = async e => {
      e.preventDefault(); const v = k => $('pi-' + k).value.trim(), n = k => parseInt(v(k), 10) || 0, type = $('pi-type').value;
      const old = Object.fromEntries((i.categories || []).map(c => [c.code, c]));
      const cats = type === 'Tender' ? v('cats').split(',').map(s => s.trim().toUpperCase()).filter(Boolean).map(c => old[c] || { code: c, name: c, modify: 'any' }) : [];
      const body = { ...i, id: id || '', symbol: v('sym'), name: v('name'), type, exchanges: ['NSE'], isin: v('isin'), open: v('open'), close: v('close'), open_time: v('ot'), day_close: v('dc'), close_time: v('ct'),
        price_low: v('lo') || '0', price_high: v('hi') || '0', tick: v('tick') || '0', lot_size: n('lot'), registrar: v('reg'), withdrawn: $('pi-wd').checked, categories: cats,
        product: { ...p, kind: $('pi-kind').value, series: v('ser'), min_qty: n('min'), max_qty: n('max'), funds_buffer_pct: v('buf') || '0', record_date: v('rec'), allot_date: v('al') } };
      delete body.status; delete body.timeline;
      try { const r = await post('/v1/ipo/issues', body); closeModal(); toast(`${r.name} saved`); render({ keep: true }); } catch (err) { $('pi-err').innerHTML = errHTML(err); }
    };
  },
  // The last reading (read now if there is none); Read again asks for a
  // fresh one, which the server takes at most every 3 minutes.
  ipoDemand: async (id, el, ev, refresh) => {
    let d; try { d = await get(`/v1/ipo/issues/${ipoEnc(id)}/demand${refresh ? '?refresh=1' : ''}`); } catch (e) { toast(e.message, true); return; }
    const i = prodIssueOf(id) || {};
    modal(mhd('Demand on NSE · ' + esc(i.name || d.symbol), `From NSE’s eIPO query server, read ${d.at ? ipoTime(d.at) : 'never'}. Read again at most every 3 minutes.`) + `<div class="bd">
      ${d.error ? `<div class="note warn">${esc(d.error)}</div>` : ''}<div class="kv"><div><small>Quantity bid</small><b>${nf(d.total_qty)}</b></div><div><small>Bids</small><b>${nf(d.total_bids)}</b></div>${d.offered ? `<div><small>Issue size</small><b>${nf(d.offered)}</b></div>` : ''}</div>
      <h4 style="margin:14px 0 6px">By category</h4>${tbl([['Category'], ['Sub-category'], ['Quantity', 'r'], ['Bids', 'r']], d.by_category.map(c => `<tr><td>${esc(c.category)}</td><td>${esc(c.sub_category || '')}</td><td class="r">${nf(c.qty)}</td><td class="r">${nf(c.bids)}</td></tr>`), { empty: 'No category figures.' })}
      <h4 style="margin:14px 0 6px">By price</h4>${tbl([['Price'], ['Quantity', 'r'], ['Cumulative', 'r'], ['Bids', 'r'], ['Cumulative bids', 'r']], d.by_price.map(p => `<tr><td>${p.cut_off ? 'Cut-off' : '₹' + esc(p.price)}${p.series ? ' · ' + esc(p.series) : ''}</td><td class="r">${nf(p.qty)}</td><td class="r">${p.cum_qty ? nf(p.cum_qty) : '—'}</td><td class="r">${nf(p.bids)}</td><td class="r">${p.cum_bids ? nf(p.cum_bids) : '—'}</td></tr>`), { empty: 'No price points.' })}</div>
      <div class="ft"><button class="btn" data-act="close">Close</button><button class="btn pri" data-act="ipoDemandRefresh" data-id="${esc(id)}">Read again</button></div>`);
  },
  ipoDemandRefresh: id => A.ipoDemand(id, null, null, true),
});
