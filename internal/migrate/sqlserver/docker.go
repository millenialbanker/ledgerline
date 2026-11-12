package sqlserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The Docker fallback, for a Mac or Linux laptop with no SQL Server (there
// is no SQL Server for macOS, so on a Mac this is the normal path).
//
// The tool starts Microsoft's SQL Server image as Express, with a random sa
// password kept only in memory, the work folder mounted read-only at /work
// and the SQL port published on 127.0.0.1 only, on a port Docker picks. It
// then talks to it with the same driver as any other server, so values
// keep their exact types (decimals as text) and nothing differs between a
// Windows server and the container: going through `docker exec sqlcmd`
// would mean parsing sqlcmd's text output and losing exactness.
//
// The container is always removed at the end. The image is kept: removing
// it would mean another 1.5 GB download next time.

// Image is the SQL Server image (linux/amd64 only).
const Image = "mcr.microsoft.com/mssql/server:2022-latest"

// Runner runs the docker command. Tests replace it.
type Runner interface {
	// Run runs docker with args; env is added to the environment; line, if
	// not nil, gets each line of output as it comes.
	Run(ctx context.Context, env []string, line func(string), args ...string) ([]byte, error)
	// Found says whether the docker command is installed.
	Found() bool
}

// ExecRunner runs the real docker command.
type ExecRunner struct{}

func (ExecRunner) Found() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

func (ExecRunner) Run(ctx context.Context, env []string, line func(string), args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if line == nil {
		cmd.Stdout = &out
		err := cmd.Run()
		if err != nil && errb.Len() > 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(lastLines(errb.String(), 3)))
		}
		return out.Bytes(), err
	}
	pr, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
		out.WriteString(sc.Text() + "\n")
		line(sc.Text())
	}
	err = cmd.Wait()
	if err != nil && errb.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(lastLines(errb.String(), 3)))
	}
	return out.Bytes(), err
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

// DockerError is a step that needs the user, in plain English.
type DockerError struct {
	Step string // installed, running, memory, rosetta, download, start
	Msg  string
}

func (e *DockerError) Error() string { return e.Msg }

// DockerInfo is what `docker info` says.
type DockerInfo struct {
	MemTotal     int64  `json:"MemTotal"`
	Architecture string `json:"Architecture"`
	OSType       string `json:"OSType"`
	Name         string `json:"Name"`
}

// Docker runs SQL Server in a container for the tool.
type Docker struct {
	R         Runner
	Work      string // mounted read-only at /work
	Developer bool   // MSSQL_PID=Developer (the user ticked the option)
	GOOS      string // runtime.GOOS unless a test sets it
	GOARCH    string
	Status    func(msg string) // each step, shown on the screen
	Wait      time.Duration    // how long SQL Server may take to start (default 4 minutes)

	connect  func(ctx context.Context, e Endpoint, l Login) (*Server, error)
	name     string
	password string
	Info     DockerInfo
	Warnings []string
}

func (d *Docker) status(msg string) {
	if d.Status != nil {
		d.Status(msg)
	}
}

func (d *Docker) goos() string {
	if d.GOOS != "" {
		return d.GOOS
	}
	return runtime.GOOS
}

func (d *Docker) goarch() string {
	if d.GOARCH != "" {
		return d.GOARCH
	}
	return runtime.GOARCH
}

// Check says whether Docker is installed, running and big enough.
func (d *Docker) Check(ctx context.Context) error {
	d.Warnings = nil
	d.status("Looking for Docker")
	if !d.R.Found() {
		return &DockerError{Step: "installed", Msg: "Docker is not installed. There is no SQL Server for " + osName(d.goos()) + ", so the tool runs Microsoft's free SQL Server Express inside Docker. Install Docker Desktop (or OrbStack or Colima), open it, then press Try again."}
	}
	out, err := d.R.Run(ctx, nil, nil, "info", "--format", "{{json .}}")
	if err != nil {
		return &DockerError{Step: "running", Msg: "Docker is installed but not running. Open Docker Desktop and wait until it says it is running, then press Try again."}
	}
	var info DockerInfo
	if err := json.Unmarshal(bytes.TrimSpace(out), &info); err != nil {
		return &DockerError{Step: "running", Msg: "Docker did not answer as expected. Open Docker Desktop and wait until it says it is running, then press Try again."}
	}
	d.Info = info
	gb := float64(info.MemTotal) / (1 << 30)
	switch {
	case info.MemTotal > 0 && info.MemTotal < 2<<30:
		return &DockerError{Step: "memory", Msg: fmt.Sprintf("Docker has only %.1f GB of memory and SQL Server needs at least 2 GB. In Docker Desktop open Settings → Resources, set Memory to 4 GB or more, apply, then press Try again.", gb)}
	case info.MemTotal > 0 && info.MemTotal < 4<<30:
		d.Warnings = append(d.Warnings, fmt.Sprintf("Docker has %.1f GB of memory. SQL Server will work but slowly; 4 GB or more is better (Docker Desktop → Settings → Resources).", gb))
	}
	return nil
}

