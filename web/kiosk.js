'use strict';
/* The office attendance kiosk (/kiosk/<firm>): a tablet HR registered once.
   Pairing: HR's one-time code, entered here from the office network, gives
   the tablet its token (kept in this browser only). Then each person enters
   their employee code and kiosk PIN and taps In or Out; the kiosk takes a
   photo for the manager to review (no face matching) and shows only the
   name and the time. Five wrong PINs lock the PIN (internal/ops/hrtime.go). */
(() => {
  const T = decodeURIComponent(location.pathname.split('/')[2] || '');
  const KEY = 'll-kiosk-' + T;
  const $ = id => document.getElementById(id);
  const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  let token = '';
  try { token = localStorage.getItem(KEY) || ''; } catch (e) {}
  let stream = null, timer = null;

  const call = async (path, body) => {
    const h = { 'Content-Type': 'application/json' };
    if (token) h['X-Kiosk-Token'] = token;
    const r = await fetch(`/kiosk-api/${encodeURIComponent(T)}${path}`, { method: 'POST', headers: h, body: JSON.stringify(body) });
    let d = null; try { d = await r.json(); } catch (e) {}
    if (!r.ok) { const err = new Error(d?.error?.message || 'Not accepted'); err.status = r.status; throw err; }
    return d;
  };
  const tick = () => { $('clock').textContent = new Date().toLocaleString('en-IN', { weekday: 'short', day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit', second: '2-digit' }); };
  setInterval(tick, 1000); tick();

  function pairView(msg) {
    $('card').innerHTML = `<h1>Register this kiosk</h1><p class="dim small">HR registers the kiosk on Attendance and gives you a pairing code. Enter it here, from the office network.</p>
      <div class="fld"><label for="pc">Pairing code</label><input class="in" id="pc" inputmode="numeric" autocomplete="off" maxlength="12"></div>
      ${msg ? `<div class="err">${esc(msg)}</div>` : ''}<button class="btn out-btn" id="pair">Register</button>`;
    $('pair').onclick = async () => {
      try { const r = await call('/pair', { code: $('pc').value.trim() }); token = r.token; try { localStorage.setItem(KEY, token); } catch (e) {} punchView(); }
      catch (e) { pairView(e.message); }
    };
  }

  async function camera() {
    if (stream || !navigator.mediaDevices?.getUserMedia) return;
    try { stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'user', width: 480, height: 360 }, audio: false }); $('cam').srcObject = stream; $('cam').hidden = false; } catch (e) { stream = null; }
  }
  function photo() {
    const v = $('cam'); if (!stream || !v.videoWidth) return '';
    const c = $('snap'); c.width = 320; c.height = Math.round(320 * v.videoHeight / v.videoWidth);
    c.getContext('2d').drawImage(v, 0, 0, c.width, c.height);
    return c.toDataURL('image/jpeg', 0.7).split(',')[1] || '';
  }

  function punchView(msg, ok) {
    clearTimeout(timer);
    $('card').innerHTML = `${ok ? `<div class="ok">${esc(ok)}</div>` : ''}${msg ? `<div class="err">${esc(msg)}</div>` : ''}
      <div class="fld"><label for="ec">Employee code</label><input class="in" id="ec" autocomplete="off" autocapitalize="characters" maxlength="20"></div>
      <div class="fld"><label for="pin">Kiosk PIN</label><input class="in" id="pin" type="password" inputmode="numeric" autocomplete="off" maxlength="6"></div>
      <div class="keys">${[1, 2, 3, 4, 5, 6, 7, 8, 9, '⌫', 0, 'C'].map(k => `<button type="button" data-k="${k}">${k}</button>`).join('')}</div>
      <div class="row"><button class="btn in-btn" data-kind="in">In</button><button class="btn out-btn" data-kind="out">Out</button></div>`;
    let field = 'ec';
    $('ec').onfocus = () => { field = 'ec'; }; $('pin').onfocus = () => { field = 'pin'; };
    $('card').querySelectorAll('[data-k]').forEach(b => b.onclick = () => { const f = $(field), k = b.dataset.k; if (k === '⌫') f.value = f.value.slice(0, -1); else if (k === 'C') f.value = ''; else f.value += k; });
    $('card').querySelectorAll('[data-kind]').forEach(b => b.onclick = async () => {
      const code = $('ec').value.trim().toUpperCase(), pin = $('pin').value.trim();
      if (!code || !pin) { punchView('Enter your employee code and PIN.'); return; }
      $('card').querySelectorAll('button').forEach(x => { x.disabled = true; });
      try {
        const r = await call('/punch', { employee: code, pin, kind: b.dataset.kind, photo: photo() || undefined });
        const at = r.at ? new Date(r.at).toLocaleTimeString('en-IN', { hour: '2-digit', minute: '2-digit' }) : '';
        punchView('', `${r.name} · ${b.dataset.kind === 'in' ? 'in' : 'out'} at ${at}`);
        timer = setTimeout(() => punchView(), 6000);
      } catch (e) {
        if (e.status === 403 && /kiosk/i.test(e.message) && /registered|revoked|token/i.test(e.message)) { token = ''; try { localStorage.removeItem(KEY); } catch (x) {} pairView(e.message); return; }
        punchView(e.message);
      }
    });
    $('ec').focus();
  }

  camera();
  if (token) punchView(); else pairView();
})();
