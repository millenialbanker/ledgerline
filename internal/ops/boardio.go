package ops

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"ledgerline/internal/docs"
)

// ---- Boards: import from Trello, export, collections, recently viewed ----
//
//   - Import from Trello: a board's JSON export (Trello: Menu → Print,
//     export and share → Export as JSON) becomes a private team board:
//     lists, labels, cards, checklists, custom fields and comments, with
//     Trello's members mapped to Ledgerline users by the person importing.
//     Comments are posted by that person, each saying who wrote it in
//     Trello and when; attachments become links to their Trello addresses
//     (nothing is downloaded); covers keep their colour only. The export is
//     read defensively: anything that is not a Trello board export is
//     refused, and counts are capped.
//   - Export: a board as Ledgerline's own JSON (board admins) or its cards
//     as CSV (members), a card as JSON, and print views; each is written to
//     the board's activity.
//   - Collections: firm-wide groups of boards on the boards home. Anyone at
//     head office makes and names them; board admins add their boards.
//   - Recently viewed: the last boards each person opened, kept on the
//     server so it follows them between computers.

const (
	cBoardColl   = "boardcoll"   // a collection, by id
	cBoardRecent = "boardrecent" // a person's recently viewed boards, by user

	// BoardImportMax is the largest Trello export taken.
	BoardImportMax = 20 << 20

	trelloMaxLists      = 500
	trelloMaxCards      = 5000
	trelloMaxChecklists = 20  // on a card
	trelloMaxItems      = 200 // in a checklist
	trelloMaxAllItems   = 50000
	trelloMaxComments   = 20000
	trelloMaxCardCmts   = 500
	trelloMaxAttach     = 100 // on a card
	trelloMaxLabels     = 50
	trelloMaxFields     = 50
	trelloMaxMembers    = 200

	boardRecentShown = 6
	boardRecentKept  = 20
	boardMaxColls    = 100
	boardCollMax     = 500 // boards in a collection
)

// ---- reading a Trello export ----

// tStr reads a JSON string, number or boolean as text; null and anything
// else as "". Trello's exports are loosely typed.
type tStr string

func (s *tStr) UnmarshalJSON(b []byte) error {
	var v any
	if json.Unmarshal(b, &v) != nil {
		*s = ""
		return nil
	}
	switch x := v.(type) {
	case string:
		*s = tStr(strings.ToValidUTF8(strings.ReplaceAll(x, "\x00", ""), "�"))
	case float64:
		*s = tStr(strconv.FormatFloat(x, 'f', -1, 64))
	case bool:
		*s = tStr(strconv.FormatBool(x))
	default:
		*s = ""
	}
	return nil
}

// tNum reads a number or a numeric string; anything else is 0.
type tNum float64

func (n *tNum) UnmarshalJSON(b []byte) error {
	var v any
	*n = 0
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	switch x := v.(type) {
	case float64:
		*n = tNum(x)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
			*n = tNum(f)
		}
	}
	return nil
}

// tBool reads true, false, "true" or "false".
type tBool bool

func (t *tBool) UnmarshalJSON(b []byte) error {
	var v any
	*t = false
	if json.Unmarshal(b, &v) == nil {
		switch x := v.(type) {
		case bool:
			*t = tBool(x)
		case string:
			*t = tBool(strings.EqualFold(x, "true"))
		}
	}
	return nil
}

// tStrs reads an array of strings, ignoring anything else.
type tStrs []string

func (t *tStrs) UnmarshalJSON(b []byte) error {
	var vs []tStr
	*t = nil
	if json.Unmarshal(b, &vs) == nil {
		for _, v := range vs {
			if v != "" {
				*t = append(*t, string(v))
			}
		}
	}
	return nil
}

// tObj is an object read leniently: a field of the wrong type is dropped.
type tObj[T any] struct{ V T }

func (o *tObj[T]) UnmarshalJSON(b []byte) error {
	_ = json.Unmarshal(b, &o.V)
	return nil
}

type tList struct {
	ID     tStr  `json:"id"`
	Name   tStr  `json:"name"`
	Closed tBool `json:"closed"`
	Pos    tNum  `json:"pos"`
}

type tLabel struct {
	ID    tStr `json:"id"`
	Name  tStr `json:"name"`
	Color tStr `json:"color"`
}

type tCover struct {
	Color        tStr `json:"color"`
	IDAttachment tStr `json:"idAttachment"`
	Size         tStr `json:"size"`
}

type tAttachment struct {
	Name tStr `json:"name"`
	URL  tStr `json:"url"`
}

type tFieldItem struct {
	IDCustomField tStr                  `json:"idCustomField"`
	IDValue       tStr                  `json:"idValue"`
	Value         tObj[map[string]tStr] `json:"value"`
}

type tCard struct {
	ID          tStr                `json:"id"`
	Name        tStr                `json:"name"`
	Desc        tStr                `json:"desc"`
	Closed      tBool               `json:"closed"`
	IDList      tStr                `json:"idList"`
	IDLabels    tStrs               `json:"idLabels"`
	IDMembers   tStrs               `json:"idMembers"`
	Pos         tNum                `json:"pos"`
	Due         tStr                `json:"due"`
	Start       tStr                `json:"start"`
	DueComplete tBool               `json:"dueComplete"`
	IDShort     tNum                `json:"idShort"`
	Cover       tObj[tCover]        `json:"cover"`
	Attachments tObj[[]tAttachment] `json:"attachments"`
	FieldItems  tObj[[]tFieldItem]  `json:"customFieldItems"`
}

type tCheckItem struct {
	Name     tStr `json:"name"`
	State    tStr `json:"state"`
	Pos      tNum `json:"pos"`
	Due      tStr `json:"due"`
	IDMember tStr `json:"idMember"`
}

type tChecklist struct {
	ID         tStr               `json:"id"`
	Name       tStr               `json:"name"`
	IDCard     tStr               `json:"idCard"`
	Pos        tNum               `json:"pos"`
	CheckItems tObj[[]tCheckItem] `json:"checkItems"`
}

type tOption struct {
	ID    tStr                  `json:"id"`
	Value tObj[map[string]tStr] `json:"value"`
	Pos   tNum                  `json:"pos"`
}

type tField struct {
	ID      tStr                   `json:"id"`
	Name    tStr                   `json:"name"`
	Type    tStr                   `json:"type"`
	Pos     tNum                   `json:"pos"`
	Options tObj[[]tOption]        `json:"options"`
	Display tObj[map[string]tBool] `json:"display"`
}

type tMember struct {
	ID       tStr `json:"id"`
	FullName tStr `json:"fullName"`
	Username tStr `json:"username"`
}

type tMembership struct {
	IDMember   tStr `json:"idMember"`
	MemberType tStr `json:"memberType"`
}

type tAction struct {
	Type tStr `json:"type"`
	Date tStr `json:"date"`
	Data tObj[struct {
		Text tStr `json:"text"`
		Card tObj[struct {
			ID tStr `json:"id"`
		}] `json:"card"`
	}] `json:"data"`
	MemberCreator tObj[tMember] `json:"memberCreator"`
	IDCreator     tStr          `json:"idMemberCreator"`
}

// trelloComment is a comment found in the export's actions.
type trelloComment struct {
	Card, Text, Date, By, Name, Username string
}

