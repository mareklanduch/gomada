package tui

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/zalando/go-keyring"

	"github.com/mareklanduch/gomada/internal/app"
	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/omada/omadatest"
	"github.com/mareklanduch/gomada/internal/secret"
)

var update = flag.Bool("update", false, "rewrite the golden files with the current screens")

// harness drives a Model the way Bubble Tea would, but synchronously: each
// command runs to completion and its message is fed straight back in. That
// makes every test deterministic.
type harness struct {
	t    *testing.T
	fake *omadatest.Controller
	path string
	app  *app.App
	m    *Model
	quit bool
}

// newHarness prepares a fake controller, an empty config, and an in-memory
// keychain, and adds the named hosts.
func newHarness(t *testing.T, hosts ...string) *harness {
	t.Helper()
	keyring.MockInit()
	t.Setenv(secret.EnvVar, "")
	t.Setenv(secret.EnvPassphrase, "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	h := &harness{t: t, fake: omadatest.New(t), path: filepath.Join(t.TempDir(), "config.yaml")}
	h.app = h.newApp()
	for _, name := range hosts {
		err := h.app.AddHost(t.Context(), app.NewHost{Name: name, URL: h.fake.URL, ClientID: omadatest.ClientID, Secret: omadatest.Secret})
		if err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func (h *harness) newApp() *app.App {
	h.t.Helper()
	a, err := app.New(app.Options{ConfigPath: h.path})
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

// start creates the model at the given terminal size and runs its first
// commands.
func (h *harness) start(width, height int, opts Options) *harness {
	h.t.Helper()
	h.m = New(h.t.Context(), h.app, opts, 0)
	h.send(tea.WindowSizeMsg{Width: width, Height: height})
	h.run(h.m.Init())
	return h
}

func (h *harness) send(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	h.run(cmd)
}

func (h *harness) run(cmd tea.Cmd) {
	h.t.Helper()
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			h.run(c)
		}
	case tea.QuitMsg:
		h.quit = true
	default:
		h.send(msg)
	}
}

// keyMsg builds the message for a key given by name ("enter", "esc", "tab",
// "shift+tab", "up", ..., "ctrl+c") or as a single character.
func keyMsg(key string) tea.KeyPressMsg {
	special := map[string]tea.KeyPressMsg{
		"enter":     {Code: tea.KeyEnter},
		"esc":       {Code: tea.KeyEscape},
		"tab":       {Code: tea.KeyTab},
		"shift+tab": {Code: tea.KeyTab, Mod: tea.ModShift},
		"backspace": {Code: tea.KeyBackspace},
		"up":        {Code: tea.KeyUp},
		"down":      {Code: tea.KeyDown},
		"left":      {Code: tea.KeyLeft},
		"right":     {Code: tea.KeyRight},
		"pgup":      {Code: tea.KeyPgUp},
		"pgdown":    {Code: tea.KeyPgDown},
		"home":      {Code: tea.KeyHome},
		"end":       {Code: tea.KeyEnd},
		"ctrl+c":    {Code: 'c', Mod: tea.ModCtrl},
		"ctrl+u":    {Code: 'u', Mod: tea.ModCtrl},
	}
	if msg, ok := special[key]; ok {
		return msg
	}
	r := []rune(key)
	if len(r) != 1 {
		panic("unknown key " + key)
	}
	msg := tea.KeyPressMsg{Code: r[0], Text: key}
	if lower := []rune(strings.ToLower(key)); lower[0] != r[0] {
		msg = tea.KeyPressMsg{Code: lower[0], ShiftedCode: r[0], Mod: tea.ModShift, Text: key}
	}
	return msg
}

// press sends keys by name.
func (h *harness) press(keys ...string) *harness {
	h.t.Helper()
	for _, k := range keys {
		h.send(keyMsg(k))
	}
	return h
}

// typeText sends each character of text as a key press.
func (h *harness) typeText(text string) *harness {
	h.t.Helper()
	for _, r := range text {
		h.send(keyMsg(string(r)))
	}
	return h
}

// screen returns what is drawn, without styling.
func (h *harness) screen() string {
	return ansi.Strip(h.m.View().Content)
}

func (h *harness) lines() []string { return strings.Split(h.screen(), "\n") }

// footer returns the bottom line.
func (h *harness) footer() string {
	lines := h.lines()
	return lines[len(lines)-1]
}

func (h *harness) wantScreen(wants ...string) {
	h.t.Helper()
	screen := h.screen()
	for _, want := range wants {
		if !strings.Contains(screen, want) {
			h.t.Errorf("the screen does not show %q:\n%s", want, screen)
		}
	}
}

func (h *harness) wantNotOnScreen(texts ...string) {
	h.t.Helper()
	screen := h.screen()
	for _, text := range texts {
		if strings.Contains(screen, text) {
			h.t.Errorf("the screen shows %q but must not:\n%s", text, screen)
		}
	}
}

func (h *harness) wantFooter(want string) {
	h.t.Helper()
	if got := h.footer(); !strings.Contains(got, want) {
		h.t.Errorf("bottom line = %q, want it to contain %q", got, want)
	}
}

// golden compares the screen with a file in testdata. Trailing spaces are
// dropped so the files stay readable.
func (h *harness) golden(name string) {
	h.t.Helper()
	lines := h.lines()
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	got := strings.Join(lines, "\n") + "\n"
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			h.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatalf("%v (run the tests with -update to create it)", err)
	}
	if got != string(want) {
		h.t.Errorf("screen differs from %s (run the tests with -update if the change is intended)\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}

// oneSite leaves the fake controller with a single site, so the UI opens it
// without asking.
func (h *harness) oneSite() *harness {
	h.fake.SetSites(omadatest.Record{"siteId": "site-1", "name": "Default"})
	return h
}

func TestKeyNames(t *testing.T) {
	// The model matches keys by these names; make sure the test helper and
	// Bubble Tea agree on them.
	for _, key := range []string{
		"enter", "esc", "tab", "shift+tab", "backspace", "up", "down", "left", "right",
		"pgup", "pgdown", "home", "end", "ctrl+c", "ctrl+u", "a", "H", "R", "/", "?",
	} {
		if got := keyMsg(key).String(); got != key {
			t.Errorf("keyMsg(%q).String() = %q", key, got)
		}
	}
}

func TestOneHostOneSiteOpensDevices(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(100, 12, Options{})
	h.golden("devices-100x12")
	if h.quit {
		t.Error("the program quit")
	}
}

func TestSmallScreenScrollsSideways(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(34, 8, Options{})
	h.golden("devices-34x8")
	h.press("l")
	h.golden("devices-34x8-right")
	h.press("right", "l", "l", "l", "l", "l", "l", "l", "l") // far past the last column
	h.wantScreen("‹    NAME         MAC", "Core router  AA-00-00-00-00-0")
	h.press("h", "left", "h", "h", "h", "h", "h", "h", "h", "h")
	h.golden("devices-34x8")
}

func TestClientsTab(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(100, 12, Options{})
	h.press("tab")
	h.golden("clients-100x12")
	h.press("tab")
	h.wantScreen("[Devices]", "Core router")
	h.press("shift+tab")
	h.wantScreen("[Clients]", "Laptop")
}

func TestDetailsAndHelp(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(60, 16, Options{})
	h.press("enter")
	h.golden("device-details-60x16")
	h.press("x") // any key goes back
	h.wantScreen("[Devices]", "Rack switch")

	h.press("?")
	h.golden("help-60x16")
	h.wantNotOnScreen("quit")
	h.press("j", "j", "j", "j", "j") // the key list scrolls when it does not fit
	h.wantScreen("quit")
	h.press("esc")
	h.wantScreen("[Devices]", "Rack switch")
}

// TestRealProgram runs the model inside Bubble Tea itself, with a scripted
// keyboard, to check the wiring the other tests bypass.
func TestRealProgram(t *testing.T) {
	h := newHarness(t, "home").oneSite()
	var out strings.Builder
	p := tea.NewProgram(New(t.Context(), h.app, Options{}, 0),
		tea.WithInput(strings.NewReader("q")), tea.WithOutput(&out), tea.WithWindowSize(80, 24))
	if _, err := p.Run(); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		t.Error("the program drew nothing")
	}
}

func TestDetailsScrollOnSmallScreen(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(40, 6, Options{})
	h.press("enter")
	h.wantScreen("Core router", "Status")
	h.wantNotOnScreen("Memory")
	h.press("j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j")
	h.wantScreen("Memory")
	h.press("k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k")
	h.wantScreen("Status")
}

func TestFirstRunWizard(t *testing.T) {
	h := newHarness(t).start(80, 20, Options{})
	h.wantScreen("add a controller", "Pick a short name")
	h.wantFooter("Name: █")

	h.typeText("my host").press("enter")
	h.wantScreen("Error: invalid host name")
	h.press("ctrl+u").typeText("home").press("enter")

	h.wantFooter("Address: █")
	h.typeText("ftp://x").press("enter")
	h.wantScreen("Error: invalid controller address")
	h.press("ctrl+u").typeText(h.fake.URL + "/login").press("enter")

	h.wantScreen("Name: home", "Address: "+h.fake.URL, "Create an Open API application")
	h.wantFooter("Client ID: █")
	h.press("enter")
	h.wantScreen("Error: The client ID must not be empty.")
	h.typeText(omadatest.ClientID).press("enter")

	h.wantFooter("Client secret: █")
	h.typeText("wrong")
	h.wantFooter("Client secret: •••••█")
	h.wantNotOnScreen("wrong")
	h.press("enter")
	h.wantScreen("Error: authentication failed")
	h.wantFooter("Client ID: " + omadatest.ClientID + "█") // back at the client ID, kept

	h.press("enter")
	h.send(tea.PasteMsg{Content: omadatest.Secret + "\n"}) // pasting works too
	h.wantNotOnScreen(omadatest.Secret)
	h.press("enter")

	// Two sites: the site list comes up.
	h.wantScreen("home › sites", "Default", "Workshop")
	h.press("j", "enter")
	h.wantScreen("home › Workshop", "Workshop AP")

	if got, _ := keyring.Get("gomada", "home"); got != omadatest.Secret {
		t.Errorf("keychain holds %q", got)
	}
	hosts := h.newApp().Hosts()
	if len(hosts) != 1 || hosts[0].Name != "home" || hosts[0].URL != h.fake.URL || hosts[0].Site != "Workshop" {
		t.Errorf("saved hosts = %+v", hosts)
	}
}

func TestWizardCancelWithoutHostsQuits(t *testing.T) {
	h := newHarness(t).start(80, 20, Options{})
	h.press("esc")
	if !h.quit {
		t.Error("esc in the first-run wizard must quit")
	}
}

func TestWizardUnreachableController(t *testing.T) {
	h := newHarness(t).start(80, 20, Options{})
	url := h.fake.URL
	h.fake.Close()
	h.typeText("home").press("enter").typeText(url).press("enter")
	h.typeText("id").press("enter").typeText("secret").press("enter")
	h.wantScreen("Error: could not read the controller ID")
	h.wantFooter("Address: " + url + "█")
}

func TestWizardSelfSignedCertificate(t *testing.T) {
	h := newHarness(t)
	h.fake = omadatest.NewTLS(t)
	fp := omada.Fingerprint(h.fake.Certificate())
	h.start(100, 24, Options{})

	h.typeText("home").press("enter").typeText(h.fake.URL).press("enter")
	h.wantScreen("self-signed", fp)
	h.wantFooter("Trust this certificate? y/N")
	h.press("n")
	h.wantScreen("Error: The certificate was not trusted.")
	h.wantFooter("Address: ")

	h.press("enter", "y")
	h.typeText(omadatest.ClientID).press("enter").typeText(omadatest.Secret).press("enter")
	h.wantScreen("home › sites")

	raw, err := os.ReadFile(h.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "tls_pin: "+fp) {
		t.Errorf("the pin was not saved:\n%s", raw)
	}
}

func TestWizardWithoutKeychain(t *testing.T) {
	h := newHarness(t)
	keyring.MockInitWithError(errors.New("no secret service"))
	h.start(80, 20, Options{})
	h.typeText("home").press("enter").typeText(h.fake.URL).press("enter")
	h.typeText(omadatest.ClientID).press("enter").typeText(omadatest.Secret).press("enter")

	h.wantScreen("no keychain", "encrypted with a passphrase")
	h.wantFooter("Choose a passphrase: █")
	h.press("enter")
	h.wantScreen("Error: The passphrase must not be empty.")
	h.typeText("pw").press("enter")
	h.wantFooter("Repeat the passphrase: █")
	h.typeText("pW").press("enter")
	h.wantScreen("Error: The two passphrases differ.")
	h.typeText("pw").press("enter").typeText("pw").press("enter")

	// Connected without being asked again: the passphrase is remembered
	// for this run.
	h.wantScreen("home › sites")
	if hosts := h.newApp().Hosts(); len(hosts) != 1 || hosts[0].SecretStore != secret.KindFile {
		t.Errorf("saved hosts = %+v", hosts)
	}
}

func TestPassphrasePromptOnConnect(t *testing.T) {
	h := newHarness(t)
	keyring.MockInitWithError(errors.New("no secret service"))
	h.app.SetPassphrase(func(string) (string, error) { return "pw", nil })
	err := h.app.AddHost(t.Context(), app.NewHost{
		Name: "home", URL: h.fake.URL, ClientID: omadatest.ClientID, Secret: omadatest.Secret, SecretStore: secret.KindFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	h.app = h.newApp()
	h.start(80, 20, Options{})

	h.wantScreen("encrypted with a passphrase")
	h.wantFooter("Passphrase for home: █")
	h.typeText("nope").press("enter")
	h.wantFooter("Passphrase for home: █") // asked again
	h.typeText("pw")
	h.wantFooter("Passphrase for home: ••█")
	h.press("enter")
	h.wantScreen("home › sites")

	// The token renewal later in the session must not ask again.
	h.press("enter")
	h.fake.ExpireTokens()
	h.press("R")
	h.wantScreen("Core router")
	h.wantFooter("enter details")

	t.Run("esc goes to the host list", func(*testing.T) {
		h.app = h.newApp()
		h.start(80, 20, Options{})
		h.press("esc")
		h.wantScreen("Hosts", "home")
	})
	t.Run("passphrase from the environment", func(t *testing.T) {
		t.Setenv(secret.EnvPassphrase, "pw")
		h.app = h.newApp()
		h.start(80, 20, Options{})
		h.wantScreen("home › Default") // the site opened above is remembered
	})
}

func TestSeveralHostsShowHostList(t *testing.T) {
	h := newHarness(t, "home", "office")
	if err := h.app.UseHost("office"); err != nil {
		t.Fatal(err)
	}
	h.start(80, 12, Options{})
	h.wantScreen("Hosts", "home", "office", h.fake.URL, "2/2") // cursor on the current host
	h.wantFooter("enter open · a add · x remove")
	h.press("enter")
	h.wantScreen("office › sites")
	h.press("enter")
	h.wantScreen("office › Default")
}

func TestHostAndSiteFromOptions(t *testing.T) {
	h := newHarness(t, "home", "office").start(80, 12, Options{Host: "office", Site: "workshop"})
	h.wantScreen("office › Workshop", "Workshop AP")

	h = newHarness(t, "home").start(80, 12, Options{Site: "Garage"})
	h.wantScreen("home › sites")
	h.wantFooter(`Error: no site named "Garage"`)

	h = newHarness(t, "home").start(80, 12, Options{Host: "nope"})
	h.wantScreen("Hosts")
	h.wantFooter("Error: could not open nope: unknown host")
}

func TestLastSiteIsReopened(t *testing.T) {
	h := newHarness(t, "home").start(80, 12, Options{})
	h.wantScreen("home › sites")
	h.press("j", "enter")
	h.wantScreen("home › Workshop")

	h.app = h.newApp() // a new run
	h.start(80, 12, Options{})
	h.wantScreen("home › Workshop", "Workshop AP")
}

func TestSwitchingSitesAndHosts(t *testing.T) {
	h := newHarness(t, "home", "office").start(80, 12, Options{Host: "home", Site: "Default"})
	h.press("S")
	h.wantScreen("home › sites", "1/2")
	h.press("esc") // back to the open site
	h.wantScreen("home › Default")
	h.press("S", "j", "enter")
	h.wantScreen("home › Workshop", "Workshop AP")
	h.wantNotOnScreen("Core router")

	h.press("H")
	h.wantScreen("Hosts", "1/2")
	h.press("esc")
	h.wantScreen("home › Workshop")
	h.press("H", "j", "enter")
	h.wantScreen("office › sites")
	h.press("R") // reloading the site list stays on the site list
	h.wantScreen("office › sites")
}

func TestReboot(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(80, 12, Options{})
	const call = "POST /sites/site-1/devices/AA-00-00-00-00-02/reboot"
	h.press("j", "r")
	h.wantFooter("Reboot Rack switch (switch)? y/N")
	h.press("enter") // anything but y cancels
	h.wantFooter("Canceled.")
	if h.fake.Called(call) {
		t.Fatal("rebooted without confirmation")
	}
	h.press("r", "y")
	h.wantFooter("Rebooting Rack switch.")
	if !h.fake.Called(call) {
		t.Fatalf("calls = %v", h.fake.Calls())
	}
}

func TestRenameDevice(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(80, 12, Options{})
	h.press("G", "n") // AP office is last
	h.wantFooter("New name for AP office: AP office█")
	h.press("backspace", "backspace", "backspace", "backspace", "backspace", "backspace").typeText("study").press("enter")
	h.wantFooter("Renamed AP office to AP study.")
	h.wantScreen("AP study")
	if got := h.fake.DeviceName("site-1", "AA-00-00-00-00-03"); got != "AP study" {
		t.Errorf("name on the controller = %q", got)
	}

	h.press("n", "ctrl+u", "enter")
	h.wantFooter("Canceled: nothing was entered.")
	h.press("n", "esc")
	h.wantFooter("Canceled.")
	h.press("n", "ctrl+u").typeText("-bad").press("enter")
	h.wantFooter("Error: the name must not start with")
	if got := h.fake.DeviceName("site-1", "AA-00-00-00-00-03"); got != "AP study" {
		t.Errorf("name on the controller = %q after rejected renames", got)
	}
}

func TestClientActions(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(100, 12, Options{})
	h.press("tab")
	h.wantFooter("enter details · n rename · b block · c reconnect · ? help")

	h.press("b")
	h.wantFooter("Block Laptop (CC-00-00-00-00-01)? y/N")
	h.press("y")
	h.wantFooter("Blocked Laptop. Unblock with: gomada client unblock CC-00-00-00-00-01")
	if !h.fake.Blocked("CC-00-00-00-00-01") {
		t.Error("not blocked on the controller")
	}

	h.press("c")
	h.wantFooter("Asked Laptop to reconnect.")
	h.press("j", "c") // NAS is wired
	h.wantFooter("Error: NAS is a wired client")

	h.press("G", "n").typeText("Office printer").press("enter")
	h.wantScreen("Office printer")
	if got := h.fake.ClientName("site-1", "CC-00-00-00-00-03"); got != "Office printer" {
		t.Errorf("name on the controller = %q", got)
	}
}

func TestFilterAndSort(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(100, 12, Options{})
	h.press("/")
	h.wantFooter("/█")
	h.typeText("AP")
	h.wantFooter("/AP█")
	h.wantScreen("AP office", "AP garage", "1/2")
	h.wantNotOnScreen("Core router", "Rack switch")
	h.press("enter")
	h.wantFooter("filter: AP (esc clears)")
	h.press("j", "enter") // details of the second match
	h.wantScreen("AP office", "EAP670")
	h.press("esc", "esc")              // the first closes the details, the second clears the filter
	h.wantScreen("Core router", "4/4") // the cursor stays on the row it was on

	h.press("/").typeText("zzz")
	h.wantScreen("No device matches the filter", "0/0")
	h.press("esc") // esc while typing clears the filter
	h.wantScreen("Core router")

	h.press("g", "r")
	h.wantFooter("Reboot Core router")
	h.press("n", "/").typeText("zzz").press("enter", "r")
	h.wantFooter("Select a device first.")
	h.press("esc")

	h.press("o") // the status symbol column has no title and is skipped
	h.wantFooter("Sorted by name.")
	h.wantScreen("NAME▲")
	if lines := h.lines(); !strings.Contains(lines[2], "AP garage") {
		t.Errorf("first row after sorting by name = %q", lines[2])
	}
}

func TestRemoveHosts(t *testing.T) {
	h := newHarness(t, "home", "office").start(80, 12, Options{Host: "home", Site: "Default"})
	h.press("H", "j", "x")
	h.wantFooter("Remove host office and its stored secret? y/N")
	h.press("n")
	h.wantScreen("office")
	h.press("x", "y")
	h.wantFooter("Removed host office.")
	h.wantNotOnScreen("office ")
	h.press("esc") // the open host is still there
	h.wantScreen("home › Default")

	h.press("H", "x", "y") // removing the last host leads to the wizard
	h.wantScreen("add a controller")
	if hosts := h.newApp().Hosts(); len(hosts) != 0 {
		t.Errorf("hosts = %+v", hosts)
	}
	if _, err := keyring.Get("gomada", "home"); err == nil {
		t.Error("the secret survived the removal")
	}
}

func TestAddHostFromHostList(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(80, 16, Options{})
	h.press("H", "a")
	h.wantScreen("add a controller")
	h.typeText("home").press("enter")
	h.wantScreen("already exists")
	h.press("esc") // with hosts present, esc goes back to the list
	h.wantScreen("Hosts")
	if h.quit {
		t.Error("the program quit")
	}
	h.press("a").typeText("second").press("enter").typeText(h.fake.URL).press("enter")
	h.typeText(omadatest.ClientID).press("enter").typeText(omadatest.Secret).press("enter")
	h.wantScreen("second › Default")
}

func TestControllerDownAtStart(t *testing.T) {
	h := newHarness(t, "home")
	h.fake.Close()
	h.start(80, 12, Options{})
	h.wantScreen("Hosts", "home")
	h.wantFooter("Error: could not open home: controller unreachable")
}

func TestLoadErrorKeepsRows(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(80, 12, Options{})
	h.fake.Close()
	h.press("R")
	h.wantScreen("Core router") // the rows loaded earlier stay
	h.wantFooter("Error: controller unreachable")

	h.press("tab") // a tab that never loaded shows the problem in its place
	h.wantScreen("Could not load the clients", "Press R to try again")
}

func TestExpiredTokenIsRenewedSilently(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(80, 12, Options{})
	before := h.fake.Logins()
	h.fake.ExpireTokens()
	h.press("R")
	h.wantScreen("Core router")
	h.wantFooter("enter details")
	if got := h.fake.Logins() - before; got != 1 {
		t.Errorf("logins = %d, want one silent renewal", got)
	}
}

func TestEmptySite(t *testing.T) {
	h := newHarness(t, "home").start(80, 12, Options{Site: "Workshop"})
	h.press("tab")
	h.wantScreen("No clients here.", "0/0")
	h.press("enter", "n", "b") // nothing to act on; must not panic
	h.wantFooter("Select a client first.")
}

func TestPeriodicReload(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(80, 12, Options{})
	count := func() int { return len(h.fake.Calls()) }

	before := count()
	h.send(tickMsg{})
	if count() != before+1 {
		t.Errorf("a tick on the main screen made %d requests, want 1", count()-before)
	}
	h.fake.SetDevices("site-1", omadatest.Record{"mac": "01", "name": "New device", "type": "ap", "status": 1})
	h.send(tickMsg{})
	h.wantScreen("New device")

	before = count()
	h.press("r") // a question is open
	h.send(tickMsg{})
	h.press("esc", "H") // on the host list
	h.send(tickMsg{})
	if count() != before {
		t.Errorf("ticks reloaded %d times while a question was open or another screen was shown", count()-before)
	}
}

func TestStaleAnswersAreIgnored(t *testing.T) {
	h := newHarness(t, "home").start(80, 12, Options{Site: "Default"})
	h.wantScreen("home › Default", "Core router")

	// Rows of a site that is no longer open.
	h.send(rowsMsg{tab: h.m.tabs[0], sess: h.m.sess, siteID: "site-2"})
	h.wantScreen("Core router")
	// The answer to a connection nobody is waiting for.
	h.send(connectedMsg{host: "office", err: errors.New("boom")})
	h.wantScreen("home › Default")
	h.wantNotOnScreen("boom")
	// Wizard answers outside the wizard.
	h.send(certMsg{err: errors.New("boom")})
	h.send(addedMsg{err: errors.New("boom")})
	h.wantScreen("home › Default")
}

func TestQuit(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(80, 12, Options{})
	h.press("n")
	h.typeText("q") // q is text while a prompt is open
	if h.quit {
		t.Fatal("q quit while typing a name")
	}
	h.press("ctrl+c")
	if !h.quit {
		t.Error("ctrl+c must always quit")
	}
	h = newHarness(t, "home").oneSite().start(80, 12, Options{})
	h.press("q")
	if !h.quit {
		t.Error("q must quit from a list")
	}
}

// Whatever the terminal size and screen, the UI must draw exactly as many
// lines as the terminal has, none wider than the terminal.
func TestScreenAlwaysFitsTheTerminal(t *testing.T) {
	sizes := [][2]int{{1, 1}, {5, 2}, {10, 3}, {20, 4}, {34, 8}, {80, 24}, {200, 50}}
	steps := []struct {
		name string
		keys []string
	}{
		{"devices", nil},
		{"scrolled right", []string{"l", "l"}},
		{"clients", []string{"tab"}},
		{"details", []string{"enter"}},
		{"help", []string{"esc", "?"}},
		{"confirm", []string{"esc", "tab", "r"}},
		{"prompt", []string{"esc", "n"}},
		{"long input", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p"}},
		{"filter", []string{"esc", "/", "a", "p"}},
		{"sites", []string{"esc", "esc", "S"}},
		{"hosts", []string{"H"}},
		{"wizard", []string{"a"}},
		{"wizard error", []string{"!", "enter"}},
	}
	for _, size := range sizes {
		h := newHarness(t, "home", "a-host-with-a-rather-long-name").start(size[0], size[1], Options{Host: "home", Site: "Default"})
		for _, step := range steps {
			h.press(step.keys...)
			lines := h.lines()
			if len(lines) != size[1] {
				t.Errorf("%dx%d %s: drew %d lines", size[0], size[1], step.name, len(lines))
			}
			for i, line := range lines {
				if w := ansi.StringWidth(line); w > size[0] {
					t.Errorf("%dx%d %s: line %d is %d wide: %q", size[0], size[1], step.name, i, w, line)
				}
			}
		}
	}
}

func TestNoSizeYet(t *testing.T) {
	h := newHarness(t, "home")
	h.m = New(t.Context(), h.app, Options{}, 0)
	if got := h.m.View().Content; got != "" {
		t.Errorf("before the terminal size is known the view must be empty, got %q", got)
	}
	if !h.m.View().AltScreen {
		t.Error("the UI must use the alternate screen")
	}
}

func TestLongInputKeepsTheEndVisible(t *testing.T) {
	h := newHarness(t, "home").oneSite().start(20, 8, Options{})
	h.press("n", "ctrl+u").typeText("a name that is longer than the screen")
	h.wantFooter("the screen█")
	if !strings.HasPrefix(h.footer(), "…") {
		t.Errorf("bottom line = %q, want it cut on the left", h.footer())
	}
}
