package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChangesDiffAndFileToggle(t *testing.T) {
	stub := &sseServer{replies: []string{
		"FILE: proj/main.go\n<<<<<<< SEARCH\n\tprintln(\"hi\")\n=======\n\tprintln(\"hello\")\n>>>>>>> REPLACE\n",
		"FILE: proj/main.go\n<<<<<<< SEARCH\n\tprintln(\"hello\")\n=======\n\tprintln(\"hey\")\n>>>>>>> REPLACE\n",
	}}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	m, up, _, _ := editModel(t, srv)
	if err := up.Put("user", "proj/main.go", []byte("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n")); err != nil {
		t.Fatal(err)
	}
	send(t, m, "change it")
	key(m, "ctrl+e")
	v := m.View()
	if !strings.Contains(v, "upload → proposed") || !strings.Contains(v, "+    println(\"hello\")") || !strings.Contains(v, "-    println(\"hi\")") {
		t.Fatalf("diff pane missing:\n%s", v)
	}
	key(m, "d")
	v = m.View()
	if !strings.Contains(v, "proposed version") || !strings.Contains(v, "4      println(\"hello\")") || strings.Contains(v, "-    println(\"hi\")") {
		t.Fatalf("file view wrong:\n%s", v)
	}
	key(m, "ctrl+r")
	if !strings.Contains(m.View(), "upload → proposed") {
		t.Fatal("ctrl+r should toggle back to the diff")
	}

	// Accept, then a second proposal: the diff base is the upload unless b.
	key(m, "a")
	key(m, "esc")
	send(t, m, "again")
	key(m, "ctrl+e")
	v = m.View()
	if !strings.Contains(v, "-    println(\"hi\")") || !strings.Contains(v, "+    println(\"hey\")") {
		t.Fatalf("diff should compare the upload with the proposal:\n%s", v)
	}
	key(m, "b")
	v = m.View()
	if !strings.Contains(v, "accepted → proposed") || !strings.Contains(v, "-    println(\"hello\")") {
		t.Fatalf("b should diff against the accepted version:\n%s", v)
	}
}

func TestChangesPaneFocusAndHunks(t *testing.T) {
	var src, repl strings.Builder
	for i := 0; i < 80; i++ {
		src.WriteString("line\n")
	}
	src.WriteString("first\n")
	for i := 0; i < 80; i++ {
		src.WriteString("line\n")
	}
	src.WriteString("second\n")
	repl.WriteString("FILE: big.txt\n<<<<<<< SEARCH\nfirst\n=======\nFIRST\n>>>>>>> REPLACE\n\nFILE: big.txt\n<<<<<<< SEARCH\nsecond\n=======\nSECOND\n>>>>>>> REPLACE\n")
	stub := &sseServer{replies: []string{repl.String()}}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	m, up, _, _ := editModel(t, srv)
	if err := up.Put("user", "big.txt", []byte(src.String())); err != nil {
		t.Fatal(err)
	}
	send(t, m, "upper")
	key(m, "ctrl+e")
	if m.chTop != 0 {
		t.Fatal("diff should start at the top")
	}
	key(m, "]")
	if m.chTop == 0 {
		t.Fatal("] should jump to the second hunk")
	}
	key(m, "[")
	if m.chTop != 0 {
		t.Fatalf("[ should go back to the first hunk, at %d", m.chTop)
	}
	key(m, "tab")
	if !m.chFocus {
		t.Fatal("tab should focus the diff pane")
	}
	key(m, "j")
	if m.chTop != 1 {
		t.Fatalf("j in the diff pane should scroll, at %d", m.chTop)
	}
	key(m, "esc")
	if m.chFocus || m.screen != screenChanges {
		t.Fatal("esc should first leave the pane")
	}
	key(m, "esc")
	if m.screen != screenChat {
		t.Fatal("second esc should return to chat")
	}
}
