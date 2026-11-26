'use strict';
/* Ledgerline mobile apps: what the staff app (/m/, mstaff.js) and the client
   app (/app/, mclient.js) share. The pages work in any phone browser; inside
   the native shell (mobile/staff, mobile/client: Capacitor) they also use:

   - LedgerlineShell (the shell's own plugin): the device secret kept in the
     iOS keychain or Android keystore, released only by Face ID, Touch ID or
     fingerprint; the rooted or jailbroken phone check; the staff app's
     server address.
   - PushNotifications, App, Device, Preferences, Filesystem and Share
     (Capacitor's own plugins).

   Sign-in: the first time with the password (and two-factor code); then
   the page registers the phone (POST /v1/mobile/devices) and hands the
   device secret to the shell. Next time the shell releases the secret after
   the biometric check and the page unlocks (POST /v1/mobile/unlock). The
   app locks after the firm's idle time, and when it comes back from the
   background after the firm's lock time. No client data is written to the
   console, to local storage or to the shell's logs. */

const $ = id => document.getElementById(id);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const num = v => { const n = parseFloat(String(v ?? '').replace(/[₹,\s]/g, '')); return isNaN(n) ? 0 : n; };
const inr = (v, dp = 0) => { const n = num(v); return (n < 0 ? '−' : '') + '₹' + Math.abs(n).toLocaleString('en-IN', { minimumFractionDigits: dp, maximumFractionDigits: dp }); };
const nf = v => num(v).toLocaleString('en-IN');
const MONS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const fmtDate = d => { const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(d || ''); return m ? `${+m[3]} ${MONS[+m[2] - 1]} ${m[1]}` : esc(d || ''); };
const todayIST = () => new Date(Date.now() + 330 * 60000).toISOString().slice(0, 10);
const pill = (t, k = 'nt') => `<span class="pill ${k}">${esc(t)}</span>`;
const sgn = v => num(v) < 0 ? 'neg' : num(v) > 0 ? 'pos' : '';

const MS = {
  app: document.body.dataset.app,
  firm: new URLSearchParams(location.search).get('firm') || '',
  cfg: null, me: null, device: null, rooted: false, route: '', pending: '',
  screens: {}, tabs: [], title: '', locked: true, lastTouch: Date.now(), pausedAt: 0,
};

/* ---------- API ---------- */
class ApiError extends Error { constructor(status, body) { super(body?.error?.message || ('HTTP ' + status)); this.status = status; this.code = body?.error?.code; this.details = body?.error?.details; } }
async function api(method, path, body, raw) {
  const h = { 'X-Requested-With': 'ledgerline' };
  let b;
  if (raw) b = body; else if (body !== undefined) { b = JSON.stringify(body); h['Content-Type'] = 'application/json'; }
  const res = await fetch(path, { method, headers: h, body: b, credentials: 'same-origin' });
  const ct = res.headers.get('Content-Type') || '';
  let data = null;
  if (ct.includes('json')) { try { data = await res.json(); } catch (e) {} } else data = await res.text();
  if (res.status === 401 && !path.startsWith('/v1/auth/') && !path.startsWith('/v1/mobile/unlock') && !MS.locked) { MS.lock('Your session ended. Unlock to continue.'); throw new ApiError(401, data); }
  if (!res.ok && res.status !== 207) throw new ApiError(res.status, data);
  return data;
}
const get = p => api('GET', p);
const post = (p, b) => api('POST', p, b || {});
const errBox = e => `<div class="m-err" role="alert">${esc(e.message || e)}${Array.isArray(e.details?.fields) ? `<ul>${e.details.fields.map(f => `<li>${esc(f.message)}</li>`).join('')}</ul>` : ''}</div>`;

