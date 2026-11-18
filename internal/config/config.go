// Package config loads ssh-llm-tui settings from a baked-in default, an
// optional YAML file, and environment variables. Environment variables win.
package config

import (
	"embed"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed default.yaml
var defaultFS embed.FS

func defaultYAML() []byte {
	b, err := defaultFS.ReadFile("default.yaml")
	if err != nil {
		panic("baked-in config: " + err.Error())
	}
	return b
}

// Duration is a time.Duration that unmarshals from a Go duration string
// such as "60s" or "10m".
type Duration time.Duration

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return fmt.Errorf("duration: %w", err)
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", raw, err)
	}
	if parsed < 0 {
		return fmt.Errorf("duration must be >= 0, got %s", parsed)
	}
	*d = Duration(parsed)
	return nil
}

// Config is the process configuration.
type Config struct {
	Server  Server  `yaml:"server"`
	DMR     DMR     `yaml:"dmr"`
	Welcome Welcome `yaml:"welcome"`
	Models  Models  `yaml:"models"`
	Loading Loading `yaml:"loading"`
	Chat    Chat    `yaml:"chat"`
	Footer  Footer  `yaml:"footer"`
	Uploads Uploads `yaml:"uploads"`
	Edit    Edit    `yaml:"edit"`
	// Downloads is the read-only folder for edited files and archives.
	Downloads Downloads `yaml:"downloads"`
	GPU       GPU       `yaml:"gpu"`
	Fit       Fit       `yaml:"fit"`
}

// Fit controls the memory check before a model loads, and the context and
// placement pickers.
type Fit struct {
	// Check is off, on (a model that does not fit is refused; an unknown
	// need asks for confirmation) or strict (an unknown need is refused).
	Check string `yaml:"check"`
	// MarginMiB and MarginPct set the headroom kept free on every device:
	// max(MarginMiB, MarginPct of the device's total memory).
	MarginMiB int64   `yaml:"margin_mib"`
	MarginPct float64 `yaml:"margin_pct"`
	// Contexts is the list offered by the context picker, e.g. "8k,16k".
	Contexts string `yaml:"contexts"`
	// DefaultContext is the preselected context: "auto" (the largest that
	// fits) or a token count.
	DefaultContext string `yaml:"default_context"`
	// ContextSelect and TargetSelect show the pickers (on or off).
	ContextSelect string `yaml:"context_select"`
	TargetSelect  string `yaml:"target_select"`
	// AllowCPU offers the CPU (system RAM) target.
	AllowCPU string `yaml:"allow_cpu"`
	// ModelStorePath is a read-only mount of the DMR model store. Empty
	// disables header reading; the check then relies on measured usage.
	ModelStorePath string `yaml:"model_store_path"`
	// KVCacheType is an optional llama.cpp KV cache type (f16, q8_0, q4_0).
	KVCacheType string `yaml:"kv_cache_type"`
	// CPUThreads sets llama.cpp threads for the CPU target. 0 keeps the default.
	CPUThreads int `yaml:"cpu_threads"`
	// UsagePath stores measured GPU usage per model, context and target.
	UsagePath string `yaml:"usage_path"`

	// ContextChoices is Contexts parsed. Filled by Load.
	ContextChoices []int `yaml:"-"`
	// DefaultContextTokens is DefaultContext parsed; 0 means auto.
	DefaultContextTokens int `yaml:"-"`
}

// FitOn reports whether the memory check runs.
func (c Config) FitOn() bool { return c.Fit.Check != "off" }

// FitStrict reports whether an unknown memory need blocks loading.
func (c Config) FitStrict() bool { return c.Fit.Check == "strict" }

// ContextPicker reports whether the context picker is shown.
func (c Config) ContextPicker() bool { return c.Fit.ContextSelect != "off" }

// TargetPicker reports whether the GPU/CPU picker is shown.
func (c Config) TargetPicker() bool { return c.Fit.TargetSelect != "off" }

// CPUAllowed reports whether the CPU target is offered.
func (c Config) CPUAllowed() bool { return c.Fit.AllowCPU != "off" }

// ConfiguresDMR reports whether the app sends a runtime configuration to
// DMR before loading. With every fit feature off the app behaves like
// earlier releases and only warms the model.
func (c Config) ConfiguresDMR() bool {
	return c.FitOn() || c.ContextPicker() || c.TargetPicker() || c.Fit.KVCacheType != ""
}

// DefaultUploadIgnore is the default UPLOAD_IGNORE list.
const DefaultUploadIgnore = ".git,node_modules,.venv,__pycache__,.DS_Store"

