'use strict';
/* Ledgerline console: Security → Cyber controls & data protection
   (V.cyber): restore drills, technology vendors' exit plans, the SBOM,
   the server's clock source, the half-yearly review of the Admins'
   activity, clients' fraud reports and data requests, closed accounts due
   for erasure, and the portal terms and privacy notice. In the client
   portal: My data & security (V.pprivacy) and the terms at first sign-in.
   On the sign-in screen: the unlock code; on Users: Unlock. On People:
   the insider trading code's records and staff controls.

   Server: internal/ops/resilience.go, authlock.go, dataprivacy.go,
   fraudreport.go, pit.go, staffctl.go; internal/api/cyber.go. Loaded
   after app.js, security.js and hrms.js. Names are prefixed cy. */

const cyDT = id => `<input class="in" type="datetime-local" id="${id}">`;

V.cyber = {
  load: async () => { const d = await get('/v1/security/cyber'); try { d.res = await get('/v1/security/resilience'); } catch (e) { d.res = null; } return d; },
  html: d => {
    const can = has('Compliance officer');
    const drill = d.drills[0];
    const crit = d.vendors.filter(v => v.critical), gaps = crit.filter(v => (v.gaps || []).length);
    const openFr = d.fraud_reports.filter(f => f.status === 'Open'), openDr = d.data_requests.filter(r => r.status === 'Open'), dueEr = d.erasure.filter(e => e.due);
    return ph('Cyber controls & data protection', 'The records SEBI’s cyber security framework (CSCRF), its cloud framework, CERT-In’s directions and the DPDP Act ask the firm to keep: restore drills, exit plans for technology vendors, the software bill of materials, the clock source, the review of the Admins’ activity, clients’ fraud reports and data requests, and erasure once records need not be kept.',
      `<button class="btn" data-act="dl" data-id="/v1/security/sbom" data-name="ledgerline-sbom.cdx.json">Ledgerline SBOM</button>`) +
      `<div class="kpis">${kpi('Last restore drill', drill ? fmtDate(drill.started.slice(0, 10)) : 'none', drill ? `RTO ${nf(drill.rto_min)} min · RPO ${nf(drill.rpo_min)} min` : 'twice a year', d.drill_due ? 'bad' : 'good')}
        ${kpi('Critical vendors', nf(crit.length), gaps.length ? `${gaps.length} exit plan(s) incomplete` : 'exit plans complete', gaps.length ? 'warn' : 'good')}
        ${kpi('Clock source', esc(d.ntp.state), esc(d.ntp.from || ''), d.ntp.state === 'NIC/NPL' ? 'good' : 'warn')}
        ${kpi('Fraud reports open', nf(openFr.length), 'from clients', openFr.length ? 'warn' : 'good')}
        ${kpi('Data requests open', nf(openDr.length), 'answer within 90 days', openDr.some(r => r.due < S.date) ? 'bad' : openDr.length ? 'warn' : 'good')}
        ${kpi('Due for erasure', nf(dueEr.length), 'closed, retention over', dueEr.length ? 'warn' : 'good')}</div>
      <div class="note small">Sign-in: ${d.sign_in.out_of_band_unlock ? 'a locked sign-in needs an out-of-band unlock' : '<b>the timed lock is on</b> (CSCRF asks for an out-of-band unlock)'}, ${nf(d.sign_in.lock_after)} wrong tries lock it, ${esc(d.sign_in.rate_limit)}; passwords ${d.sign_in.password_days ? `last ${nf(d.sign_in.password_days)} days` : 'never expire'}, the last ${nf(d.sign_in.password_history)} cannot be reused. Change these on <button class="linkish" data-act="go" data-id="incidents">Incidents</button> → Rules and contacts.${d.retention_warn ? ' ' + esc(d.retention_warn) : ''}</div>
      <div class="note small">${esc(d.ntp.line)}</div><div class="note small ${d.glitch.over && d.glitch.ibt !== 'no' ? 'warn' : ''}"><b>Technical glitch framework.</b> ${esc(d.glitch.line)}</div>
      <div class="panel"><div class="hd">${segBtns('cyTab', [['drills', 'Restore drills'], ['vendors', 'Vendors & exit plans'], ['review', 'Admin activity review'], ['fraud', 'Fraud reports'], ['data', 'Data requests & erasure'], ['terms', 'Portal terms & notice'], ['recovery', 'Recovery point'], ['capacity', 'Capacity'], ['evidence', 'Ledgerline evidence'], ['access', 'Access & inspection']])}</div>${cyTab(d, can)}</div>`;
  }
};
UI.cyTab = UI.cyTab || 'drills';

