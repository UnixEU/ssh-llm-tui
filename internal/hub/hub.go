// Package hub lists the models a Docker Hub namespace publishes, for the
// model search. It uses the public, unauthenticated Hub API:
//
//   - GET /v2/repositories/<namespace>/?page_size=100&ordering=pull_count
//   - GET /v2/repositories/<namespace>/<repo>/tags?page_size=100
//
// Results are cached for a while, since the Hub rate-limits anonymous
// clients.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultURL is the public Docker Hub API origin.
const DefaultURL = "https://hub.docker.com"

// maxPages caps how many pages of 100 entries one listing reads.
const maxPages = 10

// ErrRateLimited is returned when the Hub answers HTTP 429.
var ErrRateLimited = errors.New("docker hub rate limit reached, try again in a few minutes")

// errForbidden is HTTP 403, which the Hub returns when an anonymous client
// pages too far.
var errForbidden = errors.New("docker hub: HTTP 403")

// Repo is one model repository.
type Repo struct {
	Name        string // without the namespace, e.g. "qwen3"
	Description string
	Pulls       int64
	Stars       int
	Updated     time.Time
}

// Tag is one tag of a repository.
type Tag struct {
	Name    string
	Size    int64 // bytes, the sum of the artifact's layers
	Updated time.Time
}

// Options configures a Client.
type Options struct {
	BaseURL   string
	Namespace string
	TTL       time.Duration
	HTTP      *http.Client
	// AllTags keeps repos and tags that DMR's llama.cpp backend cannot
	// run (vLLM repos, MLX and safetensors tags).
	AllTags bool
}

// Client reads the Hub. It is safe for concurrent use and shared by all
// sessions, so the cache is too.
type Client struct {
	base    string
	ns      string
	ttl     time.Duration
	http    *http.Client
	allTags bool

	mu    sync.Mutex
	repos cached[[]Repo]
	tags  map[string]cached[[]Tag]
}

type cached[T any] struct {
	at  time.Time
	val T
}

// New returns a client with defaults filled in.
func New(opts Options) *Client {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = DefaultURL
	}
	ns := strings.Trim(strings.TrimSpace(opts.Namespace), "/")
	if ns == "" {
		ns = "ai"
	}
	hc := opts.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{base: base, ns: ns, ttl: opts.TTL, http: hc, allTags: opts.AllTags,
		tags: map[string]cached[[]Tag]{}}
}

// Namespace is the Hub namespace searched, e.g. "ai".
func (c *Client) Namespace() string { return c.ns }

// Ref is the reference DMR pulls for repo and tag, e.g. "ai/qwen3:8b".
func (c *Client) Ref(repo, tag string) string {
	if tag == "" {
		tag = "latest"
	}
	return c.ns + "/" + repo + ":" + tag
}

func (c *Client) fresh(at time.Time) bool {
	return !at.IsZero() && (c.ttl <= 0 || time.Since(at) < c.ttl)
}

// Repos lists the namespace's repositories, most pulled first.
func (c *Client) Repos(ctx context.Context) ([]Repo, error) {
	c.mu.Lock()
	if c.fresh(c.repos.at) {
		r := c.repos.val
		c.mu.Unlock()
		return r, nil
	}
	c.mu.Unlock()
	// Anonymous clients may not page past the first 100 repositories, so
	// the most pulled ones come first and, when the namespace is larger,
	// the first page by name in both directions fills in the rest.
	seen := map[string]bool{}
	var out []Repo
	total := 0
	for i, order := range []string{"pull_count", "name", "-name"} {
		if i > 0 && len(seen) >= total {
			break
		}
		next := fmt.Sprintf("%s/v2/repositories/%s/?page_size=100&ordering=%s", c.base, url.PathEscape(c.ns), order)
		for page := 0; next != "" && page < maxPages; page++ {
			var body struct {
				Count   int    `json:"count"`
				Next    string `json:"next"`
				Results []struct {
					Name        string    `json:"name"`
					Description string    `json:"description"`
					PullCount   int64     `json:"pull_count"`
					StarCount   int       `json:"star_count"`
					LastUpdated time.Time `json:"last_updated"`
				} `json:"results"`
			}
			err := c.get(ctx, next, &body)
			if errors.Is(err, errForbidden) && (page > 0 || i > 0) {
				break
			}
			if err != nil {
				if i > 0 {
					break
				}
				return nil, err
			}
			if body.Count > total {
				total = body.Count
			}
			for _, r := range body.Results {
				if seen[r.Name] {
					continue
				}
				seen[r.Name] = true
				if !c.allTags && strings.HasSuffix(r.Name, "-vllm") {
					continue
				}
				out = append(out, Repo{Name: r.Name, Description: strings.TrimSpace(r.Description), Pulls: r.PullCount, Stars: r.StarCount, Updated: r.LastUpdated})
			}
			next = body.Next
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Pulls > out[j].Pulls })
	c.mu.Lock()
	c.repos = cached[[]Repo]{at: time.Now(), val: out}
	c.mu.Unlock()
	return out, nil
}

