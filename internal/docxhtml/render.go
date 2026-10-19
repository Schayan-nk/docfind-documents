// Package docxhtml renders a .docx as HTML for on-screen viewing.
//
// The point is to show the document, not a transcript of it: the tables, the
// fonts, the bold headings, the signature image, the right-to-left paragraphs.
// A list of extracted words tells you a file mentions a valve; it does not tell
// you whether the proforma invoice is the one with the right total on it.
//
// This is a viewer, not a printer. Page geometry, headers and footers, footnotes
// and field codes are deliberately out of scope -- for the real thing there is
// "Open in Word". What it does cover is everything that appears in the body of
// the documents this system generates.
//
// Every string that comes out of the document is escaped, and every style value
// is filtered against a fixed set of shapes before it reaches the page. The
// output is a fragment with no script in it.
package docxhtml

import (
	"encoding/base64"
	"fmt"
	"html"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/Schayan-nk/docfind-documents/internal/ooxml"
	"github.com/Schayan-nk/docfind-documents/internal/xmltree"
)

// Options tunes the rendering.
type Options struct {
	// MaxImageBytes caps a single embedded image. Pictures become data URIs, so
	// a scanned page at full resolution would otherwise be sent to the viewer
	// in its entirety. Zero uses a sane default.
	MaxImageBytes int

	// MaxTotalImageBytes caps every image together, for the same reason.
	MaxTotalImageBytes int
}

func (o Options) withDefaults() Options {
	if o.MaxImageBytes <= 0 {
		o.MaxImageBytes = 4 << 20
	}
	if o.MaxTotalImageBytes <= 0 {
		o.MaxTotalImageBytes = 24 << 20
	}
	return o
}

// Render turns a document into an HTML fragment.
func Render(pkg *ooxml.Package, opt Options) (string, error) {
	opt = opt.withDefaults()

	part := pkg.Part("word/document.xml")
	if part == nil {
		return "", fmt.Errorf("docxhtml: this file has no word/document.xml; it is not a Word document")
	}
	doc, err := xmltree.Parse(part.Data)
	if err != nil {
		return "", fmt.Errorf("docxhtml: parsing the document: %w", err)
	}
	body := doc.Root().Child("w:body")
	if body == nil {
		return "", fmt.Errorf("docxhtml: the document has no body")
	}

	r := &renderer{
		pkg:    pkg,
		opt:    opt,
		styles: loadStyles(pkg),
		numFmt: loadNumbering(pkg),
		rels:   loadRels(pkg, "word/document.xml"),
	}

	var b strings.Builder
	b.WriteString(`<div class="dx-doc"`)
	if r.documentIsRTL(body) {
		b.WriteString(` dir="rtl"`)
	}
	b.WriteString(">")
	r.blocks(&b, body)
	b.WriteString("</div>")
	return b.String(), nil
}

type renderer struct {
	pkg    *ooxml.Package
	opt    Options
	styles *styleTable
	numFmt map[string]map[int]string // numId -> level -> format
	rels   map[string]string         // r:id -> part name

	imageBytes int
	listStack  []string // open list elements, innermost last
}

// documentIsRTL reports whether the document reads right to left overall, which
// decides how the page itself is laid out rather than any single paragraph.
func (r *renderer) documentIsRTL(body *xmltree.Node) bool {
	rtl, ltr := 0, 0
	for _, p := range body.FindAll("w:p") {
		if pPr := p.Child("w:pPr"); pPr != nil && isOn(pPr.Child("w:bidi")) {
			rtl++
			continue
		}
		ltr++
	}
	return rtl > ltr
}

// blocks renders the block-level children of a body or table cell.
func (r *renderer) blocks(b *strings.Builder, parent *xmltree.Node) {
	for _, n := range parent.Children {
		switch n.Name {
		case "w:p":
			r.paragraph(b, n)
		case "w:tbl":
			r.closeLists(b, 0)
			r.table(b, n)
		case "w:sdt":
			// A content control wraps ordinary content; render what is inside.
			if c := n.Child("w:sdtContent"); c != nil {
				r.blocks(b, c)
			}
		}
	}
	r.closeLists(b, 0)
}

