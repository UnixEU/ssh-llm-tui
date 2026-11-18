package edits

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNoMatch means the SEARCH text was not found in the file.
var ErrNoMatch = errors.New("SEARCH text not found")

// ErrAmbiguous means the SEARCH text was found more than once.
var ErrAmbiguous = errors.New("SEARCH text matches more than one place")

// MatchError explains a failed SEARCH with the lines of the file that
// looked closest, so the model can be asked to retry.
type MatchError struct {
	Err     error
	Count   int
	Near    []string
	NearAt  int // 1-based first line of Near
	Pattern []string
}

func (e *MatchError) Error() string {
	if errors.Is(e.Err, ErrAmbiguous) {
		return fmt.Sprintf("%v (%d places); add more surrounding lines", e.Err, e.Count)
	}
	if e.NearAt > 0 {
		return fmt.Sprintf("%v; the closest lines start at line %d", e.Err, e.NearAt)
	}
	return e.Err.Error()
}

func (e *MatchError) Unwrap() error { return e.Err }

// text is a file split into lines with its line-ending style kept aside.
// A file that mixes CRLF and LF keeps the "\r" on its lines, so lines an
// edit does not touch keep their ending.
type text struct {
	lines []string
	crlf  bool
	final bool // ends with a line break
}

func splitText(s string) text {
	n := strings.Count(s, "\n")
	t := text{crlf: n > 0 && strings.Count(s, "\r\n") == n}
	if t.crlf {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	if s == "" {
		return t
	}
	t.final = strings.HasSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\n")
	t.lines = strings.Split(s, "\n")
	return t
}

func (t text) String() string {
	if len(t.lines) == 0 {
		return ""
	}
	eol := "\n"
	if t.crlf {
		eol = "\r\n"
	}
	out := strings.Join(t.lines, eol)
	if t.final {
		out += eol
	}
	return out
}

// replaceLines applies one SEARCH/REPLACE to lines. Matching runs in order:
// exact lines, then ignoring trailing whitespace, then ignoring indentation
// (the replacement is re-indented to the file). Zero or several matches in
// the first pass that finds any is an error.
func replaceLines(lines, search, repl []string) ([]string, error) {
	search = trimCR(search)
	repl = trimCR(repl)
	if i, n, err := find(lines, search, exactEq); err == nil {
		return splice(lines, i, len(search), withCR(repl, lines[i])), nil
	} else if n > 1 {
		return nil, &MatchError{Err: ErrAmbiguous, Count: n, Pattern: search}
	}
	core := trimBlank(search)
	if len(core) == 0 {
		return nil, &MatchError{Err: ErrNoMatch, Pattern: search}
	}
	coreRepl := repl
	if len(core) != len(search) {
		coreRepl = trimBlank(repl)
	}
	if i, n, err := find(lines, core, rtrimEq); err == nil {
		return splice(lines, i, len(core), withCR(coreRepl, lines[i])), nil
	} else if n > 1 {
		return nil, &MatchError{Err: ErrAmbiguous, Count: n, Pattern: search}
	}
	if i, n, err := find(lines, core, indentEq); err == nil {
		return splice(lines, i, len(core), withCR(reindent(coreRepl, indentMap(core, lines[i:i+len(core)])), lines[i])), nil
	} else if n > 1 {
		return nil, &MatchError{Err: ErrAmbiguous, Count: n, Pattern: search}
	}
	near, at := closest(lines, core)
	return nil, &MatchError{Err: ErrNoMatch, Near: near, NearAt: at, Pattern: search}
}

func trimCR(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.TrimSuffix(s, "\r")
	}
	return out
}

