'use strict';
/* Ledgerline console: Boards quick edit, keyboard, undo and the editor.
   Builds on boards.js (loaded first) and uses the core's own popovers,
   pickers and calls (bdPop, bdPatch, bdMoveOpen, bdUpload, bdGot...), so
   nothing here duplicates them.
     Quick edit   a card's ⋯, a right-click or e: the title in place, and
                  Open card, Edit labels, Change members, Change cover, Edit
                  dates, Move, Copy, Copy link, Mirror (boardextras), Archive.
     Keyboard     a visible selection (arrows or h j k l), Enter, e t s v c d
                  m, 1-0 labels, b board switcher, w menu, ; label names,
                  Ctrl/Cmd+C and V for cards, z and Shift+Z; the core's keys.
     Undo         after each change on this screen (BOARDS.onApi) a toast
                  with Undo, and a stack per board for the tab; redo.
     Composer     #label @person ^top|^bottom|^3 with autocomplete
                  (BOARDS.compose); several pasted lines become several
                  cards; files dropped on a list become a card.
     Editor       the description's toolbar and emoji; the safe markdown
                  (bdMd, bdInline): headings, quotes, code, tables, rules,
                  strike, smart card links and :shortcodes:.
   Actions and UI state start with bu, classes with bu-. */

const BU = {
  qe: null, sel: '', hover: '', lastB: '',
  truth: new Map(), truthV: null,          // each card as the server last had it
  undo: {}, redo: {}, busy: false, seq: 0, // per board: [{ id, text, ops, ready }]
  clip: null, cac: null, sw: null, paste: null, ed: null,
  links: new Map()                          // smart links: 'board/card' -> what is known
};
Object.assign(BD_IC, {
  bold: '<path d="M7.5 5h5.3a3.3 3.3 0 0 1 0 6.6H7.5zM7.5 11.6h6a3.45 3.45 0 0 1 0 6.9h-6z"/>',
  italic: '<path d="M11 5h7M6 19h7M14.5 5l-5 14"/>',
  strike: '<path d="M4.5 12h15M16.2 7.6C15.5 6 13.9 5 11.8 5 9.3 5 7.5 6.4 7.5 8.3c0 1.2.6 2.1 1.8 2.7M8.1 16.2c.6 1.7 2.4 2.8 4.7 2.8 2.7 0 4.6-1.4 4.6-3.4 0-.6-.2-1.2-.5-1.7"/>',
  heading: '<path d="M6 5v14M17 5v14M6 12h11"/>',
  ul: '<path d="M10 7h9.5M10 12h9.5M10 17h9.5"/><circle class="f" cx="5.5" cy="7" r="1.4"/><circle class="f" cx="5.5" cy="12" r="1.4"/><circle class="f" cx="5.5" cy="17" r="1.4"/>',
  ol: '<path d="M10.5 7h9M10.5 12h9M10.5 17h9M4.6 5.6 6 5v4.4M4.4 14a1.3 1.3 0 0 1 2.5.5c0 1.1-2.5 1.8-2.5 3.1h2.7"/>',
  quote: '<path d="M5 7.5h5.2v5H7.6c0 2.2-1 3.7-2.6 4.6M13.8 7.5H19v5h-2.6c0 2.2-1 3.7-2.6 4.6"/>',
  code: '<path d="m9 7-5 5 5 5M15 7l5 5-5 5"/>',
  at: '<circle cx="12" cy="12" r="3.6"/><path d="M15.6 12v1.5a2.4 2.4 0 0 0 4.8 0V12a8.4 8.4 0 1 0-3.4 6.8"/>',
  mirror: '<rect x="3.5" y="7.5" width="12" height="12.5" rx="2"/><path d="M8.5 4h10a2 2 0 0 1 2 2v10"/>',
  undo: '<path d="M8.5 5.5 4 10l4.5 4.5M4 10h10.5a5 5 0 0 1 0 10H11"/>',
  pencil: '<path d="M4.5 19.5l1-4 10.2-10.2a2.1 2.1 0 0 1 3 3L8.5 18.5zM14 7l3 3"/>'
});
const buMac = () => /Mac|iPhone|iPad|iPod/.test(navigator.platform || navigator.userAgent || '');
const buMod = () => buMac() ? '⌘' : 'Ctrl';
const buCardEl = id => id ? document.querySelector(`#bd-board .bd-card[data-card="${CSS.escape(id)}"]`) : null;
const buCard = id => (BD.v?.cards || []).find(c => c.id === id) || null;
const buLinked = c => !!c?.link && BD.v?.board.kind === 'ledgerline';
const buListName = (b, id) => (b?.lists || []).find(l => l.id === id)?.name || '';
const buLow = s => s ? s[0].toLowerCase() + s.slice(1) : s;
const buSet = a => (a || []).slice().sort().join('\u0001');
const buEq = (a, b) => Array.isArray(a) || Array.isArray(b) ? buSet(a) === buSet(b) : (a ?? '') === (b ?? '');

/* ============================== the safe markdown ============================== */
/* Everything is escaped first; only these tags are made: p br b i s code
   pre a h4-h6 ul ol li blockquote hr table, and spans for mentions. Links go
   only to http(s) and to the console's own #routes. */
const BU_EMOJI = [['smile', '😄'], ['joy', '😂'], ['wink', '😉'], ['heart_eyes', '😍'], ['thinking', '🤔'], ['cry', '😢'], ['sob', '😭'], ['scream', '😱'],
  ['thumbsup', '👍'], ['thumbsdown', '👎'], ['clap', '👏'], ['pray', '🙏'], ['wave', '👋'], ['raised_hands', '🙌'], ['ok_hand', '👌'], ['muscle', '💪'],
  ['eyes', '👀'], ['heart', '❤️'], ['fire', '🔥'], ['star', '⭐'], ['sparkles', '✨'], ['tada', '🎉'], ['rocket', '🚀'], ['100', '💯'],
  ['white_check_mark', '✅'], ['x', '❌'], ['warning', '⚠️'], ['exclamation', '❗'], ['question', '❓'], ['bulb', '💡'], ['memo', '📝'], ['calendar', '📅'],
  ['hourglass', '⏳'], ['lock', '🔒'], ['bell', '🔔'], ['moneybag', '💰'], ['chart_with_upwards_trend', '📈'], ['bank', '🏦'], ['pushpin', '📌'], ['email', '📧']];
