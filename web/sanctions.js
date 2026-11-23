'use strict';
/* Ledgerline console: sanctions screening.

   - KYC & PMLA → Sanctions: the UN Security Council, UAPA and WMD lists
     (and the debarment and defaulting-client lists) with the version of
     each, the last screening of all clients, the sanctions cases with their
     24-hour clock and report register, the settings, and FATF public
     statements with the enhanced due diligence they call for.

   Server: internal/ops/sanctions.go and sanctionsmatch.go;
   internal/api/sanctions.go. Names are prefixed sc. */

Object.assign(UI, { scF: 'open' });
const SC_OPEN = ['To verify', 'Clearing proposed', 'Confirmed'];
const scTime = s => s ? new Date(s).toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'short' }) : '';
const scLate = k => SC_OPEN.includes(k.status) && new Date(k.due_by) < new Date() && !(k.status === 'Confirmed' && (k.reports || []).every(r => r.reported_on));
const scDone = k => (k.reports || []).every(r => r.reported_on);
const scCase = id => (C.kyc && C.kyc.sanc && C.kyc.sanc.cases || []).find(k => k.id === id);

function scStatus(k) {
  const tone = { 'To verify': 'warn', 'Clearing proposed': 'warn', Confirmed: 'bad', Reported: 'bad', 'False positive': 'good', Unfrozen: 'nt' }[k.status] || 'nt';
  return pill(k.status, tone) + (k.held ? ' ' + pill('Client held', 'bad') : '') + (scLate(k) ? ' ' + pill('Past 24 hours', 'bad') : '');
}

function scActions(k) {
  const me = S.me?.actor, b = (a, l, cls) => `<button class="btn sm ${cls || ''}" data-act="scAct" data-id="${esc(k.id)}" data-a="${a}">${l}</button>`;
  const out = [`<button class="btn sm" data-act="scOpen" data-id="${esc(k.id)}">Open</button>`];
  if (k.status === 'To verify') out.push(b('confirm', 'Confirm match', 'bad'), b('clear', 'Not a match'));
  if (k.status === 'Clearing proposed') { if (k.clear_by !== me) out.push(b('approve_clear', 'Approve clearing', 'pri')); out.push(b('reject_clear', 'Reject')); }
  if (k.status === 'Confirmed' && scDone(k)) out.push(b('close', 'Close as reported', 'pri'));
  return out.join(' ');
}

