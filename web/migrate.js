'use strict';
/* Ledgerline console: migration from the old back office (screen
   "migrate", Files), the old documents archive (screen "archive", Client
   documents), "Before Ledgerline" on the client screen, and "Earlier
   years" in the client portal. The migration tool at the office makes an
   encrypted package (.llmig); here it is uploaded in chunks, checked with a
   dry run, proposed by one person, approved by another, loaded and
   reconciled. Loaded after app.js. */

Object.assign(UI, { migPkg: '', migFile: '', migRun: '', arcQ: { ucc: '', type: '', from: '', to: '', signed: '' }, arcRes: null, h360: { fy: '', kind: 'LEDGER' }, pOldFY: '' });
const MIG_ST = { Uploading: 'warn', Uploaded: 'acc', Checking: 'acc', Checked: 'good', 'Check failed': 'bad', Proposed: 'warn', Rejected: 'nt', Loading: 'acc', Interrupted: 'bad', Failed: 'bad', Loaded: 'good', 'Undo proposed': 'warn', Undoing: 'acc', Undone: 'nt', Pass: 'good', Fail: 'bad', 'Not checked': 'nt', Pending: 'nt', Done: 'good' };
const migPill = s => pill(s, MIG_ST[s] || 'nt');
const migSize = b => { const n = num(b); if (n >= 1 << 30) return nf(n / (1 << 30), 2) + ' GB'; if (n >= 1 << 20) return nf(n / (1 << 20), 1) + ' MB'; if (n >= 1024) return nf(n / 1024) + ' KB'; return nf(n) + ' B'; };
const migWhen = t => t && !String(t).startsWith('0001') ? new Date(t).toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'short' }) : '—';
const migLabel = l => l ? `<div class="note"><b>${esc(l)}</b>. These are the old system's records as they were brought across; nothing here can be changed.</div>` : '';
const migKindName = { master: 'Go-live file', history: 'History', 'documents-index': 'Document index', documents: 'Documents', report: 'Tool report' };
let migTimer = null;

V.migrate = {
  load: async () => {
    const v = await get('/v1/migration');
    let pk = null, hf = [];
    if (UI.migPkg && v.packages.some(p => p.id === UI.migPkg)) pk = await get('/v1/migration/packages/' + encodeURIComponent(UI.migPkg));
    else UI.migPkg = '';
    if (v.migrations.some(m => m.status === 'Loaded') && has('Ops checker', 'Compliance officer')) hf = await get('/v1/history-files').catch(() => []);
    return { ...v, pk, hf };
  },
  html: d => {
    clearTimeout(migTimer);
    if (d.packages.some(p => p.checking || p.status === 'Checking') || d.migrations.some(m => m.status === 'Loading' || m.status === 'Undoing'))
      migTimer = setTimeout(() => { if (S.view === 'migrate' && !$('mroot').innerHTML) render({ keep: true }); }, 3000);
    const live = d.migrations.find(m => !['Rejected', 'Undone'].includes(m.status));
    return ph('Migration from the old back office', 'Bring a firm across from its previous back office (SharePro first). Run the Ledgerline migration tool at the office; it reads the old system\'s backups and makes one encrypted package (.llmig). Upload it here, check it (nothing changes), propose the load, and a second person approves it. Balances, clients and holdings load as the go-live files; earlier years come as read-only history and the old contract notes and statements go to the document archive.',
      d.can_run ? '<label class="btn pri" for="mig-file">Upload package</label><input type="file" id="mig-file" accept=".llmig" hidden>' : '') +
      `<div class="kpis">${kpi('Packages on the server', nf(d.packages.length), 'up to ' + nf(d.max_gb) + ' GB each (Settings)')}${kpi('Migration', live ? esc(live.status) : 'none yet', live ? esc(live.system + ' · firm ' + live.firm + ' ' + live.firm_name) : 'upload a package to start', live && live.status === 'Loaded' ? 'good' : live ? 'warn' : '')}${live && live.recon ? kpi('Reconciliation', `${live.recon.passed} passed · ${live.recon.failed} failed`, 'as of ' + fmtDate(live.recon.as_of), live.recon.failed ? 'bad' : 'good') : ''}</div>
      <div id="mig-up"></div>` +
      migPackages(d) + (d.pk ? migDetail(d, d.pk) : '') + d.migrations.map(m => migCard(d, m)).join('') + migHistFiles(d);
  }
};

function migPackages(d) {
  const rows = d.packages.map(p => {
    const m = p.manifest, part = m && m.part && m.part.of > 1 ? `part ${m.part.index} of ${m.part.of}${m.part.note ? ' · ' + esc(m.part.note) : ''}` : '';
    const parts = (p.parts || []).length ? `<div class="small">${p.parts.map(x => `${x.package ? '✓' : '✗'} part ${x.index}${x.package ? '' : ' not uploaded'}`).join(' · ')}</div>` : '';
    const st = p.checking ? 'Checking' : p.status;
    return `<tr class="${UI.migPkg === p.id ? 'sel' : ''}"><td><button class="linkish" data-act="migOpen" data-id="${esc(p.id)}">${esc(p.name)}</button><div class="dim small mono">${esc(p.id)}</div></td>
      <td class="r">${migSize(p.size)}</td><td>${migPill(st)}${p.status === 'Uploading' ? `<div class="dim small">${nf(p.chunks - p.missing)} of ${nf(p.chunks)} chunks · choose the same file again to resume</div>` : ''}${p.check && p.check.error ? `<div class="small neg wrap">${esc(p.check.error)}</div>` : ''}</td>
      <td class="small">${m ? esc(m.source.system) + ' · ' + esc(m.tool) + (part ? '<br>' + part : '') + parts : '<span class="dim">open it to see</span>'}</td>
      <td class="small">${p.check ? `${p.check.errors ? `<span class="neg">${nf(p.check.errors)} errors</span>` : '<span class="pos">no errors</span>'}${p.check.warnings ? ' · ' + nf(p.check.warnings) + ' warnings' : ''}${p.check.firm ? ' · firm ' + esc(p.check.firm) : ''}` : '—'}</td>
      <td class="small">${esc(p.uploaded_by)}<div class="dim">${migWhen(p.uploaded_at)}</div></td>
      <td>${d.can_run && ['Uploaded', 'Checked', 'Check failed'].includes(p.status) && !p.checking ? `<button class="btn sm" data-act="migCheck" data-id="${esc(p.id)}">${p.check ? 'Check again' : 'Check'}</button>` : ''}${d.can_run && p.status === 'Checked' ? `<button class="btn sm pri" data-act="migPropose" data-id="${esc(p.id)}">Propose load</button>` : ''}${d.can_run && !p.checking ? `<button class="btn sm" data-act="migDiscard" data-id="${esc(p.id)}">Remove</button>` : ''}</td></tr>`;
  });
  return `<div class="panel"><div class="hd"><h3>Packages</h3><small>a split package's parts are uploaded one by one; the screen shows which parts are here</small></div>${tbl([['Package'], ['Size', 'r'], ['Status'], ['Made by'], ['Dry run'], ['Uploaded'], ['']], rows, { empty: 'No package uploaded yet.' })}</div>`;
}

