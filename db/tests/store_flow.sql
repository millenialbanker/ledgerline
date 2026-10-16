-- Executes the store's statements in the order the Go code issues them.
-- Generated PREPAREs are included first (see internal/pgstore TestDumpPrepared).
SET ROLE ll_test_app;
BEGIN;
SELECT set_config('app.tenant_id', 'alpha', true);
-- ledger: post a two-leg entry the way Append does
EXECUTE q_upsert_account('alpha', 'CLIENT:BH2001', 'LIABILITY');
EXECUTE q_upsert_account('alpha', 'CC:NSE-CM', 'ASSET');
EXECUTE q_insert_entry('alpha', 'BILL:2026-10-16:BH2001:NSE-CM', '2026-10-16', 'Bill NSE-CM', 'BILL', '2026-10-16/BH2001/NSE-CM', 0, '', repeat('f',64), 'eod');
SELECT id AS je FROM ll.journal_entry WHERE idem_key = 'BILL:2026-10-16:BH2001:NSE-CM' \gset
EXECUTE q_insert_posting('alpha', :'je', 1, 'CLIENT:BH2001', 1413675, 0, 'Trade value');
EXECUTE q_insert_posting('alpha', :'je', 2, 'CC:NSE-CM', 0, 1413675, '');
COMMIT;
BEGIN;
SELECT set_config('app.tenant_id', 'alpha', true);
EXECUTE q_entry_by_key('alpha', 'BILL:2026-10-16:BH2001:NSE-CM');
EXECUTE q_list_entries('alpha', '', '2026-10-01', '2026-10-31', 'CLIENT:BH2001');
-- raw files: second identical upload gets prior_id
EXECUTE q_insert_raw('alpha', 'NSE-CM-TRADE', 'a.csv', repeat('1',64), 3, '\x616263');
EXECUTE q_insert_raw('alpha', 'NSE-CM-TRADE', 'b.csv', repeat('1',64), 3, '\x616263');
SELECT count(*) FILTER (WHERE prior_id IS NOT NULL) AS with_prior FROM ll.raw_file \gset
\if :with_prior
\else
  \echo 'FAIL: prior_id not set on duplicate upload' \\ \quit
\endif
SELECT min(id) AS rf FROM ll.raw_file \gset
-- trades: insert, duplicate is a no-op, list by key
EXECUTE q_insert_trade('alpha','EXCHANGE','NSE','FO','2026-10-16','2000311402','1400000022100002','BH1004','NIFTY','CE','2026-10-20','25900','S',130,'112.50','10:02:45','NRML','ALG51022','API-GW-01',:'rf',3);
EXECUTE q_insert_trade('alpha','EXCHANGE','NSE','FO','2026-10-16','2000311402','1400000022100002','BH1004','NIFTY','CE','2026-10-20','25900','S',130,'112.50','10:02:45','NRML','ALG51022','API-GW-01',:'rf',3);
EXECUTE q_list_trades('alpha', 'EXCHANGE', '2026-10-16', 'NSE|FO|2026-10-16|2000311402');
-- breaks: open, rerun without it clears, rerun with it reopens, resolve
EXECUTE q_insert_run('alpha','RUN-1','2026-10-16',17,17,11,7,now());
EXECUTE q_upsert_break('alpha','BRK-x','2026-10-16','QTY_MISMATCH','NSE','CM','1000452307','','BH1002','LT|EQ||','18752.5','[{"field":"qty","exchange":"5","oms":"6"}]','fix',now(),'RUN-1');
EXECUTE q_insert_run('alpha','RUN-2','2026-10-16',17,17,12,6,now());
EXECUTE q_clear_breaks('alpha','2026-10-16','RUN-2');
EXECUTE q_list_breaks('alpha','2026-10-16','CLEARED','');
EXECUTE q_upsert_break('alpha','BRK-x','2026-10-16','QTY_MISMATCH','NSE','CM','1000452307','','BH1002','LT|EQ||','18752.5','[]','fix',now(),'RUN-3');
EXECUTE q_resolve_break('alpha','BRK-x','Fixed OMS fill','ops',now());
EXECUTE q_list_breaks('alpha','','','BRK-x');
EXECUTE q_insert_audit('alpha', now(), 'ops', 'recon.break.resolved', 'BRK-x', '{"resolution":"Fixed OMS fill"}');
EXECUTE q_list_audit('alpha', 5);
-- documents: insert once, stale update refused, version advances, history kept, prefix list escapes LIKE
EXECUTE q_doc_insert('alpha','client','BH1001','{"name":"A"}','ops');
EXECUTE q_doc_insert('alpha','client','BH1001','{"name":"dup"}','ops');
EXECUTE q_doc_insert('alpha','client','BH_1','{"name":"underscore"}','ops');
EXECUTE q_doc_update('alpha','client','BH1001','{"name":"B"}','ops',1);
EXECUTE q_doc_update('alpha','client','BH1001','{"name":"stale"}','ops',1);
SELECT (SELECT version FROM ll.doc WHERE coll='client' AND id='BH1001') = 2
   AND (SELECT body->>'name' FROM ll.doc WHERE coll='client' AND id='BH1001') = 'B'
   AND (SELECT count(*) FROM ll.doc_history WHERE coll='client' AND id='BH1001') = 2 AS docs_ok \gset
\if :docs_ok
\else
  \echo 'FAIL: document versioning' \\ \quit
\endif
SELECT count(*) = 1 AS like_ok FROM ll.doc WHERE tenant_id='alpha' AND coll='client' AND id LIKE replace(replace(replace('BH_', '\', '\\'), '%', '\%'), '_', '\_') || '%' \gset
\if :like_ok
\else
  \echo 'FAIL: doc prefix must treat _ literally' \\ \quit
\endif
EXECUTE q_doc_list('alpha','client','BH');
EXECUTE q_doc_get('alpha','client','BH1001');
COMMIT;
\echo 'store flow passed'
