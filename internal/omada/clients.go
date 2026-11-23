package omada

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
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
