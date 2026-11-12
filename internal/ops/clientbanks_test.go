package ops

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// Synthetic clients only: made-up codes, accounts and IFSCs.

func cbClient(t *testing.T, ucc string) Client {
	t.Helper()
	c := bseClient(ucc, "ABCPK1234F", time.Now())
	c.Status, c.Tags = "Active", []string{}
	c.Bank = BankAccount{Bank: "HDFC Bank", IFSC: "HDFC0001234", Account: "50100012345678", Branch: "FC Road"}
	return c
}

func cbProof(t *testing.T, o *Ops, ucc, kind string) string {
	t.Helper()
	d, err := o.AddClientDoc(t.Context(), T, "priya.nair", ucc, kind, "proof.png", []byte("\x89PNG\r\n\x1a\n0000000000000000"))
	if err != nil {
		t.Fatal(err)
	}
	return d.ID
}

// cbChange raises a bank account change as a maker and has a checker
// approve it.
func cbChange(t *testing.T, o *Ops, ucc string, values map[string]string, doc string) ChangeRequest {
	t.Helper()
	ctx := t.Context()
	r, err := o.RequestChange(ctx, T, "priya.nair", ucc, "Bank account", values, doc)
	if err != nil {
		t.Fatalf("request %v: %v", values, err)
	}
	if _, err := o.DecideChange(ctx, T, "priya.nair", r.ID, true, ""); err == nil {
		t.Fatal("the maker approved the bank change")
	}
	r, err = o.DecideChange(ctx, T, "farhan.khan", r.ID, true, "")
	if err != nil || r.Status != "Approved" {
		t.Fatalf("approve %v: %+v %v", values, r, err)
	}
	return r
}

