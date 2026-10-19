package find

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Schayan-nk/docfind-documents/internal/format"
)

// Asking for documents in Persian, and being told what the question was taken
// to mean.
//
// No model is called here, and that is the design rather than a saving.
//
// The assistant on /ask answers questions about the reporting database, and it
// works because the model never writes a query: it picks an entry from a
// closed catalogue and the parameters are validated. The same principle
// applies to searching the archive, and here it goes further -- the vocabulary
// a document search needs is small and entirely known. Nine document
// categories, twelve Jalali month names, a handful of relative dates, and a
// reference-number format this application mints itself. Rules cover all of
// it.
//
// What that buys is the Interpretation panel. When a model reports what it
// understood, the report is another thing the model produced and can be wrong
// about -- it can describe a filter it did not apply. Here the panel is
// generated FROM the filter, by the code that built it, so "understood as
// Proforma Invoice, filed in Tir 1405" and the query that runs cannot
// disagree. A reader who sees the wrong reading can fix their words, which is
// the whole point of showing it.
//
// It also means an empty search costs nothing and works offline, on a box
// whose API key has expired, and for a reader whose question would otherwise
// have left the building.

// Understanding is one thing recognised in a question.
type Understanding struct {
	// Kind is what sort of thing this is: "category", "date", "reference" or
	// "term". The page colours by it.
	Kind string `json:"kind"`
	// From is the words in the question this came from, so a reader can see
	// which part of what they typed produced it.
	From string `json:"from"`
	// Means is what it was taken to mean, in Persian.
	Means string `json:"means"`
}

// Interpretation is a question, what it was understood to mean, and the search
// it became.
type Interpretation struct {
	Raw        string          `json:"raw"`
	Understood []Understanding `json:"understood"`
	// Terms is what was left over and searched for as ordinary words.
	Terms []string `json:"terms,omitempty"`
	// Summary is one sentence describing the whole reading, for the top of the
	// panel.
	Summary string `json:"summary"`
	// Unused names words that were recognised as nothing and dropped rather
	// than searched for -- stop words, mostly. Shown because a reader whose
	// key word was dropped needs to know it was.
	Unused []string `json:"unused,omitempty"`
}

// jalaliMonthNames maps every Persian month name to its number.
//
// Both the common spellings are accepted for the two that have them.
var jalaliMonthNames = map[string]int{
	"فروردین": 1, "فروردین‌ماه": 1,
	"اردیبهشت": 2, "ارديبهشت": 2,
	"خرداد": 3, "تیر": 4, "تير": 4,
	"مرداد": 5, "امرداد": 5,
	"شهریور": 6, "شهريور": 6,
	"مهر": 7, "آبان": 8, "ابان": 8,
	"آذر": 9, "اذر": 9,
	"دی": 10, "بهمن": 11, "اسفند": 12,
}

// categoryWords maps Persian words for a document to a category.
//
// The categories themselves are English in the archive ("Proforma Invoice"),
// because that is what the file names carry; nobody searching for one types
// that. Several Persian words land on one category on purpose -- پیش‌فاکتور
// and پروفرما are the same document.
var categoryWords = map[string]string{
	"استعلام":     "Inquiry",
	"استعلامیه":   "Inquiry",
	"پیشنهادفنی":  "Technical Offer",
	"پیشنهاد فنی": "Technical Offer",
	"پروفرما":     "Proforma Invoice",
	"پیشفاکتور":   "Proforma Invoice",
	"پیش فاکتور":  "Proforma Invoice",
	"پیش‌فاکتور":  "Proforma Invoice",
	"سفارشخرید":   "Purchase Order",
	"سفارش خرید":  "Purchase Order",
	"فاکتور":      "Invoice",
	"صورتحساب":    "Invoice",
	"قرارداد":     "Contract",
	"بستهبندی":    "Packing List",
	"بسته بندی":   "Packing List",
	"لیست بسته":   "Packing List",
	"تاییدیه":     "Order Confirmation",
	"تأییدیه":     "Order Confirmation",
	"قیمت":        "Quotation",
	"پیشنهادقیمت": "Quotation",
}

