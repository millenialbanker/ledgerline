package run

import (
	"bytes"
	"html/template"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ledgerline/internal/migrate/pkg"
)

// CheckLabel is a check's name for people.
func CheckLabel(name string) string {
	switch name {
	case pkg.CheckTBDebit:
		return "Trial balance: debit total"
	case pkg.CheckTBCredit:
		return "Trial balance: credit total"
	case pkg.CheckClients:
		return "Clients"
	case pkg.CheckClientDebit:
		return "Client debit balances: total"
	case pkg.CheckClientCredit:
		return "Client credit balances: total"
	case pkg.CheckDPAccounts:
		return "Demat accounts"
	case pkg.CheckDPQuantity:
		return "Demat holdings: total quantity"
	case pkg.CheckDocuments:
		return "Archived documents"
	case pkg.CheckBankAccounts:
		return "Client bank accounts"
	case pkg.CheckPositions:
		return "Open positions"
	}
	return name
}

// IssueGroup is the issues of one area.
type IssueGroup struct {
	Area   string
	Issues []pkg.Issue
	Errors int
}

func sevRank(s string) int {
	switch strings.ToLower(s) {
	case "error":
		return 0
	case "warning":
		return 1
	}
	return 2
}

// GroupIssues groups issues by area, areas with errors first, errors first
// within each area.
func GroupIssues(is []pkg.Issue) []IssueGroup {
	by := map[string]*IssueGroup{}
	var order []string
	for _, i := range is {
		g := by[i.Area]
		if g == nil {
			g = &IssueGroup{Area: i.Area}
			by[i.Area] = g
			order = append(order, i.Area)
		}
		g.Issues = append(g.Issues, i)
		if sevRank(i.Severity) == 0 {
			g.Errors++
		}
	}
	out := make([]IssueGroup, 0, len(order))
	for _, a := range order {
		g := by[a]
		sort.SliceStable(g.Issues, func(x, y int) bool { return sevRank(g.Issues[x].Severity) < sevRank(g.Issues[y].Severity) })
		out = append(out, *g)
	}
	sort.SliceStable(out, func(x, y int) bool { return (out[x].Errors > 0) && out[y].Errors == 0 })
	return out
}

type docRow struct {
	Firm, FY string
	Counts   []int
	Total    int
}

type reportData struct {
	Created  string
	Tool     string
	System   string
	Server   string
	Parts    []PartFile
	Beside   bool
	Plan     string
	Backups  []pkg.Backup
	Firms    []pkg.Firm
	Checks   []pkg.Check
	Groups   []IssueGroup
	Errors   int
	Warnings int
	Dropped  []pkg.Dropped
	DocTypes []string
	DocRows  []docRow
	Files    []pkg.File
	Steps    []string
}

var funcs = template.FuncMap{
	"indian": Indian,
	"int":    func(n int) string { return IndianInt(int64(n)) },
	"size":   Size,
	"base":   filepath.Base,
	"label":  CheckLabel,
	"date": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("02 Jan 2006 15:04")
	},
	"lower": strings.ToLower,
}

