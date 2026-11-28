//! The state of the terminal UI and how messages change it.

use std::sync::Arc;

use super::table::Table;
use super::text::upper_first;
use super::views::{self, ActionKind, Item, List, Tab, View};
use super::{Cmd, Key, Msg, Options, Passphrases};
use crate::app::{self, App, Certificate, HostInfo, NewHost, Session};
use crate::error::{Error, Result};
use crate::omada::{Site, Ssid};
use crate::output::{self, Field};
use crate::qr::Code;
use crate::secret;

/// What fills the terminal.
#[derive(Debug, Clone, Copy, PartialEq)]
pub(super) enum Screen {
    /// Waiting for a controller.
    Loading,
    /// The saved hosts.
    Hosts,
    /// The sites of the open host.
    Sites,
    /// The tabs of the open site.
    Main,
    /// Adding a host.
    Wizard,
}

/// What temporarily takes the keyboard on top of a screen.
#[derive(Debug, Clone, Copy, PartialEq)]
pub(super) enum Overlay {
    None,
    /// Typing a filter.
    Filter,
    /// Typing an answer.
    Prompt,
    /// Answering yes or no.
    Confirm,
    /// Reading the details of a row.
    Details,
    /// Reading the key list.
    Help,
}

/// What an open text prompt is for; it decides what enter and esc do.
#[derive(Debug, Clone)]
pub(super) enum PromptFor {
    WizName,
    WizUrl,
    WizClientId,
    WizSecret,
    WizPassphrase,
    WizPassphraseAgain {
        first: String,
    },
    /// The passphrase that unlocks a host's secret file.
    Passphrase {
        host: String,
    },
    /// The input of an action on a row.
    Action {
        kind: ActionKind,
        item: Box<Item>,
    },
}

/// What an open yes/no question is about.
#[derive(Debug, Clone)]
pub(super) enum ConfirmFor {
    TrustCertificate { fingerprint: String },
    Action { kind: ActionKind, item: Box<Item> },
}

/// A one-line text field with the cursor always at the end. That is all
/// the UI needs for a filter, a name, or a passphrase.
#[derive(Debug, Clone, Default)]
pub(super) struct LineInput {
    text: String,
    /// Show bullets instead of the text.
    pub masked: bool,
}

const CURSOR_BLOCK: &str = "█";

impl LineInput {
    fn new(initial: &str, masked: bool) -> LineInput {
        LineInput { text: initial.to_string(), masked }
    }

    pub fn value(&self) -> &str {
        &self.text
    }

    /// Applies a key press and reports whether it changed the text.
    fn key(&mut self, key: &Key) -> bool {
        match (key.name.as_str(), key.text) {
            ("backspace", _) => self.text.pop().is_some(),
            ("ctrl+u", _) => {
                self.text.clear();
                true
            }
            (_, Some(c)) => {
                self.text.push(c);
                true
            }
            _ => false,
        }
    }

    /// Appends pasted text, without line breaks.
    fn paste(&mut self, s: &str) {
        self.text.extend(s.chars().filter(|c| !matches!(c, '\r' | '\n')));
    }

    pub fn view(&self) -> String {
        if self.masked {
            "•".repeat(self.text.chars().count()) + CURSOR_BLOCK
        } else {
            format!("{}{CURSOR_BLOCK}", self.text)
        }
    }
}

/// The state of the "add a controller" screen. It asks one question at a
/// time in the bottom line, using the same prompt as everything else.
#[derive(Debug, Clone, Default)]
pub(super) struct Wizard {
    pub host: NewHost,
    /// Explains the current question.
    pub hint: String,
    /// Why the last answer was not accepted.
    pub err: String,
    /// What is being checked, while waiting for the controller.
    pub working: String,
}

const HINT_OPEN_API: &str = "Create an Open API application in the controller first: Settings > Platform Integration > Open API, in client credentials mode. It shows the client ID and the client secret.";

/// The state of the terminal UI.
pub(crate) struct Model {
    pub(super) app: Arc<App>,
    pass: Arc<Passphrases>,

    want_host: String, // host to open at start, from the command line
    want_site: String, // site to open at start, from the command line
    all_sites: bool,   // show the site list even when one site would be chosen

    pub(super) color: bool,
    pub(super) screen: Screen,
    pub(super) overlay: Overlay,

    pub(super) hosts: List,
    pub(super) sites: List,
    pub(super) tabs: Vec<Tab>,
    pub(super) tab: usize,

