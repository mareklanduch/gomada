// Package omadatest provides a fake Omada controller for tests. It speaks
// enough of the Open API over HTTP for the client, the CLI, and the TUI to be
// tested without a real controller: tokens, paging, error codes, and the
// supported actions.
package omadatest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Credentials every fake controller accepts.
const (
	ControllerID = "fake-omadac-id"
	ClientID     = "fake-client-id"
	Secret       = "fake-client-secret"
)

// Record is a JSON object as the controller would send it. Using raw maps
// lets tests reproduce quirks such as numbers sent as strings.
type Record = map[string]any

// Controller is a fake Omada controller.
type Controller struct {
	*httptest.Server

	mu sync.Mutex
	// TokenLifetime is the expiresIn value of issued tokens, in seconds.
	TokenLifetime int
	// MaxPageSize caps the page size, to force paging in tests. Zero means
	// no cap.
	MaxPageSize int
	// RevokeOnLogin makes each login invalidate all earlier tokens.
	RevokeOnLogin bool

	sites    []Record
	devices  map[string][]Record // by site ID
	clients  map[string][]Record // by site ID
	tokens   map[string]bool
	logins   int
	nextTok  int
	calls    []string
	blocked  map[string]bool
	wlans    map[string][]Record // WLAN groups by site ID
	ssids    map[string][]Record // Wi-Fi networks by WLAN group ID
	alerts   map[string][]Record // by site ID; "resolved" is internal
	firmware map[string]Record   // latest firmware info by device MAC
	upgrades map[string]*upgrade // running or finished upgrades by device MAC

	// UpgradePolls is how many times the result of an upgrade is reported
	// as unfinished before it completes.
	UpgradePolls int
	// FailUpgrades makes upgrades finish without success.
	FailUpgrades bool
}

// upgrade is the state of one firmware upgrade.
type upgrade struct {
	polls    int
	finished bool
	success  bool
}

// Epoch is the moment the sample data of the fake controller is dated
// around. Tests that show times set their clock to it.
var Epoch = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

// New starts a fake controller over plain HTTP with a small sample network.
// It is shut down when the test ends.
func New(t testing.TB) *Controller {
	t.Helper()
	c := newController()
	c.Server = httptest.NewServer(c)
	t.Cleanup(c.Close)
	return c
}

// NewTLS is like New but serves HTTPS with a certificate that is not trusted
// by the system roots, like a typical controller.
func NewTLS(t testing.TB) *Controller {
	t.Helper()
	c := newController()
	c.Server = httptest.NewTLSServer(c)
	t.Cleanup(c.Close)
	return c
}

