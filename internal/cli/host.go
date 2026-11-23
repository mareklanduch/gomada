package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mareklanduch/gomada/internal/app"
	"github.com/mareklanduch/gomada/internal/output"
	"github.com/mareklanduch/gomada/internal/secret"
)

func (c *cli) hostCmd() *cobra.Command {
	cmd := group("host", "Manage saved controllers")
	cmd.AddCommand(c.hostAddCmd(), c.hostListCmd(), c.hostUseCmd(), c.hostRemoveCmd(), c.hostTestCmd())
	return cmd
}

func (c *cli) hostAddCmd() *cobra.Command {
	var (
		h           app.NewHost
		secretStdin bool
	)
	cmd := &cobra.Command{
		Use:   "add [NAME]",
		Short: "Add a controller",
		Long: `Add a controller and check that its credentials work.

Create an Open API application in the controller first (Settings > Platform
Integration > Open API, client credentials mode). It gives you the client ID
and client secret.

In a terminal, anything not given as a flag is asked for. The client secret is
never a flag: it is typed without echo, read from standard input with
--secret-stdin, or taken from the GOMADA_CLIENT_SECRET environment variable.`,
		Example: `  gomada host add home
  gomada host add home --url https://192.168.0.2:8043 --client-id 9f8e7d
  echo "$SECRET" | gomada host add ci --url https://omada.example.net --client-id 9f8e7d --secret-stdin --secret-store env`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usagef("usage: %s [NAME]", cmd.CommandPath())
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				h.Name = args[0]
			}
			return c.hostAdd(cmd, h, secretStdin)
		},
	}
	f := cmd.Flags()
	f.StringVar(&h.URL, "url", "", "controller address, e.g. https://192.168.0.2:8043")
	f.StringVar(&h.ClientID, "client-id", "", "client ID of the Open API application")
	f.StringVar(&h.ControllerID, "controller-id", "", "controller ID (omadacId); discovered from the controller when omitted")
	f.BoolVar(&secretStdin, "secret-stdin", false, "read the client secret from standard input")
	f.StringVar(&h.SecretStore, "secret-store", "", "where to keep the secret: keyring, file, or env (default: keyring, falling back to file)")
	f.StringVar(&h.TLSPin, "tls-pin", "", "fingerprint of the controller's self-signed certificate (sha256:...)")
	f.BoolVar(&h.InsecureSkipTLSVerify, "insecure-skip-tls-verify", false, "DANGEROUS: accept any certificate, which lets an attacker read the client secret")
	return cmd
}

func (c *cli) hostAdd(cmd *cobra.Command, h app.NewHost, secretStdin bool) error {
	a, err := c.app()
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	// ask fills in a value that was not given as a flag.
	ask := func(value *string, question, flag string) error {
		if *value != "" {
			return nil
		}
		if !c.Interactive {
			return usagef("%s is required when not running in a terminal", flag)
		}
		answer, err := c.prompt(question)
		if err != nil {
			return err
		}
		if answer == "" {
			return usagef("%s must not be empty", flag)
		}
		*value = answer
		return nil
	}

	if err := ask(&h.Name, "Name for this host (e.g. home): ", "NAME"); err != nil {
		return err
	}
	if err := ask(&h.URL, "Controller address (e.g. https://192.168.0.2:8043): ", "--url"); err != nil {
		return err
	}
	if h.URL, err = app.NormalizeURL(h.URL); err != nil {
		return usageError{err}
	}

	if h.InsecureSkipTLSVerify {
		fmt.Fprintln(c.Err, "WARNING: certificate checking is off for this host. Anyone on the network path can read the client secret.")
	} else if h.TLSPin == "" {
		cert, err := a.CheckCertificate(ctx, h.URL)
		if err != nil {
			return err
		}
		if !cert.Trusted {
			if err := c.confirmCertificate(cert.Fingerprint); err != nil {
				return err
			}
			h.TLSPin = cert.Fingerprint
		}
	}

	if err := ask(&h.ClientID, "Client ID: ", "--client-id"); err != nil {
		return err
	}
	if h.Secret, err = c.readSecret(secretStdin); err != nil {
		return err
	}

	err = a.AddHost(ctx, h)
	if errors.Is(err, app.ErrKeyringUnavailable) && h.SecretStore == "" {
		fmt.Fprintln(c.Err, "This system has no keychain, so the client secret will be kept in a file encrypted with a passphrase.")
		pass, perr := c.newPassphrase()
		if perr != nil {
			return perr
		}
		a.SetPassphrase(func(string) (string, error) { return pass, nil })
		h.SecretStore = secret.KindFile
		err = a.AddHost(ctx, h)
	}
	if err != nil {
		return err
	}

	where := map[string]string{
		secret.KindKeyring: "the system keychain",
		secret.KindFile:    "an encrypted file next to the config",
		secret.KindEnv:     "nowhere; it is read from " + secret.EnvVar + " on each run",
		"":                 "the system keychain",
	}[h.SecretStore]
	fmt.Fprintf(c.Out, "Added host %q (%s). The client secret is kept in %s.\n", h.Name, h.URL, where)
	if a.CurrentHost() == h.Name {
		fmt.Fprintf(c.Out, "%q is now the current host.\n", h.Name)
	}
	return nil
}

