// Package find indexes generated documents so they can be searched by
// reference number, document type or their text content.
//
// The archive on disk is the source of truth; this package only ever reads it.
package find

import (
	"os"
	"strings"

	"github.com/Schayan-nk/docfind-documents/internal/ooxml"
	"github.com/Schayan-nk/docfind-documents/internal/xmltree"
)

// ExtractText returns the visible text of a .docx.
//
// Every text-bearing part is included -- body, headers, footers, footnotes --
// because a reference number often lives only in the header, and a search that
// missed it would look broken for exactly the documents people search for most.
func ExtractText(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return extractTextBytes(raw)
}

func extractTextBytes(raw []byte) (string, error) {
	pkg, err := ooxml.Open(raw)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	for _, name := range pkg.TextPartNames() {
		part := pkg.Part(name)
		if part == nil {
			continue
		}
		doc, err := xmltree.Parse(part.Data)
		if err != nil {
			// A damaged part should not lose the rest of the document.
			continue
		}
		root := doc.Root()
		if root == nil {
			continue
		}
		for _, p := range root.FindAll("w:p") {
			line := paragraphText(p)
			if strings.TrimSpace(line) == "" {
				continue
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}

// paragraphText concatenates a paragraph's runs, skipping nested paragraphs so
// text-box content is not duplicated into its container.
func paragraphText(p *xmltree.Node) string {
	var b strings.Builder
	var walk func(n *xmltree.Node, depth int)
	walk = func(n *xmltree.Node, depth int) {
		if n.Kind != xmltree.KindElement {
			return
		}
		if depth > 0 && n.Name == "w:p" {
			return
		}
		if n.Name == "w:t" {
			b.WriteString(n.TextContent())
			return
		}
		if n.Name == "w:tab" {
			b.WriteByte('\t')
			return
		}
		for _, c := range n.Children {
			walk(c, depth+1)
		}
	}
	for _, c := range p.Children {
		walk(c, 1)
	}
	return b.String()
}
