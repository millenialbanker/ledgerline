'use strict';
/* Ledgerline console: client limits to BOW (BOLTPlusOnWeb) by its API.
   A panel on Live RMS (RMS & margin): each client's Securities and
   Commodities limit now, what was last pushed to BOW and how it went, the
   day's OTP sign-in, Push to BOW now, and a client's BOW user by hand.
   Loaded after app.js; it wraps the Live RMS screen so app.js is unchanged. */

Object.assign(UI, { bowQ: '', bowF: 'All' });
const BOW_ST = { Pushed: 'good', Unchanged: 'good', Removed: 'nt', Failed: 'bad', 'Not in BOW': 'warn' };
const bowRisk = () => has('Risk manager');
const bowStamp = t => t ? `${fmtDate(t.slice(0, 10))} ${t.slice(11, 16)}` : '—';

{
  const load = V.live.load, html = V.live.html;
  V.live.load = async () => {
    const d = await load();
    try { d.bow = await get(`/v1/bow?date=${S.date}`); }
    catch (e) { d.bow = e.status === 409 || e.status === 404 || e.status === 403 ? null : { error: e.message }; }
    return d;
  };
  V.live.html = d => html(d) + bowPanel(d.bow);
}

function bowPanel(b) {
  if (!b) return '';
  if (b.error) return `<div class="panel"><div class="hd"><h3>BOW limits</h3></div><div class="bd"><div class="note warn">${esc(b.error)}</div></div></div>`;
  const s = b.session || {}, last = (b.runs || [])[0];
  const sign = s.signed_in ? pill('Signed in ' + bowStamp(s.at), 'good') : s.otp ? pill('Not signed in today', 'warn') : pill('Signs in by itself (no OTP)', 'nt');
  const diff = b.rows.filter(r => r.differs).length, failed = b.rows.filter(r => r.last.status === 'Failed').length;
  return `<div class="panel"><div class="hd"><h3>BOW limits</h3><small>BOLTPlusOnWeb ${esc(b.env)} · Securities × ${esc(b.sec_mult)}, Commodities × ${esc(b.com_mult)}</small>
      <div class="tools">${bowRisk() ? `${s.otp && !s.signed_in ? '<button class="btn sm pri" data-act="bowSignIn">Sign in to BOW</button>' : ''}<button class="btn sm" data-act="bowMaster">Refresh BOW client list</button><button class="btn sm pri" data-act="bowPush" data-id="">Push to BOW now</button>` : ''}</div></div>
    <div class="bd"><div class="note">${pill('Connection: ' + b.status, b.status === 'Working' ? 'good' : b.status === 'Problem' ? 'bad' : 'nt')}${b.problem ? ` <span class="small ${b.status === 'Problem' ? 'neg' : 'dim'}">${esc(b.problem)}</span>` : ''} · ${sign} ·
      ${b.auto ? pill(`Automatic: start of day ${esc(b.bod_at)}${b.bod_done ? ' ✓' : ''}, then changes every ${b.every} min to 23:30`, 'acc') : pill('Automatic pushes off (Connections)', 'warn')}
      · BOW client list ${b.master_date ? fmtDate(b.master_date) + ' (' + b.master_size + ' users)' : 'not fetched yet'}
      ${last ? ` · last push ${bowStamp(last.at)} ${esc(last.kind)}: ${last.pushed} pushed, ${last.unchanged} unchanged, ${last.failed} failed${last.stopped ? ` <span class="neg">stopped: ${esc(last.stopped)}</span>` : ''}` : ''}</div>
      ${b.note ? `<div class="note warn small">${esc(b.note)}</div>` : ''}
      <div class="tools">${segBtns('bowF', [['All', 'All'], ['Differs', `Not yet in BOW · ${diff}`], ['Failed', `Failed · ${failed}`]], 'bow-t')}<input class="in" data-ui="bowQ" data-part="bow-t" placeholder="Filter name or UCC" value="${esc(UI.bowQ)}"></div></div>
    <div id="bow-t">${P['bow-t']({ bow: b })}</div></div>`;
}

