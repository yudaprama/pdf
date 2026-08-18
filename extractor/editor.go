/*
 * This file is subject to the terms and conditions defined in
 * file 'LICENSE.md', which is part of this source code package.
 */

package extractor

import (
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/yudaprama/pdf/common"
	"github.com/yudaprama/pdf/contentstream"
	"github.com/yudaprama/pdf/core"
	"github.com/yudaprama/pdf/model"
)

// Box describes a rectangular area on a page, typically a text match location.
type Box struct {
	BBox model.PdfRectangle
}

// Match contains search results for one page.
type Match struct {
	Pattern   string
	Indexes   [][]int
	Locations []Box
}

// PageReplace summarizes the replacements performed on one page.
type PageReplace struct {
	Page     int
	Matched  int // occurrences of the pattern found in the page text
	Replaced int // occurrences replaced
}

// ReplaceReport summarizes a Replace operation across all processed pages.
type ReplaceReport struct {
	Pattern     string
	Replacement string
	Pages       []PageReplace
}

// TotalMatched returns the number of pattern occurrences found.
func (r ReplaceReport) TotalMatched() int {
	total := 0
	for _, p := range r.Pages {
		total += p.Matched
	}
	return total
}

// TotalReplaced returns the number of occurrences replaced.
func (r ReplaceReport) TotalReplaced() int {
	total := 0
	for _, p := range r.Pages {
		total += p.Replaced
	}
	return total
}

// Editor provides text search and replacement helpers for a PDF reader.
type Editor struct {
	reader *model.PdfReader
}

// NewEditor returns a new editor instance for the provided reader.
func NewEditor(reader *model.PdfReader) *Editor {
	return &Editor{reader: reader}
}

// Search finds all occurrences of pattern on the selected pages.
// If pages is empty, all pages are searched.
func (e *Editor) Search(pattern string, pages []int) (map[int]Match, error) {
	if e == nil || e.reader == nil {
		return nil, fmt.Errorf("nil editor or reader")
	}
	if pattern == "" {
		return nil, fmt.Errorf("pattern cannot be empty")
	}

	targetPages, err := normalizePages(e.reader, pages)
	if err != nil {
		return nil, err
	}

	matchesPerPage := map[int]Match{}
	for _, pageNum := range targetPages {
		page, err := e.reader.GetPage(pageNum)
		if err != nil {
			return nil, err
		}
		ex, err := New(page)
		if err != nil {
			return nil, err
		}
		pageText, _, _, err := ex.ExtractPageText()
		if err != nil {
			return nil, err
		}

		match, err := getMatch(pageText.Text(), pageText.Marks(), pattern)
		if err != nil {
			return nil, err
		}
		if len(match.Indexes) > 0 {
			matchesPerPage[pageNum] = match
		}
	}

	return matchesPerPage, nil
}

// Replace replaces all occurrences of pattern with replacement on selected pages.
// If pages is empty, all pages are processed.
//
// Patterns may contain spaces that span layout gaps in the source document
// (text split across multiple show-text operations): the gaps are matched via
// synthetic separators and never consume encoded characters. Replacement
// width changes are compensated with TJ adjustments so that the glyphs
// following a replacement stay in place. An error is returned (without
// modifying the page) when a font cannot encode a replacement rune, in which
// case no characters are silently dropped.
func (e *Editor) Replace(pattern, replacement string, pages []int) error {
	_, err := e.ReplaceWithReport(pattern, replacement, pages)
	return err
}

// ReplaceWithReport replaces all occurrences of pattern with replacement on
// selected pages and returns a per-page summary. If pages is empty, all pages
// are processed.
//
// Patterns may contain spaces that span layout gaps in the source document
// (text split across multiple show-text operations): the gaps are matched via
// synthetic separators and never consume encoded characters. Replacement
// width changes are compensated with TJ adjustments so that the glyphs
// following a replacement stay in place. An error is returned (without
// modifying the document) when a font cannot encode a replacement rune, in
// which case no characters are silently dropped.
func (e *Editor) ReplaceWithReport(pattern, replacement string, pages []int) (ReplaceReport, error) {
	report := ReplaceReport{Pattern: pattern, Replacement: replacement}
	if e == nil || e.reader == nil {
		return report, fmt.Errorf("nil editor or reader")
	}
	if pattern == "" {
		return report, fmt.Errorf("pattern cannot be empty")
	}

	targetPages, err := normalizePages(e.reader, pages)
	if err != nil {
		return report, err
	}

	for _, pageNum := range targetPages {
		page, err := e.reader.GetPage(pageNum)
		if err != nil {
			return report, err
		}
		matched, replaced, err := searchReplacePageText(page, pattern, replacement)
		if err != nil {
			return report, err
		}
		if matched > 0 {
			report.Pages = append(report.Pages, PageReplace{
				Page:     pageNum,
				Matched:  matched,
				Replaced: replaced,
			})
		}
	}

	return report, nil
}

