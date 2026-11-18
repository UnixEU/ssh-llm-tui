package uploads

import (
	"archive/zip"
	"bytes"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

var docxText = regexp.MustCompile(`(?s)<w:t[^>]*>(.*?)</w:t>`)

// Extract pulls prompt text out of an uploaded file. note explains a skip
// or an empty result. Scanned PDFs usually come back with an empty string
// and a note, because this extractor has no OCR.
func Extract(name string, data []byte) (text, note string) {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".pdf":
		text, err := extractPDF(data)
		if err != nil {
			return "", "pdf: " + err.Error()
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return "", "no extractable text (scanned PDF?)"
		}
		return text, ""
	case ".docx":
		text, err := extractDOCX(data)
		if err != nil {
			return "", "docx: " + err.Error()
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return "", "docx contained no text"
		}
		return text, ""
	default:
		if !looksText(data) {
			return "", "not UTF-8 text"
		}
		return string(data), ""
	}
}

func looksText(data []byte) bool {
	if len(data) == 0 {
		return true
	}
	sample := data
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return false
	}
	return utf8.Valid(data)
}

func extractPDF(data []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	plain, err := r.GetPlainText()
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(plain)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func extractDOCX(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var doc *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			doc = f
			break
		}
	}
	if doc == nil {
		return "", io.ErrUnexpectedEOF
	}
	rc, err := doc.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 8<<20))
	if err != nil {
		return "", err
	}
	parts := docxText.FindAllSubmatch(raw, -1)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(decodeXML(string(p[1])))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func decodeXML(s string) string {
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#10;", "\n")
	return s
}
