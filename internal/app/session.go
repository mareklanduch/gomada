package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mareklanduch/gomada/internal/omada"
)

// Session is a connection to one host.
type Session struct {
	app    *App
	name   string
	url    string
	client *omada.Client

	secretOnce func() (string, error)

	fwMu     sync.Mutex
	firmware map[string]firmwareEntry // by device MAC
}

// Connect prepares a session with a host. An empty name means the current
// host. No request is made yet: the session logs in on first use, and only
// if it has no valid cached token.
func (a *App) Connect(name string) (*Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if name == "" {
		name = a.cfg.Current
	}
	if name == "" {
		if len(a.cfg.Hosts) == 0 {
			return nil, ErrNoHosts
		}
		return nil, errors.New("no current host; choose one with: gomada host use NAME")
	}
	h, ok := a.cfg.Hosts[name]
	if !ok {
		return nil, a.unknownHost(name)
	}
	store, err := a.secretStore(name, h.SecretStore)
	if err != nil {
		return nil, fmt.Errorf("host %q: %w", name, err)
	}

	// The secret is read at most once per session and then kept in memory,
	// so renewing a token never asks for a passphrase again. A failed read
	// is not remembered, so it can be retried.
	var (
		mu     sync.Mutex
		cached string
	)
	s := &Session{app: a, name: name, url: h.URL}
	s.secretOnce = func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if cached != "" {
			return cached, nil
		}
		v, err := store.Get(name)
		if err != nil {
			return "", err
		}
		cached = v
		return v, nil
	}
	s.client = omada.New(omada.Options{
		BaseURL:      h.URL,
		ControllerID: h.Controller,
		ClientID:     h.ClientID,
		Secret:       func(context.Context) (string, error) { return s.secretOnce() },
		HTTPClient:   a.httpClient(omada.TLSOptions{Pin: h.TLSPin, Insecure: h.InsecureSkipTLSVerify}),
		Cache:        a.tokenCache(name, h),
		Now:          a.now,
	})
	s.firmware = map[string]firmwareEntry{}
	return s, nil
}

// Host returns the name of the host this session talks to.
func (s *Session) Host() string { return s.name }

// URL returns the controller address.
func (s *Session) URL() string { return s.url }

// Unlock reads the client secret now. The terminal UI calls it at start so
// that a later token renewal never has to stop and ask for a passphrase.
func (s *Session) Unlock() error {
	_, err := s.secretOnce()
	return err
}

// Sites lists the sites of the host.
func (s *Session) Sites(ctx context.Context) ([]omada.Site, error) { return s.client.Sites(ctx) }

// ErrSiteRequired means the host has several sites and none was chosen.
var ErrSiteRequired = errors.New("this host has several sites; choose one with --site")

// ResolveSite finds the site to work on. want is a site name or ID; when it
// is empty the only site is used, or else the site used last on this host.
func (s *Session) ResolveSite(ctx context.Context, want string) (omada.Site, error) {
	sites, err := s.Sites(ctx)
	if err != nil {
		return omada.Site{}, err
	}
	return s.ChooseSite(sites, want)
}

// ChooseSite is ResolveSite for a site list that was already fetched.
func (s *Session) ChooseSite(sites []omada.Site, want string) (omada.Site, error) {
	names := func() string {
		var out []string
		for _, site := range sites {
			out = append(out, site.Name)
		}
		return strings.Join(out, ", ")
	}
	if want == "" {
		switch {
		case len(sites) == 0:
			return omada.Site{}, errors.New("this host has no sites the application may access")
		case len(sites) == 1:
			return sites[0], nil
		}
		if last := s.LastSite(); last != "" {
			if site, err := matchSite(sites, last); err == nil {
				return site, nil
			}
		}
		return omada.Site{}, fmt.Errorf("%w (sites: %s)", ErrSiteRequired, names())
	}
	site, err := matchSite(sites, want)
	if err != nil {
		return omada.Site{}, fmt.Errorf("%w (sites: %s)", err, names())
	}
	return site, nil
}

