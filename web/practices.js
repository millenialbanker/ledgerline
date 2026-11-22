'use strict';
/* Ledgerline console: Settings → Firm practices (screen "practices",
   firmpractices.go). The questionnaire a firm answers about how it works:
   each question with why it matters, its options (the one in force now
   ticked, options not built yet shown as Not available yet), and Other
   with the firm's own words. Ticking an option switches the behaviour in
   Ledgerline; changes that affect money or client limits wait for the
   compliance officer or another Admin. The go-live checklist sits on top
   until every question is answered. Questions are grouped by section
   (the general ones first, then Firm securities, firmsecpractices.go).
   Also: a client's Finspot login in the
   client drawer (finspotlogin.go). Loaded after app.js. */

const FP_ST = { 'Applied': 'good', 'Confirmed (no change)': 'good', 'Approved and applied': 'good', 'Waiting for approval': 'warn', 'Rejected': 'bad', 'Withdrawn': 'nt', 'Other: recorded, needs configuring': 'acc' };
const fpWhen = t => t ? esc(fmtTS(t)) : '—';

V.practices = {
  load: () => get('/v1/firm-practices'),
  html: d => {
    const left = d.total - d.answered;
    return ph('Firm practices', 'How your firm works, question by question. Ticking an option switches that behaviour on in Ledgerline. Choose Other if your firm works another way: Ledgerline keeps what it does now and asks for it to be configured.') +
      fpChecklist(d) +
      `<div class="note ${left ? 'warn' : ''}">${left ? `<b>${d.answered} of ${d.total} answered.</b> Answer every question before going live.` : `<b>All ${d.total} questions answered.</b> You can change an answer at any time.`} ${pill('Needs a second approver', 'warn')} marks a change that affects money or client limits: the compliance officer or another Admin approves it before it applies.${d.can_answer ? '' : ' Only an Admin answers.'}</div>` +
      d.questions.map((q, i) => (q.section && q.section !== d.questions[i - 1]?.section ? `<h2 class="fp-sec" id="fp-sec-${esc(q.section.toLowerCase().replace(/\W+/g, '-'))}" style="margin:22px 0 8px">${esc(q.section)}</h2>${q.section === 'Firm securities' ? '<p class="dim small" style="margin:0 0 10px">How the firm keeps its own securities: investments held as lots at cost, stock-in-trade as a purchase and sale book, or both. The register, reports and valuation are on Accounts → Firm securities.</p>' : ''}` : '') +
        (q.applies === false ? `<div class="panel"><div class="hd"><h3>${q.n}. ${esc(q.question)}</h3><small>${pill('Applies once the firm keeps its own securities', 'nt')}</small></div></div>` : fpQuestion(q, d))).join('');
  }
};

function fpChecklist(d) {
  const items = d.checklist || [];
  const open = items.filter(i => !i.ok);
  if (!open.length) return '';
  return `<div class="panel"><div class="hd"><h3>Go-live checklist</h3><small>${open.length} to do · Ledgerline's provider sees the same list</small></div><div class="bd"><ul class="small" style="margin:0;padding-left:18px">${items.map(i => `<li style="margin:3px 0">${i.ok ? '✓ ' : ''}<span class="${i.ok ? 'dim' : ''}">${esc(i.label)}</span>${i.detail ? ` <span class="dim">· ${esc(i.detail)}</span>` : ''}</li>`).join('')}</ul></div></div>`;
}

