package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mareklanduch/gomada/internal/app"
	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/omada/omadatest"
)

// atEpoch pins the clock to the moment the fake controller's data is dated.
func atEpoch(o *app.Options) { o.Now = func() time.Time { return omadatest.Epoch } }

func connected(t *testing.T) (*env, *app.Session) {
	t.Helper()
	e := newEnv(t)
	a := e.app(atEpoch)
	e.add(a, "home")
	return e, e.session(a, "home")
}

func TestWifi(t *testing.T) {
	_, s := connected(t)
	ctx := t.Context()
	ssids, err := s.SSIDs(ctx, "site-1")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, ssid := range ssids {
		names = append(names, ssid.Name)
	}
	if got := strings.Join(names, ","); got != "Guests,Home-5G,IoT,Staff" {
		t.Errorf("order = %s, want sorted by name", got)
	}

	home, err := s.FindSSID(ctx, "site-1", "home-5g")
	if err != nil || home.ID != "ssid-1" {
		t.Fatalf("FindSSID by name: %+v, %v", home, err)
	}
	if byID, err := s.FindSSID(ctx, "site-1", "ssid-2"); err != nil || byID.Name != "IoT" {
		t.Errorf("FindSSID by ID: %+v, %v", byID, err)
	}
	if _, err := s.FindSSID(ctx, "site-1", "nope"); err == nil {
		t.Error("unknown network: want an error")
	}

	if pw, err := s.WifiPassword(ctx, "site-1", home); err != nil || pw != "correct horse battery" {
		t.Errorf("password = %q, %v", pw, err)
	}
	guests, _ := s.FindSSID(ctx, "site-1", "Guests")
	if pw, err := s.WifiPassword(ctx, "site-1", guests); err != nil || pw != "" {
		t.Errorf("open network: %q, %v", pw, err)
	}
}

func TestFindSSIDAmbiguous(t *testing.T) {
	e, s := connected(t)
	e.fake.SetSSIDs("site-1",
		omadatest.Record{"ssidId": "a", "name": "Twin", "security": 0},
		omadatest.Record{"ssidId": "b", "name": "twin", "security": 0})
	if _, err := s.FindSSID(t.Context(), "site-1", "TWIN"); err == nil || !strings.Contains(err.Error(), "network ID") {
		t.Errorf("err = %v, want a hint to use the ID", err)
	}
	if got, err := s.FindSSID(t.Context(), "site-1", "b"); err != nil || got.Name != "twin" {
		t.Errorf("by ID: %+v, %v", got, err)
	}
}

func TestWifiQRPayload(t *testing.T) {
	no := false
	tests := []struct {
		name     string
		ssid     omada.SSID
		password string
		want     string
		wantErr  bool
	}{
		{"personal", omada.SSID{Name: "Home-5G", Security: omada.SecurityPersonal}, "correct horse", "WIFI:T:WPA;S:Home-5G;P:correct horse;;", false},
		{"open", omada.SSID{Name: "Guests", Security: omada.SecurityNone}, "", "WIFI:T:nopass;S:Guests;;", false},
		{"hidden", omada.SSID{Name: "IoT", Security: omada.SecurityPersonal, Broadcast: &no}, "pw", "WIFI:T:WPA;S:IoT;P:pw;H:true;;", false},
		{"special characters", omada.SSID{Name: `Café; "5G"`, Security: omada.SecurityPersonal}, `a:b,c\d;`, `WIFI:T:WPA;S:Café\; \"5G\";P:a\:b\,c\\d\;;;`, false},
		{"password not revealed", omada.SSID{Name: "Home", Security: omada.SecurityPersonal}, "", "", true},
		{"enterprise", omada.SSID{Name: "Staff", Security: omada.SecurityEnterprise}, "", "", true},
		{"ppsk", omada.SSID{Name: "PPSK", Security: omada.SecurityPPSK}, "", "", true},
	}
	for _, tt := range tests {
		got, err := app.WifiQRPayload(tt.ssid, tt.password)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("%s: got %q, %v; want %q", tt.name, got, err, tt.want)
		}
		if err != nil && !errors.Is(err, app.ErrNoQR) {
			t.Errorf("%s: err = %v, want ErrNoQR", tt.name, err)
		}
	}
}

func TestAlerts(t *testing.T) {
	_, s := connected(t)
	alerts, err := s.Alerts(t.Context(), "site-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 3 || app.Unresolved(alerts) != 2 {
		t.Fatalf("got %d alerts, %d unresolved; want 3 and 2 (the 90-day-old one is outside the window)", len(alerts), app.Unresolved(alerts))
	}
	a, err := s.FindAlert(t.Context(), "site-1", "alert-2")
	if err != nil || a.Level != "Error" {
		t.Errorf("FindAlert: %+v, %v", a, err)
	}
	if _, err := s.FindAlert(t.Context(), "site-1", "alert-4"); err == nil {
		t.Error("an alert outside the window must not be found")
	}
}

func TestBlockedClientsAreSorted(t *testing.T) {
	e, s := connected(t)
	ctx := t.Context()
	for _, name := range []string{"printer", "Laptop"} {
		c, err := s.FindClient(ctx, "site-1", name)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.BlockClient(ctx, "site-1", c); err != nil {
			t.Fatal(err)
		}
	}
	blocked, err := s.BlockedClients(ctx, "site-1")
	if err != nil || len(blocked) != 2 || blocked[0].Label() != "Laptop" || blocked[1].Label() != "printer" {
		t.Fatalf("blocked = %+v, %v", blocked, err)
	}
	if err := s.UnblockClient(ctx, "site-1", blocked[0].MAC); err != nil || e.fake.Blocked(blocked[0].MAC) {
		t.Errorf("unblock: %v", err)
	}
}

