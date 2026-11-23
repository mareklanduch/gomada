package omada

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// Security modes of a Wi-Fi network in the Open API.
const (
	SecurityNone       = 0
	SecurityEnterprise = 2
	SecurityPersonal   = 3
	SecurityPPSK       = 4
	SecurityPPSKRadius = 5
)

// Bits of the band field of a Wi-Fi network.
const (
	Band2G = 1 << iota
	Band5G
	Band6G
)

// SSID is one Wi-Fi network of a site.
type SSID struct {
	// WLANID is the WLAN group the network belongs to. It is filled in by
	// SSIDs, not sent by the controller.
	WLANID    string `json:"-"`
	ID        string `json:"ssidId"`
	Name      string `json:"name"`
	Band      int    `json:"band"`
	Security  int    `json:"security"`
	Guest     bool   `json:"guestNetEnable"`
	Broadcast *bool  `json:"broadcast"`
	VLANOn    bool   `json:"vlanEnable"`
	VLANID    Num    `json:"vlanId"`
}

// Hidden reports whether the network does not announce its name.
func (s SSID) Hidden() bool { return s.Broadcast != nil && !*s.Broadcast }

// SecurityText describes the security mode in a word or two.
func (s SSID) SecurityText() string {
	switch s.Security {
	case SecurityNone:
		return "open"
	case SecurityEnterprise:
		return "WPA-Enterprise"
	case SecurityPersonal:
		return "WPA-Personal"
	case SecurityPPSK, SecurityPPSKRadius:
		return "PPSK"
	}
	return "unknown"
}

// BandText lists the bands the network is on: "2.4 + 5 GHz".
func (s SSID) BandText() string {
	var bands []string
	for _, b := range []struct {
		bit  int
		name string
	}{{Band2G, "2.4"}, {Band5G, "5"}, {Band6G, "6"}} {
		if s.Band&b.bit != 0 {
			bands = append(bands, b.name)
		}
	}
	if len(bands) == 0 {
		return ""
	}
	return strings.Join(bands, " + ") + " GHz"
}

// SSIDs lists the Wi-Fi networks of every WLAN group of a site.
func (c *Client) SSIDs(ctx context.Context, siteID string) ([]SSID, error) {
	// The group list is a plain array on current controllers; a paged
	// wrapper is accepted too.
	var raw json.RawMessage
	if err := c.call(ctx, http.MethodGet, sitePath(siteID, "/wireless-network/wlans"), nil, &raw); err != nil {
		return nil, err
	}
	type group struct {
		ID string `json:"wlanId"`
	}
	var groups []group
	if json.Unmarshal(raw, &groups) != nil {
		var paged struct {
			Data []group `json:"data"`
		}
		if err := json.Unmarshal(raw, &paged); err != nil {
			return nil, err
		}
		groups = paged.Data
	}
	all := []SSID{}
	for _, g := range groups {
		ssids, err := list[SSID](ctx, c, sitePath(siteID, "/wireless-network/wlans/"+url.PathEscape(g.ID)+"/ssids"))
		if err != nil {
			return nil, err
		}
		for i := range ssids {
			ssids[i].WLANID = g.ID
		}
		all = append(all, ssids...)
	}
	return all, nil
}

// SSIDPassword fetches the password of a WPA-Personal network. It is empty
// when the network has none or the controller does not reveal it.
func (c *Client) SSIDPassword(ctx context.Context, siteID string, s SSID) (string, error) {
	var detail struct {
		PSK struct {
			Key string `json:"securityKey"`
		} `json:"pskSetting"`
	}
	path := sitePath(siteID, "/wireless-network/wlans/"+url.PathEscape(s.WLANID)+"/ssids/"+url.PathEscape(s.ID))
	if err := c.call(ctx, http.MethodGet, path, nil, &detail); err != nil {
		return "", err
	}
	return detail.PSK.Key, nil
}