    pub(super) sess: Option<Arc<Session>>,
    pub(super) site: Site,
    connecting: String,      // host being opened
    pub(super) busy: String, // what the loading screen says

    pub(super) status: String,
    pub(super) status_err: bool,

    // Text prompt and yes/no question state.
    pub(super) input: LineInput,
    pub(super) input_label: String,
    prompt_for: Option<PromptFor>,
    pub(super) question: String,
    confirm_for: Option<ConfirmFor>,
    pub(super) detail_title: String,
    pub(super) detail: Vec<Field>,
    pub(super) detail_top: usize,

    // The Wi-Fi network whose details are open, and what is known of it.
    pub(super) wifi: Option<Ssid>,
    wifi_password: String,
    wifi_loaded: bool,
    /// `None` while loading and when there is no QR code.
    pub(super) wifi_code: Option<Code>,
    /// Shown in place of the QR code.
    pub(super) wifi_note: String,
    show_password: bool,

    pub(super) wiz: Wizard,
    /// Set when the person asked to leave.
    pub quit: bool,
}

fn cmd(f: impl FnOnce() -> Option<Msg> + Send + 'static) -> Vec<Cmd> {
    vec![Box::new(f)]
}

impl Model {
    /// Returns the model. `color` false draws without color, whatever the
    /// options and the environment say; tests rely on that.
    pub fn new(app: Arc<App>, opts: &Options, color: bool) -> Model {
        let pass = Arc::new(Passphrases::default());
        let lookup = pass.clone();
        app.set_passphrase(Arc::new(move |host| lookup.lookup(host)));
        let no_color_env = std::env::var_os("NO_COLOR").is_some_and(|v| !v.is_empty());
        Model {
            app,
            pass,
            want_host: opts.host.clone(),
            want_site: opts.site.clone(),
            all_sites: false,
            color: color && !opts.no_color && !no_color_env,
            screen: Screen::Loading,
            overlay: Overlay::None,
            hosts: views::hosts_list(),
            sites: views::sites_list(),
            tabs: views::tabs(),
            tab: 0,
            sess: None,
            site: Site::default(),
            connecting: String::new(),
            busy: String::new(),
            status: String::new(),
            status_err: false,
            input: LineInput::default(),
            input_label: String::new(),
            prompt_for: None,
            question: String::new(),
            confirm_for: None,
            detail_title: String::new(),
            detail: Vec::new(),
            detail_top: 0,
            wifi: None,
            wifi_password: String::new(),
            wifi_loaded: false,
            wifi_code: None,
            wifi_note: String::new(),
            show_password: false,
            wiz: Wizard::default(),
            quit: false,
        }
    }

    /// Decides the first screen: the wizard without hosts, the only host
    /// directly, or the host list when there are several.
    pub fn init(&mut self) -> Vec<Cmd> {
        let hosts = self.load_hosts();
        if hosts.is_empty() {
            self.start_wizard();
            Vec::new()
        } else if !self.want_host.is_empty() {
            self.connect(&self.want_host.clone())
        } else if let [only] = hosts.as_slice() {
            self.connect(&only.name)
        } else {
            self.screen = Screen::Hosts;
            self.hosts.table.select(&self.app.current_host());
            Vec::new()
        }
    }

    /// Refreshes the host list from the configuration.
    fn load_hosts(&mut self) -> Vec<HostInfo> {
        let hosts = self.app.hosts();
        let (rows, items) = views::host_rows(&hosts);
        self.hosts.table.set_rows(rows);
        self.hosts.items = items;
        self.hosts.loaded = true;
        hosts
    }

    /// Returns the list on screen, or `None` when the screen has none.
    pub(super) fn current(&self) -> Option<&List> {
        match self.screen {
            Screen::Hosts => Some(&self.hosts),
            Screen::Sites => Some(&self.sites),
            Screen::Main => Some(&self.active_tab().list),
            _ => None,
        }
    }

    pub(super) fn current_mut(&mut self) -> Option<&mut List> {
        match self.screen {
            Screen::Hosts => Some(&mut self.hosts),
            Screen::Sites => Some(&mut self.sites),
            Screen::Main => Some(&mut self.tabs[self.tab].active_mut().list),
            _ => None,
        }
    }

    fn table_mut(&mut self) -> Option<&mut Table> {
        self.current_mut().map(|l| &mut l.table)
    }

    /// Returns the tab on screen: the open tab, or its other view when that
    /// is switched on.
    pub(super) fn active_tab(&self) -> &Tab {
        self.tabs[self.tab].active()
    }

