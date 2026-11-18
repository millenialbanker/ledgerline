package ops

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ledgerline/internal/docs"
	"ledgerline/internal/money"
)

// ---- NSE eIPO Query Server 2.0.0 (29 October 2026): an issue's demand ----
//
// The query server answers two calls with Basic authentication, the
// credentials being base64 of {member code}^{login id}:{password} (given by
// NSE separately from the eIPO API login):
//
//   - /mktdata/v3/demand/{symbol}: absolute and cumulative quantity and
//     bid count at each price point (cut-off as its own point; for a debt
//     issue by series, without the cumulative figures);
//   - /mktdata/v3/catwise/{symbol}: quantity and bid count by category and
//     sub-category.
//
// The document's headings say GET and its tables POST; Ledgerline asks
// with GET and, if the server answers 405 (method not allowed), once more
// with POST. The base is Appendix B's (bidcollection.nseindia.com/eipo,
// UAT eipouat.nseindia.com/eipo); the tables' older www.nseindiaipo.com
// address can be entered on the connection.
//
// Demand is read when someone asks, at most every few minutes an issue
// (eipoDemandEvery), and kept so the issue's page shows the last reading.

const connNSEEIPOQuery = "nse-eipo-query"

const (
	eipoQueryLive = "https://bidcollection.nseindia.com/eipo"
	eipoQueryUAT  = "https://eipouat.nseindia.com/eipo"

	eipoDemandEvery = 3 * time.Minute
)

func init() {
	ConnProviders = append(ConnProviders, ConnProvider{ID: connNSEEIPOQuery, Name: "NSE eIPO Query Server (issue demand)", Group: "Exchanges", Test: "https",
		Purpose: "Shows an open issue's demand on NSE by price and by category on the issue. NSE gives the query server's login separately (msm@nse.co.in); fields left empty use the NSE eIPO API connection's",
		Fields: []ConnField{{Key: "member", Label: "Member code (empty: the eIPO API's)", Optional: true}, {Key: "login", Label: "Query login ID (empty: the eIPO API's)", Optional: true},
			{Key: "password", Label: "Query password (empty: the eIPO API's)", Secret: true, Optional: true},
			{Key: "env", Label: "Environment (empty: Live)", Optional: true, Options: []string{"Live", "Mock (UAT)"}},
			{Key: "url", Label: "Address (leave empty for NSE's)", Optional: true, Placeholder: eipoQueryLive}, fNotes}})
}

// EIPOQueryConfig is the query server connection.
type EIPOQueryConfig struct{ Base, Member, Login, Password string }

// EIPODemandPoint is the demand at one price point.
type EIPODemandPoint struct {
	CutOff  bool      `json:"cut_off"`
	Series  string    `json:"series,omitempty"`
	Price   money.Dec `json:"price,omitempty"`
	AbsQty  int64     `json:"qty"`
	CumQty  int64     `json:"cum_qty,omitempty"`
	AbsBids int64     `json:"bids"`
	CumBids int64     `json:"cum_bids,omitempty"`
}

// EIPOCatDemand is the demand in one category and sub-category.
type EIPOCatDemand struct {
	Category    string `json:"category"`
	SubCategory string `json:"sub_category,omitempty"`
	Qty         int64  `json:"qty"`
	Bids        int64  `json:"bids"`
}

// EIPOQueryClient talks to NSE's eIPO query server; replaced in tests.
type EIPOQueryClient interface {
	Demand(ctx context.Context, c EIPOQueryConfig, symbol string) ([]EIPODemandPoint, error)
	CatWise(ctx context.Context, c EIPOQueryConfig, symbol string) ([]EIPOCatDemand, error)
}

var eipoQuery EIPOQueryClient = nseEIPOQueryHTTP{client: &http.Client{Timeout: 30 * time.Second}}

type nseEIPOQueryHTTP struct{ client *http.Client }

