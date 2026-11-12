// Ledgerline migration tool page. Talks only to the tool on 127.0.0.1;
// every call carries the tool's key. No data is ever sent anywhere else.
'use strict';

const TOKEN = document.querySelector('meta[name="ll-token"]').content;
const $ = (id) => document.getElementById(id);
const CHUNK = 8 << 20;

let S = null;          // last state from the tool
let screen = '';
let uploading = false;
const queue = [];      // files waiting to upload
const sent = {};       // id -> bytes sent (for progress before the tool's state catches up)

// ---- helpers ----

function el(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === undefined || v === null || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else if (v === true) e.setAttribute(k, '');
    else e.setAttribute(k, v);
  }
  for (const c of kids.flat()) {
    if (c === undefined || c === null || c === false) continue;
    e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return e;
}

function fill(id, ...rows) {
  const t = $(id);
  t.replaceChildren(...rows.flat());
}

function show(id, on) { $(id).classList.toggle('hidden', !on); }

// Indian digit grouping: 12,34,567 and -1,23,456.78
function indian(v) {
  const s = String(v);
  const m = /^([-+]?)(\d+)(\.\d+)?$/.exec(s.trim());
  if (!m) return s;
  let i = m[2];
  if (i.length > 3) {
    const head = i.slice(0, -3).replace(/\B(?=(\d{2})+(?!\d))/g, ',');
    i = head + ',' + i.slice(-3);
  }
  return (m[1] === '-' ? '-' : '') + i + (m[3] || '');
}

function size(n) {
  if (n === undefined || n === null || n < 0) return '';
  if (n >= 1 << 30) return (n / (1 << 30)).toFixed(1) + ' GB';
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB';
  if (n >= 1 << 10) return Math.floor(n / 1024) + ' KB';
  return n + ' bytes';
}

async function call(path, body) {
  const r = await fetch(path, {
    method: 'POST',
    headers: { 'X-LL-Token': TOKEN, 'Content-Type': 'application/json' },
    body: JSON.stringify(body || {}),
  });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || ('The tool answered ' + r.status));
  return j;
}

function act(fn) {
  return async (ev) => {
    const b = ev && ev.currentTarget;
    if (b) b.disabled = true;
    try { await fn(ev); } catch (e) { alert(e.message); } finally { if (b) b.disabled = false; }
    poll();
  };
}

// ---- state ----

async function poll() {
  try {
    const r = await fetch('/api/state', { headers: { 'X-LL-Token': TOKEN } });
    if (!r.ok) throw new Error('state ' + r.status);
    S = await r.json();
    show('gone', false);
    render();
  } catch (e) {
    show('gone', true);
  }
}

function render() {
  if (screen !== S.screen) {
    const first = screen === '';
    screen = S.screen;
    for (const s of ['drop', 'found', 'choose', 'working', 'done', 'bye']) show('s-' + s, s === screen);
    if (screen === 'choose') enterChoose(first);
    window.scrollTo(0, 0);
  }
  const order = ['drop', 'found', 'choose', 'working', 'done'];
  document.querySelectorAll('#steps span').forEach((sp) => {
    const i = order.indexOf(sp.dataset.s), c = order.indexOf(screen);
    sp.className = i === c ? 'on' : (i < c ? 'past' : '');
  });
  ({ drop: renderDrop, found: renderFound, choose: renderChoose, working: renderWorking, done: renderDone })[screen]?.();
}

// ---- a) drop ----

function renderServer() {
  const v = S.server;
  const box = $('server');
  box.className = 'card';
  $('srv-msg').textContent = v.message || '';
  $('srv-msg').className = v.status === 'ready' ? '' : 'dim';
  fill('srv-warn', (v.warnings || []).map((w) => el('p', { class: 'note warn small' }, w)));
  show('srv-ready', v.status === 'ready');
  show('srv-login', v.status === 'login' || box.dataset.login === '1');
  show('srv-none', v.status === 'none');
  show('dk-check', v.status === 'none' && v.can_docker);
  show('srv-docker', v.status === 'docker');
  show('dk-start', v.status === 'docker' && v.docker_ok);
  if (v.status === 'ready') box.dataset.login = '';
  // no SQL Server on a Mac or Linux: check Docker straight away, once
  if (v.status === 'none' && v.can_docker && !box.dataset.dk) {
    box.dataset.dk = '1';
    call('/api/docker/check').catch(() => {});
  }
  const sel = $('srv-ep');
  const want = (v.endpoints || []).map((e) => label(e)).join('|');
  if (sel.dataset.k !== want) {
    sel.dataset.k = want;
    fill('srv-ep', (v.endpoints || []).map((e, i) => el('option', { value: i }, label(e))), el('option', { value: -1 }, 'Other (type it)'));
  }
}