// trelloBoard is what Ledgerline takes from a Trello board export.
type trelloBoard struct {
	Name, Desc, Background string
	Lists                  []tList
	Labels                 []tLabel
	Cards                  []tCard
	Checklists             map[string][]tChecklist // by card
	Fields                 []tField
	Members                []tMember
	MemberType             map[string]string
	Comments               map[string][]trelloComment // by card, oldest first
	Actions                int
	Notes                  []string // what was left out
	totals                 map[string]int
}

var trelloIDRe = regexp.MustCompile(`^[0-9a-f]{24}$`)

// tArray reads a JSON array element by element: an element that cannot be
// read is skipped, and at most max are kept. It returns the elements kept
// and how many there were.
func tArray[T any](raw json.RawMessage, max int) ([]T, int) {
	var arr []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &arr) != nil {
		return nil, 0
	}
	out := make([]T, 0, min(len(arr), max))
	for _, e := range arr {
		if len(out) >= max {
			break
		}
		var x T
		if json.Unmarshal(e, &x) == nil {
			out = append(out, x)
		}
	}
	return out, len(arr)
}

func nPlural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%s %ss", humanInt(n), one)
}

func humanInt(n int) string {
	s := strconv.Itoa(n)
	if n < 1000 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// parseTrello reads a Trello board export, or says why it is not one.
func parseTrello(raw []byte) (trelloBoard, error) {
	if len(raw) == 0 {
		return trelloBoard{}, invalid("the file is empty")
	}
	if len(raw) > BoardImportMax {
		return trelloBoard{}, invalid("a Trello export can be up to %d MB", BoardImportMax>>20)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		var arr []any
		if json.Unmarshal(raw, &arr) == nil {
			return trelloBoard{}, invalid("this is not a Trello board export: it is a list, where a board export is one board")
		}
		return trelloBoard{}, invalid("this is not a JSON file, or it is cut short: export the board from Trello again (Menu → Print, export and share → Export as JSON)")
	}
	var id, name tStr
	_ = json.Unmarshal(top["id"], &id)
	_ = json.Unmarshal(top["name"], &name)
	isArr := func(k string) bool { v := bytes.TrimSpace(top[k]); return len(v) > 0 && v[0] == '[' }
	if _, ok := top["idList"]; ok && !isArr("lists") {
		return trelloBoard{}, invalid("this is a Trello card export; export the whole board instead (board menu → Print, export and share → Export as JSON)")
	}
	if !trelloIDRe.MatchString(string(id)) || !isArr("lists") || !isArr("cards") || (!isArr("labels") && !isArr("actions") && top["prefs"] == nil) {
		return trelloBoard{}, invalid("this is not a Trello board export: it has no Trello board with lists and cards. In Trello open the board, then Menu → Print, export and share → Export as JSON")
	}
	t := trelloBoard{Name: strings.TrimSpace(string(name)), Checklists: map[string][]tChecklist{}, Comments: map[string][]trelloComment{}, MemberType: map[string]string{}, totals: map[string]int{}}
	var desc tStr
	_ = json.Unmarshal(top["desc"], &desc)
	t.Desc = string(desc)
	var prefs tObj[map[string]tStr]
	_ = json.Unmarshal(top["prefs"], &prefs)
	t.Background = trelloBackground(string(prefs.V["background"]))
	note := func(kept, total int, what string) {
		if total > kept {
			t.Notes = append(t.Notes, fmt.Sprintf("Only the first %s are imported; the export has %s.", nPlural(kept, what), humanInt(total)))
		}
	}
	var n int
	t.Lists, n = tArray[tList](top["lists"], trelloMaxLists)
	note(len(t.Lists), n, "list")
	t.Labels, _ = tArray[tLabel](top["labels"], 1000)
	if n = len(trelloLabels(t.Labels, func(string) bool { return false })); n == trelloMaxLabels {
		all := 0
		for _, l := range t.Labels {
			if l.ID != "" && (l.Name != "" || l.Color != "") {
				all++
			}
		}
		note(n, all, "label")
	}
	t.Fields, n = tArray[tField](top["customFields"], trelloMaxFields)
	note(len(t.Fields), n, "custom field")
	t.Members, n = tArray[tMember](top["members"], trelloMaxMembers)
	note(len(t.Members), n, "member")
	ms, _ := tArray[tMembership](top["memberships"], 2000)
	for _, m := range ms {
		t.MemberType[string(m.IDMember)] = strings.ToLower(string(m.MemberType))
	}
	sort.SliceStable(t.Lists, func(i, j int) bool { return t.Lists[i].Pos < t.Lists[j].Pos })
	lists := map[string]int{}
	for i, l := range t.Lists {
		lists[string(l.ID)] = i
	}
	cards, total := tArray[tCard](top["cards"], 1<<20)
	sort.SliceStable(cards, func(i, j int) bool {
		li, lj := lists[string(cards[i].IDList)], lists[string(cards[j].IDList)]
		if li != lj {
			return li < lj
		}
		return cards[i].Pos < cards[j].Pos
	})
	orphans := 0
	for _, c := range cards {
		if _, ok := lists[string(c.IDList)]; !ok || c.ID == "" {
			orphans++
			continue
		}
		if len(t.Cards) < trelloMaxCards {
			t.Cards = append(t.Cards, c)
		}
	}
	note(len(t.Cards), total-orphans, "card")
	if orphans > 0 {
		t.Notes = append(t.Notes, fmt.Sprintf("%s in no list of the export %s left out.", nPlural(orphans, "card"), map[bool]string{true: "is", false: "are"}[orphans == 1]))
	}
	kept := map[string]bool{}
	for _, c := range t.Cards {
		kept[string(c.ID)] = true
	}
	cls, _ := tArray[tChecklist](top["checklists"], 1<<20)
	sort.SliceStable(cls, func(i, j int) bool { return cls[i].Pos < cls[j].Pos })
	items, clsLeft, itemsLeft := 0, 0, 0
	for _, cl := range cls {
		card := string(cl.IDCard)
		if !kept[card] {
			continue
		}
		if len(t.Checklists[card]) >= trelloMaxChecklists {
			clsLeft++
			continue
		}
		its := cl.CheckItems.V
		sort.SliceStable(its, func(i, j int) bool { return its[i].Pos < its[j].Pos })
		if len(its) > trelloMaxItems {
			itemsLeft += len(its) - trelloMaxItems
			its = its[:trelloMaxItems]
		}
		if items+len(its) > trelloMaxAllItems {
			itemsLeft += items + len(its) - trelloMaxAllItems
			its = its[:max(0, trelloMaxAllItems-items)]
		}
		items += len(its)
		cl.CheckItems.V = its
		t.Checklists[card] = append(t.Checklists[card], cl)
	}
	if clsLeft > 0 {
		t.Notes = append(t.Notes, fmt.Sprintf("%s beyond %d on a card %s left out.", nPlural(clsLeft, "checklist"), trelloMaxChecklists, map[bool]string{true: "is", false: "are"}[clsLeft == 1]))
	}
	if itemsLeft > 0 {
		t.Notes = append(t.Notes, fmt.Sprintf("%s beyond %d in a checklist %s left out.", nPlural(itemsLeft, "checklist item"), trelloMaxItems, map[bool]string{true: "is", false: "are"}[itemsLeft == 1]))
	}
	acts, nActs := tArray[tAction](top["actions"], 1<<20)
	t.Actions = nActs
	var cms []trelloComment
	for _, a := range acts {
		if a.Type != "commentCard" || !kept[string(a.Data.V.Card.V.ID)] || strings.TrimSpace(string(a.Data.V.Text)) == "" {
			continue
		}
		mc := a.MemberCreator.V
		cms = append(cms, trelloComment{Card: string(a.Data.V.Card.V.ID), Text: string(a.Data.V.Text), Date: string(a.Date), By: firstOf(string(mc.ID), string(a.IDCreator)), Name: string(mc.FullName), Username: string(mc.Username)})
	}
	sort.SliceStable(cms, func(i, j int) bool { return cms[i].Date < cms[j].Date }) // Trello lists actions newest first
	cmLeft := 0
	if len(cms) > trelloMaxComments {
		cmLeft = len(cms) - trelloMaxComments
		cms = cms[len(cms)-trelloMaxComments:]
	}
	for _, cm := range cms {
		if len(t.Comments[cm.Card]) >= trelloMaxCardCmts {
			cmLeft++
			continue
		}
		t.Comments[cm.Card] = append(t.Comments[cm.Card], cm)
	}
	if cmLeft > 0 {
		t.Notes = append(t.Notes, fmt.Sprintf("%s left out (at most %s in all, %d on a card); the oldest go first.", nPlural(cmLeft, "comment"), humanInt(trelloMaxComments), trelloMaxCardCmts))
	}
	if nActs >= 1000 {
		t.Notes = append(t.Notes, "Trello's export can leave out older activity, so the oldest comments of a busy board may be missing.")
	}
	return t, nil
}

func trelloBackground(bg string) string {
	switch bg = strings.ToLower(strings.TrimSpace(bg)); {
	case bg == "grey" || bg == "gray":
		return "black"
	case contains(boardColors, bg):
		return bg
	}
	return "blue"
}

// trelloColor is a Trello colour on Ledgerline's palette: the ten colours
// as they are; the light and dark shades as Ledgerline's subtle and bold
// ones where the board takes them (ok), otherwise their colour.
func trelloColor(c string, ok func(string) bool) string {
	c = strings.ToLower(strings.TrimSpace(c))
	base, shade, _ := strings.Cut(c, "_")
	if !contains(boardColors, base) {
		return "black"
	}
	if s := map[string]string{"light": "_subtle", "dark": "_bold"}[shade]; s != "" && ok(base+s) {
		return base + s
	}
	return base
}

// trelloDate is a Trello date (UTC, RFC 3339) as a card date in IST:
// with the time, or the day alone.
func trelloDate(s string, withTime bool) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	if withTime {
		return t.In(ist).Format("2006-01-02T15:04")
	}
	return t.In(ist).Format("2006-01-02")
}

