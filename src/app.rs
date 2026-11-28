//! The use cases shared by the command line and the terminal UI: managing
//! hosts, connecting, and acting on devices and clients. Both front ends
//! call this module and never the API client directly, so a feature behaves
//! the same in both.

use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use chrono::{DateTime, Utc};
use serde::Serialize;

use crate::config::{self, Config};
use crate::error::{Error, Result};
use crate::omada::{
    self, Alert, Client, Clock, Device, Firmware, Http, KnownClient, Logger, NetClient, Site, Ssid, TlsOptions,
    TokenCache,
};
use crate::secret::{self, Keychain};
use crate::tokencache;

/// Returns the passphrase that unlocks a host's secret when it is kept in
/// an encrypted file.
pub type PassphraseFn = Arc<dyn Fn(&str) -> Result<String> + Send + Sync>;

/// Configures an [`App`].
#[derive(Default)]
pub struct Options {
    /// The configuration file. `None` means the default path.
    pub config_path: Option<PathBuf>,
    /// Bounds each request to a controller. Zero means the default.
    pub timeout: Duration,
    /// Turns off keeping access tokens between runs.
    pub no_token_cache: bool,
    /// When set, receives one line per request to a controller.
    pub logger: Option<Logger>,
    /// `None` means the system clock.
    pub now: Option<Clock>,
    /// `None` means there is no way to ask for a passphrase.
    pub passphrase: Option<PassphraseFn>,
    /// `None` means the operating system keychain.
    pub keychain: Option<Arc<dyn Keychain>>,
}

/// The application core. It is safe for concurrent use.
pub struct App {
    path: PathBuf,
    timeout: Duration,
    no_token_cache: bool,
    logger: Option<Logger>,
    now: Clock,
    keychain: Arc<dyn Keychain>,
    passphrase: Mutex<Option<PassphraseFn>>,
    cfg: Mutex<Config>,
}

/// Describes a saved host.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct HostInfo {
    pub name: String,
    pub url: String,
    pub controller: String,
    pub client_id: String,
    pub secret_store: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub site: String,
    pub current: bool,
}

/// Describes the TLS certificate a controller presents.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Certificate {
    /// Identifies the certificate ("sha256:<hex>"). It is empty for plain
    /// http addresses.
    pub fingerprint: String,
    /// Whether the system already trusts the certificate. When false, the
    /// fingerprint must be confirmed by a person and pinned.
    pub trusted: bool,
}

/// The input to [`App::add_host`].
#[derive(Debug, Clone, Default)]
pub struct NewHost {
    pub name: String,
    pub url: String,
    pub client_id: String,
    pub secret: String,
    /// The omadacId. Empty means: ask the controller.
    pub controller_id: String,
    /// The confirmed fingerprint of an untrusted certificate.
    pub tls_pin: String,
    /// Accepts any certificate.
    pub insecure_skip_tls_verify: bool,
    /// "keyring", "file", or "env". Empty means keyring.
    pub secret_store: String,
}

/// Turns what a person types as the controller address into a base URL: it
/// adds https:// when the scheme is missing and drops any path.
pub fn normalize_url(raw: &str) -> Result<String> {
    let raw = raw.trim();
    if raw.is_empty() {
        return Err(Error::other("the controller address is empty"));
    }
    let with_scheme = if raw.contains("://") { raw.to_string() } else { format!("https://{raw}") };
    let u = url::Url::parse(&with_scheme).map_err(|e| Error::other(format!("invalid controller address: {e}")))?;
    if u.scheme() != "https" && u.scheme() != "http" {
        return Err(Error::other(format!("invalid controller address: unsupported scheme {:?}", u.scheme())));
    }
    let host = u
        .host_str()
        .filter(|h| !h.is_empty())
        .ok_or_else(|| Error::other("invalid controller address: no host name"))?;
    Ok(match u.port() {
        Some(port) => format!("{}://{host}:{port}", u.scheme()),
        None => format!("{}://{host}", u.scheme()),
    })
}

/// Rewrites a MAC address in any common notation (aa:bb:cc:dd:ee:ff,
/// AA-BB-..., aabb.ccdd.eeff, aabbccddeeff) into the controller's notation,
/// AA-BB-CC-DD-EE-FF. `None` means `s` is not a MAC address.
pub fn format_mac(s: &str) -> Option<String> {
    let hex: String = s.trim().chars().filter(|c| !matches!(c, ':' | '-' | '.')).collect::<String>().to_uppercase();
    if hex.len() != 12 || !hex.chars().all(|c| c.is_ascii_hexdigit()) {
        return None;
    }
    let pairs: Vec<&str> = (0..12).step_by(2).map(|i| &hex[i..i + 2]).collect();
    Some(pairs.join("-"))
}

