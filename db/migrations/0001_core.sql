-- Ledgerline core schema.
--
-- Guarantees enforced by the database itself, not only by application code:
--   * Tenant isolation: every table has row-level security keyed on the
--     app.tenant_id setting, forced even for the table owner.
--   * The journal is append-only: UPDATE and DELETE on journal tables, raw
--     files, trade events and the audit log raise an error.
--   * Every journal entry balances: a deferred constraint trigger checks at
--     commit that debits equal credits and that there are at least 2 postings.
--   * Idempotency: (tenant_id, idem_key) is unique.

BEGIN;

CREATE SCHEMA IF NOT EXISTS ll;
SET search_path = ll, public;

-- Application role. Grant it to the login role the service uses.
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ledgerline_app') THEN
    CREATE ROLE ledgerline_app NOLOGIN;
  END IF;
END $$;

CREATE OR REPLACE FUNCTION ll.current_tenant() RETURNS text
LANGUAGE sql STABLE AS $$ SELECT nullif(current_setting('app.tenant_id', true), '') $$;

CREATE OR REPLACE FUNCTION ll.forbid_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% is append-only; % is not allowed', TG_TABLE_NAME, TG_OP
    USING ERRCODE = 'P0001', HINT = 'Post a reversing entry instead of editing history.';
END $$;

CREATE TABLE ll.tenant (
  id          text PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9_-]{1,62}$'),
  name        text NOT NULL,
  sebi_reg    text,
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ll.raw_file (
  tenant_id    text NOT NULL REFERENCES ll.tenant(id),
  id           text NOT NULL,
  source       text NOT NULL,
  name         text NOT NULL,
  sha256       char(64) NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  size_bytes   bigint NOT NULL CHECK (size_bytes >= 0),
  prior_id     text,
  received_at  timestamptz NOT NULL DEFAULT now(),
  content      bytea NOT NULL,
  PRIMARY KEY (tenant_id, id)
);
CREATE INDEX raw_file_sha ON ll.raw_file (tenant_id, source, sha256);
CREATE SEQUENCE ll.raw_file_seq;

CREATE TABLE ll.trade_event (
  tenant_id        text NOT NULL REFERENCES ll.tenant(id),
  origin           text NOT NULL CHECK (origin IN ('EXCHANGE', 'OMS')),
  exchange         text NOT NULL,
  segment          text NOT NULL CHECK (segment IN ('CM', 'FO', 'CD', 'COM')),
  trade_date       date NOT NULL,
  trade_no         text NOT NULL,
  order_no         text,
  ucc              text NOT NULL,
  symbol           text NOT NULL,
  instrument_type  text NOT NULL CHECK (instrument_type IN ('EQ', 'FUT', 'CE', 'PE')),
  expiry           date,
  strike           numeric(20, 6),
  side             char(1) NOT NULL CHECK (side IN ('B', 'S')),
  qty              bigint NOT NULL CHECK (qty > 0),
  price            numeric(24, 8) NOT NULL CHECK (price >= 0),
  trade_time       time,
  product          text,
  algo_id          text,
  terminal         text,
  file_id          text,
  line             integer,
  created_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, origin, exchange, segment, trade_date, trade_no),
  FOREIGN KEY (tenant_id, file_id) REFERENCES ll.raw_file (tenant_id, id),
  CHECK (instrument_type = 'EQ' OR expiry IS NOT NULL),
  CHECK (instrument_type NOT IN ('CE', 'PE') OR strike IS NOT NULL)
);
CREATE INDEX trade_event_client ON ll.trade_event (tenant_id, ucc, trade_date);

CREATE TABLE ll.recon_run (
  tenant_id       text NOT NULL REFERENCES ll.tenant(id),
  id              text NOT NULL,
  trade_date      date NOT NULL,
  exchange_count  integer NOT NULL,
  oms_count       integer NOT NULL,
  matched         integer NOT NULL,
  breaks          integer NOT NULL,
  run_at          timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);

