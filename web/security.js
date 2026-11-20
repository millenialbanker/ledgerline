'use strict';
/* Ledgerline console: Security → Incidents (the register, the CERT-In
   6-hour clock and the report step, the alert settings) and Security → Log
   export (syslog over TLS or HTTPS to the firm's SIEM, the download).
   Compliance officer and Admin. Loaded after app.js. */

Object.assign(UI, { incTab: 'open', incSel: '' });
const INC_ST = { Open: 'bad', Investigating: 'warn', Reported: 'acc', Closed: 'nt' };
const SEV_K = { Critical: 'bad', High: 'bad', Medium: 'warn', Low: 'acc', Info: 'nt' };
const incPill = s => pill(s, INC_ST[s] || 'nt');
const sevPill = s => pill(s, SEV_K[s] || 'nt');
const incSrc = s => ({ detected: 'Detected by a rule', firm: 'Recorded by the firm', provider: 'Notice from Ledgerline' })[s] || s;
const incCan = () => has('Compliance officer');
// Times typed in are IST, whatever the browser's time zone.
const nowLocal = () => new Date(Date.now() + 19800000).toISOString().slice(0, 16);
const istDay = back => new Date(Date.now() + 19800000 - back * 864e5).toISOString().slice(0, 10);

function incClock(c) {
  if (c.reported) return pill(c.late ? 'Reported late' : 'Reported in time', c.late ? 'warn' : 'good');
  if (c.overdue) return pill('6 h passed', 'bad');
  if (/Closed/.test(c.line)) return pill('Not reported', 'nt');
  const h = Math.floor(c.left_min / 60), m = c.left_min % 60;
  return pill(`${h} h ${String(m).padStart(2, '0')} min left`, c.left_min < 120 ? 'bad' : 'warn');
}

V.incidents = {
  load: () => get('/v1/security/incidents'),
  html: d => {
    const open = d.incidents.filter(i => i.status !== 'Closed');
    const rows = (UI.incTab === 'open' ? open : UI.incTab === 'all' ? d.incidents : []);
    const body = UI.incTab === 'rules' ? incRules(d) : UI.incTab === 'events' ? `<div id="inc-ev">${P['inc-ev'](d)}</div>` : incTable(rows, d);
    return ph('Security incidents', `Incidents opened by Ledgerline's security rules, recorded by the firm, or notified by Ledgerline. CERT-In's directions of 28 April 2022 ask for a report within ${6} hours of noticing a reportable incident: each incident shows the time left, in IST. Record the report to CERT-In (and to SEBI or the exchanges, as CSCRF asks) here.`,
      incCan() ? '<button class="btn pri" data-act="incNew">Record an incident</button>' : '') +
      `<div class="kpis">${kpi('Open', nf(d.open), 'not closed', d.open ? 'warn' : 'good')}${kpi('Past 6 hours, not reported', nf(d.overdue), 'if reportable, report now', d.overdue ? 'bad' : 'good')}${kpi('Alerts go to', nf(d.contacts.length), esc(d.contacts.join(', ') || 'no one: set contacts under Rules and contacts'), d.contacts.length ? '' : 'bad')}</div>
      <div class="note small">${esc(d.retention)}</div>
      <div class="panel"><div class="hd">${segBtns('incTab', [['open', 'Open'], ['all', 'All'], ['events', 'Events'], ['rules', 'Rules and contacts']])}</div>${body}</div>`;
  }
};

