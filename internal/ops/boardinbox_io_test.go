package ops

import (
	"errors"
	"strings"
	"testing"

	"ledgerline/internal/docs"
)

// TestBoardInboxStaysPrivate: a person's Inbox never shows in collections
// or recently viewed, takes no email, gets no AI, and only its owner can
// export it.
func TestBoardInboxStaysPrivate(t *testing.T) {
	o, ctx := setup(t)
	me, other := "priya.nair", "farhan.khan"
	in, err := o.EnsureInbox(ctx, T, me)
	if err != nil {
		t.Fatal(err)
	}
	c, err := o.AddInboxCard(ctx, T, me, CardInput{Title: "Call the client back about the bank proof"})
	if err != nil {
		t.Fatal(err)
	}
	// Recently viewed: the Inbox is not kept, for anyone.
	if err := o.BoardViewed(ctx, T, me, in.ID); err != nil {
		t.Fatal(err)
	}
	if rs, _ := o.RecentBoards(ctx, T, me); len(rs) != 0 {
		t.Fatalf("recent %+v", rs)
	}
	if err := o.BoardViewed(ctx, T, other, in.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another person viewed my Inbox: %v", err)
	}
	// Collections: it cannot go in one, and someone else's Inbox is out of reach.
	coll, _ := o.SaveBoardCollection(ctx, T, me, "", "Mine")
	if _, err := o.SetBoardInCollection(ctx, T, me, coll.ID, in.ID, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Inbox in a collection: %v", err)
	}
	if _, err := o.SetBoardInCollection(ctx, T, "admin", coll.ID, in.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an Admin reached my Inbox: %v", err)
	}
	// Even an Inbox put in a collection by hand is shown to no one.
	docs.Update(ctx, o.Docs, T, cBoardColl, coll.ID, me, func(x *BoardCollection) error { x.Boards = append(x.Boards, in.ID); return nil })
	for _, who := range []string{me, other, "admin"} {
		cs, _ := o.BoardCollections(ctx, T, who)
		for _, x := range cs {
			if len(x.Boards) != 0 {
				t.Fatalf("%s sees %v in %s", who, x.Boards, x.Name)
			}
		}
	}
	// Email: no card address, and mail with a key is refused.
	if e, err := o.CardEmail(ctx, T, me, in.ID, c.ID); err != nil || e.On || !strings.Contains(e.Why, "Inbox") {
		t.Fatalf("Inbox card address %+v %v", e, err)
	}
	ib, _ := docs.Update(ctx, o.Docs, T, cBoard, in.ID, me, func(x *Board) error { x.EmailKey = boardID("", 32); return nil })
	if _, _, err := o.BoardInboundMail(ctx, T, cardMailKey(ib, c), sampleBoardMail()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Inbox card took mail: %v", err)
	}
	// AI: not offered, with the reason.
	if st, err := o.CardAI(ctx, T, me, in.ID, c.ID); err != nil || st.On || !strings.Contains(st.Why, "team boards") {
		t.Fatalf("AI on the Inbox %+v %v", st, err)
	}
	// Export: its owner only.
	if _, err := o.ExportBoardJSON(ctx, T, "admin", in.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an Admin exported my Inbox: %v", err)
	}
	if _, _, err := o.ExportBoardCSV(ctx, T, other, in.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else exported my Inbox: %v", err)
	}
	if ex, err := o.ExportBoardJSON(ctx, T, me, in.ID); err != nil || len(ex.Cards) != 1 {
		t.Fatalf("owner's export %v", err)
	}
	// Stickers are fine on the owner's own cards.
	if _, err := o.CardSticker(ctx, T, me, in.ID, c.ID, StickerOp{Add: "pin"}); err != nil {
		t.Fatal(err)
	}
}
