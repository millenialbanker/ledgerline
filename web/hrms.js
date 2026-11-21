'use strict';
/* Ledgerline console: the People group (HRMS module, internal/ops/hrms.go
   and the files beside it) and Payroll inside it. Screens: My HR (the
   signed-in person's own records and check-in), Staff (employee master and
   org chart), Joiners & leavers (checklists tied to access), Certificates &
   own trading, Attendance, Leave, HR policies, Payroll, and Settings →
   Modules. With HRMS off the firm sees none of it (the server answers 404
   too). Managers see their team, branch managers their branch: the server
   decides what each person sees. Loaded after app.js. */

SCOPED_VIEWS.push('me', 'people', 'attendance', 'leave');

const HR_VIEWS = ['me', 'people', 'hrlife', 'hrcomp', 'attendance', 'leave', 'hrpolicy', 'payroll'];
// hrViewOK: whether this user sees an HR screen (app.js viewAllowed).
// The module switches always apply; which screens a person opens goes by
// their roles: by name for the built-in roles, by the screens of roles the
// firm made or edited (roles.js).
function hrViewOK(k) {
  const a = S.me?.access, seen = () => a.admin || !!a.screens?.[k]?.view;
  if (k === 'modules') return a ? seen() : role() === 'Admin';
  if (!HR_VIEWS.includes(k)) return true;
  const m = S.me?.modules || {};
  if (!m.hrms) return false;
  if (k === 'payroll' && !m.payroll) return false;
  if (a) return seen();
  if (k === 'payroll') return has('Payroll', 'HR manager');
  if (k === 'hrcomp' || k === 'hrpolicy') return has('HR manager', 'Compliance officer');
  if (k === 'hrlife') return has('HR manager', 'Compliance officer', 'Payroll');
  return true;
}

const hrThisMonth = () => new Date().toISOString().slice(0, 7);
const hrToday = () => new Date().toISOString().slice(0, 10);
const hrEnc = encodeURIComponent;
const HR_ST = { Active: 'good', Probation: 'acc', 'Leaving': 'warn', Left: 'nt', Valid: 'good', Expiring: 'warn', Expired: 'bad', Missing: 'bad', Open: 'warn', Done: 'good', Approved: 'good', Pending: 'warn', 'Pending approval': 'warn', Rejected: 'bad', Cancelled: 'nt', Withdrawn: 'nt', Present: 'good', 'Half day': 'warn', Absent: 'bad', 'On duty': 'acc', Leave: 'acc', Holiday: 'nt', 'Weekly off': 'nt', Draft: 'nt', Submitted: 'warn', Paid: 'good', Waiting: 'warn', Failed: 'bad' };
const hrPill = s => s ? pill(s, HR_ST[s] || 'nt') : '';
const hrDays = d => (+d || 0) % 1 ? (+d).toFixed(1) : String(+d || 0);
const MODE_NAME = { office: 'Office', home: 'Home', field: 'Field', onduty: 'On duty' };

// Reading a chosen file as base64 (punch photos, bills) or raw bytes.
const hrB64 = f => new Promise((ok, no) => { const r = new FileReader(); r.onload = () => ok(String(r.result).split(',')[1] || ''); r.onerror = no; r.readAsDataURL(f); });
async function hrUpload(url, input) {
  const f = $(input)?.files?.[0]; if (!f) throw new Error('Choose a file first');
  return api('POST', url + (url.includes('?') ? '&' : '?') + 'name=' + hrEnc(f.name), f, true);
}

/* ---------- a small editor for lists of rows (policies) ---------- */
const HR_ROWS = {};
function hrRows(id, cols, rows, can) {
  HR_ROWS[id] = cols;
  const cell = (c, v) => {
    const dis = can ? '' : 'disabled';
    if (c[2] === 'bool') return `<td><input type="checkbox" data-k="${c[0]}" data-t="bool" ${v ? 'checked' : ''} ${dis} aria-label="${esc(c[1])}"></td>`;
    if (Array.isArray(c[2])) return `<td><select class="in" data-k="${c[0]}" data-t="text" ${dis} aria-label="${esc(c[1])}">${c[2].map(o => `<option ${o === v ? 'selected' : ''}>${esc(o)}</option>`).join('')}</select></td>`;
    const val = c[2] === 'list' ? (v || []).join(', ') : (v ?? '');
    return `<td><input class="in ${c[2] === 'num' || c[2] === 'float' ? 'mono' : ''}" style="min-width:${c[3] || 90}px" data-k="${c[0]}" data-t="${c[2] || 'text'}" value="${esc(val)}" ${dis} aria-label="${esc(c[1])}"></td>`;
  };
  const row = r => `<tr data-r>${cols.map(c => cell(c, r[c[0]])).join('')}${can ? '<td><button class="btn sm" data-act="hrRowDel" aria-label="Remove">×</button></td>' : ''}</tr>`;
  return `<div class="tw"><table class="tbl" id="rows-${id}"><thead><tr>${cols.map(c => `<th>${esc(c[1])}</th>`).join('')}${can ? '<th></th>' : ''}</tr></thead><tbody>${(rows || []).map(row).join('')}</tbody></table></div>${can ? `<button class="btn sm" data-act="hrRowAdd" data-id="${id}">Add a row</button>` : ''}`;
}
function hrRowsRead(id) {
  return [...document.querySelectorAll(`#rows-${id} tbody tr[data-r]`)].map(tr => {
    const o = {};
    tr.querySelectorAll('[data-k]').forEach(x => {
      const t = x.dataset.t, v = x.type === 'checkbox' ? x.checked : x.value.trim();
      o[x.dataset.k] = t === 'bool' ? v : t === 'num' ? (parseInt(v, 10) || 0) : t === 'float' ? (parseFloat(v) || 0) : t === 'list' ? v.split(',').map(s => s.trim()).filter(Boolean) : v;
    });
    return o;
  });
}

/* ============================== My HR ============================== */
V.me = {
  load: () => get('/v1/hr/me?month=' + (UI.hrMeMonth || hrThisMonth())),
  html: d => {
    if (!d.linked) return ph('My HR', 'Your own attendance, leave, payslips and declarations.') + '<div class="note">Your login is not linked to an employee record yet. Ask HR to link it on Staff.</div>';
    const p = d.profile, a = d.attendance, s = a.summary || {}, modes = (a.policy?.modes || ['office']);
    const today = (a.days || []).find(x => x.date === hrToday());
    const checkin = `<div class="panel"><div class="hd"><h3>Check in or out</h3><small>${today ? `Today: ${hrPill(today.status)} ${today.in ? 'in ' + esc(today.in) : ''}${today.out ? ' · out ' + esc(today.out) : ''}` : ''}</small></div><div class="bd">
      <div class="form"><div class="fld"><label for="me-mode">Where you are working</label><select class="in" id="me-mode">${modes.map(m => `<option value="${m}">${esc(MODE_NAME[m] || m)}</option>`).join('')}</select></div>
      <div class="fld"><label for="me-note">Note (needed for field and on-duty: whom you are visiting, where)</label><input class="in" id="me-note" maxlength="300"></div>
      <label class="check"><input type="checkbox" id="me-loc"> Send my location (needed for field punches; an office punch is accepted on the office network or within the office's radius)</label></div>
      <div class="tools"><button class="btn pri" data-act="hrPunch" data-id="in">Check in</button><button class="btn" data-act="hrPunch" data-id="out">Check out</button><button class="btn" data-act="hrWFHReq">Ask to work from home</button></div>
      <p class="dim small">Policy: ${esc(a.policy?.name || a.policy?.id || '—')}${a.policy?.office_per_week ? ` · ${a.policy.office_per_week} office days a week` : ''}${a.policy?.office_per_month ? ` · ${a.policy.office_per_month} office days a month` : ''}${(a.policy?.fixed_home_days || []).length ? ` · home on ${a.policy.fixed_home_days.join(', ')}` : ''}${a.policy?.any_home_per_week ? ` · any ${a.policy.any_home_per_week} home days a week` : ''}. The staff phone app checks in the same way after unlocking with your phone's fingerprint or face.</p></div></div>`;
    const month = `<div class="panel"><div class="hd"><h3>Attendance</h3><input class="in mono" type="month" id="me-month" value="${esc(a.month)}" aria-label="Month" style="height:30px"></div>
      <div class="kpis">${kpi('Office', s.office || 0, s.need_office ? 'needed ' + s.need_office : '')}${kpi('Home', s.home || 0)}${kpi('Field', s.field || 0)}${kpi('Leave', s.leave || 0)}${kpi('Absent', s.absent || 0, '', s.absent ? 'bad' : '')}</div>
      ${(s.exceptions || []).length ? `<div class="note warn">${s.exceptions.map(esc).join('<br>')}</div>` : ''}
      ${hrDaysTable(a.days || [], false)}</div>`;
    const bal = d.leave.balances || [];
    const leave = `<div class="panel"><div class="hd"><h3>Leave · ${esc(d.leave.year || '')}</h3><button class="btn sm pri" data-act="hrLeaveApply">Apply for leave</button></div>
      ${tbl([['Type'], ['Carried', 'r'], ['Granted', 'r'], ['Accrued', 'r'], ['Taken', 'r'], ['Waiting', 'r'], ['Balance', 'r']], bal.map(b => `<tr><td>${esc(b.name)} <span class="dim">${esc(b.type)}</span></td><td class="r">${hrDays(b.carried)}</td><td class="r">${hrDays(b.granted)}</td><td class="r">${hrDays(b.accrued)}</td><td class="r">${hrDays(b.taken)}</td><td class="r">${hrDays(b.pending)}</td><td class="r"><b>${hrDays(b.balance)}</b></td></tr>`))}
      ${tbl([['From'], ['To'], ['Type'], ['Days', 'r'], ['Status'], ['']], (d.leave.requests || []).slice(0, 20).map(r => `<tr><td>${fmtDate(r.from)}</td><td>${fmtDate(r.to)}</td><td>${esc(r.type)}</td><td class="r">${hrDays(r.days)}</td><td>${hrPill(r.status)}${r.note ? ` <span class="dim small">${esc(r.note)}</span>` : ''}</td><td>${['Pending', 'Approved'].includes(r.status) && r.from >= hrToday() ? `<button class="btn sm" data-act="hrLeaveCancel" data-id="${esc(r.id)}">Cancel</button>` : ''}</td></tr>`), { empty: 'No leave requests.' })}</div>`;
    const pay = d.payroll ? `<div class="panel"><div class="hd"><h3>Payslips and tax</h3><small>Financial year ${esc(d.fy)}</small></div><div class="bd">
      ${tbl([['Month'], ['Gross', 'r'], ['Net', 'r'], ['']], (d.payslips || []).map(x => `<tr><td>${esc(x.month)}</td><td class="r">${inr(x.gross)}</td><td class="r"><b>${inr(x.net)}</b></td><td><button class="btn sm" data-act="dl" data-id="/v1/payroll/runs/${esc(x.month)}/payslips/${esc(p.code)}" data-name="payslip-${esc(x.month)}.pdf">Payslip</button></td></tr>`), { empty: 'No payslips yet.' })}
      <div class="tools"><button class="btn" data-act="hrTaxDecl">Tax declaration${d.tax_declaration ? ' · ' + esc(d.tax_declaration.status) : ''}</button><button class="btn" data-act="hrTaxProof">Upload a proof</button><button class="btn" data-act="hrClaim">Claim a reimbursement</button><button class="btn" data-act="dl" data-id="/v1/payroll/form130/${esc(p.code)}?fy=${esc(d.fy)}" data-name="form130-${esc(d.fy)}.pdf">Form 130 Part B (was Form 16)</button></div>
      ${(d.claims || []).length ? tbl([['Date'], ['Claim'], ['Amount', 'r'], ['Status']], d.claims.map(c => `<tr><td>${fmtDate(c.date)}</td><td>${esc(c.kind)} <span class="dim small">${esc(c.note || '')}</span></td><td class="r">${inr(c.amount)}</td><td>${hrPill(c.status)}${c.paid_in ? ' <span class="dim small">paid in ' + esc(c.paid_in) + '</span>' : ''}</td></tr>`)) : ''}</div></div>` : '';
    const ot = d.own_trading || {};
    const trading = `<div class="panel"><div class="hd"><h3>Own trading</h3><small>The firm's code of conduct: declare your accounts and your relatives', holdings each year, and ask before trading where the code requires</small></div><div class="bd">
      ${tbl([['Holder'], ['Relation'], ['Broker / DP'], ['Account'], ['Outside the firm']], (ot.accounts || []).map(x => `<tr><td>${esc(x.holder)}</td><td>${esc(x.relation)}</td><td>${esc(x.broker)}</td><td class="mono">${esc(x.account)}</td><td>${x.outside ? pill('Outside', 'warn') : ''}</td></tr>`), { empty: 'No accounts declared.' })}
      ${tbl([['Pre-clearance'], ['Security'], ['Side'], ['Qty', 'r'], ['Status'], ['Valid till']], (ot.pre_clearances || []).slice(0, 15).map(x => `<tr><td class="mono">${esc(x.id)}</td><td>${esc(x.security)}</td><td>${esc(x.side)}</td><td class="r">${nf(x.qty)}</td><td>${hrPill(x.status)}${(x.warnings || []).map(w => ' ' + pill(w, 'warn')).join('')}</td><td>${fmtDate(x.valid_till)}</td></tr>`), { empty: 'No pre-clearance requests.' })}
      <div class="tools"><button class="btn" data-act="hrDeclAcct">Declare an account</button><button class="btn" data-act="hrDeclHold">Declare holdings</button><button class="btn" data-act="hrPreClear">Ask for pre-clearance</button></div></div></div>`;
    const certs = `<div class="panel"><div class="hd"><h3>Certificates</h3><button class="btn sm" data-act="hrMyCert">Add a certificate</button></div>${tbl([['Certificate'], ['Needed for'], ['Number'], ['Valid till'], ['State']], (p.cert_status || []).map(c => `<tr><td>${esc(c.cert)}</td><td class="small">${esc(c.duty || '')}</td><td class="mono">${esc(c.number || '')}</td><td>${fmtDate(c.valid_till)}</td><td>${hrPill(c.state)}</td></tr>`), { empty: 'Your duties need no certificate.' })}</div>`;
    const lists = (d.checklists || []).length ? `<div class="panel"><div class="hd"><h3>Your ${esc(d.checklists[0].kind === 'leave' ? 'leaving' : 'joining')} checklist</h3></div><div class="bd"><ul class="small">${d.checklists.flatMap(c => c.items).map(i => `<li>${i.done ? '✓ ' : i.na ? '— ' : ''}${esc(i.label)}</li>`).join('')}</ul></div></div>` : '';
    return ph('My HR', `${esc(p.name)} · ${esc(p.code)} · ${esc(p.designation || '')}${p.manager_name ? ' · reports to ' + esc(p.manager_name) : ''}`, `<button class="btn" data-act="hrMyPIN">Kiosk PIN${a.pin_set ? ' · set' : ''}</button>${d.manager ? `<button class="btn" data-act="go" data-id="leave">My team (${d.team_size})</button>` : ''}`) +
      lists + checkin + month + leave + pay + trading + certs +
      `<div class="panel"><div class="hd"><h3>My details</h3><small>Identifiers are masked. Ask HR to correct anything.</small></div><div class="bd grid g3 small">${hrFacts(p)}</div></div>`;
  }
};

