package find

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The Persian on every screen follows the mechanical rules of the
// persian-writing guide (github.com/ali2000hos/persian-writing): no dash in
// Persian prose (a comma, a semicolon, a colon or a rewrite instead), the
// half-space after «می» and before «ها», Persian ی and ک rather than the
// Arabic letters, and none of the bureaucratic verbs that make a sentence
// sound like a form letter.
//
// Only what a reader sees is checked: code comments are English and are
// skipped. A lone «—» meaning "no value" in a table cell is not prose and
// never has a Persian letter beside it, so it passes.

const faLetters = `\x{0621}-\x{063A}\x{0641}-\x{064A}\x{067E}\x{0686}\x{0698}\x{06A9}\x{06AF}\x{06CC}`

var persianRules = []struct {
	name, hint string
	rx         *regexp.Regexp
	scope      func(path string) bool
}{
	{"dash in Persian text", "use «،» «؛» «:» or rewrite the sentence",
		regexp.MustCompile(`[` + faLetters + `][^\n—–]{0,6}[—–]|[—–][^\n—–]{0,6}[` + faLetters + `]`), all},
	{"«می» without a half-space", "write می‌ with a zero-width non-joiner",
		regexp.MustCompile(`(^|[^` + faLetters + `])ن?می [` + faLetters + `]`), uiOnly},
	{"«ها» without a half-space", "write ‌ها with a zero-width non-joiner",
		regexp.MustCompile(`[` + faLetters + `] ها(ی|یی)?([^` + faLetters + `]|$)`), uiOnly},
	{"bureaucratic phrasing", "«است»، «برای»، «در جهت»; not the form-letter words",
		regexp.MustCompile(`می‌باشد|می‌باشند|لازم به ذکر است|در راستای|از اهمیت ویژه‌ای برخوردار`), all},
	{"Arabic ي or ك", "use Persian ی (U+06CC) and ک (U+06A9)",
		regexp.MustCompile(`[يك]`), func(p string) bool { return uiOnly(p) }},
}

func all(string) bool { return true }

// uiOnly is the pages, their scripts and this package's own messages. The
// assistant catalogue in internal/reports is excluded from the spelling
// rules on purpose: its keyword lists hold the unspaced forms people
// actually type, so that «سفارش های مشتری» still finds the answer.
func uiOnly(p string) bool {
	return filepath.Base(filepath.Dir(p)) == "find"
}

// normaliser lines deliberately contain the Arabic letters they fold away.
var normaliser = regexp.MustCompile(`replace|Replace|fold|Fold|normal|Normal`)

func persianTextFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	add := func(dir string, exts ...string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || strings.HasSuffix(n, "_test.go") {
				continue
			}
			// Generated catalogue data: values copied from the database,
			// which must match it byte for byte, Arabic letters included.
			if strings.HasPrefix(n, "askvaluedata") || strings.HasPrefix(n, "askpairdata") || strings.HasPrefix(n, "askbreakdowndata") {
				continue
			}
			// The rule given to the models names the banned words and the
			// dash in order to ban them: a mention, not a use.
			if n == "persianstyle.go" {
				continue
			}
			for _, x := range exts {
				if strings.HasSuffix(n, x) {
					out = append(out, filepath.Join(dir, n))
				}
			}
		}
	}
	add(".", ".html", ".js", ".go")
	for _, d := range []string{"../auth"} {
		add(d, ".go")
	}
	return out
}

// Each rule catches its own example and passes the corrected form, so the
// scan below cannot pass by matching nothing.
func TestPersianRulesCatchTheirExamples(t *testing.T) {
	cases := map[string][2]string{
		"dash in Persian text":      {`'کارتابل — بدون ورود'`, `'کارتابل، بدون ورود'`},
		"«می» without a half-space": {`'فرستاده می شود'`, `'فرستاده می‌شود'`},
		"«ها» without a half-space": {`'سفارش های باز'`, `'سفارش‌های باز'`},
		"bureaucratic phrasing":     {`'در دسترس نمی‌باشد'`, `'در دسترس نیست'`},
		"Arabic ي or ك":             {`'مشتري كوچك'`, `'مشتری کوچک'`},
	}
	for _, r := range persianRules {
		c, ok := cases[r.name]
		if !ok {
			t.Errorf("rule %q has no example", r.name)
			continue
		}
		if !r.rx.MatchString(c[0]) {
			t.Errorf("%s: missed %s", r.name, c[0])
		}
		if r.rx.MatchString(c[1]) {
			t.Errorf("%s: flagged the corrected %s", r.name, c[1])
		}
	}
}

func TestPersianTextFollowsTheWritingRules(t *testing.T) {
	files := persianTextFiles(t)
	if len(files) < 40 {
		t.Fatalf("scanned %d files; the file list is no longer finding the pages", len(files))
	}
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		inBlock := false
		for i, line := range strings.Split(string(b), "\n") {
			s := strings.TrimSpace(line)
			if strings.Contains(s, "<!--") && !strings.Contains(s, "-->") {
				inBlock = true
			}
			if inBlock {
				if strings.Contains(s, "-->") {
					inBlock = false
				}
				continue
			}
			if strings.HasPrefix(s, "//") || strings.HasPrefix(s, "*") || strings.HasPrefix(s, "/*") || strings.HasPrefix(s, "<!--") {
				continue
			}
			code := line
			if k := strings.Index(code, " // "); k >= 0 && !strings.HasSuffix(path, ".go") {
				code = code[:k]
			}
			for _, r := range persianRules {
				if !r.scope(path) || !r.rx.MatchString(code) {
					continue
				}
				if r.name == "Arabic ي or ك" && normaliser.MatchString(code) {
					continue
				}
				t.Errorf("%s:%d: %s (%s): %.90s", path, i+1, r.name, r.hint, strings.TrimSpace(code))
			}
		}
	}
}