const BU_EMO = Object.assign(Object.fromEntries(BU_EMOJI), { '+1': '👍', '-1': '👎' });
const BU_HREF = /^(https?:\/\/[^\s<>"]+|#[\w-]+(?:\/[\w-]+)*)$/;
const BU_CARD = /^#boards\/([\w-]+)\/([\w-]+)$/;
function buSelf() { return (location.origin + location.pathname).replace(/&/g, '&amp;'); }
// A link to a card in this console: [board, card] or null. u is escaped text.
function buCardLink(u) {
  const self = buSelf();
  const h = u.startsWith(self) ? u.slice(self.length) : u.startsWith('#') ? u : '';
  const m = BU_CARD.exec(h); return m ? [m[1], m[2]] : null;
}
function bdInline(s, mentions) {
  const keep = [];
  const put = html => { keep.push(html); return `\u0000${keep.length - 1}\u0000`; };
  const fmt = h => h.replace(/\*\*(?=\S)([^*]*?\S)\*\*/g, '<b>$1</b>')
    .replace(/~~(?=\S)([^~]*?\S)~~/g, '<s>$1</s>')
    .replace(/(^|[\s(>])_(?=\S)([^_]*?\S)_(?=$|[\s).,;:!?<])/g, '$1<i>$2</i>')
    .replace(/(^|[\s(>])\*(?=[^\s*])([^*]*?[^\s*])\*(?=$|[\s).,;:!?<])/g, '$1<i>$2</i>')
    .replace(/:([a-z0-9_+-]{1,32}):/g, (all, k) => BU_EMO[k] ? `<span class="bu-emo" role="img" aria-label="${k.replace(/_/g, ' ')}">${BU_EMO[k]}</span>` : all);
  let h = esc(String(s ?? '').replace(/\u0000/g, ''));
  h = h.replace(/`([^`]+)`/g, (_, c) => put(`<code>${c}</code>`));
  h = h.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (all, t, u) => {
    if (!BU_HREF.test(u)) return all;
    const cl = buCardLink(u);
    if (cl) return put(`<a href="#boards/${cl[0]}/${cl[1]}" class="bu-ilink">${fmt(t)}</a>`);
    return put(u.startsWith('#') ? `<a href="${u}" class="bu-ilink">${fmt(t)}</a>` : `<a href="${u}" target="_blank" rel="noopener noreferrer">${fmt(t)}</a>`);
  });
  h = h.replace(/(^|[\s(])(https?:\/\/[^\s<]+[^\s<.,;:!?)\]'"])/g, (_, p, u) => { const cl = buCardLink(u); return p + put(cl ? buChip(cl[0], cl[1], u) : `<a href="${u}" target="_blank" rel="noopener noreferrer">${u}</a>`); });
  h = h.replace(/(^|[\s(])(#boards\/[\w-]+\/[\w-]+)(?=$|[\s).,;:!?])/g, (_, p, u) => { const cl = buCardLink(u); return p + put(buChip(cl[0], cl[1], u)); });
  if (mentions) h = h.replace(/(^|[\s(])@([a-z0-9][a-z0-9._-]*[a-z0-9]|[a-z0-9])/g, (all, p, id) => { const u = bdUsers().find(x => x.id === id); return u ? p + put(`<span class="bd-mention${id === bdMe() ? ' me' : ''}" title="@${esc(id)}">@${esc(u.name)}</span>`) : all; });
  h = fmt(h);
  for (let i = 0; i < 4 && h.includes('\u0000'); i++) h = h.replace(/\u0000(\d+)\u0000/g, (_, n) => keep[+n] ?? '');
  return h;
}
const BU_SEP = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;
function buCells(s) {
  let x = s.trim().replace(/\\\|/g, '\u0001');
  if (x.startsWith('|')) x = x.slice(1);
  if (x.endsWith('|')) x = x.slice(0, -1);
  return x.split('|').map(c => c.trim().replace(/\u0001/g, '|'));
}
function bdMd(t, mentions, depth = 0) {
  const lines = String(t || '').replace(/\u0000/g, '').replace(/\r\n?/g, '\n').split('\n');
  const inl = s => bdInline(s, mentions);
  let out = '', para = [], list = null;
  const flushP = () => { if (para.length) { out += `<p>${para.map(inl).join('<br>')}</p>`; para = []; } };
  const flushL = () => { if (list) { out += `<${list.tag}${list.start > 1 ? ` start="${list.start}"` : ''}>${list.items.map(x => `<li>${x}</li>`).join('')}</${list.tag}>`; list = null; } };
  const flush = () => { flushP(); flushL(); };
  for (let i = 0; i < lines.length; i++) {
    const ln = lines[i];
    let m;
    if ((m = /^\s{0,3}(```+|~~~+)\s*[\w+.-]*\s*$/.exec(ln))) {
      flush();
      const fence = m[1][0].repeat(3), body = [];
      for (i++; i < lines.length && !lines[i].trim().startsWith(fence); i++) body.push(lines[i]);
      out += `<pre class="bu-pre"><code>${esc(body.join('\n'))}</code></pre>`;
      continue;
    }
    if (!ln.trim()) { flush(); continue; }
    if ((m = /^\s{0,3}(#{1,3})\s+(.+?)\s*#*\s*$/.exec(ln))) { flush(); const n = m[1].length + 3; out += `<h${n} class="bu-h">${inl(m[2])}</h${n}>`; continue; }
    if (/^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/.test(ln)) { flush(); out += '<hr>'; continue; }
    if (/^\s{0,3}>/.test(ln)) {
      flush();
      const q = [];
      for (; i < lines.length && /^\s{0,3}>/.test(lines[i]); i++) q.push(lines[i].replace(/^\s{0,3}>\s?/, ''));
      i--;
      out += `<blockquote>${depth < 3 ? bdMd(q.join('\n'), mentions, depth + 1) : `<p>${q.map(inl).join('<br>')}</p>`}</blockquote>`;
      continue;
    }
    if (ln.includes('|') && i + 1 < lines.length && lines[i + 1].includes('|') && BU_SEP.test(lines[i + 1])) {
      flush();
      const head = buCells(ln), al = buCells(lines[i + 1]).map(c => /^:-+:$/.test(c) ? ' class="bu-c"' : /-:$/.test(c) ? ' class="bu-r"' : '');
      const rows = [];
      for (i += 2; i < lines.length && lines[i].trim() && lines[i].includes('|'); i++) rows.push(buCells(lines[i]));
      i--;
      const cell = (tag, cs) => head.map((_, j) => `<${tag}${al[j] || ''}>${inl(cs[j] ?? '')}</${tag}>`).join('');
      out += `<div class="bu-tw"><table class="bu-tbl"><thead><tr>${cell('th', head)}</tr></thead><tbody>${rows.map(r => `<tr>${cell('td', r)}</tr>`).join('')}</tbody></table></div>`;
      continue;
    }
    if ((m = /^\s*[-*+]\s+(.*)$/.exec(ln))) { flushP(); if (list?.tag !== 'ul') { flushL(); list = { tag: 'ul', items: [] }; } list.items.push(inl(m[1])); continue; }
    if ((m = /^\s*(\d{1,9})[.)]\s+(.*)$/.exec(ln))) { flushP(); if (list?.tag !== 'ol') { flushL(); list = { tag: 'ol', items: [], start: +m[1] }; } list.items.push(inl(m[2])); continue; }
    flushL(); para.push(ln);
  }
  flush();
  return out;
}

/* ---------- smart card links ---------- */
function buChipHTML(b, c, v) {
  const other = !BD.v || b !== BD.v.board.id;
  const bname = v.boardName || (BD.home || BD.boards || []).find(x => x.id === b)?.name || '';
  const where = [v.list, other ? bname : ''].filter(Boolean).join(' · ');
  return `<a class="bu-chip" href="#boards/${esc(b)}/${esc(c)}" title="Open card #${v.no}${other && bname ? ' on ' + esc(bname) : ''}">${bdI('card')}<span class="bu-chip-no">#${v.no}</span><span class="bu-chip-t">${esc(v.title)}</span>${where ? `<span class="bu-chip-l">${esc(where)}</span>` : ''}${v.archived ? '<span class="bu-chip-a">Archived</span>' : ''}</a>`;
}
function buLinkInfo(b, c) {
  if (BD.v && b === BD.v.board.id) {
    const x = buCard(c);
    if (x) return { st: 'ok', no: x.no, title: x.title, list: buListName(BD.v.board, x.list) };
  }
  const k = b + '/' + c, v = BU.links.get(k);
  if (!v || (!v.busy && Date.now() - v.at > 120000)) setTimeout(() => buLinkFetch(b, c), 0);
  return v || null;
}
// A card link drawn as a chip when the person can see the card; otherwise
// (or until it is known) the plain link. u is the escaped text of the link.
function buChip(b, c, u) {
  const v = buLinkInfo(b, c);
  if (v?.st === 'ok') return buChipHTML(b, c, v);
  return `<a href="#boards/${esc(b)}/${esc(c)}" class="bu-ilink" data-bul="${esc(b)}/${esc(c)}">${u}</a>`;
}
async function buLinkFetch(b, c) {
  const k = b + '/' + c, old = BU.links.get(k);
  if (old?.busy) return;
  BU.links.set(k, { ...(old || { st: 'load' }), busy: true, at: Date.now() });
  let v;
  try {
    const x = (await get(`/v1/boards/${bdE(b)}/cards/${bdE(c)}`)).card;
    v = { st: 'ok', no: x.no, title: x.title, archived: !!x.archived, list: BD.v && b === BD.v.board.id ? buListName(BD.v.board, x.list) : '', at: Date.now() };
    if (!v.list) { try { const h = (await get('/v1/boards/search?q=' + bdE('#' + x.no))).find(y => y.board === b && y.card === c); if (h) { v.list = h.list; v.boardName = h.board_name; } } catch (e) {} }
  } catch (e) { v = { st: 'no', at: Date.now() }; }
  BU.links.set(k, v);
  document.querySelectorAll('[data-bul]').forEach(a => { if (a.dataset.bul !== k) return; if (v.st === 'ok') a.outerHTML = buChipHTML(b, c, v); else a.removeAttribute('data-bul'); });
}

/* ============================== the description's toolbar ============================== */
function buEdBar(id) {
  const b = (cmd, icon, label, key, i) => `<button type="button" class="bu-tb-b" data-act="buFmt" data-id="${cmd}" data-ed="${id}" aria-label="${label}" title="${label}${key ? ` (${key})` : ''}" tabindex="${i ? -1 : 0}"${cmd === 'emoji' ? ' data-popkey="bu-emoji"' : ''}>${bdI(icon)}</button>`;
  const m = buMod();
  return `<div class="bu-tb" role="toolbar" aria-label="Formatting" aria-controls="${id}">${b('bold', 'bold', 'Bold', m + '+B', 0)}${b('italic', 'italic', 'Italic', m + '+I', 1)}${b('strike', 'strike', 'Strikethrough', '', 1)}<span class="bu-tb-sep"></span>${b('heading', 'heading', 'Heading', '', 1)}${b('ul', 'ul', 'Bulleted list', '', 1)}${b('ol', 'ol', 'Numbered list', '', 1)}${b('quote', 'quote', 'Quote', '', 1)}${b('code', 'code', 'Code', '', 1)}<span class="bu-tb-sep"></span>${b('link', 'link', 'Link', m + '+K', 1)}${b('mention', 'at', 'Mention someone', '', 1)}${b('emoji', 'smile', 'Emoji', '', 1)}</div>`;
}
// Replace [a, b) with text, keeping the box's own undo where the browser can.
function buEdPut(ta, a, b, text, s0, s1) {
  ta.focus({ preventScroll: true });
  ta.setSelectionRange(a, b);
  let ok = false;
  try { ok = document.execCommand('insertText', false, text); } catch (e) {}
  if (!ok || ta.value.slice(a, a + text.length) !== text) { ta.setRangeText(text, a, b, 'end'); ta.dispatchEvent(new Event('input', { bubbles: true })); }
  ta.setSelectionRange(a + (s0 ?? text.length), a + (s1 ?? s0 ?? text.length));
}
function buWrap(ta, pre, post, ph) {
  const v = ta.value, s = ta.selectionStart, e = ta.selectionEnd, sel = v.slice(s, e);
  if (sel && v.slice(s - pre.length, s) === pre && v.slice(e, e + post.length) === post) { buEdPut(ta, s - pre.length, e + post.length, sel, 0, sel.length); return; }
  if (sel.length > pre.length + post.length && sel.startsWith(pre) && sel.endsWith(post)) { const inner = sel.slice(pre.length, sel.length - post.length); buEdPut(ta, s, e, inner, 0, inner.length); return; }
  const t = sel || ph;
  buEdPut(ta, s, e, pre + t + post, pre.length, pre.length + t.length);
}
function buLines(ta, add, test, strip) {
  const v = ta.value, s = ta.selectionStart, e = ta.selectionEnd;
  const a = v.lastIndexOf('\n', s - 1) + 1, z0 = v.indexOf('\n', e > s && v[e - 1] === '\n' ? e - 1 : e), z = z0 < 0 ? v.length : z0;
  const ls = v.slice(a, z).split('\n'), used = ls.filter(l => l.trim());
  const off = used.length && used.every(l => test.test(l));
  let n = 0;
  const out = ls.map(l => !l.trim() && ls.length > 1 ? l : off ? l.replace(strip, '') : add(l.replace(strip, ''), ++n)).join('\n');
  if (used.length) buEdPut(ta, a, z, out, 0, out.length); else buEdPut(ta, a, z, out);
}
function buFmtGo(ta, cmd, el) {
  if (!ta) return;
  const v = ta.value, s = ta.selectionStart, e = ta.selectionEnd, sel = v.slice(s, e);
  if (cmd === 'bold') buWrap(ta, '**', '**', 'bold text');
  else if (cmd === 'italic') buWrap(ta, '_', '_', 'italic text');
  else if (cmd === 'strike') buWrap(ta, '~~', '~~', 'struck text');
  else if (cmd === 'heading') buLines(ta, l => '## ' + l, /^\s{0,3}#{1,6}\s/, /^\s{0,3}#{1,6}\s+/);
  else if (cmd === 'ul') buLines(ta, l => '- ' + l, /^\s*[-*+]\s/, /^\s*[-*+]\s+/);
  else if (cmd === 'ol') buLines(ta, (l, n) => n + '. ' + l, /^\s*\d+[.)]\s/, /^\s*\d+[.)]\s+/);
  else if (cmd === 'quote') buLines(ta, l => '> ' + l, /^\s{0,3}>/, /^\s{0,3}>\s?/);
  else if (cmd === 'code') {
    if (sel.includes('\n') || (!sel && (s === 0 || v[s - 1] === '\n') && (s === v.length || v[s] === '\n'))) {
      const pre = s && v[s - 1] !== '\n' ? '\n' : '', post = e < v.length && v[e] !== '\n' ? '\n' : '', body = sel || 'code';
      buEdPut(ta, s, e, `${pre}\`\`\`\n${body}\n\`\`\`${post}`, pre.length + 4, pre.length + 4 + body.length);
    } else buWrap(ta, '`', '`', 'code');
  } else if (cmd === 'link') {
    if (/^https?:\/\/\S+$/.test(sel)) buEdPut(ta, s, e, `[link text](${sel})`, 1, 10);
    else { const t = sel || 'link text'; buEdPut(ta, s, e, `[${t}](https://)`, t.length + 3, t.length + 11); }
  } else if (cmd === 'mention') {
    const pre = s && !/\s/.test(v[s - 1]) ? ' ' : '';
    buEdPut(ta, s, e, pre + '@');
  } else if (cmd === 'emoji') {
    BU.ed = { id: ta.id, s, e };
    bdPop('bu-emoji', el || ta, 'Emoji', () => `<div class="bu-emos" role="group" aria-label="Emoji">${BU_EMOJI.map(([k, ch]) => `<button type="button" class="bu-emob" data-act="buEmojiGo" data-id="${k}" aria-label="${k.replace(/_/g, ' ')}" title=":${k}:">${ch}</button>`).join('')}</div><p class="bd-hint">Or type :${'smile'}: and the like.</p>`, { live: false });
  }
}

