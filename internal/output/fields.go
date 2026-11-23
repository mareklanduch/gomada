package output

import (
	"fmt"

	"github.com/mareklanduch/gomada/internal/omada"
)

// Field is one labeled value of a details view.
type Field struct {
	Label string
	Value string
}

// DeviceFields lists everything worth showing about one device.
func DeviceFields(d omada.Device) []Field {
	fields := []Field{
		{"Name", OrNone(d.Name)},
		{"Status", d.StatusText()},
		{"Type", OrNone(d.Type)},
		{"Model", OrNone(d.Model)},
		{"IP address", OrNone(d.IP)},
		{"MAC address", OrNone(d.MAC)},
		{"Firmware", OrNone(d.Firmware)},
		{"Serial", OrNone(d.Serial)},
	}
	if d.Online() {
		fields = append(fields,
			Field{"Uptime", Uptime(string(d.Uptime))},
			Field{"CPU", Percent(float64(d.CPU))},
			Field{"Memory", Percent(float64(d.Mem))},
		)
	}
	return fields
}

// Signal formats a wireless signal strength, or "wired" for wired clients.
func Signal(c omada.NetClient) string {
	switch {
	case !c.Wireless:
		return "wired"
	case c.RSSI == 0:
		return None
	}
	return fmt.Sprintf("%.0f dBm", float64(c.RSSI))
}

// ClientFields lists everything worth showing about one client.
func ClientFields(c omada.NetClient) []Field {
	fields := []Field{
		{"Name", OrNone(c.Name)},
		{"Host name", OrNone(c.HostName)},
		{"IP address", OrNone(c.IP)},
		{"MAC address", OrNone(c.MAC)},
		{"Vendor", OrNone(c.Vendor)},
		{"Connected to", OrNone(c.ConnectedTo())},
		{"Signal", Signal(c)},
	}
	if c.NetworkName != "" {
		fields = append(fields, Field{"Network", c.NetworkName})
	}
	return append(fields,
		Field{"Downloaded", Bytes(float64(c.TrafficDown))},
		Field{"Uploaded", Bytes(float64(c.TrafficUp))},
		Field{"Uptime", Seconds(float64(c.Uptime))},
	)
}