func TestFirmwareCache(t *testing.T) {
	e := newEnv(t)
	now := omadatest.Epoch
	a := e.app(func(o *app.Options) { o.Now = func() time.Time { return now } })
	e.add(a, "home")
	s := e.session(a, "home")
	ctx := t.Context()
	devs, err := s.Devices(ctx, "site-1")
	if err != nil {
		t.Fatal(err)
	}
	const ap = "AA-00-00-00-00-03"
	firmwareCalls := func() int {
		n := 0
		for _, c := range e.fake.Calls() {
			if strings.HasSuffix(c, "/latest-firmware-info") {
				n++
			}
		}
		return n
	}

	if _, ok := s.CachedFirmware(ap); ok {
		t.Fatal("nothing must be cached before the first refresh")
	}
	if !s.RefreshFirmware(ctx, "site-1", devs) {
		t.Fatal("the first refresh must fetch")
	}
	if got := firmwareCalls(); got != 3 {
		t.Errorf("%d firmware requests, want 3: one per connected device, none for the offline one", got)
	}
	fw, ok := s.CachedFirmware(ap)
	if !ok || !fw.UpdateAvailable() || fw.Latest != "1.1.2" {
		t.Errorf("cached = %+v, %v", fw, ok)
	}
	if s.RefreshFirmware(ctx, "site-1", devs) || firmwareCalls() != 3 {
		t.Error("a second refresh right away must not ask again")
	}

	now = now.Add(11 * time.Minute)
	if _, ok := s.CachedFirmware(ap); ok {
		t.Error("the cache must expire")
	}
	if !s.RefreshFirmware(ctx, "site-1", devs) || firmwareCalls() != 6 {
		t.Errorf("after expiry: %d requests, want 6", firmwareCalls())
	}
}

func TestRefreshFirmwareSkipsDevicesThatFail(t *testing.T) {
	_, s := connected(t)
	devs := []omada.Device{
		{MAC: "FF-FF-FF-FF-FF-FF", Name: "ghost", Status: omada.StatusConnected},
		{MAC: "AA-00-00-00-00-03", Name: "AP office", Status: omada.StatusConnected},
	}
	if !s.RefreshFirmware(t.Context(), "site-1", devs) {
		t.Fatal("the device that answers must still be fetched")
	}
	if _, ok := s.CachedFirmware("AA-00-00-00-00-03"); !ok {
		t.Error("AP office is not cached")
	}
}

func TestUpgradeDevice(t *testing.T) {
	e, s := connected(t)
	e.fake.UpgradePolls = 2
	ctx := t.Context()
	ap, _ := s.FindDevice(ctx, "site-1", "AP office")
	gateway, _ := s.FindDevice(ctx, "site-1", "Core router")
	offline, _ := s.FindDevice(ctx, "site-1", "AP garage")

	if _, err := s.UpgradeDevice(ctx, "site-1", gateway); !errors.Is(err, app.ErrUpToDate) || !strings.Contains(err.Error(), "1.2.3") {
		t.Errorf("gateway: err = %v, want ErrUpToDate naming the version", err)
	}
	if _, err := s.UpgradeDevice(ctx, "site-1", offline); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("offline device: err = %v", err)
	}
	if e.fake.Called("POST /sites/site-1/devices/AA-00-00-00-00-01/start-online-upgrade") {
		t.Error("no upgrade must be requested for a device that is up to date")
	}

	fw, err := s.UpgradeDevice(ctx, "site-1", ap)
	if err != nil || fw.Current != "1.0.14" || fw.Latest != "1.1.2" {
		t.Fatalf("upgrade: %+v, %v", fw, err)
	}
	if _, ok := s.CachedFirmware(ap.MAC); ok {
		t.Error("the cached firmware information must be dropped when an upgrade starts")
	}
	ok, err := s.WaitForUpgrade(ctx, "site-1", ap, time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("wait: %v, %v", ok, err)
	}
	if got := e.fake.FirmwareVersion(ap.MAC); got != "1.1.2" {
		t.Errorf("installed = %q", got)
	}
}

func TestWaitForUpgradeFailureAndCancel(t *testing.T) {
	e, s := connected(t)
	e.fake.FailUpgrades = true
	ctx := t.Context()
	ap, _ := s.FindDevice(ctx, "site-1", "AP office")
	if _, err := s.UpgradeDevice(ctx, "site-1", ap); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.WaitForUpgrade(ctx, "site-1", ap, time.Millisecond); err != nil || ok {
		t.Errorf("failed upgrade: %v, %v; want false without an error", ok, err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.WaitForUpgrade(canceled, "site-1", ap, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled wait: err = %v", err)
	}
}

func TestWaitForUpgradeSurvivesAControllerThatStopsAnswering(t *testing.T) {
	e, s := connected(t)
	ap, _ := s.FindDevice(t.Context(), "site-1", "AP office")
	e.fake.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.WaitForUpgrade(ctx, "site-1", ap, time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the wait to keep trying until its deadline", err)
	}
}
