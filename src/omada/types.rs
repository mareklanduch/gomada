//! What the controller sends, as Rust types.
//!
//! Controller versions differ on details: a number may arrive as a string,
//! a field may be `null` or missing. Every field therefore decodes
//! leniently, so that one odd value never makes a whole list unreadable.

use chrono::{DateTime, TimeZone, Utc};
use serde::{Deserialize, Deserializer};
use serde_json::Value;

/// Decodes a field that may be `null`: `null` becomes the default value.
pub(crate) fn nullable<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

/// Decodes a number that the controller may send as a JSON number, as a
/// string holding a number, or not at all: 12, 12.5, "12", "", and null.
pub(crate) fn num<'de, D: Deserializer<'de>>(d: D) -> Result<f64, D::Error> {
    use serde::de::Error;
    match Value::deserialize(d)? {
        Value::Null => Ok(0.0),
        Value::Number(n) => n.as_f64().ok_or_else(|| D::Error::custom(format!("omada: {n} is not a number"))),
        Value::String(s) => {
            let s = s.trim();
            if s.is_empty() {
                return Ok(0.0);
            }
            s.parse::<f64>()
                .ok()
                .filter(|f| f.is_finite())
                .ok_or_else(|| D::Error::custom(format!("omada: {s:?} is not a number")))
        }
        other => Err(D::Error::custom(format!("omada: {other} is not a number"))),
    }
}

/// Like [`num`], for whole numbers such as a status code.
pub(crate) fn int<'de, D: Deserializer<'de>>(d: D) -> Result<i64, D::Error> {
    Ok(num(d)? as i64)
}

/// Decodes text that the controller may send as a JSON string or as a
/// number. Uptime, for example, arrives as "41day(s) 6h" from some devices
/// and as a count of seconds from others.
pub(crate) fn text<'de, D: Deserializer<'de>>(d: D) -> Result<String, D::Error> {
    use serde::de::Error;
    match Value::deserialize(d)? {
        Value::Null => Ok(String::new()),
        Value::String(s) => Ok(s),
        Value::Number(n) => Ok(n.to_string()),
        Value::Bool(b) => Ok(b.to_string()),
        other => Err(D::Error::custom(format!("omada: expected text, got {other}"))),
    }
}

fn from_millis(ms: f64) -> DateTime<Utc> {
    Utc.timestamp_millis_opt(ms as i64).single().unwrap_or(DateTime::UNIX_EPOCH)
}

/// Describes a controller. It is available without logging in.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct Info {
    #[serde(default, rename = "omadacId", deserialize_with = "text")]
    pub controller_id: String,
    #[serde(default, rename = "controllerVer", deserialize_with = "text")]
    pub version: String,
    #[serde(default, rename = "apiVer", deserialize_with = "text")]
    pub api_version: String,
}

/// One site managed by a controller.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct Site {
    #[serde(default, rename = "siteId", deserialize_with = "nullable")]
    pub id: String,
    #[serde(default, deserialize_with = "nullable")]
    pub name: String,
}

pub const TYPE_GATEWAY: &str = "gateway";
pub const TYPE_SWITCH: &str = "switch";
pub const TYPE_AP: &str = "ap";

pub const STATUS_DISCONNECTED: i64 = 0;
pub const STATUS_CONNECTED: i64 = 1;
pub const STATUS_PENDING: i64 = 2;
pub const STATUS_HEARTBEAT: i64 = 3;
pub const STATUS_ISOLATED: i64 = 4;

/// Detailed device states worth telling apart from the plain status.
pub const DETAIL_UPGRADING: i64 = 12;
pub const DETAIL_REBOOTING: i64 = 13;

