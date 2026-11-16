'use strict';
/* Ledgerline console: board automation (Trello's Butler for boards).
   Rules, card buttons, board buttons, scheduled and due-date commands and
   the automation log of the open board, written and read as sentences.
   Plugs into BOARDS (boards.js): "Automation" in the board menu, the
   board's buttons in its header, card buttons in the card window.
   Server: internal/ops/boardrules.go, internal/api/boardrules.go.
   Actions are named A.brSomething, UI state UI.brSomething, and every
   other name starts br or BR_. */

Object.assign(UI, { brTab: 'rule', brDraft: null, brAsk: '', brAskCards: 0, brLog: null, brLogAuto: '', brData: null, brBoard: '', brAt: 0, brCtx: null, brLoading: null });

const BR_TABS = [['rule', 'Rules'], ['card_button', 'Card buttons'], ['board_button', 'Board buttons'], ['schedule', 'Scheduled'], ['due', 'Due date'], ['log', 'Log']];
const BR_KIND = { rule: 'rule', card_button: 'card button', board_button: 'board button', schedule: 'scheduled command', due: 'due-date command' };
const BR_NEW = { rule: 'New rule', card_button: 'New card button', board_button: 'New board button', schedule: 'New scheduled command', due: 'New due-date command' };
const BR_INTRO = {
  rule: 'A rule watches the board. When something happens to a card, and the card meets the conditions, it does the actions in order.',
  card_button: 'A card button appears in every card window of this board. Pressing it does its actions on that card.',
  board_button: 'A board button sits in the board header. Pressing it does its actions on every open card of a list, or of the board, that meets the conditions.',
  schedule: 'A scheduled command runs at a time of day (IST): every day, on working days, on chosen weekdays or on a day of the month. Working days leave out the firm’s trading holidays in Settings.',
  due: 'A due-date command runs once for each card, a set time before or after the card’s due date. A due date without a time counts as 23:59.',
  log: 'Every run of every rule, button and command on this board: what set it off, what it did, or why it stopped.'
};
const BR_EVENTS = [['card_added', 'a card is added to'], ['card_moved_in', 'a card is moved into'], ['card_moved_out', 'a card is moved out of'], ['label_added', 'a label is added to a card'], ['label_removed', 'a label is removed from a card'],
  ['member_added', 'someone is added to a card'], ['due_set', 'a due date is set on a card'], ['card_done', 'a card is marked complete'], ['checklist_done', 'a checklist on a card is completed'], ['card_archived', 'a card is archived'], ['comment_posted', 'a comment is posted on a card']];
const BR_CONDS = [['has_label', 'it has the label'], ['lacks_label', 'it does not have the label'], ['has_member', 'it has the member'], ['lacks_member', 'it does not have the member'], ['in_list', 'it is in the list'], ['not_in_list', 'it is not in the list'],
  ['title_contains', 'its title contains'], ['has_due', 'it has a due date'], ['no_due', 'it has no due date'], ['overdue', 'it is overdue'], ['complete', 'it is complete'], ['not_complete', 'it is not complete']];
const BR_ACTS = [['move', 'Move the card to', 'card'], ['copy', 'Copy the card to', 'card'], ['add_label', 'Add the label', 'card'], ['remove_label', 'Remove the label', 'card'], ['add_member', 'Add the member', 'card'], ['remove_member', 'Remove the member', 'card'],
  ['join', 'Add the person who set it off', 'card'], ['set_due', 'Set the due date', 'card'], ['clear_due', 'Remove the due date', 'card'], ['mark_done', 'Mark it complete', 'card'], ['mark_undone', 'Mark it not complete', 'card'],
  ['add_checklist', 'Add a checklist', 'card'], ['comment', 'Post a comment', 'card'], ['archive', 'Archive the card', 'card'], ['sort_list', 'Sort a list', 'any'], ['notify', 'Notify people', 'any'],
  ['create_card', 'Create a card', 'board'], ['move_all', 'Move every card of a list', 'board'], ['archive_list', 'Archive every card of a list', 'board']];
const BR_NOT_LL = ['move', 'copy', 'archive', 'create_card', 'move_all', 'archive_list'];
const BR_SORTS = [['due', 'due date'], ['title', 'title'], ['newest', 'newest first'], ['oldest', 'oldest first']];
const BR_EVERY = [['day', 'Every day'], ['weekday', 'Every weekday (Mon–Fri)'], ['working_day', 'Every working day'], ['weekdays', 'On chosen weekdays'], ['month_day', 'On a day of the month'], ['first_working', 'On the first working day of the month'], ['last_working', 'On the last working day of the month']];
const BR_WD = [[1, 'Mon'], [2, 'Tue'], [3, 'Wed'], [4, 'Thu'], [5, 'Fri'], [6, 'Sat'], [0, 'Sun']];
const BR_WDN = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
const BR_STATUS = { done: ['Done', 'br-st-done'], failed: ['Failed', 'br-st-bad'], stopped: ['Stopped: loop', 'br-st-warn'], limit: ['Daily limit', 'br-st-bad'], skipped: ['Skipped', 'br-st-nt'] };
const brEnc = encodeURIComponent;
const brPath = (b, rest) => `/v1/boards/${brEnc(b)}/automation${rest || ''}`;
const BR_ICON = { bolt: '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M9.2 1.5 3.5 9h4l-.7 5.5L12.5 7h-4z"/></svg>', play: '<svg viewBox="0 0 16 16" aria-hidden="true"><path d="M5 3.2v9.6L12.6 8z"/></svg>' };

/* ---------- the board's context ---------- */
function brCtx() {
  let c = null;
  try { c = BOARDS.ctx(); } catch (e) { c = null; }
  if (c && c.board && (!UI.brCtx || UI.brCtx.board?.id === c.board.id)) UI.brCtx = c;
  return UI.brCtx || c;
}
const brLists = ctx => (ctx?.board?.lists || []).filter(l => !l.archived).sort((a, b) => a.pos - b.pos);
const brLabels = ctx => ctx?.board?.labels || [];
const brPeople = ctx => (ctx?.users || []).filter(u => u.id && !String(u.id).startsWith('system:'));
const brLedgerline = ctx => ctx?.board?.kind === 'ledgerline';
function brListName(ctx, id) { const l = (ctx?.board?.lists || []).find(x => x.id === id); return l ? l.name : 'a list that no longer exists'; }
function brFindList(ctx, re, fallback) { const ls = brLists(ctx); return (ls.find(l => re.test(l.name)) || ls[fallback === 'last' ? ls.length - 1 : (fallback || 0)] || ls[0] || {}).id || ''; }
function brPlain(html) { const d = document.createElement('div'); d.innerHTML = html; return d.textContent.replace(/\s+/g, ' ').trim(); }
function brClip(s, n) { s = String(s || ''); return s.length > n ? s.slice(0, n - 1) + '…' : s; }
const brToday = () => new Date(Date.now() + 330 * 60000).toISOString().slice(0, 10); // IST
function brTime(iso) {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}:\d{2})/.exec(iso || ''); if (!m) return '';
  return (`${m[1]}-${m[2]}-${m[3]}` === brToday() ? 'today' : `${+m[3]} ${MON[+m[2] - 1]}`) + ' ' + m[4];
}

