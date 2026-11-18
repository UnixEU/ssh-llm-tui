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
	GPU     GPU     `yaml:"gpu"`
}

// Uploads limits files received over SCP and SFTP.
type Uploads struct {
	Dir             string `yaml:"dir"`
	MaxBytes        int64  `yaml:"max_bytes"`
	MaxFiles        int    `yaml:"max_files"`
	TokenBudget     int    `yaml:"token_budget"`
	ImageMaxPx      int    `yaml:"image_max_px"`
	ImageMaxBytes   int64  `yaml:"image_max_bytes"`
	FilesBoxRows    int    `yaml:"files_box_rows"`
	VisionModels    string `yaml:"vision_models"`
	NonVisionModels string `yaml:"non_vision_models"`
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
		c.Uploads.MaxFiles = 10
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
