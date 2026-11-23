// Package config reads and writes the gomada configuration file: the list of
// controller profiles ("hosts") and which one is current. The file never
// holds secrets or tokens.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"go.yaml.in/yaml/v3"
)

// Version is the configuration format written by this build.
const Version = 1

// EnvPath names the environment variable that overrides the config file
// location.
const EnvPath = "GOMADA_CONFIG"

// Config is the content of the configuration file.
type Config struct {
	Version int `yaml:"version"`
	// Current is the name of the host used when none is given.
	Current string `yaml:"current,omitempty"`
	// NoTokenCache turns off keeping access tokens between runs.
	NoTokenCache bool `yaml:"no_token_cache,omitempty"`
	// Hosts are the saved controller profiles by name.
	Hosts map[string]*Host `yaml:"hosts,omitempty"`
}

// Host is one saved controller profile.
type Host struct {
	// URL is the controller address, e.g. "https://192.168.0.2:8043".
	URL string `yaml:"url"`
	// Controller is the controller's omadacId.
	Controller string `yaml:"controller"`
	// ClientID identifies the Open API application.
	ClientID string `yaml:"client_id"`
	// SecretStore says where the client secret is kept: "keyring", "file",
	// or "env".
	SecretStore string `yaml:"secret_store"`
	// TLSPin is the fingerprint of the controller's self-signed certificate.
	TLSPin string `yaml:"tls_pin,omitempty"`
	// InsecureSkipTLSVerify accepts any certificate. Avoid it.
	InsecureSkipTLSVerify bool `yaml:"insecure_skip_tls_verify,omitempty"`
	// Site is the site used last on this host.
	Site string `yaml:"site,omitempty"`
}

// DefaultPath returns where the configuration file lives: $GOMADA_CONFIG if
// set, else gomada/config.yaml below $XDG_CONFIG_HOME if set, else below the
// operating system's configuration directory.
func DefaultPath() (string, error) {
	if p := os.Getenv(EnvPath); p != "" {
		return p, nil
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		var err error
		if base, err = os.UserConfigDir(); err != nil {
			return "", fmt.Errorf("cannot find the configuration directory: %w", err)
		}
	}
	return filepath.Join(base, "gomada", "config.yaml"), nil
}

// Load reads the configuration file. A missing file is not an error: it
// yields an empty configuration.
func Load(path string) (*Config, error) {
	cfg := &Config{Version: Version, Hosts: map[string]*Host{}}
	data, err := os.ReadFile(path) //nolint:gosec // the path is the user's own config file
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("%s is not a valid config file: %w", path, err)
	}
	if err := migrate(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.Hosts == nil {
		cfg.Hosts = map[string]*Host{}
	}
	for name, h := range cfg.Hosts {
		if h == nil {
			return nil, fmt.Errorf("%s: host %q is empty", path, name)
		}
	}
	return cfg, nil
}

// migrate brings a configuration read from disk up to the current Version.
// Each future format change adds one step here.
func migrate(cfg *Config) error {
	switch {
	case cfg.Version == 0:
		cfg.Version = Version // files written by hand without a version
	case cfg.Version > Version:
		return fmt.Errorf("the config file has version %d, but this gomada understands up to version %d; please upgrade gomada", cfg.Version, Version)
	}
	return nil
}

// Save writes the configuration atomically, readable by the owner only.
func (c *Config) Save(path string) error {
	c.Version = Version
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return WriteFileAtomic(path, buf.Bytes())
}

// WriteFileAtomic writes data to path through a temporary file and a rename,
// so a crash never leaves a half-written file. The file and any directory it
// has to create are accessible to the owner only.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".gomada-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // a no-op after the rename succeeded
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// HostNames returns the host names in alphabetical order.
func (c *Config) HostNames() []string {
	names := make([]string, 0, len(c.Hosts))
	for name := range c.Hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var hostName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// ValidateHostName checks a profile name. Names end up in file names and
// keychain entries, so they are limited to letters, digits, dot, dash, and
// underscore.
func ValidateHostName(name string) error {
	if !hostName.MatchString(name) {
		return fmt.Errorf("invalid host name %q: use letters, digits, '.', '-', or '_', starting with a letter or digit, at most 63 characters", name)
	}
	return nil
}
