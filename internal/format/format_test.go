package format

import (
	"testing"
)

func TestJalaliRoundTrip(t *testing.T) {
	gy, gm, gd := 2026, 8, 16
	jy, jm, jd := ToJalali(gy, gm, gd)
	bgy, bgm, bgd := FromJalali(jy, jm, jd)
	if bgy != gy || bgm != gm || bgd != gd {
		t.Errorf("round trip mismatch: got %d-%d-%d, want %d-%d-%d", bgy, bgm, bgd, gy, gm, gd)
	}
}
