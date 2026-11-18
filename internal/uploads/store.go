// Package uploads stores files received over SCP and SFTP, one set per SSH
// username. Files keep their relative path, so a whole directory tree can be
// uploaded. Bytes stay in memory and are removed when that user's last TUI
// session ends.
package uploads

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const (
	DefaultMaxBytes      = 10 << 20
	DefaultMaxFiles      = 1000
	DefaultMaxTotalBytes = 200 << 20
	DefaultBudget        = 8192
	// DefaultAutoInclude is how many files are switched into the prompt
	// automatically. Later uploads arrive excluded.
	DefaultAutoInclude = 20
	// describeMax caps the "Attached files" list in the prompt.
	describeMax = 100
	// omittedMax caps the list of file names that did not fit the budget.
	omittedMax = 20
)

// File is one uploaded object. Name is the slash-separated path relative
// to the user's upload directory, for example "src/main.go".
type File struct {
	Name     string
	Size     int64
	Included bool
	Text     string
	Tokens   int
	Note     string
	IsImage  bool
	MIME     string
	Width    int
	Height   int
	Sent     bool
	raw      int64
	data     []byte
	encoded  []byte
}

// Base is the last path component.
func (f File) Base() string { return path.Base(f.Name) }

// Dir is the parent directory, "" at the top level.
func (f File) Dir() string {
	d := path.Dir(f.Name)
	if d == "." {
		return ""
	}
	return d
}

// Entry is one directory listing item.
type Entry struct {
	Name  string // last component
	Path  string // full relative path, "" for the root
	IsDir bool
	Size  int64 // bytes as uploaded, what Read returns
}

// Store is safe for concurrent SCP, SFTP, and TUI sessions.
// File bytes live under dir/<user>/<relative path>. The in-memory index is
// the extracted text and the include flag.
type Store struct {
	mu            sync.Mutex
	dir           string
	maxBytes      int64
	maxFiles      int
	maxTotal      int64
	imageMaxPx    int
	imageMaxBytes int
	autoBudget    int
	autoFiles     int
	ignore        map[string]bool
	users         map[string]*userBox
}

type userBox struct {
	sessions int
	files    []*File // sorted by Name
	dirs     map[string]bool
	bytes    int64
	// ignoredDirs are directories created inside an ignored component,
	// kept so Stat answers while the client walks them.
	ignoredDirs map[string]bool
	// ignoredSeen makes the ignore log line appear once per ignored path.
	ignoredSeen map[string]bool
}

// New returns an empty store rooted at dir and deletes anything already there.
// Non-positive limits use the defaults. An empty dir uses /uploads.
func New(dir string, maxBytes int64, maxFiles int) *Store {
	if strings.TrimSpace(dir) == "" {
		dir = "/uploads"
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxFiles <= 0 {
		maxFiles = DefaultMaxFiles
	}
	s := &Store{
		dir:           dir,
		maxBytes:      maxBytes,
		maxFiles:      maxFiles,
		maxTotal:      DefaultMaxTotalBytes,
		imageMaxPx:    DefaultImageMaxPx,
		imageMaxBytes: DefaultImageMaxBytes,
		autoBudget:    DefaultBudget,
		autoFiles:     DefaultAutoInclude,
		ignore:        map[string]bool{},
		users:         map[string]*userBox{},
	}
	for _, name := range DefaultIgnore {
		s.ignore[name] = true
	}
	_ = s.Wipe()
	return s
}

// Dir is the on-disk root.
func (s *Store) Dir() string { return s.dir }

// SetImageLimits sets the downscale and encoded-size caps used for images.
func (s *Store) SetImageLimits(maxPx, maxBytes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxPx > 0 {
		s.imageMaxPx = maxPx
	}
	if maxBytes > 0 {
		s.imageMaxBytes = maxBytes
	}
}

// SetMaxTotalBytes caps the sum of stored file sizes per user.
func (s *Store) SetMaxTotalBytes(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > 0 {
		s.maxTotal = n
	}
}

// SetIgnore replaces the ignored component names. Paths with one of these
// components are accepted from clients and dropped. nil disables it.
func (s *Store) SetIgnore(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ignore = map[string]bool{}
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			s.ignore[name] = true
		}
	}
}