/* ============================== undo and redo ============================== */
const BU_FIELDS = ['title', 'description', 'labels', 'members', 'start', 'due', 'due_done', 'reminder', 'archived'];
const buPick = c => ({ id: c.id, board: c.board, no: c.no, list: c.list, pos: c.pos, title: c.title, description: c.description || '', labels: (c.labels || []).slice(), members: (c.members || []).slice(),
  start: c.start || '', due: c.due || '', due_done: !!c.due_done, reminder: +c.reminder || 0, archived: !!c.archived, link: !!c.link });
function buSnap() { if (!BD.v || BU.truthV === BD.v) return; BU.truthV = BD.v; BU.truth = new Map(BD.v.cards.map(c => [c.id, buPick(c)])); }
function buCardText(c, keys, before, after) {
  const no = '#' + c.no, has = k => keys.includes(k);
  if (has('archived')) return after.archived ? `Archived ${no}` : `Restored ${no}`;
  if (has('title')) return `Renamed ${no}`;
  if (has('labels')) {
    const add = after.labels.filter(x => !before.labels.includes(x)), rm = before.labels.filter(x => !after.labels.includes(x)), nm = id => { const l = bdLabel(id); return l ? (l.name || BD_CN[l.color]) : 'a'; };
    if (add.length === 1 && !rm.length) return `Added the label ${nm(add[0])} to ${no}`;
    if (rm.length === 1 && !add.length) return `Removed the label ${nm(rm[0])} from ${no}`;
    return `Changed the labels on ${no}`;
  }
  if (has('members')) {
    const add = after.members.filter(x => !before.members.includes(x)), rm = before.members.filter(x => !after.members.includes(x));
    if (add.length === 1 && !rm.length) return `Added ${bdName(add[0])} to ${no}`;
    if (rm.length === 1 && !add.length) return `Removed ${bdName(rm[0])} from ${no}`;
    return `Changed the members of ${no}`;
  }
  if (has('description')) return `Changed the description of ${no}`;
  if (keys.length === 1 && has('due_done')) return after.due_done ? `Marked ${no} complete` : `Marked ${no} not complete`;
  return `Changed the dates of ${no}`;
}
function buEntry(method, path, body, r) {
  const m = /^\/v1\/boards\/([\w-]+)\/(cards|lists)\/([\w-]+)(?:\/([\w-]+))?\/?(?:\?.*)?$/.exec(path);
  if (!m || m[1] !== UI.bdBoard) return null;
  const [, board, what, id, op = ''] = m, b = BD.v.board, card = r && r.id && r.board ? r : null;
  if (what === 'cards' && method === 'PATCH' && !op && card) {
    const keys = Object.keys(body || {}).filter(k => BU_FIELDS.includes(k));
    if (!keys.length) return null;
    // The window's (or quick edit's) card is replaced only after the call: it is the state the change was made from.
    const src = BD.cd && BD.cd.id === id ? buPick(BD.cd) : BU.truth.get(id);
    const now = buPick(card), after = Object.fromEntries(keys.map(k => [k, now[k]]));
    const before = src ? Object.fromEntries(keys.map(k => [k, src[k]])) : keys.length === 1 && keys[0] === 'archived' ? { archived: !after.archived } : null;
    if (before?.labels) before.labels = before.labels.filter(bdLabel); // a label removed from the board cannot come back
    if (!before || keys.every(k => buEq(before[k], after[k]))) return null;
    return { board, text: buCardText(card, keys, before, after), ops: [{ type: 'patch', id, no: card.no, before, after }] };
  }
  if (what === 'cards' && method === 'POST' && op === 'move' && card) {
    if ((body?.board && body.board !== board) || card.board !== board) return null; // to another board: not here
    const t = BU.truth.get(id);
    if (!t || (b.kind === 'ledgerline' && (t.link || card.link))) return null; // a Ledgerline card's move acts on its record
    const before = { list: t.list, pos: t.pos }, after = { list: card.list, pos: card.pos };
    if (before.list === after.list && before.pos === after.pos) return null;
    return { board, text: before.list === after.list ? `Moved #${card.no} within ${buListName(b, card.list)}` : `Moved #${card.no} to ${buListName(b, card.list)}`, ops: [{ type: 'move', id, no: card.no, before, after }] };
  }
  if (what !== 'lists') return null;
  const name = buListName(b, id), inList = [...BU.truth.values()].filter(c => c.list === id && !c.archived);
  if (method === 'PUT' && !op && body?.archived === true) return { board, text: `Archived the list ${name}`, ops: [{ type: 'list', id, before: { archived: false }, after: { archived: true } }] };
  if (method !== 'POST') return null;
  if (op === 'move-cards' && r?.moved) {
    if (b.kind === 'ledgerline' && inList.some(c => c.link)) return null;
    return { board, settle: true, text: `Moved ${bdPl(r.moved, 'card')} to ${buListName(b, body?.to)}`, ops: inList.map(c => ({ type: 'move', id: c.id, no: c.no, before: { list: c.list, pos: c.pos }, after: null })) };
  }
  if (op === 'sort' && inList.length > 1) return { board, settle: true, text: `Sorted the list ${name}`, ops: inList.map(c => ({ type: 'move', id: c.id, no: c.no, before: { list: c.list, pos: c.pos }, after: null })) };
  if (op === 'archive-cards' && r?.archived) return { board, text: `Archived ${bdPl(r.archived, 'card')} in ${name}`, ops: inList.map(c => ({ type: 'patch', id: c.id, no: c.no, before: { archived: false }, after: { archived: true } })) };
  return null;
}
BOARDS.onApi.push((method, path, body, r) => {
  if (!BD.v || method === 'GET') return;
  const m = /^\/v1\/boards\/[\w-]+\/cards\/([\w-]+)(?:\/([\w-]+))?/.exec(path);
  if (!BU.busy) { const ent = buEntry(method, path, body, r); if (ent) buRecord(ent); }
  if (r && r.id && r.board === UI.bdBoard && r.list) BU.truth.set(r.id, buPick(r));
  // Quick edit closes once its card is moved, copied or archived.
  if (BU.qe && m && m[1] === BU.qe.id && (m[2] === 'move' || m[2] === 'copy' || body?.archived)) setTimeout(() => buQeClose(), 0);
});
function buRecord(ent) {
  ent.id = ++BU.seq;
  if (ent.settle) {
    // A list-wide change: read where its cards ended up.
    ent.ready = get(bdURL()).then(v => { ent.ops.forEach(o => { const c = v.cards.find(x => x.id === o.id); o.after = c ? { list: c.list, pos: c.pos } : null; }); }).catch(() => { ent.ops = []; });
  }
  const b = ent.board, st = BU.undo[b] = BU.undo[b] || [];
  st.push(ent); if (st.length > 30) st.shift();
  BU.redo[b] = [];
  buToast(ent.text, 'buUndo', 'Undo', ent.id);
}
// The console's toast, with a button. Drawn after the core's own toast for the same change.
function buToast(msg, act, label, id) {
  setTimeout(() => {
    const t = $('toast'); if (!t) return;
    clearTimeout(toast.t); t.hidden = false;
    t.setAttribute('role', 'status');
    t.innerHTML = `<span>${esc(msg)}</span>${act ? `<button class="bu-tbtn" data-act="${act}" data-id="${id || ''}" title="${label === 'Undo' ? 'Undo (z)' : 'Redo (Shift+Z)'}">${bdI('undo')}${label}</button>` : ''}`;
    toast.t = setTimeout(() => { t.hidden = true; }, 9000);
  }, 0);
}
async function buNow(ent, ops) {
  if (ops.some(o => o.type === 'list')) { const v = await get(bdURL()); return { lists: v.board.lists }; }
  if (ops.length === 1) { try { return { cards: new Map([[ops[0].id, (await get(bdCURL(ops[0].id))).card]]) }; } catch (e) { if (e.status === 404) return { cards: new Map() }; throw e; } }
  const [a, z] = await Promise.all([get(bdURL()), get(bdURL('?archived=1'))]);
  return { cards: new Map([...a.cards, ...z.cards].map(c => [c.id, c])) };
}
function buStill(o, now, have, board) {
  if (o.type === 'list') { const l = (now.lists || []).find(x => x.id === o.id); return !!l && !!l.archived === o[have].archived; }
  const c = now.cards.get(o.id); if (!c || c.board !== board) return false;
  if (o.type === 'move') return !c.archived && c.list === o[have].list && c.pos === o[have].pos;
  const p = buPick(c); return Object.keys(o[have]).every(k => buEq(p[k], o[have][k]));
}
async function buApply(o, want) {
  if (o.type === 'list') return bdApi('PUT', bdURL(`/lists/${bdE(o.id)}`), { archived: o[want].archived });
  if (o.type === 'move') return bdApi('POST', bdCURL(o.id, '/move'), { list: o[want].list, pos: o[want].pos });
  return bdApi('PATCH', bdCURL(o.id), o[want]);
}
async function buUndo(id, redo) {
  if (!UI.bdBoard || !BD.v || BU.busy) return;
  const b = UI.bdBoard, st = (redo ? BU.redo : BU.undo)[b] || [];
  const ent = id ? st.find(x => x.id === +id) : st[st.length - 1];
  if (!ent) { toast(redo ? 'Nothing to redo on this board' : 'Nothing to undo on this board'); return; }
  const have = redo ? 'before' : 'after', want = redo ? 'after' : 'before', verb = redo ? 'redo' : 'undo';
  BU.busy = true;
  try {
    await ent.ready;
    const ops = ent.ops.filter(o => o.before && o.after);
    if (!ops.length) { st.splice(st.indexOf(ent), 1); toast(`Can’t ${verb} that change`); return; }
    const now = await buNow(ent, ops);
    const bad = ops.find(o => !buStill(o, now, have, b));
    if (bad) {
      st.splice(st.indexOf(ent), 1);
      toast(`Can’t ${verb}: ${bad.type === 'list' ? 'the list' : '#' + bad.no} was changed since. Nothing was changed.`);
      return;
    }
    for (const o of ops) await buApply(o, want);
    st.splice(st.indexOf(ent), 1);
    const other = redo ? BU.undo : BU.redo; (other[b] = other[b] || []).push(ent);
    await BOARDS.reload();
    buToast(`${redo ? 'Redone' : 'Undone'}: ${buLow(ent.text)}`, redo ? 'buUndo' : 'buRedo', redo ? 'Undo' : 'Redo', ent.id);
  } catch (e) { toast(e.message, true); bdSoon(100); }
  finally { BU.busy = false; }
}

