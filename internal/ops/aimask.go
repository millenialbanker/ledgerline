package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ---- masking before any AI call ----
//
// Text sent to an AI provider has the identifiers in it replaced by stable
// tokens ("[PAN-1]", "[CLIENT-2]"), and the tokens in the answer are put
// back before a person sees it. The same value gets the same token within
// one call, so the model can still say "the client [CLIENT-1] with PAN
// [PAN-1]". Names come from the firm's own client and staff lists;
// identifiers are found by their shape:
//
//	PAN            5 letters, 4 digits, 1 letter
//	Aadhaar        12 digits, first 2–9, optionally in groups of 4
//	phone          Indian mobiles (10 digits from 6–9, optionally +91 or 0)
//	email          anything@domain.tld
//	bank account   9 to 18 digits (also catches demat and other long numbers)
//	client code    the firm's UCCs
//	client name    the firm's clients (full name, or first and last)
//	staff name     the firm's users (full name, first and last, user ID,
//	               and a capitalised first name)
//
// Masking is on for every category by default. An Admin may turn off the
// name, code, phone and email categories (for example when a model needs
// a name to answer); PAN, Aadhaar and bank account numbers are always
// masked. The answer log always stores the fully masked text.

// Mask categories.
const (
	AIMaskPAN        = "pan"
	AIMaskAadhaar    = "aadhaar"
	AIMaskAccount    = "account"
	AIMaskPhone      = "phone"
	AIMaskEmail      = "email"
	AIMaskClientCode = "client_code"
	AIMaskClientName = "client_name"
	AIMaskStaffName  = "staff_name"
)

// maskOrder is the order categories are applied: emails first (they hold
// names and digits), then fixed shapes, then the firm's lists.
var maskOrder = []string{AIMaskEmail, AIMaskPAN, AIMaskAadhaar, AIMaskPhone, AIMaskAccount, AIMaskClientCode, AIMaskClientName, AIMaskStaffName}

var maskLocked = map[string]bool{AIMaskPAN: true, AIMaskAadhaar: true, AIMaskAccount: true}

var maskInfo = map[string]struct{ Label, Token string }{
	AIMaskPAN:        {"PAN", "PAN"},
	AIMaskAadhaar:    {"Aadhaar-like numbers (12 digits)", "AADHAAR"},
	AIMaskAccount:    {"Bank account and other long numbers (9–18 digits)", "ACCOUNT"},
	AIMaskPhone:      {"Phone numbers", "PHONE"},
	AIMaskEmail:      {"Email addresses", "EMAIL"},
	AIMaskClientCode: {"Client codes (UCC)", "CLIENT-CODE"},
	AIMaskClientName: {"Client names", "CLIENT"},
	AIMaskStaffName:  {"Staff names and user IDs", "STAFF"},
}

var (
	reMaskEmail2  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	reMaskPAN2    = regexp.MustCompile(`\b[A-Za-z]{5}[0-9]{4}[A-Za-z]\b`)
	reMaskAadhaar = regexp.MustCompile(`\b[2-9][0-9]{3}[ -]?[0-9]{4}[ -]?[0-9]{4}\b`)
	reMaskPhone   = regexp.MustCompile(`(?:\+91[ -]?|\b0|\b)[6-9][0-9]{4}[ -]?[0-9]{5}\b`)
	reMaskAccount = regexp.MustCompile(`\b[0-9]{9,18}\b`)
	reMaskWord    = regexp.MustCompile(`[\p{L}][\p{L}\p{N}.'’_-]*`)
	reMaskToken   = regexp.MustCompile(`\[(PAN|AADHAAR|ACCOUNT|PHONE|EMAIL|CLIENT-CODE|CLIENT|STAFF)-[0-9]+\]`)
)

// AIMaskCategory is one category as the AI controls screen shows it.
type AIMaskCategory struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	On     bool   `json:"on"`
	Locked bool   `json:"locked"`
}

func aiMaskCategories(c AIControls) []AIMaskCategory {
	off := map[string]bool{}
	for _, u := range c.Unmasked {
		off[u] = true
	}
	var out []AIMaskCategory
	for _, id := range maskOrder {
		out = append(out, AIMaskCategory{ID: id, Label: maskInfo[id].Label, On: maskLocked[id] || !off[id], Locked: maskLocked[id]})
	}
	return out
}

