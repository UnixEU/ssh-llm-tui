package sshserver

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/downloads"
	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/ssh"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

func startStoresServer(t *testing.T, stores Stores) string {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.AuthMode = "open"
	cfg.Server.HostKeyPath = filepath.Join(t.TempDir(), "keys", "ssh_host_ed25519_key")
	srv, err := NewWith(cfg.Server, func(ssh.Session) (tea.Model, []tea.ProgramOption) {
		return idleModel{}, nil
	}, nil, stores)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return ln.Addr().String()
}

func newStores(t *testing.T) (*uploads.Store, *downloads.Store) {
	t.Helper()
	up := uploads.New(t.TempDir(), 1<<20, 100)
	dl := downloads.New(t.TempDir(), 1<<20)
	if err := dl.Export("user", []downloads.File{
		{Path: "proj/src/main.go", Data: []byte("package main\n")},
		{Path: "proj/README.md", Data: []byte("# readme\n")},
	}, []byte("diff --git a/x b/x\n")); err != nil {
		t.Fatal(err)
	}
	return up, dl
}

func sortedNames(t *testing.T, c *sftp.Client, dir string) string {
	t.Helper()
	entries, err := c.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() {
			n += "/"
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestSFTPRootAndDownloads(t *testing.T) {
	up, dl := newStores(t)
	addr := startStoresServer(t, Stores{Uploads: up, Downloads: dl})
	c, err := sftp.NewClient(dialUser(t, addr))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if rp, err := c.RealPath("."); err != nil || rp != "/" {
		t.Fatalf("realpath . %q %v", rp, err)
	}
	if got := sortedNames(t, c, "/"); got != "downloads/ uploads/" {
		t.Fatalf("root %q", got)
	}
	// Uploads behave as before, with or without the uploads/ prefix.
	for _, p := range []string{"/top.txt", "uploads/a.txt", "/uploads/dir/b.txt"} {
		if err := c.MkdirAll(filepath.Dir(p)); err != nil && filepath.Dir(p) != "/" && filepath.Dir(p) != "uploads" {
			t.Fatal(err)
		}
		f, err := c.Create(p)
		if err != nil {
			t.Fatalf("create %s: %v", p, err)
		}
		io.WriteString(f, "x")
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got := sortedNames(t, c, "/uploads"); got != "a.txt dir/ top.txt" {
		t.Fatalf("uploads %q", got)
	}
	if rp, _ := c.RealPath("uploads/dir"); rp != "/uploads/dir" {
		t.Fatalf("realpath uploads/dir %q", rp)
	}
	if rp, _ := c.RealPath("downloads/files"); rp != "/downloads/files" {
		t.Fatalf("realpath downloads/files %q", rp)
	}

	// Read.
	if got := sortedNames(t, c, "/downloads"); got != "changes.patch files/" {
		t.Fatalf("downloads %q", got)
	}
	if got := sortedNames(t, c, "downloads/files/proj"); got != "README.md src/" {
		t.Fatalf("proj %q", got)
	}
	f, err := c.Open("/downloads/files/proj/src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(f)
	f.Close()
	if string(body) != "package main\n" {
		t.Fatalf("body %q", body)
	}
	st, err := c.Stat("/downloads/changes.patch")
	if err != nil || st.IsDir() || st.Size() == 0 {
		t.Fatalf("stat %+v %v", st, err)
	}

	// Writes are refused.
	deny := func(what string, err error) {
		t.Helper()
		if err == nil || !errors.Is(err, os.ErrPermission) {
			t.Fatalf("%s: want permission denied, got %v", what, err)
		}
	}
	_, err = c.Create("/downloads/new.txt")
	deny("create", err)
	_, err = c.Create("downloads/files/proj/README.md")
	deny("overwrite", err)
	deny("mkdir", c.Mkdir("/downloads/dir"))
	deny("rename in", c.Rename("/uploads/a.txt", "/downloads/a.txt"))
	deny("rename out", c.Rename("/downloads/changes.patch", "/uploads/changes.patch"))
	deny("chmod", c.Chmod("/downloads/changes.patch", 0o600))
	if _, err := up.Stat("user", "a.txt"); err != nil {
		t.Fatal("rename moved the upload")
	}

	// Delete works.
	if err := c.Remove("/downloads/files/proj/README.md"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveDirectory("/downloads/files"); err != nil {
		t.Fatal(err)
	}
	if got := sortedNames(t, c, "/downloads"); got != "changes.patch" {
		t.Fatalf("after delete %q", got)
	}
	if _, err := c.Stat("/downloads/files"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("files/ still there: %v", err)
	}

	// Another user sees an empty downloads folder.
	other, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{User: "other", HostKeyCallback: gossh.InsecureIgnoreHostKey(), Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	oc, err := sftp.NewClient(other)
	if err != nil {
		t.Fatal(err)
	}
	defer oc.Close()
	if got := sortedNames(t, oc, "/downloads"); got != "" {
		t.Fatalf("other user sees %q", got)
	}
}

func TestLegacySCPDownloadsFolder(t *testing.T) {
	up, dl := newStores(t)
	addr := startStoresServer(t, Stores{Uploads: up, Downloads: dl})
	conn := dialUser(t, addr)
	tree := strings.Join(scpSource(t, conn, "scp -r -f downloads/files"), "|")
	for _, part := range []string{"D0755 0 files|D0755 0 proj|", "D0755 0 src|C0644 13 main.go|E", "C0644 9 README.md"} {
		if !strings.Contains(tree, part) {
			t.Fatalf("tree %s missing %s", tree, part)
		}
	}
	single := strings.Join(scpSource(t, conn, "scp -f /downloads/changes.patch"), "|")
	if single != "C0644 19 changes.patch" {
		t.Fatalf("single %s", single)
	}
	whole := strings.Join(scpSource(t, conn, "scp -r -f ~/downloads"), "|")
	if !strings.HasPrefix(whole, "D0755 0 downloads|") || !strings.Contains(whole, "C0644 19 changes.patch") {
		t.Fatalf("whole %s", whole)
	}
	if err := scpPut(conn, "downloads/", "evil.txt", "x"); err == nil {
		t.Fatal("scp -t into downloads accepted")
	}
	if _, err := dl.Stat("user", "evil.txt"); err == nil {
		t.Fatal("file written into downloads")
	}
	// Uploads over legacy scp still work.
	if err := scpPut(conn, "/uploads", "ok.txt", "fine"); err != nil {
		t.Fatal(err)
	}
	if got, _ := up.Read("user", "ok.txt"); string(got) != "fine" {
		t.Fatalf("upload %q", got)
	}
}

func TestDownloadsWipedWithLastTUISession(t *testing.T) {
	up, dl := newStores(t)
	addr := startStoresServer(t, Stores{Uploads: up, Downloads: dl})
	conn := dialUser(t, addr)
	sess, err := conn.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := dl.Export("user", []downloads.File{{Path: "a.txt", Data: []byte("a")}}, []byte("p")); err != nil {
		t.Fatal(err)
	}
	if err := up.Put("user", "a.txt", []byte("a")); err != nil {
		t.Fatal(err)
	}
	// OpenSSH closes the connection when the TUI exits.
	sess.Close()
	conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, derr := dl.Stat("user", downloads.PatchName)
		_, uerr := up.Stat("user", "a.txt")
		if derr != nil && uerr != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not wiped: downloads %v uploads %v", derr, uerr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dl.Dir(), "user")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("user dir left: %v", err)
	}
}
