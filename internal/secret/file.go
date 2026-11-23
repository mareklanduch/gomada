package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/crypto/scrypt"

	"github.com/mareklanduch/gomada/internal/config"
)

// scrypt cost parameters for new files. They are stored in each file, so
// they can be raised later without breaking existing files.
const (
	scryptN = 1 << 15
	scryptR = 8
	scryptP = 1
	keyLen  = 32 // AES-256
)

// File stores each secret in its own file, encrypted with a key derived from
// a passphrase (scrypt, then AES-256-GCM). It is the fallback for systems
// without a keychain, such as servers reached over SSH.
type File struct {
	// Dir is the directory holding the secret files.
	Dir string
	// Passphrase returns the passphrase. A nil Passphrase makes Get and Set
	// fail with ErrPassphraseRequired.
	Passphrase func() (string, error)
}

// sealed is the on-disk format.
type sealed struct {
	Version int    `json:"version"`
	KDF     string `json:"kdf"`
	N       int    `json:"n"`
	R       int    `json:"r"`
	P       int    `json:"p"`
	Salt    []byte `json:"salt"`
	Nonce   []byte `json:"nonce"`
	Data    []byte `json:"data"`
}

func (f File) path(profile string) string {
	return filepath.Join(f.Dir, profile+".secret.json")
}

func (f File) passphrase() (string, error) {
	if f.Passphrase == nil {
		return "", ErrPassphraseRequired
	}
	p, err := f.Passphrase()
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", errors.New("the passphrase must not be empty")
	}
	return p, nil
}

func aead(passphrase string, s sealed) (cipher.AEAD, error) {
	key, err := scrypt.Key([]byte(passphrase), s.Salt, s.N, s.R, s.P, keyLen)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Get decrypts and returns the secret.
func (f File) Get(profile string) (string, error) {
	raw, err := os.ReadFile(f.path(profile))
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	var s sealed
	if err := json.Unmarshal(raw, &s); err != nil || s.KDF != "scrypt" {
		return "", fmt.Errorf("%s is not a gomada secret file", f.path(profile))
	}
	pass, err := f.passphrase()
	if err != nil {
		return "", err
	}
	gcm, err := aead(pass, s)
	if err != nil {
		return "", err
	}
	if len(s.Nonce) != gcm.NonceSize() {
		return "", ErrWrongPassphrase
	}
	// The profile name is authenticated, so a file cannot be moved to
	// another profile.
	plain, err := gcm.Open(nil, s.Nonce, s.Data, []byte(profile))
	if err != nil {
		return "", ErrWrongPassphrase
	}
	return string(plain), nil
}

// Set encrypts and stores the secret.
func (f File) Set(profile, secret string) error {
	pass, err := f.passphrase()
	if err != nil {
		return err
	}
	s := sealed{Version: 1, KDF: "scrypt", N: scryptN, R: scryptR, P: scryptP, Salt: make([]byte, 16)}
	if _, err := rand.Read(s.Salt); err != nil {
		return err
	}
	gcm, err := aead(pass, s)
	if err != nil {
		return err
	}
	s.Nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(s.Nonce); err != nil {
		return err
	}
	s.Data = gcm.Seal(nil, s.Nonce, []byte(secret), []byte(profile))
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := config.WriteFileAtomic(f.path(profile), raw); err != nil {
		return fmt.Errorf("write secret file: %w", err)
	}
	return nil
}

// Delete removes the secret file.
func (f File) Delete(profile string) error {
	err := os.Remove(f.path(profile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("delete secret file: %w", err)
	}
	return nil
}
