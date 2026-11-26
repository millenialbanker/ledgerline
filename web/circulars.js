'use strict';
/* Ledgerline console: circulars become cards. On Calendar & circulars:
   whether each circular applies to the firm (with the reason, and the
   compliance officer's own decision), the obligations the AI model
   proposes for review, the actions with their stage, evidence and the
   Ledgerline area they may change, and the "Ledgerline updates" tab.
   In Settings: the firm profile the rules read. On the Circulars board
   (a Ledgerline board): the "Ledgerline updates" filter, and Open in
   Ledgerline opens the circular.
   Server: internal/ops/circapply.go, circai.go, circboard.go,
   firmprofile.go; internal/api/circularcards.go. Names are prefixed cc. */

const CC_V = { applies: ['Applies', 'good'], no: ['Doesn’t apply', 'nt'], unsure: ['Not sure', 'warn'] };
const CC_STAGES = [['', 'To do'], ['In progress', 'In progress'], ['Waiting for evidence', 'Waiting for evidence']];
const CC_ROLES = ['Compliance officer', 'Ops checker', 'Ops maker', 'Risk manager', 'Admin'];
const ccVerdict = a => CC_V[a?.verdict] || CC_V.unsure;
const ccPill = a => a ? pill(ccVerdict(a)[0], ccVerdict(a)[1]) : '';
const ccWhy = a => a ? `${ccVerdict(a)[0]}: ${a.reason}` : '';
const ccState = () => C.circulars?.state || { areas: [], questions: [] };
const ccArea = id => (ccState().areas || []).find(a => a.id === id);
const ccSoft = id => id && id !== 'none' && !!ccArea(id);
const ccAccepted = (c, t) => !!t.accepted || c.status !== 'New';
const ccComp = () => has('Compliance officer');

/* ---------- the Circulars tab: the inbox of new circulars ---------- */
function ccInboxHTML(d) {
  const st = ccState(), news = d.circulars.filter(c => c.status === 'New');
  const n = v => news.filter(c => c.applies?.verdict === v).length;
  const review = news.filter(c => c.applies?.verdict !== 'no').reduce((s, c) => s + (c.to_review || 0), 0);
  const qs = (st.questions || []).length ? `<div class="note warn cc-q"><b>So Ledgerline can tell what applies:</b> ${st.questions.map(esc).join(' ')} <button class="linkish" data-act="go" data-id="settings">Settings → Firm profile</button></div>` : '';
  const ai = st.ai_on ? '<span class="cc-ai on">AI reads new circulars and proposes obligations for you to accept</span>' : `<span class="cc-ai">AI reading is off: actions come from Ledgerline’s rules.${st.ai_why ? ' ' + esc(st.ai_why) : ''}</span>`;
  const board = st.board ? `<button class="btn sm" data-act="ccBoard" data-id="${esc(st.board)}">Open the Circulars board</button>` : (has('Admin') ? '<span class="dim small">Set up the Circulars board on Boards → Ledgerline boards to work these as cards.</span>' : '');
  const rows = news.slice(0, 8).map(c => `<li><button class="cc-row" data-act="ciOpen" data-id="${esc(c.id)}"><span class="cc-row-t"><b>${esc(c.subject)}</b><small>${esc(c.source)} ${esc(c.ref)} · ${fmtDate(c.date)}</small></span><span class="cc-row-a">${ccPill(c.applies)}<small>${esc(c.applies?.reason || '')}</small></span><span class="cc-row-n">${c.to_review ? `<b>${c.to_review}</b> to review` : '<span class="dim">nothing to review</span>'}</span></button></li>`).join('');
  return `<section class="cc-inbox" aria-labelledby="cc-inbox-h"><div class="cc-inbox-hd"><div><h3 id="cc-inbox-h">New circulars</h3>
      <p>${news.length ? `${n('applies')} apply to you, ${n('unsure')} may apply, ${n('no')} look not to apply · <b>${review}</b> obligation${review === 1 ? '' : 's'} to review` : 'No new circulars to review.'}</p></div><div class="cc-inbox-acts">${board}</div></div>
    ${ai}${qs}${rows ? `<ul class="cc-rows">${rows}</ul>${news.length > 8 ? `<p class="dim small">And ${news.length - 8} more below.</p>` : ''}` : ''}</section>`;
}

