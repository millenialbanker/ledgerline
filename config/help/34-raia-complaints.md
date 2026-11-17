Ledgy help articles: the complaints tables of each business, and impersonation. Format is described in internal/help/help.go.

# Investor complaints

## raia-complaints-tables
title: Publish the complaints tables of the broker, DP, RA and IA
screen: helpdesk
roles: Compliance officer, Ops maker, Ops checker
ask: research analyst complaints data on the website; investment adviser complaints on the home page; ra ia complaints table; annexure e complaints format; annexure c complaints format; complaints due to impersonation line; dp complaints data on the website; which business a complaint concerns
related: complaints, impersonation-register, periodic-work

Each business the firm has publishes its own table of complaints by the 7th of the following month, in its own format:

- **Stock broking**: Annexure B of SEBI/HO/MIRSD/MIRSD-PoD1/P/CIR/2025/22 (rows for investors, SCORES 2.0, stock exchanges and other sources; pending split under and over 3 months).
- **Depository participant**: rows for investors, SCORES, depositories and other sources. The DPs' own annexure could not be read, so the screen says so: confirm the rows before publishing.
- **Research analyst** (Annexure E of the RA master circular of 6 Feb 2026) and **investment adviser** (Annexure C of the IA master circular): no stock exchanges row, and a line for the complaints received because some other entity impersonated the RA or IA. An IA also shows it on its home page without scrolling, in font size 12 or above, within 7 days of the month's end.

On [[helpdesk|Investor complaints]] → **Monthly disclosure**, choose the month and the business. **CSV** gives the tables; **For the website (HTML)** gives them as a section in 12-point type for the web team to place. Each table has the monthly trend and the annual trend with their grand totals.

**Mark each complaint's business.** When you record a complaint, choose the business it concerns and tick **Received because someone impersonated the firm** when it is so. A complaint about the DP is the DP's unless you choose otherwise. A research analyst's or investment adviser's complaint, or one caused by impersonation, may come from someone who is not a client: leave the client empty and give their name and contact. To change it later, open the complaint and **Save** the business.

The annexures' note lets an RA or IA leave the impersonation complaints out of the totals (they are closed after the due process). The compliance officer chooses that under Impersonation → **Reporting details**; the impersonation line still counts them.

The tables are made each month by **Investor complaints disclosure** on [[periodic|Periodic work]]: one file for each business. Publishing them on the website and app is the firm's step.

## impersonation-register
title: Record impersonation of the firm and report it each month
screen: helpdesk
roles: Compliance officer, Ops maker, Ops checker
ask: someone is impersonating our research analyst; fake app using the firm's name; report impersonation to bse; raia inspection report impersonation; impersonation nil report; fir for impersonation; takedown of a fake website; impersonation of the dp to nsdl
related: raia-complaints-tables, complaints, periodic-work

When someone uses the firm's name, an officer's name or its research or advisory name on a website, an app or a social media handle, record it on [[helpdesk|Investor complaints]] → **Impersonation** → **Record a case**:

1. Which of the firm's businesses was impersonated, who, when and where it was found (website, app, social media), the link or handle and the name the impersonator used.
2. **FIR**: the police station, number and date. File it with the police or the cyber police; for a case that came from a SCORES complaint, enclose the FIR on SCORES.
3. **Takedown requests**: each request to the platform, the app store or the authority (MeitY for websites), with its status.
4. **Notice on the website**: the home page names the entity and the police station where the FIR was filed.
5. **Clients alerted**: when and how clients were told to check offers through the official website, handles and customer care.
6. Keep the FIR copy and screenshots with the case (PDF, PNG or JPEG up to 5 MB), and the impersonator's bank accounts, up to 5.

A complaint the case caused is linked to it from the complaint. The nightly [[selfinspect|Self-inspection]] flags an open case more than 7 days old without every step (7 days is Ledgerline's own measure).

**Monthly reports.** Under **Monthly reports**:

- **BSE, for a research analyst and an investment adviser** (notices 20250804-59 and 20250804-58): within 7 days of the month's end, on BSE's IA/RA Membership Portal → RAIA Inspection → Report Impersonation. Download the month's **Report**, enter it on the portal and **Record the submission** with the acknowledgement. With no case in the month, **Record the nil check**: where the firm looked and when. BSE does not say whether a nil entry is needed: record the check each month.
- **The depository, for the DP** (NSDL/POLICY/2024/0048): by the 10th, each case emailed to dp_igreport@nsdl.com with the DP ID, DP name, particulars, action taken and status. The report has those columns. NSDL needs no nil report. CDSL's communiqué DP2024-234 was not read: confirm with CDSL how it takes the report.

Enter the RA and IA registration numbers and the DP IDs under **Reporting details**. The reports are tasks on [[periodic|Periodic work]]: **Impersonation cases to BSE** (only for a firm with RA or IA registrations, set in Settings → Firm profile) and **Impersonation of the DP**.
