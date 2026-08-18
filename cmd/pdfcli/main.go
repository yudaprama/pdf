package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/yudaprama/pdf/core"
	"github.com/yudaprama/pdf/extractor"
	"github.com/yudaprama/pdf/model"
)

type pdfWriterCfg struct {
	title    string
	author   string
	subject  string
	keywords string
	creator  string
	producer string
}

type exitCodeError struct {
	code int
	msg  string
}

func (e exitCodeError) Error() string { return e.msg }

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	cmd := args[0]
	args = args[1:]

	var err error
	switch cmd {
	case "extract":
		err = cmdExtract(args)
	case "search":
		err = cmdSearch(args)
	case "replace":
		err = cmdReplace(args)
	case "merge":
		err = cmdMerge(args)
	case "split":
		err = cmdSplit(args)
	case "info":
		err = cmdInfo(args)
	case "metadata":
		if len(args) == 0 {
			usage()
			os.Exit(1)
		}
		sub, rest := args[0], args[1:]
		switch sub {
		case "get":
			err = cmdMetadataGet(rest)
		case "set":
			err = cmdMetadataSet(rest)
		default:
			usage()
			os.Exit(1)
		}
	case "images":
		err = cmdExtractImages(args)
	default:
		usage()
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", cmd, err)
		var ece exitCodeError
		if errors.As(err, &ece) {
			os.Exit(ece.code)
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `pdfcli — PDF operations via github.com/yudaprama/pdf

Usage:
  pdfcli extract [--md] [--json] [--pages N,N] <input.pdf>  Extract text (stdout; --md = Markdown)
  pdfcli search <pattern> [--pages N,N] <input.pdf>         Search text (JSON stdout)
  pdfcli replace <pattern> <replacement> [--pages N,N]      Replace text
      [--in-place] <input.pdf> [<output.pdf>]
  pdfcli merge [--json] <input.pdf>... <output.pdf>         Merge PDFs
  pdfcli split [--ranges R,R] <input.pdf> <outdir/>         Split PDF
  pdfcli info <input.pdf>                                   Page info (JSON stdout)
  pdfcli metadata get <input.pdf>                           Read metadata (JSON stdout)
  pdfcli metadata set [--json] <input.pdf> <output.pdf>     Write metadata
      --title "" --author "" --subject "" --keywords "" --creator "" --producer ""
  pdfcli images [--pages N,N] [--format png|jpg]            Extract images to files
      <input.pdf> <outdir/>

Any <input.pdf> may be '-' to read the PDF bytes from stdin.

Flags:
  --pages N,N  Page selection: "1,3,5" or "1-3,5" or "*" (all)
  --ranges R,R Split ranges: "1-2,3,4-5"
  --format png|jpg  Image output format (default png)
  --in-place   replace: atomically overwrite <input.pdf> (temp file + rename)
  --json       Machine-readable JSON output (extract, merge, replace, metadata set)
`)
}

// --- helpers ----------------------------------------------------------------

