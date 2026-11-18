// Package transfer is the SCP and SFTP front of the upload store.
// Both use the same SSH listener and the same auth as the TUI.
package transfer

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"log"
	"path"
	"strings"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/scp"
)

// SCPHandler serves the legacy scp protocol: scp -t (upload, with -r for
// directory trees) and scp -f (download of that user's files and
// directories).
type SCPHandler struct {
	Store *uploads.Store
}

// Middleware is the wish middleware. Non-scp sessions fall through.
func Middleware(store *uploads.Store) wish.Middleware {
	h := SCPHandler{Store: store}
	return scp.Middleware(h, h)
}

// target resolves where an incoming file goes. entry.Filepath is the scp
// target joined with the directories the client opened and the file name.
// For a plain "scp file host:uploads/new.txt" that path is
// "uploads/new.txt/file"; the parent is not a directory, so the target is
// the file name the client asked for, as with a regular scp server.
func (h SCPHandler) target(s ssh.Session, entry *scp.FileEntry) (string, error) {
	rel, err := uploadTarget(entry.Filepath)
	if err != nil {
		return "", err
	}
	rel = remapped(s, rel)
	if rel == "" {
		return "", errIsDir
	}
	parent := path.Dir(rel)
	if parent == "." || scp.GetInfo(s.Command()).Recursive {
		return rel, nil
	}
	if st, err := h.Store.Stat(s.User(), parent); err == nil && st.IsDir {
		return rel, nil
	}
	return parent, nil
}

// Write reads this file's SCP stream to completion. The protocol delivers
// one file at a time on the session, so the buffer is not shared across
// goroutines the way an SFTP WriteAt is.
func (h SCPHandler) Write(s ssh.Session, entry *scp.FileEntry) (int64, error) {
	name, err := h.target(s, entry)
	if err != nil {
		log.Printf("WARN scp user=%s op=write name=%q err=%v", s.User(), entry.Filepath, err)
		return 0, err
	}
	if h.Store.Discard(s.User(), name, "write") {
		// Read the bytes off the stream so the next header lines up.
		n, err := io.Copy(io.Discard, entry.Reader)
		return n, err
	}
	if entry.Size > h.Store.MaxBytes() {
		err := fmt.Errorf("%s: %d bytes exceeds the per-file limit of %d bytes (UPLOAD_MAX_BYTES)", name, entry.Size, h.Store.MaxBytes())
		log.Printf("WARN scp user=%s op=write name=%q err=%v", s.User(), name, err)
		return 0, err
	}
	if err := h.Store.Precheck(s.User(), name); err != nil {
		log.Printf("WARN scp user=%s op=write name=%q err=%v", s.User(), name, err)
		return 0, err
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
	log.Printf("INFO scp user=%s op=write name=%q bytes=%d", s.User(), name, entry.Size)
	return entry.Size, nil
}

// remapKey stores, per session, the rename of the top directory of a
// recursive upload into a target that does not exist yet.
type remapKey struct{}

type remap struct{ from, to string }

// remapped applies the session's top-directory rename to rel.
func remapped(s ssh.Session, rel string) string {
	r, ok := s.Context().Value(remapKey{}).(remap)
	if !ok {
		return rel
	}
	if rel == r.from {
		return r.to
	}
	if strings.HasPrefix(rel, r.from+"/") {
		return r.to + rel[len(r.from):]
	}
	return rel
}

// Mkdir creates the directory the client opened with a D line. As with
// a regular scp server, "scp -r dir host:uploads/new" creates "new" with
// the contents of dir when "new" does not exist, and "new/dir" when it does.
func (h SCPHandler) Mkdir(s ssh.Session, entry *scp.DirEntry) error {
	rel, err := uploadTarget(entry.Filepath)
	if err == nil && rel == "" {
		return nil
	}
	if err == nil {
		if _, set := s.Context().Value(remapKey{}).(remap); !set {
			target, terr := uploadTarget(scp.GetInfo(s.Command()).Path)
			if terr == nil && target != "" && rel == path.Join(target, entry.Name) {
				if st, serr := h.Store.Stat(s.User(), target); serr != nil || !st.IsDir {
					s.Context().SetValue(remapKey{}, remap{from: rel, to: target})
				} else {
					// The target exists: keep paths as they are.
					s.Context().SetValue(remapKey{}, remap{from: rel, to: rel})
				}
			}
		}
		rel = remapped(s, rel)
	}
	if err == nil {
		err = h.Store.Mkdir(s.User(), rel)
	}
	if err != nil {
		log.Printf("WARN scp user=%s op=mkdir name=%q err=%v", s.User(), entry.Filepath, err)
		return err
	}
	log.Printf("INFO scp user=%s op=mkdir name=%q", s.User(), rel)
	return nil
}

// Glob resolves the path of scp -f into store paths. The root and a
// trailing wildcard expand to the matching children of that directory.
// Without -r only files are returned. Matches come in reverse name order,
// see WalkDir.
func (h SCPHandler) Glob(s ssh.Session, pattern string) ([]string, error) {
	recursive := scp.GetInfo(s.Command()).Recursive
	user := s.User()
	dir, match := "", "*"
	if rel, err := uploadTarget(pattern); err != nil {
		return nil, err
	} else if rel != "" {
		base := path.Base(rel)
		if strings.ContainsAny(base, "*?[") {
			dir, match = path.Dir(rel), base
			if dir == "." {
				dir = ""
			}
		} else {
			st, err := h.Store.Stat(user, rel)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", rel, err)
			}
			if st.IsDir && !recursive {
				return nil, fmt.Errorf("%s: is a directory, use scp -r", rel)
			}
			return []string{rel}, nil
		}
	}
	entries, err := h.Store.ReadDir(user, dir)
	if err != nil {
		return nil, err
	}
	// Reverse name order, for the same reason as in WalkDir.
	var out []string
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.IsDir && !recursive {
			continue
		}
		if ok, _ := path.Match(match, e.Name); ok {
			out = append(out, e.Path)
		}
	}
	return out, nil
}

