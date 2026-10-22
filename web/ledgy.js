/* Ledgy: the helper panel. Answers "how do I…" and "why can't I…" from the
   help articles, with live checks on the firm's data, and links straight to
   the screen. Loaded after app.js and uses its helpers ($, esc, get, post,
   toast, A, S). */
'use strict';
const LG = { open: false, busy: false, log: [], view: '', sugg: {}, gaps: null };

function lgRoot() {
  let r = $('ledgy');
  if (!r) {
    r = document.createElement('div'); r.id = 'ledgy'; document.body.appendChild(r);
  }
  return r;
}
function lgAllowed() { return !!S.me && S.me.role !== 'Client' && !setupOpen && !S.me.auth?.must_change_password && !S.me.auth?.totp_setup_required; }

// Small markdown: numbered and bulleted lists (one level of nesting),
// **bold**, `code`, and [[screen|Label]] links that open a screen.
function lgInline(s) {
  return esc(s)
    .replace(/\[\[([a-z]+)\|([^\]]+)\]\]/g, (m, v, l) => `<button class="lg-go" data-act="ledgyGo" data-id="${v}">${l}</button>`)
    .replace(/\*\*([^*]+)\*\*/g, '<b>$1</b>')
    .replace(/`([^`]+)`/g, '<code>$1</code>');
}
function lgMD(src) {
  const out = []; let list = null, sub = null;
  const close = () => { if (sub) { list.items[list.items.length - 1] += `<ul>${sub.join('')}</ul>`; sub = null; } if (list) { out.push(`<${list.t}>${list.items.map(i => `<li>${i}</li>`).join('')}</${list.t}>`); list = null; } };
  for (const raw of String(src || '').split('\n')) {
    const ind = raw.match(/^\s*/)[0].length, l = raw.trim();
    const num = l.match(/^\d+\.\s+(.*)$/), bul = l.match(/^-\s+(.*)$/);
    if (!l) { close(); continue; }
    if (ind >= 2 && list && (num || bul)) { (sub ||= []).push(`<li>${lgInline((num || bul)[1])}</li>`); continue; }
    if (num || bul) {
      const t = num ? 'ol' : 'ul';
      if (sub) { list.items[list.items.length - 1] += `<ul>${sub.join('')}</ul>`; sub = null; }
      if (list && list.t !== t) close();
      if (!list) list = { t, items: [] };
      list.items.push(lgInline((num || bul)[1])); continue;
    }
    close(); out.push(`<p>${lgInline(l)}</p>`);
  }
  close();
  return out.join('');
}

const lgName = () => ((S.me?.users || []).find(u => u.id === S.me.actor)?.name || '').split(' ')[0];
const LG_VIEW_NAMES = () => Object.fromEntries(NAV.flatMap(g => g[1]));

function lgArticle(a, opts = {}) {
  const names = LG_VIEW_NAMES();
  return `<div class="lg-art">
    <div class="lg-ah"><b>${esc(a.title)}</b>${a.roles?.length ? `<small>${esc(a.roles.join(' · '))}</small>` : ''}</div>
    <div class="lg-body">${lgMD(a.body)}</div>
    ${a.screen && names[a.screen] ? `<button class="btn sm pri" data-act="ledgyGo" data-id="${esc(a.screen)}">Open ${esc(names[a.screen])}</button>` : ''}
    ${opts.feedback ? `<div class="lg-fb" data-q="${esc(opts.q)}" data-a="${esc(a.id)}"><span>Did this help?</span><button class="linkish" data-act="ledgyFB" data-id="1">Yes</button><button class="linkish" data-act="ledgyFB" data-id="0">No</button></div>` : ''}
  </div>`;
}

function lgFacts(fs) {
  return (fs || []).map(f => `<div class="lg-facts"><div class="lg-fh">${esc(f.title)}</div>${f.lines.map(l => `<div class="lg-fl ${l.state}"><i></i><span>${esc(l.text)}</span>${l.ucc ? `<button class="linkish small" data-act="client" data-id="${esc(l.ucc)}">Open</button>` : l.screen ? `<button class="linkish small" data-act="ledgyGo" data-id="${esc(l.screen)}">Go</button>` : ''}</div>`).join('')}</div>`).join('');
}

function lgLinks(title, ls) {
  return ls?.length ? `<div class="lg-links"><small>${title}</small>${ls.map(l => `<button class="lg-chip" data-act="ledgyArt" data-id="${esc(l.id)}">${esc(l.title)}</button>`).join('')}</div>` : '';
}

function lgAnswerHTML(m) {
  const a = m.answer;
  if (m.article) return lgArticle(m.article, { feedback: false });
  let h = '';
  if (a.ai) h += `<div class="lg-ai">${lgMD(a.ai)}<small>Written by ${esc(a.ai_by)} from the help articles below.</small></div>`;
  if (a.ai_note) h += `<div class="lg-note">${esc(a.ai_note)}</div>`;
  h += lgFacts(a.facts);
  if (a.perms?.length) h += `<div class="lg-facts"><div class="lg-fh">Who can do it</div>${a.perms.map(p => `<div class="lg-fl info"><i></i><span><b>${esc(p.what)}:</b> ${esc(p.roles.join(' or '))}${p.roles.includes('Admin') ? '' : ' (and Admin)'}</span></div>`).join('')}<div class="lg-note">Your role: ${esc(a.role)}</div></div>`;
  if (a.article) h += lgArticle(a.article, { feedback: true, q: a.question });
  if (!a.confident) {
    h += `<div class="lg-unsure"><p>I'm not sure I have this one.${a.others.length ? ' Is it one of these?' : ''}</p>${lgLinks('', a.others)}<p class="lg-note">I've noted the question so the help can be improved.</p></div>`;
    if (a.suggest?.length) h += `<div class="lg-links"><small>Or try</small>${a.suggest.map(q => `<button class="lg-chip" data-act="ledgyQ" data-id="${esc(q)}">${esc(q)}</button>`).join('')}</div>`;
  } else {
    h += lgLinks('Also see', [...(a.others || []), ...(a.related || [])].filter((x, i, arr) => arr.findIndex(y => y.id === x.id) === i && x.id !== a.article?.id).slice(0, 5));
  }
  return h;
}

function lgRender() {
  const r = lgRoot();
  if (!lgAllowed()) { r.innerHTML = ''; return; }
  const names = LG_VIEW_NAMES(), vn = names[S.view] || 'this screen';
  if (!LG.open) { r.innerHTML = `<button class="lg-fab" data-act="ledgyOpen" aria-label="Ask Ledgy for help" title="Ask Ledgy (?)"><span class="lg-mark">L</span><span>Ask Ledgy</span></button>`; return; }
  const sugg = LG.sugg[S.view] || [];
  const canGaps = ['Admin', 'Compliance officer'].includes(S.me.role);
  r.innerHTML = `<section class="lg-panel" role="dialog" aria-label="Ledgy help">
    <header class="lg-head"><span class="lg-mark">L</span><div><b>Ledgy</b><small>Help with ${esc(vn)} and anything else in Ledgerline</small></div>
      ${canGaps ? `<button class="linkish small" data-act="ledgyGaps">${LG.gaps ? 'Back' : 'Unanswered'}</button>` : ''}
      <button class="lg-x" data-act="ledgyClose" aria-label="Close Ledgy">×</button></header>
    <div class="lg-scroll" id="lg-scroll">${LG.gaps ? lgGapsHTML() : (LG.log.length ? LG.log.map(m => m.q !== undefined ? `<div class="lg-q">${esc(m.q)}</div>` : `<div class="lg-a">${m.err ? `<div class="errbox">${esc(m.err)}</div>` : lgAnswerHTML(m)}</div>`).join('') : `<div class="lg-hello"><p>Hi${lgName() ? ' ' + esc(lgName()) : ''}. I know every screen in Ledgerline. Ask me how to do something, or what to do when you're stuck. Paste an error message and I'll explain it.</p><p class="lg-note">I can also check live: "why can't I close the day?", "why is BH1003 blocked?", "what's in my queue?", "who can approve payouts?"</p></div>`)}
      ${LG.busy ? '<div class="lg-a"><p class="loading">Looking…</p></div>' : ''}</div>
    ${LG.gaps ? '' : `${sugg.length && !LG.log.length ? `<div class="lg-sugg">${sugg.map(q => `<button class="lg-chip" data-act="ledgyQ" data-id="${esc(q)}">${esc(q)}</button>`).join('')}</div>` : ''}
    <form class="lg-form" id="lg-form"><input class="in" id="lg-in" placeholder="Ask anything, or paste an error" autocomplete="off" maxlength="500"><button class="btn pri" type="submit" ${LG.busy ? 'disabled' : ''}>Ask</button></form>`}
  </section>`;
  const sc = $('lg-scroll'); if (sc) sc.scrollTop = sc.scrollHeight;
  const f = $('lg-form');
  if (f) { f.onsubmit = e => { e.preventDefault(); const q = $('lg-in').value.trim(); if (q) lgAsk(q); }; if (!LG.busy) setTimeout(() => $('lg-in')?.focus(), 10); }
  if (!LG.sugg[S.view]) get(`/v1/ledgy/suggest?view=${encodeURIComponent(S.view)}`).then(r => { LG.sugg[S.view] = r.questions || []; if (LG.open && !LG.log.length) lgRender(); }).catch(() => {});
}

function lgGapsHTML() {
  const g = LG.gaps;
  if (!g.length) return '<div class="lg-hello"><p>No unanswered questions yet.</p></div>';
  return `<div class="lg-hello"><p>Questions Ledgy couldn't answer, or answers marked unhelpful, most asked first. Use them to improve the help articles (config/help).</p></div>` +
    g.map(x => `<div class="lg-gap"><div>${esc(x.question)}</div><small>${x.asked ? `asked ${x.asked}×` : ''}${x.unhelpful ? ` · not helpful ${x.unhelpful}×` : ''}${x.best ? ` · closest: ${esc(x.best)}` : ''}</small></div>`).join('');
}

