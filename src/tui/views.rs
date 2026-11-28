//! What the screens show: the lists, their rows, and what can be done to a
//! row. A new tab or action is added by describing it here.

use std::collections::HashMap;
use std::sync::Arc;

use super::table::{col, num, num_col, text, wide_col, Row, Table};
use super::text::upper_first;
use crate::app::{App, HostInfo, Session};
use crate::error::{Error, Result};
use crate::omada::{self, Alert, Device, KnownClient, NetClient, Site, Ssid};
use crate::output::{self, Field};

/// What a row stands for.
#[derive(Debug, Clone)]
pub enum Item {
    Host(HostInfo),
    Site(Site),
    Device(Device),
    Client(NetClient),
    Known(KnownClient),
    Ssid(Ssid),
    Alert(Alert),
}

/// Something a person can do to the selected row with one key. Whether it
/// asks for confirmation or for input first is part of its kind.
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum ActionKind {
    RemoveHost,
    Reboot,
    RenameDevice,
    Update,
    RenameClient,
    Block,
    Reconnect,
    Unblock,
    Password,
}

#[derive(Debug, Clone, Copy)]
pub struct Action {
    /// The key that triggers it, e.g. "r".
    pub key: &'static str,
    /// The word in the key hints, e.g. "reboot".
    pub label: &'static str,
    /// Describes the action in the key list. Empty means the label followed
    /// by "the selected <noun>".
    pub help: &'static str,
    pub kind: ActionKind,
}

const fn action(key: &'static str, label: &'static str, kind: ActionKind) -> Action {
    Action { key, label, help: "", kind }
}

/// One table together with the things its rows stand for.
pub struct List {
    /// Names what the rows are, for messages: "device", "client".
    pub noun: &'static str,
    pub table: Table,
    /// By row key.
    pub items: HashMap<String, Item>,
    pub loaded: bool,
    pub loading: bool,
    pub err: Option<String>,
    /// What can be done to the selected row.
    pub actions: Vec<Action>,
}

impl List {
    fn new(noun: &'static str, table: Table, actions: Vec<Action>) -> List {
        List { noun, table, items: HashMap::new(), loaded: false, loading: false, err: None, actions }
    }

    /// Returns the thing the cursor is on.
    pub fn selected(&self) -> Option<&Item> {
        self.items.get(self.table.selected_key()?)
    }

    /// Forgets everything that was loaded.
    pub fn reset(&mut self) {
        self.table.set_rows(Vec::new());
        self.table.set_filter("");
        self.items.clear();
        self.loaded = false;
        self.loading = false;
        self.err = None;
    }
}

/// Names a list that loads its rows from the open site.
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum View {
    Devices,
    Clients,
    Blocked,
    Wifi,
    Alerts,
}

/// A list that loads its rows from the open site.
pub struct Tab {
    pub view: View,
    pub title: &'static str,
    pub list: List,
    /// Keeps the tab loaded and current while it is not on screen, for the
    /// sake of its badge.
    pub background: bool,
    /// A second view of the same tab, switched with B: for example the
    /// blocked clients next to the connected ones. `hint` names a view in
    /// the key hints of the other.
    pub alt: Option<Box<Tab>>,
    pub show_alt: bool,
    pub hint: &'static str,
}

impl Tab {
    /// Returns the view of the tab that is on screen.
    pub fn active(&self) -> &Tab {
        match &self.alt {
            Some(alt) if self.show_alt => alt,
            _ => self,
        }
    }

    // A match on the option cannot hand back `self` in its other arm, so the
    // check and the access stay apart.
    #[allow(clippy::unnecessary_unwrap)]
    pub fn active_mut(&mut self) -> &mut Tab {
        if self.show_alt && self.alt.is_some() {
            self.alt.as_mut().unwrap()
        } else {
            self
        }
    }

    /// Returns the view of the tab that is not on screen.
    pub fn other(&self) -> &Tab {
        match &self.alt {
            Some(alt) if !self.show_alt => alt,
            _ => self,
        }
    }

