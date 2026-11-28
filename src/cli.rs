//! The command line front end. Every command is a thin layer over the app
//! module: it parses flags, calls a use case, and prints the result as a
//! table or as JSON.

use std::io::{BufRead, Write};
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use clap::{Args, CommandFactory, Parser, Subcommand};
use serde::Serialize;

use crate::app::{self, App, NewHost, Session};
use crate::error::{Error, Result};
use crate::omada::{self, Alert, Clock, Device, Firmware, KnownClient, NetClient, Site, Ssid};
use crate::output::{self, ser_num, Format};
use crate::qr;
use crate::secret::{self, Keychain};
use crate::tui;
use crate::version;

// Exit codes.
const EXIT_OK: i32 = 0;
const EXIT_FAILURE: i32 = 1;
const EXIT_USAGE: i32 = 2;
const EXIT_AUTH: i32 = 3;
const EXIT_UNREACHABLE: i32 = 4;

/// Bounds how long `device upgrade` waits for the device.
const UPGRADE_TIMEOUT: Duration = Duration::from_secs(20 * 60);

/// The command line's connection to the outside world. Tests replace it;
/// [`execute`] uses the real terminal.
pub struct Io {
    pub stdin: Box<dyn BufRead>,
    pub out: Box<dyn Write>,
    pub err: Box<dyn Write>,
    /// Whether a person is at the keyboard, so prompts make sense.
    pub interactive: bool,
    /// Reads a line without echoing it.
    pub read_password: Arc<dyn Fn() -> std::io::Result<String> + Send + Sync>,
    /// `None` means the system clock.
    pub now: Option<Clock>,
    /// How often a command that waits for the controller asks again.
    /// `None` means every five seconds.
    pub poll_every: Option<Duration>,
    /// `None` means the operating system keychain.
    pub keychain: Option<Arc<dyn Keychain>>,
    /// Starts the terminal UI.
    pub run_tui: Box<dyn FnMut(Arc<App>, tui::Options) -> Result<()>>,
}

/// Runs the command line with the process's arguments and terminal and
/// returns the exit code.
pub fn execute() -> i32 {
    use std::io::IsTerminal;
    let mut io = Io {
        stdin: Box::new(std::io::stdin().lock()),
        out: Box::new(std::io::stdout()),
        err: Box::new(std::io::stderr()),
        interactive: std::io::stdin().is_terminal() && std::io::stdout().is_terminal(),
        read_password: Arc::new(rpassword::read_password),
        now: None,
        poll_every: None,
        keychain: None,
        run_tui: Box::new(tui::run),
    };
    let args: Vec<String> = std::env::args().skip(1).collect();
    run(&mut io, &args)
}

fn version_text() -> &'static str {
    static TEXT: std::sync::OnceLock<String> = std::sync::OnceLock::new();
    TEXT.get_or_init(version::details)
}

fn parse_timeout(s: &str) -> std::result::Result<Duration, String> {
    humantime::parse_duration(s).map_err(|e| format!("{e}; write it like 15s or 1m30s"))
}

/// Manage TP-Link Omada controllers from the terminal
#[derive(Parser)]
#[command(
    name = "gomada",
    version = version_text(),
    long_about = "gomada manages TP-Link Omada controllers through the Omada Open API.\n\n\
Run it without a command to open the interactive view. Every action is also\n\
available as a command for use in scripts.",
    disable_help_subcommand = true
)]
struct Cli {
    #[command(flatten)]
    global: Global,
    #[command(subcommand)]
    cmd: Option<Cmd>,
}

#[derive(Args)]
struct Global {
    /// Host to use instead of the current one
    #[arg(long, global = true, value_name = "NAME")]
    host: Option<String>,
    /// Site name or ID to use instead of the last used one
    #[arg(long, global = true, value_name = "SITE")]
    site: Option<String>,
    /// Output format: table or json
    #[arg(short = 'o', long = "output", global = true, default_value = "table", value_name = "FORMAT")]
    format: String,
    /// Time limit for each request to the controller
    #[arg(long, global = true, default_value = "15s", value_parser = parse_timeout, value_name = "DURATION")]
    timeout: Duration,
    /// Do not ask for confirmation
    #[arg(short = 'y', long, global = true)]
    yes: bool,
    /// Log each request to the controller to standard error
    #[arg(short = 'v', long, global = true)]
    verbose: bool,
    /// Turn off color in the interactive view
    #[arg(long, global = true)]
    no_color: bool,
    /// Log in on every run and store no access token
    #[arg(long, global = true)]
    no_token_cache: bool,
    /// Config file to use (default: $GOMADA_CONFIG or the standard location)
    #[arg(long, global = true, value_name = "FILE")]
    config: Option<PathBuf>,
}

#[derive(Subcommand)]
enum Cmd {
    /// Manage saved controllers
    Host {
        #[command(subcommand)]
        cmd: Option<HostCmd>,
    },
    /// List and choose sites
    Site {
        #[command(subcommand)]
        cmd: Option<SiteCmd>,
    },
    /// List and manage gateways, switches, and access points
    Device {
        #[command(subcommand)]
        cmd: Option<DeviceCmd>,
    },
    /// List and manage connected clients
    Client {
        #[command(subcommand)]
        cmd: Option<ClientCmd>,
    },
    /// List Wi-Fi networks, show passwords and QR codes
    Wifi {
        #[command(subcommand)]
        cmd: Option<WifiCmd>,
    },
    /// Read the alerts the controller raised
    Alert {
        #[command(subcommand)]
        cmd: Option<AlertCmd>,
    },
    /// Print the version
    Version,
    /// Print a completion script for a shell
    Completion {
        /// bash, zsh, fish, powershell, or elvish
        shell: clap_complete::Shell,
    },
}

#[derive(Args)]
struct HostAdd {
    /// Name for this host, e.g. home
    name: Option<String>,
    /// Controller address, e.g. https://192.168.0.2:8043
    #[arg(long)]
    url: Option<String>,
    /// Client ID of the Open API application
    #[arg(long)]
    client_id: Option<String>,
    /// Controller ID (omadacId); discovered from the controller when omitted
    #[arg(long)]
    controller_id: Option<String>,
    /// Read the client secret from standard input
    #[arg(long)]
    secret_stdin: bool,
    /// Where to keep the secret: keyring, file, or env (default: keyring, falling back to file)
    #[arg(long, value_name = "STORE")]
    secret_store: Option<String>,
    /// Fingerprint of the controller's self-signed certificate (sha256:...)
    #[arg(long, value_name = "FINGERPRINT")]
    tls_pin: Option<String>,
    /// DANGEROUS: accept any certificate, which lets an attacker read the client secret
    #[arg(long)]
    insecure_skip_tls_verify: bool,
}