func TestClientBankAccounts(t *testing.T) {
	o, ctx := newOps(t)
	docs.Upsert(ctx, o.Docs, T, cAutoPol, "payouts", AutoPolicy{ID: "payouts", Mode: ModeOff}, "test")
	const d = "2026-10-16"
	c := cbClient(t, "CB001")
	if err := docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "test"); err != nil {
		t.Fatal(err)
	}

	// A client with one account is unchanged: no list stored, the account
	// is the primary, and the UCC records carry only bank 1.
	c, _ = o.Client(ctx, T, "CB001")
	n := c
	n.Normalise()
	if len(n.Banks) != 0 || len(registeredBanks(c)) != 1 || !primaryBank(c).Primary || primaryBank(c).Account != "50100012345678" {
		t.Fatalf("single bank: %+v %+v", n.Banks, registeredBanks(c))
	}
	if p := c.Public(); len(p.Banks) != 1 || p.Banks[0].Account != "XXXXXXXXXX5678" || !p.Banks[0].Primary {
		t.Fatalf("public: %+v", p.Banks)
	}
	src := bseUCCSources{apps: map[string]Application{}, mf: map[string]MFProfile{}, nri: map[string]NRIDetails{}}
	single := bseUCCMap(c, src, "Modify", nil, time.Now())
	if _, ok := single.Values["BANKNAME2"]; ok {
		t.Fatalf("single-bank client has a bank 2: %v", single.Values)
	}

	// The limit: the fewest UCC bank slots among the exchanges traded (MCX 3).
	if n, _ := o.MaxClientBanksNow(ctx, T); n != 3 {
		t.Fatalf("default limit %d", n)
	}
	set, _ := o.Settings(ctx, T)
	set.MaxClientBanks = 6
	if err := o.SaveSettings(ctx, T, "admin", set); err == nil {
		t.Fatal("6 accounts per client accepted")
	}
	set.MaxClientBanks, set.MemberCodes = 0, map[string]string{"NSE": "90311", "BSE": "6001"}
	if set.maxClientBanks() != 5 {
		t.Fatalf("NSE and BSE only: %d", set.maxClientBanks())
	}

	// Adding an account needs a bank proof; it is checked and approved by
	// someone else, then Active and flagged for the exchanges.
	if _, err := o.RequestChange(ctx, T, "priya.nair", "CB001", "Bank account", map[string]string{"action": "add", "bank": "ICICI Bank", "ifsc": "ICIC0000002", "account": "000201234567"}, ""); err == nil || !strings.Contains(err.Error(), "bank proof") {
		t.Fatalf("add without proof: %v", err)
	}
	proof := cbProof(t, o, "CB001", "Bank proof")
	if _, err := o.RequestChange(ctx, T, "priya.nair", "CB001", "Bank account", map[string]string{"action": "add", "bank": "HDFC Bank", "ifsc": "HDFC0001234", "account": "50100012345678"}, proof); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("adding the primary again: %v", err)
	}
	r := cbChange(t, o, "CB001", map[string]string{"action": "add", "bank": "ICICI Bank", "ifsc": "ICIC0000002", "account": "000201234567", "type": "sb", "micr": "411229002"}, proof)
	if len(r.UCCModify) != 3 || strings.Contains(r.New["account"], "01234567") {
		t.Fatalf("approved change: %+v", r)
	}
	c, _ = o.Client(ctx, T, "CB001")
	if len(c.Banks) != 2 || c.Bank.Account != "50100012345678" || !c.Banks[0].Primary || c.Banks[1].Primary {
		t.Fatalf("after add: %+v / %+v", c.Bank, c.Banks)
	}
	if b := c.Banks[1]; b.Status != BankActive || b.Type != "SB" || b.MICR != "411229002" || b.ProofDoc != proof || b.Change != r.ID || b.VerifiedOn == "" || b.VerifyMethod != "Proof checked by farhan.khan" || b.AddedOn == "" {
		t.Fatalf("new account: %+v", b)
	}
	if w, _, err := docs.Get[WorkItem](ctx, o.Docs, T, cWorkItem, "W-uccmod-"+r.ID); err != nil || w.UCC != "CB001" || !strings.Contains(w.Detail, "NSE, BSE, MCX") {
		t.Fatalf("UCC modification work: %+v %v", w, err)
	}

	// Receipts: from either registered account; another is third-party.
	rc, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: d, UCC: "CB001", Mode: "NEFT", Ref: "UTRCB1", FromAccount: "000201234567", Amount: 5000000})
	if err != nil {
		t.Fatalf("receipt from the second account: %v", err)
	}
	if _, err := o.DecideReceipt(ctx, T, "farhan.khan", rc.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: d, UCC: "CB001", Mode: "NEFT", Ref: "UTRCB2", FromAccount: "999988887777", Amount: 100000}); err == nil || !strings.Contains(err.Error(), "third-party") {
		t.Fatalf("unregistered account: %v", err)
	}
	clients, _ := o.Clients(ctx, T)
	m, err := o.matchCredit(ctx, T, "priya.nair", BankTxn{Date: d, Mode: "NEFT", Ref: "UTRCB3", Remitter: "ASHA KULKARNI", Account: "000201234567", Amount: 100000}, clients)
	if err != nil || m.Status != "Matched" || m.UCC != "CB001" {
		t.Fatalf("statement credit from the second account: %+v %v", m, err)
	}
	m, _ = o.matchCredit(ctx, T, "priya.nair", BankTxn{Date: d, Mode: "NEFT", Ref: "UTRCB4", Remitter: "SOMEONE ELSE", Account: "999988887777", Amount: 100000}, clients)
	if m.Status == "Matched" {
		t.Fatalf("unregistered credit matched: %+v", m)
	}

	// Payouts: the primary by default; another registered account when
	// chosen, with a reason; never an unregistered one.
	if _, err := o.RequestPayoutTo(ctx, T, "priya.nair", d, "CB001", 1000000, "Head office", "", "999988887777", "", "client asked"); err == nil || !strings.Contains(err.Error(), "not an active registered") {
		t.Fatalf("payout to an unregistered account: %v", err)
	}
	if _, err := o.RequestPayoutTo(ctx, T, "priya.nair", d, "CB001", 1000000, "Head office", "", "000201234567", "", ""); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("payout to the secondary without a reason: %v", err)
	}
	p1, err := o.RequestPayout(ctx, T, "priya.nair", d, "CB001", 1000000, "Head office")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := o.RequestPayoutTo(ctx, T, "priya.nair", d, "CB001", 1500000, "Head office", "", "XXXXXXXX4567", "ICIC0000002", "Client asked for the ICICI account")
	if err != nil || p2.ToAccount != "000201234567" || p2.ToIFSC != "ICIC0000002" {
		t.Fatalf("payout to the secondary (masked): %+v %v", p2, err)
	}
	for _, id := range []string{p1.ID, p2.ID} {
		if _, err := o.DecidePayout(ctx, T, "farhan.khan", d, id, true, ""); err != nil {
			t.Fatal(err)
		}
	}
	set, _ = o.Settings(ctx, T)
	set.HDFCClientID, set.HDFCInstrPrefix, set.HDFCRepeatClient = "TESTCORP01", "9", true
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	b, err := o.CreatePayoutBatch(ctx, T, "farhan.khan", d)
	if err != nil || len(b.Items) != 2 {
		t.Fatalf("batch: %+v %v", b, err)
	}
	to := map[string]string{}
	for _, it := range b.Items {
		to[it.Request] = it.Account + " " + it.IFSC
	}
	if to[p1.ID] != "50100012345678 HDFC0001234" || to[p2.ID] != "000201234567 ICIC0000002" {
		t.Fatalf("payout accounts: %v", to)
	}
	// The HDFC ENet file pays the chosen account and still refuses one not registered.
	if _, _, probs := o.hdfcRows(ctx, T, set, b, nil); len(probs) != 0 {
		t.Fatalf("HDFC problems: %v", probs)
	}
	bad := b
	bad.Items = append([]PayoutItem(nil), b.Items...)
	bad.Items[1].Account = "999988887777"
	if _, _, probs := o.hdfcRows(ctx, T, set, bad, nil); !strings.Contains(strings.Join(probs, ";"), "not the client's registered bank account") {
		t.Fatalf("HDFC with an unregistered account: %v", probs)
	}

	// The limit: a third account is allowed, a fourth is not.
	cbChange(t, o, "CB001", map[string]string{"action": "add", "bank": "Axis Bank", "ifsc": "UTIB0000003", "account": "911010000000003", "type": "CA"}, cbProof(t, o, "CB001", "Bank proof"))
	if _, err := o.RequestChange(ctx, T, "priya.nair", "CB001", "Bank account", map[string]string{"action": "add", "bank": "SBI", "ifsc": "SBIN0000004", "account": "300000000004"}, cbProof(t, o, "CB001", "Bank proof")); err == nil || !strings.Contains(err.Error(), "at most 3") {
		t.Fatalf("fourth account: %v", err)
	}

	// The exchanges' UCC records carry the accounts, primary first.
	c, _ = o.Client(ctx, T, "CB001")
	bse := bseUCCMap(c, src, "Modify", nil, time.Now())
	if bse.Values["ACCOUNTNO1"] != "50100012345678" || bse.Values["PRIMARYORSECONDARYBANK1"] != "P" || bse.Values["ACCOUNTNO2"] != "000201234567" || bse.Values["PRIMARYORSECONDARYBANK2"] != "S" ||
		bse.Values["BANKBRANCHIFSCCODE3"] != "UTIB0000003" || bse.Values["ACCOUNTNO4"] != "" {
		t.Fatalf("BSE banks: %v", bse.Values)
	}
	if bse.Hash() == single.Hash() {
		t.Fatal("the BSE record did not change: no UCC modification would be sent")
	}
	nse := nseUCIMap(c, nseUCITestDetails, nil, Settings{}, "90311", time.Now(), NSEUCIReg{})
	if len(nse.Records) == 0 {
		t.Fatalf("NSE: %v", nse.Problems)
	}
	if nb := nse.Records[0].Body; nb["ccdBankAcctNo"] != "50100012345678" || nb["ccdPriSecBnk"] != "P" || nb["ccdBankAcctNo2"] != "000201234567" || nb["ccdPriSecBnk2"] != "S" || nb["ccdBankIfsc3"] != "UTIB0000003" {
		t.Fatalf("NSE banks: %v", nb)
	}
	mc := mcxUCCMap(c, mcxTestSources(map[string]UCCDetails{c.UCC: mcxDetails(c.UCC)}, true), nil, time.Now().Format("2006-01-02"))
	if mc.Values[mxAcct1] != "50100012345678" || mc.Values["BankAccountNumber2"] != "000201234567" || mc.Values["BankAccountType2"] != "10" || mc.Values["BankAccountType3"] != "11" ||
		mc.Values["BankBranch3"] != "IFSC UTIB0000003" || mc.Masked()["BankAccountNumber2"] != "XXXXXXXX4567" {
		t.Fatalf("MCX banks: %v", mc.Values)
	}
	for _, p := range mc.Problems {
		if strings.Contains(p, "Bank account") {
			t.Fatalf("MCX problem: %v", mc.Problems)
		}
	}
	api := mcxUCCAPIRecord(mc)
	if api["bank_account_number_2"] != "000201234567" || api["bank_name_3"] != "Axis Bank" {
		t.Fatalf("MCX API banks: %v", api)
	}
	// More active accounts than a record holds is reported.
	four := c
	four.Banks = append(append([]BankAccount(nil), c.Banks...), BankAccount{Bank: "SBI", IFSC: "SBIN0000004", Account: "300000000004", Type: "SB", Status: BankActive})
	if mc4 := mcxUCCMap(four, mcxTestSources(map[string]UCCDetails{c.UCC: mcxDetails(c.UCC)}, true), nil, time.Now().Format("2006-01-02")); !strings.Contains(strings.Join(mc4.Problems, ";"), "the MCX UCC record takes 3") {
		t.Fatalf("4 accounts at MCX: %v", mc4.Problems)
	}
	if b4 := bseUCCMap(four, src, "Modify", nil, time.Now()); b4.Values["ACCOUNTNO4"] != "300000000004" {
		t.Fatalf("4 accounts at BSE: %v", b4.Values)
	}

	// Deactivating: the client's signed request (from staff); not the
	// primary; then it takes no money and gets no payouts.
	if _, err := o.RequestChange(ctx, T, "priya.nair", "CB001", "Bank account", map[string]string{"action": "deactivate", "target": "XXXXXXXX4567", "ifsc": "ICIC0000002"}, ""); err == nil || !strings.Contains(err.Error(), "signed request") {
		t.Fatalf("deactivate without the client's request: %v", err)
	}
	letter := cbProof(t, o, "CB001", "Other")
	if _, err := o.RequestChange(ctx, T, "priya.nair", "CB001", "Bank account", map[string]string{"action": "deactivate", "account": "50100012345678"}, letter); err == nil || !strings.Contains(err.Error(), "primary") {
		t.Fatalf("deactivating the primary: %v", err)
	}
	// A payout chosen to the account before it was removed is held back.
	p3, err := o.RequestPayoutTo(ctx, T, "priya.nair", d, "CB001", 500000, "Head office", "", "000201234567", "ICIC0000002", "Client asked")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.DecidePayout(ctx, T, "farhan.khan", d, p3.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	dr := cbChange(t, o, "CB001", map[string]string{"action": "deactivate", "target": "XXXXXXXX4567", "ifsc": "ICIC0000002", "reason": "Account closed"}, letter)
	if dr.New["account"] == "000201234567" {
		t.Fatal("full account number shown")
	}
	c, _ = o.Client(ctx, T, "CB001")
	if x, ok := registeredBank(c, "000201234567", ""); ok || len(activeBanks(c)) != 2 {
		t.Fatalf("after deactivate: %+v %+v", x, c.Banks)
	}
	if _, err := o.RecordReceipt(ctx, T, "priya.nair", Receipt{Date: d, UCC: "CB001", Mode: "NEFT", Ref: "UTRCB5", FromAccount: "000201234567", Amount: 100000}); err == nil {
		t.Fatal("receipt from an inactive account accepted")
	}
	if _, err := o.RequestPayoutTo(ctx, T, "priya.nair", d, "CB001", 100000, "Head office", "", "000201234567", "ICIC0000002", "Client asked"); err == nil {
		t.Fatal("payout to an inactive account accepted")
	}
	if b, err := o.CreatePayoutBatch(ctx, T, "farhan.khan", d); err == nil || len(b.Held) != 1 || !strings.Contains(b.Held[0], p3.ID) {
		t.Fatalf("payout to a removed account: %+v %v", b, err)
	}
	if mcx := mcxUCCMap(c, mcxTestSources(map[string]UCCDetails{c.UCC: mcxDetails(c.UCC)}, true), nil, time.Now().Format("2006-01-02")); mcx.Values["BankAccountNumber2"] != "911010000000003" || mcx.Values["BankAccountNumber3"] != "" {
		t.Fatalf("MCX after deactivate: %v", mcx.Values)
	}

	// Making another account the primary: payouts and Bank follow it.
	cbChange(t, o, "CB001", map[string]string{"action": "primary", "account": "911010000000003"}, letter)
	c, _ = o.Client(ctx, T, "CB001")
	if c.Bank.Account != "911010000000003" || primaryBank(c).Account != "911010000000003" || !c.Banks[0].Primary || len(activeBanks(c)) != 2 {
		t.Fatalf("after make primary: %+v %+v", c.Bank, c.Banks)
	}
	if bse := bseUCCMap(c, src, "Modify", nil, time.Now()); bse.Values["ACCOUNTNO1"] != "911010000000003" || bse.Values["ACCOUNTNO2"] != "50100012345678" {
		t.Fatalf("BSE after make primary: %v", bse.Values)
	}
	// The portal shows every account, masked.
	if pa := portalAccount(c); len(pa.Banks) != 3 || pa.Banks[0].Account != "XXXXXXXXXXX0003" || pa.Banks[0].ProofDoc != "" {
		t.Fatalf("portal accounts: %+v", pa.Banks)
	}
	// Only a primary change goes to the depository.
	chs, _ := o.ChangeRequests(ctx, T)
	prim, add := 0, 0
	for _, ch := range chs {
		if ch.UCC == "CB001" && bankChangeTouchesPrimary(ch) {
			prim++
		} else if ch.UCC == "CB001" {
			add++
		}
	}
	if prim != 1 || add != 3 {
		t.Fatalf("primary changes %d, others %d", prim, add)
	}
}

