package ops

import (
	"strings"
	"testing"
)

func TestCDSLCorpActionAndEDISFiles(t *testing.T) {
	o, ctx := setup(t)
	c, h := cdxTestClient(t, o, ctx)
	ch := xheader(t, "cdsl-corporate-action")
	ca := strings.Join(ch, ",") + "\r\n" + xline(ch, map[string]string{"Src": "CDSL", "CntrlSctiesDpstryPtcpt": "000001", "LineNb": "1", "CorpActnEvtId": "8001", "CATyp": "BON", "PrntISIN": h.ISIN,
		"CdtISIN": h.ISIN, "Rtio": "1:1", "CACntrNb": "1", "ExecDt": "2026-10-20", "RcrdDt": "2026-10-19", "CorpActnSts": "SETUP"}) + "\r\n"
	res, err := o.LoadExchangeFile(ctx, T, "priya.nair", "CA_EXP_081600_55_I_202610161200_001.csv", []byte(ca), true)
	if err != nil || res[0].Format != "cdsl-corporate-action" || res[0].Problems != 0 || res[0].Added != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if w := cdslWork(t, o, "cdslca", "8001|1"); len(w) != 1 || !strings.Contains(w[0].Detail, "record date 2026-10-19") {
		t.Errorf("corporate action work: %+v", w)
	}
	eh := xheader(t, "cdsl-edis")
	ed := strings.Join(eh, ",") + "\r\n" + xline(eh, map[string]string{"Src": "CDSL", "EdisTxnTyp": "PRT", "EDISExecQty": "0.000", "EDISPendQty": "5.000", "EdisStatus": "ACC", "ClntId": o.bo(ctx, T, c).BOID,
		"ISIN": h.ISIN, "Xchg": "NSE", "ClrMmbId": "M1234", "TxnId": "4400001", "TxnDt": D + "T10:00:00", "EDISTxnQty": "5.000"}) + "\r\n"
	res, err = o.LoadExchangeFile(ctx, T, "priya.nair", "EDIS_081600_202610161830_001.csv", []byte(ed), true)
	if err != nil || res[0].Format != "cdsl-edis" || res[0].Problems != 0 || res[0].Added != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if w := cdslWork(t, o, "cdsledis", "unknown:"); len(w) != 1 || !strings.Contains(w[0].Detail, c.UCC) {
		t.Errorf("eDIS work: %+v", w)
	}
}
