package transfer

import (
	"os"
	"path"
	"strings"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
)

// uploadTarget maps a client path onto the flat per-user upload directory.
// The directory itself is reported as dir=true for "", ".", "/", "~", "~/".
// and "uploads" or "uploads/". A file is "name" or "uploads/name".
// Any deeper path is rejected.
func uploadTarget(p string) (name string, dir bool, err error) {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	switch {
	case p == "~", p == "~/":
		p = "/"
	case strings.HasPrefix(p, "~/"):
		p = "/" + strings.TrimPrefix(p, "~/")
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(p, "/"))
	if cleaned == "/" || cleaned == "." {
		return "", true, nil
	}
	rel := strings.TrimPrefix(cleaned, "/")
	if rel == "uploads" {
		return "", true, nil
	}
	rel = strings.TrimPrefix(rel, "uploads/")
	if rel == "" {
		return "", true, nil
	}
	if strings.Contains(rel, "/") {
		return "", false, os.ErrPermission
	}
	name, err = uploads.CleanName(rel)
	if err != nil {
		return "", false, err
	}
	return name, false, nil
}
