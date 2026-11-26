Ledgy help articles: sending security logs to the firm's SIEM or SOC (syslog over TLS or HTTPS), downloading them, and how long they are kept.

# Log export

## log-export-siem
title: Sending security logs to the firm's SIEM or SOC
screen: logexport
roles: Admin, Compliance officer
ask: siem; soc; security operations centre; send logs to siem; syslog; syslog over tls; rfc 5424; log forwarding; export logs; splunk; qradar; sentinel; wazuh; elastic; log export stuck; hmac signature; cscrf logs; test log export
related: security-log-retention, security-alert-rules, connections

SEBI's CSCRF asks for the firm's logs to reach its security operations centre (SOC). Ledgerline sends three logs: the security events, the sign-in records and the audit trail. Client identifiers in the audit trail are masked before they leave.

Set up one or both destinations under [[connections|Connections]] (Admin):

- **Security monitoring: syslog over TLS**. Give the SIEM's host name and port (6514 if left empty). If the SIEM's certificate is from the firm's own CA, paste the CA certificate. Messages are RFC 5424 syslog with RFC 5425 framing, over TLS 1.2 or later.
- **Security monitoring: HTTPS (JSON lines)**. Give the address and choose **Bearer token** or **HMAC signature**, with the token or secret. Each batch is a POST of JSON lines. With HMAC, the X-Ledgerline-Signature header is sha256= and the HMAC-SHA256 of the X-Ledgerline-Timestamp header, a newline and the body.

For either, **What to send** can leave out the audit trail.

Open [[logexport|Log export]] to see each destination's state, when lines were last delivered and how many are waiting. **Send a test line** sends one line at once. A new destination starts from the time it is set up; download the history for earlier days.

If the SIEM cannot be reached, nothing is lost. Ledgerline keeps its place, tries again after 1, 2, 4 and up to 60 minutes, and sends the same lines again. The SIEM can drop repeats by each line's id. After 30 minutes of failures the Admin gets a work item, and it closes once lines flow again.

To download, choose the dates under **Download** on [[logexport|Log export]]. You get one gzip file of JSON lines, for an auditor, for CERT-In when it asks for logs, or to load a new SIEM. The download is itself recorded.

The SIEM must be in India, so that the logs stay in India as CERT-In requires.

## security-log-retention
title: How long security logs are kept
screen: logexport
roles: Compliance officer, Admin
ask: log retention; keep logs 180 days; cert-in log retention; how long are logs kept; sign-in log retention; delete old logs; logs in india; retention period
related: log-export-siem, data-in-india, audit-seals

CERT-In's directions of 28 April 2022 ask for logs to be kept for 180 days, in India. Ledgerline keeps security events and sign-in records for 730 days (two years, as CSCRF PR.AA.S9 asks for user access logs of critical systems) unless the firm sets another period. The period can't be set below 180 days, and below 730 the Incidents screen warns.

Set it under [[incidents|Incidents]] → **Rules and contacts**. The current period is stated on [[logexport|Log export]].

After the period, an event keeps its kind and time, and the person, address and details are cleared. Audit trail entries are never deleted.

Logs are kept on the Ledgerline server. Its location is shown on the Data protection panel. Copies sent to the firm's SIEM are kept for as long as the SIEM keeps them.
