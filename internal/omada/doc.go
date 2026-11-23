// Package omada is a client for the TP-Link Omada Open API.
//
// It knows nothing about configuration files, keychains, or terminals: callers
// hand it a base URL, the application credentials, and an HTTP client. The
// client logs in on demand, renews its access token before it expires, and
// retries once when the controller rejects a token.
package omada
