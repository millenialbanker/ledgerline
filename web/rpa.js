'use strict';
/* Ledgerline console: Automations. Workflows recorded in the Ledgerline bot's
   browser on a staff computer, edited here, run on demand or on a schedule;
   saved logins for other sites; paired computers. Loaded after app.js. */

const RP = { home: null, edit: null, rec: null, run: null, timer: null, shot: {} };
const KIND = k => (RP.home?.kinds || []).find(x => x.Kind === k) || { Name: k, About: '' };
const encR = encodeURIComponent;
const rpOnline = () => (RP.home?.agents || []).filter(a => a.mine && a.online);
const rpMine = () => (RP.home?.agents || []).filter(a => a.mine);
const statusPill = s => pill(s || '—', { Succeeded: 'good', Failed: 'bad', Cancelled: 'nt', Running: 'acc', Paused: 'warn', Queued: 'nt' }[s] || 'nt');
function stepText(s) {
  const t = s.target || {}, what = t.text || t.label || t.placeholder || t.name || t.css || '';
  switch (s.kind) {
    case 'open': return `Open <span class="mono">${esc(s.url)}</span>`;
    case 'type': return `Type ${s.needs_secret ? `<span class="pill warn">${s.needs_secret === 'otp' ? 'one-time password' : 'choose a saved login'}</span>` : `<span class="mono">${esc(rpValue(s.value))}</span>`} into <b>${esc(what)}</b>`;
    case 'click': return `Click <b>${esc(what)}</b>`;
    case 'check': return `${s.value === 'off' ? 'Untick' : 'Tick'} <b>${esc(what)}</b>`;
    case 'select': return `Choose <b>${esc(s.value)}</b> in ${esc(what)}`;
    case 'press': return `Press <span class="mono">${esc(s.key)}</span>`;
    case 'wait': return `Wait ${s.seconds} s`;
    case 'wait_for': return `Wait for ${s.target ? '<b>' + esc(what) + '</b>' : '“' + esc(s.value) + '”'}`;
    case 'assert': return `Check the page shows ${s.target ? '<b>' + esc(what) + '</b>' : '“' + esc(s.value) + '”'}`;
    case 'extract': return `Read <b>${esc(what)}</b> as <span class="mono">{{${esc(s.var)}}}</span>`;
    case 'download': return `Download by clicking <b>${esc(what)}</b> → <span class="mono">${esc(s.var)}</span>`;
    case 'upload': return `Upload <span class="mono">${esc(s.var)}</span> into <b>${esc(what)}</b>`;
    case 'ledgerline_upload': return `Load <span class="mono">${esc(s.var)}</span> into Ledgerline as <b>${esc(s.source)}</b>`;
    case 'pause': return `Ask me: ${esc(s.message)}`;
    case 'notify': return `Message me: ${esc(s.message)}`;
  }
  return esc(s.kind);
}
// Show saved logins by name instead of their IDs.
function rpValue(v) {
  return String(v || '').replace(/\{\{\s*secret:([A-Za-z0-9_-]+)\.(\w+)\s*\}\}/g, (m, id, f) => { const c = (RP.home?.creds || []).find(x => x.id === id); return `«${c ? c.name : 'saved login'} ${f === 'totp' ? 'code' : f}»`; });
}

V.rpa = {
  load: async () => { RP.home = await get('/v1/rpa'); if (RP.edit && RP.edit.id) { try { RP.edit = await get('/v1/rpa/flows/' + encR(RP.edit.id)); } catch (e) { RP.edit = null; } } return RP.home; },
  html: h => {
    if (RP.edit) return rpEditor();
    const mine = rpMine();
    const acts = `<button class="btn" data-act="rpPair">Add this computer</button><button class="btn pri" data-act="rpRecord" ${mine.length ? '' : 'disabled title="Pair a computer first"'}>Record a task</button>`;
    const intro = mine.length ? '' : `<div class="note"><b>Start by pairing your computer.</b> The Ledgerline bot runs there: it opens its own browser window, records what you do in it (in Ledgerline or any website), and replays it. Click <b>Add this computer</b> for the steps.</div>`;
    const tab = UI.rpaTab || 'flows';
    return ph('Automations', 'Record a task once (downloading the day’s files from an exchange portal and loading them here, filling a form, checking a status) and Ledgerline’s bot repeats it on your computer, on demand or on a schedule. Passwords stay in Ledgerline’s vault and are given to the bot only at the step that needs them.', acts) + intro +
      `<div class="tools" style="margin:6px 0 12px">${segBtns('rpaTab', [['flows', `Workflows (${h.flows.length})`], ['runs', 'Runs'], ['logins', `Saved logins (${h.creds.length})`], ['pcs', `Computers (${h.agents.length})`]])}</div>` +
      ({ flows: rpFlows, runs: rpRuns, logins: rpLogins, pcs: rpPCs }[tab] || rpFlows)(h);
  }
};

