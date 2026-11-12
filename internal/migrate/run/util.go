package run

import (
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Within says whether child is parent or inside it. windows chooses
// Windows rules (\ and / both separators, case does not matter, drive
// letters) whatever system the tool runs on, so the logic is tested for
// both. Both paths must be absolute and cleaned by the caller's system.
func Within(parent, child string, windows bool) bool {
	norm := func(p string) []string {
		if windows {
			p = strings.ToLower(strings.ReplaceAll(p, `\`, "/"))
			if strings.HasPrefix(p, "//?/") { // long path prefix
				p = p[4:]
			}
		}
		var parts []string
		for _, s := range strings.Split(p, "/") {
			switch s {
			case "", ".":
			case "..":
				if len(parts) > 0 {
					parts = parts[:len(parts)-1]
				}
			default:
				parts = append(parts, s)
			}
		}
		return parts
	}
	a, b := norm(parent), norm(child)
	if len(b) < len(a) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// WithinHere is Within with this system's rules, on absolute paths.
func WithinHere(parent, child string) bool {
	p, err1 := filepath.Abs(parent)
	c, err2 := filepath.Abs(child)
	if err1 != nil || err2 != nil {
		return true // be safe: treat as inside
	}
	if ep, err := filepath.EvalSymlinks(p); err == nil {
		p = ep
	}
	if ec, err := evalExisting(c); err == nil {
		c = ec
	}
	return Within(p, c, runtime.GOOS == "windows")
}

// evalExisting resolves symlinks in the longest existing part of p.
func evalExisting(p string) (string, error) {
	rest := ""
	for {
		if e, err := filepath.EvalSymlinks(p); err == nil {
			if rest == "" {
				return e, nil
			}
			return filepath.Join(e, rest), nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return p, nil
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// Indian groups a number the Indian way: 12,34,567 and -1,23,456.78. Text
// that is not a number comes back as it is.
func Indian(s string) string {
	t := strings.TrimSpace(s)
	sign := ""
	if strings.HasPrefix(t, "-") || strings.HasPrefix(t, "+") {
		sign, t = t[:1], t[1:]
		if sign == "+" {
			sign = ""
		}
	}
	ip, fp, hasDot := strings.Cut(t, ".")
	if ip == "" || strings.Trim(ip, "0123456789") != "" || (hasDot && strings.Trim(fp, "0123456789") != "") {
		return s
	}
	var out string
	if len(ip) <= 3 {
		out = ip
	} else {
		out = ip[len(ip)-3:]
		rest := ip[:len(ip)-3]
		for len(rest) > 2 {
			out = rest[len(rest)-2:] + "," + out
			rest = rest[:len(rest)-2]
		}
		if rest != "" {
			out = rest + "," + out
		}
	}
	if hasDot {
		out += "." + fp
	}
	return sign + out
}

// IndianInt groups an integer.
func IndianInt(n int64) string { return Indian(strconv.FormatInt(n, 10)) }

// Size is a byte count for people: 512 KB, 23.5 MB, 4.1 GB.
func Size(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1<<10:
		return strconv.FormatInt(n>>10, 10) + " KB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}