function hrFacts(p, rev = () => '') {
  const f = (l, v) => `<div><span class="dim">${l}</span><div>${v || '—'}</div></div>`;
  return f('Department', esc(p.department)) + f('Designation', esc(p.designation)) + f('Office', esc(p.branch || 'Head office')) + f('Joined', fmtDate(p.joined_on)) + f('Employment', esc(p.employment_type)) + f('Status', hrPill(p.status)) +
    f('Email', esc(p.email)) + f('Mobile', esc(p.mobile)) + f('PAN', `<span class="mono">${esc(p.pan)}</span>${p.pan ? rev('pan') : ''}`) + f('Aadhaar', `<span class="mono">${esc(p.aadhaar4)}</span>`) + f('UAN', `<span class="mono">${esc(p.uan)}</span>${p.uan ? rev('uan') : ''}`) + f('ESIC number', `<span class="mono">${esc(p.esic)}</span>${p.esic ? rev('esic') : ''}`) +
    f('Salary account', `<span class="mono">${esc(p.bank?.account)}</span> ${esc(p.bank?.ifsc || '')}${p.bank?.account ? rev('account') : ''}`) + f('Duties', esc((p.duties || []).join(', '))) + f('Ledgerline login', esc(p.user || '')) + f('Tax regime', esc(p.tax_regime || 'new'));
}

function hrDaysTable(days, canMark, code) {
  return tbl([['Date'], ['Day'], ['Status'], ['In'], ['Out'], ['Mode'], ['Punches'], canMark ? [''] : ['']], days.map(x => {
    const ph = (x.punches || []).map(p => `<span class="small">${esc((p.at || '').slice(11, 16))} ${esc(p.kind)} ${esc(MODE_NAME[p.mode] || p.mode)} <span class="dim">${esc(p.source)}${p.site ? ' · ' + esc(p.site) + (p.how ? ' (' + esc(p.how) + ')' : '') : ''}${p.has_loc ? ` · ${(+p.lat).toFixed(4)},${(+p.lng).toFixed(4)}` : ''}${p.note ? ' · ' + esc(p.note) : ''}</span>${p.selfie && canMark ? ` <button class="linkish" data-act="hrPhoto" data-id="${esc(p.selfie)}">photo</button>` : ''}</span>`).join('<br>');
    return `<tr><td class="n">${fmtDate(x.date)}</td><td class="dim small">${esc(x.kind || '')}</td><td>${hrPill(x.status)}${x.leave ? ' ' + esc(x.leave) : ''}${x.marked ? ` <span class="dim small" title="${esc(x.note || '')}">marked by ${esc(x.marked)}</span>` : ''}</td><td class="mono">${esc(x.in || '')}</td><td class="mono">${esc(x.out || '')}</td><td>${esc(MODE_NAME[x.mode] || x.mode || '')}</td><td>${ph}</td><td>${canMark ? `<button class="btn sm" data-act="hrMark" data-id="${esc(code)}|${esc(x.date)}">Mark</button>` : ''}</td></tr>`;
  }), { tall: true, empty: 'No days.' });
}

/* ============================== Staff ============================== */
V.people = {
  load: () => { UI.hrPeopleTab ||= 'list'; return Promise.all([get('/v1/hr/staff'), get('/v1/hr/org').catch(() => []), has('HR manager', 'Compliance officer') ? get('/v1/hr/kmp').catch(() => []) : Promise.resolve([])]).then(([staff, org, kmp]) => ({ staff, org, kmp })); },
  html: d => {
    const s = d.staff, q = (UI.hrQ || '').toLowerCase(), tab = UI.hrPeopleTab || 'list';
    const rows = s.employees.filter(e => !q || (e.name + ' ' + e.code + ' ' + (e.designation || '') + ' ' + (e.department || '') + ' ' + (e.branch || '')).toLowerCase().includes(q));
    const tabs = segBtns('hrPeopleTab', [['list', 'Staff'], ['org', 'Org chart'], ...(s.all ? [['kmp', 'KMP & designated persons']] : [])]);
    let body = '';
    if (tab === 'org') body = `<div class="panel"><div class="bd">${hrOrgTree(d.org)}</div></div>`;
    else if (tab === 'kmp') body = `<div class="panel"><div class="hd"><h3>Key managerial personnel and designated persons</h3><small>KMP positions and designated persons under the insider trading code are set on each person</small></div>${tbl([['Code'], ['Name'], ['Designation'], ['KMP position'], ['Designated person'], ['PAN'], ['Joined'], ['Left'], ['Status']], d.kmp.map(k => `<tr><td class="mono">${esc(k.code)}</td><td>${esc(k.name)}</td><td>${esc(k.designation)}</td><td>${esc(k.kmp || '')}</td><td>${k.designated ? 'Yes' : ''}</td><td class="mono">${esc(k.pan)}</td><td>${fmtDate(k.joined_on)}</td><td>${fmtDate(k.left_on)}</td><td>${hrPill(k.status)}</td></tr>`), { empty: 'No KMP or designated persons marked.' })}</div>`;
    else body = `<div class="panel"><div class="hd"><input class="in" id="hr-q" placeholder="Find by name, code, office" value="${esc(UI.hrQ || '')}" style="max-width:320px"><small>${rows.length} of ${s.employees.length}</small></div>
      ${tbl([['Code'], ['Name'], ['Designation'], ['Office'], ['Manager'], ['Joined'], ['Certificates'], ['Status'], ['']], rows.map(e => {
        const bad = (e.cert_status || []).filter(c => c.state !== 'Valid');
        return `<tr><td class="mono"><a href="#" data-act="hrOpen" data-id="${esc(e.code)}">${esc(e.code)}</a></td><td><b>${esc(e.name)}</b><div class="dim small">${esc(e.department || '')}</div></td><td>${esc(e.designation || '')}</td><td>${esc(e.branch || 'Head office')}${e.partner ? ' · AP ' + esc(e.partner) : ''}</td><td>${esc(e.manager_name || '')}</td><td>${fmtDate(e.joined_on)}</td><td>${(e.cert_status || []).length ? (bad.length ? bad.map(c => hrPill(c.state)).join(' ') : pill('Valid', 'good')) : '<span class="dim">—</span>'}${e.terminal_flag ? ' ' + pill('Terminal flagged', 'bad') : ''}</td><td>${hrPill(e.status)}${e.team ? '' : (e.open_tasks ? ` <span class="ct">${e.open_tasks}</span>` : '')}</td><td><button class="btn sm" data-act="hrOpen" data-id="${esc(e.code)}">Open</button></td></tr>`;
      }), { tall: true, empty: 'No staff yet.' })}</div>`;
    const c = s.counts || {};
    return ph('Staff', s.all ? 'The firm’s own people: employee master, documents, certificates and org chart. PAN, Aadhaar (last 4 digits only), UAN, ESIC number and the salary account are masked; revealing one asks for a reason and is recorded.' : 'Your team. Identifiers and pay are for HR only.',
      s.can_edit ? '<button class="btn pri" data-act="hrEdit">Add employee</button>' : '') +
      (s.all ? `<div class="kpis">${kpi('Active', c.Active || 0)}${kpi('On probation', c.Probation || 0)}${kpi('Leaving', c.Leaving || 0, '', c.Leaving ? 'warn' : '')}${kpi('Left', c.Left || 0)}</div>` : '') + tabs + body;
  }
};
function hrOrgTree(nodes) {
  if (!nodes || !nodes.length) return '<p class="dim">No one yet.</p>';
  return `<ul class="small" style="margin:4px 0;padding-left:18px">${nodes.map(n => `<li style="margin:4px 0"><a href="#" data-act="hrOpen" data-id="${esc(n.code)}"><b>${esc(n.name)}</b></a> <span class="dim">${esc(n.designation || '')}${n.branch ? ' · ' + esc(n.branch) : ''}</span>${(n.reports || []).length ? hrOrgTree(n.reports) : ''}</li>`).join('')}</ul>`;
}

// The person's drawer: details, documents, certificates, access actions.
async function hrOpenPerson(code) {
  let e; try { e = await get('/v1/hr/staff/' + hrEnc(code)); } catch (err) { toast(err.message, true); return; }
  const s = C.people?.staff || {}, hr = s.can_edit, comp = has('Compliance officer');
  const rev = f => hr && !e.team ? ` <button class="linkish small" data-act="hrReveal" data-id="${esc(e.code)}|${f}">Reveal</button>` : '';
  const docs = (e.docs || []).map(x => `<tr><td>${esc(x.kind)}</td><td><a href="#" data-act="hrDoc" data-id="${esc(e.code)}|${esc(x.id)}">${esc(x.name)}</a></td><td>${x.expiry ? fmtDate(x.expiry) : '—'}</td><td class="dim small">${esc(x.by)}</td></tr>`);
  const certs = (e.cert_status || []).map(c => `<tr><td>${esc(c.cert)}</td><td class="small">${esc(c.duty || '')}${c.blocks_terminal ? ' · terminal' : ''}</td><td class="mono">${esc(c.number || '')}</td><td>${fmtDate(c.valid_till)}</td><td>${hrPill(c.state)}</td></tr>`);
  const hd = e.home_dealing || {};
  modal(mhd(`${esc(e.name)} · ${esc(e.code)}`, `${esc(e.designation || '')}${e.department ? ' · ' + esc(e.department) : ''} · ${hrPill(e.status)}${e.terminal_flag ? ' ' + pill('Terminal flagged: a certificate has run out', 'bad') : ''}`) + `<div class="bd">
    ${e.team ? '<div class="note small">You see this person as their manager: no identifiers or pay.</div>' : ''}
    <div class="grid g3 small">${hrFacts(e, rev)}</div>
    ${(e.duties || []).some(x => x.startsWith('Dealer') || x === 'AP staff (derivatives)') ? `<div class="note small">Dealing from home: ${hd.approved ? `exchange-approved (${esc(hd.ref)}, ${fmtDate(hd.date)})` : 'not an exchange-approved dealing location, so this person cannot mark attendance from home. Dealing from a location the exchange has not approved needs the exchange’s permission.'}${comp ? ` <button class="linkish" data-act="hrHomeDeal" data-id="${esc(e.code)}">Record approval</button>` : ''}</div>` : ''}
    <h4>Certificates</h4>${tbl([['Certificate'], ['Needed for'], ['Number'], ['Valid till'], ['State']], certs, { empty: 'The duties need no certificate.' })}
    ${!e.team ? `<h4>Documents</h4>${tbl([['Kind'], ['File'], ['Expires'], ['Added by']], docs, { empty: 'No documents.' })}` : ''}
    </div><div class="ft" style="flex-wrap:wrap;gap:6px">
    <button class="btn" data-act="hrAttend" data-id="${esc(e.code)}">Attendance</button>
    ${hr ? `<button class="btn" data-act="hrEdit" data-id="${esc(e.code)}">Edit</button><button class="btn" data-act="hrDocAdd" data-id="${esc(e.code)}">Add document</button><button class="btn" data-act="hrLink" data-id="${esc(e.code)}">Link login</button><button class="btn" data-act="hrDevApprove" data-id="${esc(e.code)}">Approve new phone</button><button class="btn" data-act="hrPINUnlock" data-id="${esc(e.code)}">Unlock kiosk PIN</button><button class="btn" data-act="hrLeaveAdj" data-id="${esc(e.code)}">Adjust leave</button>` : ''}
    ${hr || comp ? `<button class="btn" data-act="hrCertAdd" data-id="${esc(e.code)}">Add certificate</button>` : ''}
    ${hr && !['Left', 'Leaving'].includes(e.status) ? `<button class="btn bad" data-act="hrLeaving" data-id="${esc(e.code)}">Record leaving</button>` : ''}
    </div>`, true);
}

