'use strict';
/* Ledgerline console: the account search box, used on every field where
   one of the firm's accounts (or a client's ledger) is picked: voucher
   lines, the party, a vendor's usual expense account, a bill's expense
   account, the bank a bill is paid from. People never type a code.

   As you type, a dropdown beside the field lists matching accounts from
   the server (GET /v1/accounts/search, which keeps an index per firm so it
   stays quick with thousands of clients): names that start with the text
   first, then names with a word that starts with it, then the rest.
   Aliases and client codes match too, and the matched letters are
   highlighted. Each line shows the name, the level-2 head, the sub-head
   and the current balance. Arrows and Enter choose, Esc closes, the mouse
   works too. The last line makes a new account: name, head, alias; it
   waits for a checker (POST /v1/accounts/new). Loaded after app.js. */

const AP = { opts: {}, picked: {}, cache: new Map(), heads: null, seq: 0, cur: null, hits: [], hl: 0, timer: 0, form: null };

// acctPick returns the box's HTML. code is the current account; o.label
// its name; o.only limits ledger prefixes ('BANK,CASH'); o.clients is ''
// (both), 'no' or 'only'; o.free lets the text stand without an account
// (the party); o.create (default true for makers) offers a new account;
// o.onPick(hit) and o.afterEnter(hit) are called when one is chosen.
function acctPick(id, code, o = {}) {
  AP.opts[id] = o;
  AP.picked[id] = code ? { code, name: o.label || code } : null;
  return `<div class="ap" data-ap="${id}"><input class="in ap-q ${o.cls || ''}" id="${id}-q" data-ap="${id}" value="${esc(o.label || '')}" placeholder="${esc(o.placeholder || 'Type a name, alias or client code')}" autocomplete="off" spellcheck="false" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="ap-dd"${o.aria ? ` aria-label="${esc(o.aria)}"` : ''}><input type="hidden" id="${id}" value="${esc(code || '')}">${o.noInfo ? '' : `<div class="ap-info" id="${id}-info">${o.info || ''}</div>`}</div>`;
}
const apDC = a => { const n = num(a); return n === 0 ? '0.00' : n > 0 ? nf(n, 2) + ' Dr' : nf(-n, 2) + ' Cr'; };
// apHL highlights len letters from at (letters, not UTF-16 units).
function apHL(text, at, len) {
  const cs = Array.from(String(text ?? ''));
  if (at < 0 || !len) return esc(text);
  return esc(cs.slice(0, at).join('')) + '<mark>' + esc(cs.slice(at, at + len).join('')) + '</mark>' + esc(cs.slice(at + len).join(''));
}
// apRank is the server's ranking, for heads matched in the page.
function apRank(s, q) {
  s = String(s).toLowerCase(); const i = s.indexOf(q);
  if (i < 0) return [-1, 0]; if (i === 0) return [0, 0];
  for (let j = i; j >= 0; j = s.indexOf(q, j + 1)) if (!/[\p{L}\p{N}]/u.test(s[j - 1])) return [1, Array.from(s.slice(0, j)).length];
  return [2, Array.from(s.slice(0, i)).length];
}
const apCanCreate = o => o.create !== false && has('Ops maker', 'Ops checker');
function apDD() {
  let d = $('ap-dd');
  if (!d) { d = document.createElement('div'); d.id = 'ap-dd'; d.className = 'ap-dd'; d.hidden = true; d.setAttribute('role', 'listbox'); document.body.appendChild(d); }
  return d;
}
function apPlace() {
  const id = AP.cur, q = id && $(id + '-q'), d = $('ap-dd'); if (!q || !d || d.hidden) return;
  const r = q.getBoundingClientRect(), vw = innerWidth, vh = innerHeight;
  let w = Math.max(r.width, 520), left = r.left;
  if (vw < 640) { left = 8; w = vw - 16; } else { w = Math.min(w, vw - 16); if (left + w > vw - 8) left = Math.max(8, vw - 8 - w); }
  d.style.left = left + 'px'; d.style.width = w + 'px';
  const below = vh - r.bottom - 8, above = r.top - 8;
  if (below < 260 && above > below) { d.style.top = ''; d.style.bottom = (vh - r.top + 4) + 'px'; d.style.maxHeight = Math.min(420, above) + 'px'; }
  else { d.style.bottom = ''; d.style.top = (r.bottom + 4) + 'px'; d.style.maxHeight = Math.min(420, below) + 'px'; }
}
function apClose(revert = true) {
  const id = AP.cur, d = $('ap-dd'); if (d) { d.hidden = true; d.innerHTML = ''; }
  AP.form = null;
  if (!id) return; AP.cur = null;
  const q = $(id + '-q'); if (!q) return;
  q.setAttribute('aria-expanded', 'false');
  const o = AP.opts[id] || {};
  if (revert && !o.free) q.value = AP.picked[id]?.name || '';
}
function apSet(id, hit) {
  AP.picked[id] = hit;
  const h = $(id), q = $(id + '-q');
  if (h) h.value = hit ? hit.code : '';
  if (q) q.value = hit ? hit.name : '';
  const inf = $(id + '-info');
  if (inf) inf.innerHTML = hit ? `${esc(hit.head || '')}${hit.sub ? ' · ' + esc(hit.sub) : ''}${hit.code && hit.balance !== undefined ? ` · <span class="mono">${apDC(hit.balance)}</span>` : ''}${hit.status === 'Pending' ? ' ' + pill('New: waits for a checker', 'warn') : ''}` : '';
  (AP.opts[id] || {}).onPick?.(hit);
}
function apOpen(id) {
  AP.cur = id; AP.form = null;
  const q = $(id + '-q'), d = apDD(); d.hidden = false; q.setAttribute('aria-expanded', 'true');
  apPlace(); apQuery(id);
}
function apQuery(id) {
  clearTimeout(AP.timer);
  AP.timer = setTimeout(() => apFetch(id), 70);
}
async function apFetch(id) {
  if (AP.cur !== id) return;
  const o = AP.opts[id] || {}, qel = $(id + '-q'); if (!qel) return;
  let q = qel.value.trim(); if (AP.picked[id] && q === AP.picked[id].name) q = '';
  const url = `/v1/accounts/search?q=${encodeURIComponent(q)}&date=${S.date}&limit=${o.limit || 12}${o.only ? '&only=' + o.only : ''}${o.clients ? '&clients=' + o.clients : ''}`;
  const c = AP.cache.get(url), my = ++AP.seq;
  let r;
  if (c && Date.now() - c.at < 20000) r = c.r;
  else { try { r = await get(url); } catch (e) { if (my === AP.seq) apRenderErr(e); return; } AP.cache.set(url, { at: Date.now(), r }); }
  if (my !== AP.seq || AP.cur !== id || AP.form) return;
  if (o.exclude) r = { ...r, hits: r.hits.filter(h => !o.exclude.includes(h.code)) };
  AP.hits = r.hits; AP.hl = 0; AP.q = q; AP.more = r.more; AP.total = r.total;
  apRender(id);
}
function apRenderErr(e) { const d = $('ap-dd'); if (d) d.innerHTML = `<div class="ap-empty">${esc(e.message)}</div>`; }
function apRender(id) {
  const d = $('ap-dd'), o = AP.opts[id] || {}; if (!d) return;
  const q = AP.q, rows = AP.hits.map((h, i) => {
    const nm = h.match === 'name' ? apHL(h.name, h.at, h.len) : esc(h.name);
    const al = h.alias ? ` <span class="ap-al">(${h.match === 'alias' ? apHL(h.alias, h.at, h.len) : esc(h.alias)})</span>` : '';
    const uc = h.client ? ` <span class="ap-ucc">${h.match === 'ucc' ? apHL(h.ucc, h.at, h.len) : esc(h.ucc)}</span>` : '';
    return `<button type="button" class="ap-opt ${i === AP.hl ? 'hl' : ''}" data-i="${i}" role="option" aria-selected="${i === AP.hl}"><span class="ap-n">${nm}${al}${uc}${h.status === 'Pending' ? ' ' + pill('New', 'warn') : ''}</span><span class="ap-h">${esc(h.head)}${h.sub ? `<span class="ap-sub"> · ${esc(h.sub)}</span>` : ''}</span><span class="ap-b">${apDC(h.balance)}</span></button>`;
  });
  const n = AP.hits.length, cr = apCanCreate(o);
  d.innerHTML = (n ? rows.join('') : `<div class="ap-empty">No account matches “${esc(q)}”.</div>`) +
    (AP.more ? `<div class="ap-more">${n} of ${AP.total} shown · type more letters to narrow</div>` : '') +
    (cr ? `<button type="button" class="ap-opt ap-new ${n === AP.hl ? 'hl' : ''}" data-i="${n}" role="option"><span class="ap-n">+ Create new account${q ? ` “${esc(q)}”` : ''}</span><span class="ap-h">waits for a checker</span><span class="ap-b"></span></button>` : '');
  apPlace();
}
function apMove(dir) {
  const o = AP.opts[AP.cur] || {}, max = AP.hits.length + (apCanCreate(o) ? 1 : 0); if (!max) return;
  AP.hl = (AP.hl + dir + max) % max;
  const d = $('ap-dd'); d.querySelectorAll('.ap-opt').forEach(b => { const on = +b.dataset.i === AP.hl; b.classList.toggle('hl', on); b.setAttribute('aria-selected', on); if (on) b.scrollIntoView({ block: 'nearest' }); });
}
function apChoose(i, viaEnter) {
  const id = AP.cur; if (!id) return;
  if (i === AP.hits.length) { apForm(id); return; }
  const h = AP.hits[i]; if (!h) return;
  apSet(id, h); apClose(false);
  if (viaEnter) (AP.opts[id] || {}).afterEnter?.(h);
}

