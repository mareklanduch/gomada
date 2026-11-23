package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/mareklanduch/gomada/internal/app"
	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/output"
	"github.com/mareklanduch/gomada/internal/secret"
	"github.com/mareklanduch/gomada/internal/tui/table"
)

// screen is what fills the terminal.
type screen int

const (
	screenLoading screen = iota // waiting for a controller
	screenHosts                 // the saved hosts
	screenSites                 // the sites of the open host
	screenMain                  // the tabs of the open site
	screenWizard                // adding a host
)

// overlay is what temporarily takes the keyboard on top of a screen.
type overlay int

const (
	overlayNone    overlay = iota
	overlayFilter          // typing a filter
	overlayPrompt          // typing an answer
	overlayConfirm         // answering yes or no
	overlayDetails         // reading the details of a row
	overlayHelp            // reading the key list
)

// Messages that bring results of background work back to the model.
type (
	// connectedMsg ends an attempt to open a host.
	connectedMsg struct {
		host  string
		sess  *app.Session
		sites []omada.Site
		err   error
	}
	// rowsMsg carries the freshly loaded rows of a tab.
	rowsMsg struct {
		tab    *tab
		sess   *app.Session
		siteID string
		rows   []table.Row
		items  map[string]any
		err    error
	}
	// actionMsg ends an action on a row.
	actionMsg struct {
		done string
		err  error
	}
	// tickMsg asks for the periodic reload.
	tickMsg struct{}
)

// Model is the Bubble Tea model of the terminal UI.
type Model struct {
	ctx     context.Context
	app     *app.App
	refresh time.Duration
	pass    *passphrases

	wantHost string // host to open at start, from the command line
	wantSite string // site to open at start, from the command line
	allSites bool   // show the site list even when one site would be chosen

	width, height int
	screen        screen
	overlay       overlay

	hosts list
	sites list
	tabs  []*tab
	tab   int

	sess       *app.Session
	site       omada.Site
	connecting string // host being opened
	busy       string // what the loading screen says

	status    string
	statusErr bool

	// Text prompt and yes/no question state.
	input       lineInput
	inputLabel  string
	submit      func(text string) tea.Cmd
	cancel      func() tea.Cmd
	question    string
	answerYes   func() tea.Cmd
	answerNo    func() tea.Cmd
	detailTitle string
	detail      []output.Field
	detailTop   int

	wiz wizard
}

// New returns the model. A refresh of zero or less turns the periodic reload
// off, which tests rely on.
func New(ctx context.Context, a *app.App, opts Options, refresh time.Duration) *Model {
	pass := &passphrases{byHost: map[string]string{}}
	a.SetPassphrase(pass.lookup)
	return &Model{
		ctx: ctx, app: a, refresh: refresh, pass: pass,
		wantHost: opts.Host, wantSite: opts.Site,
		hosts: hostsList(), sites: sitesList(),
		tabs: []*tab{devicesTab(), clientsTab()},
	}
}

// Init decides the first screen: the wizard without hosts, the only host
// directly, or the host list when there are several.
func (m *Model) Init() tea.Cmd {
	hosts := m.loadHosts()
	var first tea.Cmd
	switch {
	case len(hosts) == 0:
		m.startWizard()
	case m.wantHost != "":
		first = m.connect(m.wantHost)
	case len(hosts) == 1:
		first = m.connect(hosts[0].Name)
	default:
		m.screen = screenHosts
		m.hosts.table.Select(m.app.CurrentHost())
	}
	return tea.Batch(first, m.tick())
}

func (m *Model) tick() tea.Cmd {
	if m.refresh <= 0 {
		return nil
	}
	return tea.Tick(m.refresh, func(time.Time) tea.Msg { return tickMsg{} })
}

// loadHosts refreshes the host list from the configuration.
func (m *Model) loadHosts() []app.HostInfo {
	hosts := m.app.Hosts()
	rows, items := hostRows(hosts)
	m.hosts.table.SetRows(rows)
	m.hosts.items = items
	m.hosts.loaded = true
	return hosts
}