func readPDF(path string) (*model.PdfReader, readSeekCloser, error) {
	f, err := openInput(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	r, err := model.NewPdfReader(f)
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	return r, f, nil
}

func readPDFBytes(data []byte) (*model.PdfReader, error) {
	return model.NewPdfReader(bytes.NewReader(data))
}

// readSeekCloser is satisfied by *os.File and stdinReader.
type readSeekCloser interface {
	io.ReadSeeker
	io.Closer
}

type stdinReader struct {
	*bytes.Reader
}

func (stdinReader) Close() error { return nil }

func openInput(path string) (readSeekCloser, error) {
	if path == "-" {
		data, err := readStdin()
		if err != nil {
			return nil, err
		}
		return stdinReader{bytes.NewReader(data)}, nil
	}
	return os.Open(path)
}

type pageSpec struct {
	pages []int // nil = all
}

func parsePageSpec(s string, max int) ([]int, error) {
	s = strings.TrimSpace(s)

	// If flag not set, default to all pages via nil sentinel
	if s == "" || s == "*" {
		return nil, nil
	}

	parts := strings.Split(s, ",")
	seen := map[int]struct{}{}

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			sp := strings.SplitN(part, "-", 2)
			if len(sp) != 2 {
				return nil, fmt.Errorf("invalid range: %s", part)
			}
			start, err := strconv.Atoi(strings.TrimSpace(sp[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid range start %q: %w", sp[0], err)
			}
			end, err := strconv.Atoi(strings.TrimSpace(sp[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid range end %q: %w", sp[1], err)
			}
			if start < 1 || end > max || start > end {
				return nil, fmt.Errorf("invalid range: %s", part)
			}
			for i := start; i <= end; i++ {
				if _, ok := seen[i]; !ok {
					seen[i] = struct{}{}
				}
			}
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid page %q: %w", part, err)
		}
		if n < 1 || n > max {
			return nil, fmt.Errorf("page %d out of range (1..%d)", n, max)
		}
		if _, ok := seen[n]; !ok {
			seen[n] = struct{}{}
		}
	}

	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	slices.Sort(out)
	return out, nil
}

func resolvePages(reader *model.PdfReader, spec []int) ([]int, error) {
	if spec != nil {
		return spec, nil
	}
	n, err := reader.GetNumPages()
	if err != nil {
		return nil, err
	}
	out := make([]int, n)
	for i := 1; i <= n; i++ {
		out[i-1] = i
	}
	return out, nil
}

// --- extract ----------------------------------------------------------------

func cmdExtract(args []string) error {
	pagesSpec := ""
	markdown := false
	jsonOutput := false
	input := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pages":
			i++
			if i >= len(args) {
				return errors.New("--pages requires a value")
			}
			pagesSpec = args[i]
		case "--md":
			markdown = true
		case "--json":
			jsonOutput = true
		default:
			if input == "" {
				input = args[i]
			}
		}
	}
	if input == "" {
		return errors.New("usage: pdfcli extract [--md] [--json] [--pages N,N] <input.pdf>")
	}

	reader, f, err := readPDF(input)
	if err != nil {
		return err
	}
	defer f.Close()

	n, err := reader.GetNumPages()
	if err != nil {
		return err
	}
	pages, err := parsePageSpec(pagesSpec, n)
	if err != nil {
		return err
	}
	pages, err = resolvePages(reader, pages)
	if err != nil {
		return err
	}

	type pageResult struct {
		Page int    `json:"page"`
		Text string `json:"text"`
	}
	var results []pageResult

	for _, p := range pages {
		page, err := reader.GetPage(p)
		if err != nil {
			return fmt.Errorf("page %d: %w", p, err)
		}
		ex, err := extractor.New(page)
		if err != nil {
			return fmt.Errorf("page %d: %w", p, err)
		}

		var text string
		if markdown {
			pt, _, _, err := ex.ExtractPageText()
			if err != nil {
				return fmt.Errorf("page %d: %w", p, err)
			}
			text = pt.Markdown()
		} else {
			text, err = ex.ExtractText()
			if err != nil {
				return fmt.Errorf("page %d: %w", p, err)
			}
		}

		if jsonOutput {
			results = append(results, pageResult{Page: p, Text: text})
			continue
		}
		if markdown {
			fmt.Printf("--- page %d ---\n%s", p, text)
		} else {
			fmt.Printf("--- page %d ---\n%s\n", p, text)
		}
	}

	if jsonOutput {
		out, _ := json.Marshal(results)
		if len(results) == 0 {
			out = []byte("[]")
		}
		fmt.Println(string(out))
	}
	return nil
}

// --- search -----------------------------------------------------------------

type searchMatch struct {
	Page    int     `json:"page"`
	Matches [][]int `json:"matches"`
}

func cmdSearch(args []string) error {
	pattern := ""
	pagesSpec := ""
	input := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pages":
			i++
			if i >= len(args) {
				return errors.New("--pages requires a value")
			}
			pagesSpec = args[i]
		default:
			if pattern == "" {
				pattern = args[i]
			} else if input == "" {
				input = args[i]
			}
		}
	}
	if pattern == "" || input == "" {
		return errors.New("usage: pdfcli search <pattern> [--pages N,N] <input.pdf>")
	}

	reader, f, err := readPDF(input)
	if err != nil {
		return err
	}
	defer f.Close()

	n, err := reader.GetNumPages()
	if err != nil {
		return err
	}
	ps, err := parsePageSpec(pagesSpec, n)
	if err != nil {
		return err
	}

	editor := extractor.NewEditor(reader)
	matches, err := editor.Search(pattern, ps)
	if err != nil {
		return err
	}

	var result []searchMatch
	for page, m := range matches {
		result = append(result, searchMatch{
			Page:    page,
			Matches: m.Indexes,
		})
	}
	slices.SortFunc(result, func(a, b searchMatch) int { return a.Page - b.Page })

	out, _ := json.Marshal(result)
	fmt.Println(string(out))
	return nil
}

