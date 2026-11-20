'use strict';
/* Ledgerline console: Encryption keys (firmkey.go), a panel in Settings
   beside Sign-in security. Whose key the firm's data is under: Ledgerline's,
   or the firm's own (AWS KMS in the firm's AWS account, or the firm's key
   service), with the key masked, the last successful use and how long the
   unwrapped key stays in memory. The Admin tests a key, moves the firm onto
   it, rotates it and sets the cache window. Compliance sees it read-only.
   Loaded after app.js; it adds to the Settings screen's load. */

{
  const load = V.settings.load;
  V.settings.load = async () => {
    const d = await load();
    d.fk = has('Compliance officer') ? await get('/v1/security/keys').catch(e => ({ error: e.message })) : null;
    return d;
  };
}

const fkWhen = t => t ? esc(fmtTS(t)) : '—';

function firmKeyPanel(k) {
  if (!k) return '';
  const head = '<div class="panel"><div class="hd"><h3>Encryption keys</h3><small>Whose key your firm’s data is under</small></div><div class="bd">';
  if (k.error) return head + `<div class="note warn">${esc(k.error)}</div></div></div>`;
  if (!k.encrypted) return head + `<div class="note bad">${esc(k.master)}</div></div></div>`;
  const admin = has('Admin');
  const conns = tbl([['Firm’s own key'], ['Key'], ['State'], ['Last checked'], ['']], k.connections.map(c => `<tr><td>${esc(c.name.replace(/^Firm's own encryption key: /, ''))}</td><td class="mono small">${esc(c.key || '—')}</td>
    <td>${pill(c.status, { Working: 'good', Problem: 'bad', Saved: 'acc' }[c.status] || 'nt')}${c.problem ? `<div class="dim small wrap">${esc(c.problem)}</div>` : ''}</td><td class="n dim">${fkWhen(c.tested_at)}</td>
    <td>${admin ? `<div class="split"><button class="btn sm" data-act="go" data-id="connections">${c.status === 'Not set' ? 'Enter in Connections' : 'Change'}</button>${c.status !== 'Not set' ? `<button class="btn sm" data-act="fkTest" data-id="${esc(c.id)}">Test</button>` : ''}${k.mode === 'ledgerline' && c.status === 'Working' ? `<button class="btn sm pri" data-act="fkUse" data-id="${esc(c.id)}">Use this key</button>` : ''}</div>` : ''}</td></tr>`));
  if (k.mode !== 'firm') {
    return head + `<div class="kv"><div><small>Your firm’s data is under</small><b>Ledgerline-managed keys</b></div><div><small>Key source</small><b>${esc(k.master)}</b></div></div>
      <p class="small" style="margin:10px 0">Your firm can hold the key itself instead: a KMS key in the firm’s own AWS account (Mumbai or Hyderabad), or a key service the firm runs. Ledgerline then reads the firm’s data only while the firm’s key lets it. Enter the key in Connections, test it, then press <b>Use this key</b>. Ask Ledgy “broker-held keys” for how to set up the key and its policy.</p>
      ${conns}</div></div>`;
  }
  const ok = k.available || (!k.problem && !k.outage_since);
  const state = k.outage_since ? pill('Not available', 'bad') : k.available ? pill('In use', 'good') : pill(k.problem ? 'Not available' : 'Unwrapped on next use', k.problem ? 'bad' : 'nt');
  return head + `${!ok || k.outage_since ? `<div class="note bad"><b>Your firm’s encryption key is not available (revoked or unreachable)${k.outage_since ? ' since ' + fkWhen(k.outage_since) : ''}.</b> Ledgerline shows and saves nothing of the firm’s until it is. ${esc(k.problem || '')}</div>` : ''}
    <div class="kv"><div><small>Your firm’s data is under</small><b>The firm’s own key ${state}</b></div><div><small>Held in</small><b>${esc((k.provider || k.conn).replace(/^Firm's own encryption key: /, ''))}${k.region ? ' · ' + esc(k.region) : ''}</b></div><div><small>Key</small><b class="mono">${esc(k.key || '—')}</b></div></div>
    <div class="kv" style="margin-top:8px"><div><small>Key version in use</small><b>${k.current}${k.roots.length > 1 ? ` (also ${k.roots.filter(r => !r.current).map(r => r.version).join(', ')} for older data)` : ''}</b></div><div><small>Last successful use</small><b>${fkWhen(k.last_ok)}</b></div><div><small>Kept in memory until</small><b>${fkWhen(k.cached_until)}</b></div></div>
    <div class="kv" style="margin-top:8px"><div><small>Moved to the firm’s key</small><b>${fkWhen(k.since)}${k.set_by ? ' by ' + esc(k.set_by) : ''}</b></div><div><small>Last rotated</small><b>${k.rotated_at ? fkWhen(k.rotated_at) + ' by ' + esc(k.rotated_by) : 'Never'}</b></div>
      <div><small>Cache window (minutes)</small>${admin ? `<div class="split"><input class="in sm" id="fk-cmin" type="number" min="1" max="60" value="${k.cache_minutes}" style="width:80px"><button class="btn sm" data-act="fkCache">Save</button></div>` : `<b>${k.cache_minutes}</b>`}</div></div>
    <p class="small" style="margin:10px 0">Ledgerline keeps the firm’s unwrapped key in memory for at most ${k.cache_minutes} minutes, then asks the firm’s key again. So if the firm disables its key or revokes Ledgerline’s access, Ledgerline stops within ${k.cache_minutes} minutes: every screen then says the key is not available, nothing falls back to Ledgerline’s own key, and the Admin gets a work item. When the key answers again, service resumes by itself.</p>
    ${tbl([['Key version'], ['Wrapped by'], ['Wrapped at'], ['']], k.roots.map(r => `<tr><td class="n">${r.version}</td><td class="mono small">${esc(r.key)}</td><td class="n dim">${fkWhen(r.wrapped_at)}</td><td>${r.current ? pill('New writes', 'acc') : pill('Older data', 'nt')}</td></tr>`))}
    ${admin ? `<div class="tools" style="margin-top:10px"><button class="btn sm" data-act="fkTest" data-id="${esc(k.conn)}">Test the key now</button><button class="btn sm" data-act="fkRotate">Rotate</button><span class="dim small">Records saved before the move stay under Ledgerline’s key until next saved. Older data stays readable after a rotation.</span></div>` : ''}
    <h4 style="margin:14px 0 6px">Connections to the firm’s key</h4>${conns}</div></div>`;
}