function cyTab(d, can) {
  switch (UI.cyTab) {
    case 'recovery': case 'capacity': case 'evidence': case 'access': return cyResTab(d, can); // resilience.js
    case 'vendors': return `<div class="bd">${can ? '<button class="btn pri" data-act="cyVendor" data-id="">Add a vendor</button>' : ''}</div>` + tbl([['Vendor'], ['Service'], ['Exit plan'], ['Reviewed'], ['']], d.vendors.map(v => `<tr><td><b>${esc(v.name)}</b>${v.critical ? ' ' + pill('Critical', 'acc') : ''}</td><td>${esc(v.service)}</td>
      <td class="wrap small">${(v.gaps || []).length ? v.gaps.map(g => `<div class="neg">${esc(g)}</div>`).join('') : pill('Complete', 'good')}${v.escrow ? '<div class="dim">source code in escrow</div>' : ''}</td><td class="n">${v.reviewed_on ? fmtDate(v.reviewed_on) : '—'}</td>
      <td>${can ? `<button class="btn sm" data-act="cyVendor" data-id="${esc(v.id)}">Edit</button>` : ''}</td></tr>`), { empty: 'No vendors.' });
    case 'review': {
      const r = d.review;
      return `<div class="bd"><p class="small">Every half year, someone other than each Admin looks through that Admin’s actions (CSCRF PR.AA.S11).</p>${can ? '<button class="btn pri" data-act="cyPrivBuild">Build the review of the last half year</button>' : ''}</div>` +
        (r ? tbl([['Admin'], ['Actions', 'r'], ['Sensitive actions'], ['Signed off'], ['']], r.admins.map(a => `<tr><td class="mono">${esc(a.user)}</td><td class="r">${nf(a.actions)}</td><td class="wrap small">${(a.sensitive || []).slice(0, 8).map(esc).join('<br>') || '—'}${(a.sensitive || []).length > 8 ? `<div class="dim">and ${a.sensitive.length - 8} more</div>` : ''}</td>
          <td>${a.reviewed_by ? `${esc(a.reviewed_by)}<div class="dim small wrap">${esc(a.note)}</div>` : pill('Open', 'warn')}</td><td>${can && !a.reviewed_by && a.user !== S.me.actor ? `<button class="btn sm" data-act="cyPrivSign" data-id="${esc(r.id)}|${esc(a.user)}">Sign off</button>` : ''}</td></tr>`), { empty: 'No Admins.' }) : '<div class="bd dim">Not built yet.</div>');
    }
    case 'fraud': return tbl([['Report'], ['Client'], ['What'], ['Received'], ['Incident'], ['State'], ['']], d.fraud_reports.map(f => `<tr><td class="mono">${esc(f.id)}</td><td>${esc(f.ucc)}</td><td class="wrap"><b>${esc(f.kind)}</b><div class="small">${esc(f.detail)}</div>${f.block ? '<div class="small neg">The client asked to block the account</div>' : ''}</td>
      <td class="n">${esc(fmtTS(f.at))}</td><td>${f.incident ? `<button class="linkish mono" data-act="go" data-id="incidents">${esc(f.incident)}</button>` : ''}</td><td>${f.status === 'Open' ? pill('Open', 'warn') : pill('Closed', 'good') + `<div class="dim small wrap">${esc(f.outcome)}</div>`}</td>
      <td>${can && f.status === 'Open' ? `<button class="btn sm" data-act="cyFraudClose" data-id="${esc(f.id)}">Close…</button>` : ''}</td></tr>`), { tall: true, empty: 'No fraud reports.' });
    case 'data': return '<div class="bd"><h4 style="margin:0">Data requests (DPDP Act s.11-14)</h4></div>' + tbl([['Request'], ['Client'], ['What'], ['Received'], ['Answer by'], ['State'], ['']], d.data_requests.map(r => `<tr><td class="mono">${esc(r.id)}</td><td>${esc(r.ucc)}</td><td class="wrap"><b>${esc(r.kind)}</b><div class="small">${esc(r.detail)}</div></td><td class="n">${esc(fmtTS(r.at))}</td><td class="n ${r.status === 'Open' && r.due < S.date ? 'neg' : ''}">${fmtDate(r.due)}</td>
        <td>${r.status === 'Open' ? pill('Open', 'warn') : pill(r.status, r.status === 'Done' ? 'good' : 'nt') + `<div class="dim small wrap">${esc(r.answer)}</div>`}</td><td>${can && r.status === 'Open' ? `<button class="btn sm" data-act="cyAnswer" data-id="${esc(r.id)}">Answer…</button>` : ''}</td></tr>`), { empty: 'No requests.' }) +
      '<div class="bd"><h4 style="margin:0">Closed accounts and their retention</h4><p class="dim small">A closed client’s personal details can be erased once the firm’s record retention is over (the archive’s years). A client tagged LEGAL-HOLD, or under a sanctions case, is kept.</p></div>' +
      tbl([['Client'], ['Closed'], ['Kept until'], ['State'], ['']], d.erasure.map(e => `<tr><td>${esc(e.name)} · <span class="mono">${esc(e.ucc)}</span></td><td class="n">${fmtDate(e.closed_on)}</td><td class="n">${fmtDate(e.keep_until)}</td>
        <td>${e.erased_on ? pill('Erased ' + fmtDate(e.erased_on), 'nt') : e.hold ? pill('Legal hold', 'acc') : e.due ? pill('Due', 'warn') : pill('Kept', 'good')}</td><td>${can && e.due ? `<button class="btn sm bad" data-act="cyErase" data-id="${esc(e.ucc)}">Erase…</button>` : ''}</td></tr>`), { empty: 'No closed accounts.' });
    case 'terms': {
      const p = d.privacy;
      return `<div class="bd"><p class="small">Clients accept these at their first sign-in to the portal or the app, and again after each change (CSCRF PR.AT.S3; DPDP Act s.5). Terms version ${nf(p.terms_version)}, notice version ${nf(p.notice_version)}.</p>
        <div class="fld"><label for="cy-terms">Portal terms</label><textarea class="in" id="cy-terms" rows="8" ${can ? '' : 'disabled'}>${esc(p.terms)}</textarea></div>
        <div class="fld"><label for="cy-notice">Privacy notice</label><textarea class="in" id="cy-notice" rows="12" ${can ? '' : 'disabled'}>${esc(p.notice)}</textarea></div>
        <div class="fld"><label for="cy-off">Contact for data requests and grievances (e-mail)</label><input class="in" id="cy-off" value="${esc(p.officer || '')}" ${can ? '' : 'disabled'}></div>
        ${can ? '<button class="btn pri" data-act="cyTermsSave">Save (clients accept again)</button>' : ''}</div>`;
    }
  }
  return `<div class="bd"><p class="small">Restore a backup into a test environment at least twice a year and record it here (SEBI cloud framework s.6.2.12). For critical systems the target is back within 2 hours with no more than 15 minutes of data lost. Include a ransomware scenario restoring from an isolated or immutable copy.</p>${has('Admin', 'Compliance officer') ? '<button class="btn pri" data-act="cyDrill">Record a drill</button>' : ''}</div>` +
    tbl([['Drill'], ['What'], ['From'], ['When'], ['RTO', 'r'], ['RPO', 'r'], ['Result']], d.drills.map(x => `<tr><td class="mono">${esc(x.id)}<div class="dim small">${esc(x.kind)}</div></td><td>${esc(x.system)}</td><td class="small">${esc(x.from)}${x.isolated ? '<div class="dim">isolated copy</div>' : ''}</td>
      <td class="n">${esc(fmtTS(x.started))}</td><td class="r">${nf(x.rto_min)} min</td><td class="r">${nf(x.rpo_min)} min</td><td>${pill(x.passed ? 'Within targets' : 'Missed targets', x.passed ? 'good' : 'bad')}${x.note ? `<div class="dim small wrap">${esc(x.note)}</div>` : ''}</td></tr>`), { empty: 'No drills recorded.' });
}

