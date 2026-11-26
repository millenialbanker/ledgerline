'use strict';
/* Ledgerline console: Security → Technical glitches (V.glitches) and the
   resilience tabs of Cyber controls (cyResTab): recovery targets and the
   RPO achieved, intraday backups, capacity, Ledgerline's release evidence
   and the cloud evidence pack, the test environment, inspection access,
   confidentiality agreements and the API caller list.

   Server: internal/ops/glitches.go, cyberres.go, cyberset.go,
   backuprpo.go, capacity.go, relevidence.go, sandbox.go;
   internal/api/cyberres.go. Loaded after cyber.js. Names are prefixed tg
   (glitches) and cr (resilience). */

const tgDT = (id, v) => `<input class="in" type="datetime-local" id="${id}" value="${esc((v || '').slice(0, 16))}">`;

V.glitches = {
  load: () => get('/v1/security/glitches'),
  html: d => {
    const co = has('Compliance officer'), rec = has('Compliance officer', 'Risk manager');
    const banner = d.applies === 'no' ? `<div class="note">The technical glitch framework does not apply to the firm: ${esc(d.why)}.</div>`
      : d.applies === 'unsure' ? `<div class="note warn">Not sure the framework applies: ${esc(d.why)}.</div>` : '';
    return ph('Technical glitches', 'SEBI’s technical glitch framework: a stoppage, slowdown or variance of login, orders or the margin and funds view for 5 minutes or more in trading hours. Record each one; Ledgerline checks it against the test, runs the clocks (NSE 1 hour, exchanges and clients 2 hours, preliminary report T+1, root cause analysis 14 working days) and drafts the reports for the Samuhik Prativedan Manch portal.',
      rec && d.applies !== 'no' ? '<button class="btn pri" data-act="tgNew">Record a glitch</button>' : '') + banner +
      `<div class="kpis">${kpi('Open', nf(d.open), 'glitches', d.open ? 'warn' : 'good')}${kpi('Clocks missed', nf(d.overdue), 'not done in time', d.overdue ? 'bad' : 'good')}
        ${kpi('Registered clients', nf(d.check.clients), 'on ' + fmtDate(d.check.as_of), d.check.over ? 'warn' : 'good')}${kpi('Specified stockbroker', esc(d.specified), d.specified === 'yes' ? 'enhanced duties' : '', d.specified === 'yes' ? 'acc' : 'good')}</div>
      ${d.specified === 'yes' ? `<div class="note"><b>Specified stockbroker.</b> ${d.enhanced.map(esc).join(' ')}</div>` : ''}
      <div class="panel">${tbl([['Glitch'], ['What'], ['Began'], ['Lasted', 'r'], ['Test'], ['Clocks'], ['']], d.glitches.map(g => `<tr><td class="mono">${esc(g.id)}<div>${pill(g.status, g.status === 'Closed' ? 'nt' : 'warn')}</div>${g.incident ? `<div class="small"><button class="linkish mono" data-act="go" data-id="incidents">${esc(g.incident)}</button></div>` : ''}</td>
        <td class="wrap"><b>${esc(g.title)}</b><div class="small dim">${esc(g.system)}${g.vendor ? ' · ' + esc(g.vendor) : ''} · ${esc((g.functions || []).join(', '))}</div></td>
        <td class="n">${esc(fmtTS(g.started))}</td><td class="r">${nf(g.minutes)} min</td><td class="small wrap">${g.reportable ? pill('Reportable', 'bad') : pill('Not reportable', 'nt')}<div class="dim">${esc(g.why)}</div></td>
        <td class="small">${g.clocks.map(c => `<div class="${c.overdue ? 'neg' : c.done ? 'pos' : ''}">${c.done ? '✓' : c.overdue ? '!' : '·'} ${esc(c.name)}: ${esc(c.due_ist)}${c.late ? ' (late)' : ''}</div>`).join('') || '—'}</td>
        <td>${co || rec ? `<button class="btn sm" data-act="tgOpen" data-id="${esc(g.id)}">Open</button>` : ''}</td></tr>`), { tall: true, empty: 'No glitches recorded.' })}</div>`;
  }
};

