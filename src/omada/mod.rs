//! A client for the Omada Open API of one controller: logging in with
//! client credentials, keeping the access token fresh, paging, and the
//! calls gomada needs.

mod client;
mod http;
mod types;

pub use client::{fetch_info, Client, Clock, Options, SecretFn, ALERTS_PER_STATE};
pub use http::{fingerprint, probe_tls, Http, Logger, TlsOptions, DEFAULT_TIMEOUT};
pub use types::*;