/* ---------- the client portal: My data & security ---------- */
V.pprivacy = {
  load: async () => { const v = await portalLoad(); const [m, f] = await Promise.all([get(`/v1/portal/my-data?ucc=${encodeURIComponent(v.account.ucc)}`), get('/v1/portal/fraud')]); v.my = m; v.fraud = f; return v; },
  html: v => {
    const m = v.my, n = m.nominee;
    return pHead(v, 'My data & security', 'What we hold about you, whom it goes to and why; your requests; and how to tell us at once about a transaction you did not make.') +
      `<div class="panel"><div class="hd"><h3>Report a fraudulent transaction</h3></div><div class="bd"><p class="small">A trade, payout, share transfer, pledge, sign-in or change you did not make? Tell us now: the firm is told at once. If money left your bank account by a cyber fraud, also call 1930 or report it on cybercrime.gov.in.</p><button class="btn bad" data-act="cyFraud">Report a fraudulent transaction</button>
        ${v.fraud.length ? tbl([['Report'], ['What'], ['When'], ['State']], v.fraud.map(f => `<tr><td class="mono">${esc(f.id)}</td><td>${esc(f.kind)}</td><td class="n">${esc(fmtTS(f.at))}</td><td>${f.status === 'Open' ? pill('Being looked at', 'warn') : pill('Closed', 'good') + `<div class="dim small wrap">${esc(f.outcome)}</div>`}</td></tr>`)) : ''}</div></div>
      <div class="panel"><div class="hd"><h3>What we hold</h3><button class="btn sm" data-act="cyReq" data-id="Summary">Ask for a full summary</button></div>${tbl([['Data'], ['On record'], ['Why']], m.items.map(i => `<tr><td><b>${esc(i.what)}</b></td><td class="mono small">${esc(i.value)}</td><td class="small wrap">${esc(i.purpose)}</td></tr>`))}</div>
      <div class="panel"><div class="hd"><h3>Whom it goes to</h3></div>${tbl([['Who'], ['Role'], ['What']], m.recipients.map(r => `<tr><td>${esc(r.name)}</td><td>${esc(r.role === 'Processor' ? 'Works for us' : 'Has its own duty')}</td><td class="small wrap">${esc(r.what)}</td></tr>`))}</div>
      <div class="panel"><div class="hd"><h3>Your consents and requests</h3></div><div class="bd">${m.consents.map(c => `<div class="small">${esc(c)}</div>`).join('') || '<div class="dim small">No optional consents.</div>'}
        <div class="tools" style="margin-top:8px"><button class="btn" data-act="cyReq" data-id="Correction">Correct my data</button><button class="btn" data-act="cyReq" data-id="Withdraw consent">Withdraw a consent</button><button class="btn" data-act="cyReq" data-id="Erasure">Erase my data</button><button class="btn" data-act="cyReq" data-id="Grievance">Complain</button></div>
        <p class="dim small">We answer within 90 days. Records the law needs us to keep (SEBI’s and the PMLA’s periods) are kept for that time. Contact: ${esc(m.officer || 'our compliance officer')}.</p>
        ${m.requests.length ? tbl([['Request'], ['What'], ['State']], m.requests.map(r => `<tr><td class="mono">${esc(r.id)}</td><td>${esc(r.kind)}<div class="dim small">${esc(r.detail)}</div></td><td>${pill(r.status, r.status === 'Open' ? 'warn' : 'good')}${r.answer ? `<div class="small wrap">${esc(r.answer)}</div>` : ''}</td></tr>`)) : ''}</div></div>
      <div class="panel"><div class="hd"><h3>Nominee for your data rights</h3></div><div class="bd"><p class="small">Someone who may use these rights for you on death or incapacity (DPDP Act s.14). This is not your account’s nominee for securities.</p>
        ${n ? `<p><b>${esc(n.name)}</b> · ${esc(n.relation)} · ${esc(n.email || n.mobile || '')}</p>` : '<p class="dim small">None named.</p>'}<button class="btn" data-act="cyNominee">${n ? 'Change' : 'Name a person'}</button></div></div>`;
  }
};