// --- replace ----------------------------------------------------------------

func cmdReplace(args []string) error {
	pattern := ""
	replacement := ""
	pagesSpec := ""
	inPlace := false
	jsonOutput := false
	input := ""
	output := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pages":
			i++
			if i >= len(args) {
				return errors.New("--pages requires a value")
			}
			pagesSpec = args[i]
		case "--in-place":
			inPlace = true
		case "--json":
			jsonOutput = true
		default:
			if pattern == "" {
				pattern = args[i]
			} else if replacement == "" {
				replacement = args[i]
			} else if input == "" {
				input = args[i]
			} else if output == "" {
				output = args[i]
			}
		}
	}
	if pattern == "" || input == "" {
		return errors.New("usage: pdfcli replace <pattern> <replacement> [--pages N,N] [--in-place] <input.pdf> [<output.pdf>]")
	}
	if inPlace && input == "-" {
		return errors.New("--in-place cannot be used with stdin input ('-')")
	}
	if inPlace {
		output = input
	} else if output == "" {
		return errors.New("missing output: pass <output.pdf> or --in-place")
	}

	reader, f, err := readPDF(input)
	if err != nil {
		return err
	}
	defer f.Close()

	n, err := reader.GetNumPages()
	if err != nil {
		return err
	}
	ps, err := parsePageSpec(pagesSpec, n)
	if err != nil {
		return err
	}

	editor := extractor.NewEditor(reader)
	report, err := editor.ReplaceWithReport(pattern, replacement, ps)
	if err != nil {
		return err
	}

	if inPlace {
		tmp, err := os.CreateTemp(filepath.Dir(input), ".pdfcli-replace-*.pdf")
		if err != nil {
			return fmt.Errorf("create temp: %w", err)
		}
		tmpName := tmp.Name()
		_ = tmp.Close()
		if err := editor.WriteToFile(tmpName); err != nil {
			_ = os.Remove(tmpName)
			return err
		}
		if err := os.Rename(tmpName, input); err != nil {
			_ = os.Remove(tmpName)
			return err
		}
	} else if err := editor.WriteToFile(output); err != nil {
		return err
	}

	if report.TotalMatched() == 0 {
		err = exitCodeError{code: 2, msg: fmt.Sprintf("no matches found for %q", pattern)}
	}

	if jsonOutput {
		out, _ := json.Marshal(map[string]any{
			"pattern":     pattern,
			"replacement": replacement,
			"matched":     report.TotalMatched(),
			"replaced":    report.TotalReplaced(),
			"pages":       report.Pages,
			"output":      output,
		})
		fmt.Println(string(out))
	} else {
		fmt.Fprintf(os.Stderr, "replaced %q → %q (%d matched / %d replaced) → %s\n",
			pattern, replacement, report.TotalMatched(), report.TotalReplaced(), output)
	}
	return err
}

// --- merge ------------------------------------------------------------------