function tgForm(g) {
  const d = C.glitches, x = g || { functions: [], exchanges: d.exchanges, kind: 'Stoppage' };
  return `<div class="grid g2"><div class="fld full"><label for="tg-t">What happened</label><input class="in" id="tg-t" value="${esc(x.title || '')}"></div>
    <div class="fld"><label for="tg-s">System that failed</label><input class="in" id="tg-s" value="${esc(x.system || '')}" placeholder="Trading app, OMS, RMS…"></div>
    <div class="fld"><label for="tg-v">Outsourced provider (if any)</label><input class="in" id="tg-v" value="${esc(x.vendor || '')}"></div>
    <div class="fld"><label for="tg-k">Nature</label><select class="in" id="tg-k">${d.kinds.map(k => `<option ${k === x.kind ? 'selected' : ''}>${esc(k)}</option>`).join('')}</select></div>
    <div class="fld"><label for="tg-c">Unique clients affected</label><input class="in" id="tg-c" inputmode="numeric" value="${esc(x.clients_affected || '')}"></div>
    <div class="fld"><label for="tg-b">Began (IST)</label>${tgDT('tg-b', x.started)}</div><div class="fld"><label for="tg-n">Noticed (IST)</label>${tgDT('tg-n', x.noticed)}</div>
    <div class="fld"><label for="tg-e">Ended (IST, empty if still going on)</label>${tgDT('tg-e', x.ended)}</div>
    <div class="fld"><label for="tg-x">Left out of the framework because</label><select class="in" id="tg-x"><option value="">(not left out)</option>${d.exclusions.map(e => `<option value="${esc(e.id)}" ${e.id === x.exclusion ? 'selected' : ''}>${esc(e.name)}</option>`).join('')}</select></div>
    <div class="fld"><label for="tg-xn">Exclusion detail</label><input class="in" id="tg-xn" value="${esc(x.exclusion_note || '')}"></div></div>
    <div class="fld"><span class="lbl">Affected</span>${d.functions.map((f, i) => `<label class="check"><input type="checkbox" id="tg-f${i}" ${(x.functions || []).includes(f) ? 'checked' : ''}> ${esc(f)}</label>`).join('')}</div>
    <div class="fld"><span class="lbl">Exchanges affected</span>${['NSE', 'BSE', 'MCX', 'MSE', 'NCDEX'].map(e => `<label class="check"><input type="checkbox" id="tg-x-${e}" ${(x.exchanges || []).includes(e) ? 'checked' : ''}> ${e}</label>`).join('')}</div>
    <label class="check"><input type="checkbox" id="tg-oh" ${x.outside_hours ? 'checked' : ''}> Outside trading hours</label>
    <div class="fld"><label for="tg-d">Description</label><textarea class="in" id="tg-d" rows="2">${esc(x.description || '')}</textarea></div>
    ${g ? ['root_cause', 'impact', 'corrective', 'preventive'].map(k => `<div class="fld"><label for="tg-${k}">${{ root_cause: 'Root cause', impact: 'Impact', corrective: 'Corrective action', preventive: 'Preventive action' }[k]}</label><textarea class="in" id="tg-${k}" rows="2">${esc(x[k] || '')}</textarea></div>`).join('') : ''}
    <div class="fld"><label for="tg-note">Note</label><input class="in" id="tg-note"></div>`;
}

function tgBody(g) {
  const b = { title: $('tg-t').value, system: $('tg-s').value, vendor: $('tg-v').value, kind: $('tg-k').value, clients_affected: parseInt($('tg-c').value, 10) || 0, started: $('tg-b').value, noticed: $('tg-n').value, ended: $('tg-e').value,
    exclusion: $('tg-x').value, exclusion_note: $('tg-xn').value, functions: C.glitches.functions.filter((f, i) => $('tg-f' + i).checked), exchanges: ['NSE', 'BSE', 'MCX', 'MSE', 'NCDEX'].filter(e => $('tg-x-' + e).checked), outside_hours: $('tg-oh').checked, description: $('tg-d').value, note: $('tg-note').value };
  if (g) for (const k of ['root_cause', 'impact', 'corrective', 'preventive']) b[k] = $('tg-' + k).value;
  return b;
}

