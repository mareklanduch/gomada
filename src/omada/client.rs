use std::sync::{Arc, Mutex};

use chrono::{DateTime, Utc};
use percent_encoding::{utf8_percent_encode, AsciiSet, NON_ALPHANUMERIC};
use serde::de::DeserializeOwned;
use serde::Deserialize;
use serde_json::{json, Value};

use super::http::Http;
use super::types::*;
use crate::error::{Error, Result};

/// Returns the current time. Tests replace it.
pub type Clock = Arc<dyn Fn() -> DateTime<Utc> + Send + Sync>;

/// Returns the client secret. It is called only when the client has to log
/// in, so a valid cached token never needs the secret.
pub type SecretFn = Box<dyn Fn() -> Result<String> + Send + Sync>;

/// Configures a [`Client`].
pub struct Options {
    /// The controller address, e.g. "https://192.168.0.2:8043".
    pub base_url: String,
    /// The controller's omadacId.
    pub controller_id: String,
    /// Identifies the Open API application.
    pub client_id: String,
    pub secret: Option<SecretFn>,
    pub http: Http,
    /// Optionally keeps the access token between runs.
    pub cache: Option<Box<dyn TokenCache>>,
    /// `None` means the system clock.
    pub now: Option<Clock>,
}

#[derive(Default)]
struct State {
    token: Token,
    /// The last access token the controller refused.
    rejected: String,
}

/// Talks to one controller. It is safe for concurrent use.
pub struct Client {
    base_url: String,
    controller_id: String,
    client_id: String,
    secret: Option<SecretFn>,
    http: Http,
    cache: Option<Box<dyn TokenCache>>,
    now: Clock,
    state: Mutex<State>,
}

/// Used when the controller does not say how long a token lasts.
const FALLBACK_TOKEN_LIFETIME_SECS: f64 = 600.0;

/// Error codes the controller answers with when an access token is expired
/// or invalid.
const CODE_TOKEN_EXPIRED: i64 = -44112;
const CODE_TOKEN_INVALID: i64 = -44113;

/// The answer to a switch request for a switch that must be addressed
/// through the Easy Managed path.
const CODE_EASY_MANAGED_SWITCH: i64 = -39742;

/// Bounds list requests against a controller that never stops paging.
const MAX_PAGES: usize = 1000;

/// How many of the newest alerts are returned for each state, resolved and
/// unresolved.
pub const ALERTS_PER_STATE: usize = 100;

/// Everything but letters, digits, and `-._~` is escaped in a path segment.
const PATH_SEGMENT: &AsciiSet = &NON_ALPHANUMERIC.remove(b'-').remove(b'.').remove(b'_').remove(b'~');

fn esc(segment: &str) -> String {
    utf8_percent_encode(segment, PATH_SEGMENT).to_string()
}

fn site_path(site_id: &str, rest: &str) -> String {
    format!("/sites/{}{rest}", esc(site_id))
}

fn device_path(site_id: &str, mac: &str, rest: &str) -> String {
    site_path(site_id, &format!("/devices/{}{rest}", esc(mac)))
}

fn client_path(site_id: &str, mac: &str, rest: &str) -> String {
    site_path(site_id, &format!("/clients/{}{rest}", esc(mac)))
}

fn from_value<T: DeserializeOwned + Default>(v: Option<Value>) -> Result<T> {
    match v {
        None => Ok(T::default()),
        Some(v) => serde_json::from_value(v).map_err(|e| Error::other(format!("decode response: {e}"))),
    }
}

fn token_rejected(err: &Error) -> bool {
    err.api().is_some_and(|a| a.code == CODE_TOKEN_EXPIRED || a.code == CODE_TOKEN_INVALID || a.http_status == 401)
}

/// Asks the controller at `base_url` for its ID and version.
pub fn fetch_info(http: &Http, base_url: &str) -> Result<Info> {
    let url = format!("{}/api/info", base_url.trim_end_matches('/'));
    let info: Info = from_value(http.request("GET", &url, "", None)?)?;
    if info.controller_id.is_empty() {
        return Err(Error::other("the address answered, but not like an Omada controller (no controller ID)"));
    }
    Ok(info)
}

