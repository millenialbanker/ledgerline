// Ledgerline mobile shells: puts the shared native code into the iOS and
// Android projects that `npx cap add ios` and `npx cap add android` made,
// and sets what Capacitor's own configuration cannot:
//
//   iOS      AppDelegate.swift (push through Firebase, the app-switcher
//            cover, the LedgerlineShell plugin, the staff app's server),
//            Main.storyboard (MainViewController), Info.plist (Face ID
//            text, link scheme, background push, optional pinning),
//            Podfile (FirebaseMessaging)
//   Android  MainActivity.java (FLAG_SECURE: no screenshots or recents
//            thumbnail; the staff app's server; optional pin check),
//            LedgerlineShellPlugin.java, AndroidManifest.xml (link scheme,
//            no backup, network security config), app/build.gradle
//            (androidx.biometric), res/xml/network_security_config.xml
//
// It is run by each app's scripts/configure.mjs with --native. Running it
// again is safe: every change checks whether it was made already.
// Node 20 or later, no packages.

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));

function read(p) { return fs.readFileSync(p, 'utf8'); }
function write(p, s) { fs.mkdirSync(path.dirname(p), { recursive: true }); fs.writeFileSync(p, s); }
function patch(p, test, fn, what) {
  if (!fs.existsSync(p)) throw new Error(`${p} is missing: run npx cap add first`);
  const s = read(p);
  if (test(s)) { console.log(`  ${what}: already done`); return; }
  const t = fn(s);
  if (t === s) throw new Error(`${p}: could not ${what} (the file is not as Capacitor 7 makes it; do it by hand, see the README)`);
  write(p, t);
  console.log(`  ${what}`);
}
const xmlEsc = s => String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
const swiftStr = s => JSON.stringify(String(s));
const javaStr = s => JSON.stringify(String(s));

// opts: { root, appId, appName, scheme, runtimeServer, allowedHosts[],
//         serverHost, pins[] (SPKI SHA-256, base64), pinExpires, faceIdText,
//         attendance (location and camera permissions for check-in) }
export function applyNative(opts) {
  const root = opts.root;
  const ios = path.join(root, 'ios/App'), android = path.join(root, 'android');
  if (fs.existsSync(ios)) applyIOS(ios, opts); else console.log('ios/ not found: skipped (run npx cap add ios on a Mac)');
  if (fs.existsSync(android)) applyAndroid(android, opts); else console.log('android/ not found: skipped (run npx cap add android)');
  for (const [file, dest] of [['GoogleService-Info.plist', 'ios/App/App/GoogleService-Info.plist'], ['google-services.json', 'android/app/google-services.json']]) {
    const src = path.join(root, 'firebase', file);
    if (fs.existsSync(src) && fs.existsSync(path.dirname(path.join(root, dest)))) { fs.copyFileSync(src, path.join(root, dest)); console.log(`  copied firebase/${file} → ${dest}`); }
    else if (!fs.existsSync(src)) console.log(`  firebase/${file} not there yet: push notifications need it (see the README)`);
  }
}

function applyIOS(ios, o) {
  console.log('iOS:');
  const tpl = read(path.join(here, 'ios/AppDelegate.swift'))
    .replace('__RUNTIME_SERVER__', o.runtimeServer ? 'true' : 'false')
    .replace('__ALLOWED_HOSTS__', '[' + (o.allowedHosts || []).map(swiftStr).join(', ') + ']')
    .replace('__APP_PATH__', swiftStr(o.runtimeServer ? '/m/' : '/app/'));
  write(path.join(ios, 'App/AppDelegate.swift'), tpl);
  console.log('  AppDelegate.swift written (with MainViewController and the LedgerlineShell plugin)');
  patch(path.join(ios, 'App/Base.lproj/Main.storyboard'), s => s.includes('customClass="MainViewController"'),
    s => s.replace(/customClass="CAPBridgeViewController" customModule="Capacitor"/, 'customClass="MainViewController" customModule="App" customModuleProvider="target"'), 'Main.storyboard uses MainViewController');
  const plist = path.join(ios, 'App/Info.plist');
  const add = (key, xml) => patch(plist, s => s.includes(`<key>${key}</key>`), s => s.replace(/<\/dict>\s*<\/plist>\s*$/, `\t<key>${key}</key>\n${xml}\n</dict>\n</plist>\n`), `Info.plist ${key}`);
  add('NSFaceIDUsageDescription', `\t<string>${xmlEsc(o.faceIdText || 'Unlock the app with Face ID instead of your password.')}</string>`);
  add('CFBundleURLTypes', `\t<array>\n\t\t<dict>\n\t\t\t<key>CFBundleURLName</key>\n\t\t\t<string>${xmlEsc(o.appId)}</string>\n\t\t\t<key>CFBundleURLSchemes</key>\n\t\t\t<array>\n\t\t\t\t<string>${xmlEsc(o.scheme)}</string>\n\t\t\t</array>\n\t\t</dict>\n\t</array>`);
  add('UIBackgroundModes', '\t<array>\n\t\t<string>remote-notification</string>\n\t</array>');
  add('ITSAppUsesNonExemptEncryption', '\t<false/>');
  if (o.attendance) {
    // The staff app's attendance check-in: location with each punch, and a
    // selfie from the front camera when the firm asks.
    add('NSLocationWhenInUseUsageDescription', '\t<string>Your location is sent with each attendance check-in and check-out, and at no other time.</string>');
    add('NSCameraUsageDescription', '\t<string>Take a selfie for an attendance check-in when your firm asks for one.</string>');
  }
  add('FirebaseAppDelegateProxyEnabled', '\t<false/>');
  if (o.pins?.length && o.serverHost) {
    // ATS identity pinning (iOS 14+): connections to the host must present
    // a certificate whose public key is one of these.
    add('NSAppTransportSecurity', `\t<dict>\n\t\t<key>NSPinnedDomains</key>\n\t\t<dict>\n\t\t\t<key>${xmlEsc(o.serverHost)}</key>\n\t\t\t<dict>\n\t\t\t\t<key>NSIncludesSubdomains</key>\n\t\t\t\t<false/>\n\t\t\t\t<key>NSPinnedLeafIdentities</key>\n\t\t\t\t<array>\n${o.pins.map(p => `\t\t\t\t\t<dict>\n\t\t\t\t\t\t<key>SPKI-SHA256-BASE64</key>\n\t\t\t\t\t\t<string>${xmlEsc(p)}</string>\n\t\t\t\t\t</dict>`).join('\n')}\n\t\t\t\t</array>\n\t\t\t</dict>\n\t\t</dict>\n\t</dict>`);
  }
  patch(path.join(ios, 'Podfile'), s => s.includes("pod 'FirebaseMessaging'"), s => s.replace(/(target 'App' do\s*\n\s*capacitor_pods)/, "$1\n  pod 'FirebaseMessaging', '11.6.0'"), "Podfile adds FirebaseMessaging");
}