/* ---------- the native shell ---------- */
const Cap = window.Capacitor;
MS.native = !!(Cap && typeof Cap.isNativePlatform === 'function' && Cap.isNativePlatform());
function plug(name) {
  if (!MS.native) return null;
  try { return (Cap.Plugins && Cap.Plugins[name]) || (Cap.registerPlugin ? Cap.registerPlugin(name) : null); } catch (e) { return null; }
}
const Shell = {
  platform: () => MS.native ? Cap.getPlatform() : 'web',
  key: k => `ll.${MS.app}.${MS.cfg?.tenant || MS.firm || 'firm'}.${k}`,
  async pref(k, v) {
    const P = plug('Preferences'); if (!P) return null;
    if (v === undefined) { const r = await P.get({ key: Shell.key(k) }); return r?.value ? JSON.parse(r.value) : null; }
    if (v === null) return P.remove({ key: Shell.key(k) });
    return P.set({ key: Shell.key(k), value: JSON.stringify(v) });
  },
  async biometry() { const S = plug('LedgerlineShell'); if (!S) return { available: false }; try { return await S.biometry(); } catch (e) { return { available: false }; } },
  saveSecret: v => plug('LedgerlineShell').saveSecret({ key: Shell.key('secret'), value: v }),
  readSecret: reason => plug('LedgerlineShell').readSecret({ key: Shell.key('secret'), reason }).then(r => r.value),
  deleteSecret: () => plug('LedgerlineShell')?.deleteSecret({ key: Shell.key('secret') }).catch(() => {}),
  async integrity() { const S = plug('LedgerlineShell'); if (!S) return { rooted: false }; try { return await S.integrity(); } catch (e) { return { rooted: false }; } },
  async info() { const D = plug('Device'); try { return D ? await D.getInfo() : {}; } catch (e) { return {}; } },
  // A push token, or '' when the person says no or there is no push here.
  async pushToken() {
    const P = plug('PushNotifications'); if (!P) return '';
    try {
      let p = await P.checkPermissions();
      if (p.receive === 'prompt' || p.receive === 'prompt-with-rationale') p = await P.requestPermissions();
      if (p.receive !== 'granted') return '';
      return await new Promise(res => {
        const t = setTimeout(() => res(''), 8000);
        P.addListener('registration', x => { clearTimeout(t); res(x.value || ''); });
        P.addListener('registrationError', () => { clearTimeout(t); res(''); });
        P.register();
      });
    } catch (e) { return ''; }
  },
  // Download a file of the person's and offer it to the phone's share
  // sheet (save to Files, mail, WhatsApp). In a browser it downloads.
  async share(path, name) {
    const res = await fetch(path, { headers: { 'X-Requested-With': 'ledgerline' } });
    if (!res.ok) { let m = 'Could not get the file'; try { m = (await res.json()).error.message; } catch (e) {} throw new Error(m); }
    name = (res.headers.get('Content-Disposition') || '').match(/filename="([^"]+)"/)?.[1] || name || 'document.pdf';
    const blob = await res.blob();
    const F = plug('Filesystem'), Sh = plug('Share');
    if (F && Sh) {
      const data = await new Promise(r => { const fr = new FileReader(); fr.onload = () => r(String(fr.result).split(',')[1]); fr.readAsDataURL(blob); });
      const w = await F.writeFile({ path: name.replace(/[^\w.-]/g, '_'), data, directory: 'CACHE' });
      await Sh.share({ title: name, url: w.uri, dialogTitle: 'Share or save' });
      return;
    }
    const u = URL.createObjectURL(blob), a = document.createElement('a');
    a.href = u; a.download = name; document.body.appendChild(a); a.click(); a.remove(); setTimeout(() => URL.revokeObjectURL(u), 4000);
  },
};

