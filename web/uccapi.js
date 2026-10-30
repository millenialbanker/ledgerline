'use strict';
/* Ledgerline console: Exchange UCC by API. Staff screen "uccapi": one tab
   per exchange (NSE's UCI API, BSE's UCC REST API; MCX takes the UCC file
   only). Each tab lists the clients with their registration at that
   exchange, sends the new and changed ones, checks PAN status and previews
   a client's record before it goes. Loaded after app.js; opened from the
   Clients & onboarding group. */

Object.assign(UI, { uccEx: 'nse', uccSt: 'open', uccQ: '', uccSel: new Set() });
const UCC_EX = { nse: 'NSE', bse: 'BSE', mcx: 'MCX' };
const uccPill = s => pill(s, { Accepted: 'good', Sent: 'acc', 'Not sent': 'warn', Rejected: 'bad', 'Needs fixing': 'bad', Approved: 'good', Pending: 'warn', 'Incorrect PAN': 'bad' }[s] || 'nt');
const uccStamp = t => { if (!t) return '—'; const d = new Date(t); return isNaN(d) ? esc(t) : d.toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'short' }); };
const uccCan = () => has('Ops maker', 'Ops checker');
const uccPanCan = () => has('Ops maker', 'Ops checker', 'Compliance officer');
const uccPath = () => `/v1/ucc-api/${UI.uccEx}`;

V.uccapi = {
  load: async () => {
    const ex = UI.uccEx in UCC_EX ? UI.uccEx : 'nse';
    if (ex === 'mcx') return { ex };
    try { return { ex, v: await get(`/v1/ucc-api/${ex}`) }; }
    catch (e) { if (e.status === 404) return { ex, missing: true }; throw e; }
  },
  html: d => {
    const tabs = `<div class="panel"><div class="hd"><div class="seg">${['nse', 'bse'].map(x => `<button class="${d.ex === x ? 'on' : ''}" data-act="uccEx" data-id="${x}">${UCC_EX[x]}</button>`).join('')}<button disabled title="MCX takes the UCC file only">MCX · file upload only</button></div></div></div>`;
    const head = ph('Exchange UCC', 'Clients registered with each exchange through its UCC API: new clients from the UCC upload stage, approved changes, segment status changes and reactivations. Each record is checked against the exchange’s rules before it goes; a record that has not changed is never sent again.');
    if (d.ex === 'mcx') return head + tabs + `<div class="note">MCX has no UCC API: make the UCC upload batch on <a href="#reports" data-act="go" data-id="reports">Regulatory reports</a> and upload it on MCX’s portal.</div>`;
    if (d.missing) return head + tabs + `<div class="note">${UCC_EX[d.ex]} connection not available yet.</div>`;
    const v = d.v, c = v.counts || {};
    const conn = v.connection === 'Not set'
      ? `<div class="note warn"><b>The ${esc(v.exchange)} UCC API is not set up.</b> The firm’s admin enters the API user and password under <a href="#connections" data-act="go" data-id="connections">Connections</a>. Until then, records can be checked and previewed here but not sent.</div>`
      : v.connection === 'Problem' ? `<div class="note bad"><b>Last call to ${esc(v.exchange)} failed.</b> ${esc(v.problem || '')}</div>`
      : `<div class="note">${pill(v.live ? 'Production' : 'Test (UAT)', v.live ? 'good' : 'warn')} ${v.auto ? 'New and changed clients go automatically every 15 minutes from 08:00 to 22:00, and PAN status is refreshed once a day.' : 'Automatic sending is off: send from here.'}</div>`;
    const pend = c['Not sent'] || 0;
    return head + tabs + conn +
      `<div class="kpis">${kpi('Not sent', nf(pend), 'new or changed, ready', pend ? 'warn' : '')}${kpi('Needs fixing', nf(c['Needs fixing'] || 0), 'checked before sending', c['Needs fixing'] ? 'bad' : '')}${kpi('Sent', nf(c.Sent || 0), 'waiting for the exchange')}${kpi('Accepted', nf(c.Accepted || 0), 'registered')}${kpi('Rejected', nf(c.Rejected || 0), 'by the exchange', c.Rejected ? 'bad' : '')}</div>` +
      `<div class="panel"><div class="hd"><div class="tools">${segBtns('uccSt', [['open', 'To act on'], ['all', 'All'], ['Accepted', 'Accepted'], ['Sent', 'Sent']], 'ucc-t')}<input class="in" data-ui="uccQ" data-part="ucc-t" placeholder="Filter by client, PAN or reason" value="${esc(UI.uccQ)}"></div>
        <div class="tools">${uccCan() ? `<button class="btn ${pend ? 'pri' : ''}" data-act="uccSend" ${pend && v.connection !== 'Not set' ? '' : 'disabled'}>Send pending (${pend})</button><button class="btn" data-act="uccSend" data-id="sel" ${v.connection !== 'Not set' ? '' : 'disabled'}>Send selected</button>` : ''}
        ${uccPanCan() ? `<button class="btn" data-act="uccPAN" ${v.connection !== 'Not set' ? '' : 'disabled'}>Check PAN status</button>` : ''}</div></div><div id="ucc-t">${P['ucc-t'](d)}</div></div>` +
      `<div class="panel"><div class="hd"><h3>Runs</h3><small>each call’s request (Aadhaar masked) and answer are kept with the run</small></div>${tbl([['Run'], ['When'], ['By'], ['Clients', 'r'], ['Accepted', 'r'], ['Rejected', 'r'], ['What happened']], (v.runs || []).map(r => `<tr><td class="mono">${esc(r.id)}</td><td class="small">${uccStamp(r.at)}</td><td>${esc(r.by === 'system:scheduler' ? 'Scheduler' : r.by)}</td><td class="r">${r.sent}</td><td class="r">${r.accepted}</td><td class="r ${r.rejected ? 'neg' : ''}">${r.rejected}</td><td class="wrap small">${esc(r.note)}</td></tr>`), { empty: 'Nothing sent yet.' })}</div>`;
  }
};

