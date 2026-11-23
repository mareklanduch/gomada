package tokencache_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/tokencache"
)

var sample = omada.Token{AccessToken: "AT-1", ExpiresAt: time.Date(2026, 11, 23, 14, 0, 0, 0, time.UTC)}

func contract(t *testing.T, store tokencache.Store) {
	t.Helper()
	home := tokencache.For(store, "home", "client-a")
	if _, ok := home.Load(); ok {
		t.Fatal("Load on an empty cache must find nothing")
	}
	home.Clear() // clearing an empty cache must not fail

	home.Save(sample)
	got, ok := home.Load()
	if !ok || got.AccessToken != sample.AccessToken || !got.ExpiresAt.Equal(sample.ExpiresAt) {
		t.Fatalf("Load = %+v, %v; want %+v", got, ok, sample)
	}
	if _, ok := tokencache.For(store, "office", "client-a").Load(); ok {
		t.Error("a token must not leak to another profile")
	}
	if _, ok := tokencache.For(store, "home", "client-b").Load(); ok {
		t.Error("a token must not be used after the client ID changed")
	}

	home.Clear()
	if _, ok := home.Load(); ok {
		t.Error("Load after Clear must find nothing")
	}

	home.Save(sample)
	if err := tokencache.Delete(store, "home"); err != nil {
		t.Fatal(err)
	}
	if _, ok := home.Load(); ok {
		t.Error("Load after Delete must find nothing")
	}
	if err := tokencache.Delete(store, "home"); err != nil {
		t.Errorf("Delete of a missing token: %v", err)
	}
}

func TestKeyring(t *testing.T) {
	keyring.MockInit()
	contract(t, tokencache.Keyring{})
}

func TestFile(t *testing.T) {
	contract(t, tokencache.File{Dir: filepath.Join(t.TempDir(), "gomada")})
}

func TestFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no owner-only permission bits on Windows")
	}
	dir := filepath.Join(t.TempDir(), "gomada")
	tokencache.For(tokencache.File{Dir: dir}, "home", "c").Save(sample)
	info, err := os.Stat(filepath.Join(dir, "home.token.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600", got)
	}
}

func TestFileCorruptEntryIsIgnored(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "home.token.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := tokencache.For(tokencache.File{Dir: dir}, "home", "c").Load(); ok {
		t.Error("a corrupt entry must be ignored")
	}
}

func TestRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	if _, ok := tokencache.RuntimeDir(); ok {
		t.Error("want no runtime dir when XDG_RUNTIME_DIR is unset")
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if dir, ok := tokencache.RuntimeDir(); !ok || dir != filepath.Join("/run/user/1000", "gomada") {
		t.Errorf("RuntimeDir = %q, %v", dir, ok)
	}
}