#[derive(Deserialize)]
struct Page<T> {
    #[serde(default, rename = "totalRows", deserialize_with = "int")]
    total_rows: i64,
    #[serde(default = "Vec::new", deserialize_with = "nullable", bound(deserialize = "T: Deserialize<'de>"))]
    data: Vec<T>,
}

impl<T> Default for Page<T> {
    fn default() -> Self {
        Page { total_rows: 0, data: Vec::new() }
    }
}

impl Client {
    /// Returns a client for the controller described by `opts`.
    pub fn new(opts: Options) -> Client {
        Client {
            base_url: opts.base_url.trim_end_matches('/').to_string(),
            controller_id: opts.controller_id,
            client_id: opts.client_id,
            secret: opts.secret,
            http: opts.http,
            cache: opts.cache,
            now: opts.now.unwrap_or_else(|| Arc::new(Utc::now)),
            state: Mutex::new(State::default()),
        }
    }

    /// Makes sure the client holds a usable access token, logging in if
    /// necessary. Calling it is optional; every request does the same.
    pub fn login(&self) -> Result<()> {
        self.access_token().map(|_| ())
    }

    /// Returns a token that is valid for at least a minute. It uses, in
    /// order: the token in memory, the cached token, a fresh login.
    fn access_token(&self) -> Result<String> {
        let mut st = self.state.lock().unwrap();
        let now = (self.now)();
        if st.token.valid(now) {
            return Ok(st.token.access_token.clone());
        }
        if let Some(cache) = &self.cache {
            if let Some(t) = cache.load() {
                if t.valid(now) && t.access_token != st.rejected {
                    st.token = t;
                    return Ok(st.token.access_token.clone());
                }
            }
        }
        let t = self.fresh_token()?;
        if let Some(cache) = &self.cache {
            cache.save(&t);
        }
        st.token = t;
        Ok(st.token.access_token.clone())
    }

    fn fresh_token(&self) -> Result<Token> {
        let secret = match &self.secret {
            Some(f) => f()?,
            None => return Err(Error::Auth("no client secret available".into())),
        };
        #[derive(Deserialize, Default)]
        struct Res {
            #[serde(default, rename = "accessToken", deserialize_with = "nullable")]
            access_token: String,
            #[serde(default, rename = "expiresIn", deserialize_with = "num")]
            expires_in: f64,
        }
        let body = json!({
            "omadacId": self.controller_id,
            "client_id": self.client_id,
            "client_secret": secret,
        });
        let url = format!("{}/openapi/authorize/token?grant_type=client_credentials", self.base_url);
        let res: Res = match self.http.request("POST", &url, "", Some(&body)) {
            Ok(v) => from_value(v)?,
            Err(Error::Api(a)) if a.http_status == 404 => {
                return Err(Error::Api(a).context(
                    "the controller has no Open API at this address (HTTP 404); check the address and that this controller version offers Settings > Platform Integration > Open API",
                ))
            }
            Err(Error::Api(a)) => return Err(Error::Auth(a.to_string())),
            Err(e) => return Err(e),
        };
        if res.access_token.is_empty() {
            return Err(Error::Auth("the controller returned no access token".into()));
        }
        let lifetime = if res.expires_in > 0.0 { res.expires_in } else { FALLBACK_TOKEN_LIFETIME_SECS };
        Ok(Token {
            access_token: res.access_token,
            expires_at: (self.now)() + chrono::Duration::milliseconds((lifetime * 1000.0) as i64),
        })
    }

    /// Forgets a token the controller refused, so the next request does not
    /// reuse it from memory or from the cache.
    fn reject(&self, token: &str) {
        let mut st = self.state.lock().unwrap();
        st.rejected = token.to_string();
        if st.token.access_token == token {
            st.token = Token::default();
        }
        if let Some(cache) = &self.cache {
            if cache.load().is_some_and(|t| t.access_token == token) {
                cache.clear();
            }
        }
    }

    /// Makes an authenticated request below /openapi/v1/{controllerID}.
    /// When the controller rejects the token it logs in again and repeats
    /// the request once.
    fn call(&self, method: &str, path: &str, body: Option<&Value>) -> Result<Option<Value>> {
        let url = format!("{}/openapi/v1/{}{path}", self.base_url, esc(&self.controller_id));
        let token = self.access_token()?;
        match self.http.request(method, &url, &token, body) {
            Err(e) if token_rejected(&e) => {
                self.reject(&token);
                let token = self.access_token()?;
                self.http.request(method, &url, &token, body)
            }
            other => other,
        }
    }

