package secret_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/mareklanduch/gomada/internal/secret"
)

func pass(p string) func() (string, error) {
	return func() (string, error) { return p, nil }
}

// storeContract checks the behavior every Store must have.
func storeContract(t *testing.T, s secret.Store) {
	t.Helper()
	if _, err := s.Get("home"); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("Get on empty store: err = %v, want ErrNotFound", err)
	}
	if err := s.Delete("home"); err != nil {
		t.Fatalf("Delete of a missing secret: %v", err)
	}
	if err := s.Set("home", "s3cr3t"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("office", "other"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("home", "replaced"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get("home"); err != nil || got != "replaced" {
		t.Fatalf("Get = %q, %v; want the replaced secret", got, err)
	}
	if got, err := s.Get("office"); err != nil || got != "other" {
		t.Fatalf("Get(office) = %q, %v", got, err)
	}
	if err := s.Delete("home"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("home"); !errors.Is(err, secret.ErrNotFound) {
		t.Fatalf("Get after Delete: err = %v, want ErrNotFound", err)
	}
	if got, _ := s.Get("office"); got != "other" {
		t.Fatal("deleting one profile must not touch another")
	}
}

func TestMemoryContract(t *testing.T) { storeContract(t, secret.Memory{}) }

func TestKeyringContract(t *testing.T) {
	keyring.MockInit()
	storeContract(t, secret.Keyring{})
}

func TestKeyringUnavailable(t *testing.T) {
	keyring.MockInitWithError(errors.New("no keyring daemon"))
	t.Cleanup(keyring.MockInit)
	k := secret.Keyring{}
	if err := k.Set("home", "x"); err == nil {
		t.Error("Set: want an error")
	}
	if _, err := k.Get("home"); err == nil || errors.Is(err, secret.ErrNotFound) {
		t.Errorf("Get: err = %v, want a keychain error, not ErrNotFound", err)
	}
}

func TestFileContract(t *testing.T) {
	storeContract(t, secret.File{Dir: t.TempDir(), Passphrase: pass("correct horse")})
}

func TestFileIsEncryptedAndPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	f := secret.File{Dir: dir, Passphrase: pass("pw")}
	if err := f.Set("home", "very-secret-value"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "home.secret.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("very-secret-value")) || bytes.Contains(raw, []byte("pw")) {
		t.Errorf("the file holds the secret or passphrase in the clear: %s", raw)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("mode = %o, want 600", got)
		}
	}
}

func TestFileWrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	if err := (secret.File{Dir: dir, Passphrase: pass("right")}).Set("home", "s"); err != nil {
		t.Fatal(err)
	}
	_, err := secret.File{Dir: dir, Passphrase: pass("wrong")}.Get("home")
	if !errors.Is(err, secret.ErrWrongPassphrase) {
		t.Fatalf("err = %v, want ErrWrongPassphrase", err)
	}
}

func TestFileTampered(t *testing.T) {
	dir := t.TempDir()
	f := secret.File{Dir: dir, Passphrase: pass("pw")}
	if err := f.Set("home", "s"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "home.secret.json")
	raw, _ := os.ReadFile(path)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}

	t.Run("flipped ciphertext", func(t *testing.T) {
		m2 := map[string]any{}
		for k, v := range m {
			m2[k] = v
		}
		m2["data"] = "AAAA" + m["data"].(string)[4:]
		out, _ := json.Marshal(m2)
		if err := os.WriteFile(path, out, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Get("home"); !errors.Is(err, secret.ErrWrongPassphrase) {
			t.Errorf("err = %v, want ErrWrongPassphrase", err)
		}
	})
	t.Run("moved to another profile", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, "office.secret.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Get("office"); !errors.Is(err, secret.ErrWrongPassphrase) {
			t.Errorf("err = %v, want ErrWrongPassphrase", err)
		}
	})
	t.Run("not a secret file", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Get("home"); err == nil || errors.Is(err, secret.ErrNotFound) {
			t.Errorf("err = %v, want a format error", err)
		}
	})
}

func TestFilePassphraseHandling(t *testing.T) {
	dir := t.TempDir()
	if err := (secret.File{Dir: dir}).Set("home", "s"); !errors.Is(err, secret.ErrPassphraseRequired) {
		t.Errorf("Set without a passphrase source: err = %v", err)
	}
	if err := (secret.File{Dir: dir, Passphrase: pass("")}).Set("home", "s"); err == nil {
		t.Error("Set with an empty passphrase: want an error")
	}
	if err := (secret.File{Dir: dir, Passphrase: pass("pw")}).Set("home", "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := (secret.File{Dir: dir}).Get("home"); !errors.Is(err, secret.ErrPassphraseRequired) {
		t.Errorf("Get without a passphrase source: err = %v", err)
	}
	asked := false
	missing := secret.File{Dir: dir, Passphrase: func() (string, error) { asked = true; return "pw", nil }}
	if _, err := missing.Get("nope"); !errors.Is(err, secret.ErrNotFound) || asked {
		t.Errorf("Get of a missing secret: err = %v, asked for passphrase = %v", err, asked)
	}
}

func TestEnv(t *testing.T) {
	t.Setenv(secret.EnvVar, "")
	if _, err := (secret.Env{}).Get("home"); err == nil {
		t.Error("want an error when the variable is unset")
	}
	t.Setenv(secret.EnvVar, "from-env")
	if got, err := (secret.Env{}).Get("home"); err != nil || got != "from-env" {
		t.Errorf("Get = %q, %v", got, err)
	}
}

func TestEnvOverride(t *testing.T) {
	s := secret.EnvOverride{Store: secret.Memory{"home": "stored"}}
	t.Setenv(secret.EnvVar, "")
	if got, _ := s.Get("home"); got != "stored" {
		t.Errorf("without the variable: got %q", got)
	}
	t.Setenv(secret.EnvVar, "from-env")
	if got, _ := s.Get("home"); got != "from-env" {
		t.Errorf("with the variable: got %q", got)
	}
}
