package find

import (
	"strings"
)

// stripScripts removes <script> bodies before counting -- see the note at the
// call site for why an id in a template string is not a duplicate element.
func stripScripts(s string) string {
	var b strings.Builder
	lower := strings.ToLower(s)
	for {
		i := strings.Index(lower, "<script")
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.Index(lower[i:], "</script>")
		if j < 0 {
			b.WriteString(s[:i])
			return b.String()
		}
		b.WriteString(s[:i])
		s, lower = s[i+j+len("</script>"):], lower[i+j+len("</script>"):]
	}
}

// stripComments removes HTML comments before counting.
//
// A commented-out block is not in the document, and an id mentioned in prose
// -- this file's own comments discuss id="annTitle" -- is not a definition.
func stripComments(s string) string {
	for {
		i := strings.Index(s, "<!--")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "-->")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+3:]
	}
}