/* ---------- the terms at first sign-in ---------- */
let cyTermsOpen = false;
async function termsModal() {
  if (cyTermsOpen) return;
  cyTermsOpen = true;
  let t;
  try { t = await get('/v1/portal/terms'); } catch (e) { cyTermsOpen = false; return; }
  setTimeout(() => { modalLocked = true; });
  modal(mhd('Terms of use and privacy notice', 'Please read both and accept to use the portal.').replace(/<button class="x"[^>]*>×<\/button>/, '') + `<div class="bd">
    <div class="wrap small" style="white-space:pre-wrap;max-height:30vh;overflow:auto;border:1px solid var(--line);padding:8px;border-radius:6px">${esc(t.terms)}</div>
    <div class="wrap small" style="white-space:pre-wrap;max-height:30vh;overflow:auto;border:1px solid var(--line);padding:8px;border-radius:6px;margin-top:8px">${esc(t.notice)}</div>
    <label class="check" style="margin-top:8px"><input type="checkbox" id="tm-ok"> I have read the terms and the privacy notice and accept them</label><div id="tm-err"></div></div>
    <div class="ft"><button class="btn" data-act="signOut">Sign out</button><button class="btn pri" id="tm-go">Accept and continue</button></div>`);
  $('tm-go').onclick = async () => {
    if (!$('tm-ok').checked) { $('tm-err').innerHTML = errHTML({ message: 'Tick the box to accept.' }); return; }
    try { await post('/v1/portal/terms', { terms_version: t.terms_version, notice_version: t.notice_version }); modalLocked = false; cyTermsOpen = false; closeModal(); render(); }
    catch (e) { $('tm-err').innerHTML = errHTML(e); }
  };
}

/* ---------- People: the insider trading code and staff controls ---------- */
// Called by hrms.js's own-trading panel and the staff controls panel.
function cyPITPanel(v) {
  const co = has('Compliance officer'), ba = v.policy?.board_approval;
  return `<div class="panel"><div class="hd"><h3>Insider trading code</h3></div><div class="bd small">
    <div>${ba ? `Designated-person criteria approved by the board on ${fmtDate(ba.date)} (${esc(ba.resolution)}).` : '<span class="neg">The board’s approval of the designated-person criteria is not recorded.</span>'} ${co ? '<button class="linkish" data-act="cyBoard">Record the approval…</button>' : ''}</div>
    ${(v.designated_without_basis || []).length ? `<div class="neg">Designated without a ground: ${v.designated_without_basis.map(esc).join(', ')}</div>` : ''}
    ${(v.missing_disclosures || []).length ? `<div class="warn">No disclosure of relatives this year: ${v.missing_disclosures.map(esc).join(', ')}</div>` : ''}
    ${(v.pre_clearances_without_report || []).length ? `<div class="warn">Pre-clearances run out with no report: ${v.pre_clearances_without_report.map(esc).join(', ')}</div>` : ''}
    <div class="tools" style="margin-top:6px"><button class="btn sm" data-act="cyDiscl">My yearly disclosure</button>${co ? '<button class="btn sm" data-act="cyDesig">Designate a person…</button><button class="btn sm" data-act="cyUPSI">Record UPSI shared…</button>' : ''}</div>
    ${(v.upsi_register || []).length ? tbl([['Entry'], ['Company'], ['Shared with'], ['On'], ['Notice']], v.upsi_register.map(u => `<tr><td class="mono">${esc(u.id)}</td><td>${esc(u.subject)}<div class="dim">${esc(u.nature)}</div></td><td>${esc(u.shared_with)} ${esc(u.org || '')}<div class="dim mono">${esc(u.pan || u.other_id)}</div></td><td class="n">${fmtDate(u.shared_on)}</td><td>${u.notice ? pill('Given', 'good') : pill('Not given', 'bad')}</td></tr>`)) : ''}</div></div>`;
}

