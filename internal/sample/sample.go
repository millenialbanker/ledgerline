// Package sample generates a realistic, deterministic trading day as the
// actual files a broker receives: client master, opening balances, market
// data, holdings, carried positions, exchange trade files for NSE/BSE cash,
// NSE F&O and currency and MCX, the OMS trade book with typical breaks, the
// client bank statement, pledge requests, exchange surveillance alerts and
// IPO/MF files. The files go through the same loaders as real ones.
//
// Every name, PAN, account and trade is invented.
package sample

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// File is one generated file.
type File struct {
	Source string // file type, as accepted by the upload API
	Name   string
	Date   string // business date to pass with the upload, when needed
	Data   []byte
	Later  bool // not loaded with the day (e.g. allotment results)
}

// Day is the generated set, in load order.
type Day struct {
	Date  string
	Files []File
}

type rng struct{ s uint32 }

func (r *rng) f() float64 {
	r.s += 0x6D2B79F5
	t := r.s
	t = (t ^ t>>15) * (1 | t)
	t = (t + (t^t>>7)*(61|t)) ^ t
	return float64(t^t>>14) / 4294967296
}
func (r *rng) in(a, b int) int         { return a + int(r.f()*float64(b-a+1)) }
func (r *rng) fl(a, b float64) float64 { return a + r.f()*(b-a) }
func pick[T any](r *rng, xs []T) T     { return xs[int(r.f()*float64(len(xs)))] }

type eq struct {
	sym, isin, co     string
	px, varp, haircut float64
}

var equities = []eq{
	{"RELIANCE", "INE002A01018", "Reliance Industries", 1412, 14, 12.5}, {"HDFCBANK", "INE040A01034", "HDFC Bank", 1001, 12, 10.5},
	{"ICICIBANK", "INE090A01021", "ICICI Bank", 1385, 12, 10.5}, {"INFY", "INE009A01021", "Infosys", 1512, 13, 11},
	{"TCS", "INE467B01029", "Tata Consultancy Services", 3080, 12, 11}, {"SBIN", "INE062A01020", "State Bank of India", 868, 14, 12},
	{"LT", "INE018A01030", "Larsen & Toubro", 3750, 13, 11.5}, {"ITC", "INE154A01025", "ITC", 412, 12, 10.5},
	{"BHARTIARTL", "INE397D01024", "Bharti Airtel", 1960, 13, 11}, {"AXISBANK", "INE238A01034", "Axis Bank", 1180, 14, 12},
	{"KOTAKBANK", "INE237A01028", "Kotak Mahindra Bank", 2150, 13, 11.5}, {"BAJFINANCE", "INE296A01032", "Bajaj Finance", 1005, 16, 13.5},
	{"MARUTI", "INE585B01010", "Maruti Suzuki", 16200, 13, 11.5}, {"SUNPHARMA", "INE044A01036", "Sun Pharmaceutical", 1690, 13, 11.5},
}

type der struct {
	exch, seg, sym, typ, exp string
	strike                   float64
	lot                      int
	px, span, expo, under    float64
	delta                    float64
	index                    bool
}

var derivs = []der{
	{"NSE", "FO", "NIFTY", "FUT", "2026-10-27", 0, 65, 25840, 9, 2, 25840, 1, true},
	{"NSE", "FO", "NIFTY", "CE", "2026-10-20", 25900, 65, 112, 9, 2, 25840, .43, true},
	{"NSE", "FO", "NIFTY", "PE", "2026-10-20", 25700, 65, 86, 9, 2, 25840, -.34, true},
	{"NSE", "FO", "BANKNIFTY", "FUT", "2026-10-27", 0, 30, 57620, 9, 2, 57620, 1, true},
	{"NSE", "FO", "BANKNIFTY", "CE", "2026-10-27", 58000, 30, 410, 9, 2, 57620, .41, true},
	{"NSE", "FO", "RELIANCE", "FUT", "2026-10-27", 0, 500, 1418, 15, 3.5, 1418, 1, false},
	{"NSE", "FO", "SBIN", "FUT", "2026-10-27", 0, 750, 871, 15, 3.5, 871, 1, false},
	{"NSE", "FO", "INFY", "CE", "2026-10-27", 1540, 400, 24, 15, 3.5, 1512, .38, false},
	{"NSE", "CD", "USDINR", "FUT", "2026-10-28", 0, 1000, 88.42, 2.5, 1, 88.42, 1, false},
	{"MCX", "COM", "CRUDEOIL", "FUT", "2026-11-19", 0, 100, 5480, 18, 2, 5480, 1, false},
	{"MCX", "COM", "GOLDM", "FUT", "2026-12-03", 0, 10, 121500, 9, 1, 121500, 1, false},
	{"MCX", "COM", "SILVERMIC", "FUT", "2026-11-30", 0, 1, 148000, 11, 1, 148000, 1, false},
	{"MCX", "COM", "NATURALGAS", "FUT", "2026-11-24", 0, 1250, 312, 25, 2, 312, 1, false},
}