func (h nseEIPOQueryHTTP) get(ctx context.Context, c EIPOQueryConfig, path string) ([]eipoRec, error) {
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Member+"^"+c.Login+":"+c.Password))
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req, err := http.NewRequestWithContext(ctx, method, c.Base+path, bytes.NewReader(nil))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Ledgerline")
		req.Header.Set("Authorization", auth)
		resp, err := h.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("could not reach NSE's eIPO query server: %v", err)
		}
		out, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusMethodNotAllowed && method == http.MethodGet:
			continue
		case resp.StatusCode == 401 || resp.StatusCode == 403:
			return nil, fmt.Errorf("NSE's eIPO query server refused the login (%d): check the member code, query login ID and password", resp.StatusCode)
		case resp.StatusCode >= 300:
			return nil, fmt.Errorf("NSE's eIPO query server answered %d: %s", resp.StatusCode, trimBody(out))
		}
		recs, status, reason, err := eipoRecords(out)
		if err != nil {
			return nil, err
		}
		if !strings.EqualFold(status, "success") {
			return nil, fmt.Errorf("NSE's eIPO query server: %s", firstOf(reason, trimBody(out)))
		}
		var rows []eipoRec
		for _, r := range recs {
			if r.s("status") == "" || r.s("absoluteQuantity", "quantity", "category") != "" {
				rows = append(rows, r)
			}
		}
		return rows, nil
	}
	return nil, fmt.Errorf("NSE's eIPO query server took neither GET nor POST")
}

func (h nseEIPOQueryHTTP) Demand(ctx context.Context, c EIPOQueryConfig, symbol string) ([]EIPODemandPoint, error) {
	rows, err := h.get(ctx, c, "/mktdata/v3/demand/"+url.PathEscape(symbol))
	if err != nil {
		return nil, err
	}
	var out []EIPODemandPoint
	for _, r := range rows {
		out = append(out, EIPODemandPoint{CutOff: strings.EqualFold(r.s("cutOffIndicator"), "true"), Series: r.s("series"), Price: r.d("price"),
			AbsQty: r.i("absoluteQuantity"), CumQty: r.i("cumulativeQuantity"), AbsBids: r.i("absoluteBidCount"), CumBids: r.i("cumulativeBidCount")})
	}
	return out, nil
}

func (h nseEIPOQueryHTTP) CatWise(ctx context.Context, c EIPOQueryConfig, symbol string) ([]EIPOCatDemand, error) {
	rows, err := h.get(ctx, c, "/mktdata/v3/catwise/"+url.PathEscape(symbol))
	if err != nil {
		return nil, err
	}
	var out []EIPOCatDemand
	for _, r := range rows {
		out = append(out, EIPOCatDemand{Category: r.s("category"), SubCategory: r.s("subCategory", "subcategory"), Qty: r.i("quantity"), Bids: r.i("bidCount")})
	}
	return out, nil
}

// eipoQueryConn is the query connection, its empty fields taken from the
// eIPO API connection.
func (o *Ops) eipoQueryConn(ctx context.Context, tenant string) (EIPOQueryConfig, error) {
	c, _, err := docs.Get[Connection](ctx, o.Docs, tenant, cConn, connNSEEIPOQuery)
	if err != nil || c.Status == "Not set" {
		return EIPOQueryConfig{}, fmt.Errorf("%w: set up the NSE eIPO Query Server in Settings → Connections", ErrState)
	}
	e, _ := o.eipoConnection(ctx, tenant)
	base := eipoQueryLive
	if c.Values["env"] == "Mock (UAT)" {
		base = eipoQueryUAT
	}
	if u := strings.TrimRight(strings.TrimSpace(c.Values["url"]), "/"); u != "" {
		base = u
	}
	q := EIPOQueryConfig{Base: base, Member: firstOf(strings.TrimSpace(c.Values["member"]), e.cfg.Member), Login: firstOf(strings.TrimSpace(c.Values["login"]), e.cfg.Login),
		Password: firstOf(c.Secrets["password"], e.cfg.Password)}
	if q.Member == "" || q.Login == "" || q.Password == "" {
		return q, fmt.Errorf("%w: the NSE eIPO Query Server needs the member code, login ID and password (or the eIPO API connection's)", ErrState)
	}
	return q, nil
}

