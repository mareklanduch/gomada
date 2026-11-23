// Package output turns devices, clients, and other results into text: the
// tables and JSON the command line prints, and the formatted values the
// terminal UI shows.
package output

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// None stands in for a value that is missing.
const None = "-"

// Duration formats a length of time with its two largest units:
// "41d 6h", "3h 12m", "5m", "42s".
func Duration(d time.Duration) string {
	if d <= 0 {
		return None
	}
	days := int(d / (24 * time.Hour))
	hours := int(d/time.Hour) % 24
	minutes := int(d/time.Minute) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

// Seconds formats a count of seconds as a duration.
func Seconds(s float64) string {
	return Duration(time.Duration(s * float64(time.Second)))
}

// Uptime formats a device uptime. Controllers send it either as a count of
// seconds or as text such as "41day(s) 6h 2m 1s"; both come out as "41d 6h".
func Uptime(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return None
	}
	if secs, err := strconv.ParseFloat(raw, 64); err == nil {
		return Seconds(secs)
	}
	fields := strings.Fields(strings.ReplaceAll(raw, "day(s)", "d"))
	if len(fields) > 2 {
		fields = fields[:2]
	}
	return strings.Join(fields, " ")
}

// Bytes formats a byte count with a binary unit: "1.2 GB".
func Bytes(n float64) string {
	if n <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 || n >= 100 {
		return fmt.Sprintf("%.0f %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}

// Percent formats a load value: "12%".
func Percent(v float64) string {
	return fmt.Sprintf("%.0f%%", v)
}

// OrNone returns s, or the placeholder for missing values when s is empty.
func OrNone(s string) string {
	if s == "" {
		return None
	}
	return s
}
