// Package app holds the use cases shared by the command line and the
// terminal UI: managing hosts, connecting, and acting on devices and
// clients. Both front ends call this package and never the API client
// directly, so a feature behaves the same in both.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mareklanduch/gomada/internal/config"
	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/secret"
	"github.com/mareklanduch/gomada/internal/tokencache"
)

// Errors the front ends react to.
var (
	// ErrNoHosts means no host has been added yet.
	ErrNoHosts = errors.New("no host is set up yet; add one with: gomada host add")
	// ErrKeyringUnavailable means this system has no usable keychain, so
	// the secret has to go to an encrypted file instead.
	ErrKeyringUnavailable = errors.New("no system keychain is available")
	// ErrUntrustedCertificate means the controller's certificate is not
	// trusted and no pin was given.
	ErrUntrustedCertificate = errors.New("the controller's certificate is not trusted")
)

// Options configures an App.
type Options struct {
	// ConfigPath is the configuration file. Empty means the default path.
	ConfigPath string
	// Timeout bounds each request to a controller. Zero means the default.
	Timeout time.Duration
	// NoTokenCache turns off keeping access tokens between runs.
	NoTokenCache bool
	// Logger, when set, receives one debug line per request to a
	// controller.
	Logger *slog.Logger
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
	// Passphrase returns the passphrase that unlocks a host's secret when
	// it is kept in an encrypted file. Nil means there is no way to ask.
	Passphrase func(host string) (string, error)
}

// App is the application core. It is safe for concurrent use.
type App struct {
	opts Options
	path string

	mu  sync.Mutex
	cfg *config.Config
}

// New loads the configuration and returns the application core.
func New(opts Options) (*App, error) {
	path := opts.ConfigPath
	if path == "" {
		var err error
		if path, err = config.DefaultPath(); err != nil {
			return nil, err
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	return &App{opts: opts, path: path, cfg: cfg}, nil
}

// SetPassphrase replaces the passphrase source. The terminal UI uses it to
// supply a passphrase it has just asked for.
func (a *App) SetPassphrase(fn func(host string) (string, error)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.opts.Passphrase = fn
}

// HostInfo describes a saved host.
type HostInfo struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Controller  string `json:"controller"`
	ClientID    string `json:"client_id"`
	SecretStore string `json:"secret_store"`
	Site        string `json:"site,omitempty"`
	Current     bool   `json:"current"`
}

// Hosts lists the saved hosts in alphabetical order.
func (a *App) Hosts() []HostInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	hosts := make([]HostInfo, 0, len(a.cfg.Hosts))
	for _, name := range a.cfg.HostNames() {
		h := a.cfg.Hosts[name]
		hosts = append(hosts, HostInfo{
			Name: name, URL: h.URL, Controller: h.Controller, ClientID: h.ClientID,
			SecretStore: h.SecretStore, Site: h.Site, Current: name == a.cfg.Current,
		})
	}
	return hosts
}

// CurrentHost returns the name of the current host, or "" if there is none.
func (a *App) CurrentHost() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.Current
}

// UseHost makes a host the current one.
func (a *App) UseHost(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.cfg.Hosts[name]; !ok {
		return a.unknownHost(name)
	}
	a.cfg.Current = name
	return a.cfg.Save(a.path)
}

// RemoveHost deletes a host together with its stored secret and cached
// token.
func (a *App) RemoveHost(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	h, ok := a.cfg.Hosts[name]
	if !ok {
		return a.unknownHost(name)
	}
	if store, err := a.secretStore(name, h.SecretStore); err == nil {
		if err := store.Delete(name); err != nil {
			return err
		}
	}
	// The token may be in either cache, depending on how the host was set
	// up over time, so both are cleared. Failures here must not keep the
	// host from being removed.
	_ = tokencache.Delete(tokencache.Keyring{}, name)
	if dir, ok := tokencache.RuntimeDir(); ok {
		_ = tokencache.Delete(tokencache.File{Dir: dir}, name)
	}
	delete(a.cfg.Hosts, name)
	if a.cfg.Current == name {
		a.cfg.Current = ""
		if names := a.cfg.HostNames(); len(names) > 0 {
			a.cfg.Current = names[0]
		}
	}
	return a.cfg.Save(a.path)
}

func (a *App) unknownHost(name string) error {
	names := a.cfg.HostNames()
	if len(names) == 0 {
		return ErrNoHosts
	}
	return fmt.Errorf("unknown host %q; known hosts: %s", name, strings.Join(names, ", "))
}

// NormalizeURL turns what a person types as the controller address into a
// base URL: it adds https:// when the scheme is missing and drops any path.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("the controller address is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid controller address: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("invalid controller address: unsupported scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return "", errors.New("invalid controller address: no host name")
	}
	return u.Scheme + "://" + u.Host, nil
}

// Certificate describes the TLS certificate a controller presents.
type Certificate struct {
	// Fingerprint identifies the certificate ("sha256:<hex>"). It is empty
	// for plain http addresses.
	Fingerprint string
	// Trusted reports whether the system already trusts the certificate.
	// When false, the fingerprint must be confirmed by a person and pinned.
	Trusted bool
}

