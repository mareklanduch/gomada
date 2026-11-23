package cli_test

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/mareklanduch/gomada/internal/app"
	"github.com/mareklanduch/gomada/internal/cli"
	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/omada/omadatest"
	"github.com/mareklanduch/gomada/internal/secret"
	"github.com/mareklanduch/gomada/internal/tui"
)

// TestMain pins the time zone, so times in expected output do not depend on
// where the tests run.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

var update = flag.Bool("update", false, "rewrite the golden files with the current output")

// env is a test fixture: a fake controller, a config in a temp directory,
// and an in-memory keychain.
type env struct {
	t    *testing.T
	fake *omadatest.Controller
	path string

	// What the next command sees.
	stdin       string
	interactive bool
	passwords   []string // answers to hidden prompts, in order

	tuiOpts *tui.Options // set when the TUI was started
}

func newEnv(t *testing.T) *env {
	t.Helper()
	keyring.MockInit()
	t.Setenv(secret.EnvVar, "")
	t.Setenv(secret.EnvPassphrase, "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	return &env{t: t, fake: omadatest.New(t), path: filepath.Join(t.TempDir(), "config.yaml")}
}

// newEnvWithHost returns a fixture with the host "home" already added.
func newEnvWithHost(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.addHost("home")
	return e
}

func (e *env) addHost(name string) {
	e.t.Helper()
	e.stdin = omadatest.Secret + "\n"
	e.mustRun("host", "add", name, "--url", e.fake.URL, "--client-id", omadatest.ClientID, "--secret-stdin")
}

// run executes one command and returns what it printed and its exit code.
func (e *env) run(args ...string) (stdout, stderr string, code int) {
	e.t.Helper()
	var out, errOut bytes.Buffer
	streams := cli.IO{
		In:          strings.NewReader(e.stdin),
		Out:         &out,
		Err:         &errOut,
		Interactive: e.interactive,
		ReadPassword: func() (string, error) {
			if len(e.passwords) == 0 {
				return "", errors.New("unexpected hidden prompt")
			}
			p := e.passwords[0]
			e.passwords = e.passwords[1:]
			return p, nil
		},
		Now:       func() time.Time { return omadatest.Epoch },
		PollEvery: time.Millisecond,
		RunTUI: func(_ context.Context, _ *app.App, opts tui.Options) error {
			e.tuiOpts = &opts
			return nil
		},
	}
	code = cli.Run(e.t.Context(), streams, append([]string{"--config", e.path}, args...))
	e.stdin = ""
	return out.String(), errOut.String(), code
}

// mustRun executes a command that has to succeed and returns its output.
func (e *env) mustRun(args ...string) string {
	e.t.Helper()
	stdout, stderr, code := e.run(args...)
	if code != 0 {
		e.t.Fatalf("gomada %s: exit code %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

// wantFail executes a command that has to fail with the given exit code and
// returns what it printed to standard error.
func (e *env) wantFail(wantCode int, args ...string) string {
	e.t.Helper()
	stdout, stderr, code := e.run(args...)
	if code != wantCode {
		e.t.Fatalf("gomada %s: exit code %d, want %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, wantCode, stdout, stderr)
	}
	return stderr
}

// golden compares got with a file in testdata. The fake controller's
// address changes on every run, so it is replaced by a fixed one of the same
// length, which keeps table columns aligned.
func (e *env) golden(name, got string) {
	e.t.Helper()
	const fixed = "http://controller:8043"
	if len(e.fake.URL) != len(fixed) {
		e.t.Skipf("the fake controller's address %q has an unusual length", e.fake.URL)
	}
	got = strings.ReplaceAll(got, e.fake.URL, fixed)
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			e.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatalf("%v (run the tests with -update to create it)", err)
	}
	if got != string(want) {
		e.t.Errorf("output differs from %s (run the tests with -update if the change is intended)\ngot:\n%s\nwant:\n%s", path, got, want)
	}
}

func wantContains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}
}

func TestVersion(t *testing.T) {
	e := newEnv(t)
	wantContains(t, e.mustRun("version"), "gomada dev")
	wantContains(t, e.mustRun("--version"), "gomada dev")
}

func TestUsageErrorsExitWith2(t *testing.T) {
	e := newEnvWithHost(t)
	for _, args := range [][]string{
		{"frobnicate"},
		{"device", "frobnicate"},
		{"--frobnicate"},
		{"device", "list", "--output", "yaml"},
		{"device", "list", "extra"},
		{"device", "list", "--type", "router"},
		{"device", "show"},
		{"device", "rename", "AP office"},
		{"device", "rename", "AP office", "-bad name"},
		{"client", "list", "--wired", "--wireless"},
		{"client", "rename", "Laptop", ""},
		{"host", "use"},
		{"host", "add", "a", "b"},
		{"site", "use"},
		{"version", "extra"},
	} {
		stderr := e.wantFail(2, args...)
		wantContains(t, stderr, "gomada:", "--help")
	}
	if got := len(e.fake.Calls()); got != 0 {
		t.Errorf("usage errors must not reach the controller, got %d calls: %v", got, e.fake.Calls())
	}
}

func TestHelpListsCommands(t *testing.T) {
	e := newEnv(t)
	wantContains(t, e.mustRun("--help"), "host", "site", "device", "client", "completion", "--no-token-cache")
	wantContains(t, e.mustRun("device"), "reboot", "rename")
	wantContains(t, e.mustRun("completion", "bash"), "gomada")
}

func TestRootOpensTUI(t *testing.T) {
	e := newEnv(t)
	stderr := e.wantFail(2)
	wantContains(t, stderr, "needs a terminal")
	if e.tuiOpts != nil {
		t.Fatal("the TUI must not start without a terminal")
	}

	e.interactive = true
	e.mustRun("--host", "office", "--site", "Workshop", "--no-color")
	if e.tuiOpts == nil || *e.tuiOpts != (tui.Options{Host: "office", Site: "Workshop", NoColor: true}) {
		t.Errorf("TUI options = %+v", e.tuiOpts)
	}
}

func TestHostAddWithFlags(t *testing.T) {
	e := newEnv(t)
	e.stdin = omadatest.Secret + "\n"
	out := e.mustRun("host", "add", "home", "--url", e.fake.URL+"/login", "--client-id", omadatest.ClientID, "--secret-stdin")
	wantContains(t, out, `Added host "home"`, "system keychain", `"home" is now the current host`)
	if got, _ := keyring.Get("gomada", "home"); got != omadatest.Secret {
		t.Errorf("keychain holds %q", got)
	}

	e.addHost("office")
	e.golden("host-list", e.mustRun("host", "list"))
	e.golden("host-list-json", e.mustRun("host", "list", "-o", "json"))
}

func TestHostAddInteractive(t *testing.T) {
	e := newEnv(t)
	e.interactive = true
	e.stdin = "home\n" + e.fake.URL + "\n" + omadatest.ClientID + "\n"
	e.passwords = []string{omadatest.Secret}
	stdout, stderr, code := e.run("host", "add")
	if code != 0 {
		t.Fatalf("exit code %d\n%s\n%s", code, stdout, stderr)
	}
	wantContains(t, stderr, "Name for this host", "Controller address", "Client ID", "Client secret (not shown)")
	wantContains(t, stdout, `Added host "home"`)
	if strings.Contains(stdout+stderr, omadatest.Secret) {
		t.Error("the secret was printed")
	}
}

func TestHostAddMissingInputWithoutTerminal(t *testing.T) {
	e := newEnv(t)
	wantContains(t, e.wantFail(2, "host", "add"), "NAME is required")
	wantContains(t, e.wantFail(2, "host", "add", "home"), "--url is required")
	wantContains(t, e.wantFail(2, "host", "add", "home", "--url", e.fake.URL), "--client-id is required")
	wantContains(t, e.wantFail(2, "host", "add", "home", "--url", e.fake.URL, "--client-id", "x"), "--secret-stdin", secret.EnvVar)
	wantContains(t, e.wantFail(2, "host", "add", "home", "--url", e.fake.URL, "--client-id", "x", "--secret-stdin"), "standard input")
	wantContains(t, e.wantFail(2, "host", "add", "home", "--url", "ftp://x", "--client-id", "x"), "unsupported scheme")
}

func TestHostAddSecretFromEnvironment(t *testing.T) {
	e := newEnv(t)
	t.Setenv(secret.EnvVar, omadatest.Secret)
	out := e.mustRun("host", "add", "ci", "--url", e.fake.URL, "--client-id", omadatest.ClientID, "--secret-store", "env")
	wantContains(t, out, "read from "+secret.EnvVar)
	e.mustRun("site", "list")
	if _, err := keyring.Get("gomada", "ci"); err == nil {
		t.Error("with --secret-store env nothing must be stored")
	}
}

func TestHostAddWrongSecret(t *testing.T) {
	e := newEnv(t)
	e.stdin = "wrong\n"
	stderr := e.wantFail(3, "host", "add", "home", "--url", e.fake.URL, "--client-id", omadatest.ClientID, "--secret-stdin")
	wantContains(t, stderr, "authentication failed", "Platform Integration")
	wantContains(t, e.mustRun("host", "list", "-o", "json"), "[]")
}

func TestHostAddUnreachable(t *testing.T) {
	e := newEnv(t)
	url := e.fake.URL
	e.fake.Close()
	e.stdin = omadatest.Secret + "\n"
	stderr := e.wantFail(4, "host", "add", "home", "--url", url, "--client-id", omadatest.ClientID, "--secret-stdin")
	wantContains(t, stderr, "controller unreachable", "Check the controller address")
}

func TestHostAddSelfSignedCertificate(t *testing.T) {
	e := newEnv(t)
	e.fake = omadatest.NewTLS(t)
	fp := omada.Fingerprint(e.fake.Certificate())
	add := []string{"host", "add", "home", "--url", e.fake.URL, "--client-id", omadatest.ClientID, "--secret-stdin"}

	t.Run("without a terminal it asks for --tls-pin", func(t *testing.T) {
		e.stdin = omadatest.Secret + "\n"
		wantContains(t, e.wantFail(2, add...), fp, "--tls-pin "+fp)
	})
	t.Run("declined at the terminal", func(t *testing.T) {
		e.interactive = true
		defer func() { e.interactive = false }()
		e.stdin = "n\n"
		wantContains(t, e.wantFail(1, add[:len(add)-1]...), fp, "canceled")
	})
	t.Run("wrong pin", func(t *testing.T) {
		e.stdin = omadatest.Secret + "\n"
		stderr := e.wantFail(4, append(add, "--tls-pin", "sha256:00")...)
		wantContains(t, stderr, "certificate changed", "intercepting")
	})
	t.Run("accepted at the terminal", func(*testing.T) {
		e.interactive = true
		defer func() { e.interactive = false }()
		e.stdin = "y\n"
		e.passwords = []string{omadatest.Secret}
		e.mustRun(add[:len(add)-1]...)
		e.mustRun("site", "list") // the pin was saved and is used
	})
	t.Run("pin as a flag", func(*testing.T) {
		e.stdin = omadatest.Secret + "\n"
		add[2] = "pinned"
		e.mustRun(append(add, "--tls-pin", fp)...)
		e.mustRun("--host", "pinned", "site", "list")
	})
	t.Run("insecure", func(t *testing.T) {
		e.stdin = omadatest.Secret + "\n"
		add[2] = "insecure"
		_, stderr, code := e.run(append(add, "--insecure-skip-tls-verify")...)
		if code != 0 {
			t.Fatalf("exit code %d: %s", code, stderr)
		}
		wantContains(t, stderr, "WARNING")
	})
}

func TestHostAddWithoutKeychain(t *testing.T) {
	add := func(e *env) []string {
		return []string{"host", "add", "home", "--url", e.fake.URL, "--client-id", omadatest.ClientID, "--secret-stdin"}
	}
	noKeychain := func(t *testing.T) *env {
		e := newEnv(t)
		keyring.MockInitWithError(errors.New("no secret service"))
		return e
	}

	t.Run("passphrase typed twice", func(t *testing.T) {
		e := noKeychain(t)
		e.interactive = true
		e.stdin = omadatest.Secret + "\n"
		e.passwords = []string{"pw", "pw"}
		out := e.mustRun(add(e)...)
		wantContains(t, out, "encrypted file")

		// Later commands ask for the passphrase.
		e.passwords = []string{"pw"}
		_, stderr, code := e.run("site", "list")
		if code != 0 {
			t.Fatalf("exit code %d: %s", code, stderr)
		}
		wantContains(t, stderr, `Passphrase for host "home"`)

		e.passwords = []string{"wrong"}
		wantContains(t, e.wantFail(1, "site", "list"), "wrong passphrase")

		e.interactive = false
		wantContains(t, e.wantFail(1, "site", "list"), "passphrase is required", secret.EnvPassphrase)
		t.Setenv(secret.EnvPassphrase, "pw")
		e.mustRun("site", "list")
	})
	t.Run("passphrases differ", func(t *testing.T) {
		e := noKeychain(t)
		e.interactive = true
		e.stdin = omadatest.Secret + "\n"
		e.passwords = []string{"pw", "pW"}
		wantContains(t, e.wantFail(1, add(e)...), "differ")
	})
	t.Run("without a terminal", func(t *testing.T) {
		e := noKeychain(t)
		e.stdin = omadatest.Secret + "\n"
		wantContains(t, e.wantFail(2, add(e)...), secret.EnvPassphrase, "--secret-store env")

		t.Setenv(secret.EnvPassphrase, "pw")
		e.stdin = omadatest.Secret + "\n"
		e.mustRun(add(e)...)
		e.mustRun("site", "list")
	})
	t.Run("keyring requested explicitly", func(t *testing.T) {
		e := noKeychain(t)
		e.stdin = omadatest.Secret + "\n"
		wantContains(t, e.wantFail(1, append(add(e), "--secret-store", "keyring")...), "no system keychain")
	})
}

func TestHostUseRemoveTest(t *testing.T) {
	e := newEnvWithHost(t)
	e.addHost("office")

	wantContains(t, e.mustRun("host", "test"), "OK: home", "Sites: 2")
	wantContains(t, e.mustRun("host", "test", "office"), "OK: office")
	wantContains(t, e.mustRun("host", "use", "office"), `"office" is now the current host`)
	wantContains(t, e.mustRun("host", "test"), "OK: office")
	wantContains(t, e.wantFail(1, "host", "use", "nope"), "unknown host", "home, office")

	wantContains(t, e.wantFail(2, "host", "remove", "office"), "--yes")
	e.interactive = true
	e.stdin = "n\n"
	wantContains(t, e.wantFail(1, "host", "remove", "office"), "canceled")
	e.stdin = "y\n"
	wantContains(t, e.mustRun("host", "remove", "office"), `Removed host "office"`)
	e.interactive = false
	wantContains(t, e.mustRun("host", "remove", "home", "--yes"), "Removed")
	wantContains(t, e.wantFail(1, "host", "remove", "home", "--yes"), "no host is set up yet")
	wantContains(t, e.wantFail(1, "device", "list"), "gomada host add")

	_, stderr, _ := e.run("host", "list")
	wantContains(t, stderr, "No hosts yet")
}

func TestSites(t *testing.T) {
	e := newEnvWithHost(t)
	e.golden("site-list", e.mustRun("site", "list"))

	wantContains(t, e.wantFail(1, "device", "list"), "several sites", "--site", "Default, Workshop")
	wantContains(t, e.mustRun("device", "list", "--site", "workshop"), "Workshop AP")
	wantContains(t, e.mustRun("device", "list", "--site", "site-2"), "Workshop AP")
	wantContains(t, e.wantFail(1, "device", "list", "--site", "Garage"), `no site named "Garage"`)

	wantContains(t, e.mustRun("site", "use", "Workshop"), `now use site "Workshop"`)
	wantContains(t, e.mustRun("device", "list"), "Workshop AP")
	e.golden("site-list-json", e.mustRun("site", "list", "-o", "json"))
	wantContains(t, e.wantFail(1, "site", "use", "Garage"), "no site named")
}

func TestDeviceList(t *testing.T) {
	e := newEnvWithHost(t)
	e.mustRun("site", "use", "Default")
	e.golden("device-list", e.mustRun("device", "list"))
	e.golden("device-list-json", e.mustRun("device", "ls", "--output", "json"))

	aps := e.mustRun("device", "list", "--type", "AP")
	wantContains(t, aps, "AP office", "AP garage")
	if strings.Contains(aps, "Core router") {
		t.Errorf("--type ap shows other types:\n%s", aps)
	}
	filtered := e.mustRun("device", "list", "--filter", "offline")
	if !strings.Contains(filtered, "AP garage") || strings.Contains(filtered, "AP office") {
		t.Errorf("--filter offline:\n%s", filtered)
	}
	wantContains(t, e.mustRun("device", "list", "--filter", "zzz", "-o", "json"), "[]")
}

func TestDeviceShow(t *testing.T) {
	e := newEnvWithHost(t)
	e.mustRun("site", "use", "Default")
	e.golden("device-show", e.mustRun("device", "show", "core router"))
	e.golden("device-show-json", e.mustRun("device", "show", "aa:00:00:00:00:03", "-o", "json"))
	wantContains(t, e.wantFail(1, "device", "show", "nothing"), `no device matches "nothing"`)
}

func TestDeviceReboot(t *testing.T) {
	e := newEnvWithHost(t)
	e.mustRun("site", "use", "Default")
	const call = "POST /sites/site-1/devices/AA-00-00-00-00-03/reboot"

	wantContains(t, e.wantFail(2, "device", "reboot", "AP office"), "--yes")
	e.interactive = true
	e.stdin = "\n" // just Enter means no
	stderr := e.wantFail(1, "device", "reboot", "AP office")
	wantContains(t, stderr, "Reboot AP office (ap, AA-00-00-00-00-03)? [y/N]", "canceled")
	if e.fake.Called(call) {
		t.Fatal("the device was rebooted without confirmation")
	}

	e.stdin = "y\n"
	wantContains(t, e.mustRun("device", "reboot", "AP office"), "Rebooting AP office")
	if !e.fake.Called(call) {
		t.Fatalf("calls = %v", e.fake.Calls())
	}
	e.interactive = false
	wantContains(t, e.mustRun("device", "reboot", "aa-00-00-00-00-01", "--yes"), "Rebooting Core router")
}

func TestDeviceRename(t *testing.T) {
	e := newEnvWithHost(t)
	e.mustRun("site", "use", "Default")
	wantContains(t, e.mustRun("device", "rename", "AP office", "AP study"), "Renamed AP office to AP study")
	if got := e.fake.DeviceName("site-1", "AA-00-00-00-00-03"); got != "AP study" {
		t.Errorf("name = %q", got)
	}
	e.mustRun("device", "rename", "Core router", "Edge") // gateway path
	e.mustRun("device", "rename", "Rack switch", "Rack") // switch path
	wantContains(t, e.mustRun("device", "list"), "Edge", "Rack", "AP study")
}

func TestClientList(t *testing.T) {
	e := newEnvWithHost(t)
	e.mustRun("site", "use", "Default")
	e.golden("client-list", e.mustRun("client", "list"))
	e.golden("client-list-json", e.mustRun("client", "list", "-o", "json"))

	wired := e.mustRun("client", "list", "--wired")
	if !strings.Contains(wired, "NAS") || strings.Contains(wired, "Laptop") {
		t.Errorf("--wired:\n%s", wired)
	}
	wireless := e.mustRun("client", "list", "--wireless")
	if strings.Contains(wireless, "NAS") || !strings.Contains(wireless, "Laptop") {
		t.Errorf("--wireless:\n%s", wireless)
	}
	byNetwork := e.mustRun("client", "list", "--filter", "iot")
	if !strings.Contains(byNetwork, "printer") || strings.Contains(byNetwork, "Laptop") {
		t.Errorf("--filter iot:\n%s", byNetwork)
	}
}

func TestClientShow(t *testing.T) {
	e := newEnvWithHost(t)
	e.mustRun("site", "use", "Default")
	e.golden("client-show", e.mustRun("client", "show", "laptop"))
	e.golden("client-show-json", e.mustRun("client", "show", "NAS", "-o", "json"))
}

func TestClientActions(t *testing.T) {
	e := newEnvWithHost(t)
	e.mustRun("site", "use", "Default")
	const mac = "CC-00-00-00-00-01"

	wantContains(t, e.mustRun("client", "rename", "printer", "Office printer"), "Renamed printer to Office printer")
	if got := e.fake.ClientName("site-1", "CC-00-00-00-00-03"); got != "Office printer" {
		t.Errorf("name = %q", got)
	}

	wantContains(t, e.wantFail(2, "client", "block", "Laptop"), "--yes")
	if e.fake.Blocked(mac) {
		t.Fatal("blocked without confirmation")
	}
	wantContains(t, e.mustRun("client", "block", "Laptop", "-y"), "Blocked Laptop", "gomada client unblock "+mac)
	if !e.fake.Blocked(mac) {
		t.Fatal("not blocked")
	}
	wantContains(t, e.wantFail(1, "client", "unblock", "Laptop"), "MAC address")
	wantContains(t, e.mustRun("client", "unblock", "cc:00:00:00:00:01"), "Unblocked")
	if e.fake.Blocked(mac) {
		t.Fatal("still blocked")
	}

	wantContains(t, e.mustRun("client", "reconnect", "Laptop"), "Asked Laptop to reconnect")
	wantContains(t, e.wantFail(1, "client", "reconnect", "NAS"), "wired client")
}

func TestControllerDown(t *testing.T) {
	e := newEnvWithHost(t)
	e.fake.Close()
	wantContains(t, e.wantFail(4, "site", "list"), "controller unreachable")
}

func TestSecretChangedOnController(t *testing.T) {
	e := newEnvWithHost(t)
	if err := keyring.Set("gomada", "home", "stale"); err != nil {
		t.Fatal(err)
	}
	wantContains(t, e.wantFail(3, "site", "list", "--no-token-cache"), "authentication failed")
}

func TestTokenCacheAcrossCommands(t *testing.T) {
	e := newEnvWithHost(t)
	before := e.fake.Logins()
	for range 3 {
		e.mustRun("site", "list")
	}
	if got := e.fake.Logins() - before; got != 1 {
		t.Errorf("logins = %d, want 1 for three commands", got)
	}
	e.fake.ExpireTokens()
	e.mustRun("site", "list") // the cached token is rejected and replaced
	if got := e.fake.Logins() - before; got != 2 {
		t.Errorf("logins = %d, want 2 after the token expired", got)
	}
	for range 2 {
		e.mustRun("site", "list", "--no-token-cache")
	}
	if got := e.fake.Logins() - before; got != 4 {
		t.Errorf("logins = %d, want 4 with the cache off", got)
	}
}

func TestVerboseLogsRequestsWithoutSecrets(t *testing.T) {
	e := newEnvWithHost(t)
	_, stderr, code := e.run("site", "list", "--verbose", "--no-token-cache")
	if code != 0 {
		t.Fatalf("exit code %d: %s", code, stderr)
	}
	wantContains(t, stderr, "/openapi/authorize/token", "/sites", "status=200")
	for _, leak := range []string{omadatest.Secret, "AT-", "AccessToken"} {
		if strings.Contains(stderr, leak) {
			t.Errorf("the log contains %q:\n%s", leak, stderr)
		}
	}
}

func TestBrokenConfig(t *testing.T) {
	e := newEnv(t)
	if err := os.WriteFile(e.path, []byte("hosts: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	wantContains(t, e.wantFail(1, "host", "list"), "not a valid config file")
}

func TestCanceledContext(t *testing.T) {
	e := newEnvWithHost(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var errOut bytes.Buffer
	code := cli.Run(ctx, cli.IO{In: strings.NewReader(""), Out: &bytes.Buffer{}, Err: &errOut}, []string{"--config", e.path, "site", "list", "--no-token-cache"})
	if code != 1 || !strings.Contains(errOut.String(), "context canceled") {
		t.Errorf("code = %d, stderr = %s", code, errOut.String())
	}
}
