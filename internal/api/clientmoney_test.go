package api

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/app"
	"ledgerline/internal/charges"
	"ledgerline/internal/ingest"
	"ledgerline/internal/ops"
	"ledgerline/internal/sample"
	"ledgerline/internal/vault"
)

// The UCC-wise settlement report, the firm's bank accounts, transfers and
// the daily client money reconciliation, through the API. All data is made
// up: the sample firm and the accounts added here.
func TestClientMoneyRoutes(t *testing.T) {
	ops.PasswordIterations = 1000
	reg, _ := ingest.LoadDir("../../config/adapters")
	f, _ := os.Open("../../config/charges/sample-schedule.json")
	defer f.Close()
	sched, _ := charges.LoadSchedule(f)
	a := app.New(vault.Protect(app.MemoryStores(), vault.New(vault.Ephemeral())), reg, sched)
	o := ops.New(a)
	ctx := context.Background()
	if err := o.SeedDev(ctx, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.LoadSampleDay(ctx, "demo", "priya.nair", sample.Generate("2026-10-16"), false, func(ctx context.Context, tenant, actor, source, name string, data []byte) error {
		_, err := a.IngestFile(ctx, tenant, actor, source, "", name, data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 20, 0, 0, 0, time.FixedZone("IST", 19800)) }
	s := New(a, o, map[string]string{key: "demo"}, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
	s.exchangeReportingRoutes()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	const day = "2026-10-16"

	// The register: the compliance officer names the accounts.
	var fv ops.FirmBankView
	if c := ipoCall(t, srv, "GET", "/v1/firm-banks", "priya.nair", nil, &fv); c != 200 || fv.CanEdit || len(fv.Accounts) != 0 || len(fv.CCs) == 0 {
		t.Fatalf("register %d %+v", c, fv)
	}
	if c := ipoCall(t, srv, "GET", "/v1/firm-banks", "meera.iyer", nil, nil); c != 403 {
		t.Fatalf("branch staff saw the register: %d", c)
	}
	set, _ := o.Settings(ctx, "demo")
	dsc := map[string]any{"kind": "DSCNBA", "bank": "Made-up Bank", "account": set.PayoutDebitAccount, "ifsc": "MADE0000001", "reported": true, "serves": []string{"NSE", "BSE"}}
	if c := ipoCall(t, srv, "POST", "/v1/firm-banks", "priya.nair", dsc, nil); c != 403 {
		t.Fatalf("a maker changed the register: %d", c)
	}
	var b ops.FirmBank
	if c := ipoCall(t, srv, "POST", "/v1/firm-banks", "vikram.shah", dsc, &b); c != 200 || b.ID != "FB-01" || b.Account == set.PayoutDebitAccount {
		t.Fatalf("DSCNBA %d %+v", c, b)
	}
	var usc ops.FirmBank
	if c := ipoCall(t, srv, "POST", "/v1/firm-banks", "vikram.shah", map[string]any{"kind": "USCNBA", "bank": "Made-up Bank", "account": "000111222333", "ifsc": "MADE0000002", "reported": true}, &usc); c != 200 || usc.ID != "FB-02" {
		t.Fatalf("USCNBA %d %+v", c, usc)
	}
	if c := ipoCall(t, srv, "POST", "/v1/firm-banks", "vikram.shah", map[string]any{"kind": "Clearing corporation", "cc": "NCL", "bank": "Made-up Bank", "account": "000999888777", "ifsc": "MADE0000009"}, nil); c != 200 {
		t.Fatalf("clearing corporation %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/firm-banks", "vikram.shah", map[string]any{"kind": "Own", "bank": "Made-up Bank", "account": "12", "ifsc": "MADE0000004"}, nil); c != 422 {
		t.Fatalf("a two-digit account number: %d", c)
	}

	// The UCC-wise report of the settlement of the day.
	var rv ops.RASUCCView
	if c := ipoCall(t, srv, "GET", "/v1/ras/ucc-reports?date="+day, "priya.nair", nil, &rv); c != 200 || len(rv.Fields) != 17 || rv.CanRecord {
		t.Fatalf("reports %d %+v", c, rv)
	}
	if _, err := o.RunRAS(ctx, "demo", "farhan.khan", day, day); err != nil {
		t.Fatal(err)
	}
	var file ops.RASUCCFile
	if c := ipoCall(t, srv, "POST", "/v1/ras/ucc-reports/"+day+"/file", "priya.nair", map[string]bool{"new": false}, &file); c != 200 || file.Seq != 1 || !strings.HasPrefix(file.Name, "AAACB1234C_QS_16102026_") || file.Rows == 0 {
		t.Fatalf("file %d %+v", c, file)
	}
	var zipped, csvb []byte
	if c := ipoCall(t, srv, "GET", "/v1/ras/ucc-reports/"+day+"/file?seq=1", "priya.nair", nil, &zipped); c != 200 {
		t.Fatalf("zip %d %s", c, zipped)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil || len(zr.File) != 1 || zr.File[0].Name != file.CSV {
		t.Fatalf("zip %v %+v", err, zr)
	}
	if c := ipoCall(t, srv, "GET", "/v1/ras/ucc-reports/"+day+"/file?seq=1&csv=1", "priya.nair", nil, &csvb); c != 200 || !bytes.HasPrefix(csvb, []byte("Date of Settlement,")) {
		t.Fatalf("csv %d %.80s", c, csvb)
	}
	var det ops.RASUCCDetail
	if c := ipoCall(t, srv, "GET", "/v1/ras/ucc-reports/"+day, "anita.rao", nil, &det); c != 200 || len(det.Rows) != file.Rows {
		t.Fatalf("detail %d %d rows", c, len(det.Rows))
	}
	up := ops.RASUCCFileReq{Ref: "ACKMADEUP1", On: day, File: file.Name}
	if c := ipoCall(t, srv, "POST", "/v1/ras/ucc-reports/"+day+"/filed", "priya.nair", up, nil); c != 403 {
		t.Fatalf("a maker recorded the upload: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/ras/ucc-reports/"+day+"/filed", "farhan.khan", up, nil); c != 200 {
		t.Fatalf("upload %d", c)
	}
	ipoCall(t, srv, "GET", "/v1/ras/ucc-reports?date="+day, "priya.nair", nil, &rv)
	for _, st := range rv.Settlements {
		if st.Date == day && st.Status != "Uploaded" {
			t.Fatalf("after the upload %+v", st)
		}
	}

	// A transfer between the firm's accounts: a maker records it, a checker
	// approves it.
	var xf struct {
		Transfers []ops.FirmTransfer `json:"transfers"`
		Purposes  []ops.MoneyPurpose `json:"purposes"`
	}
	if c := ipoCall(t, srv, "GET", "/v1/money-transfers?from=2026-10-01&to="+day, "priya.nair", nil, &xf); c != 200 || len(xf.Transfers) != 0 || len(xf.Purposes) < 5 {
		t.Fatalf("transfers %d %+v", c, xf)
	}
	var x ops.FirmTransfer
	rel := map[string]any{"date": day, "purpose": "release", "from": "CC", "to": b.ID, "amount": "1000", "ref": "UTRRELMADEUP9", "withdraw": "withdrawal"}
	if c := ipoCall(t, srv, "POST", "/v1/money-transfers", "priya.nair", map[string]any{"date": day, "purpose": "release", "from": "CC", "to": b.ID, "amount": "1000", "ref": "UTRRELMADEUP7"}, nil); c != 422 {
		t.Fatalf("funds taken back with no reason: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/money-transfers", "priya.nair", rel, &x); c != 200 || x.Status != "Pending" {
		t.Fatalf("record %d %+v", c, x)
	}
	if c := ipoCall(t, srv, "POST", "/v1/money-transfers", "priya.nair", map[string]any{"date": day, "purpose": "release", "from": "CC", "to": b.ID, "amount": "0", "ref": "UTRRELMADEUP8", "withdraw": "withdrawal"}, nil); c != 422 {
		t.Fatalf("a nil amount: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/money-transfers/"+x.ID+"/decide", "priya.nair", map[string]any{"approve": true}, nil); c != 403 {
		t.Fatalf("the maker approved: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/money-transfers/"+x.ID+"/decide", "farhan.khan", map[string]any{"approve": true}, &x); c != 200 || x.Status != "Posted" || x.Entry == "" {
		t.Fatalf("approve %d %+v", c, x)
	}

	// Money kept back, by reason: the cut-off set by the compliance
	// officer, a reason recorded by a maker.
	if c := ipoCall(t, srv, "POST", "/v1/money-rules", "priya.nair", map[string]string{"funds_cutoff": "18:00"}, nil); c != 403 {
		t.Fatalf("a maker set the cut-off: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/money-rules", "vikram.shah", map[string]string{"funds_cutoff": "6pm"}, nil); c != 422 {
		t.Fatalf("a bad cut-off: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/money-rules", "vikram.shah", map[string]string{"funds_cutoff": "18:00"}, nil); c != 200 {
		t.Fatalf("cut-off %d", c)
	}
	var rt ops.RetentionEntry
	if c := ipoCall(t, srv, "POST", "/v1/money-retention", "priya.nair", map[string]any{"date": day, "code": "RC06", "amount": "250", "ref": "OFS-REFUND-1"}, &rt); c != 200 || rt.ID == "" {
		t.Fatalf("retention %d %+v", c, rt)
	}
	var kept struct {
		Report ops.RetentionReport `json:"report"`
		Rules  ops.MoneyRules      `json:"rules"`
	}
	if c := ipoCall(t, srv, "GET", "/v1/money-retention?date="+day, "anita.rao", nil, &kept); c != 200 || kept.Rules.FundsCutoff != "18:00" || len(kept.Report.Entries) != 1 {
		t.Fatalf("retained funds %d %+v", c, kept)
	}
	if c := ipoCall(t, srv, "POST", "/v1/money-retention/"+rt.ID+"/withdraw", "priya.nair", map[string]string{"reason": "entered twice"}, &rt); c != 200 || rt.Status != "Withdrawn" {
		t.Fatalf("withdraw %d %+v", c, rt)
	}
	var qs []ops.PayoutCheque
	if c := ipoCall(t, srv, "GET", "/v1/payout-cheques", "priya.nair", nil, &qs); c != 200 || len(qs) != 0 {
		t.Fatalf("cheques %d %+v", c, qs)
	}
	if c := ipoCall(t, srv, "POST", "/v1/payout-cheques", "farhan.khan", map[string]string{"request": "PO-999999", "cheque_no": "1", "date": day}, nil); c != 404 {
		t.Fatalf("a cheque for no request: %d", c)
	}

	// The day's statement: prepared by a maker, signed off by a checker.
	var r ops.MoneyRecon
	if c := ipoCall(t, srv, "GET", "/v1/money-recon?date="+day, "priya.nair", nil, &r); c != 200 || r.Status != "Not prepared" || !r.CanPrepare || r.Register != 3 || len(r.Moves) == 0 {
		t.Fatalf("statement %d %+v", c, r)
	}
	if c := ipoCall(t, srv, "GET", "/v1/money-recon?date=16-10-2026", "priya.nair", nil, nil); c != 400 {
		t.Fatalf("a bad date: %d", c)
	}
	if c := ipoCall(t, srv, "GET", "/v1/money-recon?date="+day, "meera.iyer", nil, nil); c != 403 {
		t.Fatalf("branch staff saw the statement: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/money-recon/"+day+"/prepare", "priya.nair", map[string]string{"note": "Checked against the bank statement"}, &r); c != 200 || r.Status != "Prepared" {
		t.Fatalf("prepare %d %+v", c, r)
	}
	if c := ipoCall(t, srv, "POST", "/v1/money-recon/"+day+"/sign", "priya.nair", nil, nil); c != 403 {
		t.Fatalf("a maker signed off: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/money-recon/"+day+"/return", "farhan.khan", map[string]string{}, nil); c != 422 {
		t.Fatalf("sent back with no reason: %d", c)
	}
	if c := ipoCall(t, srv, "POST", "/v1/money-recon/"+day+"/sign", "farhan.khan", map[string]string{"note": "Seen"}, &r); c != 200 || r.Status != "Signed off" || r.Signed == nil || r.Signed.By != "farhan.khan" {
		t.Fatalf("sign %d %+v", c, r)
	}
	var csvr []byte
	if c := ipoCall(t, srv, "GET", "/v1/money-recon/"+day+"/file", "anita.rao", nil, &csvr); c != 200 || !bytes.Contains(csvr, []byte("Signed off")) || !bytes.Contains(csvr, []byte("UTRRELMADEUP9")) {
		t.Fatalf("csv %d %.300s", c, csvr)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/v1/money-recon/"+day+"/document", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Actor", "vikram.shah")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Security-Policy"), "default-src 'none'") || !bytes.Contains(doc, []byte("farhan.khan")) {
		t.Fatalf("document %d %s %.200s", res.StatusCode, res.Header.Get("Content-Security-Policy"), doc)
	}
}
