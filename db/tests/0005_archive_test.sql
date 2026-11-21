-- SQL-level tests for 0005_archive_period.sql. Run with psql -v
-- ON_ERROR_STOP=1 as a superuser after 0001_core_test.sql (it uses the
-- firms and the ll_test_app role made there). Every block raises an
-- exception if a guarantee does not hold.
\set QUIET on
SET search_path = ll, public;
INSERT INTO ll.tenant (id, name) VALUES ('arcfirm', 'Archive Test Broking') ON CONFLICT DO NOTHING;
SET ROLE ll_test_app;

-- Setup: a period's entry, file, trade and migrated record, and an entry after it.
BEGIN;
SELECT set_config('app.tenant_id', 'arcfirm', true);
INSERT INTO ll.account (tenant_id, code, type) VALUES ('arcfirm', 'BANK:A', 'ASSET'), ('arcfirm', 'CLIENT:A1', 'LIABILITY'), ('arcfirm', 'INCOME:A', 'INCOME') ON CONFLICT DO NOTHING;
INSERT INTO ll.journal_entry (tenant_id, id, seq, idem_key, value_date, narration, source_kind, source_id, fingerprint, created_by)
VALUES ('arcfirm', 'JE-A1', nextval('ll.journal_seq'), 'A:1', '2019-06-01', 'Bill', 'T', '1', repeat('a', 64), 'test'),
       ('arcfirm', 'JE-A2', nextval('ll.journal_seq'), 'A:2', '2020-06-01', 'Later', 'T', '2', repeat('b', 64), 'test');
INSERT INTO ll.posting VALUES ('arcfirm', 'JE-A1', 1, 'CLIENT:A1', 10000, 0, NULL), ('arcfirm', 'JE-A1', 2, 'INCOME:A', 0, 10000, NULL),
                              ('arcfirm', 'JE-A2', 1, 'BANK:A', 5000, 0, NULL), ('arcfirm', 'JE-A2', 2, 'CLIENT:A1', 0, 5000, NULL);
INSERT INTO ll.raw_file (tenant_id, id, source, name, sha256, size_bytes, content) VALUES ('arcfirm', 'RF-A1', 'MIG-ARCHIVE', 'old.pdf', repeat('c', 64), 3, '\x010203');
INSERT INTO ll.trade_event (tenant_id, origin, exchange, segment, trade_date, trade_no, ucc, symbol, instrument_type, side, qty, price, file_id)
VALUES ('arcfirm', 'EXCHANGE', 'NSE', 'CM', '2019-06-01', 'T1', 'A1', 'TESTCO', 'EQ', 'B', 1, 10, 'RF-A1');
INSERT INTO ll.doc (tenant_id, coll, id, version, body, updated_by) VALUES ('arcfirm', 'mighistc', 'M1|2019-20|LEDGER|000000', 1, '{"gz":"x"}', 'test');
UPDATE ll.doc SET version = 2, body = '{"gz":"y"}' WHERE tenant_id = 'arcfirm' AND coll = 'mighistc';
INSERT INTO ll.archive_period (tenant_id, id, state, period_from, period_to, requested_by, record)
VALUES ('arcfirm', 'AR-20261120-110000-aaaa', 'Requested', '2019-04-01', '2020-03-31', 'admin', '{}');
COMMIT;

-- 1. Nothing is removed without an approved, verified record.
DO $$ BEGIN
  PERFORM set_config('app.tenant_id', 'arcfirm', true);
  BEGIN
    PERFORM ll.archive_remove('arcfirm', 'AR-20261120-110000-aaaa', 'journal_entry', ARRAY['JE-A1']);
    RAISE EXCEPTION 'TEST FAILED: removed without approval';
  EXCEPTION WHEN object_not_in_prerequisite_state THEN NULL;
  END;
END $$;
\echo 'ok 1 - no removal without an approved, verified archive'

