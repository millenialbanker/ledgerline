package api

import (
	"crypto/rand"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"sync"

	"ledgerline/internal/money"
	"ledgerline/internal/ops"
)

var sipRoutesDone sync.Map // *Server -> registered

// stockSIPRoutes: stock SIPs (register, the day's instalments, hand-off to
// the OMS, fills, top-up mandates), the client portal's SIP page, and
// Cashfree's notices for top-up debits.
func (s *Server) stockSIPRoutes() {
	if s.ops == nil {
		return
	}
	if _, dup := sipRoutesDone.LoadOrStore(s, true); dup {
		return // already registered on this server
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	C := func(p string, h http.HandlerFunc, m bool) { clientRoutes[p] = true; s.route(p, h, m) }
	R("GET /v1/stock-sips", s.sipScreen, false)
	R("POST /v1/stock-sips", s.sipCreate, true)
	R("POST /v1/stock-sips/preview", s.sipPreview, true)
	R("POST /v1/stock-sips/sips/{id}/action", s.sipAction, true)
	R("GET /v1/stock-sips/settings", s.sipSettingsGet, false)
	R("PUT /v1/stock-sips/settings", s.sipSettingsPut, true)
	R("POST /v1/stock-sips/run", s.sipRun, true)
	R("POST /v1/stock-sips/send", s.sipSend, true)
	R("GET /v1/stock-sips/batches/{id}/file", s.sipBasketFile, false)
	R("POST /v1/stock-sips/batches/{id}/withdraw", s.sipBasketWithdraw, true)
	R("POST /v1/stock-sips/match", s.sipMatch, true)
	R("POST /v1/stock-sips/orders/{id}/fill", s.sipFill, true)
	R("POST /v1/stock-sips/topups", s.sipTopups, true)
	R("GET /v1/stock-sips/lists/{kind}", s.sipListGet, false)
	R("POST /v1/stock-sips/lists/{kind}", s.sipListUpload, true)
	R("GET /v1/stock-sips/report.csv", s.sipReportCSV, false)
	R("POST /v1/stock-sips/mandates/{ucc}", s.sipMandateStart, true)
	C("GET /v1/portal/stock-sips", s.portalSIPs, false)
	C("POST /v1/portal/stock-sips", s.sipCreate, true)
	C("POST /v1/portal/stock-sips/preview", s.sipPreview, true)
	C("POST /v1/portal/stock-sips/{id}/action", s.sipAction, true)
	C("POST /v1/portal/stock-sips/mandate", s.portalSIPMandate, true)
	// Not signed in: Cashfree's notices carry its signature; the
	// authorisation page is reached by its unguessable token.
	s.mux.HandleFunc("POST /hooks/{tenant}/stocksip/cashfree", s.sipPayHook)
	s.mux.HandleFunc("GET /hooks/{tenant}/stocksip/mandate/{token}", s.sipMandatePage)
}

func (s *Server) sipScreen(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.StockSIPView(r.Context(), tenant(r), d)
	ok(w, v, err)
}

func (s *Server) sipCreate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ops.SIPRequest
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CreateStockSIP(r.Context(), tenant(r), actor(r), b.Date, b.SIPRequest)
	ok(w, v, err)
}

func (s *Server) sipPreview(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ops.SIPRequest
		Date string `json:"date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PreviewStockSIP(r.Context(), tenant(r), actor(r), b.Date, b.SIPRequest)
	ok(w, v, err)
}

func (s *Server) sipAction(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Action string `json:"action"`
		Until  string `json:"until"`
		Date   string `json:"date"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ChangeStockSIP(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Action, b.Until, b.Date, b.Reason)
	ok(w, v, err)
}

func (s *Server) sipSettingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.ops.StockSIPSettings(r.Context(), tenant(r)))
}

