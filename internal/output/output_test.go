package output_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/output"
)

func TestDuration(t *testing.T) {
	tests := map[time.Duration]string{
		0:                               "-",
		-time.Second:                    "-",
		42 * time.Second:                "42s",
		5*time.Minute + 3*time.Second:   "5m",
		3*time.Hour + 12*time.Minute:    "3h 12m",
		41*24*time.Hour + 6*time.Hour:   "41d 6h",
		24 * time.Hour:                  "1d 0h",
		time.Hour:                       "1h 0m",
		500 * time.Millisecond:          "0s",
		59*time.Minute + 59*time.Second: "59m",
	}
	for in, want := range tests {
		if got := output.Duration(in); got != want {
			t.Errorf("Duration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestUptime(t *testing.T) {
	tests := map[string]string{
		"":                  "-",
		"  ":                "-",
		"3600":              "1h 0m",
		"1044000":           "12d 2h",
		"41day(s) 6h 2m 1s": "41d 6h",
		"6h 2m 1s":          "6h 2m",
		"2m":                "2m",
	}
	for in, want := range tests {
		if got := output.Uptime(in); got != want {
			t.Errorf("Uptime(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBytes(t *testing.T) {
	tests := map[float64]string{
		0:            "0 B",
		-5:           "0 B",
		512:          "512 B",
		1024:         "1.0 KB",
		1536:         "1.5 KB",
		4194304:      "4.0 MB",
		1288490188:   "1.2 GB",
		94489280512:  "88.0 GB",
		107374182400: "100 GB",
	}
	for in, want := range tests {
		if got := output.Bytes(in); got != want {
			t.Errorf("Bytes(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestSignal(t *testing.T) {
	if got := output.Signal(omada.NetClient{Wireless: true, RSSI: -52}); got != "-52 dBm" {
		t.Errorf("wireless: %q", got)
	}
	if got := output.Signal(omada.NetClient{}); got != "wired" {
		t.Errorf("wired: %q", got)
	}
	if got := output.Signal(omada.NetClient{Wireless: true}); got != "-" {
		t.Errorf("unknown: %q", got)
	}
}

func TestDeviceFields(t *testing.T) {
	online := output.DeviceFields(omada.Device{Name: "AP", Status: omada.StatusConnected, Uptime: "3600", CPU: 5})
	offline := output.DeviceFields(omada.Device{Name: "AP", Status: omada.StatusDisconnected})
	if value(online, "Uptime") != "1h 0m" || value(online, "CPU") != "5%" {
		t.Errorf("online = %+v", online)
	}
	if value(offline, "Uptime") != "" || value(offline, "Status") != "offline" || value(offline, "Model") != "-" {
		t.Errorf("offline = %+v; want no load fields and placeholders for missing values", offline)
	}
}

func TestClientFields(t *testing.T) {
	c := omada.NetClient{Name: "NAS", SwitchName: "Rack switch", Port: 4, TrafficDown: 1024, Uptime: 60, NetworkName: "LAN"}
	f := output.ClientFields(c)
	if value(f, "Connected to") != "Rack switch · port 4" || value(f, "Signal") != "wired" ||
		value(f, "Downloaded") != "1.0 KB" || value(f, "Uptime") != "1m" || value(f, "Network") != "LAN" {
		t.Errorf("fields = %+v", f)
	}
}

func value(fields []output.Field, label string) string {
	for _, f := range fields {
		if f.Label == label {
			return f.Value
		}
	}
	return ""
}

func TestTable(t *testing.T) {
	var buf bytes.Buffer
	err := output.Table(&buf, []string{"Name", "ip"}, [][]string{{"Core router", "192.168.0.1"}, {"AP", "192.168.0.21"}})
	if err != nil {
		t.Fatal(err)
	}
	want := "NAME         IP\nCore router  192.168.0.1\nAP           192.168.0.21\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestFields(t *testing.T) {
	var buf bytes.Buffer
	if err := output.Fields(&buf, []output.Field{{"Name", "AP"}, {"IP address", "10.0.0.2"}}); err != nil {
		t.Fatal(err)
	}
	want := "Name:        AP\nIP address:  10.0.0.2\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := output.JSON(&buf, map[string]string{"name": "A & B <x>"}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); !strings.Contains(got, `"name": "A & B <x>"`) || !strings.HasSuffix(got, "}\n") {
		t.Errorf("got %q", got)
	}
}

func TestParseFormat(t *testing.T) {
	for _, ok := range []string{"table", "json", "JSON"} {
		if _, err := output.ParseFormat(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	if _, err := output.ParseFormat("yaml"); err == nil {
		t.Error("yaml: want an error")
	}
}

func TestMatches(t *testing.T) {
	cells := []string{"AP office", "192.168.0.21", "ap"}
	for filter, want := range map[string]bool{"": true, "OFFICE": true, "0.21": true, "switch": false} {
		if got := output.Matches(cells, filter); got != want {
			t.Errorf("Matches(%q) = %v, want %v", filter, got, want)
		}
	}
}
