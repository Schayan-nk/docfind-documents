package docxhtml

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Schayan-nk/docfind-documents/internal/ooxml"
	"github.com/Schayan-nk/docfind-documents/internal/xmltree"
)

// Word keeps most formatting in named styles and only the differences on the
// run itself, so a renderer that reads w:rPr alone shows a document with no
// headings, no fonts and no table borders. This file resolves a style to its
// effective properties: document defaults, then the style chain, then whatever
// the paragraph or run states directly.

type runProps struct {
	bold, italic, underline, strike bool
	rtl                             bool
	sizeHalfPt                      int    // w:sz is in half-points
	color                           string // validated hex
	highlight                       string
	font                            string
	vertAlign                       string // superscript | subscript
	caps                            bool
}

type paraProps struct {
	styleID     string
	heading     int
	align       string
	rtl         bool
	explicitLTR bool
	indentLeft  int // twips
	spaceBefore int
	spaceAfter  int
	numID       string
	numLevel    int
	run         runProps // formatting the paragraph applies to all its runs
	shading     string
}

func (p paraProps) css() []string {
	var s []string
	if p.align != "" {
		s = append(s, "text-align:"+p.align)
	}
	if p.indentLeft > 0 {
		s = append(s, fmt.Sprintf("padding-inline-start:%.1fpt", float64(p.indentLeft)/20))
	}
	if p.spaceBefore > 0 {
		s = append(s, fmt.Sprintf("margin-top:%.1fpt", float64(p.spaceBefore)/20))
	}
	if p.spaceAfter > 0 {
		s = append(s, fmt.Sprintf("margin-bottom:%.1fpt", float64(p.spaceAfter)/20))
	}
	if p.shading != "" {
		s = append(s, "background:"+p.shading)
	}
	return append(s, p.run.css()...)
}

func (r runProps) css() []string {
	var s []string
	if r.bold {
		s = append(s, "font-weight:700")
	}
	if r.italic {
		s = append(s, "font-style:italic")
	}
	switch {
	case r.underline && r.strike:
		s = append(s, "text-decoration:underline line-through")
	case r.underline:
		s = append(s, "text-decoration:underline")
	case r.strike:
		s = append(s, "text-decoration:line-through")
	}
	if r.sizeHalfPt > 0 {
		s = append(s, fmt.Sprintf("font-size:%.1fpt", float64(r.sizeHalfPt)/2))
	}
	if r.color != "" {
		s = append(s, "color:"+r.color)
	}
	if r.highlight != "" {
		s = append(s, "background:"+r.highlight)
	}
	if r.font != "" {
		s = append(s, `font-family:"`+r.font+`",serif`)
	}
	switch r.vertAlign {
	case "superscript":
		s = append(s, "vertical-align:super", "font-size:smaller")
	case "subscript":
		s = append(s, "vertical-align:sub", "font-size:smaller")
	}
	if r.caps {
		s = append(s, "text-transform:uppercase")
	}
	return s
}

// --- the style table --------------------------------------------------------

type styleEntry struct {
	id      string
	basedOn string
	name    string
	pPr     *xmltree.Node
	rPr     *xmltree.Node
}

type styleTable struct {
	byID       map[string]*styleEntry
	defaultRPr *xmltree.Node
	defaultPPr *xmltree.Node
}

func loadStyles(pkg *ooxml.Package) *styleTable {
	st := &styleTable{byID: map[string]*styleEntry{}}

	part := pkg.Part("word/styles.xml")
	if part == nil {
		return st
	}
	doc, err := xmltree.Parse(part.Data)
	if err != nil {
		return st
	}
	root := doc.Root()

	if d := root.Child("w:docDefaults"); d != nil {
		if rd := d.Child("w:rPrDefault"); rd != nil {
			st.defaultRPr = rd.Child("w:rPr")
		}
		if pd := d.Child("w:pPrDefault"); pd != nil {
			st.defaultPPr = pd.Child("w:pPr")
		}
	}

	for _, s := range root.ChildrenNamed("w:style") {
		id := s.Attr("w:styleId")
		if id == "" {
			continue
		}
		e := &styleEntry{id: id, pPr: s.Child("w:pPr"), rPr: s.Child("w:rPr")}
		if n := s.Child("w:name"); n != nil {
			e.name = n.Attr("w:val")
		}
		if b := s.Child("w:basedOn"); b != nil {
			e.basedOn = b.Attr("w:val")
		}
		st.byID[id] = e
	}
	return st
}

// chain returns a style and its ancestors, outermost first, so later entries
// override earlier ones. The depth limit stops a malformed file with a cycle in
// its basedOn chain from spinning forever.
func (st *styleTable) chain(id string) []*styleEntry {
	var out []*styleEntry
	seen := map[string]bool{}
	for id != "" && !seen[id] && len(out) < 16 {
		seen[id] = true
		e, ok := st.byID[id]
		if !ok {
			break
		}
		out = append([]*styleEntry{e}, out...)
		id = e.basedOn
	}
	return out
}

