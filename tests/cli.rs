//! The command line, run in-process against the fake controller.

mod support;

use support::{fake, Env};

/// A fixture with the host "home" added and its site "Default" chosen.
fn env_with_site() -> Env {
    let mut e = Env::with_host();
    e.must_run(&["site", "use", "Default"]);
    e
}

#[track_caller]
fn want_contains(got: &str, want: &str) {
    assert!(got.contains(want), "want {want:?} in:\n{got}");
}

#[test]
fn version() {
    let mut e = Env::new();
    for args in [&["version"][..], &["--version"]] {
        let out = e.must_run(args);
        assert!(out.starts_with("gomada ") && out.contains("commit") && out.ends_with('\n'), "{out}");
    }
}

#[test]
fn usage_errors_exit_with_2() {
    let mut e = Env::with_host();
    for args in [
        &["nope"][..],
        &["device", "nope"],
        &["device", "show"],
        &["device", "show", "a", "b"],
        &["device", "list", "--nope"],
        &["device", "list", "-o", "yaml"],
        &["device", "list", "--type", "router"],
        &["client", "list", "--wired", "--wireless"],
        &["client", "list", "--blocked", "--wired"],
        &["device", "rename", "AP office", "ends with a space "],
        &["--timeout", "soon", "device", "list"],
        &["completion", "tcsh"],
    ] {
        let err = e.want_fail(2, args);
        assert!(!err.is_empty(), "gomada {}: nothing on standard error", args.join(" "));
    }
    // Nothing above reached the controller.
    assert_eq!(e.fake.with(|s| s.calls.len()), 0);
}

#[test]
fn help_lists_commands() {
    let mut e = Env::new();
    let help = e.must_run(&["--help"]);
    for word in [
        "host",
        "site",
        "device",
        "client",
        "wifi",
        "alert",
        "version",
        "completion",
        "--host",
        "--site",
        "--output",
        "--timeout",
        "--yes",
        "--verbose",
        "--no-color",
        "--no-token-cache",
        "--config",
    ] {
        want_contains(&help, word);
    }
    // A group without a subcommand shows its help and succeeds.
    let group = e.must_run(&["device"]);
    for word in ["list", "show", "reboot", "rename", "firmware", "upgrade"] {
        want_contains(&group, word);
    }
    want_contains(&e.must_run(&["wifi", "qr", "--help"]), "contains the Wi-Fi password");
    want_contains(&e.must_run(&["completion", "bash"]), "gomada");
}

#[test]
fn root_opens_the_terminal_ui() {
    let mut e = Env::with_host();
    // Not at a terminal: refused as wrong usage.
    want_contains(&e.want_fail(2, &[]), "needs a terminal");
    assert!(e.tui_opts.lock().unwrap().is_none());

    e.interactive = true;
    e.must_run(&["--host", "home", "--site", "Workshop", "--no-color"]);
    let opts = e.tui_opts.lock().unwrap().clone().expect("the terminal UI was started");
    assert_eq!((opts.host.as_str(), opts.site.as_str(), opts.no_color), ("home", "Workshop", true));
}

#[test]
fn host_add_with_flags() {
    let mut e = Env::new();
    e.stdin = format!("{}\n", fake::SECRET);
    let url = e.fake.url.clone();
    let out = e.must_run(&["host", "add", "home", "--url", &url, "--client-id", fake::CLIENT_ID, "--secret-stdin"]);
    want_contains(&out, &format!("Added host \"home\" ({url}). The client secret is kept in the system keychain."));
    want_contains(&out, "\"home\" is now the current host.");

    // The secret is in the keychain and nowhere in the config.
    assert_eq!(e.keychain.values.lock().unwrap().get("home").map(String::as_str), Some(fake::SECRET));
    let config = std::fs::read_to_string(&e.path).unwrap();
    assert!(!config.contains(fake::SECRET), "{config}");
    want_contains(&config, "secret_store: keyring");
    want_contains(&config, &format!("controller: {}", fake::CONTROLLER_ID));

    // A second host does not become the current one.
    e.add_host("office");
    e.golden_run("host-list", &["host", "list"]);
    e.golden_run("host-list-json", &["host", "list", "-o", "json"]);

    // The same name again is refused.
    e.stdin = format!("{}\n", fake::SECRET);
    want_contains(
        &e.want_fail(1, &["host", "add", "home", "--url", &url, "--client-id", fake::CLIENT_ID, "--secret-stdin"]),
        "already exists",
    );
}

