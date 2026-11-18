package edits

import (
	"strings"
	"testing"
)

func TestParseBlocks(t *testing.T) {
	reply := "Here is the change.\n\n" +
		"FILE: src/app/config.go\n" +
		"```go\n" +
		"<<<<<<< SEARCH\n" +
		"\ttimeout := 30\n" +
		"=======\n" +
		"\ttimeout := 60\n" +
		">>>>>>> REPLACE\n" +
		"```\n" +
		"<<<<<<< SEARCH\n" +
		"a\n" +
		"=======\n" +
		">>>>>>> REPLACE\n" +
		"\n**FILE:** `docs/USAGE.md`\n" +
		"<<<<<<< WHOLE\n" +
		"```markdown\n" +
		"# Usage\n" +
		"```sh\nrun\n```\n" +
		"```\n" +
		">>>>>>> WHOLE\n" +
		"FILE: old.txt\r\n" +
		"<<<<<<< DELETE\r\n" +
		">>>>>>> DELETE\r\n"
	blocks, errs := Parse(reply)
	if len(errs) != 0 {
		t.Fatalf("errs %v", errs)
	}
	if len(blocks) != 4 {
		t.Fatalf("blocks %d %+v", len(blocks), blocks)
	}
	b := blocks[0]
	if b.Path != "src/app/config.go" || b.Kind != KindReplace || strings.Join(b.Search, "|") != "\ttimeout := 30" || strings.Join(b.Replace, "|") != "\ttimeout := 60" {
		t.Fatalf("block 0 %+v", b)
	}
	if blocks[1].Path != "src/app/config.go" || len(blocks[1].Replace) != 0 || blocks[1].Search[0] != "a" {
		t.Fatalf("block 1 %+v", blocks[1])
	}
	if blocks[2].Path != "docs/USAGE.md" || blocks[2].Kind != KindWhole {
		t.Fatalf("block 2 %+v", blocks[2])
	}
	// The wrapping fence is dropped, the inner one stays.
	if got := strings.Join(blocks[2].Replace, "\n"); got != "# Usage\n```sh\nrun\n```" {
		t.Fatalf("whole content %q", got)
	}
	if blocks[3].Path != "old.txt" || blocks[3].Kind != KindDelete {
		t.Fatalf("block 3 %+v", blocks[3])
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"cut off":       "FILE: a.go\n<<<<<<< SEARCH\nx\n=======\ny\n",
		"no divider":    "FILE: a.go\n<<<<<<< SEARCH\nx\n",
		"whole cut off": "FILE: a.go\n<<<<<<< WHOLE\nx\n",
		"no file":       "<<<<<<< SEARCH\nx\n=======\ny\n>>>>>>> REPLACE\n",
		"stray":         "text\n>>>>>>> REPLACE\n",
	}
	for name, reply := range cases {
		blocks, errs := Parse(reply)
		if len(blocks) != 0 || len(errs) == 0 {
			t.Fatalf("%s: blocks %v errs %v", name, blocks, errs)
		}
	}
	// Plain prose and diff-like lines are not blocks.
	blocks, errs := Parse("Use <<< for heredocs.\n==\n> quote\nFILE: \n")
	if len(blocks) != 0 || len(errs) != 0 {
		t.Fatalf("prose: %v %v", blocks, errs)
	}
}

func TestParseMarkerVariants(t *testing.T) {
	reply := "### File: a.txt\n<<<<<<<< search\nold\n========\nnew\n>>>>>>>> replace\n"
	blocks, errs := Parse(reply)
	if len(errs) != 0 || len(blocks) != 1 || blocks[0].Path != "a.txt" {
		t.Fatalf("%v %v", blocks, errs)
	}
}
