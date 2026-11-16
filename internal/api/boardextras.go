package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"ledgerline/internal/ops"
)

// Boards: templates, mirror cards and email-to-board
// (internal/ops/boardextras.go).

var (
	// bxMailLimit caps the emails one board address takes.
	bxMailLimit = &limiter{max: 120, per: time.Hour}
	// bxMailMiss slows down a caller trying addresses that do not exist.
	bxMailMiss = &limiter{max: 30, per: 15 * time.Minute}
)

func init() {
	boardRouteHooks = append(boardRouteHooks, func(s *Server, R func(p string, h http.HandlerFunc, m bool)) {
		R("GET /v1/board-templates", s.bxTemplates, false)
		R("POST /v1/board-templates/{id}/use", s.bxUseTemplate, true)
		R("GET /v1/boards/{id}/mirror-targets", s.bxMirrorTargets, false)
		R("POST /v1/boards/{id}/cards/{card}/mirror", s.bxMirror, true)
		R("GET /v1/boards/{id}/cards/{card}/mirror", s.bxMirrorSource, false)
		R("PATCH /v1/boards/{id}/cards/{card}/mirror", s.bxMirrorEdit, true)
		R("GET /v1/boards/{id}/email", s.bxEmail, false)
		R("POST /v1/boards/{id}/email", s.bxSetEmail, true)
		// A mail provider posts inbound mail here. There is no staff
		// session: the long random key in the address is the secret.
		s.mux.HandleFunc("POST /inbound/boards/{tenant}/{key}", s.bxInbound)
	})
}

func (s *Server) bxTemplates(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BoardTemplates(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) bxUseTemplate(w http.ResponseWriter, r *http.Request) {
	var b ops.BoardTemplateUse
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.UseBoardTemplate(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) bxMirrorTargets(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MirrorTargets(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) bxMirror(w http.ResponseWriter, r *http.Request) {
	var b ops.CardMove
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.MirrorCard(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b)
	ok(w, v, err)
}

func (s *Server) bxMirrorSource(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.MirrorSource(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"))
	ok(w, v, err)
}

func (s *Server) bxMirrorEdit(w http.ResponseWriter, r *http.Request) {
	var b ops.MirrorEdit
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.EditMirrorSource(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"), b)
	ok(w, v, err)
}

// bxEmailOut adds the full address when the server knows its public URL.
func (s *Server) bxEmailOut(w http.ResponseWriter, v ops.BoardEmailInfo, err error) {
	out := map[string]any{"on": v.On, "path": v.Path, "list": v.List, "lists": v.Lists, "url": ""}
	if v.Path != "" && s.PublicURL != "" {
		out["url"] = strings.TrimRight(s.PublicURL, "/") + v.Path
	}
	ok(w, out, err)
}

func (s *Server) bxEmail(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BoardEmail(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	s.bxEmailOut(w, v, err)
}

func (s *Server) bxSetEmail(w http.ResponseWriter, r *http.Request) {
	var b ops.BoardEmailInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetBoardEmail(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	s.bxEmailOut(w, v, err)
}

// bxBlocked reports, without counting a hit, whether key is over its limit.
func (l *limiter) bxBlocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, t := range l.hits[key] {
		if time.Since(t) < l.per {
			n++
		}
	}
	return n >= l.max
}

func (s *Server) bxInbound(w http.ResponseWriter, r *http.Request) {
	if s.ops == nil {
		writeErr(w, http.StatusNotFound, "not_found", "No board takes mail at this address.", nil)
		return
	}
	t, key, ip := r.PathValue("tenant"), r.PathValue("key"), clientIP(r)
	if bxMailMiss.bxBlocked(ip) {
		w.Header().Set("Retry-After", "900")
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "Too many requests to unknown addresses; try again later.", nil)
		return
	}
	if !s.ops.BoardEmailKnown(r.Context(), t, key) {
		bxMailMiss.allow(ip)
		writeErr(w, http.StatusNotFound, "not_found", "No board takes mail at this address.", nil)
		return
	}
	if !bxMailLimit.allow(t + "|" + key) {
		w.Header().Set("Retry-After", "600")
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "This board has taken too many emails in the last hour; try again later.", nil)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, ops.BoardMaxMail))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "An email can be up to 10 MB.", nil)
		return
	}
	c, dup, err := s.ops.BoardInboundMail(r.Context(), t, key, bxRawMail(r.Header.Get("Content-Type"), raw))
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"card": c.ID, "no": c.No, "duplicate": dup})
}

// bxRawMail is the raw message from what the mail provider posted: the
// message itself (message/rfc822 or any other type), SendGrid's raw form
// field "email", or the "RawEmail" of Postmark's JSON.
func bxRawMail(ctype string, body []byte) []byte {
	mt, params, _ := mime.ParseMediaType(ctype)
	switch {
	case mt == "multipart/form-data" && params["boundary"] != "":
		mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				return body
			}
			if p.FormName() == "email" {
				if b, err := io.ReadAll(p); err == nil {
					return b
				}
				return body
			}
		}
	case mt == "application/json":
		var j struct {
			RawEmail string `json:"RawEmail"`
		}
		if json.Unmarshal(body, &j) == nil && j.RawEmail != "" {
			return []byte(j.RawEmail)
		}
	}
	return body
}
