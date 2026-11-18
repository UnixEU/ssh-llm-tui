// Package sshserver is the Wish SSH frontend. A connection gets the TUI
// instead of a shell. Any username is accepted. Auth is either open
// (NoClientAuth) or an authorized_keys file.
package sshserver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/UnixEU/ssh-llm-tui/internal/config"
	"github.com/UnixEU/ssh-llm-tui/internal/session"
	"github.com/UnixEU/ssh-llm-tui/internal/transfer"
	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	"github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
)

// New builds an SSH server. The handler is invoked once per session and must
// return the bubbletea model for that connection. tracker may be nil; when
// set, the last session to exit unloads DMR runners.
func New(cfg config.Server, handler bubbletea.Handler, tracker *session.Tracker, files *uploads.Store) (*ssh.Server, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.HostKeyPath), 0o700); err != nil {
		return nil, fmt.Errorf("host key directory: %w", err)
	}

	// Last middleware runs first. SCP must run before activeterm because
	// scp clients do not request a PTY. The TUI session counter stays inside
	// activeterm so an upload does not count as a chat session.
	mws := []wish.Middleware{bubbletea.Middleware(handler)}
	if tracker != nil || files != nil {
		mws = append(mws, trackSessions(tracker, files))
	}
	mws = append(mws, activeterm.Middleware())
	if files != nil {
		mws = append(mws, transfer.Middleware(files))
	}
	mws = append(mws, logging.Middleware())

	opts := []ssh.Option{
		wish.WithAddress(cfg.Addr()),
		wish.WithHostKeyPath(cfg.HostKeyPath),
		wish.WithVersion("ssh-llm-tui"),
		wish.WithMiddleware(mws...),
	}
	if files != nil {
		opts = append(opts, wish.WithSubsystem("sftp", transfer.Subsystem(files)))
	}

	switch cfg.AuthMode {
	case "open":
		// Leaving every auth handler unset makes charmbracelet/ssh set
		// NoClientAuth. OpenSSH's first "none" request then succeeds, so a
		// client connects with no key and no password. Any username is accepted.
	case "keys":
		opts = append(opts, wish.WithAuthorizedKeys(cfg.AuthorizedKeysPath))
	default:
		return nil, fmt.Errorf("auth mode %q", cfg.AuthMode)
	}

	srv, err := wish.NewServer(opts...)
	if err != nil {
		return nil, err
	}
	return srv, nil
}

func trackSessions(t *session.Tracker, files *uploads.Store) wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(sess ssh.Session) {
			if t != nil {
				t.Enter()
			}
			if files != nil {
				files.SessionEnter(sess.User())
			}
			defer func() {
				if files != nil {
					files.SessionLeave(sess.User())
				}
				if t != nil {
					t.Leave()
				}
			}()
			next(sess)
		}
	}
}

// IsClosed reports the error returned when Shutdown has stopped the listener.
func IsClosed(err error) bool {
	return err == nil || errors.Is(err, ssh.ErrServerClosed)
}