    /// Returns a suffix for the title in the tab bar, such as a count.
    pub fn badge(&self) -> String {
        if self.view != View::Alerts {
            return String::new();
        }
        let unresolved = self.list.items.values().filter(|i| matches!(i, Item::Alert(a) if !a.resolved)).count();
        if unresolved == 0 {
            String::new()
        } else {
            format!(" ({unresolved})")
        }
    }
}

pub fn hosts_list() -> List {
    List::new(
        "host",
        Table::new(vec![col("NAME"), wide_col("ADDRESS", 40), col("LAST SITE"), col("SECRET")], 1),
        vec![action("x", "remove", ActionKind::RemoveHost)],
    )
}

pub fn host_rows(hosts: &[HostInfo]) -> (Vec<Row>, HashMap<String, Item>) {
    let rows = hosts
        .iter()
        .map(|h| Row {
            key: h.name.clone(),
            cells: vec![text(&h.name), text(&h.url), text(output::or_none(&h.site)), text(&h.secret_store)],
            dim: false,
        })
        .collect();
    let items = hosts.iter().map(|h| (h.name.clone(), Item::Host(h.clone()))).collect();
    (rows, items)
}

pub fn sites_list() -> List {
    List::new("site", Table::new(vec![col("NAME"), wide_col("ID", 40)], 1), Vec::new())
}

pub fn site_rows(sites: &[Site]) -> (Vec<Row>, HashMap<String, Item>) {
    let rows =
        sites.iter().map(|s| Row { key: s.id.clone(), cells: vec![text(&s.name), text(&s.id)], dim: false }).collect();
    let items = sites.iter().map(|s| (s.id.clone(), Item::Site(s.clone()))).collect();
    (rows, items)
}

/// The tabs of a site, in the order of the tab bar.
pub fn tabs() -> Vec<Tab> {
    let plain = |view, title, list| Tab { view, title, list, background: false, alt: None, show_alt: false, hint: "" };
    let devices = plain(
        View::Devices,
        "Devices",
        List::new(
            "device",
            Table::new(
                vec![
                    col(""),
                    col("NAME"),
                    col("TYPE"),
                    col("STATUS"),
                    col("MODEL"),
                    col("FIRMWARE"),
                    col("IP"),
                    col("UPTIME"),
                    num_col("CPU"),
                    num_col("MEM"),
                    col("MAC"),
                ],
                2,
            ),
            vec![
                action("r", "reboot", ActionKind::Reboot),
                action("n", "rename", ActionKind::RenameDevice),
                Action {
                    key: "U",
                    label: "update",
                    help: "update the firmware of the selected device",
                    kind: ActionKind::Update,
                },
            ],
        ),
    );
    // The clients that are barred from the network are not connected, so
    // they are not among the rows of the first view.
    let blocked = Tab {
        hint: "blocked",
        ..plain(
            View::Blocked,
            "Clients · blocked",
            List::new(
                "blocked client",
                Table::new(vec![col("NAME"), col("MAC"), col("TYPE"), col("LAST SEEN")], 1),
                vec![action("u", "unblock", ActionKind::Unblock)],
            ),
        )
    };
    let clients = Tab {
        hint: "connected",
        alt: Some(Box::new(blocked)),
        ..plain(
            View::Clients,
            "Clients",
            List::new(
                "client",
                Table::new(
                    vec![
                        col("NAME"),
                        col("IP"),
                        wide_col("CONNECTED TO", 36),
                        num_col("SIGNAL"),
                        num_col("DOWN"),
                        num_col("UP"),
                        num_col("UPTIME"),
                        col("MAC"),
                    ],
                    1,
                ),
                vec![
                    action("n", "rename", ActionKind::RenameClient),
                    action("b", "block", ActionKind::Block),
                    action("c", "reconnect", ActionKind::Reconnect),
                ],
            ),
        )
    };
    let wifi = plain(
        View::Wifi,
        "Wi-Fi",
        List::new(
            "Wi-Fi network",
            Table::new(vec![wide_col("NAME", 32), col("SECURITY"), col("BAND"), col("GUEST"), col("HIDDEN")], 1),
            vec![Action {
                key: "p",
                label: "password",
                help: "show the password of the selected network",
                kind: ActionKind::Password,
            }],
        ),
    );
    let alerts = Tab {
        background: true,
        ..plain(
            View::Alerts,
            "Alerts",
            List::new(
                "alert",
                Table::new(vec![col(""), col("TIME"), col("LEVEL"), wide_col("MESSAGE", 160)], 1),
                Vec::new(),
            ),
        )
    };
    vec![devices, clients, wifi, alerts]
}

