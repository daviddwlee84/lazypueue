package tui

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazypueue/internal/core"
)

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(s))
}
func fit(s string, w int) string {
	w = max(0, w)
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(0, w-ansi.StringWidth(s)))
}
func color(s, c string) string {
	if os.Getenv("NO_COLOR") != "" {
		return s
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render(s)
}
func selectedLine(s string, selected bool, w int) string {
	prefix := "  "
	if selected {
		prefix = "> "
	}
	s = fit(prefix+clean(s), w)
	if selected {
		return color(s, "117")
	}
	return s
}
func block(lines []string, w, h int) string {
	var out []string
	for i := 0; i < max(0, h); i++ {
		s := ""
		if i < len(lines) {
			s = lines[i]
		}
		out = append(out, fit(s, w))
	}
	return strings.Join(out, "\n")
}
func frame(title string, lines []string, w, h int, focused bool) string {
	if w < 3 || h < 3 {
		return block(append([]string{clean(title)}, lines...), w, h)
	}
	inner := w - 2
	marker := " "
	if focused {
		marker = "* "
	}
	title = ansi.Truncate(marker+clean(title)+" ", inner, "")
	top := "┌" + title + strings.Repeat("─", max(0, inner-ansi.StringWidth(title))) + "┐"
	bottom := "└" + strings.Repeat("─", inner) + "┘"
	if focused {
		top = color(top, "75")
		bottom = color(bottom, "75")
	}
	out := []string{top}
	for i := 0; i < h-2; i++ {
		s := ""
		if i < len(lines) {
			s = lines[i]
		}
		out = append(out, "│"+fit(s, inner)+"│")
	}
	return strings.Join(append(out, bottom), "\n")
}
func sideBySide(parts ...string) string { return lipgloss.JoinHorizontal(lipgloss.Top, parts...) }
func duration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
func taskDuration(t core.Task) string {
	if t.StartedAt == nil {
		return "—"
	}
	end := time.Now()
	if t.EndedAt != nil {
		end = *t.EndedAt
	}
	return duration(end.Sub(*t.StartedAt))
}
func stateColor(s string) string {
	switch s {
	case "running":
		return "42"
	case "queued":
		return "75"
	case "paused", "stashed", "locked":
		return "214"
	case "failed":
		return "203"
	case "succeeded":
		return "108"
	}
	return "245"
}
func (m *model) View() tea.View {
	w, h := max(1, m.width), max(1, m.height)
	if w < 16 || h < 10 {
		v := tea.NewView(block([]string{"lazypueue", "Resize terminal", "q quit"}, w, h))
		v.AltScreen = true
		return v
	}
	name := "All connections"
	if c, ok := m.connection(m.scope); ok {
		name = c.DisplayName() + " · " + c.Kind
	}
	if m.group != "" {
		name += " / " + m.group
	}
	tabs := "[1 Tasks]  2 Groups  4 Monitor"
	if m.tab == 1 {
		tabs = "1 Tasks  [2 Groups]  4 Monitor"
	}
	if m.monitor {
		tabs = "1 Tasks  2 Groups  [4 Monitor]"
	}
	header := color(fit("lazypueue  "+tabs+"   "+clean(name), w), "75")
	summary := m.summaryLine(w)
	contentH := h - 5
	var content string
	if m.taskForm != nil {
		content = block(strings.Split(m.taskForm.View(w, contentH), "\n"), w, contentH)
	} else if m.connectionForm != nil {
		content = block(strings.Split(m.connectionForm.View(w, contentH), "\n"), w, contentH)
	} else if m.editForm != nil {
		content = block(strings.Split(m.editForm.View(w, contentH), "\n"), w, contentH)
	} else if m.overlay != "" {
		content = m.overlayView(w, contentH)
	} else if m.log.Open {
		content = m.logView(w, contentH)
	} else if m.monitor {
		content = m.monitorView(w, contentH)
	} else {
		g := m.geometry()
		pane := func(r rect, title string, lines []string, focused bool) string {
			return frame(title, lines, r.W, r.H, focused)
		}
		scope := pane(g.Scope, "0 Scope", m.scopeLines(g.Scope.W-2, g.Scope.H-2), m.focus == 0)
		list := pane(g.List, m.listTitle(), m.listLines(g.List.W-2, g.List.H-2), m.focus == 1)
		detail := pane(g.Detail, "3 Details / Log", m.activeDetailLines(g.Detail.W-2, g.Detail.H-2), m.focus == 2)
		if g.Scope.W > 0 && g.List.W > 0 {
			if g.Detail.W > 0 {
				content = sideBySide(scope, list, detail)
			} else {
				content = sideBySide(scope, list)
			}
		} else if g.List.H > 0 && g.Detail.H > 0 {
			content = list + "\n" + detail
		} else if g.Scope.W > 0 {
			content = scope
		} else if g.List.W > 0 {
			content = list
		} else {
			content = detail
		}
	}

	status := clean(m.status)
	if m.formPending {
		status = "Applying… Ctrl+C cancels waiting; accepted tasks are not rolled back."
	} else if m.formUnknown {
		status = "Outcome unknown · inspect queue before retrying · Esc returns"
	} else if m.prefix {
		status = "g … (g first row, Esc cancel)"
	}
	inputLine := ""
	if m.inputMode != "" {
		inputLine = m.input.View()
	} else {
		v := m.currentView()
		if v.Query != "" {
			inputLine = "Filter: " + clean(v.Query) + "  "
		}
		if v.State != "" {
			inputLine += "Status: " + v.State + "  "
		}
		if len(m.selected) > 0 {
			inputLine += fmt.Sprintf("%d selected · Space toggle · Esc clear", len(m.selected))
		}
		if inputLine == "" {
			inputLine = "0 Scope · 1 Tasks · 2 Groups · 3 Log · 4 Monitor · Tab focus"
		}
	}
	foot := m.footer()
	view := tea.NewView(block([]string{header, summary}, w, 2) + "\n" + content + "\n" + fit(inputLine, w) + "\n" + color(fit(status, w), "214") + "\n" + fit(foot, w))
	view.AltScreen = true
	if m.mouse {
		view.MouseMode = tea.MouseModeCellMotion
	}
	return view
}
func (m *model) summaryLine(w int) string {
	if m.monitor {
		return fit(fmt.Sprintf("%d watched · page %d/%d · Tab tile · [ / ] pages · polling is a replaceable tail snapshot", len(m.watches), m.monitorPage+1, max(1, (len(m.watches)+m.monitorCapacity()-1)/m.monitorCapacity())), w)
	}
	var snap core.Snapshot
	fresh, total := 0, 0
	for _, c := range m.cfg.Connections {
		if m.scope != "all" && m.scope != c.ID {
			continue
		}
		total++
		s := m.states[c.ID]
		if s == nil || s.Err != nil || s.Snapshot.ObservedAt.IsZero() {
			continue
		}
		fresh++
		snap.Tasks = append(snap.Tasks, s.Snapshot.Tasks...)
	}
	s := core.Summarize(snap, m.group, time.Now())
	if w < 100 {
		return fit(fmt.Sprintf("Run %d · Queue %d · Fail %d · Done %d/%d %s %.0f%% · %d/%d hosts", s.Running, s.Queued, s.Failed, s.Finished, s.Total, summaryProgressBar(s, 8), s.Progress*100, fresh, total), w)
	}
	return fit(fmt.Sprintf("Running %d · Queued %d · Paused %d · Failed %d · Done %d/%d %s %.0f%% | %d/%d connections fresh", s.Running, s.Queued, s.Paused, s.Failed, s.Finished, s.Total, summaryProgressBar(s, 10), s.Progress*100, fresh, total), w)
}
func (m *model) scopeLines(w, h int) []string {
	rows := m.scopes()
	start := max(0, m.sidebarIndex-max(1, h)+1)
	var out []string
	for i := start; i < len(rows) && len(out) < h; i++ {
		r := rows[i]
		s := r.Label
		if r.Group == "" && r.Connection != "all" {
			st := m.states[r.Connection]
			if st != nil {
				if st.Err != nil {
					s += " !"
				} else if st.Pending && st.Snapshot.ObservedAt.IsZero() {
					s += " …"
				} else {
					sum := core.Summarize(st.Snapshot, "", time.Now())
					s += fmt.Sprintf(" [%d/%d]", sum.Running, sum.Queued)
				}
			}
		}
		if r.Connection == m.scope && r.Group == m.group {
			s = "● " + s
		}
		out = append(out, selectedLine(s, m.focus == 0 && i == m.sidebarIndex, w))
	}
	return out
}
func (m *model) listTitle() string {
	if m.tab == 1 {
		return fmt.Sprintf("2 Groups · %d", len(m.groups()))
	}
	return fmt.Sprintf("1 Tasks · %d", len(m.rows()))
}
func (m *model) listLines(w, h int) []string {
	if h <= 0 {
		return nil
	}
	v := m.currentView()
	if m.tab == 1 {
		rows := m.groups()
		if len(rows) == 0 {
			return []string{"No groups in this scope.", "Use : Create group or change scope."}
		}
		start := max(0, v.Index-h+1)
		var out []string
		for i := start; i < len(rows) && len(out) < h; i++ {
			r := rows[i]
			s := core.Summarize(m.states[r.Connection.ID].Snapshot, r.Group.Name, time.Now())
			slots := fmt.Sprint(r.Group.Parallel)
			if r.Group.Parallel == 0 {
				slots = "∞"
			}
			text := fmt.Sprintf("%s / %s  %d/%d %.0f%% %s slots %s", r.Connection.DisplayName(), r.Group.Name, s.Finished, s.Total, s.Progress*100, strings.Repeat("=", clamp(int(s.Progress*8), 0, 8))+strings.Repeat("-", 8-clamp(int(s.Progress*8), 0, 8)), slots)
			out = append(out, selectedLine(text, i == v.Index, w))
		}
		return out
	}
	rows := m.rows()
	if len(rows) == 0 {
		if v.Query != "" || v.State != "" {
			return []string{"No tasks match this filter.", "Esc clear · / change filter · n add"}
		}
		var errs []string
		loading := false
		for _, c := range m.cfg.Connections {
			if m.scope != "all" && m.scope != c.ID {
				continue
			}
			s := m.states[c.ID]
			if s == nil {
				continue
			}
			if s.Pending {
				loading = true
			}
			if s.Err != nil {
				errs = append(errs, clean(c.DisplayName()+": "+s.Err.Error()))
			}
		}
		if len(errs) > 0 {
			return append([]string{"Unable to read queue:"}, append(errs, "r retry · t manage connections")...)
		}
		if loading {
			return []string{"Loading queues…", "Navigation remains available."}
		}
		return []string{"No tasks yet.", "n Add a task · t Connections"}
	}
	all, index := m.taskDisplayRows(w)
	start := max(0, index-h+1)
	var lines []string
	for _, row := range all[min(start, len(all)):min(len(all), start+h)] {
		lines = append(lines, row.Text)
	}
	return lines
}

func (m *model) detailLines(w, h int) []string {
	if w <= 0 || h <= 0 {
		return nil
	}
	var lines []string
	if g, ok := m.selectedGroup(); ok {
		s := core.Summarize(m.states[g.Connection.ID].Snapshot, g.Group.Name, time.Now())
		slots := fmt.Sprint(g.Group.Parallel)
		if g.Group.Parallel == 0 {
			slots = "unlimited"
		}
		eta := "—"
		if s.ETA != nil {
			eta = "~" + duration(*s.ETA)
		}
		barWidth := max(1, min(24, w-8))
		filled := int(s.Progress * float64(barWidth))
		lines = []string{g.Connection.DisplayName() + " / " + g.Group.Name, "State: " + g.Group.Status, "Parallel: " + slots, "", fmt.Sprintf("%s%s %d%%", strings.Repeat("█", filled), strings.Repeat("░", barWidth-filled), int(s.Progress*100)), fmt.Sprintf("Finished %d / %d (includes failures)", s.Finished, s.Total), fmt.Sprintf("Running %d · queued %d", s.Running, s.Queued), fmt.Sprintf("Succeeded %d · failed %d", s.Succeeded, s.Failed), "Average: " + duration(s.AvgDuration), "Elapsed: " + duration(s.Elapsed), "Estimated remaining: " + eta, fmt.Sprintf("Failed IDs: %v", s.FailedIDs), "", "Enter: view this group's tasks", ": group actions"}
	} else if r, ok := m.task(); ok {
		t := r.Task
		if h < 12 && m.focus != 2 {
			preview := strings.Split(cleanLog(m.previewText), "\n")
			if m.previewPending {
				preview = []string{"Loading log…"}
			} else if m.previewError != "" {
				preview = []string{"Log unavailable: " + m.previewError}
			}
			head := []string{fmt.Sprintf("%s / #%d · %s · %s", r.Connection.DisplayName(), t.ID, t.State(), t.Group), "Command: " + t.Command, "Directory: " + t.Path, "── LOG TAIL · Enter expand · Tab metadata ──"}
			if s := m.states[r.Connection.ID]; s != nil && s.Err != nil {
				head[2] = "STALE: " + s.Err.Error()
			}
			count := max(1, h-len(head))
			start := max(0, len(preview)-count)
			for _, line := range append(head, preview[start:]...) {
				lines = append(lines, fit(clean(line), w))
			}
			return lines[:min(len(lines), h)]
		}
		lines = []string{fmt.Sprintf("%s / #%d · %s", r.Connection.DisplayName(), t.ID, t.State()), "Label: " + t.Label, "Group: " + t.Group, "Directory: " + t.Path, "Command: " + t.Command, fmt.Sprintf("Priority: %d", t.Priority), fmt.Sprintf("Depends on: %v", t.Dependencies), "Created: " + t.CreatedAt.Format(time.RFC3339), "Duration: " + taskDuration(t)}
		if t.ScheduledAt != nil {
			lines = append(lines, "Scheduled: "+t.ScheduledAt.Format(time.RFC3339))
		}
		if t.Result != "" {
			lines = append(lines, "Result: "+t.Result)
		}
		if t.ExitCode != nil {
			lines = append(lines, fmt.Sprintf("Exit code: %d", *t.ExitCode))
		}
		if t.Error != "" {
			lines = append(lines, "Error: "+t.Error)
		}
		if s := m.states[r.Connection.ID]; s != nil {
			if s.Err != nil {
				lines = append(lines, "STALE: "+s.Err.Error())
			}
			if !s.Snapshot.ObservedAt.IsZero() {
				lines = append(lines, "Observed "+duration(time.Since(s.Snapshot.ObservedAt))+" ago")
			}
		}
		lines = append(lines, "", "── LOG PREVIEW · Enter expand ──")
		if m.previewPending {
			lines = append(lines, "Loading log…")
		} else if m.previewError != "" {
			lines = append(lines, "Log unavailable: "+m.previewError)
		} else {
			lines = append(lines, strings.Split(cleanLog(m.previewText), "\n")...)
		}
	} else {
		lines = []string{"Select a task or group.", "n Add a task", ": Actions", "t Connections"}
	}
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, strings.Split(ansi.Wrap(clean(line), w, ""), "\n")...)
	}
	start := min(m.detailOffset, max(0, len(wrapped)-1))
	return wrapped[start:min(len(wrapped), start+h)]
}
func (m *model) logView(w, h int) string {
	title := fmt.Sprintf("%s / #%d · %s", m.log.Connection.DisplayName(), m.log.Task.ID, m.log.Task.State())
	return frame(title, m.sessionLines(m.log, w-2, h-2), w, h, true)
}