// EIPODemand is an issue's demand on NSE as last read.
type EIPODemand struct {
	Issue      string            `json:"issue"`
	Symbol     string            `json:"symbol"`
	At         string            `json:"at,omitempty"`
	ByPrice    []EIPODemandPoint `json:"by_price"`
	ByCategory []EIPOCatDemand   `json:"by_category"`
	TotalQty   int64             `json:"total_qty"`
	TotalBids  int64             `json:"total_bids"`
	// Offered is the issue size where Ledgerline knows it (NCB, SGB, tender).
	Offered int64  `json:"offered,omitempty"`
	Next    string `json:"next_refresh,omitempty"` // a fresh reading is taken from then
	Error   string `json:"error,omitempty"`        // the last reading's problem
}

// IssueDemand returns an open issue's demand on NSE: the last reading, or a
// fresh one when asked and the last is older than a few minutes.
func (o *Ops) IssueDemand(ctx context.Context, tenant, actor, issueID string, refresh bool) (EIPODemand, error) {
	if err := o.require(ctx, tenant, actor, "Viewing an issue's demand", RoleMaker, RoleChecker, RoleRisk, RoleCompliance); err != nil {
		return EIPODemand{}, err
	}
	iss, err := o.IPOIssueByID(ctx, tenant, issueID)
	if err != nil {
		return EIPODemand{}, err
	}
	if !contains(iss.Exchanges, "NSE") || iss.Type == IssueOFS {
		return EIPODemand{}, invalid("NSE's eIPO query server shows demand for issues bidding on NSE's eIPO")
	}
	id := "demand|" + iss.ID
	d, _, _ := docs.GetOr(ctx, o.Docs, tenant, cIPOState, id, EIPODemand{Issue: iss.ID, Symbol: iss.Symbol})
	if d.ByPrice == nil {
		d.ByPrice = []EIPODemandPoint{}
	}
	if d.ByCategory == nil {
		d.ByCategory = []EIPOCatDemand{}
	}
	now := o.now()
	last, _ := time.Parse(time.RFC3339, d.At)
	if !last.IsZero() {
		d.Next = last.Add(eipoDemandEvery).Format(time.RFC3339)
	}
	open := iss.Status == "Open" || iss.Status == "Closed" && now.In(ist).Format("2006-01-02") == iss.Close
	if !open || !(refresh || d.At == "") || !last.IsZero() && now.Sub(last) < eipoDemandEvery {
		if d.At == "" && !open {
			return d, fmt.Errorf("%w: demand is read while the issue is bidding", ErrState)
		}
		return d, nil
	}
	q, err := o.eipoQueryConn(ctx, tenant)
	if err != nil {
		return d, err
	}
	prices, perr := eipoQuery.Demand(ctx, q, iss.Symbol)
	cats, cerr := eipoQuery.CatWise(ctx, q, iss.Symbol)
	if perr != nil && cerr != nil {
		d.Error = perr.Error()
		_ = docs.Upsert(ctx, o.Docs, tenant, cIPOState, id, d, SystemActor)
		return d, perr
	}
	n := EIPODemand{Issue: iss.ID, Symbol: iss.Symbol, At: now.Format(time.RFC3339), ByPrice: prices, ByCategory: cats, Next: now.Add(eipoDemandEvery).Format(time.RFC3339)}
	if n.ByPrice == nil {
		n.ByPrice = []EIPODemandPoint{}
	}
	if n.ByCategory == nil {
		n.ByCategory = []EIPOCatDemand{}
	}
	if perr != nil {
		n.Error, n.ByPrice = "by price: "+perr.Error(), d.ByPrice
	}
	if cerr != nil {
		n.Error, n.ByCategory = "by category: "+cerr.Error(), d.ByCategory
	}
	for _, c := range n.ByCategory {
		n.TotalQty += c.Qty
		n.TotalBids += c.Bids
	}
	if len(n.ByCategory) == 0 {
		for _, p := range n.ByPrice {
			n.TotalQty += p.AbsQty
			n.TotalBids += p.AbsBids
		}
	}
	if iss.Product != nil {
		n.Offered = iss.Product.IssueSize
	}
	if err := docs.Upsert(ctx, o.Docs, tenant, cIPOState, id, n, SystemActor); err != nil {
		return n, err
	}
	o.log(ctx, tenant, actor, "ipo.demand_read", iss.ID, map[string]any{"points": len(n.ByPrice), "categories": len(n.ByCategory)})
	return n, nil
}