/* ---------- small pieces ---------- */
function toast(m, bad) { const t = $('m-toast'); t.textContent = m; t.className = 'm-toast' + (bad ? ' bad' : ''); t.hidden = false; clearTimeout(toast.t); toast.t = setTimeout(() => { t.hidden = true; }, 3200); }
// A bottom sheet: body HTML and buttons; onOk gets the sheet's form values.
function sheet(title, body, okLabel, onOk, danger) {
  $('m-sheet').innerHTML = `<div class="m-ov" data-x="1"></div><form class="m-sh" role="dialog" aria-modal="true" aria-label="${esc(title)}"><h2>${esc(title)}</h2><div class="m-shb">${body}</div><div id="m-sherr"></div>
    <div class="m-shf"><button type="button" class="btn" data-x="1">Cancel</button>${okLabel ? `<button type="submit" class="btn ${danger ? 'bad' : 'pri'}">${esc(okLabel)}</button>` : ''}</div></form>`;
  const f = $('m-sheet').querySelector('form');
  $('m-sheet').querySelectorAll('[data-x]').forEach(x => { x.onclick = () => closeSheet(); });
  f.onsubmit = async e => { e.preventDefault(); const bt = f.querySelector('[type=submit]'); bt.disabled = true; try { await onOk(Object.fromEntries(new FormData(f))); closeSheet(); } catch (err) { $('m-sherr').innerHTML = errBox(err); } finally { bt.disabled = false; } };
  setTimeout(() => f.querySelector('input,textarea,select')?.focus(), 40);
}
const closeSheet = () => { $('m-sheet').innerHTML = ''; };
const ic = {
  approve: '<path d="M10 2.5a7.5 7.5 0 1 0 0 15 7.5 7.5 0 0 0 0-15Z"/><path d="m6.8 10.2 2.2 2.2 4.2-4.6"/>',
  work: '<path d="M3 11 5 4h10l2 7v5H3z"/><path d="M3 11h4l1 2h4l1-2h4"/>',
  today: '<path d="M10 3a7 7 0 1 0 0 14 7 7 0 0 0 0-14Z"/><path d="M10 6.5V10l2.5 2"/>',
  clients: '<circle cx="8" cy="7" r="3"/><path d="M2.5 16c.8-3 3-4.5 5.5-4.5s4.7 1.5 5.5 4.5M13 4.5a3 3 0 0 1 0 5.5M15 12c1.4.6 2.2 2 2.6 4"/>',
  more: '<circle cx="4.5" cy="10" r="1.3"/><circle cx="10" cy="10" r="1.3"/><circle cx="15.5" cy="10" r="1.3"/>',
  home: '<path d="M3 9.5 10 3.5l7 6V17H3z"/><path d="M8 17v-5h4v5"/>',
  hold: '<path d="M3 17V9M8 17V4M13 17v-6M18 17H2"/>',
  money: '<rect x="2.5" y="5" width="15" height="10" rx="2"/><circle cx="10" cy="10" r="2.2"/>',
  doc: '<path d="M5 2.5h7l3 3v12H5z"/><path d="M8 9h4M8 12h4"/>',
  lock: '<rect x="4" y="9" width="12" height="8.5" rx="2"/><path d="M6.5 9V6.5a3.5 3.5 0 0 1 7 0V9"/>',
  face: '<path d="M3 7V4.5A1.5 1.5 0 0 1 4.5 3H7M13 3h2.5A1.5 1.5 0 0 1 17 4.5V7M17 13v2.5a1.5 1.5 0 0 1-1.5 1.5H13M7 17H4.5A1.5 1.5 0 0 1 3 15.5V13"/><path d="M7.5 8v1M12.5 8v1M10 8v3h-1M7.5 13.5c1.4 1 3.6 1 5 0"/>',
  bell: '<path d="M5 14V9a5 5 0 0 1 10 0v5l1.5 1.5h-13z"/><path d="M8.5 17.5a1.6 1.6 0 0 0 3 0"/>',
  chev: '<path d="m8 5 5 5-5 5"/>',
  ledgy: '<path d="M4 4h12v9H9l-4 3v-3H4z"/>',
  alert: '<path d="M10 3 2.5 16.5h15z"/><path d="M10 8v4M10 14.2v.3"/>',
};
const svg = (k, s = 22) => `<svg width="${s}" height="${s}" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ic[k] || ''}</svg>`;
const row = (label, value, cls = '') => `<div><dt>${label}</dt><dd class="${cls}">${value}</dd></div>`;
const facts = rows => `<dl class="ph-facts">${rows.filter(Boolean).join('')}</dl>`;
const linkRow = (label, route, sub) => `<button class="ph-row" data-go="${esc(route)}"><span class="m-rl">${esc(label)}${sub ? `<small>${esc(sub)}</small>` : ''}</span>${svg('chev', 18)}</button>`;
const empty = t => `<div class="ph-empty">${t}</div>`;

