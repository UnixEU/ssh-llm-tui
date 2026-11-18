package downloads

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportReplacesPrevious(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, 1<<20)
	if err := s.Export("u", []File{{Path: "proj/a.txt", Data: []byte("A")}, {Path: "proj/sub/b.txt", Data: []byte("B")}}, []byte("patch1")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Read("u", "files/proj/sub/b.txt"); string(got) != "B" {
		t.Fatalf("b %q", got)
	}
	if err := s.Export("u", []File{{Path: "proj/c.txt", Data: []byte("C")}}, []byte("patch2")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stat("u", "files/proj/a.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("old file kept: %v", err)
	}
	if got, _ := s.Read("u", PatchName); string(got) != "patch2" {
		t.Fatalf("patch %q", got)
	}
	entries, err := s.ReadDir("u", "")
	if err != nil || len(entries) != 2 || entries[0].Name != PatchName || entries[1].Name != FilesDir || !entries[1].IsDir {
		t.Fatalf("root %+v %v", entries, err)
	}
	// Only-deletes export: no files/ directory.
	if err := s.Export("u", nil, []byte("patch3")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stat("u", FilesDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("files/ kept: %v", err)
	}
	// Other users see nothing.
	if entries, _ := s.ReadDir("v", ""); len(entries) != 0 {
		t.Fatalf("other user %+v", entries)
	}
}

func TestPathSafety(t *testing.T) {
	s := New(t.TempDir(), 1<<20)
	for _, p := range []string{"../x", "/abs", "a/../../b"} {
		if err := s.Export("u", []File{{Path: p, Data: []byte("x")}}, nil); err == nil {
			t.Fatalf("export %q accepted", p)
		}
		if _, err := s.Stat("u", p); err == nil {
			t.Fatalf("stat %q", p)
		}
	}
	if _, err := s.WriteArchive("u", "../x.tar.gz", nil); err == nil {
		t.Fatal("archive name with a slash")
	}
	if _, err := s.WriteArchive("u", "x.zip", nil); err == nil {
		t.Fatal("zip accepted")
	}
	if _, err := s.WriteArchive("u", "x.tar.gz", []ArchiveEntry{{Name: "../evil", Data: nil}}); err == nil {
		t.Fatal("archive entry with ..")
	}
	// A symlink planted in the tree is not followed.
	s.Export("u", []File{{Path: "a.txt", Data: []byte("x")}}, nil)
	root := filepath.Join(s.Dir(), "u")
	if err := os.Symlink("/etc", filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	if _, err := s.Stat("u", "link/passwd"); err == nil {
		t.Fatal("followed a symlink")
	}
	if entries, _ := s.ReadDir("u", ""); len(entries) != 2 {
		t.Fatalf("symlink listed: %+v", entries)
	}
}

func TestLimits(t *testing.T) {
	s := New(t.TempDir(), 100)
	if err := s.Export("u", []File{{Path: "a", Data: bytes.Repeat([]byte("x"), 90)}}, []byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if err := s.Export("u", []File{{Path: "a", Data: bytes.Repeat([]byte("x"), 95)}}, []byte("0123456789")); err == nil || !strings.Contains(err.Error(), "DOWNLOAD_MAX_TOTAL_BYTES") {
		t.Fatalf("over limit: %v", err)
	}
	// The failed export left the previous one.
	if got, _ := s.Read("u", "files/a"); len(got) != 90 {
		t.Fatalf("previous export lost: %d", len(got))
	}
	big := make([]byte, 4096)
	for i := range big {
		big[i] = byte(i * 7919 % 251)
	}
	if _, err := s.WriteArchive("u", "x.tar.gz", []ArchiveEntry{{Name: "r", Data: big}}); err == nil {
		t.Fatal("archive over limit")
	}
	if entries, _ := s.ReadDir("u", ""); len(entries) != 2 {
		t.Fatalf("part file left: %+v", entries)
	}
}

func TestArchive(t *testing.T) {
	s := New(t.TempDir(), 1<<20)
	n, err := s.WriteArchive("u", "proj.tar.gz", []ArchiveEntry{{Name: "proj/src/b.go", Data: []byte("b")}, {Name: "proj/a.md", Data: []byte("a")}})
	if err != nil || n == 0 {
		t.Fatal(err)
	}
	f, _, err := s.Open("u", "proj.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(h.Name, "/") || strings.Contains(h.Name, "..") {
			t.Fatalf("unsafe %s", h.Name)
		}
		names = append(names, h.Name)
	}
	if strings.Join(names, " ") != "proj/ proj/a.md proj/src/ proj/src/b.go" {
		t.Fatalf("names %v", names)
	}
}

func TestRemoveAndWipe(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, 1<<20)
	s.Export("u", []File{{Path: "p/a", Data: []byte("a")}, {Path: "p/b", Data: []byte("b")}}, []byte("x"))
	if err := s.Remove("u", "files/p/a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("u", "files/p"); err == nil {
		t.Fatal("remove on a dir")
	}
	if err := s.RemoveDir("u", "files"); err != nil {
		t.Fatal(err)
	}
	if s.Usage("u") != 1 {
		t.Fatalf("usage %d", s.Usage("u"))
	}
	// Session counting: the last session wipes the user.
	s.SessionEnter("u")
	s.SessionEnter("u")
	s.SessionLeave("u")
	if _, err := s.Stat("u", PatchName); err != nil {
		t.Fatal("wiped too early")
	}
	s.SessionLeave("u")
	if _, err := os.Stat(filepath.Join(dir, "u")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("not wiped: %v", err)
	}
	// Process start wipes leftovers.
	os.MkdirAll(filepath.Join(dir, "old", "files"), 0o755)
	New(dir, 0)
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("start wipe left %v", entries)
	}
}