/* ============================== selection ============================== */
function buSelMark() {
  document.querySelectorAll('#bd-board .bd-card.bu-sel').forEach(x => { if (x.dataset.card !== BU.sel) x.classList.remove('bu-sel'); });
  if (!BU.sel) return;
  const el = buCardEl(BU.sel);
  if (el) el.classList.add('bu-sel'); else BU.sel = '';
}
function buSelect(id) {
  BU.sel = id || ''; buSelMark();
  const el = buCardEl(BU.sel); if (!el) return;
  el.querySelector('.bd-copen')?.focus({ preventScroll: true });
  el.scrollIntoView({ block: 'nearest', inline: 'nearest' });
}
// The card the keys act on: the selection, else the one under the pointer, else the focused one.
function buTarget() {
  if (BD.card && BD.cd) return BD.cd;
  if (bdViewId() !== 'board') return null;
  const f = document.activeElement?.closest?.('#bd-board .bd-card')?.dataset.card;
  const id = [BU.sel, BU.hover, f].find(x => x && buCardEl(x));
  return id ? buCard(id) : null;
}
function buNav(dx, dy) {
  // A collapsed list (a strip, no cards shown) is skipped.
  const ls = [...document.querySelectorAll('#bd-board .bd-list:not(.collapsed)')].map(l => [...l.querySelectorAll('.bd-cards > .bd-card')]).filter(cs => cs.length);
  if (!ls.length) return false;
  const cur = buCardEl(BU.sel) || document.activeElement?.closest?.('#bd-board .bd-card') || buCardEl(BU.hover);
  let li = cur ? ls.findIndex(cs => cs.includes(cur)) : -1, ci = li >= 0 ? ls[li].indexOf(cur) : 0;
  if (li < 0) li = 0;
  else if (dy) ci = Math.max(0, Math.min(ls[li].length - 1, ci + dy));
  else { const nl = Math.max(0, Math.min(ls.length - 1, li + dx)); if (nl !== li) { li = nl; ci = Math.min(ci, ls[li].length - 1); } }
  buSelect(ls[li][ci].dataset.card);
  return true;
}

/* ============================== quick edit ============================== */
BOARDS.quickEdit = id => buQeOpen(id);
function buQeOpen(id) {
  const c = buCard(id);
  if (!c || !BD.v || BD.card) return;
  if (bdViewId() !== 'board' || !buCardEl(id)) { bdOpenCard(id); return; }
  bdPopClose(true); buQeClose(true);
  BU.qe = { id, ret: document.activeElement };
  BD.cd = c; // the core's pickers work on BD.cd when no card window is open
  const root = document.createElement('div');
  root.id = 'bu-qe'; root.className = 'bu-qe';
  root.setAttribute('role', 'dialog'); root.setAttribute('aria-modal', 'true'); root.setAttribute('aria-label', `Quick edit card #${c.no}`);
  root.innerHTML = '<div class="bu-qe-bg" data-act="buQeClose"></div><div class="bu-qe-box" id="bu-qe-box"><div class="bu-qe-card" id="bu-qe-card"></div><div class="bu-qe-acts" id="bu-qe-acts" role="group" aria-label="Card actions"></div></div>';
  document.body.appendChild(root);
  buQePaint();
  const ta = $('bu-qe-title');
  setTimeout(() => { if (ta && document.contains(ta)) { ta.focus({ preventScroll: true }); ta.select(); bdGrow(ta); } else $('bu-qe-acts')?.querySelector('button')?.focus({ preventScroll: true }); }, 0);
}
function buQePaint() {
  const q = BU.qe; if (!q || !$('bu-qe')) return;
  const c = buCard(q.id);
  if (!c || S.view !== 'boards' || bdViewId() !== 'board') { buQeClose(); return; }
  if (!BD.card) BD.cd = c;
  const can = bdCan(), linked = buLinked(c), edit = can && !c.mirror && !linked;
  const a = document.activeElement, s = a?.selectionStart, e = a?.selectionEnd;
  const fk = a && a !== document.body && a.closest?.('#bu-qe') && a.id !== 'bu-qe-title' ? bdFocusKey(a) : '';
  const old = $('bu-qe-title');
  const tpl = document.createElement('template'); tpl.innerHTML = bdCardHTML(c).trim();
  const front = tpl.content.firstElementChild;
  front.classList.add('bu-qe-front'); front.removeAttribute('data-card');
  front.querySelector('.bd-cq')?.remove();
  const ct = front.querySelector('.bd-ct');
  if (ct) {
    const w = document.createElement('div'); w.className = 'bu-qe-tw';
    if (edit) {
      if (old) w.appendChild(old);
      else w.innerHTML = `<label class="bd-sr" for="bu-qe-title">Title of card #${c.no}</label><textarea id="bu-qe-title" class="bu-qe-tin bd-auto" rows="2" maxlength="500" data-enter="buQeSave" data-esc="buQeClose">${esc(c.title)}</textarea>`;
    } else w.innerHTML = `<h4 class="bd-ct"><span class="bd-no">#${c.no}</span> ${esc(c.title)}</h4>`;
    ct.replaceWith(w);
  }
  const note = !can ? 'You can see this card but not change it.' : linked ? 'Its title and list follow its Ledgerline record.' : c.mirror ? 'A mirror: change its title on the original card.' : '';
  const box = $('bu-qe-card');
  box.replaceChildren(front);
  box.insertAdjacentHTML('beforeend', edit ? `<div class="bu-qe-save"><button class="btn pri sm" data-act="buQeSave">Save</button><span class="bd-hint">Enter saves, Esc closes</span></div>` : note ? `<p class="bu-qe-note">${esc(note)}</p>` : '');
  if (edit && note) box.insertAdjacentHTML('beforeend', `<p class="bu-qe-note">${esc(note)}</p>`);
  $('bu-qe-acts').innerHTML = buQeActs(c);
  if (old && a === old) { old.focus({ preventScroll: true }); try { old.setSelectionRange(s, e); } catch (x) {} }
  else if (fk && !document.contains(a)) document.querySelector('#bu-qe ' + fk)?.focus({ preventScroll: true });
  const t2 = $('bu-qe-title'); if (t2) bdGrow(t2);
  buQePlace();
}
function buQeActs(c) {
  const can = bdCan(), own = can && !c.mirror, ctx = BOARDS.ctx();
  const mx = BOARDS.cardButtons.find(x => x.id === 'bxMirror');
  const mirror = can && !!mx && (!mx.show || !!bdSafe(() => mx.show(c, ctx) ? 'y' : '', 'mirror button'));
  const b = (id, icon, label, key, ok = true) => ok ? `<button class="bu-qb" data-act="buQeDo" data-id="${id}"${key ? ` data-popkey="${esc(key)}"` : ''}>${bdI(icon)}<span>${label}</span></button>` : '';
  return b('open', 'card', 'Open card') + b('labels', 'tag', 'Edit labels', 'labels', can) + b('members', 'user', 'Change members', 'members', can) + b('cover', 'image', 'Change cover', 'cover', can) +
    b('dates', 'clock', 'Edit dates', 'dates', own) + b('move', 'arrow', 'Move', 'move-' + c.id, can) + b('copy', 'copy', 'Copy', 'copy-' + c.id, can) + b('link', 'link', 'Copy link', 'clink') +
    b('mirror', 'mirror', 'Mirror', '', mirror) + b('archive', 'archive', 'Archive', '', can && !buLinked(c));
}
function buQePlace() {
  const q = BU.qe, root = $('bu-qe'); if (!q || !root) return;
  if (S.view !== 'boards') { buQeClose(true); return; }
  const box = $('bu-qe-box'), card = $('bu-qe-card'), acts = $('bu-qe-acts');
  const sheet = innerWidth <= 600;
  root.classList.toggle('sheet', sheet);
  if (sheet) { box.style.left = box.style.top = card.style.width = ''; return; }
  const el = buCardEl(q.id); if (el) q.r = el.getBoundingClientRect();
  const r = q.r || { left: 120, top: 120, width: 272 };
  const w = Math.max(256, Math.round(r.width));
  card.style.width = w + 'px';
  const aw = acts.offsetWidth, flip = r.left + w + 8 + aw > innerWidth - 8;
  box.classList.toggle('flip', flip);
  const h = Math.max(card.offsetHeight, acts.offsetHeight);
  const left = Math.max(8, Math.min(flip ? r.left - aw - 8 : r.left, innerWidth - w - aw - 16));
  box.style.left = left + 'px';
  box.style.top = Math.max(8, Math.min(r.top, innerHeight - h - 8)) + 'px';
}
function buQeClose(noFocus) {
  const q = BU.qe; if (!q) return;
  BU.qe = null;
  $('bu-qe')?.remove();
  if (BD.pop) bdPopClose(true);
  if (typeof bxPopClose === 'function') bxPopClose();
  if (!BD.card) BD.cd = null;
  if (noFocus === true) return;
  const f = buCardEl(q.id)?.querySelector('.bd-copen') || (q.ret && document.contains(q.ret) ? q.ret : null);
  f?.focus({ preventScroll: true });
}
async function buCopyLink(c, anchor) {
  const url = location.origin + location.pathname + '#boards/' + c.board + '/' + c.id;
  try { await navigator.clipboard.writeText(url); toast('Link copied'); }
  catch (e) { bdPop('clink', anchor, 'Link to this card', () => `<div class="fld"><label for="cl-url">Copy this link</label><input class="in" id="cl-url" readonly value="${esc(url)}" data-autofocus></div>`); setTimeout(() => $('cl-url')?.select(), 20); }
}

