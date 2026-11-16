'use strict';
/* Ledgerline console: Boards templates and extras. Registers into BOARDS
   (boards.js); server: internal/ops/boardextras.go, internal/api/boardextras.go.
     Boards home      the template gallery (Ledgerline's and the firm's own)
     Board menu       Make template / Stop being a template, Custom fields,
                      Change background, Email-to-board
     Card window      Make template, Mirror to another board, Stickers,
                      Summarise this card and Suggest a checklist (AI, shown
                      only while the AI gate allows them); sections for the
                      mirror's original, custom fields, stickers and the AI's
                      reply (covers and votes are the core's, in boards.js)
     Card fronts      Template, Mirror and "on front" field badges; up to
                      three stickers on the cover area
     List composer    Create from template (the board's card templates)
   Import, export, print, collections, recently viewed and the card's Share
   (with its email address) are in boardio.js.
   Actions and UI keys start with bx. */

const BX_COLORS = { green: '#2E9E6A', yellow: '#D9A400', orange: '#E07B24', red: '#D14343', purple: '#7A5AF8', blue: '#2F45C8', sky: '#2E90D1', lime: '#6E9F2B', pink: '#D6489B', black: '#344054' };
const BX_COLOR_NAMES = { green: 'Green', yellow: 'Yellow', orange: 'Orange', red: 'Red', purple: 'Purple', blue: 'Blue', sky: 'Sky', lime: 'Lime', pink: 'Pink', black: 'Black' };
const BX_TYPES = [['text', 'Text'], ['number', 'Number'], ['date', 'Date'], ['checkbox', 'Checkbox'], ['list', 'List of options']];
const bxSvg = d => `<svg class="bx-i" viewBox="0 0 16 16" width="14" height="14" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">${d}</svg>`;
const BX_ICON = {
  mirror: bxSvg('<rect x="2" y="4.5" width="8" height="9" rx="1.5"/><path d="M6 2.5h6.5A1.5 1.5 0 0 1 14 4v7"/>'),
  check: bxSvg('<path d="m3.5 8.5 3 3 6-7"/>'),
  up: bxSvg('<path d="m4 10 4-4 4 4"/>'),
  down: bxSvg('<path d="m4 6 4 4 4-4"/>'),
  tpl: bxSvg('<rect x="2.5" y="2.5" width="11" height="11" rx="2"/><path d="M5.5 6h5M5.5 8.5h5M5.5 11h3"/>'),
  mail: bxSvg('<rect x="2" y="3.5" width="12" height="9" rx="1.5"/><path d="m2.5 4.5 5.5 4 5.5-4"/>'),
  open: bxSvg('<path d="M9 2.5h4.5V7M13.5 2.5 7.5 8.5M12 9.5V13a.5.5 0 0 1-.5.5h-8A.5.5 0 0 1 3 13V5a.5.5 0 0 1 .5-.5H7"/>'),
};

UI.bxGalF = UI.bxGalF || 'all';
UI.bxMirrorInfo = UI.bxMirrorInfo || {};

/* ---------- helpers ---------- */
const bxCtx = () => (typeof BOARDS !== 'undefined' && BOARDS.ctx && BOARDS.ctx()) || null;
const bxAdmin = ctx => !!ctx && ctx.role === 'admin';
const bxEdit = ctx => !!ctx && ['admin', 'member'].includes(ctx.role) && !ctx.board.closed;
const bxEnc = encodeURIComponent;
const bxMe = () => S.me?.actor || '';
const bxName = (id, ctx) => (ctx?.users || []).find(u => u.id === id)?.name || (typeof meUser === 'function' && meUser()?.id === id ? meUser().name : '') || id;
const bxColor = c => BX_COLORS[c] || BX_COLORS.blue;
// Labels have 30 colours (green_subtle, green, green_bold and so on): the core names and colours them.
const bxLabelHex = c => (typeof BOARDS.labelHex === 'function' ? BOARDS.labelHex(c) : bxColor(String(c).split('_')[0]));
const bxLabelName = c => (typeof BOARDS.labelName === 'function' ? BOARDS.labelName(c) : BX_COLOR_NAMES[c] || c);
const bxPlural = (n, w) => `${n} ${w}${n === 1 ? '' : 's'}`;
// Board changes go through PUT; the patch carries only what changes.
const bxPut = (ctx, patch) => api('PUT', `/v1/boards/${bxEnc(ctx.board.id)}`, { ...patch });
const bxPatchCard = (card, patch) => api('PATCH', `/v1/boards/${bxEnc(card.board)}/cards/${bxEnc(card.id)}`, patch);
async function bxRefresh(card) {
  try { await Promise.resolve(BOARDS.reload()); } catch (e) {}
  if (card) BOARDS.openCard(card.board, card.id);
}
function bxFieldText(f, v) {
  if (f.type === 'date') return typeof fmtDate === 'function' ? fmtDate(v) : v;
  if (f.type === 'number' && /^-?\d+(\.\d+)?$/.test(v)) { const d = (v.split('.')[1] || '').length; return (+v).toLocaleString('en-IN', { minimumFractionDigits: d, maximumFractionDigits: d }); }
  return v;
}

/* A small popover anchored to a button, above the card window. It is placed
   again whenever its content changes (content that loads later, such as the
   mirror's boards, can make it taller), so it always stays on the screen:
   below the button if it fits, else above, else on the roomier side with
   its body scrolling. */
function bxPop(anchor, title, body, w = 304) {
  bxPopClose();
  const p = document.createElement('div');
  p.className = 'bx-pop'; p.id = 'bx-pop'; p.setAttribute('role', 'dialog'); p.setAttribute('aria-label', title);
  p.innerHTML = `<div class="bx-pop-hd"><b>${esc(title)}</b><button class="bx-x" data-act="bxPopClose" aria-label="Close">×</button></div><div class="bx-pop-bd" id="bx-pop-bd">${body}</div>`;
  document.body.appendChild(p);
  bxPop.at = { anchor, w };
  bxPopPlace();
  bxPop.mo?.disconnect();
  bxPop.mo = new MutationObserver(() => bxPopPlace());
  bxPop.mo.observe(p.querySelector('.bx-pop-bd'), { childList: true, subtree: true, characterData: true });
  if (anchor && anchor.setAttribute) anchor.setAttribute('aria-expanded', 'true');
  return p;
}
function bxPopPlace() {
  const p = document.getElementById('bx-pop'), at = bxPop.at || {};
  if (!p) return;
  const a = at.anchor, w = at.w || 304;
  const r = a && a.getBoundingClientRect && document.contains(a) ? a.getBoundingClientRect() : { left: innerWidth / 2 - w / 2, top: 96, bottom: 96 };
  p.style.width = Math.min(w, innerWidth - 16) + 'px';
  p.style.maxHeight = '';
  const h = p.offsetHeight, below = innerHeight - r.bottom - 14, above = r.top - 14;
  let top;
  if (h <= below) top = r.bottom + 6;
  else if (h <= above) top = r.top - h - 6;
  else if (below >= above) { p.style.maxHeight = Math.max(160, below) + 'px'; top = r.bottom + 6; }
  else { p.style.maxHeight = above + 'px'; top = r.top - Math.min(h, above) - 6; }
  top = Math.min(Math.max(8, top), Math.max(8, innerHeight - p.offsetHeight - 8));
  const left = Math.min(Math.max(8, r.left), Math.max(8, innerWidth - p.offsetWidth - 8));
  p.style.left = left + 'px'; p.style.top = top + 'px';
}
function bxPopClose() { const p = document.getElementById('bx-pop'); bxPop.mo?.disconnect(); bxPop.mo = null; if (p) p.remove(); }
addEventListener('resize', () => bxPopPlace());
document.addEventListener('mousedown', e => { const p = document.getElementById('bx-pop'); if (p && !p.contains(e.target)) p.remove(); });
document.addEventListener('keydown', e => { if (e.key === 'Escape' && document.getElementById('bx-pop')) { e.stopPropagation(); e.preventDefault(); bxPopClose(); } }, true);