// The circulars table's applicability and obligations cells.
function ccApplyCell(c) { return `${ccPill(c.applies)}${c.applies?.overridden ? ' <span class="tag">your decision</span>' : ''}<div class="dim small cc-why">${esc(c.applies?.reason || '')}</div>`; }
function ccObligCell(c) {
  const op = c.tasks.filter(t => t.status === 'Open').length;
  return `${c.tasks.length ? `${op} open of ${c.tasks.length}` : '—'}${c.to_review ? `<div class="small"><b>${c.to_review}</b> to review</div>` : ''}`;
}

/* ---------- the Ledgerline updates tab ---------- */
function ccUpdates(d) {
  const out = [];
  d.circulars.forEach(c => {
    if (c.status === 'Not applicable' || c.applies?.verdict === 'no') return;
    c.tasks.filter(t => t.status === 'Open' && ccSoft(t.area)).forEach(t => out.push({ c, id: t.id, title: t.title, area: t.area, role: t.role, due: t.due, st: ccAccepted(c, t) ? (t.stage || 'Accepted') : 'To review' }));
    (c.proposals || []).filter(p => p.status === 'Proposed' && ccSoft(p.area)).forEach(p => out.push({ c, id: p.id, title: p.what, area: p.area, role: p.role, due: p.due_on, st: 'Proposed' }));
  });
  return out.sort((a, b) => (a.area + a.due).localeCompare(b.area + b.due));
}
function ccUpdatesHTML(d) {
  const xs = ccUpdates(d);
  return `<div class="pad"><p class="dim small" style="margin:0">Open obligations that may need a change to a Ledgerline setting or rule, for your Ledgerline provider or Admin. On the Circulars board, the same cards carry the label “Ledgerline may need a change”.</p></div>` +
    tbl([['Ledgerline area'], ['Obligation'], ['Circular'], ['Owner'], ['Due'], ['Status']], xs.map(x => { const a = ccArea(x.area); return `<tr><td><b>${esc(a.name)}</b>${a.where ? `<div class="dim small">${esc(a.where)}</div>` : ''}</td><td class="wrap" style="max-width:380px">${esc(x.title)}</td><td class="small"><button class="linkish" data-act="ciOpen" data-id="${esc(x.c.id)}">${esc(x.c.source)} ${esc(x.c.ref)}</button></td><td class="small">${esc(x.role)}</td><td class="n">${x.due ? fmtDate(x.due) : '—'}</td><td>${pill(x.st, x.st === 'Proposed' || x.st === 'To review' ? 'warn' : 'acc')}</td></tr>`; }), { tall: true, empty: 'No open obligation needs a change to Ledgerline.' });
}

/* ---------- a circular ---------- */
function ccApplyBox(c) {
  const a = c.applies || {}, comp = ccComp();
  const reasons = (a.reasons || []).filter(r => r !== a.reason);
  const was = a.overridden ? `<div class="small">Decided by ${esc(a.by || '')}${a.at ? ' on ' + fmtDate(a.at.slice(0, 10)) : ''}. Ledgerline’s rules said: ${esc(ccWhy({ verdict: a.rules, reason: a.rules_why }))}</div>` : (reasons.length ? `<div class="dim small">Also found: ${reasons.map(esc).join('; ')}</div>` : '');
  const tools = comp ? `<div class="cc-dec"><label for="cc-why">Your decision and the reason (kept in the audit trail)</label><input class="in" id="cc-why" placeholder="e.g. We are a CDSL DP through our group company: it applies" value="">
      <div class="tools"><button class="btn sm" data-act="ccApplies" data-id="${esc(c.id)}" data-v="applies" ${a.verdict === 'applies' && a.overridden ? 'disabled' : ''}>It applies</button><button class="btn sm" data-act="ccApplies" data-id="${esc(c.id)}" data-v="no" ${a.verdict === 'no' && a.overridden ? 'disabled' : ''}>It doesn’t apply</button>${a.overridden ? `<button class="btn sm" data-act="ccApplies" data-id="${esc(c.id)}" data-v="rules">Back to Ledgerline’s rules</button>` : ''}</div></div>` : '';
  return `<div class="cc-app cc-${esc(a.verdict || 'unsure')}" role="group" aria-label="Does it apply to the firm?"><div class="cc-app-h">${ccPill(a)} <b>${esc(a.reason || '')}</b>${a.overridden ? ' <span class="tag">your decision</span>' : ''}</div>${was}${tools}</div>`;
}