#[test]
fn host_add_interactive() {
    let mut e = Env::new();
    e.interactive = true;
    e.stdin = format!("home\n{}\n{}\n", e.fake.url, fake::CLIENT_ID);
    e.passwords.lock().unwrap().push_back(format!("  {}  ", fake::SECRET));
    let (out, err, code) = e.run(&["host", "add"]);
    assert_eq!(code, 0, "{err}");
    want_contains(&out, "Added host \"home\"");
    for question in ["Name for this host", "Controller address", "Client ID: ", "Client secret (not shown): "] {
        want_contains(&err, question);
    }
}

#[test]
fn host_add_missing_input_without_a_terminal() {
    let mut e = Env::new();
    let url = e.fake.url.clone();
    want_contains(&e.want_fail(2, &["host", "add"]), "NAME is required when not running in a terminal");
    want_contains(&e.want_fail(2, &["host", "add", "home"]), "--url is required");
    want_contains(&e.want_fail(2, &["host", "add", "home", "--url", &url]), "--client-id is required");
    want_contains(&e.want_fail(2, &["host", "add", "home", "--url", &url, "--client-id", "x"]), "no client secret");
    want_contains(
        &e.want_fail(2, &["host", "add", "home", "--url", &url, "--client-id", "x", "--secret-stdin"]),
        "no client secret on standard input",
    );
    e.stdin = "\n".into();
    want_contains(
        &e.want_fail(2, &["host", "add", "home", "--url", &url, "--client-id", "x", "--secret-stdin"]),
        "is empty",
    );
    want_contains(
        &e.want_fail(2, &["host", "add", "home", "--url", "ftp://x", "--client-id", "x"]),
        "unsupported scheme",
    );
    assert!(!e.path.exists(), "nothing was saved");
}

#[test]
fn host_add_wrong_secret() {
    let mut e = Env::new();
    e.stdin = "wrong\n".into();
    let url = e.fake.url.clone();
    let err = e.want_fail(3, &["host", "add", "home", "--url", &url, "--client-id", fake::CLIENT_ID, "--secret-stdin"]);
    want_contains(&err, "authentication failed: The client id or client secret is invalid. (code -44106)");
    want_contains(&err, "Check the client ID and client secret");
    assert!(!e.path.exists() && e.keychain.values.lock().unwrap().is_empty(), "nothing was saved");
}

#[test]
fn host_add_unreachable() {
    let mut e = Env::new();
    e.stdin = "x\n".into();
    let err =
        e.want_fail(4, &["host", "add", "home", "--url", "http://127.0.0.1:1", "--client-id", "x", "--secret-stdin"]);
    want_contains(&err, "could not read the controller ID from http://127.0.0.1:1: controller unreachable");
    want_contains(&err, "Check the controller address");
}

