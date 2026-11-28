//! Stores the Open API client secret of each host. The rest of the
//! application talks to the [`Store`] trait and never knows which backend
//! is in use.

use std::collections::HashMap;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};

use aes_gcm::aead::{Aead, KeyInit, Payload};
use aes_gcm::{Aes256Gcm, Nonce};
use base64::engine::general_purpose::STANDARD as B64;
use base64::Engine;
use serde::{Deserialize, Serialize};

use crate::config::write_file_atomic;
use crate::error::{Error, Result};

/// Store kinds, as written to the `secret_store` field of the configuration.
pub const KIND_KEYRING: &str = "keyring"; // operating system keychain
pub const KIND_FILE: &str = "file"; // passphrase-encrypted file
pub const KIND_ENV: &str = "env"; // GOMADA_CLIENT_SECRET, nothing stored

/// Names the environment variable that supplies the client secret. When set
/// it wins over every store.
pub const ENV_VAR: &str = "GOMADA_CLIENT_SECRET";

/// Names the environment variable that supplies the passphrase of an
/// encrypted secret file, for use without a terminal.
pub const ENV_PASSPHRASE: &str = "GOMADA_PASSPHRASE";

/// The service name of all gomada keychain entries.
pub const KEYRING_SERVICE: &str = "gomada";

fn env(name: &str) -> Option<String> {
    std::env::var(name).ok().filter(|v| !v.is_empty())
}

/// Keeps one secret per profile (host name).
pub trait Store: Send + Sync {
    /// Returns the secret, or [`Error::SecretNotFound`].
    fn get(&self, profile: &str) -> Result<String>;
    /// Stores or replaces the secret.
    fn set(&self, profile: &str, secret: &str) -> Result<()>;
    /// Removes the secret. Deleting a missing secret is not an error.
    fn delete(&self, profile: &str) -> Result<()>;
}

/// The operating system keychain as gomada uses it: one text value per
/// account of the "gomada" service. Tests replace it with [`MemoryKeychain`].
pub trait Keychain: Send + Sync {
    /// Returns the value, or `None` when there is none.
    fn get(&self, account: &str) -> std::result::Result<Option<String>, String>;
    fn set(&self, account: &str, value: &str) -> std::result::Result<(), String>;
    /// Removes the value. Removing a missing value is not an error.
    fn delete(&self, account: &str) -> std::result::Result<(), String>;
}

/// Keychain on macOS, Credential Manager on Windows, the Secret Service on
/// Linux. Entries are named the way the Go version of gomada named them, so
/// both find each other's secrets.
pub struct OsKeychain;

impl OsKeychain {
    fn entry(account: &str) -> std::result::Result<keyring::Entry, String> {
        // go-keyring names Windows credentials "service:user"; the keyring
        // crate would use "user.service" unless told otherwise.
        #[cfg(windows)]
        let entry = keyring::Entry::new_with_target(&format!("{KEYRING_SERVICE}:{account}"), KEYRING_SERVICE, account);
        #[cfg(not(windows))]
        let entry = keyring::Entry::new(KEYRING_SERVICE, account);
        entry.map_err(|e| e.to_string())
    }
}

/// Undoes the wrapping go-keyring puts around values on macOS.
fn decode_go_keyring(raw: Vec<u8>) -> std::result::Result<String, String> {
    let text = String::from_utf8(raw).map_err(|_| "the stored value is not text".to_string())?;
    let decoded = if let Some(b64) = text.strip_prefix("go-keyring-base64:") {
        B64.decode(b64).ok()
    } else if let Some(hexed) = text.strip_prefix("go-keyring-encoded:") {
        hex::decode(hexed).ok()
    } else {
        return Ok(text);
    };
    decoded.and_then(|bytes| String::from_utf8(bytes).ok()).ok_or_else(|| "the stored value is damaged".to_string())
}

impl Keychain for OsKeychain {
    fn get(&self, account: &str) -> std::result::Result<Option<String>, String> {
        // Raw bytes, because go-keyring stores UTF-8 where the keyring
        // crate's text functions expect UTF-16 on Windows.
        match Self::entry(account)?.get_secret() {
            Ok(raw) => decode_go_keyring(raw).map(Some),
            Err(keyring::Error::NoEntry) => Ok(None),
            Err(e) => Err(e.to_string()),
        }
    }

    fn set(&self, account: &str, value: &str) -> std::result::Result<(), String> {
        Self::entry(account)?.set_secret(value.as_bytes()).map_err(|e| e.to_string())
    }

    fn delete(&self, account: &str) -> std::result::Result<(), String> {
        match Self::entry(account)?.delete_credential() {
            Ok(()) | Err(keyring::Error::NoEntry) => Ok(()),
            Err(e) => Err(e.to_string()),
        }
    }
}

