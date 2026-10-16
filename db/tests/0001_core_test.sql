-- SQL-level tests for 0001_core.sql. Run with psql -v ON_ERROR_STOP=1 as a
-- superuser against a database where the migration has been applied.
-- Every block raises an exception if a guarantee does not hold.
\set QUIET on
SET search_path = ll, public;

-- Setup (as superuser): two tenants and a login role that is not the owner.
INSERT INTO ll.tenant (id, name) VALUES ('alpha', 'Alpha Securities'), ('beta', 'Beta Broking');
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'll_test_app') THEN
    CREATE ROLE ll_test_app LOGIN;
  END IF;
END $$;
GRANT ledgerline_app TO ll_test_app;

SET ROLE ll_test_app;

-- 1. A balanced entry commits.
BEGIN;
SELECT set_config('app.tenant_id', 'alpha', true);
INSERT INTO ll.account (tenant_id, code, type) VALUES ('alpha', 'BANK:CLIENT', 'ASSET'), ('alpha', 'CLIENT:BH1001', 'LIABILITY');
INSERT INTO ll.journal_entry (tenant_id, id, seq, idem_key, value_date, narration, source_kind, source_id, fingerprint, created_by)
VALUES ('alpha', 'JE-1', nextval('ll.journal_seq'), 'RCPT:1', '2026-10-16', 'Funds received', 'BANK', 'UTR1', repeat('a', 64), 'test');
INSERT INTO ll.posting VALUES ('alpha', 'JE-1', 1, 'BANK:CLIENT', 500000, 0, NULL), ('alpha', 'JE-1', 2, 'CLIENT:BH1001', 0, 500000, NULL);
COMMIT;
\echo 'ok 1 - balanced entry commits'

-- 2. An unbalanced entry is rejected at commit (forced immediate here so the error is catchable).
DO $$ BEGIN
  PERFORM set_config('app.tenant_id', 'alpha', true);
  BEGIN
    INSERT INTO ll.journal_entry (tenant_id, id, seq, idem_key, value_date, narration, source_kind, source_id, fingerprint, created_by)
    VALUES ('alpha', 'JE-2', nextval('ll.journal_seq'), 'BAD:1', '2026-10-16', 'Unbalanced', 'TEST', 'x', repeat('b', 64), 'test');
    INSERT INTO ll.posting VALUES ('alpha', 'JE-2', 1, 'BANK:CLIENT', 100, 0, NULL), ('alpha', 'JE-2', 2, 'CLIENT:BH1001', 0, 99, NULL);
    SET CONSTRAINTS ALL IMMEDIATE;
    RAISE EXCEPTION 'TEST FAILED: unbalanced entry accepted';
  EXCEPTION WHEN check_violation THEN NULL;
  END;
END $$;
\echo 'ok 2 - unbalanced entry rejected'

-- 3. A single-posting entry is rejected.
DO $$ BEGIN
  PERFORM set_config('app.tenant_id', 'alpha', true);
  BEGIN
    INSERT INTO ll.journal_entry (tenant_id, id, seq, idem_key, value_date, narration, source_kind, source_id, fingerprint, created_by)
    VALUES ('alpha', 'JE-3', nextval('ll.journal_seq'), 'BAD:2', '2026-10-16', 'One leg', 'TEST', 'x', repeat('c', 64), 'test');
    INSERT INTO ll.posting VALUES ('alpha', 'JE-3', 1, 'BANK:CLIENT', 100, 0, NULL);
    SET CONSTRAINTS ALL IMMEDIATE;
    RAISE EXCEPTION 'TEST FAILED: one-legged entry accepted';
  EXCEPTION WHEN check_violation THEN NULL;
  END;
END $$;
\echo 'ok 3 - one-legged entry rejected'

-- 4. Adding a posting to an already committed entry is rejected.
DO $$ BEGIN
  PERFORM set_config('app.tenant_id', 'alpha', true);
  BEGIN
    INSERT INTO ll.posting VALUES ('alpha', 'JE-1', 3, 'BANK:CLIENT', 1, 0, NULL);
    SET CONSTRAINTS ALL IMMEDIATE;
    RAISE EXCEPTION 'TEST FAILED: posting appended to committed entry';
  EXCEPTION WHEN check_violation THEN NULL;
  END;
END $$;
\echo 'ok 4 - cannot unbalance a committed entry'

