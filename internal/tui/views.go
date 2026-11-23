package tui

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"

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
	details func(m *Model, item any) (string, []output.Field)
	// open, when set, replaces details: it decides what opening a row does.
	open func(m *Model, item any) tea.Cmd
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
	// help describes the action in the key list. Empty means the label
	// followed by "the selected <noun>".
	help string
	// confirm returns the question to ask before running. Nil means the
	// action runs without asking.
	confirm func(item any) string
	// prompt returns the label and starting text of a text field to show
	// before running. Nil means the action takes no input.
	prompt func(t target, item any) (label, initial string)
	// run does the work, off the UI goroutine, and returns the message to
	// show when it is done.
	run func(ctx context.Context, t target, item any, input string) (string, error)
}

// tab is a list that loads its rows from the open site. A new tab is added by
// describing it here and listing it in New.
type tab struct {
	title string
	list
	load func(ctx context.Context, s *app.Session, siteID string) ([]table.Row, map[string]any, error)

	// badge returns a suffix for the title in the tab bar, such as a count.
	badge func(t *tab) string
	// background keeps the tab loaded and current while it is not on
	// screen, for the sake of its badge.
	background bool
	// enrich fetches data that takes longer than the rows themselves. It
	// runs after the rows are on screen and reports whether it found
	// anything new, in which case the rows are loaded once more.
	enrich func(ctx context.Context, s *app.Session, siteID string, items map[string]any) bool
	// alt is a second view of the same tab, switched with B: for example
	// the blocked clients next to the connected ones. hint names a view in
	// the key hints of the other.
	alt     *tab
	showAlt bool
	hint    string
}

// active returns the view of the tab that is on screen.
func (t *tab) active() *tab {
	if t.showAlt && t.alt != nil {
		return t.alt
	}
	return t
}

