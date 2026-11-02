package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"regexp"
	"strings"

	"ledgerline/internal/docs"
	"ledgerline/internal/ingest"
	"ledgerline/internal/xfile"
)

// ---- SEGMON response / error file from NSE Clearing ----
//
// UNCONFIRMED LAYOUT. None of the circulars Ledgerline has (the public NSE
// and NCL circulars on segregation and monitoring of collateral, and the
// NCL circulars CMPT71536/71537/76432/76433) publishes the layout of the
// response NSE Clearing gives back for an uploaded SEGMON file. The reader
// is therefore lenient:
//
//   - zipped or gzipped as downloaded; comma, pipe, tab or tilde separated;
//   - with a header row: the client code is the column named like "UCC" or
//     "Client Code", the segment "Segment", the indicator "MTF", and the
//     reason every column named like error, reason, remark, description,
//     message or status;
//   - without one: a line of 66 fields or more is taken as the 65 uploaded
//     fields followed by the error (client code in field 9, segment in 8,
//     indicator in 52); a shorter line gives the first field that is one of
//     the firm's client codes and the last field as the reason.
//
// A line whose reason is empty or says success/accepted/valid is accepted.
// Each rejected line opens a work item with the reason in plain words.
// PANs in the reasons are masked. Once the firm has a real response file,
// its layout should replace this guesswork (config/formats).

const cSegmonResp = "segmonresp" // date|file name

// SegmonRejection is one line NSE Clearing did not accept.
type SegmonRejection struct {
	UCC       string `json:"ucc,omitempty"`
	Segment   string `json:"segment,omitempty"`
	Indicator string `json:"indicator,omitempty"`
	Code      string `json:"code,omitempty"`
	Reason    string `json:"reason"`
	Plain     string `json:"plain"`
	Line      int    `json:"line"`
}

// SegmonResponse is a response file as read.
type SegmonResponse struct {
	Date        string            `json:"date"`
	Name        string            `json:"name"`
	File        string            `json:"file,omitempty"` // raw file kept
	Lines       int               `json:"lines"`
	Accepted    int               `json:"accepted"`
	Rejected    int               `json:"rejected"`
	Rejections  []SegmonRejection `json:"rejections"`
	Unconfirmed bool              `json:"unconfirmed"`
	Note        string            `json:"note"`
	At          string            `json:"at"`
	By          string            `json:"by"`
}

const segmonRespNote = "NSE Clearing's SEGMON response layout is not in the published circulars; Ledgerline read this file leniently. Check the reasons against the file, and send a sample response file so the layout can be confirmed."

var (
	reRespOK   = regexp.MustCompile(`(?i)^\s*(success(ful(ly)?)?|accepted|valid|ok|processed|no error|nil|-)?\s*\.?\s*$`)
	reAnyPAN   = regexp.MustCompile(`[A-Z]{5}[0-9]{4}[A-Z]`)
	reCodeOnly = regexp.MustCompile(`^[A-Z]{0,4}\d{2,6}$`)
)

// maskPANs hides the middle of every PAN in s (one not run into other
// letters or digits; "_" and punctuation around it are fine).
func maskPANs(s string) string {
	alnum := func(i int) bool {
		if i < 0 || i >= len(s) {
			return false
		}
		c := s[i]
		return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
	}
	b := []byte(s)
	for _, m := range reAnyPAN.FindAllStringIndex(s, -1) {
		if !alnum(m[0]-1) && !alnum(m[1]) {
			copy(b[m[0]+5:m[0]+9], "XXXX")
		}
	}
	return string(b)
}

// segmonPlain says a rejection in plain words.
func segmonPlain(r SegmonRejection, date string) string {
	who := "the file"
	if r.UCC != "" {
		who = "client " + r.UCC
		if r.Segment != "" {
			who += " in " + r.Segment
		}
		if r.Indicator != "" && r.Indicator != "NA" {
			who += " (" + r.Indicator + ")"
		}
	}
	why := strings.TrimSpace(r.Reason)
	if why == "" || reCodeOnly.MatchString(why) {
		code := firstOf(r.Code, why)
		why = "error code " + code + " (NSE Clearing's code list is not published; look the code up in NCL's response guide on NMASS)"
	}
	return fmt.Sprintf("NSE Clearing did not accept the SEGMON report for %s on %s: %s. Correct it and upload a revised file with the next sequence number.", who, fmtDay(date), strings.TrimSuffix(why, "."))
}

