'use strict';
/* Ledgerline console: boards, boardplanner. A person's own things:
     Inbox    a private board (kind "inbox", one list) to jot cards down:
              a panel on the boards home and in the Planner, and the key i
              anywhere on the boards screen. Only its owner can see it. A
              card is sorted by moving it to a board.
     Planner  a page of the boards home: the week by hour, with the dates
              of your cards and the time you set aside for cards (plan
              blocks). Drag a card from the side onto an hour; drag a block
              to move it, its foot to resize it; or use the keyboard (arrows)
              or the Plan form. Blocks never change a card's dates unless you
              choose "Set as due date".
     Feeds    calendar feeds (iCalendar) of your cards or of a board, for
              Google Calendar or Outlook; off until the firm's Admin
              switches them on.
   Server: internal/ops/boardplanner.go. Uses boardviews.js's date helpers
   and its home pages (BV_HOME). Ledgerline's own calendar (Calendar &
   circulars) has no per-person API, so it is not shown here. */

Object.assign(UI, { bpInbox: null, bpInboxAt: 0, bpInboxBusy: false, bpInboxAll: false, bpPlan: null, bpWeek: '', bpDay: -1, bpFeeds: null, bpNew: null });

/* ---------- small helpers ---------- */
const bpE = encodeURIComponent;
const BP_HH = 48; // pixels an hour
const BP_STEP = 15; // minutes a block moves by
const bpNarrow = () => matchMedia('(max-width: 760px)').matches;
const bpMonday = () => UI.bpWeek || bvDS(bvMonday(bvDN(bvToday())));
const bpAt = (day, mins) => bvDS(day) + 'T' + bvHHMM(mins);
const bpLen = m => m < 60 ? `${m} min` : m % 60 ? `${Math.floor(m / 60)} h ${m % 60} min` : `${m / 60} h`;
const bpDue = c => c.due ? `<span class="bv-due bv-st-${bvStatus(c)}" title="${BV_ST[bvStatus(c)]}">${bvStIcon(bvStatus(c))}${esc(bvFmt(c.due))}<span class="bv-sr">, ${BV_ST[bvStatus(c)]}</span></span>` : '';
const bpChecks = c => c.items ? `<span class="bv-ck${c.items_done === c.items ? ' all' : ''}">${BV_I.check}${c.items_done}/${c.items}</span>` : '';
// Inbox cards come as full cards; the Planner's as PlanCards. One shape for both.
const bpCardOf = (c, board) => c.card ? c : { board, card: c.id, no: c.no, title: c.title, due: c.due, start: c.start, due_done: c.due_done, inbox: true,
  items: (c.checklists || []).reduce((n, cl) => n + (cl.items || []).length, 0), items_done: (c.checklists || []).reduce((n, cl) => n + (cl.items || []).filter(i => i.done).length, 0) };