function hrEmpForm(e) {
  const s = C.people.staff, F = (id, l, v, o = {}) => `<div class="fld ${o.full ? 'full' : ''}"><label for="he-${id}">${l}</label><input class="in ${o.mono ? 'mono' : ''}" id="he-${id}" value="${esc(v ?? '')}" ${o.type ? `type="${o.type}"` : ''} ${o.ph ? `placeholder="${esc(o.ph)}"` : ''}></div>`;
  const sel = (id, l, opts, v, blank) => `<div class="fld"><label for="he-${id}">${l}</label><select class="in" id="he-${id}">${blank ? `<option value="">${blank}</option>` : ''}${opts.map(o => { const [k, t] = Array.isArray(o) ? o : [o, o]; return `<option value="${esc(k)}" ${k === v ? 'selected' : ''}>${esc(t)}</option>`; }).join('')}</select></div>`;
  const mgrs = s.employees.filter(x => x.code !== e.code && x.status !== 'Left').map(x => [x.code, x.name + ' (' + x.code + ')']);
  return `<div class="form">${F('name', 'Name', e.name)}${F('code', 'Employee code (blank: next number)', e.code, { mono: 1 })}${F('dept', 'Department', e.department)}${F('desig', 'Designation', e.designation)}
    ${sel('branch', 'Office', s.branches, e.branch, 'Head office')}${sel('mgr', 'Manager', mgrs, e.manager, 'None')}${sel('type', 'Employment type', s.types, e.employment_type)}${sel('gender', 'Gender', [['F', 'Female'], ['M', 'Male'], ['O', 'Other']], e.gender, '—')}
    ${F('join', 'Date of joining', e.joined_on, { type: 'date' })}${F('dob', 'Date of birth', e.dob, { type: 'date' })}${F('email', 'Work email', e.email)}${F('mobile', 'Mobile', e.mobile)}
    ${F('pan', 'PAN', e.pan, { mono: 1 })}${F('aad', 'Aadhaar: last 4 digits only', e.aadhaar4, { mono: 1, ph: '1234' })}${F('uan', 'UAN', e.uan, { mono: 1 })}${F('esic', 'ESIC number', e.esic, { mono: 1 })}
    ${F('holder', 'Salary account holder', e.bank?.holder)}${F('acct', 'Salary account number', e.bank?.account, { mono: 1 })}${F('ifsc', 'IFSC', e.bank?.ifsc, { mono: 1 })}${F('state', 'State of work (professional tax)', e.state)}
    ${sel('regime', 'Tax regime', [['new', 'New (default)'], ['old', 'Old']], e.tax_regime || 'new')}${F('wp', 'Work policy ID', e.work_policy)}${F('shift', 'Shift code', e.shift)}${F('dev', 'Attendance device user ID (CSV upload)', e.device_id, { mono: 1 })}
    ${F('kmp', 'KMP position (blank if none)', e.kmp)}${F('dealer', 'Dealers register ID (if a dealer)', e.dealer, { mono: 1 })}${F('emname', 'Emergency contact', e.emergency?.name)}${F('emphone', 'Emergency phone', e.emergency?.phone)}
    <div class="fld full"><label>Duties (decide the certificates needed and the joining checklist)</label><div>${s.duties.map(d => `<label class="check" style="display:inline-flex;margin-right:12px"><input type="checkbox" data-duty="${esc(d)}" ${(e.duties || []).includes(d) ? 'checked' : ''}> ${esc(d)}</label>`).join('')}</div></div>
    <label class="check"><input type="checkbox" id="he-desig-p" ${e.designated ? 'checked' : ''}> Designated person (insider trading code)</label><label class="check"><input type="checkbox" id="he-eps" ${e.eps_member !== false ? 'checked' : ''}> EPS member</label><label class="check"><input type="checkbox" id="he-nopf" ${e.no_pf ? 'checked' : ''}> Not a PF member</label><label class="check"><input type="checkbox" id="he-dis" ${e.disabled ? 'checked' : ''}> Person with disability</label>
    ${F('notes', 'Notes', e.notes, { full: 1 })}</div><p class="dim small">Masked values sent back unchanged keep what is stored.</p>`;
}

/* ============================== Joiners & leavers ============================== */
V.hrlife = {
  load: () => Promise.all([get('/v1/hr/checklists' + (UI.hrAllLists ? '?all=1' : '')), has('HR manager', 'Compliance officer') ? get('/v1/hr/leavers') : Promise.resolve([])]).then(([lists, leavers]) => ({ lists, leavers })),
  html: d => {
    const me = S.me?.actor, hr = has('HR manager');
    const pend = d.leavers.filter(l => l.status === 'Pending approval' || l.status === 'Approved');
    return ph('Joiners & leavers', 'Joining raises a checklist: the Ledgerline login, the terminal and the exchange registration for a dealer, certificates, background check, code of conduct, own-trading declarations. A change of role or office raises an access review. A leaving needs a second person’s approval; the login is switched off on the last day by itself, and the work to withdraw terminals, registrations, devices, bank roles and email goes to the people who do it.',
      `<label class="check"><input type="checkbox" id="hr-alllists" ${UI.hrAllLists ? 'checked' : ''}> Show finished checklists</label>`) +
      (pend.length || d.leavers.length ? `<div class="panel"><div class="hd"><h3>Leavers</h3></div>${tbl([['Employee'], ['Last day'], ['Reason'], ['Recorded by'], ['Approved by'], ['Login'], ['Status'], ['']], d.leavers.map(l => `<tr><td><b>${esc(l.name)}</b> <span class="mono dim">${esc(l.employee)}</span></td><td>${fmtDate(l.last_day)}</td><td class="small">${esc(l.reason)}</td><td class="small">${esc(l.requested_by)}</td><td class="small">${esc(l.approved_by || '—')}</td><td class="small">${esc(l.user || '—')}${l.deactivated ? '<div class="dim">off ' + esc(fmtTS(l.deactivated)) + '</div>' : ''}</td><td>${hrPill(l.status)}</td><td>${hr && l.status === 'Pending approval' && l.requested_by !== me ? `<button class="btn sm pri" data-act="hrLeaveApprove" data-id="${esc(l.employee)}">Approve</button>` : ''}${hr && ['Pending approval', 'Approved'].includes(l.status) ? `<button class="btn sm" data-act="hrLeaveWithdraw" data-id="${esc(l.employee)}">Withdraw</button>` : ''}${has('Payroll') && S.me?.modules?.payroll ? `<button class="btn sm" data-act="payFnF" data-id="${esc(l.employee)}">Full and final</button>` : ''}</td></tr>`))}</div>` : '') +
      (d.lists.length ? d.lists.map(c => `<div class="panel"><div class="hd"><h3>${esc({ join: 'Joining', move: 'Access review', leave: 'Leaving' }[c.kind] || c.kind)}: ${esc(c.name)} <span class="mono dim">${esc(c.employee)}</span></h3><small>${fmtDate(c.date)} · ${hrPill(c.status)}</small></div><div class="bd"><p class="small" style="margin-top:0">${esc(c.detail || '')}</p>
        ${tbl([['To do'], ['Who'], ['Done'], ['']], c.items.map(i => `<tr><td>${esc(i.label)}</td><td class="small">${esc(i.role)}</td><td class="small">${i.done ? pill('Done', 'good') : i.na ? pill('Not applicable', 'nt') : pill('Open', 'warn')} ${i.by ? esc(i.by) : ''}${i.note ? `<div class="dim">${esc(i.note)}</div>` : ''}</td><td>${!i.done && !i.na && i.key !== 'deactivate' && (has('HR manager') || role() === i.role) ? `<button class="btn sm" data-act="hrItem" data-id="${esc(c.id)}|${esc(i.key)}|0">Done</button><button class="btn sm" data-act="hrItem" data-id="${esc(c.id)}|${esc(i.key)}|1">Not applicable</button>` : ''}</td></tr>`))}</div></div>`).join('') : '<div class="note">No open checklists.</div>');
  }
};

/* ============================== Certificates & own trading ============================== */
V.hrcomp = {
  load: () => Promise.all([get('/v1/hr/certificates'), get('/v1/hr/own-trading')]).then(([certs, trading]) => ({ certs, trading })),
  html: d => {
    const r = d.certs, t = d.trading, comp = has('Compliance officer');
    const bad = r.rows.filter(x => x.state !== 'Valid');
    return ph('Certificates & own trading', `Each person’s certificates against the firm’s register of which duties need which certificate. Reminders go ${(r.reminder_days || []).join(', ')} days before expiry; an expired certificate raises a work item and flags the person’s terminal. Own trading: accounts (with relatives’ and outside accounts), annual holdings declarations and pre-clearance under the firm’s code.`) +
      `<div class="kpis">${kpi('Certificates needed', r.rows.length)}${kpi('Expired or missing', r.rows.filter(x => ['Expired', 'Missing'].includes(x.state)).length, '', bad.length ? 'bad' : 'good')}${kpi('Expiring', r.rows.filter(x => x.state === 'Expiring').length, '', 'warn')}${kpi('Terminals flagged', r.rows.filter(x => x.flagged).length, '', r.rows.some(x => x.flagged) ? 'bad' : '')}</div>` +
      `<div class="panel"><div class="hd"><h3>Certificate register</h3><small>The rules are on HR policies</small></div>${tbl([['Person'], ['Office'], ['Certificate'], ['Needed for'], ['Number'], ['Valid till'], ['State'], ['Terminals']], r.rows.map(x => `<tr><td><a href="#" data-act="hrOpen" data-id="${esc(x.code)}">${esc(x.name)}</a> <span class="mono dim">${esc(x.code)}</span></td><td>${esc(x.branch || 'Head office')}${x.partner ? ' · AP ' + esc(x.partner) : ''}</td><td>${esc(x.cert)}</td><td class="small">${esc(x.duty)}</td><td class="mono">${esc(x.number || '')}</td><td>${fmtDate(x.valid_till)}${x.days && x.state !== 'Missing' ? ` <span class="dim small">${x.days < 0 ? -x.days + ' days ago' : x.days + ' days'}</span>` : ''}</td><td>${hrPill(x.state)}</td><td class="small">${(x.terminals || []).map(esc).join(', ')}${x.flagged ? ' ' + pill('Flagged', 'bad') : ''}</td></tr>`), { tall: true, empty: 'No one’s duties need a certificate.' })}</div>` +
      `<div class="panel"><div class="hd"><h3>Pre-clearance requests</h3><small>${esc(t.policy?.basis || '')}</small></div>${tbl([['Ref'], ['Person'], ['Account'], ['Security'], ['Side'], ['Qty', 'r'], ['Value', 'r'], ['Status'], ['']], (t.pre_clearances || []).map(x => `<tr><td class="mono">${esc(x.id)}</td><td>${esc(x.name)}</td><td class="mono small">${esc(x.account)}</td><td>${esc(x.security)}</td><td>${esc(x.side)}</td><td class="r">${nf(x.qty)}</td><td class="r">${inr(x.value)}</td><td>${hrPill(x.status)}${(x.warnings || []).map(w => ' ' + pill(w, 'warn')).join('')}${x.valid_till ? ' <span class="dim small">till ' + fmtDate(x.valid_till) + '</span>' : ''}</td><td>${comp && x.status === 'Pending' ? `<button class="btn sm pri" data-act="hrPCDecide" data-id="${esc(x.id)}|1">Approve</button><button class="btn sm" data-act="hrPCDecide" data-id="${esc(x.id)}|0">Refuse</button>` : ''}</td></tr>`), { empty: 'No requests.' })}</div>` +
      `<div class="panel"><div class="hd"><h3>Declared accounts</h3><small>${(t.missing_annual || []).length ? pill((t.missing_annual || []).length + ' annual declarations missing', 'warn') : ''}</small></div>${tbl([['Person'], ['Holder'], ['Relation'], ['Broker / DP'], ['Account'], ['Outside'], ['Declared']], (t.accounts || []).map(a => `<tr><td class="mono">${esc(a.employee)}</td><td>${esc(a.holder)}</td><td>${esc(a.relation)}</td><td>${esc(a.broker)}</td><td class="mono">${esc(a.account)}</td><td>${a.outside ? pill('Outside', 'warn') : ''}</td><td class="small">${fmtDate(a.declared_on)}</td></tr>`), { empty: 'No accounts declared.' })}
        ${(t.missing_annual || []).length ? `<div class="bd small">Annual holdings declarations missing: ${t.missing_annual.map(esc).join(', ')}</div>` : ''}</div>` +
      `<div class="panel"><div class="hd"><h3>Holdings declarations</h3></div>${tbl([['Person'], ['Kind'], ['Period'], ['Lines', 'r'], ['Declared']], (t.declarations || []).map(x => `<tr><td class="mono">${esc(x.employee)}</td><td>${esc(x.kind)}</td><td>${esc(x.period || '')}</td><td class="r">${x.nil ? 'Nil' : (x.holdings || []).length}</td><td class="small">${fmtDate(x.declared_on)} · ${esc(x.declared_by)}</td></tr>`), { empty: 'None yet.' })}</div>`;
  }
};

/* ============================== Attendance ============================== */
V.attendance = {
  load: () => {
    const m = UI.hrAttMonth || hrThisMonth(), all = has('HR manager');
    return Promise.all([get('/v1/hr/attendance/report?month=' + m), all ? get('/v1/hr/devices') : Promise.resolve([]), all ? get('/v1/hr/kiosks') : Promise.resolve([])]).then(([report, devices, kiosks]) => ({ month: m, report, devices, kiosks }));
  },
  html: d => {
    const hr = has('HR manager'), pend = d.devices.filter(x => x.pending);
    return ph('Attendance', 'Check-ins from the staff phone app (one registered phone each, unlocked with the phone’s own fingerprint or face: Ledgerline keeps no biometrics), the office kiosk, the console, and CSV files from any attendance device. Office check-ins count only on the office network or within the office’s radius. Photos are for the manager to review and are deleted after the policy’s days.',
      `<input class="in mono" type="month" id="hr-att-month" value="${esc(d.month)}" aria-label="Month" style="height:30px">${hr ? '<button class="btn" data-act="hrImport">Upload a CSV from a device</button>' : ''}<button class="btn" data-act="hrRaise">Send exceptions to managers</button>`) +
      `<div class="panel"><div class="hd"><h3>Office and home days against each person’s policy</h3><small>${esc(d.month)}</small></div>${tbl([['Person'], ['Policy'], ['Office', 'r'], ['Needed', 'r'], ['Home', 'r'], ['Field', 'r'], ['On duty', 'r'], ['Leave', 'r'], ['Absent', 'r'], ['Exceptions'], ['']], d.report.map(r => { const s = r.summary; return `<tr><td><b>${esc(r.name)}</b> <span class="mono dim">${esc(r.code)}</span></td><td class="small">${esc(r.policy)}</td><td class="r">${s.office}</td><td class="r">${s.need_office || '—'}</td><td class="r">${s.home}</td><td class="r">${s.field}</td><td class="r">${s.on_duty}</td><td class="r">${s.leave}</td><td class="r ${s.absent ? 'neg' : ''}">${s.absent}</td><td class="small">${(s.exceptions || []).map(esc).join('<br>') || '<span class="dim">—</span>'}</td><td><button class="btn sm" data-act="hrAttend" data-id="${esc(r.code)}">Days</button></td></tr>`; }), { tall: true, empty: 'No one to show.' })}</div>` +
      (hr ? `<div class="panel"><div class="hd"><h3>Registered phones</h3><small>${pend.length ? pill(pend.length + ' new phone' + (pend.length > 1 ? 's' : '') + ' waiting', 'warn') : 'one phone per person, bound on first use'}</small></div>${tbl([['Person'], ['Phone'], ['Bound'], ['New phone waiting'], ['']], d.devices.map(x => `<tr><td class="mono">${esc(x.employee)}</td><td class="mono small">${esc(x.device_id)}</td><td class="small">${fmtTS(x.bound_at)}</td><td class="small">${x.pending ? esc(x.pending) + ' · ' + fmtTS(x.pending_at) : '—'}</td><td>${x.pending ? `<button class="btn sm pri" data-act="hrDevApprove" data-id="${esc(x.employee)}">Approve</button>` : ''}</td></tr>`), { empty: 'No phones yet.' })}</div>
      <div class="panel"><div class="hd"><h3>Office kiosks</h3><button class="btn sm" data-act="hrKioskNew">Register a kiosk</button></div><div class="bd small" style="padding-bottom:0">A tablet at the office entrance: open <span class="mono">${esc(location.origin)}/kiosk/${esc(S.me?.tenant || '')}</span> on it and enter the pairing code, from the office network. People check in with their employee code and kiosk PIN; the kiosk takes a photo for review and shows only the name and time.</div>${tbl([['Kiosk'], ['Office'], ['Status'], ['Paired'], ['Last used'], ['']], d.kiosks.map(k => `<tr><td>${esc(k.name)} <span class="mono dim">${esc(k.id)}</span></td><td>${esc(k.branch || 'Head office')}</td><td>${hrPill(k.status)}</td><td class="small">${k.paired_at ? fmtTS(k.paired_at) + ' · ' + esc(k.paired_ip) : (k.pair_until ? 'pairing code valid till ' + fmtTS(k.pair_until) : '—')}</td><td class="small">${k.last_used ? fmtTS(k.last_used) : '—'}</td><td>${k.status !== 'Revoked' ? `<button class="btn sm" data-act="hrKioskRevoke" data-id="${esc(k.id)}">Revoke</button>` : ''}</td></tr>`), { empty: 'No kiosks.' })}</div>` : '');
  }
};