// Uploads limits files received over SCP and SFTP.
type Uploads struct {
	Dir           string `yaml:"dir"`
	MaxBytes      int64  `yaml:"max_bytes"`
	MaxFiles      int    `yaml:"max_files"`
	MaxTotalBytes int64  `yaml:"max_total_bytes"`
	// Ignore is a comma-separated list of path components (".git",
	// "node_modules") whose contents are accepted and discarded.
	// "none" turns it off.
	Ignore string `yaml:"ignore"`
	// AutoInclude is how many new files are included in the prompt
	// automatically, within TokenBudget. Later files arrive excluded.
	AutoInclude     int    `yaml:"auto_include"`
	TokenBudget     int    `yaml:"token_budget"`
	ImageMaxPx      int    `yaml:"image_max_px"`
	ImageMaxBytes   int64  `yaml:"image_max_bytes"`
	FilesBoxRows    int    `yaml:"files_box_rows"`
	VisionModels    string `yaml:"vision_models"`
	NonVisionModels string `yaml:"non_vision_models"`
}

// Edit controls file changes proposed by the model. Every change is
// reviewed in the TUI before it is used; uploads are never modified.
type Edit struct {
	// Mode is on or off. When on, edit mode starts enabled whenever files
	// are uploaded; the user can toggle it per session.
	Mode string `yaml:"mode"`
	// Protocol is how edits are written. Only "blocks" exists today.
	Protocol string `yaml:"protocol"`
	// MaxFiles is the number of files one reply may change.
	MaxFiles    int    `yaml:"max_files"`
	AllowNew    string `yaml:"allow_new"`
	AllowDelete string `yaml:"allow_delete"`
}

// Downloads is where edited files, the patch and archives are prepared.
type Downloads struct {
	Dir           string `yaml:"dir"`
	MaxTotalBytes int64  `yaml:"max_total_bytes"`
	Archive       string `yaml:"archive"`
	// PublicHost and PublicPort are only used to print download commands
	// with the name clients use to reach this server.
	PublicHost string `yaml:"public_host"`
	PublicPort int    `yaml:"public_port"`
}

// EditOn reports whether edit mode is available.
func (c Config) EditOn() bool { return c.Edit.Mode != "off" }

// EditAllowNew reports whether the model may create files.
func (c Config) EditAllowNew() bool { return c.Edit.AllowNew != "off" }

// EditAllowDelete reports whether the model may delete files.
func (c Config) EditAllowDelete() bool { return c.Edit.AllowDelete == "on" }

// PublicPort is the port to show in download commands.
func (c Config) PublicPort() int {
	if c.Downloads.PublicPort > 0 {
		return c.Downloads.PublicPort
	}
	return c.Server.Port
}

// GPU controls the nvidia-smi poller. Mode is auto or off.
// Temperature and power bands are the panel colours:
// value < warn is green, warn ≤ value < hot is yellow,
// hot ≤ value < crit is orange, value ≥ crit is red.
type GPU struct {
	Mode      string   `yaml:"mode"`
	Interval  Duration `yaml:"interval"`
	SMIPath   string   `yaml:"smi_path"`
	TempWarn  float64  `yaml:"temp_warn"`
	TempHot   float64  `yaml:"temp_hot"`
	TempCrit  float64  `yaml:"temp_crit"`
	PowerWarn float64  `yaml:"power_warn"`
	PowerHot  float64  `yaml:"power_hot"`
	PowerCrit float64  `yaml:"power_crit"`
	MemWarn   float64  `yaml:"mem_warn"`
	MemHot    float64  `yaml:"mem_hot"`
	MemCrit   float64  `yaml:"mem_crit"`
}

// Server is the SSH listener.
type Server struct {
	Host               string `yaml:"host"`
	Port               int    `yaml:"port"`
	HostKeyPath        string `yaml:"host_key_path"`
	AuthMode           string `yaml:"auth_mode"`
	AuthorizedKeysPath string `yaml:"authorized_keys_path"`
}

// Addr is the host:port the SSH server binds.
func (s Server) Addr() string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

// DMR is the Docker Model Runner HTTP client configuration.
type DMR struct {
	BaseURL        string   `yaml:"base_url"`
	RequestTimeout Duration `yaml:"request_timeout"`
	WarmTimeout    Duration `yaml:"warm_timeout"`
}

