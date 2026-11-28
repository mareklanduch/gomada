//! The API client and the application core against servers that behave in
//! particular ways: self-signed certificates, expiring tokens, addresses
//! that are not a controller.

mod support;

use std::io::{Read, Write};
use std::net::TcpListener;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use chrono::{DateTime, Utc};
use gomada::app::{self, App, NewHost};
use gomada::error::Error;
use gomada::omada::{self, Client, Http, TlsOptions, Token, TokenCache};
use gomada::qr::Code;
use gomada::secret::MemoryKeychain;
use support::{epoch, fake};

fn http() -> Http {
    Http::new(&TlsOptions::default(), Duration::from_secs(5), None).unwrap()
}

/// A clock that tests move by hand.
#[derive(Clone)]
struct Clock(Arc<Mutex<DateTime<Utc>>>);

impl Clock {
    fn new() -> Clock {
        Clock(Arc::new(Mutex::new(epoch())))
    }

    fn advance(&self, secs: i64) {
        *self.0.lock().unwrap() += chrono::Duration::seconds(secs);
    }

    fn source(&self) -> omada::Clock {
        let at = self.0.clone();
        Arc::new(move || *at.lock().unwrap())
    }
}

fn client(c: &fake::Controller, secret: &str, clock: &Clock, cache: Option<Box<dyn TokenCache>>) -> Client {
    let secret = secret.to_string();
    Client::new(omada::Options {
        base_url: format!("{}/", c.url), // a trailing slash is tolerated
        controller_id: fake::CONTROLLER_ID.into(),
        client_id: fake::CLIENT_ID.into(),
        secret: Some(Box::new(move || Ok(secret.clone()))),
        http: http(),
        cache,
        now: Some(clock.source()),
    })
}

#[test]
fn fetch_info() {
    let c = fake::Controller::start();
    let info = omada::fetch_info(&http(), &c.url).unwrap();
    assert_eq!((info.controller_id.as_str(), info.version.as_str()), (fake::CONTROLLER_ID, "5.15.20.1"));
}

#[test]
fn login_once_and_renew_before_expiry() {
    let c = fake::Controller::start();
    let clock = Clock::new();
    let cl = client(&c, fake::SECRET, &clock, None);
    assert_eq!(c.logins(), 0, "nothing happens before the first request");
    assert_eq!(cl.sites().unwrap().len(), 2);
    cl.devices("site-1").unwrap();
    cl.login().unwrap();
    assert_eq!(c.logins(), 1);

    // With a minute left the token is replaced before it can expire in
    // flight.
    clock.advance(7200 - 61);
    cl.sites().unwrap();
    assert_eq!(c.logins(), 1);
    clock.advance(2);
    cl.sites().unwrap();
    assert_eq!(c.logins(), 2);
}

#[test]
fn wrong_secret_and_missing_secret() {
    let c = fake::Controller::start();
    let err = client(&c, "wrong", &Clock::new(), None).sites().unwrap_err();
    assert!(err.is_auth(), "{err}");
    assert_eq!(err.to_string(), "authentication failed: The client id or client secret is invalid. (code -44106)");

    let no_secret = Client::new(omada::Options {
        base_url: c.url.clone(),
        controller_id: fake::CONTROLLER_ID.into(),
        client_id: fake::CLIENT_ID.into(),
        secret: Some(Box::new(|| Err(Error::PassphraseRequired))),
        http: http(),
        cache: None,
        now: None,
    });
    assert!(no_secret.sites().unwrap_err().is_passphrase_required(), "the reason the secret is missing is kept");
}

#[test]
fn rejected_token_is_replaced_once() {
    let c = fake::Controller::start();
    let cl = client(&c, fake::SECRET, &Clock::new(), None);
    cl.sites().unwrap();
    c.expire_tokens();
    cl.sites().unwrap();
    assert_eq!(c.logins(), 2);
}

/// A token cache in memory that counts what happens to it.
#[derive(Default)]
struct MemoryCache {
    token: Mutex<Option<Token>>,
    saves: Mutex<usize>,
}

/// A handle to a cache that several clients share, as runs of the program
/// share the keychain.
#[derive(Clone, Default)]
struct Shared(Arc<MemoryCache>);

impl std::ops::Deref for Shared {
    type Target = MemoryCache;

    fn deref(&self) -> &MemoryCache {
        &self.0
    }
}

