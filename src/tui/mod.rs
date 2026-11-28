//! The interactive terminal front end, drawn with ratatui.
//!
//! It is deliberately plain: one list on screen at a time, one line of key
//! hints, no panels side by side, and no mouse. Every screen is a table
//! from the table module, which scrolls sideways when the terminal is
//! narrow.
//!
//! The model never blocks: every request to a controller runs as a command
//! on its own thread and comes back as a message.

mod model;
mod render;
mod table;
mod text;
mod views;

#[cfg(test)]
#[path = "../../tests/support/fake.rs"]
mod fake;
#[cfg(test)]
mod tests;

use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::time::Duration;

use ratatui::crossterm::event::{self, Event, KeyCode, KeyEvent, KeyEventKind, KeyModifiers};
use ratatui::crossterm::{event::DisableBracketedPaste, event::EnableBracketedPaste, execute};

use crate::app::{App, Certificate, Session};
use crate::error::{Error, Result};
use crate::omada::Site;
use crate::secret;

pub(crate) use model::Model;

/// Configures the terminal UI.
#[derive(Debug, Clone, Default)]
pub struct Options {
    /// The host to open instead of asking.
    pub host: String,
    /// The site to open instead of the last used one.
    pub site: String,
    /// Turns off color.
    pub no_color: bool,
}

/// How often the open tab reloads by itself.
const REFRESH_EVERY: Duration = Duration::from_secs(10);

/// A key press, named the way the key hints name it: "enter", "esc", "tab",
/// "shift+tab", "up", "ctrl+c", or the character itself.
#[derive(Debug, Clone, PartialEq)]
pub(crate) struct Key {
    pub name: String,
    /// The character the key types into a text field, if any.
    pub text: Option<char>,
}

impl Key {
    fn from_event(ev: KeyEvent) -> Option<Key> {
        if ev.kind == KeyEventKind::Release {
            return None;
        }
        let special = |name: &str| Some(Key { name: name.into(), text: None });
        match ev.code {
            KeyCode::Char(c) if ev.modifiers.contains(KeyModifiers::CONTROL) => {
                special(&format!("ctrl+{}", c.to_ascii_lowercase()))
            }
            KeyCode::Char(_) if ev.modifiers.contains(KeyModifiers::ALT) => None,
            KeyCode::Char(c) => Some(Key { name: c.to_string(), text: Some(c) }),
            KeyCode::Enter => special("enter"),
            KeyCode::Esc => special("esc"),
            KeyCode::Tab if ev.modifiers.contains(KeyModifiers::SHIFT) => special("shift+tab"),
            KeyCode::Tab => special("tab"),
            KeyCode::BackTab => special("shift+tab"),
            KeyCode::Backspace => special("backspace"),
            KeyCode::Up => special("up"),
            KeyCode::Down => special("down"),
            KeyCode::Left => special("left"),
            KeyCode::Right => special("right"),
            KeyCode::PageUp => special("pgup"),
            KeyCode::PageDown => special("pgdown"),
            KeyCode::Home => special("home"),
            KeyCode::End => special("end"),
            _ => None,
        }
    }
}

/// What reaches the model: input from the terminal and the results of
/// background work.
pub(crate) enum Msg {
    Key(Key),
    Paste(String),
    /// Asks for the periodic reload.
    Tick,
    /// Ends an attempt to open a host.
    Connected {
        host: String,
        result: Result<(Arc<Session>, Vec<Site>)>,
    },
    /// Carries the freshly loaded rows of a view.
    Rows {
        view: views::View,
        sess: Arc<Session>,
        site_id: String,
        result: Result<views::Loaded>,
        /// Marks rows reloaded after the view's slow extra data arrived, so
        /// that the data is not fetched in a loop.
        enriched: bool,
    },
    /// Carries the password of the Wi-Fi network whose details are open,
    /// for its QR code.
    Wifi {
        ssid_id: String,
        result: Result<String>,
    },
    /// Ends an action on a row.
    Action(Result<String>),
    /// Brings back the wizard's certificate check.
    Cert(Result<Certificate>),
    /// Brings back the wizard's attempt to add the host.
    Added(Result<()>),
}