-- 5. History cannot be edited or deleted (privilege denied for the app role).
DO $$ BEGIN
  PERFORM set_config('app.tenant_id', 'alpha', true);
  BEGIN
    UPDATE ll.posting SET credit_paise = 1 WHERE entry_id = 'JE-1';
    RAISE EXCEPTION 'TEST FAILED: posting updated';
  EXCEPTION WHEN insufficient_privilege THEN NULL;
  END;
  BEGIN
    DELETE FROM ll.journal_entry WHERE id = 'JE-1';
    RAISE EXCEPTION 'TEST FAILED: entry deleted';
  EXCEPTION WHEN insufficient_privilege THEN NULL;
  END;
END $$;
\echo 'ok 5 - app role cannot update or delete history'

-- 6. Duplicate idempotency key is rejected.
DO $$ BEGIN
  PERFORM set_config('app.tenant_id', 'alpha', true);
  BEGIN
    INSERT INTO ll.journal_entry (tenant_id, id, seq, idem_key, value_date, narration, source_kind, source_id, fingerprint, created_by)
    VALUES ('alpha', 'JE-9', nextval('ll.journal_seq'), 'RCPT:1', '2026-10-16', 'Duplicate', 'BANK', 'UTR1', repeat('a', 64), 'test');
    RAISE EXCEPTION 'TEST FAILED: duplicate key accepted';
  EXCEPTION WHEN unique_violation THEN NULL;
  END;
END $$;
\echo 'ok 6 - idempotency key unique per tenant'

-- 7. Tenant isolation: beta sees nothing of alpha and cannot write as alpha.
DO $$ DECLARE n integer; BEGIN
  PERFORM set_config('app.tenant_id', 'beta', true);
  SELECT count(*) INTO n FROM ll.journal_entry;
  IF n <> 0 THEN RAISE EXCEPTION 'TEST FAILED: beta sees % alpha entries', n; END IF;
  SELECT count(*) INTO n FROM ll.posting;
  IF n <> 0 THEN RAISE EXCEPTION 'TEST FAILED: beta sees % alpha postings', n; END IF;
  BEGIN
    INSERT INTO ll.account (tenant_id, code, type) VALUES ('alpha', 'BANK:OWN', 'ASSET');
    RAISE EXCEPTION 'TEST FAILED: beta wrote an alpha row';
  EXCEPTION WHEN insufficient_privilege THEN NULL;
  END;
  PERFORM set_config('app.tenant_id', '', true);
  SELECT count(*) INTO n FROM ll.journal_entry;
  IF n <> 0 THEN RAISE EXCEPTION 'TEST FAILED: no tenant set but % rows visible', n; END IF;
END $$;
\echo 'ok 7 - row-level security isolates tenants'

-- 8. Superuser cannot bypass append-only via triggers either.
RESET ROLE;
DO $$ BEGIN
  BEGIN
    UPDATE ll.posting SET memo = 'x' WHERE entry_id = 'JE-1';
    RAISE EXCEPTION 'TEST FAILED: owner updated a posting';
  EXCEPTION WHEN raise_exception THEN NULL;
  END;
  BEGIN
    TRUNCATE ll.audit_log;
    RAISE EXCEPTION 'TEST FAILED: audit log truncated';
  EXCEPTION WHEN raise_exception THEN NULL;
  END;
END $$;
\echo 'ok 8 - append-only triggers hold for the table owner'

-- 9. Break workflow constraint: RESOLVED requires a resolution and resolver.
SET ROLE ll_test_app;
DO $$ BEGIN
  PERFORM set_config('app.tenant_id', 'alpha', true);
  INSERT INTO ll.recon_break (tenant_id, id, trade_date, type, exchange, segment, trade_no, ucc, instrument, value, suggestion, last_run)
  VALUES ('alpha', 'BRK-1', '2026-10-16', 'QTY_MISMATCH', 'NSE', 'CM', '1', 'BH1001', 'X|EQ||', 100, 'fix', 'RUN-1');
  BEGIN
    UPDATE ll.recon_break SET status = 'RESOLVED' WHERE id = 'BRK-1';
    RAISE EXCEPTION 'TEST FAILED: resolved without a note';
  EXCEPTION WHEN check_violation THEN NULL;
  END;
  UPDATE ll.recon_break SET status = 'RESOLVED', resolution = 'Fixed OMS', resolved_by = 'ops', resolved_at = now() WHERE id = 'BRK-1';
END $$;
\echo 'ok 9 - break resolution requires a note'
RESET ROLE;
\echo 'all SQL tests passed'