async function lgAsk(q) {
  LG.gaps = null; LG.log.push({ q }); LG.busy = true; lgRender();
  try { const a = await post('/v1/ledgy/ask', { q, view: S.view, date: S.date }); LG.log.push({ answer: a }); }
  catch (e) { LG.log.push({ err: e.message }); }
  LG.busy = false; if (LG.log.length > 40) LG.log = LG.log.slice(-40); lgRender();
}

Object.assign(A, {
  ledgyOpen: () => { LG.open = true; lgRender(); },
  ledgyClose: () => { LG.open = false; lgRender(); },
  ledgyQ: q => lgAsk(q),
  ledgyAsk: (id, el) => { LG.open = true; lgAsk('I got this error: ' + el.dataset.q); },
  ledgyGo: id => { S.view = id; closeModal(); render(); window.scrollTo(0, 0); },
  ledgyArt: async id => { try { const a = await get(`/v1/ledgy/articles/${encodeURIComponent(id)}`); LG.log.push({ q: a.title }); LG.log.push({ article: a, answer: {} }); lgRender(); } catch (e) { toast(e.message); } },
  ledgyFB: async (id, el) => {
    const box = el.closest('.lg-fb'), helpful = id === '1';
    try { await post('/v1/ledgy/feedback', { q: box.dataset.q, view: S.view, article: box.dataset.a, helpful }); box.innerHTML = helpful ? '<span>Thanks.</span>' : '<span>Thanks. I\'ve noted it so this answer can be improved.</span>'; } catch (e) { toast(e.message); }
  },
  ledgyGaps: async () => { if (LG.gaps) { LG.gaps = null; lgRender(); return; } try { LG.gaps = await get('/v1/ledgy/gaps'); lgRender(); } catch (e) { toast(e.message); } },
});

// Keep the button and panel in step with sign-in and screen changes.
const lgRenderApp = render;
render = async function (opts) { const r = await lgRenderApp(opts); lgRender(); return r; };
document.addEventListener('keydown', e => {
  if (e.key === '?' && !/INPUT|TEXTAREA|SELECT/.test(document.activeElement.tagName) && lgAllowed()) { e.preventDefault(); LG.open = true; lgRender(); }
  if (e.key === 'Escape' && LG.open && document.activeElement?.id === 'lg-in') { LG.open = false; lgRender(); }
});
lgRender();
