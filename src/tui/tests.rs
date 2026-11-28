//! Drives the model the way the runtime would, but synchronously: each
//! command runs to completion and its message is fed straight back in.
//! That makes every test deterministic. Screens are drawn with ratatui's
//! test backend and compared with the files in tests/testdata/tui.

use std::path::PathBuf;
use std::sync::atomic::Ordering;
use std::sync::Arc;

use chrono::{TimeZone, Utc};
use ratatui::backend::TestBackend;
use ratatui::Terminal;
use serde_json::json;

use super::fake::{self, Controller};
use super::{Cmd, Key, Model, Msg, Options};
use crate::app::{self, App, NewHost};
use crate::secret::MemoryKeychain;

struct Harness {
    fake: Controller,
    dir: tempfile::TempDir,
    keychain: Arc<MemoryKeychain>,
    app: Arc<App>,
    m: Model,
    size: (u16, u16),
}

fn new_app(dir: &tempfile::TempDir, keychain: &Arc<MemoryKeychain>) -> Arc<App> {
    App::new(app::Options {
        config_path: Some(dir.path().join("config.yaml")),
        now: Some(Arc::new(|| Utc.timestamp_millis_opt(fake::EPOCH_MS).unwrap())),
        keychain: Some(keychain.clone()),
        ..Default::default()
    })
    .unwrap()
}

impl Harness {
    /// Prepares a fake controller, an empty config, and an in-memory
    /// keychain, and adds the named hosts.
    fn new(hosts: &[&str]) -> Harness {
        crate::output::show_times_in_utc();
        let fake = Controller::start();
        let dir = tempfile::tempdir().unwrap();
        let keychain = Arc::new(MemoryKeychain::default());
        let app = new_app(&dir, &keychain);
        for name in hosts {
            app.add_host(&NewHost {
                name: name.to_string(),
                url: fake.url.clone(),
                client_id: fake::CLIENT_ID.into(),
                secret: fake::SECRET.into(),
                ..Default::default()
            })
            .unwrap();
        }
        let m = Model::new(app.clone(), &Options::default(), false);
        Harness { fake, dir, keychain, app, m, size: (80, 24) }
    }

    /// Leaves the fake controller with a single site, so it is opened
    /// without asking.
    fn one_site(self) -> Harness {
        self.fake.with(|s| s.sites.truncate(1));
        self
    }

    /// Creates the model at the given terminal size and runs its first
    /// commands.
    fn start(mut self, width: u16, height: u16, opts: Options) -> Harness {
        self.size = (width, height);
        self.m = Model::new(self.app.clone(), &opts, false);
        let cmds = self.m.init();
        self.run(cmds);
        self
    }

    fn run(&mut self, cmds: Vec<Cmd>) {
        for cmd in cmds {
            if let Some(msg) = cmd() {
                self.send(msg);
            }
        }
    }

    fn send(&mut self, msg: Msg) {
        let cmds = self.m.update(msg);
        self.run(cmds);
    }

    /// Presses keys given by name ("enter", "esc", "tab", "shift+tab",
    /// "up", ..., "ctrl+c") or as a single character.
    fn press(&mut self, keys: &[&str]) {
        for name in keys {
            let mut chars = name.chars();
            let text = match (chars.next(), chars.next()) {
                (Some(c), None) => Some(c),
                _ => None,
            };
            self.send(Msg::Key(Key { name: name.to_string(), text }));
        }
    }

    /// Types text into the open prompt.
    fn type_text(&mut self, text: &str) {
        for c in text.chars() {
            self.send(Msg::Key(Key { name: c.to_string(), text: Some(c) }));
        }
    }

    fn lines(&mut self) -> Vec<String> {
        let (w, h) = self.size;
        let mut terminal = Terminal::new(TestBackend::new(w, h)).unwrap();
        terminal.draw(|frame| self.m.draw(frame)).unwrap();
        let buf = terminal.backend().buffer();
        (0..h).map(|y| (0..w).map(|x| buf[(x, y)].symbol()).collect::<String>().trim_end().to_string()).collect()
    }

    fn screen(&mut self) -> String {
        self.lines().join("\n")
    }

    /// Returns the bottom line.
    fn footer(&mut self) -> String {
        self.lines().pop().unwrap_or_default()
    }

