package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"

	"ledgerline/internal/ops"
)

// Boards: import from Trello, export and print, collections and recently
// viewed boards (internal/ops/boardio.go).

func init() {
	boardRouteHooks = append(boardRouteHooks, func(s *Server, R func(p string, h http.HandlerFunc, m bool)) {
		R("POST /v1/board-imports/trello/preview", s.bioTrelloPreview, true)
		R("POST /v1/board-imports/trello", s.bioTrelloImport, true)
		R("GET /v1/boards/{id}/export", s.bioExport, false)
		R("GET /v1/boards/{id}/cards/{card}/export", s.bioCardExport, false)
		R("POST /v1/boards/{id}/printed", s.bioPrinted, true)
		R("GET /v1/boards/recent", s.bioRecent, false)
		R("POST /v1/boards/{id}/viewed", s.bioViewed, true)
		R("GET /v1/board-collections", s.bioCollections, false)
		R("POST /v1/board-collections", s.bioCollSave, true)
		R("PUT /v1/board-collections/{id}", s.bioCollSave, true)
		R("POST /v1/board-collections/{id}/delete", s.bioCollDelete, true)
		R("POST /v1/board-collections/{id}/boards", s.bioCollBoard, true)
	})
}

// bioReadAll reads a request body of up to max bytes, answering 413 above.
func bioReadAll(w http.ResponseWriter, r *http.Request, max int64, what string) ([]byte, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, max))
	if err != nil {
		var mb *http.MaxBytesError
		if errors.As(err, &mb) {
			writeErr(w, http.StatusRequestEntityTooLarge, "too_large", what, nil)
		} else {
			writeErr(w, http.StatusBadRequest, "bad_request", "The upload did not arrive whole; try again.", nil)
		}
		return nil, false
	}
	return b, true
}

// The preview's body is the Trello export itself.
func (s *Server) bioTrelloPreview(w http.ResponseWriter, r *http.Request) {
	raw, okk := bioReadAll(w, r, ops.BoardImportMax, "A Trello export can be up to 20 MB.")
	if !okk {
		return
	}
	v, err := s.ops.PreviewTrelloImport(r.Context(), tenant(r), actor(r), raw)
	ok(w, v, err)
}

// The import's body is {"name", "members", "trello": <the export>}.
func (s *Server) bioTrelloImport(w http.ResponseWriter, r *http.Request) {
	raw, okk := bioReadAll(w, r, ops.BoardImportMax+(1<<20), "A Trello export can be up to 20 MB.")
	if !okk {
		return
	}
	var in ops.TrelloImportInput
	if err := json.Unmarshal(raw, &in); err != nil {
		writeErr(w, 400, "bad_json", "Send the board's name, the member mapping and the Trello export as JSON: "+err.Error(), nil)
		return
	}
	v, err := s.ops.ImportTrello(r.Context(), tenant(r), actor(r), in)
	ok(w, v, err)
}

func bioAttachment(w http.ResponseWriter, ctype, name string) {
	ascii := strings.Map(func(c rune) rune {
		if c < 32 || c > 126 || c == '"' || c == '\\' {
			return '_'
		}
		return c
	}, name)
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", `attachment; filename="`+ascii+`"; filename*=UTF-8''`+neturl.PathEscape(name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
}

// GET /v1/boards/{id}/export?format=json|csv
func (s *Server) bioExport(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Query().Get("format") {
	case "csv":
		b, name, err := s.ops.ExportBoardCSV(r.Context(), tenant(r), actor(r), r.PathValue("id"))
		if err != nil {
			fail(w, err, nil)
			return
		}
		bioAttachment(w, "text/csv; charset=utf-8", name)
		w.Write(b)
	case "json", "":
		v, err := s.ops.ExportBoardJSON(r.Context(), tenant(r), actor(r), r.PathValue("id"))
		if err != nil {
			fail(w, err, nil)
			return
		}
		bioAttachment(w, "application/json; charset=utf-8", bioFileName(v.Board.Name)+".json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
	default:
		writeErr(w, 400, "bad_format", "format is json or csv", nil)
	}
}

func bioFileName(s string) string {
	out := strings.Map(func(c rune) rune {
		if c == '/' || c == '\\' || c == '"' || c < 32 {
			return '-'
		}
		return c
	}, strings.TrimSpace(s))
	if out == "" {
		return "board"
	}
	if r := []rune(out); len(r) > 60 {
		out = string(r[:60])
	}
	return out
}

func (s *Server) bioCardExport(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.ExportCardJSON(r.Context(), tenant(r), actor(r), r.PathValue("id"), r.PathValue("card"))
	if err != nil {
		fail(w, err, nil)
		return
	}
	bioAttachment(w, "application/json; charset=utf-8", fmt.Sprintf("%s card %d.json", bioFileName(v.Board.Name), v.Card.No))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (s *Server) bioPrinted(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Card string `json:"card"`
	}
	if !decode(w, r, &b) {
		return
	}
	err := s.ops.BoardPrinted(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Card)
	ok(w, map[string]bool{"logged": err == nil}, err)
}

func (s *Server) bioRecent(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.RecentBoards(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) bioViewed(w http.ResponseWriter, r *http.Request) {
	err := s.ops.BoardViewed(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]bool{"viewed": err == nil}, err)
}

func (s *Server) bioCollections(w http.ResponseWriter, r *http.Request) {
	v, err := s.ops.BoardCollections(r.Context(), tenant(r), actor(r))
	ok(w, v, err)
}

func (s *Server) bioCollSave(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SaveBoardCollection(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Name)
	ok(w, v, err)
}

func (s *Server) bioCollDelete(w http.ResponseWriter, r *http.Request) {
	err := s.ops.DeleteBoardCollection(r.Context(), tenant(r), actor(r), r.PathValue("id"))
	ok(w, map[string]bool{"deleted": err == nil}, err)
}

func (s *Server) bioCollBoard(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Board string `json:"board"`
		On    bool   `json:"on"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := s.ops.SetBoardInCollection(r.Context(), tenant(r), actor(r), r.PathValue("id"), b.Board, b.On)
	ok(w, v, err)
}
