'use strict';
/* Ledgerline console: team messages. Head office channels and direct
   messages, each branch's own, and the private threads between official
   desks (Risk, DP, Compliance…) and each branch or AP. Every message
   notifies everyone in the conversation; there is no mute. Loaded after
   app.js and uses its helpers. */

const CH = { home: null, open: '', view: null, pend: [], since: '', unread: 0, mentions: 0, imgs: {}, drafts: {}, ac: null, sending: false, uploading: 0, base: document.title, asked: false };
const chatOn = () => !!S.me && !clientUser();
const encU = encodeURIComponent;

/* ---------- formatting ---------- */
const chTime = s => { const t = new Date(s); return isNaN(t) ? '' : t.toLocaleTimeString('en-GB', { timeZone: 'Asia/Kolkata', hour: '2-digit', minute: '2-digit', hour12: false }); };
const chDay = s => { const t = new Date(s); if (isNaN(t)) return ''; const f = d => d.toLocaleDateString('en-GB', { timeZone: 'Asia/Kolkata', day: 'numeric', month: 'short', year: 'numeric' }); const today = f(new Date()), yest = f(new Date(Date.now() - 864e5)); const d = f(t); return d === today ? 'Today' : d === yest ? 'Yesterday' : t.toLocaleDateString('en-GB', { timeZone: 'Asia/Kolkata', weekday: 'short', day: 'numeric', month: 'short', year: 'numeric' }); };
const chWhen = s => { const t = new Date(s); if (isNaN(t)) return ''; return chDay(s) === 'Today' ? chTime(s) : chDay(s) === 'Yesterday' ? 'Yesterday' : t.toLocaleDateString('en-GB', { timeZone: 'Asia/Kolkata', day: 'numeric', month: 'short' }); };
const chSize = n => n >= 1 << 20 ? (n / (1 << 20)).toFixed(1) + ' MB' : Math.max(1, Math.round(n / 1024)) + ' KB';
const chIcon = c => c.kind === 'channel' ? '<span class="ch-ic hash">#</span>' : c.kind === 'thread' ? '<span class="ch-ic desk" aria-hidden="true">◆</span>' : c.kind === 'ledgy' ? '<span class="ch-ic ledgy">L</span>' : `<span class="ch-ic av">${esc(initials(c.title))}</span>`;
function chText(t, names) {
  let h = esc(t || '');
  h = h.replace(/\bhttps?:\/\/[^\s<]+[^\s<.,;:!?)\]'"]/g, u => `<a href="${u}" target="_blank" rel="noopener noreferrer">${u}</a>`);
  return h.replace(/(^|[^A-Za-z0-9._\-/])@([a-z0-9][a-z0-9._-]*[a-z0-9]|[a-z0-9])/g, (all, pre, id) => {
    if (id === 'all') return `${pre}<span class="mention all">@all</span>`;
    return names?.[id] ? `${pre}<span class="mention${id === S.me?.actor ? ' me' : ''}" title="@${esc(id)}">@${esc(names[id])}</span>` : all;
  });
}

