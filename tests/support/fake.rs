//! A fake Omada controller for tests. It speaks enough of the Open API over
//! HTTP for the client, the CLI, and the TUI to be tested without a real
//! controller: tokens, paging, error codes, and the supported actions.
#![allow(dead_code)]

use std::collections::{HashMap, HashSet};
use std::sync::{Arc, Mutex};

use serde_json::{json, Map, Value};

/// Credentials every fake controller accepts.
pub const CONTROLLER_ID: &str = "fake-omadac-id";
pub const CLIENT_ID: &str = "fake-client-id";
pub const SECRET: &str = "fake-client-secret";

/// The moment the sample data is dated around: 2026-11-23 12:00 UTC, in
/// milliseconds since 1970. Tests that show times set their clock to it.
pub const EPOCH_MS: i64 = 1_795_435_200_000;

const MINUTE: i64 = 60_000;
const HOUR: i64 = 60 * MINUTE;

struct Upgrade {
    polls: u32,
    finished: bool,
    success: bool,
}

/// The state of the fake controller. Tests reach in to set up quirks and
/// to check what happened.
pub struct State {
    /// The `expiresIn` value of issued tokens, in seconds.
    pub token_lifetime: i64,
    /// Caps the page size, to force paging in tests. Zero means no cap.
    pub max_page_size: usize,
    /// How many times the result of an upgrade is reported as unfinished
    /// before it completes.
    pub upgrade_polls: u32,
    /// Makes upgrades finish without success.
    pub fail_upgrades: bool,

    pub sites: Vec<Value>,
    pub devices: HashMap<String, Vec<Value>>, // by site ID
    pub clients: HashMap<String, Vec<Value>>, // by site ID
    pub wlans: HashMap<String, Vec<Value>>,   // WLAN groups by site ID
    pub ssids: HashMap<String, Vec<Value>>,   // Wi-Fi networks by WLAN group ID
    pub alerts: HashMap<String, Vec<Value>>,  // by site ID; "resolved" is internal
    pub firmware: HashMap<String, Value>,     // latest firmware info by device MAC
    pub blocked: HashSet<String>,
    pub tokens: HashSet<String>,
    pub logins: usize,
    /// The authenticated requests received so far, each as
    /// "METHOD /path-below-the-controller-id".
    pub calls: Vec<String>,
    upgrades: HashMap<String, Upgrade>,
}