/* ---------- screens ---------- */
// A screen: MS.screens[id] = { title, tab, load: async () => data, html: data => html, back }
async function go(route, opts = {}) {
  if (MS.locked) { MS.pending = route; return; }
  const [id, arg] = String(route || MS.tabs[0][0]).split(':');
  const sc = MS.screens[id] || MS.screens[MS.tabs[0][0]];
  MS.route = route;
  if (!opts.keep) { $('m-page').innerHTML = '<p class="m-load">Loading…</p>'; window.scrollTo(0, 0); }
  renderChrome(sc, id);
  try {
    const d = sc.load ? await sc.load(arg) : null;
    if (MS.route !== route) return;
    $('m-page').innerHTML = sc.html(d, arg);
    sc.after?.(d, arg);
  } catch (e) {
    if (e.status === 401) return;
    $('m-page').innerHTML = `<div class="ph-pg">${errBox(e)}<button class="btn" data-go="${esc(route)}">Try again</button></div>`;
  }
}
function renderChrome(sc, id) {
  const tab = sc.tab || id;
  $('m-top').innerHTML = `${sc.back ? `<button class="ph-back" data-go="${esc(sc.back)}">‹ Back</button>` : `<div class="m-brand">${MS.logo()}<span><b>${esc(MS.title)}</b><small>${esc(MS.sub())}</small></span></div>`}
    <span class="m-push"></span><button class="ph-ib" data-act="lockNow" aria-label="Lock">${svg('lock', 20)}</button>`;
  $('m-tabs').innerHTML = MS.tabs.map(([k, l, i]) => { const n = MS.badge?.(k) || 0; return `<button class="ph-tab ${tab === k ? 'on' : ''}" data-go="${k}" ${tab === k ? 'aria-current="page"' : ''}>${svg(i)}<span>${esc(l)}</span>${n ? `<i>${n > 99 ? '99+' : n}</i>` : ''}</button>`; }).join('');
  $('m-tabs').hidden = false; $('m-top').hidden = false;
}
const refresh = () => go(MS.route, { keep: true });
// The date the screens show: the firm's business date (today, or the last
// business day on a weekend or holiday), from the server.
MS.day = () => MS.cfg?.date || todayIST();
MS.logo = () => '<span class="mark" aria-hidden="true"><i></i><i></i><i></i></span>';
MS.sub = () => MS.cfg?.firm || '';