// Write writes all pages from the reader to writer.
func (e *Editor) Write(writer io.Writer) error {
	if e == nil || e.reader == nil {
		return fmt.Errorf("nil editor or reader")
	}
	pdfWriter := model.NewPdfWriter()

	numPages, err := e.reader.GetNumPages()
	if err != nil {
		return err
	}
	for pageNum := 1; pageNum <= numPages; pageNum++ {
		page, err := e.reader.GetPage(pageNum)
		if err != nil {
			return err
		}
		if err := pdfWriter.AddPage(page); err != nil {
			return err
		}
	}

	return pdfWriter.Write(writer)
}

// WriteToFile writes all pages from the reader to outputPath.
func (e *Editor) WriteToFile(outputPath string) error {
	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return e.Write(f)
}

func normalizePages(reader *model.PdfReader, pages []int) ([]int, error) {
	numPages, err := reader.GetNumPages()
	if err != nil {
		return nil, err
	}

	if len(pages) == 0 {
		all := make([]int, numPages)
		for i := 1; i <= numPages; i++ {
			all[i-1] = i
		}
		return all, nil
	}

	seen := map[int]struct{}{}
	out := make([]int, 0, len(pages))
	for _, page := range pages {
		if page < 1 || page > numPages {
			return nil, fmt.Errorf("invalid page number %d (valid range: 1..%d)", page, numPages)
		}
		if _, ok := seen[page]; ok {
			continue
		}
		seen[page] = struct{}{}
		out = append(out, page)
	}
	sort.Ints(out)
	return out, nil
}

func getMatch(text string, textMarks *TextMarkArray, pattern string) (Match, error) {
	indexes := indexAll(text, pattern)
	if len(indexes) == 0 {
		return Match{Pattern: pattern}, nil
	}

	m := Match{
		Pattern:   pattern,
		Indexes:   make([][]int, 0, len(indexes)),
		Locations: make([]Box, 0, len(indexes)),
	}
	for _, start := range indexes {
		end := start + len(pattern)
		spanMarks, err := textMarks.RangeOffset(start, end)
		if err != nil {
			return Match{}, err
		}
		bbox, ok := spanMarks.BBox()
		if !ok {
			return Match{}, fmt.Errorf("spanMarks.BBox has no bounding box")
		}
		m.Indexes = append(m.Indexes, []int{start, end})
		m.Locations = append(m.Locations, Box{BBox: bbox})
	}
	return m, nil
}

func indexAll(text, term string) []int {
	if term == "" {
		return nil
	}
	indexes := []int{}
	for start := 0; start < len(text); {
		i := strings.Index(text[start:], term)
		if i < 0 {
			return indexes
		}
		indexes = append(indexes, start+i)
		start += i + len(term)
	}
	return indexes
}

type textChunk struct {
	font   *model.PdfFont
	strObj *core.PdfObjectString
	val    string
	idx    int

	// sepBefore is a synthetic separator ("" / " " / "\n") inserted before this
	// chunk in the concatenated text (tc.text) to model layout gaps (word gaps,
	// line breaks) between chunks. It carries no glyph: it exists only so that
	// patterns containing spaces can match text that is split across multiple
	// show-text operations. Replacing across it never consumes encoded
	// characters.
	sepBefore string

	// op is the show-text operation that owns strObj. tjArr is the containing
	// TJ array (nil for Tj) and tjIdx is the index of strObj within it. They
	// are used to write width-compensation adjustments back into the stream.
	op    *contentstream.ContentStreamOperation
	tjArr *core.PdfObjectArray
	tjIdx int

	fontSize float64

	// extraSegments holds additional string elements rendered after strObj
	// when the replacement contains spaces that the font cannot encode.
	// Synthetic spaces of extraSpaceW (in 1/1000 text space units, i.e. the
	// same units as /Widths entries and TJ adjustment numbers) are inserted
	// between the segments. adjAfter is the width compensation inserted after
	// the chunk's last element so that following glyphs stay in place.
	extraSegments []string
	extraSpaceW   float64
	adjAfter      float64
}

