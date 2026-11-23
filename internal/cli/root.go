// Package cli is the command line front end. Every command is a thin layer
// over the app package: it parses flags, calls a use case, and prints the
// result as a table or as JSON.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mareklanduch/gomada/internal/app"
	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/output"
	"github.com/mareklanduch/gomada/internal/secret"
	"github.com/mareklanduch/gomada/internal/tui"
	"github.com/mareklanduch/gomada/internal/version"
)

// Exit codes.
const (
	exitOK          = 0
	exitFailure     = 1
	exitUsage       = 2
	exitAuth        = 3
	exitUnreachable = 4
)

// IO is the command line's connection to the outside world. Tests replace
// it; Execute uses the real terminal.
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
	// Interactive reports whether a person is at the keyboard, so prompts
	// make sense.
	Interactive bool
	// ReadPassword reads a line without echoing it.
	ReadPassword func() (string, error)
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
	// PollEvery is how often a command that waits for the controller asks
	// again. Zero means every five seconds.
	PollEvery time.Duration
	// RunTUI starts the terminal UI.
	RunTUI func(ctx context.Context, a *app.App, opts tui.Options) error
}

// Execute runs the command line with the process's arguments and terminal
// and returns the exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stdin := int(os.Stdin.Fd()) //nolint:gosec // file descriptors fit in an int
	streams := IO{
		In:          os.Stdin,
		Out:         os.Stdout,
		Err:         os.Stderr,
		Interactive: term.IsTerminal(stdin) && term.IsTerminal(int(os.Stdout.Fd())), //nolint:gosec // as above
		ReadPassword: func() (string, error) {
			b, err := term.ReadPassword(stdin)
			fmt.Fprintln(os.Stderr)
			return string(b), err
		},
		RunTUI: tui.Run,
	}
	return Run(ctx, streams, os.Args[1:])
}

// Run runs the command line with the given arguments and returns the exit
// code. Errors are printed to streams.Err.
func Run(ctx context.Context, streams IO, args []string) int {
	c := &cli{IO: streams, in: bufio.NewReader(streams.In)}
	root := c.rootCmd()
	root.SetArgs(args)
	root.SetIn(streams.In)
	root.SetOut(streams.Out)
	root.SetErr(streams.Err)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return exitOK
	}
	fmt.Fprintln(streams.Err, "gomada:", err)
	if hint := hintFor(err); hint != "" {
		fmt.Fprintln(streams.Err, hint)
	}
	return exitCode(err)
}

// usageError marks an error as wrong usage (exit code 2).
type usageError struct{ error }

func usagef(format string, args ...any) error {
	return usageError{fmt.Errorf(format, args...)}
}

func exitCode(err error) int {
	var usage usageError
	switch {
	case errors.As(err, &usage):
		return exitUsage
	case errors.Is(err, omada.ErrAuth):
		return exitAuth
	case errors.Is(err, omada.ErrUnreachable), errors.Is(err, omada.ErrCertificate):
		return exitUnreachable
	}
	return exitFailure
}

// hintFor returns a line telling the person what to do about an error.
func hintFor(err error) string {
	var (
		usage    usageError
		mismatch *omada.PinMismatchError
	)
	switch {
	case errors.As(err, &usage):
		return "Run the command with --help to see how it is used."
	case errors.Is(err, omada.ErrAuth):
		return "Check the client ID and client secret of the Open API application in the controller (Settings > Platform Integration > Open API)."
	case errors.As(err, &mismatch):
		return "If you replaced the controller's certificate yourself, remove the host and add it again. If not, someone may be intercepting the connection."
	case errors.Is(err, omada.ErrUnreachable):
		return "Check the controller address and that this machine can reach it."
	case errors.Is(err, secret.ErrPassphraseRequired):
		return "Run the command in a terminal to be asked for it, or set " + secret.EnvPassphrase + "."
	}
	return ""
}

// cli holds the state shared by all commands: the streams and the global
// flags.
type cli struct {
	IO
	in *bufio.Reader

	configPath   string
	host         string
	site         string
	format       string
	timeout      time.Duration
	yes          bool
	verbose      bool
	noColor      bool
	noTokenCache bool
}