// current returns the list on screen, or nil when the screen has none.
func (m *Model) current() *list {
	switch m.screen {
	case screenHosts:
		return &m.hosts
	case screenSites:
		return &m.sites
	case screenMain:
		return &m.tabs[m.tab].list
	}
	return nil
}

// Update handles one message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyPressMsg:
		return m, m.key(msg)
	case tea.PasteMsg:
		if m.overlay == overlayPrompt || m.overlay == overlayFilter {
			m.input.paste(msg.Content)
			m.applyFilter()
		}
		return m, nil
	case connectedMsg:
		return m, m.connected(msg)
	case rowsMsg:
		m.loadedRows(msg)
		return m, nil
	case actionMsg:
		return m, m.actionDone(msg)
	case tickMsg:
		var reload tea.Cmd
		if m.screen == screenMain && m.overlay == overlayNone && !m.tabs[m.tab].loading {
			reload = m.loadTab()
		}
		return m, tea.Batch(reload, m.tick())
	case certMsg:
		m.wizCertChecked(msg)
		return m, nil
	case addedMsg:
		return m, m.wizAdded(msg)
	}
	return m, nil
}

// setStatus shows a message in the bottom line until the next key press.
func (m *Model) setStatus(text string) { m.status, m.statusErr = text, false }

func (m *Model) setError(err error) { m.status, m.statusErr = err.Error(), true }

// ask opens a text prompt. submit gets the trimmed answer; cancel runs when
// the person presses esc and may be nil.
func (m *Model) ask(label, initial string, masked bool, submit func(string) tea.Cmd, cancel func() tea.Cmd) {
	m.overlay = overlayPrompt
	m.inputLabel = label
	m.input = newInput(initial, masked)
	m.submit, m.cancel = submit, cancel
}

// confirm asks a yes/no question. Only y runs yes; every other key runs no,
// which may be nil.
func (m *Model) confirm(question string, yes, no func() tea.Cmd) {
	m.overlay = overlayConfirm
	m.question = question
	m.answerYes, m.answerNo = yes, no
}

// key handles a key press. Overlays get the key first, then the screen.
func (m *Model) key(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if key == "ctrl+c" {
		return tea.Quit
	}
	switch m.overlay {
	case overlayHelp, overlayDetails:
		switch key {
		case "up", "k":
			m.detailTop--
		case "down", "j":
			m.detailTop++
		default:
			m.overlay = overlayNone
		}
		return nil
	case overlayConfirm:
		m.overlay = overlayNone
		yes, no := m.answerYes, m.answerNo
		m.answerYes, m.answerNo = nil, nil
		if key == "y" || key == "Y" {
			return yes()
		}
		if no != nil {
			return no()
		}
		m.setStatus("Canceled.")
		return nil
	case overlayFilter:
		switch key {
		case "enter":
			m.overlay = overlayNone
		case "esc":
			m.overlay = overlayNone
			m.input = lineInput{}
			m.applyFilter()
		default:
			if m.input.key(msg) {
				m.applyFilter()
			}
		}
		return nil
	case overlayPrompt:
		switch key {
		case "enter":
			m.overlay = overlayNone
			submit := m.submit
			m.submit, m.cancel = nil, nil
			text := m.input.value()
			if !m.input.masked {
				text = strings.TrimSpace(text)
			}
			return submit(text)
		case "esc":
			m.overlay = overlayNone
			cancel := m.cancel
			m.submit, m.cancel = nil, nil
			if cancel != nil {
				return cancel()
			}
			m.setStatus("Canceled.")
		default:
			m.input.key(msg)
		}
		return nil
	}

	m.status = ""
	switch m.screen {
	case screenWizard:
		if key == "esc" { // only reachable while the wizard is working
			return m.wizCancel()
		}
		return nil
	case screenLoading:
		switch key {
		case "q":
			return tea.Quit
		case "esc", "H":
			m.connecting = ""
			m.showHosts()
		}
		return nil
	}
	return m.listKey(key)
}

