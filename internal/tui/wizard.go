package tui

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/mareklanduch/gomada/internal/app"
	"github.com/mareklanduch/gomada/internal/omada"
	"github.com/mareklanduch/gomada/internal/secret"
)

// wizard is the state of the "add a controller" screen. It asks one question
// at a time in the bottom line, using the same prompt as everything else.
type wizard struct {
	host    app.NewHost
	hint    string // explanation of the current question
	err     string // why the last answer was not accepted
	working string // what is being checked, while waiting for the controller
}

// Messages that bring the wizard's background checks back.
type (
	certMsg struct {
		cert app.Certificate
		err  error
	}
	addedMsg struct{ err error }
)

const hintOpenAPI = "Create an Open API application in the controller first: Settings > Platform Integration > Open API, in client credentials mode. It shows the client ID and the client secret."

func (m *Model) startWizard() {
	m.screen = screenWizard
	m.wiz = wizard{}
	m.wizName()
}

// wizCancel leaves the wizard: back to the host list, or out of the program
// when there is no host to go back to.
func (m *Model) wizCancel() tea.Cmd {
	m.wiz = wizard{}
	if len(m.app.Hosts()) == 0 {
		return tea.Quit
	}
	m.showHosts()
	return nil
}

func (m *Model) wizName() {
	m.wiz.hint = "Pick a short name for this controller, for example: home"
	m.ask("Name", m.wiz.host.Name, false, func(name string) tea.Cmd {
		if err := m.app.ValidateHostName(name); err != nil {
			m.wiz.err = err.Error()
			m.wizName()
			return nil
		}
		m.wiz.host.Name, m.wiz.err = name, ""
		m.wizURL()
		return nil
	}, m.wizCancel)
}

func (m *Model) wizURL() {
	m.wiz.hint = "The address you open the controller at, for example: https://192.168.0.2:8043"
	m.ask("Address", m.wiz.host.URL, false, func(raw string) tea.Cmd {
		url, err := app.NormalizeURL(raw)
		if err != nil {
			m.wiz.err = err.Error()
			m.wizURL()
			return nil
		}
		m.wiz.host.URL, m.wiz.host.TLSPin, m.wiz.err = url, "", ""
		m.wiz.working = "Contacting " + url + "…"
		ctx, a := m.ctx, m.app
		return func() tea.Msg {
			cert, err := a.CheckCertificate(ctx, url)
			return certMsg{cert: cert, err: err}
		}
	}, m.wizCancel)
}

func (m *Model) wizCertChecked(msg certMsg) {
	if m.screen != screenWizard || m.wiz.working == "" {
		return
	}
	m.wiz.working = ""
	switch {
	case msg.err != nil:
		m.wiz.err = msg.err.Error()
		m.wizURL()
	case msg.cert.Trusted:
		m.wizClientID()
	default:
		m.wiz.hint = "The controller's certificate is not signed by a trusted authority. That is normal for a self-signed certificate. Its fingerprint is:\n\n" +
			msg.cert.Fingerprint + "\n\nIf you trust it, gomada will accept only this certificate from now on."
		m.confirm("Trust this certificate?", func() tea.Cmd {
			m.wiz.host.TLSPin = msg.cert.Fingerprint
			m.wizClientID()
			return nil
		}, func() tea.Cmd {
			m.wiz.err = "The certificate was not trusted."
			m.wizURL()
			return nil
		})
	}
}

func (m *Model) wizClientID() {
	m.wiz.hint = hintOpenAPI
	m.ask("Client ID", m.wiz.host.ClientID, false, func(id string) tea.Cmd {
		if id == "" {
			m.wiz.err = "The client ID must not be empty."
			m.wizClientID()
			return nil
		}
		m.wiz.host.ClientID, m.wiz.err = id, ""
		m.wizSecret()
		return nil
	}, m.wizCancel)
}

func (m *Model) wizSecret() {
	m.wiz.hint = hintOpenAPI + "\n\nThe secret is not shown while you type, and it is never written to the config file."
	m.ask("Client secret", "", true, func(s string) tea.Cmd {
		if s = strings.TrimSpace(s); s == "" {
			m.wiz.err = "The client secret must not be empty."
			m.wizSecret()
			return nil
		}
		m.wiz.host.Secret, m.wiz.err = s, ""
		return m.wizAdd()
	}, m.wizCancel)
}

// wizAdd checks the credentials against the controller and saves the host.
func (m *Model) wizAdd() tea.Cmd {
	m.wiz.working = "Checking the credentials…"
	ctx, a, host := m.ctx, m.app, m.wiz.host
	return func() tea.Msg { return addedMsg{err: a.AddHost(ctx, host)} }
}

func (m *Model) wizAdded(msg addedMsg) tea.Cmd {
	if m.screen != screenWizard || m.wiz.working == "" {
		return nil
	}
	m.wiz.working = ""
	name := m.wiz.host.Name
	switch {
	case msg.err == nil:
		m.wiz = wizard{}
		m.loadHosts()
		return m.connect(name)
	case errors.Is(msg.err, app.ErrKeyringUnavailable) && m.wiz.host.SecretStore == "":
		m.wiz.err = ""
		m.wizPassphrase()
	case errors.Is(msg.err, omada.ErrAuth):
		m.wiz.err = msg.err.Error()
		m.wizClientID()
	case errors.Is(msg.err, omada.ErrUnreachable), errors.Is(msg.err, omada.ErrCertificate):
		m.wiz.err = msg.err.Error()
		m.wizURL()
	default:
		m.wiz.err = msg.err.Error()
		m.wizName()
	}
	return nil
}

// wizPassphrase asks, twice, for the passphrase that encrypts the secret on
// a system without a keychain.
func (m *Model) wizPassphrase() {
	m.wiz.hint = "This system has no keychain, so the client secret will be kept in a file encrypted with a passphrase. You will be asked for the passphrase when gomada starts."
	m.ask("Choose a passphrase", "", true, func(first string) tea.Cmd {
		if first == "" {
			m.wiz.err = "The passphrase must not be empty."
			m.wizPassphrase()
			return nil
		}
		m.wiz.err = ""
		m.ask("Repeat the passphrase", "", true, func(second string) tea.Cmd {
			if first != second {
				m.wiz.err = "The two passphrases differ."
				m.wizPassphrase()
				return nil
			}
			m.pass.set(m.wiz.host.Name, first)
			m.wiz.host.SecretStore = secret.KindFile
			return m.wizAdd()
		}, m.wizCancel)
		return nil
	}, m.wizCancel)
}

// wizardLines draws the wizard: the answers so far, then the explanation of
// the current question, then any problem with the last answer.
func (m *Model) wizardLines() []string {
	h := m.wiz.host
	var paragraphs []string
	for _, f := range [][2]string{{"Name", h.Name}, {"Address", h.URL}, {"Client ID", h.ClientID}} {
		if f[1] != "" {
			paragraphs = append(paragraphs, styleFaint.Render(f[0]+":")+" "+f[1])
		}
	}
	if len(paragraphs) > 0 {
		paragraphs = append(paragraphs, "")
	}
	if m.wiz.err != "" {
		paragraphs = append(paragraphs, styleError.Render("Error: "+m.wiz.err), "")
	}
	if m.wiz.working != "" {
		paragraphs = append(paragraphs, m.wiz.working)
	} else {
		paragraphs = append(paragraphs, m.wiz.hint)
	}
	return m.wrapAll(paragraphs)
}