// --- paragraphs -------------------------------------------------------------

func (r *renderer) paragraph(b *strings.Builder, p *xmltree.Node) {
	pPr := p.Child("w:pPr")
	props := r.paragraphProps(pPr)

	// A numbered or bulleted paragraph becomes a list item, so it lines up and
	// carries its marker the way it does in Word.
	if props.numID != "" {
		r.openLists(b, props)
		b.WriteString("<li")
		writeStyle(b, props.css())
		if props.rtl {
			b.WriteString(` dir="rtl"`)
		}
		b.WriteString(">")
		r.inline(b, p, props)
		b.WriteString("</li>")
		return
	}
	r.closeLists(b, 0)

	tag := "p"
	if props.heading > 0 && props.heading <= 6 {
		tag = "h" + strconv.Itoa(props.heading)
	}

	b.WriteString("<" + tag)
	writeStyle(b, props.css())
	if props.rtl {
		b.WriteString(` dir="rtl"`)
	} else if props.explicitLTR {
		b.WriteString(` dir="ltr"`)
	}
	b.WriteString(">")

	before := b.Len()
	r.inline(b, p, props)
	if b.Len() == before {
		// An empty paragraph is spacing in the original; keep it visible.
		b.WriteString("<br>")
	}
	b.WriteString("</" + tag + ">")
}

// inline renders the runs, hyperlinks and pictures inside a paragraph.
func (r *renderer) inline(b *strings.Builder, parent *xmltree.Node, pp paraProps) {
	for _, n := range parent.Children {
		switch n.Name {
		case "w:r":
			r.run(b, n, pp)
		case "w:hyperlink":
			href := ""
			if id := n.Attr("r:id"); id != "" {
				href = r.rels["__external__"+id]
			}
			if href != "" && safeHref(href) {
				b.WriteString(`<a href="` + html.EscapeString(href) + `" rel="noreferrer noopener">`)
				r.inline(b, n, pp)
				b.WriteString("</a>")
			} else {
				r.inline(b, n, pp)
			}
		case "w:smartTag", "w:ins", "w:sdt", "w:sdtContent", "w:bookmarkStart":
			r.inline(b, n, pp)
		case "w:del":
			// Deleted text is not part of the document as it stands.
		}
	}
}

func (r *renderer) run(b *strings.Builder, run *xmltree.Node, pp paraProps) {
	rp := r.runProps(run.Child("w:rPr"), pp.styleID)

	var inner strings.Builder
	for _, n := range run.Children {
		switch n.Name {
		case "w:t":
			inner.WriteString(html.EscapeString(n.TextContent()))
		case "w:delText":
			// omitted, as above
		case "w:br":
			if n.Attr("w:type") == "page" {
				inner.WriteString(`</span><div class="dx-pagebreak"></div><span>`)
			} else {
				inner.WriteString("<br>")
			}
		case "w:tab":
			inner.WriteString(`<span class="dx-tab"></span>`)
		case "w:noBreakHyphen":
			inner.WriteString("&#8209;")
		case "w:sym":
			if c := n.Attr("w:char"); c != "" {
				if v, err := strconv.ParseInt(c, 16, 32); err == nil && v >= 0x20 {
					// Symbol fonts map into the private use area; F0xx is the
					// common encoding for Wingdings and friends.
					if v >= 0xF000 {
						v -= 0xF000
					}
					inner.WriteString("&#" + strconv.FormatInt(v, 10) + ";")
				}
			}
		case "w:drawing", "w:pict", "w:object":
			r.picture(&inner, n)
		}
	}
	if inner.Len() == 0 {
		return
	}

	style := rp.css()
	if len(style) == 0 && !rp.rtl {
		b.WriteString(inner.String())
		return
	}
	b.WriteString("<span")
	writeStyle(b, style)
	if rp.rtl {
		b.WriteString(` dir="rtl"`)
	}
	b.WriteString(">")
	b.WriteString(inner.String())
	b.WriteString("</span>")
}