// applyFilter makes the table follow the filter being typed.
func (m *Model) applyFilter() {
	if l := m.current(); l != nil && m.overlay != overlayPrompt {
		l.table.SetFilter(m.input.value())
	}
}

// listKey handles a key on one of the list screens.
func (m *Model) listKey(key string) tea.Cmd {
	l := m.current()
	switch key {
	case "q":
		return tea.Quit
	case "up", "k":
		l.table.Move(-1)
	case "down", "j":
		l.table.Move(1)
	case "pgup":
		l.table.Page(-1)
	case "pgdown":
		l.table.Page(1)
	case "g", "home":
		l.table.Top()
	case "G", "end":
		l.table.Bottom()
	case "left", "h":
		l.table.ScrollLeft()
	case "right", "l":
		l.table.ScrollRight()
	case "/":
		m.overlay = overlayFilter
		m.input = newInput(l.table.Filter(), false)
	case "o":
		l.table.CycleSort()
		if col := l.table.SortColumn(); col != "" {
			m.setStatus("Sorted by " + strings.ToLower(col) + ".")
		} else {
			m.setStatus("Back to the usual order.")
		}
	case "?":
		m.overlay = overlayHelp
		m.detailTop = 0
	case "esc":
		switch {
		case l.table.Filter() != "":
			l.table.SetFilter("")
		case m.screen != screenMain && m.site.ID != "":
			m.screen = screenMain
		}
	case "H":
		m.showHosts()
	case "S":
		if m.sess != nil {
			m.screen = screenSites
		}
	case "tab", "shift+tab":
		if m.screen == screenMain {
			step := 1
			if key == "shift+tab" {
				step = len(m.tabs) - 1
			}
			m.tab = (m.tab + step) % len(m.tabs)
			return m.loadTab()
		}
	case "R":
		return m.reload()
	case "enter":
		return m.open()
	case "a":
		if m.screen == screenHosts {
			m.startWizard()
			return nil
		}
		return m.runActionKey(l, key)
	default:
		return m.runActionKey(l, key)
	}
	return nil
}

func (m *Model) showHosts() {
	m.loadHosts()
	m.screen = screenHosts
	if m.sess != nil {
		m.hosts.table.Select(m.sess.Host())
	}
}

// reload fetches the current screen's rows again.
func (m *Model) reload() tea.Cmd {
	switch m.screen {
	case screenHosts:
		m.loadHosts()
	case screenSites:
		m.allSites = true
		return m.connect(m.sess.Host())
	case screenMain:
		return m.loadTab()
	}
	return nil
}

// open acts on enter: it opens the selected host or site, or shows the
// details of the selected row.
func (m *Model) open() tea.Cmd {
	l := m.current()
	item, ok := l.selected()
	if !ok {
		return nil
	}
	switch item := item.(type) {
	case app.HostInfo:
		return m.connect(item.Name)
	case omada.Site:
		return m.openSite(item)
	}
	if l.details != nil {
		m.detailTitle, m.detail = l.details(item)
		m.detailTop = 0
		m.overlay = overlayDetails
	}
	return nil
}

// runActionKey starts the action bound to key on the selected row: it asks
// for confirmation or input first when the action wants that.
func (m *Model) runActionKey(l *list, key string) tea.Cmd {
	for _, a := range l.actions {
		if a.key != key {
			continue
		}
		item, ok := l.selected()
		if !ok {
			m.setStatus("Select a " + l.noun + " first.")
			return nil
		}
		run := func(input string) tea.Cmd {
			m.setStatus("Working…")
			ctx, t := m.ctx, target{app: m.app, sess: m.sess, siteID: m.site.ID}
			return func() tea.Msg {
				done, err := a.run(ctx, t, item, input)
				return actionMsg{done: done, err: err}
			}
		}
		switch {
		case a.prompt != nil:
			label, initial := a.prompt(item)
			m.ask(label, initial, false, func(text string) tea.Cmd {
				if text == "" {
					m.setStatus("Canceled: nothing was entered.")
					return nil
				}
				return run(text)
			}, nil)
		case a.confirm != nil:
			m.confirm(a.confirm(item), func() tea.Cmd { return run("") }, nil)
		default:
			return run("")
		}
		return nil
	}
	return nil
}