P['ucc-t'] = d => {
  const v = d.v, q = (UI.uccQ || '').trim().toLowerCase(), st = UI.uccSt;
  const rows = (v.clients || []).filter(r => st === 'all' || (st === 'open' ? ['Not sent', 'Needs fixing', 'Rejected'].includes(r.status) : r.status === st))
    .filter(r => !q || [r.ucc, r.name, r.pan, r.reason, ...(r.problems || [])].join(' ').toLowerCase().includes(q));
  const probs = r => (r.problems || []).length ? `<ul class="small" style="margin:2px 0 0 16px;padding:0">${r.problems.slice(0, 3).map(p => `<li>${esc(p)}</li>`).join('')}${r.problems.length > 3 ? `<li class="dim">and ${r.problems.length - 3} more</li>` : ''}</ul>` : '';
  return tbl([[''], ['Client'], ['PAN'], ['Action'], ['Status'], ['Reason'], ['PAN status'], ['Sent'], ['Result'], ['']], rows.map(r => `<tr>
    <td>${uccCan() && r.status === 'Not sent' ? `<input type="checkbox" aria-label="Select ${esc(r.ucc)}" data-act="uccTick" data-id="${esc(r.ucc)}" ${UI.uccSel.has(r.ucc) ? 'checked' : ''}>` : ''}</td>
    <td><b>${esc(r.ucc)}</b><div class="dim small">${esc(r.name)}</div></td><td class="mono small">${esc(r.pan)}</td><td>${esc(r.action)}</td><td>${uccPill(r.status)}</td>
    <td class="wrap small">${r.problems && r.problems.length ? probs(r) : esc(r.reason || '')}</td><td class="small">${r.pan_status ? uccPill(r.pan_status.split(' · ')[0]) + (r.pan_status.includes(' · ') ? `<div class="neg small">${esc(r.pan_status.split(' · ').slice(1).join(' · '))}</div>` : '') : '<span class="dim">—</span>'}</td>
    <td class="small">${uccStamp(r.sent_at)}${r.ref ? `<div class="dim mono">${esc(r.ref)}</div>` : ''}</td><td class="small">${uccStamp(r.result_at)}</td>
    <td class="nowrap"><button class="btn sm" data-act="uccPreview" data-id="${esc(r.ucc)}">Preview</button></td></tr>`), { tall: true, empty: st === 'open' ? 'Nothing to act on: every client is sent or accepted.' : 'No clients match.' });
};

function uccDetailsForm(dt) {
  const f = (id, label, val, ph = '') => `<div class="fld"><label for="ucd-${id}">${label}</label><input class="in" id="ucd-${id}" value="${esc(val || '')}" placeholder="${esc(ph)}"></div>`;
  return `<details style="margin-top:12px"><summary><b>Exchange UCC details</b> <span class="dim small">what the client record does not hold; empty fields come from the online application</span></summary>
    <div class="grid g2" style="margin-top:8px">${f('a1', 'Address line 1', dt.address1)}${f('a2', 'Address line 2', dt.address2)}${f('a3', 'Address line 3', dt.address3)}${f('city', 'City', dt.city)}
    ${f('state', 'State', dt.state, 'e.g. Maharashtra')}${f('pin', 'PIN code', dt.pin)}${f('gender', 'Gender', dt.gender, 'Male, Female or Transgender')}${f('guardian', 'Father’s or spouse’s name', dt.guardian)}
    ${f('marital', 'Marital status', dt.marital, 'Single, Married, Widowed, Divorced')}${f('occ', 'Occupation', dt.occupation, 'Private sector, Public sector, Business…')}
    ${f('occd', 'Occupation details (for Others)', dt.occupation_detail)}${f('inc', 'Income declared on (YYYY-MM-DD)', dt.income_as_on)}
    ${f('cat', 'Client category code (when not from the client type)', dt.category, '2 partnership, 5 trust')}${f('cin', 'CIN (companies)', dt.cin)}
    ${f('poaf', 'POA for funds (Y/N)', dt.poa_funds, 'N')}${f('poas', 'POA for securities (Y/N)', dt.poa_securities, 'N')}</div>
    <div class="fld"><label for="ucd-contacts">Contact persons of a firm, HUF, company or trust: one a line, as name | PAN | address | phone | e-mail | DIN</label><textarea class="in mono" id="ucd-contacts" rows="3">${esc((dt.contacts || []).map(c => [c.name, c.pan, c.address, c.phone, c.email, c.din].map(x => x || '').join(' | ')).join('\n'))}</textarea></div>
    ${uccCan() ? '<div class="tools"><button class="btn" data-act="uccSaveDetails">Save details</button></div>' : ''}</details>`;
}