function rpFlows(h) {
  return `<div class="panel"><div class="hd"><h3>Workflows</h3><button class="btn sm" data-act="rpNew">New empty workflow</button></div>${tbl([['Workflow'], ['Steps', 'r'], ['Schedule'], ['Last run'], ['']], h.flows.map(f => {
    const own = f.owner === h.me, sch = f.schedule?.active ? `${esc(f.schedule.at)} · ${esc({ business: 'trading days', daily: 'every day' }[f.schedule.when] || f.schedule.when)}` : '<span class="dim">—</span>';
    const needs = (f.steps || []).some(s => s.needs_secret && s.needs_secret !== 'otp');
    return `<tr><td><b>${esc(f.name)}</b> ${f.shared ? pill('Shared', 'acc') : ''}${needs ? ' ' + pill('Needs a saved login', 'warn') : ''}<div class="dim small">${esc(f.about || '')}${own ? '' : ' · by ' + esc(f.owner)}</div></td>
      <td class="r mono">${(f.steps || []).length}</td><td class="small">${sch}</td><td class="small">${f.last_status ? statusPill(f.last_status) + ' ' + fmtStamp(f.last_run) : '<span class="dim">Never</span>'}</td>
      <td class="r nowrap"><button class="btn sm pri" data-act="rpRun" data-id="${esc(f.id)}" ${rpMine().length && !needs ? '' : 'disabled'}>Run</button> <button class="btn sm" data-act="rpEdit" data-id="${esc(f.id)}">${own || h.admin ? 'Edit' : 'View'}</button></td></tr>`;
  }), { empty: 'No workflows yet. Click Record a task.' })}</div>`;
}
function rpRuns(h) {
  return `<div class="panel">${tbl([['Run'], ['Workflow'], ['Status'], ['Started'], ['Finished'], ['']], h.runs.map(r => `<tr><td class="mono small">${esc(r.id)}<div class="dim">${esc(r.trigger)}${r.user !== h.me ? ' · ' + esc(r.user) : ''}</div></td><td>${esc(r.flow_name)}</td><td>${statusPill(r.status)}${r.status === 'Paused' ? '<div class="small warn">Waiting for you</div>' : ''}</td>
    <td class="small">${fmtStamp(r.started || r.queued)}</td><td class="small">${fmtStamp(r.ended)}</td><td class="r"><button class="btn sm" data-act="rpOpenRun" data-id="${esc(r.id)}">Open</button></td></tr>`), { empty: 'No runs yet.' })}</div>`;
}
function rpLogins(h) {
  return `<div class="note small">Saved logins are encrypted with the firm’s keys and never shown again after saving, not even to Admins. A workflow step refers to a login by name; the bot gets the password only while that step runs, and every use is recorded. ${h.admin ? 'Firm logins (the firm’s own portal accounts) can be given to named people.' : ''}</div>
    <div class="panel"><div class="hd"><h3>Saved logins</h3><button class="btn sm pri" data-act="rpCred" data-id="">Add a login</button></div>${tbl([['Name'], ['Site'], ['User name'], ['Belongs to'], ['Used', 'r'], ['']], h.creds.map(c => `<tr><td><b>${esc(c.name)}</b>${c.has_totp ? ' ' + pill('Authenticator', 'acc') : ''}<div class="dim small">${esc(c.notes || '')}</div></td><td class="small">${esc(c.site)}</td><td class="mono small">${esc(c.username)}</td>
    <td class="small">${c.owner === '*firm' ? 'The firm' + (c.users?.length ? ': ' + esc(c.users.join(', ')) : ' (nobody yet)') : c.owner === h.me ? 'You' : esc(c.owner)}</td><td class="r small">${c.uses || 0}${c.used_at ? '<div class="dim">' + fmtStamp(c.used_at) + '</div>' : ''}</td>
    <td class="r nowrap">${c.can_edit ? `<button class="btn sm" data-act="rpCred" data-id="${esc(c.id)}">Edit</button> <button class="btn sm" data-act="rpCredDel" data-id="${esc(c.id)}">Remove</button>` : ''}</td></tr>`), { empty: 'No saved logins.' })}</div>`;
}
function rpPCs(h) {
  return `<div class="panel"><div class="hd"><h3>Computers</h3><button class="btn sm pri" data-act="rpPair">Add this computer</button></div>${tbl([['Computer'], ['Belongs to'], ['Status'], ['Paired'], ['']], h.agents.map(a => `<tr><td><b>${esc(a.name)}</b><div class="dim small">${esc(a.os)} · bot ${esc(a.version)}</div></td><td>${a.mine ? 'You' : esc(a.user)}</td>
    <td>${a.online ? pill('On', 'good') : pill('Off', 'nt')}<div class="dim small">${a.seen ? 'last seen ' + fmtStamp(a.seen) : ''}</div></td><td class="small">${fmtStamp(a.paired)}</td><td class="r"><button class="btn sm" data-act="rpRevoke" data-id="${esc(a.id)}">Remove</button></td></tr>`), { empty: 'No computers paired yet.' })}</div>`;
}