// cleanText is imported text: line breaks tidied, at most n characters.
func cleanText(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"))
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// ---- the preview ----

// TrelloMember is a Trello member, with the Ledgerline user it seems to be.
type TrelloMember struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Type     string `json:"type"`  // admin, normal or observer on the Trello board
	Guess    string `json:"guess"` // a Ledgerline user, or ""
	Why      string `json:"why,omitempty"`
}

// TrelloListPreview is one list of the export.
type TrelloListPreview struct {
	Name          string   `json:"name"`
	Archived      bool     `json:"archived"`
	Cards         int      `json:"cards"`          // open cards
	ArchivedCards int      `json:"archived_cards"` // archived cards
	Titles        []string `json:"titles"`         // the first few open cards
}

// TrelloPreview is what an import would make, before it is made.
type TrelloPreview struct {
	Name          string              `json:"name"`
	Description   string              `json:"description,omitempty"`
	Lists         int                 `json:"lists"`
	ListsArchived int                 `json:"lists_archived"`
	Cards         int                 `json:"cards"`
	CardsArchived int                 `json:"cards_archived"`
	Checklists    int                 `json:"checklists"`
	Items         int                 `json:"items"`
	Labels        int                 `json:"labels"`
	Fields        int                 `json:"fields"`
	Comments      int                 `json:"comments"`
	Attachments   int                 `json:"attachments"`
	Covers        int                 `json:"covers"`
	Members       []TrelloMember      `json:"members"`
	Users         []BoardUserRef      `json:"users"` // who members can be mapped to
	ListPreview   []TrelloListPreview `json:"list_preview"`
	Notes         []string            `json:"notes"`
}

func trelloNorm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

func alnum(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}

// guessUser is the Ledgerline user a Trello member seems to be: the one
// person with the same full name, or else whose user ID or name matches
// the Trello username.
func guessUser(m tMember, us []User) (string, string) {
	one := func(match func(User) bool) string {
		found := ""
		for _, u := range us {
			if match(u) {
				if found != "" {
					return ""
				}
				found = u.ID
			}
		}
		return found
	}
	if n := trelloNorm(string(m.FullName)); n != "" {
		if id := one(func(u User) bool { return trelloNorm(u.Name) == n }); id != "" {
			return id, "same name"
		}
	}
	if un := alnum(string(m.Username)); len(un) >= 3 {
		if id := one(func(u User) bool { return alnum(u.ID) == un || alnum(u.Name) == un }); id != "" {
			return id, "same username"
		}
	}
	return "", ""
}

// importUsers are the people Trello members can be mapped to.
func (o *Ops) importUsers(ctx context.Context, tenant string) []User {
	us, _ := o.Users(ctx, tenant)
	var out []User
	for _, u := range us {
		if u.Active && u.Role != RoleClient {
			out = append(out, u)
		}
	}
	return out
}