#[derive(Subcommand)]
enum HostCmd {
    /// Add a controller
    #[command(long_about = "Add a controller and check that its credentials work.\n\n\
Create an Open API application in the controller first (Settings > Platform\n\
Integration > Open API, client credentials mode). It gives you the client ID\n\
and client secret.\n\n\
In a terminal, anything not given as a flag is asked for. The client secret is\n\
never a flag: it is typed without echo, read from standard input with\n\
--secret-stdin, or taken from the GOMADA_CLIENT_SECRET environment variable.")]
    Add(HostAdd),
    /// List saved controllers
    #[command(visible_alias = "ls")]
    List,
    /// Make a controller the current one
    Use { name: String },
    /// Remove a controller and its stored secret
    #[command(visible_alias = "rm")]
    Remove { name: String },
    /// Check that a controller is reachable and the credentials work
    Test { name: Option<String> },
}

#[derive(Subcommand)]
enum SiteCmd {
    /// List the sites of the host
    #[command(visible_alias = "ls")]
    List,
    /// Choose the site that commands work on
    Use { site: String },
}

#[derive(Subcommand)]
enum DeviceCmd {
    /// List the devices of the site
    #[command(visible_alias = "ls")]
    List {
        /// Show only this type: gateway, switch, or ap
        #[arg(long = "type", value_name = "TYPE")]
        kind: Option<String>,
        /// Show only devices with this text in any column
        #[arg(long, value_name = "TEXT")]
        filter: Option<String>,
        /// Show only connected devices with a firmware update waiting
        #[arg(long)]
        updates: bool,
    },
    /// Show the details of one device, named by MAC address or by name
    Show { device: String },
    /// Restart a device
    #[command(long_about = "Restart a device, named by MAC address or by name.\n\n\
Everything connected through the device loses its connection while it\n\
restarts, so this asks for confirmation. Pass --yes to skip the question; that\n\
is required when not running in a terminal.")]
    Reboot { device: String },
    /// Change the name of a device
    Rename { device: String, new_name: String },
    /// Show the installed and the newest firmware of a device
    #[command(long_about = "Show the installed firmware of a device and the newest one the controller\n\
knows about, with its release notes.\n\n\
The controller looks for new firmware on its own schedule; this shows what\n\
it found the last time it looked.")]
    Firmware { device: String },
    /// Install the newest firmware on a device
    #[command(long_about = "Install the newest firmware on a device. The controller downloads it from\n\
TP-Link.\n\n\
The device restarts, and everything connected through it loses its connection\n\
for a few minutes. Do not unplug the device meanwhile. This asks for\n\
confirmation; pass --yes to skip the question, which is required when not\n\
running in a terminal.\n\n\
The command waits until the device is done and reports whether the update\n\
worked. With --no-wait it returns as soon as the update has started.")]
    Upgrade {
        device: String,
        /// Return once the update has started
        #[arg(long)]
        no_wait: bool,
    },
}

#[derive(Subcommand)]
enum ClientCmd {
    /// List the clients connected to the site
    #[command(visible_alias = "ls")]
    List {
        /// Show only wired clients
        #[arg(long)]
        wired: bool,
        /// Show only wireless clients
        #[arg(long)]
        wireless: bool,
        /// List the clients that are blocked, which are not connected
        #[arg(long)]
        blocked: bool,
        /// Show only clients with this text in any column, such as a network or access point name
        #[arg(long, value_name = "TEXT")]
        filter: Option<String>,
    },
    /// Show the details of one connected client, named by MAC address, name, or host name
    Show { client: String },
    /// Change the name the controller shows for a client
    Rename { client: String, new_name: String },
    /// Bar a client from the network
    #[command(long_about = "Bar a connected client from the network.\n\n\
This asks for confirmation. Pass --yes to skip the question; that is required\n\
when not running in a terminal. Note the MAC address it prints: a blocked\n\
client is offline, so 'gomada client unblock' needs the MAC address.")]
    Block { client: String },
    /// Let a blocked client back on the network. A blocked client is offline, so it is named by MAC address
    Unblock { mac: String },
    /// Make a wireless client reconnect
    Reconnect { client: String },
}

#[derive(Subcommand)]
enum WifiCmd {
    /// List the Wi-Fi networks of the site
    #[command(visible_alias = "ls")]
    List,
    /// Show the details of one Wi-Fi network; the password only with --password
    Show {
        network: String,
        /// Also print the Wi-Fi password
        #[arg(long)]
        password: bool,
    },
    /// Print a QR code that phones can scan to join a Wi-Fi network
    #[command(long_about = "Print a QR code that phones can scan to join a Wi-Fi network.\n\n\
The code contains the Wi-Fi password, so anyone who can see it can join the\n\
network. Networks with a login per person (WPA-Enterprise, PPSK) have no\n\
shared password and so no QR code.")]
    Qr { network: String },
}

#[derive(Subcommand)]
enum AlertCmd {
    /// List the alerts of the last 30 days, newest first
    #[command(
        visible_alias = "ls",
        long_about = "List the alerts of the site from the last 30 days, newest first.\n\n\
At most the 100 newest unresolved and the 100 newest resolved alerts are\n\
listed; the controller keeps the full history."
    )]
    List {
        /// Show only alerts that are not resolved
        #[arg(long)]
        unresolved: bool,
    },
    /// Show one alert in full. Take the ID from 'gomada alert list'
    Show { id: String },
}

/// Runs the command line with the given arguments and returns the exit
/// code. Errors are printed to `io.err`.
pub fn run(io: &mut Io, args: &[String]) -> i32 {
    let argv = std::iter::once("gomada".to_string()).chain(args.iter().cloned());
    let cli = match Cli::try_parse_from(argv) {
        Ok(cli) => cli,
        Err(e) => {
            use clap::error::ErrorKind::{DisplayHelp, DisplayVersion};
            if matches!(e.kind(), DisplayHelp | DisplayVersion) {
                let _ = write!(io.out, "{}", e.render());
                return EXIT_OK;
            }
            let _ = write!(io.err, "{}", e.render());
            return EXIT_USAGE;
        }
    };
    let mut ctx = Ctx { io, g: cli.global };
    match ctx.dispatch(cli.cmd) {
        Ok(()) => EXIT_OK,
        Err(e) => {
            let _ = writeln!(ctx.io.err, "gomada: {e}");
            if let Some(hint) = hint_for(&e) {
                let _ = writeln!(ctx.io.err, "{hint}");
            }
            exit_code(&e)
        }
    }
}

