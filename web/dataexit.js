'use strict';
/* Ledgerline console: Admin → Data & exit (screen "dataexit"). A full export
   of the firm's data (an Admin asks, a second person approves, an Admin
   downloads through a link that works once), and closing the account:
   ask, second approval, cooling-off, final export, download, delete, and the
   destruction certificate. Loaded after app.js. */

const DX_ST = { 'Waiting for approval': 'warn', Building: 'acc', Ready: 'good', Failed: 'bad', Rejected: 'nt', Deleted: 'nt' };
const EX_ST = { Requested: 'warn', Approved: 'bad', Cancelled: 'nt', Purged: 'nt' };
const dxWhen = t => t ? esc(fmtTS(t)) : '—';
const dxBytes = n => !n ? '—' : n >= 1073741824 ? nf(n / 1073741824, 1) + ' GB' : n >= 1048576 ? nf(n / 1048576, 1) + ' MB' : nf(Math.max(1, n / 1024)) + ' KB';
function dxLeft(until) {
  const ms = new Date(until) - Date.now();
  if (ms <= 0) return 'ended';
  const d = Math.floor(ms / 86400000), h = Math.floor(ms % 86400000 / 3600000), m = Math.floor(ms % 3600000 / 60000);
  return (d ? d + ' day' + (d > 1 ? 's ' : ' ') : '') + h + ' h ' + m + ' min left';
}

Object.assign(UI, { dxTab: 'export' });

V.dataexit = {
  // The archive tab's data comes with the screen (archive.js).
  load: async () => {
    const d = await get('/v1/data-exit');
    try { d.arc = await get('/v1/period-archives'); } catch (e) { d.arc = null; d.arcErr = e.message; }
    return d;
  },
  html: d => {
    const firm = S.me?.tenant || '', tab = UI.dxTab;
    const tabs = [['export', 'Export & exit'], ['archive', 'Archive']];
    return ph('Data & exit', 'Take a full copy of all your firm’s data at any time, move closed years to cheaper archive storage, or close your Ledgerline account and have your data deleted with a certificate. Each needs two people: an Admin asks, the compliance officer or another Admin approves.') +
      `<div class="gtabs" role="tablist">${tabs.map(([k, l]) => `<button role="tab" class="${tab === k ? 'on' : ''}" aria-selected="${tab === k}" data-act="dxTab" data-id="${k}">${l}</button>`).join('')}</div>` +
      (tab === 'archive' ? arcTab(d) :
        (d.available ? '' : `<div class="note bad">${esc(d.problem || '')}</div>`) +
        (d.signing_note ? `<div class="note warn">${esc(d.signing_note)}</div>` : '') +
        dxExports(d) + dxExit(d, firm));
  }
};