// PreviewTrelloImport reads a Trello board export and says what importing
// it would make, with a guess at who each Trello member is.
func (o *Ops) PreviewTrelloImport(ctx context.Context, tenant, actor string, raw []byte) (TrelloPreview, error) {
	if _, err := o.boardUser(ctx, tenant, actor); err != nil {
		return TrelloPreview{}, err
	}
	t, err := parseTrello(raw)
	if err != nil {
		return TrelloPreview{}, err
	}
	us := o.importUsers(ctx, tenant)
	p := TrelloPreview{Name: cleanText(firstOf(t.Name, "Board from Trello"), 120), Description: cleanText(t.Desc, 600), Members: []TrelloMember{}, Users: []BoardUserRef{}, ListPreview: []TrelloListPreview{}, Notes: []string{}}
	for _, u := range us {
		p.Users = append(p.Users, BoardUserRef{ID: u.ID, Name: u.Name, Role: u.Role})
	}
	byList := map[string]*TrelloListPreview{}
	for _, l := range t.Lists {
		p.Lists++
		if l.Closed {
			p.ListsArchived++
		}
		p.ListPreview = append(p.ListPreview, TrelloListPreview{Name: cleanText(firstOf(string(l.Name), "Untitled list"), 120), Archived: bool(l.Closed), Titles: []string{}})
	}
	for i, l := range t.Lists {
		byList[string(l.ID)] = &p.ListPreview[i]
	}
	images := 0
	for _, c := range t.Cards {
		p.Cards++
		if c.Closed {
			p.CardsArchived++
		}
		if lp := byList[string(c.IDList)]; lp != nil {
			if c.Closed {
				lp.ArchivedCards++
			} else {
				lp.Cards++
			}
			if !c.Closed && len(lp.Titles) < 3 {
				lp.Titles = append(lp.Titles, cleanText(firstOf(string(c.Name), "Untitled card"), 80))
			}
		}
		for _, a := range c.Attachments.V {
			if trelloLinkOK(string(a.URL)) {
				p.Attachments++
			}
		}
		if c.Cover.V.Color != "" {
			p.Covers++
		} else if c.Cover.V.IDAttachment != "" {
			images++
		}
		p.Checklists += len(t.Checklists[string(c.ID)])
		for _, cl := range t.Checklists[string(c.ID)] {
			for _, it := range cl.CheckItems.V {
				if strings.TrimSpace(string(it.Name)) != "" {
					p.Items++
				}
			}
		}
		p.Comments += len(t.Comments[string(c.ID)])
	}
	p.Labels = len(trelloLabels(t.Labels, boardColorOK))
	p.Fields = len(trelloFields(t.Fields))
	for _, m := range t.Members {
		if m.ID == "" {
			continue
		}
		g, why := guessUser(m, us)
		p.Members = append(p.Members, TrelloMember{ID: string(m.ID), Name: cleanText(string(m.FullName), 120), Username: cleanText(string(m.Username), 60), Type: firstOf(t.MemberType[string(m.ID)], "normal"), Guess: g, Why: why})
	}
	p.Notes = append(p.Notes, "The board is made private: you are its admin, and the people you map become its members (Trello admins as admins, observers as observers). They get one notice that they were added; nothing else in the import notifies anyone.")
	if p.Comments > 0 {
		p.Notes = append(p.Notes, "Comments are posted by you, each saying who wrote it in Trello and when.")
	}
	if p.Attachments > 0 {
		p.Notes = append(p.Notes, "Attachments are added as links to their Trello addresses. The files are not copied, so opening one needs access to the Trello board.")
	}
	if images > 0 {
		p.Notes = append(p.Notes, fmt.Sprintf("%s with an image cover get no cover: covers keep their colour only.", nPlural(images, "card")))
	}
	p.Notes = append(p.Notes, "Power-Ups, automation, stickers and votes are not imported.")
	p.Notes = append(p.Notes, t.Notes...)
	return p, nil
}

func trelloLinkOK(u string) bool {
	return (strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) && len(u) <= 2000 && !strings.ContainsAny(u, " \n\r\t\"<>")
}

// trelloLabels are the export's labels on Ledgerline's palette (a label
// with neither a name nor a colour is left out), with their Trello IDs.
func trelloLabels(ls []tLabel, ok func(string) bool) []struct {
	From string
	BoardLabel
} {
	var out []struct {
		From string
		BoardLabel
	}
	for _, l := range ls {
		if len(out) >= trelloMaxLabels || l.ID == "" || (l.Name == "" && l.Color == "") {
			continue
		}
		out = append(out, struct {
			From string
			BoardLabel
		}{string(l.ID), BoardLabel{ID: boardID("LB-", 4), Name: cleanText(string(l.Name), 60), Color: trelloColor(string(l.Color), ok)}})
	}
	return out
}

// trelloField is a Trello custom field as a Ledgerline one.
type trelloField struct {
	From    string
	Field   BoardField
	Options map[string]string // Trello option id → text
}

func trelloFields(fs []tField) []trelloField {
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Pos < fs[j].Pos })
	var out []trelloField
	seen := map[string]bool{}
	for _, f := range fs {
		typ := strings.ToLower(string(f.Type))
		name := cleanText(string(f.Name), 60)
		if f.ID == "" || name == "" || !contains([]string{"text", "number", "date", "checkbox", "list"}, typ) {
			continue
		}
		tf := trelloField{From: string(f.ID), Field: BoardField{ID: boardID("F-", 4), Name: name, Type: typ, OnFront: bool(f.Display.V["cardFront"])}, Options: map[string]string{}}
		if typ == "list" {
			opts := f.Options.V
			sort.SliceStable(opts, func(i, j int) bool { return opts[i].Pos < opts[j].Pos })
			for _, op := range opts {
				v := cleanText(string(op.Value.V["text"]), 200)
				if v == "" || seen[f.Name.lower()+"|"+v] {
					continue
				}
				seen[f.Name.lower()+"|"+v] = true
				tf.Field.Options = append(tf.Field.Options, v)
				tf.Options[string(op.ID)] = v
			}
			if len(tf.Field.Options) == 0 {
				continue
			}
		}
		out = append(out, tf)
	}
	return out
}

func (s tStr) lower() string { return strings.ToLower(string(s)) }

// trelloFieldValue is a card's value of a custom field, as Ledgerline keeps it.
func trelloFieldValue(f trelloField, it tFieldItem) string {
	v := it.Value.V
	var s string
	switch f.Field.Type {
	case "text":
		s = cleanText(string(v["text"]), 500)
	case "number":
		s = strings.TrimSpace(string(v["number"]))
		if fl, err := strconv.ParseFloat(s, 64); err == nil && !numRe.MatchString(s) {
			s = strconv.FormatFloat(fl, 'f', -1, 64)
		}
	case "date":
		s = trelloDate(string(v["date"]), false)
	case "checkbox":
		s = map[bool]string{true: "true", false: ""}[strings.EqualFold(string(v["checked"]), "true")]
	case "list":
		s = f.Options[string(it.IDValue)]
	}
	out, err := fieldValue(f.Field, s)
	if err != nil {
		return ""
	}
	return out
}

// ---- the import ----

// TrelloImportInput is an import: the export itself, the board's name and
// who each Trello member is in Ledgerline ("" leaves them off).
type TrelloImportInput struct {
	Name    string            `json:"name"`
	Members map[string]string `json:"members"`
	Trello  json.RawMessage   `json:"trello"`
}

// TrelloImportResult is what an import made.
type TrelloImportResult struct {
	Board       Board    `json:"board"`
	Lists       int      `json:"lists"`
	Cards       int      `json:"cards"`
	Checklists  int      `json:"checklists"`
	Items       int      `json:"items"`
	Labels      int      `json:"labels"`
	Fields      int      `json:"fields"`
	Comments    int      `json:"comments"`
	Attachments int      `json:"attachments"`
	Members     int      `json:"members"`
	Notes       []string `json:"notes"`
}