-- 2. The approver must be a second person, and the state moves forwards only.
DO $$ BEGIN
  PERFORM set_config('app.tenant_id', 'arcfirm', true);
  BEGIN
    UPDATE ll.archive_period SET state = 'Approved', approved_by = 'admin' WHERE id = 'AR-20261120-110000-aaaa';
    RAISE EXCEPTION 'TEST FAILED: approved by the person who asked';
  EXCEPTION WHEN check_violation THEN NULL;
  END;
  UPDATE ll.archive_period SET state = 'Approved', approved_by = 'compliance' WHERE id = 'AR-20261120-110000-aaaa';
  BEGIN
    UPDATE ll.archive_period SET state = 'Verified' WHERE id = 'AR-20261120-110000-aaaa';
    RAISE EXCEPTION 'TEST FAILED: verified without a package';
  EXCEPTION WHEN check_violation THEN NULL;
  END;
  UPDATE ll.archive_period SET state = 'Verified', package_sha256 = repeat('d', 64), verified_at = now() WHERE id = 'AR-20261120-110000-aaaa';
  BEGIN
    UPDATE ll.archive_period SET state = 'Requested' WHERE id = 'AR-20261120-110000-aaaa';
    RAISE EXCEPTION 'TEST FAILED: state moved back';
  EXCEPTION WHEN raise_exception THEN NULL;
  END;
  BEGIN
    UPDATE ll.archive_period SET period_to = '2021-03-31' WHERE id = 'AR-20261120-110000-aaaa';
    RAISE EXCEPTION 'TEST FAILED: the period changed after approval';
  EXCEPTION WHEN raise_exception THEN NULL;
  END;
END $$;
\echo 'ok 2 - second person, forwards only, period fixed once approved'

-- 3. A key outside the period stops the whole removal; the app role cannot delete.
DO $$ BEGIN
  PERFORM set_config('app.tenant_id', 'arcfirm', true);
  BEGIN
    PERFORM ll.archive_remove('arcfirm', 'AR-20261120-110000-aaaa', 'journal_entry', ARRAY['JE-A1', 'JE-A2']);
    RAISE EXCEPTION 'TEST FAILED: removed an entry outside the period';
  EXCEPTION WHEN object_not_in_prerequisite_state THEN NULL;
  END;
  IF NOT EXISTS (SELECT 1 FROM ll.journal_entry WHERE id = 'JE-A1') THEN
    RAISE EXCEPTION 'TEST FAILED: a refused removal removed an entry';
  END IF;
  BEGIN
    DELETE FROM ll.journal_entry WHERE id = 'JE-A1';
    RAISE EXCEPTION 'TEST FAILED: the application role deleted';
  EXCEPTION WHEN insufficient_privilege THEN NULL;
  END;
  BEGIN
    PERFORM set_config('ll.archiving', 'arcfirm', true);
    UPDATE ll.doc SET version = 3, body = '{"gz":"z"}' WHERE tenant_id = 'arcfirm' AND coll = 'mighistc';
    IF (SELECT count(*) FROM ll.doc_history WHERE tenant_id = 'arcfirm' AND coll = 'mighistc') <> 3 THEN
      RAISE EXCEPTION 'TEST FAILED: setting ll.archiving skipped the document history';
    END IF;
    RAISE EXCEPTION 'rollback';
  EXCEPTION WHEN raise_exception THEN
    IF SQLERRM LIKE 'TEST FAILED%' THEN RAISE; END IF;
  END;
END $$;
\echo 'ok 3 - nothing outside the period; no DELETE; history kept for the app role'

