package dmr

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListModels(t *testing.T) {
	var gotPath, gotAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAgent = r.Header.Get("User-Agent")
		if r.Method != http.MethodGet {
			t.Errorf("method %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		// Shape captured from DMR v1.2.9 on the host (empty dmr objects).
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"docker.io/ai/gpt-oss:20b","object":"model","created":0,"owned_by":"docker","dmr":{}},{"id":"docker.io/ai/qwen3.8:27b-q4_K_M","object":"model","created":0,"owned_by":"docker","dmr":{}},{"id":"","object":"model"}]}`)
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != pathModels {
		t.Fatalf("path %s", gotPath)
	}
	if gotAgent != userAgent {
		t.Fatalf("user agent %q", gotAgent)
	}
	if len(models) != 2 {
		t.Fatalf("len %d", len(models))
	}
	if models[0].ID != "docker.io/ai/gpt-oss:20b" || models[1].ID != "docker.io/ai/qwen3.8:27b-q4_K_M" {
		t.Fatalf("order %+v", models)
	}
	if models[0].DisplayName() != "ai/gpt-oss:20b" || models[1].DisplayName() != "ai/qwen3.8:27b-q4_K_M" {
		t.Fatalf("display names %+v %+v", models[0], models[1])
	}
	if models[0].OwnedBy != "docker" || models[0].Summary() != "local model" {
		t.Fatalf("empty dmr metadata not handled: %+v", models[0])
	}
}

func TestListModelsMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"ai/zeta","object":"model","owned_by":"docker","dmr":{"parameters":"7B","quantization":"Q4_K_M","architecture":"llama","size":"4GB","context_window":8192}},{"id":"ai/alpha","dmr":{"parameters":360,"quantization":"Q4"}}]}`)
	}))
	defer srv.Close()
	models, err := testClient(t, srv.URL).ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if models[0].ID != "ai/alpha" || models[0].Parameters != "360" || models[0].DisplayName() != "ai/alpha" {
		t.Fatalf("alpha %+v", models[0])
	}
	if models[1].ContextWindow != 8192 || !strings.Contains(models[1].Summary(), "Q4_K_M") {
		t.Fatalf("zeta %+v", models[1])
	}
}

func TestListModelsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if models == nil || len(models) != 0 {
		t.Fatalf("models = %#v", models)
	}
}

func TestListModelsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "runner down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	_, err := c.ListModels(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v", err)
	}
	if httpErr.Status != http.StatusServiceUnavailable || httpErr.Path != pathModels {
		t.Fatalf("http err %+v", httpErr)
	}
}

func TestListModelsBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{`)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	if _, err := c.ListModels(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}

func TestWarm(t *testing.T) {
	var gotPath, preload, ctype string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		preload = r.Header.Get(preloadHeader)
		ctype = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if r.URL.Path == pathModels+"/load" || r.URL.Path == "/models/load" {
			t.Errorf("warm must not call %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := testClient(t, srv.URL+"/")
	if err := c.Warm(context.Background(), "ai/smollm2"); err != nil {
		t.Fatal(err)
	}
	if gotPath != pathChat {
		t.Fatalf("path %s", gotPath)
	}
	if preload != "true" {
		t.Fatalf("preload header %q", preload)
	}
	if ctype != "application/json" {
		t.Fatalf("content type %q", ctype)
	}
	if !strings.Contains(gotBody, `"model":"ai/smollm2"`) || !strings.Contains(gotBody, `"messages":[]`) {
		t.Fatalf("body %s", gotBody)
	}
	if strings.Contains(gotBody, `"stream"`) {
		t.Fatalf("warm body should not stream: %s", gotBody)
	}
}

func TestWarmError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	err := c.Warm(context.Background(), "missing")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestUnloadAll(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"unloaded_runners":2}`)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	n, err := c.UnloadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != pathUnload {
		t.Fatalf("path %s", gotPath)
	}
	if gotBody != `{"all":true}` {
		t.Fatalf("body %s", gotBody)
	}
	if n != 2 {
		t.Fatalf("unloaded %d", n)
	}
}

func TestChatStreamSSE(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(preloadHeader) != "" {
			t.Errorf("chat set preload header")
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, ": keep-alive\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	c := testClient(t, srv.URL)
	ch := c.ChatStream(context.Background(), "ai/smollm2", []Message{{Role: "user", Content: "hi"}})
	var content, reasoning string
	done := false
	for ev := range ch {
		if ev.Err != nil {
			t.Fatal(ev.Err)
		}
		content += ev.Content
		reasoning += ev.Reasoning
		if ev.Done {
			done = true
		}
	}
	if !done {
		t.Fatal("missing done")
	}
	if content != "Hello" || reasoning != "think" {
		t.Fatalf("content %q reasoning %q", content, reasoning)
	}
	if !strings.Contains(gotBody, `"stream":true`) || !strings.Contains(gotBody, `"hi"`) {
		t.Fatalf("body %s", gotBody)
	}
}

func TestChatStreamJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"plain"}}]}`)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	var got string
	done := false
	for ev := range c.ChatStream(context.Background(), "m", []Message{{Role: "user", Content: "q"}}) {
		if ev.Err != nil {
			t.Fatal(ev.Err)
		}
		got += ev.Content
		done = done || ev.Done
	}
	if got != "plain" || !done {
		t.Fatalf("got %q done %v", got, done)
	}
}

func TestChatStreamErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "oom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	ev := <-c.ChatStream(context.Background(), "m", nil)
	var httpErr *HTTPError
	if !errors.As(ev.Err, &httpErr) || httpErr.Status != 500 {
		t.Fatalf("event %+v", ev)
	}
}

func TestChatStreamReasoningToleratesOddJSON(t *testing.T) {
	var sentModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		sentModel = body.Model
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		// gpt-oss style: reasoning string, null content, then a non-string
		// reasoning_content that must not fail the decode.
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":null,\"reasoning\":\"think\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":{\"partial\":true},\"content\":\"Hi\"}}]}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	const fullID = "docker.io/ai/gpt-oss:20b"
	var content, reasoning string
	for ev := range testClient(t, srv.URL).ChatStream(context.Background(), fullID, []Message{{Role: "user", Content: "hi"}}) {
		if ev.Err != nil {
			t.Fatal(ev.Err)
		}
		content += ev.Content
		reasoning += ev.Reasoning
	}
	if sentModel != fullID {
		t.Fatalf("API model = %q, want the full id", sentModel)
	}
	if reasoning != "think" || content != "Hi" {
		t.Fatalf("content %q reasoning %q", content, reasoning)
	}
}

func TestChatStreamAPIErrorEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"error\":{\"message\":\"context length exceeded\"}}\n\n")
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	ev := <-c.ChatStream(context.Background(), "m", nil)
	if ev.Err == nil || !strings.Contains(ev.Err.Error(), "context length") {
		t.Fatalf("event %+v", ev)
	}
}

func TestChatStreamCancel(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		flusher.Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	ch := c.ChatStream(ctx, "m", []Message{{Role: "user", Content: "q"}})
	<-started
	cancel()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("stream did not stop after cancel")
		}
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	if _, err := New(Options{BaseURL: "://"}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := New(Options{BaseURL: "ftp://example.com"}); err == nil {
		t.Fatal("expected error")
	}
}

func testClient(t *testing.T, base string) *Client {
	t.Helper()
	c, err := New(Options{
		BaseURL:        base,
		RequestTimeout: 2 * time.Second,
		WarmTimeout:    2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
