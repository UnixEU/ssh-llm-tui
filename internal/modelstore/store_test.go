package modelstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	manifestHex = "1111111111111111111111111111111111111111111111111111111111111111"
	weightsHex  = "2222222222222222222222222222222222222222222222222222222222222222"
	projHex     = "3333333333333333333333333333333333333333333333333333333333333333"
)

func writeStore(t *testing.T, root string, withManifest bool) {
	t.Helper()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "blobs", "sha256"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "manifests", "sha256"), 0o755))
	idx := map[string]any{"models": []map[string]any{{
		"id":    "sha256:" + manifestHex,
		"tags":  []string{"ai/example:7b"},
		"files": []string{"sha256:" + weightsHex, "sha256:" + projHex},
	}}}
	raw, _ := json.Marshal(idx)
	must(os.WriteFile(filepath.Join(root, "models.json"), raw, 0o644))
	must(os.WriteFile(filepath.Join(root, "blobs", "sha256", weightsHex), []byte("GGUF...."), 0o644))
	must(os.WriteFile(filepath.Join(root, "blobs", "sha256", projHex), []byte("GGUF...."), 0o644))
	if withManifest {
		man := map[string]any{"layers": []map[string]any{
			{"mediaType": "application/vnd.docker.ai.gguf.v3", "digest": "sha256:" + weightsHex},
			{"mediaType": "application/vnd.docker.ai.mmproj", "digest": "sha256:" + projHex},
			{"mediaType": "application/vnd.docker.ai.license", "digest": "sha256:" + strings.Repeat("4", 64)},
		}}
		raw, _ := json.Marshal(man)
		must(os.WriteFile(filepath.Join(root, "manifests", "sha256", manifestHex), raw, 0o644))
	}
}

func TestLookupByTagUsesManifest(t *testing.T) {
	dir := t.TempDir()
	writeStore(t, dir, true)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"docker.io/ai/example:7b", "ai/example:7b", "sha256:" + manifestHex} {
		f, err := s.Lookup(ref)
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if len(f.Weights) != 1 || !strings.HasSuffix(f.Weights[0], weightsHex) {
			t.Fatalf("%s: weights %v", ref, f.Weights)
		}
		if len(f.Projectors) != 1 || !strings.HasSuffix(f.Projectors[0], projHex) {
			t.Fatalf("%s: projectors %v", ref, f.Projectors)
		}
	}
}

func TestLookupFallsBackToIndexFiles(t *testing.T) {
	dir := t.TempDir()
	writeStore(t, dir, false)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Lookup("ai/example:7b")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Weights) != 2 {
		t.Fatalf("weights %v", f.Weights)
	}
}

func TestOpenFindsNestedRoot(t *testing.T) {
	dir := t.TempDir()
	writeStore(t, filepath.Join(dir, "models"), true)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Root() != filepath.Join(dir, "models") {
		t.Fatalf("root %s", s.Root())
	}
}

func TestOpenEmptyPathIsDisabled(t *testing.T) {
	s, err := Open("")
	if s != nil || err != nil {
		t.Fatalf("got %v, %v", s, err)
	}
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("directory without models.json accepted")
	}
}

func TestUnknownModel(t *testing.T) {
	dir := t.TempDir()
	writeStore(t, dir, true)
	s, _ := Open(dir)
	if _, err := s.Lookup("ai/other:1b"); err != ErrNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestDigestPathRejectsTraversal(t *testing.T) {
	s := &Store{root: "/store"}
	for _, d := range []string{"sha256:../../etc/passwd", "md5:abc", "sha256:" + strings.Repeat("g", 64), "nocolon"} {
		if _, err := s.digestPath("blobs", d); err == nil {
			t.Fatalf("%q accepted", d)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"docker.io/ai/smollm2":       "ai/smollm2:latest",
		"ai/smollm2:latest":          "ai/smollm2:latest",
		"index.docker.io/ai/x:Q4":    "ai/x:q4",
		"registry.local:5000/a/b":    "registry.local:5000/a/b:latest",
		"registry.local:5000/a/b:v1": "registry.local:5000/a/b:v1",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
