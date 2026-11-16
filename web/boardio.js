'use strict';
/* Ledgerline console: Boards import, export and print, collections,
   recently viewed boards, and a card's Share. Registers into BOARDS
   (boards.js) and uses boardextras.js's popover (bxPop). Server:
   internal/ops/boardio.go and internal/api/boardio.go; a card's email
   address is internal/ops/boardextras.go.
     Boards home   Collections (a filter over the home, and Manage),
                   Import from Trello, Recently viewed
     Board menu    Print and export (print view, CSV, JSON), Collections
     Card window   Share: the card's link and email address, Print, Export JSON
   Actions and UI keys start with bio. */

const bioEnc = encodeURIComponent;
const bioHead = () => !(typeof scopedUser === 'function' && scopedUser()); // head office: branch and partner staff have no collections
const bioPl = (n, w, ws) => `${Number(n).toLocaleString('en-IN')} ${n === 1 ? w : (ws || w + 's')}`;
if (typeof BD_IC === 'object') Object.assign(BD_IC, {
  print: BD_IC.print || '<path d="M7 9V3.8h10V9"/><rect x="3.6" y="9" width="16.8" height="8" rx="2"/><path d="M7 14.5h10v5.7H7z"/><circle class="f" cx="17" cy="11.8" r=".9"/>',
  upload: BD_IC.upload || '<path d="M12 15.5V4.5M7.5 9 12 4.5 16.5 9"/><path d="M4.5 15v3.2a1.8 1.8 0 0 0 1.8 1.8h11.4a1.8 1.8 0 0 0 1.8-1.8V15"/>',
  folder: BD_IC.folder || '<path d="M3.8 7.2a1.7 1.7 0 0 1 1.7-1.7h4.1l2 2.2h6.9a1.7 1.7 0 0 1 1.7 1.7v8.4a1.7 1.7 0 0 1-1.7 1.7H5.5a1.7 1.7 0 0 1-1.7-1.7z"/>',
  share: BD_IC.share || '<circle cx="17.5" cy="6" r="2.5"/><circle cx="6.5" cy="12" r="2.5"/><circle cx="17.5" cy="18" r="2.5"/><path d="m8.7 10.8 6.6-3.6M8.7 13.2l6.6 3.6"/>'
});
const bioI = n => (typeof bdI === 'function' ? bdI(n) : '');

/* ============================== recently viewed ============================== */
/* Each opened board is noted on the server (once a minute at most), so the
   list follows the person to another computer. */
const bioSeen = { id: '', at: 0 };
function bioMarkViewed() {
  if (S.view !== 'boards' || !UI.bdBoard || typeof BD === 'undefined' || !BD.v || BD.v.board.id !== UI.bdBoard) return;
  const id = UI.bdBoard;
  if (bioSeen.id === id && Date.now() - bioSeen.at < 60e3) return;
  bioSeen.id = id; bioSeen.at = Date.now();
  api('POST', `/v1/boards/${bioEnc(id)}/viewed`, {}).catch(() => {});
}
if (typeof V === 'object' && V.boards?.load) {
  const load0 = V.boards.load;
  V.boards.load = async (...a) => { const r = await load0(...a); try { bioMarkViewed(); } catch (e) {} return r; };
}