Object.assign(A, {
  unlockAsk: () => confirmModal('Unlock my sign-in', 'We send a one-time code to the e-mail the firm holds for you.', `${CFG.tenant ? '' : `<div class="fld"><label for="ul-t">Firm code</label><input class="in" id="ul-t" value="${esc(S.tenant || '')}"></div>`}<div class="fld"><label for="ul-u">User ID</label><input class="in" id="ul-u" value="${esc($('li-user')?.value || '')}"></div><div class="fld"><label for="ul-c">Code (after you get it)</label><input class="in mono" id="ul-c" inputmode="numeric" maxlength="6"></div><div id="ul-msg" class="small"></div>`, 'Send code / unlock', async () => {
    const tenant = CFG.tenant || $('ul-t').value.trim().toLowerCase(), user = $('ul-u').value.trim(), code = $('ul-c').value.trim();
    if (!code) { const r = await api('POST', '/v1/auth/unlock-code', { tenant, user }); $('ul-msg').textContent = r.message; $('cm-ok').disabled = false; return; }
    await api('POST', '/v1/auth/unlock', { tenant, user, code }); closeModal(); toast('Unlocked: sign in now');
  }),
  unlockUser: id => confirmModal('Unlock ' + id, 'Confirm it is the person first, out of band: call their registered mobile or see them in person.', '<div class="fld"><label for="uu-h">How you confirmed it is them</label><input class="in" id="uu-h"></div>', 'Unlock', () => act(() => post(`/v1/users/${encodeURIComponent(id)}/unlock`, { how: $('uu-h').value.trim() }), 'Unlocked')),
  cyDrill: () => confirmModal('Record a restore drill', 'When it started, when the system was back, and the newest data in the restored copy.', `<div class="grid g2">
      <div class="fld"><label for="dr-k">Kind</label><select class="in" id="dr-k">${C.cyber.drill_kinds.map(k => `<option>${esc(k)}</option>`).join('')}</select></div>
      <div class="fld"><label for="dr-s">What was restored</label><input class="in" id="dr-s" value="Ledgerline"></div>
      <div class="fld"><label for="dr-f">From which copy</label><input class="in" id="dr-f" placeholder="Off-site backup, immutable copy…"></div>
      <div class="fld"><label class="check"><input type="checkbox" id="dr-i"> An isolated or immutable copy</label></div>
      <div class="fld"><label for="dr-st">Started (IST)</label>${cyDT('dr-st')}</div><div class="fld"><label for="dr-rs">Back (IST)</label>${cyDT('dr-rs')}</div>
      <div class="fld"><label for="dr-up">Newest data in the copy (IST)</label>${cyDT('dr-up')}</div><div class="fld"><label for="dr-fl">Simulated failure (IST, optional)</label>${cyDT('dr-fl')}</div></div>
      <div class="fld"><label for="dr-n">Note</label><input class="in" id="dr-n"></div>`, 'Record',
    () => act(() => post('/v1/security/drills', { kind: $('dr-k').value, system: $('dr-s').value, from: $('dr-f').value, isolated: $('dr-i').checked, started: $('dr-st').value, restored: $('dr-rs').value, data_up_to: $('dr-up').value, disrupted: $('dr-fl').value, note: $('dr-n').value }), 'Drill recorded')),
  cyVendor: id => {
    const v = C.cyber.vendors.find(x => x.id === id) || { service: 'Other', critical: true };
    const t = (k, l) => `<div class="fld"><label for="tv-${k}">${l}</label><textarea class="in" id="tv-${k}" rows="2">${esc(v[k] || '')}</textarea></div>`;
    const c = (k, l) => `<label class="check"><input type="checkbox" id="tv-${k}" ${v[k] ? 'checked' : ''}> ${l}</label>`;
    confirmModal(id ? 'Exit plan · ' + v.name : 'Add a technology vendor', 'Lock-in, concentration and the plan to switch (SEBI cloud framework s.9; stock brokers’ master circular para 61.2-61.3).', `<div class="grid g2">
        <div class="fld"><label for="tv-name">Vendor</label><input class="in" id="tv-name" value="${esc(v.name || '')}"></div>
        <div class="fld"><label for="tv-service">Service</label><select class="in" id="tv-service">${C.cyber.services.map(s => `<option ${s === v.service ? 'selected' : ''}>${esc(s)}</option>`).join('')}</select></div></div>
      ${c('critical', 'A critical system')}${t('lock_in', 'What makes leaving hard (lock-in)')}${t('concentration', 'What else depends on the same provider (concentration)')}${t('alternative', 'Who could take over')}${t('data_out', 'How the data comes out, in what format')}${t('steps', 'The exit steps')}
      <div class="grid g2"><div class="fld"><label for="tv-notice_days">Notice period (days)</label><input class="in" id="tv-notice_days" value="${esc(v.notice_days || '')}"></div><div class="fld"><label for="tv-reviewed_on">Reviewed on</label><input class="in" type="date" id="tv-reviewed_on" value="${esc(v.reviewed_on || '')}"></div></div>
      ${c('escrow', 'Source code in escrow')}${c('docs_clause', 'Contract: documentation')}${c('training_clause', 'Contract: training')}${c('penalties_clause', 'Contract: penalties')}${c('audit_clause', 'Contract: cooperation with audits')}
      <div class="fld"><label for="tv-approved_by">Approved by (board or proprietor, with date)</label><input class="in" id="tv-approved_by" value="${esc(v.approved_by || '')}"></div>`, 'Save',
      () => act(() => post('/v1/security/vendors', { id: id || '', name: $('tv-name').value, service: $('tv-service').value, critical: $('tv-critical').checked, lock_in: $('tv-lock_in').value, concentration: $('tv-concentration').value, alternative: $('tv-alternative').value, data_out: $('tv-data_out').value, steps: $('tv-steps').value,
        notice_days: parseInt($('tv-notice_days').value, 10) || 0, reviewed_on: $('tv-reviewed_on').value, escrow: $('tv-escrow').checked, docs_clause: $('tv-docs_clause').checked, training_clause: $('tv-training_clause').checked, penalties_clause: $('tv-penalties_clause').checked, audit_clause: $('tv-audit_clause').checked, approved_by: $('tv-approved_by').value }), 'Saved'));
  },
  cyClientData: ucc => confirmModal('Consent & KYC source · ' + ucc, 'The signed form’s consent to collect bank details, photograph and signature (IT Rules 2011, rule 5), and where the KYC record came from. A record from the CKYC registry may be used only to verify identity or address (PML Rules, rule 9(1-F)).',
    `<div class="grid g2"><div class="fld"><label for="cc-s">Consent from</label><select class="in" id="cc-s"><option value="">(no change)</option><option>Signed form</option><option>e-Sign</option><option>Migrated</option></select></div><div class="fld"><label for="cc-r">Form number or reference</label><input class="in" id="cc-r"></div>
     <div class="fld"><label for="cc-k">KYC record came from</label><select class="in" id="cc-k"><option value="-">(no change)</option><option value="">Not known</option><option>Own (in person or online)</option><option>KRA</option><option>CKYC</option><option>DigiLocker</option></select></div></div>`, 'Save',
    async () => { if ($('cc-s').value) await post(`/v1/clients/${encodeURIComponent(ucc)}/data-consent`, { source: $('cc-s').value, ref: $('cc-r').value.trim() }); if ($('cc-k').value !== '-') await post(`/v1/clients/${encodeURIComponent(ucc)}/kyc-source`, { source: $('cc-k').value }); closeModal(); toast('Saved'); }),
  cyPrivBuild: () => act(() => post('/v1/security/privileged-review', { date: S.date }), r => `Review ${r.id} built: ${r.admins.length} Admin(s)`),
  cyPrivSign: id => { const [rid, user] = id.split('|'); confirmModal('Sign off ' + user, 'Say what you looked at and found.', '<div class="fld"><label for="pr-n">Note</label><input class="in" id="pr-n"></div>', 'Sign off', () => act(() => post(`/v1/security/privileged-review/${encodeURIComponent(rid)}/${encodeURIComponent(user)}`, { note: $('pr-n').value.trim() }), 'Signed off')); },
  cyFraudClose: id => confirmModal('Close ' + id, 'What was found and done. The client gets it by e-mail.', '<div class="fld"><label for="fc-o">Outcome</label><textarea class="in" id="fc-o" rows="3"></textarea></div>', 'Close', () => act(() => post(`/v1/fraud-reports/${encodeURIComponent(id)}/close`, { outcome: $('fc-o').value.trim() }), 'Closed')),
  cyAnswer: id => confirmModal('Answer ' + id, 'The client gets the answer by e-mail.', '<div class="fld"><label for="da-a">Answer</label><textarea class="in" id="da-a" rows="3"></textarea></div><label class="check"><input type="checkbox" id="da-r"> Refuse (for example: the law needs the records kept)</label>', 'Send', () => act(() => post(`/v1/privacy/requests/${encodeURIComponent(id)}`, { done: !$('da-r').checked, answer: $('da-a').value.trim() }), 'Answered')),
  cyErase: ucc => confirmModal('Erase ' + ucc, 'The name, PAN, contacts, bank and demat numbers, nominees and document names are erased. The ledger and trades stay, without the person. This cannot be undone.', '<div class="fld"><label for="er-r">Why</label><input class="in" id="er-r" value="Retention over"></div>', 'Erase', () => act(() => post(`/v1/privacy/erase/${encodeURIComponent(ucc)}`, { reason: $('er-r').value.trim() }), 'Erased'), true),
  cyTermsSave: () => act(() => api('PUT', '/v1/privacy/settings', { terms: $('cy-terms').value, notice: $('cy-notice').value, officer: $('cy-off').value.trim() }), r => `Saved: terms v${r.terms_version}, notice v${r.notice_version}`),
  cyFraud: () => confirmModal('Report a fraudulent transaction', 'The firm is told at once. You get a reference by e-mail.', `<div class="fld"><label for="fr-k">What happened</label><select class="in" id="fr-k">${['Trade I did not place', 'Payout I did not ask for', 'Shares moved out of my demat', 'Pledge I did not make', 'Sign-in I did not make', 'Change of my details I did not ask for', 'Other'].map(k => `<option>${k}</option>`).join('')}</select></div>
      <div class="grid g2"><div class="fld"><label for="fr-w">When</label><input class="in" id="fr-w" placeholder="date and time"></div><div class="fld"><label for="fr-a">Amount (₹, if any)</label><input class="in mono" id="fr-a" inputmode="decimal"></div></div>
      <div class="fld"><label for="fr-r">Contract note, order or payout number (if any)</label><input class="in" id="fr-r"></div><div class="fld"><label for="fr-d">What you saw</label><textarea class="in" id="fr-d" rows="3"></textarea></div>
      <label class="check"><input type="checkbox" id="fr-b"> Block my account from trading and payouts until you have checked</label>`, 'Report now',
    () => act(() => post('/v1/portal/fraud', { ucc: UI.pAcct, kind: $('fr-k').value, when: $('fr-w').value, amount: $('fr-a').value, ref: $('fr-r').value, detail: $('fr-d').value, block: $('fr-b').checked }), r => `Reported: ${r.id}. The firm has been told.`), true),
  cyReq: kind => confirmModal({ Summary: 'Ask for a full summary', Correction: 'Correct my data', 'Withdraw consent': 'Withdraw a consent', Erasure: 'Erase my data', Grievance: 'Complain' }[kind] || kind, 'We answer within 90 days.', `<div class="fld"><label for="dq-d">${kind === 'Withdraw consent' ? 'Which consent (for example: WhatsApp messages)' : 'Details'}</label><textarea class="in" id="dq-d" rows="3"></textarea></div>`, 'Send',
    () => act(() => post('/v1/portal/data-requests', { ucc: UI.pAcct, kind, detail: $('dq-d').value }), r => r.status === 'Done' ? r.answer : `Request ${r.id} sent`)),
  cyNominee: () => confirmModal('Nominee for your data rights', 'Leave the name empty to remove the nominee.', '<div class="fld"><label for="dn-n">Name</label><input class="in" id="dn-n"></div><div class="fld"><label for="dn-r">Relation</label><input class="in" id="dn-r"></div><div class="grid g2"><div class="fld"><label for="dn-e">E-mail</label><input class="in" id="dn-e"></div><div class="fld"><label for="dn-m">Mobile</label><input class="in" id="dn-m"></div></div>', 'Save',
    () => act(() => post('/v1/portal/data-nominee', { ucc: UI.pAcct, name: $('dn-n').value, relation: $('dn-r').value, email: $('dn-e').value, mobile: $('dn-m').value }), 'Saved')),
  cyBoard: () => confirmModal('Board approval of designated persons', 'The criteria the board set with the compliance officer (reg. 9(4)): roles, access to UPSI and seniority.', '<div class="fld"><label for="bd-c">Criteria</label><textarea class="in" id="bd-c" rows="4"></textarea></div><div class="grid g2"><div class="fld"><label for="bd-d">Board meeting</label><input class="in" type="date" id="bd-d"></div><div class="fld"><label for="bd-r">Resolution or minute reference</label><input class="in" id="bd-r"></div></div>', 'Record',
    () => act(() => post('/v1/hr/own-trading/board-approval', { criteria: $('bd-c').value, date: $('bd-d').value, resolution: $('bd-r').value }), 'Recorded')),
  cyDesig: () => confirmModal('Designate a person', 'Under the board’s criteria; the ground is kept with the person.', '<div class="fld"><label for="ds-c">Employee code</label><input class="in" id="ds-c"></div><div class="fld"><label for="ds-g">Ground</label><select class="in" id="ds-g">' + ['Role or function', 'Access to UPSI', 'Seniority (CEO and up to two levels below)', 'Support staff with UPSI access', 'Promoter'].map(g => `<option>${g}</option>`).join('') + '</select></div><label class="check"><input type="checkbox" id="ds-off"> Remove the designation</label>', 'Save',
    () => act(() => post(`/v1/hr/staff/${encodeURIComponent($('ds-c').value.trim().toUpperCase())}/designated`, { on: !$('ds-off').checked, ground: $('ds-g').value }), 'Saved')),
  cyUPSI: () => confirmModal('Record UPSI shared', 'Who was given unpublished price sensitive information, why, and whether they were told to keep it confidential (reg. 3(5), 9A). Entries are never changed.', `<div class="grid g2"><div class="fld"><label for="up-s">Company or security</label><input class="in" id="up-s"></div><div class="fld"><label for="up-n">What the information is</label><input class="in" id="up-n"></div>
      <div class="fld"><label for="up-w">Shared with</label><input class="in" id="up-w"></div><div class="fld"><label for="up-o">Organisation</label><input class="in" id="up-o"></div>
      <div class="fld"><label for="up-p">PAN</label><input class="in mono" id="up-p"></div><div class="fld"><label for="up-i">Other identifier (no PAN)</label><input class="in" id="up-i"></div>
      <div class="fld"><label for="up-u">Purpose</label><input class="in" id="up-u"></div><div class="fld"><label for="up-d">Shared on</label><input class="in" type="date" id="up-d"></div></div><label class="check"><input type="checkbox" id="up-c"> Given notice to keep it confidential, or under a confidentiality agreement</label>`, 'Record',
    () => act(() => post('/v1/hr/own-trading/upsi', { subject: $('up-s').value, nature: $('up-n').value, shared_with: $('up-w').value, org: $('up-o').value, pan: $('up-p').value, other_id: $('up-i').value, purpose: $('up-u').value, shared_on: $('up-d').value, notice: $('up-c').checked }), 'Recorded')),
  cyDiscl: () => confirmModal('Yearly disclosure (designated persons)', 'Your immediate relatives and anyone you have a material financial relationship with (name and PAN, or another identifier), and the phone numbers you use (Schedule C cl. 12). One per line: name, relation, PAN.', `<div class="fld"><label for="pd-r">Immediate relatives</label><textarea class="in" id="pd-r" rows="3" placeholder="Asha Rao, Spouse, ABCDE1234F"></textarea></div><label class="check"><input type="checkbox" id="pd-nr"> I have no immediate relatives to declare</label>
      <div class="fld"><label for="pd-f">Material financial relationship</label><textarea class="in" id="pd-f" rows="2"></textarea></div><label class="check"><input type="checkbox" id="pd-nf"> None</label>
      <div class="fld"><label for="pd-p">Phone and mobile numbers you use</label><input class="in" id="pd-p"></div><div class="fld"><label for="pd-e">Schools and colleges (once)</label><input class="in" id="pd-e"></div><div class="fld"><label for="pd-x">Past employers (once)</label><input class="in" id="pd-x"></div>`, 'Declare',
    () => { const ppl = id => $(id).value.split('\n').map(l => l.split(',').map(x => x.trim())).filter(a => a[0]).map(([name, relation, pan]) => (/^[A-Z]{5}[0-9]{4}[A-Z]$/i.test(pan || '') ? { name, relation, pan } : { name, relation, other_id: pan || '' }));
      const list = id => $(id).value.split(',').map(x => x.trim()).filter(Boolean);
      return act(() => post('/v1/hr/own-trading/disclosures', { relatives: ppl('pd-r'), no_relatives: $('pd-nr').checked, financial: ppl('pd-f'), no_financial: $('pd-nf').checked, phones: list('pd-p'), education: list('pd-e'), past_employers: list('pd-x') }), 'Declared'); }),
  cyPreReport: id => confirmModal('Report on ' + id, 'The trade you made under the pre-clearance, or that you decided not to trade (Schedule C cl. 9).', `<label class="check"><input type="checkbox" id="rp-t" checked> I traded</label><div class="grid g2"><div class="fld"><label for="rp-d">Trade date</label><input class="in" type="date" id="rp-d"></div><div class="fld"><label for="rp-q">Quantity</label><input class="in" id="rp-q" inputmode="numeric"></div><div class="fld"><label for="rp-p">Average price (₹)</label><input class="in" id="rp-p" inputmode="decimal"></div></div><div class="fld"><label for="rp-w">If not traded: why</label><input class="in" id="rp-w"></div>`, 'Report',
    () => act(() => post(`/v1/hr/own-trading/pre-clearance/${encodeURIComponent(id)}/report`, $('rp-t').checked ? { traded: true, trade_date: $('rp-d').value, qty: parseInt($('rp-q').value, 10) || 0, price: $('rp-p').value } : { traded: false, reason: $('rp-w').value }), 'Reported')),
  cyAML: code => confirmModal('AML-sensitive position check · ' + code, 'What was checked: references, background, AML training (SEBI AML/CFT master circular para 80).', '<div class="fld"><label for="am-n">What was checked</label><textarea class="in" id="am-n" rows="3"></textarea></div><div class="fld"><label for="am-t">AML training</label><input class="in" id="am-t"></div>', 'Record',
    () => act(() => post(`/v1/hr/staff/${encodeURIComponent(code)}/aml-check`, { note: $('am-n').value, training: $('am-t').value }), 'Recorded')),
  cyCO: code => confirmModal('Compliance officer eligibility · ' + code, 'A director, or a graduate with 2 years’, or an SSLC holder with 5 years’ securities market experience; an employee (CDSL master circular 10.1.1).', '<label class="check"><input type="checkbox" id="co-e" checked> An employee of the firm</label><label class="check"><input type="checkbox" id="co-d"> A director</label><div class="grid g2"><div class="fld"><label for="co-q">Qualification</label><select class="in" id="co-q"><option>Graduate</option><option>SSLC</option><option>Other</option></select></div><div class="fld"><label for="co-y">Years of securities market experience</label><input class="in" id="co-y" inputmode="numeric"></div></div>', 'Record',
    () => act(() => post(`/v1/hr/staff/${encodeURIComponent(code)}/co-eligibility`, { employee: $('co-e').checked, director: $('co-d').checked, qualification: $('co-q').value, experience_years: parseInt($('co-y').value, 10) || 0 }), 'Recorded')),
});