    fn view_mut(&mut self, view: View) -> Option<&mut Tab> {
        self.tabs.iter_mut().find_map(|t| {
            if t.view == view {
                Some(t)
            } else {
                t.alt.as_deref_mut().filter(|alt| alt.view == view)
            }
        })
    }

    /// Handles one message and returns the background work it starts.
    pub fn update(&mut self, msg: Msg) -> Vec<Cmd> {
        match msg {
            Msg::Key(key) => self.key(key),
            Msg::Paste(text) => {
                if matches!(self.overlay, Overlay::Prompt | Overlay::Filter) {
                    self.input.paste(&text);
                    self.apply_filter();
                }
                Vec::new()
            }
            Msg::Tick => {
                let mut cmds = Vec::new();
                if self.screen == Screen::Main && self.overlay == Overlay::None {
                    if !self.active_tab().list.loading {
                        cmds.extend(self.load_tab());
                    }
                    cmds.extend(self.load_background_tabs());
                }
                cmds
            }
            Msg::Connected { host, result } => self.connected(host, result),
            Msg::Rows { view, sess, site_id, result, enriched } => {
                self.loaded_rows(view, sess, site_id, result, enriched)
            }
            Msg::Wifi { ssid_id, result } => {
                self.wifi_loaded(&ssid_id, result);
                Vec::new()
            }
            Msg::Action(result) => self.action_done(result),
            Msg::Cert(result) => {
                self.wiz_cert_checked(result);
                Vec::new()
            }
            Msg::Added(result) => self.wiz_added(result),
        }
    }

    /// Shows a message in the bottom line until the next key press.
    fn set_status(&mut self, text: impl Into<String>) {
        self.status = text.into();
        self.status_err = false;
    }

    fn set_error(&mut self, err: impl ToString) {
        self.status = err.to_string();
        self.status_err = true;
    }

    /// Opens a text prompt.
    fn ask(&mut self, label: impl Into<String>, initial: &str, masked: bool, what: PromptFor) {
        self.overlay = Overlay::Prompt;
        self.input_label = label.into();
        self.input = LineInput::new(initial, masked);
        self.prompt_for = Some(what);
    }

    /// Asks a yes/no question. Only y means yes.
    fn confirm(&mut self, question: impl Into<String>, what: ConfirmFor) {
        self.overlay = Overlay::Confirm;
        self.question = question.into();
        self.confirm_for = Some(what);
    }

    /// Handles a key press. Overlays get the key first, then the screen.
    fn key(&mut self, key: Key) -> Vec<Cmd> {
        let name = key.name.as_str();
        if name == "ctrl+c" {
            self.quit = true;
            return Vec::new();
        }
        match self.overlay {
            Overlay::Help | Overlay::Details => {
                match name {
                    "up" | "k" => self.detail_top = self.detail_top.saturating_sub(1),
                    "down" | "j" => self.detail_top = self.detail_top.saturating_add(1),
                    "p" if self.overlay == Overlay::Details && self.wifi.is_some() => {
                        self.show_password = !self.show_password;
                        if let Some(ssid) = &self.wifi {
                            self.detail = wifi_fields(ssid, self.wifi_loaded, &self.wifi_password, self.show_password);
                        }
                        // The password is the last line, below the QR code:
                        // bring it into view when it is shown, and the code
                        // back when it is not. The position is clamped to
                        // the end when drawn.
                        self.detail_top = if self.show_password { usize::MAX / 2 } else { 0 };
                    }
                    _ => self.overlay = Overlay::None,
                }
                if self.overlay == Overlay::None {
                    // Do not keep a password around.
                    self.wifi = None;
                    self.wifi_password.clear();
                    self.wifi_code = None;
                }
                return Vec::new();
            }
            Overlay::Confirm => {
                self.overlay = Overlay::None;
                let what = self.confirm_for.take();
                return self.answered(what, matches!(name, "y" | "Y"));
            }
            Overlay::Filter => {
                match name {
                    "enter" => self.overlay = Overlay::None,
                    "esc" => {
                        self.overlay = Overlay::None;
                        self.input = LineInput::default();
                        self.apply_filter();
                    }
                    _ => {
                        if self.input.key(&key) {
                            self.apply_filter();
                        }
                    }
                }
                return Vec::new();
            }
            Overlay::Prompt => {
                match name {
                    "enter" => {
                        self.overlay = Overlay::None;
                        let what = self.prompt_for.take();
                        let text = if self.input.masked {
                            self.input.value().to_string()
                        } else {
                            self.input.value().trim().to_string()
                        };
                        self.input = LineInput::default(); // do not keep a secret around
                        return self.submitted(what, text);
                    }
                    "esc" => {
                        self.overlay = Overlay::None;
                        self.input = LineInput::default();
                        return match self.prompt_for.take() {
                            Some(PromptFor::Passphrase { .. }) => {
                                self.show_hosts();
                                Vec::new()
                            }
                            Some(PromptFor::Action { .. }) | None => {
                                self.set_status("Canceled.");
                                Vec::new()
                            }
                            Some(_) => {
                                self.wiz_cancel();
                                Vec::new()
                            }
                        };
                    }
                    _ => {
                        self.input.key(&key);
                    }
                }
                return Vec::new();
            }
            Overlay::None => {}
        }

        self.status.clear();
        match self.screen {
            Screen::Wizard => {
                if name == "esc" {
                    self.wiz_cancel(); // only reachable while the wizard is working
                }
                Vec::new()
            }
            Screen::Loading => {
                match name {
                    "q" => self.quit = true,
                    "esc" | "H" => {
                        self.connecting.clear();
                        self.show_hosts();
                    }
                    _ => {}
                }
                Vec::new()
            }
            _ => self.list_key(name),
        }
    }

