package find

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// What a document says, at a glance: the amounts and payment terms in it, the
// order, invoice and reference numbers and the dates -- above the document in
// the preview (ui.html docFacts), so a proforma is read as "EUR 48,000, 30%
// advance, valid to 1405/07/15" before anybody reads it line by line.
//
// Read from the text the index already holds. Nothing is sent anywhere and
// nothing is guessed: each fact is a piece of the document's own text.

// DocFacts is what one document names.
type DocFacts struct {
	Amounts  []string `json:"amounts"`
	Payments []string `json:"payments"`
	Refs     []string `json:"refs"`
	Dates    []string `json:"dates"`
}

const (
	docFactsMax  = 8     // of each kind
	docFactsText = 60000 // bytes of text read: the facts are near the top
)

// Jalali 13xx/14xx and Gregorian 19xx/20xx dates, with / - or . between,
// year first or last. Digits are folded to ASCII first.
var docDatePattern = regexp.MustCompile(`\b(1[34]\d\d|19\d\d|20\d\d)[/.-](0?[1-9]|1[0-2])[/.-](0?[1-9]|[12]\d|3[01])\b|\b(0?[1-9]|[12]\d|3[01])[/.-](0?[1-9]|1[0-2])[/.-](19\d\d|20\d\d)\b`)

func docFactsOf(text string) DocFacts {
	if len(text) > docFactsText {
		text = text[:docFactsText]
	}
	f := DocFacts{Amounts: []string{}, Payments: []string{}, Refs: []string{}, Dates: []string{}}
	add := func(list *[]string, s string) {
		s = strings.TrimSpace(s)
		if s == "" || len(*list) >= docFactsMax {
			return
		}
		for _, x := range *list {
			if x == s {
				return
			}
		}
		*list = append(*list, s)
	}
	for _, a := range docAmounts(text) {
		add(&f.Amounts, a)
	}
	for _, t := range moneyTerms(text) {
		if t.Kind == "payment" {
			add(&f.Payments, t.Text)
		}
	}
	for _, r := range docRefs(text) {
		add(&f.Refs, r)
	}
	for _, d := range docDatePattern.FindAllString(foldPersian(text), -1) {
		add(&f.Dates, d)
	}
	return f
}

// docRefLabel is the wording a document puts in front of its own numbers --
// "Ref Nr.:", "Order No.", "Bestellnummer", «شماره سفارش» -- in English,
// German and Persian. A number is the document's reference only right after
// one: every other long number in it -- a postcode, a telephone number, a
// year, a motor's 1500 rpm -- is text the search index wants and the facts
// strip must not offer as the reference.
var docRefLabel = regexp.MustCompile(`(?i)\b(?:our\s+|your\s+)?(?:ref(?:erence)?|order|p\.?\s?[or]\.?|purchase\s+order|invoice|proforma(?:\s+invoice)?|quotation|offer|rfq|inquiry|enquiry|tender|contract|project|file|case|anfrage|angebot|bestell|auftrag|rechnung|kunden|projekt|vorgang)[\s.-]*(?:nr|no|number|nummer|num)?\.?\s*[:#]?\s*|(?:شماره|سفارش|پرونده|پیش[\x{200C} ]?فاکتور|فاکتور|درخواست|مناقصه|قرارداد|استعلام|پیشنهاد(?:\s+(?:فنی|مالی))?)\s*[:：]?\s*`)

// docYear is a number that is only a year, which a label can precede ("offer
// 2026") without it being a reference.
var docYear = regexp.MustCompile(`^(1[34]\d\d|19\d\d|20\d\d)$`)

// docRefs are the identifiers that directly follow a reference label, as
// written ("998-001", "PI-40512"), each once.
func docRefs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, loc := range docRefLabel.FindAllStringIndex(text, -1) {
		win := text[loc[1]:min(len(text), loc[1]+48)]
		ids := ExtractIdents(win)
		if len(ids) == 0 || strings.Index(win, ids[0].Raw) > 2 {
			continue // the label is followed by words, not by its number
		}
		raw := strings.TrimSpace(ids[0].Raw)
		folded := foldPersian(raw)
		if docYear.MatchString(folded) || docDatePattern.MatchString(folded) || seen[ids[0].Canonical] {
			continue
		}
		seen[ids[0].Canonical] = true
		out = append(out, raw)
	}
	return out
}

var docAmountNumber = regexp.MustCompile(`\d[\d,.]*`)

// amountValue is the number in an amount as written: "48,000 EUR" is 48000,
// "3.060,00 €" and "3,060.00 €" are 3060. A final separator followed by one
// or two digits is the decimal point; every other one groups thousands.
func amountValue(s string) float64 {
	num := docAmountNumber.FindString(foldPersian(s))
	whole, frac := num, "0"
	if i := strings.LastIndexAny(num, ".,"); i >= 0 && len(num)-i-1 >= 1 && len(num)-i-1 <= 2 {
		whole, frac = num[:i], num[i+1:]
	}
	whole = strings.NewReplacer(",", "", ".", "").Replace(whole)
	v, _ := strconv.ParseFloat(whole+"."+frac, 64)
	return v
}

// docAmounts are the amounts a document names, largest first: the total is
// what the reader is looking for. A whole number under ten is left out -- in
// a table that is a quantity whose neighbouring cell holds the currency sign
// ("3 €"), not a price anybody quotes.
func docAmounts(text string) []string {
	type amount struct {
		text  string
		value float64
	}
	var found []amount
	for _, t := range moneyTerms(text) {
		if t.Kind != "amount" {
			continue
		}
		v := amountValue(t.Text)
		if v < 10 && v == float64(int(v)) {
			continue
		}
		found = append(found, amount{t.Text, v})
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].value > found[j].value })
	out := make([]string, len(found))
	for i, a := range found {
		out[i] = a.text
	}
	return out
}

// handleDocFacts answers GET /api/doc/facts?id=, behind the same gate as the
// document itself.
func (s *Server) handleDocFacts(w http.ResponseWriter, r *http.Request) {
	doc, ok := s.ix.Doc(r.URL.Query().Get("id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, docFactsOf(doc.Text))
}