// ImportTrello makes a private team board from a Trello board export.
func (o *Ops) ImportTrello(ctx context.Context, tenant, actor string, in TrelloImportInput) (TrelloImportResult, error) {
	me, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return TrelloImportResult{}, err
	}
	t, err := parseTrello(in.Trello)
	if err != nil {
		return TrelloImportResult{}, err
	}
	name := cleanText(firstOf(strings.TrimSpace(in.Name), firstOf(t.Name, "Board from Trello")), 120)
	// Who each Trello member is: only active staff, each checked.
	staff := map[string]User{}
	for _, u := range o.importUsers(ctx, tenant) {
		staff[u.ID] = u
	}
	who := map[string]string{}
	var people []BoardPerson
	added := map[string]bool{me.ID: true}
	for _, m := range t.Members {
		uid := strings.TrimSpace(in.Members[string(m.ID)])
		if uid == "" {
			continue
		}
		if _, ok := staff[uid]; !ok {
			return TrelloImportResult{}, invalid("%s is not an active staff user; choose someone else for %s or leave them off", uid, firstOf(string(m.FullName), string(m.Username)))
		}
		who[string(m.ID)] = uid
		if added[uid] {
			continue
		}
		added[uid] = true
		role := map[string]string{"admin": BoardAdmin, "observer": BoardObserver}[t.MemberType[string(m.ID)]]
		people = append(people, BoardPerson{User: uid, Role: firstOf(role, BoardMember)})
	}
	var names []string
	for _, l := range t.Lists {
		names = append(names, cleanText(firstOf(strings.TrimSpace(string(l.Name)), "Untitled list"), 120))
	}
	if len(names) == 0 {
		names = []string{"Imported cards"}
	}
	desc := cleanText(t.Desc, 4000)
	b, err := o.CreateBoard(ctx, tenant, actor, BoardInput{Name: name, Description: &desc, Visibility: "private", Background: t.Background, Lists: names, Members: people})
	if err != nil {
		return TrelloImportResult{}, err
	}
	res := TrelloImportResult{Lists: len(t.Lists), Members: len(people), Notes: []string{}}
	listID := map[string]string{}
	labelID := map[string]string{}
	labels := trelloLabels(t.Labels, boardColorOK)
	fields := trelloFields(t.Fields)
	fieldBy := map[string]trelloField{}
	for _, f := range fields {
		fieldBy[f.From] = f
	}
	b, err = docs.Update(ctx, o.Docs, tenant, cBoard, b.ID, actor, func(x *Board) error {
		for i := range x.Lists {
			if i < len(t.Lists) {
				listID[string(t.Lists[i].ID)] = x.Lists[i].ID
				x.Lists[i].Archived = bool(t.Lists[i].Closed)
			}
		}
		x.Labels, x.Fields = nil, nil
		for _, l := range labels {
			labelID[l.From] = l.ID
			x.Labels = append(x.Labels, l.BoardLabel)
		}
		for _, f := range fields {
			x.Fields = append(x.Fields, f.Field)
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	res.Labels, res.Fields = len(labels), len(fields)
	// Card numbers: Trello's own when they are all different, else in order.
	nos, used := map[string]int64{}, map[int64]bool{}
	unique := true
	for _, c := range t.Cards {
		n := int64(c.IDShort)
		if n <= 0 || float64(n) != float64(c.IDShort) || n > 1e9 || used[n] {
			unique = false
			break
		}
		used[n] = true
	}
	var seq int64
	for _, c := range t.Cards {
		if unique {
			nos[string(c.ID)] = int64(c.IDShort)
			seq = max(seq, int64(c.IDShort))
		} else {
			seq++
			nos[string(c.ID)] = seq
		}
	}
	now := o.now()
	stamp := now.Format(time.RFC3339)
	pos := map[string]float64{}
	tick := 0
	for _, tc := range t.Cards {
		lid := listID[string(tc.IDList)]
		if lid == "" {
			continue
		}
		pos[lid] += 1024
		c := Card{Board: b.ID, ID: boardID("C-", 6), No: nos[string(tc.ID)], List: lid, Pos: pos[lid], Title: cleanText(firstOf(strings.TrimSpace(string(tc.Name)), "Untitled card"), 500),
			Description: cleanText(string(tc.Desc), 20000), Due: trelloDate(string(tc.Due), true), Start: trelloDate(string(tc.Start), false), DueDone: bool(tc.DueComplete) && tc.Due != "",
			Archived: bool(tc.Closed), CreatedBy: actor, CreatedAt: stamp, UpdatedAt: stamp}
		for _, l := range tc.IDLabels {
			if id := labelID[l]; id != "" && !contains(c.Labels, id) {
				c.Labels = append(c.Labels, id)
			}
		}
		for _, m := range tc.IDMembers {
			if u := who[m]; u != "" && !contains(c.Members, u) {
				c.Members = append(c.Members, u)
			}
		}
		if cv := tc.Cover.V; cv.Color != "" {
			if col := trelloColor(string(cv.Color), boardColorOK); col != "" {
				c.Cover = &CardCover{Color: col, Full: strings.EqualFold(string(cv.Size), "full")}
			}
		}
		for _, f := range tc.FieldItems.V {
			tf, ok := fieldBy[string(f.IDCustomField)]
			if !ok {
				continue
			}
			if v := trelloFieldValue(tf, f); v != "" {
				if c.Fields == nil {
					c.Fields = map[string]string{}
				}
				c.Fields[tf.Field.ID] = v
			}
		}
		for _, cl := range t.Checklists[string(tc.ID)] {
			ncl := Checklist{ID: boardID("CL-", 4), Name: cleanText(firstOf(strings.TrimSpace(string(cl.Name)), "Checklist"), 200), Items: []CheckItem{}}
			for _, it := range cl.CheckItems.V {
				text := cleanText(string(it.Name), 500)
				if text == "" {
					continue
				}
				ncl.Items = append(ncl.Items, CheckItem{ID: boardID("I-", 4), Text: text, Done: strings.EqualFold(string(it.State), "complete"), Member: who[string(it.IDMember)], Due: trelloDate(string(it.Due), true)})
			}
			res.Items += len(ncl.Items)
			c.Checklists = append(c.Checklists, ncl)
		}
		res.Checklists += len(c.Checklists)
		for _, a := range tc.Attachments.V {
			if len(c.Attachments) >= trelloMaxAttach || !trelloLinkOK(string(a.URL)) {
				continue
			}
			c.Attachments = append(c.Attachments, CardAttachment{ID: boardID("A-", 4), Name: cleanText(firstOf(strings.TrimSpace(string(a.Name)), string(a.URL)), 200), URL: string(a.URL), By: actor, At: stamp})
		}
		res.Attachments += len(c.Attachments)
		for _, cm := range t.Comments[string(tc.ID)] {
			// Posted by the person importing, saying who wrote it in Trello
			// and when; a millisecond apart, so they stay in order.
			tick++
			c.Comments = append(c.Comments, CardComment{ID: boardID("CM-", 5), By: actor, At: now.Add(time.Duration(tick) * time.Millisecond).Format(time.RFC3339Nano), Text: trelloCommentText(cm)})
		}
		res.Comments += len(c.Comments)
		if _, err := docs.Put(ctx, o.Docs, tenant, cBoardCard, cardKey(b.ID, c.ID), c, 0, actor); err != nil {
			return res, err
		}
		res.Cards++
	}
	b, err = docs.Update(ctx, o.Docs, tenant, cBoard, b.ID, actor, func(x *Board) error {
		x.CardSeq = max(x.CardSeq, seq)
		x.UpdatedAt = o.now().Format(time.RFC3339)
		return nil
	})
	if err != nil {
		return res, err
	}
	res.Board = b
	res.Notes = append(res.Notes, t.Notes...)
	if res.Attachments > 0 {
		res.Notes = append(res.Notes, "Attachments were added as links to Trello; the files were not copied.")
	}
	sum := fmt.Sprintf("imported this board from Trello (%s): %s, %s, %s, %s", t.Name, nPlural(res.Lists, "list"), nPlural(res.Cards, "card"), nPlural(res.Checklists, "checklist"), nPlural(res.Comments, "comment"))
	o.boardLog(ctx, tenant, actor, b, "", "board.imported", clip(sum, 400), map[string]string{"from": "trello", "cards": fmt.Sprint(res.Cards)})
	o.log(ctx, tenant, actor, "board.imported", b.ID, map[string]any{"from": "trello", "lists": res.Lists, "cards": res.Cards, "checklists": res.Checklists, "comments": res.Comments, "attachments": res.Attachments, "members": res.Members})
	// The people mapped hear that they are on the board; nothing else of
	// the import notifies anyone.
	var mapped []string
	for _, p := range people {
		mapped = append(mapped, p.User)
	}
	o.boardNotifyKind(ctx, tenant, actor, b, "", NoticeAdded, mapped, fmt.Sprintf("%s imported the Trello board %s and added you to it", actor, clip(b.Name, 80)))
	return res, nil
}

// trelloCommentText is a Trello comment as posted by the importer.
func trelloCommentText(cm trelloComment) string {
	who := firstOf(strings.TrimSpace(cm.Name), "someone")
	if u := strings.TrimSpace(cm.Username); u != "" {
		who += " (@" + clip(u, 40) + ")"
	}
	when := ""
	if t, err := time.Parse(time.RFC3339, cm.Date); err == nil {
		when = " on " + t.In(ist).Format("2 Jan 2006 at 15:04") + " IST"
	}
	head := "Imported from Trello: written by " + clip(who, 160) + when + "."
	return head + "\n\n" + cleanText(cm.Text, 5000-len([]rune(head))-2)
}

// ---- export ----

// BoardExport is Ledgerline's own export of a board.
type BoardExport struct {
	Format     string          `json:"format"` // ledgerline-board
	Version    int             `json:"version"`
	ExportedAt string          `json:"exported_at"`
	ExportedBy string          `json:"exported_by"`
	Board      Board           `json:"board"`
	People     []BoardUserRef  `json:"people"` // names of the people the board refers to
	Cards      []Card          `json:"cards"`  // archived ones too
	Activity   []BoardActivity `json:"activity"`
}

// CardExport is Ledgerline's own export of one card.
type CardExport struct {
	Format     string          `json:"format"` // ledgerline-card
	Version    int             `json:"version"`
	ExportedAt string          `json:"exported_at"`
	ExportedBy string          `json:"exported_by"`
	Board      BoardUserRef    `json:"board"` // its id and name
	List       string          `json:"list"`
	Labels     []BoardLabel    `json:"labels"`
	Fields     []BoardField    `json:"fields"`
	People     []BoardUserRef  `json:"people"`
	Card       Card            `json:"card"`
	Activity   []BoardActivity `json:"activity"`
}

// exportBoard is a board as an export shows it: no email key or stars.
func exportBoard(b Board) Board {
	b.EmailKey, b.Starred = "", nil
	return b
}

func (o *Ops) exportPeople(ctx context.Context, tenant string, b Board, cs []Card) []BoardUserRef {
	us, _ := o.Users(ctx, tenant)
	want := map[string]bool{b.CreatedBy: true}
	for _, m := range b.Members {
		want[m.User] = true
	}
	for _, c := range cs {
		for _, xs := range [][]string{c.Members, c.Watchers, c.Votes, {c.CreatedBy}} {
			for _, x := range xs {
				want[x] = true
			}
		}
		for _, cm := range c.Comments {
			want[cm.By] = true
		}
		for _, cl := range c.Checklists {
			for _, it := range cl.Items {
				want[it.Member] = true
			}
		}
	}
	out := []BoardUserRef{}
	for _, u := range us {
		if want[u.ID] {
			out = append(out, BoardUserRef{ID: u.ID, Name: u.Name, Role: u.Role})
		}
	}
	return out
}

func (o *Ops) allActivity(ctx context.Context, tenant, board, card string) []BoardActivity {
	as, _ := docs.List[BoardActivity](ctx, o.Docs, tenant, cBoardAct, board+"|")
	out := []BoardActivity{}
	for _, a := range as {
		if card == "" || a.Card == card {
			out = append(out, a)
		}
	}
	return out
}

// ExportBoardJSON is a board with every card, archived ones too, and its
// activity (board admins).
func (o *Ops) ExportBoardJSON(ctx context.Context, tenant, actor, id string) (BoardExport, error) {
	b, v, err := o.boardFor(ctx, tenant, actor, id)
	if err != nil {
		return BoardExport{}, err
	}
	if !v.can(BoardAdmin) {
		return BoardExport{}, fmt.Errorf("%w: only a board admin can export the whole board (your role on it: %s); members can export its cards as CSV", ErrForbidden, v.board)
	}
	cs, err := o.boardCards(ctx, tenant, id)
	if err != nil {
		return BoardExport{}, err
	}
	out := BoardExport{Format: "ledgerline-board", Version: 1, ExportedAt: o.now().Format(time.RFC3339), ExportedBy: actor, Board: exportBoard(b), People: o.exportPeople(ctx, tenant, b, cs), Cards: append([]Card{}, cs...)}
	out.Activity = o.allActivity(ctx, tenant, id, "")
	o.boardLog(ctx, tenant, actor, b, "", "board.exported", fmt.Sprintf("exported the board as JSON (%s)", nPlural(len(cs), "card")), map[string]string{"format": "json"})
	o.log(ctx, tenant, actor, "board.exported", b.ID, map[string]any{"format": "json", "cards": len(cs)})
	return out, nil
}

// boardCSVCell keeps a spreadsheet from reading a cell as a formula, as
// the console's CSV does (bvCsvCell in web/boardviews.js): text starting
// =, +, -, @, a tab or a carriage return gets a ' in front, unless it is
// a plain number.
func boardCSVCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) && !numRe.MatchString(s) {
		return "'" + s
	}
	return s
}

