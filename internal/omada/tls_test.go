package omada_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/omada/omadatest"
)

func TestProbeTLSSelfSigned(t *testing.T) {
	fake := omadatest.NewTLS(t)
	fp, trusted, err := omada.ProbeTLS(t.Context(), fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	if trusted {
		t.Error("the test certificate must not be trusted by the system roots")
	}
	if want := omada.Fingerprint(fake.Certificate()); fp != want || !strings.HasPrefix(fp, "sha256:") {
		t.Errorf("fingerprint = %q, want %q", fp, want)
	}
}

func TestProbeTLSPlainHTTP(t *testing.T) {
	fake := omadatest.New(t)
	fp, trusted, err := omada.ProbeTLS(t.Context(), fake.URL)
	if err != nil || !trusted || fp != "" {
		t.Errorf("got (%q, %v, %v), want trusted with no fingerprint", fp, trusted, err)
	}
}

func TestProbeTLSUnreachable(t *testing.T) {
	fake := omadatest.NewTLS(t)
	url := fake.URL
	fake.Close()
	if _, _, err := omada.ProbeTLS(t.Context(), url); !errors.Is(err, omada.ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
}

func TestUntrustedCertificateIsRefused(t *testing.T) {
	fake := omadatest.NewTLS(t)
	hc := omada.NewHTTPClient(omada.TLSOptions{}, 0)
	_, err := omada.FetchInfo(t.Context(), hc, fake.URL)
	if !errors.Is(err, omada.ErrCertificate) {
		t.Fatalf("err = %v, want ErrCertificate", err)
	}
}

func TestPinMatch(t *testing.T) {
	fake := omadatest.NewTLS(t)
	pin := omada.Fingerprint(fake.Certificate())
	hc := omada.NewHTTPClient(omada.TLSOptions{Pin: pin}, 0)
	if _, err := omada.FetchInfo(t.Context(), hc, fake.URL); err != nil {
		t.Fatal(err)
	}
}

func TestPinMismatch(t *testing.T) {
	fake := omadatest.NewTLS(t)
	hc := omada.NewHTTPClient(omada.TLSOptions{Pin: "sha256:0000"}, 0)
	_, err := omada.FetchInfo(t.Context(), hc, fake.URL)
	if !errors.Is(err, omada.ErrCertificate) {
		t.Fatalf("err = %v, want ErrCertificate", err)
	}
	var mismatch *omada.PinMismatchError
	if !errors.As(err, &mismatch) || mismatch.Got != omada.Fingerprint(fake.Certificate()) {
		t.Errorf("err = %v, want a PinMismatchError carrying the presented fingerprint", err)
	}
}

func TestInsecureAcceptsAnyCertificate(t *testing.T) {
	fake := omadatest.NewTLS(t)
	hc := omada.NewHTTPClient(omada.TLSOptions{Insecure: true}, 0)
	if _, err := omada.FetchInfo(t.Context(), hc, fake.URL); err != nil {
		t.Fatal(err)
	}
}
