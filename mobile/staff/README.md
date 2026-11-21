# Ledgerline staff app (iOS and Android)

The staff app lets a firm's operators and checkers work from their phones:
approvals waiting for them (approve or reject with a reason, amount shown),
the day's progress, alerts and exceptions, their work queue, a quick client
look-up (masked as on the desktop) and Ask Ledgy; and, when the firm has the
HRMS module on, attendance check-in and check-out (Office, Home or Field;
face or fingerprint before each punch; location with the punch; a selfie
when the firm asks).

The screens are phone pages served by Ledgerline at `/m/`. This folder is
only the native shell (Capacitor 7) around them, plus the native pieces a web
page cannot do:

| Piece | Where |
| --- | --- |
| Device secret in the iOS keychain / Android keystore, released only by Face ID, Touch ID or fingerprint | `LedgerlineShell` plugin (`../shared/ios/AppDelegate.swift`, `../shared/android/LedgerlineShellPlugin.java`) |
| Push notifications (Firebase Cloud Messaging; iPhones through APNs) | `@capacitor/push-notifications` + Firebase in `AppDelegate.swift` |
| No screenshots or screen recording (Android `FLAG_SECURE`); screen hidden in the iOS app switcher and while recorded or mirrored | `MainActivity.java`, `AppDelegate.swift` |
| Rooted / jailbroken phone warning | `LedgerlineShell.integrity()` |
| Set-up from the QR code (server address and firm), deep links `ledgerline-staff://` | `www/` (set-up page), `MainViewController` / `MainActivity` |
| Optional certificate pinning | `app.config.json` → `pinning` |
| Location and front camera for attendance check-in (asked only when the person checks in) | `NSLocationWhenInUseUsageDescription`, `NSCameraUsageDescription`, Android `ACCESS_FINE_LOCATION` and `CAMERA` (added by `npm run native`) |

**This code has not been compiled or signed in the Ledgerline repository**
(its build machine has no Xcode or Android SDK). Build it on a Mac as below.
The first build is where any mismatch with your Capacitor version shows; the
native files say which Capacitor API each part uses.

## Who publishes it

The staff app is one app for many firms: the QR code on each firm's
**Settings → Staff app** tells it which server and firm to open, and the app
opens only the servers listed in `allowedHosts`. Usually the software provider
publishes it once, under its own developer accounts, with its server domains in
`allowedHosts` (for example `["*.yourcompany.in"]`). A broker that hosts
Ledgerline itself can publish its own copy with its own server in
`allowedHosts`. Staff apps need not be public: see *Private distribution*.

## Accounts you need

- **Apple Developer Program**, enrolled as an organisation (needs a D-U-N-S
  number; US$99 a year). Apple asks that financial-services apps come from the
  company that provides the service, not an individual.
- **Google Play Console**, an organisation account (one-time US$25; D-U-N-S
  number and identity checks). A new *personal* account must run a closed test
  with at least 12 testers for 14 days before it may publish; an organisation
  account need not.
- **Firebase** (Google), a project for push notifications (free; the Spark plan
  covers Cloud Messaging). One project serves the staff app and, if you like,
  client apps too.

## What you need on the Mac

- macOS 14 or later, **Xcode 16** or later (with the iOS platform installed),
  **CocoaPods** 1.15 or later (`brew install cocoapods`).
- **Node.js 20** or later (`brew install node`).
- **Android Studio** Ladybug (2024.2) or later, with Android SDK Platform 35 and
  the JDK it bundles (21). An Android phone or emulator for testing.
- An iPhone for testing push (the iOS simulator does not get pushes from FCM).

## Build it

1. **Name the app.** Edit `app.config.json`:
   - `appId`: your reverse domain, for example `in.yourcompany.staff`. It is the
     iOS bundle ID and the Android package; it cannot change after publishing.
   - `appName`: the name under the icon (at most 30 characters).
   - `allowedHosts`: the servers the app may open (`"backoffice.yourfirm.in"`,
     or `"*.yourcompany.in"` for every subdomain). Do not use `"*"`.
   - `scheme`: keep `ledgerline-staff` (the QR code on Settings → Staff app
     uses it).
   - `version` and `build`.
2. **Install and make the native projects.**
   ```sh
   cd mobile/staff
   npm install
   npm run configure          # writes capacitor.config.json and www/config.js
   npx cap add ios
   npx cap add android
   npm run native             # puts in the native code; safe to run again
   ```
   `npm run native` reports each change. If it says a file "is not as
   Capacitor 7 makes it", make that change by hand (each change is described at
   the top of `../shared/native.mjs`).
3. **Firebase.** In the Firebase console: *Add project* (no Analytics needed),
   then *Add app*:
   - **iOS**: bundle ID = your `appId`. Download `GoogleService-Info.plist`.
   - **Android**: package name = your `appId`. Download `google-services.json`.

   Put both in `mobile/staff/firebase/` and run `npm run native` again: it copies
   them into `ios/App/App/` and `android/app/`. Do not commit them (they are in
   `.gitignore`).
4. **APNs key (iPhone pushes).** In Apple Developer → *Certificates, IDs &
   Profiles → Keys*: create a key with *Apple Push Notifications service
   (APNs)*, download the `.p8` file, note its Key ID and your Team ID. In
   Firebase → *Project settings → Cloud Messaging → Apple app configuration*,
   upload it as the *APNs Authentication Key*.