function incTable(rows) {
  return tbl([['Incident'], ['Severity'], ['What'], ['Detected (IST)'], ['CERT-In 6 hours'], ['Status'], ['']], rows.map(i => `<tr>
      <td class="mono">${esc(i.id)}<div class="dim small">${esc(incSrc(i.source))}</div></td><td>${sevPill(i.severity)}</td>
      <td class="wrap"><b>${esc(i.title)}</b><div class="dim small">${nf(i.event_count)} event(s) · last ${esc(fmtTS(i.last_seen))}${i.provider_status ? ' · Ledgerline: ' + esc(i.provider_status) : ''}</div></td>
      <td class="n">${esc(i.clock.detected_ist)}</td><td>${incClock(i.clock)}<div class="dim small">by ${esc(i.clock.deadline_ist)}</div></td>
      <td>${incPill(i.status)}${i.owner ? `<div class="dim small">${esc(i.owner)}</div>` : ''}</td>
      <td><button class="btn sm" data-act="incOpen" data-id="${esc(i.id)}">Open</button></td></tr>`), { tall: true, empty: 'No incidents.' });
}

P['inc-ev'] = d => {
  const ev = UI.incEvents;
  if (!ev) { setTimeout(() => A.incEvents(), 0); return '<div class="bd dim">Loading the last 7 days…</div>'; }
  return tbl([['Time'], ['Severity'], ['Event'], ['Who'], ['About'], ['Address'], ['Incident']], ev.map(e => `<tr><td class="n dim">${esc(fmtTS(e.at))}</td><td>${sevPill(e.severity)}</td>
    <td class="wrap">${esc(e.message)}<div class="dim small mono">${esc(e.kind)}</div></td><td>${esc(e.actor || '')}</td><td>${esc(e.subject || '')}</td><td class="mono small">${esc(e.ip || '')}</td>
    <td>${e.incident ? `<button class="linkish mono" data-act="incOpen" data-id="${esc(e.incident)}">${esc(e.incident)}</button>` : ''}</td></tr>`), { tall: true, empty: 'No security events in the last 7 days.' });
};

function incRules(d) {
  const s = d.settings, can = incCan();
  const f = (id, label, val, extra = '') => `<div class="fld"><label for="${id}">${label}</label><input class="in" id="${id}" value="${esc(val ?? '')}" ${extra} ${can ? '' : 'disabled'}></div>`;
  return `<div class="bd">
    ${tbl([['Rule'], ['Opens an incident when'], ['Severity'], ['']], d.rules.map(r => `<tr><td><b>${esc(r.name)}</b></td><td class="wrap">${esc(r.when)}</td><td>${sevPill(r.severity)}</td><td>${r.on ? pill('On', 'good') : pill('Off', 'nt')}</td></tr>`))}
    <p class="dim small">One alert per incident: later matches for the same user, address or file join the open incident. Each alert is an e-mail to the contacts below and a work item for the compliance officer and the Admin.</p>
    <div class="grid g2">
      ${f('inc-contacts', 'Alert e-mails (comma-separated; empty: the compliance and management e-mails in Settings)', (s.contacts || []).join(', '))}
      ${f('inc-nets', "The firm's own networks (addresses or ranges like 203.0.113.0/24; empty: no check)", (s.allowed_networks || []).join(', '))}
      ${f('inc-un', 'Wrong sign-ins for one user', s.failed_user_n, 'inputmode="numeric"')}${f('inc-um', '… within minutes', s.failed_user_minutes, 'inputmode="numeric"')}
      ${f('inc-in', 'Wrong sign-ins from one address', s.failed_ip_n, 'inputmode="numeric"')}${f('inc-im', '… within minutes', s.failed_ip_minutes, 'inputmode="numeric"')}
      ${f('inc-mb', 'Very large download (MB)', s.large_download_mb, 'inputmode="numeric"')}${f('inc-ret', 'Keep security events and sign-ins (days, at least 180)', s.retention_days, 'inputmode="numeric"')}
      <div class="fld"><label class="check"><input type="checkbox" id="inc-adm" ${s.no_admin_alert ? '' : 'checked'} ${can ? '' : 'disabled'}> Open an incident when someone is given the Admin role</label></div>
    </div>
    ${can ? '<div class="ft" style="padding:0;border:0"><button class="btn pri" data-act="incSaveSet">Save</button></div>' : ''}
    ${s.updated_by ? `<p class="dim small">Last changed by ${esc(s.updated_by)} ${esc(fmtTS(s.updated_at))}</p>` : ''}</div>`;
}