    #[track_caller]
    fn want_screen(&mut self, wants: &[&str]) {
        let screen = self.screen();
        for want in wants {
            assert!(screen.contains(want), "the screen does not show {want:?}:\n{screen}");
        }
    }

    #[track_caller]
    fn want_not_on_screen(&mut self, texts: &[&str]) {
        let screen = self.screen();
        for text in texts {
            assert!(!screen.contains(text), "the screen shows {text:?}:\n{screen}");
        }
    }

    #[track_caller]
    fn want_footer(&mut self, want: &str) {
        let footer = self.footer();
        assert!(footer.contains(want), "footer = {footer:?}, want {want:?}\n{}", self.screen());
    }

    /// Compares the screen with a file in tests/testdata/tui.
    #[track_caller]
    fn golden(&mut self, name: &str) {
        let got = self.screen() + "\n";
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/testdata/tui").join(format!("{name}.golden"));
        if std::env::var_os("UPDATE_GOLDEN").is_some() {
            std::fs::write(&path, &got).unwrap();
            return;
        }
        let want = std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()));
        assert!(got == want, "screen differs from {}\ngot:\n{got}\nwant:\n{want}", path.display());
    }
}

const LAPTOP: &str = "CC-00-00-00-00-01";

#[test]
fn one_host_one_site_opens_devices() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    h.golden("devices-100x12");
    assert!(!h.m.quit);
}

#[test]
fn small_screen_scrolls_sideways() {
    let mut h = Harness::new(&["home"]).one_site().start(34, 8, Options::default());
    h.golden("devices-34x8");
    h.press(&["l"]);
    h.golden("devices-34x8-right");
    h.press(&["right", "l", "l", "l", "l", "l", "l", "l", "l"]); // far past the last column
    h.want_screen(&["‹    NAME         MAC", "Core router  AA-00-00-00-00-0"]);
    h.press(&["h", "left", "h", "h", "h", "h", "h", "h", "h", "h"]);
    h.golden("devices-34x8");
}

#[test]
fn clients_tab() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    h.press(&["tab"]);
    h.golden("clients-100x12");
    h.press(&["shift+tab"]);
    h.want_screen(&["[Devices]", "Core router"]);
    h.press(&["shift+tab"]); // wraps around to the last tab
    h.want_screen(&["[Alerts (2)]"]);
    h.press(&["tab", "tab"]);
    h.want_screen(&["[Clients]", "Laptop"]);
}

#[test]
fn details_and_help() {
    let mut h = Harness::new(&["home"]).one_site().start(60, 16, Options::default());
    h.press(&["enter"]);
    h.golden("device-details-60x16");
    h.press(&["x"]); // any key goes back
    h.want_screen(&["[Devices]", "Rack switch"]);

    h.press(&["?"]);
    h.golden("help-60x16");
    h.press(&["j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j"]); // scrolls to the end and stops
    h.want_screen(&["q         quit", "1-4       jump to a tab"]);
    h.press(&["k"]);
    h.want_not_on_screen(&["1-4       jump to a tab"]);
    h.press(&["x"]);
    h.want_screen(&["[Devices]"]);
}

#[test]
fn first_run_wizard() {
    let mut h = Harness::new(&[]).one_site().start(80, 20, Options::default());
    h.want_screen(&["gomada › add a controller", "Pick a short name"]);
    h.want_footer("Name: █");

    h.type_text("bad name");
    h.press(&["enter"]);
    h.want_screen(&["Error: invalid host name"]);
    h.press(&["ctrl+u"]);
    h.type_text("home");
    h.press(&["enter"]);
    h.want_screen(&["Name: home", "The address you open the controller at"]);

    let url = h.fake.url.clone();
    h.type_text(&url); // plain http: no certificate to confirm
    h.press(&["enter"]);
    h.want_screen(&[&format!("Address: {url}"), "Create an Open API application"]);
    h.press(&["enter"]);
    h.want_screen(&["Error: The client ID must not be empty."]);
    h.type_text(fake::CLIENT_ID);
    h.press(&["enter"]);
    h.want_screen(&["The secret is not shown while you type"]);

    h.type_text("wrong");
    h.want_footer("Client secret: •••••█");
    h.want_not_on_screen(&["wrong"]);
    h.press(&["enter"]);
    h.want_screen(&["Error: authentication failed"]);
    h.want_footer(&format!("Client ID: {}█", fake::CLIENT_ID)); // back at the question that may be wrong
    h.press(&["enter"]);
    h.send(Msg::Paste(format!("{}\r\n", fake::SECRET))); // a paste, with its line break dropped
    h.press(&["enter"]);

    // The host is saved and opened.
    h.want_screen(&["home › Default", "[Devices]", "Core router"]);
    assert_eq!(h.app.hosts().len(), 1);
    assert_eq!(h.keychain.values.lock().unwrap().get("home").map(String::as_str), Some(fake::SECRET));
}

