/*
 * This file is subject to the terms and conditions defined in
 * file 'LICENSE.md', which is part of this source code package.
 */

package extractor

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yudaprama/pdf/model"
)

// mkPara builds a minimal textPara with the given font size and text.
// An empty text yields a line-less paragraph (matches how empty paras are
// skipped downstream).
func mkPara(t *testing.T, fontsize float64, text string) *textPara {
	t.Helper()
	if text == "" {
		return &textPara{}
	}
	return &textPara{
		lines: []*textLine{{
			fontsize: fontsize,
			words:    []*textWord{{text: text, newWord: true}},
		}},
	}
}

func TestMarkdownHeadingsByFontSize(t *testing.T) {
	paras := paraList{
		mkPara(t, 24, "Big Title"),
		mkPara(t, 10, strings.Repeat("body", 200)), // long body dominates the median
		mkPara(t, 10, "more body text here"),
		mkPara(t, 15, "Section"),
		mkPara(t, 12, "Subsection"),
	}
	md := parasMarkdown(paras)

	if !strings.HasPrefix(md, "# Big Title") {
		t.Errorf("expected H1 heading, got %q", firstLine(md))
	}
	if !strings.Contains(md, "\n## Section") {
		t.Errorf("expected H2 heading, got:\n%s", md)
	}
	if !strings.Contains(md, "\n### Subsection") {
		t.Errorf("expected H3 heading, got:\n%s", md)
	}
	if !strings.Contains(md, "more body text here") {
		t.Errorf("body paragraph missing:\n%s", md)
	}
}

func TestMarkdownNoHeadingForLongLargeText(t *testing.T) {
	long := strings.Repeat("large display text ", 10) // > headingMaxChars
	paras := paraList{
		mkPara(t, 20, long),
		mkPara(t, 10, "regular body"),
	}
	md := parasMarkdown(paras)
	if strings.Contains(md, "#") {
		t.Errorf("long oversized paragraph must not become a heading:\n%s", md)
	}
}

func TestMarkdownSkipsEmptyParas(t *testing.T) {
	paras := paraList{
		mkPara(t, 10, "only para"),
		mkPara(t, 10, ""),
		nil,
	}
	md := parasMarkdown(paras)
	if strings.TrimSpace(md) != "only para" {
		t.Errorf("expected only the non-empty para, got %q", md)
	}
}