/* ============================== Leave ============================== */
V.leave = {
  load: () => { const m = UI.hrLvMonth || hrThisMonth(); return Promise.all([get('/v1/hr/team?month=' + m), get('/v1/hr/on-leave?date=' + hrToday()).catch(() => [])]).then(([team, today]) => ({ month: m, team, today })); },
  html: d => {
    const t = d.team, me = S.me?.actor;
    const people = Object.fromEntries((t.people || []).map(p => [p.code, p.name]));
    const days = monthDaysJS(d.month);
    const grid = (t.people || []).length ? `<div class="tw"><table class="tbl small"><thead><tr><th>Person</th>${days.map(x => `<th class="r" title="${x}">${+x.slice(8)}</th>`).join('')}</tr></thead><tbody>${t.people.map(p => `<tr><td>${esc(p.name)}</td>${days.map(x => { const l = (t.leave || []).find(r => r.employee === p.code && r.from <= x && x <= r.to); const h = (t.holidays || []).find(hh => hh.date === x); return `<td class="r" title="${l ? esc(l.type + ' ' + l.status) : h ? esc(h.name) : ''}">${l ? `<b class="${l.status === 'Approved' ? 'pos' : 'dim'}">${esc(l.type)}</b>` : h ? '<span class="dim">H</span>' : ''}</td>`; }).join('')}</tr>`).join('')}</tbody></table></div>` : '<div class="bd dim">No team.</div>';
    return ph('Leave', 'Leave requests go to the person’s manager (HR when there is none); balances accrue by the firm’s leave types. Who is on leave today, with their backup, is used by other screens to hand work over.',
      `<input class="in mono" type="month" id="hr-lv-month" value="${esc(d.month)}" aria-label="Month" style="height:30px">${has('HR manager') ? '<button class="btn" data-act="hrCloseYear">Close the leave year</button>' : ''}`) +
      `<div class="panel"><div class="hd"><h3>Waiting for you</h3></div>${tbl([['Person'], ['Leave'], ['From'], ['To'], ['Days', 'r'], ['Reason'], ['Backup'], ['']], (t.pending || []).map(r => `<tr><td>${esc(r.name)}</td><td>${esc(r.type)}${r.half ? ' (half day)' : ''}</td><td>${fmtDate(r.from)}</td><td>${fmtDate(r.to)}</td><td class="r">${hrDays(r.days)}</td><td class="small">${esc(r.reason || '')}</td><td class="small">${esc(people[r.backup] || r.backup || '')}</td><td>${r.employee !== me ? `<button class="btn sm pri" data-act="hrLeaveDecide" data-id="${esc(r.id)}|1">Approve</button><button class="btn sm" data-act="hrLeaveDecide" data-id="${esc(r.id)}|0">Refuse</button>` : ''}</td></tr>`).concat((t.wfh_pending || []).map(w => `<tr><td>${esc(w.name)}</td><td>Work from home</td><td>${fmtDate(w.date)}</td><td></td><td></td><td class="small">${esc(w.reason)}</td><td></td><td><button class="btn sm pri" data-act="hrWFHDecide" data-id="${esc(w.id)}|1">Approve</button><button class="btn sm" data-act="hrWFHDecide" data-id="${esc(w.id)}|0">Refuse</button></td></tr>`)), { empty: 'Nothing waiting.' })}</div>` +
      `<div class="panel"><div class="hd"><h3>On leave today</h3><small>${fmtDate(hrToday())}</small></div>${tbl([['Person'], ['Leave'], ['Until'], ['Backup']], d.today.map(r => `<tr><td>${esc(r.name)}</td><td>${esc(r.type)}${r.half ? ' (half day)' : ''}</td><td>${fmtDate(r.until)}</td><td>${esc(r.backup_name || '—')}</td></tr>`), { empty: 'No one.' })}</div>` +
      `<div class="panel"><div class="hd"><h3>Team calendar</h3><small>${esc(d.month)} · H holiday</small></div>${grid}</div>`;
  }
};
function monthDaysJS(m) { const [y, mo] = m.split('-').map(Number), n = new Date(y, mo, 0).getDate(); return Array.from({ length: n }, (_, i) => `${m}-${String(i + 1).padStart(2, '0')}`); }

/* ============================== HR policies ============================== */
V.hrpolicy = {
  load: () => get('/v1/hr/policy'),
  html: p => {
    const can = has('HR manager', 'Compliance officer'), ot = p.own_trading || {}, dis = can ? '' : 'disabled';
    const F = (id, l, v, o = {}) => `<div class="fld"><label for="hp-${id}">${l}</label><input class="in ${o.mono ? 'mono' : ''}" id="hp-${id}" value="${esc(v ?? '')}" ${dis}></div>`;
    const sec = (t, sub, body) => `<div class="panel"><div class="hd"><h3>${t}</h3>${sub ? `<small>${sub}</small>` : ''}</div><div class="bd">${body}</div></div>`;
    return ph('HR policies', 'The firm’s rules for HRMS. Each certificate rule says what it is based on. Changes apply from now; records already made are kept.', can ? '<button class="btn pri" data-act="hrPolicySave">Save policies</button>' : '') +
      sec('Certificates each duty needs', 'An expired certificate on a rule marked “Flags terminal” flags the person’s trading terminal.', hrRows('cert', [['duty', 'Duty', 'text', 180], ['cert', 'Certificate', 'text', 240], ['basis', 'Based on', 'text', 320], ['blocks_terminal', 'Flags terminal', 'bool']], p.cert_rules, can) +
        `<div class="form">${F('rem', 'Reminders before expiry (days, comma separated)', (p.reminder_days || []).join(', '), { mono: 1 })}</div>`) +
      sec('Own trading code', esc(ot.basis || ''), `<div class="form">
        <div class="fld"><label for="hp-ot-app">Pre-clearance applies to</label><select class="in" id="hp-ot-app" ${dis}><option value="designated" ${ot.applies !== 'all' ? 'selected' : ''}>Designated persons</option><option value="all" ${ot.applies === 'all' ? 'selected' : ''}>All staff</option></select></div>
        ${F('ot-above', 'Pre-clear trades above (₹, 0: every trade)', ot.pre_clear_above, { mono: 1 })}${F('ot-valid', 'A pre-clearance is valid for (days)', ot.pre_clear_valid_days, { mono: 1 })}${F('ot-contra', 'No opposite trade within (days)', ot.contra_days, { mono: 1 })}${F('ot-due', 'Annual holdings declaration due by the end of month (1–12)', ot.annual_due_month, { mono: 1 })}
        ${F('ot-restr', 'Restricted list (symbols or ISINs, comma separated)', (ot.restricted || []).join(', '), { mono: 1 })}${F('ot-basis', 'Based on', ot.basis)}
        <label class="check"><input type="checkbox" id="hp-ot-rel" ${ot.relatives_covered ? 'checked' : ''} ${dis}> Relatives’ accounts are declared and covered</label><label class="check"><input type="checkbox" id="hp-ot-out" ${ot.outside_allowed ? 'checked' : ''} ${dis}> Accounts with other brokers allowed when declared</label></div>`) +
      sec('Leave types', 'Days are numbers like 1.5. Annual days are granted at the start of the leave year (prorated for joiners); monthly days accrue at the end of each month.', hrRows('lt', [['code', 'Code', 'text', 60], ['name', 'Name', 'text', 140], ['paid', 'Paid', 'bool'], ['annual', 'Annual', 'text', 60], ['monthly', 'Monthly', 'text', 60], ['max_balance', 'Max balance', 'text', 60], ['carry_max', 'Carry forward', 'text', 60], ['encash_max', 'Encash at year end', 'text', 60], ['negative', 'Without balance (LWP)', 'bool'], ['gender', 'Only for (F/M)', 'text', 40]], p.leave_types, can) +
        `<div class="form"><div class="fld"><label for="hp-ly">Leave year</label><select class="in" id="hp-ly" ${dis}><option value="calendar" ${p.leave_year !== 'financial' ? 'selected' : ''}>Calendar (January to December)</option><option value="financial" ${p.leave_year === 'financial' ? 'selected' : ''}>Financial (April to March)</option></select></div></div>`) +
      sec('Shifts', '', hrRows('sh', [['code', 'Code', 'text', 60], ['name', 'Name', 'text', 120], ['start', 'Start', 'text', 60], ['end', 'End', 'text', 60], ['grace_mins', 'Grace (min)', 'num', 50], ['half_hours', 'Half day below (hours)', 'text', 50], ['weekly_off', 'Weekly off (Sun, Sat)', 'list', 100]], p.shifts, can)) +
      sec('Office holidays', 'Besides these, exchange trading holidays from the firm calendar can count as office holidays.', hrRows('hol', [['date', 'Date (YYYY-MM-DD)', 'text', 100], ['name', 'Holiday', 'text', 180], ['branches', 'Only these offices (blank: all)', 'list', 160]], p.holidays, can) +
        `<label class="check"><input type="checkbox" id="hp-exh" ${p.exchange_holidays ? 'checked' : ''} ${dis}> Exchange trading holidays are office holidays</label>`) +
      sec('Offices for check-in', 'An office check-in from a phone counts on one of these networks or within the radius (default 100 m) of the office.', hrRows('site', [['branch', 'Office (blank: head office)', 'text', 100], ['lat', 'Latitude', 'float', 90], ['lng', 'Longitude', 'float', 90], ['radius_m', 'Radius (m)', 'num', 60], ['networks', 'Networks (CIDR, comma separated)', 'list', 220]], p.sites, can) +
        `<label class="check"><input type="checkbox" id="hp-nets" ${p.use_signin_networks ? 'checked' : ''} ${dis}> The firm’s networks from Security settings also count as office networks</label>`) +
      sec('Hybrid work policies', 'Modes: office, home, field, onduty. Home check-ins are allowed only on the fixed home days, within “any N” a week, or with a manager-approved request. Dealers on exchange terminals cannot check in from home unless their home is recorded as an exchange-approved dealing location.', hrRows('wp', [['id', 'ID', 'text', 70], ['name', 'Name', 'text', 140], ['modes', 'Modes allowed', 'list', 160], ['office_per_week', 'Office days a week', 'num', 50], ['office_per_month', 'Office days a month', 'num', 50], ['fixed_home_days', 'Fixed home days (Mon, Fri)', 'list', 110], ['any_home_per_week', 'Or any N home days a week', 'num', 50], ['departments', 'For departments', 'list', 140]], p.work_policies, can) +
        `<label class="check"><input type="checkbox" id="hp-dhf" ${p.dealers_home_free ? 'checked' : ''} ${dis}> Lift the dealer rule: dealers may work from home without an exchange-approved home location (only where they never deal from home, or the exchange has given a general permission)</label>`) +
      sec('Other', '', `<div class="form">${F('selfie', 'Keep check-in photos for (days)', p.selfie_days, { mono: 1 })}${F('deact', 'A leaver’s login is switched off at (HH:MM on the last day)', p.deactivate_at, { mono: 1 })}
        <label class="check"><input type="checkbox" id="hp-lop" ${p.absent_is_lop ? 'checked' : ''} ${dis}> A working day with no attendance and no leave is loss of pay (off: only days marked absent)</label></div>
        ${p.updated_by ? `<p class="dim small">Last changed by ${esc(p.updated_by)} · ${fmtTS(p.updated_at)}</p>` : ''}`);
  }
};

/* ============================== Payroll ============================== */
V.payroll = {
  load: () => {
    const m = UI.payMonth || hrThisMonth();
    return Promise.all([get('/v1/payroll'), get('/v1/payroll/runs/' + m).catch(() => null), get('/v1/payroll/runs/' + m + '/cbx').catch(() => null), get('/v1/payroll/runs/' + m + '/pt').catch(() => []), get('/v1/payroll/statutory').catch(() => [])])
      .then(([ov, run, cbx, pt, stat]) => ({ month: m, ov, run, cbx, pt, stat }));
  },
  html: d => {
    const tab = UI.payTab || 'run';
    const body = { run: payRunHTML, people: payPeopleHTML, money: payMoneyHTML, tax: payTaxHTML, rules: payRulesHTML }[tab](d);
    return ph('Payroll', 'The month’s salary run: made by Payroll, approved by a second person, posted to the firm’s books. Salary is the firm’s own payment, never a client payout: by the bank file, or as an HDFC CBX batch under the firm’s own CBX client code, released by the firm’s own CBX verifier and authorisers.',
      `<input class="in mono" type="month" id="pay-month" value="${esc(d.month)}" aria-label="Month" style="height:30px">`) +
      segBtns('payTab', [['run', 'Monthly run'], ['people', 'Salary structures'], ['money', 'Advances & claims'], ['tax', 'Tax & returns'], ['rules', 'Statutory tables & settings']]) + body;
  }
};