#[test]
fn wizard_cancel_without_hosts_quits() {
    let mut h = Harness::new(&[]).start(80, 20, Options::default());
    h.press(&["esc"]);
    assert!(h.m.quit);
}

#[test]
fn wizard_unreachable_controller() {
    let mut h = Harness::new(&[]).start(80, 20, Options::default());
    h.type_text("home");
    h.press(&["enter"]);
    h.type_text("https://127.0.0.1:1");
    h.press(&["enter"]);
    h.want_screen(&["Error: controller unreachable"]);
    h.want_footer("Address: https://127.0.0.1:1█");
    assert!(!h.m.quit);
}

#[test]
fn wizard_without_keychain_asks_for_a_passphrase() {
    let mut h = Harness::new(&[]).one_site().start(90, 20, Options::default());
    h.keychain.unavailable.store(true, Ordering::SeqCst);
    let url = h.fake.url.clone();
    for answer in ["server", url.as_str(), fake::CLIENT_ID, fake::SECRET] {
        h.type_text(answer);
        h.press(&["enter"]);
    }
    h.want_screen(&["This system has no keychain"]);
    h.want_footer("Choose a passphrase: █");
    h.type_text("one");
    h.press(&["enter"]);
    h.want_footer("Repeat the passphrase: █");
    h.type_text("two");
    h.press(&["enter"]);
    h.want_screen(&["Error: The two passphrases differ."]);
    for _ in 0..2 {
        h.type_text("correct horse");
        h.press(&["enter"]);
    }
    h.want_screen(&["server › Default", "Core router"]);
    assert!(h.dir.path().join("secrets").join("server.secret.json").exists());
    assert_eq!(h.app.hosts()[0].secret_store, "file");
}

#[test]
fn passphrase_prompt_on_connect() {
    // A host whose secret is in an encrypted file, set up earlier.
    let h = Harness::new(&[]).one_site();
    h.keychain.unavailable.store(true, Ordering::SeqCst);
    h.app.set_passphrase(Arc::new(|_| Ok("correct horse".to_string())));
    h.app
        .add_host(&NewHost {
            name: "server".into(),
            url: h.fake.url.clone(),
            client_id: fake::CLIENT_ID.into(),
            secret: fake::SECRET.into(),
            secret_store: "file".into(),
            ..Default::default()
        })
        .unwrap();

    let mut h = h.start(90, 12, Options::default());
    h.want_screen(&["The client secret of server is encrypted with a passphrase."]);
    h.want_footer("Passphrase for server: █");
    h.type_text("wrong");
    h.want_footer("Passphrase for server: •••••█");
    h.press(&["enter"]);
    h.want_footer("Passphrase for server: █"); // asked again
    h.type_text("correct horse");
    h.press(&["enter"]);
    h.want_screen(&["server › Default", "Core router"]);

    // An empty answer gives up and shows the hosts.
    let mut h = h.start(90, 12, Options::default());
    h.press(&["enter"]);
    h.want_screen(&["Hosts", "server"]);
}

#[test]
fn several_hosts_show_the_host_list() {
    let mut h = Harness::new(&["home", "office"]).one_site().start(100, 12, Options::default());
    h.want_screen(&["Hosts", "1/2", "▸ home", "  office", "enter open · a add · x remove"]);
    h.press(&["j", "enter"]);
    h.want_screen(&["office › Default", "[Devices]"]);
    h.press(&["H"]);
    h.want_screen(&["Hosts", "▸ office"]); // the cursor is on the open host
    h.press(&["esc"]);
    h.want_screen(&["office › Default"]);
}

