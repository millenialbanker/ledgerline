'use strict';
/* Ledgerline console: Files → Parallel run (screen "parallel").

   Before going live the firm runs Ledgerline beside its old back office and
   compares the files both make for the same day. Each comparison takes the
   old system's file and Ledgerline's, of one layout, and shows the rows
   only in one file, the rows that differ and in which column, each column's
   difference count and both files' totals for number columns. Client codes,
   demat accounts and text are masked; numbers are in full. A day matches
   when the latest comparison of every layout that day matches; the pilot
   goes live after ten matching business days in a row.

   Server: internal/ops/parallel.go; internal/api/parallel.go. Loaded after
   app.js. Names are prefixed pr. */

Object.assign(UI, { prOpen: '' });

V.parallel = {
  load: async () => {
    const d = await get('/v1/parallel');
    if (UI.prOpen) d.open = await get('/v1/parallel/compare/' + encodeURIComponent(UI.prOpen)).catch(() => null);
    return d;
  },
  html: d => {
    const left = Math.max(0, d.goal - d.streak), last = d.days[0];
    const k = `<div class="kpis">${kpi('Matching days in a row', `${nf(d.streak)} of ${nf(d.goal)}`, d.streak ? `${fmtDate(d.streak_from)} to ${fmtDate(d.streak_to)}` : 'no matching day yet', d.streak >= d.goal ? 'good' : d.streak ? 'warn' : '')}${kpi('Still to go', nf(left), left ? 'business days, each with every file matching' : 'the go-live condition is met', left ? '' : 'good')}${kpi('Last day compared', last ? fmtDate(last.date) : '—', last ? (last.match ? 'every file matches' : 'differences to explain') : 'nothing compared yet', last ? (last.match ? 'good' : 'bad') : '')}</div>`;
    const days = d.days.map(x => `<tr><td>${fmtDate(x.date)}</td><td>${x.match ? pill('Matches', 'good') : pill('Differs', 'bad')}</td><td>${x.formats.map(f => `<button class="linkish small" data-act="prOpen" data-id="${esc(f.id)}">${esc(f.format_title)}</button> ${f.match ? '<span class="small">✓</span>' : `<span class="small neg">${nf(f.differences)} differences</span>`}${f.runs > 1 ? ` <span class="dim small">(${f.runs} comparisons)</span>` : ''}`).join('<br>')}</td></tr>`);
    const recent = d.recent.map(c => `<tr><td class="mono small"><button class="linkish" data-act="prOpen" data-id="${esc(c.id)}">${esc(c.id)}</button></td><td>${fmtDate(c.date)}</td><td>${esc(c.format_title)}</td><td class="small">${esc(c.old_name)}<div class="dim">${esc(c.new_name)}</div></td><td class="r">${nf(c.old_rows)} / ${nf(c.new_rows)}</td><td>${c.match ? pill('Matches', 'good') : pill(`${nf(c.differing)} differ · ${nf(c.only_old)} only old · ${nf(c.only_new)} only new`, 'bad')}</td><td class="dim small">${esc(c.by)} · ${esc((c.at || '').slice(0, 16).replace('T', ' '))}</td></tr>`);
    return ph('Parallel run', 'Compare the old system’s file with Ledgerline’s file of the same kind and day. Rows are matched on their identifying columns (client code, demat account, account type, ISIN, segment, exchange, settlement…); record numbers and fillers are ignored; numbers are compared as numbers and text without case or extra spaces. Neither file is kept.',
      d.can_compare ? '<button class="btn pri" data-act="prCompare">Compare two files</button>' : '') + k +
      (d.open ? prDetail(d.open, d.max_lines) : '') +
      `<div class="panel"><div class="hd"><h3>Days compared</h3><small>a day matches when the latest comparison of every file that day matches; a business day with no comparison ends the run of matching days</small></div>${tbl([['Day'], ['Result'], ['Files']], days, { empty: 'No day compared yet.' })}</div>` +
      `<div class="panel"><div class="hd"><h3>Comparisons</h3><small>the latest 50</small></div>${tbl([['ID'], ['Day'], ['File'], ['Old system / Ledgerline'], ['Rows', 'r'], ['Result'], ['By']], recent, { tall: true, empty: 'No comparison yet.' })}</div>`;
  }
};