P['bow-t'] = d => {
  const b = d.bow; if (!b || !b.rows) return '';
  const q = UI.bowQ.trim().toLowerCase();
  const rows = b.rows.filter(r => (UI.bowF === 'All' || (UI.bowF === 'Differs' ? r.differs : r.last.status === 'Failed')) && (!q || (r.ucc + ' ' + r.name).toLowerCase().includes(q)));
  return tbl([['Client'], ['BOW user'], ['Securities now', 'r'], ['Commodities now', 'r'], ['Last pushed', 'r'], ['Status'], ['']], rows.map(r => `<tr>
    <td><button class="linkish mono" data-act="client" data-id="${r.ucc}">${r.ucc}</button> ${esc(r.name)}${r.limit.why ? `<div class="dim small">${esc(r.limit.why)}</div>` : ''}</td>
    <td class="mono small">${r.user ? `${esc(r.user.login_id)} <span class="dim">#${esc(r.user.usid)}</span>${r.by_hand ? ' ' + pill('by hand', 'acc') : ''}` : '<span class="dim">—</span>'}</td>
    <td class="r">${r.limit.close ? '<span class="dim">closed</span>' : inr(r.limit.securities)}</td><td class="r">${r.limit.close ? '' : inr(r.limit.commodities)}</td>
    <td class="r small">${r.last.status ? `${inr(r.last.securities)} / ${inr(r.last.commodities)}<div class="dim">${bowStamp(r.last.at)}</div>` : '<span class="dim">never</span>'}</td>
    <td>${r.last.status ? pill(r.last.status, BOW_ST[r.last.status] || 'nt') : pill('Not pushed', 'nt')}${r.queued ? ' ' + pill('change queued', 'acc') : r.differs && r.last.status ? ' ' + pill('changed', 'warn') : ''}${r.last.error ? `<div class="small neg wrap">${esc(r.last.error)}</div>` : ''}</td>
    <td>${bowRisk() ? `<button class="btn sm" data-act="bowPush" data-id="${esc(r.ucc)}">Push</button><button class="btn sm" data-act="bowUser" data-id="${esc(r.ucc)}">BOW user</button>` : ''}</td></tr>`), { tall: true, empty: 'No active clients.' });
};

Object.assign(A, {
  bowPush: id => act(() => post('/v1/bow/push', { date: S.date, uccs: id ? [id] : [] }), v => v.stopped ? 'Stopped: ' + v.stopped : `${v.pushed} pushed, ${v.unchanged} unchanged, ${v.failed} failed${(v.not_mapped || []).length ? ', ' + v.not_mapped.length + ' not in BOW' : ''}`),
  bowMaster: () => act(() => post('/v1/bow/master', {}), v => `BOW client list: ${v.users} users, ${v.matched} matched to clients`),
  bowSignIn: async () => {
    let r;
    try { r = await post('/v1/bow/sign-in', {}); } catch (e) { toast(e.message, true); return; }
    if (r.signed_in) { toast('Signed in to BOW'); render({ keep: true }); return; }
    confirmModal('Sign in to BOW', esc(r.message || 'BOW sent an OTP to the admin login\'s registered mobile and e-mail.'),
      '<div class="fld"><label for="bow-otp">OTP</label><input class="in mono" id="bow-otp" inputmode="numeric" autocomplete="one-time-code"></div>', 'Sign in',
      () => act(() => post('/v1/bow/sign-in', { otp: $('bow-otp').value.trim() }), 'Signed in to BOW'));
  },
  bowUser: id => {
    const r = (C.live?.bow?.rows || []).find(x => x.ucc === id) || {}, u = r.user || {};
    confirmModal('BOW user · ' + esc(id), 'Normally found in BOW\'s client list by the UCC. Set it here when the BOW login is different; leave the ID empty to go back to the client list.',
      `<div class="grid g2"><div class="fld"><label for="bow-usid">BOW internal user ID (USID)</label><input class="in mono" id="bow-usid" value="${r.by_hand ? esc(u.usid || '') : ''}"></div>
       <div class="fld"><label for="bow-login">BOW login ID</label><input class="in mono" id="bow-login" placeholder="${esc(id)}-T9065" value="${r.by_hand ? esc(u.login_id || '') : ''}"></div></div>`, 'Save',
      () => act(() => post(`/v1/bow/users/${encodeURIComponent(id)}`, { usid: $('bow-usid').value.trim(), login_id: $('bow-login').value.trim() }), 'BOW user saved'));
  },
});
