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
)

func TestConfigureBodies(t *testing.T) {
	ctx32k := int32(32768)
	cases := []struct {
		name string
		req  ConfigureRequest
		want string
		cli  string
	}{
		{
			name: "context only",
			req:  ConfigureRequest{Model: "ai/example:7b", ContextSize: &ctx32k},
			want: `{"model":"ai/example:7b","context-size":32768}`,
			cli:  "docker model configure --context-size 32768 ai/example:7b",
		},
		{
			name: "cpu",
			req:  ConfigureRequest{Model: "ai/example:7b", ContextSize: &ctx32k, RuntimeFlags: []string{"--device", "none"}},
			want: `{"model":"ai/example:7b","context-size":32768,"runtime-flags":["--device","none"]}`,
			cli:  "docker model configure --context-size 32768 ai/example:7b -- --device none",
		},
		{
			name: "single gpu, default context",
			req:  ConfigureRequest{Model: "ai/example:7b", RuntimeFlags: []string{"--device", "CUDA1"}},
			want: `{"model":"ai/example:7b","runtime-flags":["--device","CUDA1"]}`,
			cli:  "docker model configure ai/example:7b -- --device CUDA1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got, method, path, ctype string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				got, method, path, ctype = string(b), r.Method, r.URL.Path, r.Header.Get("Content-Type")
				w.WriteHeader(http.StatusAccepted)
			}))
			defer srv.Close()
			if err := testClient(t, srv.URL).Configure(context.Background(), tc.req); err != nil {
				t.Fatal(err)
			}
			if method != http.MethodPost || path != "/engines/_configure" || ctype != "application/json" {
				t.Fatalf("%s %s %s", method, path, ctype)
			}
			if got != tc.want {
				t.Fatalf("body %s\nwant %s", got, tc.want)
			}
			if cli := tc.req.CLI(); cli != tc.cli {
				t.Fatalf("cli %q", cli)
			}
		})
	}
}

func TestConfigureConflictIsRunnerBusy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "runner already active", http.StatusConflict)
	}))
	defer srv.Close()
	err := testClient(t, srv.URL).Configure(context.Background(), ConfigureRequest{Model: "ai/x"})
	if !errors.Is(err, ErrRunnerBusy) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "runner already active") {
		t.Fatalf("server text lost: %v", err)
	}
}

func TestConfigureOtherErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `runtime flag "--foo" is not allowed`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	err := testClient(t, srv.URL).Configure(context.Background(), ConfigureRequest{Model: "ai/x"})
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 500 || errors.Is(err, ErrRunnerBusy) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigsAndRunning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/engines/_configure":
			if r.URL.Query().Get("model") != "ai/x:1" {
				t.Errorf("query %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"Backend": "llama.cpp", "Model": "ai/x:1", "ModelID": "sha256:abc", "Mode": "completion",
				"Config": map[string]any{"context-size": 16384, "runtime-flags": []string{"--device", "none"}},
			}})
		case "/engines/ps":
			_, _ = io.WriteString(w, `[{"backend_name":"llama.cpp","model_name":"ai/x:1","mode":"completion","in_use":true}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	cfgs, err := c.Configs(context.Background(), "ai/x:1")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 || cfgs[0].ContextSize != 16384 || len(cfgs[0].RuntimeFlags) != 2 {
		t.Fatalf("configs %+v", cfgs)
	}
	runners, err := c.Running(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runners) != 1 || !runners[0].InUse || runners[0].Model != "ai/x:1" {
		t.Fatalf("runners %+v", runners)
	}
	if !SameModel("docker.io/ai/x:1", "ai/X:1") || SameModel("ai/x:1", "ai/x:2") {
		t.Fatal("SameModel")
	}
}