function dxExports(d) {
  const me = S.me?.actor;
  const rows = d.exports.map(x => {
    const canApprove = x.state === 'Waiting for approval' && has('Compliance officer') && x.requested_by !== me;
    const acts = [canApprove ? `<button class="btn sm pri" data-act="dxDecide" data-id="${esc(x.id)}">Approve or reject</button>` : '',
      x.state === 'Ready' && has() ? `<button class="btn sm pri" data-act="dxDownload" data-id="${esc(x.id)}">Download</button>` : ''].join('');
    const detail = x.state === 'Ready' ? `<div class="dim small wrap">Manifest SHA-256 <span class="mono">${esc(x.manifest_sha256)}</span><br>${esc(x.signed_with || '')}${x.public_key ? ` · public key <span class="mono">${esc(x.public_key)}</span>` : ''}</div>` : '';
    return `<tr><td class="mono small">${esc(x.id)}${x.final ? '<div>' + pill('Final export', 'bad') + '</div>' : ''}</td>
      <td>${pill(x.state, DX_ST[x.state] || 'nt')}${x.error ? `<div class="small neg wrap">${esc(x.error)}</div>` : ''}${x.note ? `<div class="dim small wrap">${esc(x.note)}</div>` : ''}</td>
      <td class="small">${esc(x.requested_by)}<div class="dim">${dxWhen(x.requested_at)}</div>${x.reason ? `<div class="dim wrap">${esc(x.reason)}</div>` : ''}</td>
      <td class="small">${esc(x.approved_by || x.rejected_by || '—')}</td>
      <td class="r">${dxBytes(x.size)}</td><td class="r">${x.documents ? nf(x.documents) : '—'}<div class="dim small">${x.stored_files ? nf(x.stored_files) + ' files' : ''}</div></td>
      <td class="small">${x.keep_until ? esc(fmtDate(x.keep_until.slice(0, 10))) : '—'}<div class="dim">${(x.downloads || []).length ? 'downloaded ' + x.downloads.length + '×' : ''}</div></td>
      <td>${acts}${detail}</td></tr>`;
  });
  return `<div class="panel"><div class="hd"><h3>Export all our data</h3>${has() ? '<button class="btn pri" data-act="dxRequest">Export all our data</button>' : ''}</div><div class="bd">
    <p class="small" style="margin-top:0">One ZIP with every record Ledgerline holds for the firm: every collection as JSON Lines, decrypted; every stored file (KYC documents, exchange, depository and bank files, contract notes, reports) as it was received; spreadsheet copies of clients, ledger, trades, holdings, bills, receipts, payments and DP transactions; the audit trail with its seals; and a signed manifest with the size and SHA-256 of every file. README.txt in the ZIP says how to check it.</p>
    <p class="dim small">It is built in the background and kept on the server, encrypted, for ${d.keep_days} days. Only an Admin downloads it. Each download link works once, for 15 minutes, and every download is in the audit trail.</p></div>
    ${tbl([['Export'], ['State'], ['Asked by'], ['Approved by'], ['Size', 'r'], ['Documents', 'r'], ['Kept until'], ['']], rows, { empty: 'No export yet.' })}</div>`;
}