/// A gateway, switch, or access point.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct Device {
    #[serde(default, deserialize_with = "nullable")]
    pub mac: String,
    #[serde(default, deserialize_with = "nullable")]
    pub name: String,
    #[serde(default, rename = "type", deserialize_with = "nullable")]
    pub kind: String,
    #[serde(default, deserialize_with = "nullable")]
    pub model: String,
    #[serde(default, deserialize_with = "nullable")]
    pub ip: String,
    #[serde(default, deserialize_with = "int")]
    pub status: i64,
    /// Refines `status`; see the `DETAIL_` constants.
    #[serde(default, rename = "detailStatus", deserialize_with = "int")]
    pub detail_status: i64,
    #[serde(default, deserialize_with = "text")]
    pub uptime: String,
    #[serde(default, rename = "cpuUtil", deserialize_with = "num")]
    pub cpu: f64,
    #[serde(default, rename = "memUtil", deserialize_with = "num")]
    pub mem: f64,
    #[serde(default, rename = "firmwareVersion", deserialize_with = "nullable")]
    pub firmware: String,
    #[serde(default, rename = "sn", deserialize_with = "nullable")]
    pub serial: String,
}

impl Device {
    /// The device's name, or its MAC address when it has no name.
    pub fn label(&self) -> &str {
        if self.name.is_empty() {
            &self.mac
        } else {
            &self.name
        }
    }

    /// Reports whether the device is connected to the controller.
    pub fn online(&self) -> bool {
        self.status == STATUS_CONNECTED
    }

    /// Describes the device status in a word or two.
    pub fn status_text(&self) -> String {
        match self.detail_status {
            DETAIL_UPGRADING => return "updating".into(),
            DETAIL_REBOOTING => return "rebooting".into(),
            _ => {}
        }
        match self.status {
            STATUS_DISCONNECTED => "offline".into(),
            STATUS_CONNECTED => "connected".into(),
            STATUS_PENDING => "pending".into(),
            STATUS_HEARTBEAT => "heartbeat missed".into(),
            STATUS_ISOLATED => "isolated".into(),
            other => format!("status {other}"),
        }
    }
}

/// A phone, laptop, or other machine connected to a site.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct NetClient {
    #[serde(default, deserialize_with = "nullable")]
    pub mac: String,
    #[serde(default, deserialize_with = "nullable")]
    pub name: String,
    #[serde(default, rename = "hostName", deserialize_with = "nullable")]
    pub host_name: String,
    #[serde(default, deserialize_with = "nullable")]
    pub ip: String,
    #[serde(default, deserialize_with = "nullable")]
    pub vendor: String,
    #[serde(default, deserialize_with = "nullable")]
    pub wireless: bool,
    #[serde(default, deserialize_with = "nullable")]
    pub ssid: String,
    #[serde(default, rename = "networkName", deserialize_with = "nullable")]
    pub network_name: String,
    #[serde(default, rename = "apName", deserialize_with = "nullable")]
    pub ap_name: String,
    #[serde(default, rename = "switchName", deserialize_with = "nullable")]
    pub switch_name: String,
    #[serde(default, rename = "gatewayName", deserialize_with = "nullable")]
    pub gateway_name: String,
    #[serde(default, deserialize_with = "num")]
    pub port: f64,
    #[serde(default, deserialize_with = "num")]
    pub rssi: f64,
    /// Seconds.
    #[serde(default, deserialize_with = "num")]
    pub uptime: f64,
    #[serde(default, rename = "trafficDown", deserialize_with = "num")]
    pub traffic_down: f64,
    #[serde(default, rename = "trafficUp", deserialize_with = "num")]
    pub traffic_up: f64,
}

impl NetClient {
    /// The client's name, falling back to its host name and then its MAC
    /// address.
    pub fn label(&self) -> &str {
        if !self.name.is_empty() {
            &self.name
        } else if !self.host_name.is_empty() {
            &self.host_name
        } else {
            &self.mac
        }
    }

    /// Describes where the client is attached: the access point and Wi-Fi
    /// network for wireless clients, the switch or gateway and port for
    /// wired ones.
    pub fn connected_to(&self) -> String {
        let (device, mut detail) = if self.wireless {
            (self.ap_name.as_str(), self.ssid.clone())
        } else if !self.switch_name.is_empty() {
            (self.switch_name.as_str(), String::new())
        } else {
            (self.gateway_name.as_str(), String::new())
        };
        if !self.wireless && self.port > 0.0 {
            detail = format!("port {}", self.port as i64);
        }
        match (device.is_empty(), detail.is_empty()) {
            (false, false) => format!("{device} · {detail}"),
            (false, true) => device.to_string(),
            _ => detail,
        }
    }
}

