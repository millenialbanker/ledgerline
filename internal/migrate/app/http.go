package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"ledgerline/internal/migrate/disk"
	"ledgerline/internal/migrate/sqlserver"
)

//go:embed static
var static embed.FS

// UI serves the page and its API on 127.0.0.1. Every request must carry
// the random token (?t= in the page's address, X-LL-Token on API calls),
// come from this computer, and name 127.0.0.1:<port> as its host.
type UI struct {
	S     *Session
	Token string
	Addr  string // 127.0.0.1:port once listening
	Quit  chan struct{}

	quitOnce sync.Once
	ln       net.Listener
	srv      *http.Server
	lastMu   sync.Mutex
	last     time.Time
}

// Idle is how long since the page last asked the tool anything.
func (u *UI) Idle() time.Duration {
	u.lastMu.Lock()
	defer u.lastMu.Unlock()
	if u.last.IsZero() {
		return 0
	}
	return time.Since(u.last)
}

// NewToken makes the random token for the page's address.
func NewToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Listen opens 127.0.0.1:port (0: a random port). Never any other address.
func (u *UI) Listen(port int) error {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("cannot open the local page on port %d: %w", port, err)
	}
	u.ln = ln
	u.Addr = ln.Addr().String()
	if u.Token == "" {
		u.Token = NewToken()
	}
	if u.Quit == nil {
		u.Quit = make(chan struct{})
	}
	return nil
}

// URL is the page's address, with the token.
func (u *UI) URL() string { return "http://" + u.Addr + "/?t=" + u.Token }