/* ---------- the screen ---------- */
V.chat = {
  load: async () => {
    CH.home = await get('/v1/chat');
    const ids = CH.home.conversations.map(c => c.id);
    if (CH.open && !ids.includes(CH.open) && !CH.view) CH.open = '';
    if (!CH.open && innerWidth > 900 && ids.length) CH.open = ids[0];
    CH.view = null;
    if (CH.open) { try { CH.view = await get('/v1/chat/c/' + encU(CH.open)); } catch (e) { CH.open = ''; } }
    chCounts(CH.home.unread, CH.home.mentions);
    return CH.home;
  },
  html: h => {
    const acts = (h.people.length ? '<button class="btn" data-act="chNewDM">New message</button>' : '') +
      (h.desks.length ? `<button class="btn" data-act="chDesk">${h.parties?.length ? 'Write as a desk' : 'Write to a desk'}</button>` : '') +
      (h.manage ? '<button class="btn" data-act="chManage">Channels & desks</button>' : '');
    const perm = ('Notification' in window) && Notification.permission !== 'granted'
      ? `<div class="note ${Notification.permission === 'denied' ? 'warn' : ''} ch-perm">${Notification.permission === 'denied'
        ? '<b>Desktop notifications are blocked in this browser.</b> New messages still appear here and in the corner of every screen. To get them outside Ledgerline, allow notifications for this site in the browser’s settings.'
        : '<b>Turn on desktop notifications</b> so new messages reach you even when Ledgerline is in the background. <button class="btn sm pri" data-act="chPerm">Turn on</button>'}</div>` : '';
    return ph('Team messages', who(h), acts) + perm +
      `<div class="chat ${CH.open ? 'has-open' : ''}"><div class="ch-list">${chList(h)}</div><section class="ch-pane" id="chPane" aria-live="polite">${chPane()}</section></div>`;
  }
};
function who(h) {
  const s = h.me.scope;
  if (s === 'ho') return 'Head office channels and direct messages, and the desks you answer for. Everyone in a conversation is told about each new message.';
  if (s.startsWith('branch:')) return `Your branch’s channels and direct messages, and your branch’s private threads with head office desks. Other branches and APs cannot see them.`;
  return 'Your private threads with head office desks. Other APs and branches cannot see them.';
}
function chList(h) {
  if (!h.conversations.length) return `<div class="empty ch-empty">No conversations yet.${h.desks.length ? ' Start one with a desk above.' : ''}${h.people.length ? ' Or send someone a message.' : ''}</div>`;
  return h.conversations.map(c => `<button class="ch-item ${c.id === CH.open ? 'on' : ''} ${c.unread ? 'unread' : ''}" data-act="chOpen" data-id="${esc(c.id)}">${chIcon(c)}
    <span class="ch-mid"><span class="ch-t"><b>${esc(c.title)}</b>${c.archived ? ' <span class="pill nt">Archived</span>' : ''}</span><span class="ch-last">${c.last_text ? `${c.last_from && c.kind !== 'dm' && c.kind !== 'ledgy' ? esc(c.last_from.split(' ')[0]) + ': ' : ''}${esc(c.last_text)}` : esc(c.sub || '')}</span></span>
    <span class="ch-meta"><small>${chWhen(c.last_at)}</small>${c.mentions ? '<span class="ch-at" title="Mentions you">@</span>' : ''}${c.unread ? `<span class="ch-n">${c.unread > 99 ? '99+' : c.unread}</span>` : ''}</span></button>`).join('');
}
function chPane() {
  const v = CH.view;
  if (!v) return '<div class="ch-blank"><p>Choose a conversation.</p></div>';
  const c = v.channel;
  let last = '', prev = null;
  const msgs = v.messages.map(m => {
    let out = '';
    const day = chDay(m.at);
    if (day !== last) { out += `<div class="ch-day"><span>${esc(day)}</span></div>`; last = day; prev = null; }
    const cont = prev && prev.from === m.from && (new Date(m.at) - new Date(prev.at)) < 5 * 60e3;
    prev = m;
    const mine = m.from === S.me.actor, ledgy = m.from === 'system:ledgy';
    const meMention = !mine && !ledgy && (m.all || (m.mentions || []).includes(S.me.actor));
    const files = (m.files || []).map(f => f.image
      ? `<button class="ch-img" data-act="chImg" data-id="${esc(f.id)}" title="${esc(f.name)}"><img data-chimg="${esc(f.id)}" alt="${esc(f.name)}"></button>`
      : `<button class="ch-file" data-act="chFile" data-id="${esc(f.id)}" data-name="${esc(f.name)}"><span class="ch-fi">${esc((f.name.split('.').pop() || 'file').toUpperCase().slice(0, 4))}</span><span><b>${esc(f.name)}</b><small>${chSize(f.size)}</small></span></button>`).join('');
    return out + `<div class="ch-msg ${cont ? 'cont' : ''} ${mine ? 'mine' : ''} ${meMention ? 'tome' : ''} ${ledgy ? 'ledgy' : ''}">
      ${cont ? '<span class="ch-gut"></span>' : `<span class="ch-av ${ledgy ? 'ledgy' : ''}">${ledgy ? 'L' : esc(initials(v.names[m.from] || m.from))}</span>`}
      <div class="ch-body">${cont ? '' : `<div class="ch-hd"><b>${esc(v.names[m.from] || m.from)}</b><small>${chTime(m.at)}</small></div>`}
        ${m.text ? `<div class="ch-tx">${chText(m.text, v.names)}</div>` : ''}${files ? `<div class="ch-files">${files}</div>` : ''}
        ${m.link ? `<button class="btn sm" data-act="go" data-id="${esc(m.link.view)}">${esc(m.link.label)}</button>` : ''}</div></div>`;
  }).join('');
  const people = v.people.length;
  const comp = v.can_post ? `<div class="ch-comp">
      <div class="ch-pend" id="chPend">${chPend()}</div>
      <div class="ch-ac" id="chAc" role="listbox" hidden></div>
      <div class="ch-row"><label class="btn ch-attach" title="Attach photos or files (up to 15 MB each)" aria-label="Attach files"><svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 11.5 12.5 20a5 5 0 0 1-7-7L14 4.5a3.5 3.5 0 0 1 5 5L10.5 18a2 2 0 0 1-3-3L15 7.5"/></svg><input type="file" id="chFiles" multiple accept="image/jpeg,image/png,image/gif,image/webp,.heic,.pdf,.xlsx,.xls,.docx,.doc,.pptx,.csv,.txt,.zip" hidden></label>
      <textarea id="chText" class="in" rows="1" maxlength="4000" placeholder="${esc(c.kind === 'dm' ? 'Message ' + c.title : 'Message ' + c.title)}${v.can_all ? ' · @ to mention, @all for everyone' : ' · @ to mention'}" aria-label="Message">${esc(CH.drafts[c.id] || '')}</textarea>
      <button class="btn pri" data-act="chSend" ${CH.sending || CH.uploading ? 'disabled' : ''}>Send</button></div>
      <small class="dim">Enter sends, Shift+Enter starts a new line. Paste a screenshot to attach it.</small></div>`
    : `<div class="ch-ro">${c.kind === 'ledgy' ? 'Ledgy writes here when the work queue has something new for you. Ask Ledgy a question with the <b>?</b> button.' : c.archived ? 'This channel is archived: it can be read but not written to.' : 'You can read this conversation but not write to it.'}</div>`;
  return `<div class="ch-head"><button class="btn sm ch-back" data-act="chBack" aria-label="Back to conversations">‹</button>${chIcon(c)}<div><h3>${esc(c.title)}</h3><small>${esc(c.sub || '')}${v.about ? ' · ' + esc(v.about) : ''}</small></div>
    <span class="ch-people" title="${esc(v.people.map(p => p.name).join(', '))}">${people} ${people === 1 ? 'person' : 'people'}</span></div>
    <div class="ch-msgs" id="chMsgs">${v.more ? '<div class="ch-more"><button class="btn sm" data-act="chMore">Earlier messages</button></div>' : ''}${msgs || '<div class="empty">No messages yet. Say hello.</div>'}</div>${comp}`;
}
function chPend() {
  return CH.pend.map((p, i) => `<span class="ch-chip ${p.err ? 'bad' : ''}">${p.preview ? `<img src="${p.preview}" alt="">` : ''}<span>${esc(p.name)}${p.ref ? '' : p.err ? ' · ' + esc(p.err) : ' · uploading…'}</span><button class="x" data-act="chUnpend" data-id="${i}" aria-label="Remove ${esc(p.name)}">×</button></span>`).join('') + (CH.uploading ? '' : '');
}