/// A client the controller remembers, connected or not.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct KnownClient {
    #[serde(default, deserialize_with = "nullable")]
    pub mac: String,
    #[serde(default, deserialize_with = "nullable")]
    pub name: String,
    #[serde(default, deserialize_with = "nullable")]
    pub wireless: bool,
    #[serde(default, deserialize_with = "nullable")]
    pub guest: bool,
    #[serde(default, rename = "block", deserialize_with = "nullable")]
    pub blocked: bool,
    /// When the client was last connected, in milliseconds since 1970.
    #[serde(default, rename = "lastSeen", deserialize_with = "num")]
    pub last_seen_millis: f64,
}

impl KnownClient {
    /// The client's name, or its MAC address when it has no name.
    pub fn label(&self) -> &str {
        if self.name.is_empty() {
            &self.mac
        } else {
            &self.name
        }
    }

    /// When the client was last connected, if the controller says.
    pub fn last_seen(&self) -> Option<DateTime<Utc>> {
        (self.last_seen_millis > 0.0).then(|| from_millis(self.last_seen_millis))
    }
}

pub const SECURITY_NONE: i64 = 0;
pub const SECURITY_ENTERPRISE: i64 = 2;
pub const SECURITY_PERSONAL: i64 = 3;
pub const SECURITY_PPSK: i64 = 4;
pub const SECURITY_PPSK_RADIUS: i64 = 5;

/// Bits of the band field of a Wi-Fi network.
pub const BAND_2G: i64 = 1;
pub const BAND_5G: i64 = 2;
pub const BAND_6G: i64 = 4;

/// One Wi-Fi network of a site.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct Ssid {
    /// The WLAN group the network belongs to. It is filled in by the
    /// client, not sent by the controller.
    #[serde(skip)]
    pub wlan_id: String,
    #[serde(default, rename = "ssidId", deserialize_with = "nullable")]
    pub id: String,
    #[serde(default, deserialize_with = "nullable")]
    pub name: String,
    #[serde(default, deserialize_with = "int")]
    pub band: i64,
    #[serde(default, deserialize_with = "int")]
    pub security: i64,
    #[serde(default, rename = "guestNetEnable", deserialize_with = "nullable")]
    pub guest: bool,
    #[serde(default)]
    pub broadcast: Option<bool>,
    #[serde(default, rename = "vlanEnable", deserialize_with = "nullable")]
    pub vlan_on: bool,
    #[serde(default, rename = "vlanId", deserialize_with = "num")]
    pub vlan_id: f64,
}

impl Ssid {
    /// Reports whether the network does not announce its name.
    pub fn hidden(&self) -> bool {
        self.broadcast == Some(false)
    }

    /// Describes the security mode in a word or two.
    pub fn security_text(&self) -> &'static str {
        match self.security {
            SECURITY_NONE => "open",
            SECURITY_ENTERPRISE => "WPA-Enterprise",
            SECURITY_PERSONAL => "WPA-Personal",
            SECURITY_PPSK | SECURITY_PPSK_RADIUS => "PPSK",
            _ => "unknown",
        }
    }

    /// Lists the bands the network is on: "2.4 + 5 GHz".
    pub fn band_text(&self) -> String {
        let bands: Vec<&str> = [(BAND_2G, "2.4"), (BAND_5G, "5"), (BAND_6G, "6")]
            .iter()
            .filter(|(bit, _)| self.band & bit != 0)
            .map(|(_, name)| *name)
            .collect();
        if bands.is_empty() {
            String::new()
        } else {
            bands.join(" + ") + " GHz"
        }
    }
}

/// Something the controller raised for a site.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct Alert {
    /// Text on current controllers; a number is accepted too.
    #[serde(default, deserialize_with = "text")]
    pub id: String,
    #[serde(default, deserialize_with = "nullable")]
    pub key: String,
    #[serde(default, deserialize_with = "nullable")]
    pub module: String,
    #[serde(default, deserialize_with = "nullable")]
    pub level: String,
    #[serde(default, deserialize_with = "nullable")]
    pub content: String,
    /// When the alert was raised, in milliseconds since 1970.
    #[serde(default, rename = "time", deserialize_with = "num")]
    pub millis: f64,
    /// Not sent with each alert; filled in from which of the two queries
    /// returned the alert.
    #[serde(skip)]
    pub resolved: bool,
}