/* ---------- sentences ---------- */
const brTk = (cls, text) => `<span class="br-tk ${cls}">${esc(text)}</span>`;
const brTkList = (ctx, id) => brTk('br-tk-list', brListName(ctx, id));
const brTkWho = id => brTk('br-tk-who', '@' + id);
const brTkText = s => brTk('br-tk-text', '“' + brClip(s, 90) + '”');
function brTkLabel(ctx, id) { const l = brLabels(ctx).find(x => x.id === id); if (!l) return brTk('br-tk-gone', 'a deleted'); return `<span class="br-tk br-tk-label br-lc-${esc(l.color)}">${esc(l.name || l.color)}</span>`; }
function brAnd(xs) { xs = xs.filter(Boolean); return xs.length < 2 ? (xs[0] || '') : xs.slice(0, -1).join(', ') + ' and ' + xs[xs.length - 1]; }
function brTrigger(ctx, t) {
  if (!t) return 'When something happens';
  const by = t.by ? ` by ${brTkWho(t.by)}` : '';
  switch (t.event) {
    case 'card_added': return `When a card is added to ${t.list ? brTkList(ctx, t.list) : 'the board'}${by}`;
    case 'card_moved_in': return `When a card is moved into ${t.list ? brTkList(ctx, t.list) : 'another list'}${by}`;
    case 'card_moved_out': return `When a card is moved out of ${t.list ? brTkList(ctx, t.list) : 'a list'}${by}`;
    case 'label_added': return `When ${t.label ? 'the ' + brTkLabel(ctx, t.label) + ' label is' : 'a label is'} added to a card${by}`;
    case 'label_removed': return `When ${t.label ? 'the ' + brTkLabel(ctx, t.label) + ' label is' : 'a label is'} removed from a card${by}`;
    case 'member_added': return `When ${t.member ? brTkWho(t.member) : 'someone'} is added to a card${by}`;
    case 'due_set': return `When a due date is set on a card${by}`;
    case 'card_done': return `When a card is marked complete${by}`;
    case 'checklist_done': return `When a checklist on a card is completed${by}`;
    case 'card_archived': return `When a card is archived${by}`;
    case 'comment_posted': return `When a comment ${t.words ? 'containing ' + brTkText(t.words) + ' ' : ''}is posted on a card${by}`;
  }
  return 'When something happens';
}
function brCond(ctx, c) {
  switch (c.kind) {
    case 'has_label': return `it has the ${brTkLabel(ctx, c.value)} label`;
    case 'lacks_label': return `it does not have the ${brTkLabel(ctx, c.value)} label`;
    case 'has_member': return c.value ? `${brTkWho(c.value)} is on it` : 'it has members';
    case 'lacks_member': return c.value ? `${brTkWho(c.value)} is not on it` : 'it has no members';
    case 'in_list': return `it is in ${brTkList(ctx, c.value)}`;
    case 'not_in_list': return `it is not in ${brTkList(ctx, c.value)}`;
    case 'title_contains': return `its title contains ${brTkText(c.value || '…')}`;
    case 'has_due': return 'it has a due date';
    case 'no_due': return 'it has no due date';
    case 'overdue': return 'it is overdue';
    case 'complete': return 'it is complete';
    case 'not_complete': return 'it is not complete';
  }
  return '';
}
function brDueText(a) {
  const d = +a.days || 0, w = a.working_days;
  const day = d === 0 ? 'today' : d === 1 && !w ? 'tomorrow' : `in ${d} ${w ? 'working ' : ''}day${d === 1 ? '' : 's'}`;
  return day + (a.time ? ` at ${esc(a.time)}` : '');
}
function brWho(ctx, to) {
  return brAnd((to || []).map(x => x === 'card_members' ? 'the card’s members' : x === 'watchers' ? 'its watchers' : brTkWho(x))) || 'no one yet';
}
function brAction(ctx, a) {
  switch (a.kind) {
    case 'move': return `move it to the ${a.top ? 'top' : 'bottom'} of ${brTkList(ctx, a.list)}`;
    case 'copy': return `copy it to ${brTkList(ctx, a.list)}`;
    case 'add_label': return `add the ${brTkLabel(ctx, a.label)} label`;
    case 'remove_label': return `remove the ${brTkLabel(ctx, a.label)} label`;
    case 'add_member': return `add ${a.member ? brTkWho(a.member) : 'someone'}`;
    case 'remove_member': return `remove ${a.member ? brTkWho(a.member) : 'someone'}`;
    case 'join': return 'add the person who set it off';
    case 'set_due': return `set it due ${brDueText(a)}`;
    case 'clear_due': return 'remove its due date';
    case 'mark_done': return 'mark it complete';
    case 'mark_undone': return 'mark it not complete';
    case 'add_checklist': { const n = (a.items || []).filter(x => String(x).trim()).length; return `add the checklist ${brTkText(a.name || 'Checklist')} (${n} item${n === 1 ? '' : 's'})`; }
    case 'comment': return `post the comment ${brTkText(a.text || '…')}`;
    case 'archive': return 'archive it';
    case 'sort_list': return `sort ${a.list ? brTkList(ctx, a.list) : 'its list'} by ${(BR_SORTS.find(s => s[0] === (a.by || 'due')) || BR_SORTS[0])[1]}`;
    case 'notify': return `notify ${brWho(ctx, a.to)}`;
    case 'create_card': return `create the card ${brTkText(a.text || '…')} in ${brTkList(ctx, a.list)}${a.due ? ', due ' + brDueText(a) : ''}`;
    case 'move_all': return `move every card in ${brTkList(ctx, a.from)} to ${brTkList(ctx, a.list)}`;
    case 'archive_list': return `archive every card in ${brTkList(ctx, a.list)}`;
  }
  return esc(a.kind);
}
function brWhen(w) {
  if (!w) return 'At some time';
  const at = ` at ${esc(w.at || '--:--')} IST`, skip = w.skip_holidays ? ', except trading holidays' : '';
  switch (w.every) {
    case 'day': return `Every day${at}${skip}`;
    case 'weekday': return `Every weekday${at}${skip}`;
    case 'working_day': return `Every working day${at}`;
    case 'weekdays': return `Every ${brAnd(BR_WD.filter(d => (w.days || []).includes(d[0])).map(d => BR_WDN[d[0]])) || '…'}${at}${skip}`;
    case 'month_day': return `On day ${+w.day || '…'} of every month${at}${skip}`;
    case 'first_working': return `On the first working day of every month${at}`;
    case 'last_working': return `On the last working day of every month${at}`;
  }
  return 'At some time';
}
function brDueAt(ctx, d) {
  if (!d) return 'Around a card’s due date';
  const n = +d.n || 0, unit = n === 1 ? String(d.unit || 'days').replace(/s$/, '') : (d.unit || 'days');
  let s = n === 0 ? 'When a card falls due' : `${n} ${unit} ${d.when === 'after' ? 'after' : 'before'} a card is due`;
  const only = [d.list ? `it is in ${brTkList(ctx, d.list)}` : '', d.label ? `it has the ${brTkLabel(ctx, d.label)} label` : '', d.done ? '' : 'it is not complete'];
  return { head: s, only: only.filter(Boolean) };
}
function brSentence(ctx, a) {
  const conds = (a.conditions || []).map(c => brCond(ctx, c)).filter(Boolean);
  const acts = a.actions || [];
  const said = xs => brAnd(xs.map(x => brAction(ctx, x)));
  switch (a.kind) {
    case 'rule': return `${brTrigger(ctx, a.trigger)}${conds.length ? ', if ' + brAnd(conds) : ''}, ${said(acts) || '…'}.`;
    case 'card_button': return `Pressed on a card: ${said(acts) || '…'}.`;
    case 'board_button': {
      const per = acts.filter(x => (BR_ACTS.find(y => y[0] === x.kind) || [])[2] === 'card'), once = acts.filter(x => !per.includes(x));
      const where = `each card in ${a.list ? brTkList(ctx, a.list) : 'the board'}${conds.length ? ' where ' + brAnd(conds) : ''}`;
      return `Pressed: ${per.length ? `for ${where}, ${said(per)}` : ''}${per.length && once.length ? '; then ' : ''}${once.length ? said(once) : ''}${acts.length ? '' : '…'}.`;
    }
    case 'schedule': return `${brWhen(a.when)}, ${said(acts) || '…'}.`;
    case 'due': { const d = brDueAt(ctx, a.due), only = [...d.only, ...conds]; return `${d.head}${only.length ? ', if ' + brAnd(only) : ''}, ${said(acts) || '…'}.`; }
  }
  return '';
}