// Welcome is the first screen.
type Welcome struct {
	Title      string `yaml:"title"`
	Message    string `yaml:"message"`
	Hint       string `yaml:"hint"`
	BannerFile string `yaml:"banner_file"`
}

// Models is the model-picker screen.
type Models struct {
	Title   string `yaml:"title"`
	Empty   string `yaml:"empty"`
	Hint    string `yaml:"hint"`
	Loading string `yaml:"loading"`
}

// Loading is the warm/unload screen.
type Loading struct {
	Title   string `yaml:"title"`
	Message string `yaml:"message"`
}

// Chat is the conversation screen.
type Chat struct {
	Title        string `yaml:"title"`
	Placeholder  string `yaml:"placeholder"`
	SystemPrompt string `yaml:"system_prompt"`
	YouLabel     string `yaml:"you_label"`
	// Markdown is on or off. Assistant replies are rendered when it is on.
	Markdown string `yaml:"markdown"`
}

// MarkdownOn reports whether assistant replies should be rendered.
func (c Config) MarkdownOn() bool {
	return !strings.EqualFold(strings.TrimSpace(c.Chat.Markdown), "off")
}

// Footer is the shortcut legend.
type Footer struct {
	ChangeModel string `yaml:"change_model"`
	Exit        string `yaml:"exit"`
	Scroll      string `yaml:"scroll"`
}

// LoadOptions controls where configuration is read from.
type LoadOptions struct {
	// Path is an optional YAML file. When empty, only the baked-in default
	// and environment variables are used.
	Path string
	// RequireFile makes a missing Path an error. When false, a missing file
	// is ignored and the baked-in default is kept.
	RequireFile bool
	// Getenv resolves environment variables. Nil means os.Getenv.
	Getenv func(string) string
}

