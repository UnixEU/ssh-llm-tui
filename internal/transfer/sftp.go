package transfer

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"sync"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	"github.com/charmbracelet/ssh"
	"github.com/pkg/sftp"
)

// Subsystem is the OpenSSH sftp subsystem handler. Clients only see their
// own upload directory, with subdirectories: put, get, list, stat, mkdir,
// rmdir, remove and rename.
func Subsystem(store *uploads.Store) ssh.SubsystemHandler {
	return func(sess ssh.Session) {
		h := sftpHandler{store: store, user: sess.User()}
		srv := sftp.NewRequestServer(sess, sftp.Handlers{
			FileGet:  h,
			FilePut:  h,
			FileCmd:  h,
			FileList: h,
		})
		err := srv.Serve()
		code := 0
		if err != nil && !errors.Is(err, io.EOF) {
			log.Printf("WARN sftp user=%s op=serve err=%v", sess.User(), err)
			code = 1
		}
		// Send the exit status before the channel closes. Without it
		// OpenSSH clients report -1 and scp exits 1 even when every file
		// went through.
		_ = sess.Exit(code)
		_ = srv.Close()
	}
}

type sftpHandler struct {
	store *uploads.Store
	user  string
}

// clientErr maps store errors onto the SFTP status codes clients expect.
// The full error is logged by the caller.
func clientErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return os.ErrNotExist
	case errors.Is(err, uploads.ErrInvalidPath), errors.Is(err, fs.ErrPermission):
		return os.ErrPermission
	}
	return err
}

func (h sftpHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	rel, err := uploadTarget(r.Filepath)
	if err != nil || rel == "" {
		h.warn("read", r.Filepath, errOr(err, errIsDir))
		return nil, os.ErrNotExist
	}
	data, err := h.store.Read(h.user, rel)
	if err != nil {
		h.warn("read", rel, err)
		return nil, os.ErrNotExist
	}
	h.info("read", rel, int64(len(data)))
	return bytes.NewReader(data), nil
}

func (h sftpHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	rel, err := uploadTarget(r.Filepath)
	if err != nil || rel == "" {
		err = errOr(err, errIsDir)
		h.warn("write", r.Filepath, err)
		return nil, clientErr(err)
	}
	if h.store.Discard(h.user, rel, "write") {
		return discardAt{}, nil
	}
	if err := h.store.Precheck(h.user, rel); err != nil {
		h.warn("write", rel, err)
		return nil, clientErr(err)
	}
	h.info("write-open", rel, 0)
	return &putAt{store: h.store, user: h.user, name: rel, max: h.store.MaxBytes()}, nil
}

func (h sftpHandler) Filecmd(r *sftp.Request) error {
	switch r.Method {
	case "Setstat":
		return nil
	case "Mkdir":
		rel, err := uploadTarget(r.Filepath)
		if err == nil && rel == "" {
			return nil
		}
		if err == nil {
			err = h.store.Mkdir(h.user, rel)
		}
		if err != nil {
			h.warn("mkdir", r.Filepath, err)
			return clientErr(err)
		}
		h.info("mkdir", rel, 0)
		return nil
	case "Remove":
		rel, err := uploadTarget(r.Filepath)
		if err == nil && rel == "" {
			err = errIsDir
		}
		if err == nil {
			err = h.store.Remove(h.user, rel)
		}
		if err != nil {
			h.warn("remove", r.Filepath, err)
			return clientErr(err)
		}
		h.info("remove", rel, 0)
		return nil
	case "Rmdir":
		rel, err := uploadTarget(r.Filepath)
		if err == nil && rel == "" {
			err = errRoot
		}
		if err == nil {
			err = h.store.RemoveDir(h.user, rel)
		}
		if err != nil {
			h.warn("rmdir", r.Filepath, err)
			return clientErr(err)
		}
		h.info("rmdir", rel, 0)
		return nil
	case "Rename", "PosixRename":
		from, err := uploadTarget(r.Filepath)
		if err == nil && from == "" {
			err = errRoot
		}
		var to string
		if err == nil {
			to, err = uploadTarget(r.Target)
			if err == nil && to == "" {
				err = errRoot
			}
		}
		if err == nil {
			err = h.store.Rename(h.user, from, to)
		}
		if err != nil {
			h.warn("rename", r.Filepath+" -> "+r.Target, err)
			return clientErr(err)
		}
		h.info("rename", from+" -> "+to, 0)
		return nil
	default:
		h.warn(r.Method, r.Filepath, os.ErrPermission)
		return os.ErrPermission
	}
}