#[test]
fn host_add_without_a_keychain_needs_a_passphrase() {
    let mut e = Env::new();
    e.keychain.unavailable.store(true, std::sync::atomic::Ordering::SeqCst);
    let url = e.fake.url.clone();
    let args = ["host", "add", "home", "--url", &url, "--client-id", fake::CLIENT_ID, "--secret-stdin"];

    // Without a terminal there is no way to choose a passphrase.
    e.stdin = format!("{}\n", fake::SECRET);
    want_contains(&e.want_fail(2, &args), "no system keychain here");

    // At a terminal it is typed twice; a mismatch is refused.
    e.interactive = true;
    e.stdin = format!("{}\n", fake::SECRET);
    *e.passwords.lock().unwrap() = ["one".to_string(), "two".to_string()].into();
    want_contains(&e.want_fail(1, &args), "the two passphrases differ");

    e.stdin = format!("{}\n", fake::SECRET);
    *e.passwords.lock().unwrap() = ["correct horse".to_string(), "correct horse".to_string()].into();
    let out = e.must_run(&args);
    want_contains(&out, "The client secret is kept in an encrypted file next to the config.");
    let sealed = std::fs::read_to_string(e.dir.path().join("secrets").join("home.secret.json")).unwrap();
    assert!(sealed.contains("scrypt") && !sealed.contains(fake::SECRET), "{sealed}");

    // Using the host asks for the passphrase; a wrong one is refused.
    let logins = e.fake.logins();
    e.passwords.lock().unwrap().push_back("wrong".into());
    want_contains(&e.want_fail(1, &["--no-token-cache", "site", "list"]), "wrong passphrase");
    e.passwords.lock().unwrap().push_back("correct horse".into());
    want_contains(&e.must_run(&["--no-token-cache", "site", "list"]), "Workshop");
    assert_eq!(e.fake.logins(), logins + 1);

    // Without a terminal the passphrase cannot be asked for, and the
    // message says what to do.
    e.interactive = false;
    let err = e.want_fail(1, &["--no-token-cache", "site", "list"]);
    want_contains(&err, "a passphrase is required");
    want_contains(&err, "GOMADA_PASSPHRASE");
}

#[test]
fn host_use_remove_test() {
    let mut e = Env::with_host();
    e.add_host("office");
    want_contains(&e.must_run(&["host", "test"]), "OK: home (");
    want_contains(&e.must_run(&["host", "test", "office"]), "Sites: 2.");
    want_contains(&e.want_fail(1, &["host", "test", "nope"]), "unknown host \"nope\"; known hosts: home, office");

    want_contains(&e.must_run(&["host", "use", "office"]), "\"office\" is now the current host.");
    want_contains(&e.want_fail(1, &["host", "use", "nope"]), "unknown host");

    // Removing needs confirmation: refused in a script, asked at a terminal.
    want_contains(&e.want_fail(2, &["host", "remove", "office"]), "pass --yes");
    e.interactive = true;
    e.stdin = "n\n".into();
    want_contains(&e.want_fail(1, &["host", "rm", "office"]), "canceled");
    e.stdin = "y\n".into();
    want_contains(&e.must_run(&["host", "rm", "office"]), "Removed host \"office\".");
    e.interactive = false;

    // The secret and the cached token are gone; the other host is current.
    let left: Vec<String> = e.keychain.values.lock().unwrap().keys().cloned().collect();
    assert!(left.iter().all(|k| k.starts_with("home")), "{left:?}");
    want_contains(&e.must_run(&["host", "list"]), "*  home");

    e.must_run(&["host", "remove", "home", "--yes"]);
    assert_eq!(e.run(&["host", "list"]), (String::new(), "No hosts yet. Add one with: gomada host add\n".into(), 0));
    assert_eq!(e.must_run(&["host", "list", "-o", "json"]), "[]\n");
    want_contains(&e.want_fail(1, &["device", "list"]), "no host is set up yet");
}

#[test]
fn sites() {
    let mut e = Env::with_host();
    e.golden_run("site-list", &["site", "list"]);
    // Two sites and none chosen: commands need --site.
    want_contains(
        &e.want_fail(1, &["device", "list"]),
        "this host has several sites; choose one with --site (sites: Default, Workshop)",
    );
    want_contains(&e.must_run(&["--site", "workshop", "device", "list"]), "Workshop AP");
    want_contains(&e.must_run(&["--site", "site-1", "device", "list"]), "Core router");
    want_contains(
        &e.want_fail(1, &["--site", "Nope", "device", "list"]),
        "no site named \"Nope\" (sites: Default, Workshop)",
    );

    want_contains(&e.must_run(&["site", "use", "Workshop"]), "now use site \"Workshop\"");
    want_contains(&e.must_run(&["device", "list"]), "Workshop AP");
    e.golden_run("site-list-json", &["site", "list", "-o", "json"]);

    // With a single site there is nothing to choose.
    e.fake.with(|s| s.sites.truncate(1));
    want_contains(&e.must_run(&["site", "list"]), "*  Default");
}