/* ============================== keys on a card that is not open ============================== */
// The core's pickers (labels, members, dates, cover) for a card on the board.
function buPickFor(c, act, anchor) { if (!BD.card) BD.cd = c; A[act]('', anchor || buCardEl(c.id)); }
async function buLabelKey(c, k) {
  const n = k === '0' ? 9 : +k - 1, l = (BD.v.board.labels || [])[n];
  if (!l) { toast(`This board has no label ${n + 1}`); return; }
  const on = !(c.labels || []).includes(l.id), ls = (c.labels || []).filter(x => x !== l.id && bdLabel(x));
  if (on) ls.push(l.id);
  await bdPatch({ labels: ls }, c.id);
}
async function buToggleKey(c, what) {
  const me = bdMe(), on = !((what === 'watch' ? c.watchers : c.votes) || []).includes(me);
  try {
    bdGot(await bdPost(bdCURL(c.id, '/toggle'), { what, on }));
    toast(what === 'watch' ? (on ? `Watching #${c.no}` : `Stopped watching #${c.no}`) : (on ? `Voted for #${c.no}` : `Took back your vote on #${c.no}`));
  } catch (e) { toast(e.message, true); }
}
async function buArchiveKey(c) {
  if (buLinked(c)) { toast(`#${c.no} follows its Ledgerline record, so it is not archived here`); return; }
  const cs = bdIn(c.list, BOARDS.ctx().cards), i = cs.findIndex(x => x.id === c.id), next = cs[i + 1] || cs[i - 1], was = BU.sel === c.id;
  if (await bdPatch({ archived: true }, c.id) && was && next) buSelect(next.id);
}
function buTitleKey(c) {
  bdOpenCard(c.id);
  if (bdCan() && !c.mirror) setTimeout(() => { if (BD.card === c.id && !BD.cw.title) A.bdTitleEdit(); }, 40);
}
// Ctrl/Cmd+C copies the selected card; Ctrl/Cmd+V pastes a copy into a list here.
function buCopyCard(c) {
  BU.clip = { board: c.board, card: c.id, no: c.no, title: c.title };
  try { navigator.clipboard?.writeText(location.origin + location.pathname + '#boards/' + c.board + '/' + c.id).catch(() => {}); } catch (e) {}
  toast(`Copied #${c.no}. ${buMod()}+V on a list pastes a copy`);
}
async function buPasteCard() {
  const clip = BU.clip; if (!clip || !BD.v) return;
  if (BD.v.board.kind === 'ledgerline') { toast('Cards can’t be pasted onto a Ledgerline board: its cards are Ledgerline’s own records'); return; }
  if (!bdCan()) { toast('You can see this board but not add cards to it'); return; }
  const sel = buCardEl(BU.sel) ? buCard(BU.sel) : null;
  const l = bdLists().find(x => x.id === (sel ? sel.list : BD.hover));
  if (!l) { toast('Point at a list, or select a card, then paste'); return; }
  let pos;
  if (sel) { const cs = bdIn(l.id, bdOpenCards()), i = cs.findIndex(x => x.id === sel.id); pos = bdMid(cs[i].pos, cs[i + 1]?.pos); }
  const body = { list: l.id, title: '', keep: ['checklists', 'labels', 'members', 'fields'], ...(pos != null ? { pos } : {}), ...(clip.board !== UI.bdBoard ? { board: UI.bdBoard } : {}) };
  try {
    const c = await bdPost(`/v1/boards/${bdE(clip.board)}/cards/${bdE(clip.card)}/copy`, body);
    if (c.board === UI.bdBoard) { BD.v.cards.push(c); bdPaint(); buSelect(c.id); }
    toast(`Pasted a copy of #${clip.no} as #${c.no}`); bdSoon();
  } catch (e) { toast(e.message, true); }
}

/* ============================== board switcher (b) ============================== */
const buRK = () => 'll-boards-recent:' + bdMe();
function buRecent() { try { const a = JSON.parse(localStorage.getItem(buRK()) || '[]'); return Array.isArray(a) ? a.filter(x => typeof x === 'string').slice(0, 8) : []; } catch (e) { return []; } }
function buRecentAdd(id) { try { localStorage.setItem(buRK(), JSON.stringify([id, ...buRecent().filter(x => x !== id)].slice(0, 8))); } catch (e) {} }
async function buSwitcher() {
  if ($('bu-sw')) return;
  bdPopClose(true);
  BU.sw = { boards: BD.home || BD.boards || null, i: 0, list: [] };
  modal(mhd('Switch board', 'Starred boards first, then the ones you opened lately. Enter opens the board.') + `<div class="bd"><div class="bu-sw" id="bu-sw"><div class="bd-srch bu-sw-q">${bdI('search')}<label class="bd-sr" for="bu-sw-q">Find a board</label><input id="bu-sw-q" type="search" role="combobox" aria-expanded="true" aria-controls="bu-sw-l" aria-autocomplete="list" placeholder="Find a board by name" autocomplete="off"></div><div class="bu-sw-l" id="bu-sw-l" role="listbox" aria-label="Boards"></div></div></div>`);
  buSwPaint();
  try { const bs = await get('/v1/boards'); BD.boards = bs; if (BU.sw) { BU.sw.boards = bs; buSwPaint(); } } catch (e) { toast(e.message, true); }
}
function buSwPaint() {
  const w = BU.sw, el = $('bu-sw-l'); if (!w || !el) return;
  if (!w.boards) { el.innerHTML = '<p class="bd-mute">Loading boards…</p>'; return; }
  const q = ($('bu-sw-q')?.value || '').trim().toLowerCase(), rec = buRecent();
  const open = w.boards.filter(b => !b.closed), byName = (a, b) => a.name.localeCompare(b.name);
  const star = open.filter(b => b.starred).sort(byName);
  const recent = rec.map(id => open.find(b => b.id === id && !b.starred && b.id !== UI.bdBoard)).filter(Boolean);
  const rest = open.filter(b => !b.starred && !recent.includes(b)).sort(byName);
  let groups;
  if (q) {
    const hit = b => b.name.toLowerCase().includes(q);
    groups = [['Boards', [...star, ...recent, ...rest, ...w.boards.filter(b => b.closed).sort(byName)].filter(hit)]];
  } else groups = [['Starred', star], ['Recently viewed', recent], ['All boards', rest]];
  w.list = groups.flatMap(g => g[1]);
  w.i = Math.max(0, Math.min(w.i, w.list.length - 1));
  let n = 0;
  const opt = b => { const i = n++, on = i === w.i; return `<button type="button" role="option" id="bu-sw-o-${i}" class="bu-sw-o${on ? ' on' : ''}" aria-selected="${on}" tabindex="-1" data-act="buSwGo" data-id="${esc(b.id)}"><span class="bu-sw-c bdc-${bdBg(b.background)}" aria-hidden="true"></span><span class="bu-sw-n">${esc(b.name)}</span><span class="bu-sw-m">${[b.id === UI.bdBoard ? 'This board' : '', b.starred ? 'Starred' : '', b.kind === 'ledgerline' ? 'Ledgerline' : '', b.visibility === 'private' ? 'Private' : '', b.closed ? 'Closed' : ''].filter(Boolean).join(' · ')}</span></button>`; };
  el.innerHTML = groups.filter(g => g[1].length).map(([t, bs]) => `<div class="bu-sw-g" role="group" aria-label="${t}"><div class="bu-sw-h" aria-hidden="true">${t}</div>${bs.map(opt).join('')}</div>`).join('') || `<p class="bd-mute">No board matches “${esc(q)}”.</p>`;
  const inp = $('bu-sw-q'); if (inp) { if (w.list.length) inp.setAttribute('aria-activedescendant', 'bu-sw-o-' + w.i); else inp.removeAttribute('aria-activedescendant'); }
  $('bu-sw-o-' + w.i)?.scrollIntoView({ block: 'nearest' });
}
function buSwKey(e, stop) {
  const w = BU.sw; if (!w) return;
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { stop(); if (w.list.length) { w.i = (w.i + (e.key === 'ArrowDown' ? 1 : w.list.length - 1)) % w.list.length; buSwPaint(); } }
  else if (e.key === 'Enter') { stop(); const b = w.list[w.i]; if (b) A.buSwGo(b.id); }
}