fn exit_code(e: &Error) -> i32 {
    if e.is_usage() {
        EXIT_USAGE
    } else if e.is_auth() {
        EXIT_AUTH
    } else if e.is_unreachable() || e.is_certificate() {
        EXIT_UNREACHABLE
    } else {
        EXIT_FAILURE
    }
}

/// Returns a line telling the person what to do about an error.
fn hint_for(e: &Error) -> Option<String> {
    if e.is_usage() {
        Some("Run the command with --help to see how it is used.".into())
    } else if e.is_auth() {
        Some("Check the client ID and client secret of the Open API application in the controller (Settings > Platform Integration > Open API).".into())
    } else if e.is_pin_mismatch() {
        Some("If you replaced the controller's certificate yourself, remove the host and add it again. If not, someone may be intercepting the connection.".into())
    } else if e.is_unreachable() {
        Some("Check the controller address and that this machine can reach it.".into())
    } else if e.is_passphrase_required() {
        Some(format!("Run the command in a terminal to be asked for it, or set {}.", secret::ENV_PASSPHRASE))
    } else {
        None
    }
}

fn env(name: &str) -> Option<String> {
    std::env::var(name).ok().filter(|v| !v.is_empty())
}

fn is_yes(answer: &str) -> bool {
    matches!(answer.to_lowercase().as_str(), "y" | "yes")
}

/// The state shared by all commands: the streams and the global flags.
struct Ctx<'a> {
    io: &'a mut Io,
    g: Global,
}

/// Prints to standard output.
macro_rules! out {
    ($ctx:expr, $($arg:tt)*) => { writeln!($ctx.io.out, $($arg)*).map_err(Error::from) };
}

/// Prints a message to standard error.
macro_rules! note {
    ($ctx:expr, $($arg:tt)*) => { writeln!($ctx.io.err, $($arg)*).map_err(Error::from) };
}

