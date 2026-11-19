Ledgy help articles: signing contract notes and statements with a USB token at the office.

# Digital signature

## usb-token-signing
title: Sign documents with a USB token
screen: dispatch
roles: Admin, Compliance officer
ask: usb token; dsc on usb token; sign with usb token; dongle; epass2003; proxkey; mtoken; watchdata; hyperpki; token signer; ledgerline signer; signing agent; class 3 dsc token; token pin; signer not connected; usb token signer not connected; approve signer certificate; reset agent key; token removed; documents held signer offline; contract notes held token
related: dispatch-register

When the firm's Class 3 certificate is on a USB token (ePass2003, ProxKey, mToken, Watchdata, HyperPKI and the like), **Ledgerline Signer** signs with it. It is a small Windows program on the office computer the token is plugged into. It connects out to Ledgerline, so nothing has to reach into your office: Ledgerline sends it only a fingerprint (SHA-256) of each document, the token signs it, and Ledgerline checks every signature against the approved certificate before using it.

**Set it up** (Admin), on [[dispatch|Dispatch register]] → **Digital signature** → **USB token**:

1. Upload the token's public certificate (.cer or .crt, exported from the token's software) if you have it; it is then approved by you. Otherwise leave it out. **Save** shows the **agent key** once: copy it.
2. On the office computer: install the token's driver from its maker, plug the token in and check that its certificate shows in the token's software.
3. Get **ledgerline-signer.exe** from your Ledgerline provider. In Command Prompt run `ledgerline-signer setup -server <Ledgerline's address>`, paste the agent key, and type the number of the firm's signing certificate.
4. Start `ledgerline-signer` and enter the token **PIN once**. Keep the window open. The PIN stays in memory only; some token drivers still show their own PIN window.
5. If you did not upload the certificate, the signer sends it when it first connects. An Admin or the **Compliance officer** checks the name, issuer, serial and SHA-256 on the screen and clicks **Approve this certificate**. An expired or not-yet-valid certificate cannot be approved; one expiring within 30 days shows a warning.
6. **Check signing** signs a test document through the token and checks it.

**Every day**: keep the computer on and the token plugged in while the day's documents go out: contract notes by the end of the trading day, and statements when they are sent. The screen shows whether the signer is connected, when it was last seen, and the documents signed and failed today.

**When the signer is not connected** (computer off, program closed, token pulled out): contract notes and statements are **held**, never sent unsigned. When the signer is back they are sent by themselves within a few minutes. After 10 minutes away during working hours, the Admin gets a work item; it closes when the signer reconnects.

- **A new or renewed token**: a different certificate waits for the same approval; nothing is signed with it until then.
- **Agent key lost, or the computer replaced**: **Reset agent key** stops the old key at once; set up the signer again with the new one.
- `ledgerline-signer -selftest` signs a sample with the token and checks it, without connecting to Ledgerline.
