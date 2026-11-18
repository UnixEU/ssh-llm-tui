package transfer

import (
	"errors"
	"os"
	"testing"
)

func TestUploadTarget(t *testing.T) {
	dirCases := []string{"", ".", "/", "~", "~/", "uploads", "uploads/", "/uploads", "/uploads/"}
	for _, c := range dirCases {
		name, dir, err := uploadTarget(c)
		if err != nil || !dir || name != "" {
			t.Fatalf("%q -> name=%q dir=%v err=%v", c, name, dir, err)
		}
	}
	fileCases := map[string]string{
		"notes.txt":           "notes.txt",
		"./notes.txt":         "notes.txt",
		"/notes.txt":          "notes.txt",
		"uploads/notes.txt":   "notes.txt",
		"/uploads/notes.txt":  "notes.txt",
		"~/notes.txt":         "notes.txt",
		"uploads/./notes.txt": "notes.txt",
	}
	for in, want := range fileCases {
		name, dir, err := uploadTarget(in)
		if err != nil || dir || name != want {
			t.Fatalf("%q -> name=%q dir=%v err=%v", in, name, dir, err)
		}
	}
	denied := []string{"../etc/passwd", "/etc/passwd", "uploads/sub/file.txt", "foo/bar", "/uploads/../etc/passwd"}
	for _, c := range denied {
		_, _, err := uploadTarget(c)
		if !errors.Is(err, os.ErrPermission) && err == nil {
			t.Fatalf("%q err=%v, want permission or invalid", c, err)
		}
	}
}
