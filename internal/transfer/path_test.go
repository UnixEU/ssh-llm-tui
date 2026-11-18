package transfer

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestUploadTarget(t *testing.T) {
	rootCases := []string{"", ".", "/", "~", "~/", "uploads", "uploads/", "/uploads", "/uploads/", "./uploads/."}
	for _, c := range rootCases {
		rel, err := uploadTarget(c)
		if err != nil || rel != "" {
			t.Fatalf("%q -> rel=%q err=%v", c, rel, err)
		}
	}
	cases := map[string]string{
		"notes.txt":                  "notes.txt",
		"./notes.txt":                "notes.txt",
		"/notes.txt":                 "notes.txt",
		"uploads/notes.txt":          "notes.txt",
		"/uploads/notes.txt":         "notes.txt",
		"~/notes.txt":                "notes.txt",
		"uploads/./notes.txt":        "notes.txt",
		"uploads/sub/file.txt":       "sub/file.txt",
		"/uploads/a/b/c/d.json":      "a/b/c/d.json",
		"docs//guide.md":             "docs/guide.md",
		`win\style\path.txt`:         "win/style/path.txt",
		"/uploads/uploads/inner.txt": "uploads/inner.txt",
		"dir/":                       "dir",
	}
	for in, want := range cases {
		rel, err := uploadTarget(in)
		if err != nil || rel != want {
			t.Fatalf("%q -> rel=%q err=%v want %q", in, rel, err, want)
		}
	}
	denied := []string{
		"../etc/passwd",
		"/uploads/../etc/passwd",
		"uploads/a/../../b",
		`..\secret`,
		"a/../../../x",
		strings.Repeat("d/", 17) + "f",
		strings.Repeat("x", 256),
	}
	for _, c := range denied {
		if rel, err := uploadTarget(c); err == nil {
			t.Fatalf("%q accepted as %q", c, rel)
		} else if !errors.Is(err, os.ErrPermission) && !strings.Contains(err.Error(), "invalid path") {
			t.Fatalf("%q err=%v", c, err)
		}
	}
}
