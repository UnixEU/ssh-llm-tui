// Package downloads holds files prepared in the TUI for the user to fetch
// over SCP or SFTP: edited files, a patch and tar.gz archives. Clients can
// read and delete them, never write. Like uploads, everything of a user is
// removed when that user's last TUI session ends, and on process start.
package downloads

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
)

// DefaultMaxTotalBytes caps one user's downloads (files, patch, archives).
const DefaultMaxTotalBytes = 200 << 20

// ErrReadOnly is returned for client writes into downloads.
var ErrReadOnly = fmt.Errorf("downloads are read-only: %w", fs.ErrPermission)

// Entry is one directory listing item.
type Entry struct {
	Name  string
	Path  string // relative to the user's downloads, "" for the root
	IsDir bool
	Size  int64
}

// Store is safe for concurrent TUI, SCP and SFTP sessions.
type Store struct {
	mu       sync.Mutex
	dir      string
	maxTotal int64
	sessions map[string]int
}

// New returns a store rooted at dir and deletes anything already there.
// An empty dir uses /downloads. Non-positive maxTotal uses the default.
func New(dir string, maxTotal int64) *Store {
	if strings.TrimSpace(dir) == "" {
		dir = "/downloads"
	}
	if maxTotal <= 0 {
		maxTotal = DefaultMaxTotalBytes
	}
	s := &Store{dir: dir, maxTotal: maxTotal, sessions: map[string]int{}}
	_ = s.Wipe()
	return s
}

// Dir is the on-disk root.
func (s *Store) Dir() string { return s.dir }

// MaxTotalBytes is the per-user cap.
func (s *Store) MaxTotalBytes() int64 { return s.maxTotal }

// Wipe removes every user's downloads.
func (s *Store) Wipe() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(s.dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// SessionEnter counts a TUI session for user.
func (s *Store) SessionEnter(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[userKey(user)]++
}

// SessionLeave drops one TUI session. The last one deletes the user's
// downloads, the same moment uploads are deleted.
func (s *Store) SessionLeave(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := userKey(user)
	if s.sessions[key] > 0 {
		s.sessions[key]--
	}
	if s.sessions[key] == 0 {
		delete(s.sessions, key)
		_ = os.RemoveAll(s.userDir(user))
	}
}

// Clear deletes every download of user.
func (s *Store) Clear(user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.RemoveAll(s.userDir(user))
}

func (s *Store) userDir(user string) string { return filepath.Join(s.dir, userKey(user)) }

// clean validates a relative path. "" is the root.
func clean(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", nil
	}
	return uploads.CleanPath(name)
}

// resolve returns the on-disk path of rel after checking that nothing on
// the way is a symlink.
func (s *Store) resolve(user, rel string) (string, error) {
	root := s.userDir(user)
	if rel == "" {
		return root, nil
	}
	cur := root
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return cur, fs.ErrNotExist
			}
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return "", fmt.Errorf("%s: symlink: %w", rel, fs.ErrPermission)
		}
		if i < len(parts)-1 && !fi.IsDir() {
			return "", fmt.Errorf("%s: not a directory: %w", rel, fs.ErrNotExist)
		}
	}
	return cur, nil
}

// Stat describes a file or directory. The root always exists.
func (s *Store) Stat(user, name string) (Entry, error) {
	rel, err := clean(name)
	if err != nil {
		return Entry{}, err
	}
	if rel == "" {
		return Entry{IsDir: true}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.resolve(user, rel)
	if err != nil {
		return Entry{}, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{Name: path.Base(rel), Path: rel, IsDir: fi.IsDir()}
	if !e.IsDir {
		e.Size = fi.Size()
	}
	return e, nil
}

// ReadDir lists a directory, sorted by name. Unfinished ".part" files are
// hidden.
func (s *Store) ReadDir(user, name string) ([]Entry, error) {
	rel, err := clean(name)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.resolve(user, rel)
	if err != nil {
		if rel == "" && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	items, err := os.ReadDir(p)
	if err != nil {
		if rel == "" && errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Entry
	for _, it := range items {
		if strings.HasSuffix(it.Name(), partSuffix) || it.Type()&fs.ModeSymlink != 0 {
			continue
		}
		e := Entry{Name: it.Name(), Path: path.Join(rel, it.Name()), IsDir: it.IsDir()}
		if !e.IsDir {
			if fi, err := it.Info(); err == nil {
				e.Size = fi.Size()
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Open opens a file for reading. The caller closes it.
func (s *Store) Open(user, name string) (*os.File, int64, error) {
	rel, err := clean(name)
	if err != nil {
		return nil, 0, err
	}
	if rel == "" {
		return nil, 0, fmt.Errorf("downloads: is a directory")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.resolve(user, rel)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		f.Close()
		if err == nil {
			err = fmt.Errorf("%s: is a directory", rel)
		}
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

// Read returns a whole file.
func (s *Store) Read(user, name string) ([]byte, error) {
	f, _, err := s.Open(user, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readAll(f)
}

// Remove deletes one file.
func (s *Store) Remove(user, name string) error {
	rel, err := clean(name)
	if err != nil {
		return err
	}
	if rel == "" {
		return fmt.Errorf("downloads: is a directory")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.resolve(user, rel)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s: is a directory", rel)
	}
	return os.Remove(p)
}

// RemoveDir deletes a directory and everything below it. "" empties the
// user's downloads.
func (s *Store) RemoveDir(user, name string) error {
	rel, err := clean(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.resolve(user, rel)
	if err != nil {
		if rel == "" && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		if rel == "" && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s: not a directory", rel)
	}
	return os.RemoveAll(p)
}

// Usage is the total size of user's downloads.
func (s *Store) Usage(user string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usageLocked(user, "")
}

// usageLocked sums file sizes under rel, skipping the subtree skip.
func (s *Store) usageLocked(user, skip string) int64 {
	root := s.userDir(user)
	var n int64
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if skip != "" {
			if rel, rerr := filepath.Rel(root, p); rerr == nil && (filepath.ToSlash(rel) == skip || strings.HasPrefix(filepath.ToSlash(rel), skip+"/")) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

func userKey(user string) string {
	user = strings.TrimSpace(user)
	if user == "" {
		return "anonymous"
	}
	var b strings.Builder
	for _, r := range user {
		if r <= 32 || r == 127 || r == '/' || r == '\\' || unicode.IsControl(r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	key := strings.ReplaceAll(b.String(), "..", "_")
	if key == "" || key == "." || key == ".." {
		return "anonymous"
	}
	return key
}
