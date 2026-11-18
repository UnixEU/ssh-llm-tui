package downloads

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
)

const (
	// FilesDir holds the changed and new files with their upload paths.
	FilesDir = "files"
	// PatchName is the unified diff of every accepted change.
	PatchName = "changes.patch"
	// ArchiveExt is the only archive format.
	ArchiveExt = ".tar.gz"

	partSuffix = ".part"
)

// File is one file written into downloads.
type File struct {
	Path string // relative, slash-separated
	Data []byte
}

// Export replaces files/ and changes.patch with a new set. files may be
// empty (a patch that only deletes). Everything is staged first, so a
// failed export leaves the previous one in place.
func (s *Store) Export(user string, files []File, patch []byte) error {
	var total int64 = int64(len(patch))
	for _, f := range files {
		total += int64(len(f.Data))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := s.usageLocked(user, FilesDir) - s.sizeLocked(user, PatchName)
	if keep+total > s.maxTotal {
		return fmt.Errorf("export is %d bytes; with what is already in downloads that is over the limit of %d bytes per user (DOWNLOAD_MAX_TOTAL_BYTES)", total, s.maxTotal)
	}
	root, err := s.rootLocked(user)
	if err != nil {
		return err
	}
	stage := filepath.Join(root, FilesDir+partSuffix)
	_ = os.RemoveAll(stage)
	for _, f := range files {
		rel, err := uploads.CleanPath(f.Path)
		if err != nil {
			_ = os.RemoveAll(stage)
			return err
		}
		dst := filepath.Join(stage, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			_ = os.RemoveAll(stage)
			return err
		}
		if err := os.WriteFile(dst, f.Data, 0o644); err != nil {
			_ = os.RemoveAll(stage)
			return err
		}
	}
	patchPart := filepath.Join(root, PatchName+partSuffix)
	if err := os.WriteFile(patchPart, patch, 0o644); err != nil {
		_ = os.RemoveAll(stage)
		return err
	}
	if err := os.RemoveAll(filepath.Join(root, FilesDir)); err != nil {
		return err
	}
	if len(files) > 0 {
		if err := os.Rename(stage, filepath.Join(root, FilesDir)); err != nil {
			return err
		}
	} else {
		_ = os.RemoveAll(stage)
	}
	return os.Rename(patchPart, filepath.Join(root, PatchName))
}

// ArchiveEntry is one file in a tar.gz archive.
type ArchiveEntry struct {
	Name string // path inside the archive, relative
	Data []byte
}

// WriteArchive writes name (ending in .tar.gz) at the top of the user's
// downloads. Entries get their parent directories. The archive is
// written as name.part and renamed when complete and within the limit.
func (s *Store) WriteArchive(user, name string, entries []ArchiveEntry) (int64, error) {
	if !strings.HasSuffix(name, ArchiveExt) || strings.ContainsAny(name, "/\\") {
		return 0, fmt.Errorf("archive name %q: want a plain name ending in %s", name, ArchiveExt)
	}
	if _, err := uploads.CleanPath(name); err != nil {
		return 0, err
	}
	sorted := append([]ArchiveEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for i := range sorted {
		rel, err := uploads.CleanPath(sorted[i].Name)
		if err != nil {
			return 0, err
		}
		sorted[i].Name = rel
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.rootLocked(user)
	if err != nil {
		return 0, err
	}
	part := filepath.Join(root, name+partSuffix)
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	if err := writeTarGz(f, sorted, time.Now()); err != nil {
		f.Close()
		_ = os.Remove(part)
		return 0, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return 0, err
	}
	fi, err := os.Stat(part)
	if err != nil {
		_ = os.Remove(part)
		return 0, err
	}
	keep := s.usageLocked(user, "") - fi.Size() - s.sizeLocked(user, name)
	if keep+fi.Size() > s.maxTotal {
		_ = os.Remove(part)
		return 0, fmt.Errorf("%s would be %d bytes; with what is already in downloads that is over the limit of %d bytes per user (DOWNLOAD_MAX_TOTAL_BYTES)", name, fi.Size(), s.maxTotal)
	}
	if err := os.Rename(part, filepath.Join(root, name)); err != nil {
		_ = os.Remove(part)
		return 0, err
	}
	return fi.Size(), nil
}

func writeTarGz(w io.Writer, entries []ArchiveEntry, now time.Time) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	dirs := map[string]bool{}
	for _, e := range entries {
		var parents []string
		for d := path.Dir(e.Name); d != "." && d != "/" && !dirs[d]; d = path.Dir(d) {
			parents = append(parents, d)
			dirs[d] = true
		}
		for i := len(parents) - 1; i >= 0; i-- {
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: parents[i] + "/", Mode: 0o755, ModTime: now, Format: tar.FormatPAX}); err != nil {
				return err
			}
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: e.Name, Mode: 0o644, Size: int64(len(e.Data)), ModTime: now, Format: tar.FormatPAX}); err != nil {
			return err
		}
		if _, err := tw.Write(e.Data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// rootLocked creates the user's directory and checks it is a real one.
func (s *Store) rootLocked(user string) (string, error) {
	root := s.userDir(user)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() || fi.Mode()&fs.ModeSymlink != 0 {
		return "", errors.New("downloads directory is not a plain directory")
	}
	return root, nil
}

func (s *Store) sizeLocked(user, rel string) int64 {
	fi, err := os.Lstat(filepath.Join(s.userDir(user), filepath.FromSlash(rel)))
	if err != nil || !fi.Mode().IsRegular() {
		return 0
	}
	return fi.Size()
}

func readAll(f *os.File) ([]byte, error) { return io.ReadAll(f) }