fn eq_fold(a: &str, b: &str) -> bool {
    a.to_lowercase() == b.to_lowercase()
}

/// Picks the one item that `target` names: by MAC address in any notation,
/// or else by exact name, ignoring case. Several matches are an error that
/// lists them, so a person can pick by MAC address.
fn find<T: Clone>(
    kind: &str,
    target: &str,
    items: &[T],
    mac: impl Fn(&T) -> &str,
    names: impl Fn(&T) -> Vec<&str>,
    describe: impl Fn(&T) -> String,
) -> Result<T> {
    if let Some(want) = format_mac(target) {
        if let Some(item) = items.iter().find(|i| format_mac(mac(i)).as_deref() == Some(&want)) {
            return Ok(item.clone());
        }
    }
    let found: Vec<&T> =
        items.iter().filter(|i| names(i).iter().any(|n| !n.is_empty() && eq_fold(n, target))).collect();
    match found.as_slice() {
        [] => Err(Error::other(format!("no {kind} matches {target:?}"))),
        [one] => Ok((*one).clone()),
        many => Err(Error::other(format!(
            "{target:?} matches several {kind}s; use the MAC address: {}",
            many.iter().map(|i| describe(i)).collect::<Vec<_>>().join(", ")
        ))),
    }
}

/// Puts gateways first, then switches, then access points.
fn sort_devices(devs: &mut [Device]) {
    let rank = |d: &Device| match d.kind.as_str() {
        omada::TYPE_GATEWAY => 0,
        omada::TYPE_SWITCH => 1,
        omada::TYPE_AP => 2,
        _ => 3,
    };
    devs.sort_by_key(|d| (rank(d), d.label().to_lowercase()));
}

impl App {
    /// Loads the configuration and returns the application core.
    pub fn new(opts: Options) -> Result<Arc<App>> {
        let path = match opts.config_path {
            Some(p) => p,
            None => config::default_path()?,
        };
        let cfg = Config::load(&path)?;
        Ok(Arc::new(App {
            path,
            timeout: if opts.timeout.is_zero() { omada::DEFAULT_TIMEOUT } else { opts.timeout },
            no_token_cache: opts.no_token_cache,
            logger: opts.logger,
            now: opts.now.unwrap_or_else(|| Arc::new(Utc::now)),
            keychain: opts.keychain.unwrap_or_else(|| Arc::new(secret::OsKeychain)),
            passphrase: Mutex::new(opts.passphrase),
            cfg: Mutex::new(cfg),
        }))
    }

    /// Replaces the passphrase source. The terminal UI uses it to supply a
    /// passphrase it has just asked for.
    pub fn set_passphrase(&self, f: PassphraseFn) {
        *self.passphrase.lock().unwrap() = Some(f);
    }

    /// Lists the saved hosts in alphabetical order.
    pub fn hosts(&self) -> Vec<HostInfo> {
        let cfg = self.cfg.lock().unwrap();
        cfg.hosts
            .iter()
            .map(|(name, h)| HostInfo {
                name: name.clone(),
                url: h.url.clone(),
                controller: h.controller.clone(),
                client_id: h.client_id.clone(),
                secret_store: h.secret_store.clone(),
                site: h.site.clone(),
                current: *name == cfg.current,
            })
            .collect()
    }

    /// Returns the name of the current host, or "" if there is none.
    pub fn current_host(&self) -> String {
        self.cfg.lock().unwrap().current.clone()
    }

    /// Makes a host the current one.
    pub fn use_host(&self, name: &str) -> Result<()> {
        let mut cfg = self.cfg.lock().unwrap();
        if !cfg.hosts.contains_key(name) {
            return Err(unknown_host(&cfg, name));
        }
        cfg.current = name.to_string();
        cfg.save(&self.path)
    }

