Ledgy help articles: the staff app and the client app on phones, push notifications, and lost phones.

# Mobile apps

## staff-app-setup
title: Switch on the staff app and set up phones
screen: staffapp
roles: Admin, Compliance officer
ask: staff app; mobile app for staff; phone app for staff; approve on my phone; approvals on phone; set up staff app; staff app qr code; who can use the staff app; give staff app access; staff app not working for me; you are not on the staff app's list; staff app switched off; install the staff app; mobile approvals app; ledgerline app on iphone; ledgerline app on android
related: staff-app-using, mobile-lost-phone, push-notifications-fcm

The staff app puts approvals, the day's progress, alerts, the work queue, a client look-up and Ask Ledgy on staff phones (iPhone and Android). It is off until an Admin switches it on.

On [[staffapp|Staff app]] (Admin → Settings):

1. Tick **The staff app is on**.
2. Choose who may use it: **everyone with a role** (for example Ops checker), **everyone of a branch** (branch staff of that branch), and **named people**. Admin is not let in unless you tick the role or the person. Clients never use the staff app; they have the client app.
3. Set the times: **lock after** so many minutes without use (5 unless you change it), **ask for the fingerprint again** after so many seconds in the background, and **ask for the password again** every so many days (30). They apply to the client app too.
4. **Save.**

**Setting up a phone.** The person installs the staff app, scans the **QR code** on the Staff app screen with the phone's camera (it opens the app with this server's address and your firm code; it holds no password), signs in with their user ID, password and two-factor code, and chooses **Use Face ID** or **Use fingerprint**. From then on the phone unlocks the app with the face or fingerprint. The server must be on https.

**Push notifications** need the Firebase connection: see the related article. **Send a test push to my phone** checks it end to end.

The phone sees exactly what its owner sees on a computer: the same role checks, the same branch or partner scope, PAN and bank account numbers masked.

## staff-app-using
title: Approve, reject and follow the day on the staff app
screen: staffapp
roles: Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Authorised person, Admin
ask: approve on the staff app; reject on my phone; check in on the staff app; attendance on my phone; check out on phone; reason for rejecting on phone; approvals waiting on phone; staff app approvals tab; staff app work tab; staff app today tab; alerts on the staff app; find a client on my phone; ask ledgy on phone; push says approvals waiting; what does the staff app show; staff app amount
related: staff-app-setup, mobile-lost-phone

The staff app has five tabs:

- **Approvals**: actions Autopilot prepared that wait for your role (Admin sees all), with the amount where the item is about money. **Approve** carries out the prepared action; **Reject, with a reason** closes it with your reason. **Done today** lists what was approved or rejected. Your name and the time go into the audit trail, as on the desktop.
- **Work**: your work queue, overdue first. Items that need a screen are done on a computer; you can close one with a note.
- **Today**: alerts and exceptions (margin breaches, open reconciliation breaks, credits to identify, settlement exceptions, bounced contract notes, reports not uploaded, overdue work) and the day's steps with what each still needs. Branch staff and authorised persons see their own clients' exceptions only.
- **Clients**: find a client by name, UCC or PAN among the clients you may see. PAN and bank account numbers are masked.
- **More**: surveillance alerts (compliance officer and Admin), **Ask Ledgy**, **My phones**, sign out.

With a role the firm made or edited on [[roles|Roles]], the app shows only items of the screens your roles include, and approves or rejects an item only as that item's screen allows on the desktop.

**Attendance.** When your firm has the HRMS module on and HR has linked your login to your employee record, the Approvals tab starts with **Check in** (or **Check out**). Choose **Office**, **Home** or **Field** (for Field, say whom or where you are visiting), add a selfie if you like, and confirm with your face or fingerprint. Your location goes with the check-in or check-out only, never in between. The HRMS module decides, and if it refuses the app says why: not on the office network or at the office, working from home not allowed today, or a new phone that HR must approve first (attendance is taken from one registered phone per person).

**Notifications** say only what kind of thing is waiting ("3 approvals are waiting for you", "1 step of today is overdue"), never a client's name, code or amount. Tap one and unlock the app to see the details.

The app **locks** after the firm's idle time and when you come back to it after a while; unlock with your face or fingerprint. Screenshots and screen recording are blocked on Android; on iPhone the screen is hidden in the app switcher.

## client-app-setup
title: The client app: switch it on, brand it and publish it
screen: clientapp
roles: Admin, Compliance officer
ask: client app; mobile app for clients; investor app; our own app for clients; white label app; app under our name; publish the client app; client app colours; client app logo; client app name; app store listing for clients; play store app for clients; sebi registration in the app; investor charter in app; grievance contacts in app; scores link in app; odr link in app; client app not available yet
related: push-notifications-fcm, mobile-lost-phone