impl Ctx<'_> {
    fn dispatch(&mut self, cmd: Option<Cmd>) -> Result<()> {
        match cmd {
            None => self.run_tui(),
            Some(Cmd::Version) => out!(self, "{}", version::string()),
            Some(Cmd::Completion { shell }) => {
                clap_complete::generate(shell, &mut Cli::command(), "gomada", &mut self.io.out);
                Ok(())
            }
            Some(Cmd::Host { cmd: None }) => self.group_help("host"),
            Some(Cmd::Site { cmd: None }) => self.group_help("site"),
            Some(Cmd::Device { cmd: None }) => self.group_help("device"),
            Some(Cmd::Client { cmd: None }) => self.group_help("client"),
            Some(Cmd::Wifi { cmd: None }) => self.group_help("wifi"),
            Some(Cmd::Alert { cmd: None }) => self.group_help("alert"),
            Some(Cmd::Host { cmd: Some(c) }) => match c {
                HostCmd::Add(add) => self.host_add(add),
                HostCmd::List => self.host_list(),
                HostCmd::Use { name } => {
                    self.app()?.use_host(&name)?;
                    out!(self, "{name:?} is now the current host.")
                }
                HostCmd::Remove { name } => self.host_remove(&name),
                HostCmd::Test { name } => self.host_test(name),
            },
            Some(Cmd::Site { cmd: Some(c) }) => match c {
                SiteCmd::List => self.site_list(),
                SiteCmd::Use { site } => {
                    let s = self.connect()?;
                    let site = s.resolve_site(&site)?;
                    s.remember_site(&site)?;
                    out!(self, "Commands on host {:?} now use site {:?}.", s.host(), site.name)
                }
            },
            Some(Cmd::Device { cmd: Some(c) }) => match c {
                DeviceCmd::List { kind, filter, updates } => {
                    self.device_list(kind.unwrap_or_default(), filter.unwrap_or_default(), updates)
                }
                DeviceCmd::Show { device } => self.device_show(&device),
                DeviceCmd::Reboot { device } => self.device_reboot(&device),
                DeviceCmd::Rename { device, new_name } => self.device_rename(&device, &new_name),
                DeviceCmd::Firmware { device } => self.device_firmware(&device),
                DeviceCmd::Upgrade { device, no_wait } => self.device_upgrade(&device, no_wait),
            },
            Some(Cmd::Client { cmd: Some(c) }) => match c {
                ClientCmd::List { wired, wireless, blocked, filter } => {
                    self.client_list(wired, wireless, blocked, filter.unwrap_or_default())
                }
                ClientCmd::Show { client } => self.client_show(&client),
                ClientCmd::Rename { client, new_name } => self.client_rename(&client, &new_name),
                ClientCmd::Block { client } => self.client_block(&client),
                ClientCmd::Unblock { mac } => {
                    let (s, site) = self.connect_site()?;
                    s.unblock_client(&site.id, &mac)?;
                    out!(self, "Unblocked {mac}.")
                }
                ClientCmd::Reconnect { client } => {
                    let (s, site, n) = self.find_client(&client)?;
                    s.reconnect_client(&site.id, &n)?;
                    out!(self, "Asked {} to reconnect.", n.label())
                }
            },
            Some(Cmd::Wifi { cmd: Some(c) }) => match c {
                WifiCmd::List => self.wifi_list(),
                WifiCmd::Show { network, password } => self.wifi_show(&network, password),
                WifiCmd::Qr { network } => self.wifi_qr(&network),
            },
            Some(Cmd::Alert { cmd: Some(c) }) => match c {
                AlertCmd::List { unresolved } => self.alert_list(unresolved),
                AlertCmd::Show { id } => self.alert_show(&id),
            },
        }
    }

    /// Prints the help of a command that only holds subcommands.
    fn group_help(&mut self, name: &str) -> Result<()> {
        let mut root = Cli::command();
        root.build();
        if let Some(sub) = root.find_subcommand_mut(name) {
            write!(self.io.out, "{}", sub.render_help())?;
        }
        Ok(())
    }

    fn run_tui(&mut self) -> Result<()> {
        if !self.io.interactive {
            return Err(Error::usage(
                "the interactive view needs a terminal; see 'gomada --help' for the commands that work without one",
            ));
        }
        let a = self.app()?;
        let opts = tui::Options {
            host: self.g.host.clone().unwrap_or_default(),
            site: self.g.site.clone().unwrap_or_default(),
            no_color: self.g.no_color,
        };
        (self.io.run_tui)(a, opts)
    }

    /// Builds the application core from the global flags.
    fn app(&mut self) -> Result<Arc<App>> {
        // The passphrase of an encrypted secret file: from the environment,
        // or by asking at the terminal. It is only asked for when the
        // secret is really read, which a valid cached token avoids.
        let (interactive, read) = (self.io.interactive, self.io.read_password.clone());
        let passphrase: app::PassphraseFn = Arc::new(move |host| {
            if let Some(p) = env(secret::ENV_PASSPHRASE) {
                return Ok(p);
            }
            if !interactive {
                return Err(Error::PassphraseRequired);
            }
            eprint!("Passphrase for host {host:?}: ");
            read().map_err(|e| Error::other(format!("read answer: {e}")))
        });
        let logger: Option<omada::Logger> = self.g.verbose.then(|| {
            Arc::new(|line: &str| eprintln!("time={} {line}", chrono::Utc::now().to_rfc3339())) as omada::Logger
        });
        App::new(app::Options {
            config_path: self.g.config.clone(),
            timeout: self.g.timeout,
            no_token_cache: self.g.no_token_cache,
            logger,
            now: self.io.now.clone(),
            passphrase: Some(passphrase),
            keychain: self.io.keychain.clone(),
        })
    }

    /// Validates the `--output` flag.
    fn format(&self) -> Result<Format> {
        output::parse_format(&self.g.format).map_err(Error::Usage)
    }

    /// Asks a question on standard error and reads one line.
    fn prompt(&mut self, question: &str) -> Result<String> {
        write!(self.io.err, "{question}")?;
        self.io.err.flush()?;
        let mut line = String::new();
        match self.io.stdin.read_line(&mut line) {
            Ok(0) => Err(Error::other("read answer: end of input")),
            Ok(_) => Ok(line.trim().to_string()),
            Err(e) => Err(Error::other(format!("read answer: {e}"))),
        }
    }

    /// Asks a question and reads the answer without echo.
    fn prompt_hidden(&mut self, question: &str) -> Result<String> {
        write!(self.io.err, "{question}")?;
        self.io.err.flush()?;
        (self.io.read_password)().map_err(|e| Error::other(format!("read answer: {e}")))
    }

    /// Guards an action that changes something. With --yes it passes. At a
    /// terminal it asks. Anywhere else it refuses, so a script can never
    /// trigger the action by accident.
    fn confirm(&mut self, question: &str) -> Result<()> {
        if self.g.yes {
            return Ok(());
        }
        if !self.io.interactive {
            return Err(Error::usage("this needs confirmation; pass --yes to go ahead without being asked"));
        }
        if is_yes(&self.prompt(&format!("{question} [y/N] "))?) {
            Ok(())
        } else {
            Err(Error::Canceled)
        }
    }

    /// Opens a session with the host chosen by --host.
    fn connect(&mut self) -> Result<Arc<Session>> {
        self.app()?.connect(self.g.host.as_deref().unwrap_or_default())
    }

    /// Opens a session and resolves the site chosen by --site.
    fn connect_site(&mut self) -> Result<(Arc<Session>, Site)> {
        let s = self.connect()?;
        let site = s.resolve_site(self.g.site.as_deref().unwrap_or_default())?;
        Ok((s, site))
    }

    // Hosts

    fn host_add(&mut self, add: HostAdd) -> Result<()> {
        let a = self.app()?;
        let mut h = NewHost {
            name: add.name.unwrap_or_default(),
            url: add.url.unwrap_or_default(),
            client_id: add.client_id.unwrap_or_default(),
            controller_id: add.controller_id.unwrap_or_default(),
            tls_pin: add.tls_pin.unwrap_or_default(),
            insecure_skip_tls_verify: add.insecure_skip_tls_verify,
            secret_store: add.secret_store.unwrap_or_default(),
            secret: String::new(),
        };

        h.name = self.ask(h.name, "Name for this host (e.g. home): ", "NAME")?;
        h.url = self.ask(h.url, "Controller address (e.g. https://192.168.0.2:8043): ", "--url")?;
        h.url = app::normalize_url(&h.url).map_err(|e| Error::Usage(e.to_string()))?;

        if h.insecure_skip_tls_verify {
            note!(self, "WARNING: certificate checking is off for this host. Anyone on the network path can read the client secret.")?;
        } else if h.tls_pin.is_empty() {
            let cert = a.check_certificate(&h.url)?;
            if !cert.trusted {
                self.confirm_certificate(&cert.fingerprint)?;
                h.tls_pin = cert.fingerprint;
            }
        }

        h.client_id = self.ask(h.client_id, "Client ID: ", "--client-id")?;
        h.secret = self.read_secret(add.secret_stdin)?;

        let mut result = a.add_host(&h);
        if result.as_ref().is_err_and(|e| e.is_keyring_unavailable()) && h.secret_store.is_empty() {
            note!(
                self,
                "This system has no keychain, so the client secret will be kept in a file encrypted with a passphrase."
            )?;
            let pass = self.new_passphrase()?;
            a.set_passphrase(Arc::new(move |_| Ok(pass.clone())));
            h.secret_store = secret::KIND_FILE.into();
            result = a.add_host(&h);
        }
        result?;

        let place = match h.secret_store.as_str() {
            secret::KIND_FILE => "an encrypted file next to the config".to_string(),
            secret::KIND_ENV => format!("nowhere; it is read from {} on each run", secret::ENV_VAR),
            _ => "the system keychain".to_string(),
        };
        out!(self, "Added host {:?} ({}). The client secret is kept in {place}.", h.name, h.url)?;
        if a.current_host() == h.name {
            out!(self, "{:?} is now the current host.", h.name)?;
        }
        Ok(())
    }

    /// Fills in a value that was not given as a flag.
    fn ask(&mut self, value: String, question: &str, flag: &str) -> Result<String> {
        if !value.is_empty() {
            return Ok(value);
        }
        if !self.io.interactive {
            return Err(Error::usage(format!("{flag} is required when not running in a terminal")));
        }
        let answer = self.prompt(question)?;
        if answer.is_empty() {
            return Err(Error::usage(format!("{flag} must not be empty")));
        }
        Ok(answer)
    }

    /// Shows the fingerprint of an untrusted certificate and asks whether
    /// to pin it.
    fn confirm_certificate(&mut self, fingerprint: &str) -> Result<()> {
        note!(self, "The controller's certificate is not signed by a trusted authority.\nThat is normal for a controller with a self-signed certificate.\n\n  Fingerprint: {fingerprint}\n")?;
        if !self.io.interactive {
            return Err(Error::usage(format!(
                "to trust this certificate, run the command again with --tls-pin {fingerprint}"
            )));
        }
        if is_yes(&self.prompt("Trust this certificate from now on? [y/N] ")?) {
            Ok(())
        } else {
            Err(Error::Canceled)
        }
    }

    /// Gets the client secret without it ever being a flag.
    fn read_secret(&mut self, from_stdin: bool) -> Result<String> {
        if from_stdin {
            let mut line = String::new();
            let read = self.io.stdin.read_line(&mut line).unwrap_or(0);
            let s = line.trim();
            return if !s.is_empty() {
                Ok(s.to_string())
            } else if read == 0 {
                Err(Error::usage("no client secret on standard input"))
            } else {
                Err(Error::usage("the client secret on standard input is empty"))
            };
        }
        if let Some(s) = env(secret::ENV_VAR) {
            return Ok(s);
        }
        if !self.io.interactive {
            return Err(Error::usage(format!(
                "no client secret: pipe it in with --secret-stdin or set {}",
                secret::ENV_VAR
            )));
        }
        let s = self.prompt_hidden("Client secret (not shown): ")?;
        let s = s.trim();
        if s.is_empty() {
            return Err(Error::usage("the client secret must not be empty"));
        }
        Ok(s.to_string())
    }

    /// Gets the passphrase for a new encrypted secret file. At a terminal
    /// it is typed twice.
    fn new_passphrase(&mut self) -> Result<String> {
        if let Some(p) = env(secret::ENV_PASSPHRASE) {
            return Ok(p);
        }
        if !self.io.interactive {
            return Err(Error::usage(format!(
                "no system keychain here: set {} to encrypt the secret in a file, or use --secret-store env",
                secret::ENV_PASSPHRASE
            )));
        }
        let first = self.prompt_hidden("Choose a passphrase (not shown): ")?;
        if first.is_empty() {
            return Err(Error::usage("the passphrase must not be empty"));
        }
        let second = self.prompt_hidden("Repeat the passphrase: ")?;
        if first != second {
            return Err(Error::other("the two passphrases differ"));
        }
        Ok(first)
    }

    fn host_list(&mut self) -> Result<()> {
        let format = self.format()?;
        let hosts = self.app()?.hosts();
        if format == Format::Json {
            return Ok(output::json(&mut self.io.out, &hosts)?);
        }
        if hosts.is_empty() {
            return note!(self, "No hosts yet. Add one with: gomada host add");
        }
        let rows: Vec<Vec<String>> = hosts
            .iter()
            .map(|h| {
                let mark = if h.current { "*" } else { "" };
                vec![mark.into(), h.name.clone(), h.url.clone(), h.secret_store.clone(), output::or_none(&h.site)]
            })
            .collect();
        Ok(output::table(&mut self.io.out, &["", "name", "url", "secret", "last site"], &rows)?)
    }

    fn host_remove(&mut self, name: &str) -> Result<()> {
        let a = self.app()?;
        if a.hosts().iter().any(|h| h.name == name) {
            self.confirm(&format!("Remove host {name:?} and its stored secret?"))?;
        }
        a.remove_host(name)?;
        out!(self, "Removed host {name:?}.")
    }

    fn host_test(&mut self, name: Option<String>) -> Result<()> {
        let a = self.app()?;
        let name = name.or_else(|| self.g.host.clone()).unwrap_or_default();
        let s = a.connect(&name)?;
        let sites = s.sites()?;
        out!(self, "OK: {} ({}) answers and accepts the credentials. Sites: {}.", s.host(), s.url(), sites.len())
    }

    // Sites

    fn site_list(&mut self) -> Result<()> {
        #[derive(Serialize)]
        struct SiteJson<'a> {
            id: &'a str,
            name: &'a str,
            current: bool,
        }
        let format = self.format()?;
        let s = self.connect()?;
        let sites = s.sites()?;
        let last = match sites.as_slice() {
            [only] => only.name.clone(),
            _ => s.last_site(),
        };
        if format == Format::Json {
            let out: Vec<SiteJson> =
                sites.iter().map(|x| SiteJson { id: &x.id, name: &x.name, current: x.name == last }).collect();
            return Ok(output::json(&mut self.io.out, &out)?);
        }
        let rows: Vec<Vec<String>> = sites
            .iter()
            .map(|x| vec![if x.name == last { "*" } else { "" }.into(), x.name.clone(), x.id.clone()])
            .collect();
        Ok(output::table(&mut self.io.out, &["", "name", "id"], &rows)?)
    }

    // Devices

    fn device_list(&mut self, kind: String, filter: String, updates: bool) -> Result<()> {
        let format = self.format()?;
        let kind = kind.to_lowercase();
        if !matches!(kind.as_str(), "" | omada::TYPE_GATEWAY | omada::TYPE_SWITCH | omada::TYPE_AP) {
            return Err(Error::usage(format!("unknown device type {kind:?}; use gateway, switch, or ap")));
        }
        let (s, site) = self.connect_site()?;
        let devs: Vec<Device> = s
            .devices(&site.id)?
            .into_iter()
            .filter(|d| (kind.is_empty() || d.kind == kind) && output::matches(&device_row(d), &filter))
            .collect();
        if updates {
            return self.list_updates(format, &s, &site, &devs);
        }
        if format == Format::Json {
            let out: Vec<DeviceJson> = devs.iter().map(DeviceJson::from).collect();
            return Ok(output::json(&mut self.io.out, &out)?);
        }
        let rows: Vec<Vec<String>> = devs.iter().map(device_row).collect();
        let header = ["status", "name", "type", "model", "ip", "mac", "uptime", "cpu", "mem"];
        Ok(output::table(&mut self.io.out, &header, &rows)?)
    }

    /// Prints the connected devices that have a firmware update waiting.
    /// It asks the controller once per device.
    fn list_updates(&mut self, format: Format, s: &Session, site: &Site, devs: &[Device]) -> Result<()> {
        let mut found: Vec<(&Device, Firmware)> = Vec::new();
        for d in devs.iter().filter(|d| d.online()) {
            let fw = s.firmware(&site.id, d).map_err(|e| e.context(format!("firmware of {}", d.label())))?;
            if fw.update_available() {
                found.push((d, fw));
            }
        }
        if format == Format::Json {
            let out: Vec<FirmwareJson> = found.iter().map(|(d, fw)| FirmwareJson::new(d, fw)).collect();
            return Ok(output::json(&mut self.io.out, &out)?);
        }
        if found.is_empty() {
            return note!(self, "Every connected device is up to date.");
        }
        let rows: Vec<Vec<String>> = found
            .iter()
            .map(|(d, fw)| {
                vec![
                    d.label().into(),
                    output::or_none(&d.kind),
                    output::or_none(&d.model),
                    fw.current.clone(),
                    fw.latest.clone(),
                ]
            })
            .collect();
        Ok(output::table(&mut self.io.out, &["name", "type", "model", "installed", "latest"], &rows)?)
    }

    fn find_device(&mut self, target: &str) -> Result<(Arc<Session>, Site, Device)> {
        let (s, site) = self.connect_site()?;
        let d = s.find_device(&site.id, target)?;
        Ok((s, site, d))
    }

    fn device_show(&mut self, target: &str) -> Result<()> {
        let format = self.format()?;
        let (_, _, d) = self.find_device(target)?;
        if format == Format::Json {
            return Ok(output::json(&mut self.io.out, &DeviceJson::from(&d))?);
        }
        Ok(output::fields(&mut self.io.out, &output::device_fields(&d))?)
    }

    fn device_reboot(&mut self, target: &str) -> Result<()> {
        let (s, site, d) = self.find_device(target)?;
        self.confirm(&format!("Reboot {} ({}, {})?", d.label(), d.kind, d.mac))?;
        s.reboot_device(&site.id, &d)?;
        out!(self, "Rebooting {}.", d.label())
    }

    fn device_rename(&mut self, target: &str, name: &str) -> Result<()> {
        omada::validate_name(name).map_err(Error::Usage)?;
        let (s, site, d) = self.find_device(target)?;
        s.rename_device(&site.id, &d, name)?;
        out!(self, "Renamed {} to {name}.", d.label())
    }

    fn device_firmware(&mut self, target: &str) -> Result<()> {
        let format = self.format()?;
        let (s, site, d) = self.find_device(target)?;
        let fw = s.firmware(&site.id, &d)?;
        if format == Format::Json {
            return Ok(output::json(&mut self.io.out, &FirmwareJson::new(&d, &fw))?);
        }
        Ok(output::fields(&mut self.io.out, &output::firmware_fields(&d, &fw))?)
    }

    fn device_upgrade(&mut self, target: &str, no_wait: bool) -> Result<()> {
        let (s, site, d) = self.find_device(target)?;
        let fw = s.firmware(&site.id, &d)?;
        let label = d.label();
        if !fw.update_available() {
            return out!(self, "{label} is already up to date (firmware {}).", output::or_none(&fw.current));
        }
        self.confirm(&format!(
            "Update {label} ({}) from {} to {}? It restarts and its clients lose their connection for a few minutes.",
            d.kind, fw.current, fw.latest
        ))?;
        s.upgrade_device(&site.id, &d)?;
        if no_wait {
            return out!(self, "Updating {label} to {}. It restarts when it is done.", fw.latest);
        }
        note!(self, "Updating {label} to {}. This takes a few minutes; do not unplug the device.", fw.latest)?;
        let poll = self.io.poll_every.unwrap_or(Duration::from_secs(5));
        match s.wait_for_upgrade(&site.id, &d, poll, UPGRADE_TIMEOUT) {
            Err(e) if e.is_deadline_exceeded() => Err(Error::other(format!(
                "{label} has not reported back after {} minutes; check it in the controller",
                UPGRADE_TIMEOUT.as_secs() / 60
            ))),
            Err(e) => Err(e),
            Ok(false) => {
                Err(Error::other(format!("the update of {label} did not succeed; check the device in the controller")))
            }
            Ok(true) => out!(self, "{label} now runs firmware {}.", fw.latest),
        }
    }

    // Clients

    fn client_list(&mut self, wired: bool, wireless: bool, blocked: bool, filter: String) -> Result<()> {
        let format = self.format()?;
        if wired && wireless {
            return Err(Error::usage("--wired and --wireless exclude each other; leave both out to see all clients"));
        }
        if blocked && (wired || wireless) {
            return Err(Error::usage(
                "--blocked lists blocked clients and cannot be combined with --wired or --wireless",
            ));
        }
        let (s, site) = self.connect_site()?;
        if blocked {
            return self.list_blocked(format, &s, &site, &filter);
        }
        let clients: Vec<NetClient> = s
            .clients(&site.id)?
            .into_iter()
            .filter(|n| {
                !(wired && n.wireless) && !(wireless && !n.wireless) && output::matches(&client_row(n), &filter)
            })
            .collect();
        if format == Format::Json {
            let out: Vec<ClientJson> = clients.iter().map(ClientJson::from).collect();
            return Ok(output::json(&mut self.io.out, &out)?);
        }
        let rows: Vec<Vec<String>> = clients.iter().map(client_row).collect();
        let header = ["name", "ip", "mac", "connected to", "signal", "down", "up", "uptime"];
        Ok(output::table(&mut self.io.out, &header, &rows)?)
    }

    /// Prints the clients that are barred from the site.
    fn list_blocked(&mut self, format: Format, s: &Session, site: &Site, filter: &str) -> Result<()> {
        #[derive(Serialize)]
        struct BlockedJson<'a> {
            mac: &'a str,
            name: &'a str,
            wireless: bool,
            /// RFC 3339, UTC.
            #[serde(skip_serializing_if = "Option::is_none")]
            last_seen: Option<String>,
        }
        let row = |k: &KnownClient| {
            let kind = if k.wireless { "wireless" } else { "wired" };
            vec![k.label().to_string(), k.mac.clone(), kind.into(), output::timestamp(k.last_seen())]
        };
        let blocked: Vec<KnownClient> =
            s.blocked_clients(&site.id)?.into_iter().filter(|k| output::matches(&row(k), filter)).collect();
        if format == Format::Json {
            let out: Vec<BlockedJson> = blocked
                .iter()
                .map(|k| BlockedJson {
                    mac: &k.mac,
                    name: &k.name,
                    wireless: k.wireless,
                    last_seen: k.last_seen().map(output::rfc3339),
                })
                .collect();
            return Ok(output::json(&mut self.io.out, &out)?);
        }
        if blocked.is_empty() {
            return note!(self, "No client is blocked.");
        }
        let rows: Vec<Vec<String>> = blocked.iter().map(row).collect();
        Ok(output::table(&mut self.io.out, &["name", "mac", "type", "last seen"], &rows)?)
    }

    /// Resolves the CLIENT argument of a command.
    fn find_client(&mut self, target: &str) -> Result<(Arc<Session>, Site, NetClient)> {
        let (s, site) = self.connect_site()?;
        let n = s.find_client(&site.id, target)?;
        Ok((s, site, n))
    }

    fn client_show(&mut self, target: &str) -> Result<()> {
        let format = self.format()?;
        let (_, _, n) = self.find_client(target)?;
        if format == Format::Json {
            return Ok(output::json(&mut self.io.out, &ClientJson::from(&n))?);
        }
        Ok(output::fields(&mut self.io.out, &output::client_fields(&n))?)
    }

    fn client_rename(&mut self, target: &str, name: &str) -> Result<()> {
        omada::validate_name(name).map_err(Error::Usage)?;
        let (s, site, n) = self.find_client(target)?;
        s.rename_client(&site.id, &n, name)?;
        out!(self, "Renamed {} to {name}.", n.label())
    }

    fn client_block(&mut self, target: &str) -> Result<()> {
        let (s, site, n) = self.find_client(target)?;
        self.confirm(&format!("Block {} ({})?", n.label(), n.mac))?;
        s.block_client(&site.id, &n)?;
        out!(self, "Blocked {} ({}). Unblock it with: gomada client unblock {}", n.label(), n.mac, n.mac)
    }

    // Wi-Fi

    fn wifi_list(&mut self) -> Result<()> {
        let format = self.format()?;
        let (s, site) = self.connect_site()?;
        let ssids = s.ssids(&site.id)?;
        if format == Format::Json {
            let out: Vec<WifiJson> = ssids.iter().map(WifiJson::from).collect();
            return Ok(output::json(&mut self.io.out, &out)?);
        }
        let rows: Vec<Vec<String>> = ssids
            .iter()
            .map(|x| {
                vec![
                    x.name.clone(),
                    x.security_text().into(),
                    output::or_none(&x.band_text()),
                    output::yes_no(x.guest),
                    output::yes_no(x.hidden()),
                ]
            })
            .collect();
        Ok(output::table(&mut self.io.out, &["name", "security", "band", "guest", "hidden"], &rows)?)
    }

    /// Resolves the NETWORK argument of a command.
    fn find_ssid(&mut self, target: &str) -> Result<(Arc<Session>, Site, Ssid)> {
        let (s, site) = self.connect_site()?;
        let ssid = s.find_ssid(&site.id, target)?;
        Ok((s, site, ssid))
    }

    fn wifi_show(&mut self, target: &str, show_password: bool) -> Result<()> {
        let format = self.format()?;
        let (s, site, ssid) = self.find_ssid(target)?;
        // shown is what the Password line says.
        let mut shown = "hidden; pass --password to show it".to_string();
        let mut j = WifiJson::from(&ssid);
        if show_password {
            j.password = s.wifi_password(&site.id, &ssid)?;
            shown = if j.password.is_empty() { "not revealed by the controller".into() } else { j.password.clone() };
        }
        if format == Format::Json {
            return Ok(output::json(&mut self.io.out, &j)?);
        }
        Ok(output::fields(&mut self.io.out, &output::ssid_fields(&ssid, &shown))?)
    }

    fn wifi_qr(&mut self, target: &str) -> Result<()> {
        let (s, site, ssid) = self.find_ssid(target)?;
        let password = s.wifi_password(&site.id, &ssid)?;
        let payload = app::wifi_qr_payload(&ssid, &password)?;
        let code = qr::Code::encode(&payload).map_err(Error::Other)?;
        // Light ink on black has the right polarity whatever the terminal's
        // own colors are. Without color the code still has the right
        // polarity on a dark terminal.
        let (start, end) =
            if self.io.interactive && env("NO_COLOR").is_none() { ("\x1b[97;40m", "\x1b[0m") } else { ("", "") };
        let mut text = String::new();
        for line in code.half_blocks(2) {
            text.push_str(&format!("{start}{line}{end}\n"));
        }
        write!(self.io.out, "{text}")?;
        note!(self, "Scan to join {}.", ssid.name)
    }

    // Alerts

    fn alert_list(&mut self, unresolved: bool) -> Result<()> {
        let format = self.format()?;
        let (s, site) = self.connect_site()?;
        let alerts: Vec<Alert> = s.alerts(&site.id)?.into_iter().filter(|a| !unresolved || !a.resolved).collect();
        if format == Format::Json {
            let out: Vec<AlertJson> = alerts.iter().map(AlertJson::from).collect();
            return Ok(output::json(&mut self.io.out, &out)?);
        }
        let rows: Vec<Vec<String>> = alerts
            .iter()
            .map(|a| {
                vec![
                    output::timestamp(Some(a.time())),
                    output::or_none(&a.level),
                    output::alert_state(a).into(),
                    a.id.clone(),
                    a.content.clone(),
                ]
            })
            .collect();
        Ok(output::table(&mut self.io.out, &["time", "level", "state", "id", "message"], &rows)?)
    }

    fn alert_show(&mut self, id: &str) -> Result<()> {
        let format = self.format()?;
        let (s, site) = self.connect_site()?;
        let a = s.find_alert(&site.id, id)?;
        if format == Format::Json {
            return Ok(output::json(&mut self.io.out, &AlertJson::from(&a))?);
        }
        Ok(output::fields(&mut self.io.out, &output::alert_fields(&a))?)
    }
}

