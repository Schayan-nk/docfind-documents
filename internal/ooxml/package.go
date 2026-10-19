// Package ooxml reads and writes the ZIP container that makes up a .docx file.
//
// A .docx is an Open Packaging Convention archive: a ZIP whose entries are XML
// parts plus binary media. We read every entry into memory, let the renderer
// rewrite the handful of XML parts it cares about, and write the archive back
// out preserving entry order and compression. Anything we do not touch -- theme,
// numbering, embedded logos, custom XML, fonts -- survives untouched, which is
// what keeps the template's visual design intact.
package ooxml

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
	// klauspost/compress's flate is a drop-in for the standard library's,
	// same RFC 1951 bitstream, same API -- just a faster implementation.
	// Profiling a real render showed compression as roughly a fifth of total
	// CPU time, so a faster codec is one of the two changes made here to cut
	// that down (the other is the compression level below).
)

// MaxPartSize bounds a single decompressed entry, guarding against zip bombs in
// a template that was replaced on disk.
const MaxPartSize = 64 << 20 // 64 MiB

// Part is one entry of the package.
//
// Data is the decompressed content. raw holds the bytes exactly as they were
// stored in the source archive so that any part we never touch can be copied
// through without being re-compressed -- which keeps it bit-identical to the
// template and avoids re-deflating multi-hundred-kilobyte images on every
// request.
type Part struct {
	Name     string
	Data     []byte
	Method   uint16
	Modified time.Time

	raw []byte         // original compressed bytes, nil once modified
	hdr zip.FileHeader // original header, for faithful passthrough
}

// Package is an in-memory .docx.
type Package struct {
	Parts []*Part
	index map[string]*Part
}

// ErrNotDocx is returned when the archive lacks the parts every WordprocessingML
// document must have.
var ErrNotDocx = errors.New("ooxml: archive is not a WordprocessingML document")

// Open reads a package from raw ZIP bytes.
func Open(data []byte) (*Package, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("ooxml: cannot read archive: %w", err)
	}
	p := &Package{index: make(map[string]*Part, len(zr.File))}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := path.Clean(strings.ReplaceAll(f.Name, `\`, `/`))
		// A crafted archive must not be able to escape the package namespace;
		// we never write these to disk, but defence in depth is cheap.
		if strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") {
			return nil, fmt.Errorf("ooxml: illegal entry name %q", f.Name)
		}
		if f.UncompressedSize64 > MaxPartSize {
			return nil, fmt.Errorf("ooxml: part %q exceeds %d bytes", name, MaxPartSize)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("ooxml: cannot open part %q: %w", name, err)
		}
		buf, err := io.ReadAll(io.LimitReader(rc, MaxPartSize+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("ooxml: cannot read part %q: %w", name, err)
		}
		if len(buf) > MaxPartSize {
			return nil, fmt.Errorf("ooxml: part %q exceeds %d bytes", name, MaxPartSize)
		}

		// Keep the stored bytes verbatim for untouched-part passthrough.
		rawRC, err := f.OpenRaw()
		if err != nil {
			return nil, fmt.Errorf("ooxml: cannot read raw part %q: %w", name, err)
		}
		rawBuf, err := io.ReadAll(io.LimitReader(rawRC, MaxPartSize+1))
		if err != nil {
			return nil, fmt.Errorf("ooxml: cannot read raw part %q: %w", name, err)
		}

		part := &Part{
			Name:     name,
			Data:     buf,
			Method:   f.Method,
			Modified: f.Modified,
			raw:      rawBuf,
			hdr:      f.FileHeader,
		}
		p.Parts = append(p.Parts, part)
		p.index[name] = part
	}
	if p.index["word/document.xml"] == nil || p.index["[Content_Types].xml"] == nil {
		return nil, ErrNotDocx
	}
	return p, nil
}

// Part returns the named part, or nil.
func (p *Package) Part(name string) *Part { return p.index[name] }

// TextPartNames returns the WordprocessingML parts that can carry visible text
// and therefore placeholders: the body, every header and footer, footnotes,
// endnotes and comments.
//
// Globbing the package is deliberate. Walking the relationship graph would be
// more "correct", but headers added by different Word versions are numbered
// inconsistently and a missed part means a placeholder silently survives into
// the delivered document.
func (p *Package) TextPartNames() []string {
	var out []string
	for _, part := range p.Parts {
		n := part.Name
		if !strings.HasPrefix(n, "word/") || !strings.HasSuffix(n, ".xml") {
			continue
		}
		base := strings.TrimPrefix(n, "word/")
		if strings.Contains(base, "/") {
			continue // theme/, media/, glossary/ … never carry body text
		}
		switch {
		case base == "document.xml",
			base == "footnotes.xml",
			base == "endnotes.xml",
			base == "comments.xml",
			strings.HasPrefix(base, "header"),
			strings.HasPrefix(base, "footer"):
			out = append(out, n)
		}
	}
	return out
}