func (c *cli) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "gomada",
		Short: "Manage TP-Link Omada controllers from the terminal",
		Long: `gomada manages TP-Link Omada controllers through the Omada Open API.

Run it without a command to open the interactive view. Every action is also
available as a command for use in scripts.`,
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usagef("unknown command %q", args[0])
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error { return c.runTUI(cmd) },
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })

	f := root.PersistentFlags()
	f.StringVar(&c.host, "host", "", "host to use instead of the current one")
	f.StringVar(&c.site, "site", "", "site name or ID to use instead of the last used one")
	f.StringVarP(&c.format, "output", "o", "table", "output format: table or json")
	f.DurationVar(&c.timeout, "timeout", omada.DefaultTimeout, "time limit for each request to the controller")
	f.BoolVarP(&c.yes, "yes", "y", false, "do not ask for confirmation")
	f.BoolVarP(&c.verbose, "verbose", "v", false, "log each request to the controller to standard error")
	f.BoolVar(&c.noColor, "no-color", false, "turn off color in the interactive view")
	f.BoolVar(&c.noTokenCache, "no-token-cache", false, "log in on every run and store no access token")
	f.StringVar(&c.configPath, "config", "", "config file to use (default: $GOMADA_CONFIG or the standard location)")

	root.AddCommand(c.hostCmd(), c.siteCmd(), c.deviceCmd(), c.clientCmd(), c.wifiCmd(), c.alertCmd(), c.versionCmd())
	return root
}

func (c *cli) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  noArgs,
		RunE: func(*cobra.Command, []string) error {
			_, err := fmt.Fprintln(c.Out, version.String())
			return err
		},
	}
}

func (c *cli) runTUI(cmd *cobra.Command) error {
	if !c.Interactive {
		return usagef("the interactive view needs a terminal; see 'gomada --help' for the commands that work without one")
	}
	a, err := c.app()
	if err != nil {
		return err
	}
	return c.RunTUI(cmd.Context(), a, tui.Options{Host: c.host, Site: c.site, NoColor: c.noColor})
}

// app builds the application core from the global flags.
func (c *cli) app() (*app.App, error) {
	opts := app.Options{
		ConfigPath:   c.configPath,
		Timeout:      c.timeout,
		NoTokenCache: c.noTokenCache,
		Passphrase:   c.askPassphrase,
		Now:          c.Now,
	}
	if c.verbose {
		opts.Logger = slog.New(slog.NewTextHandler(c.Err, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return app.New(opts)
}

// askPassphrase gets the passphrase of an encrypted secret file: from the
// environment, or by asking at the terminal.
func (c *cli) askPassphrase(host string) (string, error) {
	if p := os.Getenv(secret.EnvPassphrase); p != "" {
		return p, nil
	}
	if !c.Interactive {
		return "", secret.ErrPassphraseRequired
	}
	return c.promptHidden(fmt.Sprintf("Passphrase for host %q: ", host))
}

// pollEvery is how often a waiting command asks the controller again.
func (c *cli) pollEvery() time.Duration {
	if c.PollEvery > 0 {
		return c.PollEvery
	}
	return 5 * time.Second
}

// outputFormat validates the --output flag.
func (c *cli) outputFormat() (output.Format, error) {
	f, err := output.ParseFormat(c.format)
	if err != nil {
		return "", usageError{err}
	}
	return f, nil
}

// prompt asks a question on standard error and reads one line.
func (c *cli) prompt(question string) (string, error) {
	fmt.Fprint(c.Err, question)
	line, err := c.in.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", fmt.Errorf("read answer: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// promptHidden asks a question and reads the answer without echo.
func (c *cli) promptHidden(question string) (string, error) {
	fmt.Fprint(c.Err, question)
	s, err := c.ReadPassword()
	if err != nil {
		return "", fmt.Errorf("read answer: %w", err)
	}
	return s, nil
}

// errCanceled is returned when a person answers "no" to a confirmation.
var errCanceled = errors.New("canceled")

// confirm guards an action that changes something. With --yes it passes.
// At a terminal it asks. Anywhere else it refuses, so a script can never
// trigger the action by accident.
func (c *cli) confirm(question string) error {
	if c.yes {
		return nil
	}
	if !c.Interactive {
		return usagef("this needs confirmation; pass --yes to go ahead without being asked")
	}
	answer, err := c.prompt(question + " [y/N] ")
	if err != nil {
		return err
	}
	if a := strings.ToLower(answer); a != "y" && a != "yes" {
		return errCanceled
	}
	return nil
}

// Argument validators that report wrong usage with exit code 2.

func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return usagef("%q takes no arguments", cmd.CommandPath())
	}
	return nil
}

func exactArgs(n int, names string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return usagef("usage: %s %s", cmd.CommandPath(), names)
		}
		return nil
	}
}

// group returns a command that only holds subcommands.
func group(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usagef("unknown command %q for %q", args[0], cmd.CommandPath())
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
}