/* ---- the new-account form inside the dropdown ---- */
async function apHeads() {
  if (!AP.heads) { try { AP.heads = (await get(`/v1/account-heads?date=${S.date}`)).heads; } catch (e) { AP.heads = []; } }
  return AP.heads;
}
async function apForm(id) {
  const o = AP.opts[id] || {}, d = $('ap-dd'), name = $(id + '-q').value.trim();
  const only = o.only ? o.only.split(',') : null;
  const heads = (await apHeads()).filter(h => h.prefix && (!only || only.includes(h.prefix)));
  AP.form = { id, heads, head: null, hl: 0, list: [] };
  d.innerHTML = `<form class="ap-form" id="apn-f"><div class="ap-ft">New account</div>
    <div class="fld"><label for="apn-name">Name</label><input class="in" id="apn-name" value="${esc(name)}" autocomplete="off"></div>
    <div class="fld"><label for="apn-head">Head</label><input class="in" id="apn-head" placeholder="Type to search heads" autocomplete="off" role="combobox" aria-expanded="true" aria-controls="apn-heads"><div class="ap-hlist" id="apn-heads" role="listbox"></div></div>
    <div class="fld"><label for="apn-alias">Alias <span class="dim">(optional, short)</span></label><input class="in" id="apn-alias" maxlength="24" autocomplete="off"></div>
    <p class="dim small">The account waits for a checker. A voucher that uses it posts when the checker approves the voucher, which approves the account too.</p>
    <div id="apn-err"></div><div class="split"><button type="button" class="btn sm" id="apn-back">Back to the list</button><button class="btn sm pri" type="submit">Create account</button></div></form>`;
  apPlace();
  const hq = $('apn-head');
  const renderHeads = () => {
    const q = hq.value.trim().toLowerCase(), f = AP.form;
    f.list = f.heads.map(h => { const [rk, at] = q ? apRank(h.name, q) : [0, 0]; const [rp] = q && rk < 0 ? apRank(h.path, q) : [rk]; return { h, rk: rk >= 0 ? rk : rp >= 0 ? 3 : -1, at: rk >= 0 ? at : -1 }; })
      .filter(x => x.rk >= 0).sort((a, b) => a.rk - b.rk || a.h.name.localeCompare(b.h.name)).slice(0, 8);
    f.hl = Math.min(f.hl, Math.max(0, f.list.length - 1));
    $('apn-heads').innerHTML = f.list.length ? f.list.map((x, i) => `<button type="button" class="ap-opt ${i === f.hl ? 'hl' : ''}" data-hi="${i}" role="option"><span class="ap-n">${x.at >= 0 ? apHL(x.h.name, x.at, q.length) : esc(x.h.name)}</span><span class="ap-h">${esc(x.h.level1)}${x.h.level > 2 ? ' › ' + esc(x.h.path.split(' › ').slice(0, -1).join(' › ')) : ''}</span><span class="ap-b"></span></button>`).join('') : '<div class="ap-empty">No head matches.</div>';
  };
  const pickHead = i => { const x = AP.form.list[i]; if (!x) return; AP.form.head = x.h; hq.value = x.h.path; $('apn-heads').innerHTML = ''; $('apn-alias').focus(); };
  hq.oninput = () => { AP.form.head = null; AP.form.hl = 0; renderHeads(); };
  hq.onfocus = () => { if (!AP.form.head) renderHeads(); };
  hq.onkeydown = e => {
    const f = AP.form;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); if (f.list.length) { f.hl = (f.hl + (e.key === 'ArrowDown' ? 1 : -1) + f.list.length) % f.list.length; renderHeads(); } }
    else if (e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); if (!f.head) pickHead(f.hl); else $('apn-alias').focus(); }
  };
  $('apn-heads').onmousedown = e => { const b = e.target.closest('[data-hi]'); if (b) { e.preventDefault(); pickHead(+b.dataset.hi); } };
  $('apn-name').onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); hq.focus(); } };
  $('apn-alias').onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); e.stopPropagation(); $('apn-f').requestSubmit(); } };
  $('apn-back').onclick = () => { AP.form = null; $(id + '-q').focus(); apOpen(id); };
  $('apn-f').onsubmit = async e => {
    e.preventDefault(); e.stopPropagation();
    if (!AP.form.head) { $('apn-err').innerHTML = '<div class="note warn">Choose the head the account goes under.</div>'; hq.focus(); return; }
    try {
      const a = await post('/v1/accounts/new', { name: $('apn-name').value.trim(), head: AP.form.head.id, alias: $('apn-alias').value.trim() });
      AP.cache.clear();
      const hit = { code: a.code, name: a.name, alias: a.alias, head: a.head_name, sub: a.sub_head, balance: '0', status: 'Pending' };
      apClose(false); apSet(id, hit); toast(`${a.name} made: it waits for a checker`);
      const q = $(id + '-q'), o2 = AP.opts[id] || {};
      if (o2.afterEnter) o2.afterEnter(hit); else if (q) { AP.skip = id; q.focus(); }
    } catch (err) { $('apn-err').innerHTML = errHTML(err); }
  };
  $('apn-name').focus(); $('apn-name').select();
}