func (s *Server) sipSettingsPut(w http.ResponseWriter, r *http.Request) {
	var b ops.SIPSettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveStockSIPSettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

type sipDateBody struct {
	Date  string `json:"date"`
	Final bool   `json:"final"`
}

func (s *Server) sipRun(w http.ResponseWriter, r *http.Request) {
	var b sipDateBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RunStockSIPs(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, v, err)
}

func (s *Server) sipSend(w http.ResponseWriter, r *http.Request) {
	var b sipDateBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SendStockSIPOrders(r.Context(), tenant(r), actor(r), b.Date)
	ok(w, v, err)
}

func (s *Server) sipBasketFile(w http.ResponseWriter, r *http.Request) {
	name, b, err := s.ops.StockSIPBasket(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.NewReplacer(`"`, "", "\\", "").Replace(name)+`"`)
	w.Write(b)
}

func (s *Server) sipBasketWithdraw(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.WithdrawStockSIPBasket(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Reason)
	v.Content = nil
	ok(w, v, err)
}

func (s *Server) sipMatch(w http.ResponseWriter, r *http.Request) {
	var b sipDateBody
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MatchStockSIPFills(r.Context(), tenant(r), actor(r), b.Date, b.Final)
	ok(w, v, err)
}

func (s *Server) sipFill(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Qty   int64     `json:"qty"`
		Price money.Dec `json:"price"`
		Note  string    `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordStockSIPFill(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Qty, b.Price, b.Note)
	ok(w, v, err)
}

func (s *Server) sipTopups(w http.ResponseWriter, r *http.Request) {
	var b struct {
		TradeDate string `json:"trade_date"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RaiseStockSIPTopups(r.Context(), tenant(r), actor(r), b.TradeDate)
	ok(w, v, err)
}

func (s *Server) sipListGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.StockSIPList(r.Context(), tenant(r), r.PathValue("kind"))
	ok(w, v, err)
}

func (s *Server) sipListUpload(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 5<<20+1))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	v, err := s.ops.UploadStockSIPList(r.Context(), tenant(r), actor(r), r.PathValue("kind"), r.URL.Query().Get("name"), data)
	ok(w, map[string]any{"kind": v.Kind, "count": len(v.Items), "uploaded_at": v.UploadedAt}, err)
}

func (s *Server) sipReportCSV(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	b, err := s.ops.StockSIPReportCSV(r.Context(), tenant(r), d)
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="stock_sips_`+d+`.csv"`)
	w.Write(b)
}

func (s *Server) sipMandateStart(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.StartStockSIPMandate(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), s.publicURL(r))
	ok(w, v, err)
}

func (s *Server) portalSIPs(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	v, err := s.ops.PortalStockSIPs(r.Context(), tenant(r), actor(r), r.URL.Query().Get("ucc"), d)
	ok(w, v, err)
}

func (s *Server) portalSIPMandate(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC string `json:"ucc"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.StartStockSIPMandate(r.Context(), tenant(r), actor(r), b.UCC, s.publicURL(r))
	ok(w, v, err)
}

// sipPayHook takes Cashfree's subscription notices (mandate status, top-up
// debit results), signed with the client secret.
func (s *Server) sipPayHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the body", nil)
		return
	}
	res, err := s.ops.StockSIPPaymentHook(r.Context(), r.PathValue("tenant"), r.Header, body)
	ok(w, map[string]string{"result": res}, err)
}

// sipMandatePage is where the client authorises a top-up mandate in
// Cashfree's checkout.
func (s *Server) sipMandatePage(w http.ResponseWriter, r *http.Request) {
	p, _ := s.ops.StockSIPMandatePage(r.Context(), r.PathValue("tenant"), r.PathValue("token"))
	nb := make([]byte, 16)
	_, _ = rand.Read(nb)
	p.Nonce = base64.StdEncoding.EncodeToString(nb)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src https://sdk.cashfree.com 'nonce-"+p.Nonce+"'; connect-src https://*.cashfree.com; frame-src https://*.cashfree.com; img-src https: data:; form-action https://*.cashfree.com; frame-ancestors 'none'")
	_ = ops.SIPMandateHTML.Execute(w, p)
}
