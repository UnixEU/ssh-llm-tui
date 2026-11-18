package edits

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakeSource struct {
	files map[string]string
	bin   map[string]bool
	dirs  map[string]bool
}

func (f fakeSource) Original(name string) ([]byte, bool, bool, string) {
	s, ok := f.files[name]
	if !ok {
		return nil, false, false, ""
	}
	if f.bin[name] {
		return []byte(s), true, false, "binary file"
	}
	return []byte(s), true, true, ""
}
func (f fakeSource) IsDir(name string) bool { return f.dirs[name] }
func (f fakeSource) Ignored(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part == ".git" {
			return true
		}
	}
	return false
}

func src() fakeSource {
	return fakeSource{
		files: map[string]string{
			"proj/main.go":   "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n",
			"proj/crlf.txt":  "one\r\ntwo\r\nthree\r\n",
			"proj/noeol.txt": "alpha\nbeta",
			"proj/dup.txt":   "x\ny\nx\ny\n",
			"proj/mixed.txt": "a\nb\r\nc\n",
			"proj/logo.png":  "\x89PNG",
		},
		bin:  map[string]bool{"proj/logo.png": true},
		dirs: map[string]bool{"proj": true, "proj/sub": true},
	}
}

func rules() Rules {
	return Rules{AllowNew: true, MaxFiles: 20, MaxBytes: 1 << 20, Seen: map[string]bool{"proj/main.go": true, "proj/noeol.txt": true}}
}

func propose(t *testing.T, w *Workspace, reply string, r Rules) Result {
	t.Helper()
	blocks, errs := Parse(reply)
	return w.Propose(src(), blocks, errs, r)
}

