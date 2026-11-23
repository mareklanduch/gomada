package omada_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/omada/omadatest"
)

// clock is a settable time source.
type clock struct{ t time.Time }

func (c *clock) Now() time.Time          { return c.t }
func (c *clock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// memCache is an in-memory omada.TokenCache.
type memCache struct {
	token omada.Token
	set   bool
	saves int
}

func (m *memCache) Load() (omada.Token, bool) { return m.token, m.set }
func (m *memCache) Save(t omada.Token)        { m.token, m.set = t, true; m.saves++ }
func (m *memCache) Clear()                    { m.token, m.set = omada.Token{}, false }

func secret(s string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return s, nil }
}

func newClient(fake *omadatest.Controller, mutate ...func(*omada.Options)) *omada.Client {
	opts := omada.Options{
		BaseURL:      fake.URL,
		ControllerID: omadatest.ControllerID,
		ClientID:     omadatest.ClientID,
		Secret:       secret(omadatest.Secret),
		HTTPClient:   fake.Client(),
	}
	for _, m := range mutate {
		m(&opts)
	}
	return omada.New(opts)
}

func TestFetchInfo(t *testing.T) {
	fake := omadatest.New(t)
	info, err := omada.FetchInfo(t.Context(), fake.Client(), fake.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if info.ControllerID != omadatest.ControllerID || info.Version == "" {
		t.Errorf("info = %+v", info)
	}
}

func TestFetchInfoNotAController(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errorCode":0,"result":{}}`))
	}))
	defer srv.Close()
	if _, err := omada.FetchInfo(t.Context(), srv.Client(), srv.URL); err == nil {
		t.Fatal("want an error for a server without a controller ID")
	}
}

func TestLoginOnceAndReuseToken(t *testing.T) {
	fake := omadatest.New(t)
	c := newClient(fake)
	for range 3 {
		if _, err := c.Sites(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := fake.Logins(); got != 1 {
		t.Errorf("logins = %d, want 1", got)
	}
}

func TestLoginWrongSecret(t *testing.T) {
	fake := omadatest.New(t)
	c := newClient(fake, func(o *omada.Options) { o.Secret = secret("wrong") })
	err := c.Login(t.Context())
	if !errors.Is(err, omada.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	var apiErr *omada.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != -44106 {
		t.Errorf("err = %v, want APIError with code -44106", err)
	}
}

func TestLoginSecretError(t *testing.T) {
	fake := omadatest.New(t)
	want := errors.New("keychain locked")
	c := newClient(fake, func(o *omada.Options) {
		o.Secret = func(context.Context) (string, error) { return "", want }
	})
	if err := c.Login(t.Context()); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func TestLoginNoOpenAPI(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	c := omada.New(omada.Options{BaseURL: srv.URL, Secret: secret("x"), HTTPClient: srv.Client()})
	err := c.Login(t.Context())
	if err == nil || errors.Is(err, omada.ErrAuth) {
		t.Fatalf("err = %v, want a non-auth error about the missing Open API", err)
	}
}

func TestTokenRenewedBeforeExpiry(t *testing.T) {
	fake := omadatest.New(t)
	fake.TokenLifetime = 7200
	clk := &clock{t: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)}
	c := newClient(fake, func(o *omada.Options) { o.Now = clk.Now })

	mustSites(t, c)
	clk.Advance(7200*time.Second - 61*time.Second) // 61 s left: still good
	mustSites(t, c)
	if got := fake.Logins(); got != 1 {
		t.Fatalf("logins = %d, want 1 while the token has more than a minute left", got)
	}
	clk.Advance(2 * time.Second) // 59 s left: inside the renewal margin
	mustSites(t, c)
	if got := fake.Logins(); got != 2 {
		t.Fatalf("logins = %d, want 2 after entering the last minute", got)
	}
}

func TestRejectedTokenIsReplacedOnce(t *testing.T) {
	fake := omadatest.New(t)
	c := newClient(fake)
	mustSites(t, c)
	fake.ExpireTokens()
	mustSites(t, c) // rejected, logs in again, repeats the request
	if got := fake.Logins(); got != 2 {
		t.Errorf("logins = %d, want 2", got)
	}
}

func TestRejectedTwiceGivesUp(t *testing.T) {
	// A controller that accepts logins but refuses every token.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/authorize/token" {
			_, _ = w.Write([]byte(`{"errorCode":0,"result":{"accessToken":"t","expiresIn":7200}}`))
			return
		}
		_, _ = w.Write([]byte(`{"errorCode":-44113,"msg":"The access token is invalid."}`))
	}))
	defer srv.Close()
	c := omada.New(omada.Options{BaseURL: srv.URL, Secret: secret("x"), HTTPClient: srv.Client()})
	_, err := c.Sites(t.Context())
	var apiErr *omada.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != -44113 {
		t.Fatalf("err = %v, want the -44113 answer after one retry", err)
	}
}

