-- Versioned document store for master and workflow data (clients, holdings,
-- settings, settlement status, alerts, ...). Money is never stored here; it
-- lives in the append-only journal. Every write is copied to doc_history,
-- which is append-only, so the full change history of any record survives.
BEGIN;
SET search_path = ll, public;

CREATE TABLE ll.doc (
  tenant_id   text NOT NULL REFERENCES ll.tenant(id),
  coll        text NOT NULL CHECK (coll ~ '^[a-z][a-z0-9_]{0,40}$'),
  id          text NOT NULL CHECK (length(id) BETWEEN 1 AND 200),
  version     bigint NOT NULL CHECK (version > 0),
  body        jsonb NOT NULL,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  updated_by  text NOT NULL,
  PRIMARY KEY (tenant_id, coll, id)
);
CREATE INDEX doc_prefix ON ll.doc (tenant_id, coll, id text_pattern_ops);

CREATE TABLE ll.doc_history (
  seq         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id   text NOT NULL REFERENCES ll.tenant(id),
  coll        text NOT NULL,
  id          text NOT NULL,
  version     bigint NOT NULL,
  body        jsonb NOT NULL,
  updated_at  timestamptz NOT NULL,
  updated_by  text NOT NULL
);
CREATE INDEX doc_history_doc ON ll.doc_history (tenant_id, coll, id, version);

CREATE OR REPLACE FUNCTION ll.trg_doc_history() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'doc %/% version must advance by one (was %, got %)', NEW.coll, NEW.id, OLD.version, NEW.version USING ERRCODE = '40001';
  END IF;
  INSERT INTO ll.doc_history (tenant_id, coll, id, version, body, updated_at, updated_by)
  VALUES (NEW.tenant_id, NEW.coll, NEW.id, NEW.version, NEW.body, NEW.updated_at, NEW.updated_by);
  RETURN NEW;
END $$;
CREATE TRIGGER doc_history AFTER INSERT OR UPDATE ON ll.doc FOR EACH ROW EXECUTE FUNCTION ll.trg_doc_history();
CREATE TRIGGER doc_no_delete BEFORE DELETE ON ll.doc FOR EACH ROW EXECUTE FUNCTION ll.forbid_mutation();
CREATE TRIGGER doc_history_append_only BEFORE UPDATE OR DELETE ON ll.doc_history FOR EACH ROW EXECUTE FUNCTION ll.forbid_mutation();
CREATE TRIGGER doc_history_no_truncate BEFORE TRUNCATE ON ll.doc_history EXECUTE FUNCTION ll.forbid_mutation();

ALTER TABLE ll.doc ENABLE ROW LEVEL SECURITY;
ALTER TABLE ll.doc FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ll.doc USING (tenant_id = ll.current_tenant()) WITH CHECK (tenant_id = ll.current_tenant());
ALTER TABLE ll.doc_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE ll.doc_history FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ll.doc_history USING (tenant_id = ll.current_tenant()) WITH CHECK (tenant_id = ll.current_tenant());

GRANT SELECT, INSERT, UPDATE ON ll.doc TO ledgerline_app;
GRANT SELECT, INSERT ON ll.doc_history TO ledgerline_app;
GRANT EXECUTE ON FUNCTION ll.trg_doc_history() TO ledgerline_app;
COMMIT;