func newController() *Controller {
	return &Controller{
		TokenLifetime: 7200,
		tokens:        map[string]bool{},
		blocked:       map[string]bool{},
		upgrades:      map[string]*upgrade{},
		wlans: map[string][]Record{
			"site-1": {{"wlanId": "wlan-1", "name": "Default", "primary": true}},
			"site-2": {},
		},
		ssids: map[string][]Record{
			"wlan-1": {
				{"ssidId": "ssid-1", "name": "Home-5G", "band": 2, "security": 3, "guestNetEnable": false, "broadcast": true, "pskSetting": Record{"securityKey": "correct horse battery", "versionPsk": 4}},
				{"ssidId": "ssid-2", "name": "IoT", "band": 1, "security": 3, "guestNetEnable": false, "broadcast": false, "vlanEnable": true, "vlanId": 30, "pskSetting": Record{"securityKey": "things;and:stuff", "versionPsk": 2}},
				{"ssidId": "ssid-3", "name": "Guests", "band": 3, "security": 0, "guestNetEnable": true, "broadcast": true},
				{"ssidId": "ssid-4", "name": "Staff", "band": 7, "security": 2, "guestNetEnable": false, "broadcast": true},
			},
		},
		alerts: map[string][]Record{
			"site-1": {
				{"id": "alert-1", "key": "DEVICE_DISCONNECTED", "module": "Device", "level": "Warning", "content": "[ap:AP garage:AA-00-00-00-00-04] was disconnected.", "time": Epoch.Add(-50 * time.Minute).UnixMilli(), "resolved": false},
				{"id": "alert-2", "key": "POE_OVERLOAD", "module": "Device", "level": "Error", "content": "[switch:Rack switch:AA-00-00-00-00-02] port 7 was shut down because the PoE budget was exceeded. Reduce the load on the switch or raise the budget, then turn the port back on.", "time": Epoch.Add(-14 * time.Hour).UnixMilli(), "resolved": false},
				{"id": "alert-3", "key": "CLIENT_BLOCKED", "module": "Client", "level": "Information", "content": "[client:Unknown phone:7A-11-22-33-44-55] was blocked.", "time": Epoch.Add(-65 * time.Hour).UnixMilli(), "resolved": true},
				{"id": "alert-4", "key": "DEVICE_DISCONNECTED", "module": "Device", "level": "Warning", "content": "An alert older than the time range anyone asks for.", "time": Epoch.Add(-90 * 24 * time.Hour).UnixMilli(), "resolved": false},
			},
			"site-2": {},
		},
		firmware: map[string]Record{
			"AA-00-00-00-00-01": {"curFwVer": "1.2.3", "lastFwVer": "1.2.3", "fwReleaseLog": ""},
			"AA-00-00-00-00-02": {"curFwVer": "3.0.1", "lastFwVer": "3.0.1", "fwReleaseLog": ""},
			"AA-00-00-00-00-03": {"curFwVer": "1.0.14", "lastFwVer": "1.1.2", "fwReleaseLog": "1. Improved roaming between access points.\n2. Fixed a restart under heavy load."},
			"AA-00-00-00-00-04": {"curFwVer": "5.1.0", "lastFwVer": "", "fwReleaseLog": ""},
		},
		sites: []Record{
			{"siteId": "site-1", "name": "Default"},
			{"siteId": "site-2", "name": "Workshop"},
		},
		devices: map[string][]Record{
			"site-1": {
				{"mac": "AA-00-00-00-00-01", "name": "Core router", "type": "gateway", "model": "ER707-M2", "ip": "192.168.0.1", "status": 1, "uptime": "41day(s) 6h 2m 1s", "cpuUtil": 8, "memUtil": 31, "firmwareVersion": "1.2.3", "sn": "SN001", "ledSetting": 1},
				{"mac": "AA-00-00-00-00-02", "name": "Rack switch", "type": "switch", "model": "SG3428XMP", "ip": "192.168.0.2", "status": 1, "uptime": "41day(s) 6h 1m 0s", "cpuUtil": 12, "memUtil": 44, "firmwareVersion": "3.0.1", "sn": "SN002"},
				{"mac": "AA-00-00-00-00-03", "name": "AP office", "type": "ap", "model": "EAP670", "ip": "192.168.0.21", "status": 1, "uptime": 1044000, "cpuUtil": "5", "memUtil": "52", "firmwareVersion": "1.0.14", "sn": "SN003"},
				{"mac": "AA-00-00-00-00-04", "name": "AP garage", "type": "ap", "model": "EAP225", "ip": "192.168.0.23", "status": 0, "firmwareVersion": "5.1.0", "sn": "SN004"},
			},
			"site-2": {
				{"mac": "BB-00-00-00-00-01", "name": "Workshop AP", "type": "ap", "model": "EAP610", "ip": "10.0.0.2", "status": 1, "uptime": 3600},
			},
		},
		clients: map[string][]Record{
			"site-1": {
				{"mac": "CC-00-00-00-00-01", "name": "Laptop", "hostName": "laptop", "ip": "192.168.0.104", "wireless": true, "ssid": "Home-5G", "apName": "AP office", "rssi": -52, "uptime": 11520, "trafficDown": 1288490188, "trafficUp": 325058560, "vendor": "Apple"},
				{"mac": "CC-00-00-00-00-02", "name": "NAS", "ip": "192.168.0.10", "wireless": false, "switchName": "Rack switch", "port": 4, "uptime": "3564000", "trafficDown": 94489280512, "trafficUp": 12884901888, "networkName": "LAN"},
				{"mac": "CC-00-00-00-00-03", "hostName": "printer", "ip": "192.168.0.57", "wireless": true, "ssid": "IoT", "apName": "AP office", "rssi": -67, "uptime": 522000, "trafficDown": 4194304, "trafficUp": 1048576},
			},
			"site-2": {},
		},
	}
}

// SetSites replaces the sites. Each record needs "siteId" and "name".
func (c *Controller) SetSites(sites ...Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sites = sites
}

// SetDevices replaces the devices of a site.
func (c *Controller) SetDevices(siteID string, devices ...Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.devices[siteID] = devices
}

// SetClients replaces the clients of a site.
func (c *Controller) SetClients(siteID string, clients ...Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clients[siteID] = clients
}

// SetAlerts replaces the alerts of a site. Each record takes the fields the
// controller sends plus "resolved".
func (c *Controller) SetAlerts(siteID string, alerts ...Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.alerts[siteID] = alerts
}

