package form

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Form mouse coordinates are local to the form's View. Hosts translate global
// events and send the same WindowSize used for View; geometry is computed from
// state here, never populated by a rendering side effect.
type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool { return x >= r.x && y >= r.y && x < r.x+r.w && y < r.y+r.h }

type span struct{ start, end int }
type formField struct {
	index        int
	label, value string
}
type formHit struct {
	rect
	id    string
	index int
	field bool
}
type formPress struct {
	hit     formHit
	context string
}
type formButton struct{ id, label string }
type footerLayout struct {
	text string
	hits []formHit
}

func visibleStart(length, focus, height int) int {
	capacity := max(1, height-3)
	return max(0, min(focus-capacity/2, length-capacity))
}
func fieldBody(fields []formField, focus int) ([]string, int, map[int]span) {
	var body []string
	focusLine := 0
	spans := map[int]span{}
	for _, f := range fields {
		start := len(body)
		mark := "  "
		if f.index == focus {
			mark = "* "
			focusLine = start
		}
		body = append(body, mark+f.label)
		body = append(body, strings.Split(f.value, "\n")...)
		spans[f.index] = span{start, len(body)}
	}
	return body, focusLine, spans
}
func fieldHits(body []string, focus int, spans map[int]span, w, h int) []formHit {
	if h < 4 {
		return nil
	}
	start := visibleStart(len(body), focus, h)
	end := min(len(body), start+h-3)
	var hits []formHit
	for index, s := range spans {
		a, b := max(s.start, start), min(s.end, end)
		if b > a {
			hits = append(hits, formHit{rect: rect{0, 1 + a - start, w, b - a}, id: fmt.Sprint("field-", index), index: index, field: true})
		}
	}
	return hits
}
func buttonFooter(buttons []formButton, hint string, w, h int) footerLayout {
	var out footerLayout
	x := 0
	for _, b := range buttons {
		label := "[" + b.label + "]"
		if x > 0 {
			out.text += " "
			x++
		}
		width := ansi.StringWidth(label)
		if h >= 4 && x+width <= w {
			out.hits = append(out.hits, formHit{rect: rect{x, h - 1, width, 1}, id: b.id})
		}
		out.text += label
		x += width
	}
	if hint != "" {
		out.text += " · " + hint
	}
	return out
}
func hitAt(hits []formHit, x, y int) (formHit, bool) {
	for _, h := range hits {
		if h.contains(x, y) {
			return h, true
		}
	}
	return formHit{}, false
}
func mouseEvent(msg tea.Msg, hits []formHit, pressed **formPress, context string, onField func(int) tea.Cmd, onAction func(string) tea.Cmd, onWheel func(int) tea.Cmd) (tea.Cmd, bool) {
	switch v := msg.(type) {
	case tea.MouseClickMsg:
		*pressed = nil
		if v.Button != tea.MouseLeft {
			return nil, true
		}
		hit, ok := hitAt(hits, v.X, v.Y)
		if !ok {
			return nil, true
		}
		if hit.field {
			return onField(hit.index), true
		}
		*pressed = &formPress{hit, context}
		return nil, true
	case tea.MouseReleaseMsg:
		press := *pressed
		*pressed = nil
		if v.Button != tea.MouseLeft || press == nil || press.context != context {
			return nil, true
		}
		hit, ok := hitAt(hits, v.X, v.Y)
		if ok && hit.id == press.hit.id {
			return onAction(hit.id), true
		}
		return nil, true
	case tea.MouseWheelMsg:
		*pressed = nil
		delta := 1
		if v.Button == tea.MouseWheelUp {
			delta = -1
		} else if v.Button != tea.MouseWheelDown {
			return nil, true
		}
		return onWheel(delta), true
	case tea.MouseMotionMsg:
		return nil, true
	}
	return nil, false
}
func syntheticKey(key string) tea.KeyPressMsg {
	switch key {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "ctrl+s":
		return tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	case "ctrl+t":
		return tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(key)[0], Text: key}
}

