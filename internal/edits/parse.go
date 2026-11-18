// Package edits turns file changes proposed by a model into reviewed,
// per-user versions of uploaded files. Nothing is applied without the
// user: every parsed block becomes a pending change that can be accepted
// or rejected. Uploaded originals are never modified.
package edits

import (
	"fmt"
	"strings"
)

// Kind is what a block asks for.
type Kind int

const (
	// KindReplace replaces one exact passage of an existing file.
	KindReplace Kind = iota
	// KindWhole writes the complete content of a new or existing file.
	KindWhole
	// KindDelete removes a file.
	KindDelete
)

func (k Kind) String() string {
	switch k {
	case KindReplace:
		return "SEARCH/REPLACE"
	case KindWhole:
		return "WHOLE"
	case KindDelete:
		return "DELETE"
	}
	return "unknown"
}

// Block is one parsed edit. Search and Replace are lines without their
// line endings. Line is the 1-based line of the opening marker in the reply.
type Block struct {
	Path    string
	Kind    Kind
	Search  []string
	Replace []string
	Line    int
}

// ParseError is a block that could not be read from the reply.
type ParseError struct {
	Line   int
	Path   string
	Reason string
}

func (e ParseError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("line %d (%s): %s", e.Line, e.Path, e.Reason)
	}
	return fmt.Sprintf("line %d: %s", e.Line, e.Reason)
}

type marker int

const (
	markNone marker = iota
	markOpenSearch
	markOpenWhole
	markOpenDelete
	markDivider
	markCloseReplace
	markCloseWhole
	markCloseDelete
)

// classify recognises a marker line. Markers must stand alone on their
// line; 5 to 9 marker characters are accepted because models miscount.
func classify(line string) marker {
	s := strings.TrimSpace(line)
	if len(s) < 5 {
		return markNone
	}
	run := func(c byte) int {
		n := 0
		for n < len(s) && s[n] == c {
			n++
		}
		return n
	}
	switch s[0] {
	case '<':
		n := run('<')
		if n < 5 || n > 9 {
			return markNone
		}
		switch strings.ToUpper(strings.TrimSpace(s[n:])) {
		case "SEARCH":
			return markOpenSearch
		case "WHOLE":
			return markOpenWhole
		case "DELETE":
			return markOpenDelete
		}
	case '=':
		if n := run('='); n >= 5 && n <= 9 && n == len(s) {
			return markDivider
		}
	case '>':
		n := run('>')
		if n < 5 || n > 9 {
			return markNone
		}
		switch strings.ToUpper(strings.TrimSpace(s[n:])) {
		case "REPLACE":
			return markCloseReplace
		case "WHOLE":
			return markCloseWhole
		case "DELETE":
			return markCloseDelete
		}
	}
	return markNone
}

// filePath reads a "FILE: path" line. Markdown decoration that models add
// around it (headings, bold, backticks, quotes) is ignored.
func filePath(line string) (string, bool) {
	s := strings.TrimSpace(line)
	s = strings.TrimLeft(s, "#>*_` ")
	if len(s) < 5 || !strings.EqualFold(s[:5], "file:") {
		return "", false
	}
	p := strings.TrimSpace(s[5:])
	p = strings.Trim(p, "*_`\"' ")
	if p == "" {
		return "", false
	}
	return p, true
}

func isFence(line string) bool {
	s := strings.TrimSpace(line)
	return strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~")
}

// Parse reads edit blocks from the visible part of a reply. Reasoning
// must not be passed in. Blocks may sit inside markdown code fences.
// Several SEARCH/REPLACE blocks may follow one FILE line. Blocks that are
// not closed, for example because the reply was cut off, are reported as
// errors and not returned.
func Parse(content string) ([]Block, []ParseError) {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var blocks []Block
	var errs []ParseError
	current := ""
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if p, ok := filePath(line); ok {
			current = p
			continue
		}
		mk := classify(line)
		switch mk {
		case markNone:
			continue
		case markOpenSearch, markOpenWhole, markOpenDelete:
		default:
			errs = append(errs, ParseError{Line: i + 1, Path: current, Reason: "marker outside a block: " + strings.TrimSpace(line)})
			continue
		}
		start := i + 1
		if current == "" {
			errs = append(errs, ParseError{Line: start, Reason: "block without a FILE: line before it"})
		}
		b := Block{Path: current, Line: start}
		closed := false
		switch mk {
		case markOpenSearch:
			b.Kind = KindReplace
			inReplace := false
			for i++; i < len(lines); i++ {
				m := classify(lines[i])
				if m == markDivider && !inReplace {
					inReplace = true
					continue
				}
				if m == markCloseReplace && inReplace {
					closed = true
					break
				}
				if inReplace {
					b.Replace = append(b.Replace, lines[i])
				} else {
					b.Search = append(b.Search, lines[i])
				}
			}
			if !closed && !inReplace {
				errs = append(errs, ParseError{Line: start, Path: current, Reason: "SEARCH block without ======= and >>>>>>> REPLACE (reply cut off?)"})
				continue
			}
		case markOpenWhole:
			b.Kind = KindWhole
			for i++; i < len(lines); i++ {
				if classify(lines[i]) == markCloseWhole {
					closed = true
					break
				}
				b.Replace = append(b.Replace, lines[i])
			}
			b.Replace = unwrapFence(b.Replace)
		case markOpenDelete:
			b.Kind = KindDelete
			for i++; i < len(lines); i++ {
				if classify(lines[i]) == markCloseDelete {
					closed = true
					break
				}
				if strings.TrimSpace(lines[i]) != "" {
					break
				}
			}
		}
		if !closed {
			errs = append(errs, ParseError{Line: start, Path: current, Reason: b.Kind.String() + " block is not closed (reply cut off?)"})
			continue
		}
		if b.Path == "" {
			continue
		}
		blocks = append(blocks, b)
	}
	return blocks, errs
}

// unwrapFence drops one code fence that wraps the whole content of a WHOLE
// block, which models add out of habit. Fences inside the content stay.
func unwrapFence(lines []string) []string {
	first, last := -1, -1
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 || first == last || !isFence(lines[first]) || strings.TrimSpace(lines[last]) != strings.TrimSpace(lines[first])[:3] {
		return lines
	}
	return lines[first+1 : last]
}