function scTab(d) {
  const v = d.sanc;
  if (!v || v.error) return `<div class="bd">${errHTML({ message: (v && v.error) || 'Only the compliance officer sees sanctions cases.' })}</div>`;
  const c = v.counts || {}, n = k => c[k] || 0, lr = v.last_run, cfg = v.config;
  const lists = tbl([['List'], ['Version'], ['In force', 'r'], ['Loaded'], ['Added', 'r'], ['Delisted', 'r']], v.list_types.map(t => {
    const l = v.lists.find(x => x.list === t.id);
    return `<tr><td><b>${esc(t.name)}</b><div class="dim small wrap">${esc(t.about)}</div></td><td class="n">${l ? esc(l.version) : `<span class="dim">Not loaded</span>`}</td><td class="r">${nf(v.entries[t.id] || 0)}</td><td class="small">${l ? `${scTime(l.loaded_at)} · ${esc(l.by)}<div class="dim">${esc(l.file)} (${esc(l.format)})</div>` : ''}</td><td class="r">${l ? nf(l.added) : ''}</td><td class="r">${l ? nf(l.delisted) : ''}</td></tr>`;
  }));
  const shown = v.cases.filter(k => UI.scF === 'all' || (UI.scF === 'open' ? SC_OPEN.includes(k.status) : !SC_OPEN.includes(k.status)));
  const cases = tbl([['Case'], ['Person'], ['Matches'], ['Score'], ['Found · due'], ['Status'], ['']], shown.map(k => `<tr>
      <td class="mono">${esc(k.id)}</td>
      <td>${esc(k.person)} <span class="tag">${esc(k.role)}</span>${k.ucc ? `<div><button class="linkish mono" data-act="client" data-id="${esc(k.ucc)}">${esc(k.ucc)}</button> ${esc(k.client || '')}</div>` : '<div class="dim small">No account opened</div>'}</td>
      <td><b>${esc(k.listed_name)}</b><div class="dim small">${esc(k.list)} ${esc(k.ref || '')}${k.regime ? ' · ' + esc(k.regime) : ''}${k.statute ? ' · ' + esc(k.statute) : ''}</div></td>
      <td><b>${k.score}</b><div class="dim small wrap">${(k.why || []).map(esc).join('; ')}</div></td>
      <td class="small">${scTime(k.found)}<div class="dim">due ${scTime(k.due_by)}</div></td>
      <td>${scStatus(k)}</td><td>${scActions(k)}</td></tr>`), { tall: true, empty: UI.scF === 'open' ? 'No open sanctions case.' : 'No cases.' });
  const edd = v.edd.filter(e => e.status !== 'Done');
  return `<div class="pad"><div class="kpis">${kpi('Open cases', n('Open'), `${n('Held')} clients held`, n('Open') ? 'warn' : 'good')}${kpi('Past 24 hours', n('Past 24 hours'), 'verify and report within 24 hours of finding', n('Past 24 hours') ? 'bad' : 'good')}${kpi('Last screening of all clients', lr ? fmtDate(lr.at) : '—', lr ? `${nf(lr.clients)} clients, ${nf(lr.persons)} people · ${esc(lr.trigger)}` : 'load a list first', lr ? '' : 'warn')}${kpi('FATF EDD to do', n('EDD to do'), 'clients linked to listed jurisdictions', n('EDD to do') ? 'warn' : 'good')}</div>
    <div class="tools" style="margin:8px 0">${has('Compliance officer') ? `<button class="btn pri" data-act="scLoad">Load a list</button><button class="btn" data-act="scScreen">Screen all clients now</button><button class="btn" data-act="scFATF">Record a FATF statement</button><button class="btn" data-act="scConfig">Settings</button>` : ''}</div>
    <div class="note small">Every client, joint holder, guardian, controlling person${cfg.screen_nominees ? ' and nominee' : ''} is screened by PAN and by name (spellings, aliases, date of birth and nationality as supporting evidence): on each new application, on each list load, on each FATF statement and every ${cfg.every_days === 1 ? 'day' : cfg.every_days + ' days'}. A score of ${cfg.review_at} or more opens a case; ${cfg.hold_at} or more also holds the client at once. A confirmed match stops trading, payouts and demat debits, raises a demat freeze request and an STR alert, and must be reported within 24 hours.</div></div>
    <div class="pad" style="padding-bottom:0">${segBtns('scF', [['open', 'Open'], ['closed', 'Closed'], ['all', 'All']], 'kyc-t')}</div>${cases}
    ${edd.length ? `<div class="pad"><h3>Enhanced due diligence for FATF statements</h3></div>` + tbl([['Client'], ['Statement'], ['Level'], ['Links'], ['Due'], ['']], edd.map(e => `<tr><td><button class="linkish mono" data-act="client" data-id="${esc(e.ucc)}">${esc(e.ucc)}</button> ${esc(e.name)}</td><td class="n">${esc(e.statement)}</td><td>${pill(e.level, e.level === 'Call for action' ? 'bad' : 'warn')}</td><td class="small wrap">${e.links.map(esc).join('; ')}</td><td class="n">${fmtDate(e.due)}</td><td><button class="btn sm" data-act="scEDD" data-id="${esc(e.id)}">Record EDD</button></td></tr>`)) : ''}
    <div class="pad"><h3>Lists</h3></div>${lists}
    ${v.fatf.length ? `<div class="pad"><h3>FATF statements acted on</h3></div>` + tbl([['Plenary'], ['Date'], ['Reference'], ['Call for action'], ['Increased monitoring'], ['Clients linked', 'r']], v.fatf.map(f => `<tr><td>${esc(f.plenary)}</td><td class="n">${fmtDate(f.date)}</td><td class="small">${esc(f.ref || '')}</td><td class="small">${f.call_for_action.map(esc).join(', ') || '—'}</td><td class="small wrap">${f.monitoring.map(esc).join(', ') || '—'}</td><td class="r">${nf(f.linked)}</td></tr>`)) : ''}`;
}