// WalkDir visits p and, for a directory, everything below it. Each
// directory comes before its children and siblings come in reverse name
// order. The scp encoder files an entry under the first sibling whose
// path is a string prefix of the entry's parent, so "ab/x" would land in
// "a" if "a" came first.
func (h SCPHandler) WalkDir(s ssh.Session, p string, fn fs.WalkDirFunc) error {
	st, err := h.Store.Stat(s.User(), p)
	if err != nil {
		return fn(p, nil, err)
	}
	if !st.IsDir {
		return fn(p, fileDirEntry(path.Base(p)), nil)
	}
	if err := fn(p, dirDirEntry(path.Base(p)), nil); err != nil {
		return err
	}
	entries, err := h.Store.ReadDir(s.User(), p)
	if err != nil {
		return err
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if err := h.WalkDir(s, entries[i].Path, fn); err != nil {
			return err
		}
	}
	return nil
}

func (h SCPHandler) NewDirEntry(_ ssh.Session, p string) (*scp.DirEntry, error) {
	return &scp.DirEntry{Name: path.Base(p), Filepath: p, Mode: 0o755}, nil
}

func (h SCPHandler) NewFileEntry(s ssh.Session, p string) (*scp.FileEntry, func() error, error) {
	rel, err := uploadTarget(p)
	if err != nil {
		return nil, nil, err
	}
	data, err := h.Store.Read(s.User(), rel)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().Unix()
	return &scp.FileEntry{
		Name:     path.Base(rel),
		Filepath: rel,
		Mode:     0o644,
		Size:     int64(len(data)),
		Reader:   bytes.NewReader(data),
		Mtime:    now,
		Atime:    now,
	}, func() error { return nil }, nil
}

type fileDirEntry string

func (f fileDirEntry) Name() string               { return string(f) }
func (f fileDirEntry) IsDir() bool                { return false }
func (f fileDirEntry) Type() fs.FileMode          { return 0 }
func (f fileDirEntry) Info() (fs.FileInfo, error) { return sftpInfo{name: string(f)}, nil }

type dirDirEntry string

func (d dirDirEntry) Name() string               { return string(d) }
func (d dirDirEntry) IsDir() bool                { return true }
func (d dirDirEntry) Type() fs.FileMode          { return fs.ModeDir }
func (d dirDirEntry) Info() (fs.FileInfo, error) { return sftpInfo{name: string(d), dir: true}, nil }