/* ============================== the composer: #label @person ^place ============================== */
function buLabelAt(t, i, ls) {
  const rest = t.slice(i), low = rest.toLowerCase();
  for (const l of ls) {
    const names = l.name ? [l.name, l.name.replace(/\s+/g, '_'), l.name.replace(/\s+/g, '-')] : [l.color, BD_CN[l.color] || ''];
    for (const n of names) if (n && low.startsWith(n.toLowerCase()) && /^($|[\s.,;:!?])/.test(rest.slice(n.length))) return { id: l.id, len: n.length };
  }
  return null;
}
function buPerson(tok) {
  const t = tok.toLowerCase(), us = bdUsers();
  const ex = us.find(u => u.id.toLowerCase() === t); if (ex) return ex;
  const xs = us.filter(u => u.id.toLowerCase().startsWith(t) || u.name.toLowerCase() === t || u.name.toLowerCase().split(/\s+/)[0] === t);
  return xs.length === 1 ? xs[0] : null;
}
// "Fix bill #DP billing @farhan ^top" -> the title, labels, members and place.
BOARDS.compose = (text, list) => {
  const raw = String(text || ''), out = {}, labels = [], members = [];
  let t = ' ' + raw + ' ';
  const ls = (BD.v?.board.labels || []).slice().sort((a, b) => (b.name || '').length - (a.name || '').length);
  for (let i = t.indexOf('#'); i >= 0; i = t.indexOf('#', i)) {
    const hit = /\s/.test(t[i - 1] || ' ') ? buLabelAt(t, i + 1, ls) : null;
    if (hit) { if (!labels.includes(hit.id)) labels.push(hit.id); t = t.slice(0, i) + t.slice(i + 1 + hit.len); } else i++;
  }
  t = t.replace(/(\s)@([a-z0-9][a-z0-9._-]*[a-z0-9]|[a-z0-9])(?=[\s.,;:!?]|$)/gi, (all, p, tok) => { const u = buPerson(tok); if (!u) return all; if (!members.includes(u.id)) members.push(u.id); return p; });
  let place = '';
  t = t.replace(/(\s)\^(top|bottom|\d{1,4})(?=\s|$)/gi, (all, p, v) => { place = v.toLowerCase(); return p; });
  const title = t.replace(/\s+/g, ' ').trim();
  out.title = title || raw.trim();
  if (labels.length) out.labels = labels;
  if (members.length) out.members = members;
  if (place === 'top') out.pos = 0;
  else if (/^\d+$/.test(place) && list) {
    const cs = bdIn(list, bdOpenCards()), i = Math.max(0, +place - 1);
    if (i < cs.length) out.pos = bdMid(cs[i - 1]?.pos, cs[i].pos);
  }
  return out;
};
function buCacUpdate(el) {
  const pos = el.selectionStart;
  const m = el.selectionEnd === pos && BD.v ? /(^|\s)([@#^])([^\s@#^]*)$/.exec(el.value.slice(0, pos)) : null;
  if (!m) { buCacClose(); return; }
  const kind = m[2], q = m[3].toLowerCase();
  let opts = [];
  if (kind === '@') opts = bdUsers().filter(u => !q || u.id.toLowerCase().includes(q) || u.name.toLowerCase().includes(q)).slice(0, 6).map(u => ({ v: u.id, label: `${u.name}, @${u.id}`, html: `${bdAv(u.id)}<span class="bd-prow-t">${esc(u.name)}<small>@${esc(u.id)}</small></span>` }));
  else if (kind === '#') {
    const qq = q.replace(/[_-]/g, ' ');
    opts = (BD.v.board.labels || []).filter(l => { const n = (l.name || BD_CN[l.color]).toLowerCase(); return !q || n.includes(qq) || l.color.startsWith(q); }).slice(0, 8)
      .map(l => ({ v: l.name || l.color, label: `Label ${l.name || BD_CN[l.color]}`, html: `<span class="bd-pill wide bdc-${esc(l.color)}">${esc(l.name || BD_CN[l.color])}</span>` }));
  } else {
    const n = bdIn(BD.comp, bdOpenCards()).length;
    opts = [['top', 'Top of the list'], ['bottom', 'Bottom of the list'], ...Array.from({ length: Math.min(n + 1, 9) }, (_, i) => [String(i + 1), `Position ${i + 1}`])]
      .filter(([v, l]) => !q || v.startsWith(q) || l.toLowerCase().includes(q)).slice(0, 8).map(([v, l]) => ({ v, label: l, html: `<span class="bd-prow-t">${esc(l)}<small>^${esc(v)}</small></span>` }));
  }
  if (!opts.length) { buCacClose(); return; }
  BU.cac = { el, kind, start: pos - m[3].length - 1, end: pos, opts, i: 0 };
  let box = $('bu-cac');
  if (!box) {
    box = document.createElement('div'); box.id = 'bu-cac'; box.className = 'bd-ac bu-cac'; box.setAttribute('role', 'listbox');
    box.addEventListener('mousedown', e => e.preventDefault());
    document.body.appendChild(box);
  }
  box.setAttribute('aria-label', kind === '@' ? 'People to add' : kind === '#' ? 'Labels to add' : 'Where the card goes');
  buCacPaint();
  const r = el.getBoundingClientRect();
  box.style.left = Math.max(8, Math.min(r.left, innerWidth - 290)) + 'px';
  box.style.top = Math.max(8, r.bottom + 4 + box.offsetHeight > innerHeight - 8 ? r.top - box.offsetHeight - 4 : r.bottom + 4) + 'px';
}
function buCacPaint() {
  const a = BU.cac, box = $('bu-cac'); if (!a || !box) return;
  box.innerHTML = a.opts.map((o, i) => `<button type="button" role="option" id="bu-cac-${i}" class="${i === a.i ? 'on' : ''}" aria-selected="${i === a.i}" aria-label="${esc(o.label)}" data-act="buCacPick" data-id="${i}" tabindex="-1">${o.html}</button>`).join('');
  box.hidden = false;
  a.el.setAttribute('aria-controls', 'bu-cac'); a.el.setAttribute('aria-activedescendant', 'bu-cac-' + a.i); a.el.setAttribute('aria-expanded', 'true');
}
function buCacPick(i) {
  const a = BU.cac; if (!a || !document.contains(a.el)) { buCacClose(); return; }
  const o = a.opts[i]; if (!o) return;
  const el = a.el, v = el.value, ins = a.kind + o.v + ' ';
  el.value = v.slice(0, a.start) + ins + v.slice(a.end);
  const p = a.start + ins.length; el.focus(); el.setSelectionRange(p, p);
  if (el.dataset.bdd) BD.drafts[el.dataset.bdd] = el.value;
  bdGrow(el); buCacClose();
}
function buCacClose() { const a = BU.cac; BU.cac = null; const box = $('bu-cac'); if (box) box.hidden = true; if (a?.el) { a.el.removeAttribute('aria-activedescendant'); a.el.setAttribute('aria-expanded', 'false'); } }

/* ---------- several pasted lines ---------- */
function buPasteAsk(el, text) {
  const s = el.selectionStart, e = el.selectionEnd, all = el.value.slice(0, s) + text + el.value.slice(e);
  const lines = all.split(/\r?\n/).map(x => x.trim()).filter(Boolean);
  BU.paste = { list: BD.comp, s, e, text, lines: lines.slice(0, 100), more: lines.length > 100 };
  const n = BU.paste.lines.length;
  bdPop('bu-paste', el, 'Paste several lines', () => `<p class="bd-pop-p"><b>Make ${n} cards, one per line?</b>${BU.paste.more ? ` Only the first 100 of the ${lines.length} lines.` : ''}</p>
    <ol class="bu-paste-l">${BU.paste.lines.slice(0, 5).map(x => `<li>${esc(x)}</li>`).join('')}${n > 5 ? `<li class="bd-mute">and ${n - 5} more</li>` : ''}</ol>
    <div class="bd-row"><button class="btn pri sm" data-act="buPasteMany" data-autofocus>Make ${n} cards</button><button class="btn sm" data-act="buPasteOne">Make one card</button></div>`, { live: false });
}
async function buPasteMany() {
  const p = BU.paste; BU.paste = null; bdPopClose(true);
  if (!p || !BD.v) return;
  const list = p.list, made = [];
  BD.drafts.comp = '';
  const ta = $('bd-comp'); if (ta) ta.value = '';
  try {
    for (const line of p.lines) { const c = await bdPost(bdURL('/cards'), { list, ...BOARDS.compose(line, list) }); BD.v.cards.push(c); made.push(c); }
    toast(`Made ${bdPl(made.length, 'card')}`);
  } catch (e) {
    BD.drafts.comp = p.lines.slice(made.length).join('\n');
    toast(`${made.length ? `Made ${bdPl(made.length, 'card')}, then: ` : ''}${e.message}`, true);
  }
  bdPaint();
  const body = document.querySelector(`.bd-lbody[data-list="${CSS.escape(list)}"]`); if (body) body.scrollTop = body.scrollHeight;
  $('bd-comp')?.focus(); bdSoon();
}
function buPasteOne() {
  const p = BU.paste; BU.paste = null; bdPopClose(true);
  const ta = $('bd-comp'); if (!p || !ta) return;
  const one = p.text.split(/\r?\n/).map(x => x.trim()).filter(Boolean).join(' ');
  ta.focus(); ta.setRangeText(one, p.s, p.e, 'end');
  ta.dispatchEvent(new Event('input', { bubbles: true }));
}

/* ---------- files dropped on a list ---------- */
const buFiles = e => [...(e.dataTransfer?.types || [])].includes('Files');
function buDropAt(e) {
  if (!buFiles(e) || S.view !== 'boards' || BD.card || !BD.v || !bdCan() || bdViewId() !== 'board' || document.querySelector('#mroot .ov')) return null;
  const l = e.target.closest?.('#bd-board .bd-list'); if (!l) return null;
  return { list: l, card: e.target.closest('.bd-card') };
}
function buDropMark(d) {
  document.querySelectorAll('.bu-drop,.bu-drop-c').forEach(x => { if (x !== d?.list && x !== d?.card) x.classList.remove('bu-drop', 'bu-drop-c'); });
  if (d?.card) { d.card.classList.add('bu-drop-c'); d.list.classList.remove('bu-drop'); } else if (d) d.list.classList.add('bu-drop');
}
async function buDropFiles(d, files, y) {
  if (d.card) { bdUpload(files, d.card.dataset.card); return; }
  const list = d.list.dataset.list, cs = bdIn(list, bdOpenCards());
  const els = [...d.list.querySelectorAll('.bd-cards > .bd-card')];
  const i = els.findIndex(c => { const r = c.getBoundingClientRect(); return y < r.top + r.height / 2; });
  const at = i < 0 ? -1 : cs.findIndex(x => x.id === els[i].dataset.card);
  const body = { list, title: (files[0].name || 'Attachment').slice(0, 500), ...(at >= 0 ? { pos: bdMid(cs[at - 1]?.pos, cs[at].pos) } : {}) };
  try {
    const c = await bdPost(bdURL('/cards'), body);
    BD.v.cards.push(c); bdPaint();
    toast(`Made #${c.no} for ${bdPl(files.length, 'file')}`);
    await bdUpload(files, c.id);
  } catch (e) { toast(e.message, true); }
}

/* ============================== the shortcuts window ============================== */
function bdKeysModal() {
  const k = (keys, what) => `<tr><td>${keys.map(x => `<kbd class="bd-kbd">${x}</kbd>`).join(' ')}</td><td>${what}</td></tr>`;
  const sec = (t, rows) => `<div><h3 class="bd-kh">${t}</h3><table class="bd-keys"><tbody>${rows.join('')}</tbody></table></div>`;
  const m = buMod();
  modal(mhd('Keyboard shortcuts', 'Shortcuts work when you are not typing in a box.') + `<div class="bd"><div class="bu-keys">
    ${sec('Moving around', [k(['←', '↑', '↓', '→'], 'Select a card: up and down a list, or across lists'), k(['h', 'j', 'k', 'l'], 'The same as the arrows (left, down, up, right)'), k(['Enter'], 'Open the selected card'), k(['b'], 'Switch to another board'), k(['/'], 'Search cards on every board'), k(['w'], 'Show or hide the board menu'), k(['?'], 'Show these shortcuts'), k(['Esc'], 'Close a menu, a window or a composer, or clear the selection')])}
    ${sec('On the board', [k(['n'], 'Add a card to the list under the pointer (opening it if collapsed), or the first open list'), k(['f'], 'Filter cards'), k(['q'], 'Show only cards assigned to me'), k(['x'], 'Clear filters'), k([';'], 'Show or hide label names'), k(['z'], 'Undo your last change'), k(['Shift', 'z'], 'Redo it'), k([m, 'V'], 'Paste a copied card into the list under the pointer, or after the selected card')])}
    ${sec('On the selected or open card', [k(['e'], 'Quick edit (on the board)'), k(['t'], 'Edit the title'), k(['1', '…', '0'], 'Add or remove the board’s first ten labels'), k(['d'], 'Dates'), k(['m'], 'Members'), k(['l'], 'Labels (in an open card)'), k(['s'], 'Watch or stop watching'), k(['v'], 'Vote, or take your vote back'), k(['c'], 'Archive (or restore an open card)'), k(['Space'], 'Join or leave (in an open card)'), k([m, 'C'], 'Copy the card')])}
    ${sec('While typing', [k([m, 'Enter'], 'Save a description or a comment'), k([m, 'B'], 'Bold, in a description'), k([m, 'I'], 'Italic'), k([m, 'K'], 'Link'), k(['#'], 'In a new card’s title: add a label'), k(['@'], 'In a new card’s title: add a member. In a comment: mention someone'), k(['^'], 'In a new card’s title: ^top, ^bottom or ^3 for its place')])}
  </div><p class="dim small" style="margin:0">Drag cards and lists with the mouse; on a phone or tablet, press and hold a card to pick it up. A card’s ⋯, or a right-click on it, opens quick edit, which also moves and copies it.</p></div>`);
  document.querySelector('#mroot .md')?.classList.add('bu-keys-md');
}

/* ============================== actions ============================== */
Object.assign(A, {
  buQeClose: () => buQeClose(),
  buQeSave: async () => {
    const q = BU.qe, ta = $('bu-qe-title'); if (!q || !ta) return;
    const c = buCard(q.id), v = ta.value.replace(/\s*\n\s*/g, ' ').trim();
    if (!c) { buQeClose(); return; }
    if (!v) { ta.focus(); toast('A card needs a title', true); return; }
    if (v === c.title) { buQeClose(); return; }
    if (await bdPatch({ title: v }, q.id)) buQeClose();
  },
  buQeDo: (what, el) => {
    const q = BU.qe, c = q && buCard(q.id); if (!c) return;
    if (what === 'open') { buQeClose(); bdOpenCard(c.id); return; }
    if (what === 'labels') return buPickFor(c, 'bdPickLabels', el);
    if (what === 'members') return buPickFor(c, 'bdPickMembers', el);
    if (what === 'cover') return buPickFor(c, 'bdPickCover', el);
    if (what === 'dates') return buPickFor(c, 'bdPickDates', el);
    if (what === 'move' || what === 'copy') { BD.cd = c; bdMoveOpen(c.id, what, el); return; }
    if (what === 'link') { buCopyLink(c, el); return; }
    if (what === 'mirror') { const x = BOARDS.cardButtons.find(b => b.id === 'bxMirror'); bdPopClose(true); if (x) bdSafe(() => { x.run(c, BOARDS.ctx(), el); }, 'mirror'); return; }
    if (what === 'archive') { bdPopClose(true); bdPatch({ archived: true }, c.id).then(ok => { if (ok) buQeClose(); }); }
  },
  buUndo: id => buUndo(id, false),
  buRedo: id => buUndo(id, true),
  buFmt: (cmd, el) => buFmtGo($(el.dataset.ed), cmd, el),
  buEmojiGo: k => {
    const ed = BU.ed, ta = ed && $(ed.id), ch = BU_EMO[k]; bdPopClose(true);
    if (!ta || !ch) return;
    buEdPut(ta, ed.s, ed.e, ch);
  },
  buCacPick: i => buCacPick(+i),
  buPasteMany: () => buPasteMany(),
  buPasteOne: () => buPasteOne(),
  buSwGo: id => { BU.sw = null; closeModal(); if (id !== UI.bdBoard || !BD.v) BOARDS.openBoard(id); }
});

/* ============================== hooks and events ============================== */
BOARDS.onPaint.push(() => {
  buSnap();
  if (UI.bdBoard && BD.v && BU.lastB !== UI.bdBoard) { BU.lastB = UI.bdBoard; BU.sel = ''; buRecentAdd(UI.bdBoard); }
  if (!UI.bdBoard) BU.lastB = '';
  // Quick edit and the keys' pickers keep BD.cd as the board now has it.
  if (!BD.card && BD.cd && BD.v) BD.cd = buCard(BD.cd.id) || BD.cd;
  buSelMark();
  if (BU.qe) buQePaint();
  if (BU.cac && !document.contains(BU.cac.el)) buCacClose();
});
window.addEventListener('keydown', e => {
  if (S.view !== 'boards' || !S.me || !$('bd-root')) return;
  const t = e.target, typing = /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName) || t.isContentEditable;
  const stop = () => { e.preventDefault(); e.stopImmediatePropagation(); };
  if (BU.cac && t === BU.cac.el) {
    const a = BU.cac;
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { stop(); a.i = (a.i + (e.key === 'ArrowDown' ? 1 : a.opts.length - 1)) % a.opts.length; buCacPaint(); return; }
    if ((e.key === 'Enter' || e.key === 'Tab') && !e.shiftKey) { stop(); buCacPick(a.i); return; }
    if (e.key === 'Escape') { stop(); buCacClose(); return; }
  }
  if (e.key === 'Escape') {
    if (BD.drag) { bdDragCancel(); stop(); return; }
    if (BD.ac) { bdAcClose(); stop(); return; }
    if (BD.pop) { bdPopClose(); stop(); return; }
    if ($('bx-pop')) return; // boardextras closes its own popover
    if (BU.qe) { buQeClose(); stop(); return; }
    if (t.dataset?.esc && A[t.dataset.esc]) { A[t.dataset.esc](t.dataset.id, t, e); stop(); return; }
    if (BD.sresOpen) { BD.sresOpen = false; bdSresPaint(); stop(); return; }
    if (t.id === 'bd-search') { t.blur(); stop(); return; }
    if (typing && t.closest('.bd-cw')) { t.blur(); document.querySelector('#mroot .bd-cw')?.focus({ preventScroll: true }); stop(); return; }
    // Esc in another board file's own popover (on the page, outside the
    // window) is that popover's: the card window stays open.
    if (BD.card && t !== document.body && !t.closest?.('#mroot,#bd-root')) { modalLocked = true; setTimeout(() => { modalLocked = false; }, 0); }
    if (!BD.card && BU.sel && !document.querySelector('#mroot .ov')) { BU.sel = ''; buSelMark(); stop(); }
    return; // the console closes the window
  }
  if (BU.qe) {
    // Quick edit keeps the keyboard inside it.
    if (e.key === 'Tab' && !BD.pop && t.closest?.('#bu-qe')) {
      const fs = [...$('bu-qe').querySelectorAll('textarea,button:not([disabled])')].filter(x => x.offsetParent);
      const i = fs.indexOf(t), j = e.shiftKey ? (i <= 0 ? fs.length - 1 : i - 1) : (i >= fs.length - 1 ? 0 : i + 1);
      if (fs.length) { e.preventDefault(); fs[j].focus(); }
    }
    return;
  }
  if (t.id === 'bu-sw-q') { buSwKey(e, stop); return; }
  if (t.classList?.contains('bu-tb-b') && ['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(e.key)) {
    const bs = [...t.closest('.bu-tb').querySelectorAll('.bu-tb-b')], i = bs.indexOf(t);
    const j = e.key === 'Home' ? 0 : e.key === 'End' ? bs.length - 1 : (i + (e.key === 'ArrowRight' ? 1 : bs.length - 1)) % bs.length;
    bs.forEach((b, n) => b.tabIndex = n === j ? 0 : -1); bs[j].focus(); stop(); return;
  }
  if (typing) {
    if (t.dataset?.bued && (e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey) { const f = { b: 'bold', i: 'italic', k: 'link' }[e.key.toLowerCase()]; if (f) { stop(); buFmtGo(t, f); } }
    return;
  }
  if (e.key === 'w' && BD.pop?.key === 'menu' && !e.ctrlKey && !e.metaKey && !e.altKey) { stop(); bdPopClose(); return; }
  if (e.altKey || BD.drag) return;
  const other = document.querySelector('#mroot .ov') && !document.querySelector('#mroot .bd-cw');
  if (other || (t !== document.body && !t.closest?.('#mroot,#bd-root'))) return;
  const k = e.key;
  if (e.ctrlKey || e.metaKey) {
    if (e.shiftKey || BD.card || !UI.bdBoard || !BD.v || bdViewId() !== 'board' || BD.pop) return;
    if (k.toLowerCase() === 'c') { if (String(window.getSelection?.() || '').trim()) return; const c = buTarget(); if (c) { stop(); buCopyCard(c); } }
    else if (k.toLowerCase() === 'v' && BU.clip) { stop(); buPasteCard(); }
    return;
  }
  if (k === '/') { stop(); bdPopClose(true); closeModal(); const s = $('bd-search'); if (s) { s.focus(); s.select(); } return; }
  if (k === '?') { stop(); bdPopClose(true); bdKeysModal(); return; }
  if (k === 'b') { stop(); buSwitcher(); return; }
  if (BD.card && BD.cd) {
    const c = BD.cd, run = (act, sel) => { const b = document.querySelector(`.bd-cw ${sel}`); if (b && !b.disabled) { stop(); A[act](b.dataset.id, b, e); } };
    if (k === 'd') run('bdPickDates', '#cw-sb-dates');
    else if (k === 'l') run('bdPickLabels', '#cw-sb-labels');
    else if (k === 'm') run('bdPickMembers', '#cw-sb-members');
    else if (k === 'c' && bdCan()) { stop(); c.archived ? A.bdRestoreCard() : A.bdArchiveCard(); }
    else if (k === ' ' && bdCan() && !/^(BUTTON|A|SUMMARY)$/.test(t.tagName)) { stop(); A.bdJoin(); }
    else if (k === 't' && bdCan() && !c.mirror) { stop(); A.bdTitleEdit(); }
    else if (k === 's') { stop(); A.bdWatch(); }
    else if (k === 'v' && !BD.v.board.closed) { stop(); A.bdVote(); }
    else if (/^[0-9]$/.test(k) && bdCan()) { stop(); buLabelKey(c, k); }
    else if (k === 'z' || k === 'Z') { stop(); buUndo('', k === 'Z'); }
    return;
  }
  if (!UI.bdBoard || !BD.v || BD.pop) return;
  const onBoard = bdViewId() === 'board';
  const nav = { ArrowDown: [0, 1], j: [0, 1], ArrowUp: [0, -1], k: [0, -1], ArrowLeft: [-1, 0], h: [-1, 0], ArrowRight: [1, 0], l: [1, 0] }[k];
  if (nav) { if (onBoard && !e.shiftKey && buNav(nav[0], nav[1])) stop(); return; }
  if (k === 'f') { stop(); A.bdFilter(); return; }
  if (k === 'q') { stop(); const me = bdMe(), f = UI.bdF; f.members = f.members.includes(me) ? f.members.filter(x => x !== me) : [...f.members, me]; bdPaint(); toast(f.members.includes(me) ? 'Showing cards assigned to you' : 'Showing everyone’s cards'); return; }
  if (k === 'x') { if (bdFCount()) { stop(); A.bdFClear(); } return; }
  if (k === 'n') {
    // The list under the pointer (expanded first if it is collapsed), else the first open list.
    if (bdCan() && onBoard) {
      const ls = bdLists(), h = ls.find(x => x.id === BD.hover), l = h || ls.find(x => !bdCollapsed(x.id)) || ls[0];
      if (l) { stop(); if (bdCollapsed(l.id)) A.bdListCollapse(l.id); A.bdComp(l.id); }
    }
    return;
  }
  if (k === 'w') { const mb = document.querySelector('#bd-root [data-act="bdMenu"]'); if (mb) { stop(); A.bdMenu('', mb); } return; }
  if (k === ';') { stop(); A.bdLabNames(); return; }
  if (k === 'z' || k === 'Z') { stop(); buUndo('', k === 'Z'); return; }
  const c = buTarget(); if (!c) return;
  if (k === 'Enter') { if (!/^(BUTTON|A|SUMMARY)$/.test(t.tagName)) { stop(); bdOpenCard(c.id); } }
  else if (k === 'e') { stop(); buQeOpen(c.id); }
  else if (k === 't') { stop(); buTitleKey(c); }
  else if (k === 's') { stop(); buToggleKey(c, 'watch'); }
  else if (k === 'v') { stop(); buToggleKey(c, 'vote'); }
  else if (k === 'c' && bdCan()) { stop(); buArchiveKey(c); }
  else if (k === 'd' && bdCan() && !c.mirror) { stop(); buPickFor(c, 'bdPickDates'); }
  else if (k === 'm' && bdCan()) { stop(); buPickFor(c, 'bdPickMembers'); }
  else if (/^[0-9]$/.test(k) && bdCan()) { stop(); buLabelKey(c, k); }
}, true);
document.addEventListener('pointerover', e => { if (S.view === 'boards') BU.hover = e.target.closest?.('#bd-board .bd-card')?.dataset.card || ''; });
document.addEventListener('pointerdown', e => {
  if (BU.sel && !e.target.closest?.('#bu-qe,.bd-pop,.bx-pop')) { BU.sel = ''; buSelMark(); }
  if (BU.cac && e.target !== BU.cac.el && !e.target.closest?.('#bu-cac')) buCacClose();
}, true);
document.addEventListener('mousedown', e => { if (e.target.closest?.('.bu-tb-b')) e.preventDefault(); }); // the box keeps its focus and selection
document.addEventListener('focusin', e => {
  const t = e.target; if (!t.closest || S.view !== 'boards') return;
  const c = t.closest('#bd-board .bd-card');
  if (c && t.matches(':focus-visible')) { if (BU.sel !== c.dataset.card) { BU.sel = c.dataset.card; buSelMark(); } }
  else if (!c && BU.sel && t.closest('#bd-root')) { BU.sel = ''; buSelMark(); }
});
document.addEventListener('focusout', e => { if (BU.cac && e.target === BU.cac.el) setTimeout(() => { if (BU.cac && document.activeElement !== BU.cac.el) buCacClose(); }, 0); });
document.addEventListener('input', e => {
  if (e.target.id === 'bd-comp') buCacUpdate(e.target);
  else if (e.target.id === 'bu-sw-q' && BU.sw) { BU.sw.i = 0; buSwPaint(); }
});
document.addEventListener('contextmenu', e => {
  if (S.view !== 'boards' || BD.drag || e.shiftKey) return;
  const c = e.target.closest?.('#bd-board .bd-card'); if (!c) return;
  e.preventDefault(); buQeOpen(c.dataset.card);
});
document.addEventListener('paste', e => {
  const el = e.target; if (el.id !== 'bd-comp' || S.view !== 'boards') return;
  const text = e.clipboardData?.getData('text/plain') || '';
  if (text.split(/\r?\n/).filter(x => x.trim()).length < 2) return;
  e.preventDefault(); buCacClose(); buPasteAsk(el, text);
});
document.addEventListener('dragover', e => { const d = buDropAt(e); buDropMark(d); if (d) { e.preventDefault(); e.dataTransfer.dropEffect = 'copy'; } });
document.addEventListener('dragleave', e => { if (!e.relatedTarget || !e.relatedTarget.closest?.('#bd-board .bd-list')) buDropMark(null); });
document.addEventListener('drop', e => {
  const d = buDropAt(e); buDropMark(null); if (!d) return;
  e.preventDefault();
  const fs = [...(e.dataTransfer.files || [])]; if (fs.length) buDropFiles(d, fs, e.clientY);
});
let buRaf = 0;
const buFollow = () => { if (BU.qe && !buRaf) buRaf = requestAnimationFrame(() => { buRaf = 0; buQePlace(); }); };
window.addEventListener('scroll', buFollow, true);
window.addEventListener('resize', buFollow);
window.addEventListener('hashchange', () => { if (BU.qe) buQeClose(true); });
// A window opened from quick edit (the board's labels, say) takes over from it.
new MutationObserver(() => { if (BU.qe && document.querySelector('#mroot .ov')) buQeClose(true); }).observe($('mroot'), { childList: true });
