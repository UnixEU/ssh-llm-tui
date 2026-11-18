package dmr

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestMessageJSONTextStaysString(t *testing.T) {
	b, err := json.Marshal(Message{Role: "user", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["content"] != "hi" {
		t.Fatalf("content %#v", raw["content"])
	}
}

func TestMessageJSONImageParts(t *testing.T) {
	msg := Message{
		Role: "user",
		Parts: []ContentPart{
			{Type: "text", Text: "look"},
			ImagePart("image/png", []byte{1, 2, 3}),
		},
	}
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"image_url"`) || !strings.Contains(string(b), "data:image/png;base64,") {
		t.Fatalf("body %s", b)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	parts, ok := raw["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("content %#v", raw["content"])
	}
}

func TestVisionRejected(t *testing.T) {
	err := &HTTPError{Op: "chat", Path: "/engines/v1/chat/completions", Status: 400, Body: "missing mmproj for image input"}
	if !VisionRejected(err) {
		t.Fatal("expected vision rejection")
	}
	plain := &HTTPError{Status: 500, Body: "cuda out of memory"}
	if VisionRejected(plain) {
		t.Fatal("oom is not a vision rejection")
	}
	if VisionRejected(errors.New("connection refused")) {
		t.Fatal("transport error is not a vision rejection")
	}
}