    /// Acts on the answer to a text prompt.
    fn submitted(&mut self, what: Option<PromptFor>, text: String) -> Vec<Cmd> {
        match what {
            None => {}
            Some(PromptFor::WizName) => match self.app.validate_host_name(&text) {
                Err(e) => {
                    self.wiz.err = e.to_string();
                    self.wiz_name();
                }
                Ok(()) => {
                    self.wiz.host.name = text;
                    self.wiz.err.clear();
                    self.wiz_url();
                }
            },
            Some(PromptFor::WizUrl) => match app::normalize_url(&text) {
                Err(e) => {
                    self.wiz.err = e.to_string();
                    self.wiz_url();
                }
                Ok(url) => {
                    self.wiz.host.url = url.clone();
                    self.wiz.host.tls_pin.clear();
                    self.wiz.err.clear();
                    self.wiz.working = format!("Contacting {url}…");
                    let a = self.app.clone();
                    return cmd(move || Some(Msg::Cert(a.check_certificate(&url))));
                }
            },
            Some(PromptFor::WizClientId) => {
                if text.is_empty() {
                    self.wiz.err = "The client ID must not be empty.".into();
                    self.wiz_client_id();
                } else {
                    self.wiz.host.client_id = text;
                    self.wiz.err.clear();
                    self.wiz_secret();
                }
            }
            Some(PromptFor::WizSecret) => {
                let s = text.trim();
                if s.is_empty() {
                    self.wiz.err = "The client secret must not be empty.".into();
                    self.wiz_secret();
                } else {
                    self.wiz.host.secret = s.to_string();
                    self.wiz.err.clear();
                    return self.wiz_add();
                }
            }
            Some(PromptFor::WizPassphrase) => {
                if text.is_empty() {
                    self.wiz.err = "The passphrase must not be empty.".into();
                    self.wiz_passphrase();
                } else {
                    self.wiz.err.clear();
                    self.ask("Repeat the passphrase", "", true, PromptFor::WizPassphraseAgain { first: text });
                }
            }
            Some(PromptFor::WizPassphraseAgain { first }) => {
                if first != text {
                    self.wiz.err = "The two passphrases differ.".into();
                    self.wiz_passphrase();
                } else {
                    self.pass.set(&self.wiz.host.name, &first);
                    self.wiz.host.secret_store = secret::KIND_FILE.into();
                    return self.wiz_add();
                }
            }
            Some(PromptFor::Passphrase { host }) => {
                if text.is_empty() {
                    self.show_hosts();
                } else {
                    self.pass.set(&host, &text);
                    return self.connect(&host);
                }
            }
            Some(PromptFor::Action { kind, item }) => {
                if text.is_empty() {
                    self.set_status("Canceled: nothing was entered.");
                } else {
                    return self.run_action(kind, *item, text);
                }
            }
        }
        Vec::new()
    }

