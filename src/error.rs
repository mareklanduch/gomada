//! The one error type of the application. Front ends ask it which class an
//! error belongs to (authentication, unreachable, wrong usage, ...) to pick
//! an exit code or a next step; the classes survive added context.

use std::fmt;

/// An error answer from the controller.
#[derive(Debug, Clone, PartialEq)]
pub struct ApiError {
    /// `errorCode` from the response body, 0 if there was none.
    pub code: i64,
    /// `msg` from the response body, or the HTTP status text.
    pub message: String,
    pub http_status: u16,
}

impl fmt::Display for ApiError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.code != 0 {
            write!(f, "{} (code {})", self.message, self.code)
        } else {
            write!(f, "{} (HTTP {})", self.message, self.http_status)
        }
    }
}

#[derive(Debug, Clone, PartialEq)]
pub enum Error {
    /// The controller refused the client ID and secret.
    Auth(String),
    /// The controller could not be contacted at all.
    Unreachable(String),
    /// The controller's TLS certificate was not accepted.
    Certificate(String),
    /// The controller presented a certificate other than the pinned one.
    PinMismatch { got: String },
    /// An error answer from the controller.
    Api(ApiError),
    /// The command line was used wrongly (exit code 2).
    Usage(String),
    /// No host has been added yet.
    NoHosts,
    /// This system has no usable keychain.
    KeyringUnavailable(String),
    /// The certificate is not trusted and no pin was given.
    UntrustedCertificate(Box<Error>),
    /// The host has several sites and none was chosen.
    SiteRequired { sites: String },
    /// A Wi-Fi network cannot be joined by QR code.
    NoQr(String),
    /// A device has no newer firmware to install.
    UpToDate { label: String, current: String },
    /// No secret is stored for the host.
    SecretNotFound,
    /// The encrypted file store needs a passphrase and cannot ask for one.
    PassphraseRequired,
    /// The passphrase did not decrypt the secret file.
    WrongPassphrase,
    /// A person answered "no" to a confirmation.
    Canceled,
    /// A wait ran out of time.
    DeadlineExceeded,
    /// Another error with words in front that say what was being done.
    Context(String, Box<Error>),
    /// Anything else.
    Other(String),
}

pub type Result<T> = std::result::Result<T, Error>;

impl Error {
    pub fn other(msg: impl Into<String>) -> Error {
        Error::Other(msg.into())
    }

    pub fn usage(msg: impl Into<String>) -> Error {
        Error::Usage(msg.into())
    }

    /// Puts words in front of the error; its class stays the same.
    pub fn context(self, what: impl Into<String>) -> Error {
        Error::Context(what.into(), Box::new(self))
    }

    /// Reports whether this error, or one it wraps, satisfies `pred`.
    fn any(&self, pred: &dyn Fn(&Error) -> bool) -> bool {
        if pred(self) {
            return true;
        }
        match self {
            Error::Context(_, inner) | Error::UntrustedCertificate(inner) => inner.any(pred),
            _ => false,
        }
    }

    pub fn is_auth(&self) -> bool {
        self.any(&|e| matches!(e, Error::Auth(_)))
    }

    pub fn is_unreachable(&self) -> bool {
        self.any(&|e| matches!(e, Error::Unreachable(_)))
    }

    pub fn is_certificate(&self) -> bool {
        self.any(&|e| matches!(e, Error::Certificate(_) | Error::PinMismatch { .. }))
    }

    pub fn is_pin_mismatch(&self) -> bool {
        self.any(&|e| matches!(e, Error::PinMismatch { .. }))
    }

    pub fn is_usage(&self) -> bool {
        self.any(&|e| matches!(e, Error::Usage(_)))
    }

    pub fn is_keyring_unavailable(&self) -> bool {
        self.any(&|e| matches!(e, Error::KeyringUnavailable(_)))
    }

    pub fn is_site_required(&self) -> bool {
        self.any(&|e| matches!(e, Error::SiteRequired { .. }))
    }

    pub fn is_up_to_date(&self) -> bool {
        self.any(&|e| matches!(e, Error::UpToDate { .. }))
    }

    pub fn is_secret_not_found(&self) -> bool {
        self.any(&|e| matches!(e, Error::SecretNotFound))
    }

    pub fn is_passphrase_required(&self) -> bool {
        self.any(&|e| matches!(e, Error::PassphraseRequired))
    }

    pub fn is_wrong_passphrase(&self) -> bool {
        self.any(&|e| matches!(e, Error::WrongPassphrase))
    }

    pub fn is_deadline_exceeded(&self) -> bool {
        self.any(&|e| matches!(e, Error::DeadlineExceeded))
    }

    /// The controller's answer, when the error is one.
    pub fn api(&self) -> Option<&ApiError> {
        match self {
            Error::Api(a) => Some(a),
            Error::Context(_, inner) | Error::UntrustedCertificate(inner) => inner.api(),
            _ => None,
        }
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Auth(m) => write!(f, "authentication failed: {m}"),
            Error::Unreachable(m) => write!(f, "controller unreachable: {m}"),
            Error::Certificate(m) => write!(f, "certificate not trusted: {m}"),
            Error::PinMismatch { got } => {
                write!(f, "certificate not trusted: the controller's certificate changed; it now presents {got}")
            }
            Error::Api(a) => a.fmt(f),
            Error::Usage(m) | Error::Other(m) => f.write_str(m),
            Error::NoHosts => f.write_str("no host is set up yet; add one with: gomada host add"),
            Error::KeyringUnavailable(m) => write!(f, "no system keychain is available: {m}"),
            Error::UntrustedCertificate(inner) => {
                write!(f, "the controller's certificate is not trusted: {inner}")
            }
            Error::SiteRequired { sites } => {
                write!(f, "this host has several sites; choose one with --site (sites: {sites})")
            }
            Error::NoQr(m) => write!(f, "no QR code for this network: {m}"),
            Error::UpToDate { label, current } => {
                write!(f, "{label} is already up to date (firmware {current})")
            }
            Error::SecretNotFound => f.write_str("no client secret stored for this host"),
            Error::PassphraseRequired => f.write_str("a passphrase is required to unlock the client secret"),
            Error::WrongPassphrase => f.write_str("wrong passphrase, or the secret file is damaged"),
            Error::Canceled => f.write_str("canceled"),
            Error::DeadlineExceeded => f.write_str("the time limit was reached"),
            Error::Context(what, inner) => write!(f, "{what}: {inner}"),
        }
    }
}

impl std::error::Error for Error {}

impl From<std::io::Error> for Error {
    fn from(e: std::io::Error) -> Error {
        Error::Other(e.to_string())
    }
}