// --- resolving --------------------------------------------------------------

func (r *renderer) paragraphProps(pPr *xmltree.Node) paraProps {
	var p paraProps

	apply := func(n *xmltree.Node) {
		if n == nil {
			return
		}
		if j := n.Child("w:jc"); j != nil {
			switch j.Attr("w:val") {
			case "center":
				p.align = "center"
			case "right", "end":
				p.align = "right"
			case "both", "distribute":
				p.align = "justify"
			case "left", "start":
				p.align = "left"
			}
		}
		if b := n.Child("w:bidi"); b != nil {
			if isOn(b) {
				p.rtl, p.explicitLTR = true, false
			} else {
				p.rtl, p.explicitLTR = false, true
			}
		}
		if ind := n.Child("w:ind"); ind != nil {
			for _, a := range []string{"w:start", "w:left"} {
				if v := ind.Attr(a); v != "" {
					if n, err := strconv.Atoi(v); err == nil && n > 0 {
						p.indentLeft = n
					}
				}
			}
		}
		if sp := n.Child("w:spacing"); sp != nil {
			if v, err := strconv.Atoi(sp.Attr("w:before")); err == nil && v > 0 {
				p.spaceBefore = v
			}
			if v, err := strconv.Atoi(sp.Attr("w:after")); err == nil && v > 0 {
				p.spaceAfter = v
			}
		}
		if sh := n.Child("w:shd"); sh != nil {
			if c := colorValue(sh.Attr("w:fill")); c != "" {
				p.shading = c
			}
		}
		if np := n.Child("w:numPr"); np != nil {
			if id := np.Child("w:numId"); id != nil {
				if v := id.Attr("w:val"); v != "" && v != "0" {
					p.numID = v
				}
			}
			if lv := np.Child("w:ilvl"); lv != nil {
				if v, err := strconv.Atoi(lv.Attr("w:val")); err == nil && v >= 0 && v < 9 {
					p.numLevel = v
				}
			}
		}
	}

	apply(r.styles.defaultPPr)

	if pPr != nil {
		if ps := pPr.Child("w:pStyle"); ps != nil {
			p.styleID = ps.Attr("w:val")
		}
	}
	for _, e := range r.styles.chain(p.styleID) {
		apply(e.pPr)
		if h := headingLevel(e); h > 0 {
			p.heading = h
		}
	}
	apply(pPr)

	// Run formatting the paragraph carries, so a heading is bold even when its
	// runs say nothing.
	p.run = r.runProps(nil, p.styleID)
	if pPr != nil {
		p.run = mergeRunProps(p.run, readRunProps(pPr.Child("w:rPr")))
	}
	return p
}

var headingRe = regexp.MustCompile(`(?i)^heading\s*([1-6])$`)

