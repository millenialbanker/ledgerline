package ops

import (
	"sort"
	"strings"
	"unicode"
)

// ---- name matching for sanctions screening ----
//
// The UN Security Council, UAPA and WMD lists seldom carry an Indian PAN,
// and a designated person's name reaches the list transliterated from
// Arabic, Pashto, Korean or Cyrillic script, often in several spellings
// (Mohammed, Muhammad, Mohamad; Yusuf, Yousef). An exact comparison of
// names misses most of them, so screening scores how close two names are:
//
//   - Both names are normalised: upper case, accents folded to plain
//     letters, punctuation dropped, titles (Mr, Dr, Shri …) and company
//     endings (Pvt, Ltd …) left out.
//   - Each word is compared with the closest word of the other name by
//     Jaro-Winkler similarity, on the word as written and on a
//     transliteration key (vowels after the first letter dropped, KH/Q to
//     K, PH to F, W to V, doubled letters single …), whichever is higher;
//     a single initial matches a word that starts with it.
//   - The words of the shorter name carry the score, weighted by length; a
//     longer name's extra words lower it a little, so "Ali Khan" is not a
//     strong match for every Ali Hassan Khan.
//   - Names written as one word on one side and two on the other
//     (Abdulrahman, Abdul Rahman) are also compared run together.
//
// The score is 0-100. The date of birth and nationality, when both sides
// have them, add to it or take from it (sanctions.go): they are supporting
// evidence, never a match on their own. A PAN match is certain.
//
// SEBI's AML/CFT master circular (SEBI/HO/MIRSD/MIRSDSECFATF/P/CIR/2024/78,
// para 67) asks intermediaries to use current technology for name
// screening; it does not prescribe a method or a threshold, so the
// thresholds are the firm's settings (SanctionsConfig).

// nameStop are words left out of a name before it is compared: titles and
// company endings.
var nameStop = map[string]bool{
	"MR": true, "MRS": true, "MS": true, "MISS": true, "DR": true, "SHRI": true, "SRI": true, "SMT": true, "KUM": true, "KUMARI": true,
	"PVT": true, "PRIVATE": true, "LTD": true, "LIMITED": true, "LLP": true, "INC": true, "CORP": true, "CORPORATION": true, "CO": true, "COMPANY": true,
}

// foldRune turns an accented Latin letter into its plain letter.
func foldRune(r rune) rune {
	switch {
	case strings.ContainsRune("ÀÁÂÃÄÅĀĂĄàáâãäåāăą", r):
		return 'A'
	case strings.ContainsRune("ÇĆĈĊČçćĉċč", r):
		return 'C'
	case strings.ContainsRune("ĎĐďđ", r):
		return 'D'
	case strings.ContainsRune("ÈÉÊËĒĔĖĘĚèéêëēĕėęě", r):
		return 'E'
	case strings.ContainsRune("ĜĞĠĢĝğġģ", r):
		return 'G'
	case strings.ContainsRune("ĤĦĥħ", r):
		return 'H'
	case strings.ContainsRune("ÌÍÎÏĨĪĬĮİìíîïĩīĭįı", r):
		return 'I'
	case strings.ContainsRune("Ĵĵ", r):
		return 'J'
	case strings.ContainsRune("Ķķ", r):
		return 'K'
	case strings.ContainsRune("ĹĻĽĿŁĺļľŀł", r):
		return 'L'
	case strings.ContainsRune("ÑŃŅŇñńņň", r):
		return 'N'
	case strings.ContainsRune("ÒÓÔÕÖØŌŎŐòóôõöøōŏő", r):
		return 'O'
	case strings.ContainsRune("ŔŖŘŕŗř", r):
		return 'R'
	case strings.ContainsRune("ŚŜŞŠśŝşšß", r):
		return 'S'
	case strings.ContainsRune("ŢŤŦţťŧ", r):
		return 'T'
	case strings.ContainsRune("ÙÚÛÜŨŪŬŮŰŲùúûüũūŭůűų", r):
		return 'U'
	case strings.ContainsRune("Ŵŵ", r):
		return 'W'
	case strings.ContainsRune("ÝŸŶýÿŷ", r):
		return 'Y'
	case strings.ContainsRune("ŹŻŽźżž", r):
		return 'Z'
	}
	return unicode.ToUpper(r)
}

// nameTokens normalises a name into its words.
func sanTokens(s string) []string {
	var b strings.Builder
	for _, r := range s {
		r = foldRune(r)
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '\'' || r == '`' || r == '’':
			// Al-Qa'ida: an apostrophe joins, it does not split.
		default:
			b.WriteByte(' ')
		}
	}
	var out []string
	for _, w := range strings.Fields(b.String()) {
		if !nameStop[w] {
			out = append(out, firstOf(nameSame[w], w))
		}
	}
	return out
}

// nameSame are short forms and spellings of common name words, read as one
// (Mohd and Md are written for Mohammed in Indian records).
var nameSame = map[string]string{
	"MOHD": "MUHAMMAD", "MD": "MUHAMMAD", "MOHAMMAD": "MUHAMMAD", "MOHAMMED": "MUHAMMAD", "MOHAMED": "MUHAMMAD", "MOHAMAD": "MUHAMMAD", "MUHAMMED": "MUHAMMAD", "MUHAMAD": "MUHAMMAD", "MOHAMMEDH": "MUHAMMAD",
	"SK": "SHAIKH", "SHEIKH": "SHAIKH", "SHAIK": "SHAIKH", "SHEIK": "SHAIKH",
	"SYED": "SAYYID", "SAYED": "SAYYID", "SAIYED": "SAYYID", "SAIYID": "SAYYID", "SAYYED": "SAYYID",
}

