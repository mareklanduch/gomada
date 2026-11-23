package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/output"
)

func (c *cli) clientCmd() *cobra.Command {
	cmd := group("client", "List and manage connected clients")
	cmd.AddCommand(c.clientListCmd(), c.clientShowCmd(), c.clientRenameCmd(),
		c.clientBlockCmd(), c.clientUnblockCmd(), c.clientReconnectCmd())
	return cmd
}

type clientJSON struct {
	MAC           string  `json:"mac"`
	Name          string  `json:"name"`
	HostName      string  `json:"host_name"`
	IP            string  `json:"ip"`
	Vendor        string  `json:"vendor"`
	Wireless      bool    `json:"wireless"`
	SSID          string  `json:"ssid"`
	Network       string  `json:"network"`
	ConnectedTo   string  `json:"connected_to"`
	Port          int     `json:"port"`
	SignalDBm     float64 `json:"signal_dbm"`
	UptimeSeconds int64   `json:"uptime_seconds"`
	DownBytes     int64   `json:"down_bytes"`
	UpBytes       int64   `json:"up_bytes"`
}

func toClientJSON(n omada.NetClient) clientJSON {
	return clientJSON{
		MAC: n.MAC, Name: n.Name, HostName: n.HostName, IP: n.IP, Vendor: n.Vendor,
		Wireless: n.Wireless, SSID: n.SSID, Network: n.NetworkName, ConnectedTo: n.ConnectedTo(),
		Port: int(n.Port), SignalDBm: float64(n.RSSI), UptimeSeconds: int64(n.Uptime),
		DownBytes: int64(n.TrafficDown), UpBytes: int64(n.TrafficUp),
	}
}

func clientRow(n omada.NetClient) []string {
	return []string{
		n.Label(), output.OrNone(n.IP), n.MAC, output.OrNone(n.ConnectedTo()), output.Signal(n),
		output.Bytes(float64(n.TrafficDown)), output.Bytes(float64(n.TrafficUp)), output.Seconds(float64(n.Uptime)),
	}
}

func (c *cli) clientListCmd() *cobra.Command {
	var (
		wired, wireless bool
		filter          string
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the clients connected to the site",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := c.outputFormat()
			if err != nil {
				return err
			}
			if wired && wireless {
				return usagef("--wired and --wireless exclude each other; leave both out to see all clients")
			}
			s, site, err := c.connectSite(cmd)
			if err != nil {
				return err
			}
			all, err := s.Clients(cmd.Context(), site.ID)
			if err != nil {
				return err
			}
			clients := []omada.NetClient{}
			for _, n := range all {
				if (wired && n.Wireless) || (wireless && !n.Wireless) || !output.Matches(clientRow(n), filter) {
					continue
				}
				clients = append(clients, n)
			}
			if format == output.FormatJSON {
				out := make([]clientJSON, len(clients))
				for i, n := range clients {
					out[i] = toClientJSON(n)
				}
				return output.JSON(c.Out, out)
			}
			rows := make([][]string, len(clients))
			for i, n := range clients {
				rows[i] = clientRow(n)
			}
			return output.Table(c.Out, []string{"name", "ip", "mac", "connected to", "signal", "down", "up", "uptime"}, rows)
		},
	}
	cmd.Flags().BoolVar(&wired, "wired", false, "show only wired clients")
	cmd.Flags().BoolVar(&wireless, "wireless", false, "show only wireless clients")
	cmd.Flags().StringVar(&filter, "filter", "", "show only clients with this text in any column, such as a network or access point name")
	return cmd
}

// findClient resolves the CLIENT argument of a command.
func (c *cli) findClient(cmd *cobra.Command, target string) (session, omada.NetClient, error) {
	s, site, err := c.connectSite(cmd)
	if err != nil {
		return session{}, omada.NetClient{}, err
	}
	n, err := s.FindClient(cmd.Context(), site.ID, target)
	if err != nil {
		return session{}, omada.NetClient{}, err
	}
	return session{s, site}, n, nil
}

func (c *cli) clientShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show CLIENT",
		Short: "Show the details of one client",
		Long:  "Show the details of one connected client, named by MAC address, name, or host name.",
		Args:  exactArgs(1, "CLIENT"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := c.outputFormat()
			if err != nil {
				return err
			}
			_, n, err := c.findClient(cmd, args[0])
			if err != nil {
				return err
			}
			if format == output.FormatJSON {
				return output.JSON(c.Out, toClientJSON(n))
			}
			return output.Fields(c.Out, output.ClientFields(n))
		},
	}
}

func (c *cli) clientRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename CLIENT NEW-NAME",
		Short: "Change the name the controller shows for a client",
		Args:  exactArgs(2, "CLIENT NEW-NAME"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := omada.ValidateName(args[1]); err != nil {
				return usageError{err}
			}
			s, n, err := c.findClient(cmd, args[0])
			if err != nil {
				return err
			}
			if err := s.RenameClient(cmd.Context(), s.site.ID, n, args[1]); err != nil {
				return err
			}
			fmt.Fprintf(c.Out, "Renamed %s to %s.\n", n.Label(), args[1])
			return nil
		},
	}
}

func (c *cli) clientBlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "block CLIENT",
		Short: "Bar a client from the network",
		Long: `Bar a connected client from the network.

This asks for confirmation. Pass --yes to skip the question; that is required
when not running in a terminal. Note the MAC address it prints: a blocked
client is offline, so 'gomada client unblock' needs the MAC address.`,
		Args: exactArgs(1, "CLIENT"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, n, err := c.findClient(cmd, args[0])
			if err != nil {
				return err
			}
			if err := c.confirm(fmt.Sprintf("Block %s (%s)?", n.Label(), n.MAC)); err != nil {
				return err
			}
			if err := s.BlockClient(cmd.Context(), s.site.ID, n); err != nil {
				return err
			}
			fmt.Fprintf(c.Out, "Blocked %s (%s). Unblock it with: gomada client unblock %s\n", n.Label(), n.MAC, n.MAC)
			return nil
		},
	}
}

func (c *cli) clientUnblockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unblock MAC",
		Short: "Let a blocked client back on the network",
		Long:  "Let a blocked client back on the network. A blocked client is offline, so it is named by MAC address.",
		Args:  exactArgs(1, "MAC"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, site, err := c.connectSite(cmd)
			if err != nil {
				return err
			}
			if err := s.UnblockClient(cmd.Context(), site.ID, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(c.Out, "Unblocked %s.\n", args[0])
			return nil
		},
	}
}

func (c *cli) clientReconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reconnect CLIENT",
		Short: "Make a wireless client reconnect",
		Args:  exactArgs(1, "CLIENT"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, n, err := c.findClient(cmd, args[0])
			if err != nil {
				return err
			}
			if err := s.ReconnectClient(cmd.Context(), s.site.ID, n); err != nil {
				return err
			}
			fmt.Fprintf(c.Out, "Asked %s to reconnect.\n", n.Label())
			return nil
		},
	}
}
