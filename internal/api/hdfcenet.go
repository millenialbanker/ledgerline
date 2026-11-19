package api

import (
	"errors"
	"net/http"

	"ledgerline/internal/ops"
)

// hdfcENetRoutes: the HDFC Bank ENet payout file of a payout batch, the
// file sequence and RTGS marking. Registered with the payout routes.
func (s *Server) hdfcENetRoutes(R func(string, http.HandlerFunc, bool)) {
	R("GET /v1/payouts/batches/{id}/hdfc", s.hdfcView, false)
	R("POST /v1/payouts/batches/{id}/hdfc", s.hdfcMake, true)
	R("GET /v1/payouts/hdfc-files/{fid}", s.hdfcDownload, false)
	R("POST /v1/payouts/hdfc-files/{fid}/cancel", s.hdfcCancel, true)
	R("GET /v1/payouts/hdfc-seq", s.hdfcSeqGet, false)
	R("PUT /v1/payouts/hdfc-seq", s.hdfcSeqPut, true)
	R("POST /v1/payouts/{id}/rtgs", s.payoutRTGS, true)
	// The same batch by HDFC's CBX batch payment API (hdfcpay.go).
	R("GET /v1/payouts/batches/{id}/hdfc-api", s.hdfcAPIView, false)
	R("POST /v1/payouts/batches/{id}/hdfc-api", s.hdfcAPISend, true)
	R("POST /v1/payouts/batches/{id}/hdfc-api/check", s.hdfcAPICheck, true)
	R("POST /v1/hdfc-pay/key", s.hdfcPayKey, true)
}

func (s *Server) hdfcAPIView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.HDFCPayView(r.Context(), tenant(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) hdfcAPISend(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.SendPayoutBatchHDFC(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	var b *ops.PayoutFileBlocked
	if errors.As(err, &b) {
		fields := make([]map[string]string, len(b.Problems))
		for i, p := range b.Problems {
			fields[i] = map[string]string{"field": "", "message": p}
		}
		writeErr(w, 422, "invalid", "Batch "+b.Batch+" cannot go to HDFC yet. Fix these and try again:", map[string]any{"fields": fields, "problems": b.Problems})
		return
	}
	ok(w, v, err)
}

func (s *Server) hdfcAPICheck(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.CheckPayoutBatchHDFC(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) hdfcPayKey(w http.ResponseWriter, r *http.Request) {
	var b ops.NCLMSubject
	if !decode(w, r, &b) {
		return
	}
	k, err := s.ops.HDFCPayMakeKey(r.Context(), tenant(r), actor(r), b)
	ok(w, k, err)
}

func (s *Server) hdfcView(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.HDFCPayoutFile(r.Context(), tenant(r), r.PathValue("id"))
	ok(w, v, err)
}

// hdfcFail lists every problem that blocks the file.
func hdfcFail(w http.ResponseWriter, err error) {
	var b *ops.PayoutFileBlocked
	if errors.As(err, &b) {
		fields := make([]map[string]string, len(b.Problems))
		for i, p := range b.Problems {
			fields[i] = map[string]string{"field": "", "message": p}
		}
		writeErr(w, 422, "invalid", "The HDFC ENet file for "+b.Batch+" is blocked. Fix these and try again:", map[string]any{"fields": fields, "problems": b.Problems})
		return
	}
	fail(w, err, nil)
}

func (s *Server) hdfcMake(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Series string `json:"series"`
	}
	if r.ContentLength != 0 && !decode(w, r, &b) {
		return
	}
	f, err := s.ops.MakeHDFCPayoutFile(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Series)
	if err != nil {
		hdfcFail(w, err)
		return
	}
	f.Content = ""
	writeJSON(w, 200, f)
}

func (s *Server) hdfcDownload(w http.ResponseWriter, r *http.Request) {
	name, content, err := s.ops.DownloadHDFCPayoutFile(r.Context(), tenant(r), actor(r), r.PathValue("fid"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=us-ascii")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Write(content)
}

func (s *Server) hdfcCancel(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.CancelHDFCPayoutFile(r.Context(), tenant(r), actor(r), r.PathValue("fid"), b.Reason)
	ok(w, v, err)
}

func (s *Server) hdfcSeqGet(w http.ResponseWriter, r *http.Request) {
	d, okd := qdate(w, r)
	if !okd {
		return
	}
	m, err := s.ops.HDFCNextSeq(r.Context(), tenant(r), ops.HDFCSeriesMain, d)
	if err != nil {
		fail(w, err, nil)
		return
	}
	sec, err := s.ops.HDFCNextSeq(r.Context(), tenant(r), ops.HDFCSeriesSecond, d)
	ok(w, map[string]any{"main": m, "second": sec}, err)
}

func (s *Server) hdfcSeqPut(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Series string `json:"series"`
		Date   string `json:"date"`
		Next   int64  `json:"next"`
	}
	if !decode(w, r, &b) {
		return
	}
	if _, okd := dateParam(w, b.Date, "date", true); !okd {
		return
	}
	v, err := s.ops.SetHDFCNextSeq(r.Context(), tenant(r), actor(r), b.Series, b.Date, b.Next)
	ok(w, v, err)
}

func (s *Server) payoutRTGS(w http.ResponseWriter, r *http.Request) {
	var b struct {
		RTGS bool `json:"rtgs"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MarkPayoutRTGS(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.RTGS)
	ok(w, v, err)
}