/* ============================== template gallery ============================== */
function bxBand(t) {
  const n = Math.max(2, Math.min(5, t.lists.length)), hs = [100, 72, 88, 56, 80];
  return `<div class="bx-tpl-band" style="--bx-c:${bxColor(t.color)}"><div class="bx-tpl-cols">${Array.from({ length: n }, (_, i) => `<i style="height:${hs[i]}%"></i>`).join('')}</div></div>`;
}
function bxTplCard(t) {
  const lists = t.lists.slice(0, 4).map(l => `<span class="bx-chip">${esc(l)}</span>`).join('') + (t.lists.length > 4 ? `<span class="bx-chip more">+${t.lists.length - 4}</span>` : '');
  const meta = [bxPlural(t.lists.length, 'list'), bxPlural(t.cards.length, 'starter card'), t.items ? bxPlural(t.items, 'checklist item') : '', t.fields.length ? bxPlural(t.fields.length, 'custom field') : ''].filter(Boolean).map(m => `<span>${m}</span>`).join('');
  return `<article class="bx-tpl" style="--bx-c:${bxColor(t.color)}">${bxBand(t)}
    <div class="bx-tpl-bd"><div class="bx-tpl-team">${esc(t.builtin ? t.team : 'Your firm')}</div><h3>${esc(t.name)}</h3>${t.description ? `<p>${esc(t.description)}</p>` : '<p class="dim">No description.</p>'}
    <div class="bx-tpl-lists">${lists}</div><div class="bx-tpl-meta">${meta}</div></div>
    <div class="bx-tpl-ft"><span class="bx-tag ${t.builtin ? '' : 'firm'}">${t.builtin ? 'Ledgerline template' : 'Firm template'}</span><button class="btn sm pri" data-act="bxUseTpl" data-id="${esc(t.id)}">Use template</button></div></article>`;
}
function bxGalleryHTML() {
  const g = UI.bxGallery;
  const head = `<div class="bx-gal-hd"><div><h2>Start from a template</h2><p>Boards set up for a broking firm's teams, with lists, labels, custom fields and starter cards with checklists. The firm's own templates appear beside them.</p></div>`;
  if (!g) {
    // Rendered before load() finished (or after a change cleared it): fetch, then redraw in place.
    if (!UI.bxGalleryErr && !bxGalleryHTML.busy) { bxGalleryHTML.busy = true; bxGalleryLoad().then(() => { bxGalleryHTML.busy = false; bxGalleryRedraw(); }); }
    return `<section class="bx-gal" id="bx-gal">${head}</div><div class="bx-empty">${UI.bxGalleryErr ? esc(UI.bxGalleryErr) : 'Loading templates…'}</div></section>`;
  }
  const firm = g.filter(t => !t.builtin).length;
  const shown = g.filter(t => UI.bxGalF === 'all' || (UI.bxGalF === 'firm') === !t.builtin);
  const seg = `<div class="seg" role="group" aria-label="Show templates">${[['all', `All (${g.length})`], ['builtin', 'Ledgerline'], ['firm', `Your firm (${firm})`]].map(([v, l]) => `<button class="${UI.bxGalF === v ? 'on' : ''}" data-act="bxGalF" data-id="${v}" aria-pressed="${UI.bxGalF === v}">${l}</button>`).join('')}</div>`;
  const empty = UI.bxGalF === 'firm' ? 'No firm templates yet. An admin of a team board can make it a template from the board menu: <b>Make template</b>.' : 'No templates.';
  return `<section class="bx-gal" id="bx-gal">${head}${seg}</div>${shown.length ? `<div class="bx-gal-grid">${shown.map(bxTplCard).join('')}</div>` : `<div class="bx-empty">${empty}</div>`}</section>`;
}
async function bxGalleryLoad() {
  try { UI.bxGallery = await get('/v1/board-templates'); UI.bxGalleryErr = ''; } catch (e) { UI.bxGallery = null; UI.bxGalleryErr = e.message; }
}
function bxGalleryRedraw() { const el = document.getElementById('bx-gal'); if (el) el.outerHTML = bxGalleryHTML(); }

function bxUseModal(id) {
  const t = (UI.bxGallery || []).find(x => x.id === id);
  if (!t) return;
  const scoped = typeof scopedUser === 'function' && scopedUser();
  const pre = `<div class="bx-use-pre" style="--bx-c:${bxColor(t.color)}"><div class="bx-lab">Lists</div><div class="bx-use-lists">${t.lists.map(l => `<div class="bx-use-col"><span>${esc(l)}</span><i></i><i></i></div>`).join('')}</div>
    ${t.labels.length ? `<div class="bx-lab">Labels</div><div class="bx-use-labels">${t.labels.map(l => `<span class="bx-lbl" style="--bx-l:${bxLabelHex(l.color)}">${esc(l.name || bxLabelName(l.color))}</span>`).join('')}</div>` : ''}
    ${t.fields.length ? `<div class="bx-lab">Custom fields</div><div class="bx-use-labels">${t.fields.map(f => `<span class="bx-chip">${esc(f)}</span>`).join('')}</div>` : ''}
    ${t.cards.length ? `<div class="bx-lab">Starter cards</div><ul class="bx-use-cards">${t.cards.slice(0, 8).map(c => `<li>${esc(c)}</li>`).join('')}${t.cards.length > 8 ? `<li class="dim">and ${t.cards.length - 8} more</li>` : ''}</ul>` : ''}</div>`;
  modal(mhd('Create a board from a template', esc(t.name) + (t.builtin ? ' · Ledgerline template' : ' · Firm template')) + `<div class="bd"><div class="bx-use">
    <div class="form bx-use-form">
      <div class="fld full"><label for="bx-use-name">Board name</label><input class="in" id="bx-use-name" maxlength="120" value="${esc(t.name)}"></div>
      <div class="fld full"><label for="bx-use-vis">Who can see it</label><select class="in" id="bx-use-vis" ${scoped ? 'disabled' : ''}><option value="firm" ${scoped ? '' : 'selected'}>Everyone at head office (firm-wide)</option><option value="private" ${scoped ? 'selected' : ''}>Only the board's members (private)</option></select>${scoped ? '<span class="dim small">Branch and partner staff keep their boards private.</span>' : ''}</div>
      <label class="bx-check full"><input type="checkbox" id="bx-use-cards" ${t.cards.length ? 'checked' : 'disabled'}><span><b>Keep the starter cards</b><small>${t.cards.length ? `${bxPlural(t.cards.length, 'card')}${t.items ? ` with ${bxPlural(t.items, 'checklist item')}` : ''}${t.templates ? `, ${bxPlural(t.templates, 'card template')}` : ''}` : 'This template has no cards.'}</small></span></label>
      <div id="bx-use-err" class="full"></div>
    </div>${pre}</div></div>
    <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="bx-use-go">Create board</button></div>`);
  document.querySelector('.md')?.classList.add('bx-md-wide');
  document.getElementById('bx-use-go').onclick = async ev => {
    const btn = ev.currentTarget; btn.disabled = true;
    try {
      const b = await post(`/v1/board-templates/${bxEnc(t.id)}/use`, { name: document.getElementById('bx-use-name').value.trim(), visibility: document.getElementById('bx-use-vis').value, cards: document.getElementById('bx-use-cards').checked });
      closeModal(); toast(`${b.name} is ready`); BOARDS.openBoard(b.id);
    } catch (e) { document.getElementById('bx-use-err').innerHTML = errHTML(e); btn.disabled = false; }
  };
}

BOARDS.homeSections.push({ id: 'bxTemplates', order: 50, load: bxGalleryLoad, render: () => bxGalleryHTML() });

/* ============================== board menu ============================== */
function bxSetTemplate(ctx, on) {
  if (!on) { act(() => bxPut(ctx, { template: false }).then(r => { UI.bxGallery = null; BOARDS.reload(); return r; }), `${ctx.board.name} is no longer a template`, false); return; }
  confirmModal('Make this board a template', esc(ctx.board.name), `<div class="note">It joins the template gallery on the boards home for everyone who can see it (${ctx.board.visibility === 'private' ? "only this board's members, while it is private" : 'everyone at head office'}). A board made from it copies its lists, labels and custom fields, and its cards if the person chooses. This board carries on as it is.</div>`, 'Make template', async () => {
    await bxPut(ctx, { template: true }); closeModal(); UI.bxGallery = null; toast(`${ctx.board.name} is now a template`); BOARDS.reload();
  });
}

/* ---- custom fields ---- */
function bxFieldsBody() {
  const fs = UI.bxFields || [];
  const rows = fs.map((f, i) => `<div class="bx-frow" data-i="${i}">
    <div class="bx-fmove"><button class="bx-ico" data-act="bxFieldMove" data-id="${i}" data-d="-1" aria-label="Move ${esc(f.name || 'field')} up" ${i ? '' : 'disabled'}>${BX_ICON.up}</button><button class="bx-ico" data-act="bxFieldMove" data-id="${i}" data-d="1" aria-label="Move ${esc(f.name || 'field')} down" ${i < fs.length - 1 ? '' : 'disabled'}>${BX_ICON.down}</button></div>
    <div class="fld"><label for="bx-fn-${i}">Name</label><input class="in" id="bx-fn-${i}" data-bxf="name" data-i="${i}" maxlength="60" value="${esc(f.name)}" placeholder="e.g. Client code"></div>
    <div class="fld"><label for="bx-ft-${i}">Type</label><select class="in" id="bx-ft-${i}" data-bxf="type" data-i="${i}">${BX_TYPES.map(([v, l]) => `<option value="${v}" ${f.type === v ? 'selected' : ''}>${l}</option>`).join('')}</select></div>
    <div class="fld bx-fopts">${f.type === 'list' ? `<label for="bx-fo-${i}">Options, separated by commas</label><input class="in" id="bx-fo-${i}" data-bxf="options" data-i="${i}" value="${esc((f.options || []).join(', '))}" placeholder="NSE, BSE, MCX">` : ''}</div>
    <label class="bx-check sm"><input type="checkbox" data-bxf="on_front" data-i="${i}" ${f.on_front ? 'checked' : ''}><span>On card front</span></label>
    <button class="btn sm bad" data-act="bxFieldDel" data-id="${i}">Remove</button></div>`).join('');
  return `${fs.length ? `<div class="bx-flist">${rows}</div>` : '<div class="bx-empty">No custom fields yet. Add one for details every card should carry, such as a client code, an amount or a filing date.</div>'}
    <div class="split"><button class="btn" data-act="bxFieldAdd" ${fs.length >= 50 ? 'disabled' : ''}>Add a field</button><span class="dim small">Fields marked <b>On card front</b> show on the card in its list. Removing a field hides its values on every card.</span></div><div id="bx-fl-err"></div>`;
}
function bxFieldsPanel(ctx) {
  UI.bxCtx = ctx;
  UI.bxFields = (ctx.board.fields || []).map(f => ({ id: f.id, name: f.name, type: f.type, options: [...(f.options || [])], on_front: !!f.on_front }));
  modal(mhd('Custom fields', esc(ctx.board.name) + ' · extra details on every card') + `<div class="bd" id="bx-fl">${bxFieldsBody()}</div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="bxFieldsSave">Save fields</button></div>`);
  document.querySelector('.md')?.classList.add('bx-md-wide');
}
const bxFieldsRedraw = () => { const el = document.getElementById('bx-fl'); if (el) el.innerHTML = bxFieldsBody(); };

