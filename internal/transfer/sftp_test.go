package transfer

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	"github.com/pkg/sftp"
)

func TestPutAtConcurrentOutOfOrder(t *testing.T) {
	const size = 3 << 20
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	store := uploads.New(t.TempDir(), 8<<20, 4)
	h := sftpHandler{store: store, user: "user"}
	w, err := h.Filewrite(sftp.NewRequest("Put", "upload.bin"))
	if err != nil {
		t.Fatal(err)
	}
	const chunk = 32 << 10
	type span struct {
		off int64
		b   []byte
	}
	var spans []span
	for off := 0; off < len(payload); off += chunk {
		end := off + chunk
		if end > len(payload) {
			end = len(payload)
		}
		spans = append(spans, span{off: int64(off), b: append([]byte(nil), payload[off:end]...)})
	}
	// Highest offsets go out first so the buffer has to grow while earlier
	// chunks are still in flight.
	for i, j := 0, len(spans)-1; i < j; i, j = i+1, j-1 {
		spans[i], spans[j] = spans[j], spans[i]
	}
	var wg sync.WaitGroup
	errCh := make(chan error, len(spans))
	for _, sp := range spans {
		wg.Add(1)
		go func(off int64, b []byte) {
			defer wg.Done()
			if _, err := w.WriteAt(b, off); err != nil {
				errCh <- err
			}
		}(sp.off, sp.b)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if err := w.(io.Closer).Close(); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read("user", "upload.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("upload mismatch: stored %d bytes, want %d, first difference at %d", len(got), len(payload), firstDiff(got, payload))
	}
}

func TestGrowCapDoublesUntilLimit(t *testing.T) {
	if got := growCap(0, 100, 8<<20); got != 32<<10 {
		t.Fatalf("small %d", got)
	}
	if got := growCap(32<<10, 40<<10, 8<<20); got != 64<<10 {
		t.Fatalf("double %d", got)
	}
	if got := growCap(6<<20, 7<<20, 8<<20); got != 8<<20 {
		t.Fatalf("cap at max %d", got)
	}
	if got := growCap(0, 10, 1000); got != 1000 {
		t.Fatalf("small max %d", got)
	}
}

func TestSFTPClientConcurrentReadFrom(t *testing.T) {
	const size = 3 << 20
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	store := uploads.New(t.TempDir(), 8<<20, 4)
	client := startHandlerClient(t, store)
	f, err := client.Create("payload.bin")
	if err != nil {
		t.Fatal(err)
	}
	n, err := f.ReadFrom(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("read %d, want %d", n, len(payload))
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read("user", "payload.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("upload mismatch: stored %d bytes, want %d, first difference at %d", len(got), len(payload), firstDiff(got, payload))
	}
}

func firstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

func startHandlerClient(t *testing.T, store *uploads.Store) *sftp.Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "sftp.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	serveErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serveErr <- err
			return
		}
		h := sftpHandler{store: store, user: "user"}
		srv := sftp.NewRequestServer(conn, sftp.Handlers{
			FileGet:  h,
			FilePut:  h,
			FileCmd:  h,
			FileList: h,
		})
		serveErr <- srv.Serve()
	}()
	t.Cleanup(func() { _ = ln.Close() })

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sftp.NewClientPipe(conn, conn,
		sftp.MaxPacket(32<<10),
		sftp.MaxConcurrentRequestsPerFile(64),
		sftp.UseConcurrentWrites(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}
