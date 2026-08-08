package document

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/ledongthuc/pdf"
)

const minUsefulPDFTextRunes = 80
const pdfExtractTimeout = 30 * time.Second

var hyphenLineBreakRe = regexp.MustCompile(`([A-Za-z])-\n([A-Za-z])`)

// ParseResult is the normalized document payload sent into the RAG pipeline.
type ParseResult struct {
	Filename    string
	ContentType string
	Parser      string
	Content     string
	Pages       int
	TextChars   int
	NeedsOCR    bool
}

// ParseBytes turns an uploaded file into normalized plain text.
func ParseBytes(filename, contentType string, data []byte) (ParseResult, error) {
	contentType = normalizeContentType(contentType)
	ext := strings.ToLower(filepath.Ext(filename))
	if contentType == "application/pdf" || ext == ".pdf" {
		return parsePDF(filename, contentType, data)
	}

	text := normalizeText(string(data))
	if strings.TrimSpace(text) == "" {
		return ParseResult{}, fmt.Errorf("uploaded document is empty")
	}
	return ParseResult{
		Filename:    filename,
		ContentType: contentType,
		Parser:      "plain_text",
		Content:     text,
		TextChars:   runeLen(text),
	}, nil
}

func parsePDF(filename, contentType string, data []byte) (ParseResult, error) {
	if text, pages, parser := extractPDFExternal(data); strings.TrimSpace(text) != "" {
		text = normalizeText(text)
		chars := runeLen(text)
		return ParseResult{
			Filename:    filename,
			ContentType: contentType,
			Parser:      parser,
			Content:     text,
			Pages:       pages,
			TextChars:   chars,
			NeedsOCR:    pages > 0 && chars < minUsefulPDFTextRunes,
		}, nil
	}

	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ParseResult{}, fmt.Errorf("parse pdf failed: %w", err)
	}

	pages := r.NumPage()
	var b strings.Builder
	fonts := make(map[string]*pdf.Font)
	for i := 1; i <= pages; i++ {
		p := r.Page(i)
		for _, name := range p.Fonts() {
			if _, ok := fonts[name]; !ok {
				f := p.Font(name)
				fonts[name] = &f
			}
		}
		pageText, err := pageTextByRows(p)
		if err != nil || strings.TrimSpace(pageText) == "" {
			pageText, err = p.GetPlainText(fonts)
			if err != nil || strings.TrimSpace(pageText) == "" {
				continue
			}
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "--- page %d ---\n", i)
		b.WriteString(pageText)
	}

	text := normalizeText(b.String())
	if text == "" {
		if plain, err := r.GetPlainText(); err == nil {
			raw, _ := io.ReadAll(plain)
			text = normalizeText(string(raw))
		}
	}

	chars := runeLen(text)
	needsOCR := pages > 0 && chars < minUsefulPDFTextRunes
	if chars == 0 {
		return ParseResult{
			Filename:    filename,
			ContentType: contentType,
			Parser:      "pdf_text",
			Pages:       pages,
			NeedsOCR:    true,
		}, fmt.Errorf("pdf contains no extractable text; OCR is required")
	}

	return ParseResult{
		Filename:    filename,
		ContentType: contentType,
		Parser:      "pdf_text",
		Content:     text,
		Pages:       pages,
		TextChars:   chars,
		NeedsOCR:    needsOCR,
	}, nil
}

func extractPDFExternal(data []byte) (string, int, string) {
	if text, pages, ok := extractPDFWithPDFPlumber(data); ok {
		return text, pages, "pdfplumber"
	}
	if text, ok := extractPDFWithPdftotext(data); ok {
		return text, 0, "pdftotext"
	}
	return "", 0, ""
}

func extractPDFWithPDFPlumber(data []byte) (string, int, bool) {
	path, cleanup, err := writeTempPDF(data)
	if err != nil {
		return "", 0, false
	}
	defer cleanup()

	script := `import json, sys
import pdfplumber

path = sys.argv[1]
texts = []
with pdfplumber.open(path) as pdf:
    pages = len(pdf.pages)
    for i, page in enumerate(pdf.pages, 1):
        text = page.extract_text(x_tolerance=1, y_tolerance=3) or ""
        if text.strip():
            texts.append(f"--- page {i} ---\n{text}")
print(json.dumps({"pages": pages, "text": "\n\n".join(texts)}, ensure_ascii=False))
`

	for _, python := range pythonCandidates() {
		ctx, cancel := context.WithTimeout(context.Background(), pdfExtractTimeout)
		cmd := exec.CommandContext(ctx, python, "-c", script, path)
		out, err := cmd.Output()
		cancel()
		if err != nil || len(out) == 0 {
			continue
		}
		var result struct {
			Pages int    `json:"pages"`
			Text  string `json:"text"`
		}
		if json.Unmarshal(out, &result) != nil {
			continue
		}
		if strings.TrimSpace(result.Text) != "" {
			return result.Text, result.Pages, true
		}
	}
	return "", 0, false
}

func extractPDFWithPdftotext(data []byte) (string, bool) {
	path, cleanup, err := writeTempPDF(data)
	if err != nil {
		return "", false
	}
	defer cleanup()

	exe, err := exec.LookPath("pdftotext")
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), pdfExtractTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "-layout", "-enc", "UTF-8", path, "-").Output()
	if err != nil || len(out) == 0 {
		return "", false
	}
	text := string(out)
	return text, strings.TrimSpace(text) != ""
}

func writeTempPDF(data []byte) (string, func(), error) {
	f, err := os.CreateTemp("", "agi-saber-*.pdf")
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func pythonCandidates() []string {
	var candidates []string
	for _, key := range []string{"PDF_EXTRACT_PYTHON", "PDF_PYTHON"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			candidates = append(candidates, v)
		}
	}
	if exe, err := exec.LookPath("python3"); err == nil {
		candidates = append(candidates, exe)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3"))
	}

	seen := make(map[string]struct{}, len(candidates))
	out := candidates[:0]
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

func pageTextByRows(p pdf.Page) (string, error) {
	rows, err := p.GetTextByRow()
	if err != nil {
		return "", err
	}
	var lines []string
	for _, row := range rows {
		var parts []string
		for _, t := range row.Content {
			part := strings.TrimSpace(t.S)
			if part != "" {
				parts = append(parts, part)
			}
		}
		if len(parts) > 0 {
			lines = append(lines, strings.Join(parts, " "))
		}
	}
	return strings.Join(lines, "\n"), nil
}

func normalizeContentType(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(contentType))
	}
	return strings.ToLower(mediaType)
}

func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\u00ad", "")
	s = hyphenLineBreakRe.ReplaceAllString(s, "$1$2")

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(collapseInlineSpace(line))
		if line == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
				blank = true
			}
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func collapseInlineSpace(s string) string {
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return b.String()
}

func runeLen(s string) int { return len([]rune(s)) }