function label(e) {
  let s = e.host + (e.instance ? '\\' + e.instance : '');
  if (e.port) s += ',' + e.port;
  return s;
}

function renderDrop() {
  renderServer();
  const free = S.work_free;
  $('free').textContent = free >= 0 ? 'Free space for the work files: ' + size(free) + ' (' + S.work + ')' : '';
  const files = S.files || [];
  show('files-card', files.length > 0);
  fill('files', files.map((f) => {
    let st;
    const got = Math.max(f.received, sent[f.id] || 0);
    switch (f.status) {
      case 'uploading': case 'receiving': st = el('span', {}, el('span', { class: 'pill dim' }, 'Receiving ' + Math.floor(100 * got / Math.max(f.size, 1)) + '%')); break;
      case 'ready': st = el('span', { class: 'pill dim' }, 'Ready'); break;
      case 'unpacked': st = el('span', { class: 'pill good' }, 'Unpacked'); break;
      case 'password': st = el('span', { class: 'pill warn' }, f.wrong ? 'Password not right' : 'Needs its password'); break;
      default: st = el('span', {}, el('span', { class: 'pill bad' }, 'Not used'), ' ', el('span', { class: 'dim small' }, f.reason || ''));
    }
    return el('tr', {}, el('td', {}, f.name), el('td', { class: 'n' }, size(f.size)), el('td', {}, st),
      el('td', {}, el('button', { class: 'btn sm', onclick: act(() => call('/api/files/remove', { id: f.id })) }, 'Remove')));
  }));
  // passwords
  const pw = S.passwords || [];
  const box = $('pw-box');
  const k = pw.map((p) => p.id + p.wrong).join(',');
  if (box.dataset.k !== k) {
    box.dataset.k = k;
    fill('pw-box', pw.map((p) => {
      const inp = el('input', { type: 'password', autocomplete: 'off', placeholder: 'Password' });
      return el('div', { class: 'note warn', 'data-mt': true },
        el('p', {}, el('b', {}, p.name), p.wrong ? ': that password is not right. Try again.' : ' is protected by a password (set in SQLBackupAndFTP or 7-Zip). Enter it to unpack the backup.'),
        el('div', { class: 'row' }, inp,
          el('button', { class: 'btn pri', onclick: act(() => call('/api/password', { id: p.id, password: inp.value })) }, 'Unpack'),
          el('button', { class: 'btn', onclick: act(() => call('/api/password', { id: p.id, password: '' })) }, 'Leave it out')));
    }));
  }
  const j = S.job && S.job.kind === 'scan' ? S.job : null;
  const running = !!(j && j.running);
  show('scan-box', running);
  if (running) {
    $('scan-step').textContent = j.step || 'Working…';
    bar('scan-bar', j.done, j.total);
  }
  show('scan-err', !!(j && !j.running && j.error));
  if (j && j.error) $('scan-err').textContent = j.error;
  show('scan-cancel', running);
  const busy = files.some((f) => f.status === 'uploading' || f.status === 'receiving') || uploading;
  const ready = S.server.status === 'ready';
  $('scan').disabled = running || busy || !ready || !files.some((f) => f.status === 'ready' || f.status === 'unpacked');
  $('scan-why').textContent = !ready ? 'SQL Server is needed first (see above).' : busy ? 'Waiting for every file to be received…' : '';
  // live databases
  const live = S.live || [];
  show('live', live.length > 0);
  const lk = live.map((d) => d.name).join('|');
  if ($('live-rows').dataset.k !== lk) {
    $('live-rows').dataset.k = lk;
    fill('live-rows', live.map((d) => el('tr', {},
      el('td', {}, el('input', { type: 'checkbox', value: d.name, checked: d.use || /^(NC|NCMAIL)\d{4}$|^shadmin$|^SSMAIL$/i.test(d.name) })),
      el('td', {}, d.name), el('td', { class: 'n' }, size(d.bytes)))));
  }
}

function bar(id, done, total) {
  const b = $(id);
  const known = total > 0;
  b.classList.toggle('busy', !known);
  b.firstElementChild.style.width = known ? Math.min(100, 100 * done / total) + '%' : '';
}

// gathering dropped files and folders

const WANT = /\.(bak|7z|zip)$|\.7z\.\d{3}$/i;
const NOTE = /\.(rar|mdf|ldf|ndf)$/i;

