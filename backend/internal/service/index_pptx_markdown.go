package service

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PPTX originals are projected to server-generated Markdown inside the indexer.
// The projection is never a user-writable object: it is rebuilt from the exact
// pinned original bytes and published under that source revision's immutable
// prefix, so an ETag/version/size change produces a fresh projection.
const (
	PPTXMarkdownProjection = "pptx-markdown-v1"
	pptxMarkdownType       = "text/markdown; charset=utf-8"

	pptxMaxEntries       = 4096
	pptxMaxPartBytes     = 16 << 20 // one decompressed XML part
	pptxMaxExpandedBytes = 64 << 20 // all decompressed XML parts read
	pptxMaxRatio         = 200      // per-part expansion, above 1 MiB
	pptxMaxSlides        = 500
	pptxMaxXMLDepth      = 128
	pptxMaxXMLTokens     = 4_000_000 // across every part read
	pptxMaxMarkdownBytes = 4 << 20
	pptxMaxRelationships = 8192
)

// Fail-closed codes surfaced through FAILED index jobs and the status API.
const (
	PPTXErrMalformed     = "PPTX_MALFORMED"
	PPTXErrEncrypted     = "PPTX_ENCRYPTED_OR_LEGACY"
	PPTXErrTooManyParts  = "PPTX_TOO_MANY_ENTRIES"
	PPTXErrExpansion     = "PPTX_EXPANSION_LIMIT"
	PPTXErrTooManySlides = "PPTX_TOO_MANY_SLIDES"
	PPTXErrOutputLimit   = "PPTX_OUTPUT_TOO_LARGE"
	PPTXErrNoText        = "PPTX_NO_TEXT"
	LegacyPPTUnsupported = "LEGACY_PPT_UNSUPPORTED"
)

type pptxError struct{ code string }

func (e *pptxError) Error() string { return "pptx projection: " + e.code }

func pptxFail(code string) error { return &pptxError{code: code} }

// PPTXErrorCode returns the fail-closed code of a projection error.
func PPTXErrorCode(err error) (string, bool) {
	var target *pptxError
	if errors.As(err, &target) {
		return target.code, true
	}
	return "", false
}

type pptxPackage struct {
	files    map[string]*zip.File
	expanded int64
	tokens   int
}

// PPTXToMarkdown converts slide titles, body text, tables and speaker notes to
// Markdown. It reads only presentation, relationship, slide and notes XML parts;
// media, embeddings, macros and external targets are never opened or resolved.
func PPTXToMarkdown(data []byte, title string) ([]byte, error) {
	if int64(len(data)) > MaxIndexObjectBytes {
		return nil, pptxFail(PPTXErrExpansion)
	}
	if bytes.HasPrefix(data, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}) {
		// Password-protected OOXML and binary PowerPoint both use OLE CFB.
		return nil, pptxFail(PPTXErrEncrypted)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, pptxFail(PPTXErrMalformed)
	}
	if len(reader.File) > pptxMaxEntries {
		return nil, pptxFail(PPTXErrTooManyParts)
	}
	pkg := &pptxPackage{files: make(map[string]*zip.File, len(reader.File))}
	seen := map[string]bool{}
	for _, file := range reader.File {
		folded := strings.ToLower(file.Name)
		if seen[folded] || !pptxPartName(file.Name) {
			return nil, pptxFail(PPTXErrMalformed)
		}
		seen[folded] = true
		if file.Flags&0x1 != 0 {
			return nil, pptxFail(PPTXErrEncrypted)
		}
		pkg.files[file.Name] = file
	}
	slides, err := pkg.slideOrder()
	if err != nil {
		return nil, err
	}
	out := &pptxWriter{}
	if heading := pptxInline(title); heading != "" {
		out.line("# " + heading)
		out.line("")
	}
	hasText := false
	for i, slide := range slides {
		content, err := pkg.readSlide(slide, false)
		if err != nil {
			return nil, err
		}
		notes := pptxContent{}
		if target, err := pkg.notesFor(slide); err != nil {
			return nil, err
		} else if target != "" {
			if notes, err = pkg.readSlide(target, true); err != nil {
				return nil, err
			}
		}
		heading := "## Slide " + strconv.Itoa(i+1)
		if len(content.titles) > 0 {
			heading += ": " + strings.Join(content.titles, " / ")
		}
		out.line(heading)
		out.line("")
		for _, block := range content.blocks {
			out.line(block)
		}
		if len(content.blocks) > 0 {
			out.line("")
		}
		if len(notes.blocks) > 0 {
			out.line("### Speaker notes")
			out.line("")
			for _, block := range notes.blocks {
				out.line(block)
			}
			out.line("")
		}
		hasText = hasText || len(content.titles)+len(content.blocks)+len(notes.blocks) > 0
		if out.size > pptxMaxMarkdownBytes {
			return nil, pptxFail(PPTXErrOutputLimit)
		}
	}
	if !hasText {
		return nil, pptxFail(PPTXErrNoText)
	}
	result := bytes.TrimRight(out.buf.Bytes(), "\n")
	return append(result, '\n'), nil
}

