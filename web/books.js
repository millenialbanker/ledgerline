'use strict';
/* Ledgerline console: the firm's own books on Firm accounts & GST (screen
   "books"). The chart of accounts is kept as heads, the way Tally keeps
   it: level 1 is Assets, Liabilities, Income and Expenditure; level 2 the
   major heads; sub-heads are optional; accounts sit under any head. The
   trial balance, profit and loss and balance sheet drill down from level 1
   to level 2, sub-heads, accounts, the account's ledger and the voucher.
   Vouchers are entered with Tally's keys. Codes are Ledgerline's own and
   are not shown. Loaded after app.js and acctpick.js. */

Object.assign(UI, { bkOpen: {}, bkAll: {} });

const bkLoad0 = V.books.load;
V.books.load = async () => {
  const [d, hd, moves, htb] = await Promise.all([bkLoad0(), get(`/v1/account-heads?date=${S.date}`), get('/v1/account-moves'), get(`/v1/accounts/trial-balance?as_of=${S.date}`)]);
  AP.heads = hd.heads; AP.cache.clear();
  const names = {}; d.acc.forEach(a => { names[a.code] = a.name; });
  return Object.assign(d, { heads: hd.heads, prefixes: hd.prefixes, moves, htb, names });
};
const bkName = code => C.books?.names?.[code] || (/^CLIENT(COM|UNALLOC)?:/.test(code) ? code.split(':')[1] : code);
const bkKids = heads => { const k = {}; heads.forEach(h => (k[h.parent || h.level1] ||= []).push(h)); return k; };
const bkOpen = (scope, id, dflt) => UI.bkAll[scope] ?? (UI.bkOpen[scope + ':' + id] ?? dflt);
const bkTog = (scope, id, open, label) => `<button type="button" class="bk-tog" data-act="bkTog" data-id="${esc(scope + ':' + id)}" data-open="${open ? 1 : 0}" aria-expanded="${open}" title="${open ? 'Close' : 'Open'}">${open ? '▾' : '▸'}</button>${label}`;
const bkAllBtns = scope => `<span class="split"><button class="btn sm" data-act="bkAll" data-id="${scope}" data-v="1">Open all</button><button class="btn sm" data-act="bkAll" data-id="${scope}" data-v="0">Close all</button></span>`;
const bkPad = lv => `style="padding-left:${10 + (lv - 1) * 18}px"`;

/* ---------- chart of accounts ---------- */
P['bk-coa'] = d => {
  const ck = has('Ops checker'), mk = has('Ops maker', 'Ops checker'), me = S.me.actor;
  const byHead = {}; d.acc.forEach(a => { if (a.code !== 'CLIENT:*') (byHead[a.head] ||= []).push(a); });
  Object.values(byHead).forEach(l => l.sort((a, b) => a.name.localeCompare(b.name)));
  const kids = bkKids(d.heads), tot = {};
  const clientNode = id => { for (const n of d.htb.heads) for (const h of n.heads) if (h.id === id) return h; return null; };
  const sumOf = h => tot[h.id] ??= (h.clients ? (h.id === 'A-CLIENTDR' ? num(clientNode(h.id)?.debit) : -num(clientNode(h.id)?.credit)) : (byHead[h.id] || []).reduce((s, a) => s + num(a.balance), 0)) + (kids[h.id] || []).reduce((s, c) => s + sumOf(c), 0);
  const acctRow = (a, lv) => `<tr class="bk-acct"><td ${bkPad(lv)}><button class="linkish" data-act="acctStmt" data-id="${esc(a.code)}">${esc(a.name)}</button>${a.alias ? ` <span class="dim small">(${esc(a.alias)})</span>` : ''}${a.status === 'Pending' ? ' ' + pill('New: waits for a checker', 'warn') : ''}${a.restricted ? ' ' + pill('Restricted', 'warn') + `<div class="dim small">visible to ${a.allowed_users.map(esc).join(', ')} and admins</div>` : ''}</td>
    <td class="r">${apDC(a.balance)}</td><td><div class="split">${a.status === 'Pending' && ck && a.created_by !== me ? `<button class="btn sm pri" data-act="bkAcctDo" data-id="${esc(a.code)}" data-a="approve">Approve</button><button class="btn sm bad" data-act="bkAcctDo" data-id="${esc(a.code)}" data-a="reject">Reject</button>` : ''}${ck && !a.code.startsWith('CC:') ? `<button class="btn sm" data-act="editAccount" data-id="${esc(a.code)}">Edit</button>` : ''}${mk && a.status !== 'Pending' ? `<button class="btn sm" data-act="bkMove" data-id="${esc(a.code)}">Move</button>` : ''}</div></td></tr>`;
  const headRows = (h) => {
    const open = bkOpen('coa', h.id, false), sub = kids[h.id] || [], accts = byHead[h.id] || [], lv = h.level;
    const empty = !sub.length && !accts.length && !h.clients;
    const label = `<b>${esc(h.name)}</b>${h.clients ? ' <span class="dim small">client ledgers</span>' : ` <span class="dim small">${accts.length ? accts.length + ' account' + (accts.length === 1 ? '' : 's') : ''}${sub.length ? (accts.length ? ' · ' : '') + sub.length + ' sub-head' + (sub.length === 1 ? '' : 's') : ''}</span>`}`;
    const acts = h.clients ? `<button class="btn sm" data-act="bkClients" data-id="${h.id === 'A-CLIENTDR' ? 'dr' : 'cr'}">List clients</button>` : ck ? `<div class="split bk-hacts">${h.prefix ? `<button class="btn sm" data-act="bkNewAcct" data-id="${h.id}">+ Account</button>` : ''}<button class="btn sm" data-act="bkNewSub" data-id="${h.id}">+ Sub-head</button><button class="btn sm" data-act="bkHead" data-id="${h.id}">Rename</button><button class="btn sm" data-act="bkOrder" data-id="${h.id}" data-v="up" aria-label="Move ${esc(h.name)} up">↑</button><button class="btn sm" data-act="bkOrder" data-id="${h.id}" data-v="down" aria-label="Move ${esc(h.name)} down">↓</button>${!h.fixed && !h.accounts && !sub.length ? `<button class="btn sm bad" data-act="bkRemoveHead" data-id="${h.id}">Remove</button>` : ''}</div>` : '';
    let out = `<tr class="bk-head lv${lv}"><td ${bkPad(lv)}>${empty || h.clients ? `<span class="bk-tog sp"></span>${label}` : bkTog('coa', h.id, open, label)}</td><td class="r">${apDC(sumOf(h))}</td><td>${acts}</td></tr>`;
    if (open) { sub.forEach(s => { out += headRows(s); }); accts.forEach(a => { out += acctRow(a, lv + 2); }); }
    return out;
  };
  const rows = [];
  for (const l1 of ['Assets', 'Liabilities', 'Income', 'Expenditure']) {
    const top = kids[l1] || [];
    rows.push(`<tr class="bk-l1"><td><b>${l1}</b>${l1 === 'Liabilities' ? ' <span class="dim small">with capital and reserves</span>' : ''}</td><td class="r"><b>${apDC(top.reduce((s, h) => s + sumOf(h), 0))}</b></td><td>${ck ? `<button class="btn sm" data-act="bkNewHead" data-id="${l1}">+ Head</button>` : ''}</td></tr>`);
    top.forEach(h => rows.push(headRows(h)));
  }
  const pendA = d.acc.filter(a => a.status === 'Pending'), pendM = d.moves.filter(m => m.status === 'Pending');
  const moves = pendM.length ? `<div class="pad" style="padding-bottom:0"><h4 style="margin:0 0 6px">Moves waiting for approval</h4>${tbl([['Account'], ['From'], ['To'], ['Why'], ['Asked by'], ['']], pendM.map(m => `<tr><td>${esc(m.account_name)}</td><td>${esc(m.from_name)}</td><td><b>${esc(m.to_name)}</b></td><td class="wrap">${esc(m.reason)}</td><td class="dim small">${esc(m.by)}</td><td>${ck && m.by !== me ? `<div class="split"><button class="btn sm pri" data-act="bkMoveDo" data-id="${m.id}" data-a="approve">Approve</button><button class="btn sm bad" data-act="bkMoveDo" data-id="${m.id}" data-a="reject">Reject</button></div>` : '<span class="dim small">another checker approves</span>'}</td></tr>`))}</div>` : '';
  const newA = pendA.length ? `<div class="note warn" style="margin:12px 14px 0">${pendA.length} new account${pendA.length > 1 ? 's' : ''} made at voucher entry wait${pendA.length > 1 ? '' : 's'} for a checker: ${pendA.map(a => esc(a.name)).join(', ')}. Approving a voucher that uses one approves it too.</div>` : '';
  return newA + moves + `<div class="pad" style="padding-bottom:0"><div class="tools">${bkAllBtns('coa')}<span class="dim small" style="margin-left:auto">Balances as at ${fmtDate(S.date)}. Click a head to open it; click an account for its ledger.</span></div></div>` +
    tbl([['Head / account'], ['Balance', 'r'], ['']], rows, { tall: true }) + `<div class="pad"><span class="dim small">Every client is an account under Clients: a client in debit is shown under Assets, in credit under Liabilities. Ledgerline makes each account's code itself.</span></div>`;
};

