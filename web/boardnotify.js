'use strict';
/* Ledgerline console: board notifications by email (Trello emails people
   their notifications). The foot of the bell panel says how email is set
   for the person and opens Notification settings: their address (checked
   with a code sent to it), how often, which kinds, a pause of a day or a
   week, the emails sent lately, and for Admins the firm's switch.
   Plugs into BOARDS.bellTools (boards.js).
   Server: internal/ops/boardnotify.go, internal/api/boardnotify.go.
   Actions are A.bnSomething; UI state UI.bnSomething. */

Object.assign(UI, { bnData: null, bnAt: 0, bnLoading: false, bnDraft: null, bnStep: '', bnDev: '', bnErr: '', bnBusy: false });

const BN_KINDS = [
  ['added', 'Added to a card', 'Someone adds you to a card or gives you a checklist item'],
  ['mention', 'Mentions', 'Someone @mentions you in a comment'],
  ['comment', 'Comments on cards I watch', 'A comment on a card you watch or are on'],
  ['moved', 'Moves', 'A card you watch moves to another list'],
  ['due', 'Due-date reminders', 'A card you are on or watch is due soon or overdue'],
  ['watch', 'Other activity I watch', 'Anything else on the cards and boards you watch'],
  ['automation', 'Automation', 'Messages from a board’s rules, buttons and commands']];
const BN_EVERY = [
  ['now', 'Right away', 'Within about 5 minutes. Notifications that come close together share one email.'],
  ['hourly', 'Every hour', 'One email at the start of each hour, with what came in the hour before.'],
  ['daily', 'Once a day', 'One email a day, at the time you choose.'],
  ['never', 'Never', 'Notifications stay in the bell only.']];
const bnMONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

function bnWhen(iso) {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}:\d{2})/.exec(iso || ''); if (!m) return '';
  const t = new Date(iso);
  const d = isNaN(t) ? null : new Date(t.getTime() + 330 * 60000); // IST
  return d ? `${d.getUTCDate()} ${bnMONTHS[d.getUTCMonth()]}, ${String(d.getUTCHours()).padStart(2, '0')}:${String(d.getUTCMinutes()).padStart(2, '0')}` : `${+m[3]} ${bnMONTHS[+m[2] - 1]}, ${m[4]}`;
}
function bnHow(d) {
  if (!d) return '';
  if (d.firm_off) return 'Board email is off for the firm';
  if (!d.email) return d.pending ? 'Email: waiting for your code' : 'Email: no address yet';
  if (d.paused_until) return 'Email paused until ' + bnWhen(d.paused_until);
  return { now: 'Email: right away', hourly: 'Email: every hour', daily: `Email: daily at ${d.at}`, never: 'Email: never' }[d.every] || '';
}
async function bnLoad() {
  if (UI.bnLoading) return UI.bnData;
  UI.bnLoading = true;
  try { UI.bnData = await get('/v1/board-notify'); } catch (e) { /* the bell works without it */ } finally { UI.bnLoading = false; UI.bnAt = Date.now(); }
  if (BD.pop?.key === 'bell') bdPopRefresh();
  return UI.bnData;
}

BOARDS.bellTools.push(() => {
  if (!UI.bnLoading && (!UI.bnData || Date.now() - UI.bnAt > 60000)) setTimeout(bnLoad, 0);
  const d = UI.bnData, on = d && d.email && !d.firm_off && !d.paused_until && d.every !== 'never';
  return `<div class="bn-foot"><span class="bn-sum${on ? ' on' : ''}">${bdI('bell')}<span>${esc(bnHow(d) || 'Email settings')}</span></span><button class="btn sm" data-act="bnOpen">${bdI('gear')}Notification settings</button></div>`;
});

