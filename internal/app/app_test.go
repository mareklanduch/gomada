package app_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/mareklanduch/gomada/internal/app"
	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/omada/omadatest"
	"github.com/mareklanduch/gomada/internal/secret"
)

// env is a test fixture: a fake controller, an empty config in a temp
// directory, and an in-memory keychain.
type env struct {
	t    *testing.T
	fake *omadatest.Controller
	path string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	keyring.MockInit()
	t.Setenv(secret.EnvVar, "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	return &env{t: t, fake: omadatest.New(t), path: filepath.Join(t.TempDir(), "gomada", "config.yaml")}
}

func (e *env) app(mutate ...func(*app.Options)) *app.App {
	e.t.Helper()
	opts := app.Options{ConfigPath: e.path}
	for _, m := range mutate {
		m(&opts)
	}
	a, err := app.New(opts)
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *env) host(name string) app.NewHost {
	return app.NewHost{Name: name, URL: e.fake.URL, ClientID: omadatest.ClientID, Secret: omadatest.Secret}
}

func (e *env) add(a *app.App, name string) {
	e.t.Helper()
	if err := a.AddHost(e.t.Context(), e.host(name)); err != nil {
		e.t.Fatalf("AddHost(%s): %v", name, err)
	}
}

func (e *env) session(a *app.App, name string) *app.Session {
	e.t.Helper()
	s, err := a.Connect(name)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func passphrase(p string) func(*app.Options) {
	return func(o *app.Options) { o.Passphrase = func(string) (string, error) { return p, nil } }
}

func TestAddHost(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	e.add(a, "home")
	e.add(a, "office")

	hosts := a.Hosts()
	if len(hosts) != 2 || hosts[0].Name != "home" || !hosts[0].Current || hosts[1].Current {
		t.Fatalf("hosts = %+v, want home (current) and office", hosts)
	}
	if h := hosts[0]; h.Controller != omadatest.ControllerID || h.SecretStore != "keyring" || h.URL != e.fake.URL {
		t.Errorf("host = %+v", h)
	}
	if got, err := keyring.Get("gomada", "home"); err != nil || got != omadatest.Secret {
		t.Errorf("keychain holds %q, %v", got, err)
	}
	raw, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), omadatest.Secret) {
		t.Errorf("the config file contains the secret:\n%s", raw)
	}

	// A fresh App sees the saved state.
	if got := e.app().CurrentHost(); got != "home" {
		t.Errorf("after reload: current = %q", got)
	}
}

func TestAddHostRejections(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	e.add(a, "home")

	tests := map[string]func(*app.NewHost){
		"duplicate name":   func(h *app.NewHost) { h.Name = "home" },
		"invalid name":     func(h *app.NewHost) { h.Name = "my host" },
		"empty url":        func(h *app.NewHost) { h.URL = " " },
		"missing secret":   func(h *app.NewHost) { h.Secret = "" },
		"missing clientID": func(h *app.NewHost) { h.ClientID = "" },
		"unknown store":    func(h *app.NewHost) { h.SecretStore = "vault" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			h := e.host("other")
			mutate(&h)
			if err := a.AddHost(t.Context(), h); err == nil {
				t.Error("want an error")
			}
			if len(a.Hosts()) != 1 {
				t.Errorf("hosts = %+v, want only home", a.Hosts())
			}
		})
	}
}