/* ---------- trial balance by heads ---------- */
P['bk-tb'] = d => {
  const t = d.htb, rows = [];
  const line = (a, lv) => `<tr class="bk-acct"><td ${bkPad(lv)}>${a.code && !/^CLIENT:(DR|CR)$/.test(a.code) ? `<button class="linkish" data-act="acctStmt" data-id="${esc(a.code)}">${esc(a.name)}</button>` : esc(a.name)}</td><td class="r">${num(a.debit) ? nf(a.debit, 2) : ''}</td><td class="r">${num(a.credit) ? nf(a.credit, 2) : ''}</td></tr>`;
  const node = n => {
    const open = n.level === 1 ? bkOpen('tb', n.id, true) : bkOpen('tb', n.id, false);
    const cl = n.id === 'A-CLIENTDR' || n.id === 'L-CLIENTCR';
    const label = (n.level === 1 ? `<b>${esc(n.name)}</b>` : esc(n.name)) + (cl ? ` <span class="dim small">${n.clients} ledger${n.clients === 1 ? '' : 's'}</span> <button class="btn sm" data-act="bkClients" data-id="${n.id === 'A-CLIENTDR' ? 'dr' : 'cr'}">List</button>` : '');
    rows.push(`<tr class="bk-head lv${n.level}"><td ${bkPad(n.level)}>${cl ? `<span class="bk-tog sp"></span>${label}` : bkTog('tb', n.id, open, label)}</td><td class="r">${num(n.debit) ? nf(n.debit, 2) : ''}</td><td class="r">${num(n.credit) ? nf(n.credit, 2) : ''}</td></tr>`);
    if (!open || cl) return;
    n.heads.forEach(node); n.accounts.forEach(a => rows.push(line(a, n.level + 2)));
  };
  t.heads.forEach(node);
  return `<div class="pad" style="padding-bottom:0"><div class="tools">${bkAllBtns('tb')}<span class="dim small" style="margin-left:auto">As at ${fmtDate(t.as_of || S.date)} · each account in its debit or credit column, so the totals are the ledger’s own.</span></div></div>` +
    tbl([['Head / account'], ['Debit', 'r'], ['Credit', 'r']], rows, { tall: true, foot: `<tfoot><tr><td>Total ${t.balanced ? pill('Agrees', 'good') : pill('Does not agree', 'bad')}</td><td class="r">${nf(t.total_debit, 2)}</td><td class="r">${nf(t.total_credit, 2)}</td></tr></tfoot>` });
};