/* ---------- loading, header buttons, card buttons ---------- */
async function brLoad(board) {
  if (UI.brLoading && UI.brLoading.board === board) return UI.brLoading.p;
  const mine = { board };
  // A slower answer for a board the person has since left must not replace the newer one.
  mine.p = get(brPath(board)).then(d => { if (!UI.brLoading || UI.brLoading === mine) { UI.brData = d; UI.brBoard = board; UI.brAt = Date.now(); brSyncCardButtons(); brPaintHeader(); } return d; })
    .finally(() => { if (UI.brLoading === mine) UI.brLoading = null; });
  UI.brLoading = mine;
  return mine.p;
}
function brEnsure(ctx) {
  if (!ctx?.board?.id) return;
  if (UI.brBoard !== ctx.board.id || Date.now() - UI.brAt > 60000) brLoad(ctx.board.id).catch(() => {});
}
function brSyncCardButtons() {
  const keep = BOARDS.cardButtons.filter(b => !String(b.id).startsWith('br-cb-'));
  BOARDS.cardButtons.length = 0;
  BOARDS.cardButtons.push(...keep);
  (UI.brData?.autos || []).filter(a => a.kind === 'card_button' && a.on).forEach((a, i) => BOARDS.cardButtons.push({
    id: 'br-cb-' + a.id, icon: 'bolt', label: a.name, group: 'actions', order: 70 + i,
    show: (card, ctx) => !!UI.brData?.can_edit && ctx?.board?.id === a.board && !card?.archived,
    run: (card, ctx, el) => brRunCard(a, card, ctx || brCtx(), el)
  }));
}
function brHeaderInner(ctx) {
  const d = UI.brData;
  if (!d || !ctx?.board || UI.brBoard !== ctx.board.id) return '';
  return d.autos.filter(a => a.kind === 'board_button' && a.on).map(a =>
    `<button class="br-hb" data-act="brBoardRun" data-id="${esc(a.id)}" title="${esc(brPlain(brSentence(ctx, a)))}" ${d.can_edit ? '' : 'disabled'}>${BR_ICON.play}<span>${esc(a.name)}</span></button>`).join('');
}
function brPaintHeader() { const el = $('br-hbs'); if (el) el.innerHTML = brHeaderInner(brCtx()); }

BOARDS.boardMenu.push({ id: 'br-automation', icon: 'bolt', label: 'Automation', order: 40, run: ctx => brOpen(ctx) });
BOARDS.headerButtons.push({ id: 'br-board-buttons', order: 60, render: ctx => { brEnsure(ctx); return `<span class="br-hbs" id="br-hbs">${brHeaderInner(ctx)}</span>`; } });

function brRunText(run) {
  if (!run) return '';
  const did = (run.did || []).filter(x => x !== 'no cards matched');
  if (run.status === 'failed') return `${run.name} could not finish: ${run.error}`;
  if (run.status === 'limit') return run.error;
  if (!did.length) return `${run.name}: ${(run.did || [])[0] || 'nothing to do'}`;
  return `${run.name}: ${did.slice(0, 2).join('; ')}${did.length > 2 ? ` and ${did.length - 2} more` : ''}`;
}
async function brRunCard(a, card, ctx, el) {
  if (el) el.disabled = true;
  try {
    const r = await post(brPath(ctx.board.id, `/${brEnc(a.id)}/run`), { card: card.id });
    toast(brRunText(r.run), r.run?.status === 'failed');
    await BOARDS.reload();
    try { BOARDS.openCard(ctx.board.id, card.id); } catch (e) { /* the card window closes instead */ }
    brLoad(ctx.board.id).catch(() => {});
  } catch (e) { toast(e.message, true); } finally { if (el) el.disabled = false; }
}
async function brRunBoard(id, el, confirmed, inPanel) {
  const ctx = brCtx(), a = (UI.brData?.autos || []).find(x => x.id === id);
  if (!ctx || !a) return;
  if (el) el.disabled = true;
  try {
    const r = await post(brPath(ctx.board.id, `/${brEnc(id)}/run`), { confirm: !!confirmed });
    if (r.needs_confirm) {
      if (inPanel) { UI.brAsk = 'run:' + id; UI.brAskCards = r.cards; brRenderBody(); return; }
      confirmModal(`Run “${esc(a.name)}” on ${r.cards} cards?`, `Board buttons that reach more than ${UI.brData.confirm_over || 20} cards ask first.`, `<p class="br-confirm">${brSentence(ctx, a)}</p>`, `Run on ${r.cards} cards`, async () => { closeModal(); await brRunBoard(id, null, true); });
      return;
    }
    UI.brAsk = '';
    toast(brRunText(r.run), r.run?.status === 'failed');
    BOARDS.reload();
    await brLoad(ctx.board.id);
    if (inPanel && $('br-root')) brRender();
  } catch (e) { toast(e.message, true); } finally { if (el) el.disabled = false; }
}

/* ---------- the panel ---------- */
async function brOpen(ctx, tab) {
  UI.brCtx = ctx || brCtx();
  if (!UI.brCtx?.board) return;
  UI.brTab = tab || UI.brTab || 'rule'; UI.brDraft = null; UI.brAsk = ''; UI.brLog = null;
  modal(`<div class="br" id="br-root" aria-label="Automation"><div class="br-wait">Loading automation…</div></div>`);
  document.querySelector('#mroot .md')?.classList.add('br-md');
  try { await brLoad(UI.brCtx.board.id); } catch (e) { const r = $('br-root'); if (r) r.innerHTML = `<div class="br-pad">${errHTML(e)}</div>`; return; }
  brRender();
  if (UI.brTab === 'log') brLoadLog();
}
function brCount(kind) { return (UI.brData?.autos || []).filter(a => a.kind === kind).length; }
function brRender() {
  const root = $('br-root'); if (!root) return;
  const ctx = brCtx(), d = UI.brData || { autos: [], runs_today: 0, cap: 1000 };
  const u = d.cap ? d.runs_today / d.cap : 0, uk = d.runs_today >= d.cap ? 'bad' : u >= .8 ? 'warn' : '';
  root.innerHTML = `<header class="br-hd">
      <div class="br-hd-t"><span class="br-mark">${BR_ICON.bolt}</span><div><h2>Automation</h2><p>Rules, buttons and commands for <b>${esc(ctx.board.name)}</b>. They act as Automation; the board’s activity names the one that acted.</p></div></div>
      <div class="br-use ${uk}" title="A board runs at most ${nf(d.cap)} automations a day"><small>Runs today</small><b>${nf(d.runs_today)} <span>of ${nf(d.cap)}</span></b><i><em style="width:${Math.min(100, u * 100).toFixed(1)}%"></em></i></div>
      <button class="x" data-act="close" aria-label="Close">×</button>
    </header>
    <nav class="br-tabs" role="tablist">${BR_TABS.map(([k, l]) => `<button role="tab" aria-selected="${UI.brTab === k}" class="${UI.brTab === k ? 'on' : ''}" data-act="brTab" data-id="${k}">${l}${k !== 'log' ? `<span class="br-n">${brCount(k)}</span>` : ''}</button>`).join('')}</nav>
    <div class="br-bd" id="br-bd">${brBody()}</div>`;
}
function brRenderBody() {
  const el = $('br-bd'); if (!el) return;
  const top = el.scrollTop; el.innerHTML = brBody(); el.scrollTop = top;
  brPreview();
}
function brBody() {
  if (UI.brTab === 'log') return brLogHTML();
  if (UI.brDraft) return brEditorHTML();
  return brListHTML(UI.brTab);
}

