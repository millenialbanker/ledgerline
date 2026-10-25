package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/ops"
)

// Automations: the screen's routes (signed-in staff), and the routes the
// Ledgerline bot on a paired computer calls with its own token.

func (s *Server) rpaRoutes() {
	if s.ops == nil {
		return
	}
	R := func(p string, h http.HandlerFunc, m bool) {
		scopedRoutes[p] = true // branch staff use automations too; the server decides what each may do
		s.route(p, h, m)
	}
	R("GET /v1/rpa", s.rpaHome, false)
	R("GET /v1/rpa/flows/{id}", s.rpaFlow, false)
	R("POST /v1/rpa/flows", s.rpaSaveFlow, true)
	R("PUT /v1/rpa/flows/{id}", s.rpaSaveFlow, true)
	R("DELETE /v1/rpa/flows/{id}", s.rpaDeleteFlow, true)
	R("POST /v1/rpa/flows/{id}/run", s.rpaRun, true)
	R("POST /v1/rpa/record", s.rpaRecord, true)
	R("GET /v1/rpa/record/{id}", s.rpaRecording, false)
	R("POST /v1/rpa/record/{id}/stop", s.rpaRecordStop, true)
	R("GET /v1/rpa/runs/{id}", s.rpaRunGet, false)
	R("GET /v1/rpa/runs/{id}/shot", s.rpaShot, false)
	R("POST /v1/rpa/runs/{id}/{what}", s.rpaControl, true)
	R("POST /v1/rpa/pair", s.rpaPairCode, true)
	R("DELETE /v1/rpa/agents/{id}", s.rpaRevoke, true)
	R("POST /v1/rpa/creds", s.rpaSaveCred, true)
	R("PUT /v1/rpa/creds/{id}", s.rpaSaveCred, true)
	R("DELETE /v1/rpa/creds/{id}", s.rpaDeleteCred, true)

	// The bot's routes: no session, no cookies; a token per computer.
	A := func(p string, h func(http.ResponseWriter, *http.Request, string, ops.RPAAgent)) {
		s.mux.HandleFunc(p, s.agentAuth(h))
	}
	s.mux.HandleFunc("POST /agent/v1/{tenant}/pair", s.agentPair)
	A("GET /agent/v1/{tenant}/next", s.agentNext)
	A("POST /agent/v1/{tenant}/recordings/{id}/steps", s.agentRecSteps)
	A("POST /agent/v1/{tenant}/recordings/{id}/done", s.agentRecDone)
	A("POST /agent/v1/{tenant}/runs/{id}/event", s.agentEvent)
	A("POST /agent/v1/{tenant}/runs/{id}/done", s.agentRunDone)
	A("POST /agent/v1/{tenant}/runs/{id}/secret", s.agentSecret)
	A("POST /agent/v1/{tenant}/runs/{id}/load", s.agentLoad)
	A("POST /agent/v1/{tenant}/runs/{id}/notify", s.agentNotify)
}

