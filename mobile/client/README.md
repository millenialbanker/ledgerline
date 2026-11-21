# A broker's client app (iOS and Android)

The client app is the broker's app for its investors: balance, holdings and
today's profit or loss on open positions, ledger, contract notes and
statements (open, save or share the signed PDFs), adding money (the firm's
SEBI-validated UPI IDs and QR code), payout requests, IPO applications, mutual
funds, pledging and authorising sales (CDSL TPIN), profile change requests,
the help desk, notifications and family accounts, and an *About us* menu
with what an Indian broker's app must show: SEBI registration numbers, member
codes, investor charter, risk disclosures, grievance contacts, and links to
SEBI SCORES and the Online Dispute Resolution portal (SMART ODR).

It is **white-label**: each broker publishes it under **its own** Apple and
Google developer accounts, with its own name, icon and colours. The screens
are phone pages served by the broker's Ledgerline server at `/app/` (built on
the client portal's calls); this folder is the native shell (Capacitor 7) and
`brand.json` drives the build.

**This code has not been compiled or signed in the Ledgerline repository**
(its build machine has no Xcode or Android SDK). Build it on a Mac as below.

## brand.json

| Field | What it is |
| --- | --- |
| `appName` | The name under the icon and in the stores (at most 30 characters). |
| `appId` | Bundle ID and Android package, a reverse domain you own: `in.yourbroker.invest`. Cannot change after publishing. |
| `version`, `build` | Shown version (`1.0.0`) and the build number (raise it for every upload). |
| `serverUrl` | Your Ledgerline server, `https://host` with no path. The app opens `serverUrl/app/?firm=<firm>`. |
| `firm` | Your firm code in Ledgerline. |
| `scheme` | Link scheme for opening the app from a link: `yourbrokerapp://open?screen=documents`. |
| `colors.primary`, `colors.background` | Splash and launch colours. The colours *inside* the app come from Settings → Client app. |
| `icon`, `splash` | `assets/icon.png` (1024×1024, no transparency) and `assets/splash.png` (2732×2732, logo centred). |
| `privacyPolicyUrl`, `supportEmail` | For the store listings. |
| `allowNavigation` | Other sites the app may open inside itself: CDSL's pages for pledge and sale authorisations. Keep the defaults. |
| `pinning` | Optional certificate pinning: see below. |

The name, colours and logo shown inside the app, and the *About us* details
(DP registration, registered office, grievance officer, investor charter and
risk disclosure links), are set in Ledgerline on *Admin → Settings →
Client app*; the SEBI registration and member codes come from *Settings*.

## Accounts the broker needs

- **Apple Developer Program**, enrolled as the broking **company** (D-U-N-S
  number; US$99 a year). Apple requires apps offering financial services to be
  submitted by the regulated company itself (App Review Guideline 5.1.1(ix)),
  so the broker's own account, not the software provider's.
- **Google Play Console**, an organisation account in the company's name
  (one-time US$25; D-U-N-S number, identity checks). A new *personal* account
  must run a closed test with 12 testers for 14 days first; an organisation
  account need not. Google may ask for the SEBI registration certificate under
  its financial services policy.
- **Firebase** (Google), a project for push notifications (free). It can be
  the broker's own project, or the provider's with the broker's apps added.

## What you need on the Mac

- macOS 14 or later, **Xcode 16** or later, **CocoaPods** 1.15 or later
  (`brew install cocoapods`), **Node.js 20** or later.
- **Android Studio** Ladybug (2024.2) or later, with Android SDK Platform 35.
- An iPhone and an Android phone for testing.

## Build it

1. **Brand it.** Edit `brand.json`; put `assets/icon.png` and
   `assets/splash.png` in place.
2. **Install and make the native projects.**
   ```sh
   cd mobile/client
   npm install
   npm run configure          # capacitor.config.json from brand.json
   npx cap add ios
   npx cap add android
   npm run native             # native code, link scheme, Face ID text; safe to run again
   npx @capacitor/assets generate --iconBackgroundColor '#ffffff' --splashBackgroundColor '#ffffff'   # icons and splash from assets/
   ```
3. **Firebase.** Add an iOS app (bundle ID = `appId`) and an Android app
   (package = `appId`) to the Firebase project; put `GoogleService-Info.plist`
   and `google-services.json` in `mobile/client/firebase/` and run
   `npm run native` again (it copies them; they are not committed).
4. **APNs key.** Apple Developer → *Keys* → new key with *Apple Push
   Notifications service (APNs)*; upload the `.p8` with its Key ID and your
   Team ID in Firebase → *Project settings → Cloud Messaging → Apple app
   configuration*.