#[test]
fn device_list() {
    let mut e = env_with_site();
    e.golden_run("device-list", &["device", "list"]);
    e.golden_run("device-list-json", &["device", "ls", "--output", "json"]);

    let aps = e.must_run(&["device", "list", "--type", "AP"]);
    assert!(aps.contains("AP office") && !aps.contains("Core router"), "{aps}");
    let filtered = e.must_run(&["device", "list", "--filter", "OFFLINE"]);
    assert!(filtered.contains("AP garage") && !filtered.contains("AP office"), "{filtered}");
    assert_eq!(e.must_run(&["device", "list", "--filter", "zzz", "-o", "json"]), "[]\n");
}

#[test]
fn device_list_pages_through_long_lists() {
    let mut e = env_with_site();
    e.fake.with(|s| s.max_page_size = 1);
    let out = e.must_run(&["device", "list"]);
    assert_eq!(out.lines().count(), 5, "{out}");
    assert!(e.fake.called("GET /sites/site-1/devices"));
    assert_eq!(e.fake.with(|s| s.calls.iter().filter(|c| c.ends_with("/devices")).count()), 4);
}

#[test]
fn device_show() {
    let mut e = env_with_site();
    e.golden_run("device-show", &["device", "show", "core router"]);
    e.golden_run("device-show-json", &["device", "show", "aa:00:00:00:00:03", "-o", "json"]);
    want_contains(&e.want_fail(1, &["device", "show", "nope"]), "no device matches \"nope\"");

    e.fake.with(|s| s.devices.get_mut("site-1").unwrap()[2]["name"] = "Core router".into());
    want_contains(&e.want_fail(1, &["device", "show", "Core router"]), "matches several devices; use the MAC address");
}

#[test]
fn device_reboot() {
    let mut e = env_with_site();
    let call = "POST /sites/site-1/devices/AA-00-00-00-00-02/reboot";
    want_contains(&e.want_fail(2, &["device", "reboot", "Rack switch"]), "this needs confirmation; pass --yes");
    assert!(!e.fake.called(call));

    e.interactive = true;
    e.stdin = "no\n".into();
    let (_, err, code) = e.run(&["device", "reboot", "Rack switch"]);
    assert_eq!(code, 1);
    want_contains(&err, "Reboot Rack switch (switch, AA-00-00-00-00-02)? [y/N] ");
    assert!(!e.fake.called(call));

    e.stdin = "YES\n".into();
    assert_eq!(e.must_run(&["device", "reboot", "Rack switch"]), "Rebooting Rack switch.\n");
    assert!(e.fake.called(call));

    e.interactive = false;
    e.must_run(&["device", "reboot", "aa-00-00-00-00-01", "-y"]);
    assert!(e.fake.called("POST /sites/site-1/devices/AA-00-00-00-00-01/reboot"));
}

#[test]
fn device_rename_uses_the_endpoint_of_the_device_type() {
    let mut e = env_with_site();
    assert_eq!(e.must_run(&["device", "rename", "AP office", "Office AP"]), "Renamed AP office to Office AP.\n");
    assert_eq!(e.fake.device_name("site-1", "AA-00-00-00-00-03"), "Office AP");
    assert!(e.fake.called("PATCH /sites/site-1/aps/AA-00-00-00-00-03/general-config"));

    e.must_run(&["device", "rename", "Rack switch", "Rack"]);
    assert!(e.fake.called("PATCH /sites/site-1/switches/AA-00-00-00-00-02/general-config"));

    // A gateway keeps its LED setting, which the endpoint insists on.
    e.must_run(&["device", "rename", "Core router", "Core"]);
    assert_eq!(e.fake.device_name("site-1", "AA-00-00-00-00-01"), "Core");
    assert_eq!(e.fake.with(|s| s.devices["site-1"][0]["ledSetting"].clone()), 1);

    // An Easy Managed switch is reached through its own path.
    e.fake.with(|s| s.devices.get_mut("site-1").unwrap()[1]["easyManaged"] = true.into());
    e.must_run(&["device", "rename", "Rack", "Rack 2"]);
    assert!(e.fake.called("PATCH /sites/site-1/switches/es/AA-00-00-00-00-02/general-config"));
    assert_eq!(e.fake.device_name("site-1", "AA-00-00-00-00-02"), "Rack 2");
}

