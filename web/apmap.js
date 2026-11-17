'use strict';
/* Ledgerline console: clients mapped to authorised persons.

   On Branches & partners:
   - a warning when an AP's own mobile or email is on a client's record
     (NSE/COMP/63628, Annexure A item 28);
   - the AP client reporting tab: each exchange's weekly file of clients
     newly mapped, re-mapped or unmapped to an AP (NSE/COMP/49509; MSE
     circular 10854, item 15 of MSE/MEM/15275/2024), checked against the
     client's UCC, with the upload and its acknowledgement; a client's AP
     history; and the monthly count reconciliation with each exchange;
   - the AP's own mobile and its number at each exchange on the partner form.

   Server: internal/ops/apmapping.go; internal/api/apcomplaints.go. Names
   are prefixed am. */

Object.assign(UI, { amWeek: '', amMonth: '' });
const AM_ST = { 'In the file': 'good', 'In the file · check': 'warn', Waits: 'nt', 'To fix': 'bad', 'To upload': 'warn', Uploaded: 'good', 'Nothing to report': 'nt', Waiting: 'nt', Done: 'good', Due: 'warn', Overdue: 'bad', Upcoming: 'nt' };
const amCan = () => has('Ops maker', 'Ops checker', 'Compliance officer', 'Risk manager');
const amWrite = () => has('Ops maker', 'Ops checker', 'Compliance officer');

// The partners screen also reads the contact check and, on its tab, the report.
const amLoad0 = V.partners.load;
V.partners.load = async () => {
  const d = await amLoad0();
  if (scopedUser() || !amCan()) return d;
  const wk = UI.amWeek ? `&week=${UI.amWeek}` : '', mo = UI.amMonth ? `?month=${UI.amMonth}` : '';
  const [ac, am, amc] = await Promise.all([get('/v1/ap-mapping/contacts').catch(() => null),
    UI.ptTab === 'am' ? get(`/v1/ap-mapping?date=${S.date}${wk}`).catch(e => ({ error: e.message })) : null,
    UI.ptTab === 'am' ? get(`/v1/ap-mapping/counts${mo}`).catch(e => ({ error: e.message })) : null]);
  return Object.assign(d, { ac, am, amc });
};

// The warning on Branches & partners.
function amWarn(d) {
  const hs = d.ac || [];
  if (!hs.length) return '';
  const by = {};
  hs.forEach(h => (by[h.partner] = by[h.partner] || []).push(h));
  return `<div class="note warn"><b>An AP's own mobile or email is on ${hs.length} client record${hs.length > 1 ? 's' : ''}.</b> NSE/COMP/63628 (Annexure A item 28) does not allow it: correct the client's contact details with a change request and send the UCC to the exchanges again.
    <ul style="margin:6px 0 0 18px">${Object.entries(by).map(([ap, xs]) => `<li><b>${esc(ap)}</b> ${esc(xs[0].name)}: its own ${[...new Set(xs.map(x => x.field))].join(' and ')} on ${xs.map(x => `<button class="linkish mono" data-act="client" data-id="${esc(x.ucc)}">${esc(x.ucc)}</button>${x.own_client ? '' : ' <span class="dim small">(not its client)</span>'}`).join(', ')}</li>`).join('')}</ul></div>`;
}

/* ---------- the AP client reporting tab ---------- */
P['pt-am'] = d => {
  if (!d.am) { setTimeout(() => render({ keep: true })); return '<p class="loading pad">Loading…</p>'; }
  if (d.am.error) return `<div class="pad">${errHTML({ message: d.am.error })}</div>`;
  const v = d.am;
  const weeks = (v.weeks || []).map(w => `<option value="${esc(w)}" ${w === v.week ? 'selected' : ''}>Week to ${fmtDate(w)}</option>`).join('');
  return `<div class="bd"><div class="tools"><label class="dim small" for="am-week">Week (Monday to Sunday)</label><select class="in sm" id="am-week">${weeks}${v.weeks.includes(v.week) ? '' : `<option selected value="${esc(v.week)}">Week to ${fmtDate(v.week)}</option>`}</select><button class="btn sm" data-act="amWeek">Show</button>
      ${pill(v.status, AM_ST[v.status])}<span class="dim small">${esc(v.label)} · due ${fmtDate(v.due)}, the 2nd trading day of the next week</span>
      <button class="btn sm" data-act="amHist">A client's AP history</button></div>
    ${(v.notes || []).map(n => `<div class="note">${esc(n)}</div>`).join('')}
    <p class="dim small">Each exchange gets its own file (it is not a common submission): the clients newly mapped, re-mapped or unmapped to an AP up to the week's end, and new clients of APs, that the exchange does not have yet. A record waits while the client's UCC is not yet registered at the exchange, and goes in the week it is. Upload the file on the exchange's portal (NSE: ENIT, Compliance &gt; Authorised Person) and record the acknowledgement here.${v.started ? ` The register started on ${fmtDate(v.started)} with ${v.existing} existing mapping${v.existing === 1 ? '' : 's'}, taken as already reported.` : ''}</p></div>
    ${v.exchanges.map(amExchangeHTML).join('') || '<div class="pad dim">No exchange to report to: enter the member codes in Settings.</div>'}
    ${amChangesHTML(v)}${amCountsHTML(d.amc)}`;
};