Object.assign(A, {
  fkTest: id => act(() => post('/v1/security/keys/test', { conn: id }), r => {
    const c = (r.connections || []).find(x => x.id === id) || {};
    return c.status === 'Working' ? 'The key works: a test key was wrapped and unwrapped' : 'The key did not work: ' + (c.problem || c.status);
  }),
  fkUse: id => confirmModal('Use the firm’s own key', 'From now on everything Ledgerline saves for the firm is encrypted under a key that only the firm’s key can unlock.',
    `<p class="small">Ledgerline makes a new key for the firm, has the firm’s key wrap it and unwrap it again as a check, and keeps only the wrapped copy. If the firm later disables its key or revokes Ledgerline’s access, Ledgerline stops serving the firm’s data within the cache window, until the key is available again.</p>
     <p class="small"><b>Keep the firm’s key safe.</b> If it is deleted, the firm’s data saved from now on cannot be read by anyone, Ledgerline included.</p>
     <div class="fld"><label for="fk-c">Keep the unwrapped key in memory for (minutes, 1 to 60)</label><input class="in" id="fk-c" type="number" min="1" max="60" value="15"></div>`,
    'Use the firm’s key', () => act(() => post('/v1/security/keys/use', { conn: id, cache_minutes: parseInt($('fk-c').value, 10) || 15 }), 'Your firm’s data is now under its own key')),
  fkRotate: () => confirmModal('Rotate the firm’s key', 'A new key version for everything saved from now on.',
    '<p class="small">The older key versions are re-wrapped under the key the firm’s connection names now, so older data stays readable. To move to a new KMS key, enter the new key’s ARN in Connections first and keep the old key enabled until this has finished; after that the old key can be disabled.</p>',
    'Rotate', () => act(() => post('/v1/security/keys/rotate', {}), r => `Key version ${r.current} in use; ${Math.max(0, (r.roots || []).length - 1)} older versions re-wrapped`)),
  fkCache: () => act(() => api('PUT', '/v1/security/keys/cache', { cache_minutes: parseInt($('fk-cmin').value, 10) }), r => `The firm’s key stays in memory for at most ${r.cache_minutes} minutes`),
});