// Load builds a Config from the baked-in YAML, then the optional file, then
// environment variables. An empty environment variable does not override a
// value that is already set.
func Load(opts LoadOptions) (Config, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	cfg, err := parse(defaultYAML())
	if err != nil {
		return Config{}, fmt.Errorf("baked-in config: %w", err)
	}

	if opts.Path != "" {
		raw, err := os.ReadFile(opts.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && !opts.RequireFile {
				raw = nil
			} else {
				return Config{}, fmt.Errorf("read config %s: %w", opts.Path, err)
			}
		}
		if len(raw) > 0 {
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				return Config{}, fmt.Errorf("parse config %s: %w", opts.Path, err)
			}
		}
	}

	if err := cfg.applyEnv(getenv); err != nil {
		return Config{}, err
	}
	cfg.normalize()
	if err := cfg.parseFit(); err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func parse(raw []byte) (Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) normalize() {
	c.Server.Host = strings.TrimSpace(c.Server.Host)
	c.Server.HostKeyPath = strings.TrimSpace(c.Server.HostKeyPath)
	c.Server.AuthMode = strings.ToLower(strings.TrimSpace(c.Server.AuthMode))
	c.Server.AuthorizedKeysPath = strings.TrimSpace(c.Server.AuthorizedKeysPath)
	c.DMR.BaseURL = strings.TrimRight(strings.TrimSpace(c.DMR.BaseURL), "/")
	c.Welcome.Title = strings.TrimSpace(c.Welcome.Title)
	c.Welcome.Message = strings.TrimSpace(c.Welcome.Message)
	c.Welcome.Hint = strings.TrimSpace(c.Welcome.Hint)
	c.Welcome.BannerFile = strings.TrimSpace(c.Welcome.BannerFile)
	c.Models.Title = strings.TrimSpace(c.Models.Title)
	c.Models.Empty = strings.TrimSpace(c.Models.Empty)
	c.Models.Hint = strings.TrimSpace(c.Models.Hint)
	c.Models.Loading = strings.TrimSpace(c.Models.Loading)
	c.Loading.Title = strings.TrimSpace(c.Loading.Title)
	c.Loading.Message = strings.TrimSpace(c.Loading.Message)
	c.Chat.Title = strings.TrimSpace(c.Chat.Title)
	c.Chat.Placeholder = strings.TrimSpace(c.Chat.Placeholder)
	c.Chat.SystemPrompt = strings.TrimSpace(c.Chat.SystemPrompt)
	c.Chat.YouLabel = strings.TrimSpace(c.Chat.YouLabel)
	c.Chat.Markdown = strings.ToLower(strings.TrimSpace(c.Chat.Markdown))
	if c.Chat.Markdown == "" {
		c.Chat.Markdown = "on"
	}
	c.Footer.ChangeModel = strings.TrimSpace(c.Footer.ChangeModel)
	c.Footer.Exit = strings.TrimSpace(c.Footer.Exit)
	c.Footer.Scroll = strings.TrimSpace(c.Footer.Scroll)
	c.Uploads.Dir = strings.TrimSpace(c.Uploads.Dir)
	c.GPU.Mode = strings.ToLower(strings.TrimSpace(c.GPU.Mode))
	c.GPU.SMIPath = strings.TrimSpace(c.GPU.SMIPath)
	if c.Uploads.Dir == "" {
		c.Uploads.Dir = "/uploads"
	}
	if c.Uploads.MaxBytes <= 0 {
		c.Uploads.MaxBytes = 10 << 20
	}
	if c.Uploads.MaxFiles <= 0 {
		c.Uploads.MaxFiles = 1000
	}
	if c.Uploads.MaxTotalBytes <= 0 {
		c.Uploads.MaxTotalBytes = 200 << 20
	}
	c.Uploads.Ignore = strings.TrimSpace(c.Uploads.Ignore)
	if c.Uploads.Ignore == "" {
		c.Uploads.Ignore = DefaultUploadIgnore
	}
	if c.Uploads.AutoInclude <= 0 {
		c.Uploads.AutoInclude = 20
	}
	if c.Uploads.TokenBudget <= 0 {
		c.Uploads.TokenBudget = 8192
	}
	if c.Uploads.ImageMaxPx <= 0 {
		c.Uploads.ImageMaxPx = 1536
	}
	if c.Uploads.ImageMaxBytes <= 0 {
		c.Uploads.ImageMaxBytes = 2 << 20
	}
	if c.Uploads.FilesBoxRows <= 0 {
		c.Uploads.FilesBoxRows = 4
	}
	c.Uploads.VisionModels = strings.TrimSpace(c.Uploads.VisionModels)
	c.Uploads.NonVisionModels = strings.TrimSpace(c.Uploads.NonVisionModels)
	c.normalizeEdit()
	if c.GPU.Mode == "" {
		c.GPU.Mode = "auto"
	}
	if c.GPU.Interval <= 0 {
		c.GPU.Interval = Duration(2 * time.Second)
	}
	if c.GPU.SMIPath == "" {
		c.GPU.SMIPath = "nvidia-smi"
	}
	if c.GPU.TempWarn <= 0 {
		c.GPU.TempWarn = 40
	}
	if c.GPU.TempHot <= 0 {
		c.GPU.TempHot = 60
	}
	if c.GPU.TempCrit <= 0 {
		c.GPU.TempCrit = 70
	}
	if c.GPU.PowerWarn <= 0 {
		c.GPU.PowerWarn = 25
	}
	if c.GPU.PowerHot <= 0 {
		c.GPU.PowerHot = 100
	}
	if c.GPU.PowerCrit <= 0 {
		c.GPU.PowerCrit = 200
	}
	if c.GPU.MemWarn <= 0 {
		c.GPU.MemWarn = 40
	}
	if c.GPU.MemHot <= 0 {
		c.GPU.MemHot = 60
	}
	if c.GPU.MemCrit <= 0 {
		c.GPU.MemCrit = 90
	}
	c.normalizeFit()
}

func (c *Config) normalizeFit() {
	f := &c.Fit
	f.Check = strings.ToLower(strings.TrimSpace(f.Check))
	if f.Check == "" {
		f.Check = "on"
	}
	f.ContextSelect = onOff(f.ContextSelect)
	f.TargetSelect = onOff(f.TargetSelect)
	f.AllowCPU = onOff(f.AllowCPU)
	f.ModelStorePath = strings.TrimSpace(f.ModelStorePath)
	f.KVCacheType = strings.ToLower(strings.TrimSpace(f.KVCacheType))
	f.UsagePath = strings.TrimSpace(f.UsagePath)
	switch strings.ToLower(f.UsagePath) {
	case "":
		f.UsagePath = "/data/fit/usage.json"
	case "off", "none":
		f.UsagePath = ""
	}
	f.DefaultContext = strings.ToLower(strings.TrimSpace(f.DefaultContext))
	if f.DefaultContext == "" {
		f.DefaultContext = "auto"
	}
	if f.MarginMiB <= 0 {
		f.MarginMiB = 1024
	}
	if f.MarginPct <= 0 {
		f.MarginPct = 5
	}
	if strings.TrimSpace(f.Contexts) == "" {
		f.Contexts = "8k,16k,32k,64k,128k"
	}
}