/* ---------- Inbox ---------- */
async function bpInboxLoad() {
  UI.bpInboxBusy = true;
  try { UI.bpInbox = await get('/v1/board-inbox'); } catch (e) { UI.bpInbox = { error: e.message || 'Your Inbox could not be loaded.' }; }
  UI.bpInboxBusy = false; UI.bpInboxAt = Date.now();
  return UI.bpInbox;
}
function bpInboxFresh() { if (!UI.bpInboxBusy && (!UI.bpInbox || Date.now() - UI.bpInboxAt > 30000)) bpInboxLoad().then(bpInboxPaint); }
function bpInboxPaint() {
  const el = $('bp-inbox');
  if (el) { const f = document.activeElement?.id; el.outerHTML = bpInboxHTML(); if (f) $(f)?.focus(); }
  if (UI.bvPage === 'planner' && $('bp-side')) bvPagePaint();
}
function bpAddBox(where, label) {
  return `<div class="bp-add"><label class="bv-sr" for="bp-add-${where}">${label}</label><input id="bp-add-${where}" class="in" type="text" maxlength="500" autocomplete="off" placeholder="${esc(label)}" data-enter="bpInboxAdd" data-id="${where}"><button class="btn pri sm" data-act="bpInboxAdd" data-id="${where}">Add</button></div>`;
}
function bpInboxCard(c, o = {}) {
  return `<li><div class="bp-sc${o.drag ? '' : ' bp-sc-nd'}"${o.drag ? ` draggable="true" data-bp-drag="${esc(c.board)}" data-c="${esc(c.card)}"` : ''}>
    ${o.drag ? `<span class="bp-grip" aria-hidden="true">${bdI('grip')}</span>` : ''}<span class="bp-sc-b"><button class="bp-sc-t" data-act="bpOpen" data-id="${esc(c.board)}" data-c="${esc(c.card)}">${esc(c.title)}</button>
    ${o.board ? `<span class="bp-sc-m">${esc(c.board_name)}${c.list_name ? ' · ' + esc(c.list_name) : ''}</span>` : ''}
    ${c.due || c.items || c.planned ? `<span class="bp-sc-f">${bpDue(c)}${bpChecks(c)}${c.planned ? '<span class="bp-tag">Planned</span>' : ''}</span>` : ''}</span>
    <span class="bp-sc-a">${o.plan ? `<button class="btn sm" data-act="bpPlanForm" data-id="${esc(c.board)}" data-c="${esc(c.card)}" aria-label="Plan time for ${esc(c.title)}">Plan</button>` : ''}${c.inbox ? `<button class="btn sm" data-act="bpMove" data-id="${esc(c.card)}" data-popkey="bp-mv-${esc(c.card)}" aria-label="Move ${esc(c.title)} to a board">Move</button>` : ''}</span></div></li>`;
}
function bpInboxHTML() {
  bpInboxFresh();
  const d = UI.bpInbox, hd = sum => `<div class="bv-my-hd"><div><h3 id="bp-inbox-h">Inbox</h3><p>Jot cards down here; only you can see them. Press <kbd>i</kbd> anywhere on Boards to add one, and move each to a board when you know where it goes.</p></div>${sum || ''}</div>`;
  if (!d) return `<section class="bv bp-inbox" id="bp-inbox" aria-labelledby="bp-inbox-h" aria-busy="true">${hd()}<p class="bv-dim bv-small">Loading your Inbox…</p></section>`;
  if (d.error) return `<section class="bv bp-inbox" id="bp-inbox" aria-labelledby="bp-inbox-h">${hd()}<div class="note bad">${esc(d.error)} <button class="btn sm" data-act="bpInboxReload">Try again</button></div></section>`;
  const cs = (d.cards || []).map(c => bpCardOf(c, d.board)), show = UI.bpInboxAll ? cs : cs.slice(0, 8);
  return `<section class="bv bp-inbox" id="bp-inbox" aria-labelledby="bp-inbox-h">${hd(`<p class="bv-my-sum">${bvPlural(cs.length, 'card')}</p>`)}
    ${bpAddBox('home', 'Add a card to your Inbox')}
    ${cs.length ? `<ul class="bp-ilist">${show.map(c => bpInboxCard(c)).join('')}</ul>${cs.length > 8 ? `<button class="bv-more" data-act="bpInboxMore" aria-expanded="${UI.bpInboxAll}">${UI.bpInboxAll ? 'Show fewer' : `Show all ${cs.length}`}</button>` : ''}` : '<p class="bv-dim bv-small bp-empty">Your Inbox is empty.</p>'}</section>`;
}
async function bpInboxAdd(where, due) {
  const el = $('bp-add-' + where), title = (el?.value || '').trim();
  if (!title) { el?.focus(); toast('Type a title for the card'); return null; }
  try {
    const c = await post('/v1/board-inbox/cards', due ? { title, due } : { title });
    if (el) el.value = '';
    if (UI.bpInbox && !UI.bpInbox.error) UI.bpInbox.cards = [c, ...(UI.bpInbox.cards || [])];
    UI.bpPlan = null;
    toast('Added to your Inbox');
    bpInboxPaint();
    if (S.view === 'boards' && UI.bdBoard && UI.bdBoard === UI.bpInbox?.board) BOARDS.reload();
    setTimeout(() => $('bp-add-' + where)?.focus(), 0);
    return c;
  } catch (e) { toast(e.message, true); return null; }
}
// "Add to Inbox": the key i on the boards screen.
function bpQuickModal() {
  modal(mhd('Add to Inbox', 'Only you can see your Inbox. Move the card to a board later.') + `<div class="bd"><div class="fld"><label for="bp-add-quick">Card title</label><input id="bp-add-quick" class="in" type="text" maxlength="500" autocomplete="off" data-enter="bpQuickGo"></div>
    <div class="fld"><label for="bp-q-due">Due (optional)</label><div class="bp-row"><input id="bp-q-due" class="in" type="date" aria-label="Due date"><input id="bp-q-time" class="in" type="time" step="900" aria-label="Due time (optional)"></div></div></div>
    <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="bpQuickGo">Add to Inbox</button></div>`);
}
window.addEventListener('keydown', e => {
  if (e.key !== 'i' || S.view !== 'boards' || !S.me || clientUser() || !$('bd-root')) return;
  const t = e.target;
  if (e.ctrlKey || e.metaKey || e.altKey || e.isComposing || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName) || t.isContentEditable) return;
  if (document.querySelector('#mroot .ov') || BD.pop || BD.drag) return;
  e.preventDefault(); e.stopImmediatePropagation();
  bpQuickModal();
}, true);
// Moving an Inbox card to a board: the team boards where you can add cards.
async function bpMovePop(card, el) {
  const inbox = UI.bpInbox?.board;
  if (!inbox) return;
  let targets = UI.bpTargets;
  bdPop('bp-mv-' + card, el, 'Move to a board', () => {
    if (!targets) return '<p class="bd-mute">Loading your boards…</p>';
    if (targets.error) return `<p class="bd-mute">${esc(targets.error)}</p>`;
    if (!targets.length) return '<p class="bd-mute">There is no board where you can add cards. Create a board first.</p>';
    const b = targets.find(x => x.id === UI.bpMvBoard) || targets[0];
    return `<div class="bp-mv"><div class="fld"><label for="bp-mv-b">Board</label><select id="bp-mv-b" class="in">${targets.map(x => `<option value="${esc(x.id)}" ${x.id === b.id ? 'selected' : ''}>${esc(x.name)}</option>`).join('')}</select></div>
      <div class="fld"><label for="bp-mv-l">List</label><select id="bp-mv-l" class="in">${b.lists.map(l => `<option value="${esc(l.id)}">${esc(l.name)}</option>`).join('')}</select></div>
      <button class="btn pri sm" data-act="bpMoveGo" data-id="${esc(card)}">Move</button></div>`;
  });
  if (!targets) {
    try { targets = UI.bpTargets = await get(`/v1/boards/${bpE(inbox)}/mirror-targets`); } catch (e) { targets = { error: e.message }; }
    setTimeout(() => { UI.bpTargets = null; }, 60000);
    if (BD.pop?.key === 'bp-mv-' + card) bdPopRefresh();
  }
}
document.addEventListener('change', e => {
  if (e.target.id === 'bp-mv-b') { UI.bpMvBoard = e.target.value; bdPopRefresh(); $('bp-mv-b')?.focus(); }
});
async function bpMoveGo(card) {
  const inbox = UI.bpInbox?.board, b = $('bp-mv-b')?.value, l = $('bp-mv-l')?.value;
  if (!inbox || !b || !l) return;
  try {
    const c = await post(`/v1/boards/${bpE(inbox)}/cards/${bpE(card)}/move`, { board: b, list: l });
    bdPopClose(true);
    const name = (UI.bpTargets || []).find?.(x => x.id === b)?.name || 'the board';
    if (UI.bpInbox?.cards) UI.bpInbox.cards = UI.bpInbox.cards.filter(x => x.id !== card);
    UI.bpPlan = null;
    toast(`Moved to ${name}`);
    bpInboxPaint();
    return c;
  } catch (e) { toast(e.message, true); }
}
BOARDS.homeSections.push({ id: 'bp-inbox', order: 3, load: bpInboxLoad, render: () => bpInboxHTML() });