/* ---- events ---- */
document.addEventListener('focusin', e => {
  const el = e.target;
  if (el.classList?.contains('ap-q') && AP.skip === el.dataset.ap) { AP.skip = null; return; }
  if (el.classList?.contains('ap-q')) { if (AP.cur && AP.cur !== el.dataset.ap) apClose(); el.select(); apOpen(el.dataset.ap); return; }
  if (AP.cur && !e.target.closest?.('#ap-dd') && !e.target.closest?.(`[data-ap="${AP.cur}"]`)) apClose();
});
document.addEventListener('input', e => {
  const el = e.target; if (!el.classList?.contains('ap-q')) return;
  const id = el.dataset.ap, o = AP.opts[id] || {};
  if (o.free) { AP.picked[id] = null; const h = $(id); if (h) h.value = ''; const inf = $(id + '-info'); if (inf) inf.innerHTML = ''; }
  if (AP.cur !== id) apOpen(id); else { AP.form = null; apQuery(id); }
});
// Capture, so a picked account is taken before a screen's own Enter and
// Esc keys (the voucher entry's) see the key.
document.addEventListener('keydown', e => {
  const el = e.target;
  if (!el.classList?.contains('ap-q') || AP.cur !== el.dataset.ap || AP.form) return;
  const d = $('ap-dd'); if (!d || d.hidden) return;
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); e.stopImmediatePropagation(); apMove(e.key === 'ArrowDown' ? 1 : -1); return; }
  if (e.key === 'Enter') {
    const id = AP.cur, o = AP.opts[id] || {}, q = el.value.trim(), p = AP.picked[id];
    e.preventDefault(); e.stopImmediatePropagation();
    if (p && q === p.name) { apClose(false); o.afterEnter?.(p); return; } // unchanged: go on
    if (!q && !p && o.onEmptyEnter && o.canEnd?.()) { apClose(); o.onEmptyEnter(); return; } // an empty line ends the lines
    if (o.free && (!AP.hits.length || AP.hl >= AP.hits.length)) { apClose(false); o.afterEnter?.(null); return; } // free text stands
    apChoose(AP.hl, true); return;
  }
  if (e.key === 'Escape') { e.preventDefault(); e.stopImmediatePropagation(); apClose(); return; }
  if (e.key === 'Tab') apClose();
}, true);
// Esc inside the new-account form goes back to the list.
document.addEventListener('keydown', e => {
  if (e.key !== 'Escape' || !AP.form || !e.target.closest?.('#ap-dd')) return;
  e.preventDefault(); e.stopImmediatePropagation();
  const id = AP.form.id; AP.form = null; $(id + '-q')?.focus(); apOpen(id);
}, true);
document.addEventListener('mousedown', e => {
  const d = $('ap-dd'); if (!d || d.hidden) return;
  const b = e.target.closest?.('#ap-dd .ap-opt[data-i]');
  if (b) { e.preventDefault(); apChoose(+b.dataset.i, false); return; }
  if (!e.target.closest?.('#ap-dd') && !e.target.closest?.('.ap')) apClose();
});
addEventListener('resize', apPlace);
document.addEventListener('scroll', e => { if (!e.target.closest?.('#ap-dd')) apPlace(); }, true);
