Ledgy help articles: keys the broker holds. What it means to hold the firm's own encryption key, setting up a KMS key and its policy in the firm's AWS account, a firm's own key service, and what happens when the key is revoked.

# Keys the broker holds

## firm-held-keys
title: Can our firm hold its own encryption key?
screen: settings
roles: Admin, Compliance officer
ask: broker-held keys; broker held keys; our own encryption key; firm's own key; bring your own key; byok; hold our own key; customer managed key; cmk; who holds the encryption keys; ownership of encryption keys; sebi cloud framework keys; revoke ledgerline access; key revoked; encryption key is not available; key not available; revoked or unreachable; kms key disabled
related: firm-held-keys-aws, firm-held-keys-service, data-in-india, audit-seals

Yes. SEBI's framework for regulated entities using cloud services says ownership of data, logs and encryption keys stays with the regulated entity. Ledgerline encrypts all your firm's data (AES-256-GCM, a separate key for each firm). By default Ledgerline holds those keys. Your firm can hold the key itself instead:

- **AWS KMS in your firm's own AWS account**, in Mumbai or Hyderabad only (see "Setting up our KMS key for Ledgerline").
- **A key service your firm runs** (a key file or an HSM behind HTTPS), for firms not on AWS.

**How it works.** Ledgerline makes a key for your firm and has your key wrap (encrypt) it. Only the wrapped copy is stored. Ledgerline asks your key to unwrap it when it starts and keeps it in memory for a short time, the **cache window** (15 minutes unless you change it, 1 to 60). Then it asks again. Everything saved from then on is under your firm's key. Your key is called only to wrap and unwrap that one key, never for each record.

**Moving your firm.** An Admin enters the key in [[connections|Connections]], presses **Test**, then **Use this key** on the **Encryption keys** panel in [[settings|Settings]]. Nothing is rewritten. Records saved before the move stay under Ledgerline's key until they are next saved, and are still served only while your key is available.

**If your key is revoked or cannot be reached** (key disabled, access or grant removed, your service down):

- Within the cache window Ledgerline stops serving your firm. Every screen, and sign-in, says **"Your firm's encryption key is not available (revoked or unreachable)"**. Nothing is shown or saved. Nothing falls back to Ledgerline's key.
- An audit entry (firmkey.unavailable) and a work item for the Admin are raised once for the outage. The server's operator sees it in the server log too.
- When the key answers again, service resumes by itself within about a minute of the next request. The audit trail records firmkey.restored and the work item is closed.

**Rotating.** **Rotate** on the panel makes a new key version for new data and re-wraps the older versions under your current key, so older data stays readable. To move to a new KMS key, enter its ARN in Connections, keep the old key enabled, press **Rotate**, then disable the old key.

**Never delete your key** while your firm uses Ledgerline. Data saved under it, and backups of it, could then not be read by anyone, Ledgerline included.

**What stays under Ledgerline's key:** the wrapped copy of your firm's key, the connection with the credentials for your key, and the outage work item, because Ledgerline needs them to reach your key or to tell you it is gone. The audit trail is not encrypted so auditors can read it; its seals use Ledgerline's audit key.

**For your auditor:** the Encryption keys panel (key masked, version in use, when moved and rotated, last successful use, cache window), the audit entries firmkey.moved, firmkey.rotated, firmkey.cache_set, firmkey.unavailable and firmkey.restored, your key policy, and your AWS CloudTrail log of Ledgerline's Encrypt and Decrypt calls.

## firm-held-keys-aws
title: Setting up our KMS key for Ledgerline
screen: settings
roles: Admin
ask: create kms key; kms key policy; key policy for ledgerline; set up aws kms; aws kms key arn; cross-account role for kms; kms grant; encryption context; ledgerline-tenant; external id role; aws access key for ledgerline
related: firm-held-keys, connections

Do this in your firm's own AWS account.

