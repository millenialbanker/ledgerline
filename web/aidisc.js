'use strict';
/* Ledgerline console: AI controls ("aictl": the kill switch, masking, the
   AI inventory and the AI answer log) and Exchange disclosures ("exdisc":
   the AI & ML and SaaS reports, ready to copy into the exchange portals,
   with the filings). Both are tabs of the regulatory reports group.
   Loaded after app.js. */

Object.assign(UI, { aiTab: 'switch', aiFrom: '', aiTo: '', edTab: 'aiml', edAiml: '', edSaas: '' });
const aiStamp = t => { const d = new Date(t); return isNaN(d) ? esc(t || '') : d.toLocaleString('en-IN', { dateStyle: 'medium', timeStyle: 'short' }); };
const aiCan = () => has('Compliance officer');
const aiOutcome = s => pill(s, { Answered: 'good', 'Provider error': 'bad', 'Not metered': 'warn', 'Empty answer': 'warn', 'Unreadable answer': 'warn' }[s] || 'nt');

/* ---------- AI controls ---------- */
V.aictl = {
  load: async () => {
    const [c, log] = await Promise.all([get('/v1/ai/controls'), get(`/v1/ai/log?from=${UI.aiFrom}&to=${UI.aiTo}`)]);
    return { c, log };
  },
  html: d => {
    const c = d.c, k = c.controls;
    const server = c.server_off ? ['Off', 'bad', 'LEDGERLINE_AI_OFF is set'] : c.connected ? ['On', 'good', esc(c.provider_name + ' · ' + c.model)] : ['Not connected', 'nt', 'no AI provider on this server'];
    const firm = k.off ? ['Off', 'bad', 'since ' + aiStamp(k.off_at) + ' · ' + esc(k.off_reason)] : c.killed ? ['Off', 'bad', esc(c.killed)] : ['On', 'good', 'features still need their Settings switch'];
    const on = c.systems.filter(s => s.on).length;
    return ph('AI controls', 'Every AI use in Ledgerline and the firm’s AI tools, the switches that stop them, what is masked before anything is sent, and the log of every call. Each call goes through one check: the server’s switch, the firm’s switch, the provider’s switch and the feature’s own setting. A change applies to the next call.') +
      `<div class="kpis">${kpi('Server AI', server[0], server[2], server[1])}${kpi('Firm AI', firm[0], firm[2], firm[1])}${kpi('AI systems on', nf(on), c.systems.length + ' in the inventory', on ? 'warn' : '')}${kpi('Calls logged', nf(d.log.calls), `${fmtDate(d.log.from)} to ${fmtDate(d.log.to)}`)}</div>` +
      `<div class="panel"><div class="hd">${segBtns('aiTab', [['switch', 'Switches & masking'], ['inv', `AI inventory · ${c.systems.length}`], ['log', `Answer log · ${d.log.calls}`]])}</div></div>` +
      ({ switch: aiSwitches, inv: aiInventoryHTML, log: aiLogHTML }[UI.aiTab] || aiSwitches)(d);
  }
};