/* ---------- profit and loss, balance sheet ---------- */
const finPeriod = (d, withBranch) => `<div class="pad" style="padding-bottom:0"><div class="tools"><label class="dim small" for="fin-from">Period</label><input class="in sm" type="date" id="fin-from" value="${esc(d.fin.from)}"><span class="dim">to</span><input class="in sm" type="date" id="fin-to" value="${esc(d.fin.to)}" aria-label="to">${withBranch ? `<select class="in sm" id="fin-br" aria-label="Branch"><option value="">Whole firm</option>${d.fin.branches.map(b => `<option ${b === UI.finBranch ? 'selected' : ''}>${esc(b)}</option>`).join('')}</select>` : ''}<button class="btn sm" data-act="finPeriod">Show</button>${bkAllBtns('fin')}</div></div>`;
// finRows lays out a section and, when open, its accounts and sub-heads.
function finRows(s, lv, rows) {
  const open = bkOpen('fin', s.id || s.title, lv === 1), has = s.lines.length || s.heads.length;
  rows.push(`<tr class="bk-head lv${lv}"><td ${bkPad(lv)}>${has ? bkTog('fin', s.id || s.title, open, lv <= 2 ? `<b>${esc(s.title)}</b>` : esc(s.title)) : esc(s.title)}</td><td class="r">${lv === 1 ? `<b>${nf(s.total, 2)}</b>` : nf(s.total, 2)}</td></tr>`);
  if (!open) return;
  if (lv === 1) { s.heads.forEach(h => finRows(h, 2, rows)); return; } // level 1 lists its heads; its flat lines are the same accounts
  s.heads.forEach(h => finRows(h, lv + 1, rows));
  s.lines.forEach(l => {
    const drill = l.code === 'CLIENT:DR' ? `data-act="bkClients" data-id="dr"` : l.code === 'CLIENT:CR' ? `data-act="bkClients" data-id="cr"` : l.code && l.code !== 'PROFIT' ? `data-act="acctStmt" data-id="${esc(l.code)}"` : '';
    rows.push(`<tr class="bk-acct"><td ${bkPad(lv + 2)}>${drill ? `<button class="linkish" ${drill}>${esc(l.name)}</button>` : esc(l.name)}</td><td class="r">${nf(l.amount, 2)}</td></tr>`);
  });
}
P['bk-pl'] = d => { const rows = []; finRows(d.fin.income, 1, rows); finRows(d.fin.expenses, 1, rows);
  return finPeriod(d, true) + `<div class="tw"><table class="tbl"><tbody>${rows.join('')}<tr class="bk-l1"><td><b>${num(d.fin.profit) >= 0 ? 'Profit' : 'Loss'} for the period</b></td><td class="r"><b class="${sgn(d.fin.profit)}">${nf(Math.abs(num(d.fin.profit)), 2)}</b></td></tr></tbody></table></div><div class="pad"><span class="dim small">${d.fin.branch ? `Branch ${esc(d.fin.branch)}: income from its clients’ bills, interest and charges, and vouchers entered for the branch.` : 'Whole firm. Choose a branch to see its own income and expenses.'} Click a head to open it, an account for its ledger.</span></div>`; };
P['bk-bs'] = d => {
  if (d.fin.branch) return finPeriod(d, true) + '<div class="pad dim">A branch has no balance sheet of its own; choose Whole firm.</div>';
  const side = (title, ss, total) => { const rows = []; finRows({ id: title, title, lines: [], heads: ss, total }, 1, rows); return `<div class="tw"><table class="tbl"><tbody>${rows.join('')}</tbody></table></div>`; };
  return finPeriod(d, true) + `<div class="grid g2" style="padding:0 14px 14px">${side('Liabilities', d.fin.liabilities, d.fin.total_liabilities)}${side('Assets', d.fin.assets, d.fin.total_assets)}</div><div class="pad"><span class="dim small">As at ${fmtDate(d.fin.to)}. ${d.fin.balanced ? pill('Balances', 'good') : pill('Does not balance', 'bad')} Clients in debit are under Assets, clients in credit under Liabilities; capital and reserves sit under Liabilities.</span></div>`;
};

/* ---------- vouchers list ---------- */
const bkLine = l => `${num(l.debit) ? 'Dr' : 'Cr'} ${esc(l.name || bkName(l.account))} <span class="dim">${nf(num(l.debit) || l.credit, 2)}</span>`;
P['bk-vch'] = d => { const ck = has('Ops checker'), me = S.me.actor;
  return tbl([['Voucher'], ['Date'], ['Party / narration'], ['Lines'], ['Amount', 'r'], ['Branch'], ['Maker / checker'], ['Status'], ['']], d.vch.map(v => { const id = v.no.replaceAll('/', '-');
    return `<tr><td class="n"><button class="linkish" data-act="bkVoucher" data-id="${esc(v.no)}">${esc(v.no)}</button><div class="dim small">${v.type}${v.ref ? ' · ' + esc(v.ref) : ''}</div></td><td class="n">${fmtDate(v.date)}</td><td class="wrap">${v.party ? `<b>${esc(v.party)}</b><br>` : ''}${esc(v.narration)}</td><td class="small wrap">${v.lines.map(bkLine).join('<br>')}${(v.new_accounts || []).length ? `<div>${pill((v.new_accounts.length === 1 ? 'New account: ' : 'New accounts: ') + v.new_accounts.map(a => a.name).join(', '), 'warn')}</div>` : ''}</td><td class="r">${nf(v.total, 2)}</td><td>${esc(v.branch || 'Head office')}</td><td class="dim small">${esc(v.maker)}${v.checker ? ' · ' + esc(v.checker) : ''}</td><td>${pill(v.status, { Posted: 'good', Pending: 'warn', Rejected: 'bad', Cancelled: 'nt' }[v.status])}${v.reason ? `<div class="dim small wrap">${esc(v.reason)}</div>` : ''}</td>
    <td>${v.status === 'Pending' && ck && v.maker !== me ? `<div class="split"><button class="btn sm pri" data-act="vchDo" data-id="${id}" data-a="approve">Post</button><button class="btn sm bad" data-act="vchDo" data-id="${id}" data-a="reject">Reject</button></div>` : v.status === 'Posted' && ck ? `<button class="btn sm" data-act="vchDo" data-id="${id}" data-a="cancel">Cancel</button>` : ''}</td></tr>`; }), { tall: true, empty: 'No vouchers yet.' }); };