-- 4. Archiving: the period's rows go and a carried-forward entry stands in.
BEGIN;
SELECT set_config('app.tenant_id', 'arcfirm', true);
SELECT ll.archive_remove('arcfirm', 'AR-20261120-110000-aaaa', 'trade_event', ARRAY['EXCHANGE|NSE|CM|2019-06-01|T1']) = 1 AS trades \gset
SELECT ll.archive_remove('arcfirm', 'AR-20261120-110000-aaaa', 'journal_entry', ARRAY['JE-A1']) = 1 AS entries \gset
SELECT ll.archive_remove('arcfirm', 'AR-20261120-110000-aaaa', 'doc_history', ARRAY(SELECT seq::text FROM ll.doc_history WHERE tenant_id = 'arcfirm' AND coll = 'mighistc')) = 2 AS hist \gset
SELECT ll.archive_remove('arcfirm', 'AR-20261120-110000-aaaa', 'doc', ARRAY['mighistc|M1|2019-20|LEDGER|000000']) = 1 AS docs \gset
SELECT ll.archive_remove('arcfirm', 'AR-20261120-110000-aaaa', 'raw_file', ARRAY['RF-A1']) = 1 AS files \gset
INSERT INTO ll.journal_entry (tenant_id, id, seq, idem_key, value_date, narration, source_kind, source_id, fingerprint, created_by)
VALUES ('arcfirm', 'JE-C1', nextval('ll.journal_seq'), 'ARCHIVE-CARRY:AR-20261120-110000-aaaa:1', '2020-03-31', 'Carried forward', 'ARCHIVE-CARRY', 'AR-20261120-110000-aaaa', repeat('e', 64), 'test');
INSERT INTO ll.posting VALUES ('arcfirm', 'JE-C1', 1, 'CLIENT:A1', 10000, 0, NULL), ('arcfirm', 'JE-C1', 2, 'INCOME:A', 0, 10000, NULL);
UPDATE ll.archive_period SET state = 'Archived', carry_entries = 1 WHERE id = 'AR-20261120-110000-aaaa';
COMMIT;
DO $$ BEGIN
  PERFORM set_config('app.tenant_id', 'arcfirm', true);
  IF EXISTS (SELECT 1 FROM ll.journal_entry WHERE id = 'JE-A1') OR EXISTS (SELECT 1 FROM ll.raw_file WHERE id = 'RF-A1') OR EXISTS (SELECT 1 FROM ll.doc WHERE tenant_id = 'arcfirm' AND coll = 'mighistc') THEN
    RAISE EXCEPTION 'TEST FAILED: archived rows are still live';
  END IF;
  IF (SELECT sum(debit_paise) - sum(credit_paise) FROM ll.posting WHERE tenant_id = 'arcfirm' AND account_code = 'CLIENT:A1') <> 5000 THEN
    RAISE EXCEPTION 'TEST FAILED: the client balance changed';
  END IF;
END $$;
\echo 'ok 4 - archived rows removed, balances carried forward'