// encodedString returns val encoded with the chunk's font (raw when the font
// is unknown).
func (tc *textChunk) encodedString(val string) *core.PdfObjectString {
	encoded := val
	if tc.font != nil {
		encodedBytes, numMisses := tc.font.StringToCharcodeBytes(val)
		if numMisses != 0 {
			common.Log.Debug("WARN: some runes could not be encoded")
		}
		encoded = string(encodedBytes)
	}
	return core.MakeString(encoded)
}

func (tc *textChunk) encode() {
	if tc.strObj == nil {
		return
	}
	*tc.strObj = *tc.encodedString(tc.val)
}

type textChunks struct {
	text   string
	chunks []*textChunk

	// opRewrites maps show-text operations that must be replaced by operator
	// sequences (e.g. ' → T* + TJ) to those sequences. It is populated by
	// applyAdjustments and consumed by the caller that owns the operation list.
	opRewrites map[*contentstream.ContentStreamOperation][]*contentstream.ContentStreamOperation
}

// Layout gap classification, relative to the font size in use. The ratios are
// shared with the text extractor (text_const.go) so that Search and Replace
// classify the same gaps the same way.
const (
	defaultSpaceW = 250. // fallback synthetic space width (1/1000 units)
)

// textPen tracks the text-space pen position across show-text and positioning
// operations so that layout gaps between chunks can be classified as word
// spaces or line breaks.
type textPen struct {
	originX, originY float64 // current line origin
	x, y             float64 // pen position
	leading          float64
	fontSize         float64

	// dirX, dirY is the unit writing direction in device space, taken from the
	// text matrix. It lets rotated text advance and classify gaps along the
	// actual writing direction instead of assuming horizontal text.
	dirX, dirY float64

	// repositioned is set when a positioning operation occurred since the last
	// show-text operation. sepHint is the fallback separator classification
	// used when the exact gap cannot be computed.
	repositioned bool
	sepHint      string

	prevEndX, prevEndY float64 // pen position after the last shown chunk
	prevEndKnown       bool
}

// setDirection updates the writing direction from the vector components of
// the text matrix. Returns the pen so calls can be chained.
func (p *textPen) setDirection(a, b float64) {
	// The writing direction in text space is +x; in device space it maps to
	// (a, b) (Tm's first column).
	len := math.Hypot(a, b)
	if len < 1e-9 {
		p.dirX, p.dirY = 1, 0
		return
	}
	p.dirX, p.dirY = a/len, b/len
}

// classifyGap returns the synthetic separator implied by moving the pen to
// (x, y) from the end of the previously shown chunk. The gap is projected
// onto the writing direction (word space) and perpendicular to it (line
// break), matching the extractor's maxWordAdvanceR / lineDepthR ratios.
func (p *textPen) classifyGap(x, y float64) string {
	if !p.prevEndKnown {
		return p.sepHint
	}
	fs := p.fontSize
	if fs <= 0 {
		fs = 10
	}
	dx := x - p.prevEndX
	dy := y - p.prevEndY
	readingGap := dx*p.dirX + dy*p.dirY
	lineGap := math.Abs(-dx*p.dirY + dy*p.dirX)
	switch {
	case lineGap >= lineDepthR*fs:
		return "\n"
	case readingGap >= maxWordAdvanceR*fs:
		return " "
	default:
		return ""
	}
}

// advance moves the pen along the writing direction by the text-space width
// `w` (already scaled to points).
func (p *textPen) advance(w float64) {
	p.x += w * p.dirX
	p.y += w * p.dirY
}

// widthOf returns the summed width of the runes in `s` in 1/1000 text space
// units (the same units as /Widths entries and TJ adjustment numbers), and
// whether all metrics were available.
func widthOf(font *model.PdfFont, s string) (float64, bool) {
	if font == nil {
		return 0, false
	}
	if s == "" {
		return 0, true
	}
	var total float64
	for _, r := range s {
		m, ok := font.GetRuneMetrics(r)
		if !ok {
			return 0, false
		}
		total += m.Wx
	}
	return total, true
}