5. **iOS in Xcode.** `npx cap sync ios`, `npx cap open ios`:
   - *Signing & Capabilities*: your Team; **+ Push Notifications**;
     **+ Background Modes → Remote notifications**.
   - Drag `ios/App/App/GoogleService-Info.plist` into the *App* group (target
     *App*).
   - Version and Build from `brand.json`; then *Product → Archive →
     Distribute App → App Store Connect*.
6. **Android in Android Studio.** `npx cap sync android`,
   `npx cap open android`:
   - `android/variables.gradle`: `targetSdkVersion` at the level Google Play
     asks for now (35 or newer). `android/app/build.gradle`: `versionCode` and
     `versionName` from `brand.json`.
   - Upload key, once:
     `keytool -genkeypair -v -keystore invest-upload.jks -alias upload -keyalg RSA -keysize 2048 -validity 10000`
   - *Build → Generate Signed App Bundle*, upload the `.aab`, use Play App
     Signing.
7. **Turn it on in Ledgerline** (the server on https, `LEDGERLINE_PUBLIC_URL`
   set): *Admin → Connections → Push notifications (Firebase Cloud
   Messaging)* with the Firebase service account key; *Admin → Settings →
   Client app*: name, colours, logo, the *About us* details, and switch it on.
   Clients sign in with their portal login (staff give it from the client's
   page → *Portal access*).
8. **Try it** with a test client: sign in, choose Face ID or fingerprint,
   allow notifications; send a contract note to that client and the phone
   says "Your contract note is ready."

After a Capacitor update or a change to `brand.json`: `npm install`,
`npm run configure`, `npx cap sync`, `npm run native`.

## Certificate pinning (optional)

Set `pinning.enabled: true`, two `spkiSha256` values (your server
certificate's public key and a backup key you hold for the next renewal) and
`pinning.expires` (a date before which you will publish an update). Get a value:

```sh
openssl s_client -connect backoffice.yourbroker.in:443 -servername backoffice.yourbroker.in </dev/null 2>/dev/null \
 | openssl x509 -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | base64
```

iOS enforces it through App Transport Security (`NSPinnedDomains`); on Android
the app checks the server's key at start and closes on a mismatch (Android's
WebView does not apply pins itself). A wrong pin locks every client out until
an update reaches them: keep the backup key and renew with it.

## Store listing and what the stores ask

- **Sign-in for review.** Give Apple and Google a demo client login (a test
  account on your server with made-up data, or your UAT server) in the review
  notes. If two-factor sign-in is on for clients, give them a way through.
- **Who submits.** The broker, as the regulated company (see Accounts). Put
  the SEBI registration number in the description and in *About us*.
- **Minimum functionality (Apple 4.2).** The app unlocks with Face ID, keeps
  its credential in the keychain, gets push notifications, shares signed PDFs
  and blocks screen capture; say so in the review notes.
- **Account deletion (Apple 5.1.1(v)).** Accounts are opened through the
  broker's account opening, not in the app, so in-app deletion is not
  required; say in the review notes that clients close their account through
  the broker's closure process (SEBI rules), and that *More → My phones*
  removes the app's access.
- **Apple App Privacy**, for the client app:
  - *Contact info* (name, email, phone): collected, linked to the user, *App
    Functionality*.
  - *Financial info* (holdings, balances, transactions): collected, linked,
    *App Functionality*.
  - *Identifiers* (user ID; device ID = push token): collected, linked, *App
    Functionality*.
  - *Sensitive info*: none collected by the app (PAN and account numbers are
    shown masked; documents the client uploads for a change request are *Other
    data*, *App Functionality*).
  - Tracking **No**; third-party advertising **No**; analytics **No**.
- **Google Play Data safety**: personal info (name, email, phone), financial
  info (purchase history, other financial info), app activity limited to the
  account, device IDs; collected for app functionality and account
  management; encrypted in transit; not shared with third parties (Firebase
  delivers pushes as a service provider; pushes carry no client data); users
  can ask for deletion through the broker. Fill in the *Financial features*
  declaration (stock broking and investment services, regulated by SEBI; no
  loans), content rating, and target audience 18+.
- **Encryption export**: `ITSAppUsesNonExemptEncryption` is `false`
  (standard OS encryption only).
- **Privacy policy URL** (`privacyPolicyUrl`) and a support contact are
  required by both stores.

## Security in short

- First sign-in: the portal user ID, password and (if the firm requires it)
  two-factor code. After that the app unlocks with the phone's biometrics; the
  device secret is in the keychain or keystore behind them, and the server
  keeps only its hash.
- The app locks after the firm's idle time (5 minutes unless set otherwise)
  and when it comes back from the background; the password is asked again
  every 30 days and after a password or two-factor reset.
- No screenshots or recording on Android; on iOS the screen is hidden in the
  app switcher and while it is recorded or mirrored.
- Pushes say only that there is something to see ("Your contract note is
  ready"); nothing about the account is in a push, a log or the phone's
  storage outside the keychain.
- A rooted or jailbroken phone gets a warning.