-- 5. Restoring needs a second person; rows come back exactly and the carried-forward entry goes.
DO $$
DECLARE r jsonb;
BEGIN
  PERFORM set_config('app.tenant_id', 'arcfirm', true);
  UPDATE ll.archive_period SET state = 'Restore requested', restore_requested_by = 'admin' WHERE id = 'AR-20261120-110000-aaaa';
  BEGIN
    PERFORM ll.archive_restore('arcfirm', 'AR-20261120-110000-aaaa', 'raw_file', '[]'::jsonb || jsonb_build_object('tenant_id', 'arcfirm'));
    RAISE EXCEPTION 'TEST FAILED: restored without approval';
  EXCEPTION WHEN object_not_in_prerequisite_state THEN NULL;
  END;
  BEGIN
    UPDATE ll.archive_period SET state = 'Restore approved', restore_approved_by = 'admin' WHERE id = 'AR-20261120-110000-aaaa';
    RAISE EXCEPTION 'TEST FAILED: the restore approved by the person who asked';
  EXCEPTION WHEN check_violation THEN NULL;
  END;
  UPDATE ll.archive_period SET state = 'Restore approved', restore_approved_by = 'compliance' WHERE id = 'AR-20261120-110000-aaaa';
  PERFORM ll.archive_restore('arcfirm', 'AR-20261120-110000-aaaa', 'raw_file',
    jsonb_build_array(jsonb_build_object('tenant_id', 'arcfirm', 'id', 'RF-A1', 'source', 'MIG-ARCHIVE', 'name', 'old.pdf', 'sha256', repeat('c', 64), 'size_bytes', 3, 'received_at', now(), 'content', '\x010203')));
  PERFORM ll.archive_restore('arcfirm', 'AR-20261120-110000-aaaa', 'journal_entry',
    jsonb_build_array(jsonb_build_object('tenant_id', 'arcfirm', 'id', 'JE-A1', 'seq', 999001, 'idem_key', 'A:1', 'value_date', '2019-06-01', 'narration', 'Bill', 'source_kind', 'T', 'source_id', '1', 'fingerprint', repeat('a', 64), 'created_by', 'test', 'created_at', now())));
  PERFORM ll.archive_restore('arcfirm', 'AR-20261120-110000-aaaa', 'posting',
    jsonb_build_array(jsonb_build_object('tenant_id', 'arcfirm', 'entry_id', 'JE-A1', 'line', 1, 'account_code', 'CLIENT:A1', 'debit_paise', 10000, 'credit_paise', 0),
                      jsonb_build_object('tenant_id', 'arcfirm', 'entry_id', 'JE-A1', 'line', 2, 'account_code', 'INCOME:A', 'debit_paise', 0, 'credit_paise', 10000)));
  BEGIN
    PERFORM ll.archive_restore('arcfirm', 'AR-20261120-110000-aaaa', 'journal_entry',
      jsonb_build_array(jsonb_build_object('tenant_id', 'arcfirm', 'id', 'JE-X', 'seq', 999002, 'idem_key', 'X:1', 'value_date', '2021-06-01', 'narration', 'x', 'source_kind', 'T', 'source_id', '1', 'fingerprint', repeat('a', 64), 'created_by', 'test', 'created_at', now())));
    RAISE EXCEPTION 'TEST FAILED: restored an entry outside the period';
  EXCEPTION WHEN object_not_in_prerequisite_state THEN NULL;
  END;
  PERFORM ll.archive_restore('arcfirm', 'AR-20261120-110000-aaaa', 'doc',
    jsonb_build_array(jsonb_build_object('tenant_id', 'arcfirm', 'coll', 'mighistc', 'id', 'M1|2019-20|LEDGER|000000', 'version', 2, 'body', '{"gz":"y"}'::jsonb, 'updated_at', now(), 'updated_by', 'test')));
  IF (SELECT count(*) FROM ll.doc_history WHERE tenant_id = 'arcfirm' AND coll = 'mighistc') <> 0 THEN
    RAISE EXCEPTION 'TEST FAILED: restoring a record wrote a new history row';
  END IF;
  IF ll.archive_drop_carry('arcfirm', 'AR-20261120-110000-aaaa') <> 1 THEN
    RAISE EXCEPTION 'TEST FAILED: the carried-forward entry was not removed';
  END IF;
  UPDATE ll.archive_period SET state = 'Restored' WHERE id = 'AR-20261120-110000-aaaa';
  IF (SELECT sum(debit_paise) - sum(credit_paise) FROM ll.posting WHERE tenant_id = 'arcfirm' AND account_code = 'CLIENT:A1') <> 5000
     OR NOT EXISTS (SELECT 1 FROM ll.journal_entry WHERE id = 'JE-A1' AND seq = 999001) OR EXISTS (SELECT 1 FROM ll.journal_entry WHERE id = 'JE-C1') THEN
    RAISE EXCEPTION 'TEST FAILED: the restore did not put the books back';
  END IF;
END $$;
\echo 'ok 5 - restore needs a second person and puts the rows back'

-- 6. The current financial year is never removed.
DO $$ BEGIN
  PERFORM set_config('app.tenant_id', 'arcfirm', true);
  INSERT INTO ll.archive_period (tenant_id, id, state, period_from, period_to, requested_by, approved_by, package_sha256, verified_at, record)
  VALUES ('arcfirm', 'AR-20261120-110000-bbbb', 'Requested', ll.current_fy_start(), ll.current_fy_start() + 10, 'admin', NULL, NULL, NULL, '{}');
  UPDATE ll.archive_period SET state = 'Approved', approved_by = 'compliance' WHERE id = 'AR-20261120-110000-bbbb';
  UPDATE ll.archive_period SET state = 'Verified', package_sha256 = repeat('f', 64), verified_at = now() WHERE id = 'AR-20261120-110000-bbbb';
  BEGIN
    PERFORM ll.archive_remove('arcfirm', 'AR-20261120-110000-bbbb', 'journal_entry', ARRAY['JE-A2']);
    RAISE EXCEPTION 'TEST FAILED: removed in the current financial year';
  EXCEPTION WHEN object_not_in_prerequisite_state THEN NULL;
  END;
  BEGIN
    DELETE FROM ll.archive_period WHERE id = 'AR-20261120-110000-bbbb';
    RAISE EXCEPTION 'TEST FAILED: an archive record was deleted';
  EXCEPTION WHEN insufficient_privilege THEN NULL;
  END;
END $$;
\echo 'ok 6 - never the current financial year; records are not deleted'
RESET ROLE;
\echo 'archive SQL tests passed'
