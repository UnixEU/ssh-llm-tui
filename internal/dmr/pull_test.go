package dmr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func pullClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Options{BaseURL: srv.URL, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPullStreamsProgress(t *testing.T) {
	c := pullClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/models/create" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("request %s %s accept %q", r.Method, r.URL.Path, r.Header.Get("Accept"))
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["from"] != "ai/qwen3:8b" {
			t.Errorf("from = %q", body["from"])
		}
		fmt.Fprintln(w, `{"type":"progress","message":"Downloading","total":100,"layer":{"id":"a","size":80,"current":40}}`)
		fmt.Fprintln(w, `{"type":"progress","total":100,"layer":{"id":"b","size":20,"current":20}}`)
		fmt.Fprintln(w, `{"type":"progress","total":100,"layer":{"id":"a","size":80,"current":80}}`)
		fmt.Fprintln(w, `{"type":"success","message":"Model pulled successfully"}`)
	})
	var seen []PullProgress
	if err := c.Pull(context.Background(), "ai/qwen3:8b", func(p PullProgress) { seen = append(seen, p) }); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 4 || seen[0].Done != 40 || seen[1].Done != 60 || seen[2].Done != 100 || seen[2].Total != 100 {
		t.Fatalf("progress = %+v", seen)
	}
	if seen[3].Message != "Model pulled successfully" {
		t.Fatalf("last = %+v", seen[3])
	}
}

func TestPullErrors(t *testing.T) {
	c := pullClient(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["from"] {
		case "missing":
			http.Error(w, "Model not found", http.StatusNotFound)
		case "line":
			fmt.Fprintln(w, `{"type":"error","message":"disk full"}`)
		case "text":
			fmt.Fprintln(w, `{"type":"progress","layer":{"id":"a","size":1,"current":0}}`)
			fmt.Fprintln(w, `write failed: no space left on device`)
		}
	})
	if err := c.Pull(context.Background(), "missing", nil); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("missing: %v", err)
	}
	if err := c.Pull(context.Background(), "line", nil); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("line: %v", err)
	}
	if err := c.Pull(context.Background(), "text", nil); err == nil || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("text: %v", err)
	}
}

func TestPullCancel(t *testing.T) {
	release := make(chan struct{})
	c := pullClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"type":"progress","layer":{"id":"a","size":10,"current":1}}`)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	err := c.Pull(ctx, "ai/x", func(PullProgress) { cancel() })
	if err != context.Canceled {
		t.Fatalf("err = %v", err)
	}
}