function dxExit(d, firm) {
  const r = d.exit, me = S.me?.actor;
  const steps = `<ol class="small" style="margin:8px 0 0 18px;padding:0">${d.steps.map(s => `<li style="margin:3px 0">${s.state === 'done' ? '✓ ' : ''}${s.state === 'now' ? '<b>' + esc(s.label) + '</b>' : `<span class="${s.state === 'todo' ? 'dim' : ''}">${esc(s.label)}</span>`}${s.note ? ` <span class="dim">· ${esc(s.note)}</span>` : ''}</li>`).join('')}</ol>`;
  let body = '';
  if (!r) {
    body = `<div class="note bad"><b>Closing the account deletes all of the firm’s data from Ledgerline.</b> Clients, ledgers, trades, documents, files, history and the audit trail are deleted for good once the steps below are done. Keep the final export: it is the only copy you will have.</div>
      ${steps}
      <div class="form" style="margin-top:12px"><div class="fld"><label for="dx-cool">Cooling-off period (days)</label><input class="in sm" id="dx-cool" type="number" min="1" max="90" value="${d.cooling_off_days}" ${has() ? '' : 'disabled'}><div class="dim small" style="margin-top:4px">Time to change your mind after the second approval. Either person can cancel until the data is deleted.</div></div></div>
      ${has() ? '<div class="split" style="margin-top:10px"><button class="btn" data-act="dxCool">Save cooling-off period</button><button class="btn bad" data-act="dxClose">Close our Ledgerline account…</button></div>' : '<p class="dim small">Only an Admin asks to close the account.</p>'}`;
  } else {
    const cooling = r.state === 'Approved' && new Date(r.cooling_off_ends) > new Date(d.now);
    const ready = r.state === 'Approved' && !cooling;
    const canDecide = r.state === 'Requested' && has('Compliance officer') && r.requested_by !== me;
    const fx = d.exports.find(x => x.id === r.export_id);
    body = `<div class="note bad"><b>${r.state === 'Requested' ? 'Closing the account was asked for' : cooling ? 'The account will be closed' : 'The account is being closed'}.</b> ${esc(r.requested_by)} asked on ${dxWhen(r.requested_at)}${r.approved_by ? `; ${esc(r.approved_by)} approved on ${dxWhen(r.approved_at)}` : ''}.
        ${cooling ? `<br>Cooling-off: <b id="dx-left" data-until="${esc(r.cooling_off_ends)}">${esc(dxLeft(r.cooling_off_ends))}</b> (ends ${dxWhen(r.cooling_off_ends)}). Until then the firm works as normal and the request can be cancelled.` : ''}
        ${ready ? '<br>The cooling-off has ended: Ledgerline has stopped the firm’s scheduled work and made the final export. Do not change anything now: changes are not in the export.' : ''}</div>
      <div class="kv"><div><small>Request</small><b class="mono">${esc(r.id)}</b></div><div><small>State</small><b>${pill(r.state, EX_ST[r.state] || 'nt')}</b></div><div><small>Final export</small><b>${fx ? esc(fx.id) + ' · ' + esc(fx.state) : '—'}</b></div><div><small>Downloaded</small><b>${r.export_downloaded_at ? dxWhen(r.export_downloaded_at) : 'not yet'}</b></div></div>
      ${steps}
      <div class="split" style="margin-top:12px">
        ${canDecide ? `<button class="btn pri" data-act="dxExitDecide" data-id="${esc(r.id)}">Approve or refuse</button>` : ''}
        ${ready && fx && fx.state === 'Ready' && has() ? `<button class="btn pri" data-act="dxDownload" data-id="${esc(fx.id)}">Download the final export</button>` : ''}
        ${ready && r.export_downloaded_at && has() ? `<button class="btn bad" data-act="dxPurge" data-id="${esc(r.id)}">Delete our data now…</button>` : ''}
        ${has('Compliance officer') ? `<button class="btn" data-act="dxCancel" data-id="${esc(r.id)}">Cancel closing the account</button>` : ''}
      </div>
      <p class="dim small" style="margin-bottom:0">After the data is deleted you get a destruction certificate: what was deleted, when, who approved, the final export’s manifest hash, and the date by which copies of backups held elsewhere age out (${esc(d.backups_gone_by)}). It is signed like your other documents${d.mail_set && d.compliance_email ? ' and emailed to ' + esc(d.compliance_email) : ''}.</p>`;
  }
  const past = d.past.length ? `<h4 style="margin:14px 0 6px">Earlier requests</h4>` + tbl([['Request'], ['State'], ['Asked by'], ['Closed by'], ['Note']], d.past.map(p => `<tr><td class="mono small">${esc(p.id)}</td><td>${pill(p.state, EX_ST[p.state] || 'nt')}</td><td class="small">${esc(p.requested_by)}<div class="dim">${dxWhen(p.requested_at)}</div></td><td class="small">${esc(p.cancelled_by || '')}<div class="dim">${dxWhen(p.cancelled_at)}</div></td><td class="small wrap dim">${esc((p.notes || []).join(' · '))}</td></tr>`)) : '';
  return `<div class="panel"><div class="hd"><h3>Close our Ledgerline account and delete our data</h3><small>SEBI cloud framework: data returned, then destroyed with a certificate</small></div><div class="bd">${body}${past}</div></div>`;
}

// The cooling-off countdown, while the screen is open.
setInterval(() => { const el = document.getElementById('dx-left'); if (el) el.textContent = dxLeft(el.dataset.until); }, 30000);

function dxSaveBlob(b64, name) {
  const bin = atob(b64), u8 = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i);
  const url = URL.createObjectURL(new Blob([u8], { type: 'application/pdf' }));
  const a = document.createElement('a'); a.href = url; a.download = name; document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 4000);
}

