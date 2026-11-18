package edits

import (
	"bytes"
	"fmt"
	"strings"
)

// FileDiff is one file in a patch. A missing side is a new or deleted file.
type FileDiff struct {
	Path      string
	Old, New  string
	OldExists bool
	NewExists bool
}

// patchContext is the number of unchanged lines around each hunk.
const patchContext = 3

// Patch writes a git-style unified diff with a/ and b/ prefixes, so
// "git apply" and "patch -p1" work from the directory the paths are
// relative to. strip is removed from the front of every path ("proj/"
// for a single uploaded folder). Files that did not change are skipped.
func Patch(files []FileDiff, strip string) []byte {
	var buf bytes.Buffer
	for _, f := range files {
		if f.OldExists == f.NewExists && f.Old == f.New {
			continue
		}
		if !f.OldExists && !f.NewExists {
			continue
		}
		name := strings.TrimPrefix(f.Path, strip)
		a, b := quotePath("a/"+name), quotePath("b/"+name)
		fmt.Fprintf(&buf, "diff --git %s %s\n", a, b)
		old, new := f.Old, f.New
		switch {
		case !f.OldExists:
			buf.WriteString("new file mode 100644\n")
			a, old = "/dev/null", ""
		case !f.NewExists:
			buf.WriteString("deleted file mode 100644\n")
			b, new = "/dev/null", ""
		}
		hunks := Hunks(old, new, patchContext)
		if len(hunks) == 0 {
			// An empty new or deleted file has a header and no hunks.
			continue
		}
		fmt.Fprintf(&buf, "--- %s\n+++ %s\n", a, b)
		for _, h := range hunks {
			buf.WriteString(h.Header())
			buf.WriteByte('\n')
			for i, line := range h.Lines {
				buf.WriteString(line)
				buf.WriteByte('\n')
				if h.NoEOL[i] {
					buf.WriteString("\\ No newline at end of file\n")
				}
			}
		}
	}
	return buf.Bytes()
}

// quotePath C-quotes a path the way git does when it holds a quote, a
// backslash or a control character.
func quotePath(p string) string {
	need := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '"' || c == '\\' || c < 0x20 || c == 0x7f {
			need = true
			break
		}
	}
	if !need {
		return p
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 || c == 0x7f {
				fmt.Fprintf(&b, `\%03o`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