function voucherView(v, back) {
  modal(mhd(`${esc(v.type)} voucher ${esc(v.no)}`, `${fmtDate(v.date)} · ${esc(v.branch || 'Head office')} · ${pill(v.status, { Posted: 'good', Pending: 'warn', Rejected: 'bad', Cancelled: 'nt' }[v.status])}`) + `<div class="bd">${v.party ? `<div><span class="dim small">Party</span><br><b>${esc(v.party)}</b></div>` : ''}
    ${tbl([['Dr / Cr'], ['Account'], ['Debit', 'r'], ['Credit', 'r']], v.lines.map(l => `<tr><td>${num(l.debit) ? 'Dr' : 'Cr'}</td><td>${l.account.startsWith('CLIENT') ? esc(l.name || bkName(l.account)) : `<button class="linkish" data-act="acctStmt" data-id="${esc(l.account)}">${esc(l.name || bkName(l.account))}</button>`}</td><td class="r">${num(l.debit) ? nf(l.debit, 2) : ''}</td><td class="r">${num(l.credit) ? nf(l.credit, 2) : ''}</td></tr>`), { foot: `<tfoot><tr><td></td><td>Total</td><td class="r">${nf(v.total, 2)}</td><td class="r">${nf(v.total, 2)}</td></tr></tfoot>` })}
    <div><span class="dim small">Narration</span><br>${esc(v.narration)}</div><div class="dim small">${v.ref ? 'Cheque / UTR ' + esc(v.ref) + ' · ' : ''}made by ${esc(v.maker)}${v.checker ? ' · checked by ' + esc(v.checker) : ''}${v.entry ? ' · journal ' + esc(v.entry) : ''}${v.reason ? ' · ' + esc(v.reason) : ''}</div></div>
    <div class="ft">${back ? `<button class="btn" data-act="acctStmt" data-id="${esc(back)}">Back to the ledger</button>` : ''}<button class="btn" data-act="close">Close</button></div>`);
}
async function entryView(id, back) {
  try { const e = await get(`/v1/journal/${encodeURIComponent(id)}`);
    if (e.source?.kind === 'VOUCHER') { const v = C.books.vch.find(x => x.no === e.source.id); if (v) { voucherView(v, back); return; } }
    modal(mhd('Journal entry ' + esc(e.id), `${fmtDate(e.value_date)} · ${esc(e.source?.kind || '')} ${esc(e.source?.id || '')}`) + `<div class="bd"><div>${esc(e.narration)}</div>${tbl([['Account'], ['Debit', 'r'], ['Credit', 'r'], ['Note']], e.postings.map(p => `<tr><td>${esc(bkName(p.account))}</td><td class="r">${num(p.debit) ? nf(p.debit, 2) : ''}</td><td class="r">${num(p.credit) ? nf(p.credit, 2) : ''}</td><td class="dim small wrap">${esc(p.memo || '')}</td></tr>`))}${e.reversal_of ? `<div class="dim small">Reverses ${esc(e.reversal_of)}</div>` : ''}</div>
      <div class="ft">${back ? `<button class="btn" data-act="acctStmt" data-id="${esc(back)}">Back to the ledger</button>` : ''}<button class="btn" data-act="close">Close</button></div>`);
  } catch (err) { toast(err.message, true); }
}