/* ============================== collections ============================== */
const bioCollKey = () => 'bio.coll:' + (S.me?.actor || '');
function bioCollGet() { try { return localStorage.getItem(bioCollKey()) || ''; } catch (e) { return ''; } }
function bioCollSet(id) { UI.bioColl = id || ''; try { if (id) localStorage.setItem(bioCollKey(), id); else localStorage.removeItem(bioCollKey()); } catch (e) {} bioApplyFilter(); }
function bioApplyFilter() {
  const c = (UI.bioColls || []).find(x => x.id === UI.bioColl);
  BOARDS.homeFilter = c ? (b => c.boards.includes(b.id)) : null;
}
async function bioHomeLoad() {
  const [colls, recent] = await Promise.all([bioHead() ? get('/v1/board-collections').catch(() => []) : Promise.resolve([]), get('/v1/boards/recent').catch(() => [])]);
  UI.bioColls = colls || []; UI.bioRecent = recent || [];
  if (UI.bioColl === undefined) UI.bioColl = bioCollGet();
  if (UI.bioColl && !UI.bioColls.some(c => c.id === UI.bioColl)) bioCollSet('');
  bioApplyFilter();
}
function bioHomeBar(home) {
  const all = home?.all || (typeof BD !== 'undefined' ? BD.home : null) || [];
  const live = new Set(all.map(b => b.id));
  const cs = (UI.bioColls || []).map(c => ({ ...c, n: c.boards.filter(id => live.has(id)).length }));
  const cur = cs.find(c => c.id === UI.bioColl);
  const chip = (id, label, n) => `<button class="bio-chip${(UI.bioColl || '') === id ? ' on' : ''}" data-act="bioColl" data-id="${esc(id)}" aria-pressed="${(UI.bioColl || '') === id}">${esc(label)}${n != null ? `<span class="bio-n">${n}</span>` : ''}</button>`;
  const colls = bioHead() ? `<div class="bio-colls" role="group" aria-label="Show the boards in a collection"><span class="bio-lab">${bioI('folder')}Collections</span>
      ${chip('', 'All boards', all.length)}${cs.map(c => chip(c.id, c.name, c.n)).join('')}
      <button class="bio-chip add" data-act="bioCollNew">${bioI('plus')}New collection</button>${cs.length ? '<button class="bd-lnk bio-manage" data-act="bioCollManage">Manage</button>' : ''}</div>` : '<span></span>';
  const note = cur ? `<div class="bio-fnote" role="status">Showing ${bioPl(cur.n, 'board')} in the collection <b>${esc(cur.name)}</b>. <button class="bd-lnk" data-act="bioColl" data-id="">Show all boards</button></div>` : '';
  return `<section class="bio-home" aria-label="Collections and import"><div class="bio-bar">${colls}<button class="btn bio-imp-b" data-act="bioImport">${bioI('upload')}Import from Trello</button></div>${note}</section>`;
}
function bioRecentHTML(home) {
  const all = home?.all || (typeof BD !== 'undefined' ? BD.home : null) || [];
  const by = new Map(all.map(b => [b.id, b]));
  let xs = (UI.bioRecent || []).map(r => by.get(r.id)).filter(b => b && !b.closed);
  if (BOARDS.homeFilter) xs = xs.filter(BOARDS.homeFilter);
  if (!xs.length || typeof bdTile !== 'function') return '';
  return `<section class="bd-sec bio-recent" aria-labelledby="bd-sec-recent"><div class="bd-sec-h"><h2 id="bd-sec-recent">Recently viewed</h2><p>The last boards you opened, on any computer.</p></div><div class="bd-tiles">${xs.map(bdTile).join('')}</div></section>`;
}
BOARDS.homeSections.push(
  { id: 'bioBar', order: 5, load: bioHomeLoad, render: home => bioHomeBar(home) },
  { id: 'bioRecent', order: 15, render: home => bioRecentHTML(home) }
);

function bioCollNewModal(after) {
  modal(mhd('New collection', 'A group of boards on the boards home, for everyone at head office') + `<div class="bd"><div class="fld"><label for="bio-cn">Name</label><input class="in" id="bio-cn" maxlength="60" placeholder="e.g. Month-end, Compliance, Mumbai branch"></div>
    <p class="dim small" style="margin:0">Admins of a board add it to collections from the board menu (<b>Collections</b>). A board can be in several.</p><div id="bio-cn-err"></div></div>
    <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="bio-cn-go">Create collection</button></div>`);
  const go = async () => {
    const b = document.getElementById('bio-cn-go'); b.disabled = true;
    try { const c = await post('/v1/board-collections', { name: document.getElementById('bio-cn').value }); closeModal(); toast(`Collection ${c.name} made`); if (after) after(c); else { UI.bioColls = [...(UI.bioColls || []), c]; BOARDS.reload(); } }
    catch (e) { document.getElementById('bio-cn-err').innerHTML = errHTML(e); b.disabled = false; }
  };
  document.getElementById('bio-cn-go').onclick = go;
  document.getElementById('bio-cn').onkeydown = e => { if (e.key === 'Enter') { e.preventDefault(); go(); } };
}
function bioCollManageBody() {
  const cs = UI.bioColls || [];
  if (!cs.length) return '<div class="bx-empty">No collections yet.</div>';
  return `<ul class="bio-cman">${cs.map(c => `<li><label class="bd-sr" for="bio-cm-${esc(c.id)}">Name of ${esc(c.name)}</label><input class="in" id="bio-cm-${esc(c.id)}" data-biocm="${esc(c.id)}" maxlength="60" value="${esc(c.name)}">
      <span class="bio-cman-n">${bioPl(c.boards.length, 'board')}</span>${c.can_delete ? `<button class="btn sm bad" data-act="bioCollDel" data-id="${esc(c.id)}">Delete</button>` : '<span class="bio-cman-sp" title="Only the person who made it, or an Admin, can delete it"></span>'}</li>`).join('')}</ul>
    <p class="dim small" style="margin:0">Type a new name and press Enter to rename. Deleting a collection leaves its boards as they are; only the person who made it, or an Admin, can.</p>`;
}
function bioCollManage() {
  modal(mhd('Collections', 'Groups of boards on the boards home') + `<div class="bd" id="bio-cman">${bioCollManageBody()}</div><div class="ft"><button class="btn" data-act="bioCollNewM">New collection</button><button class="btn pri" data-act="bioCollDone">Done</button></div>`);
}

