// Package xlsx reads and writes the small subset of .xlsx workbooks this project needs: text cells in a sheet.
//
// An .xlsx file is a zip archive of XML parts, and a file from a user is untrusted input. The reader therefore
// uses only the standard library, never evaluates formulas, never follows references outside the archive, and
// enforces limits on the unpacked size, the number of rows and the number of columns. Writing produces plain
// workbooks that Excel, LibreOffice and Google Sheets open.
package xlsx

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
)

var (
	// ErrFormat: the data is not a readable .xlsx workbook (or is encrypted, or malformed).
	ErrFormat = errors.New("xlsx: not a valid workbook")
	// ErrTooManyLines: the sheet goes beyond Limits.MaxLines rows.
	ErrTooManyLines = errors.New("xlsx: too many rows")
)

// Limits bound what a file may cost to read. Zero values are replaced by the defaults of DefaultLimits.
type Limits struct {
	MaxLines    int    // highest row number looked at, blank rows included
	MaxColumns  int    // cells after this column are ignored
	MaxUnzipped uint64 // total declared size of all parts of the archive
	MaxPart     int64  // size of one XML part after unpacking
	MaxStrings  int    // entries of the shared string table
}

// DefaultLimits are meant for a user-facing import of at most a few thousand rows.
func DefaultLimits() Limits {
	return Limits{MaxLines: 5000, MaxColumns: 64, MaxUnzipped: 50 << 20, MaxPart: 16 << 20, MaxStrings: 200_000}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxLines <= 0 {
		l.MaxLines = d.MaxLines
	}
	if l.MaxColumns <= 0 {
		l.MaxColumns = d.MaxColumns
	}
	if l.MaxUnzipped <= 0 {
		l.MaxUnzipped = d.MaxUnzipped
	}
	if l.MaxPart <= 0 {
		l.MaxPart = d.MaxPart
	}
	if l.MaxStrings <= 0 {
		l.MaxStrings = d.MaxStrings
	}
	return l
}

const (
	maxParts     = 500
	nsRelations  = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	workbookPath = "xl/workbook.xml"
	relsPath     = "xl/_rels/workbook.xml.rels"
	stringsPath  = "xl/sharedStrings.xml"
)

// ReadFirstSheet calls fn for every row of the first sheet of the workbook, in order. line is the row number in
// the spreadsheet (starting at 1) and cells are the cell texts from column A on ("" for an empty cell). Rows
// that are not in the file are not reported. An error from fn stops the reading and is returned unchanged.
//
// Cells are returned as stored: numbers keep their digits, a date is its serial number, a formula is its last
// calculated value.
func ReadFirstSheet(data []byte, lim Limits, fn func(line int, cells []string) error) error {
	lim = lim.withDefaults()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrFormat, err)
	}
	if len(zr.File) > maxParts {
		return fmt.Errorf("%w: too many parts", ErrFormat)
	}
	parts := make(map[string]*zip.File, len(zr.File))
	var total uint64
	for _, f := range zr.File {
		total += f.UncompressedSize64
		if total > lim.MaxUnzipped {
			return fmt.Errorf("%w: unpacked size over the limit", ErrFormat)
		}
		parts[f.Name] = f
	}
	r := &reader{parts: parts, lim: lim}

	sheetPart, err := r.firstSheet()
	if err != nil {
		return err
	}
	shared, err := r.sharedStrings()
	if err != nil {
		return err
	}
	return r.sheet(sheetPart, shared, fn)
}

type reader struct {
	parts map[string]*zip.File
	lim   Limits
}

// limited stops a read after n bytes with ErrFormat, so one part cannot use unbounded memory.
type limited struct {
	r io.Reader
	n int64
}

func (l *limited) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, fmt.Errorf("%w: part over the size limit", ErrFormat)
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

// open returns a bounded reader of a part, or nil when the part does not exist.
func (r *reader) open(name string) (io.ReadCloser, error) {
	f := r.parts[name]
	if f == nil {
		return nil, nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFormat, err)
	}
	return struct {
		io.Reader
		io.Closer
	}{&limited{r: rc, n: r.lim.MaxPart}, rc}, nil
}