/* ---------- the editor ---------- */
function rpEditor() {
  const f = RP.edit, h = RP.home, can = !f.id || f.owner === h.me || h.admin;
  const s = f.schedule || { active: false, at: '07:30', when: 'business', agent: '' };
  const steps = f.steps || [];
  return ph(f.id ? esc(f.name) : 'New workflow', f.id ? `Version ${f.version} · ${f.owner === h.me ? 'yours' : 'by ' + esc(f.owner)}` : 'Add steps, or record them.', `<button class="btn" data-act="rpBack">Back</button>${can ? `<button class="btn pri" data-act="rpSave">Save</button>` : ''}`) +
    `<div class="grid g2"><div class="panel"><div class="bd rp-form"><div class="fld"><label for="rp-name">Name</label><input class="in" id="rp-name" value="${esc(f.name || '')}" maxlength="80" ${can ? '' : 'disabled'}></div>
      <div class="fld"><label for="rp-about">What it does</label><input class="in" id="rp-about" value="${esc(f.about || '')}" maxlength="200" ${can ? '' : 'disabled'}></div>
      <label class="check"><input type="checkbox" id="rp-shared" ${f.shared ? 'checked' : ''} ${can ? '' : 'disabled'}> Share with colleagues (they run it on their own computers, with their own logins)</label></div></div>
    <div class="panel"><div class="bd rp-form"><label class="check"><input type="checkbox" id="rp-sch" ${s.active ? 'checked' : ''} ${can ? '' : 'disabled'}> Run by itself</label>
      <div class="grid g3"><div class="fld"><label for="rp-at">At (IST)</label><input class="in" type="time" id="rp-at" value="${esc(s.at)}" ${can ? '' : 'disabled'}></div>
      <div class="fld"><label for="rp-when">On</label><select class="in" id="rp-when" ${can ? '' : 'disabled'}>${[['business', 'Trading days'], ['daily', 'Every day'], ['weekly:Mon', 'Mondays'], ['weekly:Fri', 'Fridays'], ['monthly:1', '1st of the month']].map(([v, l]) => `<option value="${v}" ${s.when === v ? 'selected' : ''}>${l}</option>`).join('')}</select></div>
      <div class="fld"><label for="rp-agent">On computer</label><select class="in" id="rp-agent" ${can ? '' : 'disabled'}>${rpMine().map(a => `<option value="${esc(a.id)}" ${s.agent === a.id ? 'selected' : ''}>${esc(a.name)}</option>`).join('') || '<option value="">Pair a computer first</option>'}</select></div></div>
      <div class="dim small">The computer must be on, with the bot running, at that time. A run that needs you (a captcha, an SMS code) pauses and Ledgy tells you.</div></div></div></div>
    <div class="panel"><div class="hd"><h3>Steps</h3>${can ? `<div class="tools">${f.id ? `<button class="btn sm" data-act="rpRecord" data-id="${esc(f.id)}">Record more</button>` : ''}<select class="in sm" id="rp-addkind">${(h.kinds || []).map(k => `<option value="${k.Kind}">${esc(k.Name)}</option>`).join('')}</select><button class="btn sm" data-act="rpAddStep">Add step</button></div>` : ''}</div>
      <ol class="rp-steps">${steps.map((st, i) => `<li class="${st.needs_secret && st.needs_secret !== 'otp' ? 'warn' : ''}"><span class="rp-n">${i + 1}</span><div class="rp-st"><small>${esc(KIND(st.kind).Name)}${st.optional ? ' · optional' : ''}</small><div>${stepText(st)}</div></div>
        ${can ? `<span class="rp-acts"><button class="btn sm" data-act="rpStep" data-id="${i}">Edit</button><button class="btn sm" data-act="rpMove" data-id="${i}:-1" ${i ? '' : 'disabled'} aria-label="Move up">↑</button><button class="btn sm" data-act="rpMove" data-id="${i}:1" ${i < steps.length - 1 ? '' : 'disabled'} aria-label="Move down">↓</button><button class="btn sm" data-act="rpDelStep" data-id="${i}" aria-label="Delete step">×</button></span>` : ''}</li>`).join('') || '<li class="empty">No steps yet.</li>'}</ol></div>
    ${f.id && can ? `<div class="tools" style="margin-top:12px;justify-content:space-between"><button class="btn" data-act="rpCopy">Make a copy</button><button class="btn" data-act="rpDelete" style="color:var(--bad)">Delete workflow</button></div>` : f.id ? '<div style="margin-top:12px"><button class="btn" data-act="rpCopy">Make a copy to change it</button></div>' : ''}`;
}
function rpCollect() {
  const f = RP.edit;
  f.name = $('rp-name').value; f.about = $('rp-about').value; f.shared = $('rp-shared').checked;
  f.schedule = { active: $('rp-sch').checked, at: $('rp-at').value, when: $('rp-when').value, agent: $('rp-agent').value };
}
function rpStepModal(i) {
  const f = RP.edit, isNew = i === -1;
  const s = isNew ? { kind: $('rp-addkind').value } : JSON.parse(JSON.stringify(f.steps[i]));
  RP.stepDraft = { i, s };
  const t = s.target || {}, k = s.kind;
  const needT = ['click', 'check', 'type', 'select', 'extract', 'download', 'upload', 'wait_for', 'assert'].includes(k);
  const vars = (f.steps || []).slice(0, isNew ? undefined : i).filter(x => x.kind === 'extract' && x.var).map(x => x.var);
  const fileVars = (f.steps || []).slice(0, isNew ? undefined : i).filter(x => x.kind === 'download' && x.var).map(x => x.var);
  const creds = RP.home.creds;
  const ins = k === 'type' || k === 'select' || k === 'notify' || k === 'wait_for' || k === 'assert' || k === 'open';
  modal(`<div class="hd"><div><h2>${esc(KIND(k).Name)}</h2><p>${esc(KIND(k).About)}</p></div><button class="x" data-act="close" aria-label="Close">×</button></div><div class="bd rp-form">
    ${k === 'open' ? `<div class="fld"><label for="sx-url">Web address</label><input class="in mono" id="sx-url" value="${esc(s.url || '')}" placeholder="https://"></div>` : ''}
    ${needT ? `<div class="fld"><label>On the page <span class="dim">(the bot tries each of these in turn)</span></label><div class="grid g3">
      <input class="in" id="sx-text" placeholder="Visible text" value="${esc(t.text || '')}"><input class="in" id="sx-label" placeholder="Label" value="${esc(t.label || '')}"><input class="in" id="sx-ph" placeholder="Placeholder" value="${esc(t.placeholder || '')}"></div>
      <details ${t.css ? '' : 'open'}><summary class="small dim">Selector and name</summary><div class="grid g2" style="margin-top:6px"><input class="in mono" id="sx-css" placeholder="CSS selector" value="${esc(t.css || '')}"><input class="in" id="sx-name" placeholder="Name or aria-label" value="${esc(t.name || '')}"></div></details></div>` : ''}
    ${['type', 'select', 'wait_for', 'assert', 'check'].includes(k) ? `<div class="fld"><label for="sx-val">${k === 'check' ? 'Tick it?' : k === 'select' ? 'Option' : k === 'type' ? 'Text to type' : 'Text on the page'}</label>${k === 'check' ? `<select class="in" id="sx-val"><option value="on" ${s.value !== 'off' ? 'selected' : ''}>Tick</option><option value="off" ${s.value === 'off' ? 'selected' : ''}>Untick</option></select>` : `<input class="in mono" id="sx-val" value="${esc(s.value || '')}">`}</div>` : ''}
    ${ins ? `<div class="tools small">Insert: ${k === 'type' ? `<select class="in sm" id="sx-cred"><option value="">a saved login…</option>${creds.map(c => `<option value="${esc(c.id)}.password">${esc(c.name)} password</option><option value="${esc(c.id)}.username">${esc(c.name)} user name</option>${c.has_totp ? `<option value="${esc(c.id)}.totp">${esc(c.name)} authenticator code</option>` : ''}`).join('')}</select>` : ''}
      <select class="in sm" id="sx-date"><option value="">a date…</option>${[['tradedate:DD-MM-YYYY', 'Trade date 23-10-2026'], ['tradedate:DDMMYYYY', 'Trade date 23102026'], ['tradedate:DD-MON-YYYY', 'Trade date 23-OCT-2026'], ['tradedate:YYYY-MM-DD', 'Trade date 2026-10-23'], ['prevtradedate:DD-MM-YYYY', 'Previous trading day'], ['today:DD-MM-YYYY', 'Today']].map(([v, l]) => `<option value="${v}">${l}</option>`).join('')}</select>
      ${vars.length ? `<select class="in sm" id="sx-var2"><option value="">a value read earlier…</option>${vars.map(v => `<option>${esc(v)}</option>`).join('')}</select>` : ''}</div>` : ''}
    ${k === 'press' ? `<div class="fld"><label for="sx-key">Key</label><select class="in" id="sx-key">${['Enter', 'Tab', 'Escape', 'Backspace', 'Delete', 'Space', 'ArrowDown', 'ArrowUp', 'ArrowLeft', 'ArrowRight', 'Home', 'End', 'PageDown', 'PageUp', 'F5'].map(x => `<option ${s.key === x ? 'selected' : ''}>${x}</option>`).join('')}</select></div>` : ''}
    ${k === 'wait' ? `<div class="fld"><label for="sx-sec">Seconds</label><input class="in" type="number" min="1" max="300" id="sx-sec" value="${s.seconds || 5}"></div>` : ''}
    ${k === 'extract' || k === 'download' ? `<div class="fld"><label for="sx-var">Keep it as</label><input class="in mono" id="sx-var" value="${esc(s.var || (k === 'download' ? 'file' + (fileVars.length + 1) : 'value' + (vars.length + 1)))}" placeholder="lower_case_name"></div>` : ''}
    ${k === 'upload' || k === 'ledgerline_upload' ? `<div class="fld"><label for="sx-var">File</label><select class="in" id="sx-var">${fileVars.map(v => `<option ${s.var === v ? 'selected' : ''}>${esc(v)}</option>`).join('') || '<option value="">Download a file in an earlier step first</option>'}</select></div>` : ''}
    ${k === 'ledgerline_upload' ? `<div class="fld"><label for="sx-src">Load as</label><select class="in" id="sx-src">${RP.home.sources.map(x => `<option ${s.source === x ? 'selected' : ''}>${esc(x)}</option>`).join('')}</select></div>` : ''}
    ${k === 'pause' || k === 'notify' ? `<div class="fld"><label for="sx-msg">${k === 'pause' ? 'What to ask' : 'Message'}</label><input class="in" id="sx-msg" value="${esc(s.message || (k === 'pause' ? 'Solve the captcha in the browser, then click Continue.' : ''))}"></div>` : ''}
    <div class="grid g2"><div class="fld"><label for="sx-label2">Step name (optional)</label><input class="in" id="sx-label2" value="${esc(s.label || '')}"></div>
    ${needT ? `<div class="fld"><label for="sx-to">Wait up to (seconds)</label><input class="in" type="number" min="1" max="600" id="sx-to" value="${s.timeout || 20}"></div>` : '<div></div>'}</div>
    <label class="check"><input type="checkbox" id="sx-opt" ${s.optional ? 'checked' : ''}> Optional: carry on if this step cannot be done (a pop-up that only sometimes appears)</label></div>
    <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="rpStepSave">${isNew ? 'Add' : 'Done'}</button></div>`);
}
function rpInsert(tok) {
  const el = $('sx-val') || $('sx-msg') || $('sx-url'); if (!el || !tok) return;
  const p = el.selectionStart ?? el.value.length; el.value = el.value.slice(0, p) + '{{' + tok + '}}' + el.value.slice(el.selectionEnd ?? p); el.focus();
}
document.addEventListener('change', e => {
  if (e.target.id === 'sx-cred' && e.target.value) { rpInsert('secret:' + e.target.value); e.target.value = ''; }
  if (e.target.id === 'sx-date' && e.target.value) { rpInsert(e.target.value); e.target.value = ''; }
  if (e.target.id === 'sx-var2' && e.target.value) { rpInsert(e.target.value); e.target.value = ''; }
});

