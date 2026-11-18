package think

import (
	"strings"
	"testing"
)

func TestPushWholeMessage(t *testing.T) {
	var p Parser
	c, r := p.Push("Hello <think>secret</think> there")
	if c != "Hello  there" || r != "secret" {
		t.Fatalf("content %q reasoning %q", c, r)
	}
	if p.InThink() {
		t.Fatal("still in think")
	}
}

func TestPushSplitAcrossChunks(t *testing.T) {
	var p Parser
	var content, reasoning string
	chunks := []string{"Hi <thi", "nk>sec", "ret</thi", "nk>!", " ok"}
	for _, chunk := range chunks {
		c, r := p.Push(chunk)
		content += c
		reasoning += r
	}
	fc, fr := p.Flush()
	content += fc
	reasoning += fr
	if content != "Hi ! ok" || reasoning != "secret" {
		t.Fatalf("content %q reasoning %q", content, reasoning)
	}
}

func TestCaseInsensitive(t *testing.T) {
	var p Parser
	c, r := p.Push("A <THINK>X</THINK> B")
	if c != "A  B" || r != "X" {
		t.Fatalf("content %q reasoning %q", c, r)
	}
}

func TestUnclosedThink(t *testing.T) {
	var p Parser
	c, r := p.Push("Answer <think>still going")
	fc, fr := p.Flush()
	if c+fc != "Answer " || r+fr != "still going" {
		t.Fatalf("content %q reasoning %q", c+fc, r+fr)
	}
}

func TestNoTags(t *testing.T) {
	var p Parser
	c, r := p.Push("just an answer")
	fc, fr := p.Flush()
	if c+fc != "just an answer" || r+fr != "" {
		t.Fatalf("content %q reasoning %q", c+fc, r+fr)
	}
}

func TestPartialOpenIsNotLeaked(t *testing.T) {
	var p Parser
	c, r := p.Push("wait <thi")
	if strings.Contains(c, "<") || r != "" {
		t.Fatalf("leaked partial tag: content %q reasoning %q", c, r)
	}
	c2, r2 := p.Push("nk>hid</think>done")
	if c+c2 != "wait done" || r+r2 != "hid" {
		t.Fatalf("content %q reasoning %q", c+c2, r+r2)
	}
}

func TestTwoBlocks(t *testing.T) {
	var p Parser
	c, r := p.Push("<think>one</think>mid<think>two</think>end")
	if c != "midend" || r != "one\ntwo" {
		t.Fatalf("content %q reasoning %q", c, r)
	}
}