    /// Acts on the answer to a yes/no question.
    fn answered(&mut self, what: Option<ConfirmFor>, yes: bool) -> Vec<Cmd> {
        match (what, yes) {
            (Some(ConfirmFor::TrustCertificate { fingerprint }), true) => {
                self.wiz.host.tls_pin = fingerprint;
                self.wiz_client_id();
            }
            (Some(ConfirmFor::TrustCertificate { .. }), false) => {
                self.wiz.err = "The certificate was not trusted.".into();
                self.wiz_url();
            }
            (Some(ConfirmFor::Action { kind, item }), true) => return self.run_action(kind, *item, String::new()),
            _ => self.set_status("Canceled."),
        }
        Vec::new()
    }

    /// Makes the table follow the filter being typed.
    fn apply_filter(&mut self) {
        if self.overlay == Overlay::Prompt {
            return;
        }
        let filter = self.input.value().to_string();
        if let Some(table) = self.table_mut() {
            table.set_filter(&filter);
        }
    }

    /// Handles a key on one of the list screens.
    fn list_key(&mut self, key: &str) -> Vec<Cmd> {
        let Some(table) = self.table_mut() else { return Vec::new() };
        match key {
            "q" => self.quit = true,
            "up" | "k" => table.move_by(-1),
            "down" | "j" => table.move_by(1),
            "pgup" => table.page(-1),
            "pgdown" => table.page(1),
            "g" | "home" => table.top(),
            "G" | "end" => table.bottom(),
            "left" | "h" => table.scroll_left(),
            "right" | "l" => table.scroll_right(),
            "/" => {
                let filter = table.filter().to_string();
                self.overlay = Overlay::Filter;
                self.input = LineInput::new(&filter, false);
            }
            "o" => {
                table.cycle_sort();
                match table.sort_column() {
                    "" => self.set_status("Back to the usual order."),
                    col => self.set_status(format!("Sorted by {}.", col.to_lowercase())),
                }
            }
            "?" => {
                self.overlay = Overlay::Help;
                self.detail_top = 0;
            }
            "esc" => {
                if !table.filter().is_empty() {
                    table.set_filter("");
                } else if self.screen != Screen::Main && !self.site.id.is_empty() {
                    self.screen = Screen::Main;
                }
            }
            "H" => self.show_hosts(),
            "S" => {
                if self.sess.is_some() {
                    self.screen = Screen::Sites;
                }
            }
            "tab" | "shift+tab" if self.screen == Screen::Main => {
                let step = if key == "tab" { 1 } else { self.tabs.len() - 1 };
                self.tab = (self.tab + step) % self.tabs.len();
                return self.load_tab();
            }
            "tab" | "shift+tab" => {}
            "1" | "2" | "3" | "4" | "5" | "6" | "7" | "8" | "9" => {
                let n = (key.as_bytes()[0] - b'1') as usize;
                if self.screen == Screen::Main && n < self.tabs.len() {
                    self.tab = n;
                    return self.load_tab();
                }
            }
            "B" => {
                if self.screen == Screen::Main && self.tabs[self.tab].alt.is_some() {
                    self.tabs[self.tab].show_alt = !self.tabs[self.tab].show_alt;
                    return self.load_tab();
                }
            }
            "R" => return self.reload(),
            "enter" => return self.open(),
            "a" if self.screen == Screen::Hosts => self.start_wizard(),
            _ => return self.run_action_key(key),
        }
        Vec::new()
    }

    fn show_hosts(&mut self) {
        self.load_hosts();
        self.screen = Screen::Hosts;
        if let Some(sess) = &self.sess {
            self.hosts.table.select(sess.host());
        }
    }

    /// Fetches the current screen's rows again.
    fn reload(&mut self) -> Vec<Cmd> {
        match self.screen {
            Screen::Hosts => {
                self.load_hosts();
                Vec::new()
            }
            Screen::Sites => match self.sess.clone() {
                Some(sess) => {
                    self.all_sites = true;
                    self.connect(sess.host())
                }
                None => Vec::new(),
            },
            Screen::Main => self.load_tab(),
            _ => Vec::new(),
        }
    }

    /// Acts on enter: it opens the selected host or site, or shows the
    /// details of the selected row.
    fn open(&mut self) -> Vec<Cmd> {
        let Some(item) = self.current().and_then(|l| l.selected()).cloned() else {
            return Vec::new();
        };
        self.detail_top = 0;
        match item {
            Item::Host(h) => self.connect(&h.name),
            Item::Site(site) => self.open_site(site),
            Item::Ssid(ssid) => self.open_wifi(ssid),
            other => {
                if let Some((title, fields)) = views::details(&other, self.sess.as_deref()) {
                    self.detail_title = title;
                    self.detail = fields;
                    self.overlay = Overlay::Details;
                }
                Vec::new()
            }
        }
    }