    fn get<T: DeserializeOwned + Default>(&self, path: &str) -> Result<T> {
        from_value(self.call("GET", path, None)?)
    }

    /// Fetches every page of a paged endpoint.
    fn list<T: DeserializeOwned>(&self, path: &str) -> Result<Vec<T>> {
        let mut all = Vec::new();
        for page in 1..=MAX_PAGES {
            let res: Page<T> = self.get(&format!("{path}?page={page}&pageSize=100"))?;
            let empty = res.data.is_empty();
            all.extend(res.data);
            if empty || all.len() as i64 >= res.total_rows {
                break;
            }
        }
        Ok(all)
    }

    /// Lists the sites the application may access.
    pub fn sites(&self) -> Result<Vec<Site>> {
        self.list("/sites")
    }

    /// Lists the devices of a site.
    pub fn devices(&self, site_id: &str) -> Result<Vec<Device>> {
        self.list(&site_path(site_id, "/devices"))
    }

    /// Restarts a device.
    pub fn reboot_device(&self, site_id: &str, mac: &str) -> Result<()> {
        self.call("POST", &device_path(site_id, mac, "/reboot"), None).map(|_| ())
    }

    /// Sets the name of a device. The Open API has one endpoint per device
    /// type, so the device's type decides which one is used.
    pub fn rename_device(&self, site_id: &str, d: &Device, name: &str) -> Result<()> {
        validate_name(name).map_err(Error::Other)?;
        let mac = esc(&d.mac);
        let body = json!({ "name": name });
        match d.kind.as_str() {
            TYPE_AP => {
                self.call("PATCH", &site_path(site_id, &format!("/aps/{mac}/general-config")), Some(&body)).map(|_| ())
            }
            TYPE_SWITCH => {
                let first =
                    self.call("PATCH", &site_path(site_id, &format!("/switches/{mac}/general-config")), Some(&body));
                match first {
                    Err(e) if e.api().is_some_and(|a| a.code == CODE_EASY_MANAGED_SWITCH) => self
                        .call("PATCH", &site_path(site_id, &format!("/switches/es/{mac}/general-config")), Some(&body))
                        .map(|_| ()),
                    other => other.map(|_| ()),
                }
            }
            TYPE_GATEWAY => {
                // The gateway endpoint requires ledSetting, so the current
                // value is read and sent back unchanged.
                #[derive(Deserialize, Default)]
                struct Current {
                    #[serde(default, rename = "ledSetting")]
                    led_setting: Option<i64>,
                }
                const LED_USE_SITE_SETTINGS: i64 = 2;
                let path = site_path(site_id, &format!("/gateways/{mac}/general-config"));
                let current: Current = self.get(&path)?;
                let body = json!({
                    "name": name,
                    "ledSetting": current.led_setting.unwrap_or(LED_USE_SITE_SETTINGS),
                });
                self.call("PATCH", &path, Some(&body)).map(|_| ())
            }
            other => Err(Error::other(format!("renaming a device of type {other:?} is not supported"))),
        }
    }

    /// Lists the clients currently connected to a site.
    pub fn clients(&self, site_id: &str) -> Result<Vec<NetClient>> {
        self.list(&site_path(site_id, "/clients"))
    }

    /// Lists the clients of a site that are barred from the network. The
    /// controller has no list of just those, so its list of known clients
    /// is read and filtered.
    pub fn blocked_clients(&self, site_id: &str) -> Result<Vec<KnownClient>> {
        let known: Vec<KnownClient> = self.list(&site_path(site_id, "/insight/clients"))?;
        Ok(known.into_iter().filter(|k| k.blocked).collect())
    }

    /// Sets the friendly name of a client.
    pub fn rename_client(&self, site_id: &str, mac: &str, name: &str) -> Result<()> {
        validate_name(name).map_err(Error::Other)?;
        self.call("PATCH", &client_path(site_id, mac, "/name"), Some(&json!({ "name": name }))).map(|_| ())
    }

    /// Bars a client from the network.
    pub fn block_client(&self, site_id: &str, mac: &str) -> Result<()> {
        self.call("POST", &client_path(site_id, mac, "/block"), None).map(|_| ())
    }