Object.assign(A, {
  dxTab: id => { UI.dxTab = id; render({ keep: true }); },
  dxRequest: () => confirmModal('Export all our data', 'Builds one ZIP of every record and file Ledgerline holds for the firm. The compliance officer or another Admin approves it first.',
    '<div class="fld"><label for="dx-why">Reason (recorded)</label><input class="in" id="dx-why" placeholder="yearly copy, auditor, moving to another system"></div>', 'Ask for the export',
    () => act(() => post('/v1/data-exports', { reason: $('dx-why').value.trim() }), 'Export asked for: waiting for a second person to approve')),
  dxDecide: id => {
    const x = C.dataexit.exports.find(e => e.id === id); if (!x) return;
    modal(mhd('Approve the full export', `${esc(x.requested_by)} asked on ${dxWhen(x.requested_at)}${x.reason ? ' · ' + esc(x.reason) : ''}`) + `<div class="bd">
      <p class="small">The export holds all of the firm’s client data, decrypted. Approve only if you know why it is needed.</p>
      <div class="fld"><label for="dx-dn">Note (needed to reject)</label><input class="in" id="dx-dn"></div><div id="dx-derr"></div></div>
      <div class="ft"><button class="btn" id="dx-rej">Reject</button><button class="btn pri" id="dx-ok">Approve and build</button></div>`);
    const go = approve => act(() => post(`/v1/data-exports/${encodeURIComponent(id)}/decide`, { approve, note: $('dx-dn').value.trim() }), approve ? 'Approved: the export is being built' : 'Rejected').catch(e => { $('dx-derr').innerHTML = errHTML(e); });
    $('dx-ok').onclick = () => go(true); $('dx-rej').onclick = () => go(false);
  },
  dxDownload: async id => {
    try { const l = await post(`/v1/data-exports/${encodeURIComponent(id)}/link`, {}); location.href = l.url; toast('Download started. The link works once.'); setTimeout(() => render({ keep: true }), 3000); }
    catch (e) { toast(e.message, true); }
  },
  dxCool: () => act(() => post('/v1/data-exit/settings', { cooling_off_days: +$('dx-cool').value }), 'Cooling-off period saved'),
  dxClose: () => {
    const firm = S.me?.tenant || '';
    confirmModal('Close our Ledgerline account', 'This starts deleting all of the firm’s data. It needs a second person’s approval and a cooling-off period, and can be cancelled until the data is deleted.',
      `<div class="note bad small">After the cooling-off, Ledgerline stops the firm’s scheduled work and makes the final export. Once an Admin has downloaded it, the data can be deleted. Deleted data cannot be brought back, and backups of it held elsewhere only age out.</div>
      <div class="fld"><label for="dx-cr">Reason</label><input class="in" id="dx-cr" placeholder="moving to another back office"></div>
      <div class="fld"><label for="dx-cc">Type the firm’s code <b class="mono">${esc(firm)}</b> to confirm</label><input class="in mono" id="dx-cc" autocomplete="off"></div>`, 'Ask to close the account',
      () => act(() => post('/v1/data-exit/request', { confirm: $('dx-cc').value.trim(), reason: $('dx-cr').value.trim() }), 'Asked: waiting for a second person to approve'), true);
  },
  dxExitDecide: id => {
    const r = C.dataexit.exit; if (!r) return;
    modal(mhd('Approve closing the account', `${esc(r.requested_by)} asked on ${dxWhen(r.requested_at)}`) + `<div class="bd">
      <div class="note bad small">Approving starts a cooling-off of ${r.cooling_off_days} days. After it, the firm’s data is exported one last time and then deleted from Ledgerline for good.</div>
      <div class="fld"><label for="dx-en">Note (needed to refuse)</label><input class="in" id="dx-en"></div><div id="dx-eerr"></div></div>
      <div class="ft"><button class="btn" id="dx-erej">Refuse</button><button class="btn bad" id="dx-eok">Approve closing the account</button></div>`);
    const go = approve => act(() => post(`/v1/data-exit/${encodeURIComponent(id)}/decide`, { approve, note: $('dx-en').value.trim() }), approve ? 'Approved: the cooling-off has started' : 'Refused').catch(e => { $('dx-eerr').innerHTML = errHTML(e); });
    $('dx-eok').onclick = () => go(true); $('dx-erej').onclick = () => go(false);
  },
  dxCancel: id => confirmModal('Cancel closing the account', 'The firm keeps its account and its data. Scheduled work starts again.',
    '<div class="fld"><label for="dx-cn">Note</label><input class="in" id="dx-cn"></div>', 'Cancel closing',
    () => act(() => post(`/v1/data-exit/${encodeURIComponent(id)}/cancel`, { note: $('dx-cn').value.trim() }), 'Closing the account was cancelled')),
  dxPurge: id => {
    const firm = S.me?.tenant || '';
    confirmModal('Delete our data now', 'This cannot be undone.',
      `<div class="note bad small"><b>Every record of the firm is deleted from Ledgerline now:</b> clients, ledgers, trades, documents, files, their history and the audit trail, and the firm’s backups and exports on this server. Everyone is signed out and no one can sign in again. Check that the final export opens and its signature verifies first.</div>
      <div class="fld"><label for="dx-pc">Type the firm’s code <b class="mono">${esc(firm)}</b> to delete</label><input class="in mono" id="dx-pc" autocomplete="off"></div>`, 'Delete all our data',
      async () => {
        const r = await post(`/v1/data-exit/${encodeURIComponent(id)}/purge`, { confirm: $('dx-pc').value.trim() });
        dxSaveBlob(r.certificate, `destruction-certificate-${r.exit.id}.pdf`);
        modal(mhd('Your data was deleted', 'The destruction certificate has been downloaded.') + `<div class="bd">
          <p class="small">Certificate SHA-256 <span class="mono">${esc(r.exit.certificate_sha256)}</span></p>
          <p class="small">Keep this link to download the certificate again (no sign-in): <span class="mono wrap">${esc(r.certificate_url)}</span></p>
          <p class="small dim">Email: ${esc(r.mailed)} · Copies of backups held elsewhere age out by: ${esc(r.exit.backups_gone_by)}</p></div>
          <div class="ft"><button class="btn pri" id="dx-done">Done</button></div>`);
        $('dx-done').onclick = () => location.reload();
        setTimeout(() => { modalLocked = true; });
      }, true);
  },
});

