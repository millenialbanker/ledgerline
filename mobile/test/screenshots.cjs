// Screenshots of the staff app (/m/) and the client app (/app/) at phone
// sizes, against a development server on BASE that seed.sh has prepared.
// The HRMS endpoints (GET /v1/hr/me, POST /v1/hr/attendance/punch) are
// faked here to their contract on branch hrms, which builds them.
// The pages and scripts are served from this checkout (no rebuild needed).
// Run as seed.sh says; prints "no page errors" when all is well.
const { chromium, devices } = require(process.env.PLAYWRIGHT || 'playwright');
const BASE = process.env.BASE || 'http://127.0.0.1:18080';
const OUT = process.env.OUT;
const CLIENT = JSON.parse(process.env.CLIENT || '{}');
const errors = [];

// The pages and scripts from the worktree, so a change needs no rebuild.
const WT = require('path').join(__dirname, '../../web/');
const fs = require('fs');
async function local(ctx) {
  for (const [pat, file, ct] of [['**/m/?*', 'm.html', 'text/html'], ['**/app/?*', 'mapp.html', 'text/html'], ['**/mshell.js', 'mshell.js', 'text/javascript'], ['**/mstaff.js', 'mstaff.js', 'text/javascript'], ['**/mclient.js', 'mclient.js', 'text/javascript'], ['**/mobileapps.js', 'mobileapps.js', 'text/javascript'], ['**/mobile.css', 'mobile.css', 'text/css'], ['**/app.js', 'app.js', 'text/javascript']])
    await ctx.route(pat, r => r.fulfill({ status: 200, contentType: ct, body: fs.readFileSync(WT + file) }));
}
async function phone(browser, dev, opts = {}) {
  const ctx = await browser.newContext({ ...devices[dev], geolocation: { latitude: 12.9716, longitude: 77.5946 }, permissions: ['geolocation'], ...opts });
  await local(ctx);
  const page = await ctx.newPage();
  page.on('pageerror', e => errors.push(`${dev}: ${e.message}`));
  page.on('console', m => { if (m.type() === 'error' && !/404|401|Failed to load resource/.test(m.text())) errors.push(`${dev} console: ${m.text()}`); });
  return { ctx, page };
}
const shot = (page, name) => page.screenshot({ path: `${OUT}/${name}.png`, fullPage: false });
const wait = ms => new Promise(r => setTimeout(r, ms));
async function tab(page, name) { await page.click(`#m-tabs [data-go="${name}"]`); await page.waitForSelector('#m-page .ph-pg'); await wait(400); }

async function signIn(page, user, pw) {
  await page.fill('input[name=user]', user);
  await page.fill('input[name=password]', pw);
  await page.click('#m-login button[type=submit]');
}

