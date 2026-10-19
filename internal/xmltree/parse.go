package xmltree

import (
	"bytes"
	"fmt"
	"strings"
)

// Parse builds a DOM from an XML part. The returned node has Kind
// KindDocument; use Root to reach the document element.
func Parse(data []byte) (*Node, error) {
	p := &parser{data: data}
	doc := &Node{Kind: KindDocument}
	stack := []*Node{doc}

	for p.pos < len(p.data) {
		cur := stack[len(stack)-1]

		if p.data[p.pos] != '<' {
			raw := p.readTextRun()
			cur.Append(&Node{Kind: KindText, Text: Unescape(raw)})
			continue
		}

		rest := p.data[p.pos:]
		switch {
		case bytes.HasPrefix(rest, []byte("<!--")):
			body, err := p.readDelimited(4, "-->")
			if err != nil {
				return nil, err
			}
			cur.Append(&Node{Kind: KindComment, Text: body})

		case bytes.HasPrefix(rest, []byte("<![CDATA[")):
			body, err := p.readDelimited(9, "]]>")
			if err != nil {
				return nil, err
			}
			cur.Append(&Node{Kind: KindCDATA, Text: body})

		case bytes.HasPrefix(rest, []byte("<?")):
			body, err := p.readDelimited(2, "?>")
			if err != nil {
				return nil, err
			}
			cur.Append(&Node{Kind: KindProcInst, Text: body})

		case bytes.HasPrefix(rest, []byte("<!")):
			body, err := p.readDirective()
			if err != nil {
				return nil, err
			}
			cur.Append(&Node{Kind: KindDirective, Text: body})

		case bytes.HasPrefix(rest, []byte("</")):
			name, err := p.readCloseTag()
			if err != nil {
				return nil, err
			}
			if cur.Kind != KindElement {
				return nil, p.errf("unexpected closing tag </%s> at top level", name)
			}
			if cur.Name != name {
				return nil, p.errf("closing tag </%s> does not match <%s>", name, cur.Name)
			}
			stack = stack[:len(stack)-1]

		default:
			el, selfClosed, err := p.readOpenTag()
			if err != nil {
				return nil, err
			}
			el.selfClosing = selfClosed
			cur.Append(el)
			if !selfClosed {
				stack = append(stack, el)
			}
		}
	}

	if len(stack) != 1 {
		return nil, p.errf("unclosed element <%s>", stack[len(stack)-1].Name)
	}
	if doc.Root() == nil {
		return nil, fmt.Errorf("xmltree: no document element")
	}
	return doc, nil
}

type parser struct {
	data []byte
	pos  int
}

func (p *parser) errf(format string, args ...any) error {
	return fmt.Errorf("xmltree: %s (at byte offset %d)", fmt.Sprintf(format, args...), p.pos)
}

func (p *parser) readTextRun() string {
	i := bytes.IndexByte(p.data[p.pos:], '<')
	var raw []byte
	if i < 0 {
		raw = p.data[p.pos:]
		p.pos = len(p.data)
	} else {
		raw = p.data[p.pos : p.pos+i]
		p.pos += i
	}
	return string(raw)
}

func (p *parser) readDelimited(openLen int, closeTok string) (string, error) {
	start := p.pos + openLen
	i := bytes.Index(p.data[start:], []byte(closeTok))
	if i < 0 {
		return "", p.errf("unterminated %q", closeTok)
	}
	body := string(p.data[start : start+i])
	p.pos = start + i + len(closeTok)
	return body, nil
}

// readDirective handles `<!DOCTYPE ...>` including quoted sections and one
// level of internal subset brackets. OOXML parts never contain one, but a
// hand-edited template might.
func (p *parser) readDirective() (string, error) {
	start := p.pos + 2
	depth := 0
	for i := start; i < len(p.data); i++ {
		switch c := p.data[i]; c {
		case '"', '\'':
			j := bytes.IndexByte(p.data[i+1:], c)
			if j < 0 {
				return "", p.errf("unterminated quoted string in directive")
			}
			i += j + 1
		case '[':
			depth++
		case ']':
			depth--
		case '>':
			if depth <= 0 {
				body := string(p.data[start:i])
				p.pos = i + 1
				return body, nil
			}
		}
	}
	return "", p.errf("unterminated directive")
}

func (p *parser) readCloseTag() (string, error) {
	start := p.pos + 2
	i := bytes.IndexByte(p.data[start:], '>')
	if i < 0 {
		return "", p.errf("unterminated closing tag")
	}
	name := string(bytes.TrimSpace(p.data[start : start+i]))
	p.pos = start + i + 1
	return name, nil
}

func isNameByte(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '/', '>', '=':
		return false
	}
	return true
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

func (p *parser) skipSpace() {
	for p.pos < len(p.data) && isSpaceByte(p.data[p.pos]) {
		p.pos++
	}
}

func (p *parser) readOpenTag() (*Node, bool, error) {
	p.pos++ // consume '<'
	nameStart := p.pos
	for p.pos < len(p.data) && isNameByte(p.data[p.pos]) {
		p.pos++
	}
	if p.pos == nameStart {
		return nil, false, p.errf("empty element name")
	}
	el := &Node{Kind: KindElement, Name: string(p.data[nameStart:p.pos])}

	for {
		p.skipSpace()
		if p.pos >= len(p.data) {
			return nil, false, p.errf("unterminated tag <%s>", el.Name)
		}
		switch p.data[p.pos] {
		case '>':
			p.pos++
			return el, false, nil
		case '/':
			if p.pos+1 < len(p.data) && p.data[p.pos+1] == '>' {
				p.pos += 2
				return el, true, nil
			}
			return nil, false, p.errf("stray '/' in tag <%s>", el.Name)
		}

		anStart := p.pos
		for p.pos < len(p.data) && isNameByte(p.data[p.pos]) {
			p.pos++
		}
		if p.pos == anStart {
			return nil, false, p.errf("malformed attribute in <%s>", el.Name)
		}
		attrName := string(p.data[anStart:p.pos])

		p.skipSpace()
		if p.pos >= len(p.data) || p.data[p.pos] != '=' {
			return nil, false, p.errf("attribute %q in <%s> has no value", attrName, el.Name)
		}
		p.pos++
		p.skipSpace()
		if p.pos >= len(p.data) {
			return nil, false, p.errf("attribute %q in <%s> has no value", attrName, el.Name)
		}
		quote := p.data[p.pos]
		if quote != '"' && quote != '\'' {
			return nil, false, p.errf("unquoted value for attribute %q in <%s>", attrName, el.Name)
		}
		p.pos++
		vStart := p.pos
		i := bytes.IndexByte(p.data[p.pos:], quote)
		if i < 0 {
			return nil, false, p.errf("unterminated value for attribute %q in <%s>", attrName, el.Name)
		}
		p.pos += i
		value := string(p.data[vStart:p.pos])
		p.pos++ // consume closing quote

		// Single-quoted values are re-emitted double-quoted, so a literal
		// double quote inside them must be escaped.
		if quote == '\'' {
			value = strings.ReplaceAll(value, `"`, "&quot;")
		}
		el.Attrs = append(el.Attrs, Attr{Name: attrName, Value: value})
	}
}
