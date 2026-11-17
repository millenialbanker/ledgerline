'use strict';
/* Ledgerline console: lists checked against the clients.

   - Revoked mobiles ("mnrl", Clients & onboarding): clients whose
     registered mobile, or a joint holder's, is on DoT's Mobile Number
     Revocation List, from NSE's daily report, DoT's MNRL (DIP) or NSDL's
     e-PASS; NSDL's reactivation data lifts only the MNRL hold. Each match
     is worked here: contacted, number updated (by a change request), not
     the client's number, reactivated.
   - Deceased clients ("deceased", Compliance): the KRAs' monthly demise
     list from NSE's ENIT, with the debit block, the death verified or
     found false, the UCC closed on each exchange before the month's
     cut-off, and the transmission information sent.

   Server: internal/ops/mnrl.go, deceased.go, listfiles.go;
   internal/api/clientlists.go. Names are prefixed cl. */

Object.assign(UI, { clMF: 'open', clMQ: '', clDF: 'open', clDQ: '' });
const CL_MST = { New: 'bad', Contacted: 'warn', 'Number updated': 'good', "Not the client's number": 'nt', Reactivated: 'good' };
const CL_DST = { 'Reported by KRA': 'bad', 'Death verified': 'warn', 'UCCs closed': 'acc', Done: 'good', 'Intimation false': 'nt' };
const CL_UST = { 'To close': 'bad', 'In the UCC upload': 'acc', Closed: 'good' };
const clMOpen = s => s === 'New' || s === 'Contacted';
const clDOpen = s => s !== 'Done' && s !== 'Intimation false';
const clStamp = t => t ? fmtDate(String(t).slice(0, 10)) : '—';
const clHist = h => (h || []).slice().reverse().map(e => `<li><b>${esc(e.status)}</b> · ${clStamp(e.at)} · ${esc(e.by)}${e.note ? `<div class="dim">${esc(e.note)}</div>` : ''}</li>`).join('');