async function readEntry(entry, out) {
  if (entry.isFile) {
    const file = await new Promise((res, rej) => entry.file(res, rej));
    out.push({ file, name: entry.fullPath.replace(/^\/+/, ''), inFolder: entry.fullPath.split('/').length > 2 });
  } else if (entry.isDirectory) {
    const reader = entry.createReader();
    for (;;) {
      const batch = await new Promise((res, rej) => reader.readEntries(res, rej));
      if (!batch.length) break;
      for (const e of batch) if (!e.name.startsWith('.')) await readEntry(e, out);
    }
  }
}

function rid() {
  const a = new Uint8Array(9);
  crypto.getRandomValues(a);
  return Array.from(a, (b) => b.toString(16).padStart(2, '0')).join('');
}

async function addFiles(list) {
  const batch = rid();
  for (const it of list) {
    if (WANT.test(it.file.name)) {
      queue.push({ batch, id: rid(), file: it.file, name: it.name });
    } else if (NOTE.test(it.file.name) || !it.inFolder) {
      await call('/api/skipped', { name: it.name }).catch(() => {});
    }
  }
  pump();
  poll();
}

async function pump() {
  if (uploading) return;
  uploading = true;
  try {
    while (queue.length) {
      const q = queue[0];
      let off = 0;
      while (off < q.file.size) {
        const chunk = q.file.slice(off, Math.min(off + CHUNK, q.file.size));
        const url = '/api/upload?' + new URLSearchParams({ batch: q.batch, id: q.id, name: q.name, offset: off, size: q.file.size });
        const r = await fetch(url, { method: 'POST', headers: { 'X-LL-Token': TOKEN, 'Content-Type': 'application/octet-stream' }, body: chunk });
        const j = await r.json().catch(() => ({}));
        if (r.status === 409 && j.value) { off = j.value.received; continue; }
        if (!r.ok) { alert(q.name + ': ' + (j.error || 'not received')); break; }
        off = j.received;
        sent[q.id] = off;
        renderDrop();
      }
      queue.shift();
    }
  } finally {
    uploading = false;
    poll();
  }
}

function setupDrop() {
  const z = $('dropzone');
  ['dragenter', 'dragover'].forEach((t) => z.addEventListener(t, (e) => { e.preventDefault(); z.classList.add('over'); }));
  ['dragleave', 'drop'].forEach((t) => z.addEventListener(t, () => z.classList.remove('over')));
  // a file dropped outside the box must not make the browser open it
  window.addEventListener('dragover', (e) => e.preventDefault());
  window.addEventListener('drop', (e) => e.preventDefault());
  z.addEventListener('drop', async (e) => {
    e.preventDefault();
    const out = [];
    const items = Array.from(e.dataTransfer.items || []);
    const entries = items.map((i) => i.webkitGetAsEntry && i.webkitGetAsEntry()).filter(Boolean);
    if (entries.length) {
      for (const en of entries) await readEntry(en, out);
    } else {
      for (const f of e.dataTransfer.files) out.push({ file: f, name: f.name, inFolder: false });
    }
    addFiles(out);
  });
  $('browse').onclick = () => $('pick').click();
  $('browse-dir').onclick = () => $('pick-dir').click();
  $('pick').onchange = (e) => { addFiles(Array.from(e.target.files, (f) => ({ file: f, name: f.name, inFolder: false }))); e.target.value = ''; };
  $('pick-dir').onchange = (e) => { addFiles(Array.from(e.target.files, (f) => ({ file: f, name: f.webkitRelativePath || f.name, inFolder: true }))); e.target.value = ''; };

  $('scan').onclick = act(() => call('/api/scan'));
  $('scan-cancel').onclick = act(() => call('/api/cancel'));
  $('srv-retry').onclick = $('srv-retry2').onclick = act(() => { $('server').dataset.login = ''; return call('/api/server/retry'); });
  $('srv-showlogin').onclick = () => { $('server').dataset.login = '1'; renderServer(); };
  $('srv-change').onclick = () => { $('server').dataset.login = '1'; renderServer(); };
  $('srv-connect').onclick = act(async () => {
    await call('/api/server/login', { endpoint: Number($('srv-ep').value), instance: $('srv-inst').value.trim(), user: $('srv-user').value, password: $('srv-pass').value });
    $('srv-pass').value = '';
  });
  $('dk-check').onclick = $('dk-retry').onclick = act(() => call('/api/docker/check'));
  $('dk-start').onclick = act(() => call('/api/docker/start', { developer: $('dk-dev').checked }));
  $('live-list').onclick = act(() => call('/api/live/list'));
  $('live-use').onclick = act(() => call('/api/live/use', { names: Array.from(document.querySelectorAll('#live-rows input:checked'), (i) => i.value) }));
}

