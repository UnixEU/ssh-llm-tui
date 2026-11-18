package transfer

import (
	"fmt"
	"path"
	"strings"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
)

// uploadTarget maps a client path onto a path relative to the per-user
// upload directory. "" is that directory itself: "", ".", "/", "~", "~/",
// "uploads" and "/uploads". A single leading "uploads/" is dropped, so
// "/uploads/docs/a.md", "uploads/docs/a.md" and "docs/a.md" are the same
// file. ".." can never climb above the upload directory, and every
// component is checked by uploads.CleanPath.
func uploadTarget(p string) (string, error) {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	switch {
	case p == "~", p == "~/":
		p = "/"
	case strings.HasPrefix(p, "~/"):
		p = "/" + strings.TrimPrefix(p, "~/")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", fmt.Errorf("invalid path %q: dot-dot component: %w", p, uploads.ErrInvalidPath)
		}
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(p, "/"))
	rel := strings.TrimPrefix(cleaned, "/")
	if rel == "" || rel == "uploads" {
		return "", nil
	}
	rel = strings.TrimPrefix(rel, "uploads/")
	return uploads.CleanPath(rel)
}
