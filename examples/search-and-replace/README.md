# Search and Replace Example

This example demonstrates text search and replacement using `extractor.Editor`.

## Run

```bash
go run ./examples/search-and-replace/replace_text.go "Australia" "America" "1,2" ./input.pdf ./output.pdf
```

- `pages` supports comma separated page numbers, for example `1,2,5`.
- Use `*` to process all pages.

## Notes

- Replacement is done by rewriting text operators in page content streams.
- Best results are achieved with PDFs that use standard text operators (`Tj`, `TJ`, `'`, `"`).
- Complex layout/fonts/encodings may require additional tuning.