#[test]
fn host_and_site_from_options() {
    let opts = Options { host: "office".into(), site: "workshop".into(), no_color: false };
    let mut h = Harness::new(&["home", "office"]).start(100, 12, opts);
    h.want_screen(&["office › Workshop", "Workshop AP"]);

    let opts = Options { host: "office".into(), site: "nope".into(), no_color: false };
    let mut h = Harness::new(&["home", "office"]).start(100, 12, opts);
    h.want_screen(&["office › sites", "Default", "Workshop"]);
    h.want_footer("Error: no site named \"nope\"");

    let opts = Options { host: "nope".into(), ..Default::default() };
    let mut h = Harness::new(&["home", "office"]).start(100, 12, opts);
    h.want_screen(&["Hosts"]);
    h.want_footer("Error: could not open nope: unknown host");
}

#[test]
fn last_site_is_reopened_and_sites_can_be_switched() {
    let mut h = Harness::new(&["home"]).start(100, 12, Options::default());
    // Two sites and none used before: the person chooses.
    h.want_screen(&["home › sites", "▸ Default", "  Workshop"]);
    h.press(&["j", "enter"]);
    h.want_screen(&["home › Workshop", "Workshop AP"]);
    assert_eq!(h.app.hosts()[0].site, "Workshop");

    // The next start goes straight there.
    let mut h = h.start(100, 12, Options::default());
    h.want_screen(&["home › Workshop", "Workshop AP"]);
    h.press(&["S"]);
    h.want_screen(&["home › sites", "▸ Workshop"]);
    h.press(&["k", "enter"]);
    h.want_screen(&["home › Default", "Core router"]);
    // R on the site list fetches the sites again and stays there.
    h.press(&["S", "R"]);
    h.want_screen(&["home › sites"]);
}

#[test]
fn reboot_asks_first() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    let call = "POST /sites/site-1/devices/AA-00-00-00-00-02/reboot";
    h.press(&["j", "r"]);
    h.want_footer("Reboot Rack switch (switch)? y/N");
    h.press(&["n"]);
    h.want_footer("Canceled.");
    assert!(!h.fake.called(call));
    h.press(&["r", "enter"]); // only y means yes
    assert!(!h.fake.called(call));
    h.press(&["r", "y"]);
    h.want_footer("Rebooting Rack switch.");
    assert!(h.fake.called(call));
}

#[test]
fn rename_device() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    h.press(&["G", "n"]);
    h.want_footer("New name for AP office: AP office█");
    h.press(&["esc"]);
    h.want_footer("Canceled.");
    h.press(&["n", "ctrl+u", "enter"]);
    h.want_footer("Canceled: nothing was entered.");
    h.press(&["n", "backspace", "backspace", "backspace", "backspace", "backspace", "backspace"]);
    h.type_text("attic ");
    h.press(&["enter"]);
    h.want_footer("Renamed AP office to AP attic.");
    assert_eq!(h.fake.device_name("site-1", "AA-00-00-00-00-03"), "AP attic");
    h.want_screen(&["▸ ●  AP attic"]); // reloaded, and the cursor followed the device

    // A name the controller would refuse is refused before any request.
    h.press(&["n", "ctrl+u"]);
    h.type_text("-x");
    h.press(&["enter"]);
    h.want_footer("Error: the name must not start with");
}

#[test]
fn client_actions() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    h.press(&["2", "j", "c"]);
    h.want_footer("Error: NAS is a wired client");
    h.press(&["k", "c"]);
    h.want_footer("Asked Laptop to reconnect.");
    h.press(&["G", "n"]);
    h.type_text("Printer");
    h.press(&["enter"]);
    h.want_footer("Renamed printer to Printer.");
    assert_eq!(h.fake.client_name("site-1", "CC-00-00-00-00-03"), "Printer");
    h.press(&["enter"]);
    h.want_screen(&["Host name     printer", "Connected to  AP office · IoT", "Signal        -67 dBm"]);
}