/* ---------- lists of rules, buttons and commands ---------- */
function brListHTML(kind) {
  const ctx = brCtx(), d = UI.brData, can = d.can_edit, items = d.autos.filter(a => a.kind === kind);
  const head = `<div class="br-intro"><p>${BR_INTRO[kind]}</p>${can ? `<button class="btn pri br-new" data-act="brNew">${BR_NEW[kind]}</button>` : `<span class="br-ro">You can read this board’s automation; its admins and members change it.</span>`}</div>`;
  if (!items.length) {
    const sug = can ? brSuggestions(ctx, kind) : [];
    return head + `<div class="br-empty"><h3>No ${BR_KIND[kind]}s yet</h3>${sug.length ? `<p>Start from one of these, or make your own.</p><div class="br-sugg">${sug.map((s, i) => `<button class="br-sg" data-act="brSuggest" data-id="${i}"><span>${brSentence(ctx, s)}</span><b>Use this</b></button>`).join('')}</div>` : ''}</div>`;
  }
  return head + `<div class="br-items">${items.map(a => brItemHTML(ctx, a, can)).join('')}</div>`;
}
function brItemHTML(ctx, a, can) {
  const runnable = a.kind === 'board_button' || a.kind === 'schedule';
  const meta = [`Made by ${esc(a.created_by)}`, a.runs ? `ran ${nf(a.runs)} time${a.runs === 1 ? '' : 's'}` : 'not run yet', a.last_run ? `last ${esc(brTime(a.last_run))}` : ''].filter(Boolean).join(' · ');
  let flag = '';
  if (a.last_status === 'failed') flag = `<div class="br-flag bad"><b>Last run failed.</b> ${esc(a.last_error)}</div>`;
  else if (a.stopped && a.stopped.slice(0, 10) === brToday()) flag = `<div class="br-flag warn"><b>Stopped by the loop guard ${esc(brTime(a.stopped))}.</b> Rules set each other off three times in a row; check for a loop.</div>`;
  else if (a.last_status === 'skipped') flag = `<div class="br-flag nt">Skipped ${esc(brTime(a.last_run))}: ${esc(a.last_error)}</div>`;
  let ask = '';
  if (UI.brAsk === 'del:' + a.id) ask = `<div class="br-ask"><span>Delete this ${BR_KIND[a.kind]}? Its past runs stay in the log.</span><button class="btn sm" data-act="brAskNo">Keep it</button><button class="btn sm br-danger" data-act="brDelYes" data-id="${esc(a.id)}">Delete</button></div>`;
  if (UI.brAsk === 'run:' + a.id) ask = `<div class="br-ask"><span>This runs on <b>${nf(UI.brAskCards)} cards</b>. Go ahead?</span><button class="btn sm" data-act="brAskNo">Cancel</button><button class="btn sm pri" data-act="brRunYes" data-id="${esc(a.id)}">Run on ${nf(UI.brAskCards)} cards</button></div>`;
  return `<article class="br-item ${a.on ? '' : 'off'}">
    <button class="br-sw ${a.on ? 'on' : ''}" role="switch" aria-checked="${a.on}" aria-label="${a.on ? 'Switch off' : 'Switch on'} ${esc(a.name)}" data-act="brToggle" data-id="${esc(a.id)}" ${can ? '' : 'disabled'}><i></i></button>
    <div class="br-item-m">
      <div class="br-item-n"><b>${esc(a.name)}</b>${a.on ? '' : '<span class="br-off">Off</span>'}</div>
      <p class="br-say">${brSentence(ctx, a)}</p>
      <div class="br-meta">${meta}</div>${flag}
    </div>
    <div class="br-item-a">${runnable && can && a.on ? `<button class="btn sm" data-act="brRunHere" data-id="${esc(a.id)}">${a.kind === 'schedule' ? 'Run now' : 'Run'}</button>` : ''}${can ? `<button class="btn sm" data-act="brEdit" data-id="${esc(a.id)}">Edit</button><button class="btn sm br-ghost" data-act="brDelAsk" data-id="${esc(a.id)}">Delete</button>` : ''}<button class="btn sm br-ghost" data-act="brLogFor" data-id="${esc(a.id)}">Log</button></div>
    ${ask}
  </article>`;
}
function brSuggestions(ctx, kind) {
  const first = brFindList(ctx, /^$/, 0), doing = brFindList(ctx, /doing|progress|review|check/i, 1), done = brFindList(ctx, /done|complete|closed|filed/i, 'last');
  const other = brPeople(ctx).find(u => u.id !== S.me?.actor && u.role !== 'observer');
  switch (kind) {
    case 'rule': return [
      { kind, name: '', trigger: { event: 'card_moved_in', list: done }, conditions: [], actions: [{ kind: 'mark_done' }] },
      { kind, name: '', trigger: { event: 'card_done' }, conditions: [{ kind: 'not_in_list', value: done }], actions: [{ kind: 'move', list: done }] },
      { kind, name: '', trigger: { event: 'card_added', list: first }, conditions: [{ kind: 'lacks_member', value: '' }], actions: [{ kind: 'join' }] }];
    case 'card_button': return [{ kind, name: 'Send for checking', conditions: [], actions: [{ kind: 'move', list: doing, top: true }, ...(other ? [{ kind: 'add_member', member: other.id }] : []), { kind: 'comment', text: 'Ready for checking, sent by {user}.' }] }];
    case 'board_button': return [{ kind, name: 'Archive finished cards', list: done, conditions: [{ kind: 'complete' }], actions: [{ kind: 'archive' }] }];
    case 'schedule': return [{ kind, name: 'Daily checks card', when: { every: 'working_day', at: '09:00' }, conditions: [], actions: [{ kind: 'create_card', list: first, text: 'Daily checks {date}', due: true, days: 0, time: '18:00', name: 'Checks', items: ['Trade files loaded', 'Reconciliation run', 'Margin file sent'] }] }];
    case 'due': return [{ kind, name: 'A day before it is due', due: { n: 1, unit: 'days', when: 'before' }, conditions: [], actions: [{ kind: 'notify', to: ['card_members'], text: '{card} is due {due}' }] }];
  }
  return [];
}

