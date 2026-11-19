package ops

import (
	"strings"
	"testing"
)

// TestAllocFileNumbers: allocation files take the day's next number, keep
// it when downloaded again, and start from the firm's setting.
func TestAllocFileNumbers(t *testing.T) {
	o, ctx := setup(t)
	name, b, err := o.ExchangeFile(ctx, T, "priya.nair", "ncl-allocation", D, XFileOptions{})
	if err != nil || !strings.HasSuffix(name, "_ALLOC_16102026.T0001") {
		t.Fatalf("first file %s %v", name, err)
	}
	if again, b2, _ := o.ExchangeFile(ctx, T, "priya.nair", "ncl-allocation", D, XFileOptions{}); again != name || string(b2) != string(b) {
		t.Fatalf("the same file downloaded again is %s", again)
	}
	// Another file of the day (a pay-out hold file, say) takes the next one.
	if n, err := o.allocFileSeq(ctx, T, "NSE", D, "Pay-out holds", []byte("D rows"), 0); err != nil || n != 2 {
		t.Fatalf("second file %d %v", n, err)
	}
	if n, _ := o.allocFileSeq(ctx, T, "NSE", D, "Pay-out hold releases", []byte("U rows"), 0); n != 3 {
		t.Fatalf("third file %d", n)
	}
	// MCX counts on its own.
	if n, _ := o.allocFileSeq(ctx, T, "MCX", D, "Client allocation", []byte("mcx"), 0); n != 1 {
		t.Fatalf("MCX's first file %d", n)
	}
	// A number given by hand is refused when another file has it.
	if _, err := o.allocFileSeq(ctx, T, "NSE", D, "Client allocation", []byte("other"), 2); err == nil || !strings.Contains(err.Error(), "T0004") {
		t.Fatalf("a used number was taken: %v", err)
	}
	if n, _ := o.allocFileSeq(ctx, T, "NSE", D, "Client allocation", []byte("other"), 7); n != 7 {
		t.Fatalf("a free number given by hand: %d", n)
	}
	// The firm starts its numbers above the office's own files.
	set, _ := o.Settings(ctx, T)
	set.AllocFileStart = 50
	if err := o.SaveSettings(ctx, T, "admin", set); err != nil {
		t.Fatal(err)
	}
	if n, _ := o.allocFileSeq(ctx, T, "NSE", "2026-10-19", "Client allocation", []byte("x"), 0); n != 50 {
		t.Fatalf("start setting: %d", n)
	}
	set.AllocFileStart = 9500
	if err := o.SaveSettings(ctx, T, "admin", set); err == nil {
		t.Fatal("a start of 9500 was taken")
	}
}