func cmdMerge(args []string) error {
	jsonOutput := false
	if len(args) > 0 && args[0] == "--json" {
		jsonOutput = true
		args = args[1:]
	}
	if len(args) < 2 {
		return errors.New("usage: pdfcli merge [--json] <input.pdf>... <output.pdf>")
	}
	output := args[len(args)-1]
	inputs := args[:len(args)-1]
	if len(inputs) < 1 {
		return errors.New("at least one input is required")
	}

	model.SetPdfTitle("")
	model.SetPdfAuthor("")
	model.SetPdfSubject("")
	model.SetPdfKeywords("")
	model.SetPdfCreator("")
	model.SetPdfProducer("")

	writer := model.NewPdfWriter()
	totalPages := 0
	for _, path := range inputs {
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		reader, err := model.NewPdfReader(f)
		if err != nil {
			f.Close()
			return fmt.Errorf("read %s: %w", path, err)
		}
		n, err := reader.GetNumPages()
		if err != nil {
			f.Close()
			return fmt.Errorf("pages %s: %w", path, err)
		}
		for i := 1; i <= n; i++ {
			page, err := reader.GetPage(i)
			if err != nil {
				f.Close()
				return fmt.Errorf("page %d of %s: %w", i, path, err)
			}
			if err := writer.AddPage(page); err != nil {
				f.Close()
				return fmt.Errorf("add page %d of %s: %w", i, path, err)
			}
			totalPages++
		}
		f.Close()
	}
	outFile, err := os.Create(output)
	if err != nil {
		return fmt.Errorf("create %s: %w", output, err)
	}
	defer outFile.Close()
	if err := writer.Write(outFile); err != nil {
		return fmt.Errorf("write %s: %w", output, err)
	}
	if jsonOutput {
		out, _ := json.Marshal(map[string]any{
			"output":    output,
			"fileCount": len(inputs),
			"pageCount": totalPages,
		})
		fmt.Println(string(out))
	} else {
		fmt.Fprintf(os.Stderr, "merged %d file(s) → %s\n", len(inputs), output)
	}
	return nil
}

// --- split ------------------------------------------------------------------

func cmdSplit(args []string) error {
	rangesSpec := ""
	input := ""
	outDir := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--ranges":
			i++
			if i >= len(args) {
				return errors.New("--ranges requires a value")
			}
			rangesSpec = args[i]
		default:
			if input == "" {
				input = args[i]
			} else if outDir == "" {
				outDir = args[i]
			}
		}
	}
	if input == "" || outDir == "" {
		return errors.New("usage: pdfcli split [--ranges R,R] <input.pdf> <outdir/>")
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}

	reader, f, err := readPDF(input)
	if err != nil {
		return err
	}
	defer f.Close()

	n, err := reader.GetNumPages()
	if err != nil {
		return err
	}

	var groups [][]int
	if rangesSpec == "" {
		for i := 1; i <= n; i++ {
			groups = append(groups, []int{i})
		}
	} else {
		parts := strings.Split(rangesSpec, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if strings.Contains(part, "-") {
				sp := strings.SplitN(part, "-", 2)
				start, _ := strconv.Atoi(strings.TrimSpace(sp[0]))
				end, _ := strconv.Atoi(strings.TrimSpace(sp[1]))
				var g []int
				for i := start; i <= end; i++ {
					g = append(g, i)
				}
				groups = append(groups, g)
			} else {
				p, _ := strconv.Atoi(part)
				groups = append(groups, []int{p})
			}
		}
	}

	model.SetPdfTitle("")
	model.SetPdfAuthor("")
	model.SetPdfSubject("")
	model.SetPdfKeywords("")
	model.SetPdfCreator("")
	model.SetPdfProducer("")

	var outputs []string
	for idx, group := range groups {
		outPath := filepath.ToSlash(filepath.Join(outDir, fmt.Sprintf("part_%03d.pdf", idx+1)))
		writer := model.NewPdfWriter()
		for _, p := range group {
			page, err := reader.GetPage(p)
			if err != nil {
				return fmt.Errorf("page %d: %w", p, err)
			}
			if err := writer.AddPage(page); err != nil {
				return fmt.Errorf("add page %d: %w", p, err)
			}
		}
		outFile, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("create %s: %w", outPath, err)
		}
		if err := writer.Write(outFile); err != nil {
			outFile.Close()
			return fmt.Errorf("write %s: %w", outPath, err)
		}
		outFile.Close()
		outputs = append(outputs, outPath)
	}
	b, _ := json.Marshal(outputs)
	fmt.Println(string(b))
	return nil
}

// --- info -------------------------------------------------------------------

type pageInfo struct {
	Page        int        `json:"page"`
	Width       float64    `json:"width,omitempty"`
	Height      float64    `json:"height,omitempty"`
	Rotation    int64      `json:"rotation,omitempty"`
	MediaBox    [4]float64 `json:"mediaBox,omitempty"`
	HasMediaBox bool       `json:"hasMediaBox"`
}

type infoResult struct {
	InputPath string     `json:"inputPath"`
	PageCount int        `json:"pageCount"`
	Pages     []pageInfo `json:"pages"`
}