/* ---------- revoked mobiles ---------- */
V.mnrl = {
  load: () => get(`/v1/mnrl?date=${S.date}`),
  html: d => {
    const c = d.counts || {}, last = (d.lists || [])[0];
    const src = tbl([['List'], ['Where to download it'], ['For'], ['How often'], ['Last loaded'], ['']], (d.sources || []).map(s => `<tr class="${s.applies ? '' : 'dim'}"><td><b>${esc(s.title)}</b><div class="dim small">${esc(s.basis)}</div></td><td class="small wrap">${esc(s.from)}</td><td class="small">${esc(s.for)}</td><td class="small">${esc(s.when)}</td><td class="n">${s.last ? fmtDate(s.last) : '<span class="dim">never</span>'}${s.last && s.last < S.date && s.when === 'Daily' && s.applies ? '<div class="small neg">not today’s</div>' : ''}</td><td>${d.can_work ? `<button class="btn sm" data-act="clMLoad" data-id="${esc(s.id)}">Load</button>` : ''}</td></tr>`));
    return ph('Revoked mobiles', 'Clients whose registered mobile, or a joint holder’s, is on DoT’s Mobile Number Revocation List (MNRL): numbers disconnected that an operator may give to someone else. Load the day’s list; each match flags the client, holds SMS and WhatsApp to the number (email to the registered address goes on) and asks an Ops maker to confirm the client’s number through another registered channel. The match closes when the mobile is changed by an approved change request, and the change goes to the exchanges’ UCC and the depository.',
      d.can_work ? '<button class="btn pri" data-act="clMLoad">Load list</button>' : '') +
      `<div class="kpis">${kpi('Open matches', nf(d.open), 'new or contacted', d.open ? 'warn' : 'good')}${kpi('Not taken up in time', nf(d.overdue), '3 business days after they were found', d.overdue ? 'bad' : 'good')}${kpi('Numbers held', nf(d.held), 'no SMS or WhatsApp to them')}${kpi('Numbers updated', nf(c['Number updated'] || 0), 'by a change request')}${kpi('Last list', last ? fmtDate(last.date) : '—', last ? esc(last.source) + ' · ' + nf(last.rows) + ' rows' : 'none loaded yet', last ? '' : 'warn')}</div>
      <div class="panel"><div class="hd"><h3>Matches</h3><div class="tools">${segBtns('clMF', [['open', `Open · ${d.open}`], ['all', 'All'], ...(d.statuses || []).filter(s => !clMOpen(s)).map(s => [s, `${s} · ${c[s] || 0}`])], 'cl-mt')}<input class="in" data-ui="clMQ" data-part="cl-mt" placeholder="Filter by client or match" value="${esc(UI.clMQ)}"></div></div><div id="cl-mt">${P['cl-mt'](d)}</div></div>
      <div class="panel"><div class="hd"><h3>The lists</h3><small>none has a published file layout: the columns are read by their header names (config/formats), so a file whose header differs is refused with what it lacks</small></div>${src}</div>
      <div class="panel"><div class="hd"><h3>Lists loaded</h3></div>${tbl([['List'], ['File'], ['For'], ['Rows', 'r'], ['New', 'r'], ['Already open', 'r'], ['Reactivated', 'r'], ['Left out', 'r'], ['By']], (d.lists || []).map(l => `<tr><td class="mono small">${esc(l.id)}<div>${esc(l.source)}</div></td><td class="small wrap mono">${esc(l.file)}${(l.notes || []).length ? `<div class="dim small wrap">${l.notes.map(esc).join('<br>')}</div>` : ''}</td><td class="n">${fmtDate(l.date)}</td><td class="r">${nf(l.rows)}</td><td class="r ${l.new ? 'neg' : ''}">${nf(l.new)}</td><td class="r">${nf(l.again)}</td><td class="r">${nf(l.cleared)}</td><td class="r" title="rows of nobody of the firm's, not a number, or not fitting the layout">${nf(l.skipped + l.issues)}</td><td class="small">${esc(l.by)}<div class="dim">${clStamp(l.at)}</div></td></tr>`), { empty: 'No list loaded yet.' })}</div>`;
  }
};
P['cl-mt'] = d => {
  const q = (UI.clMQ || '').trim().toLowerCase();
  const rows = (d.matches || []).filter(m => (UI.clMF === 'all' || (UI.clMF === 'open' ? clMOpen(m.status) : m.status === UI.clMF)) && (!q || (m.id + ' ' + m.ucc + ' ' + m.name).toLowerCase().includes(q)));
  return tbl([['Match'], ['Client'], ['Number'], ['Status'], ['What next'], ['']], rows.map(m => {
    const open = clMOpen(m.status), holder = m.holder === 'First' ? '' : `<div class="small">${esc(m.holder)} holder${m.holder_name ? ': ' + esc(m.holder_name) : ''}</div>`;
    const next = open ? (m.holder === 'First' && m.on_record ? 'Contact the client through another registered channel; raise the mobile change on <button class="linkish" data-act="go" data-id="changes">Change requests</button> with the client’s request.' : `Contact the holder and change the number at ${esc(m.depository || 'the depository')}; then mark it updated.`)
      : (m.follow || []).map(esc).join('<br>') || '<span class="dim">—</span>';
    const acts = open && d.can_work ? `<button class="btn sm" data-act="clMSet" data-id="${esc(m.id)}" data-s="Contacted">Contacted</button><button class="btn sm" data-act="clMSet" data-id="${esc(m.id)}" data-s="Not the client's number">Not the client’s number</button><button class="btn sm" data-act="clMSet" data-id="${esc(m.id)}" data-s="Reactivated">Reactivated</button>${m.holder !== 'First' || !m.on_record || m.depository === 'NSDL' ? `<button class="btn sm" data-act="clMSet" data-id="${esc(m.id)}" data-s="Number updated">Number updated</button>` : ''}` : '';
    return `<tr><td class="mono small">${esc(m.id)}<div class="dim">${esc(m.source)} · ${fmtDate(m.list_date)}</div></td>
      <td><button class="linkish mono" data-act="client" data-id="${esc(m.ucc)}">${esc(m.ucc)}</button><div class="small">${esc(m.name)}</div>${holder}${m.account ? `<div class="dim small mono">${esc(m.depository || '')} ${esc(m.account)}</div>` : ''}</td>
      <td class="mono">${esc(m.mobile)}${m.revoked || m.reason ? `<div class="dim small">${esc([m.revoked ? 'disconnected ' + m.revoked : '', m.reason].filter(Boolean).join(' · '))}</div>` : ''}${open ? `<div>${pill('SMS and WhatsApp held', 'warn')}</div>` : ''}</td>
      <td>${pill(m.status, CL_MST[m.status] || 'nt')}${open ? `<div class="small ${m.overdue ? 'neg' : 'dim'}">take up by ${fmtDate(m.due)}${m.overdue ? ' · overdue' : ''}</div>` : `<div class="dim small">${clStamp(m.closed)}${m.new_mobile ? ' · now ' + esc(m.new_mobile) : ''}${m.change ? ' · ' + esc(m.change) : ''}</div>`}</td>
      <td class="small wrap cl-next">${next}</td>
      <td><div class="split">${acts}<button class="btn sm" data-act="clMHist" data-id="${esc(m.id)}">History</button></div></td></tr>`;
  }), { tall: true, empty: (d.matches || []).length ? 'Nothing matches the filter.' : 'No client’s number has been found on a list.' });
};

