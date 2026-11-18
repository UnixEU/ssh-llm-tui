package uploads

import (
	"bytes"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNestedPutReadDirStat(t *testing.T) {
	s := testStore(t, 1000, 50)
	for _, p := range []string{"proj/README.md", "proj/src/main.go", "proj/src/util/str.go", "top.txt"} {
		if err := s.Put("user", p, []byte("body of "+p)); err != nil {
			t.Fatal(p, err)
		}
	}
	if err := s.Mkdir("user", "proj/empty/deeper"); err != nil {
		t.Fatal(err)
	}
	disk, _ := s.FilePath("user", "proj/src/util/str.go")
	if got, err := os.ReadFile(disk); err != nil || string(got) != "body of proj/src/util/str.go" {
		t.Fatalf("disk %q %v", got, err)
	}
	if !strings.HasSuffix(filepath.ToSlash(disk), "/user/proj/src/util/str.go") {
		t.Fatal(disk)
	}
	root, err := s.ReadDir("user", "")
	if err != nil || len(root) != 2 || root[0].Name != "proj" || !root[0].IsDir || root[1].Name != "top.txt" || root[1].IsDir {
		t.Fatalf("root %+v %v", root, err)
	}
	proj, err := s.ReadDir("user", "proj")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range proj {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "README.md,empty,src" {
		t.Fatalf("proj %v", names)
	}
	for _, p := range []string{"proj", "proj/src", "proj/empty", "proj/empty/deeper"} {
		if st, err := s.Stat("user", p); err != nil || !st.IsDir {
			t.Fatalf("stat %s %+v %v", p, st, err)
		}
	}
	if st, err := s.Stat("user", "proj/src/main.go"); err != nil || st.IsDir || st.Size == 0 {
		t.Fatalf("stat file %+v %v", st, err)
	}
	if _, err := s.Stat("user", "nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := s.ReadDir("user", "top.txt"); err == nil {
		t.Fatal("readdir on a file")
	}
	// A file cannot sit where a directory is and the other way round.
	if err := s.Put("user", "proj/src", []byte("x")); err == nil {
		t.Fatal("file over a directory")
	}
	if err := s.Put("user", "top.txt/inner", []byte("x")); err == nil {
		t.Fatal("file under a file")
	}
	if err := s.Mkdir("user", "top.txt"); err == nil {
		t.Fatal("dir over a file")
	}
	if f := s.List("user"); f[0].Name != "proj/README.md" || f[0].Base() != "README.md" || f[0].Dir() != "proj" {
		t.Fatalf("%+v", f[0])
	}
}

func TestRemoveRenameRemoveDir(t *testing.T) {
	s := testStore(t, 1000, 50)
	for _, p := range []string{"a/x.txt", "a/b/y.txt", "ab/z.txt"} {
		if err := s.Put("user", p, []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Remove("user", "a/b"); err == nil {
		t.Fatal("Remove on a directory")
	}
	if err := s.Rename("user", "a/x.txt", "a/b/moved.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("user", "a/x.txt"); err == nil {
		t.Fatal("old name still there")
	}
	if got, _ := s.Read("user", "a/b/moved.md"); string(got) != "a/x.txt" {
		t.Fatalf("moved %q", got)
	}
	if err := s.Rename("user", "a", "c/d"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Read("user", "c/d/b/y.txt"); string(got) != "a/b/y.txt" {
		t.Fatalf("dir rename %q", got)
	}
	disk, _ := s.FilePath("user", "c/d/b/moved.md")
	if _, err := os.Stat(disk); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("user", "c", "c/inner"); err == nil {
		t.Fatal("moved a directory into itself")
	}
	if err := s.Rename("user", "ab", "c"); err == nil {
		t.Fatal("directory replaced an existing one")
	}
	if err := s.RemoveDir("user", "c"); err != nil {
		t.Fatal(err)
	}
	if files, n := s.Usage("user"); files != 1 || n != int64(len("ab/z.txt")) {
		t.Fatalf("usage %d %d", files, n)
	}
	if _, err := s.Stat("user", "c"); err == nil {
		t.Fatal("c survived")
	}
	dir, _ := s.FilePath("user", "c")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("c on disk", err)
	}
	if err := s.Remove("user", "ab/z.txt"); err != nil {
		t.Fatal(err)
	}
	// The empty parent stays as a directory until removed.
	if st, err := s.Stat("user", "ab"); err != nil || !st.IsDir {
		t.Fatal(st, err)
	}
	if err := s.RemoveDir("user", "ab"); err != nil {
		t.Fatal(err)
	}
}

func TestLimitsFailCleanly(t *testing.T) {
	s := testStore(t, 100, 3)
	s.SetMaxTotalBytes(150)
	if err := s.Put("user", "a/1.txt", bytes.Repeat([]byte("1"), 100)); err != nil {
		t.Fatal(err)
	}
	err := s.Put("user", "a/2.txt", bytes.Repeat([]byte("2"), 60))
	if err == nil || !strings.Contains(err.Error(), "UPLOAD_MAX_TOTAL_BYTES") {
		t.Fatalf("total cap: %v", err)
	}
	if err := s.Put("user", "a/big.txt", bytes.Repeat([]byte("x"), 101)); err == nil || !strings.Contains(err.Error(), "UPLOAD_MAX_BYTES") {
		t.Fatalf("file cap: %v", err)
	}
	if err := s.Put("user", "a/2.txt", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("user", "a/3.txt", []byte("three")); err != nil {
		t.Fatal(err)
	}
	err = s.Put("user", "a/4.txt", []byte("four"))
	if err == nil || !strings.Contains(err.Error(), "UPLOAD_MAX_FILES") {
		t.Fatalf("count cap: %v", err)
	}
	if err := s.Precheck("user", "a/5.txt"); err == nil {
		t.Fatal("precheck passed at the file cap")
	}
	// Replacing an existing file is allowed at the cap.
	if err := s.Put("user", "a/3.txt", []byte("3")); err != nil {
		t.Fatal(err)
	}
	files, n := s.Usage("user")
	if files != 3 || n != 104 {
		t.Fatalf("usage %d %d", files, n)
	}
	entries, _ := os.ReadDir(filepath.Join(s.Dir(), "user", "a"))
	if len(entries) != 3 {
		t.Fatalf("disk has %d entries, want 3 (no partial files)", len(entries))
	}
}

func TestIgnoredComponentsAreDropped(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	s := testStore(t, 1000, 2)
	if err := s.Mkdir("user", "repo/.git"); err != nil {
		t.Fatal(err)
	}
	if err := s.Mkdir("user", "repo/.git/objects/aa"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := s.Put("user", "repo/.git/objects/aa/obj"+string(rune('a'+i)), []byte("blob")); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Put("user", "repo/web/node_modules/x/index.js", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("user", "repo/.DS_Store", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("user", "repo/main.go", []byte("package main")); err != nil {
		t.Fatal(err)
	}
	if files, _ := s.Usage("user"); files != 1 {
		t.Fatalf("ignored files counted: %d", files)
	}
	if st, err := s.Stat("user", "repo/.git/objects/aa"); err != nil || !st.IsDir {
		t.Fatalf("ignored dir stat %+v %v", st, err)
	}
	if entries, err := s.ReadDir("user", "repo/.git"); err != nil || len(entries) != 0 {
		t.Fatalf("ignored dir list %+v %v", entries, err)
	}
	root, _ := s.ReadDir("user", "repo")
	for _, e := range root {
		if e.Name == ".git" || e.Name == ".DS_Store" {
			t.Fatalf("ignored entry listed: %+v", root)
		}
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "user", "repo", ".git")); !os.IsNotExist(err) {
		t.Fatal(".git reached the disk")
	}
	log.SetOutput(os.Stderr)
	if n := strings.Count(logs.String(), `name="repo/.git" ignored`); n != 1 {
		t.Fatalf("ignore logged %d times:\n%s", n, logs.String())
	}

	s.SetIgnore(nil)
	if err := s.Put("user", "repo/.git/HEAD", []byte("ref")); err != nil {
		t.Fatal(err)
	}
	if files, _ := s.Usage("user"); files != 2 {
		t.Fatal("ignore list not cleared")
	}
	if got := ParseIgnore(" .git , node_modules,,a/b "); strings.Join(got, "|") != ".git|node_modules" {
		t.Fatal(got)
	}
	if ParseIgnore("none") != nil {
		t.Fatal("none")
	}
}

func TestSymlinkInUserDirIsRefused(t *testing.T) {
	s := testStore(t, 1000, 10)
	if err := s.Put("user", "a.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(s.Dir(), "user", "evil")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	if err := s.Put("user", "evil/payload.txt", []byte("x")); err == nil {
		t.Fatal("wrote through a symlink")
	}
	if err := s.Mkdir("user", "evil/sub"); err == nil {
		t.Fatal("mkdir through a symlink")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("outside dir touched: %v", entries)
	}
}

func TestAutoIncludeAndDirToggle(t *testing.T) {
	s := testStore(t, 1<<20, 100)
	s.SetAutoInclude(100, 3)
	// The first file is always included, even over the budget.
	if err := s.Put("user", "big.txt", bytes.Repeat([]byte("b"), 1000)); err != nil {
		t.Fatal(err)
	}
	s.SetIncluded("user", "big.txt", false)
	for i := 0; i < 6; i++ {
		name := "tree/f" + string(rune('0'+i)) + ".txt"
		if err := s.Put("user", name, bytes.Repeat([]byte("t"), 40)); err != nil {
			t.Fatal(err)
		}
	}
	incl := 0
	for _, f := range s.List("user") {
		if f.Included {
			incl++
		}
	}
	if incl != 3 {
		t.Fatalf("auto-included %d, want 3", incl)
	}
	n, err := s.SetIncludedDir("user", "tree", true)
	if err != nil || n != 6 {
		t.Fatal(n, err)
	}
	for _, f := range s.List("user") {
		if strings.HasPrefix(f.Name, "tree/") && !f.Included {
			t.Fatal(f.Name)
		}
	}
	if _, err := s.SetIncludedDir("user", "tree", false); err != nil {
		t.Fatal(err)
	}
	if s.IncludedTokens("user") != 0 {
		t.Fatal(s.IncludedTokens("user"))
	}
	if _, err := s.SetIncludedDir("user", "nothing", true); err == nil {
		t.Fatal("empty dir toggle")
	}
}

func TestContextNamesFilesOverBudget(t *testing.T) {
	s := testStore(t, 1<<20, 300)
	s.SetAutoInclude(1<<30, 1000)
	for i := 0; i < 200; i++ {
		name := filepath.ToSlash(filepath.Join("pkg", "dir"+string(rune('a'+i%10)), "file"+strings.Repeat("x", i%7)+string(rune('A'+i%26))+".txt"))
		_ = s.Put("user", name+string(rune('0'+i/26)), bytes.Repeat([]byte("z"), 200))
	}
	text, truncated := s.Context("user", 500)
	if !truncated {
		t.Fatal("not truncated")
	}
	if !strings.Contains(text, "left out, over the context token budget") || !strings.Contains(text, "and ") {
		t.Fatalf("no omitted note:\n%s", text[len(text)-400:])
	}
	if !strings.Contains(text, "----- pkg/") {
		t.Fatal("relative path missing from the file header")
	}
	if !strings.Contains(text, "- ... and ") {
		t.Fatal("describe list not capped")
	}
}