function migDetail(d, x) {
  const p = x.package, m = p.manifest, c = p.check;
  if (!m) return `<div class="panel"><div class="hd"><h3>${esc(p.name)}</h3><small>${migPill(p.status)}</small></div><div class="bd"><p class="dim">Check the package to see what is in it. Checking opens it with the passphrase and changes nothing.</p></div></div>`;
  const fileRows = (c ? c.files : m.files.map(f => ({ ...f }))).filter(f => !UI.migFile || f.firm === UI.migFile || !f.firm).map(f => `<tr><td class="mono small wrap">${esc(f.path)}</td><td>${esc(migKindName[f.kind] || f.kind)}</td><td class="mono small">${esc(f.source || '')}</td><td>${esc(f.fy || '')}</td><td class="r">${nf(f.rows)}</td><td class="r">${migSize(f.bytes)}</td><td>${f.checked ? (f.errors ? `<button class="linkish neg" data-act="migErrFile" data-id="${esc(f.path)}">${nf(f.errors)} errors</button>` : pill('OK', 'good')) + (f.warnings ? ' <span class="dim small">' + nf(f.warnings) + ' warnings</span>' : '') : '<span class="dim small">not dry-run</span>'}</td></tr>`);
  const errs = x.errors.filter(e => !UI.migErrOnly || e.file === UI.migErrOnly);
  return `<div class="panel"><div class="hd"><h3>${esc(p.name)}</h3><small>made ${migWhen(m.created)} by ${esc(m.tool)} · ${esc(m.source.system)}${m.source.server ? ' on ' + esc(m.source.server) : ''}</small><button class="btn sm" data-act="migClose">Close</button></div><div class="bd">
    ${c && (c.problems || []).length ? `<div class="note bad"><b>The package is damaged: ${nf(c.problems.length)} file(s) do not match their checksums.</b> Copy it again from the office and upload it again.<ul>${c.problems.slice(0, 20).map(e => `<li class="small">${esc(e)}</li>`).join('')}</ul></div>` : ''}
    ${c && !c.error && !(c.problems || []).length ? `<div class="note ${c.errors ? 'warn' : 'good'}">Every file matches its checksum. Dry run for firm <b>${esc(c.firm)}</b>, ${migWhen(c.at)} by ${esc(c.by)}: ${c.errors ? `<b>${nf(c.errors)} errors</b> to put right in the old system (or untick those files)` : '<b>no errors</b>'}${c.warnings ? `, ${nf(c.warnings)} warnings` : ''}. Nothing was saved.</div>` : ''}
    <div class="grid g2"><div>${'<h4>Backups read</h4>'}${tbl([['File'], ['Database'], ['Role'], ['Year'], ['Backup date'], ['Used']], (m.source.backups || []).map(b => `<tr><td class="small wrap">${esc(b.file || '—')}</td><td class="mono small">${esc(b.database)}</td><td>${esc(b.role)}</td><td>${esc(b.fy || '')}</td><td class="n">${b.backup_date && !b.backup_date.startsWith('0001') ? fmtDate(b.backup_date.slice(0, 10)) : '—'}</td><td>${b.used ? 'Yes' : `No${b.note ? ' · ' + esc(b.note) : ''}`}</td></tr>`), { empty: 'None listed' })}</div>
      <div>${'<h4>Firms in the package</h4>'}${tbl([['Code'], ['Name'], ['Segments'], ['Years'], ['As of']], m.firms.map(f => `<tr><td class="mono">${esc(f.code)}</td><td>${esc(f.name)}</td><td class="small">${esc((f.segments || []).join(', '))}</td><td class="small">${esc((f.years || []).join(', '))}</td><td class="n">${fmtDate(f.as_of)}</td></tr>`))}<div class="dim small">This firm loads one of them. The others can be loaded into other firms on Ledgerline with the same package.</div></div></div>
    <h4>Files · ${nf(m.files.length)}</h4>${tbl([['Path'], ['Kind'], ['Type'], ['Year'], ['Rows', 'r'], ['Size', 'r'], ['Dry run']], fileRows, { tall: true })}
    <div class="grid g2"><div><h4>What the old system shows</h4>${tbl([['Check'], ['Firm'], ['Year'], ['Value', 'r']], (m.checks || []).map(k => `<tr><td>${esc(MIG_CHECKS[k.name] || k.name)}${k.detail ? `<div class="dim small">${esc(k.detail)}</div>` : ''}</td><td class="mono">${esc(k.firm || '')}</td><td>${esc(k.fy || '')}</td><td class="r mono">${/^-?\d+\.\d\d$/.test(k.value || '') ? inr(k.value, 2) : /^-?\d+$/.test(k.value || '') ? nf(k.value) : esc(k.value)}</td></tr>`), { empty: 'The tool recorded no totals' })}</div>
      <div><h4>Left out on purpose</h4>${tbl([['Field'], ['Why'], ['Count', 'r']], (m.dropped || []).map(k => `<tr><td>${esc(k.field)}</td><td class="small wrap">${esc(k.reason)}</td><td class="r">${nf(k.count)}</td></tr>`), { empty: 'Nothing' })}<div class="dim small">Passwords and full Aadhaar numbers are never migrated.</div></div></div>
    <h4>What the migration tool found · ${nf((m.issues || []).length)}</h4>${tbl([['Severity'], ['Area'], ['Record'], ['Problem'], ['What to do']], (m.issues || []).map(i => `<tr><td>${pill(i.severity, i.severity === 'Error' ? 'bad' : i.severity === 'Warning' ? 'warn' : 'nt')}</td><td>${esc(i.area)}${i.fy ? ' · ' + esc(i.fy) : ''}</td><td class="mono small">${esc(i.key || '')}</td><td class="small wrap">${esc(i.message)}</td><td class="small wrap">${esc(i.action || '')}</td></tr>`), { tall: true, empty: 'Nothing' })}
    ${c ? `<h4>Dry run · ${nf(x.error_count)} problems${UI.migErrOnly ? ' in ' + esc(UI.migErrOnly) + ' <button class="linkish" data-act="migErrFile" data-id="">show all</button>' : ''}</h4>
      <div class="tools"><button class="btn sm" data-act="dl" data-id="/v1/migration/packages/${encodeURIComponent(p.id)}/errors.csv" data-name="migration-check-${esc(p.id)}.csv">Download every problem (CSV)</button><button class="btn sm" data-act="dl" data-id="/v1/migration/packages/${encodeURIComponent(p.id)}/clients-to-fix.csv" data-name="migration-clients-to-fix-${esc(p.id)}.csv" title="One client a row: code, name and each detail to put right in the old system">Clients to fix (CSV)</button><button class="btn sm" data-act="dl" data-id="/v1/migration/packages/${encodeURIComponent(p.id)}/clients-to-fix.csv?warnings=1" data-name="migration-clients-to-review-${esc(p.id)}.csv" title="Also the warnings that do not stop the load">With warnings</button>${x.error_count > x.errors.length ? `<span class="dim small">The first ${nf(x.errors.length)} are shown; the CSV has all ${nf(x.error_count)}.</span>` : ''}</div>
      ${tbl([['File'], ['Row', 'r'], ['Client'], ['Field'], ['Problem'], ['Recommended action']], errs.map(e => `<tr><td class="mono small wrap">${esc(e.file.split('/').pop())}</td><td class="r">${e.line ? nf(e.line) : '—'}</td><td class="small">${e.ucc ? `<span class="mono">${esc(e.ucc)}</span>${e.client ? '<div class="dim">' + esc(e.client) + '</div>' : ''}` : ''}</td><td class="mono small">${esc(e.field || '')}${e.blank ? ' <span class="dim">(missing)</span>' : ''}</td><td class="small wrap">${e.severity === 'Warning' ? pill('Warning', 'warn') + ' ' : ''}${esc(e.message)}</td><td class="small wrap">${esc(e.action)}</td></tr>`), { tall: true, empty: 'No problems: every row would load.' })}
      <div class="dim small" style="margin-top:6px">Warnings for inactive clients do not stop the load: those clients come across marked incomplete, and their details must be completed before they are reactivated.</div>` : ''}
  </div></div>`;
}