function amExchangeHTML(ex) {
  const n = ex.counts || {}, files = (n['In the file'] || 0) + (n['In the file · check'] || 0);
  const row = r => `<tr><td><button class="linkish mono" data-act="client" data-id="${esc(r.ucc)}">${esc(r.ucc)}</button> ${esc(r.name)}<div class="dim small mono">${esc(r.pan)}</div></td>
    <td>${esc(r.kind)}${r.new_client ? ' <span class="tag">new client</span>' : ''}<div class="small">${r.from ? `${esc(r.from)} <span class="mono dim">${esc(r.from_reg || '—')}</span>` : '—'} → ${r.to ? `${esc(r.to)} <span class="mono dim">${esc(r.to_reg || '—')}</span>` : 'no AP'}</div></td>
    <td class="small">${fmtDate(r.changed_on)}<div class="dim">${esc(r.by || '')}${r.via ? ' · ' + esc(r.via) : ''}</div></td>
    <td>${pill(r.state, AM_ST[r.state])}${(r.notes || []).map(x => `<div class="small wrap">${esc(x)}</div>`).join('')}</td></tr>`;
  const ups = (ex.uploads || []).map(u => `<div class="small">Uploaded ${fmtDate(u.date)} · acknowledgement <b class="mono">${esc(u.ack)}</b> · ${u.rows} row${u.rows === 1 ? '' : 's'} · ${esc(u.by)}${u.late ? ' · ' + pill('late', 'bad') : ''} <button class="linkish small" data-act="dl" data-id="/v1/periodic/files/${esc(u.file_id)}" data-name="${esc(u.file_name)}">${esc(u.file_name)}</button>${u.note ? ` <span class="dim">${esc(u.note)}</span>` : ''}</div>`).join('');
  return `<div class="panel"><div class="hd"><h3>${esc(ex.exchange)} <span class="mono dim small">${esc(ex.member)}</span></h3><small>${pill(ex.status, AM_ST[ex.status])} ${Object.entries(n).map(([k, c]) => `${c} ${esc(k.toLowerCase())}`).join(' · ')}</small>
      <div class="tools">${files && amWrite() ? `<button class="btn sm" data-act="dl" data-id="/v1/ap-mapping/file?week=${encodeURIComponent(UI.amWeek || C.partners.am.week)}&exchange=${esc(ex.exchange)}" data-name="${esc(ex.file_name)}">Download ${esc(ex.file_name)}</button><button class="btn sm pri" data-act="amUp" data-id="${esc(ex.exchange)}">Record the upload</button>` : ''}${has('Compliance officer') ? `<button class="btn sm" data-act="amTake" data-id="${esc(ex.exchange)}" title="For an exchange the firm reported to before Ledgerline had its member code">Take as reported</button>` : ''}</div></div>
    ${ex.confirmed ? '' : `<div class="pad"><span class="dim small">${esc(ex.exchange)}'s upload layout could not be confirmed: Ledgerline writes the columns of config/formats/${esc(ex.layout)}.json. Check them against the exchange's template before the first upload.</span></div>`}
    ${ex.note ? `<div class="pad"><span class="dim small">${esc(ex.note)}</span></div>` : ''}
    ${ups ? `<div class="pad">${ups}</div>` : ''}
    ${tbl([['Client'], ['Change'], ['Changed'], ['Check']], (ex.records || []).map(row), { empty: 'Nothing for this exchange: every client\'s AP is as the exchange has it.' })}</div>`;
}