Object.assign(A, {
  tgNew: () => confirmModal('Record a technical glitch', 'Ledgerline applies the 5-minute test and the exclusions, and starts the clocks from when it began.', tgForm(null), 'Record', () => act(() => post('/v1/security/glitches', tgBody(null)), g => `${g.id}: ${g.why}`)),
  tgOpen: id => {
    const g = C.glitches.glitches.find(x => x.id === id), co = has('Compliance officer');
    const others = (g.exchanges || []).filter(x => x !== 'NSE');
    confirmModal('Glitch ' + id, g.why, `<div class="tools">${co ? `<button class="btn sm" data-act="tgReport" data-id="${esc(id)}|nse">NSE told…</button>${others.length ? `<button class="btn sm" data-act="tgReport" data-id="${esc(id)}|exchanges">${esc(others.join(', '))} told…</button>` : ''}
        <button class="btn sm" data-act="tgNotice" data-id="${esc(id)}">Tell clients…</button><button class="btn sm" data-act="tgReport" data-id="${esc(id)}|prelim">Preliminary report filed…</button><button class="btn sm" data-act="tgReport" data-id="${esc(id)}|rca">RCA filed…</button>
        <button class="btn sm" data-act="dl" data-id="/v1/security/glitches/${esc(id)}/draft?annexure=B" data-name="${esc(id)}-annexure-b.txt">Draft Annexure B</button><button class="btn sm" data-act="dl" data-id="/v1/security/glitches/${esc(id)}/draft?annexure=C" data-name="${esc(id)}-annexure-c.txt">Draft Annexure C</button>
        <button class="btn sm" data-act="tgExempt" data-id="${esc(id)}">MCX exemption evidence…</button>${g.incident ? '' : `<button class="btn sm" data-act="tgIncident" data-id="${esc(id)}">Also a security incident…</button>`}` : ''}</div>
      ${g.exemption ? `<div class="note small">Exemption: ${nf(g.exemption.affected)} unique clients against the limit of ${nf(g.exemption.limit)}: ${g.exemption.below ? 'below, the exemption can be claimed' : 'not below'}.</div>` : ''}
      <div class="small" style="max-height:20vh;overflow:auto">${(g.notes || []).map(n => `<div><span class="dim">${esc(fmtTS(n.at))} ${esc(n.by)}</span> ${esc(n.text)}</div>`).join('')}</div><hr>${tgForm(g)}
      <div class="fld"><label for="tg-st">Status</label><select class="in" id="tg-st"><option value="">(no change)</option><option>Open</option><option>Resolved</option><option>Closed</option></select></div>`, 'Save',
      () => act(() => post('/v1/security/glitches/' + encodeURIComponent(id), Object.assign(tgBody(g), { status: $('tg-st').value })), 'Saved'));
  },
  tgReport: x => {
    const [id, stage] = x.split('|'), g = C.glitches.glitches.find(y => y.id === id), others = (g.exchanges || []).filter(e => e !== 'NSE');
    confirmModal({ nse: 'NSE told', exchanges: 'Exchanges told', prelim: 'Preliminary Incident Report filed', rca: 'Root Cause Analysis filed' }[stage], stage === 'prelim' || stage === 'rca' ? 'On the Samuhik Prativedan Manch portal: give its reference.' : 'When and how, with the reference or whom you told.',
      `${stage === 'exchanges' ? `<div class="fld">${others.map(e => `<label class="check"><input type="checkbox" id="tr-${e}" checked> ${e}</label>`).join('')}</div>` : ''}
      <div class="grid g2"><div class="fld"><label for="tr-at">When (IST)</label>${tgDT('tr-at', '')}</div><div class="fld"><label for="tr-h">How</label><select class="in" id="tr-h">${['Portal', 'Email', 'Phone', 'Other'].map(h => `<option>${h}</option>`).join('')}</select></div>
      <div class="fld"><label for="tr-r">Reference</label><input class="in" id="tr-r"></div><div class="fld"><label for="tr-n">Note</label><input class="in" id="tr-n"></div></div>`, 'Record',
      () => act(() => post(`/v1/security/glitches/${encodeURIComponent(id)}/report`, { stage, to: others.filter(e => $('tr-' + e)?.checked), at: $('tr-at').value, how: $('tr-h').value, reference: $('tr-r').value, note: $('tr-n').value }), 'Recorded'));
  },
  tgNotice: id => confirmModal('Tell clients', 'Send it through Client messages (the “glitch.notice” templates: e-mail, SMS, WhatsApp), or record the notice on the website or an app pop-up.',
    `<div class="grid g2"><div class="fld"><label for="tn-c">Channel</label><select class="in" id="tn-c">${['Client messages', 'Website', 'App pop-up', 'Other'].map(c => `<option>${c}</option>`).join('')}</select></div>
     <div class="fld"><label for="tn-t">To (Client messages)</label><input class="in" id="tn-t" value="all" placeholder="all, TAG:ONLINE or UCCs separated by commas"></div></div>
     <div class="fld"><label for="tn-x">What clients are told</label><textarea class="in" id="tn-x" rows="3">Orders cannot be placed in the app at present. Please call the dealing desk.</textarea></div><div class="fld"><label for="tn-s">Status</label><input class="in" id="tn-s" value="being fixed"></div>`, 'Send / record',
    () => { const t = $('tn-t').value.trim(), list = t === 'all' || t.startsWith('TAG:') ? [] : t.split(',').map(x => x.trim().toUpperCase()).filter(Boolean);
      return act(() => post(`/v1/security/glitches/${encodeURIComponent(id)}/notice`, { channel: $('tn-c').value, target: list.length ? '' : t, uccs: list, text: $('tn-x').value, status: $('tn-s').value }), 'Clients told'); }),
  tgExempt: id => confirmModal('MCX exemption evidence', 'For a minor or app-only glitch, the unique clients affected must stay below the lower of 2% of the quarter’s average unique clients and 2,000. Keep audit-based evidence.',
    `<div class="grid g2"><div class="fld"><label for="te-a">Quarter’s average unique clients</label><input class="in" id="te-a" inputmode="numeric"></div><div class="fld"><label for="te-f">Unique clients affected</label><input class="in" id="te-f" inputmode="numeric"></div></div>
     <label class="check"><input type="checkbox" id="te-o"> Only the app was affected</label><div class="fld"><label for="te-e">What the count rests on</label><textarea class="in" id="te-e" rows="2"></textarea></div><div class="fld"><label for="te-u">Auditor who checked it</label><input class="in" id="te-u"></div>`, 'Record',
    () => act(() => post(`/v1/security/glitches/${encodeURIComponent(id)}/exemption`, { quarter_avg: parseInt($('te-a').value, 10) || 0, affected: parseInt($('te-f').value, 10) || 0, app_only: $('te-o').checked, evidence: $('te-e').value, auditor: $('te-u').value }), g => g.exemption.below ? 'Below the limit' : 'Not below the limit')),
  tgIncident: id => confirmModal('Also a security incident', 'Link an incident already in the register, or leave it empty to open one from this glitch (its CERT-In clock starts when the glitch was noticed).', '<div class="fld"><label for="ti-i">Incident (INC-…)</label><input class="in mono" id="ti-i"></div>', 'Link',
    () => act(() => post(`/v1/security/glitches/${encodeURIComponent(id)}/incident`, { incident: $('ti-i').value.trim() }), g => 'Linked to ' + g.incident)),
});