// ExportBoardCSV is every card of a board, archived ones too, one a row
// (board admins and members).
func (o *Ops) ExportBoardCSV(ctx context.Context, tenant, actor, id string) ([]byte, string, error) {
	b, v, err := o.boardFor(ctx, tenant, actor, id)
	if err != nil {
		return nil, "", err
	}
	if !v.can(BoardAdmin, BoardMember) {
		return nil, "", fmt.Errorf("%w: only the board's members can export its cards (your role on it: %s)", ErrForbidden, v.board)
	}
	cs, err := o.boardCards(ctx, tenant, id)
	if err != nil {
		return nil, "", err
	}
	name := o.boardNamer(ctx, tenant)
	listPos := map[string]float64{}
	for _, l := range b.Lists {
		listPos[l.ID] = l.Pos
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Archived != cs[j].Archived {
			return !cs[i].Archived
		}
		if pi, pj := listPos[cs[i].List], listPos[cs[j].List]; pi != pj {
			return pi < pj
		}
		return cs[i].Pos < cs[j].Pos
	})
	var buf bytes.Buffer
	buf.WriteString("\ufeff")
	w := csv.NewWriter(&buf)
	w.UseCRLF = true
	head := []string{"Card", "Title", "List", "Labels", "Members", "Start", "Due", "Due complete", "Checklist items done", "Checklist items", "Description", "Comments", "Attachments", "Stickers", "Linked record", "Archived", "Created", "Created by", "Updated"}
	for _, f := range b.Fields {
		head = append(head, f.Name)
	}
	row := func(r []string) {
		for i := range r {
			r[i] = boardCSVCell(r[i])
		}
		_ = w.Write(r)
	}
	row(head)
	yn := map[bool]string{true: "Yes", false: "No"}
	for _, c := range cs {
		l, _ := boardList(b, c.List)
		list := l.Name
		if l.Archived {
			list += " (archived list)"
		}
		var labels, members []string
		for _, id := range c.Labels {
			if lb, ok := boardLabel(b, id); ok {
				labels = append(labels, firstOf(lb.Name, lb.Color))
			}
		}
		for _, m := range c.Members {
			members = append(members, name(m))
		}
		done, items := 0, 0
		for _, cl := range c.Checklists {
			for _, it := range cl.Items {
				items++
				if it.Done {
					done++
				}
			}
		}
		link := ""
		if c.Link != nil {
			link = c.Link.Label
		}
		r := []string{fmt.Sprintf("#%d", c.No), c.Title, list, strings.Join(labels, ", "), strings.Join(members, ", "), c.Start, c.Due, map[bool]string{true: "Yes", false: ""}[c.Due != "" && c.DueDone],
			fmt.Sprint(done), fmt.Sprint(items), c.Description, fmt.Sprint(len(c.Comments)), fmt.Sprint(len(c.Attachments)), strings.Join(c.Stickers, ", "), link, yn[c.Archived], c.CreatedAt, name(c.CreatedBy), c.UpdatedAt}
		for _, f := range b.Fields {
			v := c.Fields[f.ID]
			if f.Type == "checkbox" && v == "true" {
				v = "Yes"
			}
			r = append(r, v)
		}
		row(r)
	}
	w.Flush()
	o.boardLog(ctx, tenant, actor, b, "", "board.exported", fmt.Sprintf("exported the board's cards as CSV (%s)", nPlural(len(cs), "card")), map[string]string{"format": "csv"})
	o.log(ctx, tenant, actor, "board.exported", b.ID, map[string]any{"format": "csv", "cards": len(cs)})
	return buf.Bytes(), exportFileName(b.Name) + ".csv", w.Error()
}

