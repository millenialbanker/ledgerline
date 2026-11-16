'use strict';
/* Ledgerline console: Ledgerline boards (boardsync). Registers into BOARDS
   (boards.js, loaded before this file):
     homeSections  "Ledgerline boards": each board with its cards per list,
                   live, and for admins "Set up Ledgerline boards".
     cardSections  a linked card's record as it is now, with "Open in
                   Ledgerline" (and the client's drawer).
     cardButtons   "Link to Ledgerline": search clients and records the
                   person may see, and link the card to one.
     cardBadges    the linked record on a card's front (team boards).
   Server: internal/ops/boardsync.go, internal/api/boardsync.go. Names, actions
   and state are prefixed bs. */

const BS = { home: null, homeAt: 0, loading: null, err: null, live: {}, pick: null };
const BS_KIND = { client: 'Client', work: 'Work item', break: 'Break', cuspa: 'Unpaid securities', application: 'Application', ticket: 'Ticket', lead: 'Lead' };
const BS_VIEW = { client: 'clients', work: 'autopilot', break: 'recon', cuspa: 'settlement', application: 'apps', ticket: 'helpdesk', lead: 'crm' };
const BS_FIND = [['', 'All'], ['client', 'Clients'], ['work', 'Work'], ['break', 'Breaks'], ['cuspa', 'CUSPA'], ['ticket', 'Tickets'], ['lead', 'Leads']];
const BS_GOOD = ['Done', 'Resolved', 'Closed', 'Active', 'Converted', 'Released', 'Sold', 'Auto-released'];
const BS_QUIET = ['Dismissed', 'Cleared', 'Lost', 'Rejected', 'Draft'];
const BS_BAD = ['Unpaid: notice or invoke', 'Last day to invoke'];
const bsTone = s => BS_GOOD.includes(s) ? 'good' : BS_QUIET.includes(s) ? 'nt' : BS_BAD.includes(s) ? 'bad' : ['Invoked: to sell', 'Depository releases'].includes(s) ? 'acc' : 'warn';
const bsPill = s => s ? pill(s, bsTone(s)) : '';
const bsKey = l => l.kind + '|' + l.id;

/* ---------- the boards home ---------- */
function bsLoadHome(force) {
  if (BS.loading) return BS.loading;
  if (!force && BS.home && Date.now() - BS.homeAt < 15000) return Promise.resolve(BS.home);
  BS.loading = get('/v1/boards/ledgerline')
    .then(d => { BS.home = d; BS.homeAt = Date.now(); BS.err = null; return d; })
    .catch(e => { BS.err = e; return null; })
    .finally(() => { BS.loading = null; const el = $('bs-home'); if (el) el.outerHTML = bsHomeHTML(); });
  return BS.loading;
}

function bsTile(b) {
  const n = b.cards || 0;
  const seg = b.lists.filter(l => l.count).map(l => `<i class="${l.done ? 'done' : ''}" style="flex:${l.count}" title="${esc(l.name)}: ${l.count}"></i>`).join('');
  return `<a class="bs-tile" data-bg="${esc(b.background || 'blue')}" href="#boards" data-act="bsBoard" data-id="${esc(b.id)}">
    <div class="bs-tt"><b>${esc(b.name)}</b>${b.closed ? pill('Closed', 'nt') : ''}</div>
    <div class="bs-sum"><b class="mono">${nf(n)}</b> card${n === 1 ? '' : 's'}${b.overdue ? ` · <span class="bs-od">${nf(b.overdue)} overdue</span>` : ''}</div>
    <div class="bs-bar" aria-hidden="true">${seg || '<i class="none"></i>'}</div>
    <ul class="bs-lists">${b.lists.map(l => `<li class="${l.done ? 'done' : ''}${l.count ? '' : ' zero'}"><span>${esc(l.name)}</span><b class="mono">${l.count}</b></li>`).join('')}</ul>
    ${b.error ? `<div class="bs-err">Not up to date: ${esc(b.error)}</div>` : ''}
  </a>`;
}