func (c *Config) normalizeEdit() {
	e := &c.Edit
	e.Mode = onOff(e.Mode)
	e.Protocol = strings.ToLower(strings.TrimSpace(e.Protocol))
	if e.Protocol == "" {
		e.Protocol = "blocks"
	}
	if e.MaxFiles <= 0 {
		e.MaxFiles = 20
	}
	e.AllowNew = onOff(e.AllowNew)
	if strings.TrimSpace(e.AllowDelete) == "" {
		e.AllowDelete = "off"
	}
	e.AllowDelete = onOff(e.AllowDelete)
	d := &c.Downloads
	d.Dir = strings.TrimSpace(d.Dir)
	if d.Dir == "" {
		d.Dir = "/downloads"
	}
	if d.MaxTotalBytes <= 0 {
		d.MaxTotalBytes = 200 << 20
	}
	d.Archive = strings.ToLower(strings.TrimSpace(d.Archive))
	if d.Archive == "" {
		d.Archive = "tar.gz"
	}
	d.PublicHost = strings.TrimSpace(d.PublicHost)
}

func onOff(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "off", "false", "no", "0":
		return "off"
	}
	return "on"
}

// ParseTokens reads "32768", "32k" or "32K".
func ParseTokens(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := 1
	if strings.HasSuffix(s, "k") {
		mult = 1024
		s = strings.TrimSuffix(s, "k")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid token count %q", s)
	}
	return n * mult, nil
}

func (c *Config) parseFit() error {
	f := &c.Fit
	switch f.Check {
	case "on", "off", "strict":
	default:
		return fmt.Errorf("FIT_CHECK must be on, off or strict, got %q", f.Check)
	}
	if f.MarginPct >= 50 {
		return fmt.Errorf("FIT_MARGIN_PCT must be below 50")
	}
	f.ContextChoices = nil
	for _, part := range strings.Split(f.Contexts, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		n, err := ParseTokens(part)
		if err != nil {
			return fmt.Errorf("CONTEXT_CHOICES: %w", err)
		}
		f.ContextChoices = append(f.ContextChoices, n)
	}
	if len(f.ContextChoices) == 0 {
		return fmt.Errorf("CONTEXT_CHOICES is empty")
	}
	sort.Ints(f.ContextChoices)
	f.DefaultContextTokens = 0
	if f.DefaultContext != "auto" {
		n, err := ParseTokens(f.DefaultContext)
		if err != nil {
			return fmt.Errorf("DEFAULT_CONTEXT: %w", err)
		}
		f.DefaultContextTokens = n
	}
	switch f.KVCacheType {
	case "", "f16", "bf16", "f32", "q8_0", "q5_0", "q5_1", "q4_0", "q4_1", "iq4_nl":
	default:
		return fmt.Errorf("KV_CACHE_TYPE %q is not a llama.cpp cache type", f.KVCacheType)
	}
	if f.CPUThreads < 0 {
		return fmt.Errorf("CPU_THREADS must be >= 0")
	}
	return nil
}

