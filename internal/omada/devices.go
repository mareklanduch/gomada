package omada

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Device types of the Open API.
const (
	TypeGateway = "gateway"
	TypeSwitch  = "switch"
	TypeAP      = "ap"
)

// Device status values of the Open API.
const (
	StatusDisconnected = 0
	StatusConnected    = 1
	StatusPending      = 2
	StatusHeartbeat    = 3
	StatusIsolated     = 4
)

// Detailed device states worth telling apart from the plain status.
const (
	DetailUpgrading = 12
	DetailRebooting = 13
)

// Device is a gateway, switch, or access point.
type Device struct {
	MAC    string `json:"mac"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Model  string `json:"model"`
	IP     string `json:"ip"`
	Status int    `json:"status"`
	// DetailStatus refines Status; see the Detail constants.
	DetailStatus int    `json:"detailStatus"`
	Uptime       Text   `json:"uptime"`
	CPU          Num    `json:"cpuUtil"`
	Mem          Num    `json:"memUtil"`
	Firmware     string `json:"firmwareVersion"`
	Serial       string `json:"sn"`
}

// Label is the device's name, or its MAC address when it has no name.
func (d Device) Label() string {
	if d.Name != "" {
		return d.Name
	}
	return d.MAC
}

// Online reports whether the device is connected to the controller.
func (d Device) Online() bool { return d.Status == StatusConnected }

// StatusText describes the device status in a word or two.
func (d Device) StatusText() string {
	switch d.DetailStatus {
	case DetailUpgrading:
		return "updating"
	case DetailRebooting:
		return "rebooting"
	}
	switch d.Status {
	case StatusDisconnected:
		return "offline"
	case StatusConnected:
		return "connected"
	case StatusPending:
		return "pending"
	case StatusHeartbeat:
		return "heartbeat missed"
	case StatusIsolated:
		return "isolated"
	}
	return fmt.Sprintf("status %d", d.Status)
}

// Devices lists the devices of a site.
func (c *Client) Devices(ctx context.Context, siteID string) ([]Device, error) {
	return list[Device](ctx, c, sitePath(siteID, "/devices"))
}

// RebootDevice restarts a device.
func (c *Client) RebootDevice(ctx context.Context, siteID, mac string) error {
	return c.call(ctx, http.MethodPost, devicePath(siteID, mac, "/reboot"), nil, nil)
}

// codeEasyManagedSwitch is the answer to a switch request for a switch that
// must be addressed through the Easy Managed path.
const codeEasyManagedSwitch = -39742

// RenameDevice sets the name of a device. The Open API has one endpoint per
// device type, so the device's type decides which one is used.
func (c *Client) RenameDevice(ctx context.Context, siteID string, d Device, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	mac := url.PathEscape(d.MAC)
	body := map[string]any{"name": name}
	switch d.Type {
	case TypeAP:
		return c.call(ctx, http.MethodPatch, sitePath(siteID, "/aps/"+mac+"/general-config"), body, nil)
	case TypeSwitch:
		err := c.call(ctx, http.MethodPatch, sitePath(siteID, "/switches/"+mac+"/general-config"), body, nil)
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == codeEasyManagedSwitch {
			return c.call(ctx, http.MethodPatch, sitePath(siteID, "/switches/es/"+mac+"/general-config"), body, nil)
		}
		return err
	case TypeGateway:
		// The gateway endpoint requires ledSetting, so the current value is
		// read and sent back unchanged.
		path := sitePath(siteID, "/gateways/"+mac+"/general-config")
		var current struct {
			LEDSetting *int `json:"ledSetting"`
		}
		if err := c.call(ctx, http.MethodGet, path, nil, &current); err != nil {
			return err
		}
		const ledUseSiteSettings = 2
		body["ledSetting"] = ledUseSiteSettings
		if current.LEDSetting != nil {
			body["ledSetting"] = *current.LEDSetting
		}
		return c.call(ctx, http.MethodPatch, path, body, nil)
	}
	return fmt.Errorf("renaming a device of type %q is not supported", d.Type)
}

// maxNameLen is the longest device or client name the controller accepts.
const maxNameLen = 128

// ValidateName checks a device or client name against the controller's
// rules, so a bad name is refused with a clear message before any request.
func ValidateName(name string) error {
	switch {
	case name == "":
		return errors.New("the name must not be empty")
	case utf8.RuneCountInString(name) > maxNameLen:
		return fmt.Errorf("the name must be at most %d characters", maxNameLen)
	case strings.HasSuffix(name, " "):
		return errors.New("the name must not end with a space")
	case strings.ContainsRune(" +-@=", rune(name[0])):
		return errors.New("the name must not start with a space, +, -, @, or =")
	}
	return nil
}

func sitePath(siteID, rest string) string {
	return "/sites/" + url.PathEscape(siteID) + rest
}