func (m *Model) actionDone(msg actionMsg) tea.Cmd {
	if msg.err != nil {
		m.setError(msg.err)
		return nil
	}
	m.setStatus(msg.done)
	if m.screen == screenHosts {
		hosts := m.loadHosts()
		if m.sess != nil && !hasHost(hosts, m.sess.Host()) {
			m.sess, m.site = nil, omada.Site{} // the open host was removed
		}
		if len(hosts) == 0 {
			m.startWizard()
		}
		return nil
	}
	return m.reload()
}

func hasHost(hosts []app.HostInfo, name string) bool {
	for _, h := range hosts {
		if h.Name == name {
			return true
		}
	}
	return false
}

// connect opens a host: it unlocks the secret, logs in if needed, and
// fetches the sites. The secret is unlocked right away so that renewing the
// token later never has to stop and ask for a passphrase.
func (m *Model) connect(host string) tea.Cmd {
	m.screen = screenLoading
	m.connecting = host
	m.busy = "Connecting to " + host + "…"
	ctx, a := m.ctx, m.app
	return func() tea.Msg {
		sess, err := a.Connect(host)
		if err != nil {
			return connectedMsg{host: host, err: err}
		}
		if err := sess.Unlock(); err != nil {
			return connectedMsg{host: host, err: err}
		}
		sites, err := sess.Sites(ctx)
		return connectedMsg{host: host, sess: sess, sites: sites, err: err}
	}
}

func (m *Model) connected(msg connectedMsg) tea.Cmd {
	if msg.host != m.connecting {
		return nil // an answer nobody is waiting for any more
	}
	m.connecting = ""
	host := msg.host
	switch {
	case errors.Is(msg.err, secret.ErrPassphraseRequired), errors.Is(msg.err, secret.ErrWrongPassphrase):
		if errors.Is(msg.err, secret.ErrWrongPassphrase) {
			m.pass.forget(host)
			m.setError(msg.err)
		}
		m.busy = "The client secret of " + host + " is encrypted with a passphrase."
		m.ask("Passphrase for "+host, "", true, func(pass string) tea.Cmd {
			if pass == "" {
				m.showHosts()
				return nil
			}
			m.pass.set(host, pass)
			return m.connect(host)
		}, func() tea.Cmd {
			m.showHosts()
			return nil
		})
		return nil
	case msg.err != nil:
		m.showHosts()
		m.hosts.table.Select(host)
		m.setError(fmt.Errorf("could not open %s: %w", host, msg.err))
		return nil
	}

	m.sess = msg.sess
	m.site = omada.Site{}
	rows, items := siteRows(msg.sites)
	m.sites.table.SetRows(rows)
	m.sites.items = items
	m.sites.loaded = true

	want, all := m.wantSite, m.allSites
	m.wantSite, m.allSites = "", false
	site, err := m.sess.ChooseSite(msg.sites, want)
	if err != nil || all {
		m.screen = screenSites
		if err != nil && !errors.Is(err, app.ErrSiteRequired) {
			m.setError(err)
		}
		return nil
	}
	return m.openSite(site)
}

// openSite shows the tabs of a site and remembers it as the last used one.
func (m *Model) openSite(site omada.Site) tea.Cmd {
	m.site = site
	m.screen = screenMain
	m.sites.table.Select(site.ID)
	for _, t := range m.tabs {
		t.table.SetRows(nil)
		t.table.SetFilter("")
		t.items, t.loaded, t.loading, t.err = nil, false, false, nil
	}
	sess := m.sess
	remember := func() tea.Msg {
		_ = sess.RememberSite(site) // not worth interrupting anyone for
		return nil
	}
	return tea.Batch(m.loadTab(), remember)
}

