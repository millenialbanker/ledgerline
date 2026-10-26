package control

import (
	"net/http"
	"strconv"

	"ledgerline/internal/money"
)

// extraRoutes: receivables, notices, alerts, editions, saved replies,
// support numbers and customer notes.
func (s *Server) extraRoutes(A func(string, func(http.ResponseWriter, *http.Request, string))) {
	A("GET /api/alerts", func(w http.ResponseWriter, r *http.Request, by string) { out(w)(s.C.Alerts(r.Context(), by)) })
	A("POST /api/alerts/ack", func(w http.ResponseWriter, r *http.Request, by string) {
		var b struct{ ID string }
		if decode(w, r, &b, 4096) {
			err := s.C.AckAlert(r.Context(), by, b.ID)
			out(w)(map[string]bool{"ok": err == nil}, err)
		}
	})
	A("GET /api/notices", func(w http.ResponseWriter, r *http.Request, by string) { out(w)(s.C.Notices(r.Context(), by)) })
	A("POST /api/notices", func(w http.ResponseWriter, r *http.Request, by string) {
		var b NoticeInput
		if decode(w, r, &b, 16<<10) {
			out(w)(s.C.SendNotice(r.Context(), by, b))
		}
	})
	A("POST /api/notices/{id}/withdraw", func(w http.ResponseWriter, r *http.Request, by string) {
		out(w)(s.C.WithdrawNotice(r.Context(), by, r.PathValue("id")))
	})
	A("POST /api/firms/{id}/edition", func(w http.ResponseWriter, r *http.Request, by string) {
		var b struct{ Edition string }
		if decode(w, r, &b, 4096) {
			out(w)(s.C.SetFirmEdition(r.Context(), by, r.PathValue("id"), b.Edition))
		}
	})
	A("GET /api/receivables", func(w http.ResponseWriter, r *http.Request, by string) {
		rows, tot, err := s.C.Receivables(r.Context(), by)
		if err != nil {
			writeErr(w, err)
			return
		}
		tds, _ := s.C.TDSRegister(r.Context(), by)
		due, _ := s.C.RemindersDue(r.Context(), by)
		cns, _ := s.C.CreditNotes(r.Context(), by, "")
		set, _ := s.C.Settings(r.Context())
		writeJSON(w, 200, map[string]any{"ageing": rows, "total": tot, "tds": tds, "reminders": due, "credit_notes": cns, "ops": set.Ops, "mail": s.C.Mailer != nil})
	})
	A("GET /api/customers/{id}/statement", func(w http.ResponseWriter, r *http.Request, by string) {
		q := r.URL.Query()
		out(w)(s.C.Statement(r.Context(), by, r.PathValue("id"), q.Get("from"), q.Get("to")))
	})
	A("GET /api/customers/{id}/statement.pdf", func(w http.ResponseWriter, r *http.Request, by string) {
		q := r.URL.Query()
		name, b, err := s.C.StatementPDF(r.Context(), by, r.PathValue("id"), q.Get("from"), q.Get("to"))
		sendPDF(w, name, b, err)
	})
	A("GET /api/invoices/{id}/pdf", func(w http.ResponseWriter, r *http.Request, by string) {
		name, b, err := s.C.InvoicePDF(r.Context(), by, r.PathValue("id"))
		sendPDF(w, name, b, err)
	})
	A("POST /api/invoices/{id}/email", func(w http.ResponseWriter, r *http.Request, by string) {
		out(w)(s.C.SendInvoice(r.Context(), by, r.PathValue("id")))
	})
	A("POST /api/invoices/{id}/remind", func(w http.ResponseWriter, r *http.Request, by string) {
		var b struct{ Notice bool }
		if decode(w, r, &b, 4096) {
			out(w)(s.C.SendReminder(r.Context(), by, r.PathValue("id"), b.Notice))
		}
	})
	A("POST /api/invoices/{id}/credit", func(w http.ResponseWriter, r *http.Request, by string) {
		var b struct {
			Amount money.Amount `json:"amount"`
			Reason string       `json:"reason"`
		}
		if decode(w, r, &b, 4096) {
			out(w)(s.C.IssueCreditNote(r.Context(), by, r.PathValue("id"), b.Amount, b.Reason))
		}
	})
	A("GET /api/invoices/{id}/credit-notes", func(w http.ResponseWriter, r *http.Request, by string) {
		out(w)(s.C.CreditNotes(r.Context(), by, r.PathValue("id")))
	})
	A("GET /api/credit-notes/{id}/pdf", func(w http.ResponseWriter, r *http.Request, by string) {
		name, b, err := s.C.CreditNotePDF(r.Context(), by, r.PathValue("id"))
		sendPDF(w, name, b, err)
	})
	A("POST /api/invoices/{id}/tds", func(w http.ResponseWriter, r *http.Request, by string) {
		var b struct {
			Index int    `json:"index"`
			Cert  string `json:"cert"`
		}
		if decode(w, r, &b, 4096) {
			out(w)(s.C.TDSCertificate(r.Context(), by, r.PathValue("id"), b.Index, b.Cert))
		}
	})
	A("GET /api/canned", func(w http.ResponseWriter, r *http.Request, by string) { out(w)(s.C.CannedReplies(r.Context(), by)) })
	A("POST /api/canned", func(w http.ResponseWriter, r *http.Request, by string) {
		var b Canned
		if decode(w, r, &b, 16<<10) {
			out(w)(s.C.SaveCanned(r.Context(), by, b))
		}
	})
	A("GET /api/support/stats", func(w http.ResponseWriter, r *http.Request, by string) {
		out(w)(s.C.SupportNumbers(r.Context(), by, r.URL.Query().Get("month")))
	})
	A("GET /api/customers/{id}/notes", func(w http.ResponseWriter, r *http.Request, by string) {
		out(w)(s.C.CustNotes(r.Context(), by, r.PathValue("id")))
	})
	A("GET /api/followups", func(w http.ResponseWriter, r *http.Request, by string) { out(w)(s.C.CustNotes(r.Context(), by, "")) })
	A("POST /api/notes", func(w http.ResponseWriter, r *http.Request, by string) {
		var b CustNote
		if decode(w, r, &b, 16<<10) {
			out(w)(s.C.AddCustNote(r.Context(), by, b))
		}
	})
}

func sendPDF(w http.ResponseWriter, name string, b []byte, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}
