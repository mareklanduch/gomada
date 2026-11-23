package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mareklanduch/gomada/internal/omada/omadatest"
)

// newEnvAtSite returns a fixture with the host added and the site chosen.
func newEnvAtSite(t *testing.T) *env {
	t.Helper()
	e := newEnvWithHost(t)
	e.mustRun("site", "use", "Default")
	return e
}

func TestWifiList(t *testing.T) {
	e := newEnvAtSite(t)
	e.golden("wifi-list", e.mustRun("wifi", "list"))
	e.golden("wifi-list-json", e.mustRun("wifi", "ls", "-o", "json"))
	if out := e.mustRun("wifi", "list", "-o", "json"); strings.Contains(out, "correct horse") || strings.Contains(out, "password") {
		t.Errorf("the list must never contain a password:\n%s", out)
	}
}

func TestWifiShow(t *testing.T) {
	e := newEnvAtSite(t)
	hidden := e.mustRun("wifi", "show", "home-5g")
	e.golden("wifi-show", hidden)
	if strings.Contains(hidden, "correct horse") {
		t.Errorf("the password was printed without --password:\n%s", hidden)
	}
	if e.fake.Called("GET /sites/site-1/wireless-network/wlans/wlan-1/ssids/ssid-1") {
		t.Error("the password must not even be fetched without --password")
	}

	wantContains(t, e.mustRun("wifi", "show", "Home-5G", "--password"), "Password:", "correct horse battery")
	wantContains(t, e.mustRun("wifi", "show", "Home-5G", "--password", "-o", "json"), `"password": "correct horse battery"`)
	if out := e.mustRun("wifi", "show", "Home-5G", "-o", "json"); strings.Contains(out, "password") {
		t.Errorf("JSON without --password must have no password field:\n%s", out)
	}
	open := e.mustRun("wifi", "show", "Guests", "--password")
	if strings.Contains(open, "Password") {
		t.Errorf("an open network has no password line:\n%s", open)
	}
	wantContains(t, e.wantFail(1, "wifi", "show", "nope"), `no Wi-Fi network matches "nope"`)
	wantContains(t, e.wantFail(2, "wifi", "show"), "NETWORK")
}

func TestWifiQR(t *testing.T) {
	e := newEnvAtSite(t)
	stdout, stderr, code := e.run("wifi", "qr", "Home-5G")
	if code != 0 {
		t.Fatalf("exit code %d: %s", code, stderr)
	}
	wantContains(t, stderr, "Scan to join Home-5G.")
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) < 12 {
		t.Fatalf("the QR code has only %d lines:\n%s", len(lines), stdout)
	}
	width := len([]rune(lines[0]))
	for i, line := range lines {
		if len([]rune(line)) != width {
			t.Errorf("line %d is %d wide, want %d", i, len([]rune(line)), width)
		}
		if strings.Trim(line, "█▀▄ ") != "" {
			t.Errorf("line %d has characters that are not part of a QR code: %q", i, line)
		}
	}
	if strings.Contains(stdout, "\x1b") {
		t.Error("no color codes are wanted when the output is not a terminal")
	}
	if strings.Contains(stdout+stderr, "correct horse") {
		t.Error("the password was printed as text")
	}

	e.interactive = true
	colored := e.mustRun("wifi", "qr", "Guests") // an open network works too
	if !strings.HasPrefix(colored, "\x1b[97;40m") || !strings.HasSuffix(colored, "\x1b[0m\n") {
		t.Errorf("at a terminal each line must be light on black: %q", colored[:20])
	}
	t.Setenv("NO_COLOR", "1")
	if strings.Contains(e.mustRun("wifi", "qr", "Guests"), "\x1b") {
		t.Error("NO_COLOR must turn the color codes off")
	}
	e.interactive = false

	wantContains(t, e.wantFail(1, "wifi", "qr", "Staff"), "no QR code", "WPA-Enterprise")
}

func TestAlertList(t *testing.T) {
	e := newEnvAtSite(t)
	e.golden("alert-list", e.mustRun("alert", "list"))
	e.golden("alert-list-json", e.mustRun("alert", "list", "-o", "json"))

	unresolved := e.mustRun("alert", "list", "--unresolved")
	if strings.Contains(unresolved, "alert-3") || !strings.Contains(unresolved, "alert-1") || !strings.Contains(unresolved, "alert-2") {
		t.Errorf("--unresolved:\n%s", unresolved)
	}
	if out := e.mustRun("alert", "list"); strings.Contains(out, "alert-4") {
		t.Errorf("an alert older than 30 days was listed:\n%s", out)
	}
	wantContains(t, e.mustRun("--site", "Workshop", "alert", "list", "-o", "json"), "[]")
}

func TestAlertShow(t *testing.T) {
	e := newEnvAtSite(t)
	e.golden("alert-show", e.mustRun("alert", "show", "alert-2"))
	wantContains(t, e.mustRun("alert", "show", "alert-3", "-o", "json"), `"resolved": true`, `"time": "2026-11-20T19:00:00Z"`)
	wantContains(t, e.wantFail(1, "alert", "show", "nope"), `no alert with ID "nope"`)
}