/* ---- background ---- */
function bxBgPanel(ctx) {
  UI.bxCtx = ctx;
  const cur = ctx.board.background || 'blue', file = cur.startsWith('file:') ? cur.slice(5) : '';
  modal(mhd('Change background', esc(ctx.board.name)) + `<div class="bd">
    <div><div class="bx-lab">Colours</div><div class="bx-bg-grid">${Object.keys(BX_COLORS).map(c => `<button class="bx-bg ${cur === c ? 'on' : ''}" style="--bx-c:${BX_COLORS[c]}" data-act="bxBgColor" data-id="${c}" aria-label="${BX_COLOR_NAMES[c]}" aria-pressed="${cur === c}"><span>${BX_COLOR_NAMES[c]}</span></button>`).join('')}</div></div>
    <div><div class="bx-lab">Image</div><div class="bx-bg-up">${file ? `<img class="bx-bg-now" src="/v1/board-files/${bxEnc(file)}" alt="Current background">` : ''}<label class="btn" for="bx-bg-file">${file ? 'Upload another image' : 'Upload an image'}</label><input type="file" id="bx-bg-file" class="bx-hide" accept="image/png,image/jpeg,image/gif,image/webp"><span class="dim small">JPEG, PNG, GIF or WebP, up to 15 MB. A wide photo of at least 1600 pixels looks best.</span></div></div>
    <label class="bx-check"><input type="checkbox" id="bx-covers" ${ctx.board.covers_on ? 'checked' : ''}><span><b>Card covers</b><small>Show covers on the fronts of cards. An image attached to a card becomes its cover.</small></span></label>
    <div id="bx-bg-err"></div></div><div class="ft"><button class="btn pri" data-act="close">Done</button></div>`);
  document.getElementById('bx-bg-file').onchange = async ev => {
    const f = ev.target.files[0]; if (!f) return;
    try { await api('POST', `/v1/boards/${bxEnc(ctx.board.id)}/background?name=${bxEnc(f.name)}`, f, true); closeModal(); toast('Background changed'); BOARDS.reload(); } catch (e) { document.getElementById('bx-bg-err').innerHTML = errHTML(e); }
  };
  document.getElementById('bx-covers').onchange = async ev => {
    try { await bxPut(ctx, { covers_on: ev.target.checked }); ctx.board.covers_on = ev.target.checked; toast(ev.target.checked ? 'Card covers shown' : 'Card covers hidden'); BOARDS.reload(); } catch (e) { ev.target.checked = !ev.target.checked; document.getElementById('bx-bg-err').innerHTML = errHTML(e); }
  };
}