func cmdInfo(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: pdfcli info <input.pdf>")
	}
	input := args[0]

	reader, f, err := readPDF(input)
	if err != nil {
		return err
	}
	defer f.Close()

	n, err := reader.GetNumPages()
	if err != nil {
		return err
	}

	r := infoResult{InputPath: input, PageCount: n}
	for i := 1; i <= n; i++ {
		page, err := reader.GetPage(i)
		if err != nil {
			return fmt.Errorf("page %d: %w", i, err)
		}
		pi := pageInfo{Page: i}
		if page.Rotate != nil {
			pi.Rotation = *page.Rotate
		}
		if mb, err := page.GetMediaBox(); err == nil && mb != nil {
			pi.Width = mb.Width()
			pi.Height = mb.Height()
			pi.MediaBox = [4]float64{mb.Llx, mb.Lly, mb.Urx, mb.Ury}
			pi.HasMediaBox = true
		}
		r.Pages = append(r.Pages, pi)
	}

	out, _ := json.Marshal(r)
	fmt.Println(string(out))
	return nil
}

// --- metadata get -----------------------------------------------------------

func cmdMetadataGet(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: pdfcli metadata get <input.pdf>")
	}
	input := args[0]

	reader, f, err := readPDF(input)
	if err != nil {
		return err
	}
	defer f.Close()

	trailer, err := reader.GetTrailer()
	if err != nil {
		return err
	}

	meta := map[string]string{}
	infoObj := trailer.Get("Info")
	if infoObj != nil {
		if infoDict, ok := core.GetDict(infoObj); ok {
			for _, key := range infoDict.Keys() {
				meta[string(key)] = pdfObjectToString(infoDict.Get(key))
			}
		}
	}
	out, _ := json.Marshal(meta)
	fmt.Println(string(out))
	return nil
}

func pdfObjectToString(obj core.PdfObject) string {
	if obj == nil {
		return ""
	}
	switch v := core.TraceToDirectObject(obj).(type) {
	case *core.PdfObjectString:
		return v.Decoded()
	case *core.PdfObjectName:
		return string(*v)
	case *core.PdfObjectInteger:
		return strconv.FormatInt(int64(*v), 10)
	case *core.PdfObjectFloat:
		return strconv.FormatFloat(float64(*v), 'f', -1, 64)
	case *core.PdfObjectBool:
		return fmt.Sprint(bool(*v))
	default:
		return core.TraceToDirectObject(obj).String()
	}
}

// --- metadata set -----------------------------------------------------------

func cmdMetadataSet(args []string) error {
	input := ""
	output := ""
	jsonOutput := false
	meta := map[string]string{}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--title":
			i++
			if i < len(args) {
				meta["Title"] = args[i]
			}
		case "--author":
			i++
			if i < len(args) {
				meta["Author"] = args[i]
			}
		case "--subject":
			i++
			if i < len(args) {
				meta["Subject"] = args[i]
			}
		case "--keywords":
			i++
			if i < len(args) {
				meta["Keywords"] = args[i]
			}
		case "--creator":
			i++
			if i < len(args) {
				meta["Creator"] = args[i]
			}
		case "--producer":
			i++
			if i < len(args) {
				meta["Producer"] = args[i]
			}
		default:
			if input == "" {
				input = args[i]
			} else if output == "" {
				output = args[i]
			}
		}
	}
	if input == "" || output == "" {
		return errors.New("usage: pdfcli metadata set <input.pdf> <output.pdf> --title \"\" --author \"\" ...")
	}

	if v, ok := meta["Title"]; ok {
		model.SetPdfTitle(v)
	}
	if v, ok := meta["Author"]; ok {
		model.SetPdfAuthor(v)
	}
	if v, ok := meta["Subject"]; ok {
		model.SetPdfSubject(v)
	}
	if v, ok := meta["Keywords"]; ok {
		model.SetPdfKeywords(v)
	}
	if v, ok := meta["Creator"]; ok {
		model.SetPdfCreator(v)
	}
	if v, ok := meta["Producer"]; ok {
		model.SetPdfProducer(v)
	}

	reader, f, err := readPDF(input)
	if err != nil {
		return err
	}
	defer f.Close()

	writer := model.NewPdfWriter()
	n, err := reader.GetNumPages()
	if err != nil {
		return err
	}
	for i := 1; i <= n; i++ {
		page, err := reader.GetPage(i)
		if err != nil {
			return fmt.Errorf("page %d: %w", i, err)
		}
		if err := writer.AddPage(page); err != nil {
			return fmt.Errorf("add page %d: %w", i, err)
		}
	}
	outFile, err := os.Create(output)
	if err != nil {
		return fmt.Errorf("create %s: %w", output, err)
	}
	defer outFile.Close()
	if err := writer.Write(outFile); err != nil {
		return fmt.Errorf("write %s: %w", output, err)
	}
	if jsonOutput {
		out, _ := json.Marshal(map[string]any{
			"output":   output,
			"metadata": meta,
		})
		fmt.Println(string(out))
	} else {
		fmt.Fprintf(os.Stderr, "metadata written → %s\n", output)
	}
	return nil
}