    /// Deletes a host together with its stored secret and cached token.
    pub fn remove_host(self: &Arc<Self>, name: &str) -> Result<()> {
        let mut cfg = self.cfg.lock().unwrap();
        let kind = match cfg.hosts.get(name) {
            Some(h) => h.secret_store.clone(),
            None => return Err(unknown_host(&cfg, name)),
        };
        if let Ok(store) = self.secret_store(name, &kind) {
            store.delete(name)?;
        }
        // The token may be in either cache, depending on how the host was
        // set up over time, so both are cleared.
        tokencache::Store::Keyring(self.keychain.clone()).delete(name);
        if let Some(dir) = tokencache::runtime_dir() {
            tokencache::Store::File(dir).delete(name);
        }
        cfg.hosts.remove(name);
        if cfg.current == name {
            cfg.current = cfg.host_names().first().cloned().unwrap_or_default();
        }
        cfg.save(&self.path)
    }

    /// Checks that `name` can be used for a new host: it is well formed and
    /// not taken.
    pub fn validate_host_name(&self, name: &str) -> Result<()> {
        config::validate_host_name(name)?;
        if self.cfg.lock().unwrap().hosts.contains_key(name) {
            return Err(Error::other(format!(
                "a host named {name:?} already exists; remove it first or pick another name"
            )));
        }
        Ok(())
    }

    /// Connects to the controller and reports its certificate.
    pub fn check_certificate(&self, raw_url: &str) -> Result<Certificate> {
        let base = normalize_url(raw_url)?;
        let (fingerprint, trusted) = omada::probe_tls(&base, self.timeout)?;
        Ok(Certificate { fingerprint, trusted })
    }

    /// Checks the credentials against the controller and, when they work,
    /// saves the host and its secret. The first host added becomes the
    /// current one. It fails with [`Error::KeyringUnavailable`] when the
    /// secret cannot be put in the system keychain; the caller can then
    /// retry with the secret store "file".
    pub fn add_host(self: &Arc<Self>, h: &NewHost) -> Result<()> {
        self.validate_host_name(&h.name)?;
        let base = normalize_url(&h.url)?;
        if h.client_id.is_empty() || h.secret.is_empty() {
            return Err(Error::other("the client ID and client secret are required"));
        }
        let kind = if h.secret_store.is_empty() { secret::KIND_KEYRING } else { h.secret_store.as_str() };
        let tls = TlsOptions { pin: h.tls_pin.clone(), insecure: h.insecure_skip_tls_verify };

        let mut controller_id = h.controller_id.clone();
        if controller_id.is_empty() {
            controller_id = match omada::fetch_info(&self.http(&tls)?, &base) {
                Ok(info) => info.controller_id,
                Err(e) if e.is_certificate() && h.tls_pin.is_empty() => {
                    return Err(Error::UntrustedCertificate(Box::new(e)))
                }
                Err(e) => return Err(e.context(format!("could not read the controller ID from {base}"))),
            };
        }
        let secret = h.secret.clone();
        let client = Client::new(omada::Options {
            base_url: base.clone(),
            controller_id: controller_id.clone(),
            client_id: h.client_id.clone(),
            secret: Some(Box::new(move || Ok(secret.clone()))),
            http: self.http(&tls)?,
            cache: None,
            now: Some(self.now.clone()),
        });
        client.login()?;

        let mut cfg = self.cfg.lock().unwrap();
        let store = self.secret_store(&h.name, kind)?;
        if let Err(e) = store.set(&h.name, &h.secret) {
            if kind == secret::KIND_KEYRING {
                return Err(Error::KeyringUnavailable(e.to_string()));
            }
            return Err(e);
        }
        cfg.hosts.insert(
            h.name.clone(),
            config::Host {
                url: base,
                controller: controller_id,
                client_id: h.client_id.clone(),
                secret_store: kind.to_string(),
                tls_pin: h.tls_pin.clone(),
                insecure_skip_tls_verify: h.insecure_skip_tls_verify,
                site: String::new(),
            },
        );
        if cfg.current.is_empty() {
            cfg.current = h.name.clone();
        }
        cfg.save(&self.path)
    }

    /// Returns the transport for one controller.
    fn http(&self, tls: &TlsOptions) -> Result<Http> {
        Http::new(tls, self.timeout, self.logger.clone())
    }

    fn now(&self) -> DateTime<Utc> {
        (self.now)()
    }

