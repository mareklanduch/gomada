package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// lineInput is a one-line text field with the cursor always at the end.
// That is all the UI needs for a filter, a name, or a passphrase.
type lineInput struct {
	text   []rune
	masked bool // show bullets instead of the text
}

func newInput(initial string, masked bool) lineInput {
	return lineInput{text: []rune(initial), masked: masked}
}

func (l *lineInput) value() string { return string(l.text) }

// key applies a key press and reports whether it changed the text.
func (l *lineInput) key(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "backspace":
		if len(l.text) == 0 {
			return false
		}
		l.text = l.text[:len(l.text)-1]
		return true
	case "ctrl+u":
		l.text = nil
		return true
	}
	if msg.Text == "" || msg.Mod&^tea.ModShift != 0 {
		return false
	}
	l.text = append(l.text, []rune(msg.Text)...)
	return true
}

// paste appends pasted text, without line breaks.
func (l *lineInput) paste(s string) {
	s = strings.NewReplacer("\r", "", "\n", "").Replace(s)
	l.text = append(l.text, []rune(s)...)
}

const cursorBlock = "█"

func (l *lineInput) view() string {
	if l.masked {
		return strings.Repeat("•", len(l.text)) + cursorBlock
	}
	return string(l.text) + cursorBlock
}