/* ---------- the editor ---------- */
function brNewDraft(kind) {
  const ctx = brCtx(), l0 = brFindList(ctx, /^$/, 0), done = brFindList(ctx, /done|complete|closed/i, 'last');
  const d = { kind, name: '', on: true, conditions: [], actions: [] };
  if (kind === 'rule') { d.trigger = { event: 'card_moved_in', list: done }; d.actions = [brDefAct('mark_done')]; }
  if (kind === 'card_button') d.actions = [brDefAct('move')];
  if (kind === 'board_button') { d.list = done; d.actions = [brDefAct('archive')]; }
  if (kind === 'schedule') { d.when = { every: 'working_day', at: '09:00', days: [1, 2, 3, 4, 5], day: 1 }; d.actions = [Object.assign(brDefAct('create_card'), { list: l0 })]; }
  if (kind === 'due') { d.due = { n: 1, unit: 'days', when: 'before', list: '', label: '' }; d.actions = [brDefAct('notify')]; }
  return d;
}
function brDefAct(kind) {
  const ctx = brCtx(), ls = brLists(ctx), l0 = ls[0]?.id || '', l1 = (ls[1] || ls[0])?.id || '', last = ls[ls.length - 1]?.id || '';
  const lab = brLabels(ctx)[0]?.id || '', who = brPeople(ctx)[0]?.id || '';
  switch (kind) {
    case 'move': case 'copy': return { kind, list: last, top: false };
    case 'add_label': case 'remove_label': return { kind, label: lab };
    case 'add_member': case 'remove_member': return { kind, member: who };
    case 'set_due': return { kind, days: 1, time: '17:00', working_days: false };
    case 'add_checklist': return { kind, name: 'Checklist', items: [] };
    case 'comment': return { kind, text: '' };
    case 'sort_list': return { kind, list: UI.brDraft?.kind === 'schedule' || (UI.brDraft?.kind === 'board_button' && !UI.brDraft.list) ? l0 : '', by: 'due' };
    case 'notify': return { kind, to: UI.brDraft?.kind === 'schedule' ? (who ? [who] : []) : ['card_members'], text: '' };
    case 'create_card': return { kind, list: l0, text: '', labels: [], members: [], due: false, days: 1, time: '', items: [], name: '' };
    case 'move_all': return { kind, from: l1, list: last };
    case 'archive_list': return { kind, list: last };
  }
  return { kind };
}
const brOpts = (opts, v) => opts.map(([k, l]) => `<option value="${esc(k)}" ${String(k) === String(v ?? '') ? 'selected' : ''}>${esc(l)}</option>`).join('');
const brSel = (path, v, opts, label, cls) => `<select class="in br-in ${cls || ''}" data-brf="${path}" aria-label="${esc(label)}">${brOpts(opts, v)}</select>`;
const brTxt = (path, v, ph, label, cls) => `<input class="in br-in ${cls || ''}" data-brf="${path}" value="${esc(v ?? '')}" placeholder="${esc(ph || '')}" aria-label="${esc(label)}">`;
const brNum = (path, v, min, max, label) => `<input class="in br-in br-num" type="number" min="${min}" max="${max}" data-brf="${path}" data-brt="num" value="${esc(v ?? 0)}" aria-label="${esc(label)}">`;
const brTimeIn = (path, v, label) => `<input class="in br-in br-time" data-brf="${path}" value="${esc(v ?? '')}" placeholder="HH:MM" maxlength="5" inputmode="numeric" aria-label="${esc(label)}">`;
const brChk = (path, v, text) => `<label class="br-chk"><input type="checkbox" data-brf="${path}" data-brt="bool" ${v ? 'checked' : ''}> ${text}</label>`;
const brMulti = (path, vals, opts, type) => `<div class="br-chips">${opts.map(([k, l, cls]) => `<label class="br-chip ${cls || ''} ${(vals || []).map(String).includes(String(k)) ? 'on' : ''}"><input type="checkbox" data-brf="${path}" data-brt="${type || 'multi'}" value="${esc(k)}" ${(vals || []).map(String).includes(String(k)) ? 'checked' : ''}> ${l}</label>`).join('')}</div>`;
const brListOpts = (ctx, extra) => [...(extra || []), ...brLists(ctx).map(l => [l.id, l.name])];
const brLabelOpts = (ctx, extra) => [...(extra || []), ...brLabels(ctx).map(l => [l.id, (l.name || '') + (l.name ? ' · ' : '') + l.color])];
const brWhoOpts = (ctx, extra) => [...(extra || []), ...brPeople(ctx).map(u => [u.id, `${u.name || u.id} (@${u.id})`])];