function migCard(d, m) {
  const done = m.steps.filter(s => s.status === 'Done').length, me = S.me?.actor;
  const steps = m.steps.map((s, i) => `<tr><td class="r dim">${i + 1}</td><td>${esc({ master: 'Go-live file', history: 'History', documents: 'Documents' }[s.kind])}</td><td class="mono small">${esc(s.source || '')}${s.fy ? ' ' + esc(s.fy) : ''}${(s.fys || []).length ? ' ' + esc(s.fys.join(', ')) : ''}</td><td class="r">${s.kind === 'documents' ? nf(Object.values(s.counts || {}).reduce((a, b) => a + b, 0)) : nf(s.rows)}</td><td>${migPill(s.status)}${s.message ? `<div class="small wrap ${s.status === 'Failed' ? 'neg' : 'dim'}">${esc(s.message)}</div>` : ''}</td></tr>`);
  let acts = '';
  if (m.status === 'Proposed' && d.can_approve) acts = me === m.proposed_by ? '<span class="dim small">Waiting for a second person to approve.</span>' : `<button class="btn" data-act="migDecide" data-id="${esc(m.id)}" data-a="0">Reject</button><button class="btn pri" data-act="migDecide" data-id="${esc(m.id)}" data-a="1">Approve and load</button>`;
  if (['Interrupted', 'Failed'].includes(m.status) && d.can_approve) acts = `<button class="btn pri" data-act="migResume" data-id="${esc(m.id)}">Resume${m.status === 'Interrupted' && m.undo && m.undo.status === 'Approved' ? ' undo' : ''}</button>`;
  if (m.status === 'Loaded') acts = `${has('Ops checker', 'Compliance officer') ? `<button class="btn" data-act="migRecon" data-id="${esc(m.id)}">Reconcile again</button>` : ''}${m.recon ? `<button class="btn" data-act="dl" data-id="/v1/migration/runs/${encodeURIComponent(m.id)}/reconcile.csv" data-name="migration-reconciliation-${esc(m.id)}.csv">CSV</button><button class="btn" data-act="dl" data-id="/v1/migration/runs/${encodeURIComponent(m.id)}/reconcile.pdf" data-name="migration-reconciliation-${esc(m.id)}.pdf">PDF</button>` : ''}${d.can_run ? `<button class="btn bad" data-act="migUndo" data-id="${esc(m.id)}">Undo migration</button>` : ''}`;
  if (m.status === 'Failed' && d.can_run) acts += `<button class="btn bad" data-act="migUndo" data-id="${esc(m.id)}">Undo what was loaded</button>`;
  if (m.status === 'Undo proposed' && d.can_approve) acts = me === m.undo?.proposed_by ? '<span class="dim small">Waiting for a second person to approve the undo.</span>' : `<button class="btn" data-act="migUndoDecide" data-id="${esc(m.id)}" data-a="0">Keep the migration</button><button class="btn bad" data-act="migUndoDecide" data-id="${esc(m.id)}" data-a="1">Approve undo</button>`;
  const who = `Proposed by <b>${esc(m.proposed_by)}</b> ${migWhen(m.proposed_at)}${m.note ? ' · ' + esc(m.note) : ''}${m.decided_by ? ` · ${m.status === 'Rejected' ? 'rejected' : 'approved'} by <b>${esc(m.decided_by)}</b> ${migWhen(m.decided_at)}${m.decision_note ? ' · ' + esc(m.decision_note) : ''}` : ''}${m.not_live ? ' · <span class="warn">confirmed the firm is not live yet</span>' : ''}`;
  const undo = m.undo ? `<div class="note ${m.undo.status === 'Done' ? '' : 'warn'}">Undo ${esc(m.undo.status.toLowerCase())}: asked by ${esc(m.undo.proposed_by)} ${migWhen(m.undo.proposed_at)} · ${esc(m.undo.reason)}${m.undo.decided_by ? ` · decided by ${esc(m.undo.decided_by)}${m.undo.note ? ' · ' + esc(m.undo.note) : ''}` : ''}${m.undo.status === 'Done' ? ` · ${nf(m.undo.reversed)} opening entries reversed; history and archived documents taken out` : ''}</div>` : '';
  return `<div class="panel"><div class="hd"><h3>Migration ${esc(m.id)} · ${esc(m.system)} firm ${esc(m.firm)} ${esc(m.firm_name)}</h3><small>${migPill(m.status)} balances as of ${fmtDate(m.as_of)}</small></div><div class="bd">
    <div class="small">${who}</div>${m.error ? `<div class="note bad">${esc(m.error)}</div>` : ''}${undo}
    ${m.steps.length ? `<div class="tools">${migBar(m.steps.length ? done / m.steps.length : 0, m.status)}<span class="dim small">${nf(done)} of ${nf(m.steps.length)} steps · ${nf(m.docs)} documents archived</span></div>` : `<div class="dim small">${nf(m.files.length)} files ticked: ${esc(m.files.map(f => f.split('/').slice(2).join('/')).join(', '))}</div>`}
    ${m.steps.length ? `<details ${['Loading', 'Failed', 'Interrupted'].includes(m.status) ? 'open' : ''}><summary class="small">Load steps in order</summary>${tbl([['#', 'r'], ['Step'], ['File'], ['Rows', 'r'], ['Status']], steps)}</details>` : ''}
    ${m.recon ? migRecon(m) : ''}
    ${acts ? `<div class="ft" style="padding:8px 0 0;border:0">${acts}</div>` : ''}</div></div>`;
}