// Status symbols. The shape carries the meaning, so they read without color.
const SYMBOL_ONLINE: &str = "●";
const SYMBOL_OFFLINE: &str = "○";
const SYMBOL_OTHER: &str = "◐";

/// Follows the firmware version of a device that has an update waiting.
const UPDATE_MARK: &str = "↑";

pub fn wired_or_wireless(wireless: bool) -> &'static str {
    if wireless {
        "wireless"
    } else {
        "wired"
    }
}

pub type Loaded = (Vec<Row>, HashMap<String, Item>);

/// Fetches the rows of a view. It runs off the UI thread.
pub fn load(view: View, s: &Session, site_id: &str) -> Result<Loaded> {
    let mut rows = Vec::new();
    let mut items = HashMap::new();
    match view {
        View::Devices => {
            for d in s.devices(site_id)? {
                let symbol = match d.status {
                    omada::STATUS_CONNECTED => SYMBOL_ONLINE,
                    omada::STATUS_DISCONNECTED => SYMBOL_OFFLINE,
                    _ => SYMBOL_OTHER,
                };
                let (uptime, cpu, mem) = if d.online() {
                    (
                        text(output::uptime(&d.uptime)),
                        num(output::percent(d.cpu), d.cpu),
                        num(output::percent(d.mem), d.mem),
                    )
                } else {
                    (text(output::NONE), num(output::NONE, -1.0), num(output::NONE, -1.0))
                };
                let mut firmware = output::or_none(&d.firmware);
                if s.cached_firmware(&d.mac).is_some_and(|fw| fw.update_available()) {
                    firmware = format!("{firmware} {UPDATE_MARK}");
                }
                rows.push(Row {
                    key: d.mac.clone(),
                    dim: !d.online(),
                    cells: vec![
                        text(symbol),
                        text(d.label()),
                        text(output::or_none(&d.kind)),
                        text(d.status_text()),
                        text(output::or_none(&d.model)),
                        text(firmware),
                        text(output::or_none(&d.ip)),
                        uptime,
                        cpu,
                        mem,
                        text(&d.mac),
                    ],
                });
                items.insert(d.mac.clone(), Item::Device(d));
            }
        }
        View::Clients => {
            for c in s.clients(site_id)? {
                // Wired clients sort after every wireless signal strength.
                let signal = num(output::signal(&c), if c.wireless { c.rssi } else { 1.0 });
                rows.push(Row {
                    key: c.mac.clone(),
                    dim: false,
                    cells: vec![
                        text(c.label()),
                        text(output::or_none(&c.ip)),
                        text(output::or_none(&c.connected_to())),
                        signal,
                        num(output::bytes(c.traffic_down), c.traffic_down),
                        num(output::bytes(c.traffic_up), c.traffic_up),
                        num(output::seconds(c.uptime), c.uptime),
                        text(&c.mac),
                    ],
                });
                items.insert(c.mac.clone(), Item::Client(c));
            }
        }
        View::Blocked => {
            for k in s.blocked_clients(site_id)? {
                rows.push(Row {
                    key: k.mac.clone(),
                    dim: false,
                    cells: vec![
                        text(k.label()),
                        text(&k.mac),
                        text(wired_or_wireless(k.wireless)),
                        text(output::timestamp(k.last_seen())),
                    ],
                });
                items.insert(k.mac.clone(), Item::Known(k));
            }
        }
        View::Wifi => {
            for ssid in s.ssids(site_id)? {
                let key = format!("{}/{}", ssid.wlan_id, ssid.id);
                rows.push(Row {
                    key: key.clone(),
                    dim: false,
                    cells: vec![
                        text(&ssid.name),
                        text(ssid.security_text()),
                        text(output::or_none(&ssid.band_text())),
                        text(output::yes_no(ssid.guest)),
                        text(output::yes_no(ssid.hidden())),
                    ],
                });
                items.insert(key, Item::Ssid(ssid));
            }
        }
        View::Alerts => {
            for a in s.alerts(site_id)? {
                // Unresolved alerts need attention.
                let symbol = if a.resolved { SYMBOL_OFFLINE } else { SYMBOL_ONLINE };
                // The same ID could come back from both of the lists the
                // alerts are read from, so the state is part of the key.
                let key = format!("{}/{}", a.id, a.resolved);
                rows.push(Row {
                    key: key.clone(),
                    dim: a.resolved,
                    cells: vec![
                        text(symbol),
                        num(output::timestamp(Some(a.time())), a.millis),
                        text(output::or_none(&a.level)),
                        text(&a.content),
                    ],
                });
                items.insert(key, Item::Alert(a));
            }
        }
    }
    Ok((rows, items))
}