fn sample() -> State {
    let by_site = |a: Value, b: Value| -> HashMap<String, Vec<Value>> {
        HashMap::from([
            ("site-1".to_string(), a.as_array().unwrap().clone()),
            ("site-2".to_string(), b.as_array().unwrap().clone()),
        ])
    };
    State {
        token_lifetime: 7200,
        max_page_size: 0,
        upgrade_polls: 0,
        fail_upgrades: false,
        sites: vec![json!({"siteId": "site-1", "name": "Default"}), json!({"siteId": "site-2", "name": "Workshop"})],
        devices: by_site(
            json!([
                {"mac": "AA-00-00-00-00-01", "name": "Core router", "type": "gateway", "model": "ER707-M2", "ip": "192.168.0.1", "status": 1, "uptime": "41day(s) 6h 2m 1s", "cpuUtil": 8, "memUtil": 31, "firmwareVersion": "1.2.3", "sn": "SN001", "ledSetting": 1},
                {"mac": "AA-00-00-00-00-02", "name": "Rack switch", "type": "switch", "model": "SG3428XMP", "ip": "192.168.0.2", "status": 1, "uptime": "41day(s) 6h 1m 0s", "cpuUtil": 12, "memUtil": 44, "firmwareVersion": "3.0.1", "sn": "SN002"},
                {"mac": "AA-00-00-00-00-03", "name": "AP office", "type": "ap", "model": "EAP670", "ip": "192.168.0.21", "status": 1, "uptime": 1044000, "cpuUtil": "5", "memUtil": "52", "firmwareVersion": "1.0.14", "sn": "SN003"},
                {"mac": "AA-00-00-00-00-04", "name": "AP garage", "type": "ap", "model": "EAP225", "ip": "192.168.0.23", "status": 0, "firmwareVersion": "5.1.0", "sn": "SN004"},
            ]),
            json!([{"mac": "BB-00-00-00-00-01", "name": "Workshop AP", "type": "ap", "model": "EAP610", "ip": "10.0.0.2", "status": 1, "uptime": 3600}]),
        ),
        clients: by_site(
            json!([
                {"mac": "CC-00-00-00-00-01", "name": "Laptop", "hostName": "laptop", "ip": "192.168.0.104", "wireless": true, "ssid": "Home-5G", "apName": "AP office", "rssi": -52, "uptime": 11520, "trafficDown": 1288490188i64, "trafficUp": 325058560, "vendor": "Apple"},
                {"mac": "CC-00-00-00-00-02", "name": "NAS", "ip": "192.168.0.10", "wireless": false, "switchName": "Rack switch", "port": 4, "uptime": "3564000", "trafficDown": 94489280512i64, "trafficUp": 12884901888i64, "networkName": "LAN"},
                {"mac": "CC-00-00-00-00-03", "hostName": "printer", "ip": "192.168.0.57", "wireless": true, "ssid": "IoT", "apName": "AP office", "rssi": -67, "uptime": 522000, "trafficDown": 4194304, "trafficUp": 1048576},
            ]),
            json!([]),
        ),
        wlans: by_site(json!([{"wlanId": "wlan-1", "name": "Default", "primary": true}]), json!([])),
        ssids: HashMap::from([(
            "wlan-1".to_string(),
            json!([
                {"ssidId": "ssid-1", "name": "Home-5G", "band": 2, "security": 3, "guestNetEnable": false, "broadcast": true, "pskSetting": {"securityKey": "correct horse battery", "versionPsk": 4}},
                {"ssidId": "ssid-2", "name": "IoT", "band": 1, "security": 3, "guestNetEnable": false, "broadcast": false, "vlanEnable": true, "vlanId": 30, "pskSetting": {"securityKey": "things;and:stuff", "versionPsk": 2}},
                {"ssidId": "ssid-3", "name": "Guests", "band": 3, "security": 0, "guestNetEnable": true, "broadcast": true},
                {"ssidId": "ssid-4", "name": "Staff", "band": 7, "security": 2, "guestNetEnable": false, "broadcast": true},
            ])
            .as_array()
            .unwrap()
            .clone(),
        )]),
        alerts: by_site(
            json!([
                {"id": "alert-1", "key": "DEVICE_DISCONNECTED", "module": "Device", "level": "Warning", "content": "[ap:AP garage:AA-00-00-00-00-04] was disconnected.", "time": EPOCH_MS - 50 * MINUTE, "resolved": false},
                {"id": "alert-2", "key": "POE_OVERLOAD", "module": "Device", "level": "Error", "content": "[switch:Rack switch:AA-00-00-00-00-02] port 7 was shut down because the PoE budget was exceeded. Reduce the load on the switch or raise the budget, then turn the port back on.", "time": EPOCH_MS - 14 * HOUR, "resolved": false},
                {"id": "alert-3", "key": "CLIENT_BLOCKED", "module": "Client", "level": "Information", "content": "[client:Unknown phone:7A-11-22-33-44-55] was blocked.", "time": EPOCH_MS - 65 * HOUR, "resolved": true},
                {"id": "alert-4", "key": "DEVICE_DISCONNECTED", "module": "Device", "level": "Warning", "content": "An alert older than the time range anyone asks for.", "time": EPOCH_MS - 90 * 24 * HOUR, "resolved": false},
            ]),
            json!([]),
        ),
        firmware: HashMap::from([
            ("AA-00-00-00-00-01".to_string(), json!({"curFwVer": "1.2.3", "lastFwVer": "1.2.3", "fwReleaseLog": ""})),
            ("AA-00-00-00-00-02".to_string(), json!({"curFwVer": "3.0.1", "lastFwVer": "3.0.1", "fwReleaseLog": ""})),
            ("AA-00-00-00-00-03".to_string(), json!({"curFwVer": "1.0.14", "lastFwVer": "1.1.2", "fwReleaseLog": "1. Improved roaming between access points.\n2. Fixed a restart under heavy load."})),
            ("AA-00-00-00-00-04".to_string(), json!({"curFwVer": "5.1.0", "lastFwVer": "", "fwReleaseLog": ""})),
        ]),
        blocked: HashSet::new(),
        tokens: HashSet::new(),
        logins: 0,
        calls: Vec::new(),
        upgrades: HashMap::new(),
    }
}

