package transfer

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/UnixEU/ssh-llm-tui/internal/downloads"
	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
)

// area is the top-level folder a client path points into.
type area int

const (
	areaRoot area = iota
	areaUploads
	areaDownloads
)

// Top-level folder names seen by clients.
const (
	uploadsName   = "uploads"
	downloadsName = "downloads"
)

// resolve maps a client path onto an area and a path inside it. The root
// lists two folders, uploads/ and downloads/. A path that does not start
// with downloads goes to uploads, with one leading "uploads/" dropped, so
// every upload command of earlier versions keeps working.
func resolve(p string) (area, string, error) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	switch {
	case p == "~", p == "~/":
		p = "/"
	case strings.HasPrefix(p, "~/"):
		p = "/" + strings.TrimPrefix(p, "~/")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return areaRoot, "", fmt.Errorf("invalid path %q: dot-dot component: %w", p, uploads.ErrInvalidPath)
		}
	}
	rel := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(p, "/")), "/")
	switch {
	case rel == "":
		return areaRoot, "", nil
	case rel == downloadsName:
		return areaDownloads, "", nil
	case strings.HasPrefix(rel, downloadsName+"/"):
		clean, err := uploads.CleanPath(strings.TrimPrefix(rel, downloadsName+"/"))
		return areaDownloads, clean, err
	case rel == uploadsName:
		return areaUploads, "", nil
	}
	clean, err := uploads.CleanPath(strings.TrimPrefix(rel, uploadsName+"/"))
	return areaUploads, clean, err
}

// vpath is the virtual path of rel in an area, as returned by Glob and
// passed back to WalkDir and NewFileEntry.
func vpath(a area, rel string) string {
	switch a {
	case areaUploads:
		return path.Join(uploadsName, rel)
	case areaDownloads:
		return path.Join(downloadsName, rel)
	}
	return ""
}

// ventry is one item of the virtual tree.
type ventry struct {
	Name  string
	Path  string // virtual path
	IsDir bool
	Size  int64
}

// vfs joins uploads and downloads under one root for reading.
type vfs struct {
	up *uploads.Store
	dl *downloads.Store
}

func (v vfs) stat(user, p string) (ventry, error) {
	a, rel, err := resolve(p)
	if err != nil {
		return ventry{}, err
	}
	switch a {
	case areaRoot:
		return ventry{Name: "/", IsDir: true}, nil
	case areaDownloads:
		if v.dl == nil {
			return ventry{}, fs.ErrNotExist
		}
		if rel == "" {
			return ventry{Name: downloadsName, Path: downloadsName, IsDir: true}, nil
		}
		e, err := v.dl.Stat(user, rel)
		if err != nil {
			return ventry{}, err
		}
		return ventry{Name: e.Name, Path: vpath(a, rel), IsDir: e.IsDir, Size: e.Size}, nil
	}
	if rel == "" {
		return ventry{Name: uploadsName, Path: uploadsName, IsDir: true}, nil
	}
	e, err := v.up.Stat(user, rel)
	if err != nil {
		return ventry{}, err
	}
	return ventry{Name: e.Name, Path: vpath(a, rel), IsDir: e.IsDir, Size: e.Size}, nil
}

func (v vfs) readDir(user, p string) ([]ventry, error) {
	a, rel, err := resolve(p)
	if err != nil {
		return nil, err
	}
	switch a {
	case areaRoot:
		out := []ventry{}
		if v.dl != nil {
			out = append(out, ventry{Name: downloadsName, Path: downloadsName, IsDir: true})
		}
		return append(out, ventry{Name: uploadsName, Path: uploadsName, IsDir: true}), nil
	case areaDownloads:
		if v.dl == nil {
			return nil, fs.ErrNotExist
		}
		entries, err := v.dl.ReadDir(user, rel)
		if err != nil {
			return nil, err
		}
		out := make([]ventry, 0, len(entries))
		for _, e := range entries {
			out = append(out, ventry{Name: e.Name, Path: vpath(a, e.Path), IsDir: e.IsDir, Size: e.Size})
		}
		return out, nil
	}
	entries, err := v.up.ReadDir(user, rel)
	if err != nil {
		return nil, err
	}
	out := make([]ventry, 0, len(entries))
	for _, e := range entries {
		out = append(out, ventry{Name: e.Name, Path: vpath(a, e.Path), IsDir: e.IsDir, Size: e.Size})
	}
	return out, nil
}

var errReadOnly = errors.New("downloads are read-only for clients (get and delete only)")

// denyDownloads is the client error for writes into downloads.
func denyDownloads() error { return fmt.Errorf("%w: %w", errReadOnly, fs.ErrPermission) }