/* ---------- deceased clients ---------- */
V.deceased = {
  load: () => get(`/v1/deceased?date=${S.date}`),
  html: d => {
    const c = d.counts || {}, left = d.cutoff ? Math.round((new Date(d.cutoff) - new Date(S.date)) / 864e5) : null;
    const cyc = `<div class="note ${d.loaded ? '' : 'warn'}"><b>This month’s list: PANs reported by the KRAs up to the end of ${esc(clMonth(d.period))}.</b> Close their UCCs before ${fmtDate(d.cutoff)}${left !== null ? ` (${left > 0 ? left + ' day' + (left === 1 ? '' : 's') + ' left' : 'the cut-off has passed'})` : ''}${d.circular ? ', as ' + esc(d.circular) + ' says' : ' (the usual 21st: take the date from the month’s NSE circular)'}; from the cut-off those PANs cannot trade or even square off. ${d.loaded ? 'The list is loaded.' : 'Download <span class="mono">&lt;TMcode&gt;_Demise_Clients_by_KRA</span> from NSE’s member portal (ENIT &gt; Log &gt; Downloads) and load it here.'}${d.can_close ? ' <button class="linkish" data-act="clDCut">Change the cut-off</button>' : ''}</div>`;
    return ph('Deceased clients', 'Clients the KRAs report as deceased, from NSE’s monthly list. Debits are blocked at once: payouts, running account settlement, debits from the demat accounts, trading limits and mutual fund orders, until transmission. Verify the death (or, if the intimation is false, tell the KRA the same day), close the UCC on every exchange before the cut-off, and give the nominee or legal heirs the transmission procedure within 5 days of the KRA notice.',
      `${d.can_close ? '<button class="btn" data-act="dl" data-id="/v1/deceased/ucc-list" data-name="deceased-uccs-to-close.csv">UCCs to close (CSV)</button>' : ''}${d.can_load ? '<button class="btn pri" data-act="clDLoad">Load the month’s list</button>' : ''}`) + cyc +
      `<div class="kpis">${kpi('Open', nf(d.open), 'reported and not done', d.open ? 'warn' : 'good')}${kpi('Steps overdue', nf(d.overdue), 'verification, UCC or transmission', d.overdue ? 'bad' : 'good')}${kpi('To verify', nf(c['Reported by KRA'] || 0), 'death certificate')}${kpi('UCCs to close', nf(c['Death verified'] || 0), 'verified, not closed everywhere')}${kpi('Done', nf(c.Done || 0), 'closed and transmission sent', 'good')}${kpi('Intimation false', nf(c['Intimation false'] || 0), 'the KRA told; block lifted')}</div>
      <div class="panel"><div class="hd"><h3>Clients</h3><div class="tools">${segBtns('clDF', [['open', `Open · ${d.open}`], ['all', 'All'], ...(d.stages || []).map(s => [s, `${s} · ${c[s] || 0}`])], 'cl-dt')}<input class="in" data-ui="clDQ" data-part="cl-dt" placeholder="Filter by client or case" value="${esc(UI.clDQ)}"></div></div><div id="cl-dt">${P['cl-dt'](d)}</div></div>
      <div class="panel"><div class="hd"><h3>Lists loaded</h3><small>the file is kept as received</small></div>${tbl([['List'], ['File'], ['PANs reported up to'], ['Cut-off'], ['Rows', 'r'], ['Matched', 'r'], ['Already open', 'r'], ['Not clients', 'r'], ['Left out', 'r'], ['By']], (d.lists || []).map(l => `<tr><td class="mono small">${esc(l.id)}</td><td class="small wrap mono">${esc(l.file)}${(l.notes || []).length ? `<div class="dim small wrap">${l.notes.map(esc).join('<br>')}</div>` : ''}</td><td>${esc(clMonth(l.period))}</td><td class="n">${fmtDate(l.cutoff)}${l.circular ? `<div class="dim small">${esc(l.circular)}</div>` : ''}</td><td class="r">${nf(l.rows)}</td><td class="r ${l.matched ? 'neg' : ''}">${nf(l.matched)}</td><td class="r">${nf(l.again)}</td><td class="r">${nf(l.not_clients)}</td><td class="r">${nf(l.issues)}</td><td class="small">${esc(l.by)}<div class="dim">${clStamp(l.at)}</div></td></tr>`), { empty: 'No list loaded yet.' })}</div>`;
  }
};
const clMonth = p => { const m = /^(\d{4})-(\d{2})$/.exec(p || ''); return m ? `${MON[+m[2] - 1]} ${m[1]}` : (p || ''); };
const clStep = (st, due, label) => st ? `${pill(label, 'good')}<div class="dim small">${fmtDate(st.on)} · ${esc(st.by)}${st.ref ? ' · ' + esc(st.ref) : ''}</div>${st.note ? `<div class="dim small wrap">${esc(st.note)}</div>` : ''}` : `<span class="small ${due && due < S.date ? 'neg' : 'dim'}">due ${fmtDate(due)}</span>`;
P['cl-dt'] = d => {
  const q = (UI.clDQ || '').trim().toLowerCase();
  const rows = (d.cases || []).filter(x => (UI.clDF === 'all' || (UI.clDF === 'open' ? clDOpen(x.stage) : x.stage === UI.clDF)) && (!q || (x.id + ' ' + x.ucc + ' ' + x.name).toLowerCase().includes(q)));
  return tbl([['Client'], ['Reported'], ['Death'], ['UCC closed'], ['Transmission'], ['Cut-off'], ['Stage'], ['']], rows.map(x => {
    const open = clDOpen(x.stage), verified = !!x.verified;
    const death = x.false ? `${pill('Intimation false', 'nt')}<div class="dim small">${fmtDate(x.false.on)} · ${esc(x.false.by)}${x.false.ref ? ' · ' + esc(x.false.ref) : ''}</div>` : clStep(x.verified, x.verify_due, 'Verified');
    const uccs = (x.uccs || []).length ? x.uccs.map(u => `<div>${pill(u.exchange + ': ' + u.status, CL_UST[u.status] || 'nt')}${u.status === 'Closed' && (u.ref || u.on) ? ` <span class="dim small">${fmtDate(u.on)}${u.ref ? ' · ' + esc(u.ref) : ''}</span>` : ''}${open && verified && u.status !== 'Closed' && d.can_step ? ` <button class="linkish small" data-act="clDStep" data-id="${esc(x.id)}" data-a="closed" data-x="${esc(u.exchange)}">record closed</button>` : ''}</div>`).join('') : '<span class="dim small">no exchange UCC</span>';
    const acts = !open ? '' : [
      !verified && !x.false && d.can_act ? `<button class="btn sm pri" data-act="clDStep" data-id="${esc(x.id)}" data-a="verify">Death verified</button><button class="btn sm" data-act="clDStep" data-id="${esc(x.id)}" data-a="false">Intimation false</button>` : '',
      verified && !x.closed_at && d.can_close && (x.uccs || []).some(u => u.status === 'To close') ? `<button class="btn sm pri" data-act="clDStep" data-id="${esc(x.id)}" data-a="close">Close the UCCs</button>` : '',
      !x.transmission && d.can_step ? `<button class="btn sm" data-act="clDStep" data-id="${esc(x.id)}" data-a="transmission">Transmission sent</button>` : ''].join('');
    return `<tr><td><button class="linkish mono" data-act="client" data-id="${esc(x.ucc)}">${esc(x.ucc)}</button><div class="small">${esc(x.name)}</div><div class="dim small mono">${esc(x.pan)} · by ${esc(x.matched_by)}</div></td>
      <td class="n">${fmtDate(x.reported_on)}${x.kra || x.kra_status ? `<div class="dim small">${esc([x.kra, x.kra_status].filter(Boolean).join(' · '))}</div>` : ''}</td>
      <td class="small">${death}</td><td class="small">${uccs}</td><td class="small">${clStep(x.transmission, x.transmission_due, 'Sent')}</td>
      <td class="n">${fmtDate(x.cutoff)}${open && !(x.uccs || []).every(u => u.status === 'Closed') ? `<div class="small ${x.days_left <= 0 ? 'neg' : x.days_left <= 3 ? 'warn-t' : 'dim'}">${x.days_left > 0 ? x.days_left + ' day' + (x.days_left === 1 ? '' : 's') + ' left' : 'passed'}</div>` : ''}</td>
      <td>${pill(x.stage, CL_DST[x.stage] || 'nt')}${(x.overdue || []).map(o => `<div class="small neg">${esc(o)}</div>`).join('')}${open && x.positions ? `<div class="small neg">${nf(x.positions)} open position${x.positions === 1 ? '' : 's'}: square off before the cut-off</div>` : ''}${open ? `<div class="dim small" title="${esc((x.held || []).join(', '))}">debits blocked</div>` : ''}</td>
      <td><div class="split">${acts}<button class="btn sm" data-act="clDHist" data-id="${esc(x.id)}">History</button></div></td></tr>`;
  }), { tall: true, empty: (d.cases || []).length ? 'Nothing matches the filter.' : 'No client of the firm is on a KRA demise list.' });
};

