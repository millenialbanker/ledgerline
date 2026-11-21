-- Archiving a past period to cheaper storage, and restoring it.
--
-- A firm moves a closed period (financial years or dates it chooses) out of
-- the live database: Ledgerline writes an encrypted, signed archive package
-- to archive storage, reads it back and verifies it, and only then removes
-- the period's rows here. The ledger entries removed are replaced in the
-- live books by one entry of opening balances carried forward, so every
-- later balance and statement stays the same. Restoring puts the same rows
-- back (same ids, sequence numbers, seals and file bytes) and removes that
-- carried-forward entry.
--
--   * ll.archive_period is the record of each archive: the period, who
--     asked and who approved, the package's SHA-256, when it was verified,
--     the carried-forward entry, and the restore's two people. Its state
--     moves only forwards (trigger), and a row is never deleted except by
--     the firm's own purge when it closes its account.
--   * ll.archive_remove deletes the rows of one table listed by key, in the
--     caller's transaction, only when the record is approved by a second
--     person and its package verified, only for rows inside the record's
--     period, and never in the current financial year. It refuses unless
--     every key listed was deleted, so nothing outside the period can go
--     with it. The application role still has no DELETE privilege on any
--     table: the function runs as the schema owner, and the append-only
--     triggers let a row go only while it has marked the firm as archiving.
--   * ll.archive_restore inserts archived rows back, and
--     ll.archive_drop_carry removes the carried-forward entry, only when a
--     restore of that record was approved by a second person.
--   * The audit trail is never archived: its seal chain stays whole.
BEGIN;
SET search_path = ll, public;

CREATE TABLE ll.archive_period (
  tenant_id            text NOT NULL REFERENCES ll.tenant(id),
  id                   text NOT NULL CHECK (id ~ '^AR-[0-9]{8}-[0-9]{6}-[0-9a-f]{4,16}$'),
  state                text NOT NULL CHECK (state IN ('Requested', 'Approved', 'Verified', 'Archived', 'Rejected', 'Failed',
                                                      'Restore requested', 'Restore approved', 'Restored')),
  period_from          date NOT NULL,
  period_to            date NOT NULL CHECK (period_to >= period_from),
  requested_by         text NOT NULL,
  approved_by          text,
  package_sha256       text CHECK (package_sha256 IS NULL OR package_sha256 ~ '^[0-9a-f]{64}$'),
  verified_at          timestamptz,
  carry_entries        integer NOT NULL DEFAULT 0 CHECK (carry_entries >= 0),
  restore_requested_by text,
  restore_approved_by  text,
  retain_until         date,
  record               jsonb NOT NULL,
  updated_at           timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  CHECK (state NOT IN ('Approved', 'Verified', 'Archived', 'Restore requested', 'Restore approved', 'Restored')
         OR (approved_by IS NOT NULL AND approved_by <> requested_by)),
  CHECK (state NOT IN ('Verified', 'Archived', 'Restore requested', 'Restore approved', 'Restored')
         OR (package_sha256 IS NOT NULL AND verified_at IS NOT NULL)),
  CHECK (state NOT IN ('Restore approved', 'Restored')
         OR (restore_requested_by IS NOT NULL AND restore_approved_by IS NOT NULL AND restore_approved_by <> restore_requested_by))
);

