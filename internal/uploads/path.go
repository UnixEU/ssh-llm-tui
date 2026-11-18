package uploads

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"unicode"
)

const (
	// MaxDepth is the deepest relative path accepted, counted in components.
	MaxDepth = 16
	// MaxPathLen caps the cleaned relative path in bytes.
	MaxPathLen = 1024
	// MaxComponentLen caps one path component in bytes.
	MaxComponentLen = 255
)

// DefaultIgnore is the list of path components that are accepted from the
// client but never stored.
var DefaultIgnore = []string{".git", "node_modules", ".venv", "__pycache__", ".DS_Store"}

// ErrInvalidPath wraps every path rejection so callers can map it to a
// permission error.
var ErrInvalidPath = errors.New("invalid path")

type pathError struct {
	path   string
	reason string
}

func (e *pathError) Error() string {
	return fmt.Sprintf("invalid path %q: %s", e.path, e.reason)
}

func (e *pathError) Unwrap() []error { return []error{ErrInvalidPath, fs.ErrPermission} }

// CleanPath validates a path relative to the user's upload directory and
// returns it in canonical slash form, for example "docs/api/intro.md".
// Backslashes count as separators and one trailing slash is dropped.
// Absolute paths, empty, "." and ".." components, paths deeper than
// MaxDepth and paths longer than MaxPathLen are rejected. Control
// characters are removed from each component.
func CleanPath(p string) (string, error) {
	orig := p
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return "", &pathError{orig, "empty"}
	}
	if strings.HasPrefix(p, "/") {
		return "", &pathError{orig, "absolute"}
	}
	p = strings.TrimSuffix(p, "/")
	parts := strings.Split(p, "/")
	if len(parts) > MaxDepth {
		return "", &pathError{orig, fmt.Sprintf("deeper than %d levels", MaxDepth)}
	}
	for i, part := range parts {
		if part == "" {
			return "", &pathError{orig, "empty component"}
		}
		if part == "." || part == ".." {
			return "", &pathError{orig, "dot component"}
		}
		var b strings.Builder
		for _, r := range part {
			if r < 32 || r == 127 || unicode.IsControl(r) {
				continue
			}
			b.WriteRune(r)
		}
		clean := b.String()
		if strings.TrimSpace(clean) == "" || clean == "." || clean == ".." {
			return "", &pathError{orig, "empty component"}
		}
		if len(clean) > MaxComponentLen {
			return "", &pathError{orig, fmt.Sprintf("component longer than %d bytes", MaxComponentLen)}
		}
		parts[i] = clean
	}
	out := strings.Join(parts, "/")
	if len(out) > MaxPathLen {
		return "", &pathError{orig, fmt.Sprintf("longer than %d bytes", MaxPathLen)}
	}
	return out, nil
}

// ParseIgnore turns a comma-separated list into a set of component names.
// "none" or "off" returns an empty set.
func ParseIgnore(list string) []string {
	list = strings.TrimSpace(list)
	switch strings.ToLower(list) {
	case "", "none", "off", "-":
		return nil
	}
	var out []string
	for _, item := range strings.Split(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" || strings.ContainsAny(item, "/\\") {
			continue
		}
		out = append(out, item)
	}
	return out
}

// parentDirs returns every ancestor of a clean relative path, nearest last:
// "a/b/c" gives ["a", "a/b"].
func parentDirs(p string) []string {
	var out []string
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			out = append(out, p[:i])
		}
	}
	return out
}

// under reports whether p is dir itself or inside it. dir "" is the root.
func under(p, dir string) bool {
	if dir == "" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}
