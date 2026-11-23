package secret

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// KeyringService is the service name of all gomada keychain entries.
const KeyringService = "gomada"

// Keyring stores secrets in the operating system keychain: Keychain on
// macOS, Credential Manager on Windows, the Secret Service on Linux.
type Keyring struct{}

// Get returns the secret, or ErrNotFound.
func (Keyring) Get(profile string) (string, error) {
	s, err := keyring.Get(KeyringService, profile)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read from the system keychain: %w", err)
	}
	return s, nil
}

// Set stores or replaces the secret.
func (Keyring) Set(profile, secret string) error {
	if err := keyring.Set(KeyringService, profile, secret); err != nil {
		return fmt.Errorf("write to the system keychain: %w", err)
	}
	return nil
}

// Delete removes the secret.
func (Keyring) Delete(profile string) error {
	err := keyring.Delete(KeyringService, profile)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete from the system keychain: %w", err)
	}
	return nil
}
