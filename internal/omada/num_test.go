package omada_test

import (
	"encoding/json"
	"testing"

	"github.com/mareklanduch/gomada/internal/omada"
)

func TestNumUnmarshal(t *testing.T) {
	tests := []struct {
		in      string
		want    omada.Num
		wantErr bool
	}{
		{`12`, 12, false},
		{`12.5`, 12.5, false},
		{`-52`, -52, false},
		{`"12"`, 12, false},
		{`" 7 "`, 7, false},
		{`""`, 0, false},
		{`null`, 0, false},
		{`"abc"`, 0, true},
		{`true`, 0, true},
		{`{}`, 0, true},
	}
	for _, tt := range tests {
		var got omada.Num
		err := json.Unmarshal([]byte(tt.in), &got)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Num(%s) = %v, %v; want %v, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestTextUnmarshal(t *testing.T) {
	tests := []struct {
		in      string
		want    omada.Text
		wantErr bool
	}{
		{`"41day(s) 6h"`, "41day(s) 6h", false},
		{`3600`, "3600", false},
		{`null`, "", false},
		{`""`, "", false},
		{`{"a":1}`, "", true},
		{`[1]`, "", true},
	}
	for _, tt := range tests {
		var got omada.Text
		err := json.Unmarshal([]byte(tt.in), &got)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("Text(%s) = %q, %v; want %q, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestValidateName(t *testing.T) {
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	valid := []string{"a", "AP office", "Büro", string(long[:128])}
	invalid := []string{"", " lead", "trail ", "+x", "-x", "@x", "=x", string(long)}
	for _, n := range valid {
		if err := omada.ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	for _, n := range invalid {
		if err := omada.ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want an error", n)
		}
	}
}

// FuzzDecode feeds arbitrary JSON to the response types. Controllers send
// numbers as strings in places, so decoding must never panic and must fail
// cleanly on anything unexpected.
func FuzzDecode(f *testing.F) {
	for _, seed := range []string{
		`{"mac":"AA","status":1,"uptime":"1d","cpuUtil":"5","memUtil":5}`,
		`{"uptime":3600,"cpuUtil":null}`,
		`{"port":"4","rssi":-52,"trafficDown":1e9,"wireless":true}`,
		`{"cpuUtil":{}}`, `{"uptime":[]}`, `[]`, `null`, `"`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(_ *testing.T, data []byte) {
		var d omada.Device
		_ = json.Unmarshal(data, &d)
		_ = d.Label()
		_ = d.StatusText()
		var c omada.NetClient
		_ = json.Unmarshal(data, &c)
		_ = c.Label()
	})
}
