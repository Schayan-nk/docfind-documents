package find

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A fixed day to anchor every relative date on.
//
// 2026-09-19 is 28 Shahrivar 1405. Every "this month" and "last year" below
// is checked against that, so these tests mean the same thing in Esfand as
// they do today -- a date test that only passes on the day it was written is
// a date test nobody can trust.
var testNow = time.Date(2026, 9, 19, 14, 0, 0, 0, time.Local)

func TestItReadsADocumentTypeOutOfAPersianQuestion(t *testing.T) {
	cases := []struct {
		q    string
		want string
	}{
		{"پیش‌فاکتورهای پرنیان", "Proforma Invoice"},
		{"پیش فاکتور پرنیان", "Proforma Invoice"},
		{"پروفرما", "Proforma Invoice"},
		{"فاکتورهای امسال", "Invoice"},
		{"قرارداد با شرکت فولاد", "Contract"},
		{"استعلام‌های تیر", "Inquiry"},
		{"سفارش خرید آریا", "Purchase Order"},
	}
	for _, c := range cases {
		q, in := InterpretQuery(c.q, testNow, 50)
		if q.Category != c.want {
			t.Errorf("%q understood as category %q, want %q (summary: %s)",
				c.q, q.Category, c.want, in.Summary)
		}
	}
}

// "پیش فاکتور" is a proforma, not an invoice.
//
// Both words are in the vocabulary and one contains the other, so the longest
// phrase has to win. Matching the shorter one would file every proforma under
// Invoice and the reader would never see why.
func TestTheLongerDocumentTypePhraseWins(t *testing.T) {
	q, _ := InterpretQuery("پیش فاکتور", testNow, 50)
	if q.Category != "Proforma Invoice" {
		t.Errorf("«پیش فاکتور» understood as %q; it is a proforma, and reading it as "+
			"an invoice puts every one of them in the wrong place", q.Category)
	}
}

func TestItReadsJalaliDates(t *testing.T) {
	cases := []struct {
		q          string
		wantJY     int
		wantJM     int // 0 means a whole year
		wantInMean string
	}{
		{"فاکتورهای تیر ۱۴۰۴", 1404, 4, "تیر"},
		{"اسناد مرداد", 1405, 5, "مرداد"}, // year implied: this one
		{"قرارداد ۱۴۰۳", 1403, 0, "۱۴۰۳"}, // bare year
		{"استعلام‌های امسال", 1405, 0, "امسال"},
		{"استعلام‌های پارسال", 1404, 0, "پارسال"},
		{"فاکتورهای ماه گذشته", 1405, 5, "ماه گذشته"}, // Shahrivar -> Mordad
		{"فاکتورهای این ماه", 1405, 6, "این ماه"},
	}
	for _, c := range cases {
		q, in := InterpretQuery(c.q, testNow, 50)
		if q.Since.IsZero() || q.Until.IsZero() {
			t.Errorf("%q produced no date range (summary: %s)", c.q, in.Summary)
			continue
		}
		var said bool
		for _, u := range in.Understood {
			if u.Kind == "date" && strings.Contains(u.Means, c.wantInMean) {
				said = true
			}
		}
		if !said {
			var means []string
			for _, u := range in.Understood {
				means = append(means, u.Kind+":"+u.Means)
			}
			t.Errorf("%q: no date understanding mentioning %q; got %v",
				c.q, c.wantInMean, means)
		}
		// The range must be a whole month or a whole year, and it must be
		// half-open -- the end is the first instant of the next period.
		span := q.Until.Sub(q.Since)
		if c.wantJM == 0 {
			if span < 360*24*time.Hour || span > 370*24*time.Hour {
				t.Errorf("%q spans %v, which is not a year", c.q, span)
			}
		} else if span < 28*24*time.Hour || span > 32*24*time.Hour {
			t.Errorf("%q spans %v, which is not a month", c.q, span)
		}
	}
}

// The date filter is on the file's own timestamp, and the panel says so.
//
// The archive records when a document was written to disk, not the date
// printed inside it. A document filed late sits in the month it was filed, and
// a reader comparing the result against a paper file will otherwise conclude
// the search is broken. It is not broken; it is answering a slightly different
// question, and the only honest fix is to say which.
func TestTheDateFilterSaysItIsTheFileDate(t *testing.T) {
	_, in := InterpretQuery("فاکتورهای تیر ۱۴۰۴", testNow, 50)
	var found bool
	for _, u := range in.Understood {
		if u.Kind == "date" && strings.Contains(u.Means, "تاریخ ثبت فایل") {
			found = true
		}
	}
	if !found {
		t.Error("the date understanding does not say it filters on the file's own " +
			"timestamp rather than the date printed in the document")
	}
}

// A month boundary includes the last day in full.
//
// Written as its own test because getting it wrong is invisible: the results
// look right, and a handful of documents filed in the evening of the last day
// of the month are simply absent. The bound is the first instant of the next
// month, and a document at 23:59 on the last day is before it.
func TestAMonthRangeIncludesTheWholeOfItsLastDay(t *testing.T) {
	q, _ := InterpretQuery("مرداد ۱۴۰۵", testNow, 50)
	// The last instant of the month is one nanosecond before Until.
	lastMoment := q.Until.Add(-time.Nanosecond)
	if lastMoment.Before(q.Since) {
		t.Fatal("the range is empty")
	}
	// A document at that moment must pass the filter the search applies.
	d := Doc{ModTime: lastMoment}
	if !d.ModTime.Before(q.Until) || d.ModTime.Before(q.Since) {
		t.Errorf("a document filed at %v, the last moment of the month, falls outside "+
			"the range %v..%v", lastMoment, q.Since, q.Until)
	}
	// And one a nanosecond later must not.
	if !q.Until.Before(q.Until.Add(time.Nanosecond)) {
		t.Error("the bound is not exclusive")
	}
}