/* ---------- the window ---------- */
async function bnOpen() {
  if (typeof bdPopClose === 'function') bdPopClose(true);
  UI.bnStep = ''; UI.bnDev = ''; UI.bnErr = ''; UI.bnBusy = false;
  modal(mhd('Notification settings', 'How your board notifications reach you by email. The bell in Boards always has every one.') + '<div class="bd bn-bd" id="bn-bd"><p class="bn-wait">Loading…</p></div><div class="ft" id="bn-ft"></div>');
  document.querySelector('#mroot .md')?.classList.add('bn-md');
  const d = await bnLoad();
  if (!$('bn-bd')) return;
  if (!d) { $('bn-bd').innerHTML = '<p class="bn-err" role="alert">Your notification settings could not be loaded. Try again in a moment.</p>'; return; }
  UI.bnDraft = { every: d.every, at: d.at, kinds: [...d.kinds] };
  if (!d.email) UI.bnStep = d.pending ? 'code' : 'address';
  bnRender();
}
function bnRender() {
  const bd = $('bn-bd'), ft = $('bn-ft'); if (!bd || !UI.bnData) return;
  const a = document.activeElement, keep = a && bd.contains(a) ? (a.id || '') : '';
  // What is typed in the address and code boxes survives a redraw.
  const typed = Object.fromEntries(['bn-email', 'bn-code'].map(id => [id, $(id)?.value]).filter(x => x[1] != null));
  bd.innerHTML = bnBodyHTML();
  Object.entries(typed).forEach(([id, v]) => { const el = $(id); if (el && v) el.value = v; });
  ft.innerHTML = `<button class="btn" data-act="close">Close</button><button class="btn pri" data-act="bnSave" ${UI.bnBusy ? 'disabled' : ''}>Save</button>`;
  if (keep && $(keep)) $(keep).focus();
}
function bnBodyHTML() {
  const d = UI.bnData, f = UI.bnDraft, step = UI.bnStep;
  const parts = [];
  if (d.firm_off) parts.push(`<div class="bn-note bad" role="status"><b>Board email is switched off for the whole firm.</b> Nothing is emailed until an Admin switches it on again.${d.can_firm ? ' <button class="btn sm" data-act="bnFirm">Switch it on</button>' : ''}</div>`);
  else if (!d.mail_server) parts.push('<div class="bn-note warn" role="status"><b>No mail server is set up yet,</b> so nothing can be emailed. An Admin sets one in Settings → Connections.</div>');

  // The address.
  let addr = '';
  if (d.email && step !== 'address' && step !== 'code') {
    addr = `<div class="bn-addr"><span class="bn-addr-v"><b>${esc(d.email)}</b><span class="bn-ok">Confirmed</span></span><span class="bn-sp"></span><button class="btn sm" data-act="bnChange">Change</button><button class="btn sm bn-ghost" data-act="bnRemove">Stop emails</button></div>`;
  } else if (step === 'code' && d.pending) {
    addr = `<p class="bn-p">We sent a 6-digit code to <b>${esc(d.pending)}</b>. It is valid for 30 minutes.</p>
      ${UI.bnDev ? `<p class="bn-dev">Development server: the code is <b>${esc(UI.bnDev)}</b>.</p>` : ''}
      <div class="bn-inline"><label class="bd-sr" for="bn-code">Code</label><input class="in bn-code" id="bn-code" inputmode="numeric" autocomplete="one-time-code" maxlength="6" placeholder="6-digit code" data-enter="bnConfirm"><button class="btn pri sm" data-act="bnConfirm" ${UI.bnBusy ? 'disabled' : ''}>Confirm</button></div>
      <div class="bn-links"><button class="bd-lnk" data-act="bnResend">Send a new code</button><button class="bd-lnk" data-act="bnChange">Use another address</button>${d.email ? '<button class="bd-lnk" data-act="bnKeep">Keep ' + esc(d.email) + '</button>' : ''}</div>`;
  } else {
    addr = `<p class="bn-p">Use your work address. Ledgerline sends a code to it first, to check it is yours.</p>
      <div class="bn-inline"><label class="bd-sr" for="bn-email">Email address</label><input class="in" id="bn-email" type="email" autocomplete="email" maxlength="200" placeholder="name@yourfirm.in" value="${esc(d.pending || '')}" data-enter="bnSendCode"><button class="btn pri sm" data-act="bnSendCode" ${UI.bnBusy ? 'disabled' : ''}>Send code</button></div>
      ${d.email ? `<div class="bn-links"><button class="bd-lnk" data-act="bnKeep">Keep ${esc(d.email)}</button></div>` : ''}`;
  }
  if (UI.bnErr) addr += `<p class="bn-err" role="alert">${esc(UI.bnErr)}</p>`;
  parts.push(`<section class="bn-sec" aria-labelledby="bn-h-addr"><h3 id="bn-h-addr">Email address</h3>${addr}</section>`);

  // How often.
  parts.push(`<fieldset class="bn-sec bn-fs"><legend>How often</legend><div class="bn-opts">${BN_EVERY.map(([k, l, s]) => `<label class="bn-opt${f.every === k ? ' on' : ''}"><input type="radio" name="bn-every" value="${k}" data-bn="every" ${f.every === k ? 'checked' : ''}><span><b>${l}</b>${k === 'daily' ? ` <span class="bn-at">at <input class="in bn-time" id="bn-at" type="time" data-bn="at" value="${esc(f.at)}" aria-label="Time of the daily email (IST)"> IST</span>` : ''}<small>${s}</small></span></label>`).join('')}</div></fieldset>`);

  // Which kinds.
  parts.push(`<fieldset class="bn-sec bn-fs"${f.every === 'never' ? ' disabled' : ''}><legend>What to email</legend><div class="bn-kinds">${BN_KINDS.map(([k, l, s]) => `<label class="bn-kind"><input type="checkbox" value="${k}" data-bn="kind" ${f.kinds.includes(k) ? 'checked' : ''}><span><b>${l}</b><small>${s}</small></span></label>`).join('')}</div>
    <p class="bn-hint">An automation that emails you on purpose (the “Email people” action) always comes, within about 5 minutes, unless you choose Never or pause email.</p></fieldset>`);

  // Pause.
  parts.push(`<section class="bn-sec" aria-labelledby="bn-h-pause"><h3 id="bn-h-pause">Pause</h3>${d.paused_until
    ? `<div class="bn-inline"><span class="bn-p">Email is paused until <b>${esc(bnWhen(d.paused_until))}</b>.</span><button class="btn sm" data-act="bnPause" data-id="0">Start again now</button></div>`
    : `<div class="bn-inline"><button class="btn sm" data-act="bnPause" data-id="1">Pause for 1 day</button><button class="btn sm" data-act="bnPause" data-id="7">Pause for 1 week</button></div>`}
    <p class="bn-hint">What comes in while email is paused stays in the bell; it is not emailed later.</p></section>`);

  // Recent emails.
  if ((d.recent || []).length) parts.push(`<section class="bn-sec" aria-labelledby="bn-h-sent"><h3 id="bn-h-sent">Sent lately</h3><ul class="bn-sent">${d.recent.map(r => `<li><span>${esc(bnWhen(r.at))}</span><span>${r.count} notification${r.count === 1 ? '' : 's'} to ${esc(r.to)}</span><span class="bn-res${/^Sent/.test(r.result) ? ' ok' : ' bad'}">${esc(r.result)}</span></li>`).join('')}</ul></section>`);

  // The firm.
  if (d.can_firm) parts.push(`<section class="bn-sec bn-firm" aria-labelledby="bn-h-firm"><h3 id="bn-h-firm">For the whole firm</h3><div class="bn-inline"><span class="bn-p">Board notifications by email are <b>${d.firm_off ? 'off' : 'on'}</b> for everyone.</span><button class="btn sm${d.firm_off ? '' : ' bn-ghost'}" data-act="bnFirm" aria-pressed="${!d.firm_off}">${d.firm_off ? 'Switch on' : 'Switch off'}</button></div><p class="bn-hint">Admins only. Switched back on, it emails what comes in from then on.</p></section>`);

  parts.push('<p class="bn-hint bn-what">Each email holds the board’s name, the card’s number and title, the notification and a link to the card. Nothing else from the card is sent: no description, attachments or other comments.</p>');
  return parts.join('');
}