    /// Returns the store of the given kind for one host. The environment
    /// variable overrides whatever is stored.
    fn secret_store(self: &Arc<Self>, host: &str, kind: &str) -> Result<Box<dyn secret::Store>> {
        match kind {
            secret::KIND_KEYRING => Ok(Box::new(secret::EnvOverride(secret::Keyring(self.keychain.clone())))),
            secret::KIND_FILE => {
                // The source is looked up when the passphrase is needed, so
                // one set later, by the terminal UI, is still found.
                let (app, host) = (Arc::downgrade(self), host.to_string());
                let ask: secret::PassphraseFn = Box::new(move || {
                    let app = app.upgrade().ok_or(Error::PassphraseRequired)?;
                    let ask = app.passphrase.lock().unwrap().clone();
                    match ask {
                        Some(ask) => ask(&host),
                        None => Err(Error::PassphraseRequired),
                    }
                });
                let dir = self.path.parent().unwrap_or(std::path::Path::new(".")).join("secrets");
                Ok(Box::new(secret::EnvOverride(secret::File { dir, passphrase: Some(ask) })))
            }
            secret::KIND_ENV => Ok(Box::new(secret::Env)),
            other => Err(Error::other(format!(
                "unknown secret store {other:?}; use {}, {}, or {}",
                secret::KIND_KEYRING,
                secret::KIND_FILE,
                secret::KIND_ENV
            ))),
        }
    }

    /// Returns where the host's access token is kept between runs, or
    /// `None` when tokens are not cached.
    fn token_cache(&self, cfg: &Config, host: &str, h: &config::Host) -> Option<Box<dyn TokenCache>> {
        if self.no_token_cache || cfg.no_token_cache {
            return None;
        }
        let store = match h.secret_store.as_str() {
            secret::KIND_KEYRING => tokencache::Store::Keyring(self.keychain.clone()),
            secret::KIND_FILE => tokencache::Store::File(tokencache::runtime_dir()?),
            _ => return None, // env: nothing is written anywhere
        };
        Some(Box::new(tokencache::Bound { store, profile: host.to_string(), client_id: h.client_id.clone() }))
    }

    /// Prepares a session with a host. An empty name means the current
    /// host. No request is made yet: the session logs in on first use, and
    /// only if it has no valid cached token.
    pub fn connect(self: &Arc<Self>, name: &str) -> Result<Arc<Session>> {
        let cfg = self.cfg.lock().unwrap();
        let name = if name.is_empty() { cfg.current.clone() } else { name.to_string() };
        if name.is_empty() {
            if cfg.hosts.is_empty() {
                return Err(Error::NoHosts);
            }
            return Err(Error::other("no current host; choose one with: gomada host use NAME"));
        }
        let h = cfg.hosts.get(&name).ok_or_else(|| unknown_host(&cfg, &name))?;
        let store = self.secret_store(&name, &h.secret_store).map_err(|e| e.context(format!("host {name:?}")))?;

        // The secret is read at most once per session and then kept in
        // memory, so renewing a token never asks for a passphrase again. A
        // failed read is not remembered, so it can be retried.
        let once = Arc::new(SecretOnce { store, profile: name.clone(), cached: Mutex::new(String::new()) });
        let secret = once.clone();
        let client = Client::new(omada::Options {
            base_url: h.url.clone(),
            controller_id: h.controller.clone(),
            client_id: h.client_id.clone(),
            secret: Some(Box::new(move || secret.get())),
            http: self.http(&TlsOptions { pin: h.tls_pin.clone(), insecure: h.insecure_skip_tls_verify })?,
            cache: self.token_cache(&cfg, &name, h),
            now: Some(self.now.clone()),
        });
        Ok(Arc::new(Session {
            app: self.clone(),
            name,
            url: h.url.clone(),
            client,
            secret: once,
            firmware: Mutex::new(HashMap::new()),
        }))
    }
}

fn unknown_host(cfg: &Config, name: &str) -> Error {
    let names = cfg.host_names();
    if names.is_empty() {
        return Error::NoHosts;
    }
    Error::other(format!("unknown host {name:?}; known hosts: {}", names.join(", ")))
}

struct SecretOnce {
    store: Box<dyn secret::Store>,
    profile: String,
    cached: Mutex<String>,
}

impl SecretOnce {
    fn get(&self) -> Result<String> {
        let mut cached = self.cached.lock().unwrap();
        if cached.is_empty() {
            *cached = self.store.get(&self.profile)?;
        }
        Ok(cached.clone())
    }
}

/// How far back alerts are listed.
const ALERT_WINDOW_DAYS: i64 = 30;