/* ---- Archive: closed periods moved to cheaper archive storage ----
   An Admin chooses financial years (or dates) and what to archive and sees
   a preview; the compliance officer or another Admin approves; Ledgerline
   writes the package, reads it back and verifies it, and only then removes
   the rows. Restoring takes the same two people. */

const ARC_ST = { Requested: 'warn', Approved: 'acc', Verified: 'acc', Archived: 'good', Rejected: 'nt', Failed: 'bad', 'Restore requested': 'warn', 'Restore approved': 'acc', Restored: 'nt' };
Object.assign(UI, { arcMode: 'fy', arcPrev: null });

function arcTab(d) {
  const a = d.arc, me = S.me?.actor;
  if (!a) return `<div class="note bad">${esc(d.arcErr || 'The archive cannot be shown.')}</div>`;
  const notes = (a.available ? '' : `<div class="note bad">${esc(a.problem || '')}</div>`) +
    (a.storage_ok ? '' : `<div class="note warn"><b>Archive storage is not set.</b> Packages go to ${esc(a.storage)} in the server’s work folder. Ask whoever runs the server to set LEDGERLINE_ARCHIVE_DIR to a separate, cheaper disk or to object storage mounted as a folder.</div>`);
  const kinds = a.kinds.map(k => `<label class="check small"><input type="checkbox" class="arc-k" value="${esc(k.kind)}" checked> ${esc(k.title)}</label>`).join('');
  const years = a.years.map(y => `<label class="check small"><input type="checkbox" class="arc-y" value="${esc(y)}"> ${esc(y)}</label>`).join(' ');
  const p = UI.arcPrev;
  const prev = !p ? '' : `<div class="panel" style="margin-top:10px"><div class="hd"><h3>Preview · ${esc(p.label)}</h3><small>${fmtDate(p.from)} to ${fmtDate(p.to)} · nothing has changed yet</small></div><div class="bd">
      ${p.blockers.length ? `<div class="note bad"><b>This period cannot be archived now.</b><ul>${p.blockers.map(b => `<li class="small">${esc(b)}</li>`).join('')}</ul></div>` : ''}
      ${tbl([['What'], ['Rows', 'r'], ['Size', 'r']], p.kinds.map(k => `<tr><td>${esc(k.title)}</td><td class="r">${nf(k.rows)}</td><td class="r">${dxBytes(k.bytes)}</td></tr>`).concat([`<tr><td><b>All</b></td><td class="r"><b>${nf(p.rows)}</b></td><td class="r"><b>${dxBytes(p.bytes)}</b></td></tr>`]))}
      <div class="kpis">${kpi('Freed from the live database', dxBytes(p.bytes), 'about')}${kpi('Estimated saving', inr(p.saving_per_month, 0) + ' a month', 'at the storage prices below')}${kpi('Kept in the archive until', fmtDate(p.retain_until), 'at least')}</div>
      ${p.kept.length ? `<div class="note warn small"><b>Stays live (needed by open items):</b><ul>${p.kept.map(x => `<li>${esc(x)}</li>`).join('')}</ul></div>` : ''}
      ${(p.notes || []).map(x => `<div class="dim small">${esc(x)}</div>`).join('')}
      <p class="small">Ledger entries of the period are replaced in the live books by opening balances carried forward on ${fmtDate(p.to)}, so every balance, statement and report after it stays exactly the same. The audit trail stays live.</p>
      ${has() && !p.blockers.length ? '<div class="split"><button class="btn pri" data-act="arcAsk">Ask to archive this period</button></div>' : ''}</div></div>`;
  const ask = has() && a.available ? `<div class="panel"><div class="hd"><h3>Archive a period</h3><small>never the current financial year (${esc(a.current_fy)})</small></div><div class="bd">
      <div class="seg"><button class="${UI.arcMode === 'fy' ? 'on' : ''}" data-act="arcMode" data-id="fy">Financial years</button><button class="${UI.arcMode === 'dates' ? 'on' : ''}" data-act="arcMode" data-id="dates">From and to dates</button></div>
      ${UI.arcMode === 'fy' ? `<div class="fld" style="margin-top:8px"><label>Financial years (one after another)</label><div>${years}</div></div>`
        : `<div class="grid g2" style="margin-top:8px"><div class="fld"><label for="arc-from">From</label><input class="in mono" id="arc-from" type="date"></div><div class="fld"><label for="arc-to">To</label><input class="in mono" id="arc-to" type="date"></div></div>`}
      <div class="fld"><label>What to archive</label><div>${kinds}</div></div>
      <div class="fld"><label for="arc-why">Reason (recorded)</label><input class="in" id="arc-why" placeholder="lower storage cost for closed years"></div>
      <div class="split"><button class="btn" data-act="arcPreview">Preview</button></div>${prev}</div></div>` : prev;
  const rows = a.archives.map(r => {
    const running = (a.running || []).includes(r.id);
    const canDecide = has('Compliance officer') && r.requested_by !== me;
    const acts = [
      r.state === 'Requested' && canDecide ? `<button class="btn sm pri" data-act="arcDecide" data-id="${esc(r.id)}">Approve or reject</button>` : '',
      r.state === 'Failed' && canDecide && !running ? `<button class="btn sm" data-act="arcDecide" data-id="${esc(r.id)}">Start again or reject</button>` : '',
      r.state === 'Archived' && has() ? `<button class="btn sm" data-act="arcRestore" data-id="${esc(r.id)}">Restore…</button>` : '',
      r.state === 'Restore requested' && has('Compliance officer') && r.restore_requested_by !== me ? `<button class="btn sm pri" data-act="arcRestoreDecide" data-id="${esc(r.id)}">Approve or refuse the restore</button>` : '',
      r.state === 'Restored' && has() ? `<button class="btn sm" data-act="arcAgain" data-id="${esc(r.id)}">Archive again</button>` : ''].join('');
    const freed = r.freed ? Object.values(r.freed).reduce((s, x) => s + x.bytes, 0) : Object.values(r.preview || {}).reduce((s, x) => s + x.bytes, 0);
    const pk = r.package_sha256 ? `<div class="dim small wrap">Package ${dxBytes(r.package_bytes)} · manifest SHA-256 <span class="mono">${esc(r.package_sha256)}</span><br>${esc(r.signed_with || '')}${r.public_key ? ` · public key <span class="mono">${esc(r.public_key)}</span>` : ''}${r.verified_at ? ' · verified ' + dxWhen(r.verified_at) : ''}</div>` : '';
    return `<tr><td class="mono small">${esc(r.id)}</td>
      <td><b>${esc(r.label)}</b><div class="dim small">${fmtDate(r.from)} to ${fmtDate(r.to)}</div><div class="dim small">${esc((r.kinds || []).length === a.kinds.length ? 'everything' : (r.kinds || []).join(', '))}</div></td>
      <td>${pill(running ? r.state + ' · working' : r.state, ARC_ST[r.state] || 'nt')}${r.error ? `<div class="small neg wrap">${esc(r.error)}</div>` : ''}${r.note ? `<div class="dim small wrap">${esc(r.note)}</div>` : ''}${r.archived_at ? `<div class="dim small">archived ${dxWhen(r.archived_at)}</div>` : ''}${r.restored_at ? `<div class="dim small">restored ${dxWhen(r.restored_at)}</div>` : ''}</td>
      <td class="small">${esc(r.requested_by)}<div class="dim">${dxWhen(r.requested_at)}</div>${r.reason ? `<div class="dim wrap">${esc(r.reason)}</div>` : ''}${r.restore_requested_by ? `<div>restore: ${esc(r.restore_requested_by)}${r.restore_reason ? ' · ' + esc(r.restore_reason) : ''}</div>` : ''}</td>
      <td class="small">${esc(r.approved_by || r.rejected_by || '—')}${r.restore_approved_by ? `<div>restore: ${esc(r.restore_approved_by)}</div>` : ''}</td>
      <td class="r">${dxBytes(freed)}<div class="dim small">${r.clients ? nf(r.clients) + ' clients' : ''}</div></td>
      <td class="small">${r.retain_until ? esc(fmtDate(r.retain_until)) : '—'}</td>
      <td>${acts}${pk}${(r.kept_why || []).length ? `<div class="dim small wrap">Kept live: ${esc(r.kept_why.join('; '))}</div>` : ''}</td></tr>`;
  });
  const list = `<div class="panel"><div class="hd"><h3>Archives</h3><small>${a.index.filter(x => x.state === 'Archived').length} periods archived</small></div>
    ${tbl([['Archive'], ['Period'], ['State'], ['Asked by'], ['Approved by'], ['Freed', 'r'], ['Keep until'], ['']], rows, { empty: 'No period archived yet.' })}
    <div class="bd"><p class="dim small" style="margin:0">Screens and reports that reach into an archived period say so and when it was archived; client statements start after it, with the archived years in the opening balance. Nothing can be posted into an archived period. A package cannot be deleted before its keep-until date; when the firm closes its account, its archives go into the final export and are deleted with the rest of its data.</p></div></div>`;
  const s = a.settings;
  const rules = `<div class="panel"><div class="hd"><h3>Archive rules</h3></div><div class="bd"><div class="grid g3">
      <div class="fld"><label for="arc-ret">Keep archives (years after the period)</label><input class="in sm" id="arc-ret" type="number" min="${a.retain_min}" max="50" value="${s.retain_years}" ${has() ? '' : 'disabled'}><div class="dim small">At least ${a.retain_min} (SEBI and PMLA record keeping).</div></div>
      <div class="fld"><label for="arc-live">Live database storage, ₹ per GB a month</label><input class="in sm" id="arc-live" type="number" min="0" step="0.5" value="${s.live_cost_per_gb}" ${has() ? '' : 'disabled'}></div>
      <div class="fld"><label for="arc-store">Archive storage, ₹ per GB a month</label><input class="in sm" id="arc-store" type="number" min="0" step="0.5" value="${s.archive_cost_per_gb}" ${has() ? '' : 'disabled'}></div></div>
      ${has() ? '<div class="split"><button class="btn" data-act="arcRules">Save the rules</button></div>' : ''}<div class="dim small">Archive storage: <span class="mono">${esc(a.storage)}</span></div></div></div>`;
  return notes + ask + list + rules;
}

