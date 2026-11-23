package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mareklanduch/gomada/internal/omada"
)

// Wi-Fi networks

// SSIDs lists the Wi-Fi networks of a site, sorted by name.
func (s *Session) SSIDs(ctx context.Context, siteID string) ([]omada.SSID, error) {
	ssids, err := s.client.SSIDs(ctx, siteID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(ssids, func(i, j int) bool {
		return strings.ToLower(ssids[i].Name) < strings.ToLower(ssids[j].Name)
	})
	return ssids, nil
}

// FindSSID finds one Wi-Fi network by name, ignoring case, or by ID.
func (s *Session) FindSSID(ctx context.Context, siteID, target string) (omada.SSID, error) {
	ssids, err := s.SSIDs(ctx, siteID)
	if err != nil {
		return omada.SSID{}, err
	}
	var found []omada.SSID
	for _, ssid := range ssids {
		if ssid.ID == target {
			return ssid, nil
		}
		if strings.EqualFold(ssid.Name, target) {
			found = append(found, ssid)
		}
	}
	switch len(found) {
	case 0:
		return omada.SSID{}, fmt.Errorf("no Wi-Fi network matches %q", target)
	case 1:
		return found[0], nil
	}
	return omada.SSID{}, fmt.Errorf("several Wi-Fi networks are named %q; use the network ID from 'gomada wifi list -o json'", target)
}

// WifiPassword fetches the password of a Wi-Fi network. Networks without a
// shared password have none, and that is not an error.
func (s *Session) WifiPassword(ctx context.Context, siteID string, ssid omada.SSID) (string, error) {
	if ssid.Security != omada.SecurityPersonal {
		return "", nil
	}
	return s.client.SSIDPassword(ctx, siteID, ssid)
}

// ErrNoQR means a Wi-Fi network cannot be joined by QR code.
var ErrNoQR = errors.New("no QR code for this network")

// WifiQRPayload builds the text of the QR code that phones join a network
// from. It fails with ErrNoQR for networks that have no shared password to
// put in a code, and when the controller did not reveal the password.
func WifiQRPayload(ssid omada.SSID, password string) (string, error) {
	esc := strings.NewReplacer(`\`, `\\`, `;`, `\;`, `,`, `\,`, `:`, `\:`, `"`, `\"`).Replace
	hidden := ""
	if ssid.Hidden() {
		hidden = "H:true;"
	}
	switch ssid.Security {
	case omada.SecurityNone:
		return "WIFI:T:nopass;S:" + esc(ssid.Name) + ";" + hidden + ";", nil
	case omada.SecurityPersonal:
		if password == "" {
			return "", fmt.Errorf("%w: the controller did not reveal the password; the Open API application may not be allowed to read it", ErrNoQR)
		}
		return "WIFI:T:WPA;S:" + esc(ssid.Name) + ";P:" + esc(password) + ";" + hidden + ";", nil
	}
	return "", fmt.Errorf("%w: %s networks use a login per person, not one shared password", ErrNoQR, ssid.SecurityText())
}

// Alerts

// AlertWindow is how far back alerts are listed.
const AlertWindow = 30 * 24 * time.Hour

// Alerts lists the alerts of a site from the last 30 days, newest first.
func (s *Session) Alerts(ctx context.Context, siteID string) ([]omada.Alert, error) {
	return s.client.Alerts(ctx, siteID, s.app.now().Add(-AlertWindow))
}

// FindAlert finds one alert by its ID.
func (s *Session) FindAlert(ctx context.Context, siteID, id string) (omada.Alert, error) {
	alerts, err := s.Alerts(ctx, siteID)
	if err != nil {
		return omada.Alert{}, err
	}
	for _, a := range alerts {
		if string(a.ID) == id {
			return a, nil
		}
	}
	return omada.Alert{}, fmt.Errorf("no alert with ID %q in the last 30 days", id)
}

// Unresolved counts the alerts that are not resolved.
func Unresolved(alerts []omada.Alert) int {
	n := 0
	for _, a := range alerts {
		if !a.Resolved {
			n++
		}
	}
	return n
}

// Blocked clients

// BlockedClients lists the clients barred from a site, sorted by name.
func (s *Session) BlockedClients(ctx context.Context, siteID string) ([]omada.KnownClient, error) {
	blocked, err := s.client.BlockedClients(ctx, siteID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(blocked, func(i, j int) bool {
		return strings.ToLower(blocked[i].Label()) < strings.ToLower(blocked[j].Label())
	})
	return blocked, nil
}

// Firmware

// firmwareTTL is how long firmware information is reused before the
// controller is asked again. Updates appear rarely, and asking costs one
// request per device.
const firmwareTTL = 10 * time.Minute

type firmwareEntry struct {
	info    omada.Firmware
	fetched time.Time
}

// Firmware returns the installed and the newest known firmware of a device,
// asking the controller.
func (s *Session) Firmware(ctx context.Context, siteID string, d omada.Device) (omada.Firmware, error) {
	info, err := s.client.LatestFirmware(ctx, siteID, d.MAC)
	if err != nil {
		return omada.Firmware{}, err
	}
	s.fwMu.Lock()
	s.firmware[d.MAC] = firmwareEntry{info: info, fetched: s.app.now()}
	s.fwMu.Unlock()
	return info, nil
}

// CachedFirmware returns the firmware information of a device if it was
// fetched recently, without asking the controller.
func (s *Session) CachedFirmware(mac string) (omada.Firmware, bool) {
	s.fwMu.Lock()
	defer s.fwMu.Unlock()
	e, ok := s.firmware[mac]
	if !ok || s.app.now().Sub(e.fetched) > firmwareTTL {
		return omada.Firmware{}, false
	}
	return e.info, true
}

// RefreshFirmware fetches the firmware information of every connected device
// that has none cached. It reports whether anything was fetched. A device
// that cannot be asked is skipped; the others still get their answer.
func (s *Session) RefreshFirmware(ctx context.Context, siteID string, devs []omada.Device) bool {
	fetched := false
	for _, d := range devs {
		if !d.Online() {
			continue
		}
		if _, ok := s.CachedFirmware(d.MAC); ok {
			continue
		}
		if _, err := s.Firmware(ctx, siteID, d); err != nil {
			if ctx.Err() != nil {
				return fetched
			}
			continue
		}
		fetched = true
	}
	return fetched
}

// ErrUpToDate means a device has no newer firmware to install.
var ErrUpToDate = errors.New("already up to date")

// UpgradeDevice starts installing the newest firmware on a device and
// returns what is being installed. The device restarts when it is done. It
// fails with ErrUpToDate when there is nothing newer.
func (s *Session) UpgradeDevice(ctx context.Context, siteID string, d omada.Device) (omada.Firmware, error) {
	if !d.Online() {
		return omada.Firmware{}, fmt.Errorf("%s is %s; only a connected device can be updated", d.Label(), d.StatusText())
	}
	info, err := s.Firmware(ctx, siteID, d)
	if err != nil {
		return omada.Firmware{}, err
	}
	if !info.UpdateAvailable() {
		return info, fmt.Errorf("%s is %w (firmware %s)", d.Label(), ErrUpToDate, info.Current)
	}
	if err := s.client.StartUpgrade(ctx, siteID, d.MAC); err != nil {
		return info, err
	}
	s.fwMu.Lock()
	delete(s.firmware, d.MAC) // what is cached is about to be out of date
	s.fwMu.Unlock()
	return info, nil
}

// WaitForUpgrade asks every interval whether the upgrade of a device has
// finished, until it has or ctx ends. It reports whether the upgrade worked.
func (s *Session) WaitForUpgrade(ctx context.Context, siteID string, d omada.Device, interval time.Duration) (bool, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
		r, err := s.client.UpgradeStatus(ctx, siteID, d.MAC)
		if err != nil {
			// The device restarts during an upgrade and the controller may
			// not answer for it for a moment; only give up when asked to.
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			continue
		}
		if r.Finished {
			return r.Success, nil
		}
	}
}
