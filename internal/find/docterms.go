package find

import (
	"regexp"
	"strings"
)

// The amounts and payment terms a document names, and the folding that lets
// one Persian word typed several ways compare equal -- for the facts strip
// (docfacts.go).

// moneyTerm is one financial term a text names.
type moneyTerm struct {
	Kind string `json:"kind"` // amount, payment
	Text string `json:"text"`
}

var (
	amountPattern = regexp.MustCompile(`(?i)(\d[\d,.]*\s*(eur|euro|usd|\$|€|rial|irr|ریال|تومان|یورو|دلار))|((eur|usd|€|\$)\s*\d[\d,.]*)`)
	// The short Latin terms are whole words, and CAD (cash against documents)
	// only in capitals: unbounded and case-blind, "cad" was found inside
	// "decade" and "cascade" and offered as a document's payment terms.
	paymentTerms = regexp.MustCompile(`(?i)(\d{1,3}\s*%\s*(advance|down ?payment|پیش ?پرداخت|پیش‌پرداخت))|\b(t/t|l/c|letter of credit|cash against documents|net\s*\d+)\b|(?-i:\bCAD\b)|(اعتبار اسنادی|حواله|پیش ?پرداخت|پیش‌پرداخت)`)
)

// moneyTerms finds the amounts and payment terms a text names.
func moneyTerms(text string) []moneyTerm {
	var out []moneyTerm
	seen := map[string]bool{}
	add := func(kind, t string) {
		t = strings.TrimSpace(t)
		if t == "" || seen[kind+t] || len(out) >= 12 {
			return
		}
		seen[kind+t] = true
		out = append(out, moneyTerm{Kind: kind, Text: t})
	}
	for _, m := range amountPattern.FindAllString(text, -1) {
		add("amount", m)
	}
	for _, m := range paymentTerms.FindAllString(text, -1) {
		add("payment", m)
	}
	return out
}

// foldPersian folds the ways one word can be typed into one: lower case,
// Persian ی and ک for their Arabic look-alikes, the hamza and madda forms of
// alef to a plain alef, Persian and Arabic digits to ASCII, and the
// half-space, joiners, tatweel and short-vowel marks dropped.
func foldPersian(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r == '\u064a' || r == '\u0649' || r == '\u0626': // Arabic yeh, alef maksura, yeh with hamza
			r = '\u06cc' // Persian yeh
		case r == '\u0643': // Arabic kaf
			r = '\u06a9' // Persian keheh
		case r == '\u0629': // teh marbuta
			r = '\u0647' // heh
		case r == '\u0623' || r == '\u0625' || r == '\u0622' || r == '\u0671': // alef with hamza or madda, wasla
			r = '\u0627' // alef
		case r == '\u0624': // waw with hamza
			r = '\u0648' // waw
		case r >= '\u06f0' && r <= '\u06f9': // Persian digits
			r = '0' + (r - '\u06f0')
		case r >= '\u0660' && r <= '\u0669': // Arabic-Indic digits
			r = '0' + (r - '\u0660')
		case r == '\u200c' || r == '\u200d' || r == '\u0640' || r == '\u200e' || r == '\u200f':
			continue // half-space, joiner, tatweel, direction marks
		case r >= '\u064b' && r <= '\u065f' || r == '\u0670':
			continue // short vowels and the like
		}
		b.WriteRune(r)
	}
	return b.String()
}
