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
	"github.com/UnixEU/ssh-llm-tui/internal/downloads"
	"github.com/UnixEU/ssh-llm-tui/internal/session"
	"github.com/UnixEU/ssh-llm-tui/internal/transfer"
	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/activeterm"
	"github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
)

// UserSessions is per-user state that lives while the user has a TUI
// session open, such as edited files.
type UserSessions interface {
	SessionEnter(user string)
	SessionLeave(user string)
}

// Stores are the per-user file areas served over SCP and SFTP.
type Stores struct {
	Uploads   *uploads.Store
	Downloads *downloads.Store
	// Extra is other per-user state cleared with the last TUI session.
	Extra []UserSessions
}

// New builds an SSH server. The handler is invoked once per session and must
// return the bubbletea model for that connection. tracker may be nil; when
// set, the last session to exit unloads DMR runners.
func New(cfg config.Server, handler bubbletea.Handler, tracker *session.Tracker, files *uploads.Store) (*ssh.Server, error) {
	return NewWith(cfg, handler, tracker, Stores{Uploads: files})
}

// NewWith is New with downloads and other per-user state.
func NewWith(cfg config.Server, handler bubbletea.Handler, tracker *session.Tracker, stores Stores) (*ssh.Server, error) {
	files := stores.Uploads
	var users []UserSessions
	if files != nil {
		users = append(users, files)
	}
	if stores.Downloads != nil {
		users = append(users, stores.Downloads)
	}
	users = append(users, stores.Extra...)
	if err := os.MkdirAll(filepath.Dir(cfg.HostKeyPath), 0o700); err != nil {
		return nil, fmt.Errorf("host key directory: %w", err)
	}

	// Last middleware runs first. SCP must run before activeterm because
	// scp clients do not request a PTY. The TUI session counter stays inside
	// activeterm so an upload does not count as a chat session.
	mws := []wish.Middleware{bubbletea.Middleware(handler)}
	if tracker != nil || len(users) > 0 {
		mws = append(mws, trackSessions(tracker, users))
	}
	mws = append(mws, activeterm.Middleware())
	if files != nil {
		mws = append(mws, transfer.MiddlewareWith(files, stores.Downloads))
	}
	mws = append(mws, logging.Middleware())

	opts := []ssh.Option{
		wish.WithAddress(cfg.Addr()),
		wish.WithHostKeyPath(cfg.HostKeyPath),
		wish.WithVersion("ssh-llm-tui"),
		wish.WithMiddleware(mws...),
	}
	if files != nil {
		opts = append(opts, wish.WithSubsystem("sftp", transfer.SubsystemWith(files, stores.Downloads)))
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

func trackSessions(t *session.Tracker, users []UserSessions) wish.Middleware {
	return func(next ssh.Handler) ssh.Handler {
		return func(sess ssh.Session) {
			if t != nil {
				t.Enter()
			}
			for _, u := range users {
				u.SessionEnter(sess.User())
			}
			defer func() {
				for _, u := range users {
					u.SessionLeave(sess.User())
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
