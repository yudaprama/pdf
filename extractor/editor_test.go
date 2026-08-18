package extractor

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yudaprama/pdf/contentstream"
	"github.com/yudaprama/pdf/core"
	"github.com/yudaprama/pdf/model"
)

func TestIndexAll(t *testing.T) {
	indexes := indexAll("abc abc abc", "abc")
	if len(indexes) != 3 {
		t.Fatalf("expected 3 matches, got %d", len(indexes))
	}
	want := []int{0, 4, 8}
	for i := range want {
		if indexes[i] != want[i] {
			t.Fatalf("unexpected index %d at pos %d", indexes[i], i)
		}
	}
}

func TestTextChunksReplaceSingleChunk(t *testing.T) {
	tc := textChunks{}
	chunk := &textChunk{val: "hello world", idx: 0, strObj: core.MakeString("hello world")}
	tc.chunks = append(tc.chunks, chunk)
	tc.text = chunk.val

	if err := tc.replace("world", "there"); err != nil {
		t.Fatalf("replace: %v", err)
	}

	if got, want := chunk.val, "hello there"; got != want {
		t.Fatalf("chunk value mismatch: got %q want %q", got, want)
	}
	if got, want := tc.text, "hello there"; got != want {
		t.Fatalf("text mismatch: got %q want %q", got, want)
	}
}

func TestTextChunksReplaceAcrossChunks(t *testing.T) {
	tc := textChunks{}
	first := &textChunk{val: "hel", idx: 0, strObj: core.MakeString("hel")}
	second := &textChunk{val: "lo world", idx: 3, strObj: core.MakeString("lo world")}
	tc.chunks = append(tc.chunks, first, second)
	tc.text = first.val + second.val

	if err := tc.replace("hello", "hi"); err != nil {
		t.Fatalf("replace: %v", err)
	}

	if got, want := first.val, "hi"; got != want {
		t.Fatalf("first chunk mismatch: got %q want %q", got, want)
	}
	if got, want := second.val, " world"; got != want {
		t.Fatalf("second chunk mismatch: got %q want %q", got, want)
	}
	if got, want := tc.text, "hi world"; got != want {
		t.Fatalf("text mismatch: got %q want %q", got, want)
	}
}

func TestTextChunksReplaceMultipleOccurrences(t *testing.T) {
	tc := textChunks{}
	chunks := []*textChunk{
		{val: "foo", idx: 0, strObj: core.MakeString("foo")},
		{val: " and foo", idx: 3, strObj: core.MakeString(" and foo")},
	}
	tc.chunks = append(tc.chunks, chunks...)
	tc.text = "foo and foo"

	if err := tc.replace("foo", "bar"); err != nil {
		t.Fatalf("replace: %v", err)
	}

	if got, want := tc.text, "bar and bar"; got != want {
		t.Fatalf("text mismatch: got %q want %q", got, want)
	}
	if got, want := chunks[0].val, "bar"; got != want {
		t.Fatalf("chunk 0 mismatch: got %q want %q", got, want)
	}
	if got, want := chunks[1].val, " and bar"; got != want {
		t.Fatalf("chunk 1 mismatch: got %q want %q", got, want)
	}
}

func TestTextChunksReplaceAcrossSyntheticSeparator(t *testing.T) {
	tc := textChunks{}
	first := &textChunk{val: "Hello", idx: 0, strObj: core.MakeString("Hello")}
	second := &textChunk{val: "World", idx: 5, sepBefore: " ", strObj: core.MakeString("World")}
	tc.chunks = append(tc.chunks, first, second)
	tc.text = "Hello World"

	if err := tc.replace("Hello World", "Hi"); err != nil {
		t.Fatalf("replace: %v", err)
	}

	if got, want := first.val, "Hi"; got != want {
		t.Fatalf("first chunk mismatch: got %q want %q", got, want)
	}
	if got, want := second.val, ""; got != want {
		t.Fatalf("second chunk mismatch: got %q want %q", got, want)
	}
	if got, want := tc.rebuildTextText(), "Hi"; got != want {
		t.Fatalf("text mismatch: got %q want %q", got, want)
	}
}

