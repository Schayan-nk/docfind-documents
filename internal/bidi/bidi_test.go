package bidi

import "testing"

func TestToLatinDigits(t *testing.T) {
	got := ToLatinDigits("۱۲۳۴۵")
	if got != "12345" {
		t.Errorf("got %q", got)
	}
}
