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

func TestTokenIsCachedBetweenRuns(t *testing.T) {
	e := newEnv(t)
	e.add(e.app(), "home")
	before := e.fake.Logins() // AddHost logged in once to check the credentials

	for range 3 { // three separate "commands"
		if _, err := e.session(e.app(), "home").Sites(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.fake.Logins() - before; got != 1 {
		t.Errorf("logins = %d, want 1 for three runs", got)
	}
}

func TestTokenCacheCanBeTurnedOff(t *testing.T) {
	e := newEnv(t)
	e.add(e.app(), "home")
	before := e.fake.Logins()
	off := func(o *app.Options) { o.NoTokenCache = true }
	for range 2 {
		if _, err := e.session(e.app(off), "home").Sites(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.fake.Logins() - before; got != 2 {
		t.Errorf("logins = %d, want 2", got)
	}
	if _, err := keyring.Get("gomada", "home:token"); err == nil {
		t.Error("no token must be stored with the cache off")
	}

	t.Run("by config setting", func(t *testing.T) {
		raw, err := os.ReadFile(e.path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(e.path, append(raw, []byte("no_token_cache: true\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		before := e.fake.Logins()
		for range 2 {
			if _, err := e.session(e.app(), "home").Sites(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if got := e.fake.Logins() - before; got != 2 {
			t.Errorf("logins = %d, want 2", got)
		}
	})
}

func TestRejectedCachedTokenIsReplaced(t *testing.T) {
	e := newEnv(t)
	e.add(e.app(), "home")
	if _, err := e.session(e.app(), "home").Sites(t.Context()); err != nil {
		t.Fatal(err)
	}
	e.fake.ExpireTokens()
	before := e.fake.Logins()
	if _, err := e.session(e.app(), "home").Sites(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.fake.Logins() - before; got != 1 {
		t.Errorf("logins = %d, want 1", got)
	}
}

// On a machine without a keychain the token goes to the runtime directory,
// and a valid token means the passphrase is not needed at all.
func TestHeadlessTokenCacheSparesThePassphrase(t *testing.T) {
	e := newEnv(t)
	keyring.MockInitWithError(errors.New("no secret service"))
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	h := e.host("home")
	h.SecretStore = secret.KindFile
	if err := e.app(passphrase("pw")).AddHost(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	if _, err := e.session(e.app(passphrase("pw")), "home").Sites(t.Context()); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(runtimeDir, "gomada", "home.token.json")
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatalf("token file: %v", err)
	}

	noPrompt := func(o *app.Options) {
		o.Passphrase = func(string) (string, error) {
			t.Error("the passphrase was asked for although a valid token is cached")
			return "", errors.New("unexpected prompt")
		}
	}
	if _, err := e.session(e.app(noPrompt), "home").Sites(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := e.app().RemoveHost("home"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Errorf("token file survived the removal: %v", err)
	}
}

func TestHeadlessWithoutRuntimeDirDoesNotCache(t *testing.T) {
	e := newEnv(t)
	keyring.MockInitWithError(errors.New("no secret service"))
	h := e.host("home")
	h.SecretStore = secret.KindFile
	if err := e.app(passphrase("pw")).AddHost(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	before := e.fake.Logins()
	for range 2 {
		if _, err := e.session(e.app(passphrase("pw")), "home").Sites(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.fake.Logins() - before; got != 2 {
		t.Errorf("logins = %d, want 2", got)
	}
}

func TestResolveSite(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	e.add(a, "home")
	s := e.session(a, "home")

	tests := []struct {
		want, id string
		wantErr  bool
	}{
		{"Workshop", "site-2", false},
		{"workshop", "site-2", false},
		{"site-1", "site-1", false},
		{"Garage", "", true},
	}
	for _, tt := range tests {
		site, err := s.ResolveSite(t.Context(), tt.want)
		if (err != nil) != tt.wantErr || site.ID != tt.id {
			t.Errorf("ResolveSite(%q) = %+v, %v", tt.want, site, err)
		}
	}

	_, err := s.ResolveSite(t.Context(), "")
	if !errors.Is(err, app.ErrSiteRequired) || !strings.Contains(err.Error(), "Default, Workshop") {
		t.Errorf("several sites, none chosen: err = %v, want ErrSiteRequired listing the sites", err)
	}

	if err := s.RememberSite(omada.Site{ID: "site-2", Name: "Workshop"}); err != nil {
		t.Fatal(err)
	}
	fresh := e.session(e.app(), "home")
	if site, err := fresh.ResolveSite(t.Context(), ""); err != nil || site.ID != "site-2" {
		t.Errorf("after RememberSite: %+v, %v; want the last used site", site, err)
	}
	if fresh.LastSite() != "Workshop" {
		t.Errorf("LastSite = %q", fresh.LastSite())
	}

	e.fake.SetSites(omadatest.Record{"siteId": "only", "name": "Only"})
	if site, err := s.ResolveSite(t.Context(), ""); err != nil || site.ID != "only" {
		t.Errorf("single site: %+v, %v", site, err)
	}
	e.fake.SetSites()
	if _, err := s.ResolveSite(t.Context(), ""); err == nil {
		t.Error("no sites: want an error")
	}
	e.fake.SetSites(omadatest.Record{"siteId": "a", "name": "Twin"}, omadatest.Record{"siteId": "b", "name": "twin"})
	if _, err := s.ResolveSite(t.Context(), "twin"); err == nil || !strings.Contains(err.Error(), "site ID") {
		t.Errorf("ambiguous name: err = %v", err)
	}
	if site, err := s.ResolveSite(t.Context(), "b"); err != nil || site.Name != "twin" {
		t.Errorf("by ID: %+v, %v", site, err)
	}
}

func TestDevicesAreSorted(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	e.add(a, "home")
	devs, err := e.session(a, "home").Devices(t.Context(), "site-1")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range devs {
		got = append(got, d.Name)
	}
	want := "Core router, Rack switch, AP garage, AP office"
	if strings.Join(got, ", ") != want {
		t.Errorf("order = %v, want %s", got, want)
	}
}

func TestFindDevice(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	e.add(a, "home")
	s := e.session(a, "home")
	e.fake.SetDevices("site-1",
		omadatest.Record{"mac": "AA-00-00-00-00-01", "name": "Twin", "type": "ap"},
		omadatest.Record{"mac": "AA-00-00-00-00-02", "name": "twin", "type": "ap"},
		omadatest.Record{"mac": "AA-00-00-00-00-03", "name": "Solo", "type": "switch"},
		omadatest.Record{"mac": "AA-00-00-00-00-04", "name": "aa0000000003", "type": "ap"},
	)

	for target, wantMAC := range map[string]string{
		"solo":              "AA-00-00-00-00-03",
		"AA-00-00-00-00-01": "AA-00-00-00-00-01",
		"aa:00:00:00:00:02": "AA-00-00-00-00-02",
		"aa0000000003":      "AA-00-00-00-00-03", // a MAC address wins over a name
	} {
		d, err := s.FindDevice(t.Context(), "site-1", target)
		if err != nil || d.MAC != wantMAC {
			t.Errorf("FindDevice(%q) = %s, %v; want %s", target, d.MAC, err, wantMAC)
		}
	}

	_, err := s.FindDevice(t.Context(), "site-1", "TWIN")
	if err == nil || !strings.Contains(err.Error(), "AA-00-00-00-00-01") || !strings.Contains(err.Error(), "AA-00-00-00-00-02") {
		t.Errorf("ambiguous: err = %v, want both candidates listed", err)
	}
	if _, err := s.FindDevice(t.Context(), "site-1", "nothing"); err == nil {
		t.Error("no match: want an error")
	}
	if _, err := s.FindDevice(t.Context(), "site-1", "FF-FF-FF-FF-FF-FF"); err == nil {
		t.Error("unknown MAC: want an error")
	}
}

func TestDeviceActions(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	e.add(a, "home")
	s := e.session(a, "home")
	ap, err := s.FindDevice(t.Context(), "site-1", "AP office")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RebootDevice(t.Context(), "site-1", ap); err != nil {
		t.Fatal(err)
	}
	if !e.fake.Called("POST /sites/site-1/devices/AA-00-00-00-00-03/reboot") {
		t.Errorf("calls = %v", e.fake.Calls())
	}
	if err := s.RenameDevice(t.Context(), "site-1", ap, "AP study"); err != nil {
		t.Fatal(err)
	}
	if got := e.fake.DeviceName("site-1", ap.MAC); got != "AP study" {
		t.Errorf("name = %q", got)
	}
}

func TestClientActions(t *testing.T) {
	e := newEnv(t)
	a := e.app()
	e.add(a, "home")
	s := e.session(a, "home")
	ctx := t.Context()

	clients, err := s.Clients(ctx, "site-1")
	if err != nil {
		t.Fatal(err)
	}
	if clients[0].Label() != "Laptop" || clients[1].Label() != "NAS" || clients[2].Label() != "printer" {
		t.Errorf("clients not sorted by label: %+v", clients)
	}

	laptop, err := s.FindClient(ctx, "site-1", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	printer, err := s.FindClient(ctx, "site-1", "PRINTER") // by host name
	if err != nil || printer.MAC != "CC-00-00-00-00-03" {
		t.Fatalf("FindClient by host name: %+v, %v", printer, err)
	}
	nas, err := s.FindClient(ctx, "site-1", "cc:00:00:00:00:02")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.RenameClient(ctx, "site-1", printer, "Printer"); err != nil || e.fake.ClientName("site-1", printer.MAC) != "Printer" {
		t.Errorf("rename: err = %v, name = %q", err, e.fake.ClientName("site-1", printer.MAC))
	}
	if err := s.BlockClient(ctx, "site-1", laptop); err != nil || !e.fake.Blocked(laptop.MAC) {
		t.Errorf("block: err = %v", err)
	}
	if err := s.UnblockClient(ctx, "site-1", "Laptop"); err == nil {
		t.Error("unblock by name: want an error asking for a MAC address")
	}
	if err := s.UnblockClient(ctx, "site-1", "cc:00:00:00:00:01"); err != nil || e.fake.Blocked(laptop.MAC) {
		t.Errorf("unblock: err = %v, blocked = %v", err, e.fake.Blocked(laptop.MAC))
	}
	if err := s.ReconnectClient(ctx, "site-1", laptop); err != nil {
		t.Errorf("reconnect: %v", err)
	}
	before := len(e.fake.Calls())
	if err := s.ReconnectClient(ctx, "site-1", nas); err == nil || len(e.fake.Calls()) != before {
		t.Errorf("reconnect wired: err = %v; want a refusal without a request", err)
	}
}

func TestFormatMAC(t *testing.T) {
	const want = "AA-BB-CC-DD-EE-FF"
	for _, in := range []string{"aa:bb:cc:dd:ee:ff", "AA-BB-CC-DD-EE-FF", "aabb.ccdd.eeff", " aabbccddeeff "} {
		if got, ok := app.FormatMAC(in); !ok || got != want {
			t.Errorf("FormatMAC(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "laptop", "aa:bb:cc:dd:ee", "aa:bb:cc:dd:ee:fg", "aabbccddeeff00"} {
		if got, ok := app.FormatMAC(in); ok {
			t.Errorf("FormatMAC(%q) = %q, want not a MAC", in, got)
		}
	}
}
