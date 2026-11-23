package omada

import (
	"context"
	"net/http"
	"net/url"
)

// Firmware is what the controller knows about the firmware of a device.
type Firmware struct {
	Current      string `json:"curFwVer"`
	Latest       string `json:"lastFwVer"`
	ReleaseNotes string `json:"fwReleaseLog"`
}

// UpdateAvailable reports whether a newer firmware is known.
func (f Firmware) UpdateAvailable() bool {
	return f.Latest != "" && f.Latest != f.Current
}

// LatestFirmware returns the installed firmware of a device and the newest
// one the controller found the last time it checked.
func (c *Client) LatestFirmware(ctx context.Context, siteID, mac string) (Firmware, error) {
	var f Firmware
	err := c.call(ctx, http.MethodGet, devicePath(siteID, mac, "/latest-firmware-info"), nil, &f)
	return f, err
}

// StartUpgrade makes a device download and install the newest firmware. The
// device restarts when it is done.
func (c *Client) StartUpgrade(ctx context.Context, siteID, mac string) error {
	return c.call(ctx, http.MethodPost, devicePath(siteID, mac, "/start-online-upgrade"), nil, nil)
}

// UpgradeResult says how the last firmware upgrade of a device went.
type UpgradeResult struct {
	Finished bool `json:"finished"`
	Success  bool `json:"success"`
}

// UpgradeStatus reports whether the upgrade of a device has finished, and
// whether it worked.
func (c *Client) UpgradeStatus(ctx context.Context, siteID, mac string) (UpgradeResult, error) {
	var r UpgradeResult
	err := c.call(ctx, http.MethodGet, devicePath(siteID, mac, "/online-upgrade-result"), nil, &r)
	return r, err
}

func devicePath(siteID, mac, rest string) string {
	return sitePath(siteID, "/devices/"+url.PathEscape(mac)+rest)
}