-- Breaks carry workflow state, so they are updatable (status, resolution);
-- every change is also written to the audit log by the application.
CREATE TABLE ll.recon_break (
  tenant_id     text NOT NULL REFERENCES ll.tenant(id),
  id            text NOT NULL,
  trade_date    date NOT NULL,
  type          text NOT NULL,
  exchange      text NOT NULL,
  segment       text NOT NULL,
  trade_no      text NOT NULL,
  oms_trade_no  text,
  ucc           text NOT NULL,
  instrument    text NOT NULL,
  value         numeric(24, 8) NOT NULL,
  diffs         jsonb NOT NULL DEFAULT '[]',
  suggestion    text NOT NULL,
  status        text NOT NULL DEFAULT 'OPEN' CHECK (status IN ('OPEN', 'RESOLVED', 'CLEARED')),
  resolution    text,
  resolved_by   text,
  resolved_at   timestamptz,
  first_seen    timestamptz NOT NULL DEFAULT now(),
  last_run      text NOT NULL,
  PRIMARY KEY (tenant_id, id),
  CHECK (status <> 'RESOLVED' OR (resolution IS NOT NULL AND resolved_by IS NOT NULL))
);
CREATE INDEX recon_break_open ON ll.recon_break (tenant_id, trade_date, status);

CREATE TABLE ll.charge_schedule (
  tenant_id       text NOT NULL REFERENCES ll.tenant(id),
  version         text NOT NULL,
  is_sample       boolean NOT NULL DEFAULT false,
  body            jsonb NOT NULL,
  loaded_by       text NOT NULL,
  loaded_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, version)
);

CREATE TABLE ll.account (
  tenant_id  text NOT NULL REFERENCES ll.tenant(id),
  code       text NOT NULL CHECK (code ~ '^[A-Z]+:[^\s]+$'),
  type       text NOT NULL CHECK (type IN ('ASSET', 'LIABILITY', 'INCOME', 'EXPENSE', 'EQUITY')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, code)
);

CREATE SEQUENCE ll.journal_seq;

CREATE TABLE ll.journal_entry (
  tenant_id    text NOT NULL REFERENCES ll.tenant(id),
  id           text NOT NULL,
  seq          bigint NOT NULL UNIQUE,
  idem_key     text NOT NULL,
  value_date   date NOT NULL,
  narration    text NOT NULL CHECK (length(trim(narration)) > 0),
  source_kind  text NOT NULL,
  source_id    text NOT NULL,
  source_line  integer,
  reversal_of  text,
  fingerprint  char(64) NOT NULL,
  created_by   text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  UNIQUE (tenant_id, idem_key),
  FOREIGN KEY (tenant_id, reversal_of) REFERENCES ll.journal_entry (tenant_id, id)
);
-- One reversal per entry.
CREATE UNIQUE INDEX journal_one_reversal ON ll.journal_entry (tenant_id, reversal_of) WHERE reversal_of IS NOT NULL;

CREATE TABLE ll.posting (
  tenant_id     text NOT NULL,
  entry_id      text NOT NULL,
  line          smallint NOT NULL CHECK (line > 0),
  account_code  text NOT NULL,
  debit_paise   bigint NOT NULL DEFAULT 0 CHECK (debit_paise >= 0),
  credit_paise  bigint NOT NULL DEFAULT 0 CHECK (credit_paise >= 0),
  memo          text,
  PRIMARY KEY (tenant_id, entry_id, line),
  FOREIGN KEY (tenant_id, entry_id) REFERENCES ll.journal_entry (tenant_id, id),
  FOREIGN KEY (tenant_id, account_code) REFERENCES ll.account (tenant_id, code),
  CHECK ((debit_paise > 0) <> (credit_paise > 0))
);
CREATE INDEX posting_account ON ll.posting (tenant_id, account_code);

CREATE TABLE ll.audit_log (
  id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id  text NOT NULL REFERENCES ll.tenant(id),
  at         timestamptz NOT NULL DEFAULT now(),
  actor      text NOT NULL,
  action     text NOT NULL,
  object     text NOT NULL DEFAULT '',
  detail     jsonb
);
CREATE INDEX audit_log_tenant_at ON ll.audit_log (tenant_id, at DESC);