// exportFileName is a board's name as a file name.
func exportFileName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, strings.TrimSpace(s))
	s = strings.Trim(regexp.MustCompile(`-+`).ReplaceAllString(s, "-"), "-")
	return firstOf(clip(s, 60), "board")
}

// ExportCardJSON is one card with its board's labels and fields and its
// activity (anyone who can see the board).
func (o *Ops) ExportCardJSON(ctx context.Context, tenant, actor, board, card string) (CardExport, error) {
	c, b, err := o.GetCard(ctx, tenant, actor, board, card)
	if err != nil {
		return CardExport{}, err
	}
	l, _ := boardList(b, c.List)
	out := CardExport{Format: "ledgerline-card", Version: 1, ExportedAt: o.now().Format(time.RFC3339), ExportedBy: actor, Board: BoardUserRef{ID: b.ID, Name: b.Name}, List: l.Name,
		Labels: []BoardLabel{}, Fields: []BoardField{}, People: o.exportPeople(ctx, tenant, Board{}, []Card{c}), Card: c, Activity: o.allActivity(ctx, tenant, b.ID, c.ID)}
	for _, id := range c.Labels {
		if lb, ok := boardLabel(b, id); ok {
			out.Labels = append(out.Labels, lb)
		}
	}
	for _, f := range b.Fields {
		if c.Fields[f.ID] != "" {
			out.Fields = append(out.Fields, f)
		}
	}
	o.boardLog(ctx, tenant, actor, b, c.ID, "card.exported", fmt.Sprintf("exported #%d as JSON", c.No), map[string]string{"format": "json"})
	return out, nil
}

// BoardPrinted writes a print of a board, or of one of its cards, to its
// activity (the print view itself is drawn in the console).
func (o *Ops) BoardPrinted(ctx context.Context, tenant, actor, board, card string) error {
	b, _, err := o.boardFor(ctx, tenant, actor, board)
	if err != nil {
		return err
	}
	if card == "" {
		o.boardLog(ctx, tenant, actor, b, "", "board.printed", "printed the board", nil)
		return nil
	}
	c, _, err := o.GetCard(ctx, tenant, actor, board, card)
	if err != nil {
		return err
	}
	o.boardLog(ctx, tenant, actor, b, c.ID, "card.printed", fmt.Sprintf("printed #%d", c.No), nil)
	return nil
}

// ---- recently viewed ----

type boardRecentDoc struct {
	User   string   `json:"user"`
	Boards []string `json:"boards"` // most recent first
	At     string   `json:"at"`
}

// updateOr changes a document, making it from def when there is none.
func updateOr[T any](ctx context.Context, s docs.Store, tenant, coll, id, actor string, def func() T, fn func(*T) error) (T, error) {
	for i := 0; i < 5; i++ {
		v, ver, err := docs.GetOr(ctx, s, tenant, coll, id, def())
		if err != nil {
			return v, err
		}
		if err := fn(&v); err != nil {
			return v, err
		}
		if _, err := docs.Put(ctx, s, tenant, coll, id, v, ver, actor); err != nil {
			if errors.Is(err, docs.ErrStale) {
				continue
			}
			return v, err
		}
		return v, nil
	}
	var zero T
	return zero, docs.ErrStale
}

// BoardViewed notes that the person opened a board.
func (o *Ops) BoardViewed(ctx context.Context, tenant, actor, id string) error {
	if _, _, err := o.boardFor(ctx, tenant, actor, id); err != nil {
		return err
	}
	_, err := updateOr(ctx, o.Docs, tenant, cBoardRecent, actor, actor, func() boardRecentDoc { return boardRecentDoc{User: actor} }, func(d *boardRecentDoc) error {
		d.Boards = append([]string{id}, toggleIn(d.Boards, id, false)...)
		if len(d.Boards) > boardRecentKept {
			d.Boards = d.Boards[:boardRecentKept]
		}
		d.At = o.now().Format(time.RFC3339)
		return nil
	})
	return err
}