func (c *Config) applyEnv(getenv func(string) string) error {
	set := func(key string, dst *string) {
		if v, ok := lookup(getenv, key); ok {
			*dst = v
		}
	}
	set("SSH_HOST", &c.Server.Host)
	set("HOST_KEY_PATH", &c.Server.HostKeyPath)
	set("AUTH_MODE", &c.Server.AuthMode)
	set("AUTHORIZED_KEYS_PATH", &c.Server.AuthorizedKeysPath)
	set("DMR_BASE_URL", &c.DMR.BaseURL)
	set("WELCOME_TITLE", &c.Welcome.Title)
	set("WELCOME_MESSAGE", &c.Welcome.Message)
	set("WELCOME_HINT", &c.Welcome.Hint)
	set("WELCOME_BANNER_FILE", &c.Welcome.BannerFile)
	set("MODELS_TITLE", &c.Models.Title)
	set("MODELS_EMPTY", &c.Models.Empty)
	set("MODELS_HINT", &c.Models.Hint)
	set("MODELS_LOADING", &c.Models.Loading)
	set("LOADING_TITLE", &c.Loading.Title)
	set("LOADING_MESSAGE", &c.Loading.Message)
	set("CHAT_TITLE", &c.Chat.Title)
	set("CHAT_PLACEHOLDER", &c.Chat.Placeholder)
	set("SYSTEM_PROMPT", &c.Chat.SystemPrompt)
	set("CHAT_YOU_LABEL", &c.Chat.YouLabel)
	set("MARKDOWN", &c.Chat.Markdown)
	set("FOOTER_CHANGE", &c.Footer.ChangeModel)
	set("FOOTER_EXIT", &c.Footer.Exit)
	set("FOOTER_SCROLL", &c.Footer.Scroll)

	if v, ok := lookup(getenv, "SSH_PORT"); ok {
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("SSH_PORT: %w", err)
		}
		c.Server.Port = port
	}
	if err := applyDurationEnv(getenv, "DMR_REQUEST_TIMEOUT", &c.DMR.RequestTimeout); err != nil {
		return err
	}
	if err := applyDurationEnv(getenv, "DMR_WARM_TIMEOUT", &c.DMR.WarmTimeout); err != nil {
		return err
	}
	if err := applyDurationEnv(getenv, "GPU_POLL_INTERVAL", &c.GPU.Interval); err != nil {
		return err
	}
	set("UPLOAD_DIR", &c.Uploads.Dir)
	set("VISION_MODELS", &c.Uploads.VisionModels)
	set("NON_VISION_MODELS", &c.Uploads.NonVisionModels)
	set("GPU_STATS", &c.GPU.Mode)
	set("EDIT_MODE", &c.Edit.Mode)
	set("EDIT_PROTOCOL", &c.Edit.Protocol)
	set("EDIT_ALLOW_NEW", &c.Edit.AllowNew)
	set("EDIT_ALLOW_DELETE", &c.Edit.AllowDelete)
	if v, ok := lookup(getenv, "EDIT_MAX_FILES"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fmt.Errorf("EDIT_MAX_FILES must be a positive integer")
		}
		c.Edit.MaxFiles = n
	}
	set("DOWNLOAD_DIR", &c.Downloads.Dir)
	set("DOWNLOAD_ARCHIVE", &c.Downloads.Archive)
	set("PUBLIC_HOST", &c.Downloads.PublicHost)
	if v, ok := lookup(getenv, "DOWNLOAD_MAX_TOTAL_BYTES"); ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return fmt.Errorf("DOWNLOAD_MAX_TOTAL_BYTES must be a positive integer")
		}
		c.Downloads.MaxTotalBytes = n
	}
	if v, ok := lookup(getenv, "PUBLIC_PORT"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("PUBLIC_PORT must be a port number (1-65535)")
		}
		c.Downloads.PublicPort = n
	}
	set("NVIDIA_SMI", &c.GPU.SMIPath)
	if v, ok := lookup(getenv, "UPLOAD_MAX_BYTES"); ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("UPLOAD_MAX_BYTES: %w", err)
		}
		if n <= 0 {
			return fmt.Errorf("UPLOAD_MAX_BYTES must be > 0")
		}
		c.Uploads.MaxBytes = n
	}
	if v, ok := lookup(getenv, "UPLOAD_MAX_FILES"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("UPLOAD_MAX_FILES: %w", err)
		}
		if n <= 0 {
			return fmt.Errorf("UPLOAD_MAX_FILES must be > 0")
		}
		c.Uploads.MaxFiles = n
	}
	if v, ok := lookup(getenv, "UPLOAD_MAX_TOTAL_BYTES"); ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("UPLOAD_MAX_TOTAL_BYTES: %w", err)
		}
		if n <= 0 {
			return fmt.Errorf("UPLOAD_MAX_TOTAL_BYTES must be > 0")
		}
		c.Uploads.MaxTotalBytes = n
	}
	set("UPLOAD_IGNORE", &c.Uploads.Ignore)
	if v, ok := lookup(getenv, "UPLOAD_AUTO_INCLUDE"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("UPLOAD_AUTO_INCLUDE: %w", err)
		}
		if n <= 0 {
			return fmt.Errorf("UPLOAD_AUTO_INCLUDE must be > 0")
		}
		c.Uploads.AutoInclude = n
	}
	if v, ok := lookup(getenv, "CONTEXT_TOKEN_BUDGET"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("CONTEXT_TOKEN_BUDGET: %w", err)
		}
		if n <= 0 {
			return fmt.Errorf("CONTEXT_TOKEN_BUDGET must be > 0")
		}
		c.Uploads.TokenBudget = n
	}
	if v, ok := lookup(getenv, "IMAGE_MAX_PX"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("IMAGE_MAX_PX: %w", err)
		}
		if n <= 0 {
			return fmt.Errorf("IMAGE_MAX_PX must be > 0")
		}
		c.Uploads.ImageMaxPx = n
	}
	if v, ok := lookup(getenv, "IMAGE_MAX_BYTES"); ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("IMAGE_MAX_BYTES: %w", err)
		}
		if n <= 0 {
			return fmt.Errorf("IMAGE_MAX_BYTES must be > 0")
		}
		c.Uploads.ImageMaxBytes = n
	}
	if v, ok := lookup(getenv, "FILES_BOX_ROWS"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("FILES_BOX_ROWS: %w", err)
		}
		if n <= 0 {
			return fmt.Errorf("FILES_BOX_ROWS must be > 0")
		}
		c.Uploads.FilesBoxRows = n
	}
	if err := applyFloatEnv(getenv, "GPU_TEMP_WARN", &c.GPU.TempWarn); err != nil {
		return err
	}
	if err := applyFloatEnv(getenv, "GPU_TEMP_HOT", &c.GPU.TempHot); err != nil {
		return err
	}
	if err := applyFloatEnv(getenv, "GPU_TEMP_CRIT", &c.GPU.TempCrit); err != nil {
		return err
	}
	if err := applyFloatEnv(getenv, "GPU_POWER_WARN", &c.GPU.PowerWarn); err != nil {
		return err
	}
	if err := applyFloatEnv(getenv, "GPU_POWER_HOT", &c.GPU.PowerHot); err != nil {
		return err
	}
	if err := applyFloatEnv(getenv, "GPU_POWER_CRIT", &c.GPU.PowerCrit); err != nil {
		return err
	}
	if err := applyFloatEnv(getenv, "GPU_MEM_WARN", &c.GPU.MemWarn); err != nil {
		return err
	}
	if err := applyFloatEnv(getenv, "GPU_MEM_HOT", &c.GPU.MemHot); err != nil {
		return err
	}
	if err := applyFloatEnv(getenv, "GPU_MEM_CRIT", &c.GPU.MemCrit); err != nil {
		return err
	}
	set("FIT_CHECK", &c.Fit.Check)
	set("CONTEXT_CHOICES", &c.Fit.Contexts)
	set("DEFAULT_CONTEXT", &c.Fit.DefaultContext)
	set("CONTEXT_SELECT", &c.Fit.ContextSelect)
	set("TARGET_SELECT", &c.Fit.TargetSelect)
	set("ALLOW_CPU", &c.Fit.AllowCPU)
	set("MODEL_STORE_PATH", &c.Fit.ModelStorePath)
	set("KV_CACHE_TYPE", &c.Fit.KVCacheType)
	set("FIT_USAGE_PATH", &c.Fit.UsagePath)
	if v, ok := lookup(getenv, "FIT_MARGIN_MIB"); ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return fmt.Errorf("FIT_MARGIN_MIB must be a positive integer")
		}
		c.Fit.MarginMiB = n
	}
	if err := applyFloatEnv(getenv, "FIT_MARGIN_PCT", &c.Fit.MarginPct); err != nil {
		return err
	}
	if v, ok := lookup(getenv, "CPU_THREADS"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fmt.Errorf("CPU_THREADS must be a non-negative integer")
		}
		c.Fit.CPUThreads = n
	}
	return nil
}

