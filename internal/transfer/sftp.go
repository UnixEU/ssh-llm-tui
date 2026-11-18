package transfer

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"sync"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	"github.com/charmbracelet/ssh"
	"github.com/pkg/sftp"
)

// Subsystem is the OpenSSH sftp subsystem handler. Clients only see their
// own flat directory: put, list, stat, get, and remove.
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
		if err != nil && !errors.Is(err, io.EOF) {
			log.Printf("WARN sftp user=%s op=serve err=%v", sess.User(), err)
		}
		_ = srv.Close()
	}
}

type sftpHandler struct {
	store *uploads.Store
	user  string
}

func (h sftpHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	name, dir, err := uploadTarget(r.Filepath)
	if err != nil || dir {
		h.warn("read", r.Filepath, errOr(err, os.ErrNotExist))
		return nil, os.ErrNotExist
	}
	data, err := h.store.Read(h.user, name)
	if err != nil {
		h.warn("read", name, err)
		return nil, os.ErrNotExist
	}
	h.info("read", name, int64(len(data)))
	return bytes.NewReader(data), nil
}

func (h sftpHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	name, dir, err := uploadTarget(r.Filepath)
	if err != nil || dir {
		h.warn("write", r.Filepath, errOr(err, os.ErrPermission))
		if err == nil {
			err = os.ErrPermission
		}
		return nil, err
	}
	h.info("write-open", name, 0)
	return &putAt{store: h.store, user: h.user, name: name, max: h.store.MaxBytes()}, nil
}

func (h sftpHandler) Filecmd(r *sftp.Request) error {
	switch r.Method {
	case "Setstat":
		h.info("setstat", r.Filepath, 0)
		return nil
	case "Mkdir":
		_, dir, err := uploadTarget(r.Filepath)
		if err != nil || !dir {
			h.warn("mkdir", r.Filepath, os.ErrPermission)
			return os.ErrPermission
		}
		h.info("mkdir", r.Filepath, 0)
		return nil
	case "Remove", "Rmdir":
		name, dir, err := uploadTarget(r.Filepath)
		if err != nil || dir {
			h.warn("remove", r.Filepath, errOr(err, os.ErrPermission))
			if err != nil {
				return err
			}
			return os.ErrPermission
		}
		if err := h.store.Remove(h.user, name); err != nil {
			h.warn("remove", name, err)
			return err
		}
		h.info("remove", name, 0)
		return nil
	case "Rename", "PosixRename":
		from, fromDir, err := uploadTarget(r.Filepath)
		if err != nil || fromDir {
			h.warn("rename", r.Filepath, errOr(err, os.ErrPermission))
			return os.ErrPermission
		}
		to, toDir, err := uploadTarget(r.Target)
		if err != nil || toDir {
			h.warn("rename", r.Target, errOr(err, os.ErrPermission))
			return os.ErrPermission
		}
		data, err := h.store.Read(h.user, from)
		if err != nil {
			h.warn("rename", from, err)
			return err
		}
		if err := h.store.Put(h.user, to, data); err != nil {
			h.warn("rename", to, err)
			return err
		}
		if from != to {
			_ = h.store.Remove(h.user, from)
		}
		h.info("rename", from+" -> "+to, int64(len(data)))
		return nil
	default:
		h.warn(r.Method, r.Filepath, os.ErrPermission)
		return os.ErrPermission
	}
}

func (h sftpHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
		name, dir, err := uploadTarget(r.Filepath)
		if err != nil {
			h.warn("list", r.Filepath, err)
			return nil, err
		}
		if !dir {
			h.warn("list", name, os.ErrPermission)
			return nil, os.ErrPermission
		}
		h.info("list", r.Filepath, int64(len(h.store.List(h.user))))
		return h.listAll(), nil
	case "Stat", "Lstat":
		name, dir, err := uploadTarget(r.Filepath)
		if err != nil {
			h.warn("stat", r.Filepath, err)
			return nil, err
		}
		if dir {
			h.info("stat", r.Filepath, 0)
			return fileList{sftpInfo{name: "uploads", dir: true}}, nil
		}
		for _, f := range h.store.List(h.user) {
			if f.Name == name {
				h.info("stat", name, f.Size)
				return fileList{sftpInfo{name: f.Name, size: f.Size}}, nil
			}
		}
		h.warn("stat", name, os.ErrNotExist)
		return nil, os.ErrNotExist
	default:
		h.warn(r.Method, r.Filepath, os.ErrPermission)
		return nil, os.ErrPermission
	}
}

// RealPath makes clients see the upload directory as /uploads.
func (h sftpHandler) RealPath(p string) (string, error) {
	name, dir, err := uploadTarget(p)
	if err != nil {
		h.warn("realpath", p, err)
		return "", err
	}
	if dir {
		h.info("realpath", p, 0)
		return "/uploads", nil
	}
	h.info("realpath", name, 0)
	return "/uploads/" + name, nil
}

func (h sftpHandler) listAll() fileList {
	files := h.store.List(h.user)
	out := make(fileList, 0, len(files))
	for _, f := range files {
		out = append(out, sftpInfo{name: f.Name, size: f.Size})
	}
	return out
}

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
}

func (p *putAt) WriteAt(b []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fs.ErrInvalid
	}
	end := off + int64(len(b))
	if end < off || end > p.max || end > int64(^uint(0)>>1) {
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
	data := append([]byte(nil), p.buf[:int(p.size)]...)
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