// other returns the view of the tab that is not on screen.
func (t *tab) other() *tab {
	if t.showAlt || t.alt == nil {
		return t
	}
	return t.alt
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

// updateMark follows the firmware version of a device that has an update
// waiting.
const updateMark = "↑"

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
				{Title: "FIRMWARE"},
				{Title: "IP"},
				{Title: "UPTIME"},
				{Title: "CPU", Numeric: true},
				{Title: "MEM", Numeric: true},
				{Title: "MAC"},
			}, 2),
			details: func(m *Model, item any) (string, []output.Field) {
				d := item.(omada.Device)
				fields := output.DeviceFields(d)
				if fw, ok := m.sess.CachedFirmware(d.MAC); ok {
					fields = append(fields, output.Field{Label: "Latest firmware", Value: output.LatestFirmware(fw)})
					if fw.UpdateAvailable() && fw.ReleaseNotes != "" {
						fields = append(fields, output.Field{Label: "Release notes", Value: fw.ReleaseNotes})
					}
				}
				return d.Label(), fields
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
					prompt: func(_ target, item any) (string, string) {
						d := item.(omada.Device)
						return "New name for " + d.Label(), d.Name
					},
					run: func(ctx context.Context, t target, item any, name string) (string, error) {
						d := item.(omada.Device)
						return "Renamed " + d.Label() + " to " + name + ".", t.sess.RenameDevice(ctx, t.siteID, d, name)
					},
				},
				{
					// An update can take a device off the network, so it
					// asks for more than a y: the device name, typed out.
					key: "U", label: "update", help: "update the firmware of the selected device",
					prompt: func(t target, item any) (string, string) {
						d := item.(omada.Device)
						what := "Update " + d.Label()
						if fw, ok := t.sess.CachedFirmware(d.MAC); ok && fw.UpdateAvailable() {
							what += " from " + fw.Current + " to " + fw.Latest
						}
						return what + "? It restarts. Type its name to confirm", ""
					},
					run: func(ctx context.Context, t target, item any, typed string) (string, error) {
						d := item.(omada.Device)
						if typed != d.Label() {
							return "", fmt.Errorf("the name did not match %q; nothing was updated", d.Label())
						}
						fw, err := t.sess.UpgradeDevice(ctx, t.siteID, d)
						if errors.Is(err, app.ErrUpToDate) {
							return upperFirst(err.Error()) + ".", nil
						}
						if err != nil {
							return "", err
						}
						return "Updating " + d.Label() + " to " + fw.Latest + ". It restarts when it is done; do not unplug it.", nil
					},
				},
			},
		},
		enrich: func(ctx context.Context, s *app.Session, siteID string, items map[string]any) bool {
			devs := make([]omada.Device, 0, len(items))
			for _, item := range items {
				devs = append(devs, item.(omada.Device))
			}
			return s.RefreshFirmware(ctx, siteID, devs)
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
				firmware := output.OrNone(d.Firmware)
				if fw, ok := s.CachedFirmware(d.MAC); ok && fw.UpdateAvailable() {
					firmware += " " + updateMark
				}
				rows[i] = table.Row{Key: d.MAC, Dim: !d.Online(), Cells: []table.Cell{
					table.Text(symbol), table.Text(d.Label()), table.Text(output.OrNone(d.Type)), table.Text(d.StatusText()),
					table.Text(output.OrNone(d.Model)), table.Text(firmware), table.Text(output.OrNone(d.IP)), uptime, cpu, mem, table.Text(d.MAC),
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
		hint:  "connected",
		alt:   blockedTab(),
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
			details: func(_ *Model, item any) (string, []output.Field) {
				c := item.(omada.NetClient)
				return c.Label(), output.ClientFields(c)
			},
			actions: []action{
				{
					key: "n", label: "rename",
					prompt: func(_ target, item any) (string, string) {
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
						return "Blocked " + c.Label() + ". Press B to see the blocked clients.",
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

// blockedTab is the second view of the Clients tab: the clients that are
// barred from the network. They are not connected, so they are not among
// the rows of the first view.
func blockedTab() *tab {
	return &tab{
		title: "Clients · blocked",
		hint:  "blocked",
		list: list{
			noun: "blocked client",
			table: table.New([]table.Column{
				{Title: "NAME"}, {Title: "MAC"}, {Title: "TYPE"}, {Title: "LAST SEEN"},
			}, 1),
			details: func(_ *Model, item any) (string, []output.Field) {
				k := item.(omada.KnownClient)
				return k.Label(), []output.Field{
					{Label: "Name", Value: output.OrNone(k.Name)},
					{Label: "MAC address", Value: k.MAC},
					{Label: "Type", Value: wiredOrWireless(k.Wireless)},
					{Label: "Last seen", Value: output.Timestamp(k.LastSeen())},
					{Label: "State", Value: "blocked"},
				}
			},
			actions: []action{{
				key: "u", label: "unblock",
				confirm: func(item any) string {
					k := item.(omada.KnownClient)
					return fmt.Sprintf("Unblock %s (%s)?", k.Label(), k.MAC)
				},
				run: func(ctx context.Context, t target, item any, _ string) (string, error) {
					k := item.(omada.KnownClient)
					return "Unblocked " + k.Label() + ".", t.sess.UnblockClient(ctx, t.siteID, k.MAC)
				},
			}},
		},
		load: func(ctx context.Context, s *app.Session, siteID string) ([]table.Row, map[string]any, error) {
			blocked, err := s.BlockedClients(ctx, siteID)
			if err != nil {
				return nil, nil, err
			}
			rows := make([]table.Row, len(blocked))
			items := make(map[string]any, len(blocked))
			for i, k := range blocked {
				rows[i] = table.Row{Key: k.MAC, Cells: []table.Cell{
					table.Text(k.Label()), table.Text(k.MAC), table.Text(wiredOrWireless(k.Wireless)),
					table.Text(output.Timestamp(k.LastSeen())),
				}}
				items[k.MAC] = k
			}
			return rows, items, nil
		},
	}
}

func wiredOrWireless(wireless bool) string {
	if wireless {
		return "wireless"
	}
	return "wired"
}

func wifiTab() *tab {
	return &tab{
		title: "Wi-Fi",
		list: list{
			noun: "Wi-Fi network",
			table: table.New([]table.Column{
				{Title: "NAME", MaxWidth: 32}, {Title: "SECURITY"}, {Title: "BAND"}, {Title: "GUEST"}, {Title: "HIDDEN"},
			}, 1),
			open: func(m *Model, item any) tea.Cmd { return m.openWifi(item.(omada.SSID)) },
			actions: []action{{
				key: "p", label: "password", help: "show the password of the selected network",
				run: func(ctx context.Context, t target, item any, _ string) (string, error) {
					ssid := item.(omada.SSID)
					if ssid.Security != omada.SecurityPersonal {
						return ssid.Name + " has no shared password (" + ssid.SecurityText() + ").", nil
					}
					password, err := t.sess.WifiPassword(ctx, t.siteID, ssid)
					if err != nil {
						return "", err
					}
					if password == "" {
						return "The controller did not reveal the password of " + ssid.Name + ".", nil
					}
					return "Password of " + ssid.Name + ": " + password, nil
				},
			}},
		},
		load: func(ctx context.Context, s *app.Session, siteID string) ([]table.Row, map[string]any, error) {
			ssids, err := s.SSIDs(ctx, siteID)
			if err != nil {
				return nil, nil, err
			}
			rows := make([]table.Row, len(ssids))
			items := make(map[string]any, len(ssids))
			for i, ssid := range ssids {
				key := ssid.WLANID + "/" + ssid.ID
				rows[i] = table.Row{Key: key, Cells: []table.Cell{
					table.Text(ssid.Name), table.Text(ssid.SecurityText()), table.Text(output.OrNone(ssid.BandText())),
					table.Text(output.YesNo(ssid.Guest)), table.Text(output.YesNo(ssid.Hidden())),
				}}
				items[key] = ssid
			}
			return rows, items, nil
		},
	}
}

func alertsTab() *tab {
	return &tab{
		title:      "Alerts",
		background: true,
		badge: func(t *tab) string {
			unresolved := 0
			for _, item := range t.items {
				if !item.(omada.Alert).Resolved {
					unresolved++
				}
			}
			if unresolved == 0 {
				return ""
			}
			return fmt.Sprintf(" (%d)", unresolved)
		},
		list: list{
			noun: "alert",
			table: table.New([]table.Column{
				{Title: ""}, {Title: "TIME"}, {Title: "LEVEL"}, {Title: "MESSAGE", MaxWidth: 160},
			}, 1),
			details: func(_ *Model, item any) (string, []output.Field) {
				return "Alert", output.AlertFields(item.(omada.Alert))
			},
		},
		load: func(ctx context.Context, s *app.Session, siteID string) ([]table.Row, map[string]any, error) {
			alerts, err := s.Alerts(ctx, siteID)
			if err != nil {
				return nil, nil, err
			}
			rows := make([]table.Row, len(alerts))
			items := make(map[string]any, len(alerts))
			for i, a := range alerts {
				symbol := symbolOnline // unresolved: needs attention
				if a.Resolved {
					symbol = symbolOffline
				}
				// The same ID could come back from both of the lists the
				// alerts are read from, so the state is part of the key.
				key := fmt.Sprintf("%s/%t", a.ID, a.Resolved)
				rows[i] = table.Row{Key: key, Dim: a.Resolved, Cells: []table.Cell{
					table.Text(symbol), table.Num(output.Timestamp(a.Time()), float64(a.Millis)),
					table.Text(output.OrNone(a.Level)), table.Text(a.Content),
				}}
				items[key] = a
			}
			return rows, items, nil
		},
	}
}