func TestMarkdownTable(t *testing.T) {
	cell := func(text string) *textPara {
		return mkPara(t, 10, text)
	}
	tbl := &textTable{
		w: 2, h: 2,
		cells: map[uint64]*textPara{
			cellIndex(0, 0): cell("Name"),
			cellIndex(1, 0): cell("Qty"),
			cellIndex(0, 1): cell("Widget"),
			cellIndex(1, 1): cell("4"),
		},
	}
	var b bytes.Buffer
	writeMarkdownTable(tbl, &b)
	got := b.String()

	want := "| Name | Qty |\n| --- | --- |\n| Widget | 4 |\n"
	if got != want {
		t.Errorf("table mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestMarkdownTableEscapesPipes(t *testing.T) {
	tbl := &textTable{
		w: 1, h: 1,
		cells: map[uint64]*textPara{
			cellIndex(0, 0): mkPara(t, 10, "a|b"),
		},
	}
	var b bytes.Buffer
	writeMarkdownTable(tbl, &b)
	if !strings.Contains(b.String(), `a\|b`) {
		t.Errorf("pipe not escaped: %q", b.String())
	}
}

func TestMedian(t *testing.T) {
	if got := median(nil); got != 0 {
		t.Errorf("median(nil) = %v, want 0", got)
	}
	if got := median([]float64{5}); got != 5 {
		t.Errorf("median([5]) = %v, want 5", got)
	}
	if got := median([]float64{1, 9, 5}); got != 5 {
		t.Errorf("median odd = %v, want 5", got)
	}
	if got := median([]float64{1, 2, 8, 9}); got != 5 {
		t.Errorf("median even = %v, want 5", got)
	}
}

func TestMarkdownPageSizeUnused(t *testing.T) {
	// A zero PageText must not panic and Markdown() must be empty.
	pt := PageText{pageSize: model.PdfRectangle{}}
	if pt.Markdown() != "" {
		t.Errorf("expected empty markdown for zero PageText")
	}
	if !pt.markdownDone {
		t.Errorf("expected lazy markdown to be computed on first call")
	}
}

func TestMarkdownLazyReleasesParas(t *testing.T) {
	pt := PageText{paras: paraList{mkPara(t, 10, "hello")}}
	if got := pt.Markdown(); got != "hello\n" {
		t.Errorf("Markdown() = %q, want %q", got, "hello\n")
	}
	if pt.paras != nil {
		t.Errorf("expected paras to be released after rendering")
	}
	if got := pt.Markdown(); got != "hello\n" {
		t.Errorf("second Markdown() call must return the cached value, got %q", got)
	}
}

func TestMarkdownNoHeadingsWhenBodySizeUnknown(t *testing.T) {
	// Degenerate font sizes must not turn every paragraph into a heading.
	paras := paraList{
		mkPara(t, 0, "title"),
		mkPara(t, 0, "body"),
	}
	md := parasMarkdown(paras)
	if md != "title\n\nbody\n" {
		t.Errorf("expected plain body blocks, got %q", md)
	}
}

func TestMarkdownTableEmptyCellKeepsMedian(t *testing.T) {
	// A table whose (0,0) cell is empty has paraFontSize 0; it must not drag
	// the body median down and inflate heading detection.
	cell := mkPara(t, 10, strings.Repeat("body", 100))
	tbl := &textTable{
		w: 2, h: 2,
		cells: map[uint64]*textPara{
			cellIndex(0, 0): {}, // empty cell: no lines
			cellIndex(1, 0): mkPara(t, 10, "Qty"),
			cellIndex(0, 1): mkPara(t, 10, "Widget"),
			cellIndex(1, 1): mkPara(t, 10, "4"),
		},
	}
	tablePara := &textPara{table: tbl}
	paras := paraList{mkPara(t, 20, "Title"), tablePara, cell}
	md := parasMarkdown(paras)
	if !strings.HasPrefix(md, "# Title") {
		t.Errorf("heading must still be detected with an empty table cell:\n%s", md)
	}
	if !strings.Contains(md, "|  | Qty |") {
		t.Errorf("table must render with empty first cell:\n%s", md)
	}
}

func TestMarkdownHeadingCountsRunesNotBytes(t *testing.T) {
	// 60 CJK runes are 180 bytes; the heading gate counts runes.
	heading := strings.Repeat("题", 60)
	paras := paraList{
		mkPara(t, 20, heading),
		mkPara(t, 10, strings.Repeat("正文", 100)), // long body dominates the median
	}
	md := parasMarkdown(paras)
	if !strings.HasPrefix(md, "# ") {
		t.Errorf("60-rune CJK heading should still be a heading:\n%s", md)
	}
}

func TestSplitParasBySize(t *testing.T) {
	line := func(fontsize float64, text string) *textLine {
		return &textLine{
			fontsize: fontsize,
			words:    []*textWord{{text: text, newWord: true}},
		}
	}

	// A large heading merged into the same para as small body lines must be
	// split into separate runs.
	mixed := makeTextPara(model.PdfRectangle{}, []*textLine{
		line(24, "Big Title"),
		line(10, "body follows"),
		line(10, "more body"),
	})
	got := splitParasBySize(paraList{mixed})
	if len(got) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(got))
	}
	if fs := got[0].lines[0].fontsize; fs != 24 {
		t.Errorf("run 0 fontsize = %v, want 24", fs)
	}
	if n := len(got[1].lines); n != 2 {
		t.Errorf("run 1 should hold the 2 body lines, got %d", n)
	}

	// Sizes within the ratio threshold stay in one run.
	close := makeTextPara(model.PdfRectangle{}, []*textLine{
		line(10, "a"),
		line(10.5, "b"),
		line(11, "c"),
	})
	if got := splitParasBySize(paraList{close}); len(got) != 1 {
		t.Errorf("sizes within threshold must not split, got %d runs", len(got))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// geoPara builds a one-line paragraph with explicit geometry (depth + x span)
// for the borderless-table fallback tests.
func geoPara(depth, llx, urx float64, text string) *textPara {
	line := &textLine{
		PdfRectangle: model.PdfRectangle{Llx: llx, Lly: 0, Urx: urx, Ury: 10},
		depth:        depth,
		fontsize:     10,
		words:        []*textWord{{text: text, newWord: true}},
	}
	return makeTextPara(model.PdfRectangle{Llx: llx, Lly: 0, Urx: urx, Ury: 10}, []*textLine{line})
}

func TestMarkdownLineTableFallback(t *testing.T) {
	// Borderless table whose columns the page divider merged into paragraphs.
	paras := paraList{
		geoPara(92, 50, 80, "Name"),
		geoPara(92, 297, 315, "Qty"),
		geoPara(142, 50, 80, "Widget"),
		geoPara(142, 297, 303, "4"),
		geoPara(192, 50, 84, "Gadget"),
		geoPara(192, 297, 303, "7"),
	}
	md := parasMarkdown(paras)
	want := "| Name | Qty |\n| --- | --- |\n| Widget | 4 |\n| Gadget | 7 |\n"
	if !strings.Contains(md, want) {
		t.Errorf("expected GFM table:\n%s\ngot:\n%s", want, md)
	}
}

func TestMarkdownLineTableFallbackMissingCell(t *testing.T) {
	// A missing middle cell must leave an empty column, not shift cells.
	paras := paraList{
		geoPara(92, 50, 80, "Name"),
		geoPara(92, 297, 315, "Qty"),
		geoPara(142, 50, 80, "Widget"),
		geoPara(142, 450, 470, "9"), // third column appears only here
	}
	md := parasMarkdown(paras)
	want := "| Name | Qty |  |\n| --- | --- | --- |\n| Widget |  | 9 |\n"
	if !strings.Contains(md, want) {
		t.Errorf("expected padded GFM table:\n%s\ngot:\n%s", want, md)
	}
}

func TestMarkdownLineTableFallbackRejectsWideText(t *testing.T) {
	// Flowing text in two columns must not become a table: the lines are too
	// wide relative to the region.
	paras := paraList{
		geoPara(92, 50, 170, strings.Repeat("left", 30)),
		geoPara(92, 200, 320, strings.Repeat("rght", 30)),
		geoPara(142, 50, 170, strings.Repeat("left", 30)),
		geoPara(142, 200, 320, strings.Repeat("rght", 30)),
	}
	md := parasMarkdown(paras)
	if strings.Contains(md, "|") {
		t.Errorf("wide two-column text must not render as a table:\n%s", md)
	}
}

func TestMarkdownLineTableFallbackRejectsSingleColumn(t *testing.T) {
	// Stacked single-column paragraphs are body text, not a table.
	paras := paraList{
		geoPara(92, 50, 200, "first paragraph"),
		geoPara(142, 50, 200, "second paragraph"),
		geoPara(192, 50, 200, "third paragraph"),
	}
	md := parasMarkdown(paras)
	if strings.Contains(md, "|") {
		t.Errorf("single-column stack must not render as a table:\n%s", md)
	}
}

func TestMarkdownLineTableFallbackPartialMerge(t *testing.T) {
	// Real-world shape: the page divider merged the body line and the first
	// table row into one paragraph; the table columns each merged vertically.
	// The fallback must split the merged paragraph around the table.
	line := func(depth, llx, urx float64, text string) *textLine {
		return &textLine{
			PdfRectangle: model.PdfRectangle{Llx: llx, Lly: 0, Urx: urx, Ury: 10},
			depth:        depth,
			fontsize:     10,
			words:        []*textWord{{text: text, newWord: true}},
		}
	}
	merged := makeTextPara(model.PdfRectangle{}, []*textLine{
		line(84, 50, 421, "Body text paragraph with plenty of ordinary words."),
		line(94, 55, 81, "Name"),
		line(94, 311, 326, "Qty"),
	})
	left := makeTextPara(model.PdfRectangle{}, []*textLine{
		line(109, 55, 86, "Widget"),
		line(124, 55, 87, "Gadget"),
	})
	right := makeTextPara(model.PdfRectangle{}, []*textLine{
		line(109, 311, 316, "4"),
		line(124, 311, 316, "7"),
	})

	md := parasMarkdown(paraList{merged, left, right})

	if !strings.Contains(md, "Body text paragraph with plenty of ordinary words.") {
		t.Errorf("body line lost:\n%s", md)
	}
	want := "| Name | Qty |\n| --- | --- |\n| Widget | 4 |\n| Gadget | 7 |\n"
	if !strings.Contains(md, want) {
		t.Errorf("expected GFM table:\n%s\ngot:\n%s", want, md)
	}
	if strings.Index(md, "Body text") > strings.Index(md, "| Name") {
		t.Errorf("body must come before the table:\n%s", md)
	}
}

func TestMarkdownListSeparateParas(t *testing.T) {
	paras := paraList{
		mkPara(t, 10, "Intro paragraph before the list."),
		mkPara(t, 10, "• first item"),
		mkPara(t, 10, "• second item"),
		mkPara(t, 10, "• third item"),
		mkPara(t, 10, "Trailing paragraph."),
	}
	md := parasMarkdown(paras)
	if !strings.Contains(md, "Intro paragraph before the list.") {
		t.Errorf("intro lost:\n%s", md)
	}
	if !strings.Contains(md, "- first item\n- second item\n- third item") {
		t.Errorf("expected bullet list:\n%s", md)
	}
	if !strings.Contains(md, "Trailing paragraph.") {
		t.Errorf("trailing paragraph lost:\n%s", md)
	}
}

func TestMarkdownListMergedInOnePara(t *testing.T) {
	// List items merged into a single paragraph by the page divider.
	paras := paraList{
		mkPara(t, 10, "1. alpha\n2. beta\n3. gamma"),
	}
	md := parasMarkdown(paras)
	if !strings.Contains(md, "1. alpha\n2. beta\n3. gamma") {
		t.Errorf("expected ordered list:\n%s", md)
	}
}

func TestMarkdownListWithContinuation(t *testing.T) {
	paras := paraList{
		mkPara(t, 10, "- item one\nwrapped onto next line\n- item two"),
	}
	md := parasMarkdown(paras)
	if !strings.Contains(md, "- item one wrapped onto next line\n- item two") {
		t.Errorf("expected continuation joined:\n%s", md)
	}
}

func TestMarkdownSingleBulletNotList(t *testing.T) {
	// One stray bullet-looking paragraph must not become a one-item list.
	paras := paraList{
		mkPara(t, 10, "- a lone dash line"),
		mkPara(t, 10, "normal paragraph"),
	}
	md := parasMarkdown(paras)
	if strings.Contains(md, "\n- ") {
		t.Errorf("single item must not render as a list:\n%s", md)
	}
}

func TestMarkdownEscapesBodyStructure(t *testing.T) {
	paras := paraList{
		mkPara(t, 10, "# not a heading"),
		mkPara(t, 10, "> not a quote"),
		mkPara(t, 10, "---"),
		mkPara(t, 10, "2. not a list"),
	}
	md := parasMarkdown(paras)
	for _, want := range []string{`\# not a heading`, `\> not a quote`, `\---`, `\2. not a list`} {
		if !strings.Contains(md, want) {
			t.Errorf("expected escaped %q in:\n%s", want, md)
		}
	}
	if strings.Contains(md, "\n# ") || strings.Contains(md, "\n> ") {
		t.Errorf("unescaped structure leaked:\n%s", md)
	}
}

func TestMarkdownBoldHeading(t *testing.T) {
	bold := model.NewStandard14FontMustCompile(model.CourierBoldName)
	plain := model.NewStandard14FontMustCompile(model.CourierName)

	mk := func(font *model.PdfFont, text string) *textPara {
		mark := &textMark{font: font, text: text}
		word := &textWord{text: text, fontsize: 10, marks: []*textMark{mark}}
		line := &textLine{fontsize: 10, words: []*textWord{word}}
		return makeTextPara(model.PdfRectangle{}, []*textLine{line})
	}

	paras := paraList{
		mk(bold, "Bold Section Header"),
		mk(plain, strings.Repeat("body", 100)),
	}
	md := parasMarkdown(paras)
	if !strings.HasPrefix(md, "### Bold Section Header") {
		t.Errorf("expected bold short paragraph as heading:\n%s", md)
	}

	// Mixed fonts (emphasis inside body) must not be a heading.
	paras = paraList{
		mk(plain, strings.Repeat("body", 100)),
		mk(bold, "1. bold"),
		mk(plain, "2. plain"),
	}
	md = parasMarkdown(paras)
	if strings.Contains(md, "### 1. bold") {
		t.Errorf("bold list item must not be a heading:\n%s", md)
	}
}

func TestMarkdownNestedList(t *testing.T) {
	paras := paraList{
		geoPara(100, 50, 120, "• main item one"),
		geoPara(120, 70, 140, "• sub item A"),
		geoPara(140, 70, 140, "• sub item B"),
		geoPara(160, 50, 120, "• main item two"),
	}
	md := parasMarkdown(paras)
	want := "- main item one\n  - sub item A\n  - sub item B\n- main item two"
	if !strings.Contains(md, want) {
		t.Errorf("expected nested list:\n%s\ngot:\n%s", want, md)
	}
}

func TestMarkdownNestedListContinuation(t *testing.T) {
	// A wrapped item line inside the same paragraph stays within its item.
	line := func(depth, llx float64, text string) *textLine {
		return &textLine{
			PdfRectangle: model.PdfRectangle{Llx: llx, Lly: 0, Urx: llx + 40, Ury: 10},
			depth:        depth,
			fontsize:     10,
			words:        []*textWord{{text: text, newWord: true}},
		}
	}
	item := makeTextPara(model.PdfRectangle{}, []*textLine{
		line(100, 50, "- item one wraps"),
		line(110, 65, "onto a second line"),
		line(130, 50, "- item two"),
	})
	md := parasMarkdown(paraList{item})
	want := "- item one wraps onto a second line\n- item two"
	if !strings.Contains(md, want) {
		t.Errorf("expected wrapped item joined:\n%s\ngot:\n%s", want, md)
	}
}

func TestMarkdownEscapeTrailingBackslash(t *testing.T) {
	paras := paraList{
		mkPara(t, 10, "trailing slash\\"),
	}
	md := parasMarkdown(paras)
	if !strings.Contains(md, "trailing slash\\\\") {
		t.Errorf("expected escaped trailing backslash, got:\n%s", md)
	}
}

func TestMarkdownBorderlessTableEndToEnd(t *testing.T) {
	contents := `
        BT /UniDocCourier 10 Tf 50 700 Td (Name)Tj ET
        BT /UniDocCourier 10 Tf 297 700 Td (Qty)Tj ET
        BT /UniDocCourier 10 Tf 50 650 Td (Widget)Tj ET
        BT /UniDocCourier 10 Tf 297 650 Td (4)Tj ET
        BT /UniDocCourier 10 Tf 50 600 Td (Gadget)Tj ET
        BT /UniDocCourier 10 Tf 297 600 Td (7)Tj ET
        `
	resources := model.NewPdfPageResources()
	courier := model.NewStandard14FontMustCompile(model.CourierName)
	resources.SetFontByName("UniDocCourier", courier.ToPdfObject())

	e := Extractor{
		resources:             resources,
		contents:              contents,
		mediaBox:              r(0, 0, 612, 792),
		PerformParagraphMerge: true,
	}
	pt, _, _, err := e.ExtractPageText()
	if err != nil {
		t.Fatalf("Error extracting text: err=%v", err)
	}
	md := pt.Markdown()
	want := "| Name | Qty |\n| --- | --- |\n| Widget | 4 |\n| Gadget | 7 |\n"
	if !strings.Contains(md, want) {
		t.Errorf("expected GFM table in markdown:\n%s\ngot:\n%s", want, md)
	}
}