Object.assign(A, {
  uccEx: id => { UI.uccEx = id; UI.uccSel = new Set(); render(); },
  uccTick: (id, el) => { if (el.checked) UI.uccSel.add(id); else UI.uccSel.delete(id); },
  uccSend: id => {
    const uccs = id === 'sel' ? [...UI.uccSel] : [];
    if (id === 'sel' && !uccs.length) { toast('Tick the clients to send first'); return; }
    act(() => post(`${uccPath()}/send`, { uccs }), r => r.message + (r.problems?.length ? ` · ${r.problems.length} not sent: ${r.problems[0]}` : '')).then(() => { UI.uccSel = new Set(); }).catch(() => {});
  },
  uccPAN: () => act(() => post(`${uccPath()}/pan-status`, { uccs: [...UI.uccSel] }), r => r.message).catch(() => {}),
  uccPreview: async id => {
    let p;
    try { p = await get(`${uccPath()}/preview/${encodeURIComponent(id)}`); } catch (e) { toast(e.message, true); return; }
    UI.uccPv = p;
    const rec = p.records || [], keys = rec.length ? Object.keys(rec[0]).filter(k => rec.some(r => r[k])).sort() : [];
    modal(mhd(`${esc(p.ucc)} · ${esc(p.name)}`, `${uccPill(p.status)} ${esc(p.action || '')}${(p.pending || []).length ? ' · would send segment ' + p.pending.map(esc).join(', ') : ' · nothing new to send'}`) + `<div class="bd">
      ${(p.problems || []).length ? `<div class="note bad"><b>Fix these before it can be sent</b><ul>${p.problems.map(x => `<li>${esc(x)}</li>`).join('')}</ul></div>` : '<div class="note good">The record passes the exchange’s field rules.</div>'}
      ${rec.length ? `<div class="tw tall"><table class="tbl"><thead><tr><th>Field</th>${rec.map(r => `<th>Segment ${esc(r.ccdSegInd || r.segment || '')}</th>`).join('')}</tr></thead><tbody>${keys.map(k => `<tr><td class="mono small">${esc(k)}</td>${rec.map(r => `<td class="mono small wrap">${esc(r[k] || '')}</td>`).join('')}</tr>`).join('')}</tbody></table></div>
        <p class="dim small">Empty fields are sent empty and not shown. PAN and account numbers are masked here.</p>` : ''}
      ${uccDetailsForm(p.details || {})}
      ${(p.history || []).length ? `<h4>History</h4><ul class="small">${p.history.slice().reverse().map(h => `<li>${uccStamp(h.at)} · ${esc(h.what)}${h.ref ? ' · ' + esc(h.ref) : ''}${h.detail ? ' · ' + esc(h.detail) : ''}</li>`).join('')}</ul>` : ''}
      <div id="ucc-perr"></div></div><div class="ft"><button class="btn" data-act="close">Close</button>${uccCan() && (p.pending || []).length && !(p.problems || []).length ? `<button class="btn pri" data-act="uccSendOne" data-id="${esc(p.ucc)}">Send now</button>` : ''}</div>`, true);
  },
  uccSendOne: id => act(() => post(`${uccPath()}/send`, { uccs: [id] }), r => r.message).catch(() => {}),
  uccSaveDetails: async () => {
    const p = UI.uccPv; if (!p) return;
    const v = id => ($('ucd-' + id)?.value || '').trim();
    const contacts = v('contacts').split('\n').map(l => l.split('|').map(x => x.trim())).filter(x => x[0]).map(([name, pan, address, phone, email, din]) => ({ name, pan: pan || '', address: address || '', phone: phone || '', email: email || '', din: din || '' }));
    const b = { address1: v('a1'), address2: v('a2'), address3: v('a3'), city: v('city'), state: v('state'), pin: v('pin'), gender: v('gender'), guardian: v('guardian'), marital: v('marital'),
      occupation: v('occ'), occupation_detail: v('occd'), income_as_on: v('inc'), category: v('cat'), cin: v('cin'), poa_funds: v('poaf'), poa_securities: v('poas'), contacts };
    const keep = p.details || {};
    for (const k of ['country', 'perm_address1', 'perm_address2', 'perm_address3', 'perm_city', 'perm_state', 'perm_country', 'perm_pin', 'nationality', 'pep', 'ipv', 'upi', 'agreement_date']) if (keep[k]) b[k] = keep[k];
    try { await post(`/v1/ucc-api/details/${encodeURIComponent(p.ucc)}`, b); toast('Details saved'); await A.uccPreview(p.ucc); render({ keep: true }); }
    catch (e) { const el = $('ucc-perr'); if (el) el.innerHTML = errHTML(e); else toast(e.message, true); }
  },
});
