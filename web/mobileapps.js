'use strict';
/* Ledgerline console: Admin → Settings → Staff app and Client app. "Staff app" (screen staffapp):
   who may use the staff app, the set-up QR code, push notifications and
   the firm's registered staff phones. "Client app" (screen clientapp): on
   or off, the app's name, colours and logo, and the firm's details the app
   shows (SEBI registration, grievance contacts, investor charter). Loaded
   after app.js. The apps themselves are /m/ and /app/ (mshell.js). */

const MA_ROLES_ORDER = ['Ops maker', 'Ops checker', 'Risk manager', 'Compliance officer', 'Branch staff', 'Authorised person', 'HR manager', 'Payroll', 'Admin'];
const maWhen = t => t ? esc(fmtTS(t)) : '—';
async function maLoad() {
  const [s, qr, conn] = await Promise.all([get('/v1/mobile/settings'), has() ? get('/v1/mobile/staff-qr').catch(() => null) : null, get('/v1/connections').catch(() => null)]);
  s.qr = qr; s.fcm = (conn?.connections || []).find(c => c.id === 'fcm');
  return s;
}

V.staffapp = {
  load: async () => { const s = await maLoad(); s.devices = has() ? (await get('/v1/mobile/devices?all=staff').catch(() => ({ devices: [] }))).devices : []; C.staffapp = s; return s; },
  html: s => {
    const ed = s.can_edit, dis = ed ? '' : 'disabled', staff = (S.me?.users || []).filter(u => u.role !== 'Client' && u.active);
    const active = s.devices.filter(d => d.status === 'Active');
    return ph('Staff app', 'The firm’s staff approve prepared actions, follow the day, see alerts and look up clients on their phones. The app signs in with the password and two-factor code once, then unlocks with the phone’s fingerprint or face. Pushes never name a client.') +
      `<div class="kpis">${kpi('Staff app', s.staff_on ? 'On' : 'Off', s.staff_on ? 'for the people chosen below' : 'nobody can sign in to it', s.staff_on ? 'good' : '')}${kpi('Phones', nf(active.length), 'registered and working')}${kpi('Push notifications', s.fcm?.status === 'Saved' || s.fcm?.status === 'Working' ? 'Connected' : 'Not connected', 'Settings → Connections', s.fcm?.status === 'Saved' || s.fcm?.status === 'Working' ? 'good' : 'warn')}${kpi('Locks after', esc(s.idle_minutes) + ' min', 'without use')}</div>
      <div class="grid2"><div class="panel"><div class="hd"><h3>Who may use it</h3></div><div class="bd"><div class="form">
        <div class="fld full"><label class="check"><input type="checkbox" id="ma-on" ${s.staff_on ? 'checked' : ''} ${dis}> The staff app is on</label></div>
        <div class="fld full"><label>Everyone with the role</label><div class="tools">${MA_ROLES_ORDER.map(r => `<label class="check"><input type="checkbox" class="ma-role" value="${esc(r)}" ${s.staff_roles.includes(r) ? 'checked' : ''} ${dis}> ${esc(r)}</label>`).join('')}</div></div>
        ${s.branches.length ? `<div class="fld full"><label>Everyone of the branch (branch staff)</label><div class="tools">${s.branches.map(b => `<label class="check"><input type="checkbox" class="ma-br" value="${esc(b)}" ${s.staff_branches.includes(b) ? 'checked' : ''} ${dis}> ${esc(b)}</label>`).join('')}</div></div>` : ''}
        <div class="fld full"><label>And these people</label><div class="tools">${staff.map(u => `<label class="check"><input type="checkbox" class="ma-user" value="${esc(u.id)}" ${s.staff_users.includes(u.id) ? 'checked' : ''} ${dis}> ${esc(u.name)} <span class="dim small">${esc(u.role)}</span></label>`).join('')}</div></div>
        <div class="fld"><label for="ma-idle">Lock after (minutes without use)</label><input class="in sm" id="ma-idle" type="number" min="1" max="30" value="${s.idle_minutes}" ${dis}></div>
        <div class="fld"><label for="ma-lock">Ask for the fingerprint again after (seconds in the background)</label><input class="in sm" id="ma-lock" type="number" min="0" max="600" value="${s.lock_seconds}" ${dis}></div>
        <div class="fld"><label for="ma-re">Ask for the password again every (days)</label><input class="in sm" id="ma-re" type="number" min="1" max="90" value="${s.reauth_days}" ${dis}></div></div>
        <p class="dim small">These times apply to the client app too. Admin is not let in by default: tick the role or the person.</p>
        ${ed ? '<button class="btn pri" data-act="maSaveStaff">Save</button>' : '<p class="dim small">Only an Admin changes these.</p>'}</div></div>
      <div class="panel"><div class="hd"><h3>Set up a phone</h3></div><div class="bd">${s.qr ? `<div class="split" style="align-items:flex-start;gap:16px"><img src="${esc(s.qr.qr)}" alt="QR code that sets up the staff app for ${esc(s.qr.firm)}" style="width:168px;height:auto;border:1px solid var(--line);border-radius:8px;background:#fff">
          <ol class="small" style="margin:0;padding-left:18px;line-height:1.6"><li>Install the staff app from the App Store or Google Play.</li><li>Scan this code with the phone’s camera. The app opens with this server (<span class="mono">${esc(s.qr.server)}</span>) and firm code <b class="mono">${esc(s.qr.firm)}</b>.</li><li>Sign in with your user ID, password and two-factor code.</li><li>Choose “Use Face ID” or “Use fingerprint”, and allow notifications.</li></ol></div>
          ${s.qr.https ? '' : '<div class="note warn" style="margin-top:10px">This server is not on https. Phones refuse to connect to it: set LEDGERLINE_PUBLIC_URL to the https address.</div>'}<p class="dim small">The code holds no password. Or type the server address and firm code in the app. In a phone’s browser the app is at <span class="mono">${esc(s.qr.web)}</span>.</p>` : '<p class="dim">An Admin sees the set-up code here.</p>'}
        <div class="tools" style="margin-top:8px"><button class="btn" data-act="maTestPush">Send a test push to my phone</button><button class="btn" data-act="go" data-id="connections">Push notifications connection</button></div></div></div></div>
      ${has() ? `<div class="panel"><div class="hd"><h3>Staff phones</h3><small>revoke a lost phone, or one of someone who left</small></div>${tbl([['Person'], ['Phone'], ['Notifications'], ['Signed in with password'], ['Last used'], ['State'], ['']], s.devices.map(d => `<tr><td>${esc(d.user_name || d.user)}<div class="dim small mono">${esc(d.user)}</div></td><td>${esc(d.name)}<div class="dim small">${d.platform === 'ios' ? 'iPhone' : 'Android'}${d.app_version ? ' · app ' + esc(d.app_version) : ''}${d.rooted ? ' · ' + pill('Rooted or jailbroken', 'bad') : ''}</div></td><td>${d.push ? pill('On', 'good') : pill('Off', 'nt')}</td><td class="small">${maWhen(d.signed_in_at)}<div class="dim">again by ${fmtDate(d.reauth_by)}</div></td><td class="small">${maWhen(d.last_seen)}</td><td>${d.status === 'Active' ? pill('Working', 'good') : pill('Revoked', 'nt') + `<div class="dim small">${esc(d.revoked_by || '')} · ${esc(d.revoke_note || '')}</div>`}</td><td>${d.status === 'Active' ? `<button class="btn sm bad" data-act="maRevoke" data-id="${esc(d.id)}">Revoke</button>` : ''}</td></tr>`), { empty: 'No phone registered yet.' })}</div>` : ''}`;
  }
};

