package api

import (
	"io"
	"net/http"
	"strings"

	"ledgerline/internal/money"
	"ledgerline/internal/ops"
)

// vendorRoutes: the vendor master, the bill register and bill scanning.
func (s *Server) vendorRoutes() {
	s.route("GET /v1/vendors", s.vendors, false)
	s.route("POST /v1/vendors", s.saveVendor, true)
	s.route("GET /v1/vendor-bills", s.vendorBills, false)
	s.route("GET /v1/vendor-bills/csv", s.vendorBillsCSV, false)
	s.route("POST /v1/vendor-bills", s.saveVendorBill, true)
	s.route("POST /v1/vendor-bills/{id}/pay", s.payVendorBill, true)
	s.route("GET /v1/bill-scans", s.billScans, false)
	s.route("POST /v1/bill-scans", s.scanBill, true)
	s.route("GET /v1/bill-scans/{id}", s.billScan, false)
	s.route("GET /v1/bill-scans/{id}/file", s.billScanFile, false)
	s.route("POST /v1/bill-scans/{id}/ai", s.readBillAI, true)
	s.route("POST /v1/bill-scans/{id}/discard", s.discardBillScan, true)
}

func (s *Server) vendors(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.Vendors(r.Context(), tenant(r), actor(r))
	ok(w, map[string]any{"vendors": v, "tds_sections": ops.TDSSections}, err)
}

func (s *Server) saveVendor(w http.ResponseWriter, r *http.Request) {
	var v ops.Vendor
	if !decode(w, r, &v) {
		return
	}
	out, err := s.ops.SaveVendor(r.Context(), tenant(r), actor(r), v)
	ok(w, out, err)
}

func (s *Server) vendorBills(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := s.ops.VendorBills(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
	ok(w, v, err)
}

func (s *Server) vendorBillsCSV(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	b, err := s.ops.BillRegisterCSV(r.Context(), tenant(r), actor(r), q.Get("from"), q.Get("to"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="vendor_bills.csv"`)
	w.Write(b)
}

func (s *Server) saveVendorBill(w http.ResponseWriter, r *http.Request) {
	var b ops.VendorBill
	if !decode(w, r, &b) {
		return
	}
	out, err := s.ops.SaveVendorBill(r.Context(), tenant(r), actor(r), b)
	ok(w, out, err)
}

func (s *Server) payVendorBill(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Date   string       `json:"date"`
		Bank   string       `json:"bank"`
		Ref    string       `json:"ref"`
		Amount money.Amount `json:"amount"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := s.ops.PayVendorBill(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Date, b.Bank, b.Ref, b.Amount)
	ok(w, out, err)
}

func (s *Server) billScans(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BillScans(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) scanBill(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, ops.MaxBillBytes+1))
	if err != nil {
		writeErr(w, 400, "bad_request", "cannot read the file", nil)
		return
	}
	q := r.URL.Query()
	v, err := s.ops.ScanBill(r.Context(), tenant(r), actor(r), q.Get("name"), data, q.Get("ai") == "1")
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, v)
}

func (s *Server) billScan(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BillScan(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) billScanFile(w http.ResponseWriter, r *http.Request) {
	d, b, err := s.ops.BillScanFile(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", d.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Disposition", `inline; filename="`+strings.NewReplacer(`"`, "", "\\", "").Replace(d.Name)+`"`)
	w.Write(b)
}

func (s *Server) readBillAI(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ReadBillWithAI(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) discardBillScan(w http.ResponseWriter, r *http.Request) {
	err := s.ops.DiscardBillScan(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]bool{"ok": err == nil}, err)
}
