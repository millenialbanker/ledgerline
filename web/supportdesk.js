'use strict';
/* Ledgerline support desk (/support-desk): where Ledgerline's own support
   people sign in (their operator login, never a firm's), ask a firm for
   access, see whether it was approved, and open or end the session the
   firm approved. Nothing of a firm's data is shown here; the session opens
   the firm's console with the support banner. Standalone page. */

const $ = id => document.getElementById(id);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const D = { me: null, reqs: [], step: 'login', otp: false, err: '', secret: '' };
const ST = { Pending: 'warn', Approved: 'good', Refused: 'bad', Withdrawn: 'nt', Ended: 'nt', Expired: 'nt' };
const pill = (t, k) => `<span class="pill ${k || 'nt'}">${esc(t)}</span>`;
const stamp = s => { if (!s) return '—'; const t = new Date(s); return isNaN(t) ? esc(s) : esc(t.toLocaleString('en-GB', { timeZone: 'Asia/Kolkata', day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit', hour12: false })); };
function toast(m) { const t = $('toast'); t.textContent = m; t.hidden = false; clearTimeout(toast.t); toast.t = setTimeout(() => t.hidden = true, 4000); }

async function call(method, path, body) {
  const h = { 'X-Requested-With': 'ledgerline' };
  if (body !== undefined) h['Content-Type'] = 'application/json';
  const res = await fetch(path, { method, headers: h, body: body === undefined ? undefined : JSON.stringify(body) });
  let data = null; try { data = await res.json(); } catch (e) {}
  if (!res.ok) { const e = new Error(data?.error?.message || 'HTTP ' + res.status); e.code = data?.error?.code; e.status = res.status; throw e; }
  return data;
}

async function load() {
  try { D.me = await call('GET', '/v1/operator/me'); } catch (e) { D.me = null; D.step = 'login'; return show(); }
  const a = D.me.auth || {};
  if (a.must_change_password) D.step = 'password';
  else if (a.totp_setup_required) D.step = 'totp';
  else { D.step = 'desk'; try { D.reqs = (await call('GET', '/v1/operator/requests')).requests; } catch (e) { D.err = e.message; } }
  show();
}

const card = (h, body) => `<div class="login"><div class="login-card"><div class="brand big"><div class="mark"><i></i><i></i><i></i></div><div><b>Ledgerline</b><small>Support desk</small></div></div><h1>${h}</h1>${body}</div></div>`;
const errBox = () => D.err ? `<div class="errbox"><b>${esc(D.err)}</b></div>` : '';

function show() {
  const p = $('desk');
  if (D.step === 'login') {
    p.innerHTML = card('Sign in', `<p class="dim">For Ledgerline's support team. Use your own support login; a firm's logins do not work here.</p><form id="f">
      <div class="fld"><label for="u">User ID</label><input class="in" id="u" autocomplete="username" required ${D.otp ? 'readonly' : ''} value="${esc(D.user || '')}"></div>
      <div class="fld" ${D.otp ? 'hidden' : ''}><label for="pw">Password</label><input class="in" id="pw" type="password" autocomplete="current-password"></div>
      ${D.otp ? '<div class="fld"><label for="otp">Code from your authenticator app</label><input class="in mono" id="otp" inputmode="numeric" maxlength="7" required></div>' : ''}
      ${errBox()}<button class="btn pri wide" type="submit">${D.otp ? 'Verify' : 'Sign in'}</button></form>`);
    $('f').onsubmit = async e => {
      e.preventDefault(); D.user = $('u').value.trim(); if (!D.otp) D.pw = $('pw').value;
      try { await call('POST', '/v1/operator/login', { user: D.user, password: D.pw, otp: D.otp ? $('otp').value : '' }); D.otp = false; D.pw = ''; D.err = ''; load(); }
      catch (err) { if (err.code === 'otp_required') { D.otp = true; D.err = ''; } else D.err = err.message; show(); }
    };
    return;
  }
  if (D.step === 'password') {
    p.innerHTML = card('Choose a new password', `<p class="dim">You signed in with a one-time password. Choose your own (at least 12 characters).</p><form id="f">
      <div class="fld"><label for="c">One-time password</label><input class="in" id="c" type="password" required></div>
      <div class="fld"><label for="n">New password</label><input class="in" id="n" type="password" minlength="12" required></div>${errBox()}
      <button class="btn pri wide" type="submit">Save</button></form>`);
    $('f').onsubmit = async e => { e.preventDefault(); try { await call('POST', '/v1/operator/password', { current: $('c').value, new: $('n').value }); D.err = ''; load(); } catch (err) { D.err = err.message; show(); } };
    return;
  }
  if (D.step === 'totp') {
    if (!D.secret) { call('POST', '/v1/operator/2fa/setup', {}).then(r => { D.secret = r.secret; show(); }).catch(e => { D.err = e.message; show(); }); }
    p.innerHTML = card('Set up two-factor sign-in', `<p class="dim">Support logins always need a code. Add this key to your authenticator app, then enter the code it shows.</p>
      <div class="secret mono">${esc(D.secret || '…')}</div><form id="f"><div class="fld"><label for="code">Code</label><input class="in mono" id="code" inputmode="numeric" maxlength="7" required></div>${errBox()}
      <button class="btn pri wide" type="submit">Turn on</button></form>`);
    $('f').onsubmit = async e => { e.preventDefault(); try { await call('POST', '/v1/operator/2fa/enable', { code: $('code').value }); D.err = ''; D.secret = ''; load(); } catch (err) { D.err = err.message; show(); } };
    return;
  }
  const open = D.reqs.filter(r => r.status === 'Approved'), pend = D.reqs.filter(r => r.status === 'Pending'), past = D.reqs.filter(r => !['Approved', 'Pending'].includes(r.status));
  const scope = r => `${r.write ? 'read and write' : 'read-only'} · ${r.areas.map(a => esc((D.me.areas.find(x => x.id === a) || { name: a }).name)).join(', ')} · ${r.hours} h`;
  const row = (r, btns) => `<tr><td class="mono">${esc(r.tenant)} · ${esc(r.id)}</td><td class="wrap">${esc(r.reason)}<div class="dim small">Ticket ${esc(r.ticket)}</div></td><td class="small wrap">${scope(r)}</td>
    <td>${pill(r.status === 'Approved' ? 'Approved' : r.status, ST[r.status])}${r.note ? `<div class="dim small wrap">${esc(r.note)}</div>` : ''}${r.end_reason ? `<div class="dim small wrap">${esc(r.end_reason)}</div>` : ''}</td>
    <td class="small nowrap">${r.status === 'Approved' ? 'until ' + stamp(r.expires) : stamp(r.requested)}</td><td class="nowrap">${btns || ''}</td></tr>`;
  const t = (rows, empty) => `<div class="tw"><table class="tbl"><thead><tr><th>Firm · request</th><th>Why</th><th>Scope</th><th>Status</th><th>When</th><th></th></tr></thead><tbody>${rows.length ? rows.join('') : `<tr><td colspan="6" class="empty">${empty}</td></tr>`}</tbody></table></div>`;
  p.innerHTML = `<div class="ph"><div><h1>Support desk</h1><p>Signed in as <b>${esc(D.me.name)}</b> (${esc(D.me.operator)}). You see no firm's data until the firm approves your request. Everything you do in a firm is recorded in its audit trail, and PAN, Aadhaar and bank account numbers stay masked.</p></div><div class="acts"><button class="btn" id="out">Sign out</button></div></div>${errBox()}
    <div class="panel"><div class="hd"><h3>My active sessions</h3><small>Approved access, open until the time shown or until the firm ends it</small></div>${t(open.map(r => row(r, `<button class="btn sm pri" data-start="${esc(r.tenant)}|${esc(r.id)}">Open session</button> <button class="btn sm" data-end="${esc(r.tenant)}|${esc(r.id)}">End</button>`)), 'No open access.')}</div>
    <div class="panel"><div class="hd"><h3>Waiting for the firm</h3></div>${t(pend.map(r => row(r, `<button class="btn sm" data-end="${esc(r.tenant)}|${esc(r.id)}">Withdraw</button>`)), 'Nothing waiting.')}</div>
    <div class="panel"><div class="hd"><h3>Ask a firm for access</h3><small>The firm's Admin approves or refuses</small></div><div class="bd"><form id="rq"><div class="form">
      <div class="fld"><label for="rq-t">Firm</label><select class="in" id="rq-t">${(D.me.firms || []).map(f => `<option value="${esc(f.tenant)}" ${f.blocked ? 'disabled' : ''}>${esc(f.firm)} (${esc(f.tenant)})${f.blocked ? ' · blocks support' : ''}</option>`).join('')}</select></div>
      <div class="fld"><label for="rq-k">Ticket or reference</label><input class="in mono" id="rq-k" maxlength="60" required placeholder="e.g. SR-0042"></div>
      <div class="fld full"><label for="rq-r">Why you need access</label><input class="in" id="rq-r" maxlength="1000" required placeholder="What you will look at and why; the firm reads this"></div>
      <div class="fld full"><label>Areas</label>${D.me.areas.map(a => `<label class="small" style="display:block"><input type="checkbox" data-area="${esc(a.id)}"> <b>${esc(a.name)}</b> <span class="dim">${esc(a.about)}</span></label>`).join('')}</div>
      <div class="fld"><label for="rq-w">Access</label><select class="in" id="rq-w"><option value="0" selected>Read-only</option><option value="1">Read and write</option></select></div>
      <div class="fld"><label for="rq-h">Hours (1–8)</label><input class="in mono" id="rq-h" type="number" min="1" max="8" value="2"></div>
      <div class="fld full"><label><input type="checkbox" id="rq-o"> This is an outage (a firm that turned on its outage rule lets read-only requests in at once)</label></div>
    </div><div class="ft" style="padding:0;border:0"><button class="btn pri" type="submit">Send request</button></div></form></div></div>
    <div class="panel"><div class="hd"><h3>Earlier requests</h3></div>${t(past.slice(0, 50).map(r => row(r)), 'None yet.')}</div>`;
  $('out').onclick = async () => { await call('POST', '/v1/operator/logout').catch(() => {}); D.me = null; D.step = 'login'; show(); };
  $('rq').onsubmit = async e => {
    e.preventDefault();
    const areas = [...document.querySelectorAll('[data-area]')].filter(x => x.checked).map(x => x.dataset.area);
    try { const r = await call('POST', '/v1/operator/requests', { tenant: $('rq-t').value, ticket: $('rq-k').value, reason: $('rq-r').value, areas, write: $('rq-w').value === '1', hours: +$('rq-h').value, outage: $('rq-o').checked });
      D.err = ''; toast(r.status === 'Approved' ? `${r.id} let in under the firm's outage rule` : `${r.id} sent; the firm has been told`); load(); }
    catch (err) { D.err = err.message; show(); }
  };
  document.querySelectorAll('[data-start]').forEach(b => b.onclick = async () => {
    const [t, id] = b.dataset.start.split('|');
    try { const r = await call('POST', `/v1/operator/requests/${encodeURIComponent(t)}/${encodeURIComponent(id)}/start`, {}); location.href = r.open || '/'; } catch (err) { D.err = err.message; show(); }
  });
  document.querySelectorAll('[data-end]').forEach(b => b.onclick = async () => {
    const [t, id] = b.dataset.end.split('|');
    try { await call('POST', `/v1/operator/requests/${encodeURIComponent(t)}/${encodeURIComponent(id)}/end`, {}); toast(id + ' ended'); load(); } catch (err) { D.err = err.message; show(); }
  });
}

load();
