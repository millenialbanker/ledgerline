package ops

import (
	"context"
	"sort"
	"time"

	"ledgerline/internal/docs"
)

// ---- Boards: my cards across boards ----
//
// The boards home shows a person the cards they are a member of, on every
// board they can see, by when they fall due (web/boardviews.js). The
// board's own views (table, calendar, timeline, dashboard) work in the
// console from the board's cards and need nothing more from the server.

// MyCard is a card the person is a member of.
type MyCard struct {
	Board     string       `json:"board"`
	BoardName string       `json:"board_name"`
	Card      string       `json:"card"`
	No        int64        `json:"no"`
	Title     string       `json:"title"`
	List      string       `json:"list"`
	ListName  string       `json:"list_name"`
	Labels    []BoardLabel `json:"labels,omitempty"`
	Start     string       `json:"start,omitempty"`
	Due       string       `json:"due,omitempty"`
	Items     int          `json:"items,omitempty"` // checklist items
	ItemsDone int          `json:"items_done,omitempty"`
	When      string       `json:"when"` // overdue, today, week, later or none
	UpdatedAt string       `json:"updated_at"`
}

// MyCardsView is a person's open cards across boards. Cards marked
// complete are counted, not listed.
type MyCardsView struct {
	Today  string         `json:"today"`   // IST
	WeekTo string         `json:"week_to"` // the Sunday that ends this week
	Cards  []MyCard       `json:"cards"`
	Counts map[string]int `json:"counts"` // by When
	Done   int            `json:"done"`
	More   bool           `json:"more,omitempty"` // more than the cards listed
}

const myCardsMax = 500

// myCardWhen says when a card falls due: overdue (its due time has passed),
// today, this week (to Sunday), later, or none (no due date).
func myCardWhen(due string, now time.Time) string {
	t, ok := boardTime(due)
	if !ok {
		return "none"
	}
	now = now.In(ist)
	today := now.Format("2006-01-02")
	day := t.In(ist).Format("2006-01-02")
	switch {
	case t.Before(now):
		return "overdue"
	case day == today:
		return "today"
	case day <= myWeekEnd(now):
		return "week"
	}
	return "later"
}

// myWeekEnd is the Sunday that ends the week (Monday to Sunday) holding now.
func myWeekEnd(now time.Time) string {
	now = now.In(ist)
	return now.AddDate(0, 0, (7-int(now.Weekday()))%7).Format("2006-01-02")
}

// MyCards lists the open cards a person is a member of, on the boards they
// can see (not closed boards or templates), soonest due first.
func (o *Ops) MyCards(ctx context.Context, tenant, actor string) (MyCardsView, error) {
	bs, err := o.Boards(ctx, tenant, actor)
	if err != nil {
		return MyCardsView{}, err
	}
	now := o.now()
	out := MyCardsView{Today: now.Format("2006-01-02"), WeekTo: myWeekEnd(now), Cards: []MyCard{}, Counts: map[string]int{"overdue": 0, "today": 0, "week": 0, "later": 0, "none": 0}}
	for _, s := range bs {
		if s.Closed || s.Template {
			continue
		}
		b, _, err := docs.Get[Board](ctx, o.Docs, tenant, cBoard, s.ID)
		if err != nil {
			continue
		}
		cs, err := o.boardCards(ctx, tenant, s.ID)
		if err != nil {
			return MyCardsView{}, err
		}
		for _, c := range cs {
			if c.Archived || c.Template || c.Mirror != nil || !contains(c.Members, actor) {
				continue
			}
			l, ok := boardList(b, c.List)
			if !ok || l.Archived {
				continue
			}
			if c.DueDone {
				out.Done++
				continue
			}
			m := MyCard{Board: b.ID, BoardName: b.Name, Card: c.ID, No: c.No, Title: c.Title, List: l.ID, ListName: l.Name,
				Start: c.Start, Due: c.Due, When: myCardWhen(c.Due, now), UpdatedAt: c.UpdatedAt}
			for _, id := range c.Labels {
				if lb, ok := boardLabel(b, id); ok {
					m.Labels = append(m.Labels, lb)
				}
			}
			for _, cl := range c.Checklists {
				for _, it := range cl.Items {
					m.Items++
					if it.Done {
						m.ItemsDone++
					}
				}
			}
			out.Counts[m.When]++
			out.Cards = append(out.Cards, m)
		}
	}
	at := func(c MyCard) time.Time {
		t, ok := boardTime(c.Due)
		if !ok {
			return time.Date(9999, 1, 1, 0, 0, 0, 0, ist)
		}
		return t
	}
	sort.SliceStable(out.Cards, func(i, j int) bool {
		a, b := out.Cards[i], out.Cards[j]
		if ta, tb := at(a), at(b); !ta.Equal(tb) {
			return ta.Before(tb)
		}
		if a.BoardName != b.BoardName {
			return a.BoardName < b.BoardName
		}
		return a.No < b.No
	})
	if len(out.Cards) > myCardsMax {
		out.Cards, out.More = out.Cards[:myCardsMax], true
	}
	return out, nil
}