/* ---------- after drawing ---------- */
const _bindPage = bindPage;
bindPage = function () {
  _bindPage();
  if (S.view !== 'chat') return;
  chAfter(true);
};
// The panel fills the rest of the window, so the page itself does not scroll.
function chFit() {
  const c = document.querySelector('.chat'); if (!c) return;
  const top = c.getBoundingClientRect().top + window.scrollY;
  c.style.height = Math.max(420, innerHeight - top - 64) + 'px'; // clear of the Ask Ledgy button
}
addEventListener('resize', () => { if (S.view === 'chat') chFit(); });
function chAfter(scroll) {
  chFit();
  const box = $('chMsgs');
  if (box && scroll) box.scrollTop = box.scrollHeight;
  document.querySelectorAll('img[data-chimg]').forEach(chLoadImg);
  const ta = $('chText');
  if (ta) { chGrow(ta); if (innerWidth > 900 && !document.querySelector('.md')) ta.focus({ preventScroll: true }); }
  chMarkRead();
}
async function chLoadImg(img) {
  const id = img.dataset.chimg;
  if (CH.imgs[id]) { img.src = CH.imgs[id]; return; }
  try {
    const r = await fetch('/v1/chat/files/' + encU(id), { headers: { 'X-Requested-With': 'ledgerline' } });
    if (!r.ok) throw new Error();
    CH.imgs[id] = URL.createObjectURL(await r.blob());
    document.querySelectorAll(`img[data-chimg="${CSS.escape(id)}"]`).forEach(i => i.src = CH.imgs[id]);
  } catch (e) { img.alt = 'Photo could not be loaded'; }
}
const chGrow = ta => { ta.style.height = 'auto'; ta.style.height = Math.min(ta.scrollHeight, 180) + 'px'; };
async function chMarkRead() {
  const v = CH.view;
  if (!v || document.hidden || S.view !== 'chat' || !v.messages.length) return;
  const seq = v.messages[v.messages.length - 1].seq;
  const c = CH.home?.conversations.find(x => x.id === v.channel.id);
  if (c && !c.unread && !c.mentions) return;
  try { await post(`/v1/chat/c/${encU(v.channel.id)}/read`, { seq }); } catch (e) { return; }
  if (c) { CH.home.unread -= c.unread; CH.home.mentions -= c.mentions; c.unread = 0; c.mentions = 0; }
  const it = document.querySelector(`.ch-item[data-id="${CSS.escape(v.channel.id)}"]`);
  if (it) { it.classList.remove('unread'); it.querySelector('.ch-n')?.remove(); it.querySelector('.ch-at')?.remove(); }
  chTick(true);
}
// Redraw just the open conversation, keeping the draft and the caret.
function chRedrawPane(scroll) {
  const pane = $('chPane'); if (!pane) return;
  const ta = $('chText'), had = ta && document.activeElement === ta, sel = ta ? [ta.selectionStart, ta.selectionEnd] : null;
  const box = $('chMsgs'), atEnd = !box || box.scrollHeight - box.scrollTop - box.clientHeight < 80;
  pane.innerHTML = chPane();
  const n = $('chText'); if (n && had) { n.focus({ preventScroll: true }); try { n.setSelectionRange(sel[0], sel[1]); } catch (e) {} }
  chAfter(scroll || atEnd);
}
function chRedrawList() { const l = document.querySelector('.ch-list'); if (l && CH.home) l.innerHTML = chList(CH.home); }

