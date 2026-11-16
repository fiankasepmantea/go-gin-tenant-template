package xlsx

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// rowsOf reads the first sheet into a map of line -> cells.
func rowsOf(t *testing.T, data []byte, lim Limits) (map[int][]string, error) {
	t.Helper()
	out := map[int][]string{}
	err := ReadFirstSheet(data, lim, func(line int, cells []string) error {
		out[line] = cells
		return nil
	})
	return out, err
}

// zipOf builds an archive from name -> content, the way a hostile file would be put together.
func zipOf(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range parts {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const (
	wbXML  = `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="A" sheetId="1" r:id="rId7"/><sheet name="B" sheetId="2" r:id="rId8"/></sheets></workbook>`
	relXML = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId7" Type="x/worksheet" Target="worksheets/first.xml"/><Relationship Id="rId8" Type="x/worksheet" Target="/xl/worksheets/second.xml"/></Relationships>`
)

func book(sheet, shared string) map[string]string {
	p := map[string]string{
		"xl/workbook.xml":            wbXML,
		"xl/_rels/workbook.xml.rels": relXML,
		"xl/worksheets/first.xml":    `<worksheet><sheetData>` + sheet + `</sheetData></worksheet>`,
		"xl/worksheets/second.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>other sheet</t></is></c></row></sheetData></worksheet>`,
	}
	if shared != "" {
		p["xl/sharedStrings.xml"] = `<sst>` + shared + `</sst>`
	}
	return p
}

func TestWriteThenRead(t *testing.T) {
	data, err := Write(
		Sheet{Name: "Users", Header: true, Widths: []float64{10, 20}, Rows: [][]string{{"a", "b"}, {"x < y & \"z\"", ""}, {"", "last", "tail"}, {" spaced  "}}},
		Sheet{Name: "Help", Rows: [][]string{{"not read"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := rowsOf(t, data, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int][]string{1: {"a", "b"}, 2: {"x < y & \"z\""}, 3: {"", "last", "tail"}, 4: {" spaced  "}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestWriteValidatesSheets(t *testing.T) {
	for _, bad := range [][]Sheet{
		nil, {{Name: ""}}, {{Name: "a/b"}}, {{Name: strings.Repeat("x", 32)}}, {{Name: "A"}, {Name: "a"}},
	} {
		if _, err := Write(bad...); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestRead_SharedStringsRichTextAndTypes(t *testing.T) {
	shared := `<si><t>plain</t></si>` +
		`<si><r><t>rich </t></r><r><rPr><b/></rPr><t>text</t></r><rPh sb="0" eb="1"><t>HINT</t></rPh></si>` +
		`<si><t xml:space="preserve">  kept  </t></si>`
	sheet := `<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="D1" t="s"><v>2</v></c></row>` +
		`<row r="3"><c r="A3"><v>1001</v></c><c r="B3" t="b"><v>1</v></c><c r="C3" t="str"><f>A3+1</f><v>1002</v></c><c><v>nosref</v></c><c r="E3" t="e"><v>#N/A</v></c></row>` +
		`<row r="4"/>`
	got, err := rowsOf(t, zipOf(t, book(sheet, shared)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got[1]) != fmt.Sprint([]string{"plain", "rich text", "", "  kept  "}) {
		t.Errorf("row 1: %q", got[1])
	}
	// a cell without a reference follows the previous one; formulas are never evaluated
	if fmt.Sprint(got[3]) != fmt.Sprint([]string{"1001", "TRUE", "1002", "nosref", "#N/A"}) {
		t.Errorf("row 3: %q", got[3])
	}
	if _, ok := got[2]; ok {
		t.Error("a row that is not in the file must not be reported")
	}
	if c, ok := got[4]; !ok || len(c) != 0 {
		t.Errorf("empty row: %q %v", c, ok)
	}
}

func TestRead_UsesTheFirstSheetInWorkbookOrder(t *testing.T) {
	got, err := rowsOf(t, zipOf(t, book(`<row r="1"><c r="A1" t="inlineStr"><is><t>first</t></is></c></row>`, "")), Limits{})
	if err != nil || got[1][0] != "first" {
		t.Fatalf("%v %v", got, err)
	}
}

func TestRead_Limits(t *testing.T) {
	// too many rows, by number and by a row number far away
	var many strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&many, `<row r="%d"><c r="A%d"><v>1</v></c></row>`, i, i)
	}
	if _, err := rowsOf(t, zipOf(t, book(many.String(), "")), Limits{MaxLines: 20}); !errors.Is(err, ErrTooManyLines) {
		t.Errorf("many rows: %v", err)
	}
	if _, err := rowsOf(t, zipOf(t, book(`<row r="1048576"><c r="A1"><v>1</v></c></row>`, "")), Limits{MaxLines: 100}); !errors.Is(err, ErrTooManyLines) {
		t.Errorf("far row: %v", err)
	}
	// columns beyond the limit are ignored, not allocated
	got, err := rowsOf(t, zipOf(t, book(`<row r="1"><c r="A1"><v>a</v></c><c r="XFD1"><v>far</v></c></row>`, "")), Limits{MaxColumns: 8})
	if err != nil || len(got[1]) != 1 {
		t.Errorf("far column: %v %v", got, err)
	}
	// a part that unpacks to more than the limit
	huge := book(`<row r="1"><c r="A1" t="inlineStr"><is><t>`+strings.Repeat("a", 4000)+`</t></is></c></row>`, "")
	if _, err := rowsOf(t, zipOf(t, huge), Limits{MaxPart: 1000}); !errors.Is(err, ErrFormat) {
		t.Errorf("big part: %v", err)
	}
	if _, err := rowsOf(t, zipOf(t, huge), Limits{MaxUnzipped: 1000}); !errors.Is(err, ErrFormat) {
		t.Errorf("big archive: %v", err)
	}
	tooMany := `<si><t>a</t></si><si><t>b</t></si><si><t>c</t></si>`
	if _, err := rowsOf(t, zipOf(t, book(`<row r="1"/>`, tooMany)), Limits{MaxStrings: 2}); !errors.Is(err, ErrFormat) {
		t.Errorf("many strings: %v", err)
	}
}

func TestRead_RefusesMalformedAndHostileFiles(t *testing.T) {
	cell := func(attrs, inner string) map[string]string {
		return book(`<row r="1"><c `+attrs+`>`+inner+`</c></row>`, `<si><t>only</t></si>`)
	}
	cases := map[string]struct {
		data []byte
	}{
		"empty":                {nil},
		"text":                 {[]byte("username,email\nx,y")},
		"ole (encrypted)":      {append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 600)...)},
		"truncated zip":        {zipOf(t, book(`<row r="1"/>`, ""))[:40]},
		"no workbook":          {zipOf(t, map[string]string{"a.txt": "x"})},
		"bad shared index":     {zipOf(t, cell(`r="A1" t="s"`, `<v>9</v>`))},
		"negative index":       {zipOf(t, cell(`r="A1" t="s"`, `<v>-1</v>`))},
		"text as index":        {zipOf(t, cell(`r="A1" t="s"`, `<v>abc</v>`))},
		"column out of range":  {zipOf(t, cell(`r="ZZZZ1"`, `<v>1</v>`))},
		"reference of digits":  {zipOf(t, cell(`r="11"`, `<v>1</v>`))},
		"rows out of order":    {zipOf(t, book(`<row r="5"/><row r="2"/>`, ""))},
		"row number zero":      {zipOf(t, book(`<row r="0"/>`, ""))},
		"row number not a num": {zipOf(t, book(`<row r="x"/>`, ""))},
		"cell outside a row":   {zipOf(t, book(`<c r="A1"><v>1</v></c>`, ""))},
		"broken xml":           {zipOf(t, book(`<row r="1"><c r="A1"><v>1</c></row>`, ""))},
		"missing sheet part": {zipOf(t, map[string]string{
			"xl/workbook.xml": wbXML, "xl/_rels/workbook.xml.rels": relXML})},
		"unknown relationship": {zipOf(t, map[string]string{
			"xl/workbook.xml": wbXML, "xl/_rels/workbook.xml.rels": `<Relationships/>`})},
	}
	for name, c := range cases {
		if _, err := rowsOf(t, c.data, Limits{}); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRead_EntitiesAreNotExpanded(t *testing.T) {
	// a "billion laughs" style document: the standard decoder refuses undefined entities instead of expanding
	bomb := `<!DOCTYPE x [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;">]>` +
		`<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>&b;</t></is></c></row></sheetData></worksheet>`
	p := book("", "")
	p["xl/worksheets/first.xml"] = bomb
	if _, err := rowsOf(t, zipOf(t, p), Limits{}); !errors.Is(err, ErrFormat) {
		t.Errorf("entity bomb: %v", err)
	}
}

func TestRead_FnErrorStopsAndIsReturned(t *testing.T) {
	stop := errors.New("stop")
	calls := 0
	data := zipOf(t, book(`<row r="1"/><row r="2"/><row r="3"/>`, ""))
	err := ReadFirstSheet(data, Limits{}, func(int, []string) error { calls++; return stop })
	if !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("%v after %d calls", err, calls)
	}
}

func FuzzReadFirstSheet(f *testing.F) {
	good, _ := Write(Sheet{Name: "S", Rows: [][]string{{"a", "b"}, {"c"}}})
	f.Add(good)
	f.Add([]byte("PK\x03\x04"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = ReadFirstSheet(data, Limits{MaxLines: 100, MaxPart: 1 << 16, MaxUnzipped: 1 << 20}, func(int, []string) error { return nil })
	})
}
