// Package xmltree implements a byte-faithful XML DOM tailored to Office Open XML.
//
// Why not encoding/xml?
//
// Go's encoding/xml resolves namespace prefixes to URIs and re-emits them with
// generated prefixes (or as default namespaces). Round-tripping a Word part
// through it renames `w:p` to something else, drops `mc:Ignorable` bookkeeping
// and reorders/normalises attributes. Word tolerates some of that, but not all
// of it, and the failures are silent ("Word found unreadable content").
//
// This package instead keeps the *raw* qualified names and the *raw* attribute
// text exactly as they were found, so any node we do not deliberately touch is
// re-serialised byte-for-byte. Only the nodes the renderer edits change.
package xmltree

import (
	"strings"
)

// Kind classifies a Node.
type Kind uint8

const (
	// KindDocument is the synthetic root returned by Parse. It holds the XML
	// declaration, any top-level comments and the document element.
	KindDocument Kind = iota
	KindElement
	KindText
	KindCDATA
	KindComment
	KindProcInst
	KindDirective
)

// Attr is a single attribute. Value is stored in its *escaped* source form so
// that untouched attributes round-trip byte-for-byte. Use Node.GetAttr to read
// a decoded value and Node.SetAttr to write one.
type Attr struct {
	Name  string
	Value string
}

// Node is an XML node.
type Node struct {
	Kind     Kind
	Name     string // raw qualified name, e.g. "w:p"
	Attrs    []Attr
	Children []*Node
	Parent   *Node

	// Text holds decoded character data for KindText/KindCDATA, and the raw
	// inner body for KindComment/KindProcInst/KindDirective.
	Text string

	// selfClosing records whether the source used `<w:b/>` rather than
	// `<w:b></w:b>`, so untouched elements round-trip exactly.
	selfClosing bool
}

// Root returns the document element of a KindDocument node.
func (n *Node) Root() *Node {
	if n == nil {
		return nil
	}
	if n.Kind == KindElement {
		return n
	}
	for _, c := range n.Children {
		if c.Kind == KindElement {
			return c
		}
	}
	return nil
}

// Append adds children, fixing up parent pointers.
func (n *Node) Append(children ...*Node) *Node {
	for _, c := range children {
		if c == nil {
			continue
		}
		c.Parent = n
		n.Children = append(n.Children, c)
	}
	if len(n.Children) > 0 {
		n.selfClosing = false
	}
	return n
}

// GetAttr returns the decoded value of an attribute.
func (n *Node) GetAttr(name string) (string, bool) {
	for _, a := range n.Attrs {
		if a.Name == name {
			return Unescape(a.Value), true
		}
	}
	return "", false
}

// Attr returns the decoded value of an attribute, or "" when absent.
func (n *Node) Attr(name string) string {
	v, _ := n.GetAttr(name)
	return v
}

// Child returns the first direct child element with the given qualified name.
func (n *Node) Child(name string) *Node {
	if n == nil {
		return nil
	}
	for _, c := range n.Children {
		if c.Kind == KindElement && c.Name == name {
			return c
		}
	}
	return nil
}

// ChildrenNamed returns all direct child elements with the given name.
func (n *Node) ChildrenNamed(name string) []*Node {
	if n == nil {
		return nil
	}
	var out []*Node
	for _, c := range n.Children {
		if c.Kind == KindElement && c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// FindAll returns every descendant element with the given name, in document
// order. The receiver itself is included when it matches.
func (n *Node) FindAll(name string) []*Node {
	var out []*Node
	n.walk(func(c *Node) bool {
		if c.Kind == KindElement && c.Name == name {
			out = append(out, c)
		}
		return true
	})
	return out
}

func (n *Node) walk(fn func(*Node) bool) {
	if n == nil || !fn(n) {
		return
	}
	for _, c := range n.Children {
		c.walk(fn)
	}
}

// TextContent concatenates the decoded character data of the subtree.
func (n *Node) TextContent() string {
	var b strings.Builder
	n.walk(func(c *Node) bool {
		if c.Kind == KindText || c.Kind == KindCDATA {
			b.WriteString(c.Text)
		}
		return true
	})
	return b.String()
}
