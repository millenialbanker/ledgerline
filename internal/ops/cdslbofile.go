package ops

import (
	"fmt"
	"regexp"
	"strings"
)

// ---- CDSL BO upload files: the fixed-width writer ----
//
// CDSL's BO setup (upload ID 08) and BO modify (11) files, and the BO
// signature (06) and closure (13) files, are fixed-width text: a header
// (DP ID, operator ID, number of records), then for each record its lines,
// each line's fields placed by position with no separators. Numbers are
// right-justified and zero-padded; text is left-justified and padded with
// spaces; a field with no value is spaces. In a modify file only the
// fields that change are given, and a field to be made blank is filled
// with '@' to its length. The layouts (cdslbolayout.go) are from CDSL's
// communiqués CDSL/OPS/SYSTM/2023/404 and CDSL/OPS/DP/SYSTM/5565.

// boFileField is one field of a line.
type boFileField struct {
	Key   string
	Name  string
	Start int  // 1-based position on the line
	Len   int  // characters
	Type  byte // N number, C character, D date DDMMYYYY, T date and time DDMMYYYYHHMMSS
	Input byte // M mandatory, O optional, N not modifiable
}

// boClear in a value makes the field blank at CDSL (written as '@' to the
// field's length; modify files only).
const boClear = "\x00clear"

// boEOL ends every line of the files. The communiqués do not say; CDAS
// upload files are DOS text, so CR LF.
const boEOL = "\r\n"

// boLine is one line of a record: its layout ("s02", "m08"…), the line
// number written in its first two places, and the values by field key.
type boLine struct {
	Layout string
	No     int
	Who    string // for problems: "Holder", "Nominee 1"…
	V      map[string]string
}

// boFile is a whole upload file.
type boFile struct {
	Prefix   string // 08 setup, 11 modify, 06 signature, 13 closure
	DPID     string
	Operator string
	Records  [][]boLine
}

var (
	reBODigits = regexp.MustCompile(`^[0-9]*$`)
	reBOText   = regexp.MustCompile(`^[\x20-\x7E]*$`)
)

// boFormat writes one line. Problems are in words; the line is still the
// right length so a caller can show it.
func boFormat(l boLine, modify bool) (string, []string) {
	fs, ok := boLayouts[l.Layout]
	if !ok {
		return "", []string{"unknown line layout " + l.Layout}
	}
	var b strings.Builder
	var probs []string
	who := firstOf(l.Who, "Record")
	known := map[string]bool{}
	for _, f := range fs {
		known[f.Key] = true
		v := l.V[f.Key]
		if f.Key == "line" && v == "" {
			v = fmt.Sprintf("%02d", l.No)
		}
		bad := func(why string) { probs = append(probs, fmt.Sprintf("%s: %s %s", who, strings.ToLower(f.Name), why)) }
		switch {
		case v == boClear:
			if !modify {
				bad("cannot be cleared in a setup file")
			} else if f.Input == 'N' {
				bad("cannot be changed at CDSL")
			}
			b.WriteString(strings.Repeat("@", f.Len))
			continue
		case v == "":
			if f.Input == 'M' {
				bad("is missing")
			}
			b.WriteString(strings.Repeat(" ", f.Len))
			continue
		}
		if modify && f.Input == 'N' {
			bad("cannot be changed at CDSL; leave it out")
		}
		switch f.Type {
		case 'N':
			if !reBODigits.MatchString(v) {
				bad(fmt.Sprintf("must be digits only (%q)", v))
				v = strings.Repeat("0", f.Len)
			}
			v = strings.TrimLeft(v, "0")
			if len(v) > f.Len {
				bad(fmt.Sprintf("is longer than CDSL's %d digits", f.Len))
				v = v[len(v)-f.Len:]
			}
			b.WriteString(strings.Repeat("0", f.Len-len(v)) + v)
		case 'D', 'T':
			want := map[byte]int{'D': 8, 'T': 14}[f.Type]
			if len(v) != want || !reBODigits.MatchString(v) || want != f.Len {
				bad(fmt.Sprintf("must be a date written as %d digits (%q)", want, v))
				v = strings.Repeat(" ", f.Len)
			}
			b.WriteString(v)
		default:
			if !reBOText.MatchString(v) {
				bad(fmt.Sprintf("has characters CDSL's file cannot carry (%q)", v))
				v = strings.Map(func(r rune) rune {
					if r < 0x20 || r > 0x7E {
						return ' '
					}
					return r
				}, v)
			}
			if len(v) > f.Len {
				bad(fmt.Sprintf("is longer than CDSL's %d characters (%q)", f.Len, v))
				v = v[:f.Len]
			}
			b.WriteString(v + strings.Repeat(" ", f.Len-len(v)))
		}
	}
	for k, v := range l.V {
		if !known[k] && v != "" {
			probs = append(probs, fmt.Sprintf("%s: no field %q on line %s", who, k, l.Layout))
		}
	}
	return b.String(), probs
}

// Bytes writes the file. It is not written when anything is wrong.
func (f boFile) Bytes() ([]byte, []string) {
	var b strings.Builder
	h, probs := boFormat(boLine{Layout: "hdr", Who: "Header", V: map[string]string{"dp_id": f.DPID, "operator_id": f.Operator, "records": fmt.Sprint(len(f.Records))}}, false)
	b.WriteString(h + boEOL)
	modify := f.Prefix == "11"
	if len(f.Records) == 0 {
		probs = append(probs, "the file has no records")
	}
	for _, rec := range f.Records {
		for _, l := range rec {
			s, p := boFormat(l, modify)
			probs = append(probs, p...)
			b.WriteString(s + boEOL)
		}
	}
	if len(probs) > 0 {
		return nil, dedupe(probs)
	}
	return []byte(b.String()), nil
}

// boFileName is CDSL's name for an upload file: the upload ID, the DP ID
// in six digits, a dot and a running serial number of 3 to 5 digits.
func boFileName(prefix, dpid string, serial int64) string {
	d := strings.TrimLeft(strings.TrimSpace(dpid), "0")
	if len(d) < 6 {
		d = strings.Repeat("0", 6-len(d)) + d
	}
	return fmt.Sprintf("%s%s.%03d", prefix, d, serial%100000)
}

var reBOFileName = regexp.MustCompile(`^(06|08|11|13)[0-9]{6}\.[0-9]{3,5}$`)