/* ---------- actions ---------- */
Object.assign(A, {
  clMLoad: id => {
    const ss = (C.mnrl?.sources || []), cur = id || ss.find(s => s.applies)?.id || 'nse-mnrl';
    confirmModal('Load a revocation list', 'Choose the list and its file (CSV, text or Excel, zipped is fine). The columns are found by their header names.',
      `<div class="form"><div class="fld full"><label for="cl-mf">List</label><select class="in" id="cl-mf">${ss.map(s => `<option value="${esc(s.id)}" ${s.id === cur ? 'selected' : ''}>${esc(s.title)}</option>`).join('')}</select></div>
      <div class="fld"><label for="cl-md">The list’s date</label><input class="in" type="date" id="cl-md" value="${esc(S.date)}" max="${esc(S.date)}"></div>
      <div class="fld"><label for="cl-mfile">File</label><input class="in" type="file" id="cl-mfile"></div></div>`,
      'Load', async () => {
        const f = $('cl-mfile').files[0]; if (!f) throw new Error('Choose the file');
        await act(() => api('POST', `/v1/mnrl/lists?format=${encodeURIComponent($('cl-mf').value)}&name=${encodeURIComponent(f.name)}&date=${encodeURIComponent($('cl-md').value)}`, f, true),
          l => `${l.source} list: ${l.rows} rows, ${l.new} new match${l.new === 1 ? '' : 'es'}${l.cleared ? `, ${l.cleared} reactivated` : ''}${l.issues ? `, ${l.issues} left out` : ''}`);
      });
  },
  clMSet: (id, el) => {
    const s = el.dataset.s, m = (C.mnrl?.matches || []).find(x => x.id === id); if (!m) return;
    const why = { Contacted: 'How was the client reached, and what did they say? Use a channel other than the listed number: the registered email, a letter or the branch.', "Not the client's number": 'What did the check find? For example: NSE still had an old number for the code (send the UCC again).', Reactivated: 'DoT reconnected the number to the same person: give the reference (the reactivation list, or DoT’s reply to the action taken report). Only the MNRL hold is lifted.', 'Number updated': 'Where was the number changed (CDAS, NSDL’s system), and when?' }[s];
    confirmModal(`${esc(s)} · ${esc(m.ucc)} ${esc(m.mobile)}`, esc(why), '<div class="fld"><label for="cl-mn">Note</label><input class="in" id="cl-mn"></div>', s === 'Contacted' ? 'Record' : 'Close the match',
      () => act(() => post(`/v1/mnrl/${encodeURIComponent(id)}/status`, { status: s, note: $('cl-mn').value }), x => `${x.id}: ${x.status}`));
  },
  clMHist: id => {
    const m = (C.mnrl?.matches || []).find(x => x.id === id); if (!m) return;
    modal(mhd(`${esc(m.id)} · ${esc(m.ucc)}`, `${esc(m.source)} list of ${fmtDate(m.list_date)} · ${esc(m.file || '')}`) + `<div class="bd"><ul class="cl-hist">${clHist(m.history)}</ul>${(m.seen || []).length ? `<div class="dim small">On ${m.seen.length} later list${m.seen.length === 1 ? '' : 's'} again: ${m.seen.map(esc).join(', ')}</div>` : ''}${(m.follow || []).length ? `<h3 class="cc-h">What follows</h3><ul>${m.follow.map(f => `<li>${esc(f)}</li>`).join('')}</ul>` : ''}</div><div class="ft"><button class="btn" data-act="close">Close</button></div>`);
  },
  clDLoad: () => {
    const d = C.deceased || {};
    confirmModal('Load the KRAs’ demise list', 'The file &lt;TMcode&gt;_Demise_Clients_by_KRA from ENIT. Debits of every client on it are blocked at once. Take the cut-off from the month’s NSE circular.',
      `<div class="form"><div class="fld"><label for="cl-dp">PANs reported up to the end of</label><input class="in" type="month" id="cl-dp" value="${esc(d.period || '')}"></div>
      <div class="fld"><label for="cl-dc">Close the UCCs before (cut-off)</label><input class="in" type="date" id="cl-dc" value="${esc(d.cutoff || '')}"></div>
      <div class="fld full"><label for="cl-dcirc">The month’s circular</label><input class="in" id="cl-dcirc" value="${esc(d.circular || '')}" placeholder="For example NSE/ISC/77375 of 9 Nov 2026"></div>
      <div class="fld full"><label for="cl-dfile">File</label><input class="in" type="file" id="cl-dfile"></div></div>`,
      'Load', async () => {
        const f = $('cl-dfile').files[0]; if (!f) throw new Error('Choose the file');
        await act(() => api('POST', `/v1/deceased/lists?name=${encodeURIComponent(f.name)}&date=${encodeURIComponent(S.date)}&period=${encodeURIComponent($('cl-dp').value)}&cutoff=${encodeURIComponent($('cl-dc').value)}&circular=${encodeURIComponent($('cl-dcirc').value)}`, f, true),
          l => `${l.rows} rows: ${l.matched} client${l.matched === 1 ? '' : 's'} matched, debits blocked; close before ${fmtDate(l.cutoff)}`);
      });
  },
  clDCut: () => {
    const d = C.deceased || {};
    confirmModal('Change the cut-off', 'For the open clients of the period, from the month’s NSE circular.',
      `<div class="form"><div class="fld"><label for="cl-cp">PANs reported up to the end of</label><input class="in" type="month" id="cl-cp" value="${esc(d.period || '')}"></div><div class="fld"><label for="cl-cc">Cut-off</label><input class="in" type="date" id="cl-cc" value="${esc(d.cutoff || '')}"></div><div class="fld full"><label for="cl-ccirc">Circular</label><input class="in" id="cl-ccirc" value="${esc(d.circular || '')}"></div></div>`,
      'Save', () => act(() => post('/v1/deceased/cutoff', { period: $('cl-cp').value, cutoff: $('cl-cc').value, circular: $('cl-ccirc').value }), r => `${r.cases} client${r.cases === 1 ? '' : 's'} updated`));
  },
  clDStep: (id, el) => {
    const a = el.dataset.a, x = (C.deceased?.cases || []).find(c => c.id === id); if (!x) return;
    const t = {
      verify: ['Death verified', 'Verified by the next working day: online, the original seen, or a validation report of an exchange’s or depository’s investor service centre. Update the KRA the same day.', 'How it was verified'],
      false: ['Intimation false', 'The investor is alive: give the due diligence (for example a video call) and the KRA modification sent today ("intimation of death of investor is false"). The block is lifted.', 'Due diligence and the KRA request'],
      close: ['Close the UCCs', `Every segment of ${esc(x.ucc)} is marked Closed; the exchanges’ UCC paths send it with the next upload (NSE as status C, BSE as Is Active C, MCX as status 2 with the remark). No closure letter goes to the client.`, ''],
      closed: [`Closed on ${esc(el.dataset.x || '')}`, 'Record the closure made on the exchange’s portal, with its reference.', 'How it was closed'],
      transmission: ['Transmission information sent', 'The transmission procedure, the request form and the list of documents, given to the nominee, legal heirs or surviving holders.', 'To whom and how']}[a];
    const body = a === 'close' ? '' : `<div class="form"><div class="fld"><label for="cl-so">On</label><input class="in" type="date" id="cl-so" value="${esc(S.date)}" max="${esc(S.date)}"></div>${a === 'closed' || a === 'false' ? '<div class="fld"><label for="cl-sr">Reference</label><input class="in" id="cl-sr"></div>' : ''}<div class="fld full"><label for="cl-sn">${esc(t[2])}</label><input class="in" id="cl-sn"></div></div>`;
    confirmModal(`${t[0]} · ${esc(x.ucc)}`, t[1], body, t[0], () => act(() => post(`/v1/deceased/${encodeURIComponent(id)}/steps`, { action: a, on: $('cl-so')?.value || '', note: $('cl-sn')?.value || '', ref: $('cl-sr')?.value || '', exchange: el.dataset.x || '' }), c => `${c.id}: ${c.status}`), a === 'false');
  },
  clDHist: id => {
    const x = (C.deceased?.cases || []).find(c => c.id === id); if (!x) return;
    modal(mhd(`${esc(x.id)} · ${esc(x.ucc)}`, `${esc(x.file || '')} · reported ${fmtDate(x.reported_on)}`) + `<div class="bd">${(x.held || []).length ? `<div class="note">Held until transmission: ${x.held.map(esc).join(', ')}.</div>` : ''}<ul class="cl-hist">${clHist(x.history)}</ul></div><div class="ft"><button class="btn" data-act="close">Close</button></div>`);
  },
});