// The JSON printed with -o json. Field names and order are a contract with
// scripts.

#[derive(Serialize)]
struct DeviceJson {
    mac: String,
    name: String,
    #[serde(rename = "type")]
    kind: String,
    model: String,
    ip: String,
    status: String,
    online: bool,
    uptime: String,
    #[serde(serialize_with = "ser_num")]
    cpu_percent: f64,
    #[serde(serialize_with = "ser_num")]
    mem_percent: f64,
    firmware: String,
    serial: String,
}

impl From<&Device> for DeviceJson {
    fn from(d: &Device) -> Self {
        DeviceJson {
            mac: d.mac.clone(),
            name: d.name.clone(),
            kind: d.kind.clone(),
            model: d.model.clone(),
            ip: d.ip.clone(),
            status: d.status_text(),
            online: d.online(),
            uptime: d.uptime.clone(),
            cpu_percent: d.cpu,
            mem_percent: d.mem,
            firmware: d.firmware.clone(),
            serial: d.serial.clone(),
        }
    }
}

fn device_row(d: &Device) -> Vec<String> {
    let (uptime, cpu, mem) = if d.online() {
        (output::uptime(&d.uptime), output::percent(d.cpu), output::percent(d.mem))
    } else {
        (output::NONE.into(), output::NONE.into(), output::NONE.into())
    };
    vec![
        d.status_text(),
        d.label().into(),
        output::or_none(&d.kind),
        output::or_none(&d.model),
        output::or_none(&d.ip),
        d.mac.clone(),
        uptime,
        cpu,
        mem,
    ]
}

