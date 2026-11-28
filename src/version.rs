//! Build information. The release build sets `GOMADA_VERSION`,
//! `GOMADA_COMMIT`, and `GOMADA_BUILD_DATE` in the environment of the
//! compiler; the version of a release comes from its tag, not from
//! Cargo.toml.

pub const VERSION: &str = match option_env!("GOMADA_VERSION") {
    Some(v) => v,
    None => "dev",
};

pub const COMMIT: &str = match option_env!("GOMADA_COMMIT") {
    Some(c) => c,
    None => "none",
};

pub const DATE: &str = match option_env!("GOMADA_BUILD_DATE") {
    Some(d) => d,
    None => "unknown",
};

/// What follows the program name in the one-line version text.
pub fn details() -> String {
    format!("{VERSION} (commit {COMMIT}, built {DATE})")
}

/// The one-line version text.
pub fn string() -> String {
    format!("gomada {}", details())
}

#[cfg(test)]
mod tests {
    #[test]
    fn version_line() {
        let s = super::string();
        assert!(s.starts_with("gomada "), "{s}");
        assert!(s.contains("commit") && s.contains("built"), "{s}");
    }
}