func TestCacheValidTokenSkipsLogin(t *testing.T) {
	fake := omadatest.New(t)
	cache := &memCache{}

	first := newClient(fake, func(o *omada.Options) { o.Cache = cache })
	mustSites(t, first)
	if cache.saves != 1 {
		t.Fatalf("saves = %d, want the new token saved once", cache.saves)
	}

	// A second run with no way to get the secret must work from the cache.
	second := newClient(fake, func(o *omada.Options) {
		o.Cache = cache
		o.Secret = func(context.Context) (string, error) {
			return "", errors.New("the secret must not be needed")
		}
	})
	mustSites(t, second)
	if got := fake.Logins(); got != 1 {
		t.Errorf("logins = %d, want 1", got)
	}
}

func TestCacheExpiredTokenIsReplaced(t *testing.T) {
	fake := omadatest.New(t)
	clk := &clock{t: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)}
	cache := &memCache{set: true, token: omada.Token{AccessToken: "old", ExpiresAt: clk.t.Add(30 * time.Second)}}
	c := newClient(fake, func(o *omada.Options) { o.Cache, o.Now = cache, clk.Now })
	mustSites(t, c)
	if fake.Logins() != 1 || cache.token.AccessToken == "old" {
		t.Errorf("logins = %d, cached = %q; want a fresh token", fake.Logins(), cache.token.AccessToken)
	}
}

func TestCacheRejectedTokenIsClearedAndReplaced(t *testing.T) {
	fake := omadatest.New(t)
	cache := &memCache{set: true, token: omada.Token{AccessToken: "revoked", ExpiresAt: time.Now().Add(time.Hour)}}
	c := newClient(fake, func(o *omada.Options) { o.Cache = cache })
	mustSites(t, c)
	if fake.Logins() != 1 {
		t.Errorf("logins = %d, want 1", fake.Logins())
	}
	if cache.token.AccessToken == "revoked" || !cache.set {
		t.Errorf("cache = %+v, want the replacement token", cache.token)
	}
}

func TestRejectedTokenPicksUpNewerCachedToken(t *testing.T) {
	// Two clients share a cache, like a TUI and a CLI command. The
	// controller revokes older tokens on each login.
	fake := omadatest.New(t)
	fake.RevokeOnLogin = true
	cache := &memCache{}
	tui := newClient(fake, func(o *omada.Options) { o.Cache = cache })
	mustSites(t, tui)

	cache.Clear() // the CLI command finds no token and logs in
	cli := newClient(fake, func(o *omada.Options) { o.Cache = cache })
	mustSites(t, cli)

	mustSites(t, tui) // its token is now revoked; it must adopt the CLI's
	if got := fake.Logins(); got != 2 {
		t.Errorf("logins = %d, want 2: the TUI should reuse the newer cached token", got)
	}
}

func TestSitesPaging(t *testing.T) {
	fake := omadatest.New(t)
	fake.MaxPageSize = 2
	var sites []omadatest.Record
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		sites = append(sites, omadatest.Record{"siteId": id, "name": "Site " + id})
	}
	fake.SetSites(sites...)
	got, err := newClient(fake).Sites(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[4].ID != "e" {
		t.Errorf("sites = %+v, want all five across three pages", got)
	}
}

