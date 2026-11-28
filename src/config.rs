//! Reads and writes the gomada configuration file: the list of controller
//! profiles ("hosts") and which one is current. The file never holds
//! secrets or tokens.

use std::collections::BTreeMap;
use std::io::Write;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

use crate::error::{Error, Result};

/// The configuration format written by this build.
pub const VERSION: u32 = 1;

/// Names the environment variable that overrides the config file location.
pub const ENV_PATH: &str = "GOMADA_CONFIG";

fn is_false(b: &bool) -> bool {
    !*b
}

/// The content of the configuration file.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    #[serde(default)]
    pub version: u32,
    /// The name of the host used when none is given.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub current: String,
    /// Turns off keeping access tokens between runs.
    #[serde(default, skip_serializing_if = "is_false")]
    pub no_token_cache: bool,
    /// The saved controller profiles by name.
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub hosts: BTreeMap<String, Host>,
}

/// One saved controller profile.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Host {
    /// The controller address, e.g. "https://192.168.0.2:8043".
    pub url: String,
    /// The controller's omadacId.
    pub controller: String,
    /// Identifies the Open API application.
    pub client_id: String,
    /// Where the client secret is kept: "keyring", "file", or "env".
    pub secret_store: String,
    /// The fingerprint of the controller's self-signed certificate.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub tls_pin: String,
    /// Accepts any certificate. Avoid it.
    #[serde(default, skip_serializing_if = "is_false")]
    pub insecure_skip_tls_verify: bool,
    /// The site used last on this host.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub site: String,
}

/// Returns where the configuration file lives: `$GOMADA_CONFIG` if set,
/// else gomada/config.yaml below `$XDG_CONFIG_HOME` if set, else below the
/// operating system's configuration directory.
pub fn default_path() -> Result<PathBuf> {
    let env = |name: &str| std::env::var_os(name).filter(|v| !v.is_empty());
    if let Some(p) = env(ENV_PATH) {
        return Ok(PathBuf::from(p));
    }
    let base = match env("XDG_CONFIG_HOME") {
        Some(b) => PathBuf::from(b),
        None => dirs::config_dir().ok_or_else(|| Error::other("cannot find the configuration directory"))?,
    };
    Ok(base.join("gomada").join("config.yaml"))
}

impl Config {
    fn empty() -> Config {
        Config { version: VERSION, ..Default::default() }
    }

    /// Reads the configuration file. A missing file is not an error: it
    /// yields an empty configuration.
    pub fn load(path: &Path) -> Result<Config> {
        let data = match std::fs::read_to_string(path) {
            Ok(d) => d,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(Config::empty()),
            Err(e) => return Err(Error::other(format!("read config: {e}"))),
        };
        if data.trim().is_empty() {
            return Ok(Config::empty());
        }
        let shown = path.display();
        // A host written as "name:" with nothing below it would otherwise
        // be reported as a type error deep inside the file.
        if let Ok(serde_yaml_ng::Value::Mapping(top)) = serde_yaml_ng::from_str(&data) {
            if let Some(serde_yaml_ng::Value::Mapping(hosts)) = top.get("hosts") {
                for (name, host) in hosts {
                    if host.is_null() {
                        let name = name.as_str().unwrap_or_default();
                        return Err(Error::other(format!("{shown}: host {name:?} is empty")));
                    }
                }
            }
        }
        let mut cfg: Config = serde_yaml_ng::from_str(&data)
            .map_err(|e| Error::other(format!("{shown} is not a valid config file: {e}")))?;
        migrate(&mut cfg).map_err(|e| Error::other(format!("{shown}: {e}")))?;
        Ok(cfg)
    }

    /// Writes the configuration atomically, readable by the owner only.
    pub fn save(&mut self, path: &Path) -> Result<()> {
        self.version = VERSION;
        let text = serde_yaml_ng::to_string(self).map_err(|e| Error::other(format!("encode config: {e}")))?;
        write_file_atomic(path, text.as_bytes()).map_err(|e| Error::other(format!("write config: {e}")))
    }

    /// Returns the host names in alphabetical order.
    pub fn host_names(&self) -> Vec<String> {
        self.hosts.keys().cloned().collect()
    }
}

/// Brings a configuration read from disk up to the current [`VERSION`].
/// Each future format change adds one step here.
fn migrate(cfg: &mut Config) -> std::result::Result<(), String> {
    if cfg.version == 0 {
        cfg.version = VERSION; // files written by hand without a version
    } else if cfg.version > VERSION {
        return Err(format!(
            "the config file has version {}, but this gomada understands up to version {VERSION}; please upgrade gomada",
            cfg.version
        ));
    }
    Ok(())
}

/// Writes data to path through a temporary file and a rename, so a crash
/// never leaves a half-written file. The file and any directory it has to
/// create are accessible to the owner only.
pub fn write_file_atomic(path: &Path, data: &[u8]) -> std::io::Result<()> {
    let dir = match path.parent() {
        Some(d) if !d.as_os_str().is_empty() => d,
        _ => Path::new("."),
    };
    let mut builder = std::fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder.create(dir)?;
    // tempfile creates the file for the owner only (0600 on Unix).
    let mut tmp = tempfile::Builder::new().prefix(".gomada-").suffix(".tmp").tempfile_in(dir)?;
    tmp.write_all(data)?;
    tmp.as_file().sync_all()?;
    tmp.persist(path).map_err(|e| e.error)?;
    Ok(())
}