function fpQuestion(q, d) {
  const me = S.me?.actor, can = d.can_answer && !q.pending;
  const ticked = q.answer === 'other' ? 'other' : q.current;
  const opt = o => {
    const now = o.id === q.current;
    return `<label class="check" style="align-items:flex-start;margin:6px 0"><input type="radio" name="fp-${esc(q.id)}" value="${esc(o.id)}" ${ticked === o.id ? 'checked' : ''} ${can && o.available ? '' : 'disabled'}>
      <span>${esc(o.label)} ${now ? pill('In force now', 'good') : ''}${o.available ? '' : ' ' + pill('Not available yet', 'nt')}${o.note && o.note !== 'Not available yet' ? `<span class="dim small" style="display:block">${esc(o.note)}</span>` : ''}</span></label>`;
  };
  const fields = (q.fields || []).map(f => `<div class="fld"><label for="fp-${esc(q.id)}-${esc(f.key)}">${esc(f.label)}</label><input class="in" id="fp-${esc(q.id)}-${esc(f.key)}" data-fpf="${esc(q.id)}" data-k="${esc(f.key)}" value="${esc((q.values || {})[f.key] || '')}" ${can ? '' : 'disabled'}>${f.hint ? `<div class="dim small">${esc(f.hint)}</div>` : ''}</div>`).join('');
  const p = q.pending;
  const pend = p ? `<div class="note warn"><b>Waiting for approval:</b> ${esc(p.by)} proposed “${esc(p.label)}” on ${fpWhen(p.at)}${p.note ? ` · ${esc(p.note)}` : ''}. Until it is approved, Ledgerline keeps what it does now.
      <div class="tools" style="margin-top:6px">${d.can_approve && p.by !== me ? `<button class="btn sm pri" data-act="fpDecide" data-id="${esc(q.id)}">Approve or reject</button>` : ''}${p.by === me ? `<button class="btn sm" data-act="fpWithdraw" data-id="${esc(q.id)}">Withdraw</button>` : ''}</div></div>` : '';
  const hist = (q.history || []).length ? `<details style="margin-top:8px"><summary class="small">History · ${q.history.length}</summary>${tbl([['When'], ['Who'], ['Answer'], ['Status'], ['Decided by']], q.history.map(h => `<tr><td class="small">${fpWhen(h.at)}</td><td class="small">${esc(h.by)}</td><td class="small wrap">${esc(h.label)}${h.other ? `<div class="dim">${esc(h.other)}</div>` : ''}${h.note ? `<div class="dim">${esc(h.note)}</div>` : ''}${Object.keys(h.values || {}).length ? `<div class="dim">${esc(Object.entries(h.values).map(([k, v]) => k + ': ' + v).join(' · '))}</div>` : ''}</td><td>${pill(h.status, FP_ST[h.status] || 'nt')}</td><td class="small">${h.decided_by ? esc(h.decided_by) + `<div class="dim">${fpWhen(h.decided_at)}</div>${h.decision ? `<div class="dim wrap">${esc(h.decision)}</div>` : ''}` : '—'}</td></tr>`))}</details>` : '';
  const state = q.answer === 'other' ? pill('Other: needs configuring', 'acc') : q.answered ? pill('Answered', 'good') : pill('Not answered', 'warn');
  return `<div class="panel" id="fp-q-${esc(q.id)}"><div class="hd"><h3>${q.n}. ${esc(q.question)}</h3><small>${state}${q.money ? ' ' + pill('Needs a second approver', 'warn') : ''}</small></div><div class="bd">
    <p class="small" style="margin-top:0">${esc(q.why)}</p>
    ${q.note ? `<div class="note small">${esc(q.note)}</div>` : ''}
    ${q.current_note ? `<div class="dim small">${esc(q.current_note)}</div>` : ''}
    ${pend}
    <div role="radiogroup" aria-label="${esc(q.short)}">${q.options.map(opt).join('')}
      <label class="check" style="align-items:flex-start;margin:6px 0"><input type="radio" name="fp-${esc(q.id)}" value="other" ${ticked === 'other' ? 'checked' : ''} ${can ? '' : 'disabled'}><span>Other: the firm does it another way</span></label>
      <div class="fld" style="margin-left:24px"><label for="fp-${esc(q.id)}-other" class="dim small">How the firm does it (Ledgerline keeps what it does now and asks the Admin to configure it)</label><textarea class="in" id="fp-${esc(q.id)}-other" rows="2" maxlength="1000" ${can ? '' : 'disabled'}>${esc(q.other || '')}</textarea></div></div>
    ${fields ? `<div class="form">${fields}</div>` : ''}
    ${can ? `<div class="form" style="margin-top:6px"><div class="fld full"><label for="fp-${esc(q.id)}-note">Note (optional, kept in the history)</label><input class="in" id="fp-${esc(q.id)}-note" maxlength="500"></div></div><div class="tools"><button class="btn pri" data-act="fpSave" data-id="${esc(q.id)}">Save answer</button></div>` : ''}
    <div class="dim small" style="margin-top:6px">Where it is set: ${esc(q.setting || '')}${q.at ? ` · last answered by ${esc(q.by)} on ${fpWhen(q.at)}` : ''}</div>
    ${hist}</div></div>`;
}

const fpMsg = q => q.pending ? 'Saved: waiting for the compliance officer or another Admin to approve' : q.answer === 'other' ? 'Recorded: the Admin has a work item to configure it' : 'Saved: Ledgerline now works this way';

