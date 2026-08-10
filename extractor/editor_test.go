package extractor

import "testing"

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
	chunk := &textChunk{val: "hello world", idx: 0}
	tc.chunks = append(tc.chunks, chunk)
	tc.text = chunk.val

	tc.replace("world", "there")

	if got, want := chunk.val, "hello there"; got != want {
		t.Fatalf("chunk value mismatch: got %q want %q", got, want)
	}
	if got, want := tc.text, "hello there"; got != want {
		t.Fatalf("text mismatch: got %q want %q", got, want)
	}
}

func TestTextChunksReplaceAcrossChunks(t *testing.T) {
	tc := textChunks{}
	first := &textChunk{val: "hel", idx: 0}
	second := &textChunk{val: "lo world", idx: 3}
	tc.chunks = append(tc.chunks, first, second)
	tc.text = first.val + second.val

	tc.replace("hello", "hi")

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
		{val: "foo", idx: 0},
		{val: " and foo", idx: 3},
	}
	tc.chunks = append(tc.chunks, chunks...)
	tc.text = "foo and foo"

	tc.replace("foo", "bar")

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