/// How long firmware information is reused before the controller is asked
/// again. Updates appear rarely, and asking costs one request per device.
const FIRMWARE_TTL_SECS: i64 = 600;

/// A connection to one host.
pub struct Session {
    app: Arc<App>,
    name: String,
    url: String,
    client: Client,
    secret: Arc<SecretOnce>,
    /// By device MAC, with when it was fetched.
    firmware: Mutex<HashMap<String, (Firmware, DateTime<Utc>)>>,
}

impl Session {
    /// Returns the name of the host this session talks to.
    pub fn host(&self) -> &str {
        &self.name
    }

    /// Returns the controller address.
    pub fn url(&self) -> &str {
        &self.url
    }

    /// Reads the client secret now. The terminal UI calls it at start so
    /// that a later token renewal never has to stop and ask for a
    /// passphrase.
    pub fn unlock(&self) -> Result<()> {
        self.secret.get().map(|_| ())
    }

    /// Lists the sites of the host.
    pub fn sites(&self) -> Result<Vec<Site>> {
        self.client.sites()
    }

    /// Finds the site to work on. `want` is a site name or ID; when it is
    /// empty the only site is used, or else the site used last on this
    /// host.
    pub fn resolve_site(&self, want: &str) -> Result<Site> {
        self.choose_site(&self.sites()?, want)
    }

    /// Is [`Session::resolve_site`] for a site list that was already
    /// fetched.
    pub fn choose_site(&self, sites: &[Site], want: &str) -> Result<Site> {
        let names = || sites.iter().map(|s| s.name.as_str()).collect::<Vec<_>>().join(", ");
        if want.is_empty() {
            match sites {
                [] => return Err(Error::other("this host has no sites the application may access")),
                [only] => return Ok(only.clone()),
                _ => {}
            }
            let last = self.last_site();
            if !last.is_empty() {
                if let Ok(site) = match_site(sites, &last) {
                    return Ok(site);
                }
            }
            return Err(Error::SiteRequired { sites: names() });
        }
        match_site(sites, want).map_err(|e| Error::other(format!("{e} (sites: {})", names())))
    }

    /// Returns the name of the site used last on this host.
    pub fn last_site(&self) -> String {
        let cfg = self.app.cfg.lock().unwrap();
        cfg.hosts.get(&self.name).map(|h| h.site.clone()).unwrap_or_default()
    }

    /// Records the site as the one used last on this host.
    pub fn remember_site(&self, site: &Site) -> Result<()> {
        let mut cfg = self.app.cfg.lock().unwrap();
        match cfg.hosts.get_mut(&self.name) {
            Some(h) if h.site != site.name => h.site = site.name.clone(),
            _ => return Ok(()),
        }
        cfg.save(&self.app.path)
    }

    /// Lists the devices of a site, sorted by type and then name.
    pub fn devices(&self, site_id: &str) -> Result<Vec<Device>> {
        let mut devs = self.client.devices(site_id)?;
        sort_devices(&mut devs);
        Ok(devs)
    }

    /// Finds one device by MAC address or name.
    pub fn find_device(&self, site_id: &str, target: &str) -> Result<Device> {
        find(
            "device",
            target,
            &self.devices(site_id)?,
            |d| &d.mac,
            |d| vec![&d.name],
            |d| format!("{} ({})", d.label(), d.mac),
        )
    }

    /// Restarts a device.
    pub fn reboot_device(&self, site_id: &str, d: &Device) -> Result<()> {
        self.client.reboot_device(site_id, &d.mac)
    }

    /// Sets the name of a device.
    pub fn rename_device(&self, site_id: &str, d: &Device, name: &str) -> Result<()> {
        self.client.rename_device(site_id, d, name)
    }

    /// Lists the connected clients of a site, sorted by name.
    pub fn clients(&self, site_id: &str) -> Result<Vec<NetClient>> {
        let mut clients = self.client.clients(site_id)?;
        clients.sort_by_key(|c| c.label().to_lowercase());
        Ok(clients)
    }

    /// Finds one connected client by MAC address, name, or host name.
    pub fn find_client(&self, site_id: &str, target: &str) -> Result<NetClient> {
        find(
            "client",
            target,
            &self.clients(site_id)?,
            |c| &c.mac,
            |c| vec![&c.name, &c.host_name],
            |c| format!("{} ({})", c.label(), c.mac),
        )
    }

    /// Sets the friendly name of a client.
    pub fn rename_client(&self, site_id: &str, c: &NetClient, name: &str) -> Result<()> {
        self.client.rename_client(site_id, &c.mac, name)
    }