#[test]
fn block_and_unblock() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    h.press(&["2"]);
    h.want_screen(&["[Clients]", "1/3", "B blocked"]);
    h.press(&["b"]);
    h.want_footer(&format!("Block Laptop ({LAPTOP})? y/N"));
    h.press(&["y"]);
    h.want_footer("Blocked Laptop. Press B to see the blocked clients.");
    assert!(h.fake.with(|s| s.blocked.contains(LAPTOP)));
    h.want_not_on_screen(&["Laptop  "]); // a blocked client is cut off and leaves the list
    h.want_screen(&["1/2"]);

    h.press(&["B"]);
    h.golden("blocked-100x12");
    h.press(&["enter"]);
    h.want_screen(&["State        blocked", "Type         wireless"]);
    h.press(&["x", "u"]);
    h.want_footer(&format!("Unblock Laptop ({LAPTOP})? y/N"));
    h.press(&["y"]);
    h.want_footer("Unblocked Laptop.");
    assert!(h.fake.with(|s| s.blocked.is_empty()));
    h.want_screen(&["No blocked clients here."]);

    h.press(&["B"]);
    h.want_screen(&["[Clients]", "Laptop"]);

    // Leaving and reopening the site starts on the connected clients again.
    h.press(&["B", "S", "enter", "2"]);
    h.want_screen(&["[Clients]"]);
}

#[test]
fn wifi_tab() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    h.press(&["3"]);
    h.golden("wifi-100x12");
    h.press(&["j", "p"]);
    h.want_footer("Password of Home-5G: correct horse battery");
    h.press(&["k"]); // the next key press takes the password off the screen
    h.want_not_on_screen(&["correct horse"]);
    h.press(&["p"]); // Guests is open
    h.want_footer("Guests has no shared password (open).");
    h.press(&["G", "p"]);
    h.want_footer("Staff has no shared password (WPA-Enterprise).");
}

#[test]
fn wifi_qr_code() {
    let mut h = Harness::new(&["home"]).one_site().start(60, 28, Options::default());
    h.press(&["3", "j", "enter"]); // Home-5G
    h.golden("wifi-qr-60x28");
    h.want_not_on_screen(&["correct horse"]);
    h.press(&["p"]);
    h.want_screen(&["Password       correct horse battery"]);
    h.press(&["p"]);
    h.want_screen(&["Password       hidden; press p to show"]);
    h.press(&["x"]);
    h.want_screen(&["[Wi-Fi]", "Guests"]);

    // An open network has a code and no password; one with a login per
    // person has neither.
    h.press(&["g", "enter"]);
    h.want_screen(&["Guests", "██ ▄▄▄▄▄ █"]);
    h.want_not_on_screen(&["Password"]);
    h.press(&["x", "G", "enter"]);
    h.want_screen(&[
        "No QR code for this network: WPA-Enterprise networks use a",
        "login per person, not one shared password.",
    ]);
}

#[test]
fn wifi_qr_code_sizes() {
    // 29 modules: half blocks need 17 or 16 rows, sextants 11.
    let sextants = |screen: &str| screen.chars().any(|c| ('\u{1FB00}'..='\u{1FB3B}').contains(&c));
    for (height, half_blocks, sextant, note) in
        [(22u16, true, false, false), (16, false, true, false), (10, false, false, true)]
    {
        let mut h = Harness::new(&["home"]).one_site().start(60, height, Options::default());
        h.press(&["3", "j", "enter"]);
        let screen = h.screen();
        assert_eq!(screen.contains("██ ▄▄▄▄▄ █"), half_blocks, "height {height}:\n{screen}");
        assert_eq!(sextants(&screen), sextant, "height {height}:\n{screen}");
        assert_eq!(screen.contains("too small for the QR code"), note, "height {height}:\n{screen}");
    }
    // Too small for the code: p still shows the password, scrolled into view.
    let mut h = Harness::new(&["home"]).one_site().start(60, 8, Options::default());
    h.press(&["3", "j", "enter", "p"]);
    h.want_screen(&["Password       correct horse battery"]);
}

#[test]
fn wifi_password_cannot_be_read() {
    let mut h = Harness::new(&["home"]).one_site().start(80, 28, Options::default());
    h.fake.with(|s| s.ssids.get_mut("wlan-1").unwrap()[0]["pskSetting"] = json!({}));
    h.press(&["3", "j", "enter"]);
    h.want_screen(&[
        "No QR code for this network: the controller did not reveal the password",
        "Password       not revealed by the controller",
    ]);
}

#[test]
fn stale_wifi_answers_are_ignored() {
    let mut h = Harness::new(&["home"]).one_site().start(80, 28, Options::default());
    h.press(&["3"]);
    h.send(Msg::Wifi { ssid_id: "ssid-1".into(), result: Ok("leak".into()) }); // no details open
    h.want_not_on_screen(&["leak"]);
    h.press(&["enter"]); // Guests
    h.send(Msg::Wifi { ssid_id: "ssid-1".into(), result: Ok("leak".into()) }); // the answer for another network
    h.press(&["p"]);
    h.want_not_on_screen(&["leak"]);
}