func TestItReadsAReferenceNumber(t *testing.T) {
	for _, q := range []string{"سند ۳۳۳۴۴۴", "PO-333444", "inv 333444", "333444"} {
		got, in := InterpretQuery(q, testNow, 50)
		if got.Ref != "333444" {
			t.Errorf("%q gave ref %q, want 333444 (summary: %s)", q, got.Ref, in.Summary)
		}
		if got.Group != "333" {
			t.Errorf("%q gave group %q, want 333", q, got.Group)
		}
	}
}

// Grammar words are dropped and SAID to be dropped.
//
// A reader whose question was mostly grammar gets an empty result and no
// explanation unless the panel names what it threw away.
func TestStopWordsAreDroppedAndReported(t *testing.T) {
	_, in := InterpretQuery("همه قراردادهای مربوط به پرنیان را نشان بده", testNow, 50)
	if len(in.Unused) == 0 {
		t.Error("nothing reported as dropped, though the question is mostly grammar")
	}
	for _, w := range in.Unused {
		if !stopWords[w] {
			t.Errorf("%q was dropped but is not a stop word", w)
		}
	}
	// And the word that matters survives.
	var kept bool
	for _, term := range in.Terms {
		if strings.Contains(term, "پرنیان") {
			kept = true
		}
	}
	if !kept {
		t.Errorf("the company name did not survive as a search term; terms were %v", in.Terms)
	}
}

// A question that says nothing searchable says so, rather than searching for
// everything.
func TestAQuestionWithNothingInItSaysSo(t *testing.T) {
	q, in := InterpretQuery("همه را نشان بده", testNow, 50)
	if len(q.Terms) != 0 || q.Category != "" || !q.Since.IsZero() {
		t.Errorf("a question of pure grammar produced a real query: %+v", q)
	}
	if !strings.Contains(in.Summary, "پیدا نشد") {
		t.Errorf("the summary does not say nothing was understood: %q", in.Summary)
	}
}

// The panel describes the query that actually runs.
//
// This is the property the whole design exists for. Every understanding must
// correspond to a filter that was set -- a panel that says "نوع سند: قرارداد"
// while the query carries no category is worse than no panel, because it is a
// confident account of something that did not happen.
func TestEveryUnderstandingMatchesAFilterThatWasActuallySet(t *testing.T) {
	questions := []string{
		"پیش‌فاکتورهای پرنیان در تیر ۱۴۰۴",
		"قرارداد ۳۳۳۴۴۴",
		"فاکتورهای پارسال",
		"استعلام آریا این ماه",
		"سفارش خرید",
		"۱۴۰۳",
		"پرنیان",
	}
	for _, question := range questions {
		q, in := InterpretQuery(question, testNow, 50)
		for _, u := range in.Understood {
			switch u.Kind {
			case "category":
				if q.Category == "" {
					t.Errorf("%q: the panel claims a document type and the query has none",
						question)
				}
			case "date":
				if q.Since.IsZero() && q.Until.IsZero() {
					t.Errorf("%q: the panel claims a date and the query has none", question)
				}
			case "reference":
				if q.Ref == "" {
					t.Errorf("%q: the panel claims a reference and the query has none",
						question)
				}
			default:
				t.Errorf("%q: unknown understanding kind %q", question, u.Kind)
			}
		}
		// And the other way: a filter that was set must be described, or the
		// reader cannot tell why their results were narrowed.
		if q.Category != "" && !mentions(in, "category") {
			t.Errorf("%q: a category filter was applied and the panel does not say so",
				question)
		}
		if !q.Since.IsZero() && !mentions(in, "date") {
			t.Errorf("%q: a date filter was applied and the panel does not say so", question)
		}
		if q.Ref != "" && !mentions(in, "reference") {
			t.Errorf("%q: a reference filter was applied and the panel does not say so",
				question)
		}
	}
}

func mentions(in Interpretation, kind string) bool {
	for _, u := range in.Understood {
		if u.Kind == kind {
			return true
		}
	}
	return false
}

// No model is called. The interpreter is rules, and this says so out loud.
//
// If a future change routes this through aiadvice, the Interpretation panel
// stops being generated FROM the query and starts being another thing a model
// produced -- at which point it can describe a filter that was not applied,
// which is the one failure this feature was designed to rule out.
func TestTheInterpreterCallsNoModel(t *testing.T) {
	src := mustRead(t, "nlquery.go")
	for _, forbidden := range []string{"aiadvice", "http.Post", "http.Client", "openai"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("nlquery.go mentions %q. The interpretation panel is trustworthy "+
				"only because it is built from the query by the code that built the "+
				"query; a model in this path can describe a filter it did not apply",
				forbidden)
		}
	}
}

func mustRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}