// spaceWidthOf returns the width used for synthetic spaces.
func spaceWidthOf(font *model.PdfFont) float64 {
	if font != nil {
		if m, ok := font.GetRuneMetrics(' '); ok && m.Wx > 0 {
			return m.Wx
		}
	}
	return defaultSpaceW
}

func isSpaceRune(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\u00a0':
		return true
	}
	return false
}

// boundaryNeedsSpace reports whether a synthetic separator between `prev` and
// `cur` is meaningful, i.e. neither side already carries whitespace at the
// boundary.
func boundaryNeedsSpace(prev, cur string) bool {
	if prev == "" || cur == "" {
		return false
	}
	rp, rc := []rune(prev), []rune(cur)
	return !isSpaceRune(rp[len(rp)-1]) && !isSpaceRune(rc[0])
}

// textSpan is a byte range [a, b) within the val of chunk ci.
type textSpan struct {
	ci   int
	a, b int
}

// segments decomposes the byte range [start, end) of tc.text into the chunk
// val ranges it covers. Synthetic separators between chunks are not part of
// any chunk val.
func (tc *textChunks) segments(start, end int) []textSpan {
	var spans []textSpan
	for ci, ch := range tc.chunks {
		vs := ch.idx + len(ch.sepBefore)
		ve := vs + len(ch.val)
		if ve <= start || vs >= end {
			continue
		}
		a, b := start, end
		if a < vs {
			a = vs
		}
		if b > ve {
			b = ve
		}
		spans = append(spans, textSpan{ci: ci, a: a - vs, b: b - vs})
	}
	return spans
}

// validateReplacement checks that every rune of `replacement` can be encoded
// by the font of the chunk where each match starts, so that replacement never
// silently drops characters. A space that the font cannot encode is allowed:
// it is rendered positionally via a TJ adjustment.
func (tc *textChunks) validateReplacement(search, replacement string) error {
	if replacement == "" {
		return nil
	}
	for _, start := range indexAll(tc.text, search) {
		spans := tc.segments(start, start+len(search))
		if len(spans) == 0 {
			continue
		}
		font := tc.chunks[spans[0].ci].font
		if font == nil {
			continue
		}
		for _, r := range replacement {
			if font.RuneEncodable(r) || r == ' ' {
				continue
			}
			return fmt.Errorf("cannot replace %q: font %q cannot encode rune %+q; use a replacement supported by the font",
				search, font.BaseFont(), r)
		}
	}
	return nil
}

// spliceMatch applies one replacement for the match [start, end) of tc.text.
// The replacement is written into the first chunk covered by the match; the
// matched parts of the following chunks are removed, keeping the glyphs after
// the match in place via TJ width compensation.
func (tc *textChunks) spliceMatch(start, end int, replacement string, matchesPerChunk map[*textChunk]int) error {
	spans := tc.segments(start, end)
	if len(spans) == 0 {
		common.Log.Debug("replace: match maps to no chunks (synthetic separator only); skipping")
		return nil
	}

	for _, sp := range spans[1:] {
		ch := tc.chunks[sp.ci]
		delW, ok := widthOf(ch.font, ch.val[sp.a:sp.b])
		ch.val = ch.val[:sp.a] + ch.val[sp.b:]
		if ok {
			ch.adjAfter += delW
		}
		ch.encode()
	}

	// Separators fully covered by the match are consumed by the replacement.
	for _, ch := range tc.chunks {
		if ch.idx >= start && ch.idx+len(ch.sepBefore) <= end {
			ch.sepBefore = ""
		}
	}

	first := spans[0]
	ch := tc.chunks[first.ci]
	oldVal := ch.val
	oldW, oldOK := widthOf(ch.font, oldVal[first.a:first.b])

	// Split the replacement into words when the font cannot encode spaces;
	// the spaces are then rendered positionally as TJ adjustments.
	splitSpace := ch.font != nil && strings.ContainsRune(replacement, ' ') && !ch.font.RuneEncodable(' ')
	if splitSpace {
		if ch.op == nil {
			return fmt.Errorf("cannot insert spaces with font %q: no surrounding text operator to adjust", ch.font.BaseFont())
		}
		if matchesPerChunk[ch] > 1 {
			return fmt.Errorf("cannot insert spaces with font %q: multiple matches within one string", ch.font.BaseFont())
		}
	}

	var words []string
	if splitSpace {
		words = strings.Split(replacement, " ")
		ch.extraSpaceW = spaceWidthOf(ch.font)
	} else {
		words = []string{replacement}
	}

	head := oldVal[:first.a] + words[0]
	tail := oldVal[first.b:]
	if len(words) == 1 {
		ch.val = head + tail
	} else {
		ch.val = head
		ch.extraSegments = append(ch.extraSegments, words[1:]...)
		ch.extraSegments[len(ch.extraSegments)-1] += tail
	}
	ch.encode()

	if oldOK {
		newW, ok := widthOf(ch.font, words[0])
		for _, w := range words[1:] {
			ww, wok := widthOf(ch.font, w)
			if !wok {
				ok = false
				break
			}
			newW += ww
		}
		if ok {
			if splitSpace {
				newW += float64(len(words)-1) * ch.extraSpaceW
			}
			ch.adjAfter += oldW - newW
		}
	}
	return nil
}