/* ---------- Cyber controls: the resilience tabs ---------- */

function cyResTab(d, can) {
  const r = d.res;
  if (!r) return '<div class="bd dim">Not available.</div>';
  const s = r.settings, adm = has('Admin');
  switch (UI.cyTab) {
    case 'recovery': return `<div class="bd"><p class="small">Targets for a critical system: back within ${nf(s.rto_min)} minutes (RTO), no more than ${nf(s.rpo_min)} minutes of data lost (RPO).${s.target_why ? ' ' + esc(s.target_why) : ''}</p>
      <div class="note ${r.rpo.meets ? '' : 'warn'}"><b>Recovery point now.</b> ${esc(r.rpo.line)}</div>
      <div class="kpis">${kpi('Newest good backup', r.rpo.last_good ? esc(fmtTS(r.rpo.last_good)) : 'none', r.rpo.last_good ? nf(r.rpo.at_risk_min) + ' min of work since' : '', r.rpo.at_risk_min > s.rpo_min ? 'warn' : 'good')}
        ${kpi('Worst gap, 7 days', nf(r.rpo.worst_gap_min) + ' min', 'between good backups', r.rpo.worst_gap_min > s.rpo_min ? 'warn' : 'good')}${kpi('Last backup took', nf(r.rpo.backup_minutes) + ' min', '')}${kpi('Database archive', esc(r.rpo.wal), r.rpo.wal_last ? 'last ' + esc(fmtTS(r.rpo.wal_last)) : 'operator’s')}</div>
      ${can ? '<button class="btn" data-act="crSettings">Targets and intraday backups…</button>' : ''}</div>`;
    case 'capacity': {
      const c = r.capacity;
      return `<div class="bd"><p class="small">${esc(c.line)}</p>${c.alerts.map(a => `<div class="note warn small">${esc(a)}</div>`).join('')}
        <div class="kpis">${kpi('Data', nf(Math.round(c.data_mb)) + ' MB', c.growth_mb_month ? `+${nf(Math.round(c.growth_mb_month))} MB a month` : 'growth not known yet')}${kpi('In 3 years', nf(Math.round(c.in_3_years_mb)) + ' MB', 'at the present growth')}
          ${kpi('Last backup', nf(c.backup_minutes) + ' min', 'limit ' + nf(s.backup_max_min) + ' min', c.backup_minutes > s.backup_max_min ? 'warn' : 'good')}${kpi('Server memory', nf(c.server_memory_mb) + ' MB', 'shared by every firm')}</div>
        <p class="small dim">Thresholds: 95th percentile answer ${nf(s.slow_ms)} ms, ${nf(s.busy_per_min)} requests in a minute${s.data_mb ? ', data ' + nf(s.data_mb) + ' MB' : ''}. ${can ? '<button class="linkish" data-act="crSettings">Change</button>' : ''}</p></div>` +
        tbl([['Day'], ['Requests', 'r'], ['Busiest minute', 'r'], ['Slowest (p95)', 'r'], ['Errors', 'r']], c.days.map(x => `<tr><td>${fmtDate(x.day)}</td><td class="r">${nf(x.requests)}</td><td class="r ${x.peak_per_min > s.busy_per_min ? 'neg' : ''}">${nf(x.peak_per_min)}</td><td class="r ${x.p95_ms > s.slow_ms ? 'neg' : ''}">${nf(x.p95_ms)} ms</td><td class="r">${nf(x.errors)}</td></tr>`), { empty: 'No figures yet: they start with the first quarter hour of use.' });
    }
    case 'evidence': return `<div class="bd"><p class="small">${esc(r.release.line)}</p><div class="tools"><button class="btn" data-act="dl" data-id="/v1/security/evidence-pack" data-name="ledgerline-evidence.json">Download the evidence pack</button><button class="btn" data-act="dl" data-id="/v1/security/sbom" data-name="ledgerline-sbom.cdx.json">SBOM</button></div>
      <p class="small dim">VAPT and cyber audit cycle: <b>${esc(r.audit_cycle.cycle)}</b>. ${esc(r.audit_cycle.line)}</p></div>` +
      tbl([['Release'], ['Test'], ['By'], ['Result'], ['Report hash']], r.release.evidence.map(e => `<tr><td class="mono">${esc(e.version)}<div class="dim">${fmtDate(e.released)}</div></td><td>${esc(e.kind)}</td><td>${esc(e.tool)}${e.cert_in ? ' ' + pill('CERT-In', 'acc') : ''}</td><td>${pill(e.result, e.result === 'Findings open' ? 'warn' : 'good')}${e.findings ? `<div class="small dim wrap">${esc(e.findings)}</div>` : ''}</td><td class="mono small">${esc((e.report_sha256 || '').slice(0, 16))}</td></tr>`), { empty: 'Ledgerline has recorded no release tests yet.' }) +
      '<div class="bd"><h4 style="margin:0">SEBI cloud framework: principles 1 to 9</h4></div>' + tbl([['Principle'], ['What Ledgerline shows'], ['The firm’s part']], r.cloud.map(p => `<tr><td><b>${nf(p.no)}.</b> ${esc(p.name)}</td><td class="small wrap">${p.ledgerline.map(esc).join('<br>')}</td><td class="small wrap">${esc(p.firms_part)}</td></tr>`));
    case 'access': return `<div class="bd"><h4 style="margin:0">Test environment</h4><p class="small">${esc(r.sandbox.line)}</p>${adm && r.sandbox.code ? '<button class="btn" data-act="crSandbox">Prepare the test environment</button>' : ''}</div>
      <div class="bd"><h4 style="margin:0">Inspection access</h4><ol class="small">${r.inspection_procedure.map(p => `<li>${esc(p)}</li>`).join('')}</ol>${can ? '<button class="btn" data-act="crVisit" data-id="">Record a visit</button>' : ''}</div>` +
      tbl([['Visit'], ['By'], ['Inspectors'], ['Dates'], ['Given'], ['']], r.visits.map(v => `<tr><td class="mono">${esc(v.id)}</td><td>${esc(v.authority)}</td><td>${esc(v.inspectors)}</td><td class="n">${fmtDate(v.from)}${v.to ? ' – ' + fmtDate(v.to) : ''}</td><td class="small">${v.given.map(esc).join(', ')}</td><td>${v.closed ? pill('Closed', 'nt') : can ? `<button class="btn sm" data-act="crVisit" data-id="${esc(v.id)}">Close…</button>` : ''}</td></tr>`), { empty: 'No inspection visits recorded.' }) +
      `<div class="bd"><h4 style="margin:0">Confidentiality agreements</h4><p class="small">Staff and third parties get system access only after signing a confidentiality and integrity agreement (CSCRF GV.RR.S5). ${s.hold_logins ? 'New logins are held until it is recorded.' : 'New logins are not held (Settings).'}</p>${can ? '<button class="btn" data-act="crAgree" data-id="">Record an agreement</button>' : ''}</div>` +
      tbl([['Login'], ['Role'], ['Agreement'], ['']], r.agreements.map(a => `<tr><td>${esc(a.name)} <span class="mono dim">${esc(a.user)}</span></td><td>${esc(a.role)}</td><td>${a.missing ? pill(a.pending ? 'Held: not signed' : 'Not recorded', a.pending ? 'bad' : 'warn') : `${esc(a.kind)}${a.party ? ' · ' + esc(a.party) : ''}, signed ${fmtDate(a.signed)}`}</td><td>${can && a.missing ? `<button class="btn sm" data-act="crAgree" data-id="${esc(a.user)}">Record</button>` : ''}</td></tr>`)) +
      `<div class="bd small">API callers: ${s.api_callers.length ? esc(s.api_callers.join(', ')) : 'any address'}; webhooks: ${s.hook_callers.length ? esc(s.hook_callers.join(', ')) : 'any address'}; ${nf(s.api_per_minute)} calls a minute per key. Phones that look rooted or jailbroken: the apps ${r.rooted_policy === 'refuse' ? 'refuse them' : 'warn'} (Client app / Staff app settings). ${can ? '<button class="linkish" data-act="crSettings">Change</button>' : ''}</div>`;
  }
  return '';
}

