package api

import (
	"net/http"

	"ledgerline/internal/money"
	"ledgerline/internal/ops"
)

// ipoProductRoutes: NSE eIPO's other products (non-competitive bids in
// G-secs, T-bills and SDLs, sovereign gold bonds, tender offers) and the
// eIPO query server's demand for an issue. Wired from ipoRoutes, so they
// register once with it. Orders are staff-only: the client portal does
// not offer these products.
func (s *Server) ipoProductRoutes() {
	R := func(p string, h http.HandlerFunc, m bool) { s.route(p, h, m) }
	// Branch staff and authorised persons take orders for their own clients.
	B := func(p string, h http.HandlerFunc, m bool) { scopedRoutes[p] = true; s.route(p, h, m) }

	B("GET /v1/ipo/products", s.ipoProducts, false)
	R("PUT /v1/ipo/products/settings", s.ipoProductSettings, true)
	R("POST /v1/ipo/products/master", s.ipoProductMaster, true)
	B("POST /v1/ipo/products/orders", s.ipoProductPlace, true)
	B("GET /v1/ipo/products/orders/{id}", s.ipoProductOrder, false)
	B("POST /v1/ipo/products/orders/{id}/modify", s.ipoProductModify, true)
	B("POST /v1/ipo/products/orders/{id}/cancel", s.ipoProductCancel, true)
	R("POST /v1/ipo/products/orders/{id}/sgb-details", s.ipoProductSGBDetails, true)
	R("POST /v1/ipo/issues/{id}/products-send", s.ipoProductSend, true)
	R("POST /v1/ipo/issues/{id}/products-inquire", s.ipoProductInquire, true)
	R("POST /v1/ipo/issues/{id}/tender-acceptance", s.ipoTenderAcceptance, true)
	R("GET /v1/ipo/issues/{id}/demand", s.ipoDemand, false)
}

func (s *Server) ipoProducts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.EIPOProducts(r.Context(), tenant(r), actor(r), q.Get("issue"), q.Get("status"))
	ok(w, v, err)
}

func (s *Server) ipoProductSettings(w http.ResponseWriter, r *http.Request) {
	var b ops.EIPOProductSettings
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetEIPOProductSettings(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) ipoProductMaster(w http.ResponseWriter, r *http.Request) {
	added, changed, err := s.ops.EIPOSyncProducts(r.Context(), tenant(r), actor(r))
	ok(w, map[string]int{"added": added, "changed": changed}, err)
}

func (s *Server) ipoProductPlace(w http.ResponseWriter, r *http.Request) {
	var b ops.EIPOOrderInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PlaceEIPOOrder(r.Context(), tenant(r), actor(r), b)
	ok(w, v, err)
}

func (s *Server) ipoProductOrder(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.EIPOOrderByID(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) ipoProductModify(w http.ResponseWriter, r *http.Request) {
	var b ops.EIPOOrderChange
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.ModifyEIPOOrder(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) ipoProductCancel(w http.ResponseWriter, r *http.Request) {
	var b ops.IPOInstructionIn
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CancelEIPOOrder(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) ipoProductSGBDetails(w http.ResponseWriter, r *http.Request) {
	var b ops.SGBKYC
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CompleteSGBApplication(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) ipoProductSend(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.EIPOSendOrders(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) ipoProductInquire(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.EIPOInquireOrders(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) ipoTenderAcceptance(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date  string                 `json:"date"`
		Price money.Dec              `json:"price"`
		Rows  []ops.TenderAcceptance `json:"rows"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RecordTenderAcceptance(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.Price, b.Rows)
	ok(w, v, err)
}

func (s *Server) ipoDemand(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.IssueDemand(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.URL.Query().Get("refresh") == "1")
	ok(w, v, err)
}