func (tc *textChunks) rebuildText() {
	var sb strings.Builder
	pos := 0
	for _, ch := range tc.chunks {
		ch.idx = pos
		sb.WriteString(ch.sepBefore)
		sb.WriteString(ch.val)
		pos += len(ch.sepBefore) + len(ch.val)
	}
	tc.text = sb.String()
}

// applyAdjustments writes pending width compensations and synthetic spaces
// back into the content stream operations. Plain Tj operations are converted
// to TJ so that adjustment numbers can be expressed. The ' and " operators
// cannot carry adjustments, so they are rewritten into their equivalent
// operator sequences (T* Tj and Tw/Tc/T* Tj respectively); the returned map
// maps each rewritten operation to its replacement operations, for the caller
// to splice into the operation list.
func (tc *textChunks) applyAdjustments() map[*contentstream.ContentStreamOperation][]*contentstream.ContentStreamOperation {
	type insertion struct {
		afterIdx int
		elems    []core.PdfObject
	}
	arrInserts := map[*core.PdfObjectArray][]insertion{}
	opRewrites := map[*contentstream.ContentStreamOperation][]*contentstream.ContentStreamOperation{}

	for _, ch := range tc.chunks {
		if ch.adjAfter == 0 && len(ch.extraSegments) == 0 {
			continue
		}
		if ch.op == nil {
			continue
		}

		var elems []core.PdfObject
		for _, w := range ch.extraSegments {
			// Negative adjustment: the following glyphs are moved to the right,
			// creating a visible space.
			elems = append(elems, core.MakeFloat(-ch.extraSpaceW), ch.encodedString(w))
		}
		if ch.adjAfter != 0 {
			elems = append(elems, core.MakeFloat(ch.adjAfter))
		}

		switch ch.op.Operand {
		case "TJ":
			if ch.tjArr != nil {
				arrInserts[ch.tjArr] = append(arrInserts[ch.tjArr], insertion{afterIdx: ch.tjIdx, elems: elems})
			}
		case "Tj":
			if len(ch.op.Params) == 1 {
				arr := core.MakeArray(ch.op.Params[0])
				arr.Append(elems...)
				ch.op.Params = []core.PdfObject{arr}
				ch.op.Operand = "TJ"
			}
		case "'":
			// ' is T* followed by Tj: preserve the line move, then show via TJ.
			if len(ch.op.Params) == 1 {
				arr := core.MakeArray(ch.op.Params[0])
				arr.Append(elems...)
				opRewrites[ch.op] = []*contentstream.ContentStreamOperation{
					{Operand: "T*"},
					{Operand: "TJ", Params: []core.PdfObject{arr}},
				}
			}
		case "\"":
			// " is aw Tw, ac Tc, T*, Tj in one operator.
			if len(ch.op.Params) == 3 {
				arr := core.MakeArray(ch.op.Params[2])
				arr.Append(elems...)
				opRewrites[ch.op] = []*contentstream.ContentStreamOperation{
					{Operand: "Tw", Params: []core.PdfObject{ch.op.Params[0]}},
					{Operand: "Tc", Params: []core.PdfObject{ch.op.Params[1]}},
					{Operand: "T*"},
					{Operand: "TJ", Params: []core.PdfObject{arr}},
				}
			}
		default:
			common.Log.Debug("replace: skipping width compensation for operator %q", ch.op.Operand)
		}
	}

	for arr, ins := range arrInserts {
		sort.Slice(ins, func(i, j int) bool { return ins[i].afterIdx > ins[j].afterIdx })
		for _, in := range ins {
			elems := arr.Elements()
			if in.afterIdx < 0 || in.afterIdx >= len(elems) {
				continue
			}
			newArr := core.MakeArray(elems[:in.afterIdx+1]...)
			newArr.Append(in.elems...)
			newArr.Append(elems[in.afterIdx+1:]...)
			*arr = *newArr
		}
	}
	return opRewrites
}