1. **Create the key.** In the AWS console, choose region **Asia Pacific (Mumbai) ap-south-1** or **Asia Pacific (Hyderabad) ap-south-2**. Ledgerline refuses keys anywhere else. Go to Key Management Service → Create key: **Symmetric**, **Encrypt and decrypt**, single-Region. Give it an alias such as ledgerline-firm-key. Make only your own people key administrators. You may switch on automatic key rotation.
2. **Decide how Ledgerline signs in.**
   - **A role (recommended for cross-account use).** Create an IAM role that trusts the AWS account your Ledgerline provider names, with **Require external ID** set to a random phrase. Your provider gives you an access key that may assume it.
   - **An access key.** Create an IAM user in your account with no console access and no other permissions, and make an access key for it.
3. **Allow Ledgerline to use the key, for your firm only.** Add the statement below to the key policy, with your role (or user) ARN and your Ledgerline firm code. Ledgerline needs no other KMS permission.
4. **Enter it in Ledgerline.** In [[connections|Connections]] → **Firm's own encryption key: AWS KMS**: the region, the key ARN (arn:aws:kms:ap-south-1:…:key/…), the access key ID and secret, and the role ARN and external ID if you use a role. Leave the addresses empty. Press **Test**: Ledgerline wraps and unwraps a test key with your firm's context. The secret is kept encrypted and never shown again.
5. **Move the firm.** On the **Encryption keys** panel in [[settings|Settings]], press **Use this key** and choose the cache window.

The key policy statement (one line; paste it into the key policy's Statement list):

`{"Sid": "LedgerlineUsesThisKeyForOurFirmOnly", "Effect": "Allow", "Principal": {"AWS": "arn:aws:iam::111122223333:role/LedgerlineKeyUse"}, "Action": ["kms:Encrypt", "kms:Decrypt"], "Resource": "*", "Condition": {"StringEquals": {"kms:EncryptionContext:ledgerline-tenant": "yourfirmcode", "kms:EncryptionContext:ledgerline-purpose": "firm-root-key"}}}`

Ledgerline sends this encryption context with every call (and ledgerline-root-version too), so through Ledgerline the key works for your firm only.

**To revoke Ledgerline**, disable the key, or remove the statement from the key policy (or the role's trust). Ledgerline stops within the cache window. Re-enable or put it back to resume.

**To watch it**, look in AWS CloudTrail for Encrypt and Decrypt calls on the key. With a role, they show the session name ledgerline-yourfirmcode. Expect a Decrypt about once per cache window while people work.

## firm-held-keys-service
title: Using our own key service instead of AWS
screen: settings
roles: Admin
ask: own key service; hsm; key file; firm not on aws; on-premise key; unwrap service; key service protocol; wrap unwrap endpoint; mutual tls key service; shared secret key service
related: firm-held-keys, connections

A firm not on AWS can run a small HTTPS service in front of its key (a key file or an HSM). Keep it in India. Ledgerline calls two addresses, both POST with JSON:

- **address/wrap** with `{"context": {...}, "key": "<base64>"}`. Answer `{"wrapped": "<base64>", "key_id": "<your name for the key>"}`.
- **address/unwrap** with `{"context": {...}, "key_id": "...", "wrapped": "<base64>"}`. Answer `{"key": "<base64>"}`.

**context** holds ledgerline-tenant (your firm code), ledgerline-purpose (firm-root-key) and ledgerline-root-version. Your service must bind the wrapped key to it (for example as AES-GCM additional data), so it never unwraps for another firm or version.

**Checking it is Ledgerline.** Each request has X-Ledgerline-Timestamp (Unix seconds) and X-Ledgerline-Signature: v1= and the hex HMAC-SHA256, under the shared secret, of the timestamp, a new line, the path, a new line and the body. Refuse a wrong signature or a request more than 5 minutes old. The secret itself is never sent. You can also, or instead, require a client certificate (mutual TLS).

**Answers.** 200 with the JSON above. Answer 401, 403, 404, 410 or 423 to refuse: Ledgerline treats that as the key withdrawn and stops serving your firm. Any other answer counts as a failure and Ledgerline asks again later; your firm is not served meanwhile either.

**Enter it** in [[connections|Connections]] → **Firm's own encryption key: firm's key service**: the address, the shared secret (at least 32 characters), and the client certificate and key or your private CA if you use them. Press **Test**, then **Use this key** on the **Encryption keys** panel in [[settings|Settings]].
