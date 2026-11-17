package xfile

import (
	"strings"
	"testing"

	"ledgerline/internal/sheet"
)

// Made-up lists only: the numbers, codes and PANs are not real.

func TestHeadersReadByName(t *testing.T) {
	l := lib(t)
	// NSE's report, with a title line, columns in any order, a pipe as the
	// delimiter and a total under the table.
	data := "Report for disconnected Mobile numbers\n\nSr No|Mobile No.|CLIENT_CODE|Client Name\n1|98765 43210|T001|A Client\n2|+91-9123456780|T002|B Client\nTotal Records : 2\n"
	s, err := l.Recognise("12345_MNRL_17112026.txt", []byte(data))
	if err != nil || s.ID != "nse-mnrl" {
		t.Fatalf("recognised as %v: %v", s, err)
	}
	tb, err := s.Read("x", []byte(data))
	if err != nil || len(tb.Issues) != 0 || len(tb.Rows) != 2 {
		t.Fatalf("%+v %v %v", tb.Rows, tb.Issues, err)
	}
	if r := tb.Rows[1]; r.Get("ucc") != "T002" || r.Get("mobile") != "+91-9123456780" || r.Get("pan") != "" || r.Line != 5 {
		t.Fatalf("row %+v", r)
	}
	if same, _, err := s.RoundTrip("x", []byte(data)); !same || err != nil {
		t.Fatal("a layout read by header names is checked for structure only")
	}
	// The same name with DoT's columns is DoT's list: the most columns win.
	dot := "Mobile Number,Date of Disconnection,Reason\n9000000001,01-11-2026,Customer request\n"
	if s, err := l.Recognise("12345_MNRL_17112026.csv", []byte(dot)); err != nil || s.ID != "dot-mnrl" {
		t.Fatalf("DoT's list recognised as %v: %v", s, err)
	}
	// NSDL's has the DP ID and client ID.
	nsdl := "DP ID,Client ID,Holder Name,Mobile Number\nIN300000,10000001,A Holder,9000000002\n"
	if s, err := l.Recognise("IN300000_MNRL_112026.csv", []byte(nsdl)); err != nil || s.ID != "nsdl-mnrl" {
		t.Fatalf("NSDL's list recognised as %v: %v", s, err)
	}
	// A header alone never recognises such a layout.
	if s, err := l.Recognise("clients.csv", []byte("UCC,Mobile\nT001,9000000003\n")); err == nil {
		t.Fatalf("a client file was taken for %s", s.ID)
	}
	// A required column missing is an issue, not a guess.
	s, _ = l.Get("nse-mnrl")
	tb, _ = s.Read("x", []byte("Mobile,Name\n9000000004,X\n"))
	if len(tb.Issues) != 1 || !strings.Contains(tb.Issues[0].Msg, "Client Code") || len(tb.Rows) != 0 {
		t.Fatalf("%v %v", tb.Issues, tb.Rows)
	}
}

func TestHeadersNoHeaderAndWorkbook(t *testing.T) {
	l := lib(t)
	s, _ := l.Get("nse-kra-demise")
	// A plain list of PANs and codes, no header row.
	tb, err := s.Read("x", []byte("AAAPA1111A,T001\r\nBBBPB2222B,T002\r\n"))
	if err != nil || len(tb.Rows) != 2 || tb.Rows[0].Get("pan") != "AAAPA1111A" || tb.Rows[1].Get("ucc") != "T002" || len(tb.Issues) != 0 {
		t.Fatalf("%+v %v %v", tb.Rows, tb.Issues, err)
	}
	// A PAN not in the PAN form is reported.
	tb, _ = s.Read("x", []byte("PAN,Client Code\nAAAPA1111A,T001\nNOTAPAN,T002\n"))
	if len(tb.Issues) != 1 || tb.Issues[0].Column != "PAN" || tb.Issues[0].Line != 3 {
		t.Fatalf("%v", tb.Issues)
	}
	// An Excel workbook, the table under a title.
	wb, err := sheet.WriteXLSX([]sheet.Sheet{{Name: "Sheet1", Rows: [][]string{{"Demise list"}, {}, {"Sr", "Client Code", "PAN No"}, {"1", "T003", "CCCPC3333C"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := l.Recognise("12345_Demise_Clients_by_KRA.xlsx", wb); err != nil || got.ID != "nse-kra-demise" {
		t.Fatalf("%v %v", got, err)
	}
	tb, err = s.Read("x", wb)
	if err != nil || len(tb.Rows) != 1 || tb.Rows[0].Get("pan") != "CCCPC3333C" || tb.Rows[0].Get("ucc") != "T003" {
		t.Fatalf("%+v %v %v", tb.Rows, tb.Issues, err)
	}
	// Scan hands the rows on without keeping them.
	n := 0
	var t2 Table
	if err := s.Scan([]byte("PAN\nAAAPA1111A\nBBBPB2222B\n"), &t2, func(Row) { n++ }); err != nil || n != 2 || len(t2.Rows) != 0 {
		t.Fatalf("%d %v", n, err)
	}
}
