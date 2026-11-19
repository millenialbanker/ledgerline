package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"ledgerline/internal/docs"
)

// ---- N-MASS allocation file numbers ----
//
// An allocation file for NSE Clearing's N-MASS (or MCXCCL) is named
// <member>_ALLOC_<DDMMYYYY>.T<nnnn>, the T number being the file's number
// of the day, which must not repeat. Ledgerline used to write T0001 for the
// day's allocation and fixed T0002 / T0003 for the pay-out hold and release
// files, which could clash with a file the office uploads itself. Now each
// file gets the day's next number, per exchange, starting from the firm's
// "first allocation file number" (Settings, 1 by default). Asking again for
// a file with the same contents gives it the same number, so a second
// download doesn't use up a number; a number given by hand is kept, unless
// it was already used for different contents.

const cAllocSeq = "allocfileseq" // key: exchange|date

// AllocFileDay is one day's allocation file numbers for an exchange.
type AllocFileDay struct {
	Exchange string            `json:"exchange"`
	Date     string            `json:"date"`
	Files    map[string]int    `json:"files"`           // contents hash -> number
	Names    map[string]string `json:"names,omitempty"` // number -> what the file was
	Updated  string            `json:"updated"`
}

var allocSeqMu sync.Mutex

// allocFileSeq is the number for an allocation file with these contents.
func (o *Ops) allocFileSeq(ctx context.Context, tenant, exchange, date, what string, content []byte, given int) (int, error) {
	allocSeqMu.Lock()
	defer allocSeqMu.Unlock()
	key := exchange + "|" + date
	d, _, _ := docs.GetOr(ctx, o.Docs, tenant, cAllocSeq, key, AllocFileDay{Exchange: exchange, Date: date})
	if d.Files == nil {
		d.Files = map[string]int{}
	}
	if d.Names == nil {
		d.Names = map[string]string{}
	}
	sum := sha256.Sum256(content)
	h := hex.EncodeToString(sum[:])
	used := map[int]string{}
	for k, n := range d.Files {
		used[n] = k
	}
	var n int
	switch {
	case given > 0:
		if old, ok := used[given]; ok && old != h {
			return 0, invalid("allocation file T%04d of %s was already written with other contents (%s); leave the number empty to take the next, T%04d", given, fmtDay(date), d.Names[fmt.Sprint(given)], o.allocNext(ctx, tenant, used))
		}
		n = given
	case d.Files[h] > 0:
		return d.Files[h], nil
	default:
		n = o.allocNext(ctx, tenant, used)
	}
	if n > 9999 {
		return 0, invalid("no allocation file number is left for %s", fmtDay(date))
	}
	d.Files[h], d.Names[fmt.Sprint(n)], d.Updated = n, what, o.now().Format(time.RFC3339)
	if err := docs.Upsert(ctx, o.Docs, tenant, cAllocSeq, key, d, SystemActor); err != nil {
		return 0, err
	}
	return n, nil
}

// allocNext is the lowest free number from the firm's starting number.
func (o *Ops) allocNext(ctx context.Context, tenant string, used map[int]string) int {
	set, _ := o.Settings(ctx, tenant)
	n := set.AllocFileStart
	if n <= 0 {
		n = 1
	}
	for {
		if _, ok := used[n]; !ok {
			return n
		}
		n++
	}
}