function payRunHTML(d) {
  const r = d.run, me = S.me?.actor, m = d.month;
  if (!r || r.status === 'Not made') return `<div class="panel"><div class="bd"><p>No run for ${esc(m)} yet.</p><button class="btn pri" data-act="payStep" data-id="make">Make the ${esc(m)} run</button></div></div>`;
  const t = r.totals || {}, st = r.status;
  const step = (s, l, k = '') => `<button class="btn ${k}" data-act="payStep" data-id="${s}">${l}</button>`;
  const acts = [st === 'Draft' ? step('make', 'Make again') + step('submit', 'Submit for approval', 'pri') : '', st === 'Submitted' && r.made_by !== me ? step('approve', 'Approve and post', 'pri') + step('return', 'Return') : '',
    st === 'Approved' ? `${step('email', 'Email payslips')}${d.ov.cbx && !d.cbx ? step('cbx', 'Send to HDFC (CBX)', 'pri') : ''}${d.cbx && d.cbx.state === 'Accepted' ? step('cbx-check', 'Ask HDFC now') : ''}${!d.cbx ? step('paid', 'Mark paid (bank file)') : ''}` : ''].join('');
  const files = st === 'Approved' || st === 'Paid' ? ['register:Salary register', 'bank:Bank payment file', 'ecr:PF ECR', 'esi:ESI contributions'].map(x => { const [k, l] = x.split(':'); return `<button class="btn sm" data-act="dl" data-id="/v1/payroll/runs/${esc(m)}/files/${k}" data-name="${k}-${esc(m)}">${l}</button>`; }).join('') : '';
  const cbx = d.cbx ? `<div class="panel"><div class="hd"><h3>HDFC CBX salary batch</h3><small>${hrPill(d.cbx.state)} file ${esc(d.cbx.file_ref)}${d.cbx.batch_num ? ' · batch ' + esc(d.cbx.batch_num) : ''} · ${esc(d.cbx.message || '')}</small></div>${tbl([['Person'], ['Ref'], ['Type'], ['Amount', 'r'], ['Status'], ['UTR']], d.cbx.lines.map(l => `<tr><td class="mono">${esc(l.employee)}</td><td class="mono">${esc(l.ref)}</td><td>${esc(l.type)}</td><td class="r">${inr(l.amount, 2)}</td><td>${hrPill(l.status)} <span class="dim small">${esc(l.note || '')}</span></td><td class="mono">${esc(l.utr || '')}</td></tr>`))}</div>` : '';
  return `<div class="kpis">${kpi('Status', st, r.checked_by ? 'approved by ' + esc(r.checked_by) : r.made_by ? 'made by ' + esc(r.made_by) : '')}${kpi('People', r.lines.length)}${kpi('Gross', inr(t.gross))}${kpi('Net pay', inr(t.net))}${kpi('PF (both sides)', inr(num(t.emp_pf) + num(t.er_epf) + num(t.er_eps) + num(t.edli) + num(t.pf_admin)))}${kpi('ESI', inr(num(t.emp_esi) + num(t.er_esi)))}${kpi('PT', inr(t.pt))}${kpi('TDS', inr(t.tds))}</div>
    ${(r.problems || []).length ? `<div class="note warn">${r.problems.map(esc).join('<br>')}</div>` : ''}${r.returned ? `<div class="note warn">Returned: ${esc(r.returned)}</div>` : ''}
    <div class="tools">${acts}${files}</div>${cbx}
    <div class="panel">${tbl([['Person'], ['Paid days', 'r'], ['LOP', 'r'], ['Gross', 'r'], ['PF', 'r'], ['ESI', 'r'], ['PT', 'r'], ['TDS', 'r'], ['Recovery', 'r'], ['Net', 'r'], ['']], r.lines.map(l => `<tr><td><b>${esc(l.name)}</b> <span class="mono dim">${esc(l.employee)}</span>${l.hold ? ' ' + pill('On hold', 'warn') : ''}${(l.problems || []).length ? ' ' + pill(l.problems[0], 'bad') : ''}</td><td class="r">${hrDays(l.paid_days)}/${l.month_days}</td><td class="r">${l.lop ? hrDays(l.lop) : ''}</td><td class="r">${inr(l.gross)}</td><td class="r">${inr(l.emp_pf)}</td><td class="r">${inr(l.emp_esi)}</td><td class="r">${inr(l.pt)}</td><td class="r">${inr(l.tds)}</td><td class="r">${num(l.recovery) ? inr(l.recovery) : ''}</td><td class="r"><b>${inr(l.net)}</b></td><td><button class="btn sm" data-act="payLine" data-id="${esc(l.employee)}">Working</button>${st === 'Draft' ? `<button class="btn sm" data-act="payInput" data-id="${esc(l.employee)}">Inputs</button>` : ''}<button class="btn sm" data-act="dl" data-id="/v1/payroll/runs/${esc(m)}/payslips/${esc(l.employee)}" data-name="payslip-${esc(m)}-${esc(l.employee)}.pdf">Payslip</button></td></tr>`), { tall: true })}</div>
    ${(d.pt || []).length ? `<div class="panel"><div class="hd"><h3>Professional tax by state</h3></div>${tbl([['State'], ['People', 'r'], ['Amount', 'r'], ['By slab']], d.pt.map(p => `<tr><td>${esc(p.state)}</td><td class="r">${p.people}</td><td class="r">${inr(p.amount)}</td><td class="small">${Object.entries(p.slabs || {}).map(([k, v]) => `₹${esc(k)} × ${v}`).join(' · ')}</td></tr>`))}</div>` : ''}`;
}

function payPeopleHTML(d) {
  const ss = d.ov.structures || {};
  return `<div class="panel"><div class="hd"><h3>Salary structures</h3><small>Effective-dated: a new structure applies from its date; months before keep the old one</small></div>${tbl([['Person'], ['From'], ['Components'], ['Monthly gross', 'r'], ['Regime'], ['']], (d.ov.staff || []).filter(e => e.status !== 'Left').map(e => { const s = ss[e.code]; const g = s ? s.components.reduce((a, c) => a + num(c.amount), 0) : 0; return `<tr><td><b>${esc(e.name)}</b> <span class="mono dim">${esc(e.code)}</span></td><td>${s ? fmtDate(s.from) : '—'}</td><td class="small">${s ? s.components.map(c => `${esc(c.name || c.code)} ${inr(c.amount)}${c.pf_wage ? ' (PF)' : ''}`).join(' · ') : '<span class="neg">No structure: left out of the run</span>'}</td><td class="r">${s ? inr(g) : ''}</td><td>${esc(e.tax_regime || 'new')}</td><td><button class="btn sm" data-act="payStruct" data-id="${esc(e.code)}">Set structure</button></td></tr>`; }), { tall: true })}</div>`;
}

function payMoneyHTML(d) {
  const me = S.me?.actor;
  return `<div class="panel"><div class="hd"><h3>Salary advances and staff loans</h3><button class="btn sm" data-act="payLoanNew">Give an advance or loan</button></div>${tbl([['Ref'], ['Person'], ['Kind'], ['Amount', 'r'], ['Instalment', 'r'], ['From'], ['Recovered', 'r'], ['Status'], ['']], (d.ov.loans || []).map(l => `<tr><td class="mono">${esc(l.id)}</td><td class="mono">${esc(l.employee)}</td><td>${esc(l.kind)}</td><td class="r">${inr(l.amount)}</td><td class="r">${inr(l.instalment)}</td><td>${esc(l.start_month)}</td><td class="r">${inr(l.recovered)}</td><td>${hrPill(l.status)} <span class="dim small">${esc(l.made_by)}${l.approved_by ? ' · ' + esc(l.approved_by) : ''}</span></td><td>${l.status === 'Pending' && l.made_by !== me ? `<button class="btn sm pri" data-act="payLoanApprove" data-id="${esc(l.id)}">Approve and pay</button>` : ''}</td></tr>`), { empty: 'None.' })}</div>
    <div class="panel"><div class="hd"><h3>Reimbursement claims</h3><small>Approved claims are paid with the next run</small></div>${tbl([['Ref'], ['Person'], ['Date'], ['Claim'], ['Amount', 'r'], ['Bill'], ['Status'], ['']], (d.ov.claims || []).map(c => `<tr><td class="mono">${esc(c.id)}</td><td>${esc(c.name || c.employee)}</td><td>${fmtDate(c.date)}</td><td>${esc(c.kind)} <span class="dim small">${esc(c.note || '')}</span></td><td class="r">${inr(c.amount)}</td><td>${c.bill ? `<a href="#" data-act="hrDoc" data-id="${esc(c.employee)}|${esc(c.bill.id)}">bill</a>` : ''}</td><td>${hrPill(c.status)}${c.paid_in ? ' <span class="dim small">' + esc(c.paid_in) + '</span>' : ''}</td><td>${c.status === 'Pending' ? `<button class="btn sm pri" data-act="payClaimDecide" data-id="${esc(c.id)}|1">Approve</button><button class="btn sm" data-act="payClaimDecide" data-id="${esc(c.id)}|0">Refuse</button>` : ''}</td></tr>`), { empty: 'None.' })}</div>`;
}

function payTaxHTML(d) {
  const fy = UI.payFY || (() => { const n = new Date(), y = n.getMonth() >= 3 ? n.getFullYear() : n.getFullYear() - 1; return `${y}-${String(y + 1).slice(2)}`; })();
  return `<div class="panel"><div class="hd"><h3>Returns</h3><small>TDS on salary is under section 392 of the Income-tax Act 2025 (section 192 before 1 April 2026); Form 138 replaced Form 24Q and Form 130 replaced Form 16</small></div><div class="bd">
    <div class="form"><div class="fld"><label for="pay-fy">Financial year</label><input class="in mono" id="pay-fy" value="${esc(fy)}"></div></div>
    <div class="tools">${[1, 2, 3, 4].map(q => `<button class="btn sm" data-act="payF138" data-id="${q}">Form 138 Q${q}</button>`).join('')}<button class="btn sm" data-act="payF138" data-id="4|2">Form 138 Q4 Annexure II</button></div>
    <p class="dim small">Form 130 Part B for each person is on the person’s row below; payslips and Form 130 are also in each person’s My HR.</p></div></div>
    <div class="panel"><div class="hd"><h3>Tax declarations</h3></div>${tbl([['Person'], ['Year'], ['Regime'], ['Rent a month', 'r'], ['80C', 'r'], ['80D', 'r'], ['Home loan', 'r'], ['Proofs'], ['Status'], ['']], (d.ov.declarations || []).map(x => `<tr><td class="mono">${esc(x.employee)}</td><td>${esc(x.fy)}</td><td>${esc(x.regime)}</td><td class="r">${inr(x.declared.rent_monthly)}</td><td class="r">${inr(x.declared.c80)}</td><td class="r">${inr(x.declared.d80)}</td><td class="r">${inr(x.declared.home_loan)}</td><td class="small">${(x.proofs || []).map(p => `<a href="#" data-act="hrDoc" data-id="${esc(x.employee)}|${esc(p.id)}">${esc(p.name)}</a>`).join(', ')}</td><td>${hrPill(x.status)}</td><td><button class="btn sm" data-act="payVerify" data-id="${esc(x.employee)}|${esc(x.fy)}">Verify proofs</button><button class="btn sm" data-act="dl" data-id="/v1/payroll/form130/${esc(x.employee)}?fy=${esc(x.fy)}" data-name="form130-${esc(x.fy)}-${esc(x.employee)}.pdf">Form 130</button></td></tr>`), { empty: 'No declarations yet.' })}</div>`;
}

function payRulesHTML(d) {
  const s = d.ov.settings || {}, F = (id, l, v) => `<div class="fld"><label for="ps-${id}">${l}</label><input class="in mono" id="ps-${id}" value="${esc(v ?? '')}"></div>`;
  return `<div class="panel"><div class="hd"><h3>Payroll settings</h3><button class="btn sm pri" data-act="paySettingsSave">Save</button></div><div class="bd"><div class="form">
      ${F('pf', 'PF establishment code', s.pf_code)}${F('esi', 'ESI employer code', s.esi_code)}${F('tan', 'TAN', s.tan)}${F('state', 'Default state (professional tax)', s.default_state)}${F('day', 'Pay day (of the next month)', s.pay_day)}${F('bank', 'Ledger account salary is paid from', s.bank_account)}${F('debit', 'Debit account number on the bank file', s.debit_account)}${F('div', 'Leave encashment: days in a month (e.g. 26 or 30)', s.encash_divisor)}${F('cut', 'Proofs cut-off month (1–12)', s.proof_cutoff)}</div></div></div>
    <div class="panel"><div class="hd"><h3>Statutory tables</h3><button class="btn sm" data-act="payStatNew">New table from a date</button></div><div class="bd small"><p style="margin-top:0">PF, ESI, professional tax by state and income tax slabs, effective from a date with their source. A change is a new table from the date it applies; months already approved keep the figures they were paid with.</p></div>
      ${tbl([['From'], ['Source'], ['PF'], ['ESI'], ['PT states'], ['Tax regimes']], (d.stat || []).map(x => `<tr><td class="n">${fmtDate(x.from)}</td><td class="small wrap">${esc(x.source)}</td><td class="small">${esc(x.pf_employee)}% / ${esc(x.pf_employer)}% · ceiling ₹${nf(x.pf_ceiling)} · EPS ${esc(x.eps)}% · EDLI ${esc(x.edli)}% · admin ${esc(x.pf_admin)}%</td><td class="small">${esc(x.esi_employee)}% / ${esc(x.esi_employer)}% · up to ₹${nf(x.esi_threshold)}</td><td class="small">${Object.keys(x.pt || {}).map(esc).join(', ')}</td><td class="small">${Object.keys(x.tax || {}).map(esc).join(', ')}</td></tr>`))}</div>`;
}