function ccAIBox(c) {
  const st = ccState(), comp = ccComp(), r = c.ai;
  const last = r ? `<div class="dim small">Read ${r.at ? 'on ' + fmtDate(r.at.slice(0, 10)) : ''} by ${esc(r.by === 'system:circulars' ? 'Ledgerline (new circular)' : r.by)}${r.model ? ' · ' + esc(r.model) : ''}${r.refused ? '' : ` · ${r.proposed} proposed`}${r.note ? ' · ' + esc(r.note) : ''}</div>` : '';
  if (!st.ai_on) return `<div class="cc-aibox off"><b>AI reading is off.</b> The actions below come from Ledgerline’s rules for circulars on this subject. ${esc(st.ai_why || '')}${last}</div>`;
  return `<div class="cc-aibox"><div class="cc-aibox-h"><b>AI reading</b>${comp && (c.text || '').length >= 40 ? `<button class="btn sm" data-act="ccAI" data-id="${esc(c.id)}">${r && !r.refused ? 'Read again with AI' : 'Read with AI'}</button>` : ''}</div>
    <div class="small">The AI model reads the circular’s text and proposes obligations. Nothing becomes a task until you accept it: the firm is responsible for AI output (regulation 16C of the SEBI Intermediaries Regulations).</div>${last}${(c.text || '').length < 40 ? '<div class="dim small">Ledgerline has no text of this circular: load it with its text to have it read.</div>' : ''}</div>`;
}

function ccObHTML(o) {
  const a = ccArea(o.area);
  return `<div class="cc-ob-m">${o.role ? `<span>Owner: <b>${esc(o.role)}</b></span>` : ''}${o.due ? `<span>Due: <b>${fmtDate(o.due)}</b>${o.rule ? ` <span class="dim">(${esc(o.rule)})</span>` : ''}</span>` : ''}<span>Evidence: ${o.none ? 'none needed' : esc(o.needs || '—')}</span>${ccSoft(o.area) ? `<span class="cc-upd">Ledgerline may need a change: ${esc(a.name)}</span>` : ''}</div>${(o.steps || []).length ? `<ol class="cc-steps">${o.steps.map(s => `<li>${esc(s)}</li>`).join('')}</ol>` : ''}`;
}

function ccProposalsHTML(c) {
  const ps = c.proposals || [], open = ps.filter(p => p.status === 'Proposed'), done = ps.filter(p => p.status !== 'Proposed'), comp = ccComp();
  if (!ps.length) return '';
  const card = p => `<li class="cc-prop"><div class="cc-prop-t"><b>${esc(p.what)}</b></div>${ccObHTML({ role: p.role, due: p.due_on, rule: p.due_rule, needs: p.evidence, area: p.area, steps: p.steps })}
    ${comp && c.status !== 'Not applicable' ? `<div class="tools"><button class="btn sm pri" data-act="ccAccept" data-id="${esc(c.id)}" data-p="${esc(p.id)}" data-quick="1">Accept</button><button class="btn sm" data-act="ccAccept" data-id="${esc(c.id)}" data-p="${esc(p.id)}">Change and accept</button><button class="btn sm" data-act="ccReject" data-id="${esc(c.id)}" data-p="${esc(p.id)}">Reject</button></div>` : ''}</li>`;
  return `<section class="cc-props" aria-labelledby="cc-props-h"><h3 id="cc-props-h">Proposed by AI · ${open.length} to review</h3>
    ${open.length ? `<ul class="cc-prop-l">${open.map(card).join('')}</ul>` : '<p class="dim small">Every proposal has been accepted or rejected.</p>'}
    ${done.length ? `<details class="cc-done"><summary>${done.length} accepted or rejected</summary><ul>${done.map(p => `<li>${pill(p.status, p.status === 'Accepted' ? 'good' : 'nt')} ${esc(p.what)}${p.by ? ` <span class="dim small">· ${esc(p.by)}${p.note ? ': ' + esc(p.note) : ''}</span>` : ''}</li>`).join('')}</ul></details>` : ''}</section>`;
}