// translitKey is a word's transliteration key: spellings of one name in
// Latin letters come to the same key more often than not.
func translitKey(w string) string {
	if w == "" {
		return ""
	}
	r := strings.NewReplacer("KH", "K", "GH", "G", "PH", "F", "TH", "T", "DH", "D", "BH", "B", "JH", "J", "SH", "S", "CH", "C",
		"Q", "K", "CK", "K", "W", "V", "Z", "S", "X", "KS", "Y", "I", "EE", "I", "OO", "U", "OU", "U")
	w = r.Replace(w)
	vowel := func(c byte) bool { return c == 'A' || c == 'E' || c == 'I' || c == 'O' || c == 'U' }
	var b []byte
	for i := 0; i < len(w); i++ {
		c := w[i]
		if i == 0 {
			if vowel(c) {
				c = 'A' // Osama, Usama; Ibrahim, Ebrahim
			}
			b = append(b, c)
			continue
		}
		if vowel(c) {
			continue
		}
		// An H at the end, or after a vowel and before a consonant, is
		// often not written (Allah, Alla; Rahman, Raman).
		if c == 'H' && (i == len(w)-1 || (vowel(w[i-1]) && !vowel(w[i+1]))) {
			continue
		}
		if w[i-1] == c {
			continue // a doubled letter
		}
		b = append(b, c)
	}
	return string(b)
}

// jaroWinkler is the Jaro-Winkler similarity of two strings, 0 to 1.
func jaroWinkler(a, b string) float64 {
	if a == b {
		return 1
	}
	la, lb := len(a), len(b)
	if la == 0 || lb == 0 {
		return 0
	}
	win := max(la, lb)/2 - 1
	if win < 0 {
		win = 0
	}
	ma, mb := make([]bool, la), make([]bool, lb)
	m := 0
	for i := 0; i < la; i++ {
		lo, hi := max(0, i-win), min(lb-1, i+win)
		for j := lo; j <= hi; j++ {
			if !mb[j] && a[i] == b[j] {
				ma[i], mb[j] = true, true
				m++
				break
			}
		}
	}
	if m == 0 {
		return 0
	}
	t, k := 0, 0
	for i := 0; i < la; i++ {
		if !ma[i] {
			continue
		}
		for !mb[k] {
			k++
		}
		if a[i] != b[k] {
			t++
		}
		k++
	}
	mf := float64(m)
	jaro := (mf/float64(la) + mf/float64(lb) + (mf-float64(t)/2)/mf) / 3
	p := 0
	for p < min(4, la, lb) && a[p] == b[p] {
		p++
	}
	return jaro + float64(p)*0.1*(1-jaro)
}

// wordScore compares two words, 0 to 1.
func wordScore(a, b string) float64 {
	if a == b {
		return 1
	}
	if len(a) == 1 || len(b) == 1 {
		// An initial: it matches a word with that first letter, a little
		// below a full match.
		if a[0] == b[0] {
			return 0.9
		}
		return 0
	}
	ka, kb := translitKey(a), translitKey(b)
	if ka == kb && len(ka) >= 2 {
		return 0.96 // spellings of one name
	}
	// Jaro-Winkler is generous to short words that only look alike
	// (Sharma, Verma: 0.82), so below 0.75 a pair counts for nothing and
	// the rest is spread out: 0.95 counts 0.76, 0.85 counts 0.38.
	s := max(jaroWinkler(a, b), 0.97*jaroWinkler(ka, kb))
	return max(0, min(1, (s-0.75)/0.25)) * 0.95
}

// nameScore is how alike two names are, 0 to 100.
func nameScore(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	// Each word of the shorter name to its best unused word of the longer,
	// the closest pairs first.
	type pair struct {
		i, j int
		s    float64
	}
	var ps []pair
	for i, x := range short {
		for j, y := range long {
			ps = append(ps, pair{i, j, wordScore(x, y)})
		}
	}
	sort.SliceStable(ps, func(x, y int) bool { return ps[x].s > ps[y].s })
	usedI, usedJ := make([]bool, len(short)), make([]bool, len(long))
	best := make([]float64, len(short))
	for _, p := range ps {
		if usedI[p.i] || usedJ[p.j] {
			continue
		}
		usedI[p.i], usedJ[p.j] = true, true
		best[p.i] = p.s
	}
	var sum, weight float64
	for i, x := range short {
		w := float64(min(len(x), 8))
		sum += best[i] * w
		weight += w
	}
	s := sum / weight
	// A one-word name is weak evidence; extra words on the longer side
	// lower the score a little.
	if len(short) == 1 && len(long) > 1 {
		s *= 0.7
	} else {
		s *= 0.7 + 0.3*float64(len(short))/float64(len(long))
	}
	// Run together, for names split differently (Abdulrahman, Abdul Rahman).
	if len(a) != len(b) {
		ja, jb := strings.Join(a, ""), strings.Join(b, "")
		if j := 0.97 * max(jaroWinkler(ja, jb), jaroWinkler(translitKey(ja), translitKey(jb))); j > s && len(ja) >= 8 && len(jb) >= 8 {
			s = j
		}
	}
	return int(s*100 + 0.5)
}

// nameKeys are the first letters (by transliteration key) of a name's
// words, for picking the list entries worth scoring.
func nameKeys(ws []string) []byte {
	var out []byte
	for _, w := range ws {
		if k := translitKey(w); k != "" {
			out = append(out, k[0])
		}
	}
	return out
}