func TestEmptyListIsNotNil(t *testing.T) {
	fake := omadatest.New(t)
	got, err := newClient(fake).Clients(t.Context(), "site-2")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("clients = %#v, want an empty non-nil slice", got)
	}
}

func TestDevicesDecodeQuirks(t *testing.T) {
	fake := omadatest.New(t)
	devs, err := newClient(fake).Devices(t.Context(), "site-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 4 {
		t.Fatalf("got %d devices, want 4", len(devs))
	}
	gw, ap, off := devs[0], devs[2], devs[3]
	if gw.Uptime != "41day(s) 6h 2m 1s" || gw.CPU != 8 || !gw.Online() {
		t.Errorf("gateway = %+v", gw)
	}
	if ap.Uptime != "1044000" || ap.CPU != 5 || ap.Mem != 52 {
		t.Errorf("ap = %+v, want numeric uptime as text and string load as numbers", ap)
	}
	if off.Online() || off.StatusText() != "offline" || off.Uptime != "" {
		t.Errorf("offline ap = %+v", off)
	}
}

func TestClientsDecode(t *testing.T) {
	fake := omadatest.New(t)
	clients, err := newClient(fake).Clients(t.Context(), "site-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 3 {
		t.Fatalf("got %d clients, want 3", len(clients))
	}
	if c := clients[1]; c.Uptime != 3564000 || c.Port != 4 || c.Wireless || c.Label() != "NAS" {
		t.Errorf("wired client = %+v", c)
	}
	if got := clients[2].Label(); got != "printer" {
		t.Errorf("label = %q, want the host name when there is no name", got)
	}
}

func TestUnknownSite(t *testing.T) {
	fake := omadatest.New(t)
	_, err := newClient(fake).Devices(t.Context(), "nope")
	var apiErr *omada.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != -33004 {
		t.Fatalf("err = %v, want APIError -33004", err)
	}
	if errors.Is(err, omada.ErrAuth) || errors.Is(err, omada.ErrUnreachable) {
		t.Errorf("err = %v must not be classed as auth or unreachable", err)
	}
}

func TestRebootDevice(t *testing.T) {
	fake := omadatest.New(t)
	c := newClient(fake)
	if err := c.RebootDevice(t.Context(), "site-1", "AA-00-00-00-00-03"); err != nil {
		t.Fatal(err)
	}
	if !fake.Called("POST /sites/site-1/devices/AA-00-00-00-00-03/reboot") {
		t.Errorf("calls = %v", fake.Calls())
	}
	if err := c.RebootDevice(t.Context(), "site-1", "FF-FF-FF-FF-FF-FF"); err == nil {
		t.Error("want an error for an unknown device")
	}
}

func TestRenameDevice(t *testing.T) {
	fake := omadatest.New(t)
	fake.SetDevices("site-1",
		omadatest.Record{"mac": "01", "name": "gw", "type": "gateway", "ledSetting": 1},
		omadatest.Record{"mac": "02", "name": "sw", "type": "switch"},
		omadatest.Record{"mac": "03", "name": "es", "type": "switch", "easyManaged": true},
		omadatest.Record{"mac": "04", "name": "ap", "type": "ap"},
		omadatest.Record{"mac": "05", "name": "odd", "type": "olt"},
	)
	c := newClient(fake)
	devs, err := c.Devices(t.Context(), "site-1")
	if err != nil {
		t.Fatal(err)
	}
	wantCall := map[string]string{
		"01": "PATCH /sites/site-1/gateways/01/general-config",
		"02": "PATCH /sites/site-1/switches/02/general-config",
		"03": "PATCH /sites/site-1/switches/es/03/general-config",
		"04": "PATCH /sites/site-1/aps/04/general-config",
	}
	for _, d := range devs[:4] {
		newName := "new " + d.Name
		if err := c.RenameDevice(t.Context(), "site-1", d, newName); err != nil {
			t.Errorf("rename %s: %v", d.Type, err)
			continue
		}
		if got := fake.DeviceName("site-1", d.MAC); got != newName {
			t.Errorf("%s name = %q, want %q", d.MAC, got, newName)
		}
		if !fake.Called(wantCall[d.MAC]) {
			t.Errorf("%s: missing call %q in %v", d.MAC, wantCall[d.MAC], fake.Calls())
		}
	}
	if err := c.RenameDevice(t.Context(), "site-1", devs[4], "x"); err == nil {
		t.Error("want an error for an unsupported device type")
	}
	if err := c.RenameDevice(t.Context(), "site-1", devs[3], "-bad"); err == nil {
		t.Error("want an error for an invalid name")
	}
}