function amChangesHTML(v) {
  const cs = v.changes || [];
  if (!cs.length) return '';
  return `<div class="panel"><div class="hd"><h3>Changes made in the week</h3><small>${cs.length} change${cs.length === 1 ? '' : 's'} of a client's AP</small></div>${tbl([['Client'], ['Change'], ['On'], ['By'], ['Where']], cs.map(c => `<tr><td class="mono">${esc(c.ucc)} <span class="dim">${esc(c.name)}</span></td><td>${esc(c.kind)}${c.new_client ? ' <span class="tag">new client</span>' : ''} <span class="small">${esc(c.from || '—')} → ${esc(c.to || 'no AP')}</span></td><td class="n">${fmtDate(c.date)}</td><td>${esc(c.by)}</td><td class="small">${esc(c.via)}</td></tr>`), { tall: true })}</div>`;
}

/* ---------- the monthly count reconciliation ---------- */
function amCountsHTML(v) {
  if (!v) return '';
  if (v.error) return `<div class="pad">${errHTML({ message: v.error })}</div>`;
  const can = amWrite();
  const ex = x => {
    const r = x.recorded, by = {};
    (r?.lines || []).forEach(l => { by[l.partner] = l; });
    const row = l => { const g = by[l.partner]; return `<tr><td><b>${esc(l.partner)}</b> ${esc(l.name || '')}<div class="mono dim small">${esc(l.reg_no || 'no registration number')}</div></td><td class="r">${l.ledgerline}</td>
      <td class="r">${g ? `${g.exchange}` : can ? `<input class="in sm" style="width:80px" inputmode="numeric" data-amc="${esc(x.exchange)}" data-ap="${esc(l.partner)}" aria-label="${esc(x.exchange)} count for ${esc(l.partner)}">` : '—'}</td>
      <td class="r ${g && g.difference ? 'neg' : ''}">${g ? g.difference : ''}</td>
      <td class="small wrap">${g ? `${(g.missing || []).length ? 'Not on the exchange: ' + g.missing.map(esc).join(', ') : ''}${(g.extra || []).length ? ' On the exchange only: ' + g.extra.map(esc).join(', ') : ''}` : can ? `<textarea class="in sm" rows="1" data-amcl="${esc(x.exchange)}" data-ap="${esc(l.partner)}" placeholder="The exchange's list of the AP's clients (optional)" aria-label="${esc(x.exchange)} client list for ${esc(l.partner)}"></textarea>` : ''}</td></tr>`; };
    return `<div class="sub"><h4 style="margin:10px 0 4px">${esc(x.exchange)} ${r ? pill(r.matched ? 'Matched' : 'Differences noted', r.matched ? 'good' : 'warn') + ` <span class="dim small">${esc(r.by)} · ${fmtDate(r.at.slice(0, 10))}${r.note ? ' · ' + esc(r.note) : ''}</span>` : ''}</h4>
      ${tbl([['Authorised person'], ['Ledgerline', 'r'], [esc(x.exchange) + ' shows', 'r'], ['Difference', 'r'], ['Clients that differ']], x.lines.map(row), { empty: 'No active authorised person.' })}
      ${!r && can && x.lines.length ? `<div class="tools" style="margin-top:6px"><input class="in sm" style="flex:1" id="amc-note-${esc(x.exchange)}" placeholder="If the counts differ: what was done about it"><button class="btn sm pri" data-act="amCount" data-id="${esc(x.exchange)}">Record ${esc(x.exchange)}'s counts</button></div>` : ''}</div>`;
  };
  return `<div class="panel"><div class="hd"><h3>AP client counts with each exchange · ${esc(v.label)}</h3><small>${pill(v.status, AM_ST[v.status])} due ${fmtDate(v.due)} (Ledgerline's date: NSE/COMP/63628 asks for the count to be kept current)</small>
    <div class="tools"><input class="in sm" type="month" id="am-month" value="${esc(v.month)}" aria-label="Month"><button class="btn sm" data-act="amMonth">Show</button></div></div>
    <div class="bd"><p class="dim small" style="margin:0">Note the number of clients each exchange's portal shows for each AP at the month's end. Ledgerline's count is the clients mapped to the AP then who trade on that exchange. Paste the exchange's list of the AP's clients, if you have it, to see which clients differ.</p>${v.exchanges.map(ex).join('')}</div></div>`;
}