-- The state moves forwards only, and what was approved and verified
-- cannot be changed afterwards.
CREATE OR REPLACE FUNCTION ll.trg_archive_period_keep() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE ok boolean;
BEGIN
  IF NEW.tenant_id <> OLD.tenant_id OR NEW.id <> OLD.id THEN
    RAISE EXCEPTION 'archive % of firm %: the firm and id cannot change', OLD.id, OLD.tenant_id USING ERRCODE = 'P0001';
  END IF;
  IF NEW.state <> OLD.state THEN
    ok := CASE OLD.state
      WHEN 'Requested'         THEN NEW.state IN ('Approved', 'Rejected')
      WHEN 'Approved'          THEN NEW.state IN ('Verified', 'Failed')
      WHEN 'Verified'          THEN NEW.state IN ('Archived', 'Failed')
      WHEN 'Failed'            THEN NEW.state IN ('Approved', 'Rejected')
      WHEN 'Archived'          THEN NEW.state IN ('Restore requested')
      WHEN 'Restore requested' THEN NEW.state IN ('Restore approved', 'Archived')
      WHEN 'Restore approved'  THEN NEW.state IN ('Restored', 'Archived')
      ELSE false
    END;
    IF NOT ok THEN
      RAISE EXCEPTION 'archive % of firm % is %; it cannot become %', OLD.id, OLD.tenant_id, OLD.state, NEW.state USING ERRCODE = 'P0001';
    END IF;
  END IF;
  IF OLD.state NOT IN ('Requested', 'Failed') AND (NEW.period_from <> OLD.period_from OR NEW.period_to <> OLD.period_to
      OR NEW.requested_by <> OLD.requested_by OR NEW.approved_by IS DISTINCT FROM OLD.approved_by) THEN
    RAISE EXCEPTION 'archive % of firm %: the period and the people who asked and approved cannot change once approved', OLD.id, OLD.tenant_id USING ERRCODE = 'P0001';
  END IF;
  IF OLD.state IN ('Verified', 'Archived', 'Restore requested', 'Restore approved', 'Restored') AND NEW.package_sha256 IS DISTINCT FROM OLD.package_sha256 THEN
    RAISE EXCEPTION 'archive % of firm %: the verified package cannot change', OLD.id, OLD.tenant_id USING ERRCODE = 'P0001';
  END IF;
  NEW.updated_at := now();
  RETURN NEW;
END $$;
CREATE TRIGGER archive_period_keep BEFORE UPDATE ON ll.archive_period FOR EACH ROW EXECUTE FUNCTION ll.trg_archive_period_keep();
CREATE TRIGGER archive_period_no_delete BEFORE DELETE ON ll.archive_period FOR EACH ROW EXECUTE FUNCTION ll.forbid_mutation();
CREATE TRIGGER archive_period_no_truncate BEFORE TRUNCATE ON ll.archive_period EXECUTE FUNCTION ll.forbid_mutation();

ALTER TABLE ll.archive_period ENABLE ROW LEVEL SECURITY;
ALTER TABLE ll.archive_period FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ll.archive_period USING (tenant_id = ll.current_tenant()) WITH CHECK (tenant_id = ll.current_tenant());
GRANT SELECT, INSERT, UPDATE ON ll.archive_period TO ledgerline_app;
GRANT EXECUTE ON FUNCTION ll.trg_archive_period_keep() TO ledgerline_app;

-- The append-only guard, now also letting a row go while an archive
-- function has marked its firm as archiving (ll.archiving, local to the
-- transaction). The application role has no DELETE privilege, so only the
-- functions below (and ll.purge_tenant) can delete. TRUNCATE is still
-- refused.
CREATE OR REPLACE FUNCTION ll.forbid_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' AND TG_LEVEL = 'ROW' THEN
    IF OLD.tenant_id = nullif(current_setting('ll.purging', true), '')
       OR OLD.tenant_id = nullif(current_setting('ll.archiving', true), '') THEN
      RETURN OLD;
    END IF;
  END IF;
  RAISE EXCEPTION '% is append-only; % is not allowed', TG_TABLE_NAME, TG_OP
    USING ERRCODE = 'P0001', HINT = 'Post a reversing entry instead of editing history.';
END $$;

-- The document history copy, skipped only for a document that
-- ll.archive_restore puts back (its history comes back from the package
-- as it was). The skip needs both the firm marked as archiving and the
-- statement running as the schema owner, which only the SECURITY DEFINER
-- function does: the application role setting ll.archiving itself still
-- gets every change copied.
CREATE OR REPLACE FUNCTION ll.trg_doc_history() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND NEW.version <> OLD.version + 1 THEN
    RAISE EXCEPTION 'doc %/% version must advance by one (was %, got %)', NEW.coll, NEW.id, OLD.version, NEW.version USING ERRCODE = '40001';
  END IF;
  IF TG_OP = 'INSERT' AND NEW.tenant_id = nullif(current_setting('ll.archiving', true), '')
     AND current_user = (SELECT pg_get_userbyid(c.relowner) FROM pg_class c WHERE c.oid = 'll.doc'::regclass) THEN
    RETURN NEW;
  END IF;
  INSERT INTO ll.doc_history (tenant_id, coll, id, version, body, updated_at, updated_by)
  VALUES (NEW.tenant_id, NEW.coll, NEW.id, NEW.version, NEW.body, NEW.updated_at, NEW.updated_by);
  RETURN NEW;