// --- pictures ---------------------------------------------------------------

func (r *renderer) picture(b *strings.Builder, n *xmltree.Node) {
	var relID string
	for _, blip := range n.FindAll("a:blip") {
		if id := blip.Attr("r:embed"); id != "" {
			relID = id
			break
		}
	}
	if relID == "" {
		// The VML shape form, still produced by some tools.
		for _, d := range n.FindAll("v:imagedata") {
			if id := d.Attr("r:id"); id != "" {
				relID = id
				break
			}
		}
	}
	if relID == "" {
		return
	}

	target := r.rels[relID]
	if target == "" {
		return
	}
	part := r.pkg.Part(target)
	if part == nil {
		return
	}
	if len(part.Data) > r.opt.MaxImageBytes ||
		r.imageBytes+len(part.Data) > r.opt.MaxTotalImageBytes {
		b.WriteString(`<span class="dx-note">[image omitted: too large to preview]</span>`)
		return
	}
	r.imageBytes += len(part.Data)

	mime := http.DetectContentType(part.Data)
	if !strings.HasPrefix(mime, "image/") {
		mime = mimeFromExt(target)
	}
	if mime == "" {
		return
	}

	b.WriteString(`<img class="dx-img" alt="" src="data:` + mime + ";base64," +
		base64.StdEncoding.EncodeToString(part.Data) + `"`)

	// Word stores the drawn size in EMU; carrying it over keeps a signature the
	// size it was placed at rather than its pixel size.
	if w, h := extentPixels(n); w > 0 {
		b.WriteString(` style="width:` + strconv.Itoa(w) + `px`)
		if h > 0 {
			b.WriteString(";height:" + strconv.Itoa(h) + "px")
		}
		b.WriteString(`"`)
	}
	b.WriteString(">")
}

// extentPixels converts a drawing's EMU extent to CSS pixels at 96 DPI.
func extentPixels(n *xmltree.Node) (int, int) {
	for _, e := range n.FindAll("wp:extent") {
		cx, _ := strconv.Atoi(e.Attr("cx"))
		cy, _ := strconv.Atoi(e.Attr("cy"))
		if cx > 0 {
			return cx / 9525, cy / 9525
		}
	}
	return 0, 0
}

func mimeFromExt(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".emf", ".wmf":
		return "" // no browser renders these
	}
	return ""
}

// --- tables -----------------------------------------------------------------

