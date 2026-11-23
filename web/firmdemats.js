'use strict';
/* Ledgerline console: Settings → Firm demat accounts.

   The daily holding statement to the exchanges reports the firm's own demat
   accounts (pool, own, client unpaid, client collateral, pledge, early
   pay-in…), each with its NSE account type, never clients' accounts. Here
   an Admin tags each of the firm's accounts with its type and sets the
   firm's own client code; the types can be taken from the previous
   system's holding statement file. The accounts listed are the tagged ones
   and those of CDSL's last statement of holding that belong to no client.
   What stops the holding statement is listed.

   Server: internal/ops/firmdemats.go; internal/api/firmdemats.go. Loaded
   after app.js; it wraps V.settings. Names are prefixed fd. */

const fdBase = V.settings;
V.settings = {
  load: async () => {
    const [d, fd] = await Promise.all([fdBase.load(), get('/v1/firm-demats').catch(e => ({ error: e.message }))]);
    d.fd = fd;
    return d;
  },
  html: d => fdBase.html(d) + fdPanel(d.fd || {})
};

function fdPanel(v) {
  if (v.error) return `<div class="panel"><div class="hd"><h3>Firm demat accounts</h3></div><div class="bd">${errHTML({ message: v.error })}</div></div>`;
  const tn = v.type_notes || {};
  const ed = v.can_edit, opts = t => `<option value="">Not tagged</option>${v.types.map(x => `<option ${x === t ? 'selected' : ''} title="${esc(tn[x] || '')}">${esc(x)}</option>`).join('')}`;
  const rows = v.accounts.map((a, i) => `<tr><td class="mono">${esc(a.masked)}${a.client ? ` <span class="dim small">also on client ${esc(a.client)}</span>` : ''}</td>
    <td>${a.seen ? `${nf(a.isins)} ISINs · ${esc(a.qty)}` : '<span class="dim">not in the last statement of holding</span>'}</td>
    <td>${ed ? `<select class="in" id="fd-t-${i}" data-bo="${esc(a.boid)}">${opts(a.type)}</select>` : (a.type ? esc(a.type) : pill('Not tagged', 'bad'))}${a.type && tn[a.type] ? `<div class="dim small">${esc(tn[a.type])}</div>` : ''}</td></tr>`);
  const probs = (v.problems || []).length ? `<div class="note warn"><b>The holding statement${v.date ? ' for ' + fmtDate(v.date) : ''} is not made until this is put right:</b><ul>${v.problems.map(p => `<li>${esc(p)}</li>`).join('')}</ul></div>` : (v.date ? `<div class="note">The holding statement for ${fmtDate(v.date)} can be made from the tagged accounts.</div>` : '');
  const notes = (v.notes || []).length ? `<div class="note small"><b>Check before it goes:</b><ul>${v.notes.map(p => `<li>${esc(p)}</li>`).join('')}</ul></div>` : '';
  const legend = `<details class="small" style="margin-top:8px"><summary>What each type is for</summary><ul>${v.types.map(x => `<li><b>${esc(x)}</b>: ${esc(tn[x] || '')}</li>`).join('')}</ul><div class="dim">Securities clients pledge to the firm as margin stay in the clients' accounts and are not reported (NSE's FAQ); what the firm's own accounts hold, including invoked pledges, is.</div></details>`;
  return `<div class="panel"><div class="hd"><h3>Firm demat accounts</h3><small>the daily holding statement reports these accounts with their NSE account type, never clients' own accounts${v.snapshot ? ` · seen in ${esc(v.snapshot)} (${fmtDate(v.date)})` : ''}</small></div><div class="bd">
    ${probs}${notes}
    <div class="form"><div class="fld"><label for="fd-ucc">The firm's own client code (reported on each account)</label><input class="in mono" id="fd-ucc" value="${esc(v.firm_ucc || '')}" ${ed ? '' : 'disabled'}></div></div>
    ${ed ? `<div class="split" style="margin-top:8px"><button class="btn pri" data-act="fdSave">Save tags</button><label class="btn">Take the types from the old system's holding statement<input type="file" id="fd-file" accept=".json,application/json" hidden></label></div>` : '<p class="dim small">Only an Admin tags the accounts.</p>'}
    ${legend}</div>${tbl([['Account'], ['Last statement of holding'], ['Holding statement type']], rows, { empty: 'No account yet: load CDSL’s statement of holding on Exchange files.' })}</div>`;
}

Object.assign(A, {
  fdSave: () => {
    const tags = {};
    document.querySelectorAll('[id^="fd-t-"]').forEach(el => { if (el.value) tags[el.dataset.bo] = el.value; });
    act(() => api('PUT', '/v1/firm-demats', { tags, firm_ucc: $('fd-ucc').value.trim() }), 'Firm demat accounts saved');
  },
});

document.addEventListener('change', async e => {
  if (e.target.id !== 'fd-file' || !e.target.files.length) return;
  const f = e.target.files[0];
  try {
    const r = await api('POST', '/v1/firm-demats/from-statement?name=' + encodeURIComponent(f.name), await f.arrayBuffer(), true);
    toast(`Types taken for ${Object.keys(r.found || {}).length} accounts`); render({ keep: true });
  } catch (err) { toast(err.message, true); }
});