func TestClientActions(t *testing.T) {
	fake := omadatest.New(t)
	c := newClient(fake)
	ctx := t.Context()
	const site, wifi, wired = "site-1", "CC-00-00-00-00-01", "CC-00-00-00-00-02"

	if err := c.RenameClient(ctx, site, wifi, "Work laptop"); err != nil {
		t.Fatal(err)
	}
	if got := fake.ClientName(site, wifi); got != "Work laptop" {
		t.Errorf("name = %q", got)
	}
	if err := c.BlockClient(ctx, site, wifi); err != nil || !fake.Blocked(wifi) {
		t.Errorf("block: err = %v, blocked = %v", err, fake.Blocked(wifi))
	}
	if err := c.UnblockClient(ctx, site, wifi); err != nil || fake.Blocked(wifi) {
		t.Errorf("unblock: err = %v, blocked = %v", err, fake.Blocked(wifi))
	}
	if err := c.ReconnectClient(ctx, site, wifi); err != nil {
		t.Errorf("reconnect: %v", err)
	}
	var apiErr *omada.APIError
	if err := c.ReconnectClient(ctx, site, wired); !errors.As(err, &apiErr) || apiErr.Code != -41025 {
		t.Errorf("reconnect wired: err = %v, want code -41025", err)
	}
	if err := c.RenameClient(ctx, site, wifi, ""); err == nil {
		t.Error("want an error for an empty name")
	}
}

func TestUnreachable(t *testing.T) {
	fake := omadatest.New(t)
	c := newClient(fake)
	fake.Close()
	_, err := c.Sites(t.Context())
	if !errors.Is(err, omada.ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
}

func TestContextCanceled(t *testing.T) {
	fake := omadatest.New(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := newClient(fake).Sites(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, omada.ErrUnreachable) {
		t.Error("a canceled request must not be reported as an unreachable controller")
	}
}

func TestNonJSONAnswers(t *testing.T) {
	status := http.StatusBadGateway
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("<html>proxy</html>"))
	}))
	defer srv.Close()

	_, err := omada.FetchInfo(t.Context(), srv.Client(), srv.URL)
	var apiErr *omada.APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != http.StatusBadGateway {
		t.Errorf("err = %v, want APIError with HTTP 502", err)
	}

	status = http.StatusOK
	if _, err := omada.FetchInfo(t.Context(), srv.Client(), srv.URL); err == nil {
		t.Error("want an error for a 200 answer that is not JSON")
	}
}

func TestHTTP401CountsAsRejectedToken(t *testing.T) {
	logins, calls := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi/authorize/token" {
			logins++
			_, _ = w.Write([]byte(`{"errorCode":0,"result":{"accessToken":"t","expiresIn":"7200"}}`))
			return
		}
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"errorCode":0,"result":{"totalRows":0,"data":[]}}`))
	}))
	defer srv.Close()
	c := omada.New(omada.Options{BaseURL: srv.URL, Secret: secret("x"), HTTPClient: srv.Client()})
	mustSites(t, c)
	if logins != 2 {
		t.Errorf("logins = %d, want 2", logins)
	}
}

func mustSites(t *testing.T, c *omada.Client) {
	t.Helper()
	if _, err := c.Sites(t.Context()); err != nil {
		t.Fatal(err)
	}
}