func osName(goos string) string {
	switch goos {
	case "darwin":
		return "the Mac"
	case "windows":
		return "this computer"
	}
	return "Linux desktops"
}

// AppleSilicon says whether the image will run under emulation.
func (d *Docker) AppleSilicon() bool { return d.goos() == "darwin" && d.goarch() == "arm64" }

// HasImage says whether the SQL Server image is already downloaded.
func (d *Docker) HasImage(ctx context.Context) bool {
	_, err := d.R.Run(ctx, nil, nil, "image", "inspect", "--format", "{{.Id}}", Image)
	return err == nil
}

// Pull downloads the image (about 1.5 GB), reporting parts done.
func (d *Docker) Pull(ctx context.Context) error {
	d.status("Downloading SQL Server Express (about 1.5 GB, first time only)")
	layers := map[string]bool{}
	done := map[string]bool{}
	_, err := d.R.Run(ctx, nil, func(l string) {
		id, what, ok := strings.Cut(l, ": ")
		if !ok || len(id) < 6 || strings.Contains(id, " ") {
			return
		}
		switch {
		case strings.HasPrefix(what, "Pulling fs layer"), strings.HasPrefix(what, "Waiting"), strings.HasPrefix(what, "Downloading"):
			layers[id] = true
		case strings.HasPrefix(what, "Pull complete"), strings.HasPrefix(what, "Already exists"):
			layers[id] = true
			done[id] = true
		default:
			return
		}
		d.status(fmt.Sprintf("Downloading SQL Server Express (about 1.5 GB, first time only): %d of %d parts done", len(done), len(layers)))
	}, "pull", "--platform", "linux/amd64", Image)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &DockerError{Step: "download", Msg: "Could not download SQL Server from Microsoft (mcr.microsoft.com). Check the internet connection and press Try again."}
	}
	return nil
}