func (m *model) overlayView(w, h int) string {
	var lines []string
	title := ""
	switch m.overlay {
	case "log-settings":
		title = "Log collection mode / frequency"
		all := m.logSettingsLines(w - 2)
		start := max(0, m.menuIndex-max(1, h-5)+1)
		lines = append(lines, all[:min(3, len(all))]...)
		if len(all) > 3 {
			lines = append(lines, all[min(len(all), 3+start):]...)
		}
	case "upgrade-loading":
		title = "Check upgrade"
		lines = []string{"Inspecting versions, installation ownership and target…", "Esc closes this check; no upgrade has started."}
	case "upgrade-review":
		title = "Review upgrade · default No"
		if m.upgrade != nil {
			lines = m.upgrade.Lines
		}
		lines = append(lines, "", "y apply · Enter / n / Esc cancel")
	case "upgrade-running":
		title = "Upgrade in progress"
		lines = []string{"The reviewed operation is running.", "Esc returns to the dashboard; progress remains in status.", "Ctrl+C cancels waiting; completed steps are not rolled back."}
	case "actions":
		title = "Actions"
		lines = append(lines, m.input.View(), "")
		items := m.filteredActions()
		start := max(0, m.menuIndex-max(1, h-5)+1)
		for i := start; i < len(items) && len(lines) < h-2; i++ {
			a := items[i]
			s := a.Label
			if a.Key != "" {
				s += "  [" + a.Key + "]"
			}
			lines = append(lines, selectedLine(s, i == m.menuIndex, w-2))
		}
		if len(items) == 0 {
			lines = append(lines, "No applicable actions match.")
		}
	case "confirm":
		title = "Review action"
		if m.confirm != nil {
			a := m.confirm
			lines = []string{a.Title, "Connection: " + a.Connection.DisplayName() + " (" + a.Connection.ID + ")"}
			if a.Request.Group != "" {
				lines = append(lines, "Group: "+a.Request.Group)
			}
			if len(a.Request.IDs) > 0 {
				lines = append(lines, fmt.Sprintf("Task IDs (%d): %v", len(a.Request.IDs), a.Request.IDs))
			}
			if a.Request.Operation == "parallel" {
				lines = append(lines, fmt.Sprintf("Parallel tasks: %d", a.Request.Parallel))
			}
			lines = append(lines, "", a.Consequence, "", "y apply · Enter / n / Esc cancel (default No)")
		}
	case "connections":
		title = "Connections · a add · e edit · t test · A authenticate · d remove"
		all := []string{"All connections"}
		for _, c := range m.cfg.Connections {
			s := c.DisplayName() + " [" + c.Kind + "]"
			st := m.states[c.ID]
			if st != nil {
				if st.Pending {
					s += " · testing…"
				} else if st.Err != nil {
					s += " · " + st.Err.Error()
				} else if !st.Snapshot.ObservedAt.IsZero() {
					s += " · connected"
				}
			}
			all = append(all, s)
		}
		start := max(0, m.menuIndex-max(1, h-3)+1)
		for i := start; i < len(all) && len(lines) < h-2; i++ {
			lines = append(lines, selectedLine(all[i], i == m.menuIndex, w-2))
		}
	case "status":
		title = "Task status"
		states := []string{"All statuses", "Running", "Queued", "Paused", "Stashed", "Failed", "Succeeded", "Locked"}
		for i, s := range states {
			lines = append(lines, selectedLine(s, i == m.menuIndex, w-2))
		}
	case "events":
		title = "Recent operation results"
		if len(m.events) == 0 {
			lines = []string{"No operations yet."}
		} else {
			for i := len(m.events) - 1; i >= 0; i-- {
				lines = append(lines, clean(m.events[i]))
			}
		}
	case "task-info":
		title = "Task details · snapshot at opening"
		lines = m.taskInfoLines()
	case "help":
		title = "Help · Esc returns to the same selection"
		logs := []string{"LOGS / MONITOR", "PgUp/PgDn · Ctrl+U/D    Full / half-page scroll", "PgUp at the top    Load older tail (up to 10,000 lines)", "L    Live / Polling / Manual; custom polling interval", "Space    Pause / resume collection; r refresh once", "f    Start live / toggle autoscroll; G return to tail", "/    Search; n/N next/previous match", "y    Copy loaded plain log; Y copy failure report", "o    Open complete log in an external pager", "i    Task metadata and detected progress source", "e    Edit; R restart as new; I restart in place (review)", "Esc    Return; ? help; m toggle mouse", "", "MONITOR", "Space on task rows, then W    Add selected tasks", "Tab/Shift+Tab    Focus tile; [/] previous/next page", "Enter    Expand tile; a add watches; x remove watch", "Polling replaces the tail; scrolling/search freezes it.", "G resumes polling; collection pause is independent.", "Hidden pages stop log reads. Watch preferences persist."}
		lines = []string{"NAVIGATION", "↑↓ or j/k    Select / scroll", "Tab / Shift+Tab or h/l    Move pane focus", "Home / gg    First row; End / G    Last row", "0 Scope · 1 Tasks · 2 Groups · 3 Detail/log · 4 Monitor", "Enter group    Show its tasks", "/    Live filter; Enter accepts; Esc clears", "Space    Select tasks on one connection; Esc clears", "", "CURRENT ACTIONS"}
		for _, a := range m.actions() {
			if a.Key != "" {
				lines = append(lines, fmt.Sprintf("%-10s %s", a.Key, a.Label))
			}
		}
		lines = append(lines, ":          Search all applicable actions", "", "FORMS", "Ctrl+S    Review, then submit; Esc Back; Ctrl+C cancel", "Edit and destructive reviews: y apply; Enter/n/Esc No", "Ctrl+O    Advanced fields; Ctrl+G group picker", "Ctrl+D    Dependency picker; Ctrl+L connection picker", "Ctrl+T    Test a connection draft without saving", "", "Scope and refresh preserve task identity.", "Stale snapshots keep their own error and observation time.", "Dependencies require every parent to succeed.", "Queue progress counts retained finished tasks, including failures.", "Job progress is shown only when detected in task output.")
		if m.log.Open || m.monitor || m.focus == 2 {
			lines = append(append(logs, ""), lines...)
		} else {
			lines = append(append(lines, ""), logs...)
		}
	}
	if m.overlay == "confirm" || m.overlay == "events" || m.overlay == "help" || m.overlay == "task-info" || m.overlay == "upgrade-review" {
		var wrapped []string
		for _, line := range lines {
			wrapped = append(wrapped, strings.Split(ansi.Wrap(clean(line), max(1, w-2), ""), "\n")...)
		}
		start := 0
		start = clamp(m.menuIndex, 0, max(0, len(wrapped)-1))
		lines = wrapped[start:]
	}
	return frame(title, lines, w, h, true)
}
func (m *model) footer() string {
	if m.formUnknown {
		return "Outcome unknown · Esc returns without another submission"
	}
	if m.formPending {
		return "Applying · Ctrl+C cancels waiting; accepted effects remain"
	}
	if m.editForm != nil {
		return "Ctrl+S review · y apply / Enter No · Ctrl+O options · Esc back"
	}
	if m.taskForm != nil || m.connectionForm != nil {
		return "Ctrl+S review / submit · Ctrl+O advanced · Esc back · Ctrl+C cancel"
	}
	if m.inputMode != "" {
		return "Enter accept · Esc cancel · printable keys type text"
	}
	var buttons []string
	for _, b := range m.footerButtons() {
		buttons = append(buttons, "["+b.Label+"]")
	}
	suffix := " · q quit · m mouse"
	if m.overlay == "confirm" || m.overlay == "upgrade-review" {
		suffix = " · y Yes · Enter/n/Esc No"
	} else if m.log.Open {
		suffix = " · PgUp/Dn · Ctrl+U/D · / search · G tail"
	} else if m.monitor {
		suffix = " · [/] page · Tab tile · L mode"
	} else if m.overlay != "" {
		suffix = " · ↑↓ select · Esc back"
	}
	return strings.Join(buttons, " ") + suffix
}