/* ---------- Planner ---------- */
function bpPlanData() {
  const from = bpMonday(), key = from, s = UI.bpPlan;
  if (s && s.key === key && (s.busy || Date.now() - s.at < 30000)) return s;
  UI.bpPlan = { key, busy: true, at: Date.now(), data: s?.key === key ? s.data : null };
  get(`/v1/board-planner?from=${from}&days=7`)
    .then(d => { if (UI.bpPlan?.key === key) UI.bpPlan = { key, at: Date.now(), data: d }; })
    .catch(e => { if (UI.bpPlan?.key === key) UI.bpPlan = { key, at: Date.now(), error: e.message || 'Your plan could not be loaded.' }; })
    .finally(() => { if (UI.bvPage === 'planner') bvPagePaint(); });
  return UI.bpPlan;
}
const bpBlockLabel = (b, c) => `Planned: ${c ? c.title : 'a card you can no longer see'}, ${bvFmt(b.start, { wd: 1, noTime: 1 })} ${bvTm(b.start)} to ${bvHHMM(Math.min(1440, bvMin(b.start) + b.minutes))}`;
function bpBlockHTML(b, day0) {
  const c = b.card, m0 = bvMin(b.start), h = b.minutes / 60 * BP_HH, short = h < 40;
  const lab = bpBlockLabel(b, c) + '. Arrow keys move it; with Shift, up and down change its length; Enter for more.';
  return `<div class="bp-blk${c ? '' : ' bp-gone'}${short ? ' bp-short' : ''}" role="button" tabindex="0" data-bp-blk="${esc(b.id)}" data-day="${bvDN(b.start) - day0}" style="top:${m0 / 60 * BP_HH}px;height:${Math.max(18, h - 2)}px" aria-label="${esc(lab)}" title="${esc(c ? c.title : '')}">
    <span class="bp-blk-tm">${short ? bvTm(b.start) : `${bvTm(b.start)}–${bvHHMM(Math.min(1440, m0 + b.minutes))}`}</span><span class="bp-blk-t">${esc(c ? c.title : 'A card you can no longer see')}</span>${c && h >= 64 ? `<span class="bp-blk-m">${esc(c.inbox ? 'Inbox' : c.board_name)}</span>` : ''}<span class="bp-rs" data-bp-rs aria-hidden="true"></span></div>`;
}
function bpPlannerHTML() {
  const from = bpMonday(), a = bvDN(from), today = bvDN(bvToday()), s = bpPlanData(), d = s.data, narrow = bpNarrow();
  if (UI.bpDay < 0 || UI.bpDay > 6) UI.bpDay = today >= a && today <= a + 6 ? today - a : 0;
  const days = narrow ? [a + UI.bpDay] : [0, 1, 2, 3, 4, 5, 6].map(i => a + i);
  const title = bvCalTitle({ mode: 'week' }, { a, z: a + 6 });
  const tools = `<div class="bv-tools"><div class="bv-tools-l"><button class="btn sm" data-act="bpWeek" data-id="0">Today</button><span class="bv-pair"><button class="btn sm bv-ib" data-act="bpWeek" data-id="-1" aria-label="Previous week">${BV_I.left}</button><button class="btn sm bv-ib" data-act="bpWeek" data-id="1" aria-label="Next week">${BV_I.right}</button></span>
      <h2 class="bv-title" id="bp-h" aria-live="polite">${title}</h2><span class="bv-dim bv-small">Planner · times in IST${s.busy && d ? ' · updating…' : ''}</span></div>
    <div class="bv-tools-r"><span class="bv-dim bv-small bp-hint">Drag a card onto an hour, or press <b>Plan</b>. Plans do not change a card’s dates.</span></div></div>`;
  if (!d) return `<section class="bv bp-root" id="bv-page" aria-labelledby="bp-h">${tools}${s.error ? `<div class="note bad">${esc(s.error)} <button class="btn sm" data-act="bpRetry">Try again</button></div>` : '<p class="bv-dim bv-small">Loading your plan…</p>'}</section>`;
  const pick = narrow ? `<div class="seg bv-seg bp-daypick" role="group" aria-label="Day">${BV_WDS.map((w, i) => `<button class="${i === UI.bpDay ? 'on' : ''}" aria-pressed="${i === UI.bpDay}" data-act="bpDay" data-id="${i}">${w}<small>${+bvDS(a + i).slice(8)}</small></button>`).join('')}</div>` : '';
  const head = days.map(n => `<div class="bv-hw-dh${n === today ? ' bv-today' : ''}${bvWd(n) > 4 ? ' bv-we' : ''}"><small>${BV_WDS[bvWd(n)]}</small><b>${+bvDS(n).slice(8)}</b>${n === today ? '<span class="bv-sr"> (today)</span>' : ''}</div>`).join('');
  // All day: cards due that day with no time, or starting that day.
  const allday = days.map(n => {
    const cs = d.dated.filter(c => (c.due && !bvTm(c.due) && bvDN(c.due) === n) || (c.start && bvDN(c.start) === n && (!c.due || bvDN(c.due) !== n)));
    return `<div class="bp-ad${n === today ? ' bv-today' : ''}">${cs.map(c => { const st = bvStatus(c), starts = !(c.due && bvDN(c.due) === n && !bvTm(c.due)); return `<button class="bp-adc bv-st-${st}" data-act="bpOpen" data-id="${esc(c.board)}" data-c="${esc(c.card)}" title="${esc(c.title)}">${starts ? '<b>Starts</b>' : bvStIcon(st)}<span>${esc(c.title)}</span><span class="bv-sr">, ${starts ? 'starts' : 'due'} ${esc(bvFmt(starts ? c.start : c.due, { wd: 1 }))}, ${BV_ST[st]}</span></button>`; }).join('')}</div>`;
  }).join('');
  const cols = days.map(n => {
    const dues = d.dated.filter(c => c.due && bvTm(c.due) && bvDN(c.due) === n).map(c => { const st = bvStatus(c); return `<button class="bp-due bv-st-${st}" style="top:${bvMin(c.due) / 60 * BP_HH}px" data-act="bpOpen" data-id="${esc(c.board)}" data-c="${esc(c.card)}" title="Due ${esc(bvTm(c.due))}: ${esc(c.title)}">${bvStIcon(st) || BV_I.clock}<b>${bvTm(c.due)}</b><span>${esc(c.title)}</span><span class="bv-sr">, due ${esc(bvFmt(c.due, { wd: 1 }))}, ${BV_ST[st]}</span></button>`; }).join('');
    const blocks = d.blocks.filter(b => bvDN(b.start) === n).map(b => bpBlockHTML(b, n)).join('');
    const now = n === today ? `<i class="bv-hw-now" style="top:${(bvWall() % BV_DAY) / 36e5 * BP_HH}px" aria-hidden="true"></i>` : '';
    return `<div class="bv-hw-col bp-col${bvWd(n) > 4 ? ' bv-we' : ''}${n === today ? ' bv-today' : ''}" data-dn="${n}">${now}${dues}${blocks}</div>`;
  }).join('');
  const hours = [...Array(24)].map((_, h) => `<span style="top:${h * BP_HH}px">${h ? bvHHMM(h * 60) : ''}</span>`).join('');
  const nb = d.blocks.length, mins = d.blocks.reduce((t, b) => t + b.minutes, 0);
  const grid = `<div class="bp-cal" style="--bp-n:${days.length}"><div class="bv-scroll bv-hw-scroll bp-scroll" role="region" aria-label="Your week by hour" tabindex="-1">
    <div class="bv-hw-top"><div class="bv-hw-head" aria-hidden="true"><span class="bv-hw-g"></span>${head}</div><div class="bv-hw-ad-row"><span class="bv-hw-g">All day</span><div class="bp-ads">${allday}</div></div></div>
    <div class="bv-hw-body" style="height:${24 * BP_HH}px;--bv-hh:${BP_HH}px"><div class="bv-hw-hours" aria-hidden="true">${hours}</div>
      <div class="bv-hw-cols bp-cols" data-day0="${days[0]}">${cols}<i class="bp-ghost" hidden></i></div></div></div></div>`;
  const side = `<aside class="bp-side" id="bp-side" aria-label="Cards to plan">
    <section class="bp-sec" aria-labelledby="bp-in-h"><h3 id="bp-in-h">Inbox <span class="bv-ct">${d.inbox.length}</span></h3>${bpAddBox('plan', 'Add a card to your Inbox')}
      ${d.inbox.length ? `<ul class="bp-ilist">${d.inbox.map(c => bpInboxCard(c, { drag: true, plan: true })).join('')}</ul>` : '<p class="bv-dim bv-small">Your Inbox is empty.</p>'}</section>
    <section class="bp-sec" aria-labelledby="bp-my-h"><h3 id="bp-my-h">Your cards <span class="bv-ct">${d.cards.length}</span></h3><p class="bv-dim bv-small">Cards you are on, not planned this week.</p>
      ${d.cards.length ? `<ul class="bp-ilist">${d.cards.map(c => bpInboxCard(c, { drag: true, plan: true, board: true })).join('')}</ul>${d.more ? '<p class="bv-dim bv-small">The soonest 300 are shown.</p>' : ''}` : '<p class="bv-dim bv-small">Every card you are on is planned this week, or you are on none.</p>'}</section></aside>`;
  return `<section class="bv bp-root" id="bv-page" aria-labelledby="bp-h" data-key="planner:${from}">${tools}
    <p class="bv-sr" aria-live="polite" id="bp-live"></p>
    <div class="bp-wrap"><div class="bp-main">${pick}<p class="bp-sum bv-dim bv-small">${nb ? `${bvPlural(nb, 'block')} planned, ${bpLen(mins)} in all` : 'Nothing planned this week yet.'}</p>${grid}</div>${side}</div></section>`;
}
const bpBlock = id => UI.bpPlan?.data?.blocks.find(b => b.id === id);
const bpSay = t => { const l = $('bp-live'); if (l) l.textContent = t; };
// Saves a block's new time: shown at once, put back if refused.
async function bpBlockSave(b, patch, say) {
  const before = { start: b.start, minutes: b.minutes };
  Object.assign(b, patch);
  bvPagePaint();
  document.querySelector(`[data-bp-blk="${CSS.escape(b.id)}"]`)?.focus();
  if (say) bpSay(bpBlockLabel(b, b.card));
  try { const r = await api('PATCH', `/v1/board-planner/blocks/${bpE(b.id)}`, patch); Object.assign(b, { start: r.start, minutes: r.minutes }); }
  catch (e) { Object.assign(b, before); bvPagePaint(); toast(e.message, true); }
}
async function bpBlockAdd(board, card, start, minutes) {
  try {
    const r = await post('/v1/board-planner/blocks', { board, card, start, minutes: minutes || 30 });
    UI.bpPlan = null; bvPagePaint();
    toast(`Planned ${bvFmt(r.start, { wd: 1 })}, ${bpLen(r.minutes)}`);
    setTimeout(() => document.querySelector(`[data-bp-blk="${CSS.escape(r.id)}"]`)?.focus(), 400);
    return r;
  } catch (e) { toast(e.message, true); return null; }
}
async function bpBlockDel(b) {
  try { await post(`/v1/board-planner/blocks/${bpE(b.id)}/delete`); UI.bpPlan = null; bdPopClose(true); bvPagePaint(); toast('Taken off your plan'); }
  catch (e) { toast(e.message, true); }
}
// A block's own keys: arrows move it by 15 minutes or a day; Shift with
// up or down changes its length; Delete takes it off the plan.
document.addEventListener('keydown', e => {
  const el = e.target.closest?.('[data-bp-blk]');
  if (!el || e.target !== el || e.ctrlKey || e.metaKey || e.altKey) return;
  const b = bpBlock(el.dataset.bpBlk);
  if (!b) return;
  const m = bvMin(b.start), day = bvDN(b.start);
  let p = null;
  if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
    const d = e.key === 'ArrowUp' ? -BP_STEP : BP_STEP;
    if (e.shiftKey) { const n = b.minutes + d; if (n >= 15 && n <= 720 && m + n <= 1440) p = { minutes: n }; }
    else { const n = m + d; if (n >= 0 && n + b.minutes <= 1440) p = { start: bpAt(day, n) }; }
  } else if (e.key === 'ArrowLeft' || e.key === 'ArrowRight') p = { start: bpAt(day + (e.key === 'ArrowLeft' ? -1 : 1), m) };
  else if (e.key === 'Delete' || e.key === 'Backspace') { e.preventDefault(); bpBlockDel(b); return; }
  else if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); bpBlockMenu(b, el); return; }
  else return;
  e.preventDefault();
  if (!p) return;
  const from = bpMonday();
  if (p.start && (bvDN(p.start) < bvDN(from) || bvDN(p.start) > bvDN(from) + 6)) { UI.bpWeek = bvDS(bvMonday(bvDN(p.start))); UI.bpPlan = null; }
  bpBlockSave(b, p, true);
});
function bpBlockMenu(b, el) {
  bdPop('bp-blk-' + b.id, el, 'Planned time', () => {
    const c = b.card, end = Math.min(1440, bvMin(b.start) + b.minutes);
    return `<p class="bp-pm-h"><b>${esc(c ? c.title : 'A card you can no longer see')}</b><br><span class="bv-dim">${esc(bvFmt(b.start, { wd: 1, noTime: 1 }))}, ${bvTm(b.start)}–${bvHHMM(end)} (${bpLen(b.minutes)})</span></p><div class="bd-menu" role="menu">
      ${c ? `<button class="bd-mi" role="menuitem" data-act="bpOpen" data-id="${esc(c.board)}" data-c="${esc(c.card)}">${bdI('card')}<span>Open the card</span></button>` : ''}
      <button class="bd-mi" role="menuitem" data-act="bpBlockForm" data-id="${esc(b.id)}">${bdI('clock')}<span>Change the time</span></button>
      ${c && c.can_edit ? `<button class="bd-mi" role="menuitem" data-act="bpSetDue" data-id="${esc(b.id)}">${bdI('check')}<span>Set as due date (${esc(bvFmt(bpAt(bvDN(b.start), end), { wd: 1 }))})</span></button>` : ''}
      <button class="bd-mi danger" role="menuitem" data-act="bpBlockDel" data-id="${esc(b.id)}">${bdI('trash')}<span>Take off the plan</span></button></div>`;
  });
}
// The form for planning without dragging: a day, a start and a length.
function bpPlanForm(o) {
  const at = o.start || (() => { const today = bvToday(), a = bvDN(bpMonday()), n = bvDN(today) >= a && bvDN(today) <= a + 6 ? bvDN(today) : a; const now = Math.ceil(((bvWall() % BV_DAY) / 6e4 + 1) / 30) * 30; return bpAt(n, n === bvDN(today) && now < 1410 ? now : 540); })();
  const lens = [15, 30, 45, 60, 90, 120, 180, 240, 300, 360, 480, 600, 720], cur = o.minutes || 30;
  modal(mhd(o.block ? 'Change the planned time' : 'Plan time for a card', esc(o.title || '')) + `<div class="bd"><div class="bp-form">
      <div class="fld"><label for="bp-f-day">Day</label><input id="bp-f-day" class="in" type="date" value="${esc(at.slice(0, 10))}"></div>
      <div class="fld"><label for="bp-f-time">Start (IST)</label><input id="bp-f-time" class="in" type="time" step="900" value="${esc(bvTm(at))}"></div>
      <div class="fld"><label for="bp-f-len">Length</label><select id="bp-f-len" class="in">${lens.map(m => `<option value="${m}" ${m === cur ? 'selected' : ''}>${bpLen(m)}</option>`).join('')}${lens.includes(cur) ? '' : `<option value="${cur}" selected>${bpLen(cur)}</option>`}</select></div></div>
      <p class="bv-dim bv-small">This only plans your time. The card’s own dates do not change.</p><div id="bp-f-err"></div></div>
    <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="bpPlanSave" data-id="${esc(o.block || '')}" data-b="${esc(o.board || '')}" data-c="${esc(o.card || '')}">${o.block ? 'Save' : 'Plan'}</button></div>`);
}
// Dragging: a card from the side onto an hour (HTML drag and drop); a
// block to move it, or its foot to resize it (pointer).
let bpDrag = null, bpPtr = null, bpClickHold = 0;
const bpSlot = (cols, x, y, snap) => {
  const r = cols.getBoundingClientRect(), n = cols.children.length - 1 || 1, i = Math.max(0, Math.min(n - 1, Math.floor((x - r.left) / (r.width / n))));
  return { day: +cols.dataset.day0 + i, i, mins: Math.max(0, Math.min(1440 - snap, Math.floor((y - r.top) / BP_HH * 60 / snap) * snap)) };
};
function bpGhost(cols, at, mins) {
  const g = cols.querySelector('.bp-ghost');
  if (!g) return;
  g.hidden = !at;
  if (!at) return;
  const n = cols.children.length - 1 || 1;
  Object.assign(g.style, { top: at.mins / 60 * BP_HH + 'px', height: mins / 60 * BP_HH - 2 + 'px', left: `calc(${at.i / n * 100}% + 3px)`, width: `calc(${100 / n}% - 6px)` });
  g.textContent = `${bvHHMM(at.mins)}–${bvHHMM(at.mins + mins)}`;
}
document.addEventListener('dragstart', e => {
  const el = e.target.closest?.('[data-bp-drag]');
  if (!el) return;
  bpDrag = { board: el.dataset.bpDrag, card: el.dataset.c };
  e.dataTransfer.effectAllowed = 'copy';
  e.dataTransfer.setData('text/plain', el.querySelector('.bp-sc-t')?.textContent || '');
  setTimeout(() => el.classList.add('bv-dragging'));
});
document.addEventListener('dragend', e => { e.target.closest?.('[data-bp-drag]')?.classList.remove('bv-dragging'); document.querySelectorAll('.bp-cols').forEach(c => bpGhost(c, null)); bpDrag = null; });
document.addEventListener('dragover', e => {
  const cols = bpDrag && e.target.closest?.('.bp-cols');
  if (!cols) return;
  e.preventDefault(); e.dataTransfer.dropEffect = 'copy';
  bpGhost(cols, bpSlot(cols, e.clientX, e.clientY, 30), 30);
});
document.addEventListener('dragleave', e => { const cols = bpDrag && e.target.closest?.('.bp-cols'); if (cols && !cols.contains(e.relatedTarget)) bpGhost(cols, null); });
document.addEventListener('drop', e => {
  const cols = bpDrag && e.target.closest?.('.bp-cols');
  if (!cols) return;
  e.preventDefault();
  const at = bpSlot(cols, e.clientX, e.clientY, 30), d = bpDrag;
  bpGhost(cols, null); bpDrag = null;
  bpBlockAdd(d.board, d.card, bpAt(at.day, at.mins), 30);
});
document.addEventListener('pointerdown', e => {
  const el = e.target.closest?.('[data-bp-blk]');
  if (!el || e.button > 0) return;
  const b = bpBlock(el.dataset.bpBlk), cols = el.closest('.bp-cols');
  if (!b || !cols) return;
  bpPtr = { el, b, cols, mode: e.target.closest('[data-bp-rs]') ? 'size' : 'move', x0: e.clientX, y0: e.clientY, m0: bvMin(b.start), len: b.minutes, day0: bvDN(b.start), moved: false, pid: e.pointerId, at: null };
});
document.addEventListener('pointermove', e => {
  const p = bpPtr;
  if (!p || e.pointerId !== p.pid) return;
  if (!p.moved) {
    if (Math.abs(e.clientX - p.x0) < 5 && Math.abs(e.clientY - p.y0) < 5) return;
    p.moved = true;
    try { p.el.setPointerCapture(p.pid); } catch (er) {}
    p.el.classList.add('bp-dragging');
  }
  const dm = Math.round((e.clientY - p.y0) / BP_HH * 60 / BP_STEP) * BP_STEP;
  if (p.mode === 'size') {
    const len = Math.max(15, Math.min(720, 1440 - p.m0, p.len + dm));
    p.at = { minutes: len };
    p.el.style.height = len / 60 * BP_HH - 2 + 'px';
    p.el.querySelector('.bp-blk-tm').textContent = `${bvHHMM(p.m0)}–${bvHHMM(p.m0 + len)}`;
    p.el.classList.toggle('bp-short', len / 60 * BP_HH < 40);
  } else {
    const s = bpSlot(p.cols, e.clientX, e.clientY, BP_STEP), m = Math.max(0, Math.min(1440 - p.len, p.m0 + dm)), col = p.cols.children[s.i];
    p.at = { start: bpAt(s.day, m) };
    if (col && p.el.parentElement !== col) col.appendChild(p.el);
    p.el.style.top = m / 60 * BP_HH + 'px';
    p.el.querySelector('.bp-blk-tm').textContent = `${bvHHMM(m)}–${bvHHMM(m + p.len)}`;
  }
});
const bpPtrEnd = e => {
  const p = bpPtr;
  if (!p || e.pointerId !== p.pid) return;
  bpPtr = null;
  if (!p.moved) return;
  bpClickHold = Date.now() + 200;
  p.el.classList.remove('bp-dragging');
  if (e.type === 'pointerup' && p.at && (p.at.minutes ? p.at.minutes !== p.b.minutes : p.at.start !== p.b.start)) bpBlockSave(p.b, p.at);
  else bvPagePaint();
};
document.addEventListener('pointerup', bpPtrEnd);
document.addEventListener('pointercancel', bpPtrEnd);
document.addEventListener('click', e => {
  const el = e.target.closest?.('[data-bp-blk]');
  if (!el || e.target.closest('[data-bp-rs]') || Date.now() < bpClickHold) return;
  const b = bpBlock(el.dataset.bpBlk);
  if (b) bpBlockMenu(b, el);
});
matchMedia('(max-width: 760px)').addEventListener?.('change', () => { if (UI.bvPage === 'planner') bvPagePaint(); });
BV_HOME.pages.push({ id: 'planner', label: 'Planner', order: 10, render: bpPlannerHTML, after: el => { if (el) bvKeepScroll(el, sc => { const d = UI.bpPlan?.data; let m = 8 * 60; (d?.blocks || []).forEach(b => { m = Math.min(m, bvMin(b.start)); }); sc.scrollTop = Math.max(0, m / 60 * BP_HH - 10); }); } });

