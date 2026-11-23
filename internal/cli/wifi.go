package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mareklanduch/gomada/internal/app"
	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/output"
	"github.com/mareklanduch/gomada/internal/qr"
)

func (c *cli) wifiCmd() *cobra.Command {
	cmd := group("wifi", "List Wi-Fi networks, show passwords and QR codes")
	cmd.AddCommand(c.wifiListCmd(), c.wifiShowCmd(), c.wifiQRCmd())
	return cmd
}

type wifiJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Security string `json:"security"`
	Band     string `json:"band"`
	Guest    bool   `json:"guest"`
	Hidden   bool   `json:"hidden"`
	VLANID   int    `json:"vlan_id,omitempty"`
	// Password is present only when it was asked for with --password.
	Password string `json:"password,omitempty"`
}

func toWifiJSON(s omada.SSID) wifiJSON {
	j := wifiJSON{ID: s.ID, Name: s.Name, Security: s.SecurityText(), Band: s.BandText(), Guest: s.Guest, Hidden: s.Hidden()}
	if s.VLANOn {
		j.VLANID = int(s.VLANID)
	}
	return j
}

func (c *cli) wifiListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the Wi-Fi networks of the site",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := c.outputFormat()
			if err != nil {
				return err
			}
			s, site, err := c.connectSite(cmd)
			if err != nil {
				return err
			}
			ssids, err := s.SSIDs(cmd.Context(), site.ID)
			if err != nil {
				return err
			}
			if format == output.FormatJSON {
				out := make([]wifiJSON, len(ssids))
				for i, ssid := range ssids {
					out[i] = toWifiJSON(ssid)
				}
				return output.JSON(c.Out, out)
			}
			rows := make([][]string, len(ssids))
			for i, ssid := range ssids {
				rows[i] = []string{ssid.Name, ssid.SecurityText(), output.OrNone(ssid.BandText()), output.YesNo(ssid.Guest), output.YesNo(ssid.Hidden())}
			}
			return output.Table(c.Out, []string{"name", "security", "band", "guest", "hidden"}, rows)
		},
	}
}

// findSSID resolves the NETWORK argument of a command.
func (c *cli) findSSID(cmd *cobra.Command, target string) (session, omada.SSID, error) {
	s, site, err := c.connectSite(cmd)
	if err != nil {
		return session{}, omada.SSID{}, err
	}
	ssid, err := s.FindSSID(cmd.Context(), site.ID, target)
	if err != nil {
		return session{}, omada.SSID{}, err
	}
	return session{s, site}, ssid, nil
}

func (c *cli) wifiShowCmd() *cobra.Command {
	var showPassword bool
	cmd := &cobra.Command{
		Use:   "show NETWORK",
		Short: "Show the details of one Wi-Fi network",
		Long: `Show the details of one Wi-Fi network, named by its name.

The password is printed only with --password.`,
		Args: exactArgs(1, "NETWORK"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := c.outputFormat()
			if err != nil {
				return err
			}
			s, ssid, err := c.findSSID(cmd, args[0])
			if err != nil {
				return err
			}
			// shown is what the Password line says.
			shown := "hidden; pass --password to show it"
			j := toWifiJSON(ssid)
			if showPassword {
				if j.Password, err = s.WifiPassword(cmd.Context(), s.site.ID, ssid); err != nil {
					return err
				}
				shown = j.Password
				if shown == "" {
					shown = "not revealed by the controller"
				}
			}
			if format == output.FormatJSON {
				return output.JSON(c.Out, j)
			}
			return output.Fields(c.Out, output.SSIDFields(ssid, shown))
		},
	}
	cmd.Flags().BoolVar(&showPassword, "password", false, "also print the Wi-Fi password")
	return cmd
}

func (c *cli) wifiQRCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "qr NETWORK",
		Short: "Print a QR code that phones can scan to join a Wi-Fi network",
		Long: `Print a QR code that phones can scan to join a Wi-Fi network.

The code contains the Wi-Fi password, so anyone who can see it can join the
network. Networks with a login per person (WPA-Enterprise, PPSK) have no
shared password and so no QR code.`,
		Args: exactArgs(1, "NETWORK"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, ssid, err := c.findSSID(cmd, args[0])
			if err != nil {
				return err
			}
			password, err := s.WifiPassword(cmd.Context(), s.site.ID, ssid)
			if err != nil {
				return err
			}
			payload, err := app.WifiQRPayload(ssid, password)
			if err != nil {
				return err
			}
			code, err := qr.Encode(payload)
			if err != nil {
				return err
			}
			// Light ink on black has the right polarity whatever the
			// terminal's own colors are. Without color the code still has
			// the right polarity on a dark terminal.
			start, end := "", ""
			if c.Interactive && os.Getenv("NO_COLOR") == "" {
				start, end = "\x1b[97;40m", "\x1b[0m"
			}
			var b strings.Builder
			for _, line := range code.HalfBlocks(2) {
				b.WriteString(start + line + end + "\n")
			}
			fmt.Fprint(c.Out, b.String())
			fmt.Fprintf(c.Err, "Scan to join %s.\n", ssid.Name)
			return nil
		},
	}
}