func applyFloatEnv(getenv func(string) string, key string, dst *float64) error {
	v, ok := lookup(getenv, key)
	if !ok {
		return nil
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if n <= 0 {
		return fmt.Errorf("%s must be > 0", key)
	}
	*dst = n
	return nil
}

func applyDurationEnv(getenv func(string) string, key string, dst *Duration) error {
	v, ok := lookup(getenv, key)
	if !ok {
		return nil
	}
	parsed, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if parsed < 0 {
		return fmt.Errorf("%s: duration must be >= 0", key)
	}
	*dst = Duration(parsed)
	return nil
}

func lookup(getenv func(string) string, key string) (string, bool) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return "", false
	}
	return v, true
}

func (c Config) validate() error {
	if c.Server.Host == "" {
		return errors.New("server.host is empty")
	}
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port %d is outside 1-65535", c.Server.Port)
	}
	if c.Server.HostKeyPath == "" {
		return errors.New("server.host_key_path is empty")
	}
	switch c.Server.AuthMode {
	case "open", "keys":
	default:
		return fmt.Errorf("server.auth_mode %q must be open or keys", c.Server.AuthMode)
	}
	if c.Server.AuthMode == "keys" {
		if c.Server.AuthorizedKeysPath == "" {
			return errors.New("auth_mode=keys requires authorized_keys_path or AUTHORIZED_KEYS_PATH")
		}
		info, err := os.Stat(c.Server.AuthorizedKeysPath)
		if err != nil {
			return fmt.Errorf("authorized keys %s: %w", c.Server.AuthorizedKeysPath, err)
		}
		if info.IsDir() {
			return fmt.Errorf("authorized keys %s is a directory", c.Server.AuthorizedKeysPath)
		}
	}
	u, err := url.Parse(c.DMR.BaseURL)
	if err != nil {
		return fmt.Errorf("dmr.base_url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("dmr.base_url %q must be http or https", c.DMR.BaseURL)
	}
	if u.Host == "" {
		return fmt.Errorf("dmr.base_url %q is missing a host", c.DMR.BaseURL)
	}
	if c.Welcome.Title == "" {
		return errors.New("welcome.title is empty")
	}
	if c.Models.Title == "" {
		return errors.New("models.title is empty")
	}
	if c.Models.Empty == "" {
		return errors.New("models.empty is empty")
	}
	switch c.Chat.Markdown {
	case "on", "off":
	default:
		return fmt.Errorf("chat.markdown %q must be on or off", c.Chat.Markdown)
	}
	switch c.GPU.Mode {
	case "auto", "off":
	default:
		return fmt.Errorf("gpu.mode %q must be auto or off", c.GPU.Mode)
	}
	if c.Uploads.MaxBytes <= 0 {
		return errors.New("uploads.max_bytes must be > 0")
	}
	if c.Uploads.MaxFiles <= 0 {
		return errors.New("uploads.max_files must be > 0")
	}
	if c.Uploads.MaxTotalBytes <= 0 {
		return errors.New("uploads.max_total_bytes must be > 0")
	}
	if c.Uploads.MaxTotalBytes < c.Uploads.MaxBytes {
		return errors.New("uploads.max_total_bytes must be >= uploads.max_bytes")
	}
	if c.Uploads.AutoInclude <= 0 {
		return errors.New("uploads.auto_include must be > 0")
	}
	if c.Uploads.TokenBudget <= 0 {
		return errors.New("uploads.token_budget must be > 0")
	}
	if c.Uploads.ImageMaxPx <= 0 {
		return errors.New("uploads.image_max_px must be > 0")
	}
	if c.Uploads.ImageMaxBytes <= 0 {
		return errors.New("uploads.image_max_bytes must be > 0")
	}
	if c.Uploads.FilesBoxRows <= 0 {
		return errors.New("uploads.files_box_rows must be > 0")
	}
	switch c.Edit.Protocol {
	case "blocks":
	case "tools", "auto":
		return fmt.Errorf("edit.protocol %q is not available yet; use blocks", c.Edit.Protocol)
	default:
		return fmt.Errorf("edit.protocol %q must be blocks", c.Edit.Protocol)
	}
	if c.Edit.MaxFiles <= 0 {
		return errors.New("edit.max_files must be > 0")
	}
	if c.Downloads.Archive != "tar.gz" {
		return fmt.Errorf("downloads.archive %q: only tar.gz is supported", c.Downloads.Archive)
	}
	if c.Downloads.MaxTotalBytes <= 0 {
		return errors.New("downloads.max_total_bytes must be > 0")
	}
	if c.Downloads.PublicPort < 0 || c.Downloads.PublicPort > 65535 {
		return fmt.Errorf("downloads.public_port %d is outside 0-65535", c.Downloads.PublicPort)
	}
	if c.Downloads.Dir == c.Uploads.Dir {
		return errors.New("downloads.dir must differ from uploads.dir")
	}
	if !(c.GPU.TempWarn < c.GPU.TempHot && c.GPU.TempHot < c.GPU.TempCrit) {
		return fmt.Errorf("gpu temperature bands must rise: warn %v hot %v crit %v", c.GPU.TempWarn, c.GPU.TempHot, c.GPU.TempCrit)
	}
	if !(c.GPU.PowerWarn < c.GPU.PowerHot && c.GPU.PowerHot < c.GPU.PowerCrit) {
		return fmt.Errorf("gpu power bands must rise: warn %v hot %v crit %v", c.GPU.PowerWarn, c.GPU.PowerHot, c.GPU.PowerCrit)
	}
	if !(c.GPU.MemWarn < c.GPU.MemHot && c.GPU.MemHot < c.GPU.MemCrit) {
		return fmt.Errorf("gpu memory bands must rise: warn %v hot %v crit %v", c.GPU.MemWarn, c.GPU.MemHot, c.GPU.MemCrit)
	}
	return nil
}