function brEditorHTML() {
  const ctx = brCtx(), d = UI.brDraft, kind = d.kind;
  const ll = brLedgerline(ctx);
  return `<div class="br-ed">
    <div class="br-ed-top"><button class="br-back" data-act="brCancel">${BR_TABS.find(t => t[0] === kind)[1]}</button><span>/</span><h3>${d.id ? 'Edit ' + esc(BR_KIND[kind]) : BR_NEW[kind]}</h3></div>
    <div class="br-name"><label for="br-name">Name</label><input class="in" id="br-name" data-brf="name" value="${esc(d.name)}" maxlength="120" placeholder="${kind.endsWith('button') ? 'The button’s label, e.g. Send for checking' : 'Optional: left empty, it is named from its sentence'}"></div>
    ${brWhenHTML(ctx, d)}
    ${kind === 'schedule' || kind === 'card_button' ? '' : brCondsHTML(ctx, d)}
    <section class="br-step"><div class="br-k">${kind === 'board_button' ? 'Do' : 'Then'}</div><div class="br-v">
      <ol class="br-rows br-acts">${d.actions.map((a, i) => brActRow(ctx, d, a, i)).join('')}</ol>
      <button class="br-add" data-act="brAddAct">Add an action</button>
      ${ll ? '<p class="br-hint">On a Ledgerline board cards follow their records: automation can label, assign, date, check off, comment on and sort cards, and notify people.</p>' : ''}
      ${d.actions.some(a => ['comment', 'notify', 'create_card', 'add_checklist'].includes(a.kind)) ? '<p class="br-hint">In text you can use {card}, {no}, {list}, {due}, {board}, {user} (who set it off) and {date} (today).</p>' : ''}
    </div></section>
    <div class="br-dock">
      <div class="br-prev"><small>Reads as</small><p id="br-prev" aria-live="polite"></p></div>
      <div id="br-err"></div>
      <div class="br-ed-ft"><button class="btn" data-act="brCancel">Cancel</button><button class="btn pri" data-act="brSave">${d.id ? 'Save changes' : 'Save ' + BR_KIND[kind]}</button></div>
    </div>
  </div>`;
}
function brWhenHTML(ctx, d) {
  const lists = brListOpts(ctx);
  if (d.kind === 'rule') {
    const t = d.trigger || {}, ev = t.event;
    let p = '';
    if (ev === 'card_added') p = brSel('trigger.list', t.list, brListOpts(ctx, [['', 'the board (any list)']]), 'List');
    if (ev === 'card_moved_in' || ev === 'card_moved_out') p = brSel('trigger.list', t.list, brListOpts(ctx, [['', 'any list']]), 'List');
    if (ev === 'label_added' || ev === 'label_removed') p = `<span class="br-w">label</span>${brSel('trigger.label', t.label, brLabelOpts(ctx, [['', 'any label']]), 'Label')}`;
    if (ev === 'member_added') p = `<span class="br-w">who</span>${brSel('trigger.member', t.member, brWhoOpts(ctx, [['', 'anyone']]), 'Person')}`;
    if (ev === 'comment_posted') p = `<span class="br-w">containing</span>${brTxt('trigger.words', t.words, 'any words (optional)', 'Words', 'br-wide')}`;
    return `<section class="br-step"><div class="br-k">When</div><div class="br-v"><div class="br-line">${brSel('trigger.event', ev, BR_EVENTS, 'What sets it off')}${p}${brSel('trigger.by', t.by, brWhoOpts(ctx, [['', 'by anyone']]).map(([k, l]) => [k, k ? 'by ' + l : l]), 'By whom')}</div></div></section>`;
  }
  if (d.kind === 'card_button') return `<section class="br-step"><div class="br-k">Where</div><div class="br-v"><p class="br-fixed">In the card window of every card on this board, under Actions.</p></div></section>`;
  if (d.kind === 'board_button') return `<section class="br-step"><div class="br-k">On</div><div class="br-v"><div class="br-line"><span class="br-w">each open card in</span>${brSel('list', d.list, brListOpts(ctx, [['', 'every list of the board']]), 'Cards of')}</div><p class="br-hint">It asks before acting on more than ${UI.brData?.confirm_over || 20} cards.</p></div></section>`;
  if (d.kind === 'schedule') {
    const w = d.when || {};
    let p = '';
    if (w.every === 'weekdays') p = brMulti('when.days', w.days, BR_WD.map(([k, l]) => [k, l]), 'imulti');
    if (w.every === 'month_day') p = `<span class="br-w">day</span>${brNum('when.day', w.day || 1, 1, 31, 'Day of the month')}`;
    const skip = ['day', 'weekday', 'weekdays', 'month_day'].includes(w.every) ? brChk('when.skip_holidays', w.skip_holidays, 'Not on the firm’s trading holidays') : '';
    return `<section class="br-step"><div class="br-k">When</div><div class="br-v"><div class="br-line">${brSel('when.every', w.every, BR_EVERY, 'How often')}${p}<span class="br-w">at</span>${brTimeIn('when.at', w.at, 'Time (IST)')}<span class="br-w">IST</span></div>${skip ? `<div class="br-line">${skip}</div>` : ''}${w.every === 'month_day' && +w.day > 28 ? '<p class="br-hint">In shorter months it runs on the last day.</p>' : ''}</div></section>`;
  }
  if (d.kind === 'due') {
    const x = d.due || {};
    return `<section class="br-step"><div class="br-k">When</div><div class="br-v"><div class="br-line">${brNum('due.n', x.n, 0, 1440, 'How long')}${brSel('due.unit', x.unit, [['minutes', 'minutes'], ['hours', 'hours'], ['days', 'days']], 'Unit')}${brSel('due.when', x.when, [['before', 'before'], ['after', 'after']], 'Before or after')}<span class="br-w">a card is due</span></div>
      <div class="br-line"><span class="br-w">only cards in</span>${brSel('due.list', x.list, brListOpts(ctx, [['', 'any list']]), 'List')}<span class="br-w">with</span>${brSel('due.label', x.label, brLabelOpts(ctx, [['', 'any labels']]), 'Label')}${brChk('due.done', x.done, 'complete cards too')}</div>
      <p class="br-hint">It runs once for each card and due date, from now on. A due date without a time counts as 23:59.</p></div></section>`;
  }
  return '';
}
function brCondsHTML(ctx, d) {
  const cs = d.conditions || [];
  return `<section class="br-step"><div class="br-k">Only if</div><div class="br-v">
    ${cs.length ? `<ul class="br-rows">${cs.map((c, i) => `<li class="br-row"><span class="br-and">${i ? 'and' : 'the card:'}</span><div class="br-line">${brSel(`conditions.${i}.kind`, c.kind, BR_CONDS, 'Condition')}${brCondValue(ctx, c, i)}</div><button class="br-rm" data-act="brDelCond" data-id="${i}" aria-label="Remove the condition">Remove</button></li>`).join('')}</ul>` : '<p class="br-fixed">Every card, unless you add a condition.</p>'}
    <button class="br-add" data-act="brAddCond">Add a condition</button></div></section>`;
}
function brCondValue(ctx, c, i) {
  const p = `conditions.${i}.value`;
  switch (c.kind) {
    case 'has_label': case 'lacks_label': return brSel(p, c.value, brLabelOpts(ctx), 'Label');
    case 'has_member': case 'lacks_member': return brSel(p, c.value, brWhoOpts(ctx, [['', 'anyone']]), 'Person');
    case 'in_list': case 'not_in_list': return brSel(p, c.value, brListOpts(ctx), 'List');
    case 'title_contains': return brTxt(p, c.value, 'words', 'Words', 'br-wide');
  }
  return '';
}
function brActRow(ctx, d, a, i) {
  const ll = brLedgerline(ctx), sched = d.kind === 'schedule';
  const kinds = BR_ACTS.filter(([k, , w]) => !(sched && w === 'card') && !(ll && BR_NOT_LL.includes(k))).map(([k, l]) => [k, l]);
  const n = d.actions.length;
  return `<li class="br-row br-act"><span class="br-no">${i + 1}</span><div class="br-act-b"><div class="br-line">${brSel(`actions.${i}.kind`, a.kind, kinds, 'Action')}${brActParams(ctx, d, a, i)}</div>${brActMore(ctx, d, a, i)}</div>
    <div class="br-mv"><button class="br-ic" data-act="brActUp" data-id="${i}" ${i ? '' : 'disabled'} aria-label="Move up">Up</button><button class="br-ic" data-act="brActDown" data-id="${i}" ${i < n - 1 ? '' : 'disabled'} aria-label="Move down">Down</button><button class="br-rm" data-act="brDelAct" data-id="${i}" ${n > 1 ? '' : 'disabled'} aria-label="Remove the action">Remove</button></div></li>`;
}
function brActParams(ctx, d, a, i) {
  const p = k => `actions.${i}.${k}`, lists = brListOpts(ctx);
  switch (a.kind) {
    case 'move': case 'copy': return `${brSel(p('list'), a.list, lists, 'List')}${brSel(p('top'), a.top ? '1' : '', [['', 'at the bottom'], ['1', 'at the top']], 'Where in the list')}`;
    case 'add_label': case 'remove_label': return brSel(p('label'), a.label, brLabelOpts(ctx), 'Label');
    case 'add_member': case 'remove_member': return brSel(p('member'), a.member, brWhoOpts(ctx), 'Person');
    case 'set_due': return `<span class="br-w">in</span>${brNum(p('days'), a.days, 0, 365, 'Days')}${brSel(p('working_days'), a.working_days ? '1' : '', [['', 'days'], ['1', 'working days']], 'Days or working days')}<span class="br-w">at</span>${brTimeIn(p('time'), a.time, 'Time')}`;
    case 'add_checklist': return brTxt(p('name'), a.name, 'Checklist name', 'Checklist name');
    case 'sort_list': return `${brSel(p('list'), a.list, d.kind === 'schedule' || (d.kind === 'board_button' && !d.list) ? lists : brListOpts(ctx, [['', d.kind === 'board_button' ? 'the button’s list' : 'the card’s list']]), 'List')}<span class="br-w">by</span>${brSel(p('by'), a.by, BR_SORTS, 'Sort by')}`;
    case 'create_card': return `<span class="br-w">in</span>${brSel(p('list'), a.list, lists, 'List')}${brSel(p('top'), a.top ? '1' : '', [['', 'at the bottom'], ['1', 'at the top']], 'Where')}`;
    case 'move_all': return `<span class="br-w">from</span>${brSel(p('from'), a.from, lists, 'From list')}<span class="br-w">to</span>${brSel(p('list'), a.list, lists, 'To list')}`;
    case 'archive_list': return brSel(p('list'), a.list, lists, 'List');
  }
  return '';
}
function brActMore(ctx, d, a, i) {
  const p = k => `actions.${i}.${k}`;
  switch (a.kind) {
    case 'add_checklist': return `<textarea class="in br-in br-area" rows="3" data-brf="${p('items')}" data-brt="lines" placeholder="One item a line" aria-label="Checklist items">${esc((a.items || []).join('\n'))}</textarea>`;
    case 'comment': return `<textarea class="in br-in br-area" rows="2" data-brf="${p('text')}" placeholder="e.g. {card} is ready for checking" aria-label="Comment">${esc(a.text || '')}</textarea>`;
    case 'notify': {
      const who = d.kind === 'schedule' ? [] : [['card_members', 'The card’s members'], ['watchers', 'Its watchers']];
      return `${brMulti(p('to'), a.to, [...who.map(([k, l]) => [k, l, 'br-chip-k']), ...brPeople(ctx).map(u => [u.id, '@' + esc(u.id)])])}${brTxt(p('text'), a.text, 'Message (optional): e.g. {card} needs you', 'Message', 'br-full')}`;
    }
    case 'create_card': return `<div class="br-sub">${brTxt(p('text'), a.text, 'Card title, e.g. Daily recon {date}', 'Card title', 'br-full')}
      ${brLabels(ctx).length ? `<div class="br-subl"><span>Labels</span>${brMulti(p('labels'), a.labels, brLabels(ctx).map(l => [l.id, esc(l.name || l.color), 'br-lc-' + l.color]))}</div>` : ''}
      <div class="br-subl"><span>Members</span>${brMulti(p('members'), a.members, brPeople(ctx).map(u => [u.id, '@' + esc(u.id)]))}</div>
      <div class="br-line">${brChk(p('due'), a.due, 'Due')}${a.due ? `<span class="br-w">in</span>${brNum(p('days'), a.days, 0, 365, 'Days')}${brSel(p('working_days'), a.working_days ? '1' : '', [['', 'days'], ['1', 'working days']], 'Days or working days')}<span class="br-w">at</span>${brTimeIn(p('time'), a.time, 'Time')}` : ''}</div>
      <div class="br-subl"><span>Checklist</span>${brTxt(p('name'), a.name, 'Checklist name (optional)', 'Checklist name')}</div>
      <textarea class="in br-in br-area" rows="3" data-brf="${p('items')}" data-brt="lines" placeholder="Checklist items, one a line (optional)" aria-label="Checklist items">${esc((a.items || []).join('\n'))}</textarea></div>`;
  }
  return '';
}
function brPreview() { const el = $('br-prev'); if (el && UI.brDraft) el.innerHTML = brSentence(brCtx(), UI.brDraft); }

