// Package transfer is the SCP and SFTP front of the upload store.
// Both use the same SSH listener and the same auth as the TUI.
package transfer

import (
	"bytes"
	"io"
	"io/fs"
	"log"
	"path/filepath"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/scp"
)

// SCPHandler serves scp -t (upload) and scp -f (download of that user's files).
type SCPHandler struct {
	Store *uploads.Store
}

// Middleware is the wish middleware. Non-scp sessions fall through.
func Middleware(store *uploads.Store) wish.Middleware {
	h := SCPHandler{Store: store}
	return scp.Middleware(h, h)
}

func (h SCPHandler) Write(s ssh.Session, entry *scp.FileEntry) (int64, error) {
	name, _, err := uploadTarget(entry.Name)
	if err != nil || name == "" {
		name, _, err = uploadTarget(entry.Filepath)
	}
	if err != nil || name == "" {
		log.Printf("WARN scp user=%s op=write name=%q err=%v", s.User(), entry.Filepath, err)
		if err == nil {
			err = fs.ErrInvalid
		}
		return 0, err
	}
	if entry.Size > h.Store.MaxBytes() {
		log.Printf("WARN scp user=%s op=write name=%q bytes=%d err=too large", s.User(), name, entry.Size)
		return 0, fs.ErrInvalid
	}
	buf := make([]byte, entry.Size)
	n, err := io.ReadFull(entry.Reader, buf)
	if err != nil {
		log.Printf("WARN scp user=%s op=write name=%q bytes=%d err=%v", s.User(), name, n, err)
		return int64(n), err
	}
	if err := h.Store.Put(s.User(), name, buf); err != nil {
		log.Printf("WARN scp user=%s op=write name=%q bytes=%d err=%v", s.User(), name, entry.Size, err)
		return 0, err
	}
	log.Printf("INFO scp user=%s op=write name=%q bytes=%d path=%q", s.User(), name, entry.Size, entry.Filepath)
	return entry.Size, nil
}

func (h SCPHandler) Mkdir(ssh.Session, *scp.DirEntry) error {
	// The store is flat. Directory commands from clients are ignored so a
	// recursive upload still lands each file by its base name.
	return nil
}

func (h SCPHandler) Glob(s ssh.Session, pattern string) ([]string, error) {
	base := filepath.Base(pattern)
	if pattern == "" || pattern == "." || pattern == "*" || base == "." || base == "*" {
		files := h.Store.List(s.User())
		names := make([]string, 0, len(files))
		for _, f := range files {
			names = append(names, f.Name)
		}
		return names, nil
	}
	return []string{base}, nil
}

func (h SCPHandler) WalkDir(s ssh.Session, path string, fn fs.WalkDirFunc) error {
	names, err := h.Glob(s, path)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := fn(name, fileDirEntry(name), nil); err != nil {
			return err
		}
	}
	return nil
}

func (h SCPHandler) NewDirEntry(ssh.Session, string) (*scp.DirEntry, error) {
	return &scp.DirEntry{Name: ".", Filepath: ".", Mode: 0o755}, nil
}

func (h SCPHandler) NewFileEntry(s ssh.Session, path string) (*scp.FileEntry, func() error, error) {
	name := filepath.Base(path)
	data, err := h.Store.Read(s.User(), name)
	if err != nil {
		return nil, nil, err
	}
	return &scp.FileEntry{
		Name:     name,
		Filepath: name,
		Mode:     0o644,
		Size:     int64(len(data)),
		Reader:   bytes.NewReader(data),
		Mtime:    time.Now().Unix(),
		Atime:    time.Now().Unix(),
	}, func() error { return nil }, nil
}

type fileDirEntry string

func (f fileDirEntry) Name() string               { return string(f) }
func (f fileDirEntry) IsDir() bool                { return false }
func (f fileDirEntry) Type() fs.FileMode          { return 0 }
func (f fileDirEntry) Info() (fs.FileInfo, error) { return fileInfo{name: string(f)}, nil }

type fileInfo struct {
	name string
	size int64
}

func (f fileInfo) Name() string       { return f.name }
func (f fileInfo) Size() int64        { return f.size }
func (f fileInfo) Mode() fs.FileMode  { return 0o644 }
func (f fileInfo) ModTime() time.Time { return time.Time{} }
func (f fileInfo) IsDir() bool        { return false }
func (f fileInfo) Sys() any           { return nil }