/* ---------- notifications ---------- */
function chCounts(unread, mentions) {
  CH.unread = Math.max(0, unread || 0); CH.mentions = Math.max(0, mentions || 0);
  if (S.counts) { S.counts.chat = CH.unread; }
  document.title = (CH.unread ? `(${CH.unread > 99 ? '99+' : CH.unread}) ` : '') + CH.base;
  const nb = document.querySelector('.nav[data-id="chat"]');
  if (nb) { let ct = nb.querySelector('.ct'); if (CH.unread) { if (!ct) { ct = document.createElement('span'); ct.className = 'ct'; nb.appendChild(ct); } ct.textContent = CH.unread > 99 ? '99+' : CH.unread; } else ct?.remove(); }
}
const _refreshCounts = refreshCounts;
refreshCounts = async function () { await _refreshCounts(); if (chatOn()) chCounts(CH.unread, CH.mentions); };

let chBusy = false;
async function chTick(quiet) {
  if (!chatOn() || chBusy) return;
  chBusy = true;
  try {
    const p = await get('/v1/chat/pulse?since=' + encU(CH.since));
    const first = !CH.since;
    CH.since = p.now;
    chCounts(p.unread, p.mentions);
    if (first || quiet || !p.items.length) return;
    let openNew = false, listNew = false;
    for (const it of p.items) {
      const here = S.view === 'chat' && CH.open === it.channel && !document.hidden;
      if (here) openNew = true; else chNotify(it);
      listNew = true;
    }
    if (S.view === 'chat') {
      if (openNew && CH.open) { try { CH.view = await get('/v1/chat/c/' + encU(CH.open)); chRedrawPane(); } catch (e) {} }
      if (listNew) { try { CH.home = await get('/v1/chat'); chRedrawList(); chCounts(CH.home.unread, CH.home.mentions); } catch (e) {} }
    }
  } catch (e) {
  } finally { chBusy = false; }
}
function chNotify(it) {
  let box = $('chNotes');
  if (!box) { box = document.createElement('div'); box.id = 'chNotes'; box.className = 'ch-notes'; box.setAttribute('aria-live', 'assertive'); document.body.appendChild(box); }
  const n = document.createElement('button');
  n.className = 'ch-note' + (it.mention ? ' at' : '') + (it.ledgy ? ' ledgy' : '');
  n.dataset.act = 'chOpenNote'; n.dataset.id = it.channel;
  n.innerHTML = `<span class="ch-nt"><b>${esc(it.title)}</b>${it.ledgy ? '<span class="pill acc">New work</span>' : it.mention ? '<span class="pill warn">Mentions you</span>' : ''}</span><span class="ch-nb">${it.ledgy ? '' : `<b>${esc(it.from)}:</b> `}${esc(it.text)}</span>`;
  box.appendChild(n);
  while (box.children.length > 4) box.firstChild.remove();
  setTimeout(() => n.remove(), it.mention || it.ledgy ? 30000 : 10000);
  chChime(it.mention || it.ledgy);
  if ('Notification' in window && Notification.permission === 'granted' && (document.hidden || !document.hasFocus())) {
    try {
      const dn = new Notification((it.ledgy ? 'Ledgy: new work' : it.title) + (it.mention && !it.ledgy ? ' · mentions you' : ''), { body: (it.ledgy ? '' : it.from + ': ') + it.text, tag: it.channel, renotify: true });
      dn.onclick = () => { window.focus(); chGoTo(it.channel); dn.close(); };
    } catch (e) {}
  }
}
let chAudio = null;
function chChime(strong) {
  try {
    chAudio = chAudio || new (window.AudioContext || window.webkitAudioContext)();
    if (chAudio.state === 'suspended') chAudio.resume();
    const t = chAudio.currentTime;
    [[880, 0], [1320, .12]].slice(0, strong ? 2 : 1).forEach(([f, d]) => {
      const o = chAudio.createOscillator(), g = chAudio.createGain();
      o.frequency.value = f; o.type = 'sine';
      g.gain.setValueAtTime(0.0001, t + d); g.gain.exponentialRampToValueAtTime(0.12, t + d + .02); g.gain.exponentialRampToValueAtTime(0.0001, t + d + .3);
      o.connect(g).connect(chAudio.destination); o.start(t + d); o.stop(t + d + .32);
    });
  } catch (e) {}
}
function chGoTo(id) { CH.open = id; CH.view = null; if (S.view === 'chat') render({ keep: true }); else { S.view = 'chat'; render(); } }
setInterval(() => chTick(), 8000);
document.addEventListener('visibilitychange', () => { if (!document.hidden) { chTick(); if (S.view === 'chat') chMarkRead(); } });
// Browsers allow asking for notification permission, and sound, only after a click.
document.addEventListener('click', () => {
  if (CH.asked || !chatOn()) return;
  CH.asked = true;
  try { if ('Notification' in window && Notification.permission === 'default') Notification.requestPermission().then(() => { if (S.view === 'chat') render({ keep: true }); }); } catch (e) {}
  try { chAudio = chAudio || new (window.AudioContext || window.webkitAudioContext)(); } catch (e) {}
}, true);
(async function chStart() { for (let i = 0; i < 40 && !chatOn(); i++) await new Promise(r => setTimeout(r, 500)); chTick(); })();