V.clientapp = {
  load: async () => { const s = await maLoad(); C.clientapp = s; return s; },
  html: s => {
    const b = s.client || {}, ed = s.can_edit, dis = ed ? '' : 'disabled';
    const fld = (id, label, val, extra = '', type = 'text') => `<div class="fld"><label for="ca-${id}">${label}</label><input class="in" id="ca-${id}" type="${type}" value="${esc(val || '')}" ${dis} ${extra}></div>`;
    return ph('Client app', 'Your clients’ app on iPhone and Android, under your own name in the App Store and Google Play. It shows the client portal on the phone: balance, holdings, contract notes and statements, adding and withdrawing money, IPOs, mutual funds, pledges, sale authorisations, profile changes and the help desk. Pushes say only that there is something to see.') +
      `<div class="grid2"><div class="panel"><div class="hd"><h3>The app</h3></div><div class="bd"><div class="form">
        <div class="fld full"><label class="check"><input type="checkbox" id="ca-on" ${b.on ? 'checked' : ''} ${dis}> The client app is on (clients with a portal login can sign in)</label></div>
        ${fld('name', 'App name (as under the icon, at most 30 letters)', b.app_name, 'maxlength="30"')}
        <div class="fld"><label for="ca-pri">Main colour</label><input class="in sm" id="ca-pri" type="color" value="${esc(b.primary || '#1f4fd1')}" ${dis}></div>
        <div class="fld"><label for="ca-acc">Second colour</label><input class="in sm" id="ca-acc" type="color" value="${esc(b.accent || '#0f9d76')}" ${dis}></div>
        <div class="fld"><label for="ca-logo">Logo (PNG or JPEG, square, at most 150 KB)</label><input class="in" id="ca-logo" type="file" accept="image/png,image/jpeg" ${dis}>${b.logo ? `<div class="tools" style="margin-top:6px"><img src="${esc(b.logo)}" alt="Current logo" style="width:40px;height:40px;border-radius:8px;border:1px solid var(--line)">${ed ? '<button class="linkish small" data-act="caLogoOff">Remove</button>' : ''}</div>` : ''}</div></div></div></div>
      <div class="panel"><div class="hd"><h3>Shown under About us</h3><small>what an Indian broker’s app must show</small></div><div class="bd"><div class="form">
        <div class="fld full"><div class="note small">The SEBI registration as stock broker and the exchange member codes come from Settings. SEBI SCORES and the SMART ODR portal are always linked.</div></div>
        ${fld('dp', 'SEBI registration as depository participant', b.dp_reg)}${fld('office', 'Registered office address', b.registered_office)}
        ${fld('semail', 'Support email', b.support_email, '', 'email')}${fld('sphone', 'Support phone', b.support_phone, '', 'tel')}
        ${fld('gname', 'Grievance officer (name)', b.grievance_officer)}${fld('gemail', 'Grievance email', b.grievance_email, '', 'email')}${fld('gphone', 'Grievance phone', b.grievance_phone, '', 'tel')}${fld('co', 'Compliance officer (name, email, phone)', b.compliance_officer)}
        ${fld('charter', 'Investor charter (https:// link)', b.investor_charter_url, 'placeholder="https://"', 'url')}${fld('risk', 'Risk disclosures (https:// link)', b.risk_disclosure_url, 'placeholder="https://"', 'url')}${fld('web', 'Website', b.website_url, 'placeholder="https://"', 'url')}</div>
        ${ed ? '<button class="btn pri" data-act="maSaveClient">Save</button>' : '<p class="dim small">Only an Admin changes these.</p>'}</div></div></div>
      <div class="panel"><div class="hd"><h3>Publishing the app</h3></div><div class="bd small"><p style="margin-top:0">The app is published from your own Apple Developer and Google Play accounts. Your developer (or Ledgerline support) builds it on a Mac from <span class="mono">mobile/client</span>: <span class="mono">brand.json</span> takes the app name, bundle ID, icon, colours and this server’s address <span class="mono">${esc(location.origin)}/app/?firm=${esc(S.me?.tenant || '')}</span>. The README in that folder has every step, and what the stores ask about privacy: the app collects the client’s identifiers and financial information to run their account, shares nothing with third parties for advertising, and sends pushes through Firebase without client data.</p>
        <p class="dim">A client who lost a phone: the client removes it under More → My phones, or an Admin revokes it here.</p>
        <div class="tools"><input class="in sm mono" id="ca-who" placeholder="Client login (UCC in small letters)" autocapitalize="none"><button class="btn sm" data-act="caPhones">Show that client’s phones</button></div><div id="ca-ph"></div></div></div>`;
  }
};