// Tags lists a repository's tags with their sizes, newest first, with
// "latest" on top.
func (c *Client) Tags(ctx context.Context, repo string) ([]Tag, error) {
	c.mu.Lock()
	if e, ok := c.tags[repo]; ok && c.fresh(e.at) {
		c.mu.Unlock()
		return e.val, nil
	}
	c.mu.Unlock()
	var out []Tag
	next := fmt.Sprintf("%s/v2/repositories/%s/%s/tags?page_size=100", c.base, url.PathEscape(c.ns), url.PathEscape(repo))
	for page := 0; next != "" && page < maxPages; page++ {
		var body struct {
			Next    string `json:"next"`
			Results []struct {
				Name        string    `json:"name"`
				FullSize    int64     `json:"full_size"`
				LastUpdated time.Time `json:"last_updated"`
			} `json:"results"`
		}
		if err := c.get(ctx, next, &body); err != nil {
			if errors.Is(err, errForbidden) && page > 0 {
				break
			}
			return nil, err
		}
		for _, t := range body.Results {
			if !c.allTags && !Runnable(t.Name) {
				continue
			}
			out = append(out, Tag{Name: t.Name, Size: t.FullSize, Updated: t.LastUpdated})
		}
		next = body.Next
	}
	SortTags(out)
	c.mu.Lock()
	c.tags[repo] = cached[[]Tag]{at: time.Now(), val: out}
	c.mu.Unlock()
	return out, nil
}

func (c *Client) get(ctx context.Context, u string, dst any) error {
	if !strings.HasPrefix(u, c.base+"/") {
		return fmt.Errorf("docker hub: unexpected next page %q", u)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ssh-llm-tui")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker hub: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return ErrRateLimited
	case resp.StatusCode == http.StatusForbidden:
		return errForbidden
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("docker hub: not found")
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return fmt.Errorf("docker hub: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(dst)
}

// Runnable reports whether a tag is a GGUF build the llama.cpp backend can
// run. MLX and safetensors builds are for other backends.
func Runnable(tag string) bool {
	t := strings.ToLower(tag)
	return !strings.Contains(t, "mlx") && !strings.Contains(t, "safetensors") && !strings.Contains(t, "vllm")
}

// SortTags puts "latest" first, then by name.
func SortTags(tags []Tag) {
	sort.SliceStable(tags, func(i, j int) bool {
		a, b := tags[i].Name, tags[j].Name
		if (a == "latest") != (b == "latest") {
			return a == "latest"
		}
		return a < b
	})
}

// Search filters repos by query. Every word of the query must appear in the
// name or the description; name matches rank first, then by pulls. An
// empty query returns all repos.
func Search(repos []Repo, query string) []Repo {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return repos
	}
	type hit struct {
		r     Repo
		score int
	}
	var hits []hit
	for _, r := range repos {
		name, desc := strings.ToLower(r.Name), strings.ToLower(r.Description)
		score, ok := 0, true
		for _, w := range words {
			switch {
			case strings.HasPrefix(name, w):
				score += 3
			case strings.Contains(name, w):
				score += 2
			case strings.Contains(desc, w):
				score++
			default:
				ok = false
			}
		}
		if ok {
			hits = append(hits, hit{r, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]Repo, len(hits))
	for i, h := range hits {
		out[i] = h.r
	}
	return out
}
