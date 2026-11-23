package omada

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Num is a number that the controller may send as a JSON number, as a string
// holding a number, or not at all. Controller versions differ on this for
// fields such as uptime and port.
type Num float64

// UnmarshalJSON accepts 12, 12.5, "12", "", and null.
func (n *Num) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*n = 0
		return nil
	}
	s := string(b)
	if b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*n = 0
			return nil
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("omada: %q is not a number", s)
	}
	*n = Num(f)
	return nil
}

// Text is a string that the controller may send as a JSON string or as a
// number. Uptime, for example, arrives as "41day(s) 6h" from some devices and
// as a count of seconds from others.
type Text string

// UnmarshalJSON accepts "abc", 12, true, and null.
func (t *Text) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*t = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*t = Text(s)
		return nil
	}
	if b[0] == '{' || b[0] == '[' {
		return fmt.Errorf("omada: expected text, got %s", b)
	}
	*t = Text(b)
	return nil
}
