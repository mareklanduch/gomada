//! Keeps Open API access tokens between runs, so a command can skip the
//! login while its host's token is still valid.
//!
//! Tokens go to the operating system keychain where there is one, and
//! otherwise to a file in the per-user runtime directory, which lives in
//! memory and is wiped at logout. They are never written to the config file.

use std::path::PathBuf;
use std::sync::Arc;

use serde::{Deserialize, Serialize};

use crate::config::write_file_atomic;
use crate::omada::{Token, TokenCache};
use crate::secret::Keychain;

/// What is stored for one profile. The client ID is kept with the token so
/// a token is never used after the profile's credentials changed.
#[derive(Serialize, Deserialize)]
struct Entry {
    #[serde(flatten)]
    token: Token,
    client_id: String,
}

/// Where the entries are kept.
pub enum Store {
    /// The operating system keychain, next to the client secret of the same
    /// profile.
    Keyring(Arc<dyn Keychain>),
    /// Owner-only files in a directory.
    File(PathBuf),
}

fn account(profile: &str) -> String {
    format!("{profile}:token")
}

impl Store {
    fn path(dir: &std::path::Path, profile: &str) -> PathBuf {
        dir.join(format!("{profile}.token.json"))
    }

    fn get(&self, profile: &str) -> Option<String> {
        match self {
            Store::Keyring(chain) => chain.get(&account(profile)).ok().flatten(),
            Store::File(dir) => std::fs::read_to_string(Self::path(dir, profile)).ok(),
        }
    }

    fn set(&self, profile: &str, value: &str) {
        // A cache that cannot be written is not worth failing a command
        // for: the next run simply logs in again.
        match self {
            Store::Keyring(chain) => drop(chain.set(&account(profile), value)),
            Store::File(dir) => drop(write_file_atomic(&Self::path(dir, profile), value.as_bytes())),
        }
    }

    /// Removes the cached token of a profile.
    pub fn delete(&self, profile: &str) {
        match self {
            Store::Keyring(chain) => drop(chain.delete(&account(profile))),
            Store::File(dir) => drop(std::fs::remove_file(Self::path(dir, profile))),
        }
    }
}

/// Returns the gomada directory below the per-user runtime directory
/// (`$XDG_RUNTIME_DIR`, e.g. /run/user/1000), if the system has one.
pub fn runtime_dir() -> Option<PathBuf> {
    std::env::var_os("XDG_RUNTIME_DIR").filter(|v| !v.is_empty()).map(|base| PathBuf::from(base).join("gomada"))
}

/// The token cache of one profile, as the API client wants it.
pub struct Bound {
    pub store: Store,
    pub profile: String,
    pub client_id: String,
}

impl TokenCache for Bound {
    fn load(&self) -> Option<Token> {
        let raw = self.store.get(&self.profile)?;
        let entry: Entry = serde_json::from_str(&raw).ok()?;
        (entry.client_id == self.client_id && !entry.token.access_token.is_empty()).then_some(entry.token)
    }

    fn save(&self, token: &Token) {
        let entry = Entry { token: token.clone(), client_id: self.client_id.clone() };
        if let Ok(raw) = serde_json::to_string(&entry) {
            self.store.set(&self.profile, &raw);
        }
    }

    fn clear(&self) {
        self.store.delete(&self.profile);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::secret::MemoryKeychain;
    use chrono::{TimeZone, Utc};

    fn token() -> Token {
        Token { access_token: "AT-1".into(), expires_at: Utc.with_ymd_and_hms(2026, 11, 23, 14, 0, 0).unwrap() }
    }

    fn contract(make: &dyn Fn(&str) -> Bound) {
        let cache = make("id");
        assert_eq!(cache.load(), None);
        cache.save(&token());
        assert_eq!(cache.load(), Some(token()));
        // Other credentials never see the token.
        assert_eq!(make("other-id").load(), None);
        cache.clear();
        assert_eq!(cache.load(), None);
        cache.clear(); // clearing twice is fine
    }

    #[test]
    fn keyring_cache() {
        let chain = Arc::new(MemoryKeychain::default());
        contract(&|id| Bound { store: Store::Keyring(chain.clone()), profile: "home".into(), client_id: id.into() });
        // The token sits next to the secret, under its own account.
        Bound { store: Store::Keyring(chain.clone()), profile: "home".into(), client_id: "id".into() }.save(&token());
        assert!(chain.values.lock().unwrap().contains_key("home:token"));
    }

    #[test]
    fn file_cache() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("gomada");
        contract(&|id| Bound { store: Store::File(path.clone()), profile: "home".into(), client_id: id.into() });
    }

    #[test]
    fn reads_a_token_cached_by_the_go_version() {
        let chain = Arc::new(MemoryKeychain::default());
        chain
            .set(
                "home:token",
                r#"{"access_token":"AT-9","expires_at":"2026-11-23T15:00:00.123456789+01:00","client_id":"id"}"#,
            )
            .unwrap();
        let got =
            Bound { store: Store::Keyring(chain), profile: "home".into(), client_id: "id".into() }.load().unwrap();
        assert_eq!(got.access_token, "AT-9");
        assert_eq!(got.expires_at.timestamp(), Utc.with_ymd_and_hms(2026, 11, 23, 14, 0, 0).unwrap().timestamp());
    }

    #[test]
    fn damaged_entries_are_ignored() {
        let chain = Arc::new(MemoryKeychain::default());
        chain.set("home:token", "not json").unwrap();
        assert_eq!(Bound { store: Store::Keyring(chain), profile: "home".into(), client_id: "id".into() }.load(), None);
    }
}
