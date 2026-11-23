// Package tui is the interactive terminal front end.
//
// It is deliberately plain: one list on screen at a time, one line of key
// hints, no panels side by side, and no mouse. Every screen is a table from
// the table package, which scrolls sideways when the terminal is narrow.
//
// The model never blocks: every request to a controller runs as a Bubble Tea
// command and comes back as a message.
package tui

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/mareklanduch/gomada/internal/app"
	"github.com/mareklanduch/gomada/internal/secret"
)

// Options configures the terminal UI.
type Options struct {
	// Host is the host to open instead of asking.
	Host string
	// Site is the site to open instead of the last used one.
	Site string
	// NoColor turns off color.
	NoColor bool
}

// refreshEvery is how often the open tab reloads by itself.
const refreshEvery = 10 * time.Second

// Run starts the terminal UI and returns when the person quits.
func Run(ctx context.Context, a *app.App, opts Options) error {
	popts := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.NoColor {
		popts = append(popts, tea.WithColorProfile(colorprofile.ASCII))
	}
	_, err := tea.NewProgram(New(ctx, a, opts, refreshEvery), popts...).Run()
	if err != nil && ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled) {
		return nil // stopped by a signal, not a failure
	}
	return err
}

// passphrases remembers the passphrases typed during this run, so each is
// asked for once. They are kept in memory only.
type passphrases struct {
	mu     sync.Mutex
	byHost map[string]string
}

func (p *passphrases) set(host, pass string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.byHost[host] = pass
}

func (p *passphrases) forget(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.byHost, host)
}

// lookup is the passphrase source handed to the application core. When it
// has no passphrase it says so, and the UI asks for one.
func (p *passphrases) lookup(host string) (string, error) {
	if env := os.Getenv(secret.EnvPassphrase); env != "" {
		return env, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if pass, ok := p.byHost[host]; ok {
		return pass, nil
	}
	return "", secret.ErrPassphraseRequired
}