func (r *renderer) table(b *strings.Builder, tbl *xmltree.Node) {
	tblPr := tbl.Child("w:tblPr")

	b.WriteString(`<table class="dx-table"`)
	if tblPr != nil && isOn(tblPr.Child("w:bidiVisual")) {
		b.WriteString(` dir="rtl"`)
	}
	var style []string
	if tblPr != nil {
		if w := tblPr.Child("w:tblW"); w != nil {
			if css := tableWidth(w); css != "" {
				style = append(style, "width:"+css)
			}
		}
	}
	writeStyle(b, style)
	b.WriteString(">")

	for _, row := range tbl.ChildrenNamed("w:tr") {
		b.WriteString("<tr>")
		for _, cell := range row.ChildrenNamed("w:tc") {
			r.cell(b, cell)
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table>")
}

func (r *renderer) cell(b *strings.Builder, cell *xmltree.Node) {
	tcPr := cell.Child("w:tcPr")

	// A cell continuing a vertical merge is drawn by the cell above it.
	if tcPr != nil {
		if vm := tcPr.Child("w:vMerge"); vm != nil {
			if v := vm.Attr("w:val"); v == "" || v == "continue" {
				return
			}
		}
	}

	b.WriteString("<td")
	if tcPr != nil {
		if gs := tcPr.Child("w:gridSpan"); gs != nil {
			if n, err := strconv.Atoi(gs.Attr("w:val")); err == nil && n > 1 {
				b.WriteString(` colspan="` + strconv.Itoa(n) + `"`)
			}
		}
	}

	var style []string
	if tcPr != nil {
		if sh := tcPr.Child("w:shd"); sh != nil {
			if c := colorValue(sh.Attr("w:fill")); c != "" {
				style = append(style, "background:"+c)
			}
		}
		if va := tcPr.Child("w:vAlign"); va != nil {
			switch va.Attr("w:val") {
			case "center":
				style = append(style, "vertical-align:middle")
			case "bottom":
				style = append(style, "vertical-align:bottom")
			}
		}
		if w := tcPr.Child("w:tcW"); w != nil {
			if css := tableWidth(w); css != "" {
				style = append(style, "width:"+css)
			}
		}
	}
	writeStyle(b, style)
	b.WriteString(">")

	r.blocks(b, cell)
	b.WriteString("</td>")
}

func tableWidth(w *xmltree.Node) string {
	val := w.Attr("w:w")
	switch w.Attr("w:type") {
	case "pct":
		// Fiftieths of a percent in the modern form.
		if n, err := strconv.Atoi(strings.TrimSuffix(val, "%")); err == nil && n > 0 {
			if n > 100 {
				return fmt.Sprintf("%.2f%%", float64(n)/50)
			}
			return strconv.Itoa(n) + "%"
		}
	case "dxa":
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			return fmt.Sprintf("%.1fpt", float64(n)/20) // twips to points
		}
	}
	return ""
}

// --- lists ------------------------------------------------------------------

func (r *renderer) openLists(b *strings.Builder, pp paraProps) {
	tag := "ul"
	if f := r.numFmt[pp.numID][pp.numLevel]; f != "" && f != "bullet" {
		tag = "ol"
	}
	// Close any deeper levels, then open down to this one.
	r.closeLists(b, pp.numLevel+1)
	for len(r.listStack) <= pp.numLevel {
		b.WriteString("<" + tag + ` class="dx-list">`)
		r.listStack = append(r.listStack, tag)
	}
}

func (r *renderer) closeLists(b *strings.Builder, depth int) {
	for len(r.listStack) > depth {
		b.WriteString("</" + r.listStack[len(r.listStack)-1] + ">")
		r.listStack = r.listStack[:len(r.listStack)-1]
	}
}

// --- relationships ----------------------------------------------------------

// loadRels maps relationship ids to the part they point at. External targets
// are kept under a separate key so a hyperlink can never be mistaken for a part
// inside the package.
func loadRels(pkg *ooxml.Package, partName string) map[string]string {
	out := map[string]string{}

	dir, file := path.Split(partName)
	relsName := dir + "_rels/" + file + ".rels"
	part := pkg.Part(relsName)
	if part == nil {
		return out
	}
	doc, err := xmltree.Parse(part.Data)
	if err != nil {
		return out
	}
	for _, rel := range doc.Root().FindAll("Relationship") {
		id, target := rel.Attr("Id"), rel.Attr("Target")
		if id == "" || target == "" {
			continue
		}
		if strings.EqualFold(rel.Attr("TargetMode"), "External") {
			out["__external__"+id] = target
			continue
		}
		out[id] = path.Clean(path.Join(dir, target))
	}
	return out
}

func safeHref(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") ||
		strings.HasPrefix(l, "mailto:")
}

// --- small helpers ----------------------------------------------------------

func isOn(n *xmltree.Node) bool {
	if n == nil {
		return false
	}
	switch n.Attr("w:val") {
	case "", "1", "true", "on":
		return true
	}
	return false
}

// writeStyle emits a style attribute, with the declarations sorted so the same
// document always produces the same HTML.
func writeStyle(b *strings.Builder, decls []string) {
	if len(decls) == 0 {
		return
	}
	sort.Strings(decls)
	b.WriteString(` style="` + html.EscapeString(strings.Join(decls, ";")) + `"`)
}