#[test]
fn alerts_tab() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    h.want_screen(&["Alerts (2)"]); // the count shows from any tab
    h.press(&["4"]);
    h.golden("alerts-100x12");
    h.press(&["j", "enter"]);
    h.golden("alert-details-100x12");
    h.press(&["x", "l"]);
    h.want_screen(&["‹    LEVEL        MESSAGE"]); // the symbol stays, the time scrolls out
}

#[test]
fn alert_count_follows_the_controller_while_on_another_tab() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    h.want_screen(&["Alerts (2)"]);
    h.fake.with(|s| {
        s.alerts.insert("site-1".into(), vec![json!({"id": "x", "level": "Error", "content": "new", "resolved": false, "time": fake::EPOCH_MS - 60_000})]);
    });
    h.send(Msg::Tick);
    h.want_screen(&["[Devices]", "Alerts (1)"]);
}

#[test]
fn firmware() {
    let mut h = Harness::new(&["home"]).one_site().start(120, 20, Options::default());
    const AP: &str = "AA-00-00-00-00-03";
    h.golden("devices-firmware-120x20");
    // One question per connected device, and not again on reload.
    let asked = |h: &Harness| h.fake.with(|s| s.calls.iter().filter(|c| c.ends_with("/latest-firmware-info")).count());
    assert_eq!(asked(&h), 3);
    h.press(&["R"]);
    h.send(Msg::Tick);
    assert_eq!(asked(&h), 3);

    h.press(&["G", "enter"]);
    h.want_screen(&[
        "Latest firmware  1.1.2 (update available)",
        "Release notes    1. Improved roaming between access points.",
        "                 2. Fixed a restart under heavy load.",
    ]);
    h.press(&["x", "U"]);
    h.want_footer("Update AP office from 1.0.14 to 1.1.2? It restarts. Type its name to confirm: █");
    h.type_text("ap office"); // not the name, exactly
    h.press(&["enter"]);
    h.want_footer("Error: the name did not match \"AP office\"; nothing was updated");
    assert!(!h.fake.called(&format!("POST /sites/site-1/devices/{AP}/start-online-upgrade")));

    h.press(&["U"]);
    h.type_text("AP office");
    h.press(&["enter"]);
    h.want_footer("Updating AP office to 1.1.2. It restarts when it is done; do not unplug it.");
    assert!(h.fake.called(&format!("POST /sites/site-1/devices/{AP}/start-online-upgrade")));
    h.want_screen(&["updating"]);

    // A device that is up to date says so instead of failing.
    h.press(&["g", "U"]);
    h.type_text("Core router");
    h.press(&["enter"]);
    h.want_footer("Core router is already up to date (firmware 1.2.3).");
    // An offline device cannot be updated.
    h.press(&["j", "j", "U"]);
    h.type_text("AP garage");
    h.press(&["enter"]);
    h.want_footer("Error: AP garage is offline; only a connected device can be updated");
}

#[test]
fn tab_bar_on_a_narrow_screen() {
    let mut h = Harness::new(&["home"]).one_site().start(60, 16, Options::default());
    h.want_screen(&["home › Default  [Devices]  Clients   Wi-Fi  ›            1/4"]);
    h.press(&["4"]);
    h.want_screen(&["[Alerts (2)]", "‹"]);
    let mut h = Harness::new(&["home"]).one_site().start(20, 8, Options::default());
    assert_eq!(h.lines()[0], "home ›…  [Devices] ›"); // the host and site give way to the tab
    let mut h = Harness::new(&["home"]).one_site().start(9, 8, Options::default());
    assert_eq!(h.lines()[0], "[Devices]");
}