-- Balance check, run at commit for every new entry and every new posting.
CREATE OR REPLACE FUNCTION ll.check_entry_balanced(p_tenant text, p_entry text) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE d bigint; c bigint; n integer;
BEGIN
  SELECT coalesce(sum(debit_paise), 0), coalesce(sum(credit_paise), 0), count(*)
    INTO d, c, n
    FROM ll.posting WHERE tenant_id = p_tenant AND entry_id = p_entry;
  IF n < 2 THEN
    RAISE EXCEPTION 'journal entry % has % posting(s); at least 2 are required', p_entry, n USING ERRCODE = '23514';
  END IF;
  IF d <> c THEN
    RAISE EXCEPTION 'journal entry % is unbalanced: debits % paise, credits % paise', p_entry, d, c USING ERRCODE = '23514';
  END IF;
END $$;

CREATE OR REPLACE FUNCTION ll.trg_entry_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN PERFORM ll.check_entry_balanced(NEW.tenant_id, NEW.id); RETURN NULL; END $$;

CREATE OR REPLACE FUNCTION ll.trg_posting_balanced() RETURNS trigger
LANGUAGE plpgsql AS $$ BEGIN PERFORM ll.check_entry_balanced(NEW.tenant_id, NEW.entry_id); RETURN NULL; END $$;

CREATE CONSTRAINT TRIGGER journal_entry_balanced AFTER INSERT ON ll.journal_entry
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION ll.trg_entry_balanced();
CREATE CONSTRAINT TRIGGER posting_balanced AFTER INSERT ON ll.posting
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION ll.trg_posting_balanced();

-- Append-only tables.
CREATE TRIGGER journal_entry_append_only BEFORE UPDATE OR DELETE ON ll.journal_entry FOR EACH ROW EXECUTE FUNCTION ll.forbid_mutation();
CREATE TRIGGER posting_append_only       BEFORE UPDATE OR DELETE ON ll.posting       FOR EACH ROW EXECUTE FUNCTION ll.forbid_mutation();
CREATE TRIGGER raw_file_append_only      BEFORE UPDATE OR DELETE ON ll.raw_file      FOR EACH ROW EXECUTE FUNCTION ll.forbid_mutation();
CREATE TRIGGER trade_event_append_only   BEFORE UPDATE OR DELETE ON ll.trade_event   FOR EACH ROW EXECUTE FUNCTION ll.forbid_mutation();
CREATE TRIGGER audit_log_append_only     BEFORE UPDATE OR DELETE ON ll.audit_log     FOR EACH ROW EXECUTE FUNCTION ll.forbid_mutation();
-- TRUNCATE bypasses row triggers, so block it too.
CREATE TRIGGER journal_entry_no_truncate BEFORE TRUNCATE ON ll.journal_entry EXECUTE FUNCTION ll.forbid_mutation();
CREATE TRIGGER posting_no_truncate       BEFORE TRUNCATE ON ll.posting       EXECUTE FUNCTION ll.forbid_mutation();
CREATE TRIGGER audit_log_no_truncate     BEFORE TRUNCATE ON ll.audit_log     EXECUTE FUNCTION ll.forbid_mutation();

-- Row-level security on every tenant table.
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['raw_file','trade_event','recon_run','recon_break','charge_schedule','account','journal_entry','posting','audit_log'] LOOP
    EXECUTE format('ALTER TABLE ll.%I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE ll.%I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON ll.%I USING (tenant_id = ll.current_tenant()) WITH CHECK (tenant_id = ll.current_tenant())', t);
  END LOOP;
END $$;
ALTER TABLE ll.tenant ENABLE ROW LEVEL SECURITY;
ALTER TABLE ll.tenant FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_self ON ll.tenant USING (id = ll.current_tenant());

-- Privileges for the application role: no UPDATE/DELETE on history at all.
GRANT USAGE ON SCHEMA ll TO ledgerline_app;
GRANT SELECT ON ll.tenant TO ledgerline_app;
GRANT SELECT, INSERT ON ll.raw_file, ll.trade_event, ll.recon_run, ll.charge_schedule, ll.account, ll.journal_entry, ll.posting, ll.audit_log TO ledgerline_app;
GRANT SELECT, INSERT, UPDATE ON ll.recon_break TO ledgerline_app;
GRANT USAGE ON SEQUENCE ll.journal_seq, ll.raw_file_seq TO ledgerline_app;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA ll TO ledgerline_app;

COMMIT;