func TestAddHostWrongSecretSavesNothing(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	h := e.host("home")
	h.Secret = "wrong"
	if err := a.AddHost(t.Context(), h); !errors.Is(err, omada.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	if len(a.Hosts()) != 0 {
		t.Error("a host with bad credentials must not be saved")
	}
	if _, err := keyring.Get("gomada", "home"); err == nil {
		t.Error("the bad secret must not be stored")
	}
}

func TestAddHostUnreachable(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	h := e.host("home")
	e.fake.Close()
	if err := a.AddHost(t.Context(), h); !errors.Is(err, omada.ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
}

func TestAddHostExplicitControllerID(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	h := e.host("home")
	h.ControllerID = "wrong-id"
	if err := a.AddHost(t.Context(), h); !errors.Is(err, omada.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth: the given ID must be used, not discovered", err)
	}
	h.ControllerID = omadatest.ControllerID
	if err := a.AddHost(t.Context(), h); err != nil {
		t.Fatal(err)
	}
}

func TestSelfSignedCertificateFlow(t *testing.T) {
	e := newEnv(t)
	e.fake = omadatest.NewTLS(t)
	a := e.app()

	cert, err := a.CheckCertificate(t.Context(), e.fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Trusted || cert.Fingerprint != omada.Fingerprint(e.fake.Certificate()) {
		t.Fatalf("cert = %+v", cert)
	}

	h := e.host("home")
	if err := a.AddHost(t.Context(), h); !errors.Is(err, app.ErrUntrustedCertificate) {
		t.Fatalf("without a pin: err = %v, want ErrUntrustedCertificate", err)
	}
	h.TLSPin = "sha256:beef"
	if err := a.AddHost(t.Context(), h); !errors.Is(err, omada.ErrCertificate) {
		t.Fatalf("with a wrong pin: err = %v, want ErrCertificate", err)
	}
	h.TLSPin = cert.Fingerprint
	if err := a.AddHost(t.Context(), h); err != nil {
		t.Fatal(err)
	}

	// The saved pin is used for later connections.
	s := e.session(e.app(), "home")
	if _, err := s.Sites(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestInsecureHost(t *testing.T) {
	e := newEnv(t)
	e.fake = omadatest.NewTLS(t)
	a := e.app()
	h := e.host("home")
	h.InsecureSkipTLSVerify = true
	if err := a.AddHost(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if _, err := e.session(e.app(), "home").Sites(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCertificatePlainHTTP(t *testing.T) {
	e := newEnv(t)
	cert, err := e.app().CheckCertificate(t.Context(), e.fake.URL)
	if err != nil || !cert.Trusted || cert.Fingerprint != "" {
		t.Errorf("cert = %+v, err = %v", cert, err)
	}
}

func TestKeyringUnavailableFallsBackToFile(t *testing.T) {
	e := newEnv(t)
	keyring.MockInitWithError(errors.New("no secret service"))

	a := e.app(passphrase("pw"))
	h := e.host("home")
	if err := a.AddHost(t.Context(), h); !errors.Is(err, app.ErrKeyringUnavailable) {
		t.Fatalf("err = %v, want ErrKeyringUnavailable", err)
	}
	if len(a.Hosts()) != 0 {
		t.Fatal("nothing must be saved when the secret could not be stored")
	}
	h.SecretStore = secret.KindFile
	if err := a.AddHost(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e.path), "secrets", "home.secret.json")); err != nil {
		t.Errorf("secret file: %v", err)
	}

	t.Run("right passphrase", func(t *testing.T) {
		if _, err := e.session(e.app(passphrase("pw")), "home").Sites(t.Context()); err != nil {
			t.Error(err)
		}
	})
	t.Run("wrong passphrase", func(t *testing.T) {
		_, err := e.session(e.app(passphrase("nope")), "home").Sites(t.Context())
		if !errors.Is(err, secret.ErrWrongPassphrase) {
			t.Errorf("err = %v, want ErrWrongPassphrase", err)
		}
	})
	t.Run("no way to ask", func(t *testing.T) {
		s := e.session(e.app(), "home")
		if err := s.Unlock(); !errors.Is(err, secret.ErrPassphraseRequired) {
			t.Errorf("err = %v, want ErrPassphraseRequired", err)
		}
	})
	t.Run("passphrase supplied later", func(t *testing.T) {
		a := e.app()
		a.SetPassphrase(func(host string) (string, error) {
			if host != "home" {
				t.Errorf("asked for host %q", host)
			}
			return "pw", nil
		})
		if err := e.session(a, "home").Unlock(); err != nil {
			t.Error(err)
		}
	})
}

func TestSecretIsReadOncePerSession(t *testing.T) {
	e := newEnv(t)
	keyring.MockInitWithError(errors.New("no secret service"))
	h := e.host("home")
	h.SecretStore = secret.KindFile
	if err := e.app(passphrase("pw")).AddHost(t.Context(), h); err != nil {
		t.Fatal(err)
	}

	asked := 0
	a := e.app(func(o *app.Options) {
		o.Passphrase = func(string) (string, error) { asked++; return "pw", nil }
	})
	s := e.session(a, "home")
	if err := s.Unlock(); err != nil {
		t.Fatal(err)
	}
	for range 2 { // each round forces a new login
		e.fake.ExpireTokens()
		if _, err := s.Sites(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if asked != 1 {
		t.Errorf("passphrase asked %d times, want once per session", asked)
	}
}

func TestEnvSecret(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	h := e.host("ci")
	h.SecretStore = secret.KindEnv
	if err := a.AddHost(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if _, err := e.session(e.app(), "ci").Sites(t.Context()); err == nil {
		t.Error("want an error while the variable is unset")
	}
	t.Setenv(secret.EnvVar, omadatest.Secret)
	if _, err := e.session(e.app(), "ci").Sites(t.Context()); err != nil {
		t.Error(err)
	}
}

func TestEnvSecretOverridesStoredOne(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	e.add(a, "home")
	t.Setenv(secret.EnvVar, "wrong-from-env")
	_, err := e.session(e.app(func(o *app.Options) { o.NoTokenCache = true }), "home").Sites(t.Context())
	if !errors.Is(err, omada.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth from the overriding environment secret", err)
	}
}

func TestUseAndRemoveHost(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	e.add(a, "home")
	e.add(a, "office")
	if _, err := e.session(a, "home").Sites(t.Context()); err != nil { // caches a token
		t.Fatal(err)
	}

	if err := a.UseHost("office"); err != nil || a.CurrentHost() != "office" {
		t.Fatalf("UseHost: err = %v, current = %q", err, a.CurrentHost())
	}
	if err := a.UseHost("nope"); err == nil || !strings.Contains(err.Error(), "home, office") {
		t.Errorf("UseHost(nope): err = %v, want the known hosts listed", err)
	}

	if err := a.RemoveHost("office"); err != nil {
		t.Fatal(err)
	}
	if a.CurrentHost() != "home" {
		t.Errorf("current = %q, want home after removing the current host", a.CurrentHost())
	}
	if err := a.RemoveHost("home"); err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{"home", "home:token", "office"} {
		if _, err := keyring.Get("gomada", account); err == nil {
			t.Errorf("keychain entry %q survived the removal", account)
		}
	}
	if a.CurrentHost() != "" || len(e.app().Hosts()) != 0 {
		t.Error("want no hosts left")
	}
	if err := a.RemoveHost("home"); !errors.Is(err, app.ErrNoHosts) {
		t.Errorf("RemoveHost on empty config: err = %v", err)
	}
}

func TestConnectErrors(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	if _, err := a.Connect(""); !errors.Is(err, app.ErrNoHosts) {
		t.Errorf("no hosts: err = %v, want ErrNoHosts", err)
	}
	e.add(a, "home")
	if _, err := a.Connect("nope"); err == nil {
		t.Error("unknown host: want an error")
	}
	s := e.session(a, "")
	if s.Host() != "home" || s.URL() != e.fake.URL {
		t.Errorf("session = %s %s, want the current host", s.Host(), s.URL())
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"192.168.0.2:8043", "https://192.168.0.2:8043"},
		{" https://omada.example.net/ ", "https://omada.example.net"},
		{"https://host:8043/abc123/login", "https://host:8043"},
		{"http://localhost:8088", "http://localhost:8088"},
		{"omada.local", "https://omada.local"},
		{"", ""},
		{"ftp://host", ""},
		{"https://", ""},
		{"https://ho st", ""},
	}
	for _, tt := range tests {
		got, err := app.NormalizeURL(tt.in)
		if got != tt.want || (err == nil) != (tt.want != "") {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}