function aiSwitches(d) {
  const c = d.c, k = c.controls, can = aiCan(), admin = has();
  const kill = `<div class="panel"><div class="hd"><h3>Kill switch</h3><small>Admin or compliance officer · the reason goes in the audit trail</small></div><div class="bd">
    ${c.server_off ? '<div class="note bad"><b>AI is switched off on this server for every firm</b> (LEDGERLINE_AI_OFF). Ask whoever runs the server.</div>' : ''}
    ${c.india_only ? '<div class="note">India-only mode: AI features that send data abroad are refused.</div>' : ''}
    <div class="split"><div>${k.off ? `<b class="neg">AI is off for the firm.</b> <span class="dim small">${esc(k.off_reason)} · ${esc(k.off_by)} · ${aiStamp(k.off_at)}</span>` : '<b>AI is allowed for the firm.</b> <span class="dim small">Each feature still needs its own switch in Settings.</span>'}</div>
    ${can ? `<button class="btn ${k.off ? 'pri' : 'bad'}" data-act="aiOff" data-id="${k.off ? '0' : '1'}">${k.off ? 'Switch AI back on' : 'Switch all AI off'}</button>` : ''}</div></div>
    ${tbl([['Provider'], ['Where'], ['On this server'], ['For the firm'], ['']], c.providers.map(p => `<tr><td><b>${esc(p.name)}</b></td><td class="small">${esc(p.where)}</td><td>${p.connected ? pill('Connected', 'acc') : '<span class="dim">—</span>'}</td>
      <td>${p.off ? `${pill('Off', 'bad')}<div class="dim small wrap">${esc(p.off.reason)} · ${esc(p.off.by)}</div>` : pill('Allowed', 'good')}</td><td>${can ? `<button class="btn sm ${p.off ? '' : 'bad'}" data-act="aiProv" data-id="${esc(p.id)}" data-v="${p.off ? '0' : '1'}">${p.off ? 'Allow' : 'Switch off'}</button>` : ''}</td></tr>`))}</div>`;
  const mask = `<div class="panel"><div class="hd"><h3>Masking before any AI call</h3><small>Identifiers become tokens such as [PAN-1] and are put back only in the answer shown to the user. The answer log is always fully masked.</small></div>
    ${tbl([['What is masked'], ['State'], ['']], c.masking.map(m => `<tr><td>${esc(m.label)}</td><td>${m.on ? pill('Masked', 'good') : pill('Sent as it is', 'warn')}${m.locked ? ' <span class="dim small">always</span>' : ''}</td>
      <td>${admin && !m.locked ? `<button class="btn sm" data-act="aiMask" data-id="${esc(m.id)}" data-v="${m.on ? '0' : '1'}">${m.on ? 'Stop masking' : 'Mask'}</button>` : ''}</td></tr>`))}
    <div class="bd dim small">Names and client codes come from the firm’s own client and staff lists; PAN, Aadhaar-like numbers, phones, emails and account numbers are found by their shape. Only an Admin can change masking; PAN, Aadhaar and account numbers are always masked.</div></div>`;
  const hist = `<div class="panel"><div class="hd"><h3>Changes</h3><small>newest first</small></div>${tbl([['When'], ['Who'], ['What'], ['Reason']], (k.changes || []).slice().reverse().slice(0, 30).map(x => `<tr><td class="small">${aiStamp(x.at)}</td><td>${esc(x.by)}</td><td>${esc(x.what)}</td><td class="small wrap">${esc(x.reason || '')}</td></tr>`), { empty: 'No changes yet.' })}</div>`;
  return kill + mask + hist;
}

function aiInventoryHTML(d) {
  const can = aiCan();
  return `<div class="panel"><div class="hd"><h3>AI inventory</h3><small>Ledgerline’s own AI uses, from its code, and the AI tools the firm uses elsewhere. It feeds the AI &amp; ML report on Exchange disclosures.</small>${can ? '<button class="btn sm pri" data-act="aiTool">Add an AI tool</button>' : ''}</div>
    ${tbl([['System'], ['Purpose'], ['Provider · model'], ['Data sent'], ['Who can use it'], ['State'], ['']], d.c.systems.map(s => `<tr><td><b>${esc(s.name)}</b><div class="dim small">${s.source === 'firm' ? 'Firm’s own tool' + (s.vendor ? ' · ' + esc(s.vendor) : '') : 'In Ledgerline'}</div></td><td class="small wrap">${esc(s.purpose)}</td>
      <td class="small">${esc([s.provider, s.model].filter(Boolean).join(' · ') || s.tool || '—')}</td><td class="small wrap">${esc(s.data_sent || s.data_nature || '')}</td><td class="small wrap">${esc(s.who_can_use || '')}</td>
      <td>${s.on ? pill('On', 'warn') : pill('Off', 'nt')}${!s.on && s.off_why ? `<div class="dim small wrap">${esc(s.off_why)}</div>` : ''}${s.switch ? `<div class="dim small">${esc(s.switch)}</div>` : ''}</td>
      <td class="nowrap"><button class="btn sm" data-act="aiSys" data-id="${esc(s.id)}">Controls</button>${can && s.source === 'firm' ? `<button class="btn sm" data-act="aiTool" data-id="${esc(s.id)}">Edit</button>` : ''}</td></tr>`))}</div>`;
}