/* the board menu: which collections this board is in (board admins) */
async function bioBoardColls(ctx) {
  modal(mhd('Collections', esc(ctx.board.name)) + '<div class="bd" id="bio-bc"><div class="bx-empty">Loading collections…</div></div><div class="ft"><button class="btn pri" data-act="close">Done</button></div>');
  try { UI.bioColls = await get('/v1/board-collections'); } catch (e) { document.getElementById('bio-bc').innerHTML = errHTML(e); return; }
  UI.bioBC = ctx.board.id;
  bioBoardCollsPaint();
}
function bioBoardCollsPaint() {
  const el = document.getElementById('bio-bc'); if (!el) return;
  const id = UI.bioBC, cs = UI.bioColls || [];
  el.innerHTML = `<p class="dim small" style="margin:0">Collections group boards on the boards home for everyone at head office. Tick the ones this board belongs in.</p>
    ${cs.length ? `<ul class="bio-cpick">${cs.map(c => `<li><label class="bx-check"><input type="checkbox" data-biobc="${esc(c.id)}" ${c.boards.includes(id) ? 'checked' : ''}><span><b>${esc(c.name)}</b><small>${bioPl(c.boards.length, 'board')}</small></span></label></li>`).join('')}</ul>` : '<div class="bx-empty">No collections yet. Make the first one below.</div>'}
    <div class="bio-cadd"><label class="bd-sr" for="bio-bc-new">New collection</label><input class="in" id="bio-bc-new" maxlength="60" placeholder="New collection"><button class="btn" data-act="bioBCNew">Create and add</button></div><div id="bio-bc-err"></div>`;
}