// ---- b) what we found ----

function renderFound() {
  fill('found-warn', (S.warnings || []).map((w) => el('p', { class: 'note warn' }, w)));
  fill('found', (S.found || []).map((f) => el('tr', { class: f.use ? '' : 'skip' },
    el('td', {}, f.file || (f.live ? 'On this server' : '')),
    el('td', {}, f.database),
    el('td', {}, f.note),
    el('td', {}, f.fy),
    el('td', {}, f.date),
    el('td', { class: 'n' }, size(f.bytes)),
    el('td', {}, f.use ? el('span', { class: 'pill good' }, 'Use') : el('span', {}, el('span', { class: f.problem ? 'pill bad' : 'pill dim' }, 'Skip'), el('div', { class: 'small' }, f.why || ''))),
    el('td', {}, f.use ? el('button', { class: 'btn sm', onclick: act(() => call('/api/toggle', { index: f.index, skip: true })) }, 'Skip')
      : f.user_skip ? el('button', { class: 'btn sm', onclick: act(() => call('/api/toggle', { index: f.index, skip: false })) }, 'Use') : ''))));
  $('found-next').disabled = !(S.found || []).some((f) => f.use && f.role === 'company');
}

// ---- c) choose ----

function enterChoose() {
  const firms = S.firms || [];
  fill('firms', firms.length ? firms.map((f) => el('label', { class: 'check' }, el('input', { type: 'checkbox', value: f.code, checked: true }), el('span', {}, el('b', {}, f.code), ' ', f.name)))
    : el('p', { class: 'dim' }, 'Every firm in the newest company database.'));
  $('years').value = String(S.years || 8);
  if (!$('out').value) $('out').value = S.out || '';
  checkOut();
}

function renderChoose() {}

let outTimer = 0;
function checkOut() {
  clearTimeout(outTimer);
  outTimer = setTimeout(async () => {
    try {
      const r = await call('/api/check-folder', { out: $('out').value.trim() });
      $('out-info').textContent = r.ok ? 'Free space there: ' + size(r.free) : r.msg;
      $('out-info').className = r.ok ? 'dim small' : 'small strength weak';
    } catch (e) { $('out-info').textContent = e.message; }
  }, 300);
}

function strength(p) {
  if (!p) return ['', ''];
  if (p.length < 12) return ['weak', 'Too short: at least 12 characters (' + p.length + ' now).'];
  let classes = 0;
  for (const re of [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/]) if (re.test(p)) classes++;
  const words = p.trim().split(/\s+/).length;
  const score = (p.length >= 16) + (p.length >= 20) + (classes >= 3) + (words >= 3);
  if (score >= 2) return ['good', 'Strong.'];
  if (score === 1) return ['fair', 'Fair. Longer is better: add another word.'];
  return ['fair', 'Acceptable, but easy to guess. Add a word or two.'];
}

function setupChoose() {
  $('out').addEventListener('input', checkOut);
  const upd = () => {
    const [c, t] = strength($('pp1').value);
    $('pp-strength').className = 'strength ' + c;
    $('pp-strength').textContent = t;
  };
  $('pp1').addEventListener('input', upd);
  $('pp-show').onchange = (e) => { for (const id of ['pp1', 'pp2']) $(id).type = e.target.checked ? 'text' : 'password'; };
  $('years').onchange = act(() => call('/api/years', { years: Number($('years').value) }));
  $('choose-back').onclick = act(() => call('/api/go', { screen: 'found' }));
  $('start').onclick = act(async () => {
    show('choose-err', false);
    const firms = Array.from(document.querySelectorAll('#firms input:checked'), (i) => i.value);
    const all = document.querySelectorAll('#firms input').length;
    if (all && !firms.length) throw new Error('Tick at least one firm.');
    const body = {
      firms: firms.length === all ? [] : firms,
      years: Number($('years').value),
      documents: $('docs').checked,
      dormant: document.querySelector('input[name=dormant]:checked').value,
      out: $('out').value.trim(),
      passphrase: $('pp1').value,
      passphrase2: $('pp2').value,
      max_part_gb: Number($('maxpart').value) || 4,
      keep: $('keep').checked,
    };
    try {
      await call('/api/start', body);
      $('pp1').value = $('pp2').value = '';
      $('pp-strength').textContent = '';
    } catch (e) {
      $('choose-err').textContent = e.message;
      show('choose-err', true);
    }
  });
}

