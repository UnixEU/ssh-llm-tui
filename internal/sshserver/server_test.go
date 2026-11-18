package sshserver

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/UnixEU/ssh-llm-tui/internal/gpu"
	"github.com/UnixEU/ssh-llm-tui/internal/ui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish/bubbletea"
	gossh "golang.org/x/crypto/ssh"
)

func TestOpenSessionReachesEmptyModelList(t *testing.T) {
	dmrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/engines/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer dmrSrv.Close()

	cfg, err := config.Load(config.LoadOptions{Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	cfg.DMR.BaseURL = dmrSrv.URL
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = 2222
	cfg.Server.AuthMode = "open"
	cfg.Server.HostKeyPath = filepath.Join(t.TempDir(), "keys", "ssh_host_ed25519_key")

	client, err := dmr.New(dmr.Options{BaseURL: cfg.DMR.BaseURL, RequestTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	gate := gpu.New()

	srv, err := New(cfg.Server, func(sess ssh.Session) (tea.Model, []tea.ProgramOption) {
		model := ui.New(ui.Options{
			Config:   cfg,
			Client:   client,
			Gate:     gate,
			Renderer: bubbletea.MakeRenderer(sess),
			User:     sess.User(),
			Version:  "0.1.0",
			Session:  sess.Context(),
		})
		return model, []tea.ProgramOption{tea.WithAltScreen(), tea.WithoutSignalHandler()}
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	conn, err := gossh.Dial("tcp", ln.Addr().String(), &gossh.ClientConfig{
		User:            "user",
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	session, err := conn.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if err := session.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{
		gossh.ECHO: 1,
	}); err != nil {
		t.Fatal(err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Shell(); err != nil {
		t.Fatal(err)
	}

	var buf safeBuf
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, stdout)
		close(done)
	}()

	// Wish queries background color and device attributes before the first
	// frame. Answer them, or that read can sit until its own timeout.
	waitFor(t, &buf, "\x1b]11;?", 4*time.Second)
	if _, err := io.WriteString(stdin, "\x1b]11;rgb:0000/0000/0000\x07\x1b[?1;2c"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, &buf, "ssh-llm-tui", 4*time.Second)
	if _, err := io.WriteString(stdin, "\r"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, &buf, "No models are pulled", 4*time.Second)

	_ = session.Close()
	_ = conn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

func waitFor(t *testing.T, buf *safeBuf, needle string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if bytes.Contains(buf.Bytes(), []byte(needle)) {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q\noutput:\n%s", needle, buf.String())
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuf) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.b.Bytes()...)
}

func (s *safeBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