/* ---------- editing: the draft follows the form ---------- */
function brSet(path, v) {
  const ks = path.split('.'); let o = UI.brDraft;
  for (let i = 0; i < ks.length - 1; i++) { if (o[ks[i]] == null) o[ks[i]] = /^\d+$/.test(ks[i + 1]) ? [] : {}; o = o[ks[i]]; }
  o[ks[ks.length - 1]] = v;
}
function brRead(el) {
  const t = el.dataset.brt, path = el.dataset.brf;
  if (t === 'num') return parseInt(el.value, 10) || 0;
  if (t === 'bool') return el.checked;
  if (t === 'lines') return el.value.split('\n');
  if (t === 'multi' || t === 'imulti') return [...document.querySelectorAll(`#br-root [data-brf="${path}"]:checked`)].map(x => t === 'imulti' ? +x.value : x.value);
  if (/\.(top|working_days)$/.test(path)) return el.value === '1';
  return el.value;
}
function brChanged(el, structural) {
  const path = el.dataset.brf, v = brRead(el);
  let m;
  if ((m = /^actions\.(\d+)\.kind$/.exec(path))) UI.brDraft.actions[+m[1]] = brDefAct(v);
  else if ((m = /^conditions\.(\d+)\.kind$/.exec(path))) { const ctx = brCtx(); UI.brDraft.conditions[+m[1]] = { kind: v, value: /label/.test(v) ? brLabels(ctx)[0]?.id || '' : /list/.test(v) ? brLists(ctx)[0]?.id || '' : '' }; }
  else brSet(path, v);
  if (el.type === 'checkbox' && el.closest('.br-chip')) el.closest('.br-chip').classList.toggle('on', el.checked);
  if (structural) brRenderBody(); else brPreview();
}
document.addEventListener('input', e => { const el = e.target; if (!el.dataset?.brf || !el.closest('#br-root') || !UI.brDraft) return; if (el.tagName === 'SELECT' || el.type === 'checkbox') return; brChanged(el, false); });
document.addEventListener('change', e => {
  const el = e.target;
  if (el.id === 'br-logfor' && el.closest('#br-root')) { UI.brLogAuto = el.value; brLoadLog(); return; }
  if (!el.dataset?.brf || !el.closest('#br-root') || !UI.brDraft) return;
  // Text boxes update the sentence as one types; re-drawing on their change would eat the click that follows.
  // A chip only changes its own look; a select or a tick box can change which controls show.
  if (el.tagName === 'SELECT' || el.type === 'checkbox') brChanged(el, !el.closest('.br-chip'));
});

function brPayload(d) {
  const ctx = brCtx();
  const str = v => String(v ?? '').trim();
  const act = a => {
    const x = { kind: a.kind };
    ['list', 'from', 'label', 'member', 'time', 'name', 'text', 'by'].forEach(k => { if (a[k] != null && str(a[k]) !== '') x[k] = k === 'text' ? String(a[k]) : str(a[k]); });
    ['top', 'working_days', 'due'].forEach(k => { if (a[k]) x[k] = true; });
    if (a.days) x.days = +a.days || 0;
    ['items', 'to', 'labels', 'members'].forEach(k => { if (Array.isArray(a[k])) x[k] = a[k].map(str).filter(Boolean); });
    return x;
  };
  const p = { kind: d.kind, name: str(d.name) || brClip(brPlain(brSentence(ctx, d)).replace(/\.$/, ''), 120), conditions: (d.conditions || []).map(c => ({ kind: c.kind, value: str(c.value) })), actions: (d.actions || []).map(act) };
  if (d.kind === 'rule') { const t = d.trigger || {}; p.trigger = { event: t.event }; ['list', 'label', 'member', 'words', 'by'].forEach(k => { if (str(t[k])) p.trigger[k] = str(t[k]); }); }
  if (d.kind === 'board_button' && d.list) p.list = d.list;
  if (d.kind === 'schedule') { const w = d.when || {}; p.when = { every: w.every, at: str(w.at) }; if (w.every === 'weekdays') p.when.days = (w.days || []).map(Number); if (w.every === 'month_day') p.when.day = +w.day || 0; if (w.skip_holidays) p.when.skip_holidays = true; delete p.conditions; }
  if (d.kind === 'due') { const x = d.due || {}; p.due = { n: +x.n || 0, unit: x.unit || 'days', when: x.when || 'before' }; if (x.list) p.due.list = x.list; if (x.label) p.due.label = x.label; if (x.done) p.due.done = true; }
  return p;
}