#[test]
fn filter_and_sort() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    h.press(&["/"]);
    h.type_text("ap");
    h.want_footer("/ap█");
    h.want_screen(&["1/2", "AP garage", "AP office"]);
    h.want_not_on_screen(&["Core router"]);
    h.press(&["enter"]);
    h.want_footer("filter: ap (esc clears)");
    h.press(&["/"]);
    h.type_text("zzz");
    h.want_screen(&["No device matches the filter. Press esc to clear it.", "0/0"]);
    h.press(&["r"]); // typed into the filter, not an action
    h.want_footer("/apzzzr█");
    h.press(&["esc"]); // esc while typing clears the filter
    h.want_screen(&["1/4", "Core router"]);

    h.press(&["o"]);
    h.want_footer("Sorted by name.");
    h.want_screen(&["NAME▲"]);
    assert!(h.lines()[2].contains("AP garage"), "{}", h.screen());
    for _ in 0..7 {
        h.press(&["o"]);
    }
    h.want_footer("Sorted by cpu.");
    assert!(h.lines()[2].contains("AP garage") && h.lines()[3].contains("AP office"), "{}", h.screen());
    h.press(&["o", "o", "o"]);
    h.want_footer("Back to the usual order.");
}

#[test]
fn remove_hosts() {
    let mut h = Harness::new(&["home", "office"]).one_site().start(100, 12, Options::default());
    h.press(&["enter", "H", "x"]);
    h.want_footer("Remove host home and its stored secret? y/N");
    h.press(&["y"]);
    h.want_footer("Removed host home.");
    h.want_screen(&["1/1", "▸ office"]);
    h.press(&["esc"]); // the open host is gone: nothing to go back to
    h.want_screen(&["Hosts"]);
    // Removing the last host leads to the wizard.
    h.press(&["x", "y"]);
    h.want_screen(&["gomada › add a controller"]);
    assert!(h.keychain.values.lock().unwrap().is_empty());
}

#[test]
fn add_host_from_the_host_list() {
    let mut h = Harness::new(&["home", "office"]).one_site().start(100, 12, Options::default());
    h.press(&["a"]);
    h.want_screen(&["gomada › add a controller"]);
    h.type_text("home");
    h.press(&["enter"]);
    h.want_screen(&["Error: a host named \"home\" already exists"]);
    h.press(&["esc"]); // back to the list, not out of the program
    h.want_screen(&["Hosts", "1/2"]);
    assert!(!h.m.quit);
}

#[test]
fn controller_down_at_start() {
    let mut h = Harness::new(&["home"]).one_site();
    h.keychain.values.lock().unwrap().remove("home:token");
    let dead = std::mem::replace(&mut h.fake, Controller::start());
    drop(dead);
    let mut h = h.start(100, 12, Options::default());
    h.want_screen(&["Hosts", "▸ home"]);
    h.want_footer("Error: could not open home: controller unreachable");
    h.press(&["j"]); // the next key press clears the message
    h.want_footer("enter open · a add · x remove");
}

#[test]
fn load_error_keeps_rows() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    h.fake.with(|s| s.devices.clear()); // the site is gone
    h.press(&["R"]);
    h.want_screen(&["Core router"]);
    h.want_footer("Error: This site does not exist. (code -33004)");
    // A tab that never loaded says so in place of its rows.
    h.press(&["2"]);
    h.want_screen(&["Could not load the clients: This site does not exist. (code -33004)", "Press R to try again."]);
}

#[test]
fn expired_token_is_renewed_silently() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    let logins = h.fake.logins();
    h.fake.expire_tokens();
    h.press(&["R"]);
    h.want_screen(&["Core router"]);
    h.want_not_on_screen(&["Error"]);
    assert_eq!(h.fake.logins(), logins + 1);
}

#[test]
fn empty_site() {
    let opts = Options { site: "Workshop".into(), ..Default::default() };
    let mut h = Harness::new(&["home"]).start(100, 12, opts);
    h.press(&["2"]);
    h.want_screen(&["No clients here.", "0/0"]);
    h.press(&["n"]);
    h.want_footer("Select a client first.");
    h.press(&["enter", "j", "G"]); // nothing to open or move to
    h.want_screen(&["No clients here."]);
}

#[test]
fn periodic_reload() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    h.fake.with(|s| s.devices.get_mut("site-1").unwrap()[0]["name"] = "Renamed elsewhere".into());
    h.send(Msg::Tick);
    h.want_screen(&["Renamed elsewhere"]);
    // Not while something is open on top.
    h.fake.with(|s| s.devices.get_mut("site-1").unwrap()[0]["name"] = "Again".into());
    h.press(&["?"]);
    h.send(Msg::Tick);
    h.press(&["x"]);
    h.want_not_on_screen(&["Again"]);
}

