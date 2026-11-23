package omada_test

import (
	"errors"
	"testing"
	"time"

	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/omada/omadatest"
)

func epochClient(fake *omadatest.Controller) *omada.Client {
	return newClient(fake, func(o *omada.Options) { o.Now = func() time.Time { return omadatest.Epoch } })
}

func TestSSIDs(t *testing.T) {
	fake := omadatest.New(t)
	c := newClient(fake)
	ssids, err := c.SSIDs(t.Context(), "site-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ssids) != 4 {
		t.Fatalf("got %d networks, want 4", len(ssids))
	}
	home, iot, guests, staff := ssids[0], ssids[1], ssids[2], ssids[3]
	if home.Name != "Home-5G" || home.WLANID != "wlan-1" || home.BandText() != "5 GHz" || home.SecurityText() != "WPA-Personal" || home.Hidden() {
		t.Errorf("home = %+v", home)
	}
	if !iot.Hidden() || iot.BandText() != "2.4 GHz" || !iot.VLANOn || iot.VLANID != 30 {
		t.Errorf("iot = %+v", iot)
	}
	if !guests.Guest || guests.SecurityText() != "open" || guests.BandText() != "2.4 + 5 GHz" {
		t.Errorf("guests = %+v", guests)
	}
	if staff.SecurityText() != "WPA-Enterprise" || staff.BandText() != "2.4 + 5 + 6 GHz" {
		t.Errorf("staff = %+v", staff)
	}

	empty, err := c.SSIDs(t.Context(), "site-2")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("site without networks: %#v, %v", empty, err)
	}
	if got := (omada.SSID{Security: 9}).SecurityText(); got != "unknown" {
		t.Errorf("unknown security = %q", got)
	}
	if got := (omada.SSID{}).BandText(); got != "" {
		t.Errorf("no band = %q", got)
	}
}

func TestSSIDsAcrossSeveralGroupsAndPages(t *testing.T) {
	fake := omadatest.New(t)
	fake.MaxPageSize = 2
	c := newClient(fake)
	ssids, err := c.SSIDs(t.Context(), "site-1")
	if err != nil || len(ssids) != 4 {
		t.Fatalf("got %d networks, %v; want all 4 across two pages", len(ssids), err)
	}
}

func TestSSIDPassword(t *testing.T) {
	fake := omadatest.New(t)
	c := newClient(fake)
	ssids, err := c.SSIDs(t.Context(), "site-1")
	if err != nil {
		t.Fatal(err)
	}
	if pw, err := c.SSIDPassword(t.Context(), "site-1", ssids[0]); err != nil || pw != "correct horse battery" {
		t.Errorf("password = %q, %v", pw, err)
	}
	if pw, err := c.SSIDPassword(t.Context(), "site-1", ssids[2]); err != nil || pw != "" {
		t.Errorf("open network: password = %q, %v; want none", pw, err)
	}
	if _, err := c.SSIDPassword(t.Context(), "site-1", omada.SSID{WLANID: "wlan-1", ID: "nope"}); err == nil {
		t.Error("unknown network: want an error")
	}
}

func TestAlerts(t *testing.T) {
	fake := omadatest.New(t)
	c := epochClient(fake)
	alerts, err := c.Alerts(t.Context(), "site-1", omadatest.Epoch.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 3 {
		t.Fatalf("got %d alerts, want the 3 inside the time range: %+v", len(alerts), alerts)
	}
	// Newest first, with the resolved state filled in.
	if alerts[0].ID != "alert-1" || alerts[1].ID != "alert-2" || alerts[2].ID != "alert-3" {
		t.Errorf("order = %s, %s, %s", alerts[0].ID, alerts[1].ID, alerts[2].ID)
	}
	if alerts[0].Resolved || alerts[1].Resolved || !alerts[2].Resolved {
		t.Errorf("resolved flags = %v, %v, %v", alerts[0].Resolved, alerts[1].Resolved, alerts[2].Resolved)
	}
	if a := alerts[0]; a.Level != "Warning" || a.Module != "Device" || !a.Time().Equal(omadatest.Epoch.Add(-50*time.Minute)) {
		t.Errorf("alert = %+v at %v", a, a.Time())
	}

	recent, err := c.Alerts(t.Context(), "site-1", omadatest.Epoch.Add(-time.Hour))
	if err != nil || len(recent) != 1 {
		t.Errorf("last hour: %d alerts, %v; want 1", len(recent), err)
	}
	none, err := c.Alerts(t.Context(), "site-2", omadatest.Epoch.Add(-time.Hour))
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("site without alerts: %#v, %v", none, err)
	}
}

