package uploads

import (
	"archive/zip"
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T, maxBytes int64, maxFiles int) *Store {
	t.Helper()
	return New(t.TempDir(), maxBytes, maxFiles)
}

func TestCleanPath(t *testing.T) {
	bad := []string{
		"../etc/passwd", "..", ".", "/etc/passwd", `..\secret`, "", "   ",
		"a/../b", "a/./b", "a//b", "/a", "a/..", `a\..\..\b`,
		"a/\x00/b", " /a", "a/ /b",
		strings.Repeat("d/", MaxDepth) + "f",
		strings.Repeat("x", MaxComponentLen+1),
		strings.Repeat(strings.Repeat("y", 200)+"/", 6) + "z",
	}
	for _, name := range bad {
		if got, err := CleanPath(name); err == nil {
			t.Fatalf("accepted %q as %q", name, got)
		}
	}
	good := map[string]string{
		"notes.md":                     "notes.md",
		"dir/notes.md":                 "dir/notes.md",
		`dir\sub\notes.md`:             "dir/sub/notes.md",
		"a/b/":                         "a/b",
		"tab\tname.txt":                "tabname.txt",
		strings.Repeat("d/", 15) + "f": strings.Repeat("d/", 15) + "f",
	}
	for in, want := range good {
		got, err := CleanPath(in)
		if err != nil || got != want {
			t.Fatalf("%q -> %q %v, want %q", in, got, err, want)
		}
	}
}