function bsHomeHTML() {
  const d = BS.home, head = (acts) => `<div class="bs-head"><div><h2 id="bs-home-h">Ledgerline boards</h2><p>Cards that are Ledgerline’s own records. A card moves when its record does; the comments, checklists and labels people add stay on it.</p></div>${acts || ''}</div>`;
  if (!d) {
    if (!BS.loading && !BS.err) setTimeout(() => bsLoadHome(), 0);
    return `<section class="bs-home" id="bs-home" aria-labelledby="bs-home-h">${head()}${BS.err ? errHTML(BS.err) : '<p class="bs-empty">Loading…</p>'}</section>`;
  }
  const btn = d.can_set_up ? `<button class="btn sm ${d.boards.length ? '' : 'pri'}" data-act="bsSetup">Set up Ledgerline boards</button>` : '';
  const body = d.boards.length ? `<div class="bs-grid">${d.boards.map(bsTile).join('')}</div>`
    : `<p class="bs-empty">${d.can_set_up ? 'No Ledgerline boards yet. Set them up to work the queue, breaks, unpaid securities, onboarding, complaints and leads as boards.' : 'No Ledgerline boards yet: an admin sets them up.'}</p>`;
  return `<section class="bs-home" id="bs-home" aria-labelledby="bs-home-h">${head(btn)}${body}</section>`;
}

function bsSetupModal() {
  const d = BS.home; if (!d) { bsLoadHome(true).then(x => x && bsSetupModal()); return; }
  const all = d.sources.every(s => s.board);
  modal(mhd('Set up Ledgerline boards', 'One board for each: its lists are the records’ stages and its cards follow the records. A board already set up is kept as it is.') +
    `<div class="bd"><div class="bs-src">${d.sources.map(s => `<label class="bs-srcrow${s.board ? ' on' : ''}"><input type="checkbox" value="${esc(s.id)}" ${s.board ? 'checked disabled' : 'checked'}><span><span class="bs-srcn"><b>${esc(s.name)}</b>${s.board ? pill('Set up', 'good') : ''}</span><small>${esc(s.about)}</small><small class="bs-stages">Lists: ${(s.lists || []).map(esc).join(' · ')}</small></span></label>`).join('')}</div>${all ? '<p class="bs-note">Every Ledgerline board is set up. <b>Check the boards</b> gives any of them a list it lacks and brings their cards up to date.</p>' : ''}<div id="bs-su-err"></div></div>
    <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="bs-su-go">${all ? 'Check the boards' : 'Set up'}</button></div>`);
  $('bs-su-go').onclick = async () => {
    const ids = all ? d.sources.map(s => s.id) : [...document.querySelectorAll('.bs-src input:checked:not(:disabled)')].map(x => x.value);
    if (!ids.length) { closeModal(); return; }
    $('bs-su-go').disabled = true;
    try {
      const r = await post('/v1/boards/ledgerline', { sources: ids }), made = r.boards.filter(b => b.created);
      closeModal(); toast(made.length ? 'Set up ' + made.map(b => `${b.name} (${b.cards} card${b.cards === 1 ? '' : 's'})`).join(', ') : 'The Ledgerline boards are up to date');
      await bsLoadHome(true);
      if (S.view === 'boards') render({ keep: true });
    } catch (e) { $('bs-su-err').innerHTML = errHTML(e); $('bs-su-go').disabled = false; }
  };
}

/* ---------- a linked card's record ---------- */
function bsRecHTML(l, hit) {
  const h = hit && hit.rec;
  const more = h ? `${h.sub ? `<div class="bs-sub">${esc(h.sub)}</div>` : ''}${(h.lines || []).length ? `<ul class="bs-lines">${h.lines.map(x => `<li>${esc(x)}</li>`).join('')}</ul>` : ''}`
    : `<div class="bs-sub dim">${esc(hit && hit.err ? hit.err : 'Checking the record…')}</div>`;
  return `<div class="bs-chip"><span class="bs-kind">${esc(BS_KIND[l.kind] || l.kind)}</span>${bsPill(h ? h.status : l.status)}</div><b class="bs-label">${esc((h && h.label) || l.label || l.id)}</b>${more}`;
}