/* ============================== import from Trello ============================== */
const BIO_IMPORT_MAX = 20 * 1024 * 1024;
function bioImportOpen() {
  UI.bioImp = { step: 'file' };
  modal(mhd('Import a board from Trello', 'Its lists, cards, checklists, labels, custom fields and comments become a private board') + '<div class="bd" id="bio-imp"></div><div class="ft" id="bio-imp-ft"></div>');
  document.querySelector('.md')?.classList.add('bio-md-wide');
  bioImpPaint();
}
function bioImpPaint() {
  const el = document.getElementById('bio-imp'), ft = document.getElementById('bio-imp-ft'), s = UI.bioImp;
  if (!el || !s) return;
  if (s.step === 'file') {
    el.innerHTML = `<ol class="bio-steps"><li>In Trello, open the board you want to bring over.</li><li>Open the board's menu, then <b>Print, export and share</b> → <b>Export as JSON</b>, and save the file.</li><li>Choose that file here. It can be up to 20 MB.</li></ol>
      <label class="bio-drop${s.busy ? ' busy' : ''}" for="bio-imp-file" id="bio-drop">${bioI('upload')}<b>${s.busy ? esc(s.busy) : 'Choose the Trello export'}</b><small>${s.busy ? 'Reading the lists, cards and members…' : 'or drop the .json file here'}</small></label>
      <input type="file" id="bio-imp-file" class="bx-hide" accept=".json,application/json" ${s.busy ? 'disabled' : ''}>
      <div id="bio-imp-err">${s.err ? errHTML(s.err) : ''}</div>
      <p class="dim small" style="margin:0">Nothing is saved until you press <b>Import board</b>. Attachments come over as links to Trello; the files are not downloaded.</p>`;
    ft.innerHTML = '<button class="btn" data-act="close">Cancel</button>';
    document.getElementById('bio-imp-file').onchange = ev => { const f = ev.target.files[0]; if (f) bioImpRead(f); };
    return;
  }
  const p = s.p, users = p.users || [];
  const tile = (n, label, sub) => `<div class="bio-stat"><b>${Number(n).toLocaleString('en-IN')}</b><span>${label}</span>${sub ? `<small>${sub}</small>` : ''}</div>`;
  const lists = (p.list_preview || []).map(l => `<div class="bio-lcol${l.archived ? ' arch' : ''}"><div class="bio-lname" title="${esc(l.name)}">${esc(l.name)}</div>${l.titles.map(t => `<div class="bio-lcard">${esc(t)}</div>`).join('')}${l.cards > l.titles.length ? `<div class="bio-lmore">+${l.cards - l.titles.length} more</div>` : ''}${l.archived_cards ? `<div class="bio-lmore">${bioPl(l.archived_cards, 'archived card')}</div>` : ''}${!l.cards && !l.archived_cards ? '<div class="bio-lmore">No cards</div>' : ''}${l.archived ? '<span class="bio-ltag">Archived</span>' : ''}</div>`).join('');
  const opt = (m, u) => `<option value="${esc(u.id)}" ${s.map[m.id] === u.id ? 'selected' : ''}>${esc(u.name)} · ${esc(u.role)}</option>`;
  const why = m => s.map[m.id] ? (s.map[m.id] === m.guess ? `Matched by ${esc(m.why || 'name')}` : 'Chosen by you') : 'Left off: their cards keep no member';
  const members = (p.members || []).map(m => `<li class="bio-mrow"><span class="bio-av" aria-hidden="true">${esc(initials(m.name || m.username))}</span>
      <span class="bio-mwho"><b>${esc(m.name || m.username)}</b><small>${m.username ? '@' + esc(m.username) + ' · ' : ''}${esc({ admin: 'Trello admin', observer: 'Trello observer' }[m.type] || 'Trello member')}</small></span>
      <span class="bio-msel"><label class="bd-sr" for="bio-mm-${esc(m.id)}">Ledgerline user for ${esc(m.name || m.username)}</label><select class="in" id="bio-mm-${esc(m.id)}" data-biomm="${esc(m.id)}"><option value="">Leave off</option>${users.map(u => opt(m, u)).join('')}</select><small class="${s.map[m.id] ? '' : 'off'}">${why(m)}</small></span></li>`).join('');
  el.innerHTML = `<div class="bio-imp-from">From <b>${esc(s.file || 'the Trello export')}</b> · <button class="bd-lnk" data-act="bioImpBack">Choose another file</button></div>
    <div class="bio-imp-grid"><div class="bio-imp-l">
      <div class="fld"><label for="bio-imp-name">Name of the new board</label><input class="in" id="bio-imp-name" maxlength="120" value="${esc(s.name)}"></div>
      <div class="bx-lab">What comes over</div>
      <div class="bio-stats">${tile(p.lists, 'Lists', p.lists_archived ? `${p.lists_archived} archived` : '')}${tile(p.cards, 'Cards', p.cards_archived ? `${p.cards_archived} archived` : '')}${tile(p.checklists, 'Checklists', bioPl(p.items, 'item'))}${tile(p.labels, 'Labels')}${tile(p.fields, 'Custom fields')}${tile(p.comments, 'Comments')}${tile(p.attachments, 'Attachments', 'as links')}${tile((p.members || []).length, 'Members')}</div>
      <div class="bx-lab">Lists</div><div class="bio-lists">${lists || '<div class="bx-empty">No lists.</div>'}</div>
      ${(p.notes || []).length ? `<div class="bx-lab">Good to know</div><ul class="bio-notes">${p.notes.map(n => `<li>${esc(n)}</li>`).join('')}</ul>` : ''}
    </div><div class="bio-imp-r">
      <div class="bx-lab">Who is who</div>
      <p class="dim small" style="margin:0 0 8px">Choose the Ledgerline user for each Trello member, or leave them off. The people you choose join the board and keep their cards and checklist items.</p>
      ${members ? `<ul class="bio-members">${members}</ul>` : '<div class="bx-empty">The export lists no members.</div>'}
    </div></div><div id="bio-imp-err">${s.err ? errHTML(s.err) : ''}</div>`;
  ft.innerHTML = `<button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="bioImpGo" ${s.busy ? 'disabled' : ''}>${s.busy ? 'Importing…' : 'Import board'}</button>`;
}
async function bioImpRead(f) {
  const s = UI.bioImp; if (!s) return;
  s.err = null;
  if (f.size > BIO_IMPORT_MAX) { s.err = new Error(`${f.name} is ${(f.size / 1048576).toFixed(1)} MB; a Trello export can be up to 20 MB.`); bioImpPaint(); return; }
  s.busy = 'Reading ' + f.name; bioImpPaint();
  try {
    const text = await f.text();
    const p = await api('POST', '/v1/board-imports/trello/preview', text, true);
    const map = {};
    (p.members || []).forEach(m => { if (m.guess) map[m.id] = m.guess; });
    UI.bioImp = { step: 'map', text, file: f.name, p, map, name: p.name };
  } catch (e) { s.err = e; s.busy = ''; }
  bioImpPaint();
}
async function bioImpGo() {
  const s = UI.bioImp; if (!s || s.step !== 'map' || s.busy) return;
  s.name = document.getElementById('bio-imp-name')?.value.trim() || s.p.name;
  s.busy = 'import'; s.err = null; bioImpPaint();
  // The export goes as it is, inside the request: it is never parsed here.
  const env = '{"name":' + JSON.stringify(s.name) + ',"members":' + JSON.stringify(s.map) + ',"trello":' + s.text + '}';
  try {
    const r = await api('POST', '/v1/board-imports/trello', env, true);
    UI.bioImp = null; closeModal();
    toast(`Imported ${bioPl(r.cards, 'card')} from Trello into ${r.board.name}`);
    BOARDS.openBoard(r.board.id);
  } catch (e) { s.busy = ''; s.err = e; bioImpPaint(); }
}
document.addEventListener('dragover', e => { const d = e.target.closest?.('#bio-drop'); if (d) { e.preventDefault(); d.classList.add('over'); } });
document.addEventListener('dragleave', e => { const d = e.target.closest?.('#bio-drop'); if (d) d.classList.remove('over'); });
document.addEventListener('drop', e => { const d = e.target.closest?.('#bio-drop'); if (!d) return; e.preventDefault(); d.classList.remove('over'); const f = e.dataTransfer?.files?.[0]; if (f) bioImpRead(f); });