// readSegmonResponse reads one response file's lines.
func readSegmonResponse(data []byte, known map[string]bool) (lines int, rej []SegmonRejection, accepted int) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	first, _, _ := bytes.Cut(data, []byte("\n"))
	sep, best := ',', 0
	for _, c := range []rune{',', '|', '\t', '~'} {
		if n := strings.Count(string(first), string(c)); n > best {
			sep, best = c, n
		}
	}
	cr := csv.NewReader(bytes.NewReader(data))
	cr.Comma, cr.FieldsPerRecord, cr.LazyQuotes, cr.TrimLeadingSpace = sep, -1, true, true
	recs, _ := cr.ReadAll()
	if len(recs) == 0 {
		return 0, nil, 0
	}
	type cols struct{ ucc, seg, ind, code int }
	h := cols{-1, -1, -1, -1}
	var why []int
	header := false
	for i, f := range recs[0] {
		l := strings.ToLower(strings.TrimSpace(f))
		switch {
		case l == "ucc" || l == "ucc code" || strings.Contains(l, "client code") || l == "client id" || l == "clientcode" || l == "cli code":
			h.ucc, header = i, true
		case strings.Contains(l, "segment"):
			h.seg, header = i, true
		case strings.Contains(l, "mtf"):
			h.ind, header = i, true
		case strings.Contains(l, "error code") || l == "code" || l == "errorcode" || l == "reason code":
			h.code, header = i, true
			why = append(why, i)
		case strings.Contains(l, "error") || strings.Contains(l, "reason") || strings.Contains(l, "remark") || strings.Contains(l, "description") || strings.Contains(l, "message") || l == "status":
			why, header = append(why, i), true
		}
	}
	get := func(r []string, i int) string {
		if i >= 0 && i < len(r) {
			return strings.TrimSpace(r[i])
		}
		return ""
	}
	start := 0
	if header {
		start = 1
	}
	for n, r := range recs[start:] {
		blank := true
		for _, f := range r {
			blank = blank && strings.TrimSpace(f) == ""
		}
		if blank {
			continue
		}
		lines++
		x := SegmonRejection{Line: n + start + 1}
		var reasons []string
		switch {
		case header:
			x.UCC, x.Segment, x.Indicator, x.Code = get(r, h.ucc), get(r, h.seg), get(r, h.ind), get(r, h.code)
			for _, i := range why {
				if v := get(r, i); v != "" && !reRespOK.MatchString(v) && i != h.code {
					reasons = append(reasons, v)
				}
			}
			if x.Code != "" && reRespOK.MatchString(x.Code) {
				x.Code = ""
			}
		case len(r) >= 66:
			x.UCC, x.Segment, x.Indicator = get(r, 8), get(r, 7), get(r, 51)
			for _, v := range r[65:] {
				if v = strings.TrimSpace(v); v != "" && !reRespOK.MatchString(v) {
					reasons = append(reasons, v)
				}
			}
		default:
			for _, f := range r {
				if known[strings.ToUpper(strings.TrimSpace(f))] {
					x.UCC = strings.ToUpper(strings.TrimSpace(f))
					break
				}
			}
			if v := get(r, len(r)-1); v != "" && !reRespOK.MatchString(v) && !known[strings.ToUpper(v)] {
				reasons = append(reasons, v)
			}
		}
		x.Reason = maskPANs(strings.Join(reasons, "; "))
		if x.Reason == "" && x.Code == "" {
			accepted++
			continue
		}
		rej = append(rej, x)
	}
	return lines, rej, accepted
}

// SEGMONResponseFile reads NSE Clearing's response to an uploaded SEGMON
// file and opens a work item for each line it did not accept.
func (o *Ops) SEGMONResponseFile(ctx context.Context, tenant, actor, date, name string, data []byte) (SegmonResponse, error) {
	if err := o.require(ctx, tenant, actor, "Loading exchange files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return SegmonResponse{}, err
	}
	if len(data) == 0 || len(data) > MaxXFileBytes {
		return SegmonResponse{}, invalid("files must be between 1 byte and 100 MB")
	}
	if date == "" {
		date = ymdFromName(name)
	}
	if !validDate(date) {
		return SegmonResponse{}, invalid("give the date of the SEGMON file this response is for")
	}
	files, err := xfile.Unpack(name, data)
	if err != nil {
		return SegmonResponse{}, invalid("%v", err)
	}
	cs, _ := o.ClientMap(ctx, tenant)
	known := map[string]bool{}
	for u := range cs {
		known[u] = true
	}
	res := SegmonResponse{Date: date, Name: name, Rejections: []SegmonRejection{}, Unconfirmed: true, Note: segmonRespNote, At: o.now().Format("2006-01-02 15:04"), By: actor}
	for _, f := range files {
		n, rej, ok := readSegmonResponse(f.Data, known)
		res.Lines += n
		res.Accepted += ok
		res.Rejections = append(res.Rejections, rej...)
	}
	res.Rejected = len(res.Rejections)
	if rf, err := o.App.Ingest.Raw.Put(ctx, ingest.RawFile{Tenant: tenant, Source: "SEGMON-RESPONSE", Name: name, SHA256: sha256Hex(data), Size: int64(len(data)), Content: data, ReceivedAt: o.now()}); err == nil {
		res.File = rf.ID
	}
	for i := range res.Rejections {
		r := &res.Rejections[i]
		r.Plain = segmonPlain(*r, date)
		key := date + "|" + firstOf(r.UCC, fmt.Sprintf("line%d", r.Line)) + "|" + r.Segment + "|" + r.Indicator
		title := "SEGMON not accepted: " + firstOf(r.UCC, "file") + " " + r.Segment
		o.raiseWork(ctx, tenant, "segmon", key, strings.TrimSpace(title), r.Plain, r.UCC, RoleCompliance, "xfiles", date)
	}
	if len(res.Rejections) > 500 {
		res.Rejections = res.Rejections[:500]
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cSegmonResp, date+"|"+name, res, actor); err != nil {
		return res, err
	}
	o.log(ctx, tenant, actor, "segmon.response", name, map[string]any{"date": date, "lines": res.Lines, "rejected": res.Rejected})
	return res, nil
}

// SEGMONResponses lists the response files read for a date ("" for all).
func (o *Ops) SEGMONResponses(ctx context.Context, tenant, actor, date string) ([]SegmonResponse, error) {
	if err := o.require(ctx, tenant, actor, "Downloading exchange files", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return nil, err
	}
	p := ""
	if date != "" {
		p = date + "|"
	}
	rs, err := docs.List[SegmonResponse](ctx, o.Docs, tenant, cSegmonResp, p)
	if rs == nil {
		rs = []SegmonResponse{}
	}
	return rs, err
}
