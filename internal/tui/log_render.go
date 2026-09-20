package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/logview"
)

func progressBar(percent float64, width int) string {
	width = max(1, width)
	filled := clamp(int(percent*float64(width)/100), 0, width)
	return color(strings.Repeat("█", filled), "42") + color(strings.Repeat("░", width-filled), "245")
}
func (m *model) sessionLines(s *logState, w, h int) []string {
	if h <= 0 {
		return nil
	}
	if s == nil {
		return []string{"Select a task to inspect its output."}
	}
	meta := m.logModeLabel(s) + " · " + m.logFreshness(s)
	out := []string{clean(meta)}
	if s.HasProgress {
		out = append(out, fmt.Sprintf("%s %.1f%%", progressBar(s.Progress.Percent, min(18, max(1, w-10))), s.Progress.Percent))
	}
	capacity := max(0, h-len(out))
	lines := strings.Split(s.Text, "\n")
	if s.Buffer != nil {
		lines = s.Buffer.StyledLines()
	}
	start := clamp(s.Offset, 0, max(0, len(lines)-1))
	if s.AutoScroll {
		start = max(0, len(lines)-capacity)
	}
	if s.Text == "" {
		text := "No output yet."
		if !hasLog(s.Task) {
			text = "Waiting for task output; not started or no log file."
		}
		if s.Pending {
			text = "Loading log…"
		}
		return append(out, text)
	}
	for _, line := range lines[start:min(len(lines), start+capacity)] {
		out = append(out, logview.RenderStyledLine(line, s.Query, os.Getenv("NO_COLOR") != ""))
	}
	return out
}
func (m *model) monitorView(w, h int) string {
	if len(m.watches) == 0 {
		return frame("4 Monitor", []string{"No watched tasks.", "Select tasks with Space, then W / Monitor selected.", "Repeat on another connection to add cross-host watches.", "Each tile can use Live, Polling or Manual (L)."}, w, h, true)
	}
	start, end := m.monitorRange()
	rects := m.monitorRects()
	var panes []string
	for i := start; i < end; i++ {
		wstate := m.watches[i]
		r := rects[i-start]
		task, ok := m.watchRow(wstate)
		title := fmt.Sprintf("%s #%d", wstate.Connection, wstate.TaskID)
		var lines []string
		if !ok {
			lines = []string{"Task unavailable, removed, or its ID was reused.", "x removes this watch."}
		} else {
			title = task.Connection.DisplayName() + fmt.Sprintf(" #%d · ", task.Task.ID) + task.Task.State()
			s := m.sessions[task.key()]
			lines = m.sessionLines(s, r.W-2, r.H-2)
		}
		panes = append(panes, frame(title, lines, r.W, r.H, i == m.monitorIndex))
	}
	if m.monitorCapacity() == 4 {
		left := w / 2
		top := h / 2
		for len(panes) < 4 {
			index := len(panes)
			pw := left
			if index%2 == 1 {
				pw = w - left
			}
			ph := top
			if index >= 2 {
				ph = h - top
			}
			panes = append(panes, frame("", nil, pw, ph, false))
		}
		return sideBySide(panes[0], panes[1]) + "\n" + sideBySide(panes[2], panes[3])
	}
	if m.monitorCapacity() == 2 {
		if len(panes) == 1 {
			panes = append(panes, frame("", nil, w, h-h/2, false))
		}
		return panes[0] + "\n" + panes[1]
	}
	return panes[0]
}
func (m *model) activeDetailLines(w, h int) []string {
	if m.tab == 1 {
		return m.detailLines(w, h)
	}
	r, ok := m.task()
	if !ok {
		return []string{"Select a task.", "n Add · W Monitor selected"}
	}
	head := []string{fmt.Sprintf("%s #%d · %s · %s · i info", r.Connection.DisplayName(), r.Task.ID, r.Task.State(), taskDuration(r.Task)), "Command: " + clean(r.Task.Command)}
	if h > 10 {
		head = append(head, "Directory: "+clean(r.Task.Path), fmt.Sprintf("Group: %s · dependencies %v", r.Task.Group, r.Task.Dependencies))
		if r.Task.Failed() {
			head = append(head, "Result: "+r.Task.Result+" "+r.Task.Error+" · Y failure report")
		}
	}
	if s := m.states[r.Connection.ID]; s != nil && s.Err != nil {
		head = append(head, "Queue stale: "+clean(s.Err.Error()))
	}
	s := m.sessions[r.key()]
	return append(head, m.sessionLines(s, w, max(0, h-len(head)))...)
}
func (m *model) taskInfoLines() []string {
	if m.infoTask == nil {
		return []string{"No task selected."}
	}
	r := *m.infoTask
	t := r.Task
	lines := []string{fmt.Sprintf("%s / #%d · %s", r.Connection.DisplayName(), t.ID, t.State()), "Label: " + t.Label, "Group: " + t.Group, "Directory: " + t.Path, "Original command: " + t.OriginalCommand, "Command: " + t.Command, fmt.Sprintf("Priority: %d", t.Priority), fmt.Sprintf("Dependencies: %v", t.Dependencies), "Created: " + t.CreatedAt.Format(time.RFC3339)}
	for _, item := range []struct {
		name string
		at   *time.Time
	}{{"Enqueued", t.EnqueuedAt}, {"Scheduled", t.ScheduledAt}, {"Started", t.StartedAt}, {"Ended", t.EndedAt}} {
		if item.at != nil {
			lines = append(lines, item.name+": "+item.at.Format(time.RFC3339))
		}
	}
	lines = append(lines, "Duration: "+taskDuration(t))
	if t.Result != "" {
		lines = append(lines, "Result: "+t.Result)
	}
	if t.ExitCode != nil {
		lines = append(lines, fmt.Sprintf("Exit code: %d", *t.ExitCode))
	}
	if t.Error != "" {
		lines = append(lines, "Error: "+t.Error)
	}
	if s := m.sessions[r.key()]; s != nil && s.HasProgress {
		lines = append(lines, "", fmt.Sprintf("Detected progress: %.1f%%", s.Progress.Percent), "Source: "+s.Progress.Source, "Observed: "+s.Progress.ObservedAt.Format(time.RFC3339))
	}
	return lines
}
func (m *model) logSettingsLines(w int) []string {
	s := m.sessions[m.logSettingsKey]
	if s == nil {
		return []string{"No task selected."}
	}
	labels := []string{"Live follow", "Polling snapshots", "Manual only", "Poll every 2 seconds", "Poll every 5 seconds", "Poll every 10 seconds", "Poll every 30 seconds", "Poll every minute", "Custom interval…"}
	lines := []string{fmt.Sprintf("%s #%d · %s", s.Connection.DisplayName(), s.Task.ID, m.logModeLabel(s)), "Settings apply to this task; zooming keeps the same mode.", ""}
	for i, label := range labels {
		lines = append(lines, selectedLine(label, i == m.menuIndex, w))
	}
	return lines
}
func (m *model) updateSessionTasks(id string) {
	st := m.states[id]
	if st == nil {
		return
	}
	for _, s := range m.sessions {
		if s.Connection.ID != id {
			continue
		}
		found := false
		for _, t := range st.Snapshot.Tasks {
			if t.Identity() == s.Task.Identity() {
				found = true
				old := s.Task
				s.Task = t
				if t.AttemptIdentity() != s.Attempt {
					m.stopSession(s)
					s.Attempt = t.AttemptIdentity()
					s.Buffer.Replace("")
					s.Text = ""
					s.HasProgress = false
					s.Final = false
					s.Done = false
					s.NextDue = time.Time{}
					s.UpdatedAt = time.Time{}
				} else if !old.Terminal() && t.Terminal() && !s.LiveStarted {
					s.Final = s.Mode == "live" && s.Done && s.Err == ""
					if !s.Final {
						s.NextDue = time.Time{}
					}
				}
				break
			}
		}
		if !found {
			m.stopSession(s)
			s.Err = "Task removed or ID reused"
		}
	}
}

func summaryProgressBar(s core.Summary, width int) string {
	if s.Total == 0 {
		return strings.Repeat("░", width)
	}
	ok := clamp(s.Succeeded*width/s.Total, 0, width)
	failed := clamp(s.Failed*width/s.Total, 0, width-ok)
	return color(strings.Repeat("█", ok), "42") + color(strings.Repeat("█", failed), "203") + color(strings.Repeat("░", width-ok-failed), "245")
}
