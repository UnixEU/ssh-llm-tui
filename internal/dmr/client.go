// Package dmr talks to Docker Model Runner over HTTP.
//
// The client is written against DMR v1.2.9 (image docker/model-runner, CUDA),
// whose routes are registered under InferencePrefix "/engines":
//
//   - GET  /engines/v1/models
//     OpenAI model list. Documented at
//     https://docs.docker.com/ai/model-runner/api-reference/
//   - POST /engines/v1/chat/completions
//     OpenAI chat completions, including stream:true.
//     Header X-Preload-Only: true loads the model into memory and returns
//     before inference. That is what the docker model CLI uses to warm a
//     model (cmd/cli/desktop/desktop.go Preload). The server handles it in
//     pkg/inference/scheduling/http_handler.go (tag v1.2.9).
//   - POST /engines/unload
//     Body {"all":true} stops unused runners.
//     https://github.com/docker/model-runner/blob/v1.2.9/pkg/inference/scheduling/http_handler.go
//     https://github.com/docker/model-runner/blob/v1.2.9/pkg/inference/api.go
//
// POST /models/load is not a warm call. In v1.2.9 it imports a model from a
// tar stream (docker model load). This client does not call it.
//
// Unload skips runners that are inside an inference request. DMR's loader
// only evicts runners whose reference count is zero.
package dmr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	pathModels = "/engines/v1/models"
	pathChat   = "/engines/v1/chat/completions"
	pathUnload = "/engines/unload"

	preloadHeader = "X-Preload-Only"
	userAgent     = "ssh-llm-tui/1.1.1"
)

// Model is one pulled model, as listed by GET /engines/v1/models.
type Model struct {
	ID            string
	OwnedBy       string
	Parameters    string
	Quantization  string
	Architecture  string
	Size          string
	ContextWindow int
}

// DisplayName is the id shown in the TUI. DMR lists models as
// "docker.io/ai/gpt-oss:20b"; the picker shows "ai/gpt-oss:20b".
// API calls still use Model.ID unchanged.
func (m Model) DisplayName() string {
	id := strings.TrimSpace(m.ID)
	for _, prefix := range []string{"docker.io/", "index.docker.io/"} {
		if strings.HasPrefix(id, prefix) {
			return strings.TrimPrefix(id, prefix)
		}
	}
	return id
}

// Summary is a short second line for the picker.
func (m Model) Summary() string {
	parts := make([]string, 0, 4)
	if m.Parameters != "" {
		parts = append(parts, m.Parameters)
	}
	if m.Quantization != "" {
		parts = append(parts, m.Quantization)
	}
	if m.Architecture != "" {
		parts = append(parts, m.Architecture)
	}
	if m.Size != "" {
		parts = append(parts, m.Size)
	}
	if m.ContextWindow > 0 {
		parts = append(parts, fmt.Sprintf("ctx %d", m.ContextWindow))
	}
	if len(parts) == 0 {
		return "local model"
	}
	return strings.Join(parts, " · ")
}

// Message is one OpenAI chat message.
// Message is one chat turn. Reasoning is never sent back.
// Text-only messages marshal content as a JSON string. Parts marshal content
// as an OpenAI content array (text plus image_url).
type Message struct {
	Role    string        `json:"-"`
	Content string        `json:"-"`
	Parts   []ContentPart `json:"-"`
}

// ContentPart is one OpenAI-style content element.
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL is a data URL the backend can decode.
type ImageURL struct {
	URL string `json:"url"`
}

// MarshalJSON keeps text-only content a JSON string.
func (m Message) MarshalJSON() ([]byte, error) {
	payload := struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}{Role: m.Role, Content: m.Content}
	if len(m.Parts) > 0 {
		payload.Content = m.Parts
	}
	return json.Marshal(payload)
}

// ImagePart builds a data-URL image part.
func ImagePart(mime string, data []byte) ContentPart {
	return ContentPart{
		Type:     "image_url",
		ImageURL: &ImageURL{URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)},
	}
}

// VisionRejected reports whether an error means this model cannot take images.
func VisionRejected(err error) bool {
	if err == nil {
		return false
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.Status < 400 {
			return false
		}
	} else if !looksVisionText(err.Error()) {
		return false
	}
	return looksVisionText(err.Error())
}

