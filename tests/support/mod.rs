//! Shared by the integration tests: the fake controller and a harness that
//! runs the command line in-process.
#![allow(dead_code)]

pub mod fake;

use std::collections::VecDeque;
use std::io::Write;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use chrono::{DateTime, TimeZone, Utc};
use gomada::cli::{self, Io};
use gomada::secret::MemoryKeychain;
use gomada::tui;

/// The moment the sample data of the fake controller is dated around.
pub fn epoch() -> DateTime<Utc> {
    Utc.timestamp_millis_opt(fake::EPOCH_MS).unwrap()
}

/// A writer that can be read back after the command has run.
#[derive(Clone, Default)]
pub struct Captured(Arc<Mutex<Vec<u8>>>);

impl Write for Captured {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        self.0.lock().unwrap().extend_from_slice(buf);
        Ok(buf.len())
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

impl Captured {
    pub fn text(&self) -> String {
        String::from_utf8_lossy(&self.0.lock().unwrap()).into_owned()
    }
}

/// A test fixture: a fake controller, a config in a temp directory, and an
/// in-memory keychain.
pub struct Env {
    pub fake: fake::Controller,
    pub dir: tempfile::TempDir,
    pub path: PathBuf,
    pub keychain: Arc<MemoryKeychain>,

    // What the next command sees.
    pub stdin: String,
    pub interactive: bool,
    /// Answers to hidden prompts, in order.
    pub passwords: Arc<Mutex<VecDeque<String>>>,
    /// Set when the terminal UI was started.
    pub tui_opts: Arc<Mutex<Option<tui::Options>>>,
}

impl Env {
    pub fn new() -> Env {
        gomada::output::show_times_in_utc();
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("config.yaml");
        Env {
            fake: fake::Controller::start(),
            dir,
            path,
            keychain: Arc::new(MemoryKeychain::default()),
            stdin: String::new(),
            interactive: false,
            passwords: Arc::default(),
            tui_opts: Arc::default(),
        }
    }

    /// Returns a fixture with the host "home" already added.
    pub fn with_host() -> Env {
        let mut e = Env::new();
        e.add_host("home");
        e
    }

    pub fn add_host(&mut self, name: &str) {
        self.stdin = format!("{}\n", fake::SECRET);
        let url = self.fake.url.clone();
        self.must_run(&["host", "add", name, "--url", &url, "--client-id", fake::CLIENT_ID, "--secret-stdin"]);
    }

    /// Executes one command and returns what it printed and its exit code.
    pub fn run(&mut self, args: &[&str]) -> (String, String, i32) {
        let (out, err) = (Captured::default(), Captured::default());
        let (passwords, tui_opts) = (self.passwords.clone(), self.tui_opts.clone());
        let mut io = Io {
            stdin: Box::new(std::io::Cursor::new(std::mem::take(&mut self.stdin))),
            out: Box::new(out.clone()),
            err: Box::new(err.clone()),
            interactive: self.interactive,
            read_password: Arc::new(move || {
                passwords.lock().unwrap().pop_front().ok_or_else(|| std::io::Error::other("unexpected hidden prompt"))
            }),
            now: Some(Arc::new(epoch)),
            poll_every: Some(Duration::from_millis(1)),
            keychain: Some(self.keychain.clone()),
            run_tui: Box::new(move |_, opts| {
                *tui_opts.lock().unwrap() = Some(opts);
                Ok(())
            }),
        };
        let mut argv = vec!["--config".to_string(), self.path.display().to_string()];
        argv.extend(args.iter().map(|a| a.to_string()));
        let code = cli::run(&mut io, &argv);
        (out.text(), err.text(), code)
    }

    /// Executes a command that has to succeed and returns its output.
    pub fn must_run(&mut self, args: &[&str]) -> String {
        let (out, err, code) = self.run(args);
        assert_eq!(code, 0, "gomada {}\nstdout: {out}\nstderr: {err}", args.join(" "));
        out
    }

    /// Executes a command that has to fail with the given exit code and
    /// returns what it printed to standard error.
    pub fn want_fail(&mut self, want: i32, args: &[&str]) -> String {
        let (out, err, code) = self.run(args);
        assert_eq!(code, want, "gomada {}\nstdout: {out}\nstderr: {err}", args.join(" "));
        err
    }

    /// Compares `got` with a file in tests/testdata/cli. The fake
    /// controller's address changes on every run, so it is replaced by a
    /// fixed one of the same length, which keeps table columns aligned.
    pub fn golden(&self, name: &str, got: &str) {
        const FIXED: &str = "http://controller:8043";
        if got.contains(&self.fake.url) && self.fake.url.len() != FIXED.len() {
            return; // an address of unusual length would misalign the table
        }
        golden(&format!("cli/{name}"), &got.replace(&self.fake.url, FIXED));
    }
}

impl Env {
    /// Runs a command that has to succeed and compares its output with a
    /// golden file.
    pub fn golden_run(&mut self, name: &str, args: &[&str]) {
        let out = self.must_run(args);
        self.golden(name, &out);
    }
}

/// Compares `got` with tests/testdata/<name>.golden. Run the tests with
/// UPDATE_GOLDEN=1 to rewrite the files after an intended change.
pub fn golden(name: &str, got: &str) {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/testdata").join(format!("{name}.golden"));
    if std::env::var_os("UPDATE_GOLDEN").is_some() {
        std::fs::write(&path, got).unwrap();
        return;
    }
    let want = std::fs::read_to_string(&path)
        .unwrap_or_else(|e| panic!("{}: {e} (run the tests with UPDATE_GOLDEN=1 to create it)", path.display()));
    assert_eq!(got, want, "output differs from {}", path.display());
}
