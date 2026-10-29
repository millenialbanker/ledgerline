package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"ledgerline/internal/money"
	"ledgerline/internal/ops"
)

// mfRoutes: mutual fund distribution (internal/ops/mf*.go). GET /v1/mf
// (the order list) stays in opsRoutes. Safe to call twice.
func (s *Server) mfRoutes() {
	if s.ops == nil {
		return
	}
	if req, err := http.NewRequest(http.MethodGet, "/v1/mf/status", nil); err == nil {
		if _, p := s.mux.Handler(req); p == "GET /v1/mf/status" {
			return
		}
	}
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	R("GET /v1/mf/status", s.mfStatus, false)
	R("PUT /v1/mf/settings", s.mfSaveSettings, true)
	R("GET /v1/mf/schemes", s.mfSchemes, false)
	R("POST /v1/mf/nav", s.mfNAVUpload, true)
	R("POST /v1/mf/nav/fetch", s.mfNAVFetch, true)
	R("POST /v1/mf/import", s.mfImport, true)
	R("GET /v1/mf/orders", s.mfOrders, false)
	R("POST /v1/mf/orders", s.mfPlace, true)
	R("POST /v1/mf/orders/{id}/cancel", s.mfCancel, true)
	R("POST /v1/mf/orders/{id}/allot", s.mfAllot, true)
	R("GET /v1/mf/sips", s.mfSIPs, false)
	R("POST /v1/mf/sips", s.mfStartSIP, true)
	R("POST /v1/mf/sips/{id}/cancel", s.mfCancelSIP, true)
	R("POST /v1/mf/sips/{id}/pause", s.mfPauseSIP, true)
	R("POST /v1/mf/sips/{id}/instalment", s.mfInstalment, true)
	R("GET /v1/mf/mandates", s.mfMandates, false)
	R("POST /v1/mf/clients/{ucc}/mandates", s.mfAddMandate, true)
	R("POST /v1/mf/clients/{ucc}/mandates/{id}", s.mfMandateStatus, true)
	R("GET /v1/mf/clients/{ucc}", s.mfClient, false)
	R("PUT /v1/mf/clients/{ucc}", s.mfSaveProfile, true)
	R("POST /v1/mf/clients/{ucc}/register", s.mfRegister, true)
	R("POST /v1/mf/clients/{ucc}/registration", s.mfMarkReg, true)
	R("POST /v1/mf/clients/{ucc}/route", s.mfRoute, true)
	R("GET /v1/mf/units", s.mfUnits, false)
	R("POST /v1/mf/units", s.mfAddUnits, true)
	R("GET /v1/mf/commission", s.mfCommission, false)
	R("POST /v1/mf/commission/accrue", s.mfAccrue, true)
	// The client portal.
	R("GET /v1/portal/mf", s.portalMF, false)
	R("GET /v1/portal/mf/schemes", s.portalMFSchemes, false)
	R("POST /v1/portal/mf/orders", s.portalMFOrder, true)
	R("POST /v1/portal/mf/sips", s.portalMFSIP, true)
	R("POST /v1/portal/mf/sips/{id}/cancel", s.portalMFCancelSIP, true)
	R("POST /v1/portal/mf/profile", s.portalMFProfile, true)
	for _, p := range []string{"GET /v1/portal/mf", "GET /v1/portal/mf/schemes", "POST /v1/portal/mf/orders", "POST /v1/portal/mf/sips",
		"POST /v1/portal/mf/sips/{id}/cancel", "POST /v1/portal/mf/profile"} {
		clientRoutes[p] = true
	}
}

func (s *Server) mfStatus(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MFStatus(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) mfSaveSettings(w http.ResponseWriter, r *http.Request) {
	var b ops.MFSettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveMFSettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) mfSchemes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	v, err := s.ops.MFSchemes(r.Context(), tenant(r), q.Get("q"), q.Get("plan"), q.Get("platform"), limit)
	ok(w, v, err)
}

func (s *Server) portalMFSchemes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.MFSchemes(r.Context(), tenant(r), q.Get("q"), q.Get("plan"), q.Get("platform"), 50)
	ok(w, v, err)
}