async function bsLive(l) {
  const k = bsKey(l);
  let v;
  try { v = { rec: await get(`/v1/boards/link?kind=${encodeURIComponent(l.kind)}&id=${encodeURIComponent(l.id)}`), at: Date.now() }; }
  catch (e) { v = { err: e.status === 404 ? 'You cannot see this record, or it is no longer in Ledgerline.' : e.message, at: Date.now() }; }
  BS.live[k] = v;
  document.querySelectorAll('.bs-sec').forEach(el => { if (el.dataset.k === k) el.querySelector('.bs-rec').innerHTML = bsRecHTML(l, v); });
}

function bsSection(card, ctx) {
  const l = card && card.link;
  if (!l || !l.kind) return '';
  const k = bsKey(l), hit = BS.live[k], llb = ctx?.board?.kind === 'ledgerline', edit = ['admin', 'member'].includes(ctx?.role);
  if (!hit || Date.now() - hit.at > 20000) setTimeout(() => bsLive(l), 0);
  return `<section class="bs-sec" data-k="${esc(k)}" aria-label="Ledgerline record">
    <div class="bs-sec-hd"><h4>Ledgerline</h4>${llb ? '<span>This card follows its record</span>' : ''}</div>
    <div class="bs-rec">${bsRecHTML(l, hit)}</div>
    <div class="bs-acts"><button class="btn sm pri" data-act="bsOpen" data-id="${esc(k)}" data-v="${esc(l.view || '')}">Open in Ledgerline</button>${l.ucc && l.kind !== 'client' ? `<button class="btn sm" data-act="client" data-id="${esc(l.ucc)}">Client ${esc(l.ucc)}</button>` : ''}${!llb && edit ? `<button class="btn sm" data-act="bsUnlink" data-id="${esc(card.id)}" data-b="${esc(card.board)}">Remove link</button>` : ''}</div>
  </section>`;
}

async function bsOpen(kind, id, view) {
  if (kind === 'client') return clientDrawer(id);
  let rec = BS.live[kind + '|' + id]?.rec;
  if (kind === 'break' && !rec) { try { rec = await get(`/v1/boards/link?kind=break&id=${encodeURIComponent(id)}`); } catch (e) {} }
  const [v, sub] = String(view || BS_VIEW[kind] || 'dashboard').split(':');
  if (!V[v]) { toast('That screen is not open to you.', true); return; }
  if (kind === 'cuspa') Object.assign(UI, { settleTab: 'cuspa', cuView: 'lines', cuF: 'all', cuQ: id });
  if (kind === 'break') { UI.reconF = ''; if (rec?.date && rec.date !== S.date) { S.date = rec.date; toast('Showing trade date ' + fmtDate(rec.date)); } }
  if (kind === 'ticket') UI.hdTab = 'all';
  if (kind === 'lead') UI.crmTab = 'pipe';
  S.view = v; closeModal(); await render(); window.scrollTo(0, 0);
  try {
    if (kind === 'work' && v === 'dispatch' && sub) return dspModal(sub);
    const brk = kind === 'break' ? id : kind === 'work' && v === 'recon' ? id.replace(/^W-recon-/, '') : '';
    if (brk && C.recon?.all?.some(b => b.id === brk)) breakModal(brk);
    if (kind === 'ticket' && C.helpdesk?.t?.tickets?.some(t => t.id === id)) ticketDrawer(id);
    if (kind === 'lead' && C.crm?.leads?.some(l => l.id === id)) leadDrawer(id);
    if (kind === 'application' && Array.isArray(C.apps) && C.apps.some(a => a.id === id)) appModal(id);
  } catch (e) { /* the screen is open; the record's own window is a convenience */ }
}

// After a card's link changes: the board and the card window show it.
function bsAfter(board, card) {
  try { BOARDS.reload(); } catch (e) {}
  try { BOARDS.openCard(board, card); } catch (e) {}
}

