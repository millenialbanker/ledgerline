Ledgy help articles: the HRMS module (the People group): switching it on, the employee master, joiners and leavers tied to access, certificates and own trading, attendance by phone, kiosk and file, hybrid work, leave, and self-service.

# People (HRMS)

## modules
title: Switch the HRMS and Payroll modules on or off
screen: modules
roles: Admin
ask: switch on hrms; turn on payroll; modules; enable hrms module; people module; where is the people menu; hr module not showing; payroll module price; switch off hrms; settings modules; hrms not visible
related: staff-master, payroll-run

HRMS and Payroll are priced separately from the edition. Open [[modules|Modules]] (Admin → Settings), tick **HRMS** and, if you want it, **Payroll** (it needs HRMS), and click **Save**.

- With HRMS on, the **People** group appears in the sidebar: My HR, Staff, Joiners & leavers, Certificates & own trading, Attendance, Leave, HR policies and Payroll.
- With a module off nothing of it shows and its addresses answer “not found”. Its records are kept: switching it on again brings everything back.
- Your Ledgerline provider can switch a module for you from Ledgerline Control; the change arrives within about 15 minutes. A module is billed for each month it was on.

Only an Admin switches modules.

## staff-master
title: Employee master: a staff member’s record
screen: people
roles: HR manager, Admin
ask: employee master; staff master; hr record of an employee; create employee record; staff record; employee details; edit employee; employee code; designation; department; reporting manager; employment type; salary bank account of employee; emergency contact; uan of employee; esic number; aadhaar of employee; staff documents; document expiry
related: staff-reveal, joining-checklist, org-chart

Open [[people|Staff]] and click **Add employee**.

1. Fill in the name, department, designation, office, manager, date of joining and employment type. Leave the code blank to get the next number.
2. Add the identifiers: PAN, the **last 4 digits** of Aadhaar only (Ledgerline does not keep the full number), UAN, ESIC number and the salary bank account.
3. Tick the person’s **duties** (dealer in equity derivatives, DP operations, compliance officer…). Duties decide which certificates the person needs and what goes on the joining checklist.
4. Save. The joining checklist starts at once.

Open a person to **Edit**, **Add document** (with an expiry date if it has one; reminders go before it expires), **Add certificate**, **Link login** and see their **Attendance**.

A change of designation, department, office, duties or manager raises an access review (see the joiners article).

## staff-reveal
title: See a staff member’s full PAN, UAN or bank account
screen: people
roles: HR manager, Admin
ask: reveal pan of employee; full account number of employee; unmask uan; employee pan masked; see full esic number; why is pan hidden for staff; masked identifiers staff
related: staff-master

Staff identifiers are kept encrypted and shown masked, like client data. On a person’s record click **Reveal** next to the PAN, UAN, ESIC number or salary account and give the reason. The value shows once; your name, the field and the reason go to the audit trail.

Only HR managers and the Admin can reveal. Managers see their team without identifiers or pay. Aadhaar is never shown in full because only its last 4 digits are kept.

## org-chart
title: Org chart, team view and KMP list
screen: people
roles: HR manager, Compliance officer, Admin, Branch staff
ask: org chart; reporting structure; who reports to whom; my team; team members; branch staff list; key managerial personnel; kmp list; designated persons list; insider trading designated persons
related: staff-master

On [[people|Staff]], **Org chart** shows who reports to whom, from each person’s manager. Ledgerline refuses a manager that would make a loop.

What you see depends on who you are:

- HR managers, the compliance officer and the Admin see everyone.
- A manager sees the people who report to them, at any depth.
- A branch manager (named on the office) sees the staff of that branch only.
- Everyone else sees only their own record (My HR).

**KMP & designated persons** lists the people marked as key managerial personnel (with their position) and the designated persons under the insider trading code, with joining and leaving dates.

## joining-checklist
title: Joining checklist and access reviews
screen: hrlife
roles: HR manager, Compliance officer, Admin, Payroll
ask: joining checklist; new joiner checklist; onboarding employee; create ledgerline login for new employee; terminal request for new dealer; ctcl id for new dealer; approved user registration; background check; code of conduct signed; access review; role change access; employee moved branch; mover
related: leaving-employee, staff-certificates

Saving a new employee raises the **joining checklist** on [[hrlife|Joiners & leavers]]. Each item is a work item for the role that does it:

- the Ledgerline login, linked to the employee (Admin; ticked by itself when HR links the login),
- for a dealer: the trading terminal / CTCL user ID request and the exchange approved-user registration (compliance officer),
- the certificates the duties need,
- background check and the signed code of conduct (HR),
- own trading accounts and holdings declared on joining (compliance officer),
- with Payroll on: the salary structure, bank account and PF/ESI enrolment (Payroll).

Click **Done**, or **Not applicable** with the reason.

A change of designation, department, office, duties or manager raises an **access review**: review the login’s role and office, the terminals and registrations for a dealer, bank and CBX roles, and the certificates the new duties need.