/* ---- email-to-board ---- */
function bxMailBody(m, ctx) {
  const url = m.on ? (m.url || location.origin + m.path) : '';
  const lists = `<select class="in" id="bx-mail-list" aria-label="List for new cards">${(m.lists || []).map(l => `<option value="${esc(l.id)}" ${l.id === m.list ? 'selected' : ''}>${esc(l.name)}</option>`).join('')}</select>`;
  return `<div class="bx-mail">
    <div class="bx-mail-st">${m.on ? pill('On', 'good') : pill('Off')}<span>${m.on ? 'Emails sent to this board become cards in' : 'When it is on, emails become cards in'}</span>${lists}</div>
    ${m.on ? `<div class="fld"><label for="bx-mail-url">Inbound address</label><div class="bx-url"><input class="in mono" id="bx-mail-url" readonly value="${esc(url)}"><button class="btn sm" data-act="bxMailCopy">Copy</button></div>
      <span class="bx-help">In your mail provider's inbound settings (Amazon SES, SendGrid or Postmark), have mail for this board posted to this address as the raw message.</span></div>` : ''}
    <ul class="bx-mail-how"><li>The subject becomes the card's title and the text its description, with the sender on the first line.</li><li>Attachments are added if they pass the same checks as an upload (photos, PDF, Office files, CSV, text or ZIP, up to 15 MB each); others are listed on the card.</li><li>Each delivery is recorded in the board's activity. A closed board refuses mail.</li></ul>
    ${m.on ? '<div class="note warn">Anyone who has the address can add cards to this board. Keep it between the firm and its mail provider, and give the board a new address if it leaks.</div>' : ''}
    <div id="bx-mail-err"></div></div>`;
}
async function bxEmailPanel(ctx) {
  UI.bxCtx = ctx;
  modal(mhd('Email-to-board', esc(ctx.board.name)) + '<div class="bd" id="bx-mail-bd"><div class="bx-empty">Loading…</div></div><div class="ft" id="bx-mail-ft"></div>');
  try { UI.bxMail = await get(`/v1/boards/${bxEnc(ctx.board.id)}/email`); bxMailDraw(); } catch (e) { document.getElementById('bx-mail-bd').innerHTML = errHTML(e); }
}
function bxMailDraw() {
  const m = UI.bxMail, bd = document.getElementById('bx-mail-bd'), ft = document.getElementById('bx-mail-ft');
  if (!bd || !m) return;
  bd.innerHTML = bxMailBody(m, UI.bxCtx);
  ft.innerHTML = m.on ? '<button class="btn bad" data-act="bxMailOff">Switch off</button><button class="btn" data-act="bxMailRotate">Give it a new address</button><button class="btn pri" data-act="close">Done</button>'
    : '<button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="bxMailOn">Switch on</button>';
}
async function bxMailSet(body, msg) {
  const ctx = UI.bxCtx;
  try { UI.bxMail = await post(`/v1/boards/${bxEnc(ctx.board.id)}/email`, body); bxMailDraw(); if (msg) toast(msg); } catch (e) { const el = document.getElementById('bx-mail-err'); if (el) el.innerHTML = errHTML(e); else toast(e.message, true); }
}

BOARDS.boardMenu.push(
  { id: 'bxBackground', icon: 'image', label: 'Change background', order: 30, show: ctx => bxAdmin(ctx), run: bxBgPanel },
  { id: 'bxFields', icon: 'desc', label: 'Custom fields', order: 40, show: ctx => bxAdmin(ctx), run: bxFieldsPanel },
  { id: 'bxMakeTpl', icon: 'copy', label: 'Make template', order: 60, show: ctx => bxAdmin(ctx) && ctx.board.kind === 'team' && !ctx.board.template, run: ctx => bxSetTemplate(ctx, true) },
  { id: 'bxStopTpl', icon: 'copy', label: 'Stop being a template', order: 60, show: ctx => bxAdmin(ctx) && !!ctx.board.template, run: ctx => bxSetTemplate(ctx, false) },
  { id: 'bxEmail', icon: 'comment', label: 'Email-to-board', order: 70, show: ctx => bxAdmin(ctx) && ctx.board.kind === 'team', run: bxEmailPanel }
);

/* ============================== card window ============================== */
/* ---- mirror ---- */
function bxMirrorPop(card, ctx, el) {
  UI.bxCard = card; UI.bxCtx = ctx;
  bxPop(el, 'Mirror to another board', '<div class="bx-empty">Loading boards…</div>', 320);
  get(`/v1/boards/${bxEnc(card.board)}/mirror-targets`).then(ts => {
    UI.bxMirrorTargets = ts;
    const bd = document.getElementById('bx-pop-bd'); if (!bd) return;
    if (!ts.length) { bd.innerHTML = '<p class="bx-pop-note">There is no other open team board where you can add cards. Ask a board admin to make you a member of the board you want.</p>'; return; }
    bd.innerHTML = `<p class="bx-pop-note">A mirror shows this card's title, description and dates on another board and keeps them up to date. Changes made on the mirror are made on this card.</p>
      <div class="fld"><label for="bx-mi-b">Board</label><select class="in" id="bx-mi-b">${ts.map(t => `<option value="${esc(t.id)}">${esc(t.name)}</option>`).join('')}</select></div>
      <div class="fld"><label for="bx-mi-l">List</label><select class="in" id="bx-mi-l">${bxMirrorLists(ts[0])}</select></div>
      <div id="bx-mi-err"></div><button class="btn pri bx-wide" data-act="bxMirrorGo">Create mirror</button>`;
  }).catch(e => { const bd = document.getElementById('bx-pop-bd'); if (bd) bd.innerHTML = errHTML(e); });
}
const bxMirrorLists = t => (t?.lists || []).map(l => `<option value="${esc(l.id)}">${esc(l.name)}</option>`).join('');

function bxMirrorInfoHTML(card, m) {
  const hd = `<div class="bx-mirror-hd">${BX_ICON.mirror}<b>Mirror</b></div>`;
  if (!m) return `${hd}<div class="dim small">Loading the original card…</div>`;
  if (m.error) return `${hd}<div class="errbox">${esc(m.error)}</div>`;
  if (!m.visible) return `${hd}<p>This mirrors a card on a board you cannot see, so nothing of it is shown here.</p>`;
  if (m.gone) return `${hd}<p>The original card on <b>${esc(m.board_name)}</b> no longer exists.</p>`;
  const due = (m.due || '').split('T');
  const head = `${hd}<p class="bx-mirror-of">Mirror of <b>#${m.no} ${esc(m.title)}</b> on <b>${esc(m.board_name)}</b>${m.list ? ` <span class="dim">· ${esc(m.list)}</span>` : ''}${m.archived ? ' <span class="pill warn">Archived</span>' : ''}
    <button class="bx-link" data-act="bxMirrorOpen" data-b="${esc(m.board)}" data-c="${esc(m.card)}">${BX_ICON.open}Open original</button></p>
    ${m.hidden ? `<div class="note">Not everyone who can see this board can see ${esc(m.board_name)}, so on this board the mirror shows only that it is a mirror.</div>` : ''}`;
  if (!m.can_edit) return `${head}<div class="bx-mirror-ro">${m.description ? `<div class="bx-mirror-desc">${esc(m.description)}</div>` : ''}${m.due ? `<div class="small">Due ${esc(typeof fmtDate === 'function' ? fmtDate(m.due) : m.due)}${due[1] ? ' ' + esc(due[1]) : ''}${m.due_done ? ' · complete' : ''}</div>` : ''}</div><div class="note warn">${esc(m.why || 'You cannot change the original card.')}</div>`;
  return `${head}<div class="bx-mirror-form">
    <div class="fld"><label for="bx-mo-t">Title</label><input class="in" id="bx-mo-t" maxlength="500" value="${esc(m.title)}"></div>
    <div class="fld"><label for="bx-mo-d">Description</label><textarea class="in" id="bx-mo-d" rows="3">${esc(m.description || '')}</textarea></div>
    <div class="bx-mirror-due"><div class="fld"><label for="bx-mo-due">Due date</label><input class="in" type="date" id="bx-mo-due" value="${esc(due[0] || '')}"></div><div class="fld"><label for="bx-mo-tm">Time</label><input class="in" type="time" id="bx-mo-tm" value="${esc(due[1] || '')}"></div>
    <label class="bx-check sm"><input type="checkbox" id="bx-mo-done" ${m.due_done ? 'checked' : ''}><span>Complete</span></label></div>
    <div class="split"><button class="btn pri sm" data-act="bxMirrorSave" data-b="${esc(card.board)}" data-c="${esc(card.id)}">Save to the original</button><span class="dim small">Changes are made on the original card and every mirror of it.</span></div><div id="bx-mo-err"></div></div>`;
}
async function bxMirrorLoad(card) {
  try { UI.bxMirrorInfo[card.id] = await get(`/v1/boards/${bxEnc(card.board)}/cards/${bxEnc(card.id)}/mirror`); } catch (e) { UI.bxMirrorInfo[card.id] = { error: e.message }; }
  const el = document.getElementById('bx-mirror-' + card.id);
  if (el) el.innerHTML = bxMirrorInfoHTML(card, UI.bxMirrorInfo[card.id]);
}

/* ---- custom field values ---- */
function bxFieldInput(f, v, card, can) {
  const a = `id="bx-cf-${esc(f.id)}" data-bxcf="${esc(f.id)}" data-b="${esc(card.board)}" data-c="${esc(card.id)}" ${can ? '' : 'disabled'}`;
  switch (f.type) {
    case 'checkbox': return `<label class="bx-check sm"><input type="checkbox" ${a} ${v === 'true' ? 'checked' : ''}><span>${esc(f.name)}</span></label>`;
    case 'list': return `<select class="in" ${a}><option value="">Not set</option>${(f.options || []).map(o => `<option ${o === v ? 'selected' : ''}>${esc(o)}</option>`).join('')}</select>`;
    case 'date': return `<input class="in" type="date" ${a} value="${esc((v || '').slice(0, 10))}">`;
    case 'number': return `<input class="in mono" inputmode="decimal" ${a} value="${esc(v || '')}" placeholder="0">`;
    default: return `<input class="in" maxlength="500" ${a} value="${esc(v || '')}">`;
  }
}
async function bxFieldSave(el) {
  const ctx = bxCtx(), b = el.dataset.b, c = el.dataset.c, fid = el.dataset.bxcf;
  const card = (ctx?.all || ctx?.cards || []).find(x => x.id === c) || (UI.bxCard?.id === c ? UI.bxCard : {});
  const known = new Set((ctx?.board.fields || []).map(f => f.id));
  const vals = {};
  Object.entries(card.fields || {}).forEach(([k, v]) => { if (known.has(k)) vals[k] = v; });
  document.querySelectorAll(`[data-bxcf][data-c="${CSS.escape(c)}"]`).forEach(x => { vals[x.dataset.bxcf] = x.type === 'checkbox' ? (x.checked ? 'true' : '') : x.value.trim(); });
  try { const nc = await api('PATCH', `/v1/boards/${bxEnc(b)}/cards/${bxEnc(c)}`, { fields: vals }); card.fields = nc.fields; el.closest('.fld,.bx-check')?.classList.remove('bx-bad'); BOARDS.reload(); toast(`${(ctx?.board.fields || []).find(f => f.id === fid)?.name || 'Field'} saved`); }
  catch (e) { el.closest('.fld,.bx-check')?.classList.add('bx-bad'); toast(e.message, true); }
}

BOARDS.cardSections.push(
  { id: 'bxTemplateNote', order: 4, render: card => card.template ? `<div class="bx-tplnote"><span class="bx-badge tpl">${BX_ICON.tpl}Template</span><span>This card is a template. Make new cards from it with <b>Create from template</b> at the foot of any list.</span></div>` : '' },
  { id: 'bxMirror', order: 5, render: card => {
    if (!card.mirror) return '';
    setTimeout(() => bxMirrorLoad(card), 0);
    return `<section class="bx-mirror" id="bx-mirror-${esc(card.id)}">${bxMirrorInfoHTML(card, UI.bxMirrorInfo[card.id])}</section>`;
  } },
  { id: 'bxFields', order: 30, render: (card, ctx) => {
    const fs = ctx?.board?.fields || [];
    if (!fs.length) return '';
    const can = bxEdit(ctx);
    return `<section class="bx-sec"><h4 class="bx-sec-h">Custom fields</h4><div class="bx-fgrid">${fs.map(f => f.type === 'checkbox' ? `<div class="fld bx-fcheck">${bxFieldInput(f, card.fields?.[f.id], card, can)}</div>` : `<div class="fld"><label for="bx-cf-${esc(f.id)}">${esc(f.name)}</label>${bxFieldInput(f, card.fields?.[f.id], card, can)}</div>`).join('')}</div></section>`;
  } }
);

BOARDS.cardButtons.push(
  { id: 'bxMirror', icon: 'link', label: 'Mirror to another board', group: 'actions', order: 55, show: (c, ctx) => !!ctx && !c.archived && !c.template, run: bxMirrorPop },
  { id: 'bxTplOn', icon: 'card', label: 'Make template', group: 'actions', order: 60, show: (c, ctx) => bxEdit(ctx) && ctx.board.kind === 'team' && !c.template && !c.mirror, run: c => bxCardTemplate(c, true) },
  { id: 'bxTplOff', icon: 'card', label: 'Not a template', group: 'actions', order: 60, show: (c, ctx) => bxEdit(ctx) && !!c.template, run: c => bxCardTemplate(c, false) }
);
async function bxCardTemplate(card, on) {
  try { const c = await bxPatchCard(card, { template: on }); toast(on ? `#${c.no} is now a card template` : `#${c.no} is no longer a template`); bxRefresh(c); } catch (e) { toast(e.message, true); }
}

/* ============================== card fronts ============================== */
BOARDS.cardBadges.push((c, ctx) => {
  let h = '';
  if (c.template) h += `<span class="bx-badge tpl" title="Card template">${BX_ICON.tpl}Template</span>`;
  if (c.mirror) h += `<span class="bx-badge mir" title="Mirror of a card on another board">${BX_ICON.mirror}Mirror</span>`;
  for (const f of ctx?.board?.fields || []) {
    const v = c.fields?.[f.id];
    if (!f.on_front || !v) continue;
    h += f.type === 'checkbox' ? `<span class="bx-badge val" title="${esc(f.name)}">${BX_ICON.check}${esc(f.name)}</span>` : `<span class="bx-badge val" title="${esc(f.name)}"><span class="bx-fk">${esc(f.name)}</span>${esc(bxFieldText(f, v))}</span>`;
  }
  return h;
});