impl Alert {
    /// When the alert was raised.
    pub fn time(&self) -> DateTime<Utc> {
        from_millis(self.millis)
    }
}

/// What the controller knows about the firmware of a device.
#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct Firmware {
    #[serde(default, rename = "curFwVer", deserialize_with = "nullable")]
    pub current: String,
    #[serde(default, rename = "lastFwVer", deserialize_with = "nullable")]
    pub latest: String,
    #[serde(default, rename = "fwReleaseLog", deserialize_with = "nullable")]
    pub release_notes: String,
}

impl Firmware {
    /// Reports whether a newer firmware is known.
    pub fn update_available(&self) -> bool {
        !self.latest.is_empty() && self.latest != self.current
    }
}

/// How the last firmware upgrade of a device went.
#[derive(Debug, Clone, Copy, Default, PartialEq, Deserialize)]
pub struct UpgradeResult {
    #[serde(default, deserialize_with = "nullable")]
    pub finished: bool,
    #[serde(default, deserialize_with = "nullable")]
    pub success: bool,
}

/// An Open API access token.
#[derive(Debug, Clone, PartialEq, serde::Serialize, Deserialize)]
pub struct Token {
    pub access_token: String,
    pub expires_at: DateTime<Utc>,
}

impl Default for Token {
    fn default() -> Self {
        Token { access_token: String::new(), expires_at: DateTime::UNIX_EPOCH }
    }
}

/// How long before its expiry a token stops being used, so a request never
/// leaves with a token that expires in flight.
const RENEW_MARGIN_SECS: i64 = 60;

impl Token {
    /// Reports whether the token can still be used at the given time. A
    /// token with less than a minute left counts as expired.
    pub fn valid(&self, now: DateTime<Utc>) -> bool {
        !self.access_token.is_empty() && now + chrono::Duration::seconds(RENEW_MARGIN_SECS) < self.expires_at
    }
}

/// Keeps a token between runs. The client serializes its calls.
pub trait TokenCache: Send + Sync {
    /// Returns the cached token, if there is one.
    fn load(&self) -> Option<Token>;
    /// Stores a freshly issued token.
    fn save(&self, token: &Token);
    /// Forgets the cached token.
    fn clear(&self);
}

/// The longest device or client name the controller accepts.
const MAX_NAME_LEN: usize = 128;