/* ---------- the log ---------- */
async function brLoadLog(more) {
  const ctx = brCtx(); if (!ctx) return;
  const before = more && UI.brLog?.length ? UI.brLog[UI.brLog.length - 1].seq : 0;
  try {
    const rows = await get(brPath(ctx.board.id, `/log?limit=100${UI.brLogAuto ? '&auto=' + brEnc(UI.brLogAuto) : ''}${before ? '&before=' + before : ''}`));
    UI.brLog = more ? [...(UI.brLog || []), ...rows] : rows; UI.brLogMore = rows.length === 100;
  } catch (e) { UI.brLog = []; toast(e.message, true); }
  if (UI.brTab === 'log') brRenderBody();
}
function brLogHTML() {
  const d = UI.brData, rows = UI.brLog;
  const names = {}; d.autos.forEach(a => { names[a.id] = a.name; });
  const opts = [['', 'Every rule, button and command'], ...d.autos.map(a => [a.id, `${a.name} (${BR_KIND[a.kind]})`])];
  if (UI.brLogAuto && !names[UI.brLogAuto]) opts.push([UI.brLogAuto, 'A deleted automation']);
  const head = `<div class="br-intro"><p>${BR_INTRO.log}</p><select class="in" id="br-logfor" aria-label="Show the runs of">${brOpts(opts, UI.brLogAuto)}</select></div>`;
  if (!rows) return head + '<div class="br-wait">Loading the log…</div>';
  if (!rows.length) return head + `<div class="br-empty"><h3>Nothing has run yet</h3><p>Runs show here as soon as a rule, button or command acts.</p></div>`;
  return head + `<div class="br-log"><table><thead><tr><th>When</th><th>Automation</th><th>Card</th><th>Set off by</th><th>What it did</th><th>Result</th></tr></thead><tbody>${rows.map(r => {
    const st = BR_STATUS[r.status] || [r.status, 'br-st-nt'];
    const did = (r.did || []).length ? `<ul>${r.did.slice(0, 6).map(x => `<li>${esc(x)}</li>`).join('')}${r.did.length > 6 ? `<li class="br-dim">and ${r.did.length - 6} more</li>` : ''}</ul>` : '';
    return `<tr><td class="br-nowrap">${esc(brTime(r.at))}</td><td><b>${esc(r.name)}</b><div class="br-dim">${esc(BR_KIND[r.kind] || r.kind)}</div></td>
      <td>${r.card_no ? `<span class="br-cno">#${r.card_no}</span> ${esc(brClip(r.title, 60))}` : r.cards ? `${nf(r.cards)} card${r.cards === 1 ? '' : 's'}` : '<span class="br-dim">—</span>'}</td>
      <td>${esc(r.why)}</td><td>${did}${r.error ? `<div class="br-err-t ${r.status === 'skipped' ? 'nt' : ''}">${esc(r.error)}</div>` : ''}</td><td><span class="br-st ${st[1]}">${esc(st[0])}</span></td></tr>`;
  }).join('')}</tbody></table></div>${UI.brLogMore ? '<div class="br-more"><button class="btn sm" data-act="brLogMore">Show older runs</button></div>' : ''}`;
}

/* ---------- actions ---------- */
Object.assign(A, {
  brOpen: () => brOpen(brCtx()),
  brTab: id => { UI.brTab = id; UI.brDraft = null; UI.brAsk = ''; if (id === 'log') UI.brLog = null; brRender(); if (id === 'log') brLoadLog(); },
  brNew: () => { UI.brDraft = brNewDraft(UI.brTab); UI.brAsk = ''; brRenderBody(); $('br-name')?.focus(); },
  brSuggest: id => { const s = brSuggestions(brCtx(), UI.brTab)[+id]; if (!s) return; UI.brDraft = JSON.parse(JSON.stringify(s)); brRenderBody(); },
  brEdit: id => { const a = UI.brData.autos.find(x => x.id === id); if (!a) return; UI.brDraft = JSON.parse(JSON.stringify(a)); UI.brDraft.conditions ||= []; if (a.kind === 'schedule') UI.brDraft.when = Object.assign({ days: [1, 2, 3, 4, 5], day: 1 }, a.when); brRenderBody(); },
  brCancel: () => { UI.brDraft = null; brRenderBody(); },
  brAddCond: () => { const ctx = brCtx(); UI.brDraft.conditions = [...(UI.brDraft.conditions || []), { kind: 'has_label', value: brLabels(ctx)[0]?.id || '' }]; brRenderBody(); },
  brDelCond: id => { UI.brDraft.conditions.splice(+id, 1); brRenderBody(); },
  brAddAct: () => { const d = UI.brDraft; d.actions.push(brDefAct(d.kind === 'schedule' ? 'create_card' : 'add_label')); brRenderBody(); },
  brDelAct: id => { UI.brDraft.actions.splice(+id, 1); brRenderBody(); },
  brActUp: id => { const a = UI.brDraft.actions, i = +id; if (i > 0) { [a[i - 1], a[i]] = [a[i], a[i - 1]]; brRenderBody(); } },
  brActDown: id => { const a = UI.brDraft.actions, i = +id; if (i < a.length - 1) { [a[i + 1], a[i]] = [a[i], a[i + 1]]; brRenderBody(); } },
  brSave: async (id, el) => {
    const ctx = brCtx(), d = UI.brDraft; el.disabled = true;
    try {
      const body = brPayload(d);
      const a = d.id ? await api('PUT', brPath(ctx.board.id, '/' + brEnc(d.id)), body) : await post(brPath(ctx.board.id), body);
      toast(`${d.id ? 'Saved' : 'Made'} the ${BR_KIND[a.kind]} “${a.name}”`);
      UI.brDraft = null; await brLoad(ctx.board.id); brRender();
    } catch (e) { const err = $('br-err'); if (err) { err.innerHTML = errHTML(e); err.scrollIntoView({ block: 'nearest' }); } el.disabled = false; }
  },
  brToggle: async (id, el) => {
    const ctx = brCtx(), a = UI.brData.autos.find(x => x.id === id); if (!a) return; el.disabled = true;
    try { await post(brPath(ctx.board.id, `/${brEnc(id)}/on`), { on: !a.on }); toast(`${a.name} is ${a.on ? 'off' : 'on'}`); await brLoad(ctx.board.id); brRender(); }
    catch (e) { toast(e.message, true); el.disabled = false; }
  },
  brDelAsk: id => { UI.brAsk = 'del:' + id; brRenderBody(); },
  brAskNo: () => { UI.brAsk = ''; brRenderBody(); },
  brDelYes: async (id, el) => {
    const ctx = brCtx(); el.disabled = true;
    try { await post(brPath(ctx.board.id, `/${brEnc(id)}/delete`)); UI.brAsk = ''; toast('Deleted'); await brLoad(ctx.board.id); brRender(); }
    catch (e) { toast(e.message, true); el.disabled = false; }
  },
  brRunHere: (id, el) => brRunBoard(id, el, false, true),
  brRunYes: (id, el) => brRunBoard(id, el, true, true),
  brBoardRun: (id, el) => brRunBoard(id, el, false, false),
  brLogFor: id => { UI.brTab = 'log'; UI.brLogAuto = id; UI.brDraft = null; UI.brLog = null; brRender(); brLoadLog(); },
  brLogMore: () => brLoadLog(true),
});