func TestTextChunksReplaceMidWordAcrossSeparator(t *testing.T) {
	tc := textChunks{}
	first := &textChunk{val: "Hello", idx: 0, strObj: core.MakeString("Hello")}
	second := &textChunk{val: "World", idx: 5, sepBefore: " ", strObj: core.MakeString("World")}
	tc.chunks = append(tc.chunks, first, second)
	tc.text = "Hello World"

	// "o W" spans the end of the first chunk, the separator and the start of
	// the second chunk. The separator itself must not consume characters: the
	// replacement is written where the match started and the covered part of
	// the second chunk is removed.
	if err := tc.replace("o W", "o-W"); err != nil {
		t.Fatalf("replace: %v", err)
	}

	if got, want := first.val, "Hello-W"; got != want {
		t.Fatalf("first chunk mismatch: got %q want %q", got, want)
	}
	if got, want := second.val, "orld"; got != want {
		t.Fatalf("second chunk mismatch: got %q want %q", got, want)
	}
	if got, want := tc.rebuildTextText(), "Hello-World"; got != want {
		t.Fatalf("text mismatch: got %q want %q", got, want)
	}
}

func TestTextChunksReplaceUnencodableRuneFails(t *testing.T) {
	font, err := model.NewStandard14Font(model.HelveticaName)
	if err != nil {
		t.Fatalf("NewStandard14Font: %v", err)
	}
	if font.RuneEncodable('\u0e01') {
		t.Fatalf("Thai rune should not be encodable in Helvetica")
	}
	if !font.RuneEncodable(' ') {
		t.Fatalf("space should be encodable in Helvetica")
	}

	tc := textChunks{}
	chunk := &textChunk{val: "hello", idx: 0, font: font, strObj: core.MakeString("hello")}
	tc.chunks = append(tc.chunks, chunk)
	tc.text = chunk.val

	err = tc.replace("hello", "h\u0e01llo")
	if err == nil {
		t.Fatalf("expected error for unencodable rune")
	}
	if !strings.Contains(err.Error(), "cannot encode") {
		t.Fatalf("unexpected error: %v", err)
	}
	// The chunk must not have been modified.
	if got, want := chunk.val, "hello"; got != want {
		t.Fatalf("chunk was modified on failure: got %q want %q", got, want)
	}
}

func TestTextChunksWidthCompensation(t *testing.T) {
	font, err := model.NewStandard14Font(model.HelveticaName)
	if err != nil {
		t.Fatalf("NewStandard14Font: %v", err)
	}

	strObj := core.MakeString("Hello")
	op := &contentstream.ContentStreamOperation{Operand: "Tj", Params: []core.PdfObject{strObj}}
	chunk := &textChunk{
		val:      "Hello",
		idx:      0,
		font:     font,
		strObj:   strObj,
		op:       op,
		fontSize: 12,
	}
	tc := textChunks{chunks: []*textChunk{chunk}, text: "Hello"}

	if err := tc.replace("Hello", "Hey"); err != nil {
		t.Fatalf("replace: %v", err)
	}

	// The op must have been converted to TJ with a trailing adjustment that
	// compensates the width difference (Hey is narrower than Hello).
	arr, ok := core.GetArray(op.Params[0])
	if !ok {
		t.Fatalf("expected TJ array, got %T", op.Params[0])
	}
	elems := arr.Elements()
	if len(elems) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(elems))
	}
	adj, ok := core.GetFloatVal(elems[1])
	if !ok {
		t.Fatalf("expected float adjustment, got %T", elems[1])
	}
	if adj <= 0 {
		t.Fatalf("expected positive compensation (deleted width), got %v", adj)
	}
}

// rebuildTextText is a test helper returning the rebuilt concatenation text.
func (tc *textChunks) rebuildTextText() string {
	tc.rebuildText()
	return tc.text
}

