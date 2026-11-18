// Package modelstore finds the GGUF files of a Docker Model Runner model in
// a read-only mount of the runner's model store, so the fit check can read
// model headers. It never writes to the store.
//
// Store layout (DMR pkg/distribution/internal/store):
//
//	models.json                 {"models":[{"id":"sha256:…","tags":[…],"files":["sha256:…"]}]}
//	manifests/sha256/<hex>      OCI manifest of the model
//	blobs/sha256/<hex>          layer contents (GGUF, mmproj, templates…)
package modelstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ErrNotFound means the model is not in the store index.
var ErrNotFound = errors.New("model not found in the model store")

var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Store is a read-only view of a DMR model store.
type Store struct {
	root string
}

// Open locates models.json at path or at path/models. An empty path
// returns (nil, nil): the store is not configured.
func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	for _, root := range []string{path, filepath.Join(path, "models")} {
		if st, err := os.Stat(filepath.Join(root, "models.json")); err == nil && st.Mode().IsRegular() {
			return &Store{root: root}, nil
		}
	}
	return nil, fmt.Errorf("model store %s: models.json not found (is the DMR models volume mounted?)", path)
}

// Root is the directory that holds models.json.
func (s *Store) Root() string { return s.root }

// Files lists the model's blobs by role.
type Files struct {
	// ID is the store's model id (manifest digest).
	ID string
	// Weights are GGUF blobs, one per shard.
	Weights []string
	// Projectors are multimodal projector blobs.
	Projectors []string
}

type index struct {
	Models []struct {
		ID    string   `json:"id"`
		Tags  []string `json:"tags"`
		Files []string `json:"files"`
	} `json:"models"`
}

type manifest struct {
	Layers []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
	} `json:"layers"`
}

// Lookup resolves a model reference (tag as listed by DMR, or store id) to
// its blob paths.
func (s *Store) Lookup(ref string) (Files, error) {
	if s == nil {
		return Files{}, errors.New("model store is not configured")
	}
	raw, err := readLimited(filepath.Join(s.root, "models.json"), 32<<20)
	if err != nil {
		return Files{}, err
	}
	var idx index
	if err := json.Unmarshal(raw, &idx); err != nil {
		return Files{}, fmt.Errorf("models.json: %w", err)
	}
	want := Normalize(ref)
	for _, m := range idx.Models {
		match := m.ID == ref
		for _, tag := range m.Tags {
			if Normalize(tag) == want {
				match = true
			}
		}
		if !match {
			continue
		}
		files, err := s.filesFromManifest(m.ID)
		if err == nil && len(files.Weights) > 0 {
			return files, nil
		}
		return s.filesFromIndex(m.ID, m.Files)
	}
	return Files{}, ErrNotFound
}

func (s *Store) filesFromManifest(id string) (Files, error) {
	p, err := s.digestPath("manifests", id)
	if err != nil {
		return Files{}, err
	}
	raw, err := readLimited(p, 4<<20)
	if err != nil {
		return Files{}, err
	}
	var man manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return Files{}, err
	}
	out := Files{ID: id}
	for _, l := range man.Layers {
		mt := strings.ToLower(l.MediaType)
		bp, err := s.digestPath("blobs", l.Digest)
		if err != nil {
			continue
		}
		switch {
		case strings.Contains(mt, "mmproj"):
			out.Projectors = append(out.Projectors, bp)
		case strings.Contains(mt, "gguf"):
			out.Weights = append(out.Weights, bp)
		}
	}
	return out, nil
}

// filesFromIndex is the fallback when the manifest is missing or has no
// GGUF layer: every listed blob that starts with the GGUF magic is a
// candidate. The caller tells weights from projectors by architecture.
func (s *Store) filesFromIndex(id string, digests []string) (Files, error) {
	out := Files{ID: id}
	for _, d := range digests {
		bp, err := s.digestPath("blobs", d)
		if err != nil {
			continue
		}
		if isGGUF(bp) {
			out.Weights = append(out.Weights, bp)
		}
	}
	if len(out.Weights) == 0 {
		return Files{}, fmt.Errorf("model %s has no GGUF blob in the store", id)
	}
	return out, nil
}

// digestPath maps "sha256:<hex>" to dir/sha256/<hex>. Anything else is
// rejected, so a crafted index cannot point outside the store.
func (s *Store) digestPath(dir, digest string) (string, error) {
	algo, hex, ok := strings.Cut(digest, ":")
	if !ok || algo != "sha256" || !hexDigest.MatchString(hex) {
		return "", fmt.Errorf("unsupported digest %q", digest)
	}
	return filepath.Join(s.root, dir, algo, hex), nil
}

// Normalize makes DMR model references comparable: it drops the default
// registry prefix and adds the implicit :latest tag.
func Normalize(ref string) string {
	ref = strings.ToLower(strings.TrimSpace(ref))
	for _, p := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		ref = strings.TrimPrefix(ref, p)
	}
	if strings.HasPrefix(ref, "sha256:") || strings.Contains(ref, "@") {
		return ref
	}
	last := ref[strings.LastIndex(ref, "/")+1:]
	if !strings.Contains(last, ":") {
		ref += ":latest"
	}
	return ref
}

func isGGUF(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var b [4]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return false
	}
	return string(b[:]) == "GGUF"
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(path), limit)
	}
	return b, nil
}