function aiLogHTML(d) {
  const l = d.log, can = aiCan();
  return `<div class="panel"><div class="hd"><div class="tools"><input class="in sm" type="date" id="ai-from" value="${esc(l.from)}"><span class="dim">to</span><input class="in sm" type="date" id="ai-to" value="${esc(l.to)}"><button class="btn sm" data-act="aiLogShow">Show</button>
      <button class="btn sm" data-act="dl" data-id="/v1/ai/log.csv?from=${esc(l.from)}&to=${esc(l.to)}" data-name="ai_answer_log_${esc(l.from)}_${esc(l.to)}.csv">Download CSV</button></div>
      <small>${nf(l.tokens)} tokens · ${inr(l.charged, 2)} charged · kept ${nf(l.retention_days)} days${can ? ` <button class="btn sm" data-act="aiRetention">Change</button>` : ''}</small></div>
    ${tbl([['When'], ['User'], ['Feature'], ['Model'], ['Tokens', 'r'], ['Charge', 'r'], ['Outcome'], ['Prompt and answer (masked)']], l.entries.map(e => `<tr><td class="small">${aiStamp(e.at)}<div class="dim mono">${esc(e.id)}</div></td><td>${esc(e.actor)}</td><td>${esc(e.feature)}</td><td class="small">${esc(e.provider)}<div class="dim">${esc(e.model)}</div></td>
      <td class="r">${nf(e.input_tokens)} / ${nf(e.output_tokens)}</td><td class="r">${inr(e.charge, 2)}</td><td>${aiOutcome(e.outcome)}${e.note ? `<div class="dim small wrap">${esc(e.note)}</div>` : ''}</td>
      <td class="small wrap">${e.cleared ? '<span class="dim">Cleared after the retention period</span>' : `<b>Q:</b> ${esc(e.prompt)}${e.context ? `<div class="dim">${esc(e.context)}</div>` : ''}<div><b>A:</b> ${esc(e.answer)}</div>${e.masked ? `<div class="dim">masked: ${esc(e.masked)}</div>` : ''}`}</td></tr>`), { tall: true, empty: 'No AI calls in this period.' })}</div>`;
}

const aiSystemFind = id => C.aictl?.c?.systems.find(s => s.id === id);

