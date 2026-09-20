package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type rect struct{ X, Y, W, H int }

func (r rect) contains(x, y int) bool {
	return r.W > 0 && r.H > 0 && x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

type layout struct{ Scope, List, Detail rect }

func (m *model) geometry() layout {
	w, h := max(1, m.width), max(0, m.height-5)
	g := layout{}
	if w >= 120 {
		left := 24
		middle := (w - left) * 53 / 100
		g.Scope = rect{0, 2, left, h}
		g.List = rect{left, 2, middle, h}
		g.Detail = rect{left + middle, 2, w - left - middle, h}
	} else if w >= 80 {
		if m.focus == 0 {
			g.Scope = rect{0, 2, 24, h}
			g.List = rect{24, 2, w - 24, h}
		} else {
			top := min(h, max(3, h*3/5))
			g.List = rect{0, 2, w, top}
			g.Detail = rect{0, 2 + top, w, max(0, h-top)}
		}
	} else {
		r := rect{0, 2, w, h}
		switch m.focus {
		case 0:
			g.Scope = r
		case 1:
			g.List = r
		case 2:
			g.Detail = r
		}
	}
	return g
}
func (m *model) monitorRects() []rect {
	start, end := m.monitorRange()
	count := end - start
	if count == 0 {
		return nil
	}
	w, h := m.width, max(1, m.height-5)
	capacity := m.monitorCapacity()
	var out []rect
	if capacity == 4 {
		left, top := w/2, h/2
		all := []rect{{0, 2, left, top}, {left, 2, w - left, top}, {0, 2 + top, left, h - top}, {left, 2 + top, w - left, h - top}}
		return all[:count]
	}
	if capacity == 2 {
		top := h / 2
		out = append(out, rect{0, 2, w, top})
		if count > 1 {
			out = append(out, rect{0, 2 + top, w, h - top})
		}
		return out
	}
	return []rect{{0, 2, w, h}}
}

type displayTaskRow struct {
	Text, Key string
	Index     int
}

func (m *model) taskDisplayRows(w int) ([]displayTaskRow, int) {
	var all []displayTaskRow
	selected := 0
	last := ""
	v := m.currentView()
	for i, r := range m.rows() {
		state := r.Task.State()
		if state != last {
			all = append(all, displayTaskRow{Text: color("── "+strings.ToUpper(state)+" ──", stateColor(state)), Index: -1})
			last = state
		}
		if i == v.Index {
			selected = len(all)
		}
		label := r.Task.Label
		if label == "" {
			label = r.Task.Command
		}
		check := " "
		if _, ok := m.selected[r.key()]; ok {
			check = "x"
		}
		prefix := fmt.Sprintf("%s #%d ", check, r.Task.ID)
		if m.scope == "all" {
			prefix = fmt.Sprintf("%s %s #%d ", check, r.Connection.DisplayName(), r.Task.ID)
		}
		tail := "  " + r.Task.Group + " · " + taskDuration(r.Task)
		if s := m.sessions[r.key()]; s != nil && s.HasProgress {
			tail += fmt.Sprintf(" · %.0f%%", s.Progress.Percent)
		}
		if r.Task.Locked {
			tail += " [locked]"
		}
		text := prefix + ansi.Truncate(clean(label), max(1, w-ansi.StringWidth(prefix)-ansi.StringWidth(tail)-2), "…") + tail
		all = append(all, displayTaskRow{selectedLine(text, i == v.Index, w), r.key(), i})
	}
	return all, selected
}
func (m *model) displayedTasks() []displayTaskRow {
	g := m.geometry()
	all, index := m.taskDisplayRows(max(1, g.List.W-2))
	h := max(0, g.List.H-2)
	start := max(0, index-h+1)
	return all[min(start, len(all)):min(len(all), start+h)]
}
func (m *model) visibleTaskRows() []taskRow {
	if m.monitor || m.tab != 0 || m.log.Open {
		return nil
	}
	byKey := map[string]taskRow{}
	for _, r := range m.rows() {
		byKey[r.key()] = r
	}
	var out []taskRow
	for _, row := range m.displayedTasks() {
		if r, ok := byKey[row.Key]; ok {
			out = append(out, r)
		}
	}
	return out
}