    /// Starts the action bound to `key` on the selected row: it asks for
    /// confirmation or input first when the action wants that.
    fn run_action_key(&mut self, key: &str) -> Vec<Cmd> {
        let Some(list) = self.current() else { return Vec::new() };
        let Some(kind) = list.actions.iter().find(|a| a.key == key).map(|a| a.kind) else {
            return Vec::new();
        };
        let Some(item) = list.selected().cloned() else {
            let noun = list.noun;
            self.set_status(format!("Select a {noun} first."));
            return Vec::new();
        };
        if let Some((label, initial)) = views::prompt(kind, &item, self.sess.as_deref()) {
            self.ask(label, &initial, false, PromptFor::Action { kind, item: Box::new(item) });
        } else if let Some(question) = views::confirm_question(kind, &item) {
            self.confirm(question, ConfirmFor::Action { kind, item: Box::new(item) });
        } else {
            return self.run_action(kind, item, String::new());
        }
        Vec::new()
    }

    fn run_action(&mut self, kind: ActionKind, item: Item, input: String) -> Vec<Cmd> {
        self.set_status("Working…");
        let (a, sess, site_id) = (self.app.clone(), self.sess.clone(), self.site.id.clone());
        cmd(move || Some(Msg::Action(views::run(kind, &a, sess.as_deref(), &site_id, &item, &input))))
    }

    fn action_done(&mut self, result: Result<String>) -> Vec<Cmd> {
        match result {
            Err(e) => {
                self.set_error(e);
                return Vec::new();
            }
            Ok(done) => self.set_status(done),
        }
        if self.screen != Screen::Hosts {
            return self.reload();
        }
        let hosts = self.load_hosts();
        if self.sess.as_ref().is_some_and(|s| !hosts.iter().any(|h| h.name == s.host())) {
            // The open host was removed.
            self.sess = None;
            self.site = Site::default();
        }
        if hosts.is_empty() {
            self.start_wizard();
        }
        Vec::new()
    }

    /// Opens a host: it unlocks the secret, logs in if needed, and fetches
    /// the sites. The secret is unlocked right away so that renewing the
    /// token later never has to stop and ask for a passphrase.
    fn connect(&mut self, host: &str) -> Vec<Cmd> {
        self.screen = Screen::Loading;
        self.connecting = host.to_string();
        self.busy = format!("Connecting to {host}…");
        let (a, host) = (self.app.clone(), host.to_string());
        cmd(move || {
            let result = a.connect(&host).and_then(|sess| {
                sess.unlock()?;
                let sites = sess.sites()?;
                Ok((sess, sites))
            });
            Some(Msg::Connected { host, result })
        })
    }

    fn connected(&mut self, host: String, result: Result<(Arc<Session>, Vec<Site>)>) -> Vec<Cmd> {
        if host != self.connecting {
            return Vec::new(); // an answer nobody is waiting for any more
        }
        self.connecting.clear();
        let (sess, sites) = match result {
            Ok(ok) => ok,
            Err(e) if e.is_passphrase_required() || e.is_wrong_passphrase() => {
                if e.is_wrong_passphrase() {
                    self.pass.forget(&host);
                    self.set_error(e);
                }
                self.busy = format!("The client secret of {host} is encrypted with a passphrase.");
                self.ask(format!("Passphrase for {host}"), "", true, PromptFor::Passphrase { host });
                return Vec::new();
            }
            Err(e) => {
                self.show_hosts();
                self.hosts.table.select(&host);
                self.set_error(format!("could not open {host}: {e}"));
                return Vec::new();
            }
        };

        self.site = Site::default();
        let (rows, items) = views::site_rows(&sites);
        self.sites.table.set_rows(rows);
        self.sites.items = items;
        self.sites.loaded = true;

        let want = std::mem::take(&mut self.want_site);
        let all = std::mem::take(&mut self.all_sites);
        let chosen = sess.choose_site(&sites, &want);
        self.sess = Some(sess);
        match chosen {
            Ok(site) if !all => self.open_site(site),
            Ok(_) => {
                self.screen = Screen::Sites;
                Vec::new()
            }
            Err(e) => {
                self.screen = Screen::Sites;
                if !e.is_site_required() {
                    self.set_error(e);
                }
                Vec::new()
            }
        }
    }

