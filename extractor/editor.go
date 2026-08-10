/*
 * This file is subject to the terms and conditions defined in
 * file 'LICENSE.md', which is part of this source code package.
 */

package extractor

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/yudaprama/tools/pdf/common"
	"github.com/yudaprama/tools/pdf/contentstream"
	"github.com/yudaprama/tools/pdf/core"
	"github.com/yudaprama/tools/pdf/model"
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
func (e *Editor) Replace(pattern, replacement string, pages []int) error {
	if e == nil || e.reader == nil {
		return fmt.Errorf("nil editor or reader")
	}
	if pattern == "" {
		return fmt.Errorf("pattern cannot be empty")
	}

	targetPages, err := normalizePages(e.reader, pages)
	if err != nil {
		return err
	}

	for _, pageNum := range targetPages {
		page, err := e.reader.GetPage(pageNum)
		if err != nil {
			return err
		}
		if err := searchReplacePageText(page, pattern, replacement); err != nil {
			return err
		}
	}

	return nil
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
}

func (tc *textChunk) encode() {
	if tc.strObj == nil {
		return
	}
	encoded := tc.val
	if font := tc.font; font != nil {
		encodedBytes, numMisses := font.StringToCharcodeBytes(tc.val)
		if numMisses != 0 {
			common.Log.Debug("WARN: some runes could not be encoded")
		}
		encoded = string(encodedBytes)
	}
	*tc.strObj = *core.MakeString(encoded)
}

type textChunks struct {
	text   string
	chunks []*textChunk
}

func (tc *textChunks) replace(search, replacement string) {
	text := tc.text
	chunks := tc.chunks

	var chunkOffset int
	matchIdx := strings.Index(text, search)
	for currMatchIdx := matchIdx; matchIdx != -1; {
		for i, chunk := range chunks[chunkOffset:] {
			idx, lenChunk := chunk.idx, len(chunk.val)
			if currMatchIdx < idx || currMatchIdx > idx+lenChunk-1 {
				continue
			}
			chunkOffset += i + 1

			start := currMatchIdx - idx
			remaining := len(search) - (lenChunk - start)

			replaceVal := chunk.val[:start] + replacement
			if remaining < 0 {
				replaceVal += chunk.val[lenChunk+remaining:]
				chunkOffset--
			}

			chunk.val = replaceVal
			chunk.encode()

			for j := chunkOffset; remaining > 0 && j < len(chunks); j++ {
				c := chunks[j]
				l := len(c.val)

				if l > remaining {
					c.val = c.val[remaining:]
				} else {
					c.val = ""
					chunkOffset++
				}

				c.encode()
				remaining -= l
			}

			break
		}

		text = text[matchIdx+1:]
		matchIdx = strings.Index(text, search)
		currMatchIdx += matchIdx + 1
	}

	tc.text = strings.Replace(tc.text, search, replacement, -1)
}

func searchReplacePageText(page *model.PdfPage, searchText, replaceText string) error {
	contents, err := page.GetAllContentStreams()
	if err != nil {
		return err
	}

	ops, err := contentstream.NewContentStreamParser(contents).Parse()
	if err != nil {
		return err
	}

	var currFont *model.PdfFont
	tc := textChunks{}

	textProcFunc := func(objptr *core.PdfObject) {
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

		tc.chunks = append(tc.chunks, &textChunk{
			font:   currFont,
			strObj: strObj,
			val:    str,
			idx:    len(tc.text),
		})
		tc.text += str
	}

	processor := contentstream.NewContentStreamProcessor(*ops)
	processor.AddHandler(contentstream.HandlerConditionEnumAllOperands, "",
		func(op *contentstream.ContentStreamOperation, gs contentstream.GraphicsState, resources *model.PdfPageResources) error {
			switch op.Operand {
			case "Tj", "'":
				if len(op.Params) != 1 {
					common.Log.Debug("Invalid: Tj/' with invalid set of parameters - skip")
					return nil
				}
				textProcFunc(&op.Params[0])
			case "\"":
				if len(op.Params) != 3 {
					common.Log.Debug("Invalid: \" with invalid set of parameters - skip")
					return nil
				}
				textProcFunc(&op.Params[2])
			case "TJ":
				if len(op.Params) != 1 {
					common.Log.Debug("Invalid: TJ with invalid set of parameters - skip")
					return nil
				}
				arr, _ := core.GetArray(op.Params[0])
				for i := range arr.Elements() {
					obj := arr.Get(i)
					textProcFunc(&obj)
					arr.Set(i, obj)
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
			}
			return nil
		})

	if err = processor.Process(page.Resources); err != nil {
		return err
	}

	tc.replace(searchText, replaceText)
	return page.SetContentStreams([]string{ops.String()}, core.NewFlateEncoder())
}
