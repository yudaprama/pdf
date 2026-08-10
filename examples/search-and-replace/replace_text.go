package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/yudaprama/pdf/extractor"
	"github.com/yudaprama/pdf/model"
)

func main() {
	if len(os.Args) < 6 {
		fmt.Println("Usage: go run replace_text.go <pattern> <replacement> <pages> <input> <output>")
		fmt.Println("Example: go run replace_text.go \"Australia\" \"America\" \"1,2\" ./input.pdf ./output.pdf")
		fmt.Println("Use '*' for all pages")
		os.Exit(1)
	}

	pattern := os.Args[1]
	replacement := os.Args[2]
	pagesArg := os.Args[3]
	inputPath := os.Args[4]
	outputPath := os.Args[5]

	pages, err := parsePages(pagesArg)
	if err != nil {
		fmt.Printf("invalid pages argument: %v\n", err)
		os.Exit(1)
	}

	f, err := os.Open(inputPath)
	if err != nil {
		fmt.Printf("failed to open input file: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	reader, err := model.NewPdfReader(f)
	if err != nil {
		fmt.Printf("failed to create PDF reader: %v\n", err)
		os.Exit(1)
	}

	editor := extractor.NewEditor(reader)
	matches, err := editor.Search(pattern, pages)
	if err != nil {
		fmt.Printf("search failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("matches found on %d page(s)\n", len(matches))

	if err = editor.Replace(pattern, replacement, pages); err != nil {
		fmt.Printf("replace failed: %v\n", err)
		os.Exit(1)
	}

	if err = editor.WriteToFile(outputPath); err != nil {
		fmt.Printf("failed to write output file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("finished replacing %q with %q -> %s\n", pattern, replacement, outputPath)
}

func parsePages(arg string) ([]int, error) {
	if arg == "*" {
		return nil, nil
	}

	parts := strings.Split(arg, ",")
	pages := make([]int, 0, len(parts))
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		page, err := strconv.Atoi(p)
		if err != nil {
			return nil, err
		}
		pages = append(pages, page)
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("no pages provided")
	}
	return pages, nil
}
