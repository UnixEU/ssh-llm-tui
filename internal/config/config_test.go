package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func envFrom(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func TestLoadBakedInDefaults(t *testing.T) {
	cfg, err := Load(LoadOptions{Getenv: envFrom(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != 2222 {
		t.Fatalf("port = %d", cfg.Server.Port)
	}
	if cfg.Server.AuthMode != "open" {
		t.Fatalf("auth = %q", cfg.Server.AuthMode)
	}
	if cfg.DMR.BaseURL != "http://host.docker.internal:12434" {
		t.Fatalf("base url = %q", cfg.DMR.BaseURL)
	}
	if cfg.DMR.RequestTimeout.Duration() != 60*time.Second {
		t.Fatalf("request timeout = %s", cfg.DMR.RequestTimeout.Duration())
	}
	if cfg.DMR.WarmTimeout.Duration() != 10*time.Minute {
		t.Fatalf("warm timeout = %s", cfg.DMR.WarmTimeout.Duration())
	}
	if cfg.Welcome.Title != "ssh-llm-tui" {
		t.Fatalf("title = %q", cfg.Welcome.Title)
	}
	if !strings.Contains(cfg.Models.Empty, "No models are pulled") {
		t.Fatalf("empty message = %q", cfg.Models.Empty)
	}
	if !strings.Contains(cfg.Loading.Message, "{model}") {
		t.Fatalf("loading message = %q", cfg.Loading.Message)
	}
	if cfg.Uploads.MaxBytes != 10<<20 || cfg.Uploads.MaxFiles != 10 || cfg.Uploads.TokenBudget != 8192 {
		t.Fatalf("uploads %+v", cfg.Uploads)
	}
	if cfg.Uploads.ImageMaxPx != 1536 || cfg.Uploads.ImageMaxBytes != 2<<20 || cfg.Uploads.FilesBoxRows != 4 {
		t.Fatalf("image defaults %+v", cfg.Uploads)
	}
	if cfg.GPU.Mode != "auto" || cfg.GPU.Interval.Duration() != 2*time.Second {
		t.Fatalf("gpu %+v", cfg.GPU)
	}
	if cfg.GPU.TempWarn != 40 || cfg.GPU.TempHot != 60 || cfg.GPU.TempCrit != 70 {
		t.Fatalf("temp bands %+v", cfg.GPU)
	}
	if cfg.GPU.PowerWarn != 25 || cfg.GPU.PowerHot != 100 || cfg.GPU.PowerCrit != 200 {
		t.Fatalf("power bands %+v", cfg.GPU)
	}
	if cfg.GPU.MemWarn != 40 || cfg.GPU.MemHot != 60 || cfg.GPU.MemCrit != 90 {
		t.Fatalf("mem bands %+v", cfg.GPU)
	}
	if cfg.Chat.Markdown != "on" || !cfg.MarkdownOn() {
		t.Fatalf("markdown %+v", cfg.Chat.Markdown)
	}
}

func TestLoadFileThenEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := []byte(`
server:
  host: 127.0.0.1
  port: 2200
  auth_mode: open
dmr:
  base_url: http://172.17.0.1:12434/
  request_timeout: 5s
welcome:
  title: From file
  message: file message
models:
  title: Models from file
  empty: nothing here
`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(LoadOptions{
		Path: path,
		Getenv: envFrom(map[string]string{
			"WELCOME_TITLE":    "From env",
			"WELCOME_MESSAGE":  "env message",
			"DMR_BASE_URL":     "http://model-runner.docker.internal:12434",
			"SSH_PORT":         "2223",
			"DMR_WARM_TIMEOUT": "2m",
			"AUTH_MODE":        "open",
			"MODELS_EMPTY":     "still empty",
			"SYSTEM_PROMPT":    "be brief",
			"WELCOME_HINT":     "",
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Welcome.Title != "From env" {
		t.Fatalf("title = %q", cfg.Welcome.Title)
	}
	if cfg.Welcome.Message != "env message" {
		t.Fatalf("message = %q", cfg.Welcome.Message)
	}
	if cfg.Welcome.Hint != "Press any key to continue. ctrl+q exits." {
		t.Fatalf("empty env wiped hint: %q", cfg.Welcome.Hint)
	}
	if cfg.DMR.BaseURL != "http://model-runner.docker.internal:12434" {
		t.Fatalf("base = %q", cfg.DMR.BaseURL)
	}
	if cfg.Server.Port != 2223 {
		t.Fatalf("port = %d", cfg.Server.Port)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Fatalf("host = %q", cfg.Server.Host)
	}
	if cfg.DMR.RequestTimeout.Duration() != 5*time.Second {
		t.Fatalf("request timeout = %s", cfg.DMR.RequestTimeout.Duration())
	}
	if cfg.DMR.WarmTimeout.Duration() != 2*time.Minute {
		t.Fatalf("warm timeout = %s", cfg.DMR.WarmTimeout.Duration())
	}
	if cfg.Models.Title != "Models from file" {
		t.Fatalf("models title = %q", cfg.Models.Title)
	}
	if cfg.Models.Empty != "still empty" {
		t.Fatalf("empty = %q", cfg.Models.Empty)
	}
	if cfg.Chat.SystemPrompt != "be brief" {
		t.Fatalf("system = %q", cfg.Chat.SystemPrompt)
	}
	if cfg.Chat.Placeholder == "" {
		t.Fatal("file overlay cleared an omitted chat placeholder")
	}
}

func TestLoadMissingRequiredFile(t *testing.T) {
	_, err := Load(LoadOptions{
		Path:        filepath.Join(t.TempDir(), "missing.yaml"),
		RequireFile: true,
		Getenv:      envFrom(nil),
	})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestLoadMissingOptionalFileUsesDefault(t *testing.T) {
	cfg, err := Load(LoadOptions{
		Path:        filepath.Join(t.TempDir(), "missing.yaml"),
		RequireFile: false,
		Getenv:      envFrom(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Welcome.Title != "ssh-llm-tui" {
		t.Fatalf("title = %q", cfg.Welcome.Title)
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("welcome: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(LoadOptions{Path: path, Getenv: envFrom(nil)})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestLoadRejectsBadEnv(t *testing.T) {
	cases := []map[string]string{
		{"SSH_PORT": "nope"},
		{"SSH_PORT": "0"},
		{"SSH_PORT": "70000"},
		{"AUTH_MODE": "password"},
		{"DMR_BASE_URL": "not a url"},
		{"DMR_BASE_URL": "ftp://example.com"},
		{"DMR_REQUEST_TIMEOUT": "soon"},
		{"AUTH_MODE": "keys"},
		{"GPU_STATS": "yes"},
		{"UPLOAD_MAX_BYTES": "nope"},
		{"CONTEXT_TOKEN_BUDGET": "0"},
	}
	for _, env := range cases {
		_, err := Load(LoadOptions{Getenv: envFrom(env)})
		if err == nil {
			t.Fatalf("env %v: expected an error", env)
		}
	}
}

func TestLoadKeysModeRequiresFile(t *testing.T) {
	dir := t.TempDir()
	keys := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(keys, []byte("# empty of keys\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(LoadOptions{Getenv: envFrom(map[string]string{
		"AUTH_MODE":            "keys",
		"AUTHORIZED_KEYS_PATH": keys,
	})})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.AuthMode != "keys" {
		t.Fatalf("auth = %q", cfg.Server.AuthMode)
	}
	if cfg.Server.AuthorizedKeysPath != keys {
		t.Fatalf("path = %q", cfg.Server.AuthorizedKeysPath)
	}
}

func TestExampleConfigMatchesBakedIn(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	example := filepath.Join(filepath.Dir(file), "..", "..", "config.example.yaml")
	got, err := os.ReadFile(example)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(defaultYAML()) {
		t.Fatalf("config.example.yaml drifted from internal/config/default.yaml")
	}
}

func TestFitDefaults(t *testing.T) {
	cfg, err := Load(LoadOptions{Getenv: envFrom(nil)})
	if err != nil {
		t.Fatal(err)
	}
	f := cfg.Fit
	if !cfg.FitOn() || cfg.FitStrict() || !cfg.ContextPicker() || !cfg.TargetPicker() || !cfg.CPUAllowed() {
		t.Fatalf("fit switches %+v", f)
	}
	want := []int{8192, 16384, 32768, 65536, 131072}
	if len(f.ContextChoices) != len(want) {
		t.Fatalf("choices %v", f.ContextChoices)
	}
	for i := range want {
		if f.ContextChoices[i] != want[i] {
			t.Fatalf("choices %v", f.ContextChoices)
		}
	}
	if f.DefaultContextTokens != 0 || f.MarginMiB != 1024 || f.MarginPct != 5 {
		t.Fatalf("defaults %+v", f)
	}
	if f.UsagePath != "/data/fit/usage.json" || f.ModelStorePath != "" {
		t.Fatalf("paths %+v", f)
	}
}

func TestFitEnv(t *testing.T) {
	cfg, err := Load(LoadOptions{Getenv: envFrom(map[string]string{
		"FIT_CHECK":        "strict",
		"FIT_MARGIN_MIB":   "2048",
		"FIT_MARGIN_PCT":   "10",
		"CONTEXT_CHOICES":  "32k, 4096,16K",
		"DEFAULT_CONTEXT":  "16k",
		"TARGET_SELECT":    "off",
		"ALLOW_CPU":        "no",
		"MODEL_STORE_PATH": "/dmr-models",
		"KV_CACHE_TYPE":    "Q8_0",
		"CPU_THREADS":      "12",
		"FIT_USAGE_PATH":   "off",
	})})
	if err != nil {
		t.Fatal(err)
	}
	f := cfg.Fit
	if !cfg.FitStrict() || cfg.TargetPicker() || cfg.CPUAllowed() || !cfg.ContextPicker() {
		t.Fatalf("switches %+v", f)
	}
	if f.MarginMiB != 2048 || f.MarginPct != 10 || f.DefaultContextTokens != 16384 || f.CPUThreads != 12 {
		t.Fatalf("values %+v", f)
	}
	if len(f.ContextChoices) != 3 || f.ContextChoices[0] != 4096 || f.ContextChoices[2] != 32768 {
		t.Fatalf("choices %v (want sorted)", f.ContextChoices)
	}
	if f.KVCacheType != "q8_0" || f.ModelStorePath != "/dmr-models" || f.UsagePath != "" {
		t.Fatalf("strings %+v", f)
	}
}

func TestFitEnvRejectsBadValues(t *testing.T) {
	for k, v := range map[string]string{
		"FIT_CHECK":       "maybe",
		"CONTEXT_CHOICES": "8k,lots",
		"DEFAULT_CONTEXT": "big",
		"KV_CACHE_TYPE":   "q2",
		"FIT_MARGIN_MIB":  "-1",
		"FIT_MARGIN_PCT":  "75",
		"CPU_THREADS":     "x",
	} {
		if _, err := Load(LoadOptions{Getenv: envFrom(map[string]string{k: v})}); err == nil {
			t.Errorf("%s=%s accepted", k, v)
		}
	}
}