/* ---------- linking a card ---------- */
function bsPickClose() {
  const p = $('bs-pop'); if (p) p.remove();
  if (BS.pick) clearTimeout(BS.pick.t);
  BS.pick = null;
  document.removeEventListener('mousedown', bsPickOutside, true);
  document.removeEventListener('scroll', bsPickPlace, true);
}
function bsPickOutside(e) { const p = $('bs-pop'); if (p && !p.contains(e.target) && !(BS.pick && BS.pick.el.contains(e.target))) bsPickClose(); }
function bsPickPlace() {
  const P = BS.pick, p = $('bs-pop'); if (!P || !p) return;
  if (!P.el.isConnected) { bsPickClose(); return; }
  const r = P.el.getBoundingClientRect(), w = Math.min(400, innerWidth - 16), h = Math.min(420, innerHeight - 16);
  let left = r.left + w <= innerWidth - 8 ? r.left : r.right - w;
  left = Math.max(8, Math.min(left, innerWidth - w - 8));
  let top = r.bottom + 6;
  if (top + h > innerHeight - 8) top = Math.max(8, r.top - h - 6);
  Object.assign(p.style, { width: w + 'px', maxHeight: h + 'px', left: left + 'px', top: top + 'px' });
}

function bsPicker(card, el) {
  bsPickClose();
  const p = document.createElement('div');
  p.className = 'bs-pop'; p.id = 'bs-pop'; p.setAttribute('role', 'dialog'); p.setAttribute('aria-label', 'Link to Ledgerline');
  BS.pick = { card, el, kind: '', q: '', hits: [], hl: 0, t: 0, seq: 0 };
  p.innerHTML = `<div class="bs-pop-hd"><b>Link to Ledgerline</b><button type="button" class="x" data-act="bsPickClose" aria-label="Close">×</button></div>
    <div class="bs-kinds">${BS_FIND.map(([k, n]) => `<button type="button" class="${k ? '' : 'on'}" data-act="bsPickKind" data-id="${k}">${n}</button>`).join('')}</div>
    <input class="in" id="bs-q" placeholder="Client code, name or PAN, work item, break, ticket, lead…" autocomplete="off" spellcheck="false" role="combobox" aria-expanded="true" aria-controls="bs-res">
    <div class="bs-res" id="bs-res" role="listbox"><div class="bs-hint">Type two or more letters. You see only what you may see on its own screen.</div></div>
    ${card.link?.kind ? `<div class="bs-pop-ft">Linked now to <b>${esc(card.link.label || card.link.id)}</b>; choosing another replaces it.</div>` : ''}`;
  document.body.appendChild(p);
  bsPickPlace();
  const q = $('bs-q');
  q.addEventListener('input', () => { if (!BS.pick) return; BS.pick.q = q.value; clearTimeout(BS.pick.t); BS.pick.t = setTimeout(bsPickSearch, 220); });
  q.addEventListener('keydown', bsPickKey);
  setTimeout(() => { q.focus(); document.addEventListener('mousedown', bsPickOutside, true); document.addEventListener('scroll', bsPickPlace, true); }, 0);
}

async function bsPickSearch() {
  const P = BS.pick, res = $('bs-res'); if (!P || !res) return;
  const q = P.q.trim();
  if (q.length < 2) { P.hits = []; res.innerHTML = '<div class="bs-hint">Type two or more letters.</div>'; return; }
  const seq = ++P.seq;
  res.innerHTML = '<div class="bs-hint">Searching…</div>';
  try {
    const hits = await get(`/v1/boards/link-search?q=${encodeURIComponent(q)}${P.kind ? '&kind=' + P.kind : ''}`);
    if (BS.pick !== P || seq !== P.seq) return;
    P.hits = hits || []; P.hl = 0; bsPickDraw();
  } catch (e) { if (BS.pick === P && seq === P.seq) res.innerHTML = `<div class="bs-hint bs-bad">${esc(e.message)}</div>`; }
}