/* ---------- calendar feeds ---------- */
const BP_WARN = `<div class="note warn bp-warn"><b>Card titles and due dates will leave Ledgerline.</b> A calendar feed copies the title, due date, board and list of each card to the calendar service that reads it (such as Google or Microsoft), and to anyone that calendar is shared with. Card titles can name clients. Descriptions, comments, checklists and files are never sent.</div>`;
async function bpFeedsLoad() { UI.bpFeeds = await get('/v1/board-feeds'); return UI.bpFeeds; }
function bpFeedRow(label, f, scope, board, allowed) {
  return `<li class="bp-fr"><span class="bp-fr-n"><b>${esc(label)}</b><small class="bv-dim">${f ? `Address made ${esc(bvFmt(bvStamp(f.created_at), { noTime: 1 }))}${f.used_at ? ` · last read ${esc(bdWhen(f.used_at))}` : ' · not read yet'}${f.allowed ? '' : ' · feeds are off, so it is empty'}` : allowed ? 'No address yet' : 'Feeds are off'}</small></span>
    <span class="bp-fr-a">${allowed ? `<button class="btn sm${f ? '' : ' pri'}" data-act="bpFeedNew" data-id="${esc(scope)}" data-b="${esc(board || '')}">${f ? 'New address' : 'Get address'}</button>` : ''}${f ? `<button class="btn sm" data-act="bpFeedStop" data-id="${esc(f.id)}">Stop</button>` : ''}</span></li>`;
}
function bpNewHTML() {
  const n = UI.bpNew;
  if (!n) return '';
  return `<div class="bp-new" role="status"><p><b>Your feed’s address.</b> It is shown only now; if you lose it, make a new one. Anyone with it can see these cards’ titles and due dates, so keep it to yourself.</p>
    <div class="bp-row"><label class="bv-sr" for="bp-url">Feed address</label><input id="bp-url" class="in mono" readonly value="${esc(n.url)}"><button class="btn sm pri" data-act="bpCopy">Copy</button></div>
    <p class="bv-dim bv-small">Google Calendar: <b>Other calendars</b>, <b>+</b>, <b>From URL</b>, and paste it. Outlook: <b>Add calendar</b>, <b>Subscribe from web</b>. They read it again every few hours.</p></div>`;
}
function bpFeedsHTML(board) {
  const v = UI.bpFeeds;
  if (!v) return '<div class="bd"><p class="bv-dim">Loading…</p></div>';
  const mine = v.feeds.find(f => f.scope === 'mine'), bfs = v.feeds.filter(f => f.scope === 'board'), bi = board ? v.boards.find(b => b.id === board) : null;
  const firm = `<section class="bp-fs"><h3>For the firm</h3>${v.firm_on ? `<p>Calendar feeds are <b>on</b> for the firm${v.firm_by ? `, since ${esc(bvFmt(bvStamp(v.firm_at), { noTime: 1 }))} (${esc(bdName(v.firm_by))})` : ''}.</p>` : `<p>Calendar feeds are <b>off</b> for the firm${v.boards.some(b => b.on) ? ', except on boards the Admin allowed one by one' : ''}.</p>`}
    ${v.can_switch ? `<button class="btn sm" data-act="bpFirm" data-id="${v.firm_on ? 'off' : 'on'}">${v.firm_on ? 'Switch off for the firm' : 'Switch on for the firm'}</button>` : `<p class="bv-dim bv-small">The firm’s Admin switches them on or off.</p>`}</section>`;
  let body;
  if (board) {
    const allowed = v.firm_on || bi?.on;
    body = `<section class="bp-fs"><h3>This board</h3><p>${bi?.on ? 'The Admin allowed feeds of this board.' : v.firm_on ? 'Allowed, as feeds are on for the firm.' : 'Feeds of this board are off.'}</p>
      ${v.can_switch ? `<button class="btn sm" data-act="bpBoardFeedSw" data-id="${esc(board)}" data-on="${bi?.on ? '0' : '1'}">${bi?.on ? 'Stop feeds of this board' : 'Allow feeds of this board'}</button>` : ''}
      <ul class="bp-frs">${bpFeedRow('This board’s due dates', bfs.find(f => f.board === board), 'board', board, allowed)}</ul></section>`;
  } else {
    const opts = v.boards.filter(b => b.allowed && !bfs.some(f => f.board === b.id));
    body = `<section class="bp-fs"><h3>Your feeds</h3><ul class="bp-frs">${bpFeedRow('My cards: the cards you are on, and your Inbox', mine, 'mine', '', v.firm_on)}
      ${bfs.map(f => bpFeedRow(f.board_name || 'A board you can no longer see', f, 'board', f.board, f.allowed)).join('')}</ul>
      ${opts.length ? `<div class="bp-row bp-addfeed"><span aria-hidden="true">A board’s feed</span><label class="bv-sr" for="bp-fb">A board’s feed</label><select id="bp-fb" class="in sm">${opts.map(b => `<option value="${esc(b.id)}">${esc(b.name)}</option>`).join('')}</select><button class="btn sm" data-act="bpFeedNewSel">Get address</button></div>` : ''}</section>`;
  }
  return `<div class="bd bp-feeds">${bpNewHTML()}${body}${firm}<p class="bv-dim bv-small">A feed has each card’s due date as an event (all day when it has no time), its title, its board and list, and a link back here. Stop a feed and its address stops working at once.</p></div><div class="ft"><button class="btn pri" data-act="close">Done</button></div>`;
}
async function bpFeedsModal(board) {
  UI.bpNew = null; UI.bpFeedsBoard = board || '';
  const sub = board ? 'See this board’s due dates in Google Calendar or Outlook.' : 'See your cards’ due dates in Google Calendar or Outlook.';
  modal(mhd('Calendar feed' + (board ? '' : 's'), sub) + bpFeedsHTML(board));
  try { await bpFeedsLoad(); } catch (e) { UI.bpFeeds = null; toast(e.message, true); return; }
  bpFeedsPaint();
}
function bpFeedsPaint() {
  const md = document.querySelector('#mroot .md');
  if (!md || !document.querySelector('#mroot .bp-feeds, #mroot .bd')) return;
  const hd = md.querySelector('.hd')?.outerHTML || '';
  md.innerHTML = hd + bpFeedsHTML(UI.bpFeedsBoard);
}
function bpSwitchOn(what, run) {
  modal(mhd(`Switch on calendar feeds ${what}`, '') + `<div class="bd">${BP_WARN}<p>Each person makes their own feed address and can stop it at any time. Switching feeds off later empties every feed.</p>
    <label class="check"><input type="checkbox" id="bp-ok"> <span>I understand that card titles and due dates leave Ledgerline</span></label><div id="bp-sw-err"></div></div>
    <div class="ft"><button class="btn" data-act="bpFeedsBack">Cancel</button><button class="btn pri" id="bp-sw-go">Switch on</button></div>`);
  $('bp-sw-go').onclick = async () => {
    if (!$('bp-ok').checked) { $('bp-sw-err').innerHTML = '<div class="note bad">Tick the box to confirm.</div>'; $('bp-ok').focus(); return; }
    $('bp-sw-go').disabled = true;
    try { await run(); await bpFeedsModal(UI.bpFeedsBoard); toast('Calendar feeds are on'); } catch (e) { $('bp-sw-err').innerHTML = errHTML(e); $('bp-sw-go').disabled = false; }
  };
}
BV_HOME.buttons.push(() => clientUser() ? '' : `<button class="btn sm" data-act="bpFeeds">${BV_I.cal}Calendar feeds</button>`);
BOARDS.boardMenu.push({ id: 'bp-feed', label: 'Calendar feed', icon: 'clock', order: 75, show: ctx => ctx.board.kind !== 'inbox' && !ctx.board.template, run: ctx => bpFeedsModal(ctx.board.id) });