/* ---------- writing ---------- */
async function chUpload(files) {
  const v = CH.view; if (!v?.can_post) return;
  for (const f of files) {
    if (CH.pend.length >= 6) { toast('Attach at most 6 files to a message'); break; }
    if (f.size > 15 << 20) { toast(`${f.name} is larger than 15 MB`); continue; }
    const name = f.name && f.name !== 'image.png' ? f.name : `screenshot-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')}.${(f.type.split('/')[1] || 'png').replace('jpeg', 'jpg')}`;
    const p = { name, preview: f.type.startsWith('image/') ? URL.createObjectURL(f) : '', ref: null, err: '' };
    CH.pend.push(p); CH.uploading++;
    const pd = $('chPend'); if (pd) pd.innerHTML = chPend();
    try { p.ref = await api('POST', `/v1/chat/c/${encU(v.channel.id)}/files?name=${encU(name)}`, f, true); }
    catch (e) { p.err = e.message; }
    CH.uploading--;
    const pd2 = $('chPend'); if (pd2) pd2.innerHTML = chPend();
    const sb = document.querySelector('[data-act="chSend"]'); if (sb) sb.disabled = CH.uploading > 0;
  }
}
async function chSend() {
  const v = CH.view, ta = $('chText'); if (!v || !ta || CH.sending || CH.uploading) return;
  const text = ta.value.trim(), files = CH.pend.filter(p => p.ref).map(p => p.ref.id);
  if (!text && !files.length) return;
  CH.sending = true;
  try {
    await post(`/v1/chat/c/${encU(v.channel.id)}/messages`, { text, files });
    CH.drafts[v.channel.id] = ''; CH.pend.forEach(p => p.preview && URL.revokeObjectURL(p.preview)); CH.pend = [];
    CH.view = await get('/v1/chat/c/' + encU(v.channel.id));
    CH.sending = false;
    chRedrawPane(true);
    try { CH.home = await get('/v1/chat'); chRedrawList(); } catch (e) {}
  } catch (e) { CH.sending = false; toast(e.message, true); }
}
// @ autocomplete
function chAcUpdate(ta) {
  const box = $('chAc'), v = CH.view; if (!box || !v) return;
  const m = /(^|[\s(])@([a-z0-9._-]*)$/i.exec(ta.value.slice(0, ta.selectionStart));
  if (!m) { box.hidden = true; CH.ac = null; return; }
  const q = m[2].toLowerCase();
  let opts = v.people.filter(p => p.id !== S.me.actor && (p.id.includes(q) || p.name.toLowerCase().includes(q))).slice(0, 8).map(p => ({ id: p.id, label: p.name, sub: p.side + (p.role ? ' · ' + p.role : '') }));
  if (v.can_all && 'all'.startsWith(q)) opts.unshift({ id: 'all', label: 'all', sub: `Everyone here (${v.people.length})` });
  if (!opts.length) { box.hidden = true; CH.ac = null; return; }
  CH.ac = { opts, i: 0, from: ta.selectionStart - m[2].length - 1 };
  box.innerHTML = opts.map((o, i) => `<button class="${i ? '' : 'on'}" role="option" data-act="chAcPick" data-id="${i}"><b>@${esc(o.id === 'all' ? 'all' : o.label)}</b><small>${esc(o.sub)}</small></button>`).join('');
  box.hidden = false;
}
function chAcPick(i) {
  const ta = $('chText'), a = CH.ac; if (!ta || !a) return;
  const o = a.opts[i]; const before = ta.value.slice(0, a.from), after = ta.value.slice(ta.selectionStart);
  ta.value = before + '@' + o.id + ' ' + after;
  const pos = before.length + o.id.length + 2; ta.focus(); ta.setSelectionRange(pos, pos);
  CH.drafts[CH.view.channel.id] = ta.value; $('chAc').hidden = true; CH.ac = null; chGrow(ta);
}
document.addEventListener('input', e => {
  if (e.target.id !== 'chText') return;
  CH.drafts[CH.view?.channel.id] = e.target.value; chGrow(e.target); chAcUpdate(e.target);
});
document.addEventListener('keydown', e => {
  if (e.target.id !== 'chText') return;
  const a = CH.ac, box = $('chAc');
  if (a && box && !box.hidden) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); a.i = (a.i + (e.key === 'ArrowDown' ? 1 : a.opts.length - 1)) % a.opts.length; box.querySelectorAll('button').forEach((b, j) => b.classList.toggle('on', j === a.i)); return; }
    if (e.key === 'Enter' || e.key === 'Tab') { e.preventDefault(); chAcPick(a.i); return; }
    if (e.key === 'Escape') { e.preventDefault(); box.hidden = true; CH.ac = null; return; }
  }
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); chSend(); }
});
document.addEventListener('change', e => { if (e.target.id === 'chFiles') { const fs = [...e.target.files]; e.target.value = ''; chUpload(fs); } });
document.addEventListener('paste', e => {
  if (e.target.id !== 'chText') return;
  const fs = [...(e.clipboardData?.files || [])];
  if (fs.length) { e.preventDefault(); chUpload(fs); }
});
document.addEventListener('dragover', e => { if (S.view === 'chat' && e.target.closest?.('.ch-pane') && CH.view?.can_post) { e.preventDefault(); } });
document.addEventListener('drop', e => { if (S.view === 'chat' && e.target.closest?.('.ch-pane') && CH.view?.can_post && e.dataTransfer?.files?.length) { e.preventDefault(); chUpload([...e.dataTransfer.files]); } });