// Password makes a random sa password meeting SQL Server's rules.
func Password() string {
	const (
		up  = "ABCDEFGHJKLMNPQRSTUVWXYZ"
		lo  = "abcdefghijkmnopqrstuvwxyz"
		dig = "23456789"
		sym = "#%+-=_"
	)
	pick := func(set string) byte {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		return set[n.Int64()]
	}
	all := up + lo + dig + sym
	b := []byte{pick(up), pick(lo), pick(dig), pick(sym)}
	for len(b) < 28 {
		b = append(b, pick(all))
	}
	return string(b)
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// Start runs the container and waits until SQL Server answers. On success
// it returns a connected Server.
func (d *Docker) Start(ctx context.Context) (*Server, error) {
	if !d.HasImage(ctx) {
		if err := d.Pull(ctx); err != nil {
			return nil, err
		}
	}
	d.name = "llmig-" + randHex(4)
	d.password = Password()
	pid := "Express"
	if d.Developer {
		pid = "Developer"
	}
	d.status("Starting SQL Server " + pid + " in Docker")
	work, err := filepath.Abs(d.Work)
	if err != nil {
		return nil, err
	}
	args := []string{"run", "-d", "--name", d.name, "--label", "ledgerline-migration=1",
		"--platform", "linux/amd64",
		"-e", "ACCEPT_EULA=Y", "-e", "MSSQL_PID=" + pid, "-e", "MSSQL_SA_PASSWORD",
		// root inside the container so SQL Server can read the tool's own
		// 0600 files in the mounted work folder
		"--user", "0:0",
		"-v", work + ":/work:ro",
		"-p", "127.0.0.1::1433",
		Image}
	// the password reaches docker through its environment, not its
	// command line
	if _, err := d.R.Run(ctx, []string{"MSSQL_SA_PASSWORD=" + d.password}, nil, args...); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if strings.Contains(err.Error(), "mounts denied") || strings.Contains(err.Error(), "not shared") {
			return nil, &DockerError{Step: "start", Msg: "Docker may not read the work folder " + work + ". In Docker Desktop open Settings → Resources → File sharing and add it (or choose a work folder inside your home folder), then press Try again."}
		}
		return nil, &DockerError{Step: "start", Msg: "Docker could not start SQL Server: " + err.Error()}
	}
	port, err := d.port(ctx)
	if err != nil {
		d.Stop(context.Background())
		return nil, err
	}
	e := Endpoint{Host: "127.0.0.1", Port: port, Docker: true}
	l := Login{User: "sa", Password: d.password}
	connect := d.connect
	if connect == nil {
		connect = Connect
	}
	wait := d.Wait
	if wait == 0 {
		wait = 4 * time.Minute
	}
	deadline := time.Now().Add(wait)
	d.status("Waiting for SQL Server to start (up to a few minutes" + map[bool]string{true: " under Rosetta on Apple silicon", false: ""}[d.AppleSilicon()] + ")")
	for {
		if err := ctx.Err(); err != nil {
			d.Stop(context.Background())
			return nil, err
		}
		if running, logs := d.running(ctx); !running {
			d.Stop(context.Background())
			return nil, d.explainExit(logs)
		}
		s, err := connect(ctx, e, l)
		if err == nil {
			d.status("SQL Server is running in Docker")
			return s, nil
		}
		if time.Now().After(deadline) {
			_, logs := d.running(ctx)
			d.Stop(context.Background())
			return nil, d.explainExit(logs)
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (d *Docker) port(ctx context.Context) (int, error) {
	out, err := d.R.Run(ctx, nil, nil, "port", d.name, "1433/tcp")
	if err != nil {
		return 0, &DockerError{Step: "start", Msg: "Docker started SQL Server but did not say which port it is on."}
	}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		host, p, err := net.SplitHostPort(strings.TrimSpace(l))
		if err == nil && host == "127.0.0.1" {
			if n, err := strconv.Atoi(p); err == nil {
				return n, nil
			}
		}
	}
	return 0, &DockerError{Step: "start", Msg: "Docker published SQL Server on an unexpected address; the tool only uses 127.0.0.1."}
}

func (d *Docker) running(ctx context.Context) (bool, string) {
	out, err := d.R.Run(ctx, nil, nil, "inspect", "--format", "{{.State.Running}}", d.name)
	if err == nil && strings.TrimSpace(string(out)) == "true" {
		return true, ""
	}
	logs, _ := d.R.Run(ctx, nil, nil, "logs", "--tail", "20", d.name)
	return false, string(logs)
}

func (d *Docker) explainExit(logs string) error {
	l := strings.ToLower(logs)
	emul := strings.Contains(l, "rosetta") || strings.Contains(l, "qemu") || strings.Contains(l, "exec format error") || strings.Contains(l, "illegal instruction")
	if d.AppleSilicon() || emul {
		if d.AppleSilicon() {
			return &DockerError{Step: "rosetta", Msg: "SQL Server could not start. Microsoft's SQL Server image is for Intel processors only, so on Apple silicon Docker must emulate it: in Docker Desktop open Settings → General, turn on \"Use Rosetta for x86/amd64 emulation on Apple Silicon\", apply and restart, then press Try again."}
		}
		return &DockerError{Step: "rosetta", Msg: "SQL Server could not start because this computer cannot run Intel (x86-64) containers. Run the tool on an Intel or AMD computer, or on the Windows server where SharePro runs."}
	}
	if strings.Contains(l, "memory") {
		return &DockerError{Step: "memory", Msg: "SQL Server stopped for lack of memory. Give Docker at least 4 GB (Docker Desktop → Settings → Resources), then press Try again."}
	}
	msg := "SQL Server in Docker did not start."
	if t := strings.TrimSpace(lastLines(logs, 3)); t != "" {
		msg += " Its last words were: " + t
	}
	return &DockerError{Step: "start", Msg: msg}
}

// ServerPath is where a file in the work folder appears inside the
// container; ok is false for files outside it.
func (d *Docker) ServerPath(local string) (string, bool) {
	work, err := filepath.Abs(d.Work)
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(local)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(work, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return "/work/" + filepath.ToSlash(rel), true
}

// Stop removes the container (never the image).
func (d *Docker) Stop(ctx context.Context) error {
	if d.name == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	_, err := d.R.Run(ctx, nil, nil, "rm", "-f", "-v", d.name)
	if err == nil {
		d.name = ""
	}
	d.password = ""
	return err
}

// Running says whether a container is up.
func (d *Docker) Running() bool { return d.name != "" }