/* ---------- the partner form: the AP's own mobile and exchange numbers ---------- */
function amPartnerFields(p) {
  const xs = Object.keys(S.me?.member_codes || {}).filter(x => S.me.member_codes[x]);
  const regs = p.exchange_regs || {};
  return `<div class="fld"><label for="pt-m">AP's own mobile</label><input class="in mono" id="pt-m" inputmode="numeric" value="${esc(p.mobile || '')}"><div class="dim small">Must not be on any client's record (NSE/COMP/63628)</div></div>
    ${xs.length ? `<fieldset class="full"><legend class="small">AP registration number at each exchange (when it differs from the one above)</legend><div class="form">${xs.map(x => `<div class="fld"><label for="pt-x-${esc(x)}">${esc(x)}</label><input class="in mono" id="pt-x-${esc(x)}" data-amreg="${esc(x)}" value="${esc(regs[x] || '')}"></div>`).join('')}</div></fieldset>` : ''}`;
}
function amPartnerValues() {
  const regs = {};
  document.querySelectorAll('[data-amreg]').forEach(el => { if (el.value.trim()) regs[el.dataset.amreg] = el.value.trim(); });
  return { mobile: ($('pt-m')?.value || '').trim(), exchange_regs: regs };
}

Object.assign(A, {
  amWeek: () => { UI.amWeek = $('am-week').value; render({ keep: true }); },
  amMonth: () => { UI.amMonth = $('am-month').value; render({ keep: true }); },
  amUp: x => {
    const v = C.partners.am, ex = v.exchanges.find(e => e.exchange === x);
    confirmModal(`Record the upload to ${x}`, `${ex.file_name} · week ${v.label}`, `<div class="form"><div class="fld"><label for="amu-d">Uploaded on</label><input class="in" type="date" id="amu-d" value="${esc(S.date)}"></div>
      <div class="fld"><label for="amu-a">Acknowledgement or reference from the portal</label><input class="in mono" id="amu-a"></div><div class="fld full"><label for="amu-n">Note (optional)</label><input class="in" id="amu-n"></div></div>
      <div class="note">The file as made now is kept with the upload, and ${esc(x)} is taken to have these clients' APs from then on.</div>`, 'Record',
      () => act(() => post('/v1/ap-mapping/uploads', { week: v.week, exchange: x, date: $('amu-d').value, ack: $('amu-a').value.trim(), note: $('amu-n').value.trim() }), `Upload to ${x} recorded`));
  },
  amTake: x => confirmModal(`Take the present mappings as reported to ${x}`, 'For an exchange the firm reported its AP clients to before Ledgerline had its member code. It is in the audit trail.',
    `<div class="fld"><label for="amt-n">Why</label><input class="in" id="amt-n" placeholder="e.g. reported to ${esc(x)} from the previous system"></div>`, 'Take as reported',
    () => act(() => post('/v1/ap-mapping/taken-as-reported', { exchange: x, note: $('amt-n').value.trim() }), r => `${r.clients} clients taken as reported to ${x}`)),
  amCount: x => {
    const lines = [...document.querySelectorAll(`[data-amc="${x}"]`)].map(el => {
      const cl = document.querySelector(`[data-amcl="${x}"][data-ap="${el.dataset.ap}"]`);
      return { partner: el.dataset.ap, count: el.value.trim() === '' ? null : +el.value, clients: cl ? cl.value : '' };
    });
    act(() => post('/v1/ap-mapping/counts', { month: C.partners.amc.month, exchange: x, lines, note: ($(`amc-note-${x}`)?.value || '').trim() }), `${x}'s counts recorded`);
  },
  amHist: () => {
    confirmModal("A client's AP history", 'Every change of the client\'s authorised person: when, from and to, and who made it.', `<div class="fld"><label for="amh-u">Client code</label><input class="in mono" id="amh-u"></div><div id="amh-r"></div>`, 'Show', async () => {
      const u = $('amh-u').value.trim().toUpperCase();
      const h = await get(`/v1/ap-mapping/history/${encodeURIComponent(u)}`);
      $('amh-r').innerHTML = tbl([['On'], ['Change'], ['By'], ['Where']], h.map(c => `<tr><td class="n">${fmtDate(c.date) || '—'}</td><td>${esc(c.kind)} <span class="small">${esc(c.from || '—')} → ${esc(c.to || 'no AP')}</span></td><td>${esc(c.by)}</td><td class="small">${esc(c.via)}</td></tr>`), { empty: 'No AP on record for this client.' });
      $('cm-ok').disabled = false;
    });
  },
});