func TestProposeAcceptUndoRevert(t *testing.T) {
	w := newWorkspace()
	res := propose(t, w, "FILE: proj/main.go\n<<<<<<< SEARCH\n\tprintln(\"hi\")\n=======\n\tprintln(\"hello\")\n>>>>>>> REPLACE\n", rules())
	if res.Files() != 1 || len(res.Failures) != 0 {
		t.Fatalf("%+v", res)
	}
	if cur, _, _ := w.Current("proj/main.go"); !strings.Contains(cur, "\"hi\"") {
		t.Fatal("applied before accept")
	}
	if _, _, ok := w.Version("proj/main.go"); ok {
		t.Fatal("overlay before accept")
	}
	if err := w.Accept("proj/main.go"); err != nil {
		t.Fatal(err)
	}
	text, exists, ok := w.Version("proj/main.go")
	if !ok || !exists || text != "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n" {
		t.Fatalf("version %q %v %v", text, exists, ok)
	}
	// A second reply builds on the accepted version.
	propose(t, w, "FILE: proj/main.go\n<<<<<<< SEARCH\n\tprintln(\"hello\")\n=======\n\tprintln(\"hello, world\")\n>>>>>>> REPLACE\n", rules())
	if err := w.Accept("proj/main.go"); err != nil {
		t.Fatal(err)
	}
	if err := w.Undo("proj/main.go"); err != nil {
		t.Fatal(err)
	}
	if text, _, _ := w.Version("proj/main.go"); !strings.Contains(text, "\"hello\")") {
		t.Fatalf("undo %q", text)
	}
	if e := w.Entries()[0]; e.Change.Status != StatusPending {
		t.Fatalf("undone change should be pending again: %v", e.Change.Status)
	}
	if err := w.Revert("proj/main.go"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := w.Version("proj/main.go"); ok {
		t.Fatal("revert kept the edit")
	}
	if err := w.Undo("proj/main.go"); err != nil {
		t.Fatal(err)
	}
	if !w.Modified("proj/main.go") {
		t.Fatal("undo after revert")
	}
	if err := w.Reject("proj/main.go"); !errors.Is(err, ErrNoChange) {
		t.Fatalf("reject after revert: %v", err)
	}
}

func TestRejectAndAll(t *testing.T) {
	w := newWorkspace()
	propose(t, w, "FILE: proj/main.go\n<<<<<<< SEARCH\npackage main\n=======\npackage app\n>>>>>>> REPLACE\nFILE: proj/new.md\n<<<<<<< WHOLE\n# New\n>>>>>>> WHOLE\n", rules())
	if w.Pending() != 2 {
		t.Fatalf("pending %d", w.Pending())
	}
	if err := w.Reject("proj/main.go"); err != nil {
		t.Fatal(err)
	}
	n, err := w.AcceptAll()
	if n != 1 || err != nil {
		t.Fatalf("accept all %d %v", n, err)
	}
	if got := w.Added(); len(got) != 1 || got[0] != "proj/new.md" {
		t.Fatalf("added %v", got)
	}
	if text, _, _ := w.Version("proj/new.md"); text != "# New\n" {
		t.Fatalf("new %q", text)
	}
	if w.Modified("proj/main.go") {
		t.Fatal("rejected change applied")
	}
	propose(t, w, "FILE: proj/main.go\n<<<<<<< SEARCH\npackage main\n=======\npackage app\n>>>>>>> REPLACE\n", rules())
	if w.RejectAll() != 1 {
		t.Fatal("reject all")
	}
}

func TestMatchingTolerance(t *testing.T) {
	w := newWorkspace()
	// Indentation and trailing spaces differ; CRLF file.
	res := propose(t, w, "FILE: proj/main.go\n<<<<<<< SEARCH\n\nfunc main() {   \n  println(\"hi\")\n}\n=======\nfunc main() {\n  println(\"hi\")\n  println(\"bye\")\n}\n>>>>>>> REPLACE\n"+
		"FILE: proj/crlf.txt\n<<<<<<< SEARCH\ntwo\n=======\n2\n>>>>>>> REPLACE\n"+
		"FILE: proj/mixed.txt\n<<<<<<< SEARCH\nb\n=======\nB\n>>>>>>> REPLACE\n<<<<<<< SEARCH\nc\n=======\nC\n>>>>>>> REPLACE\n", rules())
	if res.Files() != 3 || len(res.Failures) != 0 {
		t.Fatalf("%+v", res.Failures)
	}
	if _, err := w.AcceptAll(); err != nil {
		t.Fatal(err)
	}
	got, _, _ := w.Version("proj/main.go")
	if got != "package main\n\nfunc main() {\n\tprintln(\"hi\")\n\tprintln(\"bye\")\n}\n" {
		t.Fatalf("reindent %q", got)
	}
	if got, _, _ := w.Version("proj/crlf.txt"); got != "one\r\n2\r\nthree\r\n" {
		t.Fatalf("crlf %q", got)
	}
	// Mixed line endings: untouched lines keep theirs.
	if got, _, _ := w.Version("proj/mixed.txt"); got != "a\nB\r\nC\n" {
		t.Fatalf("mixed %q", got)
	}
}

func TestMatchFailures(t *testing.T) {
	w := newWorkspace()
	res := propose(t, w, "FILE: proj/dup.txt\n<<<<<<< SEARCH\nx\ny\n=======\nz\n>>>>>>> REPLACE\n"+
		"FILE: proj/main.go\n<<<<<<< SEARCH\n\tprintln(\"nope\")\n=======\nx\n>>>>>>> REPLACE\n<<<<<<< SEARCH\npackage main\n=======\npackage app\n>>>>>>> REPLACE\n", rules())
	if len(res.Changes) != 2 {
		t.Fatalf("%+v", res)
	}
	dup := res.Changes[0]
	if dup.Status != StatusFailed || !strings.Contains(dup.Reason, "more than one") {
		t.Fatalf("dup %+v", dup)
	}
	main := res.Changes[1]
	if main.Status != StatusPending || main.Applied != 1 || len(main.Failures) != 1 {
		t.Fatalf("main %+v", main)
	}
	if !strings.Contains(main.Failures[0].Reason, "not found") || main.Failures[0].NearAt != 2 {
		t.Fatalf("near %+v", main.Failures[0])
	}
	msg := FailureMessage(w.Failures(), 0)
	if !strings.Contains(msg, "FILE: proj/dup.txt, SEARCH block 1") || !strings.Contains(msg, "println(\"hi\")") {
		t.Fatalf("message %s", msg)
	}
}

func TestSafetyRules(t *testing.T) {
	w := newWorkspace()
	r := rules()
	r.Seen = map[string]bool{}
	cases := map[string]string{
		"FILE: proj/main.go\n<<<<<<< WHOLE\nx\n>>>>>>> WHOLE\n":                                      "WHOLE needs the complete file",
		"FILE: proj/logo.png\n<<<<<<< WHOLE\nx\n>>>>>>> WHOLE\n":                                     "not an editable text file",
		"FILE: ../etc/passwd\n<<<<<<< WHOLE\nx\n>>>>>>> WHOLE\n":                                     "invalid path",
		"FILE: proj/.git/config\n<<<<<<< WHOLE\nx\n>>>>>>> WHOLE\n":                                  "ignored",
		"FILE: downloads/x.txt\n<<<<<<< WHOLE\nx\n>>>>>>> WHOLE\n":                                   "reserved",
		"FILE: proj/sub\n<<<<<<< WHOLE\nx\n>>>>>>> WHOLE\n":                                          "directory",
		"FILE: proj/main.go\n<<<<<<< DELETE\n>>>>>>> DELETE\n":                                       "EDIT_ALLOW_DELETE",
		"FILE: proj/missing.go\n<<<<<<< SEARCH\na\n=======\nb\n>>>>>>> REPLACE\n":                    "does not exist",
		"FILE: proj/main.go\n<<<<<<< SEARCH\n\n=======\nb\n>>>>>>> REPLACE\n":                        "empty SEARCH",
		"FILE: proj/main.go\n<<<<<<< SEARCH\npackage main\n=======\npackage main\n>>>>>>> REPLACE\n": "do not change",
	}
	for reply, want := range cases {
		res := propose(t, w, reply, r)
		if len(res.Changes) != 1 || res.Changes[0].Status != StatusFailed || !strings.Contains(res.Changes[0].Reason, want) {
			t.Fatalf("%q: %+v", reply, res.Changes)
		}
	}
	r.AllowNew = false
	res := propose(t, w, "FILE: proj/new.txt\n<<<<<<< WHOLE\nx\n>>>>>>> WHOLE\n", r)
	if res.Changes[0].Status != StatusFailed || !strings.Contains(res.Changes[0].Reason, "EDIT_ALLOW_NEW") {
		t.Fatalf("%+v", res.Changes)
	}
	// SEARCH/REPLACE works on a file the model saw truncated.
	res = propose(t, w, "FILE: proj/main.go\n<<<<<<< SEARCH\npackage main\n=======\npackage app\n>>>>>>> REPLACE\n", r)
	if res.Changes[0].Status != StatusPending {
		t.Fatalf("%+v", res.Changes)
	}
}

func TestLimits(t *testing.T) {
	w := newWorkspace()
	r := rules()
	r.MaxFiles = 1
	r.MaxBytes = 10
	res := propose(t, w, "FILE: proj/a.txt\n<<<<<<< WHOLE\nshort\n>>>>>>> WHOLE\nFILE: proj/b.txt\n<<<<<<< WHOLE\nb\n>>>>>>> WHOLE\n", r)
	if res.Changes[0].Status != StatusPending || res.Changes[1].Status != StatusFailed || !strings.Contains(res.Changes[1].Reason, "EDIT_MAX_FILES") {
		t.Fatalf("%+v", res.Changes)
	}
	res = propose(t, w, "FILE: proj/c.txt\n<<<<<<< WHOLE\nthis is longer than ten bytes\n>>>>>>> WHOLE\n", r)
	if res.Changes[0].Status != StatusFailed || !strings.Contains(res.Changes[0].Reason, "UPLOAD_MAX_BYTES") {
		t.Fatalf("%+v", res.Changes)
	}
}

func TestDeleteAllowed(t *testing.T) {
	w := newWorkspace()
	r := rules()
	r.AllowDelete = true
	propose(t, w, "FILE: proj/noeol.txt\n<<<<<<< DELETE\n>>>>>>> DELETE\n", r)
	if err := w.Accept("proj/noeol.txt"); err != nil {
		t.Fatal(err)
	}
	if _, exists, ok := w.Version("proj/noeol.txt"); !ok || exists {
		t.Fatal("not deleted")
	}
	d := w.Changed()
	if len(d) != 1 || d[0].NewExists {
		t.Fatalf("%+v", d)
	}
}

func TestStalePending(t *testing.T) {
	w := newWorkspace()
	propose(t, w, "FILE: proj/main.go\n<<<<<<< SEARCH\npackage main\n=======\npackage a\n>>>>>>> REPLACE\n", rules())
	w.Accept("proj/main.go")
	propose(t, w, "FILE: proj/main.go\n<<<<<<< SEARCH\npackage a\n=======\npackage b\n>>>>>>> REPLACE\n", rules())
	// Reverting while a proposal waits makes it stale; it is superseded.
	w.Revert("proj/main.go")
	if err := w.Accept("proj/main.go"); !errors.Is(err, ErrNoChange) {
		t.Fatalf("accept after revert: %v", err)
	}
}

func TestManagerSessions(t *testing.T) {
	m := NewManager()
	m.SessionEnter("u")
	m.SessionEnter("u")
	w := m.For("u")
	propose(t, w, "FILE: proj/new.md\n<<<<<<< WHOLE\nx\n>>>>>>> WHOLE\n", rules())
	w.AcceptAll()
	m.SessionLeave("u")
	if len(m.For("u").Added()) != 1 {
		t.Fatal("cleared while a session is open")
	}
	m.SessionLeave("u")
	if len(m.For("u").Entries()) != 0 {
		t.Fatal("not cleared after the last session")
	}
}

func TestPatchApplies(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	w := newWorkspace()
	r := rules()
	r.AllowDelete = true
	reply := "FILE: proj/main.go\n<<<<<<< SEARCH\n\tprintln(\"hi\")\n=======\n\tprintln(\"hello\")\n\tprintln(\"again\")\n>>>>>>> REPLACE\n" +
		"FILE: proj/crlf.txt\n<<<<<<< SEARCH\nthree\n=======\n3\n>>>>>>> REPLACE\n" +
		"FILE: proj/noeol.txt\n<<<<<<< SEARCH\nbeta\n=======\nbeta\ngamma\n>>>>>>> REPLACE\n" +
		"FILE: proj/dup.txt\n<<<<<<< DELETE\n>>>>>>> DELETE\n" +
		"FILE: proj/docs/new file.md\n<<<<<<< WHOLE\n# Title\n\ntext\n>>>>>>> WHOLE\n" +
		"FILE: proj/empty.txt\n<<<<<<< WHOLE\n>>>>>>> WHOLE\n"
	res := propose(t, w, reply, r)
	if len(res.Failures) != 0 {
		t.Fatalf("%+v", res.Failures)
	}
	if n, err := w.AcceptAll(); err != nil || n != 6 {
		t.Fatalf("accept %d %v", n, err)
	}
	patch := Patch(w.Changed(), "proj/")
	dir := t.TempDir()
	for name, body := range src().files {
		if !strings.HasPrefix(name, "proj/") {
			continue
		}
		p := filepath.Join(dir, strings.TrimPrefix(name, "proj/"))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pf := filepath.Join(t.TempDir(), "changes.patch")
	os.WriteFile(pf, patch, 0o644)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s\n%s", args, err, out, patch)
		}
	}
	run("init", "-q")
	run("apply", "--check", pf)
	run("apply", pf)
	for _, d := range w.Changed() {
		p := filepath.Join(dir, strings.TrimPrefix(d.Path, "proj/"))
		got, err := os.ReadFile(p)
		if !d.NewExists {
			if err == nil {
				t.Fatalf("%s not deleted", d.Path)
			}
			continue
		}
		if err != nil || string(got) != d.New {
			t.Fatalf("%s: %q want %q (%v)", d.Path, got, d.New, err)
		}
	}
}