impl TokenCache for Shared {
    fn load(&self) -> Option<Token> {
        self.token.lock().unwrap().clone()
    }

    fn save(&self, token: &Token) {
        *self.token.lock().unwrap() = Some(token.clone());
        *self.saves.lock().unwrap() += 1;
    }

    fn clear(&self) {
        *self.token.lock().unwrap() = None;
    }
}

#[test]
fn cached_tokens() {
    let c = fake::Controller::start();
    let clock = Clock::new();
    let cache = Shared::default();

    // The first run logs in and fills the cache.
    client(&c, fake::SECRET, &clock, Some(Box::new(cache.clone()))).sites().unwrap();
    assert_eq!((c.logins(), *cache.saves.lock().unwrap()), (1, 1));

    // The next run needs no login, and not even the secret.
    let no_secret = Client::new(omada::Options {
        base_url: c.url.clone(),
        controller_id: fake::CONTROLLER_ID.into(),
        client_id: fake::CLIENT_ID.into(),
        secret: Some(Box::new(|| panic!("the secret was asked for"))),
        http: http(),
        cache: Some(Box::new(cache.clone())),
        now: Some(clock.source()),
    });
    no_secret.sites().unwrap();
    assert_eq!(c.logins(), 1);

    // An expired cached token is replaced.
    clock.advance(7200);
    client(&c, fake::SECRET, &clock, Some(Box::new(cache.clone()))).sites().unwrap();
    assert_eq!(c.logins(), 2);

    // A cached token the controller rejects is cleared and replaced, once.
    c.expire_tokens();
    client(&c, fake::SECRET, &clock, Some(Box::new(cache.clone()))).sites().unwrap();
    assert_eq!(c.logins(), 3);
    assert_eq!(cache.load().unwrap().access_token, "AT-3");
}

#[test]
fn paging_and_decoding_quirks() {
    let c = fake::Controller::start();
    c.with(|s| s.max_page_size = 2);
    let cl = client(&c, fake::SECRET, &Clock::new(), None);
    let devices = cl.devices("site-1").unwrap();
    assert_eq!(devices.len(), 4);
    // Numbers as strings, uptime as text or as a count, missing fields.
    let ap = devices.iter().find(|d| d.name == "AP office").unwrap();
    assert_eq!((ap.cpu, ap.mem, ap.uptime.as_str()), (5.0, 52.0, "1044000"));
    let offline = devices.iter().find(|d| d.name == "AP garage").unwrap();
    assert_eq!((offline.online(), offline.uptime.as_str(), offline.cpu), (false, "", 0.0));
    let clients = cl.clients("site-1").unwrap();
    assert_eq!(clients.iter().find(|n| n.name == "NAS").unwrap().uptime, 3_564_000.0);
    assert!(cl.clients("site-2").unwrap().is_empty());

    let err = cl.devices("nope").unwrap_err();
    assert_eq!(err.to_string(), "This site does not exist. (code -33004)");

    // Wi-Fi networks across several groups, each paged.
    c.with(|s| {
        s.wlans.get_mut("site-1").unwrap().push(serde_json::json!({"wlanId": "wlan-2"}));
        s.ssids
            .insert("wlan-2".into(), vec![serde_json::json!({"ssidId": "x", "name": "Second group", "security": 0})]);
    });
    let ssids = cl.ssids("site-1").unwrap();
    assert_eq!(ssids.len(), 5);
    assert_eq!(ssids.last().unwrap().wlan_id, "wlan-2");
    assert_eq!(cl.ssid_password("site-1", &ssids[0]).unwrap(), "correct horse battery");
}

#[test]
fn alerts_are_split_by_state_and_sorted() {
    let c = fake::Controller::start();
    let clock = Clock::new();
    let cl = client(&c, fake::SECRET, &clock, None);
    let alerts = cl.alerts("site-1", epoch() - chrono::Duration::days(30)).unwrap();
    let ids: Vec<&str> = alerts.iter().map(|a| a.id.as_str()).collect();
    assert_eq!(ids, ["alert-1", "alert-2", "alert-3"], "newest first, the old one left out");
    assert_eq!(alerts.iter().map(|a| a.resolved).collect::<Vec<_>>(), [false, false, true]);
    assert_eq!(app::unresolved(&alerts), 2);
    assert!(c.with(|s| s.calls.len()) == 2, "one request per state");
}

