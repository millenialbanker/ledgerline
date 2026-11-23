Ledgy help articles: the KYC trading gate. Clients whose KYC the KRA has not verified, the exchanges' and CDSL's KYC lists, the KRA upload clock, CDSL's KYC and mobile freezes, and the onboarding of clients resident outside India (PROI).

# KYC trading gate

## kyc-trading-gate
title: KYC trading gate: who may not trade because of the KYC
screen: kycgate
roles: Compliance officer, Ops checker, Ops maker, Risk manager
ask: kyc trading gate; kra not validated; kyc not validated block; kra status blank; kyc pending block; block clients kra on hold; kyc registered kyc validated only; exit only kyc; clients before 31 march 2024; square off kyc not validated; not permitted to trade kyc; why is the client blocked for kyc; switch off kyc block
related: kyc-lists, kra-upload-clock, cdsl-kyc-freeze, proi-onboarding, firm-practices

A client may trade only once the KRA has verified the KYC: status **KYC Registered** or **KYC Validated** (SEBI's KYC master circular, paras 83 and 99; SEBI/HO/MIRSD/SECFATF/P/CIR/2024/41; NSE/ISC/75724; MCX/S&I/522/2026). [[kycgate|KYC trading gate]] (Compliance) shows every active client the KYC rules hold back, and why.

The gate is **on by default**. With it on, Ledgerline blocks on the front ends (the Finspot block file and API, BOW trading rights, every limit file):

- a client whose KRA status is anything but Registered or Validated. **A status never loaded counts as not verified**, so load the KRA status file on [[kyc|KYC & PMLA]] every day;
- a client on any of the exchanges' KYC lists (see "Loading the exchanges' KYC lists").

How hard the block is:

- **Exit only**: a client onboarded on or before 31 March 2024 may still close positions (SEBI's FAQ on the KRA validation circular). Fresh orders are blocked; the limit is zero.
- **No trading**: everyone else, and any client an exchange has made Not Permitted to Trade from a date now passed, cannot square off either. Finspot gets block type ALL; BOW trading rights are taken away. A client without an account opening date is treated as a new client: record the date to allow an exit.

The block lifts by itself when the KRA status is loaded as Registered or Validated and no list holds the client. Accounts flagged Inactive, blocked or not active are blocked either way.

MCX's KYC hold list bars square-off too: open positions run to expiry, so the screen shows each held client's open positions. A client whose KRA status is not verified is also sent to MCX's UCC as inactive (status 2) until it is (MCX/S&I/176/2026).

Switching the gate off is a firm practice on [[practices|Firm practices]]. SEBI does not let an unverified client transact, so the self-inspection reports the gate switched off.

## kyc-lists
title: Loading the exchanges' and CDSL's KYC lists
screen: kycgate
roles: Compliance officer, Ops checker, Ops maker
ask: kra not available file; tmcode kra not available; non validated clients by kra; nptt file; not permitted to trade file; enit downloads kyc; bse non validated clients by kras; bse pan on hold invalid not available at kra; mcx kyc on hold list; cdsl kra deficient list; billing folder kra; load kyc list; lift kyc hold
related: kyc-trading-gate, cdsl-kyc-freeze, deceased-clients

Each month the exchanges, and each cycle CDSL, publish lists of the firm's clients whose KYC is not in order at the KRA. Load each on [[kycgate|KYC trading gate]] → **Lists** (or drop the file on [[xfiles|Exchange files]]):

- **NSE, ENIT > Log > Downloads**: <TMcode>_KRA_NOT_AVAILABLE (PANs not uploaded to a KRA: upload them), <TMcode>_Non-Validated_Clients_by_KRA (KYCs on hold: get them validated) and the cycle's **NPTT** file (PANs Not Permitted To Trade: no trading and no square-off until the KRA marks them compliant; reinstated the next day). NSE/ISC/69627 and the monthly cycles (latest NSE/ISC/77375).
- **BSE**: the monthly Non_Validated_Clients_by_KRAs file, and BSE UCC's lists of PANs on hold, invalid or not available at the KRA (BSE notice 20251111-43 and its monthly successors).
- **MCX**: the monthly KYC On Hold list sent by e-mail (MCX/S&I/522/2026): no fresh trades and no square-off on MCX.
- **CDSL**: the DP-wise list of BOs with KRA-deficient KYC in the billing folder (CDSL/OPS/DP/POLCY/2026/325 and the monthly series). It is about demat accounts, not trading: see "CDSL freezes for KYC and revoked mobiles".

When loading, give the list's date and, if the circular gives one, the date from which the exchange stops the PAN (or CDSL freezes). None of these files has a published layout: Ledgerline finds the columns by their header names (PAN, client code, BO ID) and matches by PAN, then the client code. A file whose header it cannot read is refused with what is missing.

Each client on a list is **held** (shown on the Clients held back tab) and the compliance officer gets one work item per list, with the date to act by. The next list of the same kind that leaves a client out lifts that hold. Once the KRA shows the KYC as Registered or Validated, the compliance officer can also **Lift** a hold by hand, saying what was checked. After 30 days on a list, the self-inspection reports the client.

## kra-upload-clock
title: KRA upload within 3 working days
screen: kycgate
roles: Compliance officer, Ops checker, Ops maker
ask: kra upload 3 working days; kyc upload deadline; kyc not uploaded to kra; kra upload late; upload kyc to kra; modified kyc kra upload; kra upload file mark uploaded
related: kyc-trading-gate, fatca-crs

The KYC of every new client, and every change of KYC details, goes to the KRA within **3 working days** (SEBI's KYC master circular, para 84; for clients resident outside India, the PROI circular para 5.7).

[[kycgate|KYC trading gate]] → **KRA uploads** lists the KYCs done or changed in the last 30 days with the date due and the date uploaded: the day the KRA file holding the client was marked uploaded on [[kyc|KYC & PMLA]]. A new client whose KRA status is already loaded counts as at the KRA.

Each business day, a work item asks the compliance officer to upload each KYC due that day or late; it closes when the file is marked uploaded. The self-inspection **KYC uploaded to the KRA within 3 working days** reports late uploads.

## cdsl-kyc-freeze
title: CDSL freezes for KYC (reason 27) and revoked mobiles (reason 31)
screen: kycgate
roles: Compliance officer, Ops checker, Ops maker
ask: cdsl reason code 27; kyc non compliant freeze; freeze demat kyc not validated; joint holder kyc not verified; cdsl reason code 31; mnrl freeze demat; 30 day notice mobile; code 21 freeze moved to 31; fri non compliant; unfreeze after kra validation
related: kyc-lists, revoked-mobiles, kyc-trading-gate

**KYC.** Every demat account where any holder's KYC (joint holders included) is not verified by a KRA is frozen for debit and credit under **reason 27**, KYC non-compliant (CDSL/OPS/DP/POLCY/2026/234 and 2026/770). [[kycgate|KYC trading gate]] → **Demat accounts** lists each such CDSL account of the DP with every holder's KRA status (a joint holder's comes from the KRA status file), and whether CDSL's freeze master shows the reason-27 freeze. **Raise freeze requests (27)** makes a DP request for each account not frozen or requested; a checker approves it on DP requests and the DP desk sets it in CDAS. Once the KRA verifies every holder, unfreeze it by the prescribed procedure.

**Revoked mobiles.** A holder whose mobile is on DoT's revocation list (see [[mnrl|Revoked mobiles]]) and does not respond gets a **30-day notice** to register a valid mobile, saying the account will be frozen for debits. Record the notice (**Notice sent**). When it runs out with the number still not changed, a work item asks for the **debit freeze under reason 31** (MNRL/FRI non-compliant), raised with **Freeze (31)** (CDSL/OPS/DP/POLCY/2025/618).

**Code 21.** Freezes made earlier under code 21 for an invalid mobile are moved to code 31 in CDAS. The screen lists every freeze still under code 21 from CDSL's freeze master.

Two self-inspection tests check this: accounts with a holder not verified are frozen under 27 (or requested in the last 5 days), and revoked-mobile matches on CDSL accounts have a notice within 30 days, a freeze when it runs out, and no code-21 freeze left.

## proi-onboarding
title: Clients resident outside India (PROI): NRI, OCI, foreign national
screen: kycgate
roles: Compliance officer, Ops checker, Ops maker
ask: proi kyc; person resident outside india kyc; nri onboarding 3 december 2026; oci kyc; foreign national kyc; video ipv nri; vipv geo tag; passport kyc nri; overseas address proof; ckyc id nri; international mobile; nro category 37; nre category 11; nri ucc category
related: kyc-trading-gate, kra-upload-clock, nri-client

From **3 December 2026**, an individual client resident outside India (an NRI, an OCI cardholder or a foreign national) is onboarded, and re-KYCed, by SEBI's PROI process (SEBI HO/38/30/12(3)2026-MIRSD-POD2/I/25175/2026, forwarded by NSE/INSP/77335 and CDSL DP2026-780). Record it on [[kycgate|KYC trading gate]] → **PROI onboarding** → **Record**:

- the KRA and CKYCRR records **looked up before the documents were collected**, and the CKYC identifier the client gave (or None);
- the PAN verified with the Income Tax database;
- the passport number and expiry, the OCI card for an OCI, and the overseas address with the document proving it;
- the e-mail verified by a one-time code, and whether the mobile is verified. An Indian or an international mobile (with + and the country code) may be given;
- the in-person verification: physical, with the official and the date and time; or video IPV with consent, random actions, a live GPS geo-tag, VPN and proxy blocked, the client's IP in India or the KYC country (a FATF-compliant one), liveness, a face match with the passport photo, end-to-end encryption, an authorised official present and the concurrent audit;
- attested English translations of documents not in English, or Not needed.

The client cannot be activated until the record is complete. While the mobile is not verified, or is international, SMS and WhatsApp messages go to the verified e-mail instead.

**NSE category.** Category 11 means NRE only from 22 November 2025; an NRO account holder is **category 37** (NSE/ISC/71043). Record the account type on [[nri|NRI (PIS)]]: the NSE UCC goes with the right category, and a change goes as a modification.