END $$;

-- The first day of the financial year today (in India) falls in.
CREATE OR REPLACE FUNCTION ll.current_fy_start() RETURNS date
LANGUAGE sql STABLE AS $$ SELECT make_date(extract(year FROM ((now() AT TIME ZONE 'Asia/Kolkata') - interval '3 months'))::int, 4, 1) $$;

-- The collections of migrated history whose documents may be archived.
CREATE OR REPLACE FUNCTION ll.archive_doc_colls() RETURNS text[]
LANGUAGE sql IMMUTABLE AS $$ SELECT ARRAY['mighistc', 'mighists'] $$;

-- ll.archive_remove deletes the listed rows of one table for an approved,
-- verified archive and returns how many it deleted, which must be all of
-- them. Keys: journal_entry id (its postings go with it), trade_event
-- origin|exchange|segment|trade_date|trade_no, raw_file id, doc_history
-- seq, doc coll|id.
CREATE OR REPLACE FUNCTION ll.archive_remove(p_tenant text, p_archive text, p_table text, p_keys text[]) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = ll, pg_temp AS $$
DECLARE
  r ll.archive_period;
  n bigint := 0;
  want bigint := coalesce(cardinality(p_keys), 0);
BEGIN
  IF p_tenant IS NULL OR p_tenant IS DISTINCT FROM ll.current_tenant() THEN
    RAISE EXCEPTION 'archive_remove: the transaction is not set to firm %', p_tenant USING ERRCODE = '42501';
  END IF;
  SELECT * INTO r FROM ll.archive_period WHERE tenant_id = p_tenant AND id = p_archive FOR UPDATE;
  IF NOT FOUND OR r.state <> 'Verified' OR r.approved_by IS NULL OR r.approved_by = r.requested_by
     OR r.package_sha256 IS NULL OR r.verified_at IS NULL THEN
    RAISE EXCEPTION 'archive_remove: firm % has no approved, verified archive %', p_tenant, p_archive USING ERRCODE = '55000';
  END IF;
  IF r.period_to >= ll.current_fy_start() THEN
    RAISE EXCEPTION 'archive_remove: archive % reaches into the current financial year', p_archive USING ERRCODE = '55000';
  END IF;
  IF want = 0 THEN
    RETURN 0;
  END IF;
  IF cardinality(ARRAY(SELECT DISTINCT unnest(p_keys))) <> want THEN
    RAISE EXCEPTION 'archive_remove: a key is listed twice' USING ERRCODE = '22023';
  END IF;
  PERFORM set_config('ll.archiving', p_tenant, true);
  CASE p_table
  WHEN 'journal_entry' THEN
    DELETE FROM ll.posting p USING ll.journal_entry e
     WHERE p.tenant_id = p_tenant AND e.tenant_id = p_tenant AND p.entry_id = e.id AND e.id = ANY (p_keys)
       AND e.value_date BETWEEN r.period_from AND r.period_to AND e.source_kind <> 'ARCHIVE-CARRY';
    DELETE FROM ll.journal_entry e
     WHERE e.tenant_id = p_tenant AND e.id = ANY (p_keys)
       AND e.value_date BETWEEN r.period_from AND r.period_to AND e.source_kind <> 'ARCHIVE-CARRY';
    GET DIAGNOSTICS n = ROW_COUNT;
  WHEN 'trade_event' THEN
    DELETE FROM ll.trade_event t
     WHERE t.tenant_id = p_tenant AND t.trade_date BETWEEN r.period_from AND r.period_to
       AND (t.origin || '|' || t.exchange || '|' || t.segment || '|' || to_char(t.trade_date, 'YYYY-MM-DD') || '|' || t.trade_no) = ANY (p_keys);
    GET DIAGNOSTICS n = ROW_COUNT;
  WHEN 'raw_file' THEN
    -- A migrated document's file is dated by the document (kept in its
    -- encrypted index record), so its key alone is checked.
    DELETE FROM ll.raw_file f
     WHERE f.tenant_id = p_tenant AND f.id = ANY (p_keys)
       AND ((f.received_at AT TIME ZONE 'Asia/Kolkata')::date BETWEEN r.period_from AND r.period_to OR f.source = 'MIG-ARCHIVE');
    GET DIAGNOSTICS n = ROW_COUNT;
  WHEN 'doc_history' THEN
    DELETE FROM ll.doc_history h
     WHERE h.tenant_id = p_tenant AND h.seq::text = ANY (p_keys)
       AND ((h.updated_at AT TIME ZONE 'Asia/Kolkata')::date BETWEEN r.period_from AND r.period_to OR h.coll = ANY (ll.archive_doc_colls()));
    GET DIAGNOSTICS n = ROW_COUNT;
  WHEN 'doc' THEN
    DELETE FROM ll.doc d
     WHERE d.tenant_id = p_tenant AND d.coll = ANY (ll.archive_doc_colls()) AND (d.coll || '|' || d.id) = ANY (p_keys);
    GET DIAGNOSTICS n = ROW_COUNT;
  ELSE
    RAISE EXCEPTION 'archive_remove: table % cannot be archived', p_table USING ERRCODE = '22023';
  END CASE;
  PERFORM set_config('ll.archiving', '', true);
  IF n <> want THEN
    RAISE EXCEPTION 'archive_remove: only % of the % % rows listed are in archive %''s period', n, want, p_table, p_archive USING ERRCODE = '55000';
  END IF;
  RETURN n;
