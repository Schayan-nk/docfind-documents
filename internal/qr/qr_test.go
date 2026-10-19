package qr

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// testdata/segno.json is the same texts drawn by segno, an independent,
// widely used Python QR encoder (level M, byte mode, the mask forced or its
// own choice). Generated once with testdata/segno_gen.py; every forced-mask grid
// must match module for module -- which checks the data encoding, the error
// correction, the interleaving, the placement, the format and version bits
// and every mask pattern against somebody else's implementation.
type reference struct {
	Text    string   `json:"text"`
	Mask    int      `json:"mask"`
	Chosen  int      `json:"chosen"`
	Version int      `json:"version"`
	Rows    []string `json:"rows"`
}

func loadReferences(t *testing.T) []reference {
	t.Helper()
	raw, err := os.ReadFile("testdata/segno.json")
	if err != nil {
		t.Fatal(err)
	}
	var refs []reference
	if err := json.Unmarshal(raw, &refs); err != nil {
		t.Fatal(err)
	}
	return refs
}

func TestTooLongIsRefused(t *testing.T) {
	if _, err := Encode(strings.Repeat("a", 214)); !errors.Is(err, ErrTooLong) {
		t.Errorf("214 bytes: err = %v, want ErrTooLong", err)
	}
	if _, err := Encode(strings.Repeat("a", 213)); err != nil {
		t.Errorf("213 bytes, the most version 10 holds: %v", err)
	}
}

func TestTheSVGIsOnlyAPicture(t *testing.T) {
	c, err := Encode("otpauth://totp/docfind:ali?secret=JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	svg := c.SVG()
	if !strings.HasPrefix(svg, "<svg ") || !strings.HasSuffix(svg, "</svg>") {
		t.Errorf("not an SVG document: %.60s...", svg)
	}
	// Modules and nothing else: no script, no link, no handler, and not the
	// secret itself in readable form.
	for _, bad := range []string{"<script", "href", " on", "otpauth", "JBSWY3DP"} {
		if strings.Contains(svg, bad) {
			t.Errorf("the SVG carries %q", bad)
		}
	}
}