// Staff controls on People → Certificates & own trading (hrcomp).
function cyStaffPanel(v) {
  const g = v.gaps || [];
  return `<div class="panel"><div class="hd"><h3>Staff controls</h3><small>AML-sensitive positions, the DP compliance officer, DP staff photo ID</small></div>` + tbl([['Person'], ['Gap'], ['What to do'], ['']], g.map(x => `<tr><td>${esc(x.name)} <span class="mono dim">${esc(x.code)}</span></td><td>${esc(x.what)}</td><td class="small wrap">${esc(x.detail)}</td>
    <td>${x.what.startsWith('AML') ? `<button class="btn sm" data-act="cyAML" data-id="${esc(x.code)}">Record check</button>` : x.what.includes('eligib') ? `<button class="btn sm" data-act="cyCO" data-id="${esc(x.code)}">Record</button>` : ''}</td></tr>`), { empty: `No gaps: ${nf(v.aml_people)} in AML-sensitive positions, ${nf(v.dp_staff)} in DP operations, ${nf(v.compliance_officers)} compliance officer(s).` }) + '</div>';
}

// Asks why a CKYC-sourced KYC record is being opened, then opens it.
function cyPurpose(path, purposes) {
  confirmModal('Why are you opening this record?', 'This KYC record came from the CKYC registry. It may be used only to verify identity or address, or where the client, SEBI or FIU-IND’s Director allowed it (PML Rules, rule 9(1-F)). The purpose is recorded.',
    `<div class="fld"><label for="cp-p">Purpose</label><select class="in" id="cp-p">${purposes.map(p => `<option>${esc(p)}</option>`).join('')}</select></div>`, 'Open',
    () => { const p = $('cp-p').value; closeModal(); return openBlob(path + (path.includes('?') ? '&' : '?') + 'purpose=' + encodeURIComponent(p)); });
}
