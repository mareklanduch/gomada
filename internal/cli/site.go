package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mareklanduch/gomada/internal/app"
	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/output"
)

func (c *cli) siteCmd() *cobra.Command {
	cmd := group("site", "List and choose sites")
	cmd.AddCommand(c.siteListCmd(), c.siteUseCmd())
	return cmd
}

// connect opens a session with the host chosen by --host.
func (c *cli) connect() (*app.Session, error) {
	a, err := c.app()
	if err != nil {
		return nil, err
	}
	return a.Connect(c.host)
}

// connectSite opens a session and resolves the site chosen by --site.
func (c *cli) connectSite(cmd *cobra.Command) (*app.Session, omada.Site, error) {
	s, err := c.connect()
	if err != nil {
		return nil, omada.Site{}, err
	}
	site, err := s.ResolveSite(cmd.Context(), c.site)
	if err != nil {
		return nil, omada.Site{}, err
	}
	return s, site, nil
}

type siteJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

func (c *cli) siteListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List the sites of the host",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			format, err := c.outputFormat()
			if err != nil {
				return err
			}
			s, err := c.connect()
			if err != nil {
				return err
			}
			sites, err := s.Sites(cmd.Context())
			if err != nil {
				return err
			}
			last := s.LastSite()
			if len(sites) == 1 {
				last = sites[0].Name
			}
			if format == output.FormatJSON {
				out := make([]siteJSON, len(sites))
				for i, site := range sites {
					out[i] = siteJSON{ID: site.ID, Name: site.Name, Current: site.Name == last}
				}
				return output.JSON(c.Out, out)
			}
			rows := make([][]string, len(sites))
			for i, site := range sites {
				mark := ""
				if site.Name == last {
					mark = "*"
				}
				rows[i] = []string{mark, site.Name, site.ID}
			}
			return output.Table(c.Out, []string{"", "name", "id"}, rows)
		},
	}
}

func (c *cli) siteUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use SITE",
		Short: "Choose the site that commands work on",
		Args:  exactArgs(1, "SITE"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := c.connect()
			if err != nil {
				return err
			}
			site, err := s.ResolveSite(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if err := s.RememberSite(site); err != nil {
				return err
			}
			fmt.Fprintf(c.Out, "Commands on host %q now use site %q.\n", s.Host(), site.Name)
			return nil
		},
	}
}

// session is an open session together with the site it works on.
type session struct {
	*app.Session
	site omada.Site
}