function bsPickDraw() {
  const P = BS.pick, res = $('bs-res'); if (!P || !res) return;
  if (!P.hits.length) { res.innerHTML = '<div class="bs-hint">Nothing you may see matches.</div>'; return; }
  let last = '';
  res.innerHTML = P.hits.map((h, i) => {
    const g = h.kind !== last ? `<div class="bs-grp">${esc(BS_KIND[h.kind] || h.kind)}</div>` : '';
    last = h.kind;
    return g + `<button type="button" role="option" id="bs-o${i}" class="bs-hit${i === P.hl ? ' hl' : ''}" aria-selected="${i === P.hl}" data-act="bsPick" data-id="${i}"><span class="bs-hl"><b>${esc(h.label)}</b>${bsPill(h.status)}</span>${h.sub ? `<span class="bs-hs">${esc(h.sub)}</span>` : ''}</button>`;
  }).join('');
  $('bs-q')?.setAttribute('aria-activedescendant', 'bs-o' + P.hl);
}

function bsPickKey(e) {
  const P = BS.pick; if (!P) return;
  if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); bsPickClose(); P.el.focus?.(); return; }
  if (!P.hits.length) return;
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault();
    P.hl = (P.hl + (e.key === 'ArrowDown' ? 1 : P.hits.length - 1)) % P.hits.length;
    bsPickDraw(); $('bs-o' + P.hl)?.scrollIntoView({ block: 'nearest' });
  }
  if (e.key === 'Enter') { e.preventDefault(); bsPickChoose(P.hl); }
}

async function bsPickChoose(i) {
  const P = BS.pick, h = P && P.hits[+i]; if (!h) return;
  const link = { kind: h.kind, id: h.id, label: h.label, view: h.view || '', status: h.status || '', ucc: h.ucc || '' };
  try {
    await api('PATCH', `/v1/boards/${encodeURIComponent(P.card.board)}/cards/${encodeURIComponent(P.card.id)}`, { link });
    BS.live[bsKey(link)] = { rec: h, at: Date.now() };
    bsPickClose(); toast('Linked to ' + h.label);
    bsAfter(P.card.board, P.card.id);
  } catch (e) { toast(e.message, true); }
}

/* ---------- registrations ---------- */
Object.assign(A, {
  bsSetup: () => bsSetupModal(),
  bsBoard: id => BOARDS.openBoard(id),
  bsOpen: (k, el) => { const i = k.indexOf('|'); bsOpen(k.slice(0, i), k.slice(i + 1), el.dataset.v); },
  bsUnlink: async (id, el) => {
    const b = el.dataset.b;
    try { await api('PATCH', `/v1/boards/${encodeURIComponent(b)}/cards/${encodeURIComponent(id)}`, { link: { kind: '' } }); toast('Link removed'); bsAfter(b, id); }
    catch (e) { toast(e.message, true); }
  },
  bsPick: i => bsPickChoose(i),
  bsPickKind: (k, el) => {
    if (!BS.pick) return;
    BS.pick.kind = k || '';
    el.parentElement.querySelectorAll('button').forEach(x => x.classList.toggle('on', x === el));
    bsPickSearch(); $('bs-q')?.focus();
  },
  bsPickClose: () => bsPickClose(),
});

if (typeof BOARDS !== 'undefined') {
  BOARDS.homeSections.push({ id: 'ledgerline', order: 20, load: () => bsLoadHome(), render: () => bsHomeHTML() });
  BOARDS.cardSections.push({ id: 'ledgerline', order: 5, render: (card, ctx) => bsSection(card, ctx) });
  BOARDS.cardButtons.push({
    id: 'bsLink', label: 'Link to Ledgerline', group: 'add', order: 60,
    show: (card, ctx) => ['admin', 'member'].includes(ctx?.role) && !(ctx?.board?.kind === 'ledgerline' && card.link?.kind),
    run: (card, ctx, el) => bsPicker(card, el),
  });
  BOARDS.cardBadges.push((card, ctx) => {
    const l = card.link;
    if (!l || !l.kind || ctx?.board?.kind === 'ledgerline') return '';
    return `<span class="bs-badge" title="${esc((BS_KIND[l.kind] || l.kind) + ': ' + (l.label || l.id))}"><i class="t-${bsTone(l.status)}"></i>${esc(l.kind === 'client' ? l.id : (BS_KIND[l.kind] || l.kind))}${l.status ? ' · ' + esc(l.status) : ''}</span>`;
  });
}
