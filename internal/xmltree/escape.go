package xmltree

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

var namedEntities = map[string]rune{
	"amp": '&', "lt": '<', "gt": '>', "quot": '"', "apos": '\'',
	"nbsp": ' ',
}

// Unescape decodes XML entity references. Unknown references are passed through
// verbatim rather than dropped, so nothing is ever silently lost.
func Unescape(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '&' {
			b.WriteByte(s[i])
			i++
			continue
		}
		semi := strings.IndexByte(s[i:], ';')
		if semi < 0 || semi > 12 {
			b.WriteByte('&')
			i++
			continue
		}
		ref := s[i+1 : i+semi]
		switch {
		case strings.HasPrefix(ref, "#x"), strings.HasPrefix(ref, "#X"):
			if v, err := strconv.ParseInt(ref[2:], 16, 32); err == nil && utf8.ValidRune(rune(v)) {
				b.WriteRune(rune(v))
				i += semi + 1
				continue
			}
		case strings.HasPrefix(ref, "#"):
			if v, err := strconv.ParseInt(ref[1:], 10, 32); err == nil && utf8.ValidRune(rune(v)) {
				b.WriteRune(rune(v))
				i += semi + 1
				continue
			}
		default:
			if r, ok := namedEntities[ref]; ok {
				b.WriteRune(r)
				i += semi + 1
				continue
			}
		}
		b.WriteByte('&')
		i++
	}
	return b.String()
}