/* ============================== list composer ============================== */
function bxFromTemplate(listId, ctx) {
  const ts = (ctx?.all || ctx?.cards || []).filter(c => c.template);
  if (!ts.length) { toast('There are no card templates on this board yet. Open a card and choose Make template.'); return; }
  if (!bxEdit(ctx)) { toast('You cannot add cards to this board.'); return; }
  UI.bxCtx = ctx; UI.bxTplPick = ts[0].id; UI.bxTplList = listId;
  const list = (ctx.board.lists || []).find(l => l.id === listId);
  const label = id => (ctx.board.labels || []).find(l => l.id === id);
  modal(mhd('Create a card from a template', list ? 'Into the list ' + esc(list.name) : '') + `<div class="bd">
    <div class="bx-picks" role="radiogroup" aria-label="Card template">${ts.map(c => {
      const items = (c.checklists || []).reduce((n, cl) => n + (cl.items || []).length, 0);
      return `<button class="bx-pick ${c.id === UI.bxTplPick ? 'on' : ''}" role="radio" aria-checked="${c.id === UI.bxTplPick}" data-act="bxTplPick" data-id="${esc(c.id)}"><b>${esc(c.title)}</b>
        <span class="bx-pick-meta">${(c.labels || []).map(id => label(id)).filter(Boolean).map(l => `<span class="bx-lbl sm" style="--bx-l:${bxLabelHex(l.color)}">${esc(l.name || bxLabelName(l.color))}</span>`).join('')}<span class="dim small">${[(c.checklists || []).length ? bxPlural(c.checklists.length, 'checklist') + ` (${bxPlural(items, 'item')})` : '', (c.members || []).length ? bxPlural(c.members.length, 'member') : ''].filter(Boolean).join(' · ') || 'No checklists'}</span></span></button>`;
    }).join('')}</div>
    <div class="fld"><label for="bx-tp-title">Title of the new card</label><input class="in" id="bx-tp-title" maxlength="500" value="${esc(ts[0].title)}"></div>
    <span class="dim small">The new card gets the template's description, labels, members, checklists, custom fields and cover. Dates are not copied.</span><div id="bx-tp-err"></div></div>
    <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="bx-tp-go">Create card</button></div>`);
  document.getElementById('bx-tp-go').onclick = async ev => {
    ev.currentTarget.disabled = true;
    try {
      const c = await post(`/v1/boards/${bxEnc(ctx.board.id)}/cards`, { list: UI.bxTplList, title: document.getElementById('bx-tp-title').value.trim(), from_card: UI.bxTplPick });
      closeModal(); toast(`#${c.no} ${c.title} added`); bxRefresh(c);
    } catch (e) { document.getElementById('bx-tp-err').innerHTML = errHTML(e); ev.currentTarget.disabled = false; }
  };
}
BOARDS.composerButtons.push({ id: 'bxFromTpl', label: 'Create from template', run: bxFromTemplate });

/* ============================== events ============================== */
document.addEventListener('change', e => {
  const el = e.target;
  if (el.dataset?.bxcf) { bxFieldSave(el); return; }
  if (el.dataset?.bxf && UI.bxFields) {
    const f = UI.bxFields[+el.dataset.i]; if (!f) return;
    if (el.dataset.bxf === 'type') { f.type = el.value; if (f.type !== 'list') f.options = []; bxFieldsRedraw(); }
    else if (el.dataset.bxf === 'on_front') f.on_front = el.checked;
    return;
  }
  if (el.id === 'bx-mi-b') { const t = (UI.bxMirrorTargets || []).find(x => x.id === el.value); document.getElementById('bx-mi-l').innerHTML = bxMirrorLists(t); return; }
  if (el.id === 'bx-mail-list' && UI.bxMail) { bxMailSet({ on: UI.bxMail.on, list: el.value }, 'Cards from email go to ' + el.options[el.selectedIndex].text); }
});
document.addEventListener('input', e => {
  const el = e.target;
  if (!el.dataset?.bxf || !UI.bxFields) return;
  const f = UI.bxFields[+el.dataset.i]; if (!f) return;
  if (el.dataset.bxf === 'name') f.name = el.value;
  if (el.dataset.bxf === 'options') f.options = el.value.split(',').map(s => s.trim()).filter(Boolean);
});

Object.assign(A, {
  bxPopClose: () => bxPopClose(),
  bxGalF: id => { UI.bxGalF = id; bxGalleryRedraw(); },
  bxUseTpl: id => bxUseModal(id),
  bxFieldAdd: () => { UI.bxFields.push({ id: '', name: '', type: 'text', options: [], on_front: false }); bxFieldsRedraw(); const i = UI.bxFields.length - 1; setTimeout(() => document.getElementById('bx-fn-' + i)?.focus(), 0); },
  bxFieldDel: id => { UI.bxFields.splice(+id, 1); bxFieldsRedraw(); },
  bxFieldMove: (id, el) => { const i = +id, j = i + (+el.dataset.d), fs = UI.bxFields; if (j < 0 || j >= fs.length) return; [fs[i], fs[j]] = [fs[j], fs[i]]; bxFieldsRedraw(); },
  bxFieldsSave: async (id, el) => {
    const ctx = UI.bxCtx, fs = UI.bxFields.map(f => ({ id: f.id, name: f.name.trim(), type: f.type, options: f.type === 'list' ? f.options : [], on_front: f.on_front }));
    const bad = fs.find(f => !f.name) || fs.find(f => f.type === 'list' && !f.options.length);
    if (bad) { document.getElementById('bx-fl-err').innerHTML = errHTML(new Error(bad.name ? `Give the options of ${bad.name}, separated by commas.` : 'Every field needs a name.')); return; }
    el.disabled = true;
    try { await bxPut(ctx, { fields: fs }); closeModal(); toast('Custom fields saved'); BOARDS.reload(); } catch (e) { document.getElementById('bx-fl-err').innerHTML = errHTML(e); el.disabled = false; }
  },
  bxBgColor: async id => { const ctx = UI.bxCtx; try { await bxPut(ctx, { background: id }); closeModal(); toast(`Background: ${BX_COLOR_NAMES[id]}`); BOARDS.reload(); } catch (e) { document.getElementById('bx-bg-err').innerHTML = errHTML(e); } },
  bxMailOn: () => bxMailSet({ on: true, list: document.getElementById('bx-mail-list')?.value || '' }, 'Email-to-board is on'),
  bxMailOff: () => confirmModal('Switch off email-to-board', esc(UI.bxCtx.board.name), '<div class="note warn">Mail sent to the board\'s address will be refused. Switching it on again gives the board a new address.</div>', 'Switch off', async () => { await post(`/v1/boards/${bxEnc(UI.bxCtx.board.id)}/email`, { on: false }); closeModal(); toast('Email-to-board is off'); }, true),
  bxMailRotate: () => confirmModal('Give the board a new address', esc(UI.bxCtx.board.name), '<div class="note warn">The current address stops working at once. Update the address in your mail provider\'s inbound settings straight after.</div>', 'Give it a new address', async () => { const ctx = UI.bxCtx; closeModal(); await bxEmailPanel(ctx); await bxMailSet({ on: true, rotate: true }, 'The board has a new address'); }),
  bxMailCopy: async () => { const el = document.getElementById('bx-mail-url'); try { await navigator.clipboard.writeText(el.value); toast('Address copied'); } catch (e) { el.select(); toast('Select the address and copy it'); } },
  bxMirrorGo: async (id, el) => {
    const card = UI.bxCard, b = document.getElementById('bx-mi-b').value, l = document.getElementById('bx-mi-l').value;
    el.disabled = true;
    try {
      await post(`/v1/boards/${bxEnc(card.board)}/cards/${bxEnc(card.id)}/mirror`, { board: b, list: l });
      bxPopClose(); toast('Mirror added to ' + ((UI.bxMirrorTargets || []).find(t => t.id === b)?.name || 'the board'));
    } catch (e) { document.getElementById('bx-mi-err').innerHTML = errHTML(e); el.disabled = false; }
  },
  bxMirrorOpen: async (id, el) => { const b = el.dataset.b, c = el.dataset.c; closeModal(); try { await Promise.resolve(BOARDS.openBoard(b)); } catch (e) {} BOARDS.openCard(b, c); },
  bxMirrorSave: async (id, el) => {
    const d = document.getElementById('bx-mo-due').value, tm = document.getElementById('bx-mo-tm').value;
    const body = { title: document.getElementById('bx-mo-t').value, description: document.getElementById('bx-mo-d').value, due: d ? (tm ? `${d}T${tm}` : d) : '', due_done: document.getElementById('bx-mo-done').checked };
    el.disabled = true;
    try {
      UI.bxMirrorInfo[el.dataset.c] = await api('PATCH', `/v1/boards/${bxEnc(el.dataset.b)}/cards/${bxEnc(el.dataset.c)}/mirror`, body);
      toast('Saved on the original card'); bxRefresh({ board: el.dataset.b, id: el.dataset.c });
    } catch (e) { document.getElementById('bx-mo-err').innerHTML = errHTML(e); el.disabled = false; }
  },
  bxTplPick: id => {
    const ctx = UI.bxCtx, t = (ctx?.all || ctx?.cards || []).find(c => c.id === id), prev = (ctx?.all || ctx?.cards || []).find(c => c.id === UI.bxTplPick);
    const ti = document.getElementById('bx-tp-title');
    if (ti && (!ti.value.trim() || ti.value === prev?.title)) ti.value = t?.title || '';
    UI.bxTplPick = id;
    document.querySelectorAll('.bx-pick').forEach(b => { const on = b.dataset.id === id; b.classList.toggle('on', on); b.setAttribute('aria-checked', on); });
  },
});

/* ============================== stickers ============================== */
/* Ledgerline's own sticker set: hand-drawn, inline SVG, no images from
   anywhere else. The server keeps the names (ops.BoardStickers). */