/* ============================== Settings → Modules ============================== */
V.modules = {
  load: () => get('/v1/modules'),
  html: m => ph('Modules', 'Parts of Ledgerline priced separately from the edition. With a module off nothing of it shows and its records are kept, so switching it on again brings everything back. Your Ledgerline provider can also switch them for you.', '<button class="btn pri" data-act="modSave">Save</button>') +
    `<div class="panel"><div class="bd">
      <label class="check" style="margin:8px 0"><input type="checkbox" id="mod-hrms" ${m.hrms ? 'checked' : ''}> <span><b>HRMS</b> · the firm’s own people: employee master, joiners and leavers tied to access, certificates and own trading, attendance (phone, kiosk, hybrid work), leave and self-service. Adds the People group.</span></label>
      <label class="check" style="margin:8px 0"><input type="checkbox" id="mod-payroll" ${m.payroll ? 'checked' : ''}> <span><b>Payroll</b> (needs HRMS) · salary structures, the monthly run with a second approver, PF, ESI, professional tax and TDS, payslips, PF ECR and ESI files, Form 138 and Form 130, salary by bank file or HDFC CBX, postings to the firm’s books.</span></label>
      ${m.by ? `<p class="dim small">Last changed by ${esc(m.by)} · ${fmtTS(m.at)}</p>` : ''}</div></div>`
};

/* ============================== actions ============================== */
const hrPrompt = (title, sub, fields, okLabel, go) => confirmModal(title, sub, `<div class="form">${fields}</div>`, okLabel, go);
const hrF = (id, l, o = {}) => `<div class="fld ${o.full ? 'full' : ''}"><label for="${id}">${l}</label>${o.opts ? `<select class="in" id="${id}">${o.opts.map(x => { const [k, t] = Array.isArray(x) ? x : [x, x]; return `<option value="${esc(k)}" ${k === o.v ? 'selected' : ''}>${esc(t)}</option>`; }).join('')}</select>` : o.area ? `<textarea class="in" id="${id}" rows="${o.rows || 3}">${esc(o.v || '')}</textarea>` : `<input class="in ${o.mono ? 'mono' : ''}" id="${id}" ${o.type ? `type="${o.type}"` : ''} value="${esc(o.v ?? '')}" ${o.ph ? `placeholder="${esc(o.ph)}"` : ''}>`}</div>`;
const hrV = id => ($(id)?.value || '').trim();
const hrAmt = v => (v || '').replace(/[,₹\s]/g, '') || '0';

