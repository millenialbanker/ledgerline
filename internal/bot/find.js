// Ledgerline bot: find the element a step acts on. Tries the recorded CSS
// selector, then the visible text, label, placeholder and name, in the page
// and in same-site frames, so small page changes do not break a workflow.
(t) => {
  const norm = s => String(s || '').replace(/\s+/g, ' ').trim().toLowerCase();
  const visible = el => {
    if (!el || !el.isConnected) return false;
    if (t.any) return true;
    const r = el.getBoundingClientRect(), s = el.ownerDocument.defaultView.getComputedStyle(el);
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none' && s.opacity !== '0';
  };
  const textOf = el => norm(el.innerText || el.value || el.getAttribute('aria-label') || '');
  function inDoc(doc) {
    let cands = [];
    if (t.css) { try { cands = [...doc.querySelectorAll(t.css)].filter(visible); } catch (e) {} }
    if (cands.length === 1) return cands[0];
    if (cands.length > 1) {
      if (t.text) { const m = cands.find(x => textOf(x) === norm(t.text)); if (m) return m; }
      return cands[0];
    }
    const sel = t.tag && t.tag !== 'div' && t.tag !== 'span' ? t.tag : 'a,button,input,select,textarea,label,summary,[role],[onclick],[data-act],td,th,li,span,div,img';
    let pool;
    try { pool = [...doc.querySelectorAll(sel)].filter(visible); } catch (e) { pool = []; }
    if (t.label) {
      const n = norm(t.label);
      const x = pool.find(x => x.labels && [...x.labels].some(l => norm(l.innerText) === n));
      if (x) return x;
    }
    if (t.placeholder) { const x = pool.find(x => norm(x.getAttribute('placeholder')) === norm(t.placeholder)); if (x) return x; }
    if (t.name) { const x = pool.find(x => norm(x.getAttribute('aria-label')) === norm(t.name) || x.getAttribute('name') === t.name); if (x) return x; }
    if (t.text) {
      const n = norm(t.text);
      const by = f => pool.filter(f).sort((a, b) => textOf(a).length - textOf(b).length || (b.contains(a) ? -1 : a.contains(b) ? 1 : 0));
      let m = by(x => textOf(x) === n);
      if (!m.length) m = by(x => { const v = textOf(x); return v.includes(n) && v.length < n.length * 3 + 20; });
      if (m.length) return m[0];
    }
    return null;
  }
  function walk(doc, depth) {
    const el = inDoc(doc);
    if (el || depth > 3) return el;
    for (const f of doc.querySelectorAll('iframe,frame')) {
      let d = null;
      try { d = f.contentDocument; } catch (e) {}
      if (d) { const x = walk(d, depth + 1); if (x) return x; }
    }
    return null;
  }
  return walk(document, 0);
}