const BX_O = 'stroke="#1D2939" stroke-width="1.8" stroke-linejoin="round" stroke-linecap="round"';
const BX_STK = {
  tick: ['Done', `<circle cx="16" cy="16" r="12.5" fill="#1F9D63" ${BX_O}/><path d="M10 16.4l4.1 4.1 8-9" fill="none" stroke="#fff" stroke-width="3.2" stroke-linecap="round" stroke-linejoin="round"/>`],
  star: ['Star', `<path d="M16 3.4l3.8 7.7 8.4 1.2-6.1 5.9 1.4 8.4L16 22.7l-7.5 3.9 1.4-8.4-6.1-5.9 8.4-1.2z" fill="#F5C518" ${BX_O}/><path d="M11.6 13.6l2.6-.4" stroke="#fff" stroke-width="1.7" stroke-linecap="round"/>`],
  flag: ['Flag', `<path d="M8 4.2v24.6" fill="none" ${BX_O} stroke-width="2.4"/><path d="M8.6 5.6c4.2-2.2 7.2 2.1 11.2-.1 2.8-1.5 4.6-1.2 6 .1v11.2c-1.3-1.2-3.2-1.7-6 0-4 2.3-7-2-11.2.1z" fill="#D64541" ${BX_O}/>`],
  warning: ['Warning', `<path d="M16 4.3 29 27.3H3z" fill="#F5B518" ${BX_O}/><path d="M16 12.2v7" stroke="#1D2939" stroke-width="2.8" stroke-linecap="round"/><circle cx="16" cy="23.1" r="1.7" fill="#1D2939"/>`],
  fire: ['On fire', `<path d="M16.2 3c1 5.2 7.3 7.3 7.3 14.3a7.4 7.4 0 0 1-14.8 0c0-4.1 2-6.3 3.1-8.2.1 3 1.1 4.2 2.2 4.3-1.1-4.2-.1-7.4 2.2-10.4z" fill="#E8770F" ${BX_O}/><path d="M16.1 15.4c.5 3 3.6 3.6 3.6 7.1a3.6 3.6 0 0 1-7.2 0c0-2 1.5-3.1 2.1-4.1.4 1.2.9 1.7 1.5 1.7-.5-1.9-.4-3.3 0-4.7z" fill="#F5C518"/>`],
  rocket: ['Rocket', `<path d="M12.2 14.6 7.6 15l-3.1 3.1 5.1 1zM17.5 19.8l-.4 4.6-3.1 3.1-1-5.1z" fill="#D64541" ${BX_O}/><path d="M20.4 5.4c3.6-1 5.7-1 6.2-.5s.5 2.6-.5 6.2l-9.3 9.3-5.2-5.2z" fill="#fff" ${BX_O}/><circle cx="21" cy="11" r="2.3" fill="#1C8FC4" stroke="#1D2939" stroke-width="1.4"/><path d="M10.6 21.4c-2.1.5-3.6 2.1-4.1 5.1 3-.5 4.6-2 5.1-4.1z" fill="#F5C518" ${BX_O}/>`],
  thumbsup: ['Thumbs up', `<path d="M5.6 14.2h4.4v13H5.6z" fill="#2F45C8" ${BX_O}/><path d="M10 14.2l4.6-8.1c1.5-1.6 3.6-.6 3.5 1.5L17.2 13h7c1.8 0 2.9 1.6 2.4 3.3l-2.6 8.7a3 3 0 0 1-2.9 2.2H10z" fill="#F5C518" ${BX_O}/>`],
  heart: ['Heart', `<path d="M16 27.2S4 19.6 4 11.9A5.9 5.9 0 0 1 16 9.1a5.9 5.9 0 0 1 12 2.8c0 7.7-12 15.3-12 15.3z" fill="#E5484D" ${BX_O}/><path d="M8.9 10.6a3.1 3.1 0 0 1 3-2.1" fill="none" stroke="#fff" stroke-width="1.8" stroke-linecap="round"/>`],
  clock: ['Waiting', `<circle cx="16" cy="16" r="12.5" fill="#1C8FC4" ${BX_O}/><circle cx="16" cy="16" r="9.4" fill="#fff" stroke="#1D2939" stroke-width="1.4"/><path d="M16 10.2v6.3l4 2.6" fill="none" stroke="#1D2939" stroke-width="2.3" stroke-linecap="round" stroke-linejoin="round"/>`],
  pin: ['Pinned', `<path d="M16 19.4v9" stroke="#1D2939" stroke-width="2.3" stroke-linecap="round"/><path d="M11 4h10l-1.6 2.1v5.4c2.6 1.1 4.6 3.1 4.6 6.6H8c0-3.5 2-5.5 4.6-6.6V6.1z" fill="#D64541" ${BX_O}/><path d="M13.5 12.6v3" stroke="#fff" stroke-width="1.6" stroke-linecap="round"/>`],
  rupee: ['Money', `<circle cx="16" cy="16" r="12.5" fill="#F5C518" ${BX_O}/><circle cx="16" cy="16" r="9.5" fill="none" stroke="#B88A00" stroke-width="1.3"/><path d="M11.6 10.4h8.8M11.6 13.9h8.8M13.1 10.4c4.9 0 4.9 7 0 7h-1.5l6.6 7.1" fill="none" stroke="#1D2939" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round"/>`],
  chartup: ['Going up', `<rect x="4" y="5" width="24" height="22" rx="4" fill="#fff" ${BX_O}/><path d="M8.6 21.6l5-5.6 3.6 3 6.4-8M18.9 10.6h4.9v5" fill="none" stroke="#1F9D63" stroke-width="2.7" stroke-linecap="round" stroke-linejoin="round"/>`],
  chartdown: ['Going down', `<rect x="4" y="5" width="24" height="22" rx="4" fill="#fff" ${BX_O}/><path d="M8.6 10.4l5 5.6 3.6-3 6.4 8M18.9 21.4h4.9v-5" fill="none" stroke="#D64541" stroke-width="2.7" stroke-linecap="round" stroke-linejoin="round"/>`],
  bell: ['Reminder', `<path d="M16 3.4v2.2" fill="none" ${BX_O} stroke-width="2.2"/><path d="M12.9 26.2a3.1 3.1 0 0 0 6.2 0" fill="#F5C518" ${BX_O}/><path d="M8 22.4v-8.2a8 8 0 0 1 16 0v8.2l2.1 2.5H5.9z" fill="#F5C518" ${BX_O}/><path d="M11.6 13.2a4.4 4.4 0 0 1 2.5-3.4" fill="none" stroke="#fff" stroke-width="1.7" stroke-linecap="round"/>`],
  lock: ['Locked', `<path d="M10.6 14.2v-4.1a5.4 5.4 0 0 1 10.8 0v4.1" fill="none" ${BX_O} stroke-width="2.5"/><rect x="6.5" y="13.6" width="19" height="14" rx="3.2" fill="#7356D6" ${BX_O}/><circle cx="16" cy="19.4" r="2.1" fill="#fff"/><path d="M16 20.8v3" stroke="#fff" stroke-width="2.1" stroke-linecap="round"/>`],
  bug: ['Bug', `<path d="M10.2 11.2 7 8M21.8 11.2 25 8M8.6 17.4H4.2M23.4 17.4h4.4M9.6 22.6l-3.4 3.5M22.4 22.6l3.4 3.5" fill="none" ${BX_O}/><ellipse cx="16" cy="18.2" rx="7.6" ry="9" fill="#1F9D63" ${BX_O}/><path d="M16 9.8v17.2" stroke="#1D2939" stroke-width="1.6"/><circle cx="16" cy="9.2" r="4" fill="#1D2939"/><circle cx="12.4" cy="16.8" r="1.5" fill="#1D2939"/><circle cx="19.6" cy="21.2" r="1.5" fill="#1D2939"/>`],
  bulb: ['Idea', `<path d="M3.6 12.2h2.2M26.2 12.2h2.2M6.6 4.6l1.6 1.6M25.4 4.6l-1.6 1.6" fill="none" ${BX_O}/><path d="M16 4.4a8 8 0 0 0-4.6 14.6c.9.7 1.6 1.8 1.6 3h6c0-1.2.7-2.3 1.6-3A8 8 0 0 0 16 4.4z" fill="#F5C518" ${BX_O}/><path d="M13 24.4h6M13.9 27.4h4.2" fill="none" ${BX_O}/><path d="M13.3 12.6a3.1 3.1 0 0 1 2.6-2.1" fill="none" stroke="#fff" stroke-width="1.8" stroke-linecap="round"/>`],
  coffee: ['Coffee', `<path d="M11 3.8c-1.2 1.6 1.1 2.6 0 4.4M15.6 3.8c-1.2 1.6 1.1 2.6 0 4.4" fill="none" stroke="#98A2B3" stroke-width="1.7" stroke-linecap="round"/><path d="M22 14h2.1a3.1 3.1 0 0 1 0 6.2H22" fill="none" ${BX_O} stroke-width="2.2"/><path d="M5.8 11.6h16.4v8.6a6.1 6.1 0 0 1-6.1 6.1h-4.2a6.1 6.1 0 0 1-6.1-6.1z" fill="#B07A4F" ${BX_O}/><path d="M8.6 14.6c2 .6 3.8.6 5.6 0" fill="none" stroke="#fff" stroke-width="1.6" stroke-linecap="round" opacity=".7"/>`],
  trophy: ['Win', `<path d="M9.6 7.2H6a3.6 3.6 0 0 0 4.1 4.8M22.4 7.2H26a3.6 3.6 0 0 1-4.1 4.8" fill="none" ${BX_O}/><path d="M16 17.6v4.4" fill="none" ${BX_O} stroke-width="2.6"/><path d="M11 27.4h10l-1-4.6h-8z" fill="#E8770F" ${BX_O}/><path d="M9.5 4.8h13V11a6.5 6.5 0 0 1-13 0z" fill="#F5C518" ${BX_O}/><path d="M13 7.6v3.2" stroke="#fff" stroke-width="1.7" stroke-linecap="round"/>`],
  eyes: ['Looking', `<ellipse cx="10.9" cy="16" rx="5.6" ry="8.2" fill="#fff" ${BX_O}/><ellipse cx="21.1" cy="16" rx="5.6" ry="8.2" fill="#fff" ${BX_O}/><circle cx="12.5" cy="17.6" r="2.7" fill="#1D2939"/><circle cx="22.7" cy="17.6" r="2.7" fill="#1D2939"/><circle cx="13.4" cy="16.7" r=".9" fill="#fff"/><circle cx="23.6" cy="16.7" r=".9" fill="#fff"/>`],
  hundred: ['100', `<g transform="rotate(-8 16 16)"><path d="M5.4 11.4l3-2.5v13.2" fill="none" stroke="#D64541" stroke-width="2.9" stroke-linecap="round" stroke-linejoin="round"/><ellipse cx="15.1" cy="15.5" rx="3.6" ry="6.3" fill="none" stroke="#D64541" stroke-width="2.9"/><ellipse cx="24.5" cy="15.5" rx="3.6" ry="6.3" fill="none" stroke="#D64541" stroke-width="2.9"/><path d="M5 25.6h22M7.4 28.6h17" stroke="#D64541" stroke-width="2.2" stroke-linecap="round"/></g>`],
  party: ['Celebrate', `<path d="M4.8 27.6 10.4 12.4l9.4 9.4z" fill="#7356D6" ${BX_O}/><path d="M8 20.2l4 4M9.6 16l6.6 6.6" stroke="#F5C518" stroke-width="1.9" stroke-linecap="round"/><circle cx="22.2" cy="6.8" r="1.7" fill="#E5484D"/><circle cx="26.6" cy="14" r="1.5" fill="#1C8FC4"/><path d="M16 5.2l1.6 2.6M24 18.6l2.6 1M19.4 11.6c1.6-2.1 3.6-2.1 4.6-4.4" fill="none" stroke="#1F9D63" stroke-width="1.9" stroke-linecap="round"/><rect x="12.4" y="7.8" width="2.8" height="2.8" rx=".6" transform="rotate(20 13.8 9.2)" fill="#F5C518"/>`],
  question: ['Question', `<path d="M5 7.6A3.6 3.6 0 0 1 8.6 4h14.8A3.6 3.6 0 0 1 27 7.6v10.8a3.6 3.6 0 0 1-3.6 3.6H14l-6 5v-5h.6A3.6 3.6 0 0 1 5 18.4z" fill="#2F45C8" ${BX_O}/><path d="M13 10.6a3 3 0 1 1 4.3 2.7c-.9.4-1.3 1-1.3 1.9v.3" fill="none" stroke="#fff" stroke-width="2.4" stroke-linecap="round"/><circle cx="16" cy="18.6" r="1.5" fill="#fff"/>`],
  stop: ['Stop', `<path d="M11 3.4h10l7.6 7.6v10L21 28.6H11L3.4 21V11z" fill="#D64541" ${BX_O}/><path d="M9.4 16h13.2" stroke="#fff" stroke-width="3.5" stroke-linecap="round"/>`]
};
const BX_STK_MAX = 10;
const bxStkName = n => BX_STK[n]?.[0] || n;
const bxStkSvg = (n, px = 28) => `<svg class="bx-stk-svg" viewBox="0 0 32 32" width="${px}" height="${px}" aria-hidden="true" focusable="false">${BX_STK[n]?.[1] || ''}</svg>`;
function bxStkList(xs) { const ns = xs.map(bxStkName); return ns.length < 3 ? ns.join(' and ') : ns.slice(0, -1).join(', ') + ' and ' + ns[ns.length - 1]; }