type pptxWriter struct {
	buf  bytes.Buffer
	size int
}

func (w *pptxWriter) line(text string) {
	if w.size > pptxMaxMarkdownBytes || (text == "" && bytes.HasSuffix(w.buf.Bytes(), []byte("\n\n"))) {
		return
	}
	w.buf.WriteString(text)
	w.buf.WriteByte('\n')
	w.size += len(text) + 1
}

// Package part names are plain archive paths; anything that could alias another
// part or escape the archive root is ambiguous and rejected.
func pptxPartName(name string) bool {
	if name == "" || len(name) > 1024 || !utf8.ValidString(name) || strings.HasPrefix(name, "/") ||
		strings.ContainsAny(name, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// open bounds every decompressed byte: declared size, expansion ratio, the
// actual stream length (independent of the declared header) and the total.
func (p *pptxPackage) open(name string) (io.ReadCloser, error) {
	file := p.files[name]
	if file == nil {
		return nil, pptxFail(PPTXErrMalformed)
	}
	declared := file.UncompressedSize64
	if declared > pptxMaxPartBytes || p.expanded+int64(declared) > pptxMaxExpandedBytes {
		return nil, pptxFail(PPTXErrExpansion)
	}
	if declared > 1<<20 && (file.CompressedSize64 == 0 || declared/file.CompressedSize64 > pptxMaxRatio) {
		return nil, pptxFail(PPTXErrExpansion)
	}
	if file.Method != zip.Store && file.Method != zip.Deflate {
		return nil, pptxFail(PPTXErrMalformed)
	}
	body, err := file.Open()
	if err != nil {
		return nil, pptxFail(PPTXErrMalformed)
	}
	return &pptxLimited{body: body, pkg: p, left: int64(declared)}, nil
}

type pptxLimited struct {
	body io.ReadCloser
	pkg  *pptxPackage
	left int64
}

func (r *pptxLimited) Read(buffer []byte) (int, error) {
	if r.left <= 0 {
		// One extra byte proves a stream longer than its declared size.
		var probe [1]byte
		// archive/zip reports ErrFormat once a stream passes its declared size.
		n, err := r.body.Read(probe[:])
		switch {
		case n > 0 || errors.Is(err, zip.ErrFormat):
			return 0, pptxFail(PPTXErrExpansion)
		case err != nil && err != io.EOF:
			return 0, pptxFail(PPTXErrMalformed)
		}
		return 0, io.EOF
	}
	if int64(len(buffer)) > r.left {
		buffer = buffer[:r.left]
	}
	n, err := r.body.Read(buffer)
	r.left -= int64(n)
	r.pkg.expanded += int64(n)
	if r.pkg.expanded > pptxMaxExpandedBytes {
		return n, pptxFail(PPTXErrExpansion)
	}
	if err == io.EOF && r.left > 0 {
		return n, pptxFail(PPTXErrMalformed)
	}
	if err != nil && err != io.EOF {
		return n, pptxFail(PPTXErrMalformed)
	}
	return n, err
}

func (r *pptxLimited) Close() error { return r.body.Close() }

// walk streams one XML part with depth/token limits. DTDs are rejected so no
// entity declaration can be used; Go's decoder never fetches external entities.
func (p *pptxPackage) walk(name string, visit func(xml.Token, []string) error) error {
	body, err := p.open(name)
	if err != nil {
		return err
	}
	defer body.Close()
	decoder := xml.NewDecoder(body)
	stack := []string{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			if len(stack) != 0 {
				return pptxFail(PPTXErrMalformed)
			}
			return nil
		}
		if err != nil {
			if code, ok := PPTXErrorCode(err); ok {
				return pptxFail(code)
			}
			return pptxFail(PPTXErrMalformed)
		}
		if p.tokens++; p.tokens > pptxMaxXMLTokens {
			return pptxFail(PPTXErrExpansion)
		}
		switch value := token.(type) {
		case xml.Directive:
			return pptxFail(PPTXErrMalformed)
		case xml.StartElement:
			if stack = append(stack, value.Name.Local); len(stack) > pptxMaxXMLDepth {
				return pptxFail(PPTXErrMalformed)
			}
		}
		if err := visit(token, stack); err != nil {
			return err
		}
		if _, ok := token.(xml.EndElement); ok {
			stack = stack[:len(stack)-1]
		}
	}
}

func xmlAttr(element xml.StartElement, local string) string {
	for _, attribute := range element.Attr {
		if attribute.Name.Local == local {
			return attribute.Value
		}
	}
	return ""
}

type pptxRelationship struct{ kind, target string }

// relationships resolves internal targets relative to the source part. External
// targets are ignored; an internal target outside the archive root is malformed.
func (p *pptxPackage) relationships(source string) (map[string]pptxRelationship, error) {
	directory, file := path.Split(source)
	rels := directory + "_rels/" + file + ".rels"
	result := map[string]pptxRelationship{}
	if p.files[rels] == nil {
		return result, nil
	}
	err := p.walk(rels, func(token xml.Token, stack []string) error {
		element, ok := token.(xml.StartElement)
		if !ok || element.Name.Local != "Relationship" || len(stack) != 2 {
			return nil
		}
		if len(result) >= pptxMaxRelationships {
			return pptxFail(PPTXErrTooManyParts)
		}
		id, target := xmlAttr(element, "Id"), xmlAttr(element, "Target")
		if id == "" || target == "" {
			return pptxFail(PPTXErrMalformed)
		}
		if _, duplicate := result[id]; duplicate {
			return pptxFail(PPTXErrMalformed)
		}
		if strings.EqualFold(xmlAttr(element, "TargetMode"), "External") {
			result[id] = pptxRelationship{kind: "external"}
			return nil
		}
		resolved, ok := pptxResolve(directory, target)
		if !ok {
			return pptxFail(PPTXErrMalformed)
		}
		kind := xmlAttr(element, "Type")
		result[id] = pptxRelationship{kind: kind[strings.LastIndex(kind, "/")+1:], target: resolved}
		return nil
	})
	return result, err
}

func pptxResolve(directory, target string) (string, bool) {
	if strings.ContainsAny(target, "\\?#%\x00") {
		return "", false
	}
	joined := target
	if strings.HasPrefix(target, "/") {
		joined = strings.TrimPrefix(target, "/")
	} else {
		joined = directory + target
	}
	parts := []string{}
	for _, part := range strings.Split(joined, "/") {
		switch part {
		case "", ".":
		case "..":
			if len(parts) == 0 {
				return "", false
			}
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, part)
		}
	}
	resolved := strings.Join(parts, "/")
	return resolved, pptxPartName(resolved)
}