/// Background work: it runs on its own thread and may send one message
/// back.
pub(crate) type Cmd = Box<dyn FnOnce() -> Option<Msg> + Send>;

/// Remembers the passphrases typed during this run, so each is asked for
/// once. They are kept in memory only.
#[derive(Default)]
pub(crate) struct Passphrases {
    by_host: Mutex<HashMap<String, String>>,
}

impl Passphrases {
    fn set(&self, host: &str, pass: &str) {
        self.by_host.lock().unwrap().insert(host.into(), pass.into());
    }

    fn forget(&self, host: &str) {
        self.by_host.lock().unwrap().remove(host);
    }

    /// The passphrase source handed to the application core. When it has
    /// no passphrase it says so, and the UI asks for one.
    fn lookup(&self, host: &str) -> Result<String> {
        if let Some(env) = std::env::var(secret::ENV_PASSPHRASE).ok().filter(|v| !v.is_empty()) {
            return Ok(env);
        }
        self.by_host.lock().unwrap().get(host).cloned().ok_or(Error::PassphraseRequired)
    }
}

/// Starts the terminal UI and returns when the person quits.
pub fn run(app: Arc<App>, opts: Options) -> Result<()> {
    let mut model = Model::new(app, &opts, true);
    let (tx, rx) = mpsc::channel::<Msg>();
    let stop = Arc::new(AtomicBool::new(false));

    // ratatui::init switches to the alternate screen and raw mode, and
    // makes sure a panic still puts the terminal back.
    let mut terminal = ratatui::init();
    let _ = execute!(std::io::stdout(), EnableBracketedPaste);

    // Keys and pastes come from one thread, the reload tick from another.
    let (input_tx, input_stop) = (tx.clone(), stop.clone());
    let input = std::thread::spawn(move || {
        while !input_stop.load(Ordering::SeqCst) {
            if !event::poll(Duration::from_millis(100)).unwrap_or(false) {
                continue;
            }
            let msg = match event::read() {
                Ok(Event::Key(ev)) => Key::from_event(ev).map(Msg::Key),
                Ok(Event::Paste(s)) => Some(Msg::Paste(s)),
                Ok(_) => continue, // a resize: the next draw picks it up
                Err(_) => break,
            };
            let Some(msg) = msg else { continue };
            if input_tx.send(msg).is_err() {
                break;
            }
        }
    });
    let (tick_tx, tick_stop) = (tx.clone(), stop.clone());
    std::thread::spawn(move || {
        while !tick_stop.load(Ordering::SeqCst) {
            std::thread::sleep(REFRESH_EVERY);
            if tick_tx.send(Msg::Tick).is_err() {
                break;
            }
        }
    });

    let spawn = |cmds: Vec<Cmd>| {
        for cmd in cmds {
            let tx = tx.clone();
            std::thread::spawn(move || {
                if let Some(msg) = cmd() {
                    let _ = tx.send(msg);
                }
            });
        }
    };

    spawn(model.init());
    let mut outcome = Ok(());
    while !model.quit {
        if let Err(e) = terminal.draw(|frame| model.draw(frame)) {
            outcome = Err(Error::other(format!("draw: {e}")));
            break;
        }
        // Wait for one message, then take whatever else has piled up, so a
        // burst of results costs one redraw. The timeout lets a resize show
        // without a key press.
        let first = match rx.recv_timeout(Duration::from_millis(250)) {
            Ok(msg) => msg,
            Err(mpsc::RecvTimeoutError::Timeout) => continue,
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        };
        for msg in std::iter::once(first).chain(rx.try_iter()) {
            spawn(model.update(msg));
        }
    }

    stop.store(true, Ordering::SeqCst);
    let _ = input.join();
    let _ = execute!(std::io::stdout(), DisableBracketedPaste);
    ratatui::restore();
    outcome
}