/* ============================== print views ============================== */
function bioPrintShow(title, html, onPrint) {
  bioPrintClose();
  const el = document.createElement('div');
  el.id = 'bio-print'; el.className = 'bio-print'; el.setAttribute('role', 'dialog'); el.setAttribute('aria-modal', 'true'); el.setAttribute('aria-label', title);
  el.innerHTML = `<div class="bio-ptools"><b>${esc(title)}</b><span class="bio-ptools-h">This is how it prints. Your browser's print window can also save it as a PDF.</span><span class="bd-sp"></span><button class="btn pri" data-act="bioPrintGo">${bioI('print')}Print</button><button class="btn" data-act="bioPrintClose">Close</button></div><div class="bio-paper">${html}</div>`;
  document.body.appendChild(el);
  document.body.classList.add('bio-printing');
  bioPrintShow.onPrint = onPrint;
  bioPrintShow.ret = document.activeElement;
  setTimeout(() => el.querySelector('[data-act="bioPrintGo"]')?.focus(), 0);
}
function bioPrintClose() {
  const el = document.getElementById('bio-print'); if (!el) return;
  el.remove(); document.body.classList.remove('bio-printing');
  const r = bioPrintShow.ret; if (r && document.contains(r)) r.focus({ preventScroll: true });
}
document.addEventListener('keydown', e => { if (e.key === 'Escape' && document.getElementById('bio-print')) { e.stopPropagation(); e.preventDefault(); bioPrintClose(); } }, true);