func (tc *textChunks) replace(search, replacement string) error {
	if search == "" {
		return nil
	}
	matches := indexAll(tc.text, search)
	if len(matches) == 0 {
		return nil
	}
	if err := tc.validateReplacement(search, replacement); err != nil {
		return err
	}

	// matchesPerChunk decides whether positional-space splitting is safe (it
	// is only supported for chunks touched by a single match).
	matchesPerChunk := map[*textChunk]int{}
	for _, start := range matches {
		for _, sp := range tc.segments(start, start+len(search)) {
			matchesPerChunk[tc.chunks[sp.ci]]++
		}
	}

	// Apply right-to-left so that the offsets of the remaining matches stay
	// valid while the chunk values are being spliced.
	for i := len(matches) - 1; i >= 0; i-- {
		if err := tc.spliceMatch(matches[i], matches[i]+len(search), replacement, matchesPerChunk); err != nil {
			return err
		}
	}

	tc.rebuildText()
	tc.opRewrites = tc.applyAdjustments()
	return nil
}

// applyOpRewrites returns a copy of `ops` where every rewritten operation is
// replaced by its operator sequence.
func (tc *textChunks) applyOpRewrites(ops []*contentstream.ContentStreamOperation) []*contentstream.ContentStreamOperation {
	if len(tc.opRewrites) == 0 {
		return ops
	}
	out := make([]*contentstream.ContentStreamOperation, 0, len(ops))
	for _, op := range ops {
		if repl, ok := tc.opRewrites[op]; ok {
			out = append(out, repl...)
			continue
		}
		out = append(out, op)
	}
	return out
}

