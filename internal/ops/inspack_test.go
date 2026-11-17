package ops

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"ledgerline/internal/pdf"
)

const packCO = "vikram.shah" // the compliance officer in the dev seed

// packSetup is the sample day billed, with contract notes, a clock after
// it, a pledge, a complaint, a duty done and a policy document.
func packSetup(t *testing.T) (*Ops, context.Context) {
	t.Helper()
	o, ctx := setup(t)
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	m, _, err := o.Market(ctx, T, D)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.App.RunBilling(ctx, T, "priya.nair", D, m.SettlementPrices()); err != nil {
		t.Fatal(err)
	}
	if n, err := o.GenerateNotes(ctx, T, "priya.nair", D); err != nil || n == 0 {
		t.Fatalf("notes %d %v", n, err)
	}
	return o, ctx
}

// packKYCDoc puts a made-up PAN card scan on the first client the seed
// samples, and returns that client.
func packKYCDoc(t *testing.T, o *Ops, ctx context.Context, n int, seed int64) string {
	t.Helper()
	s, err := o.PackSamplePreview(ctx, T, packCO, D, PackSampleReq{Mode: "random", N: n, Seed: seed})
	if err != nil {
		t.Fatal(err)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("made-up scan")...)
	if _, err := o.AddClientDoc(ctx, T, "priya.nair", s.Clients[0], "PAN card", "pan.png", png); err != nil {
		t.Fatal(err)
	}
	return s.Clients[0]
}