var reportTmpl = template.Must(template.New("report").Funcs(funcs).Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<title>Ledgerline migration report</title>
<style>
body{font:14px/1.45 system-ui,-apple-system,"Segoe UI",sans-serif;color:#16201B;margin:28px auto;max-width:1060px;padding:0 20px}
h1{font-size:22px;margin:0 0 4px}h2{font-size:16px;margin:28px 0 8px;border-bottom:1px solid #DADDD5;padding-bottom:4px}
h3{font-size:14px;margin:16px 0 6px}
.meta{color:#525D57}
table{border-collapse:collapse;width:100%;margin:6px 0 10px}
th,td{text-align:left;padding:5px 8px;border-bottom:1px solid #EAECE6;vertical-align:top}
th{font-weight:600;color:#525D57;font-size:12.5px}
td.n,th.n{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}
.mono{font-family:ui-monospace,Menlo,Consolas,monospace;font-size:12px;word-break:break-all}
.sev{font-weight:600;white-space:nowrap}.sev.error{color:#B0281F}.sev.warning{color:#9A6200}.sev.info{color:#525D57}
.no{color:#848E88}
.box{border:1px solid #DADDD5;border-radius:8px;padding:10px 14px;background:#F7F8F5}
@media print{body{margin:0;max-width:none}h2{break-after:avoid}tr{break-inside:avoid}}
</style></head><body>
<h1>Ledgerline migration report</h1>
<p class="meta">Made {{.Created}} by {{.Tool}} from {{.System}}{{if .Server}} on SQL Server {{.Server}}{{end}}. {{.Plan}}</p>

<h2>Package</h2>
<table><tr><th>File</th><th>Holds</th><th class="n">Documents</th>{{if .Beside}}<th class="n">Size</th>{{end}}</tr>
{{range .Parts}}<tr><td>{{base .Path}}</td><td>{{if .Note}}{{.Note}}{{else}}Everything{{end}}</td><td class="n">{{int .Docs}}</td>{{if $.Beside}}<td class="n">{{size .Bytes}}</td>{{end}}</tr>{{end}}
</table>
<div class="box">Next step: in Ledgerline, open Files → Migration from old system and upload {{if gt (len .Parts) 1}}every part of {{end}}this package. Ledgerline asks for the passphrase chosen in the tool; keep the passphrase separate from the file.</div>
<p>{{if .Errors}}<b>{{int .Errors}} error(s)</b> and {{end}}{{int .Warnings}} warning(s) are listed under Issues below.</p>

<h2>What was read</h2>
<table><tr><th>File</th><th>Database</th><th>What it is</th><th>Year</th><th>Backup date</th><th class="n">Size</th><th>Used</th></tr>
{{range .Backups}}<tr><td>{{.File}}</td><td>{{.Database}}</td><td>{{.Role}}</td><td>{{.FY}}</td><td>{{date .BackupDate}}</td><td class="n">{{if .Bytes}}{{size .Bytes}}{{end}}</td><td>{{if .Used}}Yes{{else}}<span class="no">No</span>{{end}}{{if .Note}}: {{.Note}}{{end}}{{if .SHA256}}<div class="mono">SHA-256 {{.SHA256}}</div>{{end}}</td></tr>{{end}}
</table>

<h2>Firms</h2>
{{if .Firms}}<table><tr><th>Code</th><th>Name</th><th>Segments</th><th>Balances as of</th></tr>
{{range .Firms}}<tr><td>{{.Code}}</td><td>{{.Name}}</td><td>{{range $i, $s := .Segments}}{{if $i}}, {{end}}{{$s}}{{end}}</td><td>{{.AsOf}}</td></tr>{{end}}</table>{{else}}<p class="no">No firms were listed.</p>{{end}}

<h2>Checks</h2>
<p class="meta">Totals as SharePro shows them. Ledgerline recalculates each one after loading and stops if any differs.</p>
{{if .Checks}}<table><tr><th>Firm</th><th>Year</th><th>Check</th><th class="n">Value</th><th>Detail</th></tr>
{{range .Checks}}<tr><td>{{.Firm}}</td><td>{{.FY}}</td><td>{{label .Name}}</td><td class="n">{{indian .Value}}</td><td>{{.Detail}}</td></tr>{{end}}</table>{{else}}<p class="no">No checks.</p>{{end}}

<h2>Issues</h2>
{{if .Groups}}{{range .Groups}}<h3>{{.Area}}</h3>
<table><tr><th>Severity</th><th>Firm</th><th>Year</th><th>Item</th><th>What</th><th>What to do</th></tr>
{{range .Issues}}<tr><td class="sev {{lower .Severity}}">{{.Severity}}</td><td>{{.Firm}}</td><td>{{.FY}}</td><td>{{.Key}}</td><td>{{.Message}}</td><td>{{.Action}}</td></tr>{{end}}</table>{{end}}
{{else}}<p class="no">No issues.</p>{{end}}

<h2>Fields left out</h2>
{{if .Dropped}}<table><tr><th>Firm</th><th>Field</th><th>Why</th><th class="n">Records</th></tr>
{{range .Dropped}}<tr><td>{{.Firm}}</td><td>{{.Field}}</td><td>{{.Reason}}</td><td class="n">{{int .Count}}</td></tr>{{end}}</table>{{else}}<p class="no">None.</p>{{end}}
<p class="meta">Passwords and full Aadhaar numbers are never migrated.</p>

<h2>Archived documents</h2>
{{if .DocRows}}<table><tr><th>Firm</th><th>Year</th>{{range .DocTypes}}<th class="n">{{.}}</th>{{end}}<th class="n">Total</th></tr>
{{range .DocRows}}<tr><td>{{.Firm}}</td><td>{{.FY}}</td>{{range .Counts}}<td class="n">{{int .}}</td>{{end}}<td class="n"><b>{{int .Total}}</b></td></tr>{{end}}</table>{{else}}<p class="no">No archived documents are in this package.</p>{{end}}

<h2>Files in the package</h2>
<table><tr><th>File</th><th class="n">Rows</th><th class="n">Size</th></tr>
{{range .Files}}<tr><td class="mono">{{.Path}}</td><td class="n">{{int .Rows}}</td><td class="n">{{size .Bytes}}</td></tr>{{end}}</table>

<h2>Steps</h2>
<ol>{{range .Steps}}<li>{{.}}</li>{{end}}</ol>
</body></html>
`))

// Report renders report.html. beside is true for the copy saved next to
// the package, which knows the files' sizes.
func Report(res *Result, o Options, beside bool) []byte {
	m := res.Manifest
	d := reportData{
		Created: o.Now().Format("02 Jan 2006 15:04"),
		Tool:    o.Tool,
		System:  m.Source.System,
		Server:  m.Source.Server,
		Parts:   res.Parts,
		Beside:  beside,
		Backups: m.Source.Backups,
		Firms:   m.Firms,
		Checks:  m.Checks,
		Groups:  GroupIssues(m.Issues),
		Dropped: m.Dropped,
		Steps:   res.Steps,
	}
	if d.Tool == "" {
		d.Tool = "the Ledgerline migration tool"
	}
	years := o.Plan.Years
	if years == 0 {
		years = 8
	}
	d.Plan = "History: " + IndianInt(int64(years)) + " year(s). Archived documents: " + map[bool]string{true: "included", false: "left out"}[o.Plan.Documents] + ". Dormant clients: " + map[bool]string{true: "left out", false: "migrated as inactive"}[o.Plan.DormantMode == "skip"] + "."
	for _, i := range m.Issues {
		switch sevRank(i.Severity) {
		case 0:
			d.Errors++
		case 1:
			d.Warnings++
		}
	}
	types := map[string]bool{}
	for _, c := range res.Docs {
		types[c.Type] = true
	}
	for t := range types {
		d.DocTypes = append(d.DocTypes, t)
	}
	sort.Strings(d.DocTypes)
	rows := map[string]*docRow{}
	var keys []string
	for _, c := range res.Docs {
		k := c.Firm + "\x00" + c.FY
		r := rows[k]
		if r == nil {
			r = &docRow{Firm: c.Firm, FY: c.FY, Counts: make([]int, len(d.DocTypes))}
			rows[k] = r
			keys = append(keys, k)
		}
		for i, t := range d.DocTypes {
			if t == c.Type {
				r.Counts[i] += c.Count
			}
		}
		r.Total += c.Count
	}
	for _, k := range keys {
		d.DocRows = append(d.DocRows, *rows[k])
	}
	for _, f := range m.Files {
		if f.Kind != "document" {
			d.Files = append(d.Files, f)
		}
	}
	var buf bytes.Buffer
	if err := reportTmpl.Execute(&buf, d); err != nil {
		return []byte("<!DOCTYPE html><p>The report could not be written: " + template.HTMLEscapeString(err.Error()))
	}
	return buf.Bytes()
}
