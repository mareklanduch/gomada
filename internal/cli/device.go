package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/output"
)

func (c *cli) deviceCmd() *cobra.Command {
	cmd := group("device", "List and manage gateways, switches, and access points")
	cmd.AddCommand(c.deviceListCmd(), c.deviceShowCmd(), c.deviceRebootCmd(), c.deviceRenameCmd())
	return cmd
}

type deviceJSON struct {
	MAC        string  `json:"mac"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Model      string  `json:"model"`
	IP         string  `json:"ip"`
	Status     string  `json:"status"`
	Online     bool    `json:"online"`
	Uptime     string  `json:"uptime"`
	CPUPercent float64 `json:"cpu_percent"`
	MemPercent float64 `json:"mem_percent"`
	Firmware   string  `json:"firmware"`
	Serial     string  `json:"serial"`
}

func toDeviceJSON(d omada.Device) deviceJSON {
	return deviceJSON{
		MAC: d.MAC, Name: d.Name, Type: d.Type, Model: d.Model, IP: d.IP,
		Status: d.StatusText(), Online: d.Online(), Uptime: string(d.Uptime),
		CPUPercent: float64(d.CPU), MemPercent: float64(d.Mem),
		Firmware: d.Firmware, Serial: d.Serial,
	}
}

func deviceRow(d omada.Device) []string {
	uptime, cpu, mem := output.None, output.None, output.None
	if d.Online() {
		uptime, cpu, mem = output.Uptime(string(d.Uptime)), output.Percent(float64(d.CPU)), output.Percent(float64(d.Mem))
	}
	return []string{d.StatusText(), d.Label(), output.OrNone(d.Type), output.OrNone(d.Model), output.OrNone(d.IP), d.MAC, uptime, cpu, mem}
}

func (c *cli) deviceListCmd() *cobra.Command {
	var deviceType, filter string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the devices of the site",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := c.outputFormat()
			if err != nil {
				return err
			}
			deviceType = strings.ToLower(deviceType)
			switch deviceType {
			case "", omada.TypeGateway, omada.TypeSwitch, omada.TypeAP:
			default:
				return usagef("unknown device type %q; use gateway, switch, or ap", deviceType)
			}
			s, site, err := c.connectSite(cmd)
			if err != nil {
				return err
			}
			all, err := s.Devices(cmd.Context(), site.ID)
			if err != nil {
				return err
			}
			devs := []omada.Device{}
			for _, d := range all {
				if (deviceType == "" || d.Type == deviceType) && output.Matches(deviceRow(d), filter) {
					devs = append(devs, d)
				}
			}
			if format == output.FormatJSON {
				out := make([]deviceJSON, len(devs))
				for i, d := range devs {
					out[i] = toDeviceJSON(d)
				}
				return output.JSON(c.Out, out)
			}
			rows := make([][]string, len(devs))
			for i, d := range devs {
				rows[i] = deviceRow(d)
			}
			return output.Table(c.Out, []string{"status", "name", "type", "model", "ip", "mac", "uptime", "cpu", "mem"}, rows)
		},
	}
	cmd.Flags().StringVar(&deviceType, "type", "", "show only this type: gateway, switch, or ap")
	cmd.Flags().StringVar(&filter, "filter", "", "show only devices with this text in any column")
	return cmd
}

func (c *cli) deviceShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show DEVICE",
		Short: "Show the details of one device",
		Long:  "Show the details of one device, named by MAC address or by name.",
		Args:  exactArgs(1, "DEVICE"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := c.outputFormat()
			if err != nil {
				return err
			}
			s, site, err := c.connectSite(cmd)
			if err != nil {
				return err
			}
			d, err := s.FindDevice(cmd.Context(), site.ID, args[0])
			if err != nil {
				return err
			}
			if format == output.FormatJSON {
				return output.JSON(c.Out, toDeviceJSON(d))
			}
			return output.Fields(c.Out, output.DeviceFields(d))
		},
	}
}

func (c *cli) deviceRebootCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reboot DEVICE",
		Short: "Restart a device",
		Long: `Restart a device, named by MAC address or by name.

Everything connected through the device loses its connection while it
restarts, so this asks for confirmation. Pass --yes to skip the question; that
is required when not running in a terminal.`,
		Args: exactArgs(1, "DEVICE"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, site, err := c.connectSite(cmd)
			if err != nil {
				return err
			}
			d, err := s.FindDevice(cmd.Context(), site.ID, args[0])
			if err != nil {
				return err
			}
			if err := c.confirm(fmt.Sprintf("Reboot %s (%s, %s)?", d.Label(), d.Type, d.MAC)); err != nil {
				return err
			}
			if err := s.RebootDevice(cmd.Context(), site.ID, d); err != nil {
				return err
			}
			fmt.Fprintf(c.Out, "Rebooting %s.\n", d.Label())
			return nil
		},
	}
}

func (c *cli) deviceRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename DEVICE NEW-NAME",
		Short: "Change the name of a device",
		Args:  exactArgs(2, "DEVICE NEW-NAME"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := omada.ValidateName(args[1]); err != nil {
				return usageError{err}
			}
			s, site, err := c.connectSite(cmd)
			if err != nil {
				return err
			}
			d, err := s.FindDevice(cmd.Context(), site.ID, args[0])
			if err != nil {
				return err
			}
			if err := s.RenameDevice(cmd.Context(), site.ID, d, args[1]); err != nil {
				return err
			}
			fmt.Fprintf(c.Out, "Renamed %s to %s.\n", d.Label(), args[1])
			return nil
		},
	}
}