async function bxStkDo(card, body, msg) {
  try {
    const c = await api('POST', `/v1/boards/${bxEnc(card.board)}/cards/${bxEnc(card.id)}/stickers`, body);
    if (typeof bdGot === 'function') bdGot(c); else await BOARDS.reload();
    if (msg) toast(msg);
    return c;
  } catch (e) { toast(e.message, true); return null; }
}
function bxStkPickBody(card) {
  const n = (card.stickers || []).length, full = n >= BX_STK_MAX;
  return `<div class="bx-stk-grid" role="group" aria-label="Stickers">${Object.keys(BX_STK).map(k => `<button class="bx-stk-b" data-act="bxStkAdd" data-id="${k}" aria-label="Add the ${esc(bxStkName(k))} sticker" title="${esc(bxStkName(k))}" ${full ? 'disabled' : ''}>${bxStkSvg(k, 30)}</button>`).join('')}</div>
    <p class="bx-pop-note">${full ? `This card has ${BX_STK_MAX} stickers, the most it can have. Take one off in the card window to add another.` : `${n ? `${bxPlural(n, 'sticker')} on this card; up to ${BX_STK_MAX}. ` : ''}The first three show on the card's front. Click a sticker in the card window to take it off.`}</p>`;
}
function bxStkPick(card, ctx, el) {
  UI.bxCard = card;
  bxPop(el, 'Stickers', bxStkPickBody(card), 300);
}
function bxStkSection(card, ctx) {
  const xs = card.stickers || [];
  if (!xs.length) return '';
  const can = bxEdit(ctx);
  return `<section class="bx-sec bx-stk-sec"><h4 class="bx-sec-h">Stickers</h4><div class="bx-stk-row">${xs.map((k, i) => can
    ? `<button class="bx-stk-b on" data-act="bxStkDel" data-id="${i}" aria-label="Remove the ${esc(bxStkName(k))} sticker" title="Remove ${esc(bxStkName(k))}">${bxStkSvg(k, 34)}</button>`
    : `<span class="bx-stk-b ro" role="img" aria-label="${esc(bxStkName(k))} sticker" title="${esc(bxStkName(k))}">${bxStkSvg(k, 34)}</span>`).join('')}
    ${can && xs.length < BX_STK_MAX ? `<button class="bx-stk-more" data-act="bxStkMore" aria-label="Add a sticker">${bdI('plus')}</button>` : ''}</div>
    ${can ? '<p class="bx-help">Click a sticker to take it off.</p>' : ''}</section>`;
}
function bxStkFront(c) {
  const xs = c.stickers || [];
  if (!xs.length) return '';
  const more = xs.length - 3;
  return `<span class="bx-stk-front" role="img" aria-label="Stickers: ${esc(bxStkList(xs))}">${xs.slice(0, 3).map(k => `<span class="bx-stk-f">${bxStkSvg(k, 22)}</span>`).join('')}${more > 0 ? `<span class="bx-stk-n" aria-hidden="true">+${more}</span>` : ''}</span>`;
}

/* ============================== AI on cards ============================== */
/* Summarise a card, or suggest checklist items, through Ledgerline's AI
   gate (internal/ops/boardai.go). The actions show only while the gate
   allows them for the card; a refusal shows its reason and nothing else.
   Nothing the AI writes is posted or added until the person chooses. */