var (
	firstNames = []string{"Aarav", "Vivaan", "Ananya", "Ishaan", "Diya", "Kabir", "Meera", "Rohan", "Saanvi", "Arjun", "Kavya", "Nikhil", "Pooja", "Rahul", "Sneha", "Vikram", "Aditi", "Siddharth", "Neha", "Karan", "Priya", "Manish", "Ritu", "Amit", "Sunita", "Deepak", "Farhan", "Zoya", "Harpreet", "Lakshmi", "Venkat", "Suresh", "Gautam", "Tanvi", "Yash", "Nandini", "Rekha", "Imran"}
	lastNames  = []string{"Shah", "Mehta", "Iyer", "Reddy", "Nair", "Gupta", "Agarwal", "Banerjee", "Chatterjee", "Kulkarni", "Patil", "Rao", "Sharma", "Verma", "Singh", "Kapoor", "Joshi", "Desai", "Menon", "Bose", "Khan", "Das", "Pillai", "Jain", "Malhotra", "Sen"}
	cities     = []string{"Mumbai", "Pune", "Ahmedabad", "Kolkata", "Bengaluru", "Chennai", "Hyderabad", "Jaipur", "Indore", "Surat", "Delhi", "Lucknow", "Kochi", "Nagpur"}
	banks      = [][2]string{{"HDFC Bank", "HDFC"}, {"ICICI Bank", "ICIC"}, {"State Bank of India", "SBIN"}, {"Axis Bank", "UTIB"}, {"Kotak Mahindra Bank", "KKBK"}, {"Bank of Baroda", "BARB"}}
	corps      = []string{"Brightwater Capital LLP", "Sahyadri Agro Exports Pvt Ltd", "Northline Logistics Pvt Ltd"}
	incomes    = []string{"1-5 L", "5-10 L", "10-25 L", "25 L-1 Cr", ">1 Cr", "<1 L"}
	stages     = []string{"Application", "KYC verification", "In-person verification", "UCC upload"}
)

type client struct {
	ucc, name, typ, pan, mobile, email, city, status, risk, income, ras string
	segs                                                                []string
	bank, ifsc, acct, dp, dematID                                       string
	nominee, kyc, lastTrade                                             string
	api, trades                                                         bool
	upi, open                                                           int64
}

type trade struct {
	exch, seg, ucc, sym, typ, exp string
	strike                        float64
	side                          string
	qty                           int64
	price                         float64
	tm, product, algo, term       string
	no, order                     string
}

func pan(r *rng, typ, last string) string {
	A := "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	s := ""
	for i := 0; i < 3; i++ {
		s += string(A[r.in(0, 25)])
	}
	switch typ {
	case "HUF":
		s += "H"
	case "Corporate":
		s += "C"
	default:
		s += "P"
	}
	s += strings.ToUpper(last[:1])
	s += fmt.Sprintf("%04d", r.in(1000, 9999))
	return s + string(A[r.in(0, 25)])
}

func tick(x float64) float64 { return math.Round(x*20) / 20 }

func fmtExp(d, layout string) string {
	t, _ := time.Parse("2006-01-02", d)
	return strings.ToUpper(t.Format(layout))
}

