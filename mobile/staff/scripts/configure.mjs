// Ledgerline staff app: writes capacitor.config.json and www/config.js from
// app.config.json; with --native, also puts the shared native code into
// ios/ and android/ (after `npx cap add ios` / `npx cap add android`).
//
//   npm run configure      before npx cap add, and after changing app.config.json
//   npm run native         after npx cap add, and after updating Capacitor
//
// The staff app has no fixed server: it opens the server and firm set up
// from the QR code in Settings → Staff app (ledgerline-staff://setup?...),
// limited to the hosts in allowedHosts.

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { applyNative } from '../../shared/native.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const cfg = JSON.parse(fs.readFileSync(path.join(root, 'app.config.json'), 'utf8'));

const fail = m => { console.error('app.config.json: ' + m); process.exit(1); };
if (!/^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){2,}$/.test(cfg.appId || '')) fail('appId must be a reverse domain such as in.yourcompany.staff (lower case)');
if (!cfg.appName || cfg.appName.length > 30) fail('appName is 1 to 30 characters');
if (!/^[a-z][a-z0-9+.-]*$/.test(cfg.scheme || '')) fail('scheme must be lower case letters, such as ledgerline-staff');
if (!Array.isArray(cfg.allowedHosts) || !cfg.allowedHosts.length) fail('allowedHosts lists the servers the app may open, such as ["backoffice.yourfirm.in"] or ["*.yourcompany.in"]');
if (cfg.allowedHosts.includes('*')) console.warn('Warning: allowedHosts "*" lets a QR code point the app at any server. List your servers instead.');
const pins = cfg.pinning?.enabled ? cfg.pinning.spkiSha256 || [] : [];
if (cfg.pinning?.enabled && (!pins.length || !cfg.pinning.host)) fail('pinning needs host and at least one spkiSha256 (two: the current key and a backup)');

const cap = {
  appId: cfg.appId,
  appName: cfg.appName,
  webDir: 'www',
  loggingBehavior: 'none',
  server: { androidScheme: 'https', allowNavigation: cfg.allowedHosts },
  ios: { contentInset: 'never', scrollEnabled: true, webContentsDebuggingEnabled: false },
  android: { allowMixedContent: false, webContentsDebuggingEnabled: false, captureInput: true },
  plugins: {
    PushNotifications: { presentationOptions: ['badge', 'sound', 'alert'] },
    SplashScreen: { launchShowDuration: 600, backgroundColor: '#ffffff', showSpinner: false },
  },
};
fs.writeFileSync(path.join(root, 'capacitor.config.json'), JSON.stringify(cap, null, 2) + '\n');
fs.writeFileSync(path.join(root, 'www/config.js'), `// Written by scripts/configure.mjs from app.config.json.\nwindow.LL_APP = ${JSON.stringify({ name: cfg.appName, scheme: cfg.scheme, allowedHosts: cfg.allowedHosts, version: cfg.version })};\n`);
console.log('capacitor.config.json and www/config.js written');

if (process.argv.includes('--native')) {
  applyNative({ root, appId: cfg.appId, appName: cfg.appName, scheme: cfg.scheme, runtimeServer: true, allowedHosts: cfg.allowedHosts,
    serverHost: cfg.pinning?.host || '', pins, pinExpires: cfg.pinning?.expires || '', faceIdText: 'Unlock the staff app with Face ID instead of your password.', attendance: true });
  console.log('\nNext: set the version (' + cfg.version + ') and build number (' + cfg.build + ') in Xcode and android/app/build.gradle, then npx cap sync.');
}
