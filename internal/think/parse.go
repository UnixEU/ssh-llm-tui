// Package think splits model output that hides chain-of-thought inside
// <think>...</think> tags (used by Qwen and similar models) from the visible
// answer. Tags may arrive split across stream chunks.
package think

import "strings"

const (
	openTag  = "<think>"
	closeTag = "</think>"
)

// Parser is the incremental splitter for one reply.
type Parser struct {
	inThink bool
	hold    string
	seen    bool // true after any reasoning was emitted
}

// InThink reports whether the parser is inside an unclosed think tag.
func (p *Parser) InThink() bool { return p != nil && p.inThink }

// Push consumes the next content chunk and returns the visible answer text
// and the reasoning text extracted from it. Neither includes the tags.
func (p *Parser) Push(chunk string) (content, reasoning string) {
	if p == nil || chunk == "" && p.hold == "" {
		return "", ""
	}
	p.hold += chunk
	var cbuf, rbuf strings.Builder
	for {
		if p.inThink {
			i := indexFold(p.hold, closeTag)
			if i < 0 {
				emit, keep := splitPartial(p.hold, closeTag)
				rbuf.WriteString(emit)
				p.hold = keep
				break
			}
			rbuf.WriteString(p.hold[:i])
			p.hold = p.hold[i+len(closeTag):]
			p.inThink = false
			continue
		}
		i := indexFold(p.hold, openTag)
		if i < 0 {
			emit, keep := splitPartial(p.hold, openTag)
			cbuf.WriteString(emit)
			p.hold = keep
			break
		}
		cbuf.WriteString(p.hold[:i])
		p.hold = p.hold[i+len(openTag):]
		if p.seen || rbuf.Len() > 0 {
			rbuf.WriteString("\n")
		}
		p.inThink = true
	}
	if rbuf.Len() > 0 {
		p.seen = true
	}
	return cbuf.String(), rbuf.String()
}

// Flush ends the stream. A trailing incomplete tag is ordinary text.
// Unclosed reasoning is returned as reasoning.
func (p *Parser) Flush() (content, reasoning string) {
	if p == nil {
		return "", ""
	}
	rest := p.hold
	p.hold = ""
	if rest == "" {
		return "", ""
	}
	if p.inThink {
		p.inThink = false
		return "", rest
	}
	return rest, ""
}

// indexFold returns the byte index of sub inside s, ignoring ASCII case.
func indexFold(s, sub string) int {
	if sub == "" || len(s) < len(sub) {
		return -1
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if strings.EqualFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// splitPartial returns the part of s that cannot be a prefix of tag, and the
// suffix that still might grow into tag.
func splitPartial(s, tag string) (emit, keep string) {
	max := len(tag) - 1
	if max > len(s) {
		max = len(s)
	}
	for n := max; n > 0; n-- {
		if strings.EqualFold(s[len(s)-n:], tag[:n]) {
			return s[:len(s)-n], s[len(s)-n:]
		}
	}
	return s, ""
}
