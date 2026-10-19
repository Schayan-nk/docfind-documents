package find

import (
	"regexp"
	"strings"
	"testing"
)

// The rules that make this application usable on a phone, held in place.
//
// Every one of these was measured on an emulated iPhone against the running
// service, not read out of a guideline. They are here because each failure is
// invisible in a desktop browser and obvious on the device: nothing throws,
// nothing logs, the page simply behaves badly in someone's hand.

// The two stylesheets must not disagree about the touch-target minimum.
//
// server.go concatenates app.css and then uiverse.css, so uiverse wins on
// source order at equal specificity. It set .btn-sm to 40px while app.css
// asked for 44 -- and the smaller number quietly won on every page, in a file
// whose own comment cites Apple's 44x44. Measured: thirty-three buttons on the
// report builder came out 40px tall.
func TestTheTwoStylesheetsAgreeOnTheTouchMinimum(t *testing.T) {
	uv := string(uiverseCSS)

	// The specific regression: .btn-sm asking for less than 44.
	badBtnSm := regexp.MustCompile(`\.btn-sm\s*\{[^}]*min-height:\s*(\d+)px`)
	for _, m := range badBtnSm.FindAllStringSubmatch(uv, -1) {
		if m[1] != "44" {
			t.Errorf("uiverse.css sets .btn-sm to %spx. app.css asks for 44px, and uiverse.css "+
				"is concatenated AFTER it, so this number is the one that reaches the browser.",
				m[1])
		}
	}

	// The account button declares a fixed size in a rule LATER than the
	// coarse-pointer block, so its 44px override has to live after it or it
	// does nothing. This was a real regression once.
	if !strings.Contains(string(styleSheet), ".um-btn { width: 44px; height: 44px; }") {
		t.Error("app.css no longer raises the account button to 44px on touch; the override " +
			"has to live after the rule that sets its base size or it does nothing.")
	}

	// The nav pin is the OTHER half of this, and its answer is the opposite: it
	// is not sized up for touch, it is removed from touch entirely. Pinning is a
	// desktop-with-a-mouse idea, and a pinned column on an iPad is the phone
	// problem at a larger size -- so there is no touch target to get right
	// because the control is not there. If it were merely shrunk-and-shown again,
	// a remembered pin could strand the drawer open with no way to close it.
	css := string(styleSheet)
	if strings.Contains(css, ".navpin { width: 44px; height: 44px; }") {
		t.Error("the nav pin is sized up for touch again; it should be hidden on touch, not " +
			"shown as a 44px control -- pinning does not belong on a touch device")
	}
	// The test is on the PRIMARY pointer, (pointer: coarse), not any-pointer: a
	// touch-screen laptop has a touch screen AND a trackpad, answers yes to
	// (any-pointer: coarse), and used to lose its menu button and pin for it.
	// An iPad's primary pointer is the finger, so it is still caught.
	coarseHidesPin := regexp.MustCompile(`@media \(pointer: coarse\) \{\s*\.navpin \{ display: none;`)
	if !coarseHidesPin.MatchString(css) {
		t.Error("the nav pin is no longer hidden on coarse pointers, so an iPad in landscape " +
			"(past 1100px, touch screen) still offers a pin that can strand the drawer open")
	}
	// The pinned LAYOUT must be gated the same way, or hiding the button is a
	// trap: a tablet that remembered a pin would apply the pinned layout with no
	// button left to undo it.
	if !strings.Contains(css, "@media (min-width: 1100px) and (pointer: fine)") {
		t.Error("the pinned layout is not excluded on touch devices, so a remembered pin can " +
			"force the drawer open on a tablet with its toggle and close hidden -- a menu " +
			"with no way out")
	}
}
