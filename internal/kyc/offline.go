package kyc

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"time"
)

// The investor's pages for choosing a route and uploading an Aadhaar
// offline e-KYC file. They are reached from the start link and carry its
// token.

const pageHead = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}}</title>
<style>:root{--bg:#F4F3EE;--card:#fff;--ink:#16201B;--dim:#5B6660;--line:#DCDDD6;--pri:#2747A8;--priInk:#fff;--warn:#FFF1C9;--warnInk:#6B4E00;--bad:#FCE4E1;--badInk:#8A1F12}
@media(prefers-color-scheme:dark){:root{--bg:#111613;--card:#1A211D;--ink:#E6EAE7;--dim:#9AA59F;--line:#2C352F;--pri:#9DB2F2;--priInk:#0E1530;--warn:#3A3112;--warnInk:#F2DA94;--bad:#3B1C18;--badInk:#F5B5AB}}
a{color:var(--pri)}*{box-sizing:border-box}body{font:15px/1.55 system-ui,-apple-system,sans-serif;margin:0;background:var(--bg);color:var(--ink)}main{max-width:520px;margin:6vh auto;padding:0 16px}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:22px;margin-bottom:14px}h1{font-size:20px;margin:0 0 6px}h2{font-size:16px;margin:0 0 4px}p{margin:6px 0}.dim{color:var(--dim)}.small{font-size:13px}
a.opt{display:block;text-decoration:none;color:inherit}a.opt:hover .card,a.opt:focus .card{border-color:var(--pri)}.tag{display:inline-block;border-radius:6px;padding:1px 8px;font-size:12px;background:var(--warn);color:var(--warnInk);margin-left:6px}
ol{padding-left:20px;margin:8px 0}li{margin:4px 0}label{display:block;font-weight:600;margin:14px 0 6px}input[type=file],input[type=text]{width:100%;font:inherit;padding:10px;border:1px solid var(--line);border-radius:8px;background:var(--bg);color:var(--ink)}
input.code{max-width:140px;letter-spacing:6px;font-size:20px;text-align:center}button{font:inherit;margin-top:16px;padding:11px 18px;border-radius:8px;border:0;background:var(--pri);color:var(--priInk);cursor:pointer;width:100%}
.err{background:var(--bad);color:var(--badInk);border-radius:8px;padding:10px 12px;margin:10px 0}.note{background:var(--warn);color:var(--warnInk);border-radius:8px;padding:10px 12px;margin:10px 0;font-size:13px}</style></head><body><main>`

var chooseTmpl = template.Must(template.New("c").Parse(pageHead + `<div class="card"><h1>Verify your Aadhaar</h1><p class="dim">Choose how to share your Aadhaar. Either way, no video call or office visit is needed.</p></div>
<a class="opt" href="{{.DL}}"><div class="card"><h2>DigiLocker <span class="tag">Quickest</span></h2><p class="dim">Sign in with your Aadhaar and the OTP sent to your Aadhaar-linked mobile, then allow access. Your PAN record comes too, so you need not upload your PAN card. No DigiLocker account? You can create one in the same step.</p></div></a>
<a class="opt" href="{{.Off}}"><div class="card"><h2>Aadhaar offline e-KYC file</h2><p class="dim">Download a signed file from UIDAI's myAadhaar site (OTP to your Aadhaar-linked mobile) and upload it here. You will upload a photo of your PAN card later.</p></div></a>
{{if .Back}}<p class="small"><a href="{{.Back}}">Back to the account opening page</a></p>{{end}}</main></body></html>`))

var offlineTmpl = template.Must(template.New("o").Parse(pageHead + `<div class="card"><h1>Upload your Aadhaar offline e-KYC file</h1>
<ol class="small"><li>Open <b>myaadhaar.uidai.gov.in</b> and choose <b>Offline e-KYC</b> (or use the mAadhaar app).</li><li>Enter your Aadhaar number and the OTP sent to your Aadhaar-linked mobile.</li><li>Choose a <b>4-digit share code</b> and download the ZIP file.</li><li>Upload the ZIP here, unopened, with the same share code. The file must be from the last 3 days.</li></ol>
{{if .Err}}<div class="err" role="alert">{{.Err}}</div>{{end}}
{{if .Practice}}<div class="note">Practice service: <a href="{{.Practice}}">download a practice file</a> made from your details; its share code is <b>1234</b>.</div>{{end}}
<form method="post" enctype="multipart/form-data" action="{{.Action}}"><label for="f">Offline e-KYC ZIP file</label><input id="f" type="file" name="file" accept=".zip,application/zip" required>
<label for="c">Share code</label><input id="c" class="code" type="text" name="code" inputmode="numeric" pattern="[0-9]{4}" maxlength="4" autocomplete="off" required>
<button>Verify my Aadhaar</button></form>
<p class="small dim">Your Aadhaar number is not in this file, only its last four digits. {{.Left}}</p></div>
{{if .Choose}}<p class="small"><a href="{{.Choose}}">Use DigiLocker instead</a></p>{{end}}{{if .Back}}<p class="small"><a href="{{.Back}}">Back to the account opening page</a></p>{{end}}</main></body></html>`))

func choosePage(w http.ResponseWriter, x Session, dl, off string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = chooseTmpl.Execute(w, map[string]any{"Title": "Verify your Aadhaar", "DL": template.URL(dl), "Off": template.URL(off), "Back": x.ReturnURL})
}

// linkSession checks a page request's customer, session and token.
func (s *Server) linkSession(w http.ResponseWriter, r *http.Request) (Customer, Session, bool) {
	c, ok := s.customer(r.PathValue("customer"))
	x, err := s.Store.Get(r.PathValue("customer"), r.PathValue("id"))
	t := r.URL.Query().Get("t")
	if t == "" {
		t = r.FormValue("t")
	}
	if !ok || err != nil || subtle.ConstantTimeCompare([]byte(sha(t)), []byte(x.TokenHash)) != 1 {
		page(w, 404, "This link is not valid", "Go back to the account opening page and try again.", "")
		return c, x, false
	}
	if x.Status == "Completed" || x.Status == "Failed" || s.expired(x) {
		page(w, 410, "This link has been used or has expired", "Go back to the account opening page and start again.", x.ReturnURL)
		return c, x, false
	}
	if x.Method == MethodDigiLocker || !contains(s.methods(c), MethodOffline) {
		page(w, 409, "Aadhaar offline e-KYC is not available here", "Go back to the account opening page.", x.ReturnURL)
		return c, x, false
	}
	return c, x, true
}

func (s *Server) renderOffline(w http.ResponseWriter, code int, c Customer, x Session, tok, errMsg string) {
	q := "?t=" + url.QueryEscape(tok)
	d := map[string]any{"Title": "Aadhaar offline e-KYC", "Err": errMsg, "Action": template.URL(s.pub("/offline/" + c.ID + "/" + x.ID + q)), "Back": x.ReturnURL}
	if x.Method == "" && contains(s.methods(c), MethodDigiLocker) {
		d["Choose"] = template.URL(s.pub("/start/" + c.ID + "/" + x.ID + q + "&via=" + MethodDigiLocker))
	}
	if s.practice != nil {
		d["Practice"] = template.URL(s.pub("/sim/okyc/" + c.ID + "/" + x.ID + q))
	}
	if left := MaxOfflineAttempts - x.Attempts; x.Attempts > 0 && left > 0 {
		d["Left"] = fmt.Sprintf("%d tries left.", left)
		if left == 1 {
			d["Left"] = "One try left."
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = offlineTmpl.Execute(w, d)
}

func (s *Server) offlinePage(w http.ResponseWriter, r *http.Request) {
	c, x, ok := s.linkSession(w, r)
	if !ok {
		return
	}
	if x.Status == "Created" {
		x.Status = "Started"
		_ = s.Store.Put(x)
	}
	s.renderOffline(w, 200, c, x, r.URL.Query().Get("t"), "")
}

func (s *Server) offlineUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxOfflineSize+64<<10)
	c, x, ok := s.linkSession(w, r)
	if !ok {
		return
	}
	tok := r.URL.Query().Get("t")
	f, _, err := r.FormFile("file")
	if err != nil {
		s.renderOffline(w, 400, c, x, tok, "Choose the ZIP file you downloaded from myAadhaar (up to 5 MB).")
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, maxOfflineSize+1))
	f.Close()
	if err != nil {
		s.renderOffline(w, 400, c, x, tok, "The file could not be read. Try again.")
		return
	}
	o, err := VerifyOffline(data, r.FormValue("code"), s.certs(), s.now(), x.Hint.Mobile, x.Hint.Email)
	if err != nil {
		x.Attempts++
		if x.Attempts >= MaxOfflineAttempts {
			x.Status, x.Error, x.Completed = "Failed", "too many failed Aadhaar offline e-KYC uploads", s.now().UTC().Format(time.RFC3339)
			_ = s.Store.Put(x)
			_ = s.Store.Count(c.ID, "failed", s.now())
			s.notify(c, x)
			http.Redirect(w, r, backURL(x), http.StatusFound)
			return
		}
		x.Status = "Started"
		_ = s.Store.Put(x)
		msg := err.Error()
		if errors.Is(err, ErrShareCode) {
			msg = "That share code does not open this file. Use the 4 digits you chose when downloading it."
		}
		s.renderOffline(w, 422, c, x, tok, msg)
		return
	}
	a := o.Aadhaar
	x.Aadhaar, x.Method = &a, MethodOffline
	x.Checks = compare(x.Hint, x.Aadhaar, nil)
	x.Status, x.Completed = "Completed", s.now().UTC().Format(time.RFC3339)
	if err := s.Store.Put(x); err != nil {
		page(w, 500, "Something went wrong", "Please try again in a minute.", x.ReturnURL)
		return
	}
	_ = s.Store.Count(c.ID, "completed_offline", s.now())
	s.notify(c, x)
	if s.Log != nil {
		s.Log.Info("offline e-KYC completed", "customer", c.ID, "session", x.ID, "mobile", a.MobileMatch, "email", a.EmailMatch)
	}
	http.Redirect(w, r, backURL(x), http.StatusFound)
}

// practiceFile gives a practice offline e-KYC ZIP (share code 1234) for
// the investor's own details. Simulate mode only.
func (s *Server) practiceFile(w http.ResponseWriter, r *http.Request) {
	_, x, ok := s.linkSession(w, r)
	if !ok || s.practice == nil {
		return
	}
	b, err := s.practice.practiceZip(x.Hint, "1234", s.now())
	if err != nil {
		http.Error(w, "could not make the practice file", 500)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="offlineaadhaar-practice.zip"`)
	w.Write(b)
}
