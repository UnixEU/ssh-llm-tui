package dmr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// pathCreate pulls a model: POST /models/create {"from": ref}. With
// Accept: application/json DMR v1.2.9 streams one JSON object per line,
// {"type":"progress|success|warning|error","message":...,"total":...,
// "layer":{"id","size","current"}}. Cancelling the request cancels the
// pull. See pkg/inference/models/http_handler.go and manager.go (Pull).
const pathCreate = "/models/create"

// PullProgress is the state of a pull after one progress line.
type PullProgress struct {
	Done    int64 // bytes downloaded over all layers seen so far
	Total   int64 // bytes of all layers seen so far, or DMR's total
	Message string
}

type pullLine struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Total   int64  `json:"total"`
	Layer   struct {
		ID      string `json:"id"`
		Size    int64  `json:"size"`
		Current int64  `json:"current"`
	} `json:"layer"`
}

// Pull downloads ref into DMR's model store and calls fn on every progress
// line. It returns when DMR reports success, the stream ends, or ctx is
// cancelled. Warnings are passed to fn as a message.
func (c *Client) Pull(ctx context.Context, ref string, fn func(PullProgress)) error {
	body, _ := json.Marshal(map[string]string{"from": ref})
	req, err := c.newRequest(ctx, http.MethodPost, pathCreate, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return &HTTPError{Op: "pull " + ref, Path: pathCreate, Status: resp.StatusCode, Body: clip(raw)}
	}
	layers := map[string][2]int64{}
	var p PullProgress
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var l pullLine
		if json.Unmarshal(line, &l) != nil {
			// DMR writes a plain-text error after the stream has started.
			return fmt.Errorf("pull %s: %s", ref, clip(line))
		}
		switch l.Type {
		case "error":
			return fmt.Errorf("pull %s: %s", ref, strings.TrimSpace(l.Message))
		case "success":
			p.Message = strings.TrimSpace(l.Message)
			if p.Total > 0 {
				p.Done = p.Total
			}
		case "warning":
			p.Message = strings.TrimSpace(l.Message)
		default:
			if l.Layer.ID != "" {
				layers[l.Layer.ID] = [2]int64{l.Layer.Size, l.Layer.Current}
			}
			var done, total int64
			for _, v := range layers {
				total += v[0]
				done += v[1]
			}
			if l.Total > total {
				total = l.Total
			}
			p.Done, p.Total = done, total
			if l.Message != "" {
				p.Message = strings.TrimSpace(l.Message)
			}
		}
		if fn != nil {
			fn(p)
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// A failure after the stream started arrives as an error line or as
	// plain text, both handled above. A clean end is a finished pull.
	return nil
}