function migRecon(m) {
  const r = m.recon;
  const rows = r.rows.map(x => `<tr><td>${esc(x.label)}${x.fy ? ' · ' + esc(x.fy) : ''}${x.detail ? `<div class="dim small">${esc(x.detail)}</div>` : ''}</td><td class="r mono">${migNum(x.name, x.expected)}</td><td class="r mono">${x.status === 'Not checked' ? '—' : migNum(x.name, x.ledgerline)}</td><td class="r mono ${num(x.difference) ? 'neg' : ''}">${x.difference ? migNum(x.name, x.difference) : '—'}</td><td>${migPill(x.status)}${(x.behind || []).length ? `<details><summary class="small">Rows behind it · ${nf(x.behind.length + (x.more || 0))}</summary>${tbl([['Account, client or holding'], ['In the loaded file', 'r'], ['In Ledgerline', 'r']], x.behind.map(b => `<tr><td class="mono small">${/^CLIENT:/.test(b.key) || /^[A-Z0-9]{3,10}$/.test(b.key) ? `<button class="linkish" data-act="client" data-id="${esc(b.key.replace(/^CLIENT:/, '').split(' ')[0])}">${esc(b.key)}</button>` : esc(b.key)}</td><td class="r mono small">${esc(b.expected)}</td><td class="r mono small">${esc(b.ledgerline)}</td></tr>`))}${x.more ? `<div class="dim small">and ${nf(x.more)} more</div>` : ''}</details>` : ''}</td></tr>`);
  return `<h4>Reconciliation · ${migWhen(r.at)} by ${esc(r.by)}</h4>${tbl([['Check'], ['Expected (old system)', 'r'], ['Ledgerline', 'r'], ['Difference', 'r'], ['Result']], rows, { empty: 'The package carried no totals to check.' })}`;
}
const migNum = (name, v) => { if (v === '' || v == null) return '—'; const money = /balance|debit|credit/.test(name); return money ? inr(String(v).replace(/,/g, ''), 2) : nf(String(v).replace(/,/g, '')); };

function migHistFiles(d) {
  if (!d.hf.length) return '';
  return `<div class="panel"><div class="hd"><h3>Earlier years (read only)</h3><small>${esc(d.hf[0].system ? 'Migrated from ' + d.hf[0].system + ' — read only' : '')}</small></div>${tbl([['Year'], ['History'], ['Rows', 'r'], ['']], d.hf.map(h => `<tr><td>${esc(h.fy)}</td><td>${esc(h.kind)}</td><td class="r">${nf(h.rows)}</td><td><button class="btn sm" data-act="dl" data-id="/v1/history-files/${encodeURIComponent(h.fy)}/${encodeURIComponent(h.kind)}/csv" data-name="history-${esc(h.fy)}-${esc(h.kind.toLowerCase())}.csv">Export CSV</button></td></tr>`))}<div class="bd dim small">Each client's earlier years are on the client screen under Before Ledgerline, and in the client portal under Earlier years.</div></div>`;
}

/* ---------- chunked, resumable upload ---------- */
const MIG_LS = 'ledgerline-mig-upload';
// MIG_CHECKS names the totals the old system shows (as on the reconciliation).
const MIG_CHECKS = { 'trial-balance-debit': 'Trial balance: total debits', 'trial-balance-credit': 'Trial balance: total credits', clients: 'Clients', 'client-debit-total': 'Clients in debit: total', 'client-credit-total': 'Clients in credit: total', 'dp-accounts': 'Demat accounts', 'dp-holding-quantity': 'Demat holdings: total quantity', documents: 'Archived documents', 'client-bank-accounts': 'Client bank accounts', 'open-positions': 'Open positions', 'opening-difference': 'Difference in opening balances (to be confirmed by the accountant)', 'dp-tariff-versions': 'DP tariff plan versions', 'dp-tariff-accounts': 'DP accounts on a tariff plan', 'dp-amc-paid-ahead': 'DP accounts with maintenance paid ahead' };
// migBar is a progress bar: green while going and when done, red only if the load failed.
const migBar = (u, status) => `<div class="bar ${status === 'Failed' ? 'bad' : 'good'}" title="${pct(u)}"><i style="width:${Math.min(100, u * 100).toFixed(1)}%"></i></div>`;
async function migSha(buf) {
  if (!window.crypto?.subtle) return '';
  const h = await crypto.subtle.digest('SHA-256', buf);
  return [...new Uint8Array(h)].map(b => b.toString(16).padStart(2, '0')).join('');
}
async function migUpload(file) {
  const box = $('mig-up'), cs = C.migrate.chunk_size;
  const say = (t, k) => { if (box) box.innerHTML = `<div class="note ${k || ''}">${t}</div>`; };
  try {
    let saved = {}; try { saved = JSON.parse(localStorage.getItem(MIG_LS) || '{}'); } catch (e) {}
    let id = '';
    const same = C.migrate.packages.find(p => p.status === 'Uploading' && p.name === file.name && p.size === file.size);
    if (same && (saved.id === same.id || !saved.id)) id = same.id;
    if (!id) { const r = await post('/v1/migration/uploads', { name: file.name, size: file.size, chunk_size: cs }); id = r.id; }
    localStorage.setItem(MIG_LS, JSON.stringify({ id, name: file.name, size: file.size }));
    const det = await get('/v1/migration/packages/' + encodeURIComponent(id));
    const chunk = det.package.chunk_size, total = det.package.chunks;
    let left = det.missing.slice(), sent = total - left.length;
    for (const n of left) {
      const buf = await file.slice(n * chunk, Math.min((n + 1) * chunk, file.size)).arrayBuffer();
      const sha = await migSha(buf);
      for (let tries = 0; ; tries++) {
        try {
          const res = await fetch(`/v1/migration/uploads/${encodeURIComponent(id)}/chunks/${n}`, { method: 'PUT', headers: { 'X-Requested-With': 'ledgerline', 'Content-Type': 'application/octet-stream', ...(sha ? { 'X-Chunk-SHA256': sha } : {}) }, body: buf });
          if (!res.ok) { let m = 'HTTP ' + res.status; try { m = (await res.json()).error.message; } catch (e) {} throw new Error(m); }
          break;
        } catch (e) { if (tries >= 3) throw e; await new Promise(r => setTimeout(r, 1500 * (tries + 1))); }
      }
      sent++;
      say(`Uploading <b>${esc(file.name)}</b>: ${nf(sent)} of ${nf(total)} chunks (${nf(100 * sent / total, 0)}%). If the connection drops, choose the same file again and the upload carries on.<div style="margin-top:6px">${migBar(sent / total)}</div>`);
    }
    await post(`/v1/migration/uploads/${encodeURIComponent(id)}/complete`);
    localStorage.removeItem(MIG_LS);
    UI.migPkg = id; toast(file.name + ' uploaded; now check it'); render({ keep: true });
  } catch (e) { say(`<b>The upload stopped:</b> ${esc(e.message)}. Choose the same file again to carry on from where it stopped.`, 'bad'); toast(e.message, true); }
}
document.addEventListener('change', e => {
  const el = e.target;
  if (el.id === 'mig-file' && el.files.length) { migUpload(el.files[0]); el.value = ''; }
  if (el.id === 'h360-fy') { UI.h360.fy = el.value; h360Load(); }
  if (el.id === 'pold-fy') { UI.pOldFY = el.value; render({ keep: true }); }
});