    /// Shows the tabs of a site and remembers it as the last used one.
    fn open_site(&mut self, site: Site) -> Vec<Cmd> {
        self.site = site.clone();
        self.screen = Screen::Main;
        self.sites.table.select(&site.id);
        for t in &mut self.tabs {
            t.show_alt = false;
            t.list.reset();
            if let Some(alt) = &mut t.alt {
                alt.list.reset();
            }
        }
        let mut cmds = self.load_tab();
        cmds.extend(self.load_background_tabs());
        if let Some(sess) = self.sess.clone() {
            cmds.extend(cmd(move || {
                let _ = sess.remember_site(&site); // not worth interrupting anyone for
                None
            }));
        }
        cmds
    }

    /// Fetches the rows of the tab on screen in the background.
    fn load_tab(&mut self) -> Vec<Cmd> {
        self.load(self.active_tab().view, false)
    }

    /// Fetches the rows of the tabs that are kept current while another tab
    /// is on screen, such as Alerts for its count.
    fn load_background_tabs(&mut self) -> Vec<Cmd> {
        let on_screen = self.active_tab().view;
        let views: Vec<View> = self
            .tabs
            .iter()
            .filter(|t| t.background && t.view != on_screen && !t.list.loading)
            .map(|t| t.view)
            .collect();
        views.into_iter().flat_map(|v| self.load(v, false)).collect()
    }

    fn load(&mut self, view: View, enriched: bool) -> Vec<Cmd> {
        let Some(sess) = self.sess.clone() else { return Vec::new() };
        if let Some(t) = self.view_mut(view) {
            t.list.loading = true;
        }
        let site_id = self.site.id.clone();
        cmd(move || {
            let result = views::load(view, &sess, &site_id);
            Some(Msg::Rows { view, sess, site_id, result, enriched })
        })
    }

    fn loaded_rows(
        &mut self,
        view: View,
        sess: Arc<Session>,
        site_id: String,
        result: Result<views::Loaded>,
        enriched: bool,
    ) -> Vec<Cmd> {
        let same_session = self.sess.as_ref().is_some_and(|s| Arc::ptr_eq(s, &sess));
        if !same_session || site_id != self.site.id {
            return Vec::new(); // rows of a host or site that is no longer open
        }
        let Some(t) = self.view_mut(view) else { return Vec::new() };
        t.list.loading = false;
        let (rows, items) = match result {
            Ok(ok) => ok,
            Err(e) => {
                t.list.err = Some(e.to_string());
                if t.list.loaded {
                    self.set_error(e); // keep showing the rows we have
                }
                return Vec::new();
            }
        };
        t.list.err = None;
        t.list.table.set_rows(rows);
        t.list.items = items.clone();
        t.list.loaded = true;
        if view != View::Devices || enriched {
            return Vec::new();
        }
        // The rows are on screen. Now fetch what takes longer, and when
        // that brings something new, load the rows once more to show it.
        cmd(move || {
            if !views::enrich(view, &sess, &site_id, &items) {
                return None;
            }
            let result = views::load(view, &sess, &site_id);
            Some(Msg::Rows { view, sess, site_id, result, enriched: true })
        })
    }

    /// Shows the details of a Wi-Fi network and fetches its password in the
    /// background for the QR code.
    fn open_wifi(&mut self, ssid: Ssid) -> Vec<Cmd> {
        let Some(sess) = self.sess.clone() else { return Vec::new() };
        self.overlay = Overlay::Details;
        self.detail_title = ssid.name.clone();
        self.wifi_password.clear();
        self.wifi_loaded = false;
        self.wifi_code = None;
        self.show_password = false;
        self.wifi_note = "Loading the QR code…".into();
        self.detail = wifi_fields(&ssid, false, "", false);
        self.wifi = Some(ssid.clone());
        let site_id = self.site.id.clone();
        cmd(move || Some(Msg::Wifi { ssid_id: ssid.id.clone(), result: sess.wifi_password(&site_id, &ssid) }))
    }

    fn wifi_loaded(&mut self, ssid_id: &str, result: Result<String>) {
        let Some(ssid) = self.wifi.clone().filter(|w| self.overlay == Overlay::Details && w.id == ssid_id) else {
            return; // the details were closed meanwhile
        };
        self.wifi_loaded = true;
        let password = match result {
            Ok(p) => p,
            Err(e) => {
                self.wifi_note = format!("The password could not be read: {e}");
                return;
            }
        };
        self.detail = wifi_fields(&ssid, true, &password, self.show_password);
        let code =
            app::wifi_qr_payload(&ssid, &password).and_then(|payload| Code::encode(&payload).map_err(Error::Other));
        self.wifi_password = password;
        match code {
            Ok(code) => {
                self.wifi_code = Some(code);
                self.wifi_note.clear();
            }
            Err(e) => self.wifi_note = format!("{}.", upper_first(&e.to_string())),
        }
    }