/* ---------- modals for the chart ---------- */
function bkHeadOptions(sel, filter) {
  const d = C.books, groups = {};
  d.heads.filter(filter).forEach(h => (groups[h.level1] ||= []).push(h));
  return Object.keys(groups).map(l1 => `<optgroup label="${esc(l1)}">${groups[l1].map(h => `<option value="${h.id}" ${h.id === sel ? 'selected' : ''}>${'  '.repeat(h.level - 2)}${h.level > 2 ? '› ' : ''}${esc(h.name)}</option>`).join('')}</optgroup>`).join('');
}
function accountModal(a, head) {
  const isNew = !a; a = a || { code: '', name: '', alias: '', head: head || '', restricted: false, allowed_users: [] };
  modal(mhd(isNew ? 'New account' : 'Edit ' + esc(a.name), isNew ? 'Give it a name and the head it goes under. Ledgerline makes the code.' : `Under ${esc(a.head_name || '')}${a.sub_head ? ' › ' + esc(a.sub_head) : ''}. To put it under another head, use Move: a second person approves it.`) + `<form class="bd" id="af"><div class="form">
    <div class="fld"><label for="a-n">Name</label><input class="in" id="a-n" value="${esc(a.name)}" required></div><div class="fld"><label for="a-al">Alias <span class="dim">(optional, short)</span></label><input class="in" id="a-al" maxlength="24" value="${esc(a.alias || '')}"></div>
    ${isNew ? `<div class="fld full"><label for="a-h">Head</label><select class="in" id="a-h">${bkHeadOptions(a.head, h => h.prefix)}</select></div>` : ''}
    <label class="check full"><input type="checkbox" id="a-r" ${a.restricted ? 'checked' : ''}> Restricted: only these users (and admins) can see or post to it</label><div class="fld full"><label for="a-u">Users (comma separated)</label><input class="in mono" id="a-u" value="${esc(a.allowed_users.join(', '))}"></div></div>
    <div id="a-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
  $('af').onsubmit = async e => { e.preventDefault();
    try { await post('/v1/accounts', { code: a.code, name: $('a-n').value.trim(), alias: $('a-al').value.trim(), head: isNew ? $('a-h').value : '', restricted: $('a-r').checked, allowed_users: $('a-u').value.split(',').map(x => x.trim()).filter(Boolean) }); closeModal(); toast('Account saved'); UI.bkTab = 'coa'; render({ keep: true }); }
    catch (err) { $('a-err').innerHTML = errHTML(err); } };
}
function headModal(h, parent, level1) {
  const d = C.books, p = parent && d.heads.find(x => x.id === parent);
  const title = h ? 'Rename ' + esc(h.name) : p ? 'New sub-head under ' + esc(p.name) : 'New head under ' + esc(level1);
  const kinds = !h && !p ? (d.prefixes[level1] || []) : [];
  modal(mhd(title, h ? 'Reports and the search box show the new name.' : p ? 'Accounts under it work like the accounts under ' + esc(p.name) + '.' : 'Choose what kind of accounts go under it: that decides how they behave on vouchers.') + `<form class="bd" id="hf"><div class="form">
    <div class="fld"><label for="h-n">Name</label><input class="in" id="h-n" value="${esc(h?.name || '')}" required></div>
    ${kinds.length ? `<div class="fld"><label for="h-k">Kind of accounts</label><select class="in" id="h-k">${kinds.map(k => `<option value="${k[0]}">${esc(k[1])}</option>`).join('')}</select></div>` : ''}</div>
    <div id="h-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Save</button></div></form>`);
  $('hf').onsubmit = async e => { e.preventDefault();
    try { await post('/v1/account-heads', { id: h?.id || '', name: $('h-n').value.trim(), parent: parent || '', level1: level1 || '', prefix: $('h-k')?.value || '' }); closeModal(); toast('Head saved'); UI.bkTab = 'coa'; render({ keep: true }); }
    catch (err) { $('h-err').innerHTML = errHTML(err); } };
}
function moveModal(code) {
  const d = C.books, a = d.acc.find(x => x.code === code); if (!a) return;
  modal(mhd('Move ' + esc(a.name), `Now under ${esc(a.head_name)}${a.sub_head ? ' › ' + esc(a.sub_head) : ''}. Another person approves the move; the account, its ledger and its balance stay as they are.`) + `<form class="bd" id="mvf"><div class="form">
    <div class="fld full"><label for="mv-h">Move to</label><select class="in" id="mv-h">${bkHeadOptions('', h => h.level1 === a.level1 && !h.clients && h.id !== a.head)}</select></div>
    <div class="fld full"><label for="mv-r">Why</label><input class="in" id="mv-r" required placeholder="e.g. the gateway bill is a software subscription"></div></div>
    <div id="mv-err"></div><div class="ft" style="padding:0;border:0"><button class="btn" type="button" data-act="close">Cancel</button><button class="btn pri" type="submit">Ask for the move</button></div></form>`);
  $('mvf').onsubmit = async e => { e.preventDefault();
    try { await post(`/v1/accounts/${encodeURIComponent(code)}/move`, { head: $('mv-h').value, reason: $('mv-r').value.trim() }); closeModal(); toast('Move sent for approval'); UI.bkTab = 'coa'; render({ keep: true }); }
    catch (err) { $('mv-err').innerHTML = errHTML(err); } };
}
async function clientsModal(side) {
  const title = side === 'dr' ? 'Clients with debit balances' : 'Clients with credit balances';
  modal(mhd(title, `As at ${fmtDate(S.date)}, biggest first. Type to find a client by name or code.`) + `<div class="bd"><input class="in" id="cl-q" placeholder="Name or client code" autocomplete="off" aria-label="Find a client"><div id="cl-t"><p class="loading">Loading…</p></div></div><div class="ft"><button class="btn" data-act="close">Close</button></div>`);
  let seq = 0;
  const load = async () => { const my = ++seq, q = $('cl-q')?.value.trim() || '';
    try { const r = await get(`/v1/accounts/search?clients=only&side=${side}&limit=100&date=${S.date}&q=${encodeURIComponent(q)}`); if (my !== seq || !$('cl-t')) return;
      $('cl-t').innerHTML = tbl([['Client'], ['Code'], ['Balance', 'r'], ['']], r.hits.map(h => `<tr><td>${h.match === 'name' ? apHL(h.name, h.at, h.len) : esc(h.name)}</td><td class="mono">${h.match === 'ucc' ? apHL(h.ucc, h.at, h.len) : esc(h.ucc)}</td><td class="r">${apDC(h.balance)}</td><td><button class="btn sm" data-act="acctStmt" data-id="${esc(h.code)}">Ledger</button></td></tr>`), { tall: true, empty: 'No client.' }) + (r.more ? `<div class="dim small" style="margin-top:6px">${r.hits.length} of ${r.total} shown; type to narrow.</div>` : '');
    } catch (e) { $('cl-t').innerHTML = errHTML(e); } };
  $('cl-q').oninput = () => { clearTimeout(clientsModal.t); clientsModal.t = setTimeout(load, 120); };
  load();
}

/* ---------- Tally-style voucher entry ---------- */
const VX_TYPES = [['Contra', 'F4'], ['Payment', 'F5'], ['Receipt', 'F6'], ['Journal', 'F7']];
const VX_FIRST = { Payment: 'Dr', Journal: 'Dr', Receipt: 'Cr', Contra: 'Cr' };
let VX = null;
function vxLineOpts(i) {
  return { only: VX.type === 'Contra' ? 'BANK,CASH' : '', clients: VX.type === 'Journal' ? '' : 'no', aria: `Line ${i + 1} account`,
    onPick: () => { VX.dirty = true; }, afterEnter: () => $(`vx-m-${i}`)?.focus(), onEmptyEnter: () => vxEndLines(i), canEnd: () => vxLines().filter(l => l.code).length >= 2 };
}
function vxLineHTML(i, side) {
  return `<div class="vx-l" data-l="${i}"><button type="button" class="vx-dc" id="vx-dc-${i}" data-side="${side}" aria-label="Line ${i + 1}: debit or credit (D or C)">${side}</button>
    ${acctPick(`vx-a-${i}`, '', vxLineOpts(i))}<input class="in vx-amt ${side === 'Dr' ? 'dr' : 'cr'}" id="vx-m-${i}" placeholder="${side === 'Dr' ? 'Debit amount' : 'Credit amount'}" inputmode="decimal" autocomplete="off" aria-label="Line ${i + 1} amount"><button type="button" class="vx-del" data-act="vxDel" data-id="${i}" tabindex="-1" aria-label="Remove line ${i + 1}">×</button></div>`;
}
function vxLines() { return [...document.querySelectorAll('#vx .vx-l')].map(r => { const i = +r.dataset.l; return { i, side: $(`vx-dc-${i}`).dataset.side, code: $(`vx-a-${i}`).value, amt: vxAmt($(`vx-m-${i}`).value) }; }); }
const vxAmt = v => num(String(v).replace(/[,\s₹]/g, ''));
function vxTotals(skip) { let dr = 0, cr = 0; vxLines().forEach(l => { if (l.i === skip) return; if (l.side === 'Dr') dr += l.amt; else cr += l.amt; }); return { dr: Math.round(dr * 100) / 100, cr: Math.round(cr * 100) / 100 }; }
function vxShowTotals() {
  const t = vxTotals(), diff = Math.round((t.dr - t.cr) * 100) / 100;
  $('vx-tot').innerHTML = `<span>Debit <b class="mono">${nf(t.dr, 2)}</b></span><span>Credit <b class="mono">${nf(t.cr, 2)}</b></span>${diff ? `<span class="neg">Difference <b class="mono">${nf(Math.abs(diff), 2)} ${diff > 0 ? 'Dr' : 'Cr'}</b></span>` : t.dr ? pill('Balanced', 'good') : ''}`;
}
function vxSetSide(i, side) { const b = $(`vx-dc-${i}`); if (!b) return; b.dataset.side = side; b.textContent = side; const m = $(`vx-m-${i}`); m.classList.toggle('dr', side === 'Dr'); m.classList.toggle('cr', side === 'Cr'); m.placeholder = side === 'Dr' ? 'Debit amount' : 'Credit amount'; vxShowTotals(); }
function vxAddLine(side) { const i = VX.next++; $('vx-lines').insertAdjacentHTML('beforeend', vxLineHTML(i, side)); return i; }
// vxEndLines: Enter on an empty line ends the lines (from the third line
// on, as in Tally) and goes to the narration.
function vxEndLines(i) {
  const ls = vxLines(), filled = ls.filter(l => l.code);
  if (filled.length < 2) return;
  if (!ls.find(l => l.i === i)?.code && ls.length > 2) $(`vx-dc-${i}`)?.closest('.vx-l')?.remove();
  vxShowTotals(); $('vx-n').focus();
}
function vxSetType(t) {
  if (!VX || VX.type === t) return;
  VX.type = t; VX.dirty = true;
  document.querySelectorAll('#vx .vx-type').forEach(b => { const on = b.dataset.t === t; b.classList.toggle('on', on); b.setAttribute('aria-pressed', on); });
  vxLines().forEach(l => Object.assign(AP.opts[`vx-a-${l.i}`], vxLineOpts(l.i)));
  const first = vxLines()[0]; if (first && !first.code) vxSetSide(first.i, VX_FIRST[t]);
  $('vx-hint').textContent = { Payment: 'A payment credits one bank or cash account.', Receipt: 'A receipt debits one bank or cash account.', Contra: 'A contra moves money between the firm’s bank and cash accounts.', Journal: 'A journal adjusts accounts; client ledgers can be picked here (a checker approves).' }[t];
}
function vxLeave(ask) {
  if (!ask || !VX?.dirty) { VX = null; closeModal(); return; }
  $('vx-leave').hidden = false; $('vx-stay').focus();
}
async function vxSave() {
  const ls = vxLines(), lines = [];
  for (const [n, l] of ls.entries()) {
    if (!l.code && !l.amt) continue;
    if (!l.code) { $('vx-err').innerHTML = `<div class="note warn">Line ${n + 1} has an amount but no account.</div>`; $(`vx-a-${l.i}-q`).focus(); return; }
    if (!(l.amt > 0)) { $('vx-err').innerHTML = `<div class="note warn">Give line ${n + 1} an amount.</div>`; $(`vx-m-${l.i}`).focus(); return; }
    lines.push({ account: l.code, debit: l.side === 'Dr' ? l.amt.toFixed(2) : '0', credit: l.side === 'Cr' ? l.amt.toFixed(2) : '0' });
  }
  const btn = $('vx-save'); btn.disabled = true;
  try { const v = await post('/v1/vouchers', { type: VX.type, date: $('vx-d').value, branch: $('vx-b').value.trim(), party: $('vx-p-q').value.trim(), party_account: $('vx-p').value, ref: $('vx-r').value.trim(), narration: $('vx-n').value.trim(), lines });
    VX = null; closeModal(); toast(`Voucher ${v.no} sent for approval`); UI.bkTab = 'vch'; render({ keep: true }); }
  catch (err) { $('vx-err').innerHTML = errHTML(err); btn.disabled = false; }
}
function voucherModal(type) {
  VX = { type: type || 'Payment', next: 0, dirty: false };
  wideModal(`<div class="hd"><div><h2>Voucher entry</h2><p>An Ops checker posts it. Client money moves through Receipts &amp; payouts, not vouchers.</p></div><button class="x" type="button" data-act="vxLeave" aria-label="Leave">×</button></div>
    <form class="bd vx" id="vx" novalidate>
    <div class="vx-types" role="group" aria-label="Voucher type">${VX_TYPES.map(([t, k]) => `<button type="button" class="vx-type ${t === VX.type ? 'on' : ''}" data-t="${t}" aria-pressed="${t === VX.type}" tabindex="-1"><kbd>${k}</kbd> ${t}</button>`).join('')}<span class="dim small" id="vx-hint"></span></div>
    <div class="vx-head"><div class="fld"><label for="vx-d">Date</label><input class="in" type="date" id="vx-d" value="${esc(S.date)}"></div><div class="fld"><label for="vx-b">Branch</label><input class="in" id="vx-b" placeholder="Head office" autocomplete="off"></div>
      <div class="fld vx-party"><label for="vx-p-q">Party</label>${acctPick('vx-p', '', { free: true, create: false, placeholder: 'Pick an account or type a name', afterEnter: () => $('vx-r').focus() })}</div><div class="fld"><label for="vx-r">Cheque / UTR</label><input class="in mono" id="vx-r" autocomplete="off"></div></div>
    <div class="vx-grid"><div class="vx-lh"><span>Dr/Cr</span><span>Particulars</span><span class="r vx-hd">Debit</span><span class="r vx-hd">Credit</span><span class="r vx-ha">Amount</span></div><div id="vx-lines"></div></div>
    <div class="vx-tot" id="vx-tot" aria-live="polite"></div>
    <div class="fld"><label for="vx-n">Narration</label><textarea class="in" id="vx-n" rows="2"></textarea></div>
    <div class="vx-keys" aria-label="Keyboard shortcuts"><span><kbd>Enter</kbd> next field</span><span><kbd>D</kbd>/<kbd>C</kbd> Dr or Cr</span><span><kbd>↑</kbd><kbd>↓</kbd> choose an account</span><span><kbd>F4</kbd> Contra</span><span><kbd>F5</kbd> Payment</span><span><kbd>F6</kbd> Receipt</span><span><kbd>F7</kbd> Journal</span><span><kbd>Ctrl</kbd>+<kbd>A</kbd> save</span><span><kbd>Esc</kbd> leave</span></div>
    <div id="vx-err"></div>
    <div class="note warn vx-leave" id="vx-leave" hidden role="alertdialog" aria-label="Leave this voucher?"><span>Leave this voucher? What you typed is not saved.</span><span class="split"><button type="button" class="btn sm" id="vx-stay">Stay</button><button type="button" class="btn sm bad" id="vx-go">Leave</button></span></div>
    </form><div class="ft"><button class="btn" type="button" data-act="vxLeave">Leave <kbd>Esc</kbd></button><button class="btn pri" type="button" id="vx-save">Save <kbd>Ctrl+A</kbd></button></div>`);
  setTimeout(() => { modalLocked = true; });
  const t = VX.type; VX.type = ''; vxSetType(t); VX.dirty = false;
  vxAddLine(VX_FIRST[t]); vxAddLine(VX_FIRST[t] === 'Dr' ? 'Cr' : 'Dr');
  vxShowTotals();
  $('vx-save').onclick = vxSave;
  $('vx-stay').onclick = () => { $('vx-leave').hidden = true; $('vx-n').focus(); };
  $('vx-go').onclick = () => vxLeave(false);
  document.querySelectorAll('#vx .vx-type').forEach(b => { b.onclick = () => vxSetType(b.dataset.t); });
  $('vx').addEventListener('input', e => { VX.dirty = true; if (e.target.classList.contains('vx-amt')) vxShowTotals(); });
  $('vx').addEventListener('click', e => { const b = e.target.closest('.vx-dc'); if (b) { vxSetSide(+b.id.split('-')[2], b.dataset.side === 'Dr' ? 'Cr' : 'Dr'); VX.dirty = true; } });
  // The last line's amount fills in to balance.
  $('vx').addEventListener('focusin', e => {
    const m = e.target; if (!m.classList?.contains('vx-amt') || m.value) return;
    const i = +m.id.split('-')[2], side = $(`vx-dc-${i}`).dataset.side, t = vxTotals(i), diff = Math.round((t.dr - t.cr) * 100) / 100;
    if (side === 'Dr' && diff < 0 || side === 'Cr' && diff > 0) { m.value = Math.abs(diff).toFixed(2); m.select(); vxShowTotals(); }
  });
  setTimeout(() => $('vx-d').focus(), 20);
}
// The voucher entry's keys. Registered after the search box's, which
// takes Enter and Esc first while its dropdown is open.
document.addEventListener('keydown', e => {
  const f = $('vx'); if (!f || !VX) return;
  const k = e.key, el = e.target;
  const stop = () => { e.preventDefault(); e.stopPropagation(); };
  if (/^F[4-7]$/.test(k)) { stop(); vxSetType(VX_TYPES[+k[1] - 4][0]); return; }
  if ((e.ctrlKey || e.metaKey) && (k === 'a' || k === 'A')) { stop(); if ($('vx-leave').hidden) vxSave(); return; }
  if (k === 'Escape') { stop(); if (!$('vx-leave').hidden) { $('vx-leave').hidden = true; $('vx-n').focus(); } else vxLeave(true); return; }
  if (!f.contains(el)) return;
  if (el.classList.contains('vx-dc')) {
    const i = +el.id.split('-')[2];
    if (k === 'd' || k === 'D') { stop(); vxSetSide(i, 'Dr'); VX.dirty = true; return; }
    if (k === 'c' || k === 'C') { stop(); vxSetSide(i, 'Cr'); VX.dirty = true; return; }
    if (k === ' ' || k === 'ArrowUp' || k === 'ArrowDown') { stop(); vxSetSide(i, el.dataset.side === 'Dr' ? 'Cr' : 'Dr'); VX.dirty = true; return; }
    if (k === 'Enter') { stop(); $(`vx-a-${i}-q`).focus(); }
    return;
  }
  if (k !== 'Enter' || el.closest('.vx-leave')) return;
  if (el.id === 'vx-n') { if (e.shiftKey) return; stop(); $('vx-save').focus(); return; }
  if (el.id === 'vx-d') { stop(); $('vx-b').focus(); return; }
  if (el.id === 'vx-b') { stop(); $('vx-p-q').focus(); return; }
  if (el.id === 'vx-p-q') { stop(); $('vx-r').focus(); return; }
  if (el.id === 'vx-r') { stop(); const first = vxLines()[0]; if (first) $(`vx-dc-${first.i}`).focus(); return; }
  if (el.classList.contains('ap-q')) { // the dropdown is closed: go on if an account is picked
    stop(); const i = +el.dataset.ap.split('-')[2];
    if ($(`vx-a-${i}`).value) $(`vx-m-${i}`).focus(); else vxEndLines(i);
    return;
  }
  if (el.classList.contains('vx-amt')) {
    stop(); if (vxAmt(el.value)) el.value = vxAmt(el.value).toFixed(2); vxShowTotals();
    const i = +el.id.split('-')[2], t = vxTotals(), diff = Math.round((t.dr - t.cr) * 100) / 100, ls = vxLines(), pos = ls.findIndex(l => l.i === i);
    if (!diff && ls.filter(l => l.code).length >= 2) { $('vx-n').focus(); return; }
    let nx = ls[pos + 1];
    const need = diff > 0 ? 'Cr' : 'Dr';
    if (!nx) { const j = vxAddLine(need); nx = { i: j }; } else if (!nx.code) vxSetSide(nx.i, need);
    $(`vx-dc-${nx.i}`).focus();
  }
}, true);

/* ---------- actions ---------- */
Object.assign(A, {
  bkTog: (id, el) => { UI.bkOpen[id] = el.dataset.open !== '1'; delete UI.bkAll[id.split(':')[0]]; rerenderBooks(); },
  bkAll: (scope, el) => { UI.bkAll[scope] = el.dataset.v === '1'; Object.keys(UI.bkOpen).forEach(k => { if (k.startsWith(scope + ':')) delete UI.bkOpen[k]; }); rerenderBooks(); },
  bkClients: side => clientsModal(side),
  bkVoucher: no => { const v = C.books.vch.find(x => x.no === no); if (v) voucherView(v); },
  bkEntry: (id, el) => entryView(id, el.dataset.back),
  bkNewAcct: head => accountModal(null, head),
  bkNewSub: id => headModal(null, id),
  bkNewHead: l1 => headModal(null, '', l1),
  bkHead: id => headModal(C.books.heads.find(h => h.id === id)),
  bkOrder: (id, el) => act(() => post(`/v1/account-heads/${id}/order`, { dir: el.dataset.v }), 'Order saved'),
  bkRemoveHead: id => { const h = C.books.heads.find(x => x.id === id); confirmModal('Remove ' + esc(h.name) + '?', 'It has no accounts and no sub-heads.', '', 'Remove', () => act(() => post(`/v1/account-heads/${id}/remove`), 'Head removed'), true); },
  bkMove: code => moveModal(code),
  bkMoveDo: (id, el) => { const a = el.dataset.a, m = C.books.moves.find(x => x.id === id);
    confirmModal((a === 'approve' ? 'Move ' : 'Reject the move of ') + esc(m.account_name), `${esc(m.from_name)} → ${esc(m.to_name)} · ${esc(m.reason)}`, `<div class="fld"><label for="md-n">${a === 'approve' ? 'Note (optional)' : 'Reason'}</label><input class="in" id="md-n"></div>`, a === 'approve' ? 'Approve move' : 'Reject', () => act(() => post(`/v1/account-moves/${id}/${a}`, { note: $('md-n').value.trim() }), a === 'approve' ? 'Account moved' : 'Move rejected'), a !== 'approve'); },
  bkAcctDo: (code, el) => { const a = el.dataset.a, x = C.books.acc.find(y => y.code === code);
    confirmModal((a === 'approve' ? 'Approve ' : 'Reject ') + esc(x.name), `Under ${esc(x.head_name)}${x.sub_head ? ' › ' + esc(x.sub_head) : ''} · made by ${esc(x.created_by)}`, a === 'approve' ? '' : '<div class="fld"><label for="ad-r">Reason</label><input class="in" id="ad-r"></div>', a === 'approve' ? 'Approve' : 'Reject', () => act(() => post(`/v1/accounts/${encodeURIComponent(code)}/decide`, { action: a, reason: $('ad-r')?.value.trim() || '' }), a === 'approve' ? 'Account approved' : 'Account rejected'), a !== 'approve'); },
  newVoucher: () => voucherModal(),
  vxLeave: () => vxLeave(true),
  vxDel: id => { const ls = vxLines(); if (ls.length <= 2) return; $(`vx-dc-${id}`)?.closest('.vx-l')?.remove(); VX.dirty = true; vxShowTotals(); },
  editAccount: code => accountModal(C.books.acc.find(a => a.code === code)),
  acctStmt: async code => { try { const st = await get(`/v1/ledger/statement?account=${encodeURIComponent(code)}&from=${fyStart(S.date)}&to=${S.date}`);
    modal(mhd('Ledger · ' + esc(bkName(code)), `${fmtDate(st.from)} – ${fmtDate(st.to)} · opening ${apDC(st.opening)} · closing ${apDC(st.closing)}`) + `<div class="bd">${tbl([['Date'], ['Narration'], ['Debit', 'r'], ['Credit', 'r'], ['Balance', 'r']], st.lines.map(l => `<tr class="click" data-act="bkEntry" data-id="${esc(l.entry_id)}" data-back="${esc(code)}" tabindex="0"><td class="n">${fmtDate(l.value_date)}</td><td class="wrap">${esc(l.narration)}</td><td class="r">${num(l.debit) ? nf(l.debit, 2) : ''}</td><td class="r">${num(l.credit) ? nf(l.credit, 2) : ''}</td><td class="r">${apDC(l.balance)}</td></tr>`), { tall: true, empty: 'No movements in this financial year.' })}<div class="dim small">Click a line to open its voucher.</div></div>`); } catch (e) { toast(e.message, true); } },
  vchDo: (id, el) => { const a = el.dataset.a, v = C.books.vch.find(x => x.no.replaceAll('/', '-') === id), na = v.new_accounts || [];
    const lines = `<div class="small">${v.lines.map(bkLine).join('<br>')}</div>`;
    const body = a === 'approve' ? lines + (na.length ? `<div class="note warn">Posting also approves ${na.length === 1 ? 'the new account' : 'these new accounts'}: ${na.map(x => `<b>${esc(x.name)}</b> under ${esc(x.head_name || x.group || '')}`).join(', ')}.</div>` : '')
      : `${lines}<div class="fld"><label for="vd-r">Reason</label><input class="in" id="vd-r"></div>${a === 'reject' && na.length ? na.map(x => `<label class="check"><input type="checkbox" class="vd-acc" value="${esc(x.code)}" checked> Reject the new account ${esc(x.name)} too</label>`).join('') : ''}`;
    confirmModal({ approve: 'Post ', reject: 'Reject ', cancel: 'Cancel ' }[a] + v.no, esc(v.narration), body, { approve: 'Post', reject: 'Reject', cancel: 'Cancel voucher' }[a], () => act(() => post(`/v1/vouchers/${id}/${a}`, { reason: $('vd-r')?.value || '', reject_accounts: [...document.querySelectorAll('.vd-acc:checked')].map(x => x.value) }), { approve: 'Voucher posted', reject: 'Voucher rejected', cancel: 'Voucher cancelled (reversed)' }[a]), a !== 'approve'); },
});
function rerenderBooks() { if (S.view !== 'books') return; const y = scrollY; $('page').innerHTML = groupBar('books') + V.books.html(C.books); bindPage(); scrollTo(0, y); }
// Enter on a ledger line opens it, as a click does.
document.addEventListener('keydown', e => { if (e.key === 'Enter' && e.target.matches?.('tr.click[data-act="bkEntry"]')) { e.preventDefault(); e.target.click(); } });