Object.assign(A, {
  crSettings: () => {
    const s = C.cyber.res.settings, n = (k, l) => `<div class="fld"><label for="cs-${k}">${l}</label><input class="in" id="cs-${k}" inputmode="numeric" value="${esc(s[k] || '')}"></div>`;
    confirmModal('Cyber resilience settings', 'Recovery targets, intraday backups, API callers, confidentiality agreements and capacity thresholds.', `<div class="grid g2">${n('rto_min', 'RTO (minutes)')}${n('rpo_min', 'RPO (minutes)')}</div>
      <div class="fld"><label for="cs-why">Why the targets are looser than 2 hours and 15 minutes (if they are)</label><input class="in" id="cs-why" value="${esc(s.target_why || '')}"></div>
      <div class="grid g3"><div class="fld"><label for="cs-im">Intraday backups</label><select class="in" id="cs-im">${C.cyber.res.intraday_choices.map(m => `<option value="${m}" ${m === s.intraday_minutes ? 'selected' : ''}>${m ? 'every ' + m + ' minutes' : 'off'}</option>`).join('')}</select></div>
        <div class="fld"><label for="cs-if">From</label><input class="in" id="cs-if" value="${esc(s.intraday_from)}"></div><div class="fld"><label for="cs-it">To</label><input class="in" id="cs-it" value="${esc(s.intraday_to)}"></div></div>
      <p class="small dim">Each intraday backup is a full encrypted copy; the last 16 are kept.</p>
      <div class="fld"><label for="cs-ac">API callers allowed (addresses or ranges, comma separated; empty: any)</label><input class="in mono" id="cs-ac" value="${esc(s.api_callers.join(', '))}"></div>
      <div class="fld"><label for="cs-hc">Webhook callers allowed</label><input class="in mono" id="cs-hc" value="${esc(s.hook_callers.join(', '))}"></div>
      <div class="grid g2">${n('api_per_minute', 'API calls a minute per key')}${n('slow_ms', 'Slow answer (ms, 95th percentile)')}${n('busy_per_min', 'Busy minute (requests)')}${n('data_mb', 'Data size alert (MB)')}${n('backup_max_min', 'Backup time alert (minutes)')}</div>
      <label class="check"><input type="checkbox" id="cs-hold" ${s.hold_logins ? 'checked' : ''}> Hold new logins until their confidentiality agreement is recorded (logins made before are not affected)</label>`, 'Save',
      () => { const list = id => $(id).value.split(',').map(x => x.trim()).filter(Boolean), i = id => parseInt($(id).value, 10) || 0;
        return act(() => api('PUT', '/v1/security/resilience/settings', { rto_min: i('cs-rto_min'), rpo_min: i('cs-rpo_min'), target_why: $('cs-why').value, intraday_minutes: i('cs-im'), intraday_from: $('cs-if').value.trim(), intraday_to: $('cs-it').value.trim(),
          api_callers: list('cs-ac'), hook_callers: list('cs-hc'), api_per_minute: i('cs-api_per_minute'), slow_ms: i('cs-slow_ms'), busy_per_min: i('cs-busy_per_min'), data_mb: i('cs-data_mb'), backup_max_min: i('cs-backup_max_min'), hold_logins: $('cs-hold').checked }), 'Saved'); });
  },
  crAgree: user => confirmModal('Record a confidentiality agreement', 'A held login can sign in once it is recorded.', `<div class="grid g2"><div class="fld"><label for="ca-u">Login</label><input class="in mono" id="ca-u" value="${esc(user || '')}"></div>
      <div class="fld"><label for="ca-k">Whose</label><select class="in" id="ca-k"><option>Employee</option><option>Third party</option></select></div><div class="fld"><label for="ca-p">Third party (organisation)</label><input class="in" id="ca-p"></div>
      <div class="fld"><label for="ca-s">Signed on</label><input class="in" type="date" id="ca-s"></div></div><div class="fld"><label for="ca-d">Where the signed copy is</label><input class="in" id="ca-d" placeholder="HR file 12 / DOC-000123"></div>`, 'Record',
    () => act(() => post('/v1/security/agreements', { user: $('ca-u').value.trim(), kind: $('ca-k').value, party: $('ca-p').value, signed: $('ca-s').value, doc: $('ca-d').value }), 'Recorded')),
  crVisit: id => {
    const v = (C.cyber.res.visits || []).find(x => x.id === id) || { given: [] };
    confirmModal(id ? 'Close visit ' + id : 'Record an inspection visit', 'Who came, when, and what they were given.', `<div class="grid g2"><div class="fld"><label for="iv-a">Authority</label><select class="in" id="iv-a">${['SEBI', 'NSE', 'BSE', 'MCX', 'MSE', 'NCDEX', 'CDSL', 'NSDL', 'Other'].map(a => `<option ${a === v.authority ? 'selected' : ''}>${a}</option>`).join('')}</select></div>
        <div class="fld"><label for="iv-i">Inspectors</label><input class="in" id="iv-i" value="${esc(v.inspectors || '')}"></div><div class="fld"><label for="iv-f">From</label><input class="in" type="date" id="iv-f" value="${esc(v.from || S.date)}"></div><div class="fld"><label for="iv-t">To</label><input class="in" type="date" id="iv-t" value="${esc(v.to || '')}"></div></div>
      <div class="fld">${C.cyber.res.inspection_kinds.map((k, i) => `<label class="check"><input type="checkbox" id="iv-g${i}" ${v.given.includes(k) ? 'checked' : ''}> ${esc(k)}</label>`).join('')}</div>
      <div class="fld"><label for="iv-n">Note</label><input class="in" id="iv-n" value="${esc(v.note || '')}"></div>${id ? '<div class="fld"><label for="iv-o">What the inspectors observed</label><textarea class="in" id="iv-o" rows="2"></textarea></div>' : ''}`, id ? 'Close' : 'Record',
      () => act(() => post('/v1/security/inspections', { id: id || '', authority: $('iv-a').value, inspectors: $('iv-i').value, from: $('iv-f').value, to: $('iv-t').value, given: C.cyber.res.inspection_kinds.filter((k, i) => $('iv-g' + i).checked), note: $('iv-n').value, closed: !!id, outcome: id ? $('iv-o').value : '' }), 'Saved'));
  },
  crSandbox: () => act(() => post('/v1/security/sandbox', {}), r => r.password ? `Ready. Sign in to ${r.sandbox.code} as yourself with the one-time password ${r.password}` : (r.warning || 'Ready')),
});