func (h sftpHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
		rel, err := uploadTarget(r.Filepath)
		if err != nil {
			h.warn("list", r.Filepath, err)
			return nil, clientErr(err)
		}
		st, err := h.store.Stat(h.user, rel)
		if err != nil {
			h.warn("list", r.Filepath, err)
			return nil, clientErr(err)
		}
		if !st.IsDir {
			return fileList{sftpInfo{name: st.Name, size: st.Size}}, nil
		}
		entries, err := h.store.ReadDir(h.user, rel)
		if err != nil {
			h.warn("list", r.Filepath, err)
			return nil, clientErr(err)
		}
		out := make(fileList, 0, len(entries))
		for _, e := range entries {
			out = append(out, sftpInfo{name: e.Name, size: e.Size, dir: e.IsDir})
		}
		h.info("list", r.Filepath, int64(len(out)))
		return out, nil
	case "Stat", "Lstat":
		rel, err := uploadTarget(r.Filepath)
		if err != nil {
			h.warn("stat", r.Filepath, err)
			return nil, clientErr(err)
		}
		if rel == "" {
			return fileList{sftpInfo{name: "uploads", dir: true}}, nil
		}
		st, err := h.store.Stat(h.user, rel)
		if err != nil {
			// Clients stat targets before creating them, so a miss is normal.
			return nil, clientErr(err)
		}
		return fileList{sftpInfo{name: st.Name, size: st.Size, dir: st.IsDir}}, nil
	default:
		h.warn(r.Method, r.Filepath, os.ErrPermission)
		return nil, os.ErrPermission
	}
}

// RealPath makes clients see the upload directory as /uploads.
func (h sftpHandler) RealPath(p string) (string, error) {
	rel, err := uploadTarget(p)
	if err != nil {
		h.warn("realpath", p, err)
		return "", clientErr(err)
	}
	return path.Join("/uploads", rel), nil
}

var (
	errIsDir = errors.New("is a directory")
	errRoot  = errors.New("the upload root cannot be removed or renamed")
)

func (h sftpHandler) info(op, name string, n int64) {
	log.Printf("INFO sftp user=%s op=%s name=%q bytes=%d", h.user, op, name, n)
}

func (h sftpHandler) warn(op, name string, err error) {
	log.Printf("WARN sftp user=%s op=%s name=%q err=%v", h.user, op, name, err)
}

func errOr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// discardAt accepts and drops the bytes of an ignored file.
type discardAt struct{}

func (discardAt) WriteAt(b []byte, off int64) (int, error) { return len(b), nil }
func (discardAt) Close() error                             { return nil }

// putAt buffers one upload. The request server runs WriteAt on a worker
// pool, so several chunks of the same file are in flight together. Close
// is dispatched only after those writes have returned. Each chunk is
// copied under the lock before WriteAt returns.
type putAt struct {
	mu     sync.Mutex
	store  *uploads.Store
	user   string
	name   string
	max    int64
	buf    []byte
	size   int64
	closed bool
	// failed is set when a chunk was refused. Close then stores nothing,
	// so a partial file never lands in the store.
	failed bool
	tooBig sync.Once
}

func (p *putAt) WriteAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fs.ErrInvalid
	}
	end := off + int64(len(b))
	if end < off || end > p.max || end > int64(^uint(0)>>1) {
		p.mu.Lock()
		p.failed = true
		p.mu.Unlock()
		p.tooBig.Do(func() {
			log.Printf("WARN sftp user=%s op=write name=%q bytes=%d err=exceeds the per-file limit of %d bytes (UPLOAD_MAX_BYTES)", p.user, p.name, end, p.max)
		})
		return 0, fs.ErrInvalid
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, fs.ErrClosed
	}
	offN, endN := int(off), int(end)
	if end > int64(len(p.buf)) {
		if end > int64(cap(p.buf)) {
			next := make([]byte, endN, int(growCap(int64(cap(p.buf)), end, p.max)))
			copy(next, p.buf)
			p.buf = next
		} else {
			p.buf = p.buf[:endN]
		}
	}
	copy(p.buf[offN:endN], b)
	if end > p.size {
		p.size = end
	}
	return len(b), nil
}

func (p *putAt) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	if p.failed {
		p.buf = nil
		p.mu.Unlock()
		return fmt.Errorf("%s: upload refused, nothing stored", p.name)
	}
	data := append([]byte(nil), p.buf[:int(p.size)]...)
	p.buf = nil
	p.mu.Unlock()
	if err := p.store.Put(p.user, p.name, data); err != nil {
		log.Printf("WARN sftp user=%s op=write name=%q bytes=%d err=%v", p.user, p.name, len(data), err)
		return err
	}
	log.Printf("INFO sftp user=%s op=write name=%q bytes=%d", p.user, p.name, len(data))
	return nil
}

// growCap picks the next slice capacity. It doubles until need is covered
// so a long upload does not copy the whole buffer on every chunk.
func growCap(cur, need, max int64) int64 {
	if need > max {
		need = max
	}
	n := cur
	if n < 32<<10 {
		n = 32 << 10
	}
	if n > max {
		n = max
	}
	for n < need {
		if n > max/2 {
			return max
		}
		n *= 2
	}
	return n
}

type fileList []fs.FileInfo

func (f fileList) ListAt(dst []fs.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(f)) {
		return 0, io.EOF
	}
	n := copy(dst, f[offset:])
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}

type sftpInfo struct {
	name string
	size int64
	dir  bool
}

func (f sftpInfo) Name() string { return f.name }
func (f sftpInfo) Size() int64  { return f.size }
func (f sftpInfo) Mode() fs.FileMode {
	if f.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (f sftpInfo) ModTime() time.Time { return time.Time{} }
func (f sftpInfo) IsDir() bool        { return f.dir }
func (f sftpInfo) Sys() any           { return nil }
