package api

import (
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"

	"ledgerline/internal/ops"
)

// Team messages: conversations, files, and the pulse the console polls
// for notifications.

func (s *Server) chatRoutes() {
	if s.ops == nil {
		return
	}
	// Branch staff and authorised persons use messages too; the server
	// decides what each can see. Managing channels and desks is Admin-only.
	R := func(p string, h http.HandlerFunc, m bool) {
		if !strings.Contains(p, "/channels") && !strings.Contains(p, "/desks") {
			scopedRoutes[p] = true
		}
		s.route(p, h, m)
	}
	R("GET /v1/chat", s.chatHome, false)
	R("GET /v1/chat/pulse", s.chatPulse, false)
	R("GET /v1/chat/c/{id}", s.chatMessages, false)
	R("POST /v1/chat/c/{id}/messages", s.chatPost, true)
	R("POST /v1/chat/c/{id}/read", s.chatRead, true)
	R("POST /v1/chat/c/{id}/files", s.chatUpload, true)
	R("GET /v1/chat/files/{id}", s.chatFile, false)
	R("POST /v1/chat/dm", s.chatDM, true)
	R("POST /v1/chat/threads", s.chatThread, true)
	R("POST /v1/chat/channels", s.chatSaveChannel, true)
	R("PUT /v1/chat/channels/{id}", s.chatSaveChannel, true)
	R("POST /v1/chat/desks", s.chatSaveDesk, true)
	R("PUT /v1/chat/desks/{id}", s.chatSaveDesk, true)
}

func (s *Server) chatHome(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ChatHome(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) chatPulse(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ChatPulse(r.Context(), tenant(r), actor(r), r.URL.Query().Get("since"))
	ok(w, v, err)
}

func (s *Server) chatMessages(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	v, err := s.ops.ChatMessages(r.Context(), tenant(r), actor(r), r.PathValue("id"), before)
	ok(w, v, err)
}

func (s *Server) chatPost(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Text  string   `json:"text"`
		Files []string `json:"files"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.PostChat(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Text, b.Files)
	ok(w, v, err)
}

func (s *Server) chatRead(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Seq int64 `json:"seq"`
	}
	if !decode(w, r, &b) {
		return
	}
	err := s.ops.MarkChatRead(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Seq)
	ok(w, map[string]bool{"ok": err == nil}, err)
}

func (s *Server) chatUpload(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "too_large", "Files can be up to 15 MB.", nil)
		return
	}
	v, err := s.ops.UploadChatFile(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.URL.Query().Get("name"), data)
	ok(w, v, err)
}

func (s *Server) chatFile(w http.ResponseWriter, r *http.Request) {
	f, b, err := s.ops.ChatFile(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	ascii := strings.Map(func(c rune) rune {
		if c < 32 || c > 126 || c == '"' || c == '\\' {
			return '_'
		}
		return c
	}, f.Name)
	disp := "attachment"
	if f.Image {
		disp = "inline"
	}
	w.Header().Set("Content-Type", f.Type)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Disposition", disp+`; filename="`+ascii+`"; filename*=UTF-8''`+neturl.PathEscape(f.Name))
	w.Write(b)
}

func (s *Server) chatDM(w http.ResponseWriter, r *http.Request) {
	var b struct {
		User string `json:"user"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.OpenDM(r.Context(), tenant(r), actor(r), b.User)
	ok(w, v, err)
}

func (s *Server) chatThread(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Desk  string `json:"desk"`
		Party string `json:"party"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.OpenThread(r.Context(), tenant(r), actor(r), b.Desk, b.Party)
	ok(w, v, err)
}

func (s *Server) chatSaveChannel(w http.ResponseWriter, r *http.Request) {
	var b ops.ChatChannelInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveChatChannel(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}

func (s *Server) chatSaveDesk(w http.ResponseWriter, r *http.Request) {
	var b ops.ChatDeskInput
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveChatDesk(r.Context(), tenant(r), actor(r), r.PathValue("id"), b)
	ok(w, v, err)
}
