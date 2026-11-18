// Package uploads stores files received over SCP and SFTP, one set per SSH
// username. Bytes stay in memory and are removed when that user's last TUI
// session ends.
package uploads

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const (
	DefaultMaxBytes = 10 << 20
	DefaultMaxFiles = 10
	DefaultBudget   = 8192
)

// File is one uploaded object.
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
	data     []byte
	encoded  []byte
}

// Store is safe for concurrent SCP, SFTP, and TUI sessions.
// File bytes live under dir/<user>/<name>. The in-memory index is the
// extracted text and the include flag.
type Store struct {
	mu            sync.Mutex
	dir           string
	maxBytes      int64
	maxFiles      int
	imageMaxPx    int
	imageMaxBytes int
	users         map[string]*userBox
}

type userBox struct {
	sessions int
	files    []*File
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
		imageMaxPx:    DefaultImageMaxPx,
		imageMaxBytes: DefaultImageMaxBytes,
		users:         map[string]*userBox{},
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
		b.files = nil
		_ = os.RemoveAll(s.userDir(user))
	}
}

// Put stores data under a sanitized base name. An existing name is replaced
// and keeps its included flag.
func (s *Store) Put(user, name string, data []byte) error {
	clean, err := CleanName(name)
	if err != nil {
		return err
	}
	if int64(len(data)) > s.maxBytes {
		return fmt.Errorf("%s exceeds the %d byte upload limit", clean, s.maxBytes)
	}
	text, note := Extract(clean, data)
	s.mu.Lock()
	maxPx, maxImg := s.imageMaxPx, s.imageMaxBytes
	s.mu.Unlock()
	var prepared PreparedImage
	isImage := false
	if img, ok, err := PrepareImage(clean, data, maxPx, maxImg); ok {
		if err != nil {
			note = "image: " + err.Error()
		} else {
			prepared = img
			isImage = true
			text = ""
			note = ""
		}
	}
	s.mu.Lock()
	replacing := findFile(s.box(user), clean) != nil
	nfiles := len(s.box(user).files)
	s.mu.Unlock()
	if !replacing && nfiles >= s.maxFiles {
		return fmt.Errorf("at most %d files per user", s.maxFiles)
	}
	if err := s.writeFile(user, clean, data); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.box(user)
	if existing := findFile(b, clean); existing != nil {
		included := existing.Included
		*existing = *newStoredFile(clean, data, text, note, included, isImage, prepared)
		return nil
	}
	b.files = append(b.files, newStoredFile(clean, data, text, note, true, isImage, prepared))
	sort.Slice(b.files, func(i, j int) bool { return b.files[i].Name < b.files[j].Name })
	return nil
}

// List returns a snapshot. Data bytes are not copied.
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

// Read returns a copy of the stored bytes.
func (s *Store) Read(user, name string) ([]byte, error) {
	clean, err := CleanName(name)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := findFile(s.box(user), clean)
	if f == nil {
		return nil, fmt.Errorf("no such file %s", clean)
	}
	return append([]byte(nil), f.data...), nil
}

// Remove deletes one file owned by user.
func (s *Store) Remove(user, name string) error {
	clean, err := CleanName(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.box(user)
	for i, f := range b.files {
		if f.Name == clean {
			b.files = append(b.files[:i], b.files[i+1:]...)
			_ = os.Remove(s.filePath(user, clean))
			return nil
		}
	}
	return fmt.Errorf("no such file %s", clean)
}

// Clear deletes every file for user and drops them from context. The TUI
// session count is left as it is.
func (s *Store) Clear(user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.box(user)
	b.files = nil
	return os.RemoveAll(s.userDir(user))
}

// SetIncluded toggles whether the file is injected into the next prompt.
func (s *Store) SetIncluded(user, name string, on bool) error {
	clean, err := CleanName(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := findFile(s.box(user), clean)
	if f == nil {
		return fmt.Errorf("no such file %s", clean)
	}
	f.Included = on
	if on {
		f.Sent = false
	}
	return nil
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

// Describe lists included files by name for the prompt.
func (s *Store) Describe(user string) string {
	files := s.List(user)
	var b strings.Builder
	any := false
	for _, f := range files {
		if !f.Included {
			continue
		}
		if !any {
			b.WriteString("Attached files:\n")
			any = true
		}
		if f.IsImage {
			fmt.Fprintf(&b, "- %s (image %dx%d)\n", f.Name, f.Width, f.Height)
		} else {
			fmt.Fprintf(&b, "- %s\n", f.Name)
		}
	}
	return b.String()
}

// Context builds the prompt fragment for included files, truncated so the
// estimated token count stays within budget. The bool is true when anything
// was dropped or cut.
func (s *Store) Context(user string, budget int) (string, bool) {
	if budget <= 0 {
		budget = DefaultBudget
	}
	files := s.List(user)
	var b strings.Builder
	if desc := s.Describe(user); desc != "" {
		b.WriteString(desc)
		b.WriteByte('\n')
	}
	used := 0
	truncated := false
	for _, f := range files {
		if !f.Included || strings.TrimSpace(f.Text) == "" {
			continue
		}
		header := fmt.Sprintf("----- %s (%d bytes, ~%d tokens) -----\n", f.Name, f.Size, f.Tokens)
		ht := EstimateTokens(header)
		remain := budget - used - ht
		body := f.Text
		if remain <= 0 {
			truncated = true
			break
		}
		if EstimateTokens(body) > remain {
			body = truncateRunes(body, remain*4)
			body += "\n[truncated to the context token budget]\n"
			truncated = true
		}
		b.WriteString(header)
		b.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteByte('\n')
		}
		used += ht + EstimateTokens(body)
		if truncated {
			break
		}
	}
	return b.String(), truncated
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

// CleanName keeps a single path segment and rejects traversal.
func CleanName(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("invalid file name")
	}
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if part == ".." {
			return "", fmt.Errorf("invalid file name")
		}
	}
	name = parts[len(parts)-1]
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("invalid file name")
	}
	var b strings.Builder
	for _, r := range name {
		if r < 32 || r == 127 || unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	name = b.String()
	if len(name) > 128 {
		name = name[:128]
	}
	if name == "" || name == "." || name == ".." {
		return "", fmt.Errorf("invalid file name")
	}
	return name, nil
}

// FilePath is the on-disk location of a stored file.
func (s *Store) FilePath(user, name string) (string, error) {
	clean, err := CleanName(name)
	if err != nil {
		return "", err
	}
	return s.filePath(user, clean), nil
}

func (s *Store) userDir(user string) string {
	return filepath.Join(s.dir, userKey(user))
}

func (s *Store) filePath(user, clean string) string {
	return filepath.Join(s.userDir(user), clean)
}

func (s *Store) writeFile(user, clean string, data []byte) error {
	dir := s.userDir(user)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := s.filePath(user, clean)
	rel, err := filepath.Rel(s.dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("invalid file name")
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

func findFile(b *userBox, name string) *File {
	for _, f := range b.files {
		if f.Name == name {
			return f
		}
	}
	return nil
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
