package find

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The question path and the ordinary path, through the real handler.
//
// Builds a small archive on disk, so it runs everywhere and needs no database.
// What it checks is the join between the interpreter and the search: that
// ?ask=1 narrows the results the way the panel says it does, and that without
// the flag nothing changes at all.
func TestAskModeNarrowsResultsAndSaysHow(t *testing.T) {
	// Scanned the way the application scans it -- the category comes from the
	// file NAME and the date from the file's own timestamp -- so this exercises
	// documents the scanner could actually produce, rather than a Doc struct
	// filled in by hand with fields that never line up that way in practice.
	root := t.TempDir()
	ix := archiveWith(t, root, []testDoc{
		{"Pi_NO_333444.docx", "پرنیان کنتاکتور", jalaliDay(t, 1404, 4, 12)},
		{"Invoice_333445.docx", "پرنیان کنتاکتور", jalaliDay(t, 1405, 6, 3)},
	})
	s := &Server{ix: ix}

	get := func(t *testing.T, url string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		s.handleSearch(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned %d", url, rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		return body
	}

	// Without the flag: both documents, and no interpretation at all.
	plain := get(t, "/api/search?q="+urlq("پرنیان"))
	if n := len(plain["results"].([]any)); n != 2 {
		t.Errorf("the ordinary search returned %d documents, want 2", n)
	}
	if _, present := plain["interpretation"]; present {
		t.Error("an ordinary search carries an interpretation. The panel must appear " +
			"only when the box was actually read as a question, or it describes a " +
			"reading that did not happen")
	}

	// With the flag and a document type: one document, and the panel says why.
	asked := get(t, "/api/search?ask=1&q="+urlq("پیش‌فاکتورهای پرنیان"))
	res := asked["results"].([]any)
	if len(res) != 1 {
		t.Fatalf("asking for proformas returned %d documents, want 1", len(res))
	}
	if got := res[0].(map[string]any)["category"]; got != "Proforma Invoice" {
		t.Errorf("returned a %v, want the Proforma Invoice", got)
	}
	in, ok := asked["interpretation"].(map[string]any)
	if !ok {
		t.Fatal("no interpretation on an ?ask=1 search")
	}
	if !strings.Contains(in["summary"].(string), "پیش‌فاکتور") {
		t.Errorf("the summary does not name the document type it filtered on: %q",
			in["summary"])
	}

	// With a date: the other document, chosen by its file time.
	dated := get(t, "/api/search?ask=1&q="+urlq("پرنیان تیر ۱۴۰۴"))
	dres := dated["results"].([]any)
	if len(dres) != 1 {
		t.Fatalf("asking for Tir 1404 returned %d documents, want 1", len(dres))
	}
	if got := dres[0].(map[string]any)["ref"]; got != "333444" {
		t.Errorf("returned %v, want the document filed in Tir 1404", got)
	}

	// A month with nothing in it returns nothing, rather than falling back to
	// everything -- a filter that silently stops applying is worse than one
	// that returns an empty list.
	empty := get(t, "/api/search?ask=1&q="+urlq("پرنیان دی ۱۴۰۲"))
	if n := len(empty["results"].([]any)); n != 0 {
		t.Errorf("a month with no documents returned %d; the date filter stopped "+
			"applying rather than matching nothing", n)
	}
	if _, ok := empty["interpretation"]; !ok {
		t.Error("an empty result carries no interpretation, which is the case where " +
			"the reader most needs to see how their words were read")
	}
}

// The sidebar's filters still apply on top of a question.
//
// Somebody who has clicked a folder in the tree and then typed a question
// expects both to hold. Dropping the sidebar would widen their results without
// them touching it.
func TestTheSidebarFiltersSurviveAQuestion(t *testing.T) {
	root := t.TempDir()
	ix := archiveWith(t, root, []testDoc{
		{"Invoice_333444.docx", "کنتاکتور", time.Now()},
		{"Invoice_999999.docx", "کنتاکتور", time.Now()},
	})
	s := &Server{ix: ix}

	rec := httptest.NewRecorder()
	s.handleSearch(rec, httptest.NewRequest(http.MethodGet,
		"/api/search?ask=1&group=333&q="+urlq("کنتاکتور"), nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	res := body["results"].([]any)
	if len(res) != 1 {
		t.Fatalf("got %d documents, want 1 -- the group filter from the sidebar was "+
			"dropped when the query was read as a question", len(res))
	}
	if got := res[0].(map[string]any)["group"]; got != "333" {
		t.Errorf("returned group %v, want 333", got)
	}
}

// testDoc is one file to put in a temporary archive.
type testDoc struct {
	name string
	text string
	when time.Time
}

// archiveWith writes real .docx files, stamps their times, and scans them.
func archiveWith(t *testing.T, root string, docs []testDoc) *Index {
	t.Helper()
	for _, d := range docs {
		p := filepath.Join(root, d.name)
		if err := os.WriteFile(p, minimalDocx(t, d.text), 0o644); err != nil {
			t.Fatal(err)
		}
		// After the write, not before: writing sets the time again.
		if err := os.Chtimes(p, d.when, d.when); err != nil {
			t.Fatal(err)
		}
	}
	ix := NewIndex(root)
	if _, err := ix.Scan(); err != nil {
		t.Fatal(err)
	}
	if ix.Count() != len(docs) {
		t.Fatalf("scanned %d documents, wrote %d", ix.Count(), len(docs))
	}
	return ix
}

// jalaliDay returns a Gregorian instant inside the given Jalali month.
//
// Built through the interpreter's own month bounds rather than a second
// conversion written beside them: a test that converts dates its own way can
// agree with itself while disagreeing with the code it is checking.
func jalaliDay(t *testing.T, jy, jm int, day int) time.Time {
	t.Helper()
	q, _ := InterpretQuery(jalaliMonthNameOf(jm)+" "+fmt.Sprint(jy), testNow, 10)
	if q.Since.IsZero() {
		t.Fatalf("could not build a date inside %d/%d", jy, jm)
	}
	// Mid-month, so a few hours of timezone drift cannot move the document
	// into a neighbouring month and make this flap.
	return q.Since.Add(time.Duration(day)*24*time.Hour + 12*time.Hour)
}

// urlq percent-encodes a query value.
//
// Written out rather than url.QueryEscape because that encodes a space as "+",
// which is correct for a form body and wrong in a path-style query the
// interpreter then reads back -- the plus would arrive as part of a word.
func urlq(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
		}
	}
	return b.String()
}