func packWait(t *testing.T, o *Ops, ctx context.Context, id string) Pack {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		p, err := o.InspectionPack(ctx, T, packCO, id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != PackQueued && p.Status != PackBuilding {
			return p
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pack %s still building", id)
	return Pack{}
}

func unzipPack(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = b
	}
	return out
}

func TestInspectionPackTemplates(t *testing.T) {
	ts := DefaultPackTemplates()
	kinds := map[string]bool{}
	for _, tp := range ts {
		kinds[tp.Kind] = true
		if !strings.HasPrefix(tp.ID, "std-") || !tp.Standard || tp.Name == "" || tp.Authority == "" || len(tp.Sources) == 0 || tp.SampleN == 0 {
			t.Errorf("%s: incomplete %+v", tp.ID, tp)
		}
		if _, err := checkPackItems(tp.Items); err != nil {
			t.Errorf("%s: %v", tp.ID, err)
		}
		for i, it := range tp.Items {
			if it.ID != fmt.Sprintf("I%d", i+1) {
				t.Errorf("%s: item %d numbered %s", tp.ID, i, it.ID)
			}
			if it.Basis == "" {
				t.Errorf("%s %s: no basis", tp.ID, it.Title)
			}
			if it.Section == "not_held" && it.Note == "" {
				t.Errorf("%s %s: a record Ledgerline does not hold needs a note saying where it is", tp.ID, it.Title)
			}
		}
	}
	for _, k := range []string{"exchange", "depository", "sebi", "internal_audit"} {
		if !kinds[k] {
			t.Errorf("no %s template", k)
		}
	}
	// Every SEBI item but the ones Ledgerline itself defines is to confirm.
	for _, it := range ts[2].Items {
		if it.ToConfirm == "" && !strings.HasPrefix(it.Basis, "Ledgerline") && it.Section != "pmla_counts" && it.Section != "documents" {
			t.Errorf("SEBI item %q is not marked to confirm", it.Title)
		}
	}

	o, ctx := setup(t)
	if _, err := o.SavePackTemplate(ctx, T, "priya.nair", PackTemplate{Name: "x", Items: ts[0].Items}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker saved a template: %v", err)
	}
	if _, err := o.SavePackTemplate(ctx, T, packCO, ts[0]); !errors.Is(err, ErrInvalid) {
		t.Fatalf("standard template changed: %v", err)
	}
	if _, err := o.SavePackTemplate(ctx, T, packCO, PackTemplate{Name: "Bad", Items: []PackItem{{Title: "x", Section: "nope"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown section accepted: %v", err)
	}
	mine := ts[0]
	mine.ID, mine.Name = "", "NSE inspection as our auditor asks"
	mine.Items = append(mine.Items[:3:3], PackItem{Title: "Board minutes", Section: "documents", Param: "Board minutes", Period: "current"})
	saved, err := o.SavePackTemplate(ctx, T, packCO, mine)
	if err != nil || saved.ID != "TPL-0001" || len(saved.Items) != 4 || saved.Items[3].ID != "I4" || saved.Standard {
		t.Fatalf("save %v %+v", err, saved)
	}
	saved.Items = saved.Items[1:]
	if saved, err = o.SavePackTemplate(ctx, T, packCO, saved); err != nil || len(saved.Items) != 3 || saved.Items[0].ID != "I1" {
		t.Fatalf("edit %v %+v", err, saved)
	}
	all, _ := o.PackTemplates(ctx, T)
	if len(all) != 5 || all[4].ID != "TPL-0001" {
		t.Fatalf("templates %d", len(all))
	}
	if err := o.DeletePackTemplate(ctx, T, packCO, "std-sebi"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("standard template removed: %v", err)
	}
	if err := o.DeletePackTemplate(ctx, T, packCO, "TPL-0001"); err != nil {
		t.Fatal(err)
	}
	if all, _ = o.PackTemplates(ctx, T); len(all) != 4 {
		t.Fatalf("deleted template still listed")
	}
	if _, err := o.packTemplate(ctx, T, "TPL-0001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted template found: %v", err)
	}
}

func TestInspectionPackSample(t *testing.T) {
	// The algorithm is fixed: this population and seed always give these.
	pop := []string{"A01", "A02", "A03", "A04", "A05", "A06", "A07", "A08", "A09", "A10"}
	got := strings.Join(drawSample(pop, 4, 424242), ",")
	if again := strings.Join(drawSample(pop, 4, 424242), ","); again != got {
		t.Fatalf("same seed, different sample: %s %s", got, again)
	}
	if got != "A02,A06,A09,A10" {
		t.Fatalf("the sample for seed 424242 changed: %s (a seed must draw the same clients on every version)", got)
	}
	if other := strings.Join(drawSample(pop, 4, 7), ","); other == got {
		t.Fatalf("another seed drew the same sample")
	}
	if n := len(drawSample(pop, 50, 1)); n != 10 {
		t.Fatalf("sample larger than the population: %d", n)
	}

	o, ctx := setup(t)
	a, err := o.PackSamplePreview(ctx, T, packCO, D, PackSampleReq{Mode: "random", N: 8})
	if err != nil || a.Seed < 100_000_000_000 || len(a.Clients) != 8 || a.Population < 8 || len(a.PopSHA256) != 64 {
		t.Fatalf("random %v %+v", err, a)
	}
	b, _ := o.PackSamplePreview(ctx, T, packCO, D, PackSampleReq{Mode: "random", N: 8, Seed: a.Seed})
	if strings.Join(b.Clients, ",") != strings.Join(a.Clients, ",") || b.PopSHA256 != a.PopSHA256 {
		t.Fatalf("same seed drew %v then %v", a.Clients, b.Clients)
	}
	l, err := o.PackSamplePreview(ctx, T, packCO, D, PackSampleReq{Mode: "list", Given: []string{a.Clients[1] + ", " + strings.ToLower(a.Clients[0]), a.Clients[0]}})
	if err != nil || strings.Join(l.Clients, ",") != a.Clients[0]+","+a.Clients[1] {
		t.Fatalf("list %v %+v", err, l)
	}
	if _, err := o.PackSamplePreview(ctx, T, packCO, D, PackSampleReq{Mode: "list", Given: []string{"ZZ9999"}}); err == nil || !strings.Contains(err.Error(), "ZZ9999") {
		t.Fatalf("unknown client accepted: %v", err)
	}
	if _, err := o.PackSamplePreview(ctx, T, "priya.nair", D, PackSampleReq{Mode: "random", N: 3}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker drew a sample: %v", err)
	}
}

// TestInspectionPackSections runs every section on the sample day.
func TestInspectionPackSections(t *testing.T) {
	o, ctx := packSetup(t)
	set, _ := o.Settings(ctx, T)
	s, _ := o.packSample(ctx, T, D, PackSampleReq{Mode: "random", N: 5, Seed: 99})
	b := &PackBuild{O: o, Tenant: T, Actor: packCO, From: "2026-10-01", To: D, Clients: s.Clients, Watermark: "Prepared for Test inspection, 20-Oct-2026, by Vikram Shah", Set: set, Today: "2026-10-20"}
	wantRecords := map[string]bool{"client_master": true, "kyc_sample": true, "trades": true, "contract_notes": true, "client_ledgers": true, "margins": true, "segregation": true, "register_securities": true, "nomination": true}
	for _, info := range PackSections() {
		sec, _ := packSection(info.ID)
		it := PackItem{ID: "I1", Title: info.Name, Section: info.ID, Period: "period", Sampled: true}
		part, err := sec.Build(ctx, b, it)
		if err != nil {
			t.Errorf("%s: %v", info.ID, err)
			continue
		}
		if part.NotAvailable != "" {
			if len(part.Files) > 0 {
				t.Errorf("%s: not available but has files", info.ID)
			}
			t.Logf("%s: not available: %s", info.ID, part.NotAvailable)
			continue
		}
		if len(part.Files) == 0 {
			t.Errorf("%s: no files", info.ID)
		}
		if wantRecords[info.ID] && part.Records == 0 {
			t.Errorf("%s: no records on the sample day", info.ID)
		}
		for _, f := range part.Files {
			if strings.HasSuffix(f.Name, ".csv") {
				rows := rowsOfCSV(f.Data)
				if len(rows) < 2 || rows[0][0] != b.Watermark {
					t.Errorf("%s/%s: no watermark row", info.ID, f.Name)
				}
				if !strings.Contains(f.Name, "/") && f.Name != "documents.csv" && f.Records != len(rows)-2 && info.ID != "client_ledgers" {
					t.Errorf("%s/%s: %d records for %d rows", info.ID, f.Name, f.Records, len(rows)-2)
				}
			}
		}
		t.Logf("%s: %d records, %d files · %s", info.ID, part.Records, len(part.Files), part.Note)
	}
}

func TestInspectionPackNotAvailable(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	b := &PackBuild{O: o, Tenant: T, Actor: packCO, From: D, To: D, Set: set, Today: D, Watermark: "w"}
	for _, c := range []struct {
		it   PackItem
		want string
	}{
		{PackItem{Section: "not_held", Note: "Kept by the voice logger"}, "Kept by the voice logger"},
		{PackItem{Section: "self_inspection"}, "self-inspection is not in this version"},
		{PackItem{Section: "documents", Param: "VAPT report"}, "Nothing uploaded of the kind VAPT report"},
		{PackItem{Section: "kyc_sample", Sampled: true}, "No clients were sampled"},
	} {
		sec, _ := packSection(c.it.Section)
		p, err := sec.Build(ctx, b, c.it)
		if err != nil || !strings.Contains(p.NotAvailable, c.want) {
			t.Errorf("%s: %v %q", c.it.Section, err, p.NotAvailable)
		}
		if sec.Ready != nil && !strings.Contains(sec.Ready(ctx, o, T, c.it), c.want) {
			t.Errorf("%s: not flagged before the build", c.it.Section)
		}
	}
	// The self-inspection, once F2 wires it in, is used.
	packSelfInspection = func(ctx context.Context, b *PackBuild, it PackItem) (PackPart, error) {
		return PackPart{Files: []PackFile{b.CSV("self.csv", [][]string{{"Test"}, {"one"}})}, Records: 1}, nil
	}
	defer func() { packSelfInspection = nil }()
	sec, _ := packSection("self_inspection")
	if p, _ := sec.Build(ctx, b, PackItem{Section: "self_inspection"}); p.NotAvailable != "" || p.Records != 1 || sec.Ready(ctx, o, T, PackItem{}) != "" {
		t.Fatalf("wired self-inspection not used: %+v", p)
	}
	// A pack with an item that needs a sample is refused without one.
	if _, err := o.MakePack(ctx, T, packCO, PackRequest{Template: "std-exchange", From: D, To: D}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("sampled items without a sample: %v", err)
	}
	// A section that fails is reported in the index, not the whole pack.
	RegisterPackSection(PackSection{ID: "test_broken", Name: "Broken", Build: func(context.Context, *PackBuild, PackItem) (PackPart, error) { panic("boom") }})
	p, err := o.MakePack(ctx, T, packCO, PackRequest{Title: "Broken", From: D, To: D, Items: []PackItem{{Title: "Breaks", Section: "test_broken"}, {Title: "Nominations", Section: "nomination", Period: "end"}}})
	if err != nil {
		t.Fatal(err)
	}
	p = packWait(t, o, ctx, p.ID)
	if p.Status != PackReady || p.Results[0].Status != "Not available" || !strings.Contains(p.Results[0].Why, "boom") || p.Results[1].Status != "Included" {
		t.Fatalf("broken section: %s %+v", p.Status, p.Results)
	}
}

func TestInspectionPackZip(t *testing.T) {
	o, ctx := packSetup(t)
	if _, err := o.AddPackDoc(ctx, T, packCO, "VAPT report", "VAPT report 2025-26", "2026-06-20", "vapt.pdf", []byte("%PDF-1.4\n% a made-up report\n")); err != nil {
		t.Fatal(err)
	}
	kycClient := packKYCDoc(t, o, ctx, 4, 31337)
	p, err := o.MakePack(ctx, T, packCO, PackRequest{Template: "std-exchange", Title: "NSE inspection", Ref: "NSE/INSP/TEST/1", From: "2026-10-01", To: D, Sample: PackSampleReq{Mode: "random", N: 4, Seed: 31337}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != PackQueued || p.Progress.Total != len(DefaultPackTemplates()[0].Items) || p.Sample.Seed != 31337 || !strings.HasPrefix(p.ExpiresAt, "2026-11-19") {
		t.Fatalf("made %+v", p)
	}
	p = packWait(t, o, ctx, p.ID)
	if p.Status != PackReady || p.Progress.Done != p.Progress.Total {
		t.Fatalf("build %s %s", p.Status, p.Error)
	}
	_, data, err := o.PackFile(ctx, T, packCO, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != p.SHA256 {
		t.Fatal("download does not match the pack's checksum")
	}
	files := unzipPack(t, data)
	for _, f := range []string{"00-cover.html", "00-index.html", "00-index.csv", "SHA256SUMS.txt"} {
		if len(files[f]) == 0 {
			t.Fatalf("no %s in %v", f, len(files))
		}
	}
	if len(files) != p.Files {
		t.Fatalf("%d files in the ZIP, %d recorded", len(files), p.Files)
	}
	// Every file but the cover and the sums is in SHA256SUMS.txt, and matches.
	listed := 0
	for _, l := range strings.Split(string(files["SHA256SUMS.txt"]), "\n") {
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		h, name, _ := strings.Cut(l, "  ")
		sum := sha256.Sum256(files[name])
		if hex.EncodeToString(sum[:]) != h {
			t.Errorf("checksum of %s does not match", name)
		}
		listed++
	}
	if listed != len(files)-2 {
		t.Fatalf("%d checksums for %d files", listed, len(files)-2)
	}
	cover := string(files["00-cover.html"])
	for _, want := range []string{"31337", "NSE inspection", "NSE/INSP/TEST/1", "Vikram Shah", "01-Oct-2026 to 16-Oct-2026", "SHA256SUMS.txt"} {
		if !strings.Contains(cover, want) {
			t.Errorf("cover has no %q", want)
		}
	}
	for name, b := range files {
		if name == "00-cover.html" || name == "SHA256SUMS.txt" {
			continue
		}
		sum := sha256.Sum256(b)
		if !strings.Contains(cover, hex.EncodeToString(sum[:])) {
			t.Errorf("cover has no checksum for %s", name)
		}
	}
	// The index lists every item, with its files and records or why not.
	idx := csv.NewReader(bytes.NewReader(files["00-index.csv"]))
	idx.FieldsPerRecord = -1
	rows, _ := idx.ReadAll()
	if rows[0][0] != p.Watermark || rows[1][0] != "No" {
		t.Fatalf("index head %v", rows[:2])
	}
	seen := map[string]bool{}
	for _, r := range rows[2:] {
		seen[r[0]] = true
		if r[4] == "Included" && r[8] != "" && files[r[8]] == nil {
			t.Errorf("index names %s, not in the ZIP", r[8])
		}
		if r[4] == "Not available" && r[5] == "" {
			t.Errorf("item %s not available without a reason", r[1])
		}
	}
	if len(seen) != len(p.Items) {
		t.Fatalf("index has %d items of %d", len(seen), len(p.Items))
	}
	kyc := 0
	for name := range files {
		if strings.Contains(name, "/documents/"+kycClient+"/") && strings.HasSuffix(name, "PAN card.png") {
			kyc++
		}
	}
	if kyc != 1 {
		for name := range files {
			if strings.Contains(name, "kyc") {
				t.Log(name)
			}
		}
		t.Fatalf("KYC document of %s not copied into the pack", kycClient)
	}
	byTitle := map[string]PackResult{}
	for _, r := range p.Results {
		byTitle[r.Title] = r
	}
	if r := byTitle["Order logs and voice recordings of client orders"]; r.Status != "Not available" || !strings.Contains(r.Why, "voice logger") {
		t.Errorf("not held: %+v", r)
	}
	if r := byTitle["Self-inspection results for the period"]; r.Status != "Not available" {
		t.Errorf("self-inspection: %+v", r)
	}
	if r := byTitle["Cyber security: CSCRF, VAPT and cyber audit reports"]; r.Status != "Included" || r.Records != 1 {
		t.Errorf("VAPT report not in the pack: %+v", r)
	}
	if r := byTitle["Contract notes issued (copies for the sampled clients)"]; r.Records == 0 {
		t.Errorf("no contract notes: %+v", r)
	}
	html := string(files["00-index.html"])
	if !strings.Contains(html, "Not available") || !strings.Contains(html, "To confirm") || !strings.Contains(html, p.Watermark) {
		t.Error("index page lacks statuses or the watermark")
	}
	ix, err := o.PackIndex(ctx, T, packCO, p.ID)
	if err != nil || string(ix) != html {
		t.Fatalf("index view %v", err)
	}
}

// pdfText inflates a PDF's streams.
func pdfText(t *testing.T, b []byte) string {
	t.Helper()
	var out strings.Builder
	for _, m := range regexp.MustCompile(`(?s)stream\n(.*?)\nendstream`).FindAllSubmatch(b, -1) {
		if r, err := zlib.NewReader(bytes.NewReader(m[1])); err == nil {
			x, _ := io.ReadAll(r)
			out.Write(x)
		}
	}
	return out.String()
}

func TestInspectionPackWatermark(t *testing.T) {
	o, ctx := setup(t)
	set, _ := o.Settings(ctx, T)
	w := packWatermark("BSE inspection", "BSE/2026/9", time.Date(2026, 10, 20, 9, 0, 0, 0, ist), "Vikram Shah", false)
	if w != "Prepared for BSE inspection (BSE/2026/9), 20-Oct-2026, by Vikram Shah" {
		t.Fatalf("watermark %q", w)
	}
	b := &PackBuild{O: o, Tenant: T, Set: set, Watermark: w}
	c := b.CSV("x.csv", [][]string{{"A", "B"}, {"1", "2"}})
	if rows := rowsOfCSV(c.Data); rows[0][0] != w || rows[1][0] != "A" || c.Records != 1 {
		t.Fatalf("csv %q", c.Data)
	}
	h := b.HTML("x.html", []byte(`<!doctype html><html><body class="x"><h1>Report</h1></body></html>`), 1)
	if s := string(h.Data); !strings.Contains(s, `<body class="x"><div style="position:fixed`) || !strings.Contains(s, w) {
		t.Fatalf("html %s", s)
	}
	rep := pdf.Report{Title: "Long", Firm: []string{"Firm"}}
	for i := 0; i < 3; i++ {
		tb := pdf.Table{Title: "T", Cols: []pdf.Col{{Label: "A"}}}
		for j := 0; j < 80; j++ {
			tb.Rows = append(tb.Rows, []string{"row"})
		}
		rep.Sections = append(rep.Sections, tb)
	}
	f, err := b.PDF("x.pdf", rep, 240)
	if err != nil {
		t.Fatal(err)
	}
	txt := pdfText(t, f.Data)
	pages := strings.Count(txt, "Page ")
	if pages < 2 || strings.Count(txt, "(Prepared for BSE inspection") != pages {
		t.Fatalf("watermark on %d of %d pages", strings.Count(txt, "(Prepared for BSE inspection"), pages)
	}
}

func TestInspectionPackMasking(t *testing.T) {
	o, ctx := packSetup(t)
	packKYCDoc(t, o, ctx, 4, 5)
	p, err := o.MakePack(ctx, T, packCO, PackRequest{Template: "std-internal-audit", Title: "Internal audit H1", From: "2026-10-01", To: D, Sample: PackSampleReq{Mode: "random", N: 4, Seed: 5}})
	if err != nil {
		t.Fatal(err)
	}
	p = packWait(t, o, ctx, p.ID)
	c, err := o.MaskedPackCopy(ctx, T, packCO, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Mask || c.CopyOf != p.ID || strings.Join(c.Sample.Clients, ",") != strings.Join(p.Sample.Clients, ",") || !strings.Contains(c.Watermark, "masked copy") {
		t.Fatalf("copy %+v", c)
	}
	if _, err := o.MaskedPackCopy(ctx, T, packCO, c.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("masked a masked copy: %v", err)
	}
	c = packWait(t, o, ctx, c.ID)
	if c.Status != PackReady {
		t.Fatalf("copy %s %s", c.Status, c.Error)
	}
	_, data, err := o.PackFile(ctx, T, packCO, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := o.ClientMap(ctx, T)
	files := unzipPack(t, data)
	for name, b := range files {
		s := string(b)
		if strings.HasSuffix(name, ".pdf") {
			s = pdfText(t, b)
		}
		for _, u := range c.Sample.Clients {
			cl := cs[u]
			for _, v := range []string{cl.PAN, cl.Name, cl.Mobile, cl.Email} {
				if v != "" && strings.Contains(s, v) {
					t.Errorf("%s shows %q of a sampled client", name, v)
				}
			}
		}
		if strings.Contains(name, "/documents/") {
			t.Errorf("masked copy has a KYC document: %s", name)
		}
	}
	if !strings.Contains(string(files["00-cover.html"]), "Masked copy of "+p.ID) {
		t.Fatal("cover does not say it is a masked copy")
	}
	src, _ := o.InspectionPack(ctx, T, packCO, p.ID)
	if a := src.Access[len(src.Access)-1]; a.What != "Masked copy made" || a.Detail != c.ID {
		t.Fatalf("original's access list: %+v", src.Access)
	}
}

func TestInspectionPackAccess(t *testing.T) {
	o, ctx := setup(t)
	req := PackRequest{Title: "Test", From: D, To: D, Items: []PackItem{{Title: "Nominations", Section: "nomination", Period: "end"}}}
	for _, who := range []string{"priya.nair", "farhan.khan", "anita.rao", "meera.iyer"} {
		if _, err := o.MakePack(ctx, T, who, req); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s made a pack: %v", who, err)
		}
		if _, err := o.InspectionPacks(ctx, T, who); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s listed packs: %v", who, err)
		}
	}
	p, err := o.MakePack(ctx, T, packCO, req)
	if err != nil {
		t.Fatal(err)
	}
	p = packWait(t, o, ctx, p.ID)
	if _, _, err := o.PackFile(ctx, T, "priya.nair", p.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker downloaded: %v", err)
	}
	if _, err := o.PackIndex(ctx, T, "anita.rao", p.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("risk opened the index: %v", err)
	}
	if _, _, err := o.PackFile(ctx, T, "admin", p.ID); err != nil {
		t.Fatalf("admin download: %v", err)
	}
	if _, _, err := o.PackFile(ctx, T, packCO, p.ID); err != nil {
		t.Fatal(err)
	}
	p, _ = o.InspectionPack(ctx, T, packCO, p.ID)
	var whats []string
	for _, a := range p.Access {
		whats = append(whats, a.By+" "+a.What)
	}
	if strings.Join(whats, "|") != "vikram.shah Made|admin Downloaded|vikram.shah Downloaded" {
		t.Fatalf("access list %v", whats)
	}
	evs, _ := o.App.Audit.List(ctx, T, 200)
	got := map[string]int{}
	for _, e := range evs {
		if e.Object == p.ID {
			got[e.Action+" "+e.Actor]++
		}
	}
	if got["inspack.made vikram.shah"] != 1 || got["inspack.built "+SystemActor] != 1 || got["inspack.downloaded admin"] != 1 || got["inspack.downloaded vikram.shah"] != 1 {
		t.Fatalf("audit trail %v", got)
	}
	if _, err := o.AddPackDoc(ctx, T, "priya.nair", "Policy", "PMLA policy", "", "p.pdf", []byte("%PDF-1.4")); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maker uploaded a document: %v", err)
	}
}

func TestInspectionPackExpiry(t *testing.T) {
	o, ctx := setup(t)
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, ist)
	o.Now = func() time.Time { return now }
	if _, err := o.SavePackSettings(ctx, T, packCO, PackSettings{KeepDays: 0}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("keep 0 days: %v", err)
	}
	if _, err := o.SavePackSettings(ctx, T, packCO, PackSettings{KeepDays: 7}); err != nil {
		t.Fatal(err)
	}
	req := PackRequest{Title: "Test", From: D, To: D, Items: []PackItem{{Title: "Nominations", Section: "nomination", Period: "end"}}}
	p, err := o.MakePack(ctx, T, packCO, req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.ExpiresAt, "2026-10-27") {
		t.Fatalf("expires %s", p.ExpiresAt)
	}
	p = packWait(t, o, ctx, p.ID)
	now = now.AddDate(0, 0, 6)
	if _, _, err := o.PackFile(ctx, T, packCO, p.ID); err != nil {
		t.Fatalf("day 6: %v", err)
	}
	now = now.AddDate(0, 0, 1)
	v, err := o.InspectionPacks(ctx, T, packCO)
	if err != nil || len(v.Packs) != 1 || !v.Packs[0].Expired || v.Packs[0].ExpiredBy != SystemActor {
		t.Fatalf("not expired on day 7: %v %+v", err, v.Packs)
	}
	if _, _, err := o.PackFile(ctx, T, packCO, p.ID); !errors.Is(err, ErrState) {
		t.Fatalf("expired pack downloaded: %v", err)
	}
	// The record, its results and its access list stay.
	p, _ = o.InspectionPack(ctx, T, packCO, p.ID)
	if p.Status != PackReady || len(p.Results) != 1 || p.FileID == "" || p.Access[len(p.Access)-1].What != "Expired" {
		t.Fatalf("record after expiry %+v", p)
	}
	// Taken off by hand.
	q, _ := o.MakePack(ctx, T, packCO, req)
	q = packWait(t, o, ctx, q.ID)
	if q, err = o.ExpirePack(ctx, T, packCO, q.ID, "inspection closed"); err != nil || !q.Expired || q.ExpiredWhy != "inspection closed" {
		t.Fatalf("expire %v %+v", err, q)
	}
	evs, _ := o.App.Audit.List(ctx, T, 200)
	n := 0
	for _, e := range evs {
		if e.Action == "inspack.expired" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d expiry entries in the audit trail", n)
	}
}

func TestInspectionPackDocuments(t *testing.T) {
	o, ctx := setup(t)
	if _, err := o.AddPackDoc(ctx, T, packCO, "Policy", "PMLA policy", "v3", "pmla.exe", []byte("MZ\x90\x00binary")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("program accepted: %v", err)
	}
	d, err := o.AddPackDoc(ctx, T, packCO, "Policy", "PMLA policy", "v3, 2026-04-01", "pmla.pdf", []byte("%PDF-1.4 made up"))
	if err != nil {
		t.Fatal(err)
	}
	if _, b, err := o.PackDocFile(ctx, T, packCO, d.ID); err != nil || !bytes.HasPrefix(b, []byte("%PDF")) {
		t.Fatalf("file %v", err)
	}
	if _, err := o.RetirePackDoc(ctx, T, packCO, d.ID, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("retired without a reason: %v", err)
	}
	if _, err := o.RetirePackDoc(ctx, T, packCO, d.ID, "replaced by v4"); err != nil {
		t.Fatal(err)
	}
	if why := readyDocuments(ctx, o, T, PackItem{Param: "Policy"}); why == "" {
		t.Fatal("a retired document still counts")
	}
}