// Serve serves until Shutdown.
func (u *UI) Serve() error {
	u.srv = &http.Server{Handler: u.Handler(), ReadHeaderTimeout: 10 * time.Second}
	err := u.srv.Serve(u.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops the page.
func (u *UI) Shutdown() {
	if u.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		u.srv.Shutdown(ctx)
	}
}

func (u *UI) quit() { u.quitOnce.Do(func() { close(u.Quit) }) }

// Handler is the whole site.
func (u *UI) Handler() http.Handler {
	if u.Quit == nil {
		u.Quit = make(chan struct{})
	}
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	files := http.FileServer(http.FS(sub))
	mux.Handle("GET /app.js", files)
	mux.Handle("GET /app.css", files)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := fs.ReadFile(sub, "index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(strings.ReplaceAll(string(b), "__TOKEN__", u.Token)))
	})
	api := func(pattern string, h func(w http.ResponseWriter, r *http.Request) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			v, err := h(w, r)
			w.Header().Set("Content-Type", "application/json")
			if err != nil {
				code := http.StatusBadRequest
				var he *httpErr
				if errors.As(err, &he) {
					code = he.code
				}
				w.WriteHeader(code)
				json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "value": v})
				return
			}
			if v == nil {
				v = map[string]bool{"ok": true}
			}
			json.NewEncoder(w).Encode(v)
		})
	}
	s := u.S
	api("GET /api/state", func(w http.ResponseWriter, r *http.Request) (any, error) { return s.State(), nil })
	api("POST /api/server/retry", func(w http.ResponseWriter, r *http.Request) (any, error) {
		go s.FindServer(context.Background())
		return nil, nil
	})
	api("POST /api/server/login", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var in struct {
			Endpoint int    `json:"endpoint"`
			Instance string `json:"instance"`
			User     string `json:"user"`
			Password string `json:"password"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		st := s.State()
		var e sqlserver.Endpoint
		switch {
		case in.Instance != "":
			var err error
			if e, err = ParseServer(in.Instance); err != nil {
				return nil, err
			}
		case in.Endpoint >= 0 && in.Endpoint < len(st.Server.Endpoints):
			e = st.Server.Endpoints[in.Endpoint]
		default:
			return nil, errors.New("choose a SQL Server")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		return nil, s.Connect(ctx, e, sqlserver.Login{User: strings.TrimSpace(in.User), Password: in.Password})
	})
	api("POST /api/docker/check", func(w http.ResponseWriter, r *http.Request) (any, error) {
		go s.CheckDocker(context.Background())
		return nil, nil
	})
	api("POST /api/docker/start", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var in struct {
			Developer bool `json:"developer"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		go s.StartDocker(context.Background(), in.Developer)
		return nil, nil
	})
	api("POST /api/upload", func(w http.ResponseWriter, r *http.Request) (any, error) {
		q := r.URL.Query()
		off, err1 := strconv.ParseInt(q.Get("offset"), 10, 64)
		size, err2 := strconv.ParseInt(q.Get("size"), 10, 64)
		if err1 != nil || err2 != nil {
			return nil, errors.New("bad offset or size")
		}
		body := http.MaxBytesReader(w, r.Body, 16<<20)
		got, err := s.Upload(q.Get("batch"), q.Get("id"), q.Get("name"), off, size, body)
		v := map[string]int64{"received": got}
		if errors.Is(err, ErrOffset) {
			return v, &httpErr{code: http.StatusConflict, msg: err.Error()}
		}
		return v, err
	})
	api("POST /api/skipped", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var in struct {
			Name string `json:"name"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		if _, err := SafeRel(in.Name); err != nil {
			return nil, err
		}
		s.Skip(in.Name)
		return nil, nil
	})
	api("POST /api/files/remove", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var in struct {
			ID string `json:"id"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return nil, s.Remove(in.ID)
	})
	api("POST /api/password", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var in struct {
			ID       string `json:"id"`
			Password string `json:"password"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		if err := s.SetPassword(in.ID, in.Password); err != nil {
			return nil, err
		}
		return nil, s.Scan()
	})
	api("POST /api/scan", func(w http.ResponseWriter, r *http.Request) (any, error) { return nil, s.Scan() })
	api("POST /api/live/list", func(w http.ResponseWriter, r *http.Request) (any, error) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		return s.ListLive(ctx)
	})
	api("POST /api/live/use", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var in struct {
			Names []string `json:"names"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return nil, s.UseLive(in.Names)
	})
	api("POST /api/toggle", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var in struct {
			Index int  `json:"index"`
			Skip  bool `json:"skip"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return nil, s.Toggle(r.Context(), in.Index, in.Skip)
	})
	api("POST /api/years", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var in struct {
			Years int `json:"years"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return nil, s.SetYears(r.Context(), in.Years)
	})
	api("POST /api/go", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var in struct {
			Screen string `json:"screen"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		if in.Screen == "drop" {
			return nil, s.Back()
		}
		return nil, s.Go(in.Screen)
	})
	api("POST /api/check-folder", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var in struct {
			Out string `json:"out"`
		}
		if err := decode(r, &in); err != nil {
			return nil, err
		}
		return FolderInfo(s, in.Out), nil
	})
	api("POST /api/start", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var c Choice
		if err := decode(r, &c); err != nil {
			return nil, err
		}
		return nil, s.Start(c)
	})
	api("POST /api/cancel", func(w http.ResponseWriter, r *http.Request) (any, error) {
		s.Cancel()
		return nil, nil
	})
	api("POST /api/open", func(w http.ResponseWriter, r *http.Request) (any, error) {
		res := s.Result()
		if res == nil {
			return nil, errors.New("there is no package yet")
		}
		return nil, OpenFolder(res.Report)
	})
	api("POST /api/quit", func(w http.ResponseWriter, r *http.Request) (any, error) {
		go u.quit()
		return nil, nil
	})
	return u.guard(mux)
}

type httpErr struct {
	code int
	msg  string
}

func (e *httpErr) Error() string { return e.msg }

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return errors.New("the request could not be read")
	}
	return nil
}

// guard refuses anything not from this computer, not addressed to
// 127.0.0.1:<port>, or without the token.
func (u *UI) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; form-action 'none'; base-uri 'none'")
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Host != u.Addr {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+u.Addr {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		tok := r.Header.Get("X-LL-Token")
		if tok == "" && r.Method == http.MethodGet {
			tok = r.URL.Query().Get("t")
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(u.Token)) != 1 {
			http.Error(w, "This page needs the address the migration tool printed, with its key. Close this tab and use the tool's own window.", http.StatusForbidden)
			return
		}
		u.lastMu.Lock()
		u.last = time.Now()
		u.lastMu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// ParseServer reads a server typed by the user or given to --server:
// localhost\SQLEXPRESS, .\SHAREPRO, (local), 127.0.0.1,1433. Only this
// computer is allowed.
func ParseServer(s string) (sqlserver.Endpoint, error) {
	s = strings.TrimSpace(s)
	host, inst, _ := strings.Cut(s, `\`)
	port := 0
	if h, p, ok := strings.Cut(host, ","); ok {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n <= 0 || n > 65535 {
			return sqlserver.Endpoint{}, errors.New("the port after the comma must be a number")
		}
		host, port = h, n
	}
	if i, p, ok := strings.Cut(inst, ","); ok {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n <= 0 || n > 65535 {
			return sqlserver.Endpoint{}, errors.New("the port after the comma must be a number")
		}
		inst, port = i, n
	}
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "", ".", "(local)", "localhost":
		host = "localhost"
	case "127.0.0.1", "::1":
	default:
		hn, _ := os.Hostname()
		if !strings.EqualFold(host, hn) {
			return sqlserver.Endpoint{}, errors.New("the tool only reads a SQL Server on this computer; run it on the SharePro server instead")
		}
		host = "localhost"
	}
	if port > 0 && host == "localhost" {
		host = "127.0.0.1"
	}
	return sqlserver.Endpoint{Host: host, Instance: strings.TrimSpace(inst), Port: port}, nil
}

// Folder is what the page shows about the chosen save folder.
type Folder struct {
	OK   bool   `json:"ok"`
	Free int64  `json:"free"`
	Msg  string `json:"msg,omitempty"`
}

// FolderInfo checks a save folder.
func FolderInfo(s *Session, out string) Folder {
	err := s.Validate(Choice{Passphrase: strings.Repeat("x", 12), Passphrase2: strings.Repeat("x", 12), Years: 1, Out: out})
	f := Folder{OK: err == nil}
	if err != nil {
		f.Msg = err.Error()
	}
	f.Free = -1
	if err == nil {
		if n, err := disk.Free(out); err == nil {
			f.Free = n
		}
	}
	return f
}

// OpenBrowser opens the page in the default browser.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// OpenFolder shows the folder holding file in Explorer or the Finder.
func OpenFolder(file string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer.exe", filepath.Dir(file))
	case "darwin":
		cmd = exec.Command("open", "-R", file)
	default:
		cmd = exec.Command("xdg-open", filepath.Dir(file))
	}
	return cmd.Start()
}