/// Checks a profile name. Names end up in file names and keychain entries,
/// so they are limited to letters, digits, dot, dash, and underscore.
pub fn validate_host_name(name: &str) -> Result<()> {
    let mut chars = name.chars();
    let ok = chars.next().is_some_and(|c| c.is_ascii_alphanumeric())
        && chars.all(|c| c.is_ascii_alphanumeric() || matches!(c, '.' | '_' | '-'))
        && name.len() <= 63;
    if ok {
        Ok(())
    } else {
        Err(Error::other(format!(
            "invalid host name {name:?}: use letters, digits, '.', '-', or '_', starting with a letter or digit, at most 63 characters"
        )))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample() -> Config {
        let mut cfg = Config::empty();
        cfg.current = "home".into();
        cfg.hosts.insert(
            "home".into(),
            Host {
                url: "https://192.168.0.2:8043".into(),
                controller: "abc".into(),
                client_id: "id".into(),
                secret_store: "keyring".into(),
                tls_pin: "sha256:00".into(),
                site: "Default".into(),
                ..Default::default()
            },
        );
        cfg.hosts.insert(
            "ci".into(),
            Host {
                url: "http://c".into(),
                controller: "x".into(),
                client_id: "y".into(),
                secret_store: "env".into(),
                ..Default::default()
            },
        );
        cfg
    }

    #[test]
    fn missing_and_empty_files_give_an_empty_config() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("nope.yaml");
        assert_eq!(Config::load(&path).unwrap(), Config::empty());
        std::fs::write(&path, "  \n").unwrap();
        assert_eq!(Config::load(&path).unwrap(), Config::empty());
    }

    #[test]
    fn save_and_load_round_trip() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("sub").join("config.yaml");
        let mut cfg = sample();
        cfg.save(&path).unwrap();
        assert_eq!(Config::load(&path).unwrap(), cfg);
        let text = std::fs::read_to_string(&path).unwrap();
        assert!(text.starts_with("version: 1\ncurrent: home\nhosts:\n  ci:\n"), "{text}");
        assert!(!text.contains("no_token_cache") && !text.contains("insecure"), "{text}");
        assert_eq!(cfg.host_names(), ["ci", "home"]);
    }

    #[cfg(unix)]
    #[test]
    fn the_file_is_private() {
        use std::os::unix::fs::PermissionsExt;
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("sub").join("config.yaml");
        sample().save(&path).unwrap();
        assert_eq!(std::fs::metadata(&path).unwrap().permissions().mode() & 0o777, 0o600);
        assert_eq!(std::fs::metadata(path.parent().unwrap()).unwrap().permissions().mode() & 0o777, 0o700);
        // Nothing but the config is left behind.
        assert_eq!(std::fs::read_dir(path.parent().unwrap()).unwrap().count(), 1);
    }

    #[test]
    fn reads_a_file_written_by_the_go_version() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("config.yaml");
        let go = "version: 1\ncurrent: home\nhosts:\n  home:\n    url: https://192.168.0.2:8043\n    controller: abc\n    client_id: id\n    secret_store: keyring\n    tls_pin: sha256:00\n    site: Default\n  ci:\n    url: http://c\n    controller: x\n    client_id: \"y\"\n    secret_store: env\n";
        std::fs::write(&path, go).unwrap();
        assert_eq!(Config::load(&path).unwrap(), sample());
    }

    #[test]
    fn bad_files_are_refused_with_the_path() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("config.yaml");
        for (text, want) in [
            ("version: 1\nsecret: oops\n", "not a valid config file"),
            ("hosts: [1, 2]\n", "not a valid config file"),
            ("version: 99\n", "please upgrade gomada"),
            ("hosts:\n  home:\n", "host \"home\" is empty"),
            ("hosts:\n  home:\n    url: x\n    typo: 1\n", "not a valid config file"),
        ] {
            std::fs::write(&path, text).unwrap();
            let err = Config::load(&path).unwrap_err().to_string();
            assert!(err.contains(want) && err.contains("config.yaml"), "{text:?}: {err}");
        }
    }

    #[test]
    fn a_file_without_a_version_is_version_one() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("config.yaml");
        std::fs::write(&path, "current: a\n").unwrap();
        assert_eq!(Config::load(&path).unwrap().version, 1);
    }

    #[test]
    fn host_names_are_validated() {
        for ok in ["home", "a", "A.b-c_d", "9lives", &"a".repeat(63)] {
            assert!(validate_host_name(ok).is_ok(), "{ok}");
        }
        for bad in ["", ".home", "-x", "a b", "a/b", "é", &"a".repeat(64)] {
            assert!(validate_host_name(bad).is_err(), "{bad}");
        }
    }
}