function ccTasksHTML(c) {
  const comp = ccComp();
  const files = t => (t.files || []).length ? `<div class="small cc-files">Evidence files: ${t.files.map(f => f.file ? `<a href="/v1/board-files/${encodeURIComponent(f.file)}" target="_blank" rel="noopener">${esc(f.name)}</a>` : f.url ? `<a href="${esc(f.url)}" target="_blank" rel="noopener noreferrer">${esc(f.name)}</a>` : esc(f.name)).join(', ')}</div>` : '';
  if (!c.tasks.length) return '<p class="dim small">No actions yet. Add one below, or have the circular read with AI.</p>';
  return `<ul class="cc-acts">${c.tasks.map(t => {
    const acc = ccAccepted(c, t), open = t.status === 'Open', mine = has(t.role, 'Compliance officer');
    const status = !open ? pill(t.status, t.status === 'Done' ? 'good' : 'nt') : !acc ? pill('To review', 'warn') : pill(t.stage || 'To do', t.stage ? 'acc' : 'nt');
    const stage = open && acc && mine && c.status !== 'Not applicable' ? `<select class="in sm" data-cc-stage="${esc(t.id)}" data-id="${esc(c.id)}" aria-label="Stage of ${esc(t.title)}">${CC_STAGES.map(([v, l]) => `<option value="${esc(v)}" ${(t.stage || '') === v ? 'selected' : ''}>${l}</option>`).join('')}</select>` : '';
    const btns = open ? `${!acc && comp ? `<button class="btn sm pri" data-act="ccAcceptTask" data-id="${esc(c.id)}" data-t="${esc(t.id)}">Accept</button>` : ''}${mine ? `<button class="btn sm" data-act="ciTaskDone" data-id="${esc(c.id)}" data-t="${esc(t.id)}">Done</button>` : ''}${comp ? `<button class="btn sm" data-act="ciTaskDrop" data-id="${esc(c.id)}" data-t="${esc(t.id)}">Not needed</button>` : ''}` : '';
    return `<li class="cc-act${open ? '' : ' closed'}"><div class="cc-act-t"><b>${esc(t.title)}</b>${t.source === 'ai' ? ' <span class="tag">from AI</span>' : ''}<span class="cc-act-s">${stage || status}</span></div>
      ${ccObHTML({ role: t.role, due: t.due, rule: t.due_rule, needs: t.needs, none: t.no_evidence, area: t.area, steps: [] })}${t.evidence ? `<div class="dim small">${esc(t.by)}: ${esc(t.evidence)}</div>` : ''}${files(t)}${btns ? `<div class="tools">${btns}</div>` : ''}</li>`;
  }).join('')}</ul>`;
}

function ciModal(id) {
  const c = C.circulars.circulars.find(x => x.id === id); if (!c) return; const comp = ccComp(), areas = ccState().areas || [];
  modal(mhd(esc(c.subject), `${esc(c.source)} ${esc(c.ref)} · ${fmtDate(c.date)}${c.department ? ' · ' + esc(c.department) : ''}`) + `<div class="bd cc-md">
    <div class="tools">${pill(c.priority, CI_PRI[c.priority])} ${pill(c.status, CI_ST[c.status])} ${c.areas.map(a => `<span class="tag">${esc(a)}</span>`).join(' ')}${c.url ? ` <a class="btn sm" href="${esc(c.url)}" target="_blank" rel="noopener noreferrer">Open the circular</a>` : ''}${ccState().board ? ` <button class="btn sm" data-act="ccBoard" data-id="${esc(ccState().board)}">Circulars board</button>` : ''}</div>
    ${ccApplyBox(c)}
    <div class="note"><b>Summary.</b> ${esc(c.summary)}${c.effective ? `<br><b>Effective:</b> ${fmtDate(c.effective)}` : ''}</div>
    ${c.note ? `<div class="dim small">Compliance note (${esc(c.by || '')}): ${esc(c.note)}</div>` : ''}
    ${ccAIBox(c)}
    ${ccProposalsHTML(c)}
    <h3 class="cc-h">Actions</h3>
    ${ccTasksHTML(c)}
    ${comp ? `<details class="cc-add"><summary>Add an action</summary><div class="form"><div class="fld full"><label for="ci-title">What has to be done</label><input class="in" id="ci-title"></div><div class="fld"><label for="ci-role">Owner</label><select class="in" id="ci-role">${CC_ROLES.map(r => `<option>${r}</option>`).join('')}</select></div><div class="fld"><label for="ci-due">Due</label><input class="in" type="date" id="ci-due" value="${esc(c.effective || addDaysJS(S.date, 14))}"></div><div class="fld"><label for="ci-needs">Evidence to keep</label><input class="in" id="ci-needs" placeholder="e.g. Board-approved policy"></div><div class="fld"><label for="ci-area">Ledgerline area it may change</label><select class="in" id="ci-area">${areas.map(a => `<option value="${esc(a.id)}" ${a.id === 'none' ? 'selected' : ''}>${esc(a.name)}</option>`).join('')}</select></div><div class="fld full"><button class="btn" data-act="ciAdd" data-id="${esc(c.id)}">Add action</button></div></div></details>
    <div class="fld" style="margin-top:10px"><label for="ci-note">Note</label><input class="in" id="ci-note" value="${esc(c.note || '')}" placeholder="How it applies to the firm, or why it does not"></div>` : ''}
  </div><div class="ft">${comp ? `<button class="btn" data-act="ciNA" data-id="${esc(c.id)}">Not applicable</button><button class="btn pri" data-act="ciReview" data-id="${esc(c.id)}">${c.status === 'New' ? 'Mark reviewed' : 'Save note'}</button>` : '<button class="btn" data-act="close">Close</button>'}</div>`);
}

