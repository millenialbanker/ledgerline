package sharepro

import (
	"fmt"
	"strings"
	"testing"

	"ledgerline/internal/migrate"
	"ledgerline/internal/migrate/pkg"
)

// Baskets (dummy client codes with their own rate rows) become rate cards,
// clients are put on them per segment with history, a client's own rows
// stay overrides, and the trading channel (PRODUCTCD) is kept apart.
func TestBrokerageBaskets(t *testing.T) {
	co := companyFake("TT2627")
	co.on("brokerage-sqr",
		// basket B1: intraday 0.02% by dealer, 0.008% online; options ₹20 a lot on mobile
		migrate.Row{"subcd": "B1", "effectdate": d("2024-04-01"), "itemcd": "ALL", "co": "02", "productcd": "", "sqbrptge": ".020000"},
		migrate.Row{"subcd": "B1", "effectdate": d("2024-04-01"), "itemcd": "ALL", "co": "02", "productcd": "IBT", "sqbrptge": ".008000"},
		migrate.Row{"subcd": "B1", "effectdate": d("2025-04-01"), "itemcd": "ALL", "co": "02", "productcd": "IBT", "sqbrptge": ".007000"},
		migrate.Row{"subcd": "B1", "effectdate": d("2024-04-01"), "itemcd": "ALLOPT", "co": "03", "productcd": "MBT", "sqbrflrt": "20.0000", "sqflatper": "Lot"},
		migrate.Row{"subcd": "B1", "effectdate": d("2024-04-01"), "itemcd": "ALL", "co": "02", "productcd": "XYZ", "sqbrptge": ".01"},
		// two baskets whose codes make the same card ID
		migrate.Row{"subcd": "19.5.06", "effectdate": d("2024-04-01"), "itemcd": "ALL", "co": "02", "sqbrptge": ".030000"},
		migrate.Row{"subcd": "19-5-06", "effectdate": d("2024-04-01"), "itemcd": "ALL", "co": "02", "sqbrptge": ".040000"},
		// a client's own row on top of the basket
		migrate.Row{"subcd": "TEST001", "effectdate": d("2025-04-01"), "itemcd": "ALL", "co": "02", "productcd": "DIR", "sqbrptge": ".015000"},
	)
	co.on("brokerage-del",
		// F&O events: the event is in ONCAT; P-F / P-O are futures and
		// options settled by delivery of shares
		migrate.Row{"subcd": "B1", "effectdate": d("2024-04-01"), "itemcd": "ALL", "co": "03", "atwhat": "", "onwhich": "Strike", "oncat": "Excercise", "dlbrptge": ".050000"},
		migrate.Row{"subcd": "B1", "effectdate": d("2024-04-01"), "itemcd": "ALL", "co": "03", "productcd": "P-F", "onwhich": "Spot", "dlbrptge": ".150000"},
	)
	co.on("brokerage-other",
		migrate.Row{"subcd": "TEST001", "co": "02", "effectdate": d("2024-04-01"), "brkgbasket": "B1"},
		migrate.Row{"subcd": "TEST001", "co": "03", "effectdate": d("2024-04-01"), "brkgbasket": "B1"},
		migrate.Row{"subcd": "TEST002", "co": "02", "effectdate": d("2024-04-01"), "brkgbasket": "19.5.06"},
		migrate.Row{"subcd": "TEST002", "co": "02", "effectdate": d("2025-04-01"), "brkgbasket": "19-5-06"},
		migrate.Row{"subcd": "TEST002", "co": "02", "effectdate": d("2026-04-01"), "brkgbasket": ""},
		migrate.Row{"subcd": "TEST003", "co": "02", "effectdate": d("2024-04-01"), "brkgbasket": "NORATES"},
		migrate.Row{"subcd": "B1", "co": "02", "effectdate": d("2024-04-01"), "ord_minbrk": "20.00"},
	)
	r, _ := runCurrentOn(t, migrate.Plan{ClientInterest: "firm"}, co)

	_, cards := readCSV(t, r, "firms/02/master/BROKERAGE-CARDS.csv")
	var got []string
	for _, c := range cards {
		got = append(got, fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s", c["Card"], c["Name"], c["From"], c["Product"], c["Channel"]+c["Case"], c["Pct"], c["PerLot"], c["Min"]))
	}
	for _, w := range []string{
		"B1|B1|2024-04-01|CM-INTRA||0.02||20", "B1|B1|2024-04-01|CM-INTRA|online|0.008||20",
		"B1|B1|2025-04-01|CM-INTRA||0.02||20", "B1|B1|2025-04-01|CM-INTRA|online|0.007||20",
		"B1|B1|2024-04-01|FO-OPT|mobile||20|", "B1|B1|2024-04-01|FO-OPT|exercise|0.05||", "B1|B1|2024-04-01|FO-FUT|physical|0.15||",
		"19-5-06|19-5-06|2024-04-01|CM-INTRA||0.04||", "19.5.06~2|19.5.06|2024-04-01|CM-INTRA||0.03||",
	} {
		if !contains(got, w) {
			t.Errorf("card line %q not in\n%s", w, strings.Join(got, "\n"))
		}
	}
	if len(issues(r, "Brokerage", "XYZ")) != 1 {
		t.Error("unknown channel not reported")
	}
	if len(issues(r, "Brokerage", "1 brokerage baskets that clients point at have no rates")) != 1 {
		t.Errorf("basket without rates not reported: %+v", r.Manifest.Issues)
	}

	_, as := readCSV(t, r, "firms/02/master/BROKERAGE-CLIENTS.csv")
	got = nil
	for _, a := range as {
		got = append(got, fmt.Sprintf("%s|%s|%s|%s|%s", a["UCC"], a["Card"], a["From"], a["Segment"], a["Exchange"]))
	}
	want := []string{"TEST001|B1|2024-04-01|CM|NSE", "TEST001|B1|2024-04-01|FO|NSE",
		"TEST002|19.5.06~2|2024-04-01|CM|NSE", "TEST002|19-5-06|2025-04-01|CM|NSE", "TEST002||2026-04-01|CM|NSE"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("assignments\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	_, own := readCSV(t, r, "firms/02/master/BROKERAGE-CLIENT-RATES.csv")
	if len(own) != 1 || own[0]["UCC"] != "TEST001" || own[0]["Channel"] != "dealer" || own[0]["Pct"] != "0.015" {
		t.Errorf("own rates %v", own)
	}
	var items []string
	for _, f := range r.Manifest.FillIn {
		items = append(items, f.Item)
	}
	if !contains(items, pkg.FillTerminalChannels) || !contains(items, pkg.FillBrokerageDefault) {
		t.Errorf("fill in %v", items)
	}

	// interest: the firm's rate for everyone; the grace days stay
	_, in := readCSV(t, r, "firms/02/master/CLIENT-INTEREST.csv")
	if len(in) != 2 || in[0]["UCC"] != "TEST001" || in[0]["Rate"] != "" || in[0]["GraceDays"] != "3" {
		t.Errorf("interest %v", in)
	}
	if len(issues(r, "Client interest", "18% from 2026-04-01 is not migrated")) != 1 {
		t.Error("dropped own rate not reported")
	}
}