// confirmCertificate shows the fingerprint of an untrusted certificate and
// asks whether to pin it.
func (c *cli) confirmCertificate(fingerprint string) error {
	fmt.Fprintf(c.Err, "The controller's certificate is not signed by a trusted authority.\nThat is normal for a controller with a self-signed certificate.\n\n  Fingerprint: %s\n\n", fingerprint)
	if !c.Interactive {
		return usagef("to trust this certificate, run the command again with --tls-pin %s", fingerprint)
	}
	answer, err := c.prompt("Trust this certificate from now on? [y/N] ")
	if err != nil {
		return err
	}
	if a := strings.ToLower(answer); a != "y" && a != "yes" {
		return errCanceled
	}
	return nil
}

// readSecret gets the client secret without it ever being a flag.
func (c *cli) readSecret(fromStdin bool) (string, error) {
	if fromStdin {
		line, err := c.in.ReadString('\n')
		if s := strings.TrimSpace(line); s != "" {
			return s, nil
		}
		if err != nil {
			return "", usagef("no client secret on standard input")
		}
		return "", usagef("the client secret on standard input is empty")
	}
	if s := os.Getenv(secret.EnvVar); s != "" {
		return s, nil
	}
	if !c.Interactive {
		return "", usagef("no client secret: pipe it in with --secret-stdin or set %s", secret.EnvVar)
	}
	s, err := c.promptHidden("Client secret (not shown): ")
	if err != nil {
		return "", err
	}
	if s = strings.TrimSpace(s); s == "" {
		return "", usagef("the client secret must not be empty")
	}
	return s, nil
}

// newPassphrase gets the passphrase for a new encrypted secret file. At a
// terminal it is typed twice.
func (c *cli) newPassphrase() (string, error) {
	if p := os.Getenv(secret.EnvPassphrase); p != "" {
		return p, nil
	}
	if !c.Interactive {
		return "", usagef("no system keychain here: set %s to encrypt the secret in a file, or use --secret-store env", secret.EnvPassphrase)
	}
	first, err := c.promptHidden("Choose a passphrase (not shown): ")
	if err != nil {
		return "", err
	}
	if first == "" {
		return "", usagef("the passphrase must not be empty")
	}
	second, err := c.promptHidden("Repeat the passphrase: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("the two passphrases differ")
	}
	return first, nil
}

func (c *cli) hostListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List saved controllers",
		Args:    noArgs,
		RunE: func(*cobra.Command, []string) error {
			format, err := c.outputFormat()
			if err != nil {
				return err
			}
			a, err := c.app()
			if err != nil {
				return err
			}
			hosts := a.Hosts()
			if format == output.FormatJSON {
				return output.JSON(c.Out, hosts)
			}
			if len(hosts) == 0 {
				fmt.Fprintln(c.Err, "No hosts yet. Add one with: gomada host add")
				return nil
			}
			rows := make([][]string, len(hosts))
			for i, h := range hosts {
				mark := ""
				if h.Current {
					mark = "*"
				}
				rows[i] = []string{mark, h.Name, h.URL, h.SecretStore, output.OrNone(h.Site)}
			}
			return output.Table(c.Out, []string{"", "name", "url", "secret", "last site"}, rows)
		},
	}
}

func (c *cli) hostUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use NAME",
		Short: "Make a controller the current one",
		Args:  exactArgs(1, "NAME"),
		RunE: func(_ *cobra.Command, args []string) error {
			a, err := c.app()
			if err != nil {
				return err
			}
			if err := a.UseHost(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(c.Out, "%q is now the current host.\n", args[0])
			return nil
		},
	}
}

func (c *cli) hostRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove NAME",
		Aliases: []string{"rm"},
		Short:   "Remove a controller and its stored secret",
		Args:    exactArgs(1, "NAME"),
		RunE: func(_ *cobra.Command, args []string) error {
			a, err := c.app()
			if err != nil {
				return err
			}
			name := args[0]
			known := false
			for _, h := range a.Hosts() {
				known = known || h.Name == name
			}
			if known {
				if err := c.confirm(fmt.Sprintf("Remove host %q and its stored secret?", name)); err != nil {
					return err
				}
			}
			if err := a.RemoveHost(name); err != nil {
				return err
			}
			fmt.Fprintf(c.Out, "Removed host %q.\n", name)
			return nil
		},
	}
}

func (c *cli) hostTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test [NAME]",
		Short: "Check that a controller is reachable and the credentials work",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usagef("usage: %s [NAME]", cmd.CommandPath())
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := c.app()
			if err != nil {
				return err
			}
			name := c.host
			if len(args) == 1 {
				name = args[0]
			}
			s, err := a.Connect(name)
			if err != nil {
				return err
			}
			sites, err := s.Sites(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(c.Out, "OK: %s (%s) answers and accepts the credentials. Sites: %d.\n", s.Host(), s.URL(), len(sites))
			return nil
		},
	}
}