// TestClientBanksLegacyReplace: a bank account change of the old kind
// replaces the primary; the old account is kept, inactive.
func TestClientBanksLegacyReplace(t *testing.T) {
	o, ctx := newOps(t)
	c := cbClient(t, "CB002")
	docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "test")
	cbChange(t, o, "CB002", map[string]string{"bank": "ICICI Bank", "ifsc": "ICIC0001234", "account": "123412341234"}, cbProof(t, o, "CB002", "Bank proof"))
	c, _ = o.Client(ctx, T, "CB002")
	if c.Bank.Account != "123412341234" || len(activeBanks(c)) != 1 || len(c.Banks) != 2 || c.Banks[1].Status != BankInactive || c.Banks[1].Account != "50100012345678" {
		t.Fatalf("replace: %+v %+v", c.Bank, c.Banks)
	}
	// A client master re-import keeps the list; its account is the primary
	// (active again), and the approved account stays registered.
	c.Bank = BankAccount{Bank: "HDFC Bank", IFSC: "HDFC0001234", Account: "50100012345678"}
	c.Normalise()
	if c.Bank.Account != "50100012345678" || len(activeBanks(c)) != 2 || c.Banks[0].Account != "50100012345678" || !c.Banks[0].Primary || c.Banks[1].Primary {
		t.Fatalf("re-import: %+v", c.Banks)
	}
}