/// A fake Omada controller listening on a local port. It stops when
/// dropped.
pub struct Controller {
    /// The address of the controller, e.g. "http://127.0.0.1:41234".
    pub url: String,
    pub state: Arc<Mutex<State>>,
    server: Arc<tiny_http::Server>,
    thread: Option<std::thread::JoinHandle<()>>,
}

impl Controller {
    /// Starts a fake controller over plain HTTP with a small sample
    /// network.
    pub fn start() -> Controller {
        let server = Arc::new(tiny_http::Server::http("127.0.0.1:0").expect("start the fake controller"));
        let url = format!("http://{}", server.server_addr().to_ip().expect("an IP address"));
        let state = Arc::new(Mutex::new(sample()));
        let (srv, st) = (server.clone(), state.clone());
        let thread = std::thread::spawn(move || {
            for mut req in srv.incoming_requests() {
                let mut body = String::new();
                let _ = req.as_reader().read_to_string(&mut body);
                let auth = req
                    .headers()
                    .iter()
                    .find(|h| h.field.equiv("Authorization"))
                    .map(|h| h.value.as_str().to_string())
                    .unwrap_or_default();
                let (path, query) = req.url().split_once('?').unwrap_or((req.url(), ""));
                let answer = st.lock().unwrap().serve(req.method().as_str(), path, query, &auth, &body);
                let response = match answer {
                    Some(v) => tiny_http::Response::from_string(v.to_string())
                        .with_header("Content-Type: application/json".parse::<tiny_http::Header>().unwrap()),
                    None => tiny_http::Response::from_string("404 page not found\n").with_status_code(404),
                };
                let _ = req.respond(response);
            }
        });
        Controller { url, state, server, thread: Some(thread) }
    }

    /// Gives access to the state of the controller.
    pub fn with<T>(&self, f: impl FnOnce(&mut State) -> T) -> T {
        f(&mut self.state.lock().unwrap())
    }

    /// Reports whether the given request was received, e.g.
    /// "POST /sites/site-1/devices/AA-00-00-00-00-01/reboot".
    pub fn called(&self, call: &str) -> bool {
        self.with(|s| s.calls.iter().any(|c| c == call))
    }

    /// Reports how many tokens have been issued.
    pub fn logins(&self) -> usize {
        self.with(|s| s.logins)
    }

    /// Invalidates every token issued so far, as if they had expired on the
    /// controller.
    pub fn expire_tokens(&self) {
        self.with(|s| s.tokens.clear());
    }

    /// Returns the current name of a device.
    pub fn device_name(&self, site_id: &str, mac: &str) -> String {
        self.with(|s| {
            find(&s.devices[site_id], mac)
                .and_then(|i| s.devices[site_id][i]["name"].as_str().map(String::from))
                .unwrap_or_default()
        })
    }

    /// Returns the current name of a client.
    pub fn client_name(&self, site_id: &str, mac: &str) -> String {
        self.with(|s| {
            find(&s.clients[site_id], mac)
                .and_then(|i| s.clients[site_id][i]["name"].as_str().map(String::from))
                .unwrap_or_default()
        })
    }

    /// Replaces the Wi-Fi networks of a site, all in one WLAN group.
    pub fn set_ssids(&self, site_id: &str, ssids: Vec<Value>) {
        self.with(|s| {
            let wlan = format!("wlan-{site_id}");
            s.wlans.insert(site_id.into(), vec![json!({"wlanId": wlan, "name": "Default"})]);
            s.ssids.insert(wlan, ssids);
        });
    }
}

