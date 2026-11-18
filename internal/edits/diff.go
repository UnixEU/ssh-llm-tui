package edits

import (
	"fmt"
	"strings"
)

type opKind byte

const (
	opEqual opKind = ' '
	opDel   opKind = '-'
	opIns   opKind = '+'
)

// op is one line of an edit script. a and b index the old and new lines.
type op struct {
	kind opKind
	a, b int
}

// maxEditDistance bounds the Myers search. Past it, the differing middle
// is reported as one delete and one insert, which is still a valid patch.
const maxEditDistance = 4000

// splitKeep splits s into lines that keep their "\n". The last line has
// none when s does not end with a line break.
func splitKeep(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

// diffLines returns a shortest edit script from a to b (Myers).
func diffLines(a, b []string) []op {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var out []op
	for i := 0; i < pre; i++ {
		out = append(out, op{opEqual, i, i})
	}
	out = append(out, myers(a[pre:len(a)-suf], b[pre:len(b)-suf], pre, pre)...)
	for i := 0; i < suf; i++ {
		out = append(out, op{opEqual, len(a) - suf + i, len(b) - suf + i})
	}
	return out
}

func myers(a, b []string, offA, offB int) []op {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		var out []op
		for i := 0; i < n; i++ {
			out = append(out, op{opDel, offA + i, offB})
		}
		for j := 0; j < m; j++ {
			out = append(out, op{opIns, offA + n, offB + j})
		}
		return out
	}
	max := n + m
	if max > maxEditDistance {
		max = maxEditDistance
	}
	// v[k+max] is the furthest x on diagonal k. trace keeps a copy per d,
	// trimmed to the diagonals reachable at that d.
	v := make([]int, 2*max+2)
	var trace [][]int
	found := -1
	for d := 0; d <= max; d++ {
		snap := make([]int, 2*d+1)
		for k := -d; k <= d; k++ {
			snap[k+d] = v[k+max]
		}
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[k-1+max] < v[k+1+max]) {
				x = v[k+1+max]
			} else {
				x = v[k-1+max] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[k+max] = x
			if x >= n && y >= m {
				found = d
				break
			}
		}
		if found >= 0 {
			break
		}
	}
	if found < 0 {
		var out []op
		for i := 0; i < n; i++ {
			out = append(out, op{opDel, offA + i, offB})
		}
		for j := 0; j < m; j++ {
			out = append(out, op{opIns, offA + n, offB + j})
		}
		return out
	}
	// Walk back through the snapshots. snap[d] holds v before step d.
	var rev []op
	x, y := n, m
	for d := found; d > 0; d-- {
		prev := trace[d] // v after step d-1, diagonals -d..d
		get := func(k int) int {
			if k < -d || k > d {
				return -1
			}
			return prev[k+d]
		}
		k := x - y
		var pk int
		if k == -d || (k != d && get(k-1) < get(k+1)) {
			pk = k + 1
		} else {
			pk = k - 1
		}
		px := get(pk)
		py := px - pk
		for x > px && y > py {
			x--
			y--
			rev = append(rev, op{opEqual, offA + x, offB + y})
		}
		if x == px {
			y--
			rev = append(rev, op{opIns, offA + x, offB + y})
		} else {
			x--
			rev = append(rev, op{opDel, offA + x, offB + y})
		}
	}
	for x > 0 && y > 0 {
		x--
		y--
		rev = append(rev, op{opEqual, offA + x, offB + y})
	}
	out := make([]op, len(rev))
	for i := range rev {
		out[i] = rev[len(rev)-1-i]
	}
	return out
}

// Stats counts added and removed lines between two texts.
func Stats(old, new string) (added, removed int) {
	for _, o := range diffLines(splitKeep(old), splitKeep(new)) {
		switch o.kind {
		case opIns:
			added++
		case opDel:
			removed++
		}
	}
	return added, removed
}

// Hunk is one block of a unified diff. Lines start with ' ', '-' or '+'
// and keep no line ending. NoEOL marks lines that had no final newline.
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Lines              []string
	NoEOL              []bool
}

// Header is the "@@ -a,b +c,d @@" line.
func (h Hunk) Header() string {
	return fmt.Sprintf("@@ -%s +%s @@", hunkRange(h.OldStart, h.OldLines), hunkRange(h.NewStart, h.NewLines))
}

func hunkRange(start, n int) string {
	if n == 1 {
		return fmt.Sprintf("%d", start)
	}
	return fmt.Sprintf("%d,%d", start, n)
}

// Hunks builds unified diff hunks with ctx lines of context.
func Hunks(old, new string, ctx int) []Hunk {
	a, b := splitKeep(old), splitKeep(new)
	ops := diffLines(a, b)
	var hunks []Hunk
	i := 0
	for i < len(ops) {
		if ops[i].kind == opEqual {
			i++
			continue
		}
		start := i - ctx
		if start < 0 {
			start = 0
		}
		end := i
		for end < len(ops) {
			if ops[end].kind != opEqual {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == opEqual {
				run++
			}
			if run == len(ops) || run-end > 2*ctx {
				end += ctx
				if end > len(ops) {
					end = len(ops)
				}
				break
			}
			end = run
		}
		h := Hunk{}
		first := ops[start]
		h.OldStart, h.NewStart = first.a+1, first.b+1
		for _, o := range ops[start:end] {
			var line string
			switch o.kind {
			case opEqual:
				line = a[o.a]
				h.OldLines++
				h.NewLines++
			case opDel:
				line = a[o.a]
				h.OldLines++
			case opIns:
				line = b[o.b]
				h.NewLines++
			}
			noEOL := !strings.HasSuffix(line, "\n")
			h.Lines = append(h.Lines, string(o.kind)+strings.TrimSuffix(line, "\n"))
			h.NoEOL = append(h.NoEOL, noEOL)
		}
		if h.OldLines == 0 {
			h.OldStart--
		}
		if h.NewLines == 0 {
			h.NewStart--
		}
		hunks = append(hunks, h)
		i = end
	}
	return hunks
}