const passFld = (id, label) => `<div class="fld"><label for="${id}">${label || 'Package passphrase'}</label><input class="in" id="${id}" type="password" autocomplete="off" spellcheck="false"><div class="dim small">Typed each time the package is opened. It is not stored or written anywhere.</div></div>`;

Object.assign(A, {
  migOpen: id => { UI.migPkg = id; UI.migErrOnly = ''; render({ keep: true }); },
  migClose: () => { UI.migPkg = ''; render({ keep: true }); },
  migErrFile: id => { UI.migErrOnly = id; $('page').innerHTML = groupBar(S.view) + V.migrate.html(C.migrate); bindPage(); },
  migCheck: id => {
    const p = C.migrate.packages.find(x => x.id === id), firms = p?.manifest?.firms || [];
    confirmModal('Check the package', 'Opens the package, checks every file\'s checksum and runs every go-live file through the same checks as a normal import, without saving anything. Every problem is listed at once.',
      passFld('mig-pass') + (firms.length > 1 ? `<div class="fld"><label for="mig-firm">Dry run for firm</label><select class="in" id="mig-firm">${firms.map(f => `<option value="${esc(f.code)}" ${p.check?.firm === f.code ? 'selected' : ''}>${esc(f.code)} · ${esc(f.name)}</option>`).join('')}</select></div>` : ''),
      'Check', () => { const pass = $('mig-pass').value; $('mig-pass').value = ''; UI.migPkg = id; return act(() => post(`/v1/migration/packages/${encodeURIComponent(id)}/check`, { passphrase: pass, firm: $('mig-firm')?.value || '' }), 'Checking: this takes a while for a large package'); });
  },
  migDiscard: id => confirmModal('Remove the package', 'Deletes the uploaded file from the server. A loaded migration keeps everything it loaded.', '', 'Remove', () => act(() => post(`/v1/migration/packages/${encodeURIComponent(id)}/discard`), 'Removed'), true),
  migPropose: id => {
    const p = C.migrate.packages.find(x => x.id === id); if (!p) return;
    const firms = p.manifest.firms, f0 = firms.find(f => f.code === p.check?.firm) || firms[0];
    // Every part's files of the firm, in load order.
    const seen = new Set();
    const files = C.migrate.packages.filter(q => q.key === p.key && q.manifest).flatMap(q => q.manifest.files.filter(f => f.firm === f0.code && ['master', 'history', 'documents'].includes(f.kind))).filter(f => !seen.has(f.path) && seen.add(f.path));
    const ord = C.migrate.order, rank = f => f.kind === 'master' ? (ord.indexOf(f.source) + 1 || 900) : f.kind === 'history' ? 1000 : 2000;
    files.sort((a, b) => rank(a) - rank(b) || String(a.fy).localeCompare(String(b.fy)) || String(a.source).localeCompare(String(b.source)));
    const errFiles = new Set(((C.migrate.pk?.package?.id === id ? C.migrate.pk.package.check?.files : p.check?.files) || []).filter(f => f.errors).map(f => f.path));
    const row = f => `<label class="check small"><input type="checkbox" class="mig-f" value="${esc(f.path)}" ${errFiles.has(f.path) && f.kind === 'master' ? '' : 'checked'}> ${esc({ master: 'Go-live', history: 'History', documents: 'Documents' }[f.kind])} · <span class="mono">${esc(f.source || '')}</span> ${esc(f.fy || '')} · ${nf(f.rows)} ${f.kind === 'documents' ? 'documents' : 'rows'}${errFiles.has(f.path) ? ' <span class="neg">has errors</span>' : ''}</label>`;
    wideModal(mhd('Propose loading firm ' + esc(f0.code) + ' · ' + esc(f0.name), 'A second person approves the load with the passphrase. Go-live files load first in dependency order, then the earlier years, then the documents.') + `<div class="bd">
      <div class="grid g2"><div class="fld"><label for="mp-asof">Balances as of</label><input class="in mono" id="mp-asof" type="date" value="${esc(f0.as_of || '')}"><div class="dim small">The old system's last closed day. Prices and positions load for the next business day.</div></div>
      <div class="fld"><label for="mp-note">Note for the approver</label><input class="in" id="mp-note" placeholder="Trial balance agreed with the old system"></div></div>
      <div class="fld"><label>Files to load</label><div style="max-height:320px;overflow:auto">${files.map(row).join('')}</div></div>
      <label class="check"><input type="checkbox" id="mp-notlive"> This firm is not live yet (it has test or trial entries that did not come from a migration)</label>
      <div id="mp-err"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="mp-ok">Propose</button></div>`);
    $('mp-ok').onclick = async () => {
      try { await post('/v1/migration/proposals', { package: id, firm: f0.code, files: [...document.querySelectorAll('.mig-f')].filter(x => x.checked).map(x => x.value), as_of: $('mp-asof').value, not_live: $('mp-notlive').checked, note: $('mp-note').value.trim() }); closeModal(); toast('Proposed: a second person approves it'); render({ keep: true }); }
      catch (e) { $('mp-err').innerHTML = errHTML(e); }
    };
  },
  migDecide: (id, el) => {
    const ok = el.dataset.a === '1';
    confirmModal(ok ? 'Approve and load' : 'Reject the proposal', ok ? 'Checks the firm is still not live, opens the package with the passphrase and loads it. You can follow the progress here; if the server restarts, resume with the passphrase.' : 'The proposal is closed. The package stays on the server.',
      (ok ? passFld('md-pass') : '') + `<div class="fld"><label for="md-note">${ok ? 'Note' : 'Reason'}</label><input class="in" id="md-note"></div>`, ok ? 'Approve and load' : 'Reject',
      () => { const pass = ok ? $('md-pass').value : ''; if (ok) $('md-pass').value = ''; return act(() => post(`/v1/migration/runs/${encodeURIComponent(id)}/decide`, { approve: ok, passphrase: pass, note: $('md-note').value.trim() }), ok ? 'Loading' : 'Rejected'); }, !ok);
  },
  migResume: id => confirmModal('Resume', 'Carries on from the first file not yet done.', passFld('mr-pass'), 'Resume', () => { const pass = $('mr-pass').value; $('mr-pass').value = ''; return act(() => post(`/v1/migration/runs/${encodeURIComponent(id)}/resume`, { passphrase: pass }), 'Resumed'); }),
  migRecon: id => act(() => post(`/v1/migration/runs/${encodeURIComponent(id)}/reconcile`), m => `${m.recon.passed} passed, ${m.recon.failed} failed`),
  migUndo: id => confirmModal('Undo the migration', 'Allowed only while nothing else has been posted since the load. A second person approves it; then the opening entries are reversed and the earlier years and archived documents are taken out. Clients and other go-live records stay and are replaced by a new load.',
    '<div class="fld"><label for="mu-why">Why</label><input class="in" id="mu-why" placeholder="Loaded with the wrong as-of date"></div>', 'Ask for undo', () => act(() => post(`/v1/migration/runs/${encodeURIComponent(id)}/undo`, { reason: $('mu-why').value.trim() }), 'Undo asked for: a second person approves it'), true),
  migUndoDecide: (id, el) => { const ok = el.dataset.a === '1'; confirmModal(ok ? 'Approve undo' : 'Keep the migration', ok ? 'Reverses the opening entries and takes out the history and archived documents of this migration.' : 'The migration stays as loaded.', `<div class="fld"><label for="mud-note">${ok ? 'Note' : 'Reason'}</label><input class="in" id="mud-note"></div>`, ok ? 'Undo now' : 'Keep', () => act(() => post(`/v1/migration/runs/${encodeURIComponent(id)}/undo/decide`, { approve: ok, note: $('mud-note').value.trim() }), ok ? 'Undone' : 'Kept'), ok); },
});