/* ---------- actions ---------- */
Object.assign(A, {
  chOpen: id => { if (id === CH.open && CH.view) return; CH.open = id; CH.view = null; CH.pend = []; render({ keep: true }); if (innerWidth > 900) window.scrollTo(0, 0); },
  chOpenNote: (id, el) => { el.remove(); chGoTo(id); },
  chBack: () => { CH.open = ''; CH.view = null; render({ keep: true }); },
  chSend: () => chSend(),
  chUnpend: i => { const p = CH.pend.splice(+i, 1)[0]; if (p?.preview) URL.revokeObjectURL(p.preview); const pd = $('chPend'); if (pd) pd.innerHTML = chPend(); },
  chAcPick: i => chAcPick(+i),
  chImg: id => openBlob('/v1/chat/files/' + encU(id)),
  chFile: (id, el) => download('/v1/chat/files/' + encU(id), el.dataset.name),
  chPerm: async () => { try { await Notification.requestPermission(); } catch (e) {} render({ keep: true }); },
  chMore: async () => {
    const v = CH.view; if (!v?.messages.length) return;
    const box = $('chMsgs'), h0 = box.scrollHeight;
    try {
      const older = await get(`/v1/chat/c/${encU(v.channel.id)}?before=${v.messages[0].seq}`);
      v.messages = older.messages.concat(v.messages); v.more = older.more; Object.assign(v.names, older.names);
      chRedrawPane(false); const b = $('chMsgs'); b.scrollTop = b.scrollHeight - h0;
    } catch (e) { toast(e.message, true); }
  },
  chNewDM: () => {
    const ps = CH.home.people;
    modal(`<div class="hd"><div><h2>New message</h2><p>${CH.home.me.scope === 'ho' ? 'Anyone at head office.' : 'Anyone in your branch.'} To reach the other side, write to a desk.</p></div><button class="x" data-act="close" aria-label="Close">×</button></div>
      <div class="bd"><input class="in" id="chPeopleQ" placeholder="Search by name or role" autocomplete="off"><div class="ch-pick" id="chPeople">${chPeople(ps, '')}</div></div>`);
  },
  chDM: async id => { try { const c = await post('/v1/chat/dm', { user: id }); closeModal(); chGoTo(c.id); } catch (e) { toast(e.message, true); } },
  chDesk: () => {
    const h = CH.home, desk = h.parties?.length;
    if (!desk) {
      modal(`<div class="hd"><div><h2>Write to a head office desk</h2><p>Only your ${esc(h.me.side)} and the desk see the conversation.</p></div><button class="x" data-act="close" aria-label="Close">×</button></div>
        <div class="bd"><div class="ch-pick">${h.desks.map(d => `<button data-act="chThread" data-id="${esc(d.id)}"><b>${esc(d.name)}</b><small>${esc(d.about || '')}${d.staff?.length ? ' · ' + esc(d.staff.map(s => s.name).join(', ')) : ''}</small></button>`).join('')}</div></div>`);
      return;
    }
    const mine = h.desks.filter(d => d.on_desk);
    modal(`<div class="hd"><div><h2>Write as a desk</h2><p>Each branch and AP has its own private thread with each desk.</p></div><button class="x" data-act="close" aria-label="Close">×</button></div>
      <div class="bd"><div class="fld"><label for="chDeskSel">Desk</label><select class="in" id="chDeskSel">${mine.map(d => `<option value="${esc(d.id)}">${esc(d.name)}</option>`).join('')}</select></div>
      <div class="fld"><label for="chPartyQ">Branch or authorised person</label><input class="in" id="chPartyQ" placeholder="Search" autocomplete="off"></div><div class="ch-pick" id="chParties">${chParties('')}</div></div>`);
  },
  chThread: async (id, el) => {
    const desk = el.dataset.party ? $('chDeskSel').value : id;
    try { const c = await post('/v1/chat/threads', { desk, party: el.dataset.party || '' }); closeModal(); chGoTo(c.id); } catch (e) { toast(e.message, true); }
  },
  chManage: () => chManageModal(),
  chEditChannel: id => chChannelForm(id),
  chEditDesk: id => chDeskForm(id),
  chSaveChannel: async id => {
    const body = { name: $('chcName').value, about: $('chcAbout').value, scope: $('chcScope').value, archived: !!$('chcArch')?.checked, members: [...document.querySelectorAll('[data-chm]:checked')].map(x => x.dataset.chm) };
    try { await api(id ? 'PUT' : 'POST', '/v1/chat/channels' + (id ? '/' + encU(id) : ''), body); CH.home = await get('/v1/chat'); toast(id ? 'Channel saved' : 'Channel created'); chManageModal(); chRedrawList(); } catch (e) { toast(e.message, true); }
  },
  chSaveDesk: async id => {
    const body = { name: $('chdName').value, about: $('chdAbout').value, archived: !!$('chdArch')?.checked, members: [...document.querySelectorAll('[data-chm]:checked')].map(x => x.dataset.chm) };
    try { await api(id ? 'PUT' : 'POST', '/v1/chat/desks' + (id ? '/' + encU(id) : ''), body); CH.home = await get('/v1/chat'); toast(id ? 'Desk saved' : 'Desk created'); chManageModal(); } catch (e) { toast(e.message, true); }
  }
});
function chPeople(ps, q) {
  q = q.toLowerCase();
  const m = ps.filter(p => !q || (p.name + ' ' + p.role + ' ' + p.id).toLowerCase().includes(q));
  return m.length ? m.map(p => `<button data-act="chDM" data-id="${esc(p.id)}"><span class="ch-ic av">${esc(initials(p.name))}</span><span><b>${esc(p.name)}</b><small>${esc(p.role)}</small></span></button>`).join('') : '<div class="empty">Nobody matches</div>';
}
function chParties(q) {
  q = q.toLowerCase();
  const m = (CH.home.parties || []).filter(p => !q || (p.name + ' ' + p.scope).toLowerCase().includes(q)).slice(0, 60);
  return m.length ? m.map(p => `<button data-act="chThread" data-party="${esc(p.scope)}"><b>${esc(p.name)}</b><small>${p.scope.startsWith('ap:') ? 'Authorised person' : 'Branch'}</small></button>`).join('') : '<div class="empty">Nothing matches</div>';
}
document.addEventListener('input', e => {
  if (e.target.id === 'chPeopleQ') $('chPeople').innerHTML = chPeople(CH.home.people, e.target.value);
  if (e.target.id === 'chPartyQ') $('chParties').innerHTML = chParties(e.target.value);
});