/// A keychain in memory, for tests. `unavailable` makes every call fail the
/// way a system without a keychain does.
#[derive(Default)]
pub struct MemoryKeychain {
    pub values: Mutex<HashMap<String, String>>,
    pub unavailable: std::sync::atomic::AtomicBool,
}

impl MemoryKeychain {
    fn check(&self) -> std::result::Result<(), String> {
        if self.unavailable.load(std::sync::atomic::Ordering::SeqCst) {
            return Err("no keyring daemon".into());
        }
        Ok(())
    }
}

impl Keychain for MemoryKeychain {
    fn get(&self, account: &str) -> std::result::Result<Option<String>, String> {
        self.check()?;
        Ok(self.values.lock().unwrap().get(account).cloned())
    }

    fn set(&self, account: &str, value: &str) -> std::result::Result<(), String> {
        self.check()?;
        self.values.lock().unwrap().insert(account.into(), value.into());
        Ok(())
    }

    fn delete(&self, account: &str) -> std::result::Result<(), String> {
        self.check()?;
        self.values.lock().unwrap().remove(account);
        Ok(())
    }
}

/// Stores secrets in the operating system keychain.
pub struct Keyring(pub Arc<dyn Keychain>);

impl Store for Keyring {
    fn get(&self, profile: &str) -> Result<String> {
        match self.0.get(profile) {
            Ok(Some(s)) => Ok(s),
            Ok(None) => Err(Error::SecretNotFound),
            Err(e) => Err(Error::other(format!("read from the system keychain: {e}"))),
        }
    }

    fn set(&self, profile: &str, secret: &str) -> Result<()> {
        self.0.set(profile, secret).map_err(|e| Error::other(format!("write to the system keychain: {e}")))
    }

    fn delete(&self, profile: &str) -> Result<()> {
        self.0.delete(profile).map_err(|e| Error::other(format!("delete from the system keychain: {e}")))
    }
}

/// Reads the secret from the environment and stores nothing.
pub struct Env;

impl Store for Env {
    fn get(&self, _: &str) -> Result<String> {
        env(ENV_VAR).ok_or_else(|| Error::other(format!("the {ENV_VAR} environment variable is not set")))
    }

    fn set(&self, _: &str, _: &str) -> Result<()> {
        Ok(()) // the secret lives in the environment
    }

    fn delete(&self, _: &str) -> Result<()> {
        Ok(())
    }
}

/// Wraps a store so that `GOMADA_CLIENT_SECRET`, when set, is used instead
/// of the stored secret.
pub struct EnvOverride<S: Store>(pub S);

impl<S: Store> Store for EnvOverride<S> {
    fn get(&self, profile: &str) -> Result<String> {
        match env(ENV_VAR) {
            Some(s) => Ok(s),
            None => self.0.get(profile),
        }
    }

    fn set(&self, profile: &str, secret: &str) -> Result<()> {
        self.0.set(profile, secret)
    }

    fn delete(&self, profile: &str) -> Result<()> {
        self.0.delete(profile)
    }
}

/// Returns the passphrase of an encrypted secret file.
pub type PassphraseFn = Box<dyn Fn() -> Result<String> + Send + Sync>;

/// scrypt cost parameters for new files. They are stored in each file, so
/// they can be raised later without breaking existing files.
const SCRYPT_LOG_N: u8 = 15;
const SCRYPT_R: u32 = 8;
const SCRYPT_P: u32 = 1;
const KEY_LEN: usize = 32; // AES-256
const NONCE_LEN: usize = 12;

/// The most work a secret file may ask for, so a damaged or hostile file
/// cannot make gomada use gigabytes of memory.
const MAX_SCRYPT_LOG_N: u8 = 20;

/// Stores each secret in its own file, encrypted with a key derived from a
/// passphrase (scrypt, then AES-256-GCM). It is the fallback for systems
/// without a keychain, such as servers reached over SSH.
pub struct File {
    /// The directory holding the secret files.
    pub dir: PathBuf,
    /// `None` makes get and set fail with [`Error::PassphraseRequired`].
    pub passphrase: Option<PassphraseFn>,
}

mod b64 {
    use super::*;
    use serde::{Deserializer, Serializer};

    pub fn serialize<S: Serializer>(bytes: &[u8], s: S) -> std::result::Result<S::Ok, S::Error> {
        s.serialize_str(&B64.encode(bytes))
    }

    pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> std::result::Result<Vec<u8>, D::Error> {
        let text = Option::<String>::deserialize(d)?.unwrap_or_default();
        B64.decode(text).map_err(serde::de::Error::custom)
    }
}

/// The on-disk format, shared with the Go version.
#[derive(Serialize, Deserialize)]
struct Sealed {
    version: u32,
    kdf: String,
    n: u64,
    r: u32,
    p: u32,
    #[serde(with = "b64")]
    salt: Vec<u8>,
    #[serde(with = "b64")]
    nonce: Vec<u8>,
    #[serde(with = "b64")]
    data: Vec<u8>,
}

