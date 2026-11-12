package sqlserver

import (
	"context"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Instance is one SQL Server instance the SQL Browser (UDP 1434) told us
// about.
type Instance struct {
	Server    string
	Name      string // MSSQLSERVER for the default instance
	Version   string
	TCP       int
	Clustered bool
}

// ParseBrowser reads an SQL Browser answer (SSRP SVR_RESP): 0x05, a
// two-byte length, then "ServerName;X;InstanceName;Y;IsClustered;No;
// Version;15.0.2000.5;tcp;1433;;" for each instance.
func ParseBrowser(msg []byte) []Instance {
	if len(msg) < 3 || msg[0] != 5 {
		return nil
	}
	var out []Instance
	for _, rec := range strings.Split(string(msg[3:]), ";;") {
		f := strings.Split(rec, ";")
		kv := map[string]string{}
		for i := 0; i+1 < len(f); i += 2 {
			kv[strings.ToLower(f[i])] = f[i+1]
		}
		if kv["instancename"] == "" {
			continue
		}
		in := Instance{Server: kv["servername"], Name: kv["instancename"], Version: kv["version"], Clustered: strings.EqualFold(kv["isclustered"], "yes")}
		in.TCP, _ = strconv.Atoi(kv["tcp"])
		out = append(out, in)
	}
	return out
}

// browse asks the SQL Browser on host for its instances.
func browse(ctx context.Context, host string) []Instance {
	d := net.Dialer{Timeout: time.Second}
	c, err := d.DialContext(ctx, "udp", net.JoinHostPort(host, "1434"))
	if err != nil {
		return nil
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	if _, err := c.Write([]byte{3}); err != nil { // CLNT_UCAST_EX: every instance on this host
		return nil
	}
	buf := make([]byte, 64<<10)
	n, err := c.Read(buf)
	if err != nil {
		return nil
	}
	return ParseBrowser(buf[:n])
}

func tcpOpen(ctx context.Context, host string, port int) bool {
	d := net.Dialer{Timeout: 800 * time.Millisecond}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Discover finds SQL Server instances on this computer: through the SQL
// Browser, the instances Windows lists (registry), and the default port.
// It never looks beyond this computer.
func Discover(ctx context.Context) []Endpoint {
	seen := map[string]bool{}
	var out []Endpoint
	add := func(e Endpoint) {
		k := strings.ToUpper(e.Instance)
		if seen[k] {
			return
		}
		seen[k] = true
		out = append(out, e)
	}
	for _, in := range browse(ctx, "127.0.0.1") {
		e := Endpoint{Host: "localhost", Instance: in.Name}
		if strings.EqualFold(in.Name, "MSSQLSERVER") {
			e.Instance = ""
		}
		if in.TCP > 0 {
			e.Host, e.Port = "127.0.0.1", in.TCP
		}
		add(e)
	}
	for _, name := range localInstances() {
		e := Endpoint{Host: "localhost", Instance: name}
		if strings.EqualFold(name, "MSSQLSERVER") {
			e.Instance = ""
		}
		add(e)
	}
	if !seen[""] && tcpOpen(ctx, "127.0.0.1", 1433) {
		add(Endpoint{Host: "127.0.0.1", Port: 1433})
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// rank prefers the default instance (a full SQL Server is where SharePro
// usually lives), then SQLEXPRESS, then the others.
func rank(e Endpoint) int {
	switch strings.ToUpper(e.Instance) {
	case "":
		return 0
	case "SQLEXPRESS":
		return 2
	}
	return 1
}