func TestClientListBlocked(t *testing.T) {
	e := newEnvAtSite(t)
	_, stderr, code := e.run("client", "list", "--blocked")
	if code != 0 || !strings.Contains(stderr, "No client is blocked.") {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	wantContains(t, e.mustRun("client", "list", "--blocked", "-o", "json"), "[]")

	e.mustRun("client", "block", "Laptop", "--yes")
	e.mustRun("client", "block", "printer", "--yes")
	e.golden("client-list-blocked", e.mustRun("client", "list", "--blocked"))
	e.golden("client-list-blocked-json", e.mustRun("client", "list", "--blocked", "-o", "json"))
	filtered := e.mustRun("client", "list", "--blocked", "--filter", "lap")
	if !strings.Contains(filtered, "Laptop") || strings.Contains(filtered, "printer") {
		t.Errorf("--filter:\n%s", filtered)
	}
	// A blocked client is cut off, so it is gone from the connected list.
	if out := e.mustRun("client", "list"); strings.Contains(out, "Laptop") {
		t.Errorf("a blocked client is still listed as connected:\n%s", out)
	}

	e.mustRun("client", "unblock", "CC-00-00-00-00-01")
	if out := e.mustRun("client", "list", "--blocked"); strings.Contains(out, "Laptop") {
		t.Errorf("after unblocking:\n%s", out)
	}
	wantContains(t, e.wantFail(2, "client", "list", "--blocked", "--wired"), "cannot be combined")
}

func TestDeviceFirmware(t *testing.T) {
	e := newEnvAtSite(t)
	e.golden("device-firmware", e.mustRun("device", "firmware", "AP office"))
	e.golden("device-firmware-json", e.mustRun("device", "firmware", "AP office", "-o", "json"))
	wantContains(t, e.mustRun("device", "firmware", "Core router"), "Installed:", "1.2.3", "up to date")
	wantContains(t, e.mustRun("device", "firmware", "AP garage"), "unknown")
}

func TestDeviceListUpdates(t *testing.T) {
	e := newEnvAtSite(t)
	e.golden("device-list-updates", e.mustRun("device", "list", "--updates"))

	var updates []map[string]any
	if err := json.Unmarshal([]byte(e.mustRun("device", "list", "--updates", "-o", "json")), &updates); err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0]["name"] != "AP office" || updates[0]["latest"] != "1.1.2" || updates[0]["update_available"] != true {
		t.Errorf("updates = %v", updates)
	}

	e.fake.SetFirmware("AA-00-00-00-00-03", "1.1.2", "1.1.2", "")
	_, stderr, code := e.run("device", "list", "--updates")
	if code != 0 || !strings.Contains(stderr, "Every connected device is up to date.") {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
}

func TestDeviceUpgrade(t *testing.T) {
	e := newEnvAtSite(t)
	e.fake.UpgradePolls = 3
	const call = "POST /sites/site-1/devices/AA-00-00-00-00-03/start-online-upgrade"

	wantContains(t, e.wantFail(2, "device", "upgrade", "AP office"), "--yes")
	e.interactive = true
	e.stdin = "n\n"
	stderr := e.wantFail(1, "device", "upgrade", "AP office")
	wantContains(t, stderr, "Update AP office (ap) from 1.0.14 to 1.1.2?", "restarts", "canceled")
	if e.fake.Called(call) {
		t.Fatal("the update started without confirmation")
	}

	e.stdin = "y\n"
	stdout, stderr, code := e.run("device", "upgrade", "AP office")
	if code != 0 {
		t.Fatalf("exit code %d: %s", code, stderr)
	}
	wantContains(t, stderr, "Updating AP office to 1.1.2", "do not unplug")
	wantContains(t, stdout, "AP office now runs firmware 1.1.2.")
	if got := e.fake.FirmwareVersion("AA-00-00-00-00-03"); got != "1.1.2" {
		t.Errorf("installed = %q", got)
	}
	e.interactive = false

	// Nothing left to install: says so, asks nothing, and succeeds.
	wantContains(t, e.mustRun("device", "upgrade", "AP office"), "already up to date (firmware 1.1.2)")
	wantContains(t, e.mustRun("device", "upgrade", "Core router"), "already up to date")
}

func TestDeviceUpgradeNoWaitAndFailure(t *testing.T) {
	e := newEnvAtSite(t)
	out := e.mustRun("device", "upgrade", "AP office", "--yes", "--no-wait")
	wantContains(t, out, "Updating AP office to 1.1.2")
	if e.fake.Called("GET /sites/site-1/devices/AA-00-00-00-00-03/online-upgrade-result") {
		t.Error("--no-wait must not poll for the result")
	}
	wantContains(t, e.mustRun("device", "list"), "updating")

	e = newEnvAtSite(t)
	e.fake.FailUpgrades = true
	wantContains(t, e.wantFail(1, "device", "upgrade", "AP office", "--yes"), "did not succeed")
}

func TestNewCommandsInHelp(t *testing.T) {
	e := newEnv(t)
	wantContains(t, e.mustRun("--help"), "wifi", "alert")
	wantContains(t, e.mustRun("device", "--help"), "firmware", "upgrade")
	wantContains(t, e.mustRun("wifi"), "list", "show", "qr")
	for _, args := range [][]string{{"wifi", "frob"}, {"alert", "show"}, {"device", "upgrade"}, {"wifi", "qr"}} {
		e.wantFail(2, args...)
	}
	_ = omadatest.Epoch
}