#[test]
fn client_list() {
    let mut e = env_with_site();
    e.golden_run("client-list", &["client", "list"]);
    e.golden_run("client-list-json", &["client", "list", "-o", "json"]);
    let wired = e.must_run(&["client", "list", "--wired"]);
    assert!(wired.contains("NAS") && !wired.contains("Laptop"), "{wired}");
    let iot = e.must_run(&["client", "list", "--wireless", "--filter", "iot"]);
    assert!(iot.contains("printer") && !iot.contains("Laptop"), "{iot}");
}

#[test]
fn client_show() {
    let mut e = env_with_site();
    e.golden_run("client-show", &["client", "show", "laptop"]);
    e.golden_run("client-show-json", &["client", "show", "NAS", "-o", "json"]);
    // By host name and by MAC address in another notation.
    want_contains(&e.must_run(&["client", "show", "printer"]), "CC-00-00-00-00-03");
    want_contains(&e.must_run(&["client", "show", "cc00.0000.0003"]), "printer");
}

#[test]
fn client_actions() {
    let mut e = env_with_site();
    assert_eq!(e.must_run(&["client", "rename", "printer", "Printer 1"]), "Renamed printer to Printer 1.\n");
    assert_eq!(e.fake.client_name("site-1", "CC-00-00-00-00-03"), "Printer 1");

    want_contains(&e.want_fail(1, &["client", "reconnect", "NAS"]), "NAS is a wired client");
    assert_eq!(e.must_run(&["client", "reconnect", "Laptop"]), "Asked Laptop to reconnect.\n");
    assert!(e.fake.called("POST /sites/site-1/clients/CC-00-00-00-00-01/reconnect"));

    want_contains(&e.want_fail(2, &["client", "block", "Laptop"]), "pass --yes");
    assert_eq!(
        e.must_run(&["client", "block", "Laptop", "--yes"]),
        "Blocked Laptop (CC-00-00-00-00-01). Unblock it with: gomada client unblock CC-00-00-00-00-01\n"
    );
    assert!(e.fake.with(|s| s.blocked.contains("CC-00-00-00-00-01")));
    assert!(!e.must_run(&["client", "list"]).contains("Laptop"));

    want_contains(&e.want_fail(1, &["client", "unblock", "Laptop"]), "is not a MAC address");
    assert_eq!(e.must_run(&["client", "unblock", "cc:00:00:00:00:01"]), "Unblocked cc:00:00:00:00:01.\n");
    assert!(e.fake.with(|s| s.blocked.is_empty()));
    want_contains(
        &e.want_fail(1, &["client", "unblock", "cc:00:00:00:00:01"]),
        "This client does not exist. (code -41004)",
    );
}

#[test]
fn client_list_blocked() {
    let mut e = env_with_site();
    assert_eq!(e.run(&["client", "list", "--blocked"]), (String::new(), "No client is blocked.\n".into(), 0));
    assert_eq!(e.must_run(&["client", "list", "--blocked", "-o", "json"]), "[]\n");
    e.must_run(&["client", "block", "Laptop", "--yes"]);
    e.must_run(&["client", "block", "printer", "--yes"]);
    e.golden_run("client-list-blocked", &["client", "list", "--blocked"]);
    e.golden_run("client-list-blocked-json", &["client", "list", "--blocked", "-o", "json"]);
    let filtered = e.must_run(&["client", "list", "--blocked", "--filter", "lap"]);
    assert!(filtered.contains("Laptop") && !filtered.contains("printer"), "{filtered}");
}