// buildTwoChunkPDF renders "Hello" and "World" as two separate Tj operations
// separated by a wide Td gap, so that the words only form "Hello World"
// through a synthetic separator.
func buildTwoChunkPDF(t *testing.T) []byte {
	font, err := model.NewStandard14Font(model.HelveticaName)
	if err != nil {
		t.Fatalf("NewStandard14Font: %v", err)
	}

	page := model.NewPdfPage()
	page.Resources = model.NewPdfPageResources()
	if err := page.Resources.SetFontByName("F1", font.ToPdfObject()); err != nil {
		t.Fatalf("SetFontByName: %v", err)
	}
	page.MediaBox = &model.PdfRectangle{Llx: 0, Lly: 0, Urx: 612, Ury: 792}

	content := "BT /F1 12 Tf 72 720 Td (Hello) Tj 40 0 Td (World) Tj ET"
	if err := page.SetContentStreams([]string{content}, core.NewFlateEncoder()); err != nil {
		t.Fatalf("SetContentStreams: %v", err)
	}

	writer := model.NewPdfWriter()
	if err := writer.AddPage(page); err != nil {
		t.Fatalf("AddPage: %v", err)
	}
	var buf bytes.Buffer
	if err := writer.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return buf.Bytes()
}

func extractAllText(t *testing.T, data []byte) string {
	reader, err := model.NewPdfReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("NewPdfReader: %v", err)
	}
	page, err := reader.GetPage(1)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	ex, err := New(page)
	if err != nil {
		t.Fatalf("New extractor: %v", err)
	}
	pageText, _, _, err := ex.ExtractPageText()
	if err != nil {
		t.Fatalf("ExtractPageText: %v", err)
	}
	return pageText.Text()
}

func TestEditorReplaceAcrossLayoutGap(t *testing.T) {
	pdfBytes := buildTwoChunkPDF(t)

	reader, err := model.NewPdfReader(bytes.NewReader(pdfBytes))
	if err != nil {
		t.Fatalf("NewPdfReader: %v", err)
	}
	editor := NewEditor(reader)
	// The pattern contains a space that only exists as a layout gap between
	// the two Tj chunks.
	if err := editor.Replace("Hello World", "Hi Earth", nil); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	var out bytes.Buffer
	if err := editor.Write(&out); err != nil {
		t.Fatalf("Write: %v", err)
	}

	text := extractAllText(t, out.Bytes())
	if strings.Contains(text, "Hello") || strings.Contains(text, "World") {
		t.Fatalf("old text still present: %q", text)
	}
	if !strings.Contains(text, "Hi") || !strings.Contains(text, "Earth") {
		t.Fatalf("replacement missing: %q", text)
	}
}

func TestEditorReplaceUnencodableRuneKeepsPageIntact(t *testing.T) {
	pdfBytes := buildTwoChunkPDF(t)

	reader, err := model.NewPdfReader(bytes.NewReader(pdfBytes))
	if err != nil {
		t.Fatalf("NewPdfReader: %v", err)
	}
	editor := NewEditor(reader)
	if err := editor.Replace("Hello World", "Hi\u0e01", nil); err == nil {
		t.Fatalf("expected error for unencodable rune")
	}
	var out bytes.Buffer
	if err := editor.Write(&out); err != nil {
		t.Fatalf("Write: %v", err)
	}
	text := extractAllText(t, out.Bytes())
	if !strings.Contains(text, "Hello") || !strings.Contains(text, "World") {
		t.Fatalf("original text must be untouched after failed replace: %q", text)
	}
}

func TestEditorReplaceWithSpaceAddsRealGlyphs(t *testing.T) {
	// Helvetica can encode spaces: a replacement with spaces must produce
	// real space glyphs (not positional gaps).
	pdfBytes := buildTwoChunkPDF(t)

	reader, err := model.NewPdfReader(bytes.NewReader(pdfBytes))
	if err != nil {
		t.Fatalf("NewPdfReader: %v", err)
	}
	editor := NewEditor(reader)
	if err := editor.Replace("Hello", "Hey There", nil); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	var out bytes.Buffer
	if err := editor.Write(&out); err != nil {
		t.Fatalf("Write: %v", err)
	}
	text := extractAllText(t, out.Bytes())
	if !strings.Contains(text, "Hey There") {
		t.Fatalf("expected %q in text, got %q", "Hey There", text)
	}
}