// loadTab fetches the rows of the open tab in the background.
func (m *Model) loadTab() tea.Cmd {
	t := m.tabs[m.tab]
	t.loading = true
	ctx, sess, siteID := m.ctx, m.sess, m.site.ID
	return func() tea.Msg {
		rows, items, err := t.load(ctx, sess, siteID)
		return rowsMsg{tab: t, sess: sess, siteID: siteID, rows: rows, items: items, err: err}
	}
}

func (m *Model) loadedRows(msg rowsMsg) {
	if msg.sess != m.sess || msg.siteID != m.site.ID {
		return // rows of a host or site that is no longer open
	}
	t := msg.tab
	t.loading = false
	t.err = msg.err
	if msg.err != nil {
		if t.loaded {
			m.setError(msg.err) // keep showing the rows we have
		}
		return
	}
	t.table.SetRows(msg.rows)
	t.items = msg.items
	t.loaded = true
}

// View draws the whole terminal: a header line, the body, and a bottom line.
func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

var (
	styleHeader = lipgloss.NewStyle().Bold(true)
	styleFaint  = lipgloss.NewStyle().Faint(true)
	styleError  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Red)
	styleTitle  = lipgloss.NewStyle().Bold(true).Underline(true)
)

func (m *Model) render() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	bodyHeight := max(m.height-2, 0)
	lines := make([]string, 0, m.height)
	lines = append(lines, styleHeader.Render(cut(m.header(), m.width)))
	if m.height > 1 {
		body := m.body(bodyHeight)
		for len(body) < bodyHeight {
			body = append(body, "")
		}
		lines = append(lines, body[:bodyHeight]...)
		lines = append(lines, m.footer())
	}
	return strings.Join(lines, "\n")
}

// header names where the person is, and on list screens where the cursor is.
func (m *Model) header() string {
	var left, right string
	switch m.screen {
	case screenLoading:
		left = "gomada"
	case screenWizard:
		left = "gomada › add a controller"
	case screenHosts:
		left = "Hosts"
	case screenSites:
		left = m.sess.Host() + " › sites"
	case screenMain:
		left = m.sess.Host() + " › " + m.site.Name + " "
		for i, t := range m.tabs {
			if i == m.tab {
				left += " [" + t.title + "]"
			} else {
				left += "  " + t.title + " "
			}
		}
	}
	if l := m.current(); l != nil {
		right = fmt.Sprintf("%d/%d", l.table.Position(), l.table.Len())
		if l.loading {
			right = "… " + right
		}
	}
	space := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if space < 1 {
		return left
	}
	return left + strings.Repeat(" ", space) + right
}

func (m *Model) body(height int) []string {
	switch m.overlay {
	case overlayHelp:
		return m.scrolled(m.helpLines(), height)
	case overlayDetails:
		return m.scrolled(m.detailLines(), height)
	}
	switch m.screen {
	case screenLoading:
		return m.wrapAll([]string{"", m.busy})
	case screenWizard:
		return m.wizardLines()
	}

	l := m.current()
	l.table.SetSize(m.width, height)
	lines := strings.Split(l.table.View(), "\n")
	var note string
	switch {
	case l.err != nil && !l.loaded:
		note = styleError.Render("Could not load the "+l.noun+"s: "+l.err.Error()) + "\nPress R to try again."
	case !l.loaded:
		note = "Loading…"
	case l.table.Total() == 0 && m.screen == screenHosts:
		note = "No hosts yet. Press a to add one."
	case l.table.Total() == 0:
		note = "No " + l.noun + "s here."
	case l.table.Len() == 0:
		note = "No " + l.noun + " matches the filter. Press esc to clear it."
	}
	if note != "" {
		lines = append(lines[:1], m.wrapAll([]string{note})...)
	}
	return lines
}

// wrapAll wraps each paragraph to the width of the terminal.
func (m *Model) wrapAll(paragraphs []string) []string {
	var lines []string
	for _, p := range paragraphs {
		lines = append(lines, strings.Split(lipgloss.NewStyle().Width(m.width).Render(p), "\n")...)
	}
	return lines
}

// scrolled returns the part of lines that starts at the scroll position of
// the open details or help text.
func (m *Model) scrolled(lines []string, height int) []string {
	m.detailTop = max(min(m.detailTop, len(lines)-height), 0)
	return lines[m.detailTop:]
}