// ---- d) working ----

function renderWorking() {
  const j = S.job || {};
  $('w-step').textContent = j.running ? (j.step || 'Working…') : (j.error ? 'Stopped' : 'Finishing…');
  bar('w-bar', j.done, j.total);
  $('w-count').textContent = j.total ? indian(j.done) + ' of ' + indian(j.total) : (j.done ? indian(j.done) : '');
  const steps = j.steps || [];
  fill('w-steps', steps.map((s, i) => el('li', { class: i === steps.length - 1 && j.running ? 'cur' : '' }, s)));
  const log = $('w-log');
  const atEnd = log.scrollTop + log.clientHeight >= log.scrollHeight - 4;
  fill('w-log', (j.log || []).slice(-200).map((l) => el('div', { class: l.severity }, l.at + '  ' + l.msg)));
  if (atEnd) log.scrollTop = log.scrollHeight;
  show('w-err', !j.running && !!j.error);
  $('w-err').textContent = j.error || '';
  show('w-cancel', !!j.running);
  show('w-back', !j.running && !!j.error);
  show('w-restart', !j.running && !!j.error);
}

// ---- e) done ----

function renderDone() {
  const r = S.result;
  if (!r) return;
  const k = r.report;
  if ($('d-parts').dataset.k === k) return;
  $('d-parts').dataset.k = k;
  fill('d-parts', r.parts.map((p) => el('tr', {}, el('td', { class: 'mono' }, p.path), el('td', {}, p.note || 'Everything'), el('td', { class: 'n' }, indian(p.docs)), el('td', { class: 'n' }, size(p.bytes)))));
  $('d-report').textContent = 'Report: ' + r.report;
  const groups = r.groups || [];
  fill('d-issues', groups.length ? groups.map((g) => [el('h2', { 'data-mt': true }, g.Area || 'General'),
    el('table', {}, el('thead', {}, el('tr', {}, el('th', {}, 'Severity'), el('th', {}, 'Firm'), el('th', {}, 'Year'), el('th', {}, 'Item'), el('th', {}, 'What'), el('th', {}, 'What to do'))),
      el('tbody', {}, g.Issues.map((i) => el('tr', {},
        el('td', {}, el('span', { class: 'pill ' + ({ Error: 'bad', Warning: 'warn' }[i.severity] || 'dim') }, i.severity)),
        el('td', {}, i.firm || ''), el('td', {}, i.fy || ''), el('td', {}, i.key || ''), el('td', {}, i.message), el('td', {}, i.action || '')))))])
    : el('p', { class: 'dim' }, 'No issues.'));
  fill('d-checks', (r.checks || []).map((c) => el('tr', {}, el('td', {}, c.firm || ''), el('td', {}, c.fy || ''), el('td', {}, c.label), el('td', { class: 'n' }, indian(c.value)))));
  fill('d-dropped', (r.dropped || []).length ? r.dropped.map((d) => el('tr', {}, el('td', {}, d.firm || ''), el('td', {}, d.field), el('td', {}, d.reason), el('td', { class: 'n' }, indian(d.count))))
    : el('tr', {}, el('td', { colspan: 4, class: 'dim' }, 'None.')));
  fill('d-docs', (r.docs || []).length ? r.docs.map((d) => el('tr', {}, el('td', {}, d.firm), el('td', {}, d.fy), el('td', {}, d.type), el('td', { class: 'n' }, indian(d.count))))
    : el('tr', {}, el('td', { colspan: 4, class: 'dim' }, 'None in this package.')));
}

function setupRest() {
  $('found-back').onclick = act(() => { if (confirm('Go back to the files? The restored copies are removed and read again when you continue.')) return call('/api/go', { screen: 'drop' }); });
  $('found-next').onclick = act(() => call('/api/go', { screen: 'choose' }));
  $('w-cancel').onclick = act(() => { if (confirm('Stop making the package? Nothing is saved.')) return call('/api/cancel'); });
  $('w-back').onclick = act(() => call('/api/go', { screen: 'choose' }));
  $('w-restart').onclick = act(() => call('/api/go', { screen: 'drop' }));
  $('d-open').onclick = act(() => call('/api/open'));
  $('d-quit').onclick = act(async () => {
    await call('/api/quit');
    clearInterval(timer);
    for (const s of ['drop', 'found', 'choose', 'working', 'done']) show('s-' + s, false);
    show('s-bye', true);
  });
}

setupDrop();
setupChoose();
setupRest();
poll();
const timer = setInterval(poll, 800);
