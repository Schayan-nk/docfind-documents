package find

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Every script and stylesheet served with a year-long, immutable cache must be
// part of the stamp's hash, or a change to it alone does not move the stamp
// and browsers keep the old copy. pmconnect.js, pm.js and six others were left
// out until the review of the 5.37.0 candidate (2026-10-03).
func TestEveryVersionedAssetIsInTheStamp(t *testing.T) {
	src, err := os.ReadFile("cachecontrol.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "var assetStamp")
	if start < 0 {
		t.Fatal("assetStamp not found; the scan is no longer matching")
	}
	stamp := body[start:]
	stamp = stamp[:strings.Index(stamp, "\n})")]

	// The variable each file is embedded as, from every //go:embed in the package.
	embed := regexp.MustCompile(`//go:embed (\S+)\r?\nvar (\w+)`)
	vars := map[string]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range embed.FindAllStringSubmatch(string(b), -1) {
			vars["/"+m[1]] = m[2]
		}
	}

	checked := 0
	for _, p := range versionedAssetPaths {
		if !strings.HasSuffix(p, ".js") && !strings.HasSuffix(p, ".css") {
			continue
		}
		names := []string{vars[p]}
		if p == "/app.css" {
			names = append(names, "styleSheet") // served as built from app.css
		}
		found := false
		for _, n := range names {
			if n != "" && regexp.MustCompile(`\b`+n+`\b`).MatchString(stamp) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s (embedded as %q) is versioned but not hashed into assetStamp", p, vars[p])
		}
		checked++
	}
	if checked < 10 {
		t.Fatalf("checked only %d assets; the scan is no longer matching", checked)
	}
}