5. **iOS in Xcode.** `npx cap sync ios`, then `npx cap open ios`.
   - Target *App* → *Signing & Capabilities*: your Team; bundle identifier =
     `appId`. **+ Capability → Push Notifications** and **+ Capability →
     Background Modes → Remote notifications**.
   - Drag `ios/App/App/GoogleService-Info.plist` into the *App* group in the
     project navigator (tick *Copy items if needed* and target *App*). Copying
     the file is not enough: Xcode must list it.
   - *General*: Version = `version`, Build = `build`; minimum iOS 14.
   - App icon: replace `App/Assets.xcassets/AppIcon.appiconset` (a 1024×1024
     PNG, no transparency), or run `npx @capacitor/assets generate --ios` with
     `assets/icon.png`.
   - Run on your iPhone. Then *Product → Archive → Distribute App → App Store
     Connect → Upload*.
6. **Android in Android Studio.** `npx cap sync android`, then
   `npx cap open android`.
   - In `android/variables.gradle` check `targetSdkVersion` is the level
     Google Play asks for now (35 or newer); in `android/app/build.gradle` set
     `versionCode` = `build` and `versionName` = `version`.
   - Make an upload key once and keep it safe:
     `keytool -genkeypair -v -keystore staff-upload.jks -alias upload -keyalg RSA -keysize 2048 -validity 10000`
   - *Build → Generate Signed App Bundle → Android App Bundle* with that key.
     Upload the `.aab` in Play Console and let Google manage the app signing
     key (Play App Signing).
7. **Turn it on in Ledgerline.** On the server (it must be on https, with
   `LEDGERLINE_PUBLIC_URL` set to its https address):
   - *Admin → Connections → Push notifications (Firebase Cloud Messaging)*:
     paste the service account key (Firebase → *Project settings → Service
     accounts → Generate new private key*, the whole JSON file).
   - *Admin → Settings → Staff app*: switch it on, choose the roles, people
     or branches, and show the QR code to each person.
8. **Try it.** On the phone: install, scan the QR code with the camera, sign in
   with password and two-factor code, choose *Use Face ID / fingerprint*, allow
   notifications. *Send a test push to my phone* on Settings → Staff app should
   arrive within a minute.

After a Capacitor update: `npm install`, `npx cap sync`, `npm run native`.

## Certificate pinning (optional)

Set `pinning.enabled`, `pinning.host` (your server), two `spkiSha256` values
(the base64 SHA-256 of the server certificate's public key, and of a backup key
you hold for the next renewal) and `pinning.expires`. Get a value with:

```sh
openssl s_client -connect backoffice.yourfirm.in:443 -servername backoffice.yourfirm.in </dev/null 2>/dev/null \
 | openssl x509 -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64
```

iOS enforces it through App Transport Security (`NSPinnedDomains`). Android's
WebView does not apply pins to page loads, so `MainActivity` checks the
server's key when the app starts and closes the app on a mismatch. A wrong pin
locks everyone out until an app update: keep the backup key, and renew the
certificate with the same key or the backup key.

## Private distribution

- **Apple**: publish as a *Custom App* for your organisation through Apple
  Business Manager (no public listing), or as an *Unlisted* app (a direct link
  only). Both still go through App Review.
- **Google**: publish as a *private app* to your organisation through managed
  Google Play (Play Console → *Advanced settings → Managed Google Play*), or
  keep it in a closed testing track for your staff.

## Store listing and what the stores ask

- **Sign-in for review.** Apple and Google need a working demo login. Give them
  a user on a demo firm (the development server's sample data is made up) and
  say that sign-in needs the firm's QR code, or put the server and firm code in
  the review notes. If the user has two-factor sign-in, give the reviewers an
  authenticator secret or a user without it on the demo firm.
- **Minimum functionality (Apple 4.2).** The app is more than a website: it
  unlocks with Face ID, keeps its credential in the keychain, gets push
  notifications and blocks screen capture. Say so in the review notes.
- **Accounts.** The app does not create accounts (the firm's Admin does), so
  Apple's in-app account deletion rule does not apply; *More → Remove this
  phone* removes the phone's registration.
- **Apple App Privacy (privacy "nutrition label")**, for the staff app:
  - *Contact info*: not collected by the app.
  - *Identifiers → User ID* (the staff user ID) and *Device ID* (the push
    token): collected, linked to the user, for *App Functionality*.
  - *Financial info* and *Other data*: the app shows the firm's records to its
    staff; declare *Other data* for *App Functionality* if your review of the
    firm's data says so.
  - *Location → Precise location*: collected with each attendance check-in
    and check-out only, linked to the user, for *App Functionality*.
  - *User content → Photos*: the attendance selfie, when the firm asks for
    one; linked, *App Functionality*.
  - Tracking: **No**. Third-party advertising: **No**. Data used for analytics:
    **No**.
- **Google Play Data safety**: the same: user IDs and device IDs collected for
  app functionality and account management; precise location and photos
  (attendance check-in only, never in the background); data is encrypted in transit;
  nothing is shared with third parties (Firebase delivers pushes as a service
  provider, and the pushes carry no client data); users can ask the firm to
  delete their login. Also fill in the *Financial features* declaration
  (back-office tool for a stock broker; no lending), the content rating
  questionnaire and the target audience (adults; not for children).
- **Encryption export**: `ITSAppUsesNonExemptEncryption` is set to `false`
  (the app uses only the operating system's standard encryption, HTTPS and the
  keychain).
- **Privacy policy URL**: both stores need one; use the provider's or the
  firm's.

## What the server does

All of this is in Ledgerline (Go), shared with the client app:
`POST /v1/mobile/devices` registers a phone after a password sign-in;
`POST /v1/mobile/unlock` opens a session with the device secret (idle time-out
5 minutes unless the firm sets another; password again after 30 days);
Admins revoke phones on Settings → Staff app; pushes go through FCM HTTP v1
and say only things like "3 approvals are waiting for you"; the details open
in the app after unlocking.