function scCaseModal(k) {
  const can = has('Compliance officer') && (k.status === 'Confirmed' || k.status === 'Reported');
  const reg = (k.reports || []).length ? tbl([['Report to'], ['Address'], ['Reported on'], ['Reference'], ['']], k.reports.map(r => `<tr><td>${esc(r.recipient)}</td><td class="small wrap">${esc(r.address || '—')}</td><td class="n">${r.reported_on ? fmtDate(r.reported_on) : pill('Not yet', 'warn')}</td><td class="small wrap">${esc(r.ref || '')}${r.by ? `<div class="dim">${esc(r.by)}</div>` : ''}</td><td>${can && !r.reported_on ? `<button class="btn sm" data-act="scReport" data-id="${esc(k.id)}" data-r="${esc(r.recipient)}">Record</button>` : ''}</td></tr>`)) : '<div class="note small">The report register is made when the match is confirmed.</div>';
  const kv = (l, x) => `<div><small>${l}</small><b>${x || '—'}</b></div>`;
  modal(mhd('Sanctions case ' + esc(k.id), `${esc(k.person)} (${esc(k.role)}${k.ucc ? ' of ' + esc(k.ucc) : ''}) · ${esc(k.status)}`) + `<div class="bd">
    <div class="kv">${kv('Listed as', esc(k.listed_name))}${kv('List', esc(k.list + ' ' + (k.ref || '')) + (k.regime ? ' · ' + esc(k.regime) : ''))}${kv('Reported under', esc(k.statute === 'WMD' ? 'WMD Act s.12A' : k.statute === 'UAPA' ? 'UAPA s.51A' : 'Not a sanctions list'))}${kv('Score', k.score + ' · ' + (k.why || []).map(esc).join('; '))}${kv('Found', scTime(k.found) + ' · ' + esc(k.trigger))}${kv('Due by', scTime(k.due_by))}${kv('Demat freeze requests', (k.freezes || []).map(esc).join(', '))}${kv('STR alert', esc(k.alert || ''))}</div>
    <h3 style="margin-top:12px">Report register</h3>${reg}
    ${k.unfreeze_app_on ? `<div class="note small">Unfreezing application received ${fmtDate(k.unfreeze_app_on)}; forward by ${fmtDate(k.unfreeze_forward_by)}${k.unfreeze_forwarded_on ? `; forwarded ${fmtDate(k.unfreeze_forwarded_on)} (${esc(k.unfreeze_forwarded_ref)})` : ''}.</div>` : ''}
    ${(k.notes || []).length ? `<h3 style="margin-top:12px">Notes</h3><ul class="small">${k.notes.map(x => `<li>${esc(x)}</li>`).join('')}</ul>` : ''}
    </div><div class="ft"><button class="btn" data-act="close">Close</button>${can ? `<button class="btn" data-act="scAct" data-id="${esc(k.id)}" data-a="unfreeze_app">Unfreezing application</button>${k.unfreeze_app_on && !k.unfreeze_forwarded_on ? `<button class="btn" data-act="scAct" data-id="${esc(k.id)}" data-a="unfreeze_forwarded">Forwarded to the CNO</button>` : ''}<button class="btn" data-act="scAct" data-id="${esc(k.id)}" data-a="unfreeze">Unfreezing order</button>` : ''}</div>`);
}