// A description as plain text for a short excerpt: markdown marks taken out.
function bioPlain(t, n) {
  const s = String(t || '').replace(/\[([^\]]+)\]\([^)]+\)/g, '$1').replace(/\*\*|__|`/g, '').replace(/(^|\s)_([^_]+)_/g, '$1$2').replace(/^\s*[-*]\s+/gm, '• ').replace(/\n{2,}/g, '\n').trim();
  return s.length > n ? s.slice(0, n - 1) + '…' : s;
}
// A moment in full (IST), for paper.
const bioAbs = iso => { const t = new Date(iso); return isNaN(t) ? '' : t.toLocaleString('en-GB', { timeZone: 'Asia/Kolkata', day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false }); };
const bioWho = id => (typeof bdName === 'function' ? bdName(id) : id);
const bioDate = s => (typeof bdDateTxt === 'function' ? bdDateTxt(s) : s);
function bioLabelsHTML(ids, b) {
  const ls = (ids || []).map(id => (b.labels || []).find(l => l.id === id)).filter(Boolean);
  return ls.length ? `<div class="bio-plabs">${ls.map(l => `<span class="bio-plab bdc-${esc(String(l.color).split('_')[0])}">${esc(l.name || (typeof BD_CN === 'object' ? BD_CN[l.color] : l.color) || '')}</span>`).join('')}</div>` : '';
}
function bioStamp() {
  const now = new Date().toLocaleString('en-GB', { timeZone: 'Asia/Kolkata', day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false });
  return `Printed ${esc(now)} by ${esc(bioWho(S.me?.actor || ''))}`;
}
function bioCardBits(c, b) {
  const items = (c.checklists || []).flatMap(cl => cl.items || []), done = items.filter(i => i.done).length;
  const bits = [];
  if (c.start) bits.push(`Starts ${esc(bioDate(c.start))}`);
  if (c.due) bits.push(`Due ${esc(bioDate(c.due))}${c.due_done ? ' (complete)' : ''}`);
  if (items.length) bits.push(`Checklist ${done}/${items.length}`);
  if ((c.comments || []).length) bits.push(bioPl(c.comments.length, 'comment'));
  if ((c.attachments || []).length) bits.push(bioPl(c.attachments.length, 'attachment'));
  if ((c.members || []).length) bits.push(esc(c.members.map(bioWho).join(', ')));
  return bits;
}
function bioPrintBoard(ctx) {
  const b = ctx.board, lists = (b.lists || []).filter(l => !l.archived).sort((x, y) => x.pos - y.pos), shown = new Set(lists.map(l => l.id));
  const cards = (ctx.cards || []).filter(c => shown.has(c.list)), every = (ctx.all || []).filter(c => shown.has(c.list));
  const front = (b.fields || []).filter(f => f.on_front);
  const html = `<header class="bio-ph"><div><p class="bio-pkick">Board</p><h1>${esc(b.name)}</h1>${b.description ? `<p class="bio-pdesc">${esc(b.description)}</p>` : ''}</div>
      <p class="bio-pstamp">${bioStamp()}<br>${bioPl(cards.length, 'card')} in ${bioPl(lists.length, 'list')}${ctx.filtering ? ` · filtered: ${cards.length} of ${every.length}` : ''}</p></header>
    <div class="bio-plists">${lists.map(l => {
      const cs = cards.filter(c => c.list === l.id).sort((x, y) => x.pos - y.pos);
      return `<section class="bio-plist"><h2>${esc(l.name)}<span>${cs.length}</span></h2>${cs.map(c => {
        const bits = bioCardBits(c, b), fv = front.map(f => c.fields?.[f.id] ? `<span><i>${esc(f.name)}:</i> ${esc(f.type === 'checkbox' ? 'Yes' : f.type === 'date' ? bioDate(c.fields[f.id]) : c.fields[f.id])}</span>` : '').join('');
        return `<article class="bio-pcard">${bioLabelsHTML(c.labels, b)}<h3><span class="bio-pno">#${c.no}</span> ${esc(c.title)}</h3>${bits.length ? `<p class="bio-pmeta">${bits.join(' · ')}</p>` : ''}${fv ? `<p class="bio-pfv">${fv}</p>` : ''}${c.description ? `<p class="bio-pcdesc">${esc(bioPlain(c.description, 280))}</p>` : ''}</article>`;
      }).join('') || '<p class="bio-pnone">No cards</p>'}</section>`;
    }).join('')}</div>`;
  bioPrintShow('Print view: ' + b.name, html, () => api('POST', `/v1/boards/${bioEnc(b.id)}/printed`, {}).catch(() => {}));
}
function bioPrintCard(card, ctx) {
  const b = ctx?.board || BD.v.board, l = (b.lists || []).find(x => x.id === card.list);
  const dl = [['List', l?.name || ''], ['Members', (card.members || []).map(bioWho).join(', ')], ['Starts', card.start ? bioDate(card.start) : ''], ['Due', card.due ? bioDate(card.due) + (card.due_done ? ' (complete)' : '') : ''],
    ['Added', `${bioAbs(card.created_at)} by ${bioWho(card.created_by)}`]].filter(x => x[1]);
  const fields = (b.fields || []).filter(f => card.fields?.[f.id]);
  const md = t => typeof bdMd === 'function' ? bdMd(t) : `<p>${esc(t)}</p>`;
  const html = `<header class="bio-ph"><div><p class="bio-pkick">${esc(b.name)}</p><h1><span class="bio-pno">#${card.no}</span> ${esc(card.title)}</h1>${bioLabelsHTML(card.labels, b)}</div><p class="bio-pstamp">${bioStamp()}${card.archived ? '<br><b>Archived card</b>' : ''}</p></header>
    <dl class="bio-pdl">${dl.map(([k, v]) => `<dt>${k}</dt><dd>${esc(v)}</dd>`).join('')}</dl>
    ${card.description ? `<section class="bio-psec"><h2>Description</h2><div class="bio-pmd">${md(card.description)}</div></section>` : ''}
    ${fields.length ? `<section class="bio-psec"><h2>Custom fields</h2><dl class="bio-pdl">${fields.map(f => `<dt>${esc(f.name)}</dt><dd>${esc(f.type === 'checkbox' ? 'Yes' : f.type === 'date' ? bioDate(card.fields[f.id]) : card.fields[f.id])}</dd>`).join('')}</dl></section>` : ''}
    ${(card.checklists || []).map(cl => { const its = cl.items || [], d = its.filter(i => i.done).length; return `<section class="bio-psec"><h2>${esc(cl.name)} <span>${d}/${its.length}</span></h2><ul class="bio-pcl">${its.map(i => `<li class="${i.done ? 'done' : ''}"><span class="bio-pbox" aria-hidden="true">${i.done ? '✓' : ''}</span>${esc(i.text)}${i.member ? ` <i>· ${esc(bioWho(i.member))}</i>` : ''}${i.due ? ` <i>· due ${esc(bioDate(i.due))}</i>` : ''}<span class="bd-sr">${i.done ? ' (done)' : ' (not done)'}</span></li>`).join('')}</ul></section>`; }).join('')}
    ${(card.attachments || []).length ? `<section class="bio-psec"><h2>Attachments</h2><ul class="bio-patt">${card.attachments.map(a => `<li>${esc(a.name)}${a.url ? ` <i>${esc(a.url)}</i>` : ''}</li>`).join('')}</ul></section>` : ''}
    ${(card.comments || []).length ? `<section class="bio-psec"><h2>Comments</h2><ol class="bio-pcm">${card.comments.map(cm => `<li><p class="bio-pcm-h"><b>${esc(bioWho(cm.by))}</b> · ${esc(bioAbs(cm.at))}</p><div class="bio-pmd">${md(cm.text)}</div></li>`).join('')}</ol></section>` : ''}`;
  bioPrintShow(`Print view: #${card.no}`, html, () => api('POST', `/v1/boards/${bioEnc(card.board)}/printed`, { card: card.id }).catch(() => {}));
}