// CheckCertificate connects to the controller and reports its certificate.
func (a *App) CheckCertificate(ctx context.Context, rawURL string) (Certificate, error) {
	base, err := NormalizeURL(rawURL)
	if err != nil {
		return Certificate{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, a.timeout())
	defer cancel()
	fp, trusted, err := omada.ProbeTLS(ctx, base)
	if err != nil {
		return Certificate{}, err
	}
	return Certificate{Fingerprint: fp, Trusted: trusted}, nil
}

// NewHost is the input to AddHost.
type NewHost struct {
	Name     string
	URL      string
	ClientID string
	Secret   string
	// ControllerID is the omadacId. Empty means: ask the controller.
	ControllerID string
	// TLSPin is the confirmed fingerprint of an untrusted certificate.
	TLSPin string
	// InsecureSkipTLSVerify accepts any certificate.
	InsecureSkipTLSVerify bool
	// SecretStore is "keyring", "file", or "env". Empty means keyring.
	SecretStore string
}

// ValidateHostName checks that name can be used for a new host: it is well
// formed and not taken.
func (a *App) ValidateHostName(name string) error {
	if err := config.ValidateHostName(name); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.cfg.Hosts[name]; exists {
		return fmt.Errorf("a host named %q already exists; remove it first or pick another name", name)
	}
	return nil
}

// AddHost checks the credentials against the controller and, when they work,
// saves the host and its secret. The first host added becomes the current
// one. It returns ErrKeyringUnavailable when the secret cannot be put in the
// system keychain; the caller can then retry with SecretStore "file".
func (a *App) AddHost(ctx context.Context, h NewHost) error {
	if err := a.ValidateHostName(h.Name); err != nil {
		return err
	}
	base, err := NormalizeURL(h.URL)
	if err != nil {
		return err
	}
	if h.ClientID == "" || h.Secret == "" {
		return errors.New("the client ID and client secret are required")
	}
	kind := h.SecretStore
	if kind == "" {
		kind = secret.KindKeyring
	}

	hc := a.httpClient(omada.TLSOptions{Pin: h.TLSPin, Insecure: h.InsecureSkipTLSVerify})
	controllerID := h.ControllerID
	if controllerID == "" {
		info, err := omada.FetchInfo(ctx, hc, base)
		if err != nil {
			if errors.Is(err, omada.ErrCertificate) && h.TLSPin == "" {
				return fmt.Errorf("%w: %w", ErrUntrustedCertificate, err)
			}
			return fmt.Errorf("could not read the controller ID from %s: %w", base, err)
		}
		controllerID = info.ControllerID
	}
	client := omada.New(omada.Options{
		BaseURL: base, ControllerID: controllerID, ClientID: h.ClientID, HTTPClient: hc,
		Secret: func(context.Context) (string, error) { return h.Secret, nil },
	})
	if err := client.Login(ctx); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	store, err := a.secretStore(h.Name, kind)
	if err != nil {
		return err
	}
	if err := store.Set(h.Name, h.Secret); err != nil {
		if kind == secret.KindKeyring {
			return fmt.Errorf("%w: %w", ErrKeyringUnavailable, err)
		}
		return err
	}
	a.cfg.Hosts[h.Name] = &config.Host{
		URL: base, Controller: controllerID, ClientID: h.ClientID, SecretStore: kind,
		TLSPin: h.TLSPin, InsecureSkipTLSVerify: h.InsecureSkipTLSVerify,
	}
	if a.cfg.Current == "" {
		a.cfg.Current = h.Name
	}
	return a.cfg.Save(a.path)
}

// httpClient returns the HTTP client for one controller.
func (a *App) httpClient(tls omada.TLSOptions) *http.Client {
	hc := omada.NewHTTPClient(tls, a.timeout())
	if a.opts.Logger != nil {
		omada.LogRequests(hc, a.opts.Logger)
	}
	return hc
}

func (a *App) now() time.Time {
	if a.opts.Now != nil {
		return a.opts.Now()
	}
	return time.Now()
}

func (a *App) timeout() time.Duration {
	if a.opts.Timeout > 0 {
		return a.opts.Timeout
	}
	return omada.DefaultTimeout
}

// secretStore returns the store of the given kind for one host. The
// environment variable overrides whatever is stored.
func (a *App) secretStore(host, kind string) (secret.Store, error) {
	switch kind {
	case secret.KindKeyring:
		return secret.EnvOverride{Store: secret.Keyring{}}, nil
	case secret.KindFile:
		var pass func() (string, error)
		if ask := a.opts.Passphrase; ask != nil {
			pass = func() (string, error) { return ask(host) }
		}
		dir := filepath.Join(filepath.Dir(a.path), "secrets")
		return secret.EnvOverride{Store: secret.File{Dir: dir, Passphrase: pass}}, nil
	case secret.KindEnv:
		return secret.Env{}, nil
	}
	return nil, fmt.Errorf("unknown secret store %q; use %s, %s, or %s", kind, secret.KindKeyring, secret.KindFile, secret.KindEnv)
}

// tokenCache returns where the host's access token is kept between runs, or
// nil when tokens are not cached.
func (a *App) tokenCache(host string, h *config.Host) omada.TokenCache {
	if a.opts.NoTokenCache || a.cfg.NoTokenCache {
		return nil
	}
	switch h.SecretStore {
	case secret.KindKeyring:
		return tokencache.For(tokencache.Keyring{}, host, h.ClientID)
	case secret.KindFile:
		if dir, ok := tokencache.RuntimeDir(); ok {
			return tokencache.For(tokencache.File{Dir: dir}, host, h.ClientID)
		}
	}
	return nil // env: nothing is written anywhere
}