    /// Bars a client from the network.
    pub fn block_client(&self, site_id: &str, c: &NetClient) -> Result<()> {
        self.client.block_client(site_id, &c.mac)
    }

    /// Lets a blocked client back on the network. A blocked client is not
    /// connected and so cannot be looked up by name: `target` must be its
    /// MAC address.
    pub fn unblock_client(&self, site_id: &str, target: &str) -> Result<()> {
        let mac = format_mac(target).ok_or_else(|| {
            Error::other(format!(
                "{target:?} is not a MAC address; a blocked client is offline, so it can only be unblocked by MAC address"
            ))
        })?;
        self.client.unblock_client(site_id, &mac)
    }

    /// Forces a wireless client to re-associate.
    pub fn reconnect_client(&self, site_id: &str, c: &NetClient) -> Result<()> {
        if !c.wireless {
            return Err(Error::other(format!(
                "{} is a wired client; only wireless clients can be reconnected",
                c.label()
            )));
        }
        self.client.reconnect_client(site_id, &c.mac)
    }

    /// Lists the clients barred from a site, sorted by name.
    pub fn blocked_clients(&self, site_id: &str) -> Result<Vec<KnownClient>> {
        let mut blocked = self.client.blocked_clients(site_id)?;
        blocked.sort_by_key(|k| k.label().to_lowercase());
        Ok(blocked)
    }

    /// Lists the Wi-Fi networks of a site, sorted by name.
    pub fn ssids(&self, site_id: &str) -> Result<Vec<Ssid>> {
        let mut ssids = self.client.ssids(site_id)?;
        ssids.sort_by_key(|s| s.name.to_lowercase());
        Ok(ssids)
    }

    /// Finds one Wi-Fi network by name, ignoring case, or by ID.
    pub fn find_ssid(&self, site_id: &str, target: &str) -> Result<Ssid> {
        let ssids = self.ssids(site_id)?;
        if let Some(by_id) = ssids.iter().find(|s| s.id == target) {
            return Ok(by_id.clone());
        }
        let found: Vec<&Ssid> = ssids.iter().filter(|s| eq_fold(&s.name, target)).collect();
        match found.as_slice() {
            [] => Err(Error::other(format!("no Wi-Fi network matches {target:?}"))),
            [one] => Ok((*one).clone()),
            _ => Err(Error::other(format!(
                "several Wi-Fi networks are named {target:?}; use the network ID from 'gomada wifi list -o json'"
            ))),
        }
    }

    /// Fetches the password of a Wi-Fi network. Networks without a shared
    /// password have none, and that is not an error.
    pub fn wifi_password(&self, site_id: &str, ssid: &Ssid) -> Result<String> {
        if ssid.security != omada::SECURITY_PERSONAL {
            return Ok(String::new());
        }
        self.client.ssid_password(site_id, ssid)
    }

    /// Lists the alerts of a site from the last 30 days, newest first.
    pub fn alerts(&self, site_id: &str) -> Result<Vec<Alert>> {
        self.client.alerts(site_id, self.app.now() - chrono::Duration::days(ALERT_WINDOW_DAYS))
    }

    /// Finds one alert by its ID.
    pub fn find_alert(&self, site_id: &str, id: &str) -> Result<Alert> {
        self.alerts(site_id)?
            .into_iter()
            .find(|a| a.id == id)
            .ok_or_else(|| Error::other(format!("no alert with ID {id:?} in the last 30 days")))
    }

    /// Returns the installed and the newest known firmware of a device,
    /// asking the controller.
    pub fn firmware(&self, site_id: &str, d: &Device) -> Result<Firmware> {
        let info = self.client.latest_firmware(site_id, &d.mac)?;
        self.firmware.lock().unwrap().insert(d.mac.clone(), (info.clone(), self.app.now()));
        Ok(info)
    }

    /// Returns the firmware information of a device if it was fetched
    /// recently, without asking the controller.
    pub fn cached_firmware(&self, mac: &str) -> Option<Firmware> {
        let cache = self.firmware.lock().unwrap();
        let (info, fetched) = cache.get(mac)?;
        ((self.app.now() - *fetched).num_seconds() <= FIRMWARE_TTL_SECS).then(|| info.clone())
    }