Object.assign(A, {
  aiOff: id => {
    const off = id === '1';
    confirmModal(off ? 'Switch all AI off' : 'Switch AI back on', off ? 'Every AI feature stops at the next call: Ledgy answers from its articles, bills are typed. Nothing else changes.' : 'Features switched on in Settings may call the AI model again.',
      `<div class="fld"><label for="ai-why">Reason (kept in the audit trail)</label><input class="in" id="ai-why" placeholder="${off ? 'e.g. Vendor risk review pending' : 'e.g. Review completed, board note 12/2026'}"></div>`, off ? 'Switch off' : 'Switch on',
      () => act(() => post('/v1/ai/off', { off, reason: $('ai-why').value }), off ? 'AI switched off' : 'AI switched on'), off);
  },
  aiProv: (id, el) => {
    const off = el.dataset.v === '1';
    confirmModal((off ? 'Switch off ' : 'Allow ') + id, off ? 'No AI feature will call this provider.' : 'AI features may call this provider again.', '<div class="fld"><label for="ai-pw">Reason</label><input class="in" id="ai-pw"></div>', off ? 'Switch off' : 'Allow',
      () => act(() => post(`/v1/ai/providers/${encodeURIComponent(id)}`, { off, reason: $('ai-pw').value }), 'Saved'), off);
  },
  aiMask: (id, el) => act(() => post('/v1/ai/masking', { category: id, on: el.dataset.v === '1' }), 'Masking changed'),
  aiRetention: () => confirmModal('AI answer log retention', 'Older entries keep when, who, tokens, charge and outcome; their prompt and answer are cleared.', `<div class="fld"><label for="ai-ret">Keep prompts and answers for (days)</label><input class="in" id="ai-ret" type="number" min="365" max="3650" value="${C.aictl.log.retention_days}"></div>`, 'Save',
    () => act(() => post('/v1/ai/log-retention', { days: parseInt($('ai-ret').value, 10) || 0 }), 'Retention saved')),
  aiLogShow: () => { UI.aiFrom = $('ai-from').value; UI.aiTo = $('ai-to').value; render({ keep: true }); },
  aiSys: id => {
    const s = aiSystemFind(id); if (!s) return;
    modal(mhd(esc(s.name), esc(s.purpose)) + `<div class="bd"><h4 style="margin-top:0">Controls</h4><ul class="small">${(s.controls || []).map(x => `<li>${esc(x)}</li>`).join('') || '<li class="dim">None recorded</li>'}</ul>
      <h4>For the AI &amp; ML report</h4><div class="small">Order execution ${esc(s.order_execution)} · discretionary ${esc(s.discretionary)} · advice ${esc(s.advice)} · cyber security ${esc(s.cyber_security)} · in the system audit ${esc(s.in_system_audit || 'NA')}</div>
      <p class="small"><b>Implemented:</b> ${esc(s.implemented || '')}</p><p class="small"><b>Safeguards:</b> ${esc(s.safeguards || '')}</p></div><div class="ft"><button class="btn" data-act="close">Close</button></div>`);
  },
  aiTool: id => {
    const s = (id && aiSystemFind(id)) || { active: true, order_execution: 'N', discretionary: 'N', advice: 'N', cyber_security: 'N', in_system_audit: 'NA' };
    const ynSel = (k, na) => `<select class="in" id="at-${k}">${(na ? ['Y', 'N', 'NA'] : ['Y', 'N']).map(x => `<option ${s[k] === x ? 'selected' : ''}>${x}</option>`).join('')}</select>`;
    const txt = (k, l, full) => `<div class="fld ${full ? 'full' : ''}"><label for="at-${k}">${l}</label><input class="in" id="at-${k}" value="${esc(s[k] || '')}"></div>`;
    const area = (k, l) => `<div class="fld full"><label for="at-${k}">${l}</label><textarea class="in" id="at-${k}">${esc(s[k] || '')}</textarea></div>`;
    confirmModal(id ? 'Edit ' + esc(s.name) : 'Add an AI tool', 'An AI or ML tool the firm uses outside Ledgerline (a surveillance or RMS model, a chatbot, a research tool). It joins the AI inventory and, while in use, the AI &amp; ML report.',
      `<div class="form">${txt('name', 'Name')}${txt('vendor', 'Vendor (blank if in-house)')}${area('purpose', 'What it is used for')}${txt('provider', 'Provider or model')}${txt('tool', 'Name of the tool or technology')}
      ${area('data_sent', 'Data it receives')}${txt('who_can_use', 'Who uses it', true)}
      <div class="fld"><label for="at-order_execution">Order initiation, routing or execution?</label>${ynSel('order_execution')}</div><div class="fld"><label for="at-discretionary">Discretionary investment or portfolio management?</label>${ynSel('discretionary')}</div>
      <div class="fld"><label for="at-advice">Gives investment or trading advice?</label>${ynSel('advice')}</div><div class="fld"><label for="at-cyber_security">Used in cyber security?</label>${ynSel('cyber_security')}</div>
      <div class="fld"><label for="at-in_system_audit">In the system audit?</label>${ynSel('in_system_audit', true)}</div>${txt('implemented', 'How it was implemented (in-house, vendor…)')}
      ${area('claims', 'Claims made about it')}${area('description', 'How it uses AI/ML')}${area('safeguards', 'Safeguards against abnormal behaviour')}
      <div class="fld"><label for="at-in_use_from">In use from</label><input class="in" type="date" id="at-in_use_from" value="${esc(s.in_use_from || '')}"></div><div class="fld"><label for="at-in_use_to">In use until</label><input class="in" type="date" id="at-in_use_to" value="${esc(s.in_use_to || '')}"></div>
      <label class="check"><input type="checkbox" id="at-saas" ${s.saas ? 'checked' : ''}> A cloud (SaaS) service: also a row of the SaaS report</label>${txt('hosted_at', 'Where it is hosted')}
      <label class="check full"><input type="checkbox" id="at-active" ${s.active ? 'checked' : ''}> In use</label></div>`, 'Save', () => {
        const v = { id: s.source === 'firm' ? s.id : '' };
        ['name', 'vendor', 'purpose', 'provider', 'tool', 'data_sent', 'who_can_use', 'order_execution', 'discretionary', 'advice', 'cyber_security', 'in_system_audit', 'implemented', 'claims', 'description', 'safeguards', 'in_use_from', 'in_use_to', 'hosted_at'].forEach(k => { v[k] = $('at-' + k).value.trim(); });
        v.saas = $('at-saas').checked; v.active = $('at-active').checked; v.controls = s.controls || [];
        return act(() => post('/v1/ai/tools', v), 'Saved');
      });
  },
});