// categoryPersian names a category in Persian, for the panel.
var categoryPersian = map[string]string{
	"Inquiry":            "استعلام",
	"Technical Offer":    "پیشنهاد فنی",
	"Quotation":          "پیشنهاد قیمت",
	"Proforma Invoice":   "پیش‌فاکتور",
	"Purchase Order":     "سفارش خرید",
	"Order Confirmation": "تاییدیه سفارش",
	"Packing List":       "لیست بسته‌بندی",
	"Invoice":            "فاکتور",
	"Contract":           "قرارداد",
}

// stopWords are words that carry no filter and no search value.
//
// Dropped rather than searched for, and NAMED in the panel as dropped. A
// reader who typed "قرارداد با پرنیان" should see that "با" was ignored; a
// reader whose whole question was stop words should see that nothing was
// searched for at all, rather than a blank result page.
var stopWords = map[string]bool{
	"از": true, "در": true, "با": true, "به": true, "را": true, "که": true,
	"و": true, "یا": true, "برای": true, "روی": true, "های": true, "ها": true,
	"همه": true, "تمام": true, "لیست": true, "فهرست": true, "نشان": true,
	"بده": true, "بیار": true, "پیدا": true, "کن": true, "چه": true,
	"کدام": true, "هست": true, "هستند": true, "شده": true, "مربوط": true,
	"اسناد": true, "سند": true, "مدارک": true, "پرونده": true,
}

// refPattern matches the reference numbers this application mints.
//
// Six digits, optionally after a short code such as PO- or INV-, in either
// digit set -- the Tokenize pass
// has already folded Persian digits to Latin by the time a term reaches the
// index, but a reader types what is on their screen.
var refPattern = regexp.MustCompile(`(?i)\b(?:[a-z]{2,4}[\s\-_]*)?(\d{6})\b`)

// InterpretQuery reads a Persian question and returns the search it means.
//
// The returned Query is what runs; the Interpretation is what the page shows.
// They are produced together, from the same pass, so the panel cannot describe
// a filter that was not applied.
func InterpretQuery(raw string, now time.Time, limit int) (Query, Interpretation) {
	in := Interpretation{Raw: raw}
	rest := normalizePersianDigits(strings.TrimSpace(raw))

	q := Query{Limit: limit}
	if q.Limit <= 0 {
		q.Limit = 100
	}

	// 1. Reference numbers. Most specific, and the one thing that makes every
	//    other filter irrelevant: somebody who knows the number wants that
	//    document, not documents like it.
	if m := refPattern.FindStringSubmatch(rest); m != nil {
		q.Ref = m[1]
		if len(m[1]) >= 3 {
			q.Group = m[1][:3]
		}
		in.Understood = append(in.Understood, Understanding{
			Kind: "reference", From: m[0],
			Means: "شماره مرجع " + toPersianDigitsStr(m[1]),
		})
		rest = strings.Replace(rest, m[0], " ", 1)
	}

	// 2. Document category. Longest phrase first, so "پیش فاکتور" is not read
	//    as "فاکتور" with a stray word beside it.
	if cat, from, remainder := matchCategory(rest); cat != "" {
		q.Category = cat
		rest = remainder
		in.Understood = append(in.Understood, Understanding{
			Kind: "category", From: from,
			Means: "نوع سند: " + categoryPersian[cat],
		})
	}

	// 3. Dates.
	if from, until, label, words, ok := matchDate(rest, now); ok {
		q.Since, q.Until = from, until
		rest = words
		in.Understood = append(in.Understood, Understanding{
			Kind: "date", From: label.from,
			// Said explicitly, because it is the one filter here that is an
			// approximation. The archive records when a file was written, not
			// what date is printed inside it, and a document filed late sits
			// in the month it was filed.
			Means: label.means + " (بر اساس تاریخ ثبت فایل در آرشیو)",
		})
	}

	// 4. Everything left becomes ordinary search terms, minus the words that
	//    are only grammar.
	for _, w := range strings.Fields(rest) {
		clean := strings.Trim(w, "؟?.,،!:;()[]«»\"'")
		if clean == "" {
			continue
		}
		if stopWords[clean] {
			in.Unused = append(in.Unused, clean)
			continue
		}
		in.Terms = append(in.Terms, clean)
	}
	q.Terms = Tokenize(strings.Join(in.Terms, " "))

	in.Summary = summarise(q, in)
	return q, in
}

