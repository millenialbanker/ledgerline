// A broker's client app: writes capacitor.config.json and www/index.html
// from brand.json; with --native, also puts the shared native code into
// ios/ and android/ (after `npx cap add ios` / `npx cap add android`).
//
//   npm run configure      before npx cap add, and after changing brand.json
//   npm run native         after npx cap add, and after updating Capacitor
//
// The app opens <serverUrl>/app/?firm=<firm>: the client app pages of the
// broker's Ledgerline server. The name, colours and logo shown inside the
// app come from Settings → Client app on that server; brand.json is what
// the stores and the phone see (name under the icon, bundle ID, icon).

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { applyNative } from '../../shared/native.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const b = JSON.parse(fs.readFileSync(path.join(root, 'brand.json'), 'utf8'));

const fail = m => { console.error('brand.json: ' + m); process.exit(1); };
if (!/^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){2,}$/.test(b.appId || '')) fail('appId must be a reverse domain you own, such as in.yourbroker.invest (lower case)');
if (!b.appName || b.appName.length > 30) fail('appName is 1 to 30 characters');
let server;
try { server = new URL(b.serverUrl); } catch (e) { fail('serverUrl must be the https address of your Ledgerline server'); }
if (server.protocol !== 'https:' || server.pathname.length > 1) fail('serverUrl must be https://host with no path');
if (!/^[a-z0-9][a-z0-9_-]{0,40}$/.test(b.firm || '')) fail('firm is your firm code in Ledgerline');
if (!/^[a-z][a-z0-9+.-]*$/.test(b.scheme || '')) fail('scheme must be lower case letters, such as yourbrokerapp');
for (const c of Object.values(b.colors || {})) if (!/^#[0-9a-fA-F]{6}$/.test(c)) fail('colours are #RRGGBB');
const pins = b.pinning?.enabled ? b.pinning.spkiSha256 || [] : [];
if (b.pinning?.enabled && pins.length < 2) fail('pinning needs two spkiSha256 values: the current key and a backup key');
if (b.pinning?.enabled && !/^\d{4}-\d{2}-\d{2}$/.test(b.pinning.expires || '')) fail('pinning needs expires (YYYY-MM-DD): after it, Android stops checking the pins so an old app keeps working');
for (const f of [b.icon, b.splash]) if (f && !fs.existsSync(path.join(root, f))) console.warn(`Note: ${f} is not there yet (see the README: icons)`);

const appUrl = `${server.origin}/app/?firm=${encodeURIComponent(b.firm)}`;
const cap = {
  appId: b.appId,
  appName: b.appName,
  webDir: 'www',
  loggingBehavior: 'none',
  server: { url: appUrl, cleartext: false, allowNavigation: [server.hostname, ...(b.allowNavigation || [])] },
  ios: { contentInset: 'never', webContentsDebuggingEnabled: false, backgroundColor: b.colors?.background || '#ffffff' },
  android: { allowMixedContent: false, webContentsDebuggingEnabled: false, captureInput: true, backgroundColor: b.colors?.background || '#ffffff' },
  plugins: {
    PushNotifications: { presentationOptions: ['badge', 'sound', 'alert'] },
    SplashScreen: { launchShowDuration: 600, backgroundColor: b.colors?.background || '#ffffff', showSpinner: false },
  },
};
fs.writeFileSync(path.join(root, 'capacitor.config.json'), JSON.stringify(cap, null, 2) + '\n');
// The page shown only if the server cannot be reached at start.
fs.mkdirSync(path.join(root, 'www'), { recursive: true });
fs.writeFileSync(path.join(root, 'www/index.html'), `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'">
<title>${b.appName.replace(/</g, '')}</title>
<style>body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;font:16px/1.5 system-ui,-apple-system,sans-serif;background:${b.colors?.background || '#fff'};color:#16201b;text-align:center;padding:24px}b{color:${b.colors?.primary || '#1f4fd1'}}</style>
</head><body><p><b>${b.appName.replace(/</g, '')}</b><br>Cannot reach the server. Check your internet connection and open the app again.</p></body></html>
`);
console.log(`capacitor.config.json written: the app opens ${appUrl}`);

if (process.argv.includes('--native')) {
  applyNative({ root, appId: b.appId, appName: b.appName, scheme: b.scheme, runtimeServer: false, allowedHosts: [server.hostname],
    serverHost: server.hostname, pins, pinExpires: b.pinning?.expires || '', faceIdText: `Unlock ${b.appName} with Face ID instead of your password.` });
  console.log(`\nNext: set the version (${b.version}) and build number (${b.build}) in Xcode and android/app/build.gradle, add the icons, then npx cap sync.`);
}