// SetSSIDs replaces the Wi-Fi networks of a site, all in one WLAN group.
func (c *Controller) SetSSIDs(siteID string, ssids ...Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wlans[siteID] = []Record{{"wlanId": "wlan-" + siteID, "name": "Default"}}
	c.ssids["wlan-"+siteID] = ssids
}

// SetFirmware sets what the controller knows about the firmware of a device.
func (c *Controller) SetFirmware(mac, current, latest, notes string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.firmware[mac] = Record{"curFwVer": current, "lastFwVer": latest, "fwReleaseLog": notes}
}

// FirmwareVersion returns the installed firmware version of a device.
func (c *Controller) FirmwareVersion(mac string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, _ := c.firmware[mac]["curFwVer"].(string)
	return v
}

// ExpireTokens invalidates every token issued so far, as if they had
// expired on the controller.
func (c *Controller) ExpireTokens() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens = map[string]bool{}
}

// Logins reports how many tokens have been issued.
func (c *Controller) Logins() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logins
}

// Calls returns the authenticated requests received so far, each as
// "METHOD /path-below-the-controller-id", e.g.
// "POST /sites/site-1/devices/AA-00-00-00-00-01/reboot".
func (c *Controller) Calls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

// Called reports whether the given request was received.
func (c *Controller) Called(call string) bool {
	for _, got := range c.Calls() {
		if got == call {
			return true
		}
	}
	return false
}

// DeviceName returns the current name of a device.
func (c *Controller) DeviceName(siteID, mac string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r := find(c.devices[siteID], mac); r != nil {
		name, _ := r["name"].(string)
		return name
	}
	return ""
}

// ClientName returns the current name of a client.
func (c *Controller) ClientName(siteID, mac string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r := find(c.clients[siteID], mac); r != nil {
		name, _ := r["name"].(string)
		return name
	}
	return ""
}

// Blocked reports whether a client is blocked.
func (c *Controller) Blocked(mac string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.blocked[mac]
}

func find(records []Record, mac string) Record {
	for _, r := range records {
		if r["mac"] == mac {
			return r
		}
	}
	return nil
}

// ServeHTTP implements the controller's HTTP API.
func (c *Controller) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch {
	case r.URL.Path == "/api/info":
		ok(w, Record{"omadacId": ControllerID, "controllerVer": "5.15.20.1", "apiVer": "3"})
	case r.URL.Path == "/openapi/authorize/token":
		c.login(w, r)
	case strings.HasPrefix(r.URL.Path, "/openapi/v1/"+ControllerID+"/"):
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "AccessToken=")
		if !c.tokens[token] {
			fail(w, -44112, "The access token has expired. Please re-initiate the refreshToken process to obtain the access token.")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/openapi/v1/"+ControllerID)
		c.calls = append(c.calls, r.Method+" "+path)
		c.api(w, r, strings.Split(strings.Trim(path, "/"), "/"))
	default:
		http.NotFound(w, r)
	}
}