func (m *Model) detailLines() []string {
	labelWidth := 0
	for _, f := range m.detail {
		labelWidth = max(labelWidth, lipgloss.Width(f.Label))
	}
	lines := []string{styleTitle.Render(cut(m.detailTitle, m.width)), ""}
	for _, f := range m.detail {
		pad := strings.Repeat(" ", labelWidth-lipgloss.Width(f.Label))
		lines = append(lines, cut(styleFaint.Render(f.Label+pad)+"  "+f.Value, m.width))
	}
	return lines
}

func (m *Model) helpLines() []string {
	keys := [][2]string{
		{"↑ ↓  k j", "move"},
		{"← →  h l", "scroll the table sideways"},
		{"g G", "first and last row"},
		{"enter", "open or show details"},
		{"/", "filter by text in any column"},
		{"o", "sort by the next column"},
		{"R", "reload"},
		{"tab", "next tab"},
		{"S", "sites of this host"},
		{"H", "hosts"},
		{"esc", "clear the filter, go back"},
		{"q", "quit"},
	}
	if l := m.current(); l != nil {
		for i, a := range l.actions {
			keys = append(keys[:4+i], append([][2]string{{a.key, a.label + " the selected " + l.noun}}, keys[4+i:]...)...)
		}
	}
	if m.screen == screenHosts {
		keys = append(keys[:4], append([][2]string{{"a", "add a host"}}, keys[4:]...)...)
	}
	lines := []string{styleTitle.Render("Keys"), ""}
	for _, k := range keys {
		lines = append(lines, cut(fmt.Sprintf("%-9s %s", k[0], k[1]), m.width))
	}
	return lines
}

// footer is the bottom line: an open prompt, else the latest message, else
// the key hints.
func (m *Model) footer() string {
	switch m.overlay {
	case overlayFilter:
		return cutLeft("/"+m.input.view(), m.width)
	case overlayPrompt:
		return cutLeft(m.inputLabel+": "+m.input.view(), m.width)
	case overlayConfirm:
		return styleHeader.Render(cutLeft(m.question+" y/N", m.width))
	case overlayDetails, overlayHelp:
		return styleFaint.Render(cut("any key back · ↑↓ scroll", m.width))
	}
	if m.status != "" {
		if m.statusErr {
			return styleError.Render(cut("Error: "+m.status, m.width))
		}
		return cut(m.status, m.width)
	}
	return styleFaint.Render(cut(m.hints(), m.width))
}

// hints lists the most useful keys first, because a narrow terminal cuts the
// line off.
func (m *Model) hints() string {
	var hints []string
	l := m.current()
	if l != nil && l.table.Filter() != "" {
		hints = append(hints, "filter: "+l.table.Filter()+" (esc clears)")
	}
	switch m.screen {
	case screenLoading:
		return "esc hosts · q quit"
	case screenWizard:
		return "esc cancel"
	case screenHosts:
		hints = append(hints, "enter open", "a add")
	case screenSites:
		hints = append(hints, "enter open")
	case screenMain:
		hints = append(hints, "enter details")
	}
	for _, a := range l.actions {
		hints = append(hints, a.key+" "+a.label)
	}
	hints = append(hints, "? help", "/ filter")
	if m.screen == screenMain {
		hints = append(hints, "tab "+strings.ToLower(m.tabs[(m.tab+1)%len(m.tabs)].title), "S sites")
	}
	if m.screen != screenHosts {
		hints = append(hints, "H hosts")
	}
	return strings.Join(append(hints, "q quit"), " · ")
}

// cut shortens a line to the width of the terminal.
func cut(s string, width int) string { return ansi.Truncate(s, width, "…") }

// cutLeft shortens a line from the left, so the end, where the person is
// typing, stays visible.
func cutLeft(s string, width int) string {
	if over := lipgloss.Width(s) - width; over > 0 {
		return "…" + ansi.TruncateLeft(s, over+1, "")
	}
	return s
}