func (s *Server) rpaHome(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RPAHome(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) rpaFlow(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RPAFlowGet(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) rpaSaveFlow(w http.ResponseWriter, r *http.Request) {
	var b ops.RPAFlowInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveRPAFlow(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) rpaDeleteFlow(w http.ResponseWriter, r *http.Request) {
	err := s.ops.DeleteRPAFlow(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]bool{"deleted": err == nil}, err)
}

func (s *Server) rpaRun(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Agent string `json:"agent"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.StartRPARun(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Agent)
	ok(w, v, err)
}

func (s *Server) rpaRecord(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Agent    string `json:"agent"`
		Flow     string `json:"flow"`
		Name     string `json:"name"`
		StartURL string `json:"start_url"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.StartRPARecording(r.Context(), tenant(r), actor(r), b.Agent, b.Flow, b.Name, b.StartURL)
	ok(w, v, err)
}

func (s *Server) rpaRecording(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RPARecordingGet(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) rpaRecordStop(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Cancel bool `json:"cancel"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.StopRPARecording(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Cancel)
	ok(w, v, err)
}

func (s *Server) rpaRunGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RPARunGet(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, v, err)
}

func (s *Server) rpaShot(w http.ResponseWriter, r *http.Request) {
	b, err := s.ops.RPARunShot(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Write(b)
}

func (s *Server) rpaControl(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ControlRPARun(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("what"))
	ok(w, v, err)
}

func (s *Server) rpaPairCode(w http.ResponseWriter, r *http.Request) {
	code, exp, err := s.ops.RPAPairCode(r.Context(), tenant(r), actor(r))
	ok(w, map[string]string{"code": code, "expires": exp, "tenant": tenant(r), "server": s.publicURL(r)}, err)
}

func (s *Server) rpaRevoke(w http.ResponseWriter, r *http.Request) {
	err := s.ops.RevokeRPAAgent(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]bool{"revoked": err == nil}, err)
}

func (s *Server) rpaSaveCred(w http.ResponseWriter, r *http.Request) {
	var b ops.RPACredInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveRPACred(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) rpaDeleteCred(w http.ResponseWriter, r *http.Request) {
	err := s.ops.DeleteRPACred(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]bool{"deleted": err == nil}, err)
}

// ---- the bot ----

func (s *Server) agentAuth(h func(http.ResponseWriter, *http.Request, string, ops.RPAAgent)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		t := r.PathValue("tenant")
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !s.knownTenant(t) {
			writeErr(w, 401, "unauthorized", "This computer is not paired with this firm.", nil)
			return
		}
		a, err := s.ops.RPAAgentAuth(r.Context(), t, tok)
		if err != nil {
			writeErr(w, 401, "unauthorized", "This computer is not paired, or was removed in Ledgerline; pair it again.", nil)
			return
		}
		h(w, r, t, a)
	}
}

// pairing codes: a few tries per address, so codes cannot be guessed
var pairTries = struct {
	sync.Mutex
	m map[string][]time.Time
}{m: map[string][]time.Time{}}

func pairAllowed(ip string) bool {
	pairTries.Lock()
	defer pairTries.Unlock()
	now := time.Now()
	var keep []time.Time
	for _, t := range pairTries.m[ip] {
		if now.Sub(t) < 10*time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= 10 {
		pairTries.m[ip] = keep
		return false
	}
	pairTries.m[ip] = append(keep, now)
	return true
}

func (s *Server) agentPair(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !pairAllowed(ip) {
		writeErr(w, 429, "too_many", "Too many pairing attempts; wait ten minutes.", nil)
		return
	}
	t := r.PathValue("tenant")
	var b struct {
		Code    string `json:"code"`
		Name    string `json:"name"`
		OS      string `json:"os"`
		Version string `json:"version"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !s.knownTenant(t) {
		writeErr(w, 403, "forbidden", "That code is not valid; make a new one in Ledgerline.", nil)
		return
	}
	a, tok, err := s.ops.RPAPair(r.Context(), t, b.Code, b.Name, b.OS, b.Version)
	if err != nil {
		fail(w, err, nil)
		return
	}
	writeJSON(w, 201, map[string]string{"agent": a.ID, "name": a.Name, "user": a.User, "token": tok})
}

func (s *Server) agentNext(w http.ResponseWriter, r *http.Request, t string, a ops.RPAAgent) {
	wait, _ := strconv.Atoi(r.URL.Query().Get("wait"))
	if wait < 0 || wait > 25 {
		wait = 25
	}
	deadline := time.Now().Add(time.Duration(wait) * time.Second)
	for {
		j, err := s.ops.RPANextJob(r.Context(), t, a)
		if err != nil {
			fail(w, err, nil)
			return
		}
		if j != nil {
			writeJSON(w, 200, j)
			return
		}
		if time.Now().After(deadline) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (s *Server) agentRecSteps(w http.ResponseWriter, r *http.Request, t string, a ops.RPAAgent) {
	var b struct {
		Steps []ops.RPAStep `json:"steps"`
	}
	if !decode(w, r, &b) {
		return
	}
	stop, err := s.ops.RPARecordSteps(r.Context(), t, a, r.PathValue("id"), b.Steps)
	ok(w, map[string]bool{"stop": stop}, err)
}

func (s *Server) agentRecDone(w http.ResponseWriter, r *http.Request, t string, a ops.RPAAgent) {
	v, err := s.ops.RPARecordDone(r.Context(), t, a, r.PathValue("id"))
	ok(w, map[string]string{"workflow": v.Saved, "status": v.Status}, err)
}

func (s *Server) agentEvent(w http.ResponseWriter, r *http.Request, t string, a ops.RPAAgent) {
	var b ops.RPAEvent
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RPARunEvent(r.Context(), t, a, r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) agentRunDone(w http.ResponseWriter, r *http.Request, t string, a ops.RPAAgent) {
	var b struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Shot   string `json:"shot"` // base64 PNG of the screen, when a step failed
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 12<<20))
	if err := dec.Decode(&b); err != nil {
		writeErr(w, 400, "bad_request", "The request is not valid JSON.", nil)
		return
	}
	shot, _ := base64.StdEncoding.DecodeString(b.Shot)
	err := s.ops.RPARunDone(r.Context(), t, a, r.PathValue("id"), b.Status, b.Error, shot)
	ok(w, map[string]bool{"ok": err == nil}, err)
}

func (s *Server) agentSecret(w http.ResponseWriter, r *http.Request, t string, a ops.RPAAgent) {
	var b struct {
		Step  int    `json:"step"`
		Cred  string `json:"cred"`
		Field string `json:"field"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.RPASecret(r.Context(), t, a, r.PathValue("id"), b.Step, b.Cred, b.Field)
	ok(w, map[string]string{"value": v}, err)
}

func (s *Server) agentLoad(w http.ResponseWriter, r *http.Request, t string, a ops.RPAAgent) {
	step, _ := strconv.Atoi(r.URL.Query().Get("step"))
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFileBytes))
	if err != nil {
		writeErr(w, 413, "too_large", "File exceeds the 64 MB upload limit.", nil)
		return
	}
	res, err := s.ops.RPALoadFile(r.Context(), t, a, r.PathValue("id"), step, r.URL.Query().Get("name"), data)
	if err != nil {
		fail(w, err, res)
		return
	}
	writeJSON(w, 201, res)
}

func (s *Server) agentNotify(w http.ResponseWriter, r *http.Request, t string, a ops.RPAAgent) {
	var b struct {
		Step int    `json:"step"`
		Text string `json:"text"`
	}
	if !decode(w, r, &b) {
		return
	}
	err := s.ops.RPANotify(r.Context(), t, a, r.PathValue("id"), b.Step, b.Text)
	ok(w, map[string]bool{"ok": err == nil}, err)
}
