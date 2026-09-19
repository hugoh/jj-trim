package review

import (
	"fmt"

	"charm.land/bubbles/v2/key"
)

// reservedKeys already mean something on the list screen.
var reservedKeys = []string{"q", "u"} //nolint:gochecknoglobals // read-only

type keyMap struct {
	mark, cascade, unmark, next, cancel, scroll, page, help key.Binding
}

func newKeyMap(action Action) keyMap {
	for _, reserved := range reservedKeys {
		if action.markKey() == reserved ||
			action.CascadeAction != nil && action.CascadeAction.markKey() == reserved {
			panic(fmt.Sprintf("review: mark key %q is reserved", reserved))
		}
	}

	k := keyMap{
		mark:   markBinding(action),
		unmark: key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "unmark")),
		next:   key.NewBinding(key.WithKeys(keyEnter), key.WithHelp(keyEnter, "next")),
		cancel: key.NewBinding(key.WithKeys("q", keyEsc), key.WithHelp("q/esc", "cancel")),
		scroll: key.NewBinding(
			key.WithKeys("ctrl+j", "ctrl+k"),
			key.WithHelp("ctrl+j/k", "scroll"),
		),
		page: key.NewBinding(key.WithKeys("ctrl+u", "ctrl+d"), key.WithHelp("ctrl+u/d", "page")),
		help: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	}

	if action.CascadeAction != nil {
		k.cascade = markBinding(*action.CascadeAction)
	} else {
		k.cascade = key.NewBinding(key.WithDisabled())
	}

	return k
}

func markBinding(a Action) key.Binding {
	return key.NewBinding(key.WithKeys(a.markKey()), key.WithHelp(a.markKey(), a.Verb))
}

// ShortHelp implements help.KeyMap; help stays last so fitHelp can always keep it.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.mark, k.cascade, k.unmark, k.next, k.cancel, k.scroll, k.page, k.help}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

func helpEntry(keys, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(keys), key.WithHelp(keys, desc))
}

func (m *model) overlayHelp() []key.Binding {
	var screenKeys []key.Binding

	switch m.screen {
	case screenList:
		screenKeys = m.listHelp()
	case screenConfirm:
		screenKeys = []key.Binding{
			helpEntry(keyEnter, "apply the marked items"),
			helpEntry(keyEsc, "back to the list"),
			helpEntry("↑/↓ pgup/pgdn", "scroll the list"),
			helpEntry("q", "cancel"),
		}
	case screenApplied:
		screenKeys = m.appliedHelp()
	}

	return append(screenKeys, m.keys.help)
}

func (m *model) listHelp() []key.Binding {
	out := []key.Binding{
		helpEntry(m.action.markKey(), "mark to "+m.action.Verb),
	}

	if c := m.action.CascadeAction; c != nil {
		out = append(out, helpEntry(c.markKey(),
			fmt.Sprintf("mark to %s, then %s its private chain", m.action.Verb, c.Verb)))
	}

	out = append(out,
		helpEntry("u", "unmark this item"),
		helpEntry("↑/↓ j/k", "move"),
		helpEntry(keyEnter, "continue to the confirm screen"),
		helpEntry("ctrl+j/k", "scroll the detail pane"),
		helpEntry("ctrl+u/d", "detail pane: half page"),
		helpEntry("q/esc", "cancel (asks again if items are marked)"),
	)

	return append(out, m.extraHelp...)
}

func (m *model) appliedHelp() []key.Binding {
	out := []key.Binding{helpEntry("q", "quit")}

	if m.showingOpLog() {
		return append([]key.Binding{
			helpEntry("enter/esc", "continue"),
			helpEntry("↑/↓ pgup/pgdn", "scroll the op log"),
		}, out...)
	}

	return append([]key.Binding{helpEntry("any key", "continue")}, out...)
}