/* ---------- recording, runs, logins, computers ---------- */
function rpPoll(fn, ms) { clearTimeout(RP.timer); RP.timer = setTimeout(async () => { if (!document.querySelector('.md.rp-live')) return; try { await fn(); } catch (e) {} }, ms); }
function rpRecModal() {
  const r = RP.rec, live = r.status === 'Waiting' || r.status === 'Recording';
  modal(`<div class="hd"><div><h2>${live ? (r.status === 'Waiting' ? 'Waiting for your computer…' : 'Recording') : r.status === 'Done' ? 'Recorded' : 'Recording cancelled'}</h2><p>${r.status === 'Waiting' ? 'The bot on your computer opens a browser window in a moment. If nothing happens, check that ledgerline-bot is running there.' : r.status === 'Recording' ? 'Do the task in the bot’s browser window. Each step appears here as you go. Passwords you type are not recorded: steps that need one are linked to your saved login for the site.' : ''}</p></div></div>
    <div class="bd"><ol class="rp-steps live">${(r.steps || []).map((s, i) => `<li><span class="rp-n">${i + 1}</span><div class="rp-st"><small>${esc(KIND(s.kind).Name)}</small><div>${stepText(s)}</div></div></li>`).join('') || '<li class="empty">No steps yet</li>'}</ol></div>
    <div class="ft">${live ? `<button class="btn" data-act="rpRecStop" data-id="cancel">Cancel</button><button class="btn pri" data-act="rpRecStop">Stop and save</button>` : r.saved ? `<button class="btn pri" data-act="rpEdit" data-id="${esc(r.saved)}">Check the steps</button>` : '<button class="btn" data-act="close">Close</button>'}</div>`);
  document.querySelector('#mroot .md').classList.add('rp-live');
  const ol = document.querySelector('.rp-steps.live'); if (ol) ol.scrollTop = ol.scrollHeight;
  if (live || (r.stop && r.status !== 'Done' && r.status !== 'Cancelled')) rpPoll(async () => { RP.rec = await get('/v1/rpa/record/' + encR(r.id)); rpRecModal(); }, 1500);
}
async function rpRunModal(id) {
  const r = RP.run = await get('/v1/rpa/runs/' + encR(id));
  const res = {}; (r.results || []).forEach(x => res[x.step] = x);
  const live = ['Queued', 'Running', 'Paused'].includes(r.status);
  modal(`<div class="hd"><div><h2>${esc(r.flow_name)}</h2><p>${esc(r.id)} · ${statusPill(r.status)} · ${esc(r.trigger)} · ${r.status === 'Queued' ? 'waiting for the computer to pick it up' : 'started ' + fmtStamp(r.started)}${r.ended ? ' · finished ' + fmtStamp(r.ended) : ''}</p></div><button class="x" data-act="close" aria-label="Close">×</button></div>
    <div class="bd">${r.status === 'Paused' ? `<div class="note warn"><b>The bot is waiting for you.</b> ${esc(r.pause)} <button class="btn sm pri" data-act="rpCtl" data-id="continue">Continue</button></div>` : ''}${r.error ? `<div class="errbox"><b>${esc(r.error)}</b></div>` : ''}
    <ol class="rp-steps">${(r.steps || []).map((s, i) => { const x = res[i]; return `<li class="${x ? ({ Done: 'ok', Failed: 'bad', Skipped: 'skip' }[x.status] || '') : ''}"><span class="rp-n">${x ? ({ Done: '✓', Failed: '✗', Skipped: '–' }[x.status] || i + 1) : i + 1}</span><div class="rp-st"><small>${esc(KIND(s.kind).Name)}${x?.ms ? ' · ' + (x.ms / 1000).toFixed(1) + ' s' : ''}</small><div>${stepText(s)}</div>${x?.message ? `<div class="small ${x.status === 'Failed' ? 'neg' : 'dim'}">${esc(x.message)}</div>` : ''}</div></li>`; }).join('')}</ol>
    ${r.shot ? '<div class="fld"><label>The screen when it stopped</label><img class="rp-shot" id="rp-shot" alt="Screen when the run stopped"></div>' : ''}</div>
    <div class="ft">${live && r.user === RP.home?.me ? '<button class="btn" data-act="rpCtl" data-id="cancel">Cancel the run</button>' : ''}<button class="btn" data-act="close">Close</button></div>`);
  document.querySelector('#mroot .md').classList.add('rp-live');
  if (r.shot) { try { const b = await fetch(`/v1/rpa/runs/${encR(r.id)}/shot`, { headers: { 'X-Requested-With': 'ledgerline' } }); if (b.ok) $('rp-shot').src = URL.createObjectURL(await b.blob()); } catch (e) {} }
  if (live) rpPoll(() => rpRunModal(id), 2000); else if (S.view === 'rpa' && !RP.edit) { RP.home = await get('/v1/rpa'); const p = $('page'); if (p && !document.querySelector('.md:not(.rp-live)')) p.innerHTML = V.rpa.html(RP.home); }
}
function rpChooseAgent(cb) {
  const pcs = rpMine();
  if (pcs.length === 1) return cb(pcs[0].id);
  modal(`<div class="hd"><div><h2>Which computer?</h2><p>The bot must be running there.</p></div><button class="x" data-act="close" aria-label="Close">×</button></div><div class="bd"><div class="ch-pick">${pcs.map(a => `<button data-act="rpPick" data-id="${esc(a.id)}"><b>${esc(a.name)}</b><small>${a.online ? 'On' : 'Off: last seen ' + fmtStamp(a.seen)}</small></button>`).join('')}</div></div>`);
  RP.pick = cb;
}