impl Sealed {
    fn cipher(&self, passphrase: &str) -> Result<Aes256Gcm> {
        let bad = || Error::other("derive key: the secret file has unusable scrypt parameters");
        if !self.n.is_power_of_two() || self.n < 2 {
            return Err(bad());
        }
        let log_n = self.n.trailing_zeros() as u8;
        if log_n > MAX_SCRYPT_LOG_N {
            return Err(bad());
        }
        let params = scrypt::Params::new(log_n, self.r, self.p, KEY_LEN).map_err(|_| bad())?;
        let mut key = [0u8; KEY_LEN];
        scrypt::scrypt(passphrase.as_bytes(), &self.salt, &params, &mut key).map_err(|_| bad())?;
        Ok(Aes256Gcm::new(&key.into()))
    }
}

fn random(buf: &mut [u8]) -> Result<()> {
    getrandom::fill(buf).map_err(|e| Error::other(format!("no random numbers: {e}")))
}

impl File {
    fn path(&self, profile: &str) -> PathBuf {
        self.dir.join(format!("{profile}.secret.json"))
    }

    fn ask(&self) -> Result<String> {
        let ask = self.passphrase.as_ref().ok_or(Error::PassphraseRequired)?;
        let p = ask()?;
        if p.is_empty() {
            return Err(Error::other("the passphrase must not be empty"));
        }
        Ok(p)
    }
}

impl Store for File {
    fn get(&self, profile: &str) -> Result<String> {
        let path = self.path(profile);
        let raw = match std::fs::read(&path) {
            Ok(r) => r,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Err(Error::SecretNotFound),
            Err(e) => return Err(Error::other(format!("read secret file: {e}"))),
        };
        let sealed = serde_json::from_slice::<Sealed>(&raw)
            .ok()
            .filter(|s| s.kdf == "scrypt")
            .ok_or_else(|| Error::other(format!("{} is not a gomada secret file", path.display())))?;
        let pass = self.ask()?;
        let cipher = sealed.cipher(&pass)?;
        if sealed.nonce.len() != NONCE_LEN {
            return Err(Error::WrongPassphrase);
        }
        // The profile name is authenticated, so a file cannot be moved to
        // another profile.
        let plain = cipher
            .decrypt(Nonce::from_slice(&sealed.nonce), Payload { msg: &sealed.data, aad: profile.as_bytes() })
            .map_err(|_| Error::WrongPassphrase)?;
        String::from_utf8(plain).map_err(|_| Error::WrongPassphrase)
    }

    fn set(&self, profile: &str, secret: &str) -> Result<()> {
        let pass = self.ask()?;
        let mut sealed = Sealed {
            version: 1,
            kdf: "scrypt".into(),
            n: 1 << SCRYPT_LOG_N,
            r: SCRYPT_R,
            p: SCRYPT_P,
            salt: vec![0; 16],
            nonce: vec![0; NONCE_LEN],
            data: Vec::new(),
        };
        random(&mut sealed.salt)?;
        random(&mut sealed.nonce)?;
        sealed.data = sealed
            .cipher(&pass)?
            .encrypt(Nonce::from_slice(&sealed.nonce), Payload { msg: secret.as_bytes(), aad: profile.as_bytes() })
            .map_err(|_| Error::other("encrypt the secret"))?;
        let raw = serde_json::to_vec(&sealed).map_err(|e| Error::other(e.to_string()))?;
        write_file_atomic(&self.path(profile), &raw).map_err(|e| Error::other(format!("write secret file: {e}")))
    }