// SetAIMasking turns a masking category on or off (Admin only; PAN,
// Aadhaar and bank accounts are always masked).
func (o *Ops) SetAIMasking(ctx context.Context, tenant, actor, category string, on bool) (AIControls, error) {
	if err := o.require(ctx, tenant, actor, "Changing AI masking"); err != nil {
		return AIControls{}, err
	}
	if _, ok := maskInfo[category]; !ok {
		return AIControls{}, invalid("unknown masking category %q", category)
	}
	if maskLocked[category] && !on {
		return AIControls{}, invalid("%s are always masked before an AI call; this cannot be turned off", maskInfo[category].Label)
	}
	now := o.now().Format(time.RFC3339)
	c, err := o.saveAIControls(ctx, tenant, actor, func(c *AIControls) error {
		var keep []string
		for _, u := range c.Unmasked {
			if u != category {
				keep = append(keep, u)
			}
		}
		if !on {
			keep = append(keep, category)
		}
		sort.Strings(keep)
		c.Unmasked = append([]string{}, keep...)
		c.Changes = append(c.Changes, AIControlChange{At: now, By: actor, What: "Masking of " + strings.ToLower(maskInfo[category].Label) + map[bool]string{true: " on", false: " off"}[on]})
		return nil
	})
	if err == nil {
		o.log(ctx, tenant, actor, "ai.masking", category, map[string]any{"on": on})
	}
	return c, err
}

// Masker replaces identifiers with tokens and puts them back.
type Masker struct {
	on     map[string]bool
	names  map[string]string // lower-cased name words → category
	single map[string]string // capitalised single names → category
	codes  map[string]bool   // upper-cased UCCs
	maxN   int
	tok    map[string]string // category|value → token
	back   map[string]string // token → value
	count  map[string]int
	Counts map[string]int // what was masked, by category
}

// MaskLists are the firm's own names and codes.
type MaskLists struct {
	ClientNames []string
	ClientCodes []string
	StaffNames  []string
	StaffIDs    []string
}

// NewMasker builds a masker. off lists categories not to mask (locked
// categories are masked regardless).
func NewMasker(l MaskLists, off []string) *Masker {
	m := &Masker{on: map[string]bool{}, names: map[string]string{}, single: map[string]string{}, codes: map[string]bool{},
		tok: map[string]string{}, back: map[string]string{}, count: map[string]int{}, Counts: map[string]int{}}
	for _, c := range maskOrder {
		m.on[c] = true
	}
	for _, c := range off {
		if !maskLocked[c] {
			m.on[c] = false
		}
	}
	add := func(name, cat string, firstName bool) {
		ws := reMaskWord.FindAllString(name, -1)
		if len(ws) < 2 {
			return // one-word names would hide ordinary words
		}
		m.addName(ws, cat)
		if len(ws) > 2 {
			m.addName([]string{ws[0], ws[len(ws)-1]}, cat)
		}
		if firstName && len([]rune(ws[0])) >= 3 {
			m.single[ws[0]] = cat
		}
	}
	for _, n := range l.ClientNames {
		add(n, AIMaskClientName, false)
	}
	for _, n := range l.StaffNames {
		add(n, AIMaskStaffName, true)
	}
	for _, id := range l.StaffIDs {
		if len(id) >= 3 && strings.ContainsAny(id, "._-") {
			m.names[strings.ToLower(id)] = AIMaskStaffName
		}
	}
	for _, c := range l.ClientCodes {
		if c = strings.ToUpper(strings.TrimSpace(c)); len(c) >= 3 {
			m.codes[c] = true
		}
	}
	return m
}

func (m *Masker) addName(ws []string, cat string) {
	k := strings.ToLower(strings.Join(ws, " "))
	if _, ok := m.names[k]; ok && m.names[k] == AIMaskStaffName {
		return // staff wins when a client has the same name
	}
	m.names[k] = cat
	if len(ws) > m.maxN {
		m.maxN = len(ws)
	}
}

func (m *Masker) token(cat, value string) string {
	key := cat + "|" + strings.ToLower(normSpace(value))
	if t, ok := m.tok[key]; ok {
		m.Counts[cat]++
		return t
	}
	m.count[cat]++
	t := fmt.Sprintf("[%s-%d]", maskInfo[cat].Token, m.count[cat])
	m.tok[key], m.back[t] = t, value
	m.Counts[cat]++
	return t
}

func normSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// Mask replaces the identifiers in s.
func (m *Masker) Mask(s string) string {
	for _, cat := range maskOrder {
		if !m.on[cat] {
			continue
		}
		switch cat {
		case AIMaskEmail:
			s = reMaskEmail2.ReplaceAllStringFunc(s, func(v string) string { return m.token(cat, v) })
		case AIMaskPAN:
			s = reMaskPAN2.ReplaceAllStringFunc(s, func(v string) string { return m.token(cat, strings.ToUpper(v)) })
		case AIMaskAadhaar:
			s = reMaskAadhaar.ReplaceAllStringFunc(s, func(v string) string { return m.token(cat, v) })
		case AIMaskPhone:
			s = reMaskPhone.ReplaceAllStringFunc(s, func(v string) string { return m.token(cat, v) })
		case AIMaskAccount:
			s = reMaskAccount.ReplaceAllStringFunc(s, func(v string) string { return m.token(cat, v) })
		case AIMaskClientCode:
			s = reMaskWord.ReplaceAllStringFunc(s, func(w string) string {
				core := strings.TrimRight(w, ".'’_-")
				if m.codes[strings.ToUpper(core)] {
					return m.token(cat, strings.ToUpper(core)) + w[len(core):]
				}
				return w
			})
		case AIMaskClientName, AIMaskStaffName:
			s = m.maskNames(s, cat)
		}
	}
	return s
}

// maskNames replaces runs of words that make a known name of one category.
func (m *Masker) maskNames(s, cat string) string {
	idx := reMaskWord.FindAllStringIndex(s, -1)
	var b strings.Builder
	last := 0
	for i := 0; i < len(idx); i++ {
		matched := 0
		for n := m.maxN; n >= 1 && matched == 0; n-- {
			if i+n > len(idx) {
				continue
			}
			ok := true
			var parts []string
			for k := i; k < i+n; k++ {
				if k > i && strings.TrimSpace(s[idx[k-1][1]:idx[k][0]]) != "" {
					ok = false
					break
				}
				parts = append(parts, nameCore(s[idx[k][0]:idx[k][1]]))
			}
			if !ok {
				continue
			}
			key := strings.ToLower(strings.Join(parts, " "))
			if c, hit := m.names[key]; hit && c == cat {
				matched = n
			} else if n == 1 && cat == AIMaskStaffName {
				w := parts[0]
				if c, hit := m.single[w]; hit && c == cat && unicode.IsUpper([]rune(w)[0]) {
					matched = 1
				}
			}
		}
		if matched == 0 {
			continue
		}
		lw := idx[i+matched-1]
		start, end := idx[i][0], lw[0]+len(nameCore(s[lw[0]:lw[1]]))
		b.WriteString(s[last:start])
		b.WriteString(m.token(cat, s[start:end]))
		last = end
		i += matched - 1
	}
	b.WriteString(s[last:])
	return b.String()
}

// nameCore is a word without trailing punctuation or a possessive 's.
func nameCore(w string) string {
	w = strings.TrimRight(w, ".'’-")
	for _, suf := range []string{"'s", "’s", "'S", "’S"} {
		if strings.HasSuffix(w, suf) {
			return strings.TrimRight(w[:len(w)-len(suf)], ".'’-")
		}
	}
	return w
}

// Restore puts the masked values back into an answer.
func (m *Masker) Restore(s string) string {
	return reMaskToken.ReplaceAllStringFunc(s, func(t string) string {
		if v, ok := m.back[t]; ok {
			return v
		}
		return t
	})
}

// Summary is what was masked, for the answer log ("PAN ×1, client ×2").
func (m *Masker) Summary() string {
	var out []string
	for _, c := range maskOrder {
		if n := m.Counts[c]; n > 0 {
			out = append(out, fmt.Sprintf("%s ×%d", strings.ToLower(maskInfo[c].Token), n))
		}
	}
	return strings.Join(out, ", ")
}

// aiMaskLists reads the firm's client and staff names and codes.
func (o *Ops) aiMaskLists(ctx context.Context, tenant string) MaskLists {
	var l MaskLists
	if cs, err := o.Clients(ctx, tenant); err == nil {
		for _, c := range cs {
			l.ClientCodes = append(l.ClientCodes, c.UCC)
			l.ClientNames = append(l.ClientNames, c.Name)
			if c.NomineeName != "" {
				l.ClientNames = append(l.ClientNames, c.NomineeName)
			}
		}
	}
	if us, err := o.Users(ctx, tenant); err == nil {
		for _, u := range us {
			if u.Role == RoleClient {
				l.ClientNames = append(l.ClientNames, u.Name)
				continue
			}
			l.StaffNames = append(l.StaffNames, u.Name)
			l.StaffIDs = append(l.StaffIDs, u.ID)
		}
	}
	return l
}

// aiMasker is the masker for a call to a provider, under the firm's policy.
func (o *Ops) aiMasker(ctx context.Context, tenant string) *Masker {
	return NewMasker(o.aiMaskLists(ctx, tenant), o.aiControls(ctx, tenant).Unmasked)
}

// logMask masks text for the answer log: every category, whatever the
// firm's policy, so the log never holds a raw identifier.
func (o *Ops) logMask(l MaskLists, s string) string {
	return NewMasker(l, nil).Mask(s)
}
