-- Hands the restore test database to its own owner role, as on a hosted
-- database where the account that restores owns the tables but is not a
-- superuser. Used by CI for TestIntegrationBackupRestore.
ALTER SCHEMA ll OWNER TO ll_owner;
DO $$
DECLARE r record;
BEGIN
  FOR r IN SELECT tablename FROM pg_tables WHERE schemaname = 'll' LOOP
    EXECUTE format('ALTER TABLE ll.%I OWNER TO ll_owner', r.tablename);
  END LOOP;
  FOR r IN SELECT sequence_name FROM information_schema.sequences WHERE sequence_schema = 'll' LOOP
    EXECUTE format('ALTER SEQUENCE ll.%I OWNER TO ll_owner', r.sequence_name);
  END LOOP;
END $$;