/// Starts a server that answers every request with `status` and `body`.
fn canned(status: u16, body: &'static str) -> (String, Arc<tiny_http::Server>) {
    let server = Arc::new(tiny_http::Server::http("127.0.0.1:0").unwrap());
    let url = format!("http://{}", server.server_addr().to_ip().unwrap());
    let srv = server.clone();
    std::thread::spawn(move || {
        for req in srv.incoming_requests() {
            let _ = req.respond(tiny_http::Response::from_string(body).with_status_code(status));
        }
    });
    (url, server)
}

#[test]
fn addresses_that_are_not_a_controller() {
    let (url, _server) = canned(200, r#"{"errorCode":0,"result":{}}"#);
    let err = omada::fetch_info(&http(), &url).unwrap_err();
    assert!(err.to_string().contains("not like an Omada controller"), "{err}");

    let (url, _server) = canned(200, "<html>hello</html>");
    let err = omada::fetch_info(&http(), &url).unwrap_err();
    assert!(err.to_string().contains("not JSON"), "{err}");

    // A controller without the Open API answers the login with a 404.
    let (url, _server) = canned(404, "404 page not found");
    let cl = Client::new(omada::Options {
        base_url: url,
        controller_id: "x".into(),
        client_id: "x".into(),
        secret: Some(Box::new(|| Ok("x".into()))),
        http: http(),
        cache: None,
        now: None,
    });
    let err = cl.sites().unwrap_err();
    assert!(err.to_string().contains("has no Open API at this address (HTTP 404)"), "{err}");
    assert!(!err.is_auth(), "a wrong address is not a wrong secret");

    // HTTP 401 counts as a rejected token: one more login, then give up.
    let (url, _server) = canned(401, r#"{"errorCode":0,"msg":"Unauthorized"}"#);
    let err = omada::fetch_info(&http(), &url).unwrap_err();
    assert_eq!(err.api().unwrap().http_status, 401);
}

#[test]
fn unreachable() {
    let err = omada::fetch_info(
        &Http::new(&TlsOptions::default(), Duration::from_secs(2), None).unwrap(),
        "http://127.0.0.1:1",
    )
    .unwrap_err();
    assert!(err.is_unreachable() && !err.is_certificate(), "{err}");
    let err = omada::probe_tls("https://127.0.0.1:1", Duration::from_secs(2)).unwrap_err();
    assert!(err.is_unreachable(), "{err}");
}

/// What the self-signed server answers to everything: enough to pass for
/// the controller's info, a login, and an empty list.
const TLS_ANSWER: &str = r#"{"errorCode":0,"msg":"Success.","result":{"omadacId":"fake-omadac-id","accessToken":"AT-1","expiresIn":7200,"totalRows":0,"data":[]}}"#;

/// Starts an HTTPS server with a certificate no system trusts, like a
/// typical controller. Returns its address and the certificate's
/// fingerprint.
fn self_signed_server() -> (String, String) {
    use rustls::pki_types::{PrivateKeyDer, PrivatePkcs8KeyDer};
    let cert = rcgen::generate_simple_self_signed(vec!["localhost".to_string(), "127.0.0.1".to_string()]).unwrap();
    let der = cert.cert.der().clone();
    let fingerprint = omada::fingerprint(der.as_ref());
    let key = PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(cert.key_pair.serialize_der()));
    let config = rustls::ServerConfig::builder_with_provider(Arc::new(rustls::crypto::ring::default_provider()))
        .with_safe_default_protocol_versions()
        .unwrap()
        .with_no_client_auth()
        .with_single_cert(vec![der], key)
        .unwrap();
    let config = Arc::new(config);
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let url = format!("https://127.0.0.1:{}", listener.local_addr().unwrap().port());
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(stream) = stream else { continue };
            let config = config.clone();
            std::thread::spawn(move || {
                let Ok(conn) = rustls::ServerConnection::new(config) else { return };
                let mut tls = rustls::StreamOwned::new(conn, stream);
                // Read the request head, and the body if it has one.
                let mut seen = Vec::new();
                let mut buf = [0u8; 2048];
                loop {
                    match tls.read(&mut buf) {
                        Ok(0) | Err(_) => return, // a client that only looked at the certificate
                        Ok(n) => seen.extend_from_slice(&buf[..n]),
                    }
                    let text = String::from_utf8_lossy(&seen).to_lowercase();
                    if let Some(head_end) = text.find("\r\n\r\n") {
                        let length: usize = text
                            .lines()
                            .find_map(|l| l.strip_prefix("content-length:"))
                            .and_then(|v| v.trim().parse().ok())
                            .unwrap_or(0);
                        if seen.len() >= head_end + 4 + length {
                            break;
                        }
                    }
                }
                let _ = write!(
                    tls,
                    "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{TLS_ANSWER}",
                    TLS_ANSWER.len()
                );
                let _ = tls.flush();
                tls.conn.send_close_notify();
                let _ = tls.flush();
            });
        }
    });
    (url, fingerprint)
}