func looksVisionText(s string) bool {
	s = strings.ToLower(s)
	for _, n := range []string{"image", "multimodal", "mmproj", "vision", "image_url"} {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// StreamEvent is one step of a streaming completion.
type StreamEvent struct {
	Content   string
	Reasoning string
	Err       error
	Done      bool
}

// HTTPError is a non-2xx response from DMR.
type HTTPError struct {
	Op     string
	Path   string
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	msg := fmt.Sprintf("%s %s: HTTP %d", e.Op, e.Path, e.Status)
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// Client is a DMR HTTP client. It is safe for concurrent use.
type Client struct {
	baseURL        string
	http           *http.Client
	requestTimeout time.Duration
	warmTimeout    time.Duration
}

// Options configures a Client.
type Options struct {
	BaseURL        string
	HTTP           *http.Client
	RequestTimeout time.Duration
	WarmTimeout    time.Duration
}

// New returns a client. baseURL is the DMR origin, without a path prefix.
func New(opts Options) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	u, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("dmr base url: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("dmr base url %q must be an absolute http(s) URL", opts.BaseURL)
	}
	hc := opts.HTTP
	if hc == nil {
		hc = &http.Client{
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				DisableCompression:  true,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
				ForceAttemptHTTP2:   false,
			},
		}
	}
	return &Client{
		baseURL:        base,
		http:           hc,
		requestTimeout: opts.RequestTimeout,
		warmTimeout:    opts.WarmTimeout,
	}, nil
}

// ListModels returns models already pulled into DMR. The slice is empty when
// DMR has no models. It is never nil.
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	ctx, cancel := c.withTimeout(ctx, c.requestTimeout)
	defer cancel()

	req, err := c.newRequest(ctx, http.MethodGet, pathModels, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("list models: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{Op: "list models", Path: pathModels, Status: resp.StatusCode, Body: clip(body)}
	}

	var payload openAIList
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("list models: decode: %w", err)
	}
	models := make([]Model, 0, len(payload.Data))
	for _, item := range payload.Data {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		m := Model{ID: item.ID, OwnedBy: item.OwnedBy}
		if item.DMR != nil {
			m.Parameters = string(item.DMR.Parameters)
			m.Quantization = string(item.DMR.Quantization)
			m.Architecture = string(item.DMR.Architecture)
			m.Size = string(item.DMR.Size)
			if item.DMR.ContextWindow != nil {
				m.ContextWindow = int(*item.DMR.ContextWindow)
			}
		}
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// Warm loads model into the inference runner and returns when DMR says the
// runner is ready. It does not generate text.
func (c *Client) Warm(ctx context.Context, model string) error {
	ctx, cancel := c.withTimeout(ctx, c.warmTimeout)
	defer cancel()

	body, err := json.Marshal(chatRequest{
		Model:    model,
		Messages: []Message{},
	})
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, pathChat, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(preloadHeader, "true")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("warm %s: %w", model, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{Op: "warm " + model, Path: pathChat, Status: resp.StatusCode, Body: clip(respBody)}
	}
	return nil
}

// UnloadAll asks DMR to stop every runner that is not currently in a request.
// The returned count is unloaded_runners from the JSON body.
func (c *Client) UnloadAll(ctx context.Context) (int, error) {
	ctx, cancel := c.withTimeout(ctx, c.requestTimeout)
	defer cancel()

	body := []byte(`{"all":true}`)
	req, err := c.newRequest(ctx, http.MethodPost, pathUnload, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("unload: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return 0, fmt.Errorf("unload: read body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, &HTTPError{Op: "unload", Path: pathUnload, Status: resp.StatusCode, Body: clip(respBody)}
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return 0, nil
	}
	var parsed unloadResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return 0, fmt.Errorf("unload: decode: %w", err)
	}
	return parsed.UnloadedRunners, nil
}

// ChatStream starts a streaming chat completion. Events are sent until Done
// or Err, then the channel is closed. The call itself does not block on the
// network. Cancelling ctx stops the stream.
func (c *Client) ChatStream(ctx context.Context, model string, messages []Message) <-chan StreamEvent {
	out := make(chan StreamEvent)
	go func() {
		defer close(out)
		c.stream(ctx, model, messages, out)
	}()
	return out
}

func (c *Client) stream(ctx context.Context, model string, messages []Message, out chan<- StreamEvent) {
	body, err := json.Marshal(chatRequest{
		Model:    model,
		Messages: messages,
		Stream:   true,
	})
	if err != nil {
		send(ctx, out, StreamEvent{Err: err})
		return
	}
	req, err := c.newRequest(ctx, http.MethodPost, pathChat, bytes.NewReader(body))
	if err != nil {
		send(ctx, out, StreamEvent{Err: err})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		send(ctx, out, StreamEvent{Err: fmt.Errorf("chat: %w", err)})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		send(ctx, out, StreamEvent{Err: &HTTPError{Op: "chat", Path: pathChat, Status: resp.StatusCode, Body: clip(b)}})
		return
	}

	reader := bufio.NewReader(resp.Body)
	peek, err := reader.Peek(1)
	if err != nil {
		if errors.Is(err, io.EOF) {
			send(ctx, out, StreamEvent{Done: true})
			return
		}
		send(ctx, out, StreamEvent{Err: fmt.Errorf("chat: read: %w", err)})
		return
	}
	if peek[0] == '{' {
		raw, err := io.ReadAll(io.LimitReader(reader, 8<<20))
		if err != nil {
			send(ctx, out, StreamEvent{Err: fmt.Errorf("chat: read: %w", err)})
			return
		}
		ev, err := eventFromJSON(raw)
		if err != nil {
			send(ctx, out, StreamEvent{Err: err})
			return
		}
		if ev.Content != "" || ev.Reasoning != "" {
			if !send(ctx, out, ev) {
				return
			}
		}
		send(ctx, out, StreamEvent{Done: true})
		return
	}

	sc := bufio.NewScanner(reader)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if ctx.Err() != nil {
			send(ctx, out, StreamEvent{Err: ctx.Err()})
			return
		}
		line := strings.TrimRight(sc.Text(), "\r")
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			send(ctx, out, StreamEvent{Done: true})
			return
		}
		ev, err := eventFromJSON([]byte(data))
		if err != nil {
			send(ctx, out, StreamEvent{Err: fmt.Errorf("chat: decode chunk: %w", err)})
			return
		}
		if ev.Err != nil {
			send(ctx, out, ev)
			return
		}
		if ev.Content == "" && ev.Reasoning == "" {
			continue
		}
		if !send(ctx, out, ev) {
			return
		}
	}
	if err := sc.Err(); err != nil {
		send(ctx, out, StreamEvent{Err: fmt.Errorf("chat: stream: %w", err)})
		return
	}
	send(ctx, out, StreamEvent{Done: true})
}