    /// Fetches the firmware information of every connected device that has
    /// none cached. It reports whether anything was fetched. A device that
    /// cannot be asked is skipped; the others still get their answer.
    pub fn refresh_firmware(&self, site_id: &str, devs: &[Device]) -> bool {
        let mut fetched = false;
        for d in devs.iter().filter(|d| d.online()) {
            if self.cached_firmware(&d.mac).is_none() && self.firmware(site_id, d).is_ok() {
                fetched = true;
            }
        }
        fetched
    }

    /// Starts installing the newest firmware on a device and returns what
    /// is being installed. The device restarts when it is done. It fails
    /// with [`Error::UpToDate`] when there is nothing newer.
    pub fn upgrade_device(&self, site_id: &str, d: &Device) -> Result<Firmware> {
        if !d.online() {
            return Err(Error::other(format!(
                "{} is {}; only a connected device can be updated",
                d.label(),
                d.status_text()
            )));
        }
        let info = self.firmware(site_id, d)?;
        if !info.update_available() {
            return Err(Error::UpToDate { label: d.label().to_string(), current: info.current });
        }
        self.client.start_upgrade(site_id, &d.mac)?;
        self.firmware.lock().unwrap().remove(&d.mac); // what is cached is about to be out of date
        Ok(info)
    }

    /// Asks every `interval` whether the upgrade of a device has finished,
    /// until it has or `limit` has passed. It reports whether the upgrade
    /// worked.
    pub fn wait_for_upgrade(&self, site_id: &str, d: &Device, interval: Duration, limit: Duration) -> Result<bool> {
        let deadline = Instant::now() + limit;
        loop {
            if Instant::now() + interval > deadline {
                return Err(Error::DeadlineExceeded);
            }
            std::thread::sleep(interval);
            // The device restarts during an upgrade and the controller may
            // not answer for it for a moment, so errors are not final.
            if let Ok(r) = self.client.upgrade_status(site_id, &d.mac) {
                if r.finished {
                    return Ok(r.success);
                }
            }
        }
    }
}

fn match_site(sites: &[Site], want: &str) -> std::result::Result<Site, String> {
    if let Some(by_id) = sites.iter().find(|s| s.id == want) {
        return Ok(by_id.clone());
    }
    let found: Vec<&Site> = sites.iter().filter(|s| eq_fold(&s.name, want)).collect();
    match found.as_slice() {
        [] => Err(format!("no site named {want:?}")),
        [one] => Ok((*one).clone()),
        _ => Err(format!("several sites are named {want:?}; use the site ID")),
    }
}

/// Builds the text of the QR code that phones join a network from. It fails
/// with [`Error::NoQr`] for networks that have no shared password to put in
/// a code, and when the controller did not reveal the password.
pub fn wifi_qr_payload(ssid: &Ssid, password: &str) -> Result<String> {
    let esc = |s: &str| {
        let mut out = String::with_capacity(s.len());
        for c in s.chars() {
            if matches!(c, '\\' | ';' | ',' | ':' | '"') {
                out.push('\\');
            }
            out.push(c);
        }
        out
    };
    let hidden = if ssid.hidden() { "H:true;" } else { "" };
    match ssid.security {
        omada::SECURITY_NONE => Ok(format!("WIFI:T:nopass;S:{};{hidden};", esc(&ssid.name))),
        omada::SECURITY_PERSONAL if password.is_empty() => Err(Error::NoQr(
            "the controller did not reveal the password; the Open API application may not be allowed to read it".into(),
        )),
        omada::SECURITY_PERSONAL => Ok(format!("WIFI:T:WPA;S:{};P:{};{hidden};", esc(&ssid.name), esc(password))),
        _ => Err(Error::NoQr(format!(
            "{} networks use a login per person, not one shared password",
            ssid.security_text()
        ))),
    }
}