/* ---------- sign-in, unlock and lock ---------- */
function shellOnly(html) { $('m-top').hidden = true; $('m-tabs').hidden = true; $('m-page').innerHTML = html; }
function gate(inner) {
  return `<div class="m-gate"><div class="m-gcard">${MS.logo()}<h1>${esc(MS.title)}</h1><p class="dim">${esc(MS.cfg?.firm || '')}</p>${MS.rooted ? '<div class="m-warn"><b>This phone looks rooted or jailbroken.</b> Apps and websites on it can read what you see. Your account is safer on a phone that is not.</div>' : ''}${inner}</div>${MS.gateFoot ? MS.gateFoot() : ''}</div>`;
}
MS.showSignIn = function (msg, otp) {
  MS.locked = true;
  const L = MS.login || (MS.login = { user: '' });
  shellOnly(gate(`<form id="m-login" autocomplete="on">${msg ? `<p class="m-msg">${esc(msg)}</p>` : ''}
    ${MS.cfg?.tenant ? '' : `<label class="m-f">Firm code<input class="in" name="tenant" autocapitalize="none" required value="${esc(MS.firm)}"></label>`}
    <label class="m-f">${MS.app === 'client' ? 'Client ID' : 'User ID'}<input class="in" name="user" autocomplete="username" autocapitalize="none" spellcheck="false" required value="${esc(L.user)}" ${otp ? 'readonly' : ''}></label>
    ${otp ? '<label class="m-f">Code from your authenticator app<input class="in mono" name="otp" inputmode="numeric" autocomplete="one-time-code" maxlength="7" required></label>' : '<label class="m-f">Password<input class="in" name="password" type="password" autocomplete="current-password" required></label>'}
    <div id="m-lerr"></div><button class="btn pri m-wide" type="submit">${otp ? 'Verify' : 'Sign in'}</button>
    <p class="dim small">Five wrong attempts lock the account for 15 minutes.</p></form>`));
  $('m-login').onsubmit = async e => {
    e.preventDefault();
    const f = Object.fromEntries(new FormData(e.target));
    if (!otp) { L.user = f.user.trim(); L.pw = f.password; }
    try {
      await api('POST', '/v1/auth/login', { tenant: f.tenant || MS.cfg?.tenant || MS.firm, user: L.user, password: L.pw, otp: f.otp || '' });
      L.pw = ''; await signedIn(true);
    } catch (err) {
      if (err.code === 'otp_required') { MS.showSignIn('', true); return; }
      $('m-lerr').innerHTML = errBox(err);
    }
  };
};
async function signedIn(fresh) {
  MS.me = await get('/v1/me');
  const a = MS.me.auth || {};
  if ((MS.app === 'client') !== (MS.me.role === 'Client')) {
    await post('/v1/auth/logout').catch(() => {});
    MS.showSignIn(MS.app === 'client' ? 'This app is for clients. Staff use the staff app.' : 'This app is for the firm’s staff. Clients use the client app.');
    return;
  }
  if (a.must_change_password) { setupPassword(); return; }
  if (a.totp_setup_required) { setupTOTP(); return; }
  if (MS.me.terms_due) { await acceptTerms(); return; }
  // The app may refuse the person (the staff app's list): say why before
  // registering a phone that would not work.
  const why = MS.access ? await MS.access() : '';
  if (why) { MS.locked = true; shellOnly(gate(`<p class="m-msg">${esc(why)}</p><button class="btn m-wide" data-act="signOut">Sign out</button>`)); return; }
  if (fresh && MS.native && !MS.device) { await offerDevice(); return; }
  ready();
}
// A client accepts the firm's terms and privacy notice at the first
// sign-in and after each change (ops/dataprivacy.go).
async function acceptTerms() {
  let t; try { t = await get('/v1/portal/terms'); } catch (e) { shellOnly(gate(errBox(e))); return; }
  shellOnly(gate(`<form id="m-terms"><p class="m-msg">Please read the terms of use and the privacy notice, and accept them to go on.</p>
    <div class="m-msg" style="white-space:pre-wrap;max-height:30vh;overflow:auto">${esc(t.terms)}</div><div class="m-msg" style="white-space:pre-wrap;max-height:30vh;overflow:auto">${esc(t.notice)}</div>
    <label class="m-f"><input type="checkbox" name="ok" required> I have read and accept them</label><div id="m-lerr"></div><button class="btn pri m-wide">Accept and continue</button><button class="btn m-wide" type="button" data-act="signOut">Sign out</button></form>`));
  $('m-terms').onsubmit = async e => { e.preventDefault(); try { await post('/v1/portal/terms', { terms_version: t.terms_version, notice_version: t.notice_version }); await signedIn(true); } catch (err) { $('m-lerr').innerHTML = errBox(err); } };
}
function setupPassword() {
  shellOnly(gate(`<form id="m-pw"><p class="m-msg">Choose your own password before you go on: at least 12 characters, not your user ID.</p>
    <label class="m-f">Current (one-time) password<input class="in" type="password" name="current" autocomplete="current-password" required></label>
    <label class="m-f">New password<input class="in" type="password" name="next" autocomplete="new-password" minlength="12" required></label>
    <div id="m-lerr"></div><button class="btn pri m-wide">Save password</button></form>`));
  $('m-pw').onsubmit = async e => { e.preventDefault(); const f = Object.fromEntries(new FormData(e.target)); try { await post('/v1/auth/password', { current: f.current, new: f.next }); await signedIn(true); } catch (err) { $('m-lerr').innerHTML = errBox(err); } };
}
async function setupTOTP() {
  let s; try { s = await post('/v1/auth/2fa/setup'); } catch (e) { shellOnly(gate(errBox(e))); return; }
  shellOnly(gate(`<form id="m-tf"><p class="m-msg">Your firm asks for two-factor sign-in. Add this key to an authenticator app (Google Authenticator, Microsoft Authenticator), then enter the code it shows.</p>
    <p class="mono m-key">${esc(s.secret)}</p><a class="btn m-wide" href="${esc(s.uri)}">Open in my authenticator app</a>
    <label class="m-f">Code<input class="in mono" name="code" inputmode="numeric" maxlength="7" required></label><div id="m-lerr"></div><button class="btn pri m-wide">Turn on</button></form>`));
  $('m-tf').onsubmit = async e => { e.preventDefault(); try { await post('/v1/auth/2fa/enable', { code: new FormData(e.target).get('code') }); await signedIn(true); } catch (err) { $('m-lerr').innerHTML = errBox(err); } };
}
// After a password sign-in in the app: register the phone so the next
// sign-in is the fingerprint or face.
async function offerDevice() {
  const b = await Shell.biometry();
  if (!b.available) { ready(); return; }
  const what = b.kind === 'face' ? (Shell.platform() === 'ios' ? 'Face ID' : 'face unlock') : (Shell.platform() === 'ios' ? 'Touch ID' : 'your fingerprint');
  shellOnly(gate(`<div class="m-offer">${svg('face', 48)}<h2>Unlock with ${esc(what)}?</h2><p>Next time, open the app with ${esc(what)} instead of your password. You will be asked for your password again every ${MS.cfg.reauth_days || 30} days, and the app locks after ${MS.cfg.idle_minutes} minutes without use.</p>
    <div id="m-lerr"></div><button class="btn pri m-wide" data-act="enrol">Use ${esc(what)}</button><button class="btn m-wide" data-act="noEnrol">Not now</button></div>`));
}
async function enrol() {
  try {
    const [token, info] = await Promise.all([Shell.pushToken(), Shell.info()]);
    const r = await post('/v1/mobile/devices', { app: MS.app, platform: Shell.platform(), name: info.name || info.model || '', model: info.model || '', app_version: MS.version || '', push_token: token, rooted: MS.rooted });
    await Shell.saveSecret(r.secret);
    MS.device = { id: r.device.id, user: MS.me.actor };
    await Shell.pref('device', MS.device);
    toast(token ? 'This phone is set up, with notifications' : 'This phone is set up');
    ready();
  } catch (e) { $('m-lerr').innerHTML = errBox(e); }
}
MS.lock = async function (msg) {
  if (MS.locked) return;
  MS.locked = true; closeSheet();
  await post('/v1/auth/logout').catch(() => {});
  if (MS.device) showUnlock(msg); else MS.showSignIn(msg || 'Signed out. Sign in again.');
};
function showUnlock(msg, auto) {
  MS.locked = true;
  shellOnly(gate(`<div class="m-offer">${svg('lock', 44)}<h2>Locked</h2>${msg ? `<p class="m-msg">${esc(msg)}</p>` : ''}<div id="m-lerr"></div>
    <button class="btn pri m-wide" data-act="unlock">${svg('face', 18)} Unlock</button><button class="btn m-wide" data-act="usePassword">Sign in with password</button></div>`));
  if (auto) setTimeout(unlock, 250);
}
async function unlock() {
  try {
    const secret = await Shell.readSecret('Unlock ' + MS.title);
    await api('POST', '/v1/mobile/unlock', { tenant: MS.cfg.tenant, device: MS.device.id, secret });
    await signedIn(false);
  } catch (e) {
    if (e.code === 'device_revoked' || e.code === 'reauth') { await forgetDevice(); MS.showSignIn(e.message); return; }
    if ($('m-lerr')) $('m-lerr').innerHTML = errBox(e.status ? e : new Error('Not unlocked. Try again, or sign in with your password.'));
  }
}
async function forgetDevice() { MS.device = null; await Shell.deleteSecret(); await Shell.pref('device', null); }
function ready() {
  MS.locked = false; MS.lastTouch = Date.now();
  const r = MS.pending || MS.route || location.hash.slice(1) || MS.tabs[0][0]; MS.pending = '';
  MS.onReady?.();
  go(r);
}
// Sign out keeps the phone set up; "Remove this phone" forgets it.
MS.signOut = async () => { MS.locked = true; await post('/v1/auth/logout').catch(() => {}); if (MS.device) showUnlock('Signed out.'); else MS.showSignIn('Signed out.'); };
MS.removePhone = () => sheet('Remove this phone', '<p>The app forgets this phone. Next time you sign in with your password. Notifications stop.</p>', 'Remove', async () => {
  if (MS.device) await post(`/v1/mobile/devices/${encodeURIComponent(MS.device.id)}/revoke`, { note: 'removed in the app' }).catch(() => {});
  await forgetDevice(); MS.locked = true; await post('/v1/auth/logout').catch(() => {}); MS.showSignIn('This phone was removed.');
}, true);
// My phones: the person's registered phones, each revocable.
MS.phonesScreen = () => ({
  title: 'My phones', tab: 'more', back: 'more',
  load: () => get('/v1/mobile/devices'),
  html: d => `<div class="ph-pg"><h1>My phones</h1><p class="dim">Phones that open ${esc(MS.title)} with a fingerprint or face. Remove one you no longer use, or have lost.</p>
    <div class="ph-list">${d.devices.filter(x => x.status === 'Active').map(x => `<div class="ph-card info"><span class="ph-k">${esc(x.platform === 'ios' ? 'iPhone' : 'Android')}${x.id === MS.device?.id ? ' · this phone' : ''}<span>${x.push ? 'notifications on' : 'no notifications'}</span></span><b>${esc(x.name)}</b><span class="ph-sub">Last used ${fmtDate(x.last_seen)} · password again by ${fmtDate(x.reauth_by)}</span>${x.id === MS.device?.id ? '' : `<button class="btn sm" data-act="revokePhone" data-id="${esc(x.id)}">Remove</button>`}</div>`).join('') || empty('No phone set up yet.')}</div>
    ${MS.device ? '<button class="btn bad m-wide" data-act="removePhone">Remove this phone</button>' : ''}</div>`,
});