END $$;
REVOKE ALL ON FUNCTION ll.archive_remove(text, text, text, text[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ll.archive_remove(text, text, text, text[]) TO ledgerline_app;

-- ll.archive_restore inserts archived rows of one table (as JSON, the way
-- the package keeps them: row_to_json of each row) for a restore approved
-- by a second person, and returns how many it inserted.
CREATE OR REPLACE FUNCTION ll.archive_restore(p_tenant text, p_archive text, p_table text, p_rows jsonb) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = ll, pg_temp AS $$
DECLARE
  r ll.archive_period;
  n bigint := 0;
  want bigint := coalesce(jsonb_array_length(p_rows), 0);
BEGIN
  IF p_tenant IS NULL OR p_tenant IS DISTINCT FROM ll.current_tenant() THEN
    RAISE EXCEPTION 'archive_restore: the transaction is not set to firm %', p_tenant USING ERRCODE = '42501';
  END IF;
  SELECT * INTO r FROM ll.archive_period WHERE tenant_id = p_tenant AND id = p_archive FOR UPDATE;
  IF NOT FOUND OR r.state <> 'Restore approved' OR r.restore_requested_by IS NULL OR r.restore_approved_by IS NULL
     OR r.restore_approved_by = r.restore_requested_by THEN
    RAISE EXCEPTION 'archive_restore: firm % has no approved restore of archive %', p_tenant, p_archive USING ERRCODE = '55000';
  END IF;
  IF want = 0 THEN
    RETURN 0;
  END IF;
  PERFORM set_config('ll.archiving', p_tenant, true);
  CASE p_table
  WHEN 'journal_entry' THEN
    INSERT INTO ll.journal_entry SELECT * FROM jsonb_populate_recordset(NULL::ll.journal_entry, p_rows) x
     WHERE x.tenant_id = p_tenant AND x.value_date BETWEEN r.period_from AND r.period_to;
    GET DIAGNOSTICS n = ROW_COUNT;
  WHEN 'posting' THEN
    INSERT INTO ll.posting SELECT * FROM jsonb_populate_recordset(NULL::ll.posting, p_rows) x WHERE x.tenant_id = p_tenant;
    GET DIAGNOSTICS n = ROW_COUNT;
  WHEN 'trade_event' THEN
    INSERT INTO ll.trade_event SELECT * FROM jsonb_populate_recordset(NULL::ll.trade_event, p_rows) x
     WHERE x.tenant_id = p_tenant AND x.trade_date BETWEEN r.period_from AND r.period_to;
    GET DIAGNOSTICS n = ROW_COUNT;
  WHEN 'raw_file' THEN
    INSERT INTO ll.raw_file SELECT * FROM jsonb_populate_recordset(NULL::ll.raw_file, p_rows) x WHERE x.tenant_id = p_tenant;
    GET DIAGNOSTICS n = ROW_COUNT;
  WHEN 'doc_history' THEN
    INSERT INTO ll.doc_history OVERRIDING SYSTEM VALUE SELECT * FROM jsonb_populate_recordset(NULL::ll.doc_history, p_rows) x WHERE x.tenant_id = p_tenant;
    GET DIAGNOSTICS n = ROW_COUNT;
  WHEN 'doc' THEN
    INSERT INTO ll.doc SELECT * FROM jsonb_populate_recordset(NULL::ll.doc, p_rows) x
     WHERE x.tenant_id = p_tenant AND x.coll = ANY (ll.archive_doc_colls());
    GET DIAGNOSTICS n = ROW_COUNT;
  ELSE
    RAISE EXCEPTION 'archive_restore: table % cannot be restored', p_table USING ERRCODE = '22023';
  END CASE;
  PERFORM set_config('ll.archiving', '', true);
  IF n <> want THEN
    RAISE EXCEPTION 'archive_restore: only % of the % % rows belong to archive %', n, want, p_table, p_archive USING ERRCODE = '55000';
  END IF;
  RETURN n;
END $$;
REVOKE ALL ON FUNCTION ll.archive_restore(text, text, text, jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ll.archive_restore(text, text, text, jsonb) TO ledgerline_app;

-- ll.archive_drop_carry removes the entries of opening balances carried
-- forward that stood in for an archived period, for a restore approved by
-- a second person. They must all be there (the record counts them).
CREATE OR REPLACE FUNCTION ll.archive_drop_carry(p_tenant text, p_archive text) RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = ll, pg_temp AS $$
DECLARE
  r ll.archive_period;
  n bigint := 0;
BEGIN
  IF p_tenant IS NULL OR p_tenant IS DISTINCT FROM ll.current_tenant() THEN
    RAISE EXCEPTION 'archive_drop_carry: the transaction is not set to firm %', p_tenant USING ERRCODE = '42501';
  END IF;
  SELECT * INTO r FROM ll.archive_period WHERE tenant_id = p_tenant AND id = p_archive FOR UPDATE;
  IF NOT FOUND OR r.state <> 'Restore approved' OR r.restore_approved_by IS NULL OR r.restore_approved_by = r.restore_requested_by THEN
    RAISE EXCEPTION 'archive_drop_carry: firm % has no approved restore of archive %', p_tenant, p_archive USING ERRCODE = '55000';
  END IF;
  PERFORM set_config('ll.archiving', p_tenant, true);
  DELETE FROM ll.posting p USING ll.journal_entry e
   WHERE p.tenant_id = p_tenant AND e.tenant_id = p_tenant AND p.entry_id = e.id
     AND e.source_kind = 'ARCHIVE-CARRY' AND e.source_id = p_archive AND e.value_date = r.period_to;
  DELETE FROM ll.journal_entry e
   WHERE e.tenant_id = p_tenant AND e.source_kind = 'ARCHIVE-CARRY' AND e.source_id = p_archive AND e.value_date = r.period_to;
  GET DIAGNOSTICS n = ROW_COUNT;
  PERFORM set_config('ll.archiving', '', true);
  IF n <> r.carry_entries THEN
    RAISE EXCEPTION 'archive_drop_carry: % carried-forward entries of archive % found, % recorded', n, p_archive, r.carry_entries USING ERRCODE = '55000';
  END IF;
  RETURN n;
END $$;
REVOKE ALL ON FUNCTION ll.archive_drop_carry(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ll.archive_drop_carry(text, text) TO ledgerline_app;

-- A firm's purge now also deletes its archive records.
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
  FOREACH t IN ARRAY ARRAY['posting', 'journal_entry', 'account', 'charge_schedule', 'recon_break', 'recon_run', 'trade_event', 'raw_file', 'audit_log', 'doc_history', 'doc', 'archive_period'] LOOP
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