// After a change: the screen again, and the circular's window open on it.
async function ccDo(fn, msg, cid) {
  try { await fn(); if (msg) toast(msg); await render({ keep: true }); if (cid && C.circulars?.circulars?.some(x => x.id === cid)) ciModal(cid); }
  catch (e) { toast(e.message, true); }
}

function ccAcceptForm(cid, pid) {
  const c = C.circulars.circulars.find(x => x.id === cid), p = c && (c.proposals || []).find(x => x.id === pid); if (!p) return;
  const areas = ccState().areas || [];
  modal(mhd('Change and accept', `${esc(c.source)} ${esc(c.ref)} · ${esc(p.id)}. It becomes an action with a card on the Circulars board.`) + `<div class="bd"><div class="form">
    <div class="fld full"><label for="ca-what">What has to be done</label><input class="in" id="ca-what" value="${esc(p.what)}"></div>
    <div class="fld"><label for="ca-due">Due</label><input class="in" type="date" id="ca-due" value="${esc(p.due_on || '')}"><div class="dim small">${esc(p.due_rule || '')}</div></div>
    <div class="fld"><label for="ca-role">Owner</label><select class="in" id="ca-role">${CC_ROLES.map(r => `<option ${r === p.role ? 'selected' : ''}>${r}</option>`).join('')}</select></div>
    <div class="fld"><label for="ca-needs">Evidence to keep</label><input class="in" id="ca-needs" value="${esc(p.evidence || '')}"></div>
    <div class="fld"><label for="ca-area">Ledgerline area it may change</label><select class="in" id="ca-area">${areas.map(a => `<option value="${esc(a.id)}" ${a.id === p.area ? 'selected' : ''}>${esc(a.name)}</option>`).join('')}</select></div>
    <div class="fld full"><label for="ca-steps">Steps, one per line</label><textarea class="in" id="ca-steps" rows="4">${esc((p.steps || []).join('\n'))}</textarea></div>
    <label class="check full"><input type="checkbox" id="ca-none"> No evidence is needed for this one</label>
  </div><div id="ca-err"></div></div><div class="ft"><button class="btn" data-act="ciOpen" data-id="${esc(cid)}">Back</button><button class="btn pri" id="ca-go">Accept</button></div>`);
  $('ca-go').onclick = async () => {
    $('ca-go').disabled = true;
    try {
      await post(`/v1/circulars/${encodeURIComponent(cid)}`, { kind: 'accept', proposal: pid, title: $('ca-what').value, due: $('ca-due').value, role: $('ca-role').value, needs: $('ca-needs').value, area: $('ca-area').value, steps: $('ca-steps').value.split('\n').map(x => x.trim()).filter(Boolean), no_evidence: $('ca-none').checked });
      toast('Accepted: it is an action now'); await render({ keep: true }); ciModal(cid);
    } catch (e) { $('ca-err').innerHTML = errHTML(e); $('ca-go').disabled = false; }
  };
}