    // The wizard

    fn start_wizard(&mut self) {
        self.screen = Screen::Wizard;
        self.wiz = Wizard::default();
        self.wiz_name();
    }

    /// Leaves the wizard: back to the host list, or out of the program when
    /// there is no host to go back to.
    fn wiz_cancel(&mut self) {
        self.wiz = Wizard::default();
        if self.app.hosts().is_empty() {
            self.quit = true;
        } else {
            self.show_hosts();
        }
    }

    fn wiz_name(&mut self) {
        self.wiz.hint = "Pick a short name for this controller, for example: home".into();
        self.ask("Name", &self.wiz.host.name.clone(), false, PromptFor::WizName);
    }

    fn wiz_url(&mut self) {
        self.wiz.hint = "The address you open the controller at, for example: https://192.168.0.2:8043".into();
        self.ask("Address", &self.wiz.host.url.clone(), false, PromptFor::WizUrl);
    }

    fn wiz_cert_checked(&mut self, result: Result<Certificate>) {
        if self.screen != Screen::Wizard || self.wiz.working.is_empty() {
            return;
        }
        self.wiz.working.clear();
        match result {
            Err(e) => {
                self.wiz.err = e.to_string();
                self.wiz_url();
            }
            Ok(cert) if cert.trusted => self.wiz_client_id(),
            Ok(cert) => {
                self.wiz.hint = format!(
                    "The controller's certificate is not signed by a trusted authority. That is normal for a self-signed certificate. Its fingerprint is:\n\n{}\n\nIf you trust it, gomada will accept only this certificate from now on.",
                    cert.fingerprint
                );
                self.confirm("Trust this certificate?", ConfirmFor::TrustCertificate { fingerprint: cert.fingerprint });
            }
        }
    }

    fn wiz_client_id(&mut self) {
        self.wiz.hint = HINT_OPEN_API.into();
        self.ask("Client ID", &self.wiz.host.client_id.clone(), false, PromptFor::WizClientId);
    }

    fn wiz_secret(&mut self) {
        self.wiz.hint = format!(
            "{HINT_OPEN_API}\n\nThe secret is not shown while you type, and it is never written to the config file."
        );
        self.ask("Client secret", "", true, PromptFor::WizSecret);
    }

    /// Checks the credentials against the controller and saves the host.
    fn wiz_add(&mut self) -> Vec<Cmd> {
        self.wiz.working = "Checking the credentials…".into();
        let (a, host) = (self.app.clone(), self.wiz.host.clone());
        cmd(move || Some(Msg::Added(a.add_host(&host))))
    }

    fn wiz_added(&mut self, result: Result<()>) -> Vec<Cmd> {
        if self.screen != Screen::Wizard || self.wiz.working.is_empty() {
            return Vec::new();
        }
        self.wiz.working.clear();
        let name = self.wiz.host.name.clone();
        match result {
            Ok(()) => {
                self.wiz = Wizard::default();
                self.load_hosts();
                return self.connect(&name);
            }
            Err(e) if e.is_keyring_unavailable() && self.wiz.host.secret_store.is_empty() => {
                self.wiz.err.clear();
                self.wiz_passphrase();
            }
            Err(e) => {
                self.wiz.err = e.to_string();
                if e.is_auth() {
                    self.wiz_client_id();
                } else if e.is_unreachable() || e.is_certificate() {
                    self.wiz_url();
                } else {
                    self.wiz_name();
                }
            }
        }
        Vec::new()
    }

    /// Asks, twice, for the passphrase that encrypts the secret on a system
    /// without a keychain.
    fn wiz_passphrase(&mut self) {
        self.wiz.hint = "This system has no keychain, so the client secret will be kept in a file encrypted with a passphrase. You will be asked for the passphrase when gomada starts.".into();
        self.ask("Choose a passphrase", "", true, PromptFor::WizPassphrase);
    }
}

/// The detail fields of a Wi-Fi network. The password is shown only on
/// request.
fn wifi_fields(ssid: &Ssid, loaded: bool, password: &str, show: bool) -> Vec<Field> {
    let shown = if !loaded {
        "loading…"
    } else if password.is_empty() {
        "not revealed by the controller"
    } else if show {
        password
    } else {
        "hidden; press p to show"
    };
    output::ssid_fields(ssid, shown)
}