/// Fetches data that takes longer than the rows themselves. It runs after
/// the rows are on screen and reports whether it found anything new, in
/// which case the rows are loaded once more.
pub fn enrich(view: View, s: &Session, site_id: &str, items: &HashMap<String, Item>) -> bool {
    if view != View::Devices {
        return false;
    }
    let devs: Vec<Device> = items
        .values()
        .filter_map(|i| match i {
            Item::Device(d) => Some(d.clone()),
            _ => None,
        })
        .collect();
    s.refresh_firmware(site_id, &devs)
}

/// Returns the heading and fields shown when a row is opened.
pub fn details(item: &Item, sess: Option<&Session>) -> Option<(String, Vec<Field>)> {
    let field = |label: &str, value: String| Field { label: label.into(), value };
    match item {
        Item::Device(d) => {
            let mut fields = output::device_fields(d);
            if let Some(fw) = sess.and_then(|s| s.cached_firmware(&d.mac)) {
                fields.push(field("Latest firmware", output::latest_firmware(&fw)));
                if fw.update_available() && !fw.release_notes.is_empty() {
                    fields.push(field("Release notes", fw.release_notes));
                }
            }
            Some((d.label().to_string(), fields))
        }
        Item::Client(c) => Some((c.label().to_string(), output::client_fields(c))),
        Item::Known(k) => Some((
            k.label().to_string(),
            vec![
                field("Name", output::or_none(&k.name)),
                field("MAC address", k.mac.clone()),
                field("Type", wired_or_wireless(k.wireless).into()),
                field("Last seen", output::timestamp(k.last_seen())),
                field("State", "blocked".into()),
            ],
        )),
        Item::Alert(a) => Some(("Alert".into(), output::alert_fields(a))),
        Item::Host(_) | Item::Site(_) | Item::Ssid(_) => None,
    }
}

/// Returns the question to ask before running an action, if it wants one.
pub fn confirm_question(kind: ActionKind, item: &Item) -> Option<String> {
    match (kind, item) {
        (ActionKind::RemoveHost, Item::Host(h)) => Some(format!("Remove host {} and its stored secret?", h.name)),
        (ActionKind::Reboot, Item::Device(d)) => Some(format!("Reboot {} ({})?", d.label(), d.kind)),
        (ActionKind::Block, Item::Client(c)) => Some(format!("Block {} ({})?", c.label(), c.mac)),
        (ActionKind::Unblock, Item::Known(k)) => Some(format!("Unblock {} ({})?", k.label(), k.mac)),
        _ => None,
    }
}

