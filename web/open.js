'use strict';
/* Online account opening: one step at a time, resumable from the same browser. */
const T = decodeURIComponent(location.pathname.split('/')[2] || '');
const Q = new URLSearchParams(location.search);
const $ = id => document.getElementById(id);
const esc = s => String(s ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const KEY = 'll-apply-' + T;
const store = { get() { try { return localStorage.getItem(KEY) || ''; } catch (e) { return ''; } }, set(v) { try { v ? localStorage.setItem(KEY, v) : localStorage.removeItem(KEY); } catch (e) {} } };
let CFG = null, APP = null, SECRET = store.get(), DEV = {}, ERR = null, BUSY = false, STEP = null;
const STEP_TITLES = { contact: 'Confirm your mobile and email', identity: 'Your PAN and identity', address: 'Aadhaar and address from DigiLocker', bank: 'Your bank account', profile: 'How you will trade', nominee: 'Nominee', photo: 'Photograph and signature', review: 'Review and sign' };

class ApiErr extends Error { constructor(b, st) { super(b?.error?.message || 'Something went wrong (' + st + ')'); this.fields = b?.error?.details?.fields || []; } }
async function call(method, path, body, raw) {
  const h = {};
  if (SECRET) h['X-Application'] = SECRET;
  if (body !== undefined && !raw) h['Content-Type'] = 'application/json';
  const r = await fetch(`/onboard/${encodeURIComponent(T)}${path}`, { method, headers: h, body: body === undefined ? undefined : raw ? body : JSON.stringify(body) });
  const b = await r.json().catch(() => null);
  if (!r.ok) throw new ApiErr(b, r.status);
  return b;
}
const fieldErr = f => { const x = (ERR?.fields || []).find(e => e.field === f); return x ? `<span class="err">${esc(x.message)}</span>` : ''; };
const fld = (id, label, val, attrs = '') => `<div class="fld"><label for="${id}">${label}</label><input class="in" id="${id}" value="${esc(val || '')}" ${attrs}>${fieldErr(id)}</div>`;
const sel = (id, label, opts, val) => `<div class="fld"><label for="${id}">${label}</label><select class="in" id="${id}"><option value="">Choose</option>${opts.map(o => `<option ${o === val ? 'selected' : ''}>${esc(o)}</option>`).join('')}</select>${fieldErr(id)}</div>`;
const checks = names => { const cs = (APP?.checks || []).filter(c => names.includes(c.name)); return cs.length ? `<div class="res">${cs.map(c => `<div><span class="pill ${c.status.split(' ')[0]}">${esc(c.status)}</span><span>${esc(c.name)}: ${esc(c.detail)}</span></div>`).join('')}</div>` : ''; };
const hasDoc = k => (APP?.docs || []).some(d => d.kind === k);
const errBox = () => ERR && !ERR.fields.length ? `<div class="note bad">${esc(ERR.message)}</div>` : ERR?.fields.length ? `<div class="note bad">Please correct the highlighted fields.</div>` : '';

function nextStep() { if (!APP) return 'start'; if (APP.status !== 'Draft') return 'done'; return CFG.steps.find(s => !(APP.steps || []).includes(s)) || 'review'; }
let SHOWN = '';
const keyOf = el => el.id || (el.dataset.seg && 'seg:' + el.dataset.seg) || (el.dataset.decl && 'decl:' + el.dataset.decl) || '';
function snapshot() { const m = {}; $('card').querySelectorAll('input, select, textarea').forEach(el => { const k = keyOf(el); if (!k || el.type === 'file') return; m[k] = el.type === 'checkbox' || el.type === 'radio' ? el.checked : el.value; }); return m; }
function restore(m) { $('card').querySelectorAll('input, select, textarea').forEach(el => { const k = keyOf(el); if (!(k in m)) return; if (el.type === 'checkbox' || el.type === 'radio') el.checked = m[k]; else if (el.value === '' || el.tagName === 'SELECT') el.value = m[k]; }); }
function render() {
  const step = STEP || nextStep();
  $('prog').innerHTML = CFG.steps.map(s => `<li class="${(APP?.steps || []).includes(s) ? 'done' : s === step ? 'on' : ''}" title="${esc(STEP_TITLES[s])}"></li>`).join('');
  const i = CFG.steps.indexOf(step);
  const head = i >= 0 ? `<small class="dim">Step ${i + 1} of ${CFG.steps.length}</small><h1>${esc(STEP_TITLES[step])}</h1>` : '';
  const back = i > 1 ? `<button class="btn" data-go="${CFG.steps[i - 1]}">Back</button>` : '<span></span>';
  // Keep what the applicant typed or ticked when the same step is redrawn
  // (after an upload, a failed check or asking for the signing code).
  const keep = step === SHOWN ? snapshot() : null;
  $('card').innerHTML = head + (V[step] || V.start)() + errBox();
  SHOWN = step;
  if (keep) restore(keep);
  if (i > 1) $('card').insertAdjacentHTML('beforeend', `<div class="acts">${back}<span></span></div>`);
  bind(step);
}
async function run(fn) { if (BUSY) return; BUSY = true; ERR = null; document.querySelectorAll('button').forEach(b => b.disabled = true); try { await fn(); STEP = null; } catch (e) { ERR = e; } BUSY = false; render(); }
async function upload(kind, blob, name) { APP = await call('POST', `/doc?kind=${encodeURIComponent(kind)}&name=${encodeURIComponent(name)}`, blob, true); }
const saveStep = (step, data) => call('POST', '/step', { step, ...data }).then(a => { APP = a; });

const V = {
  start: () => `<h1>Open a trading and demat account</h1><p class="dim">Online, in about ten minutes, with your PAN, Aadhaar-linked mobile, bank details and a phone camera. You can stop and continue later on this device.</p>
    ${fld('mobile', 'Mobile number', '', 'inputmode="tel" autocomplete="tel" maxlength="13" placeholder="10 digits"')}${fld('email', 'Email', '', 'type="email" autocomplete="email"')}
    <label class="check"><input type="checkbox" id="agree"> <span class="small">I agree that ${esc(CFG.firm)} may contact me on this mobile and email about my application.</span></label>
    <button class="btn pri" id="go">Send codes</button>`,
  contact: () => `<p class="dim">We sent a 6-digit code to ${esc(APP.mobile)} by SMS and another to ${esc(APP.email)}.</p>
    ${DEV.mobile ? `<div class="note warn small">Development server: mobile code ${esc(DEV.mobile)}, email code ${esc(DEV.email)}.</div>` : ''}
    <div class="row">${APP.mobile_ok ? '<div class="note ok">Mobile confirmed</div>' : fld('cm', 'Code sent to your mobile', '', 'inputmode="numeric" maxlength="6" autocomplete="one-time-code"')}${APP.email_ok ? '<div class="note ok">Email confirmed</div>' : fld('ce', 'Code sent to your email', '', 'inputmode="numeric" maxlength="6"')}</div>
    <div class="acts"><button class="btn" id="resend">Send new codes</button><button class="btn pri" id="go">Confirm</button></div>`,
  identity: () => `${fld('pan', 'PAN', APP.pan, 'maxlength="10" autocapitalize="characters" class="in mono"').replace('class="in"', 'class="in mono"')}${fld('name', 'Name exactly as on your PAN', APP.name)}
    <div class="row">${fld('dob', 'Date of birth', APP.dob, 'type="date"')}${sel('gender', 'Gender', ['Male', 'Female', 'Transgender'], APP.gender)}</div>
    <div class="row">${fld('father', "Father's or spouse's name", APP.father)}${sel('marital', 'Marital status', ['Single', 'Married', 'Other'], APP.marital)}</div>
    ${checks(['PAN', 'KRA search'])}<button class="btn pri" id="go">Check my PAN</button>`,
  address: () => `<p class="dim">Sign in to DigiLocker with your Aadhaar-linked mobile and allow ${esc(CFG.firm)} to read your Aadhaar and PAN. Your address is taken from Aadhaar, and no video call or office visit is needed.</p>${checks(['Aadhaar (DigiLocker)', 'PAN'])}
    ${Q.get('dl') === 'back' && !(APP.steps || []).includes('address') ? '<div class="note">Back from DigiLocker: reading your documents…</div>' : ''}
    <button class="btn pri" id="dl">${(APP.steps || []).includes('address') ? 'Fetch again from DigiLocker' : 'Continue to DigiLocker'}</button>
    ${(APP.steps || []).includes('address') ? `<dl class="kv"><dt>Address</dt><dd>${esc([APP.address, APP.city, APP.state, APP.pin].filter(Boolean).join(', '))}</dd></dl>` : ''}
    <p class="dim small m0">No DigiLocker? Create one at digilocker.gov.in with your Aadhaar in a few minutes, then come back here.</p>`,
  bank: () => `<p class="dim">Money is paid out only to this account, and only money from it is accepted. We check it by sending ₹1.</p>
    ${fld('bankname', 'Bank', APP.bank)}<div class="row">${fld('ifsc', 'IFSC', APP.ifsc, 'maxlength="11" autocapitalize="characters"')}${fld('account', 'Account number', APP.account, 'inputmode="numeric" maxlength="18"')}</div>
    ${checks(['Bank account'])}<button class="btn pri" id="go">Verify bank account</button>`,
  profile: () => { const segs = APP.segments || ['CM']; return `<div class="fld"><label>Segments</label>${Object.entries(CFG.segments).map(([k, v]) => `<label class="check"><input type="checkbox" data-seg="${k}" ${segs.includes(k) ? 'checked' : ''}> ${esc(v)}</label>`).join('')}${fieldErr('segments')}</div>
    <div class="row">${sel('income', 'Annual income', ['<1 L', '1-5 L', '5-10 L', '10-25 L', '25 L-1 Cr', '>1 Cr'], APP.income)}${sel('occupation', 'Occupation', ['Salaried', 'Self-employed', 'Business', 'Professional', 'Student', 'Retired', 'Homemaker', 'Other'], APP.occupation)}</div>
    <div class="row">${sel('experience', 'Trading experience', ['None', 'Under 1 year', '1–5 years', 'Over 5 years'], APP.experience)}${sel('ras', 'Settle unused funds to my bank', ['Monthly', 'Quarterly'], APP.ras || 'Quarterly')}</div>
    ${fld('demat_id', 'Existing demat account (leave blank to open one with us)', APP.demat_id, 'class="in mono" maxlength="16"')}
    <div class="fld"><label for="f-inc">Income proof (needed for futures, options, currency and commodities): salary slip, ITR or 6-month bank statement</label><input class="in" type="file" id="f-inc" accept="image/*,application/pdf">${hasDoc('Income proof') ? '<span class="small dim">Uploaded ✓</span>' : ''}${fieldErr('income_proof')}</div>
    <button class="btn pri" id="go">Continue</button>`; },
  nominee: () => `<p class="dim">A nominee receives your holdings and funds if something happens to you.</p>
    <label class="check"><input type="checkbox" id="optout" ${APP.nominee_opt_out ? 'checked' : ''}> <span>I do not wish to nominate anyone</span></label>
    <div class="row">${fld('nname', "Nominee's full name", APP.nominee_name)}${sel('nrel', 'Relationship', ['Spouse', 'Son', 'Daughter', 'Father', 'Mother', 'Brother', 'Sister', 'Other'], APP.nominee_relation)}</div>
    <button class="btn pri" id="go">Continue</button>`,
  photo: () => `<div class="media"><b>Your photograph</b><p class="dim small m0">Face the camera in good light, without glasses or a cap.</p><video id="cam" autoplay playsinline muted width="320" hidden></video><div class="acts start"><button class="btn" id="camon">Open camera</button><button class="btn" id="snap" hidden>Take photograph</button><label class="btn">Upload instead<input type="file" id="f-photo" accept="image/*" hidden></label></div>${hasDoc('Photograph') ? '<span class="small dim">Photograph saved ✓</span>' : ''}</div>
    <div class="media"><b>Your signature</b><canvas class="sig" id="sig" width="600" height="180"></canvas><div class="acts start"><button class="btn" id="sigclr">Clear</button><button class="btn" id="sigsave">Save signature</button></div>${hasDoc('Signature') ? '<span class="small dim">Signature saved ✓</span>' : ''}</div>
    ${hasDoc('PAN (DigiLocker)') ? '' : `<div class="fld"><label for="f-pan">Photo of your PAN card (your DigiLocker had no PAN record)</label><input class="in" type="file" id="f-pan" accept="image/*,application/pdf">${hasDoc('PAN card') ? '<span class="small dim">Uploaded ✓</span>' : ''}</div>`}
    ${checks(['Face match'])}<button class="btn pri" id="go">Continue</button>`,
  review: () => `<dl class="kv"><dt>Name</dt><dd>${esc(APP.name)}</dd><dt>PAN</dt><dd>${esc(APP.pan)}</dd><dt>Mobile</dt><dd>${esc(APP.mobile)}</dd><dt>Email</dt><dd>${esc(APP.email)}</dd><dt>Address</dt><dd>${esc([APP.address, APP.city, APP.state, APP.pin].join(', '))}</dd><dt>Bank</dt><dd>${esc(APP.bank)} · ${esc(APP.ifsc)}</dd><dt>Segments</dt><dd>${esc((APP.segments || []).map(s => CFG.segments[s]).join(', '))}</dd><dt>Nominee</dt><dd>${APP.nominee_opt_out ? 'None' : esc(APP.nominee_name + ' (' + APP.nominee_relation + ')')}</dd></dl>
    <a class="small" href="/onboard/${encodeURIComponent(T)}/form?s=${encodeURIComponent(SECRET)}" target="_blank" rel="noopener">Read the account opening form you are signing</a>
    ${Object.entries(CFG.declarations).map(([k, v]) => `<label class="check"><input type="checkbox" data-decl="${k}"> <span>${esc(v)}</span></label>`).join('')}
    <label class="check"><input type="checkbox" id="wa"> <span>Send me account messages on WhatsApp too (optional; reply STOP any time)</span></label>
    ${DEV.esign ? `<div class="note warn small">Development server: signing code ${esc(DEV.esign)}.</div>` : ''}
    ${DEV.esign !== undefined ? `${fld('esc', 'Code sent to your mobile to sign', '', 'inputmode="numeric" maxlength="6"')}<button class="btn pri" id="sign">Sign and submit</button>` : '<button class="btn pri" id="go">Sign with a code to my mobile</button>'}`,
  done: () => APP.status === 'Rejected' ? `<h1>We could not open your account</h1><p>${esc(APP.reason)}</p><button class="btn" id="restart">Start a new application</button>` :
    APP.activated ? `<h1>Your account is open</h1><p>Your client code is <b>${esc(APP.ucc)}</b>. Your Aadhaar, PAN and bank account were verified, so nothing more is needed from you.</p><p class="dim">Your details are being registered with the exchanges; you can trade once that is done, usually by the next working day. We have emailed ${esc(APP.email)}.</p>` :
    `<h1>Application received</h1><p>Thank you. Your application number is <b>${esc(APP.id)}</b>${APP.ucc ? ` and your client code will be <b>${esc(APP.ucc)}</b>` : ''}.</p><p class="dim">We are checking your details; this usually takes one working day. We will email ${esc(APP.email)} when your account is active.</p>${checks((APP.checks || []).map(c => c.name))}`,
};

let stream = null;
async function camera(audio) { if (stream) stream.getTracks().forEach(t => t.stop()); stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'user', width: 640 }, audio }); const v = $('cam'); v.srcObject = stream; v.hidden = false; await v.play().catch(() => {}); return v; }
function stopCam() { if (stream) { stream.getTracks().forEach(t => t.stop()); stream = null; } }
function fileUp(id, kind) { const f = $(id); if (f) f.onchange = () => f.files[0] && run(() => upload(kind, f.files[0], f.files[0].name)); }