func TestPutCapsAndIsolation(t *testing.T) {
	s := testStore(t, 32, 2)
	if err := s.Put("user", "a.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("user", "too-big.txt", bytes.Repeat([]byte("x"), 33)); err == nil {
		t.Fatal("expected size cap")
	}
	if err := s.Put("user", "b.txt", []byte("second")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("user", "c.txt", []byte("third")); err == nil {
		t.Fatal("expected file count cap")
	}
	if err := s.Put("other", "a.txt", []byte("theirs")); err != nil {
		t.Fatal(err)
	}
	user := s.List("user")
	if len(user) != 2 || user[0].Name != "a.txt" || !user[0].Included {
		t.Fatalf("user %+v", user)
	}
	if s.List("other")[0].Text != "theirs" {
		t.Fatal(s.List("other"))
	}
	body, err := s.Read("user", "a.txt")
	if err != nil || string(body) != "hello" {
		t.Fatalf("read %q %v", body, err)
	}
	if _, err := s.Read("user", "../a.txt"); err == nil {
		t.Fatal("traversal read")
	}
}

func TestReplaceKeepsIncludedFlag(t *testing.T) {
	s := testStore(t, 1000, 5)
	if err := s.Put("user", "a.txt", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIncluded("user", "a.txt", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("user", "a.txt", []byte("two")); err != nil {
		t.Fatal(err)
	}
	files := s.List("user")
	if len(files) != 1 || files[0].Included || files[0].Text != "two" {
		t.Fatalf("%+v", files)
	}
}

func TestSessionLeaveClearsOnlyThatUser(t *testing.T) {
	s := testStore(t, 1000, 5)
	s.SessionEnter("user")
	s.SessionEnter("user")
	s.SessionEnter("ada")
	if err := s.Put("user", "a.txt", []byte("keep")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("ada", "b.txt", []byte("ada")); err != nil {
		t.Fatal(err)
	}
	s.SessionLeave("user")
	if len(s.List("user")) != 1 {
		t.Fatal("cleared while another user session is open")
	}
	s.SessionLeave("user")
	if len(s.List("user")) != 0 {
		t.Fatal("user files survived the last session")
	}
	if len(s.List("ada")) != 1 {
		t.Fatal("ada files were cleared")
	}
}

func TestContextBudgetTruncates(t *testing.T) {
	s := testStore(t, 10_000, 5)
	if err := s.Put("user", "a.txt", []byte(strings.Repeat("aaaa", 100))); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("user", "b.txt", []byte(strings.Repeat("bbbb", 100))); err != nil {
		t.Fatal(err)
	}
	text, truncated := s.Context("user", 40)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !strings.Contains(text, "----- a.txt") {
		t.Fatalf("missing header: %s", text)
	}
	if strings.Contains(text, "----- b.txt") {
		t.Fatalf("second file should not fit: %s", text)
	}
	if s.SetIncluded("user", "a.txt", false) != nil {
		t.Fatal("toggle")
	}
	text, truncated = s.Context("user", 10_000)
	if truncated || strings.Contains(text, "a.txt") || !strings.Contains(text, "b.txt") {
		t.Fatalf("after toggle text=%q truncated=%v", text, truncated)
	}
}

func TestDOCXAndBinary(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(`<w:document><w:t>Hello &amp; welcome</w:t><w:t>there</w:t></w:document>`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	text, note := Extract("notes.docx", buf.Bytes())
	if note != "" || !strings.Contains(text, "Hello & welcome") || !strings.Contains(text, "there") {
		t.Fatalf("text %q note %q", text, note)
	}
	text, note = Extract("blob.bin", []byte{0, 1, 2, 3})
	if text != "" || note == "" {
		t.Fatalf("binary text %q note %q", text, note)
	}
}

func TestBytesLandOnDiskAndClearRemovesThem(t *testing.T) {
	s := testStore(t, 1000, 5)
	if err := s.Put("user", "a.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	path, err := s.FilePath("user", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "hello" {
		t.Fatalf("disk %q err %v", got, err)
	}
	if err := s.Put("user", "b.txt", []byte("bye")); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear("user"); err != nil {
		t.Fatal(err)
	}
	if len(s.List("user")) != 0 {
		t.Fatal("index not cleared")
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("user dir still present: %v", err)
	}
}

func TestWipeOnStart(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, 1000, 5)
	if err := s.Put("user", "a.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	again := New(dir, 1000, 5)
	if len(again.List("user")) != 0 {
		t.Fatal("wipe on start left files in the index")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("dir not empty: %v %v", entries, err)
	}
}

func TestPutImageDecodeErrorIsNotedAndLogged(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	s := testStore(t, 1<<20, 4)
	broken := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte{0, 1, 2, 3}...)
	if err := s.Put("user", "photo.png", broken); err != nil {
		t.Fatal(err)
	}
	files := s.List("user")
	if len(files) != 1 || files[0].IsImage || !strings.Contains(files[0].Note, "image:") {
		t.Fatalf("%+v", files)
	}
	// Restore the logger before reading so a concurrent log cannot touch the buffer.
	log.SetOutput(os.Stderr)
	out := logs.String()
	if !strings.Contains(out, "WARN upload") || !strings.Contains(out, files[0].Note[len("image: "):]) {
		t.Fatalf("log %q note %q", out, files[0].Note)
	}
}

func TestRemove(t *testing.T) {
	s := testStore(t, 100, 3)
	_ = s.Put("user", "a.txt", []byte("x"))
	if err := s.Remove("user", "a.txt"); err != nil {
		t.Fatal(err)
	}
	if len(s.List("user")) != 0 {
		t.Fatal(s.List("user"))
	}
}

type testOverlay struct {
	versions map[string]string
	deleted  map[string]bool
	added    []string
}

func (o testOverlay) Version(name string) (string, bool, bool) {
	if o.deleted[name] {
		return "", false, true
	}
	v, ok := o.versions[name]
	return v, ok, ok
}

func (o testOverlay) Added() []string { return o.added }

func TestContextWithOverlay(t *testing.T) {
	s := New(t.TempDir(), 1<<20, 10)
	s.SetAutoInclude(1<<20, 10)
	_ = s.Put("u", "p/a.txt", []byte("original a"))
	_ = s.Put("u", "p/b.txt", []byte("original b"))
	_ = s.Put("u", "p/zz.txt", []byte(strings.Repeat("x", 4000)))
	ov := testOverlay{
		versions: map[string]string{"p/a.txt": "edited a", "p/new.md": "new file"},
		deleted:  map[string]bool{"p/b.txt": true},
		added:    []string{"p/new.md"},
	}
	res := s.ContextWith("u", 300, ov)
	if !strings.Contains(res.Text, "edited a") || strings.Contains(res.Text, "original a") {
		t.Fatalf("edit not used:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "original b") || strings.Contains(res.Text, "p/b.txt") {
		t.Fatalf("deleted file shown:\n%s", res.Text)
	}
	if !strings.Contains(res.Text, "new file") || !strings.Contains(res.Text, "p/new.md (new in this session)") {
		t.Fatalf("new file missing:\n%s", res.Text)
	}
	if !res.Full["p/a.txt"] || !res.Full["p/new.md"] {
		t.Fatalf("full %v", res.Full)
	}
	if res.Full["p/zz.txt"] || !res.Truncated {
		t.Fatalf("zz.txt should be truncated: %v", res.Full)
	}
	// The uploads themselves are untouched.
	if got, _ := s.Read("u", "p/a.txt"); string(got) != "original a" {
		t.Fatalf("upload changed: %q", got)
	}
	v := s.View("u")
	if _, ok, editable, _ := v.Original("p/a.txt"); !ok || !editable {
		t.Fatal("text not editable")
	}
	_ = s.Put("u", "p/doc.pdf", []byte("%PDF-1.4"))
	if _, ok, editable, why := v.Original("p/doc.pdf"); !ok || editable || why == "" {
		t.Fatal("pdf editable")
	}
	if !v.IsDir("p") || v.IsDir("p/a.txt") || !v.Ignored("p/.git/x") {
		t.Fatal("view helpers")
	}
}
