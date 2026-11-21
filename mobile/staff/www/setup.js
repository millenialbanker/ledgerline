'use strict';
// The staff app's first screen, bundled in the app: which Ledgerline server
// and firm to open. The QR code on Settings → Staff app is a link,
// ledgerline-staff://setup?server=https://…&firm=…, that the phone's camera
// opens in the app; the person confirms, then the shell saves it and
// reopens on that server's /m/ page (LedgerlineShell.setServer checks the
// host again natively).

(function () {
  const C = window.Capacitor, A = window.LL_APP || { allowedHosts: [] };
  const plug = n => C && C.Plugins ? C.Plugins[n] : null;
  const $ = id => document.getElementById(id);
  const allowed = h => A.allowedHosts.some(p => { p = p.toLowerCase(); h = h.toLowerCase(); return p === '*' || p === h || (p.startsWith('*.') && h.endsWith(p.slice(1))); });
  function show(m) { const e = $('err'); e.textContent = m; e.hidden = !m; }
  async function use(server, firm) {
    show('');
    let u;
    try { u = new URL(server.trim()); } catch (e) { show('That is not a web address.'); return; }
    if (u.protocol !== 'https:') { show('The server must be on https.'); return; }
    if (!allowed(u.hostname)) { show(`This app is not built for ${u.hostname}. Ask your Admin for the right app.`); return; }
    if (!/^[a-z0-9][a-z0-9_-]{0,40}$/i.test(firm.trim())) { show('Enter the firm code.'); return; }
    const S = plug('LedgerlineShell');
    if (!S) { show('Open this in the staff app.'); return; }
    try { await S.setServer({ url: u.origin, firm: firm.trim().toLowerCase() }); } catch (e) { show(e.message || 'Could not save the server.'); }
  }
  function fromLink(link) {
    try {
      const u = new URL(link);
      if (u.protocol !== (A.scheme || 'ledgerline-staff') + ':' || u.hostname !== 'setup') return;
      $('url').value = u.searchParams.get('server') || '';
      $('firm').value = u.searchParams.get('firm') || '';
      $('intro').textContent = 'Check the server and firm from the code, then continue.';
    } catch (e) {}
  }
  $('f').addEventListener('submit', e => { e.preventDefault(); use($('url').value, $('firm').value); });
  const App = plug('App');
  if (App) {
    App.addListener('appUrlOpen', x => fromLink(x.url));
    App.getLaunchUrl().then(x => { if (x && x.url) fromLink(x.url); }).catch(() => {});
  }
})();
