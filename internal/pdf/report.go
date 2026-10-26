package pdf

import (
	"fmt"
	"strings"
)

// Report is a statement laid out as tables: the firm's header, the
// client, a summary, then sections. Long tables run onto further pages,
// repeating their column headings.
type Report struct {
	Landscape bool
	Firm      []string // first line in bold: firm name, then registration lines
	Title     string
	Meta      []string // right of the title: number, date, period
	Party     []string // the client block
	Banner    string   // a highlighted line above the title (sample, revised)
	Summary   [][2]string
	Sections  []Table
	Notes     []string
	Footer    string   // small print on every page
	Stamp     []string // signature block printed at the end
}

// Table is one section.
type Table struct {
	Title  string
	Cols   []Col
	Rows   [][]string
	Totals []string
	Empty  string
	Note   string
}

// Col is a column. Weight sets its share of the width; Right aligns numbers.
type Col struct {
	Label  string
	Right  bool
	Weight float64
}

const (
	margin = 36.0
	body   = 8.0
)

type layout struct {
	d    *Doc
	r    Report
	y    float64
	page int
}

// Render draws the report.
func Render(r Report) *Doc {
	d := New(r.Landscape)
	l := &layout{d: d, r: r}
	l.newPage(true)
	if r.Banner != "" {
		lines := Wrap(r.Banner, 8.5, true, d.W-2*margin-16)
		h := float64(len(lines))*11 + 8
		d.Fill(margin, l.y, d.W-2*margin, h, 0.9)
		for i, s := range lines {
			d.Text(margin+8, l.y+12+float64(i)*11, 8.5, true, s)
		}
		l.y += h + 8
	}
	// Title and meta
	d.Text(margin, l.y+12, 12, true, r.Title)
	for i, m := range r.Meta {
		d.TextRight(d.W-margin, l.y+10+float64(i)*10, 8, i == 0, m)
	}
	l.y += 14 + 10*float64(max(0, len(r.Meta)-1))
	d.Line(margin, l.y+4, d.W-margin, l.y+4, 1, 0)
	l.y += 14
	for i, p := range r.Party {
		d.Text(margin, l.y+float64(i)*10, 8.5, i == 0, p)
	}
	l.y += 10*float64(len(r.Party)) + 6
	if len(r.Summary) > 0 {
		cols := 4
		cw := (d.W - 2*margin) / float64(cols)
		rows := (len(r.Summary) + cols - 1) / cols
		d.Fill(margin, l.y, d.W-2*margin, float64(rows)*24+6, 0.95)
		for i, kv := range r.Summary {
			x := margin + 8 + float64(i%cols)*cw
			y := l.y + 12 + float64(i/cols)*24
			d.TextGray(x, y, 7, false, 0.35, kv[0])
			d.Text(x, y+11, 9.5, true, kv[1])
		}
		l.y += float64(rows)*24 + 16
	}
	for _, t := range r.Sections {
		l.table(t)
	}
	for _, n := range r.Notes {
		for _, s := range Wrap(n, 7, false, d.W-2*margin) {
			l.need(10)
			d.TextGray(margin, l.y, 7, false, 0.3, s)
			l.y += 9
		}
		l.y += 3
	}
	if len(r.Stamp) > 0 {
		l.need(float64(len(r.Stamp))*10 + 16)
		l.y += 6
		h := float64(len(r.Stamp))*10 + 8
		d.Rect(d.W-margin-250, l.y, 250, h, 0.6, 0.4)
		for i, s := range r.Stamp {
			d.Text(d.W-margin-244, l.y+11+float64(i)*10, 7.5, i == 0, Fit(s, 7.5, i == 0, 238))
		}
		l.y += h
	}
	// Page numbers
	n := d.Pages()
	for i := 0; i < n; i++ {
		d.SetPage(i)
		d.TextRight(d.W-margin, d.H-20, 7, false, fmt.Sprintf("Page %d of %d", i+1, n))
	}
	return d
}

func (l *layout) newPage(first bool) {
	d := l.d
	d.AddPage()
	l.page++
	l.y = margin
	if len(l.r.Firm) > 0 {
		d.Text(margin, l.y+10, 11, true, l.r.Firm[0])
		y := l.y + 22
		for _, f := range l.r.Firm[1:] {
			if !first {
				break
			}
			d.TextGray(margin, y, 7, false, 0.3, f)
			y += 9
		}
		if !first {
			d.TextRight(d.W-margin, l.y+10, 8, false, l.r.Title+" (continued)")
		}
		l.y = y + 4
	}
	if l.r.Footer != "" {
		for i, s := range Wrap(l.r.Footer, 6.5, false, d.W-2*margin-80) {
			d.TextGray(margin, d.H-26+float64(i)*8, 6.5, false, 0.4, s)
		}
	}
}

// need moves to a new page when h more points do not fit.
func (l *layout) need(h float64) bool {
	if l.y+h > l.d.H-margin-14 {
		l.newPage(false)
		return true
	}
	return false
}

func (l *layout) table(t Table) {
	d := l.d
	width := d.W - 2*margin
	total := 0.0
	for _, c := range t.Cols {
		w := c.Weight
		if w == 0 {
			w = 1
		}
		total += w
	}
	xs := make([]float64, len(t.Cols)+1)
	xs[0] = margin
	for i, c := range t.Cols {
		w := c.Weight
		if w == 0 {
			w = 1
		}
		xs[i+1] = xs[i] + width*w/total
	}
	head := func() {
		d.Fill(margin, l.y, width, 13, 0.9)
		for i, c := range t.Cols {
			if c.Right {
				d.TextRight(xs[i+1]-3, l.y+9.2, 6.8, true, Fit(strings.ToUpper(c.Label), 6.8, true, xs[i+1]-xs[i]-6))
			} else {
				d.Text(xs[i]+3, l.y+9.2, 6.8, true, Fit(strings.ToUpper(c.Label), 6.8, true, xs[i+1]-xs[i]-6))
			}
		}
		l.y += 13
	}
	l.need(40)
	if t.Title != "" {
		d.Text(margin, l.y+10, 9.5, true, t.Title)
		l.y += 16
	}
	if len(t.Cols) > 0 {
		head()
	}
	row := func(cells []string, bold bool) {
		if l.need(12) && len(t.Cols) > 0 {
			head()
		}
		for i, c := range t.Cols {
			if i >= len(cells) {
				break
			}
			s := Fit(cells[i], body, bold, xs[i+1]-xs[i]-6)
			if c.Right {
				d.TextRight(xs[i+1]-3, l.y+8.5, body, bold, s)
			} else {
				d.Text(xs[i]+3, l.y+8.5, body, bold, s)
			}
		}
		l.y += 11.5
		d.Line(margin, l.y, margin+width, l.y, 0.3, 0.75)
	}
	if len(t.Rows) == 0 && t.Empty != "" {
		l.need(12)
		d.TextGray(margin+3, l.y+8.5, body, false, 0.4, t.Empty)
		l.y += 12
	}
	for _, r := range t.Rows {
		row(r, false)
	}
	if len(t.Totals) > 0 {
		row(t.Totals, true)
		d.Line(margin, l.y, margin+width, l.y, 0.8, 0)
	}
	if t.Note != "" {
		l.y += 3
		for _, s := range Wrap(t.Note, 7, false, width) {
			l.need(10)
			d.TextGray(margin, l.y+7, 7, false, 0.35, s)
			l.y += 9
		}
	}
	l.y += 12
}
