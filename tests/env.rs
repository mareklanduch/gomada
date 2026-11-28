//! What depends on environment variables. Everything is in one test, in a
//! test binary of its own, because the environment belongs to the whole
//! process.

mod support;

use support::{fake, Env};

#[test]
fn environment_variables() {
    // GOMADA_CLIENT_SECRET supplies the secret when adding a host, and
    // with --secret-store env nothing is stored anywhere.
    std::env::set_var("GOMADA_CLIENT_SECRET", fake::SECRET);
    let mut e = Env::new();
    let url = e.fake.url.clone();
    let out =
        e.must_run(&["host", "add", "ci", "--url", &url, "--client-id", fake::CLIENT_ID, "--secret-store", "env"]);
    assert!(out.contains("nowhere; it is read from GOMADA_CLIENT_SECRET on each run"), "{out}");
    assert!(e.keychain.values.lock().unwrap().is_empty());
    e.must_run(&["site", "list"]);
    assert!(e.keychain.values.lock().unwrap().is_empty(), "not even a token is kept");

    std::env::remove_var("GOMADA_CLIENT_SECRET");
    let err = e.want_fail(1, &["site", "list"]);
    assert!(err.contains("the GOMADA_CLIENT_SECRET environment variable is not set"), "{err}");

    // The variable wins over a stored secret.
    let mut e = Env::with_host();
    e.keychain.values.lock().unwrap().insert("home".into(), "stale".into());
    std::env::set_var("GOMADA_CLIENT_SECRET", fake::SECRET);
    e.must_run(&["--no-token-cache", "site", "list"]);
    std::env::remove_var("GOMADA_CLIENT_SECRET");
    e.want_fail(3, &["--no-token-cache", "site", "list"]);

    // GOMADA_PASSPHRASE unlocks an encrypted secret file without a
    // terminal, and the token goes to the runtime directory, so the next
    // command needs neither the passphrase nor a login.
    let mut e = Env::new();
    e.keychain.unavailable.store(true, std::sync::atomic::Ordering::SeqCst);
    let runtime = e.dir.path().join("run");
    std::env::set_var("XDG_RUNTIME_DIR", &runtime);
    std::env::set_var("GOMADA_PASSPHRASE", "correct horse");
    e.stdin = format!("{}\n", fake::SECRET);
    let url = e.fake.url.clone();
    e.must_run(&["host", "add", "server", "--url", &url, "--client-id", fake::CLIENT_ID, "--secret-stdin"]);
    e.must_run(&["site", "list"]);
    let logins = e.fake.logins();
    let token = runtime.join("gomada").join("server.token.json");
    assert!(token.exists(), "the token is cached in the runtime directory");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(std::fs::metadata(&token).unwrap().permissions().mode() & 0o777, 0o600);
    }
    std::env::remove_var("GOMADA_PASSPHRASE");
    e.must_run(&["site", "list"]);
    assert_eq!(e.fake.logins(), logins, "a valid token spares the passphrase");

    // Removing the host removes the secret file and the token.
    e.must_run(&["host", "remove", "server", "--yes"]);
    assert!(!token.exists());
    assert!(!e.dir.path().join("secrets").join("server.secret.json").exists());

    // GOMADA_CONFIG points to the config file.
    std::env::remove_var("XDG_RUNTIME_DIR");
    let e = Env::with_host();
    std::env::set_var("GOMADA_CONFIG", &e.path);
    assert_eq!(gomada::config::default_path().unwrap(), e.path);
    std::env::remove_var("GOMADA_CONFIG");
    std::env::set_var("XDG_CONFIG_HOME", e.dir.path());
    assert_eq!(gomada::config::default_path().unwrap(), e.dir.path().join("gomada").join("config.yaml"));
    std::env::remove_var("XDG_CONFIG_HOME");
}
