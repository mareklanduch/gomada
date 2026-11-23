package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Format is how the command line prints results.
type Format string

// Output formats.
const (
	FormatTable Format = "table"
	FormatJSON  Format = "json"
)

// ParseFormat checks the value of the --output flag.
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(s)); f {
	case FormatTable, FormatJSON:
		return f, nil
	}
	return "", fmt.Errorf("unknown output format %q; use table or json", s)
}

// Table writes rows as aligned columns under an upper-case header.
func Table(w io.Writer, header []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	line := func(cells []string) error {
		_, err := fmt.Fprintln(tw, strings.Join(cells, "\t"))
		return err
	}
	upper := make([]string, len(header))
	for i, h := range header {
		upper[i] = strings.ToUpper(h)
	}
	if err := line(upper); err != nil {
		return err
	}
	for _, row := range rows {
		if err := line(row); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// Fields writes labeled values, one per line, with the values aligned.
func Fields(w io.Writer, fields []Field) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range fields {
		if _, err := fmt.Fprintf(tw, "%s:\t%s\n", f.Label, f.Value); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// JSON writes v as indented JSON.
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// Matches reports whether any cell contains the filter text, ignoring case.
// An empty filter matches everything.
func Matches(cells []string, filter string) bool {
	if filter == "" {
		return true
	}
	filter = strings.ToLower(filter)
	for _, cell := range cells {
		if strings.Contains(strings.ToLower(cell), filter) {
			return true
		}
	}
	return false
}