// slideOrder follows presentation.xml's sldIdLst, i.e. the deck's visible order,
// rather than archive file names.
func (p *pptxPackage) slideOrder() ([]string, error) {
	const presentation = "ppt/presentation.xml"
	if p.files[presentation] == nil {
		return nil, pptxFail(PPTXErrMalformed)
	}
	rels, err := p.relationships(presentation)
	if err != nil {
		return nil, err
	}
	slides := []string{}
	err = p.walk(presentation, func(token xml.Token, stack []string) error {
		element, ok := token.(xml.StartElement)
		if !ok || element.Name.Local != "sldId" || len(stack) < 2 || stack[len(stack)-2] != "sldIdLst" {
			return nil
		}
		if len(slides) >= pptxMaxSlides {
			return pptxFail(PPTXErrTooManySlides)
		}
		// The numeric id and r:id share a local name; only the namespaced
		// relationship attribute names the slide part.
		var rel pptxRelationship
		ok = false
		for _, attribute := range element.Attr {
			if attribute.Name.Local == "id" && attribute.Name.Space != "" {
				rel, ok = rels[attribute.Value]
			}
		}
		if !ok || rel.kind != "slide" || !strings.HasPrefix(rel.target, "ppt/slides/") || p.files[rel.target] == nil {
			return pptxFail(PPTXErrMalformed)
		}
		slides = append(slides, rel.target)
		return nil
	})
	return slides, err
}

func (p *pptxPackage) notesFor(slide string) (string, error) {
	rels, err := p.relationships(slide)
	if err != nil {
		return "", err
	}
	target := ""
	for _, rel := range rels {
		if rel.kind != "notesSlide" {
			continue
		}
		// At most one notes page keeps the output independent of map order.
		if target != "" || !strings.HasPrefix(rel.target, "ppt/notesSlides/") || p.files[rel.target] == nil {
			return "", pptxFail(PPTXErrMalformed)
		}
		target = rel.target
	}
	return target, nil
}

