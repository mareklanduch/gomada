package cli

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/output"
)

func (c *cli) alertCmd() *cobra.Command {
	cmd := group("alert", "Read the alerts the controller raised")
	cmd.AddCommand(c.alertListCmd(), c.alertShowCmd())
	return cmd
}

type alertJSON struct {
	ID       string `json:"id"`
	Time     string `json:"time"` // RFC 3339, UTC
	Level    string `json:"level"`
	Module   string `json:"module"`
	Resolved bool   `json:"resolved"`
	Message  string `json:"message"`
}

func toAlertJSON(a omada.Alert) alertJSON {
	return alertJSON{
		ID: string(a.ID), Time: a.Time().UTC().Format(time.RFC3339), Level: a.Level,
		Module: a.Module, Resolved: a.Resolved, Message: a.Content,
	}
}

func (c *cli) alertListCmd() *cobra.Command {
	var unresolved bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the alerts of the last 30 days, newest first",
		Long: `List the alerts of the site from the last 30 days, newest first.

At most the 100 newest unresolved and the 100 newest resolved alerts are
listed; the controller keeps the full history.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := c.outputFormat()
			if err != nil {
				return err
			}
			s, site, err := c.connectSite(cmd)
			if err != nil {
				return err
			}
			all, err := s.Alerts(cmd.Context(), site.ID)
			if err != nil {
				return err
			}
			alerts := []omada.Alert{}
			for _, a := range all {
				if !unresolved || !a.Resolved {
					alerts = append(alerts, a)
				}
			}
			if format == output.FormatJSON {
				out := make([]alertJSON, len(alerts))
				for i, a := range alerts {
					out[i] = toAlertJSON(a)
				}
				return output.JSON(c.Out, out)
			}
			rows := make([][]string, len(alerts))
			for i, a := range alerts {
				rows[i] = []string{output.Timestamp(a.Time()), output.OrNone(a.Level), output.AlertState(a), string(a.ID), a.Content}
			}
			return output.Table(c.Out, []string{"time", "level", "state", "id", "message"}, rows)
		},
	}
	cmd.Flags().BoolVar(&unresolved, "unresolved", false, "show only alerts that are not resolved")
	return cmd
}

func (c *cli) alertShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show ID",
		Short: "Show one alert in full",
		Long:  "Show one alert in full. Take the ID from 'gomada alert list'.",
		Args:  exactArgs(1, "ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := c.outputFormat()
			if err != nil {
				return err
			}
			s, site, err := c.connectSite(cmd)
			if err != nil {
				return err
			}
			a, err := s.FindAlert(cmd.Context(), site.ID, args[0])
			if err != nil {
				return err
			}
			if format == output.FormatJSON {
				return output.JSON(c.Out, toAlertJSON(a))
			}
			return output.Fields(c.Out, output.AlertFields(a))
		},
	}
}