/* ---------- actions ---------- */
Object.assign(A, {
  bpOpen: (board, el) => { bdPopClose(true); closeModal(); BOARDS.openCard(board, el.dataset.c); },
  bpInboxAdd: id => bpInboxAdd(id),
  bpInboxMore: () => { UI.bpInboxAll = !UI.bpInboxAll; bpInboxPaint(); },
  bpInboxReload: () => { UI.bpInbox = null; bpInboxPaint(); bpInboxLoad().then(bpInboxPaint); },
  bpQuickGo: async () => {
    const d = $('bp-q-due')?.value, t = $('bp-q-time')?.value, due = d ? d + (t ? 'T' + t : '') : '';
    const c = await bpInboxAdd('quick', due);
    if (c) closeModal();
  },
  bpMove: (card, el) => bpMovePop(card, el),
  bpMoveGo: card => bpMoveGo(card),
  bpWeek: id => { const d = +id; UI.bpWeek = d ? bvDS(bvDN(bpMonday()) + 7 * d) : ''; if (!d) UI.bpDay = -1; bvPagePaint(); },
  bpDay: id => { UI.bpDay = +id; bvPagePaint(); document.querySelector(`[data-act=bpDay][data-id="${id}"]`)?.focus(); },
  bpRetry: () => { UI.bpPlan = null; bvPagePaint(); },
  bpPlanForm: (board, el) => {
    const all = [...(UI.bpPlan?.data?.inbox || []), ...(UI.bpPlan?.data?.cards || [])], c = all.find(x => x.board === board && x.card === el.dataset.c);
    bpPlanForm({ board, card: el.dataset.c, title: c?.title || '' });
  },
  bpBlockForm: id => { const b = bpBlock(id); if (!b) return; bdPopClose(true); bpPlanForm({ block: b.id, title: b.card?.title || '', start: b.start, minutes: b.minutes }); },
  bpPlanSave: async (id, el) => {
    const day = $('bp-f-day')?.value, tm = $('bp-f-time')?.value, len = +$('bp-f-len')?.value;
    if (!day || !tm) { $('bp-f-err').innerHTML = '<div class="note bad">Choose a day and a start time.</div>'; return; }
    const start = day + 'T' + tm;
    if (id) { const b = bpBlock(id); if (!b) return; closeModal(); if (bvDN(start) < bvDN(bpMonday()) || bvDN(start) > bvDN(bpMonday()) + 6) UI.bpWeek = bvDS(bvMonday(bvDN(start))); await bpBlockSave(b, { start, minutes: len }); UI.bpPlan = null; bvPagePaint(); return; }
    closeModal();
    if (bvDN(start) < bvDN(bpMonday()) || bvDN(start) > bvDN(bpMonday()) + 6) UI.bpWeek = bvDS(bvMonday(bvDN(start)));
    await bpBlockAdd(el.dataset.b, el.dataset.c, start, len);
  },
  bpSetDue: async id => {
    const b = bpBlock(id); if (!b) return;
    try { const c = await post(`/v1/board-planner/blocks/${bpE(id)}/set-due`); bdPopClose(true); UI.bpPlan = null; bvPagePaint(); toast(`Due date set to ${bvFmt(c.due, { wd: 1 })}`); }
    catch (e) { toast(e.message, true); }
  },
  bpBlockDel: id => { const b = bpBlock(id); if (b) bpBlockDel(b); },
  bpFeeds: () => bpFeedsModal(''),
  bpFeedsBack: () => bpFeedsModal(UI.bpFeedsBoard),
  bpFeedNew: async (scope, el) => {
    try { UI.bpNew = await post('/v1/board-feeds', { scope, board: el.dataset.b || '' }); await bpFeedsLoad(); bpFeedsPaint(); $('bp-url')?.select(); $('bp-url')?.focus(); }
    catch (e) { toast(e.message, true); }
  },
  bpFeedNewSel: () => { const b = $('bp-fb')?.value; if (b) A.bpFeedNew('board', { dataset: { b } }); },
  bpFeedStop: async id => {
    try { await post(`/v1/board-feeds/${bpE(id)}/revoke`); UI.bpNew = null; await bpFeedsLoad(); bpFeedsPaint(); toast('Feed stopped: its address no longer works'); }
    catch (e) { toast(e.message, true); }
  },
  bpCopy: async () => {
    const i = $('bp-url'); if (!i) return;
    try { await navigator.clipboard.writeText(i.value); toast('Address copied'); } catch (e) { i.select(); document.execCommand?.('copy'); toast('Address copied'); }
  },
  bpFirm: async id => {
    if (id === 'on') { bpSwitchOn('for the firm', () => api('PUT', '/v1/board-feeds/firm', { on: true })); return; }
    confirmModal('Switch off calendar feeds', 'For the firm', '<p style="margin:0">Every feed is emptied the next time a calendar reads it. Boards the Admin allowed one by one keep their feeds.</p>', 'Switch off', async () => { await api('PUT', '/v1/board-feeds/firm', { on: false }); await bpFeedsModal(UI.bpFeedsBoard); toast('Calendar feeds are off for the firm'); }, true);
  },
  bpBoardFeedSw: async (board, el) => {
    const on = el.dataset.on === '1', run = () => api('PUT', `/v1/boards/${bpE(board)}/views/feed`, { on });
    if (on) { bpSwitchOn('for this board', run); return; }
    try { await run(); await bpFeedsLoad(); bpFeedsPaint(); toast('Feeds of this board stopped'); } catch (e) { toast(e.message, true); }
  }
});
