package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazypueue/internal/core"
)

type logState struct {
	Open, Following, AutoScroll, Done bool
	Connection                        core.Connection
	Task                              core.Task
	Key                               string
	Generation                        uint64
	Text, Err, Query                  string
	Offset                            int
	Cancel                            context.CancelFunc
	Channel                           <-chan logChunkMsg
	Context                           context.Context
}
type logChunkMsg struct {
	Key        string
	Generation uint64
	Text       string
	Done       bool
	Err        error
}

func cleanLog(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}
func (m *model) stopLog() {
	if m.log.Cancel != nil {
		m.log.Cancel()
	}
	m.log.Open = false
	m.log.Generation++
	m.log.Cancel = nil
}
func (m *model) openLog(r taskRow, follow bool) tea.Cmd {
	m.stopLog()
	ctx, cancel := context.WithCancel(m.ctx)
	gen := m.log.Generation + 1
	m.log = logState{Open: true, Following: follow, AutoScroll: true, Connection: r.Connection, Task: r.Task, Key: r.key(), Generation: gen, Cancel: cancel, Context: ctx}
	if !follow {
		return func() tea.Msg {
			text, err := m.backend.Log(ctx, r.Connection, r.Task.ID, 200)
			return logChunkMsg{r.key(), gen, text, true, err}
		}
	}
	ch := make(chan logChunkMsg, 16)
	m.log.Channel = ch
	return func() tea.Msg {
		go func() {
			defer close(ch)
			err := m.backend.Follow(ctx, r.Connection, r.Task.ID, 200, func(s string) {
				select {
				case ch <- logChunkMsg{Key: r.key(), Generation: gen, Text: s}:
				case <-ctx.Done():
				}
			})
			select {
			case ch <- logChunkMsg{Key: r.key(), Generation: gen, Done: true, Err: err}:
			case <-ctx.Done():
			}
		}()
		select {
		case v := <-ch:
			return v
		case <-ctx.Done():
			return nil
		}
	}
}
func (m *model) acceptLog(msg logChunkMsg) tea.Cmd {
	if !m.log.Open || m.log.Key != msg.Key || m.log.Generation != msg.Generation {
		return nil
	}
	// Sanitize the combined buffer so split UTF-8/control sequences are never
	// sent to the terminal. The raw buffer remains bounded independently.
	m.log.Text += msg.Text
	if len(m.log.Text) > 2*1024*1024 {
		m.log.Text = m.log.Text[len(m.log.Text)-2*1024*1024:]
		m.log.Text = strings.ToValidUTF8(m.log.Text, "")
	}
	if n := strings.Count(m.log.Text, "\n"); n > 10000 {
		skip := n - 10000
		start := 0
		for skip > 0 {
			p := strings.IndexByte(m.log.Text[start:], '\n')
			if p < 0 {
				break
			}
			start += p + 1
			skip--
		}
		m.log.Text = m.log.Text[start:]
	}
	if msg.Err != nil && msg.Err != context.Canceled {
		m.log.Err = msg.Err.Error()
	}
	if msg.Done {
		m.log.Done = true
		return nil
	}
	ch, ctx := m.log.Channel, m.log.Context
	return func() tea.Msg {
		select {
		case v, ok := <-ch:
			if !ok {
				return nil
			}
			return v
		case <-ctx.Done():
			return nil
		}
	}
}
func (m *model) logLines() []string { return strings.Split(cleanLog(m.log.Text), "\n") }
func (m *model) logKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	lines := m.logLines()
	capacity := max(1, m.height-7)
	last := max(0, len(lines)-capacity)
	if m.log.AutoScroll {
		m.log.Offset = last
	}
	switch key {
	case "esc", "q", "ctrl+c":
		m.stopLog()
		return m.preview()
	case "up", "k":
		m.log.AutoScroll = false
		m.log.Offset = max(0, m.log.Offset-1)
	case "down", "j":
		m.log.AutoScroll = false
		m.log.Offset = min(last, m.log.Offset+1)
	case "pgup":
		m.log.AutoScroll = false
		m.log.Offset = max(0, m.log.Offset-capacity)
	case "pgdown":
		m.log.AutoScroll = false
		m.log.Offset = min(last, m.log.Offset+capacity)
	case "home", "g":
		m.log.AutoScroll = false
		m.log.Offset = 0
	case "end", "G":
		m.log.AutoScroll = true
		m.log.Offset = last
	case "f", "F":
		if !m.log.Following || m.log.Done {
			return m.openLog(taskRow{m.log.Connection, m.log.Task}, true)
		}
		m.log.AutoScroll = !m.log.AutoScroll
	case "/":
		m.inputMode = "log-search"
		m.input.Prompt = "Search log: "
		m.input.SetValue(m.log.Query)
		return m.input.Focus()
	case "n":
		m.findLog(1)
	case "N":
		m.findLog(-1)
	case "y":
		m.note("Copied visible log buffer.")
		return tea.SetClipboard(cleanLog(m.log.Text))
	case "r":
		return m.openLog(taskRow{m.log.Connection, m.log.Task}, m.log.Following)
	}
	return nil
}
func (m *model) findLog(direction int) {
	if m.log.Query == "" {
		return
	}
	lines := m.logLines()
	for n := 1; n <= len(lines); n++ {
		i := (m.log.Offset + direction*n) % len(lines)
		if i < 0 {
			i += len(lines)
		}
		if strings.Contains(strings.ToLower(lines[i]), strings.ToLower(m.log.Query)) {
			m.log.AutoScroll = false
			m.log.Offset = i
			m.status = fmt.Sprintf("Log match at line %d", i+1)
			return
		}
	}
	m.status = "No log matches."
}
