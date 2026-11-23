package omada

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
)

// Error classes. Use errors.Is to test for them.
var (
	// ErrAuth means the controller refused the client ID and secret.
	ErrAuth = errors.New("authentication failed")
	// ErrUnreachable means the controller could not be contacted at all.
	ErrUnreachable = errors.New("controller unreachable")
	// ErrCertificate means the controller's TLS certificate was not accepted.
	ErrCertificate = errors.New("certificate not trusted")
)

// APIError is an error answer from the controller.
type APIError struct {
	Code       int    // errorCode from the response body, 0 if there was none
	Message    string // msg from the response body, or the HTTP status text
	HTTPStatus int
}

func (e *APIError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("%s (code %d)", e.Message, e.Code)
	}
	return fmt.Sprintf("%s (HTTP %d)", e.Message, e.HTTPStatus)
}

// PinMismatchError means the controller presented a certificate other than
// the pinned one.
type PinMismatchError struct {
	Got string // fingerprint the controller presented
}

func (e *PinMismatchError) Error() string {
	return "the controller's certificate changed; it now presents " + e.Got
}

// Error codes the controller answers with when an access token is expired or
// invalid.
const (
	codeTokenExpired = -44112
	codeTokenInvalid = -44113
)

func tokenRejected(err error) bool {
	var a *APIError
	if !errors.As(err, &a) {
		return false
	}
	return a.Code == codeTokenExpired || a.Code == codeTokenInvalid || a.HTTPStatus == 401
}

// classify turns a transport error into one of the error classes.
func classify(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var (
		pin  *PinMismatchError
		cert *tls.CertificateVerificationError
		ua   x509.UnknownAuthorityError
		hn   x509.HostnameError
		ci   x509.CertificateInvalidError
	)
	switch {
	case errors.As(err, &pin):
		return fmt.Errorf("%w: %w", ErrCertificate, pin)
	case errors.As(err, &cert), errors.As(err, &ua), errors.As(err, &hn), errors.As(err, &ci):
		return fmt.Errorf("%w: %w", ErrCertificate, err)
	}
	return fmt.Errorf("%w: %w", ErrUnreachable, err)
}
