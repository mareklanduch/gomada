// Package tokencache keeps Open API access tokens between runs, so a command
// can skip the login while its host's token is still valid.
//
// Tokens go to the operating system keychain where there is one, and
// otherwise to a file in the per-user runtime directory, which lives in
// memory and is wiped at logout. They are never written to the config file.
package tokencache

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"

	"github.com/mareklanduch/gomada/internal/config"
	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/secret"
)

// entry is what is stored for one profile. The client ID is kept with the
// token so a token is never used after the profile's credentials changed.
type entry struct {
	omada.Token
	ClientID string `json:"client_id"`
}

// Store keeps one entry per profile as an opaque string.
type Store interface {
	get(profile string) (string, bool)
	set(profile, value string) error
	del(profile string) error
}

// Keyring keeps tokens in the operating system keychain, next to the client
// secret of the same profile.
type Keyring struct{}

func account(profile string) string { return profile + ":token" }

func (Keyring) get(profile string) (string, bool) {
	v, err := keyring.Get(secret.KeyringService, account(profile))
	return v, err == nil
}

func (Keyring) set(profile, value string) error {
	return keyring.Set(secret.KeyringService, account(profile), value)
}

func (Keyring) del(profile string) error {
	err := keyring.Delete(secret.KeyringService, account(profile))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// File keeps tokens in owner-only files in Dir.
type File struct{ Dir string }

func (f File) path(profile string) string { return filepath.Join(f.Dir, profile+".token.json") }

func (f File) get(profile string) (string, bool) {
	raw, err := os.ReadFile(f.path(profile))
	return string(raw), err == nil
}

func (f File) set(profile, value string) error {
	return config.WriteFileAtomic(f.path(profile), []byte(value))
}

func (f File) del(profile string) error {
	err := os.Remove(f.path(profile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// RuntimeDir returns the gomada directory below the per-user runtime
// directory ($XDG_RUNTIME_DIR, e.g. /run/user/1000), and whether the system
// has one.
func RuntimeDir() (string, bool) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		return "", false
	}
	return filepath.Join(base, "gomada"), true
}

// For returns the token cache of one profile, as the API client wants it.
func For(store Store, profile, clientID string) omada.TokenCache {
	return &bound{store: store, profile: profile, clientID: clientID}
}

// Delete removes the cached token of a profile.
func Delete(store Store, profile string) error { return store.del(profile) }

type bound struct {
	store    Store
	profile  string
	clientID string
}

func (b *bound) Load() (omada.Token, bool) {
	raw, ok := b.store.get(b.profile)
	if !ok {
		return omada.Token{}, false
	}
	var e entry
	if err := json.Unmarshal([]byte(raw), &e); err != nil || e.ClientID != b.clientID || e.AccessToken == "" {
		return omada.Token{}, false
	}
	return e.Token, true
}

// Save stores the token. A cache that cannot be written is not worth failing
// a command for, so errors are dropped: the next run simply logs in again.
func (b *bound) Save(t omada.Token) {
	raw, err := json.Marshal(entry{Token: t, ClientID: b.clientID})
	if err != nil {
		return
	}
	_ = b.store.set(b.profile, string(raw))
}

func (b *bound) Clear() { _ = b.store.del(b.profile) }