function maCollect() {
  const s = C.staffapp || C.clientapp, m = { staff_on: s.staff_on, staff_users: s.staff_users, staff_roles: s.staff_roles, staff_branches: s.staff_branches, idle_minutes: s.idle_minutes, lock_seconds: s.lock_seconds, reauth_days: s.reauth_days, client: { ...(s.client || {}) } };
  return m;
}
const maVals = cls => [...document.querySelectorAll('.' + cls)].filter(i => i.checked).map(i => i.value);
Object.assign(A, {
  maSaveStaff: () => { const m = maCollect(); Object.assign(m, { staff_on: $('ma-on').checked, staff_roles: maVals('ma-role'), staff_users: maVals('ma-user'), staff_branches: maVals('ma-br'), idle_minutes: parseInt($('ma-idle').value, 10) || 5, lock_seconds: parseInt($('ma-lock').value, 10) || 0, reauth_days: parseInt($('ma-re').value, 10) || 30 });
    act(() => api('PUT', '/v1/mobile/settings', m), 'Staff app settings saved').catch(() => {}); },
  maSaveClient: async () => {
    const m = maCollect(), v = id => $('ca-' + id).value.trim(), f = $('ca-logo').files[0];
    Object.assign(m.client, { on: $('ca-on').checked, app_name: v('name'), primary: v('pri'), accent: v('acc'), dp_reg: v('dp'), registered_office: v('office'), support_email: v('semail'), support_phone: v('sphone'), grievance_officer: v('gname'), grievance_email: v('gemail'), grievance_phone: v('gphone'), compliance_officer: v('co'), investor_charter_url: v('charter'), risk_disclosure_url: v('risk'), website_url: v('web') });
    if (f) { if (f.size > 150 * 1024) { toast('The logo is larger than 150 KB', true); return; } m.client.logo = await new Promise(r => { const fr = new FileReader(); fr.onload = () => r(fr.result); fr.readAsDataURL(f); }); }
    act(() => api('PUT', '/v1/mobile/settings', m), 'Client app settings saved').catch(() => {});
  },
  caLogoOff: () => { const m = maCollect(); m.client.logo = ''; act(() => api('PUT', '/v1/mobile/settings', m), 'Logo removed').catch(() => {}); },
  maTestPush: () => act(() => post('/v1/mobile/test-push'), r => r.sent ? 'Test push sent to your phone' : 'Not sent: see Settings → Connections', false).catch(() => {}),
  maRevoke: id => confirmModal('Revoke this phone', 'It stops working at once: its sessions end and it needs the password to sign in again. Notifications to it stop.', '<div class="fld"><label for="ma-why">Why (lost, left the firm…)</label><input class="in" id="ma-why"></div>', 'Revoke', () => act(() => post(`/v1/mobile/devices/${encodeURIComponent(id)}/revoke`, { note: $('ma-why').value }), 'Phone revoked'), true),
  caPhones: async () => {
    const who = $('ca-who').value.trim().toLowerCase(); if (!who) return;
    try { const r = await get('/v1/mobile/devices?all=' + encodeURIComponent(who)); $('ca-ph').innerHTML = tbl([['Phone'], ['Last used'], ['State'], ['']], r.devices.map(d => `<tr><td>${esc(d.name)} <span class="dim small">${esc(d.app)}</span></td><td class="small">${maWhen(d.last_seen)}</td><td>${pill(d.status === 'Active' ? 'Working' : 'Revoked', d.status === 'Active' ? 'good' : 'nt')}</td><td>${d.status === 'Active' ? `<button class="btn sm bad" data-act="maRevoke" data-id="${esc(d.id)}">Revoke</button>` : ''}</td></tr>`), { empty: 'No phone for that login.' }); }
    catch (e) { toast(e.message, true); }
  },
});
