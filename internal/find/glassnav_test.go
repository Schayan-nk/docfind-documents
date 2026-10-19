package find

import (
	"strings"
	"testing"
)

// The dock takes its destinations from nav.js's permission-checked list and
// never writes its own: a place the menu would hide must not appear here.
func TestGlassNavUsesTheCheckedDestinations(t *testing.T) {
	src := readPage(t, "glassnav.js")
	for _, want := range []string{"has('/documents')", "window.DocfindNav"} {
		if !strings.Contains(src, want) {
			t.Errorf("glassnav.js no longer gates on %s", want)
		}
	}
	nav := readPage(t, "nav.js")
	if !strings.Contains(nav, "mountGlassNav(visible, o, allowed)") {
		t.Error("nav.js does not hand the dock the filtered destinations")
	}
}

// The documents page opens on its folder panel on a phone, down to the foot of
// the window at the drawer's z-index -- over the dock's. Measured on the live
// 5.25.0 at 390px, the middle of the dock answered to a folder row. The dock
// must stay the way off the page while the panel is open, and the panel's last
// rows must scroll clear of it.
func TestTheDocumentsFolderPanelLeavesTheDockReachable(t *testing.T) {
	ui := readPage(t, "ui.html")
	for _, want := range []string{
		"body.has-glassnav.nav-open .glass-dock { z-index: calc(var(--z-drawer) + 1); }",
		"body.has-glassnav #side.drill {\n      padding-bottom: calc(var(--glass-dock-h) + 26px + env(safe-area-inset-bottom, 0px));",
	} {
		if !strings.Contains(ui, want) {
			t.Errorf("ui.html lost %q", want)
		}
	}
	// The lift is only worth anything while the dock sits under the drawer.
	css := readPage(t, "app.css")
	if !strings.Contains(css, "--z-drawer: 40;") || !strings.Contains(css, "position: fixed; z-index: 35; left: 0; right: 0; margin-inline: auto;") {
		t.Error("the dock or the drawer changed layer; check the documents panel still leaves the dock reachable")
	}
}

// The glass is darker than it looks like it needs to be (see app.css): the
// labels on it must keep their contrast over the light home panes.
func TestGlassKeepsItsContrastSettings(t *testing.T) {
	css := readPage(t, "app.css")
	for _, want := range []string{
		"--glass-bg: rgb(var(--glass-tint) / .72)",
		"brightness(.78)",
		"prefers-reduced-transparency: reduce",
		"prefers-reduced-motion: reduce",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css lost %q", want)
		}
	}
}