func (c *Controller) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ControllerID string `json:"omadacId"`
		ClientID     string `json:"client_id"`
		Secret       string `json:"client_secret"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if r.Method != http.MethodPost || r.URL.Query().Get("grant_type") != "client_credentials" {
		fail(w, -44111, "Invalid grant type.")
		return
	}
	if body.ControllerID != ControllerID || body.ClientID != ClientID || body.Secret != Secret {
		fail(w, -44106, "The client id or client secret is invalid.")
		return
	}
	if c.RevokeOnLogin {
		c.tokens = map[string]bool{}
	}
	c.logins++
	c.nextTok++
	token := fmt.Sprintf("AT-%d", c.nextTok)
	c.tokens[token] = true
	ok(w, Record{"accessToken": token, "tokenType": "bearer", "expiresIn": c.TokenLifetime, "refreshToken": "RT-" + token})
}

// api routes a request below /openapi/v1/{controllerID}. parts is the path
// split on "/", starting with "sites".
func (c *Controller) api(w http.ResponseWriter, r *http.Request, parts []string) {
	if parts[0] != "sites" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		c.page(w, r, c.sites)
		return
	}
	if len(parts) < 3 {
		http.NotFound(w, r)
		return
	}
	siteID := parts[1]
	if _, found := c.devices[siteID]; !found {
		fail(w, -33004, "This site does not exist.")
		return
	}
	rest := strings.Join(parts[2:], "/")
	switch {
	case rest == "devices" && r.Method == http.MethodGet:
		c.page(w, r, c.devices[siteID])
	case rest == "clients" && r.Method == http.MethodGet:
		// A blocked client is cut off, so it is not among the connected ones.
		connected := []Record{}
		for _, client := range c.clients[siteID] {
			if mac, _ := client["mac"].(string); !c.blocked[mac] {
				connected = append(connected, client)
			}
		}
		c.page(w, r, connected)
	case rest == "insight/clients" && r.Method == http.MethodGet:
		known := []Record{}
		for _, client := range c.clients[siteID] {
			mac, _ := client["mac"].(string)
			name := client["name"]
			if name == nil {
				name = client["hostName"]
			}
			known = append(known, Record{
				"mac": mac, "name": name, "wireless": client["wireless"], "guest": false,
				"block": c.blocked[mac], "lastSeen": Epoch.Add(-10 * time.Minute).UnixMilli(),
			})
		}
		c.page(w, r, known)
	case rest == "wireless-network/wlans" && r.Method == http.MethodGet:
		ok(w, c.wlans[siteID])
	case strings.HasPrefix(rest, "wireless-network/wlans/") && r.Method == http.MethodGet:
		c.ssid(w, r, parts[4:])
	case rest == "logs/alerts" && r.Method == http.MethodGet:
		c.alertList(w, r, siteID)
	case parts[2] == "devices" && len(parts) == 5 && strings.Contains(parts[4], "upgrade") || parts[2] == "devices" && len(parts) == 5 && parts[4] == "latest-firmware-info":
		c.firmwareAction(w, r, siteID, parts[3], parts[4])
	case parts[2] == "devices" && len(parts) == 5 && parts[4] == "reboot" && r.Method == http.MethodPost:
		if find(c.devices[siteID], parts[3]) == nil {
			fail(w, -39050, "This device does not exist.")
			return
		}
		ok(w, nil)
	case parts[len(parts)-1] == "general-config":
		c.generalConfig(w, r, siteID, parts[2:len(parts)-1])
	case parts[2] == "clients" && len(parts) == 5:
		c.clientAction(w, r, siteID, parts[3], parts[4])
	default:
		http.NotFound(w, r)
	}
}

// ssid serves the Wi-Fi networks of a WLAN group. rest is the path after
// "wlans": [wlanId, "ssids"] for the list, [wlanId, "ssids", ssidId] for one.
func (c *Controller) ssid(w http.ResponseWriter, r *http.Request, rest []string) {
	if len(rest) < 2 || rest[1] != "ssids" {
		http.NotFound(w, r)
		return
	}
	ssids, found := c.ssids[rest[0]]
	if !found {
		fail(w, -33200, "This WLAN group does not exist.")
		return
	}
	if len(rest) == 2 {
		// The list leaves out the settings that hold the password.
		summaries := []Record{}
		for _, s := range ssids {
			summary := Record{}
			for k, v := range s {
				if k != "pskSetting" {
					summary[k] = v
				}
			}
			summaries = append(summaries, summary)
		}
		c.page(w, r, summaries)
		return
	}
	for _, s := range ssids {
		if s["ssidId"] == rest[2] {
			ok(w, s)
			return
		}
	}
	fail(w, -33201, "This SSID does not exist.")
}

// alertList serves the alerts of a site. Like the controller, it insists on
// a time range and filters by it and by the resolved state.
func (c *Controller) alertList(w http.ResponseWriter, r *http.Request, siteID string) {
	q := r.URL.Query()
	start, errStart := strconv.ParseInt(q.Get("filters.timeStart"), 10, 64)
	end, errEnd := strconv.ParseInt(q.Get("filters.timeEnd"), 10, 64)
	if errStart != nil || errEnd != nil {
		fail(w, -1001, "Invalid request parameters.")
		return
	}
	matching := []Record{}
	for _, a := range c.alerts[siteID] {
		at, _ := a["time"].(int64)
		resolved, _ := a["resolved"].(bool)
		if at < start || at > end {
			continue
		}
		if want := q.Get("filters.resolved"); want != "" && want != strconv.FormatBool(resolved) {
			continue
		}
		public := Record{}
		for k, v := range a {
			if k != "resolved" {
				public[k] = v
			}
		}
		matching = append(matching, public)
	}
	c.page(w, r, matching)
}

// firmwareAction serves the firmware endpoints of one device.
func (c *Controller) firmwareAction(w http.ResponseWriter, r *http.Request, siteID, mac, action string) {
	dev := find(c.devices[siteID], mac)
	info := c.firmware[mac]
	if dev == nil {
		fail(w, -39050, "This device does not exist.")
		return
	}
	if info == nil {
		info = Record{"curFwVer": dev["firmwareVersion"], "lastFwVer": "", "fwReleaseLog": ""}
		c.firmware[mac] = info
	}
	switch {
	case action == "latest-firmware-info" && r.Method == http.MethodGet:
		ok(w, info)
	case action == "start-online-upgrade" && r.Method == http.MethodPost:
		if dev["status"] != 1 {
			fail(w, -39020, "Failed to upgrade because no device is ready for upgrading.")
			return
		}
		if info["lastFwVer"] == "" || info["lastFwVer"] == info["curFwVer"] {
			fail(w, -39026, "The upgrade file is the same with the current version.")
			return
		}
		c.upgrades[mac] = &upgrade{polls: c.UpgradePolls}
		dev["detailStatus"] = 12
		ok(w, nil)
	case action == "online-upgrade-result" && r.Method == http.MethodGet:
		up := c.upgrades[mac]
		if up == nil {
			ok(w, Record{"finished": true, "success": false})
			return
		}
		if !up.finished {
			if up.polls > 0 {
				up.polls--
			} else {
				up.finished, up.success = true, !c.FailUpgrades
				dev["detailStatus"] = 14
				if up.success {
					info["curFwVer"] = info["lastFwVer"]
					dev["firmwareVersion"] = info["lastFwVer"]
				}
			}
		}
		ok(w, Record{"finished": up.finished, "success": up.success})
	default:
		http.NotFound(w, r)
	}
}

// generalConfig serves GET and PATCH of a device's general config. kind is
// the path between the site and "general-config": ["aps", mac],
// ["switches", mac], ["switches", "es", mac], or ["gateways", mac].
func (c *Controller) generalConfig(w http.ResponseWriter, r *http.Request, siteID string, kind []string) {
	wantType := map[string]string{"aps": "ap", "switches": "switch", "gateways": "gateway"}[kind[0]]
	mac := kind[len(kind)-1]
	dev := find(c.devices[siteID], mac)
	if wantType == "" || dev == nil || dev["type"] != wantType {
		fail(w, -39050, "This device does not exist.")
		return
	}
	easyManaged, _ := dev["easyManaged"].(bool)
	if wantType == "switch" && easyManaged != (len(kind) == 3) {
		fail(w, -39742, "Easy Managed switches use a different endpoint.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		ok(w, Record{"name": dev["name"], "ledSetting": dev["ledSetting"]})
	case http.MethodPatch:
		var body struct {
			Name       string `json:"name"`
			LEDSetting *int   `json:"ledSetting"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if wantType == "gateway" && body.LEDSetting == nil {
			fail(w, -1001, "Invalid request parameters.")
			return
		}
		if body.LEDSetting != nil {
			dev["ledSetting"] = *body.LEDSetting
		}
		dev["name"] = body.Name
		ok(w, nil)
	default:
		http.NotFound(w, r)
	}
}