/* ---------- events ---------- */
const ACT = {
  lockNow: () => MS.lock('Locked.'),
  enrol, noEnrol: () => ready(), unlock, usePassword: () => MS.showSignIn(''),
  removePhone: () => MS.removePhone(),
  revokePhone: id => sheet('Remove that phone', '<p>It will need the password to sign in again.</p>', 'Remove', async () => { await post(`/v1/mobile/devices/${encodeURIComponent(id)}/revoke`, { note: 'removed from another phone' }); toast('Removed'); refresh(); }, true),
  signOut: () => MS.signOut(),
  reload: () => location.reload(),
};
document.addEventListener('click', e => {
  const g = e.target.closest('[data-go]');
  if (g) { e.preventDefault(); history.replaceState(null, '', '#' + g.dataset.go); go(g.dataset.go); return; }
  const a = e.target.closest('[data-act]');
  if (a && ACT[a.dataset.act]) { e.preventDefault(); ACT[a.dataset.act](a.dataset.id, a); }
});
['pointerdown', 'keydown'].forEach(ev => document.addEventListener(ev, () => { MS.lastTouch = Date.now(); }, { passive: true }));
// The firm's idle time-out: lock without waiting for the server to end
// the session.
setInterval(() => { if (!MS.locked && MS.cfg && Date.now() - MS.lastTouch > MS.cfg.idle_minutes * 60000) MS.lock('Locked after ' + MS.cfg.idle_minutes + ' minutes without use.'); }, 15000);