func headingLevel(e *styleEntry) int {
	for _, s := range []string{e.name, e.id} {
		if m := headingRe.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	return 0
}

// runProps resolves a run's formatting through the document defaults, the
// paragraph's style chain, the run's own character style, and finally its
// direct properties.
func (r *renderer) runProps(rPr *xmltree.Node, paraStyleID string) runProps {
	out := readRunProps(r.styles.defaultRPr)
	for _, e := range r.styles.chain(paraStyleID) {
		out = mergeRunProps(out, readRunProps(e.rPr))
	}
	if rPr != nil {
		if cs := rPr.Child("w:rStyle"); cs != nil {
			for _, e := range r.styles.chain(cs.Attr("w:val")) {
				out = mergeRunProps(out, readRunProps(e.rPr))
			}
		}
		out = mergeRunProps(out, readRunProps(rPr))
	}
	return out
}

// readRunProps reads one w:rPr without inheritance.
func readRunProps(rPr *xmltree.Node) runProps {
	var p runProps
	if rPr == nil {
		return p
	}
	p.bold = isOn(rPr.Child("w:b")) || isOn(rPr.Child("w:bCs"))
	p.italic = isOn(rPr.Child("w:i")) || isOn(rPr.Child("w:iCs"))
	p.strike = isOn(rPr.Child("w:strike"))
	p.caps = isOn(rPr.Child("w:caps"))
	p.rtl = isOn(rPr.Child("w:rtl"))

	if u := rPr.Child("w:u"); u != nil && u.Attr("w:val") != "none" {
		p.underline = true
	}
	for _, name := range []string{"w:sz", "w:szCs"} {
		if s := rPr.Child(name); s != nil {
			if v, err := strconv.Atoi(s.Attr("w:val")); err == nil && v > 0 && v < 2000 {
				p.sizeHalfPt = v
			}
		}
	}
	if c := rPr.Child("w:color"); c != nil {
		p.color = colorValue(c.Attr("w:val"))
	}
	if h := rPr.Child("w:highlight"); h != nil {
		p.highlight = highlightColor(h.Attr("w:val"))
	}
	if f := rPr.Child("w:rFonts"); f != nil {
		// cs first: Persian text carries its font there.
		for _, a := range []string{"w:cs", "w:ascii", "w:hAnsi"} {
			if v := fontName(f.Attr(a)); v != "" {
				p.font = v
				break
			}
		}
	}
	if v := rPr.Child("w:vertAlign"); v != nil {
		switch v.Attr("w:val") {
		case "superscript", "subscript":
			p.vertAlign = v.Attr("w:val")
		}
	}
	return p
}

// mergeRunProps layers b over a, where anything b states wins.
func mergeRunProps(a, b runProps) runProps {
	out := a
	out.bold = a.bold || b.bold
	out.italic = a.italic || b.italic
	out.underline = a.underline || b.underline
	out.strike = a.strike || b.strike
	out.caps = a.caps || b.caps
	out.rtl = a.rtl || b.rtl
	if b.sizeHalfPt > 0 {
		out.sizeHalfPt = b.sizeHalfPt
	}
	if b.color != "" {
		out.color = b.color
	}
	if b.highlight != "" {
		out.highlight = b.highlight
	}
	if b.font != "" {
		out.font = b.font
	}
	if b.vertAlign != "" {
		out.vertAlign = b.vertAlign
	}
	return out
}

// --- value filtering --------------------------------------------------------
//
// Everything below turns a value out of the document into something that is
// safe to put in a style attribute. The document is not hostile in practice,
// but it is a file from disk being written into a page, so each value is
// matched against a shape rather than trusted.

var hexRe = regexp.MustCompile(`^[0-9A-Fa-f]{6}$`)

func colorValue(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "#")
	if v == "" || strings.EqualFold(v, "auto") || strings.EqualFold(v, "none") {
		return ""
	}
	if !hexRe.MatchString(v) {
		return ""
	}
	if strings.EqualFold(v, "FFFFFF") {
		return "" // white on white is worse than the default
	}
	return "#" + strings.ToUpper(v)
}

// highlightColor maps Word's fixed highlight names; anything else is dropped.
var highlights = map[string]string{
	"yellow": "#FFFF00", "green": "#00FF00", "cyan": "#00FFFF", "magenta": "#FF00FF",
	"blue": "#0000FF", "red": "#FF0000", "darkBlue": "#000080", "darkCyan": "#008080",
	"darkGreen": "#008000", "darkMagenta": "#800080", "darkRed": "#800000",
	"darkYellow": "#808000", "darkGray": "#808080", "lightGray": "#C0C0C0",
}

func highlightColor(v string) string {
	if c, ok := highlights[v]; ok {
		return c
	}
	return ""
}

// fontName keeps a font family that can go inside quotes in a style attribute.
// Theme references such as "minorHAnsi" are dropped rather than guessed at.
var fontRe = regexp.MustCompile(`^[\p{L}\p{N} .\-_]{1,64}$`)

func fontName(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.HasPrefix(v, "major") || strings.HasPrefix(v, "minor") {
		return ""
	}
	if !fontRe.MatchString(v) {
		return ""
	}
	return v
}

// --- numbering --------------------------------------------------------------

// loadNumbering resolves numId to the list format at each level, which is all
// that is needed to decide between a bulleted and a numbered list.
func loadNumbering(pkg *ooxml.Package) map[string]map[int]string {
	out := map[string]map[int]string{}

	part := pkg.Part("word/numbering.xml")
	if part == nil {
		return out
	}
	doc, err := xmltree.Parse(part.Data)
	if err != nil {
		return out
	}
	root := doc.Root()

	// abstractNumId -> level -> format
	abstract := map[string]map[int]string{}
	for _, an := range root.ChildrenNamed("w:abstractNum") {
		id := an.Attr("w:abstractNumId")
		if id == "" {
			continue
		}
		levels := map[int]string{}
		for _, lvl := range an.ChildrenNamed("w:lvl") {
			n, err := strconv.Atoi(lvl.Attr("w:ilvl"))
			if err != nil {
				continue
			}
			if f := lvl.Child("w:numFmt"); f != nil {
				levels[n] = f.Attr("w:val")
			}
		}
		abstract[id] = levels
	}

	for _, num := range root.ChildrenNamed("w:num") {
		id := num.Attr("w:numId")
		if id == "" {
			continue
		}
		if a := num.Child("w:abstractNumId"); a != nil {
			if levels, ok := abstract[a.Attr("w:val")]; ok {
				out[id] = levels
			}
		}
	}
	return out
}