UI.bxAI = UI.bxAI || {};    // card id -> { on, why, can_suggest, at }
UI.bxAIRes = UI.bxAIRes || {}; // card id -> the panel: { kind, busy, text, items, picked, by, note, refused }
if (typeof BD_IC === 'object') {
  BD_IC.spark = BD_IC.spark || '<path d="M12 3.5l1.9 5.1 5.1 1.9-5.1 1.9L12 17.5l-1.9-5.1L5 10.5l5.1-1.9z"/><path d="M18.5 15.5l.8 2.2 2.2.8-2.2.8-.8 2.2-.8-2.2-2.2-.8 2.2-.8z"/>';
  BD_IC.sticker = BD_IC.sticker || '<path d="M19.8 13.6A7.8 7.8 0 1 1 10.4 4.2"/><path d="M10.4 4.2a9.4 9.4 0 0 0 9.4 9.4c0-5.2-4.2-9.4-9.4-9.4z"/>';
}
const bxAIUrl = (card, p = '') => `/v1/boards/${bxEnc(card.board)}/cards/${bxEnc(card.id)}/ai${p}`;
function bxAIState(card) {
  const s = UI.bxAI[card.id];
  if (s && Date.now() - s.at < 60e3) return s;
  if (!s?.loading) {
    UI.bxAI[card.id] = { ...(s || {}), loading: true, at: s?.at || 0 };
    get(bxAIUrl(card)).then(r => {
      const was = !!UI.bxAI[card.id]?.on;
      UI.bxAI[card.id] = { ...r, at: Date.now() };
      if (!!r.on !== was) bxCwRepaint();
    }).catch(() => { UI.bxAI[card.id] = { on: false, at: Date.now() }; });
  }
  return s || {};
}
function bxCwRepaint() { if (typeof bdCwPaint === 'function') bdCwPaint(); else BOARDS.reload(); }
function bxAIPaint(cardId) {
  const el = document.getElementById('bx-ai-' + cardId);
  const card = (typeof BD !== 'undefined' && BD.cd?.id === cardId) ? BD.cd : UI.bxCard;
  if (el && card) el.outerHTML = bxAIPanel(card, bxCtx()); else bxCwRepaint();
}
async function bxAIRun(card, kind) {
  UI.bxAIRes[card.id] = { kind, busy: true };
  bxAIPaint(card.id);
  document.getElementById('bx-ai-' + card.id)?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  try {
    const r = await post(bxAIUrl(card, kind === 'summary' ? '/summary' : '/checklist'), {});
    UI.bxAIRes[card.id] = { kind, text: r.text || '', items: r.items || [], picked: new Set((r.items || []).map((_, i) => i)), by: r.by, note: r.note };
  } catch (e) {
    // The gate refused (or the provider failed): its reason, and nothing
    // else. A refusal also takes the AI buttons away until it allows again.
    UI.bxAIRes[card.id] = { kind, refused: e.message };
    if (e.status === 422 || e.status === 403) { UI.bxAI[card.id] = { on: false, why: e.message, at: Date.now() }; bxCwRepaint(); }
  }
  bxAIPaint(card.id);
  setTimeout(() => document.querySelector(`#bx-ai-${CSS.escape(card.id)} [data-autofocus]`)?.focus({ preventScroll: true }), 0);
}
function bxAIPanel(card, ctx) {
  const r = UI.bxAIRes[card.id];
  if (!r) return `<div id="bx-ai-${esc(card.id)}" hidden></div>`;
  const what = r.kind === 'summary' ? 'Summary of this card' : 'Suggested checklist';
  const hd = `<div class="bx-ai-hd">${bdI('spark')}<b>${what}</b>${r.by ? `<span class="bx-ai-by">AI · ${esc(r.by)}</span>` : ''}<span class="bd-sp"></span><button class="bx-x bx-ai-x" data-act="bxAIClose" data-id="${esc(card.id)}" aria-label="Close">×</button></div>`;
  let body;
  if (r.busy) body = `<div class="bx-ai-busy" role="status"><span class="bx-ai-dots" aria-hidden="true"><i></i><i></i><i></i></span>${r.kind === 'summary' ? 'Reading the card and writing a summary…' : 'Reading the card and thinking of steps…'}</div>`;
  else if (r.refused) body = `<div class="note warn bx-ai-refused" role="alert" data-autofocus tabindex="-1"><b>AI is not available.</b> ${esc(r.refused)}</div>`;
  else if (r.kind === 'summary') body = `<div class="bx-ai-text" data-autofocus tabindex="-1">${esc(r.text)}</div><p class="bx-help">${esc(r.note || '')}</p>
      <div class="bx-ai-acts">${bxCanComment(ctx) ? `<button class="btn sm pri" data-act="bxAIPost" data-id="${esc(card.id)}">Post as a comment</button>` : ''}<button class="btn sm" data-act="bxAICopy" data-id="${esc(card.id)}">Copy</button><button class="btn sm" data-act="bxAIClose" data-id="${esc(card.id)}">Discard</button></div>`;
  else {
    const its = r.items || [];
    const cls = (card.checklists || []);
    body = its.length ? `<ul class="bx-ai-items" aria-label="Suggested items">${its.map((t, i) => `<li><label class="bx-check sm"><input type="checkbox" data-bxai="${esc(card.id)}" data-i="${i}" ${r.picked?.has(i) ? 'checked' : ''}><span>${esc(t)}</span></label></li>`).join('')}</ul>
      <div class="bx-ai-to"><label for="bx-ai-to-${esc(card.id)}">Add to</label><select class="in" id="bx-ai-to-${esc(card.id)}"><option value="">A new checklist</option>${cls.map(cl => `<option value="${esc(cl.id)}">${esc(cl.name)}</option>`).join('')}</select><input class="in" id="bx-ai-name-${esc(card.id)}" maxlength="200" value="Suggested steps" aria-label="Name of the new checklist"></div>
      <p class="bx-help">${esc(r.note || '')}</p>
      <div class="bx-ai-acts"><button class="btn sm pri" data-act="bxAIAdd" data-id="${esc(card.id)}" data-autofocus ${r.picked?.size ? '' : 'disabled'}>Add ${bxPlural(r.picked?.size || 0, 'item')}</button><button class="btn sm" data-act="bxAIClose" data-id="${esc(card.id)}">Discard</button></div>`
      : `<p class="bx-help" data-autofocus tabindex="-1">${esc(r.note || 'No suggestions for this card.')}</p>`;
  }
  return `<section class="bx-ai${r.refused ? ' refused' : ''}" id="bx-ai-${esc(card.id)}" aria-live="polite">${hd}${body}</section>`;
}
const bxCanComment = ctx => !!ctx && !!ctx.role && !ctx.board.closed;
const bxAIOn = c => !!UI.bxAI[c.id]?.on;

BOARDS.cardSections.push(
  { id: 'bxAI', order: 12, render: (card, ctx) => { bxAIState(card); return bxAIPanel(card, ctx); } },
  { id: 'bxStickers', order: 6, render: (card, ctx) => bxStkSection(card, ctx) }
);
BOARDS.cardButtons.push(
  { id: 'bxStickers', icon: 'sticker', label: 'Stickers', group: 'add', order: 65, show: (c, ctx) => bxEdit(ctx) && !c.archived, run: bxStkPick },
  { id: 'bxAISum', icon: 'spark', label: 'Summarise this card', group: 'actions', order: 80, show: c => bxAIOn(c), run: c => bxAIRun(c, 'summary') },
  { id: 'bxAICl', icon: 'spark', label: 'Suggest a checklist', group: 'actions', order: 81, show: (c, ctx) => bxAIOn(c) && !!UI.bxAI[c.id]?.can_suggest && bxEdit(ctx), run: c => bxAIRun(c, 'checklist') }
);
BOARDS.cardBadges.push(c => bxStkFront(c));

document.addEventListener('change', e => {
  const el = e.target;
  if (!el.dataset?.bxai) return;
  const r = UI.bxAIRes[el.dataset.bxai]; if (!r) return;
  if (el.checked) r.picked.add(+el.dataset.i); else r.picked.delete(+el.dataset.i);
  const b = document.querySelector(`#bx-ai-${CSS.escape(el.dataset.bxai)} [data-act="bxAIAdd"]`);
  if (b) { b.textContent = 'Add ' + bxPlural(r.picked.size, 'item'); b.disabled = !r.picked.size; }
});
document.addEventListener('change', e => {
  const el = e.target;
  if (!el.id?.startsWith('bx-ai-to-')) return;
  const nm = document.getElementById('bx-ai-name-' + el.id.slice(9)); if (nm) nm.hidden = !!el.value;
});

Object.assign(A, {
  bxStkAdd: async id => {
    const card = (typeof BD !== 'undefined' && BD.cd) || UI.bxCard; if (!card) return;
    const c = await bxStkDo(card, { add: id });
    if (c) { UI.bxCard = c; const bd = document.getElementById('bx-pop-bd'); if (bd) bd.innerHTML = bxStkPickBody(c); toast(`${bxStkName(id)} sticker added`); }
  },
  bxStkDel: async id => {
    const card = (typeof BD !== 'undefined' && BD.cd) || UI.bxCard; if (!card) return;
    const name = bxStkName((card.stickers || [])[+id]);
    if (await bxStkDo(card, { remove: +id })) toast(`${name} sticker taken off`);
  },
  bxStkMore: (id, el) => { const card = (typeof BD !== 'undefined' && BD.cd) || UI.bxCard; if (card) bxStkPick(card, bxCtx(), el); },
  bxAIClose: id => { delete UI.bxAIRes[id]; bxAIPaint(id); },
  bxAICopy: async id => { const t = UI.bxAIRes[id]?.text || ''; try { await navigator.clipboard.writeText(t); toast('Summary copied'); } catch (e) { toast('Select the summary and copy it'); } },
  bxAIPost: async (id, el) => {
    const r = UI.bxAIRes[id], card = (typeof BD !== 'undefined' && BD.cd?.id === id) ? BD.cd : null;
    if (!r?.text || !card) return;
    el.disabled = true;
    try {
      const c = await post(`/v1/boards/${bxEnc(card.board)}/cards/${bxEnc(card.id)}/comments`, { text: `Summary written with AI (${r.by || 'AI'}) and checked by me:\n\n${r.text}`.slice(0, 5000) });
      delete UI.bxAIRes[id];
      if (typeof bdGot === 'function') bdGot(c); else await BOARDS.reload();
      toast('Summary posted as a comment');
    } catch (e) { toast(e.message, true); el.disabled = false; }
  },
  bxAIAdd: async (id, el) => {
    const r = UI.bxAIRes[id], card = (typeof BD !== 'undefined' && BD.cd?.id === id) ? BD.cd : null;
    if (!r || !card || !r.picked?.size) return;
    const items = [...r.picked].sort((a, b) => a - b).map(i => r.items[i]);
    const to = document.getElementById('bx-ai-to-' + id)?.value || '', name = (document.getElementById('bx-ai-name-' + id)?.value || '').trim() || 'Suggested steps';
    const url = `/v1/boards/${bxEnc(card.board)}/cards/${bxEnc(card.id)}/checklists`;
    el.disabled = true;
    try {
      let c, cl = to;
      if (!cl) { c = await post(url, { op: 'add_list', name }); cl = c.checklists[c.checklists.length - 1].id; }
      for (const t of items) c = await post(url, { op: 'add_item', checklist: cl, name: t });
      delete UI.bxAIRes[id];
      if (typeof bdGot === 'function') bdGot(c); else await BOARDS.reload();
      toast(`${bxPlural(items.length, 'item')} added`);
    } catch (e) { toast(e.message, true); el.disabled = false; }
  },
});
