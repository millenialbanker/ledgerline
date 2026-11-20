-- A firm's exit: closing its Ledgerline account and deleting its data
-- (SEBI's framework for the adoption of cloud services: data portability
-- and a clear expunging clause, data returned or destroyed with a
-- certificate).
--
--   * ll.tenant_exit is the operator's record of each exit. It has no
--     foreign key to ll.tenant because it outlives the firm's own rows: it
--     keeps the firm's name and code, the dates, who asked and who
--     approved, the counts deleted and the destruction certificate.
--   * ll.purge_tenant deletes every row of one firm, in the caller's
--     transaction, and only when that firm's exit record is approved by a
--     second person, its cooling-off period has ended and its final export
--     was downloaded. The application role still has no DELETE privilege on
--     any table: the function runs as the schema owner, and the append-only
--     triggers let a row go only while the function has marked that firm as
--     being purged.
BEGIN;
SET search_path = ll, public;

CREATE TABLE ll.tenant_exit (
  tenant_id            text NOT NULL CHECK (tenant_id ~ '^[a-z0-9][a-z0-9_-]{1,62}$'),
  id                   text NOT NULL CHECK (length(id) BETWEEN 1 AND 80),
  state                text NOT NULL CHECK (state IN ('Requested', 'Approved', 'Cancelled', 'Purged')),
  requested_by         text NOT NULL,
  approved_by          text,
  cooling_off_ends     timestamptz,
  export_downloaded_at timestamptz,
  purged_at            timestamptz,
  certificate_sha256   text,
  record               jsonb NOT NULL,
  certificate          bytea,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);

-- A purged record stays purged.
CREATE OR REPLACE FUNCTION ll.trg_tenant_exit_keep() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.state = 'Purged' AND NEW.state <> 'Purged' THEN
    RAISE EXCEPTION 'exit % of firm % is purged; it cannot change state', OLD.id, OLD.tenant_id USING ERRCODE = 'P0001';
  END IF;
  NEW.updated_at := now();
  RETURN NEW;
END $$;
CREATE TRIGGER tenant_exit_keep BEFORE UPDATE ON ll.tenant_exit FOR EACH ROW EXECUTE FUNCTION ll.trg_tenant_exit_keep();
CREATE TRIGGER tenant_exit_no_delete BEFORE DELETE ON ll.tenant_exit FOR EACH ROW EXECUTE FUNCTION ll.forbid_mutation();
CREATE TRIGGER tenant_exit_no_truncate BEFORE TRUNCATE ON ll.tenant_exit EXECUTE FUNCTION ll.forbid_mutation();

-- A firm's session sees and writes only its own exit; the operator's
-- connection (no firm set) reads every firm's, for its list of exits.
ALTER TABLE ll.tenant_exit ENABLE ROW LEVEL SECURITY;
ALTER TABLE ll.tenant_exit FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_exit_read ON ll.tenant_exit FOR SELECT USING (ll.current_tenant() IS NULL OR tenant_id = ll.current_tenant());
CREATE POLICY tenant_exit_insert ON ll.tenant_exit FOR INSERT WITH CHECK (tenant_id = ll.current_tenant());
CREATE POLICY tenant_exit_update ON ll.tenant_exit FOR UPDATE USING (tenant_id = ll.current_tenant()) WITH CHECK (tenant_id = ll.current_tenant());

GRANT SELECT, INSERT, UPDATE ON ll.tenant_exit TO ledgerline_app;
GRANT EXECUTE ON FUNCTION ll.trg_tenant_exit_keep() TO ledgerline_app;

-- The append-only guard, now letting a row go while ll.purge_tenant has
-- marked its firm as being purged (ll.purging, local to the transaction).
-- TRUNCATE is still refused.
CREATE OR REPLACE FUNCTION ll.forbid_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' AND TG_LEVEL = 'ROW' THEN
    IF OLD.tenant_id = nullif(current_setting('ll.purging', true), '') THEN
      RETURN OLD;
    END IF;
  END IF;
  RAISE EXCEPTION '% is append-only; % is not allowed', TG_TABLE_NAME, TG_OP
    USING ERRCODE = 'P0001', HINT = 'Post a reversing entry instead of editing history.';
END $$;

-- ll.purge_tenant deletes one firm and returns the rows deleted per table.
-- The caller's transaction must be set to that firm (app.tenant_id), so
-- row-level security limits every statement to it as well.
CREATE OR REPLACE FUNCTION ll.purge_tenant(p_tenant text, p_exit text) RETURNS jsonb
LANGUAGE plpgsql SECURITY DEFINER SET search_path = ll, pg_temp AS $$
DECLARE
  r ll.tenant_exit;
  n jsonb := '{}'::jsonb;
  c bigint;
  t text;
BEGIN
  IF p_tenant IS NULL OR p_tenant IS DISTINCT FROM ll.current_tenant() THEN
    RAISE EXCEPTION 'purge_tenant: the transaction is not set to firm %', p_tenant USING ERRCODE = '42501';
  END IF;
  SELECT * INTO r FROM ll.tenant_exit WHERE tenant_id = p_tenant AND id = p_exit FOR UPDATE;
  IF NOT FOUND OR r.state <> 'Approved' OR r.approved_by IS NULL OR r.approved_by = r.requested_by
     OR r.cooling_off_ends IS NULL OR r.cooling_off_ends > now() OR r.export_downloaded_at IS NULL THEN
    RAISE EXCEPTION 'purge_tenant: firm % has no approved exit % ready to purge', p_tenant, p_exit USING ERRCODE = '55000';
  END IF;
  PERFORM set_config('ll.purging', p_tenant, true);
  -- Children before parents.
  FOREACH t IN ARRAY ARRAY['posting', 'journal_entry', 'account', 'charge_schedule', 'recon_break', 'recon_run', 'trade_event', 'raw_file', 'audit_log', 'doc_history', 'doc'] LOOP
    EXECUTE format('DELETE FROM ll.%I WHERE tenant_id = $1', t) USING p_tenant;
    GET DIAGNOSTICS c = ROW_COUNT;
    n := n || jsonb_build_object(t, c);
  END LOOP;
  DELETE FROM ll.tenant WHERE id = p_tenant;
  GET DIAGNOSTICS c = ROW_COUNT;
  n := n || jsonb_build_object('tenant', c);
  PERFORM set_config('ll.purging', '', true);
  RETURN n;
END $$;
REVOKE ALL ON FUNCTION ll.purge_tenant(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ll.purge_tenant(text, text) TO ledgerline_app;

COMMIT;
