package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/output"
)

// upgradeTimeout bounds how long 'device upgrade' waits for the device.
const upgradeTimeout = 20 * time.Minute

type firmwareJSON struct {
	MAC             string `json:"mac"`
	Name            string `json:"name"`
	Installed       string `json:"installed"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseNotes    string `json:"release_notes,omitempty"`
}

func toFirmwareJSON(d omada.Device, f omada.Firmware) firmwareJSON {
	installed := f.Current
	if installed == "" {
		installed = d.Firmware
	}
	return firmwareJSON{
		MAC: d.MAC, Name: d.Name, Installed: installed, Latest: f.Latest,
		UpdateAvailable: f.UpdateAvailable(), ReleaseNotes: f.ReleaseNotes,
	}
}

// listUpdates prints the connected devices that have a firmware update
// waiting. It asks the controller once per device.
func (c *cli) listUpdates(cmd *cobra.Command, format output.Format, s session, devs []omada.Device) error {
	type update struct {
		dev omada.Device
		fw  omada.Firmware
	}
	updates := []update{}
	for _, d := range devs {
		if !d.Online() {
			continue
		}
		fw, err := s.Firmware(cmd.Context(), s.site.ID, d)
		if err != nil {
			return fmt.Errorf("firmware of %s: %w", d.Label(), err)
		}
		if fw.UpdateAvailable() {
			updates = append(updates, update{d, fw})
		}
	}
	if format == output.FormatJSON {
		out := make([]firmwareJSON, len(updates))
		for i, u := range updates {
			out[i] = toFirmwareJSON(u.dev, u.fw)
		}
		return output.JSON(c.Out, out)
	}
	if len(updates) == 0 {
		fmt.Fprintln(c.Err, "Every connected device is up to date.")
		return nil
	}
	rows := make([][]string, len(updates))
	for i, u := range updates {
		rows[i] = []string{u.dev.Label(), output.OrNone(u.dev.Type), output.OrNone(u.dev.Model), u.fw.Current, u.fw.Latest}
	}
	return output.Table(c.Out, []string{"name", "type", "model", "installed", "latest"}, rows)
}

func (c *cli) deviceFirmwareCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "firmware DEVICE",
		Short: "Show the installed and the newest firmware of a device",
		Long: `Show the installed firmware of a device and the newest one the controller
knows about, with its release notes.

The controller looks for new firmware on its own schedule; this shows what
it found the last time it looked.`,
		Args: exactArgs(1, "DEVICE"),
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
			fw, err := s.Firmware(cmd.Context(), site.ID, d)
			if err != nil {
				return err
			}
			if format == output.FormatJSON {
				return output.JSON(c.Out, toFirmwareJSON(d, fw))
			}
			return output.Fields(c.Out, output.FirmwareFields(d, fw))
		},
	}
}

func (c *cli) deviceUpgradeCmd() *cobra.Command {
	var noWait bool
	cmd := &cobra.Command{
		Use:   "upgrade DEVICE",
		Short: "Install the newest firmware on a device",
		Long: `Install the newest firmware on a device. The controller downloads it from
TP-Link.

The device restarts, and everything connected through it loses its connection
for a few minutes. Do not unplug the device meanwhile. This asks for
confirmation; pass --yes to skip the question, which is required when not
running in a terminal.

The command waits until the device is done and reports whether the update
worked. With --no-wait it returns as soon as the update has started.`,
		Args: exactArgs(1, "DEVICE"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, site, err := c.connectSite(cmd)
			if err != nil {
				return err
			}
			d, err := s.FindDevice(ctx, site.ID, args[0])
			if err != nil {
				return err
			}
			fw, err := s.Firmware(ctx, site.ID, d)
			if err != nil {
				return err
			}
			if !fw.UpdateAvailable() {
				fmt.Fprintf(c.Out, "%s is already up to date (firmware %s).\n", d.Label(), output.OrNone(fw.Current))
				return nil
			}
			question := fmt.Sprintf("Update %s (%s) from %s to %s? It restarts and its clients lose their connection for a few minutes.",
				d.Label(), d.Type, fw.Current, fw.Latest)
			if err := c.confirm(question); err != nil {
				return err
			}
			if _, err := s.UpgradeDevice(ctx, site.ID, d); err != nil {
				return err
			}
			if noWait {
				fmt.Fprintf(c.Out, "Updating %s to %s. It restarts when it is done.\n", d.Label(), fw.Latest)
				return nil
			}
			fmt.Fprintf(c.Err, "Updating %s to %s. This takes a few minutes; do not unplug the device.\n", d.Label(), fw.Latest)
			waitCtx, cancel := context.WithTimeout(ctx, upgradeTimeout)
			defer cancel()
			ok, err := s.WaitForUpgrade(waitCtx, site.ID, d, c.pollEvery())
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				return fmt.Errorf("%s has not reported back after %s; check it in the controller", d.Label(), upgradeTimeout)
			case err != nil:
				return err
			case !ok:
				return fmt.Errorf("the update of %s did not succeed; check the device in the controller", d.Label())
			}
			fmt.Fprintf(c.Out, "%s now runs firmware %s.\n", d.Label(), fw.Latest)
			return nil
		},
	}
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return once the update has started")
	return cmd
}
