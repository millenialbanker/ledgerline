package ops

import (
	"strings"
	"testing"
)

// A demat account two clients name (a joint or family account, or the same
// person's two codes) loads with a warning, as the previous back office had
// it; a BO account of the DP keeps one owner, and staff add another
// client's account only to a code with the same PAN.
func TestSharedDematAccounts(t *testing.T) {
	o, ctx := migSetup(t)
	load := func(src string, data []byte) []string {
		t.Helper()
		r, err := o.LoadFile(ctx, MT, "chk.one", src, "", strings.ToLower(src)+".csv", data)
		if err != nil {
			t.Fatalf("%s: %v %+v", src, err, r.Errors)
		}
		var w []string
		for _, e := range r.Warnings {
			w = append(w, e.Msg)
		}
		return w
	}
	const shared, other = "IN30000011112222", "IN30000011113333"
	a := cmRow("TA001", "Test Client A", "ABCPA1234A", "9876500001")
	b := cmRow("TB002", "Test Client B", "ABCPB1234B", "9876500002")
	c := cmRow("TC003", "Test Client A Again", "ABCPA1234A", "9876500003")
	for _, r := range [][]string{a, b, c} {
		r[15], r[16] = "NSDL", shared
	}
	w := strings.Join(load("CLIENT-MASTER", csvOf(cmCols, a, b, c)), "\n")
	if strings.Count(w, shared+" is also the demat account of TA001 in this file") != 2 || !strings.Contains(w, "whose PAN is different") || !strings.Contains(w, "the same PAN") {
		t.Fatalf("client master warnings:\n%s", w)
	}
	for _, u := range []string{"TA001", "TB002", "TC003"} {
		if x, _ := o.Client(ctx, MT, u); x.Demat.ID != shared {
			t.Fatalf("%s demat %q", u, x.Demat.ID)
		}
	}
	// The client demat master: the shared account on two clients, and an
	// account of the DP that another code of the same person also names.
	cols := []string{"UCC", "Depository", "DematID", "Primary", "Status"}
	w = strings.Join(load("CLIENT-DEMATS", csvOf(cols, []string{"TA001", "NSDL", shared, "Y", "Active"}, []string{"TA001", "CDSL", "1234567800000001", "", "Active"},
		[]string{"TB002", "NSDL", shared, "Y", "Active"}, []string{"TB002", "NSDL", other, "", "Active"}, []string{"TC003", "CDSL", "1234567800000001", "", "Active"})), "\n")
	if !strings.Contains(w, shared+" is also the demat account of TA001 in this file, whose PAN is different") || !strings.Contains(w, "1234567800000001 is also the demat account of TA001 in this file, the same PAN") {
		t.Fatalf("client demats warnings:\n%s", w)
	}
	// The DP's record gives the account to TA001: TC003 still names it.
	dpCols := []string{"UCC", "BOID", "Depository", "Status"}
	load("DP-ACCOUNTS", csvOf(dpCols, []string{"TA001", "1234567800000001", "CDSL", "Active"}))
	if r, err := o.LoadFile(ctx, MT, "chk.one", "DP-ACCOUNTS", "", "dp2.csv", csvOf(dpCols, []string{"TB002", "1234567800000001", "CDSL", "Active"})); err == nil || len(r.Errors) == 0 || !strings.Contains(r.Errors[0].Msg, "the DP's account of TA001") {
		t.Fatalf("a BO account of the DP given to a second client: %v %+v", err, r.Errors)
	}
	// TA001's primary is now its account with the DP, so the shared one is
	// TB002's (whose primary it is)
	if by := o.boClients(ctx, MT); by["1234567800000001"] != "TA001" || by[shared] != "TB002" || by[other] != "TB002" {
		t.Fatalf("accounts' clients %v", by)
	}
	x, _ := o.Client(ctx, MT, "TC003")
	if d, ok := dematOf(o.withDemats(ctx, MT, x), "1234567800000001"); !ok || d.OwnDP {
		t.Fatalf("TC003's list %+v", o.withDemats(ctx, MT, x).Demats)
	}
	// Staff add another client's account to a code of the same PAN only.
	if _, err := o.RequestChange(ctx, MT, "mkr.one", "TB002", "Demat account", map[string]string{"action": "add", "id": "1234567800000001"}, ""); err == nil || !strings.Contains(err.Error(), "whose PAN is different") {
		t.Fatalf("another PAN's account added: %v", err)
	}
	if _, err := o.RequestChange(ctx, MT, "mkr.one", "TC003", "Demat account", map[string]string{"action": "add", "id": "1234567800000001"}, ""); err == nil || strings.Contains(err.Error(), "PAN") {
		// TC003 already names it: refused as already its own, not for the PAN
		t.Fatalf("the same PAN's account: %v", err)
	}
}

// An inactive client's closed account marked primary (a closed DP-only
// account) loads with a warning, not as the primary; an active client's is
// refused.
func TestClosedPrimaryOfInactiveClient(t *testing.T) {
	o, ctx := migSetup(t)
	cols := append(append([]string{}, cmCols...), "Inactive")
	if _, err := o.LoadFile(ctx, MT, "chk.one", "CLIENT-MASTER", "", "c.csv", csvOf(cols, append(cmRow("TA001", "Test Client A", "ABCPA1234A", "9876500001"), "N"),
		append(cmRow("TB002", "Test Client B", "ABCPB1234B", "9876500002"), "Y"))); err != nil {
		t.Fatal(err)
	}
	dpCols := []string{"UCC", "BOID", "Depository", "Status", "ClosedOn", "Primary"}
	r, err := o.LoadFile(ctx, MT, "chk.one", "DP-ACCOUNTS", "", "dp.csv", csvOf(dpCols, []string{"TB002", "1234567800000002", "CDSL", "Closed", "2024-03-31", "Y"}))
	if err != nil || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0].Msg, "is not made the primary") {
		t.Fatalf("inactive client's closed primary: %v %+v %+v", err, r.Errors, r.Warnings)
	}
	if r, err := o.LoadFile(ctx, MT, "chk.one", "DP-ACCOUNTS", "", "dp2.csv", csvOf(dpCols, []string{"TA001", "1234567800000001", "CDSL", "Closed", "2024-03-31", "Y"})); err == nil || len(r.Errors) == 0 {
		t.Fatal("an active client's closed account loaded as the primary")
	}
}