function bnApply(v, msg) { UI.bnData = v; UI.bnAt = Date.now(); UI.bnErr = ''; if (msg) toast(msg); bnRender(); }
async function bnDo(fn) {
  UI.bnBusy = true; UI.bnErr = ''; bnRender();
  try { await fn(); } catch (e) { UI.bnErr = e.message; toast(e.message, true); }
  UI.bnBusy = false; bnRender();
}
document.addEventListener('change', e => {
  const el = e.target; if (!el.dataset?.bn || !el.closest('#bn-bd') || !UI.bnDraft) return;
  const f = UI.bnDraft, k = el.dataset.bn;
  if (k === 'every') { f.every = el.value; bnRender(); }
  if (k === 'at') f.at = el.value;
  if (k === 'kind') f.kinds = el.checked ? [...new Set([...f.kinds, el.value])] : f.kinds.filter(x => x !== el.value);
});
// (Enter in the address and code boxes: boards.js runs data-enter actions.)

Object.assign(A, {
  bnOpen: () => bnOpen(),
  bnChange: () => { UI.bnStep = 'address'; UI.bnErr = ''; bnRender(); $('bn-email')?.focus(); },
  bnKeep: () => { UI.bnStep = ''; UI.bnErr = ''; bnRender(); },
  bnSendCode: () => bnDo(async () => {
    const email = ($('bn-email')?.value || UI.bnData?.pending || '').trim();
    if (!email) { throw new Error('Give your email address'); }
    const v = await post('/v1/board-notify/address', { email });
    UI.bnStep = 'code'; UI.bnDev = v.dev_code || '';
    bnApply(v, /^Sent/.test(v.sent) ? `Code sent to ${email}` : `The code was not sent: ${v.sent}`);
    setTimeout(() => $('bn-code')?.focus(), 0);
  }),
  bnResend: () => bnDo(async () => {
    const v = await post('/v1/board-notify/address', { email: UI.bnData?.pending || '' });
    UI.bnDev = v.dev_code || ''; bnApply(v, /^Sent/.test(v.sent) ? 'A new code is on its way' : `The code was not sent: ${v.sent}`);
  }),
  bnConfirm: () => bnDo(async () => {
    const code = ($('bn-code')?.value || '').trim();
    if (!/^\d{6}$/.test(code)) throw new Error('Type the 6-digit code from the email');
    const v = await post('/v1/board-notify/confirm', { code });
    UI.bnStep = ''; UI.bnDev = ''; bnApply(v, `Board emails now go to ${v.email}`);
  }),
  bnRemove: () => bnDo(async () => { const v = await post('/v1/board-notify/address', { email: '' }); UI.bnStep = 'address'; bnApply(v, 'No more board emails: the address is removed'); }),
  bnPause: id => bnDo(async () => { const v = await post('/v1/board-notify/pause', { days: +id || 0 }); bnApply(v, +id ? `Board email paused until ${bnWhen(v.paused_until)}` : 'Board email is on again'); }),
  bnFirm: () => bnDo(async () => { const off = !UI.bnData.firm_off; const v = await post('/v1/board-notify/firm', { off }); bnApply(v, off ? 'Board email is off for the whole firm' : 'Board email is on for the firm'); }),
  bnSave: () => bnDo(async () => {
    const f = UI.bnDraft;
    const v = await api('PUT', '/v1/board-notify', { every: f.every, at: f.at, kinds: f.kinds });
    bnApply(v, f.every === 'never' ? 'Saved: board notifications stay in the bell' : 'Notification settings saved');
    if (BD.pop?.key === 'bell') bdPopRefresh();
  })
});
