package omada

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// TLSOptions controls how the controller's certificate is checked.
type TLSOptions struct {
	// Pin is the fingerprint ("sha256:<hex>") of the only certificate to
	// accept. Empty means normal verification against the system roots.
	Pin string
	// Insecure accepts any certificate. It exists for emergencies only.
	Insecure bool
}

// DefaultTimeout bounds every request made with a client from NewHTTPClient.
const DefaultTimeout = 15 * time.Second

// NewHTTPClient returns an HTTP client that checks the controller's
// certificate according to opts. A timeout of zero means DefaultTimeout.
func NewHTTPClient(opts TLSOptions, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	switch {
	case opts.Insecure:
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit user opt-in
	case opts.Pin != "":
		pin := opts.Pin
		tr.TLSClientConfig = &tls.Config{
			// Chain verification is replaced by the pin check below.
			InsecureSkipVerify: true, //nolint:gosec // see VerifyConnection
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return &PinMismatchError{Got: "no certificate"}
				}
				if got := Fingerprint(cs.PeerCertificates[0]); got != pin {
					return &PinMismatchError{Got: got}
				}
				return nil
			},
		}
	}
	return &http.Client{Transport: tr, Timeout: timeout}
}

// Fingerprint returns the SHA-256 fingerprint of a certificate in the form
// stored in the configuration: "sha256:<hex>".
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ProbeTLS connects to baseURL and reports the fingerprint of the certificate
// it presents and whether that certificate is trusted by the system roots.
// For plain http URLs it reports trusted with an empty fingerprint.
func ProbeTLS(ctx context.Context, baseURL string) (fingerprint string, trusted bool, err error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", false, fmt.Errorf("invalid URL %q: %w", baseURL, err)
	}
	if u.Scheme != "https" {
		return "", true, nil
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "443"
	}
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: DefaultTimeout},
		Config:    &tls.Config{InsecureSkipVerify: true, ServerName: host}, //nolint:gosec // verified by hand below
	}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return "", false, classify(ctx, err)
	}
	defer conn.Close() //nolint:errcheck // nothing to do about a failed close

	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return "", false, fmt.Errorf("%w: the controller presented no certificate", ErrCertificate)
	}
	intermediates := x509.NewCertPool()
	for _, c := range certs[1:] {
		intermediates.AddCert(c)
	}
	_, verifyErr := certs[0].Verify(x509.VerifyOptions{DNSName: host, Intermediates: intermediates})
	return Fingerprint(certs[0]), verifyErr == nil, nil
}