#[test]
fn stale_answers_are_ignored() {
    let mut h = Harness::new(&["home", "office"]).one_site().start(100, 12, Options::default());
    h.press(&["enter"]);
    let old = h.m.sess.clone().unwrap();
    h.press(&["H", "j", "enter"]);
    h.want_screen(&["office › Default"]);
    // Rows of the host that was open before arrive late.
    h.send(Msg::Rows {
        view: super::views::View::Devices,
        sess: old,
        site_id: "site-1".into(),
        result: Err(crate::error::Error::other("late")),
        enriched: false,
    });
    h.want_not_on_screen(&["late"]);
    // So does the answer of a connection nobody is waiting for.
    h.send(Msg::Connected { host: "home".into(), result: Err(crate::error::Error::other("late")) });
    h.want_screen(&["office › Default"]);
}

#[test]
fn quit() {
    for key in ["q", "ctrl+c"] {
        let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
        h.press(&[key]);
        assert!(h.m.quit, "{key}");
    }
    // q typed into a prompt is text; ctrl+c still quits.
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    h.press(&["/", "q"]);
    assert!(!h.m.quit);
    h.want_footer("/q█");
    h.press(&["ctrl+c"]);
    assert!(h.m.quit);
}

#[test]
fn the_screen_always_fits_the_terminal() {
    let mut h = Harness::new(&["home"]).one_site().start(100, 12, Options::default());
    let screens: [&[&str]; 9] = [
        &[],
        &["enter"],
        &["x", "?"],
        &["x", "2"],
        &["3", "j", "enter"],
        &["x", "4", "enter"],
        &["x", "/"],
        &["esc", "n"],
        &["esc", "S"],
    ];
    for keys in screens {
        h.press(keys);
        for (w, hh) in [(1u16, 1u16), (2, 2), (5, 3), (10, 1), (20, 4), (34, 8), (80, 24), (200, 60)] {
            h.size = (w, hh);
            let lines = h.lines(); // must not panic at any size
            assert_eq!(lines.len(), hh as usize);
            assert!(
                lines.iter().all(|l| super::text::width(l) <= w as usize),
                "{w}x{hh} after {keys:?}:\n{}",
                lines.join("\n")
            );
        }
    }
}

#[test]
fn long_input_keeps_the_end_visible() {
    let mut h = Harness::new(&["home"]).one_site().start(30, 8, Options::default());
    h.press(&["n"]);
    h.type_text(" and a very long new name that does not fit");
    let footer = h.footer();
    assert!(footer.starts_with('…') && footer.ends_with("does not fit█"), "{footer}");
    assert_eq!(super::text::width(&footer), 30);
}

#[test]
fn key_names() {
    use ratatui::crossterm::event::{KeyCode, KeyEvent, KeyEventKind, KeyModifiers};
    let name = |code, mods| Key::from_event(KeyEvent::new(code, mods)).map(|k| k.name);
    assert_eq!(name(KeyCode::Enter, KeyModifiers::NONE).as_deref(), Some("enter"));
    assert_eq!(name(KeyCode::BackTab, KeyModifiers::SHIFT).as_deref(), Some("shift+tab"));
    assert_eq!(name(KeyCode::Char('c'), KeyModifiers::CONTROL).as_deref(), Some("ctrl+c"));
    assert_eq!(name(KeyCode::Char('G'), KeyModifiers::SHIFT).as_deref(), Some("G"));
    assert_eq!(name(KeyCode::PageDown, KeyModifiers::NONE).as_deref(), Some("pgdown"));
    assert_eq!(name(KeyCode::F(5), KeyModifiers::NONE), None);
    // Typed characters carry their text; control keys do not.
    assert_eq!(Key::from_event(KeyEvent::new(KeyCode::Char('é'), KeyModifiers::NONE)).unwrap().text, Some('é'));
    assert_eq!(Key::from_event(KeyEvent::new(KeyCode::Char('u'), KeyModifiers::CONTROL)).unwrap().text, None);
    // Windows reports key releases too; they are not key presses.
    let release = KeyEvent::new_with_kind(KeyCode::Char('q'), KeyModifiers::NONE, KeyEventKind::Release);
    assert_eq!(Key::from_event(release), None);
}