// The form for each action: [title, what to say, fields].
const SC_FORMS = {
  confirm: ['Confirm the match', 'The client is blocked, payouts and demat debits stop, a demat freeze request goes to the DP desk, the report register is made and an STR alert is raised. Report within 24 hours of finding.', [['note', 'What was checked (documents compared with the list entry)', 'area']]],
  clear: ['Not a match', 'Another compliance officer approves before the case closes and any hold is lifted.', [['note', 'Why it is not the listed person (e.g. a different date of birth or father’s name on the documents)', 'area']]],
  approve_clear: ['Approve clearing', 'The case closes as a false positive and any hold on the client is lifted.', [['note', 'Note (optional)', 'area']]],
  reject_clear: ['Reject clearing', 'The case goes back to be verified.', [['note', 'Why it is not cleared', 'area']]],
  close: ['Close as reported', 'Every report is recorded and the STR decided. The client stays held and the account frozen until an unfreezing order.', [['note', 'Note', 'area']]],
  unfreeze_app: ['Unfreezing application received', 'Forward it with full details of the frozen assets to the Central Nodal Officer within two working days.', [['date', 'Received on', 'date']]],
  unfreeze_forwarded: ['Application forwarded', 'Record when and how it went to the Central Nodal Officer.', [['date', 'Forwarded on', 'date'], ['ref', 'Reference (letter or email)', 'text']]],
  unfreeze: ['Unfreezing order', 'Lifts the hold on the client and raises unfreeze requests for frozen demat accounts.', [['ref', 'Order (number and date)', 'text'], ['note', 'What it says', 'area']]],
};

function scForm(id, a, extra) {
  const f = SC_FORMS[a], k = scCase(id);
  const field = ([key, label, type]) => `<div class="fld"><label for="sc-${key}">${esc(label)}</label>${type === 'area' ? `<textarea class="in" id="sc-${key}"></textarea>` : `<input class="in" id="sc-${key}" type="${type}" ${type === 'date' ? `value="${esc(S.date)}"` : ''}>`}</div>`;
  confirmModal(esc(f[0]) + ' · ' + esc(id), esc(k ? `${k.person} · ${k.listed_name} (${k.list} ${k.ref || ''})` : ''), `<p class="small">${esc(f[1])}</p><div class="form">${f[2].map(field).join('')}</div>`, esc(f[0]),
    () => act(() => post(`/v1/sanctions/cases/${encodeURIComponent(id)}`, Object.assign({ action: a, note: $('sc-note')?.value || '', date: $('sc-date')?.value || '', ref: $('sc-ref')?.value || '' }, extra || {})), r => `${r.id}: ${r.status.toLowerCase()}`), a === 'confirm');
}

