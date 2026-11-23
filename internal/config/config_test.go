package config_test

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/mareklanduch/gomada/internal/config"
)

func TestLoadMissingFileIsEmpty(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "nope", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != config.Version || len(cfg.Hosts) != 0 || cfg.Hosts == nil {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	want := &config.Config{
		Version: config.Version,
		Current: "home",
		Hosts: map[string]*config.Host{
			"home": {
				URL: "https://192.168.0.2:8043", Controller: "abc", ClientID: "id",
				SecretStore: "keyring", TLSPin: "sha256:5d41", Site: "Default",
			},
			"office": {URL: "https://omada.example.net", Controller: "def", ClientID: "id2", SecretStore: "file"},
		},
	}
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if names := got.HostNames(); !reflect.DeepEqual(names, []string{"home", "office"}) {
		t.Errorf("HostNames = %v", names)
	}
}

func TestSaveIsPrivateAndLeavesNoTempFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gomada")
	path := filepath.Join(dir, "config.yaml")
	cfg := &config.Config{Hosts: map[string]*config.Host{}}
	for range 2 { // the second save replaces an existing file
		if err := cfg.Save(path); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.yaml" {
		t.Errorf("directory holds %v, want only config.yaml", entries)
	}
	if runtime.GOOS == "windows" {
		return // Windows has no owner-only permission bits
	}
	for p, want := range map[string]os.FileMode{path: 0o600, dir: 0o700} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s has mode %o, want %o", p, got, want)
		}
	}
}

func TestSavedFileHoldsNoSecretFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{Hosts: map[string]*config.Host{"home": {URL: "u", ClientID: "id", SecretStore: "keyring"}}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"secret:", "client_secret", "token", "password"} {
		if strings.Contains(string(data), word) {
			t.Errorf("config file contains %q:\n%s", word, data)
		}
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]string{
		"not yaml":       "hosts: [",
		"unknown field":  "version: 1\nhots: {}\n",
		"future version": "version: 99\n",
		"empty host":     "version: 1\nhosts:\n  home:\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(path); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestLoadWithoutVersionIsMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "current: home\nhosts:\n  home:\n    url: http://x\n    controller: c\n    client_id: i\n    secret_store: env\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Version != config.Version || cfg.Hosts["home"].URL != "http://x" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Hosts == nil {
		t.Errorf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv(config.EnvPath, "/tmp/custom.yaml")
	if got, _ := config.DefaultPath(); got != "/tmp/custom.yaml" {
		t.Errorf("with %s: got %q", config.EnvPath, got)
	}
	t.Setenv(config.EnvPath, "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got, _ := config.DefaultPath(); got != filepath.Join("/xdg", "gomada", "config.yaml") {
		t.Errorf("with XDG_CONFIG_HOME: got %q", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skip("no user config dir here:", err)
	}
	if got, _ := config.DefaultPath(); got != filepath.Join(base, "gomada", "config.yaml") {
		t.Errorf("default: got %q", got)
	}
}

func TestValidateHostName(t *testing.T) {
	for _, ok := range []string{"home", "office-2", "a.b_c", "9"} {
		if err := config.ValidateHostName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "a b", "a/b", "a:b", "ü", strings.Repeat("a", 64)} {
		if err := config.ValidateHostName(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

func TestSavedFileIsReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := &config.Config{Current: "home", Hosts: map[string]*config.Host{
		"home": {URL: "https://192.168.0.2:8043", Controller: "abc", ClientID: "id", SecretStore: "keyring"},
	}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `version: 1
current: home
hosts:
  home:
    url: https://192.168.0.2:8043
    controller: abc
    client_id: id
    secret_store: keyring
`
	if string(data) != want {
		t.Errorf("got:\n%s\nwant:\n%s", data, want)
	}
}