Object.assign(A, {
  hrRowAdd: id => { const cols = HR_ROWS[id], tb = document.querySelector(`#rows-${id} tbody`); if (!tb) return; tb.insertAdjacentHTML('beforeend', hrRows(id, cols, [{}], true).match(/<tr data-r>.*?<\/tr>/s)[0]); },
  hrRowDel: (_, el) => el.closest('tr')?.remove(),
  hrOpen: id => hrOpenPerson(id),
  hrEdit: async id => {
    let e = { employment_type: 'Permanent', eps_member: true };
    if (id) { try { e = await get('/v1/hr/staff/' + hrEnc(id)); } catch (err) { toast(err.message, true); return; } }
    wideModal(mhd(id ? 'Edit ' + esc(e.name) : 'Add employee', id ? 'A change of designation, department, office, duties or manager raises an access review.' : 'Saving raises the joining checklist.') + `<div class="bd">${hrEmpForm(e)}<div id="he-err"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="he-ok">Save</button></div>`);
    $('he-ok').onclick = async () => {
      const b = { ...e, name: hrV('he-name'), code: hrV('he-code').toUpperCase(), department: hrV('he-dept'), designation: hrV('he-desig'), branch: hrV('he-branch'), manager: hrV('he-mgr'), employment_type: hrV('he-type'), gender: hrV('he-gender'), joined_on: hrV('he-join'), dob: hrV('he-dob'), email: hrV('he-email'), mobile: hrV('he-mobile'),
        pan: hrV('he-pan').toUpperCase(), aadhaar4: hrV('he-aad'), uan: hrV('he-uan'), esic: hrV('he-esic'), bank: { ...(e.bank || {}), holder: hrV('he-holder'), account: hrV('he-acct'), ifsc: hrV('he-ifsc').toUpperCase() }, state: hrV('he-state'), tax_regime: hrV('he-regime'), work_policy: hrV('he-wp'), shift: hrV('he-shift'), device_id: hrV('he-dev'),
        kmp: hrV('he-kmp'), dealer: hrV('he-dealer'), emergency: { ...(e.emergency || {}), name: hrV('he-emname'), phone: hrV('he-emphone') }, duties: [...document.querySelectorAll('[data-duty]')].filter(x => x.checked).map(x => x.dataset.duty),
        designated: $('he-desig-p').checked, eps_member: $('he-eps').checked, no_pf: $('he-nopf').checked, disabled: $('he-dis').checked, notes: hrV('he-notes') };
      if (b.aadhaar4.startsWith('XXXX')) b.aadhaar4 = e.aadhaar4;
      try { await (id ? api('PUT', '/v1/hr/staff/' + hrEnc(id), b) : post('/v1/hr/staff', b)); closeModal(); toast('Saved'); render({ keep: true }); } catch (err) { $('he-err').innerHTML = errHTML(err); }
    };
  },
  hrReveal: id => { const [code, field] = id.split('|'); hrPrompt('Reveal', 'The reason is recorded in the audit trail with your name.', hrF('rv-r', 'Why you need it', { ph: 'e.g. filing the PF return' }), 'Reveal', async () => { const r = await post(`/v1/hr/staff/${hrEnc(code)}/reveal`, { field, reason: hrV('rv-r') }); $('cm-err').innerHTML = `<div class="secret mono">${esc(r.value)}</div>`; $('cm-ok').hidden = true; }); },
  hrDoc: (id, el, e) => { const [code, doc] = id.split('|'); openBlob(`/v1/hr/staff/${hrEnc(code)}/documents/${hrEnc(doc)}`); },
  hrDocAdd: code => hrPrompt('Add a document', 'PDF or image, up to 10 MB. Documents with an expiry get reminders before it.', hrF('hd-kind', 'Kind', { opts: C.people?.staff?.doc_kinds || ['Other'] }) + hrF('hd-exp', 'Expires on (optional)', { type: 'date' }) + `<div class="fld full"><label for="hd-file">File</label><input type="file" id="hd-file" accept="application/pdf,image/*"></div>`, 'Upload',
    () => act(() => hrUpload(`/v1/hr/staff/${hrEnc(code)}/documents?kind=${hrEnc(hrV('hd-kind'))}&expiry=${hrV('hd-exp')}`, 'hd-file'), 'Added').then(() => hrOpenPerson(code))),
  hrCertAdd: code => hrPrompt('Add a certificate', 'A certificate entered by HR or compliance is verified.', hrF('hc-c', 'Certificate', { ph: 'NISM Series VIII (Equity Derivatives)' }) + hrF('hc-n', 'Certificate number', { mono: 1 }) + hrF('hc-i', 'Issued on', { type: 'date' }) + hrF('hc-v', 'Valid till', { type: 'date' }), 'Save',
    () => act(() => post(`/v1/hr/staff/${hrEnc(code)}/certs`, { cert: hrV('hc-c'), number: hrV('hc-n'), issued_on: hrV('hc-i'), valid_till: hrV('hc-v') }), 'Saved').then(() => hrOpenPerson(code))),
  hrMyCert: () => hrPrompt('Add your certificate', 'HR or compliance verify it against the certificate.', hrF('hc-c', 'Certificate', { ph: 'NISM Series VIII (Equity Derivatives)' }) + hrF('hc-n', 'Certificate number', { mono: 1 }) + hrF('hc-i', 'Issued on', { type: 'date' }) + hrF('hc-v', 'Valid till', { type: 'date' }), 'Save',
    () => act(() => post('/v1/hr/me/certs', { cert: hrV('hc-c'), number: hrV('hc-n'), issued_on: hrV('hc-i'), valid_till: hrV('hc-v') }), 'Saved')),
  hrLink: code => { const us = C.people?.staff?.users || []; hrPrompt('Link the Ledgerline login', 'The person sees their own records through this login, and it is switched off on their last day.', hrF('hl-u', 'Login', { opts: [['', '— none —'], ...us.map(u => [u.id, `${u.name} (${u.id}) · ${u.role}${u.active ? '' : ' · inactive'}`])] }), 'Link', () => act(() => post(`/v1/hr/staff/${hrEnc(code)}/user`, { user: hrV('hl-u') }), 'Linked').then(() => hrOpenPerson(code))); },
  hrHomeDeal: code => hrPrompt('Home as an exchange-approved dealing location', 'Record the exchange’s approval of this dealer’s home as a dealing location. Without it the dealer cannot check in from home. Compliance officer.', `<label class="check"><input type="checkbox" id="hh-a" checked> Approved by the exchange</label>` + hrF('hh-r', 'Exchange approval reference') + hrF('hh-d', 'Date of approval', { type: 'date' }), 'Save',
    () => act(() => post(`/v1/hr/staff/${hrEnc(code)}/home-dealing`, { approved: $('hh-a').checked, ref: hrV('hh-r'), date: hrV('hh-d') }), 'Saved').then(() => hrOpenPerson(code))),
  hrLeaving: code => hrPrompt('Record leaving', 'Another HR manager approves it. On the last day the login is switched off by itself and the exit work goes to the people who do it.', hrF('lv-d', 'Last working day', { type: 'date' }) + hrF('lv-r', 'Reason', { opts: ['Resigned', 'Retired', 'Terminated', 'End of contract', 'Absconding', 'Death', 'Other'] }), 'Record',
    () => act(() => post(`/v1/hr/staff/${hrEnc(code)}/leaving`, { last_day: hrV('lv-d'), reason: hrV('lv-r') }), 'Recorded: waiting for a second approval')),
  hrLeaveApprove: code => confirmModal('Approve the leaving', 'The login is switched off at the policy’s time on the last day (at once if that has passed).', '', 'Approve', () => act(() => post(`/v1/hr/staff/${hrEnc(code)}/leaving/approve`), 'Approved')),
  hrLeaveWithdraw: code => hrPrompt('Withdraw the leaving', 'The person stays; the leaving checklist is closed.', hrF('lw-n', 'Note'), 'Withdraw', () => act(() => post(`/v1/hr/staff/${hrEnc(code)}/leaving/withdraw`, { note: hrV('lw-n') }), 'Withdrawn')),
  hrItem: id => { const [lid, key, na] = id.split('|'); if (na === '1') return hrPrompt('Not applicable', 'Say why this does not apply.', hrF('ci-n', 'Why'), 'Save', () => act(() => post(`/v1/hr/checklists/${hrEnc(lid)}/items/${hrEnc(key)}`, { na: true, note: hrV('ci-n') }), 'Saved')); return act(() => post(`/v1/hr/checklists/${hrEnc(lid)}/items/${hrEnc(key)}`, { na: false }), 'Done'); },
  hrDevApprove: code => confirmModal('Approve the new phone', 'The old phone stops working for check-in.', '', 'Approve', () => act(() => post(`/v1/hr/staff/${hrEnc(code)}/device/approve`), 'Approved')),
  hrPINUnlock: code => confirmModal('Unlock the kiosk PIN', 'After five wrong PINs the kiosk locks the PIN until the person sets a new one or HR unlocks it.', '', 'Unlock', () => act(() => post(`/v1/hr/staff/${hrEnc(code)}/pin/unlock`), 'Unlocked', false)),
  hrLeaveAdj: code => hrPrompt('Adjust leave', 'Adds or takes away days (negative to take away), with a note, in the leave ledger.', hrF('la-t', 'Leave type', { opts: (C.hrpolicy?.leave_types || [{ code: 'CL' }, { code: 'SL' }, { code: 'EL' }]).map(t => t.code) }) + hrF('la-d', 'Days', { mono: 1, ph: '1.5' }) + hrF('la-n', 'Note'), 'Save',
    () => act(() => post(`/v1/hr/staff/${hrEnc(code)}/leave-adjust`, { type: hrV('la-t'), days: parseFloat(hrV('la-d')) || 0, note: hrV('la-n') }), 'Adjusted', false)),
  hrAttend: async code => {
    const m = UI.hrAttMonth || hrThisMonth(); let v;
    try { v = await get(`/v1/hr/staff/${hrEnc(code)}/attendance?month=${m}`); } catch (e) { toast(e.message, true); return; }
    const s = v.summary || {};
    wideModal(mhd(`${esc(v.name)} · ${esc(v.month)}`, `Office ${s.office} · home ${s.home} · field ${s.field} · leave ${s.leave} · absent ${s.absent}${(s.exceptions || []).length ? ' · ' + s.exceptions.map(esc).join('; ') : ''}`) + `<div class="bd">${hrDaysTable(v.days || [], true, code)}</div>`);
  },
  hrMark: id => { const [code, day] = id.split('|'); hrPrompt('Mark the day', `${esc(code)} · ${fmtDate(day)}. A manager’s or HR’s mark overrides the punches; you cannot mark your own.`, hrF('mk-s', 'Status', { opts: ['Present', 'Half day', 'Absent', 'On duty'] }) + hrF('mk-i', 'In (HH:MM)', { mono: 1 }) + hrF('mk-o', 'Out (HH:MM)', { mono: 1 }) + hrF('mk-m', 'Mode', { opts: Object.entries(MODE_NAME) }) + hrF('mk-n', 'Note (why)', { full: 1 }), 'Save',
    () => act(() => post(`/v1/hr/staff/${hrEnc(code)}/attendance`, { date: day, status: hrV('mk-s'), in: hrV('mk-i'), out: hrV('mk-o'), mode: hrV('mk-m'), note: hrV('mk-n') }), 'Marked')); },
  hrPhoto: id => openBlob('/v1/hr/photos/' + hrEnc(id)),
  hrImport: () => hrPrompt('Upload attendance from a device', 'A CSV from any attendance device: a user ID column and a date-time column (one punch a row). The device user ID is on each person’s record. This is a fallback; the phone app and kiosk are the usual ways.', `<div class="fld full"><label for="im-f">CSV file</label><input type="file" id="im-f" accept=".csv,text/csv"></div>`, 'Upload',
    () => act(() => hrUpload('/v1/hr/attendance/import', 'im-f'), r => `${r.days} days from ${r.punches || 0} punches${(r.unknown || []).length ? ' · unknown device IDs: ' + r.unknown.join(', ') : ''}`)),
  hrRaise: () => act(() => get(`/v1/hr/attendance/report?month=${UI.hrAttMonth || hrThisMonth()}&raise=1`), r => `${r.filter(x => (x.summary.exceptions || []).length).length} exceptions sent to managers`),
  hrKioskNew: () => hrPrompt('Register a kiosk', 'You get a pairing code, valid for a short time. Enter it on the tablet at /kiosk/' + esc(S.me?.tenant || '') + ', from the office network.', hrF('kn-n', 'Name', { ph: 'Entrance tablet' }) + hrF('kn-b', 'Office', { opts: [['', 'Head office'], ...(C.people?.staff?.branches || [])] }), 'Register',
    async () => { const r = await post('/v1/hr/kiosks', { name: hrV('kn-n'), branch: hrV('kn-b') }); modal(mhd('Pairing code', 'Enter it on the tablet. It works once.') + `<div class="bd"><div class="secret mono" style="font-size:28px">${esc(r.pairing_code)}</div><p class="small">Open <span class="mono">${esc(location.origin)}/kiosk/${esc(S.me?.tenant || '')}</span> on the tablet.</p></div><div class="ft"><button class="btn pri" data-act="close">Done</button></div>`); render({ keep: true }); }),
  hrKioskRevoke: id => confirmModal('Revoke the kiosk', 'It stops working at once.', '', 'Revoke', () => act(() => post(`/v1/hr/kiosks/${hrEnc(id)}/revoke`), 'Revoked'), true),
  hrPunch: async kind => {
    const b = { kind, mode: $('me-mode').value, note: hrV('me-note') };
    if ($('me-loc').checked && navigator.geolocation) {
      try { const pos = await new Promise((ok, no) => navigator.geolocation.getCurrentPosition(ok, no, { enableHighAccuracy: true, timeout: 10000 })); b.lat = pos.coords.latitude; b.lng = pos.coords.longitude; b.has_location = true; } catch (e) { toast('Your location could not be read', true); return; }
    }
    act(() => post('/v1/hr/attendance/punch', b), r => `${r.kind === 'in' ? 'Checked in' : 'Checked out'} at ${(r.at || '').slice(11, 16)}${r.site ? ' · ' + r.site : ''}`);
  },
  hrWFHReq: () => hrPrompt('Ask to work from home', 'Your manager approves it. Dealers on exchange terminals need their home recorded as an exchange-approved dealing location.', hrF('wf-d', 'Day', { type: 'date' }) + hrF('wf-r', 'Reason'), 'Ask', () => act(() => post('/v1/hr/wfh', { date: hrV('wf-d'), reason: hrV('wf-r') }), 'Sent to your manager')),
  hrWFHDecide: id => { const [w, ok] = id.split('|'); return act(() => post('/v1/hr/wfh/' + hrEnc(w), { approve: ok === '1' }), ok === '1' ? 'Approved' : 'Refused'); },
  hrLeaveApply: () => { const v = C.me, types = (v?.leave?.types || []).filter(t => !t.gender || t.gender === v.profile.gender); const team = (C.people?.staff?.employees || []);
    hrPrompt('Apply for leave', 'Your manager approves it. Name who covers your work: other screens hand work to them while you are away.', hrF('la-type', 'Leave', { opts: types.map(t => [t.code, t.name]) }) + hrF('la-from', 'From', { type: 'date' }) + hrF('la-to', 'To', { type: 'date' }) + `<label class="check"><input type="checkbox" id="la-half"> Half day</label>` + hrF('la-bk', 'Backup (employee code)', { mono: 1, ph: team[0]?.code || 'E0002' }) + hrF('la-rs', 'Reason', { full: 1 }), 'Apply',
      () => act(() => post('/v1/hr/leave', { type: hrV('la-type'), from: hrV('la-from'), to: hrV('la-to') || hrV('la-from'), half: $('la-half').checked, backup: hrV('la-bk').toUpperCase(), reason: hrV('la-rs') }), r => `Applied: ${hrDays(r.days)} days, waiting for approval`)); },
  hrLeaveCancel: id => confirmModal('Cancel the leave', '', '', 'Cancel leave', () => act(() => post(`/v1/hr/leave/${hrEnc(id)}/cancel`), 'Cancelled')),
  hrLeaveDecide: id => { const [r, ok] = id.split('|'); return hrPrompt(ok === '1' ? 'Approve leave' : 'Refuse leave', '', hrF('ld-n', ok === '1' ? 'Note (optional)' : 'Why'), ok === '1' ? 'Approve' : 'Refuse', () => act(() => post('/v1/hr/leave/' + hrEnc(r), { approve: ok === '1', note: hrV('ld-n') }), ok === '1' ? 'Approved' : 'Refused')); },
  hrCloseYear: () => hrPrompt('Close the leave year', 'Carries forward, encashes and lapses each balance by its leave type. Done once a year.', hrF('cy-d', 'Last day of the leave year', { type: 'date' }), 'Close the year', () => act(() => post('/v1/hr/leave-year/close', { year_end: hrV('cy-d') }), r => `${r.length} balances closed`)),
  hrMyPIN: () => hrPrompt('Your kiosk PIN', '4 to 6 digits, not a simple run like 1234. Five wrong tries lock it.', hrF('pn-1', 'New PIN', { type: 'password', mono: 1 }) + hrF('pn-2', 'Again', { type: 'password', mono: 1 }), 'Set PIN',
    () => { if (hrV('pn-1') !== hrV('pn-2')) throw new Error('The two PINs differ'); return act(() => post('/v1/hr/me/pin', { pin: hrV('pn-1') }), 'PIN set'); }),
  hrDeclAcct: () => hrPrompt('Declare a trading or demat account', 'Yours or an immediate relative’s, with the firm or another broker.', hrF('da-h', 'Holder') + hrF('da-r', 'Relation', { opts: ['Self', 'Spouse', 'Parent', 'Child', 'Sibling', 'Other dependant'] }) + hrF('da-b', 'Broker or DP') + hrF('da-a', 'Account (UCC, client ID or BO ID)', { mono: 1 }) + hrF('da-o', 'Opened on', { type: 'date' }) + `<label class="check"><input type="checkbox" id="da-out"> With another broker (outside the firm)</label>`, 'Declare',
    () => act(() => post('/v1/hr/own-trading/accounts', { holder: hrV('da-h'), relation: hrV('da-r'), broker: hrV('da-b'), account: hrV('da-a'), opened_on: hrV('da-o'), outside: $('da-out').checked }), 'Declared')),
  hrDeclHold: () => hrPrompt('Declare holdings', 'One line a holding: security, quantity, account. Or tick nil.', hrF('dh-k', 'Kind', { opts: ['Annual', 'Joining', 'Leaving', 'Change'] }) + hrF('dh-p', 'Period', { ph: 'FY 2026-27' }) + hrF('dh-l', 'Holdings (one a line: security, quantity, account)', { area: 1, rows: 5, full: 1 }) + `<label class="check"><input type="checkbox" id="dh-nil"> Nil: no holdings</label>`, 'Declare',
    () => act(() => post('/v1/hr/own-trading/holdings', { kind: hrV('dh-k'), period: hrV('dh-p'), nil: $('dh-nil').checked, holdings: hrV('dh-l').split('\n').map(l => l.split(',').map(s => s.trim())).filter(x => x[0]).map(([security, qty, account]) => ({ security, qty: parseInt(qty, 10) || 0, account: account || '' })) }), 'Declared')),
  hrPreClear: () => { const accts = C.me?.own_trading?.accounts || []; hrPrompt('Ask for pre-clearance', 'The compliance officer decides. Securities on the restricted list are refused.', hrF('pc-a', 'Account', { opts: accts.map(a => [a.id, `${a.holder} · ${a.broker} ${a.account}`]) }) + hrF('pc-s', 'Security (symbol or ISIN)', { mono: 1 }) + hrF('pc-side', 'Buy or sell', { opts: ['Buy', 'Sell'] }) + hrF('pc-q', 'Quantity', { mono: 1 }) + hrF('pc-v', 'Approximate value (₹)', { mono: 1 }) + hrF('pc-r', 'Reason (optional)'), 'Ask',
    () => act(() => post('/v1/hr/own-trading/pre-clearance', { account: hrV('pc-a'), security: hrV('pc-s').toUpperCase(), side: hrV('pc-side'), qty: parseInt(hrV('pc-q'), 10) || 0, value: hrAmt(hrV('pc-v')), reason: hrV('pc-r') }), r => r.needed ? 'Sent to compliance' : 'Recorded: the code needs no pre-clearance for this trade')); },
  hrPCDecide: id => { const [r, ok] = id.split('|'); return hrPrompt(ok === '1' ? 'Approve pre-clearance' : 'Refuse pre-clearance', '', hrF('pd-n', 'Note'), ok === '1' ? 'Approve' : 'Refuse', () => act(() => post('/v1/hr/own-trading/pre-clearance/' + hrEnc(r), { approve: ok === '1', note: hrV('pd-n') }), 'Saved')); },
  hrTaxDecl: () => { const v = C.me, d = v.tax_declaration || { regime: v.profile.tax_regime || 'new', declared: {} }, x = d.declared || {};
    hrPrompt('Tax declaration · ' + esc(v.fy), 'Payroll deducts TDS on these figures until proofs are verified. The old regime allows HRA, 80C, 80D and home loan interest; the new regime does not.', hrF('td-reg', 'Regime', { opts: [['new', 'New regime'], ['old', 'Old regime']], v: d.regime }) + hrF('td-rent', 'Rent paid a month (₹)', { mono: 1, v: x.rent_monthly }) + `<label class="check"><input type="checkbox" id="td-metro" ${x.metro ? 'checked' : ''}> Rented home in Delhi, Mumbai, Kolkata or Chennai</label>` + hrF('td-ll', 'Landlord’s PAN (rent above ₹1 lakh a year)', { mono: 1, v: x.landlord_pan }) +
      hrF('td-80c', '80C investments (₹ a year)', { mono: 1, v: x.c80 }) + hrF('td-80d', '80D health insurance (₹)', { mono: 1, v: x.d80 }) + hrF('td-nps', 'NPS 80CCD(1B) (₹)', { mono: 1, v: x.nps_1b }) + hrF('td-hl', 'Home loan interest (₹)', { mono: 1, v: x.home_loan }) + hrF('td-oi', 'Other income (₹)', { mono: 1, v: x.other_income }) + hrF('td-pi', 'Salary from an earlier employer this year (₹)', { mono: 1, v: x.prev_income }) + hrF('td-pt', 'TDS by the earlier employer (₹)', { mono: 1, v: x.prev_tds }), 'Save',
      () => act(() => post('/v1/payroll/tax-declarations', { employee: v.profile.code, fy: v.fy, regime: hrV('td-reg'), declared: { rent_monthly: hrAmt(hrV('td-rent')), metro: $('td-metro').checked, landlord_pan: hrV('td-ll').toUpperCase(), c80: hrAmt(hrV('td-80c')), d80: hrAmt(hrV('td-80d')), nps_1b: hrAmt(hrV('td-nps')), home_loan: hrAmt(hrV('td-hl')), other_income: hrAmt(hrV('td-oi')), prev_income: hrAmt(hrV('td-pi')), prev_tds: hrAmt(hrV('td-pt')) } }), 'Saved')); },
  hrTaxProof: () => { const v = C.me; hrPrompt('Upload a proof', 'Rent receipts, investment proofs, insurance premium receipts. PDF or image.', `<div class="fld full"><label for="tp-f">File</label><input type="file" id="tp-f" accept="application/pdf,image/*"></div>`, 'Upload', () => act(() => hrUpload(`/v1/payroll/tax-declarations/proofs?employee=${hrEnc(v.profile.code)}&fy=${hrEnc(v.fy)}`, 'tp-f'), 'Uploaded')); },
  hrClaim: () => hrPrompt('Claim a reimbursement', 'With the bill. Approved claims are paid with the next salary.', hrF('cl-k', 'Kind', { opts: ['Travel', 'Conveyance', 'Mobile and internet', 'Meals', 'Medical', 'Other'] }) + hrF('cl-d', 'Date', { type: 'date' }) + hrF('cl-a', 'Amount (₹)', { mono: 1 }) + hrF('cl-n', 'What for') + `<div class="fld full"><label for="cl-f">Bill</label><input type="file" id="cl-f" accept="application/pdf,image/*"></div>`, 'Claim',
    async () => { const f = $('cl-f').files[0]; const b = { kind: hrV('cl-k'), date: hrV('cl-d'), amount: hrAmt(hrV('cl-a')), note: hrV('cl-n') }; if (f) { b.bill_file = await hrB64(f); b.bill_name = f.name; } return act(() => post('/v1/payroll/claims', b), 'Claimed'); }),
  modSave: () => act(() => api('PUT', '/v1/modules', { hrms: $('mod-hrms').checked, payroll: $('mod-payroll').checked }), 'Saved').then(async () => { try { S.me = await get('/v1/me'); } catch (e) {} render(); }),
  hrPolicySave: () => {
    const p = C.hrpolicy, nums = s => s.split(',').map(x => parseInt(x, 10)).filter(x => x > 0);
    const b = { ...p, cert_rules: hrRowsRead('cert'), reminder_days: nums(hrV('hp-rem')), leave_types: hrRowsRead('lt'), leave_year: hrV('hp-ly'), shifts: hrRowsRead('sh'), holidays: hrRowsRead('hol'), exchange_holidays: $('hp-exh').checked, sites: hrRowsRead('site'), use_signin_networks: $('hp-nets').checked, work_policies: hrRowsRead('wp'),
      selfie_days: parseInt(hrV('hp-selfie'), 10) || 0, deactivate_at: hrV('hp-deact'), absent_is_lop: $('hp-lop').checked, dealers_home_free: $('hp-dhf').checked,
      own_trading: { ...(p.own_trading || {}), applies: hrV('hp-ot-app'), pre_clear_above: parseInt(hrV('hp-ot-above'), 10) || 0, pre_clear_valid_days: parseInt(hrV('hp-ot-valid'), 10) || 0, contra_days: parseInt(hrV('hp-ot-contra'), 10) || 0, annual_due_month: parseInt(hrV('hp-ot-due'), 10) || 0, restricted: hrV('hp-ot-restr').split(',').map(s => s.trim().toUpperCase()).filter(Boolean), basis: hrV('hp-ot-basis'), relatives_covered: $('hp-ot-rel').checked, outside_allowed: $('hp-ot-out').checked } };
    return act(() => api('PUT', '/v1/hr/policy', b), 'Policies saved').catch(() => {});
  },
  // Payroll.
  payStep: step => {
    const m = UI.payMonth || hrThisMonth(), go = (b, msg) => act(() => post(`/v1/payroll/runs/${m}/${step}`, b || {}), msg);
    if (step === 'return') return hrPrompt('Return the run', 'Payroll makes it again.', hrF('pr-r', 'Why'), 'Return', () => go({ reason: hrV('pr-r') }, 'Returned'));
    if (step === 'paid') return hrPrompt('Mark salary paid', 'After the bank file is processed: salary payable moves to the bank in the books.', hrF('pp-d', 'Paid on', { type: 'date' }) + hrF('pp-r', 'Bank reference'), 'Mark paid', () => go({ date: hrV('pp-d'), ref: hrV('pp-r') }, 'Marked paid'));
    if (step === 'approve') return confirmModal('Approve the run', 'Posts the month to the firm’s books: salary expense and the employer’s PF and ESI against salary payable and the statutory dues.', '', 'Approve and post', () => go({}, 'Approved and posted'));
    if (step === 'cbx') return confirmModal('Send salary to HDFC', 'A CBX batch under the firm’s own CBX client code. The payments wait in CBX for the firm’s own verifier and authorisers.', '', 'Send', () => go({}, r => `Sent: ${r.state}`));
    if (step === 'email') return go({}, r => `${Object.values(r).filter(x => /^Sent/.test(x)).length} payslips sent`);
    return go({}, r => r.status ? 'Run ' + r.status.toLowerCase() : 'Done');
  },
  payLine: code => { const l = (C.payroll?.run?.lines || []).find(x => x.employee === code); if (!l) return; const t = l.tax || {};
    const row = (k, v) => `<tr><td>${k}</td><td class="r">${v}</td></tr>`;
    wideModal(mhd(`${esc(l.name)} · ${esc(C.payroll.month)}`, `${esc(l.state || '')} · ${esc(l.regime || 'new')} regime · ${hrDays(l.paid_days)} of ${l.month_days} days paid`) + `<div class="bd grid g2"><div><h4>Earnings</h4><table class="tbl small">${(l.earnings || []).map(e => row(esc(e.name || e.code), inr(e.amount))).join('')}${row('<b>Gross</b>', '<b>' + inr(l.gross) + '</b>')}</table>
      <h4>Deductions</h4><table class="tbl small">${row('PF (employee, on ' + inr(l.pf_wages) + ')', inr(l.emp_pf))}${row('ESI (employee, on ' + inr(l.esi_wages) + ')', inr(l.emp_esi))}${row('Professional tax', inr(l.pt))}${row('TDS', inr(l.tds))}${(l.deductions || []).map(e => row(esc(e.name || e.code), inr(e.amount))).join('')}${num(l.recovery) ? row('Advance recovery', inr(l.recovery)) : ''}${row('<b>Net pay</b>', '<b>' + inr(l.net) + '</b>')}</table>
      <h4>Employer</h4><table class="tbl small">${row('EPF', inr(l.er_epf))}${row('EPS', inr(l.er_eps))}${row('EDLI', inr(l.edli))}${row('PF admin', inr(l.pf_admin))}${row('ESI', inr(l.er_esi))}</table></div>
      <div><h4>Tax for the year</h4><table class="tbl small">${row('Salary for the year', inr(t.gross_year))}${row('HRA exempt', inr(t.hra_exempt))}${row('Standard deduction', inr(t.std_deduction))}${row('Professional tax', inr(t.pt_year))}${row('80C', inr(t.ded_80c))}${row('80D', inr(t.ded_80d))}${row('NPS', inr(t.ded_nps))}${row('Home loan interest', inr(t.home_loan))}${row('Other income', inr(t.other_income))}${row('<b>Taxable income</b>', '<b>' + inr(t.taxable_income) + '</b>')}${row('Tax on slabs', inr(t.slab_tax))}${row('Rebate', inr(t.rebate))}${row('Surcharge', inr(t.surcharge))}${row('Cess', inr(t.cess))}${row('<b>Tax for the year</b>', '<b>' + inr(t.tax_year) + '</b>')}${row('Deducted before', inr(t.paid_before))}${row('Months left', t.months_left)}</table></div></div>`); },
  payInput: code => { const m = UI.payMonth || hrThisMonth();
    hrPrompt('Inputs for ' + esc(code), 'For this month only. Leave LOP empty to take it from attendance and leave.', hrF('pi-lop', 'Loss of pay days (blank: from attendance)', { mono: 1 }) + hrF('pi-arr', 'Arrears (₹, for earlier months)', { mono: 1 }) + hrF('pi-inc', 'Incentive or bonus (₹)', { mono: 1 }) + hrF('pi-ded', 'Other deduction (₹)', { mono: 1 }) + hrF('pi-dn', 'What the deduction is for') + `<label class="check"><input type="checkbox" id="pi-hold"> Hold this person’s salary</label>` + hrF('pi-n', 'Note', { full: 1 }), 'Save',
      () => { const b = { month: m, employee: code, hold: $('pi-hold').checked, note: hrV('pi-n'), arrears: [], earnings: [], deductions: [] };
        if (hrV('pi-lop') !== '') b.lop = parseFloat(hrV('pi-lop'));
        if (hrV('pi-arr')) b.arrears.push({ code: 'ARREARS', name: 'Arrears', amount: hrAmt(hrV('pi-arr')), pf_wage: true, esi_wage: true });
        if (hrV('pi-inc')) b.earnings.push({ code: 'INCENTIVE', name: 'Incentive', amount: hrAmt(hrV('pi-inc')), esi_wage: true });
        if (hrV('pi-ded')) b.deductions.push({ code: 'OTHER', name: hrV('pi-dn') || 'Other deduction', amount: hrAmt(hrV('pi-ded')) });
        return act(() => post('/v1/payroll/inputs', b).then(() => post(`/v1/payroll/runs/${m}/make`, {})), 'Saved and the run made again'); }); },
  payStruct: async code => { const cur = (C.payroll?.ov?.structures || {})[code];
    let comps = cur ? cur.components : [];
    const draw = () => `<div class="form">${hrF('st-from', 'From', { type: 'date', v: '' })}${hrF('st-g', 'Or fill a standard split for a monthly gross (₹)', { mono: 1 })}</div><button class="btn sm" id="st-std">Fill standard split</button>
      ${hrRows('comp', [['code', 'Code', 'text', 80], ['name', 'Name', 'text', 140], ['amount', 'Monthly ₹', 'text', 90], ['pf_wage', 'PF wage', 'bool'], ['esi_wage', 'ESI wage', 'bool'], ['exempt', 'Tax exempt', 'bool']], comps, true)}`;
    wideModal(mhd('Salary structure · ' + esc(code), 'Monthly amounts. PF is on the components marked PF wage (at least 50% of pay counts under the Code on Wages when the statutory table says so); ESI eligibility is on the whole monthly pay.') + `<div class="bd" id="st-bd">${draw()}<div id="st-err"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="st-ok">Save</button></div>`);
    const bind = () => { $('st-std').onclick = async () => { try { comps = await get('/v1/payroll/structures/standard?gross=' + hrAmt(hrV('st-g'))); const f = hrV('st-from'); $('st-bd').innerHTML = draw() + '<div id="st-err"></div>'; $('st-from').value = f; bind(); } catch (e) { $('st-err').innerHTML = errHTML(e); } }; };
    bind();
    $('st-ok').onclick = async () => { try { await post('/v1/payroll/structures', { employee: code, from: hrV('st-from'), components: hrRowsRead('comp').map(c => ({ ...c, amount: hrAmt(String(c.amount)) })) }); closeModal(); toast('Saved'); render({ keep: true }); } catch (e) { $('st-err').innerHTML = errHTML(e); } };
  },
  payLoanNew: () => hrPrompt('Give an advance or loan', 'A second person (Payroll or HR) approves it and records the day it was paid. Recovered from salary in instalments.', hrF('ln-e', 'Employee code', { mono: 1 }) + hrF('ln-k', 'Kind', { opts: ['Advance', 'Loan'] }) + hrF('ln-a', 'Amount (₹)', { mono: 1 }) + hrF('ln-i', 'Monthly instalment (₹)', { mono: 1 }) + hrF('ln-s', 'First recovery month', { type: 'month' }), 'Save',
    () => act(() => post('/v1/payroll/loans', { employee: hrV('ln-e').toUpperCase(), kind: hrV('ln-k'), amount: hrAmt(hrV('ln-a')), instalment: hrAmt(hrV('ln-i')), start_month: hrV('ln-s') }), 'Waiting for approval')),
  payLoanApprove: id => hrPrompt('Approve and pay', 'Posts the advance from the firm’s bank to staff advances.', hrF('la-p', 'Paid on', { type: 'date' }), 'Approve', () => act(() => post(`/v1/payroll/loans/${hrEnc(id)}/approve`, { paid_on: hrV('la-p') }), 'Approved')),
  payClaimDecide: id => { const [c, ok] = id.split('|'); return act(() => post('/v1/payroll/claims/' + hrEnc(c), { approve: ok === '1' }), ok === '1' ? 'Approved: paid with the next run' : 'Refused'); },
  payVerify: id => { const [code, fy] = id.split('|'), x = (C.payroll?.ov?.declarations || []).find(d => d.employee === code && d.fy === fy) || { declared: {} }, v = x.verified || x.declared;
    hrPrompt('Verify proofs · ' + esc(code), 'Enter the amounts the proofs support. TDS from the next run uses them.', hrF('vf-rent', 'Rent a month (₹)', { mono: 1, v: v.rent_monthly }) + hrF('vf-80c', '80C (₹)', { mono: 1, v: v.c80 }) + hrF('vf-80d', '80D (₹)', { mono: 1, v: v.d80 }) + hrF('vf-nps', 'NPS (₹)', { mono: 1, v: v.nps_1b }) + hrF('vf-hl', 'Home loan interest (₹)', { mono: 1, v: v.home_loan }), 'Verify',
      () => act(() => post('/v1/payroll/tax-declarations/verify', { employee: code, fy, verified: { ...v, rent_monthly: hrAmt(hrV('vf-rent')), c80: hrAmt(hrV('vf-80c')), d80: hrAmt(hrV('vf-80d')), nps_1b: hrAmt(hrV('vf-nps')), home_loan: hrAmt(hrV('vf-hl')) } }), 'Verified')); },
  payF138: id => { const [q, an] = String(id).split('|'), fy = hrV('pay-fy'); download(`/v1/payroll/form138?fy=${hrEnc(fy)}&quarter=${q}${an ? '&annexure=2' : ''}`, `form138-${fy}-Q${q}.csv`); },
  payFnF: code => hrPrompt('Full and final settlement', 'Leave encashment, gratuity (five years’ service or more), notice pay recovered and loans outstanding, for the leaver’s last month.', hrF('fn-n', 'Notice pay to recover (₹)', { mono: 1, v: '0' }), 'Work it out',
    async () => { const r = await post('/v1/payroll/fnf/' + hrEnc(code), { notice_recovery: hrAmt(hrV('fn-n')) }); modal(mhd('Full and final · ' + esc(code), `Last day ${fmtDate(r.last_day)} · paid with the ${esc(r.month)} run`) + `<div class="bd"><table class="tbl small"><tr><td>Service</td><td class="r">${r.service_years} years</td></tr><tr><td>Leave encashed (${hrDays(r.leave_days)} days)</td><td class="r">${inr(r.leave_encash)}</td></tr><tr><td>Gratuity</td><td class="r">${inr(r.gratuity)}</td></tr><tr><td>Notice pay recovered</td><td class="r">${inr(r.notice_recovery)}</td></tr><tr><td>Loans outstanding (recovered)</td><td class="r">${inr(r.loans_left)}</td></tr></table><ul class="small">${(r.basis || []).map(b => `<li>${esc(b)}</li>`).join('')}</ul></div><div class="ft"><button class="btn pri" data-act="close">Close</button></div>`); }),
  paySettingsSave: () => act(() => api('PUT', '/v1/payroll/settings', { pf_code: hrV('ps-pf'), esi_code: hrV('ps-esi'), tan: hrV('ps-tan').toUpperCase(), default_state: hrV('ps-state'), pay_day: parseInt(hrV('ps-day'), 10) || 0, bank_account: hrV('ps-bank'), debit_account: hrV('ps-debit'), encash_divisor: parseInt(hrV('ps-div'), 10) || 0, proof_cutoff: parseInt(hrV('ps-cut'), 10) || 0 }), 'Saved'),
  payStatNew: () => { const cur = (C.payroll?.stat || [])[0] || {}; const b = { ...cur, from: '', source: '' };
    wideModal(mhd('New statutory table', 'Start from the table in force and change what the new notification changes. Give the date it applies from and its source (notification, circular or Act section). Rates are per cent; ceilings and slabs in rupees.') + `<div class="bd"><div class="form">${hrF('sn-from', 'Applies from', { type: 'date' })}${hrF('sn-src', 'Source', { full: 1 })}</div><div class="fld"><label for="sn-json">The table</label><textarea class="in mono" id="sn-json" rows="22" spellcheck="false">${esc(JSON.stringify(b, null, 1))}</textarea></div><div id="sn-err"></div></div><div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="sn-ok">Save</button></div>`);
    $('sn-ok').onclick = async () => { try { const t = JSON.parse($('sn-json').value); t.from = hrV('sn-from'); t.source = hrV('sn-src'); await post('/v1/payroll/statutory', t); closeModal(); toast('Saved'); render({ keep: true }); } catch (e) { $('sn-err').innerHTML = errHTML(e instanceof SyntaxError ? new Error('The table is not valid: ' + e.message) : e); } };
  },
});

document.addEventListener('change', e => {
  const id = e.target.id;
  const set = (k, v) => { UI[k] = v; render({ keep: true }); };
  if (id === 'me-month') set('hrMeMonth', e.target.value);
  else if (id === 'hr-att-month') set('hrAttMonth', e.target.value);
  else if (id === 'hr-lv-month') set('hrLvMonth', e.target.value);
  else if (id === 'pay-month') set('payMonth', e.target.value);
  else if (id === 'hr-alllists') set('hrAllLists', e.target.checked);
  else if (id === 'pay-fy') UI.payFY = e.target.value;
});
document.addEventListener('input', e => {
  if (e.target.id !== 'hr-q') return;
  UI.hrQ = e.target.value; clearTimeout(UI.hrQT);
  UI.hrQT = setTimeout(() => { const pos = e.target.selectionStart; $('page').innerHTML = groupBar(S.view) + V.people.html(C.people); bindPage(); const q = $('hr-q'); if (q) { q.focus(); q.setSelectionRange(pos, pos); } }, 200);
});