fn tls_http(pin: &str, insecure: bool) -> Http {
    Http::new(&TlsOptions { pin: pin.into(), insecure }, Duration::from_secs(5), None).unwrap()
}

#[test]
fn self_signed_certificates() {
    let (url, fingerprint) = self_signed_server();
    assert!(fingerprint.starts_with("sha256:") && fingerprint.len() == 7 + 64, "{fingerprint}");

    // Looking at the certificate: its fingerprint, and that nobody vouches
    // for it.
    assert_eq!(omada::probe_tls(&url, Duration::from_secs(5)).unwrap(), (fingerprint.clone(), false));

    // Without a pin it is refused.
    let err = omada::fetch_info(&tls_http("", false), &url).unwrap_err();
    assert!(err.is_certificate() && !err.is_unreachable() && !err.is_pin_mismatch(), "{err}");

    // With its pin it is accepted, again and again.
    let pinned = tls_http(&fingerprint, false);
    for _ in 0..2 {
        assert_eq!(omada::fetch_info(&pinned, &url).unwrap().controller_id, fake::CONTROLLER_ID);
    }

    // Another certificate than the pinned one is refused, and the error
    // names what was presented instead.
    let other = format!("sha256:{}", "0".repeat(64));
    let err = omada::fetch_info(&tls_http(&other, false), &url).unwrap_err();
    assert_eq!(err, Error::PinMismatch { got: fingerprint.clone() });
    assert!(err.is_certificate() && err.is_pin_mismatch());
    assert!(err.to_string().contains("the controller's certificate changed; it now presents sha256:"), "{err}");

    // The emergency switch accepts anything.
    omada::fetch_info(&tls_http("", true), &url).unwrap();
}

#[test]
fn adding_a_host_with_a_self_signed_certificate() {
    let (url, fingerprint) = self_signed_server();
    let dir = tempfile::tempdir().unwrap();
    let keychain = Arc::new(MemoryKeychain::default());
    let app = App::new(app::Options {
        config_path: Some(dir.path().join("config.yaml")),
        keychain: Some(keychain.clone()),
        timeout: Duration::from_secs(5),
        ..Default::default()
    })
    .unwrap();

    let cert = app.check_certificate(&url).unwrap();
    assert_eq!(cert, app::Certificate { fingerprint: fingerprint.clone(), trusted: false });

    let mut host = NewHost {
        name: "home".into(),
        url: url.clone(),
        client_id: "id".into(),
        secret: "s".into(),
        ..Default::default()
    };
    // Without the confirmed pin nothing is saved, and the error says why.
    let err = app.add_host(&host).unwrap_err();
    assert!(matches!(err, Error::UntrustedCertificate(_)) && err.is_certificate(), "{err}");
    assert!(app.hosts().is_empty() && keychain.values.lock().unwrap().is_empty());

    host.tls_pin = fingerprint.clone();
    app.add_host(&host).unwrap();
    let config = std::fs::read_to_string(dir.path().join("config.yaml")).unwrap();
    assert!(config.contains(&format!("tls_pin: {fingerprint}")), "{config}");

    // Sessions use the pin from the config.
    let session = app.connect("home").unwrap();
    assert!(session.sites().unwrap().is_empty());

    // When the controller's certificate changes, gomada refuses to talk.
    let changed = config.replace(&fingerprint, &format!("sha256:{}", "1".repeat(64)));
    std::fs::write(dir.path().join("config.yaml"), changed).unwrap();
    let app = App::new(app::Options {
        config_path: Some(dir.path().join("config.yaml")),
        keychain: Some(keychain),
        no_token_cache: true,
        ..Default::default()
    })
    .unwrap();
    let err = app.connect("home").unwrap().sites().unwrap_err();
    assert!(err.is_pin_mismatch(), "{err}");
}

