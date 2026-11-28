//! Runs the read-only calls against a real controller. It is not part of
//! the normal test run:
//!
//! ```sh
//! GOMADA_TEST_URL=https://192.168.0.2:8043 \
//! GOMADA_TEST_CLIENT_ID=... GOMADA_TEST_CLIENT_SECRET=... \
//! cargo test --test live -- --ignored --nocapture
//! ```
//!
//! Set `GOMADA_TEST_TLS_PIN` to the certificate fingerprint for a
//! controller with a self-signed certificate. Nothing on the controller is
//! changed.

use std::time::Duration;

use gomada::omada::{self, Client, Http, TlsOptions};

#[test]
#[ignore = "needs a real controller; see the top of this file"]
fn live_controller() {
    let var = |name: &str| std::env::var(name).unwrap_or_default();
    let (url, id, secret) = (var("GOMADA_TEST_URL"), var("GOMADA_TEST_CLIENT_ID"), var("GOMADA_TEST_CLIENT_SECRET"));
    assert!(
        !url.is_empty() && !id.is_empty() && !secret.is_empty(),
        "set GOMADA_TEST_URL, GOMADA_TEST_CLIENT_ID, and GOMADA_TEST_CLIENT_SECRET to run this test"
    );
    let tls = TlsOptions { pin: var("GOMADA_TEST_TLS_PIN"), insecure: false };
    let http = || Http::new(&tls, Duration::ZERO, None).unwrap();

    let info = omada::fetch_info(&http(), &url).expect("info");
    println!("controller {}, version {}", info.controller_id, info.version);

    let client = Client::new(omada::Options {
        base_url: url,
        controller_id: info.controller_id,
        client_id: id,
        secret: Some(Box::new(move || Ok(secret.clone()))),
        http: http(),
        cache: None,
        now: None,
    });
    for site in client.sites().expect("sites") {
        let devices = client.devices(&site.id).unwrap_or_else(|e| panic!("devices of {}: {e}", site.name));
        let clients = client.clients(&site.id).unwrap_or_else(|e| panic!("clients of {}: {e}", site.name));
        let ssids = client.ssids(&site.id).unwrap_or_else(|e| panic!("Wi-Fi networks of {}: {e}", site.name));
        println!(
            "site {}: {} devices, {} clients, {} Wi-Fi networks",
            site.name,
            devices.len(),
            clients.len(),
            ssids.len()
        );
    }
}