// SetAutoInclude sets the auto-include rule for new files: a new file is
// included while fewer than files are included and the included token
// total stays within budget. The first file is always included.
func (s *Store) SetAutoInclude(budget, files int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if budget > 0 {
		s.autoBudget = budget
	}
	if files >= 0 {
		s.autoFiles = files
	}
}

// Wipe removes every per-user directory. Called on process start.
func (s *Store) Wipe() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users = map[string]*userBox{}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(s.dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// MaxBytes is the per-file cap.
func (s *Store) MaxBytes() int64 { return s.maxBytes }

// SessionEnter counts a TUI session for user.
func (s *Store) SessionEnter(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.box(user).sessions++
}

// SessionLeave drops one TUI session. Files are deleted when the count hits zero.
func (s *Store) SessionLeave(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.box(user)
	if b.sessions > 0 {
		b.sessions--
	}
	if b.sessions == 0 {
		b.reset()
		_ = os.RemoveAll(s.userDir(user))
	}
}

// Ignored reports whether a clean relative path has an ignored component.
// prefix is the path up to and including that component.
func (s *Store) Ignored(rel string) (prefix string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ignoredLocked(rel)
}

func (s *Store) ignoredLocked(rel string) (string, bool) {
	if len(s.ignore) == 0 || rel == "" {
		return "", false
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		if s.ignore[part] {
			return strings.Join(parts[:i+1], "/"), true
		}
	}
	return "", false
}

// noteIgnoredLocked logs the first time an ignored path is seen for user.
func (s *Store) noteIgnoredLocked(user, prefix, op string) {
	b := s.box(user)
	if b.ignoredSeen[prefix] {
		return
	}
	b.ignoredSeen[prefix] = true
	log.Printf("INFO upload user=%s op=%s name=%q ignored (UPLOAD_IGNORE), contents are discarded", user, op, prefix)
}

// Discard reports whether rel is under an ignored component. The first
// hit for each ignored path is logged at INFO. op names the client action.
func (s *Store) Discard(user, rel, op string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix, ok := s.ignoredLocked(rel)
	if ok {
		s.noteIgnoredLocked(user, prefix, op)
	}
	return ok
}

// Precheck runs the checks Put can run before the bytes arrive: the path,
// directory clashes and the file count. Transfers call it when a file is
// opened so a full store is reported before any data is sent.
func (s *Store) Precheck(user, name string) error {
	clean, err := CleanPath(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ignoredLocked(clean); ok {
		return nil
	}
	b := s.box(user)
	if b.dirs[clean] || hasChildren(b, clean) {
		return fmt.Errorf("%s: is a directory", clean)
	}
	for _, parent := range parentDirs(clean) {
		if b.find(parent) != nil {
			return fmt.Errorf("%s: parent %s is a file", clean, parent)
		}
	}
	if b.find(clean) == nil && len(b.files) >= s.maxFiles {
		return fmt.Errorf("%s: limit of %d files per user reached (UPLOAD_MAX_FILES)", clean, s.maxFiles)
	}
	if b.bytes >= s.maxTotal {
		return fmt.Errorf("%s: total upload size limit of %d bytes per user reached (UPLOAD_MAX_TOTAL_BYTES)", clean, s.maxTotal)
	}
	return nil
}

// Put stores data under a sanitized relative path. Missing parent
// directories are created. An existing file is replaced and keeps its
// included flag. Paths under an ignored component are dropped without error.
func (s *Store) Put(user, name string, data []byte) error {
	clean, err := CleanPath(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if prefix, ok := s.ignoredLocked(clean); ok {
		s.noteIgnoredLocked(user, prefix, "put")
		s.mu.Unlock()
		return nil
	}
	maxPx, maxImg := s.imageMaxPx, s.imageMaxBytes
	s.mu.Unlock()
	if int64(len(data)) > s.maxBytes {
		return fmt.Errorf("%s: %d bytes exceeds the per-file limit of %d bytes (UPLOAD_MAX_BYTES)", clean, len(data), s.maxBytes)
	}
	text, note := Extract(clean, data)
	var prepared PreparedImage
	isImage := false
	if img, ok, err := PrepareImage(path.Base(clean), data, maxPx, maxImg); ok {
		if err != nil {
			note = "image: " + err.Error()
			log.Printf("WARN upload user=%s op=put name=%q err=%v", user, clean, err)
		} else {
			prepared = img
			prepared.Name = clean
			isImage = true
			text = ""
			note = ""
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.box(user)
	if b.dirs[clean] || hasChildren(b, clean) {
		return fmt.Errorf("%s: is a directory", clean)
	}
	for _, parent := range parentDirs(clean) {
		if b.find(parent) != nil {
			return fmt.Errorf("%s: parent %s is a file", clean, parent)
		}
	}
	existing := b.find(clean)
	total := b.bytes + int64(len(data))
	if existing != nil {
		total -= existing.raw
	} else if len(b.files) >= s.maxFiles {
		return fmt.Errorf("%s: limit of %d files per user reached (UPLOAD_MAX_FILES)", clean, s.maxFiles)
	}
	if total > s.maxTotal {
		return fmt.Errorf("%s: total upload size would be %d bytes, limit is %d bytes per user (UPLOAD_MAX_TOTAL_BYTES)", clean, total, s.maxTotal)
	}
	if err := s.writeFile(user, clean, data); err != nil {
		return fmt.Errorf("%s: %w", clean, err)
	}
	for _, parent := range parentDirs(clean) {
		b.dirs[parent] = true
	}
	b.bytes = total
	if existing != nil {
		included := existing.Included
		*existing = *newStoredFile(clean, data, text, note, included, isImage, prepared)
		return nil
	}
	f := newStoredFile(clean, data, text, note, false, isImage, prepared)
	f.Included = s.autoIncludeLocked(b, f)
	b.insert(f)
	return nil
}

// autoIncludeLocked applies the auto-include rule to a new file.
func (s *Store) autoIncludeLocked(b *userBox, f *File) bool {
	count, tokens := 0, 0
	for _, g := range b.files {
		if g.Included {
			count++
			tokens += g.Tokens
		}
	}
	if count == 0 {
		return true
	}
	return count < s.autoFiles && tokens+f.Tokens <= s.autoBudget
}

// Mkdir creates a directory and any missing parents. Directories under an
// ignored component succeed without storing anything.
func (s *Store) Mkdir(user, name string) error {
	clean, err := CleanPath(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.box(user)
	if prefix, ok := s.ignoredLocked(clean); ok {
		s.noteIgnoredLocked(user, prefix, "mkdir")
		b.ignoredDirs[clean] = true
		return nil
	}
	if b.find(clean) != nil {
		return fmt.Errorf("%s: a file with that name exists", clean)
	}
	for _, parent := range parentDirs(clean) {
		if b.find(parent) != nil {
			return fmt.Errorf("%s: parent %s is a file", clean, parent)
		}
	}
	if b.dirs[clean] {
		return nil
	}
	if len(b.dirs) >= s.maxFiles {
		return fmt.Errorf("%s: limit of %d directories per user reached (UPLOAD_MAX_FILES)", clean, s.maxFiles)
	}
	if _, err := s.ensureDir(user, clean); err != nil {
		return fmt.Errorf("%s: %w", clean, err)
	}
	for _, parent := range parentDirs(clean) {
		b.dirs[parent] = true
	}
	b.dirs[clean] = true
	return nil
}

// Stat describes a file or directory. "" is the root. Ignored directories
// that a client created are reported as empty directories.
func (s *Store) Stat(user, name string) (Entry, error) {
	if strings.TrimSpace(name) == "" {
		return Entry{IsDir: true}, nil
	}
	clean, err := CleanPath(name)
	if err != nil {
		return Entry{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.box(user)
	if _, ok := s.ignoredLocked(clean); ok {
		if b.ignoredDirs[clean] {
			return Entry{Name: path.Base(clean), Path: clean, IsDir: true}, nil
		}
		return Entry{}, fs.ErrNotExist
	}
	if f := b.find(clean); f != nil {
		return Entry{Name: path.Base(clean), Path: clean, Size: f.raw}, nil
	}
	if b.dirs[clean] || hasChildren(b, clean) {
		return Entry{Name: path.Base(clean), Path: clean, IsDir: true}, nil
	}
	return Entry{}, fs.ErrNotExist
}

// ReadDir lists the direct children of a directory, sorted by name.
// "" is the root.
func (s *Store) ReadDir(user, dir string) ([]Entry, error) {
	clean := ""
	if strings.TrimSpace(dir) != "" {
		c, err := CleanPath(dir)
		if err != nil {
			return nil, err
		}
		clean = c
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.box(user)
	if clean != "" {
		if _, ok := s.ignoredLocked(clean); ok {
			if b.ignoredDirs[clean] {
				return nil, nil
			}
			return nil, fs.ErrNotExist
		}
		if b.find(clean) != nil {
			return nil, fmt.Errorf("%s: not a directory", clean)
		}
		if !b.dirs[clean] && !hasChildren(b, clean) {
			return nil, fs.ErrNotExist
		}
	}
	prefix := ""
	if clean != "" {
		prefix = clean + "/"
	}
	seen := map[string]Entry{}
	add := func(p string, file *File) {
		if !strings.HasPrefix(p, prefix) || p == clean {
			return
		}
		rest := p[len(prefix):]
		name, deeper, _ := strings.Cut(rest, "/")
		if deeper != "" || file == nil {
			if _, ok := seen[name]; !ok {
				seen[name] = Entry{Name: name, Path: prefix + name, IsDir: true}
			}
			return
		}
		seen[name] = Entry{Name: name, Path: p, Size: file.raw}
	}
	for d := range b.dirs {
		add(d, nil)
	}
	for _, f := range b.files {
		add(f.Name, f)
	}
	out := make([]Entry, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Dirs returns every known directory path, sorted.
func (s *Store) Dirs(user string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.box(user)
	out := make([]string, 0, len(b.dirs))
	for d := range b.dirs {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// List returns a snapshot sorted by path. Data bytes are not copied.
func (s *Store) List(user string) []File {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.box(user)
	out := make([]File, len(b.files))
	for i, f := range b.files {
		out[i] = *f
		out[i].data = nil
		out[i].encoded = nil
	}
	return out
}

// Usage is the number of stored files and their total size for user.
func (s *Store) Usage(user string) (files int, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.box(user)
	return len(b.files), b.bytes
}

// Read returns a copy of the stored bytes.
func (s *Store) Read(user, name string) ([]byte, error) {
	clean, err := CleanPath(name)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.box(user).find(clean)
	if f == nil {
		return nil, fmt.Errorf("no such file %s: %w", clean, fs.ErrNotExist)
	}
	return append([]byte(nil), f.data...), nil
}

// Remove deletes one file owned by user. Use RemoveDir for directories.
func (s *Store) Remove(user, name string) error {
	clean, err := CleanPath(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ignoredLocked(clean); ok {
		return nil
	}
	b := s.box(user)
	for i, f := range b.files {
		if f.Name == clean {
			b.files = append(b.files[:i], b.files[i+1:]...)
			b.bytes -= f.raw
			_ = os.Remove(s.filePath(user, clean))
			return nil
		}
	}
	if b.dirs[clean] || hasChildren(b, clean) {
		return fmt.Errorf("%s: is a directory", clean)
	}
	return fmt.Errorf("no such file %s: %w", clean, fs.ErrNotExist)
}

// RemoveDir deletes a directory and everything under it.
func (s *Store) RemoveDir(user, name string) error {
	clean, err := CleanPath(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.box(user)
	if _, ok := s.ignoredLocked(clean); ok {
		for d := range b.ignoredDirs {
			if under(d, clean) {
				delete(b.ignoredDirs, d)
			}
		}
		return nil
	}
	if b.find(clean) != nil {
		return fmt.Errorf("%s: not a directory", clean)
	}
	if !b.dirs[clean] && !hasChildren(b, clean) {
		return fmt.Errorf("no such directory %s: %w", clean, fs.ErrNotExist)
	}
	kept := b.files[:0]
	for _, f := range b.files {
		if under(f.Name, clean) {
			b.bytes -= f.raw
			continue
		}
		kept = append(kept, f)
	}
	for i := len(kept); i < len(b.files); i++ {
		b.files[i] = nil
	}
	b.files = kept
	for d := range b.dirs {
		if under(d, clean) {
			delete(b.dirs, d)
		}
	}
	return os.RemoveAll(s.filePath(user, clean))
}

// Rename moves a file or a directory. An existing target file is replaced.
// A directory cannot replace anything.
func (s *Store) Rename(user, from, to string) error {
	src, err := CleanPath(from)
	if err != nil {
		return err
	}
	dst, err := CleanPath(to)
	if err != nil {
		return err
	}
	if src == dst {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.box(user)
	if _, ok := s.ignoredLocked(src); ok {
		return fmt.Errorf("%s: ignored path (UPLOAD_IGNORE): %w", src, fs.ErrNotExist)
	}
	if _, ok := s.ignoredLocked(dst); ok {
		return fmt.Errorf("%s: target is an ignored path (UPLOAD_IGNORE): %w", dst, fs.ErrPermission)
	}
	for _, parent := range parentDirs(dst) {
		if b.find(parent) != nil {
			return fmt.Errorf("%s: parent %s is a file", dst, parent)
		}
	}
	if f := b.find(src); f != nil {
		if b.dirs[dst] || hasChildren(b, dst) {
			return fmt.Errorf("%s: is a directory", dst)
		}
		if _, err := s.ensureDir(user, path.Dir(dst)); err != nil {
			return err
		}
		if err := os.Rename(s.filePath(user, src), s.filePath(user, dst)); err != nil {
			return err
		}
		if old := b.find(dst); old != nil {
			b.bytes -= old.raw
			b.drop(dst)
		}
		b.drop(src)
		if !f.IsImage && strings.ToLower(path.Ext(src)) != strings.ToLower(path.Ext(dst)) {
			f.Text, f.Note = Extract(dst, f.data)
			f.Tokens = EstimateTokens(f.Text)
		}
		f.Name = dst
		b.insert(f)
		for _, parent := range parentDirs(dst) {
			b.dirs[parent] = true
		}
		return nil
	}
	if !b.dirs[src] && !hasChildren(b, src) {
		return fmt.Errorf("no such file or directory %s: %w", src, fs.ErrNotExist)
	}
	if under(dst, src) {
		return fmt.Errorf("%s: cannot move a directory into itself", dst)
	}
	if b.find(dst) != nil || b.dirs[dst] || hasChildren(b, dst) {
		return fmt.Errorf("%s: target exists", dst)
	}
	if _, err := s.ensureDir(user, path.Dir(dst)); err != nil {
		return err
	}
	if err := os.Rename(s.filePath(user, src), s.filePath(user, dst)); err != nil {
		return err
	}
	moved := map[string]bool{}
	for d := range b.dirs {
		if under(d, src) {
			moved[dst+strings.TrimPrefix(d, src)] = true
			delete(b.dirs, d)
		}
	}
	for d := range moved {
		b.dirs[d] = true
	}
	for _, parent := range parentDirs(dst) {
		b.dirs[parent] = true
	}
	b.dirs[dst] = true
	for _, f := range b.files {
		if under(f.Name, src) {
			f.Name = dst + strings.TrimPrefix(f.Name, src)
		}
	}
	sort.Slice(b.files, func(i, j int) bool { return b.files[i].Name < b.files[j].Name })
	return nil
}

// Clear deletes every file for user and drops them from context. The TUI
// session count is left as it is.
func (s *Store) Clear(user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.box(user).reset()
	return os.RemoveAll(s.userDir(user))
}

// SetIncluded toggles whether the file is injected into the next prompt.
func (s *Store) SetIncluded(user, name string, on bool) error {
	clean, err := CleanPath(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.box(user).find(clean)
	if f == nil {
		return fmt.Errorf("no such file %s", clean)
	}
	f.Included = on
	if on {
		f.Sent = false
	}
	return nil
}

// SetIncludedDir sets the include flag on every file under dir ("" is
// every file) and returns how many files it touched.
func (s *Store) SetIncludedDir(user, dir string, on bool) (int, error) {
	clean := ""
	if strings.TrimSpace(dir) != "" {
		c, err := CleanPath(dir)
		if err != nil {
			return 0, err
		}
		clean = c
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, f := range s.box(user).files {
		if !under(f.Name, clean) || f.Name == clean {
			continue
		}
		if f.Included != on {
			f.Included = on
			if on {
				f.Sent = false
			}
		}
		n++
	}
	if n == 0 {
		return 0, fmt.Errorf("no files under %s", clean)
	}
	return n, nil
}

// PendingImages returns included images that have not been sent yet.
func (s *Store) PendingImages(user string) []PreparedImage {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []PreparedImage
	for _, f := range s.box(user).files {
		if f.Included && f.IsImage && !f.Sent && len(f.encoded) > 0 {
			out = append(out, PreparedImage{
				Name:   f.Name,
				MIME:   f.MIME,
				Width:  f.Width,
				Height: f.Height,
				Data:   append([]byte(nil), f.encoded...),
			})
		}
	}
	return out
}

// MarkSent records that these images were accepted by the model.
func (s *Store) MarkSent(user string, names []string) {
	want := map[string]bool{}
	for _, name := range names {
		want[name] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.box(user).files {
		if want[f.Name] {
			f.Sent = true
		}
	}
}

// Describe lists included files by relative path for the prompt.
func (s *Store) Describe(user string) string {
	return describe(s.List(user))
}

func describe(files []File) string {
	var b strings.Builder
	n := 0
	for _, f := range files {
		if !f.Included {
			continue
		}
		if n == 0 {
			b.WriteString("Attached files (paths relative to the upload directory):\n")
		}
		n++
		if n > describeMax {
			continue
		}
		switch {
		case f.IsImage:
			fmt.Fprintf(&b, "- %s (image %dx%d)\n", f.Name, f.Width, f.Height)
		case f.Note == noteEdited:
			fmt.Fprintf(&b, "- %s (edited in this session)\n", f.Name)
		case f.Note == noteNew:
			fmt.Fprintf(&b, "- %s (new in this session)\n", f.Name)
		default:
			fmt.Fprintf(&b, "- %s\n", f.Name)
		}
	}
	if n > describeMax {
		fmt.Fprintf(&b, "- ... and %d more\n", n-describeMax)
	}
	return b.String()
}

// Overlay supplies edited versions of uploaded files. Version returns the
// accepted text of name; ok is false when the upload is used as it is and
// exists is false when the file was deleted. Added lists new files.
type Overlay interface {
	Version(name string) (text string, exists, ok bool)
	Added() []string
}

const (
	noteEdited = "edited in this session"
	noteNew    = "new in this session"
)

// ContextResult is the prompt fragment for included files.
type ContextResult struct {
	Text string
	// Truncated is true when anything was dropped or cut.
	Truncated bool
	// Full holds the files whose complete text is in Text.
	Full map[string]bool
}

// Context builds the prompt fragment for included files, truncated so the
// estimated token count stays within budget. Files that do not fit are
// named in a closing note. The bool is true when anything was dropped or cut.
func (s *Store) Context(user string, budget int) (string, bool) {
	r := s.ContextWith(user, budget, nil)
	return r.Text, r.Truncated
}

// ContextWith is Context with edited versions laid over the uploads.
// Edited files replace their upload, deleted files are left out and new
// files are added as included text files.
func (s *Store) ContextWith(user string, budget int, ov Overlay) ContextResult {
	if budget <= 0 {
		budget = DefaultBudget
	}
	files := s.List(user)
	if ov != nil {
		files = overlayFiles(files, ov)
	}
	res := ContextResult{Full: map[string]bool{}}
	var b strings.Builder
	if desc := describe(files); desc != "" {
		b.WriteString(desc)
		b.WriteByte('\n')
	}
	used := 0
	full := false
	var omitted []string
	for _, f := range files {
		if !f.Included {
			continue
		}
		if strings.TrimSpace(f.Text) == "" {
			if f.Text == "" && f.Note == "" && !f.IsImage {
				// An empty text file is seen in full.
				res.Full[f.Name] = true
			}
			continue
		}
		if full {
			omitted = append(omitted, f.Name)
			continue
		}
		header := fmt.Sprintf("----- %s (%d bytes, ~%d tokens) -----\n", f.Name, f.Size, f.Tokens)
		ht := EstimateTokens(header)
		remain := budget - used - ht
		body := f.Text
		if remain <= 0 {
			res.Truncated = true
			full = true
			omitted = append(omitted, f.Name)
			continue
		}
		cut := false
		if EstimateTokens(body) > remain {
			body = truncateRunes(body, remain*4)
			body += "\n[truncated to the context token budget]\n"
			res.Truncated = true
			full = true
			cut = true
		}
		if !cut {
			res.Full[f.Name] = true
		}
		b.WriteString(header)
		b.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteByte('\n')
		}
		used += ht + EstimateTokens(body)
	}
	if len(omitted) > 0 {
		shown := omitted
		if len(shown) > omittedMax {
			shown = shown[:omittedMax]
		}
		fmt.Fprintf(&b, "\n[%d included file(s) left out, over the context token budget: %s", len(omitted), strings.Join(shown, ", "))
		if len(omitted) > len(shown) {
			fmt.Fprintf(&b, ", and %d more", len(omitted)-len(shown))
		}
		b.WriteString("]\n")
	}
	res.Text = b.String()
	return res
}

// overlayFiles applies edited versions to a snapshot sorted by name.
func overlayFiles(files []File, ov Overlay) []File {
	out := make([]File, 0, len(files))
	for _, f := range files {
		text, exists, ok := ov.Version(f.Name)
		if !ok {
			out = append(out, f)
			continue
		}
		if !exists {
			continue
		}
		f.Text = text
		f.Size = int64(len(text))
		f.Tokens = EstimateTokens(text)
		f.Note = noteEdited
		out = append(out, f)
	}
	for _, name := range ov.Added() {
		text, exists, ok := ov.Version(name)
		if !ok || !exists {
			continue
		}
		out = append(out, File{Name: name, Size: int64(len(text)), Included: true, Text: text, Tokens: EstimateTokens(text), Note: noteNew})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// UserView is one user's uploads as the edit engine sees them.
type UserView struct {
	store *Store
	user  string
}

// View returns the read-only view of user's uploads used by edits.
func (s *Store) View(user string) UserView { return UserView{store: s, user: user} }

// Original returns the uploaded bytes of name and whether the model can
// edit it. Only plain UTF-8 text is editable: the model sees PDF and DOCX
// files as extracted text, not as their source.
func (v UserView) Original(name string) ([]byte, bool, bool, string) {
	clean, err := CleanPath(name)
	if err != nil {
		return nil, false, false, err.Error()
	}
	v.store.mu.Lock()
	defer v.store.mu.Unlock()
	f := v.store.box(v.user).find(clean)
	if f == nil {
		return nil, false, false, ""
	}
	data := append([]byte(nil), f.data...)
	switch ext := strings.ToLower(path.Ext(clean)); {
	case f.IsImage:
		return data, true, false, "image"
	case ext == ".pdf" || ext == ".docx":
		return data, true, false, "the model only sees the extracted text of " + ext + " files"
	case !looksText(data):
		return data, true, false, "binary file"
	}
	return data, true, true, ""
}

// IsDir reports whether name is an uploaded directory.
func (v UserView) IsDir(name string) bool {
	st, err := v.store.Stat(v.user, name)
	return err == nil && st.IsDir
}

// Ignored reports whether name has an ignored component.
func (v UserView) Ignored(name string) bool {
	_, ok := v.store.Ignored(name)
	return ok
}

// IncludedTokens is the sum of token estimates for included files that have text.
func (s *Store) IncludedTokens(user string) int {
	n := 0
	for _, f := range s.List(user) {
		if f.Included {
			n += f.Tokens
		}
	}
	return n
}

// EstimateTokens is a rough byte/4 estimate, good enough to warn before a prompt.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

// FilePath is the on-disk location of a stored file or directory.
func (s *Store) FilePath(user, name string) (string, error) {
	clean, err := CleanPath(name)
	if err != nil {
		return "", err
	}
	return s.filePath(user, clean), nil
}

func (s *Store) userDir(user string) string {
	return filepath.Join(s.dir, userKey(user))
}

func (s *Store) filePath(user, clean string) string {
	return filepath.Join(s.userDir(user), filepath.FromSlash(clean))
}

var errEscape = errors.New("path escapes the upload directory")

// ensureDir creates dir (relative, "" or "." for the user root) under the
// user directory and checks that every component on the way is a real
// directory, not a symlink.
func (s *Store) ensureDir(user, dir string) (string, error) {
	root := s.userDir(user)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if fi, err := os.Lstat(root); err != nil || !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
		return "", errEscape
	}
	if dir == "" || dir == "." {
		return root, nil
	}
	cur := root
	for _, part := range strings.Split(dir, "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if err := os.Mkdir(cur, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return "", err
			}
			if fi, err = os.Lstat(cur); err != nil {
				return "", err
			}
		case err != nil:
			return "", err
		}
		if fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir() {
			return "", errEscape
		}
	}
	rel, err := filepath.Rel(root, cur)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errEscape
	}
	return cur, nil
}

func (s *Store) writeFile(user, clean string, data []byte) error {
	parent, err := s.ensureDir(user, path.Dir(clean))
	if err != nil {
		return err
	}
	target := filepath.Join(parent, path.Base(clean))
	if fi, err := os.Lstat(target); err == nil && (fi.Mode()&fs.ModeSymlink != 0 || fi.IsDir()) {
		return errEscape
	}
	tmp, err := os.CreateTemp(parent, ".upload-*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func (b *userBox) reset() {
	b.files = nil
	b.bytes = 0
	b.dirs = map[string]bool{}
	b.ignoredDirs = map[string]bool{}
	b.ignoredSeen = map[string]bool{}
}

func (b *userBox) index(name string) (int, bool) {
	i := sort.Search(len(b.files), func(i int) bool { return b.files[i].Name >= name })
	return i, i < len(b.files) && b.files[i].Name == name
}

func (b *userBox) find(name string) *File {
	if i, ok := b.index(name); ok {
		return b.files[i]
	}
	return nil
}

func (b *userBox) insert(f *File) {
	i, ok := b.index(f.Name)
	if ok {
		b.files[i] = f
		return
	}
	b.files = append(b.files, nil)
	copy(b.files[i+1:], b.files[i:])
	b.files[i] = f
}

func (b *userBox) drop(name string) {
	if i, ok := b.index(name); ok {
		b.files = append(b.files[:i], b.files[i+1:]...)
	}
}

// hasChildren reports whether any stored file lives under dir.
func hasChildren(b *userBox, dir string) bool {
	if dir == "" {
		return len(b.files) > 0
	}
	prefix := dir + "/"
	i := sort.Search(len(b.files), func(i int) bool { return b.files[i].Name >= prefix })
	return i < len(b.files) && strings.HasPrefix(b.files[i].Name, prefix)
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
	key := b.String()
	key = strings.ReplaceAll(key, "..", "_")
	if key == "" || key == "." || key == ".." {
		return "anonymous"
	}
	return key
}

func (s *Store) box(user string) *userBox {
	key := userKey(user)
	b := s.users[key]
	if b == nil {
		b = &userBox{}
		b.reset()
		s.users[key] = b
	}
	return b
}

func newStoredFile(name string, data []byte, text, note string, included, isImage bool, img PreparedImage) *File {
	f := &File{
		Name:     name,
		Size:     int64(len(data)),
		Included: included,
		Text:     text,
		Tokens:   EstimateTokens(text),
		Note:     note,
		IsImage:  isImage,
		raw:      int64(len(data)),
		data:     append([]byte(nil), data...),
	}
	if isImage && len(img.Data) > 0 {
		f.MIME = img.MIME
		f.Width = img.Width
		f.Height = img.Height
		f.Size = int64(len(img.Data))
		f.encoded = append([]byte(nil), img.Data...)
		f.Tokens = 0
	}
	return f
}

func truncateRunes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	for len(cut) > 0 && !utf8Start(cut[len(cut)-1]) && len(cut) > maxBytes-4 {
		cut = cut[:len(cut)-1]
	}
	return cut
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