/// Checks a device or client name against the controller's rules, so a bad
/// name is refused with a clear message before any request.
pub fn validate_name(name: &str) -> Result<(), String> {
    if name.is_empty() {
        return Err("the name must not be empty".into());
    }
    if name.chars().count() > MAX_NAME_LEN {
        return Err(format!("the name must be at most {MAX_NAME_LEN} characters"));
    }
    if name.ends_with(' ') {
        return Err("the name must not end with a space".into());
    }
    if name.starts_with([' ', '+', '-', '@', '=']) {
        return Err("the name must not start with a space, +, -, @, or =".into());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[derive(Deserialize)]
    struct N {
        #[serde(default, deserialize_with = "num")]
        v: f64,
    }

    #[derive(Deserialize)]
    struct T {
        #[serde(default, deserialize_with = "text")]
        v: String,
    }

    #[test]
    fn num_accepts_numbers_strings_and_nothing() {
        for (json, want) in [
            (r#"{"v":12}"#, 12.0),
            (r#"{"v":12.5}"#, 12.5),
            (r#"{"v":"12"}"#, 12.0),
            (r#"{"v":" -52 "}"#, -52.0),
            (r#"{"v":""}"#, 0.0),
            (r#"{"v":null}"#, 0.0),
            (r#"{}"#, 0.0),
        ] {
            assert_eq!(serde_json::from_str::<N>(json).unwrap().v, want, "{json}");
        }
        for bad in [r#"{"v":"abc"}"#, r#"{"v":[1]}"#, r#"{"v":{}}"#, r#"{"v":true}"#, r#"{"v":"NaN"}"#] {
            assert!(serde_json::from_str::<N>(bad).is_err(), "{bad}");
        }
    }

    #[test]
    fn text_accepts_strings_numbers_and_nothing() {
        for (json, want) in [
            (r#"{"v":"41day(s) 6h"}"#, "41day(s) 6h"),
            (r#"{"v":1044000}"#, "1044000"),
            (r#"{"v":true}"#, "true"),
            (r#"{"v":null}"#, ""),
            (r#"{}"#, ""),
        ] {
            assert_eq!(serde_json::from_str::<T>(json).unwrap().v, want, "{json}");
        }
        assert!(serde_json::from_str::<T>(r#"{"v":{}}"#).is_err());
        assert!(serde_json::from_str::<T>(r#"{"v":[]}"#).is_err());
    }

    #[test]
    fn null_fields_decode_as_empty() {
        let d: Device = serde_json::from_str(r#"{"mac":"AA","name":null,"status":null,"cpuUtil":"5"}"#).unwrap();
        assert_eq!((d.label(), d.status, d.cpu), ("AA", 0, 5.0));
    }

    #[test]
    fn device_status_text() {
        let mut d = Device { status: STATUS_CONNECTED, ..Default::default() };
        assert_eq!(d.status_text(), "connected");
        assert!(d.online());
        d.detail_status = DETAIL_UPGRADING;
        assert_eq!(d.status_text(), "updating");
        d = Device { status: 9, ..Default::default() };
        assert_eq!(d.status_text(), "status 9");
        assert!(!d.online());
    }

    #[test]
    fn client_connected_to() {
        let wifi = NetClient {
            wireless: true,
            ap_name: "AP office".into(),
            ssid: "Home".into(),
            port: 3.0,
            ..Default::default()
        };
        assert_eq!(wifi.connected_to(), "AP office · Home");
        let wired = NetClient { switch_name: "Rack".into(), port: 4.0, ..Default::default() };
        assert_eq!(wired.connected_to(), "Rack · port 4");
        let gw = NetClient { gateway_name: "Core".into(), ..Default::default() };
        assert_eq!(gw.connected_to(), "Core");
        let bare = NetClient { port: 2.0, ..Default::default() };
        assert_eq!(bare.connected_to(), "port 2");
        assert_eq!(NetClient::default().connected_to(), "");
    }

    #[test]
    fn ssid_texts() {
        let s = Ssid { band: 7, security: SECURITY_PPSK_RADIUS, broadcast: Some(false), ..Default::default() };
        assert_eq!((s.band_text().as_str(), s.security_text(), s.hidden()), ("2.4 + 5 + 6 GHz", "PPSK", true));
        let s = Ssid::default();
        assert_eq!((s.band_text().as_str(), s.security_text(), s.hidden()), ("", "open", false));
    }

    #[test]
    fn firmware_update_available() {
        let f = |c: &str, l: &str| Firmware { current: c.into(), latest: l.into(), ..Default::default() };
        assert!(f("1.0", "1.1").update_available());
        assert!(!f("1.0", "1.0").update_available());
        assert!(!f("1.0", "").update_available());
    }

    #[test]
    fn token_validity_has_a_margin() {
        let now = Utc.with_ymd_and_hms(2026, 1, 1, 0, 0, 0).unwrap();
        let t = |secs| Token { access_token: "t".into(), expires_at: now + chrono::Duration::seconds(secs) };
        assert!(t(120).valid(now));
        assert!(!t(60).valid(now));
        assert!(!t(-5).valid(now));
        assert!(!Token::default().valid(now));
    }

    #[test]
    fn names_are_validated() {
        assert!(validate_name("AP office").is_ok());
        for bad in ["", "x ", " x", "+x", "-x", "@x", "=x"] {
            assert!(validate_name(bad).is_err(), "{bad:?}");
        }
        assert!(validate_name(&"é".repeat(128)).is_ok());
        assert!(validate_name(&"é".repeat(129)).is_err());
    }
}