/* ============================== export ============================== */
function bioExportModal(ctx) {
  const b = ctx.board, adm = ctx.role === 'admin', mem = ['admin', 'member'].includes(ctx.role);
  const opt = (act, icon, t, d, ok, why) => `<button class="bio-xopt" data-act="${act}" ${ok ? '' : 'disabled'}>${bioI(icon)}<span><b>${t}</b><small>${ok ? d : why}</small></span></button>`;
  modal(mhd('Print and export', esc(b.name)) + `<div class="bd"><div class="bio-xopts">
      ${opt('bioPrintBoardGo', 'print', 'Print', `A print-friendly view of the lists and their cards${ctx.filtering ? ', as the filter shows them' : ''}. Save it as a PDF from the print window.`, true)}
      ${opt('bioCSV', 'desc', 'Export as CSV', 'Every card, archived ones too, one a row: list, labels, members, dates, checklist progress and custom fields. Opens in Excel.', mem, "Only the board's members can export its cards.")}
      ${opt('bioJSON', 'copy', 'Export as JSON', "The whole board in Ledgerline's own format: lists, cards (archived too), checklists, comments, custom fields and activity.", adm, 'Only a board admin can export the whole board.')}
    </div><p class="dim small" style="margin:10px 0 0">Each print and export is noted in the board's activity.</p></div><div class="ft"><button class="btn" data-act="close">Close</button></div>`);
}

/* ============================== a card's Share ============================== */
function bioShare(card, ctx, el) {
  const link = `${location.origin}${location.pathname}#boards/${bioEnc(card.board)}/${bioEnc(card.id)}`;
  const member = ['admin', 'member'].includes(ctx?.role);
  UI.bioShareCard = card;
  const pop = (typeof bxPop === 'function') ? bxPop : null;
  if (!pop) return;
  pop(el, 'Share and more', `<div class="fld"><label for="bio-sh-link">Link to this card</label><div class="bx-url"><input class="in mono" id="bio-sh-link" readonly value="${esc(link)}"><button class="btn sm" data-act="bioCopy" data-id="bio-sh-link">Copy</button></div><span class="bx-help">Only people who can see the board can open it.</span></div>
    ${member && ctx?.board?.kind !== 'ledgerline' ? '<div id="bio-sh-mail" class="bio-sh-mail"><span class="bx-help">Looking up the card\'s email address…</span></div>' : ''}
    <div class="bio-sh-acts"><button class="btn sm" data-act="bioPrintCardGo">${bioI('print')}Print</button><button class="btn sm" data-act="bioCardJSON">${bioI('copy')}Export JSON</button></div>`, 340);
  if (member && ctx?.board?.kind !== 'ledgerline') {
    get(`/v1/boards/${bioEnc(card.board)}/cards/${bioEnc(card.id)}/email`).then(m => {
      const box = document.getElementById('bio-sh-mail'); if (!box) return;
      const url = m.on ? (m.url || location.origin + m.path) : '';
      box.innerHTML = m.on ? `<label for="bio-sh-mail-in">Email for this card</label><div class="bx-url"><input class="in mono" id="bio-sh-mail-in" readonly value="${esc(url)}"><button class="btn sm" data-act="bioCopy" data-id="bio-sh-mail-in">Copy</button></div>
          <span class="bx-help">Mail posted to this address by the firm's mail provider is added to the card as a comment, with its attachments. Anyone who has it can comment, so keep it within the firm.</span>`
        : `<div class="bx-lab">Email for this card</div><p class="bx-pop-note">${esc(m.why || 'This card takes no email.')}</p>`;
    }).catch(e => { const box = document.getElementById('bio-sh-mail'); if (box) box.innerHTML = `<p class="bx-pop-note">${esc(e.message)}</p>`; });
  }
}

BOARDS.boardMenu.push(
  { id: 'bioExport', icon: 'print', label: 'Print and export', order: 75, run: bioExportModal },
  { id: 'bioColls', icon: 'folder', label: 'Collections', order: 76, show: ctx => bioHead() && ctx.role === 'admin', run: bioBoardColls }
);
BOARDS.cardButtons.push({ id: 'bioShare', icon: 'share', label: 'Share', group: 'actions', order: 65, run: bioShare });

