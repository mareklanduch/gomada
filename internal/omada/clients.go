package omada

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// NetClient is a phone, laptop, or other machine connected to a site.
type NetClient struct {
	MAC         string `json:"mac"`
	Name        string `json:"name"`
	HostName    string `json:"hostName"`
	IP          string `json:"ip"`
	Vendor      string `json:"vendor"`
	Wireless    bool   `json:"wireless"`
	SSID        string `json:"ssid"`
	NetworkName string `json:"networkName"`
	APName      string `json:"apName"`
	SwitchName  string `json:"switchName"`
	GatewayName string `json:"gatewayName"`
	Port        Num    `json:"port"`
	RSSI        Num    `json:"rssi"`
	Uptime      Num    `json:"uptime"` // seconds
	TrafficDown Num    `json:"trafficDown"`
	TrafficUp   Num    `json:"trafficUp"`
}

// Label is the client's name, falling back to its host name and then its
// MAC address.
func (n NetClient) Label() string {
	switch {
	case n.Name != "":
		return n.Name
	case n.HostName != "":
		return n.HostName
	}
	return n.MAC
}

// Clients lists the clients currently connected to a site.
func (c *Client) Clients(ctx context.Context, siteID string) ([]NetClient, error) {
	return list[NetClient](ctx, c, sitePath(siteID, "/clients"))
}

// KnownClient is a client the controller remembers, connected or not.
type KnownClient struct {
	MAC      string `json:"mac"`
	Name     string `json:"name"`
	Wireless bool   `json:"wireless"`
	Guest    bool   `json:"guest"`
	Blocked  bool   `json:"block"`
	// LastSeenMillis is when the client was last connected, in
	// milliseconds since 1970.
	LastSeenMillis Num `json:"lastSeen"`
}

// Label is the client's name, or its MAC address when it has no name.
func (k KnownClient) Label() string {
	if k.Name != "" {
		return k.Name
	}
	return k.MAC
}

// LastSeen returns when the client was last connected, or the zero time
// when the controller does not say.
func (k KnownClient) LastSeen() time.Time {
	if k.LastSeenMillis <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(k.LastSeenMillis))
}

// BlockedClients lists the clients of a site that are barred from the
// network. The controller has no list of just those, so its list of known
// clients is read and filtered.
func (c *Client) BlockedClients(ctx context.Context, siteID string) ([]KnownClient, error) {
	known, err := list[KnownClient](ctx, c, sitePath(siteID, "/insight/clients"))
	if err != nil {
		return nil, err
	}
	blocked := []KnownClient{}
	for _, k := range known {
		if k.Blocked {
			blocked = append(blocked, k)
		}
	}
	return blocked, nil
}

// RenameClient sets the friendly name of a client.
func (c *Client) RenameClient(ctx context.Context, siteID, mac, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	return c.call(ctx, http.MethodPatch, clientPath(siteID, mac, "/name"), map[string]string{"name": name}, nil)
}

// BlockClient bars a client from the network.
func (c *Client) BlockClient(ctx context.Context, siteID, mac string) error {
	return c.call(ctx, http.MethodPost, clientPath(siteID, mac, "/block"), nil, nil)
}

// UnblockClient lets a blocked client back on the network.
func (c *Client) UnblockClient(ctx context.Context, siteID, mac string) error {
	return c.call(ctx, http.MethodPost, clientPath(siteID, mac, "/unblock"), nil, nil)
}

// ReconnectClient forces a wireless client to re-associate.
func (c *Client) ReconnectClient(ctx context.Context, siteID, mac string) error {
	return c.call(ctx, http.MethodPost, clientPath(siteID, mac, "/reconnect"), nil, nil)
}

func clientPath(siteID, mac, rest string) string {
	return sitePath(siteID, "/clients/"+url.PathEscape(mac)+rest)
}

// ConnectedTo describes where the client is attached: the access point and
// Wi-Fi network for wireless clients, the switch or gateway and port for
// wired ones.
func (n NetClient) ConnectedTo() string {
	var device, detail string
	switch {
	case n.Wireless:
		device, detail = n.APName, n.SSID
	case n.SwitchName != "":
		device = n.SwitchName
	default:
		device = n.GatewayName
	}
	if !n.Wireless && n.Port > 0 {
		detail = fmt.Sprintf("port %d", int(n.Port))
	}
	switch {
	case device != "" && detail != "":
		return device + " · " + detail
	case device != "":
		return device
	}
	return detail
}