/* ---------- Exchange disclosures ---------- */
const edPill = s => pill(s, { Submitted: 'good', 'Partly submitted': 'warn', Due: 'warn', Overdue: 'bad' }[s] || 'nt');
V.exdisc = {
  load: () => get(`/v1/disclosures?aiml=${encodeURIComponent(UI.edAiml)}&saas=${encodeURIComponent(UI.edSaas)}`),
  html: d => ph('Exchange disclosures', 'Ready answers for the AI &amp; ML systems report and the SaaS use report the exchanges collect on their member portals, worked out from Ledgerline’s settings, the AI inventory and its history. Check and edit each answer, copy it into the portal, then mark it submitted with the reference. Each exchange’s filing is a duty in the compliance calendar until it is.') +
    `<div class="panel"><div class="hd">${segBtns('edTab', [['aiml', `AI &amp; ML · ${esc(d.aiml.period.label)} · ${esc(d.aiml.status)}`], ['saas', `SaaS · ${esc(d.saas.period.label)} · ${esc(d.saas.status)}`], ['details', 'Details for the forms']])}</div></div>` +
    (UI.edTab === 'details' ? edDetails(d) : edReport(UI.edTab === 'saas' ? d.saas : d.aiml))
};

function edReport(v) {
  const can = aiCan() && !v.locked;
  const secs = []; v.fields.forEach(f => { let s = secs.find(x => x.name === f.section); if (!s) secs.push(s = { name: f.section, fields: [] }); s.fields.push(f); });
  const ex = tbl([['Exchange'], ['Filing'], ['']], v.exchanges.map(e => `<tr><td><b>${esc(e.exchange)}</b></td><td class="small">${e.submitted ? `${pill('Submitted', 'good')} ${fmtDate(e.submitted.date)} · reference <span class="mono">${esc(e.submitted.ref)}</span> · ${esc(e.submitted.by)}${e.submitted.late ? ' ' + pill('Late', 'bad') : ''}${e.submitted.note ? `<div class="dim">${esc(e.submitted.note)}</div>` : ''}` : `${edPill(v.status === 'Partly submitted' ? 'Due' : v.status)} due ${fmtDate(v.period.due)}`}</td>
    <td class="nowrap">${e.submitted?.file_id ? `<button class="btn sm" data-act="dl" data-id="/v1/disclosures/evidence/${esc(e.submitted.file_id)}" data-name="${esc(e.submitted.file_name)}">Acknowledgement</button>` : ''}${!e.submitted && aiCan() ? `<button class="btn sm pri" data-act="edSubmit" data-id="${esc(e.exchange)}">Mark submitted</button>` : ''}</td></tr>`), { empty: 'No exchange to file with: enter member codes in Settings or choose exchanges under Details for the forms.' });
  return `<div class="panel"><div class="hd"><div><h3>${esc(v.title)} · ${esc(v.period.label)}</h3><small>${esc(v.basis)}</small></div>
      <div class="tools"><select class="in sm" id="ed-period" aria-label="Period">${v.periods.map(p => `<option value="${esc(p.id)}" ${p.id === v.period.id ? 'selected' : ''}>${esc(p.label)} · due ${fmtDate(p.due)}</option>`).join('')}</select>
      <button class="btn sm" data-act="edCopyAll">Copy all</button><button class="btn sm" data-act="dl" data-id="/v1/disclosures/${v.kind}/${esc(v.period.id)}/csv" data-name="${v.kind}_disclosure_${esc(v.period.id)}.csv">Download CSV</button></div></div>
    <div class="bd"><div class="kpis">${kpi('Answer', esc(v.answer), v.kind === 'aiml' ? 'any AI or ML system in use' : 'data hosted on SaaS', v.answer === 'Yes' ? 'warn' : 'good')}${kpi('Status', esc(v.status), 'due ' + fmtDate(v.period.due), { Submitted: 'good', Overdue: 'bad' }[v.status] || 'warn')}${kpi(v.kind === 'aiml' ? 'Systems' : 'SaaS services', nf(v.systems.length), esc(v.systems.join(' · ')))}</div>
    ${v.notes.length ? `<ul class="small dim">${v.notes.map(n => `<li>${esc(n)}</li>`).join('')}</ul>` : ''}${v.locked ? '<div class="note">Marked submitted: the answers are kept as filed. Each filing keeps a copy of what was filed.</div>' : ''}</div>${ex}</div>` +
    secs.map(s => `<div class="panel"><div class="hd"><h3>${esc(s.name)}</h3></div>${tbl([['Question'], ['Answer'], ['']], s.fields.map(f => `<tr><td class="small wrap" style="max-width:340px">${esc(f.label)}${f.hint ? `<div class="dim">${esc(f.hint)}</div>` : ''}</td>
      <td class="wrap" style="white-space:pre-wrap">${f.value ? esc(f.value) : '<span class="neg">Not filled</span>'}${f.edited ? ' ' + pill('Edited', 'acc') : ''}</td>
      <td class="nowrap"><button class="btn sm" data-act="edCopy" data-id="${esc(f.key)}">Copy</button>${can ? `<button class="btn sm" data-act="edEdit" data-id="${esc(f.key)}">Edit</button>` : ''}</td></tr>`))}</div>`).join('');
}

