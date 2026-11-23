// Package secret stores the Open API client secret of each host. The rest of
// the application talks to the Store interface and never knows which backend
// is in use.
package secret

import (
	"errors"
	"os"
)

// Store keeps one secret per profile (host name).
type Store interface {
	// Get returns the secret, or ErrNotFound.
	Get(profile string) (string, error)
	// Set stores or replaces the secret.
	Set(profile, secret string) error
	// Delete removes the secret. Deleting a missing secret is not an error.
	Delete(profile string) error
}

// Store kinds, as written to the secret_store field of the configuration.
const (
	KindKeyring = "keyring" // operating system keychain
	KindFile    = "file"    // passphrase-encrypted file
	KindEnv     = "env"     // GOMADA_CLIENT_SECRET, nothing stored
)

// EnvVar names the environment variable that supplies the client secret. When
// set it wins over every store.
const EnvVar = "GOMADA_CLIENT_SECRET"

// EnvPassphrase names the environment variable that supplies the passphrase
// of an encrypted secret file, for use without a terminal.
const EnvPassphrase = "GOMADA_PASSPHRASE"

// Errors returned by stores.
var (
	// ErrNotFound means no secret is stored for the profile.
	ErrNotFound = errors.New("no client secret stored for this host")
	// ErrPassphraseRequired means the encrypted file store needs a
	// passphrase and has no way to ask for one right now.
	ErrPassphraseRequired = errors.New("a passphrase is required to unlock the client secret")
	// ErrWrongPassphrase means the passphrase did not decrypt the file.
	ErrWrongPassphrase = errors.New("wrong passphrase, or the secret file is damaged")
)

// Env reads the secret from the environment and stores nothing.
type Env struct{}

// Get returns the value of GOMADA_CLIENT_SECRET.
func (Env) Get(string) (string, error) {
	if s := os.Getenv(EnvVar); s != "" {
		return s, nil
	}
	return "", errors.New("the " + EnvVar + " environment variable is not set")
}

// Set does nothing: the secret lives in the environment.
func (Env) Set(string, string) error { return nil }

// Delete does nothing.
func (Env) Delete(string) error { return nil }

// EnvOverride wraps a store so that GOMADA_CLIENT_SECRET, when set, is used
// instead of the stored secret.
type EnvOverride struct{ Store }

// Get returns the environment secret if set, else the stored one.
func (o EnvOverride) Get(profile string) (string, error) {
	if s := os.Getenv(EnvVar); s != "" {
		return s, nil
	}
	return o.Store.Get(profile)
}

// Memory keeps secrets in a map. It is meant for tests.
type Memory map[string]string

// Get returns the secret, or ErrNotFound.
func (m Memory) Get(profile string) (string, error) {
	s, ok := m[profile]
	if !ok {
		return "", ErrNotFound
	}
	return s, nil
}

// Set stores the secret.
func (m Memory) Set(profile, secret string) error { m[profile] = secret; return nil }

// Delete removes the secret.
func (m Memory) Delete(profile string) error { delete(m, profile); return nil }