Object.assign(A, {
  fpSave: id => {
    const pick = document.querySelector(`input[name="fp-${id}"]:checked`);
    if (!pick) { toast('Choose an option or Other', true); return; }
    const values = {};
    document.querySelectorAll(`[data-fpf="${id}"]`).forEach(x => { values[x.dataset.k] = x.value.trim(); });
    return act(() => post(`/v1/firm-practices/${encodeURIComponent(id)}`, { option: pick.value, other: pick.value === 'other' ? $(`fp-${id}-other`).value.trim() : '', values, note: ($(`fp-${id}-note`)?.value || '').trim() }), fpMsg).catch(() => {});
  },
  fpDecide: id => {
    const q = (C.practices?.questions || []).find(x => x.id === id); if (!q || !q.pending) return;
    const now = (q.options.find(o => o.id === q.current) || {}).label || 'none of the options';
    modal(mhd('Approve a firm practice change', `${esc(q.pending.by)} proposed it on ${fpWhen(q.pending.at)}`) + `<div class="bd">
      <p class="small"><b>${esc(q.question)}</b></p>
      <p class="small">Now: ${esc(now)}<br>Proposed: <b>${esc(q.pending.label)}</b>${Object.keys(q.pending.values || {}).length ? ' · ' + esc(Object.entries(q.pending.values).map(([k, v]) => k + ': ' + v).join(' · ')) : ''}</p>
      ${q.pending.note ? `<p class="small dim">${esc(q.pending.note)}</p>` : ''}
      <div class="note small">This affects money or client limits. Approving applies it at once.</div>
      <div class="fld"><label for="fp-dn">Note (needed to reject)</label><input class="in" id="fp-dn"></div><div id="fp-derr"></div></div>
      <div class="ft"><button class="btn" id="fp-rej">Reject</button><button class="btn pri" id="fp-ok">Approve and apply</button></div>`);
    const go = approve => act(() => post(`/v1/firm-practices/${encodeURIComponent(id)}/decide`, { approve, note: $('fp-dn').value.trim() }), approve ? 'Approved: Ledgerline now works this way' : 'Rejected').catch(e => { $('fp-derr').innerHTML = errHTML(e); });
    $('fp-ok').onclick = () => go(true); $('fp-rej').onclick = () => go(false);
  },
  fpWithdraw: id => confirmModal('Withdraw the change', 'Ledgerline keeps what it does now.', '', 'Withdraw',
    () => act(() => post(`/v1/firm-practices/${encodeURIComponent(id)}/decide`, { approve: false }), 'Withdrawn')),
  fslCheck: id => act(() => post(`/v1/finspot-logins/${encodeURIComponent(id)}/check`, {}), v => v.has ? 'Finspot has a login for ' + v.act_id : 'Finspot did not find ' + v.act_id, false).then(() => clientFinspotLogin(id)).catch(() => {}),
  fslMark: id => confirmModal('Record a Finspot login', 'Record that the client’s login was made in Finspot’s admin console. Ledgerline will not create another.',
    '<div class="fld"><label for="fsl-n">Note (optional)</label><input class="in" id="fsl-n" maxlength="300" placeholder="made by the RMS desk on …"></div>', 'Record it',
    () => act(() => post(`/v1/finspot-logins/${encodeURIComponent(id)}/mark`, { note: $('fsl-n').value.trim() }), 'Recorded', false).then(() => clientFinspotLogin(id))),
  fslCreate: id => confirmModal('Create the Finspot login', 'Ledgerline creates the client’s user ID on Finspot by API with the client’s details, segments and products. No password is sent: Finspot gives the client one. If the client already exists on Finspot, Ledgerline changes it instead and records it.', '', 'Create login',
    () => act(() => post(`/v1/finspot-logins/${encodeURIComponent(id)}/create`, {}), r => r.login.has ? 'Finspot login created for ' + r.login.act_id : 'Not created: see the push on Live RMS', false).then(() => clientFinspotLogin(id))),
});

// clientFinspotLogin fills the client drawer's Finspot login line.
async function clientFinspotLogin(ucc) {
  const el = $('cl-fsl'); if (!el) return;
  const v = await get(`/v1/finspot-logins/${encodeURIComponent(ucc)}`).catch(() => null);
  if (!v || !v.used || !$('cl-fsl')) return;
  const mode = { console: 'made in Finspot’s admin console', ledgerline: 'made by Ledgerline', both: 'made in the console or by Ledgerline' }[v.mode] || '';
  const btns = [v.can_create && has('Ops maker', 'Risk manager') ? `<button class="btn sm" data-act="fslCreate" data-id="${esc(ucc)}">Create Finspot login</button>` : '',
    v.api && has('Ops maker', 'Ops checker', 'Risk manager') ? `<button class="btn sm" data-act="fslCheck" data-id="${esc(ucc)}">Check Finspot login</button>` : '',
    !v.has && has('Ops maker', 'Ops checker', 'Risk manager') ? `<button class="btn sm" data-act="fslMark" data-id="${esc(ucc)}">Made in Finspot's console</button>` : ''].join('');
  $('cl-fsl').innerHTML = `<div><small class="dim">Finspot login <span class="mono">${esc(v.act_id)}</span> · logins are ${esc(mode)}</small><div>${v.has ? pill('Has a Finspot login', 'good') + ` <span class="dim small">${esc(v.how)}${v.at ? ' · ' + fpWhen(v.at) : ''}${v.by && !v.by.startsWith('system') ? ' · ' + esc(v.by) : ''}</span>` : pill(v.requested ? 'Login asked for' : 'No Finspot login known', v.requested ? 'acc' : 'nt')}${v.note && !v.has ? ` <span class="dim small">${esc(v.note)}</span>` : ''} ${btns}</div></div>`;
}