## leaving-employee
title: An employee is leaving: approval, login switched off, exit work
screen: hrlife
roles: HR manager, Compliance officer, Admin
ask: employee leaving; resignation; record a leaver; exit checklist; deactivate login of leaver; switch off login on last day; withdraw terminal of leaving dealer; second approval for leaver; full and final; relieving letter; leaver not approved
related: joining-checklist, payroll-fnf

1. On the person’s record click **Record leaving**, with the last working day and the reason.
2. **Another** HR manager (or the Admin) approves it on [[hrlife|Joiners & leavers]]. The person who recorded it cannot.
3. On the last day, at the policy’s time (18:30 by default), Ledgerline switches the person’s Ledgerline login off by itself. If the leaving is approved after that time, the login goes off at once. If it is still not approved on the last day, the login stays on and HR gets a work item.
4. The leaving checklist sends work to the people who do it: withdraw terminals and exchange registrations (for a dealer, with the terminal IDs from the dealers register), collect devices, remove bank and CBX roles and signing authorities, disable email, handover, the holdings declaration on leaving, the exit interview and relieving letter, and with Payroll on the full and final settlement.

**Withdraw** cancels a leaving that has not happened: the person stays and the checklist closes.

## staff-certificates
title: NISM and other certificates, expiry reminders and flagged terminals
screen: hrcomp
roles: Compliance officer, HR manager, Admin
ask: staff certificate register; employee certificates; nism series vi for dp staff; certificate expiring soon; certificate reminders 60 30 7 days; terminal flagged; which certificate does each employee need; certificate register; staff certification
related: certificate-rules, staff-master

[[hrcomp|Certificates & own trading]] lists every certificate each person needs by their duties, with its number, validity and state: **Valid**, **Expiring**, **Expired** or **Missing**.

- Reminders go to the compliance officer 60, 30 and 7 days before a certificate runs out (the days are set on HR policies). Each new reminder replaces the last.
- A missing certificate raises a work item.
- An **expired** certificate raises a work item and, where the rule says so, **flags the person’s terminal**: the terminals from the dealers register are named. Withdraw or suspend the terminal until a valid certificate is on file.

Add a certificate on the person’s record (**Add certificate**). A person can add their own in My HR; it stays unverified until HR or compliance checks it.

## certificate-rules
title: Which roles need which certificate
screen: hrpolicy
roles: Compliance officer, HR manager
ask: certificate rules; which duty needs which nism; configure certificate requirement; add certificate rule; dp staff certificate; compliance officer certificate; research analyst certificate; change reminder days
related: staff-certificates

On [[hrpolicy|HR policies]], **Certificates each duty needs** is the firm’s register. Each row is a duty, the certificate, what the rule is based on, and whether an expired certificate flags the terminal. Ledgerline starts with the usual requirements (for example NISM Series VIII for equity derivatives dealers, Series VI for DP staff, III-A for the compliance officer, XV for research analysts) under the SEBI Certification of Associated Persons Regulations 2007 and the other regulations named. Change them to match your firm. A rule without **Based on** is refused.

## own-trading
title: Staff own trading: accounts, holdings and pre-clearance
screen: hrcomp
roles: Compliance officer, HR manager, Admin
ask: employee own trading; staff trading accounts; relatives accounts; outside broker account of employee; annual holdings declaration; pre-clearance; pre clear a trade; contra trade; restricted list; insider trading code staff; designated person trade approval
related: certificate-rules

The firm’s code for its people’s own trading (SEBI Prohibition of Insider Trading Regulations 2015, Schedule C, and the firm’s code of conduct) is set on [[hrpolicy|HR policies]]: who must pre-clear, above what value, how long a pre-clearance lasts, the contra-trade period, the restricted list, and when the annual holdings declaration is due.

- Each person declares their accounts, and their immediate relatives’, in **My HR**. An account with another broker goes to the compliance officer.
- Holdings are declared on joining, every year and on leaving. Missing annual declarations get a work item after the due month.
- A pre-clearance request goes to the compliance officer, who approves or refuses it. A security on the restricted list is refused at once; an opposite trade within the contra period is flagged. The person cannot approve their own.

## phone-checkin
title: Check in from the phone app or the console
screen: me
roles: Ops maker, Ops checker, Risk manager, Compliance officer, HR manager, Payroll, Branch staff, Admin
ask: check in; check out; mark attendance; punch in; punch out; attendance from phone; office check in refused; not on office network; geofence; registered phone; new phone attendance; change phone attendance; biometric attendance; fingerprint attendance
related: hybrid-work, office-kiosk, attendance-csv

In [[me|My HR]], choose where you are working and click **Check in** or **Check out**. The staff phone app does the same after you unlock it with your phone’s fingerprint or face (Ledgerline keeps no fingerprints or face data).