#[derive(Serialize)]
struct FirmwareJson {
    mac: String,
    name: String,
    installed: String,
    latest: String,
    update_available: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    release_notes: String,
}

impl FirmwareJson {
    fn new(d: &Device, f: &Firmware) -> Self {
        FirmwareJson {
            mac: d.mac.clone(),
            name: d.name.clone(),
            installed: if f.current.is_empty() { d.firmware.clone() } else { f.current.clone() },
            latest: f.latest.clone(),
            update_available: f.update_available(),
            release_notes: f.release_notes.clone(),
        }
    }
}

#[derive(Serialize)]
struct ClientJson {
    mac: String,
    name: String,
    host_name: String,
    ip: String,
    vendor: String,
    wireless: bool,
    ssid: String,
    network: String,
    connected_to: String,
    port: i64,
    #[serde(serialize_with = "ser_num")]
    signal_dbm: f64,
    uptime_seconds: i64,
    down_bytes: i64,
    up_bytes: i64,
}

impl From<&NetClient> for ClientJson {
    fn from(n: &NetClient) -> Self {
        ClientJson {
            mac: n.mac.clone(),
            name: n.name.clone(),
            host_name: n.host_name.clone(),
            ip: n.ip.clone(),
            vendor: n.vendor.clone(),
            wireless: n.wireless,
            ssid: n.ssid.clone(),
            network: n.network_name.clone(),
            connected_to: n.connected_to(),
            port: n.port as i64,
            signal_dbm: n.rssi,
            uptime_seconds: n.uptime as i64,
            down_bytes: n.traffic_down as i64,
            up_bytes: n.traffic_up as i64,
        }
    }
}