function edDetails(d) {
  const p = d.profile, dep = d.deployment, can = aiCan();
  const dis = can ? '' : 'disabled';
  return `<div class="panel"><div class="hd"><h3>Deployment</h3><small>Where Ledgerline runs for the firm: decides the SaaS report</small></div><div class="bd"><div class="form">
      <div class="fld"><label for="ed-dep">Ledgerline runs</label><select class="in" id="ed-dep" ${dis}>${[['hosted', 'Hosted by Ledgerline (SaaS)'], ['self', 'On the broker’s own server'], ['own_cloud', 'In the broker’s own cloud account']].map(([k, l]) => `<option value="${k}" ${dep.deployment === k ? 'selected' : ''}>${l}</option>`).join('')}</select></div>
      <div class="fld"><label for="ed-hp">Hosting (cloud) provider</label><input class="in" id="ed-hp" value="${esc(dep.provider)}" placeholder="e.g. Amazon Web Services" ${dis}></div>
      <div class="fld"><label for="ed-hr">Region</label><input class="in" id="ed-hr" value="${esc(dep.region)}" placeholder="e.g. AWS Mumbai (ap-south-1)" ${dis}></div>
      ${can ? '<div class="fld"><label>&nbsp;</label><button class="btn pri" data-act="edDeploy">Save deployment</button></div>' : ''}</div></div></div>
    <div class="panel"><div class="hd"><h3>Details for the forms</h3></div><div class="bd"><div class="form">
      <div class="fld"><label for="ed-cat">Category of intermediary</label><input class="in" id="ed-cat" value="${esc(p.category)}" ${dis}></div>
      <div class="fld"><label for="ed-cyc">AI &amp; ML report filed</label><select class="in" id="ed-cyc" ${dis}>${[['half-yearly', 'Half-yearly (members trading through algorithms)'], ['yearly', 'Yearly (other members)'], ['quarterly', 'Quarterly (as in the 2019 circular)']].map(([k, l]) => `<option value="${k}" ${p.aiml_cycle === k ? 'selected' : ''}>${l}</option>`).join('')}</select></div>
      <div class="fld"><label for="ed-isn">Information security contact</label><input class="in" id="ed-isn" value="${esc(p.infosec_name)}" ${dis}></div>
      <div class="fld"><label for="ed-isd">Designation</label><input class="in" id="ed-isd" value="${esc(p.infosec_designation)}" ${dis}></div>
      <div class="fld"><label for="ed-ise">Email</label><input class="in" id="ed-ise" value="${esc(p.infosec_email)}" ${dis}></div>
      <div class="fld"><label for="ed-isp">Phone</label><input class="in" id="ed-isp" value="${esc(p.infosec_phone)}" ${dis}></div>
      <div class="fld full"><label>Exchanges filed with (none ticked: every exchange the firm has a member code for${d.member_of.length ? ', now ' + esc(d.member_of.join(', ')) : ''})</label><div class="tools">${d.exchanges.map(x => `<label class="check"><input type="checkbox" data-edex="${x}" ${p.exchanges.includes(x) ? 'checked' : ''} ${dis}> ${x}</label>`).join('')}</div></div>
      <label class="check full"><input type="checkbox" id="ed-single" ${p.single_filing ? 'checked' : ''} ${dis}> File the AI &amp; ML report once, at NSE (NSE shares an NSE member’s filing with BSE, MCX and the other exchanges)</label>
      <label class="check"><input type="checkbox" id="ed-grc" ${p.saas_grc ? 'checked' : ''} ${dis}> The firm uses a SaaS-based GRC solution</label>
      <div class="fld"><label for="ed-grct">GRC solution</label><input class="in" id="ed-grct" value="${esc(p.grc_tool || '')}" ${dis}></div>
    </div></div>
    <div class="hd"><h3>Other SaaS services</h3><small>used outside Ledgerline (email, GRC, CRM…): each is a row of the SaaS report</small>${can ? '<button class="btn sm" data-act="edSaasAdd">Add</button>' : ''}</div>
    ${tbl([['Service'], ['Provider'], ['Nature'], ['Where'], ['']], p.other_saas.map((r, i) => `<tr><td>${esc(r.service)}</td><td>${esc(r.provider)}</td><td class="small">${esc(r.nature)}</td><td class="small">${esc(r.geo_location)}</td><td>${can ? `<button class="btn sm" data-act="edSaasDel" data-id="${i}">Remove</button>` : ''}</td></tr>`), { empty: 'None.' })}
    ${can ? '<div class="bd"><button class="btn pri" data-act="edProfile">Save details</button></div>' : ''}</div>`;
}