function incDetail(x) {
  const d = C.incidents, c = x.clock;
  const notes = (x.notes || []).slice().reverse().map(n => `<li><span class="dim small">${esc(fmtTS(n.at))} · ${esc(n.by)}${n.status ? ' · ' + incPill(n.status) : ''}</span><div class="wrap">${esc(n.text)}</div></li>`).join('');
  const reps = tbl([['To'], ['When'], ['How'], ['Reference'], ['By']], (x.reports || []).map(r => `<tr><td>${esc(r.to)}</td><td class="n">${esc(fmtTS(r.at))}</td><td>${esc(r.how)}</td><td class="mono">${esc(r.reference || '—')}</td><td>${esc(r.by)}</td></tr>`), { empty: 'Not reported yet.' });
  const evs = tbl([['Time'], ['Event'], ['Who'], ['Address']], (x.event_list || []).map(e => `<tr><td class="n dim">${esc(fmtTS(e.at))}</td><td class="wrap">${esc(e.message)}</td><td>${esc(e.actor || '')}</td><td class="mono small">${esc(e.ip || '')}</td></tr>`), { empty: 'No events (recorded by hand).' });
  const can = incCan() && x.status !== 'Closed';
  modal(mhd(`${esc(x.id)} · ${esc(x.title)}`, `${esc(incSrc(x.source))} · ${x.severity} · ${x.status}`) + `<div class="bd">
    <div class="note ${c.overdue ? 'bad' : c.reported ? 'good' : 'warn'}"><b>CERT-In clock.</b> Detected ${esc(c.detected_ist)}; 6 hours end ${esc(c.deadline_ist)}. ${esc(c.line)}.
      <div class="small">Report reportable incidents to CERT-In by e-mail to incident@cert-in.org.in or on CERT-In's website, using its incident reporting form, and to SEBI and the exchanges or depositories as CSCRF asks. Whether an incident is reportable is the firm's judgement against the directions' list.</div></div>
    ${x.description ? `<p class="wrap">${esc(x.description)}</p>` : ''}
    ${x.alert ? `<p class="small dim">Alert ${esc(fmtTS(x.alert.at))}: ${(x.alert.results || []).map(esc).join(' · ')}</p>` : ''}
    <h4>Reports</h4>${reps}
    <h4>Events (${nf(x.event_count)})</h4>${evs}
    <h4>History</h4><ul class="small">${notes}</ul>
    ${incCan() ? `<div class="grid g2"><div class="fld"><label for="inc-st">Status</label><select class="in" id="inc-st"><option value="">(no change)</option>${d.statuses.filter(s => s !== 'Reported').map(s => `<option ${s === x.status ? 'disabled' : ''}>${s}</option>`).join('')}</select></div>
      <div class="fld"><label for="inc-note">Note (what you found or did; needed to close)</label><input class="in" id="inc-note"></div></div>` : ''}
    <div id="inc-err"></div></div>
    <div class="ft">${incCan() ? `<button class="btn" id="inc-own">Take it on</button><button class="btn" id="inc-save">Save</button>` : ''}${can ? '<button class="btn pri" id="inc-rep">Reported to CERT-In / SEBI…</button>' : ''}</div>`);
  const upd = own => act(() => post(`/v1/security/incidents/${encodeURIComponent(x.id)}/status`, { status: $('inc-st')?.value || '', note: $('inc-note')?.value.trim() || '', own }), 'Incident updated').catch(e => { $('inc-err').innerHTML = errHTML(e); });
  if ($('inc-own')) $('inc-own').onclick = () => upd(true);
  if ($('inc-save')) $('inc-save').onclick = () => upd(false);
  if ($('inc-rep')) $('inc-rep').onclick = () => incReport(x);
}