function prDetail(c, max) {
  const cols = c.columns.filter(x => !x.key).map(x => `<tr><td>${esc(x.name)}</td><td class="r">${x.diffs ? `<b class="neg">${nf(x.diffs)}</b>` : '0'}</td><td class="r mono">${x.numeric ? esc(x.old_total) : '—'}</td><td class="r mono">${x.numeric ? esc(x.new_total) : '—'}</td><td>${x.numeric && x.old_total !== x.new_total ? pill('Totals differ', 'warn') : ''}</td></tr>`);
  const lines = c.lines.map(l => `<tr><td>${pill(l.kind, l.kind === 'differs' ? 'warn' : 'bad')}</td><td class="mono small">${esc(l.key)}</td><td>${esc(l.column || '')}</td><td class="mono small">${esc(l.old || '')}</td><td class="mono small">${esc(l.new || '')}</td></tr>`);
  return `<div class="panel"><div class="hd"><h3>${esc(c.id)} · ${esc(c.format_title)} · ${fmtDate(c.date)}</h3><small>${c.match ? 'the files match' : `${nf(c.differing)} rows differ, ${nf(c.only_old)} only in the old system’s file, ${nf(c.only_new)} only in Ledgerline’s`} · ${nf(c.matched)} rows the same</small><button class="btn sm" data-act="prClose">Close</button></div><div class="bd">
    <p class="dim small" style="margin-top:0">Old system: ${esc(c.old_name)} (${nf(c.old_rows)} rows${c.old_issues ? `, ${nf(c.old_issues)} lines not as the layout` : ''}) · Ledgerline: ${esc(c.new_name)} (${nf(c.new_rows)} rows${c.new_issues ? `, ${nf(c.new_issues)} lines not as the layout` : ''}). Matched on: ${esc(c.key_columns.join(', ') || 'the whole row')}${c.ignored.length ? ` · ignored: ${esc(c.ignored.join(', '))}` : ''}.</p></div>
    ${tbl([['Column'], ['Differences', 'r'], ['Old system total', 'r'], ['Ledgerline total', 'r'], ['']], cols, { empty: 'No columns compared.' })}
    ${c.lines.length ? `<div class="bd"><h4 style="margin:0">Differences</h4><p class="dim small">Client codes, demat accounts and text are masked; numbers are in full.${c.more_lines ? ` The first ${nf(max)} are kept; ${nf(c.more_lines)} more are not shown.` : ''}</p></div>${tbl([['What'], ['Row'], ['Column'], ['Old system'], ['Ledgerline']], lines, { tall: true })}` : ''}</div>`;
}

Object.assign(A, {
  prOpen: id => { UI.prOpen = id; render({ keep: true }); },
  prClose: () => { UI.prOpen = ''; render({ keep: true }); },
  prCompare: () => {
    const fs = (C.parallel?.formats || []).map(f => `<option value="${esc(f.id)}">${esc(f.party)} · ${esc(f.title)}${f.direction === 'out' ? ' (sent)' : ''}</option>`).join('');
    confirmModal('Compare two files', 'The old system’s file and Ledgerline’s file of the same kind, for the same day. Neither file is kept: only the differences, masked.',
      `<div class="form"><div class="fld"><label for="pr-date">Day the files are for</label><input class="in" type="date" id="pr-date" value="${esc(S.date)}"></div>
       <div class="fld"><label for="pr-fmt">Kind of file</label><select class="in" id="pr-fmt"><option value="">Recognise from the file name</option>${fs}</select></div>
       <div class="fld"><label for="pr-old">The old system’s file</label><input class="in" type="file" id="pr-old"></div>
       <div class="fld"><label for="pr-new">Ledgerline’s file</label><input class="in" type="file" id="pr-new"></div></div>`, 'Compare',
      async () => {
        const o = $('pr-old').files[0], n = $('pr-new').files[0];
        if (!o || !n) throw new Error('Choose both files');
        const fd = new FormData(); fd.append('old', o); fd.append('new', n); fd.append('format', $('pr-fmt').value); fd.append('date', $('pr-date').value);
        const r = await act(() => api('POST', '/v1/parallel/compare', fd, true), r => r.match ? 'The files match' : 'The files differ: see the differences', false);
        UI.prOpen = r.id; await render({ keep: true });
      });
  },
});