func decoder(rc io.Reader) *xml.Decoder {
	d := xml.NewDecoder(rc)
	d.Strict = true
	return d
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// firstSheet finds the part of the first sheet in workbook order.
func (r *reader) firstSheet() (string, error) {
	wb, err := r.open(workbookPath)
	if err != nil || wb == nil {
		return "", fmt.Errorf("%w: no workbook", ErrFormat)
	}
	defer func() { _ = wb.Close() }()
	rid := ""
	d := decoder(wb)
	for rid == "" {
		tok, err := d.Token()
		if err != nil {
			return "", fmt.Errorf("%w: workbook: %w", ErrFormat, err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sheet" {
			for _, a := range se.Attr {
				if a.Name.Local == "id" && (a.Name.Space == nsRelations || a.Name.Space == "r" || a.Name.Space == "") {
					rid = a.Value
				}
			}
			if rid == "" {
				return "", fmt.Errorf("%w: sheet without relationship", ErrFormat)
			}
		}
	}

	rels, err := r.open(relsPath)
	if err != nil || rels == nil {
		return "", fmt.Errorf("%w: no workbook relationships", ErrFormat)
	}
	defer func() { _ = rels.Close() }()
	d = decoder(rels)
	for {
		tok, err := d.Token()
		if err != nil {
			return "", fmt.Errorf("%w: sheet %s not found", ErrFormat, rid)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" && attr(se, "Id") == rid {
			target := attr(se, "Target")
			if strings.HasPrefix(target, "/") {
				return path.Clean(strings.TrimPrefix(target, "/")), nil
			}
			return path.Clean("xl/" + target), nil
		}
	}
}

// sharedStrings reads the shared string table; a workbook without one is fine.
func (r *reader) sharedStrings() ([]string, error) {
	rc, err := r.open(stringsPath)
	if err != nil || rc == nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	var out []string
	d := decoder(rc)
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: shared strings: %w", ErrFormat, err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "si" {
			if len(out) >= r.lim.MaxStrings {
				return nil, fmt.Errorf("%w: too many shared strings", ErrFormat)
			}
			s, err := readText(d)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
	}
}

// readText reads the text of the element the decoder just entered (<si> or <is>): all <t> elements, including
// those of rich text runs, and not the phonetic hints (<rPh>).
func readText(d *xml.Decoder) (string, error) {
	var sb strings.Builder
	depth := 1
	for depth > 0 {
		tok, err := d.Token()
		if err != nil {
			return "", fmt.Errorf("%w: %w", ErrFormat, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "rPh":
				if err := d.Skip(); err != nil {
					return "", fmt.Errorf("%w: %w", ErrFormat, err)
				}
			case "t":
				var s string
				if err := d.DecodeElement(&s, &t); err != nil {
					return "", fmt.Errorf("%w: %w", ErrFormat, err)
				}
				sb.WriteString(s)
			default:
				depth++
			}
		case xml.EndElement:
			depth--
		}
	}
	return sb.String(), nil
}

func (r *reader) sheet(part string, shared []string, fn func(int, []string) error) error {
	rc, err := r.open(part)
	if err != nil {
		return err
	}
	if rc == nil {
		return fmt.Errorf("%w: sheet part missing", ErrFormat)
	}
	defer func() { _ = rc.Close() }()

	var (
		d     = decoder(rc)
		line  int
		cells []string
		open  bool
	)
	flush := func() error {
		if !open {
			return nil
		}
		open = false
		return fn(line, cells)
	}
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return flush()
		}
		if err != nil {
			return fmt.Errorf("%w: sheet: %w", ErrFormat, err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "row":
			if err := flush(); err != nil {
				return err
			}
			next := line + 1
			if v := attr(se, "r"); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 || n <= line {
					return fmt.Errorf("%w: bad row number %q", ErrFormat, v)
				}
				next = n
			}
			if next > r.lim.MaxLines {
				return ErrTooManyLines
			}
			line, cells, open = next, nil, true
		case "c":
			if !open {
				return fmt.Errorf("%w: cell outside a row", ErrFormat)
			}
			col, val, err := r.cell(d, se, shared, len(cells))
			if err != nil {
				return err
			}
			if col >= r.lim.MaxColumns {
				continue
			}
			for len(cells) <= col {
				cells = append(cells, "")
			}
			cells[col] = val
		}
	}
}

// cell reads one <c> element. next is the column used when the cell has no reference.
func (r *reader) cell(d *xml.Decoder, se xml.StartElement, shared []string, next int) (col int, val string, err error) {
	col = next
	if ref := attr(se, "r"); ref != "" {
		if col, err = columnOf(ref); err != nil {
			return 0, "", err
		}
	}
	kind := attr(se, "t")
	depth := 1
	for depth > 0 {
		tok, err := d.Token()
		if err != nil {
			return 0, "", fmt.Errorf("%w: cell: %w", ErrFormat, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "v":
				var raw string
				if err := d.DecodeElement(&raw, &t); err != nil {
					return 0, "", fmt.Errorf("%w: %w", ErrFormat, err)
				}
				if val, err = value(kind, raw, shared); err != nil {
					return 0, "", err
				}
			case "is":
				if val, err = readText(d); err != nil {
					return 0, "", err
				}
			default:
				if err := d.Skip(); err != nil { // <f> formula and anything else: not evaluated
					return 0, "", fmt.Errorf("%w: %w", ErrFormat, err)
				}
			}
		case xml.EndElement:
			depth--
		}
	}
	return col, val, nil
}