function incReport(x) {
  const d = C.incidents;
  confirmModal('Record the report', `${esc(x.id)}: when, how and the reference number CERT-In (or SEBI, the exchange or the depository) gave. A report to CERT-In moves the incident to Reported.`,
    `<div class="grid g2"><div class="fld"><label for="ir-to">Reported to</label><select class="in" id="ir-to">${d.report_to.map(t => `<option>${esc(t)}</option>`).join('')}</select></div>
     <div class="fld"><label for="ir-how">How</label><select class="in" id="ir-how">${d.report_how.map(t => `<option>${esc(t)}</option>`).join('')}</select></div>
     <div class="fld"><label for="ir-at">When (IST)</label><input class="in" type="datetime-local" id="ir-at" value="${nowLocal()}"></div>
     <div class="fld"><label for="ir-ref">Reference or acknowledgement number</label><input class="in mono" id="ir-ref"></div></div>
     <div class="fld"><label for="ir-note">Note (for a phone report, whom you spoke to)</label><input class="in" id="ir-note"></div>`, 'Record report',
    () => act(() => post(`/v1/security/incidents/${encodeURIComponent(x.id)}/report`, { to: $('ir-to').value, how: $('ir-how').value, at: $('ir-at').value, reference: $('ir-ref').value.trim(), note: $('ir-note').value.trim() }), 'Report recorded'));
}

V.logexport = {
  load: () => get('/v1/security/log-export'),
  html: d => {
    const st = s => pill(s, { Delivering: 'good', Retrying: 'warn', Stuck: 'bad', 'Waiting to start': 'acc', 'Not set': 'nt' }[s] || 'nt');
    const today = istDay(0), from = istDay(6);
    return ph('Log export', "Security events, sign-ins and the audit trail sent to the firm's SIEM or security operations centre (SOC), as SEBI's CSCRF asks. Each destination keeps its place and sends again after a failure, so nothing is lost while the SIEM is down. Set the destinations up under Connections (Admin).",
      has('Admin') ? '<button class="btn" data-act="go" data-id="connections">Connections</button>' : '') +
      `<div class="note small">${esc(d.retention)}</div>` +
      `<div class="panel"><div class="hd"><h3>Destinations</h3></div>${tbl([['Destination'], ['Sends to'], ['What'], ['Status'], ['Last delivered'], ['Waiting'], ['']], d.dests.map(x => {
        const s = x.state || {};
        return `<tr><td><b>${esc(x.name)}</b></td><td class="mono small wrap">${esc(x.target || '—')}${x.auth ? `<div class="dim">${esc(x.auth)}</div>` : ''}</td><td class="small">${esc(x.logs || '')}</td>
          <td>${st(x.status)}${s.last_error ? `<div class="small neg wrap">${esc(s.last_error)}</div><div class="dim small">next try ${esc(fmtTS(s.next_try))}</div>` : ''}${s.last_test ? `<div class="dim small">Test ${esc(fmtTS(s.last_test))}: ${esc(s.last_test_text)}</div>` : ''}</td>
          <td class="n">${s.last_delivered ? esc(fmtTS(s.last_delivered)) + `<div class="dim small">${nf(s.delivered)} lines in all</div>` : '—'}</td><td class="r">${x.set ? nf(x.pending) + (x.pending >= 500 ? '+' : '') : ''}</td>
          <td>${x.set ? `<button class="btn sm" data-act="lxTest" data-id="${esc(x.id)}">Send a test line</button>` : ''}</td></tr>`;
      }))}<div class="bd"><p class="dim small" style="margin:0">${esc(d.fields)} Syslog: RFC 5424 messages, RFC 5425 framing over TLS 1.2 or later (port 6514 by default); facility authpriv for events and sign-ins, log audit for the audit trail. HTTPS: a POST of JSON lines (application/x-ndjson); with an HMAC secret, X-Ledgerline-Signature is sha256= and the HMAC-SHA256 of X-Ledgerline-Timestamp, a newline and the body.</p></div></div>
      <div class="panel"><div class="hd"><h3>Download</h3><small>JSON lines, gzip</small></div><div class="bd"><div class="tools">
        <label class="small dim" for="lx-from">From</label><input class="in sm" type="date" id="lx-from" value="${from}"><label class="small dim" for="lx-to">To</label><input class="in sm" type="date" id="lx-to" value="${today}">
        <select class="in sm" id="lx-logs"><option value="">Events, sign-ins and audit trail</option><option value="events">Events and sign-ins only</option></select>
        <button class="btn" data-act="lxDl">Download</button></div>
        <p class="dim small">For an auditor, for CERT-In when it asks for logs, or to load history into a new SIEM. The download is itself recorded as a security event.</p></div></div>`;
  }
};