// dateLabel carries the two strings a date understanding needs.
type dateLabel struct{ from, means string }

// matchDate recognises the date expressions people actually type.
//
// Three shapes, and no more, because a date parser that accepts everything
// accepts things it gets wrong: a named Jalali month with an optional year, a
// bare Jalali year, and a small set of relative phrases. Anything else is left
// as search terms, where being wrong costs nothing.
func matchDate(s string, now time.Time) (from, until time.Time, label dateLabel, rest string, ok bool) {
	jy, jm, _ := format.ToJalali(now.Year(), int(now.Month()), now.Day())

	// Relative phrases first: "ماه گذشته" contains no month name and would
	// otherwise fall through to the terms.
	type rel struct {
		words      string
		y, m       int
		wholeYear  bool
		meansShort string
	}
	rels := []rel{
		{"این ماه", jy, jm, false, "این ماه"},
		{"ماه جاری", jy, jm, false, "این ماه"},
		{"ماه گذشته", prevY(jy, jm), prevM(jm), false, "ماه گذشته"},
		{"ماه قبل", prevY(jy, jm), prevM(jm), false, "ماه گذشته"},
		{"امسال", jy, 0, true, "امسال"},
		{"سال جاری", jy, 0, true, "امسال"},
		{"پارسال", jy - 1, 0, true, "پارسال"},
		{"سال گذشته", jy - 1, 0, true, "پارسال"},
		{"سال قبل", jy - 1, 0, true, "پارسال"},
	}
	for _, r := range rels {
		if !strings.Contains(s, r.words) {
			continue
		}
		rest = strings.Replace(s, r.words, " ", 1)
		if r.wholeYear {
			from, until = jalaliYearBounds(r.y)
			return from, until, dateLabel{r.words, r.meansShort + " (سال " + toPersianDigitsStr(itoaSimple(r.y)) + ")"}, rest, true
		}
		from, until = jalaliMonthBounds(r.y, r.m)
		return from, until, dateLabel{r.words,
			r.meansShort + " (" + jalaliMonthNameOf(r.m) + " " + toPersianDigitsStr(itoaSimple(r.y)) + ")"}, rest, true
	}

	// A named month, with the year beside it or implied.
	for name, num := range jalaliMonthNames {
		if !strings.Contains(s, name) {
			continue
		}
		rest = strings.Replace(s, name, " ", 1)
		year := jy
		from := name
		// A four-digit Jalali year anywhere in what is left.
		if m := regexp.MustCompile(`\b(1[34]\d{2})\b`).FindStringSubmatch(rest); m != nil {
			if y, err := strconv.Atoi(m[1]); err == nil {
				year = y
				rest = strings.Replace(rest, m[1], " ", 1)
				from = name + " " + m[1]
			}
		}
		f, u := jalaliMonthBounds(year, num)
		return f, u, dateLabel{from,
			jalaliMonthNameOf(num) + " " + toPersianDigitsStr(itoaSimple(year))}, rest, true
	}

	// A bare Jalali year.
	if m := regexp.MustCompile(`\b(1[34]\d{2})\b`).FindStringSubmatch(s); m != nil {
		if y, err := strconv.Atoi(m[1]); err == nil {
			rest = strings.Replace(s, m[1], " ", 1)
			f, u := jalaliYearBounds(y)
			return f, u, dateLabel{m[1], "سال " + toPersianDigitsStr(m[1])}, rest, true
		}
	}

	return time.Time{}, time.Time{}, dateLabel{}, s, false
}

