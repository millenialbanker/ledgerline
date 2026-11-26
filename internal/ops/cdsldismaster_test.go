package ops

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"ledgerline/internal/docs"
)

// TestCDSLDISMasterByAPI: the weekly DIS master goes to CDSL through the
// common upload API when it is on, a week is never sent twice, CDSL's
// response marks each book, and a file CDSL does not process is sent
// again with the next run.
func TestCDSLDISMasterByAPI(t *testing.T) {
	o, ctx := setup(t)
	c, _ := cdxTestClient(t, o, ctx)
	key, _ := cdslAESKey(testCDSLKey, "k")
	iv, _ := cdslIV("")
	var mu sync.Mutex
	var uploaded []byte
	var upName, status string
	accept, reject := "", ""
	status = "Completed"
	srv := useCDSLServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		signed := func(b []byte) bool {
			s, err := cdslDecrypt(key, iv, r.Header.Get("AuthSign"))
			return err == nil && string(s) == string(cdslHash(b, cdslHashHexLower))
		}
		switch r.URL.Path {
		case "/" + cdslPathAuth:
			b, _ := io.ReadAll(r.Body)
			if !signed(b) {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"Body":{"success":true,"AuthToken":"TOK1"}}`))
		case "/" + cdxPathUpload:
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("upload is not multipart: %v", err)
				return
			}
			f, fh, err := r.FormFile("file")
			if err != nil {
				t.Errorf("no file: %v", err)
				return
			}
			uploaded, _ = io.ReadAll(f)
			upName = fh.Filename
			if r.Header.Get("AuthToken") != "TOK1" || r.Header.Get("FileComputeHash") != string(cdslHash(uploaded, cdslHashHexLower)) {
				w.Write([]byte(`{"Body":{"success":false,"UploadResponse":{"ErrorId":"ERR003","ErrorMessage":"Invalid Compute Hash"}}}`))
				return
			}
			w.Write([]byte(`{"Body":{"success":true,"AcknowledgmentID":"ACK91"}}`))
		case "/" + cdslPathStatus:
			w.Write([]byte(`{"Body":{"success":true,"Status":"` + status + `"}}`))
		case "/" + cdslPathDownload:
			b, _ := io.ReadAll(r.Body)
			var q map[string]string
			json.Unmarshal(b, &q)
			if q["UploadFileName"]+".zip" != upName {
				w.Write([]byte(`{"Body":{"success":false,"DownloadResponseFile":{"ErrorId":"ERR002","ErrorMessage":"Invalid Upload File Name"}}}`))
				return
			}
			resp := "IntlRefNb,TxUnqId,RjctnRsnCd1\n" + accept + ",7001,\n" + reject + ",,D07\n"
			var zb bytes.Buffer
			zw := zip.NewWriter(&zb)
			fw, _ := zw.Create("TXN_RESP.csv")
			fw.Write([]byte(resp))
			zw.Close()
			out, _ := json.Marshal(map[string]any{"Body": map[string]any{"success": true, "ResponseFileName": "TXN_RESP.zip", "ResponseFilebuffer": base64.StdEncoding.EncodeToString(zb.Bytes())}})
			w.Write(out)
		default:
			w.WriteHeader(404)
		}
	})
	set, _ := o.Settings(ctx, T)
	set.MemberCodes["CDSL-SENDER"] = "SNDR01"
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	b1, err := o.IssueDISBook(ctx, T, "priya.nair", c.UCC, 10)
	if err != nil {
		t.Fatal(err)
	}
	b2 := nextTestBook(t, o, ctx, c.UCC, 5)
	accept, reject = b1.ID, b2.ID
	today := o.now().Format("2006-01-02")

	// The API off: refused, and the weekly task makes the file instead.
	cdxTestConn(t, o, srv.URL)
	if _, err := o.SendDISMasterByAPI(ctx, T, "priya.nair", today); !errors.Is(err, ErrState) {
		t.Fatalf("API off: %v", err)
	}
	cdxTestConn(t, o, srv.URL, cdslFamUpload)
	if _, err := o.SendDISMasterByAPI(ctx, T, "vikram.shah", today); !errors.Is(err, ErrForbidden) {
		t.Fatalf("compliance sent the DIS master: %v", err)
	}

	// The weekly task sends it by API: no "upload this file" work item.
	run, err := o.RunPeriodic(ctx, T, "priya.nair", "dis_master", today, today)
	if err != nil || run.Status != "Done" || !strings.Contains(run.Summary, "Sent to CDSL by API: 2 DIS books") || !strings.Contains(run.Summary, "ACK91") {
		t.Fatalf("periodic run: %+v %v", run, err)
	}
	if hasOpenWork(t, o, "periodic", "dismaster:"+today) {
		t.Fatal("an upload-by-hand work item was raised although the API sent the file")
	}
	zr, err := zip.NewReader(bytes.NewReader(uploaded), int64(len(uploaded)))
	if err != nil || len(zr.File) != 1 || !strings.HasSuffix(upName, ".zip") {
		t.Fatalf("zip %s: %v", upName, err)
	}
	rc, _ := zr.File[0].Open()
	csvb, _ := io.ReadAll(rc)
	for _, want := range []string{"DMU", b1.ID, b2.ID, "SNDR01", "DISI"} {
		if !strings.Contains(string(csvb), want) {
			t.Fatalf("file lacks %q: %s", want, csvb)
		}
	}
	for _, id := range []string{b1.ID, b2.ID} {
		if b, _, _ := docs.Get[DISBook](ctx, o.Docs, T, cDISBook, id); b.CDSL != "Sent" || b.CDSLUpload == "" {
			t.Fatalf("book %s after sending: %+v", id, b)
		}
	}
	// Never sent twice.
	if _, err := o.SendDISMasterByAPI(ctx, T, "priya.nair", today); !errors.Is(err, ErrState) {
		t.Fatalf("sent the same books again: %v", err)
	}

	// CDSL's response: one accepted, one rejected (to the work queue).
	ups, _ := docs.List[CDSLUpload](ctx, o.Docs, T, cCDSLUpload, "")
	if len(ups) != 1 || ups[0].Kind != UploadDISMaster || len(ups[0].Books) != 2 || ups[0].Ack != "ACK91" {
		t.Fatalf("upload record: %+v", ups)
	}
	u, err := o.CheckCDSLUpload(ctx, T, "priya.nair", ups[0].ID)
	if err != nil || u.Status != "Done" || u.Results[b1.ID] != "Accepted" || !strings.Contains(u.Results[b2.ID], "D07") || u.UnreadResult {
		t.Fatalf("response: %+v %v", u, err)
	}
	if b, _, _ := docs.Get[DISBook](ctx, o.Docs, T, cDISBook, b1.ID); b.CDSL != "Accepted" {
		t.Fatalf("accepted book: %+v", b)
	}
	if b, _, _ := docs.Get[DISBook](ctx, o.Docs, T, cDISBook, b2.ID); !strings.HasPrefix(b.CDSL, "Rejected") {
		t.Fatalf("rejected book: %+v", b)
	}
	if !hasOpenWork(t, o, "cdsl", "dismaster:"+b2.ID) {
		t.Fatal("no work item for the rejected book")
	}

	// A file CDSL does not process: the books go again with the next run.
	b3 := nextTestBook(t, o, ctx, c.UCC, 3)
	status = "Failed"
	u3, err := o.SendDISMasterByAPI(ctx, T, "priya.nair", today)
	if err != nil || len(u3.Books) != 1 || u3.Books[0] != b3.ID {
		t.Fatalf("third book: %+v %v", u3, err)
	}
	if u3, err = o.CheckCDSLUpload(ctx, T, "priya.nair", u3.ID); err != nil || u3.Status != "Failed" {
		t.Fatalf("failed upload: %+v %v", u3, err)
	}
	if b, _, _ := docs.Get[DISBook](ctx, o.Docs, T, cDISBook, b3.ID); b.CDSL != "" {
		t.Fatalf("book of a failed upload not cleared for resending: %+v", b)
	}
	status = "Completed"
	if u4, err := o.SendDISMasterByAPI(ctx, T, "priya.nair", today); err != nil || len(u4.Books) != 1 || u4.Books[0] != b3.ID {
		t.Fatalf("resend: %+v %v", u4, err)
	}
}

// hasOpenWork says whether an open work item of the process has the key.
func hasOpenWork(t *testing.T, o *Ops, process, key string) bool {
	t.Helper()
	ws, err := o.WorkItems(t.Context(), T, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.Status == "Open" && w.Process == process && (w.Key == key || w.Key == process+":"+key || strings.HasSuffix(w.Key, ":"+key) || strings.HasSuffix(w.Key, key)) {
			return true
		}
	}
	return false
}