/* ---------- the old documents archive ---------- */
const arcCan = () => has('Ops maker', 'Ops checker', 'Risk manager', 'Compliance officer');
V.archive = {
  load: async () => {
    if (!arcCan()) return { docs: [], types: [], total: 0, forbidden: true };
    const q = UI.arcQ, qs = Object.entries(q).filter(([, v]) => v).map(([k, v]) => k + '=' + encodeURIComponent(v)).join('&');
    return get('/v1/archive?limit=300' + (qs ? '&' + qs : ''));
  },
  html: d => {
    const q = UI.arcQ, comp = has('Compliance officer');
    if (d.forbidden) return ph('Old documents archive', '') + '<div class="note">The archive is open to Ops, Risk and Compliance.</div>';
    const rows = d.docs.map(x => `<tr><td class="n">${fmtDate(x.date)}</td><td>${x.ucc ? `<button class="linkish mono" data-act="client" data-id="${esc(x.ucc)}">${esc(x.ucc)}</button>` : '<span class="dim">firm</span>'}</td><td>${esc(x.type)}</td><td class="small wrap">${esc(x.subject || '')}${x.to ? `<div class="dim">to ${esc(x.to)}</div>` : ''}</td><td>${esc(x.fy)}</td><td>${x.signed ? pill('Signed', 'good') : '<span class="dim">no</span>'}</td><td class="mono small">${esc(x.format)} · ${migSize(x.bytes)}</td><td class="n small">${fmtDate(x.retain_until)}${x.legal_hold ? ' ' + pill('Legal hold', 'bad') : ''}${x.deleted ? ' ' + pill('Deleted', 'nt') : ''}</td>
      <td>${x.deleted ? '' : `<button class="btn sm" data-act="dl" data-id="/v1/archive/${encodeURIComponent(x.ucc || '_')}/${encodeURIComponent(x.id)}" data-name="${esc(x.id)}.${esc(x.format)}">Download</button>`}${comp && !x.deleted ? `<button class="btn sm" data-act="arcHold" data-id="${esc(x.ucc || '_')}|${esc(x.id)}" data-on="${x.legal_hold ? 0 : 1}">${x.legal_hold ? 'Release hold' : 'Legal hold'}</button><button class="btn sm" data-act="arcDel" data-id="${esc(x.ucc || '_')}|${esc(x.id)}">Delete</button>` : ''}</td></tr>`);
    return ph('Old documents archive', 'Contract notes, statements, DP bills and other documents brought across from the old back office, as they were sent. Each is kept until its retention date (from the package, or eight years from its date); a legal hold stops deletion altogether. Every download is in the audit trail.') +
      `${d.label ? migLabel(d.label) : ''}<div class="panel"><div class="hd"><div class="tools">
        <input class="in mono" id="arc-ucc" placeholder="Client code" value="${esc(q.ucc)}" style="max-width:140px">
        <select class="in" id="arc-type"><option value="">Every type</option>${d.types.map(t => `<option ${q.type === t ? 'selected' : ''}>${esc(t)}</option>`).join('')}</select>
        <input class="in mono" type="date" id="arc-from" value="${esc(q.from)}" aria-label="From"><input class="in mono" type="date" id="arc-to" value="${esc(q.to)}" aria-label="To">
        <select class="in" id="arc-signed"><option value="">Signed or not</option><option value="Y" ${q.signed === 'Y' ? 'selected' : ''}>Signed</option><option value="N" ${q.signed === 'N' ? 'selected' : ''}>Not signed</option></select>
        <button class="btn pri" data-act="arcSearch">Search</button></div><small>${nf(d.total)} documents in the archive</small></div>
        ${tbl([['Date'], ['Client'], ['Type'], ['Subject'], ['Year'], ['Signed'], ['Format'], ['Keep until'], ['']], rows, { tall: true, empty: d.total ? 'No document matches.' : 'Nothing archived yet: documents come with a migration.' })}
        ${d.more ? '<div class="bd dim small">More documents match: narrow the search (a client, a type or dates).</div>' : ''}</div>`;
  }
};
Object.assign(A, {
  arcSearch: () => { UI.arcQ = { ucc: $('arc-ucc').value.trim().toUpperCase(), type: $('arc-type').value, from: $('arc-from').value, to: $('arc-to').value, signed: $('arc-signed').value }; render({ keep: true }); },
  arcHold: (id, el) => { const [u, d] = id.split('|'), on = el.dataset.on === '1'; confirmModal(on ? 'Legal hold' : 'Release the hold', on ? 'While on hold the document cannot be deleted at all, whatever its retention date.' : 'The document can then be deleted once its retention date has passed.', '<div class="fld"><label for="ah-why">Matter, notice or order</label><input class="in" id="ah-why"></div>', on ? 'Hold' : 'Release', () => act(() => post(`/v1/archive/${encodeURIComponent(u)}/${encodeURIComponent(d)}/hold`, { on, reason: $('ah-why').value.trim() }), on ? 'On legal hold' : 'Hold released')); },
  arcDel: id => { const [u, d] = id.split('|'); confirmModal('Delete the document', 'Allowed only after its retention date and never while on legal hold. It is then no longer shown or served to anyone.', '<div class="fld"><label for="ad-why">Why</label><input class="in" id="ad-why"></div>', 'Delete', () => act(() => post(`/v1/archive/${encodeURIComponent(u)}/${encodeURIComponent(d)}/delete`, { reason: $('ad-why').value.trim() }), 'Deleted'), true); },
});