func prevM(m int) int {
	if m == 1 {
		return 12
	}
	return m - 1
}

func prevY(y, m int) int {
	if m == 1 {
		return y - 1
	}
	return y
}

func jalaliMonthNameOf(m int) string {
	names := [...]string{"", "فروردین", "اردیبهشت", "خرداد", "تیر", "مرداد", "شهریور",
		"مهر", "آبان", "آذر", "دی", "بهمن", "اسفند"}
	if m < 1 || m > 12 {
		return ""
	}
	return names[m]
}

// jalaliMonthBounds returns the Gregorian instants a Jalali month spans.
//
// The archive stores Gregorian file times, so every Jalali filter has to be
// converted before it can be compared -- and the conversion has to be to the
// FIRST instant of the first day and the first instant of the day after the
// last, so that a file written at 23:50 on the last day of the month is inside
// it. Comparing against the last day at midnight loses a day every month.
func jalaliMonthBounds(jy, jm int) (from, until time.Time) {
	gy, gm, gd := format.FromJalali(jy, jm, 1)
	from = time.Date(gy, time.Month(gm), gd, 0, 0, 0, 0, time.Local)
	ny, nm := jy, jm+1
	if nm > 12 {
		ny, nm = jy+1, 1
	}
	gy2, gm2, gd2 := format.FromJalali(ny, nm, 1)
	until = time.Date(gy2, time.Month(gm2), gd2, 0, 0, 0, 0, time.Local)
	return from, until
}

func jalaliYearBounds(jy int) (from, until time.Time) {
	gy, gm, gd := format.FromJalali(jy, 1, 1)
	from = time.Date(gy, time.Month(gm), gd, 0, 0, 0, 0, time.Local)
	gy2, gm2, gd2 := format.FromJalali(jy+1, 1, 1)
	until = time.Date(gy2, time.Month(gm2), gd2, 0, 0, 0, 0, time.Local)
	return from, until
}

// matchCategory finds the longest document-type phrase in the question.
//
// Longest first, so "پیش فاکتور" is read as a proforma and not as an invoice
// with a stray word in front of it. That one pair is the whole reason this
// does not just iterate the map.
func matchCategory(s string) (cat, from, rest string) {
	best := ""
	for word := range categoryWords {
		if strings.Contains(s, word) && len([]rune(word)) > len([]rune(best)) {
			best = word
		}
	}
	if best == "" {
		return "", "", s
	}
	return categoryWords[best], best, strings.Replace(s, best, " ", 1)
}

// summarise writes the one-line reading shown at the top of the panel.
func summarise(q Query, in Interpretation) string {
	if len(in.Understood) == 0 && len(q.Terms) == 0 {
		return "چیزی برای جستجو پیدا نشد. یک نام، یک نوع سند یا یک شماره مرجع بنویسید."
	}
	var parts []string
	for _, u := range in.Understood {
		parts = append(parts, u.Means)
	}
	if len(in.Terms) > 0 {
		parts = append(parts, "شامل واژه‌های «"+strings.Join(in.Terms, "، ")+"»")
	}
	return "جستجو برای: " + strings.Join(parts, "، ")
}

// normalizePersianDigits rewrites Persian and Arabic-Indic digits as ASCII so
// a year or a reference number types the same from any keyboard.
func normalizePersianDigits(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= '۰' && r <= '۹':
			b.WriteRune('0' + (r - '۰'))
		case r >= '٠' && r <= '٩':
			b.WriteRune('0' + (r - '٠'))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// toPersianDigitsStr is the reverse, for anything shown on the page.
func toPersianDigitsStr(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune('۰' + (r - '0'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func itoaSimple(n int) string { return fmt.Sprintf("%d", n) }