(async () => {
  const browser = await chromium.launch();
  // ---------- staff app, iPhone, a checker ----------
  {
    const { ctx, page } = await phone(browser, 'iPhone 13');
    // The HRMS module, faked to its contract on branch hrms: GET /v1/hr/me
    // (the person's own records) and POST /v1/hr/attendance/punch.
    let punches = 0, last = null;
    const today = new Date(Date.now() + 330 * 60000).toISOString().slice(0, 10);
    await page.route('**/v1/hr/me*', r => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ linked: true, attendance: { employee: 'E0007', policy: { id: 'std', name: 'Standard', modes: ['office', 'home', 'field', 'onduty'] },
      days: [{ date: today, kind: 'working', status: last ? 'Present' : '', in: last ? '09:41' : '', mode: last ? last.mode : '', punches: last ? [{ at: '2026-11-20T04:11:00Z', kind: 'in', mode: last.mode }] : [] }] } }) }));
    await page.route('**/v1/hr/attendance/punch', async r => {
      punches++;
      const b = JSON.parse(r.request().postData());
      const known = ['kind', 'mode', 'note', 'has_location', 'lat', 'lng', 'device_id', 'biometric_unlocked', 'selfie'];
      if (!['in', 'out'].includes(b.kind) || !['office', 'home', 'field'].includes(b.mode) || b.has_location !== true || typeof b.lat !== 'number' || Object.keys(b).some(k => !known.includes(k))) errors.push('bad punch body ' + r.request().postData());
      if (b.mode === 'office') return r.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ error: { code: 'forbidden', message: 'not permitted for this role: an office punch is accepted only on the office network or at the office (within its radius); you are not' } }) });
      if (b.mode === 'field' && !b.note) return r.fulfill({ status: 422, contentType: 'application/json', body: JSON.stringify({ error: { code: 'invalid', message: 'a field punch needs your location and a note (whom you are visiting)' } }) });
      last = b;
      return r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ employee: 'E0007', name: 'Farhan Khan', at: '2026-11-20T04:11:00Z', kind: b.kind, mode: b.mode, status: 'Present' }) });
    });
    await page.goto(`${BASE}/m/?firm=demo`);
    await page.waitForSelector('#m-login');
    await shot(page, 'staff-01-signin-iphone');
    await signIn(page, 'farhan.khan', 'ledgerline-demo');
    await page.waitForSelector('.ph-seg'); await wait(800);
    await shot(page, 'staff-02-approvals-iphone');
    const first = await page.$('#m-page button.ph-card');
    if (first) {
      await first.click(); await page.waitForSelector('.ph-d'); await wait(300);
      await shot(page, 'staff-03-approval-detail-iphone');
      const rej = await page.$('[data-act=reject]');
      if (rej) { await rej.click(); await page.waitForSelector('.m-sh'); await shot(page, 'staff-04-reject-sheet-iphone'); await page.click('.m-shf [data-x]'); }
    } else errors.push('no approval waiting for farhan.khan');
    await tab(page, 'work'); await shot(page, 'staff-05-work-iphone');
    await tab(page, 'today'); await shot(page, 'staff-06-today-iphone');
    await page.screenshot({ path: `${OUT}/staff-06b-today-full-iphone.png`, fullPage: true });
    await tab(page, 'clients'); await page.fill('#m-q', 'sh'); await wait(1200); await shot(page, 'staff-07-clients-iphone');
    const cl = await page.$('#m-cl .ph-cl');
    if (cl) { await cl.click(); await page.waitForSelector('.ph-d'); await wait(300); await shot(page, 'staff-08-client-iphone'); }
    await tab(page, 'more'); await shot(page, 'staff-09-more-iphone');
    await page.click('[data-go=ledgy]'); await page.waitForSelector('#m-lg'); await page.fill('#m-lg input', 'how do I approve a payout'); await page.click('#m-lg button'); await wait(1500); await shot(page, 'staff-10-ledgy-iphone');
    // Attendance (HRMS faked): refused at the office, accepted in the field.
    await tab(page, 'approvals'); await page.waitForSelector('#m-hr'); await shot(page, 'staff-11-checkin-bar-iphone');
    await page.click('#m-hr [data-go=attendance]'); await page.waitForSelector('[data-act=punch]'); await wait(300);
    await shot(page, 'staff-12-checkin-iphone');
    await page.click('[data-act=punch]'); await page.waitForSelector('#m-hrerr .m-err'); await shot(page, 'staff-13-checkin-refused-iphone');
    await page.click('[data-act=hrMode][data-id=field]'); await wait(300); await page.fill('#m-hrnote', 'Client visit, Koramangala'); await page.click('[data-act=punch]'); await page.waitForSelector('.m-ok'); await wait(300);
    await shot(page, 'staff-14-checkin-accepted-iphone');
    if (punches !== 2) errors.push('punches ' + punches);
    await tab(page, 'approvals'); await page.waitForSelector('#m-hr'); await wait(300);
    if (!/Check out/.test(await page.textContent('#m-hr'))) errors.push('bar does not offer check-out after a check-in');
    await shot(page, 'staff-14b-checked-in-bar-iphone');
    await page.click('[data-act=lockNow]'); await page.waitForSelector('#m-login'); await shot(page, 'staff-15-locked-iphone');
    await ctx.close();
  }
  // ---------- staff app, Pixel, branch staff (no HRMS: the bar is hidden) ----------
  {
    const { ctx, page } = await phone(browser, 'Pixel 7');
    await page.goto(`${BASE}/m/?firm=demo`);
    await page.waitForSelector('#m-login');
    await signIn(page, 'meera.iyer', 'ledgerline-demo');
    await page.waitForSelector('.ph-seg'); await wait(1000);
    if (await page.$('#m-hr')) errors.push('attendance bar shown without HRMS');
    await shot(page, 'staff-20-approvals-branch-pixel');
    await tab(page, 'today'); await shot(page, 'staff-21-today-branch-pixel');
    await ctx.close();
  }
  // ---------- staff app, not on the list ----------
  {
    const { ctx, page } = await phone(browser, 'Pixel 7');
    await page.goto(`${BASE}/m/?firm=demo`);
    await page.waitForSelector('#m-login');
    await signIn(page, 'anita.rao', 'ledgerline-demo');
    await wait(1500);
    await shot(page, 'staff-22-not-on-list-pixel');
    await ctx.close();
  }
  // ---------- client app ----------
  for (const dev of ['Pixel 7', 'iPhone 13']) {
    const tag = dev.startsWith('Pixel') ? 'pixel' : 'iphone';
    const { ctx, page } = await phone(browser, dev);
    await page.goto(`${BASE}/app/?firm=demo`);
    await page.waitForSelector('#m-login');
    await shot(page, `client-01-signin-${tag}`);
    await signIn(page, CLIENT.user, tag === 'pixel' ? CLIENT.password : 'a long client password 1');
    if (tag === 'pixel') {
      await page.waitForSelector('#m-pw'); await shot(page, 'client-02-set-password-pixel');
      await page.fill('input[name=current]', CLIENT.password); await page.fill('input[name=next]', 'a long client password 1'); await page.click('#m-pw button');
    }
    await page.waitForSelector('.m-tiles'); await wait(800);
    await shot(page, `client-03-home-${tag}`);
    for (const [t, n] of [['holdings', '04-holdings'], ['funds', '05-funds'], ['documents', '07-reports']]) { await tab(page, t); await shot(page, `client-${n}-${tag}`); }
    await tab(page, 'funds'); await page.click('#m-page [data-go=addmoney]'); await page.waitForSelector('.ph-pg h1'); await wait(300); await shot(page, `client-06-addmoney-${tag}`);
    await tab(page, 'more'); await shot(page, `client-08-more-${tag}`);
    for (const [r, n] of [['about', '09-about'], ['profile', '10-profile'], ['help', '11-help'], ['ipo', '12-ipo'], ['mf', '13-mf'], ['pledge', '14-pledge'], ['ledger', '15-ledger'], ['notifications', '16-notifications']]) {
      if (tag === 'iphone' && !['about', 'profile'].includes(r)) continue;
      await page.evaluate(x => { location.hash = x; }, r);
      await page.evaluate(x => go(x), r); await page.waitForSelector('#m-page .ph-pg, #m-page .m-err'); await wait(500);
      await shot(page, `client-${n}-${tag}`);
    }
    await ctx.close();
  }
  // ---------- client app: About us before sign-in ----------
  {
    const { ctx, page } = await phone(browser, 'Pixel 7');
    await page.goto(`${BASE}/app/?firm=demo`);
    await page.waitForSelector('.m-legal button'); await page.click('.m-legal button'); await page.waitForSelector('.ph-pg h1'); await wait(300);
    await shot(page, 'client-17-about-before-signin-pixel');
    await ctx.close();
  }
  // ---------- the console's settings screens ----------
  {
    const ctx = await browser.newContext({ viewport: { width: 1366, height: 1000 } });
    await local(ctx);
    const page = await ctx.newPage();
    page.on('pageerror', e => errors.push('console: ' + e.message));
    await page.goto(`${BASE}/`);
    await page.evaluate(() => fetch('/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ user: 'admin', password: 'ledgerline-demo' }) }));
    for (const v of ['staffapp', 'clientapp']) {
      await page.goto(`${BASE}/#${v}`); await page.reload(); await page.waitForSelector('.ph h1'); await wait(1200);
      await page.screenshot({ path: `${OUT}/console-${v}.png`, fullPage: true });
    }
    await ctx.close();
  }
  await browser.close();
  console.log(errors.length ? 'ERRORS:\n' + errors.join('\n') : 'no page errors');
})().catch(e => { console.error(e); console.log(errors.join('\n')); process.exit(1); });
