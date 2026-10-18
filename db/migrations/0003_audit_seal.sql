-- Sealed audit trail. Each entry carries a keyed hash (HMAC-SHA256 with a
-- per-firm key the database never sees) over the entry and the previous
-- entry's hash. An edited row fails its seal; a removed or inserted row
-- breaks the chain. The application verifies the chain on demand. The
-- unique index stops two entries claiming the same predecessor, so the
-- chain cannot fork. Rows written before this migration stay unsealed.
BEGIN;
SET search_path = ll, public;

ALTER TABLE ll.audit_log ADD COLUMN prev_hash text, ADD COLUMN hash text;
ALTER TABLE ll.audit_log ADD CONSTRAINT audit_seal_pair CHECK ((hash IS NULL) = (prev_hash IS NULL));
CREATE UNIQUE INDEX audit_log_chain ON ll.audit_log (tenant_id, prev_hash) WHERE hash IS NOT NULL;
CREATE INDEX audit_log_tenant_id ON ll.audit_log (tenant_id, id);
COMMIT;
