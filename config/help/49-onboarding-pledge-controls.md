Ledgy help articles: onboarding checks beyond the client master (purpose, video IPV, persons with disability, HUF and NPO details for CDSL, exchange opt-outs, MCX rules, second codes, the firm's own codes), online account opening from abroad, pledge controls (letters, reconciliation, lock-ins, the 9:45 cut-off, charges at cost), the collateral register, and client code modifications on ENIT.

# Onboarding checks and pledge controls

## onboarding-checks
title: Onboarding checks: what account opening asks for beyond the client master
screen: kycgate
roles: Compliance officer, Ops checker, Ops maker
ask: purpose of relationship; intended nature of business relationship; video ipv record; vipv trained official; vipv recording; random questions vipv; person with disability kyc; guardian signature disability; liveness alternative; blink liveness; huf coparceners cdsl; karta details cdsl; disability flag cdsl; darpan id; npo demat; opt out of exchange; negative consent exchange; board resolution mcx; corporate commodity board resolution; sikkim mcx email; second ucc same pan; two client codes same pan
related: proi-onboarding, ucc-code-rules, kyc-trading-gate, proi-online

[[kycgate|KYC trading gate]] → **Onboarding checks** lists every open client with something missing. Press **Record** on the client (or type a client code and **Open a client**) and fill in what applies:

- **Purpose of the relationship**: why the client opens the account, and what for (SEBI KYC master circular, para 12). Online applicants choose it on the profile step.
- **Video IPV** of a resident client (when the IPV was by video): the trained official and the training date, the client's informed consent, the date and time, the location (in India), where the recording is kept and its SHA-256 fingerprint, the random questions asked, the OVD the client showed, the photograph matched, and the liveness check.
- **Person with disability**: the disability as CDSL records it (MDP, OTH or NO), the guardian who signs where needed (with PAN, and his own KYC at a KRA), and how liveness was checked: blinking, or an alternative such as a facial expression, a head nod or reading an OTP shown on screen (NSE's FAQ on account opening by persons with disability).
- **HUF**: the Karta and each coparcener (name, CDSL relationship code, date of birth, PAN). They go to CDSL in the harmonised BO upload; the fixed-width file has no line for them.
- **Non-profit organisation**: the NITI Aayog DARPAN ID. A CDSL account of an NPO (status 29 sub 256, or 25 sub 265) cannot be set up without it.
- **Opt-out from an exchange**: the date and the signed letter. No UCC is sent to that exchange while it stands.
- **MCX**: a corporate's board resolution for commodity derivatives (MCX/INSP/255/2026), and for a Sikkim address the date you e-mailed the identity proof, address proof and KYC to surveillance@mcxindia.com (MCX/S&I/251/2026). A daily work item reminds you of the e-mail.
- **Second code for the PAN**: why a second active code on the same exchange is allowed. Only a compliance officer can approve it.

Items marked **stops activation** keep a checker from activating the client: a video IPV that is incomplete, a person with mental disability without a guardian who signs, a corporate on MCX without its board resolution, a second code not approved, and a client code that is one of the firm's own.

## ucc-code-rules
title: Client codes the firm may not use, and the ERROR account
screen: kycgate
roles: Compliance officer
ask: client code same as tm code; ucc equals trading member code; own ucc; error ucc; error account; designated error account; proprietary code client code clash; ncl circular 201/2025; deactivate colliding ucc
related: onboarding-checks, ccm-enit

No client code may equal the firm's trading member code on any exchange, OWN, a code beginning with ERROR, or any of the firm's proprietary codes (NSE Clearing NCL/CMPT/68965, circular 201/2025). Ledgerline refuses such a code when an account is opened, skips it when it numbers a new client, and stops activation of a client who already has one.

Set the firm's codes on [[kycgate|KYC trading gate]] → **Onboarding checks** → **Set the firm's codes**: the one designated **ERROR account** (NSE/SURV/74008 asks for a single active ERROR account) and any other proprietary codes. The trading member codes come from [[settings|Settings]].

A client found with a colliding code gets a work item: deactivate the code at the exchanges and register a fresh one. The self-inspection tests `ucc_code_collision` and `error_account` check both.

## proi-online
title: Opening an account online from abroad (NRI, OCI)
screen: kycgate
roles: Compliance officer, Ops checker, Ops maker
ask: nri open account online; oci online account opening; mobile outside india application; international mobile online kyc; passport upload online; overseas address online; proi online application
related: proi-onboarding, onboarding-checks

An NRI or an OCI cardholder can apply on the online account-opening page:

- the mobile may be outside India, given with + and the country code. Our SMS may not reach it, so the e-mail code alone confirms the contacts; messages then go to the e-mail until the mobile is verified;
- on the identity step the applicant says where he lives. The PAN is checked and the KRA searched there, before any document is collected;
- the address step takes the country, the passport (and the OCI card for an OCI), the overseas address and a document proving it, all uploaded, instead of Aadhaar.

The client is created as an NRI with the PROI record filled from the application. Staff then record the CKYCRR search, the video IPV and the documents' checks on [[kycgate|KYC trading gate]] → **PROI onboarding**, including, from 3 December 2026: what more than the KRA/CKYCRR record was collected and why, the passport copy and its verification, the CERSAI form (a digital form needs the wet signature shown in the video IPV), the OVDs taken as proof of identity and address, how the overseas address was verified, and how the copies were verified (originals seen, or an allowed alternative). The client is not activated until it is complete.

## pledge-controls
title: Pledge controls: letters, daily reconciliation, lock-ins, the 9:45 cut-off, charges
screen: cdslpledge
roles: Risk manager, Ops checker, Ops maker, Compliance officer
ask: pledge setup letter; pledge set-up letter to pledgor; daily pledge reconciliation; pledge received executed pending; maker checker pledge reconciliation; lock-in invocation; locked in shares invoke; confiscation cum early pay-in 9:45; cuspa early pay-in cut-off; pledge charges actual cost; unpledge charges tariff; invocation charges tariff
related: cdsl-pledge-apis, cdsl-pledge-invocation, collateral-register

[[cdslpledge|Pledges (CDSL)]] → **Controls**:

- **Reconciliation**: the day's margin pledges, re-pledges, pledges to lenders, unpledges and invocations received, executed and not done, the ones still open, and the maker entries against checker verifications (CDSL operating instructions 8.8). After 6 p.m. a work item lists requests left open from earlier days.
- **Pledge set-up letters**: a signed letter to the pledgor with the PSN for each pledge set up (8.5.2). Press **Send letters**; they are signed and e-mailed like statements, or printed for clients on paper.
- **Locked-in securities**: lock-in balances come from CDSL's statement of holding (or **Record a lock-in**). Locked-in shares are left out of what may be invoked, and an invocation of them is held until the lock-in ends.
- **Confiscation-cum-early pay-in**: unpaid securities sold on a day are confiscated at CDSL by 9:45 a.m. on the pay-in day; after that CDSL acts only on a best-effort basis (CDSL master circular 4.10.2.1). A 9 a.m. work item lists the ones due.
- **Charges at actual cost**: record CDSL's charge for a margin pledge, unpledge, re-pledge and invocation. The firm tariff and every DP plan are checked against it, as NSE's inspection FAQ asks.

## collateral-register
title: The collateral register for inspection
screen: cdslpledge
roles: Risk manager, Compliance officer, Ops checker, Ops maker
ask: collateral register; register of collateral; collateral received acknowledgement; collateral deposited with clearing corporation; collateral returned to client; collateral statement by type; cash fdr bank guarantee securities collateral statement
related: pledge-controls, cdsl-pledge-apis

SEBI's stock broker master circular (para 25.1.2) asks for records of collateral received with the client's acknowledgement, the client's authorisation to deposit it with the clearing corporation, the deposits made, the returns to the client and corporate action benefits credited. Download the **collateral register** from [[cdslpledge|Pledges (CDSL)]] → **Controls**: choose the dates and press **Download register (CSV)**. Each line has the date and time, client, event, reference, PSN, security, quantity, status, acknowledgement or authorisation, maker, checker and the set-up letter.

The daily margin statement carries the **collateral by type** (para 25.1.4): cash, fixed deposits, bank guarantees and securities, each deposited, utilised and available, with the status. The day's margin is met from cash first, then from securities.

## ccm-enit
title: Reporting client code modifications on ENIT
screen: recon
roles: Ops checker, Compliance officer
ask: ccm reason code enit; client code modification reason code; enit compliance ccm; register of client code modifications; nse surv 74008; minimise client code modifications
related: ccm, ucc-code-rules

NSE/SURV/74008 (circular 313/2026) asks for client code modifications to be kept to a minimum and each one reported on ENIT (Compliance) with a reason code. After a modification, a work item asks the checker to report it. On [[recon|Trade reconciliation]] → **Client code modifications**, press **Record ENIT code** on the modification and enter the reason code you chose on ENIT and the date. **Register (30 days)** downloads the register of modifications with the codes. The self-inspection test `ccm_enit` lists modifications not reported.