/* ---------- client screen: Before Ledgerline ---------- */
const H360_COLS = {
  BILLS: [['BillNo', 'Bill'], ['BillDate', 'Date', 'd'], ['Exchange', 'Exch'], ['Segment', 'Seg'], ['Settlement', 'Settlement'], ['Turnover', 'Turnover', 'r'], ['Brokerage', 'Brokerage', 'r'], ['STT', 'STT', 'r'], ['GST', 'GST', 'r'], ['NetAmount', 'Net', 'r']],
  TRADES: [['TradeDate', 'Date', 'd'], ['Exchange', 'Exch'], ['Symbol', 'Scrip'], ['Instrument', 'Contract'], ['Side', 'B/S'], ['Quantity', 'Qty', 'q'], ['Price', 'Price', 'r'], ['Brokerage', 'Brokerage', 'r'], ['BillNo', 'Bill']],
  'CONTRACT-NOTES': [['CNNo', 'Contract note'], ['Date', 'Date', 'd'], ['Exchange', 'Exch'], ['Segment', 'Seg'], ['SentOn', 'Sent', 'd'], ['SentTo', 'To'], ['Signed', 'Signed'], ['DocumentID', 'Archived']],
  'RECEIPTS-PAYMENTS': [['Date', 'Date', 'd'], ['Type', 'Type'], ['VoucherNo', 'Voucher'], ['Amount', 'Amount', 'r'], ['Mode', 'Mode'], ['Bank', 'Bank'], ['ChequeNo', 'Cheque'], ['Narration', 'Narration']],
  'DP-TRANSACTIONS': [['Date', 'Date', 'd'], ['BOID', 'BO ID'], ['ISIN', 'ISIN'], ['Security', 'Security'], ['TxnType', 'Type'], ['Quantity', 'Qty', 'q'], ['Counterparty', 'Counterparty'], ['Narration', 'Narration']],
  'DP-BILLS': [['BillNo', 'Bill'], ['BillDate', 'Date', 'd'], ['PeriodFrom', 'From', 'd'], ['PeriodTo', 'To', 'd'], ['Amount', 'Amount', 'r'], ['GST', 'GST', 'r'], ['Total', 'Total', 'r']],
  'DP-HOLDINGS-YEAR-END': [['Date', 'As on', 'd'], ['ISIN', 'ISIN'], ['Security', 'Security'], ['Free', 'Free', 'q'], ['Pledged', 'Pledged', 'q'], ['Locked', 'Locked', 'q'], ['Total', 'Total', 'q'], ['Rate', 'Rate', 'r'], ['Value', 'Value', 'r']],
};
function histTable(t, archBase) {
  const spec = H360_COLS[t.kind] || t.columns.filter(c => c !== 'SourceRef').map(c => [c, c]);
  const ix = Object.fromEntries(t.columns.map((c, i) => [c, i]));
  const cell = (r, [c, , k]) => { const v = r[ix[c]] ?? ''; if (k === 'd') return `<td class="n">${fmtDate(v)}</td>`; if (k === 'r') return `<td class="r">${v === '' ? '' : nf(v, 2)}</td>`; if (k === 'q') return `<td class="r">${v === '' ? '' : nf(v)}</td>`;
    if (c === 'DocumentID' && v && archBase) return `<td><button class="btn sm" data-act="dl" data-id="${archBase}/${encodeURIComponent(v)}" data-name="${esc(v)}.pdf">Open</button></td>`; return `<td class="small wrap">${esc(v)}</td>`; };
  return tbl(spec.map(([, l, k]) => [l, k === 'r' || k === 'q' ? 'r' : '']), t.rows.map(r => `<tr>${spec.map(s => cell(r, s)).join('')}</tr>`), { tall: true, empty: 'Nothing for this year' });
}
function histStatementHTML(s) {
  return `<div class="kv"><div><small>Opening balance</small><b class="mono">${drcr(s.opening)}</b></div><div><small>Debits</small><b class="mono">${nf(s.debit, 2)}</b></div><div><small>Credits</small><b class="mono">${nf(s.credit, 2)}</b></div><div><small>Closing balance</small><b class="mono">${drcr(s.closing)}</b></div></div>` +
    tbl([['Date'], ['Voucher'], ['Particulars'], ['Debit', 'r'], ['Credit', 'r'], ['Balance', 'r']], [`<tr><td class="n dim">${esc(s.fy.slice(0, 4))}-04-01</td><td></td><td class="dim">Opening balance</td><td></td><td></td><td class="r">${drcr(s.opening)}</td></tr>`].concat(s.lines.map(l => `<tr><td class="n">${fmtDate(l.date)}</td><td class="small">${esc(l.voucher_type)} ${esc(l.voucher_no)}</td><td class="wrap">${l.segment ? `<span class="tag">${esc(l.segment)}</span> ` : ''}${esc(l.narration)}${l.cheque_no ? ` <span class="dim small">chq ${esc(l.cheque_no)}</span>` : ''}</td><td class="r">${num(l.debit) ? nf(l.debit, 2) : ''}</td><td class="r">${num(l.credit) ? nf(l.credit, 2) : ''}</td><td class="r">${drcr(l.balance)}</td></tr>`)), { tall: true });
}
let H360 = { ucc: '', idx: null };
async function migClient360(ucc) {
  const el = $('mig-360'); if (!el) return;
  const idx = await get(`/v1/history/${encodeURIComponent(ucc)}`).catch(() => null);
  if (!idx || (!idx.years.length && !idx.archived)) return;
  H360 = { ucc, idx };
  if (!idx.years.some(y => y.fy === UI.h360.fy)) UI.h360.fy = idx.years[0]?.fy || '';
  el.innerHTML = `<div><h3 style="margin:0 0 8px;font-family:var(--f-cond);font-size:15px">Before Ledgerline</h3>${migLabel(idx.label)}
    <div class="tools">${idx.years.length ? `<select class="in sm" id="h360-fy" aria-label="Financial year">${idx.years.map(y => `<option ${y.fy === UI.h360.fy ? 'selected' : ''}>${esc(y.fy)}</option>`).join('')}</select>` : ''}<div class="seg" id="h360-tabs"></div></div><div id="h360-body"></div></div>`;
  h360Load();
}
async function h360Load() {
  const { ucc, idx } = H360, y = idx.years.find(x => x.fy === UI.h360.fy) || { kinds: [] };
  const kinds = y.kinds.concat(idx.archived ? ['ARCHIVE'] : []);
  if (!kinds.includes(UI.h360.kind)) UI.h360.kind = kinds[0] || 'ARCHIVE';
  const names = Object.fromEntries(idx.kinds.map(k => [k.kind, k.title]));
  names.ARCHIVE = `Archive · ${nf(idx.archived)}`;
  const tabs = $('h360-tabs'), body = $('h360-body'); if (!tabs || !body) return;
  tabs.innerHTML = kinds.map(k => `<button class="${UI.h360.kind === k ? 'on' : ''}" data-act="h360Kind" data-id="${k}">${esc(names[k] || k)}</button>`).join('');
  body.innerHTML = '<p class="loading">Loading…</p>';
  const u = encodeURIComponent(ucc), fy = encodeURIComponent(UI.h360.fy);
  try {
    if (UI.h360.kind === 'ARCHIVE') {
      const r = await get(`/v1/archive?ucc=${u}&limit=500`);
      body.innerHTML = tbl([['Date'], ['Type'], ['Subject'], ['Year'], ['Signed'], ['Keep until'], ['']], r.docs.map(x => `<tr><td class="n">${fmtDate(x.date)}</td><td>${esc(x.type)}</td><td class="small wrap">${esc(x.subject || '')}</td><td>${esc(x.fy)}</td><td>${x.signed ? pill('Signed', 'good') : ''}</td><td class="n small">${fmtDate(x.retain_until)}${x.legal_hold ? ' ' + pill('Hold', 'bad') : ''}</td><td>${x.deleted ? pill('Deleted', 'nt') : `<button class="btn sm" data-act="dl" data-id="/v1/archive/${u}/${encodeURIComponent(x.id)}" data-name="${esc(x.id)}.${esc(x.format)}">Download</button>`}</td></tr>`), { tall: true, empty: 'No archived documents' });
    } else if (UI.h360.kind === 'LEDGER') body.innerHTML = histStatementHTML(await get(`/v1/history/${u}/${fy}/statement`));
    else body.innerHTML = histTable(await get(`/v1/history/${u}/${fy}/${encodeURIComponent(UI.h360.kind)}`), `/v1/archive/${u}`);
  } catch (e) { body.innerHTML = e.status === 403 ? '<div class="dim small">The archive is open to Ops, Risk and Compliance.</div>' : errHTML(e); }
}
A.h360Kind = id => { UI.h360.kind = id; h360Load(); };