func (c *Controller) clientAction(w http.ResponseWriter, r *http.Request, siteID, mac, action string) {
	client := find(c.clients[siteID], mac)
	if client == nil && action != "unblock" {
		fail(w, -41011, "This client does not exist.")
		return
	}
	switch {
	case action == "name" && r.Method == http.MethodPatch:
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		client["name"] = body.Name
	case action == "block" && r.Method == http.MethodPost:
		c.blocked[mac] = true
	case action == "unblock" && r.Method == http.MethodPost:
		if !c.blocked[mac] {
			fail(w, -41004, "This client does not exist.")
			return
		}
		delete(c.blocked, mac)
	case action == "reconnect" && r.Method == http.MethodPost:
		if client["wireless"] != true {
			fail(w, -41025, "Unable to reconnect a wired client.")
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	ok(w, nil)
}

func (c *Controller) page(w http.ResponseWriter, r *http.Request, all []Record) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	if page < 1 || size < 1 {
		fail(w, -1001, "Invalid request parameters.")
		return
	}
	if c.MaxPageSize > 0 && size > c.MaxPageSize {
		size = c.MaxPageSize
	}
	start := min((page-1)*size, len(all))
	end := min(start+size, len(all))
	data := all[start:end]
	if data == nil {
		data = []Record{}
	}
	ok(w, Record{"totalRows": len(all), "currentPage": page, "currentSize": size, "data": data})
}

func ok(w http.ResponseWriter, result any) {
	write(w, Record{"errorCode": 0, "msg": "Success.", "result": result})
}

func fail(w http.ResponseWriter, code int, msg string) {
	write(w, Record{"errorCode": code, "msg": msg})
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
