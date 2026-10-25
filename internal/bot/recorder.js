// Ledgerline bot recorder: runs in pages of the bot's own browser while a
// person records a workflow. It reports what they click, type, choose and
// tick, describing each element several ways so replays survive small page
// changes. What is typed into password boxes is never read: the step is
// sent as a placeholder for a saved password.
(() => {
  if (window.__llRecOn) return;
  window.__llRecOn = true;
  const send = o => { try { window.__llrec(JSON.stringify(o)); } catch (e) {} };
  const clean = s => String(s || '').replace(/\s+/g, ' ').trim();
  const short = s => clean(s).slice(0, 80);
  const uniq = sel => { try { return document.querySelectorAll(sel).length === 1; } catch (e) { return false; } };
  // Generated ids and classes change between visits; don't rely on them.
  const stable = v => !!v && v.length < 80 && !/\d{5,}|[0-9a-f]{10,}|^(ember|react|ng-|mui-|:r)|__\w+_\w{5}/i.test(v);
  const q = v => '"' + String(v).replace(/\\/g, '\\\\').replace(/"/g, '\\"') + '"';
  function cssFor(el) {
    if (el.id && stable(el.id) && uniq('#' + CSS.escape(el.id))) return '#' + CSS.escape(el.id);
    const tag = el.tagName.toLowerCase();
    for (const a of ['data-testid', 'data-test', 'data-qa', 'data-act', 'name', 'aria-label', 'placeholder', 'title', 'for', 'value']) {
      const v = el.getAttribute(a);
      if (v && stable(v)) {
        const s = tag + '[' + a + '=' + q(v) + ']';
        if (uniq(s)) return s;
        if (el.getAttribute('data-id')) { const s2 = s + '[data-id=' + q(el.getAttribute('data-id')) + ']'; if (uniq(s2)) return s2; }
      }
    }
    const parts = [];
    let e = el;
    while (e && e.nodeType === 1 && e !== document.documentElement && parts.length < 7) {
      if (e !== el && e.id && stable(e.id)) { parts.unshift('#' + CSS.escape(e.id)); break; }
      let p = e.tagName.toLowerCase();
      const sib = e.parentElement ? [...e.parentElement.children].filter(x => x.tagName === e.tagName) : [];
      if (sib.length > 1) p += ':nth-of-type(' + (sib.indexOf(e) + 1) + ')';
      parts.unshift(p);
      e = e.parentElement;
    }
    return parts.join(' > ');
  }
  function labelOf(el) {
    if (el.labels && el.labels[0]) return short(el.labels[0].innerText);
    const lb = el.getAttribute('aria-labelledby');
    if (lb) { const l = document.getElementById(lb.split(' ')[0]); if (l) return short(l.innerText); }
    return '';
  }
  const isBox = el => el.tagName === 'TEXTAREA' || (el.tagName === 'INPUT' && !['checkbox', 'radio', 'submit', 'button', 'reset', 'file', 'image', 'hidden'].includes((el.type || 'text').toLowerCase()));
  function target(el) {
    const box = isBox(el) || el.tagName === 'SELECT';
    return { css: cssFor(el), text: box ? '' : short(el.innerText || el.value || ''), role: el.getAttribute('role') || '', name: el.getAttribute('aria-label') || el.getAttribute('name') || '',
      tag: el.tagName.toLowerCase(), label: labelOf(el), placeholder: el.getAttribute('placeholder') || '', type: (el.getAttribute('type') || '').toLowerCase() };
  }
  const clickable = el => (el.closest && el.closest('a,button,[role=button],[role=link],[role=tab],[role=menuitem],[role=option],[role=checkbox],input,select,textarea,label,summary,[onclick],[data-act]')) || el;
  const hay = el => [el.name, el.id, el.autocomplete, el.placeholder, labelOf(el), el.getAttribute('aria-label')].join(' ');
  const isPw = el => (el.type || '').toLowerCase() === 'password';
  const isOtp = el => /one-time-code/.test(el.autocomplete || '') || /\b(otp|totp|2fa|mfa)\b|one.?time|verification.?code|auth.?code/i.test(hay(el));
  const isUser = el => /user|login|e-?mail|client.?(id|code)|member.?(id|code)|trading.?code|user.?id/i.test(hay(el));
  const what = el => labelOf(el) || el.getAttribute('placeholder') || el.getAttribute('aria-label') || el.name || 'the box';
  const sent = new WeakMap(); // the value last recorded for each box
  function typed(el) {
    if (!isBox(el)) return;
    const v = el.value;
    if (sent.get(el) === (isPw(el) ? '\u0000pw' + v.length : v)) return;
    sent.set(el, isPw(el) ? '\u0000pw' + v.length : v);
    if (isPw(el)) send({ kind: 'type', target: target(el), needs_secret: 'password', label: 'Type the password' });
    else if (isOtp(el)) send({ kind: 'type', target: target(el), needs_secret: 'otp', label: 'Type the one-time password' });
    else send({ kind: 'type', target: target(el), value: v, needs_secret: isUser(el) ? 'username' : '', label: 'Type in ' + what(el) });
  }
  document.addEventListener('click', ev => {
    if (!ev.isTrusted) return;
    const el = clickable(ev.target);
    if (isBox(el) || el.tagName === 'SELECT' || el.tagName === 'OPTION') return; // typing and choosing are recorded instead
    if (el.tagName === 'INPUT' && el.type === 'file') return;
    if (el.tagName === 'INPUT' && (el.type === 'checkbox' || el.type === 'radio')) {
      send({ kind: 'check', target: target(el), value: el.checked ? 'on' : 'off', label: (el.checked ? 'Tick ' : 'Untick ') + (labelOf(el) || el.name || 'the box') });
      return;
    }
    if (el.tagName === 'LABEL' && el.control && (el.control.type === 'checkbox' || el.control.type === 'radio')) return; // the box's own click follows
    // Leaving a box by clicking elsewhere: record what was typed first.
    if (document.activeElement && document.activeElement !== el) typed(document.activeElement);
    send({ kind: 'click', target: target(el), label: 'Click ' + (short(el.innerText || el.value || '') || el.getAttribute('aria-label') || el.title || el.tagName.toLowerCase()) });
  }, true);
  document.addEventListener('change', ev => {
    if (!ev.isTrusted) return;
    const el = ev.target;
    if (el.tagName === 'SELECT') { const o = el.options[el.selectedIndex]; send({ kind: 'select', target: target(el), value: o ? clean(o.text) : el.value, label: 'Choose ' + (o ? clean(o.text) : el.value) + (labelOf(el) ? ' in ' + labelOf(el) : '') }); return; }
    if (el.tagName === 'INPUT' && el.type === 'file') { send({ kind: 'upload', target: target(el), label: 'Upload a file' }); return; }
    typed(el);
  }, true);
  document.addEventListener('keydown', ev => {
    if (!ev.isTrusted || !['Enter', 'Tab', 'Escape'].includes(ev.key)) return;
    const el = ev.target;
    if (ev.key === 'Tab') { typed(el); return; }
    if (el.tagName === 'TEXTAREA' && ev.key === 'Enter') return;
    typed(el);
    send({ kind: 'press', key: ev.key, label: 'Press ' + ev.key });
  }, true);
})();