#[test]
fn wifi_list_and_show() {
    let mut e = env_with_site();
    e.golden_run("wifi-list", &["wifi", "list"]);
    e.golden_run("wifi-list-json", &["wifi", "ls", "-o", "json"]);

    let hidden = e.must_run(&["wifi", "show", "home-5g"]);
    e.golden("wifi-show", &hidden);
    assert!(!hidden.contains("correct horse battery"));
    assert!(
        !e.fake.called("GET /sites/site-1/wireless-network/wlans/wlan-1/ssids/ssid-1"),
        "the password is not even fetched"
    );

    want_contains(&e.must_run(&["wifi", "show", "Home-5G", "--password"]), "Password:       correct horse battery");
    let json = e.must_run(&["wifi", "show", "IoT", "--password", "-o", "json"]);
    want_contains(&json, "\"password\": \"things;and:stuff\"");
    want_contains(&json, "\"vlan_id\": 30");
    want_contains(&json, "\"hidden\": true");
    // An open network has no password line at all; by ID works too.
    assert!(!e.must_run(&["wifi", "show", "Guests", "--password"]).contains("Password"));
    want_contains(&e.must_run(&["wifi", "show", "ssid-4"]), "WPA-Enterprise");
    want_contains(&e.want_fail(1, &["wifi", "show", "nope"]), "no Wi-Fi network matches \"nope\"");
}

#[test]
fn wifi_qr() {
    let mut e = env_with_site();
    let (out, err, code) = e.run(&["wifi", "qr", "Home-5G"]);
    assert_eq!((code, err.as_str()), (0, "Scan to join Home-5G.\n"));
    let lines: Vec<&str> = out.lines().collect();
    assert_eq!(lines.len(), 17, "a 29-module code with a margin of two, two modules per row");
    assert!(lines.iter().all(|l| l.chars().count() == 33), "{out}");
    assert!(lines[0].chars().all(|c| c == '█'), "the margin is light");
    assert!(!out.contains('\x1b'), "no color outside a terminal");

    e.must_run(&["wifi", "qr", "Guests"]);
    want_contains(
        &e.want_fail(1, &["wifi", "qr", "Staff"]),
        "no QR code for this network: WPA-Enterprise networks use a login per person",
    );
}

#[test]
fn alerts() {
    let mut e = env_with_site();
    e.golden_run("alert-list", &["alert", "list"]);
    e.golden_run("alert-list-json", &["alert", "list", "-o", "json"]);
    let open = e.must_run(&["alert", "list", "--unresolved"]);
    assert!(open.contains("alert-1") && !open.contains("alert-3"), "{open}");
    // Older than 30 days: not listed, not found.
    assert!(!e.must_run(&["alert", "list"]).contains("alert-4"));
    want_contains(&e.want_fail(1, &["alert", "show", "alert-4"]), "no alert with ID \"alert-4\" in the last 30 days");

    e.golden_run("alert-show", &["alert", "show", "alert-2"]);
    want_contains(&e.must_run(&["alert", "show", "alert-3", "-o", "json"]), "\"resolved\": true");
}

#[test]
fn device_firmware_and_updates() {
    let mut e = env_with_site();
    e.golden_run("device-firmware", &["device", "firmware", "AP office"]);
    e.golden_run("device-firmware-json", &["device", "firmware", "AP office", "-o", "json"]);
    want_contains(&e.must_run(&["device", "firmware", "Core router"]), "Latest:     up to date");
    e.golden_run("device-list-updates", &["device", "list", "--updates"]);
    want_contains(&e.must_run(&["device", "list", "--updates", "-o", "json"]), "\"update_available\": true");

    e.fake.with(|s| s.firmware.get_mut("AA-00-00-00-00-03").unwrap()["lastFwVer"] = "1.0.14".into());
    assert_eq!(
        e.run(&["device", "list", "--updates"]),
        (String::new(), "Every connected device is up to date.\n".into(), 0)
    );
}