async function nativeHooks() {
  const A = plug('App'), P = plug('PushNotifications');
  A?.addListener('appStateChange', s => {
    if (!s.isActive) { MS.pausedAt = Date.now(); return; }
    if (!MS.locked && MS.pausedAt && Date.now() - MS.pausedAt > (MS.cfg?.lock_seconds ?? 60) * 1000) MS.lock('Locked while the app was in the background.');
    else if (!MS.locked) refresh();
  });
  // A tapped push opens its screen once unlocked; the push itself said
  // nothing about any client.
  P?.addListener('pushNotificationActionPerformed', a => { const r = a?.notification?.data?.route; if (r && MS.screens[r]) { if (MS.locked) MS.pending = r; else go(r); } });
  P?.addListener('registration', async t => { if (MS.device && !MS.locked && t?.value) await api('PUT', `/v1/mobile/devices/${encodeURIComponent(MS.device.id)}/push`, { token: t.value }).catch(() => {}); });
  A?.addListener('appUrlOpen', u => {
    try {
      const x = new URL(u.url);
      // A staff app set-up code for another server or firm: only after the
      // person confirms (the shell checks the host again).
      if (x.hostname === 'setup' && MS.app === 'staff') {
        const srv = x.searchParams.get('server') || '', firm = x.searchParams.get('firm') || '';
        sheet('Switch server', `<p>Open <b>${esc(srv)}</b>, firm <b>${esc(firm)}</b>, instead of this one? You sign in again there.</p>`, 'Switch', () => plug('LedgerlineShell').setServer({ url: srv, firm }));
        return;
      }
      const r = x.searchParams.get('screen'); if (r && MS.screens[r]) { if (MS.locked) MS.pending = r; else go(r); }
    } catch (e) {}
  });
}