function arcAskBody() {
  const kinds = [...document.querySelectorAll('.arc-k')].filter(x => x.checked).map(x => x.value);
  if (UI.arcMode === 'fy') return { fys: [...document.querySelectorAll('.arc-y')].filter(x => x.checked).map(x => x.value), kinds, reason: $('arc-why')?.value.trim() || '' };
  return { from: $('arc-from').value, to: $('arc-to').value, kinds, reason: $('arc-why')?.value.trim() || '' };
}

Object.assign(A, {
  arcMode: id => { UI.arcMode = id; UI.arcPrev = null; render({ keep: true }); },
  arcPreview: async () => {
    const body = arcAskBody();
    try { UI.arcPrev = await post('/v1/period-archives/preview', body); UI.arcAsk = body; render({ keep: true }); } catch (e) { toast(e.message, true); }
  },
  arcAsk: () => {
    const p = UI.arcPrev, body = UI.arcAsk; if (!p || !body) return;
    confirmModal('Ask to archive ' + p.label, 'The compliance officer or another Admin approves it. Ledgerline then writes the archive package, reads it back and verifies it, and only then removes the rows from the live data.',
      `<p class="small">${nf(p.rows)} rows (${dxBytes(p.bytes)}) of ${fmtDate(p.from)} to ${fmtDate(p.to)}, kept until at least ${fmtDate(p.retain_until)}.</p>`, 'Ask to archive',
      () => act(() => post('/v1/period-archives', body), () => { UI.arcPrev = null; return 'Asked: waiting for a second person to approve'; }));
  },
  arcDecide: id => {
    const r = C.dataexit.arc.archives.find(x => x.id === id); if (!r) return;
    modal(mhd((r.state === 'Failed' ? 'Start archiving again: ' : 'Approve archiving ') + esc(r.label), `${esc(r.requested_by)} asked on ${dxWhen(r.requested_at)}${r.reason ? ' · ' + esc(r.reason) : ''}`) + `<div class="bd">
      <p class="small">Ledgerline writes the period’s package to archive storage, reads it back and verifies it, and only then removes the rows from the live data. Ledger entries are replaced by opening balances carried forward, and every later balance is checked to be unchanged.</p>
      <div class="fld"><label for="arc-dn">Note (needed to reject)</label><input class="in" id="arc-dn"></div><div id="arc-derr"></div></div>
      <div class="ft"><button class="btn" id="arc-rej">Reject</button><button class="btn pri" id="arc-ok">${r.state === 'Failed' ? 'Start again' : 'Approve and archive'}</button></div>`);
    const go = approve => act(() => post(`/v1/period-archives/${encodeURIComponent(id)}/decide`, { approve, note: $('arc-dn').value.trim() }), approve ? 'Approved: archiving' : 'Rejected').catch(e => { $('arc-derr').innerHTML = errHTML(e); });
    $('arc-ok').onclick = () => go(true); $('arc-rej').onclick = () => go(false);
  },
  arcRestore: id => confirmModal('Restore the archived period', 'Puts the period’s rows back exactly as they were and removes the opening balances that stood in for them. The compliance officer or another Admin approves it.',
    '<div class="fld"><label for="arc-rr">Reason</label><input class="in" id="arc-rr" placeholder="inspection asked for the 2019-20 ledger"></div>', 'Ask to restore',
    () => act(() => post(`/v1/period-archives/${encodeURIComponent(id)}/restore`, { reason: $('arc-rr').value.trim() }), 'Asked: waiting for a second person to approve')),
  arcRestoreDecide: id => {
    const r = C.dataexit.arc.archives.find(x => x.id === id); if (!r) return;
    modal(mhd('Approve restoring ' + esc(r.label), `${esc(r.restore_requested_by)} asked on ${dxWhen(r.restore_requested_at)} · ${esc(r.restore_reason || '')}`) + `<div class="bd">
      <p class="small">The package is verified again, the rows go back with the same ids and bytes, and the balances are checked to be unchanged.</p>
      <div class="fld"><label for="arc-rn">Note (needed to refuse)</label><input class="in" id="arc-rn"></div><div id="arc-rerr"></div></div>
      <div class="ft"><button class="btn" id="arc-rrej">Keep it archived</button><button class="btn pri" id="arc-rok">Approve and restore</button></div>`);
    const go = approve => act(() => post(`/v1/period-archives/${encodeURIComponent(id)}/restore/decide`, { approve, note: $('arc-rn').value.trim() }), approve ? 'Approved: restoring' : 'Kept archived').catch(e => { $('arc-rerr').innerHTML = errHTML(e); });
    $('arc-rok').onclick = () => go(true); $('arc-rrej').onclick = () => go(false);
  },
  arcAgain: id => confirmModal('Archive the period again', 'Asks to archive the same period and kinds again. A second person approves it as before.', '', 'Ask to archive again',
    () => act(() => post(`/v1/period-archives/${encodeURIComponent(id)}/again`, {}), 'Asked: waiting for a second person to approve')),
  arcRules: () => act(() => post('/v1/period-archives/settings', { retain_years: +$('arc-ret').value, live_cost_per_gb: +$('arc-live').value, archive_cost_per_gb: +$('arc-store').value }), 'Archive rules saved'),
});