func TestHunksAndStats(t *testing.T) {
	old := strings.Repeat("line\n", 20) + "end\n"
	new := "first\n" + strings.Repeat("line\n", 20) + "END\n"
	hunks := Hunks(old, new, 3)
	if len(hunks) != 2 {
		t.Fatalf("hunks %d", len(hunks))
	}
	if hunks[0].Header() != "@@ -1,3 +1,4 @@" {
		t.Fatalf("header %s", hunks[0].Header())
	}
	a, r := Stats(old, new)
	if a != 2 || r != 1 {
		t.Fatalf("stats +%d -%d", a, r)
	}
	if h := Hunks("", "a\nb\n", 3); len(h) != 1 || h[0].Header() != "@@ -0,0 +1,2 @@" {
		t.Fatalf("new file %v", h)
	}
	// Large unrelated texts still produce a valid script.
	var x, y strings.Builder
	for i := 0; i < 5000; i++ {
		x.WriteString("a" + string(rune('0'+i%10)) + "\n")
		y.WriteString("b" + string(rune('0'+i%7)) + "\n")
	}
	if a, r := Stats(x.String(), y.String()); a != 5000 || r != 5000 {
		t.Fatalf("big +%d -%d", a, r)
	}
}

func TestRootDir(t *testing.T) {
	if RootDir([]string{"p/a", "p/b/c"}) != "p" {
		t.Fatal("root")
	}
	if RootDir([]string{"p/a", "q/b"}) != "" || RootDir([]string{"a.txt"}) != "" || RootDir(nil) != "" {
		t.Fatal("no root")
	}
}

func TestQuotePath(t *testing.T) {
	if quotePath("a/b c.txt") != "a/b c.txt" || quotePath(`a/"q".txt`) != `"a/\"q\".txt"` {
		t.Fatal(quotePath(`a/"q".txt`))
	}
}