The client app is your firm's app for its investors, published under **your own name** in the App Store and Google Play. Clients sign in with their portal login (give it from the client's page → **Portal access**) and then unlock with their face or fingerprint.

In the app clients see their balance, holdings and the day's profit or loss on open positions, ledger, contract notes and statements as signed PDFs they can save or share, how to add money (your SEBI-validated UPI IDs and QR code), payout requests, IPOs, mutual funds, pledges and sale authorisations (CDSL TPIN), profile change requests (email, mobile, address), the help desk, notifications and family accounts.

On [[clientapp|Client app]] (Admin → Settings):

- **The app**: switch it on, give it a **name** (as under the icon), the **colours** and the **logo** (PNG or JPEG, at most 150 KB).
- **Shown under About us**: what an Indian broker's app must show. The SEBI registration as stock broker and the exchange member codes come from Settings; add the DP registration, registered office, support and **grievance** contacts, the compliance officer, and links to your **investor charter** and **risk disclosures**. SEBI **SCORES** and the **SMART ODR** portal are always linked.

**Publishing**: the app is built on a Mac from `mobile/client` in Ledgerline's code, with `brand.json` giving the name, bundle ID, icon and this server's address. You need your own Apple Developer and Google Play accounts in the company's name (Apple requires financial apps to come from the regulated company) and a Firebase project for notifications. The README there lists every step and the answers for the stores' privacy forms.

## push-notifications-fcm
title: Connect push notifications (Firebase Cloud Messaging)
screen: connections
roles: Admin
ask: push notifications; firebase; fcm; firebase cloud messaging; service account key firebase; notifications not arriving on phone; test push; apns key; push on iphone; push on android; notification says approvals waiting; push notifications india only mode; do notifications contain client data
related: staff-app-setup, client-app-setup

Both apps get notifications through **Firebase Cloud Messaging** (FCM), which reaches Android phones directly and iPhones through Apple's push service.

1. In the Firebase console, open (or create) the project the apps were built with, then **Project settings → Service accounts → Generate new private key**. A JSON file downloads.
2. On [[connections|Connections]], open **Push notifications (Firebase Cloud Messaging)**, paste the whole JSON file into **Service account key** and save. The project ID is read from the key.
3. For iPhones, the Firebase project also needs your Apple **APNs key** (Firebase → Project settings → Cloud Messaging); whoever builds the app does this once.
4. On [[staffapp|Staff app]], **Send a test push to my phone**.

**What a push says.** Only fixed words with at most a count: "3 approvals are waiting for you", "1 new alert needs review", "Your contract note is ready", "There is an update on your funds". Never a client's name, code, PAN, amount or scrip: the details open in the app after the phone is unlocked. That is also why push is allowed in **India-only mode**: nothing about a client leaves India.

Pushes wait in an outbox and go out within a minute; several of the same kind become one ("5 approvals are waiting"). A phone whose app was removed is forgotten automatically.

## mobile-lost-phone
title: A lost phone, signing out a phone, and how the apps stay safe
screen: staffapp
roles: Admin, Ops maker, Ops checker, Risk manager, Compliance officer, Branch staff, Authorised person
ask: lost phone; phone stolen; revoke phone; remove phone from app; sign out phone; staff left the firm phone; client lost phone; biometric unlock; face id; fingerprint unlock; app asks for password again; app locked; rooted phone warning; jailbroken phone; screenshots blocked in app; my phones; device registered; unlock failed
related: staff-app-setup, client-app-setup

**A lost or stolen phone.** On [[staffapp|Staff app]], **Staff phones** lists every registered staff phone; an Admin presses **Revoke** and gives the reason. The phone stops working at once: its sessions end, it cannot unlock again and its notifications stop. For a client's phone, enter the client's login on [[clientapp|Client app]] → **Show that client's phones**, or the client removes it in the app (More → My phones). Anyone can remove their own phones from **My phones** in the app.

**How the apps stay safe:**

- The first sign-in on a phone is the full sign-in: password and two-factor code for staff, the portal sign-in for clients. Only then is the phone registered. The phone keeps a secret in its keychain (iPhone) or keystore (Android) that only the owner's face or fingerprint releases; the server keeps only a fingerprint of it.
- The app locks after the firm's idle time and when it comes back from the background, and asks for the password again every 30 days (Settings → Staff app). Five wrong unlocks revoke the phone and are recorded as a security event.
- A password or two-factor reset by an Admin, a deactivated user, or taking someone off the staff app's list stops their phones too.
- Screenshots and screen recording are blocked on Android. iPhones cannot block screenshots; the app hides its screen in the app switcher and while the screen is recorded or mirrored.
- A phone that looks **rooted or jailbroken** gets a warning, and its registration is recorded as a security event.
- At most five phones a person: registering a sixth removes the oldest.