func TestAlertsAreCappedPerState(t *testing.T) {
	fake := omadatest.New(t)
	var many []omadatest.Record
	for i := range omada.AlertsPerState + 50 {
		many = append(many, omadatest.Record{
			"id": i, "level": "Warning", "content": "x", "resolved": false,
			"time": omadatest.Epoch.Add(-time.Duration(i) * time.Minute).UnixMilli(),
		})
	}
	fake.SetAlerts("site-1", many...)
	alerts, err := epochClient(fake).Alerts(t.Context(), "site-1", omadatest.Epoch.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != omada.AlertsPerState {
		t.Errorf("got %d alerts, want the cap of %d", len(alerts), omada.AlertsPerState)
	}
}

func TestBlockedClients(t *testing.T) {
	fake := omadatest.New(t)
	c := newClient(fake)
	ctx := t.Context()
	const mac = "CC-00-00-00-00-01"

	blocked, err := c.BlockedClients(ctx, "site-1")
	if err != nil || blocked == nil || len(blocked) != 0 {
		t.Fatalf("nothing blocked yet: %#v, %v", blocked, err)
	}
	if err := c.BlockClient(ctx, "site-1", mac); err != nil {
		t.Fatal(err)
	}
	blocked, err = c.BlockedClients(ctx, "site-1")
	if err != nil || len(blocked) != 1 {
		t.Fatalf("blocked = %+v, %v", blocked, err)
	}
	if b := blocked[0]; b.MAC != mac || b.Label() != "Laptop" || !b.Wireless || b.LastSeen().IsZero() {
		t.Errorf("blocked client = %+v", b)
	}
	// A blocked client is cut off, so it leaves the connected list.
	connected, err := c.Clients(ctx, "site-1")
	if err != nil || len(connected) != 2 {
		t.Errorf("connected = %d clients, %v; want 2", len(connected), err)
	}
	if err := c.UnblockClient(ctx, "site-1", mac); err != nil {
		t.Fatal(err)
	}
	if blocked, _ = c.BlockedClients(ctx, "site-1"); len(blocked) != 0 {
		t.Errorf("after unblocking: %+v", blocked)
	}
	if got := (omada.KnownClient{MAC: "AA"}).Label(); got != "AA" {
		t.Errorf("label without a name = %q", got)
	}
	if !(omada.KnownClient{}).LastSeen().IsZero() {
		t.Error("a client without lastSeen must have a zero time")
	}
}

func TestFirmware(t *testing.T) {
	fake := omadatest.New(t)
	fake.UpgradePolls = 2
	c := newClient(fake)
	ctx := t.Context()
	const ap, gateway, offline = "AA-00-00-00-00-03", "AA-00-00-00-00-01", "AA-00-00-00-00-04"

	fw, err := c.LatestFirmware(ctx, "site-1", ap)
	if err != nil {
		t.Fatal(err)
	}
	if fw.Current != "1.0.14" || fw.Latest != "1.1.2" || !fw.UpdateAvailable() || fw.ReleaseNotes == "" {
		t.Errorf("firmware = %+v", fw)
	}
	upToDate, err := c.LatestFirmware(ctx, "site-1", gateway)
	if err != nil || upToDate.UpdateAvailable() {
		t.Errorf("gateway firmware = %+v, %v; want no update", upToDate, err)
	}
	if unknown, _ := c.LatestFirmware(ctx, "site-1", offline); unknown.UpdateAvailable() {
		t.Errorf("no known latest version must not count as an update: %+v", unknown)
	}
	if _, err := c.LatestFirmware(ctx, "site-1", "FF-FF-FF-FF-FF-FF"); err == nil {
		t.Error("unknown device: want an error")
	}

	var apiErr *omada.APIError
	if err := c.StartUpgrade(ctx, "site-1", gateway); !errors.As(err, &apiErr) || apiErr.Code != -39026 {
		t.Errorf("upgrade without an update: err = %v, want code -39026", err)
	}
	if err := c.StartUpgrade(ctx, "site-1", offline); !errors.As(err, &apiErr) || apiErr.Code != -39020 {
		t.Errorf("upgrade of an offline device: err = %v, want code -39020", err)
	}

	if err := c.StartUpgrade(ctx, "site-1", ap); err != nil {
		t.Fatal(err)
	}
	devs, _ := c.Devices(ctx, "site-1")
	for _, d := range devs {
		if d.MAC == ap && d.StatusText() != "updating" {
			t.Errorf("status during the upgrade = %q, want updating", d.StatusText())
		}
	}
	for i := range 2 {
		if r, err := c.UpgradeStatus(ctx, "site-1", ap); err != nil || r.Finished {
			t.Fatalf("poll %d: %+v, %v; want unfinished", i, r, err)
		}
	}
	r, err := c.UpgradeStatus(ctx, "site-1", ap)
	if err != nil || !r.Finished || !r.Success {
		t.Fatalf("final poll: %+v, %v", r, err)
	}
	if got := fake.FirmwareVersion(ap); got != "1.1.2" {
		t.Errorf("installed version = %q", got)
	}
	if fw, _ := c.LatestFirmware(ctx, "site-1", ap); fw.UpdateAvailable() {
		t.Errorf("after the upgrade: %+v", fw)
	}
}

func TestFailedUpgrade(t *testing.T) {
	fake := omadatest.New(t)
	fake.FailUpgrades = true
	c := newClient(fake)
	const ap = "AA-00-00-00-00-03"
	if err := c.StartUpgrade(t.Context(), "site-1", ap); err != nil {
		t.Fatal(err)
	}
	r, err := c.UpgradeStatus(t.Context(), "site-1", ap)
	if err != nil || !r.Finished || r.Success {
		t.Errorf("result = %+v, %v; want finished without success", r, err)
	}
	if got := fake.FirmwareVersion(ap); got != "1.0.14" {
		t.Errorf("installed version = %q, want unchanged", got)
	}
}

func TestRebootingStatus(t *testing.T) {
	d := omada.Device{Status: omada.StatusConnected, DetailStatus: omada.DetailRebooting}
	if d.StatusText() != "rebooting" {
		t.Errorf("status = %q", d.StatusText())
	}
}