// Generate builds the sample day for a trade date (YYYY-MM-DD).
func Generate(date string) Day {
	r := &rng{s: 20261016}
	td, _ := time.Parse("2006-01-02", date)
	prev := td.AddDate(0, 0, -1)
	for prev.Weekday() == time.Saturday || prev.Weekday() == time.Sunday {
		prev = prev.AddDate(0, 0, -1)
	}
	prevDate := prev.Format("2006-01-02")
	day := Day{Date: date}

	// ---- market data ----
	ltp := map[string]float64{}
	var mkt [][]string
	mkt = append(mkt, []string{"Exchange", "Segment", "Symbol", "Type", "Expiry", "Strike", "ISIN", "Company", "Lot", "LTP", "Close", "VaRPct", "ELMPct", "HaircutPct", "SPANPct", "ExposurePct", "Delta", "Underlying", "Index"})
	for i, e := range equities {
		p := tick(e.px * (1 + (r.f()-.45)*.03))
		ltp[e.sym] = p
		mkt = append(mkt, []string{"NSE", "CM", e.sym, "EQ", "", "", e.isin, e.co, "1", f2(p), f2(p), f1(e.varp - 3.5), "3.5", f1(e.haircut), "", "", "", "", "N"})
		if i < 9 {
			mkt = append(mkt, []string{"BSE", "CM", e.sym, "EQ", "", "", e.isin, e.co, "1", f2(p), f2(p), f1(e.varp - 3.5), "3.5", f1(e.haircut), "", "", "", "", "N"})
		}
	}
	dltp := map[int]float64{}
	for i, d := range derivs {
		drift := (r.f() - .45) * .016
		if d.typ != "FUT" {
			drift = (r.f() - .45) * .22
		}
		p := d.px * (1 + drift)
		if d.typ != "FUT" {
			p = math.Max(.5, tick(p))
		} else if d.px < 200 {
			p = math.Round(p*10000) / 10000
		} else {
			p = tick(p)
		}
		dltp[i] = p
		strike := ""
		if d.strike > 0 {
			strike = fmt.Sprint(d.strike)
		}
		idx := "N"
		if d.index {
			idx = "Y"
		}
		mkt = append(mkt, []string{d.exch, d.seg, d.sym, d.typ, d.exp, strike, "", "", fmt.Sprint(d.lot), fpx(p), fpx(p), "", "", "", f1(d.span), f1(d.expo), fmt.Sprint(d.delta), fpx(d.under), idx})
	}

	// ---- clients ----
	var cs []client
	used := map[string]bool{}
	oldDates := []string{"2026-08-28", "2026-09-04", "2026-09-09", "2026-09-22", "2026-09-30", "2026-10-07", "2026-10-13", "2026-10-15"}
	for i := 0; i < 64; i++ {
		typ := "Individual"
		if i%17 == 5 {
			typ = "HUF"
		} else if i%23 == 9 {
			typ = "Corporate"
		}
		var fn, ln, name string
		for {
			fn, ln = pick(r, firstNames), pick(r, lastNames)
			name = fn + " " + ln
			if !used[name] {
				break
			}
		}
		used[name] = true
		if typ == "HUF" {
			name = ln + " " + fn + " HUF"
		}
		if typ == "Corporate" {
			n := 0
			for _, c := range cs {
				if c.typ == "Corporate" {
					n++
				}
			}
			name = corps[n%len(corps)]
		}
		active := i < 56
		st := "Active"
		if !active {
			st = stages[(i-56)%4]
		}
		segs := []string{"CM"}
		if r.f() < .58 {
			segs = append(segs, "FO")
		}
		if r.f() < .16 {
			segs = append(segs, "CD")
		}
		if r.f() < .26 {
			segs = append(segs, "COM")
		}
		b := pick(r, banks)
		email := strings.ToLower(fn+"."+ln) + "@mail.example"
		c := client{ucc: fmt.Sprintf("BH%d", 1001+i), name: name, typ: typ, pan: pan(r, typ, ln), mobile: fmt.Sprintf("98%08d", r.in(10000000, 99999999)),
			email: email, city: pick(r, cities), status: st, segs: segs, risk: pick(r, []string{"Low", "Low", "Medium", "Medium", "High"}),
			income: pick(r, incomes), bank: b[0], ifsc: fmt.Sprintf("%s0%06d", b[1], r.in(1000, 999999)), acct: fmt.Sprintf("%d%08d", r.in(5010, 9199), r.in(10000000, 99999999)),
			nominee: "Opted in", kyc: pick(r, []string{"2021-03-12", "2022-07-04", "2023-01-19", "2023-09-27", "2024-05-08", "2025-02-14", "2025-07-22", "2026-03-03"})}
		c.ras = "Quarterly"
		if r.f() < .38 {
			c.ras = "Monthly"
		}
		if r.f() < .14 {
			c.nominee = "Opted out"
		}
		if r.f() < .62 {
			c.dp, c.dematID = "CDSL", fmt.Sprintf("12081600%08d", r.in(10000000, 99999999))
		} else {
			c.dp, c.dematID = "NSDL", fmt.Sprintf("IN30%d%06d", r.in(10000, 99999), r.in(100000, 999999))
		}
		c.trades = active && r.f() < .7
		if active {
			c.api = r.f() < .12
			if r.f() < .15 {
				c.upi = int64(r.in(2, 40)) * 5000
			}
			if c.trades {
				c.lastTrade = prevDate
			} else {
				c.lastTrade = pick(r, oldDates)
			}
			if r.f() < .1 {
				c.open = -int64(r.in(5000, 60000)) * 100
			} else if len(segs) > 1 {
				c.open = (int64(r.in(4, 30))*100000 + int64(r.in(0, 99999))) * 100
			} else {
				c.open = (int64(r.in(1, 10))*100000 + int64(r.in(0, 99999))) * 100
			}
		}
		cs = append(cs, c)
	}
	// Two trading clients have undeliverable email addresses, so contract
	// note delivery shows bounces.
	tradersSeen := 0
	for i := range cs {
		if cs[i].trades {
			tradersSeen++
			if tradersSeen == 4 || tradersSeen == 18 {
				cs[i].email = strings.Split(cs[i].email, "@")[0] + "@nomail.invalid"
			}
		}
	}
	byUCC := map[string]*client{}
	for i := range cs {
		byUCC[cs[i].ucc] = &cs[i]
	}
	cm := [][]string{{"UCC", "Name", "Type", "PAN", "DOB", "Mobile", "Email", "City", "Segments", "Risk", "Income", "RAS", "Bank", "IFSC", "Account", "Depository", "DematID", "Nominee", "NomineeName", "KYCDate", "Status", "API", "LastTrade", "UPIBlock"}}
	for _, c := range cs {
		nn := ""
		if c.nominee == "Opted in" {
			nn = "Spouse of " + strings.Fields(c.name)[0]
		}
		api := "N"
		if c.api {
			api = "Y"
		}
		upi := ""
		if c.upi > 0 {
			upi = fmt.Sprint(c.upi)
		}
		cm = append(cm, []string{c.ucc, c.name, c.typ, c.pan, "", c.mobile, c.email, c.city, strings.Join(c.segs, ";"), c.risk, c.income, c.ras, c.bank, c.ifsc, c.acct, c.dp, c.dematID, c.nominee, nn, c.kyc, c.status, api, c.lastTrade, upi})
	}
	day.Files = append(day.Files, File{"CLIENT-MASTER", "client_master.csv", "", csvBytes(cm), false})

	// ---- opening balances ----
	var credits, debits int64
	ob := [][]string{{"Account", "Debit", "Credit"}}
	for _, c := range cs {
		switch {
		case c.open > 0:
			ob = append(ob, []string{"CLIENT:" + c.ucc, "", paise(c.open)})
			credits += c.open
		case c.open < 0:
			ob = append(ob, []string{"CLIENT:" + c.ucc, paise(-c.open), ""})
			debits += -c.open
		}
	}
	bank := credits * 36 / 100
	cc := credits * 72 / 100
	ob = append(ob, []string{"BANK:CLIENT", paise(bank), ""}, []string{"CC:COLLATERAL", paise(cc), ""})
	diff := debits + bank + cc - credits
	if diff > 0 {
		ob = append(ob, []string{"EQUITY:OPENING", "", paise(diff)})
	} else if diff < 0 {
		ob = append(ob, []string{"EQUITY:OPENING", paise(-diff), ""})
	}
	day.Files = append(day.Files, File{"OPENING-BALANCES", "opening_balances.csv", prevDate, csvBytes(ob), false})
	day.Files = append(day.Files, File{"MARKET-DATA", "market_data_" + date + ".csv", date, csvBytes(mkt), false})

	// ---- holdings ----
	hold := map[string]map[string]int64{}
	hd := [][]string{{"UCC", "ISIN", "Symbol", "Qty", "Pledged", "AvgCost"}}
	var pledgeCands [][]string
	for _, c := range cs {
		if c.status != "Active" {
			continue
		}
		n := r.in(2, 6)
		pool := append([]eq(nil), equities...)
		hold[c.ucc] = map[string]int64{}
		for k := 0; k < n; k++ {
			j := r.in(0, len(pool)-1)
			e := pool[j]
			pool = append(pool[:j], pool[j+1:]...)
			q := int64(math.Max(1, math.Round(float64(r.in(20000, 420000))/e.px)))
			pl := int64(0)
			if r.f() < .55 {
				pl = int64(math.Round(float64(q) * r.fl(.3, 1)))
			}
			hold[c.ucc][e.sym] = q
			hd = append(hd, []string{c.ucc, e.isin, e.sym, fmt.Sprint(q), fmt.Sprint(pl), f2(tick(e.px * r.fl(.7, 1.12)))})
			if pl < q {
				pledgeCands = append(pledgeCands, []string{c.ucc, e.isin, e.sym, fmt.Sprint(q - pl)})
			}
		}
	}
	day.Files = append(day.Files, File{"DP-HOLDINGS", "dp_holdings_" + date + ".csv", "", csvBytes(hd), false})
	day.Files = append(day.Files, File{"HOLDING-LOTS", "holding_lots_" + prevDate + ".csv", prevDate, csvBytes(holdingLots(hd[1:], prevDate)), false})

	// ---- carried positions ----
	ps := [][]string{{"UCC", "Exchange", "Segment", "Symbol", "Type", "Expiry", "Strike", "Qty", "AvgPrice"}}
	for _, c := range cs {
		if c.status != "Active" {
			continue
		}
		has := func(s string) bool {
			for _, x := range c.segs {
				if x == s {
					return true
				}
			}
			return false
		}
		addPos := func(i int, qty int64, avg float64) {
			d := derivs[i]
			strike := ""
			if d.strike > 0 {
				strike = fmt.Sprint(d.strike)
			}
			ps = append(ps, []string{c.ucc, d.exch, d.seg, d.sym, d.typ, d.exp, strike, fmt.Sprint(qty), fpx(avg)})
		}
		if has("FO") && r.f() < .5 {
			for k, n := 0, r.in(1, 2); k < n; k++ {
				i := r.in(0, 7)
				d := derivs[i]
				lots := int64(r.in(1, 2))
				if d.index {
					lots = int64(r.in(1, 4))
				}
				sd := int64(1)
				if (d.typ == "FUT" && r.f() >= .6) || (d.typ != "FUT" && r.f() < .55) {
					sd = -1
				}
				avg := d.px * r.fl(.985, 1.015)
				if d.typ != "FUT" {
					avg = tick(d.px * r.fl(.8, 1.2))
				}
				addPos(i, sd*lots*int64(d.lot), avg)
			}
		}
		if has("COM") && r.f() < .55 {
			i := r.in(9, 12)
			sd := int64(1)
			if r.f() < .5 {
				sd = -1
			}
			addPos(i, sd*int64(r.in(1, 3))*int64(derivs[i].lot), derivs[i].px*r.fl(.98, 1.02))
		}
		if has("CD") && r.f() < .6 {
			sd := int64(1)
			if r.f() < .5 {
				sd = -1
			}
			addPos(8, sd*int64(r.in(2, 20))*1000, 88.42*r.fl(.997, 1.003))
		}
	}
	day.Files = append(day.Files, File{"POSITIONS", "positions_bod_" + date + ".csv", date, csvBytes(ps), false})

	// ---- pledge requests ----
	pr := [][]string{{"RequestID", "UCC", "ISIN", "Symbol", "Qty", "Time"}}
	for k, p := range pledgeCands {
		if k%6 != 2 || len(pr) > 7 {
			continue
		}
		q := 0
		fmt.Sscan(p[3], &q)
		pr = append(pr, []string{fmt.Sprintf("PLG-%d", 7700+len(pr)), p[0], p[1], p[2], fmt.Sprint(int(math.Max(1, math.Round(float64(q)*r.fl(.4, 1))))), fmt.Sprintf("1%d:%02d", len(pr)%5, r.in(10, 59))})
	}
	day.Files = append(day.Files, File{"PLEDGE-REQUESTS", "pledge_requests_" + date + ".csv", "", csvBytes(pr), false})

	// ---- trades ----
	var traders []*client
	for i := range cs {
		if cs[i].trades {
			traders = append(traders, &cs[i])
		}
	}
	tm := func() string {
		m := r.in(555, 929)
		return fmt.Sprintf("%02d:%02d:%02d", m/60, m%60, r.in(0, 59))
	}
	var raw []trade
	for n := 0; n < 260; n++ {
		c := pick(r, traders)
		x := r.f()
		seg := "CM"
		switch {
		case x < .56:
		case x < .86:
			seg = "FO"
		case x < .94:
			seg = "COM"
		default:
			seg = "CD"
		}
		ok := false
		for _, s := range c.segs {
			ok = ok || s == seg
		}
		if !ok {
			seg = "CM"
		}
		side := "B"
		if r.f() >= .52 {
			side = "S"
		}
		algo, term := "", fmt.Sprintf("CTCL-%d", r.in(11, 19))
		if c.api {
			term = "API-GW-01"
			if r.f() < .85 {
				algo = fmt.Sprintf("ALG%d", r.in(10000, 99999))
			}
		}
		var t trade
		if seg == "CM" {
			e := pick(r, equities)
			exch := "NSE"
			idx := -1
			for i, q := range equities {
				if q.sym == e.sym {
					idx = i
				}
			}
			if idx < 9 && r.f() < .22 {
				exch = "BSE"
			}
			product := "CNC"
			if r.f() >= .55 {
				product = "MIS"
			}
			p := ltp[e.sym]
			qty := int64(math.Max(1, math.Round(float64(r.in(8000, 180000))/p)))
			if product == "CNC" && side == "S" {
				if h := hold[c.ucc][e.sym]; h > 0 {
					if r.f() < .93 {
						qty = int64(math.Max(1, math.Min(float64(qty), math.Round(float64(h)*r.fl(.2, .9)))))
					} else {
						qty = h + int64(r.in(5, 40))
					}
				} else if r.f() < .92 {
					side = "B"
				}
			}
			t = trade{exch: exch, seg: "CM", ucc: c.ucc, sym: e.sym, typ: "EQ", side: side, qty: qty, price: tick(p * (1 + (r.f()-.5)*.012)), tm: tm(), product: product, algo: algo, term: term}
		} else {
			var pool []int
			for i, d := range derivs {
				if d.seg == seg {
					pool = append(pool, i)
				}
			}
			i := pick(r, pool)
			d := derivs[i]
			lots := r.in(1, 3)
			if d.index {
				lots = r.in(1, 5)
			}
			product := "NRML"
			if r.f() >= .7 {
				product = "MIS"
			}
			p := dltp[i]
			var px float64
			switch {
			case d.seg == "CD":
				px = math.Round(p*(1+(r.f()-.5)*.004)*10000) / 10000
			case d.typ == "FUT":
				px = tick(p * (1 + (r.f()-.5)*.012))
			default:
				px = math.Max(.05, tick(p*(1+(r.f()-.5)*.18)))
			}
			t = trade{exch: d.exch, seg: d.seg, ucc: c.ucc, sym: d.sym, typ: d.typ, exp: d.exp, strike: d.strike, side: side, qty: int64(lots * d.lot), price: px, tm: tm(), product: product, algo: algo, term: term}
		}
		raw = append(raw, t)
		if t.product == "MIS" {
			h, m := 0, 0
			fmt.Sscanf(t.tm, "%d:%d", &h, &m)
			later := int(math.Min(929, float64(h*60+m+r.in(5, 120))))
			u := t
			if t.side == "B" {
				u.side = "S"
			} else {
				u.side = "B"
			}
			switch {
			case t.seg == "CD":
				u.price = math.Round(t.price*(1+(r.f()-.48)*.004)*10000) / 10000
			case t.typ == "FUT" || t.typ == "EQ":
				u.price = tick(t.price * (1 + (r.f()-.48)*.014))
			default:
				u.price = math.Max(.05, tick(t.price*(1+(r.f()-.48)*.25)))
			}
			u.tm = fmt.Sprintf("%02d:%02d:%02d", later/60, later%60, r.in(0, 59))
			raw = append(raw, u)
		}
	}
	sort.SliceStable(raw, func(i, j int) bool { return raw[i].tm < raw[j].tm })
	tn := map[string]int64{"NSE-CM": 1000452301, "BSE-CM": 5100088201, "NSE-FO": 2000311401, "NSE-CD": 4000120301, "MCX-COM": 3000877101}
	on := int64(12600000)
	for i := range raw {
		k := raw[i].exch + "-" + raw[i].seg
		raw[i].no = fmt.Sprint(tn[k])
		tn[k]++
		on += int64(r.in(1, 40))
		raw[i].order = fmt.Sprintf("1300%012d", on)
	}

	// OMS copy with injected breaks.
	oms := make([]trade, len(raw))
	copy(oms, raw)
	drop := map[int]bool{}
	taken := map[int]bool{}
	take := func(filter func(trade) bool) int {
		for tries := 0; tries < 2000; tries++ {
			k := r.in(0, len(raw)-1)
			if !taken[k] && filter(raw[k]) {
				taken[k] = true
				return k
			}
		}
		return -1
	}
	any := func(trade) bool { return true }
	for k := 0; k < 4; k++ {
		drop[take(any)] = true
	}
	for k := 0; k < 3; k++ {
		i := take(any)
		lot := int64(1)
		for _, d := range derivs {
			if d.sym == raw[i].sym && d.seg == raw[i].seg {
				lot = int64(d.lot)
			}
		}
		if lot == 1 {
			lot = int64(r.in(1, 10))
		}
		oms[i].qty += lot
	}
	for k := 0; k < 3; k++ {
		i := take(func(t trade) bool { return t.seg != "CD" })
		sgn := 1.0
		if r.f() < .5 {
			sgn = -1
		}
		oms[i].price = math.Round(raw[i].price*(1+sgn*r.fl(.001, .006))*100) / 100
	}
	for k := 0; k < 2; k++ {
		i := take(any)
		for {
			o := pick(r, traders)
			if o.ucc != raw[i].ucc {
				oms[i].ucc = o.ucc
				break
			}
		}
	}
	for k := 0; k < 2; k++ {
		if i := take(func(t trade) bool { return t.algo != "" }); i >= 0 {
			oms[i].algo = ""
		}
	}
	if i := take(func(t trade) bool { return t.seg == "FO" }); i >= 0 {
		n := 0
		fmt.Sscan(raw[i].no, &n)
		oms[i].no = fmt.Sprint(n + 900000)
	}
	var omsRows []trade
	for i, t := range oms {
		if !drop[i] {
			omsRows = append(omsRows, t)
		}
	}
	for k := 0; k < 3; k++ {
		b := omsRows[r.in(0, len(omsRows)-1)]
		k2 := b.exch + "-" + b.seg
		b.no = fmt.Sprint(tn[k2] + int64(500+k))
		omsRows = append(omsRows, b)
	}

	// ---- write trade files ----
	dmy := td.Format("02-Jan-2006")
	side12 := map[string]string{"B": "1", "S": "2"}
	cmHead := []string{"TradeNo", "OrderNo", "ClientCode", "Symbol", "Series", "BuySell", "Qty", "Price", "TradeDate", "TradeTime", "Product", "AlgoId", "TerminalId"}
	foHead := []string{"TradeNo", "OrderNo", "ClientCode", "Symbol", "InstrumentType", "Expiry", "Strike", "OptionType", "BuySell", "Qty", "Price", "TradeDate", "TradeTime", "Product", "AlgoId", "TerminalId"}
	nse, bse, fo, cd := [][]string{cmHead}, [][]string{cmHead}, [][]string{foHead}, [][]string{foHead}
	var mcx []string
	idxSyms := map[string]bool{"NIFTY": true, "BANKNIFTY": true}
	for _, t := range raw {
		switch t.seg {
		case "CM":
			row := []string{t.no, t.order, t.ucc, t.sym, "EQ", side12[t.side], fmt.Sprint(t.qty), f2(t.price), dmy, t.tm, t.product, t.algo, t.term}
			if t.exch == "BSE" {
				bse = append(bse, row)
			} else {
				nse = append(nse, row)
			}
		case "FO", "CD":
			it, strike, ot := "", "", ""
			switch {
			case t.seg == "CD" && t.typ == "FUT":
				it = "FUTCUR"
			case t.seg == "CD":
				it = "OPTCUR"
			case t.typ == "FUT" && idxSyms[t.sym]:
				it = "FUTIDX"
			case t.typ == "FUT":
				it = "FUTSTK"
			case idxSyms[t.sym]:
				it = "OPTIDX"
			default:
				it = "OPTSTK"
			}
			if t.typ == "CE" || t.typ == "PE" {
				strike, ot = fmt.Sprint(t.strike), t.typ
			}
			row := []string{t.no, t.order, t.ucc, t.sym, it, fmtExpTitle(t.exp), strike, ot, side12[t.side], fmt.Sprint(t.qty), fpx(t.price), dmy, t.tm, t.product, t.algo, t.term}
			if t.seg == "CD" {
				cd = append(cd, row)
			} else {
				fo = append(fo, row)
			}
		case "COM":
			mcx = append(mcx, strings.Join([]string{t.no, t.order, t.ucc, t.sym, "FUTCOM", fmtExp(t.exp, "02Jan2006"), t.side, fmt.Sprint(t.qty), f2(t.price), td.Format("20060102"), t.tm, t.product, t.term}, "|"))
		}
	}
	day.Files = append(day.Files,
		File{"NSE-CM-TRADE", "nse_cm_trades_" + date + ".csv", "", csvBytes(nse), false},
		File{"BSE-CM-TRADE", "bse_cm_trades_" + date + ".csv", "", csvBytes(bse), false},
		File{"NSE-FO-TRADE", "nse_fo_trades_" + date + ".csv", "", csvBytes(fo), false},
		File{"NSE-CD-TRADE", "nse_cd_trades_" + date + ".csv", "", csvBytes(cd), false},
		File{"MCX-TRADE", "mcx_trades_" + date + ".txt", "", []byte(strings.Join(mcx, "\n") + "\n"), false})
	om := [][]string{{"Exchange", "Segment", "TradeNo", "OrderNo", "ClientCode", "Symbol", "InstrumentType", "Expiry", "Strike", "OptionType", "Side", "Qty", "Price", "TradeDate", "TradeTime", "Product", "AlgoId", "Terminal"}}
	sort.SliceStable(omsRows, func(i, j int) bool { return omsRows[i].tm < omsRows[j].tm })
	for _, t := range omsRows {
		it, ot, strike := t.typ, "", ""
		if t.typ == "CE" || t.typ == "PE" {
			it, ot, strike = "OPT", t.typ, fmt.Sprint(t.strike)
		}
		om = append(om, []string{t.exch, t.seg, t.no, t.order, t.ucc, t.sym, it, t.exp, strike, ot, t.side, fmt.Sprint(t.qty), fpx(t.price), date, t.tm, t.product, t.algo, t.term})
	}
	day.Files = append(day.Files, File{"OMS-TRADE", "oms_tradebook_" + date + ".csv", "", csvBytes(om), false})

	// ---- bank statement ----
	bs := [][]string{{"Date", "Time", "Mode", "Reference", "Remitter", "Account", "Amount", "DrCr"}}
	var actives []*client
	for i := range cs {
		if cs[i].status == "Active" {
			actives = append(actives, &cs[i])
		}
	}
	nRecv := 0
	for k, c := range actives {
		if r.f() >= .3 || nRecv >= 16 {
			continue
		}
		nRecv++
		bs = append(bs, []string{date, fmt.Sprintf("%02d:%02d", 9+k/6, r.in(0, 59)), pick(r, []string{"NEFT", "IMPS", "UPI"}), fmt.Sprintf("N%d%02d", r.in(100000000, 999999999), r.in(10, 99)), strings.ToUpper(c.name), c.acct, fmt.Sprint(r.in(2, 40) * 5000), "CR"})
	}
	for k := 0; k < 4; k++ {
		c := actives[(k*11+3)%len(actives)]
		last := strings.Fields(c.name)
		ln := last[len(last)-1]
		if c.typ == "HUF" {
			ln = last[0]
		}
		bs = append(bs, []string{date, fmt.Sprintf("1%d:%02d", k+1, r.in(10, 59)), pick(r, []string{"NEFT", "IMPS"}), fmt.Sprintf("N%d", r.in(100000000, 999999999)),
			strings.ToUpper(pick(r, []string{"R ", "A ", "S "}) + ln), fmt.Sprintf("%d%08d", r.in(3010, 3999), r.in(10000000, 99999999)), fmt.Sprint(r.in(4, 30) * 5000), "CR"})
	}
	bs = append(bs, []string{date, "12:44", "NEFT", fmt.Sprintf("N%d", r.in(100000000, 999999999)), "KIRAN TRADING CO", fmt.Sprintf("%d%08d", r.in(6010, 6999), r.in(10000000, 99999999)), "75000", "CR"},
		[]string{date, "14:02", "UPI", fmt.Sprintf("U%d", r.in(100000000, 999999999)), "UPI/PAYMENT", "VPA", "18500", "CR"},
		[]string{date, "16:10", "NEFT", fmt.Sprintf("P%d", r.in(100000000, 999999999)), "PAYOUT BATCH", "", "250000", "DR"})
	day.Files = append(day.Files, File{"BANK-STMT", "bank_statement_" + date + ".csv", "", csvBytes(bs), false})

	// ---- exchange surveillance alerts ----
	pc := func(k int) string { return actives[(k*7+5)%len(actives)].ucc }
	sa := [][]string{{"AlertID", "Exchange", "Type", "UCC", "Detail", "Severity"},
		{"NSE-SV-3311", "NSE", "Turnover inconsistent with income", pc(1), "30-day turnover ₹4.8 Cr against declared income 1-5 L", "High"},
		{"BSE-SV-3312", "BSE", "Concentrated trading in illiquid scrip", pc(2), "Client accounted for 31% of exchange volume in a smallcap scrip on 14-Oct", "High"},
		{"NSE-SV-3315", "NSE", "Sudden spike in F&O activity", pc(3), "Option turnover up 14x versus 3-month average", "Medium"},
		{"MCX-SV-3317", "MCX", "Order-to-trade ratio breach", pc(4), "OTR 412 on CRUDEOIL between 14:00 and 14:30", "Low"},
	}
	day.Files = append(day.Files, File{"SURV-ALERTS", "exchange_alerts_" + date + ".csv", "", csvBytes(sa), false})

	// ---- IPO / MF ----
	ipo := [][]string{{"ApplicationNo", "UCC", "Issue", "Lots", "Amount", "UPI", "Mandate"}}
	allot := [][]string{{"ApplicationNo", "AllottedLots"}}
	issues := []string{"Aranya Foods Ltd (sample issue)", "Tejas Grid Solutions Ltd (sample issue)"}
	n := 0
	for _, c := range actives {
		if r.f() >= .22 || n >= 11 {
			continue
		}
		lots := r.in(1, 3)
		id := fmt.Sprintf("IPO-%d", 5100+n)
		ipo = append(ipo, []string{id, c.ucc, issues[n%2], fmt.Sprint(lots), fmt.Sprint(lots * 14800), strings.ToLower(c.ucc) + "@okbank", pick(r, []string{"Accepted", "Accepted", "Accepted", "Pending", "Declined"})})
		a := 0
		if r.f() < .35 {
			a = 1
		}
		allot = append(allot, []string{id, fmt.Sprint(a)})
		n++
	}
	day.Files = append(day.Files, File{"IPO-BIDS", "ipo_bids.csv", "", csvBytes(ipo), false})
	mf := [][]string{{"OrderNo", "UCC", "Scheme", "Type", "Amount", "Platform", "Status"}}
	schemes := []string{"Nifty 50 Index Fund · Direct Growth (sample)", "Flexi Cap Fund · Direct Growth (sample)", "Liquid Fund · Direct Growth (sample)", "Midcap Fund · Direct Growth (sample)"}
	n = 0
	for _, c := range actives {
		if r.f() >= .3 || n >= 10 {
			continue
		}
		mf = append(mf, []string{fmt.Sprintf("MF-%d", 8800+n), c.ucc, pick(r, schemes), pick(r, []string{"SIP", "SIP", "Lumpsum", "Redemption"}), fmt.Sprint(pick(r, []int{2500, 5000, 10000, 25000, 50000})), pick(r, []string{"BSE StAR MF", "NSE NMF II"}), pick(r, []string{"Placed", "Placed", "Allotted", "Payment pending"})})
		n++
	}
	day.Files = append(day.Files, File{"MF-ORDERS", "mf_orders_" + date + ".csv", "", csvBytes(mf), false})
	day.Files = append(day.Files, File{"IPO-ALLOTMENT", "ipo_allotment_results.csv", "", csvBytes(allot), true})
	return day
}

