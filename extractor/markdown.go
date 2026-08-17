/*
 * This file is subject to the terms and conditions defined in
 * file 'LICENSE.md', which is part of this source code package.
 */

package extractor

import (
	"bytes"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Markdown returns the page text rendered as Markdown: font-size-derived
// headings (#/##/###) and detected tables as GitHub-flavored markdown tables.
// Blank-line separated paragraphs mirror the reading order of Text().
// The rendering is computed on first call and cached.
func (pt *PageText) Markdown() string {
	if !pt.markdownDone {
		pt.viewMarkdown = parasMarkdown(pt.paras)
		pt.markdownDone = true
		pt.paras = nil // the paragraph tree is no longer needed
	}
	return pt.viewMarkdown
}

// Heading thresholds relative to the page's body font size (median weighted by
// paragraph length). A paragraph qualifies as a heading only if it is short
// (single thought) and rendered notably larger than the body text.
const (
	headingMinRatio  = 1.15
	heading2MinRatio = 1.35
	heading1MinRatio = 1.7
	headingMaxChars  = 100
)

// lineTableCellWidthR is the maximum width of a line, relative to the width
// of the whole candidate region, for it to count as a table cell. Flowing
// text in multi-column layouts fills its column and must not be mistaken for
// a row of short cells.
const lineTableCellWidthR = 0.35

// mdBlock is one output block of the Markdown rendering: a paragraph, a
// detected table, a table synthesized by the borderless-table fallback, or a
// detected list.
type mdBlock struct {
	size    float64
	runes   int
	isTable bool
	isList  bool
	table   *textTable
	text    string
	para    *textPara
	bold    bool
}

// parasMarkdown renders a paraList to Markdown. Body size is the length-
// weighted median of paragraph font sizes so that a page of mostly large
// display text does not mark everything as a heading.
func parasMarkdown(paras paraList) string {
	if len(paras) == 0 {
		return ""
	}

	paras = splitParasBySize(paras)

	blocks := make([]mdBlock, 0, len(paras))
	weighted := make([]float64, 0, len(paras))

	for _, p := range paras {
		if p == nil {
			continue
		}
		b := mdBlock{size: paraFontSize(p), isTable: p.table != nil, table: p.table, para: p, bold: paraIsBold(p)}
		if b.isTable {
			var w bytes.Buffer
			writeMarkdownTable(b.table, &w)
			b.text = w.String()
		} else {
			b.text = strings.TrimSpace(p.text())
		}
		if b.text == "" {
			continue
		}
		b.runes = utf8.RuneCountInString(b.text)
		if b.size > 0 { // zero sizes (e.g. empty table corner) must not drag the median
			for i := 0; i < b.runes; i += 50 { // weight by length, capped per para
				weighted = append(weighted, b.size)
			}
		}
		blocks = append(blocks, b)
	}
	if len(blocks) == 0 {
		return ""
	}

	// Recover borderless tables that the geometric table detector missed (its
	// cells were merged into paragraphs by the page divider).
	blocks = detectLineTables(blocks)

	// Recover lists whose items were merged into paragraphs or split into
	// one-paragraph-per-item blocks.
	blocks = detectLists(blocks)

	body := median(weighted)
	// A non-positive body size means font sizes were degenerate; don't guess
	// headings from them.
	noHeadings := body <= 0

	var out bytes.Buffer
	for _, b := range blocks {
		switch {
		case b.isTable || b.isList:
			out.WriteString("\n")
			out.WriteString(b.text)
			out.WriteString("\n")
		case !noHeadings && b.runes <= headingMaxChars && b.size >= heading1MinRatio*body:
			out.WriteString("\n# ")
			out.WriteString(collapseLine(b.text))
			out.WriteString("\n")
		case !noHeadings && b.runes <= headingMaxChars && b.size >= heading2MinRatio*body:
			out.WriteString("\n## ")
			out.WriteString(collapseLine(b.text))
			out.WriteString("\n")
		case !noHeadings && b.runes <= headingMaxChars && b.size >= headingMinRatio*body:
			out.WriteString("\n### ")
			out.WriteString(collapseLine(b.text))
			out.WriteString("\n")
		case !noHeadings && b.bold && b.runes <= headingMaxChars:
			// A short all-bold paragraph at body size reads as a heading.
			out.WriteString("\n### ")
			out.WriteString(collapseLine(b.text))
			out.WriteString("\n")
		default:
			out.WriteString("\n")
			out.WriteString(escapeMarkdownBody(b.text))
			out.WriteString("\n")
		}
	}
	return strings.TrimSpace(out.String()) + "\n"
}

// splitParasBySize splits each paragraph into runs of lines whose font size is
// uniform. The paragraph merger groups by geometry, not typography, so a large
// heading directly above same-column body text can land in one textPara; its
// first-line fontsize then misclassifies the whole para (or a long merged para
// hides the heading behind the length gate). Splitting on significant size
// transitions restores the typographic boundary. Table paras pass through
// untouched.
func splitParasBySize(paras paraList) paraList {
	const sizeRatio = 1.12 // two adjacent lines this different start a new run

	out := make(paraList, 0, len(paras))
	for _, p := range paras {
		if p == nil || p.table != nil || len(p.lines) < 2 {
			out = append(out, p)
			continue
		}

		type run struct {
			fontsize float64
			lines    []*textLine
		}
		var runs []run
		for _, line := range p.lines {
			size := line.fontsize
			if n := len(runs); n > 0 {
				prev := runs[n-1].fontsize
				larger := max(prev, size)
				smaller := min(prev, size)
				if smaller <= 0 || larger/smaller > sizeRatio {
					runs = append(runs, run{fontsize: size, lines: []*textLine{line}})
					continue
				}
				runs[n-1].lines = append(runs[n-1].lines, line)
				continue
			}
			runs = append(runs, run{fontsize: size, lines: []*textLine{line}})
		}

		for _, r := range runs {
			out = append(out, makeTextPara(p.PdfRectangle, r.lines))
		}
	}
	return out
}

// paraFontSize returns a representative font size for a paragraph, tolerating
// table paragraphs whose lines slice may be empty.
func paraFontSize(p *textPara) float64 {
	if len(p.lines) > 0 {
		return p.fontsize()
	}
	if p.table != nil {
		if cell := p.table.get(0, 0); cell != nil && len(cell.lines) > 0 {
			return cell.fontsize()
		}
	}
	return 0
}

// median returns the middle value of s (average of the two middles for even
// lengths). Returns 0 for empty input.
func median(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	c := append([]float64(nil), s...)
	sort.Float64s(c)
	mid := len(c) / 2
	if len(c)%2 == 1 {
		return c[mid]
	}
	return (c[mid-1] + c[mid]) / 2
}

// collapseLine joins a heading's internal line breaks so it renders as one
// Markdown heading line.
func collapseLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// writeMarkdownTable renders a textTable as a GitHub-flavored Markdown table.
// The first row is treated as the header row; empty tables render as nothing.
func writeMarkdownTable(t *textTable, w *bytes.Buffer) {
	if t == nil || t.w == 0 || t.h == 0 {
		return
	}
	rows := make([][]string, t.h)
	for y := 0; y < t.h; y++ {
		row := make([]string, t.w)
		for x := 0; x < t.w; x++ {
			if cell := t.get(x, y); cell != nil {
				row[x] = strings.TrimSpace(cell.text())
			}
		}
		rows[y] = row
	}
	writeGFMTable(rows, w)
}

// writeGFMTable renders `rows` as a GitHub-flavored Markdown table. The first
// row is the header row; rows are padded to a common column count.
func writeGFMTable(rows [][]string, w *bytes.Buffer) {
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	if cols == 0 {
		return
	}
	for y, r := range rows {
		w.WriteString("| ")
		for x := 0; x < cols; x++ {
			text := ""
			if x < len(r) {
				text = r[x]
			}
			w.WriteString(escapeTableCell(text))
			if x < cols-1 {
				w.WriteString(" | ")
			}
		}
		w.WriteString(" |\n")
		if y == 0 {
			w.WriteString("|")
			for x := 0; x < cols; x++ {
				w.WriteString(" --- |")
			}
			w.WriteString("\n")
		}
	}
}

// detectLineTables recovers borderless tables that the geometric table
// detector missed: their cells were merged into paragraphs by the page
// divider before table detection ran. It scans the lines of runs of
// consecutive non-table paragraph blocks, groups the lines into rows by
// depth, and replaces windows of >=2 rows — where every row splits into >=2
// horizontally disjoint, cell-like lines that share an x-column grid — with a
// single synthesized table block. Blocks whose lines only partially fall in
// a table window are split around the table.
func detectLineTables(blocks []mdBlock) []mdBlock {
	out := make([]mdBlock, 0, len(blocks))
	i := 0
	for i < len(blocks) {
		if blocks[i].isTable || blocks[i].para == nil {
			out = append(out, blocks[i])
			i++
			continue
		}
		j := i
		for j < len(blocks) && !blocks[j].isTable && blocks[j].para != nil {
			j++
		}
		out = append(out, runLineTables(blocks[i:j])...)
		i = j
	}
	return out
}

// lineRef ties a text line to the block it came from.
type lineRef struct {
	line  *textLine
	block int
}

// runLineTables applies the borderless-table recovery to one run of
// consecutive non-table paragraph blocks.
func runLineTables(run []mdBlock) []mdBlock {
	var refs []lineRef
	for bi, b := range run {
		for _, l := range b.para.lines {
			refs = append(refs, lineRef{l, bi})
		}
	}
	if len(refs) == 0 {
		return run
	}
	sorted := append([]lineRef(nil), refs...)
	sort.Slice(sorted, func(i, j int) bool {
		if !isZero(sorted[i].line.depth - sorted[j].line.depth) {
			return sorted[i].line.depth < sorted[j].line.depth
		}
		return sorted[i].line.Llx < sorted[j].line.Llx
	})

	// Group the lines into rows by depth.
	var rows [][]lineRef
	for _, r := range sorted {
		if n := len(rows); n > 0 && isZero(rows[n-1][0].line.depth-r.line.depth) {
			rows[n-1] = append(rows[n-1], r)
		} else {
			rows = append(rows, []lineRef{r})
		}
	}

	// A row is structurally table-like if it has >=2 x-disjoint lines.
	rowOK := make([]bool, len(rows))
	for i, row := range rows {
		rowOK[i] = len(row) >= 2
		for k := 1; rowOK[i] && k < len(row); k++ {
			if row[k-1].line.Urx > row[k].line.Llx {
				rowOK[i] = false
			}
		}
	}

	lineRow := map[*textLine]int{}
	for i, row := range rows {
		for _, r := range row {
			lineRow[r.line] = i
		}
	}

	// Find table windows: the longest valid window starting at each row.
	regionOf := make([]int, len(rows))
	for i := range regionOf {
		regionOf[i] = -1
	}
	var regions []mdBlock
	for s := 0; s < len(rows); {
		if !rowOK[s] {
			s++
			continue
		}
		e := s
		for e+1 < len(rows) && rowOK[e+1] {
			e++
		}
		matched := false
		for ee := e; ee > s && !matched; ee-- {
			if ok, tbl := windowTable(rows[s : ee+1]); ok {
				idx := len(regions)
				regions = append(regions, tbl)
				for ri := s; ri <= ee; ri++ {
					regionOf[ri] = idx
				}
				matched = true
				s = ee + 1
			}
		}
		if !matched {
			s++
		}
	}
	if len(regions) == 0 {
		return run
	}

	// Re-emit the run: body lines keep their order, each region's table is
	// emitted where its first line occurs.
	emitted := make([]bool, len(regions))
	out := make([]mdBlock, 0, len(run))
	for _, b := range run {
		var seg []*textLine
		flush := func() {
			if len(seg) > 0 {
				out = append(out, bodyBlock(seg))
				seg = nil
			}
		}
		for _, l := range b.para.lines {
			rid := -1
			if r, ok := lineRow[l]; ok {
				rid = regionOf[r]
			}
			if rid < 0 {
				seg = append(seg, l)
				continue
			}
			flush()
			if !emitted[rid] {
				out = append(out, regions[rid])
				emitted[rid] = true
			}
		}
		flush()
	}
	return out
}

// windowTable validates a window of >=2 table-like rows and renders it as a
// GFM table block. The rows share a >=2-column x grid (x intervals that
// overlap anywhere are one column) and every line must be narrow relative to
// the region width, which excludes flowing multi-column text.
func windowTable(rows [][]lineRef) (bool, mdBlock) {
	var lines []*textLine
	for _, row := range rows {
		for _, r := range row {
			lines = append(lines, r.line)
		}
	}

	// Build the column grid: x intervals that overlap anywhere belong to the
	// same column.
	byX := append([]*textLine(nil), lines...)
	sort.Slice(byX, func(i, j int) bool { return byX[i].Llx < byX[j].Llx })
	var cols [][2]float64
	for _, l := range byX {
		if n := len(cols); n > 0 && l.Llx <= cols[n-1][1] {
			if l.Urx > cols[n-1][1] {
				cols[n-1][1] = l.Urx
			}
			continue
		}
		cols = append(cols, [2]float64{l.Llx, l.Urx})
	}
	if len(cols) < 2 {
		return false, mdBlock{}
	}

	// Every line must be cell-like: narrow relative to the whole region.
	regionWidth := cols[len(cols)-1][1] - cols[0][0]
	size := 0.0
	for _, l := range lines {
		if l.fontsize > size {
			size = l.fontsize
		}
		if l.Urx-l.Llx > lineTableCellWidthR*regionWidth {
			return false, mdBlock{}
		}
	}

	rowsText := make([][]string, len(rows))
	for i, row := range rows {
		cells := make([]string, len(cols))
		for _, r := range row {
			idx := columnIndex(cols, r.line)
			if cells[idx] != "" {
				cells[idx] += " "
			}
			cells[idx] += strings.TrimSpace(r.line.text())
		}
		rowsText[i] = cells
	}

	var w bytes.Buffer
	writeGFMTable(rowsText, &w)
	b := mdBlock{size: size, isTable: true, text: w.String()}
	b.runes = utf8.RuneCountInString(b.text)
	return true, b
}

// bodyBlock builds a body-text block from a segment of lines.
func bodyBlock(seg []*textLine) mdBlock {
	var sb strings.Builder
	for i, l := range seg {
		if i > 0 {
			sb.WriteString(getSpace(seg[i-1].depth, l.depth))
		}
		sb.WriteString(l.text())
	}
	b := mdBlock{size: seg[0].fontsize, text: strings.TrimSpace(sb.String())}
	b.runes = utf8.RuneCountInString(b.text)
	return b
}

// columnIndex returns the index of the column in `cols` that overlaps line
// `l`.
func columnIndex(cols [][2]float64, l *textLine) int {
	for i, c := range cols {
		if l.Llx <= c[1] && l.Urx >= c[0] {
			return i
		}
	}
	return len(cols)
}

// reListItem matches the leading marker of a list item: a bullet character or
// an ordered label (1. / 1) / a. / A)).
var reListItem = regexp.MustCompile(`^(?:[-•*▪◦○●‣–]|\d{1,3}[.)]|[a-zA-Z][.)])\s+`)

// listItem is one rendered Markdown list item, with the x-position of its
// first line (indent) used to recover nesting.
type listItem struct {
	indent float64 // x-position of the first line; -1 if unknown
	text   string  // rendered item text (marker + content)
}

// detectLists groups runs of consecutive paragraph blocks that contain list
// item lines into single Markdown list blocks. Lines inside a block that
// start with a marker begin a new item; other lines continue the previous
// item. Body lines that precede the first item of a run (the page divider
// commonly merges a paragraph with the list that follows it) are emitted as a
// separate body block before the list. A run qualifies only if it yields >=2
// items. Item indentation is preserved as nested lists.
func detectLists(blocks []mdBlock) []mdBlock {
	hasListItem := func(b mdBlock) bool {
		if b.isTable || b.isList || b.para == nil {
			return false
		}
		for _, line := range strings.Split(b.text, "\n") {
			if reListItem.MatchString(strings.TrimSpace(line)) {
				return true
			}
		}
		return false
	}

	out := make([]mdBlock, 0, len(blocks))
	i := 0
	for i < len(blocks) {
		if !hasListItem(blocks[i]) {
			out = append(out, blocks[i])
			i++
			continue
		}
		j := i
		var items []listItem
		var preBody []string
		for j < len(blocks) && hasListItem(blocks[j]) {
			var pb []string
			pb, items = appendListItems(items, blocks[j], j == i)
			preBody = append(preBody, pb...)
			j++
		}
		if len(items) >= 2 {
			if len(preBody) > 0 {
				out = append(out, mdBlock{size: blocks[i].size, text: strings.Join(preBody, "\n")})
			}
			out = append(out, mdBlock{size: blocks[i].size, isList: true, text: renderList(items)})
		} else {
			out = append(out, blocks[i:j]...)
		}
		i = j
	}
	return out
}

// appendListItems appends the list items found in the lines of block `b` to
// `items`. A line starting with a list marker begins a new item; other lines
// continue the previous item. Ordered labels (1. / 2) / a.) are kept as-is;
// bullet characters are normalized to "-" (a "-" marker is left unchanged).
// For the first block of a run, non-marker lines that precede the first item
// are returned as `preBody`.
func appendListItems(items []listItem, b mdBlock, firstBlock bool) (preBody []string, _ []listItem) {
	paraLines := b.para.lines
	for idx, line := range strings.Split(b.text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		indent := -1.0
		if idx < len(paraLines) {
			indent = paraLines[idx].Llx
		} else if n := len(items); n > 0 {
			indent = items[n-1].indent // e.g. newlines embedded in one line
		}
		if m := reListItem.FindString(line); m != "" {
			items = append(items, listItem{indent: indent, text: renderItemMarker(m, line)})
			continue
		}
		if firstBlock && len(items) == 0 {
			preBody = append(preBody, line)
			continue
		}
		if n := len(items); n > 0 {
			// Continuation of the previous item.
			items[n-1].text += " " + line
		}
	}
	return preBody, items
}

// renderItemMarker returns the Markdown rendering of a matched list item line:
// ordered labels and "-" bullets are kept, other bullet characters are
// normalized to "-".
func renderItemMarker(m, line string) string {
	last := m[len(strings.TrimRight(m, " \t"))-1]
	if last == '.' || last == ')' {
		return m + strings.TrimSpace(line[len(m):]) // ordered label, valid Markdown as-is
	}
	if last == '-' {
		return line // already a Markdown bullet
	}
	return "- " + strings.TrimSpace(line[len(m):])
}

// renderList renders a list with nesting: each distinct item indent becomes a
// Markdown nesting level (2-space indent).
func renderList(items []listItem) string {
	var indents []float64
	seen := make(map[float64]bool)
	for _, it := range items {
		if it.indent < 0 || seen[it.indent] {
			continue
		}
		seen[it.indent] = true
		indents = append(indents, it.indent)
	}
	sort.Float64s(indents)
	level := make(map[float64]int, len(indents))
	for k, v := range indents {
		level[v] = k
	}

	var sb strings.Builder
	for _, it := range items {
		lvl := 0
		if it.indent >= 0 {
			lvl = level[it.indent]
		}
		if lvl > 3 {
			lvl = 3
		}
		sb.WriteString(strings.Repeat("  ", lvl))
		sb.WriteString(it.text)
		sb.WriteString("\n")
	}
	return sb.String()
}

// firstLineOf returns the first line of `s`.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// paraIsBold returns true if every text mark in the paragraph is drawn with a
// bold font (by font name). Paragraphs without marks never qualify.
func paraIsBold(p *textPara) bool {
	n, bold := 0, 0
	for _, l := range p.lines {
		for _, w := range l.words {
			for _, m := range w.marks {
				if m.font == nil {
					continue
				}
				n++
				if fontNameIsBold(m.font.BaseFont()) {
					bold++
				}
			}
		}
	}
	return n > 0 && bold == n
}

// fontNameIsBold returns true if a font (base) name indicates a bold weight.
func fontNameIsBold(name string) bool {
	for _, part := range []string{"Bold", "Black", "Heavy", "Semibold", "DemiBold", "Ultra"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

// escapeMarkdownBody prevents body text from being misread as Markdown
// structure: any line starting with a structural marker (#, >, -, +, *, =, |,
// backtick, or an ordered label like "1.") is prefixed with a backslash, and
// a trailing backslash (a Markdown hard line break) is escaped.
func escapeMarkdownBody(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		c := line[0]
		if c == '#' || c == '>' || c == '-' || c == '+' || c == '*' || c == '=' || c == '|' || c == '`' ||
			(c >= '0' && c <= '9' && reListItem.MatchString(line)) {
			lines[i] = "\\" + line
		} else if strings.HasSuffix(line, "\\") {
			lines[i] = line + "\\"
		}
	}
	return strings.Join(lines, "\n")
}

// escapeTableCell makes cell text safe for a Markdown table row.
func escapeTableCell(s string) string {
	r := strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ")
	return r.Replace(s)
}
