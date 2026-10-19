package xmltree

import (
	"testing"
)

func TestFindAllAndTextContent(t *testing.T) {
	src := `<w:document xmlns:w="ns"><w:body><w:p><w:r><w:t>A</w:t></w:r></w:p><w:p><w:r><w:t>B</w:t></w:r></w:p></w:body></w:document>`
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	paras := doc.Root().FindAll("w:p")
	if len(paras) != 2 {
		t.Fatalf("expected 2 paragraphs, got %d", len(paras))
	}
	if paras[0].TextContent() != "A" || paras[1].TextContent() != "B" {
		t.Errorf("unexpected text content")
	}
}

func TestMalformedXMLReturnsError(t *testing.T) {
	_, err := Parse([]byte(`<root><a></root>`))
	if err == nil {
		t.Fatal("expected an error for mismatched tags")
	}
}