#[test]
fn session_reads_the_secret_once_and_caches_firmware() {
    let c = fake::Controller::start();
    let dir = tempfile::tempdir().unwrap();
    let keychain = Arc::new(MemoryKeychain::default());
    let clock = Clock::new();
    let app = App::new(app::Options {
        config_path: Some(dir.path().join("config.yaml")),
        keychain: Some(keychain.clone()),
        now: Some(clock.source()),
        ..Default::default()
    })
    .unwrap();
    app.add_host(&NewHost {
        name: "home".into(),
        url: c.url.clone(),
        client_id: fake::CLIENT_ID.into(),
        secret: fake::SECRET.into(),
        ..Default::default()
    })
    .unwrap();

    let s = app.connect("").unwrap(); // the current host
    assert_eq!((s.host(), s.url()), ("home", c.url.as_str()));
    s.unlock().unwrap();
    // The secret is gone from the keychain; the session still renews its
    // token, because it read the secret once and kept it.
    keychain.values.lock().unwrap().remove("home");
    clock.advance(3 * 7200);
    assert_eq!(s.sites().unwrap().len(), 2);

    // Firmware information is asked for once per device and reused for ten
    // minutes; an offline device is never asked about.
    let devices = s.devices("site-1").unwrap();
    assert_eq!(devices.iter().map(|d| d.kind.as_str()).collect::<Vec<_>>(), ["gateway", "switch", "ap", "ap"]);
    let asked = || c.with(|st| st.calls.iter().filter(|call| call.ends_with("/latest-firmware-info")).count());
    assert!(s.refresh_firmware("site-1", &devices));
    assert_eq!(asked(), 3);
    assert!(!s.refresh_firmware("site-1", &devices));
    assert_eq!(asked(), 3);
    assert!(s.cached_firmware("AA-00-00-00-00-03").unwrap().update_available());
    assert!(s.cached_firmware("AA-00-00-00-00-04").is_none());
    clock.advance(601);
    assert!(s.cached_firmware("AA-00-00-00-00-03").is_none());
    assert!(s.refresh_firmware("site-1", &devices));

    // Starting an upgrade drops what is cached about the device.
    let ap = s.find_device("site-1", "AP office").unwrap();
    assert_eq!(s.upgrade_device("site-1", &ap).unwrap().latest, "1.1.2");
    assert!(s.cached_firmware(&ap.mac).is_none());
    assert!(s.wait_for_upgrade("site-1", &ap, Duration::from_millis(1), Duration::from_secs(5)).unwrap());
    let err = s.upgrade_device("site-1", &ap).unwrap_err();
    assert!(err.is_up_to_date(), "{err}");
    assert_eq!(err.to_string(), "AP office is already up to date (firmware 1.1.2)");

    // A wait that runs out of time says so.
    c.with(|st| st.firmware.get_mut("AA-00-00-00-00-01").unwrap()["lastFwVer"] = "9.9".into());
    c.with(|st| st.upgrade_polls = 1_000_000);
    let gw = s.find_device("site-1", "Core router").unwrap();
    s.upgrade_device("site-1", &gw).unwrap();
    let err = s.wait_for_upgrade("site-1", &gw, Duration::from_millis(5), Duration::from_millis(40)).unwrap_err();
    assert!(err.is_deadline_exceeded(), "{err}");
}

#[test]
fn the_drawn_qr_code_scans() {
    // Read the code back from its half-block drawing, the way a phone
    // camera would see it: every character is one module wide and two high.
    let payload = "WIFI:T:WPA;S:Home-5G;P:correct horse battery;;";
    let lines = Code::encode(payload).unwrap().half_blocks(4);
    let light = |x: usize, y: usize| -> bool {
        let glyph = lines[y / 2].chars().nth(x).unwrap();
        matches!((glyph, y % 2), ('█', _) | ('▀', 0) | ('▄', 1))
    };
    const SCALE: usize = 4;
    let modules = lines[0].chars().count();
    let mut image = rqrr::PreparedImage::prepare_from_greyscale(modules * SCALE, modules * SCALE, |x, y| {
        // The last text row of an odd-sized drawing has no lower half.
        if light(x / SCALE, (y / SCALE).min(lines.len() * 2 - 1)) {
            255
        } else {
            0
        }
    });
    let grids = image.detect_grids();
    assert_eq!(grids.len(), 1, "one QR code is found");
    let (_, content) = grids[0].decode().unwrap();
    assert_eq!(content, payload);
}
