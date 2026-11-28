//! The HTTP transport: one request in, the `result` of the controller's
//! answer out, with certificate checking by system roots, by pin, or not at
//! all.

use std::io::Read;
use std::net::{TcpStream, ToSocketAddrs};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::client::WebPkiServerVerifier;
use rustls::crypto::{ring, verify_tls12_signature, verify_tls13_signature, CryptoProvider};
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::{ClientConfig, DigitallySignedStruct, RootCertStore, SignatureScheme};
use serde_json::Value;
use sha2::{Digest, Sha256};

use crate::error::{ApiError, Error, Result};

/// Bounds every request made with an [`Http`].
pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(15);

/// Bounds how much of a response is read.
const MAX_RESPONSE_BYTES: u64 = 16 << 20;

/// Controls how the controller's certificate is checked.
#[derive(Debug, Clone, Default)]
pub struct TlsOptions {
    /// The fingerprint ("sha256:<hex>") of the only certificate to accept.
    /// Empty means normal verification against the system roots.
    pub pin: String,
    /// Accepts any certificate. It exists for emergencies only.
    pub insecure: bool,
}

/// Receives one line per request when `--verbose` is on.
pub type Logger = Arc<dyn Fn(&str) + Send + Sync>;

/// Returns the SHA-256 fingerprint of a certificate in the form stored in
/// the configuration: "sha256:<hex>".
pub fn fingerprint(cert_der: &[u8]) -> String {
    format!("sha256:{}", hex::encode(Sha256::digest(cert_der)))
}

fn provider() -> Arc<CryptoProvider> {
    Arc::new(ring::default_provider())
}

fn system_roots() -> Arc<RootCertStore> {
    let mut roots = RootCertStore::empty();
    for cert in rustls_native_certs::load_native_certs().certs {
        let _ = roots.add(cert); // a root that cannot be parsed is skipped
    }
    Arc::new(roots)
}

/// What a certificate check saw, shared between the verifier and its user.
#[derive(Debug, Default)]
struct Seen {
    fingerprint: Option<String>,
    trusted: bool,
    mismatch: Option<String>,
}

#[derive(Debug)]
enum Check {
    /// Accept only the certificate with this fingerprint.
    Pin(String),
    /// Accept anything.
    Insecure,
    /// Accept anything, but note whether the system roots would have.
    Probe(Option<Arc<WebPkiServerVerifier>>),
}

/// Replaces chain verification. Handshake signatures are still verified, so
/// the peer has to hold the key of the certificate it shows.
#[derive(Debug)]
struct Verifier {
    check: Check,
    provider: Arc<CryptoProvider>,
    seen: Arc<Mutex<Seen>>,
}

impl ServerCertVerifier for Verifier {
    fn verify_server_cert(
        &self,
        end_entity: &CertificateDer<'_>,
        intermediates: &[CertificateDer<'_>],
        server_name: &ServerName<'_>,
        ocsp_response: &[u8],
        now: UnixTime,
    ) -> std::result::Result<ServerCertVerified, rustls::Error> {
        let got = fingerprint(end_entity.as_ref());
        let mut seen = self.seen.lock().unwrap();
        seen.fingerprint = Some(got.clone());
        match &self.check {
            Check::Pin(pin) if *pin != got => {
                seen.mismatch = Some(got);
                Err(rustls::Error::General("the certificate does not match the pin".into()))
            }
            Check::Probe(Some(inner)) => {
                seen.trusted =
                    inner.verify_server_cert(end_entity, intermediates, server_name, ocsp_response, now).is_ok();
                Ok(ServerCertVerified::assertion())
            }
            _ => Ok(ServerCertVerified::assertion()),
        }
    }

    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> std::result::Result<HandshakeSignatureValid, rustls::Error> {
        verify_tls12_signature(message, cert, dss, &self.provider.signature_verification_algorithms)
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> std::result::Result<HandshakeSignatureValid, rustls::Error> {
        verify_tls13_signature(message, cert, dss, &self.provider.signature_verification_algorithms)
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.provider.signature_verification_algorithms.supported_schemes()
    }
}

fn config_with(check: Check, seen: Arc<Mutex<Seen>>) -> Result<ClientConfig> {
    let provider = provider();
    Ok(ClientConfig::builder_with_provider(provider.clone())
        .with_safe_default_protocol_versions()
        .map_err(|e| Error::other(format!("set up TLS: {e}")))?
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(Verifier { check, provider, seen }))
        .with_no_client_auth())
}

fn tls_config(opts: &TlsOptions, seen: Arc<Mutex<Seen>>) -> Result<ClientConfig> {
    if opts.insecure {
        return config_with(Check::Insecure, seen);
    }
    if !opts.pin.is_empty() {
        return config_with(Check::Pin(opts.pin.clone()), seen);
    }
    Ok(ClientConfig::builder_with_provider(provider())
        .with_safe_default_protocol_versions()
        .map_err(|e| Error::other(format!("set up TLS: {e}")))?
        .with_root_certificates(system_roots())
        .with_no_client_auth())
}

/// Makes requests to one controller.
pub struct Http {
    agent: ureq::Agent,
    seen: Arc<Mutex<Seen>>,
    logger: Option<Logger>,
}

impl Http {
    /// Returns a transport that checks the controller's certificate
    /// according to `opts`. A timeout of zero means [`DEFAULT_TIMEOUT`].
    pub fn new(opts: &TlsOptions, timeout: Duration, logger: Option<Logger>) -> Result<Http> {
        let timeout = if timeout.is_zero() { DEFAULT_TIMEOUT } else { timeout };
        let seen = Arc::new(Mutex::new(Seen::default()));
        let agent = ureq::AgentBuilder::new()
            .timeout(timeout)
            .redirects(0)
            .tls_config(Arc::new(tls_config(opts, seen.clone())?))
            .build();
        Ok(Http { agent, seen, logger })
    }