func value(kind, raw string, shared []string) (string, error) {
	switch kind {
	case "s":
		i, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || i < 0 || i >= len(shared) {
			return "", fmt.Errorf("%w: bad shared string index %q", ErrFormat, raw)
		}
		return shared[i], nil
	case "b":
		if strings.TrimSpace(raw) == "1" {
			return "TRUE", nil
		}
		return "FALSE", nil
	default: // n, str, inlineStr, e, d: the text as stored
		return raw, nil
	}
}

// columnOf turns a cell reference like "AB12" into a zero-based column.
func columnOf(ref string) (int, error) {
	col, i := 0, 0
	for ; i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z'; i++ {
		col = col*26 + int(ref[i]-'A') + 1
		if col > 16384 {
			return 0, fmt.Errorf("%w: column out of range in %q", ErrFormat, ref)
		}
	}
	if i == 0 {
		return 0, fmt.Errorf("%w: bad cell reference %q", ErrFormat, ref)
	}
	return col - 1, nil
}

// Sheet is a sheet to write. Every cell is text. With Header the first row is bold.
type Sheet struct {
	Name   string
	Header bool
	Widths []float64 // column widths in characters, from column A; optional
	Rows   [][]string
}

// Write returns a workbook with the given sheets (at least one). Names must be 1 to 31 characters without
// any of []:*?/\ and unique.
func Write(sheets ...Sheet) ([]byte, error) {
	if len(sheets) == 0 {
		return nil, errors.New("xlsx: no sheets")
	}
	seen := map[string]bool{}
	for _, s := range sheets {
		n := len([]rune(s.Name))
		if n < 1 || n > 31 || strings.ContainsAny(s.Name, `[]:*?/\`) || seen[strings.ToLower(s.Name)] {
			return nil, fmt.Errorf("xlsx: invalid or duplicate sheet name %q", s.Name)
		}
		seen[strings.ToLower(s.Name)] = true
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = io.WriteString(w, body)
		return err
	}

	var ct, wb, rels strings.Builder
	ct.WriteString(xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
		`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	wb.WriteString(xmlHeader + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="` + nsRelations + `"><sheets>`)
	rels.WriteString(xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)

	for i, s := range sheets {
		n := strconv.Itoa(i + 1)
		ct.WriteString(`<Override PartName="/xl/worksheets/sheet` + n + `.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`)
		wb.WriteString(`<sheet name="` + escape(s.Name) + `" sheetId="` + n + `" r:id="rId` + n + `"/>`)
		rels.WriteString(`<Relationship Id="rId` + n + `" Type="` + nsRelations + `/worksheet" Target="worksheets/sheet` + n + `.xml"/>`)
		if err := add("xl/worksheets/sheet"+n+".xml", sheetXML(s)); err != nil {
			return nil, err
		}
	}
	styleID := strconv.Itoa(len(sheets) + 1)
	rels.WriteString(`<Relationship Id="rId` + styleID + `" Type="` + nsRelations + `/styles" Target="styles.xml"/></Relationships>`)
	wb.WriteString(`</sheets></workbook>`)
	ct.WriteString(`</Types>`)

	for _, p := range []struct{ name, body string }{
		{"[Content_Types].xml", ct.String()},
		{"_rels/.rels", xmlHeader + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="` + nsRelations + `/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{workbookPath, wb.String()},
		{relsPath, rels.String()},
		{"xl/styles.xml", stylesXML},
	} {
		if err := add(p.name, p.body); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

// stylesXML has two cell formats: 0 is plain and 1 is bold.
const stylesXML = xmlHeader + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
	`<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts>` +
	`<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>` +
	`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
	`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
	`<cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs>` +
	`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
	`</styleSheet>`

func sheetXML(s Sheet) string {
	var sb strings.Builder
	sb.WriteString(xmlHeader + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	if len(s.Widths) > 0 {
		sb.WriteString("<cols>")
		for i, w := range s.Widths {
			n := strconv.Itoa(i + 1)
			sb.WriteString(`<col min="` + n + `" max="` + n + `" width="` + strconv.FormatFloat(w, 'f', 1, 64) + `" customWidth="1"/>`)
		}
		sb.WriteString("</cols>")
	}
	sb.WriteString("<sheetData>")
	for r, row := range s.Rows {
		rn := strconv.Itoa(r + 1)
		sb.WriteString(`<row r="` + rn + `">`)
		for c, v := range row {
			if v == "" {
				continue
			}
			ref := columnName(c) + rn
			style := ""
			if s.Header && r == 0 {
				style = ` s="1"`
			}
			sb.WriteString(`<c r="` + ref + `" t="inlineStr"` + style + `><is><t xml:space="preserve">` + escape(v) + `</t></is></c>`)
		}
		sb.WriteString("</row>")
	}
	sb.WriteString("</sheetData></worksheet>")
	return sb.String()
}

func columnName(i int) string {
	name := ""
	for i++; i > 0; i = (i - 1) / 26 {
		name = string(rune('A'+(i-1)%26)) + name
	}
	return name
}

func escape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