Object.assign(A, {
  incOpen: async id => { try { incDetail(await get(`/v1/security/incidents/${encodeURIComponent(id)}`)); } catch (e) { toast(e.message, true); } },
  incEvents: async () => {
    const to = istDay(0), from = istDay(6);
    try { UI.incEvents = await get(`/v1/security/events?from=${from}&to=${to}&limit=500`); } catch (e) { UI.incEvents = []; toast(e.message, true); }
    if ($('inc-ev')) rerenderPart('inc-ev');
  },
  incNew: () => confirmModal('Record an incident', 'Something the firm found itself: a phishing mail opened, a lost laptop, a suspicious call. The CERT-In clock starts when it was noticed.',
    `<div class="fld"><label for="in-t">What happened (a line)</label><input class="in" id="in-t"></div>
     <div class="fld"><label for="in-d">Details</label><textarea class="in" id="in-d" rows="4"></textarea></div>
     <div class="grid g2"><div class="fld"><label for="in-s">Severity</label><select class="in" id="in-s">${['Critical', 'High', 'Medium', 'Low', 'Info'].map(s => `<option ${s === 'Medium' ? 'selected' : ''}>${s}</option>`).join('')}</select></div>
     <div class="fld"><label for="in-k">Kind</label><input class="in" id="in-k" placeholder="phishing, lost device, malware on a PC"></div>
     <div class="fld"><label for="in-at">Noticed at (IST)</label><input class="in" type="datetime-local" id="in-at" value="${nowLocal()}"></div></div>`, 'Record',
    () => act(() => post('/v1/security/incidents', { title: $('in-t').value.trim(), description: $('in-d').value.trim(), severity: $('in-s').value, kind: $('in-k').value.trim(), noticed_at: $('in-at').value }), v => 'Recorded as ' + v.id)),
  incSaveSet: () => {
    const n = id => parseInt($(id).value, 10) || 0, list = id => $(id).value.split(',').map(x => x.trim()).filter(Boolean);
    act(() => api('PUT', '/v1/security/settings', { contacts: list('inc-contacts'), allowed_networks: list('inc-nets'), failed_user_n: n('inc-un'), failed_user_minutes: n('inc-um'),
      failed_ip_n: n('inc-in'), failed_ip_minutes: n('inc-im'), large_download_mb: n('inc-mb'), retention_days: n('inc-ret'), no_admin_alert: !$('inc-adm').checked }), 'Saved');
  },
  lxTest: (id, el) => { el.disabled = true; act(() => post(`/v1/security/log-export/${encodeURIComponent(id)}/test`, {}), d => d.state?.last_test_ok ? 'Test line delivered' : 'Not delivered: ' + (d.state?.last_test_text || '')).catch(() => { el.disabled = false; }); },
  lxDl: () => { const f = $('lx-from').value, t = $('lx-to').value, l = $('lx-logs').value; download(`/v1/security/logs/download?from=${f}&to=${t}${l ? '&logs=' + l : ''}`, `ledgerline-logs-${f}-to-${t}.jsonl.gz`); },
});