    /// Lets a blocked client back on the network.
    pub fn unblock_client(&self, site_id: &str, mac: &str) -> Result<()> {
        self.call("POST", &client_path(site_id, mac, "/unblock"), None).map(|_| ())
    }

    /// Forces a wireless client to re-associate.
    pub fn reconnect_client(&self, site_id: &str, mac: &str) -> Result<()> {
        self.call("POST", &client_path(site_id, mac, "/reconnect"), None).map(|_| ())
    }

    /// Lists the Wi-Fi networks of every WLAN group of a site.
    pub fn ssids(&self, site_id: &str) -> Result<Vec<Ssid>> {
        #[derive(Deserialize)]
        struct Group {
            #[serde(default, rename = "wlanId", deserialize_with = "nullable")]
            id: String,
        }
        // The group list is a plain array on current controllers; a paged
        // wrapper is accepted too.
        let raw = self.call("GET", &site_path(site_id, "/wireless-network/wlans"), None)?;
        let groups: Vec<Group> = match raw {
            None => Vec::new(),
            Some(v @ Value::Array(_)) => from_value(Some(v))?,
            Some(v) => from_value::<Page<Group>>(Some(v))?.data,
        };
        let mut all = Vec::new();
        for g in groups {
            let path = site_path(site_id, &format!("/wireless-network/wlans/{}/ssids", esc(&g.id)));
            let ssids: Vec<Ssid> = self.list(&path)?;
            all.extend(ssids.into_iter().map(|mut s| {
                s.wlan_id = g.id.clone();
                s
            }));
        }
        Ok(all)
    }

    /// Fetches the password of a WPA-Personal network. It is empty when the
    /// network has none or the controller does not reveal it.
    pub fn ssid_password(&self, site_id: &str, s: &Ssid) -> Result<String> {
        #[derive(Deserialize, Default)]
        struct Psk {
            #[serde(default, rename = "securityKey", deserialize_with = "nullable")]
            key: String,
        }
        #[derive(Deserialize, Default)]
        struct Detail {
            #[serde(default, rename = "pskSetting", deserialize_with = "nullable")]
            psk: Psk,
        }
        let path = site_path(site_id, &format!("/wireless-network/wlans/{}/ssids/{}", esc(&s.wlan_id), esc(&s.id)));
        Ok(self.get::<Detail>(&path)?.psk.key)
    }

    /// Lists the newest alerts of a site raised since the given time,
    /// newest first: up to [`ALERTS_PER_STATE`] unresolved and as many
    /// resolved ones. The controller does not say with each alert whether
    /// it is resolved, so the two states are asked for separately.
    pub fn alerts(&self, site_id: &str, since: DateTime<Utc>) -> Result<Vec<Alert>> {
        let mut all = Vec::new();
        for resolved in [false, true] {
            let path = format!(
                "{}?page=1&pageSize={ALERTS_PER_STATE}&filters.timeStart={}&filters.timeEnd={}&filters.resolved={resolved}",
                site_path(site_id, "/logs/alerts"),
                since.timestamp_millis(),
                (self.now)().timestamp_millis(),
            );
            let res: Page<Alert> = self.get(&path)?;
            all.extend(res.data.into_iter().map(|mut a| {
                a.resolved = resolved;
                a
            }));
        }
        all.sort_by(|a, b| b.millis.total_cmp(&a.millis)); // stable
        Ok(all)
    }

    /// Returns the installed firmware of a device and the newest one the
    /// controller found the last time it checked.
    pub fn latest_firmware(&self, site_id: &str, mac: &str) -> Result<Firmware> {
        self.get(&device_path(site_id, mac, "/latest-firmware-info"))
    }

    /// Makes a device download and install the newest firmware. The device
    /// restarts when it is done.
    pub fn start_upgrade(&self, site_id: &str, mac: &str) -> Result<()> {
        self.call("POST", &device_path(site_id, mac, "/start-online-upgrade"), None).map(|_| ())
    }

    /// Reports whether the upgrade of a device has finished, and whether it
    /// worked.
    pub fn upgrade_status(&self, site_id: &str, mac: &str) -> Result<UpgradeResult> {
        self.get(&device_path(site_id, mac, "/online-upgrade-result"))
    }
}
