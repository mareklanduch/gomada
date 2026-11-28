//! gomada manages TP-Link Omada controllers from the terminal.
//!
//! One core ([`app`]) serves two front ends: the command line ([`cli`]) and
//! the interactive view ([`tui`]). Everything below them is plumbing:
//! [`omada`] speaks the controller's Open API, [`config`] keeps the list of
//! hosts, [`secret`] and [`tokencache`] keep credentials out of that list.

pub mod app;
pub mod cli;
pub mod config;
pub mod error;
pub mod omada;
pub mod output;
pub mod qr;
pub mod secret;
pub mod tokencache;
pub mod tui;
pub mod version;
