package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/diskspace"
	"github.com/UnixEU/ssh-llm-tui/internal/dmr"
	"github.com/UnixEU/ssh-llm-tui/internal/downloads"
	"github.com/UnixEU/ssh-llm-tui/internal/edits"
	"github.com/UnixEU/ssh-llm-tui/internal/fit"
	"github.com/UnixEU/ssh-llm-tui/internal/gpu"
	"github.com/UnixEU/ssh-llm-tui/internal/gpuinfo"
	"github.com/UnixEU/ssh-llm-tui/internal/modelstore"
	"github.com/UnixEU/ssh-llm-tui/internal/session"
	"github.com/UnixEU/ssh-llm-tui/internal/sshserver"
	"github.com/UnixEU/ssh-llm-tui/internal/sysmem"
	"github.com/UnixEU/ssh-llm-tui/internal/sysstat"
	"github.com/UnixEU/ssh-llm-tui/internal/ui"
	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish/bubbletea"
)

// version is the release. The Docker build may override it with -X.
var version = "1.4.0"

const defaultConfigPath = "/config/config.yaml"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println("ssh-llm-tui", version)
		return
	}
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	logger := log.New(os.Stderr, "ssh-llm-tui: ", log.LstdFlags|log.Lmsgprefix)
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	client, err := dmr.New(dmr.Options{
		BaseURL:        cfg.DMR.BaseURL,
		RequestTimeout: cfg.DMR.RequestTimeout.Duration(),
		WarmTimeout:    cfg.DMR.WarmTimeout.Duration(),
		NoUsage:        !cfg.UsageOn(),
	})
	if err != nil {
		return err
	}
	probeDMR(logger, client, cfg.DMR.BaseURL)

	gate := gpu.New()
	tracker := session.New(session.Options{
		Gate:    gate,
		Unload:  client.UnloadAll,
		Timeout: 15 * time.Second,
		Log:     logger,
	})
	files := uploads.New(cfg.Uploads.Dir, cfg.Uploads.MaxBytes, cfg.Uploads.MaxFiles)
	files.SetImageLimits(cfg.Uploads.ImageMaxPx, int(cfg.Uploads.ImageMaxBytes))
	files.SetMaxTotalBytes(cfg.Uploads.MaxTotalBytes)
	files.SetIgnore(uploads.ParseIgnore(cfg.Uploads.Ignore))
	files.SetAutoInclude(cfg.Uploads.TokenBudget, cfg.Uploads.AutoInclude)
	if err := os.MkdirAll(cfg.Uploads.Dir, 0o700); err != nil {
		logger.Printf("warning: upload dir %s: %v (files stay in memory)", cfg.Uploads.Dir, err)
	}
	dl := downloads.New(cfg.Downloads.Dir, cfg.Downloads.MaxTotalBytes)
	if _, err := os.Stat(cfg.Downloads.Dir); err != nil {
		logger.Printf("warning: download dir %s: %v (exports will fail)", cfg.Downloads.Dir, err)
	}
	checkSpace(logger, []diskspace.Check{
		{Name: "UPLOAD_MAX_TOTAL_BYTES", Dir: cfg.Uploads.Dir, Limit: cfg.Uploads.MaxTotalBytes},
		{Name: "DOWNLOAD_MAX_TOTAL_BYTES", Dir: cfg.Downloads.Dir, Limit: cfg.Downloads.MaxTotalBytes},
	})
	workspaces := edits.NewManager()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	gpus := gpuinfo.NewPoller(cfg.GPU.Mode, cfg.GPU.SMIPath, cfg.GPU.Interval.Duration())
	go gpus.Start(ctx)
	var sys *sysstat.Poller
	if cfg.SysStatsOn() {
		sys = sysstat.NewPoller(cfg.GPU.Interval.Duration())
		go sys.Start(ctx)
	}
	resolver, learned := setupFit(logger, cfg)
	srv, err := sshserver.NewWith(cfg.Server, func(sess ssh.Session) (tea.Model, []tea.ProgramOption) {
		model := ui.New(ui.Options{
			Config:    cfg,
			Client:    client,
			Gate:      gate,
			Files:     files,
			Downloads: dl,
			Edits:     workspaces,
			GPU:       gpus,
			Sys:       sys,
			Unloader:  tracker,
			Budget:    cfg.Uploads.TokenBudget,
			Resolver:  resolver,
			Learned:   learned,
			ReadRAM:   sysmem.Read,
			Renderer:  bubbletea.MakeRenderer(sess),
			User:      sess.User(),
			Version:   version,
			Session:   sess.Context(),
		})
		return model, []tea.ProgramOption{
			tea.WithAltScreen(),
			tea.WithoutSignalHandler(),
		}
	}, tracker, sshserver.Stores{Uploads: files, Downloads: dl, Extra: []sshserver.UserSessions{workspaces}})
	if err != nil {
		return err
	}

	logger.Printf("version %s listening on %s auth=%s dmr=%s", version, cfg.Server.Addr(), cfg.Server.AuthMode, cfg.DMR.BaseURL)
	if cfg.Server.AuthMode == "open" {
		logger.Printf("warning: AUTH_MODE=open accepts every connection. Use it on a trusted LAN only.")
	}
	if cfg.Server.AuthMode == "open" && cfg.Server.AuthorizedKeysPath != "" {
		logger.Printf("warning: authorized keys file %s is not enforced while AUTH_MODE=open", cfg.Server.AuthorizedKeysPath)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !sshserver.IsClosed(err) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Printf("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// setupFit opens the optional read-only model store mount and the measured
// usage file. Neither is required: without the store the check falls back
// to measured usage and asks before loading a model of unknown size.
func setupFit(logger *log.Logger, cfg config.Config) (*fit.Resolver, *fit.Learned) {
	store, err := modelstore.Open(cfg.Fit.ModelStorePath)
	switch {
	case err != nil:
		logger.Printf("warning: %v; model sizes come from measured usage only", err)
	case store != nil:
		logger.Printf("model store mounted at %s (read-only header reads)", store.Root())
	case cfg.FitOn():
		logger.Printf("MODEL_STORE_PATH is not set; the memory check uses measured usage and asks before loading unknown models")
	}
	return fit.NewResolver(store), fit.LoadLearned(cfg.Fit.UsagePath)
}

func loadConfig() (config.Config, error) {
	path := os.Getenv("CONFIG_PATH")
	opts := config.LoadOptions{Getenv: os.Getenv}
	if path != "" {
		opts.Path = path
		opts.RequireFile = true
	} else if _, err := os.Stat(defaultConfigPath); err == nil {
		opts.Path = defaultConfigPath
	}
	cfg, err := config.Load(opts)
	if err != nil {
		return config.Config{}, err
	}
	return cfg, nil
}

func probeDMR(logger *log.Logger, client *dmr.Client, base string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	models, err := client.ListModels(ctx)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) {
			logger.Printf("warning: DMR at %s is not reachable yet: %v", base, err)
			return
		}
		logger.Printf("warning: DMR at %s: %v", base, err)
		return
	}
	logger.Printf("DMR reachable at %s (%d model(s) pulled)", base, len(models))
}

// checkSpace warns when a per-user limit is larger than the filesystem
// that holds it (usually a compose tmpfs), since uploads or exports would
// then fail before the limit is reached.
func checkSpace(logger *log.Logger, checks []diskspace.Check) {
	for _, c := range checks {
		info, err := diskspace.Read(c.Dir)
		if err != nil {
			continue
		}
		if diskspace.Short(info, c.Limit) {
			logger.Printf("warning: %s is %s, but %s holds only %s; raise the tmpfs size (UPLOADS_TMPFS_SIZE / DOWNLOADS_TMPFS_SIZE in .env) or lower the limit",
				c.Name, config.FormatBytes(c.Limit), c.Dir, config.FormatBytes(info.TotalBytes))
		}
	}
}
