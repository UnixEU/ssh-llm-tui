package dmr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Runtime configuration and runner state, as used by `docker model configure`
// and `docker model ps`. Routes (DMR v1.2.9,
// pkg/inference/scheduling/http_handler.go):
//
//   - POST /engines/_configure     per-model context size and llama.cpp
//     runtime flags. 202 on success, 409 when the model's runner is busy.
//     Settings live in DMR memory and are lost when DMR restarts.
//   - GET  /engines/_configure?model=…
//   - GET  /engines/ps             running runners
const (
	pathConfigure = "/engines/_configure"
	pathPS        = "/engines/ps"
)

// ErrRunnerBusy means DMR refused a configuration change because the
// model's runner is serving a request (HTTP 409 "runner already active").
var ErrRunnerBusy = errors.New("the model is busy in another session")

// ConfigureRequest is the body of POST /engines/_configure. Field names
// match DMR's scheduling.ConfigureRequest.
type ConfigureRequest struct {
	Model        string   `json:"model"`
	ContextSize  *int32   `json:"context-size,omitempty"`
	RuntimeFlags []string `json:"runtime-flags,omitempty"`
}

// CLI renders the equivalent docker model command, for display.
func (r ConfigureRequest) CLI() string {
	parts := []string{"docker model configure"}
	if r.ContextSize != nil {
		parts = append(parts, fmt.Sprintf("--context-size %d", *r.ContextSize))
	}
	parts = append(parts, r.Model)
	if len(r.RuntimeFlags) > 0 {
		parts = append(parts, "--")
		parts = append(parts, r.RuntimeFlags...)
	}
	return strings.Join(parts, " ")
}

// Configure sets a model's runtime configuration. DMR evicts an idle runner
// of that model so the next load uses the new settings, and starts a
// background preload. Callers must decide that the model fits first.
func (c *Client) Configure(ctx context.Context, req ConfigureRequest) error {
	ctx, cancel := c.withTimeout(ctx, c.requestTimeout)
	defer cancel()
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	hreq, err := c.newRequest(ctx, http.MethodPost, pathConfigure, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(hreq)
	if err != nil {
		return fmt.Errorf("configure %s: %w", req.Model, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusConflict:
		return fmt.Errorf("%w: %s", ErrRunnerBusy, clip(respBody))
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return &HTTPError{Op: "configure " + req.Model, Path: pathConfigure, Status: resp.StatusCode, Body: clip(respBody)}
	}
	return nil
}

// ModelConfig is one entry from GET /engines/_configure.
type ModelConfig struct {
	Backend      string
	Model        string
	ContextSize  int
	RuntimeFlags []string
}

type configEntry struct {
	Backend string `json:"Backend"`
	Model   string `json:"Model"`
	Config  struct {
		ContextSize  *int32   `json:"context-size"`
		RuntimeFlags []string `json:"runtime-flags"`
	} `json:"Config"`
}

// Configs lists runtime configurations, filtered to model when not empty.
func (c *Client) Configs(ctx context.Context, model string) ([]ModelConfig, error) {
	path := pathConfigure
	if model != "" {
		path += "?model=" + url.QueryEscape(model)
	}
	body, err := c.getJSON(ctx, "list configs", path)
	if err != nil {
		return nil, err
	}
	var entries []configEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("list configs: decode: %w", err)
	}
	out := make([]ModelConfig, 0, len(entries))
	for _, e := range entries {
		mc := ModelConfig{Backend: e.Backend, Model: e.Model, RuntimeFlags: e.Config.RuntimeFlags}
		if e.Config.ContextSize != nil {
			mc.ContextSize = int(*e.Config.ContextSize)
		}
		out = append(out, mc)
	}
	return out, nil
}

// Runner is one entry from GET /engines/ps.
type Runner struct {
	Backend  string    `json:"backend_name"`
	Model    string    `json:"model_name"`
	Mode     string    `json:"mode"`
	LastUsed time.Time `json:"last_used"`
	InUse    bool      `json:"in_use"`
	Loading  bool      `json:"loading"`
}

// Running lists runners DMR has loaded.
func (c *Client) Running(ctx context.Context) ([]Runner, error) {
	body, err := c.getJSON(ctx, "list runners", pathPS)
	if err != nil {
		return nil, err
	}
	var out []Runner
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("list runners: decode: %w", err)
	}
	return out, nil
}

func (c *Client) getJSON(ctx context.Context, op, path string) ([]byte, error) {
	ctx, cancel := c.withTimeout(ctx, c.requestTimeout)
	defer cancel()
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	ref, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.ResolveReference(ref).String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: read body: %w", op, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &HTTPError{Op: op, Path: path, Status: resp.StatusCode, Body: clip(body)}
	}
	return body, nil
}

// SameModel reports whether two DMR model references name the same model,
// ignoring the default registry prefix.
func SameModel(a, b string) bool {
	return strings.EqualFold((Model{ID: a}).DisplayName(), (Model{ID: b}).DisplayName())
}
