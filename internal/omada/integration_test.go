//go:build integration

package omada_test

import (
	"context"
	"os"
	"testing"

	"github.com/mareklanduch/gomada/internal/omada"
)

// TestLiveController runs the read-only calls against a real controller. It
// is not part of the normal test run:
//
//	GOMADA_TEST_URL=https://192.168.0.2:8043 \
//	GOMADA_TEST_CLIENT_ID=... GOMADA_TEST_CLIENT_SECRET=... \
//	go test -tags integration -run TestLiveController ./internal/omada/
//
// Set GOMADA_TEST_TLS_PIN to the certificate fingerprint for a controller
// with a self-signed certificate. Nothing on the controller is changed.
func TestLiveController(t *testing.T) {
	url, id, secret := os.Getenv("GOMADA_TEST_URL"), os.Getenv("GOMADA_TEST_CLIENT_ID"), os.Getenv("GOMADA_TEST_CLIENT_SECRET")
	if url == "" || id == "" || secret == "" {
		t.Skip("set GOMADA_TEST_URL, GOMADA_TEST_CLIENT_ID, and GOMADA_TEST_CLIENT_SECRET to run this test")
	}
	ctx := t.Context()
	hc := omada.NewHTTPClient(omada.TLSOptions{Pin: os.Getenv("GOMADA_TEST_TLS_PIN")}, 0)

	info, err := omada.FetchInfo(ctx, hc, url)
	if err != nil {
		t.Fatalf("info: %v", err)
	}
	t.Logf("controller %s, version %s", info.ControllerID, info.Version)

	c := omada.New(omada.Options{
		BaseURL: url, ControllerID: info.ControllerID, ClientID: id, HTTPClient: hc,
		Secret: func(context.Context) (string, error) { return secret, nil },
	})
	sites, err := c.Sites(ctx)
	if err != nil {
		t.Fatalf("sites: %v", err)
	}
	for _, site := range sites {
		devs, err := c.Devices(ctx, site.ID)
		if err != nil {
			t.Errorf("devices of %s: %v", site.Name, err)
		}
		clients, err := c.Clients(ctx, site.ID)
		if err != nil {
			t.Errorf("clients of %s: %v", site.Name, err)
		}
		t.Logf("site %s: %d devices, %d clients", site.Name, len(devs), len(clients))
	}
}
