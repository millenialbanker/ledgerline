package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PluginKYC connects Ledgerline to the Ledgerline KYC plugin
// (cmd/ledgerline-kyc), which reads the applicant's Aadhaar and PAN from
// DigiLocker under the firm's own DigiLocker registration. The other checks
// (PAN and KRA lookups, penny drop, face match, e-Sign) go to Base.
type PluginKYC struct {
	URL  string // the plugin's address, e.g. https://kyc.example.in
	Key  string // the firm's API key for the plugin
	Base KYCProvider
	HTTP *http.Client
}

func (p *PluginKYC) Name() string {
	if p.Base != nil && p.Base.Name() != "Not connected" {
		return "Ledgerline KYC (DigiLocker) and " + p.Base.Name()
	}
	return "Ledgerline KYC (DigiLocker)"
}

func (p *PluginKYC) base() KYCProvider {
	if p.Base != nil {
		return p.Base
	}
	return manualKYC{}
}

func (p *PluginKYC) PAN(pan, name, dob string) AppCheck { return p.base().PAN(pan, name, dob) }
func (p *PluginKYC) KRA(pan string) AppCheck            { return p.base().KRA(pan) }
func (p *PluginKYC) DigiLocker(a *Application) (AppCheck, bool) {
	return AppCheck{Name: "Aadhaar (DigiLocker)", Status: "Check manually", Detail: "Continue to DigiLocker to share your Aadhaar.", Provider: p.Name()}, false
}
func (p *PluginKYC) PennyDrop(account, ifsc, name string) AppCheck {
	return p.base().PennyDrop(account, ifsc, name)
}
func (p *PluginKYC) FaceMatch(photo, panImage []byte) AppCheck {
	return p.base().FaceMatch(photo, panImage)
}
func (p *PluginKYC) ESign(formHash, name string) AppCheck { return p.base().ESign(formHash, name) }

func (p *PluginKYC) call(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.URL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.Key)
	req.Header.Set("Content-Type", "application/json")
	hc := p.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("the KYC plugin could not be reached: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Error.Message == "" {
			e.Error.Message = resp.Status
		}
		return fmt.Errorf("KYC plugin: %s", e.Error.Message)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// StartDigiLocker opens a plugin session and returns the applicant's link.
func (p *PluginKYC) StartDigiLocker(ctx context.Context, ref, returnURL string, hint DigiLockerHint) (string, string, error) {
	var r struct{ ID, URL string }
	err := p.call(ctx, http.MethodPost, "/v1/sessions", map[string]any{"reference": ref, "return_url": returnURL, "hint": hint}, &r)
	if err == nil && (r.ID == "" || r.URL == "") {
		err = errors.New("KYC plugin: no session in the reply")
	}
	return r.URL, r.ID, err
}

// DigiLockerResult reads the session; once finished it is deleted from the
// plugin, so the documents are kept only in Ledgerline.
func (p *PluginKYC) DigiLockerResult(ctx context.Context, session string) (DigiLockerResult, error) {
	var r DigiLockerResult
	if err := p.call(ctx, http.MethodGet, "/v1/sessions/"+url.PathEscape(session), nil, &r); err != nil {
		return r, err
	}
	if r.Status == "Completed" || r.Status == "Failed" || r.Status == "Expired" {
		_ = p.call(ctx, http.MethodDelete, "/v1/sessions/"+url.PathEscape(session), nil, nil)
	}
	return r, nil
}

// SimulatedKYC is the practice provider used in development.
func SimulatedKYC() KYCProvider { return simKYC{} }