fn client_row(n: &NetClient) -> Vec<String> {
    vec![
        n.label().into(),
        output::or_none(&n.ip),
        n.mac.clone(),
        output::or_none(&n.connected_to()),
        output::signal(n),
        output::bytes(n.traffic_down),
        output::bytes(n.traffic_up),
        output::seconds(n.uptime),
    ]
}

fn is_zero(n: &i64) -> bool {
    *n == 0
}

#[derive(Serialize)]
struct WifiJson {
    id: String,
    name: String,
    security: String,
    band: String,
    guest: bool,
    hidden: bool,
    #[serde(skip_serializing_if = "is_zero")]
    vlan_id: i64,
    /// Present only when it was asked for with --password.
    #[serde(skip_serializing_if = "String::is_empty")]
    password: String,
}

impl From<&Ssid> for WifiJson {
    fn from(s: &Ssid) -> Self {
        WifiJson {
            id: s.id.clone(),
            name: s.name.clone(),
            security: s.security_text().into(),
            band: s.band_text(),
            guest: s.guest,
            hidden: s.hidden(),
            vlan_id: if s.vlan_on { s.vlan_id as i64 } else { 0 },
            password: String::new(),
        }
    }
}

#[derive(Serialize)]
struct AlertJson {
    id: String,
    /// RFC 3339, UTC.
    time: String,
    level: String,
    module: String,
    resolved: bool,
    message: String,
}

