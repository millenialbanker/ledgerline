'use strict';
/* Ledgerline console: requests to Ledgerline's support team. Opened from the
   user menu. A request carries the firm, the person, their role, the screen
   they were on and the version; replies and status come back here and
   Ledgy tells the person. Loaded after app.js. */

const SP = { open: '', from: '' };
const spPill = s => pill(s, { Sent: 'nt', Open: 'acc', 'Waiting on you': 'warn', Resolved: 'good', Closed: 'nt' }[s] || 'nt');
const spPri = p => pill(p, { Urgent: 'bad', High: 'warn', Normal: 'nt', Low: 'nt' }[p] || 'nt');

V.support = {
  load: () => get('/v1/support'),
  html: h => {
    const r = SP.open && h.requests.find(x => x.id === SP.open);
    if (r) return spThread(h, r);
    const open = h.requests.filter(x => !['Resolved', 'Closed'].includes(x.status));
    return ph('Support', 'Write to Ledgerline’s support team. They see your firm, your name and role, the screen you were on and the version you run, so you only need to say what happened.', `<button class="btn pri" data-act="spNew">New request</button>`) +
      (h.connected ? '' : `<div class="note"><b>This server is not connected to Ledgerline support yet.</b> Requests are kept here and sent once your administrator connects it (LEDGERLINE_CONTROL_URL and LEDGERLINE_CONTROL_KEY).</div>`) +
      `<div class="panel"><div class="hd"><h3>${h.requests.length ? `Your requests · ${open.length} open` : 'Your requests'}</h3></div>${tbl([['Request'], ['Subject'], ['Priority'], ['Status'], ['Updated']], h.requests.map(x => {
        const last = x.messages.filter(m => m.from !== 'info').pop() || {};
        return `<tr class="click" data-act="spOpen" data-id="${esc(x.id)}"><td class="mono">${esc(x.id)}</td><td><b>${esc(x.subject)}</b><div class="dim small">${esc(x.category)}${x.user_name && x.user !== S.me.actor ? ' · ' + esc(x.user_name) : ''}${last.from === 'support' ? ' · <span class="pos">Support replied</span>' : ''}</div></td>
          <td>${spPri(x.priority)}</td><td>${spPill(x.status)}</td><td class="small nowrap">${fmtStamp(x.updated)}</td></tr>`;
      }), { empty: 'No requests yet.' })}</div>`;
  }
};

function spThread(h, r) {
  const done = r.status === 'Closed';
  return ph(esc(r.subject), `${esc(r.id)}${r.ticket ? ' · support ticket ' + esc(r.ticket) : ''} · ${esc(r.category)} · ${spPri(r.priority)} ${spPill(r.status)}`, `<button class="btn" data-act="spBack">All requests</button>${done ? '' : '<button class="btn" data-act="spClose">Close request</button>'}`) +
    `<div class="panel sp-thread">${r.messages.filter(m => m.from !== 'info').map(m => m.from === 'status'
      ? `<div class="sp-status dim small">${esc(m.text)} · ${fmtStamp(m.at)}</div>`
      : `<div class="sp-msg ${m.from === 'support' ? 'them' : 'me'}"><div class="sp-who"><b>${esc(m.who)}</b>${m.from === 'support' ? ' ' + pill('Ledgerline support', 'acc') : ''} <span class="dim small">${fmtStamp(m.at)}${m.from === 'firm' && !m.synced ? ' · not yet sent' : ''}</span></div><div class="sp-text">${esc(m.text)}</div></div>`).join('')}
    ${done ? '<div class="dim small" style="padding:10px 0">This request is closed. Write a new one if you need more help.</div>' : `<div class="fld" style="margin-top:12px"><label for="sp-reply">Reply</label><textarea class="in" id="sp-reply" rows="4" maxlength="8000"></textarea></div><div class="ft" style="padding:0;border:0"><button class="btn pri" data-act="spReply">Send</button></div>`}</div>`;
}

Object.assign(A, {
  spNew: () => {
    const h = C.support || {};
    const pri = h.priorities || ['Low', 'Normal', 'High', 'Urgent'], cat = h.categories || ['Other'];
    modal(`<div class="hd"><div><h2>New support request</h2><p>To Ledgerline’s support team. Don’t include passwords; for a client, give the UCC rather than their PAN or bank details.</p></div><button class="x" data-act="close" aria-label="Close">×</button></div>
      <form class="bd" onsubmit="return false"><div class="fld"><label for="sp-sub">Subject</label><input class="in" id="sp-sub" maxlength="150" placeholder="e.g. NSE trade file for today won’t load"></div>
      <div class="grid g2"><div class="fld"><label for="sp-cat">What is it about</label><select class="in" id="sp-cat">${cat.map(c => `<option>${esc(c)}</option>`).join('')}</select></div>
      <div class="fld"><label for="sp-pri">Priority</label><select class="in" id="sp-pri">${pri.map(p => `<option ${p === 'Normal' ? 'selected' : ''}>${esc(p)}</option>`).join('')}</select></div></div>
      <div class="fld"><label for="sp-text">What happened</label><textarea class="in" id="sp-text" rows="6" maxlength="8000" placeholder="What you were doing, what you expected and what you saw instead."></textarea></div>
      <div class="dim small">Sent with it: your firm, ${esc(meUser()?.name || S.me.actor)} (${esc(S.me.role)}), the screen “${esc(SP.from || 'Support')}” and this version of Ledgerline.</div></form>
      <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" data-act="spSend">Send</button></div>`);
  },
  spSend: async () => {
    const b = { subject: $('sp-sub').value, text: $('sp-text').value, priority: $('sp-pri').value, category: $('sp-cat').value, screen: SP.from };
    const r = await act(() => post('/v1/support', b), r => `${r.id} sent to Ledgerline support`, false).catch(() => null);
    if (r) { SP.open = r.id; if (S.view !== 'support') S.view = 'support'; render(); }
  },
  spOpen: id => { SP.open = id; $('page').innerHTML = groupBar('support') + V.support.html(C.support); bindPage(); window.scrollTo(0, 0); },
  spBack: () => { SP.open = ''; render({ keep: true }); },
  spReply: () => act(() => post('/v1/support/' + encodeURIComponent(SP.open), { text: $('sp-reply').value }), 'Reply sent'),
  spClose: () => act(() => post('/v1/support/' + encodeURIComponent(SP.open), { close: true }), 'Request closed'),
  spMenu: () => { SP.from = (NAV_VIEWS.find(v => v[0] === S.view) || [, S.view])[1]; SP.open = ''; S.view = 'support'; closeModal(); render(); window.scrollTo(0, 0); },
});
// Ledgy's links to a request ("support:SR-0004") open it.
const _spGo = A.go;
A.go = id => { const [v, t] = String(id).split(':'); if (v === 'support') SP.open = t || ''; _spGo(id); };