// TestClientBanksImport: the CLIENT-BANKS migration file loads several
// accounts per client.
func TestClientBanksImport(t *testing.T) {
	o, ctx := newOps(t)
	for _, u := range []string{"CB003", "CB004"} {
		c := cbClient(t, u)
		docs.Upsert(ctx, o.Docs, T, cClient, c.UCC, c, "test")
	}
	head := "UCC,Bank,IFSC,Account,Primary,Type,Status,Branch\n"
	f := head + "CB003,HDFC Bank,HDFC0001234,50100012345678,N,SB,Active,FC Road\n" +
		"CB003,ICICI Bank,ICIC0000002,000201234567,Y,SB,Active,\n" +
		"CB003,Axis Bank,UTIB0000003,911010000000003,,CA,Inactive,\n" +
		"CB004,SBI,SBIN0000004,300000000004,,SB,,\n"
	if src := o.DetectSource("client_banks.csv", []byte(f)); src != "CLIENT-BANKS" {
		t.Fatalf("detected %q", src)
	}
	if _, err := o.LoadFile(ctx, T, "priya.nair", "CLIENT-BANKS", "", "client_banks.csv", []byte(f)); err != nil {
		t.Fatal(err)
	}
	c, _ := o.Client(ctx, T, "CB003")
	if c.Bank.Account != "000201234567" || len(c.Banks) != 3 || len(activeBanks(c)) != 2 || !c.Banks[0].Primary || c.Banks[0].Account != "000201234567" {
		t.Fatalf("CB003: %+v %+v", c.Bank, c.Banks)
	}
	if c4, _ := o.Client(ctx, T, "CB004"); c4.Bank.Account != "300000000004" || primaryBank(c4).Account != "300000000004" {
		t.Fatalf("CB004: %+v", c4.Bank)
	}
	// Over the limit: the accounts beyond it load as inactive accounts
	// (a migration must not stop on them; receipts from them are flagged).
	set, _ := o.Settings(ctx, T)
	lim := set.maxClientBanks()
	var rows strings.Builder
	rows.WriteString(head)
	for i := 0; i < lim+1; i++ {
		fmt.Fprintf(&rows, "CB004,SBI,SBIN000000%d,30000000000%d,,SB,,\n", i, i)
	}
	if _, err := o.LoadFile(ctx, T, "priya.nair", "CLIENT-BANKS", "", "too_many.csv", []byte(rows.String())); err != nil {
		t.Fatal(err)
	}
	if c4, _ := o.Client(ctx, T, "CB004"); len(c4.Banks) != lim+1 || len(activeBanks(c4)) != lim {
		t.Fatalf("over the limit: %+v", c4.Banks)
	}
	// Two primaries: refused.
	two := head + "CB004,SBI,SBIN0000004,300000000004,Y,SB,,\nCB004,SBI,SBIN0000004,300000000004,Y,SB,,\n"
	if _, err := o.LoadFile(ctx, T, "priya.nair", "CLIENT-BANKS", "", "dup.csv", []byte(two)); err == nil {
		t.Fatal("duplicate account loaded")
	}
	_ = money.Amount(0)
}
