package tui

import (
	"context"
	"fmt"

	"github.com/mareklanduch/gomada/internal/app"
	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/output"
	"github.com/mareklanduch/gomada/internal/tui/table"
)

// list is one table together with the things its rows stand for.
type list struct {
	// noun names what the rows are, for messages: "device", "client".
	noun    string
	table   table.Model
	items   map[string]any // by row key
	loaded  bool
	loading bool
	err     error
	// actions are what can be done to the selected row.
	actions []action
	// details returns the heading and fields shown when a row is opened.
	// Nil means opening a row does something else.
	details func(item any) (string, []output.Field)
}

// selected returns the thing the cursor is on.
func (l *list) selected() (any, bool) {
	key, ok := l.table.SelectedKey()
	if !ok {
		return nil, false
	}
	item, ok := l.items[key]
	return item, ok
}

// target is what an action works on.
type target struct {
	app    *app.App
	sess   *app.Session
	siteID string
}

// action is something a person can do to the selected row with one key.
// New operations are added by describing them here; confirmation and input
// handling come with the type.
type action struct {
	key   string // the key that triggers it, e.g. "r"
	label string // the word in the key hints, e.g. "reboot"
	// confirm returns the question to ask before running. Nil means the
	// action runs without asking.
	confirm func(item any) string
	// prompt returns the label and starting text of a text field to show
	// before running. Nil means the action takes no input.
	prompt func(item any) (label, initial string)
	// run does the work, off the UI goroutine, and returns the message to
	// show when it is done.
	run func(ctx context.Context, t target, item any, input string) (string, error)
}

// tab is a list that loads its rows from the open site.
type tab struct {
	title string
	list
	load func(ctx context.Context, s *app.Session, siteID string) ([]table.Row, map[string]any, error)
}

func hostsList() list {
	return list{
		noun: "host",
		table: table.New([]table.Column{
			{Title: "NAME"}, {Title: "ADDRESS", MaxWidth: 40}, {Title: "LAST SITE"}, {Title: "SECRET"},
		}, 1),
		details: nil, // enter opens the host
		actions: []action{{
			key: "x", label: "remove",
			confirm: func(item any) string {
				return fmt.Sprintf("Remove host %s and its stored secret?", item.(app.HostInfo).Name)
			},
			run: func(_ context.Context, t target, item any, _ string) (string, error) {
				name := item.(app.HostInfo).Name
				return "Removed host " + name + ".", t.app.RemoveHost(name)
			},
		}},
	}
}

func hostRows(hosts []app.HostInfo) ([]table.Row, map[string]any) {
	rows := make([]table.Row, len(hosts))
	items := make(map[string]any, len(hosts))
	for i, h := range hosts {
		rows[i] = table.Row{Key: h.Name, Cells: []table.Cell{
			table.Text(h.Name), table.Text(h.URL), table.Text(output.OrNone(h.Site)), table.Text(h.SecretStore),
		}}
		items[h.Name] = h
	}
	return rows, items
}

func sitesList() list {
	return list{
		noun:  "site",
		table: table.New([]table.Column{{Title: "NAME"}, {Title: "ID", MaxWidth: 40}}, 1),
	}
}

func siteRows(sites []omada.Site) ([]table.Row, map[string]any) {
	rows := make([]table.Row, len(sites))
	items := make(map[string]any, len(sites))
	for i, s := range sites {
		rows[i] = table.Row{Key: s.ID, Cells: []table.Cell{table.Text(s.Name), table.Text(s.ID)}}
		items[s.ID] = s
	}
	return rows, items
}

// Status symbols. The shape carries the meaning, so they read without color.
const (
	symbolOnline  = "●"
	symbolOffline = "○"
	symbolOther   = "◐"
)