    fn delete(&self, profile: &str) -> Result<()> {
        match std::fs::remove_file(self.path(profile)) {
            Err(e) if e.kind() != std::io::ErrorKind::NotFound => Err(Error::other(format!("delete secret file: {e}"))),
            _ => Ok(()),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn pass(p: &'static str) -> Option<PassphraseFn> {
        Some(Box::new(move || Ok(p.to_string())))
    }

    /// Checks the behavior every store must have.
    fn store_contract(s: &dyn Store) {
        assert_eq!(s.get("home"), Err(Error::SecretNotFound));
        s.delete("home").unwrap();
        s.set("home", "s3cr3t").unwrap();
        s.set("office", "other").unwrap();
        s.set("home", "replaced").unwrap();
        assert_eq!(s.get("home").unwrap(), "replaced");
        assert_eq!(s.get("office").unwrap(), "other");
        s.delete("home").unwrap();
        assert_eq!(s.get("home"), Err(Error::SecretNotFound));
        assert_eq!(s.get("office").unwrap(), "other", "deleting one profile must not touch another");
    }

    #[test]
    fn keyring_contract() {
        store_contract(&Keyring(Arc::new(MemoryKeychain::default())));
    }

    #[test]
    fn keyring_unavailable_is_not_not_found() {
        let chain = MemoryKeychain::default();
        chain.unavailable.store(true, std::sync::atomic::Ordering::SeqCst);
        let k = Keyring(Arc::new(chain));
        assert!(k.set("home", "x").is_err());
        let err = k.get("home").unwrap_err();
        assert!(!err.is_secret_not_found(), "{err}");
    }

    #[test]
    fn file_contract() {
        let dir = tempfile::tempdir().unwrap();
        store_contract(&File { dir: dir.path().into(), passphrase: pass("correct horse") });
    }

    #[test]
    fn file_is_encrypted_and_private() {
        let dir = tempfile::tempdir().unwrap();
        let f = File { dir: dir.path().join("secrets"), passphrase: pass("pw-pw-pw") };
        f.set("home", "very-secret-value").unwrap();
        let path = dir.path().join("secrets").join("home.secret.json");
        let raw = std::fs::read_to_string(&path).unwrap();
        assert!(!raw.contains("very-secret-value") && !raw.contains("pw-pw-pw"), "{raw}");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(std::fs::metadata(&path).unwrap().permissions().mode() & 0o777, 0o600);
        }
    }

    #[test]
    fn file_wrong_passphrase_and_tampering() {
        let dir = tempfile::tempdir().unwrap();
        let f = File { dir: dir.path().into(), passphrase: pass("right") };
        f.set("home", "s").unwrap();
        let wrong = File { dir: dir.path().into(), passphrase: pass("wrong") };
        assert_eq!(wrong.get("home"), Err(Error::WrongPassphrase));

        let path = dir.path().join("home.secret.json");
        let raw = std::fs::read_to_string(&path).unwrap();
        // Moved to another profile.
        std::fs::write(dir.path().join("office.secret.json"), &raw).unwrap();
        assert_eq!(f.get("office"), Err(Error::WrongPassphrase));
        // Flipped ciphertext.
        let mut v: serde_json::Value = serde_json::from_str(&raw).unwrap();
        let data = v["data"].as_str().unwrap().to_string();
        v["data"] = format!("AAAA{}", &data[4..]).into();
        std::fs::write(&path, v.to_string()).unwrap();
        assert_eq!(f.get("home"), Err(Error::WrongPassphrase));
        // Absurd cost parameters are refused before any work is done.
        v["n"] = (1u64 << 40).into();
        std::fs::write(&path, v.to_string()).unwrap();
        assert!(f.get("home").unwrap_err().to_string().contains("scrypt parameters"));
        // Not a secret file.
        std::fs::write(&path, "hello").unwrap();
        let err = f.get("home").unwrap_err();
        assert!(err.to_string().contains("is not a gomada secret file"), "{err}");
    }

    #[test]
    fn file_passphrase_handling() {
        let dir = tempfile::tempdir().unwrap();
        let none = File { dir: dir.path().into(), passphrase: None };
        assert_eq!(none.set("home", "s"), Err(Error::PassphraseRequired));
        assert!(File { dir: dir.path().into(), passphrase: pass("") }.set("home", "s").is_err());
        File { dir: dir.path().into(), passphrase: pass("pw") }.set("home", "s").unwrap();
        assert_eq!(none.get("home"), Err(Error::PassphraseRequired));
        // A missing secret is reported without asking for the passphrase.
        let panics = File { dir: dir.path().into(), passphrase: Some(Box::new(|| panic!("asked"))) };
        assert_eq!(panics.get("nope"), Err(Error::SecretNotFound));
    }

    #[test]
    fn reads_a_secret_file_written_by_the_go_version() {
        // Written by the Go implementation (scrypt N=32768 r=8 p=1,
        // AES-256-GCM, the profile name as additional data).
        let go = include_str!("../tests/testdata/go-home.secret.json");
        let dir = tempfile::tempdir().unwrap();
        std::fs::write(dir.path().join("home.secret.json"), go).unwrap();
        let f = File { dir: dir.path().into(), passphrase: pass("correct horse") };
        assert_eq!(f.get("home").unwrap(), "fake-client-secret");
    }

    #[test]
    fn go_keyring_wrapping_is_undone() {
        assert_eq!(decode_go_keyring(b"plain".to_vec()).unwrap(), "plain");
        assert_eq!(decode_go_keyring(b"go-keyring-base64:czNjcjN0".to_vec()).unwrap(), "s3cr3t");
        assert_eq!(decode_go_keyring(b"go-keyring-encoded:733363723374".to_vec()).unwrap(), "s3cr3t");
        assert!(decode_go_keyring(b"go-keyring-base64:!!".to_vec()).is_err());
    }
}
