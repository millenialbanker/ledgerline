package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// bsdaTestReview proposes a review with one BSDA above the limit (LIMIT),
// one BSDA whose holder has a demat account with another DP (MULTI) and
// the eligible accounts, at CDSL and NSDL, becoming BSDA (ELIG).
func bsdaTestReview(t *testing.T, o *Ops, ctx context.Context) BSDAReview {
	t.Helper()
	over := o.bo(ctx, T, mustClient(t, o, "BH1002")) // CDSL, above the limit
	over.BSDA = true
	other := o.bo(ctx, T, mustClient(t, o, "BH1001")) // CDSL, within the limit
	other.BSDA, other.OtherDemat = true, true
	for _, b := range []BOAccount{over, other} {
		if err := upsertBO(o, ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	r, err := o.ReviewBSDA(ctx, T, "priya.nair", D)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]BSDALine{}
	for _, l := range r.Lines {
		by[l.UCC] = l
	}
	if l := by["BH1002"]; l.Code != "LIMIT" || l.To != "Regular" {
		t.Fatalf("above the limit: %+v", l)
	}
	if l := by["BH1001"]; l.Code != "MULTI" || l.To != "Regular" || !strings.Contains(l.Reason, "another DP") {
		t.Fatalf("another demat account: %+v", l)
	}
	if l := by["BH1003"]; l.Code != "ELIG" || l.To != "BSDA" {
		t.Fatalf("eligible: %+v", l)
	}
	if l := by["BH1024"]; l.Code != "ELIG" || !strings.HasPrefix(l.BOID, "IN") {
		t.Fatalf("eligible NSDL account: %+v", l)
	}
	return r
}

func bsdaDefaults(t *testing.T, o *Ops, ctx context.Context, extra map[string]string) {
	t.Helper()
	v := map[string]string{"product_number": "0001", "bo_category": "01", "bo_sub_status": "0101", "mode_of_operation": "1", "communication_preference": "1",
		"bo_settlement_planning_flag": "N", "bo_statement_cycle_code": "MO", "divnd_bank_acct_type": "10", "divnd_bank_ccy": "INR", "cust_addr_cntry_code": "IN", "primary_mobil_isd_code": "91"}
	for k, x := range extra {
		v[k] = x
	}
	if _, err := o.SaveCDSLBODefaults(ctx, T, "farhan.khan", v); err != nil {
		t.Fatal(err)
	}
}

// TestBSDAReviewToCDSL: an applied BSDA review goes to CDSL as BO
// modifications: by the BO modify API, one request per account approved
// by the review's checker, or with the API off as one BO modify file in
// CDSL's layout for upload in CDAS. NSDL accounts are left to NSDL.
func TestBSDAReviewToCDSL(t *testing.T) {
	t.Run("file for CDAS", func(t *testing.T) {
		o, ctx := setup(t)
		bsdaDefaults(t, o, ctx, map[string]string{"operator_id": "OPR001"})
		r := bsdaTestReview(t, o, ctx)
		a, err := o.ApplyBSDAReview(ctx, T, "farhan.khan", r.ID)
		if err != nil || a.Status != "Applied" || !a.FileCDSL || !reBOFileName.MatchString(a.FileName) || !strings.HasPrefix(a.FileName, "11081600.") {
			t.Fatalf("applied %+v %v", a, err)
		}
		if !strings.Contains(a.CDSLNote, "Upload "+a.FileName+" in CDAS") || !hasOpenWork(t, o, "periodic", "bsdacdsl:"+a.ID) {
			t.Fatalf("no CDAS upload asked for: %q", a.CDSLNote)
		}
		f, err := o.PeriodicFileData(ctx, T, "priya.nair", a.FileID)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(string(f.Content), "\r\n"), "\r\n")
		var want map[string]string // BO ID → flag
		want = map[string]string{}
		for _, l := range a.Lines {
			switch {
			case strings.HasPrefix(l.BOID, "IN"):
				if !strings.HasPrefix(l.CDSLStatus, "Not a CDSL account") {
					t.Fatalf("NSDL line %+v", l)
				}
			default:
				want[l.BOID] = map[bool]string{true: "Y", false: "N"}[l.To == "BSDA"]
				if l.CDSLStatus != "In the file for CDAS" {
					t.Fatalf("CDSL line %+v", l)
				}
			}
		}
		if len(lines) != 1+2*len(want) || lines[0] != fmt.Sprintf("081600OPR001%05d", len(want)) {
			t.Fatalf("%d lines for %d accounts; header %q", len(lines), len(want), lines[0])
		}
		for i := 1; i < len(lines); i += 2 {
			bo := boFieldOf(t, lines[i], "m01", "bo_id")
			if flag := boFieldOf(t, lines[i+1], "m05", "bsda"); flag != want[bo] {
				t.Fatalf("%s: flag %q, want %q", bo, flag, want[bo])
			}
			// Only the flag changes: the rest of line 05 is blank.
			if strings.Count(lines[i+1], " ") != len(lines[i+1])-3 {
				t.Fatalf("line 05 changes more than the flag: %q", lines[i+1])
			}
		}
		// Ledgerline's records changed with it.
		if !o.bo(ctx, T, mustClient(t, o, "BH1003")).BSDA || o.bo(ctx, T, mustClient(t, o, "BH1001")).BSDA {
			t.Fatal("account types not changed")
		}
	})

	t.Run("API, harmonised upload", func(t *testing.T) {
		o, ctx := setup(t)
		f := newFakeCDSL(t, o, cdslFamBO)
		bsdaDefaults(t, o, ctx, map[string]string{"bo_upload_format": cdslFmtCSV})
		r := bsdaTestReview(t, o, ctx)
		a, err := o.ApplyBSDAReview(ctx, T, "farhan.khan", r.ID)
		if err != nil || a.FileCDSL || !strings.HasPrefix(a.FileName, "BSDA_MOD_") {
			t.Fatalf("applied %+v %v", a, err)
		}
		cdsl := 0
		for _, l := range a.Lines {
			if strings.HasPrefix(l.BOID, "IN") {
				continue
			}
			cdsl++
			if l.CDSLReq == "" {
				t.Fatalf("no CDSL request for %+v", l)
			}
			req, err := o.cdslGet(ctx, T, l.CDSLReq)
			if err != nil || req.Kind != CDSLModify || req.Status != crSent || req.CDSLRef != "ACK77" || req.Maker != "priya.nair" || req.Checker != "farhan.khan" || req.BOID != l.BOID {
				t.Fatalf("request %+v %v", req, err)
			}
			name, data, err := o.CDSLRequestFile(ctx, T, "priya.nair", req.ID, "sent")
			if err != nil || !strings.HasPrefix(name, "BO_UPLD_") {
				t.Fatalf("file %s %v", name, err)
			}
			row := csvRow(t, data, "ClntId", l.BOID)
			flag, reason := map[string]string{"LIMIT": "NBSD", "MULTI": "NBSD", "ELIG": "BSDA"}[l.Code], map[string]string{"LIMIT": "VHCPL", "MULTI": "BOHMA", "ELIG": "DFT"}[l.Code]
			if row["BSDAFlg"] != flag || row["RsnBSDAMod"] != reason || row["BOTxnTyp"] != "BOMOD" {
				t.Fatalf("%s (%s): %v", l.UCC, l.Code, row)
			}
		}
		if len(f.uploads) != cdsl || !strings.Contains(a.CDSLNote, "Sent to CDSL by the BO modify API") || strings.Contains(a.CDSLNote, "not sent") {
			t.Fatalf("%d uploads for %d accounts: %q", len(f.uploads), cdsl, a.CDSLNote)
		}
		if hasOpenWork(t, o, "periodic", "bsdacdsl:"+a.ID) {
			t.Fatal("asked to upload a file the API sent")
		}
		// The review shows how each request stands.
		rs, _ := o.BSDAReviews(ctx, T)
		for _, l := range rs[0].Lines {
			if !strings.HasPrefix(l.BOID, "IN") && l.CDSLStatus != crSent {
				t.Fatalf("review line %+v", l)
			}
		}
		// A BSDA request's file is made again from its own details.
		req, _ := o.cdslGet(ctx, T, a.Lines[0].CDSLReq)
		req.Status = crFailed
		if err := o.cdslSave(ctx, T, "test", &req); err != nil {
			t.Fatal(err)
		}
		res, err := o.ActOnCDSLRequest(ctx, T, "priya.nair", req.ID, "regenerate", "", "")
		if err != nil || res.Request.Status != crChecker || res.Request.File == nil || res.Request.File.Name == req.File.Name {
			t.Fatalf("regenerate %+v %v", res.Request, err)
		}
	})

	t.Run("API refuses the upload", func(t *testing.T) {
		o, ctx := setup(t)
		srv := useCDSLServer(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/" + cdslPathAuth:
				w.Write([]byte(`{"Body":{"success":true,"AuthToken":"TOK"}}`))
			default:
				w.Write([]byte(`{"Body":{"success":false,"UploadResponse":{"ErrorId":"ERR002","ErrorMessage":"Invalid Upload File Name"}}}`))
			}
		})
		fakeCDSLConn(t, o, srv.URL, cdslFamBO)
		bsdaDefaults(t, o, ctx, nil)
		r := bsdaTestReview(t, o, ctx)
		a, err := o.ApplyBSDAReview(ctx, T, "farhan.khan", r.ID)
		if err != nil || a.Status != "Applied" || !strings.Contains(a.CDSLNote, "Sent to CDSL by the BO modify API: 0 of ") || !strings.Contains(a.CDSLNote, "not sent") {
			t.Fatalf("applied %+v %v", a, err)
		}
		// Each refused request is on the work queue for staff to retry.
		req, _ := o.cdslGet(ctx, T, a.Lines[0].CDSLReq)
		if req.Status != crRejected || !strings.Contains(req.Error, "Invalid Upload File Name") || !hasOpenWork(t, o, "cdslbo", req.ID) {
			t.Fatalf("refused request %+v", req)
		}
	})
}

// csvRow is the row of a CSV file whose column has the value, by column.
func csvRow(t *testing.T, data []byte, col, val string) map[string]string {
	t.Helper()
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil || len(rows) < 2 {
		t.Fatalf("csv: %v (%d rows)", err, len(rows))
	}
	for _, r := range rows[1:] {
		m := map[string]string{}
		for i, h := range rows[0] {
			if i < len(r) {
				m[h] = r[i]
			}
		}
		if m[col] == val {
			return m
		}
	}
	t.Fatalf("no row with %s %s", col, val)
	return nil
}