const maxMFFile = 64 << 20

func readMFBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxMFFile+1))
	if err != nil || len(data) > maxMFFile {
		writeErr(w, 400, "bad_request", "cannot read the file (at most 64 MB)", nil)
		return nil, false
	}
	return data, true
}

func (s *Server) mfNAVUpload(w http.ResponseWriter, r *http.Request) {
	data, okb := readMFBody(w, r)
	if !okb {
		return
	}
	v, err := s.ops.LoadMFNAV(r.Context(), tenant(r), actor(r), r.URL.Query().Get("name"), data)
	ok(w, v, err)
}

func (s *Server) mfNAVFetch(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.FetchMFNAV(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

// mfImport takes the file as the body; kind, platform, mapping, save_as,
// name and columns (JSON: field -> heading) in the query.
func (s *Server) mfImport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := ops.MFImportRequest{Kind: q.Get("kind"), Platform: q.Get("platform"), Mapping: q.Get("mapping"), SaveAs: q.Get("save_as"), Name: q.Get("name")}
	if c := q.Get("columns"); c != "" {
		if err := json.Unmarshal([]byte(c), &req.Columns); err != nil {
			writeErr(w, 400, "bad_columns", "columns is a JSON object of field to column heading", nil)
			return
		}
	}
	data, okb := readMFBody(w, r)
	if !okb {
		return
	}
	v, err := s.ops.ImportMFFile(r.Context(), tenant(r), actor(r), req, data)
	ok(w, v, err)
}

func (s *Server) mfOrders(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MFOrders(r.Context(), tenant(r))
	if u := r.URL.Query().Get("ucc"); u != "" && err == nil {
		out := []ops.MFOrder{}
		for _, x := range v {
			if x.UCC == u {
				out = append(out, x)
			}
		}
		v = out
	}
	ok(w, v, err)
}

func (s *Server) mfPlace(w http.ResponseWriter, r *http.Request) {
	var b ops.MFOrderInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PlaceMFOrder(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) mfCancel(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CancelMFOrder(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Note)
	ok(w, v, err)
}

func (s *Server) mfAllot(w http.ResponseWriter, r *http.Request) {
	var b ops.MFAllotment
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordMFAllotment(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) mfSIPs(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MFSIPs(r.Context(), tenant(r), r.URL.Query().Get("ucc"))
	ok(w, v, err)
}

func (s *Server) mfStartSIP(w http.ResponseWriter, r *http.Request) {
	var b ops.MFSIPInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.StartMFSIP(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) mfCancelSIP(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason     string `json:"reason"`
		OnPlatform bool   `json:"on_platform"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CancelMFSIP(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Reason, b.OnPlatform)
	ok(w, v, err)
}

func (s *Server) mfPauseSIP(w http.ResponseWriter, r *http.Request) {
	var b struct {
		From       string `json:"from"`
		Count      int    `json:"count"`
		OnPlatform bool   `json:"on_platform"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PauseMFSIP(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.From, b.Count, b.OnPlatform)
	ok(w, v, err)
}

func (s *Server) mfInstalment(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date    string `json:"date"`
		OK      bool   `json:"ok"`
		OrderNo string `json:"order_no"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordMFSIPInstalment(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.OK, b.OrderNo, b.Reason)
	ok(w, v, err)
}

func (s *Server) mfMandates(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MFMandates(r.Context(), tenant(r))
	ok(w, v, err)
}

func (s *Server) mfAddMandate(w http.ResponseWriter, r *http.Request) {
	var b ops.MFMandateInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.AddMFMandate(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b)
	ok(w, v, err)
}

func (s *Server) mfMandateStatus(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Status string `json:"status"`
		UMRN   string `json:"umrn"`
		Remark string `json:"remark"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetMFMandateStatus(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), r.PathValue("id"), b.Status, b.UMRN, b.Remark)
	ok(w, v, err)
}

func (s *Server) mfClient(w http.ResponseWriter, r *http.Request) {
	ucc := r.PathValue("ucc")
	if !s.canSee(w, r, ucc) {
		return
	}
	p, err := s.ops.MFProfile(r.Context(), tenant(r), ucc)
	if err != nil {
		fail(w, err, nil)
		return
	}
	u, _ := s.ops.MFUnits(r.Context(), tenant(r), ucc)
	sips, _ := s.ops.MFSIPs(r.Context(), tenant(r), ucc)
	ok(w, map[string]any{"profile": ops.MaskMFProfile(p), "units": u, "sips": sips}, nil)
}

func (s *Server) mfSaveProfile(w http.ResponseWriter, r *http.Request) {
	var b ops.MFProfileUpdate
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveMFProfile(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b)
	ok(w, ops.MaskMFProfile(v), err)
}

func (s *Server) mfRegister(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Platform string `json:"platform"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RegisterMFClient(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Platform)
	ok(w, ops.MaskMFProfile(v), err)
}

func (s *Server) mfMarkReg(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Platform string `json:"platform"`
		Status   string `json:"status"`
		Remark   string `json:"remark"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkMFRegistration(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Platform, b.Status, b.Remark)
	ok(w, ops.MaskMFProfile(v), err)
}

func (s *Server) mfRoute(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Route string `json:"route"`
		Note  string `json:"note"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetMFRoute(r.Context(), tenant(r), actor(r), r.PathValue("ucc"), b.Route, b.Note)
	ok(w, v, err)
}

func (s *Server) mfUnits(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MFUnits(r.Context(), tenant(r), r.URL.Query().Get("ucc"))
	ok(w, v, err)
}

func (s *Server) mfAddUnits(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC   string `json:"ucc"`
		ISIN  string `json:"isin"`
		Mode  string `json:"mode"`
		Folio string `json:"folio"`
		Date  string `json:"date"`
		Kind  string `json:"kind"`
		Units string `json:"units"`
		NAV   string `json:"nav"`
	}
	if !decode(w, r, &b) {
		return
	}
	u, err := money.ParseDec(b.Units)
	if err != nil {
		writeErr(w, 422, "invalid", "units must be a number", nil)
		return
	}
	nav, _ := money.ParseDec(b.NAV) // optional; zero when not given
	v, err := s.ops.AddMFUnits(r.Context(), tenant(r), actor(r), ops.MFUnitTxn{UCC: b.UCC, ISIN: b.ISIN, Mode: b.Mode, Folio: b.Folio, Date: b.Date, Kind: b.Kind, Units: u, NAV: nav})
	ok(w, v, err)
}

func (s *Server) mfCommission(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MFCommission(r.Context(), tenant(r), r.URL.Query().Get("month"))
	ok(w, v, err)
}

func (s *Server) mfAccrue(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Month string `json:"month"`
	}
	if !decode(w, r, &b) {
		return
	}
	if _, err := s.ops.AccrueMFTrail(r.Context(), tenant(r), actor(r), b.Month); err != nil {
		fail(w, err, nil)
		return
	}
	v, err := s.ops.MFCommission(r.Context(), tenant(r), b.Month)
	ok(w, v, err)
}

func (s *Server) portalMF(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PortalMF(r.Context(), tenant(r), actor(r), r.URL.Query().Get("ucc"))
	ok(w, v, err)
}

func (s *Server) portalMFOrder(w http.ResponseWriter, r *http.Request) {
	var b ops.MFOrderInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PortalMFOrder(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) portalMFSIP(w http.ResponseWriter, r *http.Request) {
	var b ops.MFSIPInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PortalMFSIP(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) portalMFCancelSIP(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.PortalCancelMFSIP(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) portalMFProfile(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UCC       string          `json:"ucc"`
		FATCA     bool            `json:"fatca"`
		Nominees  []ops.MFNominee `json:"nominees"`
		NomOptOut bool            `json:"nomination_opt_out"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SavePortalMFProfile(r.Context(), tenant(r), actor(r), b.UCC, b.FATCA, b.Nominees, b.NomOptOut)
	ok(w, ops.MaskMFProfile(v), err)
}