function bind(step) {
  document.querySelectorAll('[data-go]').forEach(b => b.onclick = () => { STEP = b.dataset.go; ERR = null; stopCam(); render(); });
  const go = $('go'), v = id => ($(id)?.value || '').trim();
  if (step === 'start') go.onclick = () => { if (!$('agree').checked) { ERR = new ApiErr({ error: { message: 'Please agree to be contacted about your application.' } }); render(); return; }
    run(async () => { const r = await call('POST', '/start', { mobile: v('mobile'), email: v('email'), partner: Q.get('ap') || '', lead: Q.get('lead') || '' }); SECRET = r.secret; store.set(SECRET); APP = r.application; DEV = r.dev_codes || {}; }); };
  if (step === 'contact') { $('resend').onclick = () => run(async () => { APP = await call('POST', '/verify', { channel: 'resend' }); });
    go.onclick = () => run(async () => { if (!APP.mobile_ok) APP = await call('POST', '/verify', { channel: 'mobile', code: v('cm') }); if (!APP.email_ok) APP = await call('POST', '/verify', { channel: 'email', code: v('ce') }); }); }
  if (step === 'identity') go.onclick = () => run(() => saveStep('identity', { pan: v('pan'), name: v('name'), dob: v('dob'), gender: v('gender'), father: v('father'), marital: v('marital') }));
  if (step === 'address') $('dl').onclick = () => run(async () => {
    if (!CFG.digilocker_redirect) return saveStep('address', { digilocker: true });
    const r = await call('POST', '/digilocker', {}); location.href = r.url; await new Promise(() => {}); // leaving the page
  });
  if (step === 'bank') go.onclick = () => run(() => saveStep('bank', { bank: v('bankname'), ifsc: v('ifsc'), account: v('account') }));
  if (step === 'profile') { fileUp('f-inc', 'Income proof');
    go.onclick = () => run(() => saveStep('profile', { segments: [...document.querySelectorAll('[data-seg]:checked')].map(x => x.dataset.seg), income: v('income'), occupation: v('occupation'), experience: v('experience'), ras: v('ras'), demat_id: v('demat_id') })); }
  if (step === 'nominee') go.onclick = () => run(() => saveStep('nominee', { nominee_opt_out: $('optout').checked, nominee_name: v('nname'), nominee_relation: v('nrel') }));
  if (step === 'photo') {
    fileUp('f-photo', 'Photograph'); fileUp('f-pan', 'PAN card');
    $('camon').onclick = async () => { try { await camera(false); $('snap').hidden = false; } catch (e) { ERR = new ApiErr({ error: { message: 'The camera could not be opened; upload a photograph instead.' } }); render(); } };
    $('snap').onclick = () => { const vd = $('cam'), c = document.createElement('canvas'); c.width = vd.videoWidth; c.height = vd.videoHeight; c.getContext('2d').drawImage(vd, 0, 0); stopCam(); c.toBlob(b => run(() => upload('Photograph', b, 'photograph.jpg')), 'image/jpeg', 0.85); };
    const cv = $('sig'), g = cv.getContext('2d'); let drawing = false, drawn = false;
    g.lineWidth = 3; g.lineCap = 'round'; g.strokeStyle = '#16201B';
    const pt = e => { const r = cv.getBoundingClientRect(); return [(e.clientX - r.left) * cv.width / r.width, (e.clientY - r.top) * cv.height / r.height]; };
    cv.onpointerdown = e => { drawing = drawn = true; cv.setPointerCapture(e.pointerId); g.beginPath(); g.moveTo(...pt(e)); };
    cv.onpointermove = e => { if (drawing) { g.lineTo(...pt(e)); g.stroke(); } };
    cv.onpointerup = () => { drawing = false; };
    $('sigclr').onclick = () => { g.clearRect(0, 0, cv.width, cv.height); drawn = false; };
    $('sigsave').onclick = () => { if (!drawn) return; cv.toBlob(b => run(() => upload('Signature', b, 'signature.png')), 'image/png'); };
    go.onclick = () => { stopCam(); run(() => saveStep('photo', {})); };
  }
  if (step === 'review') {
    const decl = () => Object.fromEntries([...document.querySelectorAll('[data-decl]')].map(x => [x.dataset.decl, x.checked]));
    if (go) go.onclick = () => run(async () => { const r = await call('POST', '/sign', { declarations: decl(), whatsapp: $('wa').checked, code: '' }); APP = r.application; DEV.esign = (r.dev_codes || {}).esign || ''; STEP = 'review'; });
    if ($('sign')) $('sign').onclick = () => run(async () => { const r = await call('POST', '/sign', { declarations: decl(), whatsapp: $('wa').checked, code: v('esc') }); APP = r.application; });
  }
  if (step === 'done' && $('restart')) $('restart').onclick = () => { store.set(''); SECRET = ''; APP = null; render(); };
}

(async () => {
  try {
    CFG = await call('GET', '/config');
    document.title = 'Open an account · ' + CFG.firm;
    $('firm').textContent = CFG.firm; $('reg').textContent = 'SEBI registration ' + CFG.sebi_reg;
    $('foot').textContent = (CFG.provider === 'Not connected' ? 'Your details are checked by our staff. ' : '') + 'Your information is used only to open and service your account.';
    if (SECRET) { try { APP = await call('GET', '/state'); if (APP.status === 'Draft' && !APP.mobile_ok) DEV = {}; } catch (e) { SECRET = ''; store.set(''); } }
    // Back from DigiLocker: read what was shared, then carry on.
    if (APP && Q.get('dl') === 'back' && APP.status === 'Draft' && !(APP.steps || []).includes('address')) {
      history.replaceState(null, '', location.pathname); render();
      await run(() => saveStep('address', { digilocker: true }));
      return;
    }
    render();
  } catch (e) { $('card').innerHTML = `<p>${esc(e.message)}</p>`; }
})();