impl From<&Alert> for AlertJson {
    fn from(a: &Alert) -> Self {
        AlertJson {
            id: a.id.clone(),
            time: output::rfc3339(a.time()),
            level: a.level.clone(),
            module: a.module.clone(),
            resolved: a.resolved,
            message: a.content.clone(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_command_tree_is_consistent() {
        Cli::command().debug_assert();
    }

    #[test]
    fn exit_codes_follow_the_error_class() {
        assert_eq!(exit_code(&Error::usage("x")), 2);
        assert_eq!(exit_code(&Error::Auth("x".into())), 3);
        assert_eq!(exit_code(&Error::Unreachable("x".into())), 4);
        assert_eq!(exit_code(&Error::PinMismatch { got: "sha256:00".into() }), 4);
        assert_eq!(exit_code(&Error::Unreachable("x".into()).context("could not read the controller ID")), 4);
        assert_eq!(exit_code(&Error::UntrustedCertificate(Box::new(Error::Certificate("x".into())))), 4);
        assert_eq!(exit_code(&Error::other("x")), 1);
        assert_eq!(exit_code(&Error::Canceled), 1);
    }

    #[test]
    fn hints() {
        assert!(hint_for(&Error::PinMismatch { got: "x".into() }).unwrap().contains("intercepting"));
        assert!(hint_for(&Error::PassphraseRequired).unwrap().contains("GOMADA_PASSPHRASE"));
        assert_eq!(hint_for(&Error::other("x")), None);
    }

    #[test]
    fn timeouts_parse_like_before() {
        assert_eq!(parse_timeout("15s").unwrap(), Duration::from_secs(15));
        assert_eq!(parse_timeout("1m30s").unwrap(), Duration::from_secs(90));
        assert_eq!(parse_timeout("500ms").unwrap(), Duration::from_millis(500));
        assert!(parse_timeout("soon").is_err());
    }
}
