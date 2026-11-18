package sshserver

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/ssh"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

type idleModel struct{}

func (idleModel) Init() tea.Cmd                         { return nil }
func (m idleModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (idleModel) View() string                          { return "" }

func startUploadServer(t *testing.T, store *uploads.Store) string {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 2222
	cfg.Server.AuthMode = "open"
	cfg.Server.HostKeyPath = filepath.Join(t.TempDir(), "keys", "ssh_host_ed25519_key")
	srv, err := New(cfg.Server, func(ssh.Session) (tea.Model, []tea.ProgramOption) {
		return idleModel{}, nil
	}, nil, store)
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

func dialUser(t *testing.T, addr string) *gossh.Client {
	t.Helper()
	conn, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{
		User:            "user",
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestSFTPPutListStatRemove(t *testing.T) {
	store := uploads.New(t.TempDir(), 1<<20, 10)
	addr := startUploadServer(t, store)
	client, err := sftp.NewClient(dialUser(t, addr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	put := func(remote, body string) {
		t.Helper()
		f, err := client.Create(remote)
		if err != nil {
			t.Fatalf("create %s: %v", remote, err)
		}
		if _, err := io.WriteString(f, body); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close %s: %v", remote, err)
		}
	}
	put("name.txt", "one")
	put("./dot.txt", "two")
	put("/slash.txt", "three")
	put("uploads/nested.txt", "four")

	for _, name := range []string{"name.txt", "dot.txt", "slash.txt", "nested.txt"} {
		body, err := store.Read("user", name)
		if err != nil {
			t.Fatalf("store missing %s: %v", name, err)
		}
		if len(body) == 0 {
			t.Fatalf("empty %s", name)
		}
	}
	disk, err := store.FilePath("user", "nested.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(disk); err != nil {
		t.Fatal(err)
	}

	entries, err := client.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 4 {
		t.Fatalf("list . = %d", len(entries))
	}
	uploads, err := client.ReadDir("uploads")
	if err != nil {
		t.Fatal(err)
	}
	if len(uploads) < 4 {
		t.Fatalf("list uploads = %d", len(uploads))
	}
	info, err := client.Stat("uploads/nested.txt")
	if err != nil || info.Size() != 4 {
		t.Fatalf("stat %+v %v", info, err)
	}
	root, err := client.Stat(".")
	if err != nil || !root.IsDir() {
		t.Fatalf("stat . %+v %v", root, err)
	}
	up, err := client.Stat("uploads")
	if err != nil || !up.IsDir() {
		t.Fatalf("stat uploads %+v %v", up, err)
	}

	put("name.txt", "replaced-body")
	got, err := store.Read("user", "name.txt")
	if err != nil || string(got) != "replaced-body" {
		t.Fatalf("reput %q %v", got, err)
	}
	if err := client.Remove("uploads/dot.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("user", "dot.txt"); err == nil {
		t.Fatal("dot.txt still in the store")
	}

	// The SFTP server resolves ".." against "/" before the handler sees
	// the path, so this lands inside the user's directory as etc/passwd.
	put("../etc/passwd", "contained")
	disk, err = store.FilePath("user", "etc/passwd")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(disk, store.Dir()) {
		t.Fatalf("escaped: %s", disk)
	}
	if _, err := os.Stat(disk); err != nil {
		t.Fatal(err)
	}
}

func TestServerOffersPostQuantumKex(t *testing.T) {
	cfg := gossh.ServerConfig{}
	cfg.SetDefaults()
	var mlkem, curve bool
	for _, name := range cfg.KeyExchanges {
		switch name {
		case gossh.KeyExchangeMLKEM768X25519:
			mlkem = true
		case gossh.KeyExchangeCurve25519:
			curve = true
		}
	}
	if !mlkem || !curve {
		t.Fatalf("kex list = %v", cfg.KeyExchanges)
	}
}

func TestLegacySCPUpload(t *testing.T) {
	store := uploads.New(t.TempDir(), 1<<20, 10)
	addr := startUploadServer(t, store)
	conn := dialUser(t, addr)
	for _, target := range []string{".", "uploads/", "uploads/plain.txt", "~"} {
		if err := scpPut(conn, target, "plain.txt", "scp-body"); err != nil {
			t.Fatalf("scp -t %s: %v", target, err)
		}
	}
	got, err := store.Read("user", "plain.txt")
	if err != nil || string(got) != "scp-body" {
		t.Fatalf("store %q %v", got, err)
	}
}

func scpPut(conn *gossh.Client, target, name, body string) error {
	session, err := conn.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	stdin, err := session.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		return err
	}
	if err := session.Start("scp -t " + target); err != nil {
		return err
	}
	ack := make([]byte, 1)
	if _, err := io.ReadFull(stdout, ack); err != nil {
		return err
	}
	header := "C0644 " + strconv.Itoa(len(body)) + " " + name + "\n"
	if _, err := io.WriteString(stdin, header); err != nil {
		return err
	}
	if _, err := io.ReadFull(stdout, ack); err != nil {
		return err
	}
	if _, err := io.WriteString(stdin, body); err != nil {
		return err
	}
	if _, err := stdin.Write([]byte{0}); err != nil {
		return err
	}
	if _, err := io.ReadFull(stdout, ack); err != nil {
		return err
	}
	_ = stdin.Close()
	return session.Wait()
}