Object.assign(A, {
  scOpen: id => { const k = scCase(id); if (k) scCaseModal(k); },
  scAct: (id, el) => scForm(id, el.dataset.a),
  scReport: (id, el) => confirmModal('Record a report · ' + esc(id), esc(el.dataset.r), `<div class="form"><div class="fld"><label for="sc-date">Reported on</label><input class="in" type="date" id="sc-date" value="${esc(S.date)}" max="${esc(S.date)}"></div><div class="fld"><label for="sc-ref">Reference (email date and subject, letter number or acknowledgement)</label><input class="in" id="sc-ref"></div></div>`, 'Record',
    () => act(() => post(`/v1/sanctions/cases/${encodeURIComponent(id)}`, { action: 'report', recipient: el.dataset.r, date: $('sc-date').value, ref: $('sc-ref').value }), 'Report recorded')),
  scScreen: () => act(() => post('/v1/sanctions/screen'), r => `${r.clients} clients screened: ${r.new} new case${r.new === 1 ? '' : 's'}`),
  scLoad: () => {
    const v = C.kyc.sanc;
    confirmModal('Load a list', 'The UN Security Council’s consolidated list loads as the UN publishes it (XML). Other lists load as CSV with a Name column and optional PAN, Aliases, DOB, Nationality, Ref, Regime, ListedOn, Type. A UNSC, UAPA or WMD file replaces that list: entries not in it are marked delisted. All clients are screened again.',
      `<div class="form"><div class="fld"><label for="sc-l">List</label><select class="in" id="sc-l">${v.list_types.map(t => `<option value="${esc(t.id)}">${esc(t.name)}</option>`).join('')}</select></div><div class="fld"><label for="sc-v">Version (the list’s own date; the UN file carries it)</label><input class="in" id="sc-v" placeholder="e.g. 2026-11-20"></div><div class="fld"><label for="sc-f">File</label><input class="in" type="file" id="sc-f" accept=".xml,.csv,.txt"></div></div>`, 'Load and screen',
      async () => { const f = $('sc-f').files[0]; if (!f) throw new Error('Choose the file'); await act(() => api('POST', `/v1/sanctions/lists?list=${encodeURIComponent($('sc-l').value)}&version=${encodeURIComponent($('sc-v').value)}&name=${encodeURIComponent(f.name)}`, f, true), r => `${r.version.list} ${r.version.version}: ${r.version.entries} entries (${r.version.added} new, ${r.version.delisted} delisted); ${r.run.new} new case${r.run.new === 1 ? '' : 's'}`); });
  },
  scConfig: () => {
    const c = C.kyc.sanc.config, num = (k, l, x) => `<div class="fld"><label for="sc-${k}">${l}</label><input class="in" type="number" id="sc-${k}" value="${x}"></div>`;
    confirmModal('Sanctions screening settings', 'Scores run from 0 to 100. The thresholds are the firm’s choice: no circular sets them.',
      `<div class="form">${num('ra', 'Open a case at a score of', c.review_at)}${num('ha', 'Hold the client at once at', c.hold_at)}${num('ed', 'Screen all clients every (days)', c.every_days)}${num('ma', 'Self-inspection: screened within (days)', c.max_age_days)}<label class="check"><input type="checkbox" id="sc-nom" ${c.screen_nominees ? 'checked' : ''}> <span>Screen nominees too (a match is reported, the account is not held)</span></label>
        ${c.contacts.map((x, i) => `<div class="fld"><label for="sc-c${i}">${esc(x.recipient)}</label><input class="in" id="sc-c${i}" value="${esc(x.address)}"></div>`).join('')}</div>`, 'Save',
      () => act(() => api('PUT', '/v1/sanctions/config', Object.assign({}, c, { review_at: +$('sc-ra').value, hold_at: +$('sc-ha').value, every_days: +$('sc-ed').value, max_age_days: +$('sc-ma').value, screen_nominees: $('sc-nom').checked, contacts: c.contacts.map((x, i) => ({ recipient: x.recipient, address: $('sc-c' + i).value })) })), 'Settings saved'));
  },
  scFATF: () => confirmModal('Record a FATF public statement', 'After each FATF plenary (February, June, October) SEBI, the exchanges and the depositories circulate its statements. Every client linked to a listed jurisdiction (nationality, birth, tax residence, controlling persons) gets an EDD item, and all clients are screened again.',
    `<div class="form"><div class="fld"><label for="sc-p">Plenary</label><input class="in" id="sc-p" placeholder="October 2026"></div><div class="fld"><label for="sc-d">Date of the statement or communique</label><input class="in" type="date" id="sc-d" value="${esc(S.date)}"></div><div class="fld"><label for="sc-r">Reference</label><input class="in" id="sc-r" placeholder="SEBI / exchange / CDSL communique number"></div><div class="fld"><label for="sc-cfa">Call for action (ISO codes or names, comma separated)</label><input class="in" id="sc-cfa"></div><div class="fld"><label for="sc-mon">Increased monitoring</label><textarea class="in" id="sc-mon"></textarea></div></div>`, 'Record and screen',
    () => act(() => post('/v1/sanctions/fatf', { plenary: $('sc-p').value, date: $('sc-d').value, ref: $('sc-r').value, call_for_action: $('sc-cfa').value.split(',').map(x => x.trim()).filter(Boolean), monitoring: $('sc-mon').value.split(/[,\n]/).map(x => x.trim()).filter(Boolean) }), r => `${r.linked} of ${r.clients} clients linked: EDD items raised`)),
  scEDD: id => confirmModal('Record enhanced due diligence', esc(id), `<div class="fld"><label for="sc-en">What was reviewed and what it showed</label><textarea class="in" id="sc-en"></textarea></div><label class="check"><input type="checkbox" id="sc-eh"> <span>Make the client High risk (special category: high-risk jurisdiction)</span></label>`, 'Record',
    () => act(() => post(`/v1/sanctions/edd/${encodeURIComponent(id)}`, { note: $('sc-en').value, high_risk: $('sc-eh').checked }), 'EDD recorded')),
});
