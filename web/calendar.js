'use strict';
/* Ledgerline console: backups, and the exchange holiday calendar (a tab of
   Circulars & calendar). Loaded after app.js and uses its helpers. */

/* ============================== backups ============================== */
const fmtBytes = n => n >= 1 << 30 ? (n / (1 << 30)).toFixed(2) + ' GB' : n >= 1 << 20 ? (n / (1 << 20)).toFixed(1) + ' MB' : n >= 1024 ? Math.round(n / 1024) + ' KB' : (n || 0) + ' B';
const TIER_TONE = { daily: 'nt', weekly: 'acc', quarterly: 'warn', yearly: 'good', manual: 'nt' };
const tierPills = ts => (ts || []).map(t => pill(t[0].toUpperCase() + t.slice(1), TIER_TONE[t] || 'nt')).join(' ');
// Times are shown in IST, whatever zone the server wrote them in.
const fmtStamp = s => { if (!s) return '—'; const t = new Date(s); return isNaN(t) ? esc(s) : esc(t.toLocaleString('en-GB', { timeZone: 'Asia/Kolkata', day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit', hour12: false }).replace(',', '')); };
const days = hs => new Set((hs || []).map(h => h.date)).size;

V.backups = {
  load: () => get('/v1/backups'),
  html: d => {
    const adm = has('Admin'), k = d.keep;
    const lg = d.last_good;
    const head = ph('Backups', 'After each close (equity in the evening, commodity after the MCX session) the whole firm is backed up: clients, ledgers, trades, files, settings and the audit trail, encrypted with the firm’s data keys and read back to check it before it counts. The backup of the last trading day of a week, quarter or year is also kept as a weekly, quarterly or yearly backup, so it outlives the dailies around it. A part not closed by its fallback time is backed up anyway, and again once it is closed.',
      adm ? `${d.configured ? '<button class="btn" data-act="bkPolicy">Retention</button>' : ''}<button class="btn pri" data-act="bkNow" ${d.configured && !d.running ? '' : 'disabled'}>Back up now</button>` : '');
    const probs = d.problems.length ? `<div class="note ${d.configured ? 'bad' : 'warn'}">${d.problems.map(esc).join('<br>')}</div>` : '';
    if (!d.configured) return head + probs + bkRestoreHelp(d);
    const held = ['daily', 'weekly', 'quarterly', 'yearly'].map(t => `${d.counts[t] || 0} ${t}`).join(' · ');
    return head + probs +
      (d.running ? '<div class="note"><b>A backup is running.</b> This page updates when it finishes.</div>' : '') +
      `<div class="kpis">
        ${kpi('Last good backup', lg ? fmtDate(lg.date) : 'None', lg ? `taken ${fmtStamp(lg.created)} · ${fmtBytes(lg.size)} · ${nf(lg.total_rows)} rows` : 'not yet taken', lg ? 'good' : 'bad')}
        ${kpi('Next backup', fmtDate(d.next.date), (d.next.tiers || []).join(', ') + ` · after each close; if not closed, equity at ${esc(d.fallback)}, commodity at ${esc(d.fallback_commodity)} the next morning`)}
        ${kpi('Held', nf(d.backups.length), held)}
        ${kpi('Kept', `${k.daily}·${k.weekly}·${k.quarterly}·${k.yearly}`, `daily · weekly · quarterly · yearly; year ends ${d.year_end === 'march' ? '31 March' : '31 December'}`)}
      </div>
      <div class="panel"><div class="hd"><h3>Backups held</h3><small>${esc(d.location)} · ${esc(d.backend)} storage</small></div>${tbl([['Day'], ['Kept as'], ['Taken'], ['Rows', 'r'], ['Size', 'r'], ['Check'], ['']], d.backups.map(b => {
        const ok = b.verified && !b.verify_error;
        return `<tr><td class="n"><b>${fmtDate(b.date)}</b><div class="dim small mono">${esc(b.id)}</div></td><td>${tierPills(b.tiers)}</td><td class="small">${fmtStamp(b.created)}<div class="dim">${esc(b.trigger || '')}${b.by && b.by !== 'system:scheduler' ? ' · ' + esc(b.by) : ''}</div></td><td class="r mono">${nf(b.total_rows || 0)}</td><td class="r mono">${fmtBytes(b.size)}</td><td class="small">${ok ? pill('Checked', 'good') + `<div class="dim">${fmtStamp(b.verified)}</div>` : pill('Failed', 'bad') + `<div class="wrap" style="max-width:280px">${esc(b.verify_error || '')}</div>`}</td><td>${has('Compliance officer', 'Ops checker') ? `<button class="btn sm" data-act="bkVerify" data-id="${esc(b.id)}">Check again</button>` : ''}</td></tr>`;
      }), { tall: true, empty: 'No backups yet. The first is taken when a day is closed.' })}</div>
      <div class="panel"><div class="hd"><h3>Recent runs</h3><small>Failures are retried every 15 minutes, four times, and management is emailed</small></div>${tbl([['Run'], ['Day'], ['Started'], ['Took', 'r'], ['Result'], ['Rows', 'r'], ['Size', 'r'], ['Old removed', 'r']], d.runs.map(r => `<tr><td class="n dim">${esc(r.id)}</td><td class="n">${fmtDate(r.date)}<div>${tierPills(r.tiers)}</div></td><td class="small">${fmtStamp(r.started)}<div class="dim">${esc(r.trigger)}</div></td><td class="r mono">${r.ms >= 1000 ? (r.ms / 1000).toFixed(1) + ' s' : r.ms + ' ms'}</td><td class="small wrap" style="max-width:320px">${pill(r.status, r.status === 'Succeeded' ? 'good' : 'bad')} ${esc(r.error || '')}</td><td class="r mono">${r.rows ? nf(r.rows) : '—'}</td><td class="r mono">${r.size ? fmtBytes(r.size) : '—'}</td><td class="r mono">${r.pruned || '—'}</td></tr>`), { empty: 'No runs yet.' })}</div>` +
      bkRestoreHelp(d);
  }
};
function bkRestoreHelp(d) {
  return `<div class="panel"><div class="hd"><h3>Restoring</h3><small>Done by whoever runs the server</small></div><div class="bd small">
    <p style="margin-top:0">A backup restores only into a database where the firm has no data (for example a new server after a failure); it never overwrites. It needs the same data keys the backup was made with, so keep a copy of the keys somewhere other than the backups.</p>
    <pre class="mono" style="white-space:pre-wrap;margin:8px 0">ledgerline backup verify FILE.llbak                     # check a backup opens and is complete
LEDGERLINE_DB=postgres://OWNER@HOST/DB ledgerline backup restore FILE.llbak</pre>
    <p style="margin:0">Restore runs as the database’s owner, checks the whole file before writing anything, writes everything in one transaction, then checks the audit trail’s seals. Copy the backup folder off this server every day (to storage in an Indian region) so a lost server does not take the backups with it.</p></div></div>`;
}
let bkPoll = 0;
Object.assign(A, {
  bkNow: () => confirmModal('Back up now?', 'A manual backup of the whole firm, as it is now. It is kept apart from the daily, weekly, quarterly and yearly backups.', '', 'Back up now', async () => {
    await post('/v1/backups/run'); closeModal(); toast('Backup started'); bkWatch();
  }),
  bkVerify: id => act(() => post(`/v1/backups/${encodeURIComponent(id)}/verify`), 'Backup checked: it opens and is complete'),
  bkPolicy: () => {
    const d = C.backups, k = d.keep;
    const f = (id, l, v, min, hint) => `<div class="fld"><label for="bk-${id}">${l}</label><input class="in mono" type="number" id="bk-${id}" min="${min}" max="400" value="${v}"><div class="dim small">${hint}</div></div>`;
    modal(mhd('Backup retention', 'How many backups each kind keeps. A backup kept as several kinds stays while any of them keeps it.') + `<form class="bd" id="bkf"><div class="form">
      ${f('d', 'Daily', k.daily, 7, 'at least 7')}${f('w', 'Weekly', k.weekly, 4, 'at least 4')}${f('q', 'Quarterly', k.quarterly, 4, 'at least 4')}${f('y', 'Yearly', k.yearly, 5, 'at least 5; keep as many years as your records must be kept')}${f('m', 'Manual', k.manual, 1, 'backups taken by hand')}
      <div class="fld"><label for="bk-ye">Year ends</label><select class="in" id="bk-ye"><option value="march" ${d.year_end === 'march' ? 'selected' : ''}>31 March (financial year)</option><option value="december" ${d.year_end === 'december' ? 'selected' : ''}>31 December</option></select></div>
      <div class="fld"><label for="bk-fb">Back up equity not closed by (IST)</label><input class="in mono" id="bk-fb" value="${esc(d.fallback)}" pattern="[0-2][0-9]:[0-5][0-9]"></div>
      <div class="fld"><label for="bk-fc">Back up commodity not closed by (IST, next morning)</label><input class="in mono" id="bk-fc" value="${esc(d.fallback_commodity)}" pattern="0[0-8]:[0-5][0-9]"><div class="dim small">after MCX’s session, which ends 23:30 or 23:55</div></div>
    </div><div id="bk-err"></div></form><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" form="bkf">Save</button></div>`);
    $('bkf').onsubmit = async e => {
      e.preventDefault();
      const n = id => parseInt($('bk-' + id).value, 10) || 0;
      try { await api('PUT', '/v1/backups/policy', { keep: { daily: n('d'), weekly: n('w'), quarterly: n('q'), yearly: n('y'), manual: n('m') }, year_end: $('bk-ye').value, fallback: $('bk-fb').value.trim(), fallback_commodity: $('bk-fc').value.trim() }); closeModal(); toast('Retention saved'); render({ keep: true }); }
      catch (err) { $('bk-err').innerHTML = errHTML(err); }
    };
  },
});
// While a backup runs, refresh the screen every few seconds.
function bkWatch() {
  clearTimeout(bkPoll);
  bkPoll = setTimeout(async () => {
    if (S.view !== 'backups') return;
    await render({ keep: true });
    if (C.backups?.running) bkWatch(); else toast(C.backups?.runs?.[0]?.status === 'Succeeded' ? 'Backup finished and checked' : 'Backup failed: see Recent runs', C.backups?.runs?.[0]?.status !== 'Succeeded');
  }, 2000);
}

/* ============================== exchange holidays ============================== */
const EXCH = ['NSE', 'BSE', 'MCX'], SEGN = { CM: 'Equity', FO: 'F&O', CD: 'Currency', COM: 'Commodity' };
const wday = d => new Date(d + 'T00:00:00').toLocaleDateString('en-IN', { weekday: 'short' });

// One row per date: the holiday's name, and for each exchange which
// segments close for trading (T) and for settlement (S).
function holTable(hs, opts = {}) {
  const by = {};
  for (const h of hs || []) {
    const r = by[h.date] ||= { date: h.date, names: new Set(), ex: {}, notes: new Set() };
    if (h.name) r.names.add(h.name);
    if (h.note && !h.note.startsWith('as NSE')) r.notes.add(h.note);
    const e = r.ex[h.exchange] ||= { trading: [], settlement: [] };
    e[h.kind].push(h.segment);
  }
  const rows = Object.values(by).sort((a, b) => a.date.localeCompare(b.date));
  const today = S.date;
  const cell = e => e ? ['trading', 'settlement'].filter(k => e[k].length).map(k => `<span class="hol-k ${k}" title="${k === 'trading' ? 'No trading' : 'No settlement'}: ${e[k].map(s => SEGN[s] || s).join(', ')}">${k === 'trading' ? 'T' : 'S'} <span class="mono">${e[k].sort().join(' ')}</span></span>`).join(' ') : '<span class="dim">—</span>';
  return tbl([['Date'], ['Day'], ['Holiday'], ...EXCH.map(x => [x])], rows.map(r => `<tr class="${r.date < today ? 'dim' : ''} ${opts.mark?.[r.date] ? 'hol-' + opts.mark[r.date] : ''}"><td class="n"><b>${fmtDate(r.date)}</b></td><td class="small">${wday(r.date)}</td><td>${esc([...r.names].join(' / ') || '—')}${r.notes.size ? `<div class="dim small">${esc([...r.notes].join('; '))}</div>` : ''}</td>${EXCH.map(x => `<td class="small">${cell(r.ex[x])}</td>`).join('')}</tr>`), { tall: true, empty: opts.empty || 'No holidays.' });
}

function holidaysHTML(h) {
  if (!h) return '<div class="pad">The exchange holiday calendar could not be loaded.</div>';
  const years = h.years || [];
  if (!UI.holYear || !years.some(y => String(y.year) === UI.holYear)) UI.holYear = String((years.find(y => y.pending) || years[0] || {}).year || '');
  const y = years.find(x => String(x.year) === UI.holYear) || years[0];
  if (!y) return '';
  const canLoad = has('Ops maker', 'Ops checker', 'Compliance officer'), canDecide = has('Ops checker', 'Compliance officer');
  const a = y.approved, p = y.pending, me = S.me?.actor;
  const tabs = segBtns('holYear', years.map(x => [String(x.year), `${x.year}${x.pending ? ' · to approve' : x.approved ? '' : ' · none'}`]), 'ci-t');
  const tools = canLoad ? `${h.connected.length ? `<button class="btn sm" data-act="holCheck" data-id="${y.year}">Check ${esc(h.connected.join(', '))} now</button>` : ''}<button class="btn sm" data-act="holUpload" data-id="${y.year}">Upload a circular</button>` : '';
  const chk = y.check ? `<div class="small" style="margin-top:6px"><b>Last check</b> ${fmtStamp(y.check.at)}${y.check.by === 'system:scheduler' ? ' (automatic)' : ' by ' + esc(y.check.by)}: ${esc(y.check.result)}</div>` : '';
  const auto = h.connected.length ? `${esc(h.connected.join(', '))} lists are fetched every morning in December until next year’s is found, and every Monday for changes. BSE equity, F&amp;O and currency follow NSE’s lists (BSE publishes matching circulars). Upload MCX’s circular yourself.` : 'Fetching is off on this server: upload each exchange’s circular.';
  const warn = y.warnings.length ? `<div class="note warn" style="margin:0 0 12px">${y.warnings.map(esc).join('<br>')}</div>` : '';
  const mark = {};
  if (p) { p.added.forEach(x => mark[x.date] = 'add'); p.removed.forEach(x => mark[x.date] ||= 'rm'); }
  const pend = p ? `<div class="panel hol-pend"><div class="hd"><h3>Waiting for approval · list ${esc(p.id)}</h3><small>${a ? `${days(p.added)} day${days(p.added) === 1 ? '' : 's'} added · ${days(p.removed)} removed` : `${days(p.holidays)} days, listed below`}</small></div><div class="bd">
      <div class="small">From ${p.sources.map(esc).join('; ')}. Raised ${fmtStamp(p.at)} by ${esc(p.by === 'system:scheduler' ? 'the automatic check' : p.by)}. ${a ? 'Until approved, pay-in dates and business days follow the approved list.' : 'Until approved, only weekends and the holidays in Settings count.'}</div>
      ${a && p.added.length ? `<h4 class="hol-h">Added or changed</h4>${holTable(p.added)}` : ''}${a && p.removed.length ? `<h4 class="hol-h">Removed or changed</h4>${holTable(p.removed)}` : ''}
      <div class="tools" style="margin-top:12px">${canDecide ? (p.by === me ? '<span class="dim small">You loaded this list, so someone else must approve it.</span>' : `<button class="btn pri" data-act="holDecide" data-id="${esc(p.id)}" data-v="1">Approve</button>`) + `<button class="btn" data-act="holDecide" data-id="${esc(p.id)}" data-v="0">Reject</button>` : '<span class="dim small">An Ops checker or the compliance officer approves it.</span>'}</div>
    </div></div>` : '';
  const shown = a || p;
  return `<div class="pad">
      <div class="tools" style="justify-content:space-between;flex-wrap:wrap;gap:8px">${tabs}<div class="tools">${tools}</div></div>
      <p class="small" style="margin:10px 0 0">Approved equity trading holidays decide business days; settlement holidays also move pay-in. ${auto}</p>${chk}
    </div>
    <div class="pad" style="padding-top:0">${warn}${pend}
      <div class="panel"><div class="hd"><h3>${a ? `Approved ${y.year} calendar` : p ? `${y.year} list (not yet approved)` : `${y.year} calendar`}</h3><small>${a ? `${esc(a.id)} · approved ${fmtStamp(a.decided_at)} by ${esc(a.decided_by)} · ${a.sources.map(esc).join('; ')}` : ''}</small></div>
        ${holTable(shown?.holidays, { mark: a ? mark : {}, empty: `No ${y.year} holidays yet. ${h.connected.length ? 'Check the exchanges, or upload' : 'Upload'} the exchange circular.` })}
        <div class="pad small dim">T: no trading in those segments. S: no settlement (clearing holiday). ${h.manual_holidays.length ? `Also closed, from Settings: ${h.manual_holidays.map(fmtDate).join(', ')}.` : ''}</div>
      </div>
      ${y.history.length > 1 ? `<div class="panel"><div class="hd"><h3>History</h3></div>${tbl([['List'], ['Status'], ['Raised'], ['Decided'], ['Change']], y.history.map(c => `<tr><td class="n mono">${esc(c.id)}</td><td>${pill(c.status, { Approved: 'good', Pending: 'acc', Rejected: 'bad', Superseded: 'nt' }[c.status])}</td><td class="small">${fmtStamp(c.at)} · ${esc(c.by === 'system:scheduler' ? 'automatic check' : c.by)}</td><td class="small">${c.decided_by ? fmtStamp(c.decided_at) + ' · ' + esc(c.decided_by) : '—'}${c.note ? `<div class="dim">${esc(c.note)}</div>` : ''}</td><td class="small">+${days(c.added)} / −${days(c.removed)} days</td></tr>`))}</div>` : ''}
    </div>`;
}

Object.assign(A, {
  holCheck: id => act(() => post('/v1/holidays/check', { year: +id }), r => r.result),
  holDecide: (id, el) => {
    const ok = el.dataset.v === '1';
    confirmModal(ok ? 'Approve this holiday list?' : 'Reject this holiday list?', ok ? 'Business days and pay-in dates follow it from now on. Check it against the exchange circulars first.' : 'Say why, so the next list can be checked against it.',
      `<div class="fld"><label for="hd-n">${ok ? 'Note (optional)' : 'Reason'}</label><input class="in" id="hd-n" ${ok ? 'placeholder="e.g. matches NSE circular NSE/CMTR/…"' : 'required'}></div>`, ok ? 'Approve' : 'Reject',
      () => act(() => post(`/v1/holidays/${encodeURIComponent(id)}`, { approve: ok, note: $('hd-n').value }), ok ? 'Holiday list approved' : 'Holiday list rejected'), !ok);
  },
  holUpload: id => {
    modal(mhd('Upload an exchange holiday circular', 'Save the circular’s holiday table as a CSV with a Date column and a Description column (Exchange, Segment and Type columns are used when present). It is checked, then waits for approval.') + `<form class="bd" id="huf"><div class="form">
      <div class="fld"><label for="hu-y">Year</label><input class="in mono" id="hu-y" type="number" value="${esc(id)}" required></div>
      <div class="fld"><label for="hu-x">Exchange</label><select class="in" id="hu-x">${EXCH.map(x => `<option ${x === 'MCX' ? 'selected' : ''}>${x}</option>`).join('')}</select></div>
      <div class="fld"><label for="hu-k">Holidays for</label><select class="in" id="hu-k"><option value="trading">Trading</option><option value="settlement">Settlement (clearing)</option></select></div>
      <fieldset class="fld"><legend>Segments</legend>${Object.entries(SEGN).map(([k, l]) => `<label class="check"><input type="checkbox" name="hu-s" value="${k}" ${k === 'COM' ? 'checked' : ''}> ${l} (${k})</label>`).join(' ')}</fieldset>
      <div class="fld full"><label for="hu-f">CSV file</label><input class="in" id="hu-f" type="file" accept=".csv,text/csv" required></div>
    </div><div id="hu-err"></div></form><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" form="huf">Upload for approval</button></div>`);
    $('huf').onsubmit = async e => {
      e.preventDefault();
      const f = $('hu-f').files[0]; if (!f) return;
      const segs = [...document.querySelectorAll('[name=hu-s]:checked')].map(x => x.value);
      try {
        const r = await post('/v1/holidays/upload', { year: +$('hu-y').value, exchange: $('hu-x').value, kind: $('hu-k').value, segments: segs, csv: await f.text() });
        closeModal(); toast(`${days(r.added)} day${days(r.added) === 1 ? '' : 's'} added, ${days(r.removed)} removed: waiting for approval`); UI.holYear = String(r.year); render({ keep: true });
      } catch (err) { $('hu-err').innerHTML = errHTML(err); }
    };
  },
});