impl Drop for Controller {
    fn drop(&mut self) {
        self.server.unblock();
        if let Some(t) = self.thread.take() {
            let _ = t.join();
        }
    }
}

fn find(records: &[Value], mac: &str) -> Option<usize> {
    records.iter().position(|r| r["mac"] == mac)
}

fn ok(result: Value) -> Option<Value> {
    Some(json!({"errorCode": 0, "msg": "Success.", "result": result}))
}

fn fail(code: i64, msg: &str) -> Option<Value> {
    Some(json!({"errorCode": code, "msg": msg}))
}

fn param<'a>(query: &'a str, name: &str) -> &'a str {
    query.split('&').filter_map(|kv| kv.split_once('=')).find(|(k, _)| *k == name).map_or("", |(_, v)| v)
}

/// Copies a record without one of its fields.
fn without(record: &Value, field: &str) -> Value {
    let mut copy: Map<String, Value> = record.as_object().cloned().unwrap_or_default();
    copy.remove(field);
    Value::Object(copy)
}

impl State {
    /// Implements the controller's HTTP API. `None` is a 404.
    fn serve(&mut self, method: &str, path: &str, query: &str, auth: &str, body: &str) -> Option<Value> {
        let body: Value = serde_json::from_str(body).unwrap_or(Value::Null);
        let api = format!("/openapi/v1/{CONTROLLER_ID}/");
        if path == "/api/info" {
            return ok(json!({"omadacId": CONTROLLER_ID, "controllerVer": "5.15.20.1", "apiVer": "3"}));
        }
        if path == "/openapi/authorize/token" {
            return self.login(method, query, &body);
        }
        let rest = path.strip_prefix(&api)?;
        let token = auth.strip_prefix("AccessToken=").unwrap_or(auth);
        if !self.tokens.contains(token) {
            return fail(
                -44112,
                "The access token has expired. Please re-initiate the refreshToken process to obtain the access token.",
            );
        }
        self.calls.push(format!("{method} /{rest}"));
        let parts: Vec<&str> = rest.trim_matches('/').split('/').collect();
        self.api(method, query, &body, &parts)
    }

    fn login(&mut self, method: &str, query: &str, body: &Value) -> Option<Value> {
        if method != "POST" || param(query, "grant_type") != "client_credentials" {
            return fail(-44111, "Invalid grant type.");
        }
        if body["omadacId"] != CONTROLLER_ID || body["client_id"] != CLIENT_ID || body["client_secret"] != SECRET {
            return fail(-44106, "The client id or client secret is invalid.");
        }
        self.logins += 1;
        let token = format!("AT-{}", self.logins);
        self.tokens.insert(token.clone());
        ok(
            json!({"accessToken": token, "tokenType": "bearer", "expiresIn": self.token_lifetime, "refreshToken": format!("RT-{token}")}),
        )
    }