/// Returns the label and starting text of a text field to show before
/// running an action, if it takes input.
pub fn prompt(kind: ActionKind, item: &Item, sess: Option<&Session>) -> Option<(String, String)> {
    match (kind, item) {
        (ActionKind::RenameDevice, Item::Device(d)) => Some((format!("New name for {}", d.label()), d.name.clone())),
        (ActionKind::RenameClient, Item::Client(c)) => Some((format!("New name for {}", c.label()), c.name.clone())),
        // An update can take a device off the network, so it asks for more
        // than a y: the device name, typed out.
        (ActionKind::Update, Item::Device(d)) => {
            let mut what = format!("Update {}", d.label());
            if let Some(fw) = sess.and_then(|s| s.cached_firmware(&d.mac)).filter(|fw| fw.update_available()) {
                what.push_str(&format!(" from {} to {}", fw.current, fw.latest));
            }
            Some((format!("{what}? It restarts. Type its name to confirm"), String::new()))
        }
        _ => None,
    }
}

/// Does the work of an action, off the UI thread, and returns the message
/// to show when it is done.
pub fn run(
    kind: ActionKind,
    app: &Arc<App>,
    sess: Option<&Session>,
    site_id: &str,
    item: &Item,
    input: &str,
) -> Result<String> {
    let no_session = || Error::other("no host is open");
    match (kind, item) {
        (ActionKind::RemoveHost, Item::Host(h)) => {
            app.remove_host(&h.name)?;
            Ok(format!("Removed host {}.", h.name))
        }
        (ActionKind::Reboot, Item::Device(d)) => {
            sess.ok_or_else(no_session)?.reboot_device(site_id, d)?;
            Ok(format!("Rebooting {}.", d.label()))
        }
        (ActionKind::RenameDevice, Item::Device(d)) => {
            sess.ok_or_else(no_session)?.rename_device(site_id, d, input)?;
            Ok(format!("Renamed {} to {input}.", d.label()))
        }
        (ActionKind::Update, Item::Device(d)) => {
            if input != d.label() {
                return Err(Error::other(format!("the name did not match {:?}; nothing was updated", d.label())));
            }
            match sess.ok_or_else(no_session)?.upgrade_device(site_id, d) {
                Ok(fw) => Ok(format!(
                    "Updating {} to {}. It restarts when it is done; do not unplug it.",
                    d.label(),
                    fw.latest
                )),
                Err(e) if e.is_up_to_date() => Ok(format!("{}.", upper_first(&e.to_string()))),
                Err(e) => Err(e),
            }
        }
        (ActionKind::RenameClient, Item::Client(c)) => {
            sess.ok_or_else(no_session)?.rename_client(site_id, c, input)?;
            Ok(format!("Renamed {} to {input}.", c.label()))
        }
        (ActionKind::Block, Item::Client(c)) => {
            sess.ok_or_else(no_session)?.block_client(site_id, c)?;
            Ok(format!("Blocked {}. Press B to see the blocked clients.", c.label()))
        }
        (ActionKind::Reconnect, Item::Client(c)) => {
            sess.ok_or_else(no_session)?.reconnect_client(site_id, c)?;
            Ok(format!("Asked {} to reconnect.", c.label()))
        }
        (ActionKind::Unblock, Item::Known(k)) => {
            sess.ok_or_else(no_session)?.unblock_client(site_id, &k.mac)?;
            Ok(format!("Unblocked {}.", k.label()))
        }
        (ActionKind::Password, Item::Ssid(ssid)) => {
            if ssid.security != omada::SECURITY_PERSONAL {
                return Ok(format!("{} has no shared password ({}).", ssid.name, ssid.security_text()));
            }
            let password = sess.ok_or_else(no_session)?.wifi_password(site_id, ssid)?;
            if password.is_empty() {
                Ok(format!("The controller did not reveal the password of {}.", ssid.name))
            } else {
                Ok(format!("Password of {}: {password}", ssid.name))
            }
        }
        _ => Err(Error::other("this cannot be done to the selected row")),
    }
}