func trimBlank(in []string) []string {
	start, end := 0, len(in)
	for start < end && strings.TrimSpace(in[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(in[end-1]) == "" {
		end--
	}
	return in[start:end]
}

// withCR gives replacement lines the "\r" of the matched line in a file
// with mixed line endings.
func withCR(repl []string, first string) []string {
	if !strings.HasSuffix(first, "\r") {
		return repl
	}
	out := make([]string, len(repl))
	for i, s := range repl {
		out[i] = s + "\r"
	}
	return out
}

func exactEq(a, b string) bool {
	return strings.TrimSuffix(a, "\r") == strings.TrimSuffix(b, "\r")
}

func rtrimEq(a, b string) bool {
	return strings.TrimRight(a, " \t\r") == strings.TrimRight(b, " \t\r")
}

func indentEq(a, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) }

// find returns the only position where pat matches, or the match count.
func find(lines, pat []string, eq func(a, b string) bool) (int, int, error) {
	if len(pat) == 0 || len(pat) > len(lines) {
		return -1, 0, ErrNoMatch
	}
	at, count := -1, 0
	for i := 0; i+len(pat) <= len(lines); i++ {
		ok := true
		for k := range pat {
			if !eq(lines[i+k], pat[k]) {
				ok = false
				break
			}
		}
		if ok {
			count++
			if at < 0 {
				at = i
			}
		}
	}
	switch count {
	case 0:
		return -1, 0, ErrNoMatch
	case 1:
		return at, 1, nil
	}
	return -1, count, ErrAmbiguous
}

func splice(lines []string, at, n int, repl []string) []string {
	out := make([]string, 0, len(lines)-n+len(repl))
	out = append(out, lines[:at]...)
	out = append(out, repl...)
	return append(out, lines[at+n:]...)
}

func leading(l string) string { return l[:len(l)-len(strings.TrimLeft(l, " \t"))] }

// indentMap pairs the indentation of each SEARCH line with the file line
// it matched.
func indentMap(search, file []string) map[string]string {
	m := map[string]string{}
	for k := range search {
		if strings.TrimSpace(search[k]) == "" {
			continue
		}
		from := leading(search[k])
		if _, ok := m[from]; !ok {
			m[from] = leading(file[k])
		}
	}
	return m
}

// reindent swaps the indentation of each replacement line using the
// longest SEARCH indentation it starts with.
func reindent(lines []string, m map[string]string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			out[i] = l
			continue
		}
		ind := leading(l)
		best, ok := "", false
		for from := range m {
			if strings.HasPrefix(ind, from) && (!ok || len(from) > len(best)) {
				best, ok = from, true
			}
		}
		if !ok {
			out[i] = l
			continue
		}
		out[i] = m[best] + l[len(best):]
	}
	return out
}

// closest finds the file lines that look most like the pattern, for the
// retry message. It anchors on the first pattern line that occurs in the
// file with indentation ignored.
func closest(lines, pat []string) ([]string, int) {
	if len(lines) == 0 {
		return nil, 0
	}
	window := len(pat) + 4
	if window > 40 {
		window = 40
	}
	for _, p := range pat {
		p = strings.TrimSpace(p)
		if len(p) < 4 {
			continue
		}
		for i, l := range lines {
			if strings.TrimSpace(l) == p || strings.Contains(l, p) {
				start := i - 2
				if start < 0 {
					start = 0
				}
				end := start + window
				if end > len(lines) {
					end = len(lines)
				}
				return append([]string(nil), lines[start:end]...), start + 1
			}
		}
	}
	// No line contains a pattern line: take the longest common prefix.
	best, at := 0, -1
	for _, p := range pat {
		p = strings.TrimSpace(p)
		for i, l := range lines {
			l = strings.TrimSpace(l)
			n := 0
			for n < len(p) && n < len(l) && p[n] == l[n] {
				n++
			}
			if n > best {
				best, at = n, i
			}
		}
	}
	if at < 0 || best < 4 {
		return nil, 0
	}
	start := at - 2
	if start < 0 {
		start = 0
	}
	end := start + window
	if end > len(lines) {
		end = len(lines)
	}
	return append([]string(nil), lines[start:end]...), start + 1
}