/* ---------- client portal: Earlier years ---------- */
V.pold = {
  load: async () => {
    const v = await portalLoad(), u = encodeURIComponent(v.account.ucc);
    const [idx, arc] = await Promise.all([get(`/v1/portal/history/${u}`), get(`/v1/portal/archive/${u}`)]);
    if (!idx.years.some(y => y.fy === UI.pOldFY)) UI.pOldFY = idx.years[0]?.fy || '';
    const y = idx.years.find(x => x.fy === UI.pOldFY) || { kinds: [] };
    const [st, cn] = await Promise.all([y.kinds.includes('LEDGER') ? get(`/v1/portal/history/${u}/${encodeURIComponent(UI.pOldFY)}/statement`) : null, y.kinds.includes('CONTRACT-NOTES') ? get(`/v1/portal/history/${u}/${encodeURIComponent(UI.pOldFY)}/CONTRACT-NOTES`) : null]);
    return { ...v, idx, arc, st, cn };
  },
  html: v => {
    const u = encodeURIComponent(v.account.ucc), none = !v.idx.years.length && !v.arc.docs.length;
    return pHead(v, 'Earlier years', `Your account as kept by ${esc(v.firm.name)}'s previous back office, before it moved to Ledgerline: the ledger and contract notes of earlier years, and the documents sent to you then.`) +
      (none ? '<div class="note">There are no earlier years for this account.</div>' : migLabel(v.idx.label || v.arc.label) +
      (v.idx.years.length ? `<div class="panel"><div class="hd"><h3>Ledger</h3><div class="fld" style="min-width:160px"><label for="pold-fy">Financial year</label><select class="in" id="pold-fy">${v.idx.years.map(y => `<option ${y.fy === UI.pOldFY ? 'selected' : ''}>${esc(y.fy)}</option>`).join('')}</select></div></div><div class="bd">${v.st ? histStatementHTML(v.st) : '<p class="dim">No ledger for this year.</p>'}</div></div>
        <div class="panel"><div class="hd"><h3>Contract notes ${esc(UI.pOldFY)}</h3></div>${v.cn ? histTable(v.cn, null) : '<div class="bd dim">No contract notes for this year.</div>'}</div>` : '') +
      `<div class="panel"><div class="hd"><h3>Old documents</h3><small>${nf(v.arc.docs.length)} documents</small></div>${tbl([['Date'], ['Document'], ['Year'], ['Signed'], ['']], v.arc.docs.map(x => `<tr><td class="n">${fmtDate(x.date)}</td><td>${esc(x.type)}${x.subject ? `<div class="dim small">${esc(x.subject)}</div>` : ''}</td><td>${esc(x.fy)}</td><td>${x.signed ? pill('Signed', 'good') : ''}</td><td><button class="btn sm" data-act="dl" data-id="/v1/portal/archive/${u}/${encodeURIComponent(x.id)}" data-name="${esc(x.id)}.${esc(x.format)}">Download</button></td></tr>`), { tall: true, empty: 'No old documents.' })}</div>`);
  }
};