// MS.start: the app's first moment. title is the app's name until the
// firm's configuration says otherwise.
MS.start = async function () {
  shellOnly('<p class="m-load">Loading…</p>');
  try { MS.cfg = await get(`/v1/mobile/config?app=${MS.app}&firm=${encodeURIComponent(MS.firm)}`); }
  catch (e) { shellOnly(`<div class="m-gate"><div class="m-gcard"><h1>Cannot reach your firm</h1>${errBox(e)}<button class="btn pri m-wide" data-act="reload">Try again</button></div></div>`); return; }
  MS.applyConfig?.(MS.cfg);
  if (!MS.cfg.on) { shellOnly(gate(`<p class="m-msg">${MS.app === 'client' ? 'The app is not available yet. Use the website for now.' : 'The staff app is switched off for this firm. An Admin switches it on in Settings → Staff app.'}</p>`)); return; }
  if (MS.native) {
    MS.rooted = !!(await Shell.integrity()).rooted;
    // The firm may refuse rooted or jailbroken phones (Settings → Client
    // app / Staff app; the server refuses to register them too).
    if (MS.rooted && MS.cfg.rooted_policy === 'refuse') { shellOnly(gate('<p class="m-msg">The firm does not allow the app on a phone that looks rooted or jailbroken. Use a phone that is not, or the website.</p>')); return; }
    MS.device = await Shell.pref('device').catch(() => null);
    nativeHooks();
  }
  try { await signedIn(false); }
  catch (e) {
    if (e.status !== 401) { shellOnly(gate(errBox(e))); return; }
    if (MS.device) showUnlock('', true); else MS.showSignIn('');
  }
};
