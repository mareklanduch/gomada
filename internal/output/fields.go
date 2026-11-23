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

// SSIDFields lists everything worth showing about one Wi-Fi network.
// password is what to show as the password: the password itself, or a note
// that it is hidden. It is left out for networks that have none.
func SSIDFields(s omada.SSID, password string) []Field {
	fields := []Field{
		{"Name", OrNone(s.Name)},
		{"Security", s.SecurityText()},
		{"Band", OrNone(s.BandText())},
		{"Guest network", YesNo(s.Guest)},
		{"Hidden", YesNo(s.Hidden())},
	}
	if s.VLANOn {
		fields = append(fields, Field{"VLAN", fmt.Sprintf("%d", int(s.VLANID))})
	}
	if s.Security == omada.SecurityPersonal {
		fields = append(fields, Field{"Password", password})
	}
	return fields
}

// AlertState says whether an alert is resolved.
func AlertState(a omada.Alert) string {
	if a.Resolved {
		return "resolved"
	}
	return "unresolved"
}

// AlertFields lists everything worth showing about one alert.
func AlertFields(a omada.Alert) []Field {
	return []Field{
		{"Time", Timestamp(a.Time())},
		{"Level", OrNone(a.Level)},
		{"Module", OrNone(a.Module)},
		{"State", AlertState(a)},
		{"Message", OrNone(a.Content)},
	}
}

// LatestFirmware describes the newest known firmware next to the installed
// one: the version with a note when it is an update, or that the device is
// up to date.
func LatestFirmware(f omada.Firmware) string {
	switch {
	case f.UpdateAvailable():
		return f.Latest + " (update available)"
	case f.Latest == "":
		return "unknown"
	}
	return "up to date"
}

// FirmwareFields lists the firmware state of one device.
func FirmwareFields(d omada.Device, f omada.Firmware) []Field {
	installed := f.Current
	if installed == "" {
		installed = d.Firmware
	}
	fields := []Field{
		{"Device", d.Label()},
		{"Installed", OrNone(installed)},
		{"Latest", LatestFirmware(f)},
	}
	if f.UpdateAvailable() && f.ReleaseNotes != "" {
		fields = append(fields, Field{"Release notes", f.ReleaseNotes})
	}
	return fields
}