func devicesTab() *tab {
	return &tab{
		title: "Devices",
		list: list{
			noun: "device",
			table: table.New([]table.Column{
				{Title: ""},
				{Title: "NAME"},
				{Title: "TYPE"},
				{Title: "STATUS"},
				{Title: "MODEL"},
				{Title: "IP"},
				{Title: "UPTIME"},
				{Title: "CPU", Numeric: true},
				{Title: "MEM", Numeric: true},
				{Title: "MAC"},
			}, 2),
			details: func(item any) (string, []output.Field) {
				d := item.(omada.Device)
				return d.Label(), output.DeviceFields(d)
			},
			actions: []action{
				{
					key: "r", label: "reboot",
					confirm: func(item any) string {
						d := item.(omada.Device)
						return fmt.Sprintf("Reboot %s (%s)?", d.Label(), d.Type)
					},
					run: func(ctx context.Context, t target, item any, _ string) (string, error) {
						d := item.(omada.Device)
						return "Rebooting " + d.Label() + ".", t.sess.RebootDevice(ctx, t.siteID, d)
					},
				},
				{
					key: "n", label: "rename",
					prompt: func(item any) (string, string) {
						d := item.(omada.Device)
						return "New name for " + d.Label(), d.Name
					},
					run: func(ctx context.Context, t target, item any, name string) (string, error) {
						d := item.(omada.Device)
						return "Renamed " + d.Label() + " to " + name + ".", t.sess.RenameDevice(ctx, t.siteID, d, name)
					},
				},
			},
		},
		load: func(ctx context.Context, s *app.Session, siteID string) ([]table.Row, map[string]any, error) {
			devs, err := s.Devices(ctx, siteID)
			if err != nil {
				return nil, nil, err
			}
			rows := make([]table.Row, len(devs))
			items := make(map[string]any, len(devs))
			for i, d := range devs {
				symbol := symbolOther
				switch d.Status {
				case omada.StatusConnected:
					symbol = symbolOnline
				case omada.StatusDisconnected:
					symbol = symbolOffline
				}
				uptime, cpu, mem := table.Text(output.None), table.Num(output.None, -1), table.Num(output.None, -1)
				if d.Online() {
					uptime = table.Text(output.Uptime(string(d.Uptime)))
					cpu = table.Num(output.Percent(float64(d.CPU)), float64(d.CPU))
					mem = table.Num(output.Percent(float64(d.Mem)), float64(d.Mem))
				}
				rows[i] = table.Row{Key: d.MAC, Dim: !d.Online(), Cells: []table.Cell{
					table.Text(symbol), table.Text(d.Label()), table.Text(output.OrNone(d.Type)), table.Text(d.StatusText()),
					table.Text(output.OrNone(d.Model)), table.Text(output.OrNone(d.IP)), uptime, cpu, mem, table.Text(d.MAC),
				}}
				items[d.MAC] = d
			}
			return rows, items, nil
		},
	}
}

func clientsTab() *tab {
	return &tab{
		title: "Clients",
		list: list{
			noun: "client",
			table: table.New([]table.Column{
				{Title: "NAME"},
				{Title: "IP"},
				{Title: "CONNECTED TO", MaxWidth: 36},
				{Title: "SIGNAL", Numeric: true},
				{Title: "DOWN", Numeric: true},
				{Title: "UP", Numeric: true},
				{Title: "UPTIME", Numeric: true},
				{Title: "MAC"},
			}, 1),
			details: func(item any) (string, []output.Field) {
				c := item.(omada.NetClient)
				return c.Label(), output.ClientFields(c)
			},
			actions: []action{
				{
					key: "n", label: "rename",
					prompt: func(item any) (string, string) {
						c := item.(omada.NetClient)
						return "New name for " + c.Label(), c.Name
					},
					run: func(ctx context.Context, t target, item any, name string) (string, error) {
						c := item.(omada.NetClient)
						return "Renamed " + c.Label() + " to " + name + ".", t.sess.RenameClient(ctx, t.siteID, c, name)
					},
				},
				{
					key: "b", label: "block",
					confirm: func(item any) string {
						c := item.(omada.NetClient)
						return fmt.Sprintf("Block %s (%s)?", c.Label(), c.MAC)
					},
					run: func(ctx context.Context, t target, item any, _ string) (string, error) {
						c := item.(omada.NetClient)
						return "Blocked " + c.Label() + ". Unblock with: gomada client unblock " + c.MAC,
							t.sess.BlockClient(ctx, t.siteID, c)
					},
				},
				{
					key: "c", label: "reconnect",
					run: func(ctx context.Context, t target, item any, _ string) (string, error) {
						c := item.(omada.NetClient)
						return "Asked " + c.Label() + " to reconnect.", t.sess.ReconnectClient(ctx, t.siteID, c)
					},
				},
			},
		},
		load: func(ctx context.Context, s *app.Session, siteID string) ([]table.Row, map[string]any, error) {
			clients, err := s.Clients(ctx, siteID)
			if err != nil {
				return nil, nil, err
			}
			rows := make([]table.Row, len(clients))
			items := make(map[string]any, len(clients))
			for i, c := range clients {
				// Wired clients sort after every wireless signal strength.
				signal := table.Num(output.Signal(c), 1)
				if c.Wireless {
					signal.Num = float64(c.RSSI)
				}
				rows[i] = table.Row{Key: c.MAC, Cells: []table.Cell{
					table.Text(c.Label()), table.Text(output.OrNone(c.IP)), table.Text(output.OrNone(c.ConnectedTo())), signal,
					table.Num(output.Bytes(float64(c.TrafficDown)), float64(c.TrafficDown)),
					table.Num(output.Bytes(float64(c.TrafficUp)), float64(c.TrafficUp)),
					table.Num(output.Seconds(float64(c.Uptime)), float64(c.Uptime)),
					table.Text(c.MAC),
				}}
				items[c.MAC] = c
			}
			return rows, items, nil
		},
	}
}