    /// Makes one request and returns the `result` of the answer, if it has
    /// one. Headers and bodies are never logged, so secrets and tokens
    /// cannot leak through the log.
    pub fn request(&self, method: &str, url: &str, token: &str, body: Option<&Value>) -> Result<Option<Value>> {
        let start = Instant::now();
        let outcome = self.send(method, url, token, body);
        if let Some(log) = &self.logger {
            let shown = url.split('?').next().unwrap_or(url);
            let ms = start.elapsed().as_millis();
            match &outcome {
                Ok((status, _)) => {
                    log(&format!("level=DEBUG msg=request method={method} url={shown} duration={ms}ms status={status}"))
                }
                Err(e) => log(&format!(
                    "level=DEBUG msg=\"request failed\" method={method} url={shown} duration={ms}ms error={:?}",
                    e.to_string()
                )),
            }
        }
        let (status, raw) = outcome?;
        decode(status, &raw)
    }

    fn send(&self, method: &str, url: &str, token: &str, body: Option<&Value>) -> Result<(u16, Vec<u8>)> {
        let mut req = self.agent.request(method, url).set("Accept", "application/json");
        if !token.is_empty() {
            req = req.set("Authorization", &format!("AccessToken={token}"));
        }
        self.seen.lock().unwrap().mismatch = None;
        let sent = match body {
            Some(b) => req.set("Content-Type", "application/json").send_string(&b.to_string()),
            None => req.call(),
        };
        let resp = match sent {
            Ok(resp) => resp,
            Err(ureq::Error::Status(_, resp)) => resp,
            Err(ureq::Error::Transport(t)) => return Err(self.classify(&t)),
        };
        let status = resp.status();
        let mut raw = Vec::new();
        resp.into_reader()
            .take(MAX_RESPONSE_BYTES)
            .read_to_end(&mut raw)
            .map_err(|e| Error::Unreachable(e.to_string()))?;
        Ok((status, raw))
    }