func (p *picker) body() ([]string, int, map[int]span) {
	body := []string{"Filter: " + p.query.View()}
	spans := map[int]span{-1: {0, 1}}
	for i, c := range p.visible() {
		mark := "  "
		if i == p.index {
			mark = "> "
		}
		if p.kind == "dependencies" {
			if p.selected[c.id] {
				mark += "[x] "
			} else {
				mark += "[ ] "
			}
		}
		spans[i] = span{len(body), len(body) + 1}
		body = append(body, mark+safe(c.label))
	}
	if len(body) == 1 {
		body = append(body, "No matching choices")
	}
	return body, p.index + 1, spans
}
func (p *picker) footer(w, h int) footerLayout {
	if p.kind == "dependencies" {
		return buttonFooter([]formButton{{"choose", "Toggle"}, {"apply", "Apply"}, {"back", "Back"}}, "↑↓ select · Enter toggle · Ctrl+S accept", w, h)
	}
	return buttonFooter([]formButton{{"choose", "Choose"}, {"back", "Back"}}, "↑↓ select · Enter choose", w, h)
}
func (p *picker) mouse(msg tea.Msg, w, h int, pressed **formPress, context string, dispatch func(tea.KeyPressMsg) tea.Cmd) (tea.Cmd, bool) {
	body, focus, spans := p.body()
	hits := append(fieldHits(body, focus, spans, w, h), p.footer(w, h).hits...)
	return mouseEvent(msg, hits, pressed, context, func(i int) tea.Cmd {
		if i >= 0 {
			p.index = i
		}
		return p.query.Focus()
	}, func(id string) tea.Cmd {
		switch id {
		case "back":
			return dispatch(syntheticKey("esc"))
		case "apply":
			return dispatch(syntheticKey("ctrl+s"))
		default:
			return dispatch(syntheticKey("enter"))
		}
	}, func(delta int) tea.Cmd { p.index = max(0, min(len(p.visible())-1, p.index+delta*3)); return nil })
}

