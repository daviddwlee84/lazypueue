package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type mouseHit struct {
	Kind, Token string
	Index       int
	Key         string
	Action      action
}
type mousePress struct {
	Hit    mouseHit
	Layout string
}
type uiButton struct{ Label, Key string }

func translateMouse(msg tea.Msg, x, y int) tea.Msg {
	var mouse tea.Mouse
	v, ok := msg.(tea.MouseMsg)
	if !ok {
		return msg
	}
	mouse = v.Mouse()
	mouse.X -= x
	mouse.Y -= y
	switch msg.(type) {
	case tea.MouseClickMsg:
		return tea.MouseClickMsg(mouse)
	case tea.MouseReleaseMsg:
		return tea.MouseReleaseMsg(mouse)
	case tea.MouseWheelMsg:
		return tea.MouseWheelMsg(mouse)
	case tea.MouseMotionMsg:
		return tea.MouseMotionMsg(mouse)
	}
	return msg
}
func (m *model) footerButtons() []uiButton {
	if m.taskForm != nil || m.connectionForm != nil || m.editForm != nil || m.formPending {
		return nil
	}
	switch m.overlay {
	case "confirm":
		return []uiButton{{"No", "n"}, {"Yes", "y"}}
	case "connections":
		return []uiButton{{"Open", "enter"}, {"Add", "a"}, {"Edit", "e"}, {"Test", "t"}, {"Remove", "d"}, {"Back", "esc"}}
	case "actions", "status", "log-settings":
		return []uiButton{{"Select", "enter"}, {"Back", "esc"}}
	case "upgrade-review":
		return []uiButton{{"No", "n"}, {"Yes", "y"}}
	case "upgrade-running":
		return []uiButton{{"Return to dashboard", "esc"}, {"Cancel upgrade", "ctrl+c"}}
	case "upgrade-loading":
		return []uiButton{{"Back", "esc"}, {"Cancel", "ctrl+c"}}
	case "help", "events", "task-info":
		return []uiButton{{"Back", "esc"}}
	}
	if m.inputMode != "" {
		return nil
	}
	if m.log.Open {
		return []uiButton{{"Back", "esc"}, {"Pause", " "}, {"Mode", "L"}, {"Copy", "y"}, {"Report", "Y"}, {"Info", "i"}, {"Edit", "e"}}
	}
	if m.monitor {
		return []uiButton{{"Add", "a"}, {"Expand", "enter"}, {"Pause", " "}, {"Mode", "L"}, {"Remove watch", "x"}}
	}
	if m.focus == 2 && m.tab == 0 {
		return []uiButton{{"Expand", "enter"}, {"Mode", "L"}, {"Copy", "y"}, {"Report", "Y"}, {"Info", "i"}, {"Help", "?"}}
	}
	return []uiButton{{"Add n", "n"}, {"Actions :", ":"}, {"Connections t", "t"}, {"Monitor 4", "4"}, {"Help ?", "?"}}
}
func (m *model) mouseLayoutToken() string {
	return fmt.Sprintf("%d/%d/%d/%d/%t/%t/%s/%s", m.width, m.height, m.focus, m.tab, m.monitor, m.log.Open, m.overlay, m.viewKey())
}
func (m *model) hitTest(x, y int) mouseHit {
	if m.width < 16 || m.height < 10 {
		return mouseHit{}
	}
	if y == m.height-1 {
		pos := 0
		for _, b := range m.footerButtons() {
			end := pos + len(b.Label) + 2
			if x >= pos && x < end {
				return mouseHit{Kind: "button", Token: "button:" + b.Key, Key: b.Key}
			}
			pos = end + 1
		}
	}
	if m.overlay != "" {
		switch m.overlay {
		case "actions":
			items := m.filteredActions()
			start := max(0, m.menuIndex-max(1, m.height-10)+1)
			i := y - 5 + start
			if y >= 5 && y < m.height-4 && i >= 0 && i < len(items) {
				return mouseHit{Kind: "action", Token: "action:" + items[i].ID + items[i].Label, Index: i, Action: items[i]}
			}
		case "connections":
			start := max(0, m.menuIndex-max(1, m.height-8)+1)
			i := y - 3 + start
			if y >= 3 && y < m.height-4 && i >= 0 && i <= len(m.cfg.Connections) {
				token := "all"
				if i > 0 {
					token = m.cfg.Connections[i-1].ID
				}
				return mouseHit{Kind: "connection", Token: "connection:" + token, Index: i}
			}
		case "status":
			i := y - 3
			if y >= 3 && i >= 0 && i < 8 {
				return mouseHit{Kind: "status", Token: fmt.Sprint("status:", i), Index: i}
			}
		case "log-settings":
			start := max(0, m.menuIndex-max(1, m.height-10)+1)
			i := y - 6 + start
			if y >= 6 && y < m.height-4 && i >= 0 && i < 9 {
				return mouseHit{Kind: "log-setting", Token: fmt.Sprint("log-setting:", i), Index: i}
			}
		}
		return mouseHit{Kind: "modal", Token: "modal"}
	}
	if m.log.Open {
		return mouseHit{Kind: "log", Token: m.log.Key}
	}
	if m.monitor {
		start, _ := m.monitorRange()
		for i, r := range m.monitorRects() {
			if r.contains(x, y) {
				idx := start + i
				w := m.watches[idx]
				return mouseHit{Kind: "watch", Token: fmt.Sprintf("watch:%s/%d/%s", w.Connection, w.TaskID, w.CreatedAt), Index: idx}
			}
		}
		return mouseHit{}
	}
	g := m.geometry()
	if g.Scope.contains(x, y) {
		i := y - g.Scope.Y - 1 + max(0, m.sidebarIndex-max(1, g.Scope.H-2)+1)
		rows := m.scopes()
		if y > g.Scope.Y && y < g.Scope.Y+g.Scope.H-1 && i >= 0 && i < len(rows) {
			r := rows[i]
			return mouseHit{Kind: "scope", Token: r.Connection + "/" + r.Group, Index: i}
		}
		return mouseHit{Kind: "focus", Token: "focus:0", Index: 0}
	}
	if g.List.contains(x, y) {
		offset := y - g.List.Y - 1
		if m.tab == 0 {
			rows := m.displayedTasks()
			if offset >= 0 && offset < len(rows) && rows[offset].Index >= 0 {
				return mouseHit{Kind: "task", Token: rows[offset].Key, Index: rows[offset].Index}
			}
		} else {
			rows := m.groups()
			i := offset + max(0, m.currentView().Index-max(1, g.List.H-2)+1)
			if offset >= 0 && offset < g.List.H-2 && i >= 0 && i < len(rows) {
				return mouseHit{Kind: "group", Token: rows[i].key(), Index: i}
			}
		}
		return mouseHit{Kind: "focus", Token: "focus:1", Index: 1}
	}
	if g.Detail.contains(x, y) {
		return mouseHit{Kind: "detail", Token: "detail", Index: 2}
	}
	return mouseHit{}
}
func (m *model) mouseEvent(msg tea.Msg) tea.Cmd {
	if !m.mouse {
		return nil
	}
	v, ok := msg.(tea.MouseMsg)
	if !ok {
		return nil
	}
	mouse := v.Mouse()
	hit := m.hitTest(mouse.X, mouse.Y)
	if wheel, ok := msg.(tea.MouseWheelMsg); ok {
		delta := 3
		if wheel.Button == tea.MouseWheelUp {
			delta = -3
		}
		if m.overlay != "" {
			switch m.overlay {
			case "actions":
				m.menuIndex = clamp(m.menuIndex+delta, 0, len(m.filteredActions())-1)
			case "connections":
				m.menuIndex = clamp(m.menuIndex+delta, 0, len(m.cfg.Connections))
			case "status":
				m.menuIndex = clamp(m.menuIndex+delta, 0, 7)
			case "log-settings":
				m.menuIndex = clamp(m.menuIndex+delta, 0, 8)
			default:
				m.menuIndex = max(0, m.menuIndex+delta)
			}
			return nil
		}
		switch hit.Kind {
		case "log":
			m.scrollLog(m.log, delta)
		case "watch":
			m.monitorIndex = hit.Index
			if s := m.monitorSession(); s != nil {
				m.log = s
				m.scrollLog(s, delta)
			}
		case "detail":
			if m.tab == 0 {
				m.scrollLog(m.log, delta)
			} else {
				m.detailOffset = max(0, m.detailOffset+delta)
			}
		case "scope", "focus":
			old := m.focus
			m.focus = hit.Index
			if hit.Kind == "scope" {
				m.focus = 0
			}
			cmd := m.move(delta)
			m.focus = old
			return cmd
		case "task", "group":
			old := m.focus
			m.focus = 1
			cmd := m.move(delta)
			m.focus = old
			return cmd
		}
		return nil
	}
	if _, ok := msg.(tea.MouseClickMsg); ok && mouse.Button == tea.MouseLeft {
		m.pressed = &mousePress{hit, m.mouseLayoutToken()}
		switch hit.Kind {
		case "task", "group":
			m.focus = 1
			m.currentView().Index = hit.Index
			m.currentView().Selected = hit.Token
			m.pressed.Layout = m.mouseLayoutToken()
			return m.preview()
		case "scope":
			m.focus = 0
			m.sidebarIndex = hit.Index
			m.pressed.Layout = m.mouseLayoutToken()
		case "detail":
			m.focus = 2
			m.pressed.Layout = m.mouseLayoutToken()
		case "focus":
			m.focus = hit.Index
			m.pressed.Layout = m.mouseLayoutToken()
		case "watch":
			m.monitorIndex = hit.Index
		case "action", "connection", "status", "log-setting":
			m.menuIndex = hit.Index
		}
		return nil
	}
	if _, ok := msg.(tea.MouseReleaseMsg); ok && mouse.Button == tea.MouseLeft {
		press := m.pressed
		m.pressed = nil
		if press == nil || press.Hit.Token == "" || press.Hit.Token != hit.Token || press.Layout != m.mouseLayoutToken() {
			return nil
		}
		switch hit.Kind {
		case "button":
			return m.key(keyMessage(hit.Key))
		case "action":
			m.overlay = ""
			m.input.Blur()
			return m.perform(hit.Action)
		case "connection", "status", "log-setting":
			return m.overlayKey(keyMessage("enter"))
		case "scope":
			rows := m.scopes()
			if hit.Index < len(rows) {
				r := rows[hit.Index]
				return m.switchScope(r.Connection, r.Group)
			}
		}
	}
	return nil
}
func keyMessage(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case " ":
		return tea.KeyPressMsg{Code: ' ', Text: " "}
	}
	if strings.HasPrefix(s, "ctrl+") {
		return tea.KeyPressMsg{Code: []rune(s[5:])[0], Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}
