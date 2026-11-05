package xfile

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"strings"
	"testing"
)

// A real file's summary shows no field value: not a client code, an
// amount or a date, even where they are wrong.
func TestCheckShowsNoValues(t *testing.T) {
	l := lib(t)
	s, _ := l.Get("mcx-obligation-tm")
	good := strings.Join(synth(s, &s.Records[0]), ",")
	cli := synth(s, &s.Records[1])
	cli[2], cli[8] = "SECRETUCC7", "12x45.67" // a client code; a bad amount
	short := strings.Join(synth(s, &s.Records[1])[:9], ",")
	data := good + "\n" + strings.Join(cli, ",") + "\n" + short + "\n"
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Name = "MCX_NOBL_56845_20261104.csv"
	zw.Write([]byte(data))
	zw.Close()
	rs := l.Check("MCX_NOBL_56845_20261104.csv.gz", gz.Bytes())
	if len(rs) != 1 || rs[0].Spec != "mcx-obligation-tm" || rs[0].Rows != 2 || rs[0].Issues != 2 || rs[0].Fields[13] != 2 || rs[0].Fields[9] != 1 {
		t.Fatalf("%+v", rs)
	}
	var out bytes.Buffer
	WriteCheck(&out, rs[0])
	js, _ := json.Marshal(rs)
	for _, text := range []string{out.String(), string(js)} {
		for _, leak := range []string{"SECRET", "12x45", "45.67", "56845", "20261104", "TEST"} {
			if strings.Contains(text, leak) {
				t.Fatalf("%q shown:\n%s", leak, text)
			}
		}
	}
	for _, want := range []string{"MCX_NOBL_99999_99999999.csv", "mcx-obligation-tm", "MTM Profit/Loss", "\"99A99.99\" is not a number", "13 fields on 2 lines", "the number of fields does not fit"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("%q not in:\n%s", want, out.String())
		}
	}
	// An unknown file is named (digits masked) and said to be unknown.
	rs = l.Check("CLIENT_4711.csv", []byte("a,b\n"))
	if len(rs) != 1 || rs[0].Recognised || !strings.Contains(rs[0].Note, "CLIENT_9999.csv") {
		t.Fatalf("%+v", rs)
	}
}