func eventFromJSON(raw []byte) (StreamEvent, error) {
	var chunk streamChunk
	if err := json.Unmarshal(raw, &chunk); err != nil {
		return StreamEvent{}, err
	}
	if chunk.Error != nil && chunk.Error.Message != "" {
		return StreamEvent{Err: errors.New(chunk.Error.Message)}, nil
	}
	ev := StreamEvent{}
	if len(chunk.Choices) > 0 {
		delta := chunk.Choices[0].Delta
		msg := chunk.Choices[0].Message
		ev.Content = firstText(string(delta.Content), string(msg.Content))
		// gpt-oss (and some llama.cpp builds) stream chain-of-thought in
		// reasoning or reasoning_content. Either may be null, a string, or
		// some other JSON type. Non-strings are ignored so a chunk cannot
		// abort the reply.
		ev.Reasoning = firstText(
			string(delta.Reasoning),
			string(delta.ReasoningContent),
			string(msg.Reasoning),
			string(msg.ReasoningContent),
		)
	}
	return ev, nil
}

func firstText(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func send(ctx context.Context, out chan<- StreamEvent, ev StreamEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- ev:
		return true
	}
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	ref := &url.URL{Path: path}
	req, err := http.NewRequestWithContext(ctx, method, u.ResolveReference(ref).String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return req, nil
}

func (c *Client) withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

func clip(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

type openAIList struct {
	Object string        `json:"object"`
	Data   []openAIModel `json:"data"`
}

type openAIModel struct {
	ID      string   `json:"id"`
	OwnedBy string   `json:"owned_by"`
	DMR     *dmrMeta `json:"dmr"`
}

type dmrMeta struct {
	ContextWindow *float64   `json:"context_window"`
	Architecture  flexString `json:"architecture"`
	Parameters    flexString `json:"parameters"`
	Quantization  flexString `json:"quantization"`
	Size          flexString `json:"size"`
}

type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	*f = flexString(strings.TrimSpace(string(b)))
	return nil
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream,omitempty"`
}

type unloadResponse struct {
	UnloadedRunners int `json:"unloaded_runners"`
}

// looseString decodes a JSON string. Null and non-strings become empty
// instead of failing the decode, so an odd delta cannot break the stream.
type looseString string

func (s *looseString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] != '"' {
		return nil
	}
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	*s = looseString(v)
	return nil
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          looseString `json:"content"`
			Reasoning        looseString `json:"reasoning"`
			ReasoningContent looseString `json:"reasoning_content"`
		} `json:"delta"`
		Message struct {
			Content          looseString `json:"content"`
			Reasoning        looseString `json:"reasoning"`
			ReasoningContent looseString `json:"reasoning_content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}