    /// Routes a request below /openapi/v1/{controllerID}. `parts` is the
    /// path split on "/", starting with "sites".
    fn api(&mut self, method: &str, query: &str, body: &Value, parts: &[&str]) -> Option<Value> {
        if parts[0] != "sites" {
            return None;
        }
        if parts.len() == 1 && method == "GET" {
            return self.page(query, self.sites.clone());
        }
        if parts.len() < 3 {
            return None;
        }
        let site = parts[1];
        if !self.devices.contains_key(site) {
            return fail(-33004, "This site does not exist.");
        }
        let rest = parts[2..].join("/");
        let last = parts[parts.len() - 1];
        match (method, rest.as_str()) {
            ("GET", "devices") => self.page(query, self.devices[site].clone()),
            ("GET", "clients") => {
                // A blocked client is cut off, so it is not among the
                // connected ones.
                let connected = self.clients[site]
                    .iter()
                    .filter(|c| !self.blocked.contains(c["mac"].as_str().unwrap_or("")))
                    .cloned()
                    .collect();
                self.page(query, connected)
            }
            ("GET", "insight/clients") => {
                let known = self.clients[site]
                    .iter()
                    .map(|c| {
                        let name = if c["name"].is_null() { c["hostName"].clone() } else { c["name"].clone() };
                        json!({"mac": c["mac"], "name": name, "wireless": c["wireless"], "guest": false,
                               "block": self.blocked.contains(c["mac"].as_str().unwrap_or("")), "lastSeen": EPOCH_MS - 10 * MINUTE})
                    })
                    .collect();
                self.page(query, known)
            }
            ("GET", "wireless-network/wlans") => ok(Value::Array(self.wlans.get(site).cloned().unwrap_or_default())),
            ("GET", _) if rest.starts_with("wireless-network/wlans/") => self.ssid(query, &parts[4..]),
            ("GET", "logs/alerts") => self.alert_list(query, site),
            _ if parts[2] == "devices"
                && parts.len() == 5
                && (last.contains("upgrade") || last == "latest-firmware-info") =>
            {
                self.firmware_action(method, site, parts[3], last)
            }
            ("POST", _) if parts[2] == "devices" && parts.len() == 5 && last == "reboot" => {
                match find(&self.devices[site], parts[3]) {
                    Some(_) => ok(Value::Null),
                    None => fail(-39050, "This device does not exist."),
                }
            }
            _ if last == "general-config" => self.general_config(method, body, site, &parts[2..parts.len() - 1]),
            _ if parts[2] == "clients" && parts.len() == 5 => self.client_action(method, body, site, parts[3], last),
            _ => None,
        }
    }

    /// Serves the Wi-Fi networks of a WLAN group. `rest` is the path after
    /// "wlans": [wlanId, "ssids"] for the list, [wlanId, "ssids", ssidId]
    /// for one.
    fn ssid(&self, query: &str, rest: &[&str]) -> Option<Value> {
        if rest.len() < 2 || rest[1] != "ssids" {
            return None;
        }
        let Some(ssids) = self.ssids.get(rest[0]) else {
            return fail(-33200, "This WLAN group does not exist.");
        };
        if rest.len() == 2 {
            // The list leaves out the settings that hold the password.
            return self.page(query, ssids.iter().map(|s| without(s, "pskSetting")).collect());
        }
        match ssids.iter().find(|s| s["ssidId"] == rest[2]) {
            Some(s) => ok(s.clone()),
            None => fail(-33201, "This SSID does not exist."),
        }
    }

    /// Serves the alerts of a site. Like the controller, it insists on a
    /// time range and filters by it and by the resolved state.
    fn alert_list(&self, query: &str, site: &str) -> Option<Value> {
        let (Ok(start), Ok(end)) =
            (param(query, "filters.timeStart").parse::<i64>(), param(query, "filters.timeEnd").parse::<i64>())
        else {
            return fail(-1001, "Invalid request parameters.");
        };
        let want = param(query, "filters.resolved");
        let matching = self.alerts[site]
            .iter()
            .filter(|a| {
                let at = a["time"].as_i64().unwrap_or(0);
                let resolved = a["resolved"].as_bool().unwrap_or(false);
                at >= start && at <= end && (want.is_empty() || want == resolved.to_string())
            })
            .map(|a| without(a, "resolved"))
            .collect();
        self.page(query, matching)
    }

