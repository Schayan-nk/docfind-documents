package find

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// buildScaleArchive writes n documents shaped like the real archive.
//
// The body is deliberately not one repeated sentence: an inverted index over
// identical text has one posting list as long as the archive and no others,
// which is the easiest possible case and measures nothing. Each document gets
// terms it shares with every other, terms it shares with its neighbours, and
// terms of its own -- which is the shape real documents have.
func buildScaleArchive(t *testing.T, n int) string {
	t.Helper()
	root := tempDir(t)
	// A vocabulary to draw the "shared with neighbours" terms from.
	vocab := strings.Fields(`valve body assembly flange gasket bearing coupling
		impeller housing seal bolt nut washer shaft rotor stator bracket manifold
		actuator sensor transmitter regulator filter strainer elbow reducer tee`)

	// What share of the archive is PDF rather than Word.
	//
	// The real archive is both, and the two cost wildly different amounts to
	// index: a .docx is a zip this process reads itself, while a .pdf is an
	// external pdftotext process spawned per file. Measuring an all-Word corpus
	// and calling it "the archive at scale" would miss the only bottleneck that
	// actually bites -- see the comment on pdfMix in the test above.
	//
	// DOCFIND_SCALE_PDF is a percentage. Unset means 0: the original all-Word
	// corpus, so existing runs are unchanged.
	pdfPct := 0
	if v := os.Getenv("DOCFIND_SCALE_PDF"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p >= 0 && p <= 100 {
			pdfPct = p
		}
	}

	for i := 0; i < n; i++ {
		ref := fmt.Sprintf("%03d-%03d", 100+i/50, i%50)
		var b strings.Builder
		// Shared by every document, so one posting list is the whole archive.
		b.WriteString("valve order inquiry supplier quotation delivery terms ")
		// Shared with a neighbourhood.
		for k := 0; k < 12; k++ {
			b.WriteString(vocab[(i+k)%len(vocab)])
			b.WriteByte(' ')
		}
		// This document's own, so most posting lists are short.
		fmt.Fprintf(&b, "reference %s serial %05d batch %04d ", ref, i, i%977)
		// Enough prose that extraction is doing real work.
		for k := 0; k < 30; k++ {
			fmt.Fprintf(&b, "line %d quantity %d unit price %d total %d ",
				k, (i+k)%97, (i*7+k)%1013, (i*11+k)%9973)
		}

		// Spread the PDFs through the archive rather than putting them all at
		// one end: the walk order would otherwise do all the cheap files then
		// all the expensive ones, which hides how the cost is distributed.
		name, body := fmt.Sprintf("inquiry_acme_english_%s.docx", ref), minimalDocx(t, b.String())
		if pdfPct > 0 && i%100 < pdfPct {
			name, body = fmt.Sprintf("scan_acme_%s.pdf", ref), minimalPDF(b.String())
		}
		full := filepath.Join(root, ref[:3], ref[4:], "Inquiry", name)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// minimalPDF writes a PDF with a real text layer.
//
// Hand-assembled rather than produced by a library, because the point is to
// exercise the extraction path, not to be a well-formed document: pdftotext
// rebuilds the cross-reference table it is missing and reads the text out, which
// is exactly what it does with the scanned-then-OCR'd PDFs in the real archive.
//
// The text is escaped for PDF string syntax -- an unescaped bracket would end
// the string early and silently truncate what pdftotext finds, which would make
// the extraction look cheaper than it is.
func minimalPDF(text string) []byte {
	esc := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(text)
	var stream strings.Builder
	stream.WriteString("BT /F1 10 Tf 40 750 Td 12 TL\n")
	// Broken into lines: one enormous Tj is legal but nothing like a real page,
	// and pdftotext's line handling is part of what is being measured.
	for _, chunk := range chunkEvery(esc, 90) {
		fmt.Fprintf(&stream, "(%s) Tj T*\n", chunk)
	}
	stream.WriteString("ET")
	s := stream.String()

	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	b.WriteString("1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n")
	b.WriteString("2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj\n")
	b.WriteString("3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]" +
		"/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>endobj\n")
	fmt.Fprintf(&b, "4 0 obj<</Length %d>>stream\n%s\nendstream endobj\n", len(s), s)
	b.WriteString("5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj\n")
	b.WriteString("trailer<</Root 1 0 R/Size 6>>\n%%EOF\n")
	return []byte(b.String())
}

// chunkEvery splits s into runs of at most n bytes, on a space where it can so
// a word is not cut in half.
func chunkEvery(s string, n int) []string {
	var out []string
	for len(s) > n {
		cut := strings.LastIndexByte(s[:n], ' ')
		if cut <= 0 {
			cut = n
		}
		out = append(out, s[:cut])
		s = strings.TrimLeft(s[cut:], " ")
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}