- **Office** counts only on the office network or within the office’s radius (100 m unless HR set another). Tick **Send my location** if you are not on the office network.
- **Field** needs your location and a note on whom you are visiting.
- **Home** is allowed only on your work policy’s home days or with an approved request.
- One phone per person: the first phone you use is registered. A new phone waits for HR to approve it on [[attendance|Attendance]]; then the old one stops working.
- A photo with a check-in is optional, for your manager to review only, and is deleted after 90 days (or the firm’s setting).

## office-kiosk
title: The office attendance kiosk and kiosk PIN
screen: attendance
roles: HR manager, Admin
ask: attendance kiosk; tablet at entrance; register kiosk; pairing code; kiosk pin; set kiosk pin; forgot kiosk pin; kiosk pin locked; unlock pin; kiosk photo
related: phone-checkin

A tablet at the office entrance can be the kiosk.

1. On [[attendance|Attendance]] click **Register a kiosk**. You get a pairing code that works once, for 30 minutes.
2. On the tablet, from the office network, open the address shown (ending /kiosk/ and your firm) and enter the code.
3. Each person sets a **kiosk PIN** (4 to 6 digits, not a simple run) in My HR. At the kiosk they enter their employee code and PIN and tap **In** or **Out**. The kiosk takes a photo for review and shows only the name and time.

Five wrong PINs lock the PIN until the person sets a new one or HR clicks **Unlock kiosk PIN** on their record. The kiosk works only on the office network; **Revoke** stops it at once.

## attendance-csv
title: Upload attendance from a device (CSV)
screen: attendance
roles: HR manager, Admin
ask: upload attendance file; biometric device csv; attendance machine export; import punches; device user id; unknown device id
related: phone-checkin

Any attendance device that exports a CSV can be used as a fallback. On [[attendance|Attendance]] click **Upload a CSV from a device**. The file needs a user ID column and a date-time column, one punch a row. Each person’s **device user ID** is on their record. Rows for unknown IDs are listed after the upload. The first and last punch of a day are taken as in and out.

## hybrid-work
title: Hybrid work: office days, home days and dealers at home
screen: attendance
roles: HR manager, Admin, Compliance officer
ask: work from home; wfh; hybrid policy; office days per week; home days; wfh request; approve work from home; dealer working from home; home check in refused; exchange approved location; monthly office report; attendance exceptions
related: phone-checkin, leave-requests

Work policies are set on [[hrpolicy|HR policies]]: the ways of working allowed (office, home, field, on duty), office days required a week or a month, and fixed home days or “any N” home days a week. A policy applies to its departments unless a person has their own.

- A home check-in is accepted on an allowed home day, or on a day the manager approved (**Ask to work from home** in My HR).
- **Dealers** (people on exchange terminals) cannot check in from home unless the compliance officer has recorded their home as an exchange-approved dealing location, with the exchange’s reference, on their record. Dealing from a location the exchange has not approved needs the exchange’s permission. A firm whose dealers never deal from home (or that has the exchange’s general permission) can lift the rule on HR policies.
- [[attendance|Attendance]] shows each person’s office and home days against their policy for the month. **Send exceptions to managers** raises a work item for each manager about the people below the policy.

## leave-requests
title: Apply for leave, approve it, and who is on leave today
screen: leave
roles: Ops maker, Ops checker, Risk manager, Compliance officer, HR manager, Payroll, Branch staff, Admin
ask: apply leave; leave request; approve leave; leave balance; casual leave; sick leave; earned leave; leave without pay; cancel leave; team calendar; who is on leave today; backup while on leave; carry forward leave; encash leave; close leave year
related: hybrid-work, leave-types

- Apply in [[me|My HR]] with the dates and who covers your work. Ledgerline counts working days (weekly offs and office holidays are not counted) and checks your balance.
- The request goes to your manager (HR when you have none). They approve or refuse it on [[leave|Leave]]. Nobody approves their own leave.
- **On leave today** shows who is away and their backup; other screens use it to hand work over.
- At the end of the leave year HR clicks **Close the leave year**: each balance is carried forward, encashed or lapses by its leave type.

## leave-types
title: Leave types, accrual, carry forward and encashment
screen: hrpolicy
roles: HR manager
ask: leave policy; leave types; accrual; earned leave per month; carry forward limit; encashment limit; leave year; financial year leave; shifts; weekly off; office holidays; exchange holidays office; office location for check in; office network
related: leave-requests

On [[hrpolicy|HR policies]]:

- **Leave types**: days granted each year (prorated for joiners), days accrued each month, the most that can be held, carried forward and encashed at year end, and whether it can be taken without a balance (leave without pay).
- **Leave year**: calendar or financial.
- **Shifts**: hours, grace minutes, the hours below which a day is a half day, and weekly offs.
- **Office holidays**, and whether exchange trading holidays from the firm calendar are office holidays too.
- **Offices for check-in**: each office’s location, radius and networks. The firm’s networks from Security settings can count as office networks.
