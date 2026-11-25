package hub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func hubServer(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v2/repositories/ai/" && r.URL.Query().Get("page") == "":
			fmt.Fprintf(w, `{"next":"%s/v2/repositories/ai/?page=2","results":[{"name":"gemma3","pull_count":500,"description":"Google Gemma"},{"name":"qwen3-vllm","pull_count":900}]}`, srv.URL)
		case r.URL.Path == "/v2/repositories/ai/":
			fmt.Fprint(w, `{"next":null,"results":[{"name":"qwen3","pull_count":800},{"name":"smollm2","pull_count":10,"description":"small qwen-like"}]}`)
		case r.URL.Path == "/v2/repositories/ai/qwen3/tags":
			fmt.Fprint(w, `{"next":null,"results":[{"name":"8b-q4_K_M","full_size":5027784512},{"name":"8b-mlx-bf16","full_size":1},{"name":"8b-safetensors","full_size":2},{"name":"latest","full_size":5027784512}]}`)
		case r.URL.Path == "/v2/repositories/ai/limited/tags":
			w.WriteHeader(http.StatusTooManyRequests)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReposPagesFiltersAndCaches(t *testing.T) {
	var hits int32
	srv := hubServer(t, &hits)
	c := New(Options{BaseURL: srv.URL, TTL: time.Minute})
	repos, err := c.Repos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range repos {
		names = append(names, r.Name)
	}
	if got := strings.Join(names, ","); got != "qwen3,gemma3,smollm2" {
		t.Fatalf("repos = %s", got)
	}
	before := atomic.LoadInt32(&hits)
	if _, err := c.Repos(context.Background()); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&hits) != before {
		t.Fatal("second listing should come from the cache")
	}
	all := New(Options{BaseURL: srv.URL, AllTags: true})
	repos, _ = all.Repos(context.Background())
	if len(repos) != 4 {
		t.Fatalf("AllTags should keep vllm repos, got %d", len(repos))
	}
}

func TestTags(t *testing.T) {
	var hits int32
	srv := hubServer(t, &hits)
	c := New(Options{BaseURL: srv.URL})
	tags, err := c.Tags(context.Background(), "qwen3")
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0].Name != "latest" || tags[1].Name != "8b-q4_K_M" || tags[1].Size != 5027784512 {
		t.Fatalf("tags = %+v", tags)
	}
	if _, err := c.Tags(context.Background(), "limited"); err != ErrRateLimited {
		t.Fatalf("429 should be ErrRateLimited, got %v", err)
	}
	if c.Ref("qwen3", "") != "ai/qwen3:latest" {
		t.Fatal(c.Ref("qwen3", ""))
	}
}

func TestSearch(t *testing.T) {
	repos := []Repo{{Name: "smollm2", Description: "small qwen-like"}, {Name: "qwen3"}, {Name: "gemma3"}, {Name: "qwen3-coder"}}
	var got []string
	for _, r := range Search(repos, "qwen") {
		got = append(got, r.Name)
	}
	if strings.Join(got, ",") != "qwen3,qwen3-coder,smollm2" {
		t.Fatalf("search = %v", got)
	}
	if len(Search(repos, "qwen coder")) != 1 {
		t.Fatal("every word must match")
	}
	if len(Search(repos, "  ")) != 4 {
		t.Fatal("empty query returns all")
	}
}

func TestRejectsForeignNextPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"next":"http://elsewhere.invalid/v2/x","results":[{"name":"a"}]}`)
	}))
	defer srv.Close()
	if _, err := New(Options{BaseURL: srv.URL}).Repos(context.Background()); err == nil {
		t.Fatal("a next page on another host must be refused")
	}
}

func TestReposFillPastAnonymousPagingLimit(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("page") != "":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"pagination offset too large for anonymous requests"}`)
		case q.Get("ordering") == "pull_count":
			fmt.Fprintf(w, `{"count":4,"next":"%s/v2/repositories/ai/?page=2","results":[{"name":"b","pull_count":9},{"name":"c","pull_count":5}]}`, srv.URL)
		case q.Get("ordering") == "name":
			fmt.Fprint(w, `{"count":4,"results":[{"name":"a","pull_count":1},{"name":"b","pull_count":9}]}`)
		case q.Get("ordering") == "-name":
			fmt.Fprint(w, `{"count":4,"results":[{"name":"d","pull_count":7},{"name":"c","pull_count":5}]}`)
		}
	}))
	defer srv.Close()
	repos, err := New(Options{BaseURL: srv.URL}).Repos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range repos {
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "b,d,c,a" {
		t.Fatalf("repos = %v", names)
	}
}