Object.assign(A, {
  rpEdit: async id => { closeModal(); RP.edit = { id }; S.view = 'rpa'; await render(); },
  rpBack: () => { RP.edit = null; render({ keep: true }); },
  rpNew: () => { RP.edit = { name: '', steps: [], shared: false }; render({ keep: true }); },
  rpSave: async () => {
    rpCollect(); const f = RP.edit;
    const body = { name: f.name, about: f.about, shared: f.shared, steps: f.steps, schedule: f.schedule.active ? f.schedule : (f.id ? { ...f.schedule, active: false } : null) };
    try { const r = await api(f.id ? 'PUT' : 'POST', '/v1/rpa/flows' + (f.id ? '/' + encR(f.id) : ''), body); RP.edit = r; toast('Workflow saved'); render({ keep: true }); } catch (e) { toast(e.message, true); }
  },
  rpCopy: () => { rpCollect(); RP.edit = { name: RP.edit.name + ' (copy)', about: RP.edit.about, steps: JSON.parse(JSON.stringify(RP.edit.steps || [])).map(s => { delete s.id; return s; }), shared: false }; toast('Copy made: choose your own saved logins, then save'); render({ keep: true }); },
  rpDelete: () => { if (!RP.edit?.id) return; act(() => api('DELETE', '/v1/rpa/flows/' + encR(RP.edit.id)), 'Workflow deleted').then(() => { RP.edit = null; render({ keep: true }); }); },
  rpAddStep: () => { rpCollect(); rpStepModal(-1); },
  rpStep: i => { rpCollect(); rpStepModal(+i); },
  rpStepSave: () => {
    const { i, s } = RP.stepDraft, v = id => $(id)?.value ?? '';
    const t = { text: v('sx-text'), label: v('sx-label'), placeholder: v('sx-ph'), css: v('sx-css'), name: v('sx-name'), tag: s.target?.tag || '', type: s.target?.type || '' };
    if ($('sx-text')) s.target = t;
    if ($('sx-url')) s.url = v('sx-url').trim();
    if ($('sx-val')) { s.value = v('sx-val'); if (/\{\{secret:/.test(s.value)) delete s.needs_secret; }
    if ($('sx-key')) s.key = v('sx-key');
    if ($('sx-sec')) s.seconds = +v('sx-sec');
    if ($('sx-var')) s.var = v('sx-var').trim();
    if ($('sx-src')) s.source = v('sx-src');
    if ($('sx-msg')) s.message = v('sx-msg');
    if ($('sx-to')) s.timeout = +v('sx-to') === 20 ? 0 : +v('sx-to');
    s.label = v('sx-label2'); s.optional = $('sx-opt').checked;
    if (s.needs_secret === 'username') delete s.needs_secret;
    const f = RP.edit; f.steps = f.steps || [];
    if (i === -1) f.steps.push(s); else f.steps[i] = s;
    closeModal(); $('page').innerHTML = groupBar('rpa') + rpEditor(); bindPage();
  },
  rpMove: id => { rpCollect(); const [i, d] = id.split(':').map(Number), st = RP.edit.steps; [st[i], st[i + d]] = [st[i + d], st[i]]; $('page').innerHTML = groupBar('rpa') + rpEditor(); },
  rpDelStep: i => { rpCollect(); RP.edit.steps.splice(+i, 1); $('page').innerHTML = groupBar('rpa') + rpEditor(); },
  rpRecord: id => {
    const flow = id ? RP.edit : null;
    if (flow) rpCollect();
    modal(`<div class="hd"><div><h2>${flow ? 'Record more steps' : 'Record a task'}</h2><p>The bot opens a browser window on your computer at this address. Do the task there; steps are added as you go.</p></div><button class="x" data-act="close" aria-label="Close">×</button></div>
      <div class="bd rp-form">${flow ? '' : '<div class="fld"><label for="rr-name">Name</label><input class="in" id="rr-name" placeholder="e.g. Download NSE trade files" maxlength="80"></div>'}
      <div class="fld"><label for="rr-url">Start at</label><input class="in mono" id="rr-url" value="${esc(flow ? '' : location.origin)}" placeholder="https://"></div>
      <div class="fld"><label for="rr-pc">On</label><select class="in" id="rr-pc">${rpMine().map(a => `<option value="${esc(a.id)}">${esc(a.name)}${a.online ? '' : ' (off)'}</option>`).join('')}</select></div>
      <div class="note small">Tip: save the site’s login under <b>Saved logins</b> first; the password box is then linked to it as you record. Captchas and SMS codes become “Ask me” steps.</div></div>
      <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="rpRecGo" data-id="${esc(flow?.id || '')}">Start recording</button></div>`);
  },
  rpRecGo: async id => {
    try { RP.rec = await post('/v1/rpa/record', { agent: $('rr-pc').value, flow: id || '', name: $('rr-name')?.value || '', start_url: $('rr-url').value.trim() }); rpRecModal(); } catch (e) { toast(e.message, true); }
  },
  rpRecStop: async how => { try { RP.rec = await post(`/v1/rpa/record/${encR(RP.rec.id)}/stop`, { cancel: how === 'cancel' }); rpRecModal(); } catch (e) { toast(e.message, true); } },
  rpRun: id => rpChooseAgent(async pc => { try { const r = await post(`/v1/rpa/flows/${encR(id)}/run`, { agent: pc }); rpRunModal(r.id); } catch (e) { toast(e.message, true); } }),
  rpPick: id => { const cb = RP.pick; RP.pick = null; closeModal(); cb && cb(id); },
  rpOpenRun: id => rpRunModal(id),
  rpCtl: async what => { try { await post(`/v1/rpa/runs/${encR(RP.run.id)}/${what}`, {}); rpRunModal(RP.run.id); } catch (e) { toast(e.message, true); } },
  rpPair: async () => {
    try {
      const p = await post('/v1/rpa/pair', {});
      const cmd = `ledgerline-bot pair --server ${p.server} --firm ${p.tenant} --code ${p.code}`;
      modal(`<div class="hd"><div><h2>Add this computer</h2><p>The code works once, for ten minutes.</p></div><button class="x" data-act="close" aria-label="Close">×</button></div><div class="bd">
        <ol class="rp-howto"><li>Get <b>ledgerline-bot</b> for this computer from your administrator (Windows, Mac or Linux), and have Google Chrome or Microsoft Edge installed.</li>
        <li>In a terminal (Command Prompt on Windows), run:<pre class="rp-cmd" id="rp-cmd">${esc(cmd)}</pre><button class="btn sm" data-act="rpCopyCmd">Copy</button></li>
        <li>Then start it, and keep it running while you want automations to run:<pre class="rp-cmd">ledgerline-bot</pre></li></ol>
        <div class="note small">The bot keeps its own browser profile, so its logins stay apart from your browser’s. It never stores passwords: Ledgerline gives it each one only for the step that types it.</div></div>
        <div class="ft"><button class="btn pri" data-act="close">Done</button></div>`);
      const n0 = rpMine().length;
      const watch = async k => { if (k > 60 || !$('rp-cmd')) return; try { const h = await get('/v1/rpa'); if (h.agents.filter(a => a.mine).length > n0) { RP.home = h; closeModal(); toast('Computer paired'); UI.rpaTab = 'pcs'; render({ keep: true }); return; } } catch (e) {} setTimeout(() => watch(k + 1), 5000); };
      setTimeout(() => watch(0), 5000);
    } catch (e) { toast(e.message, true); }
  },
  rpCopyCmd: () => { try { navigator.clipboard.writeText($('rp-cmd').textContent); toast('Copied'); } catch (e) {} },
  rpRevoke: id => act(() => api('DELETE', '/v1/rpa/agents/' + encR(id)), 'Computer removed: it can no longer run anything'),
  rpCred: id => {
    const c = RP.home.creds.find(x => x.id === id) || { name: '', site: '', username: '', notes: '', owner: RP.home.me, users: [] };
    const firm = c.owner === '*firm';
    modal(`<div class="hd"><div><h2>${id ? esc(c.name) : 'Add a login'}</h2><p>For a portal or app a workflow logs in to. ${id ? 'Leave the password blank to keep the saved one.' : ''}</p></div><button class="x" data-act="close" aria-label="Close">×</button></div>
      <form class="bd rp-form" autocomplete="off" onsubmit="return false"><div class="grid g2"><div class="fld"><label for="cr-name">Name</label><input class="in" id="cr-name" value="${esc(c.name)}" placeholder="e.g. NSE ENIT" maxlength="60"></div>
      <div class="fld"><label for="cr-site">Site</label><input class="in mono" id="cr-site" value="${esc(c.site)}" placeholder="https://enit.nseindia.com"></div>
      <div class="fld"><label for="cr-user">User name</label><input class="in mono" id="cr-user" value="${esc(c.username)}" autocomplete="off"></div>
      <div class="fld"><label for="cr-pw">Password</label><input class="in" type="password" id="cr-pw" autocomplete="new-password" placeholder="${id ? 'unchanged' : ''}"></div></div>
      <div class="fld"><label for="cr-totp">Authenticator key (optional)</label><input class="in mono" id="cr-totp" autocomplete="off" placeholder="${c.has_totp ? 'saved; type a new one to replace' : 'the key shown when setting up an authenticator app'}"><span class="dim small">With it, the bot types the 6-digit code itself. Codes sent by SMS are typed by you when the run asks.</span></div>
      <div class="fld"><label for="cr-notes">Notes</label><input class="in" id="cr-notes" value="${esc(c.notes || '')}" maxlength="500"></div>
      ${RP.home.admin && !id ? '<label class="check"><input type="checkbox" id="cr-firm"> A firm login (the firm’s own account), for the people chosen below</label>' : ''}
      ${RP.home.admin && (firm || !id) ? `<div class="fld" id="cr-usersbox" ${firm ? '' : 'hidden'}><label>Who may use it in their workflows</label><div class="ch-members">${(RP.home.users || []).map(u => `<label class="check"><input type="checkbox" data-cru="${esc(u.id)}" ${(c.users || []).includes(u.id) ? 'checked' : ''}> ${esc(u.name)} <span class="dim small">${esc(u.role)}</span></label>`).join('')}</div></div>` : ''}</form>
      <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="rpCredSave" data-id="${esc(id || '')}">Save</button></div>`);
  },
  rpCredSave: id => {
    const body = { name: $('cr-name').value, site: $('cr-site').value, username: $('cr-user').value, password: $('cr-pw').value, totp: $('cr-totp').value, notes: $('cr-notes').value,
      firm: !!$('cr-firm')?.checked, users: [...document.querySelectorAll('[data-cru]:checked')].map(x => x.dataset.cru) };
    act(() => api(id ? 'PUT' : 'POST', '/v1/rpa/creds' + (id ? '/' + encR(id) : ''), body), 'Login saved');
  },
  rpCredDel: id => act(() => api('DELETE', '/v1/rpa/creds/' + encR(id)), 'Login removed')
});
document.addEventListener('change', e => { if (e.target.id === 'cr-firm') $('cr-usersbox').hidden = !e.target.checked; });
// Ledgy's links to a run ("rpa:RUN-00012") open it.
const _rpGo = A.go;
A.go = id => { const [v, t] = String(id).split(':'); if (v === 'rpa') { RP.edit = null; if (t) RP.openRun = t; } _rpGo(id); if (v === 'rpa' && t) setTimeout(() => { if (RP.openRun) { rpRunModal(RP.openRun); RP.openRun = null; } }, 600); };