// --- images -----------------------------------------------------------------

func cmdExtractImages(args []string) error {
	pagesSpec := ""
	format := "png"
	input := ""
	outDir := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pages":
			i++
			if i >= len(args) {
				return errors.New("--pages requires a value")
			}
			pagesSpec = args[i]
		case "--format":
			i++
			if i >= len(args) {
				return errors.New("--format requires a value")
			}
			format = args[i]
		default:
			if input == "" {
				input = args[i]
			} else if outDir == "" {
				outDir = args[i]
			}
		}
	}
	if input == "" || outDir == "" {
		return errors.New("usage: pdfcli images [--pages N,N] [--format png|jpg] <input.pdf> <outdir/>")
	}
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}

	format = strings.ToLower(format)
	switch format {
	case "png":
	case "jpg", "jpeg":
		format = "jpg"
	default:
		return fmt.Errorf("unsupported format %q (use png or jpg)", format)
	}

	ext := format

	reader, f, err := readPDF(input)
	if err != nil {
		return err
	}
	defer f.Close()

	n, err := reader.GetNumPages()
	if err != nil {
		return err
	}
	ps, err := parsePageSpec(pagesSpec, n)
	if err != nil {
		return err
	}
	ps, err = resolvePages(reader, ps)
	if err != nil {
		return err
	}

	type extracted struct {
		Page   int     `json:"page"`
		Index  int     `json:"index"`
		Path   string  `json:"path"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Angle  float64 `json:"angle"`
	}
	var all []extracted = []extracted{}

	for _, pageNum := range ps {
		page, err := reader.GetPage(pageNum)
		if err != nil {
			return fmt.Errorf("page %d: %w", pageNum, err)
		}
		ex, err := extractor.New(page)
		if err != nil {
			return fmt.Errorf("page %d: %w", pageNum, err)
		}
		pageImages, err := ex.ExtractPageImages(nil)
		if err != nil {
			return fmt.Errorf("page %d images: %w", pageNum, err)
		}
		for i, mark := range pageImages.Images {
			if mark.Image == nil {
				continue
			}
			goimg, err := mark.Image.ToGoImage()
			if err != nil {
				return fmt.Errorf("decode image page %d idx %d: %w", pageNum, i+1, err)
			}
			outPath := filepath.ToSlash(filepath.Join(outDir,
				fmt.Sprintf("page_%03d_img_%03d.%s", pageNum, i+1, ext)))
			outFile, err := os.Create(outPath)
			if err != nil {
				return fmt.Errorf("create %s: %w", outPath, err)
			}
			switch format {
			case "png":
				err = png.Encode(outFile, goimg)
			case "jpg":
				err = jpeg.Encode(outFile, goimg, &jpeg.Options{Quality: 90})
			}
			closeErr := outFile.Close()
			if err != nil {
				return fmt.Errorf("encode %s: %w", outPath, err)
			}
			if closeErr != nil {
				return fmt.Errorf("close %s: %w", outPath, closeErr)
			}
			all = append(all, extracted{
				Page: pageNum, Index: i + 1, Path: outPath,
				Width: mark.Width, Height: mark.Height,
				X: mark.X, Y: mark.Y, Angle: mark.Angle,
			})
		}
	}

	b, _ := json.Marshal(all)
	fmt.Println(string(b))
	return nil
}

// --- stdin pipe support -----------------------------------------------------

func readStdin() ([]byte, error) {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return nil, err
	}
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return nil, errors.New("stdin is not a pipe")
	}
	return io.ReadAll(os.Stdin)
}