func matchSite(sites []omada.Site, want string) (omada.Site, error) {
	var found []omada.Site
	for _, site := range sites {
		if site.ID == want {
			return site, nil
		}
		if strings.EqualFold(site.Name, want) {
			found = append(found, site)
		}
	}
	switch len(found) {
	case 0:
		return omada.Site{}, fmt.Errorf("no site named %q", want)
	case 1:
		return found[0], nil
	}
	return omada.Site{}, fmt.Errorf("several sites are named %q; use the site ID", want)
}

// LastSite returns the name of the site used last on this host.
func (s *Session) LastSite() string {
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	if h, ok := s.app.cfg.Hosts[s.name]; ok {
		return h.Site
	}
	return ""
}

// RememberSite records the site as the one used last on this host.
func (s *Session) RememberSite(site omada.Site) error {
	s.app.mu.Lock()
	defer s.app.mu.Unlock()
	h, ok := s.app.cfg.Hosts[s.name]
	if !ok || h.Site == site.Name {
		return nil
	}
	h.Site = site.Name
	return s.app.cfg.Save(s.app.path)
}

// Devices lists the devices of a site, sorted by type and then name.
func (s *Session) Devices(ctx context.Context, siteID string) ([]omada.Device, error) {
	devs, err := s.client.Devices(ctx, siteID)
	if err != nil {
		return nil, err
	}
	sortDevices(devs)
	return devs, nil
}

// FindDevice finds one device by MAC address or name.
func (s *Session) FindDevice(ctx context.Context, siteID, target string) (omada.Device, error) {
	devs, err := s.Devices(ctx, siteID)
	if err != nil {
		return omada.Device{}, err
	}
	return find("device", target, devs,
		func(d omada.Device) string { return d.MAC },
		func(d omada.Device) []string { return []string{d.Name} },
		func(d omada.Device) string { return d.Label() + " (" + d.MAC + ")" })
}

// RebootDevice restarts a device.
func (s *Session) RebootDevice(ctx context.Context, siteID string, d omada.Device) error {
	return s.client.RebootDevice(ctx, siteID, d.MAC)
}

// RenameDevice sets the name of a device.
func (s *Session) RenameDevice(ctx context.Context, siteID string, d omada.Device, name string) error {
	return s.client.RenameDevice(ctx, siteID, d, name)
}

// Clients lists the connected clients of a site, sorted by name.
func (s *Session) Clients(ctx context.Context, siteID string) ([]omada.NetClient, error) {
	clients, err := s.client.Clients(ctx, siteID)
	if err != nil {
		return nil, err
	}
	sortClients(clients)
	return clients, nil
}

// FindClient finds one connected client by MAC address, name, or host name.
func (s *Session) FindClient(ctx context.Context, siteID, target string) (omada.NetClient, error) {
	clients, err := s.Clients(ctx, siteID)
	if err != nil {
		return omada.NetClient{}, err
	}
	return find("client", target, clients,
		func(c omada.NetClient) string { return c.MAC },
		func(c omada.NetClient) []string { return []string{c.Name, c.HostName} },
		func(c omada.NetClient) string { return c.Label() + " (" + c.MAC + ")" })
}

// RenameClient sets the friendly name of a client.
func (s *Session) RenameClient(ctx context.Context, siteID string, c omada.NetClient, name string) error {
	return s.client.RenameClient(ctx, siteID, c.MAC, name)
}

// BlockClient bars a client from the network.
func (s *Session) BlockClient(ctx context.Context, siteID string, c omada.NetClient) error {
	return s.client.BlockClient(ctx, siteID, c.MAC)
}

// UnblockClient lets a blocked client back on the network. A blocked client
// is not connected and so cannot be looked up by name: target must be its
// MAC address.
func (s *Session) UnblockClient(ctx context.Context, siteID, target string) error {
	mac, ok := FormatMAC(target)
	if !ok {
		return fmt.Errorf("%q is not a MAC address; a blocked client is offline, so it can only be unblocked by MAC address", target)
	}
	return s.client.UnblockClient(ctx, siteID, mac)
}

// ReconnectClient forces a wireless client to re-associate.
func (s *Session) ReconnectClient(ctx context.Context, siteID string, c omada.NetClient) error {
	if !c.Wireless {
		return fmt.Errorf("%s is a wired client; only wireless clients can be reconnected", c.Label())
	}
	return s.client.ReconnectClient(ctx, siteID, c.MAC)
}