// RecentBoards are the last open boards the person viewed and can still see.
func (o *Ops) RecentBoards(ctx context.Context, tenant, actor string) ([]BoardSummary, error) {
	bs, err := o.Boards(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	d, _, err := docs.GetOr(ctx, o.Docs, tenant, cBoardRecent, actor, boardRecentDoc{})
	if err != nil {
		return nil, err
	}
	by := map[string]BoardSummary{}
	for _, b := range bs {
		by[b.ID] = b
	}
	out := []BoardSummary{}
	for _, id := range d.Boards {
		if b, ok := by[id]; ok && !b.Closed && len(out) < boardRecentShown {
			out = append(out, b)
		}
	}
	return out, nil
}

// ---- collections ----

// BoardCollection is a firm-wide group of boards.
type BoardCollection struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Boards    []string `json:"boards"`
	CreatedBy string   `json:"created_by"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	Deleted   bool     `json:"deleted,omitempty"`
}

// BoardCollectionView is a collection as one person sees it: only the
// boards they can see.
type BoardCollectionView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Boards    []string `json:"boards"`
	CreatedBy string   `json:"created_by"`
	CanDelete bool     `json:"can_delete"`
}

func headOffice(u User) bool { return u.Role != RoleBranch && u.Role != RoleAP && u.Role != RoleClient }

func (o *Ops) collUser(ctx context.Context, tenant, actor string) (User, error) {
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return u, err
	}
	if !headOffice(u) {
		return u, fmt.Errorf("%w: collections are for head office", ErrForbidden)
	}
	return u, nil
}

func (o *Ops) collView(u User, c BoardCollection, seen map[string]bool) BoardCollectionView {
	v := BoardCollectionView{ID: c.ID, Name: c.Name, Boards: []string{}, CreatedBy: c.CreatedBy, CanDelete: c.CreatedBy == u.ID || u.Role == RoleAdmin}
	for _, b := range c.Boards {
		if seen[b] {
			v.Boards = append(v.Boards, b)
		}
	}
	return v
}

func (o *Ops) visibleBoards(ctx context.Context, tenant, actor string) map[string]bool {
	bs, _ := o.Boards(ctx, tenant, actor)
	seen := map[string]bool{}
	for _, b := range bs {
		seen[b.ID] = true
	}
	return seen
}

// BoardCollections are the firm's collections, by name, each with the
// boards in it the person can see. Branch and partner staff have none.
func (o *Ops) BoardCollections(ctx context.Context, tenant, actor string) ([]BoardCollectionView, error) {
	u, err := o.boardUser(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	out := []BoardCollectionView{}
	if !headOffice(u) {
		return out, nil
	}
	cs, err := docs.List[BoardCollection](ctx, o.Docs, tenant, cBoardColl, "")
	if err != nil {
		return nil, err
	}
	seen := o.visibleBoards(ctx, tenant, actor)
	for _, c := range cs {
		if !c.Deleted {
			out = append(out, o.collView(u, c, seen))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// SaveBoardCollection makes a collection (id "") or renames one (anyone
// at head office).
func (o *Ops) SaveBoardCollection(ctx context.Context, tenant, actor, id, name string) (BoardCollectionView, error) {
	u, err := o.collUser(ctx, tenant, actor)
	if err != nil {
		return BoardCollectionView{}, err
	}
	n, err := textField("collection name", name, 60, true)
	if err != nil {
		return BoardCollectionView{}, err
	}
	cs, err := docs.List[BoardCollection](ctx, o.Docs, tenant, cBoardColl, "")
	if err != nil {
		return BoardCollectionView{}, err
	}
	live := 0
	for _, c := range cs {
		if c.Deleted {
			continue
		}
		live++
		if c.ID != id && strings.EqualFold(c.Name, n) {
			return BoardCollectionView{}, invalid("there is already a collection called %s", c.Name)
		}
	}
	now := o.now().Format(time.RFC3339)
	var c BoardCollection
	if id == "" {
		if live >= boardMaxColls {
			return BoardCollectionView{}, invalid("the firm can have at most %d collections", boardMaxColls)
		}
		c = BoardCollection{ID: boardID("BC-", 5), Name: n, Boards: []string{}, CreatedBy: actor, CreatedAt: now, UpdatedAt: now}
		if _, err := docs.Put(ctx, o.Docs, tenant, cBoardColl, c.ID, c, 0, actor); err != nil {
			return BoardCollectionView{}, err
		}
		o.log(ctx, tenant, actor, "board.collection_created", c.ID, map[string]any{"name": n})
	} else {
		old := ""
		c, err = docs.Update(ctx, o.Docs, tenant, cBoardColl, id, actor, func(x *BoardCollection) error {
			if x.Deleted {
				return fmt.Errorf("%w: no such collection", ErrNotFound)
			}
			old, x.Name, x.UpdatedAt = x.Name, n, now
			return nil
		})
		if err != nil {
			if errors.Is(err, docs.ErrNotFound) {
				return BoardCollectionView{}, fmt.Errorf("%w: no such collection", ErrNotFound)
			}
			return BoardCollectionView{}, err
		}
		o.log(ctx, tenant, actor, "board.collection_renamed", c.ID, map[string]any{"from": old, "to": n})
	}
	return o.collView(u, c, o.visibleBoards(ctx, tenant, actor)), nil
}

// DeleteBoardCollection deletes a collection (the person who made it, or a
// firm Admin); its boards stay as they are.
func (o *Ops) DeleteBoardCollection(ctx context.Context, tenant, actor, id string) error {
	u, err := o.collUser(ctx, tenant, actor)
	if err != nil {
		return err
	}
	c, err := docs.Update(ctx, o.Docs, tenant, cBoardColl, id, actor, func(x *BoardCollection) error {
		if x.Deleted {
			return fmt.Errorf("%w: no such collection", ErrNotFound)
		}
		if x.CreatedBy != u.ID && u.Role != RoleAdmin {
			return fmt.Errorf("%w: only the person who made a collection, or an Admin, can delete it", ErrForbidden)
		}
		x.Deleted, x.UpdatedAt = true, o.now().Format(time.RFC3339)
		return nil
	})
	if errors.Is(err, docs.ErrNotFound) {
		return fmt.Errorf("%w: no such collection", ErrNotFound)
	}
	if err == nil {
		o.log(ctx, tenant, actor, "board.collection_deleted", id, map[string]any{"name": c.Name})
	}
	return err
}

// SetBoardInCollection adds a board to a collection or takes it out (the
// board's admins).
func (o *Ops) SetBoardInCollection(ctx context.Context, tenant, actor, id, board string, on bool) (BoardCollectionView, error) {
	u, err := o.collUser(ctx, tenant, actor)
	if err != nil {
		return BoardCollectionView{}, err
	}
	b, _, err := o.boardFor(ctx, tenant, actor, board, BoardAdmin)
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			return BoardCollectionView{}, fmt.Errorf("%w: only a board's admins can add it to a collection or take it out", ErrForbidden)
		}
		return BoardCollectionView{}, err
	}
	changed := false
	c, err := docs.Update(ctx, o.Docs, tenant, cBoardColl, id, actor, func(x *BoardCollection) error {
		if x.Deleted {
			return fmt.Errorf("%w: no such collection", ErrNotFound)
		}
		changed = contains(x.Boards, board) != on
		if on && changed && len(x.Boards) >= boardCollMax {
			return invalid("a collection can hold at most %d boards", boardCollMax)
		}
		x.Boards = toggleIn(x.Boards, board, on)
		if x.Boards == nil {
			x.Boards = []string{}
		}
		if changed {
			x.UpdatedAt = o.now().Format(time.RFC3339)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, docs.ErrNotFound) {
			return BoardCollectionView{}, fmt.Errorf("%w: no such collection", ErrNotFound)
		}
		return BoardCollectionView{}, err
	}
	if changed {
		o.boardLog(ctx, tenant, actor, b, "", "board.collection", map[bool]string{true: "added this board to the collection ", false: "took this board out of the collection "}[on]+c.Name, map[string]string{"collection": c.ID})
	}
	return o.collView(u, c, o.visibleBoards(ctx, tenant, actor)), nil
}