func (m *TaskModel) body() ([]string, int, map[int]span) {
	var fields []formField
	for i := 0; i < m.count(); i++ {
		value := m.fields[i].View()
		if i == 1 {
			value = m.command.View()
		}
		fields = append(fields, formField{i, taskLabels[i], value})
	}
	return fieldBody(fields, m.focus)
}
func (m *TaskModel) formFooter(w, h int) footerLayout {
	if m.review {
		return buttonFooter([]formButton{{"submit", "Submit"}, {"back", "Back"}, {"cancel", "Cancel"}}, "Ctrl+S submit · Esc edit", w, h)
	}
	return buttonFooter([]formButton{{"review", "Review"}, {"cancel", "Cancel"}}, "Tab fields · Ctrl+O advanced · Ctrl+S review", w, h)
}
func (m *TaskModel) mouseUpdate(msg tea.Msg) (tea.Cmd, bool) {
	ctx := fmt.Sprintf("%d/%d/%d/%t/%t/%d/%p/%d", m.width, m.height, m.focus, m.review, m.advanced, m.generation, m.chooser, m.reviewOffset)
	if m.chooser != nil {
		return m.chooser.mouse(msg, m.width, m.height, &m.pressed, ctx, m.pickerKey)
	}
	hits := m.formFooter(m.width, m.height).hits
	if !m.review {
		body, focus, spans := m.body()
		hits = append(hits, fieldHits(body, focus, spans, m.width, m.height)...)
	}
	return mouseEvent(msg, hits, &m.pressed, ctx, m.focusField, func(id string) tea.Cmd {
		key := "ctrl+s"
		if id == "back" {
			key = "esc"
		} else if id == "cancel" {
			key = "ctrl+c"
		}
		_, cmd := m.Update(syntheticKey(key))
		return cmd
	}, func(delta int) tea.Cmd {
		if m.review {
			m.reviewOffset = max(0, m.reviewOffset+delta*3)
			return nil
		}
		return m.focusField(max(0, min(m.count()-1, m.focus+delta)))
	})
}
func (m *ConnectionModel) body() ([]string, int, map[int]span) {
	var fields []formField
	for _, i := range m.visible() {
		value := m.fields[i].View()
		if i == 0 && m.editing {
			value = "  " + safe(m.fields[i].Value())
		}
		fields = append(fields, formField{i, connectionLabels[i], value})
	}
	return fieldBody(fields, m.focus)
}
func (m *ConnectionModel) formFooter(w, h int) footerLayout {
	if m.review {
		return buttonFooter([]formButton{{"submit", "Save"}, {"back", "Back"}, {"cancel", "Cancel"}}, "Ctrl+S save · Esc edit", w, h)
	}
	return buttonFooter([]formButton{{"review", "Review"}, {"test", "Test"}, {"cancel", "Cancel"}}, "Tab fields · Ctrl+O advanced · Ctrl+T test", w, h)
}
func (m *ConnectionModel) mouseUpdate(msg tea.Msg) (tea.Cmd, bool) {
	ctx := fmt.Sprintf("%d/%d/%d/%t/%t/%d/%p/%d", m.width, m.height, m.focus, m.review, m.advanced, m.generation, m.chooser, m.reviewOffset)
	if m.chooser != nil {
		return m.chooser.mouse(msg, m.width, m.height, &m.pressed, ctx, func(k tea.KeyPressMsg) tea.Cmd { _, cmd := m.Update(k); return cmd })
	}
	hits := m.formFooter(m.width, m.height).hits
	if !m.review {
		body, focus, spans := m.body()
		hits = append(hits, fieldHits(body, focus, spans, m.width, m.height)...)
	}
	return mouseEvent(msg, hits, &m.pressed, ctx, func(index int) tea.Cmd {
		if index == 0 && m.editing {
			return nil
		}
		return m.focusField(index)
	}, func(id string) tea.Cmd {
		key := "ctrl+s"
		switch id {
		case "back":
			key = "esc"
		case "cancel":
			key = "ctrl+c"
		case "test":
			key = "ctrl+t"
		}
		_, cmd := m.Update(syntheticKey(key))
		return cmd
	}, func(delta int) tea.Cmd {
		if m.review {
			m.reviewOffset = max(0, m.reviewOffset+delta*3)
			return nil
		}
		visible := m.visible()
		position := 0
		for i, f := range visible {
			if f == m.focus {
				position = i
			}
		}
		position = max(0, min(len(visible)-1, position+delta))
		if m.editing && visible[position] == 0 {
			position = min(1, len(visible)-1)
		}
		return m.focusField(visible[position])
	})
}
func (m *EditModel) mouseUpdate(msg tea.Msg) (tea.Cmd, bool) {
	ctx := fmt.Sprintf("%d/%d/%d/%t/%t/%d", m.width, m.height, m.focus, m.review, m.advanced, m.reviewOffset)
	buttons := []formButton{{"review", "Review"}, {"cancel", "Cancel"}}
	if m.review {
		buttons = []formButton{{"back", "No, edit"}, {"submit", "Yes, apply"}, {"cancel", "Cancel"}}
	}
	hits := buttonFooter(buttons, "", m.width, m.height).hits
	if !m.review {
		body, focus, spans := m.body()
		hits = append(hits, fieldHits(body, focus, spans, m.width, m.height)...)
	}
	return mouseEvent(msg, hits, &m.pressed, ctx, m.focusField, func(id string) tea.Cmd {
		key := "ctrl+s"
		switch id {
		case "back":
			key = "esc"
		case "cancel":
			key = "ctrl+c"
		case "submit":
			key = "y"
		}
		_, cmd := m.Update(syntheticKey(key))
		return cmd
	}, func(delta int) tea.Cmd {
		if m.review {
			m.reviewOffset = max(0, m.reviewOffset+delta*3)
			return nil
		}
		return m.focusField(max(0, min(m.count()-1, m.focus+delta)))
	})
}
