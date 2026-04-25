// pdf_tools.go — pdf_read tool handler.
//
// T-R1P-023: PDF parse tool — extracts text from a local PDF file.
//
// Strategy (no external dependencies):
//  1. Try pdftotext (poppler-utils) — best quality, widely available on Linux/macOS.
//  2. Try mutool (MuPDF) — another common system tool.
//  3. Fall back to raw PDF stream extraction: scan for BT...ET blocks and extract
//     text strings from Tj/TJ operators. Conservative last resort; misses some
//     encodings but works for most simple PDFs without any non-stdlib dependency.
//
// Output is capped at 100KB.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// maxPDFOutput is the max bytes returned from pdf_read.
const maxPDFOutput = 100 * 1024

// handlePDFRead implements the pdf_read tool (T-R1P-023).
func (r *Registry) handlePDFRead(input json.RawMessage) (string, error) {
	var args struct {
		Path  string `json:"path"`
		Pages string `json:"pages"` // optional: "1-5" or "3" for page range
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	path, pathErr := r.resolvePath(args.Path)
	if pathErr != nil {
		return "", pathErr
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("pdf_read: cannot read %s: %w", args.Path, err)
	}

	// Verify PDF header.
	if !bytes.HasPrefix(data, []byte("%PDF")) {
		return "", fmt.Errorf("pdf_read: %s does not appear to be a PDF (missing %%PDF header)", args.Path)
	}

	text := extractPDFText(path, data, args.Pages)
	if len(text) > maxPDFOutput {
		text = text[:maxPDFOutput] + fmt.Sprintf("\n\n[truncated at %d bytes]", maxPDFOutput)
	}
	return text, nil
}

// extractPDFText tries system PDF tools in order of quality, falling back
// to a pure-Go stream extractor.
func extractPDFText(path string, data []byte, pages string) string {
	ctx := context.Background()
	if text, ok := runPDFToText(ctx, path, pages); ok {
		return text
	}
	if text, ok := runMuTool(ctx, path, pages); ok {
		return text
	}
	return extractPDFStreams(data)
}

// runPDFToText runs pdftotext from poppler-utils.
func runPDFToText(ctx context.Context, path, pages string) (string, bool) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		return "", false
	}
	args := []string{"-layout"}
	if pages != "" {
		if f, l, ok := parsePageRange(pages); ok {
			args = append(args, "-f", strconv.Itoa(f), "-l", strconv.Itoa(l))
		}
	}
	args = append(args, path, "-") // "-" = stdout
	out, err := exec.CommandContext(ctx, "pdftotext", args...).Output() // #nosec G204 -- system binary; path is resolver-confined
	if err != nil {
		return "", false
	}
	return string(out), true
}

// runMuTool runs mutool (MuPDF).
func runMuTool(ctx context.Context, path, pages string) (string, bool) {
	if _, err := exec.LookPath("mutool"); err != nil {
		return "", false
	}
	args := []string{"draw", "-F", "text", "-o", "-"}
	if pages != "" {
		args = append(args, path, pages)
	} else {
		args = append(args, path)
	}
	out, err := exec.CommandContext(ctx, "mutool", args...).Output() // #nosec G204 -- system binary
	if err != nil {
		return "", false
	}
	return string(out), true
}

// extractPDFStreams is a pure-Go last-resort extractor. It scans for
// PDF text-showing operators (Tj, TJ, \') inside content streams.
// Quality is lower than system tools: no encoding remapping, no
// ligature expansion. Works for most ASCII-range PDFs.
func extractPDFStreams(data []byte) string {
	content := string(data)

	// Match BT...ET blocks (text objects in PDF).
	btET := regexp.MustCompile(`(?s)BT(.*?)ET`)
	// Match string literals in parentheses: (some text)
	parenStr := regexp.MustCompile(`\(([^)\\]|\\.)*\)`)
	// Match hex strings: <4865>
	hexStr := regexp.MustCompile(`<([0-9A-Fa-f\s]+)>`)
	// Text showing operators immediately after a closing paren.
	showOp := regexp.MustCompile(`^\s*Tj|\s*'|\s*"`)
	tjArr := regexp.MustCompile(`\[([^\]]*)\]\s*TJ`)

	var sb strings.Builder
	for _, block := range btET.FindAllString(content, -1) {
		// Extract simple Tj / ' strings.
		parenMatches := parenStr.FindAllStringIndex(block, -1)
		for _, loc := range parenMatches {
			p := block[loc[0]:loc[1]]
			after := block[loc[1]:]
			cap := 10
			if len(after) < cap {
				cap = len(after)
			}
			if showOp.MatchString(after[:cap]) {
				inner := decodePDFString(p[1 : len(p)-1])
				sb.WriteString(inner)
				sb.WriteByte(' ')
			}
		}
		// Extract TJ arrays (space-separated strings + kerning numbers).
		for _, m := range tjArr.FindAllStringSubmatch(block, -1) {
			arr := m[1]
			for _, s := range parenStr.FindAllString(arr, -1) {
				sb.WriteString(decodePDFString(s[1 : len(s)-1]))
			}
			for _, h := range hexStr.FindAllStringSubmatch(arr, -1) {
				if decoded, ok := decodeHexPDFString(h[1]); ok {
					sb.WriteString(decoded)
				}
			}
			sb.WriteByte(' ')
		}
		sb.WriteByte('\n')
	}

	text := sb.String()
	if strings.TrimSpace(text) == "" {
		return "(pdf_read: no extractable text found — PDF may use custom encoding or compressed streams; install pdftotext or mutool for better extraction)"
	}
	return text
}

// decodePDFString handles PDF escape sequences in parenthesized strings.
func decodePDFString(s string) string {
	var sb strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case '(':
				sb.WriteByte('(')
			case ')':
				sb.WriteByte(')')
			case '\\':
				sb.WriteByte('\\')
			default:
				sb.WriteByte(s[i+1])
			}
			i += 2
			continue
		}
		sb.WriteByte(s[i])
		i++
	}
	return sb.String()
}

// decodeHexPDFString decodes a PDF hex string (e.g. "48656c6c6f") to ASCII text.
func decodeHexPDFString(hex string) (string, bool) {
	hex = strings.ReplaceAll(hex, " ", "")
	hex = strings.ReplaceAll(hex, "\n", "")
	if len(hex)%2 != 0 {
		hex += "0"
	}
	var sb strings.Builder
	for i := 0; i+1 < len(hex); i += 2 {
		hi := hexNibble(hex[i])
		lo := hexNibble(hex[i+1])
		if hi < 0 || lo < 0 {
			return "", false
		}
		b := byte(hi<<4 | lo)
		if b >= 0x20 && b < 0x7f {
			sb.WriteByte(b)
		}
	}
	return sb.String(), true
}

func hexNibble(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// parsePageRange parses "1-5" or "3" into (first, last).
func parsePageRange(s string) (int, int, bool) {
	parts := strings.SplitN(s, "-", 2)
	f, ferr := parsePDFInt(parts[0])
	if ferr != nil {
		return 0, 0, false
	}
	if len(parts) == 1 {
		return f, f, true
	}
	l, lerr := parsePDFInt(parts[1])
	if lerr != nil {
		return 0, 0, false
	}
	return f, l, true
}

// parsePDFInt parses a non-negative integer string.
func parsePDFInt(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("not a positive integer: %s", s)
	}
	return n, nil
}