    /// Serves the firmware endpoints of one device.
    fn firmware_action(&mut self, method: &str, site: &str, mac: &str, action: &str) -> Option<Value> {
        let Some(i) = find(&self.devices[site], mac) else {
            return fail(-39050, "This device does not exist.");
        };
        let devices = self.devices.get_mut(site).unwrap();
        let installed = devices[i]["firmwareVersion"].clone();
        let info = self
            .firmware
            .entry(mac.to_string())
            .or_insert_with(|| json!({"curFwVer": installed, "lastFwVer": "", "fwReleaseLog": ""}));
        match (method, action) {
            ("GET", "latest-firmware-info") => ok(info.clone()),
            ("POST", "start-online-upgrade") => {
                if devices[i]["status"] != 1 {
                    return fail(-39020, "Failed to upgrade because no device is ready for upgrading.");
                }
                if info["lastFwVer"] == "" || info["lastFwVer"] == info["curFwVer"] {
                    return fail(-39026, "The upgrade file is the same with the current version.");
                }
                self.upgrades
                    .insert(mac.into(), Upgrade { polls: self.upgrade_polls, finished: false, success: false });
                devices[i]["detailStatus"] = json!(12);
                ok(Value::Null)
            }
            ("GET", "online-upgrade-result") => {
                let Some(up) = self.upgrades.get_mut(mac) else {
                    return ok(json!({"finished": true, "success": false}));
                };
                if !up.finished {
                    if up.polls > 0 {
                        up.polls -= 1;
                    } else {
                        up.finished = true;
                        up.success = !self.fail_upgrades;
                        devices[i]["detailStatus"] = json!(14);
                        if up.success {
                            info["curFwVer"] = info["lastFwVer"].clone();
                            devices[i]["firmwareVersion"] = info["lastFwVer"].clone();
                        }
                    }
                }
                ok(json!({"finished": up.finished, "success": up.success}))
            }
            _ => None,
        }
    }

    /// Serves GET and PATCH of a device's general config. `kind` is the
    /// path between the site and "general-config": ["aps", mac],
    /// ["switches", mac], ["switches", "es", mac], or ["gateways", mac].
    fn general_config(&mut self, method: &str, body: &Value, site: &str, kind: &[&str]) -> Option<Value> {
        let want = match kind.first().copied() {
            Some("aps") => "ap",
            Some("switches") => "switch",
            Some("gateways") => "gateway",
            _ => return fail(-39050, "This device does not exist."),
        };
        let devices = self.devices.get_mut(site).unwrap();
        let Some(dev) = find(devices, kind[kind.len() - 1]).map(|i| &mut devices[i]).filter(|d| d["type"] == want)
        else {
            return fail(-39050, "This device does not exist.");
        };
        let easy_managed = dev["easyManaged"].as_bool().unwrap_or(false);
        if want == "switch" && easy_managed != (kind.len() == 3) {
            return fail(-39742, "Easy Managed switches use a different endpoint.");
        }
        match method {
            "GET" => ok(json!({"name": dev["name"], "ledSetting": dev["ledSetting"]})),
            "PATCH" => {
                if want == "gateway" && body["ledSetting"].is_null() {
                    return fail(-1001, "Invalid request parameters.");
                }
                if !body["ledSetting"].is_null() {
                    dev["ledSetting"] = body["ledSetting"].clone();
                }
                dev["name"] = body["name"].clone();
                ok(Value::Null)
            }
            _ => None,
        }
    }

    fn client_action(&mut self, method: &str, body: &Value, site: &str, mac: &str, action: &str) -> Option<Value> {
        let clients = self.clients.get_mut(site).unwrap();
        let client = find(clients, mac).map(|i| &mut clients[i]);
        if client.is_none() && action != "unblock" {
            return fail(-41011, "This client does not exist.");
        }
        match (method, action) {
            ("PATCH", "name") => client.unwrap()["name"] = body["name"].clone(),
            ("POST", "block") => drop(self.blocked.insert(mac.into())),
            ("POST", "unblock") => {
                if !self.blocked.remove(mac) {
                    return fail(-41004, "This client does not exist.");
                }
            }
            ("POST", "reconnect") => {
                if client.unwrap()["wireless"] != true {
                    return fail(-41025, "Unable to reconnect a wired client.");
                }
            }
            _ => return None,
        }
        ok(Value::Null)
    }

    fn page(&self, query: &str, all: Vec<Value>) -> Option<Value> {
        let page: usize = param(query, "page").parse().unwrap_or(0);
        let mut size: usize = param(query, "pageSize").parse().unwrap_or(0);
        if page < 1 || size < 1 {
            return fail(-1001, "Invalid request parameters.");
        }
        if self.max_page_size > 0 {
            size = size.min(self.max_page_size);
        }
        let start = ((page - 1) * size).min(all.len());
        let end = (start + size).min(all.len());
        ok(json!({"totalRows": all.len(), "currentPage": page, "currentSize": size, "data": all[start..end]}))
    }
}
