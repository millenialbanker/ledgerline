Ledgy help articles: clients' FATCA/CRS self-certifications, sent to the KRA with the KYC.

# FATCA and CRS

## fatca-crs
title: Record clients' FATCA/CRS self-certifications and send them to the KRA
screen: kyc
roles: Ops maker, Ops checker, Compliance officer
ask: fatca; crs; fatca declaration; crs self certification; tax residency; tax resident of india only; foreign tax residency; tin; tax identification number; us person; fatca at kra; fatca not provided at kra; fatca columns in kra file; change client tax residency; controlling persons; form 166 data; validity indicator
related: fatca-legacy, kra-status, change-contact

Every client gives a FATCA/CRS self-certification when the account is opened, and the firm uploads it to the KRA with the KYC (SEBI/HO/MIRSD/SECFATF/P/CIR/2024/12 of 20 February 2024; CVL KRA communique CVL/OPS/INTERM/GENRL/24-200: with the KYC upload from 1 August 2024). The firm checks that it is reasonable and updates it when the client reports a change. Since 1 April 2026 the rules are section 508 of the Income-tax Act 2025 and Rules 238-240, with Form 166 in place of Form 61B (SEBI circular HO/38/14/(18)2026-MIRSD-POD2/I/25560/2026 of 11 November 2026).

**What is recorded:**

- **Tax residency.** *India only*, or each other country (up to 4) with its TIN, or the reason there is none: A (the country issues no TINs), B (the person can get one but has not yet; say why) or C (the country's law does not ask for it). The TIN-issuing country, when it is another country.
- **Place and country of birth** (of incorporation for a non-individual), **nationality**, and whether the client is a **US person** (a US citizen or resident for tax). Countries are ISO 3166 codes.
- **For an HUF or company: controlling persons.** Each one's name, role (CRS801 to CRS813: ownership, control by other means, senior managing official, settlor, trustee, protector, beneficiary and others), PAN, date of birth, tax residency and TIN.
- **For Form 166:** the validity indicator (a valid self-certification was obtained), the account type (custodial for a broking and demat account) and whether the account is new or pre-existing (new when opened from 1 January 2016). Ledgerline holds this data; it does not build Form 166.

**Opening an account.** The online application has a **Where you pay tax** step: an Indian resident ticks *I am a tax resident of India only* and gives the place of birth. The **New client** window has a **Tax residency** step. A client cannot be created without one.

**Changing it.** On [[kyc|KYC & PMLA]] → **FATCA / CRS**, click **Change** (or **Record**), fill in the new declaration and attach the client's signed and dated self-certification. A client can also change it from the portal (Profile & documents → Tax residency). Either way it is a change request: an Ops checker who did not raise it approves it on [[changes|Change requests]]. It then goes to the KRA as a modification in the next KRA upload file. TINs are shown masked; leave a masked TIN as it is to keep it.

**Warnings to resolve.** Each declaration is checked against the KYC details: an address, phone or nationality outside India when the client declared India only; a US place of birth or citizenship with *not a US person*; an HUF or company with no controlling persons; a foreign country with no TIN and no reason. **Resolve** records what was checked (an Ops checker or the compliance officer). A missing TIN or a declaration marked not valid is cleared only by a corrected declaration.

**The KRA upload file.** **KRA upload file** on KYC & PMLA keeps its twelve columns and adds the FATCA/CRS columns after them. Their names follow KFintech KRA's circular KARVY/OPS/INTERM/GENRL/24-07 of 11 June 2024: Place of Birth, Country of Birth, Tax Residency other than India? (Y or N), Country of Tax Residency1-4, Tax Identification Number1-4, Date of Declaration (dd-mm-yyyy). The TIN exemption reason and issuing country, nationality, US person, validity, account type, new or pre-existing and the number of controlling persons follow with Ledgerline's own names: no KRA has published them yet, so confirm them with your KRA (the layout is kra-kyc-upload on [[xfiles|Exchange files]]). Controlling persons go in their own file, one line a person (**Controlling persons file**).

**At the KRA.** Each KRA file you download is listed on the FATCA / CRS tab. Once the KRA has taken it, **Mark uploaded** with the KRA's reference: its certifications count as at the KRA. If the KRA's status file has a **FATCA** column (Y on record, N not), loading it on [[files|Files]] records it for each client.

## fatca-legacy
title: Clients onboarded before August 2024: the FATCA/CRS backlog
screen: kyc
roles: Ops maker, Ops checker, Compliance officer
ask: legacy fatca; old clients fatca; fatca backlog; fatca for existing clients; pre august 2024 clients fatca; fatca bulk upload; load fatca from old system; fatca-crs file; non individual fatca legacy
related: fatca-crs, migrate-masters

Certifications taken before FATCA/CRS went to the KRAs had to be uploaded too: within 90 days under SEBI/HO/MIRSD/SECFATF/P/CIR/2024/12 (para 4); KFintech KRA asked for legacy individual clients (onboarded before 1 August 2024) by 31 October 2024 and said the process for non-individuals would be notified separately (KARVY/OPS/INTERM/GENRL/24-009).

1. On [[kyc|KYC & PMLA]] → **FATCA / CRS**, choose **Legacy (before Aug 2024)**. Clients opened before 1 August 2024, or migrated without an opening date, are listed with what is left to do.
2. **Collect the self-certification** where there is none. If the previous system or your paper files have them, load them on [[files|Files]] as **FATCA-CRS** (one row a client; rows that repeat the client code add controlling persons; *AtKRA* Y marks one the previous system already uploaded). A client who already has a self-certification keeps it.
3. **Legacy bulk file** makes the KRA file of the certifications on record, valid and not yet at the KRA, as modifications.
4. Upload it at the KRA, then **Mark uploaded** with the KRA's reference. Load the KRA's status file when it comes.

The nightly self-inspection test *FATCA/CRS of clients onboarded before August 2024 at the KRA* fails until every legacy client's certification is at the KRA.