const edView = () => UI.edTab === 'saas' ? C.exdisc.saas : C.exdisc.aiml;
const edField = key => edView().fields.find(f => f.key === key);
async function edClip(text) {
  try { await navigator.clipboard.writeText(text); toast('Copied'); }
  catch (e) { const t = document.createElement('textarea'); t.value = text; document.body.appendChild(t); t.select(); try { document.execCommand('copy'); toast('Copied'); } catch (x) { toast('Copy failed: select the text and copy it', true); } t.remove(); }
}
function edProfileValue(over) {
  const p = { ...C.exdisc.profile };
  if ($('ed-cat')) Object.assign(p, { category: $('ed-cat').value.trim(), aiml_cycle: $('ed-cyc').value, infosec_name: $('ed-isn').value.trim(), infosec_designation: $('ed-isd').value.trim(), infosec_email: $('ed-ise').value.trim(), infosec_phone: $('ed-isp').value.trim(),
    exchanges: [...document.querySelectorAll('[data-edex]')].filter(x => x.checked).map(x => x.dataset.edex), single_filing: $('ed-single').checked, saas_grc: $('ed-grc').checked, grc_tool: $('ed-grct').value.trim() });
  return Object.assign(p, over || {});
}

Object.assign(A, {
  edCopy: key => { const f = edField(key); if (f) edClip(f.value); },
  edCopyAll: () => { const v = edView(); let sec = ''; edClip(v.fields.map(f => (f.section !== sec ? (sec = f.section, '\n' + f.section + '\n') : '') + f.label + '\n' + f.value).join('\n').trim()); },
  edEdit: key => {
    const f = edField(key), v = edView(); if (!f) return;
    const inp = f.kind === 'text' ? `<textarea class="in" id="ed-val" rows="6">${esc(f.value)}</textarea>` : `<select class="in" id="ed-val">${(f.kind === 'ynna' ? ['Y', 'N', 'NA'] : ['Y', 'N']).map(x => `<option ${f.value === x ? 'selected' : ''}>${x}</option>`).join('')}</select>`;
    modal(mhd('Edit the answer', esc(f.label)) + `<div class="bd"><div class="fld">${inp}</div><div class="dim small">Worked out by Ledgerline: ${esc(f.generated || '(blank)')}</div><div id="ed-err"></div></div>
      <div class="ft"><button class="btn" data-act="close">Cancel</button>${f.edited ? '<button class="btn" id="ed-reset">Back to Ledgerline’s answer</button>' : ''}<button class="btn pri" id="ed-save">Save</button></div>`);
    const save = async val => { try { await api('PUT', `/v1/disclosures/${v.kind}/${encodeURIComponent(v.period.id)}/answers`, { key, value: val }); closeModal(); toast('Answer saved'); render({ keep: true }); } catch (e) { $('ed-err').innerHTML = errHTML(e); } };
    $('ed-save').onclick = () => save($('ed-val').value);
    if ($('ed-reset')) $('ed-reset').onclick = () => save('');
  },
  edSubmit: exchange => {
    const v = edView();
    modal(mhd(`Mark submitted to ${esc(exchange)}`, `${esc(v.title)} · ${esc(v.period.label)}. The answers are kept as filed, and the calendar duty for ${esc(exchange)} is closed.`) + `<div class="bd"><div class="form">
      <div class="fld"><label for="eds-d">Submitted on</label><input class="in" type="date" id="eds-d" value="${esc(S.date > v.period.to ? S.date : v.period.due)}"></div>
      <div class="fld"><label for="eds-r">Reference or acknowledgement number</label><input class="in mono" id="eds-r"></div>
      <div class="fld full"><label for="eds-f">Screenshot or acknowledgement (PNG, JPEG or PDF, optional)</label><input class="in" type="file" id="eds-f" accept="image/png,image/jpeg,application/pdf"></div>
      <div class="fld full"><label for="eds-n">Note</label><input class="in" id="eds-n"></div></div><div id="eds-err"></div></div>
      <div class="ft"><button class="btn" data-act="close">Cancel</button><button class="btn pri" id="eds-go">Mark submitted</button></div>`);
    $('eds-go').onclick = async () => {
      $('eds-go').disabled = true;
      try {
        let file_id = '';
        const f = $('eds-f').files[0];
        if (f) file_id = (await api('POST', `/v1/disclosures/evidence?name=${encodeURIComponent(f.name)}`, f, true)).id;
        await post(`/v1/disclosures/${v.kind}/${encodeURIComponent(v.period.id)}/submit`, { exchange, date: $('eds-d').value, ref: $('eds-r').value, note: $('eds-n').value, file_id });
        closeModal(); toast(`Marked submitted to ${exchange}`); render({ keep: true });
      } catch (e) { $('eds-err').innerHTML = errHTML(e); $('eds-go').disabled = false; }
    };
  },
  edDeploy: () => act(() => api('PUT', '/v1/disclosures/deployment', { deployment: $('ed-dep').value, provider: $('ed-hp').value, region: $('ed-hr').value }), 'Deployment saved'),
  edProfile: () => act(() => api('PUT', '/v1/disclosures/profile', edProfileValue()), 'Details saved'),
  edSaasDel: i => act(() => api('PUT', '/v1/disclosures/profile', edProfileValue({ other_saas: C.exdisc.profile.other_saas.filter((_, j) => j !== +i) })), 'Removed'),
  edSaasAdd: () => confirmModal('Add a SaaS service', 'A cloud service the firm uses outside Ledgerline.', `<div class="form">${[['service', 'SaaS service'], ['provider', 'Cloud service provider'], ['nature', 'Nature of service'], ['environment', 'Environment and data exchanged'], ['geo_location', 'Geo-location of hosting'], ['gaps', 'Gaps against the circular'], ['deadline', 'Deadline to close the gaps']].map(([k, l]) => `<div class="fld"><label for="es-${k}">${l}</label><input class="in" id="es-${k}"></div>`).join('')}</div>`, 'Add', () => {
    const r = {}; ['service', 'provider', 'nature', 'environment', 'geo_location', 'gaps', 'deadline'].forEach(k => { r[k] = $('es-' + k).value.trim(); });
    return act(() => api('PUT', '/v1/disclosures/profile', edProfileValue({ other_saas: [...C.exdisc.profile.other_saas, r] })), 'Added');
  }),
});
document.addEventListener('change', e => {
  if (e.target.id !== 'ed-period') return;
  if (UI.edTab === 'saas') UI.edSaas = e.target.value; else UI.edAiml = e.target.value;
  render({ keep: true });
});
