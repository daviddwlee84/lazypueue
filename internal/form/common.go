// Package form contains hosted forms. A host owns the terminal, persistence and
// mutation; forms only collect and review drafts or perform read-only tests.
package form

import (
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func input(value string) textinput.Model {
	m := textinput.New()
	m.Prompt = "> "
	m.CharLimit = 8192
	m.SetWidth(68)
	m.SetValue(value)
	return m
}
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}
func fit(s string, w int) string { return ansi.Truncate(s, max(0, w), "…") }
func wrapLines(lines []string, width int) []string {
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, strings.Split(ansi.Hardwrap(safe(line), max(1, width), false), "\n")...)
	}
	return wrapped
}
func render(title string, body []string, focus, w, h int, status, footer string) string {
	w, h = max(1, w), max(1, h)
	if h < 4 {
		return strings.Join([]string{fit(title, w), fit("Ctrl+C cancel", w)}[:min(2, h)], "\n")
	}
	capacity := h - 3
	start := max(0, min(focus-capacity/2, len(body)-capacity))
	end := min(len(body), start+capacity)
	lines := []string{fit(safe(title), w)}
	for _, line := range body[start:end] {
		lines = append(lines, fit(line, w))
	}
	for len(lines) < h-2 {
		lines = append(lines, "")
	}
	lines = append(lines, fit(safe(status), w), fit(footer, w))
	return strings.Join(lines, "\n")
}

type choice struct{ id, label string }
type picker struct {
	kind     string
	choices  []choice
	query    textinput.Model
	index    int
	selected map[string]bool
}

func newPicker(kind string, choices []choice) *picker {
	return &picker{kind: kind, choices: choices, query: input(""), selected: map[string]bool{}}
}
func (p *picker) visible() []choice {
	var out []choice
	q := strings.ToLower(p.query.Value())
	for _, c := range p.choices {
		if strings.Contains(strings.ToLower(c.label), q) {
			out = append(out, c)
		}
	}
	return out
}
func (p *picker) update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "up":
			p.index = max(0, p.index-1)
			return nil
		case "down":
			p.index = min(max(0, len(p.visible())-1), p.index+1)
			return nil
		}
	}
	before := p.query.Value()
	var cmd tea.Cmd
	p.query, cmd = p.query.Update(msg)
	if p.query.Value() != before {
		p.index = 0
	}
	return cmd
}
func (p *picker) view(w, h int) string {
	body := []string{"Filter: " + p.query.View()}
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
		body = append(body, mark+safe(c.label))
	}
	if len(body) == 1 {
		body = append(body, "No matching choices")
	}
	foot := "↑↓ choose · Enter select · Esc back"
	if p.kind == "dependencies" {
		foot = "↑↓ choose · Enter toggle · Ctrl+S accept · Esc back"
	}
	return render("Choose "+p.kind, body, p.index+1, w, h, "Type to filter; j/k remain text", foot)
}