    /// Turns a transport error into one of the error classes.
    fn classify(&self, t: &ureq::Transport) -> Error {
        if let Some(got) = self.seen.lock().unwrap().mismatch.take() {
            return Error::PinMismatch { got };
        }
        let text = t.to_string();
        if is_certificate_error(t) {
            return Error::Certificate(text);
        }
        Error::Unreachable(text)
    }
}

/// Reports whether a transport error was caused by the certificate.
fn is_certificate_error(err: &(dyn std::error::Error + 'static)) -> bool {
    let mut cur: Option<&(dyn std::error::Error + 'static)> = Some(err);
    while let Some(e) = cur {
        if matches!(e.downcast_ref::<rustls::Error>(), Some(rustls::Error::InvalidCertificate(_))) {
            return true;
        }
        // An io::Error hides what it wraps from source().
        if let Some(inner) = e.downcast_ref::<std::io::Error>().and_then(|io| io.get_ref()) {
            if is_certificate_error(inner) {
                return true;
            }
        }
        cur = e.source();
    }
    let text = err.to_string();
    text.contains("invalid peer certificate") || text.contains("InvalidCertificate")
}

fn status_text(status: u16) -> &'static str {
    match status {
        400 => "Bad Request",
        401 => "Unauthorized",
        403 => "Forbidden",
        404 => "Not Found",
        405 => "Method Not Allowed",
        408 => "Request Timeout",
        429 => "Too Many Requests",
        500 => "Internal Server Error",
        502 => "Bad Gateway",
        503 => "Service Unavailable",
        504 => "Gateway Timeout",
        _ => "Unexpected status",
    }
}

/// Unwraps the envelope around every controller response.
pub(crate) fn decode(status: u16, raw: &[u8]) -> Result<Option<Value>> {
    let ok = (200..300).contains(&status);
    let http_error = |message: String| Error::Api(ApiError { code: 0, message, http_status: status });
    let env: Value = match serde_json::from_slice(raw) {
        Ok(v @ Value::Object(_)) => v,
        Ok(_) | Err(_) if !ok => return Err(http_error(status_text(status).into())),
        Ok(other) => {
            return Err(Error::other(format!("the controller sent a response that is not a JSON object: {other}")))
        }
        Err(e) => return Err(Error::other(format!("the controller sent a response that is not JSON: {e}"))),
    };
    let msg = env.get("msg").and_then(Value::as_str).unwrap_or_default();
    let code = match env.get("errorCode") {
        None | Some(Value::Null) => 0,
        Some(v) => v
            .as_i64()
            .or_else(|| v.as_str().and_then(|s| s.trim().parse().ok()))
            .ok_or_else(|| Error::other(format!("the controller sent an error code that is not a number: {v}")))?,
    };
    if code != 0 {
        return Err(Error::Api(ApiError { code, message: msg.into(), http_status: status }));
    }
    if !ok {
        let msg = if msg.is_empty() { status_text(status) } else { msg };
        return Err(http_error(msg.into()));
    }
    Ok(env.get("result").filter(|r| !r.is_null()).cloned())
}

/// Connects to `base_url` and reports the fingerprint of the certificate it
/// presents and whether that certificate is trusted by the system roots.
/// For plain http URLs it reports trusted with an empty fingerprint.
pub fn probe_tls(base_url: &str, timeout: Duration) -> Result<(String, bool)> {
    let u = url::Url::parse(base_url).map_err(|e| Error::other(format!("invalid URL {base_url:?}: {e}")))?;
    if u.scheme() != "https" {
        return Ok((String::new(), true));
    }
    let host = u.host_str().unwrap_or_default().trim_matches(['[', ']']).to_string();
    let port = u.port().unwrap_or(443);
    let unreachable = |e: std::io::Error| Error::Unreachable(e.to_string());

    let inner = WebPkiServerVerifier::builder_with_provider(system_roots(), provider()).build().ok();
    let seen = Arc::new(Mutex::new(Seen::default()));
    let config = config_with(Check::Probe(inner), seen.clone())?;
    let name =
        ServerName::try_from(host.clone()).map_err(|e| Error::other(format!("invalid host name {host:?}: {e}")))?;
    let mut conn =
        rustls::ClientConnection::new(Arc::new(config), name).map_err(|e| Error::other(format!("set up TLS: {e}")))?;

    let addr = (host.as_str(), port)
        .to_socket_addrs()
        .map_err(unreachable)?
        .next()
        .ok_or_else(|| Error::Unreachable(format!("{host} has no address")))?;
    let mut sock = TcpStream::connect_timeout(&addr, timeout).map_err(unreachable)?;
    sock.set_read_timeout(Some(timeout)).map_err(unreachable)?;
    sock.set_write_timeout(Some(timeout)).map_err(unreachable)?;
    while conn.is_handshaking() {
        conn.complete_io(&mut sock).map_err(unreachable)?;
    }
    conn.send_close_notify();
    let _ = conn.complete_io(&mut sock);

    let seen = seen.lock().unwrap();
    match &seen.fingerprint {
        Some(fp) => Ok((fp.clone(), seen.trusted)),
        None => Err(Error::Certificate("the controller presented no certificate".into())),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn decode_unwraps_the_envelope() {
        let v = decode(200, br#"{"errorCode":0,"msg":"Success.","result":{"a":1}}"#).unwrap();
        assert_eq!(v, Some(serde_json::json!({"a": 1})));
        assert_eq!(decode(200, br#"{"errorCode":0,"result":null}"#).unwrap(), None);
        assert_eq!(decode(200, br#"{"errorCode":0}"#).unwrap(), None);
    }

    #[test]
    fn decode_reports_controller_errors() {
        let err =
            decode(200, br#"{"errorCode":-44106,"msg":"The client id or client secret is invalid."}"#).unwrap_err();
        assert_eq!(err.to_string(), "The client id or client secret is invalid. (code -44106)");
        assert_eq!(err.api().unwrap().code, -44106);

        let err = decode(404, b"404 page not found").unwrap_err();
        assert_eq!(err.to_string(), "Not Found (HTTP 404)");
        let err = decode(500, br#"{"errorCode":0,"msg":"boom"}"#).unwrap_err();
        assert_eq!(err.to_string(), "boom (HTTP 500)");
        let err = decode(401, br#"{}"#).unwrap_err();
        assert_eq!(err.api().unwrap().http_status, 401);
    }

    #[test]
    fn decode_rejects_what_is_not_json() {
        assert!(decode(200, b"<html>").unwrap_err().to_string().contains("not JSON"));
        assert!(decode(200, b"[1,2]").unwrap_err().to_string().contains("not a JSON object"));
    }

    #[test]
    fn decode_never_panics_on_odd_input() {
        // The inputs a fuzzer finds first.
        for raw in [
            &b""[..],
            b"null",
            b"0",
            b"\"x\"",
            b"{\"errorCode\":\"x\"}",
            b"{\"errorCode\":\"-5\",\"msg\":7}",
            b"{\"result\":[]}",
            b"\xff\xfe",
        ] {
            let _ = decode(200, raw);
            let _ = decode(500, raw);
        }
    }

    #[test]
    fn fingerprint_format() {
        assert_eq!(fingerprint(b"abc"), "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad");
    }

    #[test]
    fn plain_http_needs_no_certificate() {
        assert_eq!(probe_tls("http://192.168.0.2:8088", DEFAULT_TIMEOUT).unwrap(), (String::new(), true));
    }
}