function applyAndroid(android, o) {
  console.log('Android:');
  const pkgDir = path.join(android, 'app/src/main/java', ...o.appId.split('.'));
  for (const f of ['MainActivity.java', 'LedgerlineShellPlugin.java']) {
    const s = read(path.join(here, 'android', f))
      .replace(/__PACKAGE__/g, o.appId)
      .replace('__RUNTIME_SERVER__', o.runtimeServer ? 'true' : 'false')
      .replace('__ALLOWED_HOSTS__', (o.allowedHosts || []).map(javaStr).join(', '))
      .replace('__PINS__', (o.pins || []).map(javaStr).join(', '))
      .replace('__PIN_HOST__', javaStr(o.serverHost || ''))
      .replace('__APP_PATH__', javaStr(o.runtimeServer ? '/m/' : '/app/'));
    write(path.join(pkgDir, f), s);
    console.log(`  ${f} written`);
  }
  const man = path.join(android, 'app/src/main/AndroidManifest.xml');
  patch(man, s => s.includes(`android:scheme="${o.scheme}"`), s => s.replace(/(<intent-filter>\s*<action android:name="android.intent.action.MAIN" \/>\s*<category android:name="android.intent.category.LAUNCHER" \/>\s*<\/intent-filter>)/,
    `$1\n\n            <intent-filter>\n                <action android:name="android.intent.action.VIEW" />\n                <category android:name="android.intent.category.DEFAULT" />\n                <category android:name="android.intent.category.BROWSABLE" />\n                <data android:scheme="${xmlEsc(o.scheme)}" />\n            </intent-filter>`), 'AndroidManifest link scheme');
  patch(man, s => s.includes('android:networkSecurityConfig'), s => s.replace(/android:allowBackup="true"/, 'android:allowBackup="false"\n        android:fullBackupContent="false"\n        android:networkSecurityConfig="@xml/network_security_config"'), 'AndroidManifest: no backup, network security config');
  patch(man, s => s.includes('android.permission.USE_BIOMETRIC'), s => s.replace(/(<uses-permission android:name="android.permission.INTERNET" \/>)/, '$1\n    <uses-permission android:name="android.permission.USE_BIOMETRIC" />\n    <uses-permission android:name="android.permission.POST_NOTIFICATIONS" />'), 'AndroidManifest permissions');
  if (o.attendance) patch(man, s => s.includes('android.permission.ACCESS_FINE_LOCATION'), s => s.replace(/(<uses-permission android:name="android.permission.INTERNET" \/>)/, '$1\n    <uses-permission android:name="android.permission.ACCESS_COARSE_LOCATION" />\n    <uses-permission android:name="android.permission.ACCESS_FINE_LOCATION" />\n    <uses-permission android:name="android.permission.CAMERA" />'), 'AndroidManifest attendance permissions (location, camera)');
  const pinSet = o.pins?.length && o.serverHost ? `\n    <domain-config cleartextTrafficPermitted="false">\n        <domain includeSubdomains="false">${xmlEsc(o.serverHost)}</domain>\n        <pin-set expiration="${xmlEsc(o.pinExpires || '')}">\n${o.pins.map(p => `            <pin digest="SHA-256">${xmlEsc(p)}</pin>`).join('\n')}\n        </pin-set>\n    </domain-config>` : '';
  write(path.join(android, 'app/src/main/res/xml/network_security_config.xml'), `<?xml version="1.0" encoding="utf-8"?>\n<!-- Written by mobile/shared/native.mjs: https only${pinSet ? ', and the server\'s public key pinned' : ''}. -->\n<network-security-config>\n    <base-config cleartextTrafficPermitted="false">\n        <trust-anchors>\n            <certificates src="system" />\n        </trust-anchors>\n    </base-config>${pinSet}\n</network-security-config>\n`);
  console.log('  res/xml/network_security_config.xml written');
  patch(path.join(android, 'app/build.gradle'), s => s.includes('androidx.biometric:biometric'), s => s.replace(/dependencies \{/, 'dependencies {\n    implementation "androidx.biometric:biometric:1.1.0"'), 'app/build.gradle adds androidx.biometric');
}

// Host patterns: "backoffice.example.in" or "*.example.in".
export function hostAllowed(host, patterns) {
  host = String(host || '').toLowerCase();
  return (patterns || []).some(p => { p = String(p).toLowerCase(); return p === '*' || p === host || (p.startsWith('*.') && host.endsWith(p.slice(1)) && host.length > p.length - 1); });
}
