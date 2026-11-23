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

	sites   []Record
	devices map[string][]Record // by site ID
	clients map[string][]Record // by site ID
	tokens  map[string]bool
	logins  int
	nextTok int
	calls   []string
	blocked map[string]bool
}

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
		c.page(w, r, c.clients[siteID])
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
