package omada_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/omada/omadatest"
)

func TestConnectedTo(t *testing.T) {
	tests := []struct {
		client omada.NetClient
		want   string
	}{
		{omada.NetClient{Wireless: true, APName: "AP office", SSID: "Home-5G"}, "AP office · Home-5G"},
		{omada.NetClient{Wireless: true, APName: "AP office"}, "AP office"},
		{omada.NetClient{Wireless: true, SSID: "Home-5G"}, "Home-5G"},
		{omada.NetClient{SwitchName: "Rack switch", Port: 4}, "Rack switch · port 4"},
		{omada.NetClient{SwitchName: "Rack switch"}, "Rack switch"},
		{omada.NetClient{GatewayName: "Core router", Port: 2}, "Core router · port 2"},
		{omada.NetClient{Port: 3}, "port 3"},
		{omada.NetClient{}, ""},
		// A wireless client must never show a switch port.
		{omada.NetClient{Wireless: true, APName: "AP", SwitchName: "Rack switch", Port: 9}, "AP"},
	}
	for _, tt := range tests {
		if got := tt.client.ConnectedTo(); got != tt.want {
			t.Errorf("ConnectedTo(%+v) = %q, want %q", tt.client, got, tt.want)
		}
	}
}

func TestDeviceLabelsAndStatus(t *testing.T) {
	if got := (omada.Device{MAC: "AA"}).Label(); got != "AA" {
		t.Errorf("label without a name = %q, want the MAC address", got)
	}
	if got := (omada.NetClient{MAC: "CC"}).Label(); got != "CC" {
		t.Errorf("client label without names = %q, want the MAC address", got)
	}
	want := map[int]string{0: "offline", 1: "connected", 2: "pending", 3: "heartbeat missed", 4: "isolated", 9: "status 9"}
	for status, text := range want {
		if got := (omada.Device{Status: status}).StatusText(); got != text {
			t.Errorf("StatusText(%d) = %q, want %q", status, got, text)
		}
	}
}

func TestLogRequestsNeverLogsSecrets(t *testing.T) {
	fake := omadatest.New(t)
	var buf bytes.Buffer
	hc := fake.Client()
	omada.LogRequests(hc, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	c := newClient(fake, func(o *omada.Options) { o.HTTPClient = hc })
	if _, err := c.Sites(t.Context()); err != nil {
		t.Fatal(err)
	}
	log := buf.String()
	for _, want := range []string{"method=POST", "/openapi/authorize/token", "method=GET", "/sites", "status=200"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q:\n%s", want, log)
		}
	}
	for _, leak := range []string{omadatest.Secret, "AT-1", "grant_type", "pageSize"} {
		if strings.Contains(log, leak) {
			t.Errorf("the log contains %q:\n%s", leak, log)
		}
	}

	fake.Close()
	buf.Reset()
	if _, err := c.Devices(t.Context(), "site-1"); err == nil {
		t.Fatal("want an error from a closed controller")
	}
	if !strings.Contains(buf.String(), "request failed") {
		t.Errorf("a failed request must be logged:\n%s", buf.String())
	}
}