type pptxContent struct {
	titles []string
	blocks []string
}

// readSlide collects text in XML shape order. Title placeholders become the
// heading; slide numbers, dates, footers and notes-page slide images are omitted.
// For notes pages only the body placeholder is speaker-note text.
func (p *pptxPackage) readSlide(name string, notes bool) (pptxContent, error) {
	var (
		content   pptxContent
		shapeKind string // "", "title", "skip", "body"
		inShape   int
		paragraph strings.Builder
		level     int
		cell      []string
		row       []string
		table     [][]string
		inTable   int
		inCell    bool
		inText    bool
		shapeText []string
	)
	flushParagraph := func() {
		text := pptxInline(paragraph.String())
		paragraph.Reset()
		if text == "" {
			return
		}
		switch {
		case inCell:
			cell = append(cell, text)
		case shapeKind == "title":
			content.titles = append(content.titles, text)
		default:
			shapeText = append(shapeText, strings.Repeat("  ", level)+"- "+text)
		}
	}
	err := p.walk(name, func(token xml.Token, stack []string) error {
		depth := len(stack)
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "sp":
				if inShape == 0 {
					inShape, shapeKind, shapeText = depth, "", nil
				}
			case "ph":
				if inShape > 0 {
					shapeKind = pptxPlaceholderKind(xmlAttr(value, "type"), notes)
				}
			case "tbl":
				if inTable == 0 {
					inTable, table = depth, nil
				}
			case "tr":
				row = nil
			case "tc":
				inCell, cell = inTable > 0, nil
			case "p":
				paragraph.Reset()
				level = 0
			case "pPr":
				if lvl, err := strconv.Atoi(xmlAttr(value, "lvl")); err == nil && lvl > 0 && lvl < 9 {
					level = lvl
				}
			case "t":
				inText = true
			case "br":
				paragraph.WriteByte(' ')
			case "tab":
				paragraph.WriteByte(' ')
			}
		case xml.CharData:
			if inText {
				if paragraph.Len()+len(value) > pptxMaxMarkdownBytes {
					return pptxFail(PPTXErrOutputLimit)
				}
				paragraph.Write(value)
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "t":
				inText = false
			case "p":
				// a:p only; p:sp shapes never use a local "p" end at shape depth.
				if inShape > 0 || inCell {
					flushParagraph()
				}
			case "tc":
				if inCell {
					row = append(row, strings.Join(cell, " "))
				}
				inCell = false
			case "tr":
				if inTable > 0 && len(row) > 0 {
					table = append(table, row)
				}
			case "tbl":
				if inTable == depth {
					if rendered := pptxTable(table); rendered != "" && (!notes || shapeKind == "body") {
						content.blocks = append(content.blocks, rendered)
					}
					inTable = 0
				}
			case "sp":
				if inShape == depth {
					if shapeKind != "skip" && (!notes || shapeKind == "body") {
						content.blocks = append(content.blocks, shapeText...)
					}
					inShape, shapeKind, shapeText = 0, "", nil
				}
			}
		}
		return nil
	})
	return content, err
}

func pptxPlaceholderKind(kind string, notes bool) string {
	switch {
	case notes && kind == "body":
		return "body"
	case notes:
		return "skip"
	case kind == "title" || kind == "ctrTitle":
		return "title"
	case kind == "sldNum" || kind == "dt" || kind == "ftr" || kind == "hdr":
		return "skip"
	}
	return "body"
}

func pptxTable(rows [][]string) string {
	width := 0
	for _, row := range rows {
		width = max(width, len(row))
	}
	if width == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteByte('\n') // a table needs a blank line after list items
	for i, row := range rows {
		builder.WriteByte('|')
		for column := 0; column < width; column++ {
			value := ""
			if column < len(row) {
				value = strings.ReplaceAll(row[column], "|", `\|`)
			}
			fmt.Fprintf(&builder, " %s |", value)
		}
		if i == 0 {
			builder.WriteString("\n|")
			builder.WriteString(strings.Repeat(" --- |", width))
		}
		if i < len(rows)-1 {
			builder.WriteByte('\n')
		}
	}
	builder.WriteByte('\n')
	return builder.String()
}

// pptxInline normalizes untrusted slide text to one line of printable text.
func pptxInline(text string) string {
	text = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, " "))
	return strings.Join(strings.Fields(text), " ")
}