/* ---------- Settings: the firm profile ---------- */
function ccProfilePanel(fp) {
  if (!fp) return '';
  const p = fp.profile, f = k => (fp.facts || []).find(x => x.key === k) || {};
  const yn = (k, label) => { const x = f(k); return `<div class="fld"><label for="fp-${k}">${esc(label)}</label><select class="in" id="fp-${k}"><option value="" ${!p[k] ? 'selected' : ''}>Go by what Ledgerline finds</option><option value="yes" ${p[k] === 'yes' ? 'selected' : ''}>Yes</option><option value="no" ${p[k] === 'no' ? 'selected' : ''}>No</option></select><div class="dim small ${x.known ? '' : 'neg'}">${esc(x.answer ? 'Your answer' : `Ledgerline finds: ${x.value || 'not known'} · ${x.from || ''}`)}</div></div>`; };
  const facts = ['exchanges', 'clearing', 'ras'].map(k => { const x = f(k); return `<div class="fp-fact"><b>${esc(x.label || '')}</b><span>${esc(x.value || '')}</span><small>${esc(x.from || '')}</small></div>`; }).join('');
  return `<div class="panel cc-fp" style="grid-column:1/-1"><div class="hd"><h3>Firm profile</h3><small>What Ledgerline needs to tell whether a circular applies · compliance officer and Admin</small></div><div class="bd">
    ${(fp.questions || []).length ? `<div class="note warn" style="margin-bottom:10px"><b>Still to answer:</b> ${fp.questions.map(esc).join(' ')}</div>` : ''}
    <div class="fp-facts">${facts}</div>
    <div class="form" style="margin-top:12px">
      ${yn('cdsl', 'CDSL depository participant')}${yn('nsdl', 'NSDL depository participant')}${yn('algo', 'Algorithmic trading (the firm’s own, or clients’ through its systems)')}${yn('ibt', 'Internet or mobile trading (clients trade through the firm’s website or app)')}
      ${yn('nri', 'NRI clients')}${yn('ap', 'Authorised persons')}${yn('branches', 'Branch offices')}${yn('other_ai', 'AI or ML tools used outside Ledgerline')}
      <div class="fld"><label for="fp-cscrf">CSCRF category</label><select class="in" id="fp-cscrf"><option value="">Not known</option>${(fp.cscrf_categories || []).map(x => `<option value="${esc(x.ID)}" ${p.cscrf === x.ID ? 'selected' : ''}>${esc(x.Name)}</option>`).join('')}</select><div class="dim small" id="fp-cscrf-n">${esc(((fp.cscrf_categories || []).find(x => x.ID === p.cscrf) || {}).Note || 'By registered clients and clients’ trading in the year (SEBI/HO/ITD-1/ITD_CSC_EXT/P/CIR/2025/60)')}</div></div>
      <label class="check"><input type="checkbox" id="fp-mtf" ${fp.mtf ? 'checked' : ''}> Margin trading facility (MTF) approved</label>
      <fieldset class="full fp-regs"><legend>Other SEBI registrations</legend><div class="fp-regs-l">${(fp.registrations || []).map(r => `<label><input type="checkbox" data-fp-reg="${esc(r.ID)}" ${(p.registrations || []).includes(r.ID) ? 'checked' : ''}> ${esc(r.Name)}</label>`).join('')}</div><input class="in" id="fp-other" placeholder="Another registration (optional)" value="${esc(p.other_registration || '')}" aria-label="Another registration"></fieldset>
    </div>
    <div class="tools" style="margin-top:10px"><button class="btn pri" data-act="ccProfSave">Save the firm profile</button>${p.by ? `<span class="dim small">Last changed by ${esc(p.by)}${p.at ? ' on ' + fmtDate(p.at.slice(0, 10)) : ''}</span>` : ''}</div>
    <p class="small" style="margin:8px 0 0">Products, depository options, client numbers, QSB and the other switches the calendar reads are on <button type="button" class="linkish" data-act="go" data-id="firmmodules">Settings → Firm modules</button>.</p>
    <p class="dim small" style="margin:8px 0 0">Exchanges, segments and the clearing route come from member codes and Cleared through above. Nothing here is client data, and it is never sent to an AI model.</p>
  </div></div>`;
}