/* ============================== events ============================== */
document.addEventListener('change', async e => {
  const el = e.target;
  if (el.dataset?.biomm && UI.bioImp?.map) {
    if (el.value) UI.bioImp.map[el.dataset.biomm] = el.value; else delete UI.bioImp.map[el.dataset.biomm];
    const nm = document.getElementById('bio-imp-name'); if (nm) UI.bioImp.name = nm.value;
    bioImpPaint(); document.getElementById('bio-mm-' + el.dataset.biomm)?.focus();
    return;
  }
  if (el.dataset?.biobc) {
    const id = el.dataset.biobc, on = el.checked;
    try {
      const c = await post(`/v1/board-collections/${bioEnc(id)}/boards`, { board: UI.bioBC, on });
      UI.bioColls = (UI.bioColls || []).map(x => x.id === id ? c : x);
      toast(on ? `Added to ${c.name}` : `Taken out of ${c.name}`);
    } catch (er) { el.checked = !on; const x = document.getElementById('bio-bc-err'); if (x) x.innerHTML = errHTML(er); else toast(er.message, true); }
    return;
  }
  if (el.dataset?.biocm) {
    const id = el.dataset.biocm;
    try {
      const c = await api('PUT', `/v1/board-collections/${bioEnc(id)}`, { name: el.value });
      UI.bioColls = (UI.bioColls || []).map(x => x.id === id ? c : x); el.value = c.name; toast(`Renamed to ${c.name}`);
    } catch (er) { toast(er.message, true); el.value = (UI.bioColls || []).find(x => x.id === id)?.name || el.value; }
  }
});
document.addEventListener('keydown', e => { if (e.key === 'Enter' && e.target.dataset?.biocm) { e.preventDefault(); e.target.blur(); } });

Object.assign(A, {
  bioColl: id => { bioCollSet(id); BOARDS.reload(); },
  bioCollNew: () => bioCollNewModal(),
  bioCollNewM: () => bioCollNewModal(c => { UI.bioColls = [...(UI.bioColls || []), c].sort((a, b) => a.name.localeCompare(b.name)); bioCollManage(); }),
  bioCollManage: () => bioCollManage(),
  bioCollDone: () => { closeModal(); BOARDS.reload(); },
  bioCollDel: id => {
    const c = (UI.bioColls || []).find(x => x.id === id); if (!c) return;
    confirmModal('Delete collection', esc(c.name), `<p style="margin:0">The collection goes for everyone. Its ${bioPl(c.boards.length, 'board')} stay as they are.</p>`, 'Delete collection', async () => {
      await post(`/v1/board-collections/${bioEnc(id)}/delete`, {});
      UI.bioColls = (UI.bioColls || []).filter(x => x.id !== id); if (UI.bioColl === id) bioCollSet('');
      toast(`Collection ${c.name} deleted`); bioCollManage();
    }, true);
  },
  bioBCNew: async (id, el) => {
    const inp = document.getElementById('bio-bc-new'), name = inp?.value.trim();
    if (!name) { inp?.focus(); return; }
    el.disabled = true;
    try {
      let c = await post('/v1/board-collections', { name });
      c = await post(`/v1/board-collections/${bioEnc(c.id)}/boards`, { board: UI.bioBC, on: true });
      UI.bioColls = [...(UI.bioColls || []), c].sort((a, b) => a.name.localeCompare(b.name));
      bioBoardCollsPaint(); toast(`Added to the new collection ${c.name}`);
    } catch (e) { const x = document.getElementById('bio-bc-err'); if (x) x.innerHTML = errHTML(e); el.disabled = false; }
  },
  bioImport: () => bioImportOpen(),
  bioImpBack: () => { UI.bioImp = { step: 'file' }; bioImpPaint(); },
  bioImpGo: () => bioImpGo(),
  bioPrintBoardGo: () => { const ctx = BOARDS.ctx(); closeModal(); if (ctx) bioPrintBoard(ctx); },
  bioPrintGo: () => { try { bioPrintShow.onPrint?.(); } catch (e) {} window.print(); },
  bioPrintClose: () => bioPrintClose(),
  bioCSV: () => { const ctx = BOARDS.ctx(); if (ctx) download(`/v1/boards/${bioEnc(ctx.board.id)}/export?format=csv`, 'board.csv'); },
  bioJSON: () => { const ctx = BOARDS.ctx(); if (ctx) download(`/v1/boards/${bioEnc(ctx.board.id)}/export?format=json`, 'board.json'); },
  bioPrintCardGo: () => { const c = (typeof BD !== 'undefined' && BD.cd) || UI.bioShareCard; if (typeof bxPopClose === 'function') bxPopClose(); if (c) bioPrintCard(c, BOARDS.ctx()); },
  bioCardJSON: () => { const c = (typeof BD !== 'undefined' && BD.cd) || UI.bioShareCard; if (c) download(`/v1/boards/${bioEnc(c.board)}/cards/${bioEnc(c.id)}/export`, `card-${c.no}.json`); },
  bioCopy: async id => { const el = document.getElementById(id); if (!el) return; try { await navigator.clipboard.writeText(el.value); toast('Copied'); } catch (e) { el.select(); toast('Select it and copy'); } },
});