func searchReplacePageText(page *model.PdfPage, searchText, replaceText string) (matched, replaced int, err error) {
	contents, err := page.GetAllContentStreams()
	if err != nil {
		return 0, 0, err
	}

	ops, err := contentstream.NewContentStreamParser(contents).Parse()
	if err != nil {
		return 0, 0, err
	}

	var currFont *model.PdfFont
	tc := textChunks{}
	pen := textPen{}

	addChunk := func(objptr *core.PdfObject, op *contentstream.ContentStreamOperation,
		tjArr *core.PdfObjectArray, tjIdx int, sepOverride string) {
		strObj, ok := core.GetString(*objptr)
		if !ok {
			common.Log.Debug("Invalid parameter, skipping")
			return
		}

		str := strObj.String()
		if currFont != nil {
			decoded, _, numMisses := currFont.CharcodeBytesToUnicode(strObj.Bytes())
			if numMisses != 0 {
				common.Log.Debug("WARN: some charcodes could not be decoded")
			}
			str = decoded
		}

		sep := ""
		if len(tc.chunks) > 0 {
			if sepOverride != "" {
				sep = sepOverride
			} else if pen.repositioned {
				sep = pen.classifyGap(pen.x, pen.y)
			}
			if sep != "" && !boundaryNeedsSpace(tc.chunks[len(tc.chunks)-1].val, str) {
				sep = ""
			}
		}

		tc.chunks = append(tc.chunks, &textChunk{
			font:      currFont,
			strObj:    strObj,
			val:       str,
			idx:       len(tc.text),
			sepBefore: sep,
			op:        op,
			tjArr:     tjArr,
			tjIdx:     tjIdx,
			fontSize:  pen.fontSize,
		})
		tc.text += sep + str

		if w, ok := widthOf(currFont, str); ok && pen.fontSize > 0 {
			pen.advance(w / 1000.0 * pen.fontSize)
			pen.prevEndX, pen.prevEndY = pen.x, pen.y
			pen.prevEndKnown = true
		} else {
			pen.prevEndKnown = false
		}
		pen.repositioned = false
	}

	tstar := func() {
		pen.originY -= pen.leading
		pen.x, pen.y = pen.originX, pen.originY
		pen.repositioned = true
		pen.sepHint = "\n"
		if pen.leading <= 0 {
			pen.sepHint = " "
		}
	}

	processor := contentstream.NewContentStreamProcessor(*ops)
	processor.AddHandler(contentstream.HandlerConditionEnumAllOperands, "",
		func(op *contentstream.ContentStreamOperation, gs contentstream.GraphicsState, resources *model.PdfPageResources) error {
			switch op.Operand {
			case "BT":
				pen.originX, pen.originY = 0, 0
				pen.x, pen.y = 0, 0
				pen.setDirection(1, 0)
				pen.repositioned = false
				pen.prevEndKnown = false
			case "Tm":
				if len(op.Params) == 6 {
					if f, err := core.GetNumbersAsFloat(op.Params); err == nil {
						pen.setDirection(f[0], f[1])
						pen.sepHint = pen.classifyGap(f[4], f[5])
						pen.originX, pen.originY = f[4], f[5]
						pen.x, pen.y = f[4], f[5]
						pen.repositioned = true
					}
				}
			case "Td", "TD":
				if len(op.Params) == 2 {
					if f, err := core.GetNumbersAsFloat(op.Params); err == nil {
						if op.Operand == "TD" {
							pen.leading = -f[1]
						}
						pen.sepHint = pen.classifyGap(pen.originX+f[0], pen.originY+f[1])
						pen.originX += f[0]
						pen.originY += f[1]
						pen.x, pen.y = pen.originX, pen.originY
						pen.repositioned = true
					}
				}
			case "T*":
				tstar()
			case "TL":
				if len(op.Params) == 1 {
					if f, err := core.GetNumberAsFloat(op.Params[0]); err == nil {
						pen.leading = f
					}
				}
			case "Tj":
				if len(op.Params) != 1 {
					common.Log.Debug("Invalid: Tj with invalid set of parameters - skip")
					return nil
				}
				addChunk(&op.Params[0], op, nil, 0, "")
			case "'":
				if len(op.Params) != 1 {
					common.Log.Debug("Invalid: ' with invalid set of parameters - skip")
					return nil
				}
				tstar()
				addChunk(&op.Params[0], op, nil, 0, "")
			case "\"":
				if len(op.Params) != 3 {
					common.Log.Debug("Invalid: \" with invalid set of parameters - skip")
					return nil
				}
				tstar()
				addChunk(&op.Params[2], op, nil, 0, "")
			case "TJ":
				if len(op.Params) != 1 {
					common.Log.Debug("Invalid: TJ with invalid set of parameters - skip")
					return nil
				}
				arr, _ := core.GetArray(op.Params[0])
				if arr == nil {
					return nil
				}
				pendingAdj := 0.0
				for i := range arr.Elements() {
					obj := arr.Get(i)
					switch obj.(type) {
					case *core.PdfObjectString:
						sep := ""
						if i > 0 && pendingAdj != 0 && pen.fontSize > 0 {
							gap := -pendingAdj / 1000.0 * pen.fontSize
							if gap >= maxWordAdvanceR*pen.fontSize {
								sep = " "
							}
						}
						addChunk(&obj, op, arr, i, sep)
						pendingAdj = 0
					case *core.PdfObjectFloat, *core.PdfObjectInteger:
						if v, err := core.GetNumberAsFloat(obj); err == nil {
							pendingAdj += v
						}
					}
					if err := arr.Set(i, obj); err != nil {
						common.Log.Debug("WARN: could not set TJ array element %d: %v", i, err)
					}
				}
			case "Tf":
				if len(op.Params) != 2 || resources == nil {
					return nil
				}
				fname, ok := core.GetName(op.Params[0])
				if !ok || fname == nil {
					return nil
				}
				fObj, has := resources.GetFontByName(*fname)
				if !has {
					return nil
				}
				pdfFont, err := model.NewPdfFontFromPdfObject(fObj)
				if err != nil {
					return nil
				}
				currFont = pdfFont
				if fs, err := core.GetNumberAsFloat(op.Params[1]); err == nil {
					pen.fontSize = fs
				}
			}
			return nil
		})

	if err = processor.Process(page.Resources); err != nil {
		return 0, 0, err
	}

	matched = len(indexAll(tc.text, searchText))
	if err := tc.replace(searchText, replaceText); err != nil {
		return matched, 0, err
	}
	finalOps := contentstream.ContentStreamOperations(tc.applyOpRewrites(*ops))
	if err := page.SetContentStreams([]string{finalOps.String()}, core.NewFlateEncoder()); err != nil {
		return matched, 0, err
	}
	return matched, matched, nil
}