/* ---------- registrations ---------- */
Object.assign(A, {
  ccBoard: id => { S.view = 'boards'; closeModal(); render().then(() => { try { BOARDS.openBoard(id); } catch (e) {} }); },
  ccApplies: (id, el) => {
    const v = el.dataset.v, why = ($('cc-why')?.value || '').trim();
    if (v !== 'rules' && !why) { toast('Give the reason first: it stays with the circular and in the audit trail'); $('cc-why')?.focus(); return; }
    ccDo(() => post(`/v1/circulars/${encodeURIComponent(id)}`, { kind: 'applies', verdict: v, note: why }), v === 'rules' ? 'Back to Ledgerline’s rules' : 'Your decision is saved', id);
  },
  ccAI: async (id, el) => { el.disabled = true; el.textContent = 'Reading…'; await ccDo(() => post(`/v1/circulars/${encodeURIComponent(id)}/ai`, {}), 'Read: check each proposal and accept or reject it', id); },
  ccAccept: (id, el) => {
    if (el.dataset.quick) return ccDo(() => post(`/v1/circulars/${encodeURIComponent(id)}`, { kind: 'accept', proposal: el.dataset.p }), 'Accepted: it is an action now', id);
    ccAcceptForm(id, el.dataset.p);
  },
  ccReject: (id, el) => confirmModal('Reject this obligation', 'Say why the firm does not need to do it; the reason stays with the circular.', '<div class="fld"><label for="cr-why">Reason</label><input class="in" id="cr-why"></div>', 'Reject',
    () => ccDo(() => post(`/v1/circulars/${encodeURIComponent(id)}`, { kind: 'reject', proposal: el.dataset.p, note: $('cr-why').value }), 'Rejected', id)),
  ccAcceptTask: (id, el) => ccDo(() => post(`/v1/circulars/${encodeURIComponent(id)}`, { kind: 'accept_task', task: el.dataset.t }), 'Accepted', id),
  ccProfSave: async () => {
    const v = k => $('fp-' + k).value, regs = [...document.querySelectorAll('[data-fp-reg]')].filter(x => x.checked).map(x => x.dataset.fpReg);
    try {
      await api('PUT', '/v1/firm-profile', { cdsl: v('cdsl'), nsdl: v('nsdl'), algo: v('algo'), ibt: v('ibt'), nri: v('nri'), ap: v('ap'), branches: v('branches'), other_ai: v('other_ai'), cscrf: v('cscrf'), mtf: $('fp-mtf').checked, registrations: regs, other_registration: $('fp-other').value });
      toast('Firm profile saved: circulars are checked against it'); render({ keep: true });
    } catch (e) { toast(e.message, true); }
  },
});

// The stage of an action, chosen in the circular's window.
document.addEventListener('change', e => {
  const el = e.target; if (!el.dataset || el.dataset.ccStage == null) return;
  const id = el.dataset.id;
  ccDo(() => post(`/v1/circulars/${encodeURIComponent(id)}`, { kind: 'stage', task: el.dataset.ccStage, stage: el.value }), 'Stage changed', id);
});

/* ---------- the Circulars board ---------- */
if (typeof BS_KIND !== 'undefined') {
  BS_KIND.circular = 'Circular obligation'; BS_VIEW.circular = 'circulars';
  BS_QUIET.push('Not applicable'); BS_GOOD.push('Accepted');
  const bsOpen0 = A.bsOpen;
  A.bsOpen = async (k, el) => {
    if (!k.startsWith('circular|')) return bsOpen0(k, el);
    const cid = k.slice('circular|'.length).replace(/\|[^|]*$/, '');
    if (!V.circulars) { toast('That screen is not open to you.', true); return; }
    UI.ciTab = 'circ'; S.view = 'circulars'; closeModal(); await render(); window.scrollTo(0, 0);
    if (C.circulars?.circulars?.some(x => x.id === cid)) ciModal(cid);
  };
}
if (typeof BOARDS !== 'undefined') {
  BOARDS.headerButtons.push({
    id: 'ccUpdates', order: 30,
    render: ctx => {
      const b = ctx?.board, lid = b?.settings?.['bs.label.circ:update'];
      if (!b || b.kind !== 'ledgerline' || b.source !== 'circulars' || !lid) return '';
      const on = UI.bdF.labels.length === 1 && UI.bdF.labels[0] === lid;
      return `<button class="bd-hb${on ? ' on' : ''}" data-act="ccUpdFilter" data-id="${esc(lid)}" aria-pressed="${on}" aria-label="Ledgerline updates: only the cards that may need a change to Ledgerline" title="Only the cards that may need a change to Ledgerline">${typeof bdI === 'function' ? bdI('gear') : ''}<span>Ledgerline updates</span></button>`;
    },
  });
  A.ccUpdFilter = id => BOARDS.filterBy('label', id);
}