/* ---------- Admin: channels and desks ---------- */
const scopeName = s => (CH.home.manage.scopes.find(x => x.scope === s) || {}).name || s;
function chManageModal() {
  const m = CH.home.manage;
  modal(`<div class="hd"><div><h2>Channels & desks</h2><p>Only the members you add can read a channel. Desks answer branches and APs privately, one thread each. You manage channels without reading them, unless you add yourself.</p></div><button class="x" data-act="close" aria-label="Close">×</button></div>
    <div class="bd"><div class="panel"><div class="hd"><h3>Channels</h3><button class="btn sm pri" data-act="chEditChannel" data-id="">New channel</button></div>
      ${tbl([['Channel'], ['For'], ['Members', 'r'], ['']], m.channels.map(c => `<tr><td><b>#${esc(c.name)}</b>${c.archived ? ' ' + pill('Archived') : ''}<div class="dim small">${esc(c.about || '')}</div></td><td>${esc(scopeName(c.scope))}</td><td class="r mono">${(c.members || []).length}</td><td class="r"><button class="btn sm" data-act="chEditChannel" data-id="${esc(c.id)}">Edit</button></td></tr>`), { empty: 'No channels yet.' })}</div>
    <div class="panel"><div class="hd"><h3>Official desks</h3><button class="btn sm pri" data-act="chEditDesk" data-id="">New desk</button></div>
      ${tbl([['Desk'], ['Answered by'], ['']], m.desks.map(d => `<tr><td><b>${esc(d.name)}</b>${d.archived ? ' ' + pill('Archived') : ''}<div class="dim small">${esc(d.about || '')}</div></td><td class="small">${(d.members || []).length ? esc(d.members.map(id => (m.staff.find(s => s.id === id) || {}).name || id).join(', ')) : '<span class="warn">Nobody: Admins answer</span>'}</td><td class="r"><button class="btn sm" data-act="chEditDesk" data-id="${esc(d.id)}">Edit</button></td></tr>`))}</div></div>`);
}
function chMembers(scope, sel) {
  const st = CH.home.manage.staff.filter(s => s.scope === scope);
  return st.length ? `<div class="ch-members">${st.map(s => `<label class="check"><input type="checkbox" data-chm="${esc(s.id)}" ${sel.includes(s.id) ? 'checked' : ''}> <span>${esc(s.name)} <span class="dim small">${esc(s.role)}</span></span></label>`).join('')}</div>` : '<div class="empty">No active staff there yet.</div>';
}
function chChannelForm(id) {
  const m = CH.home.manage, c = m.channels.find(x => x.id === id) || { name: '', about: '', scope: 'ho', members: [] };
  modal(`<div class="hd"><div><h2>${id ? '#' + esc(c.name) : 'New channel'}</h2><p>Head office channels are for head office staff; a branch’s channels for that branch’s staff.</p></div><button class="x" data-act="close" aria-label="Close">×</button></div>
    <div class="bd"><div class="ch-two"><div class="fld"><label for="chcName">Name</label><input class="in" id="chcName" value="${esc(c.name)}" placeholder="e.g. eod-ops" maxlength="40"></div>
      <div class="fld"><label for="chcScope">For</label><select class="in" id="chcScope" ${id ? 'disabled' : ''}>${m.scopes.map(s => `<option value="${esc(s.scope)}" ${s.scope === c.scope ? 'selected' : ''}>${esc(s.name)}</option>`).join('')}</select></div></div>
      <div class="fld"><label for="chcAbout">What it is for</label><input class="in" id="chcAbout" value="${esc(c.about || '')}" maxlength="200"></div>
      <div class="fld"><label>Members (only they can read it)</label><div id="chcMembers">${chMembers(c.scope, c.members || [])}</div></div>
      ${id ? `<label class="check"><input type="checkbox" id="chcArch" ${c.archived ? 'checked' : ''}> <span>Archived: members can read it but not write</span></label>` : ''}</div>
    <div class="ft"><button class="btn" data-act="chManage">Back</button><button class="btn pri" data-act="chSaveChannel" data-id="${esc(id || '')}">${id ? 'Save' : 'Create channel'}</button></div>`);
}
document.addEventListener('change', e => { if (e.target.id === 'chcScope') $('chcMembers').innerHTML = chMembers(e.target.value, []); });
function chDeskForm(id) {
  const m = CH.home.manage, d = m.desks.find(x => x.id === id) || { name: '', about: '', members: [] };
  modal(`<div class="hd"><div><h2>${id ? esc(d.name) : 'New desk'}</h2><p>Head office staff who answer branches and APs for this desk. They see every branch’s and AP’s thread with it.</p></div><button class="x" data-act="close" aria-label="Close">×</button></div>
    <div class="bd"><div class="fld"><label for="chdName">Name</label><input class="in" id="chdName" value="${esc(d.name)}" placeholder="e.g. Accounts desk" maxlength="40"></div>
      <div class="fld"><label for="chdAbout">What to bring to it</label><input class="in" id="chdAbout" value="${esc(d.about || '')}" maxlength="200"></div>
      <div class="fld"><label>Answered by</label>${chMembers('ho', d.members || [])}</div>
      ${id ? `<label class="check"><input type="checkbox" id="chdArch" ${d.archived ? 'checked' : ''}> <span>Archived: no new messages</span></label>` : ''}</div>
    <div class="ft"><button class="btn" data-act="chManage">Back</button><button class="btn pri" data-act="chSaveDesk" data-id="${esc(id || '')}">${id ? 'Save' : 'Create desk'}</button></div>`);
}