#[test]
fn device_upgrade_waits_for_the_device() {
    let mut e = env_with_site();
    e.fake.with(|s| s.upgrade_polls = 3);
    want_contains(&e.want_fail(2, &["device", "upgrade", "AP office"]), "pass --yes");
    assert!(!e.fake.called("POST /sites/site-1/devices/AA-00-00-00-00-03/start-online-upgrade"));

    let (out, err, code) = e.run(&["device", "upgrade", "AP office", "--yes"]);
    assert_eq!(code, 0, "{err}");
    assert_eq!(out, "AP office now runs firmware 1.1.2.\n");
    want_contains(&err, "Updating AP office to 1.1.2. This takes a few minutes; do not unplug the device.");
    assert_eq!(e.fake.with(|s| s.calls.iter().filter(|c| c.ends_with("/online-upgrade-result")).count()), 4);

    // Nothing left to do is not an error, and nothing is started.
    assert_eq!(
        e.must_run(&["device", "upgrade", "AP office", "--yes"]),
        "AP office is already up to date (firmware 1.1.2).\n"
    );
    assert_eq!(e.fake.with(|s| s.calls.iter().filter(|c| c.ends_with("/start-online-upgrade")).count()), 1);
}

#[test]
fn device_upgrade_no_wait_and_failure() {
    let mut e = env_with_site();
    assert_eq!(
        e.must_run(&["device", "upgrade", "AP office", "--yes", "--no-wait"]),
        "Updating AP office to 1.1.2. It restarts when it is done.\n"
    );
    assert!(!e.fake.with(|s| s.calls.iter().any(|c| c.ends_with("/online-upgrade-result"))));
    want_contains(&e.must_run(&["device", "list"]), "updating");

    let mut e = env_with_site();
    e.fake.with(|s| s.fail_upgrades = true);
    want_contains(
        &e.want_fail(1, &["device", "upgrade", "AP office", "--yes"]),
        "the update of AP office did not succeed",
    );
}

#[test]
fn controller_down() {
    let mut e = env_with_site();
    let url = e.fake.url.clone();
    e.keychain.values.lock().unwrap().remove("home:token"); // force a login
    drop(std::mem::replace(&mut e.fake, fake::Controller::start())); // the old one is gone
    let err = e.want_fail(4, &["--timeout", "2s", "device", "list"]);
    want_contains(&err, "controller unreachable");
    want_contains(&err, "Check the controller address");
    let _ = url;
}

#[test]
fn secret_changed_on_the_controller() {
    let mut e = env_with_site();
    e.keychain.values.lock().unwrap().insert("home".into(), "old-secret".into());
    // The cached token still works, so nothing is noticed yet.
    e.must_run(&["device", "list"]);
    // Once the token is gone, the stale secret is refused: exit code 3.
    e.fake.expire_tokens();
    let err = e.want_fail(3, &["device", "list"]);
    want_contains(&err, "authentication failed");
}

#[test]
fn token_is_cached_across_commands() {
    let mut e = env_with_site();
    let logins = e.fake.logins();
    e.must_run(&["device", "list"]);
    e.must_run(&["client", "list"]);
    assert_eq!(e.fake.logins(), logins, "the cached token is reused");

    // A token the controller no longer accepts is replaced without a fuss.
    e.fake.expire_tokens();
    e.must_run(&["device", "list"]);
    assert_eq!(e.fake.logins(), logins + 1);

    // --no-token-cache logs in every time and stores nothing new.
    e.must_run(&["--no-token-cache", "device", "list"]);
    e.must_run(&["--no-token-cache", "device", "list"]);
    assert_eq!(e.fake.logins(), logins + 3);
}

#[test]
fn broken_config() {
    let mut e = Env::new();
    std::fs::write(&e.path, "hosts: [not, a, map]\n").unwrap();
    let err = e.want_fail(1, &["host", "list"]);
    want_contains(&err, "is not a valid config file");
    want_contains(&err, "config.yaml");
}