/// Counts the alerts that are not resolved.
pub fn unresolved(alerts: &[Alert]) -> usize {
    alerts.iter().filter(|a| !a.resolved).count()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn urls_are_normalized() {
        for (raw, want) in [
            ("192.168.0.2:8043", "https://192.168.0.2:8043"),
            (" https://omada.example.net/login?x=1 ", "https://omada.example.net"),
            ("http://controller:8088/", "http://controller:8088"),
            ("HTTPS://Omada.Example.NET", "https://omada.example.net"),
            ("[::1]:8043", "https://[::1]:8043"),
        ] {
            assert_eq!(normalize_url(raw).unwrap(), want, "{raw}");
        }
        for bad in ["", "   ", "ftp://x", "https://", "http://:80"] {
            assert!(normalize_url(bad).is_err(), "{bad:?}");
        }
    }

    #[test]
    fn mac_addresses_in_any_notation() {
        for raw in ["aa:bb:cc:dd:ee:ff", "AA-BB-CC-DD-EE-FF", "aabb.ccdd.eeff", " aabbccddeeff "] {
            assert_eq!(format_mac(raw).as_deref(), Some("AA-BB-CC-DD-EE-FF"), "{raw}");
        }
        for bad in ["", "aa:bb", "gg:bb:cc:dd:ee:ff", "aabbccddeeff00", "Laptop", "ääääää"] {
            assert_eq!(format_mac(bad), None, "{bad}");
        }
    }

    fn dev(mac: &str, name: &str, kind: &str) -> Device {
        Device { mac: mac.into(), name: name.into(), kind: kind.into(), ..Default::default() }
    }

    #[test]
    fn find_by_mac_or_name() {
        let devs = [
            dev("AA-00-00-00-00-01", "Core", "gateway"),
            dev("AA-00-00-00-00-02", "AP", "ap"),
            dev("AA-00-00-00-00-03", "ap", "ap"),
        ];
        let look = |target: &str| {
            find("device", target, &devs, |d| &d.mac, |d| vec![&d.name], |d| format!("{} ({})", d.label(), d.mac))
        };
        assert_eq!(look("aa:00:00:00:00:02").unwrap().name, "AP");
        assert_eq!(look("core").unwrap().mac, "AA-00-00-00-00-01");
        assert_eq!(look("nope").unwrap_err().to_string(), "no device matches \"nope\"");
        assert_eq!(
            look("AP").unwrap_err().to_string(),
            "\"AP\" matches several devices; use the MAC address: AP (AA-00-00-00-00-02), ap (AA-00-00-00-00-03)"
        );
    }

    #[test]
    fn devices_sort_by_type_then_name() {
        let mut devs = vec![
            dev("4", "b-ap", "ap"),
            dev("3", "", "other"),
            dev("2", "Switch", "switch"),
            dev("1", "A-ap", "ap"),
            dev("0", "gw", "gateway"),
        ];
        sort_devices(&mut devs);
        assert_eq!(devs.iter().map(|d| d.mac.as_str()).collect::<String>(), "02143");
    }

    #[test]
    fn qr_payloads() {
        let ssid =
            |name: &str, security, broadcast| Ssid { name: name.into(), security, broadcast, ..Default::default() };
        assert_eq!(
            wifi_qr_payload(&ssid("Home-5G", omada::SECURITY_PERSONAL, Some(true)), "correct horse").unwrap(),
            "WIFI:T:WPA;S:Home-5G;P:correct horse;;"
        );
        assert_eq!(
            wifi_qr_payload(&ssid("I;o:T", omada::SECURITY_PERSONAL, Some(false)), r#"a\b,c"d"#).unwrap(),
            r#"WIFI:T:WPA;S:I\;o\:T;P:a\\b\,c\"d;H:true;;"#
        );
        assert_eq!(
            wifi_qr_payload(&ssid("Guests", omada::SECURITY_NONE, None), "").unwrap(),
            "WIFI:T:nopass;S:Guests;;"
        );
        let err = wifi_qr_payload(&ssid("Home", omada::SECURITY_PERSONAL, None), "").unwrap_err();
        assert!(matches!(err, Error::NoQr(_)) && err.to_string().contains("did not reveal"), "{err}");
        let err = wifi_qr_payload(&ssid("Staff", omada::SECURITY_ENTERPRISE, None), "").unwrap_err();
        assert_eq!(
            err.to_string(),
            "no QR code for this network: WPA-Enterprise networks use a login per person, not one shared password"
        );
    }

    #[test]
    fn site_matching() {
        let sites = [
            Site { id: "s1".into(), name: "Default".into() },
            Site { id: "s2".into(), name: "Workshop".into() },
            Site { id: "s3".into(), name: "workshop".into() },
        ];
        assert_eq!(match_site(&sites, "default").unwrap().id, "s1");
        assert_eq!(match_site(&sites, "s3").unwrap().name, "workshop");
        assert!(match_site(&sites, "Workshop").unwrap_err().contains("several sites"));
        assert!(match_site(&sites, "nope").unwrap_err().contains("no site named"));
    }
}