func fmtExpTitle(d string) string {
	t, _ := time.Parse("2006-01-02", d)
	return t.Format("02-Jan-2006")
}

func f1(x float64) string { return fmt.Sprintf("%.1f", x) }
func f2(x float64) string { return fmt.Sprintf("%.2f", x) }
func fpx(x float64) string {
	if x < 200 && x != math.Round(x*100)/100 {
		return fmt.Sprintf("%.4f", x)
	}
	return fmt.Sprintf("%.2f", x)
}
func paise(p int64) string { return fmt.Sprintf("%d.%02d", p/100, p%100) }

func csvBytes(rows [][]string) []byte {
	var b strings.Builder
	for _, r := range rows {
		for i, f := range r {
			if i > 0 {
				b.WriteByte(',')
			}
			if strings.ContainsAny(f, ",\"\n") {
				f = "\"" + strings.ReplaceAll(f, "\"", "\"\"") + "\""
			}
			b.WriteString(f)
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// holdingLots splits each holding into one to three purchase lots whose
// weighted cost matches the depository file's average cost, bought between
// April 2022 and the day before. It has its own random stream so the rest
// of the sample day is unchanged.
func holdingLots(rows [][]string, prevDate string) [][]string {
	r := &rng{s: 7319}
	start, _ := time.Parse("2006-01-02", "2022-04-01")
	end, _ := time.Parse("2006-01-02", prevDate)
	span := int(end.Sub(start).Hours()/24) - 1
	out := [][]string{{"UCC", "ISIN", "Symbol", "Qty", "BuyDate", "BuyPrice"}}
	for _, h := range rows {
		var q int64
		var avg float64
		fmt.Sscan(h[3], &q)
		fmt.Sscan(h[5], &avg)
		k := int64(r.in(1, 3))
		if k > q {
			k = q
		}
		qs, ps := []int64{}, []float64{}
		left, cost := q, avg*float64(q)
		for i := int64(0); i < k-1; i++ {
			lq := int64(math.Max(1, math.Round(float64(left)*r.fl(.2, .6))))
			lp := tick(avg * r.fl(.75, 1.25))
			qs, ps = append(qs, lq), append(ps, lp)
			left -= lq
			cost -= lp * float64(lq)
		}
		last := cost / float64(left)
		if left <= 0 || last < avg*.4 || last > avg*1.8 {
			qs, ps, left, last = nil, nil, q, avg
		}
		qs, ps = append(qs, left), append(ps, math.Round(last*100)/100)
		var days []int
		for range qs {
			days = append(days, r.in(0, span))
		}
		sort.Ints(days)
		for i := range qs {
			d := start.AddDate(0, 0, days[i])
			for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
				d = d.AddDate(0, 0, -1)
			}
			out = append(out, []string{h[0], h[1], h[2], fmt.Sprint(qs[i]), d.Format("2006-01-02"), f2(ps[i])})
		}
	}
	return out
}
